package cloudflare

import (
	"context"
	"net/url"
)

// D1Service manages D1 databases.
type D1Service struct{ c *Client }

// D1Database is a D1 database as the API reports it.
type D1Database struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// Create provisions a database and returns its id.
func (s *D1Service) Create(ctx context.Context, name string) (string, error) {
	db, err := do[D1Database](ctx, s.c, request{
		method: "POST",
		path:   s.c.accountPath("d1", "database"),
		body:   map[string]string{"name": name},
	})
	if err != nil {
		return "", err
	}
	return db.UUID, nil
}

// FindByName returns the database called name, or nil when there is none.
//
// The list endpoint accepts a name filter, so this asks the API to do the
// matching rather than paging the whole account and filtering here. The exact
// match is still checked below: the parameter filters, and nothing documents
// it as an exact-match rather than a prefix or substring.
//
// A nil result with a nil error is the "not there" answer teardown needs:
// deleting something already gone is success, not failure.
func (s *D1Service) FindByName(ctx context.Context, name string) (*D1Database, error) {
	query := url.Values{}
	query.Set("name", name)
	databases, err := do[[]D1Database](ctx, s.c, request{
		method: "GET",
		path:   s.c.accountPath("d1", "database"),
		query:  query,
	})
	if err != nil {
		return nil, err
	}
	for i := range databases {
		if databases[i].Name == name {
			return &databases[i], nil
		}
	}
	return nil, nil
}

// Delete removes a database by id.
func (s *D1Service) Delete(ctx context.Context, databaseID string) error {
	return doNoResult(ctx, s.c, request{
		method: "DELETE",
		path:   s.c.accountPath("d1", "database", databaseID),
	})
}
