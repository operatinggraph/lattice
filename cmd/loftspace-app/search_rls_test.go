package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asolgan/lattice/internal/refractor/adapter"
)

// The front-of-house unified search RLS proof: provisions BOTH protected
// tables the search fans out over (read_loftspace_identities,
// read_landlord_lease_applications) with the same non-superuser reader role
// discipline as TestLandlordReadBoundary_RLS_Enforcement /
// TestStaffIdentitiesReadBoundary_WildcardSeesEverything, then drives
// handleSearch through httptest.
//
// Gated: skipped unless POSTGRES_TEST_DSN is set and -short is not active.

func TestUnifiedSearch_RLS_Enforcement(t *testing.T) {
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

	identitiesDDL, err := adapter.BuildProtectedTableDDL("read_loftspace_identities", []string{"identity_id"}, []adapter.ColumnDef{
		{Name: "entity_key", Type: "text"},
		{Name: "identity_key", Type: "text"},
		{Name: "name", Type: "text"},
		{Name: "state", Type: "text"},
	})
	if err != nil {
		t.Fatalf("build identities DDL: %v", err)
	}
	for _, stmt := range identitiesDDL {
		exec(stmt)
	}

	landlordBody := []adapter.ColumnDef{
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
		{Name: "profile_submitted", Type: "boolean"},
		{Name: "income_to_rent_met", Type: "boolean"},
		{Name: "employment_verified", Type: "boolean"},
		{Name: "reference_count", Type: "double precision"},
		{Name: "has_co_applicant", Type: "boolean"},
		{Name: "has_guarantor", Type: "boolean"},
		{Name: "guarantor_income_to_rent_met", Type: "boolean"},
		{Name: "applicant_name", Type: "text"},
		{Name: "applicant_email", Type: "text"},
		{Name: "applicant_phone", Type: "text"},
		{Name: "qualified", Type: "boolean"},
	}
	landlordDDL, err := adapter.BuildProtectedTableDDL("read_landlord_lease_applications", []string{"app_id", "landlord_id"}, landlordBody)
	if err != nil {
		t.Fatalf("build landlord DDL: %v", err)
	}
	for _, stmt := range landlordDDL {
		exec(stmt)
	}

	_, _ = owner.Exec(ctx, "DROP OWNED BY "+rlsTestRole+" CASCADE")
	_, _ = owner.Exec(ctx, "DROP ROLE IF EXISTS "+rlsTestRole)
	exec("CREATE ROLE " + rlsTestRole + " NOSUPERUSER NOLOGIN")
	exec("GRANT USAGE ON SCHEMA " + rlsTestSchema + " TO " + rlsTestRole)
	exec("GRANT SELECT ON " + rlsTestSchema + ".read_loftspace_identities TO " + rlsTestRole)
	exec("GRANT SELECT ON " + rlsTestSchema + ".read_landlord_lease_applications TO " + rlsTestRole)
	exec("GRANT SELECT ON " + rlsTestSchema + ".actor_read_grants TO " + rlsTestRole)

	// Roster (wildcard-only, staff-visible): Alice + Bob named.
	exec(`INSERT INTO read_loftspace_identities (identity_id, entity_key, identity_key, name, state, authz_anchors, projection_seq)
	      VALUES ('id-A', 'vtx.identity.` + subAlice + `', 'vtx.identity.` + subAlice + `', 'Alice Applicant', 'claimed', '{}', 1)`)
	exec(`INSERT INTO read_loftspace_identities (identity_id, entity_key, identity_key, name, state, authz_anchors, projection_seq)
	      VALUES ('id-B', 'vtx.identity.` + subBob + `', 'vtx.identity.` + subBob + `', 'Bob Tenant', 'claimed', '{}', 1)`)

	// Larry manages unit-L (Alice applied, with decrypted contact name);
	// Linda manages unit-N (Bob applied). Landlord-anchored per row.
	insRow := func(appID, landlordID, entityKey, applicantSub, landlordSub, unitKey, addr, city, applicantName, anchor string) {
		exec(`INSERT INTO read_landlord_lease_applications
		      (app_id, landlord_id, entity_key, applicant, landlord_key, unit_key, unit_address, unit_city, applicant_name, authz_anchors, projection_seq)
		      VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 1)`,
			appID, landlordID, entityKey, "vtx.identity."+applicantSub, "vtx.identity."+landlordSub, unitKey, addr, city, applicantName, []string{anchor})
	}
	insRow("app-L", subLarry, "vtx.leaseapp.app-L", subAlice, subLarry, "vtx.unit.unit-L", "1 Main St", "Springfield", "Alice Applicant", subLarry)
	insRow("app-N", subLinda, "vtx.leaseapp.app-N", subBob, subLinda, "vtx.unit.unit-N", "2 Oak Ave", "Shelbyville", "Bob Tenant", subLinda)

	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subLarry)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subLinda)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $2, 'cap-read.root', 1, false)`, subStaff, adapter.WildcardAnchor)

	reader := poolInSchema(t, dsn, rlsTestRole)
	defer reader.Close()

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

	search := func(t *testing.T, authz, q string) (int, searchResult) {
		t.Helper()
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/search?q="+q, nil)
		if authz != "" {
			r.Header.Set("Authorization", authz)
		}
		s.handleSearch(rec, r)
		var resp searchResult
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp
	}

	t.Run("staff (wildcard) finds a person by roster name across every landlord's units", func(t *testing.T) {
		code, res := search(t, "Bearer "+mint(subStaff), "Alice")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(res.People) != 1 || res.People[0].IdentityKey != "vtx.identity."+subAlice {
			t.Fatalf("expected Alice as the sole person hit, got %+v", res.People)
		}
		if len(res.People[0].Applications) != 1 || res.People[0].Applications[0].EntityKey != "vtx.leaseapp.app-L" {
			t.Fatalf("expected Alice's app-L application, got %+v", res.People[0].Applications)
		}
	})

	t.Run("Larry finds his own applicant Alice by name with NO roster grant (landlord-scoped applicant_name match)", func(t *testing.T) {
		code, res := search(t, "Bearer "+mint(subLarry), "Alice")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(res.People) != 1 || res.People[0].Name != "Alice Applicant" {
			t.Fatalf("expected Larry to find Alice via applicant_name, got %+v", res.People)
		}
	})

	t.Run("Larry searching Bob's name (Linda's applicant) finds nothing", func(t *testing.T) {
		code, res := search(t, "Bearer "+mint(subLarry), "Bob")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(res.People) != 0 {
			t.Fatalf("Larry must not see Linda's applicant Bob, got %+v", res.People)
		}
	})

	t.Run("Larry finds his own unit by address, not Linda's", func(t *testing.T) {
		code, res := search(t, "Bearer "+mint(subLarry), "Main")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(res.Units) != 1 || res.Units[0].UnitKey != "vtx.unit.unit-L" {
			t.Fatalf("expected unit-L, got %+v", res.Units)
		}
		code, res = search(t, "Bearer "+mint(subLarry), "Shelbyville")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(res.Units) != 0 {
			t.Fatalf("Larry must not see Linda's unit-N, got %+v", res.Units)
		}
	})

	t.Run("a non-landlord, non-staff actor finds nothing", func(t *testing.T) {
		code, res := search(t, "Bearer "+mint(subAlice), "Alice")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(res.People) != 0 || len(res.Units) != 0 {
			t.Fatalf("an applicant actor (no landlord/wildcard grant) must see no search hits, got %+v", res)
		}
	})

	t.Run("blank query returns empty without a DB round trip error", func(t *testing.T) {
		code, res := search(t, "Bearer "+mint(subStaff), "")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(res.People) != 0 || len(res.Units) != 0 {
			t.Fatalf("blank query must return empty, got %+v", res)
		}
	})

	t.Run("unauthenticated is 401", func(t *testing.T) {
		if code, _ := search(t, "", "Alice"); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})
}
