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

// PackageScopeError is the typed step-8 failure surfaced when a package-
// lifecycle operation (InstallPackage / UpgradePackage / UninstallPackage)
// submits a mutation outside the surface the named package owns. Reason
// distinguishes the rules:
//
//   - "holdsRole" — the mutation key is a holdsRole link. No package Definition
//     field produces one; identity-role assignment is RBAC's op surface, never a
//     package diff's.
//
//   - "linkSource" — a created link hangs off a source endpoint the package
//     neither created in this batch nor already owned, so the package is minting
//     an edge on an existing foreign entity (the kernel's own permission, an
//     identity). Key is the link key.
//
//   - "aspectParent" — a created aspect hangs off a vertex root the package
//     neither created in this batch nor already owned. Key is the aspect key.
//
//   - "unscoped" — an update or tombstone targets a key outside the named
//     package's owned surface, so the package is reaching into another
//     package's (or the kernel's) entities. Also carries an upgrade or uninstall
//     whose payload names no package at all, which identifies no surface.
//
//   - "manifestClaim" — a manifest the batch creates or updates declares a key
//     the package cannot claim: on a create, anything it does not also mint
//     here; on an update, anything it neither creates here, nor already
//     declared, nor is reviving out of a tombstoned state. Key is the claimed
//     key, not the manifest. Unchecked, the manifest is a self-serve ownership
//     grant — and on the create path, one mintable from nothing.
//
// All map to the same wire code and the same remediation: this mutation does
// not belong to the package that submitted it. The commit path maps this onto a
// `rejected` reply with code `PackageScope`.
type PackageScopeError struct {
	Key     string // the offending mutation key, or the claimed key for "manifestClaim"
	Op      string // the mutation op (create|update|tombstone)
	Reason  string // "holdsRole" | "linkSource" | "aspectParent" | "unscoped" | "manifestClaim"
	Package string // the package the operation named (empty for an install)
}

