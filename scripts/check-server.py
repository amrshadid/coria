#!/usr/bin/env python3
"""The server checked by Python's grpcio, a gRPC implementation that is not Go.

Usage: check-server.py PROTO_DIR SOCKET TLS_ADDRESS CA_FILE NO_GRANT_KEY ADMIN_KEY METRICS_FILE

It compiles the contract with grpcio-tools into a scratch directory, then
calls the server run by scripts/check-server.sh:

  - over TLS: ListKeys with no key, a malformed key, an unknown key and a key
    with no grant, each of which must be refused with the code and message
    the contract fixes, and the same call with an admin key, which must
    succeed; every answer must carry a Magnetar-Request-Id;
  - over the local socket (unix: address): WhoAmI as the local operator,
    which must succeed, and with an unknown key, which must be refused;
  - the standard health service over TLS without a key, as raw bytes (an
    empty HealthCheckRequest; SERVING is field 1 = 1), so no health package
    is needed;
  - the text in METRICS_FILE (a scrape of /metrics) through the Prometheus
    Python client's parser.

It prints one line per check and exits 1 on the first failure.
"""

import importlib
import os
import sys
import tempfile

import grpc
from grpc_tools import protoc
from prometheus_client.parser import text_string_to_metric_families

UNAUTHENTICATED = "the request carries no valid key"
DENIED = "the key may not do this"
UNKNOWN_KEY = "mgt_111111111111_1111111111111111111111111111111111111111111"


def fail(msg):
    print("FAIL:", msg)
    sys.exit(1)


def compile_contract(proto_dir, out_dir):
    files = [os.path.join("magnetar", "v1", f) for f in sorted(os.listdir(os.path.join(proto_dir, "magnetar", "v1")))]
    import grpc_tools

    include = os.path.join(os.path.dirname(grpc_tools.__file__), "_proto")
    args = ["protoc", "-I" + proto_dir, "-I" + include, "--python_out=" + out_dir, "--grpc_python_out=" + out_dir] + files
    if protoc.main(args) != 0:
        fail("grpcio-tools could not compile the contract")
    sys.path.insert(0, out_dir)


def call(stub_method, request, key=None):
    metadata = [("authorization", "Bearer " + key)] if key else []
    try:
        _, rpc = stub_method.with_call(request, metadata=metadata, timeout=10)
        return grpc.StatusCode.OK, "", dict(rpc.initial_metadata())
    except grpc.RpcError as e:
        md = dict(e.initial_metadata() or []) | dict(e.trailing_metadata() or [])
        return e.code(), e.details(), md


def expect(name, got, code, message):
    status, details, md = got
    if status != code or (message is not None and details != message):
        fail(f"{name}: got {status} {details!r}, want {code} {message!r}")
    if not md.get("magnetar-request-id"):
        fail(f"{name}: no magnetar-request-id")
    print(f"ok   {name}: {status.name} {details!r} request-id={md['magnetar-request-id']}")


def main():
    if len(sys.argv) != 8:
        fail(__doc__)
    proto_dir, socket, tls_addr, ca_file, no_grant, admin, metrics_file = sys.argv[1:]
    out = tempfile.mkdtemp(prefix="mgt-py-")
    compile_contract(proto_dir, out)
    access_pb2 = importlib.import_module("magnetar.v1.access_pb2")
    access_grpc = importlib.import_module("magnetar.v1.access_pb2_grpc")

    with open(ca_file, "rb") as f:
        creds = grpc.ssl_channel_credentials(root_certificates=f.read())
    tls = grpc.secure_channel(tls_addr, creds)
    stub = access_grpc.AccessServiceStub(tls)
    req = access_pb2.ListKeysRequest()
    expect("tls no key", call(stub.ListKeys, req), grpc.StatusCode.UNAUTHENTICATED, UNAUTHENTICATED)
    try:
        _, rpc = stub.ListKeys.with_call(req, metadata=[("authorization", "garbage")], timeout=10)
        fail("tls malformed key was served")
    except grpc.RpcError as e:
        expect("tls malformed key", (e.code(), e.details(), dict(e.initial_metadata() or [])), grpc.StatusCode.UNAUTHENTICATED, UNAUTHENTICATED)
    expect("tls unknown key", call(stub.ListKeys, req, UNKNOWN_KEY), grpc.StatusCode.UNAUTHENTICATED, UNAUTHENTICATED)
    expect("tls key with no grant", call(stub.ListKeys, req, no_grant), grpc.StatusCode.PERMISSION_DENIED, DENIED)
    expect("tls admin key", call(stub.ListKeys, req, admin), grpc.StatusCode.OK, "")

    health = tls.unary_unary("/grpc.health.v1.Health/Check", request_serializer=None, response_deserializer=None)
    answer = health(b"", timeout=10)
    if answer != b"\x08\x01":
        fail(f"health: {answer!r}, want SERVING (08 01)")
    print("ok   tls health without a key: SERVING")
    tls.close()

    local = grpc.insecure_channel("unix:" + socket)
    lstub = access_grpc.AccessServiceStub(local)
    status, details, md = call(lstub.WhoAmI, access_pb2.WhoAmIRequest())
    expect("socket local operator", (status, details, md), grpc.StatusCode.OK, "")
    expect("socket unknown key", call(lstub.WhoAmI, access_pb2.WhoAmIRequest(), UNKNOWN_KEY), grpc.StatusCode.UNAUTHENTICATED, UNAUTHENTICATED)
    local.close()

    with open(metrics_file) as f:
        text = f.read()
    families = {fam.name: fam for fam in text_string_to_metric_families(text)}
    for name in ("magnetar_requests", "magnetar_request_duration_seconds", "magnetar_requests_in_flight",
                 "magnetar_audit_entries_written", "magnetar_audit_entries_failed",
                 "magnetar_audit_entries_dropped_refusals", "magnetar_audit_entries_dropped_records",
                 "magnetar_audit_entries_dropped_writes", "magnetar_audit_entries_rerouted", "magnetar_audit_entries_notices",
                 "magnetar_key_last_use_marks_written", "magnetar_key_last_use_marks_dropped", "magnetar_key_last_use_marks_failed"):
        if name not in families:
            fail(f"metrics: no family {name} in {sorted(families)}")
    samples = sum(len(f.samples) for f in families.values())
    print(f"ok   /metrics parsed by prometheus_client: {len(families)} families, {samples} samples")


if __name__ == "__main__":
    main()
