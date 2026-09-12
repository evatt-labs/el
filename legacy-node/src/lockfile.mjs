// Per-environment lockfile: `kraai up` records, incrementally, exactly what it
// provisions. `kraai down` reads it back and deletes the union of what the lock
// says was created and what kraai.config.mjs currently declares.
//
// This exists because `down` used to derive what to delete only from the
// CURRENT config. If a binding is removed from kraai.config.mjs between an `up`
// and the matching `down` (the GitHub Action's down-then-up re-run does
// exactly this across pushes), the resource that binding once provisioned is
// never looked up and never deleted. The lock is the durable record that
// survives a config edit; merging it with the current config means neither
// side can cause a regression: no lockfile still falls back to today's
// config-only behavior, and a stale/edited config can't hide a resource the
// lock remembers.

import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import { resourceName } from "./names.mjs";

export function lockPath(cwd, name) {
  return path.join(cwd, ".kraai", `${name}.lock.json`);
}

/**
 * The initial shape `up()` writes before any provisioning happens, so a run
 * that fails immediately after still leaves a lockfile naming the
 * environment, rather than nothing at all. `database` and `services` fill in
 * as provisioning proceeds; `writeLock` is called again each time either one
 * gains an entry.
 */
export function emptyLock({ name, elVersion, accountId, subdomain }) {
  return {
    lockfileVersion: 1,
    name,
    createdAt: new Date().toISOString(),
    elVersion,
    accountId,
    subdomain,
    database: null,
    services: {},
  };
}

/**
 * Writes the lock, creating `.kraai/` if this is the first write for this
 * environment. Safe to call repeatedly with a growing object: `up()` calls
 * this after every meaningful step (subdomain resolved, database
 * provisioned, each service deployed) so a partial `up` leaves an accurate
 * partial record instead of nothing.
 */
export function writeLock(cwd, lock) {
  mkdirSync(path.join(cwd, ".kraai"), { recursive: true });
  writeFileSync(lockPath(cwd, lock.name), `${JSON.stringify(lock, null, 2)}\n`);
}

/**
 * Reads the lock for an environment, or returns undefined if there isn't
 * one (a pre-lockfile environment, or one `down` already tore down). A file
 * that exists but fails to parse, or whose lockfileVersion isn't 1, throws
 * instead of being treated as absent: silently ignoring a corrupt or
 * future-version lockfile would let `down` regress to config-only deletion
 * with nothing telling anyone why.
 */
export function readLock(cwd, name) {
  const file = lockPath(cwd, name);
  let raw;
  try {
    raw = readFileSync(file, "utf8");
  } catch (error) {
    if (error.code === "ENOENT") return undefined;
    throw error;
  }

  let lock;
  try {
    lock = JSON.parse(raw);
  } catch (error) {
    throw new Error(`Lockfile at ${file} is not valid JSON: ${error.message}`);
  }
  if (lock?.lockfileVersion !== 1) {
    throw new Error(
      `Lockfile at ${file} has lockfileVersion ${JSON.stringify(lock?.lockfileVersion)}, expected 1. ` +
        "Refusing to guess how to read it rather than silently falling back to config-only deletion.",
    );
  }
  return lock;
}

/** Removes the lockfile if present. A no-op, not an error, if it's already gone. */
export function deleteLock(cwd, name) {
  rmSync(lockPath(cwd, name), { force: true });
}

/**
 * Dedups a lock-recorded resource list and a config-derived one into one
 * list, by name. `configEntries` is already `{ binding, name }` pairs (see
 * `mergeResources` below, which builds them); this just does the dedup.
 */
function mergeType(lockEntries, configEntries) {
  const byName = new Map();
  for (const entry of lockEntries ?? []) {
    if (!byName.has(entry.name)) byName.set(entry.name, { binding: entry.binding, name: entry.name });
  }
  for (const entry of configEntries ?? []) {
    if (!byName.has(entry.name)) byName.set(entry.name, { binding: entry.binding, name: entry.name });
  }
  return [...byName.values()];
}

/**
 * Returns the union of resources to delete for one service: what the lock
 * recorded `up()` actually created, plus what the current config declares,
 * deduplicated by resource name within each type. This is the actual fix for
 * the leak: a binding present in the lock but no longer in config (removed
 * between `up` and `down`) still appears, and a binding declared in config
 * with no lock at all (pre-lockfile environment) still appears too, so
 * neither side of the merge can cause `down` to miss something the other
 * side would have caught.
 *
 * `lockService` is a lockfile's `services[key]` entry (or undefined).
 * `configService` is the current config's service entry (or undefined) -
 * its `d1`/`kv`/`r2`/`queues` arrays only carry bindings, not names, so
 * `environmentName` is required to derive the same deterministic names
 * `up()` used when it created them (`resourceName`, keyed on
 * `configService.key`). This is a deliberate three-argument signature
 * rather than the two-argument shape resource lists alone would suggest:
 * without the environment name there is no way to turn a config binding
 * into the name that was actually provisioned for it.
 */
export function mergeResources(lockService, configService, environmentName) {
  const serviceKey = configService?.key;
  const namesFor = (entries) =>
    (entries ?? []).map((entry) => ({
      binding: entry.binding,
      name: resourceName(environmentName, serviceKey, entry.binding),
    }));

  return {
    d1: mergeType(lockService?.resources?.d1, namesFor(configService?.d1)),
    kv: mergeType(lockService?.resources?.kv, namesFor(configService?.kv)),
    r2: mergeType(lockService?.resources?.r2, namesFor(configService?.r2)),
    queues: mergeType(lockService?.resources?.queues, namesFor(configService?.queues)),
  };
}
