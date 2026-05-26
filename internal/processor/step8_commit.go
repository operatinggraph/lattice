package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// ConflictError is the typed step-8 failure surfaced when the atomic
// batch is rejected by the substrate (typically: revision condition
// failure on one of the keys). The commit path maps this onto a
// `rejected` reply with code `RevisionConflict`.
type ConflictError struct {
	ConflictingKey     string // best-effort — empty if the substrate did not report which key conflicted
	ExpectedRevision   uint64
	OperationRequestID string
	Cause              error
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("ConflictError: requestId=%s key=%s expected=%d: %v",
		e.OperationRequestID, e.ConflictingKey, e.ExpectedRevision, e.Cause)
}

func (e *ConflictError) Unwrap() error { return e.Cause }

// CommitterImpl is the step-8 implementation. Behavior:
//  1. Build a single substrate.AtomicBatch op list:
//     - one BatchOp per mutation (revision condition derived from
//       mutation.op: create→0, update→expectedRevision, tombstone→
//       expectedRevision)
//     - one BatchOp for the idempotency tracker (CreateOnly, TTL=24h)
//  2. Submit via Conn.AtomicBatch.
//  3. On a successful commit that touched `vtx.meta.*` keys: invalidate
//     the DDL cache for each affected meta-vertex (synchronous).
//
// The single returned BatchAck is propagated to the commit path as
// CommitAck. The atomic batch is "all-or-nothing": either every
// mutation + the tracker land in the same logical commit, or none do.
type CommitterImpl struct {
	Conn       *substrate.Conn
	CoreBucket string
	DDLs       *DDLCache
	Logger     *slog.Logger
	Clock      func() time.Time
	// Timeout bounds the round trip on the substrate.AtomicBatch call.
	Timeout time.Duration
}

