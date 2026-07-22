//go:build !js

package store_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"

	"github.com/operatinggraph/lattice/internal/edge/store"
	"github.com/operatinggraph/lattice/internal/edge/store/storetest"
)

// boltFactory builds bbolt-backed stores for the conformance suite. Each store
// gets its own file under the test's temp dir, and Reopen reopens that same
// file — so the durability the suite asserts is the real on-disk kind, not a
// same-process handle swap.
type boltFactory struct {
	paths map[store.Store]string
}

func (f *boltFactory) New(t *testing.T) store.Store {
	t.Helper()
	return f.open(t, filepath.Join(t.TempDir(), "edge.db"))
}

func (f *boltFactory) Reopen(t *testing.T, s store.Store) store.Store {
	t.Helper()
	path, ok := f.paths[s]
	require.True(t, ok, "Reopen called with a store this factory did not build")
	require.NoError(t, s.Close())
	delete(f.paths, s)
	return f.open(t, path)
}

func (f *boltFactory) open(t *testing.T, path string) store.Store {
	t.Helper()
	s, err := store.Open(path)
	require.NoError(t, err)
	if f.paths == nil {
		f.paths = map[store.Store]string{}
	}
	f.paths[s] = path
	// bbolt errors on a double Close, so only stores still open at test end
	// are closed here — Reopen already closed the ones it replaced.
	t.Cleanup(func() {
		if _, stillOpen := f.paths[s]; stillOpen {
			_ = s.Close()
		}
	})
	return s
}

func TestBoltStore_Conformance(t *testing.T) {
	storetest.Run(t, &boltFactory{})
}

func TestOpen_UnwritablePathFails(t *testing.T) {
	_, err := store.Open(filepath.Join(t.TempDir(), "no-such-dir", "edge.db"))
	require.Error(t, err, "opening under a non-existent directory must fail rather than silently succeed")
}

// TestOpen_PurgesMirrorOnSchemaMismatch pins personal-lens-retraction-
// design.md §3.3's migration: a store written before the Sources-attribution
// schema (every pre-R2 store — no schemaVersion key at all) carries entries
// that cannot be safely diffed against a keyset frame, so Open must purge the
// mirror + cursor rather than mixing pre-attribution entries with new ones.
// Written directly against bbolt (bypassing store.Open) to model a store this
// fire's migration predates — the bucket/key names mirror bolt.go's
// unexported constants ("val"/"meta"/"cursor") since this is an external test
// package.
func TestOpen_PurgesMirrorOnSchemaMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := bbolt.Open(path, 0o600, nil)
	require.NoError(t, err)
	require.NoError(t, db.Update(func(tx *bbolt.Tx) error {
		for _, name := range []string{"val", "local", "meta", "pending", "intents"} {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return err
			}
		}
		val := tx.Bucket([]byte("val"))
		v, err := json.Marshal(map[string]any{"key": "vtx.lease.Lk2Pn6mQrtwzKbcXvP3T", "revision": 7, "data": json.RawMessage(`{"a":1}`)})
		if err != nil {
			return err
		}
		if err := val.Put([]byte("vtx.lease.Lk2Pn6mQrtwzKbcXvP3T"), v); err != nil {
			return err
		}
		cursor, err := json.Marshal(42)
		if err != nil {
			return err
		}
		return tx.Bucket([]byte("meta")).Put([]byte("cursor"), cursor)
	}))
	require.NoError(t, db.Close())

	s, err := store.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	_, ok, err := s.Get("vtx.lease.Lk2Pn6mQrtwzKbcXvP3T")
	require.NoError(t, err)
	require.False(t, ok, "the legacy entry must be purged, not silently kept unattributed")

	_, ok, err = s.Cursor()
	require.NoError(t, err)
	require.False(t, ok, "the cursor must be cleared so the Sync Manager cold-hydrates")

	// A second Open (schema already stamped) must NOT purge again.
	require.NoError(t, s.Close())
	applied, err := func() (bool, error) {
		reopened, err := store.Open(path)
		if err != nil {
			return false, err
		}
		defer reopened.Close()
		return reopened.ApplyUpsert("vtx.lease.Lk2Pn6mQrtwzKbcXvP3T", "", 1, []byte(`{"a":1}`))
	}()
	require.NoError(t, err)
	require.True(t, applied)

	s2, err := store.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	entry, ok, err := s2.Get("vtx.lease.Lk2Pn6mQrtwzKbcXvP3T")
	require.NoError(t, err)
	require.True(t, ok, "a stable-version reopen must not re-purge an entry written after migration")
	require.Equal(t, uint64(1), entry.Revision)
}
