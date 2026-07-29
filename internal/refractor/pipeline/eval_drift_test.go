package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// --- footprintValid unit tests ---

func TestFootprintValid_UnchangedRevisionsMatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const idID = "Tfp1JdentityAaaaaaaa"
	idKey := "vtx.identity." + idID
	writeCollisionVertex(t, coreKV, idKey, "identity", map[string]any{"name": "a"})
	entry, err := coreKV.Get(ctx, idKey)
	require.NoError(t, err)

	p := &Pipeline{coreKV: coreKV, adjKV: adjKV}
	fp := ruleengine.EvalFootprint{NodeRevisions: map[string]uint64{idKey: entry.Revision}}

	valid, verr := p.footprintValid(ctx, fp)
	require.NoError(t, verr)
	require.True(t, valid, "identical revisions must validate")
}

func TestFootprintValid_RevisionChanged(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const idID = "Tfp2JdentityBbbbbbbb"
	idKey := "vtx.identity." + idID
	writeCollisionVertex(t, coreKV, idKey, "identity", map[string]any{"name": "a"})
	entry, err := coreKV.Get(ctx, idKey)
	require.NoError(t, err)

	p := &Pipeline{coreKV: coreKV, adjKV: adjKV}
	fp := ruleengine.EvalFootprint{NodeRevisions: map[string]uint64{idKey: entry.Revision}}

	// A commit lands after the footprint was captured.
	writeCollisionVertex(t, coreKV, idKey, "identity", map[string]any{"name": "b"})

	valid, verr := p.footprintValid(ctx, fp)
	require.NoError(t, verr)
	require.False(t, valid, "a moved revision must invalidate the footprint")
}

func TestFootprintValid_NewlyAbsentKey(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const idID = "Tfp3JdentityCccccccc"
	idKey := "vtx.identity." + idID
	writeCollisionVertex(t, coreKV, idKey, "identity", map[string]any{"name": "a"})
	entry, err := coreKV.Get(ctx, idKey)
	require.NoError(t, err)

	p := &Pipeline{coreKV: coreKV, adjKV: adjKV}
	fp := ruleengine.EvalFootprint{NodeRevisions: map[string]uint64{idKey: entry.Revision}}

	// Soft-delete — the same absence readNode applies, so this is a present
	// (nonzero revision) → absent (0) flip from footprintValid's perspective.
	body, merr := json.Marshal(map[string]any{"key": idKey, "class": "identity", "isDeleted": true})
	require.NoError(t, merr)
	_, err = coreKV.Put(ctx, idKey, body)
	require.NoError(t, err)

	valid, verr := p.footprintValid(ctx, fp)
	require.NoError(t, verr)
	require.False(t, valid, "a present-then-absent flip must invalidate the footprint")
}

func TestFootprintValid_NewlyPresentKey(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const idID = "Tfp4JdentityDddddddd"
	idKey := "vtx.identity." + idID

	p := &Pipeline{coreKV: coreKV, adjKV: adjKV}
	// The footprint recorded absence (revision 0) — the key did not exist
	// when the evaluation ran.
	fp := ruleengine.EvalFootprint{NodeRevisions: map[string]uint64{idKey: 0}}

	writeCollisionVertex(t, coreKV, idKey, "identity", map[string]any{"name": "new"})

	valid, verr := p.footprintValid(ctx, fp)
	require.NoError(t, verr)
	require.False(t, valid, "an absent-then-present flip must invalidate the footprint")
}

