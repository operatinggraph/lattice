package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// The Inc D headline proof: the authenticated PROVIDER read boundary enforces
// RLS on the real protected read_clinic_encounters model — the only read path
// back to a documented visit's clinical content. It provisions the table +
// policy with the SAME refractor helpers a live activation uses
// (BuildProtectedTableDDL / BuildGrantTableDDL), seeds two DIFFERENT
// providers' encounters + their self-grants, and drives handleMyEncounters
// through the real session middleware with signed session cookies.
//
// Enforcement is REAL: the reader runs as a NON-superuser role (RLS is
// bypassed by superusers/BYPASSRLS). Shares the helpers
// (skipIfNoPostgresRLS / poolInSchema / clinicRLSTestSchema /
// clinicRLSTestRole / discardLogger / testTimeout) with the patient and
// provider-schedule RLS proofs in appointments_rls_test.go /
// provider_schedule_rls_test.go — same fixture discipline, a different
// protected table.

func TestEncountersReadBoundary_RLS_Enforcement(t *testing.T) {
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
		{Name: "patient_key", Type: "text"},
		{Name: "provider_key", Type: "text"},
		{Name: "documented_at", Type: "text"},
		{Name: "summary", Type: "text"},
		{Name: "assessment", Type: "text"},
		{Name: "plan", Type: "text"},
	}
	ddl, err := adapter.BuildProtectedTableDDL("read_clinic_encounters", []string{"appointment_id"}, body)
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
	exec("GRANT SELECT ON " + clinicRLSTestSchema + ".read_clinic_encounters TO " + clinicRLSTestRole)
	exec("GRANT SELECT ON " + clinicRLSTestSchema + ".actor_read_grants TO " + clinicRLSTestRole)

	// Seed: Dr. Sam's documented visit (anchor drSam) + Dr. Pat's documented
	// visit (anchor drPat) — two DIFFERENT providers, each the treating
	// provider on their own appointment only. Self-grants for both, mirroring
	// clinicProviderReadGrants' self-anchor shape.
	exec(`INSERT INTO read_clinic_encounters (appointment_id, entity_key, patient_key, provider_key, documented_at, summary, assessment, plan, authz_anchors, projection_seq)
	      VALUES ('appt-sam', 'vtx.appointment.appt-sam', 'vtx.patient.PPPPPPPPPPPPPPPPPPP1', 'vtx.provider.`+subProviderSam+`', '2026-07-01T16:00:00Z', 'Sam patient visit summary', 'Sam assessment', 'Sam plan', $1, 1)`, []string{subProviderSam})
	exec(`INSERT INTO read_clinic_encounters (appointment_id, entity_key, patient_key, provider_key, documented_at, summary, assessment, plan, authz_anchors, projection_seq)
	      VALUES ('appt-pat', 'vtx.appointment.appt-pat', 'vtx.patient.PPPPPPPPPPPPPPPPPPP2', 'vtx.provider.`+subProviderPat+`', '2026-07-02T16:00:00Z', 'Pat patient visit summary', 'Pat assessment', 'Pat plan', $1, 1)`, []string{subProviderPat})
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

	s, cookieFor := devSessionServer(t, func(s *server) { s.pgPool = reader })

	get := func(t *testing.T, c *http.Cookie) (int, []protectedEncounterRow) {
		t.Helper()
		rec := sessionGET(s, s.handleMyEncounters, "/api/my-encounters", c)
		var resp struct {
			Encounters []protectedEncounterRow `json:"encounters"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp.Encounters
	}

	// The regression this test would catch: clinicEncountersRead's
	// authz_anchors is provider-scoped in the cypher, but a bug in the query
	// (e.g. a dropped WHERE, or a handler that read the wrong actor) could
	// still let Sam's session see Pat's clinical note — a PHI leak across
	// treating providers.
	t.Run("Sam sees only Sam's note", func(t *testing.T) {
		code, rows := get(t, cookieFor(subProviderSam))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0].AppointmentKey != "vtx.appointment.appt-sam" {
			t.Fatalf("Sam must see exactly appt-sam, got %+v", rows)
		}
		if rows[0].Summary != "Sam patient visit summary" || rows[0].Assessment != "Sam assessment" || rows[0].Plan != "Sam plan" {
			t.Fatalf("Sam's note content did not round-trip, got %+v", rows[0])
		}
	})

	t.Run("Pat sees only Pat's note", func(t *testing.T) {
		code, rows := get(t, cookieFor(subProviderPat))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0].AppointmentKey != "vtx.appointment.appt-pat" {
			t.Fatalf("Pat must see exactly appt-pat, got %+v", rows)
		}
		if rows[0].Summary != "Pat patient visit summary" {
			t.Fatalf("Pat's note content did not round-trip, got %+v", rows[0])
		}
	})

	t.Run("unauthenticated is 401", func(t *testing.T) {
		if code, _ := get(t, nil); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("forged cookie is 401", func(t *testing.T) {
		forged := &http.Cookie{Name: s.session.CookieName(), Value: "not.a.jwt"}
		if code, _ := get(t, forged); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("revoked grant hides the note", func(t *testing.T) {
		exec("UPDATE actor_read_grants SET is_deleted = true WHERE actor_id = $1", subProviderSam)
		defer exec("UPDATE actor_read_grants SET is_deleted = false WHERE actor_id = $1", subProviderSam)
		code, rows := get(t, cookieFor(subProviderSam))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 0 {
			t.Fatalf("a revoked grant must hide the note, got %+v", rows)
		}
	})
}
