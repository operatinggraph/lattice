package processor

// The read path resolves a sensitive aspect's key holder from the ciphertext's
// own keyId, never from the aspect's anchor. These tests are the divergence
// bar for the Processor's two decrypt-on-read dispositions: every fixture here
// seals under a holder the anchor does NOT name, so a resolver that still
// consulted the anchor could not make any of them pass.

import (
	"context"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

const (
	retentionHolderID = "RetCLassHoLderAAAAAA"
	encounterAnchorID = "AppointmentAnchorAAA"
)

// newKeyIDTestVault builds a real LocalBackend — fake crypto would prove
// nothing about the AEAD binding that makes keyId trustworthy in the first
// place.
func newKeyIDTestVault(t *testing.T) *vault.LocalBackend {
	t.Helper()
	v, err := vault.NewLocalBackend([]byte("lattice-testutil-vault-master-ke"), "test-v1")
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	return v
}

// sealUnderHolder mints holderKey's DEK, seeds its piiKey, encrypts plaintext
// under it, and writes the result at aspectKey — whose anchor is deliberately
// unrelated to holderKey.
func sealUnderHolder(t *testing.T, ctx context.Context, conn *substrate.Conn, v *vault.LocalBackend, holderKey, aspectKey, class, plaintext string) {
	t.Helper()
	envelope, err := v.CreateIdentityKey(ctx, holderKey)
	if err != nil {
		t.Fatalf("CreateIdentityKey(%s): %v", holderKey, err)
	}
	seedPiiKeyAspect(t, ctx, conn, holderKey, envelope)
	ct, err := v.Encrypt(ctx, holderKey, envelope, []byte(plaintext))
	if err != nil {
		t.Fatalf("Encrypt under %s: %v", holderKey, err)
	}
	seedRealCiphertextAspect(t, ctx, conn, aspectKey, class, ct)
}

// A record anchored on an appointment and custodied by a retention class
// decrypts on the reads disposition. The anchor is not an identity and holds
// no key of its own, so this passes only if the holder came from the
// ciphertext.
func TestDecryptSensitiveDoc_ClassHolderOnNonIdentityAnchor_Decrypts(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	v := newKeyIDTestVault(t)
	h := newEgressTestHydrator(t, ctx, conn, v)

	holderKey := "vtx.retentionclass." + retentionHolderID
	aspectKey := "vtx.appointment." + encounterAnchorID + ".ssn"
	sealUnderHolder(t, ctx, conn, v, holderKey, aspectKey, "ssn", `{"value":"chart note"}`)

	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{Reads: []string{aspectKey}}
	state, err := h.Hydrate(ctx, env)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if got, _ := state.Context.Hydrated[aspectKey].Data["value"].(string); got != "chart note" {
		t.Fatalf("data = %+v, want the plaintext decrypted under the class holder", state.Context.Hydrated[aspectKey].Data)
	}
	if _, ok := state.Context.SensitiveReads.plaintextKeys[aspectKey]; !ok {
		t.Fatalf("a decrypted class-held aspect must still record as carrying plaintext")
	}
}

// The divergence proof on an identity anchor: the ciphertext is sealed under
// one identity while the aspect hangs off a different one, and BOTH have a
// live piiKey. Only the ciphertext's keyId opens it — resolving from the
// anchor would reach a real, usable, wrong DEK and fail the AEAD tag.
func TestDecryptSensitiveDoc_KeyIDWinsOverADivergentAnchor(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	v := newKeyIDTestVault(t)
	h := newEgressTestHydrator(t, ctx, conn, v)

	sealingIdentity := "vtx.identity." + testNanoID1
	anchorIdentity := "vtx.identity." + testNanoID2
	anchorEnvelope, err := v.CreateIdentityKey(ctx, anchorIdentity)
	if err != nil {
		t.Fatalf("CreateIdentityKey(anchor): %v", err)
	}
	seedPiiKeyAspect(t, ctx, conn, anchorIdentity, anchorEnvelope)

	aspectKey := anchorIdentity + ".ssn"
	sealUnderHolder(t, ctx, conn, v, sealingIdentity, aspectKey, "ssn", `{"value":"123-45-6789"}`)

	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{Reads: []string{aspectKey}}
	state, err := h.Hydrate(ctx, env)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if got, _ := state.Context.Hydrated[aspectKey].Data["value"].(string); got != "123-45-6789" {
		t.Fatalf("data = %+v, want the plaintext decrypted under the SEALING identity", state.Context.Hydrated[aspectKey].Data)
	}
}

// A ciphertext naming no usable holder is refused, and the refusal never falls
// back to the anchor — which here is a perfectly good identity with a live
// key. A fallback would open a malformed record under a holder it never named.
func TestDecryptSensitiveDoc_MalformedKeyIDNeverFallsBackToTheAnchor(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	v := newKeyIDTestVault(t)
	h := newEgressTestHydrator(t, ctx, conn, v)

	anchorIdentity := "vtx.identity." + testNanoID2
	envelope, err := v.CreateIdentityKey(ctx, anchorIdentity)
	if err != nil {
		t.Fatalf("CreateIdentityKey: %v", err)
	}
	seedPiiKeyAspect(t, ctx, conn, anchorIdentity, envelope)
	ct, err := v.Encrypt(ctx, anchorIdentity, envelope, []byte(`{"value":"123-45-6789"}`))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	ct.KeyID = "not-a-vertex-key"
	aspectKey := anchorIdentity + ".ssn"
	seedRealCiphertextAspect(t, ctx, conn, aspectKey, "ssn", ct)

	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{Reads: []string{aspectKey}}
	if _, err := h.Hydrate(ctx, env); err == nil {
		t.Fatalf("a ciphertext with a malformed keyId must be refused, not opened under the anchor")
	} else if !strings.Contains(err.Error(), "resolve key holder") {
		t.Fatalf("err = %v, want the holder-resolution refusal", err)
	}
}

// The egress disposition refuses a holder the bridge cannot serve. The
// piiKeyEnvelope lens enumerates identity holders alone, so authoring a ref
// for a class-held record would produce an envelope that never projects; the
// refusal happens where the operation is authored instead.
func TestEgressReads_ClassHeldRecord_RefusedAtMint(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	v := newKeyIDTestVault(t)
	h := newEgressTestHydrator(t, ctx, conn, v)

	holderKey := "vtx.retentionclass." + retentionHolderID
	aspectKey := "vtx.appointment." + encounterAnchorID + ".ssn"
	sealUnderHolder(t, ctx, conn, v, holderKey, aspectKey, "ssn", `{"value":"chart note"}`)

	env := asPrimordialEngine(newTestEnvelope(testNanoID2))
	env.ContextHint = &ContextHint{EgressReads: []string{aspectKey}}
	_, err := h.Hydrate(ctx, env)
	if err == nil {
		t.Fatalf("a class-held record must not be authored as an egress ref")
	}
	if !strings.Contains(err.Error(), "retentionclass") {
		t.Fatalf("err = %v, want the refusal to name the holder type", err)
	}
}

// An identity-held record still egresses — the refusal above is narrow, not a
// blanket close of the egress path.
func TestEgressReads_IdentityHeldRecord_StillAuthorsARef(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	v := newKeyIDTestVault(t)
	h := newEgressTestHydrator(t, ctx, conn, v)

	identityKey := "vtx.identity." + testNanoID2
	aspectKey := identityKey + ".ssn"
	sealUnderHolder(t, ctx, conn, v, identityKey, aspectKey, "ssn", `{"value":"123-45-6789"}`)

	env := asPrimordialEngine(newTestEnvelope(testNanoID2))
	env.ContextHint = &ContextHint{EgressReads: []string{aspectKey}}
	state, err := h.Hydrate(ctx, env)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if _, ok := state.Context.Hydrated[aspectKey].Data["$sensitiveRef"].(map[string]interface{}); !ok {
		t.Fatalf("data = %+v, want a $sensitiveRef marker", state.Context.Hydrated[aspectKey].Data)
	}
}
