package processor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/substrate/keys"
)

// ConflictError is the typed step-8 failure surfaced when the atomic
// batch is rejected by the substrate (typically: revision condition
// failure on one of the keys). The commit path maps this onto a
// `rejected` reply with code `RevisionConflict`.
type ConflictError struct {
	ConflictingKey string // best-effort — empty if the substrate did not report which key conflicted
	// FallbackConditionedKeys holds the update/tombstone mutation keys THIS
	// batch conditioned on step 8's own prior-document read (readPriorDocuments)
	// because no earlier stage had already set ExpectedRevision — i.e. a key
	// the submitting script never declared in contextHint.reads (typically one
	// discovered via kv.Links at execution time), keyed to the revision the
	// batch asserted. Same structurally-benign race shape as the §3.2-defaulted
	// set (commit_path.go's `defaulted`): the commit path re-probes these for
	// movement too, both for accurate attribution and retry eligibility.
	FallbackConditionedKeys map[string]uint64
	ExpectedRevision        uint64
	OperationRequestID      string
	Cause                   error
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

// PermissionProvenanceError is the typed step-8 failure surfaced when an update
// mutation targets an existing vtx.permission.<id> root and changes
// data.operationType, data.scope, data.origin or data.declaredBy — the four
// provenance fields the origin invariant depends on staying write-once. A
// permission entity key is content-addressed on (package, operationType,
// scope) (Contract #8 §8.1), so a legitimate upgrade never changes any of the
// four on a surviving key: a real change produces a different key (create +
// tombstone), never an update. data.note / data.lanes stay freely mutable.
// The same error carries a role's .canonicalName aspect rewrite, with Field
// "value". For a role or roleindex root rewrite, Field names whichever
// top-level document field the mutation changed (e.g. "data" for a rewritten
// roleindex root), not one of the four permission-specific names above. The
// commit path maps this onto a `rejected` reply with code
// `PermissionProvenance`.
type PermissionProvenanceError struct {
	Key   string // the offending mutation key
	Field string // the write-once field the update tried to change
}

func (e *PermissionProvenanceError) Error() string {
	return fmt.Sprintf("PermissionProvenance: update on %s attempted to rewrite provenance field %q", e.Key, e.Field)
}

// BatchTooLargeError is the typed step-8 failure surfaced when the substrate
// pre-flight guard rejects the atomic batch (Contract #3 §3.9.1): the batch
// exceeds MaxBatchMessages, or a single mutation's value exceeds the
// negotiated payload ceiling. Reason distinguishes the two:
// "mutationCount" | "valueSize" (Key is only set for the latter). The commit
// path maps this onto a terminal `rejected` reply with code `BatchTooLarge` —
// a redelivery reproduces the identical over-limit batch and can never
// succeed, so it must never be retried.
type BatchTooLargeError struct {
	Reason             string // "mutationCount" | "valueSize"
	Limit              int
	Actual             int
	Key                string // valueSize only
	OperationRequestID string
	Cause              error
}

func (e *BatchTooLargeError) Error() string {
	if e.Key != "" {
		return fmt.Sprintf("BatchTooLargeError: requestId=%s reason=%s key=%s limit=%d actual=%d: %v",
			e.OperationRequestID, e.Reason, e.Key, e.Limit, e.Actual, e.Cause)
	}
	return fmt.Sprintf("BatchTooLargeError: requestId=%s reason=%s limit=%d actual=%d: %v",
		e.OperationRequestID, e.Reason, e.Limit, e.Actual, e.Cause)
}

func (e *BatchTooLargeError) Unwrap() error { return e.Cause }

// CommitterImpl is the step-8 implementation. Behavior:
//  1. Build a single substrate.AtomicBatch op list:
//     - one BatchOp per mutation (revision condition derived from
//     mutation.op: create→0, update→expectedRevision, tombstone→
//     expectedRevision)
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
	// so the outbox consumer publishes identical event IDs to those recorded in the tracker.
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
	//
	// The same read pass loads the stored document behind every update and
	// tombstone, which buildMutationValue needs to preserve what the mutation
	// does not resupply.
	prior, err := c.readPriorDocuments(ctx, result.Mutations)
	if err != nil {
		return CommitAck{}, err
	}
	if err := rejectProtectedMutations(result.Mutations, prior); err != nil {
		return CommitAck{}, err
	}
	// Sibling guard over the same read pass: a permission vertex's provenance
	// fields and a role's canonical name are write-once once committed, for
	// every path that can emit a mutation against them.
	if err := rejectPermissionRoleRewrites(result.Mutations, prior); err != nil {
		return CommitAck{}, err
	}

	ops := make([]substrate.BatchOp, 0, len(result.Mutations)+1)
	var fallbackConditioned map[string]uint64

	// Mutation ops.
	for _, m := range result.Mutations {
		val, err := buildMutationValue(env, m, now, tracker.Key, prior.doc(m.Key))
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
			// Contract #3 §3.2: an update/tombstone is conditioned on the
			// expectedRevision if supplied, else the revision read at step 4.
			// The commit path (applyHydratedRevisions) defaults ExpectedRevision
			// to the step-4 hydrated revision before Commit, so by here a default
			// update arrives already conditioned; only a write to a key never read
			// at step 4 (one discovered at execution time via kv.Links, as every
			// cascade does) reaches commit with ExpectedRevision nil.
			//
			// Such a write is not left unconditioned: this value is built from the
			// document read moments ago at step 8 — the whole document, for a
			// tombstone — so it is conditioned on THAT revision instead. Without
			// it, a commit landing in the window between that read and this batch
			// would be silently reverted by the value written here. This fallback
			// case is surfaced on ConflictError.FallbackConditionedKeys so the
			// commit path can attribute a conflict on it and retry it, exactly as
			// it already does for a §3.2-defaulted key.
			if rev, ok, fallback := conditionRevision(m, prior); ok {
				op.HasRevision = true
				op.Revision = rev
				if fallback {
					if fallbackConditioned == nil {
						fallbackConditioned = map[string]uint64{}
					}
					fallbackConditioned[m.Key] = rev
				}
			}
		}
		ops = append(ops, op)
	}

	// Tracker op — create-only with 24h TTL (Contract #4 §4.3). "Create" here
	// carries the KV Create() semantics Contract #4 §4.5 names: when step 2
	// observed an operator-tombstoned tracker value still occupying the subject
	// (SupersedesRevision non-nil, the §4.5 retry signal), the write is
	// conditioned on that revision — a raw expected-last-subject-sequence-of-0
	// create can never succeed against a subject that still carries a message,
	// which would brick the contracted tombstone-then-resubmit path. Either
	// form is the batch's mutual-exclusion point for concurrent re-executions
	// of the same requestId: exactly one racer's condition holds.
	trackerOp := substrate.BatchOp{
		Bucket: c.CoreBucket,
		Key:    tracker.Key,
		Value:  trackerVal,
		TTL:    TrackerTTL,
	}
	if tracker.SupersedesRevision != nil {
		trackerOp.HasRevision = true
		trackerOp.Revision = *tracker.SupersedesRevision
	} else {
		trackerOp.CreateOnly = true
	}
	ops = append(ops, trackerOp)

	// Transactional outbox: persist the faithful EventList as a sibling
	// aspect (vtx.op.<id>.events) in the SAME atomic batch, so it is durable
	// iff the commit succeeds. The durable outbox consumer publishes from this
	// record. It carries NO per-key TTL — it must outlive the 24h tracker so a
	// >24h Processor/consumer outage never drops events; the consumer tombstones
	// it after a confirmed publish. Ops with zero events write no outbox aspect.
	//
	// The write is deliberately UNCONDITIONED. The tracker op above is the
	// batch's sole mutual-exclusion point: a racing duplicate execution loses
	// on the tracker condition and its whole batch — outbox write included —
	// atomically fails, so the outbox needs no condition of its own for
	// correctness. A condition here would instead BRICK every legitimate
	// re-execution of a requestId whose prior incarnation's aspect was
	// tombstoned by the outbox consumer after publish (the tombstone's DEL
	// marker still occupies the subject long after the 24h tracker has
	// expired — the exact state a Contract #4 §4.3 post-TTL resubmit of a
	// deterministic requestId, e.g. every same-version `lattice-pkg install
	// --force` refresh, encounters). In the residual edge where a prior
	// incarnation's aspect is still LIVE-unpublished (>24h consumer outage +
	// deterministic-requestId reuse), the overwrite supersedes it and the
	// consumer publishes the newest event set.
	if len(events) > 0 {
		outboxAsp := NewOutboxAspect(rid, env.Actor, tracker.Key, substrate.FormatTimestamp(now), events)
		outboxVal, err := outboxAsp.Marshal()
		if err != nil {
			return CommitAck{}, fmt.Errorf("step 8: marshal outbox aspect: %w", err)
		}
		ops = append(ops, substrate.BatchOp{
			Bucket: c.CoreBucket,
			Key:    outboxAsp.Key,
			Value:  outboxVal,
		})
	}

	bctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	ack, batchErr := c.Conn.AtomicBatch(bctx, ops)
	if batchErr != nil {
		// The substrate's pre-flight size guard (Contract #3 §3.9.1) — un-wrapped,
		// never an ErrAtomicBatchRejected, so it must be checked first.
		if errors.Is(batchErr, substrate.ErrBatchTooLarge) {
			return CommitAck{}, &BatchTooLargeError{
				Reason:             "mutationCount",
				Limit:              substrate.MaxBatchMessages,
				Actual:             len(ops),
				OperationRequestID: rid,
				Cause:              batchErr,
			}
		}
		if errors.Is(batchErr, substrate.ErrValueTooLarge) {
			limit := int(c.Conn.NATS().MaxPayload()) - substrate.ValueHeadroomBytes
			key, actual := offendingValueOp(ops, limit)
			return CommitAck{}, &BatchTooLargeError{
				Reason:             "valueSize",
				Limit:              limit,
				Actual:             actual,
				Key:                key,
				OperationRequestID: rid,
				Cause:              batchErr,
			}
		}
		// Wrap in ConflictError if the underlying cause looks like a
		// revision conflict.
		if errors.Is(batchErr, substrate.ErrAtomicBatchRejected) {
			return CommitAck{}, &ConflictError{
				ConflictingKey:          guessConflictingKey(batchErr, ops),
				FallbackConditionedKeys: fallbackConditioned,
				OperationRequestID:      rid,
				Cause:                   batchErr,
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

// immutableEnvelopeFields are the Contract #1 §1.3 creation-provenance fields.
// They are established once at create and survive every later mutation: no
// script can set them (VertexDoc does not expose them to Starlark) and no
// script may overwrite them.
var immutableEnvelopeFields = [...]string{"createdAt", "createdBy", "createdByOp"}

// buildMutationValue assembles the JSON value the substrate writes for
// one mutation, injecting provenance fields per Contract #1 §1.3 +
// Contract #3 §3.2 ("Provenance fields are NOT set by the script —
// they are injected by the Processor at commit step 6 using the current
// operation's actor and timestamp").
//
// Every mutation writes the WHOLE value — the substrate has no partial update —
// so an update or tombstone that started from the script's document alone would
// silently erase everything the script did not resupply. `prior` is the stored
// document (nil if the key is absent) and is what makes the write faithful:
//
//   - update — the script's document is authoritative for mutable state, but the
//     immutable creation triplet is carried over from `prior`, overriding any
//     value the script supplied. An update over an absent key materially creates
//     it, so the triplet is stamped fresh rather than left missing.
//   - tombstone — the script supplies no document at all (the mutation parser
//     only reads `document` for create/update). The prior document is carried
//     over WHOLE and only `isDeleted` + the lastModified triplet change, so a
//     tombstoned link keeps the `class`/`sourceVertex`/`targetVertex` that make
//     it readable as a link, and a tombstoned entity keeps the provenance a
//     later revive needs.
func buildMutationValue(env *OperationEnvelope, m MutationOp, at time.Time, trackerKey string, prior map[string]interface{}) ([]byte, error) {
	stamp := substrate.FormatTimestamp(at)

	// Start from the base this mutation preserves, then layer the script's
	// document over it (a fresh map so we mutate neither the caller's struct
	// nor the commit's prior-document cache).
	doc := map[string]interface{}{}
	if m.Op == "tombstone" {
		for k, v := range prior {
			doc[k] = v
		}
	}
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
		if _, ok := doc["data"]; !ok {
			doc["data"] = map[string]interface{}{}
		}
	case "update", "tombstone":
		if m.Op == "tombstone" {
			doc["isDeleted"] = true
		}
		preserveImmutableFields(doc, prior, env, stamp, trackerKey)
	}

	doc["lastModifiedAt"] = stamp
	doc["lastModifiedBy"] = env.Actor
	doc["lastModifiedByOp"] = trackerKey

	return json.Marshal(doc)
}

// preserveImmutableFields establishes the Contract #1 §1.3 creation triplet on
// the value being written. Every field is taken from the stored document or
// stamped from this operation — never from the script. The script's own values
// are dropped first, so a document that is MISSING the triplet (every document
// written by a Processor predating provenance preservation) cannot have one
// forged onto it by supplying the fields in the mutation: the first write to
// heal such a document stamps the healing operation, and every later write
// preserves that.
func preserveImmutableFields(doc, prior map[string]interface{}, env *OperationEnvelope, stamp, trackerKey string) {
	for _, f := range immutableEnvelopeFields {
		delete(doc, f)
	}
	for _, f := range immutableEnvelopeFields {
		if v, ok := prior[f]; ok {
			doc[f] = v
			continue
		}
		switch f {
		case "createdAt":
			doc[f] = stamp
		case "createdBy":
			doc[f] = env.Actor
		case "createdByOp":
			doc[f] = trackerKey
		}
	}
}

// priorDoc is a stored document read at step 8, with the revision it was read
// at. Doc is nil when the key is absent (or unparseable, which yields nothing
// to preserve and cannot be confirmed protected — treated as absent so one
// corrupt value does not wedge commits).
type priorDoc struct {
	Doc      map[string]interface{}
	Revision uint64
	Found    bool
}

// priorDocs is the per-commit cache of stored documents, keyed by KV key. One
// KVGet per distinct key per commit serves both the protected-key guard and
// provenance preservation.
type priorDocs map[string]priorDoc

func (p priorDocs) doc(key string) map[string]interface{} { return p[key].Doc }

// lookup reports the stored document behind a key for a write-once comparison,
// separating the three states doc() collapses into one. found is false when the
// key holds nothing at all — unconstrained, there is no earlier value to
// preserve. decoded is false when an entry exists but its body did not parse:
// readPriorDoc keeps such an entry rather than failing the commit, so a guard
// that must fail closed has to treat it as a body it cannot prove unchanged,
// not as an absent one.
func (p priorDocs) lookup(key string) (doc map[string]interface{}, found, decoded bool) {
	pd, ok := p[key]
	if !ok || !pd.Found {
		return nil, false, false
	}
	return pd.Doc, true, pd.Doc != nil
}

// priorReadConcurrency bounds the in-flight reads of the step-8 prior-document
// pass. A cascade may mutate up to substrate.MaxBatchMessages keys, and the
// substrate exposes no batched get, so a sequential pass would cost that many
// serial round trips on the operation's own deadline.
const priorReadConcurrency = 16

// readPriorDocuments reads the stored document for every update/tombstone
// mutation key, plus each distinct protected root. Reading here — rather than
// trusting step-4 hydration — keeps preservation unconditional: a tombstone
// carries no script document at all, and a script that never declared the key
// as a read still must not erase what it did not supply.
//
// The read is a moment later than the step-4 revision the batch asserts, but a
// commit that succeeds proves no write landed in between, so the document read
// here is the document the batch supersedes. A mutation the script left
// unconditioned (a key discovered at execution time via kv.Links, never
// hydrated at step 4) has no such proof, so the revision read here is carried
// on priorDoc and becomes that mutation's condition — without it a tombstone,
// which writes the whole prior document back, could revert a concurrent commit
// that landed in the window.
//
// The pass is bounded by c.Timeout so a large cascade cannot burn the lane
// deadline and livelock on redelivery.
func (c *CommitterImpl) readPriorDocuments(ctx context.Context, mutations []MutationOp) (priorDocs, error) {
	keys := make([]string, 0, 2*len(mutations))
	seen := map[string]struct{}{}
	addKey := func(k string) {
		if k == "" {
			return
		}
		if _, dup := seen[k]; dup {
			return
		}
		seen[k] = struct{}{}
		keys = append(keys, k)
	}
	for _, m := range mutations {
		if m.Op != "update" && m.Op != "tombstone" {
			continue
		}
		addKey(m.Key)
		addKey(protectedRootKey(m.Key))
	}
	if len(keys) == 0 {
		return priorDocs{}, nil
	}

	rctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	prior := make(priorDocs, len(keys))
	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
		sem      = make(chan struct{}, priorReadConcurrency)
	)
	for _, key := range keys {
		wg.Add(1)
		sem <- struct{}{}
		go func(key string) {
			defer wg.Done()
			defer func() { <-sem }()
			pd, err := c.readPriorDoc(rctx, key)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("step 8: read prior document for %s: %w", key, err)
					cancel()
				}
				return
			}
			prior[key] = pd
		}(key)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return prior, nil
}

