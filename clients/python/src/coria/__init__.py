"""Python client for the CORIA research imaging archive's API.

    import coria
    from coria.v1 import tenant_pb2

    with coria.Client("coria.example.org:8443", key) as client:
        for tenant in client.pages(client.tenants.ListTenants, tenant_pb2.ListTenantsRequest(), "tenants"):
            print(tenant.name)
"""

from coria import v1
from coria.client import Client
from coria.errors import Error

__all__ = ["Client", "Error", "v1"]
