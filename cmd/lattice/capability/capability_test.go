package capability

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// wantScopeCheckFailure is the §5 no-escalation refusal
// (pkgmgr.validateGrantArtifact): the reason a correctly-routed held-permission
// set must produce, so a test asserting only "invalid" cannot pass on an
// unrelated failure.
const wantScopeCheckFailure = `requesting operator does not hold "CreateTask" at scope "any" or broader`

// bootstrapJSONForTest returns the path of the primordial identifier file
// `lattice capability review` reads through --bootstrap-json /
// BOOTSTRAP_JSON_PATH.
//
// The tests pass a PATH rather than relying on the in-process identifier
// table, because that table is exactly what the CLI binary does not have:
// cmd/lattice's root command loads no bootstrap file, so a test leaning on
// pre-populated globals would assert nothing about the binary's own wiring.
func bootstrapJSONForTest(t *testing.T) string {
	t.Helper()
	return testutil.PrimordialsFilePath(t)
}

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

	verdict, err := freshApprovalVerdict(ctx, conn, "capPropTwoHJKMNPQRST", bootstrapJSONForTest(t))
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

	verdict, err := freshApprovalVerdict(ctx, conn, "capPropThreeHJKMNPQRS", bootstrapJSONForTest(t))
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

	// The requester is an ordinary actor, so its held permissions come from
	// the role-derived cap.roles.<rest> key alone. It holds CreateTask at
	// scope "any" there — the grant's own requested scope is a subset.
	rest := "identity.grantReqHJKMNPQRST"
	putBucketEntry(t, ctx, conn, bootstrap.CapabilityKVBucket, "cap.roles."+rest, processor.CapabilityDoc{
		Actor:               requester,
		PlatformPermissions: []processor.PlatformPermission{{OperationType: "CreateTask", Scope: "any"}},
	})

	verdict, err := freshApprovalVerdict(ctx, conn, "capPropGrantOkHJKMNPQ", bootstrapJSONForTest(t))
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

	verdict, err := freshApprovalVerdict(ctx, conn, "capPropGrantBadHJKMNPQ", bootstrapJSONForTest(t))
	if err != nil {
		t.Fatalf("freshApprovalVerdict: %v", err)
	}
	if verdict["state"] != "invalid" {
		t.Fatalf("verdict = %+v, want invalid (requester holds nothing, cannot grant CreateTask/any)", verdict)
	}
}

// TestFreshApprovalVerdict_OrdinaryActorAnchorDocIsNotHeld: an ordinary
// actor's cap.<rest> anchor doc is not part of its grant set — step 3 routes
// an actor outside the system set to cap.roles.<rest> alone — so a permission
// that exists only there can never bound what its proposal may confer. The
// requester below holds CreateTask/any in the anchor doc and nothing in the
// roles projection, so the grant it proposes exceeds its own held scope.
func TestFreshApprovalVerdict_OrdinaryActorAnchorDocIsNotHeld(t *testing.T) {
	ctx, conn := setupCapabilityEnv(t)
	requester := "vtx.identity.anchorRestReqHJKMNPQ"
	seedPendingGrantProposal(t, ctx, conn, "capPropAnchorHJKMNPQ", requester, "CreateTask", "any", []string{"operator"})

	putBucketEntry(t, ctx, conn, bootstrap.CapabilityKVBucket, "cap.identity.anchorRestReqHJKMNPQ", processor.CapabilityDoc{
		Actor:               requester,
		PlatformPermissions: []processor.PlatformPermission{{OperationType: "CreateTask", Scope: "any"}},
	})

	verdict, err := freshApprovalVerdict(ctx, conn, "capPropAnchorHJKMNPQ", bootstrapJSONForTest(t))
	if err != nil {
		t.Fatalf("freshApprovalVerdict: %v", err)
	}
	if verdict["state"] != "invalid" {
		t.Fatalf("verdict = %+v, want invalid — an ordinary actor's anchor doc must not count as held", verdict)
	}
	// The reason matters: an "invalid" reached by any other route would let
	// this pass while the scope check itself was broken or skipped.
	report, _ := verdict["report"].(string)
	if !strings.Contains(report, wantScopeCheckFailure) {
		t.Fatalf("report = %q, want it to name the scope check: %q", report, wantScopeCheckFailure)
	}
}