// readPriorDoc reads and decodes one stored document. Numbers are decoded as
// json.Number: a tombstone writes the whole prior document back, so decoding
// through float64 would silently round any integer above 2^53 on the way out.
func (c *CommitterImpl) readPriorDoc(ctx context.Context, key string) (priorDoc, error) {
	entry, err := c.Conn.KVGet(ctx, c.CoreBucket, key)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return priorDoc{}, nil
		}
		return priorDoc{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(entry.Value))
	dec.UseNumber()
	var doc map[string]interface{}
	if err := dec.Decode(&doc); err != nil {
		return priorDoc{Revision: entry.Revision, Found: true}, nil
	}
	return priorDoc{Doc: doc, Revision: entry.Revision, Found: true}, nil
}

// rejectProtectedMutations is the authoritative commit-time kernel-protection
// guard. For every update/tombstone mutation it derives the 3-segment root
// (vtx.<type>.<id>) and rejects the whole operation with *ProtectedKeyError if
// the root document carries data.protected == true. Roots are served from the
// commit's document cache, so multiple aspects of one root cost a single KVGet.
// A root that does not exist is not protected. create mutations are skipped —
// create-only conflicts on overwrite already.
func rejectProtectedMutations(mutations []MutationOp, prior priorDocs) error {
	for _, m := range mutations {
		if m.Op != "update" && m.Op != "tombstone" {
			continue
		}
		root := protectedRootKey(m.Key)
		if root == "" {
			continue
		}
		if docIsProtected(prior.doc(root)) {
			return &ProtectedKeyError{Key: m.Key, Root: root, Op: m.Op}
		}
	}
	return nil
}

