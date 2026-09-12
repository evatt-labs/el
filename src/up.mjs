import path from "node:path";
import { writeFileSync } from "node:fs";
import { requireEnv } from "./env.mjs";
import { generateEnvironmentName, isValidEnvironmentName, resourceName } from "./names.mjs";
import { resolveProvider, normalizeProviderResult } from "./providers/index.mjs";
import {
  getWorkersSubdomain,
  createD1Database,
  createKvNamespace,
  createR2Bucket,
  createQueue,
} from "./cloudflare.mjs";
import { loadWranglerConfig, deployWithConfig, putSecret, applyD1Migrations } from "./wrangler.mjs";
import {
  buildDeployConfig,
  buildD1DatabasesOverride,
  buildKvNamespacesOverride,
  buildR2BucketsOverride,
  buildQueuesOverride,
} from "./deploy-config.mjs";
import { waitForReachable } from "./reachability.mjs";
import { openUrl } from "./browser.mjs";

/**
 * Provisions every D1/KV/R2/Queues resource one service declares, returning
 * the `overrides` object buildDeployConfig expects. Each resource is fresh
 * and environment-scoped — nothing here ever points at a production
 * database, namespace, bucket, or queue. D1 gets migrations_dir applied
 * (from the base config's matching binding) if one is declared; KV/R2/Queues
 * start empty, since none of them have anything resembling Neon's
 * copy-on-write branching to inherit data from.
 */
async function provisionServiceResources(
  { token, accountId },
  { name, service, serviceDir, baseConfig },
) {
  const overrides = {};

  if (service.d1?.length) {
    const ids = {};
    for (const entry of service.d1) {
      const dbName = resourceName(name, service.key, entry.binding);
      console.log(`-> Creating D1 database for "${service.key}.${entry.binding}"...`);
      const id = await createD1Database(token, accountId, dbName);
      ids[entry.binding] = { id, name: dbName };
      applyD1Migrations(serviceDir, baseConfig, entry.binding, dbName, id);
    }
    overrides.d1_databases = buildD1DatabasesOverride(service.d1, ids);
  }

  if (service.kv?.length) {
    const ids = {};
    for (const entry of service.kv) {
      const title = resourceName(name, service.key, entry.binding);
      console.log(`-> Creating KV namespace for "${service.key}.${entry.binding}"...`);
      ids[entry.binding] = await createKvNamespace(token, accountId, title);
    }
    overrides.kv_namespaces = buildKvNamespacesOverride(service.kv, ids);
  }

  if (service.r2?.length) {
    const names = {};
    for (const entry of service.r2) {
      const bucketName = resourceName(name, service.key, entry.binding);
      console.log(`-> Creating R2 bucket for "${service.key}.${entry.binding}"...`);
      names[entry.binding] = await createR2Bucket(token, accountId, bucketName);
    }
    overrides.r2_buckets = buildR2BucketsOverride(service.r2, names);
  }

  if (service.queues?.length) {
    const names = {};
    for (const entry of service.queues) {
      const queueName = resourceName(name, service.key, entry.binding);
      console.log(`-> Creating queue for "${service.key}.${entry.binding}"...`);
      await createQueue(token, accountId, queueName);
      names[entry.binding] = queueName;
    }
    overrides.queues = buildQueuesOverride(baseConfig, service.queues, names);
  }

  return overrides;
}

