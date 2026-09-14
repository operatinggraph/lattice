package loom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// Instance status values (Contract #10 §10.3).
const (
	StatusRunning  = "running"
	StatusComplete = "complete"
	StatusFailed   = "failed"
)

// loom-state key prefixes (Contract #10 §10.3). The shapes share the one bucket
// under disjoint prefixes — the same one-bucket / disjoint-prefix pattern
// capability-kv §6.1 uses for cap.ephemeral.*. backfill. is the engine's own
// bookkeeping rather than instance state: one key, whose lifetime is
// failedIndexBackfillSentinelKey's.
const (
	instancePrefix = "instance."
	tokenPrefix    = "token."
	outboxPrefix   = "outbox."
	deadlinePrefix = "deadline."
	backfillPrefix = "backfill."
)

// failedIndexBackfillSentinelKey records that the failed-index backfill has run
// to completion on this bucket, so the pass is a single GET on every start after
// the one that closed it. It is the only key under the backfill. prefix: not an
// instance sub-key, so no instance filter can match it, and no listing in this
// package enumerates it. Its lifetime is the backfill's own doc comment.
const failedIndexBackfillSentinelKey = backfillPrefix + "failedIndex"

// patternPinSuffix is the sub-key suffix of an instance's pinned pattern copy:
// instance.<instanceId>.pattern holds the full pattern definition as loaded at
// trigger time. All step resolution for a running instance reads this pin —
// never the live pattern source — so a pattern update mid-flight cannot
// mis-index the durable cursor against reordered/changed steps. The pin lives
// exactly as long as the instance is live: written in the same AtomicBatch
// that creates instance.<instanceId>, deleted in the terminal batch.
const patternPinSuffix = ".pattern"

// failedMarkerSuffix is the sub-key suffix of an instance's failed index:
// instance.<instanceId>.failed stands for an instance that is failed and
// awaiting an operator redrive. Its body carries nothing — the key's presence is
// the whole signal — because the cursor record stays authoritative for status;
// the marker only makes the failed set ENUMERABLE, which the cursor cannot be
// without reading every body.
//
// It is SETTLED by every terminal batch: written (plain PUT) on the failed arm,
// removed (TTL'd purge) on the complete arm, and removed by redrive's CAS-guarded
// batch. So a marker can disagree with its instance only while that instance is
// RUNNING — a window a stale marker lives in until the instance's next terminal,
// which then settles it either way.
const failedMarkerSuffix = ".failed"

// failedMarkerBody is the body of a failed-index marker: an empty JSON object.
// The key's presence is the signal, so the body carries no field — but it is a
// decodable value rather than a zero-length one, which every removal marker on
// this bucket also is.
const failedMarkerBody = "{}"

// patternPinFilter, failedMarkerFilter and instanceCursorFilter are the
// server-side NATS subject filters that select one instance key family each.
// `*` matches exactly one key token and an instanceId is a dot-free NanoID, so a
// three-token filter matches only its own sub-key family — never the cursor (one
// token after the prefix), never the other family — and the one-token
// instanceCursorFilter is the converse, matching the cursors and no sub-key of
// any family. Every path that has to be RIGHT resolves them through
// KVGetMultiNoSnapshot (the stream's own subject state); the heartbeat's gauge
// lists one of them through a watcher instead, and says why.
const (
	patternPinFilter     = instancePrefix + "*" + patternPinSuffix
	failedMarkerFilter   = instancePrefix + "*" + failedMarkerSuffix
	instanceCursorFilter = instancePrefix + "*"
)

func instanceKey(instanceID string) string { return instancePrefix + instanceID }

// isInstanceRecordKey reports whether k is an instance.<id> cursor record
// rather than one of its sub-keys (the .pattern pin, the .failed index). The
// instanceId is a NanoID (dot-free by construction), so a key under the
// instance. prefix whose remainder contains a '.' belongs to a sub-key family,
// never to a cursor. It is the client-side counterpart of instanceCursorFilter,
// applied to that filter's result by the failed-index backfill.
func isInstanceRecordKey(k string) bool {
	if !strings.HasPrefix(k, instancePrefix) {
		return false
	}
	return !strings.ContainsRune(k[len(instancePrefix):], '.')
}

// instanceIDFromSubKey returns the instanceId of an instance.<id><suffix>
// sub-key, or "" when k is not one of that family (wrong prefix, wrong suffix,
// or no room for an id between them). It is the inverse of patternPinKey /
// failedMarkerKey and the one place a sub-key's id is recovered, so a listing
// leg cannot invent its own string arithmetic.
func instanceIDFromSubKey(k, suffix string) string {
	if len(k) <= len(instancePrefix)+len(suffix) {
		return ""
	}
	if !strings.HasPrefix(k, instancePrefix) || !strings.HasSuffix(k, suffix) {
		return ""
	}
	return k[len(instancePrefix) : len(k)-len(suffix)]
}

// isPatternPinKey reports whether k is an instance.<id>.pattern pin sub-key —
// the shape guard kept beside isInstanceRecordKey and instanceIDFromSubKey so the
// shapes over the instance.* keyspace cannot drift apart.
// The pin is written in the same AtomicBatch that creates instance.<id> and
// removed only in the terminal batch (transition, pinnedDomains' own doc
// comment), so the set of keys this reports true for is exactly the set of
// running instances — runningInstanceCounter (health.go) counts them directly,
// with no body read.
func isPatternPinKey(k string) bool {
	return instanceIDFromSubKey(k, patternPinSuffix) != ""
}

func patternPinKey(instanceID string) string {
	return instancePrefix + instanceID + patternPinSuffix
}

