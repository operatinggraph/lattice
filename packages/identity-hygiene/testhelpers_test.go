// Shared test helpers for identity-hygiene package end-to-end tests.
//
// These live in an external test package (`identityhygiene_test`) so they
// exercise only the public Lattice surface that any Capability Package would
// see in production.
package identityhygiene_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
)

// Test actor NanoIDs — 20 chars, substrate.Alphabet only (no I/O/l/0).
const (
	operatorActorID  = "JopActHygeNPQRSTUVWX"
	operatorActorKey = "vtx.identity." + operatorActorID
	operatorCapKey   = "cap.identity." + operatorActorID

	consumerActorID  = "JcnHygeActNPQRSTUVWX"
	consumerActorKey = "vtx.identity." + consumerActorID
	consumerCapKey   = "cap.identity." + consumerActorID
)

// operatorCapDoc seeds a CapabilityDoc granting MergeIdentity (scope=any)
// to the operator actor. It also carries the operator role key so the
// Capability Authorizer step-3 check passes.
func operatorCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    operatorCapKey,
		Actor:                  operatorActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{operatorActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "MergeIdentity", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// consumerCapDoc seeds a CapabilityDoc granting only ClaimIdentity (scope=self)
// — no MergeIdentity permission. Used for TestMerge_NonOperatorActor_Denied.
func consumerCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    consumerCapKey,
		Actor:                  consumerActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{consumerActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "ClaimIdentity", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role.consumer"},
	}
}

// setupTestEnv assembles the standard identity-hygiene test environment:
// embedded NATS, KV buckets, primordials seeded, Phase 1 packages installed,
// operator + consumer cap docs seeded.
func setupTestEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, operatorCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, consumerCapDoc())
	return ctx, conn
}

// newMergePipeline builds a CapabilityPipeline for MergeIdentity tests.
func newMergePipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "ihm-" + durable,
	})
}

// seedIdentityVertex writes a minimal identity vertex + state aspect + mergedInto
// aspect directly to Core KV (no op required). The mergedInto aspect is always
// written (with empty data when not merged) so that ContextHint.Reads can include
// it without triggering a HydrationMiss. This mirrors the identity-domain
// seedIdentityVertex helper in packages/identity-domain/state_machine_test.go.
//
// Pass mergedIntoKey="" to write an empty (non-merged) mergedInto aspect.
func seedIdentityVertex(t *testing.T, ctx context.Context, conn *substrate.Conn,
	identityKey, state, mergedIntoKey string) {
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
	stateDoc := map[string]any{
		"class": "state", "vertexKey": identityKey, "localName": "state",
		"isDeleted": false, "data": map[string]any{"value": state},
	}
	sb, _ := json.Marshal(stateDoc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey+".state", sb); err != nil {
		t.Fatalf("seed state aspect %s: %v", identityKey, err)
	}
	// Always seed mergedInto (empty data when not merged) so the Hydrator
	// finds the key when ContextHint.Reads includes it.
	miData := map[string]any{}
	if mergedIntoKey != "" {
		miData["value"] = mergedIntoKey
	}
	miDoc := map[string]any{
		"class": "mergedInto", "vertexKey": identityKey, "localName": "mergedInto",
		"isDeleted": false, "data": miData,
	}
	mb, _ := json.Marshal(miDoc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey+".mergedInto", mb); err != nil {
		t.Fatalf("seed mergedInto aspect %s: %v", identityKey, err)
	}
}

// seedLinkVertex writes a link vertex envelope at the given key to Core KV.
// The key must be in the six-segment form `lnk.<srcType>.<srcId>.<rel>.<tgtType>.<tgtId>`.
// isDeleted=true produces a tombstoned link (for TestMerge_RejectsTombstonedEdge).
func seedLinkVertex(t *testing.T, ctx context.Context, conn *substrate.Conn,
	linkKey string, isDeleted bool) {
	t.Helper()
	doc := map[string]any{
		"class":     "link",
		"isDeleted": isDeleted,
		"data":      map[string]any{},
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, linkKey, b); err != nil {
		t.Fatalf("seed link vertex %s: %v", linkKey, err)
	}
}

