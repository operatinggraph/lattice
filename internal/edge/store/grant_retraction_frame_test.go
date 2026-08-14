//go:build !js

package store_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/edge/store"
)

// TestGrantRetractionFrame_RevisionPostureDecidesWhetherARevocationPrunes is
// the client half of T5 (personal-lens-grant-change-trigger-design.md §10/§4.1.1).
//
// A grant-triggered frame is a RETRACTION, and a retraction has to survive two
// guards that a cold snapshot never has to think about:
//
//   - the per-lens frame high-water mark, which drops a whole frame whose
//     revision is below the last applied one; and
//   - collectAttributed, which exempts from pruning any key whose stored
//     attribution revision EXCEEDS the frame's.
//
// Both fail in the OVER-GRANT direction for a frame that under-claims: the
// revoked key is either never examined or the whole frame is discarded, and the
// stale row survives on the device. This is why the server captures the
// revision AFTER the reprojection rather than copying Hydrate's capture-before.
//
// The revisions here are the ones the server-side half of T5 produces: the
// revoked row was written by a live delta at 140, capture-before would frame at
// 100, capture-after frames at 140.
func TestGrantRetractionFrame_RevisionPostureDecidesWhetherARevocationPrunes(t *testing.T) {
	const (
		lens = "edgeTasks"
		// The revoked row's own attribution revision — the live delta that
		// wrote it, which acked while the reprojection was still evaluating.
		rowRev = uint64(140)
		// What capture-before (Hydrate's posture) would have claimed.
		underClaimed = uint64(100)
	)
	const revokedKey = "manifest.lease-revoked"
	const keptKey = "manifest.lease-kept"

	seed := func(t *testing.T) store.Store {
		t.Helper()
		s, err := store.Open(filepath.Join(t.TempDir(), "edge.db"))
		require.NoError(t, err)
		t.Cleanup(func() { _ = s.Close() })
		for _, k := range []string{revokedKey, keptKey} {
			applied, err := s.ApplyUpsert(k, lens, rowRev, json.RawMessage(`{"rent":1800}`))
			require.NoError(t, err)
			require.True(t, applied)
		}
		return s
	}

	t.Run("a frame at the post-evaluation revision actually prunes the revoked key", func(t *testing.T) {
		s := seed(t)

		pruned, applied, err := s.ApplyKeySet(lens, rowRev, []string{keptKey})

		require.NoError(t, err)
		require.True(t, applied, "the frame must be applied, not dropped by the high-water guard")
		assert.Equal(t, []string{revokedKey}, pruned, "the revocation must actually prune")

		gone, ok, err := s.Get(revokedKey)
		require.NoError(t, err)
		require.True(t, ok)
		assert.True(t, gone.Deleted, "the revoked row must be tombstoned on the device")
		still, ok, err := s.Get(keptKey)
		require.NoError(t, err)
		require.True(t, ok)
		assert.False(t, still.Deleted, "a key the frame still names survives")
	})

	t.Run("an under-claiming frame silently leaves the revoked grant live", func(t *testing.T) {
		s := seed(t)

		pruned, applied, err := s.ApplyKeySet(lens, underClaimed, []string{keptKey})

		require.NoError(t, err)
		require.True(t, applied, "the frame is applied — this failure is not loud")
		assert.Empty(t, pruned,
			"collectAttributed exempts a key whose attribution revision exceeds the frame's, so a capture-before frame retracts nothing")

		survived, ok, err := s.Get(revokedKey)
		require.NoError(t, err)
		require.True(t, ok)
		assert.False(t, survived.Deleted,
			"THIS is the over-grant that capture-after exists to prevent: a revoked row still readable on the device")
	})

	t.Run("a frame below the last applied high-water is dropped whole", func(t *testing.T) {
		s := seed(t)

		_, applied, err := s.ApplyKeySet(lens, rowRev, []string{revokedKey, keptKey})
		require.NoError(t, err)
		require.True(t, applied)

		pruned, applied, err := s.ApplyKeySet(lens, underClaimed, []string{keptKey})

		require.NoError(t, err)
		assert.False(t, applied, "the second guard: an older frame is discarded entirely")
		assert.Empty(t, pruned)
		survived, ok, err := s.Get(revokedKey)
		require.NoError(t, err)
		require.True(t, ok)
		assert.False(t, survived.Deleted, "the whole-frame drop is the other way an under-claiming retraction fails open")
	})

	t.Run("a genuinely fresher live delta still wins over the frame", func(t *testing.T) {
		s := seed(t)

		pruned, applied, err := s.ApplyKeySet(lens, rowRev, []string{keptKey})
		require.NoError(t, err)
		require.True(t, applied)
		require.Equal(t, []string{revokedKey}, pruned)

		// The mirror-image cost of capturing after: a live evaluation that
		// wrote at a higher sequence but had not yet framed. It is
		// under-display, not over-grant, and the very next delta recovers it —
		// which is what this asserts.
		applied, err = s.ApplyUpsert(revokedKey, lens, rowRev+1, json.RawMessage(`{"rent":1900}`))
		require.NoError(t, err)
		require.True(t, applied, "a fresher live delta must land over a frame's tombstone")

		back, ok, err := s.Get(revokedKey)
		require.NoError(t, err)
		require.True(t, ok)
		assert.False(t, back.Deleted, "the fresher delta wins")
		assert.Equal(t, rowRev+1, back.Revision)
	})
}
