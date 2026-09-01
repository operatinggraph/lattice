package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func writeTestPEM(t *testing.T, dir, name string) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa keygen: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	block := &pem.Block{Type: "PUBLIC KEY", Bytes: der}
	if err := os.WriteFile(filepath.Join(dir, name), pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestLoadTrustedKeys_EmptyConfigReturnsEmptyMap(t *testing.T) {
	keys, specs, err := LoadTrustedKeys(KeySourceConfig{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 0 || len(specs) != 0 {
		t.Fatalf("got %d keys / %d specs, want 0/0 (the legitimate Fire-1 opt-out shape)", len(keys), len(specs))
	}
}

func TestLoadTrustedKeys_DirWithKeysLoadsThem(t *testing.T) {
	dir := t.TempDir()
	writeTestPEM(t, dir, "idp-key-1.pem")
	writeTestPEM(t, dir, "idp-key-2.pem")

	keys, specs, err := LoadTrustedKeys(KeySourceConfig{KeysDir: dir, KeysDirIssuer: testIss}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("got %d keys, want 2", len(keys))
	}
	if _, ok := keys["idp-key-1"]; !ok {
		t.Error("missing kid idp-key-1")
	}
	if _, ok := keys["idp-key-2"]; !ok {
		t.Error("missing kid idp-key-2")
	}
	for _, kid := range []string{"idp-key-1", "idp-key-2"} {
		if got := specs[kid]; got.Mode != ModeOpaque || got.Issuer != testIss {
			t.Errorf("specs[%q] = %+v, want ModeOpaque pinned to %q", kid, got, testIss)
		}
	}
}

// TestLoadTrustedKeys_KeysDirWithNoIssuerErrors — a configured KeysDir with
// no declared KeysDirIssuer refuses to load: a configured external source
// MUST pin an expected iss (Contract #11 §11.3, finding A8).
func TestLoadTrustedKeys_KeysDirWithNoIssuerErrors(t *testing.T) {
	dir := t.TempDir()
	writeTestPEM(t, dir, "idp-key-1.pem")

	_, _, err := LoadTrustedKeys(KeySourceConfig{KeysDir: dir}, nil)
	if err == nil {
		t.Fatal("expected an error for a KeysDir configured with no KeysDirIssuer")
	}
}

// TestLoadTrustedKeys_ExplicitDirWithNoPEMsErrors is the fix for the 3-layer
// review finding (Blind Hunter + Edge Case Hunter, Fire 2): an explicitly
// configured KeysDir that scans to zero <kid>.pem files must be a startup
// error, never a silent empty map — the caller cannot otherwise distinguish
// "misconfigured trust root" from "JWT verification was never opted into."
func TestLoadTrustedKeys_ExplicitDirWithNoPEMsErrors(t *testing.T) {
	dir := t.TempDir()
	// Wrong extension — the classic "key-sync sidecar drops .pub files"
	// misconfiguration the review flagged.
	if err := os.WriteFile(filepath.Join(dir, "idp-key-1.pub"), []byte("not a pem"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, _, err := LoadTrustedKeys(KeySourceConfig{KeysDir: dir, KeysDirIssuer: testIss}, nil)
	if err == nil {
		t.Fatal("expected an error for an explicitly configured but empty-of-.pem-files keys dir")
	}
}

func TestLoadTrustedKeys_ExplicitEmptyDirErrors(t *testing.T) {
	dir := t.TempDir()
	_, _, err := LoadTrustedKeys(KeySourceConfig{KeysDir: dir, KeysDirIssuer: testIss}, nil)
	if err == nil {
		t.Fatal("expected an error for an explicitly configured, entirely empty keys dir")
	}
}

func TestLoadTrustedKeys_DevModeAddsDevKey(t *testing.T) {
	warned := false
	keys, specs, err := LoadTrustedKeys(KeySourceConfig{
		DevMode:    true,
		DevKeyPath: "../../../deploy/gateway-dev-key/dev-public.pem",
	}, func(string) { warned = true })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := keys[DevKeyID]; !ok {
		t.Fatal("dev key not loaded under DevKeyID")
	}
	if got := specs[DevKeyID]; got.Mode != ModeNanoID {
		t.Errorf("specs[DevKeyID] = %+v, want ModeNanoID", got)
	}
	if !warned {
		t.Error("expected the dev-mode warn callback to fire")
	}
}

// TestLoadTrustedKeys_DevKidCollisionErrors is the fix for the Edge Case
// Hunter finding: a <kid>.pem in KeysDir literally named "dev.pem" would
// otherwise be silently shadowed by the checked-in dev key.
func TestLoadTrustedKeys_DevKidCollisionErrors(t *testing.T) {
	dir := t.TempDir()
	writeTestPEM(t, dir, DevKeyID+".pem")

	_, _, err := LoadTrustedKeys(KeySourceConfig{
		KeysDir:       dir,
		KeysDirIssuer: testIss,
		DevMode:       true,
		DevKeyPath:    "../../../deploy/gateway-dev-key/dev-public.pem",
	}, nil)
	if err == nil {
		t.Fatal("expected an error when a scanned key's kid collides with the reserved dev kid")
	}
}
