package wrangler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeExec records commands and replays scripted results.
type fakeExec struct {
	cmds   []Command
	output string
	err    error
	// configs captures the temp config's contents at the moment the command
	// ran, since it is deleted immediately afterwards.
	configs []string
}

func (f *fakeExec) Run(_ context.Context, cmd Command) error {
	f.capture(cmd)
	return f.err
}

func (f *fakeExec) Output(_ context.Context, cmd Command) (string, error) {
	f.capture(cmd)
	return f.output, f.err
}

func (f *fakeExec) capture(cmd Command) {
	f.cmds = append(f.cmds, cmd)
	for i, a := range cmd.Args {
		if a == "--config" && i+1 < len(cmd.Args) {
			if raw, err := os.ReadFile(cmd.Args[i+1]); err == nil {
				f.configs = append(f.configs, string(raw))
			}
		}
	}
}

func newTestCLI(t *testing.T, exec *fakeExec) *CLI {
	t.Helper()
	return New(
		WithExecutor(exec),
		WithFinder(func(string) (string, error) { return "/fake/node_modules/.bin/wrangler", nil }),
		WithWarner(func(string, ...any) {}),
	)
}

func TestStripComments(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"whole-line comment", "{\n// gone\n\"a\":1}", "{\n\n\"a\":1}"},
		{"trailing comment", `{"a":1} // gone`, `{"a":1} `},
		{
			name: "block comment, which wrangler init generates",
			in:   "{\n/**\n * docs\n */\n\"a\":1}",
			want: "{\n\n\n\n\"a\":1}",
		},
		{"inline block comment", `{"a":/* why */1}`, `{"a":1}`},
		{
			name: "a URL inside a string is not a comment",
			in:   `{"url":"https://example.com/x"}`,
			want: `{"url":"https://example.com/x"}`,
		},
		{
			name: "an escaped quote does not end the string",
			in:   `{"a":"say \"hi\" // not a comment"}`,
			want: `{"a":"say \"hi\" // not a comment"}`,
		},
		{
			name: "a block-comment opener inside a string survives",
			in:   `{"a":"/* literal */"}`,
			want: `{"a":"/* literal */"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripComments(tc.in); got != tc.want {
				t.Fatalf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

// The result must still be parseable JSON, which is the only thing that
// actually matters.
func TestLoadConfigHandlesWranglerInitOutput(t *testing.T) {
	dir := t.TempDir()
	// The shape `wrangler init` actually generates.
	body := `{
	/**
	 * For more details on how to configure Wrangler, refer to:
	 * https://developers.cloudflare.com/workers/wrangler/configuration/
	 */
	"$schema": "node_modules/wrangler/config-schema.json",
	"name": "my-worker", // the deployed name
	"main": "src/index.ts",
	"compatibility_date": "2026-01-01",
	"observability": { "enabled": true }
}`
	if err := os.WriteFile(filepath.Join(dir, "wrangler.jsonc"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	config, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if config["name"] != "my-worker" || config["main"] != "src/index.ts" {
		t.Fatalf("config = %+v", config)
	}
	if _, ok := config["observability"]; !ok {
		t.Fatal("an unknown-to-kraai key was dropped")
	}
}

// TestFindBinaryWalksUp mirrors how Node resolves node_modules.
func TestFindBinaryWalksUp(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, binaryName()), []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "services", "api", "src")
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatal(err)
	}

	got, err := FindBinary(deep)
	if err != nil {
		t.Fatalf("FindBinary: %v", err)
	}
	if got != filepath.Join(binDir, binaryName()) {
		t.Fatalf("found %q", got)
	}
}

// TestFindBinaryRefusesRatherThanFallingBack is the security property. npx
// does not consult PATH: with no local install it downloads and runs the
// latest unpinned wrangler inside a process already holding Cloudflare and
// database credentials.
func TestFindBinaryRefusesRatherThanFallingBack(t *testing.T) {
	_, err := FindBinary(t.TempDir())
	if err == nil {
		t.Fatal("FindBinary succeeded with no local wrangler — a fallback would run an unpinned binary")
	}
	if !strings.Contains(err.Error(), "npx") {
		t.Fatalf("the error should say why there is no fallback: %v", err)
	}
}

func TestDeployWritesAGeneratedConfigAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	exec := &fakeExec{}
	cli := newTestCLI(t, exec)

	config := Config{"name": "env-a-api", "main": "src/index.ts", "vars": map[string]any{"MODE": "preview"}}
	if err := cli.Deploy(t.Context(), dir, config); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	if len(exec.cmds) != 1 || exec.cmds[0].Args[0] != "deploy" {
		t.Fatalf("commands = %+v", exec.cmds)
	}
	if exec.cmds[0].Dir != dir {
		t.Fatalf("ran in %q, want the service directory", exec.cmds[0].Dir)
	}

	// The config wrangler saw must carry the generated name.
	if len(exec.configs) != 1 {
		t.Fatal("no config was written for wrangler to read")
	}
	var seen Config
	if err := json.Unmarshal([]byte(exec.configs[0]), &seen); err != nil {
		t.Fatalf("the generated config is not valid JSON: %v", err)
	}
	if seen["name"] != "env-a-api" {
		t.Fatalf("generated config = %+v", seen)
	}

	// And nothing is left behind in the service directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempConfigPrefix) {
			t.Fatalf("a generated config survived the deploy: %s", e.Name())
		}
	}
}

// TestTempConfigLivesBesideTheService: wrangler resolves `main` and other
// relative paths against the config file's own location, so a config in a
// system temp directory makes "./src/index.ts" resolve somewhere absent.
func TestTempConfigLivesBesideTheService(t *testing.T) {
	dir := t.TempDir()
	exec := &fakeExec{}
	if err := newTestCLI(t, exec).Deploy(t.Context(), dir, Config{"name": "x"}); err != nil {
		t.Fatal(err)
	}

	var configPath string
	for i, a := range exec.cmds[0].Args {
		if a == "--config" {
			configPath = exec.cmds[0].Args[i+1]
		}
	}
	if filepath.Dir(configPath) != dir {
		t.Fatalf("config was written to %q, want it inside the service directory", configPath)
	}
}

// The generated config is removed even when wrangler fails.
func TestTempConfigIsRemovedOnFailure(t *testing.T) {
	dir := t.TempDir()
	exec := &fakeExec{err: errors.New("deploy failed")}

	if err := newTestCLI(t, exec).Deploy(t.Context(), dir, Config{"name": "x"}); err == nil {
		t.Fatal("expected the deploy failure to surface")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempConfigPrefix) {
			t.Fatalf("a generated config survived a failed deploy: %s", e.Name())
		}
	}
}

// TestPutSecretUsesStdin is the credential property: a secret in argv is
// readable by any local process through /proc/<pid>/cmdline.
func TestPutSecretUsesStdin(t *testing.T) {
	exec := &fakeExec{}
	const secret = "super-secret-value"

	if err := newTestCLI(t, exec).PutSecret(t.Context(), t.TempDir(), "env-a-api", "API_KEY", secret); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}

	cmd := exec.cmds[0]
	for _, arg := range cmd.Args {
		if strings.Contains(arg, secret) {
			t.Fatalf("the secret appeared in argv: %v", cmd.Args)
		}
	}
	if cmd.Stdin == nil {
		t.Fatal("no stdin was provided — the value had to reach wrangler somehow")
	}
	delivered, err := io.ReadAll(cmd.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	if string(delivered) != secret {
		t.Fatalf("stdin carried %q", delivered)
	}
}

func TestApplyD1MigrationsIsANoOpWithoutMigrationsDir(t *testing.T) {
	exec := &fakeExec{}
	base := Config{"d1_databases": []any{map[string]any{"binding": "DB"}}}

	if err := newTestCLI(t, exec).ApplyD1Migrations(t.Context(), t.TempDir(), base, "DB", "n", "id"); err != nil {
		t.Fatalf("ApplyD1Migrations: %v", err)
	}
	if len(exec.cmds) != 0 {
		t.Fatalf("ran %v for a binding that declares no migrations", exec.cmds)
	}
}

func TestApplyD1MigrationsTargetsTheEphemeralDatabase(t *testing.T) {
	exec := &fakeExec{}
	base := Config{
		"main":               "src/index.ts",
		"compatibility_date": "2026-01-01",
		"d1_databases": []any{map[string]any{
			"binding": "DB", "migrations_dir": "migrations", "migrations_pattern": "*.sql",
		}},
	}

	if err := newTestCLI(t, exec).ApplyD1Migrations(t.Context(), t.TempDir(), base, "DB", "env-a-api-db", "uuid-1"); err != nil {
		t.Fatalf("ApplyD1Migrations: %v", err)
	}

	args := strings.Join(exec.cmds[0].Args, " ")
	if !strings.Contains(args, "d1 migrations apply env-a-api-db --remote") {
		t.Fatalf("args = %q", args)
	}

	var seen Config
	if err := json.Unmarshal([]byte(exec.configs[0]), &seen); err != nil {
		t.Fatal(err)
	}
	entries, _ := seen["d1_databases"].([]any)
	entry, _ := entries[0].(map[string]any)
	if entry["database_id"] != "uuid-1" || entry["database_name"] != "env-a-api-db" {
		t.Fatalf("migrations ran against %+v, not the freshly provisioned database", entry)
	}
	if entry["migrations_pattern"] != "*.sql" {
		t.Fatalf("migrations_pattern was dropped: %+v", entry)
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct{ in, want string }{
		{" ⛅️ wrangler 4.131.1\n---------------------", "4.131.1"},
		{"4.0.0", "4.0.0"},
		{"no version here", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := ParseVersion(tc.in); got != tc.want {
			t.Errorf("ParseVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Version is diagnostic metadata for the lockfile; a missing wrangler is not
// a reason to fail provisioning that otherwise succeeded.
func TestVersionNeverFails(t *testing.T) {
	cli := New(
		WithExecutor(&fakeExec{err: errors.New("boom")}),
		WithFinder(func(string) (string, error) { return "", errors.New("not found") }),
	)
	if got := cli.Version(t.Context(), t.TempDir()); got != "" {
		t.Fatalf("got %q, want an empty version rather than a failure", got)
	}
}

// Teardown must continue past a worker that is already gone.
func TestDeleteWorkerSwallowsFailures(t *testing.T) {
	var warned int
	cli := New(
		WithExecutor(&fakeExec{err: errors.New("no such worker")}),
		WithFinder(func(string) (string, error) { return "/fake/wrangler", nil }),
		WithWarner(func(string, ...any) { warned++ }),
	)
	if err := cli.DeleteWorker(t.Context(), t.TempDir(), "env-a-api"); err != nil {
		t.Fatalf("DeleteWorker returned %v — teardown would abort on one missing worker", err)
	}
	if warned != 1 {
		t.Fatalf("warned %d times, want 1", warned)
	}
}

// TestApplyD1MigrationsOmitsAbsentKeys pins a Go/JavaScript difference that a
// direct translation gets wrong. JSON.stringify drops an undefined value;
// a Go map with a nil value marshals it as null, and a null is not the same
// as an absent key to whatever reads the config. A service with no top-level
// main — one using a build step — would get a field it never declared.
func TestApplyD1MigrationsOmitsAbsentKeys(t *testing.T) {
	exec := &fakeExec{}
	base := Config{
		// No "main" at all.
		"compatibility_date": "2026-01-01",
		"d1_databases":       []any{map[string]any{"binding": "DB", "migrations_dir": "migrations"}},
	}

	if err := newTestCLI(t, exec).ApplyD1Migrations(t.Context(), t.TempDir(), base, "DB", "n", "id"); err != nil {
		t.Fatalf("ApplyD1Migrations: %v", err)
	}
	if strings.Contains(exec.configs[0], `"main"`) {
		t.Fatalf("a key the base config never declared appeared in the generated one:\n%s", exec.configs[0])
	}
	if !strings.Contains(exec.configs[0], `"compatibility_date"`) {
		t.Fatalf("a key the base config does declare was dropped:\n%s", exec.configs[0])
	}
}
