// Package tools — remember tool (P5-A vector memory).
//
// Semantic recall over the project's own code: op "index" chunks source
// files into .tilde/vectors.jsonl, op "recall" embeds the query in the
// same space and returns cosine top-k hits, op "status" reports counts.
//
// Containment: the store path is fixed (.tilde/vectors.jsonl); the caller
// "path" arg (file-or-dir, default Root) is resolved through contain() and
// reads go through readFileNoFollow, mirroring search.go/symbols.go skip
// rules (.git/node_modules/dist/__pycache__/.venv/target plus .hg and the
// tool's own .tilde state dir, >512KB files, symlinks never followed).
// Local files only: the sole network surface is OllamaEmbedder against
// localhost, chosen only when TILDE_EMBED_MODEL is set.
//
// Embedder selection: TILDE_EMBED_MODEL is read with os.Getenv directly
// inside Exec (per-call, so tests can t.Setenv; unset means local TF-IDF).
// TF-IDF vectors are corpus-relative — the IDF vocab persists in the JSONL
// header so recall-after-restart matches. Ollama vectors are absolute —
// the header records the model, and recall under a different model refuses
// and names the re-index fix.
//
// Policy tiers (owner wires policy; this file only documents):
//   - index: MUTATING (writes .tilde/vectors.jsonl) — suggested tier: ask.
//     NOT snapshotted (snapshotTools untouched on purpose): the store is a
//     derived cache, rebuildable at any time with op "index".
//   - recall, status: read-only — suggested tier: allow (Plan-safe).
package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tilde/internal/vec"
)

const (
	// EmbedModelEnv selects the Ollama model for index/recall. Unset (the
	// default) means local zero-dep TF-IDF — fully offline, hermetic.
	EmbedModelEnv = "TILDE_EMBED_MODEL"
	// vectorsRel is the only store path this tool ever touches.
	vectorsRel = ".tilde/vectors.jsonl"
	// rememberMaxFileBytes mirrors search.go/symbols.go: huge files are
	// skipped (read_file samples them instead).
	rememberMaxFileBytes = 512 * 1024
	// rememberMaxChunks caps one index; over it the tool refuses and names
	// the narrower-path fix instead of saving a truncated store.
	rememberMaxChunks = 2000
	// rememberRecallDefault/Cap bound recall breadth (k<=0 → default).
	rememberRecallDefault = 5
	rememberRecallCap     = 20
	// rememberSnippetMax caps one hit snippet (rune count).
	rememberSnippetMax = 120
)

// rememberSkipDirs mirrors search.go + symbols.go, plus .tilde (this
// tool's own state must never pollute the semantic index).
var rememberSkipDirs = map[string]bool{
	".git": true, ".hg": true, "node_modules": true,
	"dist": true, "__pycache__": true, ".venv": true, "target": true,
	".tilde": true,
}

// rememberExts are the only extensions chunked.
var rememberExts = map[string]bool{
	".go": true, ".py": true, ".ts": true,
	".js": true, ".rs": true, ".md": true,
}

// Remember is project-local vector memory: index/recall/status over
// <Root>/.tilde/vectors.jsonl. File-backed (survives restarts); hermetic
// tests point Root at t.TempDir() with TILDE_EMBED_MODEL unset.
type Remember struct {
	Root string
	Seen *SeenMap // recall hits count as seen (partial views, like grep)
}

func (t *Remember) Name() string { return "remember" }
func (t *Remember) Description() string {
	return "Project vector memory: index code for semantic recall, recall similar chunks, or report index status. Local files only; TF-IDF by default, localhost Ollama when TILDE_EMBED_MODEL is set."
}
func (t *Remember) Schema() map[string]any {
	return map[string]any{"type": "object",
		"properties": map[string]any{
			"op":      map[string]any{"type": "string", "enum": []string{"index", "recall", "status"}},
			"path":    map[string]any{"type": "string", "description": "File or dir to index, default project root"},
			"query":   map[string]any{"type": "string", "description": "Natural-language query (recall only)"},
			"k":       map[string]any{"type": "number", "description": "Max hits, default 5, cap 20 (recall only)"},
			"recency": map[string]any{"type": "string", "description": "Recency half-life like \"24h\" weighting recent files (recall only, empty = off, pure cosine)"},
		}, "required": []string{"op"}}
}

