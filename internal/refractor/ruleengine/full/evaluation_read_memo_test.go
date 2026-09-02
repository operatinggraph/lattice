package full

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// newTestExecutor builds an executor over the given KVs with the same memo
// wiring ExecuteWith installs, so these tests exercise the production shape
// rather than a bare struct literal. adjKV may be nil for a test that never
// traverses a relationship.
func newTestExecutor(adjKV, coreKV *substrate.KV) *executor {
	return &executor{
		ctx:           context.Background(),
		adjKV:         adjKV,
		coreKV:        coreKV,
		nodes:         map[string]*nodeRef{},
		edges:         map[string][]adjacency.EdgeEntry{},
		edgeRevisions: map[string]uint64{},
		hubEdges:      map[hubKey][]adjacency.EdgeEntry{},
		hubReadScope:  DefaultHubReadScopeMode() == HubReadScopeModeOn,
	}
}

// TestExec_AspectReadIsRepeatableWithinOneEvaluation pins the executor's
// repeatable-read contract: once an evaluation has observed a key, every later
// access inside that SAME evaluation observes the same value, even though the
// underlying Core KV entry has since been overwritten by a concurrent commit.
//
// This is the property that makes an actor-aggregate projection well-formed.
// An aspect hop resolves as a live point-read, and projectItems evaluates each
// non-aggregate WITH column once per binding row, so one grouping column is
// read once per row of the OPTIONAL MATCH cross-product. If those reads could
// disagree, one anchor would split into two groups and the pipeline's
// output-key collision guard would fail the actor closed — dropping the
// projection update rather than writing a half-result. Repeatable-read makes
// that split structurally impossible: a group splits only when the same
// expression yields two values, and the same expression over the same node is
// always the same Core KV key.
//
// The mutation here stands in for a Processor commit landing mid-evaluation
// (in the live defect, a landlord approval flipping a unit's listing from
// available to leased between two rows of one cypher run). Driving it as an
// explicit write keeps the proof deterministic — no concurrency, no sleep.
func TestExec_AspectReadIsRepeatableWithinOneEvaluation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	_, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	unit := putVertex(t, reg, coreKV, "unit", "unit", nil)
	putAspect(t, reg, coreKV, "unit", "listing", map[string]any{"status": "available"})

	ex := newTestExecutor(nil, coreKV)
	unitRef, err := ex.fetchNode(unit)
	require.NoError(t, err)
	require.NotNil(t, unitRef)

	first, err := ex.resolveProperty(unitRef, "listing")
	require.NoError(t, err)
	require.Equal(t, "available", aspectStatus(t, first))

	// A commit lands mid-evaluation.
	putAspect(t, reg, coreKV, "unit", "listing", map[string]any{"status": "leased"})

	second, err := ex.resolveProperty(unitRef, "listing")
	require.NoError(t, err)
	require.Equal(t, "available", aspectStatus(t, second),
		"a second access inside ONE evaluation must observe the value the evaluation already saw")

	// The memo is evaluation-scoped, never global: the NEXT evaluation must see
	// the committed value, or the read model would never catch up.
	next := newTestExecutor(nil, coreKV)
	nextUnit, err := next.fetchNode(unit)
	require.NoError(t, err)
	fresh, err := next.resolveProperty(nextUnit, "listing")
	require.NoError(t, err)
	require.Equal(t, "leased", aspectStatus(t, fresh),
		"a fresh evaluation must observe the committed value")
}

// TestExec_AbsentAspectStaysAbsentWithinOneEvaluation is the negative arm: an
// absence observed once is stable for the rest of the evaluation. An
// absent-then-created aspect splits a grouping key exactly as a changed one
// does (the live defect's freshUntil column flipped null → value that way), so
// memoizing only the hits would leave half the defect alive.
func TestExec_AbsentAspectStaysAbsentWithinOneEvaluation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	_, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	unit := putVertex(t, reg, coreKV, "unit", "unit", nil)

	ex := newTestExecutor(nil, coreKV)
	unitRef, err := ex.fetchNode(unit)
	require.NoError(t, err)

	absent, err := ex.resolveProperty(unitRef, "listing")
	require.NoError(t, err)
	require.Nil(t, absent, "the aspect does not exist yet")

	putAspect(t, reg, coreKV, "unit", "listing", map[string]any{"status": "available"})

	stillAbsent, err := ex.resolveProperty(unitRef, "listing")
	require.NoError(t, err)
	require.Nil(t, stillAbsent,
		"an absence observed inside ONE evaluation must stay absent for that evaluation")

	next := newTestExecutor(nil, coreKV)
	nextUnit, err := next.fetchNode(unit)
	require.NoError(t, err)
	present, err := next.resolveProperty(nextUnit, "listing")
	require.NoError(t, err)
	require.Equal(t, "available", aspectStatus(t, present),
		"a fresh evaluation must observe the created aspect")
}

// TestExec_OneRowPerAnchorWhenAspectColumnGroupsAcrossAFan is the outcome-level
// arm at the shape the live lens uses: a non-aggregate aspect column grouping
// alongside an aggregate, over a fan of neighbours (so the cross-product
// carries several binding rows). One anchor must yield exactly one row.
func TestExec_OneRowPerAnchorWhenAspectColumnGroupsAcrossAFan(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	unit := putVertex(t, reg, coreKV, "unit", "unit", nil)
	putAspect(t, reg, coreKV, "unit", "listing", map[string]any{"status": "available"})
	for _, name := range []string{"svcA", "svcB", "svcC"} {
		putVertex(t, reg, coreKV, name, "service", nil)
		putEdge(t, reg, adjKV, "providedTo", name, "unit")
	}

	out := parseExec(t, `
MATCH (u:unit {key: $actorKey})
OPTIONAL MATCH (u)<-[:providedTo]-(inst:service)
WITH u.key AS entityKey, u.listing.data.status AS unitStatus,
  count(DISTINCT inst.key) AS instCount
RETURN entityKey, unitStatus, instCount
`, ruleengine.EventContext{Parameters: map[string]any{"actorKey": unit}}, adjKV, coreKV)

	require.Len(t, out, 1, "one anchor must project exactly one row")
	require.Equal(t, "available", out[0].Values["unitStatus"])
	require.EqualValues(t, 3, out[0].Values["instCount"])
}

// aspectStatus digs data.status out of a resolved aspect body (nil-safe).
func aspectStatus(t *testing.T, aspect any) string {
	t.Helper()
	props, ok := aspect.(map[string]any)
	require.Truef(t, ok, "expected an aspect body, got %T", aspect)
	data, ok := props["data"].(map[string]any)
	require.True(t, ok, "aspect body must carry data")
	s, _ := data["status"].(string)
	return s
}
