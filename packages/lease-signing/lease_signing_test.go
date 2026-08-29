// lease-signing integration tests through the real install + Processor pipeline.
//
// These tests live in an external test package (leasesigning_test) so they
// exercise the public Lattice surface a real Capability Package sees: seed the
// kernel, install rbac + identity + orchestration-base + service-domain +
// lease-signing through the Processor, then submit the ops and assert the
// committed Core-KV shape + the emitted events.
//
// AC #4: every outcome write is a DIRECT RecordLeaseServiceOutcome op with a
// synthetic {externalRef, result} payload (the bridge's shape) — never a live
// bridge process (that is 14.5).
package leasesigning_test

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	"github.com/operatinggraph/lattice/internal/vault"
	identitydomain "github.com/operatinggraph/lattice/packages/identity-domain"
	leasesigning "github.com/operatinggraph/lattice/packages/lease-signing"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
	servicedomain "github.com/operatinggraph/lattice/packages/service-domain"
)

const (
	lsActorID  = "BBLsActorrHJKMNPQRST"
	lsActorKey = "vtx.identity." + lsActorID
	lsCapKey   = "cap.identity." + lsActorID

	// lsConsumerRoleID stands in for identity-domain's real `consumer` role
	// NanoID: this package's tests don't install identity-domain (only rbac +
	// hygiene via SetupPackageTestEnv), so lease-signing's own CreateLeaseApplication
	// scope=self grant (GrantsTo: "consumer") needs a role id registered directly.
	lsConsumerRoleID = "BBConsumerRoZeHJKMNP"
)

// lsLoomCapDoc grants Loom's primordial relay actor the two externalTask
// instanceOps whose scripts pin op.actor to `primordialActor["loom"]`. It is
// read through a func, not a package var: bootstrap's primordial globals are
// populated by SetupPackageTestEnv's EnsurePrimordials, well after package
// var initialization.
//
// lsCapDoc keeps its own grant on those ops deliberately — an operator-role
// holder that is NOT Loom is exactly the forged-submitter vector the guard
// exists to reject, and the negative tests submit as lsActorKey to prove the
// refusal comes from the script's actor check rather than from a missing grant.
func lsLoomCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    "cap.identity." + bootstrap.LoomIdentityID,
		Actor:                  bootstrap.LoomIdentityKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{bootstrap.LoomIdentityKey: 1},
		Lanes:                  []string{"default", "urgent"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateLeaseServiceInstance", Scope: "any"},
			{OperationType: "CreateLeaseDocInstance", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// lsCapDoc grants the test actor the lease-signing ops (scope any).
func lsCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    lsCapKey,
		Actor:                  lsActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{lsActorKey: 1},
		Lanes:                  []string{"default", "urgent"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateLeaseApplication", Scope: "any"},
			{OperationType: "SignLease", Scope: "any"},
			{OperationType: "WithdrawLeaseApplication", Scope: "any"},
			{OperationType: "CreateLeaseServiceInstance", Scope: "any"},
			{OperationType: "RecordLeaseServiceOutcome", Scope: "any"},
			{OperationType: "RecordServiceDispatch", Scope: "any"},
			{OperationType: "CreateLeaseDocInstance", Scope: "any"},
			{OperationType: "RecordLeaseDocOutcome", Scope: "any"},
			// The docGen instanceOp test mints a REAL identity (encrypted .name +
			// .piiKey) so the document-field assembly exercises decrypt-on-read.
			{OperationType: "CreateUnclaimedIdentity", Scope: "any"},
			// The docGen shredded-applicant test drives a REAL ShredIdentityKey
			// (urgent lane) so the field-assembly script's piiKey probe is
			// exercised against genuine vault-shredded state, not a fixture.
			{OperationType: "ShredIdentityKey", Scope: "any"},
			{OperationType: "DecideLeaseApplication", Scope: "any"},
			{OperationType: "SetApplicantProfile", Scope: "any"},
			{OperationType: "OpenRenewal", Scope: "any"},
			{OperationType: "SetRenewalTerms", Scope: "any"},
			{OperationType: "VerifyGuarantor", Scope: "any"},
			{OperationType: "SignRenewal", Scope: "any"},
			{OperationType: "CancelRenewal", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// setupLeaseEnv seeds the kernel, installs the dependency chain +
// orchestration-base + service-domain + lease-signing, and seeds the cap doc.
func setupLeaseEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t) // rbac + identity + hygiene
	installLeaseDeps(t, ctx, conn)
	testutil.SeedCapDoc(t, ctx, conn, lsCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, lsLoomCapDoc())
	// The operator grant is only half the claim — the workplace-confinement
	// guard reads the holdsRole LINK to decide whether its caller is root.
	testutil.SeedHoldsRole(t, ctx, conn, lsActorKey, bootstrap.RoleOperatorKey)
	return ctx, conn
}

// installLeaseDeps installs orchestration-base, service-domain, then
// lease-signing through the real meta-install pipeline. The success of the
// lease-signing install IS the install round-trip proof (test 5): a malformed
// DDL self-description / playbook / pattern / a canonicalName collision fails
// here, before any op runs.
func installLeaseDeps(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	defer stop()
	inst := testutil.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID, "consumer": lsConsumerRoleID, "frontOfHouse": pkgmgr.RoleID("identity-domain", "frontOfHouse"), "backOfHouse": pkgmgr.RoleID("identity-domain", "backOfHouse"), "provider": pkgmgr.RoleID("identity-domain", "provider")}
	if _, err := inst.Install(ctx, orchestrationbase.Package); err != nil {
		t.Fatalf("install orchestration-base: %v", err)
	}
	if _, err := inst.Install(ctx, servicedomain.Package); err != nil {
		t.Fatalf("install service-domain: %v", err)
	}
	if _, err := inst.Install(ctx, leasesigning.Package); err != nil {
		t.Fatalf("install lease-signing: %v", err)
	}
}

func newLeasePipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "ls-" + durable,
	})
}

func nanoIDFromRequestID(requestID string) string {
	seed := processor.SeedFromRequestID(requestID)
	pcg := rand.NewPCG(seed[0], seed[1])
	return processor.DeterministicNanoID(pcg, substrate.NanoIDLength)
}

func seedVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class string, data map[string]any) {
	t.Helper()
	if data == nil {
		data = map[string]any{}
	}
	doc := map[string]any{"class": class, "isDeleted": false, "data": data}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed vertex %s: %v", key, err)
	}
}

func readDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) map[string]any {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	return doc
}

// lsDecryptAspect opens a SENSITIVE aspect (.profile / .underwritingParties)
// custodied on the package's underwritingRecord retention-class holder — never
// the applicant's identity. Mirrors clinic-domain's clDecryptAspect: reads the
// holder's .piiKey envelope + the aspect's ciphertext, then decrypts via the
// shared TestVault (KEK-deterministic — a fresh TestVault(t) instance opens
// ciphertext ANY other TestVault(t) instance wrote, since only the in-memory
// DEK cache / shredded-set is per-instance, not the key material).
func lsDecryptAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, aspectKey string) map[string]any {
	t.Helper()
	holderKey := pkgmgr.RetentionClassKey("lease-signing", "underwritingRecord")
	envEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, holderKey+".piiKey")
	if err != nil {
		t.Fatalf("KVGet %s: %v", holderKey+".piiKey", err)
	}
	var envDoc struct {
		Data vault.Envelope `json:"data"`
	}
	if err := json.Unmarshal(envEntry.Value, &envDoc); err != nil {
		t.Fatalf("unmarshal %s: %v", holderKey+".piiKey", err)
	}

	ctEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, aspectKey)
	if err != nil {
		t.Fatalf("KVGet %s: %v", aspectKey, err)
	}
	var ctDoc struct {
		Data vault.Ciphertext `json:"data"`
	}
	if err := json.Unmarshal(ctEntry.Value, &ctDoc); err != nil {
		t.Fatalf("unmarshal %s: %v", aspectKey, err)
	}

	v := testutil.TestVault(t)
	plaintext, err := v.Decrypt(ctx, holderKey, envDoc.Data, ctDoc.Data)
	if err != nil {
		t.Fatalf("decrypt %s: %v", aspectKey, err)
	}
	var value map[string]any
	if err := json.Unmarshal(plaintext, &value); err != nil {
		t.Fatalf("unmarshal decrypted %s: %v", aspectKey, err)
	}
	return value
}

func keyExists(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) bool {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		return false
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return false
	}
	if del, _ := doc["isDeleted"].(bool); del {
		return false
	}
	return true
}

// seedApplicant seeds a live claimed identity to be the application's applicant.
func seedApplicant(t *testing.T, ctx context.Context, conn *substrate.Conn, id string) string {
	t.Helper()
	key := "vtx.identity." + id
	seedVertex(t, ctx, conn, key, "identity", map[string]any{"state": "claimed"})
	return key
}

// seedConsumerSelfCapDoc grants applicantKey (as its own actor) the three
// applicant scope=self permissions a real consumer holds (permissions.go):
// create their own application, record its qualification profile, and withdraw
// it — used only by the dedicated consumer-self-scope tests below; the standing
// operator path (lsActorKey) never needs this.
func seedConsumerSelfCapDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, applicantKey string) {
	t.Helper()
	now := time.Now().UTC()
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    "cap.identity." + applicantKey[len("vtx.identity."):],
		Actor:                  applicantKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{applicantKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateLeaseApplication", Scope: "self"},
			{OperationType: "SetApplicantProfile", Scope: "self"},
			{OperationType: "WithdrawLeaseApplication", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
	})
}

// seedUnit seeds a live location-domain unit (vtx.unit.<id>, class=location),
// with a .listing aspect {availableFrom, leaseTermMonths, rentAmount,
// rentCurrency}, to be the application's leased unit. Seeded directly (not via
// location-domain's CreateLocation / loftspace-domain's SetListing) because
// these package tests do not install those packages; the leaseapp op only
// alive-checks the unit by key, and DecideLeaseApplication's approve-time
// .tenancy stamping reads .listing directly via kv.Read.
func seedUnit(t *testing.T, ctx context.Context, conn *substrate.Conn, id string) string {
	t.Helper()
	key := "vtx.unit." + id
	seedVertex(t, ctx, conn, key, "location", map[string]any{})
	listing := map[string]any{
		"class": "listing", "isDeleted": false, "vertexKey": key, "localName": "listing",
		"data": map[string]any{
			"availableFrom":   "2026-08-01T00:00:00Z",
			"leaseTermMonths": 12,
			"rentAmount":      2400,
			"rentCurrency":    "USD",
		},
	}
	lb, _ := json.Marshal(listing)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key+".listing", lb); err != nil {
		t.Fatalf("seed unit .listing %s: %v", key, err)
	}
	return key
}

// unitKeyFor derives the deterministic unit key createApplication seeded for
// a given applicant (the same applicant-id-reused-as-unit-id convention).
func unitKeyFor(applicantKey string) string {
	return "vtx.unit." + applicantKey[len("vtx.identity."):]
}

