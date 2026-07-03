// Gate 4 Integration Test — Compensating Operation & DDL Rollback (FR53).
//
// This test is the Phase 1 Gate 4 specified in epics.md §Story 5.3 AC6.
// It exercises the full create → discover → read-compensation → rollback
// cycle without any platform restart, data surgery, or out-of-band
// intervention.
//
// Test sequence:
//  1. SetupPackageTestEnv: bootstrap + rbac-domain + identity-domain packages installed.
//  2. Submit forward CreateMetaVertex for a new DDL. Capture metaKey from tracker.
//  3. Verify DDL is discoverable: DiscoverDDL returns metaKey.
//  4. Call ReadCompensation: assert inverseOperationType == "TombstoneMetaVertex".
//  5. Construct TombstoneMetaVertex payload with expectedRevision from Core KV entry.
//  6. Submit the compensating TombstoneMetaVertex op. Assert OutcomeAccepted.
//  7. Verify DDL is no longer discoverable: DiscoverDDL returns ErrDDLNotFound.
//  8. Verify the tombstoned vertex has isDeleted: true in Core KV.
//  9. Verify same canonicalName can be re-created (idempotency is per requestId).
// 10. Repeat steps 2–9 for a meta.lens vertex.
// 11. Write health.gates.phase1.gate4 to Health KV on success.
//
// Design principles enforced:
//   - No new OperationReply fields (Guardrail 1): metaKey is read from
//     the op tracker's mutationKeys[0], not from a new reply field.
//   - No new Processor read surface (Guardrail 2): ReadCompensation is
//     client-side aiagent.Traverser only.
//   - Same Processor commit path for both forward and compensating ops (NFR-S10).
//   - No platform restart between forward and compensating ops.
package aiagent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/aiagent"
	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
)

// gate4SeederActorID is the test seeder identity for Gate 4.
const gate4SeederActorID = "Gate4SeedrActrID00001"

