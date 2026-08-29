// BackfillVisitSeriesSite / SetVisitSeriesSite integration tests for the
// clinic-reminders Capability Package — the series-shaped twin of
// clinic-domain's own BackfillAppointmentSite coverage (backfill_site_test.go
// there), against the visitSeriesSiteBackfill missing_series_site gap
// (visitseries_site.go).
//
// Same external test package / harness as integration_test.go: seed the kernel,
// install orchestration-base + clinic-domain + clinic-reminders, then submit the
// site ops and assert the committed atSite link (or its deliberate absence).
//
// The provider/building topology is seeded straight to Core KV rather than built
// through location-domain's own ops — this suite never installs location-domain
// (setupRemEnv installs three packages), and both site ops resolve their sites by
// walking practicesAt links, so the links ARE the fixture. That is the same
// shortcut seedRemFrontDeskTopology already takes for the workplace guard.
//
// Coverage:
//  1. TestVisitSeries_BackfillSite_SingleSite      — backfills when the provider practicesAt exactly one site
//  2. TestVisitSeries_BackfillSite_NoopAlreadySited — no-ops when the series already carries a live atSite link
//  3. TestVisitSeries_BackfillSite_NoopAmbiguous    — no-ops (never guesses) at zero or two-or-more sites
//  4. TestVisitSeries_BackfillSite_Idempotent       — a second dispatch is a clean no-op
//  5. TestVisitSeries_BackfillSite_RejectsTombstonedSeries — the liveness guard
//  6. TestVisitSeries_SetSite_WritesLinkAndGuard    — the human override writes the link + its CreateOnly guard
//  7. TestVisitSeries_SetSite_RejectsUnpractisedSite — ProviderNotAtSite, never a silent fall-through
//  8. TestVisitSeries_SetSite_NoopAlreadySited      — reassignment is out of scope
//  9. TestVisitSeries_SiteOps_SkipDecommissionedBuilding — a tombstoned site is not a candidate and not settable
package clinicreminders_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

const (
	vsPatientID   = "CRVSPATNTHJKMNPQRSTV"
	vsProviderAID = "CRVSPRVDAHJKMNPQRSTV"
	vsProviderBID = "CRVSPRVDBHJKMNPQRSTV"
	vsProviderCID = "CRVSPRVDCHJKMNPQRSTV"
	vsBuildingAID = "CRVSBLDGAHJKMNPQRSTV"
	vsBuildingBID = "CRVSBLDGBHJKMNPQRSTV"

	vsPatientKey   = "vtx.patient." + vsPatientID
	vsProviderAKey = "vtx.provider." + vsProviderAID
	vsProviderBKey = "vtx.provider." + vsProviderBID
	vsProviderCKey = "vtx.provider." + vsProviderCID
	vsBuildingAKey = "vtx.building." + vsBuildingAID
	vsBuildingBKey = "vtx.building." + vsBuildingBID
)

// crMissing reports whether a key is absent from Core KV entirely (never
// written), as opposed to written-and-tombstoned.
func crMissing(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) bool {
	t.Helper()
	_, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	return err != nil
}

// vsAtSiteLinkKey is the deterministic per-(series, building) atSite link key.
func vsAtSiteLinkKey(seriesKey, buildingKey string) string {
	_, sid, _ := substrate.ParseVertexKey(seriesKey)
	_, bid, _ := substrate.ParseVertexKey(buildingKey)
	return "lnk.visitseries." + sid + ".atSite.building." + bid
}

