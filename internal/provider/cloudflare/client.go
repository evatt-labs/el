// Package cloudflare talks to the Cloudflare REST API for the resources an
// environment is made of: D1 databases, KV namespaces, R2 buckets, Queues,
// and Hyperdrive configurations.
//
// # Why the API rather than wrangler
//
// Worker deploys still go through wrangler, which there is no reason to
// reimplement. Everything here does not, and Hyperdrive is the reason the
// line falls where it does: `wrangler hyperdrive create --connection-string=…`
// puts a live database password in argv, readable by any other local process
// through /proc/<pid>/cmdline for the duration of the call. The REST API
// sends it once, over TLS, in a request body.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// DefaultBaseURL is Cloudflare's API root.
const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

// maxErrorBody bounds how much of a failure response is read before giving
// up. An error body is small; anything larger is a proxy or an error page,
// and reading it in full would let a remote response dictate memory use.
const maxErrorBody = 64 << 10

// Client is a Cloudflare API client scoped to one account.
//
// Resources are grouped into services rather than exposed as one flat method
// set, so a caller writes c.D1.Delete and the package reads as the API it
// wraps.
type Client struct {
	httpClient *http.Client
	baseURL    string
	token      string
	accountID  string

	// D1, KV, R2, Queues, Hyperdrive and Workers are the resource services.
	D1         *D1Service
	KV         *KVService
	R2         *R2Service
	Queues     *QueuesService
	Hyperdrive *HyperdriveService
	Workers    *WorkersService
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient substitutes the transport, for tests and for a caller that
// has tuned its own.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.httpClient = h }
}

// WithBaseURL points the client at a different API root, which is how tests
// aim it at an httptest server.
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimSuffix(u, "/") }
}

// New builds a client for accountID authenticating with token.
func New(token, accountID string, opts ...Option) *Client {
	c := &Client{
		httpClient: &http.Client{Timeout: 60 * time.Second},
		baseURL:    DefaultBaseURL,
		token:      token,
		accountID:  accountID,
	}
	for _, opt := range opts {
		opt(c)
	}
	c.D1 = &D1Service{c: c}
	c.KV = &KVService{c: c}
	c.R2 = &R2Service{c: c}
	c.Queues = &QueuesService{c: c}
	c.Hyperdrive = &HyperdriveService{c: c}
	c.Workers = &WorkersService{c: c}
	return c
}

// accountPath builds an /accounts/<id>/… path with every segment escaped.
//
// Escaping matters: resource names reach these paths from configuration and
// from generated environment names, and an unescaped one containing a slash
// would silently address a different endpoint than the caller asked for.
func (c *Client) accountPath(segments ...string) string {
	parts := make([]string, 0, len(segments)+2)
	parts = append(parts, "accounts", url.PathEscape(c.accountID))
	for _, s := range segments {
		parts = append(parts, url.PathEscape(s))
	}
	return "/" + strings.Join(parts, "/")
}

// APIError is a non-success response from Cloudflare.
type APIError struct {
	Status int
	Path   string
	// Errors is Cloudflare's own error list, when the body carried one.
	Errors []APIErrorItem
}

// APIErrorItem is one entry from Cloudflare's errors array.
type APIErrorItem struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	if len(e.Errors) == 0 {
		return fmt.Sprintf("cloudflare API returned %d for %s", e.Status, e.Path)
	}
	msgs := make([]string, 0, len(e.Errors))
	for _, item := range e.Errors {
		msgs = append(msgs, fmt.Sprintf("%d: %s", item.Code, item.Message))
	}
	return fmt.Sprintf("cloudflare API returned %d for %s (%s)", e.Status, e.Path, strings.Join(msgs, "; "))
}

// envelope is Cloudflare's uniform response wrapper.
type envelope[T any] struct {
	Success bool           `json:"success"`
	Errors  []APIErrorItem `json:"errors"`
	Result  T              `json:"result"`
}

// request is one API call's inputs.
type request struct {
	method string
	path   string
	query  url.Values
	body   any
}

// do issues req and decodes the envelope's result into T.
//
// Generic and package-level rather than a method, because Go methods cannot
// introduce type parameters — the services below call it with their own
// result types.
func do[T any](ctx context.Context, c *Client, req request) (T, error) {
	var zero T

	var payload io.Reader
	if req.body != nil {
		encoded, err := json.Marshal(req.body)
		if err != nil {
			return zero, kerrors.Wrap(err, kerrors.CodeUnexpected, "encoding request for %s", req.path)
		}
		payload = bytes.NewReader(encoded)
	}

	endpoint := c.baseURL + req.path
	if len(req.query) > 0 {
		endpoint += "?" + req.query.Encode()
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.method, endpoint, payload)
	if err != nil {
		return zero, kerrors.Wrap(err, kerrors.CodeUnexpected, "building request for %s", req.path)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.token)
	if req.body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		// Not wrapped: a transport error can carry the full URL, and while
		// these paths hold no credential the token lives in a header that
		// some proxies echo. The path alone is enough to act on.
		return zero, kerrors.Validation("cloudflare API request to %s failed", req.path)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil {
		return zero, kerrors.Wrap(err, kerrors.CodeUnexpected, "reading response from %s", req.path)
	}

	var env envelope[T]
	// A body that does not decode is still a failure worth reporting by
	// status; Cloudflare returns HTML from its edge for some 5xx.
	decodeErr := json.Unmarshal(raw, &env)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !env.Success {
		return zero, &APIError{Status: resp.StatusCode, Path: req.path, Errors: env.Errors}
	}
	if decodeErr != nil {
		return zero, kerrors.Wrap(decodeErr, kerrors.CodeUnexpected, "decoding response from %s", req.path)
	}
	return env.Result, nil
}

// doNoResult issues req and discards the result, for deletes.
func doNoResult(ctx context.Context, c *Client, req request) error {
	_, err := do[json.RawMessage](ctx, c, req)
	return err
}
