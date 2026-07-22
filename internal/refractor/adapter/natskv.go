package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// Compile-time check that NatsKVAdapter satisfies Adapter, Truncater and KeyLister.
var _ Adapter = (*NatsKVAdapter)(nil)
var _ Truncater = (*NatsKVAdapter)(nil)
var _ KeyLister = (*NatsKVAdapter)(nil)
var _ RowReader = (*NatsKVAdapter)(nil)

// guardCASMaxAttempts caps the conditional-write retry loop a guarded adapter
// runs when a concurrent writer (the retry-queue goroutine) collides on the
// same key. On exhaustion the write returns a plain error, which the pipeline's
// failure.Classify routes as CatTransient (re-enqueue, not a pump pause).
const guardCASMaxAttempts = 8

// projectionSeqField is the top-level body field carrying the monotonic
// ordering token on a guarded write (Contract #6 §6.2).
const projectionSeqField = "projectionSeq"

// kvStore is the subset of *substrate.KV's method set NatsKVAdapter depends
// on. *substrate.KV satisfies it implicitly (no call-site changes needed);
// tests substitute a scripted fake to trigger guardedWrite's
// revision-conflict-retry and CAS-exhaustion branches deterministically,
// which a real NATS-backed store can only reach via an actual race.
type kvStore interface {
	Get(ctx context.Context, key string) (*substrate.KVEntry, error)
	Create(ctx context.Context, key string, value []byte) (uint64, error)
	Update(ctx context.Context, key string, value []byte, expectedRevision uint64) (uint64, error)
	Put(ctx context.Context, key string, value []byte) (uint64, error)
	Delete(ctx context.Context, key string) error
	ListKeys(ctx context.Context) ([]string, error)
	Purge(ctx context.Context, key string) error
	Status(ctx context.Context) error
}

// NatsKVAdapter writes materialized rows to a NATS KV bucket.
type NatsKVAdapter struct {
	kv         kvStore
	keyOrder   []string   // ordered key field names; used for deterministic composite key construction
	deleteMode DeleteMode // hard (default): kv.Delete; soft: tombstone Put
	// guarded selects the monotonic projection-write guard (Contract #6 §6.2).
	// When true, Upsert/Delete write conditionally (CAS) so a lower-seq replay
	// is rejected, a Delete becomes a soft tombstone carrying the watermark, and
	// projectionSeq is stamped into the persisted body. Set per-lens via
	// SetGuarded; the two at-risk lenses (capabilityEphemeral, myTasks) enable it.
	guarded bool
}

// New creates a NatsKVAdapter that writes to kv.
// keyOrder must match the rule's into.key field list and determines the order
// in which key values are concatenated to form the KV key
// (e.g. ["account_id","agreement_id"] → "acct-001.abc123").
// deleteMode selects hard (kv.Delete) vs soft (tombstone Put) delete projection;
// it is fixed for the life of the adapter.
//
// The adapter is built unguarded; SetGuarded enables the projection-write guard
// for the lenses that require it (the canonical-name switch in cmd/refractor
// owns that decision, keeping this constructor free of lens-name knowledge).
func New(kv *substrate.KV, keyOrder []string, deleteMode DeleteMode) (*NatsKVAdapter, error) {
	if len(keyOrder) == 0 {
		return nil, errors.New("natskv: keyOrder must not be empty")
	}
	return &NatsKVAdapter{kv: kv, keyOrder: keyOrder, deleteMode: deleteMode}, nil
}

// SetGuarded enables or disables the monotonic projection-write guard for this
// adapter. It must be called at construction time, before the pipeline starts
// writing — the flag is not safe to flip concurrently with writes.
func (a *NatsKVAdapter) SetGuarded(guarded bool) { a.guarded = guarded }

// Guarded reports whether the projection-write guard is enabled. The pipeline
// consults it to decide whether the non-stream-sequenced adjacency-watch path
// may write this adapter's keys (a guarded watermark may only be advanced or
// cleared by a stream-sequenced write).
func (a *NatsKVAdapter) Guarded() bool { return a.guarded }

