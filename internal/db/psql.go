package db

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// Runner executes one SQL statement against a database and returns its
// trimmed output (D21: every external-system touchpoint sits behind an
// interface).
//
// The interface exists for a second reason beyond testing. The psql-backed
// implementation below requires a psql binary on PATH, which sits awkwardly
// against D1's single-binary promise — a GoReleaser-built kraai cannot assume
// psql is installed. Replacing it with a pure-Go driver is then a matter of
// another Runner, with no caller changing.
type Runner interface {
	Run(ctx context.Context, conn ConnectionInfo, sql string) (string, error)
}

// PsqlRunner runs statements by shelling out to psql.
type PsqlRunner struct {
	// Binary is the psql executable; empty means "psql", resolved on PATH.
	Binary string
}

// Run executes sql via psql in tuples-only, unaligned mode and returns its
// trimmed stdout.
//
// On failure it returns an error built from scratch, never one wrapping the
// underlying failure. A failing psql attaches its argv to the error and
// echoes connection details in its own stderr, and either one would then leak
// through whichever field a caller happened to print. A brand-new error with
// a hand-written message is the only way to guarantee none of it survives,
// which is why the exit status is all that crosses this boundary.
func (p PsqlRunner) Run(ctx context.Context, conn ConnectionInfo, sql string) (string, error) {
	binary := p.Binary
	if binary == "" {
		binary = "psql"
	}

	cmd := exec.CommandContext(ctx, binary, "-tAc", sql) //nolint:gosec // sql is built by this package's callers; connection details go via Env below, never argv
	// Credentials travel in the environment, never in argv — see the package
	// comment for why that distinction is load-bearing rather than stylistic.
	cmd.Env = append(os.Environ(), conn.Env()...)
	cmd.Stdin = nil

	var stdout strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = nil // deliberately discarded: psql echoes connection details

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", kerrors.Wrap(ctx.Err(), kerrors.CodeUnexpected, "psql was cancelled")
		}
		status := "?"
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			status = exitErr.String()
		}
		return "", kerrors.Validation(
			"psql failed (%s). Run the same statement manually with the same PG* environment "+
				"variables to see the original error — it is deliberately not included here "+
				"because it may echo back connection details.", status)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Client is the Postgres operations kraai's provisioning flow needs.
type Client struct {
	runner Runner
	// retryDelay is how long WaitForConnectable waits between attempts;
	// a field so tests do not spend half a minute sleeping.
	retryDelay time.Duration
}

// New builds a Client over runner. A nil runner gets the psql-backed one.
func New(runner Runner) *Client {
	if runner == nil {
		runner = PsqlRunner{}
	}
	return &Client{runner: runner, retryDelay: 2 * time.Second}
}

// DefaultConnectAttempts is how many times WaitForConnectable probes before
// giving up, matching the JavaScript it replaces.
const DefaultConnectAttempts = 15

// WaitForConnectable polls until the database accepts a trivial query, or
// until attempts are exhausted or ctx is done.
func (c *Client) WaitForConnectable(ctx context.Context, conn ConnectionInfo, attempts int) error {
	if attempts <= 0 {
		attempts = DefaultConnectAttempts
	}
	for i := 0; i < attempts; i++ {
		if _, err := c.runner.Run(ctx, conn, "select 1"); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return kerrors.Wrap(ctx.Err(), kerrors.CodeUnexpected, "waiting for the database to accept connections")
		}
		if i == attempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return kerrors.Wrap(ctx.Err(), kerrors.CodeUnexpected, "waiting for the database to accept connections")
		case <-time.After(c.retryDelay):
		}
	}
	return kerrors.Validation("database did not become connectable within %d attempts", attempts)
}

// AssertNoBypassRLS fails unless role has BYPASSRLS disabled.
//
// Defensive, not decorative: a Neon-created role inherits BYPASSRLS from
// neon_superuser membership by default, which silently defeats row-level
// security for that role.
//
// The role name is quoted through QuoteLiteral rather than interpolated. The
// JavaScript this replaces wrote the quotes by hand — `rolname = '${role}'` —
// with QuoteLiteral sitting unused in the same file. A role name is
// configuration, not a constant, so a quote character in one breaks the query
// and a crafted one steers it. That matters more here than anywhere else in
// the package: this is the row-level-security check, and a statement that can
// be steered into returning "f" reports safety it never verified.
func (c *Client) AssertNoBypassRLS(ctx context.Context, conn ConnectionInfo, role string) error {
	out, err := c.runner.Run(ctx, conn,
		"select rolbypassrls from pg_roles where rolname = "+QuoteLiteral(role))
	if err != nil {
		return err
	}
	if out != "f" {
		return kerrors.Validation(
			"role %q has BYPASSRLS=%q — refusing to continue, row-level security would be "+
				"silently defeated for this role", role, out)
	}
	return nil
}

// RunSQL executes one statement and returns its trimmed output.
//
// There is no parameterization here: this is a single psql invocation, not a
// prepared-statement API. Build the statement with QuoteLiteral for any value
// that is not a constant this codebase wrote itself.
func (c *Client) RunSQL(ctx context.Context, conn ConnectionInfo, sql string) (string, error) {
	return c.runner.Run(ctx, conn, sql)
}

// QuoteLiteral renders value as a Postgres string literal the way the
// server's own quote_literal() does: wrapped in single quotes, with any
// single quote inside doubled.
func QuoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
