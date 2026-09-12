package tools

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// netBlockedIP reports whether an address is un-routable for this tool:
// loopback, link-local (cloud metadata lives at 169.254.169.254),
// private ranges, multicast, unspecified, or otherwise non-global.
//
// One implementation for both web_fetch and web_search — they share the
// same trust boundary, and a fix here must reach both.
func netBlockedIP(ip net.IP) bool {
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
		return true
	}
	if !ip.IsGlobalUnicast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 0 {
		return true
	}
	return false
}

// ssrfSafeDialContext resolves the target host once, refuses it when any
// resolved address is un-routable, and connects to the exact address that
// was validated.
//
// This closes the DNS-rebinding window that a validate-then-request shape
// leaves open: the transport would otherwise resolve the host a second
// time and could land on a private address that the earlier check never
// saw. Here the check and the connect share one resolution.
//
// TLS is unaffected: net/http derives SNI from the request URL's host, not
// from the dialed address.
func ssrfSafeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("refused: malformed address %q", addr)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("refused %q: DNS lookup failed: %v", host, err)
	}
	for _, a := range ips {
		if netBlockedIP(a.IP) {
			return nil, fmt.Errorf("refused %q: resolves to private/loopback/link-local address", host)
		}
	}
	var d net.Dialer
	var lastErr error
	for _, a := range ips {
		conn, derr := d.DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
		if derr == nil {
			return conn, nil
		}
		lastErr = derr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no usable address for %q", host)
	}
	return nil, lastErr
}

// newSafeTransport builds the transport used by the SSRF-guarded fetchers.
//
// Proxy is deliberately nil. An HTTP proxy would resolve the target host
// itself, moving the connection outside this guard — the exact bypass the
// pinned dialer exists to prevent. Network access is already default-deny
// and explicitly opted into, so a direct, guarded connection is the correct
// trade for these tools.
func newSafeTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           ssrfSafeDialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		IdleConnTimeout:       15 * time.Second,
		MaxIdleConns:          8,
	}
}
