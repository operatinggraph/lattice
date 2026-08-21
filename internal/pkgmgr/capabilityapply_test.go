package pkgmgr

import (
	"context"
	"encoding/json"
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
		proposalKey + ".target":   {"packageName": packageName, "mode": mode},
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
	proposalKey := "vtx.capabilityproposal." + proposalID
	body, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal weaverTarget content: %v", err)
	}
	aspects := map[string]map[string]any{
		proposalKey + ".review":   {"state": "approved"},
		proposalKey + ".artifact": {"kind": "weaverTarget", "content": string(body)},
		proposalKey + ".target":   {"packageName": "ai-target-escalation", "mode": "newPackage"},
	}
	for key, data := range aspects {
		putEnvelope(t, ctx, conn, key, map[string]any{"isDeleted": false, "data": data})
	}
	return proposalKey
}

// TestCapabilityApplyPlan_AuthoredDirectOpEscalation_Rejected is the headline
// containment: an approved authored target whose directOp gap names a protected
// operation (AssignRole) is refused at the plan builder — the escalation path
// this fire's apply-side opened. The package it installs is benign
// (ai-target-*, not protected), so only the dispatch-scope gate can catch it.
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
	if len(plan.Definition.WeaverTargets) != 1 {
		t.Fatalf("plan defines %d weaver targets, want 1", len(plan.Definition.WeaverTargets))
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
	if len(plan.Definition.Lenses) != 1 {
		t.Fatalf("plan.Definition.Lenses = %d, want 1", len(plan.Definition.Lenses))
	}
}