// seedVisitSeriesSiteTopology mints the patient, the two buildings, and the two
// providers, then wires each provider's practicesAt links per the caller's
// request: provider A gets `aSites` buildings, provider B gets `bSites`. Passing
// a provider zero buildings is the "unassigned provider" shape.
func seedVisitSeriesSiteTopology(t *testing.T, ctx context.Context, conn *substrate.Conn, aSites, bSites []string) {
	t.Helper()
	remSeedVertex(t, ctx, conn, vsPatientKey, "patient")
	remSeedVertex(t, ctx, conn, vsProviderAKey, "provider")
	remSeedVertex(t, ctx, conn, vsProviderBKey, "provider")
	remSeedVertex(t, ctx, conn, vsBuildingAKey, "building")
	remSeedVertex(t, ctx, conn, vsBuildingBKey, "building")

	wire := func(providerKey, providerID string, sites []string) {
		for _, site := range sites {
			_, siteID, _ := substrate.ParseVertexKey(site)
			testutil.SeedLink(t, ctx, conn,
				"lnk.provider."+providerID+".practicesAt.building."+siteID,
				"practicesAt", providerKey, site)
		}
	}
	wire(vsProviderAKey, vsProviderAID, aSites)
	wire(vsProviderBKey, vsProviderBID, bSites)
}

// startSitelessSeries starts a visit series (as the operator staff actor, which
// the workplace guard exempts) for the shared patient and the named provider.
// The series carries no atSite link — StartVisitSeries writes none.
func startSitelessSeries(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, providerKey string) string {
	t.Helper()
	id := crSubmit(t, ctx, conn, cp, cons, label, "StartVisitSeries", "visitseries",
		`{"patientKey":"`+vsPatientKey+`","providerKey":"`+providerKey+
			`","intervalDays":30,"startAt":"2026-08-01T09:00:00Z"}`,
		[]string{vsPatientKey, providerKey}, processor.OutcomeAccepted)
	return "vtx.visitseries." + id
}

// TestVisitSeries_BackfillSite_SingleSite proves the core backfill path: a
// series started with no site, whose provider practicesAt EXACTLY ONE site,
// gets that site's atSite link written on dispatch — the link that keeps the
// series inside its front desk's world once the provider is tombstoned.
func TestVisitSeries_BackfillSite_SingleSite(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "vsbfsingle", Instance: "cr-vsbfsingle"})
	seedVisitSeriesSiteTopology(t, ctx, conn, []string{vsBuildingAKey}, nil)

	seriesKey := startSitelessSeries(t, ctx, conn, cp, cons, "vsbfs00001", vsProviderAKey)
	lk := vsAtSiteLinkKey(seriesKey, vsBuildingAKey)
	if !crMissing(t, ctx, conn, lk) {
		t.Fatalf("a series must not carry an atSite link before the backfill runs")
	}

	crSubmit(t, ctx, conn, cp, cons, "vsbfs00002", "BackfillVisitSeriesSite", "visitseries",
		`{"seriesKey":"`+seriesKey+`"}`, []string{seriesKey}, processor.OutcomeAccepted)

	doc := crReadDoc(t, ctx, conn, lk)
	if doc["class"] != "atSite" {
		t.Fatalf("atSite link class = %v, want atSite", doc["class"])
	}
	if del, _ := doc["isDeleted"].(bool); del {
		t.Fatalf("atSite link should be alive; got isDeleted=%v", del)
	}
	if sv, _ := doc["sourceVertex"].(string); sv != seriesKey {
		t.Fatalf("link sourceVertex = %q, want %q (the series — the later-arriving vertex is the source)", sv, seriesKey)
	}
	if tv, _ := doc["targetVertex"].(string); tv != vsBuildingAKey {
		t.Fatalf("link targetVertex = %q, want %q (the site)", tv, vsBuildingAKey)
	}
}

