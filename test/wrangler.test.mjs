import { describe, expect, it } from "vitest";
import { parseWranglerVersion } from "../src/wrangler.mjs";

describe("parseWranglerVersion", () => {
  it("extracts the version from wrangler's real version banner", () => {
    expect(parseWranglerVersion("⛅️ wrangler 4.131.1\n")).toBe("4.131.1");
  });

  it("extracts a bare version string", () => {
    expect(parseWranglerVersion("4.131.1")).toBe("4.131.1");
  });

  it("returns undefined when nothing version-shaped is found", () => {
    expect(parseWranglerVersion("no version here")).toBeUndefined();
  });
});
