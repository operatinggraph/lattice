package main

import (
	"context"
	"net/http"
)

// protectedPatientRow is one row of the clinicPatientsRead protected Postgres
// read model (D1.5, the staff-wildcard increment). Email/Phone are Vault
// Fire 5 Secure-Lens columns — decrypted at projection from the patient's
// optional identifiedBy identity — so they are nil for a patient with no
// linked identity, a linked identity missing that aspect, or a shredded one;
// never an error, never a dropped row.
type protectedPatientRow struct {
	PatientKey  string  `json:"patientKey"`
	Name        string  `json:"name"`
	Email       *string `json:"email,omitempty"`
	Phone       *string `json:"phone,omitempty"`
	IdentityKey *string `json:"identityKey,omitempty"`
}

// selectPatientsSQL reads the protected model. It carries NO auth WHERE — the
// RLS policy (FORCE ROW LEVEL SECURITY + the §6.14 set-membership policy)
// injects the actor scope from the txn-local lattice.actor_id session
// variable. Every row here projects an EMPTY authz_anchors set (there is no
// per-patient self-anchor for "the whole roster"), so only an actor holding
// the reserved WildcardAnchor grant ever matches a row. Sorted by name for a
// stable switcher, mirroring the retired computePatients' sort. identity_key
// (nil for a patient with no identifiedBy link) is what lets the FE offer
// patient self-service booking — see the clinicPatientsRead lens spec.
const selectPatientsSQL = `
SELECT patient_key, name, email, phone, identity_key
FROM read_clinic_patients
ORDER BY name, patient_key`

// queryPatients runs the protected read inside a per-request transaction with a
// txn-local actor session variable — the same pooling-safety discipline as
// queryMyAppointments / queryMyVisitSeries (SET LOCAL is discarded at
// COMMIT/ROLLBACK, so the pooled connection inherits no actor across
// requests). The query itself carries no auth filter; RLS is the scope.
func queryPatients(ctx context.Context, pool pgxBeginner, actorID string) ([]protectedPatientRow, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, selectPatientsSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]protectedPatientRow, 0)
	for rows.Next() {
		var row protectedPatientRow
		if err := rows.Scan(&row.PatientKey, &row.Name, &row.Email, &row.Phone, &row.IdentityKey); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// handleStaffPatients implements GET /api/staff/patients — the clinic-wide
// patient-context switcher roster, PROTECTED and RLS-scoped (D1.5, mirroring
// handleStaffAppointments / handleStaffVisitSeries). It replaces the retired
// handlePatients, which served the same roster from the unprotected
// clinicPatients NATS-KV bucket to ANY caller with no authentication at all —
// a clinic-wide membership-disclosure PHI dump (which patients exist at this
// clinic, by full name). Every row projects an empty authz_anchors set, so
// only an actor holding the reserved WildcardAnchor grant (the bootstrap
// capabilityReadWildcardGrants lens, kernel-seeded root-equivalent identities
// only, D1 design §3.4 M5) can read a row here — unlike appointments /
// visit-series there is no patient-self view of "the whole roster" to carve
// out separately.
func (s *server) handleStaffPatients(w http.ResponseWriter, r *http.Request) {
	actor, err := s.authenticateRead(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required: "+err.Error())
		return
	}
	if s.pgPool == nil {
		s.writeError(w, http.StatusBadGateway,
			"protected read model not configured (set CLINIC_APP_PG_DSN and ensure Postgres + the clinic-domain protected lens are up)")
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	rows, err := queryPatients(ctx, s.pgPool, actor.Subject)
	if err != nil {
		s.logger.Error("read protected clinic patients (staff)", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected patients model")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"patients": rows, "count": len(rows), "scope": "rls"})
}