// permissionProvenanceFields is the set of vtx.permission.<id> root data fields
// authorization consumes, all write-once once committed. The origin invariant
// reads operationType, scope, origin and declaredBy; platformLaneGate
// (step3_auth_capability.go) reads lanes, where an entry-level value replaces
// the capability document's own lane set and can reach a privileged lane the
// core allowlist sanctions for that operationType. data.note carries no
// authorization weight and stays mutable. Order is fixed so the reported Field
// is stable.
var permissionProvenanceFields = []string{"operationType", "scope", "origin", "declaredBy", "lanes"}

// healableProvenanceFields are the provenance fields a stored permission may
// legitimately be missing, and so may gain a value for the first time. The
// installer began stamping origin/declaredBy after permissions had already been
// committed without them, so the first upgrade of such a package must be able
// to fill them in. Healing an absent value cannot launder a runtime-minted
// grant into a package-declared one: runtime minting and the origin stamp are
// halves of the same mechanism, so no permission stored without an origin can
// ever have been runtime-minted. operationType and scope are deliberately not
// healable — a permission's key is derived from them, so a stored permission
// always carries both, and an absent one is a corrupt body rather than an old
// one.
var healableProvenanceFields = map[string]bool{"origin": true, "declaredBy": true}

// committerManagedFields are written by the committer on every commit — the key
// itself, the Contract #1 §1.3 creation triplet restored from the stored
// document, and the last-modified stamps. They never carry a script's value, so
// a write-once comparison of a document body must ignore them.
var committerManagedFields = map[string]bool{
	"key":              true,
	"createdAt":        true,
	"createdBy":        true,
	"createdByOp":      true,
	"lastModifiedAt":   true,
	"lastModifiedBy":   true,
	"lastModifiedByOp": true,
}

