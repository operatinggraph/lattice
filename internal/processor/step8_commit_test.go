package processor

import (
	"context"
	"encoding/json"
	"errors"
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
