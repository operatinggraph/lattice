package weaver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// newStateTestStore starts an embedded NATS server with a TTL-capable
// weaver-state bucket and returns a markStore against it.
func newStateTestStore(t *testing.T, ctx context.Context) *markStore {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	srv := natstest.RunServer(opts)
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	if err != nil {
		t.Fatalf("substrate wrap: %v", err)
	}
	js := conn.JetStream()
	if _, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "weaver-state", LimitMarkerTTL: time.Second}); err != nil {
		t.Fatalf("create weaver-state: %v", err)
	}
	return newMarkStore(conn, "weaver-state", time.Minute, "unit-"+testNanoID(t))
}

// TestMarkClaimID_MintedThenPreserved proves the §10.3 per-open-episode token
// lifecycle the stable userTask identity rests on: the CAS-create mints a fresh
// claimId; a reclaim-replace PRESERVES it verbatim (only the lease refreshes); a
// re-create after a delete (a close→reopen) mints a NEW one.
func TestMarkClaimID_MintedThenPreserved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := newStateTestStore(t, ctx)
	const tID, eID, gap = "t1", "entityAAAAAAAAAAAAAA", "missing_onboarding"
	eKey := "vtx.leaseapp." + eID

	rev, claim1, exists, err := m.create(ctx, tID, eID, gap, eKey, "triggerLoom")
	if err != nil || exists {
		t.Fatalf("create: err=%v exists=%v", err, exists)
	}
	if !substrate.IsValidNanoID(claim1) {
		t.Fatalf("create must mint a NanoID claimId, got %q", claim1)
	}

	// Reclaim-replace preserves the claimId verbatim.
	if _, conflict, err := m.replace(ctx, tID, eID, gap, eKey, "triggerLoom", claim1, rev, markTTLBackstopFactor*m.lease); err != nil || conflict {
		t.Fatalf("replace: err=%v conflict=%v", err, conflict)
	}
	rec, _, found, err := m.get(ctx, tID, eID, gap)
	if err != nil || !found {
		t.Fatalf("get after replace: err=%v found=%v", err, found)
	}
	if rec.ClaimID != claim1 {
		t.Fatalf("reclaim must PRESERVE claimId: got %q want %q", rec.ClaimID, claim1)
	}

	// Close→reopen: delete the mark, re-create — a fresh claimId.
	if err := m.delete(ctx, tID, eID, gap); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, claim2, _, err := m.create(ctx, tID, eID, gap, eKey, "triggerLoom")
	if err != nil {
		t.Fatalf("re-create: %v", err)
	}
	if claim2 == claim1 {
		t.Fatalf("a reopened episode must mint a NEW claimId, got the same %q", claim2)
	}
}

// TestSetDisabled_RoundTrip verifies setDisabled(true)/isDisabled and
// setDisabled(false)/isDisabled round-trip (AC #3).
func TestSetDisabled_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := newStateTestStore(t, ctx)

	disabled, err := m.isDisabled(ctx, "t1")
	if err != nil {
		t.Fatalf("isDisabled (initial): %v", err)
	}
	if disabled {
		t.Fatalf("isDisabled (initial) = true, want false")
	}

	if err := m.setDisabled(ctx, "t1", true); err != nil {
		t.Fatalf("setDisabled(true): %v", err)
	}
	disabled, err = m.isDisabled(ctx, "t1")
	if err != nil {
		t.Fatalf("isDisabled (after disable): %v", err)
	}
	if !disabled {
		t.Fatalf("isDisabled (after disable) = false, want true")
	}

	if err := m.setDisabled(ctx, "t1", false); err != nil {
		t.Fatalf("setDisabled(false): %v", err)
	}
	disabled, err = m.isDisabled(ctx, "t1")
	if err != nil {
		t.Fatalf("isDisabled (after enable): %v", err)
	}
	if disabled {
		t.Fatalf("isDisabled (after enable) = true, want false")
	}
}

