package store_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/edge/store"
	"github.com/asolgan/lattice/internal/edge/store/storetest"
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
