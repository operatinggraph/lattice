package health_test

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// startHealthKV starts an in-memory NATS server and returns a KV bucket for health entries.
func startHealthKV(t *testing.T) *substrate.KV {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	_, nc := natsfixture.Server(t)

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	_, err = js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: "HEALTH"})
	require.NoError(t, err)
	kv, err := conn.OpenKV(context.Background(), "HEALTH")
	require.NoError(t, err)
	return kv
}

func TestReporter_GetStatus_FreshKV(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "active", entry.Status, "no entry → treat as active")
	assert.Nil(t, entry.PauseReason)
}

func TestReporter_SetActive(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	require.NoError(t, r.SetActive(context.Background()))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "active", entry.Status)
	assert.Nil(t, entry.PauseReason)
	assert.Nil(t, entry.LastError)
	assert.Equal(t, "my-rule", entry.RuleID)
	assert.Equal(t, uint64(0), entry.ErrorCount)
	assert.NotEmpty(t, entry.LastUpdated, "LastUpdated must be set")
}

func TestReporter_SetPaused_Infra(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	require.NoError(t, r.SetPaused(context.Background(), "infra", "nats: connection closed"))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "paused", entry.Status)
	require.NotNil(t, entry.PauseReason)
	assert.Equal(t, "infra", *entry.PauseReason)
	require.NotNil(t, entry.LastError)
	assert.Equal(t, "nats: connection closed", *entry.LastError)
	assert.Equal(t, "my-rule", entry.RuleID)
}

func TestReporter_SetPaused_Structural(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	require.NoError(t, r.SetPaused(context.Background(), "structural", "bucket not found"))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "paused", entry.Status)
	require.NotNil(t, entry.PauseReason)
	assert.Equal(t, "structural", *entry.PauseReason)
}

func TestReporter_SetPaused_ThenSetActive(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	require.NoError(t, r.SetPaused(context.Background(), "infra", "connection lost"))
	require.NoError(t, r.SetActive(context.Background()))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "active", entry.Status)
	assert.Nil(t, entry.PauseReason)
	assert.Nil(t, entry.LastError)
}

func TestReporter_LastUpdated_IsRFC3339(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")
	require.NoError(t, r.SetActive(context.Background()))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)

	_, parseErr := time.Parse(time.RFC3339, entry.LastUpdated)
	assert.NoError(t, parseErr, "LastUpdated must be valid RFC3339")
}

// TestReporter_SetActive_PreservesErrorCount verifies that calling SetActive after errors
// does NOT reset the cumulative error count (NFR4 / AC4).
func TestReporter_SetActive_PreservesErrorCount(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	require.NoError(t, r.RecordError(context.Background(), "first error"))
	require.NoError(t, r.RecordError(context.Background(), "second error"))

	// Simulate rule recovery — SetActive should NOT reset errorCount.
	require.NoError(t, r.SetActive(context.Background()))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "active", entry.Status)
	assert.Equal(t, uint64(2), entry.ErrorCount, "errorCount must survive SetActive")
}

// TestReporter_RecordError_IncrementsCount verifies that each RecordError call
// increments errorCount by exactly 1 and updates lastError.
func TestReporter_RecordError_IncrementsCount(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	require.NoError(t, r.RecordError(context.Background(), "first"))
	require.NoError(t, r.RecordError(context.Background(), "second"))
	require.NoError(t, r.RecordError(context.Background(), "third error"))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(3), entry.ErrorCount)
	require.NotNil(t, entry.LastError)
	assert.Equal(t, "third error", *entry.LastError)
}

// TestReporter_SetActive_ClearsLastError verifies that SetActive sets lastError and
// pauseReason to null even if errors were previously recorded.
func TestReporter_SetActive_ClearsLastError(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	require.NoError(t, r.RecordError(context.Background(), "boom"))

	require.NoError(t, r.SetActive(context.Background()))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "active", entry.Status)
	assert.Nil(t, entry.LastError, "lastError must be null after SetActive")
	assert.Nil(t, entry.PauseReason, "pauseReason must be null after SetActive")
	assert.Equal(t, uint64(1), entry.ErrorCount, "errorCount must be preserved")
}