// rejectPermissionRoleRewrites is the authoritative commit-time guard making a
// permission vertex's provenance, a role vertex's identity, and a roleindex
// vertex's name→role binding write-once. The package-lifecycle primitives
// (InstallPackage / UpgradePackage / UninstallPackage) carry client-supplied
// mutation bodies that no DDL gates — step 6 resolves no governing DDL for
// class "permission", "role", or "roleindex" — so without this guard an actor
// holding one of them can rewrite an entity it already holds a grant through,
// with no new grant step.
//
// Four shapes are guarded:
//
//   - A vtx.permission.<id> root: every field in permissionProvenanceFields
//     must match the stored document. A permission's key is content-addressed
//     on (package, operationType, scope), so a legitimate upgrade never changes
//     one on a surviving key — a real change yields a different key.
//
//   - A vtx.role.<id> root: the whole body is write-once. The installer creates
//     a role root with an empty data object and UpdateRole mutates only the
//     .description aspect, so no legitimate path writes this document at all.
//     Whole-body equality is what stops a top-level field from shadowing a
//     same-named aspect: the rule engine resolves a node property from the root
//     body first and only falls back to reading the aspect key, so a root that
//     gained a canonicalName field would answer every
//     `role.canonicalName.data.value` read in the real aspect's place —
//     including the kernel root-grant lens.
//
//   - A vtx.role.<id>.canonicalName aspect: its value must match the stored
//     aspect, closing the same redirection at the aspect itself.
//
//   - A vtx.roleindex.<id> root: the whole body is write-once, same as the
//     role root above. A roleindex vertex is the canonical-name→role lookup
//     (data.canonicalName, data.roleId); a rewritten data.roleId would, once a
//     consumer resolves through it, redirect a canonical role name to a
//     different role's grants with no new grant step.
//
// Like the protected-key guard this is path-independent — it fires for any
// script or future primitive emitting such a mutation, and does not rely on a
// Starlark-layer check to have run. A create is out of scope: there is no
// stored body to hold write-once. A bare tombstone is out of scope too, because
// buildMutationValue seeds the written body from the stored document and a
// tombstone that carries no document has nothing to overlay onto it; a
// tombstone that DOES carry a document overlays it exactly as an update does,
// so it is held to the same comparison.
func rejectPermissionRoleRewrites(mutations []MutationOp, prior priorDocs) error {
	for _, m := range mutations {
		switch m.Op {
		case "update":
		case "tombstone":
			if m.Document == nil {
				continue
			}
		default:
			continue
		}

		switch {
		case keys.IsVertexKeyOfType(m.Key, "permission"):
			priorDoc, found, decoded := prior.lookup(m.Key)
			if !found {
				continue
			}
			if !decoded {
				return &PermissionProvenanceError{Key: m.Key, Field: "document"}
			}
			priorData, _ := priorDoc["data"].(map[string]interface{})
			newData, _ := m.Document["data"].(map[string]interface{})
			for _, f := range permissionProvenanceFields {
				if _, present := priorData[f]; !present && healableProvenanceFields[f] {
					continue
				}
				if !docFieldEqual(priorData, newData, f) {
					return &PermissionProvenanceError{Key: m.Key, Field: f}
				}
			}

		case keys.IsVertexKeyOfType(m.Key, "role"):
			priorDoc, found, decoded := prior.lookup(m.Key)
			if !found {
				continue
			}
			if !decoded {
				return &PermissionProvenanceError{Key: m.Key, Field: "document"}
			}
			// An update writes the whole value rather than merging into the
			// stored one, so a field dropped from the mutation is erased just as
			// surely as a changed one — hence the union of both bodies' fields,
			// walked in a fixed order so the reported Field is stable.
			//
			// isDeleted is excluded: it is the entity's liveness flag, which the
			// tombstone path owns and which readers treat as false when absent,
			// so it can differ between a stored body and a faithful resupply
			// without redirecting a single read. Reviving a tombstoned role is a
			// separate mechanism from rewriting a live one's identity.
			for _, f := range unionFields(priorDoc, m.Document) {
				if committerManagedFields[f] || f == "isDeleted" {
					continue
				}
				if !docFieldEqual(priorDoc, m.Document, f) {
					return &PermissionProvenanceError{Key: m.Key, Field: f}
				}
			}

		// A vtx.roleindex.<id> root is a flat name→role lookup document with no
		// aspects, so its whole root is the write-once surface — a rewritten
		// data.roleId would, once a consumer resolves through it, redirect a
		// canonical role name to a different role's grants with no new grant
		// step. roleindex is aspect-free by construction today; a future
		// vtx.roleindex.<id>.<something> aspect would need its own arm here,
		// mirroring how the role case needed both a root arm and a
		// .canonicalName aspect arm.
		case keys.IsVertexKeyOfType(m.Key, "roleindex"):
			priorDoc, found, decoded := prior.lookup(m.Key)
			if !found {
				continue
			}
			if !decoded {
				return &PermissionProvenanceError{Key: m.Key, Field: "document"}
			}
			for _, f := range unionFields(priorDoc, m.Document) {
				if committerManagedFields[f] || f == "isDeleted" {
					continue
				}
				if !docFieldEqual(priorDoc, m.Document, f) {
					return &PermissionProvenanceError{Key: m.Key, Field: f}
				}
			}

		default:
			_, vType, _, local, ok := keys.ParseAspectKey(m.Key)
			if !ok || vType != "role" || local != "canonicalName" {
				continue
			}
			priorDoc, found, decoded := prior.lookup(m.Key)
			if !found {
				continue
			}
			if !decoded {
				return &PermissionProvenanceError{Key: m.Key, Field: "document"}
			}
			priorData, _ := priorDoc["data"].(map[string]interface{})
			newData, _ := m.Document["data"].(map[string]interface{})
			if !docFieldEqual(priorData, newData, "value") {
				return &PermissionProvenanceError{Key: m.Key, Field: "value"}
			}
		}
	}
	return nil
}

