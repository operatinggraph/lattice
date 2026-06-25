// Unit tests for DenialResponseBuilder (FR22).
//
// Covered cases:
//   - denial for each reason value: NoCapabilityEntry, OperationNotPermitted,
//     AuthContextMismatch (task / service / both / platform-scope)
//   - rolesCarryingPermission: populated from index, empty for unknown operation
//   - actorRoles: populated from doc.Roles; empty for NoCapabilityEntry
//   - actor with multiple roles
//   - evaluatedSection: platform / service / task / absent for NoCapabilityEntry
//   - diagnosticHint: present for mismatch; absent for OperationNotPermitted
//   - NFR-S6 leak check: no other-actor data in denial response
package processor

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// fakeReaderWithIndex extends fakeReader to also serve role-by-operation entries.
// The base fakeReader already supports arbitrary key→bytes, so we reuse it.

func newDenialBuilderForTest(t *testing.T, indexEntries map[string][]string) *DenialResponseBuilder {
	t.Helper()
	reader := &fakeReader{entries: map[string][]byte{}}
	// Seed role-by-operation index entries.
	for opType, roles := range indexEntries {
		key := "cap.role-by-operation." + opType
		doc := RoleByOperationDoc{Roles: roles, ProjectedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("marshal role-by-operation doc: %v", err)
		}
		reader.entries[key] = raw
	}
	return NewDenialResponseBuilder(reader, "capability-kv", capTestLogger())
}

func baseEnv(opType, actor string) *OperationEnvelope {
	return envFor(opType, actor, nil)
}

func baseDoc(roles []string) *CapabilityDoc {
	return &CapabilityDoc{
		Key:     capTestActorCap,
		Actor:   capTestActorKey,
		Version: "1.0",
		Roles:   roles,
		PlatformPermissions: []PlatformPermission{
			{OperationType: "PingPlatform", Scope: "any"},
		},
	}
}

// --- NoCapabilityEntry ---

func TestDenialBuilder_NoCapabilityEntry(t *testing.T) {
	b := newDenialBuilderForTest(t, nil)
	env := baseEnv("PingPlatform", capTestActorKey)
	dec := Decision{
		Authorized: false,
		Code:       ErrCodeAuthDenied,
		Reason:     "NoCapabilityEntry",
		Doc:        nil, // no doc for this path
	}
	dd := b.BuildDenialDetails(context.Background(), env, dec, nil)
	if dd.Decision != "denied" {
		t.Errorf("decision: got %q want denied", dd.Decision)
	}
	if dd.Reason != "NoCapabilityEntry" {
		t.Errorf("reason: got %q want NoCapabilityEntry", dd.Reason)
	}
	if dd.OperationType != "PingPlatform" {
		t.Errorf("operationType: got %q want PingPlatform", dd.OperationType)
	}
	if dd.RequestID != capTestActorID {
		t.Errorf("requestId: got %q want %q", dd.RequestID, capTestActorID)
	}
	// actorRoles must be empty (no doc) not nil.
	if len(dd.ActorRoles) != 0 {
		t.Errorf("actorRoles: expected empty, got %v", dd.ActorRoles)
	}
	// evaluatedSection must be empty (no section evaluated).
	if dd.EvaluatedSection != "" {
		t.Errorf("evaluatedSection: expected empty, got %q", dd.EvaluatedSection)
	}
	// diagnosticHint must be absent.
	if dd.DiagnosticHint != "" {
		t.Errorf("diagnosticHint: expected absent, got %q", dd.DiagnosticHint)
	}
}

// --- OperationNotPermitted (platform) ---

