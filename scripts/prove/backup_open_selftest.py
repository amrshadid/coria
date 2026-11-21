"""The self-test of backup_open.py: it makes backups with a key file written
here, checks they open to the same plaintext, then changes them in each way
the tool claims to refuse and checks that nothing is left staged or written.
Run by `backup_open.py --self-test`.
"""

import base64
import hashlib
import os
import sys
import tempfile

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "lib"))
import backup_open as bo  # noqa: E402
from backup_format import HEADER, TAG, Refused, make_backup, read_keys  # noqa: E402


def write_key_file(path, versions):
    with open(path, "w", encoding="ascii") as f:
        for v in versions:
            f.write("coria-key-v1 %d %s\n" % (v, base64.b64encode(os.urandom(32)).decode()))


def run():
    with tempfile.TemporaryDirectory() as tmp:
        key_path = os.path.join(tmp, "coria.key")
        write_key_file(key_path, [1, 2])
        other_path = os.path.join(tmp, "other.key")
        write_key_file(other_path, [1, 2])
        keys, other = read_keys(key_path), read_keys(other_path)
        size = 64
        plain = os.urandom(300)
        data = make_backup(keys, 2, size, plain)
        span = size + TAG
        out = os.path.join(tmp, "plain.bin")

        s, sha, chunks = bo.open_file(keys, data, out)
        assert (s, sha, chunks) == (300, hashlib.sha256(plain).hexdigest(), 5), (s, sha, chunks)
        assert open(out, "rb").read() == plain
        print("self-test: %-34s -> ok (5 chunks, plaintext equal)" % "intact backup")
        os.unlink(out)
        for n in (0, size, 2 * size, 5 * size):
            p = os.urandom(n)
            got = bo.open_file(keys, make_backup(keys, 1, size, p))
            assert got[0] == n and got[1] == hashlib.sha256(p).hexdigest()
        print("self-test: %-34s -> ok (empty, 1, 2 and 5 full chunks)" % "chunk boundaries")

        def refused(name, body, key_set=keys, **kw):
            try:
                bo.open_file(key_set, body, out, **kw)
            except Refused as e:
                left = os.listdir(tmp)
                assert "plain.bin" not in left and not [x for x in left if x.endswith(".partial")], left
                print("self-test: %-34s -> refused: %s" % (name, e))
                return
            raise SystemExit("self-test %s: the file opened" % name)

        c0, c1 = data[HEADER:HEADER + span], data[HEADER + span:HEADER + 2 * span]
        refused("cut after chunk 0", data[:HEADER + span])
        refused("cut at the last byte", data[:-1])
        refused("header only", data[:HEADER])
        refused("chunks 0 and 1 swapped", data[:HEADER] + c1 + c0 + data[HEADER + 2 * span:])
        refused("chunk 1 dropped", data[:HEADER] + c0 + data[HEADER + 2 * span:])
        flipped = bytearray(data)
        flipped[HEADER + span + 3] ^= 1
        refused("a bit flipped in chunk 1", bytes(flipped))
        refused("chunk 0 appended", data + c0)
        refused("an empty chunk's tag appended", data + b"\x00" * TAG)
        hdr = bytearray(data)
        hdr[10] ^= 1
        refused("key version in the header changed", bytes(hdr))
        salt = bytearray(data)
        salt[20] ^= 1
        refused("salt changed", bytes(salt))
        refused("a different key file", data, other)
        refused("not a backup", b"x" * 100)
        refused("plaintext hash not the expected", data, expect="00" * 32)

        path = os.path.join(tmp, "backup.bin")
        with open(path, "wb") as f:
            f.write(data)
        assert bo.main([path, "--key-file", key_path, "--out", out, "--expect-sha256", hashlib.sha256(plain).hexdigest()]) == 0
        assert open(out, "rb").read() == plain
        os.unlink(out)
        with open(path, "wb") as f:
            f.write(data[:-1])
        assert bo.main([path, "--key-file", key_path, "--out", out]) == 1 and not os.path.exists(out)
        assert bo.main([path, "--key-file", os.path.join(tmp, "missing")]) == 2
        print("self-test: command line exits 0, 1 (nothing written) and 2")
    print("backup_open self-test: all passed")
    return 0
