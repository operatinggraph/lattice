package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// The D1.5 headline proof: the authenticated read boundary enforces RLS on the
// real protected Postgres read model (mirroring loftspace-app's
// TestReadBoundary_RLS_Enforcement). It provisions the table + policy with the
// SAME refractor helpers a live activation uses (BuildProtectedTableDDL /
// BuildGrantTableDDL), seeds two patients' rows + self-grants, and drives
// handleMyAppointments through the real session middleware with signed
// session cookies.
//
// Enforcement is REAL: the reader runs as a NON-superuser role (RLS is bypassed
// by superusers/BYPASSRLS, so the app role must not be one — design §3.3). The
// whole fixture lives in a dedicated schema dropped at the end, so it is safe to
// point POSTGRES_TEST_DSN at a live database.
//
// Gated: skipped unless POSTGRES_TEST_DSN is set and -short is not active (CI has
// no Postgres; this is a local enforcement proof, like lease-signing's).

const (
	clinicRLSTestSchema = "clinic_rls_test"
	clinicRLSTestRole   = "clinic_rls_test_reader"
	subPatientA         = "AAAAAAAAAAAAAAAAAAAA"
	subPatientB         = "BBBBBBBBBBBBBBBBBBBB"

	// Patient C and the LOGIN that is identifiedBy-bound to it are deliberately
	// DIFFERENT NanoIDs. A and B collapse the two (their session subject is the
	// patient's own id), which is the shape the app minted before it was
	// sign-in-first and the reason a broken identity→patient bridge could stay
	// invisible to a green suite: every assertion still passed while the actual
	// login could see nothing. C is the shape a real session has.
	subPatientC  = "CCCCCCCCCCCCCCCCCCCC"
	subIdentityC = "DDDDDDDDDDDDDDDDDDDD"
)

func skipIfNoPostgresRLS(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping Postgres integration test in short mode")
	}
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("skipping: POSTGRES_TEST_DSN not set")
	}
	return dsn
}

// poolInSchema builds a pool whose every connection sets the test search_path
// (and, when role is non-empty, SET ROLE to the non-superuser reader so RLS is
// actually enforced).
func poolInSchema(t *testing.T, dsn, role string) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		if _, err := c.Exec(ctx, "SET search_path TO "+clinicRLSTestSchema+", public"); err != nil {
			return err
		}
		if role != "" {
			if _, err := c.Exec(ctx, "SET ROLE "+role); err != nil {
				return err
			}
		}
		return nil
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	return pool
}

