// Package apply executes a *plan.Plan against real resources: the mutating
// half of kraai plan (internal/plan).
//
// # Plan-then-execute, never re-expand
//
// internal/plan deliberately narrows every resource.Resource to a getter
// interface exposing Get alone, so nothing in that package can call
// Create/Update/Delete even by accident (see its package doc). Apply does
// not weaken that: it takes an already-computed *plan.Plan and, for each
// plan.Action, looks the real resource.Resource back up from the registry by
// Ref.Key() (Registry.Lookup). This keeps expansion — walking the manifest,
// deriving Refs and Specs, deciding Create/NoChange/Replace — defined in
// exactly one place, so `kraai plan`'s output stays a faithful description
// of what `kraai apply` will do: apply never recomputes that decision, it
// only carries it out.
//
// # The pre-flight gate
//
// Apply walks the whole plan before touching anything and refuses the run
// if either holds:
//
//   - any plan.ActionFailed is present — a failed Get means kraai does not
//     know whether that resource exists, so it cannot safely be created,
//     replaced, or left alone;
//   - any plan.ActionReplace is present and replacement was not explicitly
//     permitted (the CLI's --replace flag; see WithAllowReplace).
//
// Both refuse the entire run, naming every offending resource, before any
// mutation happens. Partial mutation followed by a refusal — some resources
// created, then the run aborts because a sibling's plan was unreadable — is
// the worst outcome available here: it leaves an environment half-applied
// with no plan the operator can trust to describe the rest. Gating first and
// mutating second means a refusal is always cheap to retry.
//
// # Execution order and failure semantics
//
// Waves run in ascending order via indexByWave, which derives the wave
// count from the plan itself — plan.Action.Wave, computed by
// internal/plan/graph.go's computeWaves from real dependency edges
// (resource.Registration.DependsOn plus manifest-level depends_on), not
// from a fixed set of named stages. This replaces the old three-phase
// model (database, then storage, then compute) that internal/plan and
// internal/resource/registry.go's Registration.DependsOn doc comment
// records the reasoning for reversing. Within a wave, every action runs
// concurrently under a single errgroup.Group with SetLimit, mirroring
// internal/plan/planner.go's getWave: one action's goroutine always returns
// nil to the group regardless of outcome, so one failure never cancels or
// skips its siblings in the same wave.
//
// Across waves, failure is not survived the same way: if any action in a
// wave failed, the next wave does not start, and every action in every
// later wave is reported as skipped rather than attempted. A compute
// resource that depends on a database that failed to create must not be
// deployed against a binding that does not exist.
//
// # Secrets and outputs cross waves scoped to (ServiceKey, Binding)
//
// A Neon branch produces a connection_uri secret; the Cloudflare Hyperdrive
// configuration fronting it — one wave later, since its registration
// depends on the branch's type — consumes it as
// spec.Secrets["connection_uri"]. Apply must not know what Hyperdrive or
// Neon are, so it cannot key that handoff on anything provider-specific.
//
// What it can rely on is that both resources were expanded from the same
// manifest binding by the same planner call (internal/plan/planner.go's
// expandBinding runs once per binding and produces one plannedItem per
// registration Resolve returns for it), so both carry the same
// Item.ServiceKey and Item.Binding even though their Ref.Provider and
// Ref.Type differ. That is the vendor-neutral scope this package uses: a
// secretIndex keyed by (ServiceKey, Binding), populated after every action
// that implements resource.SecretProducer succeeds, and consulted before
// every action in the same or a later wave to populate that action's own
// Spec.Secrets.
//
// resource.Outputs already has a PutSecret/Secret pair, but it keys by Ref —
// correct for a value a resource looks up about itself, wrong for this
// handoff, where the producer and consumer have different Refs entirely by
// design (different provider, different type). Rather than force Ref-scoped
// storage to serve a binding-scoped purpose, apply owns a second, narrower
// index for exactly this handoff and leaves resource.Outputs holding what it
// already holds well: states and identifiers, Put once per successful
// action regardless of kind.
//
// ActionNoChange also records outputs and harvests secrets, not just
// ActionCreate/ActionReplace: a second apply must be able to wire a Lambda's
// env vars from a database branch that already existed and needed no
// change. Skipping that step for NoChange would make apply's secret
// wiring depend on run history — whether a resource happened to be created
// this run or a previous one — which the manifest gives no reason to care
// about.
//
// # A compute resource reads its own service's credentials
//
// Every non-compute item is expanded from exactly one binding, so scoping
// its secrets to that one binding (above) was always correct for it. A
// compute item is different: internal/plan/planner.go's expandCompute sets
// Item.Binding to the service key itself, not to any one of the bindings
// the service declares, because a service's compute resource is not "part
// of" any single database/keyvalue/objects/queues binding — it is the
// deployable unit all of them exist to serve. Scoping secrets to
// (ServiceKey, Binding) alone therefore left a service's own compute
// resource unable to ever read the credentials its own bindings produce: a
// Neon branch registers connection_uri under (api, DB), but the api
// service's Lambda's own item carries Binding "api", and (api, api) is a
// key nothing ever populates.
//
// plan.Item.ReadsBindings closes this gap as plain data the planner
// computes and apply merely consults — apply still does not know what
// "compute" or a manifest capability is, and does not import
// internal/manifest to find out. For each action, execute unions
// secretIndex.forAction across every binding named in
// effectiveReadsBindings(act) rather than looking up a single bindingKey:
//
//   - Secrets from the action's own binding (act.Binding) keep their bare
//     names — this is the path every existing resource type already relies
//     on (a Hyperdrive configuration asking Spec.Secret(ctx,
//     "connection_uri") for its own branch), and it is unchanged by this:
//     an item whose ReadsBindings is exactly its own Binding — every
//     non-compute item, per expandBinding — produces exactly the same map
//     the old, single-bindingKey lookup used to.
//   - Secrets from any other binding in ReadsBindings appear namespaced as
//     "<binding>.<name>" — a Lambda for service "api" that also declares a
//     KeyValue binding "CACHE" sees the branch's credential as
//     "DB.connection_uri" and the cache's as "CACHE.<name>", never both as
//     bare "connection_uri" colliding with each other or with its own
//     binding's secrets (compute has none of its own today, but the rule
//     holds regardless).
//
// This namespacing is collision-free by construction: at most one binding
// — the action's own — may ever claim a bare name, so two different
// bindings producing a same-named secret land at two different keys no
// matter what either name is.
//
// # Known gap: sibling attributes, not just secrets
//
// resource.State.Attributes carries non-secret values a later wave may
// need — a bucket name, a namespace id — and resource.Outputs already
// stores them, keyed by Ref. But resource.Spec has no equivalent to
// Secrets for attributes, so nothing analogous to this file's secret
// handoff exists for them: a compute resource that wants a sibling
// binding's bucket name, not its credential, has no contract-level way to
// receive it. Today that resource has to derive the sibling's name itself
// through internal/naming from the same (environment, service, binding)
// inputs the planner already used to name it — which works because naming
// is deterministic, but is a workaround, not a designed seam. Building a
// namespaced Spec.Attributes alongside Spec.Secrets, populated from
// resource.Outputs the same way secretIndex is populated from
// SecretProducer, is the natural next step whenever a resource actually
// needs it.
//
// # Replace is delete-then-create, never Update
//
// Every registered resource.Resource returns resource.ErrImmutable from
// Update (see internal/resource/immutable.go and every provider adapter
// under internal/provider/*resource). plan.ActionReplace already means "this
// field cannot be reconciled with Update" (internal/plan/diff.go's
// ImmutableDiffer), so apply's replace path is Delete followed by Create —
// genuinely a new resource under the same Ref, not a patch. Apply never
// calls Update at all: there is no registered type it could do anything
// useful with.
//
// # Scope locking, within one run
//
// A wave's concurrent actions can still collide with each other even
// under D13's bounded concurrency, when the provider itself serializes by
// something other than request count — Neon permits only one in-flight
// mutation per project, discovered from a real `kraai apply` 423 (see
// internal/resource/registry.go's Registration.Scope doc comment for the
// failure and the fix). mutate resolves each action's scope via
// Registration.ScopeFor and serializes the mutating call through a
// resource.ScopeLocker shared across this Apply call's whole wave loop,
// inside the same errgroup runWave already bounds by count — the two
// mechanisms compose rather than replace each other: D13 still caps how
// many actions run at once, ScopeLocker additionally prevents two of them
// that share a scope from running their provider calls at the same
// instant.
//
// # Explicitly out of scope
//
// No cross-process locking. The lock-and-status workstream this package
// will eventually sit behind is not built, and building even a stub of it
// here would be scope this task does not own. Concurrent applies against
// the same environment, from two different invocations of kraai, are
// unguarded until that lands — kerrors.CodeLockHeld (exit code 3) stays
// unused by this package. This is unrelated to the scope locking above,
// which guards only the goroutines within one Apply call against each
// other, not one process against another.
package apply
