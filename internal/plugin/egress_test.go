package plugin

import (
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"strings"
	"syscall"
	"testing"
)

// TestBlockedEgressRanges is the table the guard's whole value rests on.
// Each blocked case is somewhere a plugin reaching the host's network
// identity would be a real incident; each allowed case is ordinary public
// internet the guard must not break.
func TestBlockedEgressRanges(t *testing.T) {
	blocked := []struct{ addr, why string }{
		{"169.254.169.254", "AWS/Azure/GCP instance metadata — the credential-exfil target"},
		{"169.254.170.2", "ECS task metadata credentials endpoint"},
		{"fe80::1", "IPv6 link-local"},
		{"127.0.0.1", "loopback"},
		{"127.0.0.53", "systemd-resolved stub, still loopback"},
		{"::1", "IPv6 loopback"},
		{"10.0.0.5", "RFC1918"},
		{"172.16.0.1", "RFC1918"},
		{"172.31.255.254", "RFC1918 upper bound"},
		{"192.168.1.1", "RFC1918"},
		{"fd00::1", "IPv6 unique-local"},
		{"100.64.0.1", "CGNAT / mesh-internal"},
		{"0.0.0.0", "unspecified"},
		{"0.1.2.3", "0.0.0.0/8 routes to local host on some stacks"},
		{"255.255.255.255", "broadcast"},
		{"224.0.0.1", "multicast"},
		// The spellings that defeat a naive check.
		{"::ffff:169.254.169.254", "IPv4-mapped IPv6 metadata address"},
		{"::ffff:127.0.0.1", "IPv4-mapped IPv6 loopback"},
		{"::ffff:10.1.2.3", "IPv4-mapped IPv6 RFC1918"},
		// These two are the ones Unmap actually protects: netip's own
		// predicates see through ::ffff: for loopback/private/link-local,
		// but Is4 does not, so the IPv4-only ranges are reachable in this
		// spelling unless the address is unmapped first.
		{"::ffff:100.64.0.1", "IPv4-mapped IPv6 CGNAT"},
		{"::ffff:0.1.2.3", "IPv4-mapped IPv6 0.0.0.0/8"},
		{"64:ff9b::a9fe:a9fe", "NAT64-embedded 169.254.169.254"},
		{"2002:a9fe:a9fe::1", "6to4-embedded 169.254.169.254"},
	}
	for _, tc := range blocked {
		ip, err := netip.ParseAddr(tc.addr)
		if err != nil {
			t.Fatalf("bad test address %q: %v", tc.addr, err)
		}
		if !isBlockedEgressIP(ip) {
			t.Errorf("%s is reachable by a plugin, want blocked (%s)", tc.addr, tc.why)
		}
	}

	allowed := []string{
		"1.1.1.1",
		"8.8.8.8",
		"93.184.216.34",
		"2606:4700:4700::1111",
		"2001:4860:4860::8888",
	}
	for _, addr := range allowed {
		ip, err := netip.ParseAddr(addr)
		if err != nil {
			t.Fatalf("bad test address %q: %v", addr, err)
		}
		if isBlockedEgressIP(ip) {
			t.Errorf("%s is blocked, want reachable — the guard is denying ordinary public internet", addr)
		}
	}
}

// TestGuardedDialRefusesIPLiteral proves the guard refuses before any
// connection attempt, using an IP literal so no DNS and no socket is
// involved at all.
func TestGuardedDialRefusesIPLiteral(t *testing.T) {
	dial := GuardedDialContext(nil)
	for _, addr := range []string{
		"169.254.169.254:80",
		"127.0.0.1:80",
		"[::ffff:169.254.169.254]:80",
		"10.0.0.1:443",
	} {
		conn, err := dial(t.Context(), "tcp", addr)
		if err == nil {
			_ = conn.Close()
			t.Errorf("dial to %s succeeded, want ErrBlockedEgress", addr)
			continue
		}
		if !errors.Is(err, ErrBlockedEgress) {
			t.Errorf("dial to %s failed with %v, want ErrBlockedEgress", addr, err)
		}
	}
}

