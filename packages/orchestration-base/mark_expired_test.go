package orchestrationbase_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
)

// The two weaver targets used throughout as siblings sharing one anchor — the
// shape the per-target marker exists for.
const (
	meTargetA = "leaseApplicationComplete"
	meTargetB = "leaseExpiry"
)

// TestMarkExpired_MergesAcrossCycles: a SECOND fire on the same entity and the
// same target (a second freshness cycle) merges into the standing marker rather
// than conflicting, and advances both this target's byTarget entry and the
// entity-wide expiredAt.
func TestMarkExpired_MergesAcrossCycles(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "me-cycles")

	// Seed an entity of an arbitrary anchor type (leaseapp) — MarkExpired is
	// type-agnostic, so the concrete type is just data here.
	entityKey := "vtx.leaseapp.BBmarkexpiredHJKMNPQ"
	seedVertex(t, ctx, conn, entityKey, "leaseapp", map[string]any{})
	markerKey := entityKey + ".freshnessExpiry"

	// First lapse: the marker is hydrated known-absent, so the script creates it.
	first := "2026-06-18T14:00:00Z"
	submitMarkExpired(t, ctx, conn, cp, cons, "MEcycle000001", entityKey, meTargetA, first)
	assertMarker(t, ctx, conn, markerKey, first, map[string]string{meTargetA: first})

	// Second lapse (a NEW freshness cycle): a different requestId + a later
	// expiredAt. The marker is now hydrated present, so the script merges.
	second := "2026-06-18T15:30:00Z"
	submitMarkExpired(t, ctx, conn, cp, cons, "MEcycle000002", entityKey, meTargetA, second)
	assertMarker(t, ctx, conn, markerKey, second, map[string]string{meTargetA: second})
}

// TestMarkExpired_SecondTargetPreservesSiblingEntry is the defect the per-target
// keying closes: two weaver targets share one anchor type and one marker slot,
// so a fire for the second target must merge its entry beside the first's, not
// replace the document. expiredAt is the maximum over both.
func TestMarkExpired_SecondTargetPreservesSiblingEntry(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "me-twotargets")

	entityKey := "vtx.leaseapp.BBmarktwotgtHJKMNPQR"
	seedVertex(t, ctx, conn, entityKey, "leaseapp", map[string]any{})
	markerKey := entityKey + ".freshnessExpiry"

	early := "2026-06-18T09:00:00Z"
	late := "2026-06-18T17:00:00Z"
	submitMarkExpired(t, ctx, conn, cp, cons, "MEtwotgt00001", entityKey, meTargetA, early)
	submitMarkExpired(t, ctx, conn, cp, cons, "MEtwotgt00002", entityKey, meTargetB, late)

	assertMarker(t, ctx, conn, markerKey, late, map[string]string{
		meTargetA: early,
		meTargetB: late,
	})
}

// TestMarkExpired_EarlierInstantNeverMovesExpiredAtBackwards pins the constraint
// the lease-convergence harness rests on: expiredAt is the monotone maximum, so
// a target firing at an instant EARLIER than one already recorded — a sibling
// on an earlier window, a replayed firing — neither rewrites that target's own
// entry nor drags the entity-wide value back.
func TestMarkExpired_EarlierInstantNeverMovesExpiredAtBackwards(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "me-monotone")

	entityKey := "vtx.leaseapp.BBmarkmonotnHJKMNPQR"
	seedVertex(t, ctx, conn, entityKey, "leaseapp", map[string]any{})
	markerKey := entityKey + ".freshnessExpiry"

	late := "2026-06-18T17:00:00Z"
	early := "2026-06-18T09:00:00Z"
	submitMarkExpired(t, ctx, conn, cp, cons, "MEmono0000001", entityKey, meTargetA, late)
	// The SAME target fires at an earlier instant: its own entry holds.
	submitMarkExpired(t, ctx, conn, cp, cons, "MEmono0000002", entityKey, meTargetA, early)
	assertMarker(t, ctx, conn, markerKey, late, map[string]string{meTargetA: late})

	// A SIBLING target's earlier lapse is recorded, and expiredAt still holds
	// at the maximum.
	submitMarkExpired(t, ctx, conn, cp, cons, "MEmono0000003", entityKey, meTargetB, early)
	assertMarker(t, ctx, conn, markerKey, late, map[string]string{
		meTargetA: late,
		meTargetB: early,
	})
}

