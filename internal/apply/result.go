package apply

import (
	"fmt"

	"github.com/evatt-labs/kraai/internal/plan"
	"github.com/evatt-labs/kraai/internal/resource"
)

// Outcome is what actually happened to one resource during an Apply run —
// the mutating analogue of plan.ActionKind. Kept as a distinct type rather
// than reusing plan.ActionKind: a plan proposes create/no-change/replace/
// (Get) failed, but an apply run additionally needs to say a mutation
// itself failed (as opposed to the read that preceded it) and that an
// action never ran at all because an earlier phase failed.
type Outcome int

const (
	// OutcomeCreated means Create succeeded.
	OutcomeCreated Outcome = iota
	// OutcomeUnchanged means the action was plan.ActionNoChange: no
	// provider call was made, but its state and secrets were still
	// recorded (see the package doc).
	OutcomeUnchanged
	// OutcomeReplaced means Delete then Create both succeeded.
	OutcomeReplaced
	// OutcomeFailed means a mutating call failed, or the action itself
	// could not be resolved to a registered resource. See Err.
	OutcomeFailed
	// OutcomeSkipped means this action was never attempted, because an
	// earlier phase had a failure and apply refuses to start a phase that
	// depends on one that did not fully succeed.
	OutcomeSkipped
)

// String implements fmt.Stringer for readable output and error messages.
func (o Outcome) String() string {
	switch o {
	case OutcomeCreated:
		return "created"
	case OutcomeUnchanged:
		return "unchanged"
	case OutcomeReplaced:
		return "replaced"
	case OutcomeFailed:
		return "failed"
	case OutcomeSkipped:
		return "skipped"
	default:
		return fmt.Sprintf("Outcome(%d)", int(o))
	}
}

// ActionResult is one plan.Action's outcome after Apply has run.
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

// Result is the ordered outcome of applying every action in a plan.Plan,
// one ActionResult per plan.Action, in the same order.
type Result struct {
	Results []ActionResult
}

// HasFailures reports whether any action failed. It does not consider
// OutcomeSkipped a failure in its own right — a skipped action recorded no
// error of its own, it simply never ran because an earlier phase did fail,
// and that earlier failure is what HasFailures already reports.
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
