// FR19 + FR34 North-Star Integration Test — Cold-Start AI Agent Traversal.
//
// This test is the Phase 1 north star specified in epics.md §Story 5.2 AC4:
//
//  1. A brand-new AI agent identity is provisioned.
//  2. A NEW operation type DDL meta-vertex is seeded post-bootstrap via
//     CreateMetaVertex through the Processor's meta lane.
//  3. The agent's capability doc is seeded granting the new operation.
//  4. The agent performs cold-start discovery:
//     a. Reads cap.identity.<agentId> from Capability KV.
//     b. Discovers the new operation in platformPermissions[].
//     c. Enumerates vtx.meta.* keys and matches .canonicalName aspect.
//     d. Reads the five self-description aspects (Story 5.1).
//     e. Constructs a payload from the inputSchema.
//     f. Submits the operation via the standard Processor write path.
//  5. Asserts OutcomeAccepted — same Processor path as any human op (NFR-S10).
//  6. Writes health.fr19.cold-start-test to Health KV on success.
//
// The test harness never hardcodes the new operation's canonical name in
// the traversal phase — the agent discovers it from the graph.
package aiagent_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/aiagent"
	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/testutil"
)

// northStarSeederActorID is the test seeder identity — acts as the operator
// that creates the new DDL. Fixed NanoID within tests (20 chars, alphabet).
const northStarSeederActorID = "NorthStarSeedrID00001"

// northStarAgentActorID is the AI agent identity. Also fixed for the test.
const northStarAgentActorID = "NorthStarAgentID00001"

