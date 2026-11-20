#!/usr/bin/env python3
"""Make the Python code of the API contract, or check that it is current.

The contract is the proto files in api/proto. This script compiles them with
the protoc that grpcio-tools bundles (the version pinned in
scripts/requirements/proto.txt) and writes the modules under
src/coria/_gen, which are checked in and never edited by hand.

protoc writes `from coria.v1 import x_pb2`, which in this package would name
the package itself instead of the generated modules, so the script rewrites
those imports to `from coria._gen.coria.v1 import x_pb2`. Nothing else is
changed, so the output is what the compiler wrote.

Usage:
  generate.py            write src/coria/_gen again
  generate.py --check    exit 1 when src/coria/_gen differs from a fresh
                         generation, listing the files that differ

The output depends on the compiler's version: run it with the pinned one.
"""

import importlib.resources
import pathlib
import re
import shutil
import sys
import tempfile

from grpc_tools import protoc

HERE = pathlib.Path(__file__).resolve().parent
PROTO_ROOT = HERE.parent.parent / "api" / "proto"
GEN = HERE / "src" / "coria" / "_gen"

# The generated modules import each other by the proto package's name.
IMPORT = re.compile(r"^(from|import) coria\.v1 ", re.MULTILINE)
PACKAGE_NOTE = '"""Python code generated from the CORIA API contract. Do not edit."""\n'


def generate(out: pathlib.Path) -> None:
    """Compile every proto file into out, with imports pointing inside the package."""
    protos = sorted(str(p.relative_to(PROTO_ROOT)) for p in PROTO_ROOT.rglob("*.proto"))
    if not protos:
        raise SystemExit(f"no proto files under {PROTO_ROOT}")
    # grpcio-tools carries the well-known types (timestamp, duration).
    wkt = importlib.resources.files("grpc_tools") / "_proto"
    args = [
        "grpc_tools.protoc",
        f"-I{PROTO_ROOT}",
        f"-I{wkt}",
        f"--python_out={out}",
        f"--pyi_out={out}",
        f"--grpc_python_out={out}",
        *protos,
    ]
    if protoc.main(args) != 0:
        raise SystemExit("grpc_tools.protoc failed")
    for path in out.rglob("*.py*"):
        text = path.read_text()
        path.write_text(
            IMPORT.sub(lambda m: f"{m.group(1)} coria._gen.coria.v1 ", text)
            .replace("from coria.v1 import", "from coria._gen.coria.v1 import")
        )
    for directory in [out, out / "coria", out / "coria" / "v1"]:
        (directory / "__init__.py").write_text(PACKAGE_NOTE)


def tree(root: pathlib.Path) -> dict:
    """Answer each file under root, by relative path, with its bytes."""
    return {
        str(p.relative_to(root)): p.read_bytes()
        for p in sorted(root.rglob("*"))
        if p.is_file() and "__pycache__" not in p.parts
    }


def main() -> int:
    check = sys.argv[1:] == ["--check"]
    if sys.argv[1:] and not check:
        print(__doc__, file=sys.stderr)
        return 2
    with tempfile.TemporaryDirectory() as tmp:
        fresh = pathlib.Path(tmp)
        generate(fresh)
        if check:
            have, want = tree(GEN), tree(fresh)
            bad = sorted(k for k in have.keys() | want.keys() if have.get(k) != want.get(k))
            for k in bad:
                print(f"differs from a fresh generation: {k}", file=sys.stderr)
            if bad:
                print("run clients/python/generate.py and commit the result", file=sys.stderr)
                return 1
            print(f"src/coria/_gen equals a fresh generation ({len(want)} files)")
            return 0
        shutil.rmtree(GEN, ignore_errors=True)
        shutil.copytree(fresh, GEN)
        print(f"wrote {len(tree(GEN))} files under {GEN}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
