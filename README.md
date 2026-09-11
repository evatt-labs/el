# el

[![CI](https://github.com/evatt-labs/el/actions/workflows/ci.yml/badge.svg)](https://github.com/evatt-labs/el/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/evatt-labs/el/graph/badge.svg)](https://codecov.io/gh/evatt-labs/el)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/evatt-labs/el/badge)](https://securityscorecards.dev/viewer/?uri=github.com/evatt-labs/el)

Ephemeral full-stack preview environments on Cloudflare Workers. Give it a
name, or let it generate one (`blue-honey-badger-12345` style). It
provisions the D1 databases, KV namespaces, R2 buckets, and queues your
services declare, deploys your Cloudflare Workers under that name, and
wires them together. Tear it down with the same name.

D1 is the default and needs nothing beyond a Cloudflare account. If your
services also need Postgres, configure a database provider (Neon is the
only one built in) and `el` forks a branch and provisions Hyperdrive
before deploying. See [Database providers](#database-providers).

```sh
npx @evatt-labs/el up                         # generates a name
npx @evatt-labs/el up blue-honey-badger-12345  # or pick one
npx @evatt-labs/el down blue-honey-badger-12345
```

## What it actually does

1. **Only if a `database` provider is configured** (see
   [Database providers](#database-providers)): runs that provider's own
   provisioning step, including any Hyperdrive config a service needs.
   Neon forks a branch from your project's default branch (copy-on-write,
   so schema, data, and roles come along automatically, nothing to
   migrate), verifies the role your Workers connect as doesn't have
   `BYPASSRLS` (Neon-created roles inherit it from `neon_superuser`
   membership by default, which silently defeats row-level security; see
   [Notes](#notes) for why this check exists), then creates a Hyperdrive
   config for every service that declares one, bound to that verified
   role, never to the role migrations run as.
2. Provisions a fresh D1 database, KV namespace, R2 bucket, and/or queue
   for every one a service declares. Never points at the resource your
   real deployment uses. D1 gets `migrations_dir` applied automatically
   if your `wrangler.jsonc` declares one.
3. Deploys each configured service as a Cloudflare Worker named
   `{environment}-{service key}`, from an **allowlisted** copy of your
   `wrangler.jsonc`. See [Bindings](#bindings-are-not-inherited-by-default)
   for why that's not "the whole file with a few fields overwritten."
4. Runs your `seed()` hook, if you have one.
5. Opens whatever URLs your `open()` hook returns in your browser.

`el` knows about Cloudflare Workers, D1, KV, R2, and Queues by default,
plus whatever a configured database provider adds (Neon, plus Hyperdrive
for the services that need it). It knows nothing about your application:
auth, seed data, and which URLs are worth a look are up to the hooks you
provide.

## Getting started

### Prerequisites

- Node.js, and `wrangler` installed as a devDependency of the service
  (or workspace root) it deploys. `el` does not fall back to
  `npx wrangler` when no local install is found. In a directory with
  nothing installed, `npx` downloads and runs the latest unpinned,
  unverified `wrangler` in a process already holding your Cloudflare
  credentials, and your database provider's if you have one configured.
  `el` fails with a clear error instead of doing that.
- A Cloudflare account.
- **Only with the Neon provider:** `psql` on `PATH`, used to verify the
  branch's role (step 1 above); Workers Paid on that Cloudflare account,
  which Hyperdrive requires; and a Neon project that already exists, with
  the role your Workers connect as (`appRole` in your `database` config)
  already created on its default branch. `el` forks that branch; it
  doesn't create the role.

### Install

```sh
npm install -D @evatt-labs/el
```

Run it with `npx el up` / `npx el down`. Every example in this README
assumes that.

### Create a Cloudflare API token

Dashboard > profile icon > API Tokens > Create Token > Custom token. `el`
calls the Workers Scripts, KV, R2, D1, and Queues APIs directly, and
`wrangler` needs its own deploy permissions. Grant all of these, scoped to
your account:

| Permission group   | Access |
| ------------------- | ------ |
| Workers Scripts     | Edit   |
| Workers KV Storage  | Edit   |
| Workers R2 Storage  | Edit   |
| D1                  | Edit   |
| Queues              | Edit   |

Add Hyperdrive (Edit) too, only if you're configuring the Neon provider.

Copy the token. This is `CLOUDFLARE_API_TOKEN` below. Cloudflare shows it
once.

### Find your Cloudflare Account ID

Dashboard > Workers & Pages > Overview. Listed in the right sidebar as
Account ID. This is `CLOUDFLARE_ACCOUNT_ID` below.

### Create a Neon API key

**Only with the Neon provider.** Skip this if you're running D1-only.

[console.neon.tech](https://console.neon.tech) > Account settings > API
keys > Create new API key. This is `NEON_API_KEY` below. It's scoped to
your whole Neon account, not one project: treat it like any credential
that can create and destroy databases.

### Set your environment variables

Export these, or put them in a `.env` file in your project root
(gitignored, never committed):

```sh
CLOUDFLARE_API_TOKEN=...
CLOUDFLARE_ACCOUNT_ID=...

# Only if you're configuring the Neon provider:
NEON_API_KEY=...
```

### Write `el.config.mjs`

See [`el.config.mjs`](#elconfigmjs) for the full reference. At minimum: one
service pointing at a directory with its own `wrangler.jsonc`. Add a
`database` block only if a service needs Postgres/Hyperdrive.

### First run

```sh
npx el up
```

Each step in [What it actually does](#what-it-actually-does) logs as it
runs. On success, `el` prints the deployed URLs and the exact teardown
command:

```sh
npx el down blue-honey-badger-12345
```

Teardown is best-effort and safe to call after a partial failure. Rerun
`el down` with the same name.

## Trust model

Running `el up`/`el down` in a directory runs `el.config.mjs` from that
directory as ordinary Node code, with full access to your environment,
including `CLOUDFLARE_API_TOKEN` and any database provider's credentials
(`NEON_API_KEY`, with the Neon provider). That's the extension mechanism
working as intended, the same as `webpack.config.js` or `next.config.js`.
The risk is what's in scope when it runs: credentials that can create and
destroy infrastructure.

Never wire `el` into a CI trigger that runs against an untrusted branch,
such as a `pull_request_target` workflow that checks out a fork's
`head.sha`. That hands an attacker-controlled `el.config.mjs` your
Cloudflare credentials, and your database provider's, directly. Trigger on
`push`/`release` to branches you control, or on `pull_request` (forked PRs
don't receive repository secrets by default).

## `el.config.mjs`

Lives in your project root, next to the services it deploys.

```js
export default {
  // Optional. Omit for D1-only. See Database providers below.
  database: {
    provider: "neon",       // built-in name, or a provider object
    project: "acme",        // everything but `provider` is the provider's own options
    database: "neondb",
    appRole: "app_user",    // least-privilege role; el verifies it lacks BYPASSRLS, see Notes
  },

  services: [
    { key: "auth", dir: "packages/auth" },
    {
      key: "api",
      dir: "packages/api",
      hyperdrive: { binding: "HYPERDRIVE" },
      d1: [{ binding: "DB" }],       // fresh database; migrations_dir applied if declared
      kv: [{ binding: "CACHE" }],    // fresh, empty namespace
      r2: [{ binding: "ASSETS" }],   // fresh, empty bucket
      queues: [{ binding: "JOBS", consumer: true }],  // fresh queue, producer + consumer
    },
  ],

  // Called once, before any deploys. ctx.urls is already populated:
  // workers.dev URLs are deterministic from name + subdomain, known
  // before anything is deployed. Return per-service vars/secrets.
  async configure({ name, urls, subdomain }) {
    return {
      vars: {
        auth: { AUTH_ISSUER: urls.auth },
        api: { AUTH_JWKS_URL: `${urls.auth}/.well-known/jwks.json` },
      },
      secrets: {
        auth: { SOME_SECRET: "..." },
      },
    };
  },

  // Runs after every service is deployed. ownerConnectionString/runSql/
  // quoteLiteral are only present when a database provider is configured
  // and supplies them (Neon does): with no `database` block, seed() gets
  // only { name, urls }. runSql is bound to the branch's owner connection.
  // Never log ownerConnectionString itself. Quote anything that isn't a
  // constant your own code wrote; runSql does not parameterize on its own.
  async seed({ name, urls, ownerConnectionString, runSql, quoteLiteral }) {
    const tenantId = crypto.randomUUID();
    runSql(
      `insert into tenants (id, slug, name) values (${quoteLiteral(tenantId)}, 'smoke', 'Smoke')`,
    );
    return { "smoke tenant": tenantId };
  },

  // Opened in the browser after everything is up.
  open({ urls }) {
    return [`${urls.auth}/health`, `${urls.api}/health`];
  },
};
```

Under `services[]`, only `key` and `dir` are required. `hyperdrive`,
`d1`, `kv`, `r2`, `queues`, and `unsafeInheritBindings` are optional.
`database` is optional too: omit it and `el` is D1-only. All three hooks
are optional.

## Database providers

`database` is optional. Omit it and `el` is D1-only: no `NEON_API_KEY`, no
`psql`, no Hyperdrive, no Workers Paid requirement. Configure it when a
service needs Postgres.

`database.provider` is either the name of a built-in provider or a
provider object you supply yourself:

```js
database: {
  provider: "neon",   // "neon" is the only built-in, registered in
                       // src/providers/index.mjs
  project: "acme",    // everything except `provider` is passed to it as
  database: "neondb", // `options`: for Neon, project, database, appRole
  appRole: "app_user",
},
```

A provider object looks like this:

```js
{
  name: "my-provider",               // string, required
  requiredEnv: ["MY_PROVIDER_KEY"],  // string[], may be empty
  validate(options) { /* throw a clear Error on bad options */ },
  async up({ name, options, services, env, log }) {
    return {
      bindings(service) { /* return { hyperdrive: [...] } or {} */ },
      seed: { /* spread into seed()'s argument, if you have one */ },
      summary: ["..."],  // lines added to the final "is live" block
    };
  },
  async down({ name, options, services, env, log }) { /* best-effort */ },
}
```

`env` carries `CLOUDFLARE_API_TOKEN`/`CLOUDFLARE_ACCOUNT_ID` plus whatever
`requiredEnv` names. Hyperdrive create/delete is a Cloudflare API call
keyed on a connection string only the provider knows, so a provider that
wants Hyperdrive needs both. `log` is `console.log`, passed through so a
provider's own step messages match `el`'s `-> ...` style. `down()` should
never throw for a resource that's already gone; see `tryDelete` in
`src/try-delete.mjs` for the pattern the Neon provider uses.

## Bindings are not inherited by default

An ephemeral environment deploys from an allowlist of your
`wrangler.jsonc`: `main`, `compatibility_date`, `compatibility_flags`,
`observability`, `durable_objects`/`migrations`, plus whatever
`configure()` returns for `vars` and whatever `el` itself provisioned
(Hyperdrive, D1, KV, R2, queues). Nothing else in the file is carried
forward.

This wasn't always true. The first version of `el` mutated the loaded
config in place, overwriting only `name`/`vars`/`hyperdrive` and leaving
everything else, including any D1 database, KV namespace, R2 bucket, or
queue binding, untouched. A disposable preview environment had live
read/write access to whatever production resources those bindings
pointed at. A security audit caught it. This section exists because of
that.

Every stateful binding has three ways to be handled, in order of
preference:

1. **Declare it in `el.config.mjs`** (`d1`/`kv`/`r2`/`queues` on the
   service). `el` provisions a fresh, environment-scoped resource and
   deploys with that instead. This is almost always what you want.
2. **`unsafeInheritBindings: true`**. Carries the committed binding
   forward verbatim, pointed at whatever it points at in production.
3. **Neither**. `el` refuses to deploy and tells you which binding
   forced the refusal.

`durable_objects` needs none of the above: a DO class lives inside the
Worker script being deployed, not a separately provisioned resource, so
a fresh Worker name under a new environment gets fresh DO storage
automatically. It's always carried forward, along with its paired
`migrations` block.

`routes`, `route`, and `triggers` (cron schedules) are never carried
forward, even with `unsafeInheritBindings`. Reassigning a route or
trigger during an ephemeral deploy can redirect production traffic to a
disposable Worker: a sharper failure mode than an ephemeral Worker
reading prod data.

### Queue consumers need a real handler

Marking a queue binding `consumer: true` only succeeds if the deployed
Worker's own code exports a `queue()` handler. Cloudflare rejects
attaching a consumer trigger to a Worker that doesn't implement one, same
as a real deployment. Verified by testing a live deploy: the producer
binding and every other resource type deployed successfully; only the
consumer trigger failed, with a `[code: 11001]` error from Cloudflare. If
your service doesn't have a `queue()` export yet, leave `consumer` unset
(or `false`) and it still gets the producer binding.

### R2 buckets are emptied before deletion

R2 refuses to delete a non-empty bucket. A preview app may have written
real objects to an ephemeral bucket during testing, so `el down` lists
and deletes every object first. Best-effort, same as the rest of
teardown.

## Never

`el` never prints a connection string, and no hook should either.
`ownerConnectionString` exists so a `seed()` hook can build its own SQL.
Prefer `runSql`/`quoteLiteral`; treat the raw string as write-only if you
use it directly.

`.el-deploy-*.json`, the mutated wrangler config `el` writes next to your
real one during a deploy, never contains secrets (those go over stdin to
`wrangler secret put`, never through this file). It does contain
whatever `vars` your `configure()` hook returned. Gitignore
`.el-deploy-*.json` in any repo this runs in, in case a crash leaves one
behind before cleanup runs.

## Known limitations

- **`el down <name>` has no ownership check.** Anyone with a valid
  `CLOUDFLARE_API_TOKEN` (and, with a database provider configured, its
  credentials too) for the target project can tear down any environment
  matching a valid name. No tagging or provenance tracking yet. Don't give
  a CI workflow broader credentials than the people who can trigger it
  should have, and don't reuse a name a human picked by hand for something
  you want to keep.
- `open()`'s URLs are validated to be `http:`/`https:` before opening,
  but `el` cannot audit what a hook itself does with your loaded
  environment. See [Trust model](#trust-model).
- **Queues are per-service, not shared.** A queue produced by one
  service and consumed by a different one in the same environment isn't
  wired automatically, same limitation Hyperdrive has. Declare the
  queue binding on whichever service needs it. Cross-service wiring
  isn't built yet.
- If your `wrangler.jsonc` declares more than one queue consumer, `el`
  can't tell which consumer's settings (`max_batch_size`, retry policy,
  dead letter queue) belong to which binding: there's no name/binding
  key at the consumer level to match on. With exactly one, it's copied
  forward. With more than one, provisioned consumers get wrangler's
  defaults, not your production settings. Override via `configure()` if
  that's not right.

## Notes

The Neon provider's `BYPASSRLS` check exists because the first version of
this tool (back when Neon was the only database it knew about, not yet a
provider) didn't have it. A Hyperdrive config was pointed at a Neon
branch's owner role instead of a restricted application role, and row-level
security was silently bypassed for every request until a live
cross-service test caught it. No unit test would have found it; only
running the real, deployed stack did. That's also why the provider
verifies the role empirically against the actual branch on every run,
rather than trusting that whatever role name you configured is safe.
