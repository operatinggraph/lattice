package objectmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// Retry + malformed-input paths of the owner-tombstone-cascade. Error injection
// goes through the real substrate: a missing KV bucket (enumeration error), a
// closed connection (connection-classified error), a raw-published KV-stream
// subject whose key fails the NATS KV charset (link-read error), and an absent
// ops stream (publish error). The two NakWithDelay sub-branches are told apart
// by the branch-specific warn log — the IsConnectionError branch is silent.

const (
	oidB = "objBhjkmnpqrstuvwxyz"
	oidC = "objChjkmnpqrstuvwxyz"
)

// loggedCascadeManager is cascadeManager with the manager's log captured so a
// test can assert WHICH error branch produced a decision. createOps controls
// whether the ops.> stream exists — without it every DetachObject publish fails
// (no stream covers the subject).
func loggedCascadeManager(t *testing.T, conn *substrate.Conn, ctx context.Context, logBuf *bytes.Buffer, createOps bool) *Manager {
	t.Helper()
	if createOps {
		if _, err := conn.JetStream().CreateOrUpdateStream(ctx, jetstream.StreamConfig{
			Name: "ops", Subjects: []string{"ops.>"},
		}); err != nil {
			t.Fatalf("create ops stream: %v", err)
		}
	}
	return New(Config{
		Conn:          conn,
		CoreKVBucket:  "core-kv",
		ObjectsBucket: "core-objects",
		EventsStream:  "core-events",
		ActorKey:      testActor,
		OpLane:        "system",
		Logger:        slog.New(slog.NewTextHandler(logBuf, nil)),
	})
}

// The link enumeration fails structurally (the core-kv bucket is absent) ⇒
// warn + NakWithDelay — retry, never guess.
func TestCascadeDetach_ListErrorNaks(t *testing.T) {
	conn, ctx := testConn(t)
	var logBuf bytes.Buffer
	m := New(Config{
		Conn:          conn,
		CoreKVBucket:  "cascade-absent-kv", // never provisioned
		ObjectsBucket: "core-objects",
		EventsStream:  "core-events",
		ActorKey:      testActor,
		OpLane:        "system",
		Logger:        slog.New(slog.NewTextHandler(&logBuf, nil)),
	})
	body, _ := json.Marshal(map[string]any{"key": ownerX, "isDeleted": true})
	msg := substrate.Message{Subject: "$KV.cascade-absent-kv." + ownerX, Body: body, Sequence: 3}

	if got := m.handleVertexUpdate(ctx, msg); got != substrate.NakWithDelay {
		t.Fatalf("decision = %v want NakWithDelay (link enumeration failed)", got)
	}
	if !strings.Contains(logBuf.String(), "cascade list object links failed") {
		t.Errorf("expected the list-error warn (the non-connection branch), log:\n%s", logBuf.String())
	}
}

// The link enumeration fails with a CONNECTION error (closed conn) ⇒
// NakWithDelay via the silent IsConnectionError branch — no warn logged.
func TestCascadeDetach_ListConnectionErrorNaksSilently(t *testing.T) {
	conn, ctx := testConn(t)
	var logBuf bytes.Buffer
	m := loggedCascadeManager(t, conn, ctx, &logBuf, false)
	// Warm the core-kv handle so the failure lands on the enumeration itself,
	// not the bucket open.
	seedVertex(t, ctx, conn, "vtx.object."+oidB, false, nil)
	conn.Close()

	if got := m.handleVertexUpdate(ctx, ownerTombstoneMsg(ownerX, 4)); got != substrate.NakWithDelay {
		t.Fatalf("decision = %v want NakWithDelay (connection lost mid-cascade)", got)
	}
	if s := logBuf.String(); strings.Contains(s, "cascade list object links failed") {
		t.Errorf("a connection error must take the silent IsConnectionError branch, got warn:\n%s", s)
	}
}

// The authoritative link read fails: the lister returns a key raw-published to
// the KV stream whose charset KVGet rejects ('~' is subject-legal but
// KV-key-illegal) ⇒ warn + NakWithDelay, and no DetachObject is submitted.
func TestCascadeDetach_LinkReadErrorNaks(t *testing.T) {
	conn, ctx := testConn(t)
	var logBuf bytes.Buffer
	m := loggedCascadeManager(t, conn, ctx, &logBuf, true)
	badLink := linkKeyFor(oidB, "att~ch", ownerX)
	body, _ := json.Marshal(map[string]any{"key": badLink, "isDeleted": false})
	if err := conn.Publish(ctx, "$KV.core-kv."+badLink, body, nil); err != nil {
		t.Fatalf("raw-publish bad link key: %v", err)
	}

	if got := m.handleVertexUpdate(ctx, ownerTombstoneMsg(ownerX, 21)); got != substrate.NakWithDelay {
		t.Fatalf("decision = %v want NakWithDelay (authoritative link read failed)", got)
	}
	if !strings.Contains(logBuf.String(), "cascade read link failed") {
		t.Errorf("expected the link-read warn, log:\n%s", logBuf.String())
	}
	if ops := drainOps(t, ctx, conn); len(ops) != 0 {
		t.Fatalf("no DetachObject may be submitted when the link read fails, got %d", len(ops))
	}
}

