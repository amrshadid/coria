import { mkdirSync, mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, beforeAll, describe, expect, inject, it } from "vitest";

import { Code, CoriaError, coriaInterceptors, createCoriaClient as createBrowserClient, pages } from "../src/browser.js";
import { createCoriaClient as createNodeClient } from "../src/node.js";
import { file_coria_v1_access, Role } from "../src/gen/coria/v1/access_pb.js";
import { file_coria_v1_audit } from "../src/gen/coria/v1/audit_pb.js";
import { file_coria_v1_catalog } from "../src/gen/coria/v1/catalog_pb.js";
import { file_coria_v1_data } from "../src/gen/coria/v1/data_pb.js";
import { file_coria_v1_metadata } from "../src/gen/coria/v1/metadata_pb.js";
import { file_coria_v1_monitor } from "../src/gen/coria/v1/monitor_pb.js";
import { file_coria_v1_quarantine } from "../src/gen/coria/v1/quarantine_pb.js";
import { file_coria_v1_receiver } from "../src/gen/coria/v1/receiver_pb.js";
import { file_coria_v1_reception } from "../src/gen/coria/v1/reception_pb.js";
import { file_coria_v1_settings, SettingSource } from "../src/gen/coria/v1/settings_pb.js";
import { file_coria_v1_storage } from "../src/gen/coria/v1/storage_pb.js";
import { DatabaseType, file_coria_v1_tenant, TenantState } from "../src/gen/coria/v1/tenant_pb.js";
import { freePort, Server } from "./server.js";

// The client attribute for each service of the contract.
const SERVICE_ATTRIBUTES: Record<string, string> = {
  AccessService: "access",
  AuditService: "audit",
  CatalogService: "catalog",
  DataService: "data",
  MetadataService: "metadata",
  MonitorService: "monitor",
  QuarantineService: "quarantine",
  ReceiverService: "receivers",
  ReceptionService: "receptions",
  SettingsService: "settings",
  StorageService: "storages",
  TenantService: "tenants",
};

const FILES = [
  file_coria_v1_access, file_coria_v1_audit, file_coria_v1_catalog, file_coria_v1_data,
  file_coria_v1_metadata, file_coria_v1_monitor, file_coria_v1_quarantine, file_coria_v1_receiver,
  file_coria_v1_reception, file_coria_v1_settings, file_coria_v1_storage, file_coria_v1_tenant,
];
const SERVICES = FILES.flatMap((f) => f.services);

let server: Server;
let baseUrl: string;
let admin: ReturnType<typeof createNodeClient>;

beforeAll(async () => {
  server = await Server.start(inject("binary"));
  baseUrl = `http://${server.address}`;
  admin = createNodeClient({ baseUrl, key: server.key });
});

afterAll(async () => {
  await server?.close();
});

/** Calls fn and answers the CoriaError it throws; fails when it does not throw. */
async function failure(fn: () => Promise<unknown>): Promise<CoriaError> {
  try {
    await fn();
  } catch (err) {
    expect(err).toBeInstanceOf(CoriaError);
    return err as CoriaError;
  }
  throw new Error("the call did not fail");
}

describe("the contract's services", () => {
  it("has a client for every service the contract defines, and no other", () => {
    expect(SERVICES.map((s) => s.name).sort()).toEqual(Object.keys(SERVICE_ATTRIBUTES).sort());
    for (const attribute of Object.values(SERVICE_ATTRIBUTES)) {
      expect(admin).toHaveProperty(attribute);
    }
  });
});

describe("who the key says the caller is", () => {
  // The same call over each way the client can speak to the server.
  const ways = [
    ["connect over HTTP/1.1 (node)", () => createNodeClient({ baseUrl, key: server.key })],
    ["connect over h2c (node)", () => createNodeClient({ baseUrl, key: server.key, httpVersion: "2" })],
    ["grpc (node)", () => createNodeClient({ baseUrl, key: server.key, protocol: "grpc" })],
    ["grpc-web over HTTP/1.1 (node)", () => createNodeClient({ baseUrl, key: server.key, protocol: "grpc-web" })],
    ["connect over fetch (browser entry)", () => createBrowserClient({ baseUrl, key: server.key })],
  ] as const;
  for (const [name, make] of ways) {
    it(`names the key holder: ${name}`, async () => {
      const me = (await make().access.whoAmI({})).principal!;
      expect(me.caller.case).toBe("key");
      if (me.caller.case !== "key") return;
      expect(me.caller.value.holder).toBe("admin");
      expect(me.grants).toHaveLength(1);
      expect(me.grants[0]!.role).toBe(Role.ADMIN);
      expect(me.grants[0]!.scope?.kind.case).toBe("installation");
    });
  }
});

