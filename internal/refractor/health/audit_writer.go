package health

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// auditStreamMaxAge is the retention period for audit stream messages.
const auditStreamMaxAge = 7 * 24 * time.Hour

// auditStreamMaxBytes bounds the consolidated audit stream to a fixed
// audit-forensics storage budget — 512 MiB — independent of how many lenses
// a deployment activates. The per-lens stream layout this replaced carried
// no byte cap at all; on one measured dev stack that grew to 95 streams /
// 20.4M messages / 5.6 GB (93% of the whole JetStream store) and contributed
// to a NATS OOM.
const auditStreamMaxBytes = 512 << 20

// AuditStreamName is the JetStream stream every lens's audit entries share.
// Entries are distinguished by subject (lattice.refractor.audit.<lensId>,
// via subjects.Audit), never by stream — one physical stream regardless of
// how many lenses a deployment activates.
const AuditStreamName = "REFRACTOR_AUDIT"

// auditStreamLegacyPrefix is the JetStream stream-name prefix the retired
// per-lens audit layout used: one stream per rule, "AUDIT_<ruleID>".
// CleanupLegacyAuditStreams deletes every stream still matching it.
const auditStreamLegacyPrefix = "AUDIT_"

// AuditEntry is the JSON payload appended to lattice.refractor.audit.<lensId>
// for each row a lens COMMITS to its target along the CDC write path — the
// pipeline's own write step and the retry queue that finishes what that step
// could not. All field names are camelCase per FR21 convention.
//
// Committed is the whole of the entry's claim: outputRowHash describes a row the
// target is holding, so a write that stored nothing appends nothing. A guarded
// write the ordering guard declined, one dropped for want of an ordering token,
// and an unguarded row skipped as byte-identical all pass through unrecorded.
//
// The trail covers that path, not every mutation the target ever sees.
// Reconciliation — the convergence sweep and the operator reproject RPC — writes
// through its own machinery and accounts for what it healed as verdicts on the
// lens's health entry rather than as entries here.
type AuditEntry struct {
	EntityID      string `json:"entityId"`
	Operation     string `json:"operation"`     // "upsert" | "delete"
	OutputRowHash string `json:"outputRowHash"` // SHA-256 hex of row JSON; empty for deletes
	Timestamp     string `json:"timestamp"`     // RFC3339 UTC
}

// AuditWriter appends one rule's audit entries to the shared AuditStreamName
// JetStream stream, addressed by that rule's own subject
// (lattice.refractor.audit.<lensId>). EnsureAuditStream must be called once
// at process startup — not per writer — before any WriteAudit.
type AuditWriter struct {
	conn   *substrate.Conn
	ruleID string
}

// NewAuditWriter creates an AuditWriter for the given rule.
// Panics if conn is nil or ruleID is empty.
func NewAuditWriter(conn *substrate.Conn, ruleID string) *AuditWriter {
	if conn == nil {
		panic("health: NewAuditWriter: conn must not be nil")
	}
	if ruleID == "" {
		panic("health: NewAuditWriter: ruleID must not be empty")
	}
	return &AuditWriter{conn: conn, ruleID: ruleID}
}

// EnsureAuditStream creates or updates the single JetStream stream backing
// every lens's audit trail (AuditStreamName, subject filter
// subjects.AuditFilter()). Idempotent — safe to call once on every process
// startup, before any AuditWriter.WriteAudit. Retention: LimitsPolicy with a
// 7-day MaxAge (NFR6 — not instantly purgeable) and a 512 MiB MaxBytes
// ceiling so the diagnostic trail cannot grow to exhaust the shared
// JetStream store no matter how many lenses a deployment activates.
//
// Call CleanupLegacyAuditStreams first. A surviving legacy per-lens stream's
// subject (lattice.refractor.audit.<ruleID>) is a literal subset of this
// stream's wildcard subject, and JetStream permanently refuses to create a
// stream whose subjects overlap an existing one's — so this call fails on
// any deployment that still has legacy streams until they are removed.
func EnsureAuditStream(ctx context.Context, conn *substrate.Conn) error {
	if err := conn.EnsureStream(ctx, substrate.StreamSpec{
		Name:     AuditStreamName,
		Subjects: []string{subjects.AuditFilter()},
		MaxAge:   auditStreamMaxAge,
		MaxBytes: auditStreamMaxBytes,
	}); err != nil {
		return fmt.Errorf("health: EnsureAuditStream: %w", err)
	}
	slog.Info("health: audit stream ready", "stream", AuditStreamName, "subjects", subjects.AuditFilter())
	return nil
}

// CleanupLegacyAuditStreams deletes every JetStream stream left over from the
// retired per-lens audit layout (one stream per rule, "AUDIT_<ruleID>"),
// superseded by the single AuditStreamName stream. Idempotent: an
// environment with no legacy streams left runs this as a no-op, so it is
// safe to call unconditionally on every startup — self-healing every
// environment, including ones already cleaned up by hand. Returns the
// number of streams deleted.
//
// Call this before EnsureAuditStream, not after: each legacy stream's
// subject is a literal subset of the consolidated stream's wildcard
// subject, and JetStream refuses to create a stream whose subjects overlap
// an existing one's, so a surviving legacy stream would otherwise fail
// every EnsureAuditStream call.
func CleanupLegacyAuditStreams(ctx context.Context, conn *substrate.Conn) (int, error) {
	names, err := conn.StreamNames(ctx)
	if err != nil {
		return 0, fmt.Errorf("health: CleanupLegacyAuditStreams: list streams: %w", err)
	}
	var deleted int
	for _, name := range names {
		if name == AuditStreamName || !strings.HasPrefix(name, auditStreamLegacyPrefix) {
			continue
		}
		if err := conn.DeleteStream(ctx, name); err != nil {
			return deleted, fmt.Errorf("health: CleanupLegacyAuditStreams: delete %q: %w", name, err)
		}
		deleted++
	}
	return deleted, nil
}

// WriteAudit publishes one audit entry for a committed successful write.
// op must be "upsert" or "delete". row is the written row data (nil or empty for deletes).
// Returns an error if marshaling or publishing fails; the caller should log and continue —
// a failed audit entry must never abort message processing.
func (a *AuditWriter) WriteAudit(ctx context.Context, entityID, op string, row map[string]any) error {
	entry := AuditEntry{
		EntityID:      entityID,
		Operation:     op,
		OutputRowHash: rowHash(op, row),
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("health: AuditWriter.WriteAudit marshal: %w", err)
	}
	if err := a.conn.Publish(ctx, subjects.Audit(a.ruleID), data, nil); err != nil {
		return fmt.Errorf("health: AuditWriter.WriteAudit publish %s: %w", entityID, err)
	}
	return nil
}

// rowHash computes a deterministic SHA-256 hex digest of the written row for upsert
// operations. Returns an empty string for deletes or nil rows (no output row to hash).
// Go's json.Marshal sorts map keys alphabetically (guaranteed since Go 1.12), so the
// digest is deterministic for the same row content regardless of map insertion order.
// An empty non-nil row marshals to "{}" and produces a valid deterministic hash.
// Returns "" with a Warn log if json.Marshal fails (non-serializable value in row).
func rowHash(op string, row map[string]any) string {
	if op != "upsert" || row == nil {
		return ""
	}
	data, err := json.Marshal(row)
	if err != nil {
		slog.Warn("health: rowHash: json.Marshal failed; outputRowHash will be empty", "err", err)
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
