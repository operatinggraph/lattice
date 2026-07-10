package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/objectcrypto"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/vault"
	privacybase "github.com/asolgan/lattice/packages/privacy-base"
)

// sensitiveObjectFixture wires an embedded JetStream server carrying the
// weaver-targets read-model bucket (the objectAttachments lens rows this app
// reads, P5), the core-objects Object Store, the privacy-base piiKeyEnvelope
// bucket, a fresh in-process Vault, and dev JWT auth — proving the
// P5-compliant crypto path end to end without a real Refractor/Processor
// (object-store-crypto-shred-design.md §9 Fire 4 Increment 2). Mirrors
// cmd/loupe/objects_crypto_e2e_test.go's sensitiveObjectFixture, substituting
// the lens-projected read model for Loupe's direct Core-KV read.
func sensitiveObjectFixture(t *testing.T) (srv *server, hs *httptest.Server, backend *vault.LocalBackend, conn *substrate.Conn, mint func(sub string) string) {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	ns := natstest.RunServer(opts)
	t.Cleanup(ns.Shutdown)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: ns.ClientURL(), Name: "loftspace-app-test"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)
	if _, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bootstrap.WeaverTargetsBucket}); err != nil {
		t.Fatalf("create %s bucket: %v", bootstrap.WeaverTargetsBucket, err)
	}
	if _, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: privacybase.PiiKeyEnvelopeBucket}); err != nil {
		t.Fatalf("create %s bucket: %v", privacybase.PiiKeyEnvelopeBucket, err)
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

	t.Setenv("LOFTSPACE_APP_DEV_AUTH", "1")
	authn, signer, err := setupReadAuth(discardLogger(), true, nil)
	if err != nil {
		t.Fatalf("setupReadAuth: %v", err)
	}

	srv = &server{conn: conn, logger: discardLogger(), natsTimeout: testTimeout, uploadCap: defaultUploadCap, authn: authn, devSigner: signer}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	hs = httptest.NewServer(mux)
	t.Cleanup(hs.Close)

	mint = func(sub string) string {
		t.Helper()
		tok, _, err := signer.mint(sub)
		if err != nil {
			t.Fatalf("mint %s: %v", sub, err)
		}
		return tok
	}
	return srv, hs, backend, conn, mint
}

// seedPiiKeyEnvelope writes governingIdentity's piiKeyEnvelope lens row
// directly — the flat, unwrapped nats-kv shape a real Refractor projection
// leaves (packages/privacy-base/lenses.go's piiKeyEnvelopeSpec), keyed by the
// identity vertex key verbatim (the IntoKey default, no Output descriptor).
// CreateIdentityKey mints a FRESH random DEK on every call (not idempotent —
// internal/vault/local.go), so this is the single place a test identity's key
// is created; callers that also need to wrap/unwrap under it (sealAndStore)
// take the returned Envelope rather than creating a second, different DEK.
func seedPiiKeyEnvelope(t *testing.T, ctx context.Context, conn *substrate.Conn, backend *vault.LocalBackend, governingIdentity string) vault.Envelope {
	t.Helper()
	env, err := backend.CreateIdentityKey(ctx, governingIdentity)
	if err != nil {
		t.Fatalf("create identity key: %v", err)
	}
	row := map[string]any{
		"key": governingIdentity, "wrappedDEK": base64.StdEncoding.EncodeToString(env.WrappedDEK),
		"keyId": env.KeyID, "kekVersion": env.KEKVersion, "alg": env.Alg,
	}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal piiKeyEnvelope row: %v", err)
	}
	if _, err := conn.KVPut(ctx, privacybase.PiiKeyEnvelopeBucket, governingIdentity, raw); err != nil {
		t.Fatalf("put piiKeyEnvelope: %v", err)
	}
	return env
}

// seedObjectAttachmentRow writes the objectAttachments lens row for oid
// directly — the real DDL-persisted shape (packages/objects-base/ddls.go's
// attach_object) as the objects-base lens (packages/objects-base/lenses.go)
// would project it, so this proves against the on-wire row shape rather than
// a hand-picked one.
func seedObjectAttachmentRow(t *testing.T, ctx context.Context, conn *substrate.Conn, oid, storeName, contentType string, size int64, ownerKey string, sensitive bool, digest, governingIdentity string, enc map[string]any) {
	t.Helper()
	row := map[string]any{
		"entityKey": "vtx.object." + oid, "storeName": storeName, "contentType": contentType, "size": size,
		"owners": []map[string]any{{"ownerKey": ownerKey}},
	}
	if sensitive {
		row["sensitive"] = true
		row["governingIdentity"] = governingIdentity
		row["digest"] = digest
		row["encryption"] = enc
	}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal objectAttachments row: %v", err)
	}
	if _, err := conn.KVPut(ctx, bootstrap.WeaverTargetsBucket, "objectAttachments."+oid, raw); err != nil {
		t.Fatalf("put objectAttachments row: %v", err)
	}
}

