package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/processor/opwire"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/substrate/keys"
	"github.com/operatinggraph/lattice/internal/testutil"
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
	// The receipt is what makes this proposal's install PROVEN, and only a
	// proven install is resumable: a newPackage proposal matched by name and
	// version alone is refused instead (see
	// TestReviewCapabilityApply_UnprovenNewPackageIsNotResumable, the row's own
	// regression test).
	putInstallReceipt(t, put, "resume1", "vtx.package.liveAlpha", "req-observed-livealpha", false)

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
	// A newPackage proposal reaches the relay only once its install is proven —
	// the receipt is that proof, and without it this close is refused.
	putInstallReceipt(t, put, "mkok1", "vtx.package.liveAlpha", "req-observed-livealpha", false)

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
	// upgradeExisting is the mode whose close still resolves by name+version
	// alone — a live package of that name is its own precondition, so its
	// presence carries no provenance signal for the receipt to add. It is
	// therefore the only mode on which the reconstructed "recovered:" pointer
	// is still reachable, which is what this test pins.
	putCapProposal(t, put, "mksub1", map[string]any{
		"intent": "approved, install committed", "kind": "lens", "content": validLensContent,
		"reviewState": "approved", "targetMode": "upgradeExisting", "targetPackageName": "alpha",
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
		"vtx.package.liveAlpha",
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
	// The receipt proves this proposal's install, so the handler reaches the
	// relay at all — an unproven newPackage close never gets that far.
	putInstallReceipt(t, put, "mkrej1", "vtx.package.liveAlpha", "req-observed-livealpha", false)
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

// seedCapabilityGrantFixture provisions capability-kv on the fixture's
// connection and writes one capability doc for actor, carrying operationType
// at scope "any".
func seedCapabilityGrantFixture(t *testing.T, srv *server, capKey, actor, operationType string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := srv.conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: bootstrap.CapabilityKVBucket,
	}); err != nil {
		t.Fatalf("create %s: %v", bootstrap.CapabilityKVBucket, err)
	}
	doc, err := json.Marshal(processor.CapabilityDoc{
		Actor:               actor,
		PlatformPermissions: []processor.PlatformPermission{{OperationType: operationType, Scope: "any"}},
	})
	if err != nil {
		t.Fatalf("marshal capability doc: %v", err)
	}
	if _, err := srv.conn.KVPut(ctx, bootstrap.CapabilityKVBucket, capKey, doc); err != nil {
		t.Fatalf("put %s: %v", capKey, err)
	}
}

// wantScopeCheckFailure is the §5 no-escalation refusal
// (pkgmgr.validateGrantArtifact): the reason a correctly-routed held-permission
// set must produce, so a test asserting only "invalid" cannot pass on an
// unrelated failure.
const wantScopeCheckFailure = `requesting operator does not hold "CreateTask" at scope "any" or broader`

func grantProposalCols(requester, operationType string) capabilityProposalCols {
	content, _ := json.Marshal(pkgmgr.GrantArtifactContent{
		OperationType: operationType, Scope: "any", GrantsTo: []string{"operator"},
	})
	return capabilityProposalCols{
		Key:         "vtx.capabilityproposal.revGrantHJKMNPQRSTUV",
		RequesterID: requester,
		Kind:        "grant",
		Content:     string(content),
	}
}

