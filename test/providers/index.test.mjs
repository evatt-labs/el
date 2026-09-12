import { describe, expect, it } from "vitest";
import { normalizeProviderResult } from "../../src/providers/index.mjs";

const provider = { name: "custom" };

describe("normalizeProviderResult", () => {
  it("defaults everything when up() returned undefined", () => {
    const result = normalizeProviderResult(undefined, provider);
    expect(result.bindings({ key: "api" })).toEqual({});
    expect(result.seed).toEqual({});
    expect(result.summary).toEqual([]);
  });

  it("defaults everything when up() returned an empty object", () => {
    const result = normalizeProviderResult({}, provider);
    expect(result.bindings({ key: "api" })).toEqual({});
    expect(result.seed).toEqual({});
    expect(result.summary).toEqual([]);
  });

  it("passes through a provided bindings function, seed, and summary", () => {
    const bindings = (service) => ({ hyperdrive: [{ binding: "HYPERDRIVE", id: `${service.key}-id` }] });
    const result = normalizeProviderResult(
      { bindings, seed: { ownerConnectionString: "postgres://..." }, summary: ["branch: main"] },
      provider,
    );
    expect(result.bindings({ key: "api" })).toEqual({
      hyperdrive: [{ binding: "HYPERDRIVE", id: "api-id" }],
    });
    expect(result.seed).toEqual({ ownerConnectionString: "postgres://..." });
    expect(result.summary).toEqual(["branch: main"]);
  });

  it("throws a clear error naming the provider when bindings isn't a function", () => {
    expect(() => normalizeProviderResult({ bindings: {} }, provider)).toThrow(
      /provider "custom" returned a non-function "bindings"/,
    );
  });

  it("normalizes a bindings() call that returns undefined to an empty object", () => {
    const result = normalizeProviderResult({ bindings: () => undefined }, provider);
    expect(result.bindings({ key: "api" })).toEqual({});
  });

  it("normalizes a bindings() call that returns a non-object to an empty object", () => {
    const result = normalizeProviderResult({ bindings: () => "not an object" }, provider);
    expect(result.bindings({ key: "api" })).toEqual({});
  });
});