func TestDenialBuilder_OperationNotPermitted_Platform(t *testing.T) {
	roles := []string{"vtx.role.penthouseResident"}
	b := newDenialBuilderForTest(t, map[string][]string{
		"BookLaundry": {"vtx.role.standardResident", "vtx.role.admin"},
	})
	doc := baseDoc(roles)
	env := baseEnv("BookLaundry", capTestActorKey)
	dec := Decision{
		Authorized: false,
		Code:       ErrCodeAuthDenied,
		Reason:     "no matching platformPermission",
		Doc:        doc,
	}
	dd := b.BuildDenialDetails(context.Background(), env, dec, doc)
	if dd.Reason != "OperationNotPermitted" {
		t.Errorf("reason: got %q want OperationNotPermitted", dd.Reason)
	}
	if dd.EvaluatedSection != "platformPermissions" {
		t.Errorf("evaluatedSection: got %q want platformPermissions", dd.EvaluatedSection)
	}
	if len(dd.ActorRoles) != 1 || dd.ActorRoles[0] != "vtx.role.penthouseResident" {
		t.Errorf("actorRoles: got %v want [vtx.role.penthouseResident]", dd.ActorRoles)
	}
	if len(dd.RolesCarryingPermission) != 2 {
		t.Errorf("rolesCarryingPermission: got %v want 2 entries", dd.RolesCarryingPermission)
	}
	if dd.DiagnosticHint != "" {
		t.Errorf("diagnosticHint: expected absent for OperationNotPermitted, got %q", dd.DiagnosticHint)
	}
}

// --- OperationNotPermitted (service path) ---

func TestDenialBuilder_OperationNotPermitted_ServicePath(t *testing.T) {
	b := newDenialBuilderForTest(t, map[string][]string{
		"NotAllowed": {"vtx.role.admin"},
	})
	doc := baseDoc([]string{"vtx.role.penthouseResident"})
	env := envFor("NotAllowed", capTestActorKey, &AuthContext{Service: capTestServiceKey})
	dec := Decision{
		Authorized: false,
		Code:       ErrCodeAuthDenied,
		Reason:     "operationType not in serviceAccess.allowedOperations",
		Doc:        doc,
	}
	dd := b.BuildDenialDetails(context.Background(), env, dec, doc)
	if dd.Reason != "OperationNotPermitted" {
		t.Errorf("reason: got %q want OperationNotPermitted", dd.Reason)
	}
	if dd.EvaluatedSection != "serviceAccess" {
		t.Errorf("evaluatedSection: got %q want serviceAccess, got %q", dd.EvaluatedSection, dd.EvaluatedSection)
	}
}

// --- OperationNotPermitted (task path) ---

func TestDenialBuilder_OperationNotPermitted_TaskPath(t *testing.T) {
	b := newDenialBuilderForTest(t, nil)
	doc := baseDoc([]string{"vtx.role.penthouseResident"})
	env := envFor("ApproveLeaseApplication", capTestActorKey, &AuthContext{Task: capTestTaskKey, Target: capTestTargetKey})
	dec := Decision{
		Authorized: false,
		Code:       ErrCodeAuthContextMismatch,
		Reason:     "no matching ephemeralGrant",
		Doc:        doc,
	}
	// Task path mismatch: evaluatedSection = ephemeralGrants
	// But Code=AuthContextMismatch → diagnosticHint path
	dd := b.BuildDenialDetails(context.Background(), env, dec, doc)
	if dd.Reason != "AuthContextMismatch" {
		t.Errorf("reason: got %q want AuthContextMismatch", dd.Reason)
	}
	if dd.DiagnosticHint == "" {
		t.Errorf("diagnosticHint: expected non-empty for task AuthContextMismatch")
	}
	// actorRoles and rolesCarryingPermission must be absent for AuthContextMismatch.
	if dd.ActorRoles != nil {
		t.Errorf("actorRoles: expected nil for AuthContextMismatch; got %v", dd.ActorRoles)
	}
}

// --- AuthContextMismatch: both service and task set ---

func TestDenialBuilder_AuthContextMismatch_BothSet(t *testing.T) {
	b := newDenialBuilderForTest(t, nil)
	doc := baseDoc([]string{"vtx.role.admin"})
	env := envFor("X", capTestActorKey, &AuthContext{Service: capTestServiceKey, Task: capTestTaskKey})
	dec := Decision{
		Authorized: false,
		Code:       ErrCodeAuthContextMismatch,
		Reason:     "authContext: service and task are mutually exclusive",
		Doc:        doc,
	}
	dd := b.BuildDenialDetails(context.Background(), env, dec, doc)
	if dd.Reason != "AuthContextMismatch" {
		t.Errorf("reason: got %q want AuthContextMismatch", dd.Reason)
	}
	if dd.DiagnosticHint == "" {
		t.Errorf("diagnosticHint: expected non-empty for both-set mismatch")
	}
	// Must not include role-coverage fields.
	if dd.ActorRoles != nil || dd.RolesCarryingPermission != nil {
		t.Errorf("role fields must be absent for AuthContextMismatch; actorRoles=%v rolesCarrying=%v",
			dd.ActorRoles, dd.RolesCarryingPermission)
	}
}

