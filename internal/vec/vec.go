// Package vec — P5-A vector memory engine (stdlib only).
//
// Deterministic, offline-by-default semantic recall over code chunks:
//
// P6-B separation note: this package is the SEMANTIC store only — durable
// facts about the codebase ranked by cosine similarity. It is NOT the
// episodic store: session logs / transcripts ("what happened and when")
// are the episodic record and live outside this package (no code change
// there). Recency here is strictly opt-in via WithRecency (the episodic
// signal): default Recall is pure cosine so the semantic store stays pure
// by default, and the owner wires any `recency` arg (e.g. remember recall
// "24h" parsed with time.ParseDuration, empty = off) down to WithRecency.
//   - Embedder is the vector-space abstraction (corpus-relative TF-IDF by
//     default, absolute Ollama vectors when the owner opts in).
//   - TFIDF is zero-dep: lowercase alnum tokenizer, per-corpus IDF fitted
//     at index time, L2-normalized vectors. Deterministic: ties break
//     alphabetically, so the same corpus always yields the same vocab.
//   - OllamaEmbedder talks only to localhost (default http://localhost:11434)
//     via POST /api/embeddings {model, prompt}, one request per text,
//     serial, 60s timeout. No other network surface exists in this package.
//   - Store is an in-memory chunk list with JSONL persistence and cosine
//     top-k recall. Recall is zero-norm safe (score 0, never NaN) and
//     tie-broken by path/start so repeated queries return stable order.
//
// TF-IDF vectors are corpus-relative: the IDF table lives in the Store
// header ({"vocab":[...],"idf":[...]}) so recall-after-restart embeds the
// query in the same space. Ollama vectors are absolute: the header carries
// {"model":...,"dim":...} and recall must use the same model.
package vec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Embedder maps texts into a vector space.
type Embedder interface {
	Embed(texts []string) ([][]float64, error)
	Dim() int
}

// MaxVocab caps the TF-IDF vocabulary: the top MaxVocab tokens by document
// frequency survive (ties broken alphabetically for determinism).
const MaxVocab = 4096

// Chunking defaults: 40-line sliding windows every 20 lines.
const (
	DefaultChunkSize   = 40
	DefaultChunkStride = 20
)

// tokenize lowercases s and splits on non-alphanumeric runes, dropping
// tokens shorter than 2 runes.
func tokenize(s string) []string {
	var toks []string
	var cur []rune
	flush := func() {
		if len(cur) >= 2 {
			toks = append(toks, string(cur))
		}
		cur = cur[:0]
	}
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur = append(cur, r)
		} else {
			flush()
		}
	}
	flush()
	return toks
}

// TFIDF is a deterministic offline embedder. Call Fit once on the indexed
// corpus, then Embed. Vectors are tf*idf weighted and L2-normalized.
// The zero value is unfitted: Embed refuses until Fit (or Restore) runs.
type TFIDF struct {
	vocab  []string
	index  map[string]int
	idf    []float64
	fitted bool
}

// Fit builds the vocabulary from texts: tokens in more than half the docs
// are dropped as stopwords, survivors are ranked by document frequency
// (ties alphabetical) and capped at MaxVocab. IDF is log((1+N)/(1+df))+1.
func (t *TFIDF) Fit(texts []string) {
	n := len(texts)
	df := map[string]int{}
	for _, text := range texts {
		seen := map[string]bool{}
		for _, tok := range tokenize(text) {
			if !seen[tok] {
				seen[tok] = true
				df[tok]++
			}
		}
	}
	type cand struct {
		tok string
		df  int
	}
	cands := make([]cand, 0, len(df))
	for tok, d := range df {
		if n > 0 && d*2 > n {
			continue // stopword: present in >50% of docs
		}
		cands = append(cands, cand{tok, d})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].df != cands[j].df {
			return cands[i].df > cands[j].df
		}
		return cands[i].tok < cands[j].tok
	})
	if len(cands) > MaxVocab {
		cands = cands[:MaxVocab]
	}
	t.vocab = make([]string, len(cands))
	t.idf = make([]float64, len(cands))
	t.index = make(map[string]int, len(cands))
	for i, c := range cands {
		t.vocab[i] = c.tok
		t.idf[i] = math.Log(float64(n+1)/float64(c.df+1)) + 1
		t.index[c.tok] = i
	}
	t.fitted = true
}

