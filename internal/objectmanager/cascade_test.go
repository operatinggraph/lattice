package objectmanager

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/substrate"
)

const testActor = "vtx.identity.objmgrActor00000000000"

// cascadeManager builds a manager wired for the owner-tombstone-cascade and
// provisions the ops.> stream the submitted DetachObject ops land on.
func cascadeManager(t *testing.T, conn *substrate.Conn, ctx context.Context) *Manager {
	t.Helper()
	js := conn.JetStream()
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: "ops", Subjects: []string{"ops.>"},
	}); err != nil {
		t.Fatalf("create ops stream: %v", err)
	}
	return New(Config{
		Conn:          conn,
		CoreKVBucket:  "core-kv",
		ObjectsBucket: "core-objects",
		EventsStream:  "core-events",
		ActorKey:      testActor,
		OpLane:        "system",
	})
}

func seedLink(t *testing.T, ctx context.Context, conn *substrate.Conn, key, source, target, localName string, isDeleted bool) {
	t.Helper()
	doc := map[string]any{"key": key, "class": localName, "isDeleted": isDeleted,
		"sourceVertex": source, "targetVertex": target, "localName": localName, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, "core-kv", key, b); err != nil {
		t.Fatalf("seed link %s: %v", key, err)
	}
}

// ownerTombstoneMsg builds the KV-stream message the cascade consumer sees when
// an owner vertex is tombstoned: subject $KV.core-kv.<ownerKey>, body the
// tombstoned root doc.
func ownerTombstoneMsg(ownerKey string, seq uint64) substrate.Message {
	body, _ := json.Marshal(map[string]any{"key": ownerKey, "class": "identity", "isDeleted": true, "data": map[string]any{}})
	return substrate.Message{Subject: "$KV.core-kv." + ownerKey, Body: body, Sequence: seq}
}

