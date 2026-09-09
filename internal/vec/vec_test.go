package vec

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tfidfFixture builds a 3-chunk store with one chunk carrying distinctive
// terms, returning the store plus the fitted embedder.
func tfidfFixture(t *testing.T) (*Store, *TFIDF, []string) {
	t.Helper()
	paths := []string{"serve.go", "geom.go", "csv.go"}
	texts := []string{
		"package main\nfunc handler() { http retry backoff middleware server }",
		"package main\nfunc slerp() { quaternion slerp hypersphere interpolation rotation }",
		"package main\nfunc parse() { csv reader comma split rows }",
	}
	tf := &TFIDF{}
	tf.Fit(texts)
	vecs, err := tf.Embed(texts)
	if err != nil {
		t.Fatal(err)
	}
	st := &Store{Vocab: tf.Vocab(), IDF: tf.IDF()}
	for i, p := range paths {
		st.Chunks = append(st.Chunks, Chunk{Path: p, StartLine: 1, Text: texts[i], Vec: vecs[i]})
	}
	return st, tf, texts
}

func embedQuery(t *testing.T, e Embedder, q string) []float64 {
	t.Helper()
	v, err := e.Embed([]string{q})
	if err != nil {
		t.Fatal(err)
	}
	return v[0]
}

func TestTfidfRecallRanksRelevantFirst(t *testing.T) {
	st, tf, _ := tfidfFixture(t)
	hits := st.Recall(embedQuery(t, tf, "quaternion slerp interpolation"), 5)
	if len(hits) != 3 {
		t.Fatalf("want 3 hits, got %d", len(hits))
	}
	if hits[0].Path != "geom.go" {
		t.Fatalf("relevant chunk must rank first, got %q (scores %+v)", hits[0].Path, hits)
	}
	if hits[0].Score <= 0 {
		t.Fatalf("relevant chunk must score > 0, got %f", hits[0].Score)
	}
}

func TestTfidfFitDropsStopwordsAndCapsVocab(t *testing.T) {
	docs := []string{
		"common shared alpha",
		"common shared beta",
		"common shared gamma",
		"common shared delta",
	}
	tf := &TFIDF{}
	tf.Fit(docs)
	seen := map[string]bool{}
	for _, w := range tf.Vocab() {
		seen[w] = true
	}
	if seen["common"] || seen["shared"] {
		t.Fatalf("stopwords (in >50%% of docs) must be dropped, vocab=%v", tf.Vocab())
	}
	if !seen["alpha"] {
		t.Fatalf("rare term must survive, vocab=%v", tf.Vocab())
	}

	// Vocab cap: 5000 unique tokens collapse to MaxVocab, deterministically.
	var many []string
	for i := 1; i <= 5000; i++ {
		many = append(many, fmt.Sprintf("w%04d", i))
	}
	a, b := &TFIDF{}, &TFIDF{}
	a.Fit(many)
	b.Fit(many)
	if a.Dim() != MaxVocab {
		t.Fatalf("vocab must cap at %d, got %d", MaxVocab, a.Dim())
	}
	if strings.Join(a.Vocab(), ",") != strings.Join(b.Vocab(), ",") {
		t.Fatal("Fit must be deterministic across runs")
	}
}

func TestTfidfEmbedRequiresFit(t *testing.T) {
	if _, err := (&TFIDF{}).Embed([]string{"hello"}); err == nil ||
		!strings.Contains(err.Error(), "Fit") {
		t.Fatalf("unfitted Embed must refuse naming Fit, got %v", err)
	}
}

