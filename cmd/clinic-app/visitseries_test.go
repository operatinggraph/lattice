package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// The D1.5 headline proof: the authenticated read boundary enforces RLS on the
// real protected visitSeriesRead Postgres model (mirroring
// TestReadBoundary_RLS_Enforcement / TestStaffAppointmentsReadBoundary_
// WildcardSeesEverything). It provisions the table + policy with the SAME
// refractor helpers a live activation uses, seeds two patients' series rows +
// self-grants, and drives handleMyVisitSeries / handleStaffVisitSeries through
// the real session middleware with signed session cookies.
//
// Gated: skipped unless POSTGRES_TEST_DSN is set and -short is not active.

var visitSeriesColumns = []adapter.ColumnDef{
	{Name: "entity_key", Type: "text"},
	{Name: "patient_key", Type: "text"},
	{Name: "patient_name", Type: "text"},
	{Name: "unlinked_patient_name", Type: "text"},
	{Name: "provider_key", Type: "text"},
	{Name: "provider_name", Type: "text"},
	{Name: "provider_specialty", Type: "text"},
	{Name: "interval_days", Type: "integer"},
	{Name: "next_due_at", Type: "text"},
	{Name: "occurrence_count", Type: "integer"},
	{Name: "series_status", Type: "text"},
}

func TestVisitSeriesReadBoundary_RLS_Enforcement(t *testing.T) {
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
	ddl, err := adapter.BuildProtectedTableDDL("read_visit_series", []string{"series_id"}, visitSeriesColumns)
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
	exec("GRANT SELECT ON " + clinicRLSTestSchema + ".read_visit_series TO " + clinicRLSTestRole)
	exec("GRANT SELECT ON " + clinicRLSTestSchema + ".actor_read_grants TO " + clinicRLSTestRole)

	exec(`INSERT INTO read_visit_series (series_id, entity_key, patient_key, patient_name, interval_days, next_due_at, occurrence_count, series_status, authz_anchors, projection_seq)
	      VALUES ('series-A', 'vtx.visitseries.A', 'vtx.patient.`+subPatientA+`', 'Alice Rivera', 30, '2026-08-01T09:00:00Z', 2, 'active', $1, 1)`, []string{subPatientA})
	exec(`INSERT INTO read_visit_series (series_id, entity_key, patient_key, patient_name, interval_days, next_due_at, occurrence_count, series_status, authz_anchors, projection_seq)
	      VALUES ('series-B', 'vtx.visitseries.B', 'vtx.patient.`+subPatientB+`', 'Bob Nguyen', 7, '2026-07-15T09:00:00Z', 0, 'active', $1, 1)`, []string{subPatientB})
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subPatientA)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subPatientB)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $2, 'cap-read.root', 1, false)`, subStaff, adapter.WildcardAnchor)

	reader := poolInSchema(t, dsn, clinicRLSTestRole)
	defer reader.Close()

	s, cookieFor := devSessionServer(t, func(s *server) { s.pgPool = reader })

	getMy := func(t *testing.T, c *http.Cookie) (int, []protectedVisitSeriesRow) {
		t.Helper()
		rec := sessionGET(s, s.handleMyVisitSeries, "/api/my-visit-series", c)
		var resp struct {
			Series []protectedVisitSeriesRow `json:"series"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp.Series
	}

	getStaff := func(t *testing.T, c *http.Cookie) (int, []protectedVisitSeriesRow) {
		t.Helper()
		rec := sessionGET(s, s.handleStaffVisitSeries, "/api/staff/visit-series", c)
		var resp struct {
			Series []protectedVisitSeriesRow `json:"series"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp.Series
	}

	t.Run("A sees only A's series", func(t *testing.T) {
		code, rows := getMy(t, cookieFor(subPatientA))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0].EntityKey != "vtx.visitseries.A" {
			t.Fatalf("A must see exactly series-A, got %+v", rows)
		}
	})

	t.Run("unauthenticated is 401", func(t *testing.T) {
		if code, _ := getMy(t, nil); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("revoked grant hides the row", func(t *testing.T) {
		exec("UPDATE actor_read_grants SET is_deleted = true WHERE actor_id = $1", subPatientA)
		defer exec("UPDATE actor_read_grants SET is_deleted = false WHERE actor_id = $1", subPatientA)
		code, rows := getMy(t, cookieFor(subPatientA))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 0 {
			t.Fatalf("a revoked grant must hide the row, got %+v", rows)
		}
	})

	t.Run("staff sees every patient's series via the wildcard grant", func(t *testing.T) {
		code, rows := getStaff(t, cookieFor(subStaff))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 2 {
			t.Fatalf("staff must see BOTH series-A and series-B, got %+v", rows)
		}
	})

	t.Run("an ordinary patient (self-grant only, no wildcard) still sees only their own row on the staff endpoint", func(t *testing.T) {
		code, rows := getStaff(t, cookieFor(subPatientB))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0].EntityKey != "vtx.visitseries.B" {
			t.Fatalf("B must see exactly series-B even on the staff endpoint, got %+v", rows)
		}
	})

	t.Run("forged cookie is 401", func(t *testing.T) {
		forged := &http.Cookie{Name: s.session.CookieName(), Value: "not.a.jwt"}
		if code, _ := getMy(t, forged); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})
}
