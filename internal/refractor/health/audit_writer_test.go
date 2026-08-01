package health_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// auditEnv holds all components needed for AuditWriter tests.
type auditEnv struct {
	nc   *nats.Conn
	conn *substrate.Conn
	js   jetstream.JetStream
}

// startAuditServer starts an in-memory NATS server with JetStream.
// Returns an auditEnv for building per-test components.
func startAuditServer(t *testing.T) *auditEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	_, nc := natsfixture.Server(t)

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	return &auditEnv{nc: nc, conn: conn, js: js}
}

// readAuditMsg reads one message for ruleID off the shared REFRACTOR_AUDIT
// stream, timing out after 2s. The consumer filters to ruleID's own subject
// so a test observes only its own entries even though every rule's
// AuditWriter publishes into the same physical stream.
func readAuditMsg(t *testing.T, js jetstream.JetStream, ruleID string) health.AuditEntry {
	t.Helper()
	cons, err := js.CreateOrUpdateConsumer(context.Background(), health.AuditStreamName, jetstream.ConsumerConfig{
		Name:          "test-consumer-" + ruleID,
		FilterSubject: subjects.Audit(ruleID),
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

// TestEnsureAuditStream_CreatesStream verifies that EnsureAuditStream creates
// the single consolidated REFRACTOR_AUDIT JetStream stream, covering every
// lens's audit subject, with the expected retention and byte ceiling (AC3).
func TestEnsureAuditStream_CreatesStream(t *testing.T) {
	env := startAuditServer(t)

	require.NoError(t, health.EnsureAuditStream(context.Background(), env.conn))

	stream, err := env.js.Stream(context.Background(), health.AuditStreamName)
	require.NoError(t, err, "REFRACTOR_AUDIT stream must exist after EnsureAuditStream")
	info, err := stream.Info(context.Background())
	require.NoError(t, err)
	assert.Equal(t, health.AuditStreamName, info.Config.Name)
	assert.Contains(t, info.Config.Subjects, subjects.AuditFilter())
	assert.Equal(t, jetstream.LimitsPolicy, info.Config.Retention)
	assert.Equal(t, 7*24*time.Hour, info.Config.MaxAge)
	assert.Equal(t, int64(512<<20), info.Config.MaxBytes, "MaxBytes must be the 512 MiB audit-forensics budget")
}

// TestEnsureAuditStream_IsIdempotent verifies that calling EnsureAuditStream
// twice does not return an error (idempotent — safe at every startup).
func TestEnsureAuditStream_IsIdempotent(t *testing.T) {
	env := startAuditServer(t)

	require.NoError(t, health.EnsureAuditStream(context.Background(), env.conn))
	require.NoError(t, health.EnsureAuditStream(context.Background(), env.conn), "second EnsureAuditStream must be idempotent")
}

// TestEnsureAuditStream_FailsWhileLegacyStreamOverlaps pins down the exact
// reason boot must run CleanupLegacyAuditStreams before EnsureAuditStream: a
// surviving legacy per-lens stream's subject
// (lattice.refractor.audit.<ruleID>) is a literal subset of the
// consolidated stream's wildcard subject, and JetStream's
// JSStreamSubjectOverlapErr permanently refuses to create a stream whose
// subjects overlap an existing one's. If this test starts failing because
// EnsureAuditStream now succeeds, the vendored NATS server's overlap
// behavior changed — recheck the boot ordering, not this assertion.
func TestEnsureAuditStream_FailsWhileLegacyStreamOverlaps(t *testing.T) {
	env := startAuditServer(t)

	_, err := env.js.CreateOrUpdateStream(context.Background(), jetstream.StreamConfig{
		Name:     "AUDIT_still-here",
		Subjects: []string{"lattice.refractor.audit.still-here"},
	})
	require.NoError(t, err)

	err = health.EnsureAuditStream(context.Background(), env.conn)
	assert.Error(t, err, "EnsureAuditStream must fail while a legacy per-lens stream still claims an overlapping subject")
}

// TestAuditWriter_WriteAudit_Upsert verifies that an upsert entry carries
// the correct entityId, operation, a non-empty outputRowHash, and a valid
// RFC3339 timestamp (AC1, AC2).
func TestAuditWriter_WriteAudit_Upsert(t *testing.T) {
	env := startAuditServer(t)
	require.NoError(t, health.EnsureAuditStream(context.Background(), env.conn))

	const ruleID = "rule-upsert"
	aw := health.NewAuditWriter(env.conn, ruleID)

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
	require.NoError(t, health.EnsureAuditStream(context.Background(), env.conn))

	const ruleID = "rule-delete"
	aw := health.NewAuditWriter(env.conn, ruleID)

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
	require.NoError(t, health.EnsureAuditStream(context.Background(), env.conn))

	const ruleID = "rule-hash"
	aw := health.NewAuditWriter(env.conn, ruleID)

	row := map[string]any{"z": "last", "a": "first", "m": float64(99)}
	require.NoError(t, aw.WriteAudit(context.Background(), "ent-1", "upsert", row))
	require.NoError(t, aw.WriteAudit(context.Background(), "ent-2", "upsert", row))

	// Read both messages via a fresh consumer filtered to this rule's subject.
	cons, err := env.js.CreateOrUpdateConsumer(context.Background(), health.AuditStreamName, jetstream.ConsumerConfig{
		Name:          "test-consumer-hash",
		FilterSubject: subjects.Audit(ruleID),
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

// TestAuditWriter_PerRuleIsolation verifies that two AuditWriters sharing the
// one consolidated REFRACTOR_AUDIT stream publish only to their own audit
// subjects — a subject-filtered consumer sees no cross-contamination
// (NFR13, AC5), even though both rules' entries live in the same stream.
func TestAuditWriter_PerRuleIsolation(t *testing.T) {
	env := startAuditServer(t)
	require.NoError(t, health.EnsureAuditStream(context.Background(), env.conn))

	const ruleA = "rule-iso-audit-a"
	const ruleB = "rule-iso-audit-b"

	awA := health.NewAuditWriter(env.conn, ruleA)
	awB := health.NewAuditWriter(env.conn, ruleB)

	rowA := map[string]any{"tenant": "A"}
	rowB := map[string]any{"tenant": "B"}
	require.NoError(t, awA.WriteAudit(context.Background(), "entity-a", "upsert", rowA))
	require.NoError(t, awB.WriteAudit(context.Background(), "entity-b", "upsert", rowB))

	entryA := readAuditMsg(t, env.js, ruleA)
	entryB := readAuditMsg(t, env.js, ruleB)

	assert.Equal(t, "entity-a", entryA.EntityID, "ruleA's filtered consumer must see only ruleA's entity")
	assert.Equal(t, "entity-b", entryB.EntityID, "ruleB's filtered consumer must see only ruleB's entity")
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

// TestCleanupLegacyAuditStreams_DeletesLegacyOnly verifies that cleanup
// deletes every stream matching the retired per-lens "AUDIT_<ruleID>"
// layout while leaving an unrelated stream untouched, and that doing so
// (run BEFORE EnsureAuditStream, as boot does) is what lets EnsureAuditStream
// succeed at all: a surviving legacy stream's subject
// (lattice.refractor.audit.<ruleID>) is a literal subset of the
// consolidated stream's wildcard subject, and JetStream's
// JSStreamSubjectOverlapErr permanently refuses to create a stream whose
// subjects overlap an existing one's — so EnsureAuditStream cannot run
// first on a deployment that still has legacy streams.
func TestCleanupLegacyAuditStreams_DeletesLegacyOnly(t *testing.T) {
	env := startAuditServer(t)

	// A pre-existing stream from before consolidation — one per lens. Created
	// before REFRACTOR_AUDIT exists, exactly like an un-migrated deployment.
	_, err := env.js.CreateOrUpdateStream(context.Background(), jetstream.StreamConfig{
		Name:     "AUDIT_legacy123",
		Subjects: []string{"lattice.refractor.audit.legacy123"},
	})
	require.NoError(t, err)

	// A stream that merely shares the family's subject namespace but not its
	// name prefix — must survive, proving the match is on stream name only.
	_, err = env.js.CreateOrUpdateStream(context.Background(), jetstream.StreamConfig{
		Name:     "UNRELATED_STREAM",
		Subjects: []string{"some.other.subject"},
	})
	require.NoError(t, err)

	deleted, err := health.CleanupLegacyAuditStreams(context.Background(), env.conn)
	require.NoError(t, err)
	assert.Equal(t, 1, deleted)

	_, err = env.js.Stream(context.Background(), "AUDIT_legacy123")
	assert.ErrorIs(t, err, jetstream.ErrStreamNotFound, "legacy per-lens stream must be deleted")

	_, err = env.js.Stream(context.Background(), "UNRELATED_STREAM")
	assert.NoError(t, err, "unrelated stream must survive cleanup")

	// With the overlapping legacy stream gone, EnsureAuditStream must now
	// succeed — it would fail with JSStreamSubjectOverlapErr had the legacy
	// stream survived.
	require.NoError(t, health.EnsureAuditStream(context.Background(), env.conn))
	_, err = env.js.Stream(context.Background(), health.AuditStreamName)
	assert.NoError(t, err, "consolidated stream must exist once cleanup has cleared the way")
}

// TestCleanupLegacyAuditStreams_NoLegacyStreams_NoOp verifies cleanup is a
// true no-op — zero deleted, no error — when no legacy stream exists, so a
// deployment already on the consolidated layout can run it on every boot.
func TestCleanupLegacyAuditStreams_NoLegacyStreams_NoOp(t *testing.T) {
	env := startAuditServer(t)
	require.NoError(t, health.EnsureAuditStream(context.Background(), env.conn))

	deleted, err := health.CleanupLegacyAuditStreams(context.Background(), env.conn)
	require.NoError(t, err)
	assert.Equal(t, 0, deleted)
}