func failedMarkerKey(instanceID string) string {
	return instancePrefix + instanceID + failedMarkerSuffix
}
func tokenKey(token string) string         { return tokenPrefix + token }
func outboxKey(token string) string        { return outboxPrefix + token }
func deadlineKey(instanceID string) string { return deadlinePrefix + instanceID }

// Instance is the persisted per-instance cursor stored in loom-state under
// instance.<instanceId> (Contract #10 §10.3). It is the durable source of truth
// for a running pattern: the cursor (current step index), the pendingToken (the
// requestId of the step currently awaited), and status.
type Instance struct {
	InstanceID   string `json:"instanceId"`
	PatternRef   string `json:"patternRef"`
	SubjectKey   string `json:"subjectKey"`
	Cursor       int    `json:"cursor"`
	PendingToken string `json:"pendingToken"`
	Status       string `json:"status"`
	RetryCount   int    `json:"retryCount"`
	// DeadlineProbe records that a deadline probe reached its rejected-or-lost
	// branch with evidence older than its own lifetime and REFUSED to decide
	// (noteDeadlineProbe). It is an operator-facing fact, never an input to a
	// verdict: nothing reads it to branch, the probe re-derives its answer from
	// the token epoch every time. Nil is the ordinary state — a note is written
	// only by the inconclusive verdict and is cleared the moment the instance
	// moves (a new token, a terminal status, a redrive). Additive JSON: a
	// binary that does not know the field decodes the record and drops the note
	// on its next write.
	DeadlineProbe *ProbeNote `json:"deadlineProbe,omitempty"`
}

// ProbeNote is the durable trace of one inconclusive deadline verdict. At is
// the instant the probe refused, RFC3339 UTC (substrate.FormatTimestamp, the
// same encoding deadlineMark.SetAt uses); Reason is the verdict the probe would
// have written had its evidence still been current, verbatim — so the operator
// reads the same sentence a fail would have carried, under the header that it
// was not asserted.
type ProbeNote struct {
	At     string `json:"at"`
	Reason string `json:"reason"`
}

// tokenPointer is the thin reverse index value stored under token.<pendingToken>
// (Contract #10 §10.3). Its presence is the correlation + idempotency guard.
type tokenPointer struct {
	InstanceID string `json:"instanceId"`
}

// outboxRecord is the command-outbox value stored under outbox.<token> (Contract
// #10 §10.3): the op Loom intends to submit, written in the SAME AtomicBatch as
// the cursor/token transition so submission is not a dual write. The relay
// fire-and-forget publishes it to ops.<lane> and deletes the record on
// publish-ack (re-publish idempotent via the chosen requestId + the Contract #4
// tracker).
type outboxRecord struct {
	RequestID string          `json:"requestId"`
	Operation string          `json:"operation"`
	Payload   json.RawMessage `json:"payload"`
	Target    string          `json:"target,omitempty"`
	Lane      string          `json:"lane"`
	Actor     string          `json:"actor"`
	// Reads is the dispatched op's ContextHint.Reads (the BARE vertex keys its
	// DDL hydrates + validates). The relay copies it onto the op envelope so the
	// Processor hydrates the op's OCC reads. Additive + backward-compatible: an
	// older persisted record with no Reads field decodes to nil → a read-free
	// envelope, exactly as before. NO `.state` suffixes — the DDLs read bare
	// keys; a non-existent `.state` would be a HydrationMiss.
	Reads []string `json:"reads,omitempty"`
	// OptionalReads is the dispatched op's ContextHint.OptionalReads (Contract
	// #2 §2.5 — declared absence-tolerant reads): keys the DDL script reads via
	// kv.Read whose absence is a legitimate branch (CreateTask's dedup key +
	// the assignee's availability aspect). Hydrated when present, recorded
	// known-absent when missing — never a HydrationMiss. Same additive
	// backward-compat as Reads.
	OptionalReads []string `json:"optionalReads,omitempty"`
	// Enumerations is the dispatched op's ContextHint.Enumerations (Contract #2
	// §2.5 class (e) — declared kv.Links link walks): the step's declared
	// enumerations with each Hub already resolved to a concrete vertex key.
	// The relay copies it onto the op envelope as metadata; nothing hydrates
	// from it (the walk runs live and paged inside the script). An older
	// persisted record with no Enumerations field decodes to nil, which is an
	// envelope declaring no walks.
	Enumerations []Enumeration `json:"enumerations,omitempty"`
	// EgressReads is the dispatched op's ContextHint.EgressReads (Contract #2
	// §2.5 class (f), sensitive-param-egress design §3.4): an externalTask
	// instanceOp's subject-templated aspect keys, hydrated ref-if-sensitive
	// rather than plaintext. Same additive backward-compat as Reads.
	EgressReads []string `json:"egressReads,omitempty"`
}

// deadlineMark is the thin value stored under deadline.<instanceId> (Contract
// #10 §10.3): the ARM. It carries a per-key TTL = the current step's deadline,
// and the server's marker for that expiry (Nats-Marker-Reason: MaxAge, which
// decodes as a KeyValuePurge) is the SIGNAL — the off-stream failed/rejected
// backstop (§10.6). The handler keys on that reason header, not on the empty
// body every removal of this key also carries; and the key's own PRESENCE is
// the currency test, since a marker's emission empties the subject and only a
// later step's arm can put a value back. The value itself is
// observability-only: the step-deadline-exceeded probe reconstructs everything
// from instance.<instanceId>.
type deadlineMark struct {
	SetAt string `json:"setAt"`
}