// TestGate4_CompensatingOpRollback is the Gate 4 integration test for FR53.
func TestGate4_CompensatingOpRollback(t *testing.T) {
	ctx, conn := testutil.SetupPackageTestEnv(t)

	seederActorKey := "vtx.identity." + gate4SeederActorID

	// Seeder needs CreateMetaVertex AND TombstoneMetaVertex permissions.
	seederCapDoc := buildCapDoc(seederActorKey, "cap.identity."+gate4SeederActorID,
		[]processor.PlatformPermission{
			{OperationType: "CreateMetaVertex", Scope: "any"},
			{OperationType: "UpdateMetaVertex", Scope: "any"},
			{OperationType: "TombstoneMetaVertex", Scope: "any"},
		},
		[]string{bootstrap.RoleOperatorKey})
	testutil.SeedCapDoc(t, ctx, conn, seederCapDoc)

	// Meta-lane pipeline for CreateMetaVertex + TombstoneMetaVertex.
	// Re-use testutil.PipelineConfig.FilterSubjects pattern from Story 5.2.
	metaCP, metaCons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:        "gate4-meta-pipeline",
		Instance:       "gate4-meta",
		FilterSubjects: []string{"ops.meta"},
	})

	tr := aiagent.NewTraverser(conn, testutil.HarnessCoreBucket, testutil.HarnessCapBucket)

	// === Sub-test A: DDL vertex type (is_ddl_class branch, AC6 steps 2–9) ===
	t.Run("DDL_VertexType", func(t *testing.T) {
		canonicalName := "RollbackTestDDL"
		payload := buildGate4VertexTypePayload(t, canonicalName)

		// Step 2: create + capture metaKey from tracker.
		metaKey := gate4SubmitAndCapture(t, ctx, conn, seederActorKey,
			metaCP, metaCons, payload, "Gate4DDLCreate0001")
		if !isMetaKey(metaKey) {
			t.Fatalf("captured metaKey %q is not a vtx.meta.* key", metaKey)
		}

		// Step 3: verify discoverable.
		gotKey, err := tr.DiscoverDDL(ctx, canonicalName)
		if err != nil {
			t.Fatalf("DiscoverDDL after create: %v", err)
		}
		if gotKey != metaKey {
			t.Fatalf("DiscoverDDL key mismatch: got %q seeder wrote %q", gotKey, metaKey)
		}

		// Step 4: read compensation aspect.
		compData, err := tr.ReadCompensation(ctx, metaKey)
		if err != nil {
			t.Fatalf("ReadCompensation: %v", err)
		}
		if compData["inverseOperationType"] != "TombstoneMetaVertex" {
			t.Fatalf("inverseOperationType: got %v want TombstoneMetaVertex", compData["inverseOperationType"])
		}

		// Step 5: get current revision of the meta-vertex for conflict detection.
		vtxEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, metaKey)
		if err != nil {
			t.Fatalf("KVGet meta-vertex for revision: %v", err)
		}
		expectedRevision := int(vtxEntry.Revision)

		// Step 6: submit compensating TombstoneMetaVertex op with expectedRevision.
		tombPayload := map[string]any{
			"metaKey":          metaKey,
			"expectedRevision": expectedRevision,
		}
		gate4Tombstone(t, ctx, conn, seederActorKey, metaCP, metaCons,
			tombPayload, "Gate4DDLTombstn0001")

		// Step 7: DDL should no longer be discoverable.
		_, err = tr.DiscoverDDL(ctx, canonicalName)
		if err == nil {
			t.Fatal("DiscoverDDL after tombstone: expected ErrDDLNotFound, got nil")
		}
		if !errors.Is(err, aiagent.ErrDDLNotFound) {
			t.Fatalf("DiscoverDDL after tombstone: expected ErrDDLNotFound, got: %v", err)
		}

		// MF-2 (AC3): the tombstone cascades to every aspect, .compensation
		// included, so a deleted meta-vertex leaves no live compensation to
		// read. ReadCompensation reports the aspect as tombstoned/absent.
		if _, err := tr.ReadCompensation(ctx, metaKey); !errors.Is(err, aiagent.ErrCompensationAspectMissing) {
			t.Fatalf("ReadCompensation after tombstone: want ErrCompensationAspectMissing, got: %v", err)
		}

		// Step 8: verify isDeleted: true in Core KV.
		gate4AssertTombstoned(t, ctx, conn, metaKey)

		// Step 9: verify same canonicalName can be re-created (idempotency is
		// per requestId — different requestId means it's a fresh operation).
		newPayload := buildGate4VertexTypePayload(t, canonicalName)
		newMetaKey := gate4SubmitAndCapture(t, ctx, conn, seederActorKey,
			metaCP, metaCons, newPayload, "Gate4DDLRecreate0001")
		if newMetaKey == metaKey {
			t.Errorf("re-create after tombstone: expected new NanoID, got same key %q", metaKey)
		}

		// Tombstone the re-created DDL to leave the kernel clean.
		newVtxEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, newMetaKey)
		if err != nil {
			t.Fatalf("KVGet re-created vertex: %v", err)
		}
		gate4Tombstone(t, ctx, conn, seederActorKey, metaCP, metaCons,
			map[string]any{"metaKey": newMetaKey, "expectedRevision": int(newVtxEntry.Revision)},
			"Gate4DDLCleanup0001")

		// SC-2 (AC6 step 9): assert no pipeline restart — consumer has fully
		// caught up with no pending or redelivered messages.
		ci, err := metaCons.Info(ctx)
		if err != nil {
			t.Fatalf("metaCons.Info after DDL cleanup: %v", err)
		}
		if ci.NumPending != 0 {
			t.Errorf("SC-2: NumPending after DDL rollback = %d, want 0", ci.NumPending)
		}
		if ci.NumRedelivered != 0 {
			t.Errorf("SC-2: NumRedelivered after DDL rollback = %d, want 0", ci.NumRedelivered)
		}

		t.Logf("Gate4/DDL_VertexType: rollback cycle passed, metaKey=%s", metaKey)
	})

	// === Sub-test B: Lens vertex (meta.lens branch, AC6 step 10) ===
	t.Run("Lens", func(t *testing.T) {
		canonicalName := "RollbackTestLens"
		payload := buildGate4LensPayload(t, canonicalName)

		// Step 2: create + capture metaKey.
		metaKey := gate4SubmitAndCapture(t, ctx, conn, seederActorKey,
			metaCP, metaCons, payload, "Gate4LensCreate0001")
		if !isMetaKey(metaKey) {
			t.Fatalf("captured lens metaKey %q is not a vtx.meta.* key", metaKey)
		}

		// Step 3: verify discoverable.
		gotKey, err := tr.DiscoverDDL(ctx, canonicalName)
		if err != nil {
			t.Fatalf("DiscoverDDL after create (lens): %v", err)
		}
		if gotKey != metaKey {
			t.Fatalf("DiscoverDDL key mismatch (lens): got %q seeder wrote %q", gotKey, metaKey)
		}

		// Step 4: read compensation.
		compData, err := tr.ReadCompensation(ctx, metaKey)
		if err != nil {
			t.Fatalf("ReadCompensation (lens): %v", err)
		}
		if compData["inverseOperationType"] != "TombstoneMetaVertex" {
			t.Fatalf("inverseOperationType (lens): got %v want TombstoneMetaVertex",
				compData["inverseOperationType"])
		}

		// Steps 5–6: compensate.
		vtxEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, metaKey)
		if err != nil {
			t.Fatalf("KVGet lens vertex: %v", err)
		}
		gate4Tombstone(t, ctx, conn, seederActorKey, metaCP, metaCons,
			map[string]any{"metaKey": metaKey, "expectedRevision": int(vtxEntry.Revision)},
			"Gate4LensTombstn0001")

		// Step 7: no longer discoverable.
		_, err = tr.DiscoverDDL(ctx, canonicalName)
		if err == nil {
			t.Fatal("DiscoverDDL after lens tombstone: expected ErrDDLNotFound, got nil")
		}
		if !errors.Is(err, aiagent.ErrDDLNotFound) {
			t.Fatalf("DiscoverDDL after lens tombstone: expected ErrDDLNotFound, got: %v", err)
		}

		// Step 8: isDeleted: true in Core KV.
		gate4AssertTombstoned(t, ctx, conn, metaKey)

		// SC-2 (AC6 step 9): assert no pipeline restart.
		ci, err := metaCons.Info(ctx)
		if err != nil {
			t.Fatalf("metaCons.Info after Lens tombstone: %v", err)
		}
		if ci.NumPending != 0 {
			t.Errorf("SC-2: NumPending after Lens rollback = %d, want 0", ci.NumPending)
		}
		if ci.NumRedelivered != 0 {
			t.Errorf("SC-2: NumRedelivered after Lens rollback = %d, want 0", ci.NumRedelivered)
		}

		t.Logf("Gate4/Lens: rollback cycle passed, metaKey=%s", metaKey)
	})

	// === Sub-test C: UpdateMetaVertex compensation round-trip (SC-3) ===
	t.Run("UpdateMetaVertex", func(t *testing.T) {
		canonicalName := "RollbackTestUpdate"
		originalDesc := "original-description"
		modifiedDesc := "modified-description"

		// Step 1: create a DDL with originalDesc.
		payload := buildGate4VertexTypePayload(t, canonicalName)
		payload["description"] = originalDesc
		metaKey := gate4SubmitAndCapture(t, ctx, conn, seederActorKey,
			metaCP, metaCons, payload, "Gate4UpdateCreate0001")
		if !isMetaKey(metaKey) {
			t.Fatalf("captured metaKey %q is not a vtx.meta.* key", metaKey)
		}

		// Step 2: read the current revision of the description aspect for
		// expectedRevision conflict detection.
		descEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, metaKey+".description")
		if err != nil {
			t.Fatalf("KVGet description aspect: %v", err)
		}
		expectedRev := int(descEntry.Revision)

		// Submit UpdateMetaVertex with modifiedDesc and expectedRevision.
		// ContextHint.Reads must declare both the vertex key (for vertex_alive)
		// and meta_key+".description" (for prior-description capture).
		updateEnv := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID("Gate4UpdateOp0001"),
			Lane:          processor.LaneMeta,
			OperationType: "UpdateMetaVertex",
			Actor:         seederActorKey,
			SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
			Class:         "root",
			Payload: mustMarshal(t, map[string]any{
				"metaKey":          metaKey,
				"description":      modifiedDesc,
				"expectedRevision": expectedRev,
			}),
			ContextHint: &processor.ContextHint{Reads: []string{metaKey, metaKey + ".description"}},
		}
		testutil.PublishOp(t, conn, updateEnv)
		testutil.DriveOne(t, ctx, metaCP, metaCons, processor.OutcomeAccepted)

		// Step 3: read .compensation and assert inverseOperationType +
		// payloadTemplate captures the prior description.
		compData, err := tr.ReadCompensation(ctx, metaKey)
		if err != nil {
			t.Fatalf("ReadCompensation after update: %v", err)
		}
		if compData["inverseOperationType"] != "UpdateMetaVertex" {
			t.Fatalf("inverseOperationType after update: got %v want UpdateMetaVertex",
				compData["inverseOperationType"])
		}
		pt, _ := compData["payloadTemplate"].(map[string]any)
		if pt == nil {
			t.Fatalf("payloadTemplate missing from compensation data: %+v", compData)
		}
		if pt["description"] != originalDesc {
			t.Fatalf("payloadTemplate.description: got %v want %q", pt["description"], originalDesc)
		}

		// Step 4: submit the compensating UpdateMetaVertex to restore prior state.
		compensateEnv := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID("Gate4Compensate0001"),
			Lane:          processor.LaneMeta,
			OperationType: "UpdateMetaVertex",
			Actor:         seederActorKey,
			SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
			Class:         "root",
			Payload: mustMarshal(t, map[string]any{
				"metaKey":     metaKey,
				"description": pt["description"], // from payloadTemplate
			}),
			ContextHint: &processor.ContextHint{Reads: []string{metaKey, metaKey + ".description"}},
		}
		testutil.PublishOp(t, conn, compensateEnv)
		testutil.DriveOne(t, ctx, metaCP, metaCons, processor.OutcomeAccepted)

		// Step 5: verify description restored to originalDesc.
		descEntry2, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, metaKey+".description")
		if err != nil {
			t.Fatalf("KVGet description after compensate: %v", err)
		}
		var descDoc struct {
			Data struct {
				Text string `json:"text"`
			} `json:"data"`
		}
		if err := json.Unmarshal(descEntry2.Value, &descDoc); err != nil {
			t.Fatalf("parse description doc: %v", err)
		}
		if descDoc.Data.Text != originalDesc {
			t.Fatalf("description after compensate: got %q want %q", descDoc.Data.Text, originalDesc)
		}

		// Cleanup: tombstone the test DDL.
		vtxEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, metaKey)
		if err != nil {
			t.Fatalf("KVGet vertex for cleanup: %v", err)
		}
		gate4Tombstone(t, ctx, conn, seederActorKey, metaCP, metaCons,
			map[string]any{"metaKey": metaKey, "expectedRevision": int(vtxEntry.Revision)},
			"Gate4UpdateCleanup0001")

		t.Logf("Gate4/UpdateMetaVertex: compensation round-trip passed, metaKey=%s", metaKey)
	})

	// Step 11: write health gate marker.
	healthRecord := map[string]any{
		"passed":      true,
		"completedAt": time.Now().UTC().Format(time.RFC3339),
	}
	healthBytes, err := json.Marshal(healthRecord)
	if err != nil {
		t.Fatalf("marshal health record: %v", err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessHealthBucket,
		"health.gates.phase1.gate4", healthBytes); err != nil {
		t.Fatalf("write health.gates.phase1.gate4: %v", err)
	}
	healthEntry, err := conn.KVGet(ctx, testutil.HarnessHealthBucket, "health.gates.phase1.gate4")
	if err != nil {
		t.Fatalf("health.gates.phase1.gate4 not found: %v", err)
	}
	var healthDoc map[string]any
	if err := json.Unmarshal(healthEntry.Value, &healthDoc); err != nil {
		t.Fatalf("parse health record: %v", err)
	}
	if healthDoc["passed"] != true {
		t.Errorf("health.passed: got %v want true", healthDoc["passed"])
	}
	t.Log("Gate 4 passed: compensating-op rollback verified for DDL + Lens vertex types")
}

