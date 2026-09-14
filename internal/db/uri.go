// Package db is kraai's Postgres touchpoint: connecting to a provisioned
// database, waiting for it to come up, and running the statements the
// provisioning flow needs.
//
// # Why connection details never travel as arguments
//
// Every credential reaches psql through PG* environment variables, never a
// positional argument. Two independent reasons, both found by testing rather
// than assumed:
//
//  1. A command-line argument containing a live credential is readable by any
//     other local process through /proc/<pid>/cmdline for the argument's whole
//     lifetime. Another process's environment is not readable that way.
//  2. A failing child process tends to get its full argv attached to the
//     error, so a credential in argv escapes through error handling even when
//     nothing ever logs it deliberately.
//
// The same reasoning is why Exec never returns the underlying error: see
// psqlRunner.Exec.
package db

import (
	"net/url"
	"strconv"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// defaultPort and defaultSSLMode are applied when the connection URI omits
// them, matching libpq's own default port and kraai's deliberately strict
// default of require rather than libpq's prefer.
const (
	defaultPort    = 5432
	defaultSSLMode = "require"
)

// ConnectionInfo is a Postgres connection URI parsed into its parts.
//
// Parsing happens once, here, so nothing else re-derives them and — more to
// the point — so nothing else has to pass the URI around as one opaque string
// that ends up somewhere it should not: an argument list, an error message, a
// log line.
type ConnectionInfo struct {
	Scheme   string
	Host     string
	Port     int
	User     string
	Password string
	Database string
	SSLMode  string
}

// ParseConnectionURI splits a Postgres connection URI into its parts.
//
// # Deliberate divergence from the JavaScript implementation
//
// Differential-tested against it across the URI shapes kraai produces: every
// field matches on every case but one. For an IPv6 literal host, Node's
// url.hostname keeps the URI's square brackets ("[2001:db8::1]") and this
// returns the bare address ("2001:db8::1").
//
// Bare is what PGHOST needs: the brackets are URI syntax disambiguating the
// port, not part of the address, and libpq reads PGHOST as a host directly.
// Feeding it a bracketed value should therefore fail to resolve. UNVERIFIED
// against a real libpq — no psql was available where this was written — and
// unreachable in practice today, since the providers kraai provisions hand
// back hostnames rather than address literals. Recorded here so the
// divergence is a decision on the record rather than a silent difference
// someone rediscovers from a connection failure.
func ParseConnectionURI(uri string) (ConnectionInfo, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		// The URI is a credential. Report that it did not parse, never what it
		// was — url.Parse's own error quotes the input back.
		return ConnectionInfo{}, kerrors.Validation("connection URI is not a valid URL")
	}

	port := defaultPort
	if raw := parsed.Port(); raw != "" {
		port, err = strconv.Atoi(raw)
		if err != nil {
			return ConnectionInfo{}, kerrors.Validation("connection URI has a non-numeric port")
		}
	}

	sslMode := defaultSSLMode
	if v := parsed.Query().Get("sslmode"); v != "" {
		sslMode = v
	}

	var user, password string
	if parsed.User != nil {
		// Username and Password return percent-decoded values already, which
		// is what the JavaScript achieved with decodeURIComponent.
		user = parsed.User.Username()
		password, _ = parsed.User.Password()
	}

	return ConnectionInfo{
		Scheme:   parsed.Scheme,
		Host:     parsed.Hostname(),
		Port:     port,
		User:     user,
		Password: password,
		Database: trimLeadingSlash(parsed.Path),
		SSLMode:  sslMode,
	}, nil
}

func trimLeadingSlash(s string) string {
	if len(s) > 0 && s[0] == '/' {
		return s[1:]
	}
	return s
}

// Env renders the connection as the PG* environment variables psql reads,
// as KEY=VALUE pairs ready to append to a child process environment.
func (c ConnectionInfo) Env() []string {
	return []string{
		"PGHOST=" + c.Host,
		"PGPORT=" + strconv.Itoa(c.Port),
		"PGUSER=" + c.User,
		"PGPASSWORD=" + c.Password,
		"PGDATABASE=" + c.Database,
		"PGSSLMODE=" + c.SSLMode,
	}
}
