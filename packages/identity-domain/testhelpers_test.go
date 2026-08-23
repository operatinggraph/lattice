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
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	"github.com/operatinggraph/lattice/internal/vault"
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

	// frontDeskActorID/Key/CapKey is a real (non-operator) frontOfHouse staff
	// actor — used by RecordIdentityPII's confinement tests (§3.2), which need
	// to distinguish "staff" from "root" the way staffActorKey (operator-
	// equivalent, despite the name) cannot.
	frontDeskActorID  = "JfrntDskHJKMNPQRSTUV"
	frontDeskActorKey = "vtx.identity." + frontDeskActorID
	frontDeskCapKey   = "cap.identity." + frontDeskActorID
	// The task path reads a DISJOINT entry (Contract #6 §6.6): FR56 ephemeral
	// grants live in `cap.ephemeral.<actor>`, never in the `cap.<actor>` doc,
	// which carries roles/permissions/service access only.
	frontDeskEphemeralCapKey = "cap.ephemeral.identity." + frontDeskActorID
)

// frontOfHouseRoleKey is identity-domain's own frontOfHouse role, resolved
// the same deterministic way consumerRoleKey is in the package's own script.
var frontOfHouseRoleKey = "vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")

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
			{OperationType: "ReconcileCredentialBinding", Scope: "any"},
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

// frontDeskCapDoc seeds a cap doc for a real frontOfHouse staff actor —
// granted RecordIdentityPII (scope=any) at the capability-KV/step-3 layer, so
// RecordIdentityPII's own script-level confinement (§3.2) is what a rejection
// in these tests actually proves, not a missing platform grant.
func frontDeskCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    frontDeskCapKey,
		Actor:                  frontDeskActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{frontDeskActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "RecordIdentityPII", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{frontOfHouseRoleKey},
	}
}

// frontDeskTaskGrantDoc is what orchestration-base's capabilityEphemeral lens
// projects for an onboarding userTask scopedTo one identity: the ONE grant that
// lets the front-desk actor record that identity's PII regardless of its
// claimed state, expiring with the task. Seeded per-test (the task key and the
// subject vary), not from setupTestEnv.
func frontDeskTaskGrantDoc(taskKey, subjectKey string) *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    frontDeskEphemeralCapKey,
		Actor:                  frontDeskActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{frontDeskActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    []processor.PlatformPermission{},
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{{
			Source:        taskKey,
			TaskKey:       taskKey,
			OperationType: "RecordIdentityPII",
			Target:        subjectKey,
			ExpiresAt:     now.Add(24 * time.Hour).Format(time.RFC3339Nano),
		}},
	}
}

// setupTestEnv assembles the standard identity-domain test environment:
// embedded NATS, KV buckets, primordials seeded, Phase 1 packages
// installed, staff + consumer + gateway + frontDesk cap docs seeded. staffKey
// and frontDeskActorKey both get their GRAPH holdsRole link seeded too (not
// just the cap doc) — RecordIdentityPII's confinement guard walks the graph
// (testutil.SeedHoldsRole's doc comment), so an actor carrying only a cap doc
// looks like an unprivileged caller no matter what it grants.
func setupTestEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, staffCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, consumerCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, secondCredCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, gatewayCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, frontDeskCapDoc())
	testutil.SeedHoldsRole(t, ctx, conn, staffActorKey, bootstrap.RoleOperatorKey)
	testutil.SeedHoldsRole(t, ctx, conn, frontDeskActorKey, frontOfHouseRoleKey)
	// The credential actors the ceremony ops submit as. ClaimIdentity and
	// CompleteCredentialLink refuse an actor with no live identity vertex,
	// which is what the Gateway's first-touch provisioning establishes on the
	// real path; a cap doc alone models an actor the auth plane knows and the
	// graph does not.
	testutil.SeedCredentialActor(t, ctx, conn, consumerActorKey, consumerRoleKey(t))
	testutil.SeedCredentialActor(t, ctx, conn, secondCredActorKey, consumerRoleKey(t))
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

