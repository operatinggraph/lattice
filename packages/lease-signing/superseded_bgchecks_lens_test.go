package leasesigning

// Rule-engine proof of the supersededBackgroundChecks convergence cypher
// (lenses.go) against the design's state-lifetime table
// (bgcheck-supersession-convergence-rule-design.md §5, rows 1-9b). The lens
// projects VIOLATING ROWS ONLY: an anchor with no qualifying successor binds
// nothing in the second MATCH, so most of these vectors assert an EMPTY
// projection rather than a false column — there is no standing row to read a
// column off.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// projectSuperseded runs supersededBackgroundChecksSpec anchored on the named
// service instance, mirroring lens_cypher_test.go's projectAt and
// bgcheck_freshness_lens_test.go's projectBgFreshness.
func (f *lensFixture) projectSuperseded(t *testing.T, instName string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(supersededBackgroundChecksSpec)
	require.NoError(t, err, "supersededBackgroundChecks cypher must parse on the full engine")
	instKey := "vtx.service." + f.ids[instName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": instKey,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// leaseServiceInstanceMeta seeds a meta vertex carrying the canonicalName the
// anchor and successor conjuncts key off — the same aspect a real DDL install
// stamps on its own type-authority meta (internal/pkgmgr/build.go).
func leaseServiceInstanceMeta(t *testing.T, f *lensFixture, name string) {
	t.Helper()
	f.vtx(t, name, "meta")
	f.aspect(t, name, "canonicalName", "canonicalName", map[string]any{"value": "leaseServiceInstance"})
}

// completedBgcheckOwned seeds one completed background-check instance, owned
// (instanceOf) by the named meta, providedTo the named subject identity.
func completedBgcheckOwned(t *testing.T, f *lensFixture, name, metaName, subjName, completedAt string) {
	t.Helper()
	f.vtxWithClass(t, name, "service", "service.backgroundCheck.instance")
	f.aspect(t, name, "outcome", "outcome", map[string]any{"status": "completed", "completedAt": completedAt})
	f.edge(t, "instanceOf", name, metaName)
	f.edge(t, "providedTo", name, subjName)
}

// Row 1 — an in-flight check (no .outcome at all) never satisfies the
// anchor's own completed conjunct, so it is not the anchor of anything here.
func TestSupersededBackgroundChecks_InFlight_NoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "subj", "identity")
	leaseServiceInstanceMeta(t, f, "meta1")
	f.vtxWithClass(t, "a", "service", "service.backgroundCheck.instance")
	f.edge(t, "instanceOf", "a", "meta1")
	f.edge(t, "providedTo", "a", "subj")

	rows := f.projectSuperseded(t, "a")
	require.Empty(t, rows, "no .outcome at all -- the anchor MATCH itself does not bind")
}

// Row 2 — a completed check with no other owned completed sibling on the
// subject is the current check: the second MATCH binds nothing, so the
// aggregation runs over zero rows and the query emits nothing for this
// anchor.
func TestSupersededBackgroundChecks_CompletedAlone_NoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "subj", "identity")
	leaseServiceInstanceMeta(t, f, "meta1")
	completedBgcheckOwned(t, f, "a", "meta1", "subj", "2026-06-01T00:00:00Z")

	rows := f.projectSuperseded(t, "a")
	require.Empty(t, rows, "the current check with no owned completed sibling projects no row")
}

// Row 3 — A completed at T1, B (owned, same class, same subject) completed
// later at T2: A's row names B, carries A's own instanceOf link key and the
// subject, and both gap/violating columns are true.
//
// Row 4 (the mirror) is proved in the same test: B's own projection, run
// right after, finds nothing later than itself.
func TestSupersededBackgroundChecks_LaterSiblingCompletes_ARowNamesB_BStaysCurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	subjKey := f.vtx(t, "subj", "identity")
	leaseServiceInstanceMeta(t, f, "meta1")
	completedBgcheckOwned(t, f, "a", "meta1", "subj", "2026-06-01T00:00:00Z")
	completedBgcheckOwned(t, f, "b", "meta1", "subj", "2026-07-01T00:00:00Z")

	aKey := "vtx.service." + f.ids["a"]
	bKey := "vtx.service." + f.ids["b"]
	wantInstanceOfLink := "lnk.service." + f.ids["a"] + ".instanceOf.meta." + f.ids["meta1"]

	rows := f.projectSuperseded(t, "a")
	require.Len(t, rows, 1, "A has exactly one qualifying successor")
	v := rows[0].Values
	require.Equal(t, aKey, v["actorKey"])
	require.Equal(t, aKey, v["entityKey"])
	require.Equal(t, subjKey, v["subjectKey"])
	require.Equal(t, wantInstanceOfLink, v["instanceOfLink"], "the one read TombstoneSupersededLeaseServiceInstance cannot derive from its own payload")
	require.Equal(t, bKey, v["supersededBy"])
	require.Equal(t, true, v["missing_retirement"])
	require.Equal(t, true, v["violating"])

	rows = f.projectSuperseded(t, "b")
	require.Empty(t, rows, "row 4: nothing is later than B, so B's own projection binds nothing in the second MATCH")
}

