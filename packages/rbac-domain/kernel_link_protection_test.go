package rbacdomain_test

// The kernel-topology-link guard is pinned unit-wise in internal/processor over
// hand-built mutation slices and a hand-wired key set. Those pins prove the
// predicate; they cannot prove that the SHIPPED ops reach it — that RevokeRole's
// tombstone of a real seeded edge is refused on the real op path, and that
// AssignRole's revive of one is admitted with the seeder's provenance intact.
// Between the two lives everything a unit pin stubs out: the DDL's own guards,
// the hydrator's read set, step 6's stored-class gate, the OCC conditioning,
// and a protected-key set wired from the same loaded table the graph was
// seeded from.
//
// So both halves are driven through the whole pipeline against a really-seeded
// kernel here.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// rmRevokingOpID stands in for the op tracker of the revocation that bricked an
// edge, in the already-revoked state the heal below starts from.
const rmRevokingOpID = "RmRvkngpXzBbCdEfGhJK"

// readStoredDoc returns the decoded document stored at key.
func readStoredDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) map[string]any {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	return doc
}

// kernelLinkPipeline builds a pipeline whose committer holds the same protected
// link set cmd/processor composes at start-up — from the loaded table this
// harness seeded the graph from. Wiring nothing would leave the guard
// protecting no link, and every assertion below would pass or fail for reasons
// having nothing to do with the guard.
func kernelLinkPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	kernelLinks, err := bootstrap.KernelTopologyLinkKeys()
	if err != nil {
		t.Fatalf("KernelTopologyLinkKeys: %v", err)
	}
	if len(kernelLinks) != 12 {
		t.Fatalf("the wired set holds %d keys, want the kernel's twelve", len(kernelLinks))
	}
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:        durable,
		Instance:       "rm-" + durable,
		KernelLinkKeys: kernelLinks,
	})
}

// TestKernelLink_RevokeRoleOnThePrimordialAdminIsRefused submits the exact op
// that filed this design's row — RevokeRole against the primordial admin's own
// `holdsRole → operator` edge, from an actor the package really grants
// RevokeRole to — and asserts the reply is a ProtectedKey rejection naming the
// edge. Before the guard this committed, and the deployment lost the operator
// grant behind every kernel operation with nothing on any path to restore it.
func TestKernelLink_RevokeRoleOnThePrimordialAdminIsRefused(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := kernelLinkPipeline(t, ctx, conn, "kernrevoke")

	linkKey := bootstrap.BootstrapHoldsRoleLinkKey
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("RmKernRvkAdmn"),
		Lane:          processor.LaneDefault,
		OperationType: "RevokeRole",
		Actor:         rmOperatorActorKey,
		SubmittedAt:   "2026-09-04T10:00:00Z",
		Class:         "rbac",
		Payload: json.RawMessage(`{"actorKey":"` + bootstrap.BootstrapIdentityKey +
			`","roleKey":"` + bootstrap.RoleOperatorKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{linkKey}},
	}

	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %q, want %q — the kernel's own operator edge was revocable", outcome, processor.OutcomeRejected)
	}
	if reply.Error == nil {
		t.Fatalf("rejected reply carries no error: %+v", reply)
	}
	if reply.Error.Code != processor.ErrCodeProtectedKey {
		t.Fatalf("reply code = %q, want %q (message: %s)",
			reply.Error.Code, processor.ErrCodeProtectedKey, reply.Error.Message)
	}
	// The details are what tell an operator WHICH edge was held and whose
	// authority it carries — a bare code sends them looking at the payload.
	if got, _ := reply.Error.Details["key"].(string); got != linkKey {
		t.Errorf("details.key = %q, want the refused link %q", got, linkKey)
	}
	if got, _ := reply.Error.Details["root"].(string); got != bootstrap.BootstrapIdentityKey {
		t.Errorf("details.root = %q, want the admin identity %q", got, bootstrap.BootstrapIdentityKey)
	}
	if got, _ := reply.Error.Details["op"].(string); got != "tombstone" {
		t.Errorf("details.op = %q, want \"tombstone\"", got)
	}

	// And the edge is still live. A rejection reply over a committed batch
	// would read identically from the reply alone.
	stored := readStoredDoc(t, ctx, conn, linkKey)
	if deleted, _ := stored["isDeleted"].(bool); deleted {
		t.Fatalf("the admin's holdsRole edge was tombstoned despite the rejection: %v", stored)
	}
}

