package marketplace

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestVerifySignedCatalogAndArtifactDigest(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	payload, _ := json.Marshal(RemoteCatalog{Items: []RemoteItem{{Name: "browser-review", Version: "1.0.0", URL: "https://example.test/review.zip", SHA256: stringsDigest("artifact")}}})
	envelope, _ := json.Marshal(SignedCatalog{KeyID: "test", Payload: base64.StdEncoding.EncodeToString(payload), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))})
	catalog, err := VerifySignedCatalog(envelope, map[string]ed25519.PublicKey{"test": pub})
	if err != nil || len(catalog.Items) != 1 {
		t.Fatalf("valid signed catalog rejected: %+v %v", catalog, err)
	}
	bad := append([]byte(nil), envelope...)
	bad[len(bad)-3] ^= 1
	if _, err := VerifySignedCatalog(bad, map[string]ed25519.PublicKey{"test": pub}); err == nil {
		t.Fatal("tampered catalog must fail verification")
	}
}

func TestRemoteValidationRejectsUnsafeURLs(t *testing.T) {
	if _, err := FetchSignedCatalog(nil, "http://example.test/catalog.json"); err == nil {
		t.Fatal("http catalog must be rejected")
	}
	if _, err := VerifySignedCatalog([]byte(`{"key_id":"x","payload":"","signature":""}`), nil); err == nil {
		t.Fatal("unknown key must be rejected")
	}
}

func stringsDigest(s string) string {
	d := sha256.Sum256([]byte(s))
	return hex.EncodeToString(d[:])
}
