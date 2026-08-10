// Builds the config actually deployed for an ephemeral environment, from
// the consumer's committed wrangler.jsonc.
//
// This exists because the first version of `el` mutated the loaded config
// in place — setting `name`/`vars`/`hyperdrive` and leaving everything else
// untouched. That meant any D1 database, KV namespace, R2 bucket, queue, or
// Durable Object binding declared at the top level of wrangler.jsonc got
// deployed to the ephemeral Worker VERBATIM, pointed at the same production
// resource the real deployment uses. An "ephemeral, disposable" preview
// environment silently had live read/write access to production data for
// every binding except the one (Hyperdrive) this file was already
// explicitly managing. A security audit is what caught this — nothing about
// it was intentional.
//
// The fix: build the deployed config from an explicit allowlist of
// structural keys, and refuse to silently carry forward anything that binds
// to a stateful resource UNLESS `up.mjs` has already provisioned a fresh,
// environment-scoped substitute for it (see OVERRIDABLE_STATEFUL_KEYS).

const SAFE_STRUCTURAL_KEYS = [
  "$schema",
  "main",
  "compatibility_date",
  "compatibility_flags",
  "observability",
  "account_id",
  "node_compat",
  // A Durable Object class lives inside the Worker script being deployed,
  // not in a separately provisioned resource — a fresh Worker name under a
  // new environment gets fresh DO storage automatically. There is nothing
  // for `el` to provision, and nothing shared with production to leak.
  // `migrations` travels with `durable_objects` for the same reason: both
  // describe this deploy's own script, not an external resource.
  "durable_objects",
  "migrations",
];

// Bind to a real, external resource. Carried forward from the committed
// config only if a service opts in with unsafeInheritBindings (still named
// deliberately) — OR overridden entirely with a fresh, environment-scoped
// resource `up.mjs` already created, in which case the override wins and no
// opt-in is needed, because there's nothing production-pointing left to
// silently inherit.
const STATEFUL_BINDING_KEYS = [
  "d1_databases",
  "kv_namespaces",
  "r2_buckets",
  "queues",
  "ai",
  "vectorize",
  "browser",
  "services",
  "mtls_certificates",
  "dispatch_namespaces",
  "analytics_engine_datasets",
  "send_email",
  "workflows",
  "images",
  "pipelines",
  "secrets_store_secrets",
  "unsafe",
];

// Never carried forward, even with unsafeInheritBindings — reassigning a
// route or a cron trigger during an ephemeral deploy is a sharper failure
// mode than an ephemeral Worker reading prod data: it can redirect real
// production traffic to a disposable Worker.
const NEVER_INHERITED_KEYS = ["routes", "route", "triggers"];

export function buildDeployConfig(
  baseConfig,
  { name, vars, hyperdrive, unsafeInheritBindings, overrides = {} },
) {
  const deployConfig = {};
  for (const key of SAFE_STRUCTURAL_KEYS) {
    if (key in baseConfig) deployConfig[key] = baseConfig[key];
  }

  const overriddenKeys = new Set(Object.keys(overrides));
  const foundStateful = STATEFUL_BINDING_KEYS.filter(
    (key) => key in baseConfig && !overriddenKeys.has(key),
  );
  if (foundStateful.length > 0) {
    if (!unsafeInheritBindings) {
      throw new Error(
        `wrangler.jsonc declares ${foundStateful.join(", ")}, which would deploy this ` +
          `ephemeral environment with live access to those production resources. Declare ` +
          `a matching d1/kv/r2/queues entry in el.config.mjs so el provisions a fresh one, ` +
          `or set unsafeInheritBindings: true on this service if inheriting production is ` +
          `really what you want — the name is deliberate.`,
      );
    }
    for (const key of foundStateful) deployConfig[key] = baseConfig[key];
  }
  for (const [key, value] of Object.entries(overrides)) {
    deployConfig[key] = value;
  }

  const foundNeverInherited = NEVER_INHERITED_KEYS.filter((key) => key in baseConfig);
  if (foundNeverInherited.length > 0) {
    console.warn(
      `  (dropping ${foundNeverInherited.join(", ")} from the deployed config — never ` +
        `carried forward, even with unsafeInheritBindings: reassigning a route or trigger ` +
        `during an ephemeral deploy can redirect real production traffic)`,
    );
  }

  deployConfig.name = name;
  deployConfig.vars = vars ?? {};
  if (hyperdrive) deployConfig.hyperdrive = hyperdrive;

  return deployConfig;
}

/** Builds the `d1_databases` array for freshly-provisioned ephemeral databases. */
export function buildD1DatabasesOverride(entries, ids) {
  return entries.map((entry) => ({
    binding: entry.binding,
    database_name: ids[entry.binding].name,
    database_id: ids[entry.binding].id,
  }));
}

/** Builds the `kv_namespaces` array for freshly-provisioned ephemeral namespaces. */
export function buildKvNamespacesOverride(entries, ids) {
  return entries.map((entry) => ({ binding: entry.binding, id: ids[entry.binding] }));
}

/** Builds the `r2_buckets` array for freshly-provisioned ephemeral buckets. */
export function buildR2BucketsOverride(entries, names) {
  return entries.map((entry) => ({ binding: entry.binding, bucket_name: names[entry.binding] }));
}

/**
 * Builds the `queues` object (producers + consumers) for freshly-provisioned
 * ephemeral queues. Consumer settings (max_batch_size, retry policy, dead
 * letter queue, ...) are copied from the base config only when it declares
 * exactly one consumer — with more than one, which base consumer belongs to
 * which binding is genuinely ambiguous without a name/binding key at the
 * consumer level, so this falls back to wrangler's own defaults rather than
 * guessing. Override with configure() if that's not right for your case.
 */
export function buildQueuesOverride(baseConfig, entries, names) {
  const producers = entries.map((entry) => ({ binding: entry.binding, queue: names[entry.binding] }));

  const baseConsumers = baseConfig.queues?.consumers ?? [];
  const baseConsumerSettings = baseConsumers.length === 1 ? baseConsumers[0] : {};
  const consumers = entries
    .filter((entry) => entry.consumer)
    .map((entry) => ({ ...baseConsumerSettings, queue: names[entry.binding] }));

  return { producers, consumers };
}
