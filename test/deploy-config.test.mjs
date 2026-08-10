import { describe, expect, it } from "vitest";
import {
  buildDeployConfig,
  buildD1DatabasesOverride,
  buildKvNamespacesOverride,
  buildR2BucketsOverride,
  buildQueuesOverride,
} from "../src/deploy-config.mjs";

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

  // A Durable Object class lives inside the deployed script itself, not a
  // separately provisioned resource — a fresh Worker name automatically gets
  // fresh DO storage. Nothing to provision, nothing shared with production.
  it("always carries durable_objects and migrations forward, no opt-in required", () => {
    const configWithDO = {
      ...base,
      durable_objects: { bindings: [{ name: "ROOM", class_name: "Room" }] },
      migrations: [{ tag: "v1", new_sqlite_classes: ["Room"] }],
    };
    const result = buildDeployConfig(configWithDO, { name: "n", vars: {} });
    expect(result.durable_objects).toEqual(configWithDO.durable_objects);
    expect(result.migrations).toEqual(configWithDO.migrations);
  });

  // This is the actual bug a security audit found: the first version of `el`
  // deployed an ephemeral Worker with live access to whatever D1/KV/R2/queue
  // bindings the committed config declared, because it only ever overwrote
  // name/vars/hyperdrive and left everything else untouched.
  it.each(["d1_databases", "kv_namespaces", "r2_buckets", "queues", "ai", "vectorize", "services"])(
    "refuses to silently deploy with a %s binding present",
    (key) => {
      const configWithBinding = { ...base, [key]: [{ binding: "SOMETHING" }] };
      expect(() => buildDeployConfig(configWithBinding, { name: "n", vars: {} })).toThrow(
        new RegExp(key),
      );
    },
  );

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

  // The point of this feature: when `el` has already provisioned a fresh,
  // environment-scoped resource, an override replaces the committed
  // (production-pointing) binding entirely — no opt-in needed, because
  // there's nothing production-pointing left in the deployed config.
  it("uses a provided override instead of throwing, with no opt-in required", () => {
    const configWithD1 = { ...base, d1_databases: [{ binding: "DB", database_id: "prod-id" }] };
    const freshD1 = [{ binding: "DB", database_name: "n-api-db", database_id: "fresh-id" }];
    const result = buildDeployConfig(configWithD1, {
      name: "n",
      vars: {},
      overrides: { d1_databases: freshD1 },
    });
    expect(result.d1_databases).toEqual(freshD1);
  });

  it("only overrides the specific stateful keys given, still gating the rest", () => {
    const configWithBoth = {
      ...base,
      d1_databases: [{ binding: "DB" }],
      kv_namespaces: [{ binding: "CACHE" }],
    };
    expect(() =>
      buildDeployConfig(configWithBoth, {
        name: "n",
        vars: {},
        overrides: { d1_databases: [{ binding: "DB", database_id: "fresh" }] },
      }),
    ).toThrow(/kv_namespaces/);
  });
});

describe("buildD1DatabasesOverride", () => {
  it("maps each declared binding to its provisioned id and name", () => {
    const result = buildD1DatabasesOverride([{ binding: "DB" }], {
      DB: { id: "uuid-1", name: "n-api-db" },
    });
    expect(result).toEqual([{ binding: "DB", database_name: "n-api-db", database_id: "uuid-1" }]);
  });

  it("handles multiple bindings independently", () => {
    const result = buildD1DatabasesOverride([{ binding: "DB" }, { binding: "ANALYTICS" }], {
      DB: { id: "uuid-1", name: "n-api-db" },
      ANALYTICS: { id: "uuid-2", name: "n-api-analytics" },
    });
    expect(result).toHaveLength(2);
    expect(result[1]).toEqual({
      binding: "ANALYTICS",
      database_name: "n-api-analytics",
      database_id: "uuid-2",
    });
  });
});

describe("buildKvNamespacesOverride", () => {
  it("maps each declared binding to its provisioned namespace id", () => {
    const result = buildKvNamespacesOverride([{ binding: "CACHE" }], { CACHE: "kv-id-1" });
    expect(result).toEqual([{ binding: "CACHE", id: "kv-id-1" }]);
  });
});

describe("buildR2BucketsOverride", () => {
  it("maps each declared binding to its provisioned bucket name", () => {
    const result = buildR2BucketsOverride([{ binding: "ASSETS" }], { ASSETS: "n-api-assets" });
    expect(result).toEqual([{ binding: "ASSETS", bucket_name: "n-api-assets" }]);
  });
});

describe("buildQueuesOverride", () => {
  it("builds a producer entry for every declared binding", () => {
    const result = buildQueuesOverride({}, [{ binding: "JOBS" }], { JOBS: "n-api-jobs" });
    expect(result.producers).toEqual([{ binding: "JOBS", queue: "n-api-jobs" }]);
    expect(result.consumers).toEqual([]);
  });

  it("builds a consumer entry only for bindings marked consumer: true", () => {
    const result = buildQueuesOverride(
      {},
      [{ binding: "JOBS", consumer: true }, { binding: "EVENTS" }],
      { JOBS: "n-api-jobs", EVENTS: "n-api-events" },
    );
    expect(result.consumers).toEqual([{ queue: "n-api-jobs" }]);
  });

  it("carries forward consumer settings when the base config has exactly one consumer", () => {
    const base = {
      queues: { consumers: [{ queue: "prod-jobs", max_batch_size: 25, max_retries: 3 }] },
    };
    const result = buildQueuesOverride(base, [{ binding: "JOBS", consumer: true }], {
      JOBS: "n-api-jobs",
    });
    expect(result.consumers).toEqual([{ queue: "n-api-jobs", max_batch_size: 25, max_retries: 3 }]);
  });

  it("falls back to no extra settings when the base config has more than one consumer", () => {
    // Which base consumer belongs to which binding is ambiguous without a
    // name/binding key at the consumer level — defaulting rather than
    // guessing wrong is the point of this test.
    const base = {
      queues: {
        consumers: [{ queue: "prod-a", max_batch_size: 1 }, { queue: "prod-b", max_batch_size: 2 }],
      },
    };
    const result = buildQueuesOverride(base, [{ binding: "JOBS", consumer: true }], {
      JOBS: "n-api-jobs",
    });
    expect(result.consumers).toEqual([{ queue: "n-api-jobs" }]);
  });
});
