// Story 3.3 unit tests for CapabilityAuthorizer.
//
// Covered cases:
//   - all four authContext shapes (none / service / task / both-set)
//   - missing entry → AuthDenied + NoCapabilityEntry
//   - infrastructure failure → returned error (commit path naks)
//   - freshness gate: fresh / above-NFR-P3 / above ceiling
//   - platform scope: any / self (matching + mismatching) / specific / owned
//   - service path: matching + service-mismatch + operation-mismatch
//   - ephemeral grant: matching + expired + target-mismatch
//   - resolved permission populated on success, nil on denial
package processor

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// --- test plumbing ---------------------------------------------------------

const (
	capTestActorID    = "Hj4kPmRtw9nbCxz5vQ2y"
	capTestActorKey   = "vtx.identity." + capTestActorID
	capTestActorCap   = "cap.identity." + capTestActorID
	capTestServiceKey = "vtx.service.executive-cleaning-NanoID"
	capTestTaskKey    = "vtx.task.Rm7q3pntwzkfbcxv5p9j"
	capTestTargetKey  = "vtx.lease.Op4Nb2mPq6rTwzKxVyP7"
)

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

type fakeReader struct {
	entries map[string][]byte
	err     error
}

func (r *fakeReader) KVGet(_ context.Context, _, key string) (*substrate.KVEntry, error) {
	if r.err != nil {
		return nil, r.err
	}
	v, ok := r.entries[key]
	if !ok {
		return nil, substrate.ErrKeyNotFound
	}
	return &substrate.KVEntry{Value: v}, nil
}

type recordingEmitter struct {
	calls []struct {
		code    string
		details map[string]any
	}
}

func (e *recordingEmitter) EmitAlert(_ context.Context, code string, details map[string]any) {
	e.calls = append(e.calls, struct {
		code    string
		details map[string]any
	}{code, details})
}

func capTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newCapAuthForTest(t *testing.T, doc *CapabilityDoc, clockAt time.Time) (*CapabilityAuthorizer, *recordingEmitter, *fakeReader) {
	t.Helper()
	emitter := &recordingEmitter{}
	reader := &fakeReader{entries: map[string][]byte{}}
	if doc != nil {
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("marshal cap doc: %v", err)
		}
		reader.entries[capTestActorCap] = raw
	}
	clock := &fakeClock{now: clockAt}
	a := NewCapabilityAuthorizer(reader, "capability-kv", clock, DefaultCapabilityAuthorizerConfig(), emitter, capTestLogger())
	return a, emitter, reader
}

func freshDoc(projectedAt time.Time) *CapabilityDoc {
	return &CapabilityDoc{
		Key:                    capTestActorCap,
		Actor:                  capTestActorKey,
		Version:                "1.0",
		ProjectedAt:            projectedAt.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{capTestActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []PlatformPermission{
			{OperationType: "ClaimIdentity", Scope: "self"},
			{OperationType: "PingPlatform", Scope: "any"},
			{OperationType: "OwnedOp", Scope: "owned"},
			{OperationType: "PlatformSpecific", Scope: "specific"},
		},
		ServiceAccess: []ServiceAccessEntry{
			{
				Service:      capTestServiceKey,
				ServiceClass: "service.cleaning.executive",
				ResolvedVia:  []string{"vtx.unit.penthouse"},
				AllowedOperations: []AllowedOperation{
					{OperationType: "BookExecutiveCleaning"},
					{OperationType: "ViewSchedule"},
				},
			},
		},
		EphemeralGrants: []EphemeralGrant{
			{
				Source:        "task",
				TaskKey:       capTestTaskKey,
				OperationType: "ApproveLeaseApplication",
				Target:        capTestTargetKey,
				ExpiresAt:     projectedAt.Add(1 * time.Hour).Format(time.RFC3339Nano),
			},
			{
				Source:        "task",
				TaskKey:       capTestTaskKey,
				OperationType: "ExpiredOp",
				Target:        capTestTargetKey,
				ExpiresAt:     projectedAt.Add(-1 * time.Hour).Format(time.RFC3339Nano),
			},
		},
		Roles: []string{"vtx.role.penthouseResident"},
	}
}

func envFor(opType, actor string, ac *AuthContext) *OperationEnvelope {
	return &OperationEnvelope{
		RequestID:     capTestActorID,
		Lane:          LaneDefault,
		OperationType: opType,
		Actor:         actor,
		SubmittedAt:   "2026-05-15T10:00:00Z",
		AuthContext:   ac,
		Payload:       json.RawMessage(`{}`),
	}
}

// --- tests -----------------------------------------------------------------

func TestCapabilityAuthorizer_PlatformAny_Allows(t *testing.T) {
	now := time.Now().UTC()
	a, _, _ := newCapAuthForTest(t, freshDoc(now), now)
	env := envFor("PingPlatform", capTestActorKey, nil)
	dec, err := a.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("expected Authorized=true; got %+v", dec)
	}
	if dec.Resolved == nil || dec.Resolved.Path != "platform" {
		t.Fatalf("expected resolved.Path=platform; got %+v", dec.Resolved)
	}
	if dec.Resolved.PlatformPermission == nil || dec.Resolved.PlatformPermission.OperationType != "PingPlatform" {
		t.Fatalf("expected PlatformPermission=PingPlatform; got %+v", dec.Resolved.PlatformPermission)
	}
}

