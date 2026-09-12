package main

import (
	"strings"
	"testing"
)

func TestParseDeployArgs(t *testing.T) {
	cases := []struct {
		name       string
		in         []string
		wantTarget string
		wantProd   bool
		wantErr    bool
	}{
		{"empty defaults to vercel", nil, "", false, false},
		{"explicit target", []string{"vercel"}, "vercel", false, false},
		{"prod flag", []string{"vercel", "--prod"}, "vercel", true, false},
		{"short prod", []string{"-p"}, "", true, false},
		{"preview resets prod", []string{"--prod", "--preview"}, "", false, false},
		{"case-insensitive target", []string{"Vercel"}, "vercel", false, false},
		{"unknown flag", []string{"--nope"}, "", false, true},
		{"flag-like target refused", []string{"-evil"}, "", false, true},
		{"extra positional", []string{"vercel", "extra"}, "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target, prod, err := parseDeployArgs(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseDeployArgs(%v) = (%q,%v), want error", tc.in, target, prod)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDeployArgs(%v) unexpected error: %v", tc.in, err)
			}
			if target != tc.wantTarget || prod != tc.wantProd {
				t.Fatalf("parseDeployArgs(%v) = (%q,%v), want (%q,%v)", tc.in, target, prod, tc.wantTarget, tc.wantProd)
			}
		})
	}
}

func TestVercelDeployArgs(t *testing.T) {
	preview := vercelDeployArgs(false)
	if strings.Join(preview, " ") != "deploy --yes" {
		t.Fatalf("preview args = %v", preview)
	}
	prod := vercelDeployArgs(true)
	if strings.Join(prod, " ") != "deploy --prod --yes" {
		t.Fatalf("prod args = %v", prod)
	}
}

func TestResolveVercelToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VERCEL_TOKEN", "")
	store, err := openCredStore()
	if err != nil {
		t.Fatal(err)
	}

	// Nothing anywhere.
	if tok, src := resolveVercelToken(store); tok != "" || src != "" {
		t.Fatalf("empty resolution = (%q,%q)", tok, src)
	}
	// Stored wins when no env.
	if err := store.Set("vercel", "stored-token"); err != nil {
		t.Fatal(err)
	}
	if tok, src := resolveVercelToken(store); tok != "stored-token" || src != "stored" {
		t.Fatalf("stored resolution = (%q,%q)", tok, src)
	}
	// Env outranks the store.
	t.Setenv("VERCEL_TOKEN", "env-token")
	if tok, src := resolveVercelToken(store); tok != "env-token" || src != "env" {
		t.Fatalf("env resolution = (%q,%q)", tok, src)
	}
	// Nil store falls back to env only.
	if tok, _ := resolveVercelToken(nil); tok != "env-token" {
		t.Fatalf("nil-store resolution = %q", tok)
	}
}

func TestDeployVercelNoTokenNamesFix(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("VERCEL_TOKEN", "")
	err := deployVercel(false)
	if err == nil {
		t.Fatal("deploy without a token must fail loud")
	}
	if !strings.Contains(err.Error(), "tilde login vercel") {
		t.Fatalf("error must name the fix, got: %v", err)
	}
}

// TestDeployTargetUnknown verifies an unknown target is rejected before any
// token or process work happens.
func TestDeployTargetUnknown(t *testing.T) {
	if err := runDeployCmd([]string{"deploy", "netlify"}); err == nil {
		t.Fatal("unknown target must fail loud")
	}
}

func TestDeploymentURL(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{"empty", "", ""},
		{"plain", "https://proj-abc123.vercel.app\n", "https://proj-abc123.vercel.app"},
		{"last wins", "https://first.vercel.app\nhttps://second.vercel.app\n", "https://second.vercel.app"},
		{"annoys trailing text", "Inspect: https://vercel.com/x\nhttps://proj.vercel.app\n", "https://proj.vercel.app"},
		{"inline url not a line", "Deployed to https://x.vercel.app successfully\n", ""},
		{"http ok", "http://localhost:3000\n", "http://localhost:3000"},
		{"no url", "Success! Built in 12s\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deploymentURL(tc.out); got != tc.want {
				t.Fatalf("deploymentURL(%q) = %q, want %q", tc.out, got, tc.want)
			}
		})
	}
}
