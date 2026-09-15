package destroy

import (
	"context"

	"golang.org/x/sync/errgroup"

	"github.com/evatt-labs/kraai/internal/kerrors"
	"github.com/evatt-labs/kraai/internal/plan"
	"github.com/evatt-labs/kraai/internal/resource"
)

// defaultConcurrency bounds Delete calls within one phase when the caller
// does not set one explicitly. Mirrors internal/apply's own
// defaultConcurrency and internal/plan/planner.go's before it, including
// their rationale (D13): goroutines are cheap, provider rate limits are
// not — this time against Delete instead of Create/Get.
const defaultConcurrency = 10

// Destroyer executes teardown plans against a fixed registry.
type Destroyer struct {
	registry    *resource.Registry
	concurrency int
}

// Option configures a Destroyer.
type Option func(*Destroyer)

// WithConcurrency sets the maximum number of Delete calls in flight at
// once within a single phase. Non-positive values are ignored, leaving
// the default in place — the same contract as apply.WithConcurrency and
// plan.WithConcurrency, for the same reason.
func WithConcurrency(n int) Option {
	return func(d *Destroyer) {
		if n > 0 {
			d.concurrency = n
		}
	}
}

// New builds a Destroyer against reg.
func New(reg *resource.Registry, opts ...Option) *Destroyer {
	d := &Destroyer{registry: reg, concurrency: defaultConcurrency}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Destroy tears down every action in p: for each one it resolves the real
// resource.Resource from the registry by Ref.Key() and calls Delete,
// unless the plan already reported the resource as never having existed
// (plan.ActionCreate), in which case nothing is called at all. See the
// package doc for the full outcome mapping and, most importantly, for why
// this has no pre-flight gate and does not stop at the first failed
// phase — both deliberate, and both the opposite of internal/apply's own
// behavior.
//
// A nil plan is a no-op: there is nothing to gate and nothing to tear
// down, so this returns an empty, non-nil Result rather than treating "the
// plan computed to nothing" as an error case the caller has to
// special-case — the same contract apply.Apply gives a nil plan.
func (d *Destroyer) Destroy(ctx context.Context, p *plan.Plan) (*Result, error) {
	if p == nil {
		return &Result{}, nil
	}

	results := make([]ActionResult, len(p.Actions))
	byPhase := indexByPhase(p.Actions)

	// Teardown runs phases in reverse (see resource.Phase's own doc
	// comment, and this package's doc). Every phase runs regardless of
	// whether an earlier one had failures — the central asymmetry with
	// apply's Apply, which stops at the first failed phase. See the
	// package doc's "Failure semantics are deliberately the OPPOSITE of
	// apply's" section.
	for _, phase := range reversedPhases() {
		idxs := byPhase[phase]
		if len(idxs) == 0 {
			continue
		}
		d.runPhase(ctx, p.Actions, idxs, results)
	}

	// A cancelled run is not a completed destroy, for the same reason
	// Apply and Planner.Plan both refuse to hand back a partial result on
	// cancellation (see their own comments): every entry Destroy already
	// wrote either reflects a real deletion, skip, or failure that
	// occurred, so nothing here is fabricated — but reporting success (a
	// nil error) for a run the caller asked to stop would be wrong
	// regardless of how accurate the partial Result is. Whatever was
	// already deleted stays deleted; it is simply not summarized as a
	// normal result for this invocation.
	if err := ctx.Err(); err != nil {
		return nil, kerrors.Wrap(err, kerrors.CodeUnexpected, "destroying was cancelled")
	}
	return &Result{Results: results}, nil
}

// reversedPhases returns resource.Phases() in reverse: PhaseCompute,
// PhaseStorage, PhaseDatabase. A small local helper rather than a change
// to resource.Phases() itself, since that function's forward order is
// correct and used elsewhere (internal/plan, internal/apply) — reversing
// is this package's own concern.
func reversedPhases() []resource.Phase {
	forward := resource.Phases()
	out := make([]resource.Phase, len(forward))
	for i, p := range forward {
		out[len(forward)-1-i] = p
	}
	return out
}

// indexByPhase groups action indices by phase, preserving each action's
// original position in actions/results so per-action output stays aligned
// regardless of how the input plan happened to be ordered. Mirrors
// internal/apply/apply.go's function of the same name and purpose.
func indexByPhase(actions []plan.Action) map[resource.Phase][]int {
	out := make(map[resource.Phase][]int, len(resource.Phases()))
	for i, a := range actions {
		out[a.Phase] = append(out[a.Phase], i)
	}
	return out
}

// runPhase executes every action at idxs concurrently, bounded by
// d.concurrency.
//
// Unlike internal/apply/apply.go's runPhase, this reports nothing back to
// its caller about whether any action failed: Destroy never gates a later
// phase on an earlier one's outcome, so there is nothing for a return
// value to communicate. Mirrors apply's central property all the same —
// the errgroup.Group's function always returns nil regardless of the
// action's outcome, so one action's failure is recorded in results and
// never propagated through the group, which would otherwise cancel every
// sibling action still in flight in the same phase.
func (d *Destroyer) runPhase(ctx context.Context, actions []plan.Action, idxs []int, results []ActionResult) {
	g := &errgroup.Group{}
	g.SetLimit(d.concurrency)

	for _, i := range idxs {
		i := i
		g.Go(func() error {
			results[i] = d.execute(ctx, actions[i])
			return nil
		})
	}
	_ = g.Wait()
}

// execute runs one action to completion and reports its outcome. It never
// returns an error itself — every failure is captured in the returned
// ActionResult, so a caller running many of these concurrently (runPhase)
// never has to decide what an error from this call would even mean for
// its siblings.
func (d *Destroyer) execute(ctx context.Context, act plan.Action) ActionResult {
	result := ActionResult{Item: act.Item, Ref: act.Ref}

	// plan.ActionCreate means Get found nothing: there is nothing to
	// delete. Skip it rather than issuing a Delete call — see the package
	// doc's "Skip what does not exist" section. Every other Kind
	// (ActionNoChange, ActionReplace, ActionFailed) is attempted below.
	if act.Kind == plan.ActionCreate {
		result.Outcome = OutcomeSkipped
		return result
	}

	reg, ok := d.registry.Lookup(act.Ref.Key())
	if !ok {
		result.Outcome = OutcomeFailed
		result.Err = kerrors.Validation(
			"no registered resource type %q for %s.%s — the plan and the registry have "+
				"drifted apart", act.Ref.Key(), act.ServiceKey, act.Binding)
		return result
	}

	// act.Kind == plan.ActionFailed reaches here too, deliberately: Get
	// could not read this resource's current state, so kraai does not know
	// whether it exists, but resource.Resource.Delete is idempotent by
	// contract (deleting something already gone is success), so attempting
	// it anyway is safe and can only make progress. See the package doc's
	// "Failure semantics are deliberately the OPPOSITE of apply's" section
	// — this is the one line of code that section exists to explain.
	if err := reg.Resource.Delete(ctx, act.Ref); err != nil {
		result.Outcome = OutcomeFailed
		result.Err = kerrors.Wrap(err, kerrors.CodeUnexpected,
			"delete %s/%s %q", act.Provider, act.Type, act.Ref.Name)
		return result
	}

	result.Outcome = OutcomeDeleted
	return result
}