func TestFootprintValid_EdgeRevisionChanged(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const idID = "Tfp5JdentityEeeeeeee"
	buildCollisionEdge(t, adjKV, "holdsRole", "identity", idID, "role", "Tfp5RgAaaaaaaaaaaaaa")
	_, rev, err := adjacency.Neighbors(ctx, adjKV, idID)
	require.NoError(t, err)
	require.NotZero(t, rev)

	p := &Pipeline{coreKV: coreKV, adjKV: adjKV}
	fp := ruleengine.EvalFootprint{EdgeRevisions: map[string]uint64{idID: rev}}

	valid, verr := p.footprintValid(ctx, fp)
	require.NoError(t, verr)
	require.True(t, valid)

	// A second role granted mid-flight bumps the identity's OWN adjacency
	// document revision (the memo entry the traversal from identity read).
	buildCollisionEdge(t, adjKV, "holdsRole", "identity", idID, "role", "Tfp5RgBbbbbbbbbbbbbb")

	valid, verr = p.footprintValid(ctx, fp)
	require.NoError(t, verr)
	require.False(t, valid, "a new edge on the footprinted node must invalidate the footprint")
}

// --- footprintValid selector-scoped tests (§13.4) ---

// TestFootprintValid_EdgeSelector_UnrelatedEdgeAdded_NoDrift pins
// footprintValid's selector-scoped branch: a node whose EvalFootprint entry
// carries a non-Fallback EdgeSelectorFootprint is validated by re-applying
// the recorded selector to a fresh read, not by comparing the whole
// document's revision — so a write to an UNRELATED relation on that node
// (defect 2's exact shape: a shared hub under grantedBy write pressure from
// an evaluation that only ever followed queuedFor) must not invalidate it,
// even though the node's whole-document revision (still present in
// EdgeRevisions) DID move underneath it.
func TestFootprintValid_EdgeSelector_UnrelatedEdgeAdded_NoDrift(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const roleID = "Tfp6RoleAaaaaaaaaaaa"
	buildCollisionEdge(t, adjKV, "queuedFor", "task", "Tfp6TaskAaaaaaaaaaaa", "role", roleID)
	edges, rev, err := adjacency.Neighbors(ctx, adjKV, roleID)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	queuedForID := edges[0].EdgeID

	p := &Pipeline{coreKV: coreKV, adjKV: adjKV}
	fp := ruleengine.EvalFootprint{
		EdgeRevisions: map[string]uint64{roleID: rev},
		EdgeSelectors: map[string]ruleengine.EdgeSelectorFootprint{
			roleID: {
				Matched: map[ruleengine.EdgeSelector]map[string]struct{}{
					{RelType: "queuedFor", Direction: "in"}: {queuedForID: {}},
				},
			},
		},
	}

	valid, verr := p.footprintValid(ctx, fp)
	require.NoError(t, verr)
	require.True(t, valid)

	// An UNRELATED grantedBy edge lands on the SAME role node — the whole-
	// document revision moves, but the queuedFor selector's matched set does
	// not.
	buildCollisionEdge(t, adjKV, "grantedBy", "permission", "Tfp6PermAaaaaaaaaaaa", "role", roleID)

	valid, verr = p.footprintValid(ctx, fp)
	require.NoError(t, verr)
	require.True(t, valid, "an unrelated grantedBy write must not register as drift for a queuedFor-scoped footprint")
}

// TestFootprintValid_EdgeSelector_RelatedEdgeAdded_Drift is the positive
// twin: a second edge matching the SAME recorded selector does invalidate
// the footprint — selector-scoping narrows what counts as drift, it must
// never mask a change the evaluation actually depended on.
func TestFootprintValid_EdgeSelector_RelatedEdgeAdded_Drift(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const roleID = "Tfp7RoleAaaaaaaaaaaa"
	buildCollisionEdge(t, adjKV, "queuedFor", "task", "Tfp7TaskAaaaaaaaaaaa", "role", roleID)
	edges, rev, err := adjacency.Neighbors(ctx, adjKV, roleID)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	queuedForID := edges[0].EdgeID

	p := &Pipeline{coreKV: coreKV, adjKV: adjKV}
	fp := ruleengine.EvalFootprint{
		EdgeRevisions: map[string]uint64{roleID: rev},
		EdgeSelectors: map[string]ruleengine.EdgeSelectorFootprint{
			roleID: {
				Matched: map[ruleengine.EdgeSelector]map[string]struct{}{
					{RelType: "queuedFor", Direction: "in"}: {queuedForID: {}},
				},
			},
		},
	}

	valid, verr := p.footprintValid(ctx, fp)
	require.NoError(t, verr)
	require.True(t, valid)

	// A SECOND task queues for the SAME role — a relevant change to exactly
	// the recorded selector.
	buildCollisionEdge(t, adjKV, "queuedFor", "task", "Tfp7TaskBbbbbbbbbbbb", "role", roleID)

	valid, verr = p.footprintValid(ctx, fp)
	require.NoError(t, verr)
	require.False(t, valid, "a second queuedFor edge on the same node must register as drift")
}

