package pkgmgr

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/substrate/keys"
)

// newCapabilityApplyHarness boots an embedded NATS with nothing but the Core
// KV bucket: CapabilityApplyPlanForProposal is read-only, so the plan builder
// needs no Processor pipeline, no primordials and no installed packages —
// deliberately leaner than newInstallerHarness, which stands up the whole
// install path.
func newCapabilityApplyHarness(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	s := natsfixture.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: s.ClientURL(), Name: "pkgmgr-capabilityapply-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	if _, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         CoreBucket,
		LimitMarkerTTL: time.Second,
	}); err != nil {
		t.Fatalf("create %s bucket: %v", CoreBucket, err)
	}
	return ctx, conn
}

// guardProposalID mints a distinct 20-char NanoID-alphabet proposal id per
// table case (n < len(keys.Alphabet)).
func guardProposalID(t *testing.T, n int) string {
	t.Helper()
	if n >= len(keys.Alphabet) {
		t.Fatalf("guardProposalID: %d exceeds the single-suffix-char id space", n)
	}
	return "CAGuardHJKMNPQRSTUV" + string(keys.Alphabet[n])
}

// seedApprovedProposal writes the three aspects CapabilityApplyPlanForProposal
// reads — .review (approved), .artifact (a well-formed lens, so the artifact
// side of the plan is never what fails) and .target — straight into Core KV in
// the {isDeleted,data} envelope readAspectData expects, and returns the
// proposal key.
func seedApprovedProposal(t *testing.T, ctx context.Context, conn *substrate.Conn, proposalID, packageName, mode string) string {
	t.Helper()
	return seedApprovedProposalWithTarget(t, ctx, conn, proposalID,
		map[string]any{"packageName": packageName, "mode": mode})
}

// seedApprovedProposalWithTarget is seedApprovedProposal with the whole .target
// body supplied, for the cases whose subject is a target FIELD (newVersion,
// baseVersion) rather than the packageName/mode pair.
func seedApprovedProposalWithTarget(t *testing.T, ctx context.Context, conn *substrate.Conn, proposalID string, target map[string]any) string {
	t.Helper()
	proposalKey := "vtx.capabilityproposal." + proposalID

	content, err := json.Marshal(LensArtifactContent{
		CanonicalName: "capApplyGuardLens",
		Adapter:       "nats-kv",
		Bucket:        "cap-apply-guard",
		Spec:          "MATCH (p:provider) RETURN p.key AS key",
	})
	if err != nil {
		t.Fatalf("marshal lens content: %v", err)
	}

	aspects := map[string]map[string]any{
		proposalKey + ".review":   {"state": "approved"},
		proposalKey + ".artifact": {"kind": "lens", "content": string(content)},
		proposalKey + ".target":   target,
	}
	for key, data := range aspects {
		b, err := json.Marshal(map[string]any{"isDeleted": false, "data": data})
		if err != nil {
			t.Fatalf("marshal %s: %v", key, err)
		}
		if _, err := conn.KVPut(ctx, CoreBucket, key, b); err != nil {
			t.Fatalf("KVPut %s: %v", key, err)
		}
	}
	return proposalKey
}

// --- dispatch-side privilege gate (authored_dispatch_scope.go) --------------

// mustNanoID mints a valid 20-char Contract #1 NanoID for a seeded meta/package
// vertex.
func mustNanoID(t *testing.T) string {
	t.Helper()
	id, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("mint NanoID: %v", err)
	}
	return id
}

// putEnvelope writes a Contract #1 {class?,isDeleted,data} envelope into Core KV.
func putEnvelope(t *testing.T, ctx context.Context, conn *substrate.Conn, key string, env map[string]any) {
	t.Helper()
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal %s: %v", key, err)
	}
	if _, err := conn.KVPut(ctx, CoreBucket, key, b); err != nil {
		t.Fatalf("KVPut %s: %v", key, err)
	}
}

// seedProtectedPackageDeclaringOps installs into the catalog a PROTECTED package
// (a real platformProtectedPackages name) whose DDL declares the given
// operationTypes in its permittedCommands — exactly the shape pkgmgr's build
// produces: a package manifest whose declaredKeys names the DDL meta root and
// its .permittedCommands aspect. This is what makes those ops resolve to a
// protected declaring package.
func seedProtectedPackageDeclaringOps(t *testing.T, ctx context.Context, conn *substrate.Conn, protectedName string, ops []string) {
	t.Helper()
	if !PlatformProtectedPackage(protectedName) {
		t.Fatalf("seed package %q is not platform-protected; the test premise is wrong", protectedName)
	}
	pkgKey := "vtx.package." + mustNanoID(t)
	ddlKey := "vtx.meta." + mustNanoID(t)
	pcKey := ddlKey + ".permittedCommands"

	putEnvelope(t, ctx, conn, ddlKey, map[string]any{"class": "meta.ddl.vertexType", "isDeleted": false, "data": map[string]any{}})
	putEnvelope(t, ctx, conn, pcKey, map[string]any{"isDeleted": false, "data": map[string]any{"commands": ops}})
	putEnvelope(t, ctx, conn, pkgKey+".manifest", map[string]any{"isDeleted": false, "data": map[string]any{
		"name":         protectedName,
		"declaredKeys": []string{ddlKey, pcKey},
	}})
}

// seedProtectedPackageDeclaringOpMeta installs into the catalog a PROTECTED
// package declaring an op-meta for the given operationType — mirroring the REAL
// installer's OpMetas loop (build.go: an op-meta is a meta.ddl.vertexType root
// carrying data.operationType, with NO permittedCommands aspect). This exercises
// the second op-collection signal (the op-meta root) independently of a DDL's
// permittedCommands.
func seedProtectedPackageDeclaringOpMeta(t *testing.T, ctx context.Context, conn *substrate.Conn, protectedName, op string) {
	t.Helper()
	if !PlatformProtectedPackage(protectedName) {
		t.Fatalf("seed package %q is not platform-protected; the test premise is wrong", protectedName)
	}
	pkgKey := "vtx.package." + mustNanoID(t)
	opMetaKey := "vtx.meta." + mustNanoID(t)

	putEnvelope(t, ctx, conn, opMetaKey, map[string]any{"class": opMetaClass, "isDeleted": false, "data": map[string]any{"operationType": op}})
	putEnvelope(t, ctx, conn, pkgKey+".manifest", map[string]any{"isDeleted": false, "data": map[string]any{
		"name":         protectedName,
		"declaredKeys": []string{opMetaKey},
	}})
}