// TestSetDisabled_IdempotentClear verifies that setDisabled(false) on a
// target that was never disabled is a no-op success (missing-key-is-success,
// mirroring delete's posture).
func TestSetDisabled_IdempotentClear(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := newStateTestStore(t, ctx)

	if err := m.setDisabled(ctx, "never-disabled", false); err != nil {
		t.Fatalf("setDisabled(false) on never-disabled target: %v", err)
	}
	if err := m.setDisabled(ctx, "never-disabled", false); err != nil {
		t.Fatalf("setDisabled(false) twice: %v", err)
	}
	disabled, err := m.isDisabled(ctx, "never-disabled")
	if err != nil {
		t.Fatalf("isDisabled: %v", err)
	}
	if disabled {
		t.Fatalf("isDisabled = true, want false")
	}
}

// TestControlKey_NoCollisionWithMark verifies the reserved-key shape:
// controlKey(targetID) has exactly ONE dot after targetID's segment (a
// single "__control" tail), while a real mark key markKey(targetID, entityID,
// gapColumn) always has TWO dots — so the two key shapes can never collide,
// regardless of entityID/gapColumn values.
func TestControlKey_NoCollisionWithMark(t *testing.T) {
	t.Parallel()
	ck := controlKey("t1")
	if got, want := strings.Count(ck, "."), 1; got != want {
		t.Fatalf("controlKey(%q) = %q has %d dots, want %d", "t1", ck, got, want)
	}
	if !strings.HasSuffix(ck, controlKeySuffix) {
		t.Fatalf("controlKey(%q) = %q does not have suffix %q", "t1", ck, controlKeySuffix)
	}

	mk := markKey("t1", "someEntityID12345678", "missing_foo")
	if got, want := strings.Count(mk, "."), 2; got != want {
		t.Fatalf("markKey(...) = %q has %d dots, want %d", mk, got, want)
	}
	if mk == ck {
		t.Fatalf("markKey and controlKey collided: %q", mk)
	}
}

// TestControlKeySuffix_NotProducibleByNanoID verifies the structural
// safety claim underpinning AC #3's reserved-key shape: "__control" can
// never be produced by substrate.NewNanoID(), because substrate.Alphabet
// contains no underscore. If this ever changes (alphabet gains "_"), this
// test fails loudly — a structural finding to escalate, per Task 1's note,
// though entityIDs are sourced from the projecting Lens, not NewNanoID(),
// so a colliding entityID would itself be a pathological Lens bug
// independent of this story.
func TestControlKeySuffix_NotProducibleByNanoID(t *testing.T) {
	t.Parallel()
	if strings.Contains(substrate.Alphabet, "_") {
		t.Fatalf("substrate.Alphabet contains '_' — __control marker keys may now collide with NanoID-derived entityIDs; escalate as a structural finding")
	}
	if !strings.Contains(controlKeySuffix, "_") {
		t.Fatalf("controlKeySuffix %q does not contain '_' — reserved-shape assumption invalid", controlKeySuffix)
	}
}

