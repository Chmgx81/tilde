package provider

import (
	"errors"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		// auth_failed variants.
		{name: "auth 401 status", err: errors.New("openai: status 401 for model \"gpt-4\""), want: ClassAuthFailed},
		{name: "auth unauthorized lower", err: errors.New("unauthorized: bad credentials"), want: ClassAuthFailed},
		{name: "auth unauthorized upper", err: errors.New("UNAUTHORIZED"), want: ClassAuthFailed},
		{name: "auth invalid api key", err: errors.New("Invalid API Key provided"), want: ClassAuthFailed},
		{name: "auth incorrect key", err: errors.New("Incorrect API key supplied"), want: ClassAuthFailed},
		{name: "auth auth_failed marker", err: errors.New("auth_failed: key rejected"), want: ClassAuthFailed},
		{name: "auth no api key", err: errors.New("NO valid API KEY in config"), want: ClassAuthFailed},

		// rate_limited variants.
		{name: "rate 429", err: errors.New("openai: status 429: slow down"), want: ClassRateLimited},
		{name: "rate limit words", err: errors.New("rate limit exceeded, slow down"), want: ClassRateLimited},
		{name: "rate underscore variant", err: errors.New("RATE_LIMIT hit"), want: ClassRateLimited},
		{name: "rate hyphen variant", err: errors.New("Rate-Limited by upstream"), want: ClassRateLimited},
		{name: "rate too many requests", err: errors.New("Too Many Requests"), want: ClassRateLimited},
		{name: "rate retry-after header", err: errors.New("Retry-After: 30"), want: ClassRateLimited},

		// timeout variants.
		{name: "timeout deadline", err: errors.New("context deadline exceeded"), want: ClassTimeout},
		{name: "timeout io", err: errors.New("Post https://api.example.com: i/o timeout"), want: ClassTimeout},
		{name: "timeout timed out upper", err: errors.New("Request TIMED OUT after 30s"), want: ClassTimeout},

		// unreachable variants.
		{name: "unreachable refused", err: errors.New("dial tcp 127.0.0.1:11434: connection refused"), want: ClassUnreachable},
		{name: "unreachable reset", err: errors.New("read: connection reset by peer"), want: ClassUnreachable},
		{name: "unreachable reset upper", err: errors.New("CONNECTION RESET BY PEER"), want: ClassUnreachable},
		{name: "unreachable no such host", err: errors.New("dial tcp: no such host api.example.com"), want: ClassUnreachable},
		{name: "unreachable unknown host", err: errors.New("Unknown Host: api.example.com"), want: ClassUnreachable},
		{name: "unreachable network", err: errors.New("network is unreachable"), want: ClassUnreachable},
		{name: "unreachable no route", err: errors.New("no route to host"), want: ClassUnreachable},
		{name: "unreachable dial tcp", err: errors.New("dial TCP 10.0.0.1:443: connect failed"), want: ClassUnreachable},
		{name: "unreachable proxy", err: errors.New("proxyconnect tcp: connection refused"), want: ClassUnreachable},

		// precedence: auth wins over rate/timeout/unreachable signatures.
		{name: "precedence auth over rate", err: errors.New("401 rate limit? no — unauthorized"), want: ClassAuthFailed},
		{name: "precedence rate over timeout", err: errors.New("rate limit: timeout waiting"), want: ClassRateLimited},
		{name: "precedence timeout over unreachable", err: errors.New("timeout: connection refused while dialing"), want: ClassTimeout},

		// nil and unknown passthrough.
		{name: "nil", err: nil, want: ""},
		{name: "unknown passthrough", err: errors.New("model returned malformed JSON"), want: ClassUnknown},
		{name: "unknown bare eof", err: errors.New("unexpected EOF"), want: ClassUnknown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.err); got != tc.want {
				t.Fatalf("Classify(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}