// seedProtectedPackageDeclaringPattern installs into the catalog a PROTECTED
// package declaring a loom pattern with the given patternId — mirroring the REAL
// installer exactly (build.go: a meta.loomPattern is emitted as a root vertex
// plus a `.spec` aspect ONLY, with NO `.canonicalName`; the pattern's identity
// is the `patternId` field INSIDE the .spec body, per loomPatternSpecBody).
// Both keys are named in the package manifest's declaredKeys. If the
// classification read a `.canonicalName` (the shape the earlier fixture wrongly
// wrote), this would now produce no protected pattern and the triggerLoom test
// would fail — which is what makes the test exercise the real path.
// It returns the pattern's meta-vertex NanoID (the other identity a triggerLoom
// may name it by, which the Weaver's registry also resolves).
func seedProtectedPackageDeclaringPattern(t *testing.T, ctx context.Context, conn *substrate.Conn, protectedName, patternID string) string {
	t.Helper()
	if !PlatformProtectedPackage(protectedName) {
		t.Fatalf("seed package %q is not platform-protected; the test premise is wrong", protectedName)
	}
	pkgKey := "vtx.package." + mustNanoID(t)
	patternNanoID := mustNanoID(t)
	patKey := "vtx.meta." + patternNanoID
	specKey := patKey + ".spec"

	putEnvelope(t, ctx, conn, patKey, map[string]any{"class": loomPatternClass, "isDeleted": false, "data": map[string]any{}})
	putEnvelope(t, ctx, conn, specKey, map[string]any{"isDeleted": false, "data": map[string]any{
		"patternId":   patternID,
		"subjectType": "identity",
		"steps":       []map[string]any{{"kind": "systemOp", "operation": "ShredIdentityKey"}},
	}})
	putEnvelope(t, ctx, conn, pkgKey+".manifest", map[string]any{"isDeleted": false, "data": map[string]any{
		"name":         protectedName,
		"declaredKeys": []string{patKey, specKey},
	}})
	return patternNanoID
}

// seedLensSpec writes a lens meta's .spec aspect with the given targetConfig, so
// refuseProtectedLensBinding can classify a target's lensRef. Returns the lens
// NanoID (the value a single-artifact target files as its lensRef).
func seedLensSpec(t *testing.T, ctx context.Context, conn *substrate.Conn, targetConfig map[string]any) string {
	t.Helper()
	lensID := mustNanoID(t)
	putEnvelope(t, ctx, conn, "vtx.meta."+lensID+".spec", map[string]any{"isDeleted": false, "data": map[string]any{
		"targetType":   "postgres",
		"cypherRule":   "MATCH (p:provider) RETURN p.key AS key",
		"targetConfig": targetConfig,
	}})
	return lensID
}

// seedApprovedWeaverTarget writes the three aspects CapabilityApplyPlanForProposal
// reads for a weaverTarget-kind proposal: an approved review, the weaverTarget
// artifact, and a target installing into a NON-protected fresh package (so the
// protected-PACKAGE guard passes and the dispatch-scope gate is what decides).
func seedApprovedWeaverTarget(t *testing.T, ctx context.Context, conn *substrate.Conn, proposalID string, content WeaverTargetArtifactContent) string {
	t.Helper()
	return seedApprovedWeaverTargetInto(t, ctx, conn, proposalID, content,
		map[string]any{"packageName": "ai-target-escalation", "mode": "newPackage"})
}

// seedApprovedWeaverTargetInto is seedApprovedWeaverTarget with the whole
// .target body supplied, for a case whose subject is the install MODE rather
// than the artifact.
func seedApprovedWeaverTargetInto(t *testing.T, ctx context.Context, conn *substrate.Conn, proposalID string, content WeaverTargetArtifactContent, target map[string]any) string {
	t.Helper()
	proposalKey := "vtx.capabilityproposal." + proposalID
	body, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal weaverTarget content: %v", err)
	}
	aspects := map[string]map[string]any{
		proposalKey + ".review":   {"state": "approved"},
		proposalKey + ".artifact": {"kind": "weaverTarget", "content": string(body)},
		proposalKey + ".target":   target,
	}
	for key, data := range aspects {
		putEnvelope(t, ctx, conn, key, map[string]any{"isDeleted": false, "data": data})
	}
	return proposalKey
}

// TestCapabilityApplyPlan_AuthoredDirectOpEscalation_Rejected is the headline
// containment: an approved authored target whose directOp gap names a protected
// operation (AssignRole) is refused at the plan builder — the escalation path
// an authored apply-side target opens if left unchecked. The package it
// installs is benign (ai-target-*, not protected), so only the dispatch-scope
// gate can catch it.
func TestCapabilityApplyPlan_AuthoredDirectOpEscalation_Rejected(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)
	seedProtectedPackageDeclaringOps(t, ctx, conn, "rbac-domain",
		[]string{"AssignRole", "GrantPermission", "CreatePermission", "TombstoneRole", "RevokePermission"})

	for n, op := range []string{"AssignRole", "GrantPermission", "CreatePermission", "TombstoneRole", "RevokePermission"} {
		t.Run(op, func(t *testing.T) {
			content := WeaverTargetArtifactContent{
				TargetID: "escalate",
				LensRef:  "someLensRefNanoId345",
				Gaps: map[string]GapActionArtifact{
					"missing_grant": {Action: "directOp", Operation: op,
						Params: map[string]string{"actorKey": "vtx.identity.attacker", "roleKey": "vtx.role.operator"}},
				},
			}
			proposalKey := seedApprovedWeaverTarget(t, ctx, conn, guardProposalID(t, n), content)
			plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
			if err == nil {
				t.Fatalf("directOp %s returned plan %+v, want a protected-dispatch refusal", op, plan)
			}
			if !strings.Contains(err.Error(), "platform-protected") || !strings.Contains(err.Error(), op) {
				t.Fatalf("error = %v, want a platform-protected refusal naming %q", err, op)
			}
		})
	}
}

