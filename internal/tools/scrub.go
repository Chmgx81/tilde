package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	reScrubPEM        = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	reScrubAnthropic  = regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`)
	reScrubOpenAI     = regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`)
	reScrubXAI        = regexp.MustCompile(`xai-[A-Za-z0-9_-]{20,}`)
	reScrubAWS        = regexp.MustCompile(`(?:AKIA|ASIA)[0-9A-Z]{16}`)
	reScrubGitHub     = regexp.MustCompile(`(?:gh[pousr]_|github_pat_)[A-Za-z0-9_]{20,}`)
	reScrubGitLab     = regexp.MustCompile(`glpat-[A-Za-z0-9_-]{20,}`)
	reScrubSlack      = regexp.MustCompile(`(?:xox[abps]|xapp)-[A-Za-z0-9-]+`)
	reScrubGoogle     = regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`)
	reScrubBearer     = regexp.MustCompile(`Bearer\s+[A-Za-z0-9_.~+/=-]+`)
	reScrubJWT        = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
	reScrubQueryParam = regexp.MustCompile(`(?i)([?&](?:key|token|secret|password|access_token|refresh_token|id_token|client_secret|api_key|apikey|auth|authorization|session|sig|signature|code)=)[^&#\s]*`)
)

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
	replace(reScrubOpenAI, "<<REDACTED:openai>>")
	replace(reScrubXAI, "<<REDACTED:xai>>")
	replace(reScrubAWS, "<<REDACTED:aws>>")
	replace(reScrubGitHub, "<<REDACTED:github>>")
	replace(reScrubGitLab, "<<REDACTED:gitlab>>")
	replace(reScrubSlack, "<<REDACTED:slack>>")
	replace(reScrubGoogle, "<<REDACTED:google>>")
	replace(reScrubBearer, "Bearer <<REDACTED:bearer>>")
	replace(reScrubJWT, "<<REDACTED:jwt>>")
	replace(reScrubQueryParam, `$1<<REDACTED:query>>`)
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
	if strings.HasSuffix(low, ".aws/credentials") {
		return true
	}
	if base == "credentials.json" || base == ".netrc" {
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