// TestReporter_SetRuleSequence_AppearsInEntry verifies that SetRuleSequence caches
// the sequence and it appears in the next health write.
func TestReporter_SetRuleSequence_AppearsInEntry(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	r.SetRuleSequence(42)
	require.NoError(t, r.SetActive(context.Background()))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(42), entry.ActiveSequence)
}

// TestReporter_SetConsumerLag verifies SetConsumerLag updates the lag field and
// that it is preserved by SetActive.
func TestReporter_SetConsumerLag(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	// First, establish an active entry.
	require.NoError(t, r.SetActive(context.Background()))

	// Update lag.
	require.NoError(t, r.SetConsumerLag(context.Background(), 100))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(100), entry.ConsumerLag)

	// SetActive should preserve the consumer lag.
	require.NoError(t, r.SetActive(context.Background()))
	entry, err = r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(100), entry.ConsumerLag, "consumerLag must be preserved by SetActive")
}

// TestReporter_SetProjectionProgress_RoundTrips verifies SetProjectionProgress
// writes ConsumerLag/ProjectionLag (same value, both names), LastProjectedAt,
// LagProgressAt, and the AckPending/AckFloorProgressAt pair, while preserving
// ErrorCount across the read-modify-write
// (lens-projection-liveness-design.md §3.2, lens-consumer-ack-window-design.md §4).
func TestReporter_SetProjectionProgress_RoundTrips(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	require.NoError(t, r.SetActive(context.Background()))
	require.NoError(t, r.RecordError(context.Background(), "boom"))

	projectedAt := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	progressAt := time.Date(2026, 7, 3, 11, 59, 0, 0, time.UTC)
	ackFloorAt := time.Date(2026, 7, 3, 11, 58, 0, 0, time.UTC)
	require.NoError(t, r.SetProjectionProgress(context.Background(), 42, projectedAt, progressAt, 9, ackFloorAt))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(42), entry.ConsumerLag, "legacy consumerLag field must still be set")
	assert.Equal(t, uint64(42), entry.ProjectionLag, "operator-facing projectionLag alias must match")
	assert.Equal(t, projectedAt.Format(time.RFC3339), entry.LastProjectedAt)
	assert.Equal(t, progressAt.Format(time.RFC3339), entry.LagProgressAt)
	assert.Equal(t, uint64(9), entry.AckPending, "ackPending must be written — it is the term consumerLag cannot see")
	assert.Equal(t, ackFloorAt.Format(time.RFC3339), entry.AckFloorProgressAt)
	assert.Equal(t, uint64(1), entry.ErrorCount, "errorCount must be preserved across the read-modify-write")
}

// TestReporter_SetProjectionProgress_ZeroTimeLeavesExisting verifies that a zero
// lastProjectedAt or lagProgressAt (no advance observed this cycle) does not
// blank an already-stored value — only a genuine advance should ever be written.
// A zero ackFloorProgressAt holds AckPending too: writing 0 over a real nonzero
// observation would erase the one field that separates a wedged consumer from a
// drained one.
func TestReporter_SetProjectionProgress_ZeroTimeLeavesExisting(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")
	require.NoError(t, r.SetActive(context.Background()))

	projectedAt := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	progressAt := time.Date(2026, 7, 3, 11, 59, 0, 0, time.UTC)
	ackFloorAt := time.Date(2026, 7, 3, 11, 58, 0, 0, time.UTC)
	require.NoError(t, r.SetProjectionProgress(context.Background(), 5, projectedAt, progressAt, 4, ackFloorAt))

	// A later cycle with lag but no fresh projection/progress (zero times) must
	// not blank the previously-recorded values.
	require.NoError(t, r.SetProjectionProgress(context.Background(), 7, time.Time{}, time.Time{}, 0, time.Time{}))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(7), entry.ConsumerLag)
	assert.Equal(t, projectedAt.Format(time.RFC3339), entry.LastProjectedAt, "lastProjectedAt must not be blanked by a zero-time update")
	assert.Equal(t, progressAt.Format(time.RFC3339), entry.LagProgressAt, "lagProgressAt must not be blanked by a zero-time update")
	assert.Equal(t, uint64(4), entry.AckPending, "ackPending must not be zeroed by a cycle that could not read ack stats")
	assert.Equal(t, ackFloorAt.Format(time.RFC3339), entry.AckFloorProgressAt, "ackFloorProgressAt must not be blanked by a zero-time update")
}