// TestSystemActorSet_EmptyResultIsNotMemoized: an empty discovery is a
// not-yet answer, not an answer. A kernel still seeding its primordial
// holdsRole links returns no system actors and no error, and latching that
// would route every actor as ordinary for the rest of the process — silently
// under-reporting a real system actor's held permissions on every later
// approve. The memo must therefore keep retrying until it sees a non-empty
// set, and stop listing core-kv once it has.
func TestSystemActorSet_EmptyResultIsNotMemoized(t *testing.T) {
	testutil.EnsurePrimordials(t)
	srv, _, _, put := newTestReviewServerWithSrv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	empty, err := srv.systemActors.get(ctx, srv.conn)
	if err != nil {
		t.Fatalf("get on an unseeded kernel: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("get = %v, want empty on an unseeded kernel", empty)
	}

	const actorID = "revLateSeededHJKMNPQ"
	put(bootstrap.CoreKVBucket,
		"lnk.identity."+actorID+".holdsRole.role."+bootstrap.RoleOperatorID,
		`{"class":"holdsRole","isDeleted":false,"data":{}}`)

	seeded, err := srv.systemActors.get(ctx, srv.conn)
	if err != nil {
		t.Fatalf("get after seeding: %v", err)
	}
	if len(seeded) != 1 || seeded[0] != "vtx.identity."+actorID {
		t.Fatalf("get = %v, want the newly seeded actor — the empty result was latched", seeded)
	}

	// Once non-empty, the answer is held: a later revocation does not
	// re-trigger the listing.
	put(bootstrap.CoreKVBucket,
		"lnk.identity."+actorID+".holdsRole.role."+bootstrap.RoleOperatorID,
		`{"class":"holdsRole","isDeleted":true,"data":{}}`)
	held, err := srv.systemActors.get(ctx, srv.conn)
	if err != nil {
		t.Fatalf("get after revocation: %v", err)
	}
	if len(held) != 1 {
		t.Fatalf("get = %v, want the memoized set — a resolved set is held for the process", held)
	}
}

// TestFreshCapabilityVerdict_OrdinaryActorAnchorDocIsNotHeld: on Loupe's
// approve path, an ordinary requester's cap.<rest> anchor doc is not part of
// its grant set, so a permission living only there cannot bound what its
// proposal confers.
func TestFreshCapabilityVerdict_OrdinaryActorAnchorDocIsNotHeld(t *testing.T) {
	testutil.EnsurePrimordials(t)
	srv, _, _, _ := newTestReviewServerWithSrv(t)
	const requesterID = "revEverydayReqHJKMNP"
	requester := "vtx.identity." + requesterID
	seedCapabilityGrantFixture(t, srv, "cap.identity."+requesterID, requester, "CreateTask")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	report, err := srv.freshCapabilityVerdict(ctx, srv.conn, grantProposalCols(requester, "CreateTask"))
	if err != nil {
		t.Fatalf("freshCapabilityVerdict: %v", err)
	}
	if report.Valid {
		t.Fatal("verdict is valid — an ordinary actor's anchor doc must not count as held")
	}
	// The reason matters: an invalid verdict reached by any other route would
	// let this pass while the scope check itself was broken or skipped.
	if !slices.ContainsFunc(report.Errors, func(e string) bool {
		return strings.Contains(e, wantScopeCheckFailure)
	}) {
		t.Fatalf("errors = %v, want one naming the scope check: %q", report.Errors, wantScopeCheckFailure)
	}
}

// TestFreshCapabilityVerdict_SystemActorAnchorDocIsHeld is the positive
// vector: the same anchor-doc-only seeding validates for a requester that
// really is a system actor (it holds the primordial operator role through a
// live holdsRole link, the predicate bootstrap.SystemActorKeys discovers).
func TestFreshCapabilityVerdict_SystemActorAnchorDocIsHeld(t *testing.T) {
	testutil.EnsurePrimordials(t)
	srv, _, _, put := newTestReviewServerWithSrv(t)
	const requesterID = "revSystemReqHJKMNPQR"
	requester := "vtx.identity." + requesterID
	put(bootstrap.CoreKVBucket,
		"lnk.identity."+requesterID+".holdsRole.role."+bootstrap.RoleOperatorID,
		`{"class":"holdsRole","isDeleted":false,"data":{}}`)
	seedCapabilityGrantFixture(t, srv, "cap.identity."+requesterID, requester, "CreateTask")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	report, err := srv.freshCapabilityVerdict(ctx, srv.conn, grantProposalCols(requester, "CreateTask"))
	if err != nil {
		t.Fatalf("freshCapabilityVerdict: %v", err)
	}
	if !report.Valid {
		t.Fatalf("verdict is invalid (%v) — a system actor's anchor doc IS part of its grant set", report.Errors)
	}
}

func TestFreshCapabilityVerdict_Lens(t *testing.T) {
	// The lens kind needs no live substrate read (held/sensitiveAspects both
	// nil), so a nil conn is safe — this is the pure decision-logic seam.
	ctx := context.Background()
	valid, err := (&server{}).freshCapabilityVerdict(ctx, nil, capabilityProposalCols{Kind: "lens", Content: validLensContent})
	if err != nil {
		t.Fatalf("valid lens: unexpected error: %v", err)
	}
	if !valid.Valid {
		t.Errorf("valid lens: want Valid, got errors %v", valid.Errors)
	}
	invalid, err := (&server{}).freshCapabilityVerdict(ctx, nil, capabilityProposalCols{Kind: "lens", Content: invalidLensContent})
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

// TestReviewCapabilityApply_RemovalRefusalIs409 drives the apply endpoint end
// to end over a proposal whose one-lens artifact does not describe the package
// it upgrades. The refusal is deterministic — the same Definition will describe
// the same package just as poorly tomorrow — so it must reach the console as a
// 409 carrying the remedy, not as the 502 the UI treats as a transport blip
// worth retrying.
//
// It posts the real request rather than calling packageApplyStatus directly:
// the mapping function agreeing about a sentinel proves nothing if the call
// site never produces it.
func TestReviewCapabilityApply_RemovalRefusalIs409(t *testing.T) {
	srv, client, base, put := newTestReviewServerWithSrv(t)
	srv.adminActor = "vtx.identity.testAdminHJKMNPQRST"

	const pkgName = "alpha"
	const installedVersion = "1.2.0"

	// The installed package, on the deterministic keys a real install writes:
	// its manifest declares a lens the proposal's artifact says nothing about,
	// so that lens is what the apply would retire.
	pkgKey := "vtx.package." + substrate.PackageEntityNanoID(pkgName, "package")
	lensKey := "vtx.meta." + pkgmgr.LensID(pkgName, "legacyLens")
	declared := []string{pkgKey, pkgKey + ".manifest", lensKey, lensKey + ".canonicalName"}
	declaredJSON, err := json.Marshal(declared)
	if err != nil {
		t.Fatalf("marshal declaredKeys: %v", err)
	}
	put(bootstrap.CoreKVBucket, pkgKey, `{"class":"package","isDeleted":false,"data":{}}`)
	put(bootstrap.CoreKVBucket, pkgKey+".manifest",
		`{"class":"packageManifest","isDeleted":false,"data":{"name":"`+pkgName+
			`","version":"`+installedVersion+`","declaredKeys":`+string(declaredJSON)+`}}`)
	put(bootstrap.CoreKVBucket, lensKey, `{"class":"meta.lens","isDeleted":false,"data":{}}`)
	put(bootstrap.CoreKVBucket, lensKey+".canonicalName",
		`{"class":"canonicalName","isDeleted":false,"data":{"value":"legacyLens"}}`)

	// The proposal's own aspects, which the plan builder reads.
	proposalKey := "vtx.capabilityproposal.remove1"
	contentJSON, err := json.Marshal(validLensContent)
	if err != nil {
		t.Fatalf("marshal artifact content: %v", err)
	}
	put(bootstrap.CoreKVBucket, proposalKey+".review", `{"isDeleted":false,"data":{"state":"approved"}}`)
	put(bootstrap.CoreKVBucket, proposalKey+".artifact",
		`{"isDeleted":false,"data":{"kind":"lens","content":`+string(contentJSON)+`}}`)
	put(bootstrap.CoreKVBucket, proposalKey+".target",
		`{"isDeleted":false,"data":{"packageName":"`+pkgName+`","mode":"upgradeExisting",`+
			`"newVersion":"2.0.0","baseVersion":"`+installedVersion+`"}}`)

	// The read-model row the handler classifies first. newVersion differs from
	// the installed version, so the resumable recovery branch does not fire and
	// the request reaches the apply.
	putCapProposal(t, put, "remove1", map[string]any{
		"intent": "approved, describes one lens of a package it does not own", "kind": "lens",
		"content": validLensContent, "reviewState": "approved", "targetMode": "upgradeExisting",
		"targetPackageName": pkgName, "targetNewVersion": "2.0.0",
	})

	res, body := postReview(t, client, base, "/api/review/capability/remove1/apply")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — a removal refusal fails identically on every retry: %+v", res.StatusCode, body)
	}
	if body["resumable"] == true {
		t.Fatalf("a refused apply committed nothing, so it is not recoverable via mark-applied: %+v", body)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "would drop keys its Definition does not describe") {
		t.Fatalf("want the removal refusal in the body, got %+v", body)
	}
	if !strings.Contains(msg, "newPackage") {
		t.Errorf("the refusal must carry its remedy through to the console, got %q", msg)
	}
}

// TestReviewCapabilityApply_SameVersionUpgradeIsNotResumable pins the ordering
// that makes the console's recovery classification sound.
//
// The classification answers "is this package live at the target version", and
// treats yes as "the install committed, close it with mark-applied". An
// upgradeExisting proposal declaring newVersion EQUAL to the installed version
// produces that same yes without ever having applied — so with the target
// preconditions running only inside the plan builder, which this branch returns
// before reaching, every such proposal was closed over an artifact that never
// landed. The refusal has to be asked for first.
func TestReviewCapabilityApply_SameVersionUpgradeIsNotResumable(t *testing.T) {
	srv, client, base, put := newTestReviewServerWithSrv(t)
	srv.adminActor = "vtx.identity.testAdminHJKMNPQRST"

	const pkgName = "gamma"
	putInstalledPackage(t, put, "liveGamma", pkgName, "1.2.0", false)

	proposalKey := "vtx.capabilityproposal.samever1"
	contentJSON, err := json.Marshal(validLensContent)
	if err != nil {
		t.Fatalf("marshal artifact content: %v", err)
	}
	put(bootstrap.CoreKVBucket, proposalKey+".review", `{"isDeleted":false,"data":{"state":"approved"}}`)
	put(bootstrap.CoreKVBucket, proposalKey+".artifact",
		`{"isDeleted":false,"data":{"kind":"lens","content":`+string(contentJSON)+`}}`)
	put(bootstrap.CoreKVBucket, proposalKey+".target",
		`{"isDeleted":false,"data":{"packageName":"`+pkgName+`","mode":"upgradeExisting",`+
			`"newVersion":"1.2.0","baseVersion":"1.2.0"}}`)

	putCapProposal(t, put, "samever1", map[string]any{
		"intent": "approved, targets its own installed version", "kind": "lens",
		"content": validLensContent, "reviewState": "approved", "targetMode": "upgradeExisting",
		"targetPackageName": pkgName, "targetNewVersion": "1.2.0",
	})

	res, body := postReview(t, client, base, "/api/review/capability/samever1/apply")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %+v", res.StatusCode, body)
	}
	if body["resumable"] == true {
		t.Fatalf("a proposal that never applied was offered the mark-applied recovery, which would close it over nothing: %+v", body)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "which is the version already installed") {
		t.Fatalf("want the precondition's own refusal, got %+v", body)
	}
}

