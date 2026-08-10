// Deploys a service's wrangler.jsonc under a different name/vars/bindings
// without touching the committed file.

import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";

/**
 * Finds a locally-installed wrangler by walking up from serviceDir, the way
 * Node itself resolves node_modules. Deliberately does NOT fall back to
 * `npx wrangler`: npx does not consult PATH, and in a directory with no
 * local install it silently downloads and runs the latest, unpinned,
 * unverified wrangler from the registry — in a process already holding
 * Cloudflare and Neon credentials. A registry compromise of wrangler would
 * land directly on those. Failing here with a clear message is the
 * intentional trade for that risk, not an oversight.
 */
function findWranglerBin(startDir) {
  let dir = path.resolve(startDir);
  const binName = process.platform === "win32" ? "wrangler.cmd" : "wrangler";
  while (true) {
    const candidate = path.join(dir, "node_modules", ".bin", binName);
    if (existsSync(candidate)) return candidate;
    const parent = path.dirname(dir);
    if (parent === dir) {
      throw new Error(
        `Could not find a locally-installed wrangler above ${startDir}. ` +
          `Add wrangler as a devDependency of this service (or the workspace root) — ` +
          `el will not fall back to \`npx wrangler\`, which would run an unpinned version.`,
      );
    }
    dir = parent;
  }
}

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
 * Runs a wrangler subcommand against a mutated copy of a service's config,
 * written alongside the real one and removed immediately after. Shared by
 * deployWithConfig (below) and applyD1Migrations.
 *
 * Must live inside serviceDir, not a system temp directory — wrangler
 * resolves `main` and other relative paths against the CONFIG FILE's
 * location, not the process cwd. A temp file elsewhere makes `./src/index.ts`
 * resolve to a path that doesn't exist.
 *
 * mode 0o600 + the "wx" flag are both load-bearing: 0o600 keeps the file
 * unreadable to other local users for the (brief) window it exists, and
 * "wx" refuses to follow a pre-existing symlink at that path (verified —
 * writeFileSync follows symlinks by default) rather than writing through it.
 * Signal handlers exist because `finally` alone does not run on Ctrl-C —
 * execFileSync blocks the event loop, so SIGINT is deferred until the sync
 * call returns and Node's default disposition then terminates without
 * unwinding the JS stack, leaving the file on disk otherwise.
 *
 * This file can contain whatever `configure()` returned for `vars` — plain
 * Worker vars, not secrets, but still worth not leaving lying around inside
 * what's usually a git working tree. Consumers should gitignore
 * `.el-deploy-*.json`.
 */
function runWithTempConfig(serviceDir, config, wranglerArgs) {
  const wrangler = findWranglerBin(serviceDir);
  const configPath = path.join(serviceDir, `.el-deploy-${String(process.pid)}.json`);
  writeFileSync(configPath, JSON.stringify(config, null, 2), { mode: 0o600, flag: "wx" });

  const cleanup = () => rmSync(configPath, { force: true });
  process.once("SIGINT", cleanup);
  process.once("SIGTERM", cleanup);
  process.once("uncaughtException", cleanup);

  try {
    execFileSync(wrangler, [...wranglerArgs, "--config", configPath], {
      cwd: serviceDir,
      stdio: "inherit",
    });
  } finally {
    cleanup();
    process.off("SIGINT", cleanup);
    process.off("SIGTERM", cleanup);
    process.off("uncaughtException", cleanup);
  }
}

export function deployWithConfig(serviceDir, config) {
  runWithTempConfig(serviceDir, config, ["deploy"]);
}

/**
 * Applies migrations_dir/migrations_pattern (from the base config's matching
 * d1_databases entry) to a freshly-provisioned ephemeral D1 database — the
 * D1 equivalent of what Neon branching gives Postgres for free. A no-op if
 * the base entry declares no migrations_dir.
 */
export function applyD1Migrations(serviceDir, baseConfig, binding, databaseName, databaseId) {
  const baseEntry = (baseConfig.d1_databases ?? []).find((d) => d.binding === binding);
  if (!baseEntry?.migrations_dir) return;

  const migrationsConfig = {
    main: baseConfig.main,
    compatibility_date: baseConfig.compatibility_date,
    d1_databases: [
      {
        binding,
        database_name: databaseName,
        database_id: databaseId,
        migrations_dir: baseEntry.migrations_dir,
        ...(baseEntry.migrations_pattern ? { migrations_pattern: baseEntry.migrations_pattern } : {}),
      },
    ],
  };
  runWithTempConfig(serviceDir, migrationsConfig, [
    "d1",
    "migrations",
    "apply",
    databaseName,
    "--remote",
  ]);
}

export function putSecret(serviceDir, workerName, secretName, value) {
  const wrangler = findWranglerBin(serviceDir);
  execFileSync(wrangler, ["secret", "put", secretName, "--name", workerName], {
    cwd: serviceDir,
    input: value,
    stdio: ["pipe", "inherit", "inherit"],
  });
}

export function deleteWorker(serviceDir, workerName) {
  try {
    const wrangler = findWranglerBin(serviceDir);
    execFileSync(wrangler, ["delete", "--name", workerName, "--force"], {
      cwd: serviceDir,
      stdio: "inherit",
    });
  } catch (error) {
    console.warn(`  (delete of ${workerName} failed or it didn't exist — continuing)`, String(error.message ?? error));
  }
}
