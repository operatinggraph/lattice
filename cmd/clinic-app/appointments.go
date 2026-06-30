package main

import (
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

// handleAppointments implements GET /api/appointments?patient=&provider= — both
// the patient's appointment tracker and a provider's schedule, served from the
// `clinicAppointments` lens read model (NOT Core KV). Pass patient to scope to one
// patient, provider to scope to one provider's schedule.
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
	patient := r.URL.Query().Get("patient")
	provider := r.URL.Query().Get("provider")
	rows := computeAppointments(keys, get, patient, provider)
	s.writeJSON(w, http.StatusOK, map[string]any{"appointments": rows, "count": len(rows)})
}
