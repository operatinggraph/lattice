package maintenancedomain

// Rule-engine proof of the workOrderResolvedNotices convergence cypher — the
// order-anchored gap that tells a reporter once that someone else resolved
// their order. Pinned per arm of the state it reads: TRUE on a resolved,
// linked, un-noticed order; FALSE after the op's marker write (resolvedFor =
// resolvedAt); FALSE when the resolver is the reporter; FALSE with no
// reportedBy link (a legacy order awaiting its backfill); FALSE unresolved.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// projectResolvedNotices runs workOrderResolvedNoticesSpec anchored on the
// named work order.
func (f *lensFixture) projectResolvedNotices(t *testing.T, name string) map[string]any {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(workOrderResolvedNoticesSpec)
	require.NoError(t, err, "workOrderResolvedNotices cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": "vtx.workorder." + f.ids[name],
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	require.Len(t, out, 1, "exactly one row per work-order anchor")
	return out[0].Values
}

// resolvedNotice writes the .resolvedNotice marker RecordWorkOrderResolvedNotice
// commits, for the given resolvedFor.
func (f *lensFixture) resolvedNotice(t *testing.T, name, resolvedFor string) {
	t.Helper()
	f.aspect(t, name, "resolvedNotice", "workOrderResolvedNotice", map[string]any{
		"resolvedFor": resolvedFor, "sentAt": "2026-07-21T11:30:05Z"})
}

// TestWorkOrderResolvedNotices_ResolvedLinkedUntoldOpensTheGap is the gap
// vector: resolved by the tech, reported by (and linked to) someone else,
// no marker — tell them. changeRef (row.resolvedAt) is the anchor's own
// stamp, non-null on every violating row.
func TestWorkOrderResolvedNotices_ResolvedLinkedUntoldOpensTheGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")
	f.resolve(t, "wo")

	v := f.projectResolvedNotices(t, "wo")
	require.Equal(t, true, v["missing_resolved_notice"], "resolved, linked, never told")
	require.Equal(t, true, v["violating"])
	require.Equal(t, "vtx.workorder."+f.ids["wo"], v["entityKey"])
	require.Equal(t, "2026-07-21T11:30:00Z", v["resolvedAt"], "row.resolvedAt is the changeRef Param — the anchor's own aspect")
	require.Equal(t, "vtx.identity."+lenstest.NanoID("tech"), v["resolvedBy"])
	require.Equal(t, "vtx.identity."+f.ids["reporter"], v["reporterKey"])
	require.Nil(t, v["resolvedFor"])
	_, isBool := v["missing_resolved_notice"].(bool)
	require.True(t, isBool, "missing_resolved_notice must be a strict bool for Weaver's openGapColumns")
}

// TestWorkOrderResolvedNotices_MarkerClosesTheGap pins the gap FALSE over the
// state the op leaves: .resolvedNotice.resolvedFor equal to the live
// resolvedAt.
func TestWorkOrderResolvedNotices_MarkerClosesTheGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")
	f.resolve(t, "wo")
	f.resolvedNotice(t, "wo", "2026-07-21T11:30:00Z")

	v := f.projectResolvedNotices(t, "wo")
	require.Equal(t, false, v["missing_resolved_notice"], "told for this resolution")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-07-21T11:30:00Z", v["resolvedFor"])
}

// TestWorkOrderResolvedNotices_MarkerForAnotherStampReopens: a marker whose
// resolvedFor is not the live resolvedAt (a shape only a rewritten resolution
// could leave — .resolution is terminal) reads as untold; the equality is the
// closure, not the marker's presence.
func TestWorkOrderResolvedNotices_MarkerForAnotherStampReopens(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")
	f.resolve(t, "wo")
	f.resolvedNotice(t, "wo", "2026-07-20T08:00:00Z")

	v := f.projectResolvedNotices(t, "wo")
	require.Equal(t, true, v["missing_resolved_notice"], "the marker names a different resolvedAt")
}

// TestWorkOrderResolvedNotices_SelfResolvedStaysClosed: the reporter resolved
// their own order — nobody is told what they did themselves. The lens's
// resolvedBy <> reporterKey conjunct is the op's SelfResolved refusal
// verbatim.
func TestWorkOrderResolvedNotices_SelfResolvedStaysClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")
	f.aspect(t, "wo", "resolution", "workOrderResolution", map[string]any{
		"notes": "Fixed it myself.", "resolvedAt": "2026-07-21T11:30:00Z", "resolvedBy": "vtx.identity." + f.ids["reporter"]})

	v := f.projectResolvedNotices(t, "wo")
	require.Equal(t, false, v["missing_resolved_notice"], "resolver = reporter; nothing to tell")
	require.Equal(t, false, v["violating"])
}

// TestWorkOrderResolvedNotices_NoLinkStaysClosed: a resolved order with no
// reportedBy link (a legacy order the workOrderQueue backfill has not yet
// linked) has no reporter to walk to — closed until the link lands, then told
// once.
func TestWorkOrderResolvedNotices_NoLinkStaysClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedLegacyWorkOrder(t, "wo")
	f.resolve(t, "wo")

	v := f.projectResolvedNotices(t, "wo")
	require.Nil(t, v["reporterKey"], "the OPTIONAL hop bound nothing")
	require.Equal(t, false, v["missing_resolved_notice"], "no link, no recipient")
	require.Equal(t, false, v["violating"])

	// The link lands (the backfill op's write): the same order is told.
	f.vtx(t, "reporter", "identity", nil)
	f.edge(t, "reportedBy", "wo", "reporter")
	v = f.projectResolvedNotices(t, "wo")
	require.Equal(t, true, v["missing_resolved_notice"], "linked now — told once")
}

// TestWorkOrderResolvedNotices_UnresolvedStaysClosed: nothing has happened
// yet.
func TestWorkOrderResolvedNotices_UnresolvedStaysClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")

	v := f.projectResolvedNotices(t, "wo")
	require.Nil(t, v["resolvedAt"])
	require.Equal(t, false, v["missing_resolved_notice"])
	require.Equal(t, false, v["violating"])
}

// TestWorkOrderResolvedNotices_SpecProjectsEveryBodyColumn: every declared
// BodyColumn is a RETURN alias of the cypher.
func TestWorkOrderResolvedNotices_SpecProjectsEveryBodyColumn(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")
	v := f.projectResolvedNotices(t, "wo")
	for _, l := range Lenses() {
		if l.CanonicalName != WorkOrderResolvedNoticesTarget {
			continue
		}
		for _, col := range l.Output.BodyColumns {
			_, ok := v[col]
			require.True(t, ok, "BodyColumn %q is not projected by the cypher", col)
		}
	}
}
