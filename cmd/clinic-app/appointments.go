package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"

	clinicdomain "github.com/asolgan/lattice/packages/clinic-domain"
)

// appointmentRow is one row of the clinic-domain `clinicAppointments` lens read
// model (P5: an application reads the lens projection, never Core KV). It is the
// joined shape the FE renders in both "My Appointments" (scoped by patientKey) and
// the provider "Schedule" (scoped by providerKey). Neighbour columns (patientName /
// providerName / providerSpecialty) are empty when a link is absent.
type appointmentRow struct {
	AppointmentKey    string `json:"appointmentKey"`
	StartsAt          string `json:"startsAt"`
	EndsAt            string `json:"endsAt"`
	Reason            string `json:"reason,omitempty"`
	Status            string `json:"status"`
	PatientKey        string `json:"patientKey"`
	PatientName       string `json:"patientName,omitempty"`
	ProviderKey       string `json:"providerKey"`
	ProviderName      string `json:"providerName,omitempty"`
	ProviderSpecialty string `json:"providerSpecialty,omitempty"`
	// ReminderSentAt is the RFC3339 instant the ~24h appointment reminder fired
	// (the clinic-reminders convergence, surfaced via the clinicAppointments lens).
	// Empty until the reminder is sent (or when clinic-reminders is not installed).
	ReminderSentAt string `json:"reminderSentAt,omitempty"`
	// FollowUpReminderSentAt is the RFC3339 instant the at-the-date follow-up reminder
	// fired (the clinic-reminders followUpReminders convergence). Empty until it fires
	// (or when clinic-reminders is not installed).
	FollowUpReminderSentAt string `json:"followUpReminderSentAt,omitempty"`
	// DocumentedAt / FollowUpRequested / FollowUpDate are the OPERATIONAL, non-PHI
	// encounter signals the clinicAppointments lens projects after RecordEncounter
	// documents a completed visit. The RAW clinical content (summary / assessment /
	// plan) is PHI and is NEVER projected (the deferred Vault plane owns its display),
	// so it never reaches this DTO. DocumentedAt is the "visit documented" presence
	// signal; FollowUpDate is set only when a follow-up was requested. All empty until
	// the visit is documented.
	DocumentedAt      string `json:"documentedAt,omitempty"`
	FollowUpRequested bool   `json:"followUpRequested,omitempty"`
	FollowUpDate      string `json:"followUpDate,omitempty"`
}

// computeAppointments assembles appointment rows from the `clinicAppointments`
// lens read model, scoped to a patient and/or a provider. A non-empty patient
// keeps only rows whose patientKey matches; a non-empty provider keeps only rows
// whose providerKey matches (both empty returns every appointment). A row that
// fails to decode or carries no appointmentKey (a tombstoned projection entry) is
// skipped. Rows are sorted by startsAt (then key) so a list reads chronologically.
func computeAppointments(keys []string, get kvGetter, patient, provider string) []appointmentRow {
	rows := make([]appointmentRow, 0, len(keys))
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var a appointmentRow
		if json.Unmarshal(raw, &a) != nil || a.AppointmentKey == "" {
			continue
		}
		if patient != "" && a.PatientKey != patient {
			continue
		}
		if provider != "" && a.ProviderKey != provider {
			continue
		}
		rows = append(rows, a)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].StartsAt != rows[j].StartsAt {
			return rows[i].StartsAt < rows[j].StartsAt
		}
		return rows[i].AppointmentKey < rows[j].AppointmentKey
	})
	return rows
}

// handleAppointments implements GET /api/appointments?provider= — the booking
// slot-picker's provider-availability check, the clinic-wide follow-ups
// worklist (unscoped), and the "All providers" schedule aggregate, served from
// the unprotected `clinicAppointments` lens read model (NOT Core KV, but also
// not authenticated).
//
// It NO LONGER accepts `?patient=` (D1.5): that vector let ANY caller read any
// named patient's full appointment history — including the post-visit
// documentedAt/followUpRequested signals — by supplying an arbitrary patient key,
// with no authentication at all. The patient-self view moved to
// handleMyAppointments, the PROTECTED, RLS-scoped, authenticated read.
//
// A single named provider's own "My Schedule" view has similarly moved to
// handleMyProviderSchedule (D1.5 Increment 2, the provider-self anchor); the FE
// now calls that authenticated path whenever a specific provider is selected.
// `?provider=` stays HERE, unauthenticated, only for the slot-picker's
// availability check (any caller — typically a patient mid-booking, not the
// provider — checking a provider's busy times to compute open slots). The
// unscoped reads (follow-ups worklist, "All providers" aggregate) remain open
// too: neither has a per-actor anchor to scope by yet, and closing them needs a
// staff/admin wildcard grant (see packages/clinic-domain/lenses.go's
// providerAppointmentsRead doc, the D1 design's M5 Loupe-all-access posture
// call) — flagged on the board, not freelanced here.
func (s *server) handleAppointments(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()

	bucket := clinicdomain.ClinicAppointmentsBucket
	keys, err := conn.KVListKeys(ctx, bucket)
	if err != nil {
		s.writeError(w, http.StatusBadGateway,
			"list "+bucket+": "+err.Error()+" (is clinic-domain installed and the Refractor projecting?)")
		return
	}
	get := func(key string) ([]byte, bool) {
		entry, err := conn.KVGet(ctx, bucket, key)
		if err != nil {
			return nil, false
		}
		return entry.Value, true
	}
	provider := r.URL.Query().Get("provider")
	rows := computeAppointments(keys, get, "", provider)
	s.writeJSON(w, http.StatusOK, map[string]any{"appointments": rows, "count": len(rows)})
}

