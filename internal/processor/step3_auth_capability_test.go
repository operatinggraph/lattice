// Unit tests for CapabilityAuthorizer.
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
	"strings"
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
	capTestActorSvc   = "cap.svc.identity." + capTestActorID       // disjoint service-access key
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
		// The disjoint-key contribution model (Contract #6 §6.1): each grant
		// source projects to its own key. Split the single fixture doc across
		// the keys the dispatcher actually reads per path:
		//   - ephemeralGrants → cap.ephemeral.<actor> (orchestration-base lens).
		//   - serviceAccess   → cap.svc.<actor>       (service-location lens).
		// The primary cap.<actor> doc keeps platformPermissions / roles only.
		grants := doc.EphemeralGrants
		svcAccess := doc.ServiceAccess
		primary := *doc
		primary.EphemeralGrants = nil
		primary.ServiceAccess = nil
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
		if len(svcAccess) > 0 {
			svcRaw, err := json.Marshal(freshServiceDoc(svcAccess))
			if err != nil {
				t.Fatalf("marshal service doc: %v", err)
			}
			reader.entries[capTestActorSvc] = svcRaw
		}
	}
	clock := &fakeClock{now: clockAt}
	a, err := newCapabilityAuthorizer(reader, "capability-kv", clock, DefaultCapabilityAuthorizerConfig(), capTestLogger(),
		capabilityAuthorizerOptions{emitter: emitter})
	if err != nil {
		t.Fatalf("newCapabilityAuthorizer: %v", err)
	}
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