// multipartUpload builds a POST /api/objects multipart body, optionally
// marking it sensitive, and returns the decoded JSON response.
func multipartUpload(t *testing.T, hs *httptest.Server, token string, content []byte, targetKey, linkName string, sensitive bool, governingIdentity string) (*http.Response, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "doc.bin")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	_ = mw.WriteField("targetKey", targetKey)
	_ = mw.WriteField("linkName", linkName)
	if sensitive {
		_ = mw.WriteField("sensitive", "true")
		_ = mw.WriteField("governingIdentity", governingIdentity)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, hs.URL+"/api/objects", &buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := hs.Client().Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer res.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	return res, body
}

// TestHandleObjectUpload_Sensitive_RequiresSelfIdentity proves the guard
// beyond Fire 2's Loupe shape: a caller may only encrypt under their OWN
// identity's DEK. Vault's WrapKey/UnwrapKey RPC trusts the caller wholesale
// (internal/vault/service.go's handleWrapKey never checks the caller against
// identityKey), so this app-level check is the only thing standing between
// an authenticated applicant and wrapping arbitrary bytes under someone
// else's governingIdentity.
func TestHandleObjectUpload_Sensitive_RequiresSelfIdentity(t *testing.T) {
	_, hs, _, _, mint := sensitiveObjectFixture(t)
	alice := "rHByTRQJyGuy9kFodCBF"
	victim := "vtx.identity.PQipBmNwsvkcQeoT37Az"

	res, body := multipartUpload(t, hs, mint(alice), []byte("hello"), "vtx.identity."+alice, "idDocument", true, victim)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (governingIdentity must be caller's own identity); body=%v", res.StatusCode, body)
	}
}

// TestHandleObjectUpload_Sensitive_RequiresAuth proves an unauthenticated
// caller cannot upload a sensitive document at all (unlike the ordinary
// byte-plane path, which needs no auth — the crypto path always requires
// knowing WHO the caller is to enforce the self-identity guard above).
func TestHandleObjectUpload_Sensitive_RequiresAuth(t *testing.T) {
	_, hs, _, _, _ := sensitiveObjectFixture(t)
	res, body := multipartUpload(t, hs, "", []byte("hello"), "vtx.identity.alice", "idDocument", true, "vtx.identity.alice")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%v", res.StatusCode, body)
	}
}

// TestHandleObjectUpload_Sensitive_SealsAndReturnsEnvelope proves the happy
// path: the applicant uploads their own ID document, the bytes land as
// ciphertext in core-objects (never plaintext), and the JSON response
// carries a complete encryption envelope + the identity-salted oid the
// browser needs to submit AttachObject itself.
func TestHandleObjectUpload_Sensitive_SealsAndReturnsEnvelope(t *testing.T) {
	_, hs, backend, conn, mint := sensitiveObjectFixture(t)
	ctx := context.Background()
	applicant := "vtx.identity.3LvdXTKPzXGFaGY6YpS3"
	seedPiiKeyEnvelope(t, ctx, conn, backend, applicant)

	plaintext := []byte("this is a scanned ID document, definitely PII")
	res, body := multipartUpload(t, hs, mint("3LvdXTKPzXGFaGY6YpS3"), plaintext, applicant, "idDocument", true, applicant)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%v", res.StatusCode, body)
	}
	if sensitive, _ := body["sensitive"].(bool); !sensitive {
		t.Fatalf("response must carry sensitive:true, got %v", body)
	}
	if body["governingIdentity"] != applicant {
		t.Fatalf("governingIdentity = %v, want %v", body["governingIdentity"], applicant)
	}
	enc, ok := body["encryption"].(map[string]any)
	if !ok || enc["wrappedCEK"] == "" || enc["keyId"] != applicant {
		t.Fatalf("encryption envelope missing/malformed: %v", body)
	}
	wantOid := substrate.SHA256NanoID("object:" + applicant + ":" + objectcrypto.Digest(plaintext))
	if body["oid"] != wantOid {
		t.Fatalf("oid = %v, want identity-salted %v", body["oid"], wantOid)
	}

	storeName, _ := body["storeName"].(string)
	rc, _, err := conn.ObjectGet(ctx, bootstrap.CoreObjectsBucket, storeName)
	if err != nil {
		t.Fatalf("read stored bytes: %v", err)
	}
	stored, _ := io.ReadAll(rc)
	_ = rc.Close()
	if bytes.Equal(stored, plaintext) {
		t.Fatalf("stored bytes must be ciphertext, got the plaintext verbatim")
	}
}