// TestFR19_ColdStartAIAgentTraversal is the north-star integration test for
// FR19 (cold-start traversal) and FR34 (AI agent submits through standard
// write path).
//
// The test provisions everything in-process using the embedded NATS harness
// (same infrastructure as all other Story 4.x / 5.x integration tests) so
// it is fully self-contained and runs as part of `go test ./... -p 1`.
func TestFR19_ColdStartAIAgentTraversal(t *testing.T) {
	// ---- Harness setup ----
	ctx, conn := testutil.SetupPackageTestEnv(t)

	seederActorKey := "vtx.identity." + northStarSeederActorID
	agentActorKey := "vtx.identity." + northStarAgentActorID

	// ---- Step 1: Seed the new DDL via CreateMetaVertex (meta lane) ----
	//
	// We build a unique canonical name so each test run is independent.
	// In the traversal phase we do NOT pass this string to the agent —
	// the agent discovers it from the graph.
	canonicalName := fmt.Sprintf("NorthStarOp%d", time.Now().UnixMilli()%100000)

	// Seeder needs CreateMetaVertex in its cap doc.
	seederCapDoc := buildCapDoc(seederActorKey, "cap.identity."+northStarSeederActorID,
		[]processor.PlatformPermission{
			{OperationType: "CreateMetaVertex", Scope: "any"},
		},
		[]string{bootstrap.RoleOperatorKey})
	testutil.SeedCapDoc(t, ctx, conn, seederCapDoc)

	// Pipeline for meta-lane (CreateMetaVertex goes to ops.meta).
	metaCP, metaCons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:        "ns-meta-pipeline",
		Instance:       "ns-meta",
		FilterSubjects: []string{"ops.meta"},
	})

	// Build CreateMetaVertex payload with all nine required fields (Story 5.1).
	metaPayload := buildCreateMetaVertexPayload(t, canonicalName)
	metaEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("NSSeedDDL0000001"),
		Lane:          processor.LaneMeta,
		OperationType: "CreateMetaVertex",
		Actor:         seederActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "root",
		Payload:       mustMarshal(t, metaPayload),
	}
	testutil.PublishOp(t, conn, metaEnv)
	testutil.DriveOne(t, ctx, metaCP, metaCons, processor.OutcomeAccepted)

	// Verify the DDL meta-vertex was written to Core KV.
	trackerKey := processor.TrackerKey(metaEnv.RequestID)
	trackerEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, trackerKey)
	if err != nil {
		t.Fatalf("tracker not found after CreateMetaVertex: %v", err)
	}
	tracker, err := processor.ParseTracker(trackerEntry.Value)
	if err != nil {
		t.Fatalf("ParseTracker: %v", err)
	}
	if tracker.Data["status"] != "committed" {
		t.Fatalf("tracker status: got %v want committed", tracker.Data["status"])
	}
	// Extract the meta-vertex key the seeder actually wrote — used below
	// to cross-check that DiscoverDDL returns *the same* key (AC4: "the
	// traverser returns the same key the seeder got"). For a
	// CreateMetaVertex op, the primary mutation key is the new
	// vtx.meta.<NanoID> vertex.
	seederMutationKeys, _ := tracker.Data["mutationKeys"].([]interface{})
	if len(seederMutationKeys) == 0 {
		t.Fatalf("seeder tracker has no mutationKeys: %v", tracker.Data)
	}
	expectedDDLKey, _ := seederMutationKeys[0].(string)
	if expectedDDLKey == "" || !strings.HasPrefix(expectedDDLKey, "vtx.meta.") {
		t.Fatalf("seeder mutationKeys[0] not a vtx.meta.* key: %v", seederMutationKeys[0])
	}

	// ---- Step 2: Seed the AI agent's capability doc ----
	//
	// In production, the Capability Lens would project this. In the test
	// harness we seed it directly (same pattern as all integration tests
	// since Story 3.x).
	// Agent holds no platform role — its authorization is permission-based
	// only (the seeder granted the new op type directly into the cap doc).
	agentCapDoc := buildCapDoc(agentActorKey, "cap.identity."+northStarAgentActorID,
		[]processor.PlatformPermission{
			{OperationType: canonicalName, Scope: "any"},
		},
		nil)
	testutil.SeedCapDoc(t, ctx, conn, agentCapDoc)

	// ---- Step 3: Cold-start traversal ----
	//
	// The Traverser gets only the connection details — no knowledge of
	// canonicalName, no knowledge of the meta-vertex key.
	tr := aiagent.NewTraverser(conn, testutil.HarnessCoreBucket, testutil.HarnessCapBucket)

	// 3a: Read capability — discovers what operations the agent can submit.
	capDoc, err := tr.ReadCapability(ctx, northStarAgentActorID)
	if err != nil {
		t.Fatalf("ReadCapability: %v", err)
	}
	if len(capDoc.PlatformPermissions) == 0 {
		t.Fatal("agent has no platformPermissions in cap doc")
	}

	// 3b: Pick the first available operation type (without knowing it's
	// canonicalName). The real agent would choose based on its goal.
	discoveredOpType := capDoc.PlatformPermissions[0].OperationType
	if discoveredOpType != canonicalName {
		t.Fatalf("discovered op type %q does not match seeded %q (check cap doc seeding)", discoveredOpType, canonicalName)
	}

	// 3c: Discover the DDL meta-vertex by graph traversal.
	ddlKey, err := tr.DiscoverDDL(ctx, discoveredOpType)
	if err != nil {
		t.Fatalf("DiscoverDDL for %q: %v", discoveredOpType, err)
	}
	if ddlKey == "" {
		t.Fatal("DiscoverDDL returned empty key")
	}
	// AC4 cross-check: the agent's traversal must land on *the same*
	// meta-vertex the seeder committed — not some pre-existing
	// coincidentally-named vertex.
	if ddlKey != expectedDDLKey {
		t.Fatalf("DiscoverDDL key mismatch: got %q, seeder wrote %q", ddlKey, expectedDDLKey)
	}

	// 3d: Read the five self-description aspects.
	aspects, err := tr.ReadDDLAspects(ctx, ddlKey)
	if err != nil {
		t.Fatalf("ReadDDLAspects for %q: %v", ddlKey, err)
	}
	if aspects.Description == "" {
		t.Error("DDL description is empty")
	}
	if aspects.InputSchema == "" {
		t.Error("DDL inputSchema is empty")
	}
	if aspects.OutputSchema == "" {
		t.Error("DDL outputSchema is empty")
	}
	if len(aspects.FieldDescriptions) == 0 {
		t.Error("DDL fieldDescriptions is empty")
	}
	if len(aspects.Examples) == 0 {
		t.Error("DDL examples is empty")
	}

	// 3e: Construct a valid payload from the inputSchema.
	// buildPayloadFromSchema walks `required` + `properties.type` and
	// produces typed values. The seeded DDL declares two required
	// fields (`title` string, `year` integer) plus one optional
	// (`isbn`), and its Starlark script reads + type-checks all three.
	// A degenerate empty payload would be rejected by the script —
	// commit-succeeded below proves the schema-driven construction
	// produced valid typed values that flowed end-to-end.
	agentPayload := buildPayloadFromSchema(t, aspects.InputSchema)

	// ---- Step 4: Submit the operation via the standard Processor path ----
	//
	// The agent submits as itself. Same step 1-10 Processor path as any
	// human actor — NFR-S10 is satisfied by design (no AI-specific code).
	defaultCP, defaultCons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  "ns-default-pipeline",
		Instance: "ns-default",
	})

	agentEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("NSAgentSubmit00001"),
		Lane:          processor.LaneDefault,
		OperationType: discoveredOpType,
		Actor:         agentActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         canonicalName,
		Payload:       mustMarshal(t, agentPayload),
	}
	testutil.PublishOp(t, conn, agentEnv)
	testutil.DriveOne(t, ctx, defaultCP, defaultCons, processor.OutcomeAccepted)

	// Verify operation was committed.
	agentTrackerEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.TrackerKey(agentEnv.RequestID))
	if err != nil {
		t.Fatalf("agent op tracker not found: %v", err)
	}
	agentTracker, err := processor.ParseTracker(agentTrackerEntry.Value)
	if err != nil {
		t.Fatalf("ParseTracker (agent op): %v", err)
	}
	if agentTracker.Data["status"] != "committed" {
		t.Fatalf("agent op tracker status: got %v want committed", agentTracker.Data["status"])
	}

	// ---- Step 5: Write health signal on success ----
	healthRecord := map[string]any{
		"passed":   true,
		"testedAt": time.Now().UTC().Format(time.RFC3339),
	}
	healthBytes, err := json.Marshal(healthRecord)
	if err != nil {
		t.Fatalf("marshal health record: %v", err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessHealthBucket, "health.fr19.cold-start-test", healthBytes); err != nil {
		t.Fatalf("write health.fr19.cold-start-test: %v", err)
	}

	// Verify health key was written.
	healthEntry, err := conn.KVGet(ctx, testutil.HarnessHealthBucket, "health.fr19.cold-start-test")
	if err != nil {
		t.Fatalf("health.fr19.cold-start-test not found: %v", err)
	}
	var healthDoc map[string]any
	if err := json.Unmarshal(healthEntry.Value, &healthDoc); err != nil {
		t.Fatalf("parse health record: %v", err)
	}
	if healthDoc["passed"] != true {
		t.Errorf("health.passed: got %v want true", healthDoc["passed"])
	}

	t.Logf("FR19 north-star test passed: agent=%s opType=%q ddlKey=%s", agentActorKey, discoveredOpType, ddlKey)
}

