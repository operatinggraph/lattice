package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// guardedNoTruncAdapter is the grant family's shape: guarded by the SQL it always
// issues, and deliberately implementing no Truncater because it shares one table
// with every other producer.
type guardedNoTruncAdapter struct{ noFrameTarget }

func (guardedNoTruncAdapter) Guarded() bool { return true }

func reactivationPipeline(t *testing.T, ruleID string, adpt adapter.Adapter) *Pipeline {
	t.Helper()
	p, err := New(ruleID, "nats_kv", "CORE", nil, nil, adpt, nil)
	require.NoError(t, err)
	return p
}

// TestTruncateForReactivation_GuardedTargetPurgesWithoutBeingAsked is the force
// rule at this seam. A guarded target's §6.2 watermark declines a replayed write
// at or below the seq it already stores, so the activation that follows would
// re-derive the new shape into a target that refuses it — the edit would look
// applied and change nothing.
func TestTruncateForReactivation_GuardedTargetPurgesWithoutBeingAsked(t *testing.T) {
	ad := &guardedTruncAdapter{guarded: true}
	p := reactivationPipeline(t, "rule-reactivate-guarded", ad)

	purged, err := p.TruncateForReactivation(context.Background(), false)

	require.NoError(t, err)
	assert.True(t, purged)
	assert.True(t, ad.truncated,
		"a guarded target must be cleared or the replay's new shape never lands on top of the old rows")
}

// An unguarded target overwrites in place, so it is cleared only for the reason
// the caller names: the new spec addresses different keys, and nothing will ever
// deliver an event that retracts the old ones.
func TestTruncateForReactivation_UnguardedTargetPurgesOnlyWhenTheKeyShapeMoved(t *testing.T) {
	t.Run("key shape moved", func(t *testing.T) {
		ad := &guardedTruncAdapter{}
		p := reactivationPipeline(t, "rule-reactivate-keyshape", ad)

		purged, err := p.TruncateForReactivation(context.Background(), true)

		require.NoError(t, err)
		assert.True(t, purged)
		assert.True(t, ad.truncated, "the old keys are unaddressable by the new lens; only the purge retracts them")
	})

	t.Run("same key shape", func(t *testing.T) {
		ad := &guardedTruncAdapter{}
		p := reactivationPipeline(t, "rule-reactivate-samekeys", ad)

		purged, err := p.TruncateForReactivation(context.Background(), false)

		require.NoError(t, err)
		assert.False(t, purged)
		assert.False(t, ad.truncated,
			"a content-only edit is re-derived over the same keys — a purge would open an absence window for nothing")
	})
}

// TestTruncateForReactivation_UndrivablePurgeDeclines covers the two DIFFERENT
// reasons a purge is declined — a target that cannot be truncated at all, and one
// that can but is not confined to this lens's own keys — each asked with the
// strongest input that could have forced it.
func TestTruncateForReactivation_UndrivablePurgeDeclines(t *testing.T) {
	t.Run("confined to nothing: a nats-kv target with no bound key prefix", func(t *testing.T) {
		ad, err := adapter.New(nil, []string{"key"}, adapter.DeleteModeHard)
		require.NoError(t, err)
		ad.SetGuarded(true)
		require.Empty(t, ad.KeyPrefix(), "precondition: nothing scoped this adapter to the lens's own keys")

		p := reactivationPipeline(t, "rule-reactivate-unscoped", ad)

		purged, err := p.TruncateForReactivation(context.Background(), true)

		require.NoError(t, err, "an unconfined target is declined, not an error — the lens still re-activates")
		assert.False(t, purged, "purging an unscoped bucket is a wipe of every other producer's rows")
	})

	t.Run("untruncatable: a guarded target shared with other producers", func(t *testing.T) {
		ad := guardedNoTruncAdapter{}
		p := reactivationPipeline(t, "rule-reactivate-untruncatable", ad)

		purged, err := p.TruncateForReactivation(context.Background(), true)

		require.NoError(t, err)
		assert.False(t, purged, "a target that cannot be cleared must not report that it was")
	})
}

// TestTruncateForReactivation_AnnouncesEveryPurgedActor: the purge is a bulk
// revocation on a cap-read producer, and it is the one write path that never
// reaches the per-key guard — so without the announcement the grant-change edge
// would go silent on the operation that withdraws the most at once.
func TestTruncateForReactivation_AnnouncesEveryPurgedActor(t *testing.T) {
	adpt := &prefixedPurgingTruncater{purgingTruncater: purgingTruncater{failAfter: -1, keys: []string{
		"cap-read.identity.Hj4kPmRtw9nbCxz5vQ2y.Zwq9PmRtw3nbCxz5vQ2y",
		"cap-read.identity.Kx3TmZpq7RvwNsY2Hc9L.Zwq9PmRtw3nbCxz5vQ2y",
	}}}
	sink := &recordingGrantSink{}
	p := reactivationPipeline(t, "cap-read-producer", adpt)
	p.SetGrantChangeSink(sink, capReadAnchorFromKey)

	purged, err := p.TruncateForReactivation(context.Background(), true)

	require.NoError(t, err)
	require.True(t, purged)
	assert.Equal(t, []string{
		"vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
		"vtx.identity.Kx3TmZpq7RvwNsY2Hc9L",
	}, sink.actors, "every actor the purge withdrew grants from is owed a re-evaluation")
}

// prefixedPurgingTruncater is the announcing truncater confined to the lens's own
// key prefix — the shape projection.ApplyTruncateScope produces, and the only one
// this seam will actually purge.
type prefixedPurgingTruncater struct{ purgingTruncater }

func (a *prefixedPurgingTruncater) KeyPrefix() string { return "cap-read." }
