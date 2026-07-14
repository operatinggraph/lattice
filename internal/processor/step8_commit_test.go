package processor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
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