// protectedAppointmentRow is one row of the PROTECTED clinicAppointmentsRead
// Postgres read model (D1.5), returned by the authenticated reader. RLS has
// already scoped the rows to the requesting actor before they reach here, so
// there is no client-side filter. Nullable columns (the OPTIONAL provider walk,
// and the optional reason/status-note/reminder/encounter fields) are pointers so
// an absent value stays absent rather than rendering a misleading empty string.
// JSON tags deliberately match appointmentRow's (appointmentKey, not entityKey)
// so the FE's shared card renderer (renderApptCard et al.) needs no changes
// between the unprotected and protected read paths.
type protectedAppointmentRow struct {
	EntityKey              string  `json:"appointmentKey"`
	StartsAt               string  `json:"startsAt"`
	EndsAt                 *string `json:"endsAt,omitempty"`
	Reason                 *string `json:"reason,omitempty"`
	Status                 string  `json:"status"`
	StatusNote             *string `json:"statusNote,omitempty"`
	PatientKey             string  `json:"patientKey"`
	PatientName            *string `json:"patientName,omitempty"`
	ProviderKey            *string `json:"providerKey,omitempty"`
	ProviderName           *string `json:"providerName,omitempty"`
	ProviderSpecialty      *string `json:"providerSpecialty,omitempty"`
	ReminderSentAt         *string `json:"reminderSentAt,omitempty"`
	FollowUpReminderSentAt *string `json:"followUpReminderSentAt,omitempty"`
	DocumentedAt           *string `json:"documentedAt,omitempty"`
	FollowUpRequested      bool    `json:"followUpRequested,omitempty"`
	FollowUpDate           *string `json:"followUpDate,omitempty"`
}

// selectMyAppointmentsSQL reads the protected model. It carries NO auth WHERE —
// the RLS policy (FORCE ROW LEVEL SECURITY + the set-membership policy) injects
// the actor scope from the txn-local lattice.actor_id session variable. Rows
// sort by starts_at (then appointment_id) for a stable, chronological view,
// mirroring computeAppointments' sort on the unprotected side.
//
// follow_up_requested, starts_at, and status are COALESCEd (to false / "" / "")
// defensively: EntityKey/StartsAt/Status/PatientKey/FollowUpRequested are plain
// (non-pointer) Go fields on protectedAppointmentRow, matching the unprotected
// appointmentRow's zero-value convention, so a scan target must never see a
// NULL even for a column the write-path treats as always-populated (starts_at
// and status are written atomically with the anchor forPatient link by
// CreateAppointment) — a defensive backstop, not a documented possible state.
const selectMyAppointmentsSQL = `
SELECT entity_key, COALESCE(starts_at, ''), ends_at, reason, COALESCE(status, ''), status_note,
       patient_key, patient_name, provider_key, provider_name, provider_specialty,
       reminder_sent_at, follow_up_reminder_sent_at, documented_at,
       COALESCE(follow_up_requested, false), follow_up_date
FROM read_clinic_appointments
ORDER BY starts_at, appointment_id`

