package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// The D1.5 staff-wildcard headline proof: a WildcardAnchor grant
// (adapter.WildcardAnchor) lets an actor read EVERY row of a protected table,
// not just its own — the real Postgres RLS enforcement of the policy's
// wildcard OR-clause (internal/refractor/adapter/rls.go), driven through
// handleStaffAppointments exactly like a live clinic-domain deployment would
// (the same read_clinic_appointments table + query queryMyAppointments
// already uses for the patient-self view).
//
// Enforcement is REAL (non-superuser reader role, same fixture discipline as
// TestReadBoundary_RLS_Enforcement). Gated: skipped unless POSTGRES_TEST_DSN
// is set and -short is not active.
const subStaff = "SSSSSSSSSSSSSSSSSSSS"

func TestStaffAppointmentsReadBoundary_WildcardSeesEverything(t *testing.T) {
	dsn := skipIfNoPostgresRLS(t)
	ctx := context.Background()

	owner := poolInSchema(t, dsn, "")
	defer owner.Close()

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}

	exec("DROP SCHEMA IF EXISTS " + clinicRLSTestSchema + " CASCADE")
	exec("CREATE SCHEMA " + clinicRLSTestSchema)
	t.Cleanup(func() {
		_, _ = owner.Exec(ctx, "DROP SCHEMA IF EXISTS "+clinicRLSTestSchema+" CASCADE")
		_, _ = owner.Exec(ctx, "DROP OWNED BY "+clinicRLSTestRole+" CASCADE")
		_, _ = owner.Exec(ctx, "DROP ROLE IF EXISTS "+clinicRLSTestRole)
	})

	for _, stmt := range adapter.BuildGrantTableDDL() {
		exec(stmt)
	}
	body := []adapter.ColumnDef{
		{Name: "entity_key", Type: "text"},
		{Name: "starts_at", Type: "text"},
		{Name: "ends_at", Type: "text"},
		{Name: "reason", Type: "text"},
		{Name: "status", Type: "text"},
		{Name: "status_note", Type: "text"},
		{Name: "patient_key", Type: "text"},
		{Name: "patient_name", Type: "text"},
		{Name: "provider_key", Type: "text"},
		{Name: "provider_name", Type: "text"},
		{Name: "provider_specialty", Type: "text"},
		{Name: "reminder_sent_at", Type: "text"},
		{Name: "follow_up_reminder_sent_at", Type: "text"},
		{Name: "documented_at", Type: "text"},
		{Name: "follow_up_requested", Type: "boolean"},
		{Name: "follow_up_date", Type: "text"},
	}
	ddl, err := adapter.BuildProtectedTableDDL("read_clinic_appointments", []string{"appointment_id"}, body)
	if err != nil {
		t.Fatalf("build protected DDL: %v", err)
	}
	for _, stmt := range ddl {
		exec(stmt)
	}

	_, _ = owner.Exec(ctx, "DROP OWNED BY "+clinicRLSTestRole+" CASCADE")
	_, _ = owner.Exec(ctx, "DROP ROLE IF EXISTS "+clinicRLSTestRole)
	exec("CREATE ROLE " + clinicRLSTestRole + " NOSUPERUSER NOLOGIN")
	exec("GRANT USAGE ON SCHEMA " + clinicRLSTestSchema + " TO " + clinicRLSTestRole)
	exec("GRANT SELECT ON " + clinicRLSTestSchema + ".read_clinic_appointments TO " + clinicRLSTestRole)
	exec("GRANT SELECT ON " + clinicRLSTestSchema + ".actor_read_grants TO " + clinicRLSTestRole)

	// Seed: patient A's appointment + patient B's appointment (disjoint
	// anchors, no relation to the staff actor); A's self-grant only (B has NO
	// grant at all, proving the wildcard — not a second self-grant — is what
	// lets staff see B's row too). The staff actor holds ONLY the wildcard
	// grant, never a self- or per-patient grant.
	exec(`INSERT INTO read_clinic_appointments (appointment_id, entity_key, starts_at, status, patient_key, patient_name, authz_anchors, projection_seq)
	      VALUES ('appt-A', 'vtx.appointment.appt-A', '2026-07-01T15:00:00Z', 'scheduled', 'vtx.patient.`+subPatientA+`', 'Alice Rivera', $1, 1)`, []string{subPatientA})
	exec(`INSERT INTO read_clinic_appointments (appointment_id, entity_key, starts_at, status, patient_key, patient_name, authz_anchors, projection_seq)
	      VALUES ('appt-B', 'vtx.appointment.appt-B', '2026-07-02T15:00:00Z', 'scheduled', 'vtx.patient.`+subPatientB+`', 'Bob Nguyen', $1, 1)`, []string{subPatientB})
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subPatientA)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $2, 'cap-read.root', 1, false)`, subStaff, adapter.WildcardAnchor)

	reader := poolInSchema(t, dsn, clinicRLSTestRole)
	defer reader.Close()

	t.Setenv("CLINIC_APP_DEV_AUTH", "1")
	authn, signer, err := setupReadAuth(discardLogger(), true, nil)
	if err != nil {
		t.Fatalf("setupReadAuth: %v", err)
	}
	s := &server{logger: discardLogger(), natsTimeout: testTimeout, pgPool: reader, authn: authn, devSigner: signer}

	mint := func(sub string) string {
		t.Helper()
		tok, _, err := signer.mint(sub)
		if err != nil {
			t.Fatalf("mint %s: %v", sub, err)
		}
		return tok
	}

	get := func(t *testing.T, authz string) (int, []protectedAppointmentRow) {
		t.Helper()
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/staff/appointments", nil)
		if authz != "" {
			r.Header.Set("Authorization", authz)
		}
		s.handleStaffAppointments(rec, r)
		var resp struct {
			Appointments []protectedAppointmentRow `json:"appointments"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp.Appointments
	}

	t.Run("staff sees every patient's appointment via the wildcard grant", func(t *testing.T) {
		code, rows := get(t, "Bearer "+mint(subStaff))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 2 {
			t.Fatalf("staff must see BOTH appt-A and appt-B, got %+v", rows)
		}
	})

	t.Run("an ordinary patient (self-grant only, no wildcard) still sees only their own row", func(t *testing.T) {
		code, rows := get(t, "Bearer "+mint(subPatientA))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0].EntityKey != "vtx.appointment.appt-A" {
			t.Fatalf("A must see exactly appt-A even on the staff endpoint, got %+v", rows)
		}
	})

	t.Run("revoked wildcard grant hides everything again", func(t *testing.T) {
		exec("UPDATE actor_read_grants SET is_deleted = true WHERE actor_id = $1 AND anchor_id = '*'", subStaff)
		defer exec("UPDATE actor_read_grants SET is_deleted = false WHERE actor_id = $1 AND anchor_id = '*'", subStaff)
		code, rows := get(t, "Bearer "+mint(subStaff))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 0 {
			t.Fatalf("a revoked wildcard grant must hide every row, got %+v", rows)
		}
	})

	t.Run("unauthenticated is 401", func(t *testing.T) {
		if code, _ := get(t, ""); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})
}