// instanceLister is the narrow substrate.Conn surface the two enumerating reads
// need: the COMPLETE filter resolution (KVGetMultiNoSnapshot — the stream's own
// subject state, never a count-bounded watcher) and the batched exact-key read.
// Nothing here can walk the whole keyspace or enumerate through a watcher, so
// both properties a verdict-bearing read needs are compile-time rather than
// review findings; a test replaces the field to assert the FILTERS these paths
// hand the server, which is the only place a widened or mis-narrowed enumeration
// is visible (a client-side re-check keeps the answer right either way).
type instanceLister interface {
	KVGetMultiNoSnapshot(ctx context.Context, bucket string, keys []string) (map[string]*substrate.KVEntry, error)
	KVGetMulti(ctx context.Context, bucket string, keys []string) (map[string]*substrate.KVEntry, error)
}

// stateStore reads and writes the loom-state key shapes. loom-state is Loom's
// own operational bucket and the only place Loom writes directly (P2); every
// step transition is a single AtomicBatch on the one bucket so the cursor
// update and the reverse-pointer add/delete land all-or-nothing.
type stateStore struct {
	conn   *substrate.Conn
	bucket string
	// lister is the surface pinnedDomains and listInstances read through. It is
	// conn for every production store; a test replaces it to observe the
	// listing calls those two paths make.
	lister instanceLister
}

func newStateStore(conn *substrate.Conn, bucket string) *stateStore {
	return &stateStore{conn: conn, bucket: bucket, lister: conn}
}

// getInstance reads the instance record for instanceID. Returns (nil, nil) when
// the key is absent.
func (s *stateStore) getInstance(ctx context.Context, instanceID string) (*Instance, error) {
	inst, _, err := s.getInstanceAtRevision(ctx, instanceID)
	return inst, err
}

// getInstanceAtRevision reads the instance record together with the KV revision
// it was read at, for a caller that writes it back under a compare-and-set (the
// redrive race guard). Returns (nil, 0, nil) when the key is absent.
func (s *stateStore) getInstanceAtRevision(ctx context.Context, instanceID string) (*Instance, uint64, error) {
	entry, err := s.conn.KVGet(ctx, s.bucket, instanceKey(instanceID))
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("loom: read instance %q: %w", instanceID, err)
	}
	var inst Instance
	if err := json.Unmarshal(entry.Value, &inst); err != nil {
		return nil, 0, fmt.Errorf("loom: unmarshal instance %q: %w", instanceID, err)
	}
	return &inst, entry.Revision, nil
}

// listInstances reads the cursor record of every instance an operator can still
// act on: those running, and those failed and awaiting a redrive. The two sets
// are the two index sub-key families — instance.*.pattern for the running set,
// instance.*.failed for the failed set — and both are resolved in ONE
// KVGetMultiNoSnapshot over the two filters, then their instanceIds feed one
// KVGetMulti over the cursor records themselves. The cursor family is not
// enumerated, so a completed instance's retained record (permanent, and the dedup
// evidence that collapses a re-emitted trigger) costs this read nothing.
//
// Why that primitive and not a key listing. It answers from the STREAM's own
// subject state — one `multi_last` under the stream lock, or a subject-filtered
// STREAM.INFO plus chunked exact-key reads past the 1,024-subject cap — so the
// enumeration cannot come back SHORT. A watcher-backed key listing can: it stops
// on a delivered-message count, so a rewrite landing mid-enumeration on an
// already-delivered subject ends it with keys undelivered and no error
// (substrate's KVListKeysFilter, whose callers tolerate a hint). instance.*.pattern
// is the most-rewritten family in this bucket — one create and one rollup purge per
// instance — and this surface is the operator's redrive queue, a Contract #10
// promise. It may not rest on a hint. The cost of the completeness is that the pin
// BODIES cross the wire for the running set; that is bounded by the actionable
// population, which is the same bound as the answer.
//
// An instance named by both families (a marker a concurrent redrive has just made
// stale, or the converse) is read once: the union is over instanceIds.
//
// The record is authoritative for status, the index only for membership, so a
// record whose Status is complete is EXCLUDED — a completed instance is not
// enumerable here, and the index that named it is not evidence against its own
// cursor. Which family named it decides how loudly that is said, because the two
// mean different things. Named by the PIN family only, it is the benign
// resolve-then-read race every batched read has: the instance completed in the
// window between the resolution and the cursor read, so the answer is one entry
// newer than the resolution — Debug. Named by the FAILED family, the index
// disagrees with a record that is not going to change again, which is a stale
// marker worth an operator's attention — Warn.
//
// A key absent from the cursor read (removed between the resolution and the read)
// is skipped, and a per-key unmarshal failure skips that record (logged) rather
// than failing the whole list — one poisoned record must not blind the operator to
// every other instance. A failure of either read, in contrast, fails the whole
// call: both are complete-or-error, so unlike a single poisoned record a failure
// means this read cannot answer for ANY instance, and a partial or empty list
// would be silently wrong rather than degraded — the same fail-closed posture
// pinnedDomains below takes.
//
// Each record is decoded directly with no isDeleted soft-delete check. That is
// correct because Loom never soft-deletes an instance cursor record: a terminal
// is recorded by flipping Status in place, never by writing an isDeleted
// envelope over instance.<id>. So every record fetched here is a live record.
//
// Residual, by construction: a RUNNING instance whose pattern pin is absent is
// named by neither family, so it is not listed. That state is an invariant break
// the engine already converts into a failed terminal the next time it touches the
// instance (errPatternPinMissing ⇒ fail ⇒ the failed index), which is what
// surfaces it here.
func (s *stateStore) listInstances(ctx context.Context, logger *slog.Logger) ([]Instance, error) {
	// legs records which family named each instance, so an excluded complete
	// record can be classified as a race (pin family) or a stale index (failed
	// family).
	type legs struct{ viaPin, viaFailed bool }
	indexed, err := s.lister.KVGetMultiNoSnapshot(ctx, s.bucket, []string{patternPinFilter, failedMarkerFilter})
	if err != nil {
		return nil, fmt.Errorf("loom: list instances: resolve %q ∪ %q: %w", patternPinFilter, failedMarkerFilter, err)
	}
	named := make(map[string]legs, len(indexed))
	for k := range indexed {
		if id := instanceIDFromSubKey(k, patternPinSuffix); id != "" {
			seen := named[id]
			seen.viaPin = true
			named[id] = seen
			continue
		}
		if id := instanceIDFromSubKey(k, failedMarkerSuffix); id != "" {
			seen := named[id]
			seen.viaFailed = true
			named[id] = seen
		}
	}
	recordKeys := make([]string, 0, len(named))
	for id := range named {
		recordKeys = append(recordKeys, instanceKey(id))
	}
	sort.Strings(recordKeys)

	entries, err := s.lister.KVGetMulti(ctx, s.bucket, recordKeys)
	if err != nil {
		return nil, fmt.Errorf("loom: list instances: read %d cursor records: %w", len(recordKeys), err)
	}
	out := make([]Instance, 0, len(recordKeys))
	for _, k := range recordKeys {
		entry, present := entries[k]
		if !present {
			// Removed between the resolution and the read — skip.
			continue
		}
		var inst Instance
		if err := json.Unmarshal(entry.Value, &inst); err != nil {
			logger.Warn("loom: instance record unparseable; skipping", "key", k, "err", err)
			continue
		}
		if inst.Status == StatusComplete {
			if named[inst.InstanceID].viaFailed {
				logger.Warn("loom: failed index names an instance whose record reads complete; excluding",
					"key", k, "status", inst.Status)
			} else {
				logger.Debug("loom: instance completed under the read; excluding",
					"key", k, "status", inst.Status)
			}
			continue
		}
		out = append(out, inst)
	}
	return out, nil
}

