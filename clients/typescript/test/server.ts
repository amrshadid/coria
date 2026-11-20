import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { closeSync, existsSync, mkdirSync, mkdtempSync, openSync, readFileSync, rmSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";

/** Answers a loopback port nobody listens on. */
export function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const s = createServer();
    s.once("error", reject);
    s.listen(0, "127.0.0.1", () => {
      const { port } = s.address() as { port: number };
      s.close(() => resolve(port));
    });
  });
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/**
 * A running server on a temporary installation, with an admin key for it.
 * Everything is synthetic: a new installation, a key made on its local
 * socket, no DICOM data.
 */
export class Server {
  readonly work: string;
  readonly data: string;
  readonly env: NodeJS.ProcessEnv;
  port = 0;
  key = "";
  private process?: ChildProcess;

  private constructor(readonly binary: string) {
    this.work = mkdtempSync(join(existsSync("/tmp") ? "/tmp" : tmpdir(), "cor-ts-srv."));
    this.data = join(this.work, "d");
    mkdirSync(this.data, { mode: 0o700 });
    this.env = { ...process.env, CORIA_DATA_DIR: this.data };
  }

  /** Initializes an installation, starts the server on a free loopback port and makes the admin key. */
  static async start(binary: string, ...extra: string[]): Promise<Server> {
    const server = new Server(binary);
    server.cli("init");
    await server.listen(...extra);
    server.key = server.makeKey("admin", true);
    return server;
  }

  /** Runs a coria command on the installation's local socket and answers its standard output. */
  cli(...args: string[]): string {
    const done = spawnSync(this.binary, args, { env: this.env, encoding: "utf8", timeout: 60_000 });
    if (done.status !== 0) throw new Error(`coria ${args.join(" ")}: ${done.stderr}`);
    return done.stdout;
  }

  /** Makes a key on the local socket, and gives it admin on the installation if asked. */
  makeKey(holder: string, admin = false): string {
    const key = this.cli("key", "create", "--holder", holder, "--no-expiry").split("\n")[0]!.trim();
    if (!key.startsWith("cor_")) throw new Error("the first line of key create is the key");
    if (admin) this.cli("grant", "set", key.split("_")[1]!, "--role", "admin", "--installation");
    return key;
  }

  /** The address of the network listener, `127.0.0.1:<port>`. */
  get address(): string {
    return `127.0.0.1:${this.port}`;
  }

  /** Starts the server on a new free port, with extra serve flags, and waits until it answers. */
  async listen(...extra: string[]): Promise<void> {
    this.port = await freePort();
    const logPath = join(this.work, "serve.log");
    const log = openSync(logPath, "a");
    const child = spawn(this.binary, ["serve", "--listen", this.address, ...extra], {
      env: this.env,
      stdio: ["ignore", log, log],
    });
    closeSync(log);
    this.process = child;
    const sock = join(this.data, "run", "coria.sock");
    for (let i = 0; i < 300; i++) {
      if (child.exitCode !== null) throw new Error(`coria serve exited: ${readFileSync(logPath, "utf8")}`);
      if (existsSync(sock)) {
        try {
          this.cli("whoami");
          return;
        } catch {
          // The socket exists before the server answers on it.
        }
      }
      await sleep(50);
    }
    throw new Error("coria serve did not answer in 15 seconds");
  }

  /** Stops the server the way an operator does, and waits for it. */
  async stop(): Promise<void> {
    const child = this.process;
    if (!child || child.exitCode !== null) return;
    const exited = new Promise((resolve) => child.once("exit", resolve));
    child.kill("SIGTERM");
    await exited;
  }

  /** Stops the server and removes the installation. */
  async close(): Promise<void> {
    await this.stop();
    rmSync(this.work, { recursive: true, force: true });
  }
}
