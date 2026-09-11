import path from "node:path";
import { requireEnv } from "./env.mjs";
import { isValidEnvironmentName, resourceName } from "./names.mjs";
import { resolveProvider } from "./providers/index.mjs";
import {
  findD1DatabaseByName,
  deleteD1Database,
  findKvNamespaceByTitle,
  deleteKvNamespace,
  deleteR2Bucket,
  findQueueByName,
  deleteQueue,
} from "./cloudflare.mjs";
import { deleteWorker } from "./wrangler.mjs";
import { tryDelete } from "./try-delete.mjs";

export async function down(config, name) {
  if (name === undefined) {
    throw new Error("Usage: el down <environment-name>");
  }
  if (!isValidEnvironmentName(name)) {
    throw new Error(`"${name}" doesn't look like an environment name \`el up\` would have created.`);
  }

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
  const token = CLOUDFLARE_API_TOKEN;
  const accountId = CLOUDFLARE_ACCOUNT_ID;

  console.log(`\n== Tearing down "${name}" ==\n`);

  console.log("-> Deleting Workers...");
  for (const service of config.services) {
    const serviceDir = path.resolve(process.cwd(), service.dir);
    deleteWorker(serviceDir, `${name}-${service.key}`);
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

  // The provider owns whatever it provisioned in up() (Neon's per-service
  // Hyperdrive configs and the branch itself) and runs last, after every
  // per-service resource above, mirroring the ordering change from up()'s
  // "provider first" (there, the database has to exist before anything
  // deploys; here, nothing about tearing down Workers/D1/KV/R2/queues
  // depends on the database still existing).
  if (provider) {
    await provider.down({
      name,
      options: providerOptions,
      services: config.services,
      env,
      log: console.log,
    });
  }

  console.log(`\n== "${name}" torn down ==\n`);
}
