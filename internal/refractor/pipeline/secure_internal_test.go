package pipeline

// Unit + engine-round-trip coverage of the Secure-Lens decrypt-at-projection
// transform (Contract #3 §3.10). The unit tests drive SecureDecryptor.Apply
// against a REAL vault.LocalBackend (fake crypto would prove nothing about
// the envelope/AEAD interplay) and a fake Core KV; the round-trip test runs
// the full openCypher engine against embedded NATS so the exact evaluation
// path a live Secure Lens takes — `node.<aspect>.data` ciphertext-envelope
// projection → evaluateForEntry → decrypt — is proven end to end.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

// fakeCoreKV satisfies coreKVGetter from a static key→value map.
type fakeCoreKV map[string][]byte

func (f fakeCoreKV) Get(_ context.Context, key string) (*substrate.KVEntry, error) {
	v, ok := f[key]
	if !ok {
		return nil, fmt.Errorf("get %q: %w", key, substrate.ErrKeyNotFound)
	}
	return &substrate.KVEntry{Key: key, Value: v, Revision: 1}, nil
}

func newTestVault(t *testing.T) *vault.LocalBackend {
	t.Helper()
	kek := make([]byte, 32)
	_, err := rand.Read(kek)
	require.NoError(t, err)
	v, err := vault.NewLocalBackend(kek, "test-kek-v1")
	require.NoError(t, err)
	return v
}

// mintIdentityPII creates an identity key + envelope in v, encrypts data
// under it, and returns the ciphertext as the generically-decoded map shape
// the cypher engine produces for `node.<aspect>.data`, plus the piiKey aspect
// document bytes as the Processor commits them.
func mintIdentityPII(t *testing.T, v *vault.LocalBackend, identityKey string, data map[string]any) (ctMap map[string]any, piiKeyDoc []byte) {
	t.Helper()
	ctx := context.Background()
	env, err := v.CreateIdentityKey(ctx, identityKey)
	require.NoError(t, err)
	plaintext, err := json.Marshal(data)
	require.NoError(t, err)
	ct, err := v.Encrypt(ctx, identityKey, env, plaintext)
	require.NoError(t, err)
	raw, err := json.Marshal(ct)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &ctMap))
	piiKeyDoc, err = json.Marshal(map[string]any{
		"class": "piiKey", "vertexKey": identityKey, "localName": "piiKey",
		"isDeleted": false, "data": env,
	})
	require.NoError(t, err)
	return ctMap, piiKeyDoc
}

func TestSecureDecryptor_DecryptsFieldAndWholeObject(t *testing.T) {
	v := newTestVault(t)
	const idKey = "vtx.identity.SecUnitAliceAAAAAAAAA"
	ctMap, piiKeyDoc := mintIdentityPII(t, v, idKey, map[string]any{"value": "Alice", "source": "signup"})

	var calls atomic.Uint64
	dec, err := NewSecureDecryptor(v, fakeCoreKV{idKey + ".piiKey": piiKeyDoc}, []SecureColumn{
		{Column: "name", IdentityKeyColumn: "identity_key", Field: "value"},
		{Column: "name_full", IdentityKeyColumn: "identity_key"},
	}, &calls)
	require.NoError(t, err)

	// Two independent copies of the ciphertext map — decryptColumn replaces
	// the column value, and both columns decrypt the same aspect here.
	ctMap2 := map[string]any{}
	for k, val := range ctMap {
		ctMap2[k] = val
	}
	results := []ruleengine.EvalResult{{
		Keys: map[string]any{"key": idKey},
		Row:  map[string]any{"identity_key": idKey, "name": ctMap, "name_full": ctMap2, "state": "active"},
	}}
	require.NoError(t, dec.Apply(context.Background(), results))

	assert.Equal(t, "Alice", results[0].Row["name"], "field-selected secure column projects the plaintext field")
	full, ok := results[0].Row["name_full"].(map[string]any)
	require.True(t, ok, "field-less secure column projects the whole decrypted object")
	assert.Equal(t, "Alice", full["value"])
	assert.Equal(t, "signup", full["source"])
	assert.Equal(t, "active", results[0].Row["state"], "non-secure columns untouched")
	assert.Equal(t, uint64(2), calls.Load(), "one Vault.Decrypt per secure column")
}

