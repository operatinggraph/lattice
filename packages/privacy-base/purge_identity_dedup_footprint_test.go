// PurgeIdentityDedupFootprint (erasure-orchestration-design.md §5.4 step 4) —
// the erasure plane's dedup sweep, driven through the real Processor commit
// path so what these prove is what production does.
//
// Two carry more weight than the rest:
//
//   - TestPurgeIdentityDedupFootprint_WideSubject_ConvergesPastOnePage is the
//     convergence proof, and it is the same proof UnbindIdentityCredentials
//     needed for the same reason. A tombstone is a SOFT delete: the key stays
//     in the keyspace and kv.Links keeps returning it with isDeleted set, so
//     the cursor-less single call design §10 point 4 originally specified
//     would sweep one page and then stall on a first page of pure tombstones
//     forever, silently, while the erasure target re-dispatched a no-op.
//   - TestPurgeIdentityDedupFootprint_IndexesBeforeDuplicateOf pins the cost
//     ordering. An indexes hit is TWO mutations and a duplicateOf hit is one,
//     so the classes are swept one per commit rather than together; an
//     implementation that drained all three collectors at once would pass
//     every other test here while putting the op back within reach of the
//     wall budget that sized SWEEP_LIMIT in the first place.
package privacybase_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	privacybase "github.com/operatinggraph/lattice/packages/privacy-base"
)

const (
	pbPurgeActorID  = "BBpurDedupHJKMNPQRST"
	pbPurgeActorKey = "vtx.identity." + pbPurgeActorID

	pbPurgeUngrantedActorID  = "BBpurNoGrantHJKMNPQR"
	pbPurgeUngrantedActorKey = "vtx.identity." + pbPurgeUngrantedActorID
)

// purgeCapDoc grants PurgeIdentityDedupFootprint on the system lane — the
// grant shape both production submitters carry: the identityErasure pattern's
// fourth step under identity.system.loom, and the identityErasureComplete
// target's gap action under identity.system.weaver. Both are operator via
// holdsRole, and both reach this op through the one operator grant.
func purgeCapDoc() *processor.CapabilityDoc {
	doc := sealCapDoc()
	doc.Key = "cap.identity." + pbPurgeActorID
	doc.Actor = pbPurgeActorKey
	doc.ProjectedFromRevisions = map[string]uint64{pbPurgeActorKey: 1}
	doc.PlatformPermissions = []processor.PlatformPermission{
		{OperationType: "PurgeIdentityDedupFootprint", Scope: "any"},
	}
	doc.Roles = []string{bootstrap.RoleOperatorKey}
	return doc
}

// purgeCapDocMissingGrant is purgeCapDoc's control: the same system lane and
// the same operator role, granting a different privacy-base op. Without it a
// denial test would be satisfied by the lane check alone and would stay green
// if this op's grant were deleted outright.
func purgeCapDocMissingGrant() *processor.CapabilityDoc {
	doc := purgeCapDoc()
	doc.Key = "cap.identity." + pbPurgeUngrantedActorID
	doc.Actor = pbPurgeUngrantedActorKey
	doc.ProjectedFromRevisions = map[string]uint64{pbPurgeUngrantedActorKey: 1}
	doc.PlatformPermissions = []processor.PlatformPermission{
		{OperationType: "SealIdentityForErasure", Scope: "any"},
	}
	return doc
}

func setupPurgeEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, staffCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, sealCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, purgeCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, purgeCapDocMissingGrant())
	return ctx, conn
}

func newPurgePipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:        durable,
		Instance:       "purge-" + durable,
		FilterSubjects: []string{"ops.system"},
	})
}

// submitPurgeAs publishes one PurgeIdentityDedupFootprint and drives it to
// wantOutcome. The declared read-set mirrors what the identityErasure pattern's
// fourth step declares through StepSpec.Reads/OptionalReads;
// TestPurgeDeclaredReadSetMatchesThePatternStep is design §13's coverage guard
// holding the two together, so a fixture that drifted from the real dispatcher
// stops proving anything about it.
// purgeFixtureReads / purgeFixtureOptionalReads are the read-set every test in
// this file submits. They are functions rather than inline literals so
// TestPurgeDeclaredReadSetMatchesThePatternStep compares the pattern step
// against what the fixture ACTUALLY sends — a second hand-written copy would let
// the fixture drift from the dispatcher while the guard stayed green.
func purgeFixtureReads(subjectKey string) []string {
	return []string{subjectKey}
}

func purgeFixtureOptionalReads(subjectKey string) []string {
	return []string{subjectKey + ".erasureRequested"}
}

func submitPurgeAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, actorKey, subjectKey, reqLabel string, wantOutcome processor.MessageOutcome) string {
	t.Helper()
	reqID := testutil.GenReqID(reqLabel)
	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneSystem,
		OperationType: "PurgeIdentityDedupFootprint",
		Actor:         actorKey,
		SubmittedAt:   "2026-08-07T13:00:00Z",
		Class:         "purgeIdentityDedupFootprint",
		Payload:       json.RawMessage(`{"subjectKey":"` + subjectKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         purgeFixtureReads(subjectKey),
			OptionalReads: purgeFixtureOptionalReads(subjectKey),
			Enumerations: []processor.EnumerationHint{
				{Hub: subjectKey, Relation: "indexes", Direction: "in"},
				{Hub: subjectKey, Relation: "duplicateOf", Direction: "out"},
				{Hub: subjectKey, Relation: "duplicateOf", Direction: "in"},
			},
		},
	})
	testutil.DriveOne(t, ctx, cp, cons, wantOutcome)
	return reqID
}

func submitPurge(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, subjectKey, reqLabel string, wantOutcome processor.MessageOutcome) string {
	t.Helper()
	return submitPurgeAs(t, ctx, conn, cp, cons, pbPurgeActorKey, subjectKey, reqLabel, wantOutcome)
}

