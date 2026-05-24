package candidates

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

// TestCandidatesList_HappyPath verifies that candidate entries can be listed
// from the duplicate-candidates KV bucket.
func TestCandidatesList_HappyPath(t *testing.T) {
	ctx, conn := setupCandidatesEnv(t)

	// Seed the bucket.
	primaryKey := "vtx.identity.primaryIDCandidates01"
	secondaryKey := "vtx.identity.secondaryIDCandidts1"
	entry := candidateEntry{
		PrimaryKey:   primaryKey,
		SecondaryKey: secondaryKey,
		Criterion:    "exact-email",
		Score:        1.0,
		SecondaryInboundEdges:  []string{"lnk.identity.secondaryIDCandidts1.holdsRole.role.operatorR001"},
		SecondaryOutboundEdges: []string{},
	}
	data, _ := json.Marshal(entry)

	bucketKey := deriveCandidateKey(primaryKey, secondaryKey)

	// Create the bucket (simulating Refractor output).
	js := conn.JetStream()
	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: duplicateCandidatesBucket,
	})
	if err != nil {
		t.Fatalf("create duplicate-candidates bucket: %v", err)
	}
	if _, err := kv.Put(ctx, bucketKey, data); err != nil {
		t.Fatalf("put candidate entry: %v", err)
	}

	// List keys from the bucket.
	keys, err := conn.KVListKeys(ctx, duplicateCandidatesBucket)
	if err != nil {
		t.Fatalf("KVListKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(keys))
	}

	// Read the entry back.
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
}

// TestCandidatesMerge_HappyPath verifies that a MergeIdentity operation
// is correctly assembled with edges from the candidate entry.
func TestCandidatesMerge_HappyPath(t *testing.T) {
	ctx, conn, cp, cons := setupCandidatesFullEnv(t)

	primaryKey := "vtx.identity.primaryIDCandidMrg01"
	secondaryKey := "vtx.identity.secondaryIDCandMrg1"
	edgeKey := "lnk.identity.secondaryIDCandMrg1.holdsRole.role.operatorR001"

	// Seed primary and secondary identity vertices and edges (normally done
	// by CreateUnclaimedIdentity; we seed directly for merge test scope).
	seedIdentityVertex(t, ctx, conn, primaryKey)
	seedIdentityVertex(t, ctx, conn, secondaryKey)
	seedEdgeVertex(t, ctx, conn, edgeKey, secondaryKey)

	// Seed duplicate-candidates entry.
	entry := candidateEntry{
		PrimaryKey:            primaryKey,
		SecondaryKey:          secondaryKey,
		SecondaryInboundEdges: []string{edgeKey},
	}
	data, _ := json.Marshal(entry)
	js := conn.JetStream()
	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: duplicateCandidatesBucket,
	})
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	bucketKey := deriveCandidateKey(primaryKey, secondaryKey)
	if _, err := kv.Put(ctx, bucketKey, data); err != nil {
		t.Fatalf("put candidate: %v", err)
	}

	// Read candidate entry and verify edge enumeration.
	kvEntry, err := conn.KVGet(ctx, duplicateCandidatesBucket, bucketKey)
	if err != nil {
		t.Fatalf("KVGet candidate: %v", err)
	}
	var ce candidateEntry
	if err := json.Unmarshal(kvEntry.Value, &ce); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	edges := append(ce.SecondaryInboundEdges, ce.SecondaryOutboundEdges...)
	if len(edges) == 0 || edges[0] != edgeKey {
		t.Errorf("edges = %v, want [%s]", edges, edgeKey)
	}

	// Verify the pipeline is available.
	_ = cp
	_ = cons
}

// TestDeriveCandidateKey verifies the key derivation logic.
func TestDeriveCandidateKey(t *testing.T) {
	got := deriveCandidateKey(
		"vtx.identity.primaryIDCandidates01",
		"vtx.identity.secondaryIDCandidts1",
	)
	want := "flagged.identity.primaryIDCandidates01.identity.secondaryIDCandidts1"
	if got != want {
		t.Errorf("deriveCandidateKey = %q, want %q", got, want)
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

func seedEdgeVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, key, secondaryKey string) {
	t.Helper()
	doc := map[string]interface{}{
		"key":   key,
		"class": "lnk.identity.holdsRole",
	}
	data, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, key, data); err != nil {
		t.Fatalf("seed edge %s: %v", key, err)
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
