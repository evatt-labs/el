package cli_test

import (
	"testing"

	"github.com/evatt-labs/kraai/internal/cli"
)

func TestDebugRequested_DefaultFalse(t *testing.T) {
	if err := cli.Execute([]string{"version"}); err != nil {
		t.Fatalf("Execute(version) = %v, want nil", err)
	}
	if cli.DebugRequested() {
		t.Errorf("DebugRequested() = true after a run without --debug, want false")
	}
}

func TestDebugRequested_TrueAfterDebugFlag(t *testing.T) {
	if err := cli.Execute([]string{"--debug", "version"}); err != nil {
		t.Fatalf("Execute(--debug version) = %v, want nil", err)
	}
	if !cli.DebugRequested() {
		t.Errorf("DebugRequested() = false after --debug, want true")
	}
}

// TestDebugRequested_ResetsAcrossExecuteCalls guards against the flag var
// leaking a stale true from a previous Execute call into a later one
// without --debug — NewRootCommand resets it on every call specifically to
// prevent this.
func TestDebugRequested_ResetsAcrossExecuteCalls(t *testing.T) {
	if err := cli.Execute([]string{"--debug", "version"}); err != nil {
		t.Fatalf("Execute(--debug version) = %v, want nil", err)
	}
	if !cli.DebugRequested() {
		t.Fatalf("DebugRequested() = false after --debug, want true")
	}

	if err := cli.Execute([]string{"version"}); err != nil {
		t.Fatalf("Execute(version) = %v, want nil", err)
	}
	if cli.DebugRequested() {
		t.Errorf("DebugRequested() = true after a subsequent run without --debug, want false")
	}
}

func TestNewRootCommand_HasDebugPersistentFlag(t *testing.T) {
	root := cli.NewRootCommand()

	flag := root.PersistentFlags().Lookup("debug")
	if flag == nil {
		t.Fatalf("root command has no --debug persistent flag")
	}
	if flag.DefValue != "false" {
		t.Errorf("--debug default = %q, want %q", flag.DefValue, "false")
	}
}
