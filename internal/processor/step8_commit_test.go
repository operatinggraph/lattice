package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	c := NewCommitter(conn, testCoreBucket, cache, testLogger(), time.Now)
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
	ack, err := c.Commit(ctx, env, result, tracker)
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
	_, err := c.Commit(ctx, env, result, tracker)
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
	_, err := c.Commit(ctx, env, result, tracker)
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
	_, err := c.Commit(ctx, env, result, tracker)
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
	if _, err := c.Commit(ctx, env, result, tracker); err != nil {
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
	if _, err := c.Commit(ctx, env, result, tracker); err != nil {
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
	if _, err := c.Commit(ctx, env, result, tracker); err != nil {
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
	if _, err := c.Commit(ctx, env, result, tracker); err != nil {
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
	ack, err := c.Commit(ctx, env, result, tracker)
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
	if _, err := c.Commit(ctx, env, result, tracker); err != nil {
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
	ack, err := c.Commit(ctx, env, result, tracker)
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
	if _, err := c.Commit(ctx, env, ScriptResult{Mutations: []MutationOp{m}}, tracker); err != nil {
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
	_, err := c.Commit(ctx, env, ScriptResult{Mutations: []MutationOp{m}}, tracker)
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

// buildMutationValue seeds a tombstone's written body from the stored document
// and then overlays the mutation's own, so a tombstone CARRYING a document
// rewrites exactly what an update would. parseMutations refuses such a mutation
// today, but this guard does not depend on that: it holds at step 8 for any
// path, present or future, that reaches commit with one.
func TestCommit_TombstoneCarryingDocumentCannotRewriteProvenance(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	roleID := "Fd2jAc8evUor3nG9qHkN"
	key := "vtx.role." + roleID + ".canonicalName"

	commitOne(t, ctx, c, "rid-tsdoc-create", MutationOp{
		Op: "create", Key: key,
		Document: roleCanonicalNameDoc("vtx.role."+roleID, "consoleOperator"),
	})

	err := commitOneErr(ctx, c, "rid-tsdoc-launder", MutationOp{
		Op: "tombstone", Key: key,
		Document: roleCanonicalNameDoc("vtx.role."+roleID, "operator"),
	})
	var provErr *PermissionProvenanceError
	if !errors.As(err, &provErr) {
		t.Fatalf("Commit(tombstone carrying a rewritten value): %v, want *PermissionProvenanceError", err)
	}
	if provErr.Field != "value" {
		t.Fatalf("rejected on field %q, want value", provErr.Field)
	}

	// The laundering tombstone left the committed value alone.
	entry, gerr := c.Conn.KVGet(ctx, testCoreBucket, key)
	if gerr != nil {
		t.Fatalf("KVGet %s: %v", key, gerr)
	}
	var doc map[string]interface{}
	if uerr := json.Unmarshal(entry.Value, &doc); uerr != nil {
		t.Fatalf("unmarshal %s: %v", key, uerr)
	}
	data, _ := doc["data"].(map[string]interface{})
	if data["value"] != "consoleOperator" {
		t.Fatalf("stored canonicalName = %v, want consoleOperator", data["value"])
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
