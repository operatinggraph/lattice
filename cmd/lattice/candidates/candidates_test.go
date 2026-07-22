package candidates

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// TestCandidatesList_HappyPath verifies that candidate entries can be listed
// from the duplicate-candidates KV bucket, and that duplicateOfCriteria
// resolves the pair's criteria from the Core-KV duplicateOf link doc (the
// lens row itself carries no PII/criteria columns — dedup-over-encrypted-pii-
// design.md §3.3).
func TestCandidatesList_HappyPath(t *testing.T) {
	ctx, conn := setupCandidatesEnv(t)

	primaryID := "primaryIDCandidates01"
	secondaryID := "secondryIDCandidates1"
	primaryKey := "vtx.identity." + primaryID
	secondaryKey := "vtx.identity." + secondaryID

	entry := candidateEntry{
		PrimaryID:    primaryID,
		SecondaryID:  secondaryID,
		PrimaryKey:   primaryKey,
		SecondaryKey: secondaryKey,
	}
	data, _ := json.Marshal(entry)

	js := conn.JetStream()
	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: duplicateCandidatesBucket,
	})
	if err != nil {
		t.Fatalf("create duplicate-candidates bucket: %v", err)
	}
	bucketKey := primaryID + "." + secondaryID
	if _, err := kv.Put(ctx, bucketKey, data); err != nil {
		t.Fatalf("put candidate entry: %v", err)
	}

	seedDuplicateOfLink(t, ctx, conn, secondaryID, primaryID, []string{"exact-email"})

	keys, err := conn.KVListKeys(ctx, duplicateCandidatesBucket)
	if err != nil {
		t.Fatalf("KVListKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(keys))
	}

	kvEntry, err := conn.KVGet(ctx, duplicateCandidatesBucket, keys[0])
	if err != nil {
		t.Fatalf("KVGet: %v", err)
	}
	var got candidateEntry
	if err := json.Unmarshal(kvEntry.Value, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.PrimaryKey != primaryKey {
		t.Errorf("primaryKey = %q, want %q", got.PrimaryKey, primaryKey)
	}
	if got.SecondaryKey != secondaryKey {
		t.Errorf("secondaryKey = %q, want %q", got.SecondaryKey, secondaryKey)
	}

	criteria := duplicateOfCriteria(ctx, conn, got.PrimaryID, got.SecondaryID)
	if len(criteria) != 1 || criteria[0] != "exact-email" {
		t.Errorf("criteria = %v, want [exact-email]", criteria)
	}
}

// TestCandidatesMerge_EnumeratesSecondaryEdgesExcludingPairEvidence proves
// enumerateSecondaryEdges picks up the secondary's real business edges
// (both directions) while excluding the duplicateOf/indexes pair-evidence
// classes, which are not business edges (§3.3).
func TestCandidatesMerge_EnumeratesSecondaryEdgesExcludingPairEvidence(t *testing.T) {
	ctx, conn, cp, cons := setupCandidatesFullEnv(t)
	_ = cp
	_ = cons

	primaryID := "primryIDCandidatMrg01"
	secondaryID := "secndryIDCandidMrg01"
	primaryKey := "vtx.identity." + primaryID
	secondaryKey := "vtx.identity." + secondaryID

	seedIdentityVertex(t, ctx, conn, primaryKey)
	seedIdentityVertex(t, ctx, conn, secondaryKey)

	outboundEdge := "lnk.identity." + secondaryID + ".holdsRole.role.operatorR001"
	inboundEdge := "lnk.task.taskAssignedToSecID001.assignedTo.identity." + secondaryID
	seedEdgeVertex(t, ctx, conn, outboundEdge)
	seedEdgeVertex(t, ctx, conn, inboundEdge)

	// Pair evidence — must NOT appear in the enumerated edge set.
	seedDuplicateOfLink(t, ctx, conn, secondaryID, primaryID, []string{"exact-email"})
	indexesLink := "lnk.identityindex.someHash0000000001.indexes.identity." + secondaryID
	seedEdgeVertex(t, ctx, conn, indexesLink)

	edges, err := enumerateSecondaryEdges(ctx, conn, secondaryID)
	if err != nil {
		t.Fatalf("enumerateSecondaryEdges: %v", err)
	}

	got := map[string]bool{}
	for _, e := range edges {
		got[e] = true
	}
	if !got[outboundEdge] {
		t.Errorf("expected outbound edge %s in %v", outboundEdge, edges)
	}
	if !got[inboundEdge] {
		t.Errorf("expected inbound edge %s in %v", inboundEdge, edges)
	}
	for _, excluded := range []string{
		"lnk.identity." + secondaryID + ".duplicateOf.identity." + primaryID,
		indexesLink,
	} {
		if got[excluded] {
			t.Errorf("pair-evidence link %s must be excluded from the merge edge set, got %v", excluded, edges)
		}
	}
}

func seedIdentityVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) {
	t.Helper()
	doc := map[string]interface{}{
		"key":       key,
		"class":     "identity",
		"isDeleted": false,
		"data":      map[string]interface{}{"state": "unclaimed"},
	}
	data, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, key, data); err != nil {
		t.Fatalf("seed identity %s: %v", key, err)
	}
}

func seedEdgeVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) {
	t.Helper()
	doc := map[string]interface{}{
		"key":       key,
		"class":     "lnk",
		"isDeleted": false,
		"data":      map[string]interface{}{},
	}
	data, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, key, data); err != nil {
		t.Fatalf("seed edge %s: %v", key, err)
	}
}

func seedDuplicateOfLink(t *testing.T, ctx context.Context, conn *substrate.Conn, secondaryID, primaryID string, criteria []string) {
	t.Helper()
	key := "lnk.identity." + secondaryID + ".duplicateOf.identity." + primaryID
	doc := map[string]interface{}{
		"key":       key,
		"class":     "duplicateOf",
		"isDeleted": false,
		"data":      map[string]interface{}{"criteria": criteria},
	}
	data, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, key, data); err != nil {
		t.Fatalf("seed duplicateOf link %s: %v", key, err)
	}
}

func setupCandidatesEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	url := testutil.StartEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "candidates-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)
	testutil.ProvisionHarness(t, ctx, conn)
	return ctx, conn
}

func setupCandidatesFullEnv(t *testing.T) (context.Context, *substrate.Conn, *processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t)

	now := time.Now().UTC()
	actorID := "JstffActHJKMNPQRSTUV"
	actorKey := "vtx.identity." + actorID
	capKey := "cap.identity." + actorID
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    capKey,
		Actor:                  actorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{actorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "MergeIdentity", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	})

	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  "candidates-cmd-test",
		Instance: "candidates-cmd",
	})
	return ctx, conn, cp, cons
}
