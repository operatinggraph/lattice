package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// buildCommitterPipeline assembles a CommitterImpl wired against a
// fresh embedded NATS + Core KV harness.
func buildCommitterPipeline(t *testing.T) (context.Context, *CommitterImpl, *DDLCache) {
	t.Helper()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	c := NewCommitter(conn, testCoreBucket, cache, testLogger(), time.Now, nil)
	return ctx, c, cache
}

func TestCommit_CleanWriteTrackerAndMutation(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: "vtx.identity." + testNanoID2,
			Document: map[string]interface{}{
				"class": "identity",
				"data":  map[string]interface{}{"name": "Andrew"},
			},
		}},
	}
	tracker := NewTracker(env, time.Now())
	ack, err := c.Commit(ctx, env, result, tracker, nil)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if ack.Count == 0 {
		t.Fatalf("ack.Count = 0")
	}
	// Tracker and mutation present.
	if _, err := c.Conn.KVGet(ctx, testCoreBucket, tracker.Key); err != nil {
		t.Fatalf("tracker missing: %v", err)
	}
	entry, err := c.Conn.KVGet(ctx, testCoreBucket, "vtx.identity."+testNanoID2)
	if err != nil {
		t.Fatalf("mutation key missing: %v", err)
	}
	// Provenance injected.
	var doc map[string]interface{}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc["createdByOp"] != tracker.Key {
		t.Fatalf("createdByOp = %v", doc["createdByOp"])
	}
	if doc["createdBy"] != env.Actor {
		t.Fatalf("createdBy = %v", doc["createdBy"])
	}
}

func TestCommit_RevisionConflictSurfacesConflictError(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	env := newTestEnvelope(testNanoID1)
	key := "vtx.identity." + testNanoID2
	// Pre-create the key so the create-only mutation conflicts.
	pre := []byte(`{"class":"identity","isDeleted":false,"data":{}}`)
	if _, err := c.Conn.KVPut(ctx, testCoreBucket, key, pre); err != nil {
		t.Fatalf("pre-seed: %v", err)
	}
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: key,
			Document: map[string]interface{}{
				"class": "identity",
			},
		}},
	}
	tracker := NewTracker(env, time.Now())
	_, err := c.Commit(ctx, env, result, tracker, nil)
	if err == nil {
		t.Fatalf("expected error from conflicting create")
	}
	var confErr *ConflictError
	if !errors.As(err, &confErr) {
		t.Fatalf("expected *ConflictError, got %T: %v", err, err)
	}
}

// TestCommit_BatchTooLarge_MutationCount proves an operation whose mutation
// count pushes the batch (mutations + tracker) over substrate.MaxBatchMessages
// surfaces as a typed *BatchTooLargeError{Reason:"mutationCount"}, not a raw
// substrate rejection (Contract #3 §3.9.1).
func TestCommit_BatchTooLarge_MutationCount(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	env := newTestEnvelope(testNanoID1)

	mutations := make([]MutationOp, substrate.MaxBatchMessages) // + tracker = MaxBatchMessages+1
	for i := range mutations {
		id, err := substrate.NewNanoID()
		if err != nil {
			t.Fatalf("NewNanoID: %v", err)
		}
		mutations[i] = MutationOp{
			Op:       "create",
			Key:      "vtx.identity." + id,
			Document: map[string]interface{}{"class": "identity"},
		}
	}
	result := ScriptResult{Mutations: mutations}
	tracker := NewTracker(env, time.Now())
	_, err := c.Commit(ctx, env, result, tracker, nil)
	if err == nil {
		t.Fatalf("expected error from an over-limit batch")
	}
	var btlErr *BatchTooLargeError
	if !errors.As(err, &btlErr) {
		t.Fatalf("expected *BatchTooLargeError, got %T: %v", err, err)
	}
	if btlErr.Reason != "mutationCount" {
		t.Fatalf("Reason = %q, want mutationCount", btlErr.Reason)
	}
	if btlErr.Limit != substrate.MaxBatchMessages {
		t.Fatalf("Limit = %d, want %d", btlErr.Limit, substrate.MaxBatchMessages)
	}
	if btlErr.Actual != substrate.MaxBatchMessages+1 {
		t.Fatalf("Actual = %d, want %d", btlErr.Actual, substrate.MaxBatchMessages+1)
	}
	// Nothing must have landed — the tracker must not exist.
	if _, gerr := c.Conn.KVGet(ctx, testCoreBucket, tracker.Key); !errors.Is(gerr, substrate.ErrKeyNotFound) {
		t.Fatalf("tracker must not exist after a rejected over-limit batch: %v", gerr)
	}
}

// TestCommit_BatchTooLarge_ValueSize proves a single mutation whose marshaled
// value exceeds the negotiated payload ceiling surfaces as a typed
// *BatchTooLargeError{Reason:"valueSize"} naming the offending key.
func TestCommit_BatchTooLarge_ValueSize(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	env := newTestEnvelope(testNanoID1)
	key := "vtx.identity." + testNanoID2

	limit := int(c.Conn.NATS().MaxPayload()) - substrate.ValueHeadroomBytes
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: key,
			Document: map[string]interface{}{
				"class": "identity",
				"data":  map[string]interface{}{"blob": strings.Repeat("x", limit+1000)},
			},
		}},
	}
	tracker := NewTracker(env, time.Now())
	_, err := c.Commit(ctx, env, result, tracker, nil)
	if err == nil {
		t.Fatalf("expected error from an oversized value")
	}
	var btlErr *BatchTooLargeError
	if !errors.As(err, &btlErr) {
		t.Fatalf("expected *BatchTooLargeError, got %T: %v", err, err)
	}
	if btlErr.Reason != "valueSize" {
		t.Fatalf("Reason = %q, want valueSize", btlErr.Reason)
	}
	if btlErr.Key != key {
		t.Fatalf("Key = %q, want %q", btlErr.Key, key)
	}
	if _, gerr := c.Conn.KVGet(ctx, testCoreBucket, key); !errors.Is(gerr, substrate.ErrKeyNotFound) {
		t.Fatalf("mutation key must not exist after a rejected oversized batch: %v", gerr)
	}
}

func TestCommit_MetaVertexMutation_InvalidatesCache(t *testing.T) {
	t.Parallel()
	ctx, c, cache := buildCommitterPipeline(t)
	env := newTestEnvelope(testNanoID1)
	env.OperationType = "RegisterDDL"
	// New DDL meta-vertex.
	newDDLKey := "vtx.meta.brandnew"
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: newDDLKey,
			Document: map[string]interface{}{
				"class": "meta.ddl.vertexType",
				"data":  map[string]interface{}{"canonicalName": "brandnew", "permittedCommands": []string{"RegisterDDL"}},
			},
		}},
	}
	tracker := NewTracker(env, time.Now())
	if _, err := c.Commit(ctx, env, result, tracker, nil); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	// Cache should now know about "brandnew".
	if _, ok := cache.Lookup("brandnew"); !ok {
		t.Fatalf("cache did not invalidate; brandnew not present")
	}
}

func TestCommit_TombstoneSetsIsDeleted(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	env := newTestEnvelope(testNanoID1)
	key := "vtx.identity." + testNanoID2
	pre := []byte(`{"key":"` + key + `","class":"identity","isDeleted":false,"data":{}}`)
	if _, err := c.Conn.KVPut(ctx, testCoreBucket, key, pre); err != nil {
		t.Fatalf("pre-seed: %v", err)
	}
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:       "tombstone",
			Key:      key,
			Document: map[string]interface{}{"class": "identity"},
		}},
	}
	tracker := NewTracker(env, time.Now())
	if _, err := c.Commit(ctx, env, result, tracker, nil); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	entry, err := c.Conn.KVGet(ctx, testCoreBucket, key)
	if err != nil {
		t.Fatalf("read tombstoned: %v", err)
	}
	var doc map[string]interface{}
	_ = json.Unmarshal(entry.Value, &doc)
	if isDel, _ := doc["isDeleted"].(bool); !isDel {
		t.Fatalf("isDeleted not set on tombstone: %v", doc)
	}
}

func TestCommit_MixedTTLBatch_TrackerHasTTLOthersDont(t *testing.T) {
	t.Parallel()
	// A single op in a batch may carry a TTL while siblings do not.
	// This test exercises that mixed shape end-to-end through the
	// CommitterImpl.
	ctx, c, _ := buildCommitterPipeline(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: "vtx.identity." + testNanoID2,
			Document: map[string]interface{}{
				"class": "identity",
			},
		}},
	}
	tracker := NewTracker(env, time.Now())
	if _, err := c.Commit(ctx, env, result, tracker, nil); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	// Both keys exist immediately after commit (TTL is 24h — tracker
	// is present; the durable identity is also present). A finer
	// per-key TTL probe would require waiting out the marker; Story
	// 1.1's spike covers that and we trust the BatchOp wiring here.
	if _, err := c.Conn.KVGet(ctx, testCoreBucket, tracker.Key); err != nil {
		t.Fatalf("tracker missing after mixed-TTL batch: %v", err)
	}
	if _, err := c.Conn.KVGet(ctx, testCoreBucket, "vtx.identity."+testNanoID2); err != nil {
		t.Fatalf("durable mutation missing: %v", err)
	}
}

func TestCommit_TrackerCarriesMutationKeysAndEventClasses(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: "vtx.identity." + testNanoID2,
			Document: map[string]interface{}{
				"class": "identity",
			},
		}},
		Events: []EventSpec{{Class: "identity.created", Data: map[string]interface{}{"x": 1}}},
	}
	tracker := NewTracker(env, time.Now())
	if _, err := c.Commit(ctx, env, result, tracker, nil); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	entry, err := c.Conn.KVGet(ctx, testCoreBucket, tracker.Key)
	if err != nil {
		t.Fatalf("read tracker: %v", err)
	}
	parsed, err := ParseTracker(entry.Value)
	if err != nil {
		t.Fatalf("ParseTracker: %v", err)
	}
	muts, _ := parsed.Data["mutationKeys"].([]interface{})
	if len(muts) != 1 {
		t.Fatalf("mutationKeys = %v", parsed.Data["mutationKeys"])
	}
	evs, _ := parsed.Data["eventClasses"].([]interface{})
	if len(evs) != 1 || evs[0] != "identity.created" {
		t.Fatalf("eventClasses = %v", parsed.Data["eventClasses"])
	}
}

