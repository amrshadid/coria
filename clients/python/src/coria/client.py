"""The channel to a CORIA server and the stubs of its services."""

from collections.abc import Callable, Iterable, Iterator
from typing import Any, Optional

import grpc

from coria._gen.coria.v1 import (
    access_pb2_grpc,
    audit_pb2_grpc,
    catalog_pb2_grpc,
    data_pb2_grpc,
    metadata_pb2_grpc,
    monitor_pb2_grpc,
    quarantine_pb2_grpc,
    receiver_pb2_grpc,
    reception_pb2_grpc,
    settings_pb2_grpc,
    storage_pb2_grpc,
    tenant_pb2_grpc,
)
from coria.errors import Error


class _KeyInterceptor(
    grpc.UnaryUnaryClientInterceptor,
    grpc.UnaryStreamClientInterceptor,
    grpc.StreamUnaryClientInterceptor,
    grpc.StreamStreamClientInterceptor,
):
    """Adds the key to every call as the bearer credential the server reads.

    Call credentials of grpc are refused on a channel without TLS, and the
    server also serves loopback without it, so the header is added here.
    """

    def __init__(self, key: str):
        self._header = ("authorization", "Bearer " + key)

    def _with_key(self, details):
        metadata = list(details.metadata or ()) + [self._header]
        return details._replace(metadata=metadata)

    def intercept_unary_unary(self, continuation, details, request):
        return continuation(self._with_key(details), request)

    def intercept_unary_stream(self, continuation, details, request):
        return continuation(self._with_key(details), request)

    def intercept_stream_unary(self, continuation, details, request_iterator):
        return continuation(self._with_key(details), request_iterator)

    def intercept_stream_stream(self, continuation, details, request_iterator):
        return continuation(self._with_key(details), request_iterator)


class _Stub:
    """A generated stub whose methods raise coria.Error instead of grpc's.

    Attribute access answers the stub's method, wrapped; everything else
    (names, arguments, the request and response messages) is the generated
    stub's own, so the API contract documents it.
    """

    def __init__(self, stub: Any, timeout: Optional[float]):
        self._stub = stub
        self._timeout = timeout

    def __getattr__(self, name: str) -> Callable:
        method = getattr(self._stub, name)
        if name.startswith("_") or not callable(method):
            raise AttributeError(name)
        streams = isinstance(method, grpc.UnaryStreamMultiCallable)

        def call(request, **kwargs):
            if not streams:
                kwargs.setdefault("timeout", self._timeout)
            try:
                result = method(request, **kwargs)
            except grpc.RpcError as err:
                raise Error.from_rpc_error(err) from None
            return _raising(result) if streams else result

        call.__name__ = name
        return call


def _raising(stream: Iterable) -> Iterator:
    """Yield a server stream's messages, turning its failure into coria.Error."""
    try:
        yield from stream
    except grpc.RpcError as err:
        raise Error.from_rpc_error(err) from None


class Client:
    """A connection to one CORIA server, with a stub for each service.

    A unary call returns the response message; a server-streaming call
    returns an iterator of messages. A failure of either raises coria.Error.
    Messages and requests are the generated classes in coria.v1 (for example
    coria.v1.tenant_pb2.ListTenantsRequest).

    Args:
        address: "host:port" of the server's network listener.
        key: the API key, as "cor_<id>_<secret>".
        tls: speak TLS (the default). False is for a loopback server started
            without a certificate; the key then travels in clear.
        root_certificates: PEM bytes of the authorities to trust instead of
            the system's, for a server with a private certificate.
        timeout: seconds a unary call may take; None for no limit.
    """

    def __init__(
        self,
        address: str,
        key: str,
        tls: bool = True,
        root_certificates: Optional[bytes] = None,
        timeout: Optional[float] = 60.0,
    ):
        if not key:
            raise ValueError("a key is required")
        if tls:
            channel = grpc.secure_channel(address, grpc.ssl_channel_credentials(root_certificates))
        else:
            if root_certificates is not None:
                raise ValueError("root_certificates need tls=True")
            channel = grpc.insecure_channel(address)
        self._channel = grpc.intercept_channel(channel, _KeyInterceptor(key))
        self._raw_channel = channel

        def stub(module, name):
            return _Stub(getattr(module, name)(self._channel), timeout)

        self.access = stub(access_pb2_grpc, "AccessServiceStub")
        self.audit = stub(audit_pb2_grpc, "AuditServiceStub")
        self.catalog = stub(catalog_pb2_grpc, "CatalogServiceStub")
        self.data = stub(data_pb2_grpc, "DataServiceStub")
        self.metadata = stub(metadata_pb2_grpc, "MetadataServiceStub")
        self.monitor = stub(monitor_pb2_grpc, "MonitorServiceStub")
        self.quarantine = stub(quarantine_pb2_grpc, "QuarantineServiceStub")
        self.receivers = stub(receiver_pb2_grpc, "ReceiverServiceStub")
        self.receptions = stub(reception_pb2_grpc, "ReceptionServiceStub")
        self.settings = stub(settings_pb2_grpc, "SettingsServiceStub")
        self.storages = stub(storage_pb2_grpc, "StorageServiceStub")
        self.tenants = stub(tenant_pb2_grpc, "TenantServiceStub")

    def close(self) -> None:
        """Close the connection."""
        self._raw_channel.close()

    def __enter__(self) -> "Client":
        return self

    def __exit__(self, *exc) -> None:
        self.close()

    @staticmethod
    def pages(call: Callable, request, field: str) -> Iterator[Any]:
        """Yield every item of a list call, page after page.

        Calls `call(request)`, yields the items of the response's repeated
        field `field`, and calls again with the response's next_page_token
        until the server answers an empty one. Set page_size on the request
        to choose the page. The request is not changed.

        Example: client.pages(client.tenants.ListTenants, ListTenantsRequest(), "tenants")
        """
        request = type(request).FromString(request.SerializeToString())
        while True:
            response = call(request)
            yield from getattr(response, field)
            if not response.next_page_token:
                return
            request.page_token = response.next_page_token
