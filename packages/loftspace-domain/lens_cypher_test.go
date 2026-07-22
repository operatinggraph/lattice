package loftspacedomain

// Rule-engine proof of the applicantRosterRead SECURE-LENS cypher.
//
// These tests drive applicantRosterReadSpec through the `full` rule engine
// directly — the same engine selected at activation via engine:"full" —
// against an embedded NATS Core/Adjacency KV seeded with ENVELOPE-shaped
// sensitive aspects (the identity `name` holds only {ct, nonce, keyId} at
// rest, Contract #3 §3.10). They prove:
//
//   - a named identity projects exactly one row whose `name` column carries
//     the ciphertext envelope WHOLE (the shape pipeline/secure.go's
//     SecureDecryptor requires — it decrypts the map, never the engine);
//   - the WHERE keys on ciphertext PRESENCE (i.name.data.ct <> null): an
//     unnamed/service identity projects no row, and a hypothetical
//     plaintext-shaped name ({value: ...}, no ct) also projects no row —
//     this lens can never carry plaintext PII into the table by itself.
//
// The decrypt half (envelope → plaintext under the owning identity's DEK,
// shredded → NULL) is the platform's, proven in
// internal/refractor/pipeline/secure_internal_test.go.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func cypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
	t.Helper()
	opts := &natsserver.Options{JetStream: true, StoreDir: jsstore.Dir(t), NoLog: true, NoSigs: true, Port: natsserver.RANDOM_PORT}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second))
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(func() { nc.Close(); s.Shutdown() })
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-cypher-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-cypher-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-cypher-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

// cNanoID returns a deterministic 20-char Contract #1 NanoID from a logical name.
func cNanoID(name string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 1469598103934665603
	for _, b := range []byte(name) {
		seed ^= uint64(b)
		seed *= 1099511628211
	}
	var out [20]byte
	for i := 0; i < 20; i++ {
		out[i] = alphabet[seed%uint64(len(alphabet))]
		seed = seed*1099511628211 + 0x9E3779B97F4A7C15
	}
	return string(out[:])
}

type lensFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string // logicalName -> bare NanoID
}

func newLensFixture(t *testing.T) *lensFixture {
	adjKV, coreKV := cypherKVs(t)
	return &lensFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}}
}

func (f *lensFixture) identity(t *testing.T, name string) string {
	t.Helper()
	id := cNanoID(name)
	f.ids[name] = id
	key := "vtx.identity." + id
	body := map[string]any{"key": key, "class": "identity", "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *lensFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx.identity." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func (f *lensFixture) project(t *testing.T) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(applicantRosterReadSpec)
	require.NoError(t, err, "applicantRosterRead cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// envelopeData is an at-rest sensitive-aspect data map as step 6.5's
// encrypt-on-write commits it: base64 ct/nonce + the wrapping key id, no
// plaintext field.
func envelopeData() map[string]any {
	return map[string]any{"ct": "3q2+7w==", "nonce": "AAAAAAAAAAAAAAAA", "keyId": "k1"}
}

// TestApplicantRosterRead_ProjectsEnvelopeWholeForNamedIdentity proves a named
// identity (ciphertext-enveloped .name + .state) projects one row: the name
// column is the envelope MAP (for the secure decryptor), identity_key doubles
// as the key-custody column, authz_anchors is empty.
func TestApplicantRosterRead_ProjectsEnvelopeWholeForNamedIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	aliceKey := f.identity(t, "alice")
	f.aspect(t, "alice", "name", "name", envelopeData())
	f.aspect(t, "alice", "state", "state", map[string]any{"value": "claimed"})

	rows := f.project(t)
	require.Len(t, rows, 1, "exactly one roster row for the one named identity")
	v := rows[0].Values
	require.Equal(t, f.ids["alice"], v["identity_id"], "identity_id is the bare NanoID (nanoIdFromKey)")
	require.Equal(t, aliceKey, v["entity_key"])
	require.Equal(t, aliceKey, v["identity_key"], "identity_key is the secure decryptor's key-custody column")
	require.Equal(t, "claimed", v["state"])
	name, ok := v["name"].(map[string]any)
	require.True(t, ok, "name must be the ciphertext envelope map, got %T (%v)", v["name"], v["name"])
	require.Equal(t, "3q2+7w==", name["ct"], "the envelope reaches the decryptor whole")
	anchors, ok := v["authz_anchors"].([]any)
	require.True(t, ok, "authz_anchors must be a list, got %T", v["authz_anchors"])
	require.Empty(t, anchors, "the roster has no per-row owner; only the WildcardAnchor grant reads it")
}

// TestApplicantRosterRead_ExcludesUnnamedAndPlaintextShapedIdentities proves
// the ciphertext-presence WHERE: an identity with no .name aspect (a service
// actor) and an identity whose .name data is plaintext-shaped ({value},
// no ct — a shape step 6.5 can never commit) both project NO row, so the lens
// can neither roster unnamed actors nor carry plaintext PII by itself.
func TestApplicantRosterRead_ExcludesUnnamedAndPlaintextShapedIdentities(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.identity(t, "svc") // no .name at all
	f.identity(t, "legacy")
	f.aspect(t, "legacy", "name", "name", map[string]any{"value": "Plain Text"})
	namedKey := f.identity(t, "bob")
	f.aspect(t, "bob", "name", "name", envelopeData())
	f.aspect(t, "bob", "state", "state", map[string]any{"value": "unclaimed"})

	rows := f.project(t)
	require.Len(t, rows, 1, "only the ciphertext-named identity projects")
	require.Equal(t, namedKey, rows[0].Values["identity_key"])
}
