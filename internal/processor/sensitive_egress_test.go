package processor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

// seedSensitiveAspectClassDDL seeds an aspectType DDL meta-vertex for class,
// with `sensitive` set as given. Mirrors buildWriteScopeValidator's
// sensitiveNote fixture (write_scope_test.go).
func seedSensitiveAspectClassDDL(t *testing.T, ctx context.Context, conn *substrate.Conn, class string, sensitive bool) {
	t.Helper()
	root := "vtx.meta." + class
	doc := []byte(`{"class":"meta.ddl.aspectType","isDeleted":false,"data":{"canonicalName":"` + class + `","sensitive":` + boolLiteral(sensitive) + `}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root, doc); err != nil {
		t.Fatalf("seed %s DDL: %v", class, err)
	}
}

func boolLiteral(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// newEgressTestHydrator wires a Hydrator over a DDLCache seeded with the
// "identity" class (the harness default) plus a sensitive "ssn" aspect class
// and a non-sensitive "nickname" aspect class — reused across the egress
// hydration table tests below. vaultBackend may be nil (proves ref authoring
// needs no live Vault).
func newEgressTestHydrator(t *testing.T, ctx context.Context, conn *substrate.Conn, vaultBackend vault.Vault) *HydratorImpl {
	t.Helper()
	seedSensitiveAspectClassDDL(t, ctx, conn, "ssn", true)
	seedSensitiveAspectClassDDL(t, ctx, conn, "nickname", false)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	h := NewHydratorWithCache(conn, testCoreBucket, cache, testLogger())
	h.Vault = vaultBackend
	return h
}

// seedCiphertextAspect writes an aspect document whose data is an opaque
// at-rest shape (a real vault.Ciphertext's marshalled fields, or any
// stand-in map — the egress path never calls the Vault, so authenticity of
// the bytes is irrelevant to the ref-marker tests).
func seedCiphertextAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class string) {
	t.Helper()
	doc := []byte(`{"class":"` + class + `","isDeleted":false,"data":{"ct":"Y2lwaGVydGV4dA==","nonce":"bm9uY2U=","keyId":"k1"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, key, doc); err != nil {
		t.Fatalf("seed aspect %s: %v", key, err)
	}
}

// TestEgressReads_SensitiveKey_HydratesAsRef: a sensitive aspect declared
// under contextHint.egressReads hydrates as a $sensitiveRef marker (ciphertext
// verbatim), never plaintext — design sensitive-param-egress §3.2/§3.3.
func TestEgressReads_SensitiveKey_HydratesAsRef(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := newEgressTestHydrator(t, ctx, conn, nil) // no Vault wired — proves ref authoring needs none.

	aspectKey := "vtx.identity." + testNanoID2 + ".ssn"
	seedCiphertextAspect(t, ctx, conn, aspectKey, "ssn")

	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{EgressReads: []string{aspectKey}}

	state, err := h.Hydrate(ctx, env)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	doc, ok := state.Context.Hydrated[aspectKey]
	if !ok {
		t.Fatalf("aspect not hydrated: %+v", state.Context.Hydrated)
	}
	sref, ok := doc.Data["$sensitiveRef"].(map[string]interface{})
	if !ok {
		t.Fatalf("data = %+v, want a $sensitiveRef marker", doc.Data)
	}
	if sref["ref"] != aspectKey {
		t.Fatalf("$sensitiveRef.ref = %v, want %q", sref["ref"], aspectKey)
	}
	ct, ok := sref["ciphertext"].(map[string]interface{})
	if !ok || ct["keyId"] != "k1" {
		t.Fatalf("$sensitiveRef.ciphertext = %+v, want the at-rest ciphertext verbatim", sref["ciphertext"])
	}
	// No plaintext ever touched this execution.
	if state.Context.SensitiveReads == nil || state.Context.SensitiveReads.plaintextRead {
		t.Fatalf("egress disposition must never mark plaintextRead")
	}
}

// TestEgressReads_NonSensitiveKey_HydratesPlain: a non-sensitive aspect under
// egressReads hydrates identically to a plain read — the disposition is
// ref-if-sensitive, not a blanket transform.
func TestEgressReads_NonSensitiveKey_HydratesPlain(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := newEgressTestHydrator(t, ctx, conn, nil)

	aspectKey := "vtx.identity." + testNanoID2 + ".nickname"
	doc := []byte(`{"class":"nickname","isDeleted":false,"data":{"value":"Andy"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, aspectKey, doc); err != nil {
		t.Fatalf("seed nickname aspect: %v", err)
	}

	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{EgressReads: []string{aspectKey}}

	state, err := h.Hydrate(ctx, env)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	got := state.Context.Hydrated[aspectKey]
	if got.Data["value"] != "Andy" {
		t.Fatalf("data = %+v, want the plain aspect unchanged", got.Data)
	}
}

// TestEgressReads_MissingKey_HydrationMiss: egressReads is fail-closed exactly
// like reads — an absent declared key faults HydrationMiss, never a silent
// absence branch (a param template's target is by definition required).
func TestEgressReads_MissingKey_HydrationMiss(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := newEgressTestHydrator(t, ctx, conn, nil)

	missingKey := "vtx.identity." + testNanoID2 + ".ssn"
	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{EgressReads: []string{missingKey}}

	_, err := h.Hydrate(ctx, env)
	var hErr *HydrationError
	if err == nil {
		t.Fatalf("expected HydrationError, got nil")
	}
	if !errors.As(err, &hErr) || hErr.Code != "HydrationMiss" || hErr.MissingKey != missingKey {
		t.Fatalf("got %v, want HydrationMiss for %q", err, missingKey)
	}
}

// TestEgressReads_SensitiveKeyRealVault_DecryptsToRefNotPlaintext proves the
// ref-vs-plaintext split against a REAL Vault (not just "no Vault wired"): a
// sensitive aspect under egressReads authors a ref even though a live,
// working Vault could have decrypted it — the disposition is a submitter
// choice, not a Vault-availability accident.
func TestEgressReads_SensitiveKeyRealVault_DecryptsToRefNotPlaintext(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	v, err := vault.NewLocalBackend([]byte("lattice-testutil-vault-master-ke"), "test-v1")
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	h := newEgressTestHydrator(t, ctx, conn, v)

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
	aspectKey := identityKey + ".ssn"
	seedRealCiphertextAspect(t, ctx, conn, aspectKey, "ssn", ct)

	// Reads (plaintext disposition): confirms the real decrypt still works
	// unchanged (egress=false regression proof).
	plainEnv := newTestEnvelope(testNanoID1)
	plainEnv.ContextHint = &ContextHint{Reads: []string{aspectKey}}
	state, err := h.Hydrate(ctx, plainEnv)
	if err != nil {
		t.Fatalf("Hydrate (reads): %v", err)
	}
	if got, _ := state.Context.Hydrated[aspectKey].Data["value"].(string); got != "123-45-6789" {
		t.Fatalf("reads disposition data = %+v, want decrypted plaintext", state.Context.Hydrated[aspectKey].Data)
	}
	if !state.Context.SensitiveReads.plaintextRead {
		t.Fatalf("reads disposition must mark plaintextRead")
	}

	// egressReads (ref disposition): the SAME key, SAME live Vault — still a
	// ref, never plaintext.
	egressEnv := newTestEnvelope(testNanoID2)
	egressEnv.ContextHint = &ContextHint{EgressReads: []string{aspectKey}}
	state2, err := h.Hydrate(ctx, egressEnv)
	if err != nil {
		t.Fatalf("Hydrate (egressReads): %v", err)
	}
	sref, ok := state2.Context.Hydrated[aspectKey].Data["$sensitiveRef"].(map[string]interface{})
	if !ok {
		t.Fatalf("egressReads disposition data = %+v, want a $sensitiveRef marker despite a live working Vault", state2.Context.Hydrated[aspectKey].Data)
	}
	if sref["ref"] != aspectKey {
		t.Fatalf("$sensitiveRef.ref = %v, want %q", sref["ref"], aspectKey)
	}
	if state2.Context.SensitiveReads.plaintextRead {
		t.Fatalf("egressReads disposition must never mark plaintextRead")
	}
}

func seedPiiKeyAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey string, envelope vault.Envelope) {
	t.Helper()
	b, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	doc := []byte(`{"class":"piiKey","isDeleted":false,"data":` + string(b) + `}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, identityKey+".piiKey", doc); err != nil {
		t.Fatalf("seed piiKey: %v", err)
	}
}

func seedRealCiphertextAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class string, ct vault.Ciphertext) {
	t.Helper()
	b, err := json.Marshal(ct)
	if err != nil {
		t.Fatalf("marshal ciphertext: %v", err)
	}
	doc := []byte(`{"class":"` + class + `","isDeleted":false,"data":` + string(b) + `}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, key, doc); err != nil {
		t.Fatalf("seed ciphertext aspect %s: %v", key, err)
	}
}

// TestContextHint_EgressReadsOverlapsReads_ParseError: a key declared in both
// reads and egressReads is an ambiguous-disposition envelope parse error
// (design §3.1), caught before hydration ever runs.
func TestContextHint_EgressReadsOverlapsReads_ParseError(t *testing.T) {
	key := "vtx.identity." + testNanoID2 + ".ssn"
	raw := []byte(`{
		"requestId": "` + testNanoID1 + `",
		"lane": "default",
		"operationType": "CreateIdentity",
		"actor": "vtx.identity.` + testNanoID2 + `",
		"submittedAt": "2026-05-13T10:00:00Z",
		"payload": {},
		"contextHint": {"reads": ["` + key + `"], "egressReads": ["` + key + `"]}
	}`)
	if _, err := ParseEnvelope(raw); err == nil {
		t.Fatalf("expected a parse error for a key declared in both reads and egressReads")
	}
}

// TestContextHint_EgressReadsOverlapsOptionalReads_ParseError: the same
// ambiguous-disposition rejection extends to optionalReads — without it, the
// optionalReads hydration loop (which runs before egressReads) would win and
// silently cache the key as PLAINTEXT, demoting the egressReads disposition
// instead of refusing loudly.
func TestContextHint_EgressReadsOverlapsOptionalReads_ParseError(t *testing.T) {
	key := "vtx.identity." + testNanoID2 + ".ssn"
	raw := []byte(`{
		"requestId": "` + testNanoID1 + `",
		"lane": "default",
		"operationType": "CreateIdentity",
		"actor": "vtx.identity.` + testNanoID2 + `",
		"submittedAt": "2026-05-13T10:00:00Z",
		"payload": {},
		"contextHint": {"optionalReads": ["` + key + `"], "egressReads": ["` + key + `"]}
	}`)
	if _, err := ParseEnvelope(raw); err == nil {
		t.Fatalf("expected a parse error for a key declared in both optionalReads and egressReads")
	}
}

// TestValidateExternalEgressGuard_PlaintextRead_ExternalEvent_Rejected: the
// commit-path guard (design §3.6) — an op that decrypted sensitive plaintext
// this execution AND emits an external.*-domain event is rejected.
func TestValidateExternalEgressGuard_PlaintextRead_ExternalEvent_Rejected(t *testing.T) {
	state := HydratedState{Context: ScriptContext{SensitiveReads: &sensitiveReadTracker{plaintextRead: true}}}
	result := ScriptResult{Events: []EventSpec{{Class: "external.docGen"}}}
	err := validateExternalEgressGuard(result, state, testNanoID1)
	if err == nil {
		t.Fatalf("expected a DDLViolation, got nil")
	}
	ddlErr, ok := err.(*DDLViolation)
	if !ok {
		t.Fatalf("expected *DDLViolation, got %T: %v", err, err)
	}
	if ddlErr.ViolatedConstraint != "externalEgressSensitivePlaintext" {
		t.Fatalf("ViolatedConstraint = %q, want externalEgressSensitivePlaintext", ddlErr.ViolatedConstraint)
	}
}

// TestValidateExternalEgressGuard_EgressRefOnly_ExternalEvent_Committed: the
// same external.* emission with NO plaintext decrypt (egressReads-only
// disposition) passes — the guard keys on what was decrypted, not on the
// event domain alone.
func TestValidateExternalEgressGuard_EgressRefOnly_ExternalEvent_Committed(t *testing.T) {
	state := HydratedState{Context: ScriptContext{SensitiveReads: &sensitiveReadTracker{plaintextRead: false}}}
	result := ScriptResult{Events: []EventSpec{{Class: "external.docGen"}}}
	if err := validateExternalEgressGuard(result, state, testNanoID1); err != nil {
		t.Fatalf("expected no violation, got %v", err)
	}
}

// TestValidateExternalEgressGuard_PlaintextRead_NonExternalEvent_Committed:
// the deliberate scope bound (design §3.6) — decrypting sensitive plaintext
// and emitting an ORDINARY domain event (not external.*) is unaffected;
// today's DDL-trust surface for non-egress events is unchanged.
func TestValidateExternalEgressGuard_PlaintextRead_NonExternalEvent_Committed(t *testing.T) {
	state := HydratedState{Context: ScriptContext{SensitiveReads: &sensitiveReadTracker{plaintextRead: true}}}
	result := ScriptResult{Events: []EventSpec{{Class: "orchestration.completed"}}}
	if err := validateExternalEgressGuard(result, state, testNanoID1); err != nil {
		t.Fatalf("expected no violation for a non-external event, got %v", err)
	}
}

// TestValidateExternalEgressGuard_NilTracker_Committed: a HydratedState built
// outside Hydrate (e.g. a test fixture) with no tracker never panics and
// never rejects — nil is treated as "nothing decrypted".
func TestValidateExternalEgressGuard_NilTracker_Committed(t *testing.T) {
	result := ScriptResult{Events: []EventSpec{{Class: "external.docGen"}}}
	if err := validateExternalEgressGuard(result, HydratedState{}, testNanoID1); err != nil {
		t.Fatalf("expected no violation with a nil tracker, got %v", err)
	}
}

// TestConnKVReader_EgressKey_ReturnsRefNotPlaintext: the lazy kv.Read seam
// honors the same disposition as step 4 (design §3.1 "one disposition, both
// read paths") — a key present in egressKeys authors a ref even when reached
// through the lazy path rather than pre-hydration.
func TestConnKVReader_EgressKey_ReturnsRefNotPlaintext(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	seedSensitiveAspectClassDDL(t, ctx, conn, "ssn", true)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	aspectKey := "vtx.identity." + testNanoID2 + ".ssn"
	seedCiphertextAspect(t, ctx, conn, aspectKey, "ssn")

	tracker := &sensitiveReadTracker{}
	r := connKVReader{
		conn: conn, bucket: testCoreBucket, ddls: cache, vault: nil,
		egressKeys: map[string]struct{}{aspectKey: {}},
		tracker:    tracker,
	}
	doc, err := r.ReadVertex(ctx, aspectKey)
	if err != nil {
		t.Fatalf("ReadVertex: %v", err)
	}
	sref, ok := doc.Data["$sensitiveRef"].(map[string]interface{})
	if !ok || sref["ref"] != aspectKey {
		t.Fatalf("data = %+v, want a $sensitiveRef marker", doc.Data)
	}
	if tracker.plaintextRead {
		t.Fatalf("lazy egress read must never mark plaintextRead")
	}
}

// TestEgressReads_NoVault_MarkerCarriesNoMAC proves the vaultless-harness
// posture (design sensitive-ref-mac-provenance-design.md §3.2): ref
// authoring with no live Vault mints a marker with no `mac` field at all —
// not an empty string, absent entirely.
func TestEgressReads_NoVault_MarkerCarriesNoMAC(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := newEgressTestHydrator(t, ctx, conn, nil)

	aspectKey := "vtx.identity." + testNanoID2 + ".ssn"
	seedCiphertextAspect(t, ctx, conn, aspectKey, "ssn")

	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{EgressReads: []string{aspectKey}}

	state, err := h.Hydrate(ctx, env)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	sref := state.Context.Hydrated[aspectKey].Data["$sensitiveRef"].(map[string]interface{})
	if _, ok := sref["mac"]; ok {
		t.Fatalf("marker = %+v, want no mac field when no Vault is wired", sref)
	}
}

// TestEgressReads_WithVault_MarkerCarriesValidMAC proves the mint side of
// the ref-provenance rule (design §3.2): a live Vault stamps the marker with
// a MAC that the Vault's own MAC recomputation (what the decryptref
// responder does server-side) verifies — over the SAME requestId the
// hydrating operation carries.
func TestEgressReads_WithVault_MarkerCarriesValidMAC(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	v, err := vault.NewLocalBackend([]byte("lattice-testutil-vault-master-ke"), "test-v1")
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	h := newEgressTestHydrator(t, ctx, conn, v)

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
	aspectKey := identityKey + ".ssn"
	seedRealCiphertextAspect(t, ctx, conn, aspectKey, "ssn", ct)

	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{EgressReads: []string{aspectKey}}

	state, err := h.Hydrate(ctx, env)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	sref := state.Context.Hydrated[aspectKey].Data["$sensitiveRef"].(map[string]interface{})
	macB64, ok := sref["mac"].(string)
	if !ok || macB64 == "" {
		t.Fatalf("marker = %+v, want a non-empty base64 mac field", sref)
	}
	gotMAC, err := base64.StdEncoding.DecodeString(macB64)
	if err != nil {
		t.Fatalf("mac not valid base64: %v", err)
	}

	// The verifier's own recomputation (what the decryptref responder does)
	// must match — mint and verify must agree byte-for-byte on the same
	// requestId the hydrating envelope carries (testNanoID1).
	wantMAC, err := v.MAC(ctx, vault.RefMACPurpose, vault.RefMACInput(aspectKey, testNanoID1, ct))
	if err != nil {
		t.Fatalf("MAC: %v", err)
	}
	if !bytes.Equal(gotMAC, wantMAC) {
		t.Fatalf("mint-side MAC does not match the verifier's independent recomputation")
	}
}

// TestConnKVReader_EgressKeyWithVault_MarkerCarriesValidMAC proves the lazy
// kv.Read seam applies the same MAC-mint disposition as step 4 (design §3.1
// "one disposition, both read paths") — including the requestID threading.
func TestConnKVReader_EgressKeyWithVault_MarkerCarriesValidMAC(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	v, err := vault.NewLocalBackend([]byte("lattice-testutil-vault-master-ke"), "test-v1")
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	seedSensitiveAspectClassDDL(t, ctx, conn, "ssn", true)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
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
	aspectKey := identityKey + ".ssn"
	seedRealCiphertextAspect(t, ctx, conn, aspectKey, "ssn", ct)

	tracker := &sensitiveReadTracker{}
	r := connKVReader{
		conn: conn, bucket: testCoreBucket, ddls: cache, vault: v,
		egressKeys: map[string]struct{}{aspectKey: {}},
		tracker:    tracker,
		requestID:  testNanoID1,
	}
	doc, err := r.ReadVertex(ctx, aspectKey)
	if err != nil {
		t.Fatalf("ReadVertex: %v", err)
	}
	sref := doc.Data["$sensitiveRef"].(map[string]interface{})
	macB64, ok := sref["mac"].(string)
	if !ok || macB64 == "" {
		t.Fatalf("marker = %+v, want a non-empty mac field", sref)
	}
	gotMAC, err := base64.StdEncoding.DecodeString(macB64)
	if err != nil {
		t.Fatalf("mac not valid base64: %v", err)
	}
	wantMAC, err := v.MAC(ctx, vault.RefMACPurpose, vault.RefMACInput(aspectKey, testNanoID1, ct))
	if err != nil {
		t.Fatalf("MAC: %v", err)
	}
	if !bytes.Equal(gotMAC, wantMAC) {
		t.Fatalf("lazy-seam MAC does not match the verifier's independent recomputation")
	}
}

// TestEgressReads_MarkerMAC_JSONRoundTripsToRawBytes proves the mint-side
// base64-STRING encoding of the marker's mac field (chosen to match its
// ciphertext siblings' already-base64-string representation in doc.Data) is
// exactly the wire shape a []byte-typed field expects: marshaling the whole
// marker to JSON (as an external event's params eventually are) and
// unmarshaling "mac" into a []byte field recovers the identical raw MAC —
// no double-encoding, no caller-side base64 handling required.
func TestEgressReads_MarkerMAC_JSONRoundTripsToRawBytes(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	v, err := vault.NewLocalBackend([]byte("lattice-testutil-vault-master-ke"), "test-v1")
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	h := newEgressTestHydrator(t, ctx, conn, v)

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
	aspectKey := identityKey + ".ssn"
	seedRealCiphertextAspect(t, ctx, conn, aspectKey, "ssn", ct)

	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{EgressReads: []string{aspectKey}}
	state, err := h.Hydrate(ctx, env)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	sref := state.Context.Hydrated[aspectKey].Data["$sensitiveRef"]

	// Marshal the whole marker to JSON — exactly what happens when this doc
	// is eventually re-serialized as an external event's params
	// (packages/orchestration-base's dict(sref) copy, then JSON over the
	// wire) — then unmarshal into a typed struct with a []byte mac field.
	raw, err := json.Marshal(sref)
	if err != nil {
		t.Fatalf("marshal marker: %v", err)
	}
	var wire struct {
		Ref        string           `json:"ref"`
		Ciphertext vault.Ciphertext `json:"ciphertext"`
		MAC        []byte           `json:"mac"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal marker: %v", err)
	}

	wantMAC, err := v.MAC(ctx, vault.RefMACPurpose, vault.RefMACInput(aspectKey, testNanoID1, ct))
	if err != nil {
		t.Fatalf("MAC: %v", err)
	}
	if !bytes.Equal(wire.MAC, wantMAC) {
		t.Fatalf("JSON round-tripped mac = %x, want %x (no double-encoding)", wire.MAC, wantMAC)
	}
}

// macFailingVault wraps a real Vault but fails every MAC call — proving the
// mint-failure fail-closed invariant (design §3.2: "a live Vault that fails
// to mint a MAC must never author an unauthenticated ref").
type macFailingVault struct {
	vault.Vault
}

func (macFailingVault) MAC(_ context.Context, _ string, _ []byte) ([]byte, error) {
	return nil, errors.New("macFailingVault: MAC always fails")
}

// TestEgressReads_MACMintFailure_HydrationFailsClosed proves a live Vault
// that cannot mint a MAC fails the hydration loudly rather than falling back
// to an unauthenticated marker.
func TestEgressReads_MACMintFailure_HydrationFailsClosed(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	real, err := vault.NewLocalBackend([]byte("lattice-testutil-vault-master-ke"), "test-v1")
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	v := macFailingVault{Vault: real}
	h := newEgressTestHydrator(t, ctx, conn, v)

	identityKey := "vtx.identity." + testNanoID2
	envelope, err := real.CreateIdentityKey(ctx, identityKey)
	if err != nil {
		t.Fatalf("CreateIdentityKey: %v", err)
	}
	seedPiiKeyAspect(t, ctx, conn, identityKey, envelope)
	ct, err := real.Encrypt(ctx, identityKey, envelope, []byte(`{"value":"123-45-6789"}`))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	aspectKey := identityKey + ".ssn"
	seedRealCiphertextAspect(t, ctx, conn, aspectKey, "ssn", ct)

	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{EgressReads: []string{aspectKey}}

	if _, err := h.Hydrate(ctx, env); err == nil {
		t.Fatalf("expected Hydrate to fail closed when Vault.MAC errors, got nil")
	}
}

// seedDeletedCiphertextAspect writes a tombstoned sensitive aspect that still
// carries its at-rest ciphertext — the shape step 8 produces, since a
// tombstone preserves the prior document body and flips only isDeleted.
func seedDeletedCiphertextAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class string) {
	t.Helper()
	doc := []byte(`{"class":"` + class + `","isDeleted":true,"data":{"ct":"Y2lwaGVydGV4dA==","nonce":"bm9uY2U=","keyId":"k1"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, key, doc); err != nil {
		t.Fatalf("seed deleted aspect %s: %v", key, err)
	}
}

// TestEgressReads_DeletedSensitiveKey_HydrationFailsClosed: a tombstoned
// sensitive aspect must never be minted into a $sensitiveRef — the bridge
// would unwrap it at the egress boundary and hand a deleted subject's PII to
// a third party. Same rule as the Refractor's soft-deleted piiKey guard
// (internal/refractor/pipeline/secure.go).
func TestEgressReads_DeletedSensitiveKey_HydrationFailsClosed(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := newEgressTestHydrator(t, ctx, conn, nil)

	aspectKey := "vtx.identity." + testNanoID2 + ".ssn"
	seedDeletedCiphertextAspect(t, ctx, conn, aspectKey, "ssn")

	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{EgressReads: []string{aspectKey}}

	if _, err := h.Hydrate(ctx, env); err == nil {
		t.Fatalf("expected Hydrate to fail closed on a deleted sensitive aspect, got nil")
	}
}

// TestReads_DeletedSensitiveKey_HydrationFailsClosed: the plaintext
// disposition is guarded identically — a tombstoned sensitive aspect never
// decrypts into a script's context. The guard precedes the nil-Vault early
// return, so it holds even for a pipeline that never wired a Vault.
func TestReads_DeletedSensitiveKey_HydrationFailsClosed(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := newEgressTestHydrator(t, ctx, conn, nil)

	aspectKey := "vtx.identity." + testNanoID2 + ".ssn"
	seedDeletedCiphertextAspect(t, ctx, conn, aspectKey, "ssn")

	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{Reads: []string{aspectKey}}

	if _, err := h.Hydrate(ctx, env); err == nil {
		t.Fatalf("expected Hydrate to fail closed on a deleted sensitive aspect, got nil")
	}
}

// TestReads_DeletedNonSensitiveKey_HydratesNormally: the guard is scoped to
// sensitive classes — an ordinary tombstoned aspect still hydrates, so a
// script can read-before-create against it.
func TestReads_DeletedNonSensitiveKey_HydratesNormally(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := newEgressTestHydrator(t, ctx, conn, nil)

	aspectKey := "vtx.identity." + testNanoID2 + ".nickname"
	doc := []byte(`{"class":"nickname","isDeleted":true,"data":{"value":"Andy"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, aspectKey, doc); err != nil {
		t.Fatalf("seed nickname aspect: %v", err)
	}

	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{Reads: []string{aspectKey}}

	state, err := h.Hydrate(ctx, env)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if got := state.Context.Hydrated[aspectKey]; !got.IsDeleted || got.Data["value"] != "Andy" {
		t.Fatalf("doc = %+v, want the tombstoned non-sensitive aspect unchanged", got)
	}
}