// pbSealForErasureAs writes the erasureRequested marker directly, at a chosen
// class and liveness.
//
// Seeded rather than submitted because the real SealIdentityForErasure
// requires a shredded piiKey, and today's ShredIdentityKey still erases the
// dedup footprint in its own commit (design §12 orders the narrowing last, so
// the shred and this op deliberately overlap until step 3 lands). Driving the
// real seal would therefore hand every test a subject whose footprint was
// already gone — the one input that cannot exercise the sweep. The chain the
// seal itself guards is proven by its own tests; what these need is a live
// footprint under a live marker.
func pbSealForErasureAs(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey, class string, tombstoned bool) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"class":     class,
		"vertexKey": identityKey,
		"localName": "erasureRequested",
		"isDeleted": tombstoned,
		"data": map[string]any{
			"requestedAt": "2026-08-07T09:00:00Z",
			"shreddedAt":  "2026-08-07T08:59:00Z",
		},
	})
	if err != nil {
		t.Fatalf("marshal erasureRequested for %s: %v", identityKey, err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey+".erasureRequested", raw); err != nil {
		t.Fatalf("seed erasureRequested on %s: %v", identityKey, err)
	}
}

func pbSealForErasure(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey string) {
	t.Helper()
	pbSealForErasureAs(t, ctx, conn, identityKey, "erasureRequested", false)
}

// livePurgeResidue counts the subject's non-tombstoned links on one relation
// in one direction, reading Core KV the same way the script's enumeration
// does. It pages past the link-list limit so a count over a wide subject is
// the real one rather than a first page.
func livePurgeResidue(t *testing.T, ctx context.Context, conn *substrate.Conn, subjectKey, relation, direction string) int {
	t.Helper()
	_, id, ok := substrate.ParseVertexKey(subjectKey)
	if !ok {
		t.Fatalf("ParseVertexKey %s", subjectKey)
	}
	filter := "lnk.*.*." + relation + ".identity." + id
	if direction == "out" {
		filter = "lnk.identity." + id + "." + relation + ".>"
	}
	live, cursor := 0, ""
	for page := 0; page < 200; page++ {
		keys, next, err := conn.KVListKeysFilter(ctx, testutil.HarnessCoreBucket, filter, cursor, 256)
		if err != nil {
			t.Fatalf("KVListKeysFilter %s: %v", filter, err)
		}
		for _, k := range keys {
			entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, k)
			if err != nil {
				t.Fatalf("KVGet %s: %v", k, err)
			}
			var doc map[string]any
			if err := json.Unmarshal(entry.Value, &doc); err != nil {
				t.Fatalf("unmarshal %s: %v", k, err)
			}
			if deleted, _ := doc["isDeleted"].(bool); !deleted {
				live++
			}
		}
		if next == "" {
			return live
		}
		cursor = next
	}
	t.Fatalf("livePurgeResidue %s: did not terminate within 200 pages", filter)
	return 0
}

// TestPurgeIdentityDedupFootprint_ErasesOwnedIndexesAndLinks is the ordinary
// path, over a footprint CreateUnclaimedIdentity really built: two owned
// identityindex vertices (email and name) with their inbound indexes links.
//
// Both the vertex and the link go. The vertex IS the hash of a contact value,
// and the link is the decrypt-free evidence tying it to this person — leaving
// either behind leaves a durable answer to a question about someone who has
// been erased.
func TestPurgeIdentityDedupFootprint_ErasesOwnedIndexesAndLinks(t *testing.T) {
	ctx, conn := setupPurgeEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "purge-idx-default", v)
	sysCP, sysCons := newPurgePipeline(t, ctx, conn, "purge-idx-system")

	identityKey := createIdentity(t, ctx, conn, cp, cons, "PurgeIdx")
	identityID := strings.TrimPrefix(identityKey, "vtx.identity.")

	emailIdxKey := pbContactIndexKey("email", "shred-purgeidx@example.com")
	nameIdxKey := pbContactIndexKey("name", "shred target")
	emailLinkKey := pbIndexesLinkKey(emailIdxKey, identityID)
	nameLinkKey := pbIndexesLinkKey(nameIdxKey, identityID)

	// The footprint is real and live before the sweep, so what the assertions
	// below observe is this op's doing rather than the fixture's.
	if readIsDeleted(t, ctx, conn, emailIdxKey) || readIsDeleted(t, ctx, conn, nameLinkKey) {
		t.Fatal("precondition: the dedup footprint is already tombstoned")
	}

	pbSealForErasure(t, ctx, conn, identityKey)
	submitPurge(t, ctx, conn, sysCP, sysCons, identityKey, "PurgeIdxDo", processor.OutcomeAccepted)

	for _, k := range []string{emailIdxKey, emailLinkKey, nameIdxKey, nameLinkKey} {
		if !readIsDeleted(t, ctx, conn, k) {
			t.Errorf("%s is still live after the sweep", k)
		}
	}
}

