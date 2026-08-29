package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// newResidentsTestConn spins up an embedded JetStream server carrying the
// weaver-targets bucket handleResidents reads — mirrors cmd/wellness-app's
// authz_test.go newTestConn.
func newResidentsTestConn(t *testing.T) *substrate.Conn {
	t.Helper()
	ns := natsfixture.StartServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: ns.ClientURL(), Name: "clinic-app-test"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)
	if _, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: weaverTargetsBucket}); err != nil {
		t.Fatalf("create %s bucket: %v", weaverTargetsBucket, err)
	}
	return conn
}

// putResident seeds one leaseApplicationComplete row.
func putResident(t *testing.T, conn *substrate.Conn, leaseAppKey, applicant string, landlordApproved bool) {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"entityKey": leaseAppKey, "applicant": applicant, "landlordApproved": landlordApproved,
	})
	if err != nil {
		t.Fatalf("marshal resident row: %v", err)
	}
	key := "leaseApplicationComplete." + leaseAppKey
	if _, err := conn.KVPut(context.Background(), weaverTargetsBucket, key, b); err != nil {
		t.Fatalf("KVPut %s: %v", key, err)
	}
}

func decodeResidents(t *testing.T, rec *httptest.ResponseRecorder) []residentRow {
	t.Helper()
	var body struct {
		Residents []residentRow `json:"residents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode residents: %v (body=%s)", err, rec.Body.String())
	}
	return body.Residents
}

// TestHandleResidents_NoSession_401: the endpoint is session-gated, closing
// the original leak's precondition — an unauthenticated caller got the whole
// roster.
func TestHandleResidents_NoSession_401(t *testing.T) {
	s, _ := devSessionServer(t, func(s *server) { s.conn = newResidentsTestConn(t) })
	rec := sessionGET(s, s.handleResidents, "/api/residents", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestHandleResidents_PatientSeesOnlyOwnRow is the headline proof: a bare
// patient session (no workplace anchor) reading /api/residents used to get
// every lease applicant's identity + landlord-approval flag. It must now see
// only the row whose bookerKey is its own identity.
func TestHandleResidents_PatientSeesOnlyOwnRow(t *testing.T) {
	const patient, otherPatient = "Hj4kPmRtw9nbCxz5vQ2y", "Kx8mNqTwZ4bRvL2yDcHf"
	gwURL := fakeGatewayActorWorkplaces(t, nil, nil) // no workplace, no operator role
	conn := newResidentsTestConn(t)
	putResident(t, conn, "vtx.leaseapp.a", "vtx.identity."+patient, true)
	putResident(t, conn, "vtx.leaseapp.b", "vtx.identity."+otherPatient, false)

	s, cookieFor := devSessionServer(t, func(s *server) { s.gatewayURL = gwURL; s.conn = conn })
	rec := sessionGET(s, s.handleResidents, "/api/residents", cookieFor(patient))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	rows := decodeResidents(t, rec)
	if len(rows) != 1 || rows[0].BookerKey != "vtx.identity."+patient {
		t.Fatalf("expected only the caller's own row, got %+v", rows)
	}
}

// TestHandleResidents_StaffSeesFullRoster: front-desk booking on behalf of
// any patient needs to resolve ANY patient's residency, so a worksAt-anchored
// session sees the whole roster — the same "front-desk's view" grant
// patients.go's protected read already describes.
func TestHandleResidents_StaffSeesFullRoster(t *testing.T) {
	const staff, patientA, patientB = "Hj4kPmRtw9nbCxz5vQ2y", "Kx8mNqTwZ4bRvL2yDcHf", "Wp6rTzYb3nMkVjD9sLqe"
	gwURL := fakeGatewayActorWorkplaces(t, map[string][]string{staff: {staffWorkplace}}, nil)
	conn := newResidentsTestConn(t)
	putResident(t, conn, "vtx.leaseapp.a", "vtx.identity."+patientA, true)
	putResident(t, conn, "vtx.leaseapp.b", "vtx.identity."+patientB, false)

	s, cookieFor := devSessionServer(t, func(s *server) { s.gatewayURL = gwURL; s.conn = conn })
	rec := sessionGET(s, s.handleResidents, "/api/residents", cookieFor(staff))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	rows := decodeResidents(t, rec)
	if len(rows) != 2 {
		t.Fatalf("expected the full 2-row roster for staff, got %+v", rows)
	}
}

// TestHandleResidents_OperatorSeesFullRoster: the operator role, with no
// workplace anchor at all, is exempted the same way the write-side
// confinement check exempts it.
func TestHandleResidents_OperatorSeesFullRoster(t *testing.T) {
	testutil.EnsurePrimordials(t)
	if bootstrap.RoleOperatorKey == "" {
		t.Fatal("primordial ids loaded but the operator role key is empty")
	}
	const root, patientA, patientB = "Hj4kPmRtw9nbCxz5vQ2y", "Kx8mNqTwZ4bRvL2yDcHf", "Wp6rTzYb3nMkVjD9sLqe"
	gwURL := fakeGatewayActorWorkplaces(t, nil, map[string]bool{root: true})
	conn := newResidentsTestConn(t)
	putResident(t, conn, "vtx.leaseapp.a", "vtx.identity."+patientA, true)
	putResident(t, conn, "vtx.leaseapp.b", "vtx.identity."+patientB, false)

	s, cookieFor := devSessionServer(t, func(s *server) { s.gatewayURL = gwURL; s.conn = conn })
	rec := sessionGET(s, s.handleResidents, "/api/residents", cookieFor(root))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	rows := decodeResidents(t, rec)
	if len(rows) != 2 {
		t.Fatalf("expected the full 2-row roster for the operator, got %+v", rows)
	}
}

func TestComputeResidents_FiltersPrefixSortsAndSkips(t *testing.T) {
	keys, get := fakeKV(map[string]any{
		"leaseApplicationComplete.b": map[string]any{
			"entityKey": "vtx.leaseapp.b", "applicant": "vtx.identity.bob", "landlordApproved": true,
		},
		"leaseApplicationComplete.a": map[string]any{
			"entityKey": "vtx.leaseapp.a", "applicant": "vtx.identity.alice", "landlordApproved": false,
		},
		// no applicant yet (projection hasn't reached that stage) — skipped
		"leaseApplicationComplete.pending": map[string]any{"entityKey": "vtx.leaseapp.pending"},
		// a row from a different convergence lens sharing the bucket — skipped by prefix
		"cafeTabSettlement.x": map[string]any{"entityKey": "vtx.leaseapp.c", "applicant": "vtx.identity.carol"},
	})

	rows := computeResidents(keys, get)
	if len(rows) != 2 {
		t.Fatalf("expected 2 residents (pending + wrong-prefix rows skipped), got %d (%+v)", len(rows), rows)
	}
	if rows[0].BookerKey != "vtx.identity.alice" || rows[1].BookerKey != "vtx.identity.bob" {
		t.Fatalf("residents not sorted by bookerKey: %+v", rows)
	}
	if rows[1].LeaseAppKey != "vtx.leaseapp.b" || !rows[1].Approved {
		t.Fatalf("unexpected resident row: %+v", rows[1])
	}
}
