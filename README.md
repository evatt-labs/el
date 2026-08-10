# el

[![CI](https://github.com/evatt-labs/el/actions/workflows/ci.yml/badge.svg)](https://github.com/evatt-labs/el/actions/workflows/ci.yml)

Ephemeral full-stack preview environments on Cloudflare Workers + Neon.
Give it a name — or let it generate one, `blue-honey-badger-12345` style —
and it forks a Neon database branch, deploys your Cloudflare Workers under
that name, and wires them together. Tear it down with the same name later.

```sh
npx @evatt-labs/el up                         # generates a name
npx @evatt-labs/el up blue-honey-badger-12345  # or pick one
npx @evatt-labs/el down blue-honey-badger-12345
```

## What it actually does

1. Forks a Neon branch from your project's default branch. Neon branches are
   copy-on-write — schema, data, and roles come along automatically, so
   there's nothing to migrate.
2. Verifies the role your Workers will connect as doesn't have `BYPASSRLS`.
   Neon-created roles inherit it from `neon_superuser` membership by
   default, which would silently defeat row-level security. This check
   exists because that happened once — see [Notes](#notes).
3. Creates a Hyperdrive config per service that needs one, bound to that
   verified role — never to the role migrations run as.
4. Deploys each configured service as a Cloudflare Worker named
   `{environment}-{service key}`, from an **allowlisted** copy of your
   `wrangler.jsonc` — see [Bindings](#bindings-are-not-inherited-by-default)
   below for why that's not "the whole file with a few fields overwritten."
5. Runs your `seed()` hook, if you have one, against the branch.
6. Opens whatever URLs your `open()` hook returns in your browser.

`el` knows about Neon, Cloudflare Workers, and Hyperdrive. It knows nothing
about your application — auth, seed data, and which URLs are worth a look
are entirely up to the hooks you provide.

## Requirements

- `wrangler` as a **devDependency** of the service (or workspace root) it
  deploys, and `psql` on `PATH`. `el` deliberately does not fall back to
  `npx wrangler` when no local install is found — in a directory with
  nothing installed, `npx` silently downloads and runs the latest,
  unpinned, unverified `wrangler` from the registry, in a process already
  holding your Cloudflare and Neon credentials. It fails with a clear
  message instead.
- A Cloudflare account with Workers Paid (Hyperdrive requires it)
- `NEON_API_KEY`, `CLOUDFLARE_API_TOKEN` (needs Hyperdrive: Edit),
  `CLOUDFLARE_ACCOUNT_ID` — in the environment or a `.env` file in your
  project root

## Trust model

**Running `el up`/`el down` in a directory means running `el.config.mjs`
from that directory as ordinary Node code, with full access to your
environment** — including `NEON_API_KEY` and `CLOUDFLARE_API_TOKEN`. That's
the extension mechanism working as intended, the same way any
`webpack.config.js` or `next.config.js` runs arbitrary code. The sharper
edge here is what's in scope when it does: real cloud credentials capable of
creating and destroying infrastructure.

**Never wire `el` into a CI trigger that runs against an untrusted branch**
— a `pull_request_target` workflow that checks out a fork's `head.sha`, for
instance. That hands an attacker-controlled `el.config.mjs` your Neon and
Cloudflare credentials directly. Trigger on `push`/`release` to branches you
control, or on `pull_request` (which does not receive repository secrets
for forked PRs by default).

## `el.config.mjs`

Lives in your project root, next to the services it deploys.

```js
export default {
  neon: {
    project: "acme",       // Neon project name
    database: "neondb",
    appRole: "app_user",   // least-privilege role — see step 2 above
  },

  services: [
    { key: "auth", dir: "packages/auth" },
    { key: "api", dir: "packages/api", hyperdrive: { binding: "HYPERDRIVE" } },
  ],

  // Called once, before any deploys. ctx.urls is already populated —
  // workers.dev URLs are deterministic from name + subdomain, known before
  // anything is actually deployed. Return per-service vars/secrets.
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

  // Runs after every service is deployed. runSql is bound to the branch's
  // owner connection — never log ownerConnectionString itself. Quote
  // anything that isn't a constant your own code wrote; runSql does not
  // parameterize on its own.
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

Every field under `services[]` and `neon` is required except `hyperdrive`
and `unsafeInheritBindings`. All three hooks are optional.

## Bindings are not inherited by default

An ephemeral environment deploys from an **allowlist** of your
`wrangler.jsonc` — `main`, `compatibility_date`, `compatibility_flags`,
`observability`, plus whatever `configure()` returns for `vars` and the
Hyperdrive binding `el` creates itself. Nothing else in the file is carried
forward.

This is deliberate, and it wasn't always true: the first version of `el`
mutated the loaded config in place, overwriting only `name`/`vars`/
`hyperdrive` and leaving everything else — including any D1 database, KV
namespace, R2 bucket, queue, or Durable Object binding — untouched. An
"ephemeral, disposable" preview environment had live read/write access to
whatever production resources those bindings pointed at. A security audit
caught it; nothing about it was intentional, and it's the reason this
section exists.

If a service genuinely needs one of those bindings in its preview
environment, opt in explicitly:

```js
services: [
  { key: "worker", dir: "packages/worker", unsafeInheritBindings: true },
],
```

`routes`, `route`, and `triggers` (cron schedules) are **never** carried
forward, even with that flag — reassigning a route or trigger during an
ephemeral deploy can redirect real production traffic to a disposable
Worker, which is a sharper failure mode than an ephemeral Worker reading
prod data.

## Releasing

`.github/workflows/publish.yml` publishes on any `v*` tag push, using npm's
[trusted publishing](https://docs.npmjs.com/trusted-publishers) (OIDC) — no
`NPM_TOKEN` secret lives in this repo.

That only works once a package version already exists on the registry:
trusted-publisher configuration lives on the package's own settings page on
npmjs.com, so nothing can bootstrap its own trust on a name that's never
been published. The first release is manual:

```sh
npm publish --provenance --access public
```

...then, on npmjs.com, add `evatt-labs/el` and `publish.yml` as this
package's trusted publisher (or `npm trust github @evatt-labs/el`). Every
tag push after that publishes with no stored credential at all.

## Never

`el` will never print a connection string, and no hook should either.
`ownerConnectionString` exists so a `seed()` hook can build its own SQL —
prefer `runSql`/`quoteLiteral`, and treat the raw string as write-only if
you use it directly.

`.el-deploy-*.json` — the mutated wrangler config `el` writes next to your
real one during a deploy — never contains secrets (those go over stdin to
`wrangler secret put`, never through this file), but it does contain
whatever `vars` your `configure()` hook returned. **Gitignore
`.el-deploy-*.json`** in any repo this runs in, in case a crash leaves one
behind before cleanup runs.

## Known limitations

- **`el down <name>` has no ownership check.** Anyone with valid
  `NEON_API_KEY`/`CLOUDFLARE_API_TOKEN` for the target project can tear down
  any environment matching a valid name — there's no tagging or provenance
  tracking yet. In practice this means: don't give broader credentials to a
  CI workflow than the people who can trigger it should have, and don't
  reuse a name a human picked by hand for something you want to keep.
- `open()`'s URLs are validated to be `http:`/`https:` before opening, but
  `el` cannot audit what a hook itself does with your loaded environment —
  see [Trust model](#trust-model).

## Notes

The `BYPASSRLS` check exists because the first version of this tool didn't
have it: a Hyperdrive config was pointed at a Neon branch's owner role
instead of a restricted application role, and row-level security was
silently bypassed for every request until a live cross-service test caught
it. No unit test would have found it — only running the real, deployed
stack did. That's also why `el` verifies the role empirically against the
actual branch on every run, rather than trusting that whatever role name
you configured is safe.