// TestPackageApplyStatus_UndeclaredSecureColumnDropIs409 covers the refusal a
// partial capability apply used to reach FIRST, before the coverage guard was
// moved ahead of it — and which any source-authored upgrade still reaches. It
// returned a bare error, so it fell through to 502: the code this console's own
// front end retries, for a state that stays refused until an author writes an
// attestation.
func TestPackageApplyStatus_UndeclaredSecureColumnDropIs409(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", pkgmgr.ErrUndeclaredSecureColumnDrop)
	if got := packageApplyStatus(err); got != http.StatusConflict {
		t.Fatalf("packageApplyStatus = %d, want 409 — an unattested erasure is not a transient", got)
	}
}

// putInstallReceipt writes the Core-KV shape targetInstall's receipt-first
// resolution reads: the create-only vtx.capabilityproposal.<id>.install aspect
// naming the package the proposal's apply committed.
func putInstallReceipt(t *testing.T, put func(bucket, key, value string), proposalID, packageKey, installRequestID string, deleted bool) {
	t.Helper()
	proposalKey := "vtx.capabilityproposal." + proposalID
	doc, err := json.Marshal(map[string]any{
		"class":     "capabilityAuthor.install",
		"isDeleted": deleted,
		"vertexKey": proposalKey,
		"localName": "install",
		"data": map[string]any{
			"packageKey":       packageKey,
			"installRequestId": installRequestID,
			"recordedAt":       "2026-08-29T00:00:00Z",
		},
	})
	if err != nil {
		t.Fatalf("marshal receipt for %s: %v", proposalID, err)
	}
	put(bootstrap.CoreKVBucket, proposalKey+".install", string(doc))
}

// resolveTargetInstall drives targetInstall for one proposal id, from the
// read-model row the console itself would have decoded.
func resolveTargetInstall(t *testing.T, srv *server, id string) installResolution {
	t.Helper()
	resolved, err := tryResolveTargetInstall(t, srv, id)
	if err != nil {
		t.Fatalf("targetInstall(%s): %v", id, err)
	}
	return resolved
}

func tryResolveTargetInstall(t *testing.T, srv *server, id string) (installResolution, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	proposalKey := "vtx.capabilityproposal." + id
	cols, ok := srv.capabilityRow(ctx, srv.conn, proposalKey)
	if !ok {
		t.Fatalf("read the read-model row for %s", id)
	}
	return srv.targetInstall(ctx, srv.conn, proposalKey, cols)
}

// seedForeignInstallFixture is the defect's own state: a newPackage proposal
// whose target.packageName AND version match a live package some OTHER writer
// installed, alongside a second live package at the same name and version that
// this proposal's own apply produced. The name scan sorts foreignAlpha first
// and so can only ever answer with it, which makes "which of the two came
// back" exactly the provenance question.
func seedForeignInstallFixture(t *testing.T, put func(bucket, key, value string), id string) {
	t.Helper()
	putCapProposal(t, put, id, map[string]any{
		"intent": "approved, a name another writer already installed", "kind": "lens", "content": validLensContent,
		"reviewState": "approved", "targetMode": "newPackage", "targetPackageName": "alpha",
		"targetNewVersion": "1.2.0",
	})
	putInstalledPackage(t, put, "foreignAlpha", "alpha", "1.2.0", false)
	putInstalledPackage(t, put, "ownAlpha", "alpha", "1.2.0", false)
}

// The legacy behaviour, preserved: with no receipt there is nothing but the
// name and the version to go on, so the foreign install resolves. This is the
// vector the receipted cases below are measured against — without it, a test
// asserting "the receipt won" could pass on a resolver that never ran either
// branch.
func TestTargetInstall_NoReceiptFallsBackToNameAndVersion(t *testing.T) {
	srv, _, _, put := newTestReviewServerWithSrv(t)
	seedForeignInstallFixture(t, put, "recv1")

	resolved := resolveTargetInstall(t, srv, "recv1")
	if !resolved.Installed {
		t.Fatalf("resolution = %+v, want installed — the name+version heuristic still answers when no receipt exists", resolved)
	}
	if resolved.PackageKey != "vtx.package.foreignAlpha" {
		t.Errorf("packageKey = %q, want the name+version match", resolved.PackageKey)
	}
	if resolved.Version != "1.2.0" {
		t.Errorf("version = %q, want the resolved package's own manifest version", resolved.Version)
	}
	if resolved.InstallRequestID != "" || resolved.ReceiptStale {
		t.Errorf("resolution = %+v, want no observed pointer and no stale flag", resolved)
	}
}

// The defect's regression test. The same proposal, now carrying the receipt its
// own apply stamped: the resolution must name the package the receipt records,
// not the same-named package at the same version the catalog scan finds first.
func TestTargetInstall_ReceiptOutranksTheNameScan(t *testing.T) {
	srv, _, _, put := newTestReviewServerWithSrv(t)
	seedForeignInstallFixture(t, put, "recv2")
	putInstallReceipt(t, put, "recv2", "vtx.package.ownAlpha", "req-observed-ownalpha", false)

	resolved := resolveTargetInstall(t, srv, "recv2")
	if !resolved.Installed {
		t.Fatalf("resolution = %+v, want installed", resolved)
	}
	if resolved.PackageKey == "vtx.package.foreignAlpha" {
		t.Fatalf("the proposal resolved to the package another writer installed — the receipt naming vtx.package.ownAlpha was ignored: %+v", resolved)
	}
	if resolved.PackageKey != "vtx.package.ownAlpha" {
		t.Errorf("packageKey = %q, want the receipt's own key", resolved.PackageKey)
	}
	if resolved.InstallRequestID != "req-observed-ownalpha" {
		t.Errorf("installRequestId = %q, want the observed pointer the receipt carries", resolved.InstallRequestID)
	}
}