// gate4SubmitAndCapture submits a CreateMetaVertex op, asserts OutcomeAccepted,
// then reads the op tracker to extract the created meta-vertex key.
//
// The metaKey comes from tracker.Data["mutationKeys"][0] — the first mutation
// in a CreateMetaVertex commit is always the vtx.meta.<NanoID> vertex key.
// No new OperationReply fields (Guardrail 1).
func gate4SubmitAndCapture(
	t *testing.T,
	ctx context.Context,
	conn *substrate.Conn,
	actorKey string,
	cp *processor.CommitPath,
	cons jetstream.Consumer,
	payload map[string]any,
	reqIDLabel string,
) string {
	t.Helper()
	reqID := testutil.GenReqID(reqIDLabel)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneMeta,
		OperationType: "CreateMetaVertex",
		Actor:         actorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "root",
		Payload:       mustMarshal(t, payload),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// Read tracker to extract the committed meta-vertex key.
	trackerEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.TrackerKey(reqID))
	if err != nil {
		t.Fatalf("tracker not found after CreateMetaVertex (%s): %v", reqIDLabel, err)
	}
	tracker, err := processor.ParseTracker(trackerEntry.Value)
	if err != nil {
		t.Fatalf("ParseTracker (%s): %v", reqIDLabel, err)
	}
	mutKeys, _ := tracker.Data["mutationKeys"].([]interface{})
	if len(mutKeys) == 0 {
		t.Fatalf("tracker has no mutationKeys (%s): %v", reqIDLabel, tracker.Data)
	}
	metaKey, _ := mutKeys[0].(string)
	if metaKey == "" {
		t.Fatalf("tracker mutationKeys[0] is empty (%s)", reqIDLabel)
	}
	return metaKey
}

