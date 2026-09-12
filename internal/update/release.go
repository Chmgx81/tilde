// Release-channel self-update: a binary installed from a release tarball
// (curl | sh, install.sh --from-release) updates by downloading the newest
// tagged release, verifying its sha256 against the release checksums.txt,
// and swapping the running binary atomically. No Go toolchain, no git
// checkout. Source installs keep runSource (pull + build).
package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// releaseDownloadBase is the release asset root. A var so tests can point
// it at an httptest server (no network in tests).
var releaseDownloadBase = "https://github.com/Chmgx81/tilde/releases/download"

// releaseHTTP is the release download client. Bounded so a stalled
// download cannot hang `tilde update` forever.
var releaseHTTP = &http.Client{Timeout: 3 * time.Minute}

// maxAssetBytes caps a downloaded asset: generous for a ~20MB binary,
// hostile to a runaway or hostile response.
const maxAssetBytes = 256 << 20

// errAssetAbsent marks a 404 for the current platform's asset.
var errAssetAbsent = errors.New("release asset not found")

// resolveNewestRelease is remoteVersion, injectable so tests exercise the
// release path without network.
var resolveNewestRelease = remoteVersion

// Run dispatches by install channel: release installs (and any binary with
// no/!unreadable install record — the curl installer historically wrote
// none) update from releases; source installs pull and build.
func Run() error {
	inst, err := readInstall()
	if err != nil {
		return runRelease()
	}
	if inst.Kind == kindRelease {
		return runRelease()
	}
	return runSource()
}

// assetName is the release artifact for a tag + platform, matching get.sh
// and the goreleaser archive name_template.
func assetName(tag, goos, goarch string) string {
	ext := "tar.gz"
	if goos == "darwin" {
		ext = "zip"
	}
	return fmt.Sprintf("tilde_%s_%s_%s.%s", tag, goos, goarch, ext)
}

func runRelease() error {
	target, err := updateTarget()
	if err != nil {
		return err
	}
	// One context spans resolve + download + verify; it must stay live for
	// the whole run (an earlier cancel would abort the download).
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	newest, _, err := resolveNewestRelease(ctx)
	if err != nil {
		return fmt.Errorf("cannot resolve the newest release (offline or rate-limited?): %v — nothing was changed", err)
	}
	if newest == "" {
		return fmt.Errorf("no release tags found — nothing to install; reinstall from a release (%s/releases)", repoURL)
	}
	if compareVersion(newest, Version) <= 0 {
		fmt.Printf("tilde: already up to date (%s)\n", Version)
		return nil
	}
	fmt.Printf("tilde: updating via release %s → %s (%s/%s)\n", Version, newest, runtime.GOOS, runtime.GOARCH)

	tmp, err := os.MkdirTemp("", "tilde-update-*")
	if err != nil {
		return fmt.Errorf("cannot create a temp dir: %v", err)
	}
	defer os.RemoveAll(tmp)

	asset := assetName(newest, runtime.GOOS, runtime.GOARCH)
	base := releaseDownloadBase + "/" + newest
	assetPath := filepath.Join(tmp, asset)
	if err := downloadFile(ctx, base+"/"+asset, assetPath); err != nil {
		if errors.Is(err, errAssetAbsent) {
			return fmt.Errorf("no release asset %s for %s/%s — supported: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 — nothing was changed", asset, runtime.GOOS, runtime.GOARCH)
		}
		return fmt.Errorf("download failed: %v — nothing was changed", err)
	}
	sums, err := downloadString(ctx, base+"/checksums.txt")
	if err != nil {
		return fmt.Errorf("cannot fetch checksums.txt: %v — refusing to install an unverified binary", err)
	}
	if err := verifyChecksum(assetPath, asset, sums); err != nil {
		return err
	}
	binPath := filepath.Join(tmp, "tilde")
	if err := extractBinary(assetPath, binPath); err != nil {
		return fmt.Errorf("cannot extract %s: %v — nothing was changed", asset, err)
	}
	if err := installBinary(binPath, target); err != nil {
		return fmt.Errorf("cannot replace %s: %v — nothing was changed; if this is a system path, reinstall with PREFIX pointing at a writable dir", target, err)
	}
	_ = RecordRelease(newest)
	if p, err := checkPath(); err == nil {
		writeCheck(p, checkFile{CheckedAt: time.Now().Unix(), Available: false, Remote: newest})
	}
	fmt.Printf("tilde: updated %s → %s — restart tilde to use it\n", Version, newest)
	return nil
}

// verifyChecksum fails closed: a missing entry or a mismatch refuses the
// install (mirrors get.sh). checksums.txt lines are "<sha256>  <name>".
func verifyChecksum(path, name, sums string) error {
	want := ""
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			want = fields[0]
			break
		}
	}
	if want == "" {
		return fmt.Errorf("no checksum entry for %s in checksums.txt — refusing to install an unverified binary", name)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("cannot read downloaded %s: %v", name, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("cannot hash %s: %v", name, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s (want %s, got %s) — download corrupt or tampered; nothing was changed", name, want, got)
	}
	return nil
}

// extractBinary pulls the "tilde" member out of a .tar.gz or .zip asset and
// writes it to dest (0755). A directory or symlink member named tilde is
// refused (a crafted archive could otherwise redirect the write).
func extractBinary(assetPath, dest string) error {
	if strings.HasSuffix(assetPath, ".zip") {
		return extractZip(assetPath, dest)
	}
	return extractTarGz(assetPath, dest)
}

func writeMember(dest string, r io.Reader) error {
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func extractTarGz(assetPath, dest string) error {
	f, err := os.Open(assetPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if filepath.Base(hdr.Name) != "tilde" {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			return fmt.Errorf("archive member %q is not a regular file — refusing", hdr.Name)
		}
		return writeMember(dest, tr)
	}
	return fmt.Errorf("archive did not contain a 'tilde' file")
}

func extractZip(assetPath, dest string) error {
	zr, err := zip.OpenReader(assetPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, zf := range zr.File {
		if filepath.Base(zf.Name) != "tilde" {
			continue
		}
		if zf.FileInfo().IsDir() || zf.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive member %q is not a regular file — refusing", zf.Name)
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		err = writeMember(dest, rc)
		rc.Close()
		return err
	}
	return fmt.Errorf("archive did not contain a 'tilde' file")
}

// downloadFile GETs url to dest, mapping 404 to errAssetAbsent and capping
// the body at maxAssetBytes.
func downloadFile(ctx context.Context, url, dest string) error {
	resp, err := get(ctx, url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errAssetAbsent
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d for %s", resp.StatusCode, url)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(resp.Body, maxAssetBytes)); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func downloadString(ctx context.Context, url string) (string, error) {
	resp, err := get(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d for %s", resp.StatusCode, url)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return string(b), err
}

func get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	return releaseHTTP.Do(req)
}
