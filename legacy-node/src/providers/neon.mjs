// The Neon database provider: forks a Neon branch, verifies the app role
// isn't RLS-exempt, and pre-provisions a Hyperdrive config per service that
// declares one. This is the only built-in provider today; see
// src/providers/index.mjs for the registry a `database.provider` string
// resolves against.

import {
  findProjectByName,
  findOrganizations,
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
  if (options?.orgId !== undefined && (typeof options.orgId !== "string" || options.orgId === "")) {
    fail("orgId must be a non-empty string if given");
  }
}

/**
 * Every Neon account belongs to at least one organization, and listing
 * projects by name (findProjectByName in ../neon.mjs) requires an org_id.
 * `organizations` is whatever findOrganizations(apiKey) returned, passed
 * in rather than fetched here so this decision (which org to use, or
 * whether to give up and ask) is plain data-in-data-out and testable
 * without a network call.
 *
 * An explicit `options.orgId` always wins over this and skips the account
 * lookup entirely: the caller only reaches this function when no explicit
 * orgId was given.
 */
export function resolveOrgId(organizations) {
  if (organizations.length === 1) return organizations[0].id;
  if (organizations.length === 0) {
    throw new Error(
      "This Neon API key's account belongs to no organizations. Neon accounts created after " +
        "the organization migration always have at least one; check the key is valid.",
    );
  }
  const listed = organizations.map((org) => `${org.name} (${org.id})`).join(", ");
  throw new Error(
    `This Neon API key's account belongs to more than one organization (${listed}). ` +
      "Set orgId in your database config to pick one.",
  );
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

  const orgId = options.orgId ?? resolveOrgId(await findOrganizations(NEON_API_KEY));

  log("-> Locating Neon project and default branch...");
  const project = await findProjectByName(NEON_API_KEY, options.project, orgId);
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
    // Handed back to down() via the lockfile, so teardown can delete the
    // exact branch and Hyperdrive configs this run created instead of
    // re-deriving their names from kraai.config.mjs, which may have changed by
    // the time `kraai down` runs.
    lock: { branchId: branch.id, branchName: branch.name, hyperdrive: hyperdriveIds },
  };
}

export async function down({ name: environmentName, options, services, env, log, lock }) {
  const { NEON_API_KEY, CLOUDFLARE_API_TOKEN, CLOUDFLARE_ACCOUNT_ID } = env;
  const token = CLOUDFLARE_API_TOKEN;
  const accountId = CLOUDFLARE_ACCOUNT_ID;

  log("-> Deleting Hyperdrive configs...");
  for (const service of services) {
    if (!service.hyperdrive) continue;
    const configName = `${environmentName}-${service.key}-hyperdrive`;
    const lockedId = lock?.hyperdrive?.[service.key];
    await tryDelete(`Hyperdrive config "${configName}"`, async () => {
      // The lock already has the exact config id up() created: delete it
      // directly rather than searching for it by name. Falls back to the
      // name-based lookup with no lock (an environment created before this
      // change, or by a custom provider that doesn't return one).
      if (lockedId) {
        await deleteHyperdriveConfig(token, accountId, lockedId);
        return;
      }
      const hyperdrive = await findHyperdriveConfigByName(token, accountId, configName);
      if (!hyperdrive) throw new Error("not found");
      await deleteHyperdriveConfig(token, accountId, hyperdrive.id);
    });
  }

  log("-> Deleting Neon branch...");
  await tryDelete(`Neon branch "${environmentName}"`, async () => {
    const orgId = options.orgId ?? resolveOrgId(await findOrganizations(NEON_API_KEY));
    const project = await findProjectByName(NEON_API_KEY, options.project, orgId);
    // Neon's delete-branch API takes a branch id, not a name, so the lock's
    // branchId is what actually lets this skip a lookup - findBranchByName
    // still runs with no lock, same fallback reasoning as Hyperdrive above.
    if (lock?.branchId) {
      await deleteBranch(NEON_API_KEY, project.id, lock.branchId);
      return;
    }
    const branch = await findBranchByName(NEON_API_KEY, project.id, environmentName);
    if (!branch) {
      console.warn(`  (no Neon branch named "${environmentName}" found, skipping)`);
      return;
    }
    await deleteBranch(NEON_API_KEY, project.id, branch.id);
  });
}
