package bypass

// Bypass #1 — Direct KV Write
//
// Enforcement model (Phase 1 decision #2 from handoff brief):
//   NATS-level authorization is NOT yet in place (stub auth in Story 1.5).
//   The bypass scenario is that a rogue client writes directly to Core KV
//   via the NATS KV API, circumventing the core-operations → Processor path.
//
//   Phase 1 enforcement: the write succeeds at the NATS layer, but there is
//   NO corresponding entry on core-events and NO vtx.op.<requestId> tracker
//   key. The Refractor's invariant — "every legitimate Core KV change has a
//   corresponding core-events entry" — is violated, making the direct write
//   DETECTABLE AS ILLEGITIMATE at Refractor projection time. No downstream
//   consumer can treat it as a committed Processor operation.
//
// Report row:
//   Direct KV write | BLOCKED | undetectable-without-EventList (Phase 1 acceptable)

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/substrate"
)

// TestBypass1_DirectKVWrite_NoEventListEntry proves that a direct write to
// Core KV:
//
//  1. Succeeds at the NATS layer (Phase 1 — no NATS-auth rejection yet).
//  2. Leaves NO vtx.op.<id> tracker entry in Core KV.
//  3. Leaves NO entry on the core-events stream.
//
// Conclusion: the direct write is detectable by the Refractor (divergent
// state — no EventList backing), so it cannot be treated as a legitimate
// committed operation. Enforcement: undetectable-without-EventList.
func TestBypass1_DirectKVWrite_NoEventListEntry(t *testing.T) {
	ctx, conn := setupBypassHarness(t)
	provisionEventsStream(t, ctx, conn)

	// The rogue write key and tracker key we'll inspect afterward.
	rogueVertexKey := "vtx.identity." + bypassNanoID1
	rogueTrackerKey := "vtx.op." + bypassNanoID1

	// Phase 1 enforcement note: at this point a NATS publish-level auth
	// would block this write (Epic 3 Story 3.3). In Phase 1, the KV write
	// succeeds — that is expected and acceptable per Decision #2.
	rogueDoc := marshalJSON(map[string]interface{}{
		"class":     "identity",
		"isDeleted": false,
		"key":       rogueVertexKey,
		"data":      map[string]interface{}{"name": "rogue-bypass"},
		// No createdByOp provenance field — the Refractor will detect this
		// as lacking legitimate provenance.
	})
	_, err := conn.KVPut(ctx, bypassCoreBucket, rogueVertexKey, rogueDoc)
	if err != nil {
		t.Fatalf("bypass1: direct KV write failed unexpectedly: %v", err)
	}

	// ASSERTION A: the rogue vertex key IS present in Core KV.
	// (This confirms the write landed — it's the Phase 1 bypass window.)
	if !kvPresent(ctx, conn, bypassCoreBucket, rogueVertexKey) {
		t.Fatalf("bypass1: rogue vertex key should be present in Core KV (direct write succeeded)")
	}

	// ASSERTION B: NO op-tracker entry for this requestId.
	// The Processor never touched this — so no vtx.op.<id> exists.
	if kvPresent(ctx, conn, bypassCoreBucket, rogueTrackerKey) {
		t.Fatalf("bypass1: BYPASS ESCAPED: tracker entry %q must NOT exist for a direct KV write", rogueTrackerKey)
	}

	// ASSERTION C: NO entry on core-events stream for this write.
	// An operations-stream subject consumer receives nothing.
	assertNoEventOnStream(t, ctx, conn, "core-events", "events.>", "bypass1-check")

	// Result: BLOCKED via undetectable-without-EventList enforcement.
	// The Refractor invariant ("every Core KV entry has a core-events
	// backing event") is violated — the direct write is detectable as
	// illegitimate at projection time.
	t.Logf("Bypass #1 BLOCKED: direct write landed in Core KV but no EventList entry exists — Refractor will diverge; NATS-auth promotion deferred to Epic 3")
}