// TestFootprintValid_EdgeSelector_Fallback_DriftsOnEither pins the coarser-
// is-always-safe fallback: a node whose EvalFootprint entry has Fallback set
// (an untyped hop consumed every edge regardless of type) is validated by
// the ORIGINAL whole-document revision comparison, and so drifts on EITHER
// a related or an unrelated edge landing on it.
func TestFootprintValid_EdgeSelector_Fallback_DriftsOnEither(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const roleID = "Tfp8RoleAaaaaaaaaaaa"
	buildCollisionEdge(t, adjKV, "queuedFor", "task", "Tfp8TaskAaaaaaaaaaaa", "role", roleID)
	_, rev, err := adjacency.Neighbors(ctx, adjKV, roleID)
	require.NoError(t, err)

	p := &Pipeline{coreKV: coreKV, adjKV: adjKV}
	fp := ruleengine.EvalFootprint{
		EdgeRevisions: map[string]uint64{roleID: rev},
		EdgeSelectors: map[string]ruleengine.EdgeSelectorFootprint{
			roleID: {Fallback: true},
		},
	}

	valid, verr := p.footprintValid(ctx, fp)
	require.NoError(t, verr)
	require.True(t, valid)

	buildCollisionEdge(t, adjKV, "grantedBy", "permission", "Tfp8PermAaaaaaaaaaaa", "role", roleID)

	valid, verr = p.footprintValid(ctx, fp)
	require.NoError(t, verr)
	require.False(t, valid, "a Fallback node must drift on ANY edge addition, related or not")
}

// TestFootprintValid_EdgeSelector_AbsentFromSelectors_FallsBack pins the
// defensive branch: a node present in EdgeRevisions but with no entry in
// EdgeSelectors at all (an old/degenerate footprint) is validated the same
// way as Fallback — coarser is always the safe direction.
func TestFootprintValid_EdgeSelector_AbsentFromSelectors_FallsBack(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const roleID = "Tfp9RoleAaaaaaaaaaaa"
	buildCollisionEdge(t, adjKV, "queuedFor", "task", "Tfp9TaskAaaaaaaaaaaa", "role", roleID)
	_, rev, err := adjacency.Neighbors(ctx, adjKV, roleID)
	require.NoError(t, err)

	p := &Pipeline{coreKV: coreKV, adjKV: adjKV}
	fp := ruleengine.EvalFootprint{
		EdgeRevisions: map[string]uint64{roleID: rev},
		// No EdgeSelectors entry at all for roleID.
	}

	buildCollisionEdge(t, adjKV, "grantedBy", "permission", "Tfp9PermAaaaaaaaaaaa", "role", roleID)

	valid, verr := p.footprintValid(ctx, fp)
	require.NoError(t, verr)
	require.False(t, valid, "a node absent from EdgeSelectors must fall back to whole-document comparison")
}

// --- executeFullForActor orchestration tests ---

