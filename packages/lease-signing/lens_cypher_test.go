package leasesigning

// Rule-engine proof of the leaseApplicationComplete convergence cypher.
//
// These tests drive the lens spec (leaseApplicationCompleteSpec) through the
// `full` rule engine directly — the same engine selected at activation via
// engine:"full" — against an embedded NATS Core/Adjacency KV. They prove the
// cypher is ONE ROW PER ANCHOR (the §0.C guard would fail closed otherwise) and
// that the gap columns are strict bools that flip on a direct .outcome /
// .signature / .ssn write (AC #1 reprojection-on-linked-constituent + AC #4
// direct-write; the live bridge round-trip is 14.5).
//
// NOTE: these assert the engine PROJECTION (the rule-engine row). The bucket
// round-trip of the SCALAR convergence columns through the actorAggregate
// projection EnvelopeFn (scalar passthrough — Contract #6 §6.13, CAR E6) is
// proven in internal/refractor's lease-signing scalar convergence e2e; the cypher
// itself is proven here.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

type lensFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string // logicalName -> bare NanoID
	types         map[string]string // bare NanoID -> type
}

func newLensFixture(t *testing.T) *lensFixture {
	adjKV, coreKV := lenstest.KVs(t)
	return &lensFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *lensFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	return f.vtxWithClass(t, name, typ, typ)
}

// vtxWithClass creates a vertex whose KEY type (vtx.<keyType>.<id>) differs from
// its envelope CLASS — the P7 shape: a service instance keys under `service`
// (so a lens MATCH (inst:service) binds via the key-type) while its envelope
// class carries the fine-grained discriminator service.<family>.instance.
func (f *lensFixture) vtxWithClass(t *testing.T, name, keyType, class string) string {
	t.Helper()
	id := lenstest.NanoID(name)
	f.ids[name] = id
	f.types[id] = keyType
	key := "vtx." + keyType + "." + id
	body := map[string]any{"key": key, "class": class, "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *lensFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[ownerName]] + "." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func (f *lensFixture) edge(t *testing.T, name, fromName, toName string) {
	t.Helper()
	ctx := context.Background()
	fromID, toID := f.ids[fromName], f.ids[toName]
	fromType, toType := f.types[fromID], f.types[toID]
	linkKey := "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID
	edgeID := name + "_" + fromID + "_" + toID
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType}))
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType}))
}

// farFutureValidUntil is a validUntil far enough ahead that a bgcheck stamped
// with it reads FRESH in any fixture recording no lapse against it. Used by the
// gap-closure tests that care about completeness, not the freshness boundary.
// Freshness reads no clock, so the distance from the wall clock buys nothing on
// its own — what makes an instance fresh is the ABSENCE of a lapse recorded at
// or after this instant.
const farFutureValidUntil = "2099-01-01T00:00:00Z"

// recordBgcheckLapse writes the freshnessExpiry marker MarkExpired commits onto
// a background-check instance when the backgroundCheckFreshness target's @at
// fires: `at` is the deadline the timer fired for, recorded under that target's
// own key in byTarget, with expiredAt carrying the entity-wide maximum (the same
// instant on a single-target entity). A marker at or after the instance's
// validUntil is a recorded lapse; one before it is a fire for an earlier
// deadline the current one has outrun.
func recordBgcheckLapse(t *testing.T, f *lensFixture, instName, at string) {
	t.Helper()
	f.aspect(t, instName, "freshnessExpiry", "freshnessExpiry", map[string]any{
		"expiredAt": at,
		"byTarget":  map[string]any{"backgroundCheckFreshness": at},
	})
}

// recordSiblingLapse writes a marker carrying ONLY another target's entry — the
// isolation vector. byTarget is keyed per target precisely so one target's fire
// cannot be read as another's, and a bare presence test on the marker aspect
// would miss that.
func recordSiblingLapse(t *testing.T, f *lensFixture, instName, siblingTargetID, at string) {
	t.Helper()
	f.aspect(t, instName, "freshnessExpiry", "freshnessExpiry", map[string]any{
		"expiredAt": at,
		"byTarget":  map[string]any{siblingTargetID: at},
	})
}

func (f *lensFixture) project(t *testing.T, appName string) []ruleengine.ProjectionResult {
	t.Helper()
	return f.projectAt(t, appName, time.Now().UTC().Format(time.RFC3339))
}

// projectAt runs the lens with an INJECTED $now so freshness boundary tests can
// place validUntil before/after the projection instant deterministically. $now
// here is the same param the live pipeline supplies (executeFullForActor sets
// params["now"] = time.Now().UTC().Format(time.RFC3339)).
func (f *lensFixture) projectAt(t *testing.T, appName, now string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(leaseApplicationCompleteSpec)
	require.NoError(t, err, "leaseApplicationComplete cypher must parse on the full engine")
	appKey := "vtx.leaseapp." + f.ids[appName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    appKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// TestLeaseApplicationComplete_ProjectsOneRowPerAnchor (test 1 — AC #1 + §0.C).
// A multi-instance fixture (the applicant has 2 service instances) is the
// fan-out case the one-row-per-anchor guard would trip. The cypher MUST emit
// exactly one row carrying the right gap columns + non-null entityKey/applicant.
func TestLeaseApplicationComplete_ProjectsOneRowPerAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	appKey := f.vtx(t, "app", "leaseapp")
	idKey := f.vtx(t, "alice", "identity")
	f.vtx(t, "unit1", "unit")
	f.edge(t, "appliesToUnit", "app", "unit1")
	// TWO service instances providedTo alice (one bgcheck, one payment), no
	// outcome yet — the multi-instance fan-out case.
	f.vtxWithClass(t, "bg1", "service", "service.backgroundCheck.instance")
	f.vtxWithClass(t, "pay1", "service", "service.payment.instance")
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "pay1", "alice")

	rows := f.project(t, "app")
	require.Len(t, rows, 1, "exactly one weaver-targets row per leaseapp anchor even with 2 instances (§0.C guard)")
	v := rows[0].Values
	require.Equal(t, appKey, v["entityKey"], "entityKey must be the full leaseapp key")
	require.Equal(t, idKey, v["applicant"], "applicant must be the full identity key")
	require.Equal(t, true, v["missing_onboarding"])
	require.Equal(t, false, v["missing_bgcheck"], "missing_bgcheck requires onboarding (ssnVal <> null) first — not yet applicable to remediate")
	require.Equal(t, true, v["missing_payment"])
	require.Equal(t, true, v["missing_signature"])
	require.Equal(t, true, v["violating"])
	// the row key (actorKey, the first RETURN column) is the full leaseapp key;
	// the bare-NanoID convergence key is derived by BuildKey from it (14.2).
	require.Equal(t, appKey, v["actorKey"])
}

// TestLeaseApplicationComplete_OutcomeFlipsGap_DirectWrite (test 2 — AC #1 + AC
// #4 + the dependent freshness). From all-gaps-open, recording the bgcheck
// instance's .outcome (status completed, validUntil in the future) flips
// missing_bgcheck false while a payment instance with NO outcome leaves
// missing_payment true; recording the applicant's ssn flips missing_onboarding
// false. Still exactly one row.
func TestLeaseApplicationComplete_OutcomeFlipsGap_DirectWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.vtx(t, "unit1", "unit")
	f.edge(t, "appliesToUnit", "app", "unit1")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"}) // onboarded
	f.vtxWithClass(t, "bg1", "service", "service.backgroundCheck.instance")
	// A FRESH completed bgcheck: validUntil far in the future (time-independent).
	f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-01T00:00:00Z", "validUntil": farFutureValidUntil})
	f.vtxWithClass(t, "pay1", "service", "service.payment.instance")
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "pay1", "alice")

	rows := f.project(t, "app")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_onboarding"], "ssn present → onboarded")
	require.Equal(t, false, v["missing_bgcheck"], "completed AND fresh bgcheck outcome → not missing")
	require.Equal(t, true, v["missing_payment"], "payment instance with no outcome → still missing")
	require.Equal(t, true, v["missing_signature"], "no signature yet")
	require.Equal(t, true, v["violating"], "payment + signature still open → violating")
}

// TestLeaseApplicationComplete_SignatureFlipsGap (test 8 — the assignTask gap
// closure at the lens level). Writing the leaseapp's .signature aspect flips
// missing_signature false; with all four applicant gaps closed BUT no landlord
// decision the row is qualified-awaiting-decision: missing_decision true, the row
// stays violating (its work is not done until the landlord decides) but NO listing
// flip fires.
func TestLeaseApplicationComplete_SignatureFlipsGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})
	f.aspect(t, "app", "signature", "signature", map[string]any{"signedAt": "2026-06-10T00:00:00Z"})
	f.vtxWithClass(t, "bg1", "service", "service.backgroundCheck.instance")
	f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-01T00:00:00Z", "validUntil": farFutureValidUntil})
	f.vtxWithClass(t, "pay1", "service", "service.payment.instance")
	// Payment is ever-completed: it carries NO validUntil here, proving the lens
	// does not apply the freshness gate to payment.
	f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-02T00:00:00Z"})
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "pay1", "alice")

	rows := f.project(t, "app")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_signature"], "signature present → not missing")
	require.Equal(t, "2026-06-10T00:00:00Z", v["signedAt"], "signedAt projects the .signature execution date for the produced lease artifact")
	require.Equal(t, false, v["missing_onboarding"])
	require.Equal(t, false, v["missing_bgcheck"])
	require.Equal(t, false, v["missing_payment"], "completed payment with no validUntil → not missing (ever-completed)")
	require.Equal(t, true, v["applicantApproved"], "all four applicant gaps closed → qualified")
	require.Equal(t, true, v["missing_decision"], "qualified but no landlord decision → awaiting decision")
	require.Equal(t, false, v["missing_listingLeased"], "no landlord approval → no listing flip")
	require.Equal(t, false, v["declined"], "no rejection → not declined")
	require.Equal(t, true, v["violating"], "qualified-awaiting-decision is still violating (work not done until landlord decides)")
}