// TestReporter_SetRebuilding verifies that SetRebuilding writes status "rebuilding",
// preserves ErrorCount and ConsumerLag, and sets PauseReason and LastError to null (AC4).
func TestReporter_SetRebuilding(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	// Pre-populate with an error and a consumer lag so we can verify preservation.
	require.NoError(t, r.SetActive(context.Background()))
	require.NoError(t, r.SetConsumerLag(context.Background(), 50))
	require.NoError(t, r.RecordError(context.Background(), "previous error"))

	require.NoError(t, r.SetRebuilding(context.Background()))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "rebuilding", entry.Status, "status must be 'rebuilding'")
	assert.Nil(t, entry.PauseReason, "pauseReason must be null during rebuild")
	assert.Nil(t, entry.LastError, "lastError must be null during rebuild")
	assert.Equal(t, uint64(1), entry.ErrorCount, "errorCount must be preserved")
	assert.Equal(t, uint64(50), entry.ConsumerLag, "consumerLag must be preserved")
	assert.Equal(t, "my-rule", entry.RuleID)
	assert.NotEmpty(t, entry.LastUpdated, "LastUpdated must be set")
}

// TestReporter_SetRebuilding_ThenSetActive verifies the full rebuild lifecycle:
// rebuilding → active with preserved counts (AC4, AC5).
func TestReporter_SetRebuilding_ThenSetActive(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	require.NoError(t, r.RecordError(context.Background(), "prior error"))
	require.NoError(t, r.SetRebuilding(context.Background()))
	require.NoError(t, r.SetActive(context.Background()))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "active", entry.Status)
	assert.Nil(t, entry.PauseReason)
	assert.Nil(t, entry.LastError)
	assert.Equal(t, uint64(1), entry.ErrorCount, "errorCount preserved through rebuild lifecycle")
}

// TestReporter_Delete verifies that Delete removes the health KV entry and subsequent
// GetStatus returns the default "active" zero entry (ErrKeyNotFound path) (FR39).
func TestReporter_Delete(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	// Write an entry first so there is something to delete.
	require.NoError(t, r.SetActive(context.Background()))

	// Confirm the entry exists.
	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "active", entry.Status)

	// Delete it.
	require.NoError(t, r.Delete(context.Background()))

	// After delete: GetStatus falls into the ErrKeyNotFound path and returns the
	// default active entry — not an error.
	entry, err = r.GetStatus(context.Background())
	require.NoError(t, err, "GetStatus must not error after Delete")
	assert.Equal(t, "active", entry.Status)
	assert.Equal(t, "my-rule", entry.RuleID)
}

// TestReporter_Delete_NoEntry verifies that Delete on a rule with no health KV entry
// is a no-op and does not return an error.
func TestReporter_Delete_NoEntry(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "never-written-rule")

	// Delete without any prior write: must not error.
	require.NoError(t, r.Delete(context.Background()))
}

// TestReporter_ActiveSequence_ThreadSafe verifies that SetRuleSequence and ActiveSequence
// are safe to call concurrently (no race detector errors).
func TestReporter_ActiveSequence_ThreadSafe(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	done := make(chan struct{})
	go func() {
		for i := uint64(0); i < 100; i++ {
			r.SetRuleSequence(i)
		}
		close(done)
	}()
	for i := 0; i < 100; i++ {
		_ = r.ActiveSequence()
	}
	<-done
}