// TestKernelLink_AssignRoleRevivesARevokedKernelEdge is the other half, and the
// reason the arm is not a blanket immutability rule: a deployment that suffered
// the brick before the guard shipped heals through AssignRole's revive_link and
// through nothing else (the seeder refuses to rewrite a soft tombstone, and a
// create conflicts on a tombstoned key forever).
//
// The already-revoked state is written the way RevokeRole itself would have
// left it — the WHOLE prior body, the seeder's creation triplet untouched,
// isDeleted flipped and a lastModified triplet stamped by the revocation — so
// the revive meets the shape it will actually meet in a bricked deployment.
func TestKernelLink_AssignRoleRevivesARevokedKernelEdge(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := kernelLinkPipeline(t, ctx, conn, "kernrevive")

	// Loom's edge rather than the admin's: the heal has to work for a service
	// actor's root-equivalence too, and a different edge keeps this test's
	// subject distinct from the refusal test's.
	linkKey := bootstrap.LoomHoldsRoleLinkKey
	seeded := readStoredDoc(t, ctx, conn, linkKey)
	createdAt, _ := seeded["createdAt"].(string)
	createdBy, _ := seeded["createdBy"].(string)
	createdByOp, _ := seeded["createdByOp"].(string)
	if createdAt == "" || createdBy == "" || createdByOp == "" {
		t.Fatalf("the seeded edge carries no creation triplet, so preservation cannot be asserted: %v", seeded)
	}

	revoked := make(map[string]any, len(seeded))
	for k, v := range seeded {
		revoked[k] = v
	}
	revoked["isDeleted"] = true
	revoked["lastModifiedAt"] = "2026-09-03T09:00:00Z"
	revoked["lastModifiedBy"] = rmOperatorActorKey
	revoked["lastModifiedByOp"] = "vtx.op." + rmRevokingOpID
	body, err := json.Marshal(revoked)
	if err != nil {
		t.Fatalf("marshal revoked link: %v", err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, linkKey, body); err != nil {
		t.Fatalf("write the revoked state at %s: %v", linkKey, err)
	}

	// The revive branch is reachable only when the caller declares the link key
	// in optionalReads — a first grant legitimately finds it absent, so it can
	// never be a required read (Contract #2 §2.5).
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("RmKernRevLoom"),
		Lane:          processor.LaneDefault,
		OperationType: "AssignRole",
		Actor:         rmOperatorActorKey,
		SubmittedAt:   "2026-09-04T10:01:00Z",
		Class:         "rbac",
		Payload: json.RawMessage(`{"actorKey":"` + bootstrap.LoomIdentityKey +
			`","roleKey":"` + bootstrap.RoleOperatorKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{bootstrap.LoomIdentityKey, bootstrap.RoleOperatorKey},
			OptionalReads: []string{linkKey},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	revived := readStoredDoc(t, ctx, conn, linkKey)
	if deleted, _ := revived["isDeleted"].(bool); deleted {
		t.Fatalf("the revive did not bring the edge back: %v", revived)
	}
	if got, _ := revived["class"].(string); got != "holdsRole" {
		t.Errorf("revived class = %q, want holdsRole", got)
	}
	if got, _ := revived["targetVertex"].(string); got != bootstrap.RoleOperatorKey {
		t.Errorf("revived targetVertex = %q, want %q", got, bootstrap.RoleOperatorKey)
	}

	// The heal must not rewrite the kernel's own history: an update writes the
	// whole value, so the creation triplet survives only because the committer
	// carries it over from the stored document. Losing it would leave the
	// kernel topology claiming it was authored by whoever ran the heal.
	for field, want := range map[string]string{
		"createdAt":   createdAt,
		"createdBy":   createdBy,
		"createdByOp": createdByOp,
	} {
		if got, _ := revived[field].(string); got != want {
			t.Errorf("the revive rewrote %s: got %q, want the seeder's %q", field, got, want)
		}
	}
	if got, _ := revived["lastModifiedByOp"].(string); got == "vtx.op."+rmRevokingOpID {
		t.Errorf("lastModifiedByOp is still the revoking operation %q — the revive stamped nothing", got)
	}
}