// createApplication submits CreateLeaseApplication and returns the app key. It
// seeds a fresh live unit (vtx.unit.<id>) for the application to apply to (now
// required) and lists both the applicant and the unit in ContextHint.Reads.
func createApplication(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, applicantKey string) string {
	t.Helper()
	reqID := testutil.GenReqID("createApp" + applicantKey[len(applicantKey)-4:])
	appID := nanoIDFromRequestID(reqID)
	// Reuse the applicant's (valid 20-char NanoID) id as the unit id: a distinct
	// key (vtx.unit.<id> vs vtx.identity.<id>) that still satisfies the link
	// key-pattern NanoID check the appliesToUnit link must pass.
	unitKey := seedUnit(t, ctx, conn, applicantKey[len("vtx.identity."):])
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseApplication",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"applicant":"` + applicantKey + `","unit":"` + unitKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{applicantKey, unitKey},
			OptionalReads: []string{guardLinkKey(applicantKey, unitKey)},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.leaseapp." + appID
}

// TestLeaseSigning_InstallRoundTrip_PlaybookAndPatternsValidate (test 5):
// installing the package end-to-end succeeds with the lens + weaverTarget + the
// three loomPatterns + the three DDLs + the op-metas present, and the expected
// meta-vertices land. setupLeaseEnv's install IS the round-trip; this test
// additionally asserts the meta-vertices committed.
func TestLeaseSigning_InstallRoundTrip_PlaybookAndPatternsValidate(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)

	// The lens meta-vertex carries the actorAggregate spec (lenses carry a
	// .canonicalName aspect).
	assertMetaByCanonical(t, ctx, conn, "meta.lens", "leaseApplicationComplete")
	// The weaverTarget + its .spec aspect (weaverTarget/loomPattern vertices
	// carry only a .spec aspect; their identity lives in the spec body).
	assertMetaBySpecField(t, ctx, conn, "meta.weaverTarget", "targetId", "leaseApplicationComplete")
	// The three loomPatterns + their .spec aspects.
	for _, pid := range []string{"backgroundCheck", "collectPayment", "onboarding"} {
		assertMetaBySpecField(t, ctx, conn, "meta.loomPattern", "patternId", pid)
	}
}

// assertMetaByCanonical scans the harness core bucket for a meta-vertex of the
// given class whose .canonicalName aspect matches name, returning its key.
func assertMetaByCanonical(t *testing.T, ctx context.Context, conn *substrate.Conn, class, name string) string {
	t.Helper()
	keys, err := conn.KVListKeys(ctx, testutil.HarnessCoreBucket)
	if err != nil {
		t.Fatalf("list keys: %v", err)
	}
	for _, k := range keys {
		// canonicalName aspects are vtx.meta.<id>.canonicalName.
		if len(k) < len(".canonicalName") || k[len(k)-len(".canonicalName"):] != ".canonicalName" {
			continue
		}
		doc := readDoc(t, ctx, conn, k)
		data, _ := doc["data"].(map[string]any)
		if v, _ := data["value"].(string); v != name {
			continue
		}
		vtxKey := k[:len(k)-len(".canonicalName")]
		vdoc := readDoc(t, ctx, conn, vtxKey)
		if cls, _ := vdoc["class"].(string); cls == class {
			return vtxKey
		}
	}
	t.Fatalf("no %s meta-vertex with canonicalName %q found", class, name)
	return ""
}

// assertMetaBySpecField scans for a meta-vertex of the given class whose .spec
// aspect body carries specField == want, asserting both the vertex and its .spec
// aspect exist. Used for weaverTarget/loomPattern (which carry no .canonicalName
// aspect — their identity is in the spec body).
func assertMetaBySpecField(t *testing.T, ctx context.Context, conn *substrate.Conn, class, specField, want string) {
	t.Helper()
	keys, err := conn.KVListKeys(ctx, testutil.HarnessCoreBucket)
	if err != nil {
		t.Fatalf("list keys: %v", err)
	}
	for _, k := range keys {
		if len(k) < len(".spec") || k[len(k)-len(".spec"):] != ".spec" {
			continue
		}
		vtxKey := k[:len(k)-len(".spec")]
		vdoc := readDoc(t, ctx, conn, vtxKey)
		if cls, _ := vdoc["class"].(string); cls != class {
			continue
		}
		doc := readDoc(t, ctx, conn, k)
		data, _ := doc["data"].(map[string]any)
		if v, _ := data[specField].(string); v == want {
			return
		}
	}
	t.Fatalf("no %s meta-vertex with .spec %s=%q found", class, specField, want)
}

// TestLeaseServiceInstance_MintsClaimVertex_EmitsExternalEvent (test 3): the
// externalTask instanceOp. Submit CreateLeaseServiceInstance and assert the
// claim vertex is minted (root {}, .class + .family aspects, providedTo link)
// and the external.<adapter> event was emitted with the bridge-reader shape.
func TestLeaseServiceInstance_MintsClaimVertex_EmitsExternalEvent(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "instanceop")

	applicantKey := seedApplicant(t, ctx, conn, "BBinstapp1cntHJKMNPQ")
	applicantID := applicantKey[len("vtx.identity."):]

	handle := "afrqvygDz1chYFednoSV"
	instKey := "vtx.service." + handle
	instReqID := testutil.GenReqID("instOpBg00001")
	env := &processor.OperationEnvelope{
		RequestID:     instReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseServiceInstance",
		Actor:         bootstrap.LoomIdentityKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseServiceInstance",
		Payload: json.RawMessage(`{"instanceKey":"` + handle + `","subjectKey":"` + applicantKey +
			`","adapter":"backgroundCheck","replyOp":"RecordLeaseServiceOutcome","params":{"family":"backgroundCheck"}}`),
		ContextHint: &processor.ContextHint{Reads: []string{applicantKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// (a) the claim vertex: key type `service` (so the lens anchors on it) and the
	// fine-grained ENVELOPE class service.<family>.instance (P7 — the discriminator
	// is the class, with NO .class/.family shadow aspect); root data {} (D5).
	instDoc := readDoc(t, ctx, conn, instKey)
	if cls, _ := instDoc["class"].(string); cls != "service.backgroundCheck.instance" {
		t.Fatalf("claim vertex class = %q, want service.backgroundCheck.instance", cls)
	}
	data, _ := instDoc["data"].(map[string]any)
	if len(data) != 0 {
		t.Fatalf("claim vertex root data must be minimal ({}), got %v", data)
	}
	allKeys, err := conn.KVListKeys(ctx, testutil.HarnessCoreBucket)
	if err != nil {
		t.Fatalf("KVListKeys: %v", err)
	}
	keySet := map[string]bool{}
	for _, k := range allKeys {
		keySet[k] = true
	}
	// No .class / .family shadow aspects (the discriminator is the envelope class).
	if keySet[instKey+".class"] {
		t.Fatalf("unexpected .class shadow aspect (the discriminator must be the envelope class, P7)")
	}
	if keySet[instKey+".family"] {
		t.Fatalf("unexpected .family shadow aspect (the lens reads inst.class)")
	}
	// (a') the instanceOf link → the leaseServiceInstance type-authority meta: the
	// step-6 write-gate resolver walks it to enforce the instance's permittedCommands
	// (Contract #1 §1.5 instanceOf terminal). Without it the fine-grained class would
	// miss the exact lookup and fall to the permissive default (no enforcement). The
	// op COMMITTED above, which already proves CreateLeaseServiceInstance resolved
	// through this chain and was permitted; this asserts the link is present + sole.
	instOfPrefix := "lnk.service." + handle + ".instanceOf.meta."
	instOfCount := 0
	for _, k := range allKeys {
		if strings.HasPrefix(k, instOfPrefix) {
			instOfCount++
		}
	}
	if instOfCount != 1 {
		t.Fatalf("want exactly one instanceOf→meta link (prefix %q), got %d", instOfPrefix, instOfCount)
	}
	// providedTo link instance→identity.
	ptLnk := "lnk.service." + handle + ".providedTo.identity." + applicantID
	ptDoc := readDoc(t, ctx, conn, ptLnk)
	if got, _ := ptDoc["sourceVertex"].(string); got != instKey {
		t.Fatalf("providedTo sourceVertex = %q, want %q", got, instKey)
	}
	if got, _ := ptDoc["targetVertex"].(string); got != applicantKey {
		t.Fatalf("providedTo targetVertex = %q, want %q", got, applicantKey)
	}

	// (b) the external.backgroundCheck event was emitted with the bridge-reader
	// shape: instanceKey == externalRef == idempotencyKey == the bare handle.
	ev := findEmittedEvent(t, ctx, conn, instReqID, "external.backgroundCheck")
	if got, _ := ev["instanceKey"].(string); got != handle {
		t.Fatalf("external event instanceKey = %q, want %q", got, handle)
	}
	if got, _ := ev["externalRef"].(string); got != handle {
		t.Fatalf("external event externalRef = %q, want %q", got, handle)
	}
	if got, _ := ev["idempotencyKey"].(string); got != handle {
		t.Fatalf("external event idempotencyKey = %q, want %q", got, handle)
	}
	if got, _ := ev["adapter"].(string); got != "backgroundCheck" {
		t.Fatalf("external event adapter = %q, want backgroundCheck", got)
	}
	if got, _ := ev["replyOp"].(string); got != "RecordLeaseServiceOutcome" {
		t.Fatalf("external event replyOp = %q, want RecordLeaseServiceOutcome", got)
	}
	// The dispatchOp seam: the bridge posts this op if its adapter returns Pending.
	if got, _ := ev["dispatchOp"].(string); got != "RecordServiceDispatch" {
		t.Fatalf("external event dispatchOp = %q, want RecordServiceDispatch", got)
	}
}

// TestLeaseServiceInstance_NonLoomOperatorDenied: the primordial actor guard.
// lsActorKey holds the operator role AND a Scope:"any" CreateLeaseServiceInstance
// grant, so step 3 authorizes it — the ONLY thing standing between it and an
// arbitrary applicant's PII reaching the backgroundCheck vendor is the script's
// `op.actor != primordialActor["loom"]` check. Submitting the identical payload
// the Loom-actor test commits above, differing in the actor alone, must be
// rejected with AuthDenied, mint no claim vertex, and emit no external event.
func TestLeaseServiceInstance_NonLoomOperatorDenied(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "instanceopforged")

	applicantKey := seedApplicant(t, ctx, conn, "BBforgapp1cntHJKMNPQ")

	handle := "forgedSvcHandAbCdEfG"
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("instOpForged01"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseServiceInstance",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseServiceInstance",
		Payload: json.RawMessage(`{"instanceKey":"` + handle + `","subjectKey":"` + applicantKey +
			`","adapter":"backgroundCheck","replyOp":"RecordLeaseServiceOutcome","params":{"family":"backgroundCheck"}}`),
		ContextHint: &processor.ContextHint{Reads: []string{applicantKey}},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("a non-Loom operator's instanceOp: outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied") {
		t.Fatalf("want an AuthDenied rejection, got %+v", reply.Error)
	}
	if !strings.Contains(reply.Error.Message, "Loom's relay actor") {
		t.Fatalf("the denial must name the actor guard, got %q", reply.Error.Message)
	}
	if keyExists(t, ctx, conn, "vtx.service."+handle) {
		t.Fatalf("a denied instanceOp must mint NO claim vertex")
	}
}

// findEmittedEvent reads the committed transactional-outbox aspect for an op's
// requestId and returns the payload of the first event of the given class. The
// outbox aspect is the faithful EventList persisted in the step-8 atomic batch
// (the outbox consumer publishes from it) — reading it asserts the emission
// without running the outbox consumer in the test harness.
func findEmittedEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, requestID, class string) map[string]any {
	t.Helper()
	outboxKey := processor.OutboxAspectKey(requestID)
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, outboxKey)
	if err != nil {
		t.Fatalf("read outbox aspect %s: %v", outboxKey, err)
	}
	ob, err := processor.ParseOutboxAspect(entry.Value)
	if err != nil {
		t.Fatalf("parse outbox aspect %s: %v", outboxKey, err)
	}
	for _, e := range ob.Data.Events {
		if e.EventType == class {
			return e.Payload
		}
	}
	t.Fatalf("no %s event emitted by op %s (events: %v)", class, requestID, eventClasses(ob.Data.Events))
	return nil
}

func eventClasses(evs processor.EventList) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.EventType)
	}
	return out
}

// assertNoEmittedEvent fails if an op's transactional-outbox aspect contains any
// event of the given class — the negative of findEmittedEvent (asserting a
// completion signal is NOT emitted on a pending dispatch).
func assertNoEmittedEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, requestID, class string) {
	t.Helper()
	outboxKey := processor.OutboxAspectKey(requestID)
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, outboxKey)
	if err != nil {
		t.Fatalf("read outbox aspect %s: %v", outboxKey, err)
	}
	ob, err := processor.ParseOutboxAspect(entry.Value)
	if err != nil {
		t.Fatalf("parse outbox aspect %s: %v", outboxKey, err)
	}
	for _, e := range ob.Data.Events {
		if e.EventType == class {
			t.Fatalf("op %s must NOT emit a %s event, but it did (events: %v)", requestID, class, eventClasses(ob.Data.Events))
		}
	}
}