// TestCommit_WritesOutboxAspectWithFaithfulEvents asserts the step-8 atomic
// batch persists the vtx.op.<id>.events outbox aspect carrying the FULL
// faithful EventList (eventId, payload, targetKey, timestamp), and that the
// outbox aspect carries NO Nats-TTL header (so it outlives the 24h tracker).
func TestCommit_WritesOutboxAspectWithFaithfulEvents(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: "vtx.identity." + testNanoID2,
			Document: map[string]interface{}{
				"class": "identity",
			},
		}},
		Events: []EventSpec{{Class: "identity.created", Data: map[string]interface{}{
			"identityKey": "vtx.identity." + testNanoID2,
			"name":        "Andrew",
		}}},
	}
	tracker := NewTracker(env, time.Now())
	ack, err := c.Commit(ctx, env, result, tracker, nil)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// The outbox aspect exists and carries the faithful EventList.
	ae, err := c.Conn.KVGet(ctx, testCoreBucket, OutboxAspectKey(env.RequestID))
	if err != nil {
		t.Fatalf("outbox aspect missing: %v", err)
	}
	aspect, err := ParseOutboxAspect(ae.Value)
	if err != nil {
		t.Fatalf("ParseOutboxAspect: %v", err)
	}
	if aspect.Class != OutboxAspectClass || aspect.LocalName != OutboxLocalName {
		t.Fatalf("aspect envelope wrong: class=%q localName=%q", aspect.Class, aspect.LocalName)
	}
	if aspect.VertexKey != tracker.Key {
		t.Fatalf("aspect vertexKey = %q, want %q", aspect.VertexKey, tracker.Key)
	}
	if len(aspect.Data.Events) != 1 {
		t.Fatalf("aspect events = %d, want 1", len(aspect.Data.Events))
	}
	ev := aspect.Data.Events[0]
	// Byte-identical to the EventList returned in the CommitAck.
	if len(ack.Events) != 1 || ack.Events[0].EventID != ev.EventID {
		t.Fatalf("persisted eventId %q != committed eventId", ev.EventID)
	}
	if ev.EventID == "" || ev.EventType != "identity.created" {
		t.Fatalf("event not faithful: %+v", ev)
	}
	if ev.Payload["identityKey"] != "vtx.identity."+testNanoID2 || ev.Payload["name"] != "Andrew" {
		t.Fatalf("event payload not faithful (the reconstruction-from-classes regression): %v", ev.Payload)
	}

	// The outbox aspect carries NO Nats-TTL; the tracker DOES (24h).
	js := c.Conn.JetStream()
	stream, err := js.Stream(ctx, "KV_"+testCoreBucket)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	outboxMsg, err := stream.GetLastMsgForSubject(ctx, "$KV."+testCoreBucket+"."+OutboxAspectKey(env.RequestID))
	if err != nil {
		t.Fatalf("get outbox msg: %v", err)
	}
	if ttl := outboxMsg.Header.Get("Nats-TTL"); ttl != "" {
		t.Fatalf("outbox aspect carries Nats-TTL=%q; must be unset so it outlives the tracker", ttl)
	}
	trackerMsg, err := stream.GetLastMsgForSubject(ctx, "$KV."+testCoreBucket+"."+tracker.Key)
	if err != nil {
		t.Fatalf("get tracker msg: %v", err)
	}
	if ttl := trackerMsg.Header.Get("Nats-TTL"); ttl == "" {
		t.Fatalf("tracker lost its Nats-TTL header")
	}
}

// TestCommit_ZeroEventsWritesNoOutboxAspect asserts an op with no events writes
// no outbox aspect (the extra BatchOp is skipped).
func TestCommit_ZeroEventsWritesNoOutboxAspect(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:       "create",
			Key:      "vtx.identity." + testNanoID2,
			Document: map[string]interface{}{"class": "identity"},
		}},
	}
	tracker := NewTracker(env, time.Now())
	if _, err := c.Commit(ctx, env, result, tracker, nil); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := c.Conn.KVGet(ctx, testCoreBucket, OutboxAspectKey(env.RequestID)); !errors.Is(err, substrate.ErrKeyNotFound) {
		t.Fatalf("zero-event op outbox lookup: got err=%v, want ErrKeyNotFound", err)
	}
}

// TestCommit_ZeroMutationEventOnly asserts an op with an EMPTY MutationBatch and
// a non-empty EventList commits a tracker-only atomic batch plus the outbox
// aspect (Contract #10 §10.9 event-only lifecycle ops). The commit path must
// accept the zero-mutation case: no upstream guard rejects an empty mutation set
// when result.Events is non-empty.
func TestCommit_ZeroMutationEventOnly(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: nil,
		Events: []EventSpec{{Class: "loom.patternStarted", Data: map[string]interface{}{
			"instanceId": testNanoID1,
			"patternRef": "vtx.meta." + testNanoID2,
		}}},
	}
	tracker := NewTracker(env, time.Now())
	ack, err := c.Commit(ctx, env, result, tracker, nil)
	if err != nil {
		t.Fatalf("zero-mutation event-only Commit must succeed, got: %v", err)
	}
	// Tracker landed (idempotency infra) despite zero mutations.
	if _, err := c.Conn.KVGet(ctx, testCoreBucket, tracker.Key); err != nil {
		t.Fatalf("tracker missing after zero-mutation commit: %v", err)
	}
	// The outbox aspect carries the one event, so the outbox consumer publishes it.
	ae, err := c.Conn.KVGet(ctx, testCoreBucket, OutboxAspectKey(env.RequestID))
	if err != nil {
		t.Fatalf("outbox aspect missing for event-only op: %v", err)
	}
	aspect, err := ParseOutboxAspect(ae.Value)
	if err != nil {
		t.Fatalf("ParseOutboxAspect: %v", err)
	}
	if len(aspect.Data.Events) != 1 || aspect.Data.Events[0].EventType != "loom.patternStarted" {
		t.Fatalf("event-only outbox not faithful: %+v", aspect.Data.Events)
	}
	if len(ack.Events) != 1 || ack.Events[0].EventType != "loom.patternStarted" {
		t.Fatalf("ack.Events not faithful: %+v", ack.Events)
	}
}

// commitOne runs a single-mutation commit with a fresh requestId and returns
// the stored document at key.
func commitOne(t *testing.T, ctx context.Context, c *CommitterImpl, rid string, m MutationOp) map[string]interface{} {
	t.Helper()
	env := newTestEnvelope(testNanoID1)
	env.RequestID = rid
	tracker := NewTracker(env, time.Now())
	if _, err := c.Commit(ctx, env, ScriptResult{Mutations: []MutationOp{m}}, tracker, nil); err != nil {
		t.Fatalf("Commit(%s %s): %v", m.Op, m.Key, err)
	}
	entry, err := c.Conn.KVGet(ctx, testCoreBucket, m.Key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", m.Key, err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", m.Key, err)
	}
	return doc
}

// An update writes the whole value, so the Contract #1 §1.3 creation triplet —
// which no script can even read, let alone resupply — must be carried over from
// the stored document rather than dropped.
func TestCommit_UpdatePreservesImmutableCreationProvenance(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	key := "vtx.identity." + testNanoID2

	created := commitOne(t, ctx, c, "rid-create-"+testNanoID2, MutationOp{
		Op: "create", Key: key,
		Document: map[string]interface{}{"class": "identity", "data": map[string]interface{}{"name": "Andrew"}},
	})

	updated := commitOne(t, ctx, c, "rid-update-"+testNanoID2, MutationOp{
		Op: "update", Key: key,
		Document: map[string]interface{}{"class": "identity", "data": map[string]interface{}{"name": "Renamed"}},
	})

	for _, f := range immutableEnvelopeFields {
		if updated[f] != created[f] {
			t.Fatalf("update erased %s: created=%v updated=%v", f, created[f], updated[f])
		}
	}
	// The mutable half still took the script's value.
	data, _ := updated["data"].(map[string]interface{})
	if data["name"] != "Renamed" {
		t.Fatalf("update did not apply script data: %v", updated["data"])
	}
	if updated["lastModifiedByOp"] == created["lastModifiedByOp"] {
		t.Fatalf("lastModifiedByOp not restamped: %v", updated["lastModifiedByOp"])
	}
}

// A script must not be able to rewrite immutable provenance by supplying it.
func TestCommit_UpdateCannotForgeImmutableProvenance(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	key := "vtx.identity." + testNanoID2

	created := commitOne(t, ctx, c, "rid-create-forge", MutationOp{
		Op: "create", Key: key,
		Document: map[string]interface{}{"class": "identity", "data": map[string]interface{}{}},
	})
	updated := commitOne(t, ctx, c, "rid-update-forge", MutationOp{
		Op: "update", Key: key,
		Document: map[string]interface{}{
			"class":       "identity",
			"createdAt":   "1999-01-01T00:00:00.000Z",
			"createdBy":   "vtx.identity." + testNanoID1,
			"createdByOp": "vtx.op.forged",
			"data":        map[string]interface{}{},
		},
	})
	for _, f := range immutableEnvelopeFields {
		if updated[f] != created[f] {
			t.Fatalf("script forged %s: got %v want %v", f, updated[f], created[f])
		}
	}
}

// A tombstone carries no script document at all, so everything the stored
// document held must survive it — otherwise a tombstoned link loses the
// class/sourceVertex/targetVertex that make it readable as a link.
func TestCommit_TombstonePreservesWholeDocument(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	key := "lnk.lease." + testNanoID1 + ".signedBy.identity." + testNanoID2

	created := commitOne(t, ctx, c, "rid-create-link", MutationOp{
		Op: "create", Key: key,
		Document: map[string]interface{}{
			"class":        "lease.signedBy.identity",
			"sourceVertex": "vtx.lease." + testNanoID1,
			"targetVertex": "vtx.identity." + testNanoID2,
			"localName":    "signedBy",
			"data":         map[string]interface{}{"role": "tenant"},
		},
	})

	tombstoned := commitOne(t, ctx, c, "rid-tombstone-link", MutationOp{Op: "tombstone", Key: key})

	if tombstoned["isDeleted"] != true {
		t.Fatalf("tombstone did not set isDeleted: %v", tombstoned["isDeleted"])
	}
	for _, f := range []string{"class", "sourceVertex", "targetVertex", "localName"} {
		if tombstoned[f] != created[f] {
			t.Fatalf("tombstone erased %s: %v (was %v)", f, tombstoned[f], created[f])
		}
	}
	for _, f := range immutableEnvelopeFields {
		if tombstoned[f] != created[f] {
			t.Fatalf("tombstone erased %s: %v (was %v)", f, tombstoned[f], created[f])
		}
	}
	data, ok := tombstoned["data"].(map[string]interface{})
	if !ok || data["role"] != "tenant" {
		t.Fatalf("tombstone erased data: %v", tombstoned["data"])
	}
	if tombstoned["lastModifiedByOp"] == created["lastModifiedByOp"] {
		t.Fatalf("tombstone did not restamp lastModifiedByOp")
	}
}

// The revive path (tombstone → update, per the Wire* revive semantics) must
// still reach back to the ORIGINAL creation provenance.
func TestCommit_ReviveThroughTombstoneKeepsOriginalProvenance(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	key := "vtx.lease." + testNanoID1

	created := commitOne(t, ctx, c, "rid-create-revive", MutationOp{
		Op: "create", Key: key,
		Document: map[string]interface{}{"class": "lease", "data": map[string]interface{}{}},
	})
	commitOne(t, ctx, c, "rid-tombstone-revive", MutationOp{Op: "tombstone", Key: key})
	revived := commitOne(t, ctx, c, "rid-revive", MutationOp{
		Op: "update", Key: key,
		Document: map[string]interface{}{"class": "lease", "isDeleted": false, "data": map[string]interface{}{}},
	})

	if revived["isDeleted"] != false {
		t.Fatalf("revive did not clear isDeleted: %v", revived["isDeleted"])
	}
	for _, f := range immutableEnvelopeFields {
		if revived[f] != created[f] {
			t.Fatalf("revive lost original %s: %v (was %v)", f, revived[f], created[f])
		}
	}
}

