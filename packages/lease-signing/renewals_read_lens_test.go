package leasesigning

// Rule-engine proof of the renewalsRead protected Postgres read model (design
// loftspace-lease-renewal-goal-authored-target-design.md §4.5, R3).
//
// These drive renewalsReadSpec through the same `full` engine selected at
// activation (engine:"full"), against an embedded NATS Core/Adjacency KV, and
// assert the ENGINE PROJECTION ROW: the headline is that authz_anchors carries
// BOTH the tenant's AND the managing landlord's bare NanoID (§6.14's set is a
// SET, any-match) — the mechanism that lets one query serve both audiences
// without a second lens or a cap-read grant producer. The Postgres RLS
// round-trip (table provisioning + the set-membership policy + SET LOCAL
// lattice.actor_id) is the platform-side proof (internal/refractor
// adapter/rls tests, POSTGRES_TEST_DSN); the cypher's dual-anchor derivation
// is proven here.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// setRootData overwrites a fixture vertex's root `data` (design D5). Used by the
// vertices whose facts are ROOT scalars rather than aspects: the renewal's
// status/cycleEnd, a task's status/expiresAt, and an operation meta-vertex's
// operationType. Every other fact lives on an aspect via f.aspect.
func (f *lensFixture) setRootData(t *testing.T, name string, data map[string]any) {
	t.Helper()
	key := "vtx." + f.types[f.ids[name]] + "." + f.ids[name]
	body := map[string]any{"key": key, "class": f.types[f.ids[name]], "isDeleted": false, "data": data}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

// projectRenewalsRead runs the renewalsRead lens over every renewal in the
// fixture with the real wall-clock $now (unused by this spec today, but
// supplied for parity with every other projection helper in this package).
func (f *lensFixture) projectRenewalsRead(t *testing.T) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(renewalsReadSpec)
	require.NoError(t, err, "renewalsRead cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"now": time.Now().UTC().Format(time.RFC3339),
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// seedOpenRenewal mints one renewal cycle for one leaseapp: a tenant identity,
// a unit managed by one landlord, and the renews link — the full well-formed
// shape the read model requires (every MATCH in renewalsReadSpec is
// REQUIRED). status defaults to "open"; callers add aspects afterward.
func (f *lensFixture) seedOpenRenewal(t *testing.T, renewalName, appName, tenantName, unitName, landlordName string) {
	t.Helper()
	f.vtx(t, renewalName, "renewal")
	f.setRootData(t, renewalName, map[string]any{"status": "open", "cycleEnd": "2027-01-01T00:00:00Z"})
	f.vtx(t, appName, "leaseapp")
	f.vtx(t, tenantName, "identity")
	f.vtx(t, unitName, "unit")
	f.vtx(t, landlordName, "identity")
	f.aspect(t, unitName, "address", "address", map[string]any{"line1": "1 Market St", "city": "San Francisco", "region": "CA"})
	f.edge(t, "renews", renewalName, appName)
	f.edge(t, "applicationFor", appName, tenantName)
	f.edge(t, "appliesToUnit", appName, unitName)
	f.edge(t, "manages", landlordName, unitName)
}

// TestRenewalsRead_ProjectsDualAnchor is the headline: a well-formed OPEN
// renewal projects one row with authz_anchors carrying BOTH the tenant's and
// the managing landlord's bare NanoID — the §6.14 any-match set that lets a
// signed-in tenant OR a signed-in landlord read the same row via one lens,
// with no second lens and no cap-read grant producer (the primordial
// self-grant already grants each identity its own NanoID).
func TestRenewalsRead_ProjectsDualAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedOpenRenewal(t, "rn", "app", "tina", "unit1", "larry")

	rows := f.projectRenewalsRead(t)
	require.Len(t, rows, 1)
	v := rows[0].Values

	renewalKey := "vtx.renewal." + f.ids["rn"]
	appKey := "vtx.leaseapp." + f.ids["app"]
	tenantKey := "vtx.identity." + f.ids["tina"]
	landlordKey := "vtx.identity." + f.ids["larry"]

	require.Equal(t, f.ids["rn"], v["renewal_id"], "renewal_id is the cycle's bare NanoID")
	require.Equal(t, renewalKey, v["entity_key"])
	require.Equal(t, appKey, v["lease_app"])
	require.Equal(t, tenantKey, v["tenant"])
	require.Equal(t, landlordKey, v["landlord"])
	require.Equal(t, "open", v["status"])
	require.Equal(t, "1 Market St", v["unit_address"])

	require.ElementsMatch(t, []string{f.ids["tina"], f.ids["larry"]}, anchorStrings(t, v["authz_anchors"]),
		"authz_anchors must carry BOTH the tenant's and the managing landlord's bare NanoID")
}

// TestRenewalsRead_ProjectsChainFacts — the display facts a real card renders:
// terms once set, guarantor verification once done, the signature once
// signed — all aspect-real hops, none bridged from the Weaver-internal
// renewalComplete lens (this lens has no runtime dependency on it).
func TestRenewalsRead_ProjectsChainFacts(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedOpenRenewal(t, "rn", "app", "tina", "unit1", "larry")
	f.aspect(t, "tina", "profile", "profile", map[string]any{"hasGuarantor": true})
	f.aspect(t, "rn", "terms", "terms", map[string]any{"rentAmount": 2600, "termMonths": 12, "setAt": "2026-07-01T00:00:00Z"})
	f.aspect(t, "rn", "guarantorVerification", "guarantorVerification", map[string]any{"verifiedAt": "2026-07-02T00:00:00Z", "method": "phone"})
	f.aspect(t, "rn", "renewalSignature", "renewalSignature", map[string]any{"signedAt": "2026-07-03T00:00:00Z"})

	rows := f.projectRenewalsRead(t)
	require.Len(t, rows, 1)
	v := rows[0].Values

	require.Equal(t, true, v["has_guarantor"])
	require.EqualValues(t, 2600, v["rent_amount"])
	require.EqualValues(t, 12, v["term_months"])
	require.Equal(t, "2026-07-01T00:00:00Z", v["terms_set_at"])
	require.Equal(t, "2026-07-02T00:00:00Z", v["guarantor_verified_at"])
	require.Equal(t, "phone", v["guarantor_method"])
	require.Equal(t, "2026-07-03T00:00:00Z", v["signed_at"])
}

// TestRenewalsRead_NoGuarantorProjectsFalseNotNull — a tenant with no .profile
// aspect at all (never set hasGuarantor) projects has_guarantor = null
// (unknown, matching every other profile-derived column's null-vs-false
// convention in this package), not a dropped row.
func TestRenewalsRead_NoGuarantorProjectsFalseNotNull(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedOpenRenewal(t, "rn", "app", "tina", "unit1", "larry")

	rows := f.projectRenewalsRead(t)
	require.Len(t, rows, 1)
	require.Nil(t, rows[0].Values["has_guarantor"], "no .profile aspect -> unknown, not false")
	require.Nil(t, rows[0].Values["terms_set_at"], "no .terms aspect yet -> null, not a dropped row")
}

// TestRenewalsRead_UnmanagedUnitProducesNoRow — a renewal whose unit has NO
// managing landlord projects NO row (the `manages` MATCH is REQUIRED, the
// leaseApplicationsRead/landlordLeaseApplicationsRead fail-closed
// convention). In practice this never happens — leaseExpiry's own gate
// already requires >=1 manager before OpenRenewal ever fires — but the lens
// itself must never hand the RLS adapter a null anchor.
func TestRenewalsRead_UnmanagedUnitProducesNoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	// A renewal on an unmanaged unit: renews/applicationFor/appliesToUnit
	// links, but NO manages link.
	f.vtx(t, "orphanRn", "renewal")
	f.vtx(t, "orphanApp", "leaseapp")
	f.vtx(t, "carol", "identity")
	f.vtx(t, "unitOrphan", "unit")
	f.edge(t, "renews", "orphanRn", "orphanApp")
	f.edge(t, "applicationFor", "orphanApp", "carol")
	f.edge(t, "appliesToUnit", "orphanApp", "unitOrphan")
	// A fully-formed renewal alongside it.
	f.seedOpenRenewal(t, "rn", "app", "tina", "unit1", "larry")

	rows := f.projectRenewalsRead(t)
	require.Len(t, rows, 1, "only the fully-managed renewal projects; the unmanaged one is excluded")
	require.Equal(t, f.ids["rn"], rows[0].Values["renewal_id"])
}

