package plan

import (
	"errors"
	"strings"
	"testing"

	"github.com/evatt-labs/kraai/internal/resource"
)

func TestRender_EmptyPlan(t *testing.T) {
	for _, p := range []*Plan{nil, {}} {
		got := Render(p)
		if got != "no resources declared\n" {
			t.Errorf("Render(%+v) = %q, want the empty-plan message", p, got)
		}
	}
}

func TestRender_EveryActionKind(t *testing.T) {
	p := &Plan{Actions: []Action{
		{
			Item: Item{ServiceKey: "api", Binding: "DB", Provider: "neon", Type: "branch", Phase: resource.PhaseDatabase},
			Ref:  resource.Ref{Name: "env-api-db"}, Kind: ActionCreate,
		},
		{
			Item: Item{ServiceKey: "api", Binding: "CACHE", Provider: "cloudflare", Type: "kv_namespace", Phase: resource.PhaseStorage},
			Ref:  resource.Ref{Name: "env-api-cache"}, Kind: ActionNoChange,
		},
		{
			Item: Item{ServiceKey: "api", Binding: "UPLOADS", Provider: "cloudflare", Type: "r2_bucket", Phase: resource.PhaseStorage},
			Ref:  resource.Ref{Name: "env-api-uploads"}, Kind: ActionReplace,
		},
		{
			Item: Item{ServiceKey: "api", Binding: "JOBS", Provider: "cloudflare", Type: "queue", Phase: resource.PhaseStorage},
			Ref:  resource.Ref{Name: "env-api-jobs"}, Kind: ActionFailed, Err: errors.New("timeout"),
		},
	}}

	got := Render(p)

	for _, want := range []string{
		"database:", "storage:",
		"+ create \"env-api-db\"",
		"= \"env-api-cache\" unchanged",
		"~ replace \"env-api-uploads\" (immutable field differs)",
		"! could not read \"env-api-jobs\": timeout",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render output missing %q; got:\n%s", want, got)
		}
	}

	// One phase header per phase, not per action.
	if strings.Count(got, "storage:") != 1 {
		t.Errorf("Render printed the storage header more than once:\n%s", got)
	}
}

func TestSymbol_UnknownKind(t *testing.T) {
	if got := symbol(ActionKind(99)); got != "?" {
		t.Errorf("symbol(unknown) = %q, want \"?\"", got)
	}
}

func TestDescribe_UnknownKind(t *testing.T) {
	a := Action{Ref: resource.Ref{Name: "x"}, Kind: ActionKind(99)}
	got := describe(a)
	if !strings.Contains(got, "unknown action") {
		t.Errorf("describe(unknown) = %q, want it to mention an unknown action", got)
	}
}
