// Package scrub — shared secret-redaction pattern set.
//
// Single source of truth for secret scrubbing, imported by internal/tools
// (thin wrappers), internal/hooks, internal/session, and internal/audit.
// It lives in its own leaf package because internal/tools imports
// internal/hooks (registry.go), so hooks cannot import tools without an
// import cycle; a leaf imported by all is cycle-free (stdlib only).
package scrub

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	reScrubPEM             = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	reScrubAnthropic       = regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`)
	reScrubOpenRouter      = regexp.MustCompile(`sk-or-[A-Za-z0-9_-]{8,}`)
	reScrubOpenAIProj      = regexp.MustCompile(`sk-proj-[A-Za-z0-9_-]{20,}`)
	reScrubOpenAI          = regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`)
	reScrubStripeLive      = regexp.MustCompile(`sk_live_[A-Za-z0-9]{8,}`)
	reScrubXAI             = regexp.MustCompile(`xai-[A-Za-z0-9_-]{20,}`)
	reScrubHuggingFace     = regexp.MustCompile(`hf_[A-Za-z0-9]{20,}`)
	reScrubAWS             = regexp.MustCompile(`(?:AKIA|ASIA)[0-9A-Z]{16}`)
	reScrubAWSSecretAssign = regexp.MustCompile(`(?i)(aws_secret_access_key["']?\s*[:=]\s*["']?)[A-Za-z0-9/+=]{40}`)
	// reScrubAWSSecret matches a bare 40-char base64-ish blob. It is only
	// applied when an AWS access-key ID (AKIA/ASIA…) is present in the same
	// string (see Scrub): an unanchored 40-char class also matches SHA-1
	// hex, base64 chunks, and other benign tokens, so gating on adjacency
	// trades a small false-negative risk (lone secret without its key ID)
	// for avoiding bulk false positives.
	reScrubAWSSecret      = regexp.MustCompile(`[A-Za-z0-9/+=]{40}`)
	reScrubGitHub         = regexp.MustCompile(`(?:gh[pousr]_|gho_|ghu_|ghs_|ghr_|github_pat_)[A-Za-z0-9_]{20,}`)
	reScrubGitLab         = regexp.MustCompile(`glpat-[A-Za-z0-9_-]{20,}`)
	reScrubSlack          = regexp.MustCompile(`(?:xox[a-z]|xapp)-[A-Za-z0-9-]+`)
	reScrubReplicate      = regexp.MustCompile(`rk_live_[A-Za-z0-9_-]{8,}`)
	reScrubNPM            = regexp.MustCompile(`npm_[A-Za-z0-9_]{8,}`)
	reScrubNpmAuth        = regexp.MustCompile(`(?i)(_authtoken\s*[:=]\s*["']?)[A-Za-z0-9_.\-/+=]{8,}`)
	reScrubPyPI           = regexp.MustCompile(`pypi-[A-Za-z0-9_.\-]{8,}`)
	reScrubGoogle         = regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`)
	reScrubBearer         = regexp.MustCompile(`Bearer\s+[A-Za-z0-9_.~+/=-]+`)
	reScrubJWT            = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
	reScrubOpenCodeAssign = regexp.MustCompile(`(?i)(opencode_api_key["']?\s*[:=]\s*["']?)[A-Za-z0-9_.\-/+=]{8,}`)
	reScrubAPIKeyAssign   = regexp.MustCompile(`(?i)(["']?(?:api[_-]?key|api_secret|authorization)["']?\s*[:=]\s*["']?)[A-Za-z0-9_.\-/+=]{8,}`)
	// reScrubSecretAssign catches password/secret-style assignments
	// (env files, JSON, YAML, KEY=value) that no prefixed-token rule
	// covers: DB_PASSWORD=…, "client_secret": "…". Deliberately
	// [: =]-joined only — a bare word followed by prose ("the secret
	// sauce") must not redact.
	reScrubSecretAssign = regexp.MustCompile(`(?i)(["']?(?:password|passwd|pwd|client_secret|access_token|secret_token|private_key)["']?\s*[:=]\s*["']?)[A-Za-z0-9_.\-/+=]{4,}`)
	reScrubQueryParam   = regexp.MustCompile(`(?i)([?&](?:key|token|secret|password|access_token|refresh_token|id_token|client_secret|api_key|apikey|auth|authorization|session|sig|signature|code)=)[^&#\s]*`)
	// reScrubVercel matches Vercel deployment tokens (vcp_ prefix).
	reScrubVercel = regexp.MustCompile(`vcp_[A-Za-z0-9]{40,}`)
	// reScrubGCPPrivateKey matches the private_key field of a GCP service
	// account JSON file. The value is redacted in place — unlike a
	// whole-document rule, reading the file still yields every other
	// field, so the output stays usable.
	reScrubGCPPrivateKey = regexp.MustCompile(`(?i)("private_key"\s*:\s*")[^"]*(")`)
	// reScrubURLCreds matches user:password@host patterns in URLs.
	reScrubURLCreds = regexp.MustCompile(`(?i)(?:https?|ftp|ssh)://[A-Za-z0-9_.\-]+:[^@\s]+@`)
)

// Scrub redacts secrets in s, returning the cleaned string and the number
// of redactions. Order matters: prefixed variants (sk-proj-) run before
// the generic sk- rule so they keep their distinct marker.
func Scrub(s string) (string, int) {
	n := 0
	replace := func(re *regexp.Regexp, repl string) {
		if m := re.FindAllStringIndex(s, -1); len(m) > 0 {
			n += len(m)
			s = re.ReplaceAllString(s, repl)
		}
	}
	replace(reScrubPEM, "<<REDACTED:pem>>")
	replace(reScrubAnthropic, "<<REDACTED:anthropic>>")
	replace(reScrubOpenRouter, "<<REDACTED:openrouter>>")
	replace(reScrubOpenAIProj, "<<REDACTED:openai-proj>>")
	replace(reScrubOpenAI, "<<REDACTED:openai>>")
	replace(reScrubStripeLive, "<<REDACTED:stripe>>")
	replace(reScrubXAI, "<<REDACTED:xai>>")
	replace(reScrubHuggingFace, "<<REDACTED:huggingface>>")
	hasAWS := reScrubAWS.MatchString(s)
	replace(reScrubAWS, "<<REDACTED:aws>>")
	replace(reScrubAWSSecretAssign, `$1<<REDACTED:aws-secret>>`)
	if hasAWS {
		replace(reScrubAWSSecret, "<<REDACTED:aws-secret>>")
	}
	replace(reScrubGitHub, "<<REDACTED:github>>")
	replace(reScrubGitLab, "<<REDACTED:gitlab>>")
	replace(reScrubSlack, "<<REDACTED:slack>>")
	replace(reScrubReplicate, "<<REDACTED:replicate>>")
	replace(reScrubNPM, "<<REDACTED:npm>>")
	replace(reScrubNpmAuth, `$1<<REDACTED:npm>>`)
	replace(reScrubPyPI, "<<REDACTED:pypi>>")
	replace(reScrubGoogle, "<<REDACTED:google>>")
	replace(reScrubBearer, "Bearer <<REDACTED:bearer>>")
	replace(reScrubJWT, "<<REDACTED:jwt>>")
	replace(reScrubOpenCodeAssign, `$1<<REDACTED:opencode>>`)
	replace(reScrubAPIKeyAssign, `$1<<REDACTED:apikey>>`)
	replace(reScrubQueryParam, `$1<<REDACTED:query>>`)
	// After the query rule: in a URL the query marker wins (no double
	// redaction); bare KEY=value assignments still fall through to here.
	replace(reScrubSecretAssign, `$1<<REDACTED:secret>>`)
	replace(reScrubVercel, "<<REDACTED:vercel>>")
	replace(reScrubGCPPrivateKey, `$1<<REDACTED:gcp-private-key>>$2`)
	replace(reScrubURLCreds, "://<<REDACTED:url-creds>>@")
	if home := os.Getenv("HOME"); home != "" {
		if c := strings.Count(s, home); c > 0 {
			n += c
			s = strings.ReplaceAll(s, home, "~")
		}
	}
	if c := strings.Count(s, "$HOME"); c > 0 {
		n += c
		s = strings.ReplaceAll(s, "$HOME", "~")
	}
	return s, n
}

func IsHighRiskPath(p string) bool {
	low := strings.ToLower(filepath.ToSlash(p))
	base := strings.ToLower(filepath.Base(low))
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	if strings.HasSuffix(low, ".aws/credentials") || strings.HasSuffix(low, ".aws/config") {
		return true
	}
	if base == "credentials.json" || base == ".netrc" || base == ".npmrc" || base == ".pypirc" || base == ".git-credentials" {
		return true
	}
	if strings.HasPrefix(base, "id_rsa") {
		return true
	}
	if strings.Contains(low, ".ssh/") && strings.HasPrefix(base, "id_") {
		return true
	}
	if strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") {
		return true
	}
	return strings.Contains(base, "secret") || strings.Contains(base, "token")
}

func AnnotateHighRisk(p string) string {
	if !IsHighRiskPath(p) {
		return ""
	}
	return "sensitive path " + p + ": contents may hold secrets — scrub output and avoid printing raw"
}
