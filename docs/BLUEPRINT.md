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

End state: every Cloudflare hosting primitive is expressible inside an
environment, and the same manifest grammar later spans AWS, Azure, and
GCP, each a bring-your-own account. This cycle ships the six Cloudflare
primitives that exist today plus the machinery (D15, D16) that makes each
further primitive, provider, or extension author an additive module.

## Decisions, one line each

| # | Decision | Why |
|---|----------|-----|
| D1 | Manifests are YAML. `yaml` (2.x, ISC, zero deps, pinned exact) is the single runtime dependency exception. | Persistent envs are read and edited by humans and review tools; JS config can't be diffed or validated by anything but Node. |
| D2 | One base file `kraai.yaml` plus overlays in `environments/<name>.yaml`. Hooks stay JS, referenced by path. | Base is the topology; overlay is policy. Hooks need code; policy doesn't. |
| D3 | Environment ownership is recorded in state, not inferred from a name's shape. | The name grammar must relax to allow `prod`; the grammar was the only guard `down` had. |
| D4 | Every resource in state carries `managed: kraai \| external`. `destroy` deletes only `kraai`-managed resources; `external` is detached, never deleted. | Adoption of existing prod is impossible without it. This is the invariant that makes pointing `kraai` at production safe. |
| D5 | Default state backend is a generic S3-compatible object store — Cloudflare R2, MinIO, AWS S3, Backblaze B2, or anything else speaking the S3 API — authenticated with dedicated storage credentials (access key id + secret access key), never a Cloudflare API token. Conditional writes (`If-Match`/`If-None-Match`) give CAS on the state object and the lock. `local` (`.kraai/`) remains a backend option. | Deriving S3 credentials from a Cloudflare API token locks the default backend to one vendor's account before D16/D18 exist to make that a real choice, and ties storage auth to a tool this generic client shouldn't need to know about. S3-compat is the de facto standard object-storage protocol; a dedicated key pair is the standard way to authenticate to it, on any target. |
| D6 | CLI verbs become `plan`, `apply`, `destroy`. `up`/`down` stay as aliases for one minor cycle, then go. | Persistent envs need a diff step before mutation. `up` implies create-only. |
| D7 | `plan` v1 diffs manifest against recorded state only. No live refresh. | Drift detection is deferred, honestly. A refresh that lies is worse than none. |
| D8 | Per-environment naming policy. Ephemeral keeps `{env}-{service}-{binding}`; persistent envs declare `naming.prefix` (default: env name) and may pin Worker names explicitly. | A real production Worker is named `api`, not `prod-api`. Without this, adoption never matches anything. |
| D9 | Routes and custom domains are declared only in persistent overlays and applied only there. Ephemeral envs never receive routes. | Same rule as today (`NEVER_INHERITED_KEYS`), now written down as an invariant. |
| D10 | Unknown keys reject at every level of the manifest. Plain YAML schema, no custom tags, no anchors merged across files. | The `neon:` key silently validated as D1-only for a release. Never again. |
| D11 | Hooks receive the resolved environment (`{ name, kind, persistent, protected, resources }`), not only `{ name, urls, subdomain }`. | One hooks file must serve every environment. |
| D12 | The Action moves to `kraai apply --env preview` with remote state. | Closes the README's admitted gap: down-then-up across pushes has no lockfile today. |
| D13 | Containers come after persistent semantics, as their own workstream. | Agreed earlier; nothing here blocks them, and adding them first would double the surface under change. |
| D14 | `NAME_PATTERN`, the `-pull-request-` infix, and `resourceName()` output are frozen for ephemeral environments. | Changing them orphans every environment already deployed by 0.4.x and 0.5.x. |
| D15 | The resource model is open-ended by construction: one `ensure()` module plus one schema block per primitive, registered in a table keyed `provider/type` that `plan`/`apply`/`destroy` iterate. Adding a primitive never touches the verbs. | The end state is every Cloudflare hosting feature inside an environment (Vectorize, Workflows, AI Gateway, Images, Stream, Email Routing, Access, zones). A fixed enum would be rewritten at each one. |
| D16 | Every service carries `provider:`, default `cloudflare`, omitted in every example today. State records `provider` on every resource. Credentials and the state backend are resolved per provider. | AWS, Azure, and GCP follow, BYO account for each. A service belongs to one cloud; resource type names (`d1`, `sqs`) already namespace themselves, so the key is the only schema cost, and it must exist before the first manifest is written by a user. |
| D17 | Per-resource schemas are generated from each provider's own machine-readable source, never hand-transcribed: wrangler's config JSON Schema and Cloudflare's per-product OpenAPI for `cloudflare`; CloudFormation resource provider schemas (the Cloud Control API surface) for `aws`; `azure-rest-api-specs` OpenAPI for `azure`; Discovery Documents for `gcp`. Every generated schema also accepts `raw:`, merged verbatim into the underlying create/update call, so a field is always representable even before `kraai` gives it a native name. | The ask is Terraform-grade coverage of every option a provider exposes. Hand-authoring that per resource drifts the moment the provider ships a field; generating it from the same source Terraform/Bicep/Cloud Control already use is the only way to keep up, and every major cloud publishes one (verified 2026-09-12: AWS CloudFormation registry schemas + Cloud Control API, Azure's OpenAPI specs feeding Bicep's type system, GCP Discovery Documents feeding `magic-modules`). |
| D18 | One extension surface for the whole tool, not a separate bespoke registry per layer: a plugin is a module (npm package or local file) declared in `plugins:` that can register providers (D16), resource types (D15), state backends (D5), and middleware, in any combination. Built-ins (the `cloudflare` provider and its resource types, the `s3-compatible` and `local` state backends) are themselves plugin modules, loaded before the project's own `plugins:` list. A later registration for the same key overrides an earlier one; overriding a built-in logs a warning and never fails. | "Pluggable, extendable, overridable" was asked for as a property of the whole tool, not the database layer alone (0.4.0's provider registry) or the cloud layer alone (D16). One registry, one override rule, one entry path — a third-party plugin and an Evatt Labs built-in go through the identical mechanism. |
| D19 | Lifecycle hooks expand beyond `configure`/`seed`/`open` (which keep their existing, narrower contracts) to named pre/post hooks around every verb and every per-resource operation: `preValidate`, `postValidate`, `prePlan`, `postPlan`, `preApply`, `postApply`, `preResourceApply`, `postResourceApply`, `preDestroy`, `postDestroy`, `preResourceDestroy`, `postResourceDestroy`, `onError`. All optional. Middleware (D18) wraps the same seams for reusable, installable, cross-project concerns; hooks stay this-project-specific. | Full lifecycle coverage was asked for explicitly. Keeping `configure`/`seed`/`open` as their own ergonomic, specific-contract hooks — rather than folding them into generic pre/post pairs — means the common case stays as easy as it is today. |

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
service keeps the 0.5.0 shape and validation. `provider` (D16) is
accepted on every service; the only valid value this cycle is
`cloudflare`, and any other value rejects with "provider X is not
supported in kraai <version>". Which resource keys a service may declare
is decided by its provider's registry entries, so `d1` under
`provider: aws` is an unknown key.

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