func TestCapabilityAuthorizer_PlatformSelf_MatchesActor(t *testing.T) {
	now := time.Now().UTC()
	a, _, _ := newCapAuthForTest(t, freshDoc(now), now)
	env := envFor("ClaimIdentity", capTestActorKey, &AuthContext{Target: capTestActorKey})
	dec, err := a.Authorize(context.Background(), env)
	if err != nil || !dec.Authorized {
		t.Fatalf("expected allow; got err=%v dec=%+v", err, dec)
	}
}

func TestCapabilityAuthorizer_PlatformSelf_TargetMismatch(t *testing.T) {
	now := time.Now().UTC()
	a, _, _ := newCapAuthForTest(t, freshDoc(now), now)
	env := envFor("ClaimIdentity", capTestActorKey, &AuthContext{Target: "vtx.identity.someoneElse"})
	dec, _ := a.Authorize(context.Background(), env)
	if dec.Authorized {
		t.Fatalf("expected deny; got %+v", dec)
	}
	if dec.Code != ErrCodeAuthDenied {
		t.Fatalf("expected AuthDenied; got %s", dec.Code)
	}
}

func TestCapabilityAuthorizer_PlatformSelf_TargetMissing(t *testing.T) {
	now := time.Now().UTC()
	a, _, _ := newCapAuthForTest(t, freshDoc(now), now)
	env := envFor("ClaimIdentity", capTestActorKey, nil)
	dec, _ := a.Authorize(context.Background(), env)
	if dec.Code != ErrCodeAuthContextMismatch {
		t.Fatalf("expected AuthContextMismatch for scope=self without target; got %s (reason=%s)", dec.Code, dec.Reason)
	}
}

func TestCapabilityAuthorizer_PlatformSpecific_DeniesAsMismatch(t *testing.T) {
	now := time.Now().UTC()
	a, _, _ := newCapAuthForTest(t, freshDoc(now), now)
	env := envFor("PlatformSpecific", capTestActorKey, &AuthContext{Target: capTestTargetKey})
	dec, _ := a.Authorize(context.Background(), env)
	if dec.Authorized || dec.Code != ErrCodeAuthContextMismatch {
		t.Fatalf("expected AuthContextMismatch for platform scope=specific; got %+v", dec)
	}
}

func TestCapabilityAuthorizer_PlatformOwned_DeniesAsNotImplemented(t *testing.T) {
	now := time.Now().UTC()
	a, _, _ := newCapAuthForTest(t, freshDoc(now), now)
	env := envFor("OwnedOp", capTestActorKey, nil)
	dec, _ := a.Authorize(context.Background(), env)
	if dec.Authorized || dec.Code != ErrCodeAuthDenied || dec.Reason != "OwnershipScopeNotImplemented" {
		t.Fatalf("expected OwnershipScopeNotImplemented; got %+v", dec)
	}
}

func TestCapabilityAuthorizer_ServicePath_Allows(t *testing.T) {
	now := time.Now().UTC()
	a, _, _ := newCapAuthForTest(t, freshDoc(now), now)
	env := envFor("BookExecutiveCleaning", capTestActorKey, &AuthContext{Service: capTestServiceKey})
	dec, err := a.Authorize(context.Background(), env)
	if err != nil || !dec.Authorized {
		t.Fatalf("expected allow; got err=%v dec=%+v", err, dec)
	}
	if dec.Resolved.Path != "service" {
		t.Fatalf("expected Path=service; got %s", dec.Resolved.Path)
	}
}

