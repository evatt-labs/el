import { describe, expect, it } from "vitest";
import { quoteLiteral } from "../src/postgres.mjs";

describe("quoteLiteral", () => {
  it("wraps a plain value in single quotes", () => {
    expect(quoteLiteral("smoke-test")).toBe("'smoke-test'");
  });

  it("doubles an embedded single quote rather than letting it close the literal", () => {
    // The case this exists for: a value like O'Brien, or an attempt to break
    // out of the literal with a bare quote, must not produce a string that
    // terminates early when substituted into SQL text.
    expect(quoteLiteral("O'Brien")).toBe("'O''Brien'");
  });

  it("neutralizes an attempted SQL injection payload", () => {
    const payload = "x'; drop table tenants; --";
    const quoted = quoteLiteral(payload);
    // The whole payload must end up inside a single literal — no unescaped
    // quote character should appear anywhere except as part of a doubled
    // pair, which is what keeps it inert as one string value.
    expect(quoted).toBe("'x''; drop table tenants; --'");
  });

  it("coerces non-string input", () => {
    expect(quoteLiteral(42)).toBe("'42'");
  });
});
