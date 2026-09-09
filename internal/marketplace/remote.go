package marketplace

// Remote support is deliberately split from local discovery. This package
// verifies a signed catalog and content digest before callers may stage an
// artifact; it never executes plugin code or mutates the active install.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxRemoteCatalogBytes  = 2 << 20
	maxRemoteArtifactBytes = 64 << 20
)

// SignedCatalog is an envelope over the exact JSON payload bytes. Publishers
// sign the payload, not a re-serialized object, so verification is unambiguous.
type SignedCatalog struct {
	KeyID     string `json:"key_id"`
	Payload   string `json:"payload"`   // base64(JSON catalog)
	Signature string `json:"signature"` // base64 Ed25519 signature
}

type RemoteCatalog struct {
	Items []RemoteItem `json:"items"`
}

type RemoteItem struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
}

func VerifySignedCatalog(envelope []byte, keys map[string]ed25519.PublicKey) (RemoteCatalog, error) {
	var signed SignedCatalog
	if err := json.Unmarshal(envelope, &signed); err != nil {
		return RemoteCatalog{}, fmt.Errorf("marketplace: invalid signed catalog: %w", err)
	}
	key := keys[signed.KeyID]
	if len(key) != ed25519.PublicKeySize {
		return RemoteCatalog{}, fmt.Errorf("marketplace: unknown signing key %q", signed.KeyID)
	}
	payload, err := base64.StdEncoding.DecodeString(signed.Payload)
	if err != nil || len(payload) == 0 || len(payload) > maxRemoteCatalogBytes {
		return RemoteCatalog{}, fmt.Errorf("marketplace: invalid or oversized catalog payload")
	}
	sig, err := base64.StdEncoding.DecodeString(signed.Signature)
	if err != nil || !ed25519.Verify(key, payload, sig) {
		return RemoteCatalog{}, fmt.Errorf("marketplace: catalog signature verification failed")
	}
	var catalog RemoteCatalog
	if err := json.Unmarshal(payload, &catalog); err != nil {
		return RemoteCatalog{}, fmt.Errorf("marketplace: signed catalog payload is invalid: %w", err)
	}
	for i := range catalog.Items {
		item := &catalog.Items[i]
		if !validName(item.Name) || item.Version == "" || item.SHA256 == "" {
			return RemoteCatalog{}, fmt.Errorf("marketplace: item %d has invalid name, version, or sha256", i)
		}
		if err := validateHTTPS(item.URL); err != nil {
			return RemoteCatalog{}, fmt.Errorf("marketplace: item %q: %w", item.Name, err)
		}
		if _, err := hex.DecodeString(item.SHA256); err != nil || len(item.SHA256) != sha256.Size*2 {
			return RemoteCatalog{}, fmt.Errorf("marketplace: item %q has invalid sha256", item.Name)
		}
	}
	return catalog, nil
}

func FetchSignedCatalog(client *http.Client, rawURL string) ([]byte, error) {
	if err := validateHTTPS(rawURL); err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return fmt.Errorf("redirects are refused for signed marketplace catalogs")
		}}
	}
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("marketplace: catalog fetch failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("marketplace: catalog returned %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxRemoteCatalogBytes+1))
	if err != nil || len(b) > maxRemoteCatalogBytes {
		return nil, fmt.Errorf("marketplace: catalog exceeds %d-byte limit", maxRemoteCatalogBytes)
	}
	return b, nil
}

func FetchArtifact(client *http.Client, item RemoteItem) ([]byte, error) {
	if err := validateHTTPS(item.URL); err != nil {
		return nil, err
	}
	want, err := hex.DecodeString(strings.ToLower(item.SHA256))
	if err != nil || len(want) != sha256.Size {
		return nil, fmt.Errorf("marketplace: invalid artifact digest")
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return fmt.Errorf("redirects are refused for marketplace artifacts")
		}}
	}
	resp, err := client.Get(item.URL)
	if err != nil {
		return nil, fmt.Errorf("marketplace: artifact fetch failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("marketplace: artifact returned %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxRemoteArtifactBytes+1))
	if err != nil || len(b) > maxRemoteArtifactBytes {
		return nil, fmt.Errorf("marketplace: artifact exceeds %d-byte limit", maxRemoteArtifactBytes)
	}
	got := sha256.Sum256(b)
	if !strings.EqualFold(hex.EncodeToString(got[:]), hex.EncodeToString(want)) {
		return nil, fmt.Errorf("marketplace: artifact digest mismatch")
	}
	return b, nil
}

// Cache stores verified catalog bytes atomically. Callers still verify the
// bytes on every read; the cache is an availability optimization, not trust.
type Cache struct{ Dir string }

func (c Cache) Put(key string, data []byte) error {
	if c.Dir == "" || filepath.Base(key) != key || key == "." || key == ".." {
		return fmt.Errorf("marketplace: invalid cache key")
	}
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.Dir, ".catalog-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(c.Dir, key))
}

func (c Cache) Get(key string) ([]byte, error) {
	if c.Dir == "" || filepath.Base(key) != key || key == "." || key == ".." {
		return nil, fmt.Errorf("marketplace: invalid cache key")
	}
	return os.ReadFile(filepath.Join(c.Dir, key))
}

func validateHTTPS(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("only https URLs without userinfo are allowed")
	}
	return nil
}

func validName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !(r == '-' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
