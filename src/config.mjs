// Loads and validates el.config.mjs from the current working directory.
//
// The config contract is deliberately narrow: el knows how to fork a Neon
// branch, verify the role it hands out isn't RLS-exempt, create a Hyperdrive
// config, and deploy named Cloudflare Workers. It knows nothing about your
// application — auth schemes, JWT signing, seed data, and which URLs are
// worth opening in a browser are all yours to supply via hooks.

import path from "node:path";
import { pathToFileURL } from "node:url";

function fail(message) {
  throw new Error(`Invalid el.config.mjs: ${message}`);
}

export async function loadConfig() {
  const configPath = path.join(process.cwd(), "el.config.mjs");
  let mod;
  try {
    mod = await import(pathToFileURL(configPath).href);
  } catch (error) {
    if (error.code === "ERR_MODULE_NOT_FOUND") {
      throw new Error(
        "No el.config.mjs found in the current directory. See the README for the config shape.",
      );
    }
    throw error;
  }
  const config = mod.default;
  validate(config);
  return config;
}

export function validate(config) {
  if (typeof config !== "object" || config === null) {
    fail("default export must be an object");
  }

  const { neon, services } = config;
  if (typeof neon?.project !== "string" || neon.project === "") {
    fail("neon.project must be a non-empty string");
  }
  if (typeof neon?.database !== "string" || neon.database === "") {
    fail("neon.database must be a non-empty string");
  }
  if (typeof neon?.appRole !== "string" || neon.appRole === "") {
    fail("neon.appRole must be a non-empty string (the least-privilege role Hyperdrive connects as)");
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
  }

  for (const hook of ["configure", "seed", "open"]) {
    if (config[hook] !== undefined && typeof config[hook] !== "function") {
      fail(`${hook} must be a function if provided`);
    }
  }
}