// TestDeleteByTargetPrefix_OnlyMatchesOwnTarget verifies that
// deleteByTargetPrefix(ctx, "t1") deletes only keys with prefix "t1." and
// does NOT delete keys belonging to "t10" — proving "t1." is never a prefix
// match for "t10." (the trailing "." in the prefix makes this safe by
// construction; this test confirms it).
func TestDeleteByTargetPrefix_OnlyMatchesOwnTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := newStateTestStore(t, ctx)

	// t1's marks.
	if _, _, _, err := m.create(ctx, "t1", "entityAAAAAAAAAAAAAA", "missing_a", "vtx.entity.entityAAAAAAAAAAAAAA", "MarkExpired"); err != nil {
		t.Fatalf("create t1 mark: %v", err)
	}
	if err := m.setDisabled(ctx, "t1", true); err != nil {
		t.Fatalf("setDisabled t1: %v", err)
	}

	// t10's marks — must survive deleteByTargetPrefix(ctx, "t1").
	if _, _, _, err := m.create(ctx, "t10", "entityBBBBBBBBBBBBBB", "missing_b", "vtx.entity.entityBBBBBBBBBBBBBB", "MarkExpired"); err != nil {
		t.Fatalf("create t10 mark: %v", err)
	}
	if err := m.setDisabled(ctx, "t10", true); err != nil {
		t.Fatalf("setDisabled t10: %v", err)
	}

	deleted, err := m.deleteByTargetPrefix(ctx, "t1")
	if err != nil {
		t.Fatalf("deleteByTargetPrefix(t1): %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleteByTargetPrefix(t1) deleted %d keys, want 2 (1 mark + 1 __control)", deleted)
	}

	// t1's mark and control marker are gone.
	if _, _, found, err := m.get(ctx, "t1", "entityAAAAAAAAAAAAAA", "missing_a"); err != nil {
		t.Fatalf("get t1 mark: %v", err)
	} else if found {
		t.Fatalf("t1 mark still present after deleteByTargetPrefix(t1)")
	}
	if disabled, err := m.isDisabled(ctx, "t1"); err != nil {
		t.Fatalf("isDisabled t1: %v", err)
	} else if disabled {
		t.Fatalf("t1 __control marker still present after deleteByTargetPrefix(t1)")
	}

	// t10's mark and control marker survive untouched.
	if _, _, found, err := m.get(ctx, "t10", "entityBBBBBBBBBBBBBB", "missing_b"); err != nil {
		t.Fatalf("get t10 mark: %v", err)
	} else if !found {
		t.Fatalf("t10 mark deleted by deleteByTargetPrefix(t1) — prefix overlap bug")
	}
	if disabled, err := m.isDisabled(ctx, "t10"); err != nil {
		t.Fatalf("isDisabled t10: %v", err)
	} else if !disabled {
		t.Fatalf("t10 __control marker deleted by deleteByTargetPrefix(t1) — prefix overlap bug")
	}
}

// TestDeleteByTargetPrefix_NoKeys verifies deleteByTargetPrefix on a target
// with no weaver-state keys returns (0, nil) — not an error.
func TestDeleteByTargetPrefix_NoKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := newStateTestStore(t, ctx)

	deleted, err := m.deleteByTargetPrefix(ctx, "ghost-target")
	if err != nil {
		t.Fatalf("deleteByTargetPrefix(ghost-target): %v", err)
	}
	if deleted != 0 {
		t.Fatalf("deleteByTargetPrefix(ghost-target) deleted %d keys, want 0", deleted)
	}
}

// TestDispatchCount_RoundTrip verifies the §E dispatch-count store: an absent key
// reads 0; increment creates at 1 and then monotonically advances; delete (the
// gap-close reset) drops it back to 0 and a later increment restarts at 1.
func TestDispatchCount_RoundTrip(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := context.Background()
	m := newStateTestStore(t, ctx)

	const targetID, gap = "t1", "missing_x"
	entityID := "entityAAAAAAAAAAAAAA"

	if got, err := m.getDispatchCount(ctx, targetID, entityID, gap); err != nil || got != 0 {
		t.Fatalf("absent dispatch-count = %d (err=%v), want 0", got, err)
	}
	for want := 1; want <= 4; want++ {
		got, err := m.incrementDispatchCount(ctx, targetID, entityID, gap)
		if err != nil || got != want {
			t.Fatalf("increment #%d = %d (err=%v), want %d", want, got, err, want)
		}
		if read, err := m.getDispatchCount(ctx, targetID, entityID, gap); err != nil || read != want {
			t.Fatalf("getDispatchCount after increment #%d = %d (err=%v), want %d", want, read, err, want)
		}
	}
	if err := m.deleteDispatchCount(ctx, targetID, entityID, gap); err != nil {
		t.Fatalf("deleteDispatchCount: %v", err)
	}
	if got, err := m.getDispatchCount(ctx, targetID, entityID, gap); err != nil || got != 0 {
		t.Fatalf("after the reset dispatch-count = %d (err=%v), want 0", got, err)
	}
	if got, err := m.incrementDispatchCount(ctx, targetID, entityID, gap); err != nil || got != 1 {
		t.Fatalf("post-reset increment = %d (err=%v), want 1 (fresh budget)", got, err)
	}
	// The reset is idempotent: deleting an already-absent count is success.
	other := "entityBBBBBBBBBBBBBB"
	if err := m.deleteDispatchCount(ctx, targetID, other, gap); err != nil {
		t.Fatalf("deleteDispatchCount on an absent count must be a no-op success: %v", err)
	}
}