// TestReporter_ClearLastError_ClearsErrorPreservesCount verifies the core
// contract: ClearLastError nils lastError but leaves the cumulative
// errorCount exactly as RecordError left it — mirroring SetActive's own
// ErrorCount-preservation contract (TestReporter_SetActive_PreservesErrorCount)
// without SetActive's Status/PauseReason side effects.
func TestReporter_ClearLastError_ClearsErrorPreservesCount(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	require.NoError(t, r.RecordError(context.Background(), "narrowed Core KV filter registration failed, fell back to the broad filter: stale rejection"))
	require.NoError(t, r.RecordError(context.Background(), "second stale error"))

	require.NoError(t, r.ClearLastError(context.Background()))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Nil(t, entry.LastError, "lastError must be nil after ClearLastError")
	assert.Equal(t, uint64(2), entry.ErrorCount, "errorCount must survive ClearLastError")
	assert.Equal(t, "active", entry.Status, "ClearLastError must not touch status")
}

// TestReporter_ClearLastError_PreservesPausedStatus verifies ClearLastError
// never writes Status/PauseReason, so a persisted pause survives the call
// untouched — the exact guarantee that lets registerWithFilterFallback call it
// at boot without racing (or fighting) the supervisor's
// restore-paused-on-startup path (substrate.ConsumerSupervisor's restoreState,
// Pipeline.Run's doc comment). An infra pause, whose lastError carries no
// diagnosis the operator has to act on, still clears.
func TestReporter_ClearLastError_PreservesPausedStatus(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	require.NoError(t, r.RecordError(context.Background(), "first error"))
	require.NoError(t, r.SetPaused(context.Background(), health.PauseReasonInfra, "target table absent"))

	require.NoError(t, r.ClearLastError(context.Background()))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "paused", entry.Status, "ClearLastError must not flip a persisted pause active")
	require.NotNil(t, entry.PauseReason)
	assert.Equal(t, health.PauseReasonInfra, *entry.PauseReason, "ClearLastError must not touch pauseReason")
	assert.Nil(t, entry.LastError, "lastError must still clear on a non-structural pause")
	assert.Equal(t, uint64(1), entry.ErrorCount, "errorCount must survive")
}

// TestReporter_ClearLastError_KeepsStructuralCause pins the one status
// ClearLastError reads for a decision. A structural pause is held until a human
// reconciles it, and its lastError IS the diagnosis they have to work from —
// while registration succeeds regardless of the pause, so every Pipeline.Run
// and every Rebuild would otherwise erase the cause of a pause it did nothing
// to resolve, leaving `paused/structural` with a null cause.
func TestReporter_ClearLastError_KeepsStructuralCause(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	const cause = `ERROR: column "row_kind" of relation "read_identity_credential_bindings" does not exist (SQLSTATE 42703)`
	require.NoError(t, r.SetPaused(context.Background(), health.PauseReasonStructural, cause))

	require.NoError(t, r.ClearLastError(context.Background()))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "paused", entry.Status)
	require.NotNil(t, entry.PauseReason)
	assert.Equal(t, health.PauseReasonStructural, *entry.PauseReason)
	require.NotNil(t, entry.LastError, "a structural pause's cause must survive ClearLastError")
	assert.Equal(t, cause, *entry.LastError)
}

// TestReporter_SetPaused_EmptyCausePreservesExisting pins the second eraser: an
// operator Pause raised over an already-structurally-paused lens carries no
// cause of its own, and must not be read as "forget the old one".
func TestReporter_SetPaused_EmptyCausePreservesExisting(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	const cause = `ERROR: relation "read_clinic_appointments" does not exist (SQLSTATE 42P01)`
	require.NoError(t, r.SetPaused(context.Background(), health.PauseReasonStructural, cause))
	require.NoError(t, r.SetPaused(context.Background(), health.PauseReasonManual, ""))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	require.NotNil(t, entry.PauseReason)
	assert.Equal(t, health.PauseReasonManual, *entry.PauseReason, "the new reason still wins")
	require.NotNil(t, entry.LastError, "an empty cause means no new cause, not forget the old one")
	assert.Equal(t, cause, *entry.LastError)
}