// TestCapabilityMaterializedWeaverTarget_DeclaresFourKeys pins the key shape a
// single-weaverTarget package installs with — the premise the whole
// edit-an-installed-target path rests on.
//
// internal/bridge's capabilityAuthor adapter admits an edit only when the
// owning package's declaredKeys are covered by the Definition ONE weaver
// target materialises, and it computes that from this exact list: the meta
// root, its `.spec`, its `.description` (emitted only when non-empty), and the
// package vertex — whose id is derived from the package NAME, so every apply of
// the package re-emits it rather than removing it. The `.manifest` aspect is
// deliberately absent: build.go snapshots declaredKeys before writing it.
//
// A fifth key appearing here would make every console-authored target
// un-editable (the bridge refuses what it cannot cover) and a key disappearing
// would make an edit's apply refuse for a reason the operator was never shown.
// Either is a change worth seeing, so it is pinned rather than derived twice.
func TestCapabilityMaterializedWeaverTarget_DeclaresFourKeys(t *testing.T) {
	const packageName = "weaver-target-coverage-7f2a"
	def, err := DefinitionForCapabilityArtifact("weaverTarget", json.RawMessage(
		`{"targetId":"coveragePin","lensRef":"AbCdEfGhJkLmNpQrStUv","description":"pinned",`+
			`"gaps":{"missing_x":{"action":"directOp","operation":"SendReminder"}}}`), packageName, "0.1.0")
	if err != nil {
		t.Fatalf("DefinitionForCapabilityArtifact: %v", err)
	}

	pkgKey := PackageVertexPrefix + entityNanoID(packageName, "package")
	metaKey := metaVertexPrefix + entityNanoID(packageName, "weaverTarget:coveragePin")
	ops, _, err := (&Installer{}).buildInstallBatch(def, pkgKey, nil, nil, nil, nil,
		[]string{entityNanoID(packageName, "weaverTarget:coveragePin")}, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildInstallBatch: %v", err)
	}

	var declared []string
	for _, op := range ops {
		if op.Key != pkgKey+".manifest" {
			continue
		}
		data, ok := op.Document["data"].(map[string]any)
		if !ok {
			t.Fatalf("manifest document has no data object: %+v", op.Document)
		}
		raw, ok := data["declaredKeys"].([]string)
		if !ok {
			t.Fatalf("manifest declaredKeys is %T, want []string", data["declaredKeys"])
		}
		declared = raw
	}
	if declared == nil {
		t.Fatal("the install batch wrote no package manifest aspect")
	}

	want := []string{metaKey, metaKey + ".spec", metaKey + ".description", pkgKey}
	if !slices.Equal(declared, want) {
		t.Fatalf("declaredKeys = %v,\nwant %v — internal/bridge's edit-coverage test admits exactly this set", declared, want)
	}
}

// TestCapabilityApplyPlan_UpgradeExisting_DispatchScopeStillBites proves the
// dispatch-side privilege gate is MODE-AGNOSTIC: it runs after the newPackage /
// upgradeExisting switch, on the Definition either mode materialises, so an
// EDIT of an installed target can no more dispatch a platform-protected
// operation than a freshly authored one.
//
// The whole fixture is built so that only that gate can be what refuses: the
// package is installed, not platform-protected, and its version preconditions
// (baseVersion equal to installed, newVersion moved) all pass. The admitted
// twin — the same edit with a business op — is what proves it.
func TestCapabilityApplyPlan_UpgradeExisting_DispatchScopeStillBites(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)
	seedProtectedPackageDeclaringOps(t, ctx, conn, "rbac-domain", []string{"AssignRole"})

	const packageName = "weaver-target-escalate-7f2a"
	const installedVersion = "0.3.4"
	seedInstalledManifest(t, ctx, conn, packageName, installedVersion)

	editTarget := func() map[string]any {
		return map[string]any{
			"packageName": packageName,
			"mode":        "upgradeExisting",
			"baseVersion": installedVersion,
			"newVersion":  "0.3.5",
		}
	}

	escalating := WeaverTargetArtifactContent{
		TargetID: "escalate",
		LensRef:  "someLensRefNanoId345",
		Gaps: map[string]GapActionArtifact{
			"missing_grant": {Action: "directOp", Operation: "AssignRole",
				Params: map[string]string{"actorKey": "vtx.identity.attacker", "roleKey": "vtx.role.operator"}},
		},
	}
	proposalKey := seedApprovedWeaverTargetInto(t, ctx, conn, "CAEditScopeHJKMNPQR1", escalating, editTarget())
	plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err == nil {
		t.Fatalf("an upgradeExisting directOp on AssignRole returned plan %+v, want a protected-dispatch refusal", plan)
	}
	if !strings.Contains(err.Error(), "platform-protected") || !strings.Contains(err.Error(), "AssignRole") {
		t.Fatalf("error = %v, want a platform-protected refusal naming AssignRole", err)
	}

	benign := escalating
	benign.Gaps = map[string]GapActionArtifact{
		"missing_reminder": {Action: "directOp", Operation: "SendReminder"},
	}
	okKey := seedApprovedWeaverTargetInto(t, ctx, conn, "CAEditScopeHJKMNPQR2", benign, editTarget())
	okPlan, err := CapabilityApplyPlanForProposal(ctx, conn, okKey)
	if err != nil {
		t.Fatalf("an upgradeExisting edit dispatching a business op must build a plan, got %v", err)
	}
	if okPlan.mode != "upgradeExisting" {
		t.Fatalf("plan.mode = %q, want upgradeExisting", okPlan.mode)
	}
}

// TestCapabilityApplyPlan_AuthoredDirectOp_ClassifiedViaOpMeta proves the
// second op-collection signal against its real shape: an op declared only by a
// protected op-meta root (data.operationType, no permittedCommands) is still
// refused, so an op-meta whose admitting DDL lives elsewhere cannot slip past.
func TestCapabilityApplyPlan_AuthoredDirectOp_ClassifiedViaOpMeta(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)
	seedProtectedPackageDeclaringOpMeta(t, ctx, conn, "identity-domain", "RecordIdentityPII")

	content := WeaverTargetArtifactContent{
		TargetID: "escalate",
		LensRef:  "someLensRefNanoId345",
		Gaps: map[string]GapActionArtifact{
			"missing_grant": {Action: "directOp", Operation: "RecordIdentityPII"},
		},
	}
	proposalKey := seedApprovedWeaverTarget(t, ctx, conn, "CAOpMetaHJKMNPQRST12", content)
	plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err == nil {
		t.Fatalf("directOp on an op-meta-declared protected op returned plan %+v, want a refusal", plan)
	}
	if !strings.Contains(err.Error(), "platform-protected") || !strings.Contains(err.Error(), "RecordIdentityPII") {
		t.Fatalf("error = %v, want a platform-protected refusal naming RecordIdentityPII", err)
	}
}

// TestCapabilityApplyPlan_AuthoredTriggerLoomByNanoID_Rejected proves the OTHER
// pattern identity: a triggerLoom naming the pattern by its meta-vertex NanoID
// (not its patternId) is refused too, matching the Weaver's dual index.
func TestCapabilityApplyPlan_AuthoredTriggerLoomByNanoID_Rejected(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)
	patternNanoID := seedProtectedPackageDeclaringPattern(t, ctx, conn, "identity-hygiene", "identityErasure")

	content := WeaverTargetArtifactContent{
		TargetID: "escalate",
		LensRef:  "someLensRefNanoId345",
		Gaps: map[string]GapActionArtifact{
			"missing_erasure": {Action: "triggerLoom", Pattern: patternNanoID, Subject: "row.key"},
		},
	}
	proposalKey := seedApprovedWeaverTarget(t, ctx, conn, "CALoomNanoHJKMNPQR12", content)
	plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err == nil {
		t.Fatalf("triggerLoom by pattern NanoID returned plan %+v, want a refusal", plan)
	}
	if !strings.Contains(err.Error(), "platform-protected") || !strings.Contains(err.Error(), patternNanoID) {
		t.Fatalf("error = %v, want a refusal naming the pattern NanoID %q", err, patternNanoID)
	}
}

