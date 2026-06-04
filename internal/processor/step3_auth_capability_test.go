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
	capTestActorEph   = "cap.ephemeral.identity." + capTestActorID // disjoint ephemeral key
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
	gets    []string // records every key fetched (for single-GET assertions)
}

func (r *fakeReader) KVGet(_ context.Context, _, key string) (*substrate.KVEntry, error) {
	r.gets = append(r.gets, key)
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
		// Ephemeral grants live in the disjoint cap.ephemeral.<actor> entry
		// produced by the orchestration-base capabilityEphemeral lens — NOT
		// in the primary cap.<actor> doc. Split the fixture: seed the grants
		// under the ephemeral key, and strip them from the primary doc.
		grants := doc.EphemeralGrants
		primary := *doc
		primary.EphemeralGrants = nil
		raw, err := json.Marshal(&primary)
		if err != nil {
			t.Fatalf("marshal cap doc: %v", err)
		}
		reader.entries[capTestActorCap] = raw
		if len(grants) > 0 {
			ephRaw, err := json.Marshal(freshEphemeralDoc(grants))
			if err != nil {
				t.Fatalf("marshal ephemeral doc: %v", err)
			}
			reader.entries[capTestActorEph] = ephRaw
		}
	}
	clock := &fakeClock{now: clockAt}
	a := NewCapabilityAuthorizer(reader, "capability-kv", clock, DefaultCapabilityAuthorizerConfig(), capTestLogger())
	return a, emitter, reader
}

// freshEphemeralDoc builds the cap.ephemeral.<actor> entry shape
// (Contract #6 §6.6 amendment): key/actor/version/projectedAt +
// ephemeralGrants only (no roles/serviceAccess/platformPermissions).
func freshEphemeralDoc(grants []EphemeralGrant) *CapabilityDoc {
	return &CapabilityDoc{
		Key:             capTestActorEph,
		Actor:           capTestActorKey,
		Version:         "1.0",
		ProjectedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		EphemeralGrants: grants,
	}
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

// TestCapabilityAuthorizer_TaskPath_SingleGetNoFallback asserts the task
// branch reads ONLY cap.ephemeral.<actor> — a single GET, with no
// cap.<actor> second read (Contract #10 §10.7 / A1).
func TestCapabilityAuthorizer_TaskPath_SingleGetNoFallback(t *testing.T) {
	now := time.Now().UTC()
	a, _, reader := newCapAuthForTest(t, freshDoc(now), now)
	env := envFor("ApproveLeaseApplication", capTestActorKey, &AuthContext{Task: capTestTaskKey, Target: capTestTargetKey})
	dec, err := a.Authorize(context.Background(), env)
	if err != nil || !dec.Authorized {
		t.Fatalf("expected allow; got err=%v dec=%+v", err, dec)
	}
	if len(reader.gets) != 1 {
		t.Fatalf("task path must be a SINGLE GET; got %d reads: %v", len(reader.gets), reader.gets)
	}
	if reader.gets[0] != capTestActorEph {
		t.Fatalf("task path must read the ephemeral key %q; got %q", capTestActorEph, reader.gets[0])
	}
	for _, k := range reader.gets {
		if k == capTestActorCap {
			t.Fatalf("task path must NOT read the primary cap.<actor> key %q (no fallback)", capTestActorCap)
		}
	}
}

// TestCapabilityAuthorizer_TaskPath_AbsentEphemeralKey asserts an absent
// cap.ephemeral.<actor> entry denies with AuthContextMismatch (A3: absence
// = denial), NOT NoCapabilityEntry, and does a single GET.
func TestCapabilityAuthorizer_TaskPath_AbsentEphemeralKey(t *testing.T) {
	now := time.Now().UTC()
	// nil doc → no ephemeral entry seeded.
	a, _, reader := newCapAuthForTest(t, nil, now)
	env := envFor("ApproveLeaseApplication", capTestActorKey, &AuthContext{Task: capTestTaskKey, Target: capTestTargetKey})
	dec, err := a.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("absent ephemeral key must NOT return error; got %v", err)
	}
	if dec.Authorized || dec.Code != ErrCodeAuthContextMismatch {
		t.Fatalf("expected AuthContextMismatch for absent ephemeral key; got %+v", dec)
	}
	if len(reader.gets) != 1 || reader.gets[0] != capTestActorEph {
		t.Fatalf("absent task path must be a single GET of the ephemeral key; got %v", reader.gets)
	}
}

// TestCapabilityAuthorizer_TaskPath_EmptyGrantsDoc asserts an
// ephemeral entry that exists but has no grants denies with
// AuthContextMismatch (A3 — empty-grants doc is denial, defensively).
func TestCapabilityAuthorizer_TaskPath_EmptyGrantsDoc(t *testing.T) {
	now := time.Now().UTC()
	a, _, reader := newCapAuthForTest(t, nil, now)
	emptyEph := freshEphemeralDoc([]EphemeralGrant{})
	raw, _ := json.Marshal(emptyEph)
	reader.entries[capTestActorEph] = raw
	env := envFor("ApproveLeaseApplication", capTestActorKey, &AuthContext{Task: capTestTaskKey, Target: capTestTargetKey})
	dec, err := a.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("empty-grants doc must NOT return error; got %v", err)
	}
	if dec.Authorized || dec.Code != ErrCodeAuthContextMismatch {
		t.Fatalf("expected AuthContextMismatch for empty-grants ephemeral doc; got %+v", dec)
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

// A grossly-stale projectedAt no longer denies: the per-operation freshness
// gate was removed (Story 1.5.4 §3.1). The operation proceeds on a
// permission match and the bounded-staleness window is an accepted risk
// (NFR-S7) backstopped operationally, not by a per-op denial.
func TestCapabilityAuthorizer_StaleProjection_StillAllows(t *testing.T) {
	projected := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	clockAt := projected.Add(10 * time.Second) // far beyond any former ceiling
	a, emitter, _ := newCapAuthForTest(t, freshDoc(projected), clockAt)
	env := envFor("PingPlatform", capTestActorKey, nil)
	dec, err := a.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("stale projection must not deny; got %+v", dec)
	}
	if dec.Resolved == nil || dec.Resolved.ProjectedAt != projected.Format(time.RFC3339Nano) {
		t.Fatalf("expected projectedAt threaded as provenance; got %+v", dec.Resolved)
	}
	if len(emitter.calls) != 0 {
		t.Fatalf("expected no freshness alert; got %+v", emitter.calls)
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