// TestLeaseServiceReply_RecordsOutcome_EmitsExternalTaskCompleted (test 4 — THE
// §0.A trap; AC #3). Pre-create a claim vertex, submit RecordLeaseServiceOutcome
// the way the live bridge does — payload {externalRef, result} with NO
// ContextHint.Reads (the bridge's actuator sets none) — and assert: the op
// commits read-free; the .outcome aspect is written (status=completed, canonical
// completedAt, and NO result — the free-form result stays off the projection
// plane, D5 root {}); the op emits orchestration.externalTaskCompleted carrying
// the BARE handle; and a second reply is rejected by the create-only .outcome
// guard, also with no Reads.
func TestLeaseServiceReply_RecordsOutcome_EmitsExternalTaskCompleted(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "replyop")

	applicantKey := seedApplicant(t, ctx, conn, "BBrepapp1cntHJKMNPQR")
	handle := "JFLdWyJmg9A32jxPvDpw"
	instKey := "vtx.service." + handle

	// Mint the claim vertex via the instanceOp (the matched pair).
	instEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("replyInst0001"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseServiceInstance",
		Actor:         bootstrap.LoomIdentityKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseServiceInstance",
		Payload: json.RawMessage(`{"instanceKey":"` + handle + `","subjectKey":"` + applicantKey +
			`","adapter":"backgroundCheck","replyOp":"RecordLeaseServiceOutcome","params":{"family":"backgroundCheck"}}`),
		ContextHint: &processor.ContextHint{Reads: []string{applicantKey}},
	}
	testutil.PublishOp(t, conn, instEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// The replyOp exactly as the live bridge submits it: payload
	// {externalRef, result} and NO ContextHint.Reads (internal/bridge's actuator
	// builds an envelope with no Reads field). It must commit read-free.
	replyReqID := testutil.GenReqID("replyRec00001")
	replyEnv := &processor.OperationEnvelope{
		RequestID:     replyReqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordLeaseServiceOutcome",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-06-18T14:00:00Z",
		Class:         "leaseServiceReply",
		Payload:       json.RawMessage(`{"externalRef":"` + handle + `","status":"completed","result":"background-check cleared for ` + applicantKey + `"}`),
	}
	testutil.PublishOp(t, conn, replyEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// (a) the .outcome aspect — status=completed, canonical completedAt derived
	// from op.submittedAt. The free-form result is NOT written to the aspect (it
	// rides the service.outcomeRecorded provenance event instead).
	odoc := readDoc(t, ctx, conn, instKey+".outcome")
	odata, _ := odoc["data"].(map[string]any)
	if got, _ := odata["status"].(string); got != "completed" {
		t.Fatalf("outcome.status = %q, want completed", got)
	}
	if got, _ := odata["completedAt"].(string); got != "2026-06-18T14:00:00Z" {
		t.Fatalf("outcome.completedAt = %q, want canonical 2026-06-18T14:00:00Z", got)
	}
	// validUntil = completedAt + the bgcheck freshness window, stamped by the
	// read-free replyOp via time.rfc3339_add (pure arithmetic on completedAt, no
	// clock). The demo window is "5m" (see scripts.go bgcheckFreshnessWindow), so
	// 14:00:00Z + 5m = 14:05:00Z. If 14.5 tunes the window, update this constant.
	if got, _ := odata["validUntil"].(string); got != "2026-06-18T14:05:00Z" {
		t.Fatalf("outcome.validUntil = %q, want completedAt + 5m window = 2026-06-18T14:05:00Z", got)
	}
	if _, present := odata["result"]; present {
		t.Fatalf("outcome aspect must NOT carry the free-form result (PII off the projection plane), got %v", odata["result"])
	}
	// (c) D5: the claim-vertex root data stays {}.
	instDoc := readDoc(t, ctx, conn, instKey)
	if d, _ := instDoc["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("claim vertex root data must stay minimal ({}), got %v", d)
	}

	// (b) THE TRAP: the op emits orchestration.externalTaskCompleted carrying the
	// BARE handle as externalRef (not the full vtx key). Without this the
	// externalTask never completes.
	completion := findEmittedEvent(t, ctx, conn, replyReqID, "orchestration.externalTaskCompleted")
	if got, _ := completion["externalRef"].(string); got != handle {
		t.Fatalf("externalTaskCompleted externalRef = %q, want the BARE handle %q", got, handle)
	}
	if completion["externalRef"] == instKey {
		t.Fatalf("externalTaskCompleted externalRef must be the bare handle, not the full vtx key")
	}

	// (b') the free-form result rides the provenance event body (kept off the
	// projection-plane aspect).
	prov := findEmittedEvent(t, ctx, conn, replyReqID, "service.outcomeRecorded")
	if got, _ := prov["result"].(string); got == "" {
		t.Fatalf("service.outcomeRecorded must carry the free-form result for provenance, got empty")
	}

	// (d) a second reply for the same handle is rejected by the create-only
	// .outcome conflict — the FR58 redelivery defense at the DDL layer. The
	// bridge submits no Reads (mirrored here), so the rejection is the batch
	// conflict on the already-existing .outcome key, NOT a state-read guard.
	reply2 := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("replyRec00002"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordLeaseServiceOutcome",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-06-18T15:00:00Z",
		Class:         "leaseServiceReply",
		Payload:       json.RawMessage(`{"externalRef":"` + handle + `","status":"completed","result":"second attempt"}`),
	}
	testutil.PublishOp(t, conn, reply2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestLeaseServiceDispatch_RecordsPendingMarker_NoCompletion: the bridge submits
// RecordServiceDispatch when its adapter returns Pending — payload
// {externalRef, vendorRef} with NO ContextHint.Reads (the bridge's actuator sets
// none). It must commit read-free; the .dispatch aspect is written
// {vendorRef, submittedAt} (D5 root {}); NO .outcome aspect is written; and it
// emits NO orchestration.externalTaskCompleted (the task is not done — the token
// stays parked), only the service.dispatchRecorded provenance. A second dispatch
// for the same handle is rejected by the create-only .dispatch guard.
func TestLeaseServiceDispatch_RecordsPendingMarker_NoCompletion(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "dispatchop")

	handle := "pendHwK4rqZbVnCdLxYj"
	instKey := "vtx.service." + handle
	vendorRef := "vendor-ref-pending-001"
	adapter := "backgroundCheck"
	replyOp := "RecordLeaseServiceOutcome"
	nextPollAt := "2026-06-19T10:00:30Z"
	deadline := "2026-06-20T10:00:00Z"

	reqID := testutil.GenReqID("dispatchRec01")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordServiceDispatch",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-06-19T10:00:00Z",
		Class:         "leaseServiceDispatch",
		// No Reads — exactly as the bridge submits.
		Payload: json.RawMessage(`{"externalRef":"` + handle + `","vendorRef":"` + vendorRef +
			`","adapter":"` + adapter + `","replyOp":"` + replyOp +
			`","nextPollAt":"` + nextPollAt + `","deadline":"` + deadline + `"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// (a) the .dispatch aspect — {vendorRef, adapter, replyOp, submittedAt
	// (canonical-UTC of op.submittedAt), nextPollAt, deadline}.
	ddoc := readDoc(t, ctx, conn, instKey+".dispatch")
	ddata, _ := ddoc["data"].(map[string]any)
	if got, _ := ddata["vendorRef"].(string); got != vendorRef {
		t.Fatalf("dispatch.vendorRef = %q, want %q", got, vendorRef)
	}
	if got, _ := ddata["submittedAt"].(string); got != "2026-06-19T10:00:00Z" {
		t.Fatalf("dispatch.submittedAt = %q, want canonical 2026-06-19T10:00:00Z", got)
	}
	if got, _ := ddata["adapter"].(string); got != adapter {
		t.Fatalf("dispatch.adapter = %q, want %q", got, adapter)
	}
	if got, _ := ddata["replyOp"].(string); got != replyOp {
		t.Fatalf("dispatch.replyOp = %q, want %q", got, replyOp)
	}
	if got, _ := ddata["nextPollAt"].(string); got != nextPollAt {
		t.Fatalf("dispatch.nextPollAt = %q, want %q", got, nextPollAt)
	}
	if got, _ := ddata["deadline"].(string); got != deadline {
		t.Fatalf("dispatch.deadline = %q, want %q", got, deadline)
	}

	// (b) NO .outcome aspect — the call is pending, not terminal (the token stays parked).
	if keyExists(t, ctx, conn, instKey+".outcome") {
		t.Fatalf("a pending dispatch must NOT write the .outcome aspect")
	}

	// (c) D5: the claim-vertex root data stays {} (the instanceOp minted it {}; the
	// dispatch op reconstructs the key read-free and does not touch the root).
	if keyExists(t, ctx, conn, instKey) {
		instDoc := readDoc(t, ctx, conn, instKey)
		if d, _ := instDoc["data"].(map[string]any); len(d) != 0 {
			t.Fatalf("claim vertex root data must stay minimal ({}), got %v", d)
		}
	}

	// (d) NO orchestration.externalTaskCompleted — Loom must NOT close the token on
	// a dispatch. Only the service.dispatchRecorded provenance is emitted.
	assertNoEmittedEvent(t, ctx, conn, reqID, "orchestration.externalTaskCompleted")
	prov := findEmittedEvent(t, ctx, conn, reqID, "service.dispatchRecorded")
	if got, _ := prov["vendorRef"].(string); got != vendorRef {
		t.Fatalf("service.dispatchRecorded vendorRef = %q, want %q", got, vendorRef)
	}

	// (e) a second dispatch for the same handle is rejected by the create-only
	// .dispatch conflict (the once-only guarantee at the DDL layer). The bridge
	// submits no Reads (mirrored here), so the rejection is the batch conflict on
	// the already-existing .dispatch key.
	dispatch2 := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("dispatchRec02"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordServiceDispatch",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-06-19T11:00:00Z",
		Class:         "leaseServiceDispatch",
		Payload: json.RawMessage(`{"externalRef":"` + handle + `","vendorRef":"vendor-ref-pending-002"` +
			`,"adapter":"` + adapter + `","replyOp":"` + replyOp +
			`","nextPollAt":"` + nextPollAt + `","deadline":"` + deadline + `"}`),
	}
	testutil.PublishOp(t, conn, dispatch2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestLeaseServiceDispatch_VendorRefRequired_Rejected: vendorRef is REQUIRED. A
// dispatch with no vendorRef is rejected (InvalidArgument), read-free, and writes
// no .dispatch aspect.
func TestLeaseServiceDispatch_VendorRefRequired_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "dispatch-vendorref-required")

	handle := "missVendorRefHandl9k"
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("dispatchMiss1"),
		Lane:          processor.LaneDefault,
		OperationType: "RecordServiceDispatch",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-06-19T12:00:00Z",
		Class:         "leaseServiceDispatch",
		Payload:       json.RawMessage(`{"externalRef":"` + handle + `"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
	if keyExists(t, ctx, conn, "vtx.service."+handle+".dispatch") {
		t.Fatalf("a rejected dispatch must not write the .dispatch aspect")
	}
}

// TestLeaseServiceReply_FailedStatus_RecordsFailedOutcome: the bridge reply
// carries the adapter's terminal status=failed (a definitive business rejection,
// e.g. a declined charge / a failed background check — NOT a transient error,
// which the bridge Naks and never replies on). The replyOp writes the .outcome
// aspect {status: failed, completedAt} read-free and still emits the completion +
// provenance events. The free-form result stays OFF the projection-plane aspect.
func TestLeaseServiceReply_FailedStatus_RecordsFailedOutcome(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "reply-failed")

	handle := "fT8kPmW2rqZbVnCdLxYj"
	instKey := "vtx.service." + handle
	reqID := testutil.GenReqID("replyFail0001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordLeaseServiceOutcome",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-06-18T19:00:00Z",
		Class:         "leaseServiceReply",
		// No Reads — exactly as the bridge submits.
		Payload: json.RawMessage(`{"externalRef":"` + handle + `","status":"failed","result":"background-check declined for vtx.identity.x"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// The .outcome aspect records status=failed (the lens reads this as the
	// service NOT having converged).
	odoc := readDoc(t, ctx, conn, instKey+".outcome")
	odata, _ := odoc["data"].(map[string]any)
	if got, _ := odata["status"].(string); got != "failed" {
		t.Fatalf("outcome.status = %q, want failed", got)
	}
	// The free-form result stays off the projection-plane aspect (PII discipline).
	if _, present := odata["result"]; present {
		t.Fatalf("outcome aspect must NOT carry the free-form result, got %v", odata["result"])
	}
	// The completion signal is still emitted on a failed outcome (the externalTask
	// completes — a definitive failure IS a completion).
	completion := findEmittedEvent(t, ctx, conn, reqID, "orchestration.externalTaskCompleted")
	if got, _ := completion["externalRef"].(string); got != handle {
		t.Fatalf("externalTaskCompleted externalRef = %q, want the bare handle %q", got, handle)
	}
}

// TestLeaseServiceReply_StatusRequired_Rejected: status is REQUIRED with no
// default. A reply with no status (the old bridge shape) and a reply with an
// out-of-enum status are both rejected (InvalidArgument), read-free.
func TestLeaseServiceReply_StatusRequired_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "reply-status-required")

	cases := []struct {
		name    string
		handle  string
		reqTag  string
		payload string
	}{
		{
			name:    "missing status",
			handle:  "missStatHandl3aBcDeF",
			reqTag:  "replyMiss00001",
			payload: `{"externalRef":"missStatHandl3aBcDeF","result":"x"}`,
		},
		{
			name:    "invalid status",
			handle:  "badStatusHandl9wXyZk",
			reqTag:  "replyBad000001",
			payload: `{"externalRef":"badStatusHandl9wXyZk","status":"maybe","result":"x"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := &processor.OperationEnvelope{
				RequestID:     testutil.GenReqID(tc.reqTag),
				Lane:          processor.LaneDefault,
				OperationType: "RecordLeaseServiceOutcome",
				Actor:         lsActorKey,
				SubmittedAt:   "2026-06-18T20:00:00Z",
				Class:         "leaseServiceReply",
				Payload:       json.RawMessage(tc.payload),
			}
			testutil.PublishOp(t, conn, env)
			testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
			// No .outcome aspect was written (the op was rejected before any mutation).
			if keyExists(t, ctx, conn, "vtx.service."+tc.handle+".outcome") {
				t.Fatalf("a rejected reply must not write the .outcome aspect")
			}
		})
	}
}

// TestLeaseServiceReply_ReadFree_CommitsWithoutHydration: the replyOp is
// read-free by design (the bridge submits no ContextHint.Reads), so it does not
// depend on the claim vertex being hydrated — it derives inst_key from the bare
// handle and writes the create-only .outcome aspect regardless. This is the
// faithful live-bridge path: the bridge only ever replies to instances it
// created, so there is no "unknown instance" guard to fire (Fix dropped the
// vertex_alive / .class checks that referenced unhydrated state). The op commits
// and emits the completion signal.
func TestLeaseServiceReply_ReadFree_CommitsWithoutHydration(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "reply-readfree")

	handle := "uWm47ejkmzurjtX69AKL"
	instKey := "vtx.service." + handle
	reqID := testutil.GenReqID("replyRf000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RecordLeaseServiceOutcome",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-06-18T18:00:00Z",
		Class:         "leaseServiceReply",
		// No Reads — exactly as the bridge submits. The op reads no state.
		Payload: json.RawMessage(`{"externalRef":"` + handle + `","status":"completed","result":"x"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// The .outcome aspect is written read-free.
	odoc := readDoc(t, ctx, conn, instKey+".outcome")
	odata, _ := odoc["data"].(map[string]any)
	if got, _ := odata["status"].(string); got != "completed" {
		t.Fatalf("outcome.status = %q, want completed", got)
	}
	// The completion signal is emitted (the load-bearing externalTask close).
	completion := findEmittedEvent(t, ctx, conn, reqID, "orchestration.externalTaskCompleted")
	if got, _ := completion["externalRef"].(string); got != handle {
		t.Fatalf("externalTaskCompleted externalRef = %q, want the bare handle %q", got, handle)
	}
}

// TestCreateLeaseApplication_RootMinimal_LinkSentenceValid (AC #1 + D5): the
// application root data is {} and the applicationFor link is sentence-valid
// (leaseapp is the source, identity the target).
func TestCreateLeaseApplication_RootMinimal_LinkSentenceValid(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "create-app")

	applicantKey := seedApplicant(t, ctx, conn, "BBcrapp1cantHJKMNPQR")
	applicantID := applicantKey[len("vtx.identity."):]
	appKey := createApplication(t, ctx, conn, cp, cons, applicantKey)
	appID := appKey[len("vtx.leaseapp."):]

	appDoc := readDoc(t, ctx, conn, appKey)
	if cls, _ := appDoc["class"].(string); cls != "leaseapp" {
		t.Fatalf("application class = %q, want leaseapp", cls)
	}
	if d, _ := appDoc["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("application root data must be minimal ({}), got %v", d)
	}
	lnk := "lnk.leaseapp." + appID + ".applicationFor.identity." + applicantID
	ldoc := readDoc(t, ctx, conn, lnk)
	if got, _ := ldoc["sourceVertex"].(string); got != appKey {
		t.Fatalf("applicationFor sourceVertex = %q, want %q (leaseapp is source)", got, appKey)
	}
	if got, _ := ldoc["targetVertex"].(string); got != applicantKey {
		t.Fatalf("applicationFor targetVertex = %q, want %q (identity is target)", got, applicantKey)
	}
}

// TestCreateLeaseApplication_UnknownApplicant_Rejected: an application for a
// non-existent applicant is rejected (no-orphan, FR29).
func TestCreateLeaseApplication_UnknownApplicant_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "create-app-orphan")

	missing := "vtx.identity.BBnxapp1cantHJKMNPQR"
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("appOrphan0001"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseApplication",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseapp",
		// The missing applicant is NOT listed in Reads (a non-existent listed key
		// is a hydration miss); omitting it lets the op reach the script where the
		// UnknownApplicant guard rejects.
		Payload:     json.RawMessage(`{"applicant":"` + missing + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestCreateLeaseApplication_AppliesToUnit_LinkSentenceValid (Increment 2): an
// application requires a live unit, writes the appliesToUnit link (leaseapp is
// source, unit is target — sentence-valid, Contract #1 §1.1), and writes the
// optional .terms aspect when moveInDate is supplied (root data stays {} — D5).
func TestCreateLeaseApplication_AppliesToUnit_LinkSentenceValid(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "create-app-unit")

	applicantKey := seedApplicant(t, ctx, conn, "BBunitapp1cntHJKMNPQ")
	unitKey := seedUnit(t, ctx, conn, "BBunitvtx1cntHJKMNPQ")
	unitID := unitKey[len("vtx.unit."):]

	reqID := testutil.GenReqID("appUnit000001")
	appID := nanoIDFromRequestID(reqID)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseApplication",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"applicant":"` + applicantKey + `","unit":"` + unitKey + `","moveInDate":"2026-08-01","leaseTermMonths":12,"requestedRent":2400}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{applicantKey, unitKey},
			OptionalReads: []string{guardLinkKey(applicantKey, unitKey)},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	appKey := "vtx.leaseapp." + appID

	lnk := "lnk.leaseapp." + appID + ".appliesToUnit.unit." + unitID
	ldoc := readDoc(t, ctx, conn, lnk)
	if got, _ := ldoc["sourceVertex"].(string); got != appKey {
		t.Fatalf("appliesToUnit sourceVertex = %q, want %q (leaseapp is source)", got, appKey)
	}
	if got, _ := ldoc["targetVertex"].(string); got != unitKey {
		t.Fatalf("appliesToUnit targetVertex = %q, want %q (unit is target)", got, unitKey)
	}

	tdoc := readDoc(t, ctx, conn, appKey+".terms")
	tdata, _ := tdoc["data"].(map[string]any)
	if got, _ := tdata["moveInDate"].(string); got != "2026-08-01" {
		t.Fatalf("terms.moveInDate = %q, want 2026-08-01", got)
	}
	appDoc := readDoc(t, ctx, conn, appKey)
	if d, _ := appDoc["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("application root data must stay minimal ({}), got %v", d)
	}
}

// TestCreateLeaseApplication_UnknownUnit_Rejected: an application naming a
// non-existent unit is rejected (no-orphan; unit is required + alive-checked).
func TestCreateLeaseApplication_UnknownUnit_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "create-app-unit-orphan")

	applicantKey := seedApplicant(t, ctx, conn, "BBnxunitapp1HJKMNPQR")
	missingUnit := "vtx.unit.BBnxunitvtxcntHJKMNP"
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("unitOrphan001"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseApplication",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseapp",
		// The applicant IS alive (listed in Reads); the unit is missing and NOT
		// listed (a non-existent listed key is a hydration miss), so the op reaches
		// the script where the UnknownUnit guard rejects.
		Payload:     json.RawMessage(`{"applicant":"` + applicantKey + `","unit":"` + missingUnit + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{applicantKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestCreateLeaseApplication_ConsumerSelfScope_Allowed exercises the real
// consumer scope=self permission (permissions.go) end to end at step 3 —
// distinct from the operator-path tests above, which never touch this grant.
// A consumer submits for themselves (applicant == actor) with
// authContext.target == actor (the scope=self step-3 requirement, Contract
// #6) and is accepted.
func TestCreateLeaseApplication_ConsumerSelfScope_Allowed(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "create-app-consumer-self")

	applicantKey := seedApplicant(t, ctx, conn, "BBConsumer1AppHJKMNP")
	unitKey := seedUnit(t, ctx, conn, "BBConsumer1UnitHJKMN")
	seedConsumerSelfCapDoc(t, ctx, conn, applicantKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("consumerSelf01"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseApplication",
		Actor:         applicantKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"applicant":"` + applicantKey + `","unit":"` + unitKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{applicantKey, unitKey},
			OptionalReads: []string{guardLinkKey(applicantKey, unitKey)},
		},
		AuthContext: &processor.AuthContext{Target: applicantKey},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

// TestCreateLeaseApplication_ConsumerNamesDifferentApplicant_Rejected: step 3's
// scope=self only checks authContext.target == actor (Contract #6) — it never
// looks at payload.applicant. A consumer satisfying that check (target ==
// actor) but naming a DIFFERENT identity as the applicant must still be
// rejected, by the Starlark applicant-self guard (scripts.go).
func TestCreateLeaseApplication_ConsumerNamesDifferentApplicant_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "create-app-consumer-forge")

	consumerKey := seedApplicant(t, ctx, conn, "BBConsumer3AppHJKMNP")
	victimKey := seedApplicant(t, ctx, conn, "BBConsumer2AppHJKMNP")
	unitKey := seedUnit(t, ctx, conn, "BBConsumer3UnitHJKMN")
	seedConsumerSelfCapDoc(t, ctx, conn, consumerKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("consumerForge1"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseApplication",
		Actor:         consumerKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"applicant":"` + victimKey + `","unit":"` + unitKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{victimKey, unitKey},
			OptionalReads: []string{guardLinkKey(victimKey, unitKey)},
		},
		AuthContext: &processor.AuthContext{Target: consumerKey},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// applyToUnit submits CreateLeaseApplication for applicantKey against a
// caller-supplied unitKey (so multiple applications can target the SAME unit —
// the per-unit duplicate-guard surface) and drives it to want. On
// OutcomeAccepted it returns the new app key; otherwise "". label must be
// unique per call (the request id — and so the minted app id — is deterministic
// from it).
func applyToUnit(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, applicantKey, unitKey string, want processor.MessageOutcome) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	appID := nanoIDFromRequestID(reqID)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateLeaseApplication",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"applicant":"` + applicantKey + `","unit":"` + unitKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{applicantKey, unitKey},
			OptionalReads: []string{guardLinkKey(applicantKey, unitKey)},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
	if want == processor.OutcomeAccepted {
		return "vtx.leaseapp." + appID
	}
	return ""
}

// TestCreateLeaseApplication_DuplicateSameApplicantSameUnit_Rejected: the
// reported bug — one applicant applying twice to the SAME unit must be rejected
// (DuplicateApplication), so a unit never accumulates duplicate live
// applications for one applicant (the bare-shell that pinned Weaver red).
func TestCreateLeaseApplication_DuplicateSameApplicantSameUnit_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "dup-same-applicant")

	applicant := seedApplicant(t, ctx, conn, "BBDUPAAAHJKMNPQRSTUV")
	unit := seedUnit(t, ctx, conn, "BBDUPUUUHJKMNPQRSTUV")

	first := applyToUnit(t, ctx, conn, cp, cons, "dupFirstAAAA", applicant, unit, processor.OutcomeAccepted)
	if first == "" {
		t.Fatalf("first application should commit")
	}
	// The per-(applicant, unit) guard link now exists + is alive.
	if !keyExists(t, ctx, conn, guardLinkKey(applicant, unit)) {
		t.Fatalf("guard link should be alive after the first apply")
	}
	// Same applicant, same unit, second time → rejected (the guard blocks it).
	applyToUnit(t, ctx, conn, cp, cons, "dupSecondBBB", applicant, unit, processor.OutcomeRejected)
}

// TestCreateLeaseApplication_DifferentApplicantsSameUnit_Allowed: two DIFFERENT
// applicants applying to one unit both commit — normal leasing (the landlord
// chooses among applicants); the guard is per-applicant, not a unit lock.
func TestCreateLeaseApplication_DifferentApplicantsSameUnit_Allowed(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "diff-applicants")

	alice := seedApplicant(t, ctx, conn, "BBDFAAAAHJKMNPQRSTUV")
	bob := seedApplicant(t, ctx, conn, "BBDFBBBBHJKMNPQRSTUV")
	unit := seedUnit(t, ctx, conn, "BBDFUUUUHJKMNPQRSTUV")

	a := applyToUnit(t, ctx, conn, cp, cons, "diffAliceAAA", alice, unit, processor.OutcomeAccepted)
	b := applyToUnit(t, ctx, conn, cp, cons, "diffBobBBBBB", bob, unit, processor.OutcomeAccepted)
	if a == "" || b == "" || a == b {
		t.Fatalf("both distinct-applicant applications should commit to distinct keys; got a=%q b=%q", a, b)
	}
	// Each applicant has their own per-(applicant, unit) guard link — the guard is
	// per-pair, not a unit lock, so both are alive and distinct.
	if !keyExists(t, ctx, conn, guardLinkKey(alice, unit)) || !keyExists(t, ctx, conn, guardLinkKey(bob, unit)) {
		t.Fatalf("both applicants' guard links should be alive (alice=%v bob=%v)",
			keyExists(t, ctx, conn, guardLinkKey(alice, unit)), keyExists(t, ctx, conn, guardLinkKey(bob, unit)))
	}
	if guardLinkKey(alice, unit) == guardLinkKey(bob, unit) {
		t.Fatalf("distinct applicants must map to distinct guard links")
	}
}

// TestCreateLeaseApplication_SameApplicantDifferentUnits_Allowed: one applicant
// may apply to two DIFFERENT units (the index is per-unit).
func TestCreateLeaseApplication_SameApplicantDifferentUnits_Allowed(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "same-app-diff-unit")

	applicant := seedApplicant(t, ctx, conn, "BBMUAPPAHJKMNPQRSTUV")
	unit1 := seedUnit(t, ctx, conn, "BBMUUNNAHJKMNPQRSTUV")
	unit2 := seedUnit(t, ctx, conn, "BBMUUNNBHJKMNPQRSTUV")

	a := applyToUnit(t, ctx, conn, cp, cons, "multiUnit1AA", applicant, unit1, processor.OutcomeAccepted)
	b := applyToUnit(t, ctx, conn, cp, cons, "multiUnit2BB", applicant, unit2, processor.OutcomeAccepted)
	if a == "" || b == "" {
		t.Fatalf("same applicant applying to two different units should both commit; got a=%q b=%q", a, b)
	}
}

// TestCreateLeaseApplication_ReapplyAfterWithdraw_RevivesGuardLink: a withdraw
// frees (tombstones) the per-(applicant, unit) guard link; a re-apply then
// REVIVES that same tombstoned link (a blind create would collide with the
// tombstone — revive-on-create) rather than minting a new one, and the new
// application commits. The guard link is the authoritative uniqueness record,
// freed only by WithdrawLeaseApplication.
func TestCreateLeaseApplication_ReapplyAfterWithdraw_RevivesGuardLink(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "guard-revive")

	applicant := seedApplicant(t, ctx, conn, "BBTMAPPAHJKMNPQRSTUV")
	unit := seedUnit(t, ctx, conn, "BBTMUUUUHJKMNPQRSTUV")
	gk := guardLinkKey(applicant, unit)

	first := applyToUnit(t, ctx, conn, cp, cons, "revFirstAAAA", applicant, unit, processor.OutcomeAccepted)
	if first == "" {
		t.Fatalf("first application should commit")
	}
	if !keyExists(t, ctx, conn, gk) {
		t.Fatalf("guard link should be alive after the first apply")
	}

	// Withdraw frees the guard link (tombstones it — present but isDeleted).
	withdraw(t, ctx, conn, cp, cons, "revWithdraw0", first, unit, applicant, processor.OutcomeAccepted)
	if keyExists(t, ctx, conn, gk) {
		t.Fatalf("guard link should be tombstoned (not alive) after withdraw")
	}
	if d, _ := readDoc(t, ctx, conn, gk)["isDeleted"].(bool); !d {
		t.Fatalf("withdrawn guard link should be a tombstone (isDeleted=true), not absent — re-apply must revive it")
	}

	// Re-apply: the tombstoned guard is revived (same key, alive again) and the new
	// application commits to a fresh key.
	second := applyToUnit(t, ctx, conn, cp, cons, "revSecondBB", applicant, unit, processor.OutcomeAccepted)
	if second == "" || second == first {
		t.Fatalf("re-application after withdrawal should commit to a new key; got %q (first=%q)", second, first)
	}
	if !keyExists(t, ctx, conn, gk) {
		t.Fatalf("guard link should be revived (alive) after re-apply")
	}
}

// withdraw submits WithdrawLeaseApplication{leaseAppKey, unit, applicant} (class
// leaseapp). reads carries the two (a) required validation links (appliesToUnit /
// applicationFor); optionalReads carries the (d) guard link it frees.
func withdraw(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseAppKey, unitKey, applicantKey string, want processor.MessageOutcome) {
	t.Helper()
	_, appID, _ := substrate.ParseVertexKey(leaseAppKey)
	_, unitID, _ := substrate.ParseVertexKey(unitKey)
	_, applicantID, _ := substrate.ParseVertexKey(applicantKey)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "WithdrawLeaseApplication",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseAppKey + `","unit":"` + unitKey + `","applicant":"` + applicantKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{
				leaseAppKey,
				"lnk.leaseapp." + appID + ".appliesToUnit.unit." + unitID,
				"lnk.leaseapp." + appID + ".applicationFor.identity." + applicantID,
			},
			OptionalReads: []string{guardLinkKey(applicantKey, unitKey)},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// withdrawAsConsumer is withdraw on the consumer scope=self path: the acting
// identity is authActor (Actor + authContext.target both authActor, the step-3
// scope=self shape), while payload.applicant names the application's applicant.
// The self-applicant is the case the new in-script guard binds — a consumer may
// withdraw only their own application.
func withdrawAsConsumer(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseAppKey, unitKey, applicantKey, authActor string, want processor.MessageOutcome) {
	t.Helper()
	_, appID, _ := substrate.ParseVertexKey(leaseAppKey)
	_, unitID, _ := substrate.ParseVertexKey(unitKey)
	_, applicantID, _ := substrate.ParseVertexKey(applicantKey)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "WithdrawLeaseApplication",
		Actor:         authActor,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseAppKey + `","unit":"` + unitKey + `","applicant":"` + applicantKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{
				leaseAppKey,
				"lnk.leaseapp." + appID + ".appliesToUnit.unit." + unitID,
				"lnk.leaseapp." + appID + ".applicationFor.identity." + applicantID,
			},
			OptionalReads: []string{guardLinkKey(applicantKey, unitKey)},
		},
		AuthContext: &processor.AuthContext{Target: authActor},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// setProfileAsConsumer is setProfile on the consumer scope=self path: the acting
// identity is authActor (Actor + authContext.target both authActor). The
// consumer path declares the applicationFor self-link keyed on the actor's own
// id — the read the in-script guard consults to bind the actor to their own
// application.
func setProfileAsConsumer(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseAppKey, unit, authActor string, payload map[string]any, want processor.MessageOutcome) {
	t.Helper()
	payload["leaseAppKey"] = leaseAppKey
	payload["unit"] = unit
	b, _ := json.Marshal(payload)
	_, appID, _ := substrate.ParseVertexKey(leaseAppKey)
	_, unitID, _ := substrate.ParseVertexKey(unit)
	_, authID, _ := substrate.ParseVertexKey(authActor)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SetApplicantProfile",
		Actor:         authActor,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseapp",
		Payload:       json.RawMessage(b),
		ContextHint: &processor.ContextHint{
			Reads: []string{
				leaseAppKey,
				"lnk.leaseapp." + appID + ".appliesToUnit.unit." + unitID,
				"lnk.leaseapp." + appID + ".applicationFor.identity." + authID,
			},
			OptionalReads: []string{unit + ".listing"},
		},
		AuthContext: &processor.AuthContext{Target: authActor},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// guardLinkKey reconstructs the per-(applicant, unit) duplicate-guard link key
// lnk.identity.<aid>.appliedToUnit.unit.<uid> from the full applicant + unit
// vertex keys (the deterministic existence-uniqueness guard).
func guardLinkKey(applicantKey, unitKey string) string {
	aid := strings.TrimPrefix(applicantKey, "vtx.identity.")
	uid := strings.TrimPrefix(unitKey, "vtx.unit.")
	return "lnk.identity." + aid + ".appliedToUnit.unit." + uid
}

// TestWithdrawLeaseApplication drives the real withdraw op: a wrong unit
// (UnitMismatch) or wrong applicant (ApplicantMismatch) is rejected without
// tombstoning; the correct withdraw tombstones the application AND frees the
// per-(applicant, unit) guard link; the applicant can then re-apply to the same
// unit; an unknown / already-withdrawn application is rejected.
func TestWithdrawLeaseApplication(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "withdraw")

	applicant := seedApplicant(t, ctx, conn, "BBWDAPPAHJKMNPQRSTUV")
	otherApplicant := seedApplicant(t, ctx, conn, "BBWDAPP2HJKMNPQRSTUV")
	unit := seedUnit(t, ctx, conn, "BBWDUUUUHJKMNPQRSTUV")
	otherUnit := seedUnit(t, ctx, conn, "BBWDOTHRHJKMNPQRSTUV")
	gk := guardLinkKey(applicant, unit)

	first := applyToUnit(t, ctx, conn, cp, cons, "wdFirstAAAA", applicant, unit, processor.OutcomeAccepted)
	if first == "" {
		t.Fatalf("first application should commit")
	}
	if !keyExists(t, ctx, conn, gk) {
		t.Fatalf("guard link should be alive before withdraw")
	}

	// Wrong unit → UnitMismatch (rejected), and the application is NOT tombstoned.
	withdraw(t, ctx, conn, cp, cons, "wdWrongUnit", first, otherUnit, applicant, processor.OutcomeRejected)
	if d, _ := readDoc(t, ctx, conn, first)["isDeleted"].(bool); d {
		t.Fatalf("a wrong-unit withdraw must NOT tombstone the application")
	}

	// Wrong applicant → ApplicantMismatch (rejected), and the application is NOT tombstoned.
	withdraw(t, ctx, conn, cp, cons, "wdWrongAppl", first, unit, otherApplicant, processor.OutcomeRejected)
	if d, _ := readDoc(t, ctx, conn, first)["isDeleted"].(bool); d {
		t.Fatalf("a wrong-applicant withdraw must NOT tombstone the application")
	}

	// Correct unit + applicant → Accepted: tombstoned + guard link freed.
	withdraw(t, ctx, conn, cp, cons, "wdCorrect01", first, unit, applicant, processor.OutcomeAccepted)
	if d, _ := readDoc(t, ctx, conn, first)["isDeleted"].(bool); !d {
		t.Fatalf("withdraw must tombstone the application")
	}
	if keyExists(t, ctx, conn, gk) {
		t.Fatalf("guard link should be freed (tombstoned) after withdraw")
	}

	// Re-apply (same applicant, same unit) → Accepted: the withdrawal unblocked it.
	second := applyToUnit(t, ctx, conn, cp, cons, "wdReapply01", applicant, unit, processor.OutcomeAccepted)
	if second == "" || second == first {
		t.Fatalf("re-application after withdrawal should commit to a new key; got %q (first=%q)", second, first)
	}
	if !keyExists(t, ctx, conn, gk) {
		t.Fatalf("guard link should be revived (alive) after re-apply")
	}

	// Double-withdraw the now-tombstoned first application → Rejected (UnknownLeaseApplication).
	withdraw(t, ctx, conn, cp, cons, "wdDouble001", first, unit, applicant, processor.OutcomeRejected)
}

// withdrawReason submits WithdrawLeaseApplication and returns the Processor's
// rejection message. authActor empty selects the operator path (no
// authContext); otherwise it is the consumer scope=self shape. The MESSAGE is
// the point: an outcome alone cannot tell an authorization denial from a
// payload-validation error, and that difference is exactly what a caller must
// not be able to observe on someone else's application.
func withdrawReason(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseAppKey, unitKey, applicantKey, authActor string) string {
	t.Helper()
	_, appID, _ := substrate.ParseVertexKey(leaseAppKey)
	_, unitID, _ := substrate.ParseVertexKey(unitKey)
	_, applicantID, _ := substrate.ParseVertexKey(applicantKey)
	actor := lsActorKey
	if authActor != "" {
		actor = authActor
	}
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "WithdrawLeaseApplication",
		Actor:         actor,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseAppKey + `","unit":"` + unitKey + `","applicant":"` + applicantKey + `"}`),
		// The two endpoint links are declared OPTIONAL here, which is what a
		// prober's envelope looks like: a client that expects them to exist
		// declares them in Reads and a miss fails at hydration, never reaching
		// the script — but the submitter chooses its own contextHint, and
		// declaring them optional makes absence a script-visible branch. So the
		// in-script ordering is the only thing standing between a prober and the
		// answer.
		ContextHint: &processor.ContextHint{
			Reads: []string{leaseAppKey},
			OptionalReads: []string{
				"lnk.leaseapp." + appID + ".appliesToUnit.unit." + unitID,
				"lnk.leaseapp." + appID + ".applicationFor.identity." + applicantID,
				guardLinkKey(applicantKey, unitKey),
			},
		},
	}
	if authActor != "" {
		env.AuthContext = &processor.AuthContext{Target: authActor}
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("%s: outcome = %v, want Rejected", label, outcome)
	}
	if reply.Error == nil {
		t.Fatalf("%s: rejected reply carries no error", label)
	}
	// The script's own failure text, past the per-op requestId the wrapper
	// carries — two submissions of the same denial differ in that id alone, and
	// comparing it would report every pair as distinguishable.
	msg := reply.Error.Message
	marker := "fail: "
	i := strings.Index(msg, marker)
	if i < 0 {
		t.Fatalf("%s: rejection carries no script failure: %s", label, msg)
	}
	return msg[i+len(marker):]
}

// TestWithdrawLeaseApplication_ProbesTellAConsumerNothing: the two endpoint
// verifications each answer differently for a real endpoint than a wrong one,
// so whichever runs before the applicant binding is a probe a consumer can walk.
// The unit space of a building is small and enumerable, so a consumer who could
// reach the unit check on a stranger's application would learn which unit it is
// for, and the applicant check would then name who applied. The binding costs no
// read, so it answers first and both probes stay behind it.
func TestWithdrawLeaseApplication_ProbesTellAConsumerNothing(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "withdraw-probe")

	victim := seedApplicant(t, ctx, conn, "BBWDPRVCTHJKMNPQRSTU")
	attacker := seedApplicant(t, ctx, conn, "BBWDPRATKHJKMNPQRSTU")
	unit := seedUnit(t, ctx, conn, "BBWDPRUNTHJKMNPQRSTU")
	otherUnit := seedUnit(t, ctx, conn, "BBWDPRQTHHJKMNPQRSTU")
	seedConsumerSelfCapDoc(t, ctx, conn, attacker)

	app := applyToUnit(t, ctx, conn, cp, cons, "wdprCreate1", victim, unit, processor.OutcomeAccepted)
	if app == "" {
		t.Fatalf("application should commit")
	}

	// The attacker must name THEMSELVES as applicant — naming the victim is what
	// the binding already rejects — so the only lever left is the unit. Guessing
	// it right must be indistinguishable from guessing it wrong.
	right := withdrawReason(t, ctx, conn, cp, cons, "wdprRight01", app, unit, attacker, attacker)
	wrong := withdrawReason(t, ctx, conn, cp, cons, "wdprWrong01", app, otherUnit, attacker, attacker)
	if right != wrong {
		t.Fatalf("the unit probe distinguishes a correct guess from a wrong one:\n  correct unit → %s\n  wrong unit   → %s\n"+
			"that difference walks a stranger's application down to the unit it is for", right, wrong)
	}
	if !strings.Contains(right, "ApplicantMismatch") {
		t.Errorf("the applicant binding should be what answers a foreign consumer, got %q", right)
	}
	if d, _ := readDoc(t, ctx, conn, app)["isDeleted"].(bool); d {
		t.Fatalf("a probe must not tombstone the application")
	}

	// The trusted operator path keeps its precise diagnostics — the guard closes
	// a leak to untrusted callers, it does not blunt every error into one.
	if got := withdrawReason(t, ctx, conn, cp, cons, "wdprOper001", app, otherUnit, victim, ""); !strings.Contains(got, "UnitMismatch") {
		t.Errorf("operator wrong-unit withdraw = %q, want UnitMismatch", got)
	}
	if got := withdrawReason(t, ctx, conn, cp, cons, "wdprOper002", app, unit, attacker, ""); !strings.Contains(got, "ApplicantMismatch") {
		t.Errorf("operator wrong-applicant withdraw = %q, want ApplicantMismatch", got)
	}
}

// TestWithdrawLeaseApplication_ConsumerSelfScope exercises the consumer
// scope=self grant (permissions.go) end to end: step 3 authorizes scope=self by
// checking authContext.target == actor, but never inspects the payload, so a
// consumer could satisfy it while naming SOMEONE ELSE's application. The
// in-script guard (scripts.go: authContextTarget == applicant) closes that gap —
// an applicant withdraws only their own application. The operator path stays
// covered by TestWithdrawLeaseApplication.
func TestWithdrawLeaseApplication_ConsumerSelfScope(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "withdraw-consumer-self")

	applicant := seedApplicant(t, ctx, conn, "BBWDCSAPPHJKMNPQRSTU")
	attacker := seedApplicant(t, ctx, conn, "BBWDCSATKHJKMNPQRSTU")
	unit := seedUnit(t, ctx, conn, "BBWDCSUNTHJKMNPQRSTU")
	seedConsumerSelfCapDoc(t, ctx, conn, applicant)
	seedConsumerSelfCapDoc(t, ctx, conn, attacker)

	// The application is created FOR the applicant (operator path); the guard
	// link is alive before any withdraw.
	app := applyToUnit(t, ctx, conn, cp, cons, "wdcsCreate01", applicant, unit, processor.OutcomeAccepted)
	if app == "" {
		t.Fatalf("application should commit")
	}

	// Attacker acts as themselves (authContext.target == attacker) but names the
	// victim as the application's applicant. The authContextTarget == applicant
	// guard answers first and rejects it (AuthDenied), before any endpoint link
	// is read. The application must NOT be tombstoned.
	withdrawAsConsumer(t, ctx, conn, cp, cons, "wdcsForge01", app, unit, applicant, attacker, processor.OutcomeRejected)
	if d, _ := readDoc(t, ctx, conn, app)["isDeleted"].(bool); d {
		t.Fatalf("a foreign-consumer withdraw must NOT tombstone the application")
	}

	// The applicant withdraws their OWN application → Accepted (tombstoned).
	withdrawAsConsumer(t, ctx, conn, cp, cons, "wdcsSelf001", app, unit, applicant, applicant, processor.OutcomeAccepted)
	if d, _ := readDoc(t, ctx, conn, app)["isDeleted"].(bool); !d {
		t.Fatalf("a self withdraw must tombstone the application")
	}
}

// TestSignLease_WritesSignatureAspect (test 8 — the assignTask gap closure; D5).
// SignLease writes the .signature aspect (root stays {}); a second SignLease is
// rejected (once-only).
func TestSignLease_WritesSignatureAspect(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "sign")

	applicantKey := seedApplicant(t, ctx, conn, "BBsignapp1cntHJKMNPQ")
	appKey := createApplication(t, ctx, conn, cp, cons, applicantKey)

	// Before SignLease: no .signature aspect (missing_signature would be true).
	if keyExists(t, ctx, conn, appKey+".signature") {
		t.Fatalf(".signature aspect must not exist before SignLease")
	}

	signEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("sign000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "SignLease",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-06-18T16:00:00Z",
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + appKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{appKey}, OptionalReads: []string{appKey + ".decision"}},
	}
	testutil.PublishOp(t, conn, signEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	sdoc := readDoc(t, ctx, conn, appKey+".signature")
	sdata, _ := sdoc["data"].(map[string]any)
	if got, _ := sdata["signedAt"].(string); got != "2026-06-18T16:00:00Z" {
		t.Fatalf("signature.signedAt = %q, want canonical 2026-06-18T16:00:00Z", got)
	}
	// D5: the application root data stays {}.
	appDoc := readDoc(t, ctx, conn, appKey)
	if d, _ := appDoc["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("application root data must stay minimal ({}) after sign, got %v", d)
	}

	// A second SignLease is rejected (the .signature CreateOnly once-only guard).
	sign2 := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("sign000000002"),
		Lane:          processor.LaneDefault,
		OperationType: "SignLease",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-06-18T17:00:00Z",
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + appKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{appKey, appKey + ".signature"}, OptionalReads: []string{appKey + ".decision"}},
	}
	testutil.PublishOp(t, conn, sign2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	// keep the identity-domain dependency reference resolved.
	_ = identitydomain.Package
}

// setUnitLeasedStatus overwrites unitKey's .listing aspect with status:leased
// (preserving the fields seedUnit wrote), simulating a RIVAL applicant's
// approval having already flipped the unit via SetListingStatus. Written
// directly rather than through the op — these package tests don't install
// loftspace-domain/location-domain (seedUnit's own doc comment).
func setUnitLeasedStatus(t *testing.T, ctx context.Context, conn *substrate.Conn, unitKey string) {
	t.Helper()
	listing := map[string]any{
		"class": "listing", "isDeleted": false, "vertexKey": unitKey, "localName": "listing",
		"data": map[string]any{
			"availableFrom":   "2026-08-01T00:00:00Z",
			"leaseTermMonths": 12,
			"rentAmount":      2400,
			"rentCurrency":    "USD",
			"status":          "leased",
		},
	}
	lb, _ := json.Marshal(listing)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, unitKey+".listing", lb); err != nil {
		t.Fatalf("set unit .listing leased %s: %v", unitKey, err)
	}
}

