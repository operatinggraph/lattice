package clinicdomain

import (
	"strings"
	"testing"
)

// TestWorkplaceAnchor_AppointmentsUseComprehension mirrors lease-signing's
// vector: EVERY workplace token must come from a pattern comprehension, because
// both arms are OPTIONAL here — a provider-less (or tombstoned-provider)
// appointment, and a site-less one — and a bare element off either would put a
// NULL in authz_anchors, which the Protected adapter rejects, failing the row's
// upsert and hiding the appointment from its own PATIENT.
func TestWorkplaceAnchor_AppointmentsUseComprehension(t *testing.T) {
	spec := clinicAppointmentsReadSpec

	if !strings.Contains(spec, "[(pr)-[:practicesAt]->(b:building) | nanoIdFromKey(b.key)]") {
		t.Fatal("the workplace anchor must be a pattern comprehension over the provider's building")
	}
	if !strings.Contains(spec, "[(a)-[:atSite]->(sb:building) | nanoIdFromKey(sb.key)]") {
		t.Fatal("the tombstoned-provider fallback must be a pattern comprehension over the appointment's own atSite building")
	}
	if !strings.Contains(spec, "CASE WHEN (pr)-[:practicesAt]->(pb:building)") {
		t.Fatal("the two arms must be selected by a CASE on the practicesAt walk (appointment_sites' own order), " +
			"never unioned — a union double-counts the site of a live provider that practises there")
	}
	if !strings.Contains(spec, "[nanoIdFromKey(p.key)]\n    + (CASE") {
		t.Error("the patient anchor must remain the first, unconditional element")
	}
	if strings.Contains(spec, "[nanoIdFromKey(p.key), nanoIdFromKey(") {
		t.Error("two-element array literal reintroduces the null-element hazard")
	}
	if strings.Contains(spec, "nanoIdFromKey(site.key)]") {
		t.Error("the fallback must not read the display-column `site` binding as a bare element — " +
			"an unmatched OPTIONAL MATCH yields a NULL element there")
	}
}

// TestWorkplaceAnchor_ProviderLensUnchanged — the provider schedule anchors
// only on its own subject (the provider), never on a building: a provider's
// own schedule needs no workplace token, unlike the patient-facing surfaces a
// front-desk actor must reach across subjects to read.
func TestWorkplaceAnchor_ProviderLensUnchanged(t *testing.T) {
	if strings.Contains(providerAppointmentsReadSpec, "practicesAt]->(b:building)") {
		t.Error("providerAppointmentsRead must not gain a workplace anchor")
	}
	if !strings.Contains(providerAppointmentsReadSpec, "[nanoIdFromKey(pr.key)]") {
		t.Error("the provider lens must keep its self-anchor exactly as shipped")
	}
}

// TestWorkplaceAnchor_PatientsRosterDedupesViaWith — the patient roster's
// workplace fan-out binds the appointment hop on its own, walks BOTH ways an
// appointment names a building off that binding (its provider's practicesAt
// sites, and its own atSite), and folds them by WITH p, id, collect(DISTINCT
// coalesce(...)) back to one row per patient (WITH's implicit GROUP BY on p,
// id) — rather than a plain MATCH left un-folded (which would fan a
// multi-appointment patient into one row per appointment, colliding on the
// patient_id IntoKey) or an un-deduped pattern comprehension (which grows
// authz_anchors by one entry per appointment forever — see
// clinicPatientsReadSpec's doc comment). ONE collect over the coalesced value,
// not one per APPOINTMENT arm concatenated, is what makes the dedup hold across
// those two arms rather than only within each.
//
// The REGISTRATION arm (patient -> registeredAtSite building) is a THIRD,
// independent fan-out, so it gets its own collect concatenated onto that one:
// coalesce picks a single value per row, and on the cross-product row of an
// appointment building and a registration building it would keep one and drop
// the other. It reads a fact RECORDED on the patient at registration, walking no
// identity — the arm must never re-derive the site from where the registering
// staffer works now, or a transfer would re-anchor that staffer's whole
// registration history. collect(DISTINCT ...) drops nulls, so a patient with no
// appointments and no recorded registration site still yields buildingAnchors =
// [] rather than a null element.
func TestWorkplaceAnchor_PatientsRosterDedupesViaWith(t *testing.T) {
	spec := clinicPatientsReadSpec

	if !strings.Contains(spec, "OPTIONAL MATCH (p)<-[:forPatient]-(a:appointment)\nOPTIONAL MATCH (a)-[:withProvider]->(pr:provider)-[:practicesAt]->(b:building)") {
		t.Fatal("the patient roster's workplace anchor must bind the appointment hop on its own, then walk " +
			"appointment -> provider -> building off it, so the atSite arm hangs off the SAME appointment")
	}
	if !strings.Contains(spec, "OPTIONAL MATCH (a)-[:atSite]->(b2:building)") {
		t.Fatal("the roster must also walk each appointment's own atSite building — the only surviving path once its provider is tombstoned")
	}
	if !strings.Contains(spec, "OPTIONAL MATCH (p)-[:registeredAtSite]->(b3:building)") {
		t.Fatal("the roster must also read the patient's RECORDED registration site — the only building token a " +
			"patient carries before their first appointment exists. One hop, off the patient: a walk through the " +
			"registering identity's current worksAt would re-anchor every patient that staffer ever registered " +
			"the moment they transfer buildings")
	}
	if !strings.Contains(spec, "WITH p, id, collect(DISTINCT coalesce(nanoIdFromKey(b.key), nanoIdFromKey(b2.key))) + collect(DISTINCT nanoIdFromKey(b3.key)) AS buildingAnchors") {
		t.Fatal("the WITH must re-project p and id explicitly, fold both APPOINTMENT arms through a single " +
			"collect(DISTINCT coalesce(...)) — coalescing them per appointment is what stops a live one " +
			"suppressing a tombstoned one's site — and concatenate the registration arm's own collect, " +
			"which cannot join that coalesce: it fans out independently, so coalesce would drop one of the two")
	}
	if !strings.Contains(spec, "[nanoIdFromKey(p.key)] + buildingAnchors") {
		t.Error("the patient self-anchor must remain the first, unconditional element")
	}
}