// TestReporter_SetPaused_EmptyCauseOnActiveLensClearsIt is the boundary of the
// guard above: the preservation is keyed on the lens ALREADY being paused, so a
// first pause raised on an active lens carrying a stale error still records "no
// cause" rather than adopting an unrelated message as its diagnosis.
func TestReporter_SetPaused_EmptyCauseOnActiveLensClearsIt(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	require.NoError(t, r.RecordError(context.Background(), "an unrelated stale error"))
	require.NoError(t, r.SetPaused(context.Background(), health.PauseReasonManual, ""))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "paused", entry.Status)
	assert.Nil(t, entry.LastError, "a first pause on an active lens must not adopt a stale error as its cause")
}

// TestReporter_ClearLastError_NoOpWhenAlreadyNil verifies ClearLastError skips
// the KV write entirely when there is nothing to clear — a fresh lens that has
// never recorded an error must not gain a health entry (or a bumped
// LastUpdated) merely from a clean boot's registration.
func TestReporter_ClearLastError_NoOpWhenAlreadyNil(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	require.NoError(t, r.ClearLastError(context.Background()))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "active", entry.Status)
	assert.Nil(t, entry.LastError)
	assert.Empty(t, entry.LastUpdated, "a no-op clear must never have written an entry")
}

// Every CUMULATIVE counter survives a status transition, and SecureRedactions
// is the one that made the omission load-bearing: Rebuild calls SetRebuilding on
// the way in and SetActive on the way out, so a dropped counter is zeroed twice
// by the very operation a retention-class destruction uses to reach the read
// models. The LensSecureRedaction issue would then go quiet while the
// unresolvable nulls were still being served — the delta-signal failure the
// counter is cumulative to avoid.
func TestReporter_StatusTransitions_PreserveTheCumulativeCounters(t *testing.T) {
	ctx := context.Background()
	kv := startHealthKV(t)
	r := health.New(kv, "counter-rule")

	require.NoError(t, r.RecordSecureRedactions(ctx, 3))
	require.NoError(t, r.RecordEvalDriftRetry(ctx))
	require.NoError(t, r.RecordEvalDriftRequeue(ctx))

	assertCounters := func(t *testing.T, stage string) {
		t.Helper()
		entry, err := r.GetStatus(ctx)
		require.NoError(t, err)
		assert.Equal(t, uint64(3), entry.SecureRedactions, "secureRedactions must survive %s", stage)
		assert.Equal(t, uint64(1), entry.EvalDriftRetries, "evalDriftRetries must survive %s", stage)
		assert.Equal(t, uint64(1), entry.EvalDriftRequeues, "evalDriftRequeues must survive %s", stage)
	}

	// The exact pair a rebuild writes, in order.
	require.NoError(t, r.SetRebuilding(ctx))
	assertCounters(t, "SetRebuilding")
	require.NoError(t, r.SetActive(ctx))
	assertCounters(t, "SetActive")

	require.NoError(t, r.SetPaused(ctx, "manual", "operator paused"))
	assertCounters(t, "SetPaused")
}