describe("failures", () => {
  it("a wrong key is Unauthenticated, with the server's message and the request id", async () => {
    const bad = `cor_${"1".repeat(12)}_${"2".repeat(40)}`;
    const err = await failure(() => createNodeClient({ baseUrl, key: bad }).access.whoAmI({}));
    expect(err.code).toBe(Code.Unauthenticated);
    expect(err.rawMessage).not.toBe("");
    expect(err.message).toBe(`[unauthenticated] ${err.rawMessage}`);
    expect(err.requestId).toMatch(/\S/);
  });

  it("a wrong key is Unauthenticated over every protocol", async () => {
    const bad = `cor_${"1".repeat(12)}_${"2".repeat(40)}`;
    for (const protocol of ["grpc", "grpc-web"] as const) {
      const err = await failure(() => createNodeClient({ baseUrl, key: bad, protocol }).access.whoAmI({}));
      expect(err.code, protocol).toBe(Code.Unauthenticated);
    }
    const err = await failure(() => createBrowserClient({ baseUrl, key: bad }).access.whoAmI({}));
    expect(err.code).toBe(Code.Unauthenticated);
  });

  it("a key without a grant cannot create a tenant", async () => {
    const nobody = createNodeClient({ baseUrl, key: server.makeKey("nobody") });
    const err = await failure(() =>
      nobody.tenants.createTenant({ name: "nope", databaseType: DatabaseType.SQLITE }),
    );
    expect([Code.PermissionDenied, Code.Unauthenticated]).toContain(err.code);
  });

  it("a connection that cannot be made is Unavailable", async () => {
    const port = await freePort();
    const err = await failure(() =>
      createNodeClient({ baseUrl: `http://127.0.0.1:${port}`, key: server.key, timeoutMs: 5000 }).access.whoAmI({}),
    );
    expect(err.code).toBe(Code.Unavailable);
    expect(err.requestId).toBe("");
    expect(err.metadata.get("coria-request-id")).toBeNull();
  });

  it("refuses to be made without a key", () => {
    expect(() => createNodeClient({ baseUrl, key: "" })).toThrow("a key is required");
  });
});

describe("tenants", () => {
  it("are created, read, paged through by cursor at page size 1, and removed", async () => {
    const names = ["ts-a", "ts-b", "ts-c"];
    for (const name of names) {
      const made = (await admin.tenants.createTenant({ name, databaseType: DatabaseType.SQLITE })).tenant!;
      expect(made.name).toBe(name);
      expect(made.databaseType).toBe(DatabaseType.SQLITE);
      expect(made.state).toBe(TenantState.READY);
    }

    const got = (await admin.tenants.getTenant({ name: "ts-b" })).tenant!;
    expect(got.name).toBe("ts-b");
    expect(got.createTime!.seconds > 0n).toBe(true);

    const onePage = await admin.tenants.listTenants({ pageSize: 1 });
    expect(onePage.tenants).toHaveLength(1);
    expect(onePage.nextPageToken).not.toBe("");

    const request = { pageSize: 1 };
    const paged: string[] = [];
    for await (const tenant of pages(admin.tenants.listTenants, request, "tenants")) paged.push(tenant.name);
    expect([...paged].sort()).toEqual(names);
    expect(new Set(paged).size).toBe(paged.length);
    // The helper writes page tokens into copies, so a request can be reused.
    expect(request).toEqual({ pageSize: 1 });

    for (const name of names) await admin.tenants.removeTenant({ name, confirmName: name });
    const err = await failure(() => admin.tenants.getTenant({ name: "ts-b" }));
    expect(err.code).toBe(Code.NotFound);
  });
});

describe("settings", () => {
  it("are set, read back with their source, listed and unset to the default", async () => {
    const scope = { kind: { case: "installation", value: {} } } as const;
    const name = "key.default-lifetime";
    const before = (await admin.settings.getSetting({ name, scope })).setting!;
    expect(before.source).toBe(SettingSource.BUILT_IN);

    await admin.settings.setSetting({ setting: { name, scope, value: "30d" } });
    const after = (await admin.settings.getSetting({ name, scope })).setting!;
    expect(after.value).toBe("30d");
    expect(after.source).toBe(SettingSource.INSTALLATION);

    const listed = new Map<string, string>();
    for await (const s of pages(admin.settings.listSettings, { scope }, "settings")) listed.set(s.name, s.value);
    expect(listed.get(name)).toBe("30d");

    await admin.settings.unsetSetting({ name, scope });
    const again = (await admin.settings.getSetting({ name, scope })).setting!;
    expect(again.value).toBe(before.value);
    expect(again.source).toBe(SettingSource.BUILT_IN);
  });
});

