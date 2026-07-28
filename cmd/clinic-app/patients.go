package main

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
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
// variable. Every row projects its own patient's NanoID as its authz_anchor,
// so an actor matches a row either by holding the reserved WildcardAnchor
// grant (the whole roster) or, via patientIdentityReadGrants, by being the
// identity that row's patient is identifiedBy (its own row only). Sorted by
// name for a stable switcher, mirroring the retired computePatients' sort.
// identity_key (nil for a patient with no identifiedBy link) is what lets the
// FE offer patient self-service booking — see the clinicPatientsRead lens spec.
const selectPatientsSQL = `
SELECT patient_key, name, email, phone, identity_key
FROM read_clinic_patients
ORDER BY name, patient_key`

// selectPatientsFilteredSQL narrows the roster to a name-ILIKE match, capped
// so a broad term can't pull the whole clinic's history into one response —
// mirrors loftspace-app's selectSearchPeopleSQL (search.go).
const selectPatientsFilteredSQL = `
SELECT patient_key, name, email, phone, identity_key
FROM read_clinic_patients
WHERE name ILIKE $1
ORDER BY name, patient_key
LIMIT 50`

// queryPatients runs the protected read inside a per-request transaction with a
// txn-local actor session variable — the same pooling-safety discipline as
// queryMyAppointments / queryMyVisitSeries (SET LOCAL is discarded at
// COMMIT/ROLLBACK, so the pooled connection inherits no actor across
// requests). The query itself carries no auth filter; RLS is the scope. q, if
// non-empty, narrows to a case-insensitive name match (front-desk typeahead) —
// empty q preserves the prior unfiltered/unlimited behavior.
func queryPatients(ctx context.Context, pool pgxBeginner, actorID, q string) ([]protectedPatientRow, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return nil, err
	}

	var rows pgx.Rows
	if q == "" {
		rows, err = tx.Query(ctx, selectPatientsSQL)
	} else {
		rows, err = tx.Query(ctx, selectPatientsFilteredSQL, "%"+q+"%")
	}
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
// clinic, by full name). An actor reads a row here either by holding the
// reserved WildcardAnchor grant (the bootstrap capabilityReadWildcardGrants
// lens, kernel-seeded root-equivalent identities only, D1 design §3.4 M5) —
// the whole roster, front-desk's view — or, via patientIdentityReadGrants,
// by being the identity that row's own patient is identifiedBy — exactly
// their own row, which is what lets a signed-in patient session find itself
// (cmd/clinic-app/web/app.js's applySelfPatientLock).
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

	q := r.URL.Query().Get("q")
	rows, err := queryPatients(ctx, s.pgPool, actor.Subject, q)
	if err != nil {
		s.logger.Error("read protected clinic patients (staff)", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected patients model")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"patients": rows, "count": len(rows), "scope": "rls"})
}
