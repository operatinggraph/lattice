package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asolgan/lattice/internal/refractor/adapter"
)

// TestObjectsD1_5_LeaseappEntitlement_RLS proves entitledToObjectOwner's
// leaseapp branch against a REAL RLS-enforced Postgres (reusing the same
// read_lease_applications + actor_read_grants fixture shape
// TestReadBoundary_RLS_Enforcement provisions): an actor is entitled to a
// leaseapp-owned object iff the protected read model resolves that leaseapp
// for them, and handleObjectList/handleObjectGet enforce it end-to-end.
//
// Gated: skipped unless POSTGRES_TEST_DSN is set (see skipIfNoPostgresRLS).
func TestObjectsD1_5_LeaseappEntitlement_RLS(t *testing.T) {
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
		{Name: "unit_key", Type: "text"},
		{Name: "unit_address", Type: "text"},
		{Name: "unit_city", Type: "text"},
		{Name: "unit_region", Type: "text"},
		{Name: "unit_rent", Type: "double precision"},
		{Name: "unit_currency", Type: "text"},
		{Name: "unit_status", Type: "text"},
		{Name: "unit_bedrooms", Type: "double precision"},
		{Name: "unit_bathrooms", Type: "double precision"},
		{Name: "unit_available_from", Type: "text"},
		{Name: "signed_at", Type: "text"},
		{Name: "landlord_decision", Type: "text"},
		{Name: "decline_reason", Type: "text"},
		{Name: "terms_move_in_date", Type: "text"},
		{Name: "terms_lease_term_months", Type: "double precision"},
		{Name: "terms_requested_rent", Type: "double precision"},
		{Name: "doc_store_name", Type: "text"},
		{Name: "doc_filename", Type: "text"},
		{Name: "doc_content_type", Type: "text"},
	}
	ddl, err := adapter.BuildProtectedTableDDL("read_lease_applications", []string{"app_id"}, body)
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
	exec("GRANT SELECT ON " + rlsTestSchema + ".read_lease_applications TO " + rlsTestRole)
	exec("GRANT SELECT ON " + rlsTestSchema + ".actor_read_grants TO " + rlsTestRole)

	// A owns app-A; B owns app-B. Self-grants for both.
	exec(`INSERT INTO read_lease_applications (app_id, entity_key, applicant, authz_anchors, projection_seq)
	      VALUES ('app-A', 'vtx.leaseapp.app-A', 'vtx.identity.`+subAlice+`', $1, 1)`, []string{subAlice})
	exec(`INSERT INTO read_lease_applications (app_id, entity_key, applicant, authz_anchors, projection_seq)
	      VALUES ('app-B', 'vtx.leaseapp.app-B', 'vtx.identity.`+subBob+`', $1, 1)`, []string{subBob})
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subAlice)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subBob)

	reader := poolInSchema(t, dsn, rlsTestRole)
	defer reader.Close()

	t.Setenv("LOFTSPACE_APP_DEV_AUTH", "1")
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

	t.Run("A is entitled to A's leaseapp, not B's", func(t *testing.T) {
		if !s.entitledToObjectOwner(ctx, subAlice, "vtx.leaseapp.app-A") {
			t.Error("A must be entitled to app-A")
		}
		if s.entitledToObjectOwner(ctx, subAlice, "vtx.leaseapp.app-B") {
			t.Error("A must NOT be entitled to app-B")
		}
	})

	t.Run("resolveAllowedObjectOwners: A requesting her own leaseapp is allowed", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/api/objects?owner=vtx.leaseapp.app-A", nil)
		r.Header.Set("Authorization", "Bearer "+mint(subAlice))
		allowed, status, msg := s.resolveAllowedObjectOwners(ctx, r, []string{"vtx.leaseapp.app-A"})
		if status != 0 {
			t.Fatalf("status = %d (%s), want 0", status, msg)
		}
		if len(allowed) != 1 || allowed[0] != "vtx.leaseapp.app-A" {
			t.Fatalf("allowed = %v, want [vtx.leaseapp.app-A]", allowed)
		}
	})

	t.Run("resolveAllowedObjectOwners: A requesting B's leaseapp is silently dropped, not leaked", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/api/objects?owner=vtx.leaseapp.app-B", nil)
		r.Header.Set("Authorization", "Bearer "+mint(subAlice))
		allowed, status, _ := s.resolveAllowedObjectOwners(ctx, r, []string{"vtx.leaseapp.app-B"})
		if status != 0 {
			t.Fatalf("status = %d, want 0 (drop, not error)", status)
		}
		if len(allowed) != 0 {
			t.Fatalf("allowed = %v, want empty — B's leaseapp must not leak to A", allowed)
		}
	})

	t.Run("authorizeObjectGet: A can view an object owned by her own leaseapp", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/api/objects/oid1", nil)
		r.Header.Set("Authorization", "Bearer "+mint(subAlice))
		ok, status, _ := s.authorizeObjectGet(ctx, r, []string{"vtx.leaseapp.app-A"})
		if !ok || status != 0 {
			t.Fatalf("ok=%v status=%d, want ok=true status=0", ok, status)
		}
	})

	t.Run("authorizeObjectGet: A cannot view an object owned only by B's leaseapp", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/api/objects/oid2", nil)
		r.Header.Set("Authorization", "Bearer "+mint(subAlice))
		ok, status, _ := s.authorizeObjectGet(ctx, r, []string{"vtx.leaseapp.app-B"})
		if ok || status != http.StatusNotFound {
			t.Fatalf("ok=%v status=%d, want ok=false status=404 (indistinguishable from absent)", ok, status)
		}
	})
}
