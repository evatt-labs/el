package lockfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPath(t *testing.T) {
	got, err := Path("/tmp/project", "swift-blue-otter")
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	want := filepath.Join("/tmp/project", ".kraai", "swift-blue-otter.lock.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEmptyHasInitialShape(t *testing.T) {
	lock := Empty("env-a", "1.2.3", "acct", "sub")
	if lock.LockfileVersion != Version {
		t.Fatalf("lockfileVersion = %d, want %d", lock.LockfileVersion, Version)
	}
	if lock.Database != nil {
		t.Fatal("database should start nil")
	}
	if lock.Services == nil || len(lock.Services) != 0 {
		t.Fatalf("services = %v, want an empty non-nil map", lock.Services)
	}
	if lock.CreatedAt == "" {
		t.Fatal("createdAt should be stamped")
	}
}

// TestEmptySerializesEmptyServicesAsObject pins the one detail a Go port gets
// wrong by default: a nil map marshals to null, and a lockfile whose services
// key is null is not the shape the JavaScript CLI produces.
func TestEmptySerializesEmptyServicesAsObject(t *testing.T) {
	encoded, err := json.Marshal(Empty("env-a", "1.2.3", "acct", "sub"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"services":{}`) {
		t.Fatalf("services did not serialize as {}: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"database":null`) {
		t.Fatalf("database did not serialize as null: %s", encoded)
	}
}

// TestWriteMatchesJavaScriptFormat pins two-space indentation, the trailing
// newline, and key order against the exact bytes the JavaScript CLI writes.
// These environments are torn down by whichever CLI the user happens to run,
// so the file has to be the same file either way.
func TestWriteMatchesJavaScriptFormat(t *testing.T) {
	dir := t.TempDir()
	lock := Empty("env-a", "1.2.3", "acct-1", "sub-1")
	lock.CreatedAt = "2026-09-14T00:00:00.000Z"
	if err := Write(dir, lock); err != nil {
		t.Fatalf("Write: %v", err)
	}

	raw, err := os.ReadFile(mustPath(t, dir, "env-a"))
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	want := `{
  "lockfileVersion": 1,
  "name": "env-a",
  "createdAt": "2026-09-14T00:00:00.000Z",
  "elVersion": "1.2.3",
  "accountId": "acct-1",
  "subdomain": "sub-1",
  "database": null,
  "services": {}
}
`
	if string(raw) != want {
		t.Fatalf("serialized form drifted from the JavaScript CLI's.\ngot:\n%s\nwant:\n%s", raw, want)
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	lock := Empty("env-a", "1.2.3", "acct", "sub")
	lock.Services["api"] = Service{
		Dir:                "services/api",
		WorkerName:         "env-a-api",
		WranglerVersion:    "4.0.0",
		CompatibilityDate:  "2026-01-01",
		CompatibilityFlags: []string{"nodejs_compat"},
		Resources: Resources{
			D1: []Resource{{Binding: "DB", Name: "env-a-api-db"}},
		},
	}
	if err := Write(dir, lock); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := Read(dir, "env-a")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got == nil {
		t.Fatal("Read returned no lock for one just written")
	}
	svc, ok := got.Services["api"]
	if !ok {
		t.Fatal("api service missing after round trip")
	}
	if svc.WorkerName != "env-a-api" || len(svc.Resources.D1) != 1 || svc.Resources.D1[0].Name != "env-a-api-db" {
		t.Fatalf("service did not survive the round trip: %+v", svc)
	}
}

func TestWriteCreatesDirAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	if _, err := os.Stat(filepath.Join(dir, ".kraai")); !os.IsNotExist(err) {
		t.Fatal("test precondition: .kraai should not exist yet")
	}

	lock := Empty("env-a", "1.0.0", "acct", "sub")
	if err := Write(dir, lock); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	lock.Subdomain = "second"
	if err := Write(dir, lock); err != nil {
		t.Fatalf("second Write: %v", err)
	}

	// Overwrite, never append: a second write must leave one parseable
	// document, not two concatenated ones.
	got, err := Read(dir, "env-a")
	if err != nil {
		t.Fatalf("Read after rewrite: %v", err)
	}
	if got.Subdomain != "second" {
		t.Fatalf("subdomain = %q, want the rewritten value", got.Subdomain)
	}
}

func TestReadMissingReturnsNothing(t *testing.T) {
	lock, err := Read(t.TempDir(), "never-created")
	if err != nil {
		t.Fatalf("a missing lockfile should not be an error, got %v", err)
	}
	if lock != nil {
		t.Fatalf("got %+v, want nil for a missing lockfile", lock)
	}
}

func TestReadRejectsCorruptAndFutureVersions(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantIn  string
	}{
		{"not json", "{not json at all", "not valid JSON"},
		{"future version", `{"lockfileVersion": 2, "name": "x"}`, "expected 1"},
		{"missing version", `{"name": "x"}`, "expected 1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, ".kraai"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(mustPath(t, dir, "env-a"), []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Read(dir, "env-a")
			if err == nil {
				t.Fatal("a corrupt lockfile was treated as readable — down would silently regress to declaration-only deletion")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("got %v, want an error mentioning %q", err, tc.wantIn)
			}
		})
	}
}

