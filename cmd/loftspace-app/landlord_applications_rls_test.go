package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asolgan/lattice/internal/refractor/adapter"
)

// The D1.3 Increment 3 headline proof: the authenticated LANDLORD read boundary
// enforces RLS on the real protected read_landlord_lease_applications model. It
// provisions the table + policy with the SAME refractor helpers a live activation
// uses (BuildProtectedTableDDL with the COMPOSITE (app_id, landlord_id) key /
// BuildGrantTableDDL), seeds two landlords' units + a co-managed unit + their
// self-grants, and drives handleLandlordApplications through httptest with minted
// JWTs.
//
// Enforcement is REAL: the reader runs as a NON-superuser role (RLS is bypassed by
// superusers/BYPASSRLS). The fixture lives in a dedicated schema dropped at the
// end, so it is safe against a live database.
//
// Gated: skipped unless POSTGRES_TEST_DSN is set and -short is not active (CI has
// no Postgres). Shares the helpers (skipIfNoPostgresRLS / poolInSchema /
// rlsTestSchema / rlsTestRole / discardLogger / testTimeout) with the applicant
// RLS proof in applications_rls_test.go.

const (
	subLarry = "LLLLLLLLLLLLLLLLLLLL"
	subLinda = "NNNNNNNNNNNNNNNNNNNN"
)