// TestLeaseApplicationComplete_ProjectsUnitColumns (Increment 2): the appliesToUnit
// walk projects the leased unit's key / address / rent as informational columns,
// one row per anchor preserved (appliesToUnit is 0..1). The unit columns are NOT
// in violating — they are display-only (unit is required at create, no gap).
func TestLeaseApplicationComplete_ProjectsUnitColumns(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	appKey := f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	unitKey := f.vtx(t, "unit1", "unit")
	f.aspect(t, "unit1", "address", "address", map[string]any{"line1": "123 Loft St", "city": "San Francisco", "region": "CA"})
	f.aspect(t, "unit1", "listing", "listing", map[string]any{"rentAmount": 2400, "rentCurrency": "USD", "bedrooms": 2, "bathrooms": 1.5, "leaseTermMonths": 12, "availableFrom": "2026-08-01T00:00:00Z", "status": "available"})
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "appliesToUnit", "app", "unit1")

	rows := f.project(t, "app")
	require.Len(t, rows, 1, "exactly one row per anchor even with the appliesToUnit walk")
	v := rows[0].Values
	require.Equal(t, appKey, v["entityKey"])
	require.Equal(t, unitKey, v["unitKey"], "unitKey carried through the WITH from the appliesToUnit walk")
	require.Equal(t, "123 Loft St", v["unitAddress"], "unitAddress aspect-hops u.address.line1")
	require.Equal(t, "San Francisco", v["unitCity"], "unitCity aspect-hops u.address.city")
	require.Equal(t, "CA", v["unitRegion"], "unitRegion aspect-hops u.address.region")
	require.EqualValues(t, 2400, v["unitRent"], "unitRent aspect-hops u.listing.rentAmount")
	require.Equal(t, "USD", v["unitCurrency"], "unitCurrency aspect-hops u.listing.rentCurrency")
	require.EqualValues(t, 2, v["unitBedrooms"], "unitBedrooms aspect-hops u.listing.bedrooms")
	require.EqualValues(t, 1.5, v["unitBathrooms"], "unitBathrooms aspect-hops u.listing.bathrooms")
	require.EqualValues(t, 12, v["unitLeaseTermMonths"], "unitLeaseTermMonths aspect-hops u.listing.leaseTermMonths")
	require.Equal(t, "2026-08-01T00:00:00Z", v["unitAvailableFrom"], "unitAvailableFrom aspect-hops u.listing.availableFrom")
	require.Equal(t, "available", v["unitStatus"], "unitStatus aspect-hops u.listing.status")
	// The four applicant gaps still open (no ssn/bgcheck/payment/signature); the
	// unit columns + applicantApproved/missing_listingLeased do not flip yet.
	require.Equal(t, false, v["applicantApproved"], "no applicant gaps closed → not approved")
	require.Equal(t, false, v["missing_listingLeased"], "not approved → listing gap stays closed")
	require.Equal(t, true, v["violating"], "the unit columns are informational, not gaps")
}

// TestLeaseApplicationComplete_ProjectsLeaseTerms: the applicant's own requested
// .terms aspect (moveInDate / leaseTermMonths / requestedRent), written by
// CreateLeaseApplication when moveInDate is supplied, projects as informational
// scalar columns so the applicant FE can render a "terms you're agreeing to"
// panel. Like the unit columns these are display-only — never in violating — and
// an application without a .terms aspect projects null terms columns (the
// graceful-degrade path the panel relies on).
func TestLeaseApplicationComplete_ProjectsLeaseTerms(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.vtx(t, "unit1", "unit")
	f.aspect(t, "unit1", "listing", "listing", map[string]any{"rentAmount": 2400, "status": "available"})
	f.aspect(t, "app", "terms", "terms", map[string]any{"moveInDate": "2026-09-01T00:00:00Z", "leaseTermMonths": 18, "requestedRent": 2300})
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "appliesToUnit", "app", "unit1")

	rows := f.project(t, "app")
	require.Len(t, rows, 1, "the .terms walk keeps one row per anchor")
	v := rows[0].Values
	require.Equal(t, "2026-09-01T00:00:00Z", v["termsMoveInDate"], "termsMoveInDate aspect-hops app.terms.moveInDate")
	require.EqualValues(t, 18, v["termsLeaseTermMonths"], "termsLeaseTermMonths aspect-hops app.terms.leaseTermMonths")
	require.EqualValues(t, 2300, v["termsRequestedRent"], "termsRequestedRent aspect-hops app.terms.requestedRent")
	require.Equal(t, true, v["violating"], "the terms columns are informational, not gaps")

	// Graceful degrade: an application with no .terms aspect projects null terms.
	f.vtx(t, "app2", "leaseapp")
	f.edge(t, "applicationFor", "app2", "alice")
	f.edge(t, "appliesToUnit", "app2", "unit1")
	rows2 := f.project(t, "app2")
	require.Len(t, rows2, 1)
	require.Nil(t, rows2[0].Values["termsMoveInDate"], "no .terms → null move-in (the FE omits the row)")
	require.Nil(t, rows2[0].Values["termsLeaseTermMonths"])
	require.Nil(t, rows2[0].Values["termsRequestedRent"])
}

// TestLeaseApplicationComplete_ProjectsQualificationProfile proves the
// .applicationSignals aspect's DERIVED qualification signals project as
// read-only scalar columns the landlord surface reads, while the RAW
// financials (annualIncome / employerName / the reference strings, on
// .profile) and the third-party identifiers (guarantorName, on
// .underwritingParties) stay UNprojected. The signals feed no gap (the row
// stays violating for the unrelated applicant gaps), and an application with
// no .applicationSignals degrades to null signal columns +
// profileSubmitted=false.
func TestLeaseApplicationComplete_ProjectsQualificationProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.edge(t, "applicationFor", "app", "alice")
	// The three aspects as SetApplicantProfile writes them in one batch. The
	// lens reads ONLY .applicationSignals.
	f.aspect(t, "app", "profile", "applicantProfile", map[string]any{
		"annualIncome":     96000, // raw — must NOT appear in any projected column
		"employmentStatus": "employed",
		"employerName":     "Acme Corp", // raw — must NOT appear
	})
	f.aspect(t, "app", "underwritingParties", "underwritingParties", map[string]any{
		"references":    []any{"Prior landlord", "Manager"}, // third-party names — must NOT appear
		"guarantorName": "Pat Guarantor",                    // third-party identifier — must NOT appear
	})
	f.aspect(t, "app", "applicationSignals", "applicationSignals", map[string]any{
		"incomeToRentMet":          true,
		"employmentVerified":       true,
		"referenceCount":           2,
		"hasCoApplicant":           false,
		"hasGuarantor":             true,
		"guarantorIncomeToRentMet": true, // derived — projects
		"submittedAt":              "2026-06-27T10:00:00Z",
	})

	rows := f.project(t, "app")
	require.Len(t, rows, 1, "the .applicationSignals walk keeps one row per anchor")
	v := rows[0].Values
	require.Equal(t, true, v["profileSubmitted"], "profileSubmitted derives from .applicationSignals.submittedAt <> null")
	require.Equal(t, true, v["incomeToRentMet"], "the stored derived bool projects verbatim")
	require.Equal(t, true, v["employmentVerified"])
	require.EqualValues(t, 2, v["referenceCount"], "referenceCount projects the op-derived count")
	require.Equal(t, false, v["hasCoApplicant"])
	require.Equal(t, true, v["hasGuarantor"])
	require.Equal(t, true, v["guarantorIncomeToRentMet"], "the derived guarantor income signal projects verbatim")
	// The RAW financials + third-party identifiers must never reach the read model.
	require.NotContains(t, v, "annualIncome", "raw income must not be projected")
	require.NotContains(t, v, "employerName", "raw employer must not be projected")
	require.NotContains(t, v, "references", "raw reference strings must not be projected")
	require.NotContains(t, v, "guarantorName", "the guarantor's own identifier must not be projected")
	require.NotContains(t, v, "guarantorAnnualIncome", "raw guarantor income must not be projected")

	// Graceful degrade: no .applicationSignals → null signals + profileSubmitted=false.
	f.vtx(t, "app2", "leaseapp")
	f.edge(t, "applicationFor", "app2", "alice")
	rows2 := f.project(t, "app2")
	require.Len(t, rows2, 1)
	require.Equal(t, false, rows2[0].Values["profileSubmitted"], "no .applicationSignals → profileSubmitted false")
	require.Nil(t, rows2[0].Values["incomeToRentMet"], "no .applicationSignals → null income signal")
	require.Nil(t, rows2[0].Values["referenceCount"])
	require.Nil(t, rows2[0].Values["hasGuarantor"])
	require.Nil(t, rows2[0].Values["guarantorIncomeToRentMet"], "no .applicationSignals → null guarantor signal")
}

// landlordDecision writes the leaseapp's .decision aspect {value, decidedAt} — the
// fact DecideLeaseApplication commits. decision is approved | declined.
func (f *lensFixture) landlordDecision(t *testing.T, appName, decision string) {
	t.Helper()
	f.aspect(t, appName, "decision", "decision", map[string]any{"value": decision, "decidedAt": "2026-06-26T10:00:00Z"})
}

// landlordDecisionReason writes the .decision aspect with an optional reason — the
// shape DecideLeaseApplication commits when a landlord declines with a rationale.
func (f *lensFixture) landlordDecisionReason(t *testing.T, appName, decision, reason string) {
	t.Helper()
	f.aspect(t, appName, "decision", "decision", map[string]any{"value": decision, "decidedAt": "2026-06-26T10:00:00Z", "reason": reason})
}

// approvedAppFixture seeds a QUALIFIED application: alice onboarded (.ssn) + the
// application signed (.signature), a fresh completed bgcheck, a completed
// payment, and the converged executed-lease document chain (a signed
// application's done-state includes the produced + anchored document) — the
// applicant-side gaps all closed (applicantApproved=true), but with NO landlord
// decision yet (the qualified-awaiting-decision state). Tests layer a unit and
// (where they exercise the listing flip) a landlordDecision on top to drive the
// landlord-gated listing-leased convergence.
func approvedAppFixture(t *testing.T) *lensFixture {
	t.Helper()
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})
	f.aspect(t, "app", "signature", "signature", map[string]any{"signedAt": "2026-06-10T00:00:00Z"})
	f.vtxWithClass(t, "bg1", "service", "service.backgroundCheck.instance")
	f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-01T00:00:00Z", "validUntil": farFutureValidUntil})
	f.vtxWithClass(t, "pay1", "service", "service.payment.instance")
	f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-02T00:00:00Z"})
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "pay1", "alice")
	attachedLeaseDoc(t, f)
	return f
}

// TestLeaseApplicationComplete_ListingLeasedGap_OpensWhenLandlordApprovedAndAvailable:
// a LANDLORD-APPROVED qualified application on a still-available unit OPENS
// missing_listingLeased and stays violating until the unit is leased.
// applicantApproved + landlordApproved are both true; missing_decision is closed.
func TestLeaseApplicationComplete_ListingLeasedGap_OpensWhenLandlordApprovedAndAvailable(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := approvedAppFixture(t)
	f.landlordDecision(t, "app", "approved")
	f.vtx(t, "unit1", "unit")
	f.aspect(t, "unit1", "listing", "listing", map[string]any{"rentAmount": 2400, "status": "available"})
	f.edge(t, "appliesToUnit", "app", "unit1")

	rows := f.project(t, "app")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_onboarding"])
	require.Equal(t, false, v["missing_bgcheck"])
	require.Equal(t, false, v["missing_payment"])
	require.Equal(t, false, v["missing_signature"])
	require.Equal(t, true, v["applicantApproved"], "all four applicant gaps closed → qualified")
	require.Equal(t, "approved", v["landlordDecision"], "the landlord's decision is projected")
	require.Equal(t, true, v["landlordApproved"], "landlord approved")
	require.Equal(t, false, v["landlordDeclined"])
	require.Equal(t, false, v["missing_decision"], "the landlord has decided → no awaiting-decision gap")
	require.Equal(t, "available", v["unitStatus"])
	require.Equal(t, true, v["missing_listingLeased"], "landlord-approved + unit not leased → listing gap opens")
	require.Equal(t, true, v["violating"], "the listing gap keeps the row violating so Weaver dispatches the flip")
}

