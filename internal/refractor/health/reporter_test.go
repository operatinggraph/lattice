package health_test

import (
	"context"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// startHealthKV starts an in-memory NATS server and returns a KV bucket for health entries.
func startHealthKV(t *testing.T) *substrate.KV {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	opts := &natsserver.Options{
		JetStream: true,
		StoreDir:  jsstore.Dir(t),
		NoLog:     true,
		NoSigs:    true,
		Port:      natsserver.RANDOM_PORT,
	}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second))

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(func() { nc.Close(); s.Shutdown() })

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
// writes ConsumerLag/ProjectionLag (same value, both names) and LastProjectedAt,
// while preserving ErrorCount across the read-modify-write (lens-projection-liveness-design.md §3.2).
func TestReporter_SetProjectionProgress_RoundTrips(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")

	require.NoError(t, r.SetActive(context.Background()))
	require.NoError(t, r.RecordError(context.Background(), "boom"))

	projectedAt := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	require.NoError(t, r.SetProjectionProgress(context.Background(), 42, projectedAt))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(42), entry.ConsumerLag, "legacy consumerLag field must still be set")
	assert.Equal(t, uint64(42), entry.ProjectionLag, "operator-facing projectionLag alias must match")
	assert.Equal(t, projectedAt.Format(time.RFC3339), entry.LastProjectedAt)
	assert.Equal(t, uint64(1), entry.ErrorCount, "errorCount must be preserved across the read-modify-write")
}

// TestReporter_SetProjectionProgress_ZeroTimeLeavesExisting verifies that a zero
// lastProjectedAt (no projection yet observed this cycle) does not blank an
// already-stored value — only a genuine advance should ever be written.
func TestReporter_SetProjectionProgress_ZeroTimeLeavesExisting(t *testing.T) {
	kv := startHealthKV(t)
	r := health.New(kv, "my-rule")
	require.NoError(t, r.SetActive(context.Background()))

	projectedAt := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	require.NoError(t, r.SetProjectionProgress(context.Background(), 5, projectedAt))

	// A later cycle with lag but no fresh projection (zero time) must not blank
	// the previously-recorded lastProjectedAt.
	require.NoError(t, r.SetProjectionProgress(context.Background(), 7, time.Time{}))

	entry, err := r.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(7), entry.ConsumerLag)
	assert.Equal(t, projectedAt.Format(time.RFC3339), entry.LastProjectedAt, "lastProjectedAt must not be blanked by a zero-time update")
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