// tombstoneUnit soft-deletes unitKey's own vertex directly, simulating the
// unit being withdrawn from the market after this application was created.
func tombstoneUnit(t *testing.T, ctx context.Context, conn *substrate.Conn, unitKey string) {
	t.Helper()
	doc := map[string]any{"class": "location", "isDeleted": true, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, unitKey, b); err != nil {
		t.Fatalf("tombstone unit %s: %v", unitKey, err)
	}
}

// TestSignLease_RejectsUnitLeasedToRival closes the live gap: Weaver only
// stops DISPATCHING new SignLease tasks once missing_signature's own gate
// ((unitStatus <> 'leased') OR (landlordDecision = 'approved')) flips false —
// it does not retract a grant already handed out. A rival applicant holding
// an unexpired ephemeral grant on a unit since leased to someone else must
// still be rejected by the op itself, not merely by an eventually-expiring
// grant.
func TestSignLease_RejectsUnitLeasedToRival(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "sign-rival")

	applicantKey := seedApplicant(t, ctx, conn, "BBsignrivaXtHJKMNPQR")
	appKey := createApplication(t, ctx, conn, cp, cons, applicantKey)
	unitKey := unitKeyFor(applicantKey)

	// The unit leases -- to whichever applicant the landlord actually
	// approved, not this one (this application never received a .decision).
	setUnitLeasedStatus(t, ctx, conn, unitKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("signrival0001"),
		Lane:          processor.LaneDefault,
		OperationType: "SignLease",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-08-27T16:00:00Z",
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + appKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{appKey}, OptionalReads: []string{appKey + ".decision"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	if keyExists(t, ctx, conn, appKey+".signature") {
		t.Fatalf("a rival applicant must NOT be able to sign a unit already leased to someone else")
	}
}

// TestSignLease_RejectsTombstonedUnit mirrors the rival case for a unit
// withdrawn from the market entirely (leaseapp_unit resolves None once the
// unit vertex itself is tombstoned).
func TestSignLease_RejectsTombstonedUnit(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "sign-tombstoned")

	applicantKey := seedApplicant(t, ctx, conn, "BBsigntomb1ntHJKMNPQ")
	appKey := createApplication(t, ctx, conn, cp, cons, applicantKey)
	unitKey := unitKeyFor(applicantKey)

	tombstoneUnit(t, ctx, conn, unitKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("signtomb00001"),
		Lane:          processor.LaneDefault,
		OperationType: "SignLease",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-08-27T16:00:00Z",
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + appKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{appKey}, OptionalReads: []string{appKey + ".decision"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	if keyExists(t, ctx, conn, appKey+".signature") {
		t.Fatalf("signing an application whose unit is tombstoned must be rejected")
	}
}

// decideReadsFor builds DecideLeaseApplication's declared ContextHint:
// leaseAppKey itself is the only required read. .tenancy, .decision (the
// terminal-decision guard's prior-value check), and .signature (the
// approve-readiness floor) are all (d) optionalReads — None is the expected
// first-decide / first-approve case. .decidedProfileSnapshot is declared too
// — it is its OWN create-only guard (scripts.go), read independently of
// .decision so a concurrent double-decide's losing create gracefully
// retries/no-ops at commit (commit_path.go's absentConditionedCreates)
// instead of hard-rejecting the whole batch; .profile / .underwritingParties
// / .applicationSignals are the data it copies on the first decision, absent
// whenever a landlord decides before a profile was ever submitted. The
// appliesToUnit link + unit.listing are declared nowhere here — the script
// resolves the unit itself from the application's own appliesToUnit link on
// the FIRST approve (a class-(e) follow-up, scripts.go), never from a
// client-declared read, unit param kept for call-site symmetry with
// decide()'s signature though unused here.
func decideReadsFor(leaseAppKey, unit string) *processor.ContextHint {
	_ = unit
	return &processor.ContextHint{
		Reads: []string{leaseAppKey},
		OptionalReads: []string{
			leaseAppKey + ".tenancy",
			leaseAppKey + ".decision",
			leaseAppKey + ".signature",
			leaseAppKey + ".decidedProfileSnapshot",
			leaseAppKey + ".profile",
			leaseAppKey + ".underwritingParties",
			leaseAppKey + ".applicationSignals",
		},
	}
}

// decide submits DecideLeaseApplication{leaseAppKey, decision} (class
// leaseapp) at the given submittedAt and asserts the outcome. unit is kept as
// a parameter so every call site still names the application's own
// appliesToUnit target for readability, but it is no longer sent — the op
// resolves the unit itself from the application's own link (scripts.go), so
// a payload unit field would only ever be a caller-supplied value the script
// ignores.
func decide(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseAppKey, decision, unit, submittedAt string, want processor.MessageOutcome) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "DecideLeaseApplication",
		Actor:         lsActorKey,
		SubmittedAt:   submittedAt,
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseAppKey + `","decision":"` + decision + `"}`),
		ContextHint:   decideReadsFor(leaseAppKey, unit),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// signLease submits SignLease{leaseAppKey} so a test can satisfy the approve-
// readiness floor (a landlord may approve only a signed application).
func signLease(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseAppKey, submittedAt string) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SignLease",
		Actor:         lsActorKey,
		SubmittedAt:   submittedAt,
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseAppKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{leaseAppKey}, OptionalReads: []string{leaseAppKey + ".decision"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

// TestDecideLeaseApplication drives the landlord decision op and its lifecycle
// guards: approving an UNSIGNED application is rejected (NotReadyToApprove); after
// signing, approve writes .decision{value:approved}; the SAME decision re-submits
// idempotently; a DIFFERENT later decision is rejected (DecisionFinal — a recorded
// decision is terminal, no silent flip); a bad enum is rejected (BadDecision); a
// tombstoned application is rejected (UnknownLeaseApplication). Root stays {} (D5).
func TestDecideLeaseApplication(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "decide")

	applicantKey := seedApplicant(t, ctx, conn, "BBdecapp1cantHJKMNPQ")
	appKey := createApplication(t, ctx, conn, cp, cons, applicantKey)
	unitKey := unitKeyFor(applicantKey)

	// Approve-readiness floor: approving an UNSIGNED application is rejected
	// (NotReadyToApprove) — the verified premature-approval bug — and writes nothing.
	decide(t, ctx, conn, cp, cons, "decideUnsign1", appKey, "approved", unitKey, "2026-06-26T09:00:00Z", processor.OutcomeRejected)
	if keyExists(t, ctx, conn, appKey+".decision") {
		t.Fatalf("a rejected premature approve must not write a .decision aspect")
	}

	// Sign the lease (the applicant's final commitment) so the approve floor is met.
	signLease(t, ctx, conn, cp, cons, "decideSign001", appKey, "2026-06-26T09:30:00Z")

	// Approve → .decision{value:approved, decidedAt} on the application; root {} (D5).
	decide(t, ctx, conn, cp, cons, "decideApprov1", appKey, "approved", unitKey, "2026-06-26T10:00:00Z", processor.OutcomeAccepted)
	ddoc := readDoc(t, ctx, conn, appKey+".decision")
	ddata, _ := ddoc["data"].(map[string]any)
	if got, _ := ddata["value"].(string); got != "approved" {
		t.Fatalf("decision.value = %q, want approved", got)
	}
	if got, _ := ddata["decidedAt"].(string); got != "2026-06-26T10:00:00Z" {
		t.Fatalf("decision.decidedAt = %q, want canonical 2026-06-26T10:00:00Z", got)
	}
	if d, _ := readDoc(t, ctx, conn, appKey)["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("application root data must stay minimal ({}) after decide, got %v", d)
	}

	// Re-submitting the SAME decision is idempotent (re-run-safe under at-least-once).
	decide(t, ctx, conn, cp, cons, "decideReappr1", appKey, "approved", unitKey, "2026-06-26T11:00:00Z", processor.OutcomeAccepted)

	// Terminal-decision guard: changing a recorded decision to a DIFFERENT value is
	// rejected (DecisionFinal) — an approved application must not silently flip — and
	// the recorded decision is unchanged.
	decide(t, ctx, conn, cp, cons, "decideFlip001", appKey, "declined", unitKey, "2026-06-26T12:00:00Z", processor.OutcomeRejected)
	ddoc = readDoc(t, ctx, conn, appKey+".decision")
	ddata, _ = ddoc["data"].(map[string]any)
	if got, _ := ddata["value"].(string); got != "approved" {
		t.Fatalf("decision.value after rejected flip = %q, want approved (unchanged)", got)
	}

	// Bad enum → BadDecision (rejected).
	decide(t, ctx, conn, cp, cons, "decideBadEnum", appKey, "maybe", unitKey, "2026-06-26T13:00:00Z", processor.OutcomeRejected)

	// Tombstoned application → UnknownLeaseApplication (rejected). Logically
	// tombstone the application, then a decision is rejected (the vertex_alive guard).
	tomb := map[string]any{"class": "leaseapp", "isDeleted": true, "data": map[string]any{}}
	tb, _ := json.Marshal(tomb)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, appKey, tb); err != nil {
		t.Fatalf("tombstone application: %v", err)
	}
	decide(t, ctx, conn, cp, cons, "decideTombsto", appKey, "approved", unitKey, "2026-06-26T14:00:00Z", processor.OutcomeRejected)
}

