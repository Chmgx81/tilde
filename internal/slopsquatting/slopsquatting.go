// Package slopsquatting — defenses against package-name hallucination attacks.
//
// "Slopsquatting" is when an attacker pre-registers the plausible-but-fake
// package names that LLMs most often hallucinate (e.g. `langchin` for
// `langchain`, `reque-sts` for `requests`). The model suggests `pip install
// langchin`; the user approves; the host runs whatever the attacker published.
//
// This package is the detection half: it flags likely-hallucinated names at
// the policy layer (see policy.go isInstallShape and the enhanced Ask prompt
// in Describe). The user confirms — never auto-approves — any install of a
// flagged name. Nothing here blocks; it makes the decision visible.
package slopsquatting

import (
	"strings"
)

// hallucinations maps names LLMs commonly invent to the real package they
// probably mean. Attackers pre-register these invented names, so flagging
// them before install is the defense.
//
// The map is deliberately conservative: only names with no legitimate
// package behind them. Real-but-related packages (torchvision, material-ui,
// lo-dash) are NOT listed — flagging a real package trains users to ignore
// the warning. Lowercase for case-insensitive matching.
var hallucinations = map[string]string{
	// Python (PyPI)
	"langchin":       "langchain",
	"reque-sts":      "requests",
	"requsts":        "requests",
	"reqeusts":       "requests",
	"num-py":         "numpy",
	"numpyy":         "numpy",
	"pandys":         "pandas",
	"pandahs":        "pandas",
	"sklearrn":       "scikit-learn",
	"tensorflwo":     "tensorflow",
	"py-test":        "pytest",
	"py-testing":     "pytest",
	"virtualnv":      "virtualenv",
	"beautifulsoupp": "beautifulsoup4",
	"selnium":        "selenium",
	"seleniun":       "selenium",
	"flsak":          "flask",
	"djnago":         "django",
	"sqlalchmy":      "sqlalchemy",
	"sqlalchmey":     "sqlalchemy",
	"open-cv":        "opencv-python",
	"matplotib":      "matplotlib",
	"matplolib":      "matplotlib",
	"transfomers":    "transformers",
	"transformes":    "transformers",

	// JavaScript/Node (npm)
	"axio":          "axios",
	"axois":         "axios",
	"lodahs":        "lodash",
	"moemnt":        "moment",
	"reac":          "react",
	"reacte":        "react",
	"typescipt":     "typescript",
	"typescrit":     "typescript",
	"vite-js":       "vite",
	"tailwind-css":  "tailwindcss",
	"antd-design":   "antd",
	"storybook-js":  "storybook",
	"eslint-config": "eslint",

	// Rust (crates.io)
	"serd":         "serde",
	"tokio-rs":     "tokio",
	"futures-rs":   "futures",
	"reqwst":       "reqwest",
	"hype":         "hyper",
	"actik-web":    "actix-web",
	"rand-rs":      "rand",
	"regex-rs":     "regex",
	"clap-rs":      "clap",
	"anyhow-rs":    "anyhow",
	"thiserror-rs": "thiserror",

	// Go (module paths that don't exist as written)
	"gorilla-mux":    "github.com/gorilla/mux",
	"gin-gonic":      "github.com/gin-gonic/gin",
	"echo-framework": "github.com/labstack/echo",
}

// LikelyHallucinated reports whether a package name is a known LLM
// hallucination and, if so, the real package it probably maps to.
// Comparison is case-insensitive.
func LikelyHallucinated(name string) (bool, string) {
	// Strip common prefixes/suffixes for matching.
	clean := strings.ToLower(strings.TrimSpace(name))
	clean = strings.TrimPrefix(clean, "@")
	clean = strings.TrimSpace(clean)

	// Direct match.
	if real, ok := hallucinations[clean]; ok {
		return true, real
	}

	// Try with common separators stripped.
	nosep := strings.ReplaceAll(clean, "-", "")
	nosep = strings.ReplaceAll(nosep, "_", "")
	if real, ok := hallucinations[nosep]; ok {
		return true, real
	}

	// Strip scope prefix (@scope/pkg → pkg).
	if idx := strings.Index(clean, "/"); idx > 0 {
		base := clean[idx+1:]
		if real, ok := hallucinations[base]; ok {
			return true, real
		}
	}

	return false, ""
}

