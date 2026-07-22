package processor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

// newEncryptTestCommitPath wires a CommitPath with a real Vault backend and a
// DDLCache seeded with a sensitive "ssn" aspect class and a non-sensitive
// "nickname" class — the harness step 6.5's tests share.
func newEncryptTestCommitPath(t *testing.T, ctx context.Context, conn *substrate.Conn) (*CommitPath, vault.Vault) {
	t.Helper()
	seedSensitiveAspectClassDDL(t, ctx, conn, "ssn", true)
	seedSensitiveAspectClassDDL(t, ctx, conn, "nickname", false)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	v, err := vault.NewLocalBackend([]byte("lattice-testutil-vault-master-ke"), "test-v1")
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	return &CommitPath{deps: Deps{
		Conn:       conn,
		CoreBucket: testCoreBucket,
		Vault:      v,
		DDLs:       cache,
		Logger:     testLogger(),
	}}, v
}

func sensitiveMutation(key, class string, value string) MutationOp {
	return MutationOp{
		Op:  "create",
		Key: key,
		Document: map[string]interface{}{
			"class":     class,
			"isDeleted": false,
			"data":      map[string]interface{}{"value": value},
		},
	}
}

// TestEncryptSensitiveMutations_MintsKeyAndEncrypts: a sensitive-class
// mutation for an identity with no piiKey yet mints one (appended to the
// batch as an extra create), reports mintedPiiKey=true, and the mutation's
// data is opaque ciphertext, never plaintext.
func TestEncryptSensitiveMutations_MintsKeyAndEncrypts(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cp, _ := newEncryptTestCommitPath(t, ctx, conn)

	identityKey := "vtx.identity." + testNanoID2
	aspectKey := identityKey + ".ssn"
	in := []MutationOp{sensitiveMutation(aspectKey, "ssn", "123-45-6789")}

	out, minted, err := cp.encryptSensitiveMutations(ctx, in)
	if err != nil {
		t.Fatalf("encryptSensitiveMutations: %v", err)
	}
	if !minted {
		t.Fatalf("mintedPiiKey = false, want true (identity had no piiKey)")
	}
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2 (the ssn mutation + the minted piiKey create)", len(out))
	}

	// The ssn mutation's data must be opaque ciphertext, not plaintext.
	var found bool
	for _, m := range out {
		if m.Key == aspectKey {
			found = true
			ct, ok := m.Document["data"].(vault.Ciphertext)
			if !ok {
				t.Fatalf("ssn mutation data = %+v (%T), want a vault.Ciphertext", m.Document["data"], m.Document["data"])
			}
			if ct.KeyID == "" {
				t.Fatalf("ciphertext = %+v, want a populated KeyID", ct)
			}
		}
	}
	if !found {
		t.Fatalf("ssn mutation missing from output: %+v", out)
	}

	// The piiKey create must be present and pointed at the identity.
	var foundKey bool
	for _, m := range out {
		if m.Key == identityKey+".piiKey" {
			foundKey = true
			if m.Op != "create" {
				t.Fatalf("piiKey mutation op = %q, want create", m.Op)
			}
		}
	}
	if !foundKey {
		t.Fatalf("no piiKey create mutation in output: %+v", out)
	}

	// The caller's original mutation slice must be untouched (step65 returns a
	// fresh Document map rather than mutating the input in place).
	inData, ok := in[0].Document["data"].(map[string]interface{})
	if !ok || inData["value"] != "123-45-6789" {
		t.Fatalf("input mutation data = %+v, want the caller's original plaintext left untouched", in[0].Document["data"])
	}
}

// TestEncryptSensitiveMutations_ReusesKeyWithinBatch: two sensitive mutations
// for the SAME identity in one batch mint the piiKey only once — the second
// reuses the cached envelope, so only one extra create mutation is appended.
func TestEncryptSensitiveMutations_ReusesKeyWithinBatch(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cp, _ := newEncryptTestCommitPath(t, ctx, conn)

	identityKey := "vtx.identity." + testNanoID2
	in := []MutationOp{
		sensitiveMutation(identityKey+".ssn", "ssn", "123-45-6789"),
		{
			Op:  "update",
			Key: identityKey + ".ssn",
			Document: map[string]interface{}{
				"class": "ssn", "isDeleted": false,
				"data": map[string]interface{}{"value": "987-65-4321"},
			},
		},
	}

	out, minted, err := cp.encryptSensitiveMutations(ctx, in)
	if err != nil {
		t.Fatalf("encryptSensitiveMutations: %v", err)
	}
	if !minted {
		t.Fatalf("mintedPiiKey = false, want true")
	}
	var piiKeyCreates int
	for _, m := range out {
		if m.Key == identityKey+".piiKey" {
			piiKeyCreates++
		}
	}
	if piiKeyCreates != 1 {
		t.Fatalf("piiKey create mutations = %d, want exactly 1 (cached across the batch)", piiKeyCreates)
	}
	if len(out) != 3 {
		t.Fatalf("len(out) = %d, want 3 (2 ssn mutations + 1 piiKey create)", len(out))
	}
}

// TestEncryptSensitiveMutations_ExistingPiiKey_NoMint: a sensitive mutation
// for an identity that already has a piiKey aspect reuses it — mintedPiiKey
// is false and no extra mutation is appended.
func TestEncryptSensitiveMutations_ExistingPiiKey_NoMint(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cp, v := newEncryptTestCommitPath(t, ctx, conn)

	identityKey := "vtx.identity." + testNanoID2
	env, err := v.CreateIdentityKey(ctx, identityKey)
	if err != nil {
		t.Fatalf("CreateIdentityKey: %v", err)
	}
	seedPiiKeyAspect(t, ctx, conn, identityKey, env)

	in := []MutationOp{sensitiveMutation(identityKey+".ssn", "ssn", "123-45-6789")}
	out, minted, err := cp.encryptSensitiveMutations(ctx, in)
	if err != nil {
		t.Fatalf("encryptSensitiveMutations: %v", err)
	}
	if minted {
		t.Fatalf("mintedPiiKey = true, want false (piiKey pre-existed)")
	}
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1 (no extra mutation appended)", len(out))
	}
}