// Embed maps each text to its L2-normalized tf*idf vector. Refuses when
// unfitted; zero-norm texts (no vocab overlap) yield the zero vector.
func (t *TFIDF) Embed(texts []string) ([][]float64, error) {
	if !t.fitted {
		return nil, fmt.Errorf("vec: tfidf embedder is not fitted — call Fit before Embed")
	}
	out := make([][]float64, len(texts))
	for i, text := range texts {
		v := make([]float64, len(t.vocab))
		for _, tok := range tokenize(text) {
			if j, ok := t.index[tok]; ok {
				v[j]++
			}
		}
		for j := range v {
			v[j] *= t.idf[j]
		}
		norm := 0.0
		for _, x := range v {
			norm += x * x
		}
		if norm > 0 {
			inv := 1 / math.Sqrt(norm)
			for j := range v {
				v[j] *= inv
			}
		}
		out[i] = v
	}
	return out, nil
}

// Dim is the vocabulary size (0 before Fit).
func (t *TFIDF) Dim() int { return len(t.vocab) }

// Vocab returns a copy of the fitted vocabulary (rank order).
func (t *TFIDF) Vocab() []string { return append([]string(nil), t.vocab...) }

// IDF returns a copy of the fitted IDF table (parallel to Vocab).
func (t *TFIDF) IDF() []float64 { return append([]float64(nil), t.idf...) }

// Restore rebuilds a fitted embedder from a persisted vocab/idf pair
// (Store header), so recall-after-restart embeds in the index-time space.
func (t *TFIDF) Restore(vocab []string, idf []float64) {
	t.vocab = append([]string(nil), vocab...)
	t.idf = append([]float64(nil), idf...)
	t.index = make(map[string]int, len(t.vocab))
	for i, w := range t.vocab {
		t.index[w] = i
	}
	t.fitted = true
}

// DefaultEmbedModel is the owner-chosen Ollama embedding model used when
// no model is named.
const DefaultEmbedModel = "nomic-embed-text"

// defaultOllamaHost is the only host this package ever dials: loopback.
// There is no caller-controlled URL, so there is no SSRF surface.
const defaultOllamaHost = "http://localhost:11434"

// OllamaEmbedder embeds via POST {Host}/api/embeddings {model, prompt},
// one request per text, serial, 60s timeout. Empty Host/Model fall back to
// the loopback default and DefaultEmbedModel.
type OllamaEmbedder struct {
	Host  string
	Model string

	mu      sync.Mutex
	lastDim int
}

func (o *OllamaEmbedder) host() string {
	h := strings.TrimRight(strings.TrimSpace(o.Host), "/")
	if h == "" {
		return defaultOllamaHost
	}
	return h
}

func (o *OllamaEmbedder) model() string {
	if m := strings.TrimSpace(o.Model); m != "" {
		return m
	}
	return DefaultEmbedModel
}

// Dim is the last successfully embedded dimension (0 before first Embed).
func (o *OllamaEmbedder) Dim() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.lastDim
}