func TestSecureDecryptor_AbsentAspectStaysNull(t *testing.T) {
	v := newTestVault(t)
	var calls atomic.Uint64
	dec, err := NewSecureDecryptor(v, fakeCoreKV{}, []SecureColumn{
		{Column: "phone", IdentityKeyColumn: "identity_key", Field: "value"},
	}, &calls)
	require.NoError(t, err)

	results := []ruleengine.EvalResult{{Row: map[string]any{"identity_key": "vtx.identity.X", "phone": nil}}}
	require.NoError(t, dec.Apply(context.Background(), results))
	assert.Nil(t, results[0].Row["phone"])
	assert.Zero(t, calls.Load(), "an absent aspect makes no Vault call")
}

func TestSecureDecryptor_ShreddedProjectsNull(t *testing.T) {
	v := newTestVault(t)
	const idKey = "vtx.identity.SecUnitShredAAAAAAAA"
	ctMap, piiKeyDoc := mintIdentityPII(t, v, idKey, map[string]any{"value": "Gone Person"})
	require.NoError(t, v.ShredKey(context.Background(), idKey))

	dec, err := NewSecureDecryptor(v, fakeCoreKV{idKey + ".piiKey": piiKeyDoc}, []SecureColumn{
		{Column: "name", IdentityKeyColumn: "identity_key", Field: "value"},
	}, nil)
	require.NoError(t, err)

	results := []ruleengine.EvalResult{{Row: map[string]any{"identity_key": idKey, "name": ctMap, "state": "active"}}}
	require.NoError(t, dec.Apply(context.Background(), results), "a shredded key is not an error — the row survives with null PII")
	assert.Nil(t, results[0].Row["name"], "shredded identity's PII projects null")
	assert.Equal(t, "active", results[0].Row["state"])
}

func TestSecureDecryptor_ShreddedPlaceholderEnvelopeProjectsNull(t *testing.T) {
	// A fresh backend sharing only the KEK (simulated restart) must still deny
	// via the durable placeholder envelope's Shredded flag — the Fire-3
	// restart-survival property, exercised here at the projection surface.
	v := newTestVault(t)
	const idKey = "vtx.identity.SecUnitPlaceholderAA"
	ctMap, _ := mintIdentityPII(t, v, idKey, map[string]any{"value": "X"})
	placeholderDoc, err := json.Marshal(map[string]any{
		"class": "piiKey", "vertexKey": idKey, "localName": "piiKey", "isDeleted": false,
		"data": vault.Envelope{Shredded: true},
	})
	require.NoError(t, err)

	dec, err := NewSecureDecryptor(v, fakeCoreKV{idKey + ".piiKey": placeholderDoc}, []SecureColumn{
		{Column: "name", IdentityKeyColumn: "identity_key", Field: "value"},
	}, nil)
	require.NoError(t, err)

	results := []ruleengine.EvalResult{{Row: map[string]any{"identity_key": idKey, "name": ctMap}}}
	require.NoError(t, dec.Apply(context.Background(), results))
	assert.Nil(t, results[0].Row["name"])
}

func TestSecureDecryptor_TamperedCiphertextIsTerminal(t *testing.T) {
	v := newTestVault(t)
	const idKey = "vtx.identity.SecUnitTamperAAAAAAA"
	ctMap, piiKeyDoc := mintIdentityPII(t, v, idKey, map[string]any{"value": "Alice"})
	// Flip the ciphertext bytes (base64 of different content).
	ctMap["ct"] = "dGFtcGVyZWQtY2lwaGVydGV4dA=="

	dec, err := NewSecureDecryptor(v, fakeCoreKV{idKey + ".piiKey": piiKeyDoc}, []SecureColumn{
		{Column: "name", IdentityKeyColumn: "identity_key", Field: "value"},
	}, nil)
	require.NoError(t, err)

	err = dec.Apply(context.Background(), []ruleengine.EvalResult{{Row: map[string]any{"identity_key": idKey, "name": ctMap}}})
	require.Error(t, err)
	assert.Equal(t, failure.CatTerminal, failure.Classify(err), "authenticated-decrypt failure must fail the projection closed")
}

