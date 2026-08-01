package adapter

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// scriptedKVStore is a kvStore fake that deterministically scripts a
// revision-conflict sequence on Create/Update, so guardedWrite's
// retry-then-succeed and CAS-exhaustion paths can be exercised without a real
// concurrent-writer race against NATS.
type scriptedKVStore struct {
	getEntry      *substrate.KVEntry
	getErr        error
	conflictsLeft int // Create/Update returns ErrRevisionConflict this many times, then succeeds
	calls         int // total Create+Update calls observed
}

func (s *scriptedKVStore) Get(ctx context.Context, key string) (*substrate.KVEntry, error) {
	return s.getEntry, s.getErr
}

func (s *scriptedKVStore) Create(ctx context.Context, key string, value []byte) (uint64, error) {
	s.calls++
	if s.conflictsLeft > 0 {
		s.conflictsLeft--
		return 0, substrate.ErrRevisionConflict
	}
	return 1, nil
}

func (s *scriptedKVStore) Update(ctx context.Context, key string, value []byte, expectedRevision uint64) (uint64, error) {
	s.calls++
	if s.conflictsLeft > 0 {
		s.conflictsLeft--
		return 0, substrate.ErrRevisionConflict
	}
	return expectedRevision + 1, nil
}

func (s *scriptedKVStore) Put(ctx context.Context, key string, value []byte) (uint64, error) {
	panic("unused by guardedWrite tests")
}
func (s *scriptedKVStore) Delete(ctx context.Context, key string) error {
	panic("unused by guardedWrite tests")
}
func (s *scriptedKVStore) ListKeys(ctx context.Context) ([]string, error) {
	panic("unused by guardedWrite tests")
}
func (s *scriptedKVStore) ListKeysPrefix(ctx context.Context, prefix string) ([]string, error) {
	panic("unused by guardedWrite tests")
}
func (s *scriptedKVStore) Purge(ctx context.Context, key string) error {
	panic("unused by guardedWrite tests")
}
func (s *scriptedKVStore) Status(ctx context.Context) error {
	panic("unused by guardedWrite tests")
}

// getErrStore is a kvStore fake whose Get always fails with a non-
// ErrKeyNotFound error (an infra hiccup, not an absent key), so upsert's
// read-before-write identity probe can be proven to fall through to Put
// rather than block or fail the write — a scenario a real NATS fixture
// cannot deterministically trigger.
type getErrStore struct {
	getErr   error
	putCalls int
}

func (s *getErrStore) Get(ctx context.Context, key string) (*substrate.KVEntry, error) {
	return nil, s.getErr
}
func (s *getErrStore) Create(ctx context.Context, key string, value []byte) (uint64, error) {
	panic("unused by upsert Get-error tests")
}
func (s *getErrStore) Update(ctx context.Context, key string, value []byte, expectedRevision uint64) (uint64, error) {
	panic("unused by upsert Get-error tests")
}
func (s *getErrStore) Put(ctx context.Context, key string, value []byte) (uint64, error) {
	s.putCalls++
	return 1, nil
}
func (s *getErrStore) Delete(ctx context.Context, key string) error {
	panic("unused by upsert Get-error tests")
}
func (s *getErrStore) ListKeys(ctx context.Context) ([]string, error) {
	panic("unused by upsert Get-error tests")
}
func (s *getErrStore) ListKeysPrefix(ctx context.Context, prefix string) ([]string, error) {
	panic("unused by upsert Get-error tests")
}
func (s *getErrStore) Purge(ctx context.Context, key string) error {
	panic("unused by upsert Get-error tests")
}
func (s *getErrStore) Status(ctx context.Context) error {
	panic("unused by upsert Get-error tests")
}

// TestUpsert_GetErrorFallsThroughToPut proves that a failed identity read
// (any error other than the ordinary absent-key case) never blocks or fails
// the write — it just means the skip-if-identical comparison couldn't prove
// identity, so upsert degrades to the unconditional Put every unguarded
// upsert used before this comparison existed.
func TestUpsert_GetErrorFallsThroughToPut(t *testing.T) {
	store := &getErrStore{getErr: errors.New("boom: infra hiccup")}
	a := &NatsKVAdapter{kv: store, keyOrder: []string{"key"}}

	outcome, err := a.upsert(context.Background(), map[string]any{"key": "k1"}, map[string]any{"v": 1}, 0)

	require.NoError(t, err)
	assert.True(t, outcome.Wrote, "a failed identity read must not block the write")
	assert.Equal(t, 1, store.putCalls)
}