// TestMarkExpired_MissingTargetId_Rejected: the targetId keys the byTarget entry
// a lens reads, so a MarkExpired without one has nowhere to record its verdict
// and must fail closed rather than write an entry no lens can find.
func TestMarkExpired_MissingTargetId_Rejected(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "me-notarget")

	entityKey := "vtx.leaseapp.BBmarknotgtHJKMNPQRS"
	seedVertex(t, ctx, conn, entityKey, "leaseapp", map[string]any{})

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("MEnotgt000001"),
		Lane:          processor.LaneDefault,
		OperationType: "MarkExpired",
		Actor:         otStaffActorKey,
		SubmittedAt:   "2026-06-18T17:00:00Z",
		Class:         "freshnessMarker",
		Payload:       json.RawMessage(`{"entityKey":"` + entityKey + `","expiredAt":"2026-06-18T17:00:00Z"}`),
		ContextHint:   markExpiredContextHint(entityKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, entityKey+".freshnessExpiry"); err == nil {
		t.Fatalf("a targetId-less MarkExpired wrote a marker (%s.freshnessExpiry must not exist)", entityKey)
	}
}

// TestMarkExpired_TypeAgnostic proves the SAME MarkExpired DDL serves a
// different anchor type (an identity vertex) — the script names no concrete
// type, resolving the entity solely from payload.entityKey.
func TestMarkExpired_TypeAgnostic(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "me-typeagnostic")

	entityKey := "vtx.identity.BBmarkexpidHJKMNPQRS"
	seedVertex(t, ctx, conn, entityKey, "identity", map[string]any{"state": "claimed"})

	submitMarkExpired(t, ctx, conn, cp, cons, "MEtype0000001", entityKey, meTargetA, "2026-06-18T16:00:00Z")
	got := readDoc(t, ctx, conn, entityKey+".freshnessExpiry")
	if cls, _ := got["class"].(string); cls != "freshnessExpiry" {
		t.Fatalf("marker class = %q, want freshnessExpiry", cls)
	}
}

// TestMarkExpired_ClassInferredFromOperationType is the RF#1↔RF#3 join: MarkExpired
// dispatched with NO `class` field (exactly what Weaver's temporal lane sends)
// resolves its DDL via the operationType→class reverse index — freshnessMarker
// is the sole vertexType DDL admitting MarkExpired (the freshnessExpiry
// aspect-type DDL also lists it, but aspectType DDLs are excluded from the
// index), so inference is unambiguous and the marker commits.
func TestMarkExpired_ClassInferredFromOperationType(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "me-inferclass")

	entityKey := "vtx.leaseapp.BBmarkenferHJKMNPQRS"
	seedVertex(t, ctx, conn, entityKey, "leaseapp", map[string]any{})

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("MEinfer000001"),
		Lane:          processor.LaneDefault,
		OperationType: "MarkExpired",
		Actor:         otStaffActorKey,
		SubmittedAt:   "2026-06-18T17:00:00Z",
		// Class deliberately OMITTED — must be inferred from operationType.
		Payload: json.RawMessage(`{"entityKey":"` + entityKey + `","targetId":"` + meTargetA +
			`","expiredAt":"2026-06-18T17:00:00Z"}`),
		ContextHint: markExpiredContextHint(entityKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	got := readDoc(t, ctx, conn, entityKey+".freshnessExpiry")
	if cls, _ := got["class"].(string); cls != "freshnessExpiry" {
		t.Fatalf("class-inferred MarkExpired marker class = %q, want freshnessExpiry", cls)
	}
}

