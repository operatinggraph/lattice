// Unit tests for AuthTraceEmitter (FR23 three-plane auth failure
// traceability).
//
// Covered cases:
//   - denial trace contains all three planes
//   - allowed-decision trace under flag (traceAllowDecisions=true)
//   - allowed-decision NOT traced when flag is false (default)
//   - nil emitter is a no-op (stub-mode safety)
//   - plane 2 lens revision sourced from projectedFromRevisions
//   - plane 3 source vertex revisions complete map
//   - trace key shape: health.processor.<instance>.auth-trace.<requestId>
//   - TTL passed to writer (1 hour)
//   - async write does not block caller
//   - NoCapabilityEntry (no doc) — minimal plane 1, empty planes 2+3
//   - AuthDenied (doc available) — planes 2+3 populated
//   - buildRecord constructs Class = "meta.healthRecord"
package processor

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// --- test plumbing ---

// fakeTraceWriter captures KVPutWithTTL calls for assertion.
type fakeTraceWriter struct {
	mu      sync.Mutex
	entries []traceWriteCall
}

type traceWriteCall struct {
	bucket string
	key    string
	value  []byte
	ttl    time.Duration
}

func (w *fakeTraceWriter) KVPutWithTTL(_ context.Context, bucket, key string, value []byte, ttl time.Duration) (uint64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = append(w.entries, traceWriteCall{bucket: bucket, key: key, value: value, ttl: ttl})
	return 1, nil
}

// waitForWrite blocks until the writer has received at least n entries or
// the timeout elapses. Used to handle the async goroutine in Emit.
func (w *fakeTraceWriter) waitForN(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		got := len(w.entries)
		w.mu.Unlock()
		if got >= n {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func (w *fakeTraceWriter) last() traceWriteCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.entries[len(w.entries)-1]
}

func (w *fakeTraceWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.entries)
}

// parseTraceRecord unmarshals the written bytes into an AuthTraceRecord.
func parseTraceRecord(t *testing.T, raw []byte) AuthTraceRecord {
	t.Helper()
	var rec AuthTraceRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("parse auth trace record: %v", err)
	}
	return rec
}

func traceTestDoc(projectedAt time.Time) *CapabilityDoc {
	return &CapabilityDoc{
		Key:         capTestActorCap,
		Actor:       capTestActorKey,
		Version:     "1.0",
		ProjectedAt: projectedAt.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{
			capTestActorKey:                  42,
			lensDefinitionKeyForCapabilityKV: 7,
			"vtx.role.penthouseResident":     3,
		},
		Lanes:               []string{"default"},
		PlatformPermissions: []PlatformPermission{{OperationType: "PingPlatform", Scope: "any"}},
		Roles:               []string{"vtx.role.penthouseResident"},
	}
}

func traceTestEmitter(t *testing.T, traceAllows bool) (*AuthTraceEmitter, *fakeTraceWriter) {
	t.Helper()
	w := &fakeTraceWriter{}
	e := NewAuthTraceEmitter(w, "health-kv", "proc-TestInst01234567", traceAllows, capTestLogger())
	return e, w
}

const traceTestInstance = "proc-TestInst01234567"
const traceTestWaitTimeout = 500 * time.Millisecond

// --- tests ---