// TestLeaseApplicationComplete_QualifiedAwaitingDecision: a qualified application
// with NO landlord decision is in the missing_decision state — violating (work not
// done) but NO listing flip (missing_listingLeased false), NOT declined. This is the
// human-gate state: nothing auto-leases on applicant-readiness alone.
func TestLeaseApplicationComplete_QualifiedAwaitingDecision(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := approvedAppFixture(t)
	f.vtx(t, "unit1", "unit")
	f.aspect(t, "unit1", "listing", "listing", map[string]any{"rentAmount": 2400, "status": "available"})
	f.edge(t, "appliesToUnit", "app", "unit1")

	rows := f.project(t, "app")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["applicantApproved"], "all four applicant gaps closed → qualified")
	require.Nil(t, v["landlordDecision"], "no decision recorded → landlordDecision null")
	require.Equal(t, false, v["landlordApproved"])
	require.Equal(t, false, v["landlordDeclined"])
	require.Equal(t, true, v["missing_decision"], "qualified + no decision → awaiting landlord review")
	require.Equal(t, "available", v["unitStatus"])
	require.Equal(t, false, v["missing_listingLeased"], "no landlord approval → NO listing flip dispatched")
	require.Equal(t, false, v["declined"], "undecided is not declined")
	require.Equal(t, true, v["violating"], "qualified-awaiting-decision is violating (work not done) but dispatches nothing")
}

// TestLeaseApplicationComplete_LandlordDeclineIsTerminal: a LANDLORD-DECLINED
// qualified application is terminal-not-violating — declined true (the FE renders
// the rejection), missing_decision false (the decision is non-null),
// missing_listingLeased false (not approved), and so violating FALSE (no work
// remains; Weaver stops reconciling).
func TestLeaseApplicationComplete_LandlordDeclineIsTerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := approvedAppFixture(t)
	f.landlordDecision(t, "app", "declined")
	f.vtx(t, "unit1", "unit")
	f.aspect(t, "unit1", "listing", "listing", map[string]any{"rentAmount": 2400, "status": "available"})
	f.edge(t, "appliesToUnit", "app", "unit1")

	rows := f.project(t, "app")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["applicantApproved"], "the applicant qualified")
	require.Equal(t, "declined", v["landlordDecision"])
	require.Equal(t, true, v["landlordDeclined"])
	require.Equal(t, true, v["declined"], "a landlord decline folds into declined (the FE's terminal banner)")
	require.Nil(t, v["declineReason"], "a reasonless decline projects null declineReason")
	require.Equal(t, false, v["missing_decision"], "a decision is recorded → no awaiting-decision gap")
	require.Equal(t, false, v["missing_listingLeased"], "declined ≠ approved → no listing flip")
	require.Equal(t, false, v["violating"], "a landlord-declined application is terminal — no work remains, Weaver stops reconciling")
}

// TestLeaseApplicationComplete_ProjectsDeclineReason pins that a landlord decline's
// optional reason projects as the declineReason column (the applicant FE renders it
// on the declined banner), and that an approve carries no reason.
func TestLeaseApplicationComplete_ProjectsDeclineReason(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	const reason = "Income below the 3x-rent threshold."
	f := approvedAppFixture(t)
	f.landlordDecisionReason(t, "app", "declined", reason)
	f.vtx(t, "unit1", "unit")
	f.aspect(t, "unit1", "listing", "listing", map[string]any{"rentAmount": 2400, "status": "available"})
	f.edge(t, "appliesToUnit", "app", "unit1")

	rows := f.project(t, "app")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["landlordDeclined"])
	require.Equal(t, reason, v["declineReason"], "the decline reason is projected for the applicant banner")

	// Re-approve (no reason) → the unconditioned upsert overwrites; declineReason gone.
	f.landlordDecision(t, "app", "approved")
	rows = f.project(t, "app")
	require.Len(t, rows, 1)
	v = rows[0].Values
	require.Equal(t, "approved", v["landlordDecision"])
	require.Nil(t, v["declineReason"], "re-approving clears the prior decline reason")
}

// TestLeaseApplicationComplete_ListingLeasedGap_RequiresApplicantReadinessEvenWhenApproved
// is the safety pin for FIX 2: missing_listingLeased requires BOTH applicant-readiness
// AND landlord approval. A landlord-APPROVED application whose applicant is NOT fully
// qualified (one applicant fact removed) must NOT open the listing gap — the unit must
// never lease to an unqualified applicant, whether the landlord approved prematurely
// or a bgcheck LAPSED after approval. Each subtest approves the landlord, then
// omits exactly one of the four applicant facts (a lapsed bgcheck is modeled by a
// recorded freshnessExpiry marker at its validUntil, which the freshness
// predicate excludes → freshBgComplete 0).
func TestLeaseApplicationComplete_ListingLeasedGap_RequiresApplicantReadinessEvenWhenApproved(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	const now = "2026-06-18T00:00:00Z"
	for _, omit := range []string{"ssn", "bgcheck-stale", "payment", "signature"} {
		t.Run("omit_"+omit, func(t *testing.T) {
			f := newLensFixture(t)
			f.vtx(t, "app", "leaseapp")
			f.vtx(t, "alice", "identity")
			// The landlord has APPROVED — yet the applicant is not (or no longer) ready.
			f.landlordDecision(t, "app", "approved")
			if omit != "ssn" {
				f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})
			}
			if omit != "signature" {
				f.aspect(t, "app", "signature", "signature", map[string]any{"signedAt": "2026-06-10T00:00:00Z"})
			}
			f.vtxWithClass(t, "bg1", "service", "service.backgroundCheck.instance")
			if omit == "bgcheck-stale" {
				// A completed bgcheck that has LAPSED after approval: its own timer
				// fired and recorded the lapse at its validUntil, so the freshness
				// predicate excludes it (the post-approval-stale race).
				const lapsedValidUntil = farFutureValidUntil
				f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-01T00:00:00Z", "validUntil": lapsedValidUntil})
				recordBgcheckLapse(t, f, "bg1", lapsedValidUntil)
			} else {
				f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-01T00:00:00Z", "validUntil": farFutureValidUntil})
			}
			f.vtxWithClass(t, "pay1", "service", "service.payment.instance")
			if omit != "payment" {
				f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-02T00:00:00Z"})
			}
			f.vtx(t, "unit1", "unit")
			f.aspect(t, "unit1", "listing", "listing", map[string]any{"rentAmount": 2400, "status": "available"})
			f.edge(t, "applicationFor", "app", "alice")
			f.edge(t, "providedTo", "bg1", "alice")
			f.edge(t, "providedTo", "pay1", "alice")
			f.edge(t, "appliesToUnit", "app", "unit1")

			rows := f.projectAt(t, "app", now)
			require.Len(t, rows, 1)
			v := rows[0].Values
			require.Equalf(t, true, v["landlordApproved"], "the landlord approved (omit %s)", omit)
			require.Equalf(t, false, v["applicantApproved"], "omitting %s leaves the applicant not qualified", omit)
			require.Equalf(t, false, v["missing_listingLeased"], "landlord-approved but applicant not ready (%s) must NOT lease the unit", omit)
			require.Equalf(t, true, v["violating"], "an open applicant gap (%s) keeps the row violating", omit)
		})
	}
}

// TestLeaseApplicationComplete_ManagerGap_OpensWhenApprovedAndUnmanaged: a
// landlord-APPROVED application whose unit carries NO `manages` link opens
// missing_manager and stays violating — the state a real DecideLeaseApplication
// approve can reach on a genuinely unmanaged unit (the op's operator/frontOfHouse
// standing grant never checks for a manager), same shape the seed-showcase bug
// (Riverside Unit 1) hit. Re-wiring a `manages` link closes the gap on
// reprojection (the unit is an appliesToUnit neighbor) with no other write.
func TestLeaseApplicationComplete_ManagerGap_OpensWhenApprovedAndUnmanaged(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := approvedAppFixture(t)
	f.landlordDecision(t, "app", "approved")
	f.vtx(t, "unit1", "unit")
	f.aspect(t, "unit1", "listing", "listing", map[string]any{"rentAmount": 2400, "status": "leased"})
	f.edge(t, "appliesToUnit", "app", "unit1")
	// Deliberately NO manages link.

	rows := f.project(t, "app")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["landlordApproved"])
	require.Equal(t, true, v["missing_manager"], "approved + unmanaged unit → the gap opens")
	require.Equal(t, true, v["violating"], "an unmanaged unit keeps the row violating (flagged, not silently dropped)")

	// Re-wire the manager — no other write — and the gap closes on reprojection.
	f.vtx(t, "landlord1", "identity")
	f.edge(t, "manages", "landlord1", "unit1")

	rows = f.project(t, "app")
	require.Len(t, rows, 1)
	v = rows[0].Values
	require.Equal(t, false, v["missing_manager"], "a manages link lands → gap closes")
	require.Equal(t, false, v["violating"], "manager assigned + unit leased → converged")
}

// TestLeaseApplicationComplete_ManagerGap_NotOpenBeforeApproval: an unmanaged
// unit does NOT open missing_manager before the landlord decides — the gap is
// gated on landlordDecision='approved' like missing_listingLeased, not on the
// unit's raw ownership state alone (an applicant browsing a not-yet-decided
// unmanaged listing is not itself a flaggable problem).
func TestLeaseApplicationComplete_ManagerGap_NotOpenBeforeApproval(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := approvedAppFixture(t)
	f.vtx(t, "unit1", "unit")
	f.aspect(t, "unit1", "listing", "listing", map[string]any{"rentAmount": 2400, "status": "available"})
	f.edge(t, "appliesToUnit", "app", "unit1")
	// No decision yet, and no manages link either.

	rows := f.project(t, "app")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Nil(t, v["landlordDecision"])
	require.Equal(t, false, v["missing_manager"], "no landlord decision yet → the manager gap is not gated open")
}

// TestLeaseApplicationComplete_ListingLeasedGap_ClosedWhenLeased: once a
// landlord-approved application's unit is leased, missing_listingLeased is false and
// the row CONVERGES (violating false) — applicantApproved + landlordApproved stay
// true. This is the post-directOp reprojection state.
func TestLeaseApplicationComplete_ListingLeasedGap_ClosedWhenLeased(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := approvedAppFixture(t)
	f.landlordDecision(t, "app", "approved")
	f.vtx(t, "unit1", "unit")
	f.aspect(t, "unit1", "listing", "listing", map[string]any{"rentAmount": 2400, "status": "leased"})
	f.edge(t, "appliesToUnit", "app", "unit1")
	f.vtx(t, "landlord1", "identity")
	f.edge(t, "manages", "landlord1", "unit1")

	rows := f.project(t, "app")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["applicantApproved"])
	require.Equal(t, true, v["landlordApproved"])
	require.Equal(t, false, v["missing_decision"])
	require.Equal(t, "leased", v["unitStatus"])
	require.Equal(t, false, v["missing_listingLeased"], "unit leased → listing gap closed")
	require.Equal(t, false, v["missing_manager"], "the unit carries a manages link → no gap")
	require.Equal(t, false, v["violating"], "landlord-approved AND unit leased → converged")
}

