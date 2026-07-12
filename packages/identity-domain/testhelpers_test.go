// Shared helpers for identity-domain package tests.
//
// These tests live in an external test package (`identitydomain_test`)
// so they exercise only the public Lattice surface that any Capability
// Package would see in production:
//   - bootstrap.SeedPrimordial seeds the kernel.
//   - testutil.InstallPhase1Packages installs rbac-domain +
//     identity-domain + identity-hygiene against that kernel.
//   - Tests submit ops, run the standard pipeline, and assert outcomes.
package identitydomain_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
	"github.com/asolgan/lattice/internal/vault"
)

// Test actor NanoIDs. 20 chars, substrate.Alphabet only (no I/O/l/0).
const (
	staffActorID  = "JstffActHJKMNPQRSTUV"
	staffActorKey = "vtx.identity." + staffActorID
	staffCapKey   = "cap.identity." + staffActorID

	consumerActorID  = "JcnsmActHJKMNPQRSTUV"
	consumerActorKey = "vtx.identity." + consumerActorID
	consumerCapKey   = "cap.identity." + consumerActorID

	// secondCredActorID/Key/CapKey is A2 — a second raw credential distinct
	// from consumerActorKey (U), used by InitiateCredentialLink/
	// CompleteCredentialLink tests (multi-credential-identity-linking-design.md
	// §3.2).
	secondCredActorID  = "JsecCrdHJKMNPQRSTUVW"
	secondCredActorKey = "vtx.identity." + secondCredActorID
	secondCredCapKey   = "cap.identity." + secondCredActorID

	gatewayActorID  = "JgtwyActHJKMNPQRSTUV"
	gatewayActorKey = "vtx.identity." + gatewayActorID
	gatewayCapKey   = "cap.identity." + gatewayActorID
)

// staffCapDoc seeds a cap doc granting the operator-equivalent staff
// actor the platformPermissions used by identity-domain tests:
// CreateUnclaimedIdentity (scope=any) + UpdateIdentityState (scope=any).
func staffCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    staffCapKey,
		Actor:                  staffActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{staffActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateUnclaimedIdentity", Scope: "any"},
			{OperationType: "UpdateIdentityState", Scope: "any"},
			{OperationType: "RotateClaimKey", Scope: "any"},
			{OperationType: "RecordIdentityPII", Scope: "any"},
			{OperationType: "RevokeActor", Scope: "any"},
			{OperationType: "UnrevokeActor", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// consumerCapDoc seeds a cap doc granting ClaimIdentity + InitiateCredentialLink
// (both scope=self) — the two ops a claimed consumer identity (U) submits
// through the normal resolved path (op.actor == U).
func consumerCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    consumerCapKey,
		Actor:                  consumerActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{consumerActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "ClaimIdentity", Scope: "self"},
			{OperationType: "InitiateCredentialLink", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role.consumer"},
	}
}

// secondCredCapDoc seeds a cap doc for A2 — a second raw credential distinct
// from U — granting only CompleteCredentialLink (scope=self), the op A2
// submits as its raw, unresolved self (Gateway raw-credential carve-out).
func secondCredCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    secondCredCapKey,
		Actor:                  secondCredActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{secondCredActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CompleteCredentialLink", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role.consumer"},
	}
}

// gatewayCapDoc seeds a cap doc granting only ProvisionConsumerIdentity —
// the Gateway's own narrow identityProvisioner-equivalent grant (scope=any).
func gatewayCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    gatewayCapKey,
		Actor:                  gatewayActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{gatewayActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "ProvisionConsumerIdentity", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role.identityProvisioner"},
	}
}

// setupTestEnv assembles the standard identity-domain test environment:
// embedded NATS, KV buckets, primordials seeded, Phase 1 packages
// installed, staff + consumer + gateway cap docs seeded.
func setupTestEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, staffCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, consumerCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, secondCredCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, gatewayCapDoc())
	return ctx, conn
}

// readAspectData reads a KV aspect and returns its data map. For a
// sensitive aspect (Contract #3 §3.10) this returns the raw ciphertext
// envelope {ct,nonce,keyId}, NOT plaintext — use readDecryptedAspectData for
// ssn/dob/name/email/phone/claimKey/credentialBinding.
func readAspectData(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) map[string]any {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	data, _ := doc["data"].(map[string]any)
	return data
}