func TestSecureDecryptor_NonEnvelopeValueIsTerminal(t *testing.T) {
	v := newTestVault(t)
	dec, err := NewSecureDecryptor(v, fakeCoreKV{}, []SecureColumn{
		{Column: "name", IdentityKeyColumn: "identity_key", Field: "value"},
	}, nil)
	require.NoError(t, err)

	// A plaintext string where a ciphertext envelope was declared — an
	// authoring defect (e.g. the column bound to a non-sensitive aspect field).
	err = dec.Apply(context.Background(), []ruleengine.EvalResult{{Row: map[string]any{"identity_key": "vtx.identity.X", "name": "plaintext"}}})
	require.Error(t, err)
	assert.Equal(t, failure.CatTerminal, failure.Classify(err))

	// A map that is not a ciphertext envelope (no ct) — same posture.
	err = dec.Apply(context.Background(), []ruleengine.EvalResult{{Row: map[string]any{"identity_key": "vtx.identity.X", "name": map[string]any{"value": "plain"}}}})
	require.Error(t, err)
	assert.Equal(t, failure.CatTerminal, failure.Classify(err))
}

func TestSecureDecryptor_MissingIdentityKeyIsTerminal(t *testing.T) {
	v := newTestVault(t)
	const idKey = "vtx.identity.SecUnitNoIdColAAAAAA"
	ctMap, piiKeyDoc := mintIdentityPII(t, v, idKey, map[string]any{"value": "Alice"})

	dec, err := NewSecureDecryptor(v, fakeCoreKV{idKey + ".piiKey": piiKeyDoc}, []SecureColumn{
		{Column: "name", IdentityKeyColumn: "identity_key", Field: "value"},
	}, nil)
	require.NoError(t, err)

	err = dec.Apply(context.Background(), []ruleengine.EvalResult{{Row: map[string]any{"name": ctMap}}})
	require.Error(t, err)
	assert.Equal(t, failure.CatTerminal, failure.Classify(err))
}

func TestSecureDecryptor_SoftDeletedPiiKeyIsTerminal(t *testing.T) {
	// A soft-deleted piiKey aspect must never open ciphertext — the engine
	// treats soft-deleted aspects as absent, and the decryptor must agree.
	v := newTestVault(t)
	const idKey = "vtx.identity.SecUnitDelPiiKeyAAA"
	ctMap, _ := mintIdentityPII(t, v, idKey, map[string]any{"value": "Alice"})
	env, err := v.CreateIdentityKey(context.Background(), "vtx.identity.SecUnitOtherAAAAAAAA")
	require.NoError(t, err)
	deletedDoc, err := json.Marshal(map[string]any{
		"class": "piiKey", "vertexKey": idKey, "localName": "piiKey",
		"isDeleted": true, "data": env,
	})
	require.NoError(t, err)

	dec, err := NewSecureDecryptor(v, fakeCoreKV{idKey + ".piiKey": deletedDoc}, []SecureColumn{
		{Column: "name", IdentityKeyColumn: "identity_key", Field: "value"},
	}, nil)
	require.NoError(t, err)

	err = dec.Apply(context.Background(), []ruleengine.EvalResult{{Row: map[string]any{"identity_key": idKey, "name": ctMap}}})
	require.Error(t, err)
	assert.Equal(t, failure.CatTerminal, failure.Classify(err))
}

