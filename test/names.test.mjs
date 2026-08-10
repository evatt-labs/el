import { describe, expect, it } from "vitest";
import { generateEnvironmentName, isValidEnvironmentName, resourceName } from "../src/names.mjs";

describe("generateEnvironmentName", () => {
  it("produces a name matching its own validator", () => {
    for (let i = 0; i < 50; i++) {
      expect(isValidEnvironmentName(generateEnvironmentName())).toBe(true);
    }
  });

  it("produces different names across calls", () => {
    const names = new Set(Array.from({ length: 20 }, () => generateEnvironmentName()));
    // Collisions are possible but should be rare across 20 draws from these
    // word lists; a set collapsing to 1 would mean the RNG isn't varying.
    expect(names.size).toBeGreaterThan(1);
  });
});

describe("isValidEnvironmentName", () => {
  it.each([
    "blue-honey-badger-12345",
    "crimson-lazy-wombat-11129",
  ])("accepts %s", (name) => {
    expect(isValidEnvironmentName(name)).toBe(true);
  });

  it.each([
    ["too few segments", "blue-honey-12345"],
    ["no numeric suffix", "blue-honey-badger"],
    ["short numeric suffix", "blue-honey-badger-123"],
    ["uppercase", "Blue-honey-badger-12345"],
    ["underscores", "blue_honey_badger_12345"],
    ["empty string", ""],
    ["arbitrary user input", "production"],
  ])("rejects %s", (_label, name) => {
    expect(isValidEnvironmentName(name)).toBe(false);
  });
});

describe("resourceName", () => {
  it("joins env, service key, and lowercased binding with hyphens", () => {
    expect(resourceName("blue-honey-badger-12345", "api", "DB")).toBe(
      "blue-honey-badger-12345-api-db",
    );
  });

  // R2 bucket names specifically must be lowercase and DNS-compliant, and a
  // binding name with underscores (MY_QUEUE, common in wrangler configs)
  // would otherwise produce an invalid bucket name.
  it("replaces underscores and other non-alphanumerics with hyphens", () => {
    expect(resourceName("n", "api", "MY_QUEUE")).toBe("n-api-my-queue");
  });

  it("collapses runs of separators rather than leaving them adjacent", () => {
    expect(resourceName("n", "api", "MY__WEIRD--BINDING")).toBe("n-api-my-weird-binding");
  });

  it("strips a leading or trailing separator produced by the binding name itself", () => {
    expect(resourceName("n", "api", "_LEADING")).toBe("n-api-leading");
  });

  it("truncates to 63 characters without leaving a trailing hyphen", () => {
    const long = resourceName("n", "api", "A".repeat(80));
    expect(long.length).toBeLessThanOrEqual(63);
    expect(long.endsWith("-")).toBe(false);
  });
});