// TestVisitSeries_BackfillSite_NoopAlreadySited proves the already-sited no-op
// on a shape that CANNOT pass by accident: the series is already sited at
// building A, while its provider practicesAt exactly one site — building B.
// Were the no-op check dropped, the exactly-one rule would fire and write a
// SECOND atSite link, to B.
func TestVisitSeries_BackfillSite_NoopAlreadySited(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "vsbfsited", Instance: "cr-vsbfsited"})
	seedVisitSeriesSiteTopology(t, ctx, conn, []string{vsBuildingBKey}, nil)

	seriesKey := startSitelessSeries(t, ctx, conn, cp, cons, "vsbfh00001", vsProviderAKey)
	// Seeded directly: building A is deliberately NOT one of this provider's
	// practicesAt sites, which no op would write today — the point is only that
	// SOME live atSite link is present.
	testutil.SeedLink(t, ctx, conn, vsAtSiteLinkKey(seriesKey, vsBuildingAKey), "atSite", seriesKey, vsBuildingAKey)

	crSubmit(t, ctx, conn, cp, cons, "vsbfh00002", "BackfillVisitSeriesSite", "visitseries",
		`{"seriesKey":"`+seriesKey+`"}`, []string{seriesKey}, processor.OutcomeAccepted)

	if !crMissing(t, ctx, conn, vsAtSiteLinkKey(seriesKey, vsBuildingBKey)) {
		t.Fatalf("an already-sited series must not gain a second atSite link from the backfill")
	}
	after := crReadDoc(t, ctx, conn, vsAtSiteLinkKey(seriesKey, vsBuildingAKey))
	if del, _ := after["isDeleted"].(bool); del {
		t.Fatalf("the existing atSite link should remain alive after a no-op backfill; got isDeleted=%v", del)
	}
}

// TestVisitSeries_BackfillSite_NoopAmbiguous proves the op never guesses: a
// series whose provider practicesAt ZERO sites, or TWO or more, is left with no
// atSite link at all. Both shapes stay missing_series_site forever, harmlessly —
// each re-dispatch is the same clean no-op.
func TestVisitSeries_BackfillSite_NoopAmbiguous(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "vsbfamb", Instance: "cr-vsbfamb"})
	// Provider A practises at BOTH buildings (ambiguous); provider B at none.
	seedVisitSeriesSiteTopology(t, ctx, conn, []string{vsBuildingAKey, vsBuildingBKey}, nil)

	t.Run("two sites", func(t *testing.T) {
		seriesKey := startSitelessSeries(t, ctx, conn, cp, cons, "vsbfa00001", vsProviderAKey)
		crSubmit(t, ctx, conn, cp, cons, "vsbfa00002", "BackfillVisitSeriesSite", "visitseries",
			`{"seriesKey":"`+seriesKey+`"}`, []string{seriesKey}, processor.OutcomeAccepted)
		for _, b := range []string{vsBuildingAKey, vsBuildingBKey} {
			if !crMissing(t, ctx, conn, vsAtSiteLinkKey(seriesKey, b)) {
				t.Fatalf("no atSite link should be written for candidate site %s when the provider has two", b)
			}
		}
	})

	t.Run("zero sites", func(t *testing.T) {
		seriesKey := startSitelessSeries(t, ctx, conn, cp, cons, "vsbfz00001", vsProviderBKey)
		crSubmit(t, ctx, conn, cp, cons, "vsbfz00002", "BackfillVisitSeriesSite", "visitseries",
			`{"seriesKey":"`+seriesKey+`"}`, []string{seriesKey}, processor.OutcomeAccepted)
		for _, b := range []string{vsBuildingAKey, vsBuildingBKey} {
			if !crMissing(t, ctx, conn, vsAtSiteLinkKey(seriesKey, b)) {
				t.Fatalf("an unassigned provider must leave the series unsited; got a link to %s", b)
			}
		}
	})
}

// TestVisitSeries_BackfillSite_Idempotent proves a second dispatch after a
// successful backfill is a clean no-op — the redelivery / second-convergence-pass
// shape, which Weaver produces routinely.
func TestVisitSeries_BackfillSite_Idempotent(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "vsbfidem", Instance: "cr-vsbfidem"})
	seedVisitSeriesSiteTopology(t, ctx, conn, []string{vsBuildingAKey}, nil)

	seriesKey := startSitelessSeries(t, ctx, conn, cp, cons, "vsbfi00001", vsProviderAKey)
	lk := vsAtSiteLinkKey(seriesKey, vsBuildingAKey)

	crSubmit(t, ctx, conn, cp, cons, "vsbfi00002", "BackfillVisitSeriesSite", "visitseries",
		`{"seriesKey":"`+seriesKey+`"}`, []string{seriesKey}, processor.OutcomeAccepted)
	first := crReadDoc(t, ctx, conn, lk)

	crSubmit(t, ctx, conn, cp, cons, "vsbfi00003", "BackfillVisitSeriesSite", "visitseries",
		`{"seriesKey":"`+seriesKey+`"}`, []string{seriesKey}, processor.OutcomeAccepted)
	second := crReadDoc(t, ctx, conn, lk)

	if del, _ := second["isDeleted"].(bool); del {
		t.Fatalf("atSite link should remain alive after a re-dispatch; got isDeleted=%v", del)
	}
	if first["targetVertex"] != second["targetVertex"] {
		t.Fatalf("atSite link target changed across re-dispatch: %v -> %v", first["targetVertex"], second["targetVertex"])
	}
}