// Row 5 — A completed, B failed later: a failed instance never satisfies the
// successor's own completed conjunct, so A stays current.
func TestSupersededBackgroundChecks_LaterSiblingFailed_NoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "subj", "identity")
	leaseServiceInstanceMeta(t, f, "meta1")
	completedBgcheckOwned(t, f, "a", "meta1", "subj", "2026-06-01T00:00:00Z")
	f.vtxWithClass(t, "b", "service", "service.backgroundCheck.instance")
	f.aspect(t, "b", "outcome", "outcome", map[string]any{"status": "failed", "completedAt": "2026-07-01T00:00:00Z"})
	f.edge(t, "instanceOf", "b", "meta1")
	f.edge(t, "providedTo", "b", "subj")

	rows := f.projectSuperseded(t, "a")
	require.Empty(t, rows, "a failed sibling never supersedes -- the op would refuse it too")
}

// Row 6 — A itself failed: the anchor's own completed conjunct refuses it
// regardless of what B does, so A is never this lens's anchor.
func TestSupersededBackgroundChecks_AnchorFailed_NoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "subj", "identity")
	leaseServiceInstanceMeta(t, f, "meta1")
	f.vtxWithClass(t, "a", "service", "service.backgroundCheck.instance")
	f.aspect(t, "a", "outcome", "outcome", map[string]any{"status": "failed", "completedAt": "2026-06-01T00:00:00Z"})
	f.edge(t, "instanceOf", "a", "meta1")
	f.edge(t, "providedTo", "a", "subj")
	completedBgcheckOwned(t, f, "b", "meta1", "subj", "2026-07-01T00:00:00Z")

	rows := f.projectSuperseded(t, "a")
	require.Empty(t, rows, "the anchor MATCH requires a completed outcome; A's failed record persists as itself")
}

// Row 7 — B completes later but is a payment instance, not a background
// check: the class conjunct on the successor keeps the two families apart, so
// A stays current.
func TestSupersededBackgroundChecks_LaterSiblingWrongClass_NoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "subj", "identity")
	leaseServiceInstanceMeta(t, f, "meta1")
	completedBgcheckOwned(t, f, "a", "meta1", "subj", "2026-06-01T00:00:00Z")
	f.vtxWithClass(t, "b", "service", "service.payment.instance")
	f.aspect(t, "b", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-07-01T00:00:00Z"})
	f.edge(t, "instanceOf", "b", "meta1")
	f.edge(t, "providedTo", "b", "subj")

	rows := f.projectSuperseded(t, "a")
	require.Empty(t, rows, "a payment instance never supersedes a background check")
}

// Row 8 — A's own instanceOf link targets something other than a
// leaseServiceInstance meta: a plain foreign vertex (label mismatch on
// `(m:meta)`), and a meta whose canonicalName names a different type
// authority. Neither ever anchors this lens.
func TestSupersededBackgroundChecks_AnchorNotOwnedByLeaseServiceInstance_NoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	t.Run("instanceOf targets a non-meta vertex", func(t *testing.T) {
		f := newLensFixture(t)
		f.vtx(t, "subj", "identity")
		f.vtxWithClass(t, "tmpl", "service", "service.backgroundCheck.template")
		f.vtxWithClass(t, "a", "service", "service.backgroundCheck.instance")
		f.aspect(t, "a", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-01T00:00:00Z"})
		f.edge(t, "instanceOf", "a", "tmpl")
		f.edge(t, "providedTo", "a", "subj")
		completedBgcheckOwned(t, f, "b", "tmpl", "subj", "2026-07-01T00:00:00Z")

		rows := f.projectSuperseded(t, "a")
		require.Empty(t, rows, "instanceOf targets a service, not a meta -- the anchor MATCH's label does not bind")
	})

	t.Run("instanceOf targets a meta with a different canonicalName", func(t *testing.T) {
		f := newLensFixture(t)
		f.vtx(t, "subj", "identity")
		f.vtx(t, "othermeta", "meta")
		f.aspect(t, "othermeta", "canonicalName", "canonicalName", map[string]any{"value": "someOtherServiceInstance"})
		completedBgcheckOwned(t, f, "a", "othermeta", "subj", "2026-06-01T00:00:00Z")
		completedBgcheckOwned(t, f, "b", "othermeta", "subj", "2026-07-01T00:00:00Z")

		rows := f.projectSuperseded(t, "a")
		require.Empty(t, rows, "the anchor's own canonicalName conjunct refuses a foreign type authority")
	})
}

