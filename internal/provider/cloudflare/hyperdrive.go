package cloudflare

import (
	"context"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// HyperdriveService manages Hyperdrive configurations.
type HyperdriveService struct{ c *Client }

// HyperdriveConfig is a Hyperdrive configuration as the API reports it.
type HyperdriveConfig struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Origin is the database a Hyperdrive configuration fronts.
//
// Declared here rather than taken from the database package on purpose: a
// provider package describing what it sends to its own API keeps the
// dependency pointing one way, and leaves the mapping where it belongs, in
// the orchestration that already knows about both.
type Origin struct {
	Scheme   string `json:"scheme"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	User     string `json:"user"`
	Password string `json:"password"`
}

// Create makes a Hyperdrive configuration for origin, returning its id.
//
// It takes the connection already split into fields rather than a connection
// string, so the password is one field of a request body rather than part of
// a string that could be logged or passed onward whole. This endpoint is what
// justifies using the REST API at all: the wrangler equivalent puts the same
// password in argv, where any local process can read it.
//
// On failure the underlying error is deliberately dropped. Cloudflare's
// validation-error format for this endpoint is not verified to keep request
// fields out of its response, and the one request field here is a live
// database password — so this does not depend on that being true.
func (s *HyperdriveService) Create(ctx context.Context, name string, o Origin, sslMode string) (string, error) {
	config, err := do[HyperdriveConfig](ctx, s.c, request{
		method: "POST",
		path:   s.c.accountPath("hyperdrive", "configs"),
		body: map[string]any{
			"name":   name,
			"origin": o,
			"mtls":   map[string]string{"sslmode": sslMode},
		},
	})
	if err != nil {
		return "", kerrors.Validation(
			"failed to create Hyperdrive config %q — the underlying error is omitted because it "+
				"may echo request details back, and this request carries a database password; "+
				"check the Cloudflare dashboard", name)
	}
	return config.ID, nil
}

// FindByName returns the configuration called name, or nil when absent.
func (s *HyperdriveService) FindByName(ctx context.Context, name string) (*HyperdriveConfig, error) {
	configs, err := do[[]HyperdriveConfig](ctx, s.c, request{
		method: "GET",
		path:   s.c.accountPath("hyperdrive", "configs"),
	})
	if err != nil {
		return nil, err
	}
	for i := range configs {
		if configs[i].Name == name {
			return &configs[i], nil
		}
	}
	return nil, nil
}

// Delete removes a configuration by id.
func (s *HyperdriveService) Delete(ctx context.Context, configID string) error {
	return doNoResult(ctx, s.c, request{
		method: "DELETE",
		path:   s.c.accountPath("hyperdrive", "configs", configID),
	})
}