// resolveToken reads the token.<token> reverse pointer, returning the instanceId
// it points at. ok is false when the pointer is absent (already advanced, or not
// a token Loom is awaiting) — the pointer's presence is the correlation guard
// (Contract #10 §10.6).
func (s *stateStore) resolveToken(ctx context.Context, token string) (instanceID string, ok bool, err error) {
	entry, err := s.conn.KVGet(ctx, s.bucket, tokenKey(token))
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("loom: resolve token %q: %w", token, err)
	}
	var ptr tokenPointer
	if err := json.Unmarshal(entry.Value, &ptr); err != nil {
		return "", false, fmt.Errorf("loom: unmarshal token pointer %q: %w", token, err)
	}
	return ptr.InstanceID, true, nil
}

// createInstance writes the initial instance.<id> cursor and its pattern pin
// (instance.<id>.pattern — the full definition as loaded at trigger time) in
// one AtomicBatch, both CreateOnly. The trigger consumer's idempotency hinges
// on the create semantics: a duplicate trigger for the same instanceId finds
// the key present and skips, and the CreateOnly rejection is the race guard
// for two triggers passing the existence check concurrently. Because the pin
// rides the same batch, a live running instance ALWAYS has a pin — a missing
// pin is an invariant break, never a fallback case. No token is written yet —
// step 0's submission write-aheads its token via transition.
//
// The pin keeps its CreateOnly write even though redrive's cannot. The two are
// not the same situation: a CreateOnly write is refused by any subject that
// still carries a marker, and a terminal batch's removal leaves one on the pin
// for the marker's lifetime. Redrive must succeed against exactly that state
// and must not depend on when the marker expires, so its guard sits on a CAS
// of the cursor instead. createInstance runs only when no cursor exists at all,
// which for a live instanceId means it never ran — so the pin subject is
// genuinely empty and CreateOnly is both correct and the tighter guard.
func (s *stateStore) createInstance(ctx context.Context, inst *Instance, pattern *Pattern) error {
	body, err := json.Marshal(inst)
	if err != nil {
		return fmt.Errorf("loom: marshal instance %q: %w", inst.InstanceID, err)
	}
	pinBody, err := json.Marshal(pattern)
	if err != nil {
		return fmt.Errorf("loom: marshal pattern pin %q: %w", inst.InstanceID, err)
	}
	ops := []substrate.BatchOp{
		{Bucket: s.bucket, Key: instanceKey(inst.InstanceID), Value: body, CreateOnly: true},
		{Bucket: s.bucket, Key: patternPinKey(inst.InstanceID), Value: pinBody, CreateOnly: true},
	}
	if _, err := s.conn.AtomicBatch(ctx, ops); err != nil {
		return fmt.Errorf("loom: create instance %q: %w", inst.InstanceID, err)
	}
	return nil
}

// errPatternPinMissing reports that instance.<id>.pattern is absent. The pin is
// written atomically with the instance and deleted only in the terminal batch,
// so for a live running instance absence is an invariant break — never a
// fallback-to-live-source case. Callers match on this sentinel to turn the
// break into an operator-visible failed terminal (§10.6: never a silent wedge)
// instead of an infinite redelivery loop; any other pin-read error stays a
// retryable error.
var errPatternPinMissing = errors.New("pattern pin missing for live instance (pin is written atomically with the instance)")

