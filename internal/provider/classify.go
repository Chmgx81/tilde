package provider

import "strings"

// Error classes for transport/provider failures (Spec §11).
//
// Classify maps raw provider errors into a small stable set consumed by
// UI/exit codes, so callers never surface raw dumps. It classifies
// transport/provider errors (HTTP statuses, timeouts, dial failures)
// — not agent logic errors (bad tool args, empty replies, etc.).
//
// Precedence is auth > rate > timeout > unreachable: the first class
// whose substring matches wins. Matching is case-insensitive substring
// on err.Error(). A nil error yields "".
const (
	ClassRateLimited = "rate_limited"
	ClassTimeout     = "timeout"
	ClassUnreachable = "unreachable"
	ClassAuthFailed  = "auth_failed"
	ClassUnknown     = "unknown"
)

// Classify returns the error class for err, or "" when err is nil.
// Anything without a recognized signature is ClassUnknown.
func Classify(err error) string {
	if err == nil {
		return ""
	}
	s := strings.ToLower(err.Error())

	// Auth first: must fail fast, never retried as transient.
	if strings.Contains(s, "401") ||
		strings.Contains(s, "unauthorized") ||
		strings.Contains(s, "invalid api key") ||
		strings.Contains(s, "incorrect api key") ||
		strings.Contains(s, "incorrect key") ||
		strings.Contains(s, "auth_failed") ||
		strings.Contains(s, "auth failed") ||
		(strings.Contains(s, "no ") && strings.Contains(s, "api key")) {
		return ClassAuthFailed
	}

	// Rate limiting: 429 / quota language / Retry-After. The bare
	// "rate" match is intentionally broad per spec (covers rate limit,
	// rate_limit, rate-limited).
	if strings.Contains(s, "429") ||
		strings.Contains(s, "too many requests") ||
		strings.Contains(s, "retry-after") ||
		strings.Contains(s, "rate") {
		return ClassRateLimited
	}

	// Timeouts: deadlines and i/o timeouts.
	if strings.Contains(s, "deadline") ||
		strings.Contains(s, "timed out") ||
		strings.Contains(s, "timeout") {
		return ClassTimeout
	}

	// Unreachable: dial/DNS/network failures. Deliberately specific —
	// a bare "EOF" is too broad (also surfaces on truncated bodies),
	// so only connection-scoped signatures count.
	for _, sub := range []string{
		"connection refused",
		"connection reset",
		"connection failed",
		"reset by peer",
		"no such host",
		"unknown host",
		"network unreachable",
		"network is unreachable",
		"no route to host",
		"dial tcp",
		"proxyconnect",
	} {
		if strings.Contains(s, sub) {
			return ClassUnreachable
		}
	}

	return ClassUnknown
}
