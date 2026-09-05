package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/operatinggraph/lattice/internal/appsession"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
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
	ddl, err := adapter.BuildProtectedTableDDL("read_lease_applications", []string{"app_id"}, applicantProtectedColumns())
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
	exec(`INSERT INTO read_lease_applications (app_id, entity_key, applicant, authz_anchors, projection_seq,
	      profile_submitted, missing_onboarding, missing_bgcheck, missing_payment, missing_signature, missing_decision,
	      inflight_bgcheck, inflight_payment, declined_bgcheck, declined_payment, declined)
	      VALUES ('app-A', 'vtx.leaseapp.app-A', 'vtx.identity.`+subAlice+`', $1, 1,
	      false, false, false, false, false, false, false, false, false, false, false)`, []string{subAlice})
	exec(`INSERT INTO read_lease_applications (app_id, entity_key, applicant, authz_anchors, projection_seq,
	      profile_submitted, missing_onboarding, missing_bgcheck, missing_payment, missing_signature, missing_decision,
	      inflight_bgcheck, inflight_payment, declined_bgcheck, declined_payment, declined)
	      VALUES ('app-B', 'vtx.leaseapp.app-B', 'vtx.identity.`+subBob+`', $1, 1,
	      false, false, false, false, false, false, false, false, false, false, false)`, []string{subBob})
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subAlice)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subBob)

	reader := poolInSchema(t, dsn, rlsTestRole)
	defer reader.Close()

	s := &server{logger: discardLogger(), natsTimeout: testTimeout, pgPool: reader}

	// asA returns a request whose context already carries a resolved session
	// for subAlice — exactly what s.session.RequireSession installs before a
	// real handler runs, since resolveAllowedObjectOwners/authorizeObjectGet
	// are exercised directly here rather than through the full mux.
	asA := func(path string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		return r.WithContext(appsession.WithSession(r.Context(), subAlice, true))
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
		allowed, status, msg := s.resolveAllowedObjectOwners(ctx, asA("/api/objects?owner=vtx.leaseapp.app-A"), []string{"vtx.leaseapp.app-A"})
		if status != 0 {
			t.Fatalf("status = %d (%s), want 0", status, msg)
		}
		if len(allowed) != 1 || allowed[0] != "vtx.leaseapp.app-A" {
			t.Fatalf("allowed = %v, want [vtx.leaseapp.app-A]", allowed)
		}
	})

	t.Run("resolveAllowedObjectOwners: A requesting B's leaseapp is silently dropped, not leaked", func(t *testing.T) {
		allowed, status, _ := s.resolveAllowedObjectOwners(ctx, asA("/api/objects?owner=vtx.leaseapp.app-B"), []string{"vtx.leaseapp.app-B"})
		if status != 0 {
			t.Fatalf("status = %d, want 0 (drop, not error)", status)
		}
		if len(allowed) != 0 {
			t.Fatalf("allowed = %v, want empty — B's leaseapp must not leak to A", allowed)
		}
	})

	t.Run("authorizeObjectGet: A can view an object owned by her own leaseapp", func(t *testing.T) {
		ok, status, _ := s.authorizeObjectGet(ctx, asA("/api/objects/oid1"), []string{"vtx.leaseapp.app-A"})
		if !ok || status != 0 {
			t.Fatalf("ok=%v status=%d, want ok=true status=0", ok, status)
		}
	})

	t.Run("authorizeObjectGet: A cannot view an object owned only by B's leaseapp", func(t *testing.T) {
		ok, status, _ := s.authorizeObjectGet(ctx, asA("/api/objects/oid2"), []string{"vtx.leaseapp.app-B"})
		if ok || status != http.StatusNotFound {
			t.Fatalf("ok=%v status=%d, want ok=false status=404 (indistinguishable from absent)", ok, status)
		}
	})
}
