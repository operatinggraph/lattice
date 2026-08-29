package weaver

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// --- T2: the C3 pin ----------------------------------------------------------

// TestCensusC3_NoMessageReadsBeyondSequence is census C3, executable.
//
// Lane 1 reads nothing from a message but its Sequence, which is the row's OCC
// revision. `msg.NumDelivered` has no reader that decides anything, and must
// not: the redelivery signal is per-MESSAGE, while the only question a live-mark
// delivery still has to answer — did THIS gap's op publish reach the Processor —
// is per-GAP, and the republish set answers it. A read added here would compile,
// and would pass every behavioural test on its own while quietly making one
// gap's Nak re-publish every other in-flight gap on the row. So the pin is
// structural: parse the package and assert the selector appears in no
// expression at all.
//
// The vacuity guard matters as much as the assertion. If substrate ever renamed
// the field, a scan for "NumDelivered" would pass by finding nothing, so the
// test first proves the field still exists to be read.
func TestCensusC3_NoMessageReadsBeyondSequence(t *testing.T) {
	t.Parallel()

	msgType := reflect.TypeOf(substrate.Message{})
	if _, ok := msgType.FieldByName("NumDelivered"); !ok {
		t.Fatalf("substrate.Message has no NumDelivered field — this scan would now pass vacuously; " +
			"re-derive census C3 against the field's new name")
	}
	if _, ok := msgType.FieldByName("Sequence"); !ok {
		t.Fatalf("substrate.Message has no Sequence field — the read this census permits is gone")
	}

	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list internal/weaver sources: %v", err)
	}
	fset := token.NewFileSet()
	scanned := 0
	var offenders []string
	for _, path := range sources {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		scanned++
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if ok && sel.Sel != nil && sel.Sel.Name == "NumDelivered" {
				offenders = append(offenders, path+":"+strconv.Itoa(fset.Position(sel.Pos()).Line))
			}
			return true
		})
	}
	if scanned == 0 {
		t.Fatalf("the scan found no non-test sources to read — it would pass vacuously")
	}
	if len(offenders) != 0 {
		t.Fatalf("NumDelivered is read at %v. Lane 1 classifies a delivery by what is OWED against the "+
			"gap (the republish set), never by how many times the message has been delivered — a "+
			"per-message signal cannot tell \"my own publish failed\" from \"a sibling gap Nak'd the "+
			"row\", so it re-publishes every in-flight gap on the row", offenders)
	}
}

// --- T9: the publish-failure retry -------------------------------------------

// republishRowMessage builds the lane-1 CDC message for one row against a sweep
// harness's engine.
func republishRowMessage(t *testing.T, h *sweepHarness, targetID, entityID string, row map[string]any, seq uint64) substrate.Message {
	t.Helper()
	body, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	return substrate.Message{
		Subject:  h.engine.rowSubjectPrefix + targetID + "." + entityID,
		Body:     body,
		Sequence: seq,
	}
}

// breakOpsStream deletes the ops stream so the actuator's publish fails, and
// returns the func that puts it back. A real failure, not a stubbed one: what is
// under test is `fire`'s disposition of a publish error.
//
// It reproduces the AMBIGUOUS failure specifically, which is the case the
// mechanism exists for. The op still reaches the wire — a core subscriber sees
// it — and what is lost is the stream's acknowledgement, so the engine cannot
// know whether the op landed. That is exactly why the recovery must re-publish
// the SAME episode identity rather than compensate by deleting the mark: under
// this ambiguity a compensating delete would let the redelivery mint a second
// episode against an op that had in fact been accepted.
func breakOpsStream(t *testing.T, ctx context.Context, h *sweepHarness) func() {
	t.Helper()
	js := h.conn.JetStream()
	if err := js.DeleteStream(ctx, "core-operations"); err != nil {
		t.Fatalf("delete ops stream: %v", err)
	}
	return func() {
		t.Helper()
		if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
			Name: "core-operations", Subjects: []string{"ops.>"},
		}); err != nil {
			t.Fatalf("restore ops stream: %v", err)
		}
	}
}

