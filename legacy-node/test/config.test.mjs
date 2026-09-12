import { describe, expect, it } from "vitest";
import { validate } from "../src/config.mjs";

function validConfig(overrides = {}) {
  return {
    services: [{ key: "api", dir: "packages/api" }],
    ...overrides,
  };
}

function neonDatabase(overrides = {}) {
  return { provider: "neon", project: "acme", database: "neondb", appRole: "app_user", ...overrides };
}

describe("validate", () => {
  it("accepts a minimal valid config with no database block", () => {
    expect(() => validate(validConfig())).not.toThrow();
  });

  it("accepts a config with a database block", () => {
    expect(() => validate(validConfig({ database: neonDatabase() }))).not.toThrow();
  });

  it("accepts hooks and a hyperdrive-bound service backed by a database provider", () => {
    expect(() =>
      validate(
        validConfig({
          database: neonDatabase(),
          services: [{ key: "api", dir: "packages/api", hyperdrive: { binding: "HYPERDRIVE" } }],
          configure: async () => ({}),
          seed: async () => ({}),
          open: () => [],
        }),
      ),
    ).not.toThrow();
  });

  it.each([
    ["non-object", "not an object"],
    ["null", null],
  ])("rejects a %s config", (_label, config) => {
    expect(() => validate(config)).toThrow(/must be an object/);
  });

  it("rejects a config still using the removed top-level neon key", () => {
    expect(() =>
      validate(
        validConfig({
          neon: { project: "acme", database: "neondb", appRole: "app_user" },
        }),
      ),
    ).toThrow(/top-level "neon" key was removed/);
  });

  it.each([
    ["a string", "neon"],
    ["an array", []],
  ])("rejects a database block that is %s", (_label, database) => {
    expect(() => validate(validConfig({ database }))).toThrow(/database must be an object/);
  });

  it("rejects an unknown provider name and names the known providers", () => {
    expect(() =>
      validate(validConfig({ database: neonDatabase({ provider: "does-not-exist" }) })),
    ).toThrow(/Unknown database provider "does-not-exist".*neon/);
  });

  it("rejects a provider name that collides with an inherited Object.prototype property", () => {
    expect(() =>
      validate(validConfig({ database: neonDatabase({ provider: "constructor" }) })),
    ).toThrow(/Unknown database provider "constructor".*neon/);
  });

  it("rejects a provider object missing up", () => {
    expect(() =>
      validate(
        validConfig({
          database: { provider: { name: "custom", down: async () => {} } },
        }),
      ),
    ).toThrow(/must implement up\(\) and down\(\)/);
  });

  it("rejects a provider object missing down", () => {
    expect(() =>
      validate(
        validConfig({
          database: { provider: { name: "custom", up: async () => {} } },
        }),
      ),
    ).toThrow(/must implement up\(\) and down\(\)/);
  });

  it("accepts a provider object with name, up, down, and validate", () => {
    expect(() =>
      validate(
        validConfig({
          database: {
            provider: {
              name: "custom",
              up: async () => ({}),
              down: async () => {},
              validate: () => {},
            },
          },
        }),
      ),
    ).not.toThrow();
  });

  it("rejects a provider object whose requiredEnv is a string instead of an array", () => {
    expect(() =>
      validate(
        validConfig({
          database: {
            provider: {
              name: "custom",
              requiredEnv: "MY_KEY",
              up: async () => ({}),
              down: async () => {},
            },
          },
        }),
      ),
    ).toThrow(/requiredEnv must be an array of non-empty strings/);
  });

  it("accepts a provider object whose requiredEnv is an array of strings", () => {
    expect(() =>
      validate(
        validConfig({
          database: {
            provider: {
              name: "custom",
              requiredEnv: ["MY_KEY", "MY_OTHER_KEY"],
              up: async () => ({}),
              down: async () => {},
            },
          },
        }),
      ),
    ).not.toThrow();
  });

  it.each([
    ["missing database.project", validConfig({ database: neonDatabase({ project: undefined }) }), /project must be/],
    ["empty database.project", validConfig({ database: neonDatabase({ project: "" }) }), /project must be/],
    ["missing database.database", validConfig({ database: neonDatabase({ database: undefined }) }), /database must be/],
    ["missing database.appRole", validConfig({ database: neonDatabase({ appRole: undefined }) }), /appRole must be/],
  ])("rejects %s", (_label, config, expected) => {
    expect(() => validate(config)).toThrow(expected);
  });

  it("rejects an empty services array", () => {
    expect(() => validate(validConfig({ services: [] }))).toThrow(/non-empty array/);
  });

  it("rejects a service with an invalid key", () => {
    expect(() => validate(validConfig({ services: [{ key: "API_one", dir: "x" }] }))).toThrow(
      /lowercase, hyphenated/,
    );
  });

  it("rejects duplicate service keys", () => {
    expect(() =>
      validate(
        validConfig({
          services: [
            { key: "api", dir: "a" },
            { key: "api", dir: "b" },
          ],
        }),
      ),
    ).toThrow(/duplicate/);
  });

  it("rejects a service missing dir", () => {
    expect(() => validate(validConfig({ services: [{ key: "api" }] }))).toThrow(/dir must be/);
  });

  it("rejects hyperdrive without a binding name", () => {
    expect(() =>
      validate(
        validConfig({ database: neonDatabase(), services: [{ key: "api", dir: "x", hyperdrive: {} }] }),
      ),
    ).toThrow(/hyperdrive\.binding/);
  });

  it("rejects a hyperdrive-bound service with no database block", () => {
    expect(() =>
      validate(
        validConfig({
          services: [{ key: "api", dir: "x", hyperdrive: { binding: "HYPERDRIVE" } }],
        }),
      ),
    ).toThrow(/hyperdrive requires a database provider/);
  });

  it.each(["configure", "seed", "open"])("rejects %s when it isn't a function", (hook) => {
    expect(() => validate(validConfig({ [hook]: "not a function" }))).toThrow(
      new RegExp(`${hook} must be a function`),
    );
  });
});
