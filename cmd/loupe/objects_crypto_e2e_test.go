package main

import (
	"bytes"
	"context"
	"encoding/base64"
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
	"github.com/operatinggraph/lattice/internal/objectcrypto"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

// sensitiveObjectFixture wires an embedded JetStream server with both a Core
// KV bucket and the core-objects Object Store, plus a fresh in-process Vault
// LocalBackend, and returns a ready httptest server whose GET /api/objects/*
// route is real (object-store-crypto-shred-design.md §3.4). Upload is
// deliberately NOT exercised here — handleSensitiveObjectUpload submits a
// real AttachObject op through the Processor, which this fixture has no fake
// responder for (deferred to Fire 4, per the Fire 2 checkpoint); this fire
// seeds the post-upload state directly, the same way vaultDecryptFixture's
// putSensitiveAspect seeds a post-commit aspect.
func sensitiveObjectFixture(t *testing.T) (hs *httptest.Server, backend *vault.LocalBackend, conn *substrate.Conn) {
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
	if _, err := conn.JetStream().CreateOrUpdateObjectStore(ctx, jetstream.ObjectStoreConfig{
		Bucket: bootstrap.CoreObjectsBucket, Storage: jetstream.FileStorage,
	}); err != nil {
		t.Fatalf("create core-objects bucket: %v", err)
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

	srv := &server{conn: conn, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), natsTimeout: 5 * time.Second, uploadCap: defaultUploadCap}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	hs = httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	return hs, backend, conn
}

// putSensitiveObjectDirect seeds the Core KV `.content` aspect + ciphertext
// bytes a real handleSensitiveObjectUpload commit would leave behind
// (object-store-crypto-shred-design.md §3.1/§3.2/§3.3): mint (or reuse)
// governingIdentity's DEK, seal plaintext under a fresh per-object CEK
// (oid-bound AAD, matching the upload handler), wrap the CEK under the DEK,
// and write both the piiKey envelope and the .content envelope directly —
// the same seeding shortcut vaultDecryptFixture's putSensitiveAspect takes
// for the aspect plane.
func putSensitiveObjectDirect(t *testing.T, ctx context.Context, conn *substrate.Conn, backend *vault.LocalBackend, governingIdentity string, plaintext []byte, contentType string) (oid string) {
	t.Helper()
	env, err := backend.CreateIdentityKey(ctx, governingIdentity)
	if err != nil {
		t.Fatalf("create identity key: %v", err)
	}
	envDoc, err := json.Marshal(map[string]any{"isDeleted": false, "data": env})
	if err != nil {
		t.Fatalf("marshal piiKey doc: %v", err)
	}
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, governingIdentity+".piiKey", envDoc); err != nil {
		t.Fatalf("put piiKey: %v", err)
	}

	plaintextDigest := objectcrypto.Digest(plaintext)
	oid = substrate.SHA256NanoID("object:" + governingIdentity + ":" + plaintextDigest)

	cek, err := objectcrypto.GenerateCEK()
	if err != nil {
		t.Fatalf("generate CEK: %v", err)
	}
	nonce, ciphertext, err := objectcrypto.Seal(cek, plaintext, []byte(oid))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	wrapped, err := backend.WrapKey(ctx, governingIdentity, env, cek)
	if err != nil {
		t.Fatalf("wrap CEK: %v", err)
	}

	storeName, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("new store name: %v", err)
	}
	if _, err := conn.ObjectPut(ctx, bootstrap.CoreObjectsBucket, storeName, bytes.NewReader(ciphertext), int64(len(ciphertext))); err != nil {
		t.Fatalf("put object bytes: %v", err)
	}

	contentDoc, err := json.Marshal(map[string]any{
		"isDeleted": false,
		"data": map[string]any{
			"digest": plaintextDigest, "size": len(ciphertext), "contentType": contentType,
			"storeName": storeName, "sensitive": true, "governingIdentity": governingIdentity,
			"encryption": map[string]any{
				"algo":       objectcrypto.ContentEncryptionAlgo,
				"nonce":      base64.StdEncoding.EncodeToString(nonce),
				"wrappedCEK": objectcrypto.EncodeWrappedCEK(wrapped),
				"keyId":      governingIdentity,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal content doc: %v", err)
	}
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, "vtx.object."+oid+".content", contentDoc); err != nil {
		t.Fatalf("put content doc: %v", err)
	}
	return oid
}

// TestSensitiveObjectGet_DefaultServesCiphertext pins the ciphertext-safe
// default (§3.4): a plain GET — no ?decrypt=true — returns the bytes exactly
// as stored, which for a sensitive object are ciphertext, never the
// plaintext, with no read-path authorization gate needed to make that safe.
func TestSensitiveObjectGet_DefaultServesCiphertext(t *testing.T) {
	hs, backend, conn := sensitiveObjectFixture(t)
	ctx := context.Background()
	plaintext := []byte("this is the applicant's signed lease PDF bytes")
	oid := putSensitiveObjectDirect(t, ctx, conn, backend, "vtx.identity.tenant1", plaintext, "application/pdf")

	res, err := hs.Client().Get(hs.URL + "/api/objects/" + oid)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	got, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if bytes.Equal(got, plaintext) {
		t.Fatal("default GET returned plaintext — sensitive object bytes must be ciphertext at rest")
	}
	if len(got) == 0 {
		t.Fatal("default GET returned empty body")
	}
}

// TestSensitiveObjectGet_DecryptTrue_RoundTrip pins the opt-in trusted-tool
// read: `?decrypt=true` unwraps the CEK via the Vault, decrypts locally, and
// serves back the exact original plaintext.
func TestSensitiveObjectGet_DecryptTrue_RoundTrip(t *testing.T) {
	hs, backend, conn := sensitiveObjectFixture(t)
	ctx := context.Background()
	plaintext := []byte("this is the applicant's signed lease PDF bytes")
	oid := putSensitiveObjectDirect(t, ctx, conn, backend, "vtx.identity.tenant2", plaintext, "application/pdf")

	res, err := hs.Client().Get(hs.URL + "/api/objects/" + oid + "?decrypt=true")
	if err != nil {
		t.Fatalf("GET decrypt=true: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	got, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("decrypted body = %q, want %q", got, plaintext)
	}
}

// TestSensitiveObjectGet_ShreddedIdentity_PermanentlyUndecryptable is the
// Fire 3 headline (object-store-crypto-shred-design.md §8 Fire 3, §6
// "Erasure"): once ShredIdentityKey destroys the governing identity's DEK,
// the wrapped CEK can never be unwrapped again, so the opt-in decrypt path
// fails closed — and the default path, already ciphertext-only, proves the
// bytes-at-rest were never recoverable through any other route either. This
// is the proof that Fire 1's DEK-wrapping alone (no blob-specific shred
// logic) makes a shredded identity's documents permanently gibberish.
func TestSensitiveObjectGet_ShreddedIdentity_PermanentlyUndecryptable(t *testing.T) {
	hs, backend, conn := sensitiveObjectFixture(t)
	ctx := context.Background()
	plaintext := []byte("applicant's SSN scan bytes")
	oid := putSensitiveObjectDirect(t, ctx, conn, backend, "vtx.identity.shredme", plaintext, "image/jpeg")

	if err := backend.ShredKey(ctx, "vtx.identity.shredme"); err != nil {
		t.Fatalf("shred key: %v", err)
	}

	// The opt-in decrypt path fails closed — it cannot unwrap the CEK.
	decRes, err := hs.Client().Get(hs.URL + "/api/objects/" + oid + "?decrypt=true")
	if err != nil {
		t.Fatalf("GET decrypt=true: %v", err)
	}
	defer decRes.Body.Close()
	if decRes.StatusCode == http.StatusOK {
		body, _ := io.ReadAll(decRes.Body)
		t.Fatalf("decrypt after shred: status = 200, want a failure; body = %s", body)
	}

	// The default path was always ciphertext-only, so it still serves the
	// same inert bytes — never plaintext, before or after the shred.
	defRes, err := hs.Client().Get(hs.URL + "/api/objects/" + oid)
	if err != nil {
		t.Fatalf("GET default: %v", err)
	}
	defer defRes.Body.Close()
	if defRes.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(defRes.Body)
		t.Fatalf("default GET after shred: status = %d, body = %s", defRes.StatusCode, body)
	}
	got, err := io.ReadAll(defRes.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if bytes.Equal(got, plaintext) {
		t.Fatal("default GET after shred returned plaintext — must remain inert ciphertext")
	}
}

// TestSensitiveObject_MultiPartyIndependentShred pins the §4.2 B-default
// shape: two identities each hold sensitive PII over byte-identical content
// (e.g. a lease both a landlord and tenant signed) — each gets its own
// identity-salted oid and its own envelope, so shredding one party's DEK
// leaves the other party's copy fully decryptable. No shared-CEK custody, no
// cross-identity coupling.
func TestSensitiveObject_MultiPartyIndependentShred(t *testing.T) {
	hs, backend, conn := sensitiveObjectFixture(t)
	ctx := context.Background()
	plaintext := []byte("the same signed lease PDF bytes, held by both parties")
	landlordOID := putSensitiveObjectDirect(t, ctx, conn, backend, "vtx.identity.landlord1", plaintext, "application/pdf")
	tenantOID := putSensitiveObjectDirect(t, ctx, conn, backend, "vtx.identity.tenant3", plaintext, "application/pdf")

	if landlordOID == tenantOID {
		t.Fatalf("identical bytes under two governing identities collapsed to one oid (%s) — cross-identity dedup must not happen for sensitive objects", landlordOID)
	}

	if err := backend.ShredKey(ctx, "vtx.identity.landlord1"); err != nil {
		t.Fatalf("shred landlord: %v", err)
	}

	// The landlord's copy is now permanently undecryptable.
	landlordRes, err := hs.Client().Get(hs.URL + "/api/objects/" + landlordOID + "?decrypt=true")
	if err != nil {
		t.Fatalf("GET landlord decrypt=true: %v", err)
	}
	defer landlordRes.Body.Close()
	if landlordRes.StatusCode == http.StatusOK {
		body, _ := io.ReadAll(landlordRes.Body)
		t.Fatalf("landlord decrypt after shred: status = 200, want failure; body = %s", body)
	}

	// The tenant's independent copy is entirely unaffected.
	tenantRes, err := hs.Client().Get(hs.URL + "/api/objects/" + tenantOID + "?decrypt=true")
	if err != nil {
		t.Fatalf("GET tenant decrypt=true: %v", err)
	}
	defer tenantRes.Body.Close()
	if tenantRes.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(tenantRes.Body)
		t.Fatalf("tenant decrypt after landlord's shred: status = %d, want 200; body = %s", tenantRes.StatusCode, body)
	}
	got, err := io.ReadAll(tenantRes.Body)
	if err != nil {
		t.Fatalf("read tenant body: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("tenant decrypted body = %q, want %q", got, plaintext)
	}
}

// TestSensitiveObjectGet_DemoMode_RevealRefused pins the one control standing
// between a hosted-demo visitor and decrypted PII (F20,
// loupe-f20-demo-operator-ux.md). A reveal is a GET, so the demo posture's
// method rule never sees it, and the vault unwrap RPC rides Loupe's own NATS
// credentials with no Lattice-Actor — so the demo operator's capability grants
// are not consulted either. If this check regresses, nothing else denies it.
func TestSensitiveObjectGet_DemoMode_RevealRefused(t *testing.T) {
	_, backend, conn := sensitiveObjectFixture(t)
	ctx := context.Background()
	plaintext := []byte("this is the applicant's signed lease PDF bytes")
	oid := putSensitiveObjectDirect(t, ctx, conn, backend, "vtx.identity.tenant2", plaintext, "application/pdf")

	demo := &server{
		conn: conn, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		natsTimeout: 5 * time.Second, uploadCap: defaultUploadCap, demoMode: true,
	}
	mux := http.NewServeMux()
	demo.registerRoutes(mux)
	hs := httptest.NewServer(mux)
	defer hs.Close()

	res, err := hs.Client().Get(hs.URL + "/api/objects/" + oid + "?decrypt=true")
	if err != nil {
		t.Fatalf("GET decrypt=true: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", res.StatusCode, body)
	}
	if bytes.Contains(body, plaintext) {
		t.Fatal("the refusal leaked the plaintext it was refusing to reveal")
	}
	if !bytes.Contains(body, []byte("read-only demo")) {
		t.Errorf("refusal should identify the demo posture, got %s", body)
	}

	// The non-decrypt path still serves (ciphertext), so demo mode narrows the
	// reveal rather than breaking object reads.
	plain, err := hs.Client().Get(hs.URL + "/api/objects/" + oid)
	if err != nil {
		t.Fatalf("GET without decrypt: %v", err)
	}
	defer plain.Body.Close()
	if plain.StatusCode != http.StatusOK {
		t.Errorf("ciphertext read status = %d, want 200", plain.StatusCode)
	}
}