// TestLeaseApplicationComplete_WinnerBgcheckGap_ReopensAfterOwnUnitLeases: the
// unitStatus='leased' suppression the fix above adds must NOT wedge shut the
// WINNING applicant's own freshness reopen — their bgcheck can go stale again
// after their own unit has already leased, and the EAGER auto-reopen-at-expiry
// @at timer (FRESHNESS below) depends on missing_bgcheck reopening so Weaver
// re-dispatches a fresh check. Caught live by
// TestLeaseConvergence_BgcheckFreshness_EagerReopen
// (internal/leaseconvergence), which drives a full approve→lease→lapse→reopen
// cycle; this is the cheap lens-level regression pin for the same defect.
func TestLeaseApplicationComplete_WinnerBgcheckGap_ReopensAfterOwnUnitLeases(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	const lapsedValidUntil = farFutureValidUntil
	app := bgFreshnessFixture(t, f, lapsedValidUntil)
	recordBgcheckLapse(t, f, "bg1", lapsedValidUntil)
	f.vtx(t, "unit1", "unit")
	f.aspect(t, "unit1", "listing", "listing", map[string]any{"rentAmount": 2400, "status": "leased"})
	f.edge(t, "appliesToUnit", "app", "unit1")
	f.vtx(t, "landlord1", "identity")
	f.edge(t, "manages", "landlord1", "unit1")

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "leased", v["unitStatus"], "this IS the applicant whose unit leased")
	require.Equal(t, "approved", v["landlordDecision"], "this row is the winner, not a rival")
	require.Equal(t, true, v["missing_bgcheck"], "a lapsed bgcheck reopens even on the winner's own already-leased unit")
	require.Equal(t, true, v["violating"], "the reopened bgcheck gap must still be violating so Weaver re-dispatches")
}

// TestLeaseApplicationComplete_RivalGapsClose_WhenUnitLeasesToSomeoneElse: a
// second, unrelated application on the SAME unit — with none of its own four
// applicant gates recorded and no landlord decision on THIS application —
// stops being violating (and so stops being dispatched RecordIdentityPII /
// SignLease / the bgcheck+payment patterns) the instant the unit leases to a
// different applicant. Board fix 2026-08-23: the four applicant gaps + missing_decision
// previously carried no unitStatus term, so a rival kept re-asking for PII/a
// signature on a unit they could no longer get.
func TestLeaseApplicationComplete_RivalGapsClose_WhenUnitLeasesToSomeoneElse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "rival", "leaseapp")
	f.vtx(t, "bob", "identity")
	f.edge(t, "applicationFor", "rival", "bob")
	f.vtx(t, "unit1", "unit")
	f.aspect(t, "unit1", "listing", "listing", map[string]any{"rentAmount": 2400, "status": "leased"})
	f.edge(t, "appliesToUnit", "rival", "unit1")
	// bob has recorded no .ssn, no bgcheck/payment instances, no .signature,
	// and the landlord never decided on THIS application.

	rows := f.project(t, "rival")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "leased", v["unitStatus"])
	require.Nil(t, v["landlordDecision"], "the rival's own application was never decided")
	require.Equal(t, false, v["missing_onboarding"], "unit already leased to someone else → no point asking for PII")
	require.Equal(t, false, v["missing_bgcheck"], "unit already leased to someone else → no point running a bgcheck")
	require.Equal(t, false, v["missing_payment"], "unit already leased to someone else → no point collecting payment")
	require.Equal(t, false, v["missing_signature"], "unit already leased to someone else → no point asking for a signature")
	require.Equal(t, false, v["missing_decision"], "no decision left to make once the unit is gone")
	require.Equal(t, false, v["missing_listingLeased"], "a rival never landlord-approved → this gap was never open")
	require.Equal(t, false, v["violating"], "a losing rival converges the instant the unit leases to someone else")
}

// TestLeaseApplicationComplete_ListingLeasedGap_NotApprovedGatesEachGap: a not-yet-
// approved application does NOT open the listing gap even on an available unit — a
// unit is leased only AFTER the applicant is FULLY approved. Each subtest omits
// exactly ONE of the four applicant facts so the "no premature lease" guarantee is
// checked at every corner (not just the missing-signature one) — this also guards
// against a desync where missing_listingLeased drops one of the four applicant
// conjuncts that applicantApproved still carries (the two re-derive them inline).
func TestLeaseApplicationComplete_ListingLeasedGap_NotApprovedGatesEachGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, omit := range []string{"ssn", "bgcheck", "payment", "signature"} {
		t.Run("omit_"+omit, func(t *testing.T) {
			f := newLensFixture(t)
			f.vtx(t, "app", "leaseapp")
			f.vtx(t, "alice", "identity")
			if omit != "ssn" {
				f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})
			}
			if omit != "signature" {
				f.aspect(t, "app", "signature", "signature", map[string]any{"signedAt": "2026-06-10T00:00:00Z"})
			}
			f.vtxWithClass(t, "bg1", "service", "service.backgroundCheck.instance")
			if omit != "bgcheck" {
				f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-01T00:00:00Z", "validUntil": farFutureValidUntil})
			}
			f.vtxWithClass(t, "pay1", "service", "service.payment.instance")
			if omit != "payment" {
				f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-02T00:00:00Z"})
			}
			f.vtx(t, "unit1", "unit")
			f.aspect(t, "unit1", "listing", "listing", map[string]any{"rentAmount": 2400, "status": "available"})
			f.edge(t, "applicationFor", "app", "alice")
			f.edge(t, "providedTo", "bg1", "alice")
			f.edge(t, "providedTo", "pay1", "alice")
			f.edge(t, "appliesToUnit", "app", "unit1")

			rows := f.project(t, "app")
			require.Len(t, rows, 1)
			v := rows[0].Values
			require.Equalf(t, false, v["applicantApproved"], "omitting %s must leave applicantApproved false", omit)
			require.Equalf(t, false, v["missing_listingLeased"], "omitting %s must keep the listing gap closed (no lease before full approval)", omit)
			require.Equalf(t, true, v["violating"], "omitting %s leaves an applicant gap open → violating", omit)
		})
	}
}

// TestLeaseApplicationComplete_ListingLeasedGap_PendingAlsoOpens: a unit in the
// intermediate 'pending' status also opens the listing gap on landlord approval (the
// gate is unitStatus <> 'leased', not == 'available'), so convergence drives it to
// leased too.
func TestLeaseApplicationComplete_ListingLeasedGap_PendingAlsoOpens(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := approvedAppFixture(t)
	f.landlordDecision(t, "app", "approved")
	f.vtx(t, "unit1", "unit")
	f.aspect(t, "unit1", "listing", "listing", map[string]any{"rentAmount": 2400, "status": "pending"})
	f.edge(t, "appliesToUnit", "app", "unit1")

	rows := f.project(t, "app")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "pending", v["unitStatus"])
	require.Equal(t, true, v["applicantApproved"])
	require.Equal(t, true, v["landlordApproved"])
	require.Equal(t, true, v["missing_listingLeased"], "pending <> leased → gap opens (drives the flip to leased)")
	require.Equal(t, true, v["violating"])
}

// TestLeaseApplicationComplete_ListingLeasedGap_NoListingNoGap: a landlord-approved
// application on a unit that has NO listing aspect does NOT open the listing gap
// (the unitStatus <> null guard). Closes the dispatch-thrash hazard — without the
// guard, SetListingStatus would reject NoListing forever while the gap stayed open.
func TestLeaseApplicationComplete_ListingLeasedGap_NoListingNoGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := approvedAppFixture(t)
	f.landlordDecision(t, "app", "approved")
	f.vtx(t, "unit1", "unit") // a unit with NO .listing aspect
	f.edge(t, "appliesToUnit", "app", "unit1")
	f.vtx(t, "landlord1", "identity")
	f.edge(t, "manages", "landlord1", "unit1")

	rows := f.project(t, "app")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["applicantApproved"])
	require.Equal(t, true, v["landlordApproved"])
	require.Equal(t, false, v["missing_decision"])
	require.Nil(t, v["unitStatus"], "no listing aspect → unitStatus null")
	require.Equal(t, false, v["missing_listingLeased"], "no listing → no transition target → gap stays closed (no thrash)")
	require.Equal(t, false, v["missing_manager"], "the unit carries a manages link → no gap")
	require.Equal(t, false, v["violating"], "landlord-approved + nothing to lease → converged")
}

// TestLeaseApplicationComplete_NoUnit_NullUnitColumns: an application with no
// appliesToUnit walk (the cypher OPTIONAL MATCH misses) projects null unit
// columns and still one row — the null-restore path (a unit is required at
// create, so this is the cypher-level safety, not a reachable production state).
func TestLeaseApplicationComplete_NoUnit_NullUnitColumns(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.edge(t, "applicationFor", "app", "alice")

	rows := f.project(t, "app")
	require.Len(t, rows, 1, "no unit walk still yields exactly one row")
	v := rows[0].Values
	require.Nil(t, v["unitKey"], "absent unit → null unitKey")
	require.Nil(t, v["unitAddress"])
	require.Nil(t, v["unitRent"])
}

// bgFreshnessFixture builds a one-applicant fixture (onboarded, signed,
// landlord-approved, a live MANAGED unit with no listing) with a completed
// payment (ever-completed, no validUntil) and a completed bgcheck whose
// validUntil is the caller's choice — the multi-instance fan-out the
// freshness tests share. All gaps but bgcheck are closed and the landlord has
// approved (so missing_decision is closed), so missing_bgcheck alone decides
// `violating`. The unit carries no .listing aspect, so missing_listingLeased
// never opens; the manages link keeps missing_manager closed (a landlord who
// already approved this application manages the unit it applies to).
// Returns the app name for projection.
func bgFreshnessFixture(t *testing.T, f *lensFixture, bgValidUntil string) string {
	t.Helper()
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.vtx(t, "unit1", "unit")
	f.edge(t, "appliesToUnit", "app", "unit1")
	f.vtx(t, "landlord1", "identity")
	f.edge(t, "manages", "landlord1", "unit1")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})
	f.aspect(t, "app", "signature", "signature", map[string]any{"signedAt": "2026-06-10T00:00:00Z"})
	f.aspect(t, "app", "decision", "decision", map[string]any{"value": "approved", "decidedAt": "2026-06-26T10:00:00Z"})
	f.vtxWithClass(t, "bg1", "service", "service.backgroundCheck.instance")
	f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-01T00:00:00Z", "validUntil": bgValidUntil})
	f.vtxWithClass(t, "pay1", "service", "service.payment.instance")
	f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-02T00:00:00Z"})
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "pay1", "alice")
	// The document leg of a signed application's done-state — closed here so the
	// freshness assertions isolate the bgcheck/payment predicates.
	attachedLeaseDoc(t, f)
	return "app"
}

// TestLeaseApplicationComplete_FreshBgcheck (freshness predicate case a). A
// completed bgcheck whose validUntil is AFTER the injected $now counts toward
// convergence: missing_bgcheck false, exactly one row even with the
// bgcheck+payment fan-out.
func TestLeaseApplicationComplete_FreshBgcheck(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	const validUntil = "2026-06-18T00:05:00Z" // 5 minutes after now → fresh
	app := bgFreshnessFixture(t, f, validUntil)

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1, "exactly one row per anchor (bgcheck+payment fan-out)")
	v := rows[0].Values
	require.Equal(t, false, v["missing_bgcheck"], "completed bgcheck with validUntil > now → not missing")
	require.Equal(t, false, v["missing_payment"])
	require.Equal(t, false, v["violating"], "all gaps closed → not violating")
}

