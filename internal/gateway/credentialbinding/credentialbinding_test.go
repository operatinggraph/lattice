package credentialbinding

import (
	"context"
	"errors"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// *substrate.KV must satisfy the read surface Resolve needs — the real
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

const rawActor = "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y"
const claimedIdentity = "vtx.identity.U9nbCxz5vQ2yHj4kPmRt"

func TestResolve_Bound(t *testing.T) {
	kv := &fakeKV{entry: &substrate.KVEntry{Value: []byte(`{"identityKey":"` + claimedIdentity + `"}`)}}
	identityKey, bound, err := New(kv).Resolve(context.Background(), rawActor)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !bound {
		t.Error("bound = false, want true (binding present)")
	}
	if identityKey != claimedIdentity {
		t.Errorf("identityKey = %q, want %q", identityKey, claimedIdentity)
	}
	if kv.gotKey != rawActor {
		t.Errorf("looked up %q, want %q", kv.gotKey, rawActor)
	}
}

func TestResolve_Unbound(t *testing.T) {
	kv := &fakeKV{err: substrate.ErrKeyNotFound}
	identityKey, bound, err := New(kv).Resolve(context.Background(), rawActor)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if bound {
		t.Error("bound = true, want false (no binding yet)")
	}
	if identityKey != "" {
		t.Errorf("identityKey = %q, want empty", identityKey)
	}
}

func TestResolve_KVError(t *testing.T) {
	kvErr := errors.New("connection refused")
	kv := &fakeKV{err: kvErr}
	_, bound, err := New(kv).Resolve(context.Background(), rawActor)
	if err == nil {
		t.Fatal("Resolve: want error on KV failure, got nil")
	}
	if !errors.Is(err, kvErr) {
		t.Errorf("error = %v, want wrapped %v", err, kvErr)
	}
	if bound {
		t.Error("bound = true on error, want false")
	}
}

func TestResolve_MalformedDocument(t *testing.T) {
	kv := &fakeKV{entry: &substrate.KVEntry{Value: []byte(`not-json`)}}
	_, bound, err := New(kv).Resolve(context.Background(), rawActor)
	if err == nil {
		t.Fatal("Resolve: want error on malformed document, got nil")
	}
	if bound {
		t.Error("bound = true on malformed document, want false")
	}
}

func TestResolve_MissingIdentityKey(t *testing.T) {
	kv := &fakeKV{entry: &substrate.KVEntry{Value: []byte(`{}`)}}
	_, bound, err := New(kv).Resolve(context.Background(), rawActor)
	if err == nil {
		t.Fatal("Resolve: want error when identityKey is empty, got nil")
	}
	if bound {
		t.Error("bound = true with empty identityKey, want false")
	}
}