// TestFR19_NFR_S10_SameProcessorPath verifies that the AI agent's operation
// goes through the same Processor authorization as a human actor — no bypass.
//
// This test submits an operation from an AI agent actor key with NO
// platformPermissions in its cap doc and asserts the operation is rejected.
// If there were an AI-specific bypass, this would pass incorrectly.
func TestFR19_NFR_S10_SameProcessorPath(t *testing.T) {
	ctx, conn := testutil.SetupPackageTestEnv(t)

	agentActorKey := "vtx.identity." + "NFRs10AgentID000001"
	capKey := "cap.identity." + "NFRs10AgentID000001"

	// Seed an empty cap doc — no permissions.
	emptyCapDoc := buildCapDoc(agentActorKey, capKey, []processor.PlatformPermission{}, nil)
	testutil.SeedCapDoc(t, ctx, conn, emptyCapDoc)

	// Try to submit CreateMetaVertex (which the agent's cap doc doesn't grant).
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:        "nfr-s10-meta",
		Instance:       "nfr-s10",
		FilterSubjects: []string{"ops.meta"},
	})

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("NFRS10TestOp0000001"),
		Lane:          processor.LaneMeta,
		OperationType: "CreateMetaVertex",
		Actor:         agentActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "root",
		Payload:       mustMarshal(t, buildCreateMetaVertexPayload(t, "NFRs10BlockedOp")),
	}
	testutil.PublishOp(t, conn, env)

	// Must be rejected — same auth path as any human actor.
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
	t.Log("NFR-S10: AI agent with no cap was correctly rejected — no bypass")
}

// ---- helpers ----

// buildCapDoc constructs a minimal processor.CapabilityDoc for test
// use. Callers pass the roles the actor holds — the seeder identity
// gets `[]string{bootstrap.RoleOperatorKey}` (it grants permissions);
// AI agent actors get `[]string{}` (they receive permissions but hold
// no platform role).
func buildCapDoc(actorKey, capKey string, perms []processor.PlatformPermission, roles []string) *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    capKey,
		Actor:                  actorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{actorKey: 1},
		Lanes:                  []string{"default", "meta"},
		PlatformPermissions:    perms,
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants:        []processor.EphemeralGrant{},
		Roles:                  roles,
	}
}