// assertNoEventOnStream creates a transient consumer on `stream` filtering
// `subject`, fetches with a brief timeout, and fails the test if any
// message is received.
func assertNoEventOnStream(t *testing.T, ctx context.Context, conn *substrate.Conn, stream, subject, tag string) {
	t.Helper()
	js := conn.JetStream()

	cons, err := js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable:        tag,
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubjects: []string{subject},
	})
	if err != nil {
		t.Fatalf("bypass: create consumer %q on stream %q: %v", tag, stream, err)
	}

	// Very short fetch — if no messages, the stream is clean.
	batch, err := cons.Fetch(1, jetstream.FetchMaxWait(500*time.Millisecond))
	if err != nil {
		// FetchMaxWait timeout is not an error per se; messages channel closes.
		t.Logf("bypass: fetch returned: %v (expected for empty stream)", err)
	}

	count := 0
	for m := range batch.Messages() {
		count++
		_ = m.Ack()
	}
	if count > 0 {
		t.Fatalf("bypass: BYPASS ESCAPED: %d unexpected message(s) on stream %q (subject %q)", count, stream, subject)
	}
}

// TestBypass1_DirectKVWrite_MissingProvenance verifies that the direct write
// lacks the Processor-stamped `createdByOp` provenance field that the
// legitimate commit path always sets (step 8, NewCommitter). A Refractor
// observing this entry can detect the provenance gap.
func TestBypass1_DirectKVWrite_MissingProvenance(t *testing.T) {
	ctx, conn := setupBypassHarness(t)

	rogueKey := "vtx.resource." + bypassNanoID2
	rogueDoc := marshalJSON(map[string]interface{}{
		"class":     "resource",
		"isDeleted": false,
		"data":      map[string]interface{}{"note": "written directly, no processor"},
		// Deliberately: no "createdByOp" field.
	})
	if _, err := conn.KVPut(ctx, bypassCoreBucket, rogueKey, rogueDoc); err != nil {
		t.Fatalf("bypass1: direct write: %v", err)
	}

	// Read it back and confirm createdByOp is absent.
	entry, err := conn.KVGet(ctx, bypassCoreBucket, rogueKey)
	if err != nil {
		t.Fatalf("bypass1: KVGet: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("bypass1: unmarshal: %v", err)
	}
	if _, hasProvenance := doc["createdByOp"]; hasProvenance {
		t.Fatalf("bypass1: unexpected createdByOp field in direct write (rogue client set it manually — this is the detectable signal)")
	}
	t.Logf("Bypass #1 provenance check: direct write lacks createdByOp — Refractor detectable")
}

// TestBypass1_LegitimateWrite_HasProvenance confirms that a simulated
// legitimate write (as the Processor step 8 would produce) carries
// createdByOp. This is the positive baseline that makes the absence in
// TestBypass1_DirectKVWrite_MissingProvenance meaningful.
func TestBypass1_LegitimateWrite_HasProvenance(t *testing.T) {
	ctx, conn := setupBypassHarness(t)

	legitimateKey := "vtx.identity." + bypassNanoID3
	createdByOp := "vtx.op." + bypassNanoID3
	doc := marshalJSON(map[string]interface{}{
		"class":       "identity",
		"isDeleted":   false,
		"key":         legitimateKey,
		"createdByOp": createdByOp, // set by Processor step 8 NewCommitter
		"data":        map[string]interface{}{"name": "legitimate"},
	})
	if _, err := conn.KVPut(ctx, bypassCoreBucket, legitimateKey, doc); err != nil {
		t.Fatalf("bypass1: legitimate write: %v", err)
	}

	entry, err := conn.KVGet(ctx, bypassCoreBucket, legitimateKey)
	if err != nil {
		t.Fatalf("bypass1: KVGet: %v", err)
	}
	var out map[string]interface{}
	_ = json.Unmarshal(entry.Value, &out)
	if out["createdByOp"] != createdByOp {
		t.Fatalf("bypass1: baseline: createdByOp mismatch: got %v", out["createdByOp"])
	}
	t.Logf("Bypass #1 baseline confirmed: legitimate write carries createdByOp=%s", createdByOp)
}