// getPinnedPattern reads the instance's pinned pattern definition
// (instance.<id>.pattern). A missing pin returns errPatternPinMissing (wrapped);
// the live pattern source is never substituted.
func (s *stateStore) getPinnedPattern(ctx context.Context, instanceID string) (*Pattern, error) {
	entry, err := s.conn.KVGet(ctx, s.bucket, patternPinKey(instanceID))
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return nil, fmt.Errorf("loom: instance %q: %w", instanceID, errPatternPinMissing)
		}
		return nil, fmt.Errorf("loom: read pattern pin %q: %w", instanceID, err)
	}
	var p Pattern
	if err := json.Unmarshal(entry.Value, &p); err != nil {
		return nil, fmt.Errorf("loom: unmarshal pattern pin %q: %w", instanceID, err)
	}
	return &p, nil
}

// pinnedDomains enumerates the completion domains of every LIVE instance's
// pinned pattern. Pins are removed in the terminal batch, so the
// instance.*.pattern family IS the live set — this is the second leg of the
// reconcile union (an in-flight instance keeps its completion domain's consumer
// alive even after its pattern is removed/updated-away; the consumer drains once
// the last live instance pinning that domain completes).
//
// One round trip, and a complete one. KVGetMultiNoSnapshot resolves the filter
// from the STREAM's own subject state and returns the bodies this function needs
// anyway, so there is no key listing to be short: a watcher-backed listing stops
// on a delivered-message count, and the pin family is the most-rewritten family
// in the bucket (one create and one rollup purge per instance), so a listing here
// could quietly omit a live pin — and an omitted pin tears down a consumer an
// in-flight instance is still waiting on.
//
// isPatternPinKey re-checks each returned key's shape, and that guard is
// load-bearing for the opposite failure. A key of another shape reaching the
// decode is silently harmless in the worst way: an Instance body unmarshals into
// a Pattern with no CompletionDomains and an empty SubjectType, which Domains()
// drops — so a mis-edited filter would return FEWER domains than there are live
// pins, with no error and no log, and drain consumers that are still needed. The
// filter handed to the server is asserted by this package's tests, which is the
// only place a widened or mis-narrowed resolution is visible at all.
//
// Error posture is asymmetric by design. An unparseable pin is logged and
// SKIPPED: its instance is already unrecoverable (advance cannot unmarshal the
// same value), so excluding its domains does not worsen its fate — and one
// poisoned pin must not freeze consumer teardown forever. A transient KV read
// error stays a hard error: the union would be incomplete, so the caller skips
// the Remove phase for that pass only.
func (s *stateStore) pinnedDomains(ctx context.Context, logger *slog.Logger) (map[string]struct{}, error) {
	entries, err := s.lister.KVGetMultiNoSnapshot(ctx, s.bucket, []string{patternPinFilter})
	if err != nil {
		return nil, fmt.Errorf("loom: read pattern pins %q: %w", patternPinFilter, err)
	}
	domains := make(map[string]struct{})
	for k, entry := range entries {
		if !isPatternPinKey(k) {
			continue
		}
		var p Pattern
		if err := json.Unmarshal(entry.Value, &p); err != nil {
			logger.Error("loom: pattern pin unparseable; excluding its domains from the reconcile union",
				"key", k, "err", err)
			continue
		}
		for _, d := range p.Domains() {
			domains[d] = struct{}{}
		}
	}
	return domains, nil
}

// tokenWriteMode selects the write condition transition puts on the new
// token.<newToken> reverse pointer. It exists because one caller — the
// operator's redrive — legitimately targets a token subject a prior removal
// left a marker on, and a marker makes a create-only write impossible for the
// marker's lifetime. See transition's doc comment for why each mode is the
// right guard on its path.
type tokenWriteMode int

const (
	// tokenCreateOnly writes the pointer create-if-absent: the subject must be
	// empty. Every advancing path uses it — it is the guard that lets only one
	// of two racing advancers of the same step commit. The zero value, so a
	// path that names no mode gets the tighter guard.
	tokenCreateOnly tokenWriteMode = iota
	// tokenPutUnderRedriveCAS writes the pointer unconditionally, for the
	// resumed step of a redrive whose guard is the compare-and-set on the
	// instance record in redrive's own batch.
	tokenPutUnderRedriveCAS
)