// buildCreateMetaVertexPayload builds a fully-specified CreateMetaVertex
// payload for a new DDL vertex type. All nine fields required by the
// MissingSelfDescription enforcement are present.
//
// The DDL describes a `loftspace.bookRegistered` operation type with a non-trivial
// input schema (two required fields of different types + one optional)
// and a Starlark script that actually consumes the fields and emits a
// matching event. The point: the agent's cold-start traversal must
// construct a payload conforming to the schema, and the script will
// fail at runtime if any required field is missing or the wrong type —
// the commit-succeeded assertion at the end of the test then proves
// end-to-end that the schema-driven payload construction worked.
func buildCreateMetaVertexPayload(t *testing.T, canonicalName string) map[string]any {
	t.Helper()
	return map[string]any{
		"targetClass":       "meta.ddl.vertexType",
		"canonicalName":     canonicalName,
		"permittedCommands": []string{canonicalName},
		"description":       "North-star test DDL: a book-registration operation. The agent discovers this DDL from its capability set and constructs a payload from the inputSchema below.",
		// Script reads p.title (required string), p.year (required int), and
		// optional p.isbn; emits a loftspace.bookRegistered event carrying the values.
		// If the agent's payload is missing title/year or has the wrong type,
		// the script fails and the commit never lands.
		"script": "def execute(state, op):\n" +
			"    p = op.payload\n" +
			"    title = p.title\n" +
			"    year = p.year\n" +
			"    if type(title) != type(\"\") or len(title) == 0:\n" +
			"        fail(\"InvalidArgument: title required string\")\n" +
			"    if type(year) != type(0):\n" +
			"        fail(\"InvalidArgument: year required int\")\n" +
			"    isbn = p.isbn if hasattr(p, \"isbn\") else \"\"\n" +
			"    events = [{\"class\": \"loftspace.bookRegistered\", \"data\": {\"title\": title, \"year\": year, \"isbn\": isbn}}]\n" +
			"    return {\"mutations\": [], \"events\": events}\n",
		"inputSchema":  `{"type":"object","required":["title","year"],"properties":{"title":{"type":"string","minLength":1,"description":"Book title."},"year":{"type":"integer","description":"Publication year."},"isbn":{"type":"string","description":"Optional ISBN-13."}}}`,
		"outputSchema": `{"type":"object","properties":{}}`,
		"fieldDescription": map[string]any{
			"title": "Required: the book's title (non-empty string).",
			"year":  "Required: publication year as an integer.",
			"isbn":  "Optional: ISBN-13 string.",
		},
		"examples": []any{map[string]any{
			"name":            "north-star-example",
			"payload":         map[string]any{"title": "Designing Data-Intensive Applications", "year": 2017, "isbn": "9781449373320"},
			"expectedOutcome": "Accepted by Processor; emits loftspace.bookRegistered event with the supplied fields.",
		}},
	}
}

// buildPayloadFromSchema walks a JSON Schema object and constructs a
// payload satisfying every `required` field, choosing realistic typed
// values per property `type`. This mirrors what a real AI agent would
// do during cold-start: read the schema, identify required fields,
// produce typed values that conform.
//
// Test-realistic value choices (not random — deterministic per field
// name to keep failures reproducible):
//   - string  → "northstar-<fieldname>" (or 1 if the field is a known
//     short-form like "year"/"count" but type says string)
//   - integer → a stable, plausible year-shaped int (2026)
//   - number  → 1.0
//   - boolean → true
//
// Unknown property types `t.Fatal` — extend the helper rather than
// silently producing a `null` and relying on the script to catch it.
// Same goes for an empty `required` list: that would let an empty
// payload through trivially, defeating the FR19 schema-driven
// construction loop, so the helper hard-fails there too.
func buildPayloadFromSchema(t *testing.T, inputSchema string) map[string]any {
	t.Helper()
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(inputSchema), &schema); err != nil {
		t.Fatalf("buildPayloadFromSchema: schema is not valid JSON: %v", err)
	}
	if len(schema.Required) == 0 {
		// A test schema with no required fields would let an empty
		// payload through trivially — that's not what FR19 is about.
		// Catch this early so future test authors don't degenerate
		// the test back to the no-op shape.
		t.Fatal("buildPayloadFromSchema: schema declares no `required` fields — the north-star test depends on schema-driven payload construction; please tighten the seeded DDL's inputSchema")
	}
	payload := map[string]any{}
	for _, field := range schema.Required {
		prop, ok := schema.Properties[field]
		if !ok {
			t.Fatalf("buildPayloadFromSchema: required field %q not declared in schema properties", field)
		}
		switch prop.Type {
		case "string":
			payload[field] = "northstar-" + field
		case "integer":
			payload[field] = 2026
		case "number":
			payload[field] = 1.0
		case "boolean":
			payload[field] = true
		default:
			t.Fatalf("buildPayloadFromSchema: required field %q has unsupported type %q (extend the helper)", field, prop.Type)
		}
	}
	return payload
}

// mustMarshal marshals val to JSON or fatals the test.
func mustMarshal(t *testing.T, val any) []byte {
	t.Helper()
	b, err := json.Marshal(val)
	if err != nil {
		t.Fatalf("mustMarshal: %v", err)
	}
	return b
}