// TestPurgeIdentityDedupFootprint_ErasesDuplicateOfBothDirections — the
// subject may be either side of a pair: the later-arriving identity that
// matched an incumbent (source), or the incumbent that later identities
// matched against (target). Both are durable pair evidence naming the erased
// person in the key itself, so sweeping one direction would leave the other
// answering the question the erasure exists to stop answering.
//
// One subject, one pair on each side, so a build that enumerated only "out"
// or only "in" fails here rather than in a case nobody wrote.
func TestPurgeIdentityDedupFootprint_ErasesDuplicateOfBothDirections(t *testing.T) {
	ctx, conn := setupPurgeEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "purge-dup-default", v)
	sysCP, sysCons := newPurgePipeline(t, ctx, conn, "purge-dup-system")

	claimA := strings.Repeat("1", 64)
	claimB := strings.Repeat("2", 64)
	claimC := strings.Repeat("3", 64)

	// The subject is the incumbent of one pair (target side) and the newcomer
	// of another (source side).
	subjectKey, subjectID := createUnclaimedWithProbe(t, ctx, conn, cp, cons,
		"Purge Dup Subject", "purge-dup-a@example.com", claimA, "PurgeDupSubj", nil)

	idxA := pbContactIndexKey("email", "purge-dup-a@example.com")
	_, newcomerID := createUnclaimedWithProbe(t, ctx, conn, cp, cons,
		"Purge Dup Newcomer", "purge-dup-a@example.com", claimB, "PurgeDupNew", []string{idxA})
	inboundDup := pbDuplicateOfLinkKey(newcomerID, subjectKey)

	// A second incumbent the subject itself collided with — but the subject
	// already exists, so the outbound pair is seeded at the shape
	// CreateUnclaimedIdentity writes it.
	incumbentKey, _ := createUnclaimedWithProbe(t, ctx, conn, cp, cons,
		"Purge Dup Incumbent", "purge-dup-b@example.com", claimC, "PurgeDupInc", nil)
	outboundDup := pbDuplicateOfLinkKey(subjectID, incumbentKey)
	testutil.SeedLink(t, ctx, conn, outboundDup, "duplicateOf", subjectKey, incumbentKey)

	if readIsDeleted(t, ctx, conn, inboundDup) || readIsDeleted(t, ctx, conn, outboundDup) {
		t.Fatal("precondition: a duplicateOf link is already tombstoned")
	}

	pbSealForErasure(t, ctx, conn, subjectKey)

	// Three passes: the indexes class first, then each duplicateOf direction.
	// Re-dispatching until the residue stops moving is exactly what the
	// erasure target does.
	for _, label := range []string{"PurgeDup1", "PurgeDup2", "PurgeDup3"} {
		submitPurge(t, ctx, conn, sysCP, sysCons, subjectKey, label, processor.OutcomeAccepted)
	}

	if !readIsDeleted(t, ctx, conn, inboundDup) {
		t.Error("the inbound duplicateOf link is still live — the subject is the incumbent of that pair, and the link names it in the key")
	}
	if !readIsDeleted(t, ctx, conn, outboundDup) {
		t.Error("the outbound duplicateOf link is still live — the subject is the newcomer of that pair")
	}
	// The other people in those pairs are untouched: nobody is erasing them.
	if readIsDeleted(t, ctx, conn, incumbentKey) {
		t.Error("sweeping the subject must not tombstone the other side's identity vertex")
	}
}

// TestPurgeIdentityDedupFootprint_IndexesBeforeDuplicateOf pins the cost
// ordering that decides how much this op commits at once.
//
// An indexes hit costs two mutations (the index vertex and the link) where a
// duplicateOf hit costs one, so draining every collector in a single commit
// would reach 4*SWEEP_LIMIT and put the op back within reach of the Starlark
// wall budget that forced SWEEP_LIMIT below the read page size in the first
// place. One class per commit is what keeps any commit at 2*SWEEP_LIMIT. An
// implementation that swept them together passes every other test in this
// file; it fails here, on the first pass leaving the duplicateOf link alone.
func TestPurgeIdentityDedupFootprint_IndexesBeforeDuplicateOf(t *testing.T) {
	ctx, conn := setupPurgeEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "purge-order-default", v)
	sysCP, sysCons := newPurgePipeline(t, ctx, conn, "purge-order-system")

	claimA := strings.Repeat("4", 64)
	claimB := strings.Repeat("5", 64)
	incumbentKey, _ := createUnclaimedWithProbe(t, ctx, conn, cp, cons,
		"Purge Order Incumbent", "purge-order@example.com", claimA, "PurgeOrdInc", nil)

	idx := pbContactIndexKey("email", "purge-order@example.com")
	subjectKey, subjectID := createUnclaimedWithProbe(t, ctx, conn, cp, cons,
		"Purge Order Newcomer", "purge-order@example.com", claimB, "PurgeOrdNew", []string{idx})

	// The newcomer owns its own name index (the email one belongs to the
	// incumbent it collided with) and carries the outbound duplicateOf pair.
	nameIdx := pbContactIndexKey("name", "purge order newcomer")
	nameLink := pbIndexesLinkKey(nameIdx, subjectID)
	dupLink := pbDuplicateOfLinkKey(subjectID, incumbentKey)
	if readIsDeleted(t, ctx, conn, nameLink) || readIsDeleted(t, ctx, conn, dupLink) {
		t.Fatal("precondition: the subject's footprint is already tombstoned")
	}

	pbSealForErasure(t, ctx, conn, subjectKey)
	submitPurge(t, ctx, conn, sysCP, sysCons, subjectKey, "PurgeOrd1", processor.OutcomeAccepted)

	if !readIsDeleted(t, ctx, conn, nameLink) {
		t.Error("the first pass must sweep the indexes class")
	}
	if readIsDeleted(t, ctx, conn, dupLink) {
		t.Error("the first pass swept duplicateOf as well as indexes; one class per commit is what bounds the batch at 2*SWEEP_LIMIT")
	}

	submitPurge(t, ctx, conn, sysCP, sysCons, subjectKey, "PurgeOrd2", processor.OutcomeAccepted)
	if !readIsDeleted(t, ctx, conn, dupLink) {
		t.Error("the second pass must reach duplicateOf once the indexes class is exhausted")
	}
}

// TestPurgeIdentityDedupFootprint_UnsealedSubject_Rejected pairs the refusal
// with its own positive vector over one corpus: the identical envelope against
// the identical subject, with only the marker changing. Without the accepted
// half, a rejection assertion would be satisfied by any of the op's other
// guards.
func TestPurgeIdentityDedupFootprint_UnsealedSubject_Rejected(t *testing.T) {
	ctx, conn := setupPurgeEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "purge-seal-default", v)
	sysCP, sysCons := newPurgePipeline(t, ctx, conn, "purge-seal-system")

	identityKey := createIdentity(t, ctx, conn, cp, cons, "PurgeSeal")

	submitPurge(t, ctx, conn, sysCP, sysCons, identityKey, "PurgeSealNo", processor.OutcomeRejected)
	if got := livePurgeResidue(t, ctx, conn, identityKey, "indexes", "in"); got != 2 {
		t.Fatalf("an unsealed subject must keep its dedup footprint: live indexes links = %d, want 2", got)
	}

	// Change exactly one fact.
	pbSealForErasure(t, ctx, conn, identityKey)
	submitPurge(t, ctx, conn, sysCP, sysCons, identityKey, "PurgeSealYes", processor.OutcomeAccepted)
	if got := livePurgeResidue(t, ctx, conn, identityKey, "indexes", "in"); got != 0 {
		t.Fatalf("live indexes links after sealing = %d, want 0", got)
	}
}