// seedSpentClaimKeyAspect writes the claimKey aspect a completed claim leaves
// behind: tombstoned, but still carrying the encrypted body, because step 8
// preserves the prior document under a tombstone. Any read path that treats
// the retained body as live data is reading a spent secret.
func seedSpentClaimKeyAspect(t *testing.T, ctx context.Context, conn *substrate.Conn,
	identityKey, hashHex string) {
	t.Helper()
	seedClaimKeyAspect(t, ctx, conn, identityKey, hashHex)
	key := identityKey + ".claimKey"
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("read seeded %s: %v", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal seeded %s: %v", key, err)
	}
	doc["isDeleted"] = true
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("tombstone %s: %v", key, err)
	}
}

// racingPipelineConfig names the one operation a racing pipeline interposes on
// and the lane it consumes, so the same pipeline can drive a test's fixture ops
// without tripping the hook.
type racingPipelineConfig struct {
	Durable       string
	FilterSubject string
	OperationType string
}

// interposingValidator runs hook exactly once, on the first operation of the
// named type to reach step 6, and then delegates. Step 6 sits AFTER the script
// has read state and decided its mutations and BEFORE step 8 takes its own
// prior-document read and commits, so a write made from the hook lands in
// precisely the window a concurrent writer of a shared vertex occupies. Nothing
// else reproduces that window deterministically: the two ends of it are inside
// one synchronous commit-path call.
//
// This is what makes a revision pin testable. A mutation carrying the revision
// its own read observed must reject here; one relying on step 8's later
// prior-document read commits and silently overwrites the racing writer.
type interposingValidator struct {
	inner  processor.Validator
	opType string
	once   sync.Once
	hook   func()
}

func (v *interposingValidator) Validate(ctx context.Context, env *processor.OperationEnvelope,
	result processor.ScriptResult, state processor.HydratedState) error {
	if env.OperationType == v.opType {
		v.once.Do(v.hook)
	}
	return v.inner.Validate(ctx, env, result, state)
}

// newRacingPipeline is testutil.CapabilityPipeline with the validator wrapped,
// so a test can commit a competing write to a key the operation under test
// already read. Built here rather than added to the shared harness because the
// interposition is meaningful for exactly this package's shared-vertex writes.
func newRacingPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cfg racingPipelineConfig, hook func()) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	logger := testutil.TestLogger()
	metrics := &processor.Metrics{}
	cache := processor.NewDDLCache(conn, testutil.HarnessCoreBucket, logger)
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("ddl cache refresh: %v", err)
	}
	authz, err := processor.SelectAuthorizerArgs(processor.SelectAuthorizerOpts{
		Mode:             processor.AuthModeCapability,
		Reader:           conn,
		CapabilityBucket: testutil.HarnessCapBucket,
		Logger:           logger,
	})
	if err != nil {
		t.Fatalf("SelectAuthorizerArgs: %v", err)
	}
	v := testutil.TestVault(t)
	hydrator := processor.NewHydratorWithCache(conn, testutil.HarnessCoreBucket, cache, logger)
	hydrator.Vault = v
	hydrator.PrimordialActors = testutil.PrimordialActors(t)
	cp := processor.NewCommitPath(processor.Deps{
		Conn:       conn,
		CoreBucket: testutil.HarnessCoreBucket,
		HealthKV:   testutil.HarnessHealthBucket,
		Authorizer: authz,
		Hydrator:   hydrator,
		Executor:   processor.NewExecutor(processor.NewStarlarkRunner(0, 0), logger),
		Validator: &interposingValidator{
			inner:  processor.NewValidator(cache, conn, testutil.HarnessCoreBucket, logger),
			opType: cfg.OperationType,
			hook:   hook,
		},
		Committer:   processor.NewCommitter(conn, testutil.HarnessCoreBucket, cache, logger, time.Now),
		Metrics:     metrics,
		Heartbeater: processor.NewHealthHeartbeater(conn, testutil.HarnessHealthBucket, "racing-"+cfg.Durable, 10*time.Second, metrics, logger),
		Logger:      logger,
		Vault:       v,
		DDLs:        cache,
	})
	cons, err := processor.EnsureConsumer(ctx, conn.JetStream(), processor.ConsumerConfig{
		StreamName:     testutil.HarnessOpsStream,
		Durable:        cfg.Durable,
		FilterSubjects: []string{cfg.FilterSubject},
		AckWait:        5 * time.Second,
	}, logger)
	if err != nil {
		t.Fatalf("EnsureConsumer: %v", err)
	}
	return cp, cons
}
