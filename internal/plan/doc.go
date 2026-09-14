// Package plan walks a resolved manifest and reports what would happen to
// each declared resource without changing anything.
//
// # Read-only by construction, not by convention
//
// A plan may only call Resource.Get (internal/resource). Nothing in this
// package ever holds a value statically typed as resource.Resource: the
// walk stores each registration's resource behind a locally defined getter
// interface exposing Get alone (see planner.go), so Create/Update/Delete
// are not merely unused — they are not in scope at any call site in this
// package. A future edit that tried to call one would fail to compile
// unless it first type-asserted back to resource.Resource, which is an
// obvious, deliberate, greppable act rather than an accidental one.
//
// # Shape
//
// Plan is an ordered list of Action, one per resource type a manifest
// binding expands to (D30), ordered by resource.Phase (D31) and stable
// within a phase. Each Action names what it is about (service, binding,
// capability, provider, type), the state Get found (nil if absent), and
// what a subsequent apply would need to do about it — Create, NoChange, or
// Replace when an existing resource's spec disagrees with it on a field
// that Update cannot reconcile (see diff.go). Rendering a Plan for a human
// is a separate, optional step (render.go): the Plan itself carries only
// structured data, so a caller can format it as plain text, JSON, or
// anything else without recomputing the walk.
//
// # Partial failure
//
// A resource whose Get fails is reported, not dropped and not treated as
// fatal to the whole run: it becomes an Action with ActionFailed and the
// error that occurred, sitting in its correct phase position alongside
// every resource that could be read. Planner.Plan itself only returns a
// non-nil error for a failure that makes the walk itself impossible — an
// unconfigured capability, an unresolvable vendor — never for a live I/O
// failure reading one resource among many. Silently omitting a resource
// kraai could not read would be worse than reporting it wrong, and
// aborting the whole plan over one unreachable API would hide every other
// answer the run did manage to get. Call Plan.HasFailures to check for
// this before treating a Plan as complete.
//
// # Concurrency
//
// Get calls within a phase run concurrently, bounded by a single
// golang.org/x/sync/errgroup limit (D13) rather than one goroutine per
// resource; phases themselves run in sequence, matching the order apply
// will need for real (D31). The limit defaults to a modest value and is
// configurable via WithConcurrency.
//
// # Capability expansion across providers (D30)
//
// A manifest entry's capability may expand to resource types registered
// under more than one literal provider string — a Postgres binding on Neon
// becomes a Neon branch and the Cloudflare Hyperdrive configuration
// fronting it. registry.Resolve is scoped to one (capability, provider)
// pair, so registrationsFor in planner.go supplements it with whatever
// else shares the capability. See that function's comment for the specific
// tension this works around and its limits.
package plan
