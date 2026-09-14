package db

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The connection URIs below are fabricated fixtures with throwaway
// credentials — a URI parser cannot be tested without them, and gosec's G101
// cannot tell a fixture from a leak.
//
//nolint:gosec // G101: fabricated test credentials, not real ones
func TestParseConnectionURI(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want ConnectionInfo
	}{
		{
			name: "a typical Neon connection string",
			uri:  "postgresql://alice:s3cret@ep-cool-name-123456.us-east-2.aws.neon.tech/appdb?sslmode=require",
			want: ConnectionInfo{
				Scheme: "postgresql", Host: "ep-cool-name-123456.us-east-2.aws.neon.tech",
				Port: 5432, User: "alice", Password: "s3cret", Database: "appdb", SSLMode: "require",
			},
		},
		{
			name: "port defaults to 5432 when omitted",
			uri:  "postgres://u:p@db.example.com/mydb",
			want: ConnectionInfo{Scheme: "postgres", Host: "db.example.com", Port: 5432, User: "u", Password: "p", Database: "mydb", SSLMode: "require"},
		},
		{
			name: "an explicit non-default port is respected",
			uri:  "postgres://u:p@db.example.com:6543/mydb",
			want: ConnectionInfo{Scheme: "postgres", Host: "db.example.com", Port: 6543, User: "u", Password: "p", Database: "mydb", SSLMode: "require"},
		},
		{
			name: "sslmode defaults to require, not libpq's prefer",
			uri:  "postgres://u:p@db.example.com/mydb",
			want: ConnectionInfo{Scheme: "postgres", Host: "db.example.com", Port: 5432, User: "u", Password: "p", Database: "mydb", SSLMode: "require"},
		},
		{
			name: "an explicit sslmode wins",
			uri:  "postgres://u:p@db.example.com/mydb?sslmode=verify-full",
			want: ConnectionInfo{Scheme: "postgres", Host: "db.example.com", Port: 5432, User: "u", Password: "p", Database: "mydb", SSLMode: "verify-full"},
		},
		{
			name: "percent-encoded credentials are decoded",
			uri:  "postgres://user%40tenant:p%40ss%3Aword@db.example.com/mydb",
			want: ConnectionInfo{Scheme: "postgres", Host: "db.example.com", Port: 5432, User: "user@tenant", Password: "p@ss:word", Database: "mydb", SSLMode: "require"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseConnectionURI(tc.uri)
			if err != nil {
				t.Fatalf("ParseConnectionURI: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// TestParseConnectionURIErrorsKeepTheCredentialOut is the property that makes
// the rest of this package's care worthwhile: a URI that fails to parse is
// still a credential, and the standard library's own parse error quotes the
// whole input back.
func TestParseConnectionURIErrorsKeepTheCredentialOut(t *testing.T) {
	const secret = "hunter2supersecret"
	_, err := ParseConnectionURI("postgres://u:" + secret + "@db.example.com:notaport/x")
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the error echoed the password back: %v", err)
	}
}

func TestConnectionInfoEnv(t *testing.T) {
	conn, err := ParseConnectionURI("postgres://u:p@h.example.com:6543/d?sslmode=verify-full")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"PGHOST=h.example.com": true, "PGPORT=6543": true, "PGUSER=u": true,
		"PGPASSWORD=p": true, "PGDATABASE=d": true, "PGSSLMODE=verify-full": true,
	}
	got := conn.Env()
	if len(got) != len(want) {
		t.Fatalf("got %d vars, want %d: %v", len(got), len(want), got)
	}
	for _, kv := range got {
		if !want[kv] {
			t.Errorf("unexpected env entry %q", kv)
		}
	}
}

func TestQuoteLiteral(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"wraps a plain value in single quotes", "tenant", "'tenant'"},
		{"doubles an embedded quote rather than letting it close the literal", "O'Brien", "'O''Brien'"},
		{"neutralizes an injection payload", "'; drop table users; --", "'''; drop table users; --'"},
		{"handles an empty value", "", "''"},
		{"doubles every quote, not just the first", "a'b'c", "'a''b''c'"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := QuoteLiteral(tc.in); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// fakeRunner records statements and replays scripted responses.
type fakeRunner struct {
	statements []string
	responses  []string
	errs       []error
	calls      int
}

func (f *fakeRunner) Run(_ context.Context, _ ConnectionInfo, sql string) (string, error) {
	f.statements = append(f.statements, sql)
	i := f.calls
	f.calls++
	if i < len(f.errs) && f.errs[i] != nil {
		return "", f.errs[i]
	}
	if i < len(f.responses) {
		return f.responses[i], nil
	}
	return "", nil
}

// TestAssertNoBypassRLSQuotesTheRole is the regression test for a live bug in
// the JavaScript this replaces: it interpolated the role straight into the
// statement while QuoteLiteral sat unused in the same file. A role name is
// configuration, not a constant.
func TestAssertNoBypassRLSQuotesTheRole(t *testing.T) {
	runner := &fakeRunner{responses: []string{"f"}}
	client := New(runner)

	err := client.AssertNoBypassRLS(t.Context(), ConnectionInfo{}, "app'; select 't")
	if err != nil {
		t.Fatalf("AssertNoBypassRLS: %v", err)
	}
	stmt := runner.statements[0]
	if !strings.Contains(stmt, "'app''; select ''t'") {
		t.Fatalf("role was not quoted; statement was: %s", stmt)
	}
	// The injected fragment must not survive as syntax.
	if strings.Contains(stmt, "= 'app'; select") {
		t.Fatalf("statement is steerable: %s", stmt)
	}
}

func TestAssertNoBypassRLSRejectsEnabledBypass(t *testing.T) {
	client := New(&fakeRunner{responses: []string{"t"}})
	err := client.AssertNoBypassRLS(t.Context(), ConnectionInfo{}, "app")
	if err == nil {
		t.Fatal("BYPASSRLS=t was accepted — row-level security would be silently defeated")
	}
	if !strings.Contains(err.Error(), "BYPASSRLS") {
		t.Fatalf("got %v, want an error naming BYPASSRLS", err)
	}
}

// A role that does not exist returns no rows, so the output is empty rather
// than "f". That must fail closed, not pass.
func TestAssertNoBypassRLSFailsClosedOnMissingRole(t *testing.T) {
	client := New(&fakeRunner{responses: []string{""}})
	if err := client.AssertNoBypassRLS(t.Context(), ConnectionInfo{}, "nonexistent"); err == nil {
		t.Fatal("an absent role was treated as safe")
	}
}

func TestWaitForConnectableRetriesThenSucceeds(t *testing.T) {
	runner := &fakeRunner{
		errs:      []error{errors.New("down"), errors.New("down"), nil},
		responses: []string{"", "", "1"},
	}
	client := New(runner)
	client.retryDelay = time.Millisecond

	if err := client.WaitForConnectable(t.Context(), ConnectionInfo{}, 5); err != nil {
		t.Fatalf("WaitForConnectable: %v", err)
	}
	if runner.calls != 3 {
		t.Fatalf("probed %d times, want 3", runner.calls)
	}
}

func TestWaitForConnectableGivesUp(t *testing.T) {
	runner := &fakeRunner{errs: []error{errors.New("d"), errors.New("d"), errors.New("d")}}
	client := New(runner)
	client.retryDelay = time.Millisecond

	err := client.WaitForConnectable(t.Context(), ConnectionInfo{}, 3)
	if err == nil {
		t.Fatal("expected failure after exhausting attempts")
	}
	if runner.calls != 3 {
		t.Fatalf("probed %d times, want exactly the 3 attempts allowed", runner.calls)
	}
}

// The wait must honour cancellation rather than sleeping out its full budget
// — with the default delay that is half a minute of ignoring a cancelled ctx.
func TestWaitForConnectableHonoursCancellation(t *testing.T) {
	runner := &fakeRunner{errs: []error{errors.New("d"), errors.New("d"), errors.New("d")}}
	client := New(runner)
	client.retryDelay = time.Hour

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	start := time.Now()
	if err := client.WaitForConnectable(ctx, ConnectionInfo{}, 15); err == nil {
		t.Fatal("expected a cancellation error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %v to notice a cancelled context", elapsed)
	}
}

// TestParseConnectionURIUnbracketsIPv6 pins the one place this parser
// deliberately disagrees with the JavaScript it replaces. See
// ParseConnectionURI's doc comment: PGHOST wants the bare address, and the
// brackets are URI syntax rather than part of the host.
func TestParseConnectionURIUnbracketsIPv6(t *testing.T) {
	got, err := ParseConnectionURI("postgresql://u:p@[2001:db8::1]:5433/d")
	if err != nil {
		t.Fatalf("ParseConnectionURI: %v", err)
	}
	if got.Host != "2001:db8::1" {
		t.Fatalf("host = %q, want the bare address with no brackets", got.Host)
	}
	if got.Port != 5433 {
		t.Fatalf("port = %d, want 5433", got.Port)
	}
	for _, kv := range got.Env() {
		if kv == "PGHOST=[2001:db8::1]" {
			t.Fatal("PGHOST carried the URI's brackets through to libpq")
		}
	}
}
