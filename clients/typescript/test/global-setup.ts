import { execFileSync } from "node:child_process";
import { existsSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import type { TestProject } from "vitest/node";

declare module "vitest" {
  export interface ProvidedContext {
    /** Path of the coria binary the tests start servers from. */
    binary: string;
    /** Path of the repository, where api/not-served.txt is. */
    repo: string;
  }
}

/** Builds the coria binary once, static as the release is, for every test file. */
export default function setup(project: TestProject): () => void {
  const repo = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..", "..");
  // A short directory: the local socket's path must fit a Unix socket address.
  const base = existsSync("/tmp") ? "/tmp" : tmpdir();
  const work = mkdtempSync(join(base, "cor-ts."));
  const binary = join(work, "coria");
  execFileSync("go", ["build", "-o", binary, "./cmd/coria"], {
    cwd: repo,
    env: { CGO_ENABLED: "0", GOTOOLCHAIN: "go1.26.11", ...process.env },
    stdio: "inherit",
    timeout: 600_000,
  });
  project.provide("binary", binary);
  project.provide("repo", repo);
  return () => rmSync(work, { recursive: true, force: true });
}