func TestLandlordReadBoundary_RLS_Enforcement(t *testing.T) {
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

	exec("DROP SCHEMA IF EXISTS " + rlsTestSchema + " CASCADE")
	exec("CREATE SCHEMA " + rlsTestSchema)
	t.Cleanup(func() {
		_, _ = owner.Exec(ctx, "DROP SCHEMA IF EXISTS "+rlsTestSchema+" CASCADE")
		_, _ = owner.Exec(ctx, "DROP OWNED BY "+rlsTestRole+" CASCADE")
		_, _ = owner.Exec(ctx, "DROP ROLE IF EXISTS "+rlsTestRole)
	})

	for _, stmt := range adapter.BuildGrantTableDDL() {
		exec(stmt)
	}
	body := []adapter.ColumnDef{
		{Name: "entity_key", Type: "text"},
		{Name: "applicant", Type: "text"},
		{Name: "landlord_key", Type: "text"},
		{Name: "unit_key", Type: "text"},
		{Name: "unit_address", Type: "text"},
		{Name: "unit_city", Type: "text"},
		{Name: "unit_region", Type: "text"},
		{Name: "unit_rent", Type: "double precision"},
		{Name: "unit_currency", Type: "text"},
		{Name: "unit_status", Type: "text"},
		{Name: "signed_at", Type: "text"},
		{Name: "landlord_decision", Type: "text"},
		{Name: "decline_reason", Type: "text"},
		{Name: "terms_move_in_date", Type: "text"},
		{Name: "terms_lease_term_months", Type: "double precision"},
		{Name: "terms_requested_rent", Type: "double precision"},
	}
	// The COMPOSITE key — this is what lets a co-managed unit's application carry one
	// row per landlord without a primary-key collision.
	ddl, err := adapter.BuildProtectedTableDDL("read_landlord_lease_applications", []string{"app_id", "landlord_id"}, body)
	if err != nil {
		t.Fatalf("build protected DDL: %v", err)
	}
	for _, stmt := range ddl {
		exec(stmt)
	}

	_, _ = owner.Exec(ctx, "DROP OWNED BY "+rlsTestRole+" CASCADE")
	_, _ = owner.Exec(ctx, "DROP ROLE IF EXISTS "+rlsTestRole)
	exec("CREATE ROLE " + rlsTestRole + " NOSUPERUSER NOLOGIN")
	exec("GRANT USAGE ON SCHEMA " + rlsTestSchema + " TO " + rlsTestRole)
	exec("GRANT SELECT ON " + rlsTestSchema + ".read_landlord_lease_applications TO " + rlsTestRole)
	exec("GRANT SELECT ON " + rlsTestSchema + ".actor_read_grants TO " + rlsTestRole)

	// Seed:
	//   - app-L on unit-L, managed by Larry           (anchor Larry)
	//   - app-N on unit-N, managed by Linda           (anchor Linda)
	//   - app-CO on unit-CO, CO-MANAGED by both        (two rows: anchor Larry / anchor Linda)
	insRow := func(appID, landlordID, entityKey, applicantID, landlordKey, unitKey string, anchor string) {
		exec(`INSERT INTO read_landlord_lease_applications
		      (app_id, landlord_id, entity_key, applicant, landlord_key, unit_key, authz_anchors, projection_seq)
		      VALUES ($1, $2, $3, $4, $5, $6, $7, 1)`,
			appID, landlordID, entityKey, applicantID, landlordKey, unitKey, []string{anchor})
	}
	insRow("app-L", subLarry, "vtx.leaseapp.app-L", "vtx.identity."+subAlice, "vtx.identity."+subLarry, "vtx.unit.unit-L", subLarry)
	insRow("app-N", subLinda, "vtx.leaseapp.app-N", "vtx.identity."+subBob, "vtx.identity."+subLinda, "vtx.unit.unit-N", subLinda)
	insRow("app-CO", subLarry, "vtx.leaseapp.app-CO", "vtx.identity."+subAlice, "vtx.identity."+subLarry, "vtx.unit.unit-CO", subLarry)
	insRow("app-CO", subLinda, "vtx.leaseapp.app-CO", "vtx.identity."+subAlice, "vtx.identity."+subLinda, "vtx.unit.unit-CO", subLinda)

	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subLarry)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subLinda)

	reader := poolInSchema(t, dsn, rlsTestRole)
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

	// White-box: the txn-local actor var must be DISCARDED at COMMIT on the SAME
	// pooled connection — the pooling-safety crux (the strongest assertion, ported
	// from the applicant RLS proof). Set actor=Larry, read (2 rows), commit; then
	// re-query the SAME conn with NO actor set: a leaked is_local var would still
	// return Larry's rows. It must return 0 (FORCE RLS + unset actor → deny-all).
	t.Run("txn-local actor is discarded on the pooled conn (no leak)", func(t *testing.T) {
		conn, err := reader.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		defer conn.Release()
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", subLarry); err != nil {
			t.Fatalf("set_config: %v", err)
		}
		var n1 int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM read_landlord_lease_applications").Scan(&n1); err != nil {
			t.Fatalf("count in txn: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if n1 != 2 {
			t.Fatalf("inside the txn Larry must see his 2 rows, got %d", n1)
		}
		var n2 int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM read_landlord_lease_applications").Scan(&n2); err != nil {
			t.Fatalf("count after commit: %v", err)
		}
		if n2 != 0 {
			t.Fatalf("after COMMIT the actor var must be gone (RLS deny-all), got %d rows", n2)
		}
	})

	t.Setenv("LOFTSPACE_APP_DEV_AUTH", "1")
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

	type unitGroup struct {
		UnitKey      string                 `json:"unitKey"`
		Applications []protectedLandlordRow `json:"applications"`
	}
	get := func(t *testing.T, authz, query string) (int, []unitGroup, int) {
		t.Helper()
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/landlord/applications"+query, nil)
		if authz != "" {
			r.Header.Set("Authorization", authz)
		}
		s.handleLandlordApplications(rec, r)
		var resp struct {
			Units            []unitGroup `json:"units"`
			ApplicationCount int         `json:"applicationCount"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp.Units, resp.ApplicationCount
	}

	// unitKeys returns the set of unit keys in a grouped response.
	unitKeys := func(units []unitGroup) map[string]int {
		m := map[string]int{}
		for _, u := range units {
			m[u.UnitKey] = len(u.Applications)
		}
		return m
	}

	t.Run("Larry sees only his units (unit-L + the co-managed unit-CO)", func(t *testing.T) {
		code, units, appCount := get(t, "Bearer "+mint(subLarry), "")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		ks := unitKeys(units)
		if _, ok := ks["vtx.unit.unit-L"]; !ok {
			t.Errorf("Larry must see unit-L, got %v", ks)
		}
		if _, ok := ks["vtx.unit.unit-CO"]; !ok {
			t.Errorf("Larry must see the co-managed unit-CO, got %v", ks)
		}
		if _, ok := ks["vtx.unit.unit-N"]; ok {
			t.Errorf("Larry must NOT see Linda's unit-N, got %v", ks)
		}
		if appCount != 2 {
			t.Errorf("Larry must see exactly 2 applications (app-L + app-CO), got %d", appCount)
		}
	})

	t.Run("Linda sees only her units (unit-N + the co-managed unit-CO)", func(t *testing.T) {
		code, units, appCount := get(t, "Bearer "+mint(subLinda), "")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		ks := unitKeys(units)
		if _, ok := ks["vtx.unit.unit-N"]; !ok {
			t.Errorf("Linda must see unit-N, got %v", ks)
		}
		if _, ok := ks["vtx.unit.unit-CO"]; !ok {
			t.Errorf("Linda must see the co-managed unit-CO, got %v", ks)
		}
		if _, ok := ks["vtx.unit.unit-L"]; ok {
			t.Errorf("Linda must NOT see Larry's unit-L, got %v", ks)
		}
		if appCount != 2 {
			t.Errorf("Linda must see exactly 2 applications (app-N + app-CO), got %d", appCount)
		}
	})

	t.Run("a non-landlord actor sees nothing", func(t *testing.T) {
		// Alice is an applicant, not a landlord — she manages no unit, so no row is
		// anchored to her: RLS returns an empty set (no 403/404 oracle).
		exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
		      VALUES ($1, $1, 'cap-read', 1, false)`, subAlice)
		defer exec("DELETE FROM actor_read_grants WHERE actor_id = $1", subAlice)
		code, units, appCount := get(t, "Bearer "+mint(subAlice), "")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(units) != 0 || appCount != 0 {
			t.Fatalf("a non-landlord must see no units/applications, got units=%v appCount=%d", units, appCount)
		}
	})

	t.Run("a forged scope query param cannot widen", func(t *testing.T) {
		// RLS keys off the verified session var, not any param — Larry stays scoped.
		code, units, _ := get(t, "Bearer "+mint(subLarry), "?landlord=vtx.identity."+subLinda)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if _, ok := unitKeys(units)["vtx.unit.unit-N"]; ok {
			t.Fatalf("a ?landlord= param must NOT leak Linda's unit to Larry, got %v", unitKeys(units))
		}
	})

	t.Run("unauthenticated is 401", func(t *testing.T) {
		if code, _, _ := get(t, "", ""); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("forged token is 401", func(t *testing.T) {
		if code, _, _ := get(t, "Bearer not.a.jwt", ""); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("revoked grant hides the landlord's units", func(t *testing.T) {
		exec("UPDATE actor_read_grants SET is_deleted = true WHERE actor_id = $1", subLarry)
		defer exec("UPDATE actor_read_grants SET is_deleted = false WHERE actor_id = $1", subLarry)
		code, units, appCount := get(t, "Bearer "+mint(subLarry), "")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(units) != 0 || appCount != 0 {
			t.Fatalf("a revoked grant must hide everything, got units=%v appCount=%d", units, appCount)
		}
	})

	t.Run("pooling safety: requests do not leak the actor var across the pool", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			_, _, larryCount := get(t, "Bearer "+mint(subLarry), "")
			if larryCount != 2 {
				t.Fatalf("iter %d: Larry leaked/lost rows, appCount=%d", i, larryCount)
			}
			_, _, lindaCount := get(t, "Bearer "+mint(subLinda), "")
			if lindaCount != 2 {
				t.Fatalf("iter %d: Linda leaked/lost rows, appCount=%d", i, lindaCount)
			}
		}
	})
}
