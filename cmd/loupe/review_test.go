package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/substrate"
	capabilityauthor "github.com/asolgan/lattice/packages/capability-author"
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
// server with the capability-proposals bucket created, wires it into a
// server + httptest.Server, and returns the client + a put helper. Mirrors
// vault_test.go's TestVaultShreds_ListsBucket pattern — the shared dev stack
// doesn't have packages/capability-author installed, so this is the only way
// to exercise the real HTTP handler end-to-end.
func newTestReviewServer(t *testing.T) (client *http.Client, baseURL string, put func(key, value string)) {
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
	if _, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: capabilityauthor.CapabilityProposalsBucket}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	put = func(key, value string) {
		t.Helper()
		putCtx, putCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer putCancel()
		if _, err := conn.KVPut(putCtx, capabilityauthor.CapabilityProposalsBucket, key, []byte(value)); err != nil {
			t.Fatalf("put %s: %v", key, err)
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
	put("vtx.capabilityproposal.pend1",
		`{"key":"vtx.capabilityproposal.pend1","intent":"list active providers by specialty","kind":"lens",`+
			`"reviewState":"pending","confidence":0.86,"model":"claude","reasonedAt":"2026-07-18T00:00:00Z"}`)
	put("vtx.capabilityproposal.authoring1",
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
	put("vtx.capabilityproposal.det1",
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

func TestHandleReview_RoutingErrors(t *testing.T) {
	client, base, _ := newTestReviewServer(t)

	cases := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodPost, "/api/review/capability", http.StatusBadRequest},
		{http.MethodGet, "/api/review/augur", http.StatusBadRequest},
		{http.MethodGet, "/api/review/", http.StatusBadRequest},
		{http.MethodGet, "/api/review/capability/a/b", http.StatusBadRequest},
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
