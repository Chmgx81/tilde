package scrub

import (
	"strings"
	"testing"
)

func TestScrubPatterns(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  string
		count int
	}{
		{"openai", "key=sk-abcdefghij1234567890XY", "key=<<REDACTED:openai>>", 1},
		{"anthropic", "key=sk-ant-api03-abcdefghij1234567890XY", "key=<<REDACTED:anthropic>>", 1},
		{"xai", "key=xai-abcdefghij1234567890XY", "key=<<REDACTED:xai>>", 1},
		{"awsakia", "key=AKIAIOSFODNN7EXAMPLE", "key=<<REDACTED:aws>>", 1},
		{"awsasia", "key=ASIAIOSFODNN7EXAMPLE", "key=<<REDACTED:aws>>", 1},
		{"github", "tok=ghp_abcdefghij1234567890ABCDEFGH", "tok=<<REDACTED:github>>", 1},
		{"githubpat", "tok=github_pat_abcdefghij1234567890ABCDEFGH", "tok=<<REDACTED:github>>", 1},
		{"gitlab", "tok=glpat-abcdefghij1234567890", "tok=<<REDACTED:gitlab>>", 1},
		{"slack", "tok=xoxb-123456789012-abcdefghij", "tok=<<REDACTED:slack>>", 1},
		{"slackapp", "tok=xapp-1-abcdefghij123456", "tok=<<REDACTED:slack>>", 1},
		{"google", "key=AIzaSyBabcdefghij1234567890ABCDEFGH1234", "key=<<REDACTED:google>>", 1},
		{"pem", "k=-----BEGIN RSA PRIVATE KEY-----\nMIIEfake\n-----END RSA PRIVATE KEY-----", "k=<<REDACTED:pem>>", 1},
		{"bearer", "Authorization: Bearer abcDEF123._~+-xyz", "Authorization: Bearer <<REDACTED:bearer>>", 1},
		{"jwt", "tok=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJVadQssw5c", "tok=<<REDACTED:jwt>>", 1},
		{"querycode", "https://example.com/cb?code=abc123&state=xyz", "https://example.com/cb?code=<<REDACTED:query>>&state=xyz", 1},
		{"querytoken", "https://example.com/?access_token=sekret&next=/", "https://example.com/?access_token=<<REDACTED:query>>&next=/", 1},
		{"multi", "a sk-abcdefghij1234567890XY b ghp_abcdefghij1234567890ABCDEFGH", "a <<REDACTED:openai>> b <<REDACTED:github>>", 2},
		{"clean", "hello world, nothing secret here", "hello world, nothing secret here", 0},
		{"cleanhome", "see /home/otheruser/docs for details", "see /home/otheruser/docs for details", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, n := Scrub(tc.in)
			if n != tc.count {
				t.Errorf("count = %d, want %d", n, tc.count)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestScrubHome(t *testing.T) {
	t.Setenv("HOME", "/home/testuser")
	got, n := Scrub("read /home/testuser/docs notes")
	if got != "read ~/docs notes" {
		t.Errorf("got %q, want %q", got, "read ~/docs notes")
	}
	if n != 1 {
		t.Errorf("count = %d, want 1", n)
	}
	got, n = Scrub("read $HOME/docs notes")
	if got != "read ~/docs notes" {
		t.Errorf("got %q, want %q", got, "read ~/docs notes")
	}
	if n != 1 {
		t.Errorf("count = %d, want 1", n)
	}
}

func TestScrubHighRiskPaths(t *testing.T) {
	risky := []string{
		".env",
		"proj/.env.local",
		"/home/u/.aws/credentials",
		"config/credentials.json",
		"/home/u/.netrc",
		"/home/u/.ssh/id_rsa",
		"/home/u/.ssh/id_ed25519",
		"certs/server.pem",
		"certs/server.key",
		"my_secret.yaml",
		"tokens.txt",
	}
	for _, p := range risky {
		if !IsHighRiskPath(p) {
			t.Errorf("IsHighRiskPath(%q) = false, want true", p)
		}
		if a := AnnotateHighRisk(p); a == "" || strings.Contains(a, "\n") {
			t.Errorf("AnnotateHighRisk(%q) = %q, want one non-empty line", p, a)
		}
	}
}

func TestScrubNewPatterns(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		check string
		leak  string
	}{
		{"openrouter", "key=sk-or-v1-abcdefghij1234567890", "<<REDACTED:openrouter>>", "sk-or-v1-abcdefghij1234567890"},
		{"opencode", "OPENCODE_API_KEY=abcDEF123-_4567890", "<<REDACTED:opencode>>", "abcDEF123-_4567890"},
		{"opencodejson", `{"opencode_api_key": "abcDEF123-_4567890"}`, "<<REDACTED:opencode>>", "abcDEF123-_4567890"},
		{"apikeyassign", "api_key=mysecretvalue123", "<<REDACTED:apikey>>", "mysecretvalue123"},
		{"apikeydash", "api-key=mysecretvalue123", "<<REDACTED:apikey>>", "mysecretvalue123"},
		{"apiauth", "authorization=mytokenvalue12345", "<<REDACTED:apikey>>", "mytokenvalue12345"},
		{"awssecret", "AKIAIOSFODNN7EXAMPLE wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "<<REDACTED:aws-secret>>", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		{"awssecretassign", "aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "<<REDACTED:aws-secret>>", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		{"xoxp", "tok=xoxp-123456789012-abcdefghij", "<<REDACTED:slack>>", "xoxp-123456789012-abcdefghij"},
		{"replicate", "tok=rk_live_abcdefghij1234567890", "<<REDACTED:replicate>>", "rk_live_abcdefghij1234567890"},
		{"npm", "tok=npm_abcdefghij1234567890XYZ", "<<REDACTED:npm>>", "npm_abcdefghij1234567890XYZ"},
		{"npmauth", "//registry.npmjs.org/:_authToken=npm_abcdefghij1234567890XYZ", "<<REDACTED:npm>>", "npm_abcdefghij1234567890XYZ"},
		{"pypi", "pwd=pypi-AgEIcHlwaS5vcmcCJHh4eHh4eHh4eA", "<<REDACTED:pypi>>", "pypi-AgEIcHlwaS5vcmcCJHh4eHh4eHh4eA"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, n := Scrub(tc.in)
			if n < 1 {
				t.Errorf("count = %d, want >=1", n)
			}
			if !strings.Contains(got, tc.check) {
				t.Errorf("got %q, want to contain %q", got, tc.check)
			}
			if strings.Contains(got, tc.leak) {
				t.Errorf("got %q, still contains secret %q", got, tc.leak)
			}
		})
	}
}

func TestScrubHighRiskPathsExtended(t *testing.T) {
	risky := []string{
		"/home/u/.npmrc",
		"proj/.pypirc",
		"/home/u/.git-credentials",
		"/home/u/.aws/config",
		"/home/u/.ssh/id_rsa",
		"/home/u/.ssh/id_rsa.pub",
		"id_rsa_backup",
	}
	for _, p := range risky {
		if !IsHighRiskPath(p) {
			t.Errorf("IsHighRiskPath(%q) = false, want true", p)
		}
	}
}
func TestScrubNormalPaths(t *testing.T) {
	safe := []string{
		"main.go",
		"README.md",
		"config.yaml",
		"internal/tools/registry.go",
		"docs/guide.md",
	}
	for _, p := range safe {
		if IsHighRiskPath(p) {
			t.Errorf("IsHighRiskPath(%q) = true, want false", p)
		}
		if a := AnnotateHighRisk(p); a != "" {
			t.Errorf("AnnotateHighRisk(%q) = %q, want empty", p, a)
		}
	}
}