// A stored document written before provenance preservation has no creation
// triplet to carry over. Healing it must stamp the healing operation — a script
// supplying the fields must not be able to backdate the entity by filling the
// gap itself.
func TestCommit_UpdateCannotForgeProvenanceOntoLegacyDocument(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	key := "vtx.identity." + testNanoID2

	// A legacy envelope: written straight to KV, so it carries no triplet.
	legacy := []byte(`{"key":"` + key + `","class":"identity","isDeleted":false,"data":{"name":"Andrew"}}`)
	if _, err := c.Conn.KVPut(ctx, testCoreBucket, key, legacy); err != nil {
		t.Fatalf("seed legacy document: %v", err)
	}

	healed := commitOne(t, ctx, c, "rid-heal-legacy", MutationOp{
		Op: "update", Key: key,
		Document: map[string]interface{}{
			"class":       "identity",
			"createdAt":   "1999-01-01T00:00:00.000Z",
			"createdBy":   "vtx.identity." + testNanoID1,
			"createdByOp": "vtx.op.forged",
			"data":        map[string]interface{}{"name": "Renamed"},
		},
	})

	if healed["createdAt"] == "1999-01-01T00:00:00.000Z" {
		t.Fatalf("script backdated createdAt onto a legacy document: %v", healed["createdAt"])
	}
	if healed["createdByOp"] == "vtx.op.forged" {
		t.Fatalf("script forged createdByOp onto a legacy document: %v", healed["createdByOp"])
	}
	// The healing operation stamped itself instead.
	if healed["createdByOp"] != healed["lastModifiedByOp"] {
		t.Fatalf("healing op did not stamp its own createdByOp: %v (lastModifiedByOp %v)",
			healed["createdByOp"], healed["lastModifiedByOp"])
	}
	for _, f := range immutableEnvelopeFields {
		if s, ok := healed[f].(string); !ok || s == "" {
			t.Fatalf("healing left %s unset: %v", f, healed[f])
		}
	}
}

// An update over a key with no stored document materially creates it, so the
// envelope must not be left permanently missing its creation triplet.
func TestCommit_UpdateOverAbsentKeyStampsCreationProvenance(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	key := "vtx.identity." + testNanoID2

	doc := commitOne(t, ctx, c, "rid-update-absent", MutationOp{
		Op: "update", Key: key,
		Document: map[string]interface{}{"class": "identity", "data": map[string]interface{}{}},
	})
	for _, f := range immutableEnvelopeFields {
		if s, ok := doc[f].(string); !ok || s == "" {
			t.Fatalf("update over absent key left %s unset: %v", f, doc[f])
		}
	}
}

// commitOneErr runs a single-mutation commit with a fresh requestId and returns
// the commit error rather than failing the test, so a guard's rejection is an
// assertable outcome.
func commitOneErr(ctx context.Context, c *CommitterImpl, rid string, m MutationOp) error {
	env := newTestEnvelope(testNanoID1)
	env.RequestID = rid
	tracker := NewTracker(env, time.Now())
	_, err := c.Commit(ctx, env, ScriptResult{Mutations: []MutationOp{m}}, tracker, nil)
	return err
}

// permissionData mirrors the shape a permission body has once it has been
// through the substrate: every list is a []interface{}, as JSON decoding
// produces, so a comparison that behaves differently for []string cannot pass
// here and fail in production.
func permissionData(operationType, scope, origin, declaredBy, note string, lanes ...string) map[string]interface{} {
	data := map[string]interface{}{
		"operationType": operationType,
		"scope":         scope,
		"origin":        origin,
		"declaredBy":    declaredBy,
		"note":          note,
	}
	if lanes != nil {
		list := make([]interface{}, 0, len(lanes))
		for _, l := range lanes {
			list = append(list, l)
		}
		data["lanes"] = list
	}
	return data
}

func permissionDocFrom(data map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"class": "permission", "data": data}
}

func permissionDoc(operationType, scope, origin, declaredBy, note string, lanes ...string) map[string]interface{} {
	return permissionDocFrom(permissionData(operationType, scope, origin, declaredBy, note, lanes...))
}

func roleRootDoc() map[string]interface{} {
	return map[string]interface{}{"class": "role", "data": map[string]interface{}{}}
}

func roleCanonicalNameDoc(roleKey, value string) map[string]interface{} {
	return map[string]interface{}{
		"class":     "canonicalName",
		"vertexKey": roleKey,
		"localName": "canonicalName",
		"data":      map[string]interface{}{"value": value},
	}
}

func roleindexDoc(canonicalName, roleID string) map[string]interface{} {
	return map[string]interface{}{
		"class": "roleindex",
		"data":  map[string]interface{}{"canonicalName": canonicalName, "roleId": roleID},
	}
}

// A permission vertex's operationType/scope/origin/declaredBy/lanes are what
// authorization reads, and no DDL gates class "permission" or "role" — so an
// actor holding a package-lifecycle op could otherwise widen a grant it already
// holds by rewriting the body of the very key its grantedBy link points at, or
// redirect a role's identity. Those fields, a role root's whole body, and a
// role's canonicalName aspect are write-once at commit time. data.note carries
// no authorization weight and stays mutable.
func TestCommit_PermissionRoleProvenanceIsWriteOnce(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)

	const (
		permIDNote         = "Pm4kHj9nbCxz5vQ2yRtw"
		permIDOpType       = "Qn5jKm8pcDya6wR3zSux"
		permIDScope        = "Rp6hLn7qdEzb7xS4aTvy"
		permIDOrigin       = "Sq7gMp6rfFac8yT5bUwz"
		permIDDeclaredBy   = "Tr8fNq5sgGbd9zU6cVxA"
		permIDAbsentPrior  = "Us9ePr4thHce1aV7dWyB"
		permIDLanes        = "Zx5cUw2ypNhj6fA3iBdG"
		permIDLaneReorder  = "Ay6dVx3zqPik7gB4jCeH"
		permIDHeal         = "Bz7eWy4arQjm8hC5kDfJ"
		permIDHealPartial  = "Ca8fXz5bsRkn9jD6mEgK"
		roleIDNoop         = "Vt1dQs3ujJdf2bW8eXzC"
		roleIDRewrite      = "Wu2cRt2vkKeg3cX9fYaD"
		roleIDRootShadow   = "Db9gYa6ctSmp1kE7nFhL"
		roleIDRootNoop     = "Ec1hZb7duTnq2mF8pGjM"
		identityIDUnaffect = "Xv3bSu1wmLfh4dY1gZbE"
		roleindexIDNoop    = "Fh4jKn7qStvx2cAy8wZm"
		roleindexIDRewrite = "Gj5kLp8rTuwy3dBz9xAq"
	)

	priorPerm := permissionDoc("ReadListing", "own", "package", "listings-domain", "original note", "default")

	tests := []struct {
		name string
		key  string
		// prior is committed as a create before the mutation; nil leaves the key absent.
		prior map[string]interface{}
		// op defaults to "update".
		op        string
		update    map[string]interface{}
		wantField string // "" means the mutation must be accepted
	}{
		{
			name:   "permission note changes",
			key:    "vtx.permission." + permIDNote,
			prior:  priorPerm,
			update: permissionDoc("ReadListing", "own", "package", "listings-domain", "revised note", "default"),
		},
		{
			name:   "permission lanes reordered but unchanged as a set",
			key:    "vtx.permission." + permIDLaneReorder,
			prior:  permissionDoc("ReadListing", "own", "package", "listings-domain", "n", "default", "urgent"),
			update: permissionDoc("ReadListing", "own", "package", "listings-domain", "n", "urgent", "default"),
		},
		{
			name:   "role canonicalName rewritten to the same value",
			key:    "vtx.role." + roleIDNoop + ".canonicalName",
			prior:  roleCanonicalNameDoc("vtx.role."+roleIDNoop, "consoleOperator"),
			update: roleCanonicalNameDoc("vtx.role."+roleIDNoop, "consoleOperator"),
		},
		{
			name:   "role root resupplied unchanged",
			key:    "vtx.role." + roleIDRootNoop,
			prior:  roleRootDoc(),
			update: roleRootDoc(),
		},
		{
			name:   "roleindex resupplied unchanged",
			key:    "vtx.roleindex." + roleindexIDNoop,
			prior:  roleindexDoc("consoleOperator", "Hk6mNq9sUvyz4eCr1wDp"),
			update: roleindexDoc("consoleOperator", "Hk6mNq9sUvyz4eCr1wDp"),
		},
		{
			name:  "non permission or role class is unaffected",
			key:   "vtx.identity." + identityIDUnaffect,
			prior: map[string]interface{}{"class": "identity", "data": map[string]interface{}{"operationType": "ReadListing"}},
			update: map[string]interface{}{
				"class": "identity",
				"data":  map[string]interface{}{"operationType": "ShredRetentionClassKey"},
			},
		},
		{
			name:      "permission operationType rewritten",
			key:       "vtx.permission." + permIDOpType,
			prior:     priorPerm,
			update:    permissionDoc("ShredRetentionClassKey", "own", "package", "listings-domain", "original note", "default"),
			wantField: "operationType",
		},
		{
			name:      "permission scope rewritten",
			key:       "vtx.permission." + permIDScope,
			prior:     priorPerm,
			update:    permissionDoc("ReadListing", "any", "package", "listings-domain", "original note", "default"),
			wantField: "scope",
		},
		{
			name:      "permission origin rewritten",
			key:       "vtx.permission." + permIDOrigin,
			prior:     permissionDoc("ReadListing", "own", "runtime", "listings-domain", "original note", "default"),
			update:    permissionDoc("ReadListing", "own", "package", "listings-domain", "original note", "default"),
			wantField: "origin",
		},
		{
			name:      "permission declaredBy rewritten",
			key:       "vtx.permission." + permIDDeclaredBy,
			prior:     priorPerm,
			update:    permissionDoc("ReadListing", "own", "package", "rbac-domain", "original note", "default"),
			wantField: "declaredBy",
		},
		{
			// platformLaneGate honours an entry-level lanes value over the
			// capability document's own, and the core allowlist sanctions the
			// meta lane for the package-lifecycle trio — so widening lanes
			// widens an already-held grant with no new grant step.
			name:      "permission lanes widened to a privileged lane",
			key:       "vtx.permission." + permIDLanes,
			prior:     permissionDoc("UpgradePackage", "any", "package", "console-operator", "n", "default"),
			update:    permissionDoc("UpgradePackage", "any", "package", "console-operator", "n", "meta"),
			wantField: "lanes",
		},
		{
			name:      "role canonicalName rewritten to a different value",
			key:       "vtx.role." + roleIDRewrite + ".canonicalName",
			prior:     roleCanonicalNameDoc("vtx.role."+roleIDRewrite, "consoleOperator"),
			update:    roleCanonicalNameDoc("vtx.role."+roleIDRewrite, "operator"),
			wantField: "value",
		},
		{
			// The rule engine resolves role.canonicalName from the ROOT body
			// first and only then reads the .canonicalName aspect, so a
			// top-level field on the root answers the kernel root-grant lens in
			// the real aspect's place. The role root is write-once in full.
			name:  "role root gains a shadowing canonicalName field",
			key:   "vtx.role." + roleIDRootShadow,
			prior: roleRootDoc(),
			update: map[string]interface{}{
				"class":         "role",
				"data":          map[string]interface{}{},
				"canonicalName": map[string]interface{}{"data": map[string]interface{}{"value": "operator"}},
			},
			wantField: "canonicalName",
		},
		{
			// The roleindex root is write-once as a whole document, mirroring
			// the role root case — a rewritten data.roleId changes the
			// top-level "data" field, since the guard walks the document's
			// own top-level fields rather than descending into nested data.
			name:      "roleindex roleId rewritten to a different role",
			key:       "vtx.roleindex." + roleindexIDRewrite,
			prior:     roleindexDoc("consoleOperator", "Hk6mNq9sUvyz4eCr1wDp"),
			update:    roleindexDoc("consoleOperator", "Jm7nPr2tVwz5fEs3xFq8"),
			wantField: "data",
		},
		{
			// origin/declaredBy postdate some stored permissions, so the first
			// upgrade to carry them must be able to fill them in.
			name:   "permission gains origin and declaredBy for the first time",
			key:    "vtx.permission." + permIDHeal,
			prior:  permissionDocFrom(map[string]interface{}{"operationType": "ReadListing", "scope": "own"}),
			update: permissionDoc("ReadListing", "own", "package", "listings-domain", "n"),
		},
		{
			name:   "permission gains only declaredBy when origin was already stored",
			key:    "vtx.permission." + permIDHealPartial,
			prior:  permissionDocFrom(map[string]interface{}{"operationType": "ReadListing", "scope": "own", "origin": "package"}),
			update: permissionDoc("ReadListing", "own", "package", "listings-domain", "n"),
		},
		{
			name:   "permission key with no prior document",
			key:    "vtx.permission." + permIDAbsentPrior,
			prior:  nil,
			update: permissionDoc("ShredRetentionClassKey", "any", "package", "listings-domain", ""),
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rid := fmt.Sprintf("rid-provenance-%d", i)
			if tt.prior != nil {
				commitOne(t, ctx, c, rid+"-create", MutationOp{
					Op: "create", Key: tt.key, Document: tt.prior,
				})
			}
			op := tt.op
			if op == "" {
				op = "update"
			}
			err := commitOneErr(ctx, c, rid+"-mutate", MutationOp{
				Op: op, Key: tt.key, Document: tt.update,
			})
			var provErr *PermissionProvenanceError
			switch {
			case tt.wantField == "":
				if err != nil {
					t.Fatalf("Commit(%s %s): %v, want accepted", op, tt.key, err)
				}
			case !errors.As(err, &provErr):
				t.Fatalf("Commit(%s %s): %v, want *PermissionProvenanceError on field %q",
					op, tt.key, err, tt.wantField)
			case provErr.Field != tt.wantField:
				t.Fatalf("rejected on field %q, want %q", provErr.Field, tt.wantField)
			case provErr.Key != tt.key:
				t.Fatalf("rejected key %q, want %q", provErr.Key, tt.key)
			}
		})
	}
}

