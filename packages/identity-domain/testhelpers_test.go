// Story 4.7 cleanup — shared helpers for identity-domain package tests.
//
// These tests live in an external test package (`identitydomain_test`)
// so they exercise only the public Lattice surface that any Capability
// Package would see in production:
//   - bootstrap.SeedPrimordial seeds the kernel.
//   - testutil.InstallPhase1Packages installs rbac-domain +
//     identity-domain + identity-hygiene against that kernel.
//   - Tests submit ops, run the standard pipeline, and assert outcomes.
//
// The package was previously tested via
// internal/processor/identity_*_test.go — which seeded the identity
// DDL via the legacy bootstrap.IdentityDDL() helper. Story 4.7
// retired that helper; tests now read the same DDL via the installed
// package and assert through the production pipeline.
package identitydomain_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
)

// Test actor NanoIDs. 20 chars, substrate.Alphabet only (no I/O/l/0).
const (
	staffActorID  = "JstffActHJKMNPQRSTUV"
	staffActorKey = "vtx.identity." + staffActorID
	staffCapKey   = "cap.identity." + staffActorID

	consumerActorID  = "JcnsmActHJKMNPQRSTUV"
	consumerActorKey = "vtx.identity." + consumerActorID
	consumerCapKey   = "cap.identity." + consumerActorID
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
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// consumerCapDoc seeds a cap doc granting only ClaimIdentity (scope=self).
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
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role.consumer"},
	}
}

// setupTestEnv assembles the standard identity-domain test environment:
// embedded NATS, KV buckets, primordials seeded, Phase 1 packages
// installed, staff + consumer cap docs seeded.
func setupTestEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, staffCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, consumerCapDoc())
	return ctx, conn
}

// readAspectData reads a KV aspect and returns its data map.
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

// nanoIDsFromRequestID returns the first two NanoIDs the identity
// DDL's Starlark would generate from the given requestId. The first
// is the identity ID; the second is the claim-key plaintext.
func nanoIDsFromRequestID(requestID string) (identityID, claimKeyPlaintext string) {
	seed := processor.SeedFromRequestID(requestID)
	pcg := rand.NewPCG(seed[0], seed[1])
	identityID = processor.DeterministicNanoID(pcg, substrate.NanoIDLength)
	claimKeyPlaintext = processor.DeterministicNanoID(pcg, substrate.NanoIDLength)
	return
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

// seedClaimKeyAspect writes a claimKey aspect with a given pre-computed hash.
func seedClaimKeyAspect(t *testing.T, ctx context.Context, conn *substrate.Conn,
	identityKey, hashHex string) {
	t.Helper()
	for len(hashHex) < 64 {
		hashHex += "0"
	}
	if len(hashHex) > 64 {
		hashHex = hashHex[:64]
	}
	doc := map[string]any{
		"class":     "claimKey",
		"vertexKey": identityKey,
		"localName": "claimKey",
		"isDeleted": false,
		"data":      map[string]any{"hash": hashHex, "algo": "sha256"},
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey+".claimKey", b); err != nil {
		t.Fatalf("seed claimKey aspect %s: %v", identityKey, err)
	}
}
