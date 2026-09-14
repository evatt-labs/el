package cloudflare

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestLargeListingIsNotTruncated: a real R2 page carries up to 1000 keys and
// a busy account's KV listing is larger still. Truncating a success response
// fails the call, and it fails it in teardown, where the result is an
// orphaned resource nobody is tracking.
func TestLargeListingIsNotTruncated(t *testing.T) {
	var b strings.Builder
	b.WriteString(`[`)
	const n = 1000
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":"ns-%d","title":"a-reasonably-long-namespace-title-for-environment-%d"}`, i, i)
	}
	b.WriteString(`]`)
	payload := b.String()
	t.Logf("listing payload is %d bytes", len(payload))

	client, _ := newTestClient(t, func(*recorded) (int, string) { return ok(payload) })

	found, err := client.KV.FindByTitle(t.Context(), fmt.Sprintf("a-reasonably-long-namespace-title-for-environment-%d", n-1))
	if err != nil {
		t.Fatalf("a legitimate large listing failed: %v", err)
	}
	if found == nil {
		t.Fatal("the last entry was lost — the response was truncated")
	}
}

// TestOversizedResponseIsReportedHonestly: past the ceiling the call must say
// the response was too large, not blame Cloudflare for a 200 it answered
// correctly. The bug this replaced reported `cloudflare API returned 200`,
// which sends the reader to a dashboard showing nothing wrong.
func TestOversizedResponseIsReportedHonestly(t *testing.T) {
	huge := strings.Repeat("x", maxResponseBody+1024)
	client, _ := newTestClient(t, func(*recorded) (int, string) {
		return 200, `{"success":true,"errors":[],"result":"` + huge + `"}`
	})

	_, err := client.Workers.Subdomain(t.Context())
	if err == nil {
		t.Fatal("expected an error for an oversized response")
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Fatalf("an oversized response was blamed on the API: %v", err)
	}
	if !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("got %v, want an error naming the size ceiling", err)
	}
}

// A decode failure on a 2xx is this end's problem and must say so, rather
// than being reported as an API error.
func TestUndecodableSuccessBodyIsNotBlamedOnTheAPI(t *testing.T) {
	client, _ := newTestClient(t, func(*recorded) (int, string) {
		return 200, `{"success":true,"result":{"subdomain":` // truncated on purpose
	})

	_, err := client.Workers.Subdomain(t.Context())
	if err == nil {
		t.Fatal("expected an error")
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Fatalf("an undecodable body was reported as an API failure: %v", err)
	}
}

// A non-2xx whose body is not JSON at all — Cloudflare's edge serves HTML for
// some 5xx — must still report the status rather than a decode error.
func TestNonJSONErrorBodyStillReportsStatus(t *testing.T) {
	client, _ := newTestClient(t, func(*recorded) (int, string) {
		return 502, `<html><head><title>502 Bad Gateway</title></head></html>`
	})

	_, err := client.Workers.Subdomain(t.Context())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("got %T (%v), want an *APIError carrying the status", err, err)
	}
	if apiErr.Status != 502 {
		t.Fatalf("status = %d, want 502", apiErr.Status)
	}
}
