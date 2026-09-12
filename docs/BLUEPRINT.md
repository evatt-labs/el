# kraai blueprint: environments as manifests

Status: draft for review. Nothing in this document is implemented yet.
Companion file: `docs/workstreams.yaml` (machine-readable ordering and
acceptance criteria). When a decision here changes, change it here first.

## Thesis

Cloudflare has no notion of an environment. `kraai` is that notion: a
named, declared set of Workers and the resources they bind, either
ephemeral (a pull request) or persistent (`dev`, `qa`, `prod`), described
in YAML and applied with one command. Today `kraai` does the ephemeral
half from a JS config. This blueprint turns it into the whole thing.

## Decisions, one line each

| # | Decision | Why |
|---|----------|-----|
| D1 | Manifests are YAML. `yaml` (2.x, ISC, zero deps, pinned exact) is the single runtime dependency exception. | Persistent envs are read and edited by humans and review tools; JS config can't be diffed or validated by anything but Node. |
| D2 | One base file `kraai.yaml` plus overlays in `environments/<name>.yaml`. Hooks stay JS, referenced by path. | Base is the topology; overlay is policy. Hooks need code; policy doesn't. |
| D3 | Environment ownership is recorded in state, not inferred from a name's shape. | The name grammar must relax to allow `prod`; the grammar was the only guard `down` had. |
| D4 | Every resource in state carries `managed: kraai \| external`. `destroy` deletes only `kraai`-managed resources; `external` is detached, never deleted. | Adoption of existing prod is impossible without it. This is the invariant that makes pointing `kraai` at production safe. |
| D5 | State lives in the Cloudflare account, in an R2 bucket, using S3 conditional writes for the lock and for CAS on the state object. Local `.kraai/` remains as a fallback backend. | KV has no conditional write and is eventually consistent. A Durable Object works but means shipping a Worker into every customer account; that is the hosted control plane, not the OSS CLI. |
| D6 | CLI verbs become `plan`, `apply`, `destroy`. `up`/`down` stay as aliases for one minor cycle, then go. | Persistent envs need a diff step before mutation. `up` implies create-only. |
| D7 | `plan` v1 diffs manifest against recorded state only. No live refresh. | Drift detection is deferred, honestly. A refresh that lies is worse than none. |
| D8 | Per-environment naming policy. Ephemeral keeps `{env}-{service}-{binding}`; persistent envs declare `naming.prefix` (default: env name) and may pin Worker names explicitly. | A real production Worker is named `api`, not `prod-api`. Without this, adoption never matches anything. |
| D9 | Routes and custom domains are declared only in persistent overlays and applied only there. Ephemeral envs never receive routes. | Same rule as today (`NEVER_INHERITED_KEYS`), now written down as an invariant. |
| D10 | Unknown keys reject at every level of the manifest. Plain YAML schema, no custom tags, no anchors merged across files. | The `neon:` key silently validated as D1-only for a release. Never again. |
| D11 | Hooks receive the resolved environment (`{ name, kind, persistent, protected, resources }`), not only `{ name, urls, subdomain }`. | One hooks file must serve every environment. |
| D12 | The Action moves to `kraai apply --env preview` with remote state. | Closes the README's admitted gap: down-then-up across pushes has no lockfile today. |
| D13 | Containers come after persistent semantics, as their own workstream. | Agreed earlier; nothing here blocks them, and adding them first would double the surface under change. |
| D14 | `NAME_PATTERN`, the `-pull-request-` infix, and `resourceName()` output are frozen for ephemeral environments. | Changing them orphans every environment already deployed by 0.4.x and 0.5.x. |

## Manifest schema

### `kraai.yaml` (base, required)

```yaml
version: 1                      # schema version, integer, required
hooks: ./kraai.hooks.mjs        # optional; exports configure/seed/open

database:                       # optional; omit for D1-only
  provider: neon
  project: acme
  database: neondb
  appRole: app_user

services:
  auth:
    dir: packages/auth
  api:
    dir: packages/api
    hyperdrive: { binding: HYPERDRIVE }
    d1:     [{ binding: DB }]
    kv:     [{ binding: CACHE }]
    r2:     [{ binding: ASSETS }]
    queues: [{ binding: JOBS, consumer: true }]
```