// TestLeaseApplicationComplete_FailedBgcheck pins that a terminal FAILED outcome
// is NOT counted toward convergence. A FRESH (validUntil > now) bgcheck whose
// .outcome.status is "failed" — a definitive business rejection, now a producible
// terminal state — keeps missing_bgcheck (hence violating) TRUE. The fresh
// validUntil isolates the status from the freshness predicate: freshness is not
// the reason the instance is excluded, the status is. Guards against a refactor of
// the cypher's `outcome.status = 'completed'` CASE (e.g. to `IS NOT NULL`) that
// would silently count a failed outcome as converged — every other lens test uses
// completed/absent and would not catch it.
func TestLeaseApplicationComplete_FailedBgcheck(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	const validUntil = "2026-06-18T00:05:00Z" // fresh (> now): the status, not staleness, excludes it
	app := bgFreshnessFixture(t, f, validUntil)
	// Overwrite the fixture's completed bgcheck with a terminal FAILURE (still fresh).
	f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "failed", "completedAt": "2026-06-01T00:00:00Z", "validUntil": validUntil})

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["missing_bgcheck"], "a FAILED bgcheck (even fresh) is not converged → gap stays open")
	require.Equal(t, false, v["missing_payment"], "the fixture's payment is completed → not missing")
	require.Equal(t, true, v["violating"], "missing_bgcheck alone keeps the application violating")
	require.Equal(t, true, v["declined_bgcheck"], "a standing failed bgcheck with no completed-fresh override → declined_bgcheck")
	require.Equal(t, false, v["declined_payment"], "the fixture's payment is completed → not declined")
	require.Equal(t, true, v["declined"], "any standing rejection → declined")
}

// TestLeaseApplicationComplete_DeclinedSupersededByFreshRetry pins that the
// declined disposition tracks the CURRENT verdict: a failed bgcheck instance that
// is later superseded by a completed-fresh bgcheck (Weaver re-dispatches a fresh
// call on a failed outcome) flips declined_bgcheck back to false. The failed
// instance still exists in the graph, so this guards the (freshBgComplete = 0)
// AND-term — a naive declined = (bgFailed > 0) would wrongly stay declined after a
// successful retry.
func TestLeaseApplicationComplete_DeclinedSupersededByFreshRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	const validUntil = "2026-06-18T00:05:00Z" // fresh (> now)
	app := bgFreshnessFixture(t, f, validUntil)
	// The fixture's bg1 cleared (completed + fresh); add a SECOND, earlier bgcheck
	// instance that FAILED (a prior attempt) providedTo the same applicant (alice) —
	// the retry (bg1) then cleared.
	f.vtxWithClass(t, "bgFail", "service", "service.backgroundCheck.instance")
	f.aspect(t, "bgFail", "outcome", "outcome", map[string]any{"status": "failed", "completedAt": "2026-06-01T00:00:00Z", "validUntil": validUntil})
	f.edge(t, "providedTo2", "bgFail", "alice")

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_bgcheck"], "the fresh-completed retry closes the gap")
	require.Equal(t, false, v["declined_bgcheck"], "a completed-fresh bgcheck supersedes the earlier failure → not declined")
	require.Equal(t, false, v["declined"], "no standing rejection once the retry clears")
}

// The two HUMAN userTask gaps no longer carry a maxretries_<g> cap — the interim
// create-once cap was retired by the §10.3 general fix (Weaver's stable
// claimId-derived userTask identity dedups re-dispatch at the Processor/Loom).
// There is therefore no maxretries_onboarding/_signature column to assert here.

// TestLeaseApplicationComplete_FailedPayment pins the same predicate on the
// payment CASE (a distinct cypher line from bgcheck): a payment instance whose
// .outcome.status is "failed" is NOT counted, so missing_payment (hence violating)
// stays TRUE while the bgcheck gap is closed.
func TestLeaseApplicationComplete_FailedPayment(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	app := bgFreshnessFixture(t, f, farFutureValidUntil) // bgcheck completed + fresh → not the gap
	// Overwrite the fixture's completed payment with a terminal FAILURE.
	f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "failed", "completedAt": "2026-06-02T00:00:00Z"})

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_bgcheck"], "the fixture's bgcheck is completed + fresh → not missing")
	require.Equal(t, true, v["missing_payment"], "a FAILED payment is not converged → gap stays open")
	require.Equal(t, true, v["violating"], "missing_payment alone keeps the application violating")
}

// TestLeaseApplicationComplete_LapsedBgcheck_RecordedMarkerReopensGap (freshness
// predicate case b). A completed bgcheck whose own timer has fired — the
// backgroundCheckFreshness marker records an instant at or after its validUntil —
// no longer counts: missing_bgcheck RE-OPENS whenever the row is (re)evaluated (a
// lapsed background check is a missing background check).
//
// The deadline is deliberately in the FAR FUTURE, which is what makes this vector
// discriminating: under the clock-reading form the check would still be counted
// fresh, and only the recorded lapse closes it out. A past deadline here would
// pass under either form and pin nothing.
func TestLeaseApplicationComplete_LapsedBgcheck_RecordedMarkerReopensGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const validUntil = farFutureValidUntil
	app := bgFreshnessFixture(t, f, validUntil)
	recordBgcheckLapse(t, f, "bg1", validUntil)

	rows := f.project(t, app)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["missing_bgcheck"], "a recorded lapse at the deadline -> gap RE-OPENS")
	require.Equal(t, false, v["missing_payment"], "payment carries no freshness window")
	require.Equal(t, true, v["violating"], "re-opened bgcheck gap -> violating again")
}

// TestLeaseApplicationComplete_UnlapsedBgcheck_ReadsFreshWithoutAClock is the
// POSITIVE vector for the negation's polarity, and it is the one that would go
// red if the comparison were written without NOT. The deadline is in the past
// relative to any wall clock the suite ever runs at, and NO timer has recorded a
// lapse against it — compareAny answers false on the nil marker, the negation
// counts the check, and the gap stays closed. A never-lapsed instance reading
// FRESH is the required default; without this vector the lapse test above passes
// for a predicate that counts nothing.
func TestLeaseApplicationComplete_UnlapsedBgcheck_ReadsFreshWithoutAClock(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	app := bgFreshnessFixture(t, f, "2020-06-01T00:00:00Z")

	v := f.project(t, app)[0].Values
	require.Equal(t, false, v["missing_bgcheck"],
		"no recorded lapse -> the check counts, however long ago its validUntil passed")
	require.Equal(t, false, v["violating"], "every gap closed -> not violating")
}

// TestLeaseApplicationComplete_LapsedBgcheck_BoundaryEquals pins the boundary the
// `>=` chooses: a marker recorded EXACTLY at validUntil is a lapse (the timer
// fired for that deadline, and it is that firing the marker records), so the gap
// opens. The strictly-earlier marker in the re-arm test is the other side of it.
func TestLeaseApplicationComplete_LapsedBgcheck_BoundaryEquals(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const validUntil = farFutureValidUntil
	app := bgFreshnessFixture(t, f, validUntil)
	recordBgcheckLapse(t, f, "bg1", validUntil)

	v := f.project(t, app)[0].Values
	require.Equal(t, true, v["missing_bgcheck"], "marker == validUntil IS a lapse (>= boundary)")
}

// TestLeaseApplicationComplete_ReArmedBgcheck_MarkerBehindDeadlineIsFresh is the
// RE-ARM vector, and it is the argument for comparing against the deadline
// instead of testing the marker's presence. The marker is PERMANENT — nothing
// clears it — so a re-checked instance whose new validUntil sits past the
// recorded instant must read fresh again with NO clearing write anywhere. A
// presence test would strand it lapsed forever.
func TestLeaseApplicationComplete_ReArmedBgcheck_MarkerBehindDeadlineIsFresh(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	app := bgFreshnessFixture(t, f, farFutureValidUntil)
	recordBgcheckLapse(t, f, "bg1", "2026-06-18T00:00:00Z") // the PRIOR window's fire

	v := f.project(t, app)[0].Values
	require.Equal(t, false, v["missing_bgcheck"],
		"a recorded lapse the current deadline has outrun is not a lapse of THIS window")
}

// TestLeaseApplicationComplete_SiblingTargetLapseIsNotThisOne is the ISOLATION
// vector. The marker is one aspect shared by every target that fires on the
// entity, keyed per target in byTarget for exactly this reason: another target's
// recorded fire — even one well past this deadline — says nothing about the
// background check's own window, and reading the marker's presence (or its
// entity-wide expiredAt maximum) instead of this target's own entry would close
// the check out on someone else's timer.
func TestLeaseApplicationComplete_SiblingTargetLapseIsNotThisOne(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	app := bgFreshnessFixture(t, f, farFutureValidUntil)
	recordSiblingLapse(t, f, "bg1", "leaseExpiry", "2100-01-01T00:00:00Z")

	v := f.project(t, app)[0].Values
	require.Equal(t, false, v["missing_bgcheck"],
		"another target's fire is not this target's lapse (byTarget is keyed per target)")
}

// TestLeaseApplicationComplete_NewInstanceReadsFreshBesideALapsedOne is the
// re-dispatch shape the live loop actually produces: Weaver re-opens the gap, a
// FRESH instance is minted, and the lapsed one is never touched again (its marker
// is permanent). The count is per instance, so the new one closes the gap while
// the old one keeps its recorded lapse.
func TestLeaseApplicationComplete_NewInstanceReadsFreshBesideALapsedOne(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const lapsedDeadline = "2099-01-01T00:00:00Z"
	app := bgFreshnessFixture(t, f, lapsedDeadline)
	recordBgcheckLapse(t, f, "bg1", lapsedDeadline)
	f.vtxWithClass(t, "bg2", "service", "service.backgroundCheck.instance")
	f.aspect(t, "bg2", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-18T00:00:00Z", "validUntil": "2100-01-01T00:00:00Z"})
	f.edge(t, "providedTo", "bg2", "alice")

	rows := f.project(t, app)
	require.Len(t, rows, 1, "still one row per anchor across the two-instance fan-out")
	require.Equal(t, false, rows[0].Values["missing_bgcheck"],
		"the fresh re-check closes the gap while the lapsed instance keeps its marker")
}

// TestLeaseApplicationComplete_CompletedBgcheckWithNoValidUntil pins the explicit
// (validUntil <> null) conjunct. Under the negation the engine's nil-false lands
// on the FRESH side, so an outcome with no window at all would count forever
// without that conjunct — the opposite of the shipped meaning, where a check with
// no validUntil never satisfied `validUntil > $now`.
func TestLeaseApplicationComplete_CompletedBgcheckWithNoValidUntil(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	app := bgFreshnessFixture(t, f, farFutureValidUntil)
	f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-01T00:00:00Z"})

	v := f.project(t, app)[0].Values
	require.Equal(t, true, v["missing_bgcheck"],
		"a completed outcome with no validUntil has no window to be current within")
}

// TestLeaseApplicationComplete_PaymentIgnoresValidUntil (freshness case c). A
// completed payment whose validUntil is in the PAST still closes missing_payment:
// the freshness policy is bgcheck-only; payment is ever-completed. The bgcheck
// here is fresh so missing_bgcheck stays false — only the payment branch is under
// test. Exactly one row across the multi-instance fan-out.
func TestLeaseApplicationComplete_PaymentIgnoresValidUntil(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})
	f.aspect(t, "app", "signature", "signature", map[string]any{"signedAt": "2026-06-10T00:00:00Z"})
	f.aspect(t, "app", "decision", "decision", map[string]any{"value": "approved", "decidedAt": "2026-06-26T10:00:00Z"})
	f.vtxWithClass(t, "bg1", "service", "service.backgroundCheck.instance")
	f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-17T00:00:00Z", "validUntil": "2026-06-18T00:05:00Z"})
	f.vtxWithClass(t, "pay1", "service", "service.payment.instance")
	// Payment completed long ago with a PAST validUntil — the lens must ignore it.
	f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-01-01T00:00:00Z", "validUntil": "2026-01-01T00:05:00Z"})
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "pay1", "alice")
	attachedLeaseDoc(t, f) // the document leg closed — only the payment branch is under test

	rows := f.projectAt(t, "app", now)
	require.Len(t, rows, 1, "exactly one row per anchor")
	v := rows[0].Values
	require.Equal(t, false, v["missing_payment"], "payment with a PAST validUntil still counts (ever-completed)")
	require.Equal(t, false, v["missing_bgcheck"], "the bgcheck is fresh")
	require.Equal(t, false, v["violating"])
}