// TestFreshApprovalVerdict_SystemActorAnchorDocIsHeld is the positive vector
// the test above needs to not be vacuous: the SAME anchor-doc-only seeding
// validates when the requester really is a system actor (it holds the
// primordial operator role through a live holdsRole link, which is the
// predicate bootstrap.SystemActorKeys discovers).
//
// It also exercises the CLI's own identifier source: the roleOperator NanoID
// the seeded link key is built from comes from the bootstrap FILE the command
// reads, not from an in-process table a test populated on its behalf.
func TestFreshApprovalVerdict_SystemActorAnchorDocIsHeld(t *testing.T) {
	bootstrapPath := bootstrapJSONForTest(t)
	ctx, conn := setupCapabilityEnv(t)
	const requesterID = "systemActorReqHJKMNP"
	requester := "vtx.identity." + requesterID
	seedPendingGrantProposal(t, ctx, conn, "capPropSysActrHJKMNP", requester, "CreateTask", "any", []string{"operator"})

	putBucketEntry(t, ctx, conn, bootstrap.CoreKVBucket,
		"lnk.identity."+requesterID+".holdsRole.role."+bootstrap.RoleOperatorID,
		map[string]any{"class": "holdsRole", "isDeleted": false, "data": map[string]any{}})
	putBucketEntry(t, ctx, conn, bootstrap.CapabilityKVBucket, "cap.identity."+requesterID, processor.CapabilityDoc{
		Actor:               requester,
		PlatformPermissions: []processor.PlatformPermission{{OperationType: "CreateTask", Scope: "any"}},
	})

	verdict, err := freshApprovalVerdict(ctx, conn, "capPropSysActrHJKMNP", bootstrapPath)
	if err != nil {
		t.Fatalf("freshApprovalVerdict: %v", err)
	}
	if verdict["state"] != "valid" {
		t.Fatalf("verdict = %+v, want valid — a system actor's anchor doc IS part of its grant set", verdict)
	}
}

// TestFreshApprovalVerdict_GrantReadsTheBootstrapFile: the system-actor
// routing is keyed on the primordial roleOperator NanoID, which lives only in
// the bootstrap file — cmd/lattice's root command loads no such file, so the
// grant path must load it itself. Pointed at a path that does not exist, the
// approve must fail loudly and name the path.
//
// Without that load the identifier is empty, SystemActorKeys matches no link,
// and every requester — the primordial admin and the kernel service actors
// included — silently routes as an ordinary actor: a legitimate grant proposal
// then reports invalid with no indication why.
func TestFreshApprovalVerdict_GrantReadsTheBootstrapFile(t *testing.T) {
	ctx, conn := setupCapabilityEnv(t)
	requester := "vtx.identity.needsBootstrapHJKMNP"
	seedPendingGrantProposal(t, ctx, conn, "capPropNoBootHJKMNPQ", requester, "CreateTask", "any", []string{"operator"})

	missing := filepath.Join(t.TempDir(), "absent.lattice.bootstrap.json")
	_, err := freshApprovalVerdict(ctx, conn, "capPropNoBootHJKMNPQ", missing)
	if err == nil {
		t.Fatal("approve of a grant proposal succeeded with no bootstrap file — the identifier table was never loaded")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("err = %v, want it to name the bootstrap path %q", err, missing)
	}
	// A machine that cannot obtain the identifier table has not judged the
	// proposal, and `-o json` must not report it as though it had: the
	// approve path renders this class as BootstrapLoadError, everything else
	// as ValidationError.
	if !errors.Is(err, errBootstrapIdentifiers) {
		t.Fatalf("err = %v, want it to wrap errBootstrapIdentifiers so the CLI reports BootstrapLoadError", err)
	}
}

// TestFreshApprovalVerdict_ValidationFailureIsNotABootstrapError is the
// discriminator's other side: a proposal that genuinely fails §5 must NOT be
// tagged as a configuration fault, or the two become indistinguishable again.
func TestFreshApprovalVerdict_ValidationFailureIsNotABootstrapError(t *testing.T) {
	ctx, conn := setupCapabilityEnv(t)
	requester := "vtx.identity.anchorRestReqHJKMNPQ"
	seedPendingGrantProposal(t, ctx, conn, "capPropAnchorHJKMNPQ", requester, "CreateTask", "any", []string{"operator"})

	verdict, err := freshApprovalVerdict(ctx, conn, "capPropAnchorHJKMNPQ", bootstrapJSONForTest(t))
	if err != nil {
		t.Fatalf("freshApprovalVerdict: %v", err)
	}
	if verdict["state"] != "invalid" {
		t.Fatalf("verdict = %+v, want invalid", verdict)
	}
}

// TestFreshApprovalVerdict_NonGrantNeedsNoBootstrapFile: only the grant kind
// consults the system-actor set, so a lens approve must not start demanding a
// bootstrap file (or paying for a core-kv listing) to re-validate.
func TestFreshApprovalVerdict_NonGrantNeedsNoBootstrapFile(t *testing.T) {
	ctx, conn := setupCapabilityEnv(t)
	seedPendingLensProposal(t, ctx, conn, "capPropNoBootLnsHJKM", "MATCH (p:provider) RETURN p.key AS key")

	missing := filepath.Join(t.TempDir(), "absent.lattice.bootstrap.json")
	verdict, err := freshApprovalVerdict(ctx, conn, "capPropNoBootLnsHJKM", missing)
	if err != nil {
		t.Fatalf("freshApprovalVerdict: %v", err)
	}
	if verdict["state"] != "valid" {
		t.Fatalf("verdict = %+v, want valid", verdict)
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