// The DetachObject publish fails (no stream covers ops.system) ⇒ warn +
// NakWithDelay — the tombstone redelivers and the deterministic requestIds make
// the retry collapse-safe.
func TestCascadeDetach_SubmitErrorNaks(t *testing.T) {
	conn, ctx := testConn(t)
	var logBuf bytes.Buffer
	m := loggedCascadeManager(t, conn, ctx, &logBuf, false) // no ops stream
	objKey := "vtx.object." + oidB
	lk := linkKeyFor(oidB, "photoOf", ownerX)
	seedVertex(t, ctx, conn, objKey, false, map[string]any{"liveLinks": 1})
	seedLink(t, ctx, conn, lk, objKey, ownerX, "photoOf", false)

	if got := m.handleVertexUpdate(ctx, ownerTombstoneMsg(ownerX, 33)); got != substrate.NakWithDelay {
		t.Fatalf("decision = %v want NakWithDelay (DetachObject publish failed)", got)
	}
	if !strings.Contains(logBuf.String(), "cascade submit DetachObject failed") {
		t.Errorf("expected the submit-error warn, log:\n%s", logBuf.String())
	}
}

// submitDetach surfaces the publish error to its caller, and the SAME call
// succeeds once the ops stream exists — the positive sibling proving the
// failure was the publish, not the inputs.
func TestSubmitDetach_PublishErrorSurfaces(t *testing.T) {
	conn, ctx := testConn(t)
	m := New(Config{
		Conn:          conn,
		CoreKVBucket:  "core-kv",
		ObjectsBucket: "core-objects",
		EventsStream:  "core-events",
		ActorKey:      testActor,
		OpLane:        "system",
	})
	lk := linkKeyFor(oidB, "photoOf", ownerX)
	objKey := "vtx.object." + oidB

	if err := m.submitDetach(ctx, oidB, ownerX, "photoOf", lk, objKey, 5); err == nil {
		t.Fatalf("submitDetach must return the publish error when no stream covers ops.system")
	}

	if _, err := conn.JetStream().CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: "ops", Subjects: []string{"ops.>"},
	}); err != nil {
		t.Fatalf("create ops stream: %v", err)
	}
	if err := m.submitDetach(ctx, oidB, ownerX, "photoOf", lk, objKey, 5); err != nil {
		t.Fatalf("submitDetach with the ops stream present: %v", err)
	}
	ops := drainOps(t, ctx, conn)
	if len(ops) != 1 || ops[0].OperationType != "DetachObject" {
		t.Fatalf("expected the succeeding call to land 1 DetachObject, got %+v", ops)
	}
}

// A listed lnk.object.* key parseObjectLinkKey rejects (7 segments) is skipped
// and the cascade continues to the well-formed link: exactly one DetachObject,
// Ack — a malformed neighbor never wedges the owner's cascade.
func TestCascade_SkipsUnparseableListedKey(t *testing.T) {
	conn, ctx := testConn(t)
	m := cascadeManager(t, conn, ctx)
	_, xid, ok := splitVertexRoot(ownerX)
	if !ok {
		t.Fatalf("splitVertexRoot(%q) rejected a valid root", ownerX)
	}
	objKey := "vtx.object." + oidC
	good := linkKeyFor(oidC, "photoOf", ownerX)
	sevenSeg := "lnk.object." + oidC + ".extra.photoOf.identity." + xid
	seedVertex(t, ctx, conn, objKey, false, map[string]any{"liveLinks": 1})
	seedLink(t, ctx, conn, good, objKey, ownerX, "photoOf", false)
	seedLink(t, ctx, conn, sevenSeg, objKey, ownerX, "photoOf", false)

	if got := m.handleVertexUpdate(ctx, ownerTombstoneMsg(ownerX, 13)); got != substrate.Ack {
		t.Fatalf("decision = %v want Ack", got)
	}
	ops := drainOps(t, ctx, conn)
	if len(ops) != 1 {
		t.Fatalf("expected 1 DetachObject (the well-formed link only), got %d: %+v", len(ops), ops)
	}
	var p struct{ LinkName string }
	_ = json.Unmarshal(ops[0].Payload, &p)
	if p.LinkName != "photoOf" {
		t.Errorf("linkName = %q want photoOf (from the well-formed link)", p.LinkName)
	}
}

