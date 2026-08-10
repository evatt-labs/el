// Deploys a service's wrangler.jsonc under a different name/vars/bindings
// without touching the committed file.

import { execFileSync } from "node:child_process";
import { readFileSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";

/**
 * Strips full-line `//` comments from JSONC. Only safe for configs whose
 * comments are always on their own line — never appended after a value on
 * the same line, which would risk eating a `://` inside a URL string. Most
 * hand-written wrangler.jsonc files follow that convention; if a consumer's
 * doesn't, this will mangle it, so keep comments on their own line.
 */
function stripJsonComments(text) {
  return text
    .split("\n")
    .filter((line) => !line.trim().startsWith("//"))
    .join("\n");
}

export function loadWranglerConfig(serviceDir) {
  const raw = readFileSync(path.join(serviceDir, "wrangler.jsonc"), "utf8");
  return JSON.parse(stripJsonComments(raw));
}

/**
 * Deploys a mutated copy of a service's config, written alongside the real
 * one and removed immediately after.
 *
 * Must live inside serviceDir, not a system temp directory — wrangler
 * resolves `main` and other relative paths against the CONFIG FILE's
 * location, not the process cwd. A temp file elsewhere makes `./src/index.ts`
 * resolve to a path that doesn't exist.
 */
export function deployWithConfig(serviceDir, config) {
  const configPath = path.join(serviceDir, `.el-deploy-${String(process.pid)}.json`);
  writeFileSync(configPath, JSON.stringify(config, null, 2));
  try {
    execFileSync("npx", ["wrangler", "deploy", "--config", configPath], {
      cwd: serviceDir,
      stdio: "inherit",
    });
  } finally {
    rmSync(configPath, { force: true });
  }
}

export function putSecret(serviceDir, workerName, secretName, value) {
  execFileSync("npx", ["wrangler", "secret", "put", secretName, "--name", workerName], {
    cwd: serviceDir,
    input: value,
    stdio: ["pipe", "inherit", "inherit"],
  });
}

export function deleteWorker(serviceDir, workerName) {
  try {
    execFileSync("npx", ["wrangler", "delete", "--name", workerName, "--force"], {
      cwd: serviceDir,
      stdio: "inherit",
    });
  } catch (error) {
    console.warn(`  (delete of ${workerName} failed or it didn't exist — continuing)`, String(error.message ?? error));
  }
}

/** Creates a Hyperdrive config and returns its id, parsed from wrangler's own output. */
export function createHyperdriveConfig(serviceDir, name, connectionString) {
  const output = execFileSync(
    "npx",
    ["wrangler", "hyperdrive", "create", name, `--connection-string=${connectionString}`],
    { cwd: serviceDir, encoding: "utf8" },
  );
  process.stdout.write(output);
  const match = /Created new Hyperdrive PostgreSQL config: ([a-f0-9]+)/.exec(output);
  if (!match) {
    throw new Error("Could not parse Hyperdrive config id from wrangler output");
  }
  return match[1];
}