// TestPurgeIdentityDedupFootprint_ForeignMarkerClass_Rejected — the marker's
// CLASS is what arms this verb, not its key. privacy-base's aspect-type DDL
// gates the class rather than the key, so any package script can write some
// other class there; a presence-only check would let such a write hand a
// service actor the right to break a live person's dedup hygiene.
func TestPurgeIdentityDedupFootprint_ForeignMarkerClass_Rejected(t *testing.T) {
	ctx, conn := setupPurgeEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "purge-class-default", v)
	sysCP, sysCons := newPurgePipeline(t, ctx, conn, "purge-class-system")

	identityKey := createIdentity(t, ctx, conn, cp, cons, "PurgeCls")

	pbSealForErasureAs(t, ctx, conn, identityKey, "someOtherClass", false)
	submitPurge(t, ctx, conn, sysCP, sysCons, identityKey, "PurgeClsNo", processor.OutcomeRejected)
	if got := livePurgeResidue(t, ctx, conn, identityKey, "indexes", "in"); got != 2 {
		t.Fatalf("a foreign-class marker must not arm the sweep: live indexes links = %d, want 2", got)
	}

	// The positive vector: the same key, the real class.
	pbSealForErasureAs(t, ctx, conn, identityKey, "erasureRequested", false)
	submitPurge(t, ctx, conn, sysCP, sysCons, identityKey, "PurgeClsYes", processor.OutcomeAccepted)
	if got := livePurgeResidue(t, ctx, conn, identityKey, "indexes", "in"); got != 0 {
		t.Fatalf("live indexes links after a real marker = %d, want 0", got)
	}
}

// TestPurgeIdentityDedupFootprint_TombstonedMarker_StillArms — a tombstoned
// marker still closes the write path, so it must still arm the sweep. A guard
// that reopened on a tombstone would be the one failure mode a fail-closed
// guard may not have: erasure would become un-completable by tombstoning one
// aspect.
func TestPurgeIdentityDedupFootprint_TombstonedMarker_StillArms(t *testing.T) {
	ctx, conn := setupPurgeEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "purge-tomb-default", v)
	sysCP, sysCons := newPurgePipeline(t, ctx, conn, "purge-tomb-system")

	identityKey := createIdentity(t, ctx, conn, cp, cons, "PurgeTomb")
	pbSealForErasureAs(t, ctx, conn, identityKey, "erasureRequested", true)

	submitPurge(t, ctx, conn, sysCP, sysCons, identityKey, "PurgeTombDo", processor.OutcomeAccepted)
	if got := livePurgeResidue(t, ctx, conn, identityKey, "indexes", "in"); got != 0 {
		t.Fatalf("a tombstoned marker must still arm the sweep: live indexes links = %d, want 0", got)
	}
}

// TestPurgeIdentityDedupFootprint_UngrantedActor_Denied proves the grant is
// what authorizes this, not the lane: the control actor holds the same
// operator role on the same system lane, granting a different privacy-base op.
func TestPurgeIdentityDedupFootprint_UngrantedActor_Denied(t *testing.T) {
	ctx, conn := setupPurgeEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "purge-auth-default", v)
	sysCP, sysCons := newPurgePipeline(t, ctx, conn, "purge-auth-system")

	identityKey := createIdentity(t, ctx, conn, cp, cons, "PurgeAuth")
	pbSealForErasure(t, ctx, conn, identityKey)

	submitPurgeAs(t, ctx, conn, sysCP, sysCons, pbPurgeUngrantedActorKey, identityKey, "PurgeAuthNo", processor.OutcomeRejected)
	if got := livePurgeResidue(t, ctx, conn, identityKey, "indexes", "in"); got != 2 {
		t.Fatalf("a denied sweep must change nothing: live indexes links = %d, want 2", got)
	}
	submitPurge(t, ctx, conn, sysCP, sysCons, identityKey, "PurgeAuthYes", processor.OutcomeAccepted)
}

// TestPurgeIdentityDedupFootprint_SecondRunIsNoOp — idempotence by tombstone.
// The pattern's step runs once but the erasure target re-dispatches this op
// every reconcile pass until the residue reaches zero, so a swept subject is
// the input it sees most often. A second run must rewrite nothing rather than
// re-tombstoning what is already gone, which is asserted as an unchanged
// revision — a bare re-accept would be satisfied by an op that wrote the same
// tombstone again.
func TestPurgeIdentityDedupFootprint_SecondRunIsNoOp(t *testing.T) {
	ctx, conn := setupPurgeEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "purge-idem-default", v)
	sysCP, sysCons := newPurgePipeline(t, ctx, conn, "purge-idem-system")

	identityKey := createIdentity(t, ctx, conn, cp, cons, "PurgeIdem")
	identityID := strings.TrimPrefix(identityKey, "vtx.identity.")
	emailIdxKey := pbContactIndexKey("email", "shred-purgeidem@example.com")
	emailLinkKey := pbIndexesLinkKey(emailIdxKey, identityID)

	pbSealForErasure(t, ctx, conn, identityKey)
	submitPurge(t, ctx, conn, sysCP, sysCons, identityKey, "PurgeIdem1", processor.OutcomeAccepted)

	firstVertex, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, emailIdxKey)
	if err != nil {
		t.Fatalf("KVGet identityindex: %v", err)
	}
	firstLink, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, emailLinkKey)
	if err != nil {
		t.Fatalf("KVGet indexes link: %v", err)
	}

	submitPurge(t, ctx, conn, sysCP, sysCons, identityKey, "PurgeIdem2", processor.OutcomeAccepted)

	secondVertex, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, emailIdxKey)
	if err != nil {
		t.Fatalf("KVGet identityindex after re-run: %v", err)
	}
	if secondVertex.Revision != firstVertex.Revision {
		t.Errorf("re-run rewrote an already-tombstoned identityindex (revision %d -> %d); the sweep must skip what it already swept",
			firstVertex.Revision, secondVertex.Revision)
	}
	secondLink, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, emailLinkKey)
	if err != nil {
		t.Fatalf("KVGet indexes link after re-run: %v", err)
	}
	if secondLink.Revision != firstLink.Revision {
		t.Errorf("re-run rewrote an already-tombstoned indexes link (revision %d -> %d)",
			firstLink.Revision, secondLink.Revision)
	}
}