// TestMarkExpired_AbsentEntity_Rejected is the C1 target-existence guard: a
// MarkExpired whose entityKey points at a vertex that does NOT exist must be
// rejected (NotFound) — no marker is written onto an absent parent. The marker
// aspect is non-sensitive, so step-6's sensitiveAspectScope never fires; this
// script-level vertex_alive on the hydrated root is the sole guard.
func TestMarkExpired_AbsentEntity_Rejected(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "me-absent")

	// Deliberately NOT seeded: the entity does not exist in Core KV.
	absentKey := "vtx.leaseapp.BBmarkabsentHJKMNPQ"

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("MEabsent00001"),
		Lane:          processor.LaneDefault,
		OperationType: "MarkExpired",
		Actor:         otStaffActorKey,
		SubmittedAt:   "2026-06-18T18:00:00Z",
		Class:         "freshnessMarker",
		Payload: json.RawMessage(`{"entityKey":"` + absentKey + `","targetId":"` + meTargetA +
			`","expiredAt":"2026-06-18T18:00:00Z"}`),
		// The DDL hydrates the root — which is absent, so the guard fails closed.
		ContextHint: markExpiredContextHint(absentKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// The marker aspect must NOT have been written.
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, absentKey+".freshnessExpiry"); err == nil {
		t.Fatalf("a marker aspect was written onto an absent entity (%s.freshnessExpiry must not exist)", absentKey)
	}
}

// TestMarkExpired_TombstonedEntity_Rejected proves the C1 guard also fires for a
// tombstoned (isDeleted) parent — a stale firing whose entity was deleted after
// the timer armed must not resurrect a marker on the dead vertex.
func TestMarkExpired_TombstonedEntity_Rejected(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "me-tomb")

	entityKey := "vtx.leaseapp.BBmarktombHJKMNPQRS"
	// Seed it tombstoned.
	dead := map[string]any{"class": "leaseapp", "isDeleted": true, "data": map[string]any{}}
	b, _ := json.Marshal(dead)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, entityKey, b); err != nil {
		t.Fatalf("seed tombstoned entity: %v", err)
	}

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("MEtomb000001"),
		Lane:          processor.LaneDefault,
		OperationType: "MarkExpired",
		Actor:         otStaffActorKey,
		SubmittedAt:   "2026-06-18T18:30:00Z",
		Class:         "freshnessMarker",
		Payload: json.RawMessage(`{"entityKey":"` + entityKey + `","targetId":"` + meTargetA +
			`","expiredAt":"2026-06-18T18:30:00Z"}`),
		ContextHint: markExpiredContextHint(entityKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestMarkExpired_NonOperatorActor_Denied drives the REAL capability auth path
// (CapabilityPipeline, AuthModeCapability) for an actor whose cap doc grants
// other ops but NOT MarkExpired: the op must be DENIED at step 3 — proving the
// scope:any MarkExpired grant is correctly gated to the operator-equivalent
// service actor and not open to any caller. (The e2e runs AuthModeStub, so this
// is the place the grant is actually exercised against the validator.)
func TestMarkExpired_NonOperatorActor_Denied(t *testing.T) {
	ctx, conn := setupOrchEnv(t)
	cp, cons := newTaskPipeline(t, ctx, conn, "me-denied")

	// A second actor with a cap doc that grants CreateTask but NOT MarkExpired,
	// and is NOT in the operator role — so the MarkExpired operator grant cannot
	// authorize it.
	const nonOpActorID = "BBnonopActHJKMNPQRST"
	const nonOpActorKey = "vtx.identity." + nonOpActorID
	const nonOpCapKey = "cap.identity." + nonOpActorID
	now := time.Now().UTC()
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    nonOpCapKey,
		Actor:                  nonOpActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{nonOpActorKey: 1},
		Lanes:                  []string{"default"},
		// Grants an unrelated op; deliberately omits MarkExpired. No operator role.
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateTask", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{},
	})

	entityKey := "vtx.leaseapp.BBmarkdenyHJKMNPQRS"
	seedVertex(t, ctx, conn, entityKey, "leaseapp", map[string]any{})

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("MEdeny000001"),
		Lane:          processor.LaneDefault,
		OperationType: "MarkExpired",
		Actor:         nonOpActorKey,
		SubmittedAt:   "2026-06-18T19:00:00Z",
		Class:         "freshnessMarker",
		Payload: json.RawMessage(`{"entityKey":"` + entityKey + `","targetId":"` + meTargetA +
			`","expiredAt":"2026-06-18T19:00:00Z"}`),
		ContextHint: markExpiredContextHint(entityKey),
	}
	testutil.PublishOp(t, conn, env)
	// Auth denial surfaces as OutcomeRejected (step 3 terminates the op).
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// And the marker must NOT have been written (the denial precedes any commit).
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, entityKey+".freshnessExpiry"); err == nil {
		t.Fatalf("a denied MarkExpired wrote a marker (%s.freshnessExpiry must not exist)", entityKey)
	}
}

