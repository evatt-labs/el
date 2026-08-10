# el

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
   `{environment}-{service key}`, running whatever `configure()` hook you
   provide to compute its vars and secrets.
5. Runs your `seed()` hook, if you have one, against the branch.
6. Opens whatever URLs your `open()` hook returns in your browser.

`el` knows about Neon, Cloudflare Workers, and Hyperdrive. It knows nothing
about your application — auth, seed data, and which URLs are worth a look
are entirely up to the hooks you provide.

## Requirements

- `wrangler` and `psql` on `PATH`
- A Cloudflare account with Workers Paid (Hyperdrive requires it)
- `NEON_API_KEY`, `CLOUDFLARE_API_TOKEN` (needs Hyperdrive: Edit),
  `CLOUDFLARE_ACCOUNT_ID` — in the environment or a `.env` file in your
  project root

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
  // owner connection — never log ownerConnectionString itself. Whatever you
  // return here gets printed in the summary.
  async seed({ name, urls, ownerConnectionString, runSql }) {
    const tenantId = crypto.randomUUID();
    runSql(`insert into tenants (id, slug, name) values ('${tenantId}','smoke','Smoke')`);
    return { "smoke tenant": tenantId };
  },

  // Opened in the browser after everything is up.
  open({ urls }) {
    return [`${urls.auth}/health`, `${urls.api}/health`];
  },
};
```

Every field under `services[]` and `neon` is required except `hyperdrive`.
All three hooks are optional.

## Never

`el` will never print a connection string, and no hook should either.
`ownerConnectionString`/`runSql` exist so a `seed()` hook can touch the
database without shelling out itself — treat the string as write-only.

## Notes

The `BYPASSRLS` check in step 2 exists because the first version of this
tool didn't have it: a Hyperdrive config was pointed at a Neon branch's
owner role instead of a restricted application role, and row-level security
was silently bypassed for every request until a live cross-service test
caught it. No unit test would have found it — only running the real,
deployed stack did. That's also why `el` verifies the role empirically
against the actual branch on every run, rather than trusting that whatever
role name you configured is safe.
