package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// ErrNoRows is returned by a Scan that matched nothing. Defined here so
// callers compare against this package rather than importing the driver.
var ErrNoRows = errors.New("db: no rows in result set")

// PgxConnector dials with jackc/pgx. It is the default Connector and the only
// place in this package that knows which driver is in use.
type PgxConnector struct{}

// Connect opens a single connection to the database described by info.
//
// The error deliberately does not wrap pgx's: a connection failure can carry
// the host and user it was attempting, and this package's whole posture is
// that connection details do not travel into messages that get printed or
// logged. The failure reason is preserved in the sentence, the credential is
// not.
func (PgxConnector) Connect(ctx context.Context, info ConnectionInfo) (Querier, error) {
	conn, err := pgx.Connect(ctx, info.DSN())
	if err != nil {
		return nil, kerrors.Validation(
			"could not connect to the database at %s — the underlying error is omitted because "+
				"it may carry connection details", info.Redacted())
	}
	return &pgxConn{conn: conn}, nil
}

// pgxConn adapts a *pgx.Conn to Querier.
type pgxConn struct {
	conn   *pgx.Conn
	closed bool
}

func (p *pgxConn) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return pgxRow{row: p.conn.QueryRow(ctx, sql, args...)}
}

func (p *pgxConn) Exec(ctx context.Context, sql string, args ...any) error {
	if _, err := p.conn.Exec(ctx, sql, args...); err != nil {
		return kerrors.Wrap(err, kerrors.CodeUnexpected, "executing statement")
	}
	return nil
}

func (p *pgxConn) Ping(ctx context.Context) error {
	return p.conn.Ping(ctx)
}

// Close is idempotent: WaitForConnectable's probe and a caller's deferred
// close can both reach it.
func (p *pgxConn) Close(ctx context.Context) error {
	if p.closed {
		return nil
	}
	p.closed = true
	return p.conn.Close(ctx)
}

// pgxRow adapts pgx.Row to Row, translating the driver's no-rows sentinel
// into this package's so callers never import pgx to compare an error.
type pgxRow struct{ row pgx.Row }

func (r pgxRow) Scan(dest ...any) error {
	err := r.row.Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoRows
	}
	return err
}
