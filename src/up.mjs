import path from "node:path";
import { requireEnv } from "./env.mjs";
import { generateEnvironmentName, isValidEnvironmentName, resourceName } from "./names.mjs";
import {
  findProjectByName,
  findDefaultBranch,
  createBranch,
  getConnectionUri,
} from "./neon.mjs";
import {
  getWorkersSubdomain,
  createHyperdriveConfig,
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
import { waitForConnectable, assertNoBypassRls, runSql, quoteLiteral } from "./postgres.mjs";
import { parseConnectionUri } from "./connection-uri.mjs";
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

export async function up(config, requestedName) {
  if (requestedName !== undefined && !isValidEnvironmentName(requestedName)) {
    throw new Error(
      `"${requestedName}" doesn't match the expected word-word-word-NNNNN shape. Omit it to generate one.`,
    );
  }
  const name = requestedName ?? generateEnvironmentName();

  const { NEON_API_KEY, CLOUDFLARE_API_TOKEN, CLOUDFLARE_ACCOUNT_ID } = requireEnv(
    "NEON_API_KEY",
    "CLOUDFLARE_API_TOKEN",
    "CLOUDFLARE_ACCOUNT_ID",
  );

  console.log(`\n== Provisioning "${name}" ==\n`);

  console.log("-> Locating Neon project and default branch...");
  const project = await findProjectByName(NEON_API_KEY, config.neon.project);
  const parentBranch = await findDefaultBranch(NEON_API_KEY, project.id);

  console.log(`-> Branching "${parentBranch.name}" -> "${name}" (copy-on-write, includes schema + roles)...`);
  const branch = await createBranch(NEON_API_KEY, project.id, parentBranch.id, name);

  console.log("-> Waiting for the new branch's compute to accept connections...");
  const ownerUri = await getConnectionUri(NEON_API_KEY, project.id, {
    branchId: branch.id,
    database: config.neon.database,
    role: "neondb_owner",
  });
  await waitForConnectable(ownerUri);

  const appUri = await getConnectionUri(NEON_API_KEY, project.id, {
    branchId: branch.id,
    database: config.neon.database,
    role: config.neon.appRole,
  });
  console.log(`-> Verifying "${config.neon.appRole}" did not inherit BYPASSRLS...`);
  assertNoBypassRls(appUri, config.neon.appRole);

  console.log("-> Resolving the account's workers.dev subdomain...");
  const subdomain = await getWorkersSubdomain(CLOUDFLARE_API_TOKEN, CLOUDFLARE_ACCOUNT_ID);
  const urls = {};
  for (const service of config.services) {
    urls[service.key] = `https://${name}-${service.key}.${subdomain}.workers.dev`;
  }

  const appConnection = parseConnectionUri(appUri);
  const hyperdriveIds = {};
  for (const service of config.services) {
    if (!service.hyperdrive) continue;
    console.log(`-> Creating Hyperdrive config for "${service.key}"...`);
    hyperdriveIds[service.key] = await createHyperdriveConfig(
      CLOUDFLARE_API_TOKEN,
      CLOUDFLARE_ACCOUNT_ID,
      `${name}-${service.key}-hyperdrive`,
      appConnection,
    );
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
      hyperdrive: service.hyperdrive
        ? [{ binding: service.hyperdrive.binding, id: hyperdriveIds[service.key] }]
        : undefined,
      unsafeInheritBindings: service.unsafeInheritBindings ?? false,
      overrides: resourceOverrides,
    });
    deployWithConfig(serviceDir, deployConfig);

    const serviceSecrets = secrets[service.key] ?? {};
    for (const [secretName, value] of Object.entries(serviceSecrets)) {
      putSecret(serviceDir, workerName, secretName, value);
    }
  }

  let seedResult = {};
  if (config.seed) {
    console.log("-> Running seed() hook...");
    seedResult =
      (await config.seed({
        name,
        urls,
        ownerConnectionString: ownerUri,
        runSql: (sql) => runSql(ownerUri, sql),
        quoteLiteral,
      })) ?? {};
  }

  if (config.open) {
    console.log("-> Opening URLs in your browser...");
    for (const url of config.open({ name, urls })) openUrl(url);
  }

  const lines = [`\n== "${name}" is live ==\n`];
  for (const [key, url] of Object.entries(urls)) lines.push(`  ${key}: ${url}`);
  lines.push(`  branch: ${branch.name} (Neon)`);
  for (const [label, value] of Object.entries(seedResult)) lines.push(`  ${label}: ${value}`);
  lines.push(`\nTear it down:\n\n  el down ${name}\n`);
  console.log(lines.join("\n"));

  return { name, urls, branch };
}
