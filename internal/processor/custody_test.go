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

	// A DDL that declares no custody carries an empty kind, which every
	// consumer reads as the identity kind.
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
	return v.Validate(ctx, env, ScriptResult{Mutations: []MutationOp{m}}, HydratedState{}, nil)
}

// THE POSITIVE VECTOR, and the reason this whole design exists: a sensitive
// aspect whose DDL custodies its DEK on a retention class may anchor on a
// vertex that is not an identity. Without it a record whose retention
// obligation outlives its subject has no expressible home and sits in Core KV
// as plaintext.
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
// aspect that declares no custody is still rejected — otherwise a package
// could flip Sensitive:true, forget custody, and get plaintext at rest
// instead of an error.
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

// A sensitive aspect whose custody resolves to NO key holder must fail the
// operation, never pass through. Step 6 rejects every shape that reaches here,
// but that is an invariant spanning two steps that read a DDL cache a
// concurrent meta-commit can invalidate BETWEEN them. Passing through would
// commit the plaintext of a declared-sensitive aspect on exactly the race the
// invariant does not cover, so step 6.5 refuses on its own authority.
func TestEncryptSensitiveMutations_SensitiveWithNoHolder_IsAnError(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cp, _ := newEncryptTestCommitPath(t, ctx, conn)

	aspectKey := "vtx.appointment." + testNanoID2 + ".ssn"
	in := []MutationOp{sensitiveMutation(aspectKey, "ssn", "123-45-6789")}
	out, _, err := cp.encryptSensitiveMutations(ctx, in, HydratedState{})
	if err == nil {
		t.Fatalf("a sensitive aspect with no resolvable holder must fail, got mutations %v", mutationKeys(out))
	}
	if !strings.Contains(err.Error(), "no key holder") {
		t.Fatalf("error must name the missing holder, got: %v", err)
	}
}