func TestAuthTrace_DenialContainsThreePlanes(t *testing.T) {
	projectedAt := time.Now().Add(-100 * time.Millisecond)
	doc := traceTestDoc(projectedAt)
	emitter, w := traceTestEmitter(t, false)

	dec := Decision{
		Authorized: false,
		Code:       ErrCodeAuthDenied,
		Reason:     "OperationNotPermitted",
		Doc:        doc,
	}
	env := baseEnv("PingPlatform", capTestActorKey)

	emitter.Emit(env, dec)

	if !w.waitForN(1, traceTestWaitTimeout) {
		t.Fatal("timeout waiting for auth trace write")
	}

	call := w.last()
	if call.bucket != "health-kv" {
		t.Errorf("bucket: got %q, want %q", call.bucket, "health-kv")
	}
	wantKey := "health.processor." + traceTestInstance + ".auth-trace." + capTestActorID
	if call.key != wantKey {
		t.Errorf("key: got %q, want %q", call.key, wantKey)
	}
	if call.ttl != authTraceOneHourTTL {
		t.Errorf("ttl: got %v, want %v", call.ttl, authTraceOneHourTTL)
	}

	rec := parseTraceRecord(t, call.value)

	// Meta fields.
	if rec.Class != "meta.healthRecord" {
		t.Errorf("class: got %q, want meta.healthRecord", rec.Class)
	}
	if rec.AuthOutcome != "denied" {
		t.Errorf("authOutcome: got %q, want denied", rec.AuthOutcome)
	}
	if rec.AuthCode != string(ErrCodeAuthDenied) {
		t.Errorf("authCode: got %q, want %q", rec.AuthCode, ErrCodeAuthDenied)
	}
	if rec.RequestID != capTestActorID {
		t.Errorf("requestId: got %q, want %q", rec.RequestID, capTestActorID)
	}
	if rec.Actor != capTestActorKey {
		t.Errorf("actor: got %q, want %q", rec.Actor, capTestActorKey)
	}

	// Plane 1.
	if rec.Plane1.CapabilityKVKey != capTestActorCap {
		t.Errorf("plane1.capabilityKVKey: got %q, want %q", rec.Plane1.CapabilityKVKey, capTestActorCap)
	}
	if rec.Plane1.ProjectedAt != projectedAt.Format(time.RFC3339Nano) {
		t.Errorf("plane1.projectedAt: got %q", rec.Plane1.ProjectedAt)
	}
	if rec.Plane1.Result != "no-match" {
		t.Errorf("plane1.result: got %q, want no-match", rec.Plane1.Result)
	}
	// EvaluatedSection: no authContext → platform
	if rec.Plane1.EvaluatedSection != "platformPermissions" {
		t.Errorf("plane1.evaluatedSection: got %q, want platformPermissions", rec.Plane1.EvaluatedSection)
	}

	// Plane 2.
	if rec.Plane2.LensDefinitionKey != lensDefinitionKeyForCapabilityKV {
		t.Errorf("plane2.lensDefinitionKey: got %q", rec.Plane2.LensDefinitionKey)
	}
	if rec.Plane2.LensRevisionAtProjection != 7 {
		t.Errorf("plane2.lensRevision: got %d, want 7", rec.Plane2.LensRevisionAtProjection)
	}
	if rec.Plane2.CypherRuleBodyHash == "" {
		t.Error("plane2.cypherRuleBodyHash: empty")
	}

	// Plane 3.
	if len(rec.Plane3.SourceVertexRevisions) == 0 {
		t.Error("plane3.sourceVertexRevisions: empty")
	}
	if rev, ok := rec.Plane3.SourceVertexRevisions[capTestActorKey]; !ok || rev != 42 {
		t.Errorf("plane3: actor rev: got %d ok=%v, want 42", rev, ok)
	}
	if rev, ok := rec.Plane3.SourceVertexRevisions[lensDefinitionKeyForCapabilityKV]; !ok || rev != 7 {
		t.Errorf("plane3: lens rev: got %d ok=%v, want 7", rev, ok)
	}
}

func TestAuthTrace_AllowedDecisionTracedWhenFlagOn(t *testing.T) {
	projectedAt := time.Now().Add(-50 * time.Millisecond)
	emitter, w := traceTestEmitter(t, true) // traceAllowDecisions=true

	resolved := &ResolvedPermission{
		CapKey:      capTestActorCap,
		ProjectedAt: projectedAt.Format(time.RFC3339Nano),
		Path:        "platform",
		PlatformPermission: &PlatformPermission{
			OperationType: "PingPlatform",
			Scope:         "any",
		},
	}
	dec := Decision{
		Authorized: true,
		Resolved:   resolved,
	}
	env := baseEnv("PingPlatform", capTestActorKey)

	emitter.Emit(env, dec)

	if !w.waitForN(1, traceTestWaitTimeout) {
		t.Fatal("timeout waiting for auth trace write")
	}

	rec := parseTraceRecord(t, w.last().value)
	if rec.AuthOutcome != "allowed" {
		t.Errorf("authOutcome: got %q, want allowed", rec.AuthOutcome)
	}
	if rec.AuthCode != "" {
		t.Errorf("authCode should be empty on allow: got %q", rec.AuthCode)
	}
	// Plane 1 from resolved.
	if rec.Plane1.CapabilityKVKey != capTestActorCap {
		t.Errorf("plane1.capabilityKVKey: got %q", rec.Plane1.CapabilityKVKey)
	}
	if rec.Plane1.Result != "matched" {
		t.Errorf("plane1.result: got %q, want matched", rec.Plane1.Result)
	}
	if rec.Plane1.MatchedPermissionPath != "platform" {
		t.Errorf("plane1.matchedPermissionPath: got %q, want platform", rec.Plane1.MatchedPermissionPath)
	}
}

