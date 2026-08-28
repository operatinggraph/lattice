package processor

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

// deletedSensitiveFixture seeds a sensitive "ssn" aspect class DDL plus a real
// Vault-encrypted body, and returns that body as the generic map a tombstoned
// document carries: step 8 preserves the prior document, so a tombstone still
// holds the at-rest ciphertext verbatim.
//
// It also returns the base64 `ct` string on its own. That string — not a
// marshalling of the whole envelope — is the needle the scrub assertions use:
// a struct marshals its fields in declaration order (ct, nonce, keyId) while
// the same envelope as a generic map marshals sorted (ct, keyId, nonce), so a
// whole-envelope needle can never match the delivered document and would make
// every scrub assertion pass for free.
func deletedSensitiveFixture(t *testing.T, ctx context.Context, conn *substrate.Conn) (*DDLCache, vault.Vault, string, map[string]interface{}, string) {
	t.Helper()
	seedSensitiveAspectClassDDL(t, ctx, conn, "ssn", true)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	v, err := vault.NewLocalBackend([]byte("lattice-testutil-vault-master-ke"), "test-v1")
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	identityKey := "vtx.identity." + testNanoID2
	envelope, err := v.CreateIdentityKey(ctx, identityKey)
	if err != nil {
		t.Fatalf("CreateIdentityKey: %v", err)
	}
	seedPiiKeyAspect(t, ctx, conn, identityKey, envelope)
	ct, err := v.Encrypt(ctx, identityKey, envelope, []byte(`{"value":"123-45-6789"}`))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	raw, err := json.Marshal(ct)
	if err != nil {
		t.Fatalf("marshal ciphertext: %v", err)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal ciphertext body: %v", err)
	}
	ctB64, _ := body["ct"].(string)
	if ctB64 == "" {
		t.Fatalf("fixture body %+v carries no ct string; the scrub needle would never fire", body)
	}
	return cache, v, identityKey + ".ssn", body, ctB64
}

// assertNeedleFires renders doc and fails unless the needle is present. Every
// scrub assertion runs this against the pre-call document first: an assertion
// that the secret is gone means nothing until the secret is shown to be there.
func assertNeedleFires(t *testing.T, doc *VertexDoc, needle string) {
	t.Helper()
	rendered, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal doc: %v", err)
	}
	if !bytes.Contains(rendered, []byte(needle)) {
		t.Fatalf("needle %q is absent from the un-scrubbed doc %s; the scrub assertion would pass vacuously", needle, rendered)
	}
}

func assertScrubbed(t *testing.T, doc *VertexDoc, needle string) {
	t.Helper()
	if doc.Data == nil {
		t.Fatalf("Data is nil; parseVertexDoc normalizes every hydrated document to a non-nil map")
	}
	if len(doc.Data) != 0 {
		t.Fatalf("Data = %+v, want empty — the retained body must be scrubbed", doc.Data)
	}
	rendered, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal doc: %v", err)
	}
	if bytes.Contains(rendered, []byte(needle)) {
		t.Fatalf("delivered doc %s still carries %q", rendered, needle)
	}
}

// TestDecryptSensitiveDoc_DeletedNonEgress_DeliversScrubbed: a tombstoned
// sensitive aspect read under the plaintext disposition is DELIVERED — with
// IsDeleted intact so the script's own filter fires — and its retained
// ciphertext is scrubbed rather than handed onward.
func TestDecryptSensitiveDoc_DeletedNonEgress_DeliversScrubbed(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache, v, aspectKey, body, ctB64 := deletedSensitiveFixture(t, ctx, conn)

	doc := &VertexDoc{
		Key:       aspectKey,
		Class:     "ssn",
		VertexKey: "vtx.identity." + testNanoID2,
		LocalName: "ssn",
		IsDeleted: true,
		Data:      body,
	}
	assertNeedleFires(t, doc, ctB64)

	tracker := &sensitiveReadTracker{}
	if err := decryptSensitiveDoc(ctx, conn, testCoreBucket, cache, v, doc, false, tracker, "req1", nil); err != nil {
		t.Fatalf("decryptSensitiveDoc on a deleted sensitive aspect must deliver, not error: %v", err)
	}
	if !doc.IsDeleted {
		t.Fatalf("IsDeleted = false, want true — the script's own tombstone filter depends on it")
	}
	assertScrubbed(t, doc, ctB64)
	if _, ok := tracker.plaintextKeys[aspectKey]; ok {
		t.Fatalf("plaintextKeys = %+v, want %q NOT recorded — a scrubbed doc carries no plaintext", tracker.plaintextKeys, aspectKey)
	}
}