// TestCapabilityApplyPlan_AuthoredAssignTaskEscalation_Rejected: the same class
// through assignTask, whose Operation is dispatched as a CreateTask forOperation
// — an arbitrary privileged op reached one indirection over.
func TestCapabilityApplyPlan_AuthoredAssignTaskEscalation_Rejected(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)
	seedProtectedPackageDeclaringOps(t, ctx, conn, "rbac-domain", []string{"AssignRole"})

	content := WeaverTargetArtifactContent{
		TargetID: "escalate",
		LensRef:  "someLensRefNanoId345",
		Gaps: map[string]GapActionArtifact{
			"missing_grant": {Action: "assignTask", Operation: "AssignRole", Assignee: "vtx.identity.attacker", Target: "vtx.role.operator"},
		},
	}
	proposalKey := seedApprovedWeaverTarget(t, ctx, conn, "CAEscAssignHJKMNPQR1", content)
	plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err == nil {
		t.Fatalf("assignTask AssignRole returned plan %+v, want a protected-dispatch refusal", plan)
	}
	if !strings.Contains(err.Error(), "platform-protected") || !strings.Contains(err.Error(), "AssignRole") {
		t.Fatalf("error = %v, want a platform-protected refusal naming AssignRole", err)
	}
}

// TestCapabilityApplyPlan_AuthoredTriggerLoomEscalation_Rejected: a triggerLoom
// gap naming a pattern a protected package declares (e.g. an identityErasure
// mass-shred) is refused — the pattern is resolved to its declaring package the
// same way an operation is.
func TestCapabilityApplyPlan_AuthoredTriggerLoomEscalation_Rejected(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)
	seedProtectedPackageDeclaringPattern(t, ctx, conn, "identity-hygiene", "identityErasure")

	content := WeaverTargetArtifactContent{
		TargetID: "escalate",
		LensRef:  "someLensRefNanoId345",
		Gaps: map[string]GapActionArtifact{
			"missing_erasure": {Action: "triggerLoom", Pattern: "identityErasure", Subject: "row.key"},
		},
	}
	proposalKey := seedApprovedWeaverTarget(t, ctx, conn, "CAEscLoomHJKMNPQRST1", content)
	plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err == nil {
		t.Fatalf("triggerLoom identityErasure returned plan %+v, want a protected-dispatch refusal", plan)
	}
	if !strings.Contains(err.Error(), "platform-protected") || !strings.Contains(err.Error(), "identityErasure") {
		t.Fatalf("error = %v, want a platform-protected refusal naming identityErasure", err)
	}
}

// TestCapabilityApplyPlan_AuthoredBusinessOp_Unaffected is the differential that
// proves the OP NAME is what bites: the identical target shape, with a
// business-domain op that no protected package declares, builds a real plan.
// (Disabling the deny — the coordinator's mutation check — makes the escalation
// tests above go green while this one is unchanged, isolating the gate.)
func TestCapabilityApplyPlan_AuthoredBusinessOp_Unaffected(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)
	// rbac-domain declares AssignRole; a cafe op it does NOT declare is business.
	seedProtectedPackageDeclaringOps(t, ctx, conn, "rbac-domain", []string{"AssignRole"})

	content := WeaverTargetArtifactContent{
		TargetID: "remind",
		Gaps: map[string]GapActionArtifact{
			"missing_reminder": {Action: "directOp", Operation: "SendCafeReminder"},
		},
	}
	proposalKey := seedApprovedWeaverTarget(t, ctx, conn, "CABizOpHJKMNPQRSTUV1", content)
	plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err != nil {
		t.Fatalf("business-domain directOp: want a buildable plan, got %v", err)
	}
	if plan.PackageName != "ai-target-escalation" {
		t.Fatalf("plan.PackageName = %q, want ai-target-escalation", plan.PackageName)
	}
	if len(plan.definition.WeaverTargets) != 1 {
		t.Fatalf("plan defines %d weaver targets, want 1", len(plan.definition.WeaverTargets))
	}
}

// TestCapabilityApplyPlan_AuthoredProtectedLensBinding_Rejected pins the
// read-amplification half: a target bound to a protected-posture or
// secure-column lens is refused, even when its gap op is benign.
func TestCapabilityApplyPlan_AuthoredProtectedLensBinding_Rejected(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)

	for n, tc := range []struct {
		name         string
		targetConfig map[string]any
	}{
		{"protected", map[string]any{"protected": true, "table": "roles"}},
		{"secureColumns", map[string]any{"table": "pii", "secureColumns": []map[string]any{{"column": "ssn", "holderTypes": []string{"clinician"}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lensID := seedLensSpec(t, ctx, conn, tc.targetConfig)
			content := WeaverTargetArtifactContent{
				TargetID: "read-amplify",
				LensRef:  lensID,
				Gaps: map[string]GapActionArtifact{
					"missing_thing": {Action: "directOp", Operation: "SendCafeReminder"},
				},
			}
			proposalKey := seedApprovedWeaverTarget(t, ctx, conn, guardProposalID(t, n), content)
			plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
			if err == nil {
				t.Fatalf("binding to a %s lens returned plan %+v, want a refusal", tc.name, plan)
			}
			if !strings.Contains(err.Error(), "secure") || !strings.Contains(err.Error(), lensID) {
				t.Fatalf("error = %v, want a protected/secure-lens refusal naming %q", err, lensID)
			}
		})
	}
}

// TestCapabilityApplyPlan_AuthoredNonProtectedLensBinding_Unaffected: a target
// bound to an ordinary lens (no protected posture, no secure columns) builds.
func TestCapabilityApplyPlan_AuthoredNonProtectedLensBinding_Unaffected(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)
	lensID := seedLensSpec(t, ctx, conn, map[string]any{"bucket": "onboarding", "key": []string{"key"}})

	content := WeaverTargetArtifactContent{
		TargetID: "ok",
		LensRef:  lensID,
		Gaps: map[string]GapActionArtifact{
			"missing_thing": {Action: "directOp", Operation: "SendCafeReminder"},
		},
	}
	proposalKey := seedApprovedWeaverTarget(t, ctx, conn, "CAOkLensHJKMNPQRSTU1", content)
	if _, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey); err != nil {
		t.Fatalf("binding to an ordinary lens: want a buildable plan, got %v", err)
	}
}

// TestCapabilityApplyPlan_AuthoredProposedOp_Rejected closes the proposedOp
// bypass: its op is sourced from the violation ROW (ga.Operation is empty), so
// it escapes the static operationType classification entirely. An authored
// target may not emit it at all — a total bar, no catalog needed. (No protected
// package is even seeded here: the refusal must not depend on classification.)
func TestCapabilityApplyPlan_AuthoredProposedOp_Rejected(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)

	content := WeaverTargetArtifactContent{
		TargetID: "recurse",
		LensRef:  "someLensRefNanoId345",
		Gaps: map[string]GapActionArtifact{
			"missing_thing": {Action: "proposedOp"},
		},
	}
	proposalKey := seedApprovedWeaverTarget(t, ctx, conn, "CAPropOpHJKMNPQRSTU1", content)
	plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err == nil {
		t.Fatalf("proposedOp gap returned plan %+v, want an outright refusal", plan)
	}
	if !strings.Contains(err.Error(), "proposedOp") {
		t.Fatalf("error = %v, want a refusal naming the proposedOp action", err)
	}
}

