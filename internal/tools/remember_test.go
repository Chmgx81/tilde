package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tilde/internal/vec"
)

func rememberNoEnv(t *testing.T) {
	t.Helper()
	t.Setenv(EmbedModelEnv, "")
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rememberAlphaGo() string {
	return `package alpha

// Quaternion slerp on the hypersphere: spherical interpolation
// between two unit quaternions.
type Quaternion struct{ X, Y, Z, W float64 }

func SlerpQuaternion(a, b Quaternion, t float64) Quaternion {
	// hypersphere interpolation factor from the slerp parameter
	_ = a
	_ = b
	return Quaternion{W: 1 - t}
}
`
}

func rememberBetaGo() string {
	return `package beta

import "net/http"

// Retry middleware with exponential backoff for flaky upstreams.
func RetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// backoff then retry the upstream handler
		next.ServeHTTP(w, r)
	})
}
`
}

func TestRememberIndexRecallStatusRoundTrip(t *testing.T) {
	rememberNoEnv(t)
	root := t.TempDir()
	ctx := context.Background()
	writeFile(t, root, "pkg/alpha.go", rememberAlphaGo())
	writeFile(t, root, "pkg/beta.go", rememberBetaGo())
	// Own state dir must never pollute the index: 50 lines would chunk
	// into 2 windows if walked.
	var scratch strings.Builder
	for i := 0; i < 50; i++ {
		scratch.WriteString("scratch filler line for the state dir\n")
	}
	writeFile(t, root, ".tilde/scratch.md", scratch.String())

	r := &Remember{Root: root, Seen: NewSeenMap(root)}
	out, err := r.Exec(ctx, map[string]any{"op": "index"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2 chunk(s) from 2 file(s)") {
		t.Fatalf("want 2 chunks from 2 files (.tilde skipped), got %q", out)
	}
	if !strings.Contains(out, "backend tfidf") {
		t.Fatalf("default backend must be tfidf, got %q", out)
	}
	if fi, err := os.Stat(filepath.Join(root, ".tilde", "vectors.jsonl")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("store file must exist with 0600, got %v %v", fi, err)
	}

	out, err = r.Exec(ctx, map[string]any{"op": "recall", "query": "quaternion slerp hypersphere interpolation", "k": 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "alpha.go:1:") {
		t.Fatalf("relevant chunk must hit first, got %q", out)
	}
	if strings.Contains(out, "beta.go") {
		t.Fatalf("k=1 must return only the top hit, got %q", out)
	}
	if !strings.Contains(out, "begin untrusted output") {
		t.Fatalf("recall content must be fenced, got %q", out)
	}

	out, err = r.Exec(ctx, map[string]any{"op": "status"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"backend: tfidf", "chunks: 2", "vocab:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status must report %q, got %q", want, out)
		}
	}
}

func TestRememberStatusEmptyAndRecallWithoutIndex(t *testing.T) {
	rememberNoEnv(t)
	r := &Remember{Root: t.TempDir()}
	ctx := context.Background()

	out, err := r.Exec(ctx, map[string]any{"op": "status"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "0 chunks") || !strings.Contains(out, "index") {
		t.Fatalf("empty status must say 0 chunks + index fix, got %q", out)
	}
	if _, err := r.Exec(ctx, map[string]any{"op": "recall", "query": "anything"}); err == nil ||
		!strings.Contains(err.Error(), "index") {
		t.Fatalf("recall without index must name the index fix, got %v", err)
	}
}

func TestRememberRecallNeedsQueryAndOpSet(t *testing.T) {
	rememberNoEnv(t)
	r := &Remember{Root: t.TempDir()}
	ctx := context.Background()
	if _, err := r.Exec(ctx, map[string]any{"op": "recall"}); err == nil ||
		!strings.Contains(err.Error(), "query") {
		t.Fatalf("recall without query must be refused, got %v", err)
	}
	if _, err := r.Exec(ctx, map[string]any{"op": "embed"}); err == nil ||
		!strings.Contains(err.Error(), "index|recall|status") {
		t.Fatalf("unknown op must name the valid set, got %v", err)
	}
}

func TestRememberChunkCapNamesNarrowerPath(t *testing.T) {
	rememberNoEnv(t)
	root := t.TempDir()
	ctx := context.Background()
	// 3 files x 15000 short lines (~270KB each, under the 512KB skip)
	// chunk into 749 windows each = 2247 > the 2000 cap.
	for f := 0; f < 3; f++ {
		var b strings.Builder
		for i := 0; i < 15000; i++ {
			b.WriteString("tok alpha beta line\n")
		}
		writeFile(t, root, "big/file"+string(rune('0'+f))+".md", b.String())
	}
	r := &Remember{Root: root}
	_, err := r.Exec(ctx, map[string]any{"op": "index"})
	if err == nil || !strings.Contains(err.Error(), "2000") || !strings.Contains(err.Error(), "narrower") {
		t.Fatalf("over-cap index must refuse naming cap + narrower-path fix, got %v", err)
	}
	if _, serr := os.Stat(filepath.Join(root, ".tilde", "vectors.jsonl")); !os.IsNotExist(serr) {
		t.Fatal("refused index must not save a partial store")
	}
}

func TestRememberModelMismatchRefusesWithReindexFix(t *testing.T) {
	rememberNoEnv(t)
	ctx := context.Background()

	// TF-IDF index + Ollama env set: recall refuses.
	root := t.TempDir()
	writeFile(t, root, "a.go", "package a\nfunc A() {}\n")
	r := &Remember{Root: root}
	if _, err := r.Exec(ctx, map[string]any{"op": "index"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EmbedModelEnv, "other-model")
	_, err := r.Exec(ctx, map[string]any{"op": "recall", "query": "A"})
	if err == nil || !strings.Contains(err.Error(), "TF-IDF") || !strings.Contains(err.Error(), "index") {
		t.Fatalf("tfidf-index + model env must refuse naming re-index, got %v", err)
	}

	// Ollama index (built directly) + different env model: recall refuses.
	root2 := t.TempDir()
	st := &vec.Store{
		Model: "model-a",
		Dim:   2,
		Chunks: []vec.Chunk{
			{Path: "a.go", StartLine: 1, Text: "alpha beta", Vec: []float64{1, 0}},
		},
	}
	if err := st.SaveJSONL(filepath.Join(root2, ".tilde", "vectors.jsonl")); err != nil {
		t.Fatal(err)
	}
	r2 := &Remember{Root: root2}
	t.Setenv(EmbedModelEnv, "model-b")
	_, err = r2.Exec(ctx, map[string]any{"op": "recall", "query": "alpha"})
	if err == nil || !strings.Contains(err.Error(), "model-a") ||
		!strings.Contains(err.Error(), "model-b") || !strings.Contains(err.Error(), "index") {
		t.Fatalf("model drift must refuse naming both models + re-index, got %v", err)
	}
}

func TestRememberSkipsLinksAndHugeFiles(t *testing.T) {
	rememberNoEnv(t)
	root := t.TempDir()
	ctx := context.Background()
	writeFile(t, root, "real.go", "package real\nfunc Real() {}\n")
	if err := os.Symlink("real.go", filepath.Join(root, "link.go")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "huge.go", strings.Repeat("x\n", 300000)) // ~600KB > cap

	r := &Remember{Root: root}
	out, err := r.Exec(ctx, map[string]any{"op": "index"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1 chunk(s) from 1 file(s)") {
		t.Fatalf("only the real small file may index, got %q", out)
	}
	if !strings.Contains(out, "skipped 1 symlink") {
		t.Fatalf("receipt must note the skipped symlink, got %q", out)
	}
}