// Row 8b — B's instanceOf targets a DIFFERENT meta vertex that also happens
// to carry canonicalName "leaseServiceInstance" (a second, same-named type
// authority): the `(newer)-[:instanceOf]->(m)` re-bind to the SAME meta A's
// own instanceOf targets keeps a foreign check from ever superseding this
// one, name collision notwithstanding.
func TestSupersededBackgroundChecks_LaterSiblingOwnedByDifferentSameNamedMeta_NoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "subj", "identity")
	leaseServiceInstanceMeta(t, f, "meta1")
	leaseServiceInstanceMeta(t, f, "meta2")
	completedBgcheckOwned(t, f, "a", "meta1", "subj", "2026-06-01T00:00:00Z")
	completedBgcheckOwned(t, f, "b", "meta2", "subj", "2026-07-01T00:00:00Z")

	rows := f.projectSuperseded(t, "a")
	require.Empty(t, rows, "B is owned by a DIFFERENT meta vertex, even though it carries the same canonicalName")
}

// Row 9 — two later completed siblings: supersededBy is a deterministic
// member of the qualifying set (max(newer.key)), not necessarily the newest.
// Either B or C alone would satisfy the op; the lens picks whichever key
// compares greater as a plain string.
func TestSupersededBackgroundChecks_TwoLaterSiblings_SupersededByIsMaxKey(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "subj", "identity")
	leaseServiceInstanceMeta(t, f, "meta1")
	completedBgcheckOwned(t, f, "a", "meta1", "subj", "2026-06-01T00:00:00Z")
	completedBgcheckOwned(t, f, "b", "meta1", "subj", "2026-07-01T00:00:00Z")
	completedBgcheckOwned(t, f, "c", "meta1", "subj", "2026-08-01T00:00:00Z")

	bKey := "vtx.service." + f.ids["b"]
	cKey := "vtx.service." + f.ids["c"]
	want := bKey
	if cKey > bKey {
		want = cKey
	}

	rows := f.projectSuperseded(t, "a")
	require.Len(t, rows, 1)
	require.Equal(t, want, rows[0].Values["supersededBy"], "supersededBy is a deterministic member of the qualifying set (max key), valid either way to the op")
}

// Row 9b — A and B complete in the same second (rfc3339_utc is whole-seconds,
// and Weaver paces backgroundCheck dispatch at 2/s, so a tie is reachable):
// exactly one of the pair projects a row, the one with the SMALLER key,
// naming the greater. The tie-break is TEXTUALLY the same rule
// TombstoneSupersededLeaseServiceInstance's own recency guard applies
// (scripts.go) -- drift between the two is what mutation (c) below proves
// this test would catch.
func TestSupersededBackgroundChecks_SameSecondTie_SmallerKeyNamesGreater(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "subj", "identity")
	leaseServiceInstanceMeta(t, f, "meta1")
	const tiedAt = "2026-06-01T00:05:00Z"
	completedBgcheckOwned(t, f, "tieA", "meta1", "subj", tiedAt)
	completedBgcheckOwned(t, f, "tieB", "meta1", "subj", tiedAt)

	aKey := "vtx.service." + f.ids["tieA"]
	bKey := "vtx.service." + f.ids["tieB"]
	lesserName, greaterName, greaterKey := "tieA", "tieB", bKey
	if bKey < aKey {
		lesserName, greaterName, greaterKey = "tieB", "tieA", aKey
	}

	rows := f.projectSuperseded(t, lesserName)
	require.Len(t, rows, 1, "the smaller key is the one that projects a row")
	require.Equal(t, greaterKey, rows[0].Values["supersededBy"], "naming the greater key")

	rows = f.projectSuperseded(t, greaterName)
	require.Empty(t, rows, "the greater key survives -- nothing outranks it in the tie")
}
