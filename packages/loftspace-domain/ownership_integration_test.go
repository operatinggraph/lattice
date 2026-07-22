// Ownership-link integration tests for the loftspace-domain Capability Package.
//
// External test package (loftspacedomain_test): seed the kernel + the phase-1
// packages + location-domain + loftspace-domain through the real meta-install
// pipeline (setupLoftspaceEnv), then submit AssignUnitOwner / RemoveUnitOwner and
// assert the committed landlord→unit management link
// (lnk.identity.<landlordID>.manages.unit.<unitID>, class "manages") — the
// ownership relationship the cap-read.residence grant lens projects (D1.3).
//
// Coverage:
//  1. TestLoftspace_AssignUnitOwner            — link committed alive, class/source/target correct
//  2. TestLoftspace_AssignUnitOwnerIdempotent  — re-assign is a clean no-op (one live link, no conflict)
//  3. TestLoftspace_RemoveThenReassign         — remove tombstones; re-assign revives (CAS), alive again
//  4. TestLoftspace_AssignRejectsDeadLandlord  — tombstoned landlord identity → Rejected, no link
//  5. TestLoftspace_AssignRejectsNonLocationUnit — unit-shaped key, wrong class → Rejected, no link
//  6. TestLoftspace_RemoveUnitOwnerNoLink      — remove with no link → accepted no-op, nothing committed
//  7. TestLoftspace_AssignUnauthorizedDenied   — consumer cap (no ownership ops) → Rejected
package loftspacedomain_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// lsLandlordID is a valid 20-char Contract #1 NanoID (no I/O/l/0): the happy
// path mints a real management link, whose key the Processor classifies.
const (
	lsLandlordID  = "LSmgrActorHJKMNPQRST"
	lsLandlordKey = "vtx.identity." + lsLandlordID
)

// manageLinkKey is the deterministic per-(landlord, unit) management link key.
func manageLinkKey(landlordKey, unitKey string) string {
	_, lid, _ := substrate.ParseVertexKey(landlordKey)
	_, uid, _ := substrate.ParseVertexKey(unitKey)
	return "lnk.identity." + lid + ".manages.unit." + uid
}

// assignUnitOwner submits AssignUnitOwner(landlord, unit) with the expected
// outcome. Both endpoints are listed in ContextHint.Reads (alive-checked
// in-script); the management link is read on demand.
func assignUnitOwner(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, landlordKey, unitKey string, actor string, want processor.MessageOutcome) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "AssignUnitOwner",
		Actor:         actor,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "loftspaceOwnership",
		Payload:       json.RawMessage(`{"landlord":"` + landlordKey + `","unit":"` + unitKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{landlordKey, unitKey},
			OptionalReads: []string{manageLinkKey(landlordKey, unitKey)},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// removeUnitOwner submits RemoveUnitOwner(landlord, unit). The link is read on
// demand (d, declared optionalReads — it may not exist, idempotent no-op).
func removeUnitOwner(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, landlordKey, unitKey string, want processor.MessageOutcome) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "RemoveUnitOwner",
		Actor:         lsStaffActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "loftspaceOwnership",
		Payload:       json.RawMessage(`{"landlord":"` + landlordKey + `","unit":"` + unitKey + `"}`),
		ContextHint:   &processor.ContextHint{OptionalReads: []string{manageLinkKey(landlordKey, unitKey)}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// TestLoftspace_AssignUnitOwner: a landlord identity + a location unit →
// AssignUnitOwner commits the management link, alive, class "manages", source =
// the landlord identity, target = the unit.
func TestLoftspace_AssignUnitOwner(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "assign")

	lsSeedVertex(t, ctx, conn, lsLandlordKey, "identity", false)
	unitKey := createUnit(t, ctx, conn, cp, cons)

	assignUnitOwner(t, ctx, conn, cp, cons, "assign0001", lsLandlordKey, unitKey, lsStaffActorKey, processor.OutcomeAccepted)

	lk := manageLinkKey(lsLandlordKey, unitKey)
	doc := lsReadDoc(t, ctx, conn, lk)
	if doc["class"] != "manages" {
		t.Fatalf("link class = %v, want manages", doc["class"])
	}
	if del, _ := doc["isDeleted"].(bool); del {
		t.Fatalf("management link should be alive; got isDeleted=%v", del)
	}
	if sv, _ := doc["sourceVertex"].(string); sv != lsLandlordKey {
		t.Fatalf("link sourceVertex = %q, want %q (the landlord identity)", sv, lsLandlordKey)
	}
	if tv, _ := doc["targetVertex"].(string); tv != unitKey {
		t.Fatalf("link targetVertex = %q, want %q (the unit)", tv, unitKey)
	}
}

// TestLoftspace_AssignUnitOwnerIdempotent: a second AssignUnitOwner on the same
// pair is a clean no-op (no RevisionConflict, the link stays single + alive).
func TestLoftspace_AssignUnitOwnerIdempotent(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "assign-idem")

	lsSeedVertex(t, ctx, conn, lsLandlordKey, "identity", false)
	unitKey := createUnit(t, ctx, conn, cp, cons)

	assignUnitOwner(t, ctx, conn, cp, cons, "idem0001", lsLandlordKey, unitKey, lsStaffActorKey, processor.OutcomeAccepted)
	// Second assign: already live → idempotent no-op, still Accepted.
	assignUnitOwner(t, ctx, conn, cp, cons, "idem0002", lsLandlordKey, unitKey, lsStaffActorKey, processor.OutcomeAccepted)

	lk := manageLinkKey(lsLandlordKey, unitKey)
	doc := lsReadDoc(t, ctx, conn, lk)
	if del, _ := doc["isDeleted"].(bool); del {
		t.Fatalf("link should remain alive after a re-assign; got isDeleted=%v", del)
	}
}