// The consumer-filter footprint survives a status transition for a STRONGER
// reason than the counters above: nothing restores it within a cycle. The
// LagPoller re-observes the projection fields every tick, but only a filter
// re-derivation writes these three — and a rebuild calls SetRebuilding on the
// way in and SetActive on the way out, so dropping them would erase the
// footprint on the very operation that re-derives it.
//
// Losing them is worse than a reset, too: an ABSENT filterMode means "this lens
// has never derived a consumer filter", so a paused lens would read as one that
// has not yet decided rather than one whose filter is known and still in place.
func TestReporter_StatusTransitions_PreserveTheConsumerFilterFootprint(t *testing.T) {
	ctx := context.Background()
	kv := startHealthKV(t)
	r := health.New(kv, "footprint-rule")

	require.NoError(t, r.SetFilterState(ctx, health.FilterModeNarrowedRelation, 4, ""))

	assertFootprint := func(t *testing.T, stage string) {
		t.Helper()
		entry, err := r.GetStatus(ctx)
		require.NoError(t, err)
		assert.Equal(t, health.FilterModeNarrowedRelation, entry.FilterMode, "filterMode must survive %s", stage)
		assert.Equal(t, 4, entry.FilterLabelCount, "filterLabelCount must survive %s", stage)
		assert.Empty(t, entry.FilterBroadReason, "filterBroadReason must survive %s", stage)
	}

	// The exact pair a rebuild writes, in order.
	require.NoError(t, r.SetRebuilding(ctx))
	assertFootprint(t, "SetRebuilding")
	require.NoError(t, r.SetActive(ctx))
	assertFootprint(t, "SetActive")

	require.NoError(t, r.SetPaused(ctx, "manual", "operator paused"))
	assertFootprint(t, "SetPaused")

	// A broad reason has to survive the same three, or a lens paused while
	// waiting on its taxonomy would present as one that never derived a filter.
	require.NoError(t, r.SetFilterState(ctx, health.FilterModeBroad, 0, health.FilterBroadReasonTaxonomyUnresolvable))
	require.NoError(t, r.SetRebuilding(ctx))
	require.NoError(t, r.SetActive(ctx))
	require.NoError(t, r.SetPaused(ctx, "manual", "operator paused"))
	entry, err := r.GetStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, health.FilterModeBroad, entry.FilterMode)
	assert.Equal(t, health.FilterBroadReasonTaxonomyUnresolvable, entry.FilterBroadReason)
}

// The recorder for a structural pause that cleared under the lens's own probe.
// All three fields are load-bearing and none of them can be reconstructed later:
// the entry the supervisor writes on either side of the recovery reads `active`,
// and the pause's diagnosis leaves LastError the moment the pause clears.
func TestReporter_RecordStructuralAutoRecovery(t *testing.T) {
	ctx := context.Background()
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	require.NoError(t, r.SetPaused(ctx, "structural", `column "discharged_at" does not exist`))
	require.NoError(t, r.SetActive(ctx))
	require.NoError(t, r.RecordStructuralAutoRecovery(ctx, `column "discharged_at" does not exist`, 2))

	entry, err := r.GetStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, `column "discharged_at" does not exist`, entry.StructuralAutoRecoveredCause)
	assert.Equal(t, 2, entry.StructuralAutoRecoveryAttempts)
	_, parseErr := time.Parse(time.RFC3339, entry.StructuralAutoRecoveredAt)
	assert.NoError(t, parseErr, "structuralAutoRecoveredAt must be valid RFC3339")
	// It records the recovery ONLY. The supervisor owns the status, and has
	// already written it by the time this is called.
	assert.Equal(t, "active", entry.Status)
	assert.Equal(t, "my-rule", entry.RuleID)
}

// Read-modify-write like every other setter, so it cannot blank the counters and
// the footprint an operator reads beside it. A recovery is the one moment those
// values matter most — the errorCount is what says how bad the outage was.
func TestReporter_RecordStructuralAutoRecovery_PreservesTheRestOfTheEntry(t *testing.T) {
	ctx := context.Background()
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	require.NoError(t, r.RecordError(ctx, "first failure"))
	require.NoError(t, r.SetFilterState(ctx, health.FilterModeNarrowedRelation, 4, ""))
	require.NoError(t, r.RecordStructuralAutoRecovery(ctx, "table absent", 1))

	entry, err := r.GetStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), entry.ErrorCount)
	assert.Equal(t, health.FilterModeNarrowedRelation, entry.FilterMode)
	assert.Equal(t, 4, entry.FilterLabelCount)
}