// --- concurrent-fire branches, driven at the script seam ---
//
// The two racers' interleaving is not something the pull-consumer harness can
// order, so these drive the SEQUENCE the Processor's in-process retry produces:
// each racer is executed against exactly the hydrated state step 4 would hand
// it, and the loser is then re-executed against the state its re-hydration sees
// after the winner committed (commit_path.go's re-hydrate + re-execute on a
// §3.2-defaulted conflict, and on a known-absent-conditioned create whose key
// materialized). What the merge must survive is that second execution.

// TestMarkExpired_FirstFireRace_LoserRetryLandsAsUpdate: both racers see the
// marker known-absent and both emit a `create` — the mutation kind that carries
// the observed-absence condition, which is what makes the loser's conflict
// retry-eligible. The loser then re-executes against the winner's committed
// marker, takes the update branch, and BOTH byTarget entries survive.
func TestMarkExpired_FirstFireRace_LoserRetryLandsAsUpdate(t *testing.T) {
	entityKey := "vtx.leaseapp.BBmarkrace1HJKMNPQRS"
	markerKey := entityKey + ".freshnessExpiry"
	winnerAt := "2026-06-18T09:00:00Z"
	loserAt := "2026-06-18T17:00:00Z"

	// Both racers hydrate the marker as known-absent.
	winner := runMarkExpiredScript(t, entityKey, meTargetA, winnerAt, nil, true)
	loser := runMarkExpiredScript(t, entityKey, meTargetB, loserAt, nil, true)
	assertMutation(t, winner, "create", markerKey, winnerAt, map[string]string{meTargetA: winnerAt})
	assertMutation(t, loser, "create", markerKey, loserAt, map[string]string{meTargetB: loserAt})

	// The winner commits; the loser's create conflicts, the Processor
	// re-hydrates (the marker now exists) and re-executes.
	retry := runMarkExpiredScript(t, entityKey, meTargetB, loserAt, mutationDoc(t, winner), false)
	assertMutation(t, retry, "update", markerKey, loserAt, map[string]string{
		meTargetA: winnerAt,
		meTargetB: loserAt,
	})
}