// TestPurgeIdentityDedupFootprint_TombstonePreservesBody — the tombstones
// carry no document, because buildMutationValue seeds a tombstone's document
// from the PRIOR body. ShredIdentityKey spends a kv.Read per index vertex to
// carry that body forward by hand; skipping those reads is what buys this op
// headroom against the wall budget, and it is only sound if the verb really
// does preserve what the manual form preserved.
//
// A build that reverted to a bodyless update would leave a tombstone with no
// class and no data — indistinguishable from a key that never existed, to any
// auditor reading what an erasure removed.
func TestPurgeIdentityDedupFootprint_TombstonePreservesBody(t *testing.T) {
	ctx, conn := setupPurgeEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "purge-body-default", v)
	sysCP, sysCons := newPurgePipeline(t, ctx, conn, "purge-body-system")

	identityKey := createIdentity(t, ctx, conn, cp, cons, "PurgeBody")
	identityID := strings.TrimPrefix(identityKey, "vtx.identity.")
	emailIdxKey := pbContactIndexKey("email", "shred-purgebody@example.com")
	emailLinkKey := pbIndexesLinkKey(emailIdxKey, identityID)

	beforeVertex := readDoc(t, ctx, conn, emailIdxKey)
	beforeData, _ := beforeVertex["data"].(map[string]any)
	if len(beforeData) == 0 {
		t.Fatal("precondition: the identityindex vertex carries no data to preserve")
	}

	pbSealForErasure(t, ctx, conn, identityKey)
	submitPurge(t, ctx, conn, sysCP, sysCons, identityKey, "PurgeBodyDo", processor.OutcomeAccepted)

	afterVertex := readDoc(t, ctx, conn, emailIdxKey)
	if class, _ := afterVertex["class"].(string); class != "identityindex" {
		t.Errorf("tombstoned identityindex lost its class: %q", class)
	}
	afterData, _ := afterVertex["data"].(map[string]any)
	if len(afterData) != len(beforeData) {
		t.Errorf("tombstoned identityindex lost its data: %v -> %v", beforeData, afterData)
	}
	afterLink := readDoc(t, ctx, conn, emailLinkKey)
	if class, _ := afterLink["class"].(string); class != "indexes" {
		t.Errorf("tombstoned indexes link lost its class: %q", class)
	}
}

// TestPurgeIdentityDedupFootprint_WideSubject_ConvergesPastOnePage is the
// convergence proof, and the reason the enumeration pages on the read side.
//
// 300 owned index vertices is past the 256-key READ page, and that is the
// number that matters — not merely "more than one sweep". A cursor-less single
// kv.Links call — what design §10 point 4 originally specified — sweeps
// SWEEP_LIMIT live links per pass, so it keeps working while the first page
// still holds live ones. Only once the accumulated tombstones fill that page
// does it find nothing and stall at a non-zero residue forever, with the
// erasure target re-dispatching a no-op. A fixture under the page size cannot
// reach that state and would pass against the broken implementation — verified
// by mutation: at 150 links the single-page build passes this test, at 300 it
// fails.
//
// The assertion is STRICT DECREASE to zero rather than a mutation count, so
// tuning SWEEP_LIMIT does not touch this test — only an implementation that
// genuinely fails to converge does.
func TestPurgeIdentityDedupFootprint_WideSubject_ConvergesPastOnePage(t *testing.T) {
	ctx, conn := setupPurgeEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "purge-wide-default", v)
	sysCP, sysCons := newPurgePipeline(t, ctx, conn, "purge-wide-system")

	identityKey := createIdentity(t, ctx, conn, cp, cons, "PurgeWide")
	identityID := strings.TrimPrefix(identityKey, "vtx.identity.")

	// 300 DISTINCT owned identityindex vertices with their inbound indexes
	// links, at the real key shape. Every character of every id — the fixed
	// prefix and suffix included — must lie inside the canonical NanoID
	// alphabet (internal/substrate/keys/nanoid.go: no I, O, l or 0), because
	// connLinkLister SKIPS a key ParseLinkKey rejects. A fixture id carrying
	// one stray character therefore does not fail loudly; it silently
	// disappears from the enumeration, and this test would then be measuring
	// a two-link subject while claiming to measure a wide one.
	//
	// The distinctness assertion guards the other direction: a collision would
	// quietly shrink this back to the single-pass case it exists to differ
	// from.
	const (
		wantIndexes = 300
		safe        = "ABCDEFGHJKMNPQRSTUVW" // 20 chars, no I/l/O/0
	)
	seen := map[string]bool{}
	for i := 0; i < wantIndexes; i++ {
		idxID := "BBpurdx" + string(safe[i/len(safe)]) + string(safe[i%len(safe)]) + "HJKMNPQRSTU"
		seen[idxID] = true
		idxKey := "vtx.identityindex." + idxID
		seedVertex(t, ctx, conn, idxKey, "identityindex", map[string]any{"identityKey": identityKey}, false)
		testutil.SeedLink(t, ctx, conn, "lnk.identityindex."+idxID+".indexes.identity."+identityID,
			"indexes", idxKey, identityKey)
	}
	if len(seen) != wantIndexes {
		t.Fatalf("fixture is not wide: %d distinct index ids for %d vertices", len(seen), wantIndexes)
	}

	// +2 for the email and name indexes CreateUnclaimedIdentity built.
	residue := livePurgeResidue(t, ctx, conn, identityKey, "indexes", "in")
	if residue != wantIndexes+2 {
		t.Fatalf("fixture: live indexes links = %d, want %d", residue, wantIndexes+2)
	}

	pbSealForErasure(t, ctx, conn, identityKey)

	// Re-dispatch exactly as the erasure target does, requiring every pass to
	// strictly decrease the residue until it reaches zero. A stall — the
	// single-call implementation's failure mode — trips the decrease check on
	// the pass after the first rather than running to the cap.
	for pass := 1; pass <= 24; pass++ {
		// A distinct label per pass: GenReqID is deterministic in its label,
		// so a shared one would make every pass after the first a dedup
		// replay of the same requestId rather than a new sweep.
		submitPurge(t, ctx, conn, sysCP, sysCons, identityKey, "PurgeWide"+string(rune('A'+pass)), processor.OutcomeAccepted)
		next := livePurgeResidue(t, ctx, conn, identityKey, "indexes", "in")
		if next == 0 {
			// More than one pass is itself part of the contract: 152 links at
			// SWEEP_LIMIT = 64 cannot be one commit, and a build that drained
			// them all at once would satisfy the decrease check while
			// committing a batch the subject's connectivity sized — the exact
			// property §10 retires the refusal by holding.
			if pass < 2 {
				t.Fatalf("a %d-link subject was swept in %d pass(es); the mutation count must be bounded by SWEEP_LIMIT, not by the subject",
					wantIndexes+2, pass)
			}
			return
		}
		if next >= residue {
			t.Fatalf("pass %d did not decrease the residue (%d -> %d); the sweep has stalled and the erasure target would re-dispatch a no-op forever",
				pass, residue, next)
		}
		residue = next
	}
	t.Fatalf("residue did not reach zero within 24 passes (still %d)", residue)
}

