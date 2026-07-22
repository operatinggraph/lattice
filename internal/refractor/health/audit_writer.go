package health

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// auditStreamMaxAge is the default retention period for audit stream messages.
const auditStreamMaxAge = 7 * 24 * time.Hour

// AuditEntry is the JSON payload appended to lattice.refractor.audit.<lensId> on each
// successful write. All field names are camelCase per FR21 convention.
type AuditEntry struct {
	EntityID      string `json:"entityId"`
	Operation     string `json:"operation"`     // "upsert" | "delete"
	OutputRowHash string `json:"outputRowHash"` // SHA-256 hex of row JSON; empty for deletes
	Timestamp     string `json:"timestamp"`     // RFC3339 UTC
}

// AuditWriter appends audit entries to the per-rule JetStream audit stream after
// each successful write operation. The stream is append-only (LimitsPolicy, 7-day
// MaxAge). Call EnsureStream once at startup before WriteAudit.
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

// EnsureStream creates or updates the JetStream audit stream for this rule.
// Idempotent — safe to call on every process startup. Must be called before WriteAudit.
// Stream name: "AUDIT_<ruleID>"; subject: lattice.refractor.audit.<lensId>.
// Retention: LimitsPolicy with 7-day MaxAge (NFR6 — not instantly purgeable).
func (a *AuditWriter) EnsureStream(ctx context.Context) error {
	if err := a.conn.EnsureStream(ctx, substrate.StreamSpec{
		Name:     auditStreamName(a.ruleID),
		Subjects: []string{subjects.Audit(a.ruleID)},
		MaxAge:   auditStreamMaxAge,
	}); err != nil {
		return fmt.Errorf("health: AuditWriter.EnsureStream %s: %w", a.ruleID, err)
	}
	slog.Info("health: audit stream ready", "ruleId", a.ruleID, "stream", auditStreamName(a.ruleID))
	return nil
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

// auditStreamName returns the JetStream stream name for the given ruleID.
func auditStreamName(ruleID string) string {
	return "AUDIT_" + ruleID
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
