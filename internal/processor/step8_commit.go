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

// ProtectedKeyError is the typed step-8 failure surfaced when an update or
// tombstone mutation targets a root document carrying data.protected == true.
// This is the authoritative, path-independent kernel-protection backstop: it
// closes the bricking hole for EVERY op at once (InstallPackage,
// UninstallPackage, meta-root mutations, and any future DDL) regardless of
// whether the originating script declared the root in ContextHint.Reads.
// The commit path maps this onto a `rejected` reply with code `ProtectedKey`.
// create mutations are exempt — create-only already conflicts on overwrite.
type ProtectedKeyError struct {
	Key  string // the offending mutation key
	Root string // the derived protected root (vtx.<type>.<id>)
	Op   string // the mutation op (update|tombstone)
}

func (e *ProtectedKeyError) Error() string {
	return fmt.Sprintf("ProtectedKey: %s on %s targets protected kernel root %s", e.Op, e.Key, e.Root)
}

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

	// Authoritative protected-key guard (Story 1.5.5 P1). For every update
	// or tombstone, derive the 3-segment root and reject the WHOLE operation
	// if the root document carries data.protected == true. This is the
	// path-independent kernel/auth bricking backstop — the script-level
	// install/uninstall checks are best-effort defense-in-depth only.
	// create mutations are exempt (create-only already conflicts on overwrite).
	if err := c.rejectProtectedMutations(ctx, result.Mutations); err != nil {
		return CommitAck{}, err
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

	bctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	ack, batchErr := c.Conn.AtomicBatch(bctx, ops)
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

	// Synchronous DDL cache invalidation for any committed `vtx.meta.*`
	// mutation. A single operation (e.g. a cascade tombstone) emits many
	// aspect mutations under one meta-vertex root; collapse them to the
	// distinct 3-segment roots so each root is invalidated exactly once
	// (Invalidate is idempotent, but this avoids redundant KV reads).
	if c.DDLs != nil && hasMetaVertexMutation(result.Mutations) {
		seen := map[string]struct{}{}
		for _, m := range result.Mutations {
			if !strings.HasPrefix(m.Key, "vtx.meta.") {
				continue
			}
			parts := strings.Split(m.Key, ".")
			if len(parts) < 3 {
				continue
			}
			root := strings.Join(parts[:3], ".")
			if _, dup := seen[root]; dup {
				continue
			}
			seen[root] = struct{}{}
			if err := c.DDLs.Invalidate(ctx, root); err != nil {
				c.Logger.Warn("step 8: DDL cache invalidation failed (commit already durable)",
					"key", root, "error", err)
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
		Stream:    ack.Stream,
		Sequence:  ack.Sequence,
		BatchID:   ack.BatchID,
		Count:     ack.Count,
		Events:    events,
		Revisions: mutationRevisions(ack.Revisions, result.Mutations),
	}, nil
}

// mutationRevisions filters the substrate's per-key revision map down to
// the operation's business mutation keys, excluding the idempotency
// tracker key. Returns nil when the substrate did not derive revisions.
func mutationRevisions(acked map[string]uint64, mutations []MutationOp) map[string]uint64 {
	if len(acked) == 0 {
		return nil
	}
	out := make(map[string]uint64, len(mutations))
	for _, m := range mutations {
		if rev, ok := acked[m.Key]; ok {
			out[m.Key] = rev
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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

// rejectProtectedMutations is the authoritative commit-time kernel-protection
// guard. For every update/tombstone mutation it derives the 3-segment root
// (vtx.<type>.<id>), KVGets the root document, and rejects the whole operation
// with *ProtectedKeyError if data.protected == true. Root→protected lookups
// are cached within the single commit so multiple aspects of one root cost a
// single KVGet. A root that does not exist (ErrKeyNotFound) is not protected.
// create mutations are skipped — create-only conflicts on overwrite already.
func (c *CommitterImpl) rejectProtectedMutations(ctx context.Context, mutations []MutationOp) error {
	cache := map[string]bool{} // root → protected
	for _, m := range mutations {
		if m.Op != "update" && m.Op != "tombstone" {
			continue
		}
		root := protectedRootKey(m.Key)
		if root == "" {
			continue
		}
		protected, seen := cache[root]
		if !seen {
			p, err := c.rootIsProtected(ctx, root)
			if err != nil {
				return fmt.Errorf("step 8: protected-key check for %s: %w", root, err)
			}
			protected = p
			cache[root] = p
		}
		if protected {
			return &ProtectedKeyError{Key: m.Key, Root: root, Op: m.Op}
		}
	}
	return nil
}

// rootIsProtected reads the root document and reports whether data.protected
// is true. A not-found root is reported as not protected (allow).
func (c *CommitterImpl) rootIsProtected(ctx context.Context, root string) (bool, error) {
	entry, err := c.Conn.KVGet(ctx, c.CoreBucket, root)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return false, nil
		}
		return false, err
	}
	var doc struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		// A root we cannot parse cannot be confirmed protected; treat as
		// non-protected so a corrupt unrelated doc does not wedge commits.
		return false, nil
	}
	if doc.Data == nil {
		return false, nil
	}
	prot, ok := doc.Data["protected"].(bool)
	return ok && prot, nil
}

// protectedRootKey derives the 3-segment root of a mutation key
// (vtx.<type>.<id> from vtx.<type>.<id>.<aspect...>). Returns "" for keys that
// have no 3-segment vtx root (e.g. links, which are not vertex-rooted and are
// not kernel-protected entities). Aspect and root keys alike map to the root.
func protectedRootKey(key string) string {
	parts := strings.Split(key, ".")
	if len(parts) < 3 || parts[0] != "vtx" {
		return ""
	}
	return strings.Join(parts[:3], ".")
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
