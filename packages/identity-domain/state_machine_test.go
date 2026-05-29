// Identity Domain DDL & State Machine integration tests for the
// identity-domain Capability Package.
//
// Validates the identity DDL's Starlark script, hydration of state +
// mergedInto aspects, the state-machine validator, and the
// IdentityMerged guard end-to-end through the 10-step Processor
// pipeline.
//
// Coverage:
//  1. TestIdentity_StateMachine_AllowedTransitions  — legal unclaimed -> claimed
//  2. TestIdentity_StateMachine_RejectsDisallowed   — illegal transitions
//  3. TestIdentity_MergedGuard_RejectsMutation      — merged identity rejects
//  4. TestIdentity_FR7_LeaseTombstoneDoesNotCascade — substrate isolation
//  5. TestIdentity_RolePermissionGrantsProjected    — Capability KV audit
//
// FR7 substrate-cascade-isolation tests the underlying KV model
// using an identity vertex as a witness; conceptually it belongs to
// the identity-domain because the FR is phrased in terms of identity
// cascade safety. It does not exercise the identity DDL script.
package identitydomain_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
)

func newIdentityPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "ids-" + durable,
	})
}

// seedIdentityVertex writes an identity vertex + state aspect + (always)
// mergedInto aspect so ContextHint.Reads can include all three.
func seedIdentityVertex(t *testing.T, ctx context.Context, conn *substrate.Conn,
	identityKey, state, mergedInto string) {
	t.Helper()
	vtxDoc := map[string]any{
		"class":     "identity",
		"isDeleted": false,
		"data":      map[string]any{},
	}
	vb, _ := json.Marshal(vtxDoc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey, vb); err != nil {
		t.Fatalf("seed identity vertex %s: %v", identityKey, err)
	}
	stateAspect := map[string]any{
		"class": "state", "vertexKey": identityKey, "localName": "state",
		"isDeleted": false, "data": map[string]any{"value": state},
	}
	sb, _ := json.Marshal(stateAspect)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey+".state", sb); err != nil {
		t.Fatalf("seed state aspect: %v", err)
	}
	miData := map[string]any{}
	if mergedInto != "" {
		miData["value"] = mergedInto
	}
	miAspect := map[string]any{
		"class": "mergedInto", "vertexKey": identityKey, "localName": "mergedInto",
		"isDeleted": false, "data": miData,
	}
	mb, _ := json.Marshal(miAspect)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey+".mergedInto", mb); err != nil {
		t.Fatalf("seed mergedInto aspect: %v", err)
	}
}

// TestIdentity_StateMachine_AllowedTransitions submits UpdateIdentityState
// for the single allowed transition (unclaimed -> claimed) and asserts
// step-8 commit + IdentityStateChanged event.
func TestIdentity_StateMachine_AllowedTransitions(t *testing.T) {
	cases := []struct {
		name      string
		fromState string
		toState   string
		identityID string
		reqLabel  string
	}{
		{"unclaimed-to-claimed", "unclaimed", "claimed", "JdAU1cHJKMNPQRSTUVWX", "AllU2c"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx, conn := setupTestEnv(t)
			cp, cons := newIdentityPipeline(t, ctx, conn, "ids-allow-"+tc.reqLabel)

			identityKey := "vtx.identity." + tc.identityID
			seedIdentityVertex(t, ctx, conn, identityKey, tc.fromState, "")

			env := &processor.OperationEnvelope{
				RequestID:     testutil.GenReqID(tc.reqLabel),
				Lane:          processor.LaneDefault,
				OperationType: "UpdateIdentityState",
				Actor:         staffActorKey,
				SubmittedAt:   "2026-05-22T10:00:00Z",
				Class:         "identity",
				Payload: json.RawMessage(`{"identityKey":"` + identityKey +
					`","newState":"` + tc.toState + `"}`),
				ContextHint: &processor.ContextHint{Reads: []string{
					identityKey + ".state",
					identityKey + ".mergedInto",
				}},
			}
			testutil.PublishOp(t, conn, env)
			testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

			stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
			if got, _ := stateAspect["value"].(string); got != tc.toState {
				t.Fatalf("state = %q, want %q", got, tc.toState)
			}
			assertTrackerEvent(t, ctx, conn, env.RequestID, "IdentityStateChanged")
		})
	}
}

