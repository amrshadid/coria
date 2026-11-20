# @coria/client

The TypeScript client of a CORIA archive's API. Its code is generated from the
protobuf contract in `api/proto` with `@bufbuild/protoc-gen-es`, so it has every
service and message the contract has; a thin layer on top opens the
connection, sends the key and turns failures into one error class.

It speaks the Connect protocol by default, which works from a browser over
plain HTTP/1.1 with `fetch`. On Node it can also speak gRPC or gRPC-web.

## Install

```sh
npm install ./clients/typescript
```

It needs Node 22 or later (the Connect package for Node does), or a browser.
Its dependencies are the Connect packages and `@bufbuild/protobuf`.

## Use in a browser

```ts
import { createCoriaClient, pages } from "@coria/client";

const coria = createCoriaClient({ baseUrl: "https://coria.example.org:8443", key });

const me = await coria.access.whoAmI({});
for await (const tenant of pages(coria.tenants.listTenants, { pageSize: 50 }, "tenants")) {
  console.log(tenant.name);
}
```

A browser on another origin can call the server only if the server, or a proxy
in front of it, answers the browser's cross-origin (CORS) check; CORIA's server
does not add CORS headers itself. The key is a secret: do not put one in a page
that others can read.

## Use on Node

```ts
import { CoriaError, Code, createCoriaClient } from "@coria/client/node";
import { readFileSync } from "node:fs";

const coria = createCoriaClient({
  baseUrl: "https://coria.example.org:8443",
  key,
  protocol: "grpc", // "connect" (the default), "grpc" or "grpc-web"
  nodeOptions: { ca: readFileSync("ca.pem") }, // for a private certificate authority
});

try {
  await coria.tenants.getTenant({ name: "pilot" });
} catch (err) {
  if (err instanceof CoriaError && err.code === Code.NotFound) {
    console.log("no such tenant; request id", err.requestId);
  } else {
    throw err;
  }
}
```

## What it offers

- `createCoriaClient({ baseUrl, key, transport?, timeoutMs? })` returns one
  client per service: `access`, `audit`, `catalog`, `data`, `metadata`,
  `monitor`, `quarantine`, `receivers`, `receptions`, `settings`, `storages`,
  `tenants`. Each method takes the request (a plain object of its fields) and
  returns the response message; a server-streaming method returns an async
  iterable. `key` (`cor_<id>_<secret>`) is sent with every call as
  `Authorization: Bearer <key>`. `timeoutMs` defaults to 60000; 0 means none.
  On Node, `protocol`, `httpVersion` (`"1.1"` or `"2"`) and `nodeOptions` choose
  how to connect.
- `pages(call, request, field)` yields the items of a list call across all its
  pages, following `nextPageToken` until it is empty. The request is not
  changed.
- A failed call throws `CoriaError`, a Connect error with `code` (a `Code`),
  `rawMessage` and `requestId` (the id to look for in the audit log; empty when
  the server sent none, for example when the connection failed). A handshake
  that fails certificate checks has the code `Internal` and the TLS library's
  message.
- To bring your own transport, build it with `coriaInterceptors(key)` among
  its interceptors and pass it as `transport`.
- Messages, enums and service descriptors are in the generated modules, for
  example `import { DatabaseType } from "@coria/client/gen/tenant_pb"`. The
  contract's comments say what each method needs and answers.

The entry `@coria/client` imports no Node module, so a bundler can use it for a
browser; `@coria/client/node` is for Node.

## Develop

The generated code is in `src/gen`, checked in and never edited by hand.

```sh
cd clients/typescript
npm ci
npm run generate         # make it again
npm run generate:check   # fail if it is out of date
npm run typecheck
npm test                 # needs Go and, for the TLS test, openssl
```

The tests build the `coria` binary, start it on a temporary installation and
call it with a key made on that installation, over each protocol. They use no
DICOM data. `npm run build` writes the compiled package to `dist/`, which is
not checked in.