// freshServiceDoc builds the cap.svc.<actor> entry shape (Contract #6 §6.1 /
// §6.5, service-location's capabilityServiceAccess lens): key/actor/version/
// projectedAt + serviceAccess only (no roles/ephemeralGrants/
// platformPermissions). The service path reads this disjoint key.
func freshServiceDoc(svc []ServiceAccessEntry) *CapabilityDoc {
	return &CapabilityDoc{
		Key:           capTestActorSvc,
		Actor:         capTestActorKey,
		Version:       "1.0",
		ProjectedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		ServiceAccess: svc,
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
				Service:     capTestServiceKey,
				ResolvedVia: []string{"vtx.unit.penthouse"},
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

// TestCapabilityAuthorizer_ServicePath_ReadsServiceKey proves the re-point: the
// service path's single KV GET targets the disjoint cap.svc.<actor> key
// (service-location's projection), NOT the core cap.<actor> key. Exactly one
// key is read (one-key-per-path).
func TestCapabilityAuthorizer_ServicePath_ReadsServiceKey(t *testing.T) {
	now := time.Now().UTC()
	a, _, reader := newCapAuthForTest(t, freshDoc(now), now)
	env := envFor("BookExecutiveCleaning", capTestActorKey, &AuthContext{Service: capTestServiceKey})
	dec, err := a.Authorize(context.Background(), env)
	if err != nil || !dec.Authorized {
		t.Fatalf("expected allow; got err=%v dec=%+v", err, dec)
	}
	if dec.Resolved.CapKey != capTestActorSvc {
		t.Fatalf("service path must read %q; got CapKey=%q", capTestActorSvc, dec.Resolved.CapKey)
	}
	if len(reader.gets) != 1 || reader.gets[0] != capTestActorSvc {
		t.Fatalf("service path must issue exactly one GET against %q; got %v", capTestActorSvc, reader.gets)
	}
}

// TestCapabilityAuthorizer_ServicePath_DenyByAbsence proves a service op denies
// by absence when no cap.svc.<actor> projection exists (Contract #6 §6.8): an
// actor with a primary cap.<actor> doc but NO cap.svc key is denied on the
// service path. A platform op for the SAME actor still authorizes — the absence
// is path-local to the service key.
func TestCapabilityAuthorizer_ServicePath_DenyByAbsence(t *testing.T) {
	now := time.Now().UTC()
	// Seed only the primary doc (no serviceAccess → no cap.svc key written).
	doc := freshDoc(now)
	doc.ServiceAccess = nil
	a, _, reader := newCapAuthForTest(t, doc, now)
	env := envFor("BookExecutiveCleaning", capTestActorKey, &AuthContext{Service: capTestServiceKey})
	dec, err := a.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("service op must deny by absence with no cap.svc projection; got %+v", dec)
	}
	if dec.Reason != "NoCapabilityEntry" {
		t.Fatalf("expected NoCapabilityEntry deny-by-absence; got code=%s reason=%s", dec.Code, dec.Reason)
	}
	if len(reader.gets) != 1 || reader.gets[0] != capTestActorSvc {
		t.Fatalf("service deny-by-absence must read exactly cap.svc.<actor>; got %v", reader.gets)
	}
}

// TestCapabilityAuthorizer_ServicePath_SystemActorDeniesByAbsence proves the
// re-point is UNCONDITIONAL: a system (kernel-seeded) actor that drives the
// service path (sets ac.Service) reads cap.svc.<systemActor> like any other
// actor and denies by absence when that key is missing. System actors never
// set ac.Service in production, but the dispatch must not special-case them off
// the service key.
func TestCapabilityAuthorizer_ServicePath_SystemActorDeniesByAbsence(t *testing.T) {
	const systemActorID = "LoomSvcActorAaBbCcDd"
	systemActorKey := "vtx.identity." + systemActorID
	// Seed the system actor's PLATFORM cap doc (cap.identity.<id>) granting it
	// root ops — but NO cap.svc.<id>. A platform op would pass; a service op
	// must deny by absence.
	reader := &fakeReader{entries: map[string][]byte{}}
	platDoc := rootEquivalentDoc(systemActorID)
	raw, err := json.Marshal(platDoc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	reader.entries[platDoc.Key] = raw
	a, err := NewCapabilityAuthorizer(reader, "capability-kv", &fakeClock{now: time.Now()},
		DefaultCapabilityAuthorizerConfig(), capTestLogger())
	if err != nil {
		t.Fatalf("NewCapabilityAuthorizer: %v", err)
	}

	env := &OperationEnvelope{
		RequestID:     "ReqSysSvcDenyAaBbCc0",
		Lane:          LaneDefault,
		OperationType: "BookExecutiveCleaning",
		Actor:         systemActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		AuthContext:   &AuthContext{Service: "vtx.service.someService"},
	}
	dec, err := a.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("system actor on the service path must deny by absence of cap.svc; got %+v", dec)
	}
	if dec.Reason != "NoCapabilityEntry" {
		t.Fatalf("expected NoCapabilityEntry; got code=%s reason=%s", dec.Code, dec.Reason)
	}
	if len(reader.gets) != 1 || reader.gets[0] != "cap.svc.identity."+systemActorID {
		t.Fatalf("system actor service path must read cap.svc.identity.<id>; got %v", reader.gets)
	}
}

// TestCapabilityAuthorizer_ServicePath_TombstonedSvcDoc proves the service-plane
// resurrection guard at the auth boundary: a soft-tombstoned cap.svc.<actor>
// doc (empty serviceAccess body — what the lens's emptyBehavior:delete leaves
// transiently, or a stale availableAt-era doc replayed after the residence/
// availability was withdrawn) authorizes NOTHING. The matcher reads the body,
// finds no serviceAccess entry, and denies — an availableAt-era grant cannot
// resurrect access once the topology that produced it is gone.
func TestCapabilityAuthorizer_ServicePath_TombstonedSvcDoc(t *testing.T) {
	now := time.Now().UTC()
	doc := freshDoc(now)
	// Build an authorizer whose cap.svc key carries an EMPTY serviceAccess body
	// (the tombstone shape) even though an earlier availableAt-era doc granted
	// the service. The empty body must not authorize.
	a, _, reader := newCapAuthForTest(t, doc, now)
	emptySvc := freshServiceDoc([]ServiceAccessEntry{})
	raw, err := json.Marshal(emptySvc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	reader.entries[capTestActorSvc] = raw

	env := envFor("BookExecutiveCleaning", capTestActorKey, &AuthContext{Service: capTestServiceKey})
	dec, err := a.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("an empty/tombstoned cap.svc body must NOT authorize (no resurrection); got %+v", dec)
	}
	if dec.Code != ErrCodeAuthContextMismatch {
		t.Fatalf("expected AuthContextMismatch (service not in serviceAccess); got %+v", dec)
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

func TestServiceKeyFromActor(t *testing.T) {
	got, err := serviceKeyFromActor("vtx.identity.ABC")
	if err != nil || got != "cap.svc.identity.ABC" {
		t.Fatalf("got (%q,%v), want (cap.svc.identity.ABC, nil)", got, err)
	}
	if _, err := serviceKeyFromActor("ABC"); err == nil {
		t.Fatalf("expected error for malformed actor")
	}
	if _, err := serviceKeyFromActor(""); err == nil {
		t.Fatalf("expected error for empty actor")
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

// TestAuthRegistry_DuplicatePathRejected proves AC4: two entries selecting the
// same path are a configuration error surfaced at authorizer construction
// (fail-closed), never resolved by issuing N reads or merging docs.
func TestAuthRegistry_DuplicatePathRejected(t *testing.T) {
	reader := &fakeReader{entries: map[string][]byte{}}
	dupe := authEntry{
		name:            "platform", // collides with the core platform seed entry
		selects:         func(ac *AuthContext) bool { return true },
		kind:            matchPlatformPermissionKind,
		keyDerivation:   capabilityKeyFromActor,
		absentKeyCode:   ErrCodeAuthDenied,
		absentKeyReason: "NoCapabilityEntry",
	}
	a, err := NewCapabilityAuthorizer(reader, "capability-kv", &fakeClock{now: time.Now()},
		DefaultCapabilityAuthorizerConfig(), capTestLogger(), dupe)
	if err == nil {
		t.Fatalf("duplicate path must be rejected at construction; got authorizer %v", a)
	}
	if a != nil {
		t.Fatalf("rejected construction must return a nil authorizer; got %v", a)
	}
}

// TestAuthRegistry_ExtensionPoint_RoutesNewPath proves AC5: a data-declared
// entry binding a NEW path predicate → an existing core matcher kind (platform)
// → a NEW disjoint key derivation routes correctly and matches with NO edit to
// the matcher-kind implementation. This is the shape Story 12.6 uses.
func TestAuthRegistry_ExtensionPoint_RoutesNewPath(t *testing.T) {
	now := time.Now().UTC()

	// A throwaway disjoint key + the doc seeded there. The new path is selected
	// when authContext.target carries a sentinel marker; it reuses the platform
	// matcher kind unchanged.
	const extKey = "cap.ext.identity." + capTestActorID
	const extMarker = "vtx.ext-route.marker"

	extDoc := &CapabilityDoc{
		Key:         extKey,
		Actor:       capTestActorKey,
		Version:     "1.0",
		ProjectedAt: now.Format(time.RFC3339Nano),
		// A platform-path read-model doc (incl. a scoped package extra) is subject
		// to the step-3 lane gate, so it must carry its own lane grant.
		Lanes: []string{"default"},
		PlatformPermissions: []PlatformPermission{
			{OperationType: "ExtRoutedOp", Scope: "any"},
		},
	}
	extRaw, err := json.Marshal(extDoc)
	if err != nil {
		t.Fatalf("marshal ext doc: %v", err)
	}

	reader := &fakeReader{entries: map[string][]byte{extKey: extRaw}}
	extEntry := authEntry{
		name:    "ext-route",
		selects: func(ac *AuthContext) bool { return ac != nil && ac.Target == extMarker },
		kind:    matchPlatformPermissionKind,
		keyDerivation: func(actor string) (string, error) {
			rest, ok := strings.CutPrefix(actor, "vtx.")
			if !ok {
				return "", errors.New("bad actor")
			}
			return "cap.ext." + rest, nil
		},
		absentKeyCode:   ErrCodeAuthDenied,
		absentKeyReason: "NoCapabilityEntry",
		coverage:        authCoverage{kind: pathPlatform, scopeTag: "ext-route"},
	}
	a, err := NewCapabilityAuthorizer(reader, "capability-kv", &fakeClock{now: now},
		DefaultCapabilityAuthorizerConfig(), capTestLogger(), extEntry)
	if err != nil {
		t.Fatalf("NewCapabilityAuthorizer with extra entry: %v", err)
	}

	env := envFor("ExtRoutedOp", capTestActorKey, &AuthContext{Target: extMarker})
	dec, err := a.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("expected the new path to authorize; got %+v", dec)
	}
	if dec.Resolved == nil || dec.Resolved.Path != "platform" {
		t.Fatalf("expected platform matcher kind to set Path=platform; got %+v", dec.Resolved)
	}
	if dec.Resolved.CapKey != extKey {
		t.Fatalf("expected the new disjoint key %q to back the decision; got %q", extKey, dec.Resolved.CapKey)
	}
	// Exactly one GET, of the new disjoint key — one-key-per-path holds.
	if len(reader.gets) != 1 || reader.gets[0] != extKey {
		t.Fatalf("extension path must be a single GET of the new key; got %v", reader.gets)
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

// --- Fire 2: lane authorization gate (Contract #2 §2.3) --------------------
//
// The step-3 lane gate: the platform path's lane authority is the actor's
// standing doc.Lanes (post-parse, pre-matcher, fail-closed); the service and
// task paths confer the `default` lane only (pre-read reject on non-default).

// envForLane is envFor with an explicit lane (envFor hardcodes LaneDefault).
func envForLane(opType, actor string, lane Lane, ac *AuthContext) *OperationEnvelope {
	e := envFor(opType, actor, ac)
	e.Lane = lane
	return e
}

func TestCapabilityAuthorizer_LaneGate_PlatformLaneGranted(t *testing.T) {
	now := time.Now().UTC()
	doc := freshDoc(now)
	doc.Lanes = []string{"default", "meta", "urgent", "system"}
	a, _, _ := newCapAuthForTest(t, doc, now)
	// A granted non-default lane passes the gate and falls through to the matcher.
	env := envForLane("PingPlatform", capTestActorKey, LaneSystem, nil)
	dec, err := a.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("expected a granted lane to pass the gate and authorize; got %+v", dec)
	}
}

func TestCapabilityAuthorizer_LaneGate_PlatformLaneUngranted(t *testing.T) {
	now := time.Now().UTC()
	a, _, _ := newCapAuthForTest(t, freshDoc(now), now) // doc.Lanes = ["default"]
	env := envForLane("PingPlatform", capTestActorKey, LaneSystem, nil)
	dec, _ := a.Authorize(context.Background(), env)
	if dec.Authorized || dec.Code != ErrCodeLaneUnauthorized {
		t.Fatalf("expected LaneUnauthorized for an ungranted lane; got %+v", dec)
	}
}

func TestCapabilityAuthorizer_LaneGate_PlatformEmptyLanesFailClosed(t *testing.T) {
	now := time.Now().UTC()
	doc := freshDoc(now)
	doc.Lanes = nil
	a, _, _ := newCapAuthForTest(t, doc, now)
	// Fail-closed: an empty granted set denies even the default lane.
	env := envForLane("PingPlatform", capTestActorKey, LaneDefault, nil)
	dec, _ := a.Authorize(context.Background(), env)
	if dec.Authorized || dec.Code != ErrCodeLaneUnauthorized {
		t.Fatalf("expected fail-closed LaneUnauthorized on empty doc.Lanes; got %+v", dec)
	}
}

func TestCapabilityAuthorizer_LaneGate_ServiceNonDefaultRejectedNoRead(t *testing.T) {
	now := time.Now().UTC()
	a, _, reader := newCapAuthForTest(t, freshDoc(now), now)
	env := envForLane("BookExecutiveCleaning", capTestActorKey, LaneSystem, &AuthContext{Service: capTestServiceKey})
	dec, _ := a.Authorize(context.Background(), env)
	if dec.Authorized || dec.Code != ErrCodeLaneUnauthorized {
		t.Fatalf("expected LaneUnauthorized for a service-path non-default lane; got %+v", dec)
	}
	if len(reader.gets) != 0 {
		t.Fatalf("the service-path lane reject must precede the read (no GET); got %v", reader.gets)
	}
}

func TestCapabilityAuthorizer_LaneGate_TaskNonDefaultRejectedNoRead(t *testing.T) {
	now := time.Now().UTC()
	a, _, reader := newCapAuthForTest(t, freshDoc(now), now)
	env := envForLane("ApproveLeaseApplication", capTestActorKey, LaneMeta,
		&AuthContext{Task: capTestTaskKey, Target: capTestTargetKey})
	dec, _ := a.Authorize(context.Background(), env)
	if dec.Authorized || dec.Code != ErrCodeLaneUnauthorized {
		t.Fatalf("expected LaneUnauthorized for a task-path non-default lane; got %+v", dec)
	}
	if len(reader.gets) != 0 {
		t.Fatalf("the task-path lane reject must precede the read (no GET); got %v", reader.gets)
	}
}

// TestCapabilityAuthorizer_LaneGate_PerOpLanesNarrowerThanDoc proves C1's
// per-matched-permission gate (scoped-privileged-lane-grants-design.md): an
// entry's own Lanes, when present, are the authority for that op — even a
// lane the doc otherwise grants is denied if the matched entry doesn't carry
// it.
func TestCapabilityAuthorizer_LaneGate_PerOpLanesNarrowerThanDoc(t *testing.T) {
	now := time.Now().UTC()
	doc := freshDoc(now)
	doc.Lanes = []string{"default", "meta"}
	doc.PlatformPermissions = append(doc.PlatformPermissions,
		PlatformPermission{OperationType: "DefaultOnlyOp", Scope: "any", Lanes: []string{"default"}})
	a, _, _ := newCapAuthForTest(t, doc, now)

	env := envForLane("DefaultOnlyOp", capTestActorKey, LaneMeta, nil)
	dec, err := a.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if dec.Authorized || dec.Code != ErrCodeLaneUnauthorized {
		t.Fatalf("expected LaneUnauthorized: entry's own Lanes=[default] must override doc.Lanes; got %+v", dec)
	}
}

// TestCapabilityAuthorizer_LaneGate_PerOpLanesGrantedFallsThrough proves the
// same entry's own lane, when it IS in its Lanes, authorizes normally.
func TestCapabilityAuthorizer_LaneGate_PerOpLanesGrantedFallsThrough(t *testing.T) {
	now := time.Now().UTC()
	doc := freshDoc(now)
	doc.Lanes = []string{"default", "meta"}
	doc.PlatformPermissions = append(doc.PlatformPermissions,
		PlatformPermission{OperationType: "DefaultOnlyOp", Scope: "any", Lanes: []string{"default"}})
	a, _, _ := newCapAuthForTest(t, doc, now)

	env := envForLane("DefaultOnlyOp", capTestActorKey, LaneDefault, nil)
	dec, err := a.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("expected authorized: default is in the entry's own Lanes; got %+v", dec)
	}
	if dec.Resolved == nil || dec.Resolved.PlatformPermission == nil || dec.Resolved.PlatformPermission.OperationType != "DefaultOnlyOp" {
		t.Fatalf("expected Resolved.PlatformPermission=DefaultOnlyOp; got %+v", dec.Resolved)
	}
}

// TestCapabilityAuthorizer_LaneGate_PerOpLanesAbsentFallsBackToDoc proves an
// entry with no Lanes of its own still defers to doc.Lanes (the pre-C1
// behavior, unchanged) — Fire 1 is additive, not a behavior change for
// today's permissions (none set Lanes yet).
func TestCapabilityAuthorizer_LaneGate_PerOpLanesAbsentFallsBackToDoc(t *testing.T) {
	now := time.Now().UTC()
	doc := freshDoc(now)
	doc.Lanes = []string{"default", "system"}
	a, _, _ := newCapAuthForTest(t, doc, now)

	env := envForLane("PingPlatform", capTestActorKey, LaneSystem, nil)
	dec, err := a.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("expected authorized via doc.Lanes fallback (entry carries no Lanes); got %+v", dec)
	}
}

func TestCapabilityAuthorizer_LaneGate_ServiceDefaultPasses(t *testing.T) {
	now := time.Now().UTC()
	a, _, _ := newCapAuthForTest(t, freshDoc(now), now)
	// Default lane on the service path is unaffected by the gate.
	env := envForLane("BookExecutiveCleaning", capTestActorKey, LaneDefault, &AuthContext{Service: capTestServiceKey})
	dec, err := a.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("expected service-path default lane to pass the gate and authorize; got %+v", dec)
	}
}

func TestStubAuthorizer_BypassesLaneGate(t *testing.T) {
	stub := NewStubAuthorizerWithEmitter(capTestLogger(), &recordingEmitter{})
	// The degraded stub mode bypasses all auth, including the lane gate.
	env := envForLane("X", capTestActorKey, LaneSystem, nil)
	dec, err := stub.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("stub Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("stub must bypass the lane gate (allow-all degraded mode); got %+v", dec)
	}
}

// --- Fire 2: core privileged-lane allowlist (scoped-privileged-lane-grants-
// design.md §3.3) -----------------------------------------------------------
//
// A per-op Lanes entry is always package-projected (cap.roles.<actor>, per
// §3.3's grounding — the anchor's own cypher-projected entries never carry
// per-op lanes). An allowlisted {operationType, lane} pair is honored
// verbatim; a non-allowlisted privileged lane is stripped to default and
// raises PrivilegedLaneGrantRejected — never silently honored.

func TestCapabilityAuthorizer_LaneGate_AllowlistedPrivilegedLaneGranted(t *testing.T) {
	now := time.Now().UTC()
	doc := freshDoc(now)
	doc.PlatformPermissions = append(doc.PlatformPermissions,
		PlatformPermission{OperationType: "InstallPackage", Scope: "any", Lanes: []string{"meta"}})
	a, emitter, _ := newCapAuthForTest(t, doc, now)

	env := envForLane("InstallPackage", capTestActorKey, LaneMeta, nil)
	dec, err := a.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("expected authorized: InstallPackage@meta is allowlisted (v1); got %+v", dec)
	}
	if len(emitter.calls) != 0 {
		t.Fatalf("expected no alert for an allowlisted privileged grant; got %+v", emitter.calls)
	}
}

func TestCapabilityAuthorizer_LaneGate_NonAllowlistedPrivilegedLaneStripped(t *testing.T) {
	now := time.Now().UTC()
	doc := freshDoc(now)
	doc.PlatformPermissions = append(doc.PlatformPermissions,
		PlatformPermission{OperationType: "TombstoneMetaVertex", Scope: "any", Lanes: []string{"meta"}})
	a, emitter, _ := newCapAuthForTest(t, doc, now)

	// The rogue grant claims meta but TombstoneMetaVertex@meta isn't
	// allowlisted — stripped to default, so a meta submission denies.
	env := envForLane("TombstoneMetaVertex", capTestActorKey, LaneMeta, nil)
	dec, _ := a.Authorize(context.Background(), env)
	if dec.Authorized || dec.Code != ErrCodeLaneUnauthorized {
		t.Fatalf("expected LaneUnauthorized: non-allowlisted privileged lane must be stripped; got %+v", dec)
	}
	if len(emitter.calls) != 1 || emitter.calls[0].code != AlertCodePrivilegedLaneGrantRejected {
		t.Fatalf("expected one privileged-lane-grant-rejected alert; got %+v", emitter.calls)
	}
	if got := emitter.calls[0].details["operationType"]; got != "TombstoneMetaVertex" {
		t.Fatalf("expected alert details.operationType=TombstoneMetaVertex; got %+v", emitter.calls[0].details)
	}
}

func TestCapabilityAuthorizer_LaneGate_NonAllowlistedPrivilegedLaneStillGrantsDefault(t *testing.T) {
	now := time.Now().UTC()
	doc := freshDoc(now)
	doc.PlatformPermissions = append(doc.PlatformPermissions,
		PlatformPermission{OperationType: "TombstoneMetaVertex", Scope: "any", Lanes: []string{"meta"}})
	a, _, _ := newCapAuthForTest(t, doc, now)

	// Stripped to default, not to nothing: a default submission still works.
	env := envForLane("TombstoneMetaVertex", capTestActorKey, LaneDefault, nil)
	dec, err := a.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("expected the stripped grant to still confer default; got %+v", dec)
	}
}