// wellKnownPackages is the set of legitimate package names. A name that
// matches this set exactly (case- and separator-insensitive) is not a typo
// — skip it before computing edit distances so real packages aren't
// flagged as typos of other real packages (e.g. `requests` is close to
// `reqwest`). The set covers the ecosystems the DB above reasons about;
// it does not need to be exhaustive, only to protect names near a listed
// one from false "possible typo" warnings.
var wellKnownPackages = map[string]bool{
	// Python / PyPI
	"langchain": true, "requests": true, "numpy": true, "pandas": true,
	"tensorflow": true, "torch": true, "flask": true, "django": true,
	"fastapi": true, "selenium": true, "pytest": true, "matplotlib": true,
	"scipy": true, "transformers": true, "sqlalchemy": true, "pydantic": true,
	"uvicorn": true, "httpx": true, "aiohttp": true, "celery": true,
	"redis": true, "pymongo": true, "psycopg2": true, "tqdm": true,
	"rich": true, "typer": true, "paramiko": true, "boto3": true,
	"scikit-learn": true, "opencv-python": true, "beautifulsoup4": true,
	// JavaScript / npm
	"axios": true, "lodash": true, "react": true, "next": true, "vue": true,
	"angular": true, "svelte": true, "vite": true, "webpack": true,
	"rollup": true, "babel": true, "tailwindcss": true, "typescript": true,
	"jest": true, "vitest": true, "eslint": true, "prettier": true,
	"express": true, "mongoose": true, "dotenv": true, "jsonwebtoken": true,
	"passport": true, "redux": true, "zustand": true, "playwright": true,
	"puppeteer": true, "socket.io": true,
	// Rust / crates.io
	"serde": true, "tokio": true, "reqwest": true, "clap": true,
	"rand": true, "regex": true, "rayon": true, "tracing": true,
	"anyhow": true, "thiserror": true, "hyper": true, "actix-web": true,
	"sqlx": true,
	// Go modules
	"gin": true, "gorm": true, "cobra": true, "viper": true, "logrus": true,
	"zap": true, "testify": true, "gorilla": true,
}

// normalizePkg lowercases, trims, drops a scope prefix, and strips
// hyphen/underscore separators, so `@scope/Lang-Chin` and `lang_chin`
// compare on equal footing.
func normalizePkg(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	if idx := strings.Index(s, "/"); idx >= 0 && idx+1 < len(s) {
		s = s[idx+1:]
	}
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, "_", "")
	s = strings.ReplaceAll(s, ".", "")
	return s
}

// isWellKnown reports whether a name (in any spelling) is a known package.
func isWellKnown(lower string) bool {
	if wellKnownPackages[lower] {
		return true
	}
	n := normalizePkg(lower)
	for k := range wellKnownPackages {
		if normalizePkg(k) == n {
			return true
		}
	}
	return false
}

// KnownPackage reports whether name is a legitimate well-known package.
// Callers use it to avoid wording a known-good install as "unverified".
func KnownPackage(name string) bool {
	return isWellKnown(strings.ToLower(strings.TrimSpace(name)))
}

// IsCommonTypo reports whether a name is a plausible misspelling of a
// well-known package — a weak signal on its own, but combined with
// Ask-tier approval it gives the user a second chance. Comparison is
// case- and separator-insensitive. Short names (under five characters)
// only flag at distance 1, which keeps the warning precise. Names that
// are themselves well-known packages return false.
func IsCommonTypo(name string) bool {
	lower := normalizePkg(name)
	if len(lower) < 3 {
		return false
	}
	if isWellKnown(strings.ToLower(strings.TrimSpace(name))) {
		return false
	}
	maxD := 2
	if len(lower) < 5 {
		maxD = 1
	}
	for k := range wellKnownPackages {
		d := levenshtein(lower, normalizePkg(k))
		if d >= 1 && d <= maxD {
			return true
		}
	}
	return false
}

// levenshtein computes the edit distance between s and t. Short and
// allocation-light — fine for package-name lengths.
func levenshtein(s, t string) int {
	if len(s) == 0 {
		return len(t)
	}
	if len(t) == 0 {
		return len(s)
	}
	// Use a single-row DP to keep it O(min) space.
	prev := make([]int, len(t)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(s); i++ {
		cur := make([]int, len(t)+1)
		cur[0] = i
		for j := 1; j <= len(t); j++ {
			cost := 1
			if s[i-1] == t[j-1] {
				cost = 0
			}
			cur[j] = min(
				cur[j-1]+1,     // insert
				prev[j]+1,      // delete
				prev[j-1]+cost, // substitute
			)
		}
		prev = cur
	}
	return prev[len(t)]
}