// TestLoftspace_RemoveThenReassign: RemoveUnitOwner tombstones the link; a
// subsequent AssignUnitOwner revives it (CAS on the tombstone revision) → alive.
func TestLoftspace_RemoveThenReassign(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "remove-reassign")

	lsSeedVertex(t, ctx, conn, lsLandlordKey, "identity", false)
	unitKey := createUnit(t, ctx, conn, cp, cons)
	lk := manageLinkKey(lsLandlordKey, unitKey)

	assignUnitOwner(t, ctx, conn, cp, cons, "rr0001", lsLandlordKey, unitKey, lsStaffActorKey, processor.OutcomeAccepted)

	removeUnitOwner(t, ctx, conn, cp, cons, "rr0002", lsLandlordKey, unitKey, processor.OutcomeAccepted)
	dead := lsReadDoc(t, ctx, conn, lk)
	if del, _ := dead["isDeleted"].(bool); !del {
		t.Fatalf("link should be tombstoned after RemoveUnitOwner; got isDeleted=%v", del)
	}

	// Re-assign revives the tombstoned link (a blind create would collide).
	assignUnitOwner(t, ctx, conn, cp, cons, "rr0003", lsLandlordKey, unitKey, lsStaffActorKey, processor.OutcomeAccepted)
	revived := lsReadDoc(t, ctx, conn, lk)
	if del, _ := revived["isDeleted"].(bool); del {
		t.Fatalf("link should be alive again after re-assign (revive); got isDeleted=%v", del)
	}
}

// TestLoftspace_AssignRejectsDeadLandlord: a tombstoned landlord identity is
// rejected (no-orphan) and no link is committed.
func TestLoftspace_AssignRejectsDeadLandlord(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "dead-landlord")

	deadLandlord := "vtx.identity.LSdeadlordHJKMNPQR"
	lsSeedVertex(t, ctx, conn, deadLandlord, "identity", true) // alive=false
	unitKey := createUnit(t, ctx, conn, cp, cons)

	assignUnitOwner(t, ctx, conn, cp, cons, "deadL0001", deadLandlord, unitKey, lsStaffActorKey, processor.OutcomeRejected)
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, manageLinkKey(deadLandlord, unitKey)); err == nil {
		t.Fatalf("a management link was committed for a dead landlord")
	}
}

// TestLoftspace_AssignRejectsNonLocationUnit: a unit-shaped key whose class is
// not location is rejected (NotAUnit) and no link is committed.
func TestLoftspace_AssignRejectsNonLocationUnit(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "non-location")

	lsSeedVertex(t, ctx, conn, lsLandlordKey, "identity", false)
	fakeUnit := "vtx.unit.LSnotlocatnHJKMNPQR"
	lsSeedVertex(t, ctx, conn, fakeUnit, "identity", false) // unit-shaped key, wrong class

	assignUnitOwner(t, ctx, conn, cp, cons, "nonLoc0001", lsLandlordKey, fakeUnit, lsStaffActorKey, processor.OutcomeRejected)
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, manageLinkKey(lsLandlordKey, fakeUnit)); err == nil {
		t.Fatalf("a management link was committed for a non-location unit")
	}
}

// TestLoftspace_RemoveUnitOwnerNoLink: RemoveUnitOwner with no existing link is
// an accepted no-op; nothing is committed at the link key.
func TestLoftspace_RemoveUnitOwnerNoLink(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "remove-nolink")

	lsSeedVertex(t, ctx, conn, lsLandlordKey, "identity", false)
	unitKey := createUnit(t, ctx, conn, cp, cons)

	removeUnitOwner(t, ctx, conn, cp, cons, "noLink0001", lsLandlordKey, unitKey, processor.OutcomeAccepted)
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, manageLinkKey(lsLandlordKey, unitKey)); err == nil {
		t.Fatalf("a no-op RemoveUnitOwner should commit no link")
	}
}

// TestLoftspace_AssignUnauthorizedDenied: AssignUnitOwner as the consumer actor
// (no ownership permissions) → Rejected.
func TestLoftspace_AssignUnauthorizedDenied(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "owner-unauth")

	lsSeedVertex(t, ctx, conn, lsLandlordKey, "identity", false)
	unitKey := createUnit(t, ctx, conn, cp, cons)

	assignUnitOwner(t, ctx, conn, cp, cons, "ownAuth0001", lsLandlordKey, unitKey, lsConsumerKey, processor.OutcomeRejected)
}