func TestReadBoundary_RLS_Enforcement(t *testing.T) {
	dsn := skipIfNoPostgresRLS(t)
	ctx := context.Background()

	// Owner pool (superuser) provisions + seeds within an isolated schema.
	owner := poolInSchema(t, dsn, "")
	defer owner.Close()

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}

	// Clean slate + isolated schema (CASCADE drops everything from a prior run).
	exec("DROP SCHEMA IF EXISTS " + clinicRLSTestSchema + " CASCADE")
	exec("CREATE SCHEMA " + clinicRLSTestSchema)
	t.Cleanup(func() {
		_, _ = owner.Exec(ctx, "DROP SCHEMA IF EXISTS "+clinicRLSTestSchema+" CASCADE")
		_, _ = owner.Exec(ctx, "DROP OWNED BY "+clinicRLSTestRole+" CASCADE")
		_, _ = owner.Exec(ctx, "DROP ROLE IF EXISTS "+clinicRLSTestRole)
	})

	// Provision the grant table + the protected table with the real refractor DDL
	// (the policy references actor_read_grants — both unqualified, resolved in the
	// schema via search_path).
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
		{Name: "site_key", Type: "text"},
		{Name: "site_name", Type: "text"},
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

	// The non-superuser reader role (RLS is bypassed by superusers — the app must
	// not be one). USAGE on the schema + SELECT on both tables (the policy subquery
	// reads actor_read_grants).
	// Tolerant pre-clean (the role may not exist on a first run; DROP OWNED errors
	// if the role is absent).
	_, _ = owner.Exec(ctx, "DROP OWNED BY "+clinicRLSTestRole+" CASCADE")
	_, _ = owner.Exec(ctx, "DROP ROLE IF EXISTS "+clinicRLSTestRole)
	exec("CREATE ROLE " + clinicRLSTestRole + " NOSUPERUSER NOLOGIN")
	exec("GRANT USAGE ON SCHEMA " + clinicRLSTestSchema + " TO " + clinicRLSTestRole)
	exec("GRANT SELECT ON " + clinicRLSTestSchema + ".read_clinic_appointments TO " + clinicRLSTestRole)
	exec("GRANT SELECT ON " + clinicRLSTestSchema + ".actor_read_grants TO " + clinicRLSTestRole)

	// Seed: patient A's appointment (anchor A) + patient B's appointment (anchor
	// B); self-grants for both.
	exec(`INSERT INTO read_clinic_appointments (appointment_id, entity_key, starts_at, status, patient_key, patient_name, authz_anchors, projection_seq)
	      VALUES ('appt-A', 'vtx.appointment.appt-A', '2026-07-01T15:00:00Z', 'scheduled', 'vtx.patient.`+subPatientA+`', 'Alice Rivera', $1, 1)`, []string{subPatientA})
	exec(`INSERT INTO read_clinic_appointments (appointment_id, entity_key, starts_at, status, patient_key, patient_name, authz_anchors, projection_seq)
	      VALUES ('appt-B', 'vtx.appointment.appt-B', '2026-07-02T15:00:00Z', 'scheduled', 'vtx.patient.`+subPatientB+`', 'Bob Nguyen', $1, 1)`, []string{subPatientB})
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subPatientA)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subPatientB)

	// Patient C, reachable only through the identity bridge: the row anchors on
	// the PATIENT, while the grant's actor is the bound LOGIN — exactly what
	// patientIdentityReadGrants projects. No self-grant exists for C, so if the
	// bridge row were missing or its actor/anchor columns were transposed, C's
	// session would read nothing.
	exec(`INSERT INTO read_clinic_appointments (appointment_id, entity_key, starts_at, status, patient_key, patient_name, authz_anchors, projection_seq)
	      VALUES ('appt-C', 'vtx.appointment.appt-C', '2026-07-03T15:00:00Z', 'scheduled', 'vtx.patient.`+subPatientC+`', 'Carol Okafor', $1, 1)`, []string{subPatientC})
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $2, 'cap-read.patient.clinic', 1, false)`, subIdentityC, subPatientC)

	// The reader pool runs as the non-superuser role.
	reader := poolInSchema(t, dsn, clinicRLSTestRole)
	defer reader.Close()

	// Defense-in-depth: prove the reader is actually non-superuser — a superuser
	// (or BYPASSRLS) role skips RLS entirely, which would make every "A sees only
	// A" assertion below pass for the wrong reason.
	t.Run("reader role is not a superuser", func(t *testing.T) {
		var isSuper string
		if err := reader.QueryRow(ctx, "SELECT current_setting('is_superuser')").Scan(&isSuper); err != nil {
			t.Fatalf("is_superuser: %v", err)
		}
		if isSuper != "off" {
			t.Fatalf("reader must be non-superuser (else RLS is bypassed), got is_superuser=%s", isSuper)
		}
	})

	// The signed-in app: the demo session posture over the shared dev key.
	s, cookieFor := devSessionServer(t, func(s *server) { s.pgPool = reader })

	get := func(t *testing.T, c *http.Cookie) (int, []protectedAppointmentRow) {
		t.Helper()
		rec := sessionGET(s, s.handleMyAppointments, "/api/my-appointments", c)
		var resp struct {
			Appointments []protectedAppointmentRow `json:"appointments"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp.Appointments
	}

	t.Run("A sees only A", func(t *testing.T) {
		code, rows := get(t, cookieFor(subPatientA))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0].EntityKey != "vtx.appointment.appt-A" {
			t.Fatalf("A must see exactly appt-A, got %+v", rows)
		}
	})

	// The bridge itself: a session whose subject is a LOGIN, not a patient.
	t.Run("a bound login reads its patient's rows and only those", func(t *testing.T) {
		code, rows := get(t, cookieFor(subIdentityC))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0].EntityKey != "vtx.appointment.appt-C" {
			t.Fatalf("the bound login must see exactly appt-C (the identity→patient grant bridge is what makes a real sign-in work), got %+v", rows)
		}
	})

	t.Run("an unbound login sees nothing", func(t *testing.T) {
		// subPatientC is the patient's own id. It anchors the row but is NOT the
		// actor of any grant, so a session presenting it must read nothing —
		// this is what fails if the bridge is ever collapsed back into a
		// patient-as-actor self-grant.
		code, rows := get(t, cookieFor(subPatientC))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 0 {
			t.Fatalf("a patient id presented as a session subject must read nothing, got %+v", rows)
		}
	})

	t.Run("B sees only B", func(t *testing.T) {
		code, rows := get(t, cookieFor(subPatientB))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0].EntityKey != "vtx.appointment.appt-B" {
			t.Fatalf("B must see exactly appt-B, got %+v", rows)
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

	t.Run("revoked grant hides the row", func(t *testing.T) {
		// Soft-tombstone A's grant: the policy filters NOT is_deleted, so A now
		// sees nothing — the RLS path honors revocation.
		exec("UPDATE actor_read_grants SET is_deleted = true WHERE actor_id = $1", subPatientA)
		defer exec("UPDATE actor_read_grants SET is_deleted = false WHERE actor_id = $1", subPatientA)
		code, rows := get(t, cookieFor(subPatientA))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 0 {
			t.Fatalf("a revoked grant must hide the row, got %+v", rows)
		}
	})

	t.Run("pooling safety: a second request inherits no actor", func(t *testing.T) {
		// Drain the pool through several sequential requests; if SET LOCAL leaked
		// across the pooled conn, an unauth'd-but-pooled query would inherit A.
		// Each request authenticates independently, so the only way B's request
		// returns A's row is a leaked session var — assert it never does.
		for i := 0; i < 5; i++ {
			_, rowsA := get(t, cookieFor(subPatientA))
			if len(rowsA) != 1 || rowsA[0].EntityKey != "vtx.appointment.appt-A" {
				t.Fatalf("iter %d: A leaked %+v", i, rowsA)
			}
			_, rowsB := get(t, cookieFor(subPatientB))
			if len(rowsB) != 1 || rowsB[0].EntityKey != "vtx.appointment.appt-B" {
				t.Fatalf("iter %d: B leaked %+v", i, rowsB)
			}
		}
	})
}
