package pipeline

// The two sibling arms of the anchor-type under-approximation
// (auth-plane-projection-latency-design.md §18.1, second bullet): a VERTEX event
// whose type is the actor type, and that vertex's TOMBSTONE. Both answer for the
// event's own anchor and, before this, for nobody else — so on a lens binding the
// actor type at a non-anchor position the peer anchor kept a row derived from a
// vertex that had changed or vanished. The tombstone case fails in the over-grant
// direction, which §4.7 names as the one that must never happen.
//
// The fixture's lens renders a property of `report`, an identity bound at a
// non-anchor position — the reduced form of `capabilityEphemeral`'s
// (identity)<-[:reportsTo]-(report:identity) 2-hop. Every assertion here is on
// what the ADAPTER saw, so a regression fails on the writes rather than on a
// derived set.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// reportsToEnvelopeFn is the anchor-keyed capability shape, carrying the
// non-anchor identity position's rendered property through so a test can assert
// the peer's row really moved rather than merely being rewritten.
func reportsToEnvelopeFn(row, _, params map[string]any) (map[string]any, map[string]any, error) {
	actorKey, _ := row["actorKey"].(string)
	if actorKey == "" {
		actorKey, _ = params["actorKey"].(string)
	}
	outKey := "cap." + strings.TrimPrefix(actorKey, "vtx.")
	return map[string]any{"key": outKey, "reportName": row["reportName"]}, map[string]any{"key": outKey}, nil
}

type reportsToFixture struct {
	t      *testing.T
	p      *Pipeline
	adpt   *recordingAdapter
	coreKV *substrate.KV
	adjKV  *substrate.KV
	ids    map[string]string
	names  map[string]string
}

// declineWhenNoReportEnvelopeFn models `capabilityEphemeral`'s envelope: an
// anchor whose whole row was delegated through a vertex that has since gone
// declines with ErrDeleteProjection, and executeFullForActorOnce turns that into
// a Delete against the anchor's own key. It is the second of the four
// peer-Delete paths, and the one the headline lens really takes.
func declineWhenNoReportEnvelopeFn(row, keys, params map[string]any) (map[string]any, map[string]any, error) {
	outRow, outKeys, err := reportsToEnvelopeFn(row, keys, params)
	if err != nil {
		return nil, nil, err
	}
	if outRow["reportName"] == nil {
		return nil, outKeys, ErrDeleteProjection
	}
	return outRow, outKeys, nil
}

func newReportsToFixture(t *testing.T) *reportsToFixture {
	t.Helper()
	return newReportsToFixtureWith(t, reportsToEnvelopeFn)
}

func newReportsToFixtureWith(t *testing.T, envFn EnvelopeFn) *reportsToFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	eng := full.New()
	cr, err := eng.Parse(reportsToSpec)
	require.NoError(t, err)

	adpt := &recordingAdapter{}
	p := &Pipeline{
		ruleID:               "reports-to-peer",
		coreKVBucket:         "CORE",
		coreKV:               coreKV,
		adjKV:                adjKV,
		engineKind:           ruleengine.EngineFull,
		envelopeFn:           envFn,
		plainReprojectLabels: map[string]struct{}{"identity": {}},
		patternClosedOutput:  true,
		adpt:                 adpt,
	}
	p.UseFullEngine(eng, cr)
	p.SetActorEnumerator(NewActorEnumerator(adjKV, coreKV, "identity"))
	p.SetSweepPlan(SweepPlan{AnchorType: "identity", KeyPrefix: "cap.identity."})

	f := &reportsToFixture{
		t: t, p: p, adpt: adpt, coreKV: coreKV, adjKV: adjKV,
		ids: map[string]string{}, names: map[string]string{},
	}
	f.vertex("report", "Rhea")
	f.vertex("manager", "Mika")
	f.edge("reportsTo", "report", "manager")
	return f
}

func (f *reportsToFixture) vertex(name, displayName string) {
	f.t.Helper()
	id, err := substrate.NewNanoID()
	require.NoError(f.t, err)
	f.ids[name] = id
	f.names[name] = displayName
	writeCollisionVertex(f.t, f.coreKV, substrate.VertexKey("identity", id), "identity",
		map[string]any{"name": displayName})
}

func (f *reportsToFixture) key(name string) string {
	return substrate.VertexKey("identity", f.ids[name])
}

func (f *reportsToFixture) edge(rel, from, to string) {
	f.t.Helper()
	fromID, toID := f.ids[from], f.ids[to]
	linkKey := "lnk.identity." + fromID + "." + rel + ".identity." + toID
	for _, evt := range []adjacency.CoreKVEvent{
		{CoreKvKey: linkKey, EdgeID: linkKey, Name: rel, Direction: "outbound",
			NodeID: fromID, OtherNodeID: toID, OtherType: "identity"},
		{CoreKvKey: linkKey, EdgeID: linkKey, Name: rel, Direction: "inbound",
			NodeID: toID, OtherNodeID: fromID, OtherType: "identity"},
	} {
		require.NoError(f.t, adjacency.Build(context.Background(), f.adjKV, evt))
	}
}