func TestDelete(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, Empty("env-a", "1.0.0", "a", "s")); err != nil {
		t.Fatal(err)
	}
	if err := Delete(dir, "env-a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(mustPath(t, dir, "env-a")); !os.IsNotExist(err) {
		t.Fatal("lockfile still present after Delete")
	}
	// Already gone is success, not an error.
	if err := Delete(dir, "env-a"); err != nil {
		t.Fatalf("second Delete should be a no-op, got %v", err)
	}
}

func TestMergeResources(t *testing.T) {
	const env = "env-a"
	const key = "api"

	t.Run("both sides empty yields empty non-nil lists", func(t *testing.T) {
		got := MergeResources(nil, nil, key, env)
		for name, list := range map[string][]Resource{
			"d1": got.D1, "kv": got.KV, "r2": got.R2, "queues": got.Queues,
		} {
			if list == nil {
				t.Errorf("%s is nil, want an empty list — it would serialize as null", name)
			}
			if len(list) != 0 {
				t.Errorf("%s = %v, want empty", name, list)
			}
		}
	})

	t.Run("keeps a resource the lock records but nothing declares", func(t *testing.T) {
		lockSvc := &Service{Resources: Resources{D1: []Resource{{Binding: "OLD", Name: "env-a-api-old"}}}}
		got := MergeResources(lockSvc, &Bindings{}, key, env)
		if len(got.D1) != 1 || got.D1[0].Name != "env-a-api-old" {
			t.Fatalf("got %v — a binding removed between up and down would leak", got.D1)
		}
	})

	t.Run("keeps a declared resource with no lock at all", func(t *testing.T) {
		got := MergeResources(nil, &Bindings{D1: []string{"DB"}}, key, env)
		if len(got.D1) != 1 {
			t.Fatalf("got %v, want the declared binding for a pre-lockfile environment", got.D1)
		}
		if got.D1[0].Binding != "DB" {
			t.Fatalf("binding = %q, want DB", got.D1[0].Binding)
		}
	})

	t.Run("counts a resource present on both sides once", func(t *testing.T) {
		name := namingFor(t, env, key, "DB")
		lockSvc := &Service{Resources: Resources{D1: []Resource{{Binding: "DB", Name: name}}}}
		got := MergeResources(lockSvc, &Bindings{D1: []string{"DB"}}, key, env)
		if len(got.D1) != 1 {
			t.Fatalf("got %d entries, want 1 — the same resource would be deleted twice", len(got.D1))
		}
	})

	t.Run("dedupes by name when the two sides disagree on binding", func(t *testing.T) {
		name := namingFor(t, env, key, "DB")
		lockSvc := &Service{Resources: Resources{D1: []Resource{{Binding: "RENAMED", Name: name}}}}
		got := MergeResources(lockSvc, &Bindings{D1: []string{"DB"}}, key, env)
		if len(got.D1) != 1 {
			t.Fatalf("got %d entries, want 1 — dedup is by name, not binding", len(got.D1))
		}
		if got.D1[0].Binding != "RENAMED" {
			t.Fatalf("binding = %q, want the lock's record of what was actually provisioned", got.D1[0].Binding)
		}
	})

	t.Run("merges each type independently", func(t *testing.T) {
		lockSvc := &Service{Resources: Resources{
			KV: []Resource{{Binding: "CACHE", Name: "env-a-api-cache"}},
		}}
		got := MergeResources(lockSvc, &Bindings{R2: []string{"BUCKET"}}, key, env)
		if len(got.KV) != 1 || len(got.R2) != 1 {
			t.Fatalf("kv=%v r2=%v, want one entry each", got.KV, got.R2)
		}
		if len(got.D1) != 0 || len(got.Queues) != 0 {
			t.Fatalf("d1=%v queues=%v, want both empty", got.D1, got.Queues)
		}
	})
}

// namingFor derives a resource name the same way MergeResources does, so a
// test asserting on dedup uses the real derived name rather than a guess that
// would silently stop matching if the naming policy changed.
func namingFor(t *testing.T, env, key, binding string) string {
	t.Helper()
	return MergeResources(nil, &Bindings{D1: []string{binding}}, key, env).D1[0].Name
}

// mustPath is Path with the error asserted away, for tests whose subject is
// something other than name validation.
func mustPath(t *testing.T, dir, name string) string {
	t.Helper()
	p, err := Path(dir, name)
	if err != nil {
		t.Fatalf("Path(%q, %q): %v", dir, name, err)
	}
	return p
}

// TestPathRejectsTraversal is the reason Path validates at all: filepath.Join
// cleans its argument, so an unvalidated name escapes the lock directory
// entirely and Read would open, or Write clobber, whatever it lands on.
func TestPathRejectsTraversal(t *testing.T) {
	for _, name := range []string{
		"../../../etc/passwd",
		"..",
		"nested/name",
		"name/../../escape",
		"",
		"Has-Capitals",
		"trailing-",
	} {
		if _, err := Path("/tmp/project", name); err == nil {
			t.Errorf("Path accepted %q", name)
		}
	}

	// The read and write paths must refuse it too, not just Path.
	dir := t.TempDir()
	if _, err := Read(dir, "../escape"); err == nil {
		t.Error("Read accepted a traversing name")
	}
	if err := Delete(dir, "../escape"); err == nil {
		t.Error("Delete accepted a traversing name")
	}
	if err := Write(dir, &Lock{LockfileVersion: Version, Name: "../escape"}); err == nil {
		t.Error("Write accepted a traversing name")
	}
}