// decideReason submits DecideLeaseApplication{leaseAppKey, decision, reason}
// so the optional reason path can be exercised separately from the no-reason
// decide helper. unit mirrors decide's — kept for call-site readability, no
// longer sent (the op resolves it itself, scripts.go).
func decideReason(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseAppKey, decision, reason, unit, submittedAt string, want processor.MessageOutcome) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"leaseAppKey": leaseAppKey, "decision": decision, "reason": reason})
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "DecideLeaseApplication",
		Actor:         lsActorKey,
		SubmittedAt:   submittedAt,
		Class:         "leaseapp",
		Payload:       json.RawMessage(payload),
		ContextHint:   decideReadsFor(leaseAppKey, unit),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// TestDecideLeaseApplication_Reason drives the optional decline reason: a decline
// with a reason stores .decision.reason (no signature needed — the readiness floor
// applies only to approve); the decision is terminal, so a different later decision
// is rejected and the reason is preserved; a same-value reasonless re-decline
// (idempotent) clears the reason. The reason is applicant feedback + a fair-housing
// record, projected by the lens as declineReason.
func TestDecideLeaseApplication_Reason(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "decidereason")

	applicantKey := seedApplicant(t, ctx, conn, "BBdecrsn1cantHJKMNPQ")
	appKey := createApplication(t, ctx, conn, cp, cons, applicantKey)
	unitKey := unitKeyFor(applicantKey)

	// Decline with a reason → .decision{value:declined, decidedAt, reason}. A decline
	// carries no approve-readiness floor, so an unsigned application can be declined.
	const reason = "Income below the 3x-rent threshold."
	decideReason(t, ctx, conn, cp, cons, "declineRsn1", appKey, "declined", reason, unitKey, "2026-06-26T10:00:00Z", processor.OutcomeAccepted)
	ddata, _ := readDoc(t, ctx, conn, appKey+".decision")["data"].(map[string]any)
	if got, _ := ddata["value"].(string); got != "declined" {
		t.Fatalf("decision.value = %q, want declined", got)
	}
	if got, _ := ddata["reason"].(string); got != reason {
		t.Fatalf("decision.reason = %q, want %q", got, reason)
	}

	// Terminal: a DIFFERENT later decision is rejected (DecisionFinal); the decision
	// does not flip and the reason is preserved.
	decide(t, ctx, conn, cp, cons, "reasonFlip01", appKey, "approved", unitKey, "2026-06-26T11:00:00Z", processor.OutcomeRejected)
	ddata, _ = readDoc(t, ctx, conn, appKey+".decision")["data"].(map[string]any)
	if got, _ := ddata["value"].(string); got != "declined" {
		t.Fatalf("decision.value after rejected flip = %q, want declined (unchanged)", got)
	}
	if got, _ := ddata["reason"].(string); got != reason {
		t.Fatalf("decision.reason after rejected flip = %q, want preserved %q", got, reason)
	}

	// A same-value reasonless re-decline (idempotent) clears the reason — the
	// unconditioned upsert carries only what the caller supplies this time.
	decide(t, ctx, conn, cp, cons, "declineNoRsn1", appKey, "declined", unitKey, "2026-06-26T12:00:00Z", processor.OutcomeAccepted)
	ddata, _ = readDoc(t, ctx, conn, appKey+".decision")["data"].(map[string]any)
	if _, present := ddata["reason"]; present {
		t.Fatalf("a reasonless re-decline must clear the reason key, got %v", ddata["reason"])
	}
}