// TestLeaseApplicationComplete_NoCompletedBgcheck. A bgcheck instance that is NOT
// yet completed (no .outcome) leaves missing_bgcheck true, and the row still
// projects despite the completed payment sharing the providedTo link.
func TestLeaseApplicationComplete_NoCompletedBgcheck(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.vtx(t, "unit1", "unit")
	f.edge(t, "appliesToUnit", "app", "unit1")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})
	f.vtxWithClass(t, "bg1", "service", "service.backgroundCheck.instance")
	f.vtxWithClass(t, "pay1", "service", "service.payment.instance")
	f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-02T00:00:00Z"})
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "pay1", "alice")

	rows := f.projectAt(t, "app", now)
	require.Len(t, rows, 1, "row still projects when the bgcheck is not yet completed")
	v := rows[0].Values
	require.Equal(t, true, v["missing_bgcheck"], "bgcheck instance present but no completed outcome → missing")
}

// TestLeaseApplicationComplete_PaymentInstanceNoBgcheck_NoDrop is the regression
// guard for the FR58 double-act path the freshness design must NOT reintroduce.
// The applicant has a COMPLETED payment instance and NO bgcheck instance at all —
// the real transient convergence window (payment's instanceOp commits + reprojects
// before bgcheck's). The bgcheck predicate is a count fold over the single
// no-WHERE providedTo fan, so with no bgcheck every CASE is null and the count is
// zero WITHOUT dropping the anchor — the payment instance keeps the providedTo fan
// non-empty and the leaseapp projects exactly one row. A design that instead read
// the bgcheck from a separate WHERE-filtered match could drop the anchor here
// (Weaver would read an entity deletion, clear the gap marks, and on row
// re-appearance re-dispatch a SECOND bgcheck Loom instance — a second external
// call). One row, missing_bgcheck true, missing_payment false.
func TestLeaseApplicationComplete_PaymentInstanceNoBgcheck_NoDrop(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.vtx(t, "unit1", "unit")
	f.edge(t, "appliesToUnit", "app", "unit1")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})
	// Only a COMPLETED payment instance providedTo alice — NO bgcheck instance.
	f.vtxWithClass(t, "pay1", "service", "service.payment.instance")
	f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-02T00:00:00Z"})
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "providedTo", "pay1", "alice")

	rows := f.projectAt(t, "app", now)
	require.Len(t, rows, 1, "the anchor row MUST NOT drop when a payment instance exists but no bgcheck (FR58 double-act guard)")
	v := rows[0].Values
	require.Equal(t, true, v["missing_bgcheck"], "no bgcheck instance → gap open")
	require.Equal(t, false, v["missing_payment"], "completed payment → gap closed")
}

// TestLeaseApplicationComplete_MultipleFreshBgchecks is the row-collision guard:
// providedTo is on the IDENTITY, not the application, so an applicant can
// accumulate >1 completed-fresh bgcheck (multiple applications on one identity, or
// freshness re-dispatches). Every bgcheck fact this lens derives is folded into the
// SAME aggregation over the single no-WHERE providedTo fan, so the anchor stays one
// row however many instances hang off the identity; a separate, unaggregated
// bgcheck match would expand one row per instance and trip
// guardOutputKeyCollision (the lens errors and goes yellow).
func TestLeaseApplicationComplete_MultipleFreshBgchecks(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	const earlier = "2026-06-18T00:05:00Z" // both fresh (> now)
	const later = "2026-06-18T00:09:00Z"
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})
	f.aspect(t, "app", "signature", "signature", map[string]any{"signedAt": "2026-06-10T00:00:00Z"})
	f.aspect(t, "app", "decision", "decision", map[string]any{"value": "approved", "decidedAt": "2026-06-26T10:00:00Z"})
	// TWO completed-fresh bgchecks providedTo the same identity, distinct validUntil.
	f.vtxWithClass(t, "bg1", "service", "service.backgroundCheck.instance")
	f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-01T00:00:00Z", "validUntil": earlier})
	f.vtxWithClass(t, "bg2", "service", "service.backgroundCheck.instance")
	f.aspect(t, "bg2", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-02T00:00:00Z", "validUntil": later})
	f.vtxWithClass(t, "pay1", "service", "service.payment.instance")
	f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-02T00:00:00Z"})
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "bg2", "alice")
	f.edge(t, "providedTo", "pay1", "alice")
	attachedLeaseDoc(t, f) // the document leg closed — freshness aggregation is under test

	rows := f.projectAt(t, "app", now)
	require.Len(t, rows, 1, "two fresh bgchecks on one identity must still project ONE row (no guardOutputKeyCollision)")
	v := rows[0].Values
	require.Equal(t, false, v["missing_bgcheck"], "a fresh bgcheck counts → gap closed")
	require.Equal(t, false, v["violating"], "all gaps closed → not violating")
}

// TestLeaseApplicationComplete_AllGapsOpenProjectsOneRow: with all gaps open (no
// PII, no bgcheck, no payment, no signature) the anchor still projects exactly one
// row — a brand-new application is never dropped by the freshness derivation.
func TestLeaseApplicationComplete_AllGapsOpenProjectsOneRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.vtx(t, "unit1", "unit")
	f.edge(t, "appliesToUnit", "app", "unit1")
	f.edge(t, "applicationFor", "app", "alice")

	rows := f.project(t, "app")
	require.Len(t, rows, 1, "a fresh all-gaps-open application projects exactly one row")
	v := rows[0].Values
	require.Equal(t, true, v["violating"])
	require.Equal(t, false, v["missing_bgcheck"], "missing_bgcheck requires onboarding (ssnVal <> null) first — not yet applicable before PII is recorded")
}

// inflightBgFixture seeds an applicant (PII recorded, signed — so onboarding and
// signature are not the gap under test) whose single bgcheck service instance
// carries a .dispatch marker (present iff withDispatch) and the given .outcome
// status (nil → omit the .outcome aspect entirely). It is the in-flight
// suppression fixture: the bgcheck gap stays open (no completed outcome), and the
// dispatch/outcome shape drives inflight_bgcheck. The predicate is presence-based
// (a .dispatch present + no .outcome), so no deadline is modeled. Returns the
// leaseapp logical name.
func inflightBgFixture(t *testing.T, f *lensFixture, withDispatch bool, outcomeStatus *string) string {
	t.Helper()
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.vtx(t, "unit1", "unit")
	f.edge(t, "appliesToUnit", "app", "unit1")
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})
	f.aspect(t, "app", "signature", "signature", map[string]any{"signedAt": "2026-06-10T00:00:00Z"})
	f.vtxWithClass(t, "bg1", "service", "service.backgroundCheck.instance")
	if withDispatch {
		f.aspect(t, "bg1", "dispatch", "dispatch", map[string]any{
			"vendorRef": "vendor-ref-1", "adapter": "backgroundCheck", "replyOp": "RecordLeaseServiceOutcome",
			"submittedAt": "2026-06-17T00:00:00Z", "nextPollAt": "2026-06-18T00:01:00Z", "deadline": "2026-06-18T00:05:00Z",
		})
	}
	if outcomeStatus != nil {
		f.aspect(t, "bg1", "outcome", "outcome", map[string]any{"status": *outcomeStatus, "completedAt": "2026-06-17T12:00:00Z"})
	}
	// A completed payment so missing_payment is not the only open gap — the row
	// stays violating purely on the bgcheck dimension.
	f.vtxWithClass(t, "pay1", "service", "service.payment.instance")
	f.aspect(t, "pay1", "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-02T00:00:00Z"})
	f.edge(t, "applicationFor", "app", "alice")
	f.edge(t, "providedTo", "bg1", "alice")
	f.edge(t, "providedTo", "pay1", "alice")
	return "app"
}

// TestLeaseApplicationComplete_InflightBgcheck pins the in-flight suppression
// companion: a bgcheck with a .dispatch marker PRESENT and NO .outcome projects
// inflight_bgcheck=true while the gap stays open (missing_bgcheck=true,
// violating=true). Weaver reads inflight_bgcheck to skip re-dispatch of the
// legitimately-pending call. The row also carries the constant maxretries_<g>
// caps (the budget bound, baked from retry_budget.go).
func TestLeaseApplicationComplete_InflightBgcheck(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	app := inflightBgFixture(t, f, true, nil)

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1, "exactly one row per anchor with a dispatch marker in the fan")
	v := rows[0].Values
	require.Equal(t, true, v["inflight_bgcheck"], "a .dispatch marker present, no .outcome → in flight")
	require.Equal(t, false, v["inflight_payment"], "payment has no dispatch marker → not in flight")
	require.Equal(t, true, v["missing_bgcheck"], "an in-flight call is NOT a completed one → gap stays open")
	require.Equal(t, true, v["violating"], "the gap stays violating while the call is in flight")
	requireMaxRetriesColumns(t, v)
}

// TestLeaseApplicationComplete_InflightBgcheck_DeadIrrelevant is the FIX-FIRST
// correctness pin: inflight is PRESENCE-based, NOT deadline-bounded. A .dispatch
// present + no .outcome is in flight regardless of the dispatch deadline — so a
// dead/slow bridge that never posts a timeout outcome keeps inflight_bgcheck=true
// (Weaver waits, never double-dispatching against the vendor) rather than flipping
// it false at the deadline. The fixture's deadline is in the past relative to
// $now, yet inflight stays true.
func TestLeaseApplicationComplete_InflightBgcheck_DeadIrrelevant(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	// $now is AFTER the fixture's dispatch deadline (2026-06-18T00:05:00Z): under a
	// deadline-bounded predicate this would read inflight=false (the old
	// double-dispatch hole); under the presence-based predicate it stays true.
	const now = "2026-06-19T00:00:00Z"
	app := inflightBgFixture(t, f, true, nil)

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["inflight_bgcheck"], "a still-unresolved call stays in flight past its deadline (no double-dispatch)")
	require.Equal(t, true, v["missing_bgcheck"], "the gap is still open")
	require.Equal(t, true, v["violating"])
}