// TestHandleObjectGet_Sensitive_DefaultServesCiphertext pins the
// ciphertext-safe default: a plain GET (no ?decrypt=true) on a sensitive,
// identity-owned object returns exactly the stored ciphertext bytes, never
// the plaintext — proven end-to-end through the real HTTP handler + the
// existing D1.5 owner-entitlement gate.
func TestHandleObjectGet_Sensitive_DefaultServesCiphertext(t *testing.T) {
	_, hs, backend, conn, mint := sensitiveObjectFixture(t)
	ctx := context.Background()
	applicant := "vtx.identity.CxfkHaFmU5rq87ZnNwEC"
	env := seedPiiKeyEnvelope(t, ctx, conn, backend, applicant)

	plaintext := []byte("proof of income pay stub contents")
	oid, ciphertext, storeName, digest, enc := sealAndStore(t, ctx, conn, backend, applicant, env, plaintext)
	seedObjectAttachmentRow(t, ctx, conn, oid, storeName, "application/pdf", int64(len(ciphertext)), applicant, true, digest, applicant, enc)

	req, _ := http.NewRequest(http.MethodGet, hs.URL+"/api/objects/"+oid, nil)
	req.Header.Set("Authorization", "Bearer "+mint("CxfkHaFmU5rq87ZnNwEC"))
	res, err := hs.Client().Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	got, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if !bytes.Equal(got, ciphertext) {
		t.Fatalf("default GET must serve ciphertext verbatim, got %d bytes vs %d ciphertext bytes", len(got), len(ciphertext))
	}
	if bytes.Equal(got, plaintext) {
		t.Fatalf("default GET must NEVER serve the plaintext")
	}
}

// TestHandleObjectGet_Sensitive_DecryptTrue_RoundTrip proves ?decrypt=true
// round-trips to the exact original plaintext for the owning applicant.
func TestHandleObjectGet_Sensitive_DecryptTrue_RoundTrip(t *testing.T) {
	_, hs, backend, conn, mint := sensitiveObjectFixture(t)
	ctx := context.Background()
	applicant := "vtx.identity.tQ1UF6Q7GaTfr5ZLZYPt"
	env := seedPiiKeyEnvelope(t, ctx, conn, backend, applicant)

	plaintext := []byte("a passport scan's worth of bytes")
	oid, _, storeName, digest, enc := sealAndStore(t, ctx, conn, backend, applicant, env, plaintext)
	seedObjectAttachmentRow(t, ctx, conn, oid, storeName, "image/jpeg", int64(len(plaintext)), applicant, true, digest, applicant, enc)

	req, _ := http.NewRequest(http.MethodGet, hs.URL+"/api/objects/"+oid+"?decrypt=true", nil)
	req.Header.Set("Authorization", "Bearer "+mint("tQ1UF6Q7GaTfr5ZLZYPt"))
	res, err := hs.Client().Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	got, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("decrypt=true must round-trip to the exact plaintext, got %q", got)
	}
}

// TestHandleObjectGet_Sensitive_DecryptTrue_WrongUserDenied proves the
// existing D1.5 owner-entitlement gate (authorizeObjectGet) still applies on
// the decrypt branch — a stranger cannot decrypt someone else's ID document
// just by guessing the oid.
func TestHandleObjectGet_Sensitive_DecryptTrue_WrongUserDenied(t *testing.T) {
	_, hs, backend, conn, mint := sensitiveObjectFixture(t)
	ctx := context.Background()
	applicant := "vtx.identity.TvusEprLkxRLfaTrvmQu"
	env := seedPiiKeyEnvelope(t, ctx, conn, backend, applicant)

	plaintext := []byte("someone else's sensitive document")
	oid, _, storeName, digest, enc := sealAndStore(t, ctx, conn, backend, applicant, env, plaintext)
	seedObjectAttachmentRow(t, ctx, conn, oid, storeName, "image/jpeg", int64(len(plaintext)), applicant, true, digest, applicant, enc)

	req, _ := http.NewRequest(http.MethodGet, hs.URL+"/api/objects/"+oid+"?decrypt=true", nil)
	req.Header.Set("Authorization", "Bearer "+mint("hb8sfKSJRymkSyod9obZ"))
	res, err := hs.Client().Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (not entitled, indistinguishable from absent)", res.StatusCode)
	}
}