func TestAuthTrace_AllowedDecisionNotTracedWhenFlagOff(t *testing.T) {
	emitter, w := traceTestEmitter(t, false) // traceAllowDecisions=false (default)

	resolved := &ResolvedPermission{
		CapKey: capTestActorCap,
		Path:   "platform",
	}
	dec := Decision{Authorized: true, Resolved: resolved}
	env := baseEnv("PingPlatform", capTestActorKey)

	emitter.Emit(env, dec)

	// Give the goroutine a short window — should NOT write.
	time.Sleep(50 * time.Millisecond)
	if w.count() != 0 {
		t.Errorf("expected no trace write when flag is off, got %d", w.count())
	}
}

func TestAuthTrace_NilEmitterIsNoop(t *testing.T) {
	// Calling Emit on nil *AuthTraceEmitter must not panic.
	var emitter *AuthTraceEmitter
	dec := Decision{Authorized: false, Code: ErrCodeAuthDenied, Reason: "NoCapabilityEntry"}
	env := baseEnv("PingPlatform", capTestActorKey)
	emitter.Emit(env, dec) // must not panic
}

func TestAuthTrace_NoCapabilityEntry_MinimalPlanes(t *testing.T) {
	emitter, w := traceTestEmitter(t, false)

	// NoCapabilityEntry has no doc.
	dec := Decision{
		Authorized: false,
		Code:       ErrCodeAuthDenied,
		Reason:     "NoCapabilityEntry",
		Doc:        nil,
	}
	env := baseEnv("PingPlatform", capTestActorKey)

	emitter.Emit(env, dec)

	if !w.waitForN(1, traceTestWaitTimeout) {
		t.Fatal("timeout")
	}

	rec := parseTraceRecord(t, w.last().value)
	if rec.Plane1.Result != "no-entry" {
		t.Errorf("plane1.result: got %q, want no-entry", rec.Plane1.Result)
	}
	if rec.Plane1.CapabilityKVKey != "" {
		t.Errorf("plane1.capabilityKVKey should be empty for no-entry, got %q", rec.Plane1.CapabilityKVKey)
	}
	if rec.Plane2.LensDefinitionKey != "" {
		t.Errorf("plane2.lensDefinitionKey should be empty for no-entry, got %q", rec.Plane2.LensDefinitionKey)
	}
	if len(rec.Plane3.SourceVertexRevisions) != 0 {
		t.Errorf("plane3.sourceVertexRevisions should be empty for no-entry, got %v", rec.Plane3.SourceVertexRevisions)
	}
}

func TestAuthTrace_DenialWithDoc_PlanesPopulated(t *testing.T) {
	projectedAt := time.Now().Add(-5 * time.Second)
	doc := traceTestDoc(projectedAt)
	emitter, w := traceTestEmitter(t, false)

	dec := Decision{
		Authorized: false,
		Code:       ErrCodeAuthDenied,
		Reason:     "no matching platformPermission",
		Doc:        doc,
	}
	env := baseEnv("PingPlatform", capTestActorKey)

	emitter.Emit(env, dec)

	if !w.waitForN(1, traceTestWaitTimeout) {
		t.Fatal("timeout")
	}

	rec := parseTraceRecord(t, w.last().value)
	if rec.AuthCode != string(ErrCodeAuthDenied) {
		t.Errorf("authCode: got %q", rec.AuthCode)
	}
	// Plane 2 should have lens revision from the doc.
	if rec.Plane2.LensRevisionAtProjection != 7 {
		t.Errorf("plane2.lensRevision: got %d, want 7", rec.Plane2.LensRevisionAtProjection)
	}
	// Plane 3 should have all source revisions.
	if len(rec.Plane3.SourceVertexRevisions) != 3 {
		t.Errorf("plane3 revisions: got %d, want 3", len(rec.Plane3.SourceVertexRevisions))
	}
}