func (e *PackageScopeError) Error() string {
	switch e.Reason {
	case "holdsRole":
		return fmt.Sprintf("PackageScope: package-lifecycle %s on %s targets a holdsRole link, which no package declares", e.Op, e.Key)
	case "linkSource":
		return fmt.Sprintf("PackageScope: %s of link %s hangs off a source endpoint package %q does not own", e.Op, e.Key, e.Package)
	case "aspectParent":
		return fmt.Sprintf("PackageScope: %s of aspect %s hangs off a vertex package %q does not own", e.Op, e.Key, e.Package)
	case "manifestClaim":
		return fmt.Sprintf("PackageScope: package %q's manifest declares %s, which it does not own and is not minting here", e.Package, e.Key)
	default:
		return fmt.Sprintf("PackageScope: %s on %s is outside package %q's owned keys", e.Op, e.Key, e.Package)
	}
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
//
// prior is the map ReadPrior produced for the validated mutation set. Commit
// tops it up for any update/tombstone key it was handed that the map does not
// cover (the task auto-completion's injected update), so the guards and the
// body preservation below never see a missing entry. A nil map is legal and
// costs a full pass.
func (c *CommitterImpl) Commit(ctx context.Context, env *OperationEnvelope, result ScriptResult, tracker Tracker, prior PriorDocs) (CommitAck, error) {
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
	prior, err = c.topUpPrior(ctx, result.Mutations, prior)
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
	// Package-lifecycle scoping, same enforcement point: a package diff hangs
	// edges only off its own entities, and an upgrade's or uninstall's
	// update/tombstone set stays inside the surface the named package's own
	// stored manifest declares — a manifest this guard validates rather than
	// trusts, since it is the document that set is read from.
	if err := c.rejectPackageScopeViolations(ctx, env, result.Mutations, prior); err != nil {
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
			// document the prior pass read at step 5.5 — the whole document, for a
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
	// carries the KV Create() semantics Contract #4 §4.4 names: when step 2
	// observed an operator-tombstoned tracker value still occupying the subject
	// (SupersedesRevision non-nil, the §4.4 retry signal), the write is
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
//   - tombstone — the written body is the prior document WHOLE, and only
//     `isDeleted` + the lastModified triplet change, so a tombstoned link keeps
//     the `class`/`sourceVertex`/`targetVertex` that make it readable as a link,
//     and a tombstoned entity keeps the provenance a later revive needs. A
//     tombstone carries no document (Contract #3 §3.3 — one supplied is not
//     honored, and the mutation parser refuses the shape outright), and none is
//     read here: a tombstone can never modify, blank, or reclaim the stored
//     body.
func buildMutationValue(env *OperationEnvelope, m MutationOp, at time.Time, trackerKey string, prior map[string]interface{}) ([]byte, error) {
	stamp := substrate.FormatTimestamp(at)

	// A fresh map, so we mutate neither the caller's struct nor the commit's
	// prior-document cache.
	doc := map[string]interface{}{}
	if m.Op == "tombstone" {
		for k, v := range prior {
			doc[k] = v
		}
	} else {
		for k, v := range m.Document {
			doc[k] = v
		}
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

// priorDoc is a stored document read by the prior pass, with the revision it
// was read at. Doc is nil when the key is absent, and also when the stored
// value did not parse — Found stays true for the latter, so a consumer that
// must tell the two apart uses PriorDocs.lookup.
//
// Absent and undecodable are both permissive for the step-8 guards: an
// unparseable body yields nothing to preserve and cannot be confirmed
// protected, and one corrupt value must not wedge every commit that touches
// its root. The step-6 stored-class gate draws the line differently, because it
// reads the body to decide what governs the write: it admits a TOMBSTONE of an
// undecodable body (nothing readable is written forward, and a corrupt key must
// stay removable) and refuses an UPDATE of one.
type priorDoc struct {
	Doc      map[string]interface{}
	Revision uint64
	Found    bool
}

// PriorDocs is the per-operation cache of stored documents, keyed by KV key.
// One KVGet per distinct key per operation serves the step-6 stored-class
// gate, the protected-key guard, and provenance preservation.
//
// A key with an entry has been read, and the entry's Found reports whether Core
// KV held anything — but the accessors below do NOT expose that distinction:
// both doc() and lookup() answer "absent" for a key the map never read. That is
// sound because the production pipeline always runs ReadPrior over the mutation
// set before Validate, and Commit tops the map up for anything appended after,
// so every key a consumer asks about has been read. A caller that skips those
// passes — a test handing Commit a nil map, say — validates and commits as if
// every key were absent, which is the permissive direction.
type PriorDocs map[string]priorDoc

func (p PriorDocs) doc(key string) map[string]interface{} { return p[key].Doc }

// lookup reports the stored document behind a key, separating the two states
// doc() collapses into one. found is false when Core KV held nothing at the key
// (and, per the type doc above, when the key was never read at all —
// indistinguishable here, and permissive either way). decoded is false when an
// entry exists but its body did not parse: readPriorDoc keeps such an entry
// rather than failing the commit, so a consumer that reads the body to reach a
// verdict has to treat it as a body it cannot read, not as an absent one.
func (p PriorDocs) lookup(key string) (doc map[string]interface{}, found, decoded bool) {
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

// ReadPrior reads the stored document for every update/tombstone MUTATION key.
// Reading these — rather than trusting step-4 hydration — keeps the reads
// unconditional: a tombstone carries no script document at all, and a script
// that never declared the key as a read still must not erase what it did not
// supply, nor escape the gate on the class stored there.
//
// It runs at step 5.5, once per pipeline attempt, ahead of step 6: the class
// stored at an update's or tombstone's key is what governs that mutation
// (Contract #1 §1.5), so the gate needs the same documents step 8 needs for
// preservation. Every key reaching here has already passed the batch-wide
// key-shape pre-pass (validateMutationKeyShapes), which is what keeps a
// malformed key a terminal keyPattern refusal rather than an ErrInvalidKey the
// caller must treat as a retryable read fault.
//
// The protected ROOTS the step-8 guards read are deliberately NOT in this pass;
// Commit reads them itself (see commitKeysFor). The split is by consumer: a
// mutation key read here is also the key the batch is conditioned on, so a
// concurrent write between this read and the commit makes the batch conflict
// and the whole pipeline re-run. An aspect's parent root is in no batch and
// conditions nothing, so a read of it here would let a root turn protected
// between step 6 and step 8 with no one the wiser. Total round trips per commit
// are unchanged either way: each key is read once.
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
// deadline and livelock on redelivery. An error is a read fault: the caller
// redelivers, never refuses and never proceeds on a partial map.
func (c *CommitterImpl) ReadPrior(ctx context.Context, mutations []MutationOp) (PriorDocs, error) {
	return c.readKeys(ctx, priorKeysFor(mutations, nil))
}

// priorKeysFor lists the distinct update/tombstone MUTATION keys a prior pass
// must read, skipping any key `have` already holds an entry for. A nil `have`
// asks for the whole set.
func priorKeysFor(mutations []MutationOp, have PriorDocs) []string {
	keys := make([]string, 0, len(mutations))
	seen := map[string]struct{}{}
	for _, m := range mutations {
		if m.Op != "update" && m.Op != "tombstone" {
			continue
		}
		addPriorKey(&keys, seen, have, m.Key)
	}
	return keys
}

// commitKeysFor lists what Commit's own pass must read on top of the map it was
// handed: every update/tombstone mutation key the map does not cover (a
// mutation appended after validation), then every distinct protected root the
// step-8 guards consult that neither the map nor this list already covers.
//
// Roots come second and last so they are read at COMMIT time — the guards'
// verdict on a root that turned protected after step 6 must be the fresh one.
// A root the handed map already holds needs no re-read: that only happens when
// the root is itself a mutation key, and such a key is batch-conditioned on the
// revision it was read at, so a concurrent flip makes the commit conflict and
// the pipeline re-run from hydration.
func commitKeysFor(mutations []MutationOp, have PriorDocs) []string {
	keys := priorKeysFor(mutations, have)
	seen := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		seen[k] = struct{}{}
	}
	for _, m := range mutations {
		if m.Op != "update" && m.Op != "tombstone" {
			continue
		}
		addPriorKey(&keys, seen, have, protectedRootKey(m.Key))
	}
	return keys
}

// addPriorKey appends key to keys unless it is empty, already listed, or
// already held by have.
func addPriorKey(keys *[]string, seen map[string]struct{}, have PriorDocs, key string) {
	if key == "" {
		return
	}
	if _, dup := seen[key]; dup {
		return
	}
	if _, known := have[key]; known {
		return
	}
	seen[key] = struct{}{}
	*keys = append(*keys, key)
}

// topUpPrior returns prior extended with what Commit's consumers need and the
// step-5.5 pass did not read: the protected roots of every aspect mutation,
// read fresh here, and any mutation key the handed map does not cover.
//
// The mutation set Commit receives is not always the set step 6 validated — the
// task auto-completion appends an update of the task root after validation, and
// re-derives it on a batch conflict — and every consumer of the map degrades
// silently on a missing entry: provenance would be re-stamped from the current
// operation and the guards would read the injected key as absent.
func (c *CommitterImpl) topUpPrior(ctx context.Context, mutations []MutationOp, prior PriorDocs) (PriorDocs, error) {
	missing := commitKeysFor(mutations, prior)
	if len(missing) == 0 {
		if prior == nil {
			return PriorDocs{}, nil
		}
		return prior, nil
	}
	read, err := c.readKeys(ctx, missing)
	if err != nil {
		return nil, err
	}
	merged := make(PriorDocs, len(prior)+len(read))
	for k, pd := range prior {
		merged[k] = pd
	}
	for k, pd := range read {
		merged[k] = pd
	}
	return merged, nil
}

// readKeys performs the bounded concurrent pass over an explicit key list.
func (c *CommitterImpl) readKeys(ctx context.Context, keys []string) (PriorDocs, error) {
	if len(keys) == 0 {
		return PriorDocs{}, nil
	}

	rctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	prior := make(PriorDocs, len(keys))
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
// commit's document cache, so multiple aspects of one root cost a single KVGet
// — and they are read at COMMIT time (topUpPrior), not with the step-5.5
// stored-class pass, so a root that turns protected after step 6 is still seen
// by this guard. A root that does not exist is not protected. create mutations
// are skipped — create-only conflicts on overwrite already.
func rejectProtectedMutations(mutations []MutationOp, prior PriorDocs) error {
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
// Like the protected-key guard, the update arm is path-independent — it fires
// for any script or future primitive emitting such a mutation, and does not
// rely on a Starlark-layer check to have run. A create is out of scope: there
// is no stored body to hold write-once. A tombstone is out of scope by
// construction, not by policy: it carries no document (the mutation parser
// refuses one) and buildMutationValue writes the stored body back whole,
// flipping only isDeleted, so there is no supplied value for this guard to
// compare — hence the bare `continue` on that arm.
func rejectPermissionRoleRewrites(mutations []MutationOp, prior PriorDocs) error {
	for _, m := range mutations {
		switch m.Op {
		case "update":
		case "tombstone":
			continue
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

// packageLifecycleOps are the three operation types that carry a client-supplied
// mutation batch on behalf of a named Capability Package.
func isPackageLifecycleOp(name string) bool {
	switch name {
	case "InstallPackage", "UpgradePackage", "UninstallPackage":
		return true
	}
	return false
}

// packageManifestLocalName is the aspect a package records its declared-key set
// under. Its key shape — an aspect of a vtx.package root — is what identifies a
// manifest, not the name a payload claims: a batch forging one for a package it
// does not name is still forging a manifest.
const packageManifestLocalName = "manifest"

// packageLifecycleType names which package-lifecycle primitive an envelope
// actually runs, or "" for an envelope that runs none.
//
// The executing script is selected by CLASS, not by operationType: resolveClass
// (step4_hydrate.go) prefers env.Class, then payload.class, and only then falls
// back to the operationType's registered class. Nothing binds the three
// together, so an envelope carrying class "UpgradePackage" runs the upgrade
// script whatever operationType it declares — and a guard keyed on operationType
// alone would stand down for exactly the envelope that most needs it.
//
// The first two arms mirror resolveClass exactly. The third does not: resolveClass
// resolves an absent class through the DDL cache's command reverse index, where
// this reads the raw operationType. The two agree for the package primitives,
// whose DDLs register their own name as their only permitted command, and reading
// the raw value can only widen what this guard covers, never narrow it.
//
// payloadClass is only consulted when the envelope carries no class of its own,
// because that is the only case resolveClass consults it either — which is what
// lets the caller skip decoding the payload for the overwhelming majority of
// operations, none of which are package-lifecycle ones.
func packageLifecycleType(env *OperationEnvelope, payloadClass string) string {
	for _, candidate := range []string{env.Class, payloadClass, env.OperationType} {
		if isPackageLifecycleOp(candidate) {
			return candidate
		}
	}
	return ""
}

// packageScope is the key surface one package-lifecycle batch is entitled to
// touch: the keys it creates in this very batch, plus — for an upgrade or
// uninstall — the keys the named package's own stored manifest already declared,
// plus whatever that manifest legitimately claims anew in this same batch.
type packageScope struct {
	name        string
	pkgKey      string
	manifestKey string
	// created holds every key this batch creates. A create needs no ownership
	// proof of its own (create-only conflicts on an existing key), but it is
	// what makes a freshly minted entity a legitimate link endpoint, a
	// legitimate aspect parent, and a legitimate new manifest claim.
	created map[string]struct{}
	// owned holds the keys an update or tombstone may target: the prior
	// declared set, the package's own root and manifest aspect, and the new
	// claims validated against the claim rules below.
	owned map[string]struct{}
	// resolved is false when no live manifest backs the name — nothing is
	// owned, and the batch may create but never update or tombstone.
	resolved bool
}

func (s *packageScope) has(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}

func (s *packageScope) ownsOrCreates(key string) bool {
	return s.has(s.created, key) || s.has(s.owned, key)
}

// rejectPackageScopeViolations is the authoritative commit-time guard binding a
// package-lifecycle batch to the surface the package it acts for actually owns.
// The package-lifecycle primitives carry client-supplied mutation bodies that no
// DDL gates, and step 3 authorizes the VERB rather than the target, so without
// this nothing at all scopes the batch. Five rules hold it:
//
//   - No package-lifecycle mutation may touch a holdsRole link. No Definition
//     field and none of the three package scripts produce one — binding an
//     identity to a role is RBAC's own op surface, reached through a grant step
//     a package diff never takes.
//
//   - A manifest is content-validated on CREATE as well as on update. The
//     manifest is the document every ownership answer here is ultimately read
//     from, so an unvalidated create is a way to mint that root of trust from
//     nothing: declare a victim key in a brand-new package's manifest, then
//     return in a second operation and touch it as a key you now "own". On a
//     create only this batch's own creates (and the package root) may be
//     declared — nothing pre-exists to have been declared or dropped before.
//
//   - An update or tombstone must target a key in the package's owned set, and
//     the manifest update that GROWS that set may only add keys the package can
//     claim: created here, already declared, the package's own root/manifest, or
//     a key this batch is itself reviving whose stored document is tombstoned (a
//     dead key has no live owner to displace).
//
//   - A created LINK's source endpoint must be a key this batch creates or the
//     named package already owns. Contract #1 §1.1 makes the source the
//     later-arriving vertex — the entity whose owner is minting the edge — so
//     this is the "you may only hang an edge off your own entity" rule. The
//     TARGET is deliberately unconstrained: a package legitimately grants a
//     permission to the primordial operator role, offers a pane to a role it did
//     not declare, and subtypes its DDL under another package's abstract type.
//     What it closes is an EXISTING foreign entity being linked from — notably a
//     grantedBy edge minted on the kernel's own permission. It does not reach a
//     package minting a fresh permission of its own and granting that (the
//     create-forgery gap, which needs server-side Definition content and stays
//     open).
//
//   - A created ASPECT's parent vertex must likewise be a key this batch creates
//     or the package already owns. Same principle as the link rule: an aspect is
//     a new facet bolted onto an existing entity, and a package may only bolt
//     one onto its own — the more so because a vtx.meta.* write invalidates the
//     DDL cache in-commit, so a forged aspect on a primordial op-meta root is
//     live immediately.
func (c *CommitterImpl) rejectPackageScopeViolations(ctx context.Context, env *OperationEnvelope, mutations []MutationOp, prior PriorDocs) error {
	// Every operation in the platform passes through here, so the envelope's own
	// fields settle the question before the payload is touched: a class-carrying
	// envelope needs no decode at all, and only a class-less one pays for the
	// payload.class arm resolveClass would itself have read.
	payloadClass := ""
	if env.Class == "" {
		if decoded, ok := jsonToGenericMap(env.Payload); ok {
			payloadClass, _ = decoded["class"].(string)
		}
	}
	lifecycle := packageLifecycleType(env, payloadClass)
	if lifecycle == "" {
		return nil
	}
	payload, payloadDecoded := jsonToGenericMap(env.Payload)

	for _, m := range mutations {
		if _, _, linkName, _, _, ok := keys.ParseLinkKey(m.Key); ok && linkName == "holdsRole" {
			return &PackageScopeError{Key: m.Key, Op: m.Op, Reason: "holdsRole"}
		}
	}

	scope, err := c.resolvePackageScope(ctx, lifecycle, payload, payloadDecoded, mutations, prior)
	if err != nil {
		return err
	}

	if err := rejectForgedManifestCreates(scope, mutations); err != nil {
		return err
	}

	for _, m := range mutations {
		if m.Op == "create" {
			continue
		}
		if !scope.resolved || !scope.has(scope.owned, m.Key) {
			return &PackageScopeError{Key: m.Key, Op: m.Op, Reason: "unscoped", Package: scope.name}
		}
	}

	for _, m := range mutations {
		if m.Op != "create" {
			continue
		}
		if type1, id1, _, _, _, ok := keys.ParseLinkKey(m.Key); ok {
			if !scope.ownsOrCreates(keys.VertexKey(type1, id1)) {
				return &PackageScopeError{Key: m.Key, Op: m.Op, Reason: "linkSource", Package: scope.name}
			}
			continue
		}
		if parent, _, _, _, ok := keys.ParseAspectKey(m.Key); ok {
			if !scope.ownsOrCreates(parent) {
				return &PackageScopeError{Key: m.Key, Op: m.Op, Reason: "aspectParent", Package: scope.name}
			}
		}
	}
	return nil
}

// rejectForgedManifestCreates validates every package-manifest aspect the batch
// CREATES, whichever package name the payload claims — the key shape is what
// makes a document a manifest. Nothing pre-exists a created manifest, so the only
// keys it may declare are the ones this same batch mints, plus the package root
// it hangs off. A real install satisfies this by construction: build.go's
// addCreate appends every created key to the very slice that becomes
// declaredKeys, so the declared set IS the create set.
func rejectForgedManifestCreates(scope *packageScope, mutations []MutationOp) error {
	for _, m := range mutations {
		if m.Op != "create" || m.Document == nil {
			continue
		}
		parent, parentType, _, localName, ok := keys.ParseAspectKey(m.Key)
		if !ok || parentType != "package" || localName != packageManifestLocalName {
			continue
		}
		for k := range declaredKeySet(m.Document) {
			if k == parent || scope.has(scope.created, k) {
				continue
			}
			return &PackageScopeError{Key: k, Op: m.Op, Reason: "manifestClaim", Package: scope.name}
		}
	}
	return nil
}

// resolvePackageScope assembles the surface a batch owns. InstallPackage names
// no installed package and is create-only, so its surface is exactly what it
// creates. An upgrade or uninstall resolves the named package's vertex key
// server-side and reads its manifest: a name that resolves to no live manifest
// yields an unresolved scope, which owns nothing.
func (c *CommitterImpl) resolvePackageScope(ctx context.Context, lifecycle string, payload map[string]interface{}, payloadDecoded bool, mutations []MutationOp, prior PriorDocs) (*packageScope, error) {
	scope := &packageScope{
		created: make(map[string]struct{}, len(mutations)),
		owned:   map[string]struct{}{},
	}
	for _, m := range mutations {
		if m.Op == "create" {
			scope.created[m.Key] = struct{}{}
		}
	}
	if lifecycle == "InstallPackage" {
		return scope, nil
	}

	name, _ := payload["name"].(string)
	scope.name = name
	if !payloadDecoded || name == "" {
		// An upgrade or uninstall acts FOR a package; a payload that does not
		// decode, or names none, identifies no surface to act within and no
		// legitimate submitter produces one. The package scripts validate the
		// name long before step 8, and this guard is path-independent precisely
		// because it does not depend on their having run.
		if len(mutations) > 0 {
			return nil, &PackageScopeError{Key: mutations[0].Key, Op: mutations[0].Op, Reason: "unscoped"}
		}
		return scope, nil
	}
	scope.pkgKey = keys.VertexKey("package", substrate.PackageEntityNanoID(name, "package"))
	scope.manifestKey = keys.AspectKey(scope.pkgKey, packageManifestLocalName)

	// The batch's own prior read is preferred over a fresh one: an upgrade and
	// an uninstall both mutate the manifest, so it is already loaded AND already
	// the revision the batch conditions on (conditionRevision's fallback), which
	// makes the document validated here the document the commit supersedes. A
	// batch that does not mutate its manifest falls back to an out-of-band read,
	// which no batch condition covers.
	manifest, loaded := prior[scope.manifestKey]
	if !loaded || !manifest.Found {
		read, err := c.readPriorDoc(ctx, scope.manifestKey)
		if err != nil {
			return nil, fmt.Errorf("step 8: read package manifest for %q: %w", name, err)
		}
		manifest = read
	}
	// A tombstoned manifest is an UNINSTALLED package. Its declaredKeys survive
	// in the body a tombstone carries forward, and they still list the retention
	// -class holder keys uninstall deliberately leaves live and undeclared — so
	// honouring it would hand a dead package's stale set to a live batch.
	if !manifest.Found || docIsTombstoned(manifest.Doc) {
		return scope, nil
	}
	scope.resolved = true

	priorDeclared := declaredKeySet(manifest.Doc)
	for k := range priorDeclared {
		scope.owned[k] = struct{}{}
	}
	scope.owned[scope.pkgKey] = struct{}{}
	scope.owned[scope.manifestKey] = struct{}{}

	claims, err := validatedManifestClaims(scope, priorDeclared, mutations, prior)
	if err != nil {
		return nil, err
	}
	for k := range claims {
		scope.owned[k] = struct{}{}
	}
	return scope, nil
}

// validatedManifestClaims reads the declaredKeys the batch's own manifest UPDATE
// submits and returns the keys it adds to the prior set, having proved each one
// claimable. A key is claimable when this batch creates it, when it is the
// package's own root or manifest aspect, or when this batch is itself reviving
// it — an update or tombstone whose stored document is tombstoned. A dead key has
// no live owner to displace, and re-adding an entity a previous version dropped
// is exactly that shape (diffManifest emits the revival as an update, since the
// key still exists). Anything else is a package writing another package's key
// into its own ownership record, which is the whole vulnerability wearing the
// manifest as a disguise.
//
// Requiring the claim to be a mutation of this same batch is also what bounds
// the work: liveness is read from the prior-document map the commit already
// loaded, so a manifest listing thousands of keys costs no reads at all rather
// than one round trip each.
func validatedManifestClaims(scope *packageScope, priorDeclared map[string]struct{}, mutations []MutationOp, prior PriorDocs) (map[string]struct{}, error) {
	revived := map[string]struct{}{}
	for _, m := range mutations {
		if m.Op == "update" || m.Op == "tombstone" {
			revived[m.Key] = struct{}{}
		}
	}
	claims := map[string]struct{}{}
	for _, m := range mutations {
		if m.Op != "update" || m.Key != scope.manifestKey || m.Document == nil {
			continue
		}
		for k := range declaredKeySet(m.Document) {
			if _, already := priorDeclared[k]; already {
				continue
			}
			if k == scope.pkgKey || k == scope.manifestKey || scope.has(scope.created, k) {
				claims[k] = struct{}{}
				continue
			}
			if _, touched := revived[k]; touched && docIsTombstoned(prior.doc(k)) {
				claims[k] = struct{}{}
				continue
			}
			return nil, &PackageScopeError{Key: k, Op: m.Op, Reason: "manifestClaim", Package: scope.name}
		}
	}
	return claims, nil
}

// declaredKeySet reads the key set a package manifest document declares. An
// unreadable or absent list yields an empty set, which constrains every
// update/tombstone the operation carries rather than waving them through: a
// manifest that exists but whose declaredKeys cannot be read is not evidence
// that a key belongs to the package.
func declaredKeySet(manifest map[string]interface{}) map[string]struct{} {
	out := map[string]struct{}{}
	data, ok := manifest["data"].(map[string]interface{})
	if !ok {
		return out
	}
	list, ok := data["declaredKeys"].([]interface{})
	if !ok {
		return out
	}
	for _, k := range list {
		if s, ok := k.(string); ok {
			out[s] = struct{}{}
		}
	}
	return out
}

// docIsTombstoned reports whether a stored document carries the Contract #1
// liveness flag in its deleted state. An absent or unreadable document is not
// tombstoned — a guard must not read "I could not tell" as "it is already dead".
func docIsTombstoned(doc map[string]interface{}) bool {
	if doc == nil {
		return false
	}
	del, ok := doc["isDeleted"].(bool)
	return ok && del
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
func conditionRevision(m MutationOp, prior PriorDocs) (rev uint64, ok bool, fallback bool) {
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
