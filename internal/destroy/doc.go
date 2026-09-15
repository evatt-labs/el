// Package destroy tears down a *plan.Plan against real resources: the
// deleting mirror of internal/apply, and kraai destroy's engine the way
// internal/apply is kraai apply's.
//
// # Plan-then-execute, never re-expand
//
// Exactly like internal/apply: destroy takes an already-computed
// *plan.Plan and, for each plan.Action, looks the real resource.Resource
// back up from the registry by Ref.Key() (Registry.Lookup). Manifest
// expansion stays defined in exactly one place (internal/plan), so
// `kraai plan`'s output stays a faithful description of what both
// mutating verbs — apply and destroy — will touch. Destroy never imports
// internal/manifest and never walks a service's bindings itself.
//
// # Reverse wave order
//
// Apply provisions wave 0 first, then 1, then 2, and so on — plan.Action.Wave,
// computed by internal/plan/graph.go's computeWaves from real dependency
// edges (resource.Registration.DependsOn plus manifest-level depends_on),
// because a Hyperdrive configuration needs its database branch to exist
// first and a Lambda needs its artifact bucket and execution role to exist
// before it deploys. Destroy walks the same waves backwards — the highest
// wave number first, down to 0 — for the same dependency shape in reverse:
// the compute resource that reads a database binding should stop reading
// from it before the binding disappears out from under it. This replaces
// the old fixed three-phase order (database, storage, compute run forward;
// compute, storage, database run backward) with the same reversal applied
// to however many waves this plan's actual dependency graph produced — see
// internal/resource/registry.go's Registration.DependsOn doc comment for
// why the fixed phases were retired. Within a wave, every action runs
// concurrently under one errgroup.Group with SetLimit (D13), the same
// bounded-parallel shape apply and plan both use and for the same reason.
//
// # Skip what does not exist
//
// A plan.ActionCreate means Get found nothing: there is nothing to
// delete. destroy reports that action as OutcomeSkipped and issues no
// Delete call for it at all, rather than calling Delete on a resource
// known not to exist. This matters in the common case this workstream
// exists to unblock — a previous apply that failed halfway, leaving some
// resources created and others not — where skipping the ones that were
// never created saves real API calls instead of merely tolerating them.
//
// Every other Kind — ActionNoChange, ActionReplace, and ActionFailed — is
// attempted: the first two because the resource is known or believed to
// exist, and ActionFailed for the reason the next section explains.
//
// # Failure semantics are deliberately the OPPOSITE of apply's
//
// This is the central design point of this package, and the reason it is
// not simply internal/apply with Create swapped for Delete.
//
// Apply refuses the entire run before it starts if any plan.ActionFailed
// is present (see internal/apply's package doc, "The pre-flight gate"):
// creating something whose current state is unknown is dangerous, because
// apply cannot tell whether a Create would collide with something already
// there. Apply also stops at the first wave with a failure and reports
// every later wave as skipped, because a compute resource that depends on
// a database resource that failed to fully create would be deployed
// against a binding that does not exist.
//
// Neither protection makes sense for teardown, and both would be actively
// harmful if copied over:
//
//   - An ActionFailed here means Get could not read a resource's current
//     state — kraai does not know whether it exists. Refusing to tear
//     down an environment because one resource was momentarily unreadable
//     would strand every other, perfectly deletable resource in it, along
//     with the money it keeps costing. resource.Resource.Delete is
//     idempotent by explicit contract ("deleting something already gone
//     is success" — see resource.go): attempting a delete for a resource
//     that may not exist costs nothing extra and can only make progress,
//     never cause harm. So destroy attempts the delete anyway.
//   - A failure partway through one wave must not stop a later wave
//     from running. Apply's cross-wave gate exists because a
//     half-created database is not safe to deploy code against; there is
//     no destroy-side analogue of that danger. A Lambda that failed to
//     delete does not make deleting the database behind it any less safe
//     — the environment is going away regardless of what order its pieces
//     go in, and every resource destroy manages to remove is one fewer
//     the operator has to find and delete by hand afterward.
//
// Within a wave, one action's failure still never cancels its siblings —
// the same "the errgroup func always returns nil" pattern apply and plan
// both use, for the same reason: a goroutine that returned its action's
// error to the errgroup would abort every other action still in flight in
// the same wave, which is exactly the behavior wave-level (not
// action-level) failure handling exists to avoid, here as much as in
// apply.
//
// Destroy's job, in one sentence, is to make as much progress as possible
// and report precisely what it could not remove, so a human knows exactly
// what to clean up by hand. A future change that makes destroy gate on
// ActionFailed or stop at the first failed wave — "to match apply" —
// would reintroduce the exact problem this workstream exists to solve:
// an apply that fails halfway would then have no way to be torn down.
//
// # Imports are deleted too (D8)
//
// docs/BLUEPRINT.md D8: an imported resource — one the manifest points at
// by id or name rather than one kraai created — is fully owned once
// referenced, with no permanent never-delete flag distinguishing it from
// a native one. This package does not special-case Ref.Import at all: an
// imported resource's plan.Action reaches execute exactly like any other,
// and its Delete is called exactly like any other. `protected` plus
// `--confirm-name` (D14, enforced by internal/cli/destroy.go before a
// *plan.Plan is even computed) is the actual safety net for a sensitive
// imported resource, not an ownership tier this package would need to
// track.
//
// # Scope locking, within one run
//
// Exactly like internal/apply: execute resolves each action's
// Registration.ScopeFor(act.Spec) and runs its Delete call through a
// resource.ScopeLocker shared across this Destroy call, inside the same
// errgroup runWave already bounds by count. A teardown deleting several
// branches in the same Neon project is exactly as capable of tripping
// Neon's one-mutation-per-project serialization as an apply creating them
// — see internal/resource/registry.go's Registration.Scope doc comment.
//
// # No cross-process locking
//
// Same explicit scope note as internal/apply: the lock-and-status
// workstream this package will eventually sit behind is not built.
// Concurrent destroys (or a destroy racing an apply) against the same
// environment, from two different invocations of kraai, are unguarded
// until that lands — kerrors.CodeLockHeld (exit code 3) stays unused by
// this package. This is unrelated to the scope locking above, which
// guards only the goroutines within one Destroy call against each other.
package destroy