// TestVisitSeries_BackfillSite_RejectsTombstonedSeries pins the liveness guard:
// a dispatch that lands after the series was tombstoned writes nothing (the
// AdvanceVisitSeries guard idiom).
func TestVisitSeries_BackfillSite_RejectsTombstonedSeries(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "vsbftomb", Instance: "cr-vsbftomb"})
	seedVisitSeriesSiteTopology(t, ctx, conn, []string{vsBuildingAKey}, nil)

	// Seeded already-tombstoned, the shape a tombstone leaves behind (the
	// AdvanceVisitSeries dead-series vector's own fixture).
	dead := "vtx.visitseries.CRdeadSiteSeriesMNPQ"
	doc := map[string]any{"class": "visitseries", "isDeleted": true, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, dead, b); err != nil {
		t.Fatalf("seed tombstoned series: %v", err)
	}
	// Its provider practises at exactly one site, so the ONLY thing standing
	// between this dispatch and a written link is the liveness guard.
	_, deadID, _ := substrate.ParseVertexKey(dead)
	testutil.SeedLink(t, ctx, conn,
		"lnk.visitseries."+deadID+".withProvider.provider."+vsProviderAID,
		"withProvider", dead, vsProviderAKey)

	crSubmit(t, ctx, conn, cp, cons, "vsbft00002", "BackfillVisitSeriesSite", "visitseries",
		`{"seriesKey":"`+dead+`"}`, []string{dead}, processor.OutcomeRejected)

	if !crMissing(t, ctx, conn, vsAtSiteLinkKey(dead, vsBuildingAKey)) {
		t.Fatalf("no atSite link may be written for a tombstoned series")
	}
}

// TestVisitSeries_SetSite_WritesLinkAndGuard proves the human override: with the
// provider practising at TWO sites — exactly the case BackfillVisitSeriesSite
// refuses to guess — a staffer names one, and the op writes the atSite link plus
// the CreateOnly .siteAssignment guard that serializes concurrent callers.
func TestVisitSeries_SetSite_WritesLinkAndGuard(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "vssetsite", Instance: "cr-vssetsite"})
	seedVisitSeriesSiteTopology(t, ctx, conn, []string{vsBuildingAKey, vsBuildingBKey}, nil)

	seriesKey := startSitelessSeries(t, ctx, conn, cp, cons, "vsset00001", vsProviderAKey)
	crSubmit(t, ctx, conn, cp, cons, "vsset00002", "SetVisitSeriesSite", "visitseries",
		`{"seriesKey":"`+seriesKey+`","site":"`+vsBuildingBKey+`"}`, []string{seriesKey}, processor.OutcomeAccepted)

	doc := crReadDoc(t, ctx, conn, vsAtSiteLinkKey(seriesKey, vsBuildingBKey))
	if tv, _ := doc["targetVertex"].(string); tv != vsBuildingBKey {
		t.Fatalf("link targetVertex = %q, want the CHOSEN site %q", tv, vsBuildingBKey)
	}
	if crMissing(t, ctx, conn, vsAtSiteLinkKey(seriesKey, vsBuildingBKey)) {
		t.Fatalf("the chosen site's atSite link must be written")
	}
	if !crMissing(t, ctx, conn, vsAtSiteLinkKey(seriesKey, vsBuildingAKey)) {
		t.Fatalf("only the CHOSEN site may be linked, never every site the provider practises at")
	}

	guard := crReadDoc(t, ctx, conn, seriesKey+".siteAssignment")
	if guard["class"] != "visitSeriesSiteAssignment" {
		t.Fatalf("guard aspect class = %v, want visitSeriesSiteAssignment", guard["class"])
	}
	if del, _ := guard["isDeleted"].(bool); del {
		t.Fatalf("the guard aspect must be alive — it is what makes a second concurrent commit reject")
	}
}

