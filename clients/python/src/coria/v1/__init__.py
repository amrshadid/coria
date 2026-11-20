"""The messages and stubs of the coria.v1 API contract, generated from api/proto.

The generated modules live in coria._gen.coria.v1; this package gives them the
names the contract does, so `from coria.v1 import tenant_pb2` works. Each
service's messages are in <file>_pb2 and its stub in <file>_pb2_grpc.
"""

import importlib
import pkgutil
import sys

from coria._gen.coria import v1 as _gen

for _info in pkgutil.iter_modules(_gen.__path__):
    _module = importlib.import_module(f"{_gen.__name__}.{_info.name}")
    sys.modules[f"{__name__}.{_info.name}"] = _module
    globals()[_info.name] = _module