func TestGuardedWrite_CreateRetriesThroughRevisionConflictThenSucceeds(t *testing.T) {
	store := &scriptedKVStore{
		getErr:        substrate.ErrKeyNotFound, // key absent -> Create path
		conflictsLeft: 3,
	}
	a := &NatsKVAdapter{kv: store, keyOrder: []string{"key"}, guarded: true}

	err := a.guardedWrite(context.Background(), "k1", map[string]any{"v": 1}, 5, false)

	require.NoError(t, err)
	assert.Equal(t, 4, store.calls, "must retry Create through 3 conflicts then succeed on the 4th attempt")
}

func TestGuardedWrite_UpdateRetriesThroughRevisionConflictThenSucceeds(t *testing.T) {
	store := &scriptedKVStore{
		getEntry:      &substrate.KVEntry{Value: []byte(`{"projectionSeq":1}`), Revision: 10},
		conflictsLeft: 2,
	}
	a := &NatsKVAdapter{kv: store, keyOrder: []string{"key"}, guarded: true}

	err := a.guardedWrite(context.Background(), "k1", map[string]any{"v": 1}, 5, false)

	require.NoError(t, err)
	assert.Equal(t, 3, store.calls, "must retry Update through 2 conflicts then succeed on the 3rd attempt")
}

func TestGuardedWrite_CASExhaustionReturnsError(t *testing.T) {
	store := &scriptedKVStore{
		getErr:        substrate.ErrKeyNotFound,
		conflictsLeft: guardCASMaxAttempts, // never resolves within the attempt budget
	}
	a := &NatsKVAdapter{kv: store, keyOrder: []string{"key"}, guarded: true}

	err := a.guardedWrite(context.Background(), "k1", map[string]any{"v": 1}, 5, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "revision conflict not resolved after 8 attempts")
	assert.Equal(t, guardCASMaxAttempts, store.calls, "must give up exactly at the attempt cap, no extra call")
}

func TestStoredProjectionSeq(t *testing.T) {
	t.Run("empty data", func(t *testing.T) {
		seq, ok := storedProjectionSeq(nil)
		assert.False(t, ok)
		assert.Zero(t, seq)
	})
	t.Run("malformed JSON", func(t *testing.T) {
		seq, ok := storedProjectionSeq([]byte(`{not json`))
		assert.False(t, ok)
		assert.Zero(t, seq)
	})
	t.Run("legacy doc with no projectionSeq field", func(t *testing.T) {
		seq, ok := storedProjectionSeq([]byte(`{"row":{"a":1}}`))
		assert.False(t, ok)
		assert.Zero(t, seq)
	})
	t.Run("valid watermark", func(t *testing.T) {
		seq, ok := storedProjectionSeq([]byte(`{"projectionSeq":42}`))
		assert.True(t, ok)
		assert.Equal(t, uint64(42), seq)
	})
	t.Run("zero watermark", func(t *testing.T) {
		seq, ok := storedProjectionSeq([]byte(`{"projectionSeq":0}`))
		assert.True(t, ok)
		assert.Zero(t, seq)
	})
	t.Run("negative watermark treated as malformed, not wrapped", func(t *testing.T) {
		// A hand-corrupted or pre-guard doc could carry a negative value; the
		// float64->uint64 conversion would otherwise wrap to a bogus near-max
		// value that permanently no-ops every future write to the key.
		seq, ok := storedProjectionSeq([]byte(`{"projectionSeq":-1}`))
		assert.False(t, ok)
		assert.Zero(t, seq)
	})
	t.Run("non-numeric watermark", func(t *testing.T) {
		seq, ok := storedProjectionSeq([]byte(`{"projectionSeq":"not-a-number"}`))
		assert.False(t, ok)
		assert.Zero(t, seq)
	})
	t.Run("null watermark", func(t *testing.T) {
		seq, ok := storedProjectionSeq([]byte(`{"projectionSeq":null}`))
		assert.False(t, ok)
		assert.Zero(t, seq)
	})
}