// A rejection must leave the stored document untouched — the guard runs before
// the atomic batch, so the escalated body never lands.
func TestCommit_PermissionProvenanceRejectionLeavesPriorBody(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	key := "vtx.permission." + testNanoID2

	commitOne(t, ctx, c, "rid-perm-create", MutationOp{
		Op: "create", Key: key,
		Document: permissionDoc("ReadListing", "own", "package", "listings-domain", "note"),
	})
	if err := commitOneErr(ctx, c, "rid-perm-escalate", MutationOp{
		Op: "update", Key: key,
		Document: permissionDoc("ShredRetentionClassKey", "any", "package", "listings-domain", "note"),
	}); err == nil {
		t.Fatal("Commit(escalating update): nil error, want rejection")
	}

	entry, err := c.Conn.KVGet(ctx, testCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	data, _ := doc["data"].(map[string]interface{})
	if data["operationType"] != "ReadListing" {
		t.Fatalf("stored operationType = %v, want ReadListing", data["operationType"])
	}
}

// Tombstoning first must not launder a rewrite. A revive is necessarily an
// update — a create asserts revision 0 and the tombstone already sits at a later
// revision — and a bare tombstone supplies no fields, so the value the guard
// compares against is still the one that was committed before the tombstone.
func TestCommit_TombstoneThenReviveCannotRewriteProvenance(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	roleID := "Yw4aTv9xnMgi5eZ2hAcF"
	key := "vtx.role." + roleID + ".canonicalName"

	commitOne(t, ctx, c, "rid-revive-create", MutationOp{
		Op: "create", Key: key,
		Document: roleCanonicalNameDoc("vtx.role."+roleID, "consoleOperator"),
	})
	if err := commitOneErr(ctx, c, "rid-revive-tombstone", MutationOp{
		Op: "tombstone", Key: key,
	}); err != nil {
		t.Fatalf("Commit(tombstone): %v", err)
	}

	err := commitOneErr(ctx, c, "rid-revive-update", MutationOp{
		Op: "update", Key: key,
		Document: roleCanonicalNameDoc("vtx.role."+roleID, "operator"),
	})
	var provErr *PermissionProvenanceError
	if !errors.As(err, &provErr) {
		t.Fatalf("Commit(revive with a new value): %v, want *PermissionProvenanceError", err)
	}
	if provErr.Field != "value" {
		t.Fatalf("rejected on field %q, want value", provErr.Field)
	}
}

// Contract #3 §3.3: a tombstone carries no document, and one supplied is not
// honored — a tombstone can never modify, blank, or reclaim the stored body.
// The mutation parser refuses the shape outright (a Starlark tombstone with a
// `document` is an InvalidReturnShape), so this is a unit assertion over a
// mutation no production path can construct. It pins the property at the
// enforcement point, so no path reaching commit with one — present or future —
// can launder a rewrite through a delete.
func TestCommit_TombstoneDocumentBranchRemoved(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)

	t.Run("a role canonicalName aspect", func(t *testing.T) {
		roleID := "Fd2jAc8evUor3nG9qHkN"
		key := "vtx.role." + roleID + ".canonicalName"
		commitOne(t, ctx, c, "rid-tsdoc-create", MutationOp{
			Op: "create", Key: key,
			Document: roleCanonicalNameDoc("vtx.role."+roleID, "consoleOperator"),
		})

		if err := commitOneErr(ctx, c, "rid-tsdoc-launder", MutationOp{
			Op: "tombstone", Key: key,
			Document: roleCanonicalNameDoc("vtx.role."+roleID, "operator"),
		}); err != nil {
			t.Fatalf("Commit(tombstone carrying a document): %v", err)
		}

		doc := readStoredDoc(t, ctx, c, key)
		data, _ := doc["data"].(map[string]interface{})
		if data["value"] != "consoleOperator" {
			t.Fatalf("stored canonicalName = %v, want the stored body untouched (consoleOperator)", data["value"])
		}
		if doc["isDeleted"] != true {
			t.Fatalf("isDeleted = %v, want true", doc["isDeleted"])
		}
		if doc["class"] != "canonicalName" {
			t.Fatalf("class = %v, want the stored class carried over", doc["class"])
		}
	})

	t.Run("a roleindex root", func(t *testing.T) {
		roleindexID := "Kn8pRt3vWy6cDf9hJm2q"
		key := "vtx.roleindex." + roleindexID
		commitOne(t, ctx, c, "rid-rits-create", MutationOp{
			Op: "create", Key: key,
			Document: roleindexDoc("consoleOperator", "Hk6mNq9sUvyz4eCr1wDp"),
		})

		if err := commitOneErr(ctx, c, "rid-rits-launder", MutationOp{
			Op: "tombstone", Key: key,
			Document: roleindexDoc("consoleOperator", "Jm7nPr2tVwz5fEs3xFq8"),
		}); err != nil {
			t.Fatalf("Commit(tombstone carrying a document): %v", err)
		}

		doc := readStoredDoc(t, ctx, c, key)
		data, _ := doc["data"].(map[string]interface{})
		if data["roleId"] != "Hk6mNq9sUvyz4eCr1wDp" {
			t.Fatalf("stored roleId = %v, want the stored body untouched", data["roleId"])
		}
		if doc["isDeleted"] != true {
			t.Fatalf("isDeleted = %v, want true", doc["isDeleted"])
		}
	})
}

// readStoredDoc decodes what Core KV holds at key.
func readStoredDoc(t *testing.T, ctx context.Context, c *CommitterImpl, key string) map[string]interface{} {
	t.Helper()
	entry, err := c.Conn.KVGet(ctx, testCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]interface{}
	if uerr := json.Unmarshal(entry.Value, &doc); uerr != nil {
		t.Fatalf("unmarshal %s: %v", key, uerr)
	}
	return doc
}

// The mutation set Commit receives is not always the set step 6 validated: the
// task auto-completion appends an update of the task root AFTER validation, and
// re-derives it on a batch conflict. Every consumer of the prior map degrades
// silently on a missing entry, so Commit tops the map up for what it was not
// handed — without which the injected update re-stamps the task's creation
// provenance from the current operation.
//
// The injected mutation is built the way readTaskAutoCompletion builds it,
// ExpectedRevision and all: that is the only shape the platform emits, so a
// fixture without one would pin a path production never takes.
func TestCommit_TopsUpPriorForInjectedMutation(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	key := "vtx.task.Rq7tVx2zBd5gJn8kMp3s"

	creator := newTestEnvelope(testNanoID1)
	creator.RequestID = "rid-topup-create"
	creator.Actor = "vtx.identity." + testNanoID1
	ack, err := c.Commit(ctx, creator, ScriptResult{Mutations: []MutationOp{{
		Op: "create", Key: key,
		Document: map[string]interface{}{"class": "task", "isDeleted": false,
			"data": map[string]interface{}{"status": "open"}},
	}}}, NewTracker(creator, time.Now()), nil)
	if err != nil {
		t.Fatalf("Commit(create task): %v", err)
	}
	created := readStoredDoc(t, ctx, c, key)
	rev := ack.Revisions[key]
	if rev == 0 {
		t.Fatalf("create ack must name the task root's revision: %v", ack.Revisions)
	}

	// The injected shape: an update of a key the handed map does not cover,
	// CAS'd on the revision the auto-complete read.
	injected := ScriptResult{Mutations: []MutationOp{{
		Op: "update", Key: key, ExpectedRevision: &rev,
		Document: map[string]interface{}{"class": "task", "isDeleted": false,
			"data": map[string]interface{}{"status": "complete", "expiresAt": ""}},
	}}}

	completer := newTestEnvelope(testNanoID2)
	completer.RequestID = "rid-topup-complete"
	completer.Actor = "vtx.identity." + testNanoID2
	if _, err := c.Commit(ctx, completer, injected, NewTracker(completer, time.Now()), PriorDocs{}); err != nil {
		t.Fatalf("Commit(injected update with an unread prior): %v", err)
	}

	after := readStoredDoc(t, ctx, c, key)
	for _, f := range []string{"createdAt", "createdBy", "createdByOp"} {
		if after[f] != created[f] {
			t.Fatalf("%s = %v after the injected update, want the creating operation's %v", f, after[f], created[f])
		}
	}
	if after["createdBy"] != creator.Actor {
		t.Fatalf("createdBy = %v, want the creating actor %q", after["createdBy"], creator.Actor)
	}
	if data, _ := after["data"].(map[string]interface{}); data["status"] != "complete" {
		t.Fatalf("status = %v, want complete", data["status"])
	}
}

// Same revive attempt as TestCommit_TombstoneThenReviveCannotRewriteProvenance,
// retargeted at a vtx.roleindex.<id> root: tombstoning first must not launder a
// roleId rewrite in through the revive.
func TestCommit_TombstoneThenReviveCannotRewriteRoleindexProvenance(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	roleindexID := "Mp9qSu4wYz7dEg1kNr3s"
	key := "vtx.roleindex." + roleindexID

	commitOne(t, ctx, c, "rid-rirev-create", MutationOp{
		Op: "create", Key: key,
		Document: roleindexDoc("consoleOperator", "Hk6mNq9sUvyz4eCr1wDp"),
	})
	if err := commitOneErr(ctx, c, "rid-rirev-tombstone", MutationOp{
		Op: "tombstone", Key: key,
	}); err != nil {
		t.Fatalf("Commit(tombstone): %v", err)
	}

	err := commitOneErr(ctx, c, "rid-rirev-update", MutationOp{
		Op: "update", Key: key,
		Document: roleindexDoc("consoleOperator", "Jm7nPr2tVwz5fEs3xFq8"),
	})
	var provErr *PermissionProvenanceError
	if !errors.As(err, &provErr) {
		t.Fatalf("Commit(revive with a new roleId): %v, want *PermissionProvenanceError", err)
	}
	if provErr.Field != "data" {
		t.Fatalf("rejected on field %q, want data", provErr.Field)
	}
}

