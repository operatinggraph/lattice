package loom

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

// Instance status values (Contract #10 §10.3).
const (
	StatusRunning  = "running"
	StatusComplete = "complete"
	StatusFailed   = "failed"
)

// loom-state key prefixes (Contract #10 §10.3). The four shapes share the one
// bucket under disjoint prefixes — the same one-bucket / disjoint-prefix pattern
// capability-kv §6.1 uses for cap.ephemeral.*.
const (
	instancePrefix = "instance."
	tokenPrefix    = "token."
	outboxPrefix   = "outbox."
	deadlinePrefix = "deadline."
)

// patternPinSuffix is the sub-key suffix of an instance's pinned pattern copy:
// instance.<instanceId>.pattern holds the full pattern definition as loaded at
// trigger time. All step resolution for a running instance reads this pin —
// never the live pattern source — so a pattern update mid-flight cannot
// mis-index the durable cursor against reordered/changed steps. The pin lives
// exactly as long as the instance is live: written in the same AtomicBatch
// that creates instance.<instanceId>, deleted in the terminal batch.
const patternPinSuffix = ".pattern"

func instanceKey(instanceID string) string { return instancePrefix + instanceID }

// isInstanceRecordKey reports whether k is an instance.<id> cursor record (not an
// instance.<id>.pattern pin sub-key). The instanceId is a NanoID (dot-free by
// construction), so a key under the instance. prefix whose remainder contains a
// '.' is a sub-key (the .pattern pin), never an instance record. Used by
// listInstances, the control-plane's instance.* scan.
func isInstanceRecordKey(k string) bool {
	if !strings.HasPrefix(k, instancePrefix) {
		return false
	}
	return !strings.ContainsRune(k[len(instancePrefix):], '.')
}

// isPatternPinKey reports whether k is an instance.<id>.pattern pin sub-key —
// the counterpart of isInstanceRecordKey, kept beside it so the two filters
// over the instance.* keyspace cannot drift apart. The pin is written in the
// same AtomicBatch that creates instance.<id> and deleted only in the
// terminal batch (transition, pinnedDomains' own doc comment), so the set of
// keys this reports true for is exactly the set of running instances —
// runningInstanceCounter (health.go) counts them directly, with no body read.
func isPatternPinKey(k string) bool {
	return strings.HasPrefix(k, instancePrefix) && strings.HasSuffix(k, patternPinSuffix)
}