// TestCountKey_Shape verifies the reserved count-key shape is disjoint from the
// mark key (2 dots) and the __control marker (1 dot): a count key has exactly 3
// dots and the __count suffix, and __count can never be a NanoID entityId or a
// gapColumn (no underscore in the alphabet; the dot is forbidden in a gap column),
// so the three weaver-state key families never collide.
func TestCountKey_Shape(t *testing.T) {
	t.Parallel()
	ck := countKey("t1", "someEntityID12345678", "missing_x")
	if got, want := strings.Count(ck, "."), 3; got != want {
		t.Fatalf("countKey(...) = %q has %d dots, want %d", ck, got, want)
	}
	if !strings.HasSuffix(ck, countKeySuffix) {
		t.Fatalf("countKey(...) = %q does not have suffix %q", ck, countKeySuffix)
	}
	mk := markKey("t1", "someEntityID12345678", "missing_x")
	if ck == mk {
		t.Fatalf("countKey and markKey collided: %q", ck)
	}
	if strings.HasSuffix(mk, countKeySuffix) {
		t.Fatalf("a real mark key %q must not carry the count suffix", mk)
	}
	if strings.Contains(substrate.Alphabet, "_") {
		t.Fatalf("substrate.Alphabet contains '_' — __count count keys may now collide with NanoID-derived entityIds; escalate as a structural finding")
	}
}

// TestDispatchCount_TTLBackstop proves the count carries the long per-key TTL
// backstop (dispatchCountTTLBackstopFactor × lease) on the wire — the GC of an
// orphaned count whose gap-close was never observed. The factor is far larger than
// the mark's, so the count never expires mid-chain.
func TestDispatchCount_TTLBackstop(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := context.Background()
	m := newStateTestStore(t, ctx) // lease = time.Minute (see newStateTestStore)

	const targetID, gap = "t1", "missing_x"
	entityID := "entityAAAAAAAAAAAAAA"
	if _, err := m.incrementDispatchCount(ctx, targetID, entityID, gap); err != nil {
		t.Fatalf("increment dispatch-count: %v", err)
	}
	key := countKey(targetID, entityID, gap)
	stream, err := m.conn.JetStream().Stream(ctx, "KV_weaver-state")
	if err != nil {
		t.Fatalf("open weaver-state stream: %v", err)
	}
	raw, err := stream.GetLastMsgForSubject(ctx, "$KV.weaver-state."+key)
	if err != nil {
		t.Fatalf("read raw count message: %v", err)
	}
	wantTTL := (dispatchCountTTLBackstopFactor * time.Minute).String()
	if got := raw.Header.Get("Nats-TTL"); got != wantTTL {
		t.Fatalf("count Nats-TTL header = %q, want %q (dispatchCountTTLBackstopFactor × lease)", got, wantTTL)
	}
}