// readPriorDoc keeps an entry whose body does not parse rather than failing the
// commit, so the guard sees a key that exists with no readable prior value. It
// cannot prove such a write leaves the guarded fields alone, and this guard
// fails closed rather than treating the key as absent.
func TestCommit_UndecodablePriorDocumentIsRejected(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	key := "vtx.permission.Ge3kBd9fwVps4pH1rJmP"

	if _, err := c.Conn.KVPut(ctx, testCoreBucket, key, []byte("{not json")); err != nil {
		t.Fatalf("seed corrupt document: %v", err)
	}

	err := commitOneErr(ctx, c, "rid-corrupt-update", MutationOp{
		Op: "update", Key: key,
		Document: permissionDoc("ShredRetentionClassKey", "any", "package", "listings-domain", "n"),
	})
	var provErr *PermissionProvenanceError
	if !errors.As(err, &provErr) {
		t.Fatalf("Commit(update over a corrupt body): %v, want *PermissionProvenanceError", err)
	}
	if provErr.Field != "document" {
		t.Fatalf("rejected on field %q, want document", provErr.Field)
	}
}

// packageVertexKey is the Contract #1 vertex key an installed package is
// recorded under, derived from its name exactly as the installer derives it.
func packageVertexKey(name string) string {
	return substrate.VertexKey("package", substrate.PackageEntityNanoID(name, "package"))
}

// manifestDoc renders a package manifest aspect declaring declaredKeys.
func manifestDoc(pkgKey, name string, declaredKeys []string) map[string]interface{} {
	declared := make([]interface{}, 0, len(declaredKeys))
	for _, k := range declaredKeys {
		declared = append(declared, k)
	}
	return map[string]interface{}{
		"class":     "manifest",
		"vertexKey": pkgKey,
		"localName": "manifest",
		"data": map[string]interface{}{
			"name": name, "version": "0.1.0", "declaredKeys": declared,
		},
	}
}

// seedPackage commits a package root vertex and its manifest aspect the way an
// install does, so a later UpgradePackage/UninstallPackage has a stored
// declared-key set to be scoped against.
func seedPackage(t *testing.T, ctx context.Context, c *CommitterImpl, rid, name string, declaredKeys []string) {
	t.Helper()
	pkgKey := packageVertexKey(name)
	commitOne(t, ctx, c, rid+"-pkgroot", MutationOp{
		Op: "create", Key: pkgKey,
		Document: map[string]interface{}{
			"class": "package",
			"data":  map[string]interface{}{"name": name, "version": "0.1.0"},
		},
	})
	commitOne(t, ctx, c, rid+"-manifest", MutationOp{
		Op: "create", Key: pkgKey + ".manifest",
		Document: manifestDoc(pkgKey, name, declaredKeys),
	})
}

// commitPackageOpErr runs a package-lifecycle commit for a named package and
// returns the commit error, so a scope rejection is an assertable outcome. The
// envelope's class and operationType are set independently: the guard must key
// on the class that selects the script, not on operationType alone.
func commitPackageOpErr(ctx context.Context, c *CommitterImpl, rid, operationType, class, packageName string, mutations []MutationOp) error {
	env := newTestEnvelope(testNanoID1)
	env.RequestID = rid
	env.OperationType = operationType
	env.Class = class
	payload, _ := json.Marshal(map[string]any{"name": packageName})
	env.Payload = payload
	tracker := NewTracker(env, time.Now())
	_, err := c.Commit(ctx, env, ScriptResult{Mutations: mutations}, tracker, nil)
	return err
}

func entityDoc(class string) map[string]interface{} {
	return map[string]interface{}{"class": class, "data": map[string]interface{}{"note": "n"}}
}

// scopeCasePackage names the package a table case installs. Every case gets its
// own so one case's seeded keys can never satisfy — or collide with — another's.
func scopeCasePackage(caseIndex int) string {
	return fmt.Sprintf("scope-case-%d", caseIndex)
}

// scopeCaseKey resolves a case-local symbolic key name to a concrete Contract #1
// key, minted per case so no two cases share a document. "@pkg"/"@manifest" are
// the case's own package root and manifest aspect; "@<relation>:<src>:<dst>" is
// a link between two other symbols; anything else is "<vertexType>:<name>".
func scopeCaseKey(caseIndex int, symbol string) string {
	id := func(tag string) string {
		return substrate.PackageEntityNanoID(scopeCasePackage(caseIndex), tag)
	}
	switch symbol {
	case "":
		return ""
	case "@pkg":
		return packageVertexKey(scopeCasePackage(caseIndex))
	case "@manifest":
		return packageVertexKey(scopeCasePackage(caseIndex)) + ".manifest"
	}
	if rest, ok := strings.CutPrefix(symbol, "asp|"); ok {
		parent, localName, found := strings.Cut(rest, "|")
		if !found {
			panic("scopeCaseKey: aspect symbol " + symbol + " is not asp|<parent>|<localName>")
		}
		return scopeCaseKey(caseIndex, parent) + "." + localName
	}
	if rest, ok := strings.CutPrefix(symbol, "@"); ok {
		parts := strings.Split(rest, "|")
		if len(parts) != 3 {
			panic("scopeCaseKey: link symbol " + symbol + " is not @<relation>|<source>|<target>")
		}
		srcType, _, _ := strings.Cut(parts[1], ":")
		dstType, _, _ := strings.Cut(parts[2], ":")
		return "lnk." + srcType + "." + id(parts[1]) + "." + parts[0] + "." + dstType + "." + id(parts[2])
	}
	vertexType, localName, ok := strings.Cut(symbol, ":")
	if !ok {
		panic("scopeCaseKey: symbol " + symbol + " is not <vertexType>:<name>")
	}
	return substrate.VertexKey(vertexType, id(vertexType+":"+localName))
}