func TestVecChunkFileWindows(t *testing.T) {
	var lines []string
	for i := 1; i <= 100; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	got := ChunkFile("f.go", lines, 0, 0) // defaults 40/20
	var starts []int
	for _, c := range got {
		starts = append(starts, c.StartLine)
	}
	want := []int{1, 21, 41, 61}
	if fmt.Sprint(starts) != fmt.Sprint(want) {
		t.Fatalf("default windows start %v, want %v", starts, want)
	}
	if n := len(strings.Split(got[len(got)-1].Text, "\n")); n != 40 {
		t.Fatalf("last window must hold 40 lines, got %d", n)
	}
	custom := ChunkFile("f.go", lines, 10, 5)
	if len(custom) == 0 || custom[0].StartLine != 1 || custom[1].StartLine != 6 {
		t.Fatalf("custom size/stride windows wrong: %+v", custom[:2])
	}
	if out := ChunkFile("f.go", nil, 0, 0); len(out) != 0 {
		t.Fatalf("empty input must yield no chunks, got %d", len(out))
	}
}

func TestVecSaveLoadRoundTripPreservesOrder(t *testing.T) {
	st, tf, _ := tfidfFixture(t)
	q := embedQuery(t, tf, "quaternion slerp interpolation")
	before := st.Recall(q, 5)

	path := filepath.Join(t.TempDir(), ".tilde", "vectors.jsonl")
	if err := st.SaveJSONL(path); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("store file must be 0600, got %v %v", fi, err)
	}

	loaded := &Store{}
	if err := loaded.LoadJSONL(path); err != nil {
		t.Fatal(err)
	}
	after := loaded.Recall(q, 5)
	if len(after) != len(before) {
		t.Fatalf("recall count changed: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if after[i].Path != before[i].Path || after[i].Score != before[i].Score {
			t.Fatalf("order/score changed at %d: %+v vs %+v", i, before[i], after[i])
		}
	}
}

func TestVecRecallAfterRestartMatches(t *testing.T) {
	st, tf, _ := tfidfFixture(t)
	q := "quaternion slerp interpolation"
	want := st.Recall(embedQuery(t, tf, q), 5)

	path := filepath.Join(t.TempDir(), "vectors.jsonl")
	if err := st.SaveJSONL(path); err != nil {
		t.Fatal(err)
	}
	loaded := &Store{}
	if err := loaded.LoadJSONL(path); err != nil {
		t.Fatal(err)
	}
	// Fresh embedder restored from the persisted header only.
	restored := loaded.TFIDF()
	got := loaded.Recall(embedQuery(t, restored, q), 5)
	for i := range want {
		if got[i].Path != want[i].Path || got[i].Score != want[i].Score {
			t.Fatalf("restart changed recall at %d: %+v vs %+v", i, want[i], got[i])
		}
	}
}

func TestVecSaveLoadOllamaHeaderPreserved(t *testing.T) {
	st := &Store{
		Model: "test-model",
		Dim:   2,
		Chunks: []Chunk{
			{Path: "a.go", StartLine: 1, Text: "alpha", Vec: []float64{1, 0}},
		},
	}
	path := filepath.Join(t.TempDir(), "vectors.jsonl")
	if err := st.SaveJSONL(path); err != nil {
		t.Fatal(err)
	}
	loaded := &Store{}
	if err := loaded.LoadJSONL(path); err != nil {
		t.Fatal(err)
	}
	if loaded.Model != "test-model" || loaded.Dim != 2 || loaded.Backend() != "ollama" {
		t.Fatalf("ollama header lost: %+v", loaded)
	}
}

func TestVecCosineZeroSafe(t *testing.T) {
	zero := []float64{0, 0, 0}
	x := []float64{0.5, 0.5, 0.5}
	for _, s := range []float64{Cosine(zero, x), Cosine(x, zero), Cosine(nil, nil), Cosine(zero, zero)} {
		if s != 0 || math.IsNaN(s) {
			t.Fatalf("zero-norm cosine must be 0, got %v", s)
		}
	}
	st := &Store{Chunks: []Chunk{
		{Path: "a.go", StartLine: 1, Text: "a", Vec: x},
		{Path: "b.go", StartLine: 1, Text: "b", Vec: x},
	}}
	hits := st.Recall(zero, 5)
	if len(hits) != 2 {
		t.Fatalf("zero query must still return rows, got %d", len(hits))
	}
	for _, h := range hits {
		if h.Score != 0 || math.IsNaN(h.Score) {
			t.Fatalf("zero-query score must be 0, got %v", h.Score)
		}
	}
}

