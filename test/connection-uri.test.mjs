import { describe, expect, it } from "vitest";
import { parseConnectionUri } from "../src/connection-uri.mjs";

describe("parseConnectionUri", () => {
  it("parses a typical Neon connection string", () => {
    const parsed = parseConnectionUri(
      "postgresql://neondb_owner:npg_abc123@ep-foo-123.us-east-2.aws.neon.tech/neondb?sslmode=require&channel_binding=require",
    );
    expect(parsed).toEqual({
      scheme: "postgresql",
      host: "ep-foo-123.us-east-2.aws.neon.tech",
      port: 5432,
      user: "neondb_owner",
      password: "npg_abc123",
      database: "neondb",
      sslmode: "require",
    });
  });

  it("defaults port to 5432 when omitted", () => {
    expect(parseConnectionUri("postgres://u:p@host/db").port).toBe(5432);
  });

  it("respects an explicit non-default port", () => {
    expect(parseConnectionUri("postgres://u:p@host:6543/db").port).toBe(6543);
  });

  it("defaults sslmode to require when absent", () => {
    expect(parseConnectionUri("postgres://u:p@host/db").sslmode).toBe("require");
  });

  it("decodes percent-encoded credentials", () => {
    // A password containing a literal "@" or "/" must be percent-encoded in
    // the URI, or URL parsing itself breaks — this is the case worth
    // pinning, since a Neon-generated password could plausibly contain
    // characters that need encoding.
    const parsed = parseConnectionUri("postgres://user%40x:p%2Fss@host/db");
    expect(parsed.user).toBe("user@x");
    expect(parsed.password).toBe("p/ss");
  });
});