// transition applies one transition as a single AtomicBatch on loom-state
// (Contract #10 §10.3): update instance.<id>; optionally write the new
// token.<newToken> reverse pointer; optionally delete the prior token.<oldToken>;
// optionally write the outbox.<outbox.RequestID> op record; and arm or disarm
// deadline.<instanceId>. All-or-nothing — so the op submission (the outbox
// record) is part of the same atomic fact as the cursor advance and is NOT a
// dual write (the command-outbox pattern, §10.3).
//
//   - newToken == "" writes no forward pointer (a terminal has no next step),
//     and tokenMode is then unread.
//   - oldToken == "" deletes no prior pointer (the initial step had none).
//   - outbox != nil writes the op-to-submit record (the relay publishes it).
//   - deadlineTTL > 0 arms (PUT, fresh TTL) deadline.<instanceId> (re-arm on
//     each step); deadlineTTL <= 0 deletes it (terminal).
//   - inst.Status != running (terminal) also removes the instance's pattern pin
//     (instance.<id>.pattern) in the same batch and settles the failed index
//     (instance.<id>.failed) for the arm taken — written on failed, removed on
//     complete. The cursor record itself
//     persists: its presence is the dedup guard that collapses a re-emitted
//     trigger for the same instanceId onto the instance that already ran
//     (Contract #10 §10.9, and the triggerLoom clause of
//     docs/contracts/10-orchestration-substrate.md), and Weaver re-dispatches a
//     stable claimId-seeded instanceId for as long as its gap stays open.
//
// The write-ahead invariant (loom.md crash-safety invariant 1) holds by construction: the op
// record is persisted in this batch and the relay's publish is the only side
// effect, decoupled and idempotent.
//
// tokenMode is the write condition on token.<newToken>; the two modes guard
// different things.
//
// tokenCreateOnly is the race guard for an ADVANCING instance: two advancers of
// the same step (a live completion and a deadline-probe recovery) derive the
// same deterministic newToken, so the loser's batch is rejected here and only
// one advance commits a given step. A genuine crash-retry never reaches it —
// the prior attempt's batch is all-or-nothing, so a re-GET sees PendingToken
// already == newToken and routes to the drop branch, not a re-submit.
//
// tokenPutUnderRedriveCAS is the step a redrive resumes, and it is exempt from
// that guard on both counts. It cannot use create-only: the token is derived
// from (instanceId, cursor), so resuming at the failed cursor re-derives the
// token the failing transition removed, and a removal's marker refuses a
// create-only write for the marker's lifetime. It does not need create-only
// either: after redrive's CAS on the instance record this instance has no live
// advancer — it was terminal, so no deadline is armed; the old token is gone,
// so a late completion resolves nothing and drops on advance's cursor check;
// and a concurrent redrive lost that CAS and returned before submitting.
//
// One other writer reaches that window and is benign in both orders: a
// redelivered patternStarted, whose resume gate is exactly running-with-an-
// empty-pending-token — the state redrive leaves between its CAS and the
// resumed step's submission. While the marker stands, the resume's create-only
// write is refused → Nak → redelivered, by which time the redrive's put has
// landed and the gate reads a pending token → Ack. With the marker expired the
// resume commits first and the redrive's put rewrites the same pointer, cursor
// and outbox record with identical content, the doubled op collapsing on the
// Contract #4 tracker.
//
// expectedRevision, when non-zero, conditions the instance-record write on the
// revision the caller read it at (getInstanceAtRevision) — redrive's CAS shape,
// applied to a batch. Zero writes it unconditionally. The deadline probe is the
// caller that needs it: it decides a terminal from a record it read some round
// trips ago, and an advance landing in that window must refuse the write rather
// than flip an instance that has already moved to its next step.
func (s *stateStore) transition(ctx context.Context, inst *Instance, newToken, oldToken string, tokenMode tokenWriteMode, outbox *outboxRecord, deadlineTTL time.Duration, expectedRevision uint64) error {
	if newToken != "" || inst.Status != StatusRunning {
		// An inconclusive deadline verdict is a statement about ONE parked
		// step's evidence, so it dies with that step: a new token is a new
		// step, and a terminal status is a decided instance. Cleared inside the
		// batch that moves the instance, exactly as the failed index is settled
		// below — a derived fact settled by a second write could outlive the
		// state it describes. A transition that neither writes a token nor
		// leaves running (a re-arm-shaped rewrite) leaves the note standing,
		// because the step it describes is still the parked one.
		inst.DeadlineProbe = nil
	}
	body, err := json.Marshal(inst)
	if err != nil {
		return fmt.Errorf("loom: marshal instance %q: %w", inst.InstanceID, err)
	}
	instOp := substrate.BatchOp{Bucket: s.bucket, Key: instanceKey(inst.InstanceID), Value: body}
	if expectedRevision != 0 {
		instOp.HasRevision = true
		instOp.Revision = expectedRevision
	}
	ops := []substrate.BatchOp{instOp}
	if inst.Status != StatusRunning {
		// Terminal (complete/failed): remove the pattern pin in the SAME batch
		// that flips the status. The pin's removal is what lets the reconcile
		// union drain — a domain kept alive only by this instance's pinned
		// pattern is torn down on the next reconcile. The cursor record itself
		// is kept, expiring on the retention TTL stamped above (if any) rather
		// than being removed here, so it can still answer a redelivered trigger
		// and an operator's inspect/redrive.
		ops = append(ops, substrate.BatchOp{
			Bucket: s.bucket,
			Key:    patternPinKey(inst.InstanceID),
			Purge:  true,
			TTL:    tombstoneTTL,
		})
		// Every terminal batch SETTLES the failed index for the arm it takes, so
		// the index cannot outlive the status it claims. The failed arm writes
		// the marker — a plain PUT, never CreateOnly: a redriven instance that
		// fails again inside the minute its purge marker lives would have a
		// create-only write refused by exactly that marker, and the second
		// failure would then be invisible to the list. The complete arm removes
		// it, which is what heals a marker written for a failure the instance has
		// since been redriven past: a complete instance is not actionable, and
		// nothing else would ever remove that marker (redrive refuses a
		// non-failed instance). Both writes are unconditioned and idempotent, so
		// they settle the index whatever it held. The cost of the removal arm is
		// one transient subject per completing instance that never had a marker,
		// carrying the marker TTL and gone a minute later — the price of settling
		// it inside the atomic batch rather than reading first and racing.
		if inst.Status == StatusFailed {
			ops = append(ops, substrate.BatchOp{
				Bucket: s.bucket,
				Key:    failedMarkerKey(inst.InstanceID),
				Value:  []byte(failedMarkerBody),
			})
		} else {
			ops = append(ops, substrate.BatchOp{
				Bucket: s.bucket,
				Key:    failedMarkerKey(inst.InstanceID),
				Purge:  true,
				TTL:    tombstoneTTL,
			})
		}
	}
	if newToken != "" {
		ptrBody, err := json.Marshal(tokenPointer{InstanceID: inst.InstanceID})
		if err != nil {
			return fmt.Errorf("loom: marshal token pointer: %w", err)
		}
		ops = append(ops, substrate.BatchOp{
			Bucket:     s.bucket,
			Key:        tokenKey(newToken),
			Value:      ptrBody,
			CreateOnly: tokenMode == tokenCreateOnly,
		})
	}
	if oldToken != "" && oldToken != newToken {
		ops = append(ops, substrate.BatchOp{
			Bucket: s.bucket,
			Key:    tokenKey(oldToken),
			Purge:  true,
			TTL:    tombstoneTTL,
		})
	}
	if outbox != nil {
		obBody, err := json.Marshal(outbox)
		if err != nil {
			return fmt.Errorf("loom: marshal outbox record: %w", err)
		}
		ops = append(ops, substrate.BatchOp{
			Bucket: s.bucket,
			Key:    outboxKey(outbox.RequestID),
			Value:  obBody,
		})
	}
	if deadlineTTL > 0 {
		dlBody, err := json.Marshal(deadlineMark{SetAt: substrate.FormatTimestamp(time.Now())})
		if err != nil {
			return fmt.Errorf("loom: marshal deadline mark: %w", err)
		}
		// Re-arming the per-instance deadline by overwriting the same key relies on
		// loom-state being History:1 (the default): the new PUT evicts the prior
		// TTL'd message via the per-subject limit, so an earlier step's deadline
		// cannot fire after the cursor has advanced. Raising the bucket's history
		// would break that guarantee.
		ops = append(ops, substrate.BatchOp{
			Bucket: s.bucket,
			Key:    deadlineKey(inst.InstanceID),
			Value:  dlBody,
			TTL:    deadlineTTL,
		})
	} else {
		ops = append(ops, substrate.BatchOp{
			Bucket: s.bucket,
			Key:    deadlineKey(inst.InstanceID),
			Purge:  true,
			TTL:    tombstoneTTL,
		})
	}
	if _, err := s.conn.AtomicBatch(ctx, ops); err != nil {
		return fmt.Errorf("loom: transition instance %q: %w", inst.InstanceID, err)
	}
	return nil
}

