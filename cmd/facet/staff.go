package main

import (
	"context"
	"net/http"
	"time"

	"github.com/operatinggraph/lattice/internal/appsession"
)

// facet-staff-worlds-design.md §3.4: the Protected worklist pane. Unlike the
// manifest half (§3.3), which rides the personal SYNC plane and mirrors onto
// the device, these rows are CROSS-IDENTITY and PII-bearing, so they are
// session-scoped server-pane reads — never mirrored, unavailable offline.
//
// The read spine is §3.5's: RLS runs as the signed-in staff actor, and the
// only tokens that actor holds are the BUILDING NanoIDs staffReadGrants
// projects for its (holdsRole frontOfHouse × worksAt building) pairs. No
// query here filters by workplace itself — the confinement is the grant, so a
// staff actor with no worksAt link reads zero rows rather than a filtered
// view of everyone's. Same fail-closed shape as an empty grant set.

// staffApplicationRow is one "application to review" — a pending-decision
// lease application at the reader's workplace.
type staffApplicationRow struct {
	AppID         string  `json:"appId"`
	ApplicantName string  `json:"applicantName"`
	UnitAddress   string  `json:"unitAddress"`
	UnitCity      string  `json:"unitCity"`
	SignedAt      string  `json:"signedAt"`
	MoveInDate    string  `json:"moveInDate"`
	Qualified     *bool   `json:"qualified"`
	RequestedRent float64 `json:"requestedRent"`
}

// staffAppointmentRow is one row of today's schedule at the reader's workplace.
type staffAppointmentRow struct {
	AppointmentID     string `json:"appointmentId"`
	StartsAt          string `json:"startsAt"`
	EndsAt            string `json:"endsAt"`
	Status            string `json:"status"`
	PatientName       string `json:"patientName"`
	ProviderName      string `json:"providerName"`
	ProviderSpecialty string `json:"providerSpecialty"`
}

// selectStaffApplicationsSQL reads the pending-decision slice of the landlord
// lease-application model. No landlord/building predicate: RLS already scoped
// the visible rows to the workplace tokens this actor holds.
const selectStaffApplicationsSQL = `
SELECT app_id, applicant_name, unit_address, unit_city,
       signed_at, terms_move_in_date, qualified, terms_requested_rent
FROM read_landlord_lease_applications
WHERE landlord_decision IS NULL
ORDER BY signed_at NULLS LAST
LIMIT 200`

// selectStaffScheduleSQL reads one day of the clinic appointment model.
//
// The column list is deliberately NARROWER than the table: `reason` and the
// encounter-derived signals (documented_at, follow_up_requested, follow_up_date)
// are clinical content, and a front-desk worklist's business is visit existence
// and timing — the same PII narrowing §3.4 states for the front-desk buckets.
// Widening this list is a PHI decision, not a display tweak.
//
// starts_at is an ISO-8601 text column, so a lexicographic half-open range is
// an exact day filter.
const selectStaffScheduleSQL = `
SELECT appointment_id, starts_at, ends_at, status,
       patient_name, provider_name, provider_specialty
FROM read_clinic_appointments
WHERE starts_at >= $1 AND starts_at < $2
ORDER BY starts_at
LIMIT 200`

