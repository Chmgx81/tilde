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
// matches this set exactly is not a typo — skip it before computing
// edit distances so real packages aren't flagged as typos of other
// real packages (e.g. `requests` is 1 edit from `reqwest`).
var wellKnownPackages = map[string]bool{
	"langchain": true, "requests": true, "numpy": true, "pandas": true,
	"tensorflow": true, "torch": true, "flask": true, "django": true,
	"fastapi": true, "selenium": true, "pytest": true, "matplotlib": true,
	"scipy": true, "transformers": true, "axios": true, "lodash": true,
	"react": true, "next": true, "vite": true, "tailwindcss": true,
	"typescript": true, "jest": true, "eslint": true, "prettier": true,
	"serde": true, "tokio": true, "reqwest": true, "clap": true,
	"rand": true, "regex": true,
}

// IsCommonTypo reports whether a name differs by edit distance 1-2 from a
// well-known package — a weak signal on its own, but combined with
// Ask-tier approval it gives the user a second chance. Names that are
// themselves well-known packages return false (correct spelling, not a typo).
func IsCommonTypo(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	// Exact match to a known package — not a typo.
	if wellKnownPackages[lower] {
		return false
	}
	for k := range wellKnownPackages {
		d := levenshtein(lower, k)
		if d >= 1 && d <= 2 {
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

func min(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