// TestIdentity_StateMachine_RejectsDisallowed asserts ScriptError on
// illegal transitions; no state mutation occurs.
func TestIdentity_StateMachine_RejectsDisallowed(t *testing.T) {
	cases := []struct {
		name      string
		fromState string
		toState   string
		idSuffix  string
		reqLabel  string
	}{
		{"unclaimed-to-merged-illegal", "unclaimed", "merged", "u2m", "DenU2m"},
		{"claimed-to-unclaimed-illegal", "claimed", "unclaimed", "c2u", "DenC2u"},
		{"unclaimed-to-unclaimed-same", "unclaimed", "unclaimed", "u2u", "DenU2u"},
		{"claimed-to-merged-illegal", "claimed", "merged", "c2m", "DenC2m"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx, conn := setupTestEnv(t)
			cp, cons := newIdentityPipeline(t, ctx, conn, "ids-deny-"+tc.idSuffix)

			identityID := testutil.GenReqID("IdD" + tc.idSuffix)
			identityKey := "vtx.identity." + identityID
			seedIdentityVertex(t, ctx, conn, identityKey, tc.fromState, "")

			env := &processor.OperationEnvelope{
				RequestID:     testutil.GenReqID(tc.reqLabel),
				Lane:          processor.LaneDefault,
				OperationType: "UpdateIdentityState",
				Actor:         staffActorKey,
				SubmittedAt:   "2026-05-22T10:00:00Z",
				Class:         "identity",
				Payload: json.RawMessage(`{"identityKey":"` + identityKey +
					`","newState":"` + tc.toState + `"}`),
				ContextHint: &processor.ContextHint{Reads: []string{
					identityKey + ".state",
					identityKey + ".mergedInto",
				}},
			}
			testutil.PublishOp(t, conn, env)
			testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

			stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
			if got, _ := stateAspect["value"].(string); got != tc.fromState {
				t.Fatalf("state mutated despite rejection: %q -> %q", tc.fromState, got)
			}
		})
	}
}