// Embed sends each text serially; the first failure aborts (fail loud —
// a 404/model-missing names the `ollama pull <model>` fix).
func (o *OllamaEmbedder) Embed(texts []string) ([][]float64, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	out := make([][]float64, len(texts))
	for i, text := range texts {
		v, err := o.embedOne(client, text)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func (o *OllamaEmbedder) embedOne(client *http.Client, text string) ([]float64, error) {
	model := o.model()
	body, err := json.Marshal(map[string]string{"model": model, "prompt": text})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", o.host()+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("vec: ollama request for model %q: %v", model, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vec: ollama at %s unreachable for model %q: %v — is `ollama serve` running?", o.host(), model, err)
	}
	defer resp.Body.Close()
	// Surface transport truncation as itself: a failed read must not
	// masquerade as "bad JSON" and send the user down the wrong fix path.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("vec: ollama response body unreadable for model %q: %v — retry; if it persists the server is truncating responses", model, err)
	}
	if resp.StatusCode == http.StatusNotFound ||
		(resp.StatusCode != http.StatusOK && modelMissingIn(string(raw))) {
		return nil, pullErr(model, strings.TrimSpace(string(raw)))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vec: ollama embeddings failed for model %q: HTTP %d %s", model, resp.StatusCode, squeeze(string(raw), 200))
	}
	var decoded struct {
		Embedding  json.RawMessage `json:"embedding"`
		Embeddings json.RawMessage `json:"embeddings"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("vec: ollama bad JSON for model %q: %v", model, err)
	}
	src := decoded.Embedding
	if len(src) == 0 {
		src = decoded.Embeddings
	}
	if len(src) == 0 {
		return nil, fmt.Errorf("vec: ollama response for model %q has no embedding array — run \"ollama pull %s\" to ensure the model supports embeddings, then retry", model, model)
	}
	v, err := parseEmbedding(src)
	if err != nil {
		return nil, fmt.Errorf("vec: ollama bad embedding for model %q: %v", model, err)
	}
	o.mu.Lock()
	o.lastDim = len(v)
	o.mu.Unlock()
	return v, nil
}

// pullErr names the exact recovery: `ollama pull <model>`.
func pullErr(model, detail string) error {
	if detail == "" {
		detail = "model not found"
	}
	return fmt.Errorf("vec: ollama model %q unavailable: %s — run \"ollama pull %s\" to fetch it, then retry", model, squeeze(detail, 200), model)
}

// modelMissingIn reports whether a non-200 body reads as a missing model.
func modelMissingIn(body string) bool {
	l := strings.ToLower(body)
	for _, sub := range []string{
		"not found", "no such model", "does not exist",
		"could not find", "unknown model", "model not",
	} {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// parseEmbedding accepts a flat number array or an array of arrays
// (first row wins) — whichever shape the endpoint returned.
func parseEmbedding(raw json.RawMessage) ([]float64, error) {
	var single []float64
	if err := json.Unmarshal(raw, &single); err == nil && single != nil {
		return single, nil
	}
	var multi [][]float64
	if err := json.Unmarshal(raw, &multi); err == nil && len(multi) > 0 {
		return multi[0], nil
	}
	return nil, fmt.Errorf("no numeric embedding array")
}

// squeeze collapses whitespace and caps error details at n runes.
func squeeze(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}

// Chunk is one indexed window: repo-relative path, 1-based start line,
// text, file mtime at index time (via os.Stat; zero when unknown —
// JSONL-tolerant, see chunkLine), and (once embedded) its vector.
type Chunk struct {
	Path      string
	StartLine int
	Text      string
	ModTime   time.Time
	Vec       []float64
}

// ScoredChunk is a Chunk plus its cosine score against the query.
type ScoredChunk struct {
	Path      string
	StartLine int
	Text      string
	Score     float64
}

// Store is the in-memory vector index. Vocab/IDF persist the TF-IDF space;
// Model/Dim record the Ollama space — exactly one backend is live per file.
type Store struct {
	Chunks []Chunk
	Vocab  []string
	IDF    []float64
	Model  string
	Dim    int
}

// Backend reports which space this store was indexed in.
func (s *Store) Backend() string {
	if s.Model != "" {
		return "ollama"
	}
	return "tfidf"
}

// TFIDF restores an embedder from the persisted vocab/idf header.
func (s *Store) TFIDF() *TFIDF {
	t := &TFIDF{}
	t.Restore(s.Vocab, s.IDF)
	return t
}

// Cosine is cosine similarity, zero-norm safe: any zero/empty side scores
// 0, never NaN. Length mismatch compares the shared prefix (same-space
// vectors always share a length; this only guards corrupt rows).
func Cosine(a, b []float64) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var dot, na, nb float64
	for i := 0; i < n; i++ {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// RecallOption tunes Recall. Only WithRecency exists; zero value = off.
type recallOpts struct {
	halfLife time.Duration
}

// RecallOption is a functional option for Store.Recall.
type RecallOption func(*recallOpts)

// WithRecency weights scores by file recency (the episodic signal):
// score = cosine * exp(-age/halfLife), where age = now - chunk ModTime.
// Zero halfLife (the default) disables recency: pure cosine, so the
// semantic store stays pure unless the caller opts in. Zero ModTime
// (unknown, e.g. pre-recency JSONL lines) means age 0 — full cosine,
// no boost penalty. Future mtimes clamp to age 0.
func WithRecency(halfLife time.Duration) RecallOption {
	return func(o *recallOpts) { o.halfLife = halfLife }
}

// Recall returns the cosine top-k over chunks (k<=0 defaults to 5, capped
// at 20), score-descending with path/start tie-breaks for stable order.
// Pass WithRecency(halfLife) to multiply each cosine by the exponential
// recency decay; without it (default) Recall is pure semantic similarity.
func (s *Store) Recall(queryVec []float64, k int, opts ...RecallOption) []ScoredChunk {
	if k <= 0 {
		k = 5
	}
	if k > 20 {
		k = 20
	}
	var ro recallOpts
	for _, o := range opts {
		if o != nil {
			o(&ro)
		}
	}
	recencyOn := ro.halfLife > 0
	var now time.Time
	var decayDenom float64
	if recencyOn {
		now = time.Now()
		decayDenom = ro.halfLife.Seconds()
	}
	out := make([]ScoredChunk, 0, len(s.Chunks))
	for _, c := range s.Chunks {
		score := Cosine(queryVec, c.Vec)
		if recencyOn {
			var ageSec float64
			if !c.ModTime.IsZero() {
				if d := now.Sub(c.ModTime); d > 0 {
					ageSec = d.Seconds()
				}
			}
			score *= math.Exp(-ageSec / decayDenom)
		}
		out = append(out, ScoredChunk{
			Path:      c.Path,
			StartLine: c.StartLine,
			Text:      c.Text,
			Score:     score,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].StartLine < out[j].StartLine
	})
	if len(out) > k {
		out = out[:k]
	}
	return out
}

// ChunkFile slices lines into sliding windows (size/stride default to
// DefaultChunkSize/DefaultChunkStride when <=0). StartLine is 1-based.
func ChunkFile(path string, lines []string, size, stride int) []Chunk {
	if size <= 0 {
		size = DefaultChunkSize
	}
	if stride <= 0 {
		stride = DefaultChunkStride
	}
	var out []Chunk
	for start := 0; start < len(lines); {
		end := start + size
		if end > len(lines) {
			end = len(lines)
		}
		out = append(out, Chunk{
			Path:      path,
			StartLine: start + 1,
			Text:      strings.Join(lines[start:end], "\n"),
		})
		if end == len(lines) {
			break
		}
		start += stride
	}
	return out
}

// storeHeader is the first JSONL line: the space definition. TF-IDF stores
// carry vocab+idf; Ollama stores carry model+dim.
type storeHeader struct {
	Vocab []string  `json:"vocab,omitempty"`
	IDF   []float64 `json:"idf,omitempty"`
	Model string    `json:"model,omitempty"`
	Dim   int       `json:"dim,omitempty"`
}

// chunkLine is one JSONL line per chunk. Mtime is omitempty: lines
// written before recency (P6-B) carry no mtime and load as zero ModTime
// (age 0 — no penalty), so old stores keep working with no migration.
type chunkLine struct {
	Path  string    `json:"path"`
	Start int       `json:"start"`
	Text  string    `json:"text"`
	Mtime time.Time `json:"mtime,omitempty"`
	Vec   []float64 `json:"vec"`
}

// SaveJSONL persists the store atomically (temp file in the same dir +
// rename, 0600) so a crash mid-write leaves the old index intact. A
// final-component symlink is refused (same wording as the tool layer).
func (s *Store) SaveJSONL(path string) error {
	if st, err := os.Lstat(path); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("vec: refusing %q: symlink at the final path — point at a real file and retry", path)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("vec: cannot create parent dirs for %q: %v", path, err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(storeHeader{Vocab: s.Vocab, IDF: s.IDF, Model: s.Model, Dim: s.Dim}); err != nil {
		return err
	}
	for _, c := range s.Chunks {
		if err := enc.Encode(chunkLine{Path: c.Path, Start: c.StartLine, Text: c.Text, Mtime: c.ModTime, Vec: c.Vec}); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, ".vectors-*.tmp")
	if err != nil {
		return fmt.Errorf("vec: cannot create temp file for %q: %v", path, err)
	}
	tmpName := tmp.Name()
	failed := true
	defer func() {
		if failed {
			os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return fmt.Errorf("vec: cannot write %q: %v", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("vec: cannot write %q: %v", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("vec: cannot write %q: %v", path, err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("vec: cannot write %q: %v", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("vec: cannot write %q: %v", path, err)
	}
	failed = false
	return nil
}

// LoadJSONL replaces the store with the file's contents. A missing file
// returns the os error untouched (callers map IsNotExist to "index first").
// Corrupt lines fail loud; the header must pair vocab with idf.
func (s *Store) LoadJSONL(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	*s = Store{}
	first := true
	for _, ln := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if first {
			first = false
			var hdr storeHeader
			if err := json.Unmarshal([]byte(ln), &hdr); err != nil {
				return fmt.Errorf("vec: corrupt header in %q: %v — re-index to rebuild it", path, err)
			}
			if len(hdr.Vocab) != len(hdr.IDF) {
				return fmt.Errorf("vec: corrupt header in %q: vocab (%d) and idf (%d) lengths differ — re-index to rebuild it", path, len(hdr.Vocab), len(hdr.IDF))
			}
			s.Vocab, s.IDF, s.Model, s.Dim = hdr.Vocab, hdr.IDF, hdr.Model, hdr.Dim
			continue
		}
		var cl chunkLine
		if err := json.Unmarshal([]byte(ln), &cl); err != nil {
			return fmt.Errorf("vec: corrupt chunk line in %q: %v — re-index to rebuild it", path, err)
		}
		s.Chunks = append(s.Chunks, Chunk{Path: cl.Path, StartLine: cl.Start, Text: cl.Text, ModTime: cl.Mtime, Vec: cl.Vec})
	}
	return nil
}