// TestVisitSeries_SetSite_RejectsUnpractisedSite proves the site is HARD
// validated, never a silent fall-through: a building the series' own provider
// does not practicesAt is rejected, so this op cannot invent a practice
// relationship that AssignProviderSite has not established.
func TestVisitSeries_SetSite_RejectsUnpractisedSite(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "vssetbad", Instance: "cr-vssetbad"})
	seedVisitSeriesSiteTopology(t, ctx, conn, []string{vsBuildingAKey}, nil)

	seriesKey := startSitelessSeries(t, ctx, conn, cp, cons, "vssetb00001", vsProviderAKey)
	crSubmit(t, ctx, conn, cp, cons, "vssetb00002", "SetVisitSeriesSite", "visitseries",
		`{"seriesKey":"`+seriesKey+`","site":"`+vsBuildingBKey+`"}`, []string{seriesKey}, processor.OutcomeRejected)

	if !crMissing(t, ctx, conn, vsAtSiteLinkKey(seriesKey, vsBuildingBKey)) {
		t.Fatalf("a rejected SetVisitSeriesSite must write no atSite link")
	}
	// The POSITIVE control: the same op with a site the provider DOES practise
	// at is accepted, so the rejection above is attributable to the membership
	// check rather than to anything else on the path.
	crSubmit(t, ctx, conn, cp, cons, "vssetb00003", "SetVisitSeriesSite", "visitseries",
		`{"seriesKey":"`+seriesKey+`","site":"`+vsBuildingAKey+`"}`, []string{seriesKey}, processor.OutcomeAccepted)
	if crMissing(t, ctx, conn, vsAtSiteLinkKey(seriesKey, vsBuildingAKey)) {
		t.Fatalf("a practised site must be settable")
	}
}

// TestVisitSeries_SetSite_NoopAlreadySited proves reassignment is out of scope:
// once a series carries a live atSite link, a second SetVisitSeriesSite naming a
// DIFFERENT (equally valid) site no-ops cleanly rather than moving it — and
// writes no second link.
func TestVisitSeries_SetSite_NoopAlreadySited(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "vssetdup", Instance: "cr-vssetdup"})
	seedVisitSeriesSiteTopology(t, ctx, conn, []string{vsBuildingAKey, vsBuildingBKey}, nil)

	seriesKey := startSitelessSeries(t, ctx, conn, cp, cons, "vssetd00001", vsProviderAKey)
	crSubmit(t, ctx, conn, cp, cons, "vssetd00002", "SetVisitSeriesSite", "visitseries",
		`{"seriesKey":"`+seriesKey+`","site":"`+vsBuildingAKey+`"}`, []string{seriesKey}, processor.OutcomeAccepted)

	crSubmit(t, ctx, conn, cp, cons, "vssetd00003", "SetVisitSeriesSite", "visitseries",
		`{"seriesKey":"`+seriesKey+`","site":"`+vsBuildingBKey+`"}`, []string{seriesKey}, processor.OutcomeAccepted)

	if !crMissing(t, ctx, conn, vsAtSiteLinkKey(seriesKey, vsBuildingBKey)) {
		t.Fatalf("a re-site must not write a second atSite link — reassignment is out of scope")
	}
	first := crReadDoc(t, ctx, conn, vsAtSiteLinkKey(seriesKey, vsBuildingAKey))
	if del, _ := first["isDeleted"].(bool); del {
		t.Fatalf("the original atSite link must survive a no-op re-site; got isDeleted=%v", del)
	}
}