func TestSecureDecryptor_MalformedPiiKeyDocIsTerminal(t *testing.T) {
	// A piiKey that exists but cannot be parsed is permanently unusable — it
	// must Terminal-DLQ, not Nak-loop as an unclassified (transient) error.
	v := newTestVault(t)
	const idKey = "vtx.identity.SecUnitBadPiiKeyAAA"
	ctMap, _ := mintIdentityPII(t, v, idKey, map[string]any{"value": "Alice"})

	dec, err := NewSecureDecryptor(v, fakeCoreKV{idKey + ".piiKey": []byte("{not json")}, []SecureColumn{
		{Column: "name", IdentityKeyColumn: "identity_key", Field: "value"},
	}, nil)
	require.NoError(t, err)

	err = dec.Apply(context.Background(), []ruleengine.EvalResult{{Row: map[string]any{"identity_key": idKey, "name": ctMap}}})
	require.Error(t, err)
	assert.Equal(t, failure.CatTerminal, failure.Classify(err))
}

func TestSecureDecryptor_MissingDeclaredFieldIsTerminal(t *testing.T) {
	// The plaintext decrypts but lacks the declared field — a spec/DDL
	// mismatch must fail loud, not project a null indistinguishable from a
	// shred.
	v := newTestVault(t)
	const idKey = "vtx.identity.SecUnitNoFieldAAAAA"
	ctMap, piiKeyDoc := mintIdentityPII(t, v, idKey, map[string]any{"value": "Alice"})

	dec, err := NewSecureDecryptor(v, fakeCoreKV{idKey + ".piiKey": piiKeyDoc}, []SecureColumn{
		{Column: "name", IdentityKeyColumn: "identity_key", Field: "displayName"},
	}, nil)
	require.NoError(t, err)

	err = dec.Apply(context.Background(), []ruleengine.EvalResult{{Row: map[string]any{"identity_key": idKey, "name": ctMap}}})
	require.Error(t, err)
	assert.Equal(t, failure.CatTerminal, failure.Classify(err))
}

func TestSecureDecryptor_CiphertextWithoutPiiKeyIsTerminal(t *testing.T) {
	v := newTestVault(t)
	const idKey = "vtx.identity.SecUnitNoPiiKeyAAAAA"
	ctMap, _ := mintIdentityPII(t, v, idKey, map[string]any{"value": "Alice"})

	dec, err := NewSecureDecryptor(v, fakeCoreKV{}, []SecureColumn{
		{Column: "name", IdentityKeyColumn: "identity_key", Field: "value"},
	}, nil)
	require.NoError(t, err)

	err = dec.Apply(context.Background(), []ruleengine.EvalResult{{Row: map[string]any{"identity_key": idKey, "name": ctMap}}})
	require.Error(t, err)
	assert.Equal(t, failure.CatTerminal, failure.Classify(err))
}

func TestSecureDecryptor_DeleteResultsPassThrough(t *testing.T) {
	v := newTestVault(t)
	var calls atomic.Uint64
	dec, err := NewSecureDecryptor(v, fakeCoreKV{}, []SecureColumn{
		{Column: "name", IdentityKeyColumn: "identity_key"},
	}, &calls)
	require.NoError(t, err)

	results := []ruleengine.EvalResult{
		{Delete: true, Keys: map[string]any{"key": "read_x"}},
		{Row: nil},
	}
	require.NoError(t, dec.Apply(context.Background(), results))
	assert.Zero(t, calls.Load())
}

