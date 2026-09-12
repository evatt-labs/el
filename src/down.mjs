import path from "node:path";
import { requireEnv } from "./env.mjs";
import { isValidEnvironmentName } from "./names.mjs";
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
import { deleteLock, lockPath, mergeResources, readLock } from "./lockfile.mjs";

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

  // Corrupt or unreadable-version lockfile: propagate, don't swallow. A
  // silent fallback to config-only deletion here would reintroduce the
  // exact leak this feature exists to close, with nothing telling anyone
  // why.
  const lock = readLock(process.cwd(), name);
  if (!lock) {
    console.log(
      `  (no ${lockPath(process.cwd(), name)} found; deleting only what el.config.mjs currently declares)`,
    );
  }

  // Computed once per service, up front, rather than inside each of the
  // four deletion loops below: mergeResources builds all four resource
  // types together, so doing it once per service avoids recomputing it four
  // times for the same service.
  const mergedByService = new Map(
    config.services.map((service) => [service.key, mergeResources(lock?.services?.[service.key], service, name)]),
  );

  // tryDelete (src/try-delete.mjs) never rethrows, by design, so it can't be
  // wrapped to observe failure. This reimplements its exact try/warn shape
  // locally, with the one addition down() needs: whether anything warned, so
  // the lockfile is only deleted after a clean teardown. try-delete.mjs
  // itself is untouched; the Neon provider's own use of it (which has no
  // reason to report back to down.mjs) still behaves exactly as before.
  let hadWarnings = false;
  async function trackedTryDelete(label, fn) {
    try {
      await fn();
    } catch (error) {
      hadWarnings = true;
      console.warn(`  (${label} failed, continuing)`, String(error.message ?? error));
    }
  }

  console.log("-> Deleting Workers...");
  for (const service of config.services) {
    const serviceDir = path.resolve(process.cwd(), service.dir);
    deleteWorker(serviceDir, `${name}-${service.key}`);
  }

  console.log("-> Deleting D1 databases...");
  for (const service of config.services) {
    for (const entry of mergedByService.get(service.key).d1) {
      await trackedTryDelete(`D1 database "${entry.name}"`, async () => {
        const database = await findD1DatabaseByName(token, accountId, entry.name);
        // Already gone is the goal, not a failure: a resource the merged
        // list names but that a previous, partial `el down` already deleted
        // must not count as a warning, or the lockfile could never be
        // cleared on a retry (see the note on hadWarnings above).
        if (!database) {
          console.warn(`  (no D1 database named "${entry.name}" found, skipping)`);
          return;
        }
        await deleteD1Database(token, accountId, database.uuid);
      });
    }
  }

  console.log("-> Deleting KV namespaces...");
  for (const service of config.services) {
    for (const entry of mergedByService.get(service.key).kv) {
      await trackedTryDelete(`KV namespace "${entry.name}"`, async () => {
        const namespace = await findKvNamespaceByTitle(token, accountId, entry.name);
        if (!namespace) {
          console.warn(`  (no KV namespace named "${entry.name}" found, skipping)`);
          return;
        }
        await deleteKvNamespace(token, accountId, namespace.id);
      });
    }
  }

  // R2 has no find-by-name step (see cloudflare.mjs): deleteR2Bucket is
  // called directly, so a bucket that's already gone surfaces as a thrown
  // Cloudflare API error, same as any other failure, and IS counted as a
  // warning below. Unlike D1/KV/queues, there's no cheap way here to tell
  // "already deleted" apart from a real failure without adding a find step
  // this codebase doesn't otherwise need for R2.
  console.log("-> Deleting R2 buckets...");
  for (const service of config.services) {
    for (const entry of mergedByService.get(service.key).r2) {
      await trackedTryDelete(`R2 bucket "${entry.name}"`, () => deleteR2Bucket(token, accountId, entry.name));
    }
  }

  console.log("-> Deleting queues...");
  for (const service of config.services) {
    for (const entry of mergedByService.get(service.key).queues) {
      await trackedTryDelete(`queue "${entry.name}"`, async () => {
        const queue = await findQueueByName(token, accountId, entry.name);
        if (!queue) {
          console.warn(`  (no queue named "${entry.name}" found, skipping)`);
          return;
        }
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
      lock: lock?.database?.lock,
    });
  }

  // Only deleted once teardown completed with no warnings above: a Worker
  // delete failure and a provider-internal failure (Neon's own tryDelete)
  // aren't tracked by hadWarnings, so a lock can still be removed after
  // either of those even though something didn't come down cleanly. This is
  // a known gap in "no warnings from tryDelete" as the criterion, not
  // something this change closes: hadWarnings only observes the four
  // per-resource loops above, which is where this feature's own fix lives.
  if (lock && !hadWarnings) {
    deleteLock(process.cwd(), name);
  } else if (lock) {
    console.log(
      `  (keeping ${lockPath(process.cwd(), name)}: teardown reported warnings above, rerun \`el down ${name}\` to retry)`,
    );
  }

  console.log(`\n== "${name}" torn down ==\n`);
}
