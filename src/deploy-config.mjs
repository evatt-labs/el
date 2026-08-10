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
// to a stateful resource.

const SAFE_STRUCTURAL_KEYS = [
  "$schema",
  "main",
  "compatibility_date",
  "compatibility_flags",
  "observability",
  "account_id",
  "node_compat",
];

// Anything that binds to a real, shared resource. Carried forward only if a
// service opts in with unsafeInheritBindings — named that deliberately, so
// choosing it reads as a choice, not a default.
const STATEFUL_BINDING_KEYS = [
  "d1_databases",
  "kv_namespaces",
  "r2_buckets",
  "queues",
  "durable_objects",
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

export function buildDeployConfig(baseConfig, { name, vars, hyperdrive, unsafeInheritBindings }) {
  const deployConfig = {};
  for (const key of SAFE_STRUCTURAL_KEYS) {
    if (key in baseConfig) deployConfig[key] = baseConfig[key];
  }

  const foundStateful = STATEFUL_BINDING_KEYS.filter((key) => key in baseConfig);
  if (foundStateful.length > 0) {
    if (!unsafeInheritBindings) {
      throw new Error(
        `wrangler.jsonc declares ${foundStateful.join(", ")}, which would deploy this ` +
          `ephemeral environment with live access to those production resources. Set ` +
          `unsafeInheritBindings: true on this service in el.config.mjs if that's really ` +
          `what you want — the name is deliberate.`,
      );
    }
    for (const key of foundStateful) deployConfig[key] = baseConfig[key];
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
