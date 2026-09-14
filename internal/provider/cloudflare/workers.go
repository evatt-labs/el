package cloudflare

import "context"

// WorkersService covers the account-level Workers settings kraai reads.
type WorkersService struct{ c *Client }

// Subdomain returns the account's workers.dev subdomain, which every
// generated environment URL is built from.
func (s *WorkersService) Subdomain(ctx context.Context) (string, error) {
	result, err := do[struct {
		Subdomain string `json:"subdomain"`
	}](ctx, s.c, request{
		method: "GET",
		path:   s.c.accountPath("workers", "subdomain"),
	})
	if err != nil {
		return "", err
	}
	return result.Subdomain, nil
}
