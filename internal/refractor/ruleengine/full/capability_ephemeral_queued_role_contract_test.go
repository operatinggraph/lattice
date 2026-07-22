// Contract #6 §6.6 conformance for the FR28 role-queue fan-out addition to
// the LITERAL orchestration-base `capabilityEphemeral` cypher — the
// auth-plane assertion the design's adversarial pass calls out: a role
// holder IS granted a queued task's operation; a non-holder is NOT.
package full_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
)

// TestCapabilityEphemeralLens_QueuedRoleFanOut_GrantsHolder: a task queued to
// a role (queuedFor) grants its bound operation to a holder of that role --
// the same ephemeralGrants shape the direct assignedTo path produces.
func TestCapabilityEphemeralLens_QueuedRoleFanOut_GrantsHolder(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := contractStartKVs(t)

	roleKey := contractPutVertex(t, coreKV, "role", "leasingTeam", nil)
	holderKey := contractPutVertex(t, coreKV, "identity", "holder", map[string]any{"name": "holder"})
	opKey := contractPutVertex(t, coreKV, "meta", "approveOp", map[string]any{
		"operationType": "ApproveLeaseApplication",
	})
	targetKey := contractPutVertex(t, coreKV, "leaseApp", "applicant", map[string]any{"state": "pending"})
	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	contractPutVertex(t, coreKV, "task", "qtask", map[string]any{
		"status":    "open",
		"expiresAt": future,
	})

	contractPutEdge(t, adjKV, "holdsRole", "identity", "holder", "role", "leasingTeam")
	contractPutEdge(t, adjKV, "queuedFor", "task", "qtask", "role", "leasingTeam")
	contractPutEdge(t, adjKV, "forOperation", "task", "qtask", "meta", "approveOp")
	contractPutEdge(t, adjKV, "scopedTo", "task", "qtask", "leaseApp", "applicant")

	body := literalCapabilityEphemeralSpec(t)
	eng := full.New()
	cr, err := eng.Parse(body)
	require.NoError(t, err, "literal capabilityEphemeral cypher must parse")

	now := time.Now().UTC().Format(time.RFC3339)
	params := map[string]any{
		"actorKey":    holderKey,
		"now":         now,
		"projectedAt": time.Now().UTC().Format(time.RFC3339),
	}
	out, err := eng.ExecuteWith(context.Background(), cr,
		ruleengine.EventContext{Parameters: params}, adjKV, coreKV)
	require.NoError(t, err, "literal capabilityEphemeral cypher must execute")
	require.Len(t, out, 1, "ephemeral query should produce exactly one row")

	wrapper := ephemeralDescriptor(t).EnvelopeFn("vtx.meta.test-eph-lens-queued",
		func(k string) uint64 { return 1 })
	envRow, _, envErr := wrapper(out[0].Values, out[0].Key, params)
	require.NoError(t, envErr, "ephemeral envelope wrapping must succeed")

	eg, ok := envRow["ephemeralGrants"].([]any)
	require.True(t, ok, "envelope.ephemeralGrants must be an array")
	grantFound := false
	for _, e := range eg {
		m, ok := e.(map[string]any)
		if !ok || m["taskKey"] == nil {
			continue
		}
		grantFound = true
		require.Equal(t, "ApproveLeaseApplication", m["operationType"],
			"role-holder's queued grant must be link-sourced from forOperation")
		require.Equal(t, targetKey, m["target"], "role-holder's queued grant target must be link-sourced from scopedTo")
	}
	require.True(t, grantFound, "a role-holder must be granted the queued task's operation; roleKey=%s opKey=%s", roleKey, opKey)
}