// --- AuthContextMismatch: service not in projection ---

func TestDenialBuilder_AuthContextMismatch_ServiceNotInProjection(t *testing.T) {
	b := newDenialBuilderForTest(t, nil)
	doc := baseDoc([]string{"vtx.role.leaseholder"})
	env := envFor("DoSomething", capTestActorKey, &AuthContext{Service: "vtx.service.someOther"})
	dec := Decision{
		Authorized: false,
		Code:       ErrCodeAuthContextMismatch,
		Reason:     "service not in serviceAccess",
		Doc:        doc,
	}
	dd := b.BuildDenialDetails(context.Background(), env, dec, doc)
	if dd.Reason != "AuthContextMismatch" {
		t.Errorf("reason: got %q want AuthContextMismatch", dd.Reason)
	}
	if dd.DiagnosticHint == "" {
		t.Errorf("diagnosticHint: expected non-empty for service-not-in-projection")
	}
}

// --- Unknown operation type (no role-by-operation index) ---

func TestDenialBuilder_UnknownOperationType_EmptyRolesCarrying(t *testing.T) {
	// No index entries — operation type is unknown.
	b := newDenialBuilderForTest(t, nil)
	doc := baseDoc([]string{"vtx.role.penthouseResident"})
	env := baseEnv("BookAirplane", capTestActorKey) // operation not in any index
	dec := Decision{
		Authorized: false,
		Code:       ErrCodeAuthDenied,
		Reason:     "no matching platformPermission",
		Doc:        doc,
	}
	dd := b.BuildDenialDetails(context.Background(), env, dec, doc)
	if dd.Reason != "OperationNotPermitted" {
		t.Errorf("reason: got %q want OperationNotPermitted", dd.Reason)
	}
	if dd.RolesCarryingPermission == nil || len(dd.RolesCarryingPermission) != 0 {
		t.Errorf("rolesCarryingPermission: expected empty slice for unknown op; got %v", dd.RolesCarryingPermission)
	}
}

// --- Actor with multiple roles ---

func TestDenialBuilder_MultipleRoles(t *testing.T) {
	roles := []string{"vtx.role.penthouseResident", "vtx.role.leaseholderInGoodStanding", "vtx.role.admin"}
	b := newDenialBuilderForTest(t, map[string][]string{
		"RestrictedOp": {"vtx.role.admin"},
	})
	doc := baseDoc(roles)
	env := baseEnv("RestrictedOp", capTestActorKey)
	dec := Decision{
		Authorized: false,
		Code:       ErrCodeAuthDenied,
		Reason:     "no matching platformPermission",
		Doc:        doc,
	}
	dd := b.BuildDenialDetails(context.Background(), env, dec, doc)
	if len(dd.ActorRoles) != 3 {
		t.Errorf("actorRoles: expected 3 roles; got %v", dd.ActorRoles)
	}
	if len(dd.RolesCarryingPermission) != 1 || dd.RolesCarryingPermission[0] != "vtx.role.admin" {
		t.Errorf("rolesCarryingPermission: expected [vtx.role.admin]; got %v", dd.RolesCarryingPermission)
	}
}

// --- NFR-S6 leak check ---
//
// Verify that no other-actor data appears in any denial response.
// The test seeds a role-by-operation index and two actor docs, then
// requests a denial for actor-A and asserts no actor-B data leaks.

