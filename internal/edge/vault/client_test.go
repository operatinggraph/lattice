package vault_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	edgeoverlay "github.com/operatinggraph/lattice/internal/edge/overlay"
	edgestore "github.com/operatinggraph/lattice/internal/edge/store"
	"github.com/operatinggraph/lattice/internal/edge/transport/natstransport"
	edgevault "github.com/operatinggraph/lattice/internal/edge/vault"
	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	corevault "github.com/operatinggraph/lattice/internal/vault"
)

// testIdentityID is a valid Contract #1 NanoID used throughout — mirrors
// internal/refractor/control's personal_sessionkey_test.go fixture.
const testIdentityID = "AAAAAAAAAAAAAAAAAAAA"

// fakeCoreKV is a minimal coreKVGetter test double: an in-memory key->entry
// map, mirroring internal/refractor/control/personal_sessionkey_test.go's
// own fakeCoreKV (unexported there, so duplicated here at the package
// boundary rather than imported).
type fakeCoreKV struct {
	entries map[string]*substrate.KVEntry
}

func (f *fakeCoreKV) Get(_ context.Context, key string) (*substrate.KVEntry, error) {
	e, ok := f.entries[key]
	if !ok {
		return nil, substrate.ErrKeyNotFound
	}
	return e, nil
}

func piiKeyEntry(t *testing.T, envelope corevault.Envelope) *substrate.KVEntry {
	t.Helper()
	value, err := json.Marshal(struct {
		Data corevault.Envelope `json:"data"`
	}{Data: envelope})
	require.NoError(t, err)
	return &substrate.KVEntry{Value: value}
}

func openTestStore(t *testing.T) edgestore.Store {
	t.Helper()
	st, err := edgestore.Open(filepath.Join(t.TempDir(), "edge.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestNew_RejectsInvalidIdentityID(t *testing.T) {
	_, err := edgevault.New(nil, edgevault.Config{IdentityID: "not-a-nanoid"})
	require.Error(t, err)
}

func TestClient_Decrypt_RoundTrip(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	identityKey := "vtx.identity." + testIdentityID
	kek := make([]byte, 32)
	_, err := rand.Read(kek)
	require.NoError(t, err)

	// Build the envelope first (outside the running backend — CreateIdentityKey
	// needs a backend instance, so build it against a throwaway one with the
	// same KEK, mirroring how the real piiKey aspect is authored once and
	// read many times).
	seed, err := corevault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)
	envelope, err := seed.CreateIdentityKey(ctx, identityKey)
	require.NoError(t, err)

	conn, backend := startTestControlServiceWithKEK(t, ctx, envelope, kek)
	plaintext := []byte(`{"ssn":"123-45-6789"}`)
	ct, err := backend.Encrypt(ctx, identityKey, envelope, plaintext)
	require.NoError(t, err)

	client, err := edgevault.New(natstransport.New(conn), edgevault.Config{IdentityID: testIdentityID, TTL: time.Minute})
	require.NoError(t, err)

	got, err := client.Decrypt(ctx, ct)
	require.NoError(t, err)
	assert.Equal(t, plaintext, got)
}

// startTestControlServiceWithKEK is startTestControlService but takes an
// explicit kek so the caller can mint the identity's envelope and encrypt
// under the exact same backend instance the control service serves from
// (the DEK cache is per-backend-instance, and Decrypt/Encrypt must agree on
// the one true DEK for the identity).
func startTestControlServiceWithKEK(t *testing.T, ctx context.Context, envelope corevault.Envelope, kek []byte) (*substrate.Conn, *corevault.LocalBackend) {
	t.Helper()
	url := testutil.StartEmbeddedNATS(t)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url})
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	backend, err := corevault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetCoreKV(&fakeCoreKV{entries: map[string]*substrate.KVEntry{
		"vtx.identity." + testIdentityID + ".piiKey": piiKeyEntry(t, envelope),
	}})
	svc.SetVault(backend)
	require.NoError(t, svc.StartNATSListener(ctx, conn.NATS()))

	return conn, backend
}