// setProfile submits SetApplicantProfile merging the leaseAppKey + unit into the
// supplied payload, and asserts the outcome. reads carries the (a) required
// appliesToUnit validation link; optionalReads carries the (d) unit.listing rent
// lookup — absent falls through to an unknown income-to-rent signal (scripts.go).
func setProfile(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseAppKey, unit string, payload map[string]any, want processor.MessageOutcome) {
	t.Helper()
	payload["leaseAppKey"] = leaseAppKey
	payload["unit"] = unit
	b, _ := json.Marshal(payload)
	_, appID, _ := substrate.ParseVertexKey(leaseAppKey)
	_, unitID, _ := substrate.ParseVertexKey(unit)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SetApplicantProfile",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseapp",
		Payload:       json.RawMessage(b),
		ContextHint: &processor.ContextHint{
			Reads:         []string{leaseAppKey, "lnk.leaseapp." + appID + ".appliesToUnit.unit." + unitID},
			OptionalReads: []string{unit + ".listing"},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// TestSetApplicantProfile drives the qualification-profile op through the real
// Processor: it writes THREE aspects in one batch — .profile (the applicant's
// own raw financials + the guarantor's relationship/income), .underwritingParties
// (the guarantor's / co-applicant's OWN identifiers), and .applicationSignals
// (the op-derived booleans/counts) — incomeToRentMet is computed against the
// unit's listing rent, an unconditioned re-submit overwrites all three, and a
// wrong unit is rejected (UnitMismatch).
func TestSetApplicantProfile(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "setprofile")

	applicantKey := seedApplicant(t, ctx, conn, "BBsetprof1cntHJKMNPQ")
	appKey := createApplication(t, ctx, conn, cp, cons, applicantKey)
	unitKey := "vtx.unit." + applicantKey[len("vtx.identity."):]
	// Seed a listing rent on the application's unit so incomeToRentMet derives
	// (the op kv.Reads unit.listing.data.rentAmount on demand).
	seedVertex(t, ctx, conn, unitKey+".listing", "listing", map[string]any{"rentAmount": 2000})

	// 96000/12 = 8000 monthly ≥ 3×2000 = 6000 → incomeToRentMet true.
	setProfile(t, ctx, conn, cp, cons, "prof1", appKey, unitKey, map[string]any{
		"annualIncome":          96000,
		"employmentStatus":      "employed",
		"employerName":          "Acme Corp",
		"references":            []any{"Prior landlord", "Manager"},
		"hasGuarantor":          true,
		"guarantorName":         "Pat Guarantor",
		"guarantorRelationship": "parent",
		"guarantorAnnualIncome": 120000,
	}, processor.OutcomeAccepted)
	pdata := lsDecryptAspect(t, ctx, conn, appKey+".profile")
	updata := lsDecryptAspect(t, ctx, conn, appKey+".underwritingParties")
	sdata, _ := readDoc(t, ctx, conn, appKey+".applicationSignals")["data"].(map[string]any)
	if got, _ := pdata["annualIncome"].(float64); got != 96000 {
		t.Fatalf("profile.annualIncome (raw, stored) = %v, want 96000", pdata["annualIncome"])
	}
	if got, _ := pdata["employerName"].(string); got != "Acme Corp" {
		t.Fatalf("profile.employerName = %q, want Acme Corp", got)
	}
	if got, _ := sdata["employmentVerified"].(bool); !got {
		t.Fatalf("employed ⇒ applicationSignals.employmentVerified=true, got %v", sdata["employmentVerified"])
	}
	if got, _ := sdata["referenceCount"].(float64); got != 2 {
		t.Fatalf("applicationSignals.referenceCount = %v, want 2", sdata["referenceCount"])
	}
	if got, _ := sdata["incomeToRentMet"].(bool); !got {
		t.Fatalf("8000 ≥ 3×2000 ⇒ applicationSignals.incomeToRentMet=true, got %v", sdata["incomeToRentMet"])
	}
	if got, _ := sdata["hasGuarantor"].(bool); !got {
		t.Fatalf("applicationSignals.hasGuarantor=true expected, got %v", sdata["hasGuarantor"])
	}
	if got, _ := sdata["hasCoApplicant"].(bool); got {
		t.Fatalf("applicationSignals.hasCoApplicant defaults false, got %v", sdata["hasCoApplicant"])
	}
	// The guarantor's OWN identifier is stored on .underwritingParties (never
	// projected); guarantorRelationship/guarantorAnnualIncome stay on .profile
	// (they describe the applicant's qualification story). The op derives
	// guarantorIncomeToRentMet (on .applicationSignals) from the guarantor's
	// income vs the same rent.
	if got, _ := updata["guarantorName"].(string); got != "Pat Guarantor" {
		t.Fatalf("underwritingParties.guarantorName = %q, want Pat Guarantor", got)
	}
	if got, _ := pdata["guarantorRelationship"].(string); got != "parent" {
		t.Fatalf("profile.guarantorRelationship = %q, want parent", got)
	}
	if got, _ := pdata["guarantorAnnualIncome"].(float64); got != 120000 {
		t.Fatalf("profile.guarantorAnnualIncome (raw, stored) = %v, want 120000", pdata["guarantorAnnualIncome"])
	}
	if got, _ := sdata["guarantorIncomeToRentMet"].(bool); !got {
		t.Fatalf("120000/12 = 10000 ≥ 3×2000 ⇒ applicationSignals.guarantorIncomeToRentMet=true, got %v", sdata["guarantorIncomeToRentMet"])
	}
	// The co-applicant fields are structurally absent (no co-applicant on this
	// submission), never written on underwritingParties.
	if _, present := updata["coApplicantName"]; present {
		t.Fatalf("no co-applicant ⇒ underwritingParties.coApplicantName absent, got %v", updata["coApplicantName"])
	}

	// Re-submit (unconditioned upsert): lower income, student, no employer →
	// overwrites all three aspects. 60000/12 = 5000 < 6000 → incomeToRentMet false.
	setProfile(t, ctx, conn, cp, cons, "prof2", appKey, unitKey, map[string]any{
		"annualIncome":     60000,
		"employmentStatus": "student",
	}, processor.OutcomeAccepted)
	pdata = lsDecryptAspect(t, ctx, conn, appKey+".profile")
	updata = lsDecryptAspect(t, ctx, conn, appKey+".underwritingParties")
	sdata, _ = readDoc(t, ctx, conn, appKey+".applicationSignals")["data"].(map[string]any)
	if got, _ := sdata["incomeToRentMet"].(bool); got {
		t.Fatalf("5000 < 6000 ⇒ applicationSignals.incomeToRentMet=false, got %v", sdata["incomeToRentMet"])
	}
	if got, _ := sdata["employmentVerified"].(bool); got {
		t.Fatalf("student ⇒ applicationSignals.employmentVerified=false, got %v", sdata["employmentVerified"])
	}
	if got, _ := pdata["employerName"].(string); got != "" {
		t.Fatalf("a re-submit without employerName must overwrite (clear) it to \"\" (STABLE shape), got %q", got)
	}
	if got, _ := sdata["referenceCount"].(float64); got != 0 {
		t.Fatalf("no references ⇒ applicationSignals.referenceCount=0, got %v", sdata["referenceCount"])
	}
	// The re-submit had no guarantor → the whole guarantor group is structurally
	// absent from BOTH sensitive aspects, and the derived signal is dropped too
	// (the unconditioned upsert replaces the whole aspect).
	if _, present := updata["guarantorName"]; present {
		t.Fatalf("a re-submit without a guarantor must clear underwritingParties.guarantorName, got %v", updata["guarantorName"])
	}
	if _, present := pdata["guarantorRelationship"]; present {
		t.Fatalf("a re-submit without a guarantor must clear profile.guarantorRelationship, got %v", pdata["guarantorRelationship"])
	}
	if _, present := sdata["guarantorIncomeToRentMet"]; present {
		t.Fatalf("no guarantor ⇒ applicationSignals.guarantorIncomeToRentMet absent, got %v", sdata["guarantorIncomeToRentMet"])
	}

	// A guarantor whose own income is below 3× rent → guarantorIncomeToRentMet=false
	// (the signal separates a strong guarantor from a weak one — 50000/12 = 4166 < 6000).
	setProfile(t, ctx, conn, cp, cons, "prof3g", appKey, unitKey, map[string]any{
		"annualIncome":          60000,
		"employmentStatus":      "employed",
		"hasGuarantor":          true,
		"guarantorAnnualIncome": 50000,
	}, processor.OutcomeAccepted)
	sdata, _ = readDoc(t, ctx, conn, appKey+".applicationSignals")["data"].(map[string]any)
	gv, present := sdata["guarantorIncomeToRentMet"].(bool)
	if !present || gv {
		t.Fatalf("50000/12 = 4166 < 6000 ⇒ applicationSignals.guarantorIncomeToRentMet=false (present), got present=%v val=%v", present, sdata["guarantorIncomeToRentMet"])
	}
	// hasGuarantor=true but no guarantorName supplied → underwritingParties still
	// carries the key, STABLE-shape "" (never dropped).
	updata = lsDecryptAspect(t, ctx, conn, appKey+".underwritingParties")
	if got, present := updata["guarantorName"]; !present || got != "" {
		t.Fatalf("hasGuarantor with no name ⇒ underwritingParties.guarantorName=\"\" (present), got present=%v val=%v", present, got)
	}

	// Wrong unit (alive, but not this application's appliesToUnit target) → reject.
	wrongUnit := seedUnit(t, ctx, conn, "BBwrongunitHJKMNPQRS")
	setProfile(t, ctx, conn, cp, cons, "prof3", appKey, wrongUnit, map[string]any{
		"annualIncome":     50000,
		"employmentStatus": "employed",
	}, processor.OutcomeRejected)
}

// TestSetApplicantProfile_ConsumerSelfScope exercises the consumer scope=self
// grant (permissions.go) end to end: step 3 authorizes scope=self by checking
// authContext.target == actor, but never inspects which application is named, so
// a consumer could satisfy it while profiling SOMEONE ELSE's application. The
// in-script guard (scripts.go: the applicationFor self-link keyed on the actor's
// own id) closes that gap — an applicant profiles only their own application.
// The operator path stays covered by TestSetApplicantProfile.
func TestSetApplicantProfile_ConsumerSelfScope(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "setprofile-consumer-self")

	applicant := seedApplicant(t, ctx, conn, "BBSPCSAPPHJKMNPQRSTU")
	attacker := seedApplicant(t, ctx, conn, "BBSPCSATKHJKMNPQRSTU")
	seedConsumerSelfCapDoc(t, ctx, conn, applicant)
	seedConsumerSelfCapDoc(t, ctx, conn, attacker)

	// The application (and its unit) are created FOR the applicant.
	appKey := createApplication(t, ctx, conn, cp, cons, applicant)
	unitKey := unitKeyFor(applicant)

	profile := func() map[string]any {
		return map[string]any{"annualIncome": 90000, "employmentStatus": "employed"}
	}

	// Attacker acts as themselves (authContext.target == attacker). The self-link
	// keyed on the attacker's id does not exist on the applicant's application, so
	// the guard rejects (AuthDenied); no .profile aspect is written.
	setProfileAsConsumer(t, ctx, conn, cp, cons, "spcsForge01", appKey, unitKey, attacker, profile(), processor.OutcomeRejected)
	if keyExists(t, ctx, conn, appKey+".profile") {
		t.Fatalf("a foreign-consumer SetApplicantProfile must NOT write the .profile aspect")
	}

	// The applicant profiles their OWN application → Accepted (.profile written).
	setProfileAsConsumer(t, ctx, conn, cp, cons, "spcsSelf001", appKey, unitKey, applicant, profile(), processor.OutcomeAccepted)
	pdata := lsDecryptAspect(t, ctx, conn, appKey+".profile")
	if got, _ := pdata["annualIncome"].(float64); got != 90000 {
		t.Fatalf("self SetApplicantProfile should write annualIncome=90000, got %v", pdata["annualIncome"])
	}
}