// buildKey concatenates key field values in keyOrder order, joined with ".".
// Lattice key shape convention (Contract #1) uses "." as the segment
// separator throughout — vtx.<type>.<id>.<aspect>, lnk.<…>, cap.identity.<id>.
// Returns an error if any key field is absent from keys.
func (a *NatsKVAdapter) buildKey(keys map[string]any) (string, error) {
	parts := make([]string, len(a.keyOrder))
	for i, field := range a.keyOrder {
		val, ok := keys[field]
		if !ok {
			return "", fmt.Errorf("natskv: key field %q absent from keys map", field)
		}
		parts[i] = fmt.Sprintf("%v", val)
	}
	return strings.Join(parts, "."), nil
}

// Upsert serializes row to JSON and writes it to the KV bucket under the
// constructed key. An unguarded adapter writes unconditionally (idempotent
// last-writer-wins, ignoring projectionSeq). A guarded adapter writes
// conditionally: it drops the write as an idempotent no-op when a write with an
// equal-or-higher projectionSeq already landed, and otherwise stamps
// projectionSeq into the persisted body and commits via a CAS loop so a lower-seq
// replay can never overwrite a newer projection (Contract #6 §6.2).
func (a *NatsKVAdapter) Upsert(ctx context.Context, keys map[string]any, row map[string]any, projectionSeq uint64) error {
	key, err := a.buildKey(keys)
	if err != nil {
		return fmt.Errorf("natskv upsert: %w", err)
	}
	if a.guarded {
		return a.guardedWrite(ctx, key, row, projectionSeq, false)
	}
	data, err := json.Marshal(row)
	if err != nil {
		return fmt.Errorf("natskv upsert: marshal row: %w", err)
	}
	if _, err := a.kv.Put(ctx, key, data); err != nil {
		return fmt.Errorf("natskv upsert: put %s: %w", key, err)
	}
	return nil
}

// Delete projects a Core KV deletion into the target KV bucket. The behavior is
// fixed at construction time by the adapter's deleteMode:
//
//   - DeleteModeHard (default): physically removes the key via kv.Delete. Lineage
//     already lives in Core KV, so the derived view reflects deletions as
//     removals. Deleting a never-existed key is idempotent — the absent-key
//     ErrKeyNotFound is swallowed and nil returned.
//   - DeleteModeSoft: writes a tombstone document {isDeleted:true, projectedAt:…}
//     for audit/forensic targets that opt in. Overwriting a never-existed key is
//     naturally idempotent (Put creates).
//
// Both absence (hard) and tombstone (soft) are treated as denial by the
// capability authorizer (step3_auth_capability): an absent key resolves to
// NoCapabilityEntry and an isDeleted doc to a denied entry. The freshness-ceiling
// comparison that originally motivated soft-delete on the capability plane was
// removed in Story 1.5.4, so absence and tombstone are now equivalent for auth.
func (a *NatsKVAdapter) Delete(ctx context.Context, keys map[string]any, projectionSeq uint64) error {
	key, err := a.buildKey(keys)
	if err != nil {
		return fmt.Errorf("natskv delete: %w", err)
	}
	if a.guarded {
		// A guarded delete is always a soft tombstone carrying the watermark,
		// regardless of the lens's deleteMode: the high-water mark must survive
		// physical absence so a lower-seq replay still loses. Absence and an
		// isDeleted tombstone are equivalent for authorization (Contract #6 §6.8).
		return a.guardedWrite(ctx, key, nil, projectionSeq, true)
	}
	if a.deleteMode == DeleteModeSoft {
		tombstone := map[string]any{
			"isDeleted":   true,
			"projectedAt": time.Now().UTC().Format(time.RFC3339),
		}
		data, err := json.Marshal(tombstone)
		if err != nil {
			return fmt.Errorf("natskv delete: marshal tombstone: %w", err)
		}
		if _, err := a.kv.Put(ctx, key, data); err != nil {
			return fmt.Errorf("natskv delete: put tombstone %s: %w", key, err)
		}
		return nil
	}
	// Hard delete: physically remove the key. Deleting an absent key is a no-op.
	if err := a.kv.Delete(ctx, key); err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return nil
		}
		return fmt.Errorf("natskv delete: delete %s: %w", key, err)
	}
	return nil
}