// TestEffectKey_Shape verifies the reserved `__effect` key shape is disjoint
// from the mark (2 dots), the `__control` marker (1 dot), and the `__count`
// key (3 dots, `__count` suffix): an effect key has exactly 3 dots and the
// `__effect` marker never collides with any of the other three families.
func TestEffectKey_Shape(t *testing.T) {
	t.Parallel()
	ek := effectKey("t1", "missing_x", "directOp")
	if got, want := strings.Count(ek, "."), 3; got != want {
		t.Fatalf("effectKey(...) = %q has %d dots, want %d", ek, got, want)
	}
	if !strings.Contains(ek, effectKeyMarker) {
		t.Fatalf("effectKey(...) = %q does not contain marker %q", ek, effectKeyMarker)
	}
	mk := markKey("t1", "someEntityID12345678", "missing_x")
	ck := countKey("t1", "someEntityID12345678", "missing_x")
	if ek == mk || ek == ck {
		t.Fatalf("effectKey collided with markKey/countKey: %q", ek)
	}
	if strings.HasSuffix(mk, countKeySuffix) || strings.Contains(mk, effectKeyMarker) {
		t.Fatalf("a real mark key %q must not carry the count suffix or the effect marker", mk)
	}
	targetID, gapColumn, actionRef, ok := splitEffectKey(ek)
	if !ok || targetID != "t1" || gapColumn != "missing_x" || actionRef != "directOp" {
		t.Fatalf("splitEffectKey(%q) = (%q,%q,%q,%v), want (t1,missing_x,directOp,true)",
			ek, targetID, gapColumn, actionRef, ok)
	}
	if _, _, _, ok := splitEffectKey(mk); ok {
		t.Fatalf("splitEffectKey must reject a real mark key %q", mk)
	}
}

// TestEffectDispatchClose_RoundTrip proves the §10.3 `__effect` confidence
// window: dispatch appends a pending (false) slot; close flips the OLDEST
// still-pending slot to true (FIFO, not per-entity paired); a close with no
// pending slot (nothing dispatched yet, or every slot already closed) is a
// no-op; the window survives a fresh markStore instance against the same
// bucket (durable — proves "counters survive restart").
func TestEffectDispatchClose_RoundTrip(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := context.Background()
	m := newStateTestStore(t, ctx)
	const targetID, gap, action = "t1", "missing_x", "directOp"

	// A close before any dispatch is a no-op success (nothing pending).
	if err := m.recordEffectClose(ctx, targetID, gap, action); err != nil {
		t.Fatalf("recordEffectClose on an absent window must be a no-op: %v", err)
	}
	mismatches, err := m.scanEffectMismatches(ctx)
	if err != nil || len(mismatches) != 0 {
		t.Fatalf("scanEffectMismatches after a no-op close = %v (err=%v), want none", mismatches, err)
	}

	for i := 0; i < 3; i++ {
		if err := m.recordEffectDispatch(ctx, targetID, gap, action); err != nil {
			t.Fatalf("recordEffectDispatch #%d: %v", i, err)
		}
	}
	rec, _, ok, err := readEffectStats(ctx, m, targetID, gap, action)
	if err != nil || !ok {
		t.Fatalf("read effect stats: err=%v ok=%v", err, ok)
	}
	if len(rec.Window) != 3 || rec.Window[0] || rec.Window[1] || rec.Window[2] {
		t.Fatalf("window after 3 dispatches = %v, want [false false false]", rec.Window)
	}

	// Close flips the OLDEST pending slot (index 0), not the most recent.
	if err := m.recordEffectClose(ctx, targetID, gap, action); err != nil {
		t.Fatalf("recordEffectClose: %v", err)
	}
	rec, _, ok, err = readEffectStats(ctx, m, targetID, gap, action)
	if err != nil || !ok {
		t.Fatalf("read effect stats after close: err=%v ok=%v", err, ok)
	}
	if !rec.Window[0] || rec.Window[1] || rec.Window[2] {
		t.Fatalf("window after 1 close = %v, want [true false false] (oldest-pending flip)", rec.Window)
	}

	// Simulate a restart: a fresh markStore against the SAME durable bucket
	// must read back the identical window — the state lives in weaver-state,
	// not in-process.
	restarted := newMarkStore(m.conn, m.bucket, m.lease, "unit-restarted-"+testNanoID(t))
	rec2, _, ok, err := readEffectStats(ctx, restarted, targetID, gap, action)
	if err != nil || !ok {
		t.Fatalf("read effect stats after simulated restart: err=%v ok=%v", err, ok)
	}
	if len(rec2.Window) != 3 || !rec2.Window[0] || rec2.Window[1] || rec2.Window[2] {
		t.Fatalf("window after simulated restart = %v, want [true false false]", rec2.Window)
	}
}