// readAspectData reads a KV aspect envelope and returns its "data" map.
func readAspectData(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) map[string]any {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	data, _ := doc["data"].(map[string]any)
	return data
}

// assertTrackerEvent asserts the op tracker for reqID records an event of class eventClass.
func assertTrackerEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, reqID, eventClass string) {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.TrackerKey(reqID))
	if err != nil {
		t.Fatalf("tracker not found for %s: %v", reqID, err)
	}
	tr, err := processor.ParseTracker(entry.Value)
	if err != nil {
		t.Fatalf("ParseTracker: %v", err)
	}
	ecs, _ := tr.Data["eventClasses"].([]interface{})
	for _, ec := range ecs {
		if ec == eventClass {
			return
		}
	}
	t.Fatalf("%s not in tracker eventClasses: %v", eventClass, ecs)
}

// seedDuplicateCandidateEntry writes a simulated `duplicate-candidates`
// Lens output bucket entry. This mirrors how a real Refractor would write
// the entry after projecting the duplicateCandidates cypher; the test
// controls it directly to avoid needing a live Refractor.
//
// The bucket is a NATS KV store. The key format is:
//
//	flagged.identity.<loID>.identity.<hiID>
//
// and the value contains secondaryInboundEdges + secondaryOutboundEdges as
// the operator CLI would read before submitting MergeIdentity.
func seedDuplicateCandidateEntry(
	t *testing.T, ctx context.Context, conn *substrate.Conn,
	primaryKey, secondaryKey string,
	inboundEdges, outboundEdges []string,
) {
	t.Helper()

	// Build the bucket key: flagged.identity.<loID>.identity.<hiID>
	// The lens orders by key; in tests we just use primaryKey < secondaryKey.
	entry := map[string]any{
		"primaryKey":            primaryKey,
		"secondaryKey":          secondaryKey,
		"secondaryInboundEdges": inboundEdges,
		"secondaryOutboundEdges": outboundEdges,
	}
	b, _ := json.Marshal(entry)

	js := conn.JetStream()
	// The pkgmgr installer records the Lens meta-vertex in core-kv but does NOT
	// create the output bucket (that's the Refractor's job at runtime). In tests
	// we create the bucket here so we can seed it directly, simulating what a
	// running Refractor would have projected.
	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: "duplicate-candidates",
	})
	if err != nil {
		t.Fatalf("create/open duplicate-candidates KV: %v", err)
	}
	// Key: flagged.identity.<primaryID>.identity.<secondaryID>
	primaryID := primaryKey[len("vtx.identity."):]
	secondaryID := secondaryKey[len("vtx.identity."):]
	candidateKey := "flagged.identity." + primaryID + ".identity." + secondaryID
	if _, err := kv.Put(ctx, candidateKey, b); err != nil {
		t.Fatalf("seed duplicate-candidates entry %s: %v", candidateKey, err)
	}
}

// mergeReads builds the ContextHint.Reads slice for a MergeIdentity op:
// primary vertex, secondary vertex, their state aspects, and all edge keys.
func mergeReads(primaryKey, secondaryKey string, edges []string) []string {
	reads := []string{
		primaryKey,
		secondaryKey,
		primaryKey + ".state",
		primaryKey + ".mergedInto",
		secondaryKey + ".state",
		secondaryKey + ".mergedInto",
	}
	reads = append(reads, edges...)
	return reads
}

// mergePayload builds the JSON payload for a MergeIdentity op.
func mergePayload(primaryKey, secondaryKey string, edges []string) json.RawMessage {
	type payload struct {
		Primary   string   `json:"primary"`
		Secondary string   `json:"secondary"`
		Edges     []string `json:"edges"`
	}
	b, _ := json.Marshal(payload{
		Primary:   primaryKey,
		Secondary: secondaryKey,
		Edges:     edges,
	})
	return b
}