// TestCapabilityEphemeralLens_QueuedRoleFanOut_NonHolderGetsNoGrant: the
// adversarial vector -- an identity that does NOT hold the queued role gets
// NO grant for that task (its ephemeralGrants array carries only degenerate
// null entries, not the queued task's grant).
func TestCapabilityEphemeralLens_QueuedRoleFanOut_NonHolderGetsNoGrant(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := contractStartKVs(t)

	contractPutVertex(t, coreKV, "role", "leasingTeam", nil)
	nonHolderKey := contractPutVertex(t, coreKV, "identity", "nonHolder", map[string]any{"name": "nonHolder"})
	contractPutVertex(t, coreKV, "meta", "approveOp", map[string]any{
		"operationType": "ApproveLeaseApplication",
	})
	contractPutVertex(t, coreKV, "leaseApp", "applicant", map[string]any{"state": "pending"})
	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	contractPutVertex(t, coreKV, "task", "qtask", map[string]any{
		"status":    "open",
		"expiresAt": future,
	})

	// NOTE: "nonHolder" deliberately does NOT get a holdsRole edge to
	// leasingTeam.
	contractPutEdge(t, adjKV, "queuedFor", "task", "qtask", "role", "leasingTeam")
	contractPutEdge(t, adjKV, "forOperation", "task", "qtask", "meta", "approveOp")
	contractPutEdge(t, adjKV, "scopedTo", "task", "qtask", "leaseApp", "applicant")

	body := literalCapabilityEphemeralSpec(t)
	eng := full.New()
	cr, err := eng.Parse(body)
	require.NoError(t, err, "literal capabilityEphemeral cypher must parse")

	params := map[string]any{
		"actorKey":    nonHolderKey,
		"now":         time.Now().UTC().Format(time.RFC3339),
		"projectedAt": time.Now().UTC().Format(time.RFC3339),
	}
	out, err := eng.ExecuteWith(context.Background(), cr,
		ruleengine.EventContext{Parameters: params}, adjKV, coreKV)
	require.NoError(t, err, "literal capabilityEphemeral cypher must execute")
	require.Len(t, out, 1, "ephemeral query should produce exactly one row")

	// A non-holder's row carries only the degenerate {taskKey:null} entry (no
	// real grant), so the envelope wrapper's realness filter finds zero real
	// grants and signals a delete -- the auth-correctness proof: absence, not
	// an empty-but-present grant list, matching Contract #6 §6.8 ("no entry =
	// no access") and the design's adversarial vector (a non-role-holder is
	// NEVER granted a queued task's op).
	_, _, envErr := ephemeralDescriptor(t).EnvelopeFn("vtx.meta.test-eph-lens-queued-non-holder",
		func(k string) uint64 { return 1 })(out[0].Values, out[0].Key, params)
	require.ErrorIs(t, envErr, pipeline.ErrDeleteProjection,
		"a non-role-holder must NEVER be granted the queued task's operation (auth-correctness vector)")
}