func TestRecencyReordersRecentAboveSimilar(t *testing.T) {
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	recent := now.Add(-time.Minute)

	// Identical vectors: default order is the path tie-break (a.go first);
	// recency must flip it so the recent chunk wins.
	st := &Store{Chunks: []Chunk{
		{Path: "a.go", StartLine: 1, Text: "aaa", Vec: []float64{1, 0}, ModTime: old},
		{Path: "b.go", StartLine: 1, Text: "bbb", Vec: []float64{1, 0}, ModTime: recent},
	}}
	q := []float64{1, 0}
	if got := st.Recall(q, 5); got[0].Path != "a.go" {
		t.Fatalf("default recall must keep path order, got %q", got[0].Path)
	}
	if got := st.Recall(q, 5, WithRecency(time.Hour)); got[0].Path != "b.go" {
		t.Fatalf("recency must rank the recent chunk first, got %+v", got)
	}

	// Similar but unequal cosines: the older chunk scores higher purely
	// semantically, yet recency must still lift the recent one above it.
	norm := math.Sqrt(0.9*0.9 + 0.4359*0.4359)
	st2 := &Store{Chunks: []Chunk{
		{Path: "old.go", StartLine: 1, Text: "old", Vec: []float64{1, 0}, ModTime: old},
		{Path: "new.go", StartLine: 1, Text: "new", Vec: []float64{0.9 / norm, 0.4359 / norm}, ModTime: recent},
	}}
	if got := st2.Recall(q, 5); got[0].Path != "old.go" {
		t.Fatalf("pure cosine must rank old.go first, got %q", got[0].Path)
	}
	if got := st2.Recall(q, 5, WithRecency(time.Hour)); got[0].Path != "new.go" {
		t.Fatalf("recency must lift new.go above old.go, got %+v", got)
	}
}

func TestRecencyOffByDefaultIdenticalOrder(t *testing.T) {
	st, tf, _ := tfidfFixture(t)
	q := embedQuery(t, tf, "quaternion slerp interpolation")
	plain := st.Recall(q, 5)
	withZero := st.Recall(q, 5, WithRecency(0))
	if len(plain) != len(withZero) {
		t.Fatalf("count changed: %d vs %d", len(plain), len(withZero))
	}
	for i := range plain {
		if plain[i].Path != withZero[i].Path || plain[i].Score != withZero[i].Score {
			t.Fatalf("WithRecency(0) must equal default at %d: %+v vs %+v", i, plain[i], withZero[i])
		}
	}
}

func TestRecencyZeroModTimeNoPenalty(t *testing.T) {
	now := time.Now()
	st := &Store{Chunks: []Chunk{
		{Path: "a.go", StartLine: 1, Text: "aaa", Vec: []float64{1, 0}}, // zero ModTime
		{Path: "b.go", StartLine: 1, Text: "bbb", Vec: []float64{1, 0}, ModTime: now.Add(-48 * time.Hour)},
	}}
	q := []float64{1, 0}
	got := st.Recall(q, 5, WithRecency(time.Hour))
	if len(got) != 2 {
		t.Fatalf("want 2 hits, got %d", len(got))
	}
	if got[0].Path != "a.go" {
		t.Fatalf("zero-ModTime chunk must keep full cosine (no penalty), got %+v", got)
	}
	if got[0].Score != 1 {
		t.Fatalf("zero-ModTime score must equal cosine 1, got %f", got[0].Score)
	}
	// Future mtimes clamp to age 0 (never boost above cosine).
	st.Chunks[1].ModTime = now.Add(time.Hour)
	got = st.Recall(q, 5, WithRecency(time.Hour))
	for _, h := range got {
		if h.Score > 1 {
			t.Fatalf("future mtime must not boost above cosine, got %+v", got)
		}
	}
}