// TestCapabilityApplyPlan_AuthoredUnknownAction_Rejected pins the default-deny
// tail: record-time's known-action check is client-bypassable, so an action
// outside the vocabulary reaching apply is refused, never dispatched. Empty is
// covered by the same tail (an authored artifact cannot goal-author).
func TestCapabilityApplyPlan_AuthoredUnknownAction_Rejected(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)

	for n, action := range []string{"frobnicate", ""} {
		action := action
		t.Run("action="+action, func(t *testing.T) {
			content := WeaverTargetArtifactContent{
				TargetID: "weird",
				LensRef:  "someLensRefNanoId345",
				Gaps: map[string]GapActionArtifact{
					"missing_thing": {Action: action, Operation: "SendCafeReminder"},
				},
			}
			proposalKey := seedApprovedWeaverTarget(t, ctx, conn, guardProposalID(t, n), content)
			plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
			if err == nil {
				t.Fatalf("action %q returned plan %+v, want a default-deny refusal", action, plan)
			}
			if !strings.Contains(err.Error(), "not a dispatch-safe action") {
				t.Fatalf("error = %v, want a default-deny refusal", err)
			}
		})
	}
}

// TestCapabilityApplyPlan_AuthoredSurfaceGap_Unaffected proves surface is inert
// and allowed: it raises a Health-KV issue and dispatches no operation
// (internal/weaver/evaluator.go), so it carries none of the Weaver's authority
// and the default-deny tail must not catch it.
func TestCapabilityApplyPlan_AuthoredSurfaceGap_Unaffected(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)

	content := WeaverTargetArtifactContent{
		TargetID: "surfaced",
		Gaps: map[string]GapActionArtifact{
			"missing_thing": {Action: "surface", IssueCode: "OnboardingStalled", IssueSeverity: "warning"},
		},
	}
	proposalKey := seedApprovedWeaverTarget(t, ctx, conn, "CASurfaceHJKMNPQRST1", content)
	if _, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey); err != nil {
		t.Fatalf("surface gap (inert): want a buildable plan, got %v", err)
	}
}

// TestCapabilityApplyPlan_PlatformProtectedPackage_Rejected: every
// platform-protected package name is refused in BOTH modes, on an otherwise
// perfectly applicable approved proposal (approved review, well-formed lens
// artifact). The refusal must not depend on the live install catalog — none of
// these packages is installed in this harness, so an upgradeExisting case that
// slipped past the guard would fail with the "no package by that name is
// installed" message instead, which the assertion rejects.
func TestCapabilityApplyPlan_PlatformProtectedPackage_Rejected(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)

	protected := make([]string, 0, len(platformProtectedPackages))
	for name := range platformProtectedPackages {
		protected = append(protected, name)
	}
	sort.Strings(protected)

	n := 0
	for _, mode := range []string{"newPackage", "upgradeExisting"} {
		for _, packageName := range protected {
			proposalID := guardProposalID(t, n)
			n++
			t.Run(mode+"/"+packageName, func(t *testing.T) {
				proposalKey := seedApprovedProposal(t, ctx, conn, proposalID, packageName, mode)

				plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
				if err == nil {
					t.Fatalf("CapabilityApplyPlanForProposal(%s as %s) returned plan %+v, want a platform-protected refusal", packageName, mode, plan)
				}
				if !strings.Contains(err.Error(), "platform-protected") || !strings.Contains(err.Error(), packageName) {
					t.Fatalf("error = %v, want one naming %q as platform-protected", err, packageName)
				}
			})
		}
	}
}

// TestPlatformProtectedPackage_Normalizes: the deny-list is consulted through
// a normalizing predicate, so a near-miss spelling of a protected name cannot
// walk past an exact-byte map lookup — which would land an AI-authored package
// under a name indistinguishable from the real one in Loupe's package list.
// Normalization is case + surrounding whitespace ONLY: it must never widen
// into substring or prefix matching, or a legitimately distinct package whose
// name merely extends a protected one would be refused.
func TestPlatformProtectedPackage_Normalizes(t *testing.T) {
	protected := []string{
		"rbac-domain",
		"Rbac-Domain",
		"RBAC-DOMAIN",
		" rbac-domain ",
		"rbac-domain\t",
		"\n identity-hygiene ",
		"Demo-Operator",
	}
	for _, name := range protected {
		if !PlatformProtectedPackage(name) {
			t.Errorf("PlatformProtectedPackage(%q) = false, want true", name)
		}
	}

	allowed := []string{
		"rbac-domain-extended",
		"my-rbac-domain",
		"rbac",
		"cafe-domain",
		"",
	}
	for _, name := range allowed {
		if PlatformProtectedPackage(name) {
			t.Errorf("PlatformProtectedPackage(%q) = true, want false — the guard must not over-match", name)
		}
	}
}

// TestCapabilityApplyPlan_NearMissProtectedName_Rejected: the normalization is
// wired into the plan builder's own check, not just the exported predicate — a
// proposal declaring a whitespace/case variant of a protected name is refused
// there too, in the mode (newPackage on a name nothing has installed) that
// would otherwise sail through the live-catalog binding and build a Definition.
func TestCapabilityApplyPlan_NearMissProtectedName_Rejected(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)

	for n, packageName := range []string{"Rbac-Domain", " rbac-domain ", "rbac-domain\t"} {
		t.Run(packageName, func(t *testing.T) {
			proposalKey := seedApprovedProposal(t, ctx, conn, guardProposalID(t, n), packageName, "newPackage")

			plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
			if err == nil {
				t.Fatalf("CapabilityApplyPlanForProposal(%q) returned plan %+v, want a platform-protected refusal", packageName, plan)
			}
			if !strings.Contains(err.Error(), "platform-protected") {
				t.Fatalf("error = %v, want a platform-protected refusal", err)
			}
		})
	}
}

// TestCapabilityApplyPlan_ProtectedPrefixName_Unaffected: a genuinely distinct
// package whose name merely extends a protected one still builds — the guard
// is an exact (normalized) name match, never a prefix one.
func TestCapabilityApplyPlan_ProtectedPrefixName_Unaffected(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)

	proposalKey := seedApprovedProposal(t, ctx, conn, "CAGuardExtendedHJKMN", "rbac-domain-extended", "newPackage")
	plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err != nil {
		t.Fatalf("CapabilityApplyPlanForProposal(rbac-domain-extended): %v", err)
	}
	if plan.PackageName != "rbac-domain-extended" {
		t.Fatalf("plan.PackageName = %q, want rbac-domain-extended", plan.PackageName)
	}
}

