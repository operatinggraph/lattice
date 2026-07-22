package revocation

import (
	"context"
	"errors"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// *substrate.KV must satisfy the read surface the Checker needs — the real
// wiring (Conn.OpenKV → Checker) is a compile-time guarantee, so the unit tests
// below can use a fake without drifting from production.
var _ kvGetter = (*substrate.KV)(nil)

type fakeKV struct {
	entry *substrate.KVEntry
	err   error
	gotKey string
}

func (f *fakeKV) Get(_ context.Context, key string) (*substrate.KVEntry, error) {
	f.gotKey = key
	return f.entry, f.err
}

const actor = "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y"

func TestIsRevoked_Present(t *testing.T) {
	// A key present under any value means revoked.
	kv := &fakeKV{entry: &substrate.KVEntry{}}
	revoked, err := New(kv).IsRevoked(context.Background(), actor)
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if !revoked {
		t.Error("revoked = false, want true (key present)")
	}
	if kv.gotKey != actor {
		t.Errorf("looked up %q, want %q", kv.gotKey, actor)
	}
}

func TestIsRevoked_Absent(t *testing.T) {
	// An absent key (ErrKeyNotFound) means the actor is live.
	kv := &fakeKV{err: substrate.ErrKeyNotFound}
	revoked, err := New(kv).IsRevoked(context.Background(), actor)
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if revoked {
		t.Error("revoked = true, want false (key absent)")
	}
}

func TestIsRevoked_KVError(t *testing.T) {
	// A transport/KV failure surfaces as an error so the caller fails closed.
	kvErr := errors.New("connection refused")
	kv := &fakeKV{err: kvErr}
	revoked, err := New(kv).IsRevoked(context.Background(), actor)
	if err == nil {
		t.Fatal("IsRevoked: want error on KV failure, got nil")
	}
	if !errors.Is(err, kvErr) {
		t.Errorf("error = %v, want wrapped %v", err, kvErr)
	}
	if revoked {
		t.Error("revoked = true on error, want false")
	}
}
