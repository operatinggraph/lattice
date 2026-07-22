package vault_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/edge/transport/natstransport"
	edgevault "github.com/operatinggraph/lattice/internal/edge/vault"
	corevault "github.com/operatinggraph/lattice/internal/vault"
)

// meRow builds the shape packages/edge-manifest's edgeIdentitySpec projects
// for `manifest.me`: displayName resolves to null against a sealed aspect,
// and sealedName carries the envelope itself.
func meRow(t *testing.T, sealed *corevault.Ciphertext, plaintextName string) json.RawMessage {
	t.Helper()
	row := map[string]any{
		"identityKey": "vtx.identity." + testIdentityID,
		"claimed":     true,
	}
	if sealed != nil {
		row["displayName"] = nil
		row["sealedName"] = sealed
	}
	if plaintextName != "" {
		row["displayName"] = plaintextName
	}
	data, err := json.Marshal(row)
	require.NoError(t, err)
	return data
}

// sealNameAspect seals the identity-domain `name` aspect's data object the
// way the Processor's step 6.5 does — it encrypts the whole `data` map, so
// the plaintext is {"value": "..."}, not the bare string.
func sealNameAspect(t *testing.T, ctx context.Context, backend *corevault.LocalBackend, envelope corevault.Envelope, name string) corevault.Ciphertext {
	t.Helper()
	plaintext, err := json.Marshal(map[string]string{"value": name})
	require.NoError(t, err)
	ct, err := backend.Encrypt(ctx, "vtx.identity."+testIdentityID, envelope, plaintext)
	require.NoError(t, err)
	return ct
}

// newSealedFixture starts a control service and returns a decorator wired to
// it plus the sealed `name` ciphertext for the given name.
func newSealedFixture(t *testing.T, ctx context.Context, name string) (*edgevault.SelfName, corevault.Ciphertext, *corevault.LocalBackend) {
	t.Helper()
	identityKey := "vtx.identity." + testIdentityID
	kek := make([]byte, 32)
	_, err := rand.Read(kek)
	require.NoError(t, err)
	seed, err := corevault.NewLocalBackend(kek, "v1")
	require.NoError(t, err)
	envelope, err := seed.CreateIdentityKey(ctx, identityKey)
	require.NoError(t, err)

	conn, backend := startTestControlServiceWithKEK(t, ctx, envelope, kek)
	ct := sealNameAspect(t, ctx, backend, envelope, name)

	client, err := edgevault.New(natstransport.New(conn), edgevault.Config{IdentityID: testIdentityID, TTL: time.Hour})
	require.NoError(t, err)
	return edgevault.NewSelfName(client), ct, backend
}

func displayNameOf(t *testing.T, data json.RawMessage) any {
	t.Helper()
	var row map[string]any
	require.NoError(t, json.Unmarshal(data, &row))
	return row["displayName"]
}

// The N3 green bar: a sealed self-name reaches the renderer as a plaintext
// displayName, decrypted in memory by the engine running as that identity.
func TestSelfName_Decorate_FillsDisplayNameFromSealedEnvelope(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	selfName, ct, _ := newSealedFixture(t, ctx, "Sam Okafor")

	got := selfName.Decorate(ctx, "manifest.me", meRow(t, &ct, ""))
	assert.Equal(t, "Sam Okafor", displayNameOf(t, got))
}

// The shred story at the display surface: once the identity's key is
// shredded the session-key request is refused permanently, so the row is
// left alone and the renderer's floor rule paints the typed fallback. The
// name must never survive the shred as a cached or partially-decoded value.
func TestSelfName_Decorate_ShreddedIdentity_LeavesRowUndecorated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	selfName, ct, backend := newSealedFixture(t, ctx, "Sam Okafor")
	require.NoError(t, backend.ShredKey(ctx, "vtx.identity."+testIdentityID))

	got := selfName.Decorate(ctx, "manifest.me", meRow(t, &ct, ""))
	assert.Nil(t, displayNameOf(t, got))
}

// A row whose displayName already resolved (a stack with no Vault, where the
// aspect was never sealed) is passed through byte-for-byte — no session key
// is requested for a name that is already there.
func TestSelfName_Decorate_PlaintextDisplayName_PassesThrough(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	selfName, _, _ := newSealedFixture(t, ctx, "Sam Okafor")

	row := meRow(t, nil, "Demo Tenant")
	got := selfName.Decorate(ctx, "manifest.me", row)
	assert.Equal(t, string(row), string(got))
}

// Only the me-row carries a sealed self-name; every other manifest row is
// handed through untouched.
func TestSelfName_Decorate_OtherKeys_PassThrough(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	selfName, ct, _ := newSealedFixture(t, ctx, "Sam Okafor")

	row := meRow(t, &ct, "")
	got := selfName.Decorate(ctx, "manifest.svc.abc", row)
	assert.Equal(t, string(row), string(got))
}

// A nil decorator is the posture of an engine wired without a Vault control
// plane — every row passes through rather than panicking.
func TestSelfName_Decorate_NilDecorator_PassesThrough(t *testing.T) {
	var selfName *edgevault.SelfName
	row := json.RawMessage(`{"identityKey":"vtx.identity.x"}`)
	assert.Equal(t, string(row), string(selfName.Decorate(context.Background(), "manifest.me", row)))
}