// TestMarkExpired_SteadyStateRace_ConflictRetryKeepsBothEntries: the marker is
// already present, so both racers emit an `update` with NO explicit
// expectedRevision — which is precisely what makes the Processor condition each
// on the revision it was hydrated at (Contract #3 §3.2), so the second writer
// conflicts instead of overwriting. The loser re-executes against the merged
// document and neither target's entry is lost.
func TestMarkExpired_SteadyStateRace_ConflictRetryKeepsBothEntries(t *testing.T) {
	entityKey := "vtx.leaseapp.BBmarkrace2HJKMNPQRS"
	markerKey := entityKey + ".freshnessExpiry"
	priorAt := "2026-06-18T06:00:00Z"
	winnerAt := "2026-06-18T09:00:00Z"
	loserAt := "2026-06-18T17:00:00Z"

	// A standing marker both racers hydrate at the same revision.
	standing := markerDoc(entityKey, priorAt, map[string]string{meTargetA: priorAt})
	winner := runMarkExpiredScript(t, entityKey, meTargetA, winnerAt, standing, false)
	loser := runMarkExpiredScript(t, entityKey, meTargetB, loserAt, standing, false)
	assertMutation(t, winner, "update", markerKey, winnerAt, map[string]string{meTargetA: winnerAt})
	assertMutation(t, loser, "update", markerKey, loserAt, map[string]string{
		meTargetA: priorAt,
		meTargetB: loserAt,
	})
	for _, m := range [][]processor.MutationOp{winner.Mutations, loser.Mutations} {
		if m[0].ExpectedRevision != nil {
			t.Fatalf("the merge must carry NO explicit expectedRevision — the Processor's §3.2 default "+
				"is what conditions it on the hydrated revision; got %d", *m[0].ExpectedRevision)
		}
	}

	// The winner commits; the loser conflicts, re-hydrates the merged document
	// and re-executes.
	retry := runMarkExpiredScript(t, entityKey, meTargetB, loserAt, mutationDoc(t, winner), false)
	assertMutation(t, retry, "update", markerKey, loserAt, map[string]string{
		meTargetA: winnerAt,
		meTargetB: loserAt,
	})
}

// --- helpers ---

// markExpiredContextHint is the read declaration Weaver's temporal lane sends:
// the entity root fail-closed, the marker aspect absence-tolerant (the DDL
// merges into it, and its absence on a first lapse is a legitimate branch).
func markExpiredContextHint(entityKey string) *processor.ContextHint {
	return &processor.ContextHint{
		Reads:         []string{entityKey},
		OptionalReads: []string{entityKey + ".freshnessExpiry"},
	}
}