// queryMyAppointments runs the protected read inside a per-request transaction
// with a txn-local actor session variable. The transaction is the
// pooling-safety crux: set_config(..., is_local=true) is discarded at
// COMMIT/ROLLBACK, so the pooled connection returns clean and the next request
// inherits no actor (deny) until it sets its own. The query itself carries no
// auth filter — RLS is the scope.
//
// actorID must be the bare identity NanoID (VerifiedActor.Subject), matching the
// patient_key anchor's nanoIdFromKey representation and the actor_id column in
// actor_read_grants.
func queryMyAppointments(ctx context.Context, pool pgxBeginner, actorID string) ([]protectedAppointmentRow, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, selectMyAppointmentsSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]protectedAppointmentRow, 0)
	for rows.Next() {
		var row protectedAppointmentRow
		if err := rows.Scan(
			&row.EntityKey, &row.StartsAt, &row.EndsAt, &row.Reason, &row.Status, &row.StatusNote,
			&row.PatientKey, &row.PatientName, &row.ProviderKey, &row.ProviderName, &row.ProviderSpecialty,
			&row.ReminderSentAt, &row.FollowUpReminderSentAt, &row.DocumentedAt,
			&row.FollowUpRequested, &row.FollowUpDate,
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

// handleMyAppointments implements GET /api/my-appointments — the patient's own
// appointment tracker, served from the PROTECTED clinicAppointmentsRead Postgres
// read model as an AUTHENTICATED actor (D1.5, mirroring loftspace-app's
// handleApplications). The actor comes ONLY from the verified JWT; RLS returns
// only that actor's rows, so there is no client-supplied patient filter (unlike
// the old `/api/appointments?patient=` vector, which let anyone impersonate any
// patient).
func (s *server) handleMyAppointments(w http.ResponseWriter, r *http.Request) {
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

	rows, err := queryMyAppointments(ctx, s.pgPool, actor.Subject)
	if err != nil {
		// Log the detail (which can carry the failing SQL / schema names) and return
		// a generic message — never echo a raw DB error to the client.
		s.logger.Error("read protected clinic appointments", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected appointments model")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"appointments": rows, "count": len(rows), "scope": "rls"})
}

// selectMyProviderScheduleSQL is selectMyAppointmentsSQL's provider-anchored
// sibling (D1.5 Increment 2), reading the providerAppointmentsRead protected
// model instead of the patient-anchored one. Same column surface
// (protectedAppointmentRow is shared by both queries — the two tables project
// identical columns, just anchored on a different actor), but patient_key
// additionally needs COALESCE(..., ''): forPatient is OPTIONAL in
// providerAppointmentsReadSpec (patient is the display neighbour here, not the
// anchor — the reverse of clinicAppointmentsRead, where forPatient is
// REQUIRED and patient_key is never null), so an appointment with no patient
// link would otherwise scan a NULL into protectedAppointmentRow.PatientKey's
// non-pointer string field.
const selectMyProviderScheduleSQL = `
SELECT entity_key, COALESCE(starts_at, ''), ends_at, reason, COALESCE(status, ''), status_note,
       COALESCE(patient_key, ''), patient_name, provider_key, provider_name, provider_specialty,
       reminder_sent_at, follow_up_reminder_sent_at, documented_at,
       COALESCE(follow_up_requested, false), follow_up_date
FROM read_provider_appointments
ORDER BY starts_at, appointment_id`

// queryMyProviderSchedule is queryMyAppointments' provider-anchored sibling —
// identical txn-local actor + pooling-safety discipline, reading
// read_provider_appointments instead of read_clinic_appointments. actorID must
// be the bare provider identity NanoID (VerifiedActor.Subject), matching the
// provider_key anchor's nanoIdFromKey representation.
func queryMyProviderSchedule(ctx context.Context, pool pgxBeginner, actorID string) ([]protectedAppointmentRow, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, selectMyProviderScheduleSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]protectedAppointmentRow, 0)
	for rows.Next() {
		var row protectedAppointmentRow
		if err := rows.Scan(
			&row.EntityKey, &row.StartsAt, &row.EndsAt, &row.Reason, &row.Status, &row.StatusNote,
			&row.PatientKey, &row.PatientName, &row.ProviderKey, &row.ProviderName, &row.ProviderSpecialty,
			&row.ReminderSentAt, &row.FollowUpReminderSentAt, &row.DocumentedAt,
			&row.FollowUpRequested, &row.FollowUpDate,
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

// handleMyProviderSchedule implements GET /api/my-schedule — a provider's own
// schedule, served from the PROTECTED providerAppointmentsRead Postgres read
// model as an AUTHENTICATED actor (D1.5 Increment 2, mirroring
// handleMyAppointments / loftspace-app's handleLandlordApplications). The
// actor comes ONLY from the verified JWT; RLS returns only that provider's
// rows, so there is no client-supplied provider filter (unlike the old
// `/api/appointments?provider=` vector, which let anyone read any named
// provider's full schedule).
func (s *server) handleMyProviderSchedule(w http.ResponseWriter, r *http.Request) {
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

	rows, err := queryMyProviderSchedule(ctx, s.pgPool, actor.Subject)
	if err != nil {
		// Log the detail (which can carry the failing SQL / schema names) and return
		// a generic message — never echo a raw DB error to the client.
		s.logger.Error("read protected provider schedule", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected provider schedule model")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"appointments": rows, "count": len(rows), "scope": "rls"})
}