// TestSecureLens_ShredReprojectsViaPiiKeyEvent proves the right-to-erasure
// path end to end through the stream handler: a projected plaintext row is
// overwritten with null PII when the shred's piiKey aspect mutation arrives
// as a CDC event — no unrelated anchor event needed.
func TestSecureLens_ShredReprojectsViaPiiKeyEvent(t *testing.T) {
	ctx := context.Background()
	p, v, coreKV, targetKV, identityKey, vertexBody, piiKeyDoc := newSecureRoundTripPipeline(t, "SecShredHandAAAAAAAA")

	// 1. The identity vertex event projects the plaintext row.
	dec, err := p.handle(ctx, substrate.Message{
		Subject:  "$KV.sec-core." + identityKey,
		Body:     vertexBody,
		Sequence: 10,
	})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	entry, err := targetKV.Get(ctx, identityKey)
	require.NoError(t, err)
	var row map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &row))
	require.Equal(t, "Alice Applicant", row["name"], "pre-shred row carries plaintext")

	// 2. Shred: the backend denies, and the committed piiKey update arrives
	// as an aspect CDC event (what ShredIdentityKey produces).
	require.NoError(t, v.ShredKey(ctx, identityKey))
	var piiDoc map[string]any
	require.NoError(t, json.Unmarshal(piiKeyDoc, &piiDoc))
	data := piiDoc["data"].(map[string]any)
	data["shredded"] = true
	shreddedDoc, err := json.Marshal(piiDoc)
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, identityKey+".piiKey", shreddedDoc)
	require.NoError(t, err)

	dec, err = p.handle(ctx, substrate.Message{
		Subject:  "$KV.sec-core." + identityKey + ".piiKey",
		Body:     shreddedDoc,
		Sequence: 11,
	})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)

	// 3. The projected row's PII is now null; the row itself survives.
	entry, err = targetKV.Get(ctx, identityKey)
	require.NoError(t, err)
	row = nil
	require.NoError(t, json.Unmarshal(entry.Value, &row))
	assert.Nil(t, row["name"], "the shred's piiKey event scrubs the projected plaintext")
	assert.Equal(t, identityKey, row["identity_key"], "non-PII columns survive the scrub")
}

// newSecureRoundTripPipeline builds an embedded-NATS pipeline with a secure
// full-engine lens over one seeded identity (encrypted name + piiKey),
// returning everything the round-trip tests drive.
func newSecureRoundTripPipeline(t *testing.T, idSuffix string) (*Pipeline, *vault.LocalBackend, *substrate.KV, *substrate.KV, string, []byte, []byte) {
	t.Helper()
	ctx := context.Background()

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
	for _, b := range []string{"sec-core", "sec-adj", "sec-target"} {
		_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: b})
		require.NoError(t, err)
	}
	coreKV, err := conn.OpenKV(ctx, "sec-core")
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, "sec-adj")
	require.NoError(t, err)
	targetKV, err := conn.OpenKV(ctx, "sec-target")
	require.NoError(t, err)

	v := newTestVault(t)
	identityKey := "vtx.identity." + idSuffix
	ctMap, piiKeyDoc := mintIdentityPII(t, v, identityKey, map[string]any{"value": "Alice Applicant"})
	vertexBody, err := json.Marshal(map[string]any{
		"key": identityKey, "class": "identity", "isDeleted": false,
		"createdAt": "2026-07-02T10:00:00Z", "lastModifiedAt": "2026-07-02T10:00:00Z",
		"data": map[string]any{},
	})
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, identityKey, vertexBody)
	require.NoError(t, err)
	nameBody, err := json.Marshal(map[string]any{
		"key": identityKey + ".name", "class": "name", "vertexKey": identityKey,
		"localName": "name", "isDeleted": false, "data": ctMap,
	})
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, identityKey+".name", nameBody)
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, identityKey+".piiKey", piiKeyDoc)
	require.NoError(t, err)

	const cypher = `MATCH (i:identity)
WHERE i.name.data.ct <> null
RETURN i.key AS key, i.key AS identity_key, i.name.data AS name`

	eng := full.New()
	cr, err := eng.Parse(cypher)
	require.NoError(t, err)
	fullCR, ok := cr.(*full.CompiledRule)
	require.True(t, ok)
	fullCR.KeyColumns = []string{"key"}
	require.NoError(t, fullCR.ValidateKeyColumns())
	require.NoError(t, fullCR.ValidateReturnAliases("name", "identity_key"))

	adpt, err := adapter.New(targetKV, []string{"key"}, adapter.DeleteModeHard)
	require.NoError(t, err)
	p, err := New("secure-rt", "nats_kv", "sec-core", adjKV, coreKV, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)

	decr, err := NewSecureDecryptor(v, coreKV, []SecureColumn{
		{Column: "name", IdentityKeyColumn: "identity_key", Field: "value"},
	}, nil)
	require.NoError(t, err)
	p.SetSecureDecryptor(decr)

	return p, v, coreKV, targetKV, identityKey, vertexBody, piiKeyDoc
}

