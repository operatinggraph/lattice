package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// The reconnaissance-surface proof for GET /api/ledger (handleLedger /
// leaseVisibleToActor): the handler must not trust the leaseAppKey query
// param unchecked — skipping the authenticateRead call would let a signed-in
// resident read another lease's balance/accountKey by guessing its key
// (filed in verticals.md as "App-server GET endpoints apply no per-lease/
// patient ownership filter"). one_bill.go's handleOneBillStatement already
// closes the same class of leak for the combined statement via
// queryApplications; this proves the ledger applies the equivalent gate
// for BOTH parties who legitimately view a lease's ledger — the tenant
// (queryApplicationByKey) and the managing landlord
// (queryLandlordApplications), mirroring renderLedgerPanel's two callers in
// web/app.js.
//
// Enforcement is REAL Postgres RLS (non-superuser reader role), the same
// fixture discipline as applications_rls_test.go / landlord_applications_rls_test.go.
// Gated: skipped unless POSTGRES_TEST_DSN is set and -short is not active.
func TestLedgerReadBoundary_TenantAndLandlordVisible_StrangerDenied(t *testing.T) {
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

	tenantDDL, err := adapter.BuildProtectedTableDDL("read_lease_applications", []string{"app_id"}, []adapter.ColumnDef{
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
		{Name: "profile_submitted", Type: "boolean"},
		{Name: "income_to_rent_met", Type: "boolean"},
		{Name: "employment_verified", Type: "boolean"},
		{Name: "reference_count", Type: "double precision"},
		{Name: "has_co_applicant", Type: "boolean"},
		{Name: "has_guarantor", Type: "boolean"},
		{Name: "guarantor_income_to_rent_met", Type: "boolean"},
		{Name: "missing_onboarding", Type: "boolean"},
		{Name: "missing_bgcheck", Type: "boolean"},
		{Name: "missing_payment", Type: "boolean"},
		{Name: "missing_signature", Type: "boolean"},
		{Name: "missing_decision", Type: "boolean"},
		{Name: "inflight_bgcheck", Type: "boolean"},
		{Name: "inflight_payment", Type: "boolean"},
		{Name: "declined_bgcheck", Type: "boolean"},
		{Name: "declined_payment", Type: "boolean"},
		{Name: "declined", Type: "boolean"},
	})
	if err != nil {
		t.Fatalf("build tenant protected DDL: %v", err)
	}
	for _, stmt := range tenantDDL {
		exec(stmt)
	}

	landlordDDL, err := adapter.BuildProtectedTableDDL("read_landlord_lease_applications", []string{"app_id", "landlord_id"}, []adapter.ColumnDef{
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
	})
	if err != nil {
		t.Fatalf("build landlord protected DDL: %v", err)
	}
	for _, stmt := range landlordDDL {
		exec(stmt)
	}

	_, _ = owner.Exec(ctx, "DROP OWNED BY "+rlsTestRole+" CASCADE")
	_, _ = owner.Exec(ctx, "DROP ROLE IF EXISTS "+rlsTestRole)
	exec("CREATE ROLE " + rlsTestRole + " NOSUPERUSER NOLOGIN")
	exec("GRANT USAGE ON SCHEMA " + rlsTestSchema + " TO " + rlsTestRole)
	exec("GRANT SELECT ON " + rlsTestSchema + ".read_lease_applications TO " + rlsTestRole)
	exec("GRANT SELECT ON " + rlsTestSchema + ".read_landlord_lease_applications TO " + rlsTestRole)
	exec("GRANT SELECT ON " + rlsTestSchema + ".actor_read_grants TO " + rlsTestRole)

	// app-A: Alice's own lease, no landlord row (tenant-only visibility path).
	exec(`INSERT INTO read_lease_applications (app_id, entity_key, applicant, authz_anchors, projection_seq,
	      profile_submitted, missing_onboarding, missing_bgcheck, missing_payment, missing_signature, missing_decision,
	      inflight_bgcheck, inflight_payment, declined_bgcheck, declined_payment, declined)
	      VALUES ('app-A', 'vtx.leaseapp.app-A', 'vtx.identity.`+subAlice+`', $1, 1,
	      false, false, false, false, false, false, false, false, false, false, false)`, []string{subAlice})
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subAlice)

	// app-L: managed by Larry (landlord-only visibility path — no tenant row for
	// app-L at all, proving the landlord branch alone clears the gate).
	exec(`INSERT INTO read_landlord_lease_applications
	      (app_id, landlord_id, entity_key, applicant, landlord_key, unit_key, authz_anchors, projection_seq)
	      VALUES ('app-L', $1, 'vtx.leaseapp.app-L', 'vtx.identity.someone', 'vtx.identity.`+subLarry+`', 'vtx.unit.unit-L', $2, 1)`,
		subLarry, []string{subLarry})
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subLarry)

	reader := poolInSchema(t, dsn, rlsTestRole)
	defer reader.Close()

	s, cookieFor := devSessionServer(t, func(s *server) { s.pgPool = reader })

	// getLedger drives handleLedger through the real session middleware exactly
	// as the FE would. It never reaches s.requireConn (no NATS conn is wired
	// here) unless the ownership gate passes — a 401/403 proves the gate
	// rejected the request before any lens read was attempted; a 502 proves it
	// cleared the gate and only then hit the (unwired) NATS conn.
	getLedger := func(t *testing.T, c *http.Cookie, leaseAppKey string) int {
		t.Helper()
		return sessionGET(s, s.handleLedger, "/api/ledger?leaseAppKey="+leaseAppKey, c).Code
	}
	pastTheGate := func(t *testing.T, code int) {
		t.Helper()
		if code == http.StatusUnauthorized || code == http.StatusForbidden {
			t.Fatalf("status = %d, want past the ownership gate (502, no NATS conn wired)", code)
		}
	}

	t.Run("unauthenticated is 401 — the confirmed leak this closes", func(t *testing.T) {
		if code := getLedger(t, nil, "vtx.leaseapp.app-A"); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("the tenant clears the gate on her own lease", func(t *testing.T) {
		pastTheGate(t, getLedger(t, cookieFor(subAlice), "vtx.leaseapp.app-A"))
	})

	t.Run("the managing landlord clears the gate on a lease with no tenant row for him", func(t *testing.T) {
		pastTheGate(t, getLedger(t, cookieFor(subLarry), "vtx.leaseapp.app-L"))
	})

	t.Run("a stranger to app-A is 403, not served Alice's ledger", func(t *testing.T) {
		if code := getLedger(t, cookieFor(subBob), "vtx.leaseapp.app-A"); code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (Bob is neither tenant nor managing landlord of app-A)", code)
		}
	})

	t.Run("a stranger to app-L is 403, not served Larry's managed lease", func(t *testing.T) {
		if code := getLedger(t, cookieFor(subBob), "vtx.leaseapp.app-L"); code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (Bob manages no unit and is not the applicant)", code)
		}
	})

	t.Run("the tenant cannot pull the landlord-only lease by guessing its key", func(t *testing.T) {
		if code := getLedger(t, cookieFor(subAlice), "vtx.leaseapp.app-L"); code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (Alice is not app-L's tenant or landlord)", code)
		}
	})
}