// TestCapabilityApplyPlan_VerticalPackage_Unaffected: a vertical
// business-domain name is NOT on the deny-list — the plan builds through to a
// real Definition (newPackage on a name nothing has installed), which is
// exactly the case this guard must leave working.
func TestCapabilityApplyPlan_VerticalPackage_Unaffected(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)

	proposalKey := seedApprovedProposal(t, ctx, conn, "CAGuardVerticalHJKMN", "cafe-domain", "newPackage")
	plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err != nil {
		t.Fatalf("CapabilityApplyPlanForProposal(cafe-domain): %v", err)
	}
	if plan.PackageName != "cafe-domain" {
		t.Fatalf("plan.PackageName = %q, want cafe-domain", plan.PackageName)
	}
	if len(plan.definition.Lenses) != 1 {
		t.Fatalf("plan.definition.Lenses = %d, want 1", len(plan.definition.Lenses))
	}
}

// seedInstalledManifest writes the one aspect findInstalledPackage matches on:
// a live vtx.package.<id>.manifest carrying name + version. Enough for the
// version preconditions, which read the installed version and nothing else.
func seedInstalledManifest(t *testing.T, ctx context.Context, conn *substrate.Conn, name, version string) {
	t.Helper()
	pkgKey := PackageVertexPrefix + entityNanoID(name, "package")
	putEnvelope(t, ctx, conn, pkgKey, map[string]any{"class": "package", "isDeleted": false, "data": map[string]any{}})
	putEnvelope(t, ctx, conn, pkgKey+".manifest", map[string]any{"isDeleted": false, "data": map[string]any{
		"name": name, "version": version, "declaredKeys": []string{pkgKey, pkgKey + ".manifest"},
	}})
}

// TestCapabilityApplyPlan_UpgradeExisting_RequiresMovedVersion covers the three
// preconditions an upgradeExisting proposal must satisfy, plus the admitted
// case that proves each refusal is about the field it names rather than about
// the fixture being unbuildable for some other reason.
//
// Each refusal reads a field the proposal already carries, so none of them
// needs a new op field: the shared newVersion default is meaningful only for a
// package that does not exist yet; a newVersion equal to the installed version
// leaves "installed at newVersion" meaning both "this apply committed" and "it
// never ran", which is the discriminator the console's recovery branch rests
// on; and a baseVersion that is absent or no longer installed is a stale apply
// whose described artifacts overwrite whatever moved since.
func TestCapabilityApplyPlan_UpgradeExisting_RequiresMovedVersion(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)

	const packageName = "cap-version-guard-pkg"
	const installedVersion = "1.1.0"
	seedInstalledManifest(t, ctx, conn, packageName, installedVersion)

	cases := []struct {
		name        string
		newVersion  string
		baseVersion string
		wantField   string // "" → the admitted case
	}{
		// Each wantField is a clause unique to ITS refusal. Matching on the bare
		// field name would let a row pass on its neighbour's message — both
		// version rules name "baseVersion", so a disabled absent-check would go
		// green on the mismatch-check's wording.
		{"newVersion absent", "", installedVersion, "no target.newVersion"},
		{"newVersion equals installed", installedVersion, installedVersion, "which is the version already installed"},
		{"newVersion equals installed modulo whitespace", " " + installedVersion + " ", installedVersion, "which is the version already installed"},
		{"baseVersion absent", "1.2.0", "", "no target.baseVersion"},
		{"baseVersion not installed", "1.2.0", "1.0.0", "no longer there"},
		{"baseVersion matches modulo whitespace", "1.2.0", " " + installedVersion + " ", ""},
		{"admitted", "1.2.0", installedVersion, ""},
	}
	for n, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proposalKey := seedApprovedProposalWithTarget(t, ctx, conn, guardProposalID(t, n), map[string]any{
				"packageName": packageName,
				"mode":        "upgradeExisting",
				"newVersion":  tc.newVersion,
				"baseVersion": tc.baseVersion,
			})

			plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
			if tc.wantField == "" {
				if err != nil {
					t.Fatalf("a well-formed upgradeExisting proposal must build a plan, got %v", err)
				}
				if plan.mode != "upgradeExisting" {
					t.Fatalf("plan.mode = %q, want upgradeExisting — ApplyCapabilityPlan resolves RequireInstalled from it", plan.mode)
				}
				return
			}
			if err == nil {
				t.Fatalf("want a refusal naming %s, got plan %+v", tc.wantField, plan)
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Fatalf("refusal must name the offending field %q, got %v", tc.wantField, err)
			}
		})
	}
}

// TestApplyCapabilityPlan_SetsRefuseRemovals is what makes the whole design
// falsifiable: a real upgradeExisting proposal at a real multi-entity package,
// applied the sanctioned way, refuses instead of retiring every key the
// proposal's one-lens artifact does not describe. It drives ApplyCapabilityPlan
// rather than asserting over the options it builds, so deleting the guard from
// Apply — or the option from ApplyCapabilityPlan — turns this red.
func TestApplyCapabilityPlan_SetsRefuseRemovals(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	installed := removalFixtureDef("0.1.0")
	if _, err := inst.Install(ctx, installed); err != nil {
		t.Fatalf("Install: %v", err)
	}
	proposalKey := seedApprovedProposalWithTarget(t, ctx, conn, "CAApplyUpgradeHJKMN", map[string]any{
		"packageName": installed.Name,
		"mode":        "upgradeExisting",
		"newVersion":  "0.2.0",
		"baseVersion": "0.1.0",
	})
	plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err != nil {
		t.Fatalf("CapabilityApplyPlanForProposal: %v", err)
	}
	before := coreKVSnapshot(t, ctx, conn)

	res, err := inst.ApplyCapabilityPlan(ctx, plan)
	if err == nil {
		t.Fatalf("a one-artifact proposal applied over a multi-entity package must refuse, got: %+v", res)
	}
	var refusal *ApplyWouldRemoveError
	if !errors.As(err, &refusal) {
		t.Fatalf("want *ApplyWouldRemoveError, got %T (%v)", err, err)
	}
	if refusal.PackageName != installed.Name {
		t.Errorf("PackageName = %q, want %q", refusal.PackageName, installed.Name)
	}
	if len(refusal.UndescribedKeys) == 0 {
		t.Fatal("a refusal with an empty RemovedKeys names nothing an operator can act on")
	}
	if after := coreKVSnapshot(t, ctx, conn); !maps.Equal(before, after) {
		t.Fatal("the refused capability apply must commit nothing")
	}
}

