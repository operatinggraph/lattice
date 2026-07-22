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

	"github.com/operatinggraph/lattice/internal/gateway/revocation"
	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestGatewayRevocations_ListsBucket pins the read seam of the F11 revoke
// surface: entries in the token-revocation bucket come back as sorted rows
// carrying the materializer's audit fields, and a doc-less/unparseable entry
// still lists by key (presence IS the revocation).
func TestGatewayRevocations_ListsBucket(t *testing.T) {
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	ns := natstest.RunServer(opts)
	defer ns.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: ns.ClientURL(), Name: "loupe-test"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()
	if _, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: revocation.BucketName}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	put := func(key, value string) {
		t.Helper()
		if _, err := conn.KVPut(ctx, revocation.BucketName, key, []byte(value)); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
	put("vtx.identity.zzz999", `{"revokedAt":"2026-07-03T10:00:00Z","by":"vtx.identity.admin1","reason":"compromised"}`)
	put("vtx.identity.aaa111", `not-json`)

	srv := &server{conn: conn, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), natsTimeout: 5 * time.Second}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	hs := httptest.NewServer(mux)
	defer hs.Close()

	res, err := hs.Client().Get(hs.URL + "/api/gateway/revocations")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var body struct {
		Revocations []revocationRow `json:"revocations"`
		Count       int             `json:"count"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Count != 2 || len(body.Revocations) != 2 {
		t.Fatalf("count = %d, rows = %+v, want 2", body.Count, body.Revocations)
	}
	// Sorted by actor: aaa111 (unparseable doc → key only) before zzz999.
	if body.Revocations[0].Actor != "vtx.identity.aaa111" || body.Revocations[0].By != "" {
		t.Errorf("row 0 = %+v, want bare aaa111", body.Revocations[0])
	}
	r1 := body.Revocations[1]
	if r1.Actor != "vtx.identity.zzz999" || r1.By != "vtx.identity.admin1" ||
		r1.Reason != "compromised" || r1.RevokedAt != "2026-07-03T10:00:00Z" {
		t.Errorf("row 1 = %+v, want the full audit fields", r1)
	}

	// An unrevoked (deleted) actor drops from the list.
	if err := conn.KVDelete(ctx, revocation.BucketName, "vtx.identity.aaa111"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	res2, err := hs.Client().Get(hs.URL + "/api/gateway/revocations")
	if err != nil {
		t.Fatalf("GET after delete: %v", err)
	}
	defer res2.Body.Close()
	var body2 struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(res2.Body).Decode(&body2); err != nil {
		t.Fatalf("decode after delete: %v", err)
	}
	if body2.Count != 1 {
		t.Errorf("count after unrevoke = %d, want 1", body2.Count)
	}

	// A read endpoint on the security surface answers only GET.
	res3, err := hs.Client().Post(hs.URL+"/api/gateway/revocations", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res3.Body.Close()
	if res3.StatusCode != http.StatusBadRequest {
		t.Errorf("POST status = %d, want 400", res3.StatusCode)
	}
}

// TestGatewayRevocations_BucketMissing pins the degraded shape: a stack whose
// bootstrap predates the kill-switch has no token-revocation bucket, and the
// endpoint reports that as an upstream error, not an empty (falsely clean)
// list.
func TestGatewayRevocations_BucketMissing(t *testing.T) {
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	ns := natstest.RunServer(opts)
	defer ns.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: ns.ClientURL(), Name: "loupe-test"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	srv := &server{conn: conn, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), natsTimeout: 5 * time.Second}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	hs := httptest.NewServer(mux)
	defer hs.Close()

	res, err := hs.Client().Get(hs.URL + "/api/gateway/revocations")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", res.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil || body.Error == "" {
		t.Fatalf("want an {error} body, got err=%v body=%+v", err, body)
	}
}
