package main

import (
	"bytes"
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

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
	privacybase "github.com/operatinggraph/lattice/packages/privacy-base"
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

// vaultDecryptFixture wires an embedded JetStream server + Core KV bucket +
// a live lattice.vault.decrypt responder (the Loupe→Vault RPC F12's Reveal
// proxies to), and returns a ready httptest server plus the backend so a
// test can shred a key mid-scenario.
func vaultDecryptFixture(t *testing.T) (hs *httptest.Server, backend *vault.LocalBackend, conn *substrate.Conn) {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	ns := natstest.RunServer(opts)
	t.Cleanup(ns.Shutdown)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: ns.ClientURL(), Name: "loupe-test"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)
	if _, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bootstrap.CoreKVBucket}); err != nil {
		t.Fatalf("create core-kv bucket: %v", err)
	}

	kek := make([]byte, 32)
	backend, err = vault.NewLocalBackend(kek, "v1")
	if err != nil {
		t.Fatalf("new local backend: %v", err)
	}
	svc := vault.NewService(backend, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := svc.StartNATSListener(ctx, conn.NATS()); err != nil {
		t.Fatalf("start vault listener: %v", err)
	}

	srv := &server{conn: conn, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), natsTimeout: 5 * time.Second}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	hs = httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	return hs, backend, conn
}

// putSensitiveAspect encrypts plaintext under identityKey's DEK (minting the
// key if absent) and writes both the aspect ciphertext and the identity's
// piiKey envelope into Core KV — the shape a real Processor commit-path
// write leaves behind (Contract #3 §3.10).
func putSensitiveAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, backend *vault.LocalBackend, identityKey, aspectKey string, plaintext []byte) {
	t.Helper()
	env, err := backend.CreateIdentityKey(ctx, identityKey)
	if err != nil {
		t.Fatalf("create identity key: %v", err)
	}
	ct, err := backend.Encrypt(ctx, identityKey, env, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	envDoc, err := json.Marshal(map[string]any{"isDeleted": false, "data": env})
	if err != nil {
		t.Fatalf("marshal piiKey doc: %v", err)
	}
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, identityKey+".piiKey", envDoc); err != nil {
		t.Fatalf("put piiKey: %v", err)
	}
	aspectDoc, err := json.Marshal(map[string]any{"isDeleted": false, "data": ct})
	if err != nil {
		t.Fatalf("marshal aspect doc: %v", err)
	}
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, aspectKey, aspectDoc); err != nil {
		t.Fatalf("put aspect: %v", err)
	}
}