// The consumer Acks (never Naks) a message it cannot classify — a foreign-bucket
// subject, a non-root key, an empty body (KV delete-marker), an unparseable
// root — and submits no ops for any of them.
func TestCascade_UnclassifiableMessagesAcked(t *testing.T) {
	conn, ctx := testConn(t)
	m := cascadeManager(t, conn, ctx)
	tombBody, _ := json.Marshal(map[string]any{"key": ownerX, "isDeleted": true})
	cases := []struct {
		name string
		msg  substrate.Message
	}{
		{"foreign bucket subject", substrate.Message{Subject: "$KV.other-kv." + ownerX, Body: tombBody, Sequence: 1}},
		{"aspect subject (4-segment key)", substrate.Message{Subject: "$KV.core-kv." + ownerX + ".profile", Body: tombBody, Sequence: 2}},
		{"empty body (delete marker)", substrate.Message{Subject: "$KV.core-kv." + ownerX, Sequence: 3}},
		{"unparseable root", substrate.Message{Subject: "$KV.core-kv." + ownerX, Body: []byte("{not json"), Sequence: 4}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := m.handleVertexUpdate(ctx, tc.msg); got != substrate.Ack {
				t.Fatalf("decision = %v want Ack", got)
			}
		})
	}
	if ops := drainOps(t, ctx, conn); len(ops) != 0 {
		t.Fatalf("unclassifiable messages must submit no ops, got %d", len(ops))
	}
}

// splitVertexRoot accepts exactly the 3-segment vtx.<type>.<id> shape and
// rejects aspects, links, empty segments, and truncated keys.
func TestSplitVertexRoot(t *testing.T) {
	t.Parallel()
	wantID := strings.TrimPrefix(ownerX, "vtx.identity.")
	vtype, id, ok := splitVertexRoot(ownerX)
	if !ok || vtype != "identity" || id != wantID {
		t.Fatalf("splitVertexRoot(%q) = (%q, %q, %v), want (identity, %q, true)", ownerX, vtype, id, ok, wantID)
	}

	rejects := []string{
		"",                  // empty
		"vtx.identity",      // 2 segments
		ownerX + ".profile", // 4-segment aspect
		"lnk.photoOf." + wantID, // 3 segments but not vtx
		"vtx.." + wantID,        // empty type segment
		"vtx.identity.",         // empty id segment
	}
	for _, key := range rejects {
		if vt, vid, ok := splitVertexRoot(key); ok {
			t.Errorf("splitVertexRoot(%q) = (%q, %q, true), want reject", key, vt, vid)
		}
	}
}

// parseObjectLinkKey accepts exactly the 6-segment
// lnk.object.<oid>.<linkName>.<tgtType>.<tgtId> whose target equals the owner,
// and rejects an owner mismatch on either segment, non-object links, and wrong
// segment counts.
func TestParseObjectLinkKey(t *testing.T) {
	t.Parallel()
	_, xid, ok := splitVertexRoot(ownerX)
	if !ok {
		t.Fatalf("splitVertexRoot(%q) rejected a valid root", ownerX)
	}
	_, yid, _ := splitVertexRoot(ownerY)
	good := linkKeyFor(oidB, "photoOf", ownerX)

	oid, linkName, ok := parseObjectLinkKey(good, "identity", xid)
	if !ok || oid != oidB || linkName != "photoOf" {
		t.Fatalf("parseObjectLinkKey(%q) = (%q, %q, %v), want (%q, photoOf, true)", good, oid, linkName, ok, oidB)
	}

	rejects := []struct {
		name, key, ownerType, ownerID string
	}{
		{"owner id mismatch", good, "identity", yid},
		{"owner type mismatch", good, "unit", xid},
		{"non-object link", "lnk.document." + oidB + ".photoOf.identity." + xid, "identity", xid},
		{"non-link key", "vtx.object." + oidB + ".photoOf.identity." + xid, "identity", xid},
		{"5 segments", "lnk.object." + oidB + ".photoOf.identity", "identity", xid},
		{"7 segments", "lnk.object." + oidB + ".extra.photoOf.identity." + xid, "identity", xid},
	}
	for _, tc := range rejects {
		t.Run(tc.name, func(t *testing.T) {
			if o, n, ok := parseObjectLinkKey(tc.key, tc.ownerType, tc.ownerID); ok {
				t.Errorf("parseObjectLinkKey(%q, %q, %q) = (%q, %q, true), want reject", tc.key, tc.ownerType, tc.ownerID, o, n)
			}
		})
	}
}