// TestSecureLens_FullEngineRoundTrip proves the live evaluation path: the
// full engine projects a sensitive aspect's ciphertext envelope via
// `i.name.data`, and evaluateForEntry's decrypt hook rewrites it to
// plaintext — then, after a shred, to null — before any write path sees the
// row. The lens's WHERE keys aspect presence off the envelope's ct field
// (data.value does not exist for an encrypted aspect).
func TestSecureLens_FullEngineRoundTrip(t *testing.T) {
	ctx := context.Background()
	p, v, coreKV, _, identityKey, vertexBody, _ := newSecureRoundTripPipeline(t, "SecE2eRoundTripAAAAA")

	var vertexProps map[string]any
	require.NoError(t, json.Unmarshal(vertexBody, &vertexProps))
	entry := ruleengine.NodeEntry{CoreKVKey: identityKey, NodeLabel: "identity", Properties: vertexProps}

	results, _, err := p.evaluateForEntry(ctx, entry)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "Alice Applicant", results[0].Row["name"], "the projected row carries decrypted plaintext")
	assert.Equal(t, identityKey, results[0].Row["identity_key"])

	// Raw Core KV still holds ciphertext — decryption happened only in the
	// projection row, never wrote back.
	rawName, err := coreKV.Get(ctx, identityKey+".name")
	require.NoError(t, err)
	assert.NotContains(t, string(rawName.Value), "Alice Applicant")

	// Shred, then re-evaluate: the same row now projects null PII.
	require.NoError(t, v.ShredKey(ctx, identityKey))
	results, _, err = p.evaluateForEntry(ctx, entry)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Nil(t, results[0].Row["name"], "post-shred reprojection self-nullifies the PII column")
}

