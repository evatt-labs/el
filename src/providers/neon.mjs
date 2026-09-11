// The Neon database provider: forks a Neon branch, verifies the app role
// isn't RLS-exempt, and pre-provisions a Hyperdrive config per service that
// declares one. This is the only built-in provider today; see
// src/providers/index.mjs for the registry a `database.provider` string
// resolves against.

import {
  findProjectByName,
  findDefaultBranch,
  createBranch,
  findBranchByName,
  deleteBranch,
  getConnectionUri,
} from "../neon.mjs";
import { createHyperdriveConfig, findHyperdriveConfigByName, deleteHyperdriveConfig } from "../cloudflare.mjs";
import { waitForConnectable, assertNoBypassRls, runSql, quoteLiteral } from "../postgres.mjs";
import { parseConnectionUri } from "../connection-uri.mjs";
import { tryDelete } from "../try-delete.mjs";

const IDENTIFIER = /^[A-Za-z_][A-Za-z0-9_$]*$/;

function fail(message) {
  throw new Error(`Invalid Neon provider options: ${message}`);
}

export const name = "neon";

export const requiredEnv = ["NEON_API_KEY"];

export function validate(options) {
  if (typeof options?.project !== "string" || options.project === "") {
    fail("project must be a non-empty string");
  }
  if (typeof options?.database !== "string" || options.database === "") {
    fail("database must be a non-empty string");
  }
  if (typeof options?.appRole !== "string" || !IDENTIFIER.test(options.appRole)) {
    fail(
      "appRole must be a valid Postgres identifier (the least-privilege role Hyperdrive " +
        `connects as), got ${JSON.stringify(options?.appRole)}`,
    );
  }
}

/**
 * The per-service `bindings()` lookup up() hands back, split out as its own
 * function so it's testable without the network calls the rest of up()
 * makes. A service without `hyperdrive` gets no binding at all: nothing
 * for buildDeployConfig's `hyperdrive` param to add.
 */
export function bindingsFor(service, hyperdriveIds) {
  if (!service.hyperdrive) return {};
  return { hyperdrive: [{ binding: service.hyperdrive.binding, id: hyperdriveIds[service.key] }] };
}

export async function up({ name: environmentName, options, services, env, log }) {
  const { NEON_API_KEY, CLOUDFLARE_API_TOKEN, CLOUDFLARE_ACCOUNT_ID } = env;

  log("-> Locating Neon project and default branch...");
  const project = await findProjectByName(NEON_API_KEY, options.project);
  const parentBranch = await findDefaultBranch(NEON_API_KEY, project.id);

  log(`-> Branching "${parentBranch.name}" -> "${environmentName}" (copy-on-write, includes schema + roles)...`);
  const branch = await createBranch(NEON_API_KEY, project.id, parentBranch.id, environmentName);

  log("-> Waiting for the new branch's compute to accept connections...");
  const ownerUri = await getConnectionUri(NEON_API_KEY, project.id, {
    branchId: branch.id,
    database: options.database,
    role: "neondb_owner",
  });
  await waitForConnectable(ownerUri);

  const appUri = await getConnectionUri(NEON_API_KEY, project.id, {
    branchId: branch.id,
    database: options.database,
    role: options.appRole,
  });
  log(`-> Verifying "${options.appRole}" did not inherit BYPASSRLS...`);
  assertNoBypassRls(appUri, options.appRole);

  const appConnection = parseConnectionUri(appUri);
  const hyperdriveIds = {};
  for (const service of services) {
    if (!service.hyperdrive) continue;
    log(`-> Creating Hyperdrive config for "${service.key}"...`);
    hyperdriveIds[service.key] = await createHyperdriveConfig(
      CLOUDFLARE_API_TOKEN,
      CLOUDFLARE_ACCOUNT_ID,
      `${environmentName}-${service.key}-hyperdrive`,
      appConnection,
    );
  }

  return {
    bindings: (service) => bindingsFor(service, hyperdriveIds),
    seed: {
      ownerConnectionString: ownerUri,
      runSql: (sql) => runSql(ownerUri, sql),
      quoteLiteral,
    },
    summary: [`branch: ${branch.name} (Neon)`],
  };
}

export async function down({ name: environmentName, options, services, env, log }) {
  const { NEON_API_KEY, CLOUDFLARE_API_TOKEN, CLOUDFLARE_ACCOUNT_ID } = env;
  const token = CLOUDFLARE_API_TOKEN;
  const accountId = CLOUDFLARE_ACCOUNT_ID;

  log("-> Deleting Hyperdrive configs...");
  for (const service of services) {
    if (!service.hyperdrive) continue;
    const configName = `${environmentName}-${service.key}-hyperdrive`;
    await tryDelete(`Hyperdrive config "${configName}"`, async () => {
      const hyperdrive = await findHyperdriveConfigByName(token, accountId, configName);
      if (!hyperdrive) throw new Error("not found");
      await deleteHyperdriveConfig(token, accountId, hyperdrive.id);
    });
  }

  log("-> Deleting Neon branch...");
  await tryDelete(`Neon branch "${environmentName}"`, async () => {
    const project = await findProjectByName(NEON_API_KEY, options.project);
    const branch = await findBranchByName(NEON_API_KEY, project.id, environmentName);
    if (!branch) {
      console.warn(`  (no Neon branch named "${environmentName}" found, skipping)`);
      return;
    }
    await deleteBranch(NEON_API_KEY, project.id, branch.id);
  });
}
