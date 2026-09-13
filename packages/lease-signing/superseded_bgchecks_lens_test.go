package leasesigning

// Rule-engine proof of the supersededBackgroundChecks convergence cypher
// (lenses.go) against the design's state-lifetime table
// (bgcheck-supersession-convergence-rule-design.md §5, rows 1-9b). The lens
// projects VIOLATING ROWS ONLY: an anchor with no qualifying successor binds
// nothing in the second MATCH, so most of these vectors assert an EMPTY
// projection rather than a false column — there is no standing row to read a
// column off.
//
// One row of that table is the lens's boundary rather than its rule: the
// successor's OWNERSHIP is not bound as a hop here (binding it would make the
// meta a derivation hub — lenses.go's spec comment). What the successor does
// carry is the zero-hop signal of this package's own .outcome aspect class, which
// is what keeps a foreign instance with a greater key from winning max() and
// starving the retirement; a forged aspect class still projects, and the op's own
// enumeration refuses it. Every OTHER conjunct the op checks, this lens mirrors
// exactly.

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
	completedOwnedInstance(t, f, name, "service.backgroundCheck.instance", metaName, subjName, completedAt)
}

// completedOwnedInstance seeds one completed lease-signing service instance of
// the given envelope class, owned (instanceOf) by the named meta and providedTo
// the named subject identity. The .outcome aspect is written exactly as
// RecordLeaseServiceOutcome writes it — aspect class leaseServiceOutcome, data
// {status, completedAt, validUntil} (scripts.go) — because the aspect's own CLASS
// is a conjunct of this lens, so a fixture that stamped any other class would be
// seeding the FOREIGN shape and testing the wrong thing. validUntil equals
// completedAt here: this lens never reads it (backgroundCheckFreshness does),
// and only its presence distinguishes the shape.
func completedOwnedInstance(t *testing.T, f *lensFixture, name, vertexClass, metaName, subjName, completedAt string) {
	t.Helper()
	f.vtxWithClass(t, name, "service", vertexClass)
	f.aspect(t, name, "outcome", "leaseServiceOutcome",
		map[string]any{"status": "completed", "completedAt": completedAt, "validUntil": completedAt})
	f.edge(t, "instanceOf", name, metaName)
	f.edge(t, "providedTo", name, subjName)
}

// foreignCompletedBgcheck seeds the shape service-domain's own generic service
// mechanism really produces for the backgroundCheck family it also admits
// (packages/service-domain/ddls.go): the same envelope class
// service.backgroundCheck.instance and a providedTo link to the same applicant,
// but RecordServiceOutcome's aspect — class `outcome`, data {status, completedAt},
// no validUntil — and an instanceOf link to a service TEMPLATE vertex rather than
// to any leaseServiceInstance meta.
func foreignCompletedBgcheck(t *testing.T, f *lensFixture, name, tmplName, subjName, completedAt string) {
	t.Helper()
	f.vtxWithClass(t, name, "service", "service.backgroundCheck.instance")
	f.aspect(t, name, "outcome", "outcome", map[string]any{"status": "completed", "completedAt": completedAt})
	f.edge(t, "instanceOf", name, tmplName)
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
// authority), and B carries OUR .outcome aspect class, so the successor's
// zero-hop ownership signal admits it. The lens PROJECTS a row naming B: real
// ownership is deliberately not a conjunct here, because binding the successor to
// the anchor's own (m) would make that meta position reachable from the successor
// side and turn it into a derivation hub — every completion reprojecting every
// instance this package owns (lenses.go's spec comment;
// internal/refractor/pipeline's TestDeriveAnchors_SupersededBgchecks_StaysInsideTheApplicant
// holds the shape). The row is the op's to refuse: it proves the successor's
// ownership by a bounded instanceOf walk and rejects this pair NotOwned
// (TestTombstoneSupersededLeaseServiceInstance_ForeignSuccessorForgingOurOutcomeClass_Rejected),
// so the foreign check retires nothing and the row raises a loud, per-entity
// GapBudgetExhausted instead.
//
// In production the minter this vector describes cannot exist: the write gate
// resolves an aspect mutation's governing DDL by exact class, so only this
// package's own leaseServiceOutcome aspectType DDL may stamp that class, and no
// shipped package forges it. The vector stands as the op's belt — the one shape
// that reaches the op past the lens's signal is a forging minter, and the op
// refuses it.
func TestSupersededBackgroundChecks_LaterSiblingOwnedByDifferentSameNamedMeta_ProjectsARowTheOpRefuses(t *testing.T) {
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
	require.Len(t, rows, 1, "the lens does not judge the successor's ownership -- the op does")
	require.Equal(t, "vtx.service."+f.ids["b"], rows[0].Values["supersededBy"],
		"the row names the foreign-owned successor, which is what the op refuses NotOwned")
	require.Equal(t, "lnk.service."+f.ids["a"]+".instanceOf.meta."+f.ids["meta1"], rows[0].Values["instanceOfLink"],
		"the ANCHOR's ownership is still a conjunct, and its own link key is still the projected read")
}

// The anchor's class conjunct is the anchor's, not only the successor's: a
// completed PAYMENT instance with a later completed payment sibling is never an
// anchor here. Payments accumulate by design (missing_payment closes
// permanently on the first completed one), so retiring them is out of this
// rule's scope entirely, and row 7's successor-side class conjunct would not
// stop a payment pair from pairing with itself. Both are seeded as genuine
// lease-signing instances — RecordLeaseServiceOutcome serves both families, so a
// real payment's outcome carries this package's own aspect class — which leaves
// the VERTEX class conjunct as the only thing excluding them.
func TestSupersededBackgroundChecks_PaymentAnchor_NoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "subj", "identity")
	leaseServiceInstanceMeta(t, f, "meta1")
	completedOwnedInstance(t, f, "payA", "service.payment.instance", "meta1", "subj", "2026-06-01T00:00:00Z")
	completedOwnedInstance(t, f, "payB", "service.payment.instance", "meta1", "subj", "2026-07-01T00:00:00Z")

	rows := f.projectSuperseded(t, "payA")
	require.Empty(t, rows, "the anchor MATCH admits only service.backgroundCheck.instance")
}