// TestCapabilityEphemeralLens_ClaimTask_GrantNarrowsToClaimant is the design
// doc's own flagged pre-build item (fr28-role-queue-fallback-design.md §10):
// "assert the reprojection ordering (a non-claimant's grant must drop before
// or atomically-with the claim's visibility)". It runs the LITERAL
// capabilityEphemeral cypher TWICE against the same graph -- once in the
// pre-claim (queuedFor) state, once in the post-claim (queuedFor tombstoned +
// assignedTo(claimant) created, exactly what ClaimTask's atomic mutation
// batch produces) -- and asserts the grant narrows: the non-claimant loses it
// entirely (no transient double-grant), the claimant keeps it via the direct
// assignedTo branch.
func TestCapabilityEphemeralLens_ClaimTask_GrantNarrowsToClaimant(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := contractStartKVs(t)

	claimantKey := contractPutVertex(t, coreKV, "identity", "claimant", map[string]any{"name": "claimant"})
	otherHolderKey := contractPutVertex(t, coreKV, "identity", "otherHolder", map[string]any{"name": "otherHolder"})
	contractPutVertex(t, coreKV, "role", "leasingTeam", nil)
	contractPutVertex(t, coreKV, "meta", "approveOp", map[string]any{
		"operationType": "ApproveLeaseApplication",
	})
	contractPutVertex(t, coreKV, "leaseApp", "applicant", map[string]any{"state": "pending"})
	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	contractPutVertex(t, coreKV, "task", "qtask", map[string]any{
		"status":    "open",
		"expiresAt": future,
	})

	contractPutEdge(t, adjKV, "holdsRole", "identity", "claimant", "role", "leasingTeam")
	contractPutEdge(t, adjKV, "holdsRole", "identity", "otherHolder", "role", "leasingTeam")
	contractPutEdge(t, adjKV, "queuedFor", "task", "qtask", "role", "leasingTeam")
	contractPutEdge(t, adjKV, "forOperation", "task", "qtask", "meta", "approveOp")
	contractPutEdge(t, adjKV, "scopedTo", "task", "qtask", "leaseApp", "applicant")

	body := literalCapabilityEphemeralSpec(t)
	eng := full.New()
	cr, err := eng.Parse(body)
	require.NoError(t, err, "literal capabilityEphemeral cypher must parse")

	runFor := func(actorKey string) (envRow map[string]any, envErr error) {
		params := map[string]any{
			"actorKey":    actorKey,
			"now":         time.Now().UTC().Format(time.RFC3339),
			"projectedAt": time.Now().UTC().Format(time.RFC3339),
		}
		out, err := eng.ExecuteWith(context.Background(), cr,
			ruleengine.EventContext{Parameters: params}, adjKV, coreKV)
		require.NoError(t, err, "literal capabilityEphemeral cypher must execute")
		require.Len(t, out, 1, "ephemeral query should produce exactly one row")
		envRow, _, envErr = ephemeralDescriptor(t).EnvelopeFn("vtx.meta.test-eph-lens-narrow",
			func(k string) uint64 { return 1 })(out[0].Values, out[0].Key, params)
		return envRow, envErr
	}
	grantedFor := func(envRow map[string]any, envErr error) bool {
		if envErr != nil {
			return false // ErrDeleteProjection == zero real grants
		}
		eg, _ := envRow["ephemeralGrants"].([]any)
		for _, e := range eg {
			if m, ok := e.(map[string]any); ok && m["taskKey"] != nil {
				return true
			}
		}
		return false
	}

	// --- pre-claim: BOTH role-holders are granted via the queued branch ---
	require.True(t, grantedFor(runFor(claimantKey)), "pre-claim: claimant must be granted via the role-queue fan-out")
	require.True(t, grantedFor(runFor(otherHolderKey)), "pre-claim: the other role-holder must ALSO be granted (fan-out to every holder)")

	// --- simulate ClaimTask's atomic mutation batch: tombstone queuedFor,
	// create assignedTo(claimant) -- exactly ddls.go's ClaimTask branch ---
	contractTombstoneEdge(t, adjKV, "queuedFor", "task", "qtask", "role", "leasingTeam")
	contractPutEdge(t, adjKV, "assignedTo", "task", "qtask", "identity", "claimant")

	// --- post-claim: the claimant keeps the grant (direct branch now), the
	// other holder loses it ENTIRELY -- no transient double-grant, no gap ---
	require.True(t, grantedFor(runFor(claimantKey)), "post-claim: the claimant must still be granted (now via the direct assignedTo branch)")
	require.False(t, grantedFor(runFor(otherHolderKey)), "post-claim: the non-claimant's grant must have narrowed away entirely")
}

// contractTombstoneEdge removes a previously-created adjacency edge in both
// directions, simulating a Core KV link tombstone's effect on the Adjacency
// KV (the state a `ClaimTask` commit produces for the retracted `queuedFor`
// link once CDC reprojects it).
func contractTombstoneEdge(t *testing.T, adjKV *substrate.KV, name, fromType, fromName, toType, toName string) {
	t.Helper()
	ctx := context.Background()
	fromID := contractStableID(fromType + ":" + fromName)
	toID := contractStableID(toType + ":" + toName)
	edgeID := name + ":" + fromID + ":" + toID
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: edgeID, EdgeID: edgeID, Name: name,
		Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType,
		IsDeleted: true,
	}))
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: edgeID, EdgeID: edgeID, Name: name,
		Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType,
		IsDeleted: true,
	}))
}

// literalCapabilityEphemeralSpec returns the LITERAL orchestration-base
// capabilityEphemeral lens cypher (Decision #6 — no hand-copied simplified
// rule).
func literalCapabilityEphemeralSpec(t *testing.T) string {
	t.Helper()
	for _, ls := range orchestrationbase.Lenses() {
		if ls.CanonicalName == "capabilityEphemeral" {
			return ls.Spec
		}
	}
	t.Fatal("orchestration-base must declare a capabilityEphemeral lens")
	return ""
}