// unackedPublishCtx bounds the wait for a stream acknowledgement that will never
// come: with the stream deleted the publish has no responder, so without a
// deadline of its own it would block until the test's own context expired.
func unackedPublishCtx(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	pubCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	t.Cleanup(cancel)
	return pubCtx
}

// TestFire_PublishFailureIsRepublishedOnTheRedeliveryItAsksFor is T9's prompt
// half, and the whole reason the republish set exists.
//
// `fire`'s publish failure returns Nak, which asks for the row back immediately.
// On that redelivery the mark is present with a live lease, so the anti-storm
// drop would swallow the failed publish and leave recovery to the sweep's
// lease-expiry reclaim — up to a whole MarkLease later. The set is what makes
// the immediate redelivery do the work it was asked for, and it re-publishes the
// SAME episode: the mark's preserved claimId derives the same requestId, so a
// publish that was in fact ambiguous (op accepted, ack lost) collapses on the
// Contract #4 tracker instead of minting a second episode.
func TestFire_PublishFailureIsRepublishedOnTheRedeliveryItAsksFor(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureRepublish"
	const col = "missing_x"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{col: {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	row := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID,
		"violating": true,
		col:         true,
	}
	h.putRow(t, ctx, targetID, entityID, row)

	// The dispatch happens with the ops stream gone: the mark is created, the
	// publish fails, and the row is Nak'd for the retry.
	restore := breakOpsStream(t, ctx, h)
	dec := h.engine.handleRow(unackedPublishCtx(t, ctx), republishRowMessage(t, h, targetID, entityID, row, 1))
	if dec != substrate.Nak {
		t.Fatalf("a failed op publish must Nak for an immediate retry, got %v", dec)
	}
	// The un-acknowledged op that reached the wire anyway: this is the ambiguity
	// the re-publish has to be idempotent against, so its identity is captured
	// and compared to the retry's below.
	unacked := h.nextOp(t)
	if !h.engine.republish.owes(targetID, entityID, col) {
		t.Fatalf("the failed publish must be recorded as owed — without it the redelivery cannot tell "+
			"this failure from a sibling gap's Nak; owed = %v", h.engine.republish.owed)
	}
	rec, markRev, found, err := h.engine.marks.get(ctx, targetID, entityID, col)
	if err != nil || !found {
		t.Fatalf("the episode's mark must exist (err=%v found=%v) — the set is not a substitute for it",
			err, found)
	}
	wantRequestID := deriveEpisodeRequestID(targetID, entityID, col, markRev)
	wantClaimID := rec.ClaimID
	if unacked["requestId"] != wantRequestID {
		t.Fatalf("setup: the un-acked op's requestId = %v, want %v", unacked["requestId"], wantRequestID)
	}

	// The substrate recovers, and the redelivery the Nak asked for arrives.
	restore()
	dec = h.engine.handleRow(ctx, republishRowMessage(t, h, targetID, entityID, row, 1))
	if dec != substrate.Ack {
		t.Fatalf("the re-publish must Ack, got %v", dec)
	}
	op := h.nextOp(t)
	if op["requestId"] != wantRequestID {
		t.Fatalf("re-publish requestId = %v, want the SAME episode %v — a fresh identity would be a "+
			"second episode, which is exactly what an ambiguous publish failure must not produce",
			op["requestId"], wantRequestID)
	}
	if op["requestId"] != unacked["requestId"] {
		t.Fatalf("the retry (%v) does not collapse onto the op that may already have landed (%v)",
			op["requestId"], unacked["requestId"])
	}

	// The episode is unchanged: same mark, same claimId, no second dispatch
	// counted against the retry budget.
	after, afterRev, found, err := h.engine.marks.get(ctx, targetID, entityID, col)
	if err != nil || !found {
		t.Fatalf("the re-publish must not disturb the mark (err=%v found=%v)", err, found)
	}
	if afterRev != markRev || after.ClaimID != wantClaimID {
		t.Fatalf("the re-publish minted a new episode: rev %d -> %d, claimId %q -> %q",
			markRev, afterRev, wantClaimID, after.ClaimID)
	}
	if h.engine.republish.owes(targetID, entityID, col) {
		t.Fatalf("a successful publish must retire the obligation, or every later delivery re-publishes")
	}

	// And the obligation is genuinely spent: the next delivery takes the plain
	// anti-storm drop.
	if dec := h.engine.handleRow(ctx, republishRowMessage(t, h, targetID, entityID, row, 1)); dec != substrate.Ack {
		t.Fatalf("the delivery after the repair must Ack, got %v", dec)
	}
	h.requireNoOp(t)
}

// TestFire_PublishFailureFallsBackToReclaimWhenTheSetIsLost is T9's other half:
// what a restart costs. The set is process memory, so a crash between the failed
// publish and its redelivery loses the obligation — the redelivery then takes
// the ordinary anti-storm drop, and the mark's lease expiring into the sweep's
// reclaim is the backstop. Bounded by one MarkLease, never lost.
func TestFire_PublishFailureFallsBackToReclaimWhenTheSetIsLost(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureRepubLost"
	const col = "missing_x"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{col: {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	row := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID,
		"violating": true,
		col:         true,
	}
	h.putRow(t, ctx, targetID, entityID, row)

	restore := breakOpsStream(t, ctx, h)
	if dec := h.engine.handleRow(unackedPublishCtx(t, ctx), republishRowMessage(t, h, targetID, entityID, row, 1)); dec != substrate.Nak {
		t.Fatalf("setup: the failed publish must Nak, got %v", dec)
	}
	restore()
	h.nextOp(t) // the un-acked op that reached the wire regardless

	// The crash: the process comes back with an empty set and a mark it has no
	// memory of owing anything against.
	h.engine.republish = newRepublishSet()

	if dec := h.engine.handleRow(ctx, republishRowMessage(t, h, targetID, entityID, row, 1)); dec != substrate.Ack {
		t.Fatalf("with no obligation recorded the redelivery takes the anti-storm drop, got %v", dec)
	}
	h.requireNoOp(t)

	// The backstop: the mark's lease expires and the sweep reclaims it, firing a
	// fresh episode for the same gap.
	key := markKey(targetID, entityID, col)
	h.reexpireMark(t, ctx, key)
	h.agePastWarmup()
	h.pass(ctx)
	op := h.nextOp(t)
	if op["operationType"] != "FixX" {
		t.Fatalf("the sweep's lease-expiry reclaim is the backstop for a lost obligation; got %v",
			op["operationType"])
	}
}

// --- the set's own contract --------------------------------------------------

// TestRepublishSet_Lifetime walks the entries' lifetime: the routes that create
// them, the routes that retire them, and the cap.
func TestRepublishSet_Lifetime(t *testing.T) {
	t.Parallel()
	r := newRepublishSet()
	const targetA, targetB = "targetA", "targetB"

	if r.owes(targetA, "e1", "missing_x") {
		t.Fatalf("an untouched key owes nothing")
	}
	if !r.add(targetA, "e1", "missing_x") {
		t.Fatalf("the first obligation must be recorded")
	}
	if !r.owes(targetA, "e1", "missing_x") {
		t.Fatalf("a recorded obligation must be readable")
	}
	// Keyed per (target, entity, gap) — the mark's own identity. A sibling gap
	// on the same row, and the same gap on a sibling row, are separate facts.
	if r.owes(targetA, "e1", "missing_y") || r.owes(targetA, "e2", "missing_x") || r.owes(targetB, "e1", "missing_x") {
		t.Fatalf("the obligation leaked across the key's segments")
	}

	// Adding twice is one entry, and clearing is idempotent.
	r.add(targetA, "e1", "missing_x")
	r.clear(targetA, "e1", "missing_x")
	r.clear(targetA, "e1", "missing_x")
	if r.owes(targetA, "e1", "missing_x") {
		t.Fatalf("a cleared obligation must not survive a duplicate add")
	}

	// The target teardown drops the whole target and nothing else.
	r.add(targetA, "e1", "missing_x")
	r.add(targetA, "e2", "missing_x")
	r.add(targetB, "e1", "missing_x")
	r.clearTarget(targetA)
	if r.owes(targetA, "e1", "missing_x") || r.owes(targetA, "e2", "missing_x") {
		t.Fatalf("a revoked target's obligations must go with it")
	}
	if !r.owes(targetB, "e1", "missing_x") {
		t.Fatalf("one target's teardown must not touch another's")
	}

	// The cap refuses past its limit, per target, and says so to the caller.
	fresh := newRepublishSet()
	for i := 0; i < republishCapPerTarget; i++ {
		if !fresh.add(targetA, "e"+strconv.Itoa(i), "missing_x") {
			t.Fatalf("obligation %d was refused below the cap", i)
		}
	}
	if fresh.add(targetA, "eOverflow", "missing_x") {
		t.Fatalf("past the cap an insertion must be refused, not silently held")
	}
	if fresh.owes(targetA, "eOverflow", "missing_x") {
		t.Fatalf("a refused key must not read as owed — it degrades to the reclaim ladder")
	}
	// An ALREADY-held key is never refused: the cap bounds how many distinct
	// obligations are tracked, not whether a tracked one can be re-asserted.
	if !fresh.add(targetA, "e0", "missing_x") {
		t.Fatalf("re-asserting a held obligation must not be refused")
	}
	// The cap is per target.
	if !fresh.add(targetB, "e0", "missing_x") {
		t.Fatalf("one target at its cap must not refuse another target's first obligation")
	}
	// Clearing one readmits.
	fresh.clear(targetA, "e0", "missing_x")
	if !fresh.add(targetA, "eOverflow", "missing_x") {
		t.Fatalf("a freed slot must be readmitted")
	}
}

// TestClearClosedMarks_RetiresTheRepublishObligation pins the other retirement
// route. An obligation says "this episode's op may not have landed"; a gap that
// CLOSED has no episode left to owe anything for, so the level-reconcile that
// clears the mark must clear the obligation with it — otherwise a later reopen
// of the same gap would find a stale entry and re-publish the new episode's op
// once for no reason.
func TestClearClosedMarks_RetiresTheRepublishObligation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureRepublishClose"
	const col = "missing_x"
	target := &Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{col: {Action: actionDirectOp, Operation: "FixX"}},
	}
	h.seedTarget(target)
	entityID := testNanoID(t)
	open := map[string]any{"entityKey": "vtx.leaseApp." + entityID, "violating": true, col: true}
	h.putRow(t, ctx, targetID, entityID, open)

	restore := breakOpsStream(t, ctx, h)
	if dec := h.engine.handleRow(unackedPublishCtx(t, ctx), republishRowMessage(t, h, targetID, entityID, open, 1)); dec != substrate.Nak {
		t.Fatalf("setup: the failed publish must Nak, got %v", dec)
	}
	restore()
	h.nextOp(t) // the un-acked op that reached the wire regardless
	if !h.engine.republish.owes(targetID, entityID, col) {
		t.Fatalf("setup: the obligation must stand")
	}

	// The gap closes before the redelivery lands.
	closed := map[string]any{"entityKey": "vtx.leaseApp." + entityID, "violating": false, col: false}
	if dec := h.engine.handleRow(ctx, republishRowMessage(t, h, targetID, entityID, closed, 2)); dec != substrate.Ack {
		t.Fatalf("the closing row must Ack, got %v", dec)
	}
	if h.engine.republish.owes(targetID, entityID, col) {
		t.Fatalf("a closed gap owes nothing; its obligation must be retired with its mark")
	}
	h.requireNoOp(t)
}