// TestApplyCapabilityPlan_NewPackageRaceRefuses covers why RefuseRemovals is
// set on the newPackage arm too, where it is otherwise inert. The plan builder
// established the name was free; by the time the apply runs it is taken, so
// Apply dispatches on install state and takes the IN-PLACE branch — and a
// newPackage Definition describes none of the occupant's keys. Without the
// option the apply would tombstone a package it has never seen.
//
// The occupant is installed at a DIFFERENT version here, which is what routes
// the race into the delta and therefore into this refusal. Its same-version
// twin never computes a delta at all and is caught by a separate post-condition
// — see TestApplyCapabilityPlan_NewPackageRaceAtSameVersionIsNotSuccess, which
// covers that half.
func TestApplyCapabilityPlan_NewPackageRaceRefuses(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	const racedName = "cap-race-target-pkg"
	proposalKey := seedApprovedProposal(t, ctx, conn, "CAApplyRaceHJKMNPQ", racedName, "newPackage")
	plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err != nil {
		t.Fatalf("CapabilityApplyPlanForProposal (name free): %v", err)
	}

	// The name is claimed between the plan build and the apply.
	occupant := removalFixtureDef("0.2.0")
	occupant.Name = racedName
	if _, err := inst.Install(ctx, occupant); err != nil {
		t.Fatalf("install the racing occupant: %v", err)
	}
	before := coreKVSnapshot(t, ctx, conn)

	res, err := inst.ApplyCapabilityPlan(ctx, plan)
	if err == nil {
		t.Fatalf("a newPackage plan landing on an occupied name must refuse, got: %+v", res)
	}
	if !errors.Is(err, ErrApplyWouldRemove) {
		t.Fatalf("want ErrApplyWouldRemove, got %v", err)
	}
	if after := coreKVSnapshot(t, ctx, conn); !maps.Equal(before, after) {
		t.Fatal("the occupant's keys must be untouched by the refused race")
	}
}

// TestApplyCapabilityPlan_NewPackageRaceAtSameVersionIsNotSuccess covers the
// half of the newPackage race that never reaches a delta, and therefore never
// reaches the removal refusal: the occupant holds the name at the SAME version
// the plan carries, so Apply's same-version branch returns Action "skip" with a
// nil error before anything is computed.
//
// Nothing is destroyed on that path, which is exactly why it needs pinning: the
// caller reads a nil error and goes on to submit
// MarkCapabilityProposalApplied, closing the proposal over an artifact that was
// never installed. A skip is a truthful answer to "did this apply change
// anything" and a false answer to "did this proposal land", and only the second
// question is the one being asked here.
func TestApplyCapabilityPlan_NewPackageRaceAtSameVersionIsNotSuccess(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	const racedName = "cap-race-samever-pkg"
	proposalKey := seedApprovedProposal(t, ctx, conn, "CAApplySameVerHJKMN", racedName, "newPackage")
	plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err != nil {
		t.Fatalf("CapabilityApplyPlanForProposal (name free): %v", err)
	}

	// The occupant claims the name at the version the plan itself carries — a
	// newPackage proposal declaring no newVersion takes the shared 0.1.0
	// default, so this is the version an unlucky race actually collides on.
	occupant := removalFixtureDef("0.1.0")
	occupant.Name = racedName
	if _, err := inst.Install(ctx, occupant); err != nil {
		t.Fatalf("install the racing occupant: %v", err)
	}
	before := coreKVSnapshot(t, ctx, conn)

	res, err := inst.ApplyCapabilityPlan(ctx, plan)
	if err == nil {
		t.Fatalf("an apply that installed nothing must not be reported as success: %+v", res)
	}
	if !errors.Is(err, ErrPackageNameClaimed) {
		t.Fatalf("want ErrPackageNameClaimed, got %v", err)
	}
	if res != nil {
		t.Fatalf("a caller reading Action/PackageKey off this result would record an install that never happened, got %+v", res)
	}
	if !strings.Contains(err.Error(), racedName) {
		t.Errorf("the refusal must name the claimed package, got %v", err)
	}

	// The occupant is somebody else's package, and this apply neither wrote to
	// it nor took anything from it.
	if after := coreKVSnapshot(t, ctx, conn); !maps.Equal(before, after) {
		t.Fatal("the occupant's keys must be untouched by the refused race")
	}
}

// TestApplyCapabilityPlan_UpgradeRequiresTheBaseStillInstalled pins
// RequireInstalled, which nothing else does: the option's other stated effect
// (defeating Apply's same-version skip) is unreachable through this entry point,
// because the plan builder refuses a newVersion equal to the installed version
// before a plan carrying one can exist. So this is the only observable
// consequence, and without it the option is unfalsifiable.
//
// The package is uninstalled between the plan build and the apply. The honest
// answer is that the base is gone; the answer without RequireInstalled is a
// fresh install landing on the uninstall's own tombstones, which fails later and
// says something else entirely.
func TestApplyCapabilityPlan_UpgradeRequiresTheBaseStillInstalled(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	installed := removalFixtureDef("0.1.0")
	if _, err := inst.Install(ctx, installed); err != nil {
		t.Fatalf("Install: %v", err)
	}
	proposalKey := seedApprovedProposalWithTarget(t, ctx, conn, "CAApplyGoneHJKMNPQR", map[string]any{
		"packageName": installed.Name,
		"mode":        "upgradeExisting",
		"newVersion":  "0.2.0",
		"baseVersion": "0.1.0",
	})
	plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err != nil {
		t.Fatalf("CapabilityApplyPlanForProposal: %v", err)
	}

	// The base goes away between the plan and the apply.
	if _, err := inst.Uninstall(ctx, installed.Name, UninstallOptions{}); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	res, err := inst.ApplyCapabilityPlan(ctx, plan)
	if err == nil {
		t.Fatalf("an upgrade whose base was uninstalled must say so, got: %+v", res)
	}
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("want ErrNotInstalled — anything else means the apply tried a fresh install onto the uninstall's tombstones; got %v", err)
	}
}

// TestApplyCapabilityPlan_NeverForces pins that Force is not set. Force would
// defeat the same-version skip, which is the tempting thing for a future author
// to reach for, and it would simultaneously re-open the same-version diff path
// the plan builder refuses for an independent reason.
//
// It is observable through the one behaviour only Force produces here: with the
// package installed at the plan's own version and RequireInstalled in play, a
// covering apply either skips or diffs, and Force is what decides. Setting it
// would turn this same-version case into an in-place diff.
func TestApplyCapabilityPlan_NeverForces(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	installed := removalFixtureDef("0.1.0")
	if _, err := inst.Install(ctx, installed); err != nil {
		t.Fatalf("Install: %v", err)
	}
	// A newPackage plan whose name is occupied at its own version. Without
	// Force, Apply's same-version branch returns skip and the post-condition
	// converts it. With Force, Apply would diff-apply into the occupant instead
	// and the error would be the coverage refusal — a different sentinel, on an
	// apply that had no business computing a delta at all.
	proposalKey := seedApprovedProposal(t, ctx, conn, "CAApplyNoForceHJKMN", installed.Name, "newPackage")
	plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err == nil {
		t.Fatalf("precondition: a newPackage plan for an installed name must not build; got %+v", plan)
	}

	// Build the plan while the name is free, then let it be claimed.
	ctx2, conn2, inst2 := newInstallerHarness(t)
	const racedName = "cap-noforce-pkg"
	pk2 := seedApprovedProposal(t, ctx2, conn2, "CAApplyNoForce2HJKM", racedName, "newPackage")
	plan2, err := CapabilityApplyPlanForProposal(ctx2, conn2, pk2)
	if err != nil {
		t.Fatalf("CapabilityApplyPlanForProposal: %v", err)
	}
	occupant := removalFixtureDef("0.1.0")
	occupant.Name = racedName
	if _, err := inst2.Install(ctx2, occupant); err != nil {
		t.Fatalf("install the occupant: %v", err)
	}
	_, err = inst2.ApplyCapabilityPlan(ctx2, plan2)
	if !errors.Is(err, ErrPackageNameClaimed) {
		t.Fatalf("want ErrPackageNameClaimed from the un-forced same-version path; Force would have produced a delta instead. got %v", err)
	}
	_ = inst
}

