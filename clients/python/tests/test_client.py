"""The client against a real server: what it sends, what it returns, how it fails."""

import grpc
import pytest

import coria
from conftest import free_port
from coria.v1 import access_pb2, receiver_pb2, scope_pb2, settings_pb2, storage_pb2, tenant_pb2

# The names of the contract's services and the client attribute for each.
SERVICE_ATTRIBUTES = {
    "AccessService": "access",
    "AuditService": "audit",
    "CatalogService": "catalog",
    "DataService": "data",
    "MetadataService": "metadata",
    "MonitorService": "monitor",
    "QuarantineService": "quarantine",
    "ReceiverService": "receivers",
    "ReceptionService": "receptions",
    "SettingsService": "settings",
    "StorageService": "storages",
    "TenantService": "tenants",
}


def test_every_service_has_a_stub(client):
    """A service added to the contract and left out of Client fails here."""
    from coria._gen.coria.v1 import (
        access_pb2, audit_pb2, catalog_pb2, data_pb2, metadata_pb2, monitor_pb2,
        quarantine_pb2, receiver_pb2, reception_pb2, settings_pb2, storage_pb2, tenant_pb2,
    )
    defined = set()
    for module in (access_pb2, audit_pb2, catalog_pb2, data_pb2, metadata_pb2, monitor_pb2,
                   quarantine_pb2, receiver_pb2, reception_pb2, settings_pb2, storage_pb2, tenant_pb2):
        defined |= set(module.DESCRIPTOR.services_by_name)
    assert defined == set(SERVICE_ATTRIBUTES)
    for attribute in SERVICE_ATTRIBUTES.values():
        assert hasattr(client, attribute)


def test_whoami_names_the_key_holder(client):
    """The key reaches the server as a bearer credential and is recognized."""
    me = client.access.WhoAmI(access_pb2.WhoAmIRequest()).principal
    assert me.WhichOneof("caller") == "key"
    assert me.key.holder == "admin"
    assert len(me.grants) == 1
    assert me.grants[0].role == access_pb2.ROLE_ADMIN
    assert me.grants[0].scope.WhichOneof("kind") == "installation"


def test_a_wrong_key_is_unauthenticated(server):
    """The one exception carries the code and the server's message."""
    bad = "cor_" + "1" * 12 + "_" + "2" * 40
    with coria.Client(f"127.0.0.1:{server.port}", bad, tls=False) as c:
        with pytest.raises(coria.Error) as caught:
            c.access.WhoAmI(access_pb2.WhoAmIRequest())
    assert caught.value.code == grpc.StatusCode.UNAUTHENTICATED
    assert caught.value.message


def test_a_key_without_a_grant_is_refused(server):
    """A key that holds no role cannot list tenants."""
    key = server.make_key("nobody")
    with coria.Client(f"127.0.0.1:{server.port}", key, tls=False) as c:
        with pytest.raises(coria.Error) as caught:
            c.tenants.CreateTenant(tenant_pb2.CreateTenantRequest(name="nope", database_type=tenant_pb2.DATABASE_TYPE_SQLITE))
    assert caught.value.code in (grpc.StatusCode.PERMISSION_DENIED, grpc.StatusCode.UNAUTHENTICATED)


def test_tenant_lifecycle_and_paging(client):
    """Tenants are created, read, paged through by cursor, and removed."""
    names = ["pytest-a", "pytest-b", "pytest-c"]
    for name in names:
        made = client.tenants.CreateTenant(tenant_pb2.CreateTenantRequest(name=name, database_type=tenant_pb2.DATABASE_TYPE_SQLITE)).tenant
        assert made.name == name
        assert made.database_type == tenant_pb2.DATABASE_TYPE_SQLITE
        assert made.state == tenant_pb2.TENANT_STATE_READY

    got = client.tenants.GetTenant(tenant_pb2.GetTenantRequest(name="pytest-b")).tenant
    assert got.name == "pytest-b"
    assert got.create_time.seconds > 0

    one_page = client.tenants.ListTenants(tenant_pb2.ListTenantsRequest(page_size=1))
    assert len(one_page.tenants) == 1
    assert one_page.next_page_token

    paged = [t.name for t in client.pages(
        client.tenants.ListTenants, tenant_pb2.ListTenantsRequest(page_size=1), "tenants")]
    assert sorted(paged) == names
    assert len(paged) == len(set(paged))

    for name in names:
        client.tenants.RemoveTenant(tenant_pb2.RemoveTenantRequest(name=name, confirm_name=name))
    with pytest.raises(coria.Error) as caught:
        client.tenants.GetTenant(tenant_pb2.GetTenantRequest(name="pytest-b"))
    assert caught.value.code == grpc.StatusCode.NOT_FOUND


def test_pages_leaves_the_request_unchanged(client):
    """The helper writes page tokens into a copy, so a request can be reused."""
    request = tenant_pb2.ListTenantsRequest(page_size=1)
    for name in ("pytest-d", "pytest-e"):
        client.tenants.CreateTenant(tenant_pb2.CreateTenantRequest(name=name, database_type=tenant_pb2.DATABASE_TYPE_SQLITE))
    try:
        assert len(list(client.pages(client.tenants.ListTenants, request, "tenants"))) >= 2
        assert request.page_token == ""
    finally:
        for name in ("pytest-d", "pytest-e"):
            client.tenants.RemoveTenant(tenant_pb2.RemoveTenantRequest(name=name, confirm_name=name))