func TestCapabilityAuthorizer_ServicePath_ServiceNotProjected(t *testing.T) {
	now := time.Now().UTC()
	a, _, _ := newCapAuthForTest(t, freshDoc(now), now)
	env := envFor("BookExecutiveCleaning", capTestActorKey, &AuthContext{Service: "vtx.service.someOther"})
	dec, _ := a.Authorize(context.Background(), env)
	if dec.Code != ErrCodeAuthContextMismatch {
		t.Fatalf("expected AuthContextMismatch for unknown service; got %+v", dec)
	}
}

func TestCapabilityAuthorizer_ServicePath_OpNotAllowed(t *testing.T) {
	now := time.Now().UTC()
	a, _, _ := newCapAuthForTest(t, freshDoc(now), now)
	env := envFor("NotAllowed", capTestActorKey, &AuthContext{Service: capTestServiceKey})
	dec, _ := a.Authorize(context.Background(), env)
	if dec.Code != ErrCodeAuthDenied {
		t.Fatalf("expected AuthDenied for op-not-in-service; got %+v", dec)
	}
}

func TestCapabilityAuthorizer_TaskPath_Allows(t *testing.T) {
	now := time.Now().UTC()
	a, _, _ := newCapAuthForTest(t, freshDoc(now), now)
	env := envFor("ApproveLeaseApplication", capTestActorKey, &AuthContext{Task: capTestTaskKey, Target: capTestTargetKey})
	dec, err := a.Authorize(context.Background(), env)
	if err != nil || !dec.Authorized {
		t.Fatalf("expected allow; got err=%v dec=%+v", err, dec)
	}
	if dec.Resolved.Path != "task" {
		t.Fatalf("expected Path=task; got %s", dec.Resolved.Path)
	}
}

func TestCapabilityAuthorizer_TaskPath_Expired(t *testing.T) {
	now := time.Now().UTC()
	a, _, _ := newCapAuthForTest(t, freshDoc(now), now)
	env := envFor("ExpiredOp", capTestActorKey, &AuthContext{Task: capTestTaskKey, Target: capTestTargetKey})
	dec, _ := a.Authorize(context.Background(), env)
	if dec.Authorized || dec.Code != ErrCodeAuthContextMismatch {
		t.Fatalf("expected AuthContextMismatch for expired grant; got %+v", dec)
	}
}

func TestCapabilityAuthorizer_TaskPath_TargetMismatch(t *testing.T) {
	now := time.Now().UTC()
	a, _, _ := newCapAuthForTest(t, freshDoc(now), now)
	env := envFor("ApproveLeaseApplication", capTestActorKey, &AuthContext{Task: capTestTaskKey, Target: "vtx.lease.someOther"})
	dec, _ := a.Authorize(context.Background(), env)
	if dec.Code != ErrCodeAuthContextMismatch {
		t.Fatalf("expected AuthContextMismatch for target-mismatch; got %+v", dec)
	}
}

func TestCapabilityAuthorizer_BothServiceAndTaskSet(t *testing.T) {
	now := time.Now().UTC()
	a, _, _ := newCapAuthForTest(t, freshDoc(now), now)
	env := envFor("ApproveLeaseApplication", capTestActorKey, &AuthContext{Service: capTestServiceKey, Task: capTestTaskKey})
	dec, _ := a.Authorize(context.Background(), env)
	if dec.Code != ErrCodeAuthContextMismatch {
		t.Fatalf("expected AuthContextMismatch when both service and task set; got %+v", dec)
	}
}

func TestCapabilityAuthorizer_MissingEntry_NoCapabilityEntry(t *testing.T) {
	now := time.Now().UTC()
	a, _, _ := newCapAuthForTest(t, nil, now)
	env := envFor("PingPlatform", capTestActorKey, nil)
	dec, err := a.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("missing entry must NOT return error; got %v", err)
	}
	if dec.Authorized || dec.Code != ErrCodeAuthDenied || dec.Reason != "NoCapabilityEntry" {
		t.Fatalf("expected AuthDenied/NoCapabilityEntry; got %+v", dec)
	}
}

func TestCapabilityAuthorizer_InfrastructureFailure_ReturnsError(t *testing.T) {
	now := time.Now().UTC()
	a, _, reader := newCapAuthForTest(t, nil, now)
	reader.err = errors.New("nats broken")
	env := envFor("PingPlatform", capTestActorKey, nil)
	_, err := a.Authorize(context.Background(), env)
	if err == nil {
		t.Fatalf("expected infra error from Authorize")
	}
}