### Schema generation strategy (D17)

Each provider module's resource schemas are build artifacts, not prose:
a generator script pulls the provider's own machine-readable schema
(wrangler's JSON Schema / OpenAPI for `cloudflare`, CloudFormation
resource provider schemas for `aws`, `azure-rest-api-specs` for
`azure`, Discovery Documents for `gcp`) and emits the validator plus
the field table this document would otherwise hand-transcribe. Nothing
in BLUEPRINT.md enumerates a provider's full field set for that reason;
the generator is the source of truth, checked into the repo as generated
output with its source commit/version recorded, regenerated on demand.

Every generated schema accepts a sibling `raw:` object, merged directly
into the underlying create/update request after `kraai`'s own fields are
applied and after validation of everything else. `raw:` exists so a
field newly added by a provider, or one `kraai` hasn't modeled a
friendly name for yet, is never a blocker — it is never itself
schema-validated beyond "must be an object."

### Vocabulary (tentative — from the 2026-09-12 design session, not yet
locked)

The examples elsewhere in this document use the 0.5.0 Cloudflare-specific
field names (`hyperdrive`, `d1`, `kv`, `r2`, `queues`) because they
predate this session's vendor-neutral direction. The direction agreed so
far, pending the generator work in D17 actually landing:

- `providers: { compute: cloudflare, postgres: neon }` at the manifest
  root names *who fulfils what*; vendor names appear nowhere else.
- Services declare capabilities, not vendor resources: tentatively
  `databases` (with `engine: sqlite | postgres`, replacing bare `d1`
  and the top-level `database` block), `keyvalue` (replacing `kv`),
  `objects` (replacing `r2`), `queues` (unchanged name, already
  generic).