// TestSecureLens_NeighborShredReprojectsAnchoredRows proves the
// right-to-erasure path for a secure lens whose secure columns decrypt a
// NEIGHBOR identity's PII (the lens anchors on a different vertex type that
// reaches the identity through its MATCH walk — the landlord
// lease-applications shape). The shred's piiKey aspect event arrives on the
// identity, which is NOT this lens's anchor; the scrub still lands because
// the piiKey reprojection re-executes the lens's UNANCHORED cypher (no
// {key: $actorKey} on the anchor MATCH), which re-scans every anchor and
// re-projects every row with a fresh decrypt — no per-anchor enumeration
// needed. This test pins that load-bearing behavior: if the executor ever
// starts seeding an unanchored MATCH from the triggering vertex (binding
// zero rows for a non-anchor identity), the shredded plaintext would
// survive, and this test fails.
func TestSecureLens_NeighborShredReprojectsAnchoredRows(t *testing.T) {
	ctx := context.Background()

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
	for _, b := range []string{"secn-core", "secn-adj", "secn-target"} {
		_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: b})
		require.NoError(t, err)
	}
	coreKV, err := conn.OpenKV(ctx, "secn-core")
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, "secn-adj")
	require.NoError(t, err)
	targetKV, err := conn.OpenKV(ctx, "secn-target")
	require.NoError(t, err)

	v := newTestVault(t)
	const identityID = "SecNbrPersonAAAAAAAA"
	const appID = "SecNbrLeaseappAAAAAA"
	identityKey := "vtx.identity." + identityID
	appKey := "vtx.leaseapp." + appID
	ctMap, piiKeyDoc := mintIdentityPII(t, v, identityKey, map[string]any{"value": "Alice Applicant"})

	put := func(key string, body map[string]any) []byte {
		raw, merr := json.Marshal(body)
		require.NoError(t, merr)
		_, perr := coreKV.Put(ctx, key, raw)
		require.NoError(t, perr)
		return raw
	}
	appBody := put(appKey, map[string]any{
		"key": appKey, "class": "leaseapp", "isDeleted": false,
		"createdAt": "2026-07-02T10:00:00Z", "lastModifiedAt": "2026-07-02T10:00:00Z",
		"data": map[string]any{},
	})
	put(identityKey, map[string]any{
		"key": identityKey, "class": "identity", "isDeleted": false,
		"createdAt": "2026-07-02T10:00:00Z", "lastModifiedAt": "2026-07-02T10:00:00Z",
		"data": map[string]any{},
	})
	put(identityKey+".name", map[string]any{
		"key": identityKey + ".name", "class": "name", "vertexKey": identityKey,
		"localName": "name", "isDeleted": false, "data": ctMap,
	})
	_, err = coreKV.Put(ctx, identityKey+".piiKey", piiKeyDoc)
	require.NoError(t, err)

	// The applicationFor link, both adjacency directions, so the engine's
	// traversal resolves it.
	linkKey := "lnk.leaseapp." + appID + ".applicationFor.identity." + identityID
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: linkKey, Name: "applicationFor",
		Direction: "outbound", NodeID: appID, OtherNodeID: identityID, OtherType: "identity",
	}))
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: linkKey, Name: "applicationFor",
		Direction: "inbound", NodeID: identityID, OtherNodeID: appID, OtherType: "leaseapp",
	}))

	const cypher = `MATCH (app:leaseapp)
MATCH (app)-[:applicationFor]->(id:identity)
RETURN app.key AS key, id.key AS applicant, id.name.data AS applicant_name`

	eng := full.New()
	cr, err := eng.Parse(cypher)
	require.NoError(t, err)
	fullCR, ok := cr.(*full.CompiledRule)
	require.True(t, ok)
	fullCR.KeyColumns = []string{"key"}
	require.NoError(t, fullCR.ValidateKeyColumns())
	require.NoError(t, fullCR.ValidateReturnAliases("applicant_name", "applicant"))

	adpt, err := adapter.New(targetKV, []string{"key"}, adapter.DeleteModeHard)
	require.NoError(t, err)
	p, err := New("secure-neighbor", "nats_kv", "secn-core", adjKV, coreKV, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)
	decr, err := NewSecureDecryptor(v, coreKV, []SecureColumn{
		{Column: "applicant_name", IdentityKeyColumn: "applicant", Field: "value"},
	}, nil)
	require.NoError(t, err)
	p.SetSecureDecryptor(decr)

	// 1. The leaseapp anchor event projects the row with the neighbor
	// identity's decrypted name.
	dec, err := p.handle(ctx, substrate.Message{
		Subject: "$KV.secn-core." + appKey, Body: appBody, Sequence: 10,
	})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	entry, err := targetKV.Get(ctx, appKey)
	require.NoError(t, err)
	var row map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &row))
	require.Equal(t, "Alice Applicant", row["applicant_name"], "pre-shred row carries the neighbor identity's plaintext")

	// 2. Shred the identity; its piiKey aspect update arrives as a CDC event
	// on the IDENTITY — not this lens's anchor.
	require.NoError(t, v.ShredKey(ctx, identityKey))
	var piiDoc map[string]any
	require.NoError(t, json.Unmarshal(piiKeyDoc, &piiDoc))
	piiDoc["data"].(map[string]any)["shredded"] = true
	shreddedDoc, err := json.Marshal(piiDoc)
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, identityKey+".piiKey", shreddedDoc)
	require.NoError(t, err)

	dec, err = p.handle(ctx, substrate.Message{
		Subject: "$KV.secn-core." + identityKey + ".piiKey", Body: shreddedDoc, Sequence: 11,
	})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)

	// 3. The leaseapp-keyed row's PII is scrubbed; the row itself survives.
	entry, err = targetKV.Get(ctx, appKey)
	require.NoError(t, err)
	row = nil
	require.NoError(t, json.Unmarshal(entry.Value, &row))
	assert.Nil(t, row["applicant_name"], "the neighbor identity's shred scrubs the anchored row's plaintext")
	assert.Equal(t, identityKey, row["applicant"], "non-PII columns survive the scrub")
}