// The receipt narrows the heuristic; it does not remove its guard. A receipt's
// installRequestId is caller-supplied and verified by nothing, so a receipt
// alone must never close a proposal over a package at a version the proposal
// does not target — that is the refusal the name+version pair was carrying, and
// dropping it would leave whoever can submit the receipt op able to bind an
// approved-but-never-applied proposal to any live package of that name.
func TestTargetInstall_ReceiptAtTheWrongVersionDoesNotClose(t *testing.T) {
	srv, _, _, put := newTestReviewServerWithSrv(t)
	putCapProposal(t, put, "recvver", map[string]any{
		"intent": "approved, receipt points at another version", "kind": "lens", "content": validLensContent,
		"reviewState": "approved", "targetMode": "newPackage", "targetPackageName": "alpha",
		"targetNewVersion": "1.2.0",
	})
	putInstalledPackage(t, put, "ownAlpha", "alpha", "0.9.9", false)
	putInstallReceipt(t, put, "recvver", "vtx.package.ownAlpha", "req-observed-ownalpha", false)

	resolved := resolveTargetInstall(t, srv, "recvver")
	if resolved.Installed {
		t.Fatalf("a receipt at the wrong version closed the proposal anyway: %+v", resolved)
	}
	if !resolved.ReceiptStale || resolved.ReceiptPackageKey != "vtx.package.ownAlpha" || resolved.ReceiptVersion != "0.9.9" {
		t.Errorf("resolution = %+v, want the stale receipt reported with the version it is actually at", resolved)
	}

	// The positive vector: move that same package to the target version and the
	// same receipt now closes — so the refusal above is the version comparison,
	// not an unread receipt.
	putInstalledPackage(t, put, "ownAlpha", "alpha", "1.2.0", false)
	resolved = resolveTargetInstall(t, srv, "recvver")
	if !resolved.Installed || resolved.PackageKey != "vtx.package.ownAlpha" || resolved.ReceiptStale {
		t.Errorf("resolution = %+v, want the receipted package once it is at the target version", resolved)
	}
}

// A tombstone retains the prior document, so a reader that does not filter
// isDeleted sees a revoked receipt as live — and this receipt is the strongest
// claim the resolver makes. A dead one must contribute nothing, leaving the
// name+version fallback to answer as if no receipt were ever written.
func TestTargetInstall_TombstonedReceiptIsIgnored(t *testing.T) {
	srv, _, _, put := newTestReviewServerWithSrv(t)
	seedForeignInstallFixture(t, put, "recv3")
	putInstallReceipt(t, put, "recv3", "vtx.package.ownAlpha", "req-observed-ownalpha", true)

	resolved := resolveTargetInstall(t, srv, "recv3")
	if resolved.PackageKey == "vtx.package.ownAlpha" || resolved.InstallRequestID != "" || resolved.ReceiptStale {
		t.Fatalf("a tombstoned receipt was read as live: %+v", resolved)
	}
	if !resolved.Installed || resolved.PackageKey != "vtx.package.foreignAlpha" {
		t.Errorf("resolution = %+v, want the name+version fallback's answer", resolved)
	}

	// The paired positive vector: the identical fixture with the receipt ALIVE
	// resolves to the receipted package, so the fall-through above is the
	// isDeleted filter and not a receipt the reader never looked for.
	putInstallReceipt(t, put, "recv3", "vtx.package.ownAlpha", "req-observed-ownalpha", false)
	resolved = resolveTargetInstall(t, srv, "recv3")
	if !resolved.Installed || resolved.PackageKey != "vtx.package.ownAlpha" {
		t.Errorf("resolution = %+v, want the live receipt's own package", resolved)
	}
}

// receiptedThenUninstalledFixture is the stale-receipt state: this proposal's
// install committed and was receipted, that package was later uninstalled, and
// a DIFFERENT package now holds the same name at the same version. The name
// scan therefore says "installed" while the receipt says the proposal's own
// artifact is gone.
func receiptedThenUninstalledFixture(t *testing.T, put func(bucket, key, value string), id string, rootDeleted, manifestDeleted bool) {
	t.Helper()
	putCapProposal(t, put, id, map[string]any{
		"intent": "approved, its own install since removed", "kind": "lens", "content": validLensContent,
		"reviewState": "approved", "targetMode": "newPackage", "targetPackageName": "alpha",
		"targetNewVersion": "1.2.0",
	})
	putInstalledPackage(t, put, "foreignAlpha", "alpha", "1.2.0", false)
	put(bootstrap.CoreKVBucket, "vtx.package.deadAlpha",
		`{"class":"package","isDeleted":`+boolLit(rootDeleted)+`,"data":{}}`)
	put(bootstrap.CoreKVBucket, "vtx.package.deadAlpha.manifest",
		`{"class":"packageManifest","isDeleted":`+boolLit(manifestDeleted)+
			`,"data":{"name":"alpha","version":"1.2.0","declaredKeys":[]}}`)
	putInstallReceipt(t, put, id, "vtx.package.deadAlpha", "req-observed-deadalpha", false)
}

// The receipt names a package that has since been uninstalled, while another
// package now answers the name+version pair. Closing over that one would record
// an artifact this proposal never wrote — the exact defect — so the resolution
// refuses, and it carries what it knows so the endpoints can say why.
//
// Root-only and manifest-only tombstones are separate rows: an uninstall
// tombstones both, so a fixture that only ever sets both passes even if one of
// the two isDeleted checks is dropped.
func TestTargetInstall_StaleReceiptRefusesAndReportsWhy(t *testing.T) {
	cases := []struct {
		name                     string
		id                       string
		rootDeleted, manifestDel bool
	}{
		{"both tombstoned, as a real uninstall leaves them", "recv4a", true, true},
		{"only the package root tombstoned", "recv4b", true, false},
		{"only the manifest tombstoned", "recv4c", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _, _, put := newTestReviewServerWithSrv(t)
			receiptedThenUninstalledFixture(t, put, c.id, c.rootDeleted, c.manifestDel)

			resolved := resolveTargetInstall(t, srv, c.id)
			if resolved.Installed {
				t.Fatalf("a proposal whose own install is gone was closed over the package that replaced it: %+v", resolved)
			}
			if !resolved.ReceiptStale || resolved.ReceiptPackageKey != "vtx.package.deadAlpha" {
				t.Fatalf("resolution = %+v, want the stale receipt named", resolved)
			}
			if resolved.ReceiptVersion != "" {
				t.Errorf("receiptVersion = %q, want empty — the receipted package is not live", resolved.ReceiptVersion)
			}
			// The fall-through still ran: what the name scan found is what the
			// operator has to be warned about.
			if resolved.PackageKey != "vtx.package.foreignAlpha" {
				t.Errorf("packageKey = %q, want the package that now holds the name", resolved.PackageKey)
			}
		})
	}

	// The positive vector for all three rows: leave the receipted package LIVE
	// and the same fixture closes over it, so each refusal above is its own
	// isDeleted check firing rather than a receipt that was never read.
	srv, _, _, put := newTestReviewServerWithSrv(t)
	receiptedThenUninstalledFixture(t, put, "recv4d", false, false)
	resolved := resolveTargetInstall(t, srv, "recv4d")
	if !resolved.Installed || resolved.PackageKey != "vtx.package.deadAlpha" || resolved.ReceiptStale {
		t.Errorf("resolution = %+v, want the receipted package while it is live", resolved)
	}
}

