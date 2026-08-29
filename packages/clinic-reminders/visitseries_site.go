package clinicreminders

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// The visit series' own record of WHERE it is seen — the series-shaped twin of
// clinic-domain's appointment atSite link, and for the same reason. Staff reach
// a series through visitSeriesRead's workplace anchor, which walks the series'
// provider to the buildings that provider practicesAt. Contract #1 filters a
// tombstoned vertex out of every graph walk, so the moment a provider is
// tombstoned (or has its last practicesAt withdrawn) that walk yields nothing
// and the series keeps only its patient self-anchor — dropping a live standing
// cadence out of every front-desk world, readable then only by the reserved
// WildcardAnchor holder. The series' OWN atSite link survives that, because it
// is written independently at op time against a site the provider practised at
// when the write was validated.
//
//	lnk.visitseries.<id>.atSite.building.<id>   class "atSite"   ("visitseries atSite building")
//	vtx.visitseries.<id>.siteAssignment          class visitSeriesSiteAssignment = {}
//
//	op BackfillVisitSeriesSite{seriesKey}          Weaver-dispatched auto-remediation
//	op SetVisitSeriesSite{seriesKey, site}         the human-facing manual counterpart
//	lens visitSeriesSiteBackfill (weaver-target, full)   (missing_series_site gap)
//	playbook missing_series_site → directOp(BackfillVisitSeriesSite, seriesKey: row.entityKey)
//
// Contract #1 §1.1 direction: the series is the later-arriving vertex, the
// building the pre-existing one, so the series is the SOURCE — the same sentence
// shape its own forPatient / withProvider links read in (visitseries.go), and the
// same one clinic-domain's appointment atSite link reads in.
//
// Reassignment is deliberately out of scope, exactly as it is for the
// appointment: both ops no-op when the series already carries a live atSite
// link. The gap being closed is "this series names no site at all", never "the
// site it names is wrong".
const (
	visitSeriesSiteAssignmentAspectDDL = "visitSeriesSiteAssignment"

	backfillVisitSeriesSiteOp = "BackfillVisitSeriesSite"
	setVisitSeriesSiteOp      = "SetVisitSeriesSite"

	// VisitSeriesSiteBackfillTarget is the §10.8 TargetID == the
	// visitSeriesSiteBackfill lens's OutputKeyPattern prefix (the §10.2↔§10.8
	// binding Weaver reads).
	VisitSeriesSiteBackfillTarget = "visitSeriesSiteBackfill"
)

// visitSeriesSiteAssignmentAspectTypeDDL declares the .siteAssignment aspect —
// a pure existence marker with no relationship field (the atSite LINK is the
// actual site relationship), written CreateOnly by BOTH site ops, in the SAME
// atomic batch as the link it accompanies.
//
// It is the single-writer lock over a series' site, and it is needed because
// neither op is self-serializing. SetVisitSeriesSite's site is caller-CHOSEN, so
// two concurrent calls picking DIFFERENT sites for the same still-site-less
// series would both pass the "no live atSite link yet" read and both commit a
// DIFFERENT, non-colliding link key (the target segment varies with the chosen
// site). BackfillVisitSeriesSite chooses nothing, but its exactly-one-site rule
// reads a set that MOVES: a provider's site count can go 1 -> 2
// (AssignProviderSite) between its sites_for_provider read and a concurrent
// SetVisitSeriesSite's, and the two then commit different links for the same
// series just the same. Either way CreateOnly on the LINK cannot be the lock,
// because its key differs between the racers. This aspect's key is
// target-INDEPENDENT — fixed per series — so CreateOnly on IT is: the loser's
// whole batch, link included, rejects at commit, and the at-most-one-atSite-link
// invariant that series_site()'s early return and the read spec's either/or CASE
// both rest on holds by construction. Never tombstoned or rewritten once
// created. Declaration-only; NON-sensitive (it attaches to a visitseries, not an
// identity).
func visitSeriesSiteAssignmentAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     visitSeriesSiteAssignmentAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{backfillVisitSeriesSiteOp, setVisitSeriesSiteOp},
		Description: "Visit-series site-assignment guard aspect (clinic-reminders). Stored as " +
			"vtx.visitseries.<NanoID>.siteAssignment (class visitSeriesSiteAssignment) = {} — a pure existence " +
			"marker, no relationship field (the atSite LINK is the actual site relationship; this aspect exists " +
			"only to serialize concurrent writers). Written CreateOnly by BOTH BackfillVisitSeriesSite and " +
			"SetVisitSeriesSite, in the SAME atomic batch as the atSite link it accompanies. Neither op is " +
			"self-serializing: two concurrent SetVisitSeriesSite calls choosing DIFFERENT sites for the same " +
			"still-site-less series both see no live atSite link and both attempt to commit, and a " +
			"BackfillVisitSeriesSite racing a SetVisitSeriesSite can do the same when the provider's site count " +
			"changes between their reads. This aspect's key is the SAME for every such writer (unlike the atSite " +
			"link's, which varies by chosen site) — CreateOnly commits it exactly once, and the loser's whole " +
			"batch (including its atSite link) rejects, so a series never ends up carrying two atSite links. " +
			"Never tombstoned or rewritten once created. Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. The lock is the KEY (fixed per series), never a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "visit-series site-assignment guard aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.visitseries.<NanoID>.siteAssignment; written once by whichever of BackfillVisitSeriesSite / SetVisitSeriesSite sites the series first, alongside the atSite link, to serialize concurrent callers.",
			},
		},
	}
}

