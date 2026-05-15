package health_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/health"
	"github.com/asolgan/lattice/internal/refractor/subjects"
)

// auditEnv holds all components needed for AuditWriter tests.
type auditEnv struct {
	nc *nats.Conn
	js jetstream.JetStream
}

// startAuditServer starts an in-memory NATS server with JetStream.
// Returns an auditEnv for building per-test components.
func startAuditServer(t *testing.T) *auditEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	opts := &natsserver.Options{
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoLog:     true,
		NoSigs:    true,
		Port:      natsserver.RANDOM_PORT,
	}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err, "create test NATS server")
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second), "NATS server not ready within 5s")

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err, "connect to test NATS server")
	t.Cleanup(func() { nc.Close(); s.Shutdown() })

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	return &auditEnv{nc: nc, js: js}
}

// readAuditMsg reads one message from the audit stream for ruleID, timing out after 2s.
func readAuditMsg(t *testing.T, js jetstream.JetStream, ruleID string) health.AuditEntry {
	t.Helper()
	cons, err := js.CreateOrUpdateConsumer(context.Background(), "AUDIT_"+ruleID, jetstream.ConsumerConfig{
		Name:          "test-consumer-" + ruleID,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckNonePolicy,
	})
	require.NoError(t, err)

	msg, err := cons.Next(jetstream.FetchMaxWait(2 * time.Second))
	require.NoError(t, err, "must receive one audit message")

	var entry health.AuditEntry
	require.NoError(t, json.Unmarshal(msg.Data(), &entry))
	return entry
}

// TestAuditWriter_EnsureStream_CreatesStream verifies that EnsureStream creates
// a JetStream stream named "AUDIT_<ruleId>" (AC3).
func TestAuditWriter_EnsureStream_CreatesStream(t *testing.T) {
	env := startAuditServer(t)

	const ruleID = "rule-ensure"
	aw := health.NewAuditWriter(env.js, ruleID)
	require.NoError(t, aw.EnsureStream(context.Background()))

	// Stream must exist and have the expected name.
	stream, err := env.js.Stream(context.Background(), "AUDIT_"+ruleID)
	require.NoError(t, err, "AUDIT_<ruleId> stream must exist after EnsureStream")
	info, err := stream.Info(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "AUDIT_"+ruleID, info.Config.Name)
	assert.Contains(t, info.Config.Subjects, subjects.Audit(ruleID))
	assert.Equal(t, jetstream.LimitsPolicy, info.Config.Retention)
	assert.Equal(t, 7*24*time.Hour, info.Config.MaxAge)
}

// TestAuditWriter_EnsureStream_IsIdempotent verifies that calling EnsureStream
// twice does not return an error (idempotent — safe at every startup).
func TestAuditWriter_EnsureStream_IsIdempotent(t *testing.T) {
	env := startAuditServer(t)

	const ruleID = "rule-idem"
	aw := health.NewAuditWriter(env.js, ruleID)
	require.NoError(t, aw.EnsureStream(context.Background()))
	require.NoError(t, aw.EnsureStream(context.Background()), "second EnsureStream must be idempotent")
}

// TestAuditWriter_WriteAudit_Upsert verifies that an upsert entry carries
// the correct entityId, operation, a non-empty outputRowHash, and a valid
// RFC3339 timestamp (AC1, AC2).
func TestAuditWriter_WriteAudit_Upsert(t *testing.T) {
	env := startAuditServer(t)

	const ruleID = "rule-upsert"
	aw := health.NewAuditWriter(env.js, ruleID)
	require.NoError(t, aw.EnsureStream(context.Background()))

	row := map[string]any{"name": "Alice", "score": float64(42)}
	require.NoError(t, aw.WriteAudit(context.Background(), "entity-123", "upsert", row))

	entry := readAuditMsg(t, env.js, ruleID)
	assert.Equal(t, "entity-123", entry.EntityID)
	assert.Equal(t, "upsert", entry.Operation)
	assert.NotEmpty(t, entry.OutputRowHash, "outputRowHash must be non-empty for upsert")
	assert.Len(t, entry.OutputRowHash, 64, "SHA-256 hex digest must be 64 chars")
	_, parseErr := time.Parse(time.RFC3339, entry.Timestamp)
	assert.NoError(t, parseErr, "Timestamp must be valid RFC3339")
}