// TestPurgeIdentityDedupFootprint_ForeignSourcedIndexesLink_SpareTheVertex is
// the confinement test, and it guards the one place this op could become an
// arbitrary-vertex delete primitive.
//
// The enumeration's server filter is lnk.*.*.indexes.identity.<subjectId> — the
// source TYPE is a wildcard — sourceVertex is derived faithfully from whatever
// the key says, and a document-less tombstone carries no class, so step 6 skips
// DDL resolution and never consults permittedCommands on the key being
// destroyed. The indexes linkType ships permittedCommands empty by design, so
// no platform mechanism constrains the source either. Without the key-shape
// check in sweep_indexes, a link naming a victim's identity root as its source
// makes this op tombstone that victim.
//
// The vector is planted rather than produced by a shipped op, because no
// shipped op creates a non-identityindex-sourced indexes link. That is the
// point: the safety today rests on the shape of the current corpus, not on
// anything the platform enforces, and this op holds a scope:any grant.
//
// The link itself must STILL be swept. It is genuinely the subject's inbound
// edge, removing it is what shrinks the residue, and refusing the whole sweep
// would let one planted link make a person unerasable — trading a destructive
// failure for a fail-open one.
func TestPurgeIdentityDedupFootprint_ForeignSourcedIndexesLink_SpareTheVertex(t *testing.T) {
	ctx, conn := setupPurgeEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "purge-foreign-default", v)
	sysCP, sysCons := newPurgePipeline(t, ctx, conn, "purge-foreign-system")

	identityKey := createIdentity(t, ctx, conn, cp, cons, "PurgeForeign")
	identityID := strings.TrimPrefix(identityKey, "vtx.identity.")

	// A bystander nobody is erasing, named as the source of an indexes link
	// pointing at the subject.
	const victimKey = "vtx.identity.BBpurVictimHJKMNPQRS"
	seedVertex(t, ctx, conn, victimKey, "identity", map[string]any{}, false)
	plantedLink := "lnk.identity.BBpurVictimHJKMNPQRS.indexes.identity." + identityID
	testutil.SeedLink(t, ctx, conn, plantedLink, "indexes", victimKey, identityKey)

	// The positive vector shares the corpus: a real, identityindex-sourced link
	// created by CreateUnclaimedIdentity. Without it this test would pass
	// against a build that swept nothing at all.
	realIdxKey := pbContactIndexKey("email", "shred-purgeforeign@example.com")
	realLink := pbIndexesLinkKey(realIdxKey, identityID)

	pbSealForErasure(t, ctx, conn, identityKey)
	submitPurge(t, ctx, conn, sysCP, sysCons, identityKey, "PurgeForeignDo", processor.OutcomeAccepted)

	if readIsDeleted(t, ctx, conn, victimKey) {
		t.Error("the sweep tombstoned a vertex outside the identityindex keyspace — a link naming any vertex as its source would then be an arbitrary-vertex delete, and a document-less tombstone reaches step 8 with no DDL consulted")
	}
	if !readIsDeleted(t, ctx, conn, plantedLink) {
		t.Error("the planted link survived; it is the subject's own inbound edge and the residue cannot converge while it is live")
	}
	if !readIsDeleted(t, ctx, conn, realIdxKey) {
		t.Error("the real identityindex vertex was not tombstoned — the positive vector this refusal is measured against")
	}
	if !readIsDeleted(t, ctx, conn, realLink) {
		t.Error("the real indexes link was not tombstoned")
	}
	if got := livePurgeResidue(t, ctx, conn, identityKey, "indexes", "in"); got != 0 {
		t.Errorf("live indexes residue = %d, want 0 — sparing the foreign vertex must not stall convergence", got)
	}
}