// TestLeaseApplicationComplete_InflightBgcheck_OutcomePresent: a .dispatch marker
// present but an .outcome already written is NOT in flight — the call resolved
// (the create-only outcome landed), so inflight_bgcheck is false and Weaver
// resumes dispatching a FRESH call. The status here is 'failed' (so the gap stays
// open) to isolate the inflight predicate from the completed-gap-close path.
func TestLeaseApplicationComplete_InflightBgcheck_OutcomePresent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	failed := "failed"
	app := inflightBgFixture(t, f, true, &failed)

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["inflight_bgcheck"], "an .outcome present (status != null) → not in flight; re-dispatch resumes")
	require.Equal(t, true, v["missing_bgcheck"], "a failed outcome does not close the gap")
}

// humanGapFixture seeds a bare application whose two HUMAN-paced gaps are both
// open: no ssn aspect on the applicant (missing_onboarding) and no signature
// aspect on the application (missing_signature). Nothing else is seeded, so the
// only thing that can move inflight_onboarding / inflight_signature is the task
// the caller adds. Returns the leaseapp logical name.
func humanGapFixture(t *testing.T, f *lensFixture) string {
	t.Helper()
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.vtx(t, "unit1", "unit")
	f.edge(t, "appliesToUnit", "app", "unit1")
	f.edge(t, "applicationFor", "app", "alice")
	return "app"
}

// seedTask seeds one user task bound to an operation and scoped to a target:
// root {status, expiresAt} scalars (Contract #10 §10.1 — the task DDL's root is
// scalars-only, no aspects), a forOperation edge to a meta-vertex whose root
// data.operationType names the bound op, and a scopedTo edge to the target the
// task hangs off.
func (f *lensFixture) seedTask(t *testing.T, taskName, opName, operationType, scopedTo, status string) {
	t.Helper()
	f.vtx(t, taskName, "task")
	f.setRootData(t, taskName, map[string]any{"status": status, "expiresAt": "2026-08-18T00:00:00Z"})
	f.vtx(t, opName, "meta")
	f.setRootData(t, opName, map[string]any{"operationType": operationType})
	f.edge(t, "forOperation", taskName, opName)
	f.edge(t, "scopedTo", taskName, scopedTo)
}

// TestLeaseApplicationComplete_InflightSignature pins the signature gap's
// suppression companion: an OPEN SignLease task scoped to the application projects
// inflight_signature=true while the gap itself stays open. Only a person signing
// closes missing_signature, so without this companion Weaver re-dispatches the
// assignTask every time the mark lease expires, against a task already sitting
// open.
func TestLeaseApplicationComplete_InflightSignature(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	app := humanGapFixture(t, f)
	f.seedTask(t, "sigtask", "signlease", "SignLease", "app", "open")

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1, "the task fan must not multiply the anchor row")
	v := rows[0].Values
	require.Equal(t, true, v["inflight_signature"], "an open SignLease task scoped to the app → in flight")
	require.Equal(t, true, v["missing_signature"], "an assigned task is NOT a signature → the gap stays open")
	require.Equal(t, true, v["violating"], "the gap stays violating while the task is outstanding")
}

// TestLeaseApplicationComplete_InflightOnboarding pins the onboarding companion.
// The onboarding remediation is triggerLoom(onboarding), whose single userTask
// step binds RecordIdentityPII against the APPLICANT — so the task hangs off the
// identity, not the application, and that is where the companion looks.
func TestLeaseApplicationComplete_InflightOnboarding(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	app := humanGapFixture(t, f)
	f.seedTask(t, "onbtask", "recordpii", "RecordIdentityPII", "alice", "open")

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["inflight_onboarding"], "an open RecordIdentityPII task on the applicant → in flight")
	require.Equal(t, true, v["missing_onboarding"], "an assigned task is NOT a recorded ssn → the gap stays open")
	require.Equal(t, false, v["inflight_signature"], "the onboarding task must not suppress the signature gap")
}

// TestLeaseApplicationComplete_InflightHumanGaps_WrongOperationDoesNotSuppress is
// the over-suppression guard, and the reason both companions discriminate on
// op.data.operationType rather than merely counting open tasks. Several gaps hang
// tasks off the same applicant and the same application; an open task bound to an
// unrelated operation must NOT park a gap it cannot close.
func TestLeaseApplicationComplete_InflightHumanGaps_WrongOperationDoesNotSuppress(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	app := humanGapFixture(t, f)
	// Both tasks are OPEN and correctly placed — on the application and on the
	// applicant — but neither is bound to the operation that closes its gap.
	f.seedTask(t, "othertask", "otherop", "AttachObject", "app", "open")
	f.seedTask(t, "othertask2", "otherop2", "RecordLeaseServiceOutcome", "alice", "open")

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["inflight_signature"], "an open task for another op must not suppress the signature gap")
	require.Equal(t, false, v["inflight_onboarding"], "an open task for another op must not suppress the onboarding gap")
	require.Equal(t, true, v["violating"])
}

// TestLeaseApplicationComplete_InflightHumanGaps_ClosedTaskResumesDispatch: a task
// that is no longer open stops suppressing, so a cancelled or expired-and-closed
// remediation is re-dispatched rather than leaving the gap parked forever. The
// status filter lives in the OPTIONAL MATCH's WHERE, so a closed task contributes
// no row at all.
func TestLeaseApplicationComplete_InflightHumanGaps_ClosedTaskResumesDispatch(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	app := humanGapFixture(t, f)
	f.seedTask(t, "sigtask", "signlease", "SignLease", "app", "completed")
	f.seedTask(t, "onbtask", "recordpii", "RecordIdentityPII", "alice", "cancelled")

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["inflight_signature"], "a closed task no longer suppresses → re-dispatch resumes")
	require.Equal(t, false, v["inflight_onboarding"], "a cancelled task no longer suppresses → re-dispatch resumes")
}

// TestLeaseApplicationComplete_InflightBgcheck_NoDispatch: a bgcheck instance with
// NO .dispatch marker (and no .outcome) is not in flight — vendorRef <> null is
// false when the .dispatch aspect is absent, so inflight_bgcheck=false and the gap
// is dispatchable.
func TestLeaseApplicationComplete_InflightBgcheck_NoDispatch(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	app := inflightBgFixture(t, f, false, nil)

	rows := f.projectAt(t, app, now)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["inflight_bgcheck"], "no .dispatch aspect (vendorRef = null) → not in flight")
	require.Equal(t, true, v["missing_bgcheck"], "the gap is open and dispatchable")
}

// requireMaxRetriesColumns asserts the lens projects the constant per-gap retry
// caps as numeric columns equal to the package constants (the §E mechanism-B
// budget bound Weaver reads off the row). The full engine returns numeric literals
// as int (a Go int via the parser), but the cross-component path carries JSON
// numbers as float64, so accept either — what matters is the integer value.
func requireMaxRetriesColumns(t *testing.T, v map[string]any) {
	t.Helper()
	requireIntColumn(t, v, "maxretries_bgcheck", maxBgcheckRetries)
	requireIntColumn(t, v, "maxretries_payment", maxPaymentRetries)
}

func requireIntColumn(t *testing.T, v map[string]any, col string, want int) {
	t.Helper()
	got, ok := v[col]
	require.Truef(t, ok, "row must carry the %s column", col)
	switch n := got.(type) {
	case int:
		require.Equalf(t, want, n, "%s", col)
	case int64:
		require.Equalf(t, want, int(n), "%s", col)
	case float64:
		require.Equalf(t, want, int(n), "%s", col)
	default:
		t.Fatalf("%s is %T, not a numeric cap", col, got)
	}
}

// TestLeaseApplicationComplete_MaxRetriesColumns pins the constant retry-cap
// columns: every convergence row carries maxretries_bgcheck / maxretries_payment
// equal to the package constants, regardless of the gap state. They are the
// package-owned budget bound Weaver compares its weaver-state dispatch-count
// against; the budget accounting itself is proven in internal/weaver (the lens no
// longer projects a failed-count).
func TestLeaseApplicationComplete_MaxRetriesColumns(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	// All-gaps-open application — the caps must still be present.
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "alice", "identity")
	f.edge(t, "applicationFor", "app", "alice")

	rows := f.project(t, "app")
	require.Len(t, rows, 1)
	requireMaxRetriesColumns(t, rows[0].Values)
}

// ---------------------------------------------------------------------------
// applicantOnboarding — the identity-anchored onboarding target.
//
// The defect these pin: recording PII is one piece of work per PERSON, and it
// used to be dispatched from a target anchored on the leaseapp. An applicant
// holding N applications projected N violating rows, each of which dispatched
// its own Loom instance, so one person received N identical RecordIdentityPII
// cards. The fix re-anchors the gap where the work lives; these tests hold that
// anchor in place.
// ---------------------------------------------------------------------------