describe("a tenant's storage and receiver", () => {
  it("are added, read and removed", async () => {
    const installation = { kind: { case: "installation", value: {} } } as const;
    const root = mkdtempSync(join(tmpdir(), "cor-ts-root."));
    mkdirSync(join(root, "archive"), { recursive: true });
    await admin.settings.setSetting({
      setting: { name: "storage.directory-roots", scope: installation, value: `fast=${root}` },
    });
    await admin.tenants.createTenant({ name: "ts-r", databaseType: DatabaseType.SQLITE });
    // A role on the installation does not reach inside a tenant: the key is
    // given admin on this tenant as well.
    const me = (await admin.access.whoAmI({})).principal!;
    if (me.caller.case !== "key") throw new Error("the caller is not a key");
    await admin.access.setGrant({
      keyId: me.caller.value.id,
      scope: { kind: { case: "tenant", value: "ts-r" } },
      role: Role.ADMIN,
    });
    const port = await freePort();
    await admin.settings.setSetting({
      setting: { name: "receiver.ports", scope: { kind: { case: "tenant", value: "ts-r" } }, value: String(port) },
    });
    try {
      await admin.storages.addStorage({
        tenant: "ts-r",
        storage: { name: "main", type: { case: "directory", value: { root: "fast", path: "main" } } },
      });
      const storages: string[] = [];
      for await (const s of pages(admin.storages.listStorages, { tenant: "ts-r" }, "storages")) storages.push(s.name);
      expect(storages).toEqual(["main"]);

      await admin.receivers.addReceiver({
        tenant: "ts-r",
        receiver: {
          name: "scanner",
          storage: "main",
          enabled: false,
          type: {
            case: "dimse",
            value: {
              port,
              aeTitle: "TSTEST",
              callers: {
                case: "allowedCallers",
                value: { rules: [{ callingAeTitle: "SCANNER1" }] },
              },
            },
          },
        },
      });
      const got = (await admin.receivers.getReceiver({ tenant: "ts-r", name: "scanner" })).receiver!;
      expect(got.storage).toBe("main");
      expect(got.enabled).toBe(false);
      expect(got.type.case).toBe("dimse");
      if (got.type.case === "dimse") {
        expect(got.type.value.port).toBe(port);
        expect(got.type.value.aeTitle).toBe("TSTEST");
      }

      await admin.receivers.removeReceiver({ tenant: "ts-r", name: "scanner" });
      const err = await failure(() => admin.receivers.getReceiver({ tenant: "ts-r", name: "scanner" }));
      expect(err.code).toBe(Code.NotFound);
    } finally {
      await admin.tenants.removeTenant({ name: "ts-r", confirmName: "ts-r" });
      await admin.settings.unsetSetting({ name: "storage.directory-roots", scope: installation });
    }
  });
});

describe("services without handlers", () => {
  // api/not-served.txt lists the services the server does not serve yet.
  // When a handler arrives the service leaves the file and its own tests
  // take over.
  const listed = readFileSync(join(inject("repo"), "api", "not-served.txt"), "utf8")
    .split("\n")
    .map((l) => l.split("#")[0]!.trim().replace(/^coria\.v1\./, ""))
    .filter((l) => l !== "");

  it("lists at least one service", () => {
    expect(listed.length).toBeGreaterThan(0);
  });

  for (const service of listed) {
    it(`${service} answers Unimplemented to every method`, async () => {
      const descriptor = SERVICES.find((s) => s.name === service);
      expect(descriptor, service).toBeDefined();
      expect(descriptor!.methods.length).toBeGreaterThan(0);
      const client = admin[SERVICE_ATTRIBUTES[service]! as keyof typeof admin] as unknown as Record<
        string,
        (request: object) => unknown
      >;
      for (const method of descriptor!.methods) {
        const err = await failure(async () => {
          const result = client[method.localName]!({});
          if (method.methodKind === "server_streaming") {
            for await (const _ of result as AsyncIterable<unknown>) break;
          } else {
            await (result as Promise<unknown>);
          }
        });
        expect(err.code, `${service}.${method.name}`).toBe(Code.Unimplemented);
      }
    });
  }
});

describe("transports and interceptors", () => {
  it("accepts a transport the caller built", async () => {
    const { createConnectTransport } = await import("@connectrpc/connect-node");
    const transport = createConnectTransport({
      baseUrl,
      httpVersion: "1.1",
      interceptors: coriaInterceptors(server.key),
    });
    const me = (await createBrowserClient({ transport }).access.whoAmI({})).principal!;
    expect(me.caller.case).toBe("key");
  });
});
