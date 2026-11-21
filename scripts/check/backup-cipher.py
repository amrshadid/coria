#!/usr/bin/env python3
"""Check CORIA's backup file format against Python's standard library and
the cryptography package.

A backup that only the code that wrote it can open proves nothing about the
format. This script implements the format from its description in
internal/secret/doc.go, with HKDF written out from RFC 5869 over hmac and
hashlib, AES-GCM from the cryptography package (OpenSSL), and nothing of
CORIA's:

    8 bytes   "CORIA-BK"
    1 byte    format, 0x01
    4 bytes   key version, big-endian
    4 bytes   chunk size S, big-endian
    32 bytes  salt
    chunks:   ciphertext of S bytes (the last: 0 to S) || 16-byte tag

    backup key = HKDF-SHA-256(key of that version, no salt, "coria/backup/v1")
    file key   = HKDF-SHA-256(backup key, salt, "coria/backup/v1/file")
    nonce_i    = i (8 bytes big-endian) || 00 00 00 || last
    AD_i       = header (49 bytes) || i (8 bytes big-endian) || last

The Go test TestBackupOpensInPython (internal/secret/outside_test.go) runs it
with `make check-backup-cipher`:

    backup-cipher.py open <key file> <backup by Go> <hex sha256 of plaintext> <out>

It opens the file Go wrote and compares the plaintext's SHA-256, checks that a
cut at a chunk boundary, two chunks swapped, a flipped bit and an appended
chunk each fail, then writes the same plaintext as a new backup at <out> for
Go to open. Any disagreement exits with status 1 and a reason on standard
error.

    backup-cipher.py make <key file> <version> <chunk size> <hex plaintext> <out>

writes one backup, for a test that needs a fixed file made outside Go.
"""

import base64
import binascii
import hashlib
import hmac
import os
import struct
import sys

from cryptography.exceptions import InvalidTag
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

MAGIC = b"CORIA-BK"
FORMAT = 0x01
HEADER = 49
TAG = 16
PREFIX = "coria-key-v1"


class Refused(Exception):
    """The file does not open."""


def fail(reason):
    print("check-backup-cipher: " + reason, file=sys.stderr)
    sys.exit(1)


def read_keys(path):
    """Read a key file: one "coria-key-v1 <version> <base64 key>" per line."""
    keys = {}
    with open(path, "rb") as f:
        text = f.read().decode("ascii")
    for number, line in enumerate(text.splitlines(), 1):
        fields = line.split(" ")
        if len(fields) != 3 or fields[0] != PREFIX:
            fail("key file line %d is not in the documented format" % number)
        key = base64.b64decode(fields[2], validate=True)
        if len(key) != 32:
            fail("key file line %d does not hold a 32-byte key" % number)
        keys[int(fields[1])] = key
    return keys


def hkdf(ikm, salt, info):
    """HKDF-SHA-256 with 32 bytes of output (RFC 5869 2.2, 2.3)."""
    prk = hmac.new(salt if salt else b"\x00" * 32, ikm, hashlib.sha256).digest()
    return hmac.new(prk, info + b"\x01", hashlib.sha256).digest()


def file_key(keys, header):
    version = struct.unpack(">I", header[9:13])[0]
    if version not in keys:
        raise Refused("key version %d is not in the key file" % version)
    backup = hkdf(keys[version], None, b"coria/backup/v1")
    return hkdf(backup, header[17:HEADER], b"coria/backup/v1/file")


def nonce_ad(header, index, last):
    flag = b"\x01" if last else b"\x00"
    return struct.pack(">Q", index) + b"\x00\x00\x00" + flag, header + struct.pack(">Q", index) + flag


def open_backup(keys, data):
    if len(data) < HEADER or data[:8] != MAGIC or data[8] != FORMAT:
        raise Refused("not a backup")
    header = data[:HEADER]
    size = struct.unpack(">I", header[13:17])[0]
    if not 1 <= size <= 1 << 20:
        raise Refused("chunk size")
    aead = AESGCM(file_key(keys, header))
    out = []
    pos, index = HEADER, 0
    while True:
        chunk = data[pos:pos + size + TAG]
        if len(chunk) == 0:
            raise Refused("the last chunk is missing")
        pos += len(chunk)
        last = pos == len(data)
        if len(chunk) < TAG:
            raise Refused("a chunk shorter than its tag")
        nonce, ad = nonce_ad(header, index, last)
        try:
            out.append(aead.decrypt(nonce, chunk, ad))
        except InvalidTag:
            raise Refused("chunk %d does not open" % index)
        if last:
            return b"".join(out)
        index += 1


def make_backup(keys, version, size, plaintext):
    header = MAGIC + bytes([FORMAT]) + struct.pack(">II", version, size) + os.urandom(32)
    aead = AESGCM(file_key(keys, header))
    pieces = [plaintext[i:i + size] for i in range(0, len(plaintext), size)] or [b""]
    out = [header]
    for index, piece in enumerate(pieces):
        nonce, ad = nonce_ad(header, index, index == len(pieces) - 1)
        out.append(aead.encrypt(nonce, piece, ad))
    return b"".join(out)


def expect_refused(keys, data, what):
    try:
        open_backup(keys, data)
    except Refused:
        return
    fail("the backup Go wrote still opens in Python with " + what)


def check_open(argv):
    keys = read_keys(argv[2])
    with open(argv[3], "rb") as f:
        data = f.read()
    want = argv[4]
    try:
        plaintext = open_backup(keys, data)
    except Refused as e:
        fail("the backup Go wrote does not open in Python: %s" % e)
    if hashlib.sha256(plaintext).hexdigest() != want:
        fail("the backup Go wrote opens in Python to a different plaintext")
    size = struct.unpack(">I", data[13:17])[0]
    chunks = (len(data) - HEADER + size + TAG - 1) // (size + TAG)
    print("opened the %d-byte backup Go wrote: %d chunks of %d, %d bytes of plaintext"
          % (len(data), chunks, size, len(plaintext)))
    if chunks < 3:
        fail("the backup Go wrote has fewer than 3 chunks, too few to check reordering")

    span = size + TAG
    c0, c1 = data[HEADER:HEADER + span], data[HEADER + span:HEADER + 2 * span]
    expect_refused(keys, data[:HEADER + span], "a cut after chunk 0")
    expect_refused(keys, data[:HEADER] + c1 + c0 + data[HEADER + 2 * span:], "chunks 0 and 1 swapped")
    flipped = bytearray(data)
    flipped[HEADER + span + 3] ^= 0x01
    expect_refused(keys, bytes(flipped), "a bit flipped in chunk 1")
    expect_refused(keys, data + c0, "chunk 0 appended")
    print("refused a cut at a chunk boundary, two chunks swapped, a flipped bit and an appended chunk")

    version = struct.unpack(">I", data[9:13])[0]
    written = make_backup(keys, version, size, plaintext)
    with open(argv[5], "wb") as f:
        f.write(written)
    print("wrote a %d-byte backup with key version %d" % (len(written), version))


def main(argv):
    if len(argv) == 6 and argv[1] == "open":
        check_open(argv)
    elif len(argv) == 7 and argv[1] == "make":
        keys = read_keys(argv[2])
        data = make_backup(keys, int(argv[3]), int(argv[4]), binascii.unhexlify(argv[5]))
        with open(argv[6], "wb") as f:
            f.write(data)
    else:
        fail("usage: backup-cipher.py open <key file> <backup> <hex sha256> <out> | "
             "make <key file> <version> <chunk size> <hex plaintext> <out>")


if __name__ == "__main__":
    main(sys.argv)
