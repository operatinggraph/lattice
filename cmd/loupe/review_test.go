package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/processor/opwire"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/packages/augur"
	capabilityauthor "github.com/operatinggraph/lattice/packages/capability-author"
)

func TestCapabilityProposalIDFromKey(t *testing.T) {
	cases := []struct {
		key    string
		wantID string
		wantOK bool
	}{
		{"vtx.capabilityproposal.abc123", "abc123", true},
		{"vtx.capabilityproposal.", "", false},
		{"vtx.capabilityproposal.abc.def", "", false}, // a dotted tail is never a bare NanoID
		{"vtx.identity.abc123", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		id, ok := capabilityProposalIDFromKey(c.key)
		if id != c.wantID || ok != c.wantOK {
			t.Errorf("capabilityProposalIDFromKey(%q) = (%q, %v), want (%q, %v)", c.key, id, ok, c.wantID, c.wantOK)
		}
	}
}

func TestDecodeCapabilityProposalCols(t *testing.T) {
	if _, ok := decodeCapabilityProposalCols([]byte(`not json`)); ok {
		t.Error("malformed JSON should not decode")
	}
	if _, ok := decodeCapabilityProposalCols([]byte(`{"intent":"no key field"}`)); ok {
		t.Error("a row missing key should not decode (poison entry)")
	}
	cols, ok := decodeCapabilityProposalCols([]byte(`{"key":"vtx.capabilityproposal.a1","intent":"list active providers","reviewState":"pending","confidence":0.86}`))
	if !ok {
		t.Fatal("well-formed row should decode")
	}
	if cols.Intent != "list active providers" || cols.ReviewState != "pending" || cols.Confidence != 0.86 {
		t.Errorf("decoded cols = %+v", cols)
	}
}

func TestComputeCapabilityProposals(t *testing.T) {
	store := map[string][]byte{
		"vtx.capabilityproposal.bbb2":   []byte(`{"key":"vtx.capabilityproposal.bbb2","intent":"b","reviewState":"pending"}`),
		"vtx.capabilityproposal.aaa1":   []byte(`{"key":"vtx.capabilityproposal.aaa1","intent":"a","reviewState":"approved"}`),
		"vtx.capabilityproposal.poison": []byte(`not json`),
		"vtx.capabilityproposal.":       []byte(`{"key":"vtx.capabilityproposal.","intent":"no id"}`), // decodes but ID extraction fails
	}
	get := func(key string) ([]byte, bool) { b, ok := store[key]; return b, ok }
	keys := make([]string, 0, len(store))
	for k := range store {
		keys = append(keys, k)
	}

	rows := computeCapabilityProposals(keys, get)
	if len(rows) != 2 {
		t.Fatalf("want 2 well-formed rows (poison + no-id skipped), got %d: %+v", len(rows), rows)
	}
	// Key-sorted (aaa1 before bbb2) — the display sort is the JS logic tier's job.
	if rows[0].ProposalID != "aaa1" || rows[1].ProposalID != "bbb2" {
		t.Errorf("want key-sorted [aaa1, bbb2], got [%s, %s]", rows[0].ProposalID, rows[1].ProposalID)
	}
}

// newTestReviewServer spins up an embedded (deterministic, isolated) NATS
// server with both the capability-proposals and augur-proposals buckets
// created, wires it into a server + httptest.Server, and returns the client +
// a bucket-scoped put helper. Mirrors vault_test.go's TestVaultShreds_ListsBucket
// pattern — the shared dev stack doesn't have packages/capability-author or
// packages/augur installed, so this is the only way to exercise the real HTTP
// handler end-to-end.
func newTestReviewServer(t *testing.T) (client *http.Client, baseURL string, put func(bucket, key, value string)) {
	t.Helper()
	_, client, baseURL, put = newTestReviewServerWithSrv(t)
	return client, baseURL, put
}

// newTestReviewServerWithSrv is newTestReviewServer plus the *server itself,
// for the tests that must reach a handler directly — driving a SUCCESSFUL op
// relay needs both a stub gateway on srv.gatewayURL and an operator token in
// the request context, and the token is placed there by requireOperator, which
// this fixture deliberately does not install.
func newTestReviewServerWithSrv(t *testing.T) (srv *server, client *http.Client, baseURL string, put func(bucket, key, value string)) {
	t.Helper()
	ns := natsfixture.StartServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: ns.ClientURL(), Name: "loupe-test"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)
	for _, bucket := range []string{capabilityauthor.CapabilityProposalsBucket, augur.AugurProposalsBucket, bootstrap.CoreKVBucket} {
		if _, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket}); err != nil {
			t.Fatalf("create bucket %s: %v", bucket, err)
		}
	}

	put = func(bucket, key, value string) {
		t.Helper()
		putCtx, putCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer putCancel()
		if _, err := conn.KVPut(putCtx, bucket, key, []byte(value)); err != nil {
			t.Fatalf("put %s/%s: %v", bucket, key, err)
		}
	}

	srv = &server{conn: conn, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), natsTimeout: 5 * time.Second}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	return srv, hs.Client(), hs.URL, put
}

