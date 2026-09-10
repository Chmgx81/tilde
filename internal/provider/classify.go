package provider

import "strings"

// Error classes for transport/provider failures (Spec §11).
//
// Classify maps raw provider errors into a small stable set consumed by
// UI/exit codes, so callers never surface raw dumps. It classifies
// transport/provider errors (HTTP statuses, timeouts, dial failures)
// — not agent logic errors (bad tool args, empty replies, etc.).
//
// Precedence is cancelled > auth > rate > timeout > unreachable: the first class
// whose substring matches wins. Matching is case-insensitive substring
// on err.Error(). A nil error yields "".
const (
	ClassRateLimited = "rate_limited"
	ClassTimeout     = "timeout"
	ClassUnreachable = "unreachable"
	ClassAuthFailed  = "auth_failed"
	ClassCancelled   = "cancelled"
	ClassUnknown     = "unknown"
)

// Classify returns the error class for err, or "" when err is nil.
// Anything without a recognized signature is ClassUnknown.
func Classify(err error) string {
	if err == nil {
		return ""
	}
	s := strings.ToLower(err.Error())

	// Cancellation first: a user- or timeout-cancelled turn is neither
	// auth, rate, timeout, nor unreachable — and must never read as
	// "unknown". (Deadline-exceeded stays a timeout; see below.)
	if strings.Contains(s, "context canceled") ||
		strings.Contains(s, "context cancelled") {
		return ClassCancelled
	}

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

	// Rate limiting: 429 / quota language / Retry-After. Matched on
	// word-boundary phrases — a bare "rate" substring misfires on
	// ordinary words (generate, separate, operate, moderate, ...), which
	// used to misclassify errors like "failed to generate response" and
	// drive wrong retry/backoff behavior.
	if strings.Contains(s, "429") ||
		strings.Contains(s, "too many requests") ||
		strings.Contains(s, "retry-after") ||
		strings.Contains(s, "rate limit") ||
		strings.Contains(s, "rate_limit") ||
		strings.Contains(s, "rate-limit") ||
		strings.Contains(s, "ratelimit") ||
		strings.Contains(s, "rate exceeded") ||
		strings.Contains(s, "over quota") ||
		strings.Contains(s, "quota exceeded") {
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