`services` becomes a map keyed by service key (the key was already unique
and already validated by `/^[a-z][a-z0-9-]*$/`). Everything under a
service keeps the 0.5.0 shape and validation.

### `environments/<name>.yaml` (overlay, one per environment)

```yaml
kind: persistent                # persistent | ephemeral, required
protected: true                 # apply asks; destroy requires typing the name
naming:
  prefix: ""                    # "" means Workers are named by service key alone
  workers:                      # optional explicit pins, win over prefix
    api: acme-api
database:
  mode: root                    # root | fork; fork is today's branch-per-env
routes:                         # persistent only; rejected on ephemeral
  api:
    - pattern: api.acme.com
      custom_domain: true
resources:                      # adopt existing resources by id
  api:
    d1:
      DB: { managed: external, id: 0e1f...-uuid }
    r2:
      ASSETS: { managed: external, name: acme-assets }
```

```yaml
kind: ephemeral
name: from-pull-request         # or omit: kraai generates a name
ttl: 72h                        # optional; gc target, see below
database:
  mode: fork
```

Resolution: base is loaded, overlay is loaded, both schema-validated
independently, then merged by explicit rule per key (never a generic deep
merge). `services` cannot be added or removed by an overlay; an overlay
can only attach `routes` and `resources` to services the base declares.

### Validation rules

- Unknown keys reject at every level, with the path in the error.
- `kind: ephemeral` with `routes`, `protected`, or `naming` rejects.
- `kind: persistent` with `name` or `ttl` rejects.
- `managed: external` requires exactly one of `id` or `name`, per
  resource type (D1 and Hyperdrive are id-addressed; KV, R2, Queues are
  name-addressed).
- `hooks` must resolve to a file inside the project root.
- `version` other than `1` rejects with an upgrade message.

## Name grammar and naming policy

Today `NAME_PATTERN = /^[a-z]{2,15}-[a-z]{2,15}-[a-z]{2,15}-\d{5}$/` is
both the generator's output shape and `down`'s only ownership check.

- Ephemeral names: unchanged. Generated as today; `from-pull-request`
  still yields `<repoword>-pull-request-NNNNN`.
- Persistent names: `/^[a-z][a-z0-9-]{0,30}[a-z0-9]$/`, and the file name
  under `environments/` is the environment name.
- Worker names: `naming.workers[key]`, else `${prefix}-${key}` when prefix
  is non-empty, else `key`. Worker names are limited to 255 chars; the
  workers.dev subdomain additionally requires 63 or fewer and no leading
  or trailing dash (Cloudflare docs, verified 2026-09-12).
- Resource names for `kraai`-managed resources follow `resourceName()`
  with the same prefix rule. `external` resources keep whatever name or
  id they already have.

## State model

One state document per environment, JSON, schema-versioned.

```json
{
  "stateVersion": 2,
  "name": "prod",
  "kind": "persistent",
  "kraaiVersion": "0.6.0",
  "accountId": "...",
  "subdomain": "...",
  "manifestHash": "sha256:...",
  "updatedAt": "2026-09-12T00:00:00Z",
  "services": {
    "api": {
      "worker": { "name": "acme-api", "managed": "kraai" },
      "d1": { "DB": { "id": "...", "name": "...", "managed": "external" } },
      "kv": { "CACHE": { "id": "...", "managed": "kraai" } }
    }
  },
  "database": { "provider": "neon", "lock": { "branchId": "..." } }
}
```

`stateVersion: 1` is today's lockfile. `apply` upgrades v1 in place
(every v1 resource becomes `managed: kraai`, since v1 only ever created).

### Backend

```
kraai-state (R2 bucket, created on first apply if absent)
  envs/<name>/state.json     CAS via If-Match: <etag>
  envs/<name>/lock           create via If-None-Match: *
                             body: { holder, startedAt, pid, hostname }
```