func TestRecencyJSONLRoundTripAndTolerance(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	st := &Store{Chunks: []Chunk{
		{Path: "a.go", StartLine: 1, Text: "alpha", Vec: []float64{1, 0}, ModTime: now},
	}}
	path := filepath.Join(t.TempDir(), "vectors.jsonl")
	if err := st.SaveJSONL(path); err != nil {
		t.Fatal(err)
	}
	loaded := &Store{}
	if err := loaded.LoadJSONL(path); err != nil {
		t.Fatal(err)
	}
	if len(loaded.Chunks) != 1 || !loaded.Chunks[0].ModTime.Equal(now) {
		t.Fatalf("mtime must survive round-trip, got %+v", loaded.Chunks)
	}

	// Pre-recency lines without mtime load as zero ModTime (no migration).
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	var hdr map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &hdr); err != nil {
		t.Fatal(err)
	}
	stripped := lines[0] + "\n"
	for _, ln := range lines[1:] {
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatal(err)
		}
		delete(m, "mtime")
		b, _ := json.Marshal(m)
		stripped += string(b) + "\n"
	}
	if err := os.WriteFile(path, []byte(stripped), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := &Store{}
	if err := legacy.LoadJSONL(path); err != nil {
		t.Fatal(err)
	}
	if !legacy.Chunks[0].ModTime.IsZero() {
		t.Fatalf("missing mtime must load as zero ModTime, got %v", legacy.Chunks[0].ModTime)
	}
}

func TestRecencyFixtureMtimesViaChtimes(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.go")
	newPath := filepath.Join(dir, "new.go")
	for _, p := range []string{oldPath, newPath} {
		if err := os.WriteFile(p, []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	oldT := now.Add(-72 * time.Hour)
	newT := now.Add(-time.Minute)
	if err := os.Chtimes(oldPath, oldT, oldT); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newPath, newT, newT); err != nil {
		t.Fatal(err)
	}
	modOf := func(p string) time.Time {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return fi.ModTime()
	}
	// Index-time population model: ModTime comes from os.Stat at index
	// time — the same call remember's index will make per file.
	st := &Store{Chunks: []Chunk{
		{Path: "old.go", StartLine: 1, Text: "x", Vec: []float64{1, 0}, ModTime: modOf(oldPath)},
		{Path: "new.go", StartLine: 1, Text: "x", Vec: []float64{1, 0}, ModTime: modOf(newPath)},
	}}
	got := st.Recall([]float64{1, 0}, 5, WithRecency(24*time.Hour))
	if got[0].Path != "new.go" {
		t.Fatalf("recent file must win, got %+v", got)
	}
}

func TestOllamaEmbedPassthrough(t *testing.T) {
	var gotModel, gotPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings" {
			t.Errorf("path = %q, want /api/embeddings", r.URL.Path)
		}
		var req struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		gotModel, gotPrompt = req.Model, req.Prompt
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"embedding":[0.5,0.25,0.125]}`))
	}))
	defer srv.Close()

	emb := &OllamaEmbedder{Host: srv.URL, Model: "test-model"}
	vecs, err := emb.Embed([]string{"hello"})
	if err != nil {
		t.Fatal(err)
	}
	if gotModel != "test-model" || gotPrompt != "hello" {
		t.Fatalf("request = model %q prompt %q", gotModel, gotPrompt)
	}
	if len(vecs) != 1 || len(vecs[0]) != 3 || vecs[0][0] != 0.5 || vecs[0][2] != 0.125 {
		t.Fatalf("embedding not passed through: %v", vecs)
	}
	if emb.Dim() != 3 {
		t.Fatalf("Dim = %d, want 3", emb.Dim())
	}
}

func TestOllamaEmbed404NamesPull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"model \"ghost-9\" not found, try pulling it first"}`))
	}))
	defer srv.Close()

	emb := &OllamaEmbedder{Host: srv.URL, Model: "ghost-9"}
	_, err := emb.Embed([]string{"hello"})
	if err == nil {
		t.Fatal("404 must fail loud")
	}
	if !strings.Contains(err.Error(), "ollama pull ghost-9") {
		t.Fatalf("404 error must name the pull fix, got %v", err)
	}
}