// Last-writer-wins on a second recovery: the fields describe the LATEST
// self-heal, not the first. A lens that heals, relapses and heals again must
// present its current distance from the relapse latch, or an operator reading a
// stale attempt count would think the lens had more headroom than it has.
func TestReporter_RecordStructuralAutoRecovery_LatestRecoveryWins(t *testing.T) {
	ctx := context.Background()
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	require.NoError(t, r.RecordStructuralAutoRecovery(ctx, "table absent", 1))
	require.NoError(t, r.RecordStructuralAutoRecovery(ctx, "constraint absent", 2))

	entry, err := r.GetStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, "constraint absent", entry.StructuralAutoRecoveredCause)
	assert.Equal(t, 2, entry.StructuralAutoRecoveryAttempts)
}

// The sibling of TestReporter_StatusTransitions_PreserveTheConsumerFilterFootprint
// for the auto-recovery record, and the sharper case of the same class: the
// footprint is re-derived by a rebuild, while this is written ONCE and by
// nothing else. A status transition that dropped it would not reset a value, it
// would delete the event — and the pause it describes is already gone from
// pauseReason and lastError by then, so nothing else on the entry could
// reconstruct it.
//
// The erasure would land exactly where the record matters most. A self-heal that
// does not hold re-pauses within one probe interval (10s, the same order as the
// heartbeat), so a stamp dropped by the next SetPaused is observed by no beat at
// all: the attempt count would be unreadable in precisely the flapping case it
// exists to report, and a lens that flapped to its relapse latch would end
// carrying no record of ever having self-healed.
func TestReporter_StatusTransitions_PreserveTheStructuralAutoRecoveryRecord(t *testing.T) {
	ctx := context.Background()
	kv := startHealthKV(t)
	r := health.New(kv, "recovery-rule")

	require.NoError(t, r.RecordStructuralAutoRecovery(ctx, `column "discharged_at" does not exist`, 2))
	stampedAt := func() string {
		entry, err := r.GetStatus(ctx)
		require.NoError(t, err)
		return entry.StructuralAutoRecoveredAt
	}()
	require.NotEmpty(t, stampedAt)

	assertRecord := func(t *testing.T, stage string) {
		t.Helper()
		entry, err := r.GetStatus(ctx)
		require.NoError(t, err)
		assert.Equal(t, stampedAt, entry.StructuralAutoRecoveredAt, "recovery stamp must survive %s", stage)
		assert.Equal(t, `column "discharged_at" does not exist`, entry.StructuralAutoRecoveredCause,
			"recovery cause must survive %s", stage)
		assert.Equal(t, 2, entry.StructuralAutoRecoveryAttempts, "attempt count must survive %s", stage)
	}

	// The relapse sequence, in order: a self-heal that did not hold re-pauses
	// within one probe interval, which is the transition that would erase it.
	require.NoError(t, r.SetPaused(ctx, "structural", "the same fault again"))
	assertRecord(t, "SetPaused")
	require.NoError(t, r.SetActive(ctx))
	assertRecord(t, "SetActive")

	// And the rebuild pair, which an operator runs BECAUSE the recovery told
	// them one might be owed — the operation that would otherwise erase the
	// reason it was run.
	require.NoError(t, r.SetRebuilding(ctx))
	assertRecord(t, "SetRebuilding")
	require.NoError(t, r.SetActive(ctx))
	assertRecord(t, "SetActive after rebuild")
}