// TestAuditWriter_WriteAudit_Delete verifies that a delete entry has an empty
// outputRowHash (AC1 — empty for deletes, no output row to hash).
func TestAuditWriter_WriteAudit_Delete(t *testing.T) {
	env := startAuditServer(t)

	const ruleID = "rule-delete"
	aw := health.NewAuditWriter(env.js, ruleID)
	require.NoError(t, aw.EnsureStream(context.Background()))

	require.NoError(t, aw.WriteAudit(context.Background(), "entity-del", "delete", nil))

	entry := readAuditMsg(t, env.js, ruleID)
	assert.Equal(t, "entity-del", entry.EntityID)
	assert.Equal(t, "delete", entry.Operation)
	assert.Empty(t, entry.OutputRowHash, "outputRowHash must be empty for delete")
}

// TestAuditWriter_RowHashIsDeterministic verifies that the same row written
// twice produces identical outputRowHash values (AC1 — deterministic SHA-256).
func TestAuditWriter_RowHashIsDeterministic(t *testing.T) {
	env := startAuditServer(t)

	const ruleID = "rule-hash"
	aw := health.NewAuditWriter(env.js, ruleID)
	require.NoError(t, aw.EnsureStream(context.Background()))

	row := map[string]any{"z": "last", "a": "first", "m": float64(99)}
	require.NoError(t, aw.WriteAudit(context.Background(), "ent-1", "upsert", row))
	require.NoError(t, aw.WriteAudit(context.Background(), "ent-2", "upsert", row))

	// Read both messages via a fresh consumer and compare hashes.
	cons, err := env.js.CreateOrUpdateConsumer(context.Background(), "AUDIT_"+ruleID, jetstream.ConsumerConfig{
		Name:          "test-consumer-hash",
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckNonePolicy,
	})
	require.NoError(t, err)

	var entries []health.AuditEntry
	for i := 0; i < 2; i++ {
		msg, err := cons.Next(jetstream.FetchMaxWait(2 * time.Second))
		require.NoError(t, err, "must receive message %d", i+1)
		var e health.AuditEntry
		require.NoError(t, json.Unmarshal(msg.Data(), &e))
		entries = append(entries, e)
	}

	require.Len(t, entries, 2)
	assert.NotEmpty(t, entries[0].OutputRowHash)
	assert.Equal(t, entries[0].OutputRowHash, entries[1].OutputRowHash,
		"same row must produce identical outputRowHash regardless of write order")
}

// TestAuditWriter_PerRuleIsolation verifies that two AuditWriters publish only to
// their own audit subjects with no cross-contamination (NFR13, AC5).
func TestAuditWriter_PerRuleIsolation(t *testing.T) {
	env := startAuditServer(t)

	const ruleA = "rule-iso-audit-a"
	const ruleB = "rule-iso-audit-b"

	awA := health.NewAuditWriter(env.js, ruleA)
	awB := health.NewAuditWriter(env.js, ruleB)
	require.NoError(t, awA.EnsureStream(context.Background()))
	require.NoError(t, awB.EnsureStream(context.Background()))

	rowA := map[string]any{"tenant": "A"}
	rowB := map[string]any{"tenant": "B"}
	require.NoError(t, awA.WriteAudit(context.Background(), "entity-a", "upsert", rowA))
	require.NoError(t, awB.WriteAudit(context.Background(), "entity-b", "upsert", rowB))

	entryA := readAuditMsg(t, env.js, ruleA)
	entryB := readAuditMsg(t, env.js, ruleB)

	assert.Equal(t, "entity-a", entryA.EntityID, "ruleA stream must contain ruleA entity")
	assert.Equal(t, "entity-b", entryB.EntityID, "ruleB stream must contain ruleB entity")
	assert.NotEqual(t, entryA.OutputRowHash, entryB.OutputRowHash, "different rows must produce different hashes")
}

// TestAuditWriter_NilWriter_NoOp verifies AC6: when no AuditWriter is configured on
// the pipeline (auditWriter == nil), the pipeline processes messages without panic.
// The nil-guard lives in pipeline.writeAudit, not in AuditWriter itself — calling
// (*AuditWriter)(nil).WriteAudit(...) directly would panic. The guard must be in the
// caller. Integration coverage for this path is in pipeline_test.go:
// TestPipeline_NilAuditWriter_NoOp.
func TestAuditWriter_NilWriter_NoOp(t *testing.T) {
	// Confirm that AuditWriter.WriteAudit on a nil receiver panics — meaning the
	// nil-guard in pipeline.writeAudit is the correct and necessary defence.
	var aw *health.AuditWriter
	assert.Panics(t, func() {
		// This MUST panic: the nil-guard is not in WriteAudit, it is in pipeline.writeAudit.
		// If this ever stops panicking, the guard may have been moved incorrectly.
		_ = aw.WriteAudit(context.Background(), "x", "upsert", nil)
	}, "(*AuditWriter)(nil).WriteAudit must panic — nil-guard belongs in pipeline.writeAudit")
}