// TestHandleObjectGet_Sensitive_ShreddedIdentity_PermanentlyUndecryptable
// mirrors cmd/loupe's Fire 3 proof: ShredIdentityKey makes the opt-in decrypt
// path fail permanently, while the default (ciphertext) path is unaffected —
// no blob-specific shred logic; it falls out of Fire 1's DEK-wrapping.
func TestHandleObjectGet_Sensitive_ShreddedIdentity_PermanentlyUndecryptable(t *testing.T) {
	_, hs, backend, conn, mint := sensitiveObjectFixture(t)
	ctx := context.Background()
	applicant := "vtx.identity.W511DQ4C3WbTEPn8wWPs"
	env := seedPiiKeyEnvelope(t, ctx, conn, backend, applicant)

	plaintext := []byte("a document that will become permanently unrecoverable")
	oid, ciphertext, storeName, digest, enc := sealAndStore(t, ctx, conn, backend, applicant, env, plaintext)
	seedObjectAttachmentRow(t, ctx, conn, oid, storeName, "image/jpeg", int64(len(plaintext)), applicant, true, digest, applicant, enc)

	if err := backend.ShredKey(ctx, applicant); err != nil {
		t.Fatalf("shred key: %v", err)
	}

	token := mint("W511DQ4C3WbTEPn8wWPs")

	t.Run("decrypt=true now fails", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, hs.URL+"/api/objects/"+oid+"?decrypt=true", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := hs.Client().Do(req)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer res.Body.Close()
		if res.StatusCode == http.StatusOK {
			t.Fatalf("decrypt must fail once the identity is shredded, got 200")
		}
	})

	t.Run("default ciphertext path is unaffected", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, hs.URL+"/api/objects/"+oid, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := hs.Client().Do(req)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer res.Body.Close()
		got, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK || !bytes.Equal(got, ciphertext) {
			t.Fatalf("default path must keep serving the same inert ciphertext after a shred")
		}
	})
}

// sealAndStore replicates what a real sensitive handleObjectUpload commits
// (seal under a fresh CEK, wrap it, store the ciphertext) without going
// through the HTTP handler — used by the GET-side tests to seed state
// directly, mirroring cmd/loupe/objects_crypto_e2e_test.go's
// putSensitiveObjectDirect. Takes env (from seedPiiKeyEnvelope) rather than
// minting its own — CreateIdentityKey is not idempotent, so a second call for
// the same identity would wrap under a DIFFERENT DEK than the one already
// projected to the piiKeyEnvelope lens bucket, breaking UnwrapKey.
func sealAndStore(t *testing.T, ctx context.Context, conn *substrate.Conn, backend *vault.LocalBackend, governingIdentity string, env vault.Envelope, plaintext []byte) (oid string, ciphertext []byte, storeName, digest string, enc map[string]any) {
	t.Helper()
	digest = objectcrypto.Digest(plaintext)
	oid = substrate.SHA256NanoID("object:" + governingIdentity + ":" + digest)

	cek, err := objectcrypto.GenerateCEK()
	if err != nil {
		t.Fatalf("generate CEK: %v", err)
	}
	nonce, ct, err := objectcrypto.Seal(cek, plaintext, []byte(oid))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	wrapped, err := backend.WrapKey(ctx, governingIdentity, env, cek)
	if err != nil {
		t.Fatalf("wrap CEK: %v", err)
	}

	storeName, err = substrate.NewNanoID()
	if err != nil {
		t.Fatalf("new store name: %v", err)
	}
	if _, err := conn.ObjectPut(ctx, bootstrap.CoreObjectsBucket, storeName, bytes.NewReader(ct), int64(len(ct))); err != nil {
		t.Fatalf("put object bytes: %v", err)
	}

	enc = map[string]any{
		"algo": objectcrypto.ContentEncryptionAlgo, "nonce": base64.StdEncoding.EncodeToString(nonce),
		"wrappedCEK": objectcrypto.EncodeWrappedCEK(wrapped), "keyId": governingIdentity,
	}
	return oid, ct, storeName, digest, enc
}