func TestCapabilityAuthorizer_LaneGate_AllowlistIsPerOperation(t *testing.T) {
	now := time.Now().UTC()
	doc := freshDoc(now)
	// InstallPackage@meta is allowlisted; InstallPackage@urgent is not.
	doc.PlatformPermissions = append(doc.PlatformPermissions,
		PlatformPermission{OperationType: "InstallPackage", Scope: "any", Lanes: []string{"urgent"}})
	a, emitter, _ := newCapAuthForTest(t, doc, now)

	env := envForLane("InstallPackage", capTestActorKey, LaneUrgent, nil)
	dec, _ := a.Authorize(context.Background(), env)
	if dec.Authorized || dec.Code != ErrCodeLaneUnauthorized {
		t.Fatalf("expected LaneUnauthorized: InstallPackage@urgent is not on the v1 allowlist; got %+v", dec)
	}
	if len(emitter.calls) != 1 || emitter.calls[0].code != AlertCodePrivilegedLaneGrantRejected {
		t.Fatalf("expected one privileged-lane-grant-rejected alert; got %+v", emitter.calls)
	}
}

func TestCapabilityAuthorizer_LaneGate_AnchorRootGrantNeverAllowlistChecked(t *testing.T) {
	now := time.Now().UTC()
	doc := freshDoc(now)
	// The anchor floor: doc-level Lanes carries all four; no entry sets its
	// own per-op Lanes (mirrors bootstrap/lenses.go's CapabilityLensDefinition
	// — the root grant is never entry-scoped).
	doc.Lanes = []string{"default", "meta", "urgent", "system"}
	// TombstoneMetaVertex isn't on the privileged-lane allowlist at all, but
	// the anchor's root grant reaches lane authority via the doc.Lanes
	// fallback (no entry-level Lanes), which the allowlist never touches.
	doc.PlatformPermissions = append(doc.PlatformPermissions,
		PlatformPermission{OperationType: "TombstoneMetaVertex", Scope: "any"})
	a, emitter, _ := newCapAuthForTest(t, doc, now)

	env := envForLane("TombstoneMetaVertex", capTestActorKey, LaneMeta, nil)
	dec, err := a.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("expected the anchor's doc.Lanes-fallback grant to authorize unchecked; got %+v", dec)
	}
	if len(emitter.calls) != 0 {
		t.Fatalf("expected no alert for a doc.Lanes-fallback (anchor) grant; got %+v", emitter.calls)
	}
}