// TestPurgeIdentityDedupFootprint_EmitsAlways pins the emission the pattern's
// last step advances on, in BOTH directions — a pass that swept something and a
// pass that found nothing.
//
// The empty pass is the one that matters and the one a work-conditional
// emission would miss: a subject whose dedup footprint was always empty would
// never emit, so the pattern's fourth and final step would ride its 60s
// StepTimeout into the deadline probe on every ordinary erasure, advancing
// correctly while logging "check completionDomains" against a pattern that
// declared them correctly.
//
// No read model consumes it, which is why it is an audit record rather than a
// retraction signal, and why it names the subject: privacy.keyShredded,
// privacy.erasureRequested, identity.unbound and privacy.erasureCompleted all
// carry the same subject key, because an erasure's whole point is to be
// attestable.
func TestPurgeIdentityDedupFootprint_EmitsAlways(t *testing.T) {
	ctx, conn := setupPurgeEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "purge-ev-default", v)
	sysCP, sysCons := newPurgePipeline(t, ctx, conn, "purge-ev-system")

	// createIdentity leaves a real dedup footprint: an email and a name index
	// entry, both under one page, so a single pass drains the class.
	identityKey := createIdentity(t, ctx, conn, cp, cons, "PurgeEv")
	pbSealForErasure(t, ctx, conn, identityKey)

	if got := livePurgeResidue(t, ctx, conn, identityKey, "indexes", "in"); got == 0 {
		t.Fatal("precondition: the fixture left no dedup footprint, so pass 1 would not be a sweep with anything to announce")
	}

	// Pass 1 — a sweep with something to announce.
	reqSwept := submitPurge(t, ctx, conn, sysCP, sysCons, identityKey, "PurgeEvSwept", processor.OutcomeAccepted)
	swept := purgeSweepEvent(t, ctx, conn, reqSwept)
	if got := swept.Payload["relation"]; got != "indexes" {
		t.Errorf("relation = %v, want indexes", got)
	}
	if got, ok := swept.Payload["purged"].(float64); !ok || got == 0 {
		t.Errorf("purged = %v, want the count this pass removed", swept.Payload["purged"])
	}
	if got := swept.Payload["identityKey"]; got != identityKey {
		t.Errorf("identityKey = %v, want %s", got, identityKey)
	}
	if swept.TargetKey != identityKey {
		t.Errorf("TargetKey = %q, want the subject %s — step 7 falls back to the mutation at the same index, which here is an identityindex vertex, so an audit record would name the wrong entity",
			swept.TargetKey, identityKey)
	}

	// Pass 2 — the other class. Without this the `relation` label could be
	// hardcoded to "indexes" on both branches and every other assertion here
	// would still pass.
	if got := livePurgeResidue(t, ctx, conn, identityKey, "indexes", "in"); got != 0 {
		t.Fatalf("precondition: pass 1 left %d live indexes links", got)
	}
	const dupPeerKey = "vtx.identity.PurgeEvDupPeerHJKMNP"
	seedVertex(t, ctx, conn, dupPeerKey, "identity", map[string]any{}, false)
	testutil.SeedLink(t, ctx, conn,
		pbDuplicateOfLinkKey(strings.TrimPrefix(identityKey, "vtx.identity."), dupPeerKey),
		"duplicateOf", identityKey, dupPeerKey)

	dup := purgeSweepEvent(t, ctx, conn,
		submitPurge(t, ctx, conn, sysCP, sysCons, identityKey, "PurgeEvDup", processor.OutcomeAccepted))
	if got := dup.Payload["relation"]; got != "duplicateOf" {
		t.Errorf("relation = %v, want duplicateOf — the label must name the class this pass actually swept, not the first one it tried", got)
	}

	// Pass 3 — nothing left on any class. The convergence signal, and the case
	// a work-conditional emission drops on the floor.
	reqEmpty := submitPurge(t, ctx, conn, sysCP, sysCons, identityKey, "PurgeEvEmpty", processor.OutcomeAccepted)
	empty := purgeSweepEvent(t, ctx, conn, reqEmpty)
	if got := empty.Payload["relation"]; got != "" {
		t.Errorf("relation = %v, want empty — no class had a live link left", got)
	}
	if got := empty.Payload["purged"]; got != float64(0) {
		t.Errorf("purged = %v, want 0", got)
	}
	if empty.TargetKey != identityKey {
		t.Errorf("TargetKey = %q, want the subject %s — with no mutation to fall back on, an unset target would be empty", empty.TargetKey, identityKey)
	}
}

// purgeSweepEvent reads the single privacy.dedupFootprintSwept event a purge
// commit emitted, off its outbox aspect. Fails the test if the commit emitted
// no event at all — the failure mode the pattern's last step cannot survive.
func purgeSweepEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, reqID string) processor.Event {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.OutboxAspectKey(reqID))
	if err != nil {
		t.Fatalf("KVGet outbox aspect for %s: %v — the sweep emitted no event, so the identityErasure pattern's last step can only advance by deadline", reqID, err)
	}
	aspect, err := processor.ParseOutboxAspect(entry.Value)
	if err != nil {
		t.Fatalf("parse outbox aspect: %v", err)
	}
	if len(aspect.Data.Events) != 1 {
		t.Fatalf("the sweep emitted %d event(s), want exactly 1: %+v", len(aspect.Data.Events), aspect.Data.Events)
	}
	ev := aspect.Data.Events[0]
	if ev.EventType != "privacy.dedupFootprintSwept" {
		t.Fatalf("event type = %q, want privacy.dedupFootprintSwept", ev.EventType)
	}
	return ev
}

// TestPurgeIdentityDedupFootprint_AbsentOrTombstonedSubject_Rejected — the
// target-existence guard, in both of its forms. The Weaver dispatches this op
// off a lens row, and a row can outlive the vertex it names, so "the subject is
// gone" is a state this op really sees rather than a theoretical one.
func TestPurgeIdentityDedupFootprint_AbsentOrTombstonedSubject_Rejected(t *testing.T) {
	ctx, conn := setupPurgeEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "purge-absent-default", v)
	sysCP, sysCons := newPurgePipeline(t, ctx, conn, "purge-absent-system")

	const absentKey = "vtx.identity.BBpurAbsentHJKMNPQR"
	pbSealForErasure(t, ctx, conn, absentKey)
	submitPurge(t, ctx, conn, sysCP, sysCons, absentKey, "PurgeAbsent", processor.OutcomeRejected)

	const goneKey = "vtx.identity.BBpurGoneHJKMNPQRST"
	seedVertex(t, ctx, conn, goneKey, "identity", map[string]any{}, true)
	pbSealForErasure(t, ctx, conn, goneKey)
	submitPurge(t, ctx, conn, sysCP, sysCons, goneKey, "PurgeGone", processor.OutcomeRejected)

	// The positive vector over the same corpus: a live subject, same envelope,
	// accepted — so neither rejection above is passing on the marker guard or
	// on authorization instead.
	liveKey := createIdentity(t, ctx, conn, cp, cons, "PurgeLive")
	pbSealForErasure(t, ctx, conn, liveKey)
	submitPurge(t, ctx, conn, sysCP, sysCons, liveKey, "PurgeLiveDo", processor.OutcomeAccepted)
}