// The receipt records which package the apply wrote, not what it is called now,
// so the name is re-checked against the live manifest. The fixture keeps a live
// foreign alpha@1.2.0 so the receipt branch and the fallback give DIFFERENT
// answers — otherwise both the refusal and its positive vector pass for a
// resolver that never reads the receipt at all.
func TestTargetInstall_ReceiptedPackageUnderAnotherNameIsNotInstalled(t *testing.T) {
	srv, _, _, put := newTestReviewServerWithSrv(t)
	putCapProposal(t, put, "recv5", map[string]any{
		"intent": "approved, receipt points at a package carrying another name", "kind": "lens", "content": validLensContent,
		"reviewState": "approved", "targetMode": "newPackage", "targetPackageName": "alpha",
		"targetNewVersion": "1.2.0",
	})
	putInstalledPackage(t, put, "foreignAlpha", "alpha", "1.2.0", false)
	putInstalledPackage(t, put, "otherPkg", "beta", "1.2.0", false)
	putInstallReceipt(t, put, "recv5", "vtx.package.otherPkg", "req-observed-otherpkg", false)

	resolved := resolveTargetInstall(t, srv, "recv5")
	if resolved.Installed {
		t.Fatalf("a receipt naming a package under a different name resolved as installed: %+v", resolved)
	}
	if !resolved.ReceiptStale || resolved.PackageKey != "vtx.package.foreignAlpha" {
		t.Fatalf("resolution = %+v, want the stale flag and the name scan's own find", resolved)
	}

	// The positive vector: rename the receipted package's manifest to the
	// proposal's target and the same receipt now stands — and it resolves to
	// otherPkg, which the name scan (which answers foreignAlpha) never would.
	putInstalledPackage(t, put, "otherPkg", "alpha", "1.2.0", false)
	resolved = resolveTargetInstall(t, srv, "recv5")
	if !resolved.Installed || resolved.PackageKey != "vtx.package.otherPkg" {
		t.Errorf("resolution = %+v, want the receipted package once its manifest carries the target name", resolved)
	}
}

