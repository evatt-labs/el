import { describe, expect, it } from "vitest";
import { validate } from "../src/config.mjs";

function validConfig(overrides = {}) {
  return {
    neon: { project: "acme", database: "neondb", appRole: "app_user" },
    services: [{ key: "api", dir: "packages/api" }],
    ...overrides,
  };
}

describe("validate", () => {
  it("accepts a minimal valid config", () => {
    expect(() => validate(validConfig())).not.toThrow();
  });

  it("accepts hooks and a hyperdrive-bound service", () => {
    expect(() =>
      validate(
        validConfig({
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

  it.each([
    ["missing neon.project", validConfig({ neon: { database: "neondb", appRole: "app_user" } })],
    ["empty neon.project", validConfig({ neon: { project: "", database: "neondb", appRole: "app_user" } })],
    ["missing neon.database", validConfig({ neon: { project: "acme", appRole: "app_user" } })],
    ["missing neon.appRole", validConfig({ neon: { project: "acme", database: "neondb" } })],
  ])("rejects %s", (_label, config) => {
    expect(() => validate(config)).toThrow(/neon\./);
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
      validate(validConfig({ services: [{ key: "api", dir: "x", hyperdrive: {} }] })),
    ).toThrow(/hyperdrive\.binding/);
  });

  it.each(["configure", "seed", "open"])("rejects %s when it isn't a function", (hook) => {
    expect(() => validate(validConfig({ [hook]: "not a function" }))).toThrow(
      new RegExp(`${hook} must be a function`),
    );
  });
});