// TestIdentity_MergedGuard_RejectsMutation seeds a merged identity
// and asserts UpdateIdentityState rejects it with no mutation.
func TestIdentity_MergedGuard_RejectsMutation(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newIdentityPipeline(t, ctx, conn, "ids-merged")

	survivorKey := "vtx.identity." + testutil.GenReqID("SurvivorVtx")
	mergedID := testutil.GenReqID("MergedVtx")
	mergedKey := "vtx.identity." + mergedID
	seedIdentityVertex(t, ctx, conn, mergedKey, "merged", survivorKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("MgRq"),
		Lane:          processor.LaneDefault,
		OperationType: "UpdateIdentityState",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T10:00:00Z",
		Class:         "identity",
		Payload: json.RawMessage(`{"identityKey":"` + mergedKey +
			`","newState":"claimed"}`),
		ContextHint: &processor.ContextHint{Reads: []string{
			mergedKey + ".state",
			mergedKey + ".mergedInto",
		}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	stateAspect := readAspectData(t, ctx, conn, mergedKey+".state")
	if got, _ := stateAspect["value"].(string); got != "merged" {
		t.Fatalf("merged identity mutated: state = %q", got)
	}
	_ = survivorKey
}

// TestIdentity_FR7_LeaseTombstoneDoesNotCascade verifies that
// tombstoning a vtx.lease.<X> linked to an identity does NOT mutate
// the identity vertex, its state aspect, or the link envelope.
func TestIdentity_FR7_LeaseTombstoneDoesNotCascade(t *testing.T) {
	ctx, conn := setupTestEnv(t)

	identityID := testutil.GenReqID("FR7IdVtx")
	identityKey := "vtx.identity." + identityID
	seedIdentityVertex(t, ctx, conn, identityKey, "claimed", "")

	leaseID := testutil.GenReqID("FR7LseVtx")
	leaseKey := "vtx.lease." + leaseID
	leaseDoc, _ := json.Marshal(map[string]any{
		"class":     "lease",
		"isDeleted": false,
		"data":      map[string]any{"note": "ad-hoc FR7 test lease"},
	})
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, leaseKey, leaseDoc); err != nil {
		t.Fatalf("seed lease: %v", err)
	}

	linkKey := "lnk.identity." + identityID + ".hasLease.lease." + leaseID
	linkDoc, _ := json.Marshal(map[string]any{
		"class":         "hasLease",
		"isDeleted":     false,
		"youngerVertex": identityKey,
		"olderVertex":   leaseKey,
		"localName":     "hasLease",
		"data":          map[string]any{},
	})
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, linkKey, linkDoc); err != nil {
		t.Fatalf("seed link: %v", err)
	}

	preIdentity, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, identityKey)
	if err != nil {
		t.Fatalf("get identity pre: %v", err)
	}
	preState, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, identityKey+".state")
	if err != nil {
		t.Fatalf("get state pre: %v", err)
	}
	preLink, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, linkKey)
	if err != nil {
		t.Fatalf("get link pre: %v", err)
	}

	tombDoc, _ := json.Marshal(map[string]any{
		"class":     "lease",
		"isDeleted": true,
		"data":      map[string]any{},
	})
	_, err = conn.AtomicBatch([]substrate.BatchOp{
		{Bucket: testutil.HarnessCoreBucket, Key: leaseKey, Value: tombDoc, CreateOnly: false},
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("tombstone batch: %v", err)
	}

	postIdentity, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, identityKey)
	if err != nil {
		t.Fatalf("get identity post: %v", err)
	}
	if postIdentity.Revision != preIdentity.Revision {
		t.Fatalf("identity vertex revision changed: pre=%d post=%d (FR7 cascade observed)",
			preIdentity.Revision, postIdentity.Revision)
	}
	postState, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, identityKey+".state")
	if err != nil {
		t.Fatalf("get state post: %v", err)
	}
	if postState.Revision != preState.Revision {
		t.Fatalf("identity.state revision changed: pre=%d post=%d", preState.Revision, postState.Revision)
	}
	postLink, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, linkKey)
	if err != nil {
		t.Fatalf("get link post: %v", err)
	}
	if postLink.Revision != preLink.Revision {
		t.Fatalf("link revision changed: pre=%d post=%d (FR7 cascade observed on link)",
			preLink.Revision, postLink.Revision)
	}
	postLease, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, leaseKey)
	if err != nil {
		t.Fatalf("get lease post: %v", err)
	}
	var leaseEnv map[string]any
	_ = json.Unmarshal(postLease.Value, &leaseEnv)
	if del, _ := leaseEnv["isDeleted"].(bool); !del {
		t.Fatalf("lease should be tombstoned: %+v", leaseEnv)
	}
}

// TestIdentity_RolePermissionGrantsProjected asserts the staff cap doc
// (seeded by setupTestEnv via staffCapDoc) carries the staff
// platformPermissions. This is the per-test fixture audit; in
// production the same shape comes from the Capability Lens projection
// over the rbac-domain + identity-domain installed packages.
func TestIdentity_RolePermissionGrantsProjected(t *testing.T) {
	ctx, conn := setupTestEnv(t)

	js := conn.JetStream()
	capKV, err := js.KeyValue(ctx, testutil.HarnessCapBucket)
	if err != nil {
		t.Fatalf("open capability-kv: %v", err)
	}
	entry, err := capKV.Get(ctx, staffCapKey)
	if err != nil {
		t.Fatalf("get staff cap entry: %v", err)
	}
	var doc processor.CapabilityDoc
	if err := json.Unmarshal(entry.Value(), &doc); err != nil {
		t.Fatalf("unmarshal cap doc: %v", err)
	}
	expected := []string{"CreateUnclaimedIdentity"}
	permMap := map[string]string{}
	for _, p := range doc.PlatformPermissions {
		permMap[p.OperationType] = p.Scope
	}
	for _, op := range expected {
		if _, ok := permMap[op]; !ok {
			t.Errorf("staff cap doc missing operationType %q", op)
		}
	}
}
