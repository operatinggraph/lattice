package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// The D1.5 headline proof: a WildcardAnchor grant (adapter.WildcardAnchor) lets
// an actor read the ENTIRE applicantRosterRead protected table — the real
// Postgres RLS enforcement of the policy's wildcard OR-clause
// (internal/refractor/adapter/rls.go), driven through handleStaffIdentities
// exactly like a live loftspace-domain deployment would. Like
// clinicPatientsRead there is no self-anchor here: every seeded row carries an
// EMPTY authz_anchors set, so an ordinary actor (self-grant only, no wildcard)
// must see NOTHING — proving the roster has no accidental public-read
// fallback.
//
// Enforcement is REAL (non-superuser reader role, same fixture discipline as
// TestReadBoundary_RLS_Enforcement). Gated: skipped unless POSTGRES_TEST_DSN
// is set and -short is not active.

const subStaff = "SSSSSSSSSSSSSSSSSSSS"

func TestStaffIdentitiesReadBoundary_WildcardSeesEverything(t *testing.T) {
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
	ddl, err := adapter.BuildProtectedTableDDL("read_loftspace_identities", []string{"identity_id"}, []adapter.ColumnDef{
		{Name: "entity_key", Type: "text"},
		{Name: "identity_key", Type: "text"},
		{Name: "name", Type: "text"},
		{Name: "state", Type: "text"},
	})
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
	exec("GRANT SELECT ON " + rlsTestSchema + ".read_loftspace_identities TO " + rlsTestRole)
	exec("GRANT SELECT ON " + rlsTestSchema + ".actor_read_grants TO " + rlsTestRole)

	// Seed two identities with an EMPTY authz_anchors set each (the roster has no
	// self-anchor) plus a self-grant for identity A only, proving A's own
	// cap-read self-grant does NOT unlock the roster row (only the wildcard
	// does — the row itself carries no anchor a self-grant could ever match).
	exec(`INSERT INTO read_loftspace_identities (identity_id, entity_key, identity_key, name, state, authz_anchors, projection_seq)
	      VALUES ('id-A', 'vtx.identity.`+subAlice+`', 'vtx.identity.`+subAlice+`', 'Alice Renter', 'claimed', '{}', 1)`)
	exec(`INSERT INTO read_loftspace_identities (identity_id, entity_key, identity_key, name, state, authz_anchors, projection_seq)
	      VALUES ('id-B', 'vtx.identity.`+subBob+`', 'vtx.identity.`+subBob+`', 'Bob Tenant', 'unclaimed', '{}', 1)`)
	// A crypto-shredded identity: the Secure Lens projects its name as NULL.
	// selectIdentitiesSQL's `name IS NOT NULL` filter must keep it out of the
	// picker even for the wildcard reader (a NULL name would also fail the
	// plain-string Scan).
	exec(`INSERT INTO read_loftspace_identities (identity_id, entity_key, identity_key, name, state, authz_anchors, projection_seq)
	      VALUES ('id-S', 'vtx.identity.shredded', 'vtx.identity.shredded', NULL, 'claimed', '{}', 1)`)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subAlice)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $2, 'cap-read.root', 1, false)`, subStaff, adapter.WildcardAnchor)

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

	get := func(t *testing.T, authz string) (int, []protectedIdentityRow) {
		t.Helper()
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/staff/identities", nil)
		if authz != "" {
			r.Header.Set("Authorization", authz)
		}
		s.handleStaffIdentities(rec, r)
		var resp struct {
			Identities []protectedIdentityRow `json:"identities"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp.Identities
	}

	t.Run("staff sees every NAMED identity via the wildcard grant", func(t *testing.T) {
		code, rows := get(t, "Bearer "+mint(subStaff))
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 2 {
			t.Fatalf("staff must see both named identities (and NOT the NULL-name shredded row), got %+v", rows)
		}
		for _, row := range rows {
			if row.IdentityKey == "vtx.identity.shredded" {
				t.Fatalf("the shredded (NULL-name) identity must not appear in the picker: %+v", rows)
			}
		}
	})

	t.Run("an ordinary identity (self-grant only, no wildcard) sees nothing — no self-anchor exists on the roster", func(t *testing.T) {
		code, rows := get(t, "Bearer "+mint(subAlice))
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
