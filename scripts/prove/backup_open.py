#!/usr/bin/env python3
"""Open a CORIA backup file outside CORIA: with the installation's key file,
Python's hmac and hashlib (HKDF, RFC 5869) and the cryptography package's
AES-256-GCM, checking every chunk, and refusing a changed file.

    backup_open.py BACKUP --key-file KEYFILE [--out PLAINTEXT] [--expect-sha256 HEX]
    backup_open.py --self-test

The file is the one internal/secret/doc.go describes (chunked AES-256-GCM, the
nonce a counter with a last-chunk flag, the header in every chunk's
authenticated data), so a file cut at any byte, with chunks swapped, dropped or
added, or with a bit changed, does not open. What it holds, once opened, is the
backup's own plaintext; this tool decrypts and authenticates, and says the
plaintext's size and SHA-256 to compare with another tool's.

With --out the plaintext is written only after the final chunk opened as the
final one: it goes to a staging file beside OUT (mode 0600) that is renamed over
OUT at the end and removed on any error, so a refused file leaves nothing
behind, as a restore must. Without --out nothing is written; the file is only
checked. Chunks are checked one at a time, but bytes of a chunk that opened
belong to a file that may turn out cut short further on, which is why nothing
is made live early.

Exit status: 0 and a summary on standard output; 1 and
"backup_open: refused: <reason>" on standard error for a file that does not
open (or whose plaintext SHA-256 is not --expect-sha256); 2 for a mistake in the
command line or the key file. Nothing of CORIA's is used.
"""

import argparse
import hashlib
import os
import struct
import sys
import tempfile

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "lib"))
from backup_format import TAG, KeyFileError, Refused, open_chunks, read_keys  # noqa: E402


def open_file(keys, data, out=None, expect=None):
    """Authenticate every chunk of data; return (plaintext size, SHA-256 hex,
    chunk count). With out, the plaintext is staged beside it and renamed over
    it after the last chunk (and after the SHA-256 matched expect, when given);
    on Refused or any error nothing is left."""
    digest = hashlib.sha256()
    size = chunks = 0
    stage = None
    if out:
        fd, stage = tempfile.mkstemp(prefix=os.path.basename(out) + ".", suffix=".partial",
                                     dir=os.path.dirname(os.path.abspath(out)))
        handle = os.fdopen(fd, "wb")
    try:
        for piece in open_chunks(keys, data):
            digest.update(piece)
            size += len(piece)
            chunks += 1
            if out:
                handle.write(piece)
        if expect and digest.hexdigest() != expect.lower():
            raise Refused("the plaintext's SHA-256 is %s, not the expected %s" % (digest.hexdigest(), expect.lower()))
        if out:
            handle.flush()
            os.fsync(handle.fileno())
            handle.close()
            os.replace(stage, out)
            stage = None
    finally:
        if stage is not None:
            handle.close()
            os.unlink(stage)
    return size, digest.hexdigest(), chunks


def main(argv):
    p = argparse.ArgumentParser(description="Open a CORIA backup with the installation's key file; "
                                "see this tool's docstring.")
    p.add_argument("backup", nargs="?", help="the backup file")
    p.add_argument("--key-file", help="the installation key file (coria-key-v1 lines)")
    p.add_argument("--out", help="write the plaintext here, after every chunk opened")
    p.add_argument("--expect-sha256", help="hex SHA-256 the plaintext must have")
    p.add_argument("--self-test", action="store_true", help="check the tool against backups it makes and breaks")
    args = p.parse_args(argv)
    if args.self_test:
        import backup_open_selftest
        return backup_open_selftest.run()
    if not args.backup or not args.key_file:
        p.error("a backup and --key-file are required")
    try:
        keys = read_keys(args.key_file)
        with open(args.backup, "rb") as f:
            data = f.read()
    except (KeyFileError, OSError, ValueError) as e:
        print("backup_open: %s" % e, file=sys.stderr)
        return 2
    try:
        size, sha, chunks = open_file(keys, data, args.out, args.expect_sha256)
    except Refused as e:
        print("backup_open: refused: %s" % e, file=sys.stderr)
        return 1
    version, chunk = struct.unpack(">II", data[9:17])
    print("backup_open: ok: %d bytes of ciphertext, key version %d, %d chunks of %d (%d-byte tags), every chunk opened, "
          "the last as the last" % (len(data), version, chunks, chunk, TAG))
    print("plaintext: %d bytes sha256 %s%s" % (size, sha, (" written to " + args.out) if args.out else " (not written)"))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