// decidedSnapshot reads back the leaseapp's .decidedProfileSnapshot aspect —
// SENSITIVE, custodied on the SAME underwritingRecord retention-class holder
// as .profile (lsDecryptAspect opens it the same way) — and returns its three
// nested data maps (each {} when the corresponding source aspect was never
// submitted).
func decidedSnapshot(t *testing.T, ctx context.Context, conn *substrate.Conn, appKey string) (profile, underwritingParties, applicationSignals map[string]any) {
	t.Helper()
	data := lsDecryptAspect(t, ctx, conn, appKey+".decidedProfileSnapshot")
	profile, _ = data["profile"].(map[string]any)
	underwritingParties, _ = data["underwritingParties"].(map[string]any)
	applicationSignals, _ = data["applicationSignals"].(map[string]any)
	return
}

// TestDecideLeaseApplication_StampsDecidedProfileSnapshotOnFirstDecisionOnly
// proves the fair-housing preservation snapshot (design §5 "Applicant-profile
// terminal state", build note 2026-08-29): DecideLeaseApplication CREATE-ONLY
// stamps .decidedProfileSnapshot on the FIRST .decision write of EITHER value
// — approve OR decline — copying the then-current .profile /
// .underwritingParties / .applicationSignals data. A DECLINE with a
// previously-submitted profile stamps it with that data present; a
// SetApplicantProfile submitted AFTER the decision (which stays freely
// re-submittable — the guard-shape falsification in design §5 means
// .decision can never confine it) overwrites the LIVE aspects but must NOT
// change the already-stamped snapshot — proven both by a direct read-back
// and by a SUBSEQUENT idempotent same-value re-decline, which must neither
// hard-reject (the create-only gate reads the snapshot's OWN key, scripts.go,
// not merely .decision) nor re-derive the snapshot from the now-changed live
// .profile; and a decision reached with no profile ever submitted still
// succeeds, stamping an empty snapshot rather than failing the decision.
func TestDecideLeaseApplication_StampsDecidedProfileSnapshotOnFirstDecisionOnly(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "decidedsnapshot")

	// (a) decline with a previously-submitted profile stamps the snapshot,
	// create-only, with the nested profile/underwritingParties/
	// applicationSignals data present.
	applicantKey := seedApplicant(t, ctx, conn, "BBdecsnap1cntHJKMNPQ")
	appKey := createApplication(t, ctx, conn, cp, cons, applicantKey)
	unitKey := unitKeyFor(applicantKey)
	seedVertex(t, ctx, conn, unitKey+".listing", "listing", map[string]any{"rentAmount": 2000})

	setProfile(t, ctx, conn, cp, cons, "decsnapProf1", appKey, unitKey, map[string]any{
		"annualIncome":     96000,
		"employmentStatus": "employed",
		"employerName":     "Acme Corp",
		"references":       []any{"Prior landlord"},
	}, processor.OutcomeAccepted)

	decideReason(t, ctx, conn, cp, cons, "decsnapDecl1", appKey, "declined",
		"Income below the 3x-rent threshold.", unitKey, "2026-06-26T10:00:00Z", processor.OutcomeAccepted)

	profile, underwritingParties, signals := decidedSnapshot(t, ctx, conn, appKey)
	if got, _ := profile["annualIncome"].(float64); got != 96000 {
		t.Fatalf("decidedProfileSnapshot.profile.annualIncome = %v, want 96000", profile["annualIncome"])
	}
	if got, _ := profile["employerName"].(string); got != "Acme Corp" {
		t.Fatalf("decidedProfileSnapshot.profile.employerName = %q, want Acme Corp", got)
	}
	if refs, _ := underwritingParties["references"].([]any); len(refs) != 1 {
		t.Fatalf("decidedProfileSnapshot.underwritingParties.references = %v, want [\"Prior landlord\"]", underwritingParties["references"])
	}
	if got, _ := signals["employmentVerified"].(bool); !got {
		t.Fatalf("decidedProfileSnapshot.applicationSignals.employmentVerified = %v, want true", signals["employmentVerified"])
	}
	if got, _ := signals["referenceCount"].(float64); got != 1 {
		t.Fatalf("decidedProfileSnapshot.applicationSignals.referenceCount = %v, want 1", signals["referenceCount"])
	}

	// (b) a SECOND SetApplicantProfile issued AFTER the decision overwrites
	// the LIVE .profile aspect (it stays an unconditioned upsert), but must
	// NOT change the already-stamped (create-only) snapshot.
	setProfile(t, ctx, conn, cp, cons, "decsnapProf2", appKey, unitKey, map[string]any{
		"annualIncome":     40000,
		"employmentStatus": "unemployed",
	}, processor.OutcomeAccepted)
	livePdata := lsDecryptAspect(t, ctx, conn, appKey+".profile")
	if got, _ := livePdata["annualIncome"].(float64); got != 40000 {
		t.Fatalf("live .profile must reflect the post-decision re-submit: annualIncome = %v, want 40000", livePdata["annualIncome"])
	}

	// A legitimate SERIAL re-decision — the SAME decision value re-submitted
	// (idempotent under the terminal-decision guard) — must neither hard-
	// reject (proving the create-only gate reads the snapshot's OWN key, not
	// just SetApplicantProfile's unrelated upsert) nor re-derive the
	// snapshot from the now-changed LIVE .profile: it must still read the
	// ORIGINAL decision-time values (96000), never the post-decision
	// re-submitted 40000.
	decideReason(t, ctx, conn, cp, cons, "decsnapDecl2", appKey, "declined",
		"Income below the 3x-rent threshold.", unitKey, "2026-06-26T11:00:00Z", processor.OutcomeAccepted)

	profile, _, signals = decidedSnapshot(t, ctx, conn, appKey)
	if got, _ := profile["annualIncome"].(float64); got != 96000 {
		t.Fatalf("decidedProfileSnapshot.profile.annualIncome must stay the DECISION-TIME value 96000 after a post-decision re-submit AND an idempotent re-decline, got %v", profile["annualIncome"])
	}
	if got, _ := signals["employmentVerified"].(bool); !got {
		t.Fatalf("decidedProfileSnapshot.applicationSignals.employmentVerified must stay the decision-time value true, got %v", signals["employmentVerified"])
	}

	// (c) a decision reached with NO profile ever submitted still succeeds
	// and stamps an EMPTY snapshot — a landlord may decide before
	// SetApplicantProfile is ever called; the decision must not fail on
	// missing profile data.
	noProfileApplicant := seedApplicant(t, ctx, conn, "BBdecsnapEmptHJKRSTU")
	noProfileAppKey := createApplication(t, ctx, conn, cp, cons, noProfileApplicant)
	noProfileUnitKey := unitKeyFor(noProfileApplicant)
	decide(t, ctx, conn, cp, cons, "decsnapEmpty1", noProfileAppKey, "declined", noProfileUnitKey, "2026-06-26T10:00:00Z", processor.OutcomeAccepted)

	emptyProfile, emptyParties, emptySignals := decidedSnapshot(t, ctx, conn, noProfileAppKey)
	if len(emptyProfile) != 0 {
		t.Fatalf("decidedProfileSnapshot.profile with no profile ever submitted must be empty, got %v", emptyProfile)
	}
	if len(emptyParties) != 0 {
		t.Fatalf("decidedProfileSnapshot.underwritingParties with no profile ever submitted must be empty, got %v", emptyParties)
	}
	if len(emptySignals) != 0 {
		t.Fatalf("decidedProfileSnapshot.applicationSignals with no profile ever submitted must be empty, got %v", emptySignals)
	}
}
