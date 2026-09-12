// Postgres helpers shared by the core provisioning flow and by consumer
// hooks (seed, etc.) — all built on `psql`, so no pg driver dependency.
//
// Connection details always go through psql's PG* environment variables,
// never as a positional argument. Two independent reasons, both found by
// testing rather than assumed:
//
//   1. A CLI argument containing a live credential is visible to any other
//      local process via /proc/<pid>/cmdline (or `ps`) for the argument's
//      entire lifetime — env vars of another process are not readable that
//      way on Linux.
//   2. When the child process fails, execFileSync throws an Error whose
//      .message is literally "Command failed: " + the full argv — so a
//      credential in argv leaks through error handling even if nothing
//      ever explicitly logs it. See scrubbedExec below.

import { execFileSync } from "node:child_process";
import { parseConnectionUri } from "./connection-uri.mjs";

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function pgEnv(connectionUri) {
  const { host, port, user, password, database, sslmode } = parseConnectionUri(connectionUri);
  return {
    ...process.env,
    PGHOST: host,
    PGPORT: String(port),
    PGUSER: user,
    PGPASSWORD: password,
    PGDATABASE: database,
    PGSSLMODE: sslmode,
  };
}

/**
 * Runs psql and, on failure, throws a fresh Error carrying only what's safe
 * to surface — never the original Error object. Node attaches the full argv
 * to `.message`, and also to `.stack`/`.output`/`.stderr`; rethrowing the
 * original by reference (or spreading it) would leak through whichever of
 * those a caller happens to print. A brand new Error with a hand-written
 * message is the only way to guarantee none of that survives.
 */
function scrubbedExec(args, options) {
  try {
    return execFileSync("psql", args, { stdio: ["ignore", "pipe", "pipe"], ...options });
  } catch (error) {
    throw new Error(
      `psql failed (exit ${String(error.status ?? "?")}). Run with the same PG* env vars ` +
        `manually to see the original error — it is deliberately not included here because ` +
        `it may echo back connection details.`,
    );
  }
}

export async function waitForConnectable(connectionUri, attempts = 15) {
  const env = pgEnv(connectionUri);
  for (let i = 0; i < attempts; i++) {
    try {
      scrubbedExec(["-c", "select 1"], { env });
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
  const env = pgEnv(connectionUri);
  const output = scrubbedExec(
    ["-tAc", `select rolbypassrls from pg_roles where rolname = '${role}'`],
    { env },
  )
    .toString()
    .trim();
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
 *
 * `sql` gets no parameterization of its own — `runSql` is one `psql -c`
 * call, not a prepared-statement API. Build the string with `quoteLiteral`
 * below for any value that isn't a constant your own code wrote.
 */
export function runSql(connectionUri, sql) {
  const env = pgEnv(connectionUri);
  return scrubbedExec(["-tAc", sql], { env }).toString().trim();
}

/**
 * Quotes a value as a Postgres string literal, the way `quote_literal()`
 * does server-side: wraps in single quotes, doubles any single quote inside.
 * Use this for anything interpolated into a `runSql` call that isn't a
 * hard-coded constant — a tenant name derived from a PR title, for example.
 */
export function quoteLiteral(value) {
  return `'${String(value).replaceAll("'", "''")}'`;
}
