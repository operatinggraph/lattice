package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/jsstore"
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
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	ns := natstest.RunServer(opts)
	t.Cleanup(ns.Shutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: ns.ClientURL(), Name: "loupe-test"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)
	for _, bucket := range []string{capabilityauthor.CapabilityProposalsBucket, augur.AugurProposalsBucket} {
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

	srv := &server{conn: conn, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), natsTimeout: 5 * time.Second}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	return hs.Client(), hs.URL, put
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
		{http.MethodGet, "/api/review/capability/x/approve", http.StatusBadRequest},  // GET on a POST endpoint
		{http.MethodPost, "/api/review/augur/x/approve", http.StatusBadRequest},      // augur has no approve endpoint
		{http.MethodPost, "/api/review/augur/x/apply", http.StatusBadRequest},        // augur has no apply endpoint
		{http.MethodPost, "/api/review/capability/x/bogus", http.StatusBadRequest},   // unknown verb
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
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	ns := natstest.RunServer(opts)
	t.Cleanup(ns.Shutdown)

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