func TestCapabilityAuthorizer_Freshness_AboveCeiling_DeniesAndAlerts(t *testing.T) {
	projected := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	clockAt := projected.Add(10 * time.Second) // way above 2.5s ceiling
	a, emitter, _ := newCapAuthForTest(t, freshDoc(projected), clockAt)
	env := envFor("PingPlatform", capTestActorKey, nil)
	dec, _ := a.Authorize(context.Background(), env)
	if dec.Code != ErrCodeAuthFreshnessExceeded {
		t.Fatalf("expected AuthFreshnessExceeded; got %+v", dec)
	}
	if len(emitter.calls) == 0 || emitter.calls[0].code != "auth-freshness-exceeded" {
		t.Fatalf("expected auth-freshness-exceeded alert; got %+v", emitter.calls)
	}
}

func TestCapabilityAuthorizer_Freshness_AboveNFRP3_AllowsAndRecords(t *testing.T) {
	projected := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	clockAt := projected.Add(800 * time.Millisecond) // above 500ms but below 2.5s
	a, emitter, _ := newCapAuthForTest(t, freshDoc(projected), clockAt)
	env := envFor("PingPlatform", capTestActorKey, nil)
	dec, _ := a.Authorize(context.Background(), env)
	if !dec.Authorized {
		t.Fatalf("expected allow despite sub-ceiling staleness; got %+v", dec)
	}
	if len(emitter.calls) != 0 {
		t.Fatalf("expected no alerts for sub-ceiling staleness; got %+v", emitter.calls)
	}
	stats := a.StalenessStats()
	if stats.Count != 1 {
		t.Fatalf("expected one staleness sample recorded; got %d", stats.Count)
	}
	if got := a.StalenessExceedingNFRP3(); got != 1 {
		t.Fatalf("expected StalenessExceedingNFRP3=1; got %d", got)
	}
}

func TestCapabilityAuthorizer_LatencyStats_AlwaysRecords(t *testing.T) {
	now := time.Now().UTC()
	a, _, _ := newCapAuthForTest(t, freshDoc(now), now)
	env := envFor("PingPlatform", capTestActorKey, nil)
	if _, err := a.Authorize(context.Background(), env); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if a.LatencyStats().Count != 1 {
		t.Fatalf("expected one latency sample; got %d", a.LatencyStats().Count)
	}
}

func TestCapabilityAuthorizer_DenialDoesNotPopulateResolved(t *testing.T) {
	now := time.Now().UTC()
	a, _, _ := newCapAuthForTest(t, freshDoc(now), now)
	env := envFor("NeverHeardOf", capTestActorKey, nil)
	dec, _ := a.Authorize(context.Background(), env)
	if dec.Resolved != nil {
		t.Fatalf("denial must NOT carry a Resolved pointer; got %+v", dec.Resolved)
	}
}

func TestCapabilityKeyFromActor(t *testing.T) {
	got, err := capabilityKeyFromActor("vtx.identity.ABC")
	if err != nil || got != "cap.identity.ABC" {
		t.Fatalf("got (%q,%v), want (cap.identity.ABC, nil)", got, err)
	}
	if _, err := capabilityKeyFromActor("ABC"); err == nil {
		t.Fatalf("expected error for malformed actor")
	}
}

func TestSelectAuthorizerArgs_DefaultsToCapability(t *testing.T) {
	reader := &fakeReader{entries: map[string][]byte{}}
	a, err := SelectAuthorizerArgs(SelectAuthorizerOpts{
		Mode:             "",
		Reader:           reader,
		CapabilityBucket: "capability-kv",
		Logger:           capTestLogger(),
	})
	if err != nil {
		t.Fatalf("SelectAuthorizerArgs: %v", err)
	}
	if _, ok := a.(*CapabilityAuthorizer); !ok {
		t.Fatalf("expected *CapabilityAuthorizer for default mode; got %T", a)
	}
}

func TestSelectAuthorizerArgs_CapabilityWithoutReaderErrors(t *testing.T) {
	_, err := SelectAuthorizerArgs(SelectAuthorizerOpts{
		Mode:   AuthModeCapability,
		Logger: capTestLogger(),
	})
	if err == nil {
		t.Fatalf("expected error when capability mode lacks reader")
	}
}

func TestStubAuthorizer_EmitsAlertOnFirstCall(t *testing.T) {
	emitter := &recordingEmitter{}
	stub := NewStubAuthorizerWithEmitter(capTestLogger(), emitter)
	env := envFor("X", capTestActorKey, nil)
	if _, err := stub.Authorize(context.Background(), env); err != nil {
		t.Fatalf("stub Authorize: %v", err)
	}
	if len(emitter.calls) == 0 || emitter.calls[0].code != "stub-auth-active" {
		t.Fatalf("expected stub-auth-active alert on first call; got %+v", emitter.calls)
	}
}