// guardedWrite performs a monotonic, conditional write under the projection
// guard (Contract #6 §6.2). delete selects a soft tombstone body
// {isDeleted:true, projectionSeq} over a live upsert body (row + injected
// projectionSeq). It reads the current entry, drops the write as an idempotent
// no-op when the stored projectionSeq is greater than or equal to the incoming
// one, and otherwise commits with the entry's revision as the CAS precondition.
// A revision conflict (a concurrent writer landed first) triggers a re-read and
// re-compare, bounded by guardCASMaxAttempts; on exhaustion it returns a plain
// error (routed transient) after a warn naming the key.
//
// A guarded write always carries a real JetStream stream sequence (≥ 1); the
// only way to reach here with incomingSeq == 0 is a non-stream caller (the
// adjacency-watch path, which already skips guarded keys) or a failed metadata
// read. Such a write carries no ordering and is dropped as a fail-closed no-op
// so it can neither create a clobberable seq-0 key nor no-op a real update.
func (a *NatsKVAdapter) guardedWrite(ctx context.Context, key string, row map[string]any, incomingSeq uint64, delete bool) error {
	if incomingSeq == 0 {
		slog.Warn("natskv guarded write: dropping sequence-less write (no ordering token)",
			"key", key, "delete", delete)
		return nil
	}
	body := a.guardedBody(row, incomingSeq, delete)
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("natskv guarded write: marshal %s: %w", key, err)
	}

	for attempt := 0; attempt < guardCASMaxAttempts; attempt++ {
		entry, getErr := a.kv.Get(ctx, key)
		if getErr != nil {
			if !errors.Is(getErr, substrate.ErrKeyNotFound) {
				return fmt.Errorf("natskv guarded write: get %s: %w", key, getErr)
			}
			// Key absent: create it. A concurrent create wins the revision and
			// we re-read on the next iteration.
			if _, createErr := a.kv.Create(ctx, key, data); createErr != nil {
				if errors.Is(createErr, substrate.ErrRevisionConflict) {
					continue
				}
				return fmt.Errorf("natskv guarded write: create %s: %w", key, createErr)
			}
			return nil
		}

		if storedSeq, ok := storedProjectionSeq(entry.Value); ok && storedSeq >= incomingSeq {
			// A write with an equal-or-higher watermark already landed; this is
			// an idempotent no-op (a stale lower-seq replay loses).
			return nil
		}

		if _, updErr := a.kv.Update(ctx, key, data, entry.Revision); updErr != nil {
			if errors.Is(updErr, substrate.ErrRevisionConflict) {
				continue
			}
			return fmt.Errorf("natskv guarded write: update %s: %w", key, updErr)
		}
		return nil
	}

	slog.Warn("natskv guarded write: CAS loop exhausted under contention",
		"key", key, "attempts", guardCASMaxAttempts, "projectionSeq", incomingSeq)
	return fmt.Errorf("natskv guarded write: %s: revision conflict not resolved after %d attempts", key, guardCASMaxAttempts)
}

// guardedBody builds the persisted document for a guarded write: a soft
// tombstone for a delete, or the projection row with projectionSeq injected as a
// top-level field for an upsert.
func (a *NatsKVAdapter) guardedBody(row map[string]any, incomingSeq uint64, delete bool) map[string]any {
	if delete {
		return map[string]any{
			"isDeleted":        true,
			"projectedAt":      time.Now().UTC().Format(time.RFC3339),
			projectionSeqField: incomingSeq,
		}
	}
	body := make(map[string]any, len(row)+1)
	for k, v := range row {
		body[k] = v
	}
	body[projectionSeqField] = incomingSeq
	return body
}

// storedProjectionSeq extracts the projectionSeq watermark from a persisted
// guarded body. Returns (0, false) when the body is empty, unparseable, or
// carries no projectionSeq (legacy doc written before the guard) — the caller
// then treats it as the lowest possible watermark and proceeds to write.
func storedProjectionSeq(data []byte) (uint64, bool) {
	if len(data) == 0 {
		return 0, false
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return 0, false
	}
	raw, ok := doc[projectionSeqField]
	if !ok {
		return 0, false
	}
	// json.Unmarshal into map[string]any always decodes a JSON number as
	// float64 (this function never uses a json.Decoder.UseNumber() reader,
	// so json.Number is unreachable here). A negative value is a malformed
	// watermark, not a real low seq — treated as absent rather than
	// converted, which would wrap to a bogus near-max uint64 and poison
	// every future guarded write to the key (permanent false "already
	// newer" no-op).
	v, ok := raw.(float64)
	if !ok || v < 0 {
		return 0, false
	}
	return uint64(v), true
}

