package destroy

import (
	"fmt"

	"github.com/evatt-labs/kraai/internal/plan"
	"github.com/evatt-labs/kraai/internal/resource"
)

// Outcome is what actually happened to one resource during a Destroy run —
// the deleting analogue of apply.Outcome. A narrower set than apply's:
// destroy has no replace/no-change distinction (every attempted action is
// simply "deleted" or "failed to delete"), and no cross-phase
// OutcomeSkipped, since a phase failure never stops a later phase here
// (see the package doc). The one OutcomeSkipped this package does report
// means something apply's never does: the resource never existed, so
// nothing was attempted for it.
type Outcome int

const (
	// OutcomeDeleted means Delete succeeded (including deleting a resource
	// already gone, which resource.Resource.Delete's contract treats as
	// success).
	OutcomeDeleted Outcome = iota
	// OutcomeSkipped means this action was plan.ActionCreate — Get found
	// nothing, so there was nothing to delete — and no Delete call was
	// issued.
	OutcomeSkipped
	// OutcomeFailed means Delete itself failed, or the action could not be
	// resolved to a registered resource type. See Err.
	OutcomeFailed
)

// String implements fmt.Stringer for readable output and error messages.
func (o Outcome) String() string {
	switch o {
	case OutcomeDeleted:
		return "deleted"
	case OutcomeSkipped:
		return "skipped"
	case OutcomeFailed:
		return "failed"
	default:
		return fmt.Sprintf("Outcome(%d)", int(o))
	}
}

// ActionResult is one plan.Action's outcome after Destroy has run.
type ActionResult struct {
	plan.Item
	// Ref is the resource identity this outcome is about, carried straight
	// from the plan.Action it came from.
	Ref resource.Ref
	// Outcome is what happened.
	Outcome Outcome
	// Err is set if and only if Outcome is OutcomeFailed.
	Err error
}

// Result is the ordered outcome of destroying every action in a
// plan.Plan, one ActionResult per plan.Action, in the same order the plan
// carried them in (not the reverse execution order destroy actually ran
// them in — see the package doc's "Reverse phase order" section).
type Result struct {
	Results []ActionResult
}

// HasFailures reports whether any action failed to delete. It does not
// consider OutcomeSkipped a failure: a skipped action was never attempted
// because the resource never existed, which is success, not a gap.
func (r *Result) HasFailures() bool {
	if r == nil {
		return false
	}
	for _, res := range r.Results {
		if res.Outcome == OutcomeFailed {
			return true
		}
	}
	return false
}