// TestDecryptSensitiveDoc_DeletedEgress_Refuses: the egress disposition keeps
// refusing outright. A $sensitiveRef marker is a capability the bridge opens
// at the external boundary, and one over a dead aspect must never leave the
// Processor — the deliver-scrubbed relaxation is the non-egress arm alone.
func TestDecryptSensitiveDoc_DeletedEgress_Refuses(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache, v, aspectKey, body, _ := deletedSensitiveFixture(t, ctx, conn)

	doc := &VertexDoc{
		Key:       aspectKey,
		Class:     "ssn",
		VertexKey: "vtx.identity." + testNanoID2,
		LocalName: "ssn",
		IsDeleted: true,
		Data:      body,
	}
	tracker := &sensitiveReadTracker{}
	err := decryptSensitiveDoc(ctx, conn, testCoreBucket, cache, v, doc, true, tracker, "req1", nil)
	if err == nil {
		t.Fatalf("egress disposition returned nil for a deleted sensitive aspect; doc.Data = %+v", doc.Data)
	}
	if !strings.Contains(err.Error(), "read deleted sensitive aspect") {
		t.Fatalf("err = %v, want the deleted-sensitive refusal", err)
	}
	if _, ok := doc.Data["$sensitiveRef"]; ok {
		t.Fatalf("egress refusal still authored a ref marker: %+v", doc.Data)
	}
}

// countingVault wraps a real Vault and counts Decrypt calls, optionally failing
// them with decryptErr. It is what makes the tombstoned arm's decrypt visible:
// the arm discards both the plaintext and the error, so the call count is the
// only evidence the work happened.
type countingVault struct {
	vault.Vault
	decrypts   int
	decryptErr error
}

func (c *countingVault) Decrypt(ctx context.Context, keyHolderKey string, envelope vault.Envelope, ct vault.Ciphertext) ([]byte, error) {
	c.decrypts++
	if c.decryptErr != nil {
		return nil, c.decryptErr
	}
	return c.Vault.Decrypt(ctx, keyHolderKey, envelope, ct)
}

// TestDecryptSensitiveDoc_DeletedNonEgress_StillDecryptsForTimingEqualization:
// the tombstoned non-egress arm performs the same decrypt the live path
// performs, and withholds every part of the result.
//
// The decrypt is the equalization (nfr-s6-release-quantum-payload-design.md
// §4.2): skipping it makes a tombstoned sensitive read measurably cheaper than
// a live one, and for an identity's `.claimKey` that difference separates an
// already-claimed identity from an unclaimed one. The rest of the assertions
// are the other half of the obligation — equalizing the TIME must not widen the
// REACH.
func TestDecryptSensitiveDoc_DeletedNonEgress_StillDecryptsForTimingEqualization(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache, v, aspectKey, body, ctB64 := deletedSensitiveFixture(t, ctx, conn)

	counting := &countingVault{Vault: v}
	doc := &VertexDoc{
		Key:       aspectKey,
		Class:     "ssn",
		VertexKey: "vtx.identity." + testNanoID2,
		LocalName: "ssn",
		IsDeleted: true,
		Data:      body,
	}
	assertNeedleFires(t, doc, ctB64)

	tracker := &sensitiveReadTracker{}
	if err := decryptSensitiveDoc(ctx, conn, testCoreBucket, cache, counting, doc, false, tracker, "req1", nil); err != nil {
		t.Fatalf("decryptSensitiveDoc on a deleted sensitive aspect must deliver, not error: %v", err)
	}
	if counting.decrypts != 1 {
		t.Fatalf("Decrypt called %d times, want exactly 1 — the tombstoned arm must do the same work the live path does, "+
			"or its cheaper path is itself the enumeration signal (design §4.2)", counting.decrypts)
	}
	assertScrubbed(t, doc, ctB64)
	if _, ok := doc.Data["$sensitiveRef"]; ok {
		t.Fatalf("the tombstoned non-egress arm authored an egress ref: %+v", doc.Data)
	}
	if _, ok := tracker.plaintextKeys[aspectKey]; ok {
		t.Fatalf("plaintextKeys = %+v, want %q NOT recorded — the decrypted plaintext is discarded, never delivered",
			tracker.plaintextKeys, aspectKey)
	}
}

