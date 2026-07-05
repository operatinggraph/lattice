package capability

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
)

func setupCapabilityEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	url := testutil.StartEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "capability-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)
	testutil.ProvisionHarness(t, ctx, conn)
	return ctx, conn
}

func putBucketEntry(t *testing.T, ctx context.Context, conn *substrate.Conn, bucket, key string, v any) {
	t.Helper()
	js := conn.JetStream()
	// ProvisionHarness already creates capability-kv (with a TTL config);
	// re-creating it here with a bare config conflicts. Reuse it if present,
	// only creating buckets ProvisionHarness didn't already provision
	// (capability-proposals).
	kv, err := js.KeyValue(ctx, bucket)
	if err != nil {
		kv, err = js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket})
		if err != nil {
			t.Fatalf("create bucket %q: %v", bucket, err)
		}
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := kv.Put(ctx, key, data); err != nil {
		t.Fatalf("put %s/%s: %v", bucket, key, err)
	}
}

func seedPendingLensProposal(t *testing.T, ctx context.Context, conn *substrate.Conn, proposalID, spec string) {
	t.Helper()
	content, _ := json.Marshal(pkgmgr.LensArtifactContent{
		CanonicalName: "activeProvidersBySpecialty",
		Adapter:       "nats-kv",
		Bucket:        "active-providers",
		Spec:          spec,
	})
	row := proposalRow{
		Key:               "vtx.capabilityproposal." + proposalID,
		ProposalKey:       "vtx.capabilityproposal." + proposalID,
		RequesterID:       "vtx.identity.reqIdentityHJKMNPQR",
		Intent:            "a lens listing active providers by specialty",
		Kind:              "lens",
		Content:           string(content),
		TargetMode:        "newPackage",
		TargetPackageName: "ai-lens-pkg",
		ReviewState:       "pending",
	}
	putBucketEntry(t, ctx, conn, proposalsBucket, row.Key, row)
}

func seedPendingGrantProposal(t *testing.T, ctx context.Context, conn *substrate.Conn, proposalID, requesterID, operationType, scope string, grantsTo []string) {
	t.Helper()
	content, _ := json.Marshal(pkgmgr.GrantArtifactContent{
		OperationType: operationType,
		Scope:         scope,
		GrantsTo:      grantsTo,
	})
	row := proposalRow{
		Key:               "vtx.capabilityproposal." + proposalID,
		ProposalKey:       "vtx.capabilityproposal." + proposalID,
		RequesterID:       requesterID,
		Intent:            "widen a role's permissions",
		Kind:              "grant",
		Content:           string(content),
		TargetMode:        "newPackage",
		TargetPackageName: "ai-grant-pkg",
		ReviewState:       "pending",
	}
	putBucketEntry(t, ctx, conn, proposalsBucket, row.Key, row)
}