// TestReporter_SetPersonalSweepProgress covers the personal convergence sweep's
// health write (personal-lens-grant-change-trigger-design.md §4.3), including
// the rule it inherits from SetAuditProgress: a zero cycle stamp is "this tick
// did not close a cycle", never "forget the one that did".
func TestReporter_SetPersonalSweepProgress(t *testing.T) {
	ctx := context.Background()
	kv := startHealthKV(t)
	r := health.New(kv, "personal-rule")

	// An intermediate tick: a cursor and a live queue depth, no cycle claim.
	require.NoError(t, r.SetPersonalSweepProgress(ctx, "Hj4kPmRtw9nbCxz5vQ2y", time.Time{}, 3))
	entry, err := r.GetStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Hj4kPmRtw9nbCxz5vQ2y", entry.PersonalSweepCursor)
	assert.Equal(t, uint64(3), entry.PersonalSweepQueueDepth)
	assert.Empty(t, entry.PersonalSweepCycleCompletedAt,
		"a tick that closed no cycle must not claim coverage the walk has not earned")

	// The tick that closes a cycle stamps it.
	completed := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, r.SetPersonalSweepProgress(ctx, "Kx3TmZpq7RvwNsY2Hc9L", completed, 0))
	entry, err = r.GetStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Kx3TmZpq7RvwNsY2Hc9L", entry.PersonalSweepCursor)
	assert.Equal(t, completed.Format(time.RFC3339), entry.PersonalSweepCycleCompletedAt)
	assert.Equal(t, uint64(0), entry.PersonalSweepQueueDepth,
		"the depth is a live GAUGE, so a drained queue overwrites a busy one — unlike the cycle stamp, a stale depth is worse than none")

	// And the next intermediate tick leaves that claim alone. Writing a zero
	// here would erase, once a minute, the only field that says what the sweep
	// has actually covered.
	require.NoError(t, r.SetPersonalSweepProgress(ctx, "Wq7bNmXt4RkzPy2LcH8v", time.Time{}, 1))
	entry, err = r.GetStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, completed.Format(time.RFC3339), entry.PersonalSweepCycleCompletedAt)
	assert.Equal(t, uint64(1), entry.PersonalSweepQueueDepth)

	// A concurrent fault write must not lose it, and vice versa: both are
	// read-modify-write under the same writeMu.
	require.NoError(t, r.RecordGrantReprojectIssue(ctx, "overflow", "dropped 2 signal(s)"))
	entry, err = r.GetStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Wq7bNmXt4RkzPy2LcH8v", entry.PersonalSweepCursor)
	require.NotNil(t, entry.LastError)
}

// The status-transition half. The completeness gate in
// entry_carry_forward_completeness_test.go pins that every Entry field survives
// the three wholesale writers; this pins the BEHAVIOUR for these three, which is
// the sharper statement: the sweep is one PROCESS-level walk fanned onto
// per-lens entries, so a lens pausing and resuming observes nothing about it,
// and the sweep only rewrites the fields once per interval — so a dropped value
// would stand for a whole cycle rather than being repaired by the next tick.
func TestReporter_StatusTransitions_PreserveThePersonalSweepProgress(t *testing.T) {
	ctx := context.Background()
	kv := startHealthKV(t)
	r := health.New(kv, "personal-transition-rule")

	completed := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, r.SetPersonalSweepProgress(ctx, "Hj4kPmRtw9nbCxz5vQ2y", completed, 7))

	assertProgress := func(t *testing.T, stage string) {
		t.Helper()
		entry, err := r.GetStatus(ctx)
		require.NoError(t, err)
		assert.Equal(t, "Hj4kPmRtw9nbCxz5vQ2y", entry.PersonalSweepCursor, "cursor must survive %s", stage)
		assert.Equal(t, completed.Format(time.RFC3339), entry.PersonalSweepCycleCompletedAt,
			"cycle claim must survive %s", stage)
		assert.Equal(t, uint64(7), entry.PersonalSweepQueueDepth, "queue depth must survive %s", stage)
	}

	require.NoError(t, r.SetPaused(ctx, "manual", "operator paused"))
	assertProgress(t, "SetPaused")
	require.NoError(t, r.SetActive(ctx))
	assertProgress(t, "SetActive")
	require.NoError(t, r.SetRebuilding(ctx))
	assertProgress(t, "SetRebuilding")
	require.NoError(t, r.SetActive(ctx))
	assertProgress(t, "SetActive after rebuild")
}
