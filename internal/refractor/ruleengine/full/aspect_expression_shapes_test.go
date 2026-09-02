package full

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// putAspect writes an aspect body at the Contract #1 aspect key
// vtx.<class>.<id>.<localName>. The body mirrors what a package's
// make_aspect emits (location-domain/ddls.go), so the engine sees the same
// shape it sees on a live stack: envelope fields plus a `data` object.
func putAspect(t testing.TB, reg *fixtureRegistry, kv *substrate.KV, vertexName, localName string, data map[string]any) {
	t.Helper()
	vk := vtxKey(reg, vertexName)
	require.NotEmpty(t, vk, "fixture: %q not registered", vertexName)
	body, err := json.Marshal(map[string]any{
		"class": localName, "isDeleted": false,
		"vertexKey": vk, "localName": localName, "data": data,
	})
	require.NoError(t, err)
	_, err = kv.Put(context.Background(), vk+"."+localName, body)
	require.NoError(t, err)
}

// The two expression shapes edge-manifest's edgeIdentitySpec depends on, and
// which display-name-convention-design.md §3 N3's live tail could not
// separate from a stale compiled rule: the emitted manifest.me row carried
// neither `sealedName` nor an anchor `name`, with the correct spec installed
// and a reprojection demonstrably running. The design names two candidates —
// (a) Refractor executing a rule older than the spec it holds, or (b) the
// engine not resolving these forms — and calls for exactly this test to tell
// them apart before touching the lens or the renderer.
//
//   - an aspect's whole `.data` object in scalar alias position
//     (identity.name.data AS sealedName), where every other corpus use
//     navigates one field deeper to a leaf (…data.value);
//   - a neighbour's aspect hop INSIDE a collect() map literal
//     (collect({name: loc.presentation.data.name})), where the corpus's
//     collected aspect hops are all off the anchor, not off an
//     OPTIONAL MATCH neighbour.
//
// Both shapes route through the single expression evaluator at
// executor.go's resolveProperty call site, so a divergence here would be a
// real engine gap rather than a lens bug.

// TestAspectExpr_WholeDataObject_InScalarAliasPosition: `x.<aspect>.data`
// with no further navigation yields the aspect's whole data object, not
// null. This is the sealedName shape — the { ct, nonce, keyId } envelope is
// the value the edge engine decrypts, so a null here would mean the N3
// self-name could never arrive regardless of the lens.
func TestAspectExpr_WholeDataObject_InScalarAliasPosition(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putAspect(t, reg, coreKV, "alice", "name", map[string]any{
		"ct": "Y2lwaGVy", "nonce": "bm9uY2U", "keyId": "vtx.key.Kk1aaaaaaaaaaaaaaaaa",
	})

	results := parseExec(t, `
MATCH (identity:identity {key: $actorKey})
RETURN
  identity.key AS anchor,
  identity.name.data AS sealedName,
  identity.name.data.value AS displayName
`, ruleengine.EventContext{Parameters: map[string]any{"actorKey": vtxKey(reg, "alice")}},
		adjKV, coreKV)

	require.Len(t, results, 1)
	sealed, ok := results[0].Values["sealedName"].(map[string]any)
	require.True(t, ok, "an aspect's whole .data object must resolve in scalar alias position, got %#v",
		results[0].Values["sealedName"])
	require.Equal(t, "Y2lwaGVy", sealed["ct"])
	require.Equal(t, "vtx.key.Kk1aaaaaaaaaaaaaaaaa", sealed["keyId"])
	require.Nil(t, results[0].Values["displayName"],
		"a sealed name has no plaintext .value — this is why N3 projects the envelope")
}

