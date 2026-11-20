import { createClient, type Client, type Interceptor, type Transport } from "@connectrpc/connect";

import { AccessService } from "./gen/coria/v1/access_pb.js";
import { AuditService } from "./gen/coria/v1/audit_pb.js";
import { CatalogService } from "./gen/coria/v1/catalog_pb.js";
import { DataService } from "./gen/coria/v1/data_pb.js";
import { MetadataService } from "./gen/coria/v1/metadata_pb.js";
import { MonitorService } from "./gen/coria/v1/monitor_pb.js";
import { QuarantineService } from "./gen/coria/v1/quarantine_pb.js";
import { ReceiverService } from "./gen/coria/v1/receiver_pb.js";
import { ReceptionService } from "./gen/coria/v1/reception_pb.js";
import { SettingsService } from "./gen/coria/v1/settings_pb.js";
import { StorageService } from "./gen/coria/v1/storage_pb.js";
import { TenantService } from "./gen/coria/v1/tenant_pb.js";
import { CoriaError } from "./errors.js";

/** One typed client for each service of the API contract. */
export interface CoriaClients {
  /** Keys, grants and who the caller is. */
  readonly access: Client<typeof AccessService>;
  /** The audit log. */
  readonly audit: Client<typeof AuditService>;
  /** Subjects, sessions and scans of a tenant. */
  readonly catalog: Client<typeof CatalogService>;
  /** Pixel and other bulk data of a tenant. */
  readonly data: Client<typeof DataService>;
  /** DICOM metadata of a tenant. */
  readonly metadata: Client<typeof MetadataService>;
  /** Health and counters of the installation. */
  readonly monitor: Client<typeof MonitorService>;
  /** Files set aside with a reason. */
  readonly quarantine: Client<typeof QuarantineService>;
  /** The receivers of a tenant. */
  readonly receivers: Client<typeof ReceiverService>;
  /** Receptions in progress or finished. */
  readonly receptions: Client<typeof ReceptionService>;
  /** Settings of the installation and of its tenants. */
  readonly settings: Client<typeof SettingsService>;
  /** The storages of a tenant. */
  readonly storages: Client<typeof StorageService>;
  /** Tenants. */
  readonly tenants: Client<typeof TenantService>;
}

/**
 * Makes an interceptor that sends the key with every call as
 * `Authorization: Bearer <key>`, the credential the server reads.
 */
export function keyInterceptor(key: string): Interceptor {
  if (!key) throw new TypeError("a key is required");
  const value = `Bearer ${key}`;
  return (next) => (req) => {
    req.header.set("authorization", value);
    return next(req);
  };
}

/**
 * An interceptor that turns every failure into a {@link CoriaError}, for a
 * unary call and for a server stream at the message that fails.
 */
export const errorInterceptor: Interceptor = (next) => async (req) => {
  let res;
  try {
    res = await next(req);
  } catch (err) {
    throw CoriaError.wrap(err);
  }
  if (!res.stream) return res;
  const messages = res.message;
  return {
    ...res,
    message: (async function* () {
      try {
        yield* messages;
      } catch (err) {
        throw CoriaError.wrap(err);
      }
    })(),
  };
};

/**
 * The interceptors a transport needs for {@link createClients}: the key and
 * the error conversion. Pass them to a transport you build yourself.
 */
export function coriaInterceptors(key: string): Interceptor[] {
  return [errorInterceptor, keyInterceptor(key)];
}

/** Makes one client for each service, all over the given transport. */
export function createClients(transport: Transport): CoriaClients {
  return {
    access: createClient(AccessService, transport),
    audit: createClient(AuditService, transport),
    catalog: createClient(CatalogService, transport),
    data: createClient(DataService, transport),
    metadata: createClient(MetadataService, transport),
    monitor: createClient(MonitorService, transport),
    quarantine: createClient(QuarantineService, transport),
    receivers: createClient(ReceiverService, transport),
    receptions: createClient(ReceptionService, transport),
    settings: createClient(SettingsService, transport),
    storages: createClient(StorageService, transport),
    tenants: createClient(TenantService, transport),
  };
}