// TestGuardedDialRefusesLiveLoopbackListener is the same check against a
// socket that genuinely exists and would otherwise accept: the guard has
// to refuse a reachable internal service, not merely an unroutable
// address that would have failed anyway.
func TestGuardedDialRefusesLiveLoopbackListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	// Control: an unguarded dialer does connect, so the refusal below is
	// the guard and not a dead listener.
	var plain net.Dialer
	conn, err := plain.DialContext(t.Context(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("unguarded dial to the test listener failed: %v", err)
	}
	_ = conn.Close()

	if conn, err := GuardedDialContext(nil)(t.Context(), "tcp", ln.Addr().String()); err == nil {
		_ = conn.Close()
		t.Fatal("guarded dial reached a live loopback listener")
	} else if !errors.Is(err, ErrBlockedEgress) {
		t.Fatalf("got %v, want ErrBlockedEgress", err)
	}
}

// TestGuardedDialAllowsPublicAddress pins the permit path and, with it,
// the anti-rebinding property: the address handed down to the real dialer
// must be the vetted IP, never the hostname the plugin supplied.
//
// net.Dialer.Control runs after address selection and before connect, so
// returning an error from it records exactly what would have been dialed
// without opening a socket or touching the network.
func TestGuardedDialAllowsPublicAddress(t *testing.T) {
	errStop := errors.New("stop before connecting")
	var dialed []string
	base := &net.Dialer{
		Control: func(_, address string, _ syscall.RawConn) error {
			dialed = append(dialed, address)
			return errStop
		},
	}

	_, err := guardedDialContext(base)(t.Context(), "tcp", "93.184.216.34:443")
	if errors.Is(err, ErrBlockedEgress) {
		t.Fatal("a public address was blocked — the guard is denying ordinary internet")
	}
	if !errors.Is(err, errStop) {
		t.Fatalf("got %v, want the base dialer to have been reached", err)
	}
	if len(dialed) != 1 || dialed[0] != "93.184.216.34:443" {
		t.Fatalf("base dialer saw %v, want exactly the vetted IP and port", dialed)
	}
}

// TestHTTPCapabilityRejectsNonHTTPSchemes covers what a plugin may ask
// the host to do at all, as distinct from where it may go: file:// never
// reaches a dialer, so the egress guard would never see it.
func TestHTTPCapabilityRejectsNonHTTPSchemes(t *testing.T) {
	c := NewHTTPCapability(nil)
	for _, u := range []string{
		"file:///etc/passwd",
		"file:///proc/self/environ",
		"gopher://127.0.0.1:6379/_INFO",
		"ftp://internal.invalid/",
	} {
		body, _ := json.Marshal(httpFetchRequest{Method: "GET", URL: u})
		_, err := c.Invoke(t.Context(), body)
		if err == nil {
			t.Errorf("%s was accepted, want a scheme rejection", u)
			continue
		}
		if !strings.Contains(err.Error(), "scheme") {
			t.Errorf("%s failed with %v, want a scheme rejection", u, err)
		}
	}
}

// TestHTTPCapabilityBlocksMetadataEndToEnd is the finding itself, run
// through the real capability with its default client: a plugin asking
// for cloud instance credentials gets a blocked-egress error rather than
// an IAM role.
func TestHTTPCapabilityBlocksMetadataEndToEnd(t *testing.T) {
	c := NewHTTPCapability(nil)
	body, _ := json.Marshal(httpFetchRequest{
		Method: "GET",
		URL:    "http://169.254.169.254/latest/meta-data/iam/security-credentials/",
	})

	out, err := c.Invoke(t.Context(), body)
	if err == nil {
		t.Fatalf("plugin reached the instance metadata service and got %d bytes back", len(out))
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("got %v, want a blocked-egress error", err)
	}
}
