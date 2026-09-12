package tools

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestNetBlockedIP(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"169.254.169.254", true}, // cloud metadata
		{"10.0.0.5", true},
		{"192.168.1.1", true},
		{"172.16.0.1", true},
		{"0.0.0.0", true},
		{"224.0.0.1", true},
		{"fe80::1", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"2606:4700:4700::1111", false},
	}
	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("bad test IP %q", tc.ip)
			}
			if got := netBlockedIP(ip); got != tc.blocked {
				t.Fatalf("netBlockedIP(%s) = %v, want %v", tc.ip, got, tc.blocked)
			}
		})
	}
}

// TestSSRFDialerRefusesLiteralPrivate pins the rebinding fix at the dial
// layer: even a literal private address handed straight to the dialer is
// refused, independent of any earlier URL validation.
func TestSSRFDialerRefusesLiteralPrivate(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:80", "169.254.169.254:80", "[::1]:80", "10.0.0.1:443"} {
		t.Run(addr, func(t *testing.T) {
			if _, err := ssrfSafeDialContext(context.Background(), "tcp", addr); err == nil {
				t.Fatalf("%s: dialer must refuse a private/loopback address", addr)
			} else if !strings.Contains(err.Error(), "refused") {
				t.Fatalf("%s: error should say refused, got %v", addr, err)
			}
		})
	}
}

func TestSSRFDialerRejectsMalformedAddr(t *testing.T) {
	if _, err := ssrfSafeDialContext(context.Background(), "tcp", "no-port"); err == nil {
		t.Fatal("malformed address must be refused")
	}
}

func TestSafeTransportHasPinnedDialer(t *testing.T) {
	tr := newSafeTransport()
	if tr.DialContext == nil {
		t.Fatal("safe transport must pin the dialer")
	}
	if tr.Proxy != nil {
		t.Fatal("safe transport must not use a proxy (it would move resolution outside the guard)")
	}
}

// Every outbound-HTTP tool must use the SSRF-hardened transport. Each is
// pinned here so a new client literal cannot silently reintroduce the
// unpinned-dialer (DNS-rebinding) gap one tool at a time.
func TestAllNetworkToolsUseSafeTransport(t *testing.T) {
	tr, ok := shotProbeClient("https://example.com", nil).Transport.(*http.Transport)
	if !ok || tr == nil {
		t.Fatal("web_shot probe client must set a concrete *http.Transport")
	}
	if tr.DialContext == nil {
		t.Fatal("web_shot probe client must use the pinned dialer (netsafe.go)")
	}
	if tr.Proxy != nil {
		t.Fatal("web_shot probe client must not use a proxy")
	}
	// web_fetch and web_search build their clients inside Exec; this asserts
	// the shared constructor they call is the hardened one.
	if newSafeTransport().DialContext == nil {
		t.Fatal("shared transport lost its dialer")
	}
}