// evalDriftCypher matches one identity and its (at most one) held role,
// surfacing the role's name so a test can observe whether a returned row
// reflects pre- or post-drift state.
const evalDriftCypher = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)
RETURN identity.key AS actorKey, role.key AS roleKey, role.data.name AS roleName
`

// evalDriftEnvelopeFn derives the anchor-keyed output the real capability
// envelope shape uses (one row per actor, keyed off the actor), carrying
// roleName through untouched so a test can assert on it directly.
func evalDriftEnvelopeFn(row, _, params map[string]any) (map[string]any, map[string]any, error) {
	actorKey, _ := row["actorKey"].(string)
	if actorKey == "" {
		actorKey, _ = params["actorKey"].(string)
	}
	suffix := actorKey
	if rest, ok := strings.CutPrefix(actorKey, "vtx."); ok {
		suffix = rest
	}
	outKey := "cap." + suffix
	return map[string]any{"key": outKey, "roleName": row["roleName"]}, map[string]any{"key": outKey}, nil
}

// evalDriftFixture seeds one identity holding one role and returns the
// compiled engine/rule plus the identity's key and vertex props.
func evalDriftFixture(t *testing.T, coreKV, adjKV *substrate.KV, idID, roleID, roleName string) (eng *full.Engine, cr ruleengine.CompiledRule, idKey string, nodeProps map[string]any) {
	t.Helper()
	idKey = "vtx.identity." + idID
	writeCollisionVertex(t, coreKV, idKey, "identity", map[string]any{"name": "actor"})
	writeCollisionVertex(t, coreKV, "vtx.role."+roleID, "role", map[string]any{"name": roleName})
	buildCollisionEdge(t, adjKV, "holdsRole", "identity", idID, "role", roleID)

	eng = full.New()
	var err error
	cr, err = eng.Parse(evalDriftCypher)
	require.NoError(t, err)

	nodeProps = map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	return eng, cr, idKey, nodeProps
}

// TestExecuteFullForActor_InlineRetryConverges proves the design's common
// case: a commit lands in the gap between footprint capture and validation
// (here, a role-name edit — the design's "validate→write gap" risk, §11),
// the first validation catches it, ONE inline re-execution reads the
// converged state, and its own footprint validates clean — so
// executeFullForActor returns the POST-drift row, not the one it first read.
//
// Without footprint validation, this call would return the FIRST
// execution's row — captured (via the hook) before the mutation landed, so
// roleName would read "original", not "updated". Asserting "updated" is
// therefore a real proof the retry ran, not a vacuous assertion.
func TestExecuteFullForActor_InlineRetryConverges(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, healthKV := newCollisionKVs(t)
	ctx := context.Background()

	eng, cr, idKey, nodeProps := evalDriftFixture(t, coreKV, adjKV,
		"Tfp6JdentityFfffffff", "Tfp6RgAaaaaaaaaaaaaa", "original")

	reporter := health.New(healthKV, "rule-drift-retry")
	p := &Pipeline{
		ruleID:                      "rule-drift-retry",
		coreKV:                      coreKV,
		adjKV:                       adjKV,
		engineKind:                  ruleengine.EngineFull,
		fullEngine:                  eng,
		fullCR:                      cr,
		envelopeFn:                  evalDriftEnvelopeFn,
		authPlane:                   true,
		requiresFootprintValidation: true,
		reporter:                    reporter,
	}

	fired := 0
	hookCtx := full.WithFootprintCapturedHook(ctx, func() {
		fired++
		if fired == 1 {
			// A commit lands after the FIRST evaluation's footprint is
			// captured, but before executeFullForActor gets to validate it.
			writeCollisionVertex(t, coreKV, "vtx.role.Tfp6RgAaaaaaaaaaaaaa", "role",
				map[string]any{"name": "updated"})
		}
		// The retry's own evaluation must see a stable footprint — no further
		// mutation on the second (or any later) call.
	})

	results, err := p.executeFullForActor(hookCtx, idKey, nodeProps)
	require.NoError(t, err)
	require.Equal(t, 2, fired, "one initial execution + one retry, no more")
	require.Len(t, results, 1)
	require.Equal(t, "updated", results[0].Row["roleName"],
		"the returned row must reflect the CONVERGED post-drift state, not the one first read")

	entry, gerr := reporter.GetStatus(ctx)
	require.NoError(t, gerr)
	require.Equal(t, uint64(1), entry.EvalDriftRetries)
	require.Equal(t, uint64(0), entry.EvalDriftRequeues)
}

// TestExecuteFullForActor_SustainedDrift_ReturnsErrEvalDrift_NeverEmpty proves
// the design's other stated fail direction: when the read surface keeps
// moving (sustained churn — a commit lands again on every attempt,
// including the one allowed retry), executeFullForActor returns
// failure.ErrEvalDrift and a NIL result slice — never an empty-but-non-nil
// result set, which four downstream consumers (design §4.3: presence-check
// Delete, diff-retraction mass-Delete, empty keyset frame, sweep
// false-convergence) would misread as "the actor has zero rows now".
func TestExecuteFullForActor_SustainedDrift_ReturnsErrEvalDrift_NeverEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, healthKV := newCollisionKVs(t)
	ctx := context.Background()

	eng, cr, idKey, nodeProps := evalDriftFixture(t, coreKV, adjKV,
		"Tfp7JdentityGggggggg", "Tfp7RgAaaaaaaaaaaaaa", "v0")

	reporter := health.New(healthKV, "rule-drift-sustained")
	p := &Pipeline{
		ruleID:                      "rule-drift-sustained",
		coreKV:                      coreKV,
		adjKV:                       adjKV,
		engineKind:                  ruleengine.EngineFull,
		fullEngine:                  eng,
		fullCR:                      cr,
		envelopeFn:                  evalDriftEnvelopeFn,
		authPlane:                   true,
		requiresFootprintValidation: true,
		reporter:                    reporter,
	}

	fired := 0
	hookCtx := full.WithFootprintCapturedHook(ctx, func() {
		fired++
		// A commit lands after EVERY execution's footprint is captured — the
		// role never stops moving, so no attempt's re-read can ever validate.
		writeCollisionVertex(t, coreKV, "vtx.role.Tfp7RgAaaaaaaaaaaaaa", "role",
			map[string]any{"name": "v" + string(rune('0'+fired))})
	})

	results, err := p.executeFullForActor(hookCtx, idKey, nodeProps)
	require.Error(t, err)
	require.ErrorIs(t, err, failure.ErrEvalDrift)
	require.Nil(t, results, "sustained drift must never return an empty-but-non-nil result set")
	require.Equal(t, failure.CatTransient, failure.Classify(err))
	require.Equal(t, 1+maxFootprintRetries, fired, "the first execution plus exactly maxFootprintRetries retries")

	entry, gerr := reporter.GetStatus(ctx)
	require.NoError(t, gerr)
	require.Equal(t, uint64(maxFootprintRetries), entry.EvalDriftRetries)
	require.Equal(t, uint64(1), entry.EvalDriftRequeues)
}

// TestHandle_SustainedDrift_NoAdapterWrite_RequeuesViaRetryQueue drives the
// SAME sustained-churn scenario through the real message-handling entry
// point (p.handle) — the pump path the design's §4.2 seam is meant to cover
// end to end. It asserts on the adapter's recorded call log (the
// negative-test-false-pass discipline CLAUDE.md requires): without footprint
// validation, evaluateForEntryRaw would succeed on its first (already-stale)
// execution and handle() would proceed straight to writeResults, which WOULD
// call adpt.Upsert. With validation, the evaluate stage itself fails with
// failure.ErrEvalDrift before writeResults is ever reached, so the adapter's
// upsert/delete logs must stay empty — and, per the design's routing
// (dispositionEvalErr), the actor is requeued through the SAME re-evaluate
// closure enqueueActorReprojectRetry already uses for a write-stage
// transient failure, not silently dropped via a bare Nak.
func TestHandle_SustainedDrift_NoAdapterWrite_RequeuesViaRetryQueue(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, healthKV := newCollisionKVs(t)
	ctx := context.Background()

	const (
		idID   = "Tfp8JdentityHhhhhhhh"
		roleID = "Tfp8RgAaaaaaaaaaaaaa"
	)
	eng, cr, idKey, _ := evalDriftFixture(t, coreKV, adjKV, idID, roleID, "v0")

	reporter := health.New(healthKV, "rule-drift-handle")
	adpt := &recordingAdapter{}
	retryQueue := failure.NewRetryQueue()

	p := &Pipeline{
		ruleID:                      "rule-drift-handle",
		coreKVBucket:                "CORE",
		coreKV:                      coreKV,
		adjKV:                       adjKV,
		engineKind:                  ruleengine.EngineFull,
		fullEngine:                  eng,
		fullCR:                      cr,
		envelopeFn:                  evalDriftEnvelopeFn,
		authPlane:                   true,
		requiresFootprintValidation: true,
		reporter:                    reporter,
		actorEnumerator:             NewActorEnumerator(adjKV, coreKV, "identity"),
		adpt:                        adpt,
		retryQueue:                  retryQueue,
		retryMaxAttempts:            3,
	}

	fired := 0
	hookCtx := full.WithFootprintCapturedHook(ctx, func() {
		fired++
		writeCollisionVertex(t, coreKV, "vtx.role."+roleID, "role",
			map[string]any{"name": "v" + string(rune('0'+fired))})
	})

	body, merr := json.Marshal(map[string]any{
		"key": idKey, "class": "identity", "isDeleted": false,
		"createdAt": "2026-05-15T10:00:00Z", "lastModifiedAt": "2026-05-15T10:00:00Z",
		"data": map[string]any{},
	})
	require.NoError(t, merr)
	msg := substrate.Message{Subject: "$KV.CORE." + idKey, Body: body, Sequence: 42}

	dec, herr := p.handle(hookCtx, msg)
	require.NoError(t, herr)
	require.Equal(t, substrate.Ack, dec, "a requeued drift is fully disposed — Ack, not left pending")

	require.Empty(t, adpt.upserts, "no blended/stale row may ever reach the adapter")
	require.Empty(t, adpt.deletes)

	require.Equal(t, 1, retryQueue.Len(), "the actor must be requeued through the re-evaluate closure")

	entry, gerr := reporter.GetStatus(ctx)
	require.NoError(t, gerr)
	require.Equal(t, uint64(1), entry.EvalDriftRequeues)
}

// TestExecuteFullForActor_NonAuthPlaneLens_SkipsValidation is the scope-
// predicate control: an actor-aggregate lens that is NOT auth-plane pays no
// validation cost at all — a hook that would otherwise force sustained
// drift is simply never consulted, and the first execution's result stands.
func TestExecuteFullForActor_NonAuthPlaneLens_SkipsValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	eng, cr, idKey, nodeProps := evalDriftFixture(t, coreKV, adjKV,
		"Tfp9JdentityJjjjjjjj", "Tfp9RgAaaaaaaaaaaaaa", "original")

	p := &Pipeline{
		ruleID:     "rule-not-auth-plane",
		coreKV:     coreKV,
		adjKV:      adjKV,
		engineKind: ruleengine.EngineFull,
		fullEngine: eng,
		fullCR:     cr,
		envelopeFn: evalDriftEnvelopeFn,
		authPlane:  false, // the scope predicate's other half
	}

	fired := 0
	hookCtx := full.WithFootprintCapturedHook(ctx, func() {
		fired++
		writeCollisionVertex(t, coreKV, "vtx.role.Tfp9RgAaaaaaaaaaaaaa", "role",
			map[string]any{"name": "updated"})
	})

	results, err := p.executeFullForActor(hookCtx, idKey, nodeProps)
	require.NoError(t, err)
	require.Equal(t, 1, fired, "no retry — validation never ran")
	require.Len(t, results, 1)
	require.Equal(t, "original", results[0].Row["roleName"],
		"a non-auth-plane lens returns its first (unvalidated) execution unchanged")
}
