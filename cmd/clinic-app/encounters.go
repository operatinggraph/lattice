package main

import (
	"context"
	"net/http"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// protectedEncounterRow is one row of the PROTECTED clinicEncountersRead
// Postgres read model — the only read path back to a documented visit's
// clinical content (RecordEncounter's .encounter aspect, decrypted at
// projection under the clinicalRecord retention-class key). RLS has already
// scoped the rows to the requesting actor (the treating provider, or a
// WildcardAnchor holder) before they reach here, so there is no client-side
// filter and no way to ask for a note by appointment id that RLS did not
// already agree to return.
//
// AppointmentKey carries entity_key (the full vtx key), matching
// protectedAppointmentRow.EntityKey's json tag exactly, so the FE can join an
// appointment card to its note by that field without a second key format to
// reconcile. PatientKey/ProviderKey/DocumentedAt/Summary/Assessment/Plan are
// plain (non-pointer) strings — see selectMyEncountersSQL's COALESCE comment
// for why a defensively-COALESCEd empty string, not a pointer, is the right
// shape here.
type protectedEncounterRow struct {
	AppointmentKey string `json:"appointmentKey"`
	PatientKey     string `json:"patientKey,omitempty"`
	ProviderKey    string `json:"providerKey"`
	DocumentedAt   string `json:"documentedAt"`
	Summary        string `json:"summary"`
	Assessment     string `json:"assessment,omitempty"`
	Plan           string `json:"plan,omitempty"`
}

// selectMyEncountersSQL reads the protected model. It carries NO auth WHERE —
// the RLS policy injects the actor scope from the txn-local
// lattice.actor_id session variable, exactly like selectMyProviderScheduleSQL.
//
// Every column is COALESCEd defensively, even the ones clinicEncountersRead's
// cypher treats as always-populated for a returned row (withProvider is a
// REQUIRED match, and the WHERE presence filter keys off documentedAt, so a
// row exists only once RecordEncounter has written both sibling aspects in
// one batch, with an unfilled optional field written as ""). That guarantee
// lives in the lens spec, not in this query — protectedEncounterRow's fields
// are plain (non-pointer) strings, so a scan target must never see a NULL
// regardless. patient_key is the one genuinely OPTIONAL column (forPatient is
// an OPTIONAL walk), COALESCEd to "" rather than carried as a pointer,
// mirroring selectMyProviderScheduleSQL's own patient_key column.
//
// Rows sort by documented_at (then appointment_id) for a stable order; the FE
// only ever looks a row up by appointmentKey, so the order is cosmetic.
const selectMyEncountersSQL = `
SELECT entity_key, COALESCE(patient_key, ''), COALESCE(provider_key, ''), COALESCE(documented_at, ''),
       COALESCE(summary, ''), COALESCE(assessment, ''), COALESCE(plan, '')
FROM read_clinic_encounters
ORDER BY documented_at, appointment_id`

// queryMyEncounters is queryMyProviderSchedule's clinicEncountersRead sibling
// — identical txn-local actor + pooling-safety discipline (set_config is
// is_local=true, so it is discarded at COMMIT/ROLLBACK and the pooled
// connection returns clean for the next request), reading read_clinic_encounters
// instead of read_provider_appointments. actorID must be the bare identity
// NanoID (VerifiedActor.Subject) — the treating provider's own NanoID, or a
// WildcardAnchor holder's.
func queryMyEncounters(ctx context.Context, pool pgxBeginner, actorID string) ([]protectedEncounterRow, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, selectMyEncountersSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]protectedEncounterRow, 0)
	for rows.Next() {
		var row protectedEncounterRow
		if err := rows.Scan(
			&row.AppointmentKey, &row.PatientKey, &row.ProviderKey, &row.DocumentedAt,
			&row.Summary, &row.Assessment, &row.Plan,
		); err != nil {
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

// handleMyEncounters implements GET /api/my-encounters — the clinical record
// itself, served from the PROTECTED clinicEncountersRead Postgres read model
// as an AUTHENTICATED actor (mirroring handleMyProviderSchedule). The actor
// comes ONLY from the verified session cookie; RLS returns only the rows that
// actor's own bare NanoID (or a held WildcardAnchor) anchors, so there is no
// client-supplied appointment or provider filter — a caller cannot ask for a
// note by id and get it if RLS would not otherwise return it. A whole-list
// shape, like every other protected read this app serves: the FE joins a
// note to its appointment card client-side by appointmentKey, the same way
// renderApptCard already joins the operational documentedAt/followUp*
// columns from the appointment row.
func (s *server) handleMyEncounters(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusBadRequest, "GET required")
		return
	}
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

	rows, err := queryMyEncounters(ctx, s.pgPool, actor.Subject)
	if err != nil {
		// Log the detail (which can carry the failing SQL / schema names) and return
		// a generic message — never echo a raw DB error, let alone clinical content,
		// to the client.
		s.logger.Error("read protected clinic encounters", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected encounters model")
		return
	}
	resp := s.withProjectionHealth(ctx, pkgmgr.LensID("clinic-domain", "clinicEncountersRead"),
		map[string]any{"encounters": rows, "count": len(rows), "scope": "rls"})
	s.writeJSON(w, http.StatusOK, resp)
}
