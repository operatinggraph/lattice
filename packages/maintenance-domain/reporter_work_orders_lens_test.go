package maintenancedomain

// Rule-engine proof of the reporterWorkOrdersRead cypher — the reporter-
// anchored protected read model a tenant's own card lists. Same harness the
// convergence lens proofs use (lensFixture, lens_cypher_test.go).
//
//   - TestReporterWorkOrdersRead_ProjectsForTheReporter: an order with a
//     reportedBy link projects one row, anchored on the reporter's id alone,
//     with the unit and summary/priority/reported_at from .report.
//   - TestReporterWorkOrdersRead_BuildingLocatedOrderProjectsWithNullUnit:
//     the unit hop is OPTIONAL — a staff report at a building still lists.
//   - TestReporterWorkOrdersRead_OpenTaskCounts: an open task counts one, a
//     cancelled task counts zero, both beside each other count one.
//   - TestReporterWorkOrdersRead_ResolvedFieldsAndNotice: .resolution and the
//     .resolvedNotice marker project resolved_at / resolution_notes /
//     notice_sent_at.
//   - TestReporterWorkOrdersRead_UnlinkedOrderProjectsNothing: no reportedBy
//     link, no identity to anchor a row on.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

func (f *lensFixture) projectReporterWorkOrders(t *testing.T) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(reporterWorkOrdersReadSpec)
	require.NoError(t, err, "reporterWorkOrdersRead cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// seedWorkOrderAtUnit is seedWorkOrder plus the locatedAt link to a unit
// carrying an .address — the tenant-reported shape.
func (f *lensFixture) seedWorkOrderAtUnit(t *testing.T, name, unitName string) string {
	t.Helper()
	key := f.seedWorkOrder(t, name)
	if _, ok := f.ids[unitName]; !ok {
		f.vtx(t, unitName, "unit", nil)
		f.aspect(t, unitName, "address", "unitAddress", map[string]any{"line1": "12 Riverside Walk, 3B", "city": "Springfield"})
	}
	f.edge(t, "locatedAt", name, unitName)
	return key
}

func TestReporterWorkOrdersRead_ProjectsForTheReporter(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	woKey := f.seedWorkOrderAtUnit(t, "wo", "u1")

	rows := f.projectReporterWorkOrders(t)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, f.ids["wo"], v["work_order_id"], "IntoKey is the order's bare id")
	require.Equal(t, woKey, v["work_order_key"])
	require.Equal(t, "vtx.identity."+f.ids["reporter"], v["reporter_key"])
	require.Equal(t, "vtx.unit."+f.ids["u1"], v["unit_key"])
	require.Equal(t, "12 Riverside Walk, 3B", v["unit_address"])
	require.Equal(t, "Kitchen tap is dripping", v["summary"])
	require.Equal(t, "normal", v["priority"])
	require.Equal(t, "2026-07-21T09:00:00Z", v["reported_at"])
	require.EqualValues(t, 0, v["open_task_count"])
	require.Nil(t, v["resolved_at"])
	require.Nil(t, v["resolution_notes"])
	require.Nil(t, v["notice_sent_at"])
	anchors, ok := v["authz_anchors"].([]any)
	require.True(t, ok, "authz_anchors must be a list, got %T", v["authz_anchors"])
	require.Equal(t, []any{f.ids["reporter"]}, anchors, "the reporter alone anchors the row — a self surface, not a portfolio one")
}

func TestReporterWorkOrdersRead_BuildingLocatedOrderProjectsWithNullUnit(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")
	f.vtx(t, "tower", "building", nil)
	f.edge(t, "locatedAt", "wo", "tower")

	rows := f.projectReporterWorkOrders(t)
	require.Len(t, rows, 1, "a staff report at a building still lists for its reporter")
	v := rows[0].Values
	require.Nil(t, v["unit_key"], "the unit hop is OPTIONAL and bound nothing")
	require.Nil(t, v["unit_address"])
	require.Equal(t, []any{f.ids["reporter"]}, v["authz_anchors"])
}

func TestReporterWorkOrdersRead_OpenTaskCounts(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrderAtUnit(t, "wo", "u1")
	f.seedTask(t, "cancelled", "wo", "cancelled")
	f.seedTask(t, "open", "wo", "open")

	rows := f.projectReporterWorkOrders(t)
	require.Len(t, rows, 1, "the task fan never multiplies the row")
	require.EqualValues(t, 1, rows[0].Values["open_task_count"], "count(DISTINCT CASE …): one open, one cancelled → 1")
}

func TestReporterWorkOrdersRead_ResolvedFieldsAndNotice(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrderAtUnit(t, "wo", "u1")
	f.seedTask(t, "task", "wo", "complete")
	f.resolve(t, "wo")
	f.resolvedNotice(t, "wo", "2026-07-21T11:30:00Z")

	rows := f.projectReporterWorkOrders(t)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "2026-07-21T11:30:00Z", v["resolved_at"])
	require.Equal(t, "Replaced the washer.", v["resolution_notes"])
	require.Equal(t, "2026-07-21T11:30:05Z", v["notice_sent_at"], "the marker's sentAt — the card says the reporter was told")
	require.EqualValues(t, 0, v["open_task_count"], "a complete task is not open")
}

func TestReporterWorkOrdersRead_UnlinkedOrderProjectsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedLegacyWorkOrder(t, "wo")

	require.Empty(t, f.projectReporterWorkOrders(t), "no reportedBy link, no identity to anchor a row on")
}

// TestReporterWorkOrdersRead_SpecProjectsEveryColumn: every declared Postgres
// column and the IntoKey are RETURN aliases of the cypher.
func TestReporterWorkOrdersRead_SpecProjectsEveryColumn(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrderAtUnit(t, "wo", "u1")
	rows := f.projectReporterWorkOrders(t)
	require.Len(t, rows, 1)
	for _, l := range Lenses() {
		if l.CanonicalName != ReporterWorkOrdersReadLens {
			continue
		}
		require.True(t, l.Protected)
		require.True(t, l.DiffRetraction, "the row walks links structurally; an unwired link needs target-diff retraction")
		for _, k := range l.IntoKey {
			_, ok := rows[0].Values[k]
			require.True(t, ok, "IntoKey %q is not projected by the cypher", k)
		}
		for _, c := range l.Columns {
			_, ok := rows[0].Values[c.Name]
			require.True(t, ok, "column %q is not projected by the cypher", c.Name)
		}
		_, ok := rows[0].Values["authz_anchors"]
		require.True(t, ok, "a protected lens projects authz_anchors")
	}
}