// handleVertex writes name's vertex root to Core KV and drives the resulting CDC
// message through the real message path, so the arm's own relevance gate,
// enumeration and write loop all run as they do in production.
func (f *reportsToFixture) handleVertex(name string, deleted bool, seq uint64) {
	f.t.Helper()
	key := f.key(name)
	body, err := json.Marshal(map[string]any{
		"key": key, "class": "identity", "isDeleted": deleted,
		"createdAt": "2026-05-15T10:00:00Z", "lastModifiedAt": "2026-05-15T10:00:00Z",
		"data": map[string]any{"name": f.names[name]},
	})
	require.NoError(f.t, err)
	_, err = f.coreKV.Put(context.Background(), key, body)
	require.NoError(f.t, err)

	dec, err := f.p.handle(context.Background(), substrate.Message{
		Subject: "$KV.CORE." + key, Body: body, Sequence: seq})
	require.NoError(f.t, err)
	require.Equal(f.t, substrate.Ack, dec)
}

// rename changes name's rendered property in Core KV without emitting an event,
// so a later event on that vertex has something the peer's row must pick up.
func (f *reportsToFixture) rename(name, to string) {
	f.t.Helper()
	f.names[name] = to
}

func (f *reportsToFixture) forget() { f.adpt.upserts, f.adpt.deletes = nil, nil }

func (f *reportsToFixture) upsertedKeys() []string {
	out := make([]string, 0, len(f.adpt.upserts))
	for _, w := range f.adpt.upserts {
		k, _ := w.keys["key"].(string)
		out = append(out, k)
	}
	return out
}

func (f *reportsToFixture) deletedKeys() []string {
	out := make([]string, 0, len(f.adpt.deletes))
	for _, w := range f.adpt.deletes {
		k, _ := w.keys["key"].(string)
		out = append(out, k)
	}
	return out
}

func (f *reportsToFixture) rowFor(name string) map[string]any {
	want := capKeyFor(f.key(name))
	for i := len(f.adpt.upserts) - 1; i >= 0; i-- {
		if k, _ := f.adpt.upserts[i].keys["key"].(string); k == want {
			return f.adpt.upserts[i].row
		}
	}
	return nil
}

// TestPeerAnchor_ReportTombstoneReprojectsTheManager is the headline. Soft-
// deleting the report retracts the report's own row AND reprojects the manager's
// — whose row rendered a property of the vertex that just vanished. Without the
// peer arm the manager keeps rendering a report that no longer exists, which on
// the auth plane is a grant outliving its source.
func TestPeerAnchor_ReportTombstoneReprojectsTheManager(t *testing.T) {
	f := newReportsToFixture(t)

	// Ground truth first: the manager's row really does carry the report.
	f.handleVertex("manager", false, 1)
	require.Equal(t, "Rhea", f.rowFor("manager")["reportName"],
		"the fixture must have a manager row that depends on the report")
	f.forget()

	f.handleVertex("report", true, 2)

	require.Contains(t, f.deletedKeys(), capKeyFor(f.key("report")),
		"the tombstoned actor's own projection is still retracted")
	require.Contains(t, f.upsertedKeys(), capKeyFor(f.key("manager")),
		"the manager binds the tombstoned identity at a non-anchor position")
	require.Nil(t, f.rowFor("manager")["reportName"],
		"the manager's reprojected row must no longer render the vanished report")
}

// TestPeerAnchor_ManagerIsReprojectedNeverDeleted is the mass-retraction inverse.
// The peer's own vertex is intact; only a neighbour of it went away. A shape that
// deleted everything the walk returned would trade a missed retraction for a
// mass one, which is the worse failure.
func TestPeerAnchor_ManagerIsReprojectedNeverDeleted(t *testing.T) {
	f := newReportsToFixture(t)
	f.handleVertex("report", true, 1)

	require.NotContains(t, f.deletedKeys(), capKeyFor(f.key("manager")),
		"a peer anchor is reprojected, never retracted — its own vertex is intact")
	require.Equal(t, []string{capKeyFor(f.key("report"))}, f.deletedKeys(),
		"exactly one row is retracted: the tombstoned actor's own")
}