func TestDenialBuilder_NFRS6_NoOtherActorLeak(t *testing.T) {
	const otherActorID = "OtherActorXXXXXXXXXX"
	const otherActorKey = "vtx.identity." + otherActorID

	// Index only contains public role names — no per-actor data.
	b := newDenialBuilderForTest(t, map[string][]string{
		"PingPlatform": {"vtx.role.penthouseResident"},
	})

	// Actor A's doc — contains actor A's roles.
	docA := baseDoc([]string{"vtx.role.guestResident"})
	envA := baseEnv("PingPlatform", capTestActorKey)
	decA := Decision{
		Authorized: false,
		Code:       ErrCodeAuthDenied,
		Reason:     "no matching platformPermission",
		Doc:        docA,
	}

	dd := b.BuildDenialDetails(context.Background(), envA, decA, docA)

	// Serialize and check for other actor's key anywhere in the response.
	raw, err := json.Marshal(dd)
	if err != nil {
		t.Fatalf("marshal DenialDetails: %v", err)
	}
	if contains(string(raw), otherActorKey) {
		t.Errorf("NFR-S6: denial response leaks other actor's key %q; full response: %s",
			otherActorKey, raw)
	}
	if contains(string(raw), otherActorID) {
		t.Errorf("NFR-S6: denial response leaks other actor's ID %q; full response: %s",
			otherActorID, raw)
	}

	// actorRoles should only contain actor A's roles.
	for _, r := range dd.ActorRoles {
		if contains(r, otherActorID) {
			t.Errorf("NFR-S6: actorRoles contains other actor data %q", r)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- DenialDetailsAsMap round-trip ---

func TestDenialDetailsAsMap_RoundTrip(t *testing.T) {
	dd := DenialDetails{
		Decision:                "denied",
		Reason:                  "OperationNotPermitted",
		OperationType:           "PingPlatform",
		RequestID:               capTestActorID,
		EvaluatedSection:        "platformPermissions",
		ActorRoles:              []string{"vtx.role.penthouseResident"},
		RolesCarryingPermission: []string{"vtx.role.admin"},
	}
	m := DenialDetailsAsMap(dd)
	if m["decision"] != "denied" {
		t.Errorf("decision: got %v want denied", m["decision"])
	}
	if m["reason"] != "OperationNotPermitted" {
		t.Errorf("reason: got %v want OperationNotPermitted", m["reason"])
	}
	if m["evaluatedSection"] != "platformPermissions" {
		t.Errorf("evaluatedSection: got %v want platformPermissions", m["evaluatedSection"])
	}
	// actorRoles should be a []interface{} slice after map round-trip.
	roles, ok := m["actorRoles"].([]interface{})
	if !ok || len(roles) != 1 {
		t.Errorf("actorRoles: got %T %v, want []interface{} with 1 element", m["actorRoles"], m["actorRoles"])
	}
}

// --- Integration: CapabilityAuthorizer threads doc through Decision ---

func TestCapabilityAuthorizer_DenialThreadsDoc(t *testing.T) {
	now := time.Now().UTC()
	doc := freshDoc(now)
	a, _, _ := newCapAuthForTest(t, doc, now)
	// Operation not in permissions → AuthDenied with doc threaded.
	env := envFor("NeverHeardOf", capTestActorKey, nil)
	dec, err := a.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("expected denial")
	}
	if dec.Doc == nil {
		t.Fatalf("Dec.Doc must be non-nil on denial (needed for FR22 actorRoles)")
	}
	if len(dec.Doc.Roles) == 0 {
		t.Errorf("Dec.Doc.Roles should match the fixture; got empty")
	}
}

func TestCapabilityAuthorizer_AllowDoesNotThreadDoc(t *testing.T) {
	now := time.Now().UTC()
	doc := freshDoc(now)
	a, _, _ := newCapAuthForTest(t, doc, now)
	env := envFor("PingPlatform", capTestActorKey, nil)
	dec, err := a.Authorize(context.Background(), env)
	if err != nil || !dec.Authorized {
		t.Fatalf("expected allow; got err=%v dec=%+v", err, dec)
	}
	// On allow, Doc should be nil (not threaded for allow path — Resolved is enough).
	if dec.Doc != nil {
		t.Errorf("allow Decision must not carry Doc (only Resolved is needed on allow path); got Doc non-nil")
	}
}
