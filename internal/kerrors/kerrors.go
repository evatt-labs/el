// Package kerrors defines kraai's error types.
//
// WORK IN PROGRESS: this file is a placeholder pushed early (mid-task
// interrupt) so the errors-package workstream branch exists on GitHub
// rather than only in a local worktree. See the PR description for what's
// actually done, partial, and not started — this is not the final shape of
// the package.
//
// Per docs/BLUEPRINT.md D18/D19, this package will provide: a base error
// type wrapping github.com/cockroachdb/errors (so errors.Is/As/Unwrap work
// against it and its cause), a Code plus a fixed ExitCode() per D19's
// exit-code table, and typed constructors for the generic/validation/
// lock-held/confirmation-required buckets. Only cmd/kraai's centralized
// handler is meant to act on ExitCode(), print to stdout/stderr, or read
// KRAAI_DEBUG.
package kerrors