func TestReviewCapabilityQueue_ListsBucket(t *testing.T) {
	client, base, put := newTestReviewServer(t)
	put(capabilityauthor.CapabilityProposalsBucket, "vtx.capabilityproposal.pend1",
		`{"key":"vtx.capabilityproposal.pend1","intent":"list active providers by specialty","kind":"lens",`+
			`"reviewState":"pending","confidence":0.86,"model":"claude","reasonedAt":"2026-07-18T00:00:00Z"}`)
	put(capabilityauthor.CapabilityProposalsBucket, "vtx.capabilityproposal.authoring1",
		`{"key":"vtx.capabilityproposal.authoring1","intent":"reasoning in flight"}`)

	res, err := client.Get(base + "/api/review/capability")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var body struct {
		Proposals []capabilityProposalRow `json:"proposals"`
		Count     int                     `json:"count"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Count != 2 || len(body.Proposals) != 2 {
		t.Fatalf("want 2 proposals, got %+v", body)
	}
	byID := map[string]capabilityProposalRow{}
	for _, p := range body.Proposals {
		byID[p.ProposalID] = p
	}
	if byID["pend1"].Intent != "list active providers by specialty" || byID["pend1"].ReviewState != "pending" {
		t.Errorf("pend1 row = %+v", byID["pend1"])
	}
	if byID["authoring1"].Kind != "" {
		t.Errorf("authoring1 row should have no kind yet (reasoning in flight), got %+v", byID["authoring1"])
	}
}

func TestReviewCapabilityDetail_Found(t *testing.T) {
	client, base, put := newTestReviewServer(t)
	put(capabilityauthor.CapabilityProposalsBucket, "vtx.capabilityproposal.det1",
		`{"key":"vtx.capabilityproposal.det1","intent":"a new lens","kind":"lens","reviewState":"pending",`+
			`"rationale":"no existing lens covers this","confidence":0.72}`)

	res, err := client.Get(base + "/api/review/capability/det1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var row capabilityProposalRow
	if err := json.NewDecoder(res.Body).Decode(&row); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if row.ProposalID != "det1" || row.Rationale != "no existing lens covers this" {
		t.Errorf("row = %+v", row)
	}
}

func TestReviewCapabilityDetail_NotFound(t *testing.T) {
	client, base, _ := newTestReviewServer(t)

	res, err := client.Get(base + "/api/review/capability/doesnotexist")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
}

func TestReviewCapabilityDetail_RejectsDottedID(t *testing.T) {
	client, base, _ := newTestReviewServer(t)

	res, err := client.Get(base + "/api/review/capability/a.b")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (a dotted id is never a valid control name)", res.StatusCode)
	}
}

func TestAugurProposalIDFromKey(t *testing.T) {
	cases := []struct {
		key    string
		wantID string
		wantOK bool
	}{
		{"vtx.augurproposal.abc123", "abc123", true},
		{"vtx.augurproposal.", "", false},
		{"vtx.augurproposal.abc.def", "", false}, // a dotted tail is never a bare handle
		{"vtx.capabilityproposal.abc123", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		id, ok := augurProposalIDFromKey(c.key)
		if id != c.wantID || ok != c.wantOK {
			t.Errorf("augurProposalIDFromKey(%q) = (%q, %v), want (%q, %v)", c.key, id, ok, c.wantID, c.wantOK)
		}
	}
}

func TestDecodeAugurProposalCols(t *testing.T) {
	if _, ok := decodeAugurProposalCols([]byte(`not json`)); ok {
		t.Error("malformed JSON should not decode")
	}
	if _, ok := decodeAugurProposalCols([]byte(`{"gapColumn":"no key field"}`)); ok {
		t.Error("a row missing key should not decode (poison entry)")
	}
	cols, ok := decodeAugurProposalCols([]byte(`{"key":"vtx.augurproposal.a1","gapColumn":"missing_approval","reviewState":"pending","confidence":0.82}`))
	if !ok {
		t.Fatal("well-formed row should decode")
	}
	if cols.GapColumn != "missing_approval" || cols.ReviewState != "pending" || cols.Confidence != 0.82 {
		t.Errorf("decoded cols = %+v", cols)
	}
}

func TestComputeAugurProposals(t *testing.T) {
	store := map[string][]byte{
		"vtx.augurproposal.bbb2":   []byte(`{"key":"vtx.augurproposal.bbb2","gapColumn":"b","reviewState":"pending"}`),
		"vtx.augurproposal.aaa1":   []byte(`{"key":"vtx.augurproposal.aaa1","gapColumn":"a","reviewState":"approved"}`),
		"vtx.augurproposal.poison": []byte(`not json`),
		"vtx.augurproposal.":       []byte(`{"key":"vtx.augurproposal.","gapColumn":"no id"}`), // decodes but ID extraction fails
	}
	get := func(key string) ([]byte, bool) { b, ok := store[key]; return b, ok }
	keys := make([]string, 0, len(store))
	for k := range store {
		keys = append(keys, k)
	}

	rows := computeAugurProposals(keys, get)
	if len(rows) != 2 {
		t.Fatalf("want 2 well-formed rows (poison + no-id skipped), got %d: %+v", len(rows), rows)
	}
	if rows[0].ProposalID != "aaa1" || rows[1].ProposalID != "bbb2" {
		t.Errorf("want key-sorted [aaa1, bbb2], got [%s, %s]", rows[0].ProposalID, rows[1].ProposalID)
	}
}

func TestReviewAugurQueue_ListsBucket(t *testing.T) {
	client, base, put := newTestReviewServer(t)
	put(augur.AugurProposalsBucket, "vtx.augurproposal.pend1",
		`{"key":"vtx.augurproposal.pend1","gapColumn":"missing_approval","entityId":"vtx.leaseapp.abc","`+
			`proposedAction":"assignTask","reviewState":"pending","confidence":0.82,"model":"claude","reasonedAt":"2026-07-18T00:00:00Z"}`)
	put(augur.AugurProposalsBucket, "vtx.augurproposal.authoring1",
		`{"key":"vtx.augurproposal.authoring1","gapColumn":"missing_bgcheck"}`)

	res, err := client.Get(base + "/api/review/augur")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var body struct {
		Proposals []augurProposalRow `json:"proposals"`
		Count     int                `json:"count"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Count != 2 || len(body.Proposals) != 2 {
		t.Fatalf("want 2 proposals, got %+v", body)
	}
	byID := map[string]augurProposalRow{}
	for _, p := range body.Proposals {
		byID[p.ProposalID] = p
	}
	if byID["pend1"].GapColumn != "missing_approval" || byID["pend1"].ReviewState != "pending" {
		t.Errorf("pend1 row = %+v", byID["pend1"])
	}
	if byID["authoring1"].ProposedAction != "" {
		t.Errorf("authoring1 row should have no proposedAction yet (reasoning in flight), got %+v", byID["authoring1"])
	}
}

func TestReviewAugurDetail_Found(t *testing.T) {
	client, base, put := newTestReviewServer(t)
	put(augur.AugurProposalsBucket, "vtx.augurproposal.det1",
		`{"key":"vtx.augurproposal.det1","gapColumn":"missing_approval","reviewState":"pending",`+
			`"rationale":"no playbook entry","confidence":0.72}`)

	res, err := client.Get(base + "/api/review/augur/det1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var row augurProposalRow
	if err := json.NewDecoder(res.Body).Decode(&row); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if row.ProposalID != "det1" || row.Rationale != "no playbook entry" {
		t.Errorf("row = %+v", row)
	}
}

func TestReviewAugurDetail_NotFound(t *testing.T) {
	client, base, _ := newTestReviewServer(t)

	res, err := client.Get(base + "/api/review/augur/doesnotexist")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
}

func TestReviewAugurDetail_RejectsDottedID(t *testing.T) {
	client, base, _ := newTestReviewServer(t)

	res, err := client.Get(base + "/api/review/augur/a.b")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (a dotted id is never a valid control name)", res.StatusCode)
	}
}

func TestHandleReview_RoutingErrors(t *testing.T) {
	client, base, _ := newTestReviewServer(t)

	cases := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodPost, "/api/review/capability", http.StatusBadRequest},
		{http.MethodPost, "/api/review/augur", http.StatusBadRequest},
		{http.MethodGet, "/api/review/bogus", http.StatusBadRequest},
		{http.MethodGet, "/api/review/", http.StatusBadRequest},
		{http.MethodGet, "/api/review/capability/a/b", http.StatusBadRequest},
		{http.MethodGet, "/api/review/augur/a/b", http.StatusBadRequest},
		// F16.2 action endpoints: POST-only, capability-only, known verbs only.
		{http.MethodGet, "/api/review/capability/x/approve", http.StatusBadRequest}, // GET on a POST endpoint
		{http.MethodPost, "/api/review/augur/x/approve", http.StatusBadRequest},     // augur has no approve endpoint
		{http.MethodPost, "/api/review/augur/x/apply", http.StatusBadRequest},       // augur has no apply endpoint
		{http.MethodPost, "/api/review/capability/x/bogus", http.StatusBadRequest},  // unknown verb
		{http.MethodGet, "/api/review/capability/x/mark-applied", http.StatusBadRequest},
		{http.MethodPost, "/api/review/augur/x/mark-applied", http.StatusBadRequest},
	}
	for _, c := range cases {
		req, err := http.NewRequest(c.method, base+c.path, nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		res.Body.Close()
		if res.StatusCode != c.want {
			t.Errorf("%s %s: status = %d, want %d", c.method, c.path, res.StatusCode, c.want)
		}
	}
}

// validLensContent is a capability-artifact "content" payload (a JSON string
// per the DDL) that ValidateCapabilityArtifact("lens", …) accepts with no live
// substrate read — mirrors internal/pkgmgr's TestValidateCapabilityArtifact_ValidLens.
const validLensContent = `{"canonicalName":"activeProvidersBySpecialty","adapter":"nats-kv","bucket":"active-providers","spec":"MATCH (p:provider) RETURN p.key AS key"}`

// invalidLensContent parses as JSON but fails §5 validation (unparseable
// cypher) — the re-validation "blocked" path, no error, report.Valid=false.
const invalidLensContent = `{"canonicalName":"brokenLens","adapter":"nats-kv","bucket":"broken-lens","spec":"MATCH (p:provider RETURN p.key AS key"}`

// putCapProposal writes a capability-proposals read-model row from a field map,
// json-encoding it so a content field carrying its own JSON needs no manual
// escaping.
func putCapProposal(t *testing.T, put func(bucket, key, value string), id string, fields map[string]any) {
	t.Helper()
	key := "vtx.capabilityproposal." + id
	fields["key"] = key
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal proposal %s: %v", id, err)
	}
	put(capabilityauthor.CapabilityProposalsBucket, key, string(raw))
}

func postReview(t *testing.T, client *http.Client, base, path string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	res.Body.Close()
	return res, body
}

func TestReviewCapabilityApprove_BlockedOnRevalidation(t *testing.T) {
	client, base, put := newTestReviewServer(t)
	putCapProposal(t, put, "blk1", map[string]any{
		"intent": "a lens that no longer validates", "kind": "lens",
		"content": invalidLensContent, "reviewState": "pending",
	})

	res, body := postReview(t, client, base, "/api/review/capability/blk1/approve")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (blocked is a 200 with a blocked flag)", res.StatusCode)
	}
	if body["blocked"] != true || body["validationState"] != "invalid" {
		t.Errorf("want blocked:true + validationState:invalid, got %+v", body)
	}
}

func TestReviewCapabilityApprove_ValidReachesGatewaySubmit(t *testing.T) {
	client, base, put := newTestReviewServer(t)
	putCapProposal(t, put, "ok1", map[string]any{
		"intent": "a valid lens", "kind": "lens",
		"content": validLensContent, "reviewState": "pending",
	})

	// The test server has no gatewayURL/operator token, so a proposal that
	// PASSES re-validation proceeds to the Gateway relay and fails there — a
	// 502 whose message proves re-validation was cleared and the submit was
	// attempted (the live Gateway path itself is the F16.1-shipped op relay).
	res, body := postReview(t, client, base, "/api/review/capability/ok1/approve")
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (valid → attempts gateway submit)", res.StatusCode)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "submit approve") {
		t.Errorf("want a submit-approve gateway error, got %+v", body)
	}
}

func TestReviewCapabilityApprove_NotPending(t *testing.T) {
	client, base, put := newTestReviewServer(t)
	putCapProposal(t, put, "appr1", map[string]any{
		"intent": "already approved", "kind": "lens",
		"content": validLensContent, "reviewState": "approved",
	})

	res, _ := postReview(t, client, base, "/api/review/capability/appr1/approve")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (only a pending proposal is approvable)", res.StatusCode)
	}
}

func TestReviewCapabilityApprove_NoArtifact(t *testing.T) {
	client, base, put := newTestReviewServer(t)
	// Pending but reasoning still in flight — no kind/content recorded yet.
	putCapProposal(t, put, "flight1", map[string]any{
		"intent": "reasoning in flight", "reviewState": "pending",
	})

	res, _ := postReview(t, client, base, "/api/review/capability/flight1/approve")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (no artifact to re-validate yet)", res.StatusCode)
	}
}

func TestReviewCapabilityApprove_NotFound(t *testing.T) {
	client, base, _ := newTestReviewServer(t)
	res, _ := postReview(t, client, base, "/api/review/capability/missing1/approve")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
}

func TestReviewCapabilityApprove_RejectsDottedID(t *testing.T) {
	client, base, _ := newTestReviewServer(t)
	res, _ := postReview(t, client, base, "/api/review/capability/a.b/approve")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (a dotted id is never a valid control name)", res.StatusCode)
	}
}

func TestReviewCapabilityApply_NoAdminActor(t *testing.T) {
	client, base, put := newTestReviewServer(t)
	putCapProposal(t, put, "app1", map[string]any{
		"intent": "approved, ready to apply", "kind": "lens",
		"content": validLensContent, "reviewState": "approved",
	})

	// The test server loads no bootstrap file, so adminActor is empty — apply
	// must refuse before touching the installer.
	res, body := postReview(t, client, base, "/api/review/capability/app1/apply")
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (no admin actor loaded)", res.StatusCode)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "admin actor not loaded") {
		t.Errorf("want an admin-actor error, got %+v", body)
	}
}

// The cold-load half of the recovery story: an operator who closed the tab
// after a half-committed apply comes back and clicks Apply. Without the
// resumable marker they get the plan builder's bare refusal and no pointer to
// the control that actually finishes the job — the classifier's one decision,
// on the only path that survives a page reload.
func TestReviewCapabilityApply_AlreadyInstalledIsResumable(t *testing.T) {
	srv, client, base, put := newTestReviewServerWithSrv(t)
	srv.adminActor = "vtx.identity.testAdminHJKMNPQRST"
	putCapProposal(t, put, "resume1", map[string]any{
		"intent": "approved, install already committed", "kind": "lens", "content": validLensContent,
		"reviewState": "approved", "targetMode": "newPackage", "targetPackageName": "alpha",
		"targetNewVersion": "1.2.0",
	})
	putInstalledPackage(t, put, "liveAlpha", "alpha", "1.2.0", false)

	res, body := postReview(t, client, base, "/api/review/capability/resume1/apply")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", res.StatusCode)
	}
	if body["resumable"] != true {
		t.Errorf("want resumable:true so the console can point at the recovery, got %+v", body)
	}
	if body["packageKey"] != "vtx.package.liveAlpha" {
		t.Errorf("packageKey = %v", body["packageKey"])
	}
}

// The ordinary first apply must be untouched by that pre-check: the target is
// approved but nothing is installed, so it proceeds into the plan builder.
func TestReviewCapabilityApply_NotYetInstalledProceeds(t *testing.T) {
	srv, client, base, put := newTestReviewServerWithSrv(t)
	srv.adminActor = "vtx.identity.testAdminHJKMNPQRST"
	putCapProposal(t, put, "fresh1", map[string]any{
		"intent": "approved, never installed", "kind": "lens", "content": validLensContent,
		"reviewState": "approved", "targetMode": "newPackage", "targetPackageName": "alpha",
		"targetNewVersion": "1.2.0",
	})

	res, body := postReview(t, client, base, "/api/review/capability/fresh1/apply")
	if body["resumable"] == true {
		t.Fatalf("a never-installed target was classified resumable: %+v", body)
	}
	// It reaches CapabilityApplyPlanForProposal, which reads the proposal's
	// aspects from Core KV — absent in this fixture, so it refuses there.
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 from the plan builder", res.StatusCode)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "build apply plan") {
		t.Errorf("want the plan-builder refusal, got %+v", body)
	}
}

// The console must not be the way around the plan builder's deny-list. An
// AI-authored proposal need only declare target.newVersion equal to the
// version a platform package is ALREADY at (trivially guessable) to route
// apply into the resumable branch, which never calls
// CapabilityApplyPlanForProposal at all and instead tells the operator to
// finish via mark-applied. The protected check has to land first.
func TestReviewCapabilityApply_PlatformProtectedRefusedBeforeResumable(t *testing.T) {
	srv, client, base, put := newTestReviewServerWithSrv(t)
	srv.adminActor = "vtx.identity.testAdminHJKMNPQRST"
	putCapProposal(t, put, "prot1", map[string]any{
		"intent": "approved, targets the authz base", "kind": "lens", "content": validLensContent,
		"reviewState": "approved", "targetMode": "upgradeExisting", "targetPackageName": "rbac-domain",
		"targetNewVersion": "1.2.0",
	})
	// Genuinely installed at exactly the declared version — the state that
	// makes the resumable branch fire.
	putInstalledPackage(t, put, "liveRbac", "rbac-domain", "1.2.0", false)

	res, body := postReview(t, client, base, "/api/review/capability/prot1/apply")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", res.StatusCode)
	}
	if body["resumable"] == true {
		t.Fatalf("a platform-protected proposal was steered into the mark-applied recovery: %+v", body)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "platform-protected") || !strings.Contains(msg, "rbac-domain") {
		t.Errorf("want a platform-protected refusal naming rbac-domain, got %+v", body)
	}
}

// A near-miss spelling must not walk past the deny-list either: the console
// check normalizes the same way the plan builder's does.
func TestReviewCapabilityApply_PlatformProtectedNearMissRefused(t *testing.T) {
	srv, client, base, put := newTestReviewServerWithSrv(t)
	srv.adminActor = "vtx.identity.testAdminHJKMNPQRST"
	putCapProposal(t, put, "prot2", map[string]any{
		"intent": "approved, lookalike name", "kind": "lens", "content": validLensContent,
		"reviewState": "approved", "targetMode": "newPackage", "targetPackageName": " Rbac-Domain ",
		"targetNewVersion": "1.2.0",
	})

	res, body := postReview(t, client, base, "/api/review/capability/prot2/apply")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", res.StatusCode)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "platform-protected") {
		t.Errorf("want a platform-protected refusal, got %+v", body)
	}
}

// mark-applied never runs the plan builder, so it owns the same refusal
// outright: what it relays stamps review.state=applied with a real appliedAs
// link into the platform package's vertex — a falsified audit record even
// though no install ran.
func TestReviewCapabilityMarkApplied_PlatformProtectedRefused(t *testing.T) {
	client, base, put := newTestReviewServer(t)
	putCapProposal(t, put, "mkprot1", map[string]any{
		"intent": "approved, targets the authz base", "kind": "lens", "content": validLensContent,
		"reviewState": "approved", "targetMode": "upgradeExisting", "targetPackageName": "rbac-domain",
		"targetNewVersion": "1.2.0",
	})
	putInstalledPackage(t, put, "liveRbac", "rbac-domain", "1.2.0", false)

	res, body := postReview(t, client, base, "/api/review/capability/mkprot1/mark-applied")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", res.StatusCode)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "platform-protected") || !strings.Contains(msg, "rbac-domain") {
		t.Errorf("want a platform-protected refusal naming rbac-domain, got %+v", body)
	}
}

// putInstalledPackage writes the Core-KV shape mark-applied's resolver reads:
// a vtx.package.<id> root plus its .manifest aspect carrying name/version.
func putInstalledPackage(t *testing.T, put func(bucket, key, value string), id, name, version string, deleted bool) {
	t.Helper()
	key := "vtx.package." + id
	put(bootstrap.CoreKVBucket, key,
		`{"class":"package","isDeleted":`+boolLit(deleted)+`,"data":{}}`)
	put(bootstrap.CoreKVBucket, key+".manifest",
		`{"class":"packageManifest","isDeleted":`+boolLit(deleted)+
			`,"data":{"name":"`+name+`","version":"`+version+`","declaredKeys":[]}}`)
}

func boolLit(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestFindInstalledPackageByName(t *testing.T) {
	docs := map[string]string{
		"vtx.package.aaa":          `{"isDeleted":false,"data":{}}`,
		"vtx.package.aaa.manifest": `{"isDeleted":false,"data":{"name":"alpha","version":"1.2.0"}}`,
		"vtx.package.bbb":          `{"isDeleted":false,"data":{}}`,
		"vtx.package.bbb.manifest": `{"isDeleted":true,"data":{"name":"beta","version":"0.1.0"}}`,
		"vtx.package.ccc":          `{"isDeleted":true,"data":{}}`,
		"vtx.package.ccc.manifest": `{"isDeleted":false,"data":{"name":"gamma","version":"0.3.0"}}`,
		// A second live claim on "alpha", sorting after aaa: first-claim wins.
		"vtx.package.zzz":          `{"isDeleted":false,"data":{}}`,
		"vtx.package.zzz.manifest": `{"isDeleted":false,"data":{"name":"alpha","version":"9.9.9"}}`,
	}
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	get := func(key string) ([]byte, bool) {
		raw, ok := docs[key]
		return []byte(raw), ok
	}

	key, version, ok := findInstalledPackageByName(keys, get, "alpha")
	if !ok || key != "vtx.package.aaa" || version != "1.2.0" {
		t.Errorf("alpha = (%q, %q, %v), want (vtx.package.aaa, 1.2.0, true) — sorted first claim wins", key, version, ok)
	}
	if _, _, ok := findInstalledPackageByName(keys, get, "beta"); ok {
		t.Error("beta resolved, but its manifest is tombstoned (uninstalled)")
	}
	if _, _, ok := findInstalledPackageByName(keys, get, "gamma"); ok {
		t.Error("gamma resolved, but its package ROOT is tombstoned")
	}
	if _, _, ok := findInstalledPackageByName(keys, get, "delta"); ok {
		t.Error("delta resolved, but no manifest records that name")
	}
	if _, _, ok := findInstalledPackageByName(keys, get, ""); ok {
		t.Error("an empty name resolved; a proposal with no target.packageName must never match a package")
	}
}

func TestRecoveredInstallRequestID(t *testing.T) {
	// The prefix is the point: this pointer was reconstructed, not observed,
	// and must not read like the "install:<name>@<version>" a real apply stamps.
	if got := recoveredInstallRequestID("alpha", "1.2.0"); got != "recovered:alpha@1.2.0" {
		t.Errorf("= %q", got)
	}
}

func TestTargetInstallVersion(t *testing.T) {
	// The default must track CapabilityApplyPlanForProposal's own: a proposal
	// declaring no newVersion installs at 0.1.0, so that is the version the
	// recovery check has to look for.
	if got := targetInstallVersion(capabilityProposalCols{TargetNewVersion: "2.0.0"}); got != "2.0.0" {
		t.Errorf("declared = %q", got)
	}
	if got := targetInstallVersion(capabilityProposalCols{}); got != "0.1.0" {
		t.Errorf("undeclared = %q, want the 0.1.0 plan-builder default", got)
	}
}

func TestMarkOpFailure(t *testing.T) {
	if got := markOpFailure(&processor.OperationReply{Status: processor.ReplyStatusAccepted}, nil); got != "" {
		t.Errorf("an accepted reply = %q, want no failure", got)
	}
	// The case that matters: a Processor refusal arrives as a well-formed
	// reply with a nil error, so branching on err alone calls it a success.
	got := markOpFailure(&processor.OperationReply{
		Status: processor.ReplyStatusRejected,
		Error:  &opwire.ReplyError{Code: "InvalidApplyTransition", Message: "proposal is not approved"},
	}, nil)
	if !strings.Contains(got, "proposal is not approved") {
		t.Errorf("a rejected reply = %q, want the rejection reason", got)
	}
	if got := markOpFailure(&processor.OperationReply{Status: processor.ReplyStatusRejected}, nil); got == "" {
		t.Error("a rejected reply with no error detail reported no failure")
	}
	if got := markOpFailure(nil, errors.New("gateway unreachable")); got != "gateway unreachable" {
		t.Errorf("transport error = %q", got)
	}
	if got := markOpFailure(nil, nil); got == "" {
		t.Error("a nil reply with no error reported no failure")
	}
}

func TestReviewCapabilityMarkApplied_NotApproved(t *testing.T) {
	client, base, put := newTestReviewServer(t)
	putCapProposal(t, put, "mkpend1", map[string]any{
		"intent": "still pending", "kind": "lens", "content": validLensContent,
		"reviewState": "pending", "targetPackageName": "alpha",
	})

	res, body := postReview(t, client, base, "/api/review/capability/mkpend1/mark-applied")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (only an approved proposal is recoverable)", res.StatusCode)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "not approved") {
		t.Errorf("want a not-approved reason, got %+v", body)
	}
}

func TestReviewCapabilityMarkApplied_AlreadyApplied(t *testing.T) {
	client, base, put := newTestReviewServer(t)
	putCapProposal(t, put, "mkdone1", map[string]any{
		"intent": "already closed", "kind": "lens", "content": validLensContent,
		"reviewState": "applied", "targetPackageName": "alpha", "targetNewVersion": "1.0.0",
	})

	// The likeliest way to reach this is a double-click or a retry against a
	// lagging read model, where "nothing to recover" is the honest answer —
	// not "it is not approved", which describes a different problem.
	res, body := postReview(t, client, base, "/api/review/capability/mkdone1/mark-applied")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", res.StatusCode)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "already applied") {
		t.Errorf("want an already-applied reason, got %+v", body)
	}
}

func TestReviewCapabilityMarkApplied_NoInstalledPackage(t *testing.T) {
	client, base, put := newTestReviewServer(t)
	putCapProposal(t, put, "mknone1", map[string]any{
		"intent": "approved, nothing installed", "kind": "lens", "content": validLensContent,
		"reviewState": "approved", "targetMode": "newPackage", "targetPackageName": "alpha",
		"targetNewVersion": "1.0.0",
	})

	// Nothing in core-kv claims the name, so the install half never committed:
	// recovery must refuse and send the operator to Apply rather than relay an
	// op the Processor would reject anyway.
	res, body := postReview(t, client, base, "/api/review/capability/mknone1/mark-applied")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (no live installed package)", res.StatusCode)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "run Apply") {
		t.Errorf("want the run-Apply guidance, got %+v", body)
	}
}

// The defect this pins: an upgradeExisting target has a package of its name
// installed BEFORE the apply — that is the mode's own precondition — so a
// name-only check reports every never-applied upgrade proposal as recoverable
// and closes it over an artifact that was never installed. The version is the
// only thing separating the two states.
func TestReviewCapabilityMarkApplied_UpgradeAtOldVersionIsNotRecoverable(t *testing.T) {
	client, base, put := newTestReviewServer(t)
	putCapProposal(t, put, "mkupg1", map[string]any{
		"intent": "approved upgrade, never applied", "kind": "lens", "content": validLensContent,
		"reviewState": "approved", "targetMode": "upgradeExisting", "targetPackageName": "alpha",
		"targetBaseVersion": "1.0.0", "targetNewVersion": "2.0.0",
	})
	putInstalledPackage(t, put, "liveAlpha", "alpha", "1.0.0", false)

	res, body := postReview(t, client, base, "/api/review/capability/mkupg1/mark-applied")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — alpha is installed, but at the PRE-upgrade version", res.StatusCode)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "2.0.0") {
		t.Errorf("the refusal should name the version it looked for, got %+v", body)
	}
}

func TestReviewCapabilityMarkApplied_TombstonedPackageIsNotRecoverable(t *testing.T) {
	client, base, put := newTestReviewServer(t)
	putCapProposal(t, put, "mkdead1", map[string]any{
		"intent": "approved, package since uninstalled", "kind": "lens", "content": validLensContent,
		"reviewState": "approved", "targetMode": "newPackage", "targetPackageName": "alpha",
		"targetNewVersion": "1.0.0",
	})
	// Same name AND the target version, so the tombstone is the only thing
	// that can produce the refusal.
	putInstalledPackage(t, put, "deadAlpha", "alpha", "1.0.0", true)

	res, _ := postReview(t, client, base, "/api/review/capability/mkdead1/mark-applied")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — an uninstall tombstones the manifest, it does not remove the key", res.StatusCode)
	}
}

func TestReviewCapabilityMarkApplied_InstalledReachesGatewaySubmit(t *testing.T) {
	client, base, put := newTestReviewServer(t)
	putCapProposal(t, put, "mkok1", map[string]any{
		"intent": "approved, install committed", "kind": "lens", "content": validLensContent,
		"reviewState": "approved", "targetMode": "newPackage", "targetPackageName": "alpha",
		"targetNewVersion": "1.2.0",
	})
	putInstalledPackage(t, put, "liveAlpha", "alpha", "1.2.0", false)

	// Every precondition holds, so the handler derives the payload and relays
	// it; this fixture has no operator credential, so the relay fails — a 502
	// naming the mark-applied submit proves the state check and the package
	// resolution both cleared. What the payload CARRIES is pinned by
	// TestReviewCapabilityMarkApplied_SubmitsResolvedPackage below.
	res, body := postReview(t, client, base, "/api/review/capability/mkok1/mark-applied")
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (preconditions cleared → attempts gateway submit)", res.StatusCode)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "mark applied") {
		t.Errorf("want a mark-applied relay error, got %+v", body)
	}
}

// stubGateway stands in for the real Gateway: it records the relayed operation
// request and answers with reply. Driving the handler past the relay is the
// only way to assert what the op actually carries — the resolved package key
// and the read set the DDL's read posture depends on.
func stubGateway(t *testing.T, reply processor.OperationReply) (url string, captured *gatewayOperationRequest) {
	t.Helper()
	captured = &gatewayOperationRequest{}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(captured); err != nil {
			t.Errorf("decode relayed request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(hs.Close)
	return hs.URL, captured
}

// callMarkApplied drives the handler directly with an operator token in the
// request context (requireOperator, which normally puts it there, is not part
// of this fixture).
func callMarkApplied(t *testing.T, srv *server, id string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/review/capability/"+id+"/mark-applied", nil)
	req = req.WithContext(context.WithValue(req.Context(), operatorTokenContextKey{}, "test-operator-token"))
	rec := httptest.NewRecorder()
	srv.reviewCapabilityMarkApplied(rec, req, id)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec, body
}

func TestReviewCapabilityMarkApplied_SubmitsResolvedPackage(t *testing.T) {
	srv, _, _, put := newTestReviewServerWithSrv(t)
	putCapProposal(t, put, "mksub1", map[string]any{
		"intent": "approved, install committed", "kind": "lens", "content": validLensContent,
		"reviewState": "approved", "targetMode": "newPackage", "targetPackageName": "alpha",
		"targetNewVersion": "1.2.0",
	})
	putInstalledPackage(t, put, "liveAlpha", "alpha", "1.2.0", false)
	url, captured := stubGateway(t, processor.OperationReply{Status: processor.ReplyStatusAccepted, Decision: "committed"})
	srv.gatewayURL = url

	rec, body := callMarkApplied(t, srv, "mksub1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %+v", rec.Code, body)
	}
	if body["packageKey"] != "vtx.package.liveAlpha" {
		t.Errorf("reply packageKey = %v", body["packageKey"])
	}
	if body["installRequestId"] != "recovered:alpha@1.2.0" {
		t.Errorf("reply installRequestId = %v", body["installRequestId"])
	}

	if captured.OperationType != "MarkCapabilityProposalApplied" {
		t.Errorf("relayed operationType = %q", captured.OperationType)
	}
	var payload map[string]any
	if err := json.Unmarshal(captured.Payload, &payload); err != nil {
		t.Fatalf("decode relayed payload: %v", err)
	}
	// The resolved ROOT key, never the .manifest aspect it was found through —
	// the op records this as the appliedAs link target.
	if payload["packageKey"] != "vtx.package.liveAlpha" {
		t.Errorf("payload packageKey = %v, want the package root", payload["packageKey"])
	}
	if payload["proposalId"] != "mksub1" {
		t.Errorf("payload proposalId = %v", payload["proposalId"])
	}
	wantReads := []string{
		"vtx.capabilityproposal.mksub1.review",
		"vtx.capabilityproposal.mksub1.target",
		"vtx.package.liveAlpha.manifest",
	}
	if strings.Join(captured.Reads, ",") != strings.Join(wantReads, ",") {
		t.Errorf("declared reads = %v, want %v", captured.Reads, wantReads)
	}
}

func TestReviewCapabilityMarkApplied_RejectedReplyIsNotSuccess(t *testing.T) {
	srv, _, _, put := newTestReviewServerWithSrv(t)
	putCapProposal(t, put, "mkrej1", map[string]any{
		"intent": "approved, install committed", "kind": "lens", "content": validLensContent,
		"reviewState": "approved", "targetMode": "newPackage", "targetPackageName": "alpha",
		"targetNewVersion": "1.2.0",
	})
	putInstalledPackage(t, put, "liveAlpha", "alpha", "1.2.0", false)
	// A Processor refusal comes back as a well-formed reply with a nil error,
	// so a handler branching on the error alone reports it as success.
	url, _ := stubGateway(t, processor.OperationReply{
		Status: processor.ReplyStatusRejected,
		Error:  &opwire.ReplyError{Code: "InvalidApplyTransition", Message: "proposal is not approved"},
	})
	srv.gatewayURL = url

	rec, body := callMarkApplied(t, srv, "mkrej1")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — a rejection is the Processor's own refusal, not an upstream fault", rec.Code)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "proposal is not approved") {
		t.Errorf("want the rejection reason surfaced, got %+v", body)
	}
}

func TestReviewCapabilityMarkApplied_RejectsDottedID(t *testing.T) {
	client, base, _ := newTestReviewServer(t)
	res, _ := postReview(t, client, base, "/api/review/capability/a.b/mark-applied")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
}

func TestReviewCapabilityMarkApplied_NotFound(t *testing.T) {
	client, base, _ := newTestReviewServer(t)
	res, _ := postReview(t, client, base, "/api/review/capability/missingmk/mark-applied")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
}

func TestFreshCapabilityVerdict_Lens(t *testing.T) {
	// The lens kind needs no live substrate read (held/sensitiveAspects both
	// nil), so a nil conn is safe — this is the pure decision-logic seam.
	ctx := context.Background()
	valid, err := freshCapabilityVerdict(ctx, nil, capabilityProposalCols{Kind: "lens", Content: validLensContent})
	if err != nil {
		t.Fatalf("valid lens: unexpected error: %v", err)
	}
	if !valid.Valid {
		t.Errorf("valid lens: want Valid, got errors %v", valid.Errors)
	}
	invalid, err := freshCapabilityVerdict(ctx, nil, capabilityProposalCols{Kind: "lens", Content: invalidLensContent})
	if err != nil {
		t.Fatalf("invalid lens: unexpected error: %v", err)
	}
	if invalid.Valid {
		t.Errorf("invalid lens: want !Valid (unparseable cypher)")
	}
}

// newBareReviewServer spins up an embedded NATS server with NO review buckets
// created — the state of a stack where neither packages/capability-author nor
// packages/augur is installed. The queue handlers must classify the missing
// bucket as unprovisioned, not a fault.
func newBareReviewServer(t *testing.T) (client *http.Client, baseURL string) {
	t.Helper()
	ns := natsfixture.StartServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: ns.ClientURL(), Name: "loupe-test-bare"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)

	srv := &server{conn: conn, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), natsTimeout: 5 * time.Second}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	return hs.Client(), hs.URL
}

func TestReviewQueue_UnprovisionedWhenBucketAbsent(t *testing.T) {
	client, base := newBareReviewServer(t)

	cases := []struct{ tab, wantPkg string }{
		{"capability", "capability-author"},
		{"augur", "augur"},
	}
	for _, c := range cases {
		res, err := client.Get(base + "/api/review/" + c.tab)
		if err != nil {
			t.Fatalf("GET %s: %v", c.tab, err)
		}
		var body map[string]any
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatalf("%s decode: %v", c.tab, err)
		}
		res.Body.Close()
		// An absent bucket is a benign not-installed state, not a 502.
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 (unprovisioned is not an error)", c.tab, res.StatusCode)
		}
		if body["unprovisioned"] != true || body["packageName"] != c.wantPkg {
			t.Errorf("%s: want unprovisioned:true + packageName:%s, got %+v", c.tab, c.wantPkg, body)
		}
		if cnt, _ := body["count"].(float64); cnt != 0 {
			t.Errorf("%s: want count 0, got %v", c.tab, body["count"])
		}
	}
}