func patternPinKey(instanceID string) string {
	return instancePrefix + instanceID + patternPinSuffix
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
// #10 §10.3). It carries a per-key TTL = the current step's deadline; the
// server's marker for that expiry (Nats-Marker-Reason: MaxAge, which decodes as
// a KeyValuePurge) is the off-stream failed/rejected backstop (§10.6). What the
// handler keys on is the empty body, which every marker on this key shares —
// an explicit removal decodes the same way. The value is observability-only:
// the step-deadline-exceeded handler reconstructs everything from
// instance.<instanceId>.
type deadlineMark struct {
	SetAt string `json:"setAt"`
}

// stateStore reads and writes the two loom-state key shapes. loom-state is
// Loom's own operational bucket and the only place Loom writes directly (P2);
// every step transition is a single AtomicBatch on the one bucket so the cursor
// update and the reverse-pointer add/delete land all-or-nothing.
type stateStore struct {
	conn   *substrate.Conn
	bucket string
}

func newStateStore(conn *substrate.Conn, bucket string) *stateStore {
	return &stateStore{conn: conn, bucket: bucket}
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

// listInstances reads every instance.<id> cursor record in loom-state (running
// and retained terminals — only the pattern pin is deleted at terminal, the
// record persists). The .pattern pin sub-keys are filtered out by
// isInstanceRecordKey. The whole set is fetched in one KVGetMulti; a key
// absent from the response (deleted between the list and the batched read) is
// skipped, and a per-key unmarshal failure SKIPS that record (logged) rather
// than failing the whole list — one poisoned record must not blind the
// operator to every other instance. A genuine KVGetMulti failure, in
// contrast, fails the whole call: unlike a single poisoned record, it means
// this read cannot answer for ANY instance, so returning a partial or empty
// list would be silently wrong rather than degraded — the same fail-closed
// posture pinnedDomains below already takes on its own KVGetMulti call.
//
// Each record is decoded directly with no isDeleted soft-delete check. That is
// correct because Loom never soft-deletes an instance cursor record: a terminal
// is recorded by flipping Status (complete/failed) in place, never by writing an
// isDeleted envelope over instance.<id>; the only thing the terminal batch
// removes is the pattern pin. So every instance.<id> key that lists is a live
// record. This mirrors runningInstanceCounter, which decodes the same keys the
// same way.
func (s *stateStore) listInstances(ctx context.Context, logger *slog.Logger) ([]Instance, error) {
	keys, err := s.conn.KVListKeys(ctx, s.bucket)
	if err != nil {
		return nil, fmt.Errorf("loom: list instances: %w", err)
	}
	var recordKeys []string
	for _, k := range keys {
		if isInstanceRecordKey(k) {
			recordKeys = append(recordKeys, k)
		}
	}
	entries, err := s.conn.KVGetMulti(ctx, s.bucket, recordKeys)
	if err != nil {
		return nil, fmt.Errorf("loom: list instances: get-multi %d keys: %w", len(recordKeys), err)
	}
	out := make([]Instance, 0, len(recordKeys))
	for _, k := range recordKeys {
		entry, present := entries[k]
		if !present {
			// Deleted between list and read — skip.
			continue
		}
		var inst Instance
		if err := json.Unmarshal(entry.Value, &inst); err != nil {
			logger.Warn("loom: instance record unparseable; skipping", "key", k, "err", err)
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
// pinned pattern. Pins are deleted in the terminal batch, so listing
// instance.*.pattern keys yields exactly the live set — this is the second leg
// of the reconcile union (an in-flight instance keeps its completion domain's
// consumer alive even after its pattern is removed/updated-away; the consumer
// drains once the last live instance pinning that domain completes).
//
// Error posture is asymmetric by design. An unparseable pin is logged and
// SKIPPED: its instance is already unrecoverable (advance cannot unmarshal the
// same value), so excluding its domains does not worsen its fate — and one
// poisoned pin must not freeze consumer teardown forever. A transient KV read
// error stays a hard error: the union would be incomplete, so the caller skips
// the Remove phase for that pass only.
func (s *stateStore) pinnedDomains(ctx context.Context, logger *slog.Logger) (map[string]struct{}, error) {
	keys, err := s.conn.KVListKeys(ctx, s.bucket)
	if err != nil {
		return nil, fmt.Errorf("loom: list pinned patterns: %w", err)
	}
	var pinKeys []string
	for _, k := range keys {
		if isPatternPinKey(k) {
			pinKeys = append(pinKeys, k)
		}
	}
	entries, err := s.conn.KVGetMulti(ctx, s.bucket, pinKeys)
	if err != nil {
		return nil, fmt.Errorf("loom: read pattern pins: %w", err)
	}
	domains := make(map[string]struct{})
	for _, k := range pinKeys {
		entry, present := entries[k]
		if !present {
			// Deleted between list and read (its instance reached terminal).
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
//   - inst.Status != running (terminal) also deletes the instance's pattern pin
//     (instance.<id>.pattern) in the same batch. The cursor record itself
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
func (s *stateStore) transition(ctx context.Context, inst *Instance, newToken, oldToken string, tokenMode tokenWriteMode, outbox *outboxRecord, deadlineTTL time.Duration) error {
	body, err := json.Marshal(inst)
	if err != nil {
		return fmt.Errorf("loom: marshal instance %q: %w", inst.InstanceID, err)
	}
	ops := []substrate.BatchOp{
		{Bucket: s.bucket, Key: instanceKey(inst.InstanceID), Value: body},
	}
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

// redrive re-pins pattern and flips inst.Status back to running in one
// AtomicBatch, for a manual operator redrive of a failed instance
// (Engine.RedriveInstance). expectedRevision is the revision the caller read the
// instance record at (getInstanceAtRevision).
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

// disarmDeadline removes deadline.<instanceId> without touching the cursor or
// token — used by the userTask creation-deadline probe once the task vertex
// exists: the bounded creation wait is over, so the deadline is removed and the
// wait for the human becomes unbounded (§10.6).
//
// The removal is guarded on the key already being present. This matters because
// disarming a still-running instance does NOT change instance state, so the
// onDeadline handler does not self-guard against re-entry: the disarm's own
// marker re-fires the deadline watcher, which probes and disarms again. Skipping
// the removal when the key is already gone makes the second pass a true no-op
// (no fresh marker) and breaks that loop. A missing key is not an error — and
// the probe is what supplies that, since an unconditioned purge of an absent
// key is accepted by the server rather than reported as not-found.
func (s *stateStore) disarmDeadline(ctx context.Context, instanceID string) error {
	if _, err := s.conn.KVGet(ctx, s.bucket, deadlineKey(instanceID)); err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return nil
		}
		return fmt.Errorf("loom: probe deadline %q: %w", instanceID, err)
	}
	if err := s.conn.KVPurgeWithTTL(ctx, s.bucket, deadlineKey(instanceID), tombstoneTTL, 0); err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return nil
		}
		return fmt.Errorf("loom: disarm deadline %q: %w", instanceID, err)
	}
	return nil
}

// deleteToken removes a token.<token> reverse pointer (used when a redelivered
// completion resolves to an already-advanced instance and the stale pointer must
// be cleared). A missing pointer is not an error.
//
// The removal is guarded on the key being present, the same probe
// disarmDeadline runs and for the same reason: an unconditioned purge of an
// absent key is accepted by the server rather than reported as not-found, so
// it CREATES a marker on a subject that held nothing. advance calls this on
// every redelivered completion that no longer matches the cursor, so an
// unguarded purge would mint a fresh subject per redelivery on a token that
// may never have been written at all.
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
