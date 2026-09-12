// Loads and validates kraai.config.mjs from the current working directory.
//
// The config contract is deliberately narrow: kraai knows how to deploy named
// Cloudflare Workers, provision per-service D1/KV/R2/Queues resources, and,
// if a `database` provider is configured, provision that database ahead of
// the deploy. With no `database` block, kraai is D1-only: no database
// credentials, no Hyperdrive. It knows nothing about your application:
// auth schemes, JWT signing, seed data, and which URLs are worth opening in
// a browser are all yours to supply via hooks.

import { existsSync } from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { resolveProvider } from "./providers/index.mjs";

function fail(message) {
  throw new Error(`Invalid kraai.config.mjs: ${message}`);
}

export async function loadConfig() {
  const configPath = path.join(process.cwd(), "kraai.config.mjs");
  if (!existsSync(configPath)) {
    throw new Error(
      "No kraai.config.mjs found in the current directory. See the README for the config shape.",
    );
  }
  // Checked existence first specifically so an import error INSIDE a config
  // that does exist (a missing dependency, a syntax error) surfaces as
  // itself, rather than being misreported as "no config found" — both raise
  // the same ERR_MODULE_NOT_FOUND when Node can't resolve something.
  const mod = await import(pathToFileURL(configPath).href);
  const config = mod.default;
  validate(config);
  return config;
}

export function validate(config) {
  if (typeof config !== "object" || config === null) {
    fail("default export must be an object");
  }

  if (config.neon !== undefined) {
    fail(
      `the top-level "neon" key was removed in 0.4.0, move it to ` +
        `database: { provider: "neon", project, database, appRole }`,
    );
  }

  const { database, services } = config;

  if (database !== undefined) {
    if (typeof database !== "object" || database === null || Array.isArray(database)) {
      fail("database must be an object if provided");
    }
    const { provider: providerRef, ...options } = database;
    let provider;
    try {
      provider = resolveProvider(providerRef);
    } catch (error) {
      fail(error.message);
    }
    if (typeof provider.validate === "function") {
      provider.validate(options);
    }
  }

  if (!Array.isArray(services) || services.length === 0) {
    fail("services must be a non-empty array");
  }
  const seenKeys = new Set();
  for (const service of services) {
    if (typeof service?.key !== "string" || !/^[a-z][a-z0-9-]*$/.test(service.key)) {
      fail(`each service.key must be a lowercase, hyphenated identifier (got ${JSON.stringify(service?.key)})`);
    }
    if (seenKeys.has(service.key)) fail(`duplicate service.key "${service.key}"`);
    seenKeys.add(service.key);
    if (typeof service.dir !== "string" || service.dir === "") {
      fail(`service "${service.key}": dir must be a non-empty string`);
    }
    if (service.hyperdrive && typeof service.hyperdrive.binding !== "string") {
      fail(`service "${service.key}": hyperdrive.binding must be a string when hyperdrive is set`);
    }
    if (service.hyperdrive && database === undefined) {
      fail(
        `service "${service.key}": hyperdrive requires a database provider, add a top-level ` +
          `"database" block to kraai.config.mjs`,
      );
    }
    if (
      service.unsafeInheritBindings !== undefined &&
      typeof service.unsafeInheritBindings !== "boolean"
    ) {
      fail(`service "${service.key}": unsafeInheritBindings must be a boolean if set`);
    }

    for (const field of ["d1", "kv", "r2"]) {
      if (service[field] === undefined) continue;
      if (!Array.isArray(service[field])) {
        fail(`service "${service.key}": ${field} must be an array if provided`);
      }
      for (const entry of service[field]) {
        if (typeof entry?.binding !== "string" || entry.binding === "") {
          fail(`service "${service.key}": each ${field} entry needs a non-empty binding`);
        }
      }
    }

    if (service.queues !== undefined) {
      if (!Array.isArray(service.queues)) {
        fail(`service "${service.key}": queues must be an array if provided`);
      }
      for (const entry of service.queues) {
        if (typeof entry?.binding !== "string" || entry.binding === "") {
          fail(`service "${service.key}": each queues entry needs a non-empty binding`);
        }
        if (entry.consumer !== undefined && typeof entry.consumer !== "boolean") {
          fail(`service "${service.key}": queues entry "${entry.binding}".consumer must be a boolean if set`);
        }
      }
    }
  }

  for (const hook of ["configure", "seed", "open"]) {
    if (config[hook] !== undefined && typeof config[hook] !== "function") {
      fail(`${hook} must be a function if provided`);
    }
  }
}
