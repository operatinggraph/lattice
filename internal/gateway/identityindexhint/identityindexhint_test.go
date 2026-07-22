package identityindexhint

import (
	"context"
	"errors"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// *substrate.KV must satisfy the read surface Lookup needs — the real
// wiring (Conn.OpenKV → Resolver) is a compile-time guarantee, so the unit
// tests below can use a fake without drifting from production.
var _ kvGetter = (*substrate.KV)(nil)

type fakeKV struct {
	entry  *substrate.KVEntry
	err    error
	gotKey string
}

func (f *fakeKV) Get(_ context.Context, key string) (*substrate.KVEntry, error) {
	f.gotKey = key
	return f.entry, f.err
}

const indexKey = "vtx.identityindex.Hj4kPmRtw9nbCxz5vQ2y"
const matchedIdentity = "vtx.identity.U9nbCxz5vQ2yHj4kPmRt"

func TestLookup_Found(t *testing.T) {
	kv := &fakeKV{entry: &substrate.KVEntry{Value: []byte(`{"identityKey":"` + matchedIdentity + `"}`)}}
	identityKey, found, err := New(kv).Lookup(context.Background(), indexKey)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !found {
		t.Error("found = false, want true (hint present)")
	}
	if identityKey != matchedIdentity {
		t.Errorf("identityKey = %q, want %q", identityKey, matchedIdentity)
	}
	if kv.gotKey != indexKey {
		t.Errorf("looked up %q, want %q", kv.gotKey, indexKey)
	}
}

func TestLookup_NotFound(t *testing.T) {
	kv := &fakeKV{err: substrate.ErrKeyNotFound}
	identityKey, found, err := New(kv).Lookup(context.Background(), indexKey)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if found {
		t.Error("found = true, want false (no hint)")
	}
	if identityKey != "" {
		t.Errorf("identityKey = %q, want empty", identityKey)
	}
}

func TestLookup_KVError(t *testing.T) {
	kvErr := errors.New("connection refused")
	kv := &fakeKV{err: kvErr}
	_, found, err := New(kv).Lookup(context.Background(), indexKey)
	if err == nil {
		t.Fatal("Lookup: want error on KV failure, got nil")
	}
	if !errors.Is(err, kvErr) {
		t.Errorf("error = %v, want wrapped %v", err, kvErr)
	}
	if found {
		t.Error("found = true on error, want false")
	}
}

func TestLookup_MalformedDocument(t *testing.T) {
	kv := &fakeKV{entry: &substrate.KVEntry{Value: []byte(`not-json`)}}
	_, found, err := New(kv).Lookup(context.Background(), indexKey)
	if err == nil {
		t.Fatal("Lookup: want error on malformed document, got nil")
	}
	if found {
		t.Error("found = true on malformed document, want false")
	}
}

func TestLookup_MissingIdentityKey(t *testing.T) {
	kv := &fakeKV{entry: &substrate.KVEntry{Value: []byte(`{}`)}}
	_, found, err := New(kv).Lookup(context.Background(), indexKey)
	if err == nil {
		t.Fatal("Lookup: want error when identityKey is empty, got nil")
	}
	if found {
		t.Error("found = true with empty identityKey, want false")
	}
}
