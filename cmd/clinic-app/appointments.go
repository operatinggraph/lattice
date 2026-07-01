package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	clinicdomain "github.com/asolgan/lattice/packages/clinic-domain"
)

// availabilityRow is the UNAUTHENTICATED provider-availability view (D1.5): the
// booking slot-picker only needs a named provider's busy time windows to compute
// open slots (see computeOpenSlots in app.js), never patient identity or visit
// content — so this DTO carries nothing else.
type availabilityRow struct {
	AppointmentKey string `json:"appointmentKey"`
	StartsAt       string `json:"startsAt"`
	EndsAt         string `json:"endsAt"`
	Status         string `json:"status"`
}

// computeAvailability assembles a single NAMED provider's busy windows from the
// `clinicAppointments` lens read model. It decodes each row into a struct that
// only carries appointmentKey/startsAt/endsAt/status/providerKey — patient
// identity, visit reason, and encounter/documentation fields are never
// unmarshaled at all, so this unauthenticated path can't leak them regardless of
// what the lens projects. A row that fails to decode, carries no appointmentKey
// (a tombstoned projection entry), or belongs to a different provider is
// skipped. Rows are sorted by startsAt (then key) so a list reads
// chronologically.
func computeAvailability(keys []string, get kvGetter, provider string) []availabilityRow {
	rows := make([]availabilityRow, 0, len(keys))
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var a struct {
			AppointmentKey string `json:"appointmentKey"`
			StartsAt       string `json:"startsAt"`
			EndsAt         string `json:"endsAt"`
			Status         string `json:"status"`
			ProviderKey    string `json:"providerKey"`
		}
		if json.Unmarshal(raw, &a) != nil || a.AppointmentKey == "" {
			continue
		}
		if a.ProviderKey != provider {
			continue
		}
		rows = append(rows, availabilityRow{
			AppointmentKey: a.AppointmentKey,
			StartsAt:       a.StartsAt,
			EndsAt:         a.EndsAt,
			Status:         a.Status,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].StartsAt != rows[j].StartsAt {
			return rows[i].StartsAt < rows[j].StartsAt
		}
		return rows[i].AppointmentKey < rows[j].AppointmentKey
	})
	return rows
}

// handleAppointments implements GET /api/appointments?provider=<key> — the
// booking slot-picker's provider-availability check ONLY, served from the
// unprotected `clinicAppointments` lens read model (NOT Core KV, but also not
// authenticated). `provider` is REQUIRED (D1.5, the staff wildcard
// increment): any caller (typically a patient mid-booking, not the provider)
// may check a NAMED provider's busy times to compute open slots, but a blank
// `?provider=` would return the clinic-wide unscoped dump — the
// follow-ups-worklist/"All providers" vector this increment closes. That
// clinic-wide view now lives at handleStaffAppointments, the authenticated,
// RLS-scoped read.
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
//
// The response rows are the minimal availabilityRow shape (D1.5, PHI
// over-exposure fix): the caller supplied the provider key, so echoing it and
// the provider's own name/specialty back is redundant, and every OTHER field
// the lens projects (patient identity, reason, documentedAt/followUp*,
// reminders) was pure PHI/PII an unauthenticated availability check never
// needed. The FE's slot picker (providerAppointments → computeOpenSlots /
// apptBlocks) only ever reads startsAt/endsAt/status.
func (s *server) handleAppointments(w http.ResponseWriter, r *http.Request) {
	// TrimSpace so a whitespace-only value (e.g. "?provider=%20") is treated as
	// blank too — the guard exists to close the clinic-wide dump, so it must
	// reject anything that isn't a real provider key, not just the empty string.
	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	if provider == "" {
		s.writeError(w, http.StatusBadRequest,
			"provider is required (the clinic-wide unscoped view moved to the authenticated /api/staff/appointments)")
		return
	}
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
	rows := computeAvailability(keys, get, provider)
	s.writeJSON(w, http.StatusOK, map[string]any{"appointments": rows, "count": len(rows)})
}

// protectedAppointmentRow is one row of the PROTECTED clinicAppointmentsRead
// Postgres read model (D1.5), returned by the authenticated reader. RLS has
// already scoped the rows to the requesting actor before they reach here, so
// there is no client-side filter. Nullable columns (the OPTIONAL provider walk,
// and the optional reason/status-note/reminder/encounter fields) are pointers so
// an absent value stays absent rather than rendering a misleading empty string.
// JSON tags deliberately match availabilityRow's (appointmentKey, not entityKey)
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
// mirroring computeAvailability's sort on the unprotected side.
//
// follow_up_requested, starts_at, and status are COALESCEd (to false / "" / "")
// defensively: EntityKey/StartsAt/Status/PatientKey/FollowUpRequested are plain
// (non-pointer) Go fields on protectedAppointmentRow, so a scan target must never see a
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
// additionally needs COALESCE(..., ”): forPatient is OPTIONAL in
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

// handleStaffAppointments implements GET /api/staff/appointments — the
// clinic-wide staff views (the follow-ups worklist, the "All providers"
// schedule aggregate) that used to read the unprotected, unscoped
// handleAppointments path (D1.5, the staff wildcard increment closing that
// vector). It reuses queryMyAppointments verbatim: the query itself carries
// no auth filter, so the SAME read_clinic_appointments query that scopes an
// ordinary patient to their own rows returns EVERY row for an actor holding
// the reserved WildcardAnchor ("*") grant (internal/refractor/adapter.
// WildcardAnchor) — the bootstrap capabilityReadWildcardGrants lens grants it
// to the kernel-seeded root-equivalent identities only (D1 design §3.4 M5).
// This is still RLS, never a bypass: an all-access read is attributable and
// revocable exactly like any other actor_read_grants row.
func (s *server) handleStaffAppointments(w http.ResponseWriter, r *http.Request) {
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
		s.logger.Error("read protected clinic appointments (staff)", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not read the protected appointments model")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"appointments": rows, "count": len(rows), "scope": "rls"})
}