func TestClient_Decrypt_CachesSessionKeyAcrossCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	identityKey := "vtx.identity." + testIdentityID
	kek := make([]byte, 32)
	_, err := rand.Read(kek)
	require.NoError(t, err)
	seed, err := corevault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)
	envelope, err := seed.CreateIdentityKey(ctx, identityKey)
	require.NoError(t, err)

	conn, backend := startTestControlServiceWithKEK(t, ctx, envelope, kek)
	ct1, err := backend.Encrypt(ctx, identityKey, envelope, []byte("first"))
	require.NoError(t, err)
	ct2, err := backend.Encrypt(ctx, identityKey, envelope, []byte("second"))
	require.NoError(t, err)

	client, err := edgevault.New(natstransport.New(conn), edgevault.Config{IdentityID: testIdentityID, TTL: time.Hour})
	require.NoError(t, err)

	got1, err := client.Decrypt(ctx, ct1)
	require.NoError(t, err)
	assert.Equal(t, []byte("first"), got1)

	// Stop the control service's transport so a second RPC would fail —
	// proving this second Decrypt call reuses the cached session key rather
	// than round-tripping to the (now-unreachable) control plane again.
	conn.Close()

	got2, err := client.Decrypt(ctx, ct2)
	require.NoError(t, err)
	assert.Equal(t, []byte("second"), got2)
}

func TestClient_Decrypt_ShreddedIdentity_Denied(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	identityKey := "vtx.identity." + testIdentityID
	kek := make([]byte, 32)
	_, err := rand.Read(kek)
	require.NoError(t, err)
	seed, err := corevault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)
	envelope, err := seed.CreateIdentityKey(ctx, identityKey)
	require.NoError(t, err)

	conn, backend := startTestControlServiceWithKEK(t, ctx, envelope, kek)
	ct, err := backend.Encrypt(ctx, identityKey, envelope, []byte("payload"))
	require.NoError(t, err)
	require.NoError(t, backend.ShredKey(ctx, identityKey))

	client, err := edgevault.New(natstransport.New(conn), edgevault.Config{IdentityID: testIdentityID})
	require.NoError(t, err)

	_, err = client.Decrypt(ctx, ct)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shredded")
}

func TestReader_Read_PassesThroughPlaintext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	envelope := corevault.Envelope{} // unused: this test never decrypts.
	conn, _ := startTestControlServiceWithKEK(t, ctx, envelope, mustRandKEK(t))
	client, err := edgevault.New(natstransport.New(conn), edgevault.Config{IdentityID: testIdentityID})
	require.NoError(t, err)

	st := openTestStore(t)
	ov := edgeoverlay.New(st)
	_, err = st.ApplyUpsert("vtx.lease.Lk2Pn6mQrtwzKbcXvP3T", 1, json.RawMessage(`{"status":"active"}`))
	require.NoError(t, err)

	reader := edgevault.NewReader(ov, client)
	v, ok, err := reader.Read(ctx, "vtx.lease.Lk2Pn6mQrtwzKbcXvP3T")
	require.NoError(t, err)
	require.True(t, ok)
	assert.JSONEq(t, `{"status":"active"}`, string(v.Data))
}

func TestReader_Read_DecryptsSensitiveAspect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	identityKey := "vtx.identity." + testIdentityID
	kek := mustRandKEK(t)
	seed, err := corevault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)
	envelope, err := seed.CreateIdentityKey(ctx, identityKey)
	require.NoError(t, err)

	conn, backend := startTestControlServiceWithKEK(t, ctx, envelope, kek)
	plaintext := []byte(`{"ssn":"123-45-6789"}`)
	ct, err := backend.Encrypt(ctx, identityKey, envelope, plaintext)
	require.NoError(t, err)
	ctJSON, err := json.Marshal(ct)
	require.NoError(t, err)

	client, err := edgevault.New(natstransport.New(conn), edgevault.Config{IdentityID: testIdentityID})
	require.NoError(t, err)

	st := openTestStore(t)
	ov := edgeoverlay.New(st)
	_, err = st.ApplyUpsert("vtx.identity."+testIdentityID+".ssn", 1, ctJSON)
	require.NoError(t, err)

	reader := edgevault.NewReader(ov, client)
	v, ok, err := reader.Read(ctx, "vtx.identity."+testIdentityID+".ssn")
	require.NoError(t, err)
	require.True(t, ok)
	assert.JSONEq(t, string(plaintext), string(v.Data))
}

func mustRandKEK(t *testing.T) []byte {
	t.Helper()
	kek := make([]byte, 32)
	_, err := rand.Read(kek)
	require.NoError(t, err)
	return kek
}