// TestPeerAnchor_DecliningPeerGetsTheDeleteItEarned is the positive vector the
// assertion above needs. That test's fixture never declines, runs with
// zeroRowRetraction false and multiEnvelopeFn nil, so three of the four
// peer-Delete paths are unreachable in it and its NotContains could hold for the
// wrong reason. Here the peer's envelope DOES decline — the shape
// `capabilityEphemeral` takes for a manager whose every grant was delegated
// through the report that just vanished — and the Delete that lands is the
// manager's own correct retraction, not a mass one: it comes from the manager's
// freshly re-executed row set, and the report still gets exactly one Delete of
// its own.
func TestPeerAnchor_DecliningPeerGetsTheDeleteItEarned(t *testing.T) {
	f := newReportsToFixtureWith(t, declineWhenNoReportEnvelopeFn)

	f.handleVertex("manager", false, 1)
	require.Equal(t, "Rhea", f.rowFor("manager")["reportName"],
		"while the report is live the manager's envelope does not decline")
	require.Empty(t, f.deletedKeys())
	f.forget()

	f.handleVertex("report", true, 2)

	require.ElementsMatch(t, []string{
		capKeyFor(f.key("report")), capKeyFor(f.key("manager")),
	}, f.deletedKeys(),
		"the report's tombstone retracts its own row; the manager's declining envelope retracts the manager's")
	require.Empty(t, f.upsertedKeys(),
		"a declining peer writes no row — the Delete is the whole of its reprojection")
}

// TestPeerAnchor_ReportVertexEditReprojectsTheManager is the vertex-arm twin: a
// live property edit on the report, not a tombstone. The manager renders that
// property, so its row moves on an event that never named it.
func TestPeerAnchor_ReportVertexEditReprojectsTheManager(t *testing.T) {
	f := newReportsToFixture(t)
	f.handleVertex("manager", false, 1)
	require.Equal(t, "Rhea", f.rowFor("manager")["reportName"])
	f.forget()

	f.rename("report", "Rhea Okonjo")
	f.handleVertex("report", false, 2)

	require.Contains(t, f.upsertedKeys(), capKeyFor(f.key("manager")))
	require.Equal(t, "Rhea Okonjo", f.rowFor("manager")["reportName"],
		"the manager's row must pick up the renamed report")
	require.Empty(t, f.deletedKeys(), "a live edit retracts nothing")
}

// TestPeerAnchor_TombstoneReachesTheManagerThroughTheEnumerator drives the same
// event with the derivation switched off, so the ActorEnumerator seam decides
// rather than the pattern-directed walk. It is not a hypothetical arm: thirty of
// the corpus's fifty-four actor-aware cyphers take the walk — eighteen personal
// lenses, which never receive a sweep plan and so must keep the accidental heal;
// five whose index still refuses after Increment 4a-1; and seven binding the
// actor type off-anchor. `capabilityServiceAccess` is among the refusals
// permanently, on `containedIn*0..`. internal/refractor's
// actor_onekey_corpus_census_test.go derives the split; every one of those
// thirty reaches the enumerator on every event.
func TestPeerAnchor_TombstoneReachesTheManagerThroughTheEnumerator(t *testing.T) {
	f := newReportsToFixture(t)
	f.p.SetAnchorDerivationMode(DerivationModeOff)

	f.handleVertex("report", true, 1)

	require.Contains(t, f.upsertedKeys(), capKeyFor(f.key("manager")),
		"the enumerator's walk from the tombstoned identity must reach the manager")
	require.Equal(t, []string{capKeyFor(f.key("report"))}, f.deletedKeys())
}

// TestPeerAnchor_KnobOffSuppressesThePeerArm pins the operator's way back on the
// arm that actually walks. enumerateAnchors has its own knob test; this one
// proves the peer arm consults the same switch, so `off` really does restore the
// prior behaviour end to end rather than half of it.
func TestPeerAnchor_KnobOffSuppressesThePeerArm(t *testing.T) {
	f := newReportsToFixture(t)
	f.p.SetActorPeerAnchorMode(PeerAnchorModeOff)

	f.handleVertex("report", true, 1)

	require.Equal(t, []string{capKeyFor(f.key("report"))}, f.deletedKeys(),
		"the tombstoned actor's own retraction is unaffected by the knob")
	require.Empty(t, f.upsertedKeys(),
		"with the knob off the manager is not reached — the known under-approximation, reinstated on purpose")
}

// TestPeerAnchor_SingleActorPositionLensReprojectsNobodyElse is the control that
// keeps the two arms honest: on a lens binding the actor type only at the anchor,
// the peer walk answers "nobody" and the arms behave exactly as they did. If this
// ever fails, the widening escaped the population it was proven for.
func TestPeerAnchor_SingleActorPositionLensReprojectsNobodyElse(t *testing.T) {
	f := newCoHolderFixture(t)

	body, err := json.Marshal(map[string]any{
		"key": f.key("alice"), "class": "identity", "isDeleted": false,
		"createdAt": "2026-05-15T10:00:00Z", "lastModifiedAt": "2026-05-15T10:00:00Z",
		"data": map[string]any{"name": "alice-renamed"},
	})
	require.NoError(t, err)
	_, err = f.p.coreKV.Put(context.Background(), f.key("alice"), body)
	require.NoError(t, err)
	dec, err := f.p.handle(context.Background(), substrate.Message{
		Subject: "$KV.CORE." + f.key("alice"), Body: body, Sequence: 1})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)

	require.Equal(t, []string{capKeyFor(f.key("alice"))}, f.writtenActors(),
		"capabilityRoles binds identity only at the anchor — a co-holder's row cannot move")
}