// The kind gate mirrors step 6's own, in the SKIP direction. A DDL cache
// lookup is keyed on canonicalName alone, so a VERTEX mutation carrying an
// aspect DDL's class resolves to that aspect DDL — and step 6 gates its
// custody check on the mutation being an aspect, so it never checks one.
// Step 6.5 must gate identically: an asymmetry is what lets one step act on
// a mutation the other never validated. Encrypting here would produce a
// vertex root no read path decrypts.
func TestEncryptSensitiveMutations_VertexMutationWithAspectClass_PassesThrough(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cp, _ := newEncryptTestCommitPath(t, ctx, conn)

	key := "vtx.ssn." + testNanoID2
	in := []MutationOp{{
		Op:  "create",
		Key: key,
		Document: map[string]interface{}{
			"class":     "ssn",
			"isDeleted": false,
			"data":      map[string]interface{}{"value": "123-45-6789"},
		},
	}}
	out, minted, err := cp.encryptSensitiveMutations(ctx, in, HydratedState{})
	if err != nil {
		t.Fatalf("a non-aspect mutation must pass through as step 6 lets it, got: %v", err)
	}
	if minted {
		t.Fatal("no key may be minted for a non-aspect mutation")
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

// seedCustodyAspect writes the REAL reserved aspect key a package install
// emits — `vtx.meta.<ddl>.custody` — as opposed to the root-document fallback
// the other fixtures use. Both halves of the contract must be exercised: the
// install writes this key, so a typo in the suffix or in the `data` nesting
// would otherwise pass every test while breaking every real install.
func seedCustodyAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, class, kind, holderKey string, deleted bool) {
	t.Helper()
	root := "vtx.meta." + class
	doc := []byte(`{"class":"custody","vertexKey":"` + root + `","localName":"custody","isDeleted":` +
		boolLiteral(deleted) + `,"data":{"kind":"` + kind + `","holderKey":"` + holderKey + `"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root+".custody", doc); err != nil {
		t.Fatalf("seed %s.custody: %v", root, err)
	}
}

// The install-produced path, end to end: a plain sensitive DDL root plus a
// real `.custody` aspect resolves to the declared kind and holder.
func TestDDLCache_LoadsCustodyFromTheRealAspectKey(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	seedSensitiveAspectClassDDL(t, ctx, conn, "encounter", true)
	seedCustodyAspect(t, ctx, conn, "encounter", CustodyKindRetentionClass, testRetentionClassKey, false)

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	ref, ok := cache.Lookup("encounter")
	if !ok {
		t.Fatal("encounter DDL not in cache")
	}
	if ref.CustodyKind != CustodyKindRetentionClass || ref.CustodyHolderKey != testRetentionClassKey {
		t.Fatalf("the real .custody aspect must drive custody, got kind=%q holder=%q", ref.CustodyKind, ref.CustodyHolderKey)
	}
}

// A tombstone retains the prior document, so a revoked custody declaration
// must read as ABSENT. Otherwise a package that removes its Custody keeps
// writing to the class DEK forever, with no way to un-declare it short of
// deleting the whole DDL.
func TestDDLCache_TombstonedCustodyReadsAsAbsent(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	seedSensitiveAspectClassDDL(t, ctx, conn, "encounter", true)
	seedCustodyAspect(t, ctx, conn, "encounter", CustodyKindRetentionClass, testRetentionClassKey, true)

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	ref, ok := cache.Lookup("encounter")
	if !ok {
		t.Fatal("encounter DDL not in cache")
	}
	if ref.CustodyKind != "" || ref.CustodyHolderKey != "" {
		t.Fatalf("a tombstoned custody declaration must read as absent, got kind=%q holder=%q", ref.CustodyKind, ref.CustodyHolderKey)
	}
}

// An unparseable custody body must not be readable as ANY custodian. Zeroing
// it would degrade the DDL to identity custody (re-pointing a retained
// record's DEK at the subject whose erasure it was declared to survive), and
// failing the load would drop the DDL entirely — after which the class is not
// sensitive to anyone and commits as plaintext. The class stays loaded,
// sensitive, and unwritable.
func TestDDLCache_MalformedCustodyPoisonsTheClass(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	seedSensitiveAspectClassDDL(t, ctx, conn, "encounter", true)
	root := "vtx.meta.encounter"
	if _, err := conn.KVPut(ctx, testCoreBucket, root+".custody",
		[]byte(`{"isDeleted":false,"data":{"kind":123,"holderKey":"x"}}`)); err != nil {
		t.Fatalf("seed malformed custody: %v", err)
	}

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	ref, ok := cache.Lookup("encounter")
	if !ok {
		t.Fatal("the DDL must stay loaded — dropping it makes the class non-sensitive and its data plaintext")
	}
	if !ref.Sensitive {
		t.Fatal("the class must stay sensitive")
	}
	if ref.CustodyKind != CustodyKindUnresolvable {
		t.Fatalf("CustodyKind = %q, want the poison value %q", ref.CustodyKind, CustodyKindUnresolvable)
	}

	// And the poison is load-bearing: step 6 refuses the write outright.
	v := NewValidator(cache, conn, testCoreBucket, testLogger())
	err := validateOneMutation(t, v, ctx, aspectMutation("vtx.identity."+testNanoID2+".encounter", "encounter"))
	if err == nil {
		t.Fatal("a class whose custodian is unknown must reject its writes")
	}
	var ddlErr *DDLViolation
	if !errors.As(err, &ddlErr) || ddlErr.ViolatedConstraint != "sensitiveAspectScope" {
		t.Fatalf("want sensitiveAspectScope violation, got %v", err)
	}
}

// --- The budget fail-open closure -----------------------------------------

// Steps 6 and 6.5 re-resolve the SAME mutation against the SAME shared
// live-read budget. Step 6 can resolve a class as sensitive, pass its
// anchoring check, and leave the budget exhausted; step 6.5's identical call
// then comes back empty. Reading that emptiness as "not sensitive" is what
// committed plaintext. It is now an error instead.
func TestEncryptSensitiveMutations_BudgetExhaustedResolution_IsAnError(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cp, _ := newEncryptTestCommitPath(t, ctx, conn)

	// A class with no exact DDL, so resolution must walk the instanceOf chain
	// and spend live reads — against a budget with nothing left.
	in := []MutationOp{sensitiveMutation("vtx.identity."+testNanoID2+".ssn2", "ssn.v2", "123-45-6789")}
	state := HydratedState{Context: ScriptContext{LiveReads: &liveReadBudgetTracker{budget: 0}}}

	_, _, err := cp.encryptSensitiveMutations(ctx, in, state)
	if err == nil {
		t.Fatal("a resolution that came back empty after a live-read fault must fail the operation, not skip encryption")
	}
	if !strings.Contains(err.Error(), "live-read fault") {
		t.Fatalf("the error must name the fault, got: %v", err)
	}
}

// The other half of the same rule, and the reason it is two conditions rather
// than one: step 6 keeps its fail-open to the permissive default. A budget
// exhausted during STEP 6's own walk resolves to no DDL and the mutation
// passes — unchanged, and asserted here so the asymmetry is deliberate rather
// than incidental.
func TestStep6_BudgetExhausted_StillFailsOpenToPermissive(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	v := newCustodyValidator(t, ctx, conn)

	m := aspectMutation("vtx.appointment."+testNanoID2+".notes", "unregistered.v2")
	state := HydratedState{Context: ScriptContext{LiveReads: &liveReadBudgetTracker{budget: 0}}}
	env := newTestEnvelope(testNanoID1)
	if err := v.Validate(ctx, env, ScriptResult{Mutations: []MutationOp{m}}, state, nil); err != nil {
		t.Fatalf("step 6 must still fail open to the permissive default, got: %v", err)
	}
}