// readDecryptedAspectData reads a sensitive aspect (identityKey.localName)
// and decrypts it via the shared TestVault, returning the plaintext data map
// exactly as readAspectData would have returned pre-Vault. Requires the
// identity to already carry a piiKey (written by the real Processor's
// step-6.5 encrypt hook, or by seedSensitiveAspect for a directly-seeded
// fixture).
func readDecryptedAspectData(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey, localName string) map[string]any {
	t.Helper()
	v := testutil.TestVault(t)
	env := readTestPiiKeyEnvelope(t, ctx, conn, identityKey)

	aspectKey := identityKey + "." + localName
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, aspectKey)
	if err != nil {
		t.Fatalf("KVGet %s: %v", aspectKey, err)
	}
	var doc struct {
		Data vault.Ciphertext `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", aspectKey, err)
	}
	plaintext, err := v.Decrypt(ctx, identityKey, env, doc.Data)
	if err != nil {
		t.Fatalf("decrypt %s: %v", aspectKey, err)
	}
	var value map[string]any
	if err := json.Unmarshal(plaintext, &value); err != nil {
		t.Fatalf("unmarshal decrypted %s: %v", aspectKey, err)
	}
	return value
}

// readTestPiiKeyEnvelope reads and parses identityKey's piiKey aspect.
func readTestPiiKeyEnvelope(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey string) vault.Envelope {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, identityKey+".piiKey")
	if err != nil {
		t.Fatalf("KVGet piiKey for %s: %v", identityKey, err)
	}
	var doc struct {
		Data vault.Envelope `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal piiKey for %s: %v", identityKey, err)
	}
	return doc.Data
}

// seedSensitiveAspect writes a sensitive aspect directly to Core KV (no op
// required), ciphertext-encoded exactly as the real Processor's step-6.5
// encrypt hook would produce it — lazily minting the identity's piiKey via
// the shared TestVault if absent. Used by fixture helpers that pre-seed
// state the real create/claim ops don't cover (e.g. a pre-claimed identity),
// so decrypt-on-read (step 4 / kv.Read) works against fixture data exactly
// as it would against a real commit.
func seedSensitiveAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey, localName string, plaintext map[string]any) {
	t.Helper()
	v := testutil.TestVault(t)
	env := ensureTestPiiKey(t, ctx, conn, v, identityKey)

	pt, err := json.Marshal(plaintext)
	if err != nil {
		t.Fatalf("marshal plaintext for %s.%s: %v", identityKey, localName, err)
	}
	ct, err := v.Encrypt(ctx, identityKey, env, pt)
	if err != nil {
		t.Fatalf("encrypt %s.%s: %v", identityKey, localName, err)
	}
	doc := map[string]any{
		"class":     localName,
		"vertexKey": identityKey,
		"localName": localName,
		"isDeleted": false,
		"data":      ct,
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey+"."+localName, b); err != nil {
		t.Fatalf("seed %s aspect: %v", localName, err)
	}
}

// ensureTestPiiKey returns identityKey's existing piiKey envelope, or mints
// and seeds a fresh one via v — the fixture-side mirror of the Processor's
// step-6.5 lazy piiKey creation.
func ensureTestPiiKey(t *testing.T, ctx context.Context, conn *substrate.Conn, v vault.Vault, identityKey string) vault.Envelope {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, identityKey+".piiKey")
	if err == nil {
		var doc struct {
			Data vault.Envelope `json:"data"`
		}
		if uerr := json.Unmarshal(entry.Value, &doc); uerr != nil {
			t.Fatalf("unmarshal piiKey for %s: %v", identityKey, uerr)
		}
		return doc.Data
	}
	if !errors.Is(err, substrate.ErrKeyNotFound) {
		t.Fatalf("read piiKey for %s: %v", identityKey, err)
	}
	env, err := v.CreateIdentityKey(ctx, identityKey)
	if err != nil {
		t.Fatalf("CreateIdentityKey for %s: %v", identityKey, err)
	}
	doc := map[string]any{
		"class":     "piiKey",
		"vertexKey": identityKey,
		"localName": "piiKey",
		"isDeleted": false,
		"data":      env,
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey+".piiKey", b); err != nil {
		t.Fatalf("seed piiKey for %s: %v", identityKey, err)
	}
	return env
}

