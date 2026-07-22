package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// The D1.5 headline proof: a WildcardAnchor grant (adapter.WildcardAnchor)
// lets an actor read the ENTIRE clinicPatientsRead protected table — the real
// Postgres RLS enforcement of the policy's wildcard OR-clause
// (internal/refractor/adapter/rls.go), driven through handleStaffPatients
// exactly like a live clinic-domain deployment would. Unlike
// clinicAppointmentsRead/providerAppointmentsRead there is no self-anchor
// here: every seeded row carries an EMPTY authz_anchors set, so an ordinary
// actor (no wildcard grant at all) must see NOTHING — proving the roster has
// no accidental public-read fallback.
//
// Enforcement is REAL (non-superuser reader role, same fixture discipline as
// TestReadBoundary_RLS_Enforcement / TestStaffAppointmentsReadBoundary_
// WildcardSeesEverything). Gated: skipped unless POSTGRES_TEST_DSN is set and
// -short is not active.

func TestStaffPatientsReadBoundary_WildcardSeesEverything(t *testing.T) {
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
	ddl, err := adapter.BuildProtectedTableDDL("read_clinic_patients", []string{"patient_id"}, []adapter.ColumnDef{
		{Name: "entity_key", Type: "text"},
		{Name: "patient_key", Type: "text"},
		{Name: "name", Type: "text"},
		{Name: "identity_key", Type: "text"},
		{Name: "email", Type: "text"},
		{Name: "phone", Type: "text"},
	})
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
	exec("GRANT SELECT ON " + clinicRLSTestSchema + ".read_clinic_patients TO " + clinicRLSTestRole)
	exec("GRANT SELECT ON " + clinicRLSTestSchema + ".actor_read_grants TO " + clinicRLSTestRole)

	// Seed two patients with an EMPTY authz_anchors set each (the roster has no
	// self-anchor) plus a self-grant for patient A only, proving A's own
	// cap-read self-grant does NOT unlock the roster row (only the wildcard
	// does — the row itself carries no anchor a self-grant could ever match).
	exec(`INSERT INTO read_clinic_patients (patient_id, entity_key, patient_key, name, authz_anchors, projection_seq)
	      VALUES ('pat-A', 'vtx.patient.`+subPatientA+`', 'vtx.patient.`+subPatientA+`', 'Alice Rivera', '{}', 1)`)
	exec(`INSERT INTO read_clinic_patients (patient_id, entity_key, patient_key, name, authz_anchors, projection_seq)
	      VALUES ('pat-B', 'vtx.patient.`+subPatientB+`', 'vtx.patient.`+subPatientB+`', 'Bob Nguyen', '{}', 1)`)
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

	getPath := func(t *testing.T, path, authz string) (int, []protectedPatientRow) {
		t.Helper()
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if authz != "" {
			r.Header.Set("Authorization", authz)
		}
		s.handleStaffPatients(rec, r)
		var resp struct {
			Patients []protectedPatientRow `json:"patients"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp.Patients
	}
	get := func(t *testing.T, authz string) (int, []protectedPatientRow) {
		t.Helper()
		return getPath(t, "/api/staff/patients", authz)
	}

	t.Run("staff sees every patient via the wildcard grant", func(t *testing.T) {
		code, rows := get(t, "Bearer "+mint(subStaff))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 2 {
			t.Fatalf("staff must see BOTH patients, got %+v", rows)
		}
	})

	t.Run("staff filters by name via ?q= — case-insensitive substring", func(t *testing.T) {
		code, rows := getPath(t, "/api/staff/patients?q=riv", "Bearer "+mint(subStaff))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0].Name != "Alice Rivera" {
			t.Fatalf("q=riv must match only Alice Rivera, got %+v", rows)
		}
	})

	t.Run("?q= still enforces RLS — no wildcard grant sees nothing even on a matching name", func(t *testing.T) {
		code, rows := getPath(t, "/api/staff/patients?q=riv", "Bearer "+mint(subPatientA))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 0 {
			t.Fatalf("a non-wildcard actor must see no roster rows even under a name filter, got %+v", rows)
		}
	})

	t.Run("an ordinary patient (self-grant only, no wildcard) sees nothing — no self-anchor exists on the roster", func(t *testing.T) {
		code, rows := get(t, "Bearer "+mint(subPatientA))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 0 {
			t.Fatalf("a non-wildcard actor must see no roster rows, got %+v", rows)
		}
	})

	t.Run("unauthenticated is 401", func(t *testing.T) {
		if code, _ := get(t, ""); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
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
}
