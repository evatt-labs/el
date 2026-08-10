import path from "node:path";
import { requireEnv } from "./env.mjs";
import { isValidEnvironmentName } from "./names.mjs";
import { findProjectByName, findBranchByName, deleteBranch } from "./neon.mjs";
import { findHyperdriveConfigByName, deleteHyperdriveConfig } from "./cloudflare.mjs";
import { deleteWorker } from "./wrangler.mjs";

export async function down(config, name) {
  if (name === undefined) {
    throw new Error("Usage: el down <environment-name>");
  }
  if (!isValidEnvironmentName(name)) {
    throw new Error(`"${name}" doesn't look like an environment name \`el up\` would have created.`);
  }

  const { NEON_API_KEY, CLOUDFLARE_API_TOKEN, CLOUDFLARE_ACCOUNT_ID } = requireEnv(
    "NEON_API_KEY",
    "CLOUDFLARE_API_TOKEN",
    "CLOUDFLARE_ACCOUNT_ID",
  );

  console.log(`\n== Tearing down "${name}" ==\n`);

  console.log("-> Deleting Workers...");
  for (const service of config.services) {
    const serviceDir = path.resolve(process.cwd(), service.dir);
    deleteWorker(serviceDir, `${name}-${service.key}`);
  }

  console.log("-> Deleting Hyperdrive configs...");
  for (const service of config.services) {
    if (!service.hyperdrive) continue;
    const configName = `${name}-${service.key}-hyperdrive`;
    const hyperdrive = await findHyperdriveConfigByName(CLOUDFLARE_API_TOKEN, CLOUDFLARE_ACCOUNT_ID, configName);
    if (hyperdrive) {
      await deleteHyperdriveConfig(CLOUDFLARE_API_TOKEN, CLOUDFLARE_ACCOUNT_ID, hyperdrive.id);
    } else {
      console.warn(`  (no Hyperdrive config named "${configName}" found — skipping)`);
    }
  }

  console.log("-> Deleting Neon branch...");
  const project = await findProjectByName(NEON_API_KEY, config.neon.project);
  const branch = await findBranchByName(NEON_API_KEY, project.id, name);
  if (branch) {
    await deleteBranch(NEON_API_KEY, project.id, branch.id);
  } else {
    console.warn(`  (no Neon branch named "${name}" found — skipping)`);
  }

  console.log(`\n== "${name}" torn down ==\n`);
}