// TestApplyCapabilityPlan_RemedyIsQualifiedPerMode pins that the refusal's
// stated next move depends on what the proposal was trying to do. "Propose it
// as a new package" is the answer for an upgrade of a package the proposal does
// not own, and is nonsense for a proposal that already IS a new package and lost
// a race for its name — a fixed clause would be false for one of them.
func TestApplyCapabilityPlan_RemedyIsQualifiedPerMode(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	installed := removalFixtureDef("0.1.0")
	if _, err := inst.Install(ctx, installed); err != nil {
		t.Fatalf("Install: %v", err)
	}
	upgradeKey := seedApprovedProposalWithTarget(t, ctx, conn, "CAApplyRemedyUpHJKM", map[string]any{
		"packageName": installed.Name,
		"mode":        "upgradeExisting",
		"newVersion":  "0.2.0",
		"baseVersion": "0.1.0",
	})
	upgradePlan, err := CapabilityApplyPlanForProposal(ctx, conn, upgradeKey)
	if err != nil {
		t.Fatalf("CapabilityApplyPlanForProposal: %v", err)
	}
	_, err = inst.ApplyCapabilityPlan(ctx, upgradePlan)
	var upgradeRefusal *ApplyWouldRemoveError
	if !errors.As(err, &upgradeRefusal) {
		t.Fatalf("want *ApplyWouldRemoveError, got %T (%v)", err, err)
	}
	if !strings.Contains(upgradeRefusal.Remedy, "newPackage") {
		t.Errorf("an upgradeExisting refusal must point at newPackage, got %q", upgradeRefusal.Remedy)
	}
	if !strings.Contains(upgradeRefusal.Remedy, "RENAMED") {
		t.Errorf("the remedy must cover the rename case, which lands here looking like an edit; got %q", upgradeRefusal.Remedy)
	}

	// The newPackage arm: a race onto an occupied name at a DIFFERENT version,
	// which reaches the delta and so produces the coverage refusal.
	ctx2, conn2, inst2 := newInstallerHarness(t)
	const racedName = "cap-remedy-race-pkg"
	newKey := seedApprovedProposal(t, ctx2, conn2, "CAApplyRemedyNwHJKM", racedName, "newPackage")
	newPlan, err := CapabilityApplyPlanForProposal(ctx2, conn2, newKey)
	if err != nil {
		t.Fatalf("CapabilityApplyPlanForProposal: %v", err)
	}
	occupant := removalFixtureDef("0.2.0")
	occupant.Name = racedName
	if _, err := inst2.Install(ctx2, occupant); err != nil {
		t.Fatalf("install the occupant: %v", err)
	}
	_, err = inst2.ApplyCapabilityPlan(ctx2, newPlan)
	var newRefusal *ApplyWouldRemoveError
	if !errors.As(err, &newRefusal) {
		t.Fatalf("want *ApplyWouldRemoveError, got %T (%v)", err, err)
	}
	if strings.Contains(newRefusal.Remedy, "propose it as `newPackage`") {
		t.Errorf("a proposal that already IS newPackage must not be told to become one: %q", newRefusal.Remedy)
	}
	if !strings.Contains(newRefusal.Remedy, "name that is free") {
		t.Errorf("the newPackage remedy must send the author to a free name, got %q", newRefusal.Remedy)
	}
}

// TestMaterializedDefinition_IsNotAWriteHandle pins that inspection cannot
// become authorship.
//
// Definition is a struct of slices and maps, so an accessor returning it by
// value hands out the plan's own backing arrays: a caller "inspecting" the plan
// could edit the lens body and have ApplyCapabilityPlan submit the edited one.
// That defeats the entire reason the field is unexported — what lands has to be
// what review approved — and no lint rule can see it, because the attack
// involves no call to Apply at all.
func TestMaterializedDefinition_IsNotAWriteHandle(t *testing.T) {
	ctx, conn := newCapabilityApplyHarness(t)
	proposalKey := seedApprovedProposal(t, ctx, conn, "CAApplyAliasHJKMNPQ", "alias-target-pkg", "newPackage")
	plan, err := CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err != nil {
		t.Fatalf("CapabilityApplyPlanForProposal: %v", err)
	}
	if len(plan.definition.Lenses) != 1 {
		t.Fatalf("fixture: want one lens, got %d", len(plan.definition.Lenses))
	}
	approvedSpec := plan.definition.Lenses[0].Spec
	approvedName := plan.definition.Lenses[0].CanonicalName

	// Everything a caller holding the returned value could reach.
	inspected := plan.MaterializedDefinition()
	inspected.Lenses[0].Spec = "MATCH (x:anything) RETURN x.key AS key"
	inspected.Lenses[0].CanonicalName = "smuggledLens"
	inspected.Name = "some-other-package"
	if len(inspected.Depends) > 0 {
		inspected.Depends[0] = "smuggled-dependency"
	}

	if plan.definition.Lenses[0].Spec != approvedSpec {
		t.Fatalf("the plan's lens body was rewritten through the accessor: %q", plan.definition.Lenses[0].Spec)
	}
	if plan.definition.Lenses[0].CanonicalName != approvedName {
		t.Fatalf("the plan's lens identity was rewritten through the accessor: %q", plan.definition.Lenses[0].CanonicalName)
	}
	if plan.definition.Name != "alias-target-pkg" {
		t.Fatalf("the plan's target package was rewritten through the accessor: %q", plan.definition.Name)
	}

	// And what a second inspection sees is still the approved artifact — the
	// assertion that would catch a copy shallow enough to share one level down.
	again := plan.MaterializedDefinition()
	if again.Lenses[0].Spec != approvedSpec || again.Lenses[0].CanonicalName != approvedName {
		t.Fatalf("a later inspection saw the mutation: %+v", again.Lenses[0])
	}
}
