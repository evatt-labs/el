// The database provider registry. `database.provider` in el.config.mjs
// resolves against this map when given as a string; a config can also
// supply its own provider object directly, bypassing the registry entirely.
// Neon is the only built-in today: this file is the one place a future
// built-in provider gets added.

import * as neon from "./neon.mjs";

export const PROVIDERS = {
  neon,
};

/**
 * Resolves a `database.provider` value to an actual provider object. Shared
 * by config validation (src/config.mjs, which wraps whatever this throws
 * with the "Invalid el.config.mjs" prefix) and up.mjs/down.mjs (which call
 * this again at runtime against a config that has already passed
 * validation (cheap, and avoids threading the resolved provider through
 * loadConfig's return value).
 */
export function resolveProvider(provider) {
  if (typeof provider === "string") {
    // Object.hasOwn, not `PROVIDERS[provider]`: a plain object literal
    // inherits from Object.prototype, so a string like "constructor" or
    // "toString" would otherwise resolve to something truthy that isn't a
    // registered provider at all, skip this error, and fail later with an
    // unrelated TypeError deep inside up.mjs/down.mjs.
    if (!Object.hasOwn(PROVIDERS, provider)) {
      throw new Error(
        `Unknown database provider "${provider}". Known providers: ${Object.keys(PROVIDERS).join(", ")}`,
      );
    }
    return PROVIDERS[provider];
  }
  if (typeof provider === "object" && provider !== null) {
    if (typeof provider.name !== "string" || provider.name === "") {
      throw new Error("database.provider object must have a non-empty string name");
    }
    if (typeof provider.up !== "function" || typeof provider.down !== "function") {
      throw new Error(`database.provider "${provider.name}" must implement up() and down() functions`);
    }
    if (provider.requiredEnv !== undefined) {
      const isValidRequiredEnv =
        Array.isArray(provider.requiredEnv) &&
        provider.requiredEnv.every((entry) => typeof entry === "string" && entry !== "");
      if (!isValidRequiredEnv) {
        throw new Error(
          `database.provider "${provider.name}" requiredEnv must be an array of non-empty strings`,
        );
      }
    }
    return provider;
  }
  throw new Error("database.provider must be a string naming a built-in provider or a provider object");
}

/**
 * Normalizes whatever a provider's up() resolved to into the shape up.mjs
 * relies on everywhere downstream: bindings() always a callable function
 * that always returns an object, seed always an object, summary always an
 * array, lock always an object. A provider is free to return `{}` or nothing
 * at all from up() (no hyperdrive support, nothing for seed(), nothing for
 * the summary, nothing for the lockfile), and its bindings() is free to
 * return nothing for a service it has no binding for. Without this, up.mjs
 * would call `undefined()` or read `.hyperdrive` off `undefined` the first
 * time it asked a compliant-but-minimal provider for a service's bindings,
 * after that provider had already provisioned real infrastructure.
 *
 * `lock` defaults to `{}` the same way `seed` does: additive, not breaking,
 * so an existing provider that predates the lockfile still works, it just
 * gives `down()` nothing extra to key off of and el falls back to its
 * name-based lookups for that provider's resources.
 */
export function normalizeProviderResult(result, provider) {
  if (result?.bindings !== undefined && typeof result.bindings !== "function") {
    throw new Error(`database provider "${provider.name}" returned a non-function "bindings" from up()`);
  }
  const bindingsFn = result?.bindings;
  return {
    bindings: (service) => {
      const bindings = bindingsFn ? bindingsFn(service) : undefined;
      return typeof bindings === "object" && bindings !== null ? bindings : {};
    },
    seed: result?.seed ?? {},
    summary: result?.summary ?? [],
    lock: result?.lock ?? {},
  };
}