// remTombstoneVertex soft-deletes a seeded vertex in place — isDeleted: true
// with every link it carries left live, the shape TombstoneLocation leaves
// behind (it cascades to no practicesAt link).
func remTombstoneVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class string) {
	t.Helper()
	doc := map[string]any{"class": class, "isDeleted": true, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("tombstone vertex %s: %v", key, err)
	}
}

// TestVisitSeries_SiteOps_SkipDecommissionedBuilding pins the liveness screen
// sites_for_provider applies to a practicesAt link's TARGET, which both site ops
// inherit as their candidate set and their whitelist.
//
// The vector that matters is the FIRST: a provider practising at exactly one
// building, which is then decommissioned. Without the screen the exactly-one
// rule fires and wires the series to a dead building — and that is worse than
// leaving it unsited, because every read walk drops a tombstoned vertex (so the
// link confers NO workplace anchor, the exact stranding this mechanism exists to
// prevent) while series_site() sees a live LINK and makes both site ops, and the
// missing_series_site gap itself, no-op forever. Unrecoverable from one write.
func TestVisitSeries_SiteOps_SkipDecommissionedBuilding(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "vsdeadsite", Instance: "cr-vsdeadsite"})
	// Provider A practises at building A alone; provider B at both.
	seedVisitSeriesSiteTopology(t, ctx, conn, []string{vsBuildingAKey}, []string{vsBuildingAKey, vsBuildingBKey})
	// Building A is decommissioned after the fact — the practicesAt links to it
	// stay live, because TombstoneLocation cascades to nothing.
	remTombstoneVertex(t, ctx, conn, vsBuildingAKey, "building")

	t.Run("backfill does not wire a dead building", func(t *testing.T) {
		seriesKey := startSitelessSeries(t, ctx, conn, cp, cons, "vsdead00001", vsProviderAKey)
		crSubmit(t, ctx, conn, cp, cons, "vsdead00002", "BackfillVisitSeriesSite", "visitseries",
			`{"seriesKey":"`+seriesKey+`"}`, []string{seriesKey}, processor.OutcomeAccepted)

		if !crMissing(t, ctx, conn, vsAtSiteLinkKey(seriesKey, vsBuildingAKey)) {
			t.Fatalf("a decommissioned building must not be backfilled as the series' site — the link would " +
				"confer no workplace anchor AND permanently block both site ops")
		}
		// It stays a candidate for the gap, not a converged row: the series is
		// still unsited, so missing_series_site is still true and a later
		// AssignProviderSite to a live building can still close it.
		if !crMissing(t, ctx, conn, seriesKey+".siteAssignment") {
			t.Fatalf("no site-assignment guard may be claimed when nothing was sited")
		}
	})

	t.Run("set rejects a dead building", func(t *testing.T) {
		seriesKey := startSitelessSeries(t, ctx, conn, cp, cons, "vsdead00003", vsProviderBKey)
		crSubmit(t, ctx, conn, cp, cons, "vsdead00004", "SetVisitSeriesSite", "visitseries",
			`{"seriesKey":"`+seriesKey+`","site":"`+vsBuildingAKey+`"}`, []string{seriesKey}, processor.OutcomeRejected)
		if !crMissing(t, ctx, conn, vsAtSiteLinkKey(seriesKey, vsBuildingAKey)) {
			t.Fatalf("a rejected SetVisitSeriesSite must write no atSite link")
		}
		// POSITIVE control on the SAME series: provider B's other building is
		// alive and still settable, so the rejection above is attributable to
		// the target-liveness screen and not to anything else on the path.
		crSubmit(t, ctx, conn, cp, cons, "vsdead00005", "SetVisitSeriesSite", "visitseries",
			`{"seriesKey":"`+seriesKey+`","site":"`+vsBuildingBKey+`"}`, []string{seriesKey}, processor.OutcomeAccepted)
		if crMissing(t, ctx, conn, vsAtSiteLinkKey(seriesKey, vsBuildingBKey)) {
			t.Fatalf("a live site of the same provider must remain settable")
		}
	})

	t.Run("backfill converges on the surviving site when only the dead one drops", func(t *testing.T) {
		// Provider C practises at the same two buildings as B, one now dead — so
		// after the screen exactly ONE live site remains and the exactly-one rule
		// SHOULD fire. This is what stops the screen from being read as "any
		// tombstoned site makes the provider ambiguous". A THIRD provider, not a
		// reuse of B: the per-(patient, provider) active-series guard would
		// reject a second series for the pair the sub-test above already used,
		// and this vector would then fail for a reason that has nothing to do
		// with sites.
		remSeedVertex(t, ctx, conn, vsProviderCKey, "provider")
		for _, site := range []string{vsBuildingAKey, vsBuildingBKey} {
			_, siteID, _ := substrate.ParseVertexKey(site)
			testutil.SeedLink(t, ctx, conn,
				"lnk.provider."+vsProviderCID+".practicesAt.building."+siteID,
				"practicesAt", vsProviderCKey, site)
		}
		seriesKey := startSitelessSeries(t, ctx, conn, cp, cons, "vsdead00006", vsProviderCKey)
		crSubmit(t, ctx, conn, cp, cons, "vsdead00007", "BackfillVisitSeriesSite", "visitseries",
			`{"seriesKey":"`+seriesKey+`"}`, []string{seriesKey}, processor.OutcomeAccepted)

		doc := crReadDoc(t, ctx, conn, vsAtSiteLinkKey(seriesKey, vsBuildingBKey))
		if tv, _ := doc["targetVertex"].(string); tv != vsBuildingBKey {
			t.Fatalf("link targetVertex = %q, want the one SURVIVING site %q", tv, vsBuildingBKey)
		}
		if !crMissing(t, ctx, conn, vsAtSiteLinkKey(seriesKey, vsBuildingAKey)) {
			t.Fatalf("the dead building must not be linked")
		}
	})
}

