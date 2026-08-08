package processor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

// testRetentionClassKey is a well-formed retention-class holder key. The type
// segment is all-lowercase on purpose — a camelCase segment is not a Contract
// #1 vertex key at all, which is the trap this whole feature had to design
// around.
const testRetentionClassKey = "vtx." + RetentionClassVertexType + ".Rc7kPmRtw9nbCxz5vQ2y"

// seedCustodiedAspectDDL seeds a sensitive aspect-type DDL that declares
// retention-class custody, using the same root-document fallback shape
// seedSensitiveAspectClassDDL uses.
func seedCustodiedAspectDDL(t *testing.T, ctx context.Context, conn *substrate.Conn, class, kind, holderKey string) {
	t.Helper()
	root := "vtx.meta." + class
	doc := []byte(`{"class":"meta.ddl.aspectType","isDeleted":false,"data":{"canonicalName":"` + class +
		`","sensitive":true,"custody":{"kind":"` + kind + `","holderKey":"` + holderKey + `"}}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root, doc); err != nil {
		t.Fatalf("seed %s DDL: %v", class, err)
	}
}

// --- The cache seam -------------------------------------------------------

// A DDL declaring retention-class custody loads its kind AND its resolved
// holder key, so the commit path performs no extra read to learn either.
func TestDDLCache_LoadsCustodyDeclaration(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	seedCustodiedAspectDDL(t, ctx, conn, "encounter", CustodyKindRetentionClass, testRetentionClassKey)
	seedSensitiveAspectClassDDL(t, ctx, conn, "ssn", true)

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	ref, ok := cache.Lookup("encounter")
	if !ok {
		t.Fatal("encounter DDL not in cache")
	}
	if ref.CustodyKind != CustodyKindRetentionClass {
		t.Fatalf("CustodyKind = %q, want %q", ref.CustodyKind, CustodyKindRetentionClass)
	}
	if ref.CustodyHolderKey != testRetentionClassKey {
		t.Fatalf("CustodyHolderKey = %q, want %q", ref.CustodyHolderKey, testRetentionClassKey)
	}

	// A DDL that declares no custody stays exactly as it was: empty kind, which
	// every consumer reads as the identity kind.
	plain, ok := cache.Lookup("ssn")
	if !ok {
		t.Fatal("ssn DDL not in cache")
	}
	if plain.CustodyKind != "" || plain.CustodyHolderKey != "" {
		t.Fatalf("undeclared custody must stay zero, got kind=%q holder=%q", plain.CustodyKind, plain.CustodyHolderKey)
	}
}

// --- Step 6: the anchoring rule is conditional ----------------------------

func newCustodyValidator(t *testing.T, ctx context.Context, conn *substrate.Conn) *ValidatorImpl {
	t.Helper()
	seedCustodiedAspectDDL(t, ctx, conn, "encounter", CustodyKindRetentionClass, testRetentionClassKey)
	seedCustodiedAspectDDL(t, ctx, conn, "brokenCustody", CustodyKindRetentionClass, "vtx.identity."+testNanoID1)
	seedCustodiedAspectDDL(t, ctx, conn, "unknownKind", "somethingElse", "")
	seedSensitiveAspectClassDDL(t, ctx, conn, "ssn", true)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return NewValidator(cache, conn, testCoreBucket, testLogger())
}

func aspectMutation(key, class string) MutationOp {
	return MutationOp{
		Op:  "create",
		Key: key,
		Document: map[string]interface{}{
			"class":     class,
			"isDeleted": false,
			"data":      map[string]interface{}{"value": "x"},
		},
	}
}

func validateOneMutation(t *testing.T, v *ValidatorImpl, ctx context.Context, m MutationOp) error {
	t.Helper()
	env := newTestEnvelope(testNanoID1)
	return v.Validate(ctx, env, ScriptResult{Mutations: []MutationOp{m}}, HydratedState{})
}

// THE POSITIVE VECTOR, and the reason this whole design exists: a sensitive
// aspect whose DDL custodies its DEK on a retention class may anchor on a
// vertex that is not an identity. Before this, step 6 refused it outright,
// which is why two retained-class records sat in Core KV as plaintext.
func TestStep6_RetentionClassCustody_PermitsNonIdentityAnchor(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	v := newCustodyValidator(t, ctx, conn)

	m := aspectMutation("vtx.appointment."+testNanoID2+".encounter", "encounter")
	if err := validateOneMutation(t, v, ctx, m); err != nil {
		t.Fatalf("retention-class custody must permit a non-identity anchor, got: %v", err)
	}
}

// The regression guard on the positive vector: permitting a non-identity
// anchor is a property of the DECLARED CUSTODY, not a general loosening. An
// aspect that declares no custody keeps today's rejection byte-for-byte —
// otherwise a package could flip Sensitive:true, forget custody, and get
// plaintext at rest instead of an error.
func TestStep6_UndeclaredCustody_StillRefusesNonIdentityAnchor(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	v := newCustodyValidator(t, ctx, conn)

	m := aspectMutation("vtx.appointment."+testNanoID2+".ssn", "ssn")
	err := validateOneMutation(t, v, ctx, m)
	if err == nil {
		t.Fatal("a sensitive aspect with no custody declaration must still be identity-anchored")
	}
	var ddlErr *DDLViolation
	if !errors.As(err, &ddlErr) || ddlErr.ViolatedConstraint != "sensitiveAspectScope" {
		t.Fatalf("want sensitiveAspectScope violation, got %v", err)
	}
	if !strings.Contains(ddlErr.Detail, "may only attach to identity vertices") {
		t.Fatalf("the undeclared-custody message must be today's message, got %q", ddlErr.Detail)
	}
}

// An identity-custodied sensitive aspect on its own identity still passes —
// the path every shipped sensitive aspect takes.
func TestStep6_IdentityCustody_PermitsIdentityAnchor(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	v := newCustodyValidator(t, ctx, conn)

	m := aspectMutation("vtx.identity."+testNanoID2+".ssn", "ssn")
	if err := validateOneMutation(t, v, ctx, m); err != nil {
		t.Fatalf("identity-anchored sensitive aspect must pass: %v", err)
	}
}

// A retention-class DDL whose holder key is not a retentionclass vertex is a
// malformed declaration the install should never have produced. It fails
// CLOSED rather than falling back to the anchor: falling back would silently
// custody a retained record on the very subject whose erasure it was declared
// to survive.
func TestStep6_RetentionClassCustody_MalformedHolderRejects(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	v := newCustodyValidator(t, ctx, conn)

	m := aspectMutation("vtx.appointment."+testNanoID2+".brokenCustody", "brokenCustody")
	err := validateOneMutation(t, v, ctx, m)
	if err == nil {
		t.Fatal("a retention-class holder key of the wrong vertex type must reject")
	}
	var ddlErr *DDLViolation
	if !errors.As(err, &ddlErr) || ddlErr.ViolatedConstraint != "sensitiveAspectScope" {
		t.Fatalf("want sensitiveAspectScope violation, got %v", err)
	}
}

// An unrecognized custody kind rejects rather than degrading to identity —
// the permissive reading is exactly the wrong one on this plane.
func TestStep6_UnknownCustodyKind_Rejects(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	v := newCustodyValidator(t, ctx, conn)

	m := aspectMutation("vtx.identity."+testNanoID2+".unknownKind", "unknownKind")
	err := validateOneMutation(t, v, ctx, m)
	if err == nil {
		t.Fatal("an unknown custody kind must reject, even on an identity anchor")
	}
	var ddlErr *DDLViolation
	if !errors.As(err, &ddlErr) || !strings.Contains(ddlErr.Detail, "unknown custody kind") {
		t.Fatalf("want an unknown-kind violation, got %v", err)
	}
}

// --- Step 6.5: the DEK comes from the holder ------------------------------

// The acceptance criterion's write half: a retained aspect on an appointment
// encrypts under the RETENTION CLASS's key, and that key is minted onto the
// class holder — a vertex the operation never named — inside the same atomic
// batch.
func TestEncryptSensitiveMutations_RetentionClassCustody_EncryptsUnderHolder(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	seedCustodiedAspectDDL(t, ctx, conn, "encounter", CustodyKindRetentionClass, testRetentionClassKey)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	v, err := vault.NewLocalBackend([]byte("lattice-testutil-vault-master-ke"), "test-v1")
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	cp := &CommitPath{deps: Deps{
		Conn: conn, CoreBucket: testCoreBucket, Vault: v, DDLs: cache, Logger: testLogger(),
	}}

	aspectKey := "vtx.appointment." + testNanoID2 + ".encounter"
	in := []MutationOp{sensitiveMutation(aspectKey, "encounter", "chief complaint")}

	out, minted, err := cp.encryptSensitiveMutations(ctx, in, HydratedState{})
	if err != nil {
		t.Fatalf("encryptSensitiveMutations: %v", err)
	}
	if !minted {
		t.Fatal("the class holder had no piiKey, so the batch must report a mint")
	}

	// The key aspect landed on the HOLDER, not on the appointment.
	var keyMut *MutationOp
	for i := range out {
		if out[i].Key == testRetentionClassKey+".piiKey" {
			keyMut = &out[i]
		}
		if strings.HasPrefix(out[i].Key, "vtx.appointment.") && strings.HasSuffix(out[i].Key, ".piiKey") {
			t.Fatalf("a class-custodied aspect must never mint a key on its anchor: %s", out[i].Key)
		}
	}
	if keyMut == nil {
		t.Fatalf("no piiKey minted on the holder %s; batch = %v", testRetentionClassKey, mutationKeys(out))
	}
	if keyMut.Op != "create" {
		t.Fatalf("holder piiKey op = %q, want create", keyMut.Op)
	}

	// The aspect's data is ciphertext, and it names the HOLDER as its key.
	ct := ciphertextOfMutation(t, out, aspectKey)
	if ct.KeyID != testRetentionClassKey {
		t.Fatalf("ciphertext KeyID = %q, want the holder %q", ct.KeyID, testRetentionClassKey)
	}
	if strings.Contains(string(ct.CT), "chief complaint") {
		t.Fatal("plaintext survived into the stored ciphertext")
	}
}

// One holder, many records, one mint: a batch writing several retained
// aspects under the same class mints the class key exactly once. This is the
// property that makes a retention class usable at all — a per-record mint
// would put one key per record, which is per-record custody wearing a class's
// name.
func TestEncryptSensitiveMutations_RetentionClassCustody_MintsHolderKeyOnce(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	seedCustodiedAspectDDL(t, ctx, conn, "encounter", CustodyKindRetentionClass, testRetentionClassKey)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	v, err := vault.NewLocalBackend([]byte("lattice-testutil-vault-master-ke"), "test-v1")
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	cp := &CommitPath{deps: Deps{
		Conn: conn, CoreBucket: testCoreBucket, Vault: v, DDLs: cache, Logger: testLogger(),
	}}

	in := []MutationOp{
		sensitiveMutation("vtx.appointment."+testNanoID1+".encounter", "encounter", "first"),
		sensitiveMutation("vtx.appointment."+testNanoID2+".encounter", "encounter", "second"),
	}
	out, _, err := cp.encryptSensitiveMutations(ctx, in, HydratedState{})
	if err != nil {
		t.Fatalf("encryptSensitiveMutations: %v", err)
	}
	mints := 0
	for _, m := range out {
		if m.Key == testRetentionClassKey+".piiKey" {
			mints++
		}
	}
	if mints != 1 {
		t.Fatalf("holder key minted %d times across one batch, want exactly 1", mints)
	}
}

// A sensitive aspect on a non-identity anchor that declares NO custody still
// passes through unencrypted here — step 6 rejected it upstream, so step 6.5
// never sees one in a real pipeline and must not invent a holder for it.
func TestEncryptSensitiveMutations_NonIdentityAnchor_NoCustody_PassesThrough(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cp, _ := newEncryptTestCommitPath(t, ctx, conn)

	aspectKey := "vtx.appointment." + testNanoID2 + ".ssn"
	in := []MutationOp{sensitiveMutation(aspectKey, "ssn", "123-45-6789")}
	out, minted, err := cp.encryptSensitiveMutations(ctx, in, HydratedState{})
	if err != nil {
		t.Fatalf("encryptSensitiveMutations: %v", err)
	}
	if minted {
		t.Fatal("no key may be minted for an aspect with no resolvable holder")
	}
	data, _ := out[0].Document["data"].(map[string]interface{})
	if data == nil || data["value"] != "123-45-6789" {
		t.Fatalf("mutation must pass through untouched, got %v", out[0].Document["data"])
	}
}

func mutationKeys(ms []MutationOp) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Key
	}
	return out
}

func ciphertextOfMutation(t *testing.T, ms []MutationOp, key string) vault.Ciphertext {
	t.Helper()
	for _, m := range ms {
		if m.Key != key {
			continue
		}
		raw, err := json.Marshal(m.Document["data"])
		if err != nil {
			t.Fatalf("marshal data for %s: %v", key, err)
		}
		var ct vault.Ciphertext
		if err := json.Unmarshal(raw, &ct); err != nil {
			t.Fatalf("parse ciphertext for %s: %v", key, err)
		}
		if len(ct.CT) == 0 {
			t.Fatalf("%s was not encrypted", key)
		}
		return ct
	}
	t.Fatalf("mutation %s not in batch", key)
	return vault.Ciphertext{}
}