// gate4Tombstone submits a TombstoneMetaVertex op and asserts OutcomeAccepted.
//
// The TombstoneMetaVertex Starlark script calls vertex_alive(state, meta_key),
// which requires the metaKey to be declared in ContextHint.Reads so the
// Hydrator (step 4) loads it into the script's state dict. This is a
// client-side contract requirement (Contract #2 §2.5), not a Processor change.
func gate4Tombstone(
	t *testing.T,
	ctx context.Context,
	conn *substrate.Conn,
	actorKey string,
	cp *processor.CommitPath,
	cons jetstream.Consumer,
	payload map[string]any,
	reqIDLabel string,
) {
	t.Helper()
	// Extract the metaKey from the payload to declare it in ContextHint.Reads.
	metaKey, _ := payload["metaKey"].(string)
	if metaKey == "" {
		t.Fatalf("gate4Tombstone: payload missing metaKey (%s)", reqIDLabel)
	}
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(reqIDLabel),
		Lane:          processor.LaneMeta,
		OperationType: "TombstoneMetaVertex",
		Actor:         actorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "root",
		Payload:       mustMarshal(t, payload),
		// ContextHint.Reads declares the metaKey so the Hydrator loads it
		// into state for the vertex_alive() check in the Starlark script.
		ContextHint: &processor.ContextHint{Reads: []string{metaKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

// gate4AssertTombstoned reads a Core KV key and asserts its isDeleted field
// is true.
func gate4AssertTombstoned(
	t *testing.T,
	ctx context.Context,
	conn *substrate.Conn,
	metaKey string,
) {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, metaKey)
	if err != nil {
		t.Fatalf("KVGet tombstoned vertex %s: %v", metaKey, err)
	}
	var doc struct {
		IsDeleted bool `json:"isDeleted"`
	}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("parse vertex doc %s: %v", metaKey, err)
	}
	if !doc.IsDeleted {
		t.Errorf("vertex %s: isDeleted=false after TombstoneMetaVertex (want true)", metaKey)
	}
}

// isMetaKey returns true if the key matches the vtx.meta.* pattern.
func isMetaKey(key string) bool {
	parts := splitDot(key)
	return len(parts) == 3 && parts[0] == "vtx" && parts[1] == "meta"
}

func splitDot(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// buildGate4VertexTypePayload builds a CreateMetaVertex payload for a DDL vertex
// type with all required Story 5.1 self-description aspects (AC6 step 2).
func buildGate4VertexTypePayload(t *testing.T, canonicalName string) map[string]any {
	t.Helper()
	return map[string]any{
		"targetClass":       "meta.ddl.vertexType",
		"canonicalName":     canonicalName,
		"permittedCommands": []string{"DoRollbackTest"},
		"description":       "Ephemeral DDL for Gate 4 rollback test. Created and immediately tombstoned.",
		"script":            "def execute(state, op):\n    return {\"mutations\": [], \"events\": []}",
		"inputSchema":       `{"type":"object","properties":{}}`,
		"outputSchema":      `{"type":"object","properties":{}}`,
		"fieldDescription":  map[string]any{"note": "No fields for this test-only DDL."},
		"examples": []any{map[string]any{
			"name":            "test",
			"payload":         map[string]any{},
			"expectedOutcome": "Accepted.",
		}},
	}
}

// buildGate4LensPayload builds a CreateMetaVertex payload for a meta.lens
// vertex (AC6 step 10).
func buildGate4LensPayload(t *testing.T, canonicalName string) map[string]any {
	t.Helper()
	return map[string]any{
		"targetClass":   "meta.lens",
		"canonicalName": canonicalName,
		"description":   "Ephemeral Lens for Gate 4 rollback test.",
		"spec": `{"id":"rollback-test-lens","canonicalName":"` + canonicalName +
			`","targetType":"nats_kv","targetConfig":{"bucket":"capability-kv","key":["key"]},"cypherRule":"MATCH (n:identity) RETURN n.key AS key","engine":"full"}`,
	}
}