func (t *Remember) Exec(_ context.Context, args map[string]any) (string, error) {
	// Env is read here (not cached at construction) so each call honors
	// the current TILDE_EMBED_MODEL, matching t.Setenv-style tests.
	envModel := strings.TrimSpace(os.Getenv(EmbedModelEnv))
	switch op := optStr(args, "op", "status"); op {
	case "index":
		return t.index(optStr(args, "path", ""), envModel)
	case "recall":
		return t.recall(optStr(args, "query", ""), optInt(args, "k", rememberRecallDefault), optStr(args, "recency", ""), envModel)
	case "status":
		return t.status()
	default:
		return "", fmt.Errorf("unknown op %q: pass one of index|recall|status", op)
	}
}

// vectorsFull resolves the fixed store path (containment + final-symlink
// refusal, same wording as the memory tool).
func (t *Remember) vectorsFull() (string, error) {
	full, err := contain(t.Root, vectorsRel)
	if err != nil {
		return "", err
	}
	if st, err := os.Lstat(full); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing %q: symlink at the final path — point at a real file inside the project and retry", vectorsRel)
	}
	return full, nil
}

// index walks a file-or-dir (default Root), chunks supported extensions,
// embeds (TF-IDF default, Ollama when envModel is set) and saves the store.
// MUTATING: writes .tilde/vectors.jsonl (owner: ask tier).
func (t *Remember) index(sub, envModel string) (string, error) {
	display := sub
	if display == "" {
		display = "."
	}
	base := t.Root
	if sub != "" && sub != "." {
		var err error
		if base, err = contain(t.Root, sub); err != nil {
			return "", err
		}
	}
	st, err := os.Lstat(base)
	if err != nil {
		return "", fmt.Errorf("cannot index %q: %v — check the path with glob first", display, err)
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing %q: symlink — point at a real file or dir inside the project and retry", display)
	}

	var files []string
	skippedLinks := 0
	if st.IsDir() {
		// Walk is lexical (deterministic); unreadable entries are skipped
		// like search.go, never fatal.
		_ = filepath.Walk(base, func(path string, info os.FileInfo, werr error) error {
			if werr != nil {
				return nil
			}
			if info.IsDir() {
				if rememberSkipDirs[info.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			lst, lerr := os.Lstat(path)
			if lerr != nil || lst.Mode()&os.ModeSymlink != 0 || !lst.Mode().IsRegular() {
				skippedLinks++
				return nil
			}
			if lst.Size() > rememberMaxFileBytes {
				return nil
			}
			if !rememberExts[strings.ToLower(filepath.Ext(path))] {
				return nil
			}
			files = append(files, path)
			return nil
		})
	} else {
		if !st.Mode().IsRegular() {
			return "", fmt.Errorf("refusing %q: not a regular file — pass a file or dir inside the project", display)
		}
		if st.Size() > rememberMaxFileBytes {
			return "", fmt.Errorf("refusing %q: file is %d bytes (over the %d-byte cap) — index a subdir of smaller files instead", display, st.Size(), rememberMaxFileBytes)
		}
		if !rememberExts[strings.ToLower(filepath.Ext(base))] {
			return "", fmt.Errorf("no indexable content in %q: only .go/.py/.ts/.js/.rs/.md are chunked — pass a supported file or a dir", display)
		}
		files = []string{base}
	}

	var chunks []vec.Chunk
	readFiles := 0
	for _, f := range files {
		rel, rerr := filepath.Rel(t.Root, f)
		if rerr != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		data, rerr := readFileNoFollow(t.Root, rel)
		if rerr != nil {
			continue // vanished or swapped mid-walk — skip like search.go
		}
		readFiles++
		// Recency signal (§9 episodic): stamp index-time file mtime on
		// every chunk from this file; recall weights by it only when
		// asked (recency arg), so the semantic default stays pure.
		var mt time.Time
		if fi, serr := os.Stat(f); serr == nil {
			mt = fi.ModTime()
		}
		for _, c := range vec.ChunkFile(rel, strings.Split(string(data), "\n"), 0, 0) {
			if strings.TrimSpace(c.Text) == "" {
				continue
			}
			c.ModTime = mt
			chunks = append(chunks, c)
		}
		if len(chunks) > rememberMaxChunks {
			return "", fmt.Errorf("refusing index: %d+ chunks under %q exceeds the %d-chunk cap — re-run op \"index\" with a narrower \"path\" (a subdir or single file) and retry", len(chunks), display, rememberMaxChunks)
		}
	}
	if len(chunks) == 0 {
		return "", fmt.Errorf("no indexable chunks under %q: only .go/.py/.ts/.js/.rs/.md under %d bytes are chunked — check the path with glob first", display, rememberMaxFileBytes)
	}

	store := &vec.Store{Chunks: chunks}
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}
	backend := ""
	if envModel != "" {
		emb := &vec.OllamaEmbedder{Model: envModel}
		vecs, err := emb.Embed(texts)
		if err != nil {
			return "", err
		}
		for i := range chunks {
			store.Chunks[i].Vec = vecs[i]
		}
		store.Model = envModel
		if len(vecs) > 0 {
			store.Dim = len(vecs[0])
		}
		backend = fmt.Sprintf("backend ollama model=%q dim=%d", store.Model, store.Dim)
	} else {
		tf := &vec.TFIDF{}
		tf.Fit(texts)
		vecs, err := tf.Embed(texts)
		if err != nil {
			return "", err
		}
		for i := range chunks {
			store.Chunks[i].Vec = vecs[i]
		}
		store.Vocab, store.IDF = tf.Vocab(), tf.IDF()
		backend = fmt.Sprintf("backend tfidf vocab=%d dim=%d", len(store.Vocab), tf.Dim())
	}

	full, err := t.vectorsFull()
	if err != nil {
		return "", err
	}
	if err := store.SaveJSONL(full); err != nil {
		return "", err
	}
	out := fmt.Sprintf("indexed %d chunk(s) from %d file(s) under %q (%s) → %s.", len(chunks), readFiles, display, backend, vectorsRel)
	if skippedLinks > 0 {
		plural := "entries"
		if skippedLinks == 1 {
			plural = "entry"
		}
		out += fmt.Sprintf("\n[note: skipped %d symlink/non-regular %s — links are never followed]", skippedLinks, plural)
	}
	return out, nil
}

// recall embeds the query in the index's own space and returns cosine
// top-k `path:start: score snippet` lines, fenced as untrusted content
// (chunk text is prior file content). READ-ONLY (owner: allow tier).
func (t *Remember) recall(query string, k int, recency, envModel string) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("op \"recall\" needs \"query\" as a non-empty string")
	}
	var opts []vec.RecallOption
	recNote := "pure cosine"
	if strings.TrimSpace(recency) != "" {
		d, err := time.ParseDuration(strings.TrimSpace(recency))
		if err != nil || d <= 0 {
			return "", fmt.Errorf("unknown recency %q: pass a Go duration like \"24h\" or \"\" for off (pure cosine)", recency)
		}
		opts = append(opts, vec.WithRecency(d))
		recNote = "recency half-life " + strings.TrimSpace(recency)
	}
	if k <= 0 {
		k = rememberRecallDefault
	}
	if k > rememberRecallCap {
		k = rememberRecallCap
	}
	store, err := t.loadStore()
	if err != nil {
		return "", err
	}
	var qvec []float64
	backend := ""
	if store.Model != "" {
		use := envModel
		if use == "" {
			use = store.Model // recall under the index model by default
		}
		if use != store.Model {
			return "", fmt.Errorf("refusing recall: index was built with ollama model %q but TILDE_EMBED_MODEL=%q — re-run op \"index\" with TILDE_EMBED_MODEL=%q (or re-index everything under the new model) and retry", store.Model, envModel, store.Model)
		}
		vecs, err := (&vec.OllamaEmbedder{Model: store.Model}).Embed([]string{query})
		if err != nil {
			return "", err
		}
		qvec = vecs[0]
		backend = fmt.Sprintf("ollama model=%q", store.Model)
	} else {
		if envModel != "" {
			return "", fmt.Errorf("refusing recall: index was built with local TF-IDF but TILDE_EMBED_MODEL=%q is set — unset it, or re-run op \"index\" with TILDE_EMBED_MODEL=%q to rebuild under Ollama, then retry", envModel, envModel)
		}
		vecs, err := store.TFIDF().Embed([]string{query})
		if err != nil {
			return "", err
		}
		qvec = vecs[0]
		backend = fmt.Sprintf("tfidf vocab=%d (corpus-relative)", len(store.Vocab))
	}

	hits := store.Recall(qvec, k, opts...)
	lines := make([]string, 0, len(hits))
	for _, h := range hits {
		lines = append(lines, fmt.Sprintf("%s:%d: %.4f %s", h.Path, h.StartLine, h.Score, snippetOf(h.Text)))
	}
	body := strings.Join(lines, "\n") +
		fmt.Sprintf("\n[note: backend %s, %s — showing top %d of %d chunks]", backend, recNote, len(hits), len(store.Chunks))
	if t.Seen != nil {
		for _, h := range hits {
			t.Seen.Mark(h.Path)
		}
	}
	return Fence(body), nil
}