// TestPurgeIdentityDedupFootprint_DuplicateOfOnly_ConvergesPastOnePage is the
// convergence proof for the OTHER class.
//
// The indexes sweep and the duplicateOf sweep share a collector but not a
// mutation shape, and the duplicateOf arm is reached only after the indexes
// class is exhausted — so a paging defect confined to the second arm would be
// invisible to the indexes convergence test. 300 links again, past the 256-key
// read page, for the same reason: a fixture inside one page cannot reach the
// state where the accumulated tombstones fill it.
func TestPurgeIdentityDedupFootprint_DuplicateOfOnly_ConvergesPastOnePage(t *testing.T) {
	ctx, conn := setupPurgeEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "purge-dupwide-default", v)
	sysCP, sysCons := newPurgePipeline(t, ctx, conn, "purge-dupwide-system")

	subjectKey := createIdentity(t, ctx, conn, cp, cons, "DupWideIdent")
	subjectID := strings.TrimPrefix(subjectKey, "vtx.identity.")

	const (
		wantDups = 300
		safe     = "ABCDEFGHJKMNPQRSTUVW" // 20 chars, every one inside the NanoID alphabet
	)
	seen := map[string]bool{}
	for i := 0; i < wantDups; i++ {
		otherID := "BBpurDp" + string(safe[i/len(safe)]) + string(safe[i%len(safe)]) + "HJKMNPQRSTU"
		seen[otherID] = true
		// Outbound: the subject is the later-arriving side of the pair.
		testutil.SeedLink(t, ctx, conn, "lnk.identity."+subjectID+".duplicateOf.identity."+otherID,
			"duplicateOf", subjectKey, "vtx.identity."+otherID)
	}
	if len(seen) != wantDups {
		t.Fatalf("fixture is not wide: %d distinct counterparties for %d links", len(seen), wantDups)
	}

	pbSealForErasure(t, ctx, conn, subjectKey)

	// Pass 1 spends itself on the indexes class CreateUnclaimedIdentity built;
	// every pass after it is the duplicateOf arm.
	//
	// Labels must differ in a character GenReqID KEEPS: it substitutes an
	// out-of-alphabet byte with the padding char for that position, so a label
	// ending in a digit can collide with the shorter label it extends.
	submitPurge(t, ctx, conn, sysCP, sysCons, subjectKey, "DupWideSeeded", processor.OutcomeAccepted)
	residue := livePurgeResidue(t, ctx, conn, subjectKey, "duplicateOf", "out")
	if residue != wantDups {
		t.Fatalf("the indexes pass must not touch duplicateOf: live outbound duplicateOf = %d, want %d", residue, wantDups)
	}

	for pass := 1; pass <= 24; pass++ {
		submitPurge(t, ctx, conn, sysCP, sysCons, subjectKey, "DupWidePass"+string(safe[pass]), processor.OutcomeAccepted)
		next := livePurgeResidue(t, ctx, conn, subjectKey, "duplicateOf", "out")
		if next == 0 {
			if pass < 2 {
				t.Fatalf("a %d-link subject was swept in %d pass(es); the mutation count must be bounded by SWEEP_LIMIT, not by the subject", wantDups, pass)
			}
			return
		}
		if next >= residue {
			t.Fatalf("pass %d did not decrease the duplicateOf residue (%d -> %d); the sweep has stalled", pass, residue, next)
		}
		residue = next
	}
	t.Fatalf("duplicateOf residue did not reach zero within 24 passes (still %d)", residue)
}

// TestPurgeDeclaredReadSetMatchesThePatternStep is design §13's coverage guard,
// which had nothing to compare against until the pattern landed.
//
// Every test in this file submits the op with a hand-written ContextHint. That
// literal is a claim about what the real dispatcher declares, and a claim
// nothing checked: the fixture could keep declaring a key the pattern dropped
// and every assertion here would go on passing against a read-set production
// never sends. Rendering the step's subject-relative templates against a
// concrete subject is what makes the two comparable at all.
func TestPurgeDeclaredReadSetMatchesThePatternStep(t *testing.T) {
	const subjectKey = "vtx.identity.PurgeReadSetPin12345"

	var step pkgmgr.StepSpec
	for _, s := range privacybase.LoomPatterns()[0].Steps {
		if s.Operation == "PurgeIdentityDedupFootprint" {
			step = s
		}
	}
	if step.Operation == "" {
		t.Fatal("the identityErasure pattern no longer binds PurgeIdentityDedupFootprint — this op's only orchestrated dispatcher is gone")
	}

	render := func(tmpl []string) []string {
		out := make([]string, 0, len(tmpl))
		for _, e := range tmpl {
			out = append(out, subjectKey+strings.TrimPrefix(e, "subject"))
		}
		return out
	}
	wantReads := purgeFixtureReads(subjectKey)
	wantOptional := purgeFixtureOptionalReads(subjectKey)

	if got := render(step.Reads); !slices.Equal(got, wantReads) {
		t.Errorf("the pattern step's rendered Reads = %v, but every test here submits %v — the fixture is proving a read-set the dispatcher does not send", got, wantReads)
	}
	if got := render(step.OptionalReads); !slices.Equal(got, wantOptional) {
		t.Errorf("the pattern step's rendered OptionalReads = %v, but every test here submits %v", got, wantOptional)
	}
}