// TestVisitSeries_BackfillSite_ClaimsSiteAssignmentGuard pins the CreateOnly
// guard on the BACKFILL path too, not only on Set. Backfill chooses nothing, but
// the set it reads MOVES: a provider gaining a second site between a Backfill's
// read and a concurrent Set's would otherwise let both commit different,
// non-colliding atSite links onto one series — breaking the at-most-one
// invariant series_site()'s early return and the read spec's either/or CASE both
// rest on. The guard's key does not vary with the chosen site, so it is the lock.
func TestVisitSeries_BackfillSite_ClaimsSiteAssignmentGuard(t *testing.T) {
	ctx, conn := setupRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "vsbfguard", Instance: "cr-vsbfguard"})
	seedVisitSeriesSiteTopology(t, ctx, conn, []string{vsBuildingAKey}, nil)

	seriesKey := startSitelessSeries(t, ctx, conn, cp, cons, "vsbfg00001", vsProviderAKey)
	crSubmit(t, ctx, conn, cp, cons, "vsbfg00002", "BackfillVisitSeriesSite", "visitseries",
		`{"seriesKey":"`+seriesKey+`"}`, []string{seriesKey}, processor.OutcomeAccepted)

	guard := crReadDoc(t, ctx, conn, seriesKey+".siteAssignment")
	if guard["class"] != "visitSeriesSiteAssignment" {
		t.Fatalf("guard aspect class = %v, want visitSeriesSiteAssignment", guard["class"])
	}
	if del, _ := guard["isDeleted"].(bool); del {
		t.Fatalf("the guard aspect must be alive — it is what makes a racing writer's whole batch reject")
	}
	// And a re-dispatch is still a clean no-op: the already-sited early return
	// fires before any mutation, so the CreateOnly guard is never re-attempted
	// (which would reject the redelivery Weaver produces routinely).
	crSubmit(t, ctx, conn, cp, cons, "vsbfg00003", "BackfillVisitSeriesSite", "visitseries",
		`{"seriesKey":"`+seriesKey+`"}`, []string{seriesKey}, processor.OutcomeAccepted)
}