func TestAuthTrace_TraceKeyShape(t *testing.T) {
	emitter, w := traceTestEmitter(t, false)

	dec := Decision{Authorized: false, Code: ErrCodeAuthDenied, Reason: "NoCapabilityEntry"}
	env := baseEnv("PingPlatform", capTestActorKey)

	emitter.Emit(env, dec)

	if !w.waitForN(1, traceTestWaitTimeout) {
		t.Fatal("timeout")
	}

	got := w.last().key
	want := "health.processor." + traceTestInstance + ".auth-trace." + capTestActorID
	if got != want {
		t.Errorf("key: got %q, want %q", got, want)
	}
}

func TestAuthTrace_TTLIsOneHour(t *testing.T) {
	emitter, w := traceTestEmitter(t, false)

	dec := Decision{Authorized: false, Code: ErrCodeAuthDenied, Reason: "NoCapabilityEntry"}
	env := baseEnv("PingPlatform", capTestActorKey)

	emitter.Emit(env, dec)

	if !w.waitForN(1, traceTestWaitTimeout) {
		t.Fatal("timeout")
	}

	if w.last().ttl != time.Hour {
		t.Errorf("ttl: got %v, want 1h", w.last().ttl)
	}
}

func TestAuthTrace_Plane2_CypherHashIsDeterministic(t *testing.T) {
	projectedAt := time.Now().Add(-100 * time.Millisecond)
	doc := traceTestDoc(projectedAt)

	// Build plane 2 twice with the same doc — hash must be identical.
	p2a := buildPlane2FromDoc(doc)
	p2b := buildPlane2FromDoc(doc)

	if p2a.CypherRuleBodyHash == "" {
		t.Error("cypherRuleBodyHash is empty")
	}
	if p2a.CypherRuleBodyHash != p2b.CypherRuleBodyHash {
		t.Errorf("cypherRuleBodyHash is not deterministic: %q vs %q",
			p2a.CypherRuleBodyHash, p2b.CypherRuleBodyHash)
	}
}

func TestAuthTrace_ServicePath_EvaluatedSection(t *testing.T) {
	projectedAt := time.Now().Add(-50 * time.Millisecond)
	doc := traceTestDoc(projectedAt)
	emitter, w := traceTestEmitter(t, false)

	dec := Decision{
		Authorized: false,
		Code:       ErrCodeAuthContextMismatch,
		Reason:     "service not in serviceAccess",
		Doc:        doc,
	}
	env := envFor("BookCleaning", capTestActorKey, &AuthContext{Service: capTestServiceKey})

	emitter.Emit(env, dec)

	if !w.waitForN(1, traceTestWaitTimeout) {
		t.Fatal("timeout")
	}

	rec := parseTraceRecord(t, w.last().value)
	// AuthContextMismatch with service set → evaluatedSection = serviceAccess
	if rec.Plane1.EvaluatedSection != "serviceAccess" {
		t.Errorf("plane1.evaluatedSection: got %q, want serviceAccess", rec.Plane1.EvaluatedSection)
	}
}

func TestAuthTrace_TaskPath_EvaluatedSection(t *testing.T) {
	projectedAt := time.Now().Add(-50 * time.Millisecond)
	doc := traceTestDoc(projectedAt)
	emitter, w := traceTestEmitter(t, false)

	dec := Decision{
		Authorized: false,
		Code:       ErrCodeAuthContextMismatch,
		Reason:     "no matching ephemeralGrant",
		Doc:        doc,
	}
	env := envFor("ApproveLeaseApplication", capTestActorKey, &AuthContext{Task: capTestTaskKey, Target: capTestTargetKey})

	emitter.Emit(env, dec)

	if !w.waitForN(1, traceTestWaitTimeout) {
		t.Fatal("timeout")
	}

	rec := parseTraceRecord(t, w.last().value)
	if rec.Plane1.EvaluatedSection != "ephemeralGrants" {
		t.Errorf("plane1.evaluatedSection: got %q, want ephemeralGrants", rec.Plane1.EvaluatedSection)
	}
}

func TestAuthTrace_VerifySubstrateConnSatisfiesInterface(t *testing.T) {
	// Compile-time check: *substrate.Conn must satisfy AuthTraceWriter.
	// We use a type assertion to make the test explicit rather than just a
	// var _ = check (which compile-time-only fails silently at test time).
	var _ AuthTraceWriter = (*substrate.Conn)(nil)
}