// The starvation vector, and the reason the successor carries an ownership
// SIGNAL rather than nothing: A is owned and superseded by the owned B, while a
// FOREIGN completed background check F sits on the same applicant with a key
// GREATER than B's. max(newer.key) ranks by key, so without the successor's
// .outcome-class conjunct the row would name F — a successor the op refuses
// NotOwned on every delivery, which does not merely fail, it STARVES the
// genuine retirement: B never gets named, A is never retired, and the target
// exhausts its budget on a row that can never be satisfied. The conjunct keeps
// max() over this package's own candidates, so the row names B.
func TestSupersededBackgroundChecks_ForeignRivalWithGreaterKey_RowNamesTheOwnedSuccessor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "subj", "identity")
	leaseServiceInstanceMeta(t, f, "meta1")
	f.vtxWithClass(t, "foreignTmpl", "service", "service.backgroundCheck.template")
	completedBgcheckOwned(t, f, "a", "meta1", "subj", "2026-06-01T00:00:00Z")
	completedBgcheckOwned(t, f, "starveB", "meta1", "subj", "2026-07-01T00:00:00Z")
	// F completes LATER than B as well, so recency cannot be what decides it.
	foreignCompletedBgcheck(t, f, "starveF", "foreignTmpl", "subj", "2026-08-01T00:00:00Z")

	bKey := "vtx.service." + f.ids["starveB"]
	fKey := "vtx.service." + f.ids["starveF"]
	// lenstest.NanoID is a pure hash of the logical name, so this ordering is
	// fixed — asserted rather than assumed, because the vector is only the
	// starvation vector while F outranks B.
	require.Greater(t, fKey, bKey, "the fixture's foreign rival must hold the GREATER key for max() to prefer it")

	rows := f.projectSuperseded(t, "a")
	require.Len(t, rows, 1)
	require.Equal(t, bKey, rows[0].Values["supersededBy"],
		"max() must range over this package's own candidates -- naming F would starve A's retirement behind a permanent NotOwned")
}

// The same-applicant join is the join: B is later, completed and owned by the
// very same meta, but providedTo a DIFFERENT applicant, so it supersedes
// nothing of A's. Two applicants' checks are never each other's successors, and
// the shared meta is not a path the pattern offers between them.
func TestSupersededBackgroundChecks_LaterSiblingOnAnotherApplicant_NoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "subj", "identity")
	f.vtx(t, "otherSubj", "identity")
	leaseServiceInstanceMeta(t, f, "meta1")
	completedBgcheckOwned(t, f, "a", "meta1", "subj", "2026-06-01T00:00:00Z")
	completedBgcheckOwned(t, f, "b", "meta1", "otherSubj", "2026-07-01T00:00:00Z")

	rows := f.projectSuperseded(t, "a")
	require.Empty(t, rows, "B is another applicant's check -- the providedTo join through the shared identity binds nothing")
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
// (scripts.go); drift between the two is what this vector catches.
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