// queryStaffWorklist runs both worklist reads inside ONE transaction with a
// single txn-local actor session variable — the credentials.go shape, extended
// to two SELECTs so both panes observe the same grant state. actorID must be
// the session identity's own bare NanoID; nothing here accepts a caller-supplied
// actor or workplace.
func queryStaffWorklist(ctx context.Context, pool pgxBeginner, actorID string, dayStart, dayEnd string) ([]staffApplicationRow, []staffAppointmentRow, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return nil, nil, err
	}

	apps := []staffApplicationRow{}
	rows, err := tx.Query(ctx, selectStaffApplicationsSQL)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var a staffApplicationRow
		var applicantName, unitAddress, unitCity, signedAt, moveIn pgtypeText
		var rent pgtypeFloat
		if err := rows.Scan(&a.AppID, &applicantName, &unitAddress, &unitCity,
			&signedAt, &moveIn, &a.Qualified, &rent); err != nil {
			rows.Close()
			return nil, nil, err
		}
		a.ApplicantName, a.UnitAddress, a.UnitCity = applicantName.val, unitAddress.val, unitCity.val
		a.SignedAt, a.MoveInDate, a.RequestedRent = signedAt.val, moveIn.val, rent.val
		apps = append(apps, a)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows.Close()

	sched := []staffAppointmentRow{}
	rows2, err := tx.Query(ctx, selectStaffScheduleSQL, dayStart, dayEnd)
	if err != nil {
		return nil, nil, err
	}
	for rows2.Next() {
		var s staffAppointmentRow
		var startsAt, endsAt, status, patientName, providerName, specialty pgtypeText
		if err := rows2.Scan(&s.AppointmentID, &startsAt, &endsAt, &status,
			&patientName, &providerName, &specialty); err != nil {
			rows2.Close()
			return nil, nil, err
		}
		s.StartsAt, s.EndsAt, s.Status = startsAt.val, endsAt.val, status.val
		s.PatientName, s.ProviderName, s.ProviderSpecialty = patientName.val, providerName.val, specialty.val
		sched = append(sched, s)
	}
	if err := rows2.Err(); err != nil {
		return nil, nil, err
	}
	rows2.Close()

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return apps, sched, nil
}

// pgtypeText / pgtypeFloat scan a nullable column into its zero value. Every
// display column on both models is optional (an unlinked unit, an unnamed
// provider, an unsigned application), and a null must cost a FIELD, never the
// row — the same rule §6's null-element hazard settled for the anchors.
type pgtypeText struct{ val string }

func (t *pgtypeText) Scan(src any) error {
	if src == nil {
		t.val = ""
		return nil
	}
	switch v := src.(type) {
	case string:
		t.val = v
	case []byte:
		t.val = string(v)
	}
	return nil
}

type pgtypeFloat struct{ val float64 }

func (f *pgtypeFloat) Scan(src any) error {
	if src == nil {
		f.val = 0
		return nil
	}
	if v, ok := src.(float64); ok {
		f.val = v
	}
	return nil
}

// handleStaffWorklist implements GET /api/staff/worklist — the front-desk pane.
func (s *server) handleStaffWorklist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	identityID, ok := appsession.Identity(r.Context())
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "no session identity")
		return
	}
	// These rows are OTHER people's names and appointments. The boot-env
	// fallback identity proves nothing about who is connecting — it hands the
	// process's identity to any caller — so it must never reach a
	// cross-identity PII surface, exactly as credentials.go refuses it for the
	// SENSITIVE credential set. RLS would still confine the rows to the boot
	// identity's grants; it cannot tell that the caller isn't that identity.
	if !appsession.ViaCookie(r.Context()) {
		s.writeError(w, http.StatusForbidden, "sign in to view the staff worklist")
		return
	}
	if s.pgPool == nil {
		s.writeError(w, http.StatusBadGateway,
			"protected read model not configured (set FACET_PG_DSN and ensure Postgres + the lease-signing/clinic protected lenses are up)")
		return
	}
	dayStart, dayEnd := utcDayBounds(time.Now().UTC())

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	apps, sched, err := queryStaffWorklist(ctx, s.pgPool, identityID, dayStart, dayEnd)
	if err != nil {
		s.logger.Error("facet: read staff worklist", "identityId", identityID, "err", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected staff worklist models")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"applications": apps,
		"schedule":     sched,
		"day":          dayStart[:10],
	})
}

// utcDayBounds returns the half-open [start, end) ISO-8601 bounds of t's UTC day.
func utcDayBounds(t time.Time) (string, string) {
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return start.Format(time.RFC3339), start.AddDate(0, 0, 1).Format(time.RFC3339)
}
