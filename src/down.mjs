import path from "node:path";
import { requireEnv } from "./env.mjs";
import { isValidEnvironmentName, resourceName } from "./names.mjs";
import { findProjectByName, findBranchByName, deleteBranch } from "./neon.mjs";
import {
  findHyperdriveConfigByName,
  deleteHyperdriveConfig,
  findD1DatabaseByName,
  deleteD1Database,
  findKvNamespaceByTitle,
  deleteKvNamespace,
  deleteR2Bucket,
  findQueueByName,
  deleteQueue,
} from "./cloudflare.mjs";
import { deleteWorker } from "./wrangler.mjs";

/** Best-effort: reports a failure but never stops the rest of teardown. */
async function tryDelete(label, fn) {
  try {
    await fn();
  } catch (error) {
    console.warn(`  (${label} failed — continuing)`, String(error.message ?? error));
  }
}

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
  const token = CLOUDFLARE_API_TOKEN;
  const accountId = CLOUDFLARE_ACCOUNT_ID;

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
    await tryDelete(`Hyperdrive config "${configName}"`, async () => {
      const hyperdrive = await findHyperdriveConfigByName(token, accountId, configName);
      if (!hyperdrive) throw new Error("not found");
      await deleteHyperdriveConfig(token, accountId, hyperdrive.id);
    });
  }

  console.log("-> Deleting D1 databases...");
  for (const service of config.services) {
    for (const entry of service.d1 ?? []) {
      const dbName = resourceName(name, service.key, entry.binding);
      await tryDelete(`D1 database "${dbName}"`, async () => {
        const database = await findD1DatabaseByName(token, accountId, dbName);
        if (!database) throw new Error("not found");
        await deleteD1Database(token, accountId, database.uuid);
      });
    }
  }

  console.log("-> Deleting KV namespaces...");
  for (const service of config.services) {
    for (const entry of service.kv ?? []) {
      const title = resourceName(name, service.key, entry.binding);
      await tryDelete(`KV namespace "${title}"`, async () => {
        const namespace = await findKvNamespaceByTitle(token, accountId, title);
        if (!namespace) throw new Error("not found");
        await deleteKvNamespace(token, accountId, namespace.id);
      });
    }
  }

  console.log("-> Deleting R2 buckets...");
  for (const service of config.services) {
    for (const entry of service.r2 ?? []) {
      const bucketName = resourceName(name, service.key, entry.binding);
      await tryDelete(`R2 bucket "${bucketName}"`, () => deleteR2Bucket(token, accountId, bucketName));
    }
  }

  console.log("-> Deleting queues...");
  for (const service of config.services) {
    for (const entry of service.queues ?? []) {
      const queueName = resourceName(name, service.key, entry.binding);
      await tryDelete(`queue "${queueName}"`, async () => {
        const queue = await findQueueByName(token, accountId, queueName);
        if (!queue) throw new Error("not found");
        await deleteQueue(token, accountId, queue.queue_id);
      });
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