export async function up(config, requestedName, { output, noOpen = false } = {}) {
  if (requestedName !== undefined && !isValidEnvironmentName(requestedName)) {
    throw new Error(
      `"${requestedName}" doesn't match the expected word-word-word-NNNNN shape. Omit it to generate one.`,
    );
  }
  const name = requestedName ?? generateEnvironmentName();

  // A `database` block is optional. With none, el is D1-only: no database
  // provider means no extra required env var, and providerResult keeps its
  // no-op defaults for the rest of this run (empty bindings, no seed
  // helpers, no summary line).
  let provider;
  let providerOptions;
  if (config.database) {
    const { provider: providerRef, ...options } = config.database;
    provider = resolveProvider(providerRef);
    providerOptions = options;
  }

  const env = requireEnv(
    "CLOUDFLARE_API_TOKEN",
    "CLOUDFLARE_ACCOUNT_ID",
    ...(provider?.requiredEnv ?? []),
  );
  const { CLOUDFLARE_API_TOKEN, CLOUDFLARE_ACCOUNT_ID } = env;

  console.log(`\n== Provisioning "${name}" ==\n`);

  console.log("-> Resolving the account's workers.dev subdomain...");
  const subdomain = await getWorkersSubdomain(CLOUDFLARE_API_TOKEN, CLOUDFLARE_ACCOUNT_ID);
  const urls = {};
  for (const service of config.services) {
    urls[service.key] = `https://${name}-${service.key}.${subdomain}.workers.dev`;
  }

  // Provider provisioning (Neon's branch fork + Hyperdrive, when configured)
  // happens before configure(), matching the ordering the Neon-only version
  // of this tool always had: the database exists, and its connection is
  // known, before configure() or any service deploy runs.
  //
  // normalizeProviderResult always returns a callable bindings(), an
  // object seed, and an array summary, whatever a configured provider did
  // or didn't return from up(): everything downstream can use
  // providerResult unconditionally. With no provider configured at all,
  // these no-op defaults (no bindings, no seed helpers, no summary line)
  // are exactly what D1-only needs.
  let providerResult = { bindings: () => ({}), seed: {}, summary: [] };
  if (provider) {
    const result = await provider.up({
      name,
      options: providerOptions,
      services: config.services,
      env,
      log: console.log,
    });
    providerResult = normalizeProviderResult(result, provider);
  }

  let vars = {};
  let secrets = {};
  if (config.configure) {
    console.log("-> Running configure() hook...");
    const result = await config.configure({ name, urls, subdomain });
    vars = result?.vars ?? {};
    secrets = result?.secrets ?? {};
  }

  for (const service of config.services) {
    const serviceDir = path.resolve(process.cwd(), service.dir);
    const workerName = `${name}-${service.key}`;
    const baseConfig = loadWranglerConfig(serviceDir);

    const resourceOverrides = await provisionServiceResources(
      { token: CLOUDFLARE_API_TOKEN, accountId: CLOUDFLARE_ACCOUNT_ID },
      { name, service, serviceDir, baseConfig },
    );

    console.log(`-> Deploying "${workerName}"...`);
    const deployConfig = buildDeployConfig(baseConfig, {
      name: workerName,
      vars: vars[service.key] ?? {},
      hyperdrive: providerResult.bindings(service).hyperdrive,
      unsafeInheritBindings: service.unsafeInheritBindings ?? false,
      overrides: resourceOverrides,
    });
    deployWithConfig(serviceDir, deployConfig);

    const serviceSecrets = secrets[service.key] ?? {};
    for (const [secretName, value] of Object.entries(serviceSecrets)) {
      putSecret(serviceDir, workerName, secretName, value);
    }
  }

  // Every environment gets a workers.dev hostname that has never existed
  // before, and Cloudflare's own docs say a first deploy to a new
  // subdomain can show edge errors "while DNS is propagating" — not a
  // one-off, a property of this tool's naming pattern. Wait for each URL
  // to clear before declaring the environment ready or opening a browser
  // tab into that exact window. Run in parallel — deploys already happened
  // sequentially above, no reason to serialize the waits too.
  console.log("-> Waiting for deployed Workers to become reachable...");
  await Promise.all(Object.values(urls).map((url) => waitForReachable(url)));

  // Without a database provider, seed() gets no Postgres helpers: there is
  // no owner connection to hand it. A consumer whose seed() destructures
  // ownerConnectionString/runSql/quoteLiteral without a provider configured
  // gets `undefined` for each, same as any other missing object property.
  //
  // providerResult.seed is spread first, name/urls second, so el's own
  // keys always win: a provider whose seed object happened to use `name`
  // or `urls` as a field can't shadow the environment name or its URLs.
  let seedResult = {};
  if (config.seed) {
    console.log("-> Running seed() hook...");
    seedResult =
      (await config.seed({
        ...providerResult.seed,
        name,
        urls,
      })) ?? {};
  }

  if (noOpen) {
    if (config.open) console.log("-> Skipping open() (--no-open)");
  } else if (config.open) {
    console.log("-> Opening URLs in your browser...");
    for (const url of config.open({ name, urls })) openUrl(url);
  }

  const lines = [`\n== "${name}" is live ==\n`];
  for (const [key, url] of Object.entries(urls)) lines.push(`  ${key}: ${url}`);
  for (const line of providerResult.summary) lines.push(`  ${line}`);
  for (const [label, value] of Object.entries(seedResult)) lines.push(`  ${label}: ${value}`);
  lines.push(`\nTear it down:\n\n  el down ${name}\n`);
  console.log(lines.join("\n"));

  const result = { name, urls, summary: providerResult.summary, seed: seedResult };

  if (output) {
    // Deliberately limited to these four keys: name, urls, summary, seed.
    // No connection strings, no vars, no secrets. This file is meant to be
    // read back by a CI workflow (the GitHub Action does exactly that) and
    // may end up somewhere less locked-down than the process that produced
    // it, so it never carries anything a hook returned that isn't already
    // safe to hand to a third party.
    const outputPath = path.resolve(process.cwd(), output);
    writeFileSync(outputPath, `${JSON.stringify(result, null, 2)}\n`, { mode: 0o600 });
  }

  return result;
}
