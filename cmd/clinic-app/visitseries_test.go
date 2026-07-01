package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asolgan/lattice/internal/refractor/adapter"
)

// The D1.5 headline proof: the authenticated read boundary enforces RLS on the
// real protected visitSeriesRead Postgres model (mirroring
// TestReadBoundary_RLS_Enforcement / TestStaffAppointmentsReadBoundary_
// WildcardSeesEverything). It provisions the table + policy with the SAME
// refractor helpers a live activation uses, seeds two patients' series rows +
// self-grants, and drives handleMyVisitSeries / handleStaffVisitSeries through
// httptest with minted JWTs.
//
// Gated: skipped unless POSTGRES_TEST_DSN is set and -short is not active.

var visitSeriesColumns = []adapter.ColumnDef{
	{Name: "entity_key", Type: "text"},
	{Name: "patient_key", Type: "text"},
	{Name: "patient_name", Type: "text"},
	{Name: "provider_key", Type: "text"},
	{Name: "provider_name", Type: "text"},
	{Name: "provider_specialty", Type: "text"},
	{Name: "interval_days", Type: "integer"},
	{Name: "next_due_at", Type: "text"},
	{Name: "occurrence_count", Type: "integer"},
	{Name: "active", Type: "boolean"},
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

	exec(`INSERT INTO read_visit_series (series_id, entity_key, patient_key, patient_name, interval_days, next_due_at, occurrence_count, active, authz_anchors, projection_seq)
	      VALUES ('series-A', 'vtx.visitseries.A', 'vtx.patient.`+subPatientA+`', 'Alice Rivera', 30, '2026-08-01T09:00:00Z', 2, true, $1, 1)`, []string{subPatientA})
	exec(`INSERT INTO read_visit_series (series_id, entity_key, patient_key, patient_name, interval_days, next_due_at, occurrence_count, active, authz_anchors, projection_seq)
	      VALUES ('series-B', 'vtx.visitseries.B', 'vtx.patient.`+subPatientB+`', 'Bob Nguyen', 7, '2026-07-15T09:00:00Z', 0, true, $1, 1)`, []string{subPatientB})
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subPatientA)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subPatientB)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $2, 'cap-read.root', 1, false)`, subStaff, adapter.WildcardAnchor)

	reader := poolInSchema(t, dsn, clinicRLSTestRole)
	defer reader.Close()

	t.Setenv("CLINIC_APP_DEV_AUTH", "1")
	authn, signer, err := setupReadAuth(discardLogger(), true)
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

	getMy := func(t *testing.T, authz string) (int, []protectedVisitSeriesRow) {
		t.Helper()
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/my-visit-series", nil)
		if authz != "" {
			r.Header.Set("Authorization", authz)
		}
		s.handleMyVisitSeries(rec, r)
		var resp struct {
			Series []protectedVisitSeriesRow `json:"series"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp.Series
	}

	getStaff := func(t *testing.T, authz string) (int, []protectedVisitSeriesRow) {
		t.Helper()
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/staff/visit-series", nil)
		if authz != "" {
			r.Header.Set("Authorization", authz)
		}
		s.handleStaffVisitSeries(rec, r)
		var resp struct {
			Series []protectedVisitSeriesRow `json:"series"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp.Series
	}

	t.Run("A sees only A's series", func(t *testing.T) {
		code, rows := getMy(t, "Bearer "+mint(subPatientA))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0].EntityKey != "vtx.visitseries.A" {
			t.Fatalf("A must see exactly series-A, got %+v", rows)
		}
	})

	t.Run("unauthenticated is 401", func(t *testing.T) {
		if code, _ := getMy(t, ""); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("revoked grant hides the row", func(t *testing.T) {
		exec("UPDATE actor_read_grants SET is_deleted = true WHERE actor_id = $1", subPatientA)
		defer exec("UPDATE actor_read_grants SET is_deleted = false WHERE actor_id = $1", subPatientA)
		code, rows := getMy(t, "Bearer "+mint(subPatientA))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 0 {
			t.Fatalf("a revoked grant must hide the row, got %+v", rows)
		}
	})

	t.Run("staff sees every patient's series via the wildcard grant", func(t *testing.T) {
		code, rows := getStaff(t, "Bearer "+mint(subStaff))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 2 {
			t.Fatalf("staff must see BOTH series-A and series-B, got %+v", rows)
		}
	})

	t.Run("an ordinary patient (self-grant only, no wildcard) still sees only their own row on the staff endpoint", func(t *testing.T) {
		code, rows := getStaff(t, "Bearer "+mint(subPatientB))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0].EntityKey != "vtx.visitseries.B" {
			t.Fatalf("B must see exactly series-B even on the staff endpoint, got %+v", rows)
		}
	})

	t.Run("forged token is 401", func(t *testing.T) {
		if code, _ := getMy(t, "Bearer not.a.jwt"); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})
}