// TestEncryptSensitiveMutations_NonSensitiveClass_PassesThrough: a mutation
// whose DDL class is not declared sensitive is left byte-for-byte unchanged.
func TestEncryptSensitiveMutations_NonSensitiveClass_PassesThrough(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cp, _ := newEncryptTestCommitPath(t, ctx, conn)

	identityKey := "vtx.identity." + testNanoID2
	in := []MutationOp{sensitiveMutation(identityKey+".nickname", "nickname", "Andy")}

	out, minted, err := cp.encryptSensitiveMutations(ctx, in)
	if err != nil {
		t.Fatalf("encryptSensitiveMutations: %v", err)
	}
	if minted {
		t.Fatalf("mintedPiiKey = true, want false (nothing sensitive written)")
	}
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1 (unchanged)", len(out))
	}
	data := out[0].Document["data"].(map[string]interface{})
	if data["value"] != "Andy" {
		t.Fatalf("data = %+v, want the plaintext value unchanged", data)
	}
}

// TestEncryptSensitiveMutations_Tombstone_PassesThrough: a tombstone mutation
// has no data to encrypt and is left unchanged regardless of class.
func TestEncryptSensitiveMutations_Tombstone_PassesThrough(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cp, _ := newEncryptTestCommitPath(t, ctx, conn)

	identityKey := "vtx.identity." + testNanoID2
	in := []MutationOp{{Op: "tombstone", Key: identityKey + ".ssn"}}

	out, minted, err := cp.encryptSensitiveMutations(ctx, in)
	if err != nil {
		t.Fatalf("encryptSensitiveMutations: %v", err)
	}
	if minted {
		t.Fatalf("mintedPiiKey = true, want false (a tombstone mints no key)")
	}
	if len(out) != 1 || out[0].Op != "tombstone" {
		t.Fatalf("out = %+v, want the tombstone unchanged", out)
	}
}

// TestEncryptSensitiveMutations_NonIdentityAnchor_PassesThrough: a sensitive
// class anchored under a non-identity vertex type is left alone here (step 6
// already rejects a non-identity-anchored sensitive aspect ahead of this
// stage, so a malformed key surviving to 6.5 must not panic or encrypt).
func TestEncryptSensitiveMutations_NonIdentityAnchor_PassesThrough(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cp, _ := newEncryptTestCommitPath(t, ctx, conn)

	in := []MutationOp{sensitiveMutation("vtx.task."+testNanoID2+".ssn", "ssn", "123-45-6789")}

	out, minted, err := cp.encryptSensitiveMutations(ctx, in)
	if err != nil {
		t.Fatalf("encryptSensitiveMutations: %v", err)
	}
	if minted {
		t.Fatalf("mintedPiiKey = true, want false")
	}
	data := out[0].Document["data"].(map[string]interface{})
	if data["value"] != "123-45-6789" {
		t.Fatalf("data = %+v, want the plaintext left untouched (no identity key to encrypt under)", data)
	}
}

// TestEnsureIdentityKey_ExistingKey_NoExtraMutation: calling ensureIdentityKey
// directly for an identity that already has a piiKey returns the stored
// envelope and appends nothing to extra.
func TestEnsureIdentityKey_ExistingKey_NoExtraMutation(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cp, v := newEncryptTestCommitPath(t, ctx, conn)

	identityKey := "vtx.identity." + testNanoID2
	want, err := v.CreateIdentityKey(ctx, identityKey)
	if err != nil {
		t.Fatalf("CreateIdentityKey: %v", err)
	}
	seedPiiKeyAspect(t, ctx, conn, identityKey, want)

	var extra []MutationOp
	got, err := cp.ensureIdentityKey(ctx, identityKey, &extra)
	if err != nil {
		t.Fatalf("ensureIdentityKey: %v", err)
	}
	if len(extra) != 0 {
		t.Fatalf("extra = %+v, want none (key already existed)", extra)
	}
	gotB, _ := json.Marshal(got)
	wantB, _ := json.Marshal(want)
	if string(gotB) != string(wantB) {
		t.Fatalf("envelope = %s, want the stored envelope %s", gotB, wantB)
	}
}

// TestEnsureIdentityKey_NoKey_MintsAndAppends: an identity with no piiKey
// mints a fresh envelope via the Vault and appends exactly one create
// mutation for the piiKey aspect to extra.
func TestEnsureIdentityKey_NoKey_MintsAndAppends(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cp, _ := newEncryptTestCommitPath(t, ctx, conn)

	identityKey := "vtx.identity." + testNanoID2
	var extra []MutationOp
	env, err := cp.ensureIdentityKey(ctx, identityKey, &extra)
	if err != nil {
		t.Fatalf("ensureIdentityKey: %v", err)
	}
	if len(extra) != 1 {
		t.Fatalf("extra = %+v, want exactly 1 mutation", extra)
	}
	m := extra[0]
	if m.Op != "create" || m.Key != identityKey+".piiKey" {
		t.Fatalf("extra[0] = %+v, want a create at %s.piiKey", m, identityKey)
	}
	if m.Document["data"] == nil {
		t.Fatalf("piiKey mutation has no data: %+v", m.Document)
	}
	if env.KeyID == "" {
		t.Fatalf("ensureIdentityKey returned a zero envelope: %+v", env)
	}
}