// projectApplicantOnboarding runs the applicantOnboarding lens over one identity
// anchor, the same way projectAt runs the leaseapp-anchored one: $actorKey is
// the anchor's own vertex key and $now is the projection instant the live
// pipeline supplies.
func (f *lensFixture) projectApplicantOnboarding(t *testing.T, identityName, now string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(applicantOnboardingSpec)
	require.NoError(t, err, "applicantOnboarding cypher must parse on the full engine")
	idKey := "vtx.identity." + f.ids[identityName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    idKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// twoApplicationsFixture seeds the shape the defect was witnessed on: ONE
// applicant identity holding TWO live lease applications, each on its own
// listed-and-available unit, with no `.ssn` aspect anywhere — so onboarding is
// genuinely outstanding and both applications are genuinely live.
func twoApplicationsFixture(t *testing.T, f *lensFixture) {
	t.Helper()
	f.vtx(t, "alice", "identity")
	f.vtx(t, "app1", "leaseapp")
	f.vtx(t, "app2", "leaseapp")
	f.vtx(t, "unit1", "unit")
	f.vtx(t, "unit2", "unit")
	f.aspect(t, "unit1", "listing", "listing", map[string]any{"rentAmount": 2400, "status": "available"})
	f.aspect(t, "unit2", "listing", "listing", map[string]any{"rentAmount": 1800, "status": "available"})
	f.edge(t, "applicationFor", "app1", "alice")
	f.edge(t, "applicationFor", "app2", "alice")
	f.edge(t, "appliesToUnit", "app1", "unit1")
	f.edge(t, "appliesToUnit", "app2", "unit2")
}

// TestApplicantOnboarding_TwoApplicationsProjectOneRow is the headline: the
// applicant fan-out collapses inside the aggregator, so two applications yield
// exactly ONE violating row — the §0.C guardOutputKeyCollision shape, and the
// reason one applicant can only ever be one gap on this target.
func TestApplicantOnboarding_TwoApplicationsProjectOneRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	twoApplicationsFixture(t, f)
	aliceKey := "vtx.identity." + f.ids["alice"]

	rows := f.projectApplicantOnboarding(t, "alice", now)
	require.Len(t, rows, 1, "one applicant is exactly one row however many applications they hold")
	v := rows[0].Values
	require.Equal(t, aliceKey, v["actorKey"], "the row key seeds from the identity anchor")
	require.Equal(t, aliceKey, v["entityKey"])
	require.Equal(t, aliceKey, v["applicant"], "row.applicant is the triggerLoom Subject the playbook templates")
	require.Equal(t, true, v["missing_onboarding"], "no .ssn and a live application → the person owes PII")
	require.Equal(t, true, v["violating"], "this target's single gap IS its violating predicate")
	require.Equal(t, false, v["inflight_onboarding"], "no task exists yet")
}

// TestApplicantOnboarding_SsnClosesTheGapForEveryApplication: the gap closes on
// the APPLICANT's own `.ssn` write — one RecordIdentityPII, both applications
// satisfied — and the row stops violating, which is what makes EmptyBehavior
// "delete" retire it.
func TestApplicantOnboarding_SsnClosesTheGapForEveryApplication(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	twoApplicationsFixture(t, f)
	f.aspect(t, "alice", "ssn", "ssn", map[string]any{"value": "123456789"})

	rows := f.projectApplicantOnboarding(t, "alice", now)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_onboarding"], "ssn recorded once → onboarded, for every application at once")
	require.Equal(t, false, v["violating"])
}

// TestApplicantOnboarding_NoLiveApplicationDoesNotAsk pins the per-application
// half of the gate, evaluated inside the count CASE: an applicant whose only
// application's unit has leased to someone else is not asked for their SSN. This
// is the same terminal-not-violating rule leaseApplicationComplete already
// applies to its four applicant gaps — without it, a losing rival keeps being
// asked for PII against a unit they can no longer get, indefinitely.
func TestApplicantOnboarding_NoLiveApplicationDoesNotAsk(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	twoApplicationsFixture(t, f)
	// Both units lease to somebody else; neither of alice's applications was
	// approved, so neither takes the landlordDecision escape hatch.
	f.aspect(t, "unit1", "listing", "listing", map[string]any{"rentAmount": 2400, "status": "leased"})
	f.aspect(t, "unit2", "listing", "listing", map[string]any{"rentAmount": 1800, "status": "leased"})

	rows := f.projectApplicantOnboarding(t, "alice", now)
	require.Len(t, rows, 1, "the anchor never drops — the filtering lives inside the CASE, not on the OPTIONAL MATCH")
	v := rows[0].Values
	require.Equal(t, false, v["missing_onboarding"], "no application still needs onboarding → stop asking")
	require.Equal(t, false, v["violating"])
}

// TestApplicantOnboarding_OneLiveApplicationAmongDeadOnesStillAsks is the
// converse, and the reason the gate is evaluated PER APPLICATION rather than
// over the applicant as a whole: one live application is enough.
func TestApplicantOnboarding_OneLiveApplicationAmongDeadOnesStillAsks(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	twoApplicationsFixture(t, f)
	f.aspect(t, "unit1", "listing", "listing", map[string]any{"rentAmount": 2400, "status": "leased"})

	rows := f.projectApplicantOnboarding(t, "alice", now)
	require.Len(t, rows, 1)
	require.Equal(t, true, rows[0].Values["missing_onboarding"], "app2's unit is still available → the person still owes PII")
}

// TestApplicantOnboarding_InflightSuppressesRedispatch pins the §10.3
// suppression companion on the new anchor. Only a person closes this gap, so
// without the companion every mark-lease reclaim re-dispatches a remediation
// whose task is already sitting open. The task hangs off the identity, which on
// this target IS the anchor — no neighbour hop.
func TestApplicantOnboarding_InflightSuppressesRedispatch(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	twoApplicationsFixture(t, f)
	f.seedTask(t, "onbtask", "recordpii", "RecordIdentityPII", "alice", "open")

	rows := f.projectApplicantOnboarding(t, "alice", now)
	require.Len(t, rows, 1, "the task fan collapses too — still one row")
	v := rows[0].Values
	require.Equal(t, true, v["inflight_onboarding"], "an open RecordIdentityPII task on the applicant → in flight")
	require.Equal(t, true, v["missing_onboarding"], "an assigned task is NOT a recorded ssn → the gap stays open")
	require.Equal(t, true, v["violating"])
}

// TestApplicantOnboarding_WrongOperationDoesNotSuppress is the over-suppression
// guard, and the reason the companion discriminates on op.data.operationType
// rather than counting open tasks: several gaps hang tasks off the same
// applicant, and an open task bound to an operation that cannot close this gap
// must not park it.
func TestApplicantOnboarding_WrongOperationDoesNotSuppress(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	twoApplicationsFixture(t, f)
	f.seedTask(t, "othertask", "otherop", "RecordLeaseServiceOutcome", "alice", "open")
	// A CLOSED RecordIdentityPII task no longer suppresses either — a cancelled
	// or expired remediation must be re-dispatchable, not parked forever.
	f.seedTask(t, "staletask", "recordpii", "RecordIdentityPII", "alice", "cancelled")

	rows := f.projectApplicantOnboarding(t, "alice", now)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["inflight_onboarding"], "neither an unrelated op nor a cancelled task suppresses this gap")
	require.Equal(t, true, v["missing_onboarding"])
}

// TestLeaseApplicationComplete_SurfacedOnboardingStillViolatesBothApplications
// pins the OTHER half of the split. missing_onboarding is now surface-declared
// on the leaseapp-anchored target — it dispatches nothing — but it is still a
// projected column and still folded into `violating`, on EVERY application the
// un-onboarded applicant holds. The application genuinely is blocked on
// onboarding; the applicant FE's stepper reads the column; only the playbook
// action changed.
func TestLeaseApplicationComplete_SurfacedOnboardingStillViolatesBothApplications(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	twoApplicationsFixture(t, f)

	for _, app := range []string{"app1", "app2"} {
		rows := f.projectAt(t, app, now)
		require.Len(t, rows, 1, "%s projects one row", app)
		v := rows[0].Values
		require.Equalf(t, true, v["missing_onboarding"], "%s: the column survives the demotion to surface", app)
		require.Equalf(t, true, v["violating"], "%s: an application blocked on onboarding is still violating", app)
	}

	ga, declared := gapAction(t, "leaseApplicationComplete", "missing_onboarding")
	require.True(t, declared, "the projected column must still be a declared gap (else dispatchGap's config-error arm holds the row on the long redelivery floor)")
	require.Equal(t, "surface", ga.Action, "the leaseapp anchor must not dispatch this gap — that is the defect")
	require.NotEmpty(t, ga.IssueCode)
}

// TestApplicantOnboarding_OneApplicantYieldsOneDispatch is the regression the
// whole split exists to prevent, asserted at the seam where the duplication was
// actually minted.
//
// Weaver derives a triggerLoom's Loom instanceId from
// (targetID, entityID, gapColumn, claimID) — internal/weaver/actuator.go's
// deriveStableInstanceID — where entityID is the ROW's own entity. Distinct rows
// therefore give distinct instanceIds, distinct Loom instances, and distinct
// RecordIdentityPII tasks; no idempotency token downstream can tell them apart,
// which is why the observed burst produced four cards for one person from one
// correct lineage. So the count of DISTINCT (targetID, entityID, gapColumn)
// tuples a fixture yields for `missing_onboarding` across every DISPATCHING
// target IS the count of tasks the applicant receives.
//
// The walk is driven off the package's own declarations rather than a hardcoded
// target name, so it re-fails if the dispatching action ever moves back: with
// missing_onboarding declared triggerLoom on the leaseapp-anchored target, this
// fixture's two applications contribute two tuples and the assertion reads 2.
func TestApplicantOnboarding_OneApplicantYieldsOneDispatch(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	twoApplicationsFixture(t, f)
	aliceKey := "vtx.identity." + f.ids["alice"]

	// The rows this fixture projects, per target: every anchor of every target
	// that could carry the column. A target declaring missing_onboarding with no
	// entry here would leave the assertion blind, so the walk fails on one.
	rowsByTarget := map[string][]ruleengine.ProjectionResult{
		"leaseApplicationComplete": append(f.projectAt(t, "app1", now), f.projectAt(t, "app2", now)...),
		"applicantOnboarding":      f.projectApplicantOnboarding(t, "alice", now),
	}
	require.Len(t, rowsByTarget["leaseApplicationComplete"], 2, "the fixture must actually fan out, else this proves nothing")

	type dispatchKey struct{ targetID, entityID, gapColumn string }
	dispatches := map[dispatchKey]string{} // → the resolved triggerLoom Subject
	dispatchingTargets := 0
	for _, wt := range WeaverTargets() {
		ga, declared := wt.Gaps["missing_onboarding"]
		if !declared {
			continue
		}
		rows, known := rowsByTarget[wt.TargetID]
		require.Truef(t, known, "target %q declares missing_onboarding but this test projects no rows for it — extend rowsByTarget", wt.TargetID)
		if ga.Action == "surface" {
			continue // raises a Health issue and Acks; mints no artifact
		}
		dispatchingTargets++
		require.Equalf(t, "triggerLoom", ga.Action, "target %q: unexpected onboarding action", wt.TargetID)
		require.Equalf(t, "row.applicant", ga.Subject, "target %q: the onboarding pattern's subject is the applicant identity", wt.TargetID)
		for _, row := range rows {
			if row.Values["missing_onboarding"] != true {
				continue
			}
			entityKey, ok := row.Values["entityKey"].(string)
			require.True(t, ok, "every convergence row carries entityKey")
			subject, ok := row.Values["applicant"].(string)
			require.True(t, ok, "row.applicant must resolve, or the dispatch has no subject")
			dispatches[dispatchKey{wt.TargetID, entityKey, "missing_onboarding"}] = subject
		}
	}

	require.Lenf(t, dispatches, 1,
		"one applicant with two live applications must yield exactly ONE RecordIdentityPII dispatch, got %d: %v", len(dispatches), dispatches)
	require.Equal(t, 1, dispatchingTargets, "exactly one target may dispatch onboarding — two would each mint their own instance")
	for k, subject := range dispatches {
		require.Equal(t, "applicantOnboarding", k.targetID)
		require.Equal(t, aliceKey, k.entityID, "the dispatch is keyed on the PERSON, not on either application")
		require.Equal(t, aliceKey, subject, "and it asks that same person")
	}
}

// gapAction resolves one declared gap off a target by TargetID, so a test can
// assert on the playbook without assuming target ordering.
func gapAction(t *testing.T, targetID, gapColumn string) (pkgmgr.GapActionSpec, bool) {
	t.Helper()
	for _, wt := range WeaverTargets() {
		if wt.TargetID != targetID {
			continue
		}
		ga, ok := wt.Gaps[gapColumn]
		return ga, ok
	}
	t.Fatalf("target %q is not declared by this package", targetID)
	return pkgmgr.GapActionSpec{}, false
}