// TestEffectDispatch_RingEviction proves the sliding window is a FIFO ring
// capped at effectWindowSize: dispatching past the cap evicts the OLDEST
// entry (whatever its outcome), never grows unbounded.
func TestEffectDispatch_RingEviction(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := context.Background()
	m := newStateTestStore(t, ctx)
	const targetID, gap, action = "t1", "missing_x", "directOp"

	for i := 0; i < effectWindowSize+5; i++ {
		if err := m.recordEffectDispatch(ctx, targetID, gap, action); err != nil {
			t.Fatalf("recordEffectDispatch #%d: %v", i, err)
		}
	}
	rec, _, ok, err := readEffectStats(ctx, m, targetID, gap, action)
	if err != nil || !ok {
		t.Fatalf("read effect stats: err=%v ok=%v", err, ok)
	}
	if len(rec.Window) != effectWindowSize {
		t.Fatalf("window len = %d after %d dispatches, want capped at %d", len(rec.Window), effectWindowSize+5, effectWindowSize)
	}
}

// TestScanEffectMismatches_FullWindowZeroCloses proves the heartbeat-cadence
// scan raises a mismatch only once the window is FULL (effectWindowSize
// dispatches) AND carries zero closes — a not-yet-full window (a normal
// in-progress chain) never alerts, and a single close anywhere in a full
// window clears it.
func TestScanEffectMismatches_FullWindowZeroCloses(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := context.Background()
	m := newStateTestStore(t, ctx)
	const targetID, gap, action = "t1", "missing_x", "directOp"

	for i := 0; i < effectWindowSize-1; i++ {
		if err := m.recordEffectDispatch(ctx, targetID, gap, action); err != nil {
			t.Fatalf("recordEffectDispatch #%d: %v", i, err)
		}
	}
	mismatches, err := m.scanEffectMismatches(ctx)
	if err != nil || len(mismatches) != 0 {
		t.Fatalf("scanEffectMismatches on a not-yet-full window = %v (err=%v), want none", mismatches, err)
	}

	// The window-filling dispatch, still zero closes: now it must alert.
	if err := m.recordEffectDispatch(ctx, targetID, gap, action); err != nil {
		t.Fatalf("recordEffectDispatch (window-filling): %v", err)
	}
	mismatches, err = m.scanEffectMismatches(ctx)
	if err != nil || len(mismatches) != 1 {
		t.Fatalf("scanEffectMismatches on a full zero-close window = %v (err=%v), want exactly 1", mismatches, err)
	}
	if mismatches[0].TargetID != targetID || mismatches[0].GapColumn != gap || mismatches[0].ActionRef != action {
		t.Fatalf("mismatch = %+v, want target=%s gap=%s action=%s", mismatches[0], targetID, gap, action)
	}

	// One close clears the mismatch.
	if err := m.recordEffectClose(ctx, targetID, gap, action); err != nil {
		t.Fatalf("recordEffectClose: %v", err)
	}
	mismatches, err = m.scanEffectMismatches(ctx)
	if err != nil || len(mismatches) != 0 {
		t.Fatalf("scanEffectMismatches after one close in a full window = %v (err=%v), want none", mismatches, err)
	}
}

// readEffectStats reads back the effectStats value for a (target, gap,
// action) triple, unmarshalled — a test-only accessor mirroring markStore.get.
func readEffectStats(ctx context.Context, m *markStore, targetID, gap, action string) (effectStats, uint64, bool, error) {
	entry, err := m.conn.KVGet(ctx, m.bucket, effectKey(targetID, gap, action))
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return effectStats{}, 0, false, nil
		}
		return effectStats{}, 0, false, err
	}
	var stats effectStats
	if uErr := json.Unmarshal(entry.Value, &stats); uErr != nil {
		return effectStats{}, 0, false, uErr
	}
	return stats, entry.Revision, true, nil
}
