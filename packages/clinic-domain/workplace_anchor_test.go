package clinicdomain

import (
	"strings"
	"testing"
)

// TestWorkplaceAnchor_AppointmentsUseComprehension mirrors lease-signing's
// vector: the workplace token must be a pattern comprehension, because
// withProvider is OPTIONAL here and a provider-less appointment would otherwise
// put a NULL element in authz_anchors — which the Protected adapter rejects,
// failing the row's upsert and hiding the appointment from its own PATIENT.
func TestWorkplaceAnchor_AppointmentsUseComprehension(t *testing.T) {
	spec := clinicAppointmentsReadSpec

	if !strings.Contains(spec, "[(pr)-[:practicesAt]->(b:building) | nanoIdFromKey(b.key)]") {
		t.Fatal("the workplace anchor must be a pattern comprehension over the provider's building")
	}
	if !strings.Contains(spec, "[nanoIdFromKey(p.key)] +") {
		t.Error("the patient anchor must remain the first, unconditional element")
	}
	if strings.Contains(spec, "[nanoIdFromKey(p.key), nanoIdFromKey(") {
		t.Error("two-element array literal reintroduces the null-element hazard")
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
// workplace fan-out walks patient -> appointment -> provider -> building as an
// OPTIONAL MATCH folded by WITH p, id, collect(DISTINCT ...) back to one row
// per patient (WITH's implicit GROUP BY on p, id), rather than a plain MATCH
// left un-folded (which would fan a multi-appointment patient into one row
// per appointment, colliding on the patient_id IntoKey) or an un-deduped
// pattern comprehension (which grows authz_anchors by one entry per
// appointment forever — see clinicPatientsReadSpec's doc comment).
// collect(DISTINCT ...) drops nulls, so a patient with no appointments still
// yields buildingAnchors = [] rather than a null element.
func TestWorkplaceAnchor_PatientsRosterDedupesViaWith(t *testing.T) {
	spec := clinicPatientsReadSpec

	if !strings.Contains(spec, "OPTIONAL MATCH (p)<-[:forPatient]-(a:appointment)-[:withProvider]->(pr:provider)-[:practicesAt]->(b:building)") {
		t.Fatal("the patient roster's workplace anchor must walk patient -> appointment -> provider -> building via an OPTIONAL MATCH")
	}
	if !strings.Contains(spec, "collect(DISTINCT nanoIdFromKey(b.key))") {
		t.Fatal("the workplace anchor must dedupe via collect(DISTINCT ...), not accumulate one entry per appointment")
	}
	if !strings.Contains(spec, "WITH p, id, collect(DISTINCT nanoIdFromKey(b.key)) AS buildingAnchors") {
		t.Fatal("the WITH must re-project p and id explicitly, keeping the row one-per-patient")
	}
	if !strings.Contains(spec, "[nanoIdFromKey(p.key)] + buildingAnchors") {
		t.Error("the patient self-anchor must remain the first, unconditional element")
	}
}