// TestVaultDecrypt_RoundTrip pins the Reveal happy path end to end: a Core-KV
// sensitive aspect + its identity's piiKey envelope decrypt over the real
// lattice.vault.decrypt RPC back into the original plaintext JSON.
func TestVaultDecrypt_RoundTrip(t *testing.T) {
	hs, backend, conn := vaultDecryptFixture(t)
	ctx := context.Background()
	putSensitiveAspect(t, ctx, conn, backend, "vtx.identity.abc123", "vtx.identity.abc123.ssn", []byte(`{"value":"123-45-6789"}`))

	res, err := hs.Client().Post(hs.URL+"/api/vault/decrypt", "application/json",
		bytes.NewReader([]byte(`{"aspectKey":"vtx.identity.abc123.ssn"}`)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	var body struct {
		Plaintext json.RawMessage `json:"plaintext"`
		Shredded  bool            `json:"shredded"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Shredded {
		t.Fatal("shredded = true, want a live reveal")
	}
	var got struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body.Plaintext, &got); err != nil || got.Value != "123-45-6789" {
		t.Fatalf("plaintext = %s, want {value: 123-45-6789}", body.Plaintext)
	}
}

// TestVaultDecrypt_ShreddedIdentity pins the crypto-shred proof's "after"
// state: once ShredIdentityKey has run, Reveal reports {"shredded":true}
// rather than a generic error, so the UI can render "permanently unreadable".
func TestVaultDecrypt_ShreddedIdentity(t *testing.T) {
	hs, backend, conn := vaultDecryptFixture(t)
	ctx := context.Background()
	putSensitiveAspect(t, ctx, conn, backend, "vtx.identity.shredme", "vtx.identity.shredme.ssn", []byte(`{"value":"111-22-3333"}`))
	if err := backend.ShredKey(ctx, "vtx.identity.shredme"); err != nil {
		t.Fatalf("shred key: %v", err)
	}

	res, err := hs.Client().Post(hs.URL+"/api/vault/decrypt", "application/json",
		bytes.NewReader([]byte(`{"aspectKey":"vtx.identity.shredme.ssn"}`)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	var body struct {
		Shredded bool `json:"shredded"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Shredded {
		t.Error("shredded = false, want true after ShredKey")
	}
}

// TestVaultDecrypt_RejectsMalformedRequests pins the guard rails: a
// non-aspect key, a non-sensitive (plaintext) aspect, and a non-POST method
// all fail closed rather than reaching the Vault RPC.
func TestVaultDecrypt_RejectsMalformedRequests(t *testing.T) {
	hs, _, conn := vaultDecryptFixture(t)
	ctx := context.Background()

	// A vertex root, not an aspect.
	res1, err := hs.Client().Post(hs.URL+"/api/vault/decrypt", "application/json",
		bytes.NewReader([]byte(`{"aspectKey":"vtx.identity.abc123"}`)))
	if err != nil {
		t.Fatalf("POST vertex key: %v", err)
	}
	defer res1.Body.Close()
	if res1.StatusCode != http.StatusBadRequest {
		t.Errorf("vertex key status = %d, want 400", res1.StatusCode)
	}

	// A real aspect whose data is plaintext, not a ciphertext envelope.
	plainDoc, _ := json.Marshal(map[string]any{"isDeleted": false, "data": map[string]any{"value": "not encrypted"}})
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, "vtx.identity.abc123.nickname", plainDoc); err != nil {
		t.Fatalf("put plain aspect: %v", err)
	}
	res2, err := hs.Client().Post(hs.URL+"/api/vault/decrypt", "application/json",
		bytes.NewReader([]byte(`{"aspectKey":"vtx.identity.abc123.nickname"}`)))
	if err != nil {
		t.Fatalf("POST plaintext aspect: %v", err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusBadRequest {
		t.Errorf("plaintext aspect status = %d, want 400", res2.StatusCode)
	}

	// GET is not accepted — this is a privileged, state-inspecting RPC proxy,
	// mirrored on the POST-only /api/op convention.
	res3, err := hs.Client().Get(hs.URL + "/api/vault/decrypt")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res3.Body.Close()
	if res3.StatusCode != http.StatusBadRequest {
		t.Errorf("GET status = %d, want 400", res3.StatusCode)
	}

	// A sensitive-shaped aspect hanging off a meta-vertex (not an identity) —
	// Contract #1 §1.6 anchors sensitive aspects to identities only. This
	// must fail closed with a clear 400, not a misleading "no piiKey" 502.
	metaCT, _ := json.Marshal(map[string]any{
		"isDeleted": false,
		"data":      map[string]any{"ct": "YWJj", "nonce": "bnBu", "keyId": "k1"},
	})
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, "vtx.meta.notAnIdentity.ssn", metaCT); err != nil {
		t.Fatalf("put meta aspect: %v", err)
	}
	res4, err := hs.Client().Post(hs.URL+"/api/vault/decrypt", "application/json",
		bytes.NewReader([]byte(`{"aspectKey":"vtx.meta.notAnIdentity.ssn"}`)))
	if err != nil {
		t.Fatalf("POST meta-anchored aspect: %v", err)
	}
	defer res4.Body.Close()
	if res4.StatusCode != http.StatusBadRequest {
		t.Errorf("meta-anchored aspect status = %d, want 400", res4.StatusCode)
	}

	// A ciphertext envelope missing nonce/keyId is incomplete, not decryptable
	// — reject locally rather than forwarding a partial envelope to the RPC.
	partialCT, _ := json.Marshal(map[string]any{
		"isDeleted": false,
		"data":      map[string]any{"ct": "YWJj"},
	})
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, "vtx.identity.abc123.partial", partialCT); err != nil {
		t.Fatalf("put partial-envelope aspect: %v", err)
	}
	res5, err := hs.Client().Post(hs.URL+"/api/vault/decrypt", "application/json",
		bytes.NewReader([]byte(`{"aspectKey":"vtx.identity.abc123.partial"}`)))
	if err != nil {
		t.Fatalf("POST partial envelope: %v", err)
	}
	defer res5.Body.Close()
	if res5.StatusCode != http.StatusBadRequest {
		t.Errorf("partial envelope status = %d, want 400", res5.StatusCode)
	}
}
