package clinicdomain

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// TestClinicPatientsRead_TombstonedPatientRetractsItsRow is the retraction pin
// for clinicPatientsRead's declaration comment (lenses.go): patient_id's
// RETURN expression, nanoIdFromKey(p.key), resolves back across
// clinicPatientsReadSpec's WITH boundary to p — the pattern variable the WITH
// carries under its own name — so the closure predicate
// (internal/refractor/ruleengine/full/anchor_delete.go) admits this lens to
// the read-free anchor Delete path, which is the retraction TRANSPORT for a
// TombstonePatient root tombstone. DiffRetraction is declared alongside it as
// the lens's continuous healer, not as a substitute for this path: without the
// resolution proven here, a tombstone would depend entirely on the next
// DiffRetraction diff to remove the roster row — carrying decrypted
// name/email/phone PHI — from read_clinic_patients, rather than being removed
// read-free, immediately, by the anchor Delete itself.
func TestClinicPatientsRead_TombstonedPatientRetractsItsRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	patientKey := f.vtx(t, "alice", "patient")
	f.aspect(t, "alice", "demographics", "patientDemographics", map[string]any{
		"registeredAt": "2026-06-01T09:00:00Z", "fullName": "Alice Rivera",
	})

	rows := f.project(t, clinicPatientsReadSpec)
	require.Len(t, rows, 1, "precondition: the live patient projects a roster row")
	require.Equal(t, f.ids["alice"], rows[0].Values["patient_id"])

	// A root tombstone carries the prior document whole, with only isDeleted
	// flipped (internal/processor/step8_commit.go) — the shape the CDC event
	// delivers, and the one AnchorDeleteResult must resolve the key from
	// read-free.
	body := map[string]any{"key": patientKey, "class": "patient", "isDeleted": true, "data": map[string]any{}}

	eng := full.New()
	compiled, err := eng.Parse(clinicPatientsReadSpec)
	require.NoError(t, err, "clinicPatientsReadSpec must parse on the full engine")
	cr, isFull := compiled.(*full.CompiledRule)
	require.True(t, isFull)
	cr.KeyColumns = []string{"patient_id"}
	require.NoError(t, cr.ValidateKeyColumns(),
		"the lens must activate against its declared IntoKey")

	keys, ok := eng.AnchorDeleteResult(cr, patientKey, "patient", body)
	require.True(t, ok,
		"patient_id must resolve read-free from the tombstoned root body, across the WITH boundary, to p's own key column — "+
			"this is the retraction transport; DiffRetraction is the healer, not a substitute for it")
	require.Equal(t, map[string]any{"patient_id": f.ids["alice"]}, keys,
		"the Delete must target the exact row the live patient upserted")
}
