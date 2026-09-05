package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	natsjetstream "github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// The D1.3 Fire 3 headline proof: the authenticated read boundary enforces RLS on
// the real protected Postgres read model. It provisions the table + policy with
// the SAME refractor helpers a live activation uses (BuildProtectedTableDDL /
// BuildGrantTableDDL), seeds two applicants' rows + self-grants, and drives
// handleApplications through httptest with minted JWTs.
//
// Enforcement is REAL: the reader runs as a NON-superuser role (RLS is bypassed
// by superusers/BYPASSRLS, so the app role must not be one — design §3.3). The
// whole fixture lives in a dedicated schema dropped at the end, so it is safe to
// point POSTGRES_TEST_DSN at a live database.
//
// Gated: skipped unless POSTGRES_TEST_DSN is set and -short is not active (CI has
// no Postgres; this is a local enforcement proof, like the Fire 1/2 gated tests).

const (
	rlsTestSchema = "loftspace_rls_test"
	rlsTestRole   = "loftspace_rls_test_reader"
	subAlice      = "AAAAAAAAAAAAAAAAAAAA"
	subBob        = "BBBBBBBBBBBBBBBBBBBB"
	// subCarl / subDave / subEve exercise the approval gate's three vectors
	// (approved+signed+anchored, declined, signed-but-undecided).
	subCarl = "CCCCCCCCCCCCCCCCCCCC"
	subDave = "DDDDDDDDDDDDDDDDDDDD"
	subEve  = "EEEEEEEEEEEEEEEEEEEE"
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
		if _, err := c.Exec(ctx, "SET search_path TO "+rlsTestSchema+", public"); err != nil {
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
	exec("DROP SCHEMA IF EXISTS " + rlsTestSchema + " CASCADE")
	exec("CREATE SCHEMA " + rlsTestSchema)
	t.Cleanup(func() {
		_, _ = owner.Exec(ctx, "DROP SCHEMA IF EXISTS "+rlsTestSchema+" CASCADE")
		_, _ = owner.Exec(ctx, "DROP OWNED BY "+rlsTestRole+" CASCADE")
		_, _ = owner.Exec(ctx, "DROP ROLE IF EXISTS "+rlsTestRole)
	})

	// Provision the grant table + the protected table with the real refractor DDL
	// (the policy references actor_read_grants — both unqualified, resolved in the
	// schema via search_path).
	for _, stmt := range adapter.BuildGrantTableDDL() {
		exec(stmt)
	}
	body := applicantProtectedColumns()
	ddl, err := adapter.BuildProtectedTableDDL("read_lease_applications", []string{"app_id"}, body)
	if err != nil {
		t.Fatalf("build protected DDL: %v", err)
	}
	for _, stmt := range ddl {
		exec(stmt)
	}

	// The lease-document GET falls back to read_landlord_lease_applications
	// when the applicant-scoped read finds no row (this test's own "A
	// requesting B's key" vector exercises exactly that fallback) — the
	// handler always queries it, so it must exist even with zero rows here.
	landlordDDL, err := adapter.BuildProtectedTableDDL("read_landlord_lease_applications", []string{"app_id", "landlord_id"}, landlordProtectedColumns())
	if err != nil {
		t.Fatalf("build landlord protected DDL: %v", err)
	}
	for _, stmt := range landlordDDL {
		exec(stmt)
	}

	// The non-superuser reader role (RLS is bypassed by superusers — the app must
	// not be one). USAGE on the schema + SELECT on both tables (the policy subquery
	// reads actor_read_grants).
	// Tolerant pre-clean (the role may not exist on a first run; DROP OWNED errors
	// if the role is absent).
	_, _ = owner.Exec(ctx, "DROP OWNED BY "+rlsTestRole+" CASCADE")
	_, _ = owner.Exec(ctx, "DROP ROLE IF EXISTS "+rlsTestRole)
	exec("CREATE ROLE " + rlsTestRole + " NOSUPERUSER NOLOGIN")
	exec("GRANT USAGE ON SCHEMA " + rlsTestSchema + " TO " + rlsTestRole)
	exec("GRANT SELECT ON " + rlsTestSchema + ".read_lease_applications TO " + rlsTestRole)
	exec("GRANT SELECT ON " + rlsTestSchema + ".read_landlord_lease_applications TO " + rlsTestRole)
	exec("GRANT SELECT ON " + rlsTestSchema + ".actor_read_grants TO " + rlsTestRole)

	// Seed: A's application (anchor A, signed with NO doc pointers — the
	// lease-document tests exercise the inside-the-convergence-window answer) +
	// B's application (anchor B, unsigned); self-grants. Both rows carry
	// unit_bedrooms/unit_bathrooms/unit_available_from so the round-trip
	// assertions below guard against the SELECT/Scan silently dropping them.
	exec(`INSERT INTO read_lease_applications (app_id, entity_key, applicant, landlord_decision, signed_at, unit_bedrooms, unit_bathrooms, unit_available_from, authz_anchors, projection_seq,
	      profile_submitted, missing_onboarding, missing_bgcheck, missing_payment, missing_signature, missing_decision,
	      inflight_bgcheck, inflight_payment, declined_bgcheck, declined_payment, declined)
	      VALUES ('app-A', 'vtx.leaseapp.app-A', 'vtx.identity.`+subAlice+`', 'approved', '2026-07-15T00:00:00Z', 2, 1, '2026-08-01', $1, 1,
	      false, false, false, false, false, false, false, false, false, false, false)`, []string{subAlice})
	exec(`INSERT INTO read_lease_applications (app_id, entity_key, applicant, unit_bedrooms, unit_bathrooms, unit_available_from, authz_anchors, projection_seq,
	      profile_submitted, missing_onboarding, missing_bgcheck, missing_payment, missing_signature, missing_decision,
	      inflight_bgcheck, inflight_payment, declined_bgcheck, declined_payment, declined)
	      VALUES ('app-B', 'vtx.leaseapp.app-B', 'vtx.identity.`+subBob+`', 3, 2, '2026-09-15', $1, 1,
	      false, true, true, true, true, false, false, false, false, false, false)`, []string{subBob})
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subAlice)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subBob)

	// The approval-gate vectors: Carl (approved + signed + ANCHORED — the true
	// 200 path, proven against a real embedded-NATS object store below), Dave
	// (declined, signed), Eve (signed, no decision yet — undecided). All three
	// must read "not approved" from the gate, never "being generated" (that
	// message is reserved for an approved-but-still-converging document).
	docBytes := []byte("EXECUTED LEASE — the real bytes a live docGen render would store.")
	const docStoreName = "docStoreCarl"
	exec(`INSERT INTO read_lease_applications (app_id, entity_key, applicant, landlord_decision, signed_at, doc_store_name, doc_filename, doc_content_type, authz_anchors, projection_seq,
	      profile_submitted, missing_onboarding, missing_bgcheck, missing_payment, missing_signature, missing_decision,
	      inflight_bgcheck, inflight_payment, declined_bgcheck, declined_payment, declined)
	      VALUES ('app-C', 'vtx.leaseapp.app-C', 'vtx.identity.`+subCarl+`', 'approved', '2026-07-20T00:00:00Z', $2, 'signed-lease-app-C.txt', 'text/plain; charset=utf-8', $1, 1,
	      false, false, false, false, false, false, false, false, false, false, false)`, []string{subCarl}, docStoreName)
	exec(`INSERT INTO read_lease_applications (app_id, entity_key, applicant, landlord_decision, signed_at, authz_anchors, projection_seq,
	      profile_submitted, missing_onboarding, missing_bgcheck, missing_payment, missing_signature, missing_decision,
	      inflight_bgcheck, inflight_payment, declined_bgcheck, declined_payment, declined)
	      VALUES ('app-D', 'vtx.leaseapp.app-D', 'vtx.identity.`+subDave+`', 'declined', '2026-07-20T00:00:00Z', $1, 1,
	      false, false, false, false, false, false, false, false, false, false, true)`, []string{subDave})
	exec(`INSERT INTO read_lease_applications (app_id, entity_key, applicant, signed_at, authz_anchors, projection_seq,
	      profile_submitted, missing_onboarding, missing_bgcheck, missing_payment, missing_signature, missing_decision,
	      inflight_bgcheck, inflight_payment, declined_bgcheck, declined_payment, declined)
	      VALUES ('app-E', 'vtx.leaseapp.app-E', 'vtx.identity.`+subEve+`', '2026-07-20T00:00:00Z', $1, 1,
	      false, false, false, false, false, true, false, false, false, false, false)`, []string{subEve})
	for _, sub := range []string{subCarl, subDave, subEve} {
		exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
		      VALUES ($1, $1, 'cap-read', 1, false)`, sub)
	}

	// An embedded NATS JetStream server carrying the core-objects bucket, with
	// Carl's executed-lease bytes actually stored under docStoreName — the real
	// byte-streaming path, not a stand-in. This proves the approved+signed+
	// anchored vector really answers 200 with the anchored bytes, not merely
	// "not 404."
	ns := natsfixture.StartServer(t)
	natsCtx, natsCancel := context.WithCancel(context.Background())
	t.Cleanup(natsCancel)
	natsConn, err := substrate.Connect(natsCtx, substrate.ConnectOpts{URL: ns.ClientURL(), Name: "loftspace-app-lease-doc-test"})
	if err != nil {
		t.Fatalf("connect embedded NATS: %v", err)
	}
	t.Cleanup(natsConn.Close)
	if _, err := natsConn.JetStream().CreateOrUpdateObjectStore(natsCtx, natsjetstream.ObjectStoreConfig{
		Bucket: bootstrap.CoreObjectsBucket, Storage: natsjetstream.FileStorage,
	}); err != nil {
		t.Fatalf("create core-objects bucket: %v", err)
	}
	if _, err := natsConn.ObjectPut(natsCtx, bootstrap.CoreObjectsBucket, docStoreName, bytes.NewReader(docBytes), int64(len(docBytes))); err != nil {
		t.Fatalf("put lease document bytes: %v", err)
	}

	// The reader pool runs as the non-superuser role.
	reader := poolInSchema(t, dsn, rlsTestRole)
	defer reader.Close()

	// Defense-in-depth: prove the reader is actually non-superuser — a superuser (or
	// BYPASSRLS) role skips RLS entirely, which would make every "A sees only A"
	// assertion below pass for the wrong reason.
	t.Run("reader role is not a superuser", func(t *testing.T) {
		var isSuper string
		if err := reader.QueryRow(ctx, "SELECT current_setting('is_superuser')").Scan(&isSuper); err != nil {
			t.Fatalf("is_superuser: %v", err)
		}
		if isSuper != "off" {
			t.Fatalf("reader must be non-superuser (else RLS is bypassed), got is_superuser=%s", isSuper)
		}
	})

	// The txn-local actor var must be DISCARDED at COMMIT on the SAME pooled
	// connection — the pooling-safety crux. Set actor=A, read (1 row), commit; then
	// re-query the SAME conn with NO actor set: if the is_local var leaked it would
	// still return A's row. It must return 0 (FORCE RLS + unset → NULL → deny-all).
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
		if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", subAlice); err != nil {
			t.Fatalf("set_config: %v", err)
		}
		var n1 int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM read_lease_applications").Scan(&n1); err != nil {
			t.Fatalf("count in txn: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if n1 != 1 {
			t.Fatalf("inside the txn A must see 1 row, got %d", n1)
		}
		var n2 int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM read_lease_applications").Scan(&n2); err != nil {
			t.Fatalf("count after commit: %v", err)
		}
		if n2 != 0 {
			t.Fatalf("after COMMIT the actor var must be gone (RLS deny-all), got %d rows", n2)
		}
	})

	// The authenticated app: the demo session posture over the shared dev key.
	// s.conn is the embedded-NATS connection carrying Carl's anchored bytes —
	// every OTHER path below that touches NATS is pointer-absent (404 before
	// ObjectGet is ever reached), so wiring it here does not disturb them.
	s, cookieFor := devSessionServer(t, func(s *server) { s.pgPool = reader; s.conn = natsConn })

	get := func(t *testing.T, c *http.Cookie, query string) (int, []protectedApplicationRow) {
		t.Helper()
		rec := sessionGET(s, s.handleApplications, "/api/applications"+query, c)
		var resp struct {
			Applications []protectedApplicationRow `json:"applications"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp.Applications
	}

	t.Run("A sees only A", func(t *testing.T) {
		code, rows := get(t, cookieFor(subAlice), "")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0].EntityKey != "vtx.leaseapp.app-A" {
			t.Fatalf("A must see exactly app-A, got %+v", rows)
		}
		if !rows[0].LandlordApproved {
			t.Errorf("app-A landlord_decision=approved must derive landlordApproved=true")
		}
		if rows[0].UnitBedrooms == nil || *rows[0].UnitBedrooms != 2 {
			t.Errorf("app-A unitBedrooms = %v, want 2", rows[0].UnitBedrooms)
		}
		if rows[0].UnitBathrooms == nil || *rows[0].UnitBathrooms != 1 {
			t.Errorf("app-A unitBathrooms = %v, want 1", rows[0].UnitBathrooms)
		}
		if rows[0].UnitAvailableFrom == nil || *rows[0].UnitAvailableFrom != "2026-08-01" {
			t.Errorf("app-A unitAvailableFrom = %v, want 2026-08-01", rows[0].UnitAvailableFrom)
		}
	})

	t.Run("B sees only B", func(t *testing.T) {
		code, rows := get(t, cookieFor(subBob), "")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0].EntityKey != "vtx.leaseapp.app-B" {
			t.Fatalf("B must see exactly app-B, got %+v", rows)
		}
	})

	t.Run("?applicant=B while authed as A is defeated", func(t *testing.T) {
		// The forgeable client filter is gone; RLS keys off the verified session
		// var, so the query param does nothing — A still sees only A.
		code, rows := get(t, cookieFor(subAlice), "?applicant=vtx.identity."+subBob)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0].EntityKey != "vtx.leaseapp.app-A" {
			t.Fatalf("?applicant=B must NOT leak B to A, got %+v", rows)
		}
	})

	t.Run("unauthenticated is 401", func(t *testing.T) {
		if code, _ := get(t, nil, ""); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("forged cookie is 401", func(t *testing.T) {
		forged := &http.Cookie{Name: s.session.CookieName(), Value: "not.a.jwt"}
		if code, _ := get(t, forged, ""); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("revoked grant hides the row", func(t *testing.T) {
		// Soft-tombstone A's grant: the policy filters NOT is_deleted, so A now
		// sees nothing — the RLS path honors revocation.
		exec("UPDATE actor_read_grants SET is_deleted = true WHERE actor_id = $1", subAlice)
		defer exec("UPDATE actor_read_grants SET is_deleted = false WHERE actor_id = $1", subAlice)
		code, rows := get(t, cookieFor(subAlice), "")
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
			_, rowsA := get(t, cookieFor(subAlice), "")
			if len(rowsA) != 1 || rowsA[0].EntityKey != "vtx.leaseapp.app-A" {
				t.Fatalf("iter %d: A leaked %+v", i, rowsA)
			}
			_, rowsB := get(t, cookieFor(subBob), "")
			if len(rowsB) != 1 || rowsB[0].EntityKey != "vtx.leaseapp.app-B" {
				t.Fatalf("iter %d: B leaked %+v", i, rowsB)
			}
		}
	})

	// The executed-lease document GET: same RLS-scoped model, same authenticated
	// actor, a different endpoint. The GET streams the ANCHORED artifact by the
	// row's doc pointer columns, off the embedded-NATS s.conn wired above (which
	// carries ONLY Carl's app-C bytes) — every other vector below is decided
	// before ObjectGet is reached (not approved, or no pointer yet), so it never
	// touches NATS at all.
	getDoc := func(t *testing.T, c *http.Cookie, leaseAppKey string) (int, string) {
		t.Helper()
		rec := sessionGET(s, s.handleLeaseDocumentGet, "/api/lease-document?leaseAppKey="+leaseAppKey, c)
		return rec.Code, rec.Body.String()
	}

	t.Run("lease-document: signed but not yet anchored is 404 being-generated", func(t *testing.T) {
		// app-A is signed with NO doc pointers — the convergence window.
		code, body := getDoc(t, cookieFor(subAlice), "vtx.leaseapp.app-A")
		if code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (document converging), body=%s", code, body)
		}
		if !strings.Contains(body, "being generated") {
			t.Fatalf("a signed-but-unanchored application answers the honest async message, got %q", body)
		}
	})

	t.Run("lease-document: A requesting B's key is 404, not leaked", func(t *testing.T) {
		code, body := getDoc(t, cookieFor(subAlice), "vtx.leaseapp.app-B")
		if code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (RLS-hidden, indistinguishable from absent)", code)
		}
		if strings.Contains(body, "being generated") {
			t.Fatalf("an RLS-hidden key must read as absent, never as converging: %q", body)
		}
	})

	t.Run("lease-document: B's application is unsigned, 404", func(t *testing.T) {
		code, body := getDoc(t, cookieFor(subBob), "vtx.leaseapp.app-B")
		if code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (no document for an unsigned application)", code)
		}
		if strings.Contains(body, "being generated") {
			t.Fatalf("an unsigned application is not converging a document: %q", body)
		}
	})

	t.Run("lease-document: unauthenticated is 401", func(t *testing.T) {
		if code, _ := getDoc(t, nil, "vtx.leaseapp.app-A"); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("lease-document: approved + signed + anchored streams 200 with the real bytes", func(t *testing.T) {
		code, body := getDoc(t, cookieFor(subCarl), "vtx.leaseapp.app-C")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", code, body)
		}
		if body != string(docBytes) {
			t.Fatalf("body = %q, want the anchored bytes %q", body, docBytes)
		}
	})

	t.Run("lease-document: declined is 404 not approved", func(t *testing.T) {
		code, body := getDoc(t, cookieFor(subDave), "vtx.leaseapp.app-D")
		if code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (declined)", code)
		}
		if !strings.Contains(body, "not approved") {
			t.Fatalf("a declined application must answer the approval-gate message, got %q", body)
		}
		if strings.Contains(body, "being generated") {
			t.Fatalf("a declined application must never read as converging: %q", body)
		}
	})

	t.Run("lease-document: signed but undecided is 404 not approved (never being-generated)", func(t *testing.T) {
		code, body := getDoc(t, cookieFor(subEve), "vtx.leaseapp.app-E")
		if code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (undecided)", code)
		}
		if !strings.Contains(body, "not approved") {
			t.Fatalf("a signed-but-undecided application must answer the approval-gate message, got %q", body)
		}
		if strings.Contains(body, "being generated") {
			t.Fatalf("undecided must never read as converging (only an APPROVED, still-converging document does): %q", body)
		}
	})
}
