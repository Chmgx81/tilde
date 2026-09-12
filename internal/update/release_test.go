package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stubNewestRelease makes the release resolver return one tag and restores
// it on the returned func (defer stubNewestRelease("v9.9.9")()).
func stubNewestRelease(tag string) func() {
	prev := resolveNewestRelease
	resolveNewestRelease = func(context.Context) (string, string, error) { return tag, "", nil }
	return func() { resolveNewestRelease = prev }
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func tarGz(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestAssetName(t *testing.T) {
	if got := assetName("v1.2.3", "linux", "amd64"); got != "tilde_v1.2.3_linux_amd64.tar.gz" {
		t.Fatalf("linux asset = %q", got)
	}
	if got := assetName("v1.2.3", "darwin", "arm64"); got != "tilde_v1.2.3_darwin_arm64.zip" {
		t.Fatalf("darwin asset = %q", got)
	}
}

func TestVerifyChecksum(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(p, "a.bin", sha256hex("hello")+"  a.bin\n"); err != nil {
		t.Fatalf("matching checksum must pass: %v", err)
	}
	if err := verifyChecksum(p, "a.bin", ""); err == nil {
		t.Fatal("missing entry must refuse")
	}
	if err := verifyChecksum(p, "a.bin", strings.Repeat("0", 64)+"  a.bin\n"); err == nil {
		t.Fatal("mismatch must refuse")
	}
}

// releaseServer serves one release: the platform asset + checksums.txt.
func releaseServer(t *testing.T, tag string, asset string, assetBody []byte, sums string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case asset:
			w.Write(assetBody)
		case "checksums.txt":
			io.WriteString(w, sums)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestRunReleaseUpdatesBinary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	newBin := []byte("#!/bin/sh\necho new\n")
	asset := assetName("v9.9.9", runtime.GOOS, runtime.GOARCH)
	body := tarGz(t, "tilde", newBin)
	srv := releaseServer(t, "v9.9.9", asset, body, sha256hex(string(body))+"  "+asset+"\n")
	defer srv.Close()
	prev := releaseDownloadBase
	releaseDownloadBase = srv.URL
	defer func() { releaseDownloadBase = prev }()
	defer stubNewestRelease("v9.9.9")()

	target := filepath.Join(t.TempDir(), "tilde")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TILDE_UPDATE_TARGET", target)

	if err := runRelease(); err != nil {
		t.Fatalf("runRelease: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != string(newBin) {
		t.Fatalf("target not replaced: %q", got)
	}
	inst, err := readInstall()
	if err != nil || inst.Kind != kindRelease || inst.Tag != "v9.9.9" {
		t.Fatalf("install record = %+v, %v", inst, err)
	}
}

func TestRunReleaseRefusesChecksumMismatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	newBin := []byte("new")
	asset := assetName("v9.9.9", runtime.GOOS, runtime.GOARCH)
	srv := releaseServer(t, "v9.9.9", asset, tarGz(t, "tilde", newBin), strings.Repeat("0", 64)+"  "+asset+"\n")
	defer srv.Close()
	prev := releaseDownloadBase
	releaseDownloadBase = srv.URL
	defer func() { releaseDownloadBase = prev }()
	defer stubNewestRelease("v9.9.9")()

	target := filepath.Join(t.TempDir(), "tilde")
	os.WriteFile(target, []byte("old"), 0o755)
	t.Setenv("TILDE_UPDATE_TARGET", target)

	err := runRelease()
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("checksum mismatch must refuse, got %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Fatalf("refused update must not touch the binary, got %q", got)
	}
}

func TestRunReleaseRefusesDowngrade(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	target := filepath.Join(t.TempDir(), "tilde")
	os.WriteFile(target, []byte("old"), 0o755)
	t.Setenv("TILDE_UPDATE_TARGET", target)
	defer stubNewestRelease(Version)() // same as built-in → up to date
	if err := runRelease(); err != nil {
		t.Fatalf("same/newer check should be a no-op success, got %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Fatalf("up-to-date must not touch the binary, got %q", got)
	}
}

func TestRunReleaseMissingPlatformAsset(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	prev := releaseDownloadBase
	releaseDownloadBase = srv.URL
	defer func() { releaseDownloadBase = prev }()
	defer stubNewestRelease("v9.9.9")()
	t.Setenv("TILDE_UPDATE_TARGET", filepath.Join(t.TempDir(), "tilde"))

	err := runRelease()
	if err == nil || !strings.Contains(err.Error(), "no release asset") {
		t.Fatalf("missing platform asset must name it, got %v", err)
	}
}

func TestDownloadOnceResumesFromPartial(t *testing.T) {
	full := []byte("0123456789ABCDEFGHIJ")
	half := len(full) / 2
	var gotRange string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		w.Header().Set("Content-Range", "bytes 10-19/20")
		w.WriteHeader(http.StatusPartialContent)
		w.Write(full[half:])
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(dest, full[:half], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := downloadOnce(context.Background(), srv.URL, dest); err != nil {
		t.Fatalf("downloadOnce: %v", err)
	}
	if gotRange != "bytes=10-" {
		t.Fatalf("resume must send a Range header, got %q", gotRange)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != string(full) {
		t.Fatalf("resumed file = %q, want %q", got, full)
	}
}

func TestDownloadOnceRestartsWhenRangeIgnored(t *testing.T) {
	full := []byte("whole-file-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // ignores Range
		w.Write(full)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "f.bin")
	os.WriteFile(dest, []byte("stale-partial"), 0o644) // a 200 must replace, not append
	if err := downloadOnce(context.Background(), srv.URL, dest); err != nil {
		t.Fatalf("downloadOnce: %v", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != string(full) {
		t.Fatalf("200 must restart cleanly, got %q", got)
	}
}

func TestDownloadOnceAlreadyComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable) // 416: nothing left
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "f.bin")
	os.WriteFile(dest, []byte("done"), 0o644)
	if err := downloadOnce(context.Background(), srv.URL, dest); err != nil {
		t.Fatalf("416 (already complete) must be a no-op success, got %v", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "done" {
		t.Fatalf("416 must leave the file intact, got %q", got)
	}
}

func TestDownloadFileRetriesThenSucceeds(t *testing.T) {
	full := []byte("retry-me")
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write(full)
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "f.bin")
	if err := downloadFile(context.Background(), srv.URL, dest); err != nil {
		t.Fatalf("downloadFile should retry past a transient 500, got %v", err)
	}
	if calls < 2 {
		t.Fatalf("expected a retry, calls = %d", calls)
	}
	if got, _ := os.ReadFile(dest); string(got) != string(full) {
		t.Fatalf("file = %q, want %q", got, full)
	}
}

func TestExtractBinaryRefusesNonRegularMember(t *testing.T) {
	dir := t.TempDir()
	asset := filepath.Join(dir, "a.tar.gz")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "tilde", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	tw.Close()
	gz.Close()
	os.WriteFile(asset, buf.Bytes(), 0o644)

	if err := extractBinary(asset, filepath.Join(dir, "out")); err == nil {
		t.Fatal("a symlink member named tilde must be refused")
	}
}
