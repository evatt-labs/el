package deployconfig

import (
	"strings"
	"testing"

	"github.com/evatt-labs/kraai/internal/wrangler"
)

func collectWarnings(warnings *[]string) func(string, ...any) {
	return func(format string, args ...any) {
		_ = args
		*warnings = append(*warnings, format)
	}
}

// TestEveryStatefulBindingIsRefused is the finding this package exists for: a
// security audit found ephemeral previews deployed with live production
// bindings for every stateful resource except Hyperdrive. Each key is checked
// individually so adding one to the list without wiring it up fails here.
func TestEveryStatefulBindingIsRefused(t *testing.T) {
	for _, key := range statefulBindingKeys {
		t.Run(key, func(t *testing.T) {
			base := wrangler.Config{"main": "src/index.ts", key: []any{map[string]any{"binding": "X"}}}

			_, err := Build(base, Options{Name: "env-a-api"})
			if err == nil {
				t.Fatalf("%s was inherited without an override or opt-in — the ephemeral Worker "+
					"would have live access to the production resource", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("the error should name %s: %v", key, err)
			}
		})
	}
}

// TestUnknownKeysAreDroppedNotInherited is what makes the allowlist safe
// against Cloudflare shipping binding types faster than kraai learns them.
func TestUnknownKeysAreDroppedNotInherited(t *testing.T) {
	base := wrangler.Config{
		"main": "src/index.ts",
		// A plausible future binding this package has never heard of.
		"hyperdrive_v2_clusters": []any{map[string]any{"binding": "PROD", "id": "prod-cluster"}},
	}

	deployed, err := Build(base, Options{Name: "env-a-api"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, present := deployed["hyperdrive_v2_clusters"]; present {
		t.Fatal("an unrecognised binding was inherited — a new stateful type would leak production by default")
	}
}

// Safe by default is not the same as silent: a dropped key produces a Worker
// that fails at runtime with an unbound variable and nothing explaining why.
func TestUnknownKeysAreReported(t *testing.T) {
	var warnings []string
	base := wrangler.Config{"main": "src/index.ts", "some_new_binding": []any{}}

	if _, err := Build(base, Options{Name: "n", Warn: collectWarnings(&warnings)}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(warnings) == 0 {
		t.Fatal("an unrecognised key was dropped with no warning")
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "unrecognised") {
		t.Fatalf("warnings = %v", warnings)
	}
}

// TestRoutesAreNeverInherited: reassigning a route during an ephemeral deploy
// can redirect real production traffic to a Worker that is about to be
// deleted — a sharper failure than reading production data.
func TestRoutesAreNeverInherited(t *testing.T) {
	for _, key := range neverInheritedKeys {
		t.Run(key, func(t *testing.T) {
			base := wrangler.Config{"main": "src/index.ts", key: "example.com/*"}

			// Even with the opt-in, which is the point.
			deployed, err := Build(base, Options{Name: "env-a-api", UnsafeInheritBindings: true})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if _, present := deployed[key]; present {
				t.Fatalf("%s survived even with unsafeInheritBindings", key)
			}
		})
	}
}

// TestOverridesCannotReintroduceRoutes hardens the guarantee against its own
// callers. Nothing supplies routes through overrides today, but that is a
// property of the callers rather than of this function, and this is the one
// rule here that protects production traffic rather than production data.
func TestOverridesCannotReintroduceRoutes(t *testing.T) {
	var warnings []string
	deployed, err := Build(wrangler.Config{"main": "src/index.ts"}, Options{
		Name:      "env-a-api",
		Overrides: map[string]any{"routes": []any{"prod.example.com/*"}},
		Warn:      collectWarnings(&warnings),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, present := deployed["routes"]; present {
		t.Fatal("an override reintroduced routes into the deployed config")
	}
	if len(warnings) == 0 {
		t.Fatal("dropping a route supplied by an override was silent")
	}
}

// A provisioned substitute needs no opt-in: there is nothing
// production-pointing left to inherit once it is replaced.
func TestAnOverrideRemovesTheNeedToOptIn(t *testing.T) {
	base := wrangler.Config{
		"main":         "src/index.ts",
		"d1_databases": []any{map[string]any{"binding": "DB", "database_id": "PROD-ID"}},
	}
	fresh := D1Databases([]Binding{{Binding: "DB"}}, map[string]D1Resource{
		"DB": {Name: "env-a-api-db", ID: "fresh-id"},
	})

	deployed, err := Build(base, Options{Name: "env-a-api", Overrides: map[string]any{"d1_databases": fresh}})
	if err != nil {
		t.Fatalf("a provisioned substitute should not need an opt-in: %v", err)
	}

	entries, _ := deployed["d1_databases"].([]any)
	entry, _ := entries[0].(map[string]any)
	if entry["database_id"] != "fresh-id" {
		t.Fatalf("the deployed config points at %v, not the freshly provisioned database", entry["database_id"])
	}
}

func TestOptInInheritsProductionBindings(t *testing.T) {
	base := wrangler.Config{
		"main":          "src/index.ts",
		"kv_namespaces": []any{map[string]any{"binding": "CACHE", "id": "prod-ns"}},
	}

	deployed, err := Build(base, Options{Name: "env-a-api", UnsafeInheritBindings: true})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, present := deployed["kv_namespaces"]; !present {
		t.Fatal("the deliberate opt-in did not inherit the binding")
	}
}

func TestStructuralKeysCarryForward(t *testing.T) {
	base := wrangler.Config{
		"$schema":             "node_modules/wrangler/config-schema.json",
		"main":                "src/index.ts",
		"compatibility_date":  "2026-01-01",
		"compatibility_flags": []any{"nodejs_compat"},
		"observability":       map[string]any{"enabled": true},
		"durable_objects":     map[string]any{"bindings": []any{}},
		"migrations":          []any{map[string]any{"tag": "v1"}},
		"name":                "the-committed-name",
	}

	deployed, err := Build(base, Options{Name: "env-a-api"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, key := range []string{"$schema", "main", "compatibility_date", "compatibility_flags", "observability", "durable_objects", "migrations"} {
		if _, present := deployed[key]; !present {
			t.Errorf("structural key %s was dropped", key)
		}
	}
	// The generated name must win over the committed one, or the ephemeral
	// deploy overwrites the production Worker.
	if deployed["name"] != "env-a-api" {
		t.Fatalf("name = %v, want the generated environment name", deployed["name"])
	}
}

// Durable Objects live inside the deployed script, so a fresh Worker name
// gets fresh storage — there is nothing shared with production to leak, which
// is why they are structural rather than stateful.
func TestDurableObjectsAreNotTreatedAsStateful(t *testing.T) {
	base := wrangler.Config{
		"main":            "src/index.ts",
		"durable_objects": map[string]any{"bindings": []any{map[string]any{"name": "COUNTER", "class_name": "Counter"}}},
	}
	if _, err := Build(base, Options{Name: "env-a-api"}); err != nil {
		t.Fatalf("durable_objects should not require an opt-in: %v", err)
	}
}

func TestVarsAlwaysPresent(t *testing.T) {
	deployed, err := Build(wrangler.Config{"main": "x"}, Options{Name: "n"})
	if err != nil {
		t.Fatal(err)
	}
	vars, ok := deployed["vars"].(map[string]any)
	if !ok {
		t.Fatalf("vars = %v, want an object even when empty", deployed["vars"])
	}
	if len(vars) != 0 {
		t.Fatalf("vars = %v", vars)
	}
}

func TestHyperdriveOnlyWhenProvided(t *testing.T) {
	without, err := Build(wrangler.Config{"main": "x"}, Options{Name: "n"})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := without["hyperdrive"]; present {
		t.Fatal("hyperdrive appeared with none provided")
	}

	with, err := Build(wrangler.Config{"main": "x"}, Options{
		Name:       "n",
		Hyperdrive: []any{map[string]any{"binding": "HYPERDRIVE", "id": "hd-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := with["hyperdrive"]; !present {
		t.Fatal("hyperdrive was not deployed")
	}
}

func TestQueuesOverride(t *testing.T) {
	base := wrangler.Config{"queues": map[string]any{
		"consumers": []any{map[string]any{"queue": "prod-q", "max_batch_size": float64(10), "max_retries": float64(3)}},
	}}
	bindings := []Binding{{Binding: "JOBS", Consumer: true}, {Binding: "EVENTS"}}
	names := map[string]string{"JOBS": "env-a-jobs", "EVENTS": "env-a-events"}

	got := Queues(base, bindings, names)

	producers, _ := got["producers"].([]any)
	if len(producers) != 2 {
		t.Fatalf("producers = %v, want one per binding", producers)
	}
	consumers, _ := got["consumers"].([]any)
	if len(consumers) != 1 {
		t.Fatalf("consumers = %v, want only the binding the service consumes", consumers)
	}
	consumer, _ := consumers[0].(map[string]any)
	if consumer["queue"] != "env-a-jobs" {
		t.Fatalf("consumer points at %v, not the ephemeral queue", consumer["queue"])
	}
	if consumer["max_retries"] != float64(3) {
		t.Fatalf("the single base consumer's settings were not copied: %v", consumer)
	}
}

// With more than one base consumer, which belongs to which binding is
// genuinely ambiguous — the entries carry no binding key — so settings must
// not be guessed and silently applied to the wrong queue.
func TestQueuesDoesNotGuessBetweenMultipleConsumers(t *testing.T) {
	base := wrangler.Config{"queues": map[string]any{
		"consumers": []any{
			map[string]any{"queue": "a", "max_retries": float64(1)},
			map[string]any{"queue": "b", "max_retries": float64(99)},
		},
	}}

	got := Queues(base, []Binding{{Binding: "JOBS", Consumer: true}}, map[string]string{"JOBS": "env-a-jobs"})

	consumers, _ := got["consumers"].([]any)
	consumer, _ := consumers[0].(map[string]any)
	if _, present := consumer["max_retries"]; present {
		t.Fatalf("settings were guessed from one of two ambiguous consumers: %v", consumer)
	}
	if consumer["queue"] != "env-a-jobs" {
		t.Fatalf("consumer = %v", consumer)
	}
}

// The override builders must preserve declaration order, since bindings are
// positional in the deployed arrays.
func TestOverrideBuildersPreserveOrder(t *testing.T) {
	bindings := []Binding{{Binding: "A"}, {Binding: "B"}, {Binding: "C"}}
	ids := map[string]string{"A": "1", "B": "2", "C": "3"}

	out := KVNamespaces(bindings, ids)
	for i, want := range []string{"A", "B", "C"} {
		entry, _ := out[i].(map[string]any)
		if entry["binding"] != want {
			t.Fatalf("position %d = %v, want %s", i, entry["binding"], want)
		}
	}
}

// TestHyperdriveIsDroppedLoudlyWhenNotReplaced: the committed binding points
// at production, so dropping it is right — but doing it silently leaves a
// Worker that fails on an unbound binding with nothing explaining why.
func TestHyperdriveIsDroppedLoudlyWhenNotReplaced(t *testing.T) {
	var warnings []string
	base := wrangler.Config{
		"main":       "src/index.ts",
		"hyperdrive": []any{map[string]any{"binding": "HYPERDRIVE", "id": "prod-hyperdrive"}},
	}

	deployed, err := Build(base, Options{Name: "env-a-api", Warn: collectWarnings(&warnings)})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, present := deployed["hyperdrive"]; present {
		t.Fatal("the production Hyperdrive binding was deployed to an ephemeral Worker")
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "hyperdrive") {
		t.Fatalf("dropping it was silent: %v", warnings)
	}
}

// TestNewerBindingTypesAreRefused pins the keys added against Cloudflare's
// current reference rather than inherited from the JavaScript's stale list.
func TestNewerBindingTypesAreRefused(t *testing.T) {
	for _, key := range []string{"ai_search", "ai_search_namespaces", "tail_consumers", "containers"} {
		t.Run(key, func(t *testing.T) {
			base := wrangler.Config{"main": "src/index.ts", key: []any{map[string]any{"binding": "X"}}}
			if _, err := Build(base, Options{Name: "n"}); err == nil {
				t.Fatalf("%s was not treated as a stateful binding", key)
			}
		})
	}
}