// visitSeriesSiteBackfillLens is the missing_series_site convergence lens —
// clinic-domain's clinicSiteBackfill shape applied to this package's own series:
// MATCH the anchor by $actorKey, OPTIONAL MATCH the neighbour whose absence IS
// the gap.
func visitSeriesSiteBackfillLens() pkgmgr.LensSpec {
	return pkgmgr.LensSpec{
		CanonicalName:  VisitSeriesSiteBackfillTarget,
		Class:          "meta.lens",
		Adapter:        "nats-kv",
		Bucket:         "weaver-targets",
		Engine:         "full",
		Spec:           visitSeriesSiteBackfillSpec,
		ProjectionKind: "actorAggregate",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "visitseries",
			OutputKeyPattern: VisitSeriesSiteBackfillTarget + ".{actorSuffix}",
			BodyColumns:      []string{"violating", "missing_series_site", "entityKey", "providerKey"},
			EmptyBehavior:    "delete",
			KeyColumn:        "entityId",
			Freshness:        "auto",
		},
	}
}

// visitSeriesSiteBackfillSpec is the one-row-per-series missing_series_site
// cypher. A series with no atSite link (the OPTIONAL MATCH binds site=null) is
// missing_series_site; one that already carries the link is not.
//
// The gap is NOT gated on the series still being active. A paused or ended
// series is exactly as invisible to its front desk as a live one once its
// provider is tombstoned, and staff need to reach a finished cadence to read its
// history — so every live series vertex is a candidate, whatever its lifecycle
// state. (A tombstoned series matches nothing at all: Contract #1 filters it out
// of the anchor MATCH, and EmptyBehavior "delete" retracts its row.)
//
// Non-convergence safety (mirrors clinic-domain's own missing_site note):
// BackfillVisitSeriesSite only writes the atSite link when the series' provider
// practicesAt EXACTLY ONE LIVE site. Zero (an unassigned, unlinked, or dead
// provider, or one whose every building has since been decommissioned)
// or two-or-more (genuinely ambiguous which site) both leave the op a clean
// no-op, so such a series stays missing_series_site forever and Weaver
// re-dispatches against it on every convergence pass — harmlessly: each dispatch
// is an idempotent no-op (empty mutations/events), never a retry that could
// clobber anything or accumulate side effects. No retry-count column is needed;
// the two permanently-open shapes are distinguished from a transient one by that
// same idempotent-no-op property, not by a counter. SetVisitSeriesSite is the
// human escape hatch for the two-or-more case; the zero case needs
// AssignProviderSite run first, and no op here can invent that relationship.
//
// providerKey is INFORMATIONAL — an operator looking at a permanently-open row
// needs to know WHICH provider's site assignment is missing or ambiguous. Only
// entityKey + the two bools are load-bearing for dispatch.
const visitSeriesSiteBackfillSpec = `MATCH (s:visitseries {key: $actorKey})
OPTIONAL MATCH (s)-[:atSite]->(site:building)
OPTIONAL MATCH (s)-[:withProvider]->(pr:provider)
RETURN
  s.key AS actorKey,
  s.key AS entityKey,
  pr.key AS providerKey,
  (site.key = null) AS missing_series_site,
  (site.key = null) AS violating
`

// visitSeriesSiteBackfillTarget returns the §10.8 playbook: the single
// missing_series_site gap → directOp(BackfillVisitSeriesSite) over the series,
// routing only the candidate key — the op resolves the provider and its sites
// itself (a link read no GapActionSpec Reads template can express), so no
// row.<column> beyond entityKey is needed. Same Reads shape as
// visitSeriesDueTarget: the row's own entityKey, the liveness-guard hydration.
func visitSeriesSiteBackfillTarget() pkgmgr.WeaverTargetSpec {
	return pkgmgr.WeaverTargetSpec{
		TargetID: VisitSeriesSiteBackfillTarget,
		Description: "Every recurring visit series records the clinic site its visits happen at. A series without " +
			"one has its site filled in from the provider's practice location, when that provider works at " +
			"exactly one site.",
		LensRef: VisitSeriesSiteBackfillTarget,
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_series_site": {
				Action:    "directOp",
				Operation: backfillVisitSeriesSiteOp,
				// BackfillVisitSeriesSite is unique to the visitseries
				// vertexType DDL today, but pinned regardless — the same
				// defensive shape clinic-domain's own clinicSiteBackfill
				// target uses (targets.go).
				Class:  visitSeriesVertexDDL,
				Params: map[string]string{"seriesKey": "row.entityKey"},
				Reads:  []string{"row.entityKey"},
			},
		},
	}
}
