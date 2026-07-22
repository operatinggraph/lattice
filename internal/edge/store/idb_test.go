//go:build js

package store_test

import (
	"errors"
	"fmt"
	"sync/atomic"
	"syscall/js"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/edge/store"
	"github.com/operatinggraph/lattice/internal/edge/store/storetest"
)

// idbFactory builds IndexedDB-backed stores for the conformance suite. Each
// store gets its own database name, and Reopen reopens that same database — so
// the durability the suite asserts is a real close-and-reopen against the
// browser's storage, the browser analogue of the bbolt factory's file reopen.
type idbFactory struct {
	names map[store.Store]string
	n     atomic.Uint64
}

func (f *idbFactory) New(t *testing.T) store.Store {
	t.Helper()
	name := fmt.Sprintf("edge-conformance-%d", f.n.Add(1))
	// Delete first, so "a fresh, empty store" holds by construction. The test
	// browser happens to hand out a clean profile per run, which would make
	// this look unnecessary — but that is the runner's incidental behaviour,
	// and a suite whose emptiness precondition silently depends on it would
	// start failing on any runner that reuses a profile.
	deleteDB(t, name)
	return f.open(t, name)
}

// deleteDB removes a database outright and waits for the deletion to land.
func deleteDB(t *testing.T, name string) {
	t.Helper()
	req := js.Global().Get("indexedDB").Call("deleteDatabase", name)
	done := make(chan error, 1)

	onSuccess := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		done <- nil
		return nil
	})
	defer onSuccess.Release()
	onError := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		done <- errors.New("deleteDatabase failed")
		return nil
	})
	defer onError.Release()

	req.Set("onsuccess", onSuccess)
	req.Set("onerror", onError)
	require.NoError(t, <-done)
}

func (f *idbFactory) Reopen(t *testing.T, s store.Store) store.Store {
	t.Helper()
	name, ok := f.names[s]
	require.True(t, ok, "Reopen called with a store this factory did not build")
	require.NoError(t, s.Close())
	delete(f.names, s)
	return f.open(t, name)
}

func (f *idbFactory) open(t *testing.T, name string) store.Store {
	t.Helper()
	s, err := store.OpenIDB(name)
	require.NoError(t, err)
	if f.names == nil {
		f.names = map[store.Store]string{}
	}
	f.names[s] = name
	t.Cleanup(func() {
		if _, stillOpen := f.names[s]; stillOpen {
			_ = s.Close()
		}
	})
	return s
}

// TestIDBStore_Conformance is the gate edge-browser-node-design.md §3.3 names:
// the browser host's store passes the same suite the bbolt store does, run
// against a real IndexedDB in a real browser, before the semantics packages are
// pointed at it.
func TestIDBStore_Conformance(t *testing.T) {
	storetest.Run(t, &idbFactory{})
}