// A package-lifecycle batch carries client-supplied mutation bodies that no DDL
// gates, and step 3 authorizes the VERB rather than the target — so nothing but
// this guard binds the batch to the package it claims to act for. It holds four
// rules: no holdsRole link at all; a created link may only hang off a source
// endpoint the package owns; an update or tombstone may only target an owned
// key; and the manifest the owned set is READ from is itself validated, so it
// cannot be used to grant ownership to itself.
func TestCommit_PackageMutationsAreScopedToTheirPackage(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)

	type mutation struct {
		op  string
		key string // a scopeCaseKey symbol
		// claims, when set on a mutation targeting "@manifest", is the
		// declaredKeys the submitted manifest document carries.
		claims []string
	}
	tests := []struct {
		name string
		// installed seeds the case's own package vertex + manifest aspect.
		installed bool
		declared  []string
		// seed is created before the guarded batch; tombstoned names the subset
		// of it left in the tombstoned state; uninstalled tombstones the seeded
		// manifest aspect.
		seed        []mutation
		tombstoned  []string
		uninstalled bool
		// opType and class default to the case's lifecycle op; a case may set
		// them independently to prove the guard keys on the executing class.
		opType     string
		class      string
		mutations  []mutation
		wantReason string
		wantKey    string
	}{
		{
			name:      "upgrade updates a surviving declared key",
			installed: true,
			declared:  []string{"@pkg", "permission:own"},
			seed:      []mutation{{op: "create", key: "permission:own"}},
			opType:    "UpgradePackage",
			mutations: []mutation{{op: "update", key: "permission:own"}},
		},
		{
			name:      "upgrade tombstones a declared key it no longer declares",
			installed: true,
			declared:  []string{"@pkg", "meta:droppedLens"},
			seed:      []mutation{{op: "create", key: "meta:droppedLens"}},
			opType:    "UpgradePackage",
			mutations: []mutation{{op: "tombstone", key: "meta:droppedLens"}},
		},
		{
			name:      "upgrade updates its own package root and manifest aspect",
			installed: true,
			declared:  []string{"@pkg"},
			opType:    "UpgradePackage",
			mutations: []mutation{
				{op: "update", key: "@pkg"},
				{op: "update", key: "@manifest", claims: []string{"@pkg"}},
			},
		},
		{
			name:      "upgrade creates a key its prior manifest never declared",
			installed: true,
			declared:  []string{"@pkg"},
			opType:    "UpgradePackage",
			mutations: []mutation{
				{op: "create", key: "meta:freshLens"},
				{op: "update", key: "@manifest", claims: []string{"@pkg", "meta:freshLens"}},
			},
		},
		{
			// The legitimate re-add, mirroring diffManifest's revive branch: the
			// entity a prior version dropped is tombstoned, the new manifest
			// declares it again, and the diff emits an update rather than a
			// create because the key still exists.
			name:       "upgrade revives a tombstoned key its new manifest re-declares",
			installed:  true,
			declared:   []string{"@pkg"},
			seed:       []mutation{{op: "create", key: "meta:revivedLens"}},
			tombstoned: []string{"meta:revivedLens"},
			opType:     "UpgradePackage",
			mutations: []mutation{
				{op: "update", key: "@manifest", claims: []string{"@pkg", "meta:revivedLens"}},
				{op: "update", key: "meta:revivedLens"},
			},
		},
		{
			name:      "uninstall tombstones its whole declared set plus its manifest",
			installed: true,
			declared:  []string{"@pkg", "permission:own"},
			seed:      []mutation{{op: "create", key: "permission:own"}},
			opType:    "UninstallPackage",
			mutations: []mutation{
				{op: "tombstone", key: "permission:own"},
				{op: "tombstone", key: "@pkg"},
				{op: "tombstone", key: "@manifest"},
			},
		},
		{
			// A fresh install mints both endpoints of its own forOperation edge.
			name:   "install creates a link between two keys it creates here",
			opType: "InstallPackage",
			mutations: []mutation{
				{op: "create", key: "permission:own"},
				{op: "create", key: "meta:opMeta"},
				{op: "create", key: "@forOperation|permission:own|meta:opMeta"},
			},
		},
		{
			// The shape every real package ships: grant a permission it declares
			// to a role it did not (the primordial operator). The TARGET endpoint
			// is deliberately unconstrained — constraining it would reject every
			// package in the repo.
			name:   "install grants its own permission to a role it never declared",
			opType: "InstallPackage",
			mutations: []mutation{
				{op: "create", key: "permission:own"},
				{op: "create", key: "@grantedBy|permission:own|role:foreignOperator"},
			},
		},
		{
			name:      "upgrade grants a surviving declared permission to a foreign role",
			installed: true,
			declared:  []string{"@pkg", "permission:own"},
			seed:      []mutation{{op: "create", key: "permission:own"}},
			opType:    "UpgradePackage",
			mutations: []mutation{{op: "create", key: "@grantedBy|permission:own|role:foreignOperator"}},
		},
		{
			// The guard is scoped to the package-lifecycle trio: RBAC's own ops
			// are exactly how a holdsRole link is meant to be written.
			name:      "a non package operation may write a holdsRole link",
			opType:    "GrantRole",
			mutations: []mutation{{op: "create", key: "@holdsRole|identity:actor|role:operator"}},
		},
		{
			name:       "upgrade updates a live key another package owns",
			installed:  true,
			declared:   []string{"@pkg"},
			seed:       []mutation{{op: "create", key: "permission:foreign"}},
			opType:     "UpgradePackage",
			mutations:  []mutation{{op: "update", key: "permission:foreign"}},
			wantReason: "unscoped",
			wantKey:    "permission:foreign",
		},
		{
			name:       "upgrade tombstones a live key another package owns",
			installed:  true,
			declared:   []string{"@pkg"},
			seed:       []mutation{{op: "create", key: "meta:foreignLens"}},
			opType:     "UpgradePackage",
			mutations:  []mutation{{op: "tombstone", key: "meta:foreignLens"}},
			wantReason: "unscoped",
			wantKey:    "meta:foreignLens",
		},
		{
			name:       "uninstall tombstones a live key it never declared",
			installed:  true,
			declared:   []string{"@pkg"},
			seed:       []mutation{{op: "create", key: "permission:foreign"}},
			opType:     "UninstallPackage",
			mutations:  []mutation{{op: "tombstone", key: "permission:foreign"}},
			wantReason: "unscoped",
			wantKey:    "permission:foreign",
		},
		{
			// The confirmed dominant repro: a bare create of a role assignment
			// inside a package diff, which needs no prior document to rewrite.
			name:       "upgrade creates a holdsRole link",
			installed:  true,
			declared:   []string{"@pkg"},
			opType:     "UpgradePackage",
			mutations:  []mutation{{op: "create", key: "@holdsRole|identity:actor|role:operator"}},
			wantReason: "holdsRole",
			wantKey:    "@holdsRole|identity:actor|role:operator",
		},
		{
			name:       "install creates a holdsRole link",
			opType:     "InstallPackage",
			mutations:  []mutation{{op: "create", key: "@holdsRole|identity:actor|role:operator"}},
			wantReason: "holdsRole",
			wantKey:    "@holdsRole|identity:actor|role:operator",
		},
		{
			name:       "uninstall tombstones a holdsRole link it declared",
			installed:  true,
			declared:   []string{"@pkg", "@holdsRole|identity:actor|role:operator"},
			seed:       []mutation{{op: "create", key: "@holdsRole|identity:actor|role:operator"}},
			opType:     "UninstallPackage",
			mutations:  []mutation{{op: "tombstone", key: "@holdsRole|identity:actor|role:operator"}},
			wantReason: "holdsRole",
			wantKey:    "@holdsRole|identity:actor|role:operator",
		},
		{
			// Grant an EXISTING kernel permission to a role the actor already
			// holds. Not a holdsRole link, so a name-matching rule misses it; the
			// source endpoint is a permission this package never owned.
			name:       "install grants a permission it does not own to a role",
			opType:     "InstallPackage",
			seed:       []mutation{{op: "create", key: "permission:kernel"}},
			mutations:  []mutation{{op: "create", key: "@grantedBy|permission:kernel|role:operator"}},
			wantReason: "linkSource",
			wantKey:    "@grantedBy|permission:kernel|role:operator",
		},
		{
			name:       "upgrade grants a permission another package owns to a role",
			installed:  true,
			declared:   []string{"@pkg"},
			seed:       []mutation{{op: "create", key: "permission:kernel"}},
			opType:     "UpgradePackage",
			mutations:  []mutation{{op: "create", key: "@grantedBy|permission:kernel|role:operator"}},
			wantReason: "linkSource",
			wantKey:    "@grantedBy|permission:kernel|role:operator",
		},
		{
			// The manifest is the document the owned set is read FROM, so an
			// unchecked exemption on it is a self-serve ownership grant.
			name:      "upgrade claims a live foreign key into its own manifest",
			installed: true,
			declared:  []string{"@pkg"},
			seed:      []mutation{{op: "create", key: "permission:foreign"}},
			opType:    "UpgradePackage",
			mutations: []mutation{
				{op: "update", key: "@manifest", claims: []string{"@pkg", "permission:foreign"}},
				{op: "update", key: "permission:foreign"},
			},
			wantReason: "manifestClaim",
			wantKey:    "permission:foreign",
		},
		{
			// An absent key is claimable only by creating it here — otherwise a
			// package could squat a key another package has yet to mint.
			name:       "upgrade claims an absent key it does not create",
			installed:  true,
			declared:   []string{"@pkg"},
			opType:     "UpgradePackage",
			mutations:  []mutation{{op: "update", key: "@manifest", claims: []string{"@pkg", "meta:neverMinted"}}},
			wantReason: "manifestClaim",
			wantKey:    "meta:neverMinted",
		},
		{
			// Reviving a dead key still needs the manifest to claim it, and the
			// claim is what the update is then measured against.
			name:       "upgrade revives a dead key without claiming it",
			installed:  true,
			declared:   []string{"@pkg"},
			seed:       []mutation{{op: "create", key: "meta:foreignDeadLens"}},
			tombstoned: []string{"meta:foreignDeadLens"},
			opType:     "UpgradePackage",
			mutations:  []mutation{{op: "update", key: "meta:foreignDeadLens"}},
			wantReason: "unscoped",
			wantKey:    "meta:foreignDeadLens",
		},
		{
			name:       "upgrade naming an uninstalled package rejects",
			opType:     "UpgradePackage",
			mutations:  []mutation{{op: "update", key: "permission:foreign"}},
			wantReason: "unscoped",
			wantKey:    "permission:foreign",
		},
		{
			// A tombstoned manifest is an uninstalled package. Its declaredKeys
			// survive in the body the tombstone carried forward and still list
			// the retention-class holders uninstall deliberately left live, so
			// honouring it would hand a dead package's stale set to a live batch.
			name:        "upgrade naming an already uninstalled package rejects",
			installed:   true,
			declared:    []string{"@pkg", "permission:own"},
			seed:        []mutation{{op: "create", key: "permission:own"}},
			uninstalled: true,
			opType:      "UpgradePackage",
			mutations:   []mutation{{op: "update", key: "permission:own"}},
			wantReason:  "unscoped",
			wantKey:     "permission:own",
		},
		{
			name:       "uninstall naming an uninstalled package rejects",
			opType:     "UninstallPackage",
			mutations:  []mutation{{op: "tombstone", key: "permission:foreign"}},
			wantReason: "unscoped",
			wantKey:    "permission:foreign",
		},
		{
			name:      "upgrade naming an uninstalled package may still create",
			opType:    "UpgradePackage",
			mutations: []mutation{{op: "create", key: "meta:freshLens"}},
		},
		{
			// The executing script is chosen by CLASS, so an envelope wearing a
			// granted operationType while running the upgrade script must be
			// held to the upgrade's rules.
			name:       "a lifecycle class under an unrelated operationType is still scoped",
			opType:     "CreateIdentity",
			class:      "UpgradePackage",
			mutations:  []mutation{{op: "update", key: "permission:foreign"}},
			wantReason: "unscoped",
			wantKey:    "permission:foreign",
		},
		{
			name:       "a lifecycle class under an unrelated operationType is still holdsRole gated",
			opType:     "CreateIdentity",
			class:      "InstallPackage",
			mutations:  []mutation{{op: "create", key: "@holdsRole|identity:actor|role:operator"}},
			wantReason: "holdsRole",
			wantKey:    "@holdsRole|identity:actor|role:operator",
		},
		{
			// The converse: a lifecycle operationType under a class that runs no
			// package script is still covered, since operationType is the
			// fallback resolveClass itself uses.
			name:       "a lifecycle operationType under an unrelated class is still scoped",
			opType:     "UpgradePackage",
			class:      "identity",
			mutations:  []mutation{{op: "update", key: "permission:foreign"}},
			wantReason: "unscoped",
			wantKey:    "permission:foreign",
		},
		{
			// The shape a real install submits: build.go's addCreate appends
			// every created key to the very slice that becomes declaredKeys, so
			// an honest manifest declares exactly what its batch mints.
			name:   "install declares exactly the keys it creates",
			opType: "InstallPackage",
			mutations: []mutation{
				{op: "create", key: "@pkg"},
				{op: "create", key: "permission:own"},
				{op: "create", key: "meta:ownLens"},
				{op: "create", key: "@manifest", claims: []string{"@pkg", "permission:own", "meta:ownLens"}},
			},
		},
		{
			// A manifest is the document every ownership answer is read from, so
			// an unvalidated CREATE mints that root of trust from nothing: forge
			// the declared set here, then return in a second op and touch the
			// victim key as one you now "own".
			name:   "install forges a manifest declaring a key it does not create",
			opType: "InstallPackage",
			seed:   []mutation{{op: "create", key: "permission:foreign"}},
			mutations: []mutation{
				{op: "create", key: "@pkg"},
				{op: "create", key: "@manifest", claims: []string{"@pkg", "permission:foreign"}},
			},
			wantReason: "manifestClaim",
			wantKey:    "permission:foreign",
		},
		{
			name:   "upgrade on an uninstalled name forges a manifest",
			opType: "UpgradePackage",
			seed:   []mutation{{op: "create", key: "permission:foreign"}},
			mutations: []mutation{
				{op: "create", key: "@pkg"},
				{op: "create", key: "@manifest", claims: []string{"@pkg", "permission:foreign"}},
			},
			wantReason: "manifestClaim",
			wantKey:    "permission:foreign",
		},
		{
			// A manifest is identified by its KEY SHAPE, not by the name the
			// payload claims — forging one for a package the batch does not even
			// name is still forging a manifest.
			name:   "install forges a manifest for a package it never names",
			opType: "InstallPackage",
			seed:   []mutation{{op: "create", key: "permission:foreign"}},
			mutations: []mutation{
				{op: "create", key: "package:unnamed"},
				{op: "create", key: "asp|package:unnamed|manifest", claims: []string{"permission:foreign"}},
			},
			wantReason: "manifestClaim",
			wantKey:    "permission:foreign",
		},
		{
			name:   "install creates an aspect on a vertex it mints here",
			opType: "InstallPackage",
			mutations: []mutation{
				{op: "create", key: "meta:ownLens"},
				{op: "create", key: "asp|meta:ownLens|canonicalName"},
			},
		},
		{
			name:      "upgrade creates an aspect on a vertex it already declared",
			installed: true,
			declared:  []string{"@pkg", "meta:ownLens"},
			seed:      []mutation{{op: "create", key: "meta:ownLens"}},
			opType:    "UpgradePackage",
			mutations: []mutation{{op: "create", key: "asp|meta:ownLens|description"}},
		},
		{
			// A vtx.meta.* write invalidates the DDL cache in-commit, so an
			// aspect forged onto a primordial op-meta root is live immediately.
			name:       "upgrade creates an aspect on a foreign vertex",
			installed:  true,
			declared:   []string{"@pkg"},
			seed:       []mutation{{op: "create", key: "meta:foreignOpMeta"}},
			opType:     "UpgradePackage",
			mutations:  []mutation{{op: "create", key: "asp|meta:foreignOpMeta|dispatch"}},
			wantReason: "aspectParent",
			wantKey:    "asp|meta:foreignOpMeta|dispatch",
		},
		{
			name:       "install creates an aspect on a foreign vertex",
			opType:     "InstallPackage",
			seed:       []mutation{{op: "create", key: "meta:foreignOpMeta"}},
			mutations:  []mutation{{op: "create", key: "asp|meta:foreignOpMeta|dispatch"}},
			wantReason: "aspectParent",
			wantKey:    "asp|meta:foreignOpMeta|dispatch",
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rid := fmt.Sprintf("rid-pkgscope-%d", i)
			key := func(symbol string) string { return scopeCaseKey(i, symbol) }
			pkgName := scopeCasePackage(i)
			build := func(m mutation) MutationOp {
				doc := entityDoc("entity")
				if m.claims != nil {
					claimed := make([]string, 0, len(m.claims))
					for _, sym := range m.claims {
						claimed = append(claimed, key(sym))
					}
					doc = manifestDoc(key("@pkg"), pkgName, claimed)
				}
				return MutationOp{Op: m.op, Key: key(m.key), Document: doc}
			}

			if tt.installed {
				declared := make([]string, 0, len(tt.declared))
				for _, sym := range tt.declared {
					declared = append(declared, key(sym))
				}
				seedPackage(t, ctx, c, rid, pkgName, declared)
			}
			for j, m := range tt.seed {
				commitOne(t, ctx, c, fmt.Sprintf("%s-seed-%d", rid, j), build(m))
			}
			for j, sym := range tt.tombstoned {
				if err := commitOneErr(ctx, c, fmt.Sprintf("%s-tomb-%d", rid, j),
					MutationOp{Op: "tombstone", Key: key(sym)}); err != nil {
					t.Fatalf("seed tombstone %s: %v", key(sym), err)
				}
			}
			if tt.uninstalled {
				if err := commitOneErr(ctx, c, rid+"-uninstall",
					MutationOp{Op: "tombstone", Key: key("@manifest")}); err != nil {
					t.Fatalf("seed manifest tombstone: %v", err)
				}
			}

			mutations := make([]MutationOp, 0, len(tt.mutations))
			for _, m := range tt.mutations {
				mutations = append(mutations, build(m))
			}
			class := tt.class
			if class == "" {
				class = tt.opType
			}
			err := commitPackageOpErr(ctx, c, rid+"-guarded", tt.opType, class, pkgName, mutations)

			var pkgErr *PackageScopeError
			switch {
			case tt.wantReason == "":
				if err != nil {
					t.Fatalf("Commit(op %s class %s): %v, want accepted", tt.opType, class, err)
				}
			case !errors.As(err, &pkgErr):
				t.Fatalf("Commit(op %s class %s): %v, want *PackageScopeError with reason %q",
					tt.opType, class, err, tt.wantReason)
			case pkgErr.Reason != tt.wantReason:
				t.Fatalf("rejected with reason %q, want %q (err %v)", pkgErr.Reason, tt.wantReason, err)
			case pkgErr.Key != key(tt.wantKey):
				t.Fatalf("rejected key %q, want %q", pkgErr.Key, key(tt.wantKey))
			}
		})
	}
}

