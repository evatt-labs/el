import path from "node:path";
import { requireEnv } from "./env.mjs";
import { generateEnvironmentName, isValidEnvironmentName } from "./names.mjs";
import {
  findProjectByName,
  findDefaultBranch,
  createBranch,
  getConnectionUri,
} from "./neon.mjs";
import { getWorkersSubdomain, createHyperdriveConfig } from "./cloudflare.mjs";
import { loadWranglerConfig, deployWithConfig, putSecret } from "./wrangler.mjs";
import { buildDeployConfig } from "./deploy-config.mjs";
import { waitForConnectable, assertNoBypassRls, runSql, quoteLiteral } from "./postgres.mjs";
import { parseConnectionUri } from "./connection-uri.mjs";
import { openUrl } from "./browser.mjs";

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
    console.log(`-> Deploying "${name}-${service.key}"...`);
    const serviceDir = path.resolve(process.cwd(), service.dir);
    const workerName = `${name}-${service.key}`;
    const baseConfig = loadWranglerConfig(serviceDir);
    const deployConfig = buildDeployConfig(baseConfig, {
      name: workerName,
      vars: vars[service.key] ?? {},
      hyperdrive: service.hyperdrive
        ? [{ binding: service.hyperdrive.binding, id: hyperdriveIds[service.key] }]
        : undefined,
      unsafeInheritBindings: service.unsafeInheritBindings ?? false,
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
