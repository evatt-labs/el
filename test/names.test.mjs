import { describe, expect, it } from "vitest";
import { generateEnvironmentName, isValidEnvironmentName } from "../src/names.mjs";

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