// A rejection runs before the atomic batch, so nothing the out-of-scope batch
// carried may land — including the mutations that were themselves in scope.
func TestCommit_PackageScopeRejectionLeavesTheWholeBatchUnwritten(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)

	const (
		pkg        = "scope-atomicity-package"
		ownPermID  = "Ub3wXy6zAbcd9eFg4hjK"
		foreignID  = "Vc4xYz7aBcde2fGh5jkL"
		identityID = "Wd5yZa8bCdef3gHj6kmN"
		roleID     = "Xe6zAb9cDefg4hJk7mnP"
	)
	ownKey := "vtx.permission." + ownPermID
	foreignKey := "vtx.permission." + foreignID

	seedPackage(t, ctx, c, "rid-atomic", pkg, []string{packageVertexKey(pkg), ownKey})
	commitOne(t, ctx, c, "rid-atomic-own", MutationOp{
		Op: "create", Key: ownKey, Document: entityDoc("permission"),
	})
	commitOne(t, ctx, c, "rid-atomic-foreign", MutationOp{
		Op: "create", Key: foreignKey, Document: entityDoc("permission"),
	})

	err := commitPackageOpErr(ctx, c, "rid-atomic-guarded", "UpgradePackage", "UpgradePackage", pkg, []MutationOp{
		{Op: "update", Key: ownKey, Document: map[string]interface{}{
			"class": "permission", "data": map[string]interface{}{"note": "in scope, still not written"},
		}},
		{Op: "tombstone", Key: foreignKey},
		{Op: "create", Key: "lnk.identity." + identityID + ".holdsRole.role." + roleID,
			Document: entityDoc("holdsRole")},
	})
	var pkgErr *PackageScopeError
	if !errors.As(err, &pkgErr) {
		t.Fatalf("Commit(out-of-scope upgrade batch): %v, want *PackageScopeError", err)
	}

	entry, err := c.Conn.KVGet(ctx, testCoreBucket, ownKey)
	if err != nil {
		t.Fatalf("KVGet %s: %v", ownKey, err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", ownKey, err)
	}
	data, _ := doc["data"].(map[string]interface{})
	if data["note"] != "n" {
		t.Fatalf("an in-scope mutation of the rejected batch landed: note = %v", data["note"])
	}
	foreign, err := c.Conn.KVGet(ctx, testCoreBucket, foreignKey)
	if err != nil {
		t.Fatalf("KVGet %s: %v", foreignKey, err)
	}
	var foreignDoc map[string]interface{}
	if err := json.Unmarshal(foreign.Value, &foreignDoc); err != nil {
		t.Fatalf("unmarshal %s: %v", foreignKey, err)
	}
	if del, _ := foreignDoc["isDeleted"].(bool); del {
		t.Fatalf("%s was tombstoned by a rejected batch", foreignKey)
	}
}

// The manifest is read server-side from the package vertex key the NAME derives,
// so the name a caller asserts buys exactly that package's owned surface and
// nothing else. The identical batch is accepted for the package that owns the
// key and rejected for the one that does not.
func TestCommit_PackageScopeFollowsTheNamedPackagesOwnManifest(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)

	const (
		pkgA   = "scope-claimant-a"
		pkgB   = "scope-claimant-b"
		permID = "Yf7aBc2dEfgh5jKm8npQ"
	)
	permKey := "vtx.permission." + permID

	seedPackage(t, ctx, c, "rid-claim-a", pkgA, []string{packageVertexKey(pkgA), permKey})
	seedPackage(t, ctx, c, "rid-claim-b", pkgB, []string{packageVertexKey(pkgB)})
	commitOne(t, ctx, c, "rid-claim-perm", MutationOp{
		Op: "create", Key: permKey, Document: entityDoc("permission"),
	})

	update := []MutationOp{{Op: "update", Key: permKey, Document: entityDoc("permission")}}
	if err := commitPackageOpErr(ctx, c, "rid-claim-owner", "UpgradePackage", "UpgradePackage", pkgA, update); err != nil {
		t.Fatalf("Commit(upgrade of %q's own key, named as %q): %v, want accepted", pkgA, pkgA, err)
	}

	err := commitPackageOpErr(ctx, c, "rid-claim-other", "UpgradePackage", "UpgradePackage", pkgB, update)
	var pkgErr *PackageScopeError
	if !errors.As(err, &pkgErr) {
		t.Fatalf("Commit(upgrade of %q's key, named as %q): %v, want *PackageScopeError", pkgA, pkgB, err)
	}
	if pkgErr.Reason != "unscoped" || pkgErr.Package != pkgB {
		t.Fatalf("rejected as %+v, want reason unscoped for package %q", pkgErr, pkgB)
	}
}

// The two-operation forge the manifest-create rule exists to stop: op 1 mints a
// package whose manifest declares a victim key it never created; op 2 comes back
// as a routine upgrade of that package and touches the key as one it now owns.
// Op 2 is only reachable if op 1 commits, so the whole sequence has to die at
// op 1 — the manifest is the root of trust every later ownership answer is read
// from, and a create that mints it from nothing establishes nothing.
func TestCommit_ForgedManifestCannotBootstrapOwnership(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)

	const (
		pkg      = "scope-forge-bootstrap"
		victimID = "Ah9cDe4fGhjk7mNp2qrS"
	)
	victimKey := "vtx.permission." + victimID
	pkgKey := packageVertexKey(pkg)

	commitOne(t, ctx, c, "rid-forge-victim", MutationOp{
		Op: "create", Key: victimKey, Document: entityDoc("permission"),
	})

	// Op 1 — mint the package and a manifest claiming the victim key.
	err := commitPackageOpErr(ctx, c, "rid-forge-op1", "InstallPackage", "InstallPackage", pkg, []MutationOp{
		{Op: "create", Key: pkgKey, Document: entityDoc("package")},
		{Op: "create", Key: pkgKey + ".manifest",
			Document: manifestDoc(pkgKey, pkg, []string{pkgKey, victimKey})},
	})
	var pkgErr *PackageScopeError
	if !errors.As(err, &pkgErr) {
		t.Fatalf("Commit(forged manifest create): %v, want *PackageScopeError", err)
	}
	if pkgErr.Reason != "manifestClaim" || pkgErr.Key != victimKey {
		t.Fatalf("rejected as %+v, want manifestClaim on %s", pkgErr, victimKey)
	}

	// Nothing from op 1 may have landed, so op 2 has no manifest to read back.
	if _, err := c.Conn.KVGet(ctx, testCoreBucket, pkgKey+".manifest"); !errors.Is(err, substrate.ErrKeyNotFound) {
		t.Fatalf("KVGet forged manifest: %v, want ErrKeyNotFound", err)
	}

	// Op 2 — the follow-up that would have cashed the forged claim.
	err = commitPackageOpErr(ctx, c, "rid-forge-op2", "UpgradePackage", "UpgradePackage", pkg, []MutationOp{
		{Op: "update", Key: victimKey, Document: entityDoc("permission")},
	})
	if !errors.As(err, &pkgErr) {
		t.Fatalf("Commit(follow-up upgrade): %v, want *PackageScopeError", err)
	}
	if pkgErr.Reason != "unscoped" {
		t.Fatalf("rejected with reason %q, want unscoped", pkgErr.Reason)
	}

	entry, err := c.Conn.KVGet(ctx, testCoreBucket, victimKey)
	if err != nil {
		t.Fatalf("KVGet %s: %v", victimKey, err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", victimKey, err)
	}
	if doc["createdByOp"] == nil {
		t.Fatalf("victim key lost its creation provenance: %+v", doc)
	}
}

// An upgrade or uninstall acts FOR a package. A payload that does not decode
// names none, so it identifies no surface — and unlike the update path, a
// create-only batch must not slip through on the technicality that there was
// nothing to measure against an owned set.
func TestCommit_PackageScopeRejectsAnUndecodablePayloadIncludingCreates(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)

	submit := func(rid string, m MutationOp) error {
		env := newTestEnvelope(testNanoID1)
		env.RequestID = rid
		env.OperationType = "UpgradePackage"
		env.Class = "UpgradePackage"
		env.Payload = json.RawMessage(`{"name": `)
		tracker := NewTracker(env, time.Now())
		_, err := c.Commit(ctx, env, ScriptResult{Mutations: []MutationOp{m}}, tracker, nil)
		return err
	}

	for _, m := range []MutationOp{
		{Op: "update", Key: "vtx.permission.Bj2dEf5gHjkm8nPq3rtU", Document: entityDoc("permission")},
		{Op: "create", Key: "vtx.permission.Ck3eFg6hJkmn9pQr4stV", Document: entityDoc("permission")},
	} {
		err := submit("rid-malformed-"+m.Op, m)
		var pkgErr *PackageScopeError
		if !errors.As(err, &pkgErr) {
			t.Fatalf("Commit(%s under an undecodable payload): %v, want *PackageScopeError", m.Op, err)
		}
		if pkgErr.Reason != "unscoped" {
			t.Fatalf("%s rejected with reason %q, want unscoped", m.Op, pkgErr.Reason)
		}
	}
}