// --- Fire 3: consoleOperator-shaped pkg-lifecycle grant
// (scoped-privileged-lane-grants-design.md §7 item 3 / §9 triad) ----------
//
// consoleOperatorDoc mirrors the actual cap.roles.<actor> shape
// packages/console-operator projects for a consoleOperator holder: an
// ORDINARY actor (doc.Lanes = ["default"] only, no anchor, no
// SystemActorKeys) whose PlatformPermissions carry the allowlisted
// pkg-lifecycle trio with their own per-op Lanes:["meta"] — never a
// doc-level privileged lane.

func consoleOperatorDoc(now time.Time) *CapabilityDoc {
	doc := freshDoc(now)
	doc.Lanes = []string{"default"}
	trio := []string{"InstallPackage", "UninstallPackage", "UpgradePackage"}
	for _, op := range trio {
		doc.PlatformPermissions = append(doc.PlatformPermissions,
			PlatformPermission{OperationType: op, Scope: "any", Lanes: []string{"meta"}})
	}
	return doc
}

func TestCapabilityAuthorizer_ConsoleOperatorPkgLifecycleGrant_InstallPackageAtMetaAllowed(t *testing.T) {
	now := time.Now().UTC()
	a, emitter, _ := newCapAuthForTest(t, consoleOperatorDoc(now), now)

	env := envForLane("InstallPackage", capTestActorKey, LaneMeta, nil)
	dec, err := a.Authorize(context.Background(), env)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("expected a consoleOperator-shaped grant to authorize InstallPackage@meta; got %+v", dec)
	}
	if len(emitter.calls) != 0 {
		t.Fatalf("expected no PrivilegedLaneGrantRejected alert for the allowlisted grant; got %+v", emitter.calls)
	}
}

