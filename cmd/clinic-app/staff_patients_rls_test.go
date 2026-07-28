package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// The D1.5 headline proof: a WildcardAnchor grant (adapter.WildcardAnchor)
// lets an actor read the ENTIRE clinicPatientsRead protected table — the real
// Postgres RLS enforcement of the policy's wildcard OR-clause
// (internal/refractor/adapter/rls.go), driven through handleStaffPatients
// exactly like a live clinic-domain deployment would. Each seeded row also
// carries its OWN patient's NanoID as its sole authz_anchor (mirroring
// clinicPatientsReadSpec's `[nanoIdFromKey(p.key)] AS authz_anchors`), so an
// ordinary actor holding the identifiedBy-bridged self-grant
// (patientIdentityReadGrants) sees exactly their own row and no other —
// proving both halves at once: the roster has no accidental public-read
// fallback, and the self-anchor bridge that lets a patient find their own
// record actually works.
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

	// Seed two patients, each carrying its OWN NanoID as its sole authz_anchor
	// (the real clinicPatientsReadSpec shape), plus the identifiedBy-bridged
	// self-grant for patient A only (patientIdentityReadGrants: actor_id = the
	// signed-in identity, anchor_id = the patient's own key — which equals
	// subPatientA in this fixture) — proving A's self-grant unlocks EXACTLY
	// A's own row, never B's, and that the wildcard is still the only way to
	// see the whole roster.
	exec(`INSERT INTO read_clinic_patients (patient_id, entity_key, patient_key, name, authz_anchors, projection_seq)
	      VALUES ('pat-A', 'vtx.patient.`+subPatientA+`', 'vtx.patient.`+subPatientA+`', 'Alice Rivera', '{`+subPatientA+`}', 1)`)
	exec(`INSERT INTO read_clinic_patients (patient_id, entity_key, patient_key, name, authz_anchors, projection_seq)
	      VALUES ('pat-B', 'vtx.patient.`+subPatientB+`', 'vtx.patient.`+subPatientB+`', 'Bob Nguyen', '{`+subPatientB+`}', 1)`)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subPatientA)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $2, 'cap-read.root', 1, false)`, subStaff, adapter.WildcardAnchor)

	reader := poolInSchema(t, dsn, clinicRLSTestRole)
	defer reader.Close()

	s, cookieFor := devSessionServer(t, func(s *server) { s.pgPool = reader })

	getPath := func(t *testing.T, path string, c *http.Cookie) (int, []protectedPatientRow) {
		t.Helper()
		rec := sessionGET(s, s.handleStaffPatients, path, c)
		var resp struct {
			Patients []protectedPatientRow `json:"patients"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp.Patients
	}
	get := func(t *testing.T, c *http.Cookie) (int, []protectedPatientRow) {
		t.Helper()
		return getPath(t, "/api/staff/patients", c)
	}

	t.Run("staff sees every patient via the wildcard grant", func(t *testing.T) {
		code, rows := get(t, cookieFor(subStaff))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 2 {
			t.Fatalf("staff must see BOTH patients, got %+v", rows)
		}
	})

	t.Run("staff filters by name via ?q= — case-insensitive substring", func(t *testing.T) {
		code, rows := getPath(t, "/api/staff/patients?q=riv", cookieFor(subStaff))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0].Name != "Alice Rivera" {
			t.Fatalf("q=riv must match only Alice Rivera, got %+v", rows)
		}
	})

	t.Run("?q= still enforces RLS — a non-wildcard actor's filtered search never surfaces another patient's row", func(t *testing.T) {
		code, rows := getPath(t, "/api/staff/patients?q=nguy", cookieFor(subPatientA))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 0 {
			t.Fatalf("patient A must never see patient B's row, even under a name filter A can't have typed, got %+v", rows)
		}
	})

	t.Run("?q= matching the caller's own name still returns only their own row", func(t *testing.T) {
		code, rows := getPath(t, "/api/staff/patients?q=riv", cookieFor(subPatientA))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0].Name != "Alice Rivera" {
			t.Fatalf("a self-anchored patient searching their own name must see exactly their own row, got %+v", rows)
		}
	})

	t.Run("an ordinary patient (self-grant, no wildcard) sees exactly their own row via the identifiedBy self-anchor", func(t *testing.T) {
		code, rows := get(t, cookieFor(subPatientA))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0].Name != "Alice Rivera" {
			t.Fatalf("a self-anchored patient must see exactly their own roster row, got %+v", rows)
		}
	})

	t.Run("an ordinary patient with no grant at all sees nothing", func(t *testing.T) {
		code, rows := get(t, cookieFor(subPatientB))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 0 {
			t.Fatalf("patient B holds no read grant on the roster (no self-grant seeded for B), got %+v", rows)
		}
	})

	t.Run("unauthenticated is 401", func(t *testing.T) {
		if code, _ := get(t, nil); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("revoked wildcard grant hides everything again", func(t *testing.T) {
		exec("UPDATE actor_read_grants SET is_deleted = true WHERE actor_id = $1 AND anchor_id = '*'", subStaff)
		defer exec("UPDATE actor_read_grants SET is_deleted = false WHERE actor_id = $1 AND anchor_id = '*'", subStaff)
		code, rows := get(t, cookieFor(subStaff))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 0 {
			t.Fatalf("a revoked wildcard grant must hide every row, got %+v", rows)
		}
	})
}