// submitMarkExpired publishes one MarkExpired op (with explicit class) and drives
// it to OutcomeAccepted.
func submitMarkExpired(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, reqSeed, entityKey, targetID, expiredAt string) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(reqSeed),
		Lane:          processor.LaneDefault,
		OperationType: "MarkExpired",
		Actor:         otStaffActorKey,
		SubmittedAt:   expiredAt,
		Class:         "freshnessMarker",
		Payload: json.RawMessage(`{"entityKey":"` + entityKey + `","targetId":"` + targetID +
			`","expiredAt":"` + expiredAt + `"}`),
		ContextHint: markExpiredContextHint(entityKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

// assertMarker reads the committed marker aspect and asserts its expiredAt and
// its whole byTarget map — the whole map, so a merge that dropped a sibling's
// entry fails here rather than passing on "no lower value".
func assertMarker(t *testing.T, ctx context.Context, conn *substrate.Conn, markerKey, wantExpiredAt string, wantByTarget map[string]string) {
	t.Helper()
	doc := readDoc(t, ctx, conn, markerKey)
	data, _ := doc["data"].(map[string]any)
	assertMarkerData(t, data, wantExpiredAt, wantByTarget)
}

func assertMarkerData(t *testing.T, data map[string]any, wantExpiredAt string, wantByTarget map[string]string) {
	t.Helper()
	if data == nil {
		t.Fatalf("marker carries no data object")
	}
	if got, _ := data["expiredAt"].(string); got != wantExpiredAt {
		t.Fatalf("marker expiredAt = %q, want %q (the monotone maximum over byTarget)", got, wantExpiredAt)
	}
	byTarget, _ := data["byTarget"].(map[string]any)
	if len(byTarget) != len(wantByTarget) {
		t.Fatalf("marker byTarget = %v, want %v", byTarget, wantByTarget)
	}
	for target, want := range wantByTarget {
		if got, _ := byTarget[target].(string); got != want {
			t.Fatalf("marker byTarget[%q] = %q, want %q (in %v)", target, got, want, byTarget)
		}
	}
}

// markerDoc is the hydrated marker document step 4 hands the script for a
// standing marker.
func markerDoc(entityKey, expiredAt string, byTarget map[string]string) *processor.VertexDoc {
	entries := map[string]any{}
	for k, v := range byTarget {
		entries[k] = v
	}
	return &processor.VertexDoc{
		Key:       entityKey + ".freshnessExpiry",
		Class:     "freshnessExpiry",
		VertexKey: entityKey,
		LocalName: "freshnessExpiry",
		Data:      map[string]any{"expiredAt": expiredAt, "byTarget": entries},
		Revision:  7,
	}
}

// mutationDoc turns a script result's marker mutation into the hydrated
// document a re-execution would read after that mutation committed.
func mutationDoc(t *testing.T, res processor.ScriptResult) *processor.VertexDoc {
	t.Helper()
	if len(res.Mutations) != 1 {
		t.Fatalf("expected exactly one mutation, got %d", len(res.Mutations))
	}
	m := res.Mutations[0]
	data, _ := m.Document["data"].(map[string]any)
	vertexKey, _ := m.Document["vertexKey"].(string)
	return &processor.VertexDoc{
		Key:       m.Key,
		Class:     "freshnessExpiry",
		VertexKey: vertexKey,
		LocalName: "freshnessExpiry",
		Data:      data,
		Revision:  9,
	}
}

// runMarkExpiredScript executes the shipped MarkExpired script against exactly
// the hydrated state step 4 would build: the entity root always present, and the
// marker either hydrated (standing != nil) or recorded known-absent.
func runMarkExpiredScript(t *testing.T, entityKey, targetID, expiredAt string, standing *processor.VertexDoc, knownAbsent bool) processor.ScriptResult {
	t.Helper()
	markerKey := entityKey + ".freshnessExpiry"
	hydrated := map[string]processor.VertexDoc{
		entityKey: {Key: entityKey, Class: "leaseapp", Data: map[string]any{}, Revision: 3},
	}
	absent := map[string]struct{}{}
	if standing != nil {
		hydrated[markerKey] = *standing
	}
	if knownAbsent {
		absent[markerKey] = struct{}{}
	}
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("MEunit" + targetID),
		Lane:          processor.LaneDefault,
		OperationType: "MarkExpired",
		Actor:         otStaffActorKey,
		SubmittedAt:   expiredAt,
		Class:         "freshnessMarker",
		Payload: json.RawMessage(`{"entityKey":"` + entityKey + `","targetId":"` + targetID +
			`","expiredAt":"` + expiredAt + `"}`),
		ContextHint: markExpiredContextHint(entityKey),
	}
	res, err := processor.NewStarlarkRunner(0, 0).Run(context.Background(), processor.ScriptContext{
		Operation:    env,
		Hydrated:     hydrated,
		KnownAbsent:  absent,
		ScriptSource: orchestrationbase.MarkExpiredDDL().Script,
		ScriptClass:  "freshnessMarker",
	})
	if err != nil {
		t.Fatalf("run MarkExpired script (%s): %v", targetID, err)
	}
	return res
}

// assertMutation asserts the script proposed exactly one marker mutation of the
// given kind, carrying the expected merged data.
func assertMutation(t *testing.T, res processor.ScriptResult, wantOp, wantKey, wantExpiredAt string, wantByTarget map[string]string) {
	t.Helper()
	if len(res.Mutations) != 1 {
		t.Fatalf("expected exactly one mutation, got %d", len(res.Mutations))
	}
	m := res.Mutations[0]
	if m.Op != wantOp {
		t.Fatalf("mutation op = %q, want %q", m.Op, wantOp)
	}
	if m.Key != wantKey {
		t.Fatalf("mutation key = %q, want %q", m.Key, wantKey)
	}
	data, _ := m.Document["data"].(map[string]any)
	assertMarkerData(t, data, wantExpiredAt, wantByTarget)
}