// TestRenewalsRead_CoManagedUnitPicksMinLandlord — many-landlords is legal
// (design §4.3 B5); renewalsRead's `landlord` display/action-target field
// takes the SAME deterministic min(key) pick renewalCompleteSpec uses (the
// planner assigns tasks to exactly one canonical manager, so the row stays
// one-per-anchor, unlike the landlord-only lens's fan-out), but authz_anchors
// — a DIFFERENT question, READ access — carries EVERY co-manager's NanoID,
// not just the canonical one. A non-canonical co-manager cannot act on the
// renewal via the assigned task (that goes to the canonical manager alone),
// but they CAN read it — caught in review: an anchor set collapsed to the
// min-key pick would have silently denied a legitimate co-manager read
// access to a unit they genuinely manage.
func TestRenewalsRead_CoManagedUnitPicksMinLandlord(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedOpenRenewal(t, "rn", "app", "tina", "unit1", "larry")
	f.vtx(t, "linda", "identity")
	f.edge(t, "manages", "linda", "unit1")

	rows := f.projectRenewalsRead(t)
	require.Len(t, rows, 1, "co-management fans out at the landlord lens, not here — renewalsRead stays one row per renewal")
	landlordIDs := []string{f.ids["larry"], f.ids["linda"]}
	minID := landlordIDs[0]
	if landlordIDs[1] < minID {
		minID = landlordIDs[1]
	}
	v := rows[0].Values
	require.Equal(t, "vtx.identity."+minID, v["landlord"], "the display/action-target field is still the canonical min-key manager")
	require.ElementsMatch(t, []string{f.ids["tina"], f.ids["larry"], f.ids["linda"]}, anchorStrings(t, v["authz_anchors"]),
		"authz_anchors must carry EVERY co-manager, not just the canonical min-key pick — a non-canonical co-manager must still be able to READ this renewal")
}