// TestDecryptSensitiveDoc_DeletedNonEgress_ShreddedKeyStillScrubs: the discard
// swallows the ERROR as well as the plaintext.
//
// A shredded key envelope makes Decrypt fail mid-way. Propagating that would
// turn a tombstoned read that succeeds today into an InternalError whose
// reachability depends on the target's state — a new state-dependent class
// invented by the very change that removes one (design §6.2).
func TestDecryptSensitiveDoc_DeletedNonEgress_ShreddedKeyStillScrubs(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache, v, aspectKey, body, ctB64 := deletedSensitiveFixture(t, ctx, conn)

	counting := &countingVault{Vault: v, decryptErr: vault.ErrKeyShredded}
	doc := &VertexDoc{
		Key:       aspectKey,
		Class:     "ssn",
		VertexKey: "vtx.identity." + testNanoID2,
		LocalName: "ssn",
		IsDeleted: true,
		Data:      body,
	}
	assertNeedleFires(t, doc, ctB64)

	tracker := &sensitiveReadTracker{}
	if err := decryptSensitiveDoc(ctx, conn, testCoreBucket, cache, counting, doc, false, tracker, "req1", nil); err != nil {
		t.Fatalf("a shredded key on the tombstoned path must be swallowed, got %v", err)
	}
	if counting.decrypts != 1 {
		t.Fatalf("Decrypt called %d times, want exactly 1", counting.decrypts)
	}
	assertScrubbed(t, doc, ctB64)
	if _, ok := tracker.plaintextKeys[aspectKey]; ok {
		t.Fatalf("plaintextKeys = %+v, want %q NOT recorded", tracker.plaintextKeys, aspectKey)
	}
}

// TestDecryptSensitiveDoc_DeletedNonEgress_UnparseableCiphertextStillScrubs
// pins the arm on its DEGRADATION path, not only on its happy one.
//
// A body that is not a ciphertext envelope is what a stack that ran without a
// Vault leaves at rest, so the discard short-circuits and Decrypt is never
// reached. It short-circuits at vault.KeyHolder rather than at
// ciphertextFromData: that parse is a JSON round trip into a typed struct, so a
// map carrying none of the envelope's fields decodes to a ZERO Ciphertext
// without error, and it is the missing keyId that stops the sequence.
//
// That is not a hidden failure: the live path returns "resolve key holder for
// %s" at the very same call, having done the very same work, so the two arms
// still cost the same — which is the whole property. What the tombstoned arm may
// not do is surface it, because an error here would be one more answer that
// depends on the target's state.
func TestDecryptSensitiveDoc_DeletedNonEgress_UnparseableCiphertextStillScrubs(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache, v, aspectKey, _, _ := deletedSensitiveFixture(t, ctx, conn)

	counting := &countingVault{Vault: v}
	const needle = "123-45-6789"
	doc := &VertexDoc{
		Key:       aspectKey,
		Class:     "ssn",
		VertexKey: "vtx.identity." + testNanoID2,
		LocalName: "ssn",
		IsDeleted: true,
		Data:      map[string]interface{}{"value": needle},
	}
	assertNeedleFires(t, doc, needle)

	tracker := &sensitiveReadTracker{}
	if err := decryptSensitiveDoc(ctx, conn, testCoreBucket, cache, counting, doc, false, tracker, "req1", nil); err != nil {
		t.Fatalf("a body that is not a ciphertext envelope must be swallowed on the tombstoned path, got %v", err)
	}
	if counting.decrypts != 0 {
		t.Fatalf("Decrypt called %d times, want 0 — a body with no envelope to parse cannot reach it", counting.decrypts)
	}
	assertScrubbed(t, doc, needle)
	if _, ok := tracker.plaintextKeys[aspectKey]; ok {
		t.Fatalf("plaintextKeys = %+v, want %q NOT recorded", tracker.plaintextKeys, aspectKey)
	}
}

