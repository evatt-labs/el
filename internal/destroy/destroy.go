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
	byWave := indexByWave(p.Actions)
	// One locker per run, mirroring internal/apply.Apply — see
	// resource.ScopeLocker's doc comment on why it is not a shared
	// singleton, and internal/resource/registry.go's Registration.Scope
	// doc comment for why teardown needs this too: two concurrent
	// deletes against the same Neon project can 423 each other exactly
	// like two concurrent creates can.
	locker := resource.NewScopeLocker()

	// Teardown runs waves in reverse (see plan.Item.Wave's own doc
	// comment, and this package's doc). Every wave runs regardless of
	// whether an earlier one had failures — the central asymmetry with
	// apply's Apply, which stops at the first failed wave. See the
	// package doc's "Failure semantics are deliberately the OPPOSITE of
	// apply's" section.
	for wave := len(byWave) - 1; wave >= 0; wave-- {
		idxs := byWave[wave]
		if len(idxs) == 0 {
			continue
		}
		d.runWave(ctx, p.Actions, idxs, results, locker)
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

// indexByWave groups action indices by wave, preserving each action's
// original position in actions/results so per-action output stays aligned
// regardless of how the input plan happened to be ordered. Indexed
// directly by wave number (0..max), mirroring internal/apply/apply.go's
// function of the same name and purpose.
func indexByWave(actions []plan.Action) [][]int {
	maxWave := 0
	for _, a := range actions {
		if a.Wave > maxWave {
			maxWave = a.Wave
		}
	}
	out := make([][]int, maxWave+1)
	for i, a := range actions {
		out[a.Wave] = append(out[a.Wave], i)
	}
	return out
}

// runWave executes every action at idxs concurrently, bounded by
// d.concurrency.
//
// Unlike internal/apply/apply.go's runWave, this reports nothing back to
// its caller about whether any action failed: Destroy never gates a later
// wave on an earlier one's outcome, so there is nothing for a return
// value to communicate. Mirrors apply's central property all the same —
// the errgroup.Group's function always returns nil regardless of the
// action's outcome, so one action's failure is recorded in results and
// never propagated through the group, which would otherwise cancel every
// sibling action still in flight in the same wave.
func (d *Destroyer) runWave(
	ctx context.Context, actions []plan.Action, idxs []int, results []ActionResult,
	locker *resource.ScopeLocker,
) {
	g := &errgroup.Group{}
	g.SetLimit(d.concurrency)

	for _, i := range idxs {
		i := i
		g.Go(func() error {
			results[i] = d.execute(ctx, actions[i], locker)
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
func (d *Destroyer) execute(ctx context.Context, act plan.Action, locker *resource.ScopeLocker) ActionResult {
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

	// act.Spec is still the plan's derived desired state even in
	// teardown (plan.Action carries the same fields for both verbs — see
	// the package doc), so Registration.Scope resolves exactly the way
	// it does in internal/apply, from the same Spec a Create for this
	// same Ref.Key() would have used.
	scope := reg.ScopeFor(act.Spec)

	// act.Kind == plan.ActionFailed reaches here too, deliberately: Get
	// could not read this resource's current state, so kraai does not know
	// whether it exists, but resource.Resource.Delete is idempotent by
	// contract (deleting something already gone is success), so attempting
	// it anyway is safe and can only make progress. See the package doc's
	// "Failure semantics are deliberately the OPPOSITE of apply's" section
	// — this is the one line of code that section exists to explain.
	err := locker.Do(scope, func() error { return reg.Resource.Delete(ctx, act.Ref) })
	if err != nil {
		result.Outcome = OutcomeFailed
		result.Err = kerrors.Wrap(err, kerrors.CodeUnexpected,
			"delete %s/%s %q", act.Provider, act.Type, act.Ref.Name)
		return result
	}

	result.Outcome = OutcomeDeleted
	return result
}