// drainOps reads every op published to ops.> (best-effort, short fetch).
func drainOps(t *testing.T, ctx context.Context, conn *substrate.Conn) []cascadeOpEnvelope {
	t.Helper()
	js := conn.JetStream()
	cons, err := js.CreateOrUpdateConsumer(ctx, "ops", jetstream.ConsumerConfig{
		FilterSubject: "ops.>", AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		t.Fatalf("ops consumer: %v", err)
	}
	var out []cascadeOpEnvelope
	batch, err := cons.Fetch(64, jetstream.FetchMaxWait(time.Second))
	if err != nil {
		t.Fatalf("fetch ops: %v", err)
	}
	for msg := range batch.Messages() {
		var env cascadeOpEnvelope
		if err := json.Unmarshal(msg.Data(), &env); err != nil {
			t.Fatalf("unmarshal op: %v", err)
		}
		out = append(out, env)
		_ = msg.Ack()
	}
	return out
}

const (
	oidA   = "objAaaaaaaaaaaaaaaaaa"
	ownerX = "vtx.identity.ownerXXXXXXXXXXXXXXX"
	ownerY = "vtx.identity.ownerYYYYYYYYYYYYYYY"
)

func linkKeyFor(oid, linkName, ownerKey string) string {
	vtype, id, _ := splitVertexRoot(ownerKey)
	return "lnk.object." + oid + "." + linkName + "." + vtype + "." + id
}

// (a) A dead owner with a live object→owner link ⇒ exactly one DetachObject is
// submitted with the right oid/targetKey/linkName, reads, authTarget, and actor.
func TestCascade_DetachesDanglingLink(t *testing.T) {
	conn, ctx := testConn(t)
	m := cascadeManager(t, conn, ctx)

	objKey := "vtx.object." + oidA
	lk := linkKeyFor(oidA, "photoOf", ownerX)
	seedVertex(t, ctx, conn, objKey, false, map[string]any{"linkEpoch": 1, "liveLinks": 1})
	seedLink(t, ctx, conn, lk, objKey, ownerX, "photoOf", false)

	if got := m.handleVertexUpdate(ctx, ownerTombstoneMsg(ownerX, 100)); got != substrate.Ack {
		t.Fatalf("decision = %v want Ack", got)
	}
	ops := drainOps(t, ctx, conn)
	if len(ops) != 1 {
		t.Fatalf("expected 1 DetachObject, got %d: %+v", len(ops), ops)
	}
	op := ops[0]
	if op.OperationType != "DetachObject" {
		t.Errorf("operationType = %q want DetachObject", op.OperationType)
	}
	if op.Actor != testActor {
		t.Errorf("actor = %q want %q", op.Actor, testActor)
	}
	var p struct{ Oid, TargetKey, LinkName string }
	_ = json.Unmarshal(op.Payload, &p)
	if p.Oid != oidA || p.TargetKey != ownerX || p.LinkName != "photoOf" {
		t.Errorf("payload = %+v want oid=%s targetKey=%s linkName=photoOf", p, oidA, ownerX)
	}
	if op.ContextHint == nil || len(op.ContextHint.Reads) != 2 || op.ContextHint.Reads[0] != lk || op.ContextHint.Reads[1] != objKey {
		t.Errorf("contextHint.reads = %+v want [%s %s]", op.ContextHint, lk, objKey)
	}
	if op.AuthContext == nil || op.AuthContext.Target != objKey {
		t.Errorf("authContext.target = %+v want %s", op.AuthContext, objKey)
	}
}

// (b) A non-isDeleted root (create/touch) ⇒ no op, Ack.
func TestCascade_IgnoresNonTombstone(t *testing.T) {
	conn, ctx := testConn(t)
	m := cascadeManager(t, conn, ctx)
	objKey := "vtx.object." + oidA
	lk := linkKeyFor(oidA, "photoOf", ownerX)
	seedVertex(t, ctx, conn, objKey, false, map[string]any{"liveLinks": 1})
	seedLink(t, ctx, conn, lk, objKey, ownerX, "photoOf", false)

	body, _ := json.Marshal(map[string]any{"key": ownerX, "isDeleted": false})
	if got := m.handleVertexUpdate(ctx, substrate.Message{Subject: "$KV.core-kv." + ownerX, Body: body, Sequence: 5}); got != substrate.Ack {
		t.Fatalf("decision = %v want Ack", got)
	}
	if ops := drainOps(t, ctx, conn); len(ops) != 0 {
		t.Fatalf("a live (non-tombstone) root must submit no ops, got %d", len(ops))
	}
}

// (c) An already soft-tombstoned link ⇒ skipped (no op).
func TestCascade_SkipsAlreadyDetachedLink(t *testing.T) {
	conn, ctx := testConn(t)
	m := cascadeManager(t, conn, ctx)
	objKey := "vtx.object." + oidA
	lk := linkKeyFor(oidA, "photoOf", ownerX)
	seedVertex(t, ctx, conn, objKey, false, map[string]any{"liveLinks": 0})
	seedLink(t, ctx, conn, lk, objKey, ownerX, "photoOf", true) // already detached

	if got := m.handleVertexUpdate(ctx, ownerTombstoneMsg(ownerX, 7)); got != substrate.Ack {
		t.Fatalf("decision = %v want Ack", got)
	}
	if ops := drainOps(t, ctx, conn); len(ops) != 0 {
		t.Fatalf("an already-detached link must submit no ops, got %d", len(ops))
	}
}

// (d) A tombstoned OBJECT vertex (no inbound object-links naming it as owner) ⇒
// no op, Ack — objects are link sources, never targets.
func TestCascade_ObjectRootIsNoop(t *testing.T) {
	conn, ctx := testConn(t)
	m := cascadeManager(t, conn, ctx)
	// One unrelated live link in the space (its owner is alive).
	seedVertex(t, ctx, conn, "vtx.object."+oidA, true, map[string]any{"liveLinks": 0})
	seedLink(t, ctx, conn, linkKeyFor(oidA, "photoOf", ownerX), "vtx.object."+oidA, ownerX, "photoOf", false)

	if got := m.handleVertexUpdate(ctx, ownerTombstoneMsg("vtx.object."+oidA, 9)); got != substrate.Ack {
		t.Fatalf("decision = %v want Ack", got)
	}
	if ops := drainOps(t, ctx, conn); len(ops) != 0 {
		t.Fatalf("an object-root tombstone (no inbound object links) must submit no ops, got %d", len(ops))
	}
}

// (e) Multi-owner object: linked to X (dead) and Y (alive). Only the X link is
// detached; the Y attachment is untouched.
func TestCascade_MultiOwnerOnlyDeadDetached(t *testing.T) {
	conn, ctx := testConn(t)
	m := cascadeManager(t, conn, ctx)
	objKey := "vtx.object." + oidA
	lkX := linkKeyFor(oidA, "photoOf", ownerX)
	lkY := linkKeyFor(oidA, "photoOf", ownerY)
	seedVertex(t, ctx, conn, objKey, false, map[string]any{"liveLinks": 2})
	seedLink(t, ctx, conn, lkX, objKey, ownerX, "photoOf", false)
	seedLink(t, ctx, conn, lkY, objKey, ownerY, "photoOf", false)

	if got := m.handleVertexUpdate(ctx, ownerTombstoneMsg(ownerX, 11)); got != substrate.Ack {
		t.Fatalf("decision = %v want Ack", got)
	}
	ops := drainOps(t, ctx, conn)
	if len(ops) != 1 {
		t.Fatalf("expected exactly 1 DetachObject (the dead owner's link), got %d: %+v", len(ops), ops)
	}
	var p struct{ TargetKey string }
	_ = json.Unmarshal(ops[0].Payload, &p)
	if p.TargetKey != ownerX {
		t.Errorf("detached targetKey = %q, want the DEAD owner %q", p.TargetKey, ownerX)
	}
}

// (f) Redelivery of the SAME tombstone (same sequence) re-submits the SAME
// deterministic requestId (Contract #4 tracker collapse → no duplicate detach).
func TestCascade_RedeliveryStableRequestID(t *testing.T) {
	conn, ctx := testConn(t)
	m := cascadeManager(t, conn, ctx)
	objKey := "vtx.object." + oidA
	lk := linkKeyFor(oidA, "photoOf", ownerX)
	seedVertex(t, ctx, conn, objKey, false, map[string]any{"liveLinks": 1})
	seedLink(t, ctx, conn, lk, objKey, ownerX, "photoOf", false)

	msg := ownerTombstoneMsg(ownerX, 42)
	_ = m.handleVertexUpdate(ctx, msg)
	_ = m.handleVertexUpdate(ctx, msg) // redelivery, same sequence
	ops := drainOps(t, ctx, conn)
	if len(ops) != 2 {
		t.Fatalf("expected 2 published envelopes (both deliveries), got %d", len(ops))
	}
	if ops[0].RequestID != ops[1].RequestID {
		t.Errorf("redelivery requestIds differ: %q vs %q (must be stable)", ops[0].RequestID, ops[1].RequestID)
	}
	if !substrate.IsValidNanoID(ops[0].RequestID) {
		t.Errorf("requestId %q is not a Contract #1 NanoID", ops[0].RequestID)
	}
}