// identityIDFromRequestID returns the first NanoID the identity DDL's
// Starlark would generate from the given requestId — the identity ID.
// Under Option C the script no longer mints a claim-key plaintext; the
// client supplies claimKeyHash, so a single deterministic NanoID is all
// the test needs to predict the created identity key.
func identityIDFromRequestID(requestID string) string {
	seed := processor.SeedFromRequestID(requestID)
	pcg := rand.NewPCG(seed[0], seed[1])
	return processor.DeterministicNanoID(pcg, substrate.NanoIDLength)
}

// contactIndexKey mirrors the Starlark `crypto.sha256NanoID(prefix +
// ":" + value)` computation. contactType is "email" or "phone";
// value is the normalized contact.
func contactIndexKey(contactType, value string) string {
	return "vtx.identityindex." + sha256NanoID(contactType+":"+value)
}

// credentialIndexKey mirrors `crypto.sha256NanoID(actorKey)`.
func credentialIndexKey(actorKey string) string {
	return "vtx.credentialindex." + sha256NanoID(actorKey)
}

// sha256NanoID reproduces the crypto.sha256NanoID Starlark builtin —
// PCG-seeded NanoID from SHA-256 of the input.
func sha256NanoID(s string) string {
	sum := sha256.Sum256([]byte(s))
	seed := [2]uint64{
		(uint64(sum[0]) << 56) | (uint64(sum[1]) << 48) | (uint64(sum[2]) << 40) | (uint64(sum[3]) << 32) |
			(uint64(sum[4]) << 24) | (uint64(sum[5]) << 16) | (uint64(sum[6]) << 8) | uint64(sum[7]),
		(uint64(sum[8]) << 56) | (uint64(sum[9]) << 48) | (uint64(sum[10]) << 40) | (uint64(sum[11]) << 32) |
			(uint64(sum[12]) << 24) | (uint64(sum[13]) << 16) | (uint64(sum[14]) << 8) | uint64(sum[15]),
	}
	pcg := rand.NewPCG(seed[0], seed[1])
	return processor.DeterministicNanoID(pcg, substrate.NanoIDLength)
}

// sha256HexOf returns the hex-encoded SHA-256 hash of s.
func sha256HexOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// seedDirectIdentity writes a minimal identity vertex + state aspect
// directly to Core KV (no op required). Used to pre-set specific
// states for rejection tests.
func seedDirectIdentity(t *testing.T, ctx context.Context, conn *substrate.Conn,
	identityKey, state, mergedInto string) {
	t.Helper()
	vtxDoc := map[string]any{
		"class":     "identity",
		"isDeleted": false,
		"data":      map[string]any{},
	}
	vb, _ := json.Marshal(vtxDoc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey, vb); err != nil {
		t.Fatalf("seed identity vertex %s: %v", identityKey, err)
	}
	stateDoc := map[string]any{
		"class": "state", "vertexKey": identityKey, "localName": "state",
		"isDeleted": false, "data": map[string]any{"value": state},
	}
	sb, _ := json.Marshal(stateDoc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey+".state", sb); err != nil {
		t.Fatalf("seed state aspect %s: %v", identityKey, err)
	}
	if mergedInto != "" {
		miDoc := map[string]any{
			"class": "mergedInto", "vertexKey": identityKey, "localName": "mergedInto",
			"isDeleted": false, "data": map[string]any{"value": mergedInto},
		}
		mb, _ := json.Marshal(miDoc)
		if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey+".mergedInto", mb); err != nil {
			t.Fatalf("seed mergedInto aspect %s: %v", identityKey, err)
		}
	}
}

// seedClaimKeyAspect writes a claimKey aspect with a given pre-computed hash,
// sensitive-encrypted exactly as the real Processor would (via
// seedSensitiveAspect) so ClaimIdentity's decrypt-on-read of `.claimKey`
// works against this fixture identically to a real CreateUnclaimedIdentity.
func seedClaimKeyAspect(t *testing.T, ctx context.Context, conn *substrate.Conn,
	identityKey, hashHex string) {
	t.Helper()
	for len(hashHex) < 64 {
		hashHex += "0"
	}
	if len(hashHex) > 64 {
		hashHex = hashHex[:64]
	}
	seedSensitiveAspect(t, ctx, conn, identityKey, "claimKey", map[string]any{"hash": hashHex, "algo": "sha256"})
}