- `binding` is the fixed, developer-chosen variable name the service
  code reads (`env.DB`); it is never randomized and never appears in
  the generated resource name. The underlying resource's actual name/id
  is generated per `resourceName()` (ephemeral) or `naming` (persistent,
  D8) and is what state and adoption track.
- Engine-inherent tuning (Hyperdrive-style query `caching`, queue
  consumer settings) nests under the resource entry itself, keyed by
  concept, not by vendor. Vendor-only knobs nest under
  `providers.<capability>` config instead.
- Hyperdrive is not a user-facing concept: it is how Cloudflare
  implements `engine: postgres`, provisioned invisibly the way D1 is
  invisibly how it implements `engine: sqlite`.

This vocabulary does not update the schema examples in this revision;
that's the next review pass, once D17's generator exists to check the
examples against instead of hand-typing them again.

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
      "provider": "cloudflare",
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

Backends are plugins (D18); `s3-compatible` and `local` ship as
built-ins.

```
<bucket>/<prefix>            bucket, prefix, endpoint: backend config
  envs/<name>/state.json     CAS via If-Match: <etag>
  envs/<name>/lock           create via If-None-Match: *
                             body: { holder, startedAt, pid, hostname }
```

- `s3-compatible` speaks plain S3 SigV4 (stdlib `node:crypto`, no AWS
  SDK) against any endpoint implementing it — Cloudflare R2, MinIO, AWS
  S3, Backblaze B2, DigitalOcean Spaces, and so on. Config: `endpoint`,
  `bucket`, `region` (default `auto`), and credentials —
  `accessKeyId` / `secretAccessKey`, read from
  `KRAAI_STATE_ACCESS_KEY_ID` / `KRAAI_STATE_SECRET_ACCESS_KEY` when not
  given inline. These are dedicated storage credentials, never a
  Cloudflare API token or any other cloud's own identity token — R2's S3
  API itself needs a key pair minted for that purpose, not an account API
  token (Cloudflare R2 docs, verified 2026-09-12), and a generic backend
  can't assume every target even has a notion of "API token."
- Conditional PutObject (`If-Match` / `If-None-Match`, `412 Precondition
  Failed` on mismatch) is part of the S3 API itself, not an R2-specific
  extension — the same primitive Terraform's S3 backend uses for its
  native lockfile, and why S3-compatible is the default over any single
  cloud's own storage API.
- Lock TTL: a lock older than 30 minutes is stale; `apply --force-unlock`
  removes it and says who held it.
- A Cloudflare-KV-backed option was considered and rejected: its REST
  write endpoint has no conditional header, and Cloudflare's own docs say
  changes may take up to 60 seconds to become visible.
- A Durable-Object-backed option was deferred: it needs a Worker deployed
  into the account. That Worker is the seed of the hosted control plane
  and belongs to `kraai-api`, not the OSS CLI — nothing stops it being
  added later as another `stateBackends` plugin.
- `local` (`.kraai/<name>.state.json`) stays for offline and the test
  suite. An environment declares its backend in the overlay:
  `state: { backend: s3-compatible | local, ...backendConfig }`, default
  `s3-compatible` for persistent environments and the Action, `local`
  otherwise.
- State documents carry no backend-specific fields; swapping backends, or
  writing a third one as a plugin, never changes the state schema.

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

## Plugins, middleware, and lifecycle hooks

### Plugin shape

```js
export default {
  name: "kraai-plugin-example",
  providers: {                         // extends D16: capability -> vendor -> impl
    postgres: { neon: neonProvider },
  },
  resourceTypes: {                     // extends D15: "provider/type" -> module
    "cloudflare/vectorize": vectorizeResource,
  },
  stateBackends: {                     // extends D5: backend name -> impl
    "s3-compatible": s3CompatibleBackend,
  },
  middleware: [
    async (ctx, next) => { /* before */ await next(); /* after */ },
  ],
};
```

Declared in `kraai.yaml`:

```yaml
plugins:
  - kraai-plugin-example        # resolved as an npm package
  - ./plugins/cost-guard.mjs    # or a local file, relative to the project root
```

Every built-in — the `cloudflare` provider and its resource types, the
`s3-compatible` and `local` state backends — is itself one of these
plugin objects, loaded before the project's own `plugins:` list. A later
registration for the same key (capability/vendor pair, `provider/type`
resource key, or backend name) overrides an earlier one; overriding a
built-in logs a warning naming the plugin that did it, and never fails
validation. This is what makes every layer actually overridable, not
merely extendable in the single direction of "add a new one."