// docFieldEqual compares one field across two documents. Presence is compared
// before value so adding a field where none was stored, or dropping one that
// was, counts as a change. Values are compared as canonical JSON: the stored
// document decodes numbers as json.Number where a script supplies float64, and
// a field may hold a list (data.lanes) or a nested object rather than a scalar.
func docFieldEqual(prior, next map[string]interface{}, field string) bool {
	priorVal, priorOK := prior[field]
	nextVal, nextOK := next[field]
	if priorOK != nextOK {
		return false
	}
	if field == "lanes" {
		// platformLaneGate tests membership, so a reordering grants exactly
		// what the stored order granted and only the set is write-once.
		if p, ok := stringSet(priorVal); ok {
			n, ok := stringSet(nextVal)
			return ok && p == n
		}
	}
	return canonicalJSON(priorVal) == canonicalJSON(nextVal)
}

// unionFields lists every field name appearing in either document, sorted so a
// comparison walking them reports the same field on every run.
func unionFields(a, b map[string]interface{}) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, doc := range []map[string]interface{}{a, b} {
		for f := range doc {
			if _, dup := seen[f]; dup {
				continue
			}
			seen[f] = struct{}{}
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// stringSet renders a list of strings order-independently. It reports false for
// any other shape, so a caller falls back to an exact comparison rather than
// treating an unexpected value as equal to something.
func stringSet(v any) (string, bool) {
	items, ok := v.([]interface{})
	if !ok {
		return "", false
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, ok := it.(string)
		if !ok {
			return "", false
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return strings.Join(out, "\x00"), true
}

// canonicalJSON renders a value for comparison. json.Marshal sorts map keys, so
// two equal objects render identically regardless of insertion order. A value
// that cannot be marshalled renders as a unique token so it never compares
// equal to anything, including another unmarshallable value.
func canonicalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("\x00unmarshallable:%T:%v", v, v)
	}
	return string(b)
}

// docIsProtected reports whether a stored document carries data.protected.
func docIsProtected(doc map[string]interface{}) bool {
	if doc == nil {
		return false
	}
	data, ok := doc["data"].(map[string]interface{})
	if !ok {
		return false
	}
	prot, ok := data["protected"].(bool)
	return ok && prot
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

// conditionRevision reports the revision an update/tombstone mutation will be
// batch-conditioned on, and whether that condition is a FALLBACK — step 8's own
// prior-read rather than an already-set ExpectedRevision (a §3.2 default or an
// explicit caller CAS, indistinguishable by this point and both handled
// identically here). ok is false when the mutation has neither an explicit
// revision nor a prior document to condition on (a write to a key that does
// not yet exist — stays unconditioned, as today).
func conditionRevision(m MutationOp, prior priorDocs) (rev uint64, ok bool, fallback bool) {
	if m.ExpectedRevision != nil {
		return *m.ExpectedRevision, true, false
	}
	if p, found := prior[m.Key]; found && p.Found {
		return p.Revision, true, true
	}
	return 0, false, false
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

// offendingValueOp finds the first non-delete op whose value exceeds limit,
// mirroring the substrate's own pre-flight value-size guard so the typed
// BatchTooLargeError can report the specific key and actual size.
func offendingValueOp(ops []substrate.BatchOp, limit int) (key string, actual int) {
	for _, op := range ops {
		if !op.Delete && len(op.Value) > limit {
			return op.Key, len(op.Value)
		}
	}
	return "", 0
}