- R2's S3 API returns `412 Precondition Failed` when `If-Match` /
  `If-None-Match` conditions fail on PutObject (Cloudflare R2 docs,
  verified 2026-09-12). This is the same primitive Terraform's S3 backend
  uses for its native lockfile.
- Credentials: the S3 API does not take a bearer token. Access Key ID is
  the API token's `id`; Secret Access Key is `sha256(token value)`
  (R2 docs, verified 2026-09-12). The token id is available from
  `GET /user/tokens/verify`. Whether a general-purpose token with
  `Workers R2 Storage Edit` signs successfully, versus one minted from the
  R2 dashboard, is unverified: workstream `state-backend-r2` must prove it
  live before anything depends on it, and fall back to a documented
  `KRAAI_R2_ACCESS_KEY_ID` / `KRAAI_R2_SECRET_ACCESS_KEY` pair if not.
- Lock TTL: a lock older than 30 minutes is stale; `apply --force-unlock`
  removes it and says who held it.
- KV was rejected: its REST write endpoint has no conditional header, and
  the docs state changes may take up to 60 seconds to become visible.
- Durable Object was deferred: it needs a Worker deployed into the account.
  That Worker is the seed of the hosted control plane and belongs to
  `kraai-api`, not to the CLI.
- Local backend (`.kraai/<name>.state.json`) stays for offline and for
  the test suite; an environment declares its backend in the overlay:
  `state: { backend: r2 | local }`, default `r2` for persistent and for
  the Action, `local` otherwise.

### Ownership invariant

`destroy` and `apply` may delete a resource only if it is present in state
with `managed: kraai`. A resource that exists in the account but is absent
from state is never touched. A resource marked `external` is left alone on
destroy; deleting the managed Worker that bound it is the detach. There is
no flag that overrides this.

Workers are always `managed: kraai`; there is no `external` Worker. Adopting
one is the exception to D7 (no live refresh) and is defined below.

### Worker adoption

Bindings are not inherited by default, so a first `apply` that finds an
existing Worker named `api` and simply deploys over it would drop every
live binding, var, and secret the manifest doesn't declare. Instead:

- `plan` emits `adopt` for a Worker that exists in the account but is
  absent from state. Adopt reads the live Worker's bindings, vars, and
  secret names.
- If any live binding or var is absent from the manifest and the
  overlay's `resources` block, `plan` lists them and `apply` refuses.
  `--allow-binding-drop` overrides, once, and the dropped names are
  written to the plan output.
- Secret names are compared, never values. A secret the manifest's hooks
  don't return is reported as "kept" and left in place; wrangler's
  `secret put` is additive.
- After adopt, the Worker is recorded in state as `managed: kraai` with
  `adoptedAt` set, and every subsequent `apply` treats it as owned.

## Commands

| Command | Does |
|---------|------|
| `kraai plan --env <name>` | Load base + overlay, load state, print create / update / detach / delete per resource. Exit 0 on no changes, 2 on changes, 1 on error. No mutation, no lock. |
| `kraai apply --env <name>` | Acquire lock, run plan, apply it, write state after every mutation (as `up` does today), release lock. `--auto-approve` for CI. |
| `kraai destroy --env <name>` | Acquire lock, delete `managed: kraai` resources in reverse order, leave `external` untouched, delete state only when no warnings. |
| `kraai gc` | List ephemeral environments in the state bucket whose `ttl` has elapsed; destroy each. `--dry-run` lists only. |
| `kraai state list \| show <name>` | Read-only views of the backend. |
| `kraai up` / `kraai down` | Aliases for `apply` / `destroy` on an ephemeral overlay, removed one minor after `apply` ships. |

`--env` selects the overlay file; the environment name is what the overlay
resolves to. For `kind: persistent` they are the same string. For
`name: from-pull-request` the name is `<repoword>-pull-request-NNNNN`,
re-derived from the same PR context on every command, and state is keyed
by that resolved name (`envs/<resolved>/state.json`). `gc` lists by
resolved name.

