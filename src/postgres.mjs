// Postgres helpers shared by the core provisioning flow and by consumer
// hooks (seed, etc.) — all built on `psql`, so no pg driver dependency.

import { execFileSync } from "node:child_process";

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

export async function waitForConnectable(connectionUri, attempts = 15) {
  for (let i = 0; i < attempts; i++) {
    try {
      execFileSync("psql", [connectionUri, "-c", "select 1"], { stdio: "pipe" });
      return;
    } catch {
      await sleep(2000);
    }
  }
  throw new Error("Database did not become connectable in time");
}

/**
 * Defensive, not decorative: Neon-created roles inherit BYPASSRLS from
 * neon_superuser membership by default, which silently defeats row-level
 * security for that role. Verified the hard way once — see the README.
 */
export function assertNoBypassRls(connectionUri, role) {
  const output = execFileSync(
    "psql",
    [connectionUri, "-tAc", `select rolbypassrls from pg_roles where rolname = '${role}'`],
    { encoding: "utf8" },
  ).trim();
  if (output !== "f") {
    throw new Error(
      `Role "${role}" has BYPASSRLS=${output} — refusing to continue. ` +
        `Row-level security would be silently defeated for this role.`,
    );
  }
}

/**
 * Runs a SQL statement and returns stdout — for hooks that need to seed
 * data. Never print `connectionUri` yourself; it is a live credential. This
 * function exists so hooks don't have to shell out to psql directly.
 */
export function runSql(connectionUri, sql) {
  return execFileSync("psql", [connectionUri, "-tAc", sql], { encoding: "utf8" }).trim();
}