// ListKeys returns every live key in the bucket, split back into its
// keyOrder field-name map (the inverse of buildKey). A single-field keyOrder
// (the common `IntoKey: ["key"]` capability-envelope shape) maps the whole
// key string verbatim — a Lattice key is itself "."-segmented
// (cap.identity.<id>), so splitting would misparse it. A composite keyOrder
// (2+ fields, e.g. app_id+landlord_id) splits on "." and requires the
// segment count to match exactly — safe because the platform's NanoID
// alphabet carries no dots, so no individual composite field value can
// introduce a spurious segment; a key that doesn't match is skipped (it
// belongs to a different lens sharing the bucket, or predates a keyOrder
// change) rather than surfacing a malformed partial map.
// A soft-delete-mode bucket's tombstone documents remain live NATS-KV keys
// (unlike a hard delete) and so are still listed here — acceptable because
// no live DiffRetraction lens targets a soft-delete NATS-KV bucket today.
func (a *NatsKVAdapter) ListKeys(ctx context.Context) ([]map[string]any, error) {
	keys, err := a.kv.ListKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("natskv list keys: %w", err)
	}
	out := make([]map[string]any, 0, len(keys))
	if len(a.keyOrder) == 1 {
		field := a.keyOrder[0]
		for _, k := range keys {
			out = append(out, map[string]any{field: k})
		}
		return out, nil
	}
	for _, k := range keys {
		parts := strings.Split(k, ".")
		if len(parts) != len(a.keyOrder) {
			slog.Warn("natskv list keys: skipping key with unexpected segment count",
				"key", k, "wantSegments", len(a.keyOrder), "gotSegments", len(parts))
			continue
		}
		m := make(map[string]any, len(parts))
		for i, field := range a.keyOrder {
			m[field] = parts[i]
		}
		out = append(out, m)
	}
	return out, nil
}

// GetRow reads back the row previously written at keys, stripped of the
// guard's internal projectionSeq bookkeeping field (callers merge this into a
// freshly computed partial row — projectionSeq is re-added by the next
// Upsert's own guard, never carried by the caller). Returns (nil, false, nil)
// when the key does not exist or holds a soft-delete tombstone (isDeleted) —
// both read as "no row to carry forward from," the same posture Upsert's own
// absent-key branch takes.
func (a *NatsKVAdapter) GetRow(ctx context.Context, keys map[string]any) (map[string]any, bool, error) {
	key, err := a.buildKey(keys)
	if err != nil {
		return nil, false, fmt.Errorf("natskv get row: %w", err)
	}
	entry, err := a.kv.Get(ctx, key)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("natskv get row: get %s: %w", key, err)
	}
	if len(entry.Value) == 0 {
		return nil, false, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return nil, false, fmt.Errorf("natskv get row: unmarshal %s: %w", key, err)
	}
	if isDeleted, _ := doc["isDeleted"].(bool); isDeleted {
		return nil, false, nil
	}
	delete(doc, projectionSeqField)
	return doc, true, nil
}

// Truncate clears the bucket by purging every key, so a rebuild's stream replay
// starts from an empty high-water state and the highest-seq write wins
// (Contract #6 §6.2). Purge removes each key's prior revisions and leaves a
// delete marker as the latest revision, so a subsequent Get returns
// ErrKeyNotFound: a guarded rebuild then takes the absent→Create path on the
// first replay and never reads a stale projectionSeq watermark, eliminating the
// rejected-write holes a lower-seq replay against a live watermark would leave.
func (a *NatsKVAdapter) Truncate(ctx context.Context) error {
	keys, err := a.kv.ListKeys(ctx)
	if err != nil {
		return fmt.Errorf("natskv truncate: list keys: %w", err)
	}
	for _, key := range keys {
		// Purge is idempotent: a key deleted out from under us between the list
		// and the purge is not an error.
		if err := a.kv.Purge(ctx, key); err != nil {
			return fmt.Errorf("natskv truncate: purge %s: %w", key, err)
		}
	}
	return nil
}

// Probe checks whether the NATS KV bucket is reachable by calling kv.Status.
// Returns nil if the bucket is accessible; returns an infrastructure or structural
// error that failure.Classify can route appropriately.
func (a *NatsKVAdapter) Probe(ctx context.Context) error {
	return a.kv.Status(ctx)
}

// Close is a no-op; the NATS KV handle lifecycle is managed by the caller.
func (a *NatsKVAdapter) Close() error {
	return nil
}