def test_settings_round_trip(client):
    """A setting is set, read back with its source, and unset to its default."""
    scope = scope_pb2.Scope(installation=scope_pb2.Installation())
    name = "key.default-lifetime"
    before = client.settings.GetSetting(settings_pb2.GetSettingRequest(name=name, scope=scope)).setting
    assert before.source == settings_pb2.SETTING_SOURCE_BUILT_IN

    client.settings.SetSetting(settings_pb2.SetSettingRequest(
        setting=settings_pb2.Setting(name=name, scope=scope, value="30d")))
    after = client.settings.GetSetting(settings_pb2.GetSettingRequest(name=name, scope=scope)).setting
    assert after.value == "30d"
    assert after.source == settings_pb2.SETTING_SOURCE_INSTALLATION

    listed = {s.name: s.value for s in client.pages(
        client.settings.ListSettings, settings_pb2.ListSettingsRequest(scope=scope), "settings")}
    assert listed[name] == "30d"

    client.settings.UnsetSetting(settings_pb2.UnsetSettingRequest(name=name, scope=scope))
    again = client.settings.GetSetting(settings_pb2.GetSettingRequest(name=name, scope=scope)).setting
    assert again.value == before.value
    assert again.source == settings_pb2.SETTING_SOURCE_BUILT_IN


def test_storage_and_receiver_of_a_tenant(client, server, tmp_path):
    """A storage on a named root and a receiver that uses it are added, read and removed."""
    scope = scope_pb2.Scope(installation=scope_pb2.Installation())
    root = tmp_path / "archive"
    root.mkdir()
    client.settings.SetSetting(settings_pb2.SetSettingRequest(setting=settings_pb2.Setting(
        name="storage.directory-roots", scope=scope, value=f"fast={root}")))
    client.tenants.CreateTenant(tenant_pb2.CreateTenantRequest(name="pytest-r", database_type=tenant_pb2.DATABASE_TYPE_SQLITE))
    # A role on the installation does not reach inside a tenant: the key is
    # given admin on this tenant as well.
    key_id = client.access.WhoAmI(access_pb2.WhoAmIRequest()).principal.key.id
    client.access.SetGrant(access_pb2.SetGrantRequest(
        key_id=key_id, scope=scope_pb2.Scope(tenant="pytest-r"), role=access_pb2.ROLE_ADMIN))
    port = free_port()
    client.settings.SetSetting(settings_pb2.SetSettingRequest(setting=settings_pb2.Setting(
        name="receiver.ports", scope=scope_pb2.Scope(tenant="pytest-r"), value=str(port))))
    try:
        client.storages.AddStorage(storage_pb2.AddStorageRequest(
            tenant="pytest-r",
            storage=storage_pb2.Storage(name="main", directory=storage_pb2.DirectoryStorage(root="fast", path="main"))))
        listed = [s.name for s in client.pages(
            client.storages.ListStorages, storage_pb2.ListStoragesRequest(tenant="pytest-r"), "storages")]
        assert listed == ["main"]

        client.receivers.AddReceiver(receiver_pb2.AddReceiverRequest(
            tenant="pytest-r",
            receiver=receiver_pb2.Receiver(
                name="scanner", storage="main", enabled=False,
                dimse=receiver_pb2.DimseReceiver(port=port, ae_title="PYTEST", allowed_callers=receiver_pb2.AllowedCallers(
                    rules=[receiver_pb2.CallerRule(calling_ae_title="SCANNER1")])))))
        got = client.receivers.GetReceiver(receiver_pb2.GetReceiverRequest(tenant="pytest-r", name="scanner")).receiver
        assert got.storage == "main"
        assert got.dimse.port == port
        assert got.dimse.ae_title == "PYTEST"
        assert not got.enabled

        client.receivers.RemoveReceiver(receiver_pb2.RemoveReceiverRequest(tenant="pytest-r", name="scanner"))
        with pytest.raises(coria.Error) as caught:
            client.receivers.GetReceiver(receiver_pb2.GetReceiverRequest(tenant="pytest-r", name="scanner"))
        assert caught.value.code == grpc.StatusCode.NOT_FOUND
    finally:
        client.tenants.RemoveTenant(tenant_pb2.RemoveTenantRequest(name="pytest-r", confirm_name="pytest-r"))
        client.settings.UnsetSetting(settings_pb2.UnsetSettingRequest(name="storage.directory-roots", scope=scope))


def not_served_services():
    """The services api/not-served.txt lists as having no handler yet."""
    from conftest import REPO
    lines = (REPO / "api" / "not-served.txt").read_text().splitlines()
    return [l.split("#")[0].strip().removeprefix("coria.v1.") for l in lines if l.split("#")[0].strip()]


@pytest.mark.parametrize("service", not_served_services())
def test_a_service_without_handlers_answers_unimplemented(client, service):
    """Every method of a service the server does not serve yet answers UNIMPLEMENTED.

    When a handler arrives, the service leaves api/not-served.txt and this
    test no longer covers it; its own tests take over.
    """
    import importlib
    module = importlib.import_module(f"coria.v1.{service.removesuffix('Service').lower()}_pb2")
    descriptor = module.DESCRIPTOR.services_by_name[service]
    stub = getattr(client, SERVICE_ATTRIBUTES[service])
    assert descriptor.methods
    for method in descriptor.methods:
        request = method.input_type._concrete_class()
        with pytest.raises(coria.Error) as caught:
            result = getattr(stub, method.name)(request)
            if method.server_streaming:
                next(iter(result))
        assert caught.value.code == grpc.StatusCode.UNIMPLEMENTED, method.full_name
