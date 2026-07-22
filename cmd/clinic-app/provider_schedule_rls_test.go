package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// The D1.5 Increment 2 headline proof: the authenticated PROVIDER read boundary
// enforces RLS on the real protected read_provider_appointments model. It
// provisions the table + policy with the SAME refractor helpers a live
// activation uses (BuildProtectedTableDDL / BuildGrantTableDDL), seeds two
// providers' appointments + their self-grants, and drives
// handleMyProviderSchedule through httptest with minted JWTs.
//
// Enforcement is REAL: the reader runs as a NON-superuser role (RLS is bypassed
// by superusers/BYPASSRLS). Shares the helpers (skipIfNoPostgresRLS /
// poolInSchema / clinicRLSTestSchema / clinicRLSTestRole / discardLogger /
// testTimeout) with the patient RLS proof in appointments_rls_test.go.

const (
	subProviderSam = "SSSSSSSSSSSSSSSSSSSS"
	subProviderPat = "PPPPPPPPPPPPPPPPPPPP"
)

func TestProviderScheduleReadBoundary_RLS_Enforcement(t *testing.T) {
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
	ddl, err := adapter.BuildProtectedTableDDL("read_provider_appointments", []string{"appointment_id"}, body)
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
	exec("GRANT SELECT ON " + clinicRLSTestSchema + ".read_provider_appointments TO " + clinicRLSTestRole)
	exec("GRANT SELECT ON " + clinicRLSTestSchema + ".actor_read_grants TO " + clinicRLSTestRole)

	// Seed: Dr. Sam's appointment (anchor drSam) + Dr. Pat's appointment (anchor
	// drPat); self-grants for both.
	exec(`INSERT INTO read_provider_appointments (appointment_id, entity_key, starts_at, status, provider_key, provider_name, authz_anchors, projection_seq)
	      VALUES ('appt-sam', 'vtx.appointment.appt-sam', '2026-07-01T15:00:00Z', 'scheduled', 'vtx.provider.`+subProviderSam+`', 'Dr. Sam Okafor', $1, 1)`, []string{subProviderSam})
	exec(`INSERT INTO read_provider_appointments (appointment_id, entity_key, starts_at, status, provider_key, provider_name, authz_anchors, projection_seq)
	      VALUES ('appt-pat', 'vtx.appointment.appt-pat', '2026-07-02T15:00:00Z', 'scheduled', 'vtx.provider.`+subProviderPat+`', 'Dr. Pat Nguyen', $1, 1)`, []string{subProviderPat})
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subProviderSam)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subProviderPat)

	reader := poolInSchema(t, dsn, clinicRLSTestRole)
	defer reader.Close()

	t.Run("reader role is not a superuser", func(t *testing.T) {
		var isSuper string
		if err := reader.QueryRow(ctx, "SELECT current_setting('is_superuser')").Scan(&isSuper); err != nil {
			t.Fatalf("is_superuser: %v", err)
		}
		if isSuper != "off" {
			t.Fatalf("reader must be non-superuser (else RLS is bypassed), got is_superuser=%s", isSuper)
		}
	})

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
		r := httptest.NewRequest(http.MethodGet, "/api/my-schedule", nil)
		if authz != "" {
			r.Header.Set("Authorization", authz)
		}
		s.handleMyProviderSchedule(rec, r)
		var resp struct {
			Appointments []protectedAppointmentRow `json:"appointments"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp.Appointments
	}

	t.Run("Sam sees only Sam's appointment", func(t *testing.T) {
		code, rows := get(t, "Bearer "+mint(subProviderSam))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0].EntityKey != "vtx.appointment.appt-sam" {
			t.Fatalf("Sam must see exactly appt-sam, got %+v", rows)
		}
	})

	t.Run("Pat sees only Pat's appointment", func(t *testing.T) {
		code, rows := get(t, "Bearer "+mint(subProviderPat))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0].EntityKey != "vtx.appointment.appt-pat" {
			t.Fatalf("Pat must see exactly appt-pat, got %+v", rows)
		}
	})

	t.Run("unauthenticated is 401", func(t *testing.T) {
		if code, _ := get(t, ""); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("forged token is 401", func(t *testing.T) {
		if code, _ := get(t, "Bearer not.a.jwt"); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("revoked grant hides the row", func(t *testing.T) {
		exec("UPDATE actor_read_grants SET is_deleted = true WHERE actor_id = $1", subProviderSam)
		defer exec("UPDATE actor_read_grants SET is_deleted = false WHERE actor_id = $1", subProviderSam)
		code, rows := get(t, "Bearer "+mint(subProviderSam))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 0 {
			t.Fatalf("a revoked grant must hide the row, got %+v", rows)
		}
	})
}
