package wrangler

import (
	"context"
	"os/exec"
	"strings"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// execExecutor runs commands with os/exec. It is the only place in this
// package that spawns a process.
type execExecutor struct{}

func (execExecutor) Run(ctx context.Context, cmd Command) error {
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args...) //nolint:gosec // Path is a wrangler binary resolved from node_modules; args are built by this package
	c.Dir = cmd.Dir
	c.Stdin = cmd.Stdin
	c.Stdout = cmd.Stdout
	c.Stderr = cmd.Stderr
	if err := c.Run(); err != nil {
		// The command line is not included: wrangler is invoked with a
		// generated config path and a worker name, and keeping the habit of
		// not echoing argv is cheaper than auditing every call site for
		// whether this particular one is safe.
		return kerrors.Wrap(err, kerrors.CodeUnexpected, "wrangler %s failed", firstArg(cmd.Args))
	}
	return nil
}

func (execExecutor) Output(ctx context.Context, cmd Command) (string, error) {
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args...) //nolint:gosec // see Run
	c.Dir = cmd.Dir
	c.Stdin = cmd.Stdin
	out, err := c.Output()
	if err != nil {
		return "", kerrors.Wrap(err, kerrors.CodeUnexpected, "wrangler %s failed", firstArg(cmd.Args))
	}
	return string(out), nil
}

// firstArg names the subcommand for an error message without reproducing the
// whole argument list.
func firstArg(args []string) string {
	if len(args) == 0 {
		return "(no subcommand)"
	}
	return strings.Join(args[:min(2, len(args))], " ")
}
