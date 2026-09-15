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
// Phases run in sequence via resource.Phases() (database, then storage, then
// compute), matching internal/plan and internal/resource/registry.go's
// rationale for phases over a dependency graph. Within a phase, every action
// runs concurrently under a single errgroup.Group with SetLimit, mirroring
// internal/plan/planner.go's getPhase: one action's goroutine always returns
// nil to the group regardless of outcome, so one failure never cancels or
// skips its siblings in the same phase.
//
// Across phases, failure is not survived the same way: if any action in a
// phase failed, the next phase does not start, and every action in every
// later phase is reported as skipped rather than attempted. A compute
// resource that binds to a database that failed to create must not be
// deployed against a binding that does not exist.
//
// # Secrets and outputs cross phases scoped to (ServiceKey, Binding)
//
// A Neon branch (PhaseDatabase) produces a connection_uri secret; the
// Cloudflare Hyperdrive configuration fronting it (PhaseStorage) consumes it
// as spec.Secrets["connection_uri"]. Apply must not know what Hyperdrive or
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
// every action in the same or a later phase to populate that action's own
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
// # Explicitly out of scope
//
// No locking. The lock-and-status workstream this package will eventually
// sit behind is not built, and building even a stub of it here would be
// scope this task does not own. Concurrent applies against the same
// environment are unguarded until that lands — kerrors.CodeLockHeld (exit
// code 3) stays unused by this package.
package apply
