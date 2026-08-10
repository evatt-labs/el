import { describe, expect, it } from "vitest";
import { buildDeployConfig } from "../src/deploy-config.mjs";

const base = {
  $schema: "./node_modules/wrangler/config-schema.json",
  main: "./src/index.ts",
  compatibility_date: "2026-08-08",
  observability: { enabled: true },
  vars: { SHOULD_NOT_SURVIVE: "the committed vars are never carried forward as-is" },
  env: { production: { vars: { SECRET: "prod value" } } },
};

describe("buildDeployConfig", () => {
  it("carries forward only the safe structural keys plus the given overrides", () => {
    const result = buildDeployConfig(base, { name: "blue-honey-badger-12345", vars: { X: "1" } });
    expect(result).toEqual({
      $schema: base.$schema,
      main: base.main,
      compatibility_date: base.compatibility_date,
      observability: base.observability,
      name: "blue-honey-badger-12345",
      vars: { X: "1" },
    });
  });

  it("never carries forward the committed vars or env blocks", () => {
    const result = buildDeployConfig(base, { name: "n", vars: {} });
    expect(result.vars).toEqual({});
    expect(result.env).toBeUndefined();
  });

  it("includes hyperdrive only when explicitly passed", () => {
    const withoutHyperdrive = buildDeployConfig(base, { name: "n", vars: {} });
    expect(withoutHyperdrive.hyperdrive).toBeUndefined();

    const withHyperdrive = buildDeployConfig(base, {
      name: "n",
      vars: {},
      hyperdrive: [{ binding: "HYPERDRIVE", id: "abc123" }],
    });
    expect(withHyperdrive.hyperdrive).toEqual([{ binding: "HYPERDRIVE", id: "abc123" }]);
  });

  // This is the actual bug a security audit found: the first version of `el`
  // deployed an ephemeral Worker with live access to whatever D1/KV/R2/queue/
  // Durable Object bindings the committed config declared, because it only
  // ever overwrote name/vars/hyperdrive and left everything else untouched.
  it.each([
    "d1_databases",
    "kv_namespaces",
    "r2_buckets",
    "queues",
    "durable_objects",
    "ai",
    "vectorize",
    "services",
  ])("refuses to silently deploy with a %s binding present", (key) => {
    const configWithBinding = { ...base, [key]: [{ binding: "SOMETHING" }] };
    expect(() => buildDeployConfig(configWithBinding, { name: "n", vars: {} })).toThrow(
      new RegExp(key),
    );
  });

  it("carries a stateful binding forward only with explicit opt-in", () => {
    const configWithD1 = { ...base, d1_databases: [{ binding: "DB", database_id: "abc" }] };
    const result = buildDeployConfig(configWithD1, {
      name: "n",
      vars: {},
      unsafeInheritBindings: true,
    });
    expect(result.d1_databases).toEqual([{ binding: "DB", database_id: "abc" }]);
  });

  it("never carries forward routes or triggers, even with unsafeInheritBindings", () => {
    const configWithRoutes = {
      ...base,
      routes: [{ pattern: "example.com/*" }],
      triggers: { crons: ["0 0 * * *"] },
    };
    const result = buildDeployConfig(configWithRoutes, {
      name: "n",
      vars: {},
      unsafeInheritBindings: true,
    });
    expect(result.routes).toBeUndefined();
    expect(result.triggers).toBeUndefined();
  });
});