// TestAspectExpr_NeighbourAspectHop_InsideCollect: an OPTIONAL MATCH
// neighbour's aspect field resolves inside a collect() map literal. This is
// the anchors shape — {key, name, container, containerName} where name and
// containerName are aspect hops off two different neighbours.
func TestAspectExpr_NeighbourAspectHop_InsideCollect(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	putVertex(t, reg, coreKV, "unit1", "unit", nil)
	putVertex(t, reg, coreKV, "bldg", "building", nil)
	putAspect(t, reg, coreKV, "unit1", "presentation", map[string]any{"name": "Unit 1"})
	putAspect(t, reg, coreKV, "bldg", "presentation", map[string]any{"name": "Riverside Building"})
	putEdge(t, reg, adjKV, "residesIn", "alice", "unit1")
	putEdge(t, reg, adjKV, "containedIn", "unit1", "bldg")

	results := parseExec(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc)
OPTIONAL MATCH (loc)-[:containedIn]->(container)
RETURN
  identity.key AS anchor,
  collect(DISTINCT {key: loc.key, name: loc.presentation.data.name, container: container.key, containerName: container.presentation.data.name}) AS anchors
`, ruleengine.EventContext{Parameters: map[string]any{"actorKey": vtxKey(reg, "alice")}},
		adjKV, coreKV)

	require.Len(t, results, 1)
	anchors, ok := results[0].Values["anchors"].([]any)
	require.True(t, ok, "anchors must collect, got %#v", results[0].Values["anchors"])
	require.Len(t, anchors, 1)
	entry, ok := anchors[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, vtxKey(reg, "unit1"), entry["key"])
	require.Equal(t, "Unit 1", entry["name"],
		"a neighbour's aspect hop must resolve inside a collect map literal")
	require.Equal(t, "Riverside Building", entry["containerName"],
		"a second-hop neighbour's aspect must resolve inside the same map literal")
}

// The per-target recorded-lapse shape: an aspect whose `data` carries a
// `byTarget` map keyed by weaver-target id, read four navigation hops deep as
// `x.<aspect>.data.byTarget.<targetId>`. One hop resolves the aspect, the rest
// is ordinary map navigation (values.go's resolveProperty/propertyOf), and the
// grammar bounds the lookup chain at nothing — but the corpus's deepest shipped
// read is `x.<aspect>.data.<leaf>`, so the depth is pinned here rather than
// argued from the resolver.
//
// The three positions a convergence lens puts the read in are pinned together
// because they route through different evaluator arms: a scalar alias, an
// ordering comparison against another aspect's leaf, and the CASE/NOT forms a
// freshUntil column and a freshness (rather than lapse) predicate need.
const (
	expiryMarkerTargetID = "appointmentReminders"
	expiryMarkerLapsed   = "2026-06-18T14:00:00Z"
	expiryMarkerDeadline = "2026-06-18T13:00:00Z"
)

// TestAspectExpr_ByTargetMapRead_ScalarAliasAndComparison pins the four-deep
// read in a scalar alias, inside a `>=` comparison against another aspect's
// leaf, inside `CASE WHEN <that comparison> THEN null ELSE <deadline> END`, and
// under `NOT (<comparison>)` — the negation form the corpus already uses
// (visitseries.go's exclusion), never `= False`.
func TestAspectExpr_ByTargetMapRead_ScalarAliasAndComparison(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "appt", "appointment", nil)
	putAspect(t, reg, coreKV, "appt", "freshnessExpiry", map[string]any{
		"expiredAt": expiryMarkerLapsed,
		"byTarget":  map[string]any{expiryMarkerTargetID: expiryMarkerLapsed},
	})
	putAspect(t, reg, coreKV, "appt", "schedule", map[string]any{"remindAt": expiryMarkerDeadline})

	results := parseExec(t, `
MATCH (a:appointment {key: $anchorKey})
RETURN
  a.key AS anchor,
  a.freshnessExpiry.data.byTarget.`+expiryMarkerTargetID+` AS recordedLapse,
  (a.freshnessExpiry.data.byTarget.`+expiryMarkerTargetID+` >= a.schedule.data.remindAt) AS lapsed,
  (CASE WHEN (a.freshnessExpiry.data.byTarget.`+expiryMarkerTargetID+` >= a.schedule.data.remindAt) THEN null ELSE a.schedule.data.remindAt END) AS freshUntil,
  NOT (a.freshnessExpiry.data.byTarget.`+expiryMarkerTargetID+` >= a.schedule.data.remindAt) AS fresh
`, ruleengine.EventContext{Parameters: map[string]any{"anchorKey": vtxKey(reg, "appt")}},
		adjKV, coreKV)

	require.Len(t, results, 1)
	require.Equal(t, expiryMarkerLapsed, results[0].Values["recordedLapse"],
		"a four-deep aspect read (aspect → data → byTarget → targetId) must resolve in scalar alias position")
	require.Equal(t, true, results[0].Values["lapsed"],
		"a recorded lapse at or after the deadline must compare true")
	require.Nil(t, results[0].Values["freshUntil"],
		"a lapsed row's freshUntil is null — the CASE arm the timer reads")
	require.Equal(t, false, results[0].Values["fresh"],
		"NOT over the lapse comparison is the freshness polarity, and it must invert")
}

// TestAspectExpr_ByTargetMapRead_DeadlineNotYetReached pins the other side of
// the comparison with both operands present: a recorded lapse for a target
// whose deadline has since moved past it reads not-lapsed, and freshUntil
// carries the deadline verbatim. This is the re-arm vector — no clearing write
// is involved, the deadline simply overtakes the recorded instant.
func TestAspectExpr_ByTargetMapRead_DeadlineNotYetReached(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "appt", "appointment", nil)
	putAspect(t, reg, coreKV, "appt", "freshnessExpiry", map[string]any{
		"expiredAt": expiryMarkerDeadline,
		"byTarget":  map[string]any{expiryMarkerTargetID: expiryMarkerDeadline},
	})
	putAspect(t, reg, coreKV, "appt", "schedule", map[string]any{"remindAt": expiryMarkerLapsed})

	results := parseExec(t, `
MATCH (a:appointment {key: $anchorKey})
RETURN
  (a.freshnessExpiry.data.byTarget.`+expiryMarkerTargetID+` >= a.schedule.data.remindAt) AS lapsed,
  (CASE WHEN (a.freshnessExpiry.data.byTarget.`+expiryMarkerTargetID+` >= a.schedule.data.remindAt) THEN null ELSE a.schedule.data.remindAt END) AS freshUntil,
  NOT (a.freshnessExpiry.data.byTarget.`+expiryMarkerTargetID+` >= a.schedule.data.remindAt) AS fresh
`, ruleengine.EventContext{Parameters: map[string]any{"anchorKey": vtxKey(reg, "appt")}},
		adjKV, coreKV)

	require.Len(t, results, 1)
	require.Equal(t, false, results[0].Values["lapsed"],
		"a recorded lapse strictly before the deadline must compare false")
	require.Equal(t, expiryMarkerLapsed, results[0].Values["freshUntil"],
		"an unlapsed row's freshUntil carries the deadline verbatim — what arms the timer")
	require.Equal(t, true, results[0].Values["fresh"])
}

// TestAspectExpr_ByTargetMapRead_OtherTargetsEntryIsIsolated pins the whole
// reason the marker is keyed per target: a lapse recorded for a SIBLING target
// on the same anchor must not read as this target's lapse.
func TestAspectExpr_ByTargetMapRead_OtherTargetsEntryIsIsolated(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "appt", "appointment", nil)
	putAspect(t, reg, coreKV, "appt", "freshnessExpiry", map[string]any{
		"expiredAt": expiryMarkerLapsed,
		"byTarget":  map[string]any{"pastDueAppointments": expiryMarkerLapsed},
	})
	putAspect(t, reg, coreKV, "appt", "schedule", map[string]any{"remindAt": expiryMarkerDeadline})

	results := parseExec(t, `
MATCH (a:appointment {key: $anchorKey})
RETURN
  a.freshnessExpiry.data.byTarget.`+expiryMarkerTargetID+` AS recordedLapse,
  (a.freshnessExpiry.data.byTarget.`+expiryMarkerTargetID+` >= a.schedule.data.remindAt) AS lapsed,
  NOT (a.freshnessExpiry.data.byTarget.`+expiryMarkerTargetID+` >= a.schedule.data.remindAt) AS fresh
`, ruleengine.EventContext{Parameters: map[string]any{"anchorKey": vtxKey(reg, "appt")}},
		adjKV, coreKV)

	require.Len(t, results, 1)
	require.Nil(t, results[0].Values["recordedLapse"],
		"a sibling target's byTarget entry must not resolve under this target's key")
	require.Equal(t, false, results[0].Values["lapsed"],
		"a sibling target's lapse must not open this target's gap — the isolation the byTarget keying buys")
	require.Equal(t, true, results[0].Values["fresh"])
}

// TestAspectExpr_ByTargetMapRead_MarkerWithNoByTargetMap is the migration
// window: a marker aspect carrying `expiredAt` alone, with no `byTarget` object
// at all. The four-deep read must resolve nil at the THIRD hop — never erroring,
// and never falling through to the sibling scalar — so the comparison is false
// and such an anchor reads not-expired until a fire records its own entry.
func TestAspectExpr_ByTargetMapRead_MarkerWithNoByTargetMap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "appt", "appointment", nil)
	putAspect(t, reg, coreKV, "appt", "freshnessExpiry", map[string]any{"expiredAt": expiryMarkerLapsed})
	putAspect(t, reg, coreKV, "appt", "schedule", map[string]any{"remindAt": expiryMarkerDeadline})

	results := parseExec(t, `
MATCH (a:appointment {key: $anchorKey})
RETURN
  a.freshnessExpiry.data.expiredAt AS entityWide,
  a.freshnessExpiry.data.byTarget.`+expiryMarkerTargetID+` AS recordedLapse,
  (a.freshnessExpiry.data.byTarget.`+expiryMarkerTargetID+` >= a.schedule.data.remindAt) AS lapsed,
  NOT (a.freshnessExpiry.data.byTarget.`+expiryMarkerTargetID+` >= a.schedule.data.remindAt) AS fresh
`, ruleengine.EventContext{Parameters: map[string]any{"anchorKey": vtxKey(reg, "appt")}},
		adjKV, coreKV)

	require.Len(t, results, 1)
	require.Equal(t, expiryMarkerLapsed, results[0].Values["entityWide"],
		"the sibling scalar is present — this vector is about the missing map, not a missing aspect")
	require.Nil(t, results[0].Values["recordedLapse"],
		"a missing byTarget object must resolve nil at that hop, never fall through to the sibling scalar")
	require.Equal(t, false, results[0].Values["lapsed"],
		"an entity-wide expiredAt with no per-target entry must NOT open this target's gap")
	require.Equal(t, true, results[0].Values["fresh"])
}

// TestAspectExpr_ByTargetMapRead_NilSemantics pins compareAny's nil-false for
// both operands of the four-deep read — the default the converted lenses rest
// on. With the marker aspect entirely absent the row is not expired (matching
// today's `deadline <= $now` before the deadline), and the negated form reads
// fresh; with the deadline absent the comparison is false in the other
// direction too, byte-identical to today.
func TestAspectExpr_ByTargetMapRead_NilSemantics(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	// No freshnessExpiry aspect at all: nothing has ever fired for this anchor.
	putVertex(t, reg, coreKV, "appt", "appointment", nil)
	putAspect(t, reg, coreKV, "appt", "schedule", map[string]any{"remindAt": expiryMarkerDeadline})
	// A second anchor carrying the marker but no deadline aspect.
	putVertex(t, reg, coreKV, "noDeadline", "appointment", nil)
	putAspect(t, reg, coreKV, "noDeadline", "freshnessExpiry", map[string]any{
		"expiredAt": expiryMarkerLapsed,
		"byTarget":  map[string]any{expiryMarkerTargetID: expiryMarkerLapsed},
	})

	noMarker := parseExec(t, `
MATCH (a:appointment {key: $anchorKey})
RETURN
  a.freshnessExpiry.data.byTarget.`+expiryMarkerTargetID+` AS recordedLapse,
  (a.freshnessExpiry.data.byTarget.`+expiryMarkerTargetID+` >= a.schedule.data.remindAt) AS lapsed,
  (CASE WHEN (a.freshnessExpiry.data.byTarget.`+expiryMarkerTargetID+` >= a.schedule.data.remindAt) THEN null ELSE a.schedule.data.remindAt END) AS freshUntil,
  NOT (a.freshnessExpiry.data.byTarget.`+expiryMarkerTargetID+` >= a.schedule.data.remindAt) AS fresh
`, ruleengine.EventContext{Parameters: map[string]any{"anchorKey": vtxKey(reg, "appt")}},
		adjKV, coreKV)

	require.Len(t, noMarker, 1)
	require.Nil(t, noMarker[0].Values["recordedLapse"],
		"an absent marker aspect binds nil rather than dropping the row")
	require.Equal(t, false, noMarker[0].Values["lapsed"],
		"nil >= deadline must be FALSE — a never-fired anchor is not expired")
	require.Equal(t, expiryMarkerDeadline, noMarker[0].Values["freshUntil"],
		"with no marker the CASE takes the ELSE arm and the deadline arms the timer")
	require.Equal(t, true, noMarker[0].Values["fresh"],
		"NOT(nil-false) is TRUE — a never-lapsed anchor reads fresh, the polarity the freshness family needs")

	noDeadline := parseExec(t, `
MATCH (a:appointment {key: $anchorKey})
RETURN
  (a.freshnessExpiry.data.byTarget.`+expiryMarkerTargetID+` >= a.schedule.data.remindAt) AS lapsed,
  NOT (a.freshnessExpiry.data.byTarget.`+expiryMarkerTargetID+` >= a.schedule.data.remindAt) AS fresh
`, ruleengine.EventContext{Parameters: map[string]any{"anchorKey": vtxKey(reg, "noDeadline")}},
		adjKV, coreKV)

	require.Len(t, noDeadline, 1)
	require.Equal(t, false, noDeadline[0].Values["lapsed"],
		"marker >= nil must be FALSE — no stored deadline, no gap")
	require.Equal(t, true, noDeadline[0].Values["fresh"])
}