### `protected`

`protected: true` is not decoration; it survives CI flags.

- `apply`: interactive prompt. `--auto-approve` is accepted only together
  with `--protected-ok`; either alone exits 1 with a message.
- `destroy`: never accepts `--auto-approve`. Interactive runs must type
  the environment name; non-interactive runs must pass
  `--confirm-name <name>` and it must match exactly.

Ensure semantics per resource type (find-or-create by name, adopt by id):

| Resource | Find | Create | Update in place | Delete |
|----------|------|--------|-----------------|--------|
| Worker | by name (adopt, see above) | `wrangler deploy` | `wrangler deploy` | API delete |
| D1 | by name / id | API create | migrations via `d1 migrations apply` (tracked in `d1_migrations`; re-run being a no-op is verify-first) | API delete |
| KV | by title | API create | none | API delete |
| R2 | by name | API create | none | empty then delete (as today) |
| Queue | by name | API create | consumer settings | API delete |
| Hyperdrive | by name / id | API create | origin update | API delete |
| Neon branch | by name / id | provider `up` | none | provider `down` |

## Routes and custom domains

- Applied from the persistent overlay only; ephemeral envs never get them.
- `custom_domain: true` makes wrangler create the DNS record on deploy.
- Zone-level token permissions are required and are not in the README's
  token table today. Workstream `routes` must verify the exact permission
  names against the Cloudflare permissions reference and expand the table
  before shipping. Do not guess them into docs.

## Hooks contract change

```js
export async function configure({ name, urls, subdomain, environment }) {}
export async function seed({ name, urls, environment, ...provider }) {}
export function open({ urls, environment }) {}
```

`environment` is `{ name, kind, persistent, protected, resources }` where
`resources` mirrors the state document's `services` block after ensure.
Existing hooks that ignore the new argument keep working.

## Migration from 0.5.0

1. `kraai.config.mjs` → `kraai.yaml` + `environments/preview.yaml` +
   `kraai.hooks.mjs`. Manual; the surface is one object. `kraai init`
   writes the three files from a 0.5.0 config as a courtesy but is not a
   supported converter beyond that.
2. `.kraai/<name>.lock.json` (v1) is read by `apply` and upgraded to
   state v2 on the local backend. `apply --env preview --migrate-state`
   pushes it to R2. `kraai down <name>` keeps working against v1 files
   until the alias is removed.
3. The Action's `uses: evatt-labs/kraai@vN` step gains `env: preview` as
   its default and needs no other change from users.

## Invariants

- Manifests and state never contain a secret or a connection string.
- Unknown keys reject.
- Ephemeral environments never receive routes.
- Nothing marked `external` is ever deleted.
- Nothing absent from state is ever deleted.
- `plan` never mutates and never locks.
- Ephemeral names and `resourceName()` output are byte-identical to 0.5.0.

## Non-goals for this cycle

- Terraform as an engine, or Terraform state import.
- Drift refresh against live APIs.
- Cross-service queue wiring (unchanged limitation).
- AWS.
- Container-to-container networking (Cloudflare Containers are HTTP-only
  through a Worker; the containers workstream inherits that).

## Open questions for review

1. `services` as a map vs today's array. Map is proposed because overlays
   address services by key. Confirm.
2. Default backend for persistent envs is `r2`. If the credential path in
   D5 fails live, the fallback is a second env-var pair. Acceptable?
3. `protected` prompts on `apply`, not only `destroy`, and `destroy` on a
   protected env never takes `--auto-approve`. Confirm.
5. Worker adoption refuses on undeclared live bindings unless
   `--allow-binding-drop`. Alternative: adopt copies undeclared bindings
   into state as `external` automatically. Proposed: refuse; explicit
   beats inferred for prod.
4. `gc` reads `ttl` from the overlay at apply time and stores the deadline
   in state. Alternative: compute at gc time from `updatedAt`. Proposed:
   store the deadline.
