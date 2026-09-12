import { describe, expect, it } from "vitest";
import { name, requiredEnv, validate, bindingsFor } from "../../src/providers/neon.mjs";

function validOptions(overrides = {}) {
  return { project: "acme", database: "neondb", appRole: "app_user", ...overrides };
}

describe("neon provider", () => {
  it("has the expected name and requiredEnv", () => {
    expect(name).toBe("neon");
    expect(requiredEnv).toEqual(["NEON_API_KEY"]);
  });

  it("accepts valid options", () => {
    expect(() => validate(validOptions())).not.toThrow();
  });

  it("rejects a missing project", () => {
    expect(() => validate(validOptions({ project: undefined }))).toThrow(/project must be/);
  });

  it("rejects an empty project", () => {
    expect(() => validate(validOptions({ project: "" }))).toThrow(/project must be/);
  });

  it("rejects a missing database", () => {
    expect(() => validate(validOptions({ database: undefined }))).toThrow(/database must be/);
  });

  it("rejects a bad appRole identifier", () => {
    expect(() => validate(validOptions({ appRole: "not-a-valid-identifier" }))).toThrow(
      /appRole must be a valid Postgres identifier/,
    );
  });
});

describe("bindingsFor", () => {
  it("returns an empty object for a service without hyperdrive", () => {
    expect(bindingsFor({ key: "api" }, {})).toEqual({});
  });

  it("returns the hyperdrive binding for a service that declares one", () => {
    const result = bindingsFor(
      { key: "api", hyperdrive: { binding: "HYPERDRIVE" } },
      { api: "hyperdrive-id-123" },
    );
    expect(result).toEqual({ hyperdrive: [{ binding: "HYPERDRIVE", id: "hyperdrive-id-123" }] });
  });
});
