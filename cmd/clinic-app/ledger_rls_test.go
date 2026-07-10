package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asolgan/lattice/internal/refractor/adapter"
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

	patientKey := "vtx.patient." + subPatientA
	exec(`INSERT INTO read_clinic_patients (patient_id, entity_key, patient_key, name, authz_anchors, projection_seq)
	      VALUES ('pat-A', $1, $1, 'Alice Rivera', '{}', 1)`, patientKey)
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

	// getLedger drives handleLedger through httptest exactly as the FE would.
	// It never reaches s.requireConn (no NATS conn is wired here) unless the
	// auth + visibility gate passes — a 401/403 proves the gate rejected the
	// request before any lens read was attempted.
	getLedger := func(t *testing.T, authz string) int {
		t.Helper()
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/ledger?patientKey="+patientKey, nil)
		if authz != "" {
			r.Header.Set("Authorization", authz)
		}
		s.handleLedger(rec, r)
		return rec.Code
	}

	t.Run("unauthenticated is 401 — the confirmed leak this closes", func(t *testing.T) {
		if code := getLedger(t, ""); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("an authenticated actor with no wildcard grant is 403", func(t *testing.T) {
		if code := getLedger(t, "Bearer "+mint(subPatientA)); code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (no wildcard grant, no self-anchor on the roster)", code)
		}
	})

	t.Run("the staff wildcard actor clears the gate (past auth, into the NATS lens read)", func(t *testing.T) {
		// No NATS conn is wired in this fixture, so a passing gate surfaces as
		// 502 (requireConn) rather than 200 — the point is proving it is no
		// longer 401/403, i.e. the wildcard grant was honored.
		code := getLedger(t, "Bearer "+mint(subStaff))
		if code == http.StatusUnauthorized || code == http.StatusForbidden {
			t.Fatalf("status = %d, want past the auth gate (502, no NATS conn wired) — the wildcard grant should have cleared it", code)
		}
	})

	t.Run("revoked wildcard grant denies again", func(t *testing.T) {
		exec("UPDATE actor_read_grants SET is_deleted = true WHERE actor_id = $1 AND anchor_id = $2", subStaff, adapter.WildcardAnchor)
		defer exec("UPDATE actor_read_grants SET is_deleted = false WHERE actor_id = $1 AND anchor_id = $2", subStaff, adapter.WildcardAnchor)
		if code := getLedger(t, "Bearer "+mint(subStaff)); code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 once the wildcard grant is revoked", code)
		}
	})
}
