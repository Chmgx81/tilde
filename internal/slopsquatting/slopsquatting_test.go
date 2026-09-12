package slopsquatting

import "testing"

func TestLikelyHallucinated(t *testing.T) {
	tests := []struct {
		name      string
		wantMatch bool
		wantReal  string
	}{
		// Python hallucinations
		{"langchin", true, "langchain"},
		{"reque-sts", true, "requests"},
		{"numpyy", true, "numpy"},
		{"tensorflwo", true, "tensorflow"},
		{"sklearrn", true, "scikit-learn"},
		{"matplotib", true, "matplotlib"},
		// npm hallucinations
		{"axio", true, "axios"},
		{"lodahs", true, "lodash"},
		{"typescipt", true, "typescript"},
		// Rust hallucinations
		{"serd", true, "serde"},
		{"reqwst", true, "reqwest"},
		// Real packages — should NOT match
		{"langchain", false, ""},
		{"requests", false, ""},
		{"numpy", false, ""},
		{"react", false, ""},
		{"serde", false, ""},
		// Scope prefix
		{"@scope/pkg", false, ""},
		// Empty
		{"", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMatch, gotReal := LikelyHallucinated(tt.name)
			if gotMatch != tt.wantMatch {
				t.Errorf("LikelyHallucinated(%q) match = %v, want %v", tt.name, gotMatch, tt.wantMatch)
			}
			if tt.wantMatch && gotReal != tt.wantReal {
				t.Errorf("LikelyHallucinated(%q) real = %q, want %q", tt.name, gotReal, tt.wantReal)
			}
		})
	}
}

func TestIsCommonTypo(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		// Close to real packages (edit distance 1-2)
		{"langchin", true}, // langchain (1 deletion)
		{"requsts", true},  // requests (1 deletion)
		{"numpi", true},    // numpy (1 substitution)
		{"pandaz", true},   // pandas (1 substitution)
		{"floack", true},   // flask (2 edits)
		// Too far from any known package
		{"xyzabc", false},
		{"totally-fake", false},
		// Real packages — exact match, not a typo
		{"langchain", false},
		{"requests", false},
		{"numpy", false},
		{"flask", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCommonTypo(tt.name); got != tt.want {
				t.Errorf("IsCommonTypo(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestLevenshtein(t *testing.T) {
	tests := []struct {
		s, t string
		want int
	}{
		{"", "", 0},
		{"a", "", 1},
		{"", "a", 1},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"abc", "a", 2},
		{"kitten", "sitting", 3},
		{"langchain", "langchin", 1},
		{"requests", "requsts", 1},
	}
	for _, tt := range tests {
		got := levenshtein(tt.s, tt.t)
		if got != tt.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tt.s, tt.t, got, tt.want)
		}
	}
}