// A receipt that exists and cannot be read is NOT an absent receipt. Falling
// back there would answer a provenance question with the very heuristic the
// receipt was written to replace, and would do it silently — the one outcome
// coreKVGetter's absent-vs-unreadable split exists to prevent.
func TestTargetInstall_UnreadableReceiptIsAnError(t *testing.T) {
	cases := []struct {
		name, id, doc string
	}{
		{"undecodable document", "recv6a", `not json at all`},
		{"live receipt recording no packageKey", "recv6b",
			`{"class":"capabilityAuthor.install","isDeleted":false,"data":{"installRequestId":"req-observed"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _, _, put := newTestReviewServerWithSrv(t)
			seedForeignInstallFixture(t, put, c.id)
			put(bootstrap.CoreKVBucket, "vtx.capabilityproposal."+c.id+".install", c.doc)

			resolved, err := tryResolveTargetInstall(t, srv, c.id)
			if err == nil {
				t.Fatalf("an unusable receipt silently fell back to the name scan: %+v", resolved)
			}
			if !strings.Contains(err.Error(), "install receipt") {
				t.Errorf("error = %v, want it to name the receipt it could not use", err)
			}
		})
	}

	// The positive vector: the same fixture with a WELL-FORMED receipt resolves
	// without error, so the errors above are the decode guard and not a read
	// that fails for every document.
	srv, _, _, put := newTestReviewServerWithSrv(t)
	seedForeignInstallFixture(t, put, "recv6c")
	putInstallReceipt(t, put, "recv6c", "vtx.package.ownAlpha", "req-observed-ownalpha", false)
	resolved := resolveTargetInstall(t, srv, "recv6c")
	if !resolved.Installed || resolved.PackageKey != "vtx.package.ownAlpha" {
		t.Errorf("resolution = %+v, want the well-formed receipt honoured", resolved)
	}
}

// The operator-facing half of the stale-receipt state. "No package named X is
// installed at version V — this proposal's install never committed, so run
// Apply" is false in both clauses here, and Apply then refuses because the name
// IS live, leaving the proposal holding two contradictory refusals. The message
// has to say what actually happened.
func TestReviewCapabilityMarkApplied_StaleReceiptExplainsItself(t *testing.T) {
	srv, client, base, put := newTestReviewServerWithSrv(t)
	srv.adminActor = "vtx.identity.testAdminHJKMNPQRST"
	receiptedThenUninstalledFixture(t, put, "mkstale1", true, true)

	res, body := postReview(t, client, base, "/api/review/capability/mkstale1/mark-applied")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", res.StatusCode)
	}
	msg, _ := body["error"].(string)
	if strings.Contains(msg, "never committed") {
		t.Fatalf("the refusal still claims the install never committed, which the receipt disproves: %q", msg)
	}
	for _, want := range []string{"vtx.package.deadAlpha", "uninstalled", "vtx.package.foreignAlpha"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q does not mention %q", msg, want)
		}
	}
}

// Apply must not answer a stale receipt with the plan builder's opaque refusal
// either — and it must never call it resumable, which would send the operator
// to a recovery that refuses.
func TestReviewCapabilityApply_StaleReceiptIsNotResumable(t *testing.T) {
	srv, client, base, put := newTestReviewServerWithSrv(t)
	srv.adminActor = "vtx.identity.testAdminHJKMNPQRST"
	receiptedThenUninstalledFixture(t, put, "appstale1", true, true)

	res, body := postReview(t, client, base, "/api/review/capability/appstale1/apply")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", res.StatusCode)
	}
	if body["resumable"] == true {
		t.Fatalf("a proposal whose own install is gone was offered the mark-applied recovery: %+v", body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "vtx.package.deadAlpha") {
		t.Errorf("want the refusal to name the receipted package, got %+v", body)
	}
}

// The recovery endpoint's audit pointer. With a receipt it stamps the OBSERVED
// installRequestId; TestReviewCapabilityMarkApplied_SubmitsResolvedPackage is
// the paired vector for the no-receipt case, where the reconstructed
// "recovered:" pointer is stamped instead.
func TestReviewCapabilityMarkApplied_StampsObservedReceipt(t *testing.T) {
	srv, _, _, put := newTestReviewServerWithSrv(t)
	seedForeignInstallFixture(t, put, "mkrcpt1")
	putInstallReceipt(t, put, "mkrcpt1", "vtx.package.ownAlpha", "req-observed-ownalpha", false)
	url, captured := stubGateway(t, processor.OperationReply{Status: processor.ReplyStatusAccepted, Decision: "committed"})
	srv.gatewayURL = url

	rec, body := callMarkApplied(t, srv, "mkrcpt1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %+v", rec.Code, body)
	}
	if body["installRequestId"] != "req-observed-ownalpha" {
		t.Errorf("reply installRequestId = %v, want the observed pointer", body["installRequestId"])
	}
	if body["packageKey"] != "vtx.package.ownAlpha" {
		t.Errorf("reply packageKey = %v, want the receipt's own key", body["packageKey"])
	}
	var payload map[string]any
	if err := json.Unmarshal(captured.Payload, &payload); err != nil {
		t.Fatalf("decode relayed payload: %v", err)
	}
	if payload["installRequestId"] != "req-observed-ownalpha" || payload["packageKey"] != "vtx.package.ownAlpha" {
		t.Errorf("relayed payload = %+v, want the receipt's package and observed pointer", payload)
	}
	// The op reads the package ROOT as well as its manifest, so the recovery
	// endpoint declares all four or the whole op fails on a hydration miss.
	wantReads := []string{
		"vtx.capabilityproposal.mkrcpt1.review",
		"vtx.capabilityproposal.mkrcpt1.target",
		"vtx.package.ownAlpha",
		"vtx.package.ownAlpha.manifest",
	}
	if strings.Join(captured.Reads, ",") != strings.Join(wantReads, ",") {
		t.Errorf("declared reads = %v, want %v", captured.Reads, wantReads)
	}
}

func TestApplyInstallRequestID(t *testing.T) {
	// The observed receipt names the actual commit, so it wins outright.
	observed := &pkgmgr.ApplyResult{
		Action: "install", PackageName: "alpha", ToVersion: "1.2.0",
		InstallRequestID: "req-observed-alpha",
	}
	if got := applyInstallRequestID(observed); got != "req-observed-alpha" {
		t.Errorf("with a receipt = %q, want the observed pointer", got)
	}
	// Without one there is only the reconstruction, which cannot tell this
	// apply from any other write at the same name and version.
	none := &pkgmgr.ApplyResult{Action: "skip", PackageName: "alpha", ToVersion: "1.2.0"}
	if got := applyInstallRequestID(none); got != "skip:alpha@1.2.0" {
		t.Errorf("without a receipt = %q, want the composed fallback", got)
	}
}

// stubGatewayRecording stands in for the Gateway across the MULTI-submit close:
// it records every relayed request in order and answers each from replyFor, so
// a test can assert what was submitted, in what order, and what the handler did
// with a per-op outcome.
func stubGatewayRecording(t *testing.T, replyFor func(gatewayOperationRequest) processor.OperationReply) (url string, relayed func() []gatewayOperationRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []gatewayOperationRequest
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req gatewayOperationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode relayed request: %v", err)
		}
		mu.Lock()
		seen = append(seen, req)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(replyFor(req))
	}))
	t.Cleanup(hs.Close)
	return hs.URL, func() []gatewayOperationRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]gatewayOperationRequest(nil), seen...)
	}
}

// acceptEvery is the stub reply for a close where both submits commit.
func acceptEvery(gatewayOperationRequest) processor.OperationReply {
	return processor.OperationReply{Status: processor.ReplyStatusAccepted, Decision: "committed"}
}

// rejectReceipt answers the receipt submit with a Processor refusal and commits
// everything else — the likelier of the two close failures, and the one that
// otherwise produces a clean 200 carrying no trace of it.
func rejectReceipt(req gatewayOperationRequest) processor.OperationReply {
	if req.OperationType == "RecordCapabilityInstallReceipt" {
		return processor.OperationReply{
			Status: processor.ReplyStatusRejected,
			Error:  &opwire.ReplyError{Code: "UnknownPackage", Message: "vtx.package.ownAlpha is not a live installed package"},
		}
	}
	return acceptEvery(req)
}

// callCloseApply drives the apply's close directly, with an operator token in
// the context (requireOperator, which normally puts it there, is not part of
// this fixture).
func callCloseApply(t *testing.T, srv *server, id string, res *pkgmgr.ApplyResult) (int, map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, operatorTokenContextKey{}, "test-operator-token")
	return srv.closeApply(ctx, id, "vtx.capabilityproposal."+id, res)
}

// committedApplyResult is the shape ApplyCapabilityPlan returns from an arm
// that actually committed: the Processor's receipt field is populated.
func committedApplyResult() *pkgmgr.ApplyResult {
	return &pkgmgr.ApplyResult{
		PackageName:      "alpha",
		PackageKey:       "vtx.package.ownAlpha",
		Action:           "install",
		ToVersion:        "1.2.0",
		InstallRequestID: "req-observed-ownalpha",
	}
}

func TestCloseApply_SubmitsReceiptBeforeMarkApplied(t *testing.T) {
	srv, _, _, _ := newTestReviewServerWithSrv(t)
	url, relayed := stubGatewayRecording(t, acceptEvery)
	srv.gatewayURL = url

	status, body := callCloseApply(t, srv, "close1", committedApplyResult())
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %+v", status, body)
	}
	if body["receipt"] != receiptRecorded {
		t.Errorf("receipt = %v, want %q", body["receipt"], receiptRecorded)
	}
	if _, present := body["receiptFailure"]; present {
		t.Errorf("a recorded receipt reported a failure: %+v", body)
	}
	if body["installRequestId"] != "req-observed-ownalpha" {
		t.Errorf("installRequestId = %v, want the observed pointer", body["installRequestId"])
	}

	ops := relayed()
	if len(ops) != 2 {
		t.Fatalf("relayed %d ops, want 2: %+v", len(ops), ops)
	}
	// Order is the point: the receipt is stamped while the proposal is still
	// approved — the state the op requires — and mark-applied flips it away.
	if ops[0].OperationType != "RecordCapabilityInstallReceipt" || ops[1].OperationType != "MarkCapabilityProposalApplied" {
		t.Fatalf("relayed order = [%s, %s]", ops[0].OperationType, ops[1].OperationType)
	}
	var payload map[string]any
	if err := json.Unmarshal(ops[0].Payload, &payload); err != nil {
		t.Fatalf("decode receipt payload: %v", err)
	}
	want := map[string]any{
		"proposalId":       "close1",
		"packageKey":       "vtx.package.ownAlpha",
		"installRequestId": "req-observed-ownalpha",
	}
	for k, v := range want {
		if payload[k] != v {
			t.Errorf("receipt payload[%s] = %v, want %v", k, payload[k], v)
		}
	}
	if len(payload) != len(want) {
		t.Errorf("receipt payload = %+v, want exactly the three declared fields", payload)
	}
	wantReads := []string{
		"vtx.capabilityproposal.close1.review",
		"vtx.capabilityproposal.close1.target",
		"vtx.package.ownAlpha",
		"vtx.package.ownAlpha.manifest",
	}
	// Both close ops run the same guards over the same four keys, and each
	// hydrates from its declared set alone — so both are pinned, not just the
	// new one.
	for i, op := range ops {
		if strings.Join(op.Reads, ",") != strings.Join(wantReads, ",") {
			t.Errorf("%s reads = %v, want %v", ops[i].OperationType, op.Reads, wantReads)
		}
	}
}

// A retry of the same close must carry the SAME requestId, so the Contract #4
// tracker collapses it. A minted one would reach the commit batch instead,
// where .install's create-only conditioning refuses it — and the close would
// then report "no receipt" while a valid one sits in KV.
func TestCloseApply_ReceiptRequestIDIsDerivedAndStable(t *testing.T) {
	srv, _, _, _ := newTestReviewServerWithSrv(t)
	url, relayed := stubGatewayRecording(t, acceptEvery)
	srv.gatewayURL = url

	callCloseApply(t, srv, "close6", committedApplyResult())
	callCloseApply(t, srv, "close6", committedApplyResult())
	ops := relayed()
	if len(ops) != 4 {
		t.Fatalf("relayed %d ops, want 4", len(ops))
	}
	first, second := ops[0].RequestID, ops[2].RequestID
	if first == "" {
		t.Fatal("the receipt submitted no requestId, so the Gateway mints one and a retry cannot dedup")
	}
	if first != second {
		t.Errorf("retry requestId = %q, want the first submit's %q", second, first)
	}
	if !keys.IsValidNanoID(first) {
		t.Errorf("requestId %q is not a valid Contract #1 NanoID, so the envelope is rejected before it is tracked", first)
	}
	// The paired vector: a receipt naming a DIFFERENT package is a different
	// receipt and must NOT collapse into the first one — create-only
	// conditioning is what arbitrates that, and it only gets to if the ids differ.
	other := committedApplyResult()
	other.PackageKey = "vtx.package.foreignAlpha"
	callCloseApply(t, srv, "close6", other)
	if got := relayed()[4].RequestID; got == first {
		t.Errorf("a receipt naming another package derived the same requestId %q, so it would dedup instead of being refused", got)
	}
}

// An arm that committed nothing has no install to bind, so recording one would
// stamp a fiction. That is "not applicable", a different fact from a receipt
// that was submitted and refused — and the response has to distinguish them.
func TestCloseApply_NoObservedReceiptIsNotApplicable(t *testing.T) {
	srv, _, _, _ := newTestReviewServerWithSrv(t)
	url, relayed := stubGatewayRecording(t, acceptEvery)
	srv.gatewayURL = url

	res := committedApplyResult()
	res.Action, res.Skipped, res.InstallRequestID = "skip", true, ""

	status, body := callCloseApply(t, srv, "close2", res)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %+v", status, body)
	}
	if body["receipt"] != receiptNotApplicable {
		t.Errorf("receipt = %v, want %q — nothing committed, so nothing was refused either", body["receipt"], receiptNotApplicable)
	}
	if body["installRequestId"] != "skip:alpha@1.2.0" {
		t.Errorf("installRequestId = %v, want the composed fallback", body["installRequestId"])
	}
	ops := relayed()
	if len(ops) != 1 || ops[0].OperationType != "MarkCapabilityProposalApplied" {
		t.Fatalf("relayed %+v, want the mark-applied submit alone", ops)
	}
}

// The non-fatal proof, and the invisibility fix with it: a rejected receipt is
// not a failed apply, so the close carries on and answers 200 — but a 200
// carrying no trace of the refusal is how a permission gap disables the whole
// feature unnoticed. The binding is unobtainable afterwards (.install is
// create-only and the op needs an approved proposal, which mark-applied has
// just flipped), so this response is the only place the fact ever surfaces.
func TestCloseApply_ReceiptFailureIsNonFatalAndVisible(t *testing.T) {
	srv, _, _, _ := newTestReviewServerWithSrv(t)
	url, relayed := stubGatewayRecording(t, rejectReceipt)
	srv.gatewayURL = url

	status, body := callCloseApply(t, srv, "close3", committedApplyResult())
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a failed receipt is not a failed apply; body %+v", status, body)
	}
	if body["receipt"] != receiptFailed {
		t.Errorf("receipt = %v, want %q", body["receipt"], receiptFailed)
	}
	failure, _ := body["receiptFailure"].(string)
	if !strings.Contains(failure, "not a live installed package") {
		t.Errorf("receiptFailure = %q, want the Processor's own reason on the SUCCESS path", failure)
	}
	ops := relayed()
	if len(ops) != 2 || ops[1].OperationType != "MarkCapabilityProposalApplied" {
		t.Fatalf("relayed %+v, want the close to carry on to mark-applied", ops)
	}
}

// When the close fails too, the operator's resumable error has to say what
// recovery will do. That holds for BOTH no-receipt outcomes — a refused submit
// and an apply with nothing to bind — because either leaves recovery resolving
// by name and version.
func TestCloseApply_MissingReceiptNamedInResumableError(t *testing.T) {
	rejectEverything := func(gatewayOperationRequest) processor.OperationReply {
		return processor.OperationReply{
			Status: processor.ReplyStatusRejected,
			Error:  &opwire.ReplyError{Code: "InvalidApplyTransition", Message: "proposal is not approved"},
		}
	}
	cases := []struct {
		name, id    string
		mutate      func(*pkgmgr.ApplyResult)
		wantReceipt string
	}{
		{"the receipt submit was refused", "close4", func(*pkgmgr.ApplyResult) {}, receiptFailed},
		{"the apply had nothing to bind", "close7", func(r *pkgmgr.ApplyResult) {
			r.Action, r.Skipped, r.InstallRequestID = "skip", true, ""
		}, receiptNotApplicable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _, _, _ := newTestReviewServerWithSrv(t)
			url, _ := stubGatewayRecording(t, rejectEverything)
			srv.gatewayURL = url

			res := committedApplyResult()
			c.mutate(res)
			status, body := callCloseApply(t, srv, c.id, res)
			if status != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502", status)
			}
			if body["resumable"] != true || body["receipt"] != c.wantReceipt {
				t.Errorf("body = %+v, want resumable:true and receipt:%q", body, c.wantReceipt)
			}
			if msg, _ := body["error"].(string); !strings.Contains(msg, "name and version alone") {
				t.Errorf("want the error to say what recovery falls back to, got %q", msg)
			}
		})
	}

	// The paired vector: with the receipt committed and only the close failing,
	// that clause must be absent — otherwise the assertions above pass for a
	// message that always carries it.
	srv, _, _, _ := newTestReviewServerWithSrv(t)
	url, _ := stubGatewayRecording(t, func(req gatewayOperationRequest) processor.OperationReply {
		if req.OperationType == "RecordCapabilityInstallReceipt" {
			return acceptEvery(req)
		}
		return rejectEverything(req)
	})
	srv.gatewayURL = url
	status, body := callCloseApply(t, srv, "close5", committedApplyResult())
	if status != http.StatusBadGateway || body["receipt"] != receiptRecorded {
		t.Fatalf("status = %d, body %+v", status, body)
	}
	if msg, _ := body["error"].(string); strings.Contains(msg, "name and version alone") {
		t.Errorf("a recorded receipt was still reported as missing: %q", msg)
	}
}

// A reply this console cannot interpret is not a commit. Reading success as
// "not rejected" would report an empty status as a landed receipt, which is the
// one direction a close must never guess in.
func TestMarkOpFailure_UnrecognizedStatusIsNotSuccess(t *testing.T) {
	if got := markOpFailure(&processor.OperationReply{}, nil); got == "" {
		t.Error("an empty reply status was reported as a commit")
	}
	// The paired positive: duplicate IS a commit — the Contract #4 tracker
	// collapsing a retry means the effect is in state, and a derived requestId
	// makes that the ordinary outcome of a retried receipt.
	if got := markOpFailure(&processor.OperationReply{Status: processor.ReplyStatusDuplicate}, nil); got != "" {
		t.Errorf("a duplicate reply = %q, want no failure", got)
	}
}

// seedUnprovenNewPackage is the board row's own state: a newPackage proposal,
// approved but with no receipt, and a live package that some OTHER writer
// installed at exactly the name and version the proposal targets. Everything
// the name+version heuristic can see says "closeable"; nothing at all says this
// proposal wrote it.
func seedUnprovenNewPackage(t *testing.T, put func(bucket, key, value string), id string) {
	t.Helper()
	putCapProposal(t, put, id, map[string]any{
		"intent": "approved, never applied", "kind": "lens", "content": validLensContent,
		"reviewState": "approved", "targetMode": "newPackage", "targetPackageName": "alpha",
		"targetNewVersion": "1.2.0",
	})
	putInstalledPackage(t, put, "foreignAlpha", "alpha", "1.2.0", false)
}

// TestReviewCapabilityMarkApplied_UnprovenNewPackageIsRefused is the board
// row's regression test: "a newPackage proposal is closed over a same-named
// package it never wrote."
//
// This is the state the row names, and it is the one the receipt cannot fix by
// existing: a proposal whose apply never ran has no receipt, so the resolution
// falls back to name+version and matches the foreign install exactly. The close
// has to refuse on the ABSENCE of provenance, not merely prefer provenance when
// it happens to be there.
func TestReviewCapabilityMarkApplied_UnprovenNewPackageIsRefused(t *testing.T) {
	client, base, put := newTestReviewServer(t)
	seedUnprovenNewPackage(t, put, "unproven1")

	res, body := postReview(t, client, base, "/api/review/capability/unproven1/mark-applied")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — this proposal was closed over a package nothing shows it wrote", res.StatusCode)
	}
	msg, _ := body["error"].(string)
	for _, want := range []string{"no install receipt is recorded", "newPackage", "lattice-pkg"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q does not mention %q — it must name the gap and the deliberate path out", msg, want)
		}
	}
}

// The positive vector for that refusal, and the proof it is a PROVENANCE check
// rather than a blanket block on newPackage: the identical proposal carrying a
// receipt for that same package closes normally.
func TestReviewCapabilityMarkApplied_ProvenNewPackageStillCloses(t *testing.T) {
	srv, _, _, put := newTestReviewServerWithSrv(t)
	seedUnprovenNewPackage(t, put, "unproven2")
	putInstallReceipt(t, put, "unproven2", "vtx.package.foreignAlpha", "req-observed-foreignalpha", false)
	url, _ := stubGateway(t, processor.OperationReply{Status: processor.ReplyStatusAccepted, Decision: "committed"})
	srv.gatewayURL = url

	rec, body := callMarkApplied(t, srv, "unproven2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a receipted newPackage proposal is closable; body %+v", rec.Code, body)
	}
	if body["installRequestId"] != "req-observed-foreignalpha" {
		t.Errorf("installRequestId = %v, want the observed pointer", body["installRequestId"])
	}
}

// The narrowing held: upgradeExisting keeps today's behaviour exactly. A live
// package of that name is that mode's own precondition — present before the
// apply by definition — so its presence never carried a provenance signal to
// lose, and the version preconditions own that mode instead.
func TestReviewCapabilityMarkApplied_UnprovenUpgradeStillCloses(t *testing.T) {
	srv, _, _, put := newTestReviewServerWithSrv(t)
	putCapProposal(t, put, "unproven3", map[string]any{
		"intent": "approved, upgrade whose close never landed", "kind": "lens", "content": validLensContent,
		"reviewState": "approved", "targetMode": "upgradeExisting", "targetPackageName": "alpha",
		"targetBaseVersion": "1.1.0", "targetNewVersion": "1.2.0",
	})
	putInstalledPackage(t, put, "liveAlpha", "alpha", "1.2.0", false)
	url, _ := stubGateway(t, processor.OperationReply{Status: processor.ReplyStatusAccepted, Decision: "committed"})
	srv.gatewayURL = url

	rec, body := callMarkApplied(t, srv, "unproven3")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — upgradeExisting must be untouched by the newPackage refusal; body %+v", rec.Code, body)
	}
	if body["installRequestId"] != "recovered:alpha@1.2.0" {
		t.Errorf("installRequestId = %v, want the reconstructed recovery pointer", body["installRequestId"])
	}
}

// The apply endpoint's half. Its 409 used to answer this exact state with
// resumable:true and "close it with mark-applied" — advice pointing straight at
// the close that is now refused, and the opposite of what ApplyCapabilityPlan
// says about the same state (ErrPackageNameClaimed: the artifact did NOT land).
func TestReviewCapabilityApply_UnprovenNewPackageIsNotResumable(t *testing.T) {
	srv, client, base, put := newTestReviewServerWithSrv(t)
	srv.adminActor = "vtx.identity.testAdminHJKMNPQRST"
	seedUnprovenNewPackage(t, put, "unproven4")

	res, body := postReview(t, client, base, "/api/review/capability/unproven4/apply")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", res.StatusCode)
	}
	if body["resumable"] == true {
		t.Fatalf("apply still steers this proposal into the mark-applied close that refuses it: %+v", body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "no install receipt is recorded") {
		t.Errorf("want the provenance refusal, got %+v", body)
	}

	// The paired vector: with the receipt present the SAME fixture is resumable
	// again, so the refusal above is the provenance check and not apply
	// refusing every already-installed newPackage target.
	putInstallReceipt(t, put, "unproven4", "vtx.package.foreignAlpha", "req-observed-foreignalpha", false)
	res, body = postReview(t, client, base, "/api/review/capability/unproven4/apply")
	if res.StatusCode != http.StatusConflict || body["resumable"] != true {
		t.Errorf("status = %d, body = %+v; want the receipted proposal classified resumable", res.StatusCode, body)
	}
}