### Middleware

Middleware wraps four seams, Koa-style (`async (ctx, next) => {}`;
`next()` invokes the next middleware, or the underlying operation if
there isn't one): `resource.ensure`, `resource.destroy`, `state.read`,
`state.write`. Declared middleware from every loaded plugin runs in
`plugins:` array order, outermost first. `ctx` carries
`{ operation, environment, manifest, resource? }`; a middleware can
inspect or replace `ctx`, short-circuit by not calling `next()`, or wrap
the result. Uses: audit logging, cost estimation, policy enforcement
("no `objects` buckets in `prod` over 10 GiB"), notifications.
Middleware is the extension point for concerns that don't belong to one
project's own hooks file — reusable and installable, not project-specific.

### Lifecycle hooks

`hooks: ./kraai.hooks.mjs` (D2) exports any subset of:

```js
export async function preValidate({ environment }) {}
export async function postValidate({ environment }) {}
export async function prePlan({ environment }) {}
export async function postPlan({ environment, diff }) {}
export async function preApply({ environment }) {}
export async function postApply({ environment, result }) {}
export async function preResourceApply({ environment, resource }) {}
export async function postResourceApply({ environment, resource, result }) {}
export async function preDestroy({ environment }) {}
export async function postDestroy({ environment }) {}
export async function preResourceDestroy({ environment, resource }) {}
export async function postResourceDestroy({ environment, resource }) {}
export async function onError({ environment, error }) {}

// unchanged, specific contracts — still the ergonomic path for the
// common case, not subsumed into the generic pre/post pairs above:
export async function configure({ name, urls, subdomain, environment }) {}
export async function seed({ name, urls, environment, ...provider }) {}
export function open({ urls, environment }) {}
```

All optional; a hooks file implementing none of the new ones behaves
exactly as a 0.5.0 hooks file does. `environment` is
`{ name, kind, persistent, protected, resources }` everywhere (D11);
`resources` mirrors the state document's `services` block after ensure.

## Migration from 0.5.0

1. `kraai.config.mjs` → `kraai.yaml` + `environments/preview.yaml` +
   `kraai.hooks.mjs`. Manual; the surface is one object. `kraai init`
   writes the three files from a 0.5.0 config as a courtesy but is not a
   supported converter beyond that.
2. `.kraai/<name>.lock.json` (v1) is read by `apply` and upgraded to
   state v2 on the local backend. `apply --env preview --migrate-state`
   pushes it to the environment's configured backend (`s3-compatible` by
   default). `kraai down <name>` keeps working against v1 files until
   the alias is removed.
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
- AWS, Azure, GCP providers. D16 reserves the key; nothing else lands.
- Container-to-container networking (Cloudflare Containers are HTTP-only
  through a Worker; the containers workstream inherits that).

## Open questions for review

1. ~~`services` as a map vs today's array.~~ **Resolved 2026-09-12: map**,
   keyed by service key.
2. ~~Default backend for persistent envs is `r2`, with a Cloudflare-token
   credential path.~~ **Resolved 2026-09-12:** default backend is
   `s3-compatible` (works against R2, MinIO, AWS S3, B2, ...), always
   authenticated with dedicated storage credentials, never a Cloudflare
   API token.
3. `protected` prompts on `apply`, not only `destroy`, and `destroy` on a
   protected env never takes `--auto-approve`. Confirm.
4. Worker adoption refuses on undeclared live bindings unless
   `--allow-binding-drop`. Alternative: adopt copies undeclared bindings
   into state as `external` automatically. Proposed: refuse; explicit
   beats inferred for prod.
5. `gc` reads `ttl` from the overlay at apply time and stores the deadline
   in state. Alternative: compute at gc time from `updatedAt`. Proposed:
   store the deadline.
6. D18's override rule is warn-and-replace, never a hard error, even when
   a plugin overrides a built-in. Alternative: require an explicit
   `override: true` on the plugin registration, and fail without it.
   Proposed: warn-and-replace; a hard-fail mode is easy to add later and
   a strict default punishes the exact "override a built-in" use case
   this was built for.
7. Middleware (D19) wraps four seams (`resource.ensure/destroy`,
   `state.read/write`) but not hook invocation itself. Should middleware
   also wrap `configure`/`seed`/`open` and the new pre/post hooks?
   Proposed: not yet — hooks are already project-specific; the seams
   that need reusable, cross-project wrapping are the ones every project
   shares regardless of what hooks it writes.
