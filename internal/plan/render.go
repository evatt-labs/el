package plan

import (
	"fmt"
	"strings"
)

// Render turns a Plan into plain, human-readable text.
//
// Deliberately not a method on Plan and not called anywhere else in this
// package: computing a plan and presenting it are different jobs with
// different audiences: apply needs the structured Actions, a human at a
// terminal needs a formatted summary, and a future command layer may want
// JSON or a TUI instead of either. Keeping this a free function operating
// on the finished Plan means none of those callers has to recompute
// anything, and adding a second renderer never touches Planner or Plan.
func Render(p *Plan) string {
	if p == nil || len(p.Actions) == 0 {
		return "no resources declared\n"
	}

	var b strings.Builder
	phase := p.Actions[0].Phase
	fmt.Fprintf(&b, "%s:\n", phase)
	for _, a := range p.Actions {
		if a.Phase != phase {
			phase = a.Phase
			fmt.Fprintf(&b, "\n%s:\n", phase)
		}
		fmt.Fprintf(&b, "  %s %s (%s/%s, %s.%s)\n", symbol(a.Kind), describe(a), a.Provider, a.Type, a.ServiceKey, a.Binding)
	}
	return b.String()
}

// symbol is the one-character marker Render prefixes each line with.
func symbol(k ActionKind) string {
	switch k {
	case ActionCreate:
		return "+"
	case ActionReplace:
		return "~"
	case ActionFailed:
		return "!"
	case ActionNoChange:
		return "="
	default:
		return "?"
	}
}

// describe is the human-readable outcome Render prints for one Action.
func describe(a Action) string {
	switch a.Kind {
	case ActionCreate:
		return fmt.Sprintf("create %q", a.Ref.Name)
	case ActionNoChange:
		return fmt.Sprintf("%q unchanged", a.Ref.Name)
	case ActionReplace:
		return fmt.Sprintf("replace %q (immutable field differs)", a.Ref.Name)
	case ActionFailed:
		return fmt.Sprintf("could not read %q: %v", a.Ref.Name, a.Err)
	default:
		return fmt.Sprintf("%q: unknown action %s", a.Ref.Name, a.Kind)
	}
}