// redrive re-pins pattern, flips inst.Status back to running and removes the
// instance's failed index, in one AtomicBatch, for a manual operator redrive of
// a failed instance (Engine.RedriveInstance). expectedRevision is the revision
// the caller read the instance record at (getInstanceAtRevision).
//
// The index removal rides this batch rather than a read-then-remove because the
// batch is the only place it can be atomic with the status flip — a redriven
// instance must never be listed as awaiting a redrive. Removing a marker that is
// already absent — a cursor reading `failed` with no marker beside it, which the
// backfill has not reached yet — mints a transient subject that expires with the
// marker TTL, the cheaper of the two wrong answers.
//
// The race guard for two concurrent redrives of one instance is that CAS on the
// instance record: both readers see revision R, the winner's batch bumps it, and
// the loser's expected-R batch is rejected whole — so only one redrive can
// re-pin and re-submit. It rides the instance key rather than the pin because
// the instance record is never removed, while the pin is: the terminal batch's
// pin removal leaves a marker on that subject, a marker makes the subject
// non-empty, and a CreateOnly re-pin (expected-last-subject-sequence 0) is
// refused by exactly that. The marker is present for the marker's lifetime and
// the guard must not depend on it, so the pin is written as an ordinary put,
// guarded by the same batch's CAS.
func (s *stateStore) redrive(ctx context.Context, inst *Instance, pattern *Pattern, expectedRevision uint64) error {
	// A redrive is the operator's answer to whatever the last probe said, so
	// any inconclusive deadline verdict on the record is spent. Cleared in the
	// same batch that flips the status, for the reason transition's clear
	// carries: a derived fact settles with the state it describes.
	inst.DeadlineProbe = nil
	body, err := json.Marshal(inst)
	if err != nil {
		return fmt.Errorf("loom: marshal instance %q: %w", inst.InstanceID, err)
	}
	pinBody, err := json.Marshal(pattern)
	if err != nil {
		return fmt.Errorf("loom: marshal pattern pin %q: %w", inst.InstanceID, err)
	}
	ops := []substrate.BatchOp{
		{Bucket: s.bucket, Key: instanceKey(inst.InstanceID), Value: body, HasRevision: true, Revision: expectedRevision},
		{Bucket: s.bucket, Key: patternPinKey(inst.InstanceID), Value: pinBody},
		{Bucket: s.bucket, Key: failedMarkerKey(inst.InstanceID), Purge: true, TTL: tombstoneTTL},
	}
	if _, err := s.conn.AtomicBatch(ctx, ops); err != nil {
		return fmt.Errorf("loom: redrive instance %q: %w", inst.InstanceID, err)
	}
	return nil
}

// outboxExists reports whether the command-outbox record for token is still
// present (i.e. the relay has not yet published + deleted it). Used by the
// step-deadline-exceeded probe to distinguish "not yet relayed" from "rejected"
// (§10.6).
func (s *stateStore) outboxExists(ctx context.Context, token string) (bool, error) {
	_, err := s.conn.KVGet(ctx, s.bucket, outboxKey(token))
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("loom: read outbox %q: %w", token, err)
	}
	return true, nil
}

