"""The client over TLS, with a private certificate authority."""

import shutil
import subprocess

import grpc
import pytest

import coria
from coria.v1 import access_pb2

from conftest import free_port


@pytest.mark.skipif(shutil.which("openssl") is None, reason="needs the openssl tool")
def test_tls_with_a_private_certificate(server, tmp_path):
    """A certificate the system does not trust is accepted only with root_certificates."""
    cert, key = tmp_path / "cert.pem", tmp_path / "key.pem"
    subprocess.run(
        ["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes",
         "-days", "1", "-subj", "/CN=coria-test", "-addext", "subjectAltName=IP:127.0.0.1",
         "-keyout", str(key), "-out", str(cert)],
        check=True, capture_output=True,
    )
    server.stop()
    port = free_port()
    server.process = server._start(f"127.0.0.1:{port}", "--tls-cert", str(cert), "--tls-key", str(key))
    address = f"127.0.0.1:{port}"
    try:
        with coria.Client(address, server.key, root_certificates=cert.read_bytes()) as c:
            assert c.access.WhoAmI(access_pb2.WhoAmIRequest()).principal.key.holder == "admin"
        with coria.Client(address, server.key, timeout=10) as untrusting:
            with pytest.raises(coria.Error) as caught:
                untrusting.access.WhoAmI(access_pb2.WhoAmIRequest())
        assert caught.value.code == grpc.StatusCode.UNAVAILABLE
    finally:
        server.stop()
        server.port = free_port()
        server.process = server._start(f"127.0.0.1:{server.port}")