// seedTombstonedRealCiphertextAspect writes the tombstone a spent sensitive
// aspect leaves in Core KV: isDeleted true, prior body retained.
func seedTombstonedRealCiphertextAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class, vertexKey string, body map[string]interface{}) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	doc := []byte(`{"class":"` + class + `","isDeleted":true,"vertexKey":"` + vertexKey + `","localName":"` + class + `","data":` + string(raw) + `}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, key, doc); err != nil {
		t.Fatalf("seed tombstoned aspect %s: %v", key, err)
	}
}

// TestHydrate_DeletedSensitiveRead_ScrubbedIntoState: the same disposition
// split at the real step 4 seam. A tombstoned sensitive key under `reads`
// hydrates (scrubbed) so the script runs and renders its own rejection; the
// same key under `egressReads` fails hydration with the refusal named.
func TestHydrate_DeletedSensitiveRead_ScrubbedIntoState(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache, v, aspectKey, body, ctB64 := deletedSensitiveFixture(t, ctx, conn)
	seedTombstonedRealCiphertextAspect(t, ctx, conn, aspectKey, "ssn", "vtx.identity."+testNanoID2, body)

	h := NewHydratorWithCache(conn, testCoreBucket, cache, testLogger())
	h.Vault = v

	readEnv := newTestEnvelope(testNanoID1)
	readEnv.ContextHint = &ContextHint{Reads: []string{aspectKey}}
	state, err := h.Hydrate(ctx, readEnv)
	if err != nil {
		t.Fatalf("Hydrate (reads) on a tombstoned sensitive key must succeed: %v", err)
	}
	got, ok := state.Context.Hydrated[aspectKey]
	if !ok {
		t.Fatalf("hydrated set = %+v, want %q present so the script can filter it", state.Context.Hydrated, aspectKey)
	}
	if !got.IsDeleted {
		t.Fatalf("hydrated %s has IsDeleted false", aspectKey)
	}
	assertScrubbed(t, &got, ctB64)
	if _, ok := state.Context.RequiredAbsent[aspectKey]; ok {
		t.Fatalf("%s recorded required-absent; it is present-but-deleted, and the script must see it", aspectKey)
	}
	if _, ok := state.Context.SensitiveReads.plaintextKeys[aspectKey]; ok {
		t.Fatalf("a scrubbed doc must not be recorded as carrying plaintext")
	}

	// The SAME seeded key under the egress disposition, so the two arms differ
	// in nothing but the declaration.
	egressEnv := newTestEnvelope(testNanoID2)
	egressEnv.ContextHint = &ContextHint{EgressReads: []string{aspectKey}}
	_, err = h.Hydrate(ctx, egressEnv)
	if err == nil {
		t.Fatalf("Hydrate (egressReads) on a tombstoned sensitive key must fail")
	}
	if !strings.Contains(err.Error(), "read deleted sensitive aspect") {
		t.Fatalf("egress hydrate err = %v, want the deleted-sensitive refusal (any other failure would satisfy a bare non-nil check)", err)
	}
}

// TestLazyKVRead_DeletedSensitiveKey_ScrubbedAndEgressRefuses covers the OTHER
// caller of the disposition — the lazy `kv.Read` seam a script reaches through
// for an undeclared key (connKVReader.ReadVertex). It resolves sensitivity with
// the same call step 4 makes, so both arms must behave identically here; an
// undeclared read is precisely the path a submitter cannot be relied on to
// have declared, so it may not be the lenient one.
func TestLazyKVRead_DeletedSensitiveKey_ScrubbedAndEgressRefuses(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache, v, aspectKey, body, ctB64 := deletedSensitiveFixture(t, ctx, conn)
	seedTombstonedRealCiphertextAspect(t, ctx, conn, aspectKey, "ssn", "vtx.identity."+testNanoID2, body)

	tracker := &sensitiveReadTracker{}
	plain := connKVReader{conn: conn, bucket: testCoreBucket, ddls: cache, vault: v, tracker: tracker, requestID: testNanoID1}
	doc, err := plain.ReadVertex(ctx, aspectKey)
	if err != nil {
		t.Fatalf("lazy ReadVertex on a tombstoned sensitive key must deliver, not error: %v", err)
	}
	if doc == nil {
		t.Fatalf("lazy ReadVertex returned nil for a present-but-deleted key; the script must see the tombstone")
	}
	if !doc.IsDeleted {
		t.Fatalf("lazy doc = %+v, want IsDeleted true", doc)
	}
	assertScrubbed(t, doc, ctB64)
	if _, ok := tracker.plaintextKeys[aspectKey]; ok {
		t.Fatalf("lazy scrubbed read must record no plaintext key, got %v", tracker.plaintextKeys)
	}

	egress := connKVReader{
		conn: conn, bucket: testCoreBucket, ddls: cache, vault: v,
		egressKeys: map[string]struct{}{aspectKey: {}},
		tracker:    &sensitiveReadTracker{},
		requestID:  testNanoID1,
	}
	if _, err := egress.ReadVertex(ctx, aspectKey); err == nil {
		t.Fatalf("lazy egress read of a tombstoned sensitive key must refuse")
	} else if !strings.Contains(err.Error(), "read deleted sensitive aspect") {
		t.Fatalf("lazy egress err = %v, want the deleted-sensitive refusal", err)
	}
}