// resolveClass reads payload.class when the envelope carries no top-level one,
// so an envelope can run the upgrade script while declaring neither a lifecycle
// class nor a lifecycle operationType. The guard has to follow the class down
// the same path, or the cheapest disguise walks straight past it.
func TestCommit_PackageScopeFollowsAPayloadCarriedClass(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	const pkg = "scope-payload-class"
	victimKey := "vtx.permission.Dm4fGh7jKmnp2qRs5tuW"

	commitOne(t, ctx, c, "rid-payloadclass-seed", MutationOp{
		Op: "create", Key: victimKey, Document: entityDoc("permission"),
	})

	env := newTestEnvelope(testNanoID1)
	env.RequestID = "rid-payloadclass-guarded"
	env.OperationType = "CreateIdentity"
	env.Class = ""
	payload, err := json.Marshal(map[string]any{"class": "UpgradePackage", "name": pkg})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env.Payload = payload
	tracker := NewTracker(env, time.Now())
	_, err = c.Commit(ctx, env, ScriptResult{Mutations: []MutationOp{
		{Op: "update", Key: victimKey, Document: entityDoc("permission")},
	}}, tracker, nil)

	var pkgErr *PackageScopeError
	if !errors.As(err, &pkgErr) {
		t.Fatalf("Commit(upgrade class carried in the payload): %v, want *PackageScopeError", err)
	}
	if pkgErr.Reason != "unscoped" || pkgErr.Key != victimKey {
		t.Fatalf("rejected as %+v, want unscoped on %s", pkgErr, victimKey)
	}
}

// The step-8 guards read a mutation's protected ROOT at commit time, not with
// the step-5.5 stored-class pass, so a root that turns protected between
// validation and the batch is still refused. An aspect's root is in no batch
// and conditions nothing, so nothing else on the path would notice the flip —
// unlike a mutation key, whose own read revision conditions the write.
func TestCommit_ProtectedRootFlippedAfterValidateIsRefused(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	root := "vtx.identity." + testNanoID2
	aspect := root + ".ssn"
	seedIdentityRootProtection(t, ctx, conn, root, false)
	seedAspect(t, ctx, conn, aspect, "ssn")
	seedScriptSource(t, ctx, conn, "identity", `
def execute(state, op):
    return {"mutations": [{"op": "tombstone", "key": "`+aspect+`"}], "events": []}
`)

	logger := testLogger()
	cache := NewDDLCache(conn, testCoreBucket, logger)
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("ddl cache refresh: %v", err)
	}
	// The interposed hook is a concurrent operator marking the root protected
	// in the window step 6 has just left.
	flipped := &hookedValidator{inner: NewValidator(cache, conn, testCoreBucket, logger), after: func() {
		seedIdentityRootProtection(t, ctx, conn, root, true)
	}}
	cp, cons := newInjectedPipeline(t, ctx, conn,
		NewCommitter(conn, testCoreBucket, cache, logger, time.Now, nil), flipped, "root-flip")

	env := newTestEnvelope(testNanoID1)
	sub := publishWithReply(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeRejected)

	reply := awaitReply(t, sub)
	if reply.Error == nil || reply.Error.Code != ErrCodeProtectedKey {
		t.Fatalf("reply error = %+v, want ProtectedKey", reply.Error)
	}
	if got := reply.Error.Details["root"]; got != root {
		t.Fatalf("details.root = %v, want %s", got, root)
	}
	if doc := readStoredDocConn(t, ctx, conn, aspect); doc["isDeleted"] == true {
		t.Fatalf("the refused tombstone must not have landed")
	}
}

// hookedValidator runs a side effect the instant step 6 returns — the seam a
// test uses to move the world in the window between validation and the batch.
type hookedValidator struct {
	inner Validator
	after func()
}

func (h *hookedValidator) Validate(ctx context.Context, env *OperationEnvelope, result ScriptResult, state HydratedState, prior PriorDocs) error {
	err := h.inner.Validate(ctx, env, result, state, prior)
	if h.after != nil {
		h.after()
	}
	return err
}

// seedIdentityRootProtection writes an identity root carrying data.protected.
func seedIdentityRootProtection(t *testing.T, ctx context.Context, conn *substrate.Conn, key string, protected bool) {
	t.Helper()
	doc := []byte(fmt.Sprintf(
		`{"class":"identity","isDeleted":false,"data":{"protected":%t}}`, protected))
	if _, err := conn.KVPut(ctx, testCoreBucket, key, doc); err != nil {
		t.Fatalf("seed root %s: %v", key, err)
	}
}

// seedAspect writes a minimal committed aspect envelope.
func seedAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class string) {
	t.Helper()
	doc := []byte(fmt.Sprintf(`{"class":%q,"isDeleted":false,"data":{}}`, class))
	if _, err := conn.KVPut(ctx, testCoreBucket, key, doc); err != nil {
		t.Fatalf("seed aspect %s: %v", key, err)
	}
}

// readStoredDocConn decodes what Core KV holds at key, off a bare connection.
func readStoredDocConn(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) map[string]interface{} {
	t.Helper()
	entry, err := conn.KVGet(ctx, testCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]interface{}
	if uerr := json.Unmarshal(entry.Value, &doc); uerr != nil {
		t.Fatalf("unmarshal %s: %v", key, uerr)
	}
	return doc
}

// The task auto-completion's injected update is a PLATFORM-AUTHORED mutation
// appended after step 6, and it is exempt from permittedCommands by design: the
// `task` DDL admits the task operations alone, so gating the injection would
// refuse every task-bound business operation the moment it closed its own task.
// The exemption is pinned in both directions — the same mutation set IS refused
// when put through the validator, and commits when it rides the injection path.
//
// It doubles as the end-to-end top-up pin: the injected key is one no prior
// pass read, so the task root's creation provenance survives only because
// Commit reads it for itself.
func TestCommit_InjectedTaskUpdateIsNotGatedByPermittedCommands(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	// A task DDL whose permittedCommands names the task operations and not the
	// business operation below — the shipped shape.
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta.task", []byte(
		`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{"canonicalName":"task","permittedCommands":["CompleteTask","CancelTask"]}}`,
	)); err != nil {
		t.Fatalf("seed task DDL: %v", err)
	}
	logger := testLogger()
	cache := NewDDLCache(conn, testCoreBucket, logger)
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	c := NewCommitter(conn, testCoreBucket, cache, logger, time.Now, nil)

	taskKey := "vtx.task.Wq4tYx7zBd2gJn5kMp8s"
	creator := newTestEnvelope(testNanoID1)
	creator.RequestID = "rid-inject-create"
	creator.Actor = "vtx.identity." + testNanoID1
	if _, err := c.Commit(ctx, creator, ScriptResult{Mutations: []MutationOp{{
		Op: "create", Key: taskKey,
		Document: map[string]interface{}{"class": "task", "isDeleted": false,
			"data": map[string]interface{}{"status": "open", "expiresAt": "2030-01-01T00:00:00Z"}},
	}}}, NewTracker(creator, time.Now()), nil); err != nil {
		t.Fatalf("Commit(create task): %v", err)
	}
	created := readStoredDocConn(t, ctx, conn, taskKey)

	env := newTestEnvelope(testNanoID2)
	env.RequestID = "rid-inject-complete"
	env.OperationType = "ApproveLease" // the task DDL does not admit it
	env.Actor = "vtx.identity." + testNanoID2
	business := ScriptResult{Mutations: []MutationOp{{
		Op:  "create",
		Key: "vtx.leaseapp." + testNanoID1,
		Document: map[string]interface{}{"class": "leaseapp", "isDeleted": false,
			"data": map[string]interface{}{"state": "approved"}},
	}}}

	// What step 6 would say about the injection, had it seen it.
	ac, err := readTaskAutoCompletion(ctx, conn, testCoreBucket, taskKey)
	if err != nil || !ac.open {
		t.Fatalf("readTaskAutoCompletion: open=%v err=%v", ac.open, err)
	}
	v := NewValidator(cache, conn, testCoreBucket, logger)
	prior := PriorDocs{taskKey: priorDoc{Doc: created, Revision: ac.revision, Found: true}}
	if err := v.Validate(ctx, env, business, HydratedState{}, prior); err != nil {
		t.Fatalf("the business mutation itself must validate: %v", err)
	}
	augmented := injectTaskAutoCompletion(business, ac)
	verr := v.Validate(ctx, env, augmented, HydratedState{}, prior)
	var ddlErr *DDLViolation
	if !errors.As(verr, &ddlErr) || ddlErr.MutationKey != taskKey {
		t.Fatalf("the injected update must be what step 6 would refuse, got %T %v", verr, verr)
	}

	// And the injection path commits it regardless — that is the exemption.
	cp := acTestCommitPath(conn, c)
	if _, err := cp.commitWithTaskAutoComplete(ctx, env, business, NewTracker(env, time.Now()),
		acTaskPathPermission(taskKey), PriorDocs{}); err != nil {
		t.Fatalf("the platform-authored injection must commit: %v", err)
	}

	after := readStoredDocConn(t, ctx, conn, taskKey)
	if data, _ := after["data"].(map[string]interface{}); data["status"] != "complete" {
		t.Fatalf("status = %v, want complete", data["status"])
	}
	for _, f := range []string{"createdAt", "createdBy", "createdByOp"} {
		if after[f] != created[f] {
			t.Fatalf("%s = %v after the injection, want the creating operation's %v", f, after[f], created[f])
		}
	}
}

// The prior pass runs inside the OCC retry loop, once per attempt. A key
// discovered at execution time is conditioned on the revision that pass read,
// so a write landing in the window makes the batch conflict — and the retry
// must re-read, or attempt two conditions on the same stale revision and the
// operation can never converge.
func TestCommitPath_OCCRetryReReadsPriorDocuments(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	key := "vtx.identity." + testNanoID2
	seedAspect(t, ctx, conn, key, "identity")
	seedScriptSource(t, ctx, conn, "identity", `
def execute(state, op):
    return {"mutations": [{"op": "update", "key": "`+key+`",
                           "document": {"class": "identity", "isDeleted": False,
                                        "data": {"name": "Andrew"}}}], "events": []}
`)
	logger := testLogger()
	cache := NewDDLCache(conn, testCoreBucket, logger)
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("ddl cache refresh: %v", err)
	}
	racer := &racingPriorCommitter{
		inner: NewCommitter(conn, testCoreBucket, cache, logger, time.Now, nil),
		bump: func() {
			seedAspect(t, ctx, conn, key, "identity")
		},
	}
	cp, cons := newInjectedPipeline(t, ctx, conn, racer, nil, "occ-reread")

	env := newTestEnvelope(testNanoID1)
	publishEnvelope(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeAccepted)

	if got := racer.reads.Load(); got != 2 {
		t.Fatalf("ReadPrior calls = %d, want 2 (the retry must re-read)", got)
	}
	if got := racer.commits.Load(); got != 2 {
		t.Fatalf("Commit calls = %d, want 2 (attempt one conflicts, attempt two commits)", got)
	}
	if doc := readStoredDocConn(t, ctx, conn, key); doc["class"] != "identity" {
		t.Fatalf("committed class = %v", doc["class"])
	}
}

// racingPriorCommitter is the real committer with one concurrent writer wedged
// into the window the prior pass opens: after the FIRST read it moves the key,
// so that attempt's batch is conditioned on a revision the world has left.
type racingPriorCommitter struct {
	inner   *CommitterImpl
	bump    func()
	reads   atomic.Uint64
	commits atomic.Uint64
}

func (r *racingPriorCommitter) ReadPrior(ctx context.Context, mutations []MutationOp) (PriorDocs, error) {
	prior, err := r.inner.ReadPrior(ctx, mutations)
	if err == nil && r.reads.Add(1) == 1 {
		r.bump()
	}
	return prior, err
}

func (r *racingPriorCommitter) Commit(ctx context.Context, env *OperationEnvelope, result ScriptResult, tracker Tracker, prior PriorDocs) (CommitAck, error) {
	r.commits.Add(1)
	return r.inner.Commit(ctx, env, result, tracker, prior)
}
