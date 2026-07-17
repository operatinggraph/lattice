package vault

import (
	"context"
	"testing"
)

// TestLocalBackendStats verifies the operational-counter seam the Processor's
// health.vault heartbeat reads (design D — the Vault's own Health-KV group):
// call counters are cumulative and the gauges track the cache/deny-list.
func TestLocalBackendStats(t *testing.T) {
	ctx := context.Background()
	kek := make([]byte, dekKeySize)
	for i := range kek {
		kek[i] = byte(i)
	}
	b, err := NewLocalBackend(kek, "v1")
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}

	if got := b.Stats(); got != (Stats{Backend: LocalBackendName}) {
		t.Fatalf("fresh backend Stats = %+v, want zero-valued with backend=%q", got, LocalBackendName)
	}

	const id = "vtx.identity.V1StaShredStats0"
	env, err := b.CreateIdentityKey(ctx, id)
	if err != nil {
		t.Fatalf("CreateIdentityKey: %v", err)
	}

	ct, err := b.Encrypt(ctx, id, env, []byte("123-45-6789"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := b.Decrypt(ctx, id, env, ct); err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	s := b.Stats()
	if s.Backend != LocalBackendName {
		t.Errorf("Backend = %q, want %q", s.Backend, LocalBackendName)
	}
	if s.EncryptCalls != 1 {
		t.Errorf("EncryptCalls = %d, want 1", s.EncryptCalls)
	}
	if s.DecryptCalls != 1 {
		t.Errorf("DecryptCalls = %d, want 1", s.DecryptCalls)
	}
	// Encrypt+Decrypt both unwrap the same identity's DEK; the second hits the
	// cache, so exactly one entry is cached.
	if s.DEKCacheSize != 1 {
		t.Errorf("DEKCacheSize = %d, want 1", s.DEKCacheSize)
	}
	if s.ShredCalls != 0 || s.ShreddedCount != 0 {
		t.Errorf("pre-shred: ShredCalls=%d ShreddedCount=%d, want 0/0", s.ShredCalls, s.ShreddedCount)
	}

	if err := b.ShredKey(ctx, id); err != nil {
		t.Fatalf("ShredKey: %v", err)
	}
	s = b.Stats()
	if s.ShredCalls != 1 {
		t.Errorf("ShredCalls = %d, want 1", s.ShredCalls)
	}
	if s.ShreddedCount != 1 {
		t.Errorf("ShreddedCount = %d, want 1", s.ShreddedCount)
	}
	// The shred evicts the identity's cached DEK.
	if s.DEKCacheSize != 0 {
		t.Errorf("post-shred DEKCacheSize = %d, want 0", s.DEKCacheSize)
	}

	// A post-shred decrypt still counts as a call, and still refuses.
	if _, err := b.Decrypt(ctx, id, env, ct); err != ErrKeyShredded {
		t.Fatalf("post-shred Decrypt err = %v, want ErrKeyShredded", err)
	}
	if got := b.Stats().DecryptCalls; got != 2 {
		t.Errorf("DecryptCalls after refused call = %d, want 2", got)
	}
}

// TestLocalBackend_MAC_NilMacKeysMapDoesNotPanic proves MAC derives correctly
// even for a LocalBackend assembled via a struct literal that skips
// NewLocalBackend and so never initializes macKeys — a defensive guard, not
// a supported construction path (NewLocalBackend remains the only sanctioned
// constructor), but one bad struct literal must not panic the process on
// "assignment to entry in nil map."
func TestLocalBackend_MAC_NilMacKeysMapDoesNotPanic(t *testing.T) {
	b := &LocalBackend{kek: make([]byte, dekKeySize), kekVersion: "v1"}
	mac, err := b.MAC(context.Background(), "purpose", []byte("data"))
	if err != nil {
		t.Fatalf("MAC on a nil macKeys map: %v", err)
	}
	if len(mac) == 0 {
		t.Fatalf("MAC returned an empty result")
	}
}