// deadlineArmed reports whether deadline.<instanceId> currently holds a value —
// the probe's currency test (§10.6). A MaxAge marker's emission empties the
// subject, so a value present when the probe runs was put there afterwards: an
// advance to a later step (every running step arms) or another replica's
// re-arm. Either way the arm this marker expired is no longer the current one.
// A removal marker — DEL or purge — reads absent exactly like a never-written
// key, which is what makes absence the right answer for "the arm that expired
// is still the one this instance is on".
func (s *stateStore) deadlineArmed(ctx context.Context, instanceID string) (bool, error) {
	_, err := s.conn.KVGet(ctx, s.bucket, deadlineKey(instanceID))
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("loom: read deadline %q: %w", instanceID, err)
	}
	return true, nil
}

// errTokenPointerMissing reports that token.<pendingToken> is absent for an
// instance whose record still names that token as pending. The pointer is
// written in the SAME transition batch that records the token and is removed
// only by the batch that replaces or clears it, so for a live running instance
// absence is an invariant break — never a "not written yet" case. Callers match
// on this sentinel to turn the break into an operator-visible failed terminal
// (§10.6: never a silent wedge) rather than reading it as an answer; any other
// pointer-read error stays a retryable error.
var errTokenPointerMissing = errors.New("token pointer missing for pending token (pointer is written atomically with the instance)")

// tokenEpoch returns the instant the pending step's token pointer was written —
// the epoch of the step itself, which is what bounds the lifetime of the
// evidence the deadline probe reads about that step.
//
// The pointer is the right clock precisely because nothing refreshes it inside
// a step: it is written create-only in the step's own transition batch and
// re-put only by a redrive (which re-submits, so a fresh tracker follows), while
// re-arms touch deadline.<instanceId> alone and any probe note is written on the
// instance record. The instance record's own timestamp cannot serve — the note
// write would refresh it, and the next redelivery would then read a fresh epoch.
//
// A missing pointer is errTokenPointerMissing (wrapped), not a zero time: the
// caller must not read an invariant break as "infinitely old".
func (s *stateStore) tokenEpoch(ctx context.Context, token string) (time.Time, error) {
	entry, err := s.conn.KVGet(ctx, s.bucket, tokenKey(token))
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return time.Time{}, fmt.Errorf("loom: token %q: %w", token, errTokenPointerMissing)
		}
		return time.Time{}, fmt.Errorf("loom: read token epoch %q: %w", token, err)
	}
	return entry.Timestamp, nil
}

// noteDeadlineProbe records an inconclusive deadline verdict on the instance
// record and nothing else: a single-key compare-and-set put at the revision the
// probe read the instance at. A refused condition means the instance moved
// under the probe, and the caller treats that as the answer (the same drop
// probeFail takes), not as an error.
//
// It is deliberately NOT a transition. transition with no deadline TTL purges
// deadline.<instanceId>, and that key is already expired on this path — an
// unconditioned purge of an absent key is accepted by the server rather than
// reported as not-found, so it would MINT a marker on an empty subject and wake
// the probe again on evidence of its own making. That is the hazard deleteToken
// documents, and the one shape this verdict must never create. For the same
// reason it is not an AtomicBatch: the record is the only thing that changes.
func (s *stateStore) noteDeadlineProbe(ctx context.Context, inst *Instance, reason string, at time.Time, expectedRevision uint64) error {
	inst.DeadlineProbe = &ProbeNote{At: substrate.FormatTimestamp(at), Reason: reason}
	body, err := json.Marshal(inst)
	if err != nil {
		return fmt.Errorf("loom: marshal instance %q: %w", inst.InstanceID, err)
	}
	if _, err := s.conn.KVUpdate(ctx, s.bucket, instanceKey(inst.InstanceID), body, expectedRevision); err != nil {
		return fmt.Errorf("loom: note deadline probe %q: %w", inst.InstanceID, err)
	}
	return nil
}

// rearmDeadline re-arms deadline.<instanceId> with a fresh TTL outside a
// transition batch — used by the probe's "relay not yet delivered" branch to
// extend the deadline without advancing the cursor (§10.6).
func (s *stateStore) rearmDeadline(ctx context.Context, instanceID string, ttl time.Duration) error {
	body, err := json.Marshal(deadlineMark{SetAt: substrate.FormatTimestamp(time.Now())})
	if err != nil {
		return fmt.Errorf("loom: marshal deadline mark: %w", err)
	}
	if _, err := s.conn.KVPutWithTTL(ctx, s.bucket, deadlineKey(instanceID), body, ttl); err != nil {
		return fmt.Errorf("loom: rearm deadline %q: %w", instanceID, err)
	}
	return nil
}

// deleteToken removes a token.<token> reverse pointer (used when a redelivered
// completion resolves to an already-advanced instance and the stale pointer must
// be cleared). A missing pointer is not an error.
//
// The removal is guarded on the key being present because an unconditioned
// purge of an absent key is accepted by the server rather than reported as
// not-found, so it CREATES a marker on a subject that held nothing. advance
// calls this on every redelivered completion that no longer matches the cursor,
// so an unguarded purge would mint a fresh subject per redelivery on a token
// that may never have been written at all.
func (s *stateStore) deleteToken(ctx context.Context, token string) error {
	if _, err := s.conn.KVGet(ctx, s.bucket, tokenKey(token)); err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return nil
		}
		return fmt.Errorf("loom: probe token %q: %w", token, err)
	}
	if err := s.conn.KVPurgeWithTTL(ctx, s.bucket, tokenKey(token), tombstoneTTL, 0); err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return nil
		}
		return fmt.Errorf("loom: delete token %q: %w", token, err)
	}
	return nil
}
