package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// The D1.5-class proof for GET /api/ledger (cmd/clinic-app/ledger.go
// PatientVisibleToActor): live-verified 2026-07-10 that the endpoint served
// ANY patient's full billing history to an unauthenticated caller — the same
// class of leak D1.5 already closed on handleAppointments' old `?patient=`
// vector. The ledger has no protected read model of its own, so the fix
// reuses the already-provisioned clinicPatientsRead protected table
// (patients.go) as the ledger's authorization gate: a roster row is visible
// ONLY to an actor holding the reserved WildcardAnchor grant (staff), so
// gating on it closes the vector without standing up new schema.
//
// Enforcement is REAL Postgres RLS (non-superuser reader role), the same
// fixture discipline as staff_patients_rls_test.go. Gated: skipped unless
// POSTGRES_TEST_DSN is set and -short is not active.
func TestLedgerReadBoundary_WildcardRequiredNonWildcardDenied(t *testing.T) {
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
		{Name: "unlinked_name", Type: "text"},
		{Name: "identity_key", Type: "text"},
		{Name: "email", Type: "text"},
		{Name: "phone", Type: "text"},
		{Name: "no_show_count", Type: "integer"},
		{Name: "last_no_show_at", Type: "text"},
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

	patientKey := "vtx.patient." + subPatientA
	exec(`INSERT INTO read_clinic_patients (patient_id, entity_key, patient_key, name, authz_anchors, projection_seq)
	      VALUES ('pat-A', $1, $1, 'Alice Rivera', '{}', 1)`, patientKey)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $2, 'cap-read.root', 1, false)`, subStaff, adapter.WildcardAnchor)

	reader := poolInSchema(t, dsn, clinicRLSTestRole)
	defer reader.Close()

	s, cookieFor := devSessionServer(t, func(s *server) { s.pgPool = reader })

	// getLedger drives handleLedger through the real session middleware exactly
	// as the FE would. It never reaches s.requireConn (no NATS conn is wired
	// here) unless the session + visibility gate passes — a 401/403 proves the
	// gate rejected the request before any lens read was attempted.
	getLedger := func(t *testing.T, c *http.Cookie) int {
		t.Helper()
		return sessionGET(s, s.handleLedger, "/api/ledger?patientKey="+patientKey, c).Code
	}

	t.Run("unauthenticated is 401 — the confirmed leak this closes", func(t *testing.T) {
		if code := getLedger(t, nil); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("an authenticated actor with no wildcard grant is 403", func(t *testing.T) {
		if code := getLedger(t, cookieFor(subPatientA)); code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (no wildcard grant, no self-anchor on the roster)", code)
		}
	})

	t.Run("the staff wildcard actor clears the gate (past auth, into the NATS lens read)", func(t *testing.T) {
		// No NATS conn is wired in this fixture, so a passing gate surfaces as
		// 502 (requireConn) rather than 200 — the point is proving it is no
		// longer 401/403, i.e. the wildcard grant was honored.
		code := getLedger(t, cookieFor(subStaff))
		if code == http.StatusUnauthorized || code == http.StatusForbidden {
			t.Fatalf("status = %d, want past the auth gate (502, no NATS conn wired) — the wildcard grant should have cleared it", code)
		}
	})

	t.Run("revoked wildcard grant denies again", func(t *testing.T) {
		exec("UPDATE actor_read_grants SET is_deleted = true WHERE actor_id = $1 AND anchor_id = $2", subStaff, adapter.WildcardAnchor)
		defer exec("UPDATE actor_read_grants SET is_deleted = false WHERE actor_id = $1 AND anchor_id = $2", subStaff, adapter.WildcardAnchor)
		if code := getLedger(t, cookieFor(subStaff)); code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 once the wildcard grant is revoked", code)
		}
	})
}

// TestArrearsReadBoundary_EmptyVisibleSetVsWildcard is the D1.5-class proof
// for GET /api/staff/arrears (handleStaffArrears): the endpoint's own
// confinement is the roster queryPatients already serves, so it inherits
// exactly clinicPatientsRead's RLS shape rather than standing up a second
// protected model. Unlike handleLedger (one named patientKey, 403 when
// invisible), an actor here names no patient at all — a non-wildcard actor's
// visible roster is simply EMPTY, so the correct response is 200 with an
// empty "arrears" array (nothing to group), not a 403: there is no
// forbidden resource being named, just nobody this actor may see.
func TestArrearsReadBoundary_EmptyVisibleSetVsWildcard(t *testing.T) {
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
		{Name: "unlinked_name", Type: "text"},
		{Name: "identity_key", Type: "text"},
		{Name: "email", Type: "text"},
		{Name: "phone", Type: "text"},
		{Name: "no_show_count", Type: "integer"},
		{Name: "last_no_show_at", Type: "text"},
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

	patientKey := "vtx.patient." + subPatientA
	exec(`INSERT INTO read_clinic_patients (patient_id, entity_key, patient_key, name, authz_anchors, projection_seq)
	      VALUES ('pat-A', $1, $1, 'Alice Rivera', '{}', 1)`, patientKey)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $2, 'cap-read.root', 1, false)`, subStaff, adapter.WildcardAnchor)

	reader := poolInSchema(t, dsn, clinicRLSTestRole)
	defer reader.Close()

	s, cookieFor := devSessionServer(t, func(s *server) { s.pgPool = reader })

	getArrears := func(t *testing.T, c *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		return sessionGET(s, s.handleStaffArrears, "/api/staff/arrears", c)
	}

	t.Run("unauthenticated is 401", func(t *testing.T) {
		if code := getArrears(t, nil).Code; code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("an authenticated actor with no wildcard grant sees an empty roster, 200 with no arrears rows", func(t *testing.T) {
		rec := getArrears(t, cookieFor(subPatientA))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (empty visible set is not an error)", rec.Code)
		}
		var body struct {
			Arrears []arrearsRow `json:"arrears"`
			Count   int          `json:"count"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v (%s)", err, rec.Body.String())
		}
		if len(body.Arrears) != 0 || body.Count != 0 {
			t.Fatalf("body = %+v, want an empty arrears array and count 0", body)
		}
	})

	t.Run("the staff wildcard actor clears the gate (past auth, into the NATS lens read)", func(t *testing.T) {
		// The wildcard actor's visible roster is non-empty (Alice Rivera's
		// row), so the handler proceeds past the empty-set short-circuit into
		// s.requireConn — no NATS conn is wired in this fixture, so a passing
		// gate surfaces as 502 rather than 200/401.
		code := getArrears(t, cookieFor(subStaff)).Code
		if code == http.StatusUnauthorized || code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502 (past auth, into the NATS lens read with no conn wired)", code)
		}
	})

	t.Run("revoked wildcard grant returns to the empty-roster 200", func(t *testing.T) {
		exec("UPDATE actor_read_grants SET is_deleted = true WHERE actor_id = $1 AND anchor_id = $2", subStaff, adapter.WildcardAnchor)
		defer exec("UPDATE actor_read_grants SET is_deleted = false WHERE actor_id = $1 AND anchor_id = $2", subStaff, adapter.WildcardAnchor)
		rec := getArrears(t, cookieFor(subStaff))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (no wildcard grant now, empty roster again)", rec.Code)
		}
	})
}