// status reports chunk/vocab/dim counts (missing store = empty, not an
// error). READ-ONLY (owner: allow tier).
func (t *Remember) status() (string, error) {
	full, err := t.vectorsFull()
	if err != nil {
		return "", err
	}
	store := &vec.Store{}
	if err := store.LoadJSONL(full); err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("not indexed yet: 0 chunks — run op \"index\" to build %s.", vectorsRel), nil
		}
		return "", err
	}
	if store.Model != "" {
		return fmt.Sprintf("backend: ollama model=%q, chunks: %d, dim: %d, file: %s.",
			store.Model, len(store.Chunks), store.Dim, vectorsRel), nil
	}
	return fmt.Sprintf("backend: tfidf, chunks: %d, vocab: %d, dim: %d, file: %s.",
		len(store.Chunks), len(store.Vocab), len(store.Vocab), vectorsRel), nil
}

// loadStore reads the index; a missing/empty store names the index-first
// fix instead of failing cryptically downstream.
func (t *Remember) loadStore() (*vec.Store, error) {
	full, err := t.vectorsFull()
	if err != nil {
		return nil, err
	}
	store := &vec.Store{}
	if err := store.LoadJSONL(full); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("nothing indexed yet — run op \"index\" first to build %s, then recall", vectorsRel)
		}
		return nil, err
	}
	if len(store.Chunks) == 0 {
		return nil, fmt.Errorf("nothing indexed yet — run op \"index\" first to build %s, then recall", vectorsRel)
	}
	return store, nil
}

// snippetOf returns the first non-blank chunk line, capped at
// rememberSnippetMax runes (mirrors the symbols.go 120-char trim).
func snippetOf(text string) string {
	first := ""
	for _, ln := range strings.Split(text, "\n") {
		if s := strings.TrimSpace(ln); s != "" {
			first = s
			break
		}
	}
	if first == "" {
		return "(blank chunk)"
	}
	if len([]rune(first)) > rememberSnippetMax {
		return string([]rune(first)[:rememberSnippetMax]) + "…"
	}
	return first
}
