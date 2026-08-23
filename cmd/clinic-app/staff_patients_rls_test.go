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
		{Name: "unlinked_name", Type: "text"},
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
	      VALUES ('pat-A', 'vtx.patient.` + subPatientA + `', 'vtx.patient.` + subPatientA + `', 'Alice Rivera', '{` + subPatientA + `}', 1)`)
	exec(`INSERT INTO read_clinic_patients (patient_id, entity_key, patient_key, name, authz_anchors, projection_seq)
	      VALUES ('pat-B', 'vtx.patient.` + subPatientB + `', 'vtx.patient.` + subPatientB + `', 'Bob Nguyen', '{` + subPatientB + `}', 1)`)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subPatientA)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $2, 'cap-read.root', 1, false)`, subStaff, adapter.WildcardAnchor)

	// A third row proves the name/unlinked_name fallback (the disjoint-column
	// COALESCE selectPatientsSQL now projects): patient W is a walk-in — no
	// identifiedBy identity, so its SECURE name column is NULL and only its
	// plaintext unlinked_name is populated, the mirror image of A/B above. No
	// self-grant is seeded for it, so only the wildcard staff actor can see it.
	exec(`INSERT INTO read_clinic_patients (patient_id, entity_key, patient_key, unlinked_name, authz_anchors, projection_seq)
	      VALUES ('pat-W', 'vtx.patient.WWWWWWWWWWWWWWWWWWWW', 'vtx.patient.WWWWWWWWWWWWWWWWWWWW', 'Wendy Walk-in', '{WWWWWWWWWWWWWWWWWWWW}', 1)`)

	// A fourth row is an IDENTIFIED patient whose identity's .name aspect was
	// shredded (ShredIdentityKey): both name and unlinked_name are NULL — the
	// disjoint pair's only shared-empty state. queryPatients must still return
	// this row (as "no name", never an error) rather than fail the scan.
	exec(`INSERT INTO read_clinic_patients (patient_id, entity_key, patient_key, authz_anchors, projection_seq)
	      VALUES ('pat-S', 'vtx.patient.SSSSSSSSSSSSSSSSSSSS', 'vtx.patient.SSSSSSSSSSSSSSSSSSSS', '{SSSSSSSSSSSSSSSSSSSS}', 1)`)

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
		if len(rows) != 4 {
			t.Fatalf("staff must see all FOUR patients (A, B, walk-in W, shredded S), got %+v", rows)
		}
	})

	// The disjoint name/unlinked_name COALESCE (selectPatientsSQL): an
	// identified patient's name comes from the SECURE column (A), a walk-in's
	// comes from the plaintext column (W), and a shredded identified patient
	// (S, both columns NULL) still comes back as a row with an empty Name
	// rather than an error — proving protectedPatientRow.Name's plain-string
	// COALESCE(..., '') never chokes on the all-NULL case.
	t.Run("name falls back to unlinked_name for a walk-in, and a shredded identified patient still comes back as a row", func(t *testing.T) {
		code, rows := get(t, cookieFor(subStaff))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		byKey := make(map[string]protectedPatientRow, len(rows))
		for _, r := range rows {
			byKey[r.PatientKey] = r
		}
		identified, ok := byKey["vtx.patient."+subPatientA]
		if !ok || identified.Name != "Alice Rivera" {
			t.Fatalf("identified patient A must project its SECURE name column, got %+v (present=%v)", identified, ok)
		}
		walkIn, ok := byKey["vtx.patient.WWWWWWWWWWWWWWWWWWWW"]
		if !ok || walkIn.Name != "Wendy Walk-in" {
			t.Fatalf("walk-in patient W must fall back to unlinked_name, got %+v (present=%v)", walkIn, ok)
		}
		shredded, ok := byKey["vtx.patient.SSSSSSSSSSSSSSSSSSSS"]
		if !ok {
			t.Fatalf("a shredded identified patient (both name columns NULL) must still come back as a row, not be dropped")
		}
		if shredded.Name != "" {
			t.Fatalf("a shredded identified patient's Name must be empty, not %q", shredded.Name)
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

// TestStaffPatientsReadBoundary_WorkplaceAnchorSeesSharedBuildingOnly proves
// the front-desk half of clinicPatientsReadSpec's authz_anchors end to end:
// a worksAt-anchored front-desk actor (service-location's staffReadGrants,
// grant_source cap-read.staff — never the reserved WildcardAnchor) must see a
// patient whose authz_anchors carries the SAME building their care touches,
// and must NOT see a patient anchored to a different building. package.go
// (clinic-domain) and clinicPatientsReadSpec's own doc comment both claim
// this path already works ("patient-self-plus-workplace-plus-staff-wildcard"
// — commit f4e90653); this is the RLS boundary test that was missing to back
// that claim (verticals.md "worksAt front-desk staffer's patient-context
// switcher is empty despite clinic-wide appointment access").
func TestStaffPatientsReadBoundary_WorkplaceAnchorSeesSharedBuildingOnly(t *testing.T) {
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

	const (
		patientRiverside = "vtx.patient.CCCCCCCCCCCCCCCCCCCC"
		riverside        = "RRRRRRRRRRRRRRRRRRRR"
		patientDowntown  = "vtx.patient.DDDDDDDDDDDDDDDDDDDD"
		downtown         = "TTTTTTTTTTTTTTTTTTTT"
		frontDeskAtRiver = "FFFFFFFFFFFFFFFFFFFF"
	)

	// Patient C's care touches Riverside (a provider practicesAt Riverside for
	// an appointment of theirs); patient D's touches Downtown only — mirrors
	// clinicPatientsReadSpec's own authz_anchors shape: [patient's own NanoID]
	// + [the practicesAt building of every provider it has an appointment
	// with].
	exec(`INSERT INTO read_clinic_patients (patient_id, entity_key, patient_key, name, authz_anchors, projection_seq)
	      VALUES ('pat-C', '` + patientRiverside + `', '` + patientRiverside + `', 'Cara Ibarra', '{CCCCCCCCCCCCCCCCCCCC,` + riverside + `}', 1)`)
	exec(`INSERT INTO read_clinic_patients (patient_id, entity_key, patient_key, name, authz_anchors, projection_seq)
	      VALUES ('pat-D', '` + patientDowntown + `', '` + patientDowntown + `', 'Dana Osei', '{DDDDDDDDDDDDDDDDDDDD,` + downtown + `}', 1)`)

	// The front-desk actor holds ONLY a per-building cap-read.staff grant
	// (service-location's staffReadGrants shape) for Riverside — never the
	// WildcardAnchor and never a per-patient self-grant.
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $2, 'cap-read.staff', 1, false)`, frontDeskAtRiver, riverside)

	reader := poolInSchema(t, dsn, clinicRLSTestRole)
	defer reader.Close()

	s, cookieFor := devSessionServer(t, func(s *server) { s.pgPool = reader })

	get := func(t *testing.T, c *http.Cookie) (int, []protectedPatientRow) {
		t.Helper()
		rec := sessionGET(s, s.handleStaffPatients, "/api/staff/patients", c)
		var resp struct {
			Patients []protectedPatientRow `json:"patients"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp.Patients
	}

	t.Run("a worksAt-anchored front-desk actor sees the patient sharing its building, not the one at another building", func(t *testing.T) {
		code, rows := get(t, cookieFor(frontDeskAtRiver))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0].PatientKey != patientRiverside {
			t.Fatalf("front-desk actor worksAt Riverside must see exactly patient C (Riverside), got %+v", rows)
		}
	})
}