func TestCapabilityAuthorizer_ConsoleOperatorPkgLifecycleGrant_UngrantedMetaOpDenied(t *testing.T) {
	now := time.Now().UTC()
	a, _, _ := newCapAuthForTest(t, consoleOperatorDoc(now), now)

	// CreateMetaVertex is never granted to consoleOperator — no matching
	// PlatformPermission entry at all, regardless of lane.
	env := envForLane("CreateMetaVertex", capTestActorKey, LaneMeta, nil)
	dec, _ := a.Authorize(context.Background(), env)
	if dec.Authorized || dec.Code != ErrCodeAuthDenied {
		t.Fatalf("expected AuthDenied for an ungranted op; got %+v", dec)
	}
}

func TestCapabilityAuthorizer_ConsoleOperatorPkgLifecycleGrant_UrgentAndSystemLanesUnauthorized(t *testing.T) {
	now := time.Now().UTC()
	for _, lane := range []Lane{LaneUrgent, LaneSystem} {
		a, emitter, _ := newCapAuthForTest(t, consoleOperatorDoc(now), now)
		env := envForLane("InstallPackage", capTestActorKey, lane, nil)
		dec, _ := a.Authorize(context.Background(), env)
		// consoleOperator's grant only ever carries Lanes:["meta"] — urgent/
		// system are simply absent from the granted set (never a rogue
		// privileged claim needing the allowlist's strip-and-alert path), so
		// this denies via the plain lane-membership check, no alert.
		if dec.Authorized || dec.Code != ErrCodeLaneUnauthorized {
			t.Fatalf("lane %s: expected LaneUnauthorized (InstallPackage is only allowlisted at meta); got %+v", lane, dec)
		}
		if len(emitter.calls) != 0 {
			t.Fatalf("lane %s: expected no alert (the grant never claimed this lane); got %+v", lane, emitter.calls)
		}
	}
}