// NewCommitter constructs the real Committer.
func NewCommitter(conn *substrate.Conn, coreBucket string, cache *DDLCache, logger *slog.Logger, clock func() time.Time) *CommitterImpl {
	if conn == nil {
		panic("processor: NewCommitter requires Conn")
	}
	if coreBucket == "" {
		panic("processor: NewCommitter requires coreBucket")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if clock == nil {
		clock = time.Now
	}
	return &CommitterImpl{
		Conn:       conn,
		CoreBucket: coreBucket,
		DDLs:       cache,
		Logger:     logger,
		Clock:      clock,
		Timeout:    5 * time.Second,
	}
}

// Commit implements Committer. Builds the atomic batch from the validated
// MutationBatch + tracker and submits it. The commit path supplies the bare
// tracker; the Committer enriches `data` with `mutationKeys` and `eventClasses`
// (Contract #4 §4.2) before serialization so it holds the authoritative
// serialization moment.
func (c *CommitterImpl) Commit(ctx context.Context, env *OperationEnvelope, result ScriptResult, tracker Tracker) (CommitAck, error) {
	now := c.Clock()
	rid := env.RequestID

	// Enrich tracker with Contract #4 §4.2 fields.
	mutKeys := make([]string, 0, len(result.Mutations))
	for _, m := range result.Mutations {
		mutKeys = append(mutKeys, m.Key)
	}
	// Build the EventList once here. The same list is returned in CommitAck
	// so step 9 publishes identical event IDs to those recorded in the tracker.
	events, err := BuildEventList(env, result, now)
	if err != nil {
		return CommitAck{}, fmt.Errorf("step 8: build event list: %w", err)
	}
	if tracker.Data == nil {
		tracker.Data = map[string]any{}
	}
	tracker.Data["mutationKeys"] = mutKeys
	tracker.Data["eventClasses"] = events.EventClasses()

	trackerVal, err := tracker.Marshal()
	if err != nil {
		return CommitAck{}, fmt.Errorf("step 8: marshal tracker: %w", err)
	}

	ops := make([]substrate.BatchOp, 0, len(result.Mutations)+1)

	// Mutation ops.
	for _, m := range result.Mutations {
		val, err := buildMutationValue(env, m, now, tracker.Key)
		if err != nil {
			return CommitAck{}, fmt.Errorf("step 8: build mutation %s: %w", m.Key, err)
		}
		op := substrate.BatchOp{
			Bucket: c.CoreBucket,
			Key:    m.Key,
			Value:  val,
		}
		switch m.Op {
		case "create":
			op.CreateOnly = true
		case "update", "tombstone":
			if m.ExpectedRevision != nil {
				op.HasRevision = true
				op.Revision = *m.ExpectedRevision
			}
			// If no expectedRevision is supplied, the BatchOp goes
			// through unconditioned. Contract #3 §3.2 says "if omitted,
			// Processor uses the revision read during step 4" — this
			// hardening is deferred; only explicit overrides are carried forward.
		}
		ops = append(ops, op)
	}

	// Tracker op — always CreateOnly with 24h TTL (Contract #4 §4.3).
	ops = append(ops, substrate.BatchOp{
		Bucket:     c.CoreBucket,
		Key:        tracker.Key,
		Value:      trackerVal,
		CreateOnly: true,
		TTL:        TrackerTTL,
	})

	ack, batchErr := c.Conn.AtomicBatch(ops, c.Timeout)
	if batchErr != nil {
		// Wrap in ConflictError if the underlying cause looks like a
		// revision conflict.
		if errors.Is(batchErr, substrate.ErrAtomicBatchRejected) {
			return CommitAck{}, &ConflictError{
				ConflictingKey:     guessConflictingKey(batchErr, ops),
				OperationRequestID: rid,
				Cause:              batchErr,
			}
		}
		return CommitAck{}, batchErr
	}

	// Synchronous DDL cache invalidation for any committed `vtx.meta.*` mutation.
	if c.DDLs != nil && hasMetaVertexMutation(result.Mutations) {
		for _, m := range result.Mutations {
			if !strings.HasPrefix(m.Key, "vtx.meta.") {
				continue
			}
			if err := c.DDLs.Invalidate(ctx, m.Key); err != nil {
				c.Logger.Warn("step 8: DDL cache invalidation failed (commit already durable)",
					"key", m.Key, "error", err)
			}
		}
	}

	c.Logger.Info("step 8: committed",
		"requestId", rid,
		"mutations", len(result.Mutations),
		"events", len(events),
		"trackerKey", tracker.Key,
		"stream", ack.Stream,
		"seq", ack.Sequence,
		"batchID", ack.BatchID)

	return CommitAck{
		Stream:   ack.Stream,
		Sequence: ack.Sequence,
		BatchID:  ack.BatchID,
		Count:    ack.Count,
		Events:   events,
	}, nil
}

// buildMutationValue assembles the JSON value the substrate writes for
// one mutation, injecting provenance fields per Contract #1 §1.3 +
// Contract #3 §3.2 ("Provenance fields are NOT set by the script —
// they are injected by the Processor at commit step 6 using the current
// operation's actor and timestamp"). For tombstones, only the
// lastModified* triplet + isDeleted=true are injected — the existing
// `data` is left intact.
func buildMutationValue(env *OperationEnvelope, m MutationOp, at time.Time, trackerKey string) ([]byte, error) {
	stamp := substrate.FormatTimestamp(at)

	// Start with the document the script supplied (a fresh map so we
	// don't mutate the caller's struct).
	doc := map[string]interface{}{}
	for k, v := range m.Document {
		doc[k] = v
	}
	doc["key"] = m.Key

	switch m.Op {
	case "create":
		if _, ok := doc["isDeleted"]; !ok {
			doc["isDeleted"] = false
		}
		doc["createdAt"] = stamp
		doc["createdBy"] = env.Actor
		doc["createdByOp"] = trackerKey
		doc["lastModifiedAt"] = stamp
		doc["lastModifiedBy"] = env.Actor
		doc["lastModifiedByOp"] = trackerKey
		if _, ok := doc["data"]; !ok {
			doc["data"] = map[string]interface{}{}
		}
	case "update":
		doc["lastModifiedAt"] = stamp
		doc["lastModifiedBy"] = env.Actor
		doc["lastModifiedByOp"] = trackerKey
	case "tombstone":
		doc["isDeleted"] = true
		doc["lastModifiedAt"] = stamp
		doc["lastModifiedBy"] = env.Actor
		doc["lastModifiedByOp"] = trackerKey
	}

	return json.Marshal(doc)
}

// guessConflictingKey extracts the best-effort key that caused an
// AtomicBatch rejection. NATS reports the failing subject in the
// error description but the substrate wrap loses the exact key
// boundary; we walk ops to find the most likely candidate (the
// tracker key is the canonical guess when the underlying err_code
// indicates "wrong last sequence" — the tracker is the one op that's
// CreateOnly in every successful path).
func guessConflictingKey(err error, ops []substrate.BatchOp) string {
	s := err.Error()
	// Look for any of our keys in the error description.
	for _, op := range ops {
		if op.Key != "" && strings.Contains(s, op.Key) {
			return op.Key
		}
	}
	return ""
}
