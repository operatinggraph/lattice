package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	natsjetstream "github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/substrate"
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
	subOwen  = "OOOOOOOOOOOOOOOOOOOO"
	subPeg   = "PPPPPPPPPPPPPPPPPPPP"
	// buildingRiverside is a BUILDING anchor shared by every co-manager row on
	// unit-CO3 (verticals.md: "12 Riverside Walk renders leaseapp …7×") — not a
	// landlord identity. unit-CO's Larry/Linda rows above carry only each
	// landlord's own self-anchor, so that fixture cannot reproduce the live fan-out;
	// this one grants Peg read access to the BUILDING anchor itself, the shape
	// that actually triggered it.
	buildingRiverside = "RRRRRRRRRRRRRRRRRRRR"
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
	body := landlordProtectedColumns()
	// The COMPOSITE key — this is what lets a co-managed unit's application carry one
	// row per landlord without a primary-key collision.
	ddl, err := adapter.BuildProtectedTableDDL("read_landlord_lease_applications", []string{"app_id", "landlord_id"}, body)
	if err != nil {
		t.Fatalf("build protected DDL: %v", err)
	}
	for _, stmt := range ddl {
		exec(stmt)
	}

	// The lease-document GET reads read_lease_applications FIRST, falling back
	// to this landlord table only when that read finds no row (the "landlord
	// reads a document off their own landlord-scoped row" vectors below
	// exercise exactly that fallback) — the handler always queries it, so it
	// must exist even with zero rows for tests that don't seed it.
	applicantDDL, err := adapter.BuildProtectedTableDDL("read_lease_applications", []string{"app_id"}, applicantProtectedColumns())
	if err != nil {
		t.Fatalf("build applicant protected DDL: %v", err)
	}
	for _, stmt := range applicantDDL {
		exec(stmt)
	}

	_, _ = owner.Exec(ctx, "DROP OWNED BY "+rlsTestRole+" CASCADE")
	_, _ = owner.Exec(ctx, "DROP ROLE IF EXISTS "+rlsTestRole)
	exec("CREATE ROLE " + rlsTestRole + " NOSUPERUSER NOLOGIN")
	exec("GRANT USAGE ON SCHEMA " + rlsTestSchema + " TO " + rlsTestRole)
	exec("GRANT SELECT ON " + rlsTestSchema + ".read_landlord_lease_applications TO " + rlsTestRole)
	exec("GRANT SELECT ON " + rlsTestSchema + ".read_lease_applications TO " + rlsTestRole)
	exec("GRANT SELECT ON " + rlsTestSchema + ".actor_read_grants TO " + rlsTestRole)

	// Seed:
	//   - app-L on unit-L, managed by Larry           (anchor Larry)
	//   - app-N on unit-N, managed by Linda           (anchor Linda)
	//   - app-CO on unit-CO, CO-MANAGED by both        (two rows: anchor Larry / anchor Linda)
	insRowAnchors := func(appID, landlordID, entityKey, applicantID, landlordKey, unitKey string, anchors []string) {
		exec(`INSERT INTO read_landlord_lease_applications
		      (app_id, landlord_id, entity_key, applicant, landlord_key, unit_key, authz_anchors, projection_seq)
		      VALUES ($1, $2, $3, $4, $5, $6, $7, 1)`,
			appID, landlordID, entityKey, applicantID, landlordKey, unitKey, anchors)
	}
	insRow := func(appID, landlordID, entityKey, applicantID, landlordKey, unitKey string, anchor string) {
		insRowAnchors(appID, landlordID, entityKey, applicantID, landlordKey, unitKey, []string{anchor})
	}
	insRow("app-L", subLarry, "vtx.leaseapp.app-L", "vtx.identity."+subAlice, "vtx.identity."+subLarry, "vtx.unit.unit-L", subLarry)
	insRow("app-N", subLinda, "vtx.leaseapp.app-N", "vtx.identity."+subBob, "vtx.identity."+subLinda, "vtx.unit.unit-N", subLinda)
	insRow("app-CO", subLarry, "vtx.leaseapp.app-CO", "vtx.identity."+subAlice, "vtx.identity."+subLarry, "vtx.unit.unit-CO", subLarry)
	insRow("app-CO", subLinda, "vtx.leaseapp.app-CO", "vtx.identity."+subAlice, "vtx.identity."+subLinda, "vtx.unit.unit-CO", subLinda)

	// app-L carries decrypted Secure-Lens contact values (what the projection
	// writes post-decrypt); every other row leaves them NULL — an applicant with
	// no recorded contact aspects or a crypto-shredded one. Both shapes must
	// scan and serve. app-L is also QUALIFIED (D1.5 Rec-C remainder); app-CO is
	// left at the column default (NULL, COALESCEd to false) so the round-trip
	// proves both a true value and the false default scan/serve correctly.
	exec(`UPDATE read_landlord_lease_applications
	      SET applicant_name=$1, applicant_email=$2, applicant_phone=$3, qualified=true
	      WHERE app_id='app-L'`, "Alice Applicant", "alice@example.com", "+15550001111")

	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subLarry)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, subLinda)

	const landlordDocStoreName = "docStoreLandlordFallback"
	landlordDocBytes := []byte("EXECUTED LEASE — landlord-fallback bytes.")

	// An embedded NATS JetStream server carrying app-F's anchored bytes (seeded
	// below, near the lease-document sub-tests, so it does not inflate Larry's
	// application count for the assertions that run before it), proving the
	// landlord-fallback 200 is a real byte stream, not merely "not 404."
	ns := natsfixture.StartServer(t)
	natsCtx, natsCancel := context.WithCancel(context.Background())
	t.Cleanup(natsCancel)
	natsConn, err := substrate.Connect(natsCtx, substrate.ConnectOpts{URL: ns.ClientURL(), Name: "loftspace-app-landlord-lease-doc-test"})
	if err != nil {
		t.Fatalf("connect embedded NATS: %v", err)
	}
	t.Cleanup(natsConn.Close)
	if _, err := natsConn.JetStream().CreateOrUpdateObjectStore(natsCtx, natsjetstream.ObjectStoreConfig{
		Bucket: bootstrap.CoreObjectsBucket, Storage: natsjetstream.FileStorage,
	}); err != nil {
		t.Fatalf("create core-objects bucket: %v", err)
	}
	if _, err := natsConn.ObjectPut(natsCtx, bootstrap.CoreObjectsBucket, landlordDocStoreName, bytes.NewReader(landlordDocBytes), int64(len(landlordDocBytes))); err != nil {
		t.Fatalf("put landlord-fallback lease document bytes: %v", err)
	}

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

	s, cookieFor := devSessionServer(t, func(s *server) { s.pgPool = reader; s.conn = natsConn })

	getDoc := func(t *testing.T, c *http.Cookie, leaseAppKey string) (int, string) {
		t.Helper()
		rec := sessionGET(s, s.handleLeaseDocumentGet, "/api/lease-document?leaseAppKey="+leaseAppKey, c)
		return rec.Code, rec.Body.String()
	}

	type unitGroup struct {
		UnitKey      string                 `json:"unitKey"`
		Applications []protectedLandlordRow `json:"applications"`
	}
	get := func(t *testing.T, c *http.Cookie, query string) (int, []unitGroup, int) {
		t.Helper()
		rec := sessionGET(s, s.handleLandlordApplications, "/api/landlord/applications"+query, c)
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
		code, units, appCount := get(t, cookieFor(subLarry), "")
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

	t.Run("secure contact columns serve to the managing landlord, null-safe", func(t *testing.T) {
		code, units, _ := get(t, cookieFor(subLarry), "")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		var withContact, nullContact *protectedLandlordRow
		for i := range units {
			for j := range units[i].Applications {
				a := &units[i].Applications[j]
				switch a.EntityKey {
				case "vtx.leaseapp.app-L":
					withContact = a
				case "vtx.leaseapp.app-CO":
					nullContact = a
				}
			}
		}
		if withContact == nil || nullContact == nil {
			t.Fatalf("expected both app-L and app-CO in Larry's scope")
		}
		if withContact.ApplicantName == nil || *withContact.ApplicantName != "Alice Applicant" {
			t.Errorf("applicantName = %v, want Alice Applicant", withContact.ApplicantName)
		}
		if withContact.ApplicantEmail == nil || *withContact.ApplicantEmail != "alice@example.com" {
			t.Errorf("applicantEmail = %v, want alice@example.com", withContact.ApplicantEmail)
		}
		if withContact.ApplicantPhone == nil || *withContact.ApplicantPhone != "+15550001111" {
			t.Errorf("applicantPhone = %v, want +15550001111", withContact.ApplicantPhone)
		}
		if nullContact.ApplicantName != nil || nullContact.ApplicantEmail != nil || nullContact.ApplicantPhone != nil {
			t.Errorf("a contactless/shredded applicant's columns must stay null, got %v/%v/%v",
				nullContact.ApplicantName, nullContact.ApplicantEmail, nullContact.ApplicantPhone)
		}
		if !withContact.Qualified {
			t.Errorf("app-L's qualified=true must round-trip through the real Postgres scan, got false")
		}
		if nullContact.Qualified {
			t.Errorf("app-CO's qualified column default (NULL -> COALESCE false) must round-trip false, got true")
		}
	})

	t.Run("Linda sees only her units (unit-N + the co-managed unit-CO)", func(t *testing.T) {
		code, units, appCount := get(t, cookieFor(subLinda), "")
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
		code, units, appCount := get(t, cookieFor(subAlice), "")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(units) != 0 || appCount != 0 {
			t.Fatalf("a non-landlord must see no units/applications, got units=%v appCount=%d", units, appCount)
		}
	})

	t.Run("a forged scope query param cannot widen", func(t *testing.T) {
		// RLS keys off the verified session var, not any param — Larry stays scoped.
		code, units, _ := get(t, cookieFor(subLarry), "?landlord=vtx.identity."+subLinda)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if _, ok := unitKeys(units)["vtx.unit.unit-N"]; ok {
			t.Fatalf("a ?landlord= param must NOT leak Linda's unit to Larry, got %v", unitKeys(units))
		}
	})

	t.Run("unauthenticated is 401", func(t *testing.T) {
		if code, _, _ := get(t, nil, ""); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("forged cookie is 401", func(t *testing.T) {
		forged := &http.Cookie{Name: s.session.CookieName(), Value: "not.a.jwt"}
		if code, _, _ := get(t, forged, ""); code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", code)
		}
	})

	t.Run("revoked grant hides the landlord's units", func(t *testing.T) {
		exec("UPDATE actor_read_grants SET is_deleted = true WHERE actor_id = $1", subLarry)
		defer exec("UPDATE actor_read_grants SET is_deleted = false WHERE actor_id = $1", subLarry)
		code, units, appCount := get(t, cookieFor(subLarry), "")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(units) != 0 || appCount != 0 {
			t.Fatalf("a revoked grant must hide everything, got units=%v appCount=%d", units, appCount)
		}
	})

	t.Run("a building-anchored reader sees ONE row per co-managed application, not one per co-manager", func(t *testing.T) {
		// Reproduces the live fan-out (verticals.md): three co-managing landlords'
		// rows for the SAME application, each carrying the shared building anchor
		// alongside their own — then a reader granted the BUILDING anchor (not any
		// landlord's own identity) matches all three rows' authz_anchors.
		insRowAnchors("app-CO3", subLarry, "vtx.leaseapp.app-CO3", "vtx.identity."+subAlice,
			"vtx.identity."+subLarry, "vtx.unit.unit-CO3", []string{subLarry, buildingRiverside})
		insRowAnchors("app-CO3", subLinda, "vtx.leaseapp.app-CO3", "vtx.identity."+subAlice,
			"vtx.identity."+subLinda, "vtx.unit.unit-CO3", []string{subLinda, buildingRiverside})
		insRowAnchors("app-CO3", subOwen, "vtx.leaseapp.app-CO3", "vtx.identity."+subAlice,
			"vtx.identity."+subOwen, "vtx.unit.unit-CO3", []string{subOwen, buildingRiverside})
		defer exec("DELETE FROM read_landlord_lease_applications WHERE app_id = 'app-CO3'")

		exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
		      VALUES ($1, $2, 'cap-read', 1, false)`, subPeg, buildingRiverside)
		defer exec("DELETE FROM actor_read_grants WHERE actor_id = $1", subPeg)

		code, units, appCount := get(t, cookieFor(subPeg), "")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if appCount != 1 {
			t.Fatalf("Peg's building-anchor grant matches 3 co-manager rows for ONE application; appCount = %d, want 1", appCount)
		}
		ks := unitKeys(units)
		if n := ks["vtx.unit.unit-CO3"]; n != 1 {
			t.Fatalf("unit-CO3 must render its ONE application once, not once per co-manager; got %d", n)
		}
	})

	t.Run("pooling safety: requests do not leak the actor var across the pool", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			_, _, larryCount := get(t, cookieFor(subLarry), "")
			if larryCount != 2 {
				t.Fatalf("iter %d: Larry leaked/lost rows, appCount=%d", i, larryCount)
			}
			_, _, lindaCount := get(t, cookieFor(subLinda), "")
			if lindaCount != 2 {
				t.Fatalf("iter %d: Linda leaked/lost rows, appCount=%d", i, lindaCount)
			}
		}
	})

	// app-F / app-G exist ONLY in the landlord table (no read_lease_applications
	// row at all) — the lease-document GET's landlord-scope fallback vectors:
	// Larry reads the executed lease off his own landlord-anchored row when he
	// is not the applicant, and a declined one still gates on approval there.
	// Seeded here, after every fixed-appCount assertion above, so these two
	// extra applications never inflate Larry's counted totals.
	insRow("app-F", subLarry, "vtx.leaseapp.app-F", "vtx.identity."+subAlice, "vtx.identity."+subLarry, "vtx.unit.unit-F", subLarry)
	insRow("app-G", subLarry, "vtx.leaseapp.app-G", "vtx.identity."+subAlice, "vtx.identity."+subLarry, "vtx.unit.unit-G", subLarry)
	exec(`UPDATE read_landlord_lease_applications
	      SET landlord_decision='approved', signed_at='2026-07-25T00:00:00Z',
	          doc_store_name=$1, doc_filename='signed-lease-app-F.txt', doc_content_type='text/plain; charset=utf-8'
	      WHERE app_id='app-F'`, landlordDocStoreName)
	exec(`UPDATE read_landlord_lease_applications
	      SET landlord_decision='declined', signed_at='2026-07-25T00:00:00Z'
	      WHERE app_id='app-G'`)

	t.Run("lease-document: landlord fallback streams 200 off the landlord-scoped row", func(t *testing.T) {
		code, body := getDoc(t, cookieFor(subLarry), "vtx.leaseapp.app-F")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", code, body)
		}
		if body != string(landlordDocBytes) {
			t.Fatalf("body = %q, want the anchored bytes %q", body, landlordDocBytes)
		}
	})

	t.Run("lease-document: landlord-scoped row declined is 404 not approved", func(t *testing.T) {
		code, body := getDoc(t, cookieFor(subLarry), "vtx.leaseapp.app-G")
		if code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (declined)", code)
		}
		if !strings.Contains(body, "not approved") {
			t.Fatalf("a declined landlord-scoped application must answer the approval-gate message, got %q", body)
		}
	})

	t.Run("lease-document: a non-managing actor cannot reach the landlord fallback", func(t *testing.T) {
		code, body := getDoc(t, cookieFor(subLinda), "vtx.leaseapp.app-F")
		if code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (RLS-hidden, not Larry's manager)", code)
		}
		if strings.Contains(body, "being generated") {
			t.Fatalf("an RLS-hidden key must read as absent, never as converging: %q", body)
		}
	})
}
