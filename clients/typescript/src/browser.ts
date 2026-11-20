import { createConnectTransport } from "@connectrpc/connect-web";
import type { Transport } from "@connectrpc/connect";

import { assertSecure, coriaInterceptors, createClients, type CoriaClients } from "./core.js";

export { CoriaError, REQUEST_ID_HEADER } from "./errors.js";
export { pages } from "./pages.js";
export { coriaInterceptors, createClients, errorInterceptor, keyInterceptor } from "./core.js";
export type { CoriaClients } from "./core.js";
export { Code } from "@connectrpc/connect";

/** Options of {@link createCoriaClient} for a browser or any runtime with fetch. */
export type CoriaClientOptions =
  | {
      /** The server's address, such as `https://coria.example.org:8443`. */
      baseUrl: string;
      /** The API key, `cor_<id>_<secret>`. */
      key: string;
      /**
       * Milliseconds a unary call may take; 60000 when absent, 0 for no
       * limit. A server stream has no deadline unless its call passes one.
       */
      timeoutMs?: number;
      /**
       * Send the key over plain `http:` to a host that is not this machine.
       * Off by default: such an address is refused, because the key would
       * travel in clear.
       */
      allowInsecure?: boolean;
      transport?: undefined;
    }
  | {
      /**
       * A transport you built, whose interceptors include
       * `coriaInterceptors(key)`. `baseUrl` and `key` are then not used.
       */
      transport: Transport;
      baseUrl?: string;
      key?: string;
      allowInsecure?: undefined;
      /** The deadline of a unary call made without its own; see above. */
      timeoutMs?: number;
    };

/**
 * Makes one typed client per service of a CORIA server, over the Connect
 * protocol with `fetch`, which works from a browser over plain HTTP/1.1.
 * Every failure is thrown as a {@link CoriaError}. For gRPC or gRPC-web from
 * Node, use `coria/node`.
 *
 * A browser may call a server on another origin only if the server (or a
 * proxy in front of it) answers the browser's cross-origin check.
 */
export function createCoriaClient(options: CoriaClientOptions): CoriaClients {
  const { timeoutMs } = options;
  if (options.transport) return createClients(options.transport, { timeoutMs });
  if (!options.baseUrl) throw new TypeError("baseUrl is required");
  assertSecure(options.baseUrl, options.allowInsecure);
  return createClients(
    createConnectTransport({ baseUrl: options.baseUrl, interceptors: coriaInterceptors(options.key) }),
    { timeoutMs },
  );
}