func TestReadProposals_FiltersAndParses(t *testing.T) {
	ctx, conn := setupCapabilityEnv(t)
	seedPendingLensProposal(t, ctx, conn, "capPropOneHJKMNPQRST", "MATCH (p:provider) RETURN p.key AS key")

	rows, err := readProposals(ctx, conn)
	if err != nil {
		t.Fatalf("readProposals: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Kind != "lens" || rows[0].ReviewState != "pending" {
		t.Fatalf("unexpected row: %+v", rows[0])
	}
}

func TestFreshApprovalVerdict_LensValid(t *testing.T) {
	ctx, conn := setupCapabilityEnv(t)
	seedPendingLensProposal(t, ctx, conn, "capPropTwoHJKMNPQRST", "MATCH (p:provider) RETURN p.key AS key")

	verdict, err := freshApprovalVerdict(ctx, conn, "capPropTwoHJKMNPQRST")
	if err != nil {
		t.Fatalf("freshApprovalVerdict: %v", err)
	}
	if verdict["state"] != "valid" {
		t.Fatalf("verdict = %+v, want valid", verdict)
	}
}

func TestFreshApprovalVerdict_LensUnparseableCypher(t *testing.T) {
	ctx, conn := setupCapabilityEnv(t)
	seedPendingLensProposal(t, ctx, conn, "capPropThreeHJKMNPQRS", "not cypher at all {{{")

	verdict, err := freshApprovalVerdict(ctx, conn, "capPropThreeHJKMNPQRS")
	if err != nil {
		t.Fatalf("freshApprovalVerdict: %v", err)
	}
	if verdict["state"] != "invalid" {
		t.Fatalf("verdict = %+v, want invalid", verdict)
	}
	if verdict["report"] == "" {
		t.Fatalf("expected a non-empty report explaining the rejection")
	}
}

func TestFreshApprovalVerdict_GrantWithinRequesterScope(t *testing.T) {
	ctx, conn := setupCapabilityEnv(t)
	requester := "vtx.identity.grantReqHJKMNPQRST"
	seedPendingGrantProposal(t, ctx, conn, "capPropGrantOkHJKMNPQ", requester, "CreateTask", "any", []string{"operator"})

	// The requester holds CreateTask at scope "any" via the platform-scope
	// cap.<rest> key — the grant's own requested scope is a subset.
	rest := "identity.grantReqHJKMNPQRST"
	putBucketEntry(t, ctx, conn, bootstrap.CapabilityKVBucket, "cap."+rest, processor.CapabilityDoc{
		Actor:               requester,
		PlatformPermissions: []processor.PlatformPermission{{OperationType: "CreateTask", Scope: "any"}},
	})

	verdict, err := freshApprovalVerdict(ctx, conn, "capPropGrantOkHJKMNPQ")
	if err != nil {
		t.Fatalf("freshApprovalVerdict: %v", err)
	}
	if verdict["state"] != "valid" {
		t.Fatalf("verdict = %+v, want valid (requester holds the operationType/scope being granted)", verdict)
	}
}

func TestFreshApprovalVerdict_GrantExceedsRequesterScope(t *testing.T) {
	ctx, conn := setupCapabilityEnv(t)
	requester := "vtx.identity.grantReqNoScopeHJKMNP"
	seedPendingGrantProposal(t, ctx, conn, "capPropGrantBadHJKMNPQ", requester, "CreateTask", "any", []string{"operator"})
	// Deliberately do not seed any capability-kv entry for the requester —
	// an absent held-permission projection must fail closed (invalid), never
	// silently pass the scope check.

	verdict, err := freshApprovalVerdict(ctx, conn, "capPropGrantBadHJKMNPQ")
	if err != nil {
		t.Fatalf("freshApprovalVerdict: %v", err)
	}
	if verdict["state"] != "invalid" {
		t.Fatalf("verdict = %+v, want invalid (requester holds nothing, cannot grant CreateTask/any)", verdict)
	}
}

func TestHeldPermissionsForActor_UnionsPlatformAndRoleKeys(t *testing.T) {
	ctx, conn := setupCapabilityEnv(t)
	actor := "vtx.identity.unionActorHJKMNPQRST"
	rest := "identity.unionActorHJKMNPQRST"

	putBucketEntry(t, ctx, conn, bootstrap.CapabilityKVBucket, "cap."+rest, processor.CapabilityDoc{
		Actor:               actor,
		PlatformPermissions: []processor.PlatformPermission{{OperationType: "CreateTask", Scope: "any"}},
	})
	putBucketEntry(t, ctx, conn, bootstrap.CapabilityKVBucket, "cap.roles."+rest, processor.CapabilityDoc{
		Actor:               actor,
		PlatformPermissions: []processor.PlatformPermission{{OperationType: "GrantPermission", Scope: "self"}},
	})

	held, err := heldPermissionsForActor(ctx, conn, actor)
	if err != nil {
		t.Fatalf("heldPermissionsForActor: %v", err)
	}
	if len(held) != 2 {
		t.Fatalf("expected 2 held permissions (platform ∪ roles), got %d: %+v", len(held), held)
	}
	want := map[pkgmgr.HeldPermission]bool{
		{OperationType: "CreateTask", Scope: "any"}:       true,
		{OperationType: "GrantPermission", Scope: "self"}: true,
	}
	for _, h := range held {
		if !want[h] {
			t.Fatalf("unexpected held permission %+v", h)
		}
	}
}

func TestHeldPermissionsForActor_AbsentKeysAreEmptyNotError(t *testing.T) {
	ctx, conn := setupCapabilityEnv(t)
	held, err := heldPermissionsForActor(ctx, conn, "vtx.identity.neverGrantedHJKMNPQR")
	if err != nil {
		t.Fatalf("heldPermissionsForActor: %v", err)
	}
	if len(held) != 0 {
		t.Fatalf("expected no held permissions, got %+v", held)
	}
}

func TestValidateBareID_RejectsKeyShapeMetacharacters(t *testing.T) {
	for _, bad := range []string{"", "has.dot", "has*star", "has>gt", "has space", "has\ttab"} {
		if err := validateBareID(bad); err == nil {
			t.Errorf("validateBareID(%q) = nil, want an error", bad)
		}
	}
	if err := validateBareID("capPropOneHJKMNPQRST"); err != nil {
		t.Errorf("validateBareID(bare id) = %v, want nil", err)
	}
}

func TestReadProposal_RejectsNonBareProposalID(t *testing.T) {
	ctx, conn := setupCapabilityEnv(t)
	if _, err := readProposal(ctx, conn, "vtx.capabilityproposal.someId"); err == nil {
		t.Fatal("readProposal with a dotted id = nil error, want rejection")
	}
}
