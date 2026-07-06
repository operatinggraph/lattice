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
	privacybase "github.com/asolgan/lattice/packages/privacy-base"
)

// TestVaultShreds_ListsBucket pins the read seam of the F12 Vault page's
// shred-status summary: entries in the privacy-shreds bucket come back as
// sorted rows, and a doc-less/unparseable entry still lists by key (the key
// alone names a shredded identity).
func TestVaultShreds_ListsBucket(t *testing.T) {
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
	if _, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: privacybase.ShredStatusBucket}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	put := func(key, value string) {
		t.Helper()
		if _, err := conn.KVPut(ctx, privacybase.ShredStatusBucket, key, []byte(value)); err != nil {
			t.Fatalf("put %s: %v", key, err)
		}
	}
	put("vtx.identity.zzz999", `{"identityKey":"vtx.identity.zzz999","shredded":true,"shreddedAt":"2026-07-05T10:00:00Z",`+
		`"vaultKeyDestroyed":true,"vaultKeyDestroyedAt":"2026-07-05T10:00:01Z"}`)
	put("vtx.identity.aaa111", `not-json`)

	srv := &server{conn: conn, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), natsTimeout: 5 * time.Second}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	hs := httptest.NewServer(mux)
	defer hs.Close()

	res, err := hs.Client().Get(hs.URL + "/api/vault/shreds")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var body struct {
		Shreds []shredRow `json:"shreds"`
		Count  int        `json:"count"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Count != 2 || len(body.Shreds) != 2 {
		t.Fatalf("count = %d, rows = %+v, want 2", body.Count, body.Shreds)
	}
	// Sorted by identity key: aaa111 (unparseable doc → key only, unshredded
	// finalization fields) before zzz999.
	if body.Shreds[0].IdentityKey != "vtx.identity.aaa111" || body.Shreds[0].Shredded {
		t.Errorf("row 0 = %+v, want bare aaa111 unshredded", body.Shreds[0])
	}
	r1 := body.Shreds[1]
	if r1.IdentityKey != "vtx.identity.zzz999" || !r1.Shredded || !r1.VaultKeyDestroyed || r1.ProjectionsNullified {
		t.Errorf("row 1 = %+v, want shredded+vaultKeyDestroyed, projectionsNullified still pending", r1)
	}

	// An identity removed from the ledger (e.g. an identity-hygiene merge)
	// drops from the list.
	if err := conn.KVDelete(ctx, privacybase.ShredStatusBucket, "vtx.identity.aaa111"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	res2, err := hs.Client().Get(hs.URL + "/api/vault/shreds")
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
		t.Errorf("count after removal = %d, want 1", body2.Count)
	}

	// A read endpoint on the shred ledger answers only GET.
	res3, err := hs.Client().Post(hs.URL+"/api/vault/shreds", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res3.Body.Close()
	if res3.StatusCode != http.StatusBadRequest {
		t.Errorf("POST status = %d, want 400", res3.StatusCode)
	}
}

// TestVaultShreds_BucketMissing pins the degraded shape: a stack whose
// privacy-base package isn't installed has no privacy-shreds bucket, and the
// endpoint reports that as an upstream error, not a falsely clean empty list.
func TestVaultShreds_BucketMissing(t *testing.T) {
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

	res, err := hs.Client().Get(hs.URL + "/api/vault/shreds")
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
