// UnbindIdentityCredentials (erasure-orchestration-design.md §5.4) — the
// erasure plane's credential sweep, driven through the real Processor commit
// path so what these prove is what production does.
//
// Two of these carry more weight than the rest:
//
//   - TestUnbindIdentityCredentials_WideSubject_ConvergesPastOnePage is the
//     convergence proof. Design §10 point 4 argued one cursor-less kv.Links
//     call per commit would suffice because "the cursor lives in the world
//     (the remaining live links), not in the script". It does not: a tombstone
//     is a SOFT delete, the key stays in the keyspace, and kv.Links keeps
//     returning it with isDeleted set. A subject past one page would therefore
//     stall on a first page of pure tombstones, forever and silently, while
//     the erasure target re-dispatched a no-op. This test is what makes that
//     claim executable rather than assumed.
//   - TestUnbindIdentityCredentials_InboundSweep_LeavesSubjectAspectUntouched
//     pins the other correction: the subject's own credentialBinding array is
//     NOT rewritten. It is sensitive, and the subject's DEK is shredded by the
//     time this op runs, so a rewrite would fault in hydrate on every
//     redelivery and leave the person unerasable. The array is already erased
//     by key destruction; identity.unbound is what shrinks the readable copy.
package identitydomain_test

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

const (
	unbindActorID  = "BBunbindSweepHJKMNPQ"
	unbindActorKey = "vtx.identity." + unbindActorID

	unbindUngrantedActorID  = "BBunbindNoGrantHJKMN"
	unbindUngrantedActorKey = "vtx.identity." + unbindUngrantedActorID
)

// unbindCapDoc grants UnbindIdentityCredentials on the system lane — the shape
// the Loom service actor carries in production, submitting the identityErasure
// pattern's third step as an operator-equivalent actor.
func unbindCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    "cap.identity." + unbindActorID,
		Actor:                  unbindActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{unbindActorKey: 1},
		Lanes:                  []string{"system"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "UnbindIdentityCredentials", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// unbindCapDocMissingGrant is unbindCapDoc's control: the same system lane and
// the same operator role, granting a different identity-domain op. Without it
// a denial test would be satisfied by the lane check alone and would stay
// green if this op's grant were deleted outright.
func unbindCapDocMissingGrant() *processor.CapabilityDoc {
	doc := unbindCapDoc()
	doc.Key = "cap.identity." + unbindUngrantedActorID
	doc.Actor = unbindUngrantedActorKey
	doc.ProjectedFromRevisions = map[string]uint64{unbindUngrantedActorKey: 1}
	doc.PlatformPermissions = []processor.PlatformPermission{
		{OperationType: "ReconcileCredentialBinding", Scope: "any"},
	}
	return doc
}

func setupUnbindEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := setupTestEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, unbindCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, unbindCapDocMissingGrant())
	return ctx, conn
}

func newUnbindPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:        durable,
		Instance:       "unbind-" + durable,
		FilterSubjects: []string{"ops.system"},
	})
}

// submitUnbindAs publishes one UnbindIdentityCredentials and drives it to
// wantOutcome. The declared read-set is what the identityErasure pattern's
// step will declare through StepSpec.Reads/OptionalReads; until that pattern
// lands this literal is the only thing pinning it (design §13's coverage guard
// has nothing to compare against yet).
//
// The subject's own credentialBinding is deliberately absent from the set and
// must stay absent: it is sensitive, and by the time this op runs the
// subject's DEK is shredded, so declaring it would fault the op in hydrate.
func submitUnbindAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, actorKey, subjectKey, reqLabel string, wantOutcome processor.MessageOutcome) string {
	t.Helper()
	reqID := testutil.GenReqID(reqLabel)
	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneSystem,
		OperationType: "UnbindIdentityCredentials",
		Actor:         actorKey,
		SubmittedAt:   "2026-08-07T12:00:00Z",
		Class:         "unbindIdentityCredentials",
		Payload:       json.RawMessage(`{"subjectKey":"` + subjectKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{subjectKey},
			OptionalReads: []string{subjectKey + ".erasureRequested"},
			Enumerations: []processor.EnumerationHint{
				{Hub: subjectKey, Relation: "boundTo", Direction: "in"},
				{Hub: subjectKey, Relation: "boundTo", Direction: "out"},
			},
		},
	})
	testutil.DriveOne(t, ctx, cp, cons, wantOutcome)
	return reqID
}

func submitUnbind(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, subjectKey, reqLabel string, wantOutcome processor.MessageOutcome) string {
	t.Helper()
	return submitUnbindAs(t, ctx, conn, cp, cons, unbindActorKey, subjectKey, reqLabel, wantOutcome)
}

// liveBoundToCount counts the subject's non-tombstoned boundTo links in one
// direction, reading Core KV the same way the script's enumeration does. It
// pages past the link-list limit so a count taken over a wide subject is the
// real one rather than a first page.
func liveBoundToCount(t *testing.T, ctx context.Context, conn *substrate.Conn, subjectKey, direction string) int {
	t.Helper()
	_, id, ok := substrate.ParseVertexKey(subjectKey)
	if !ok {
		t.Fatalf("ParseVertexKey %s", subjectKey)
	}
	filter := "lnk.*.*.boundTo.identity." + id
	if direction == "out" {
		filter = "lnk.identity." + id + ".boundTo.>"
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
	t.Fatalf("liveBoundToCount %s: did not terminate within 200 pages", filter)
	return 0
}

// assertBoundToLive is assertTombstoned's opposite, for a link the sweep must
// leave alone.
func assertBoundToLive(t *testing.T, ctx context.Context, conn *substrate.Conn, key, why string) {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v — %s", key, err, why)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	if deleted, _ := doc["isDeleted"].(bool); deleted {
		t.Fatalf("%s is tombstoned — %s", key, why)
	}
}

func assertTombstoned(t *testing.T, ctx context.Context, conn *substrate.Conn, key, why string) {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v — %s", key, err, why)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	if deleted, _ := doc["isDeleted"].(bool); !deleted {
		t.Fatalf("%s is still live — %s", key, why)
	}
}

// TestUnbindIdentityCredentials_InboundSweep_LeavesSubjectAspectUntouched is
// the shape of the whole op on the ordinary path, and it pins the one place
// this build knowingly departs from §5.4's "copies UnlinkCredential's body".
//
// Both of the subject's credentials go — including the last one, where
// UnlinkCredential refuses. A person being erased keeps no sign-in path; that
// is the point, and it is why this verb is granted only to the service actors
// and refuses an unsealed subject.
func TestUnbindIdentityCredentials_InboundSweep_LeavesSubjectAspectUntouched(t *testing.T) {
	t.Parallel()
	ctx, conn := setupUnbindEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "unbind-in-default")
	sysCP, sysCons := newUnbindPipeline(t, ctx, conn, "unbind-in-system")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "UnbInSw")
	linkSecondCredential(t, ctx, conn, cp, cons, uKey, secondCredActorKey, "UnbInSwLink", "link-secret-unb-in")

	// Someone else's credential bound to someone else — a boundTo touching
	// neither end of the subject. This op holds a scope:any grant and issues
	// document-less tombstones, so nothing downstream of the script re-checks
	// what it names: the enumeration's key filter is the whole confinement, and
	// a filter widened by one segment would take this link with the subject's.
	const bystanderCred = "vtx.identity.BBunbByCredHJKMNPQRS"
	const bystanderOwner = "vtx.identity.BBunbByHubbHJKMNPQRS"
	bystanderLink := "lnk.identity.BBunbByCredHJKMNPQRS.boundTo.identity.BBunbByHubbHJKMNPQRS"
	testutil.SeedLink(t, ctx, conn, bystanderLink, "boundTo", bystanderCred, bystanderOwner)

	// The array carries both credentials before the sweep. Reading it here
	// also proves the subject's DEK is alive at fixture time, so the
	// unreadability asserted after the seal is a property of the erasure
	// rather than of the fixture.
	before := readDecryptedAspectData(t, ctx, conn, uKey, "credentialBinding")
	if creds, _ := before["credentials"].([]interface{}); len(creds) != 2 {
		t.Fatalf("fixture: credentials len = %d, want 2", len(creds))
	}
	beforeEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, uKey+".credentialBinding")
	if err != nil {
		t.Fatalf("KVGet credentialBinding: %v", err)
	}

	sealForErasure(t, ctx, conn, uKey)
	reqID := submitUnbind(t, ctx, conn, sysCP, sysCons, uKey, "UnbInSwDo", processor.OutcomeAccepted)

	for _, cred := range []string{consumerActorKey, secondCredActorKey} {
		assertTombstoned(t, ctx, conn, credentialIndexKey(cred),
			"every credential of an erased person must stop resolving, including the last one")
	}
	if got := liveBoundToCount(t, ctx, conn, uKey, "in"); got != 0 {
		t.Fatalf("live inbound boundTo links = %d, want 0", got)
	}
	assertBoundToLive(t, ctx, conn, bystanderLink,
		"a boundTo between two other people was tombstoned; this sweep is confined to the subject by its key filter alone")
	assertTrackerEvent(t, ctx, conn, reqID, "identity.unbound")

	// The load-bearing negative: the subject's own credentialBinding is
	// untouched. Its revision must be unchanged — a rewrite would need the
	// subject's DEK, which the seal's own precondition has already destroyed.
	afterEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, uKey+".credentialBinding")
	if err != nil {
		t.Fatalf("KVGet credentialBinding after sweep: %v", err)
	}
	if afterEntry.Revision != beforeEntry.Revision {
		t.Fatalf("subject's credentialBinding was rewritten (revision %d -> %d); it is sensitive and the "+
			"subject's DEK is shredded by the time this op runs, so a rewrite faults in hydrate on every redelivery",
			beforeEntry.Revision, afterEntry.Revision)
	}
}

// TestUnbindIdentityCredentials_UnsealedSubject_Rejected pairs the refusal
// with its own positive vector over one corpus: the identical envelope against
// the identical subject, with only the marker changing. Without the accepted
// half, a rejection assertion would be satisfied by any of the op's other
// guards.
func TestUnbindIdentityCredentials_UnsealedSubject_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupUnbindEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "unbind-seal-default")
	sysCP, sysCons := newUnbindPipeline(t, ctx, conn, "unbind-seal-system")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "UnbSeal")

	// Unsealed: refused, and the credential survives.
	submitUnbind(t, ctx, conn, sysCP, sysCons, uKey, "UnbSealNo", processor.OutcomeRejected)
	if got := liveBoundToCount(t, ctx, conn, uKey, "in"); got != 1 {
		t.Fatalf("an unsealed subject must keep its credential: live inbound links = %d, want 1", got)
	}
	assertKeyPresent(t, ctx, conn, credentialIndexKey(consumerActorKey),
		"a refused sweep must not have tombstoned anything")

	// Change exactly one fact.
	sealForErasure(t, ctx, conn, uKey)
	submitUnbind(t, ctx, conn, sysCP, sysCons, uKey, "UnbSealYes", processor.OutcomeAccepted)
	if got := liveBoundToCount(t, ctx, conn, uKey, "in"); got != 0 {
		t.Fatalf("live inbound links after sealing = %d, want 0", got)
	}
}

// TestUnbindIdentityCredentials_ForeignMarkerClass_Rejected — the marker's
// CLASS is what arms this verb, not its key. privacy-base's aspect-type DDL
// gates the class rather than the key, so any package script can write some
// other class there; a presence-only check would let such a write hand a
// service actor the right to strip a live person's sign-in methods.
func TestUnbindIdentityCredentials_ForeignMarkerClass_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupUnbindEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "unbind-class-default")
	sysCP, sysCons := newUnbindPipeline(t, ctx, conn, "unbind-class-system")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "UnbCls")

	sealForErasureAs(t, ctx, conn, uKey, "someOtherClass", false)
	submitUnbind(t, ctx, conn, sysCP, sysCons, uKey, "UnbClsNo", processor.OutcomeRejected)
	if got := liveBoundToCount(t, ctx, conn, uKey, "in"); got != 1 {
		t.Fatalf("a foreign-class marker must not arm the sweep: live inbound links = %d, want 1", got)
	}

	// The positive vector: the same key, the real class.
	sealForErasureAs(t, ctx, conn, uKey, "erasureRequested", false)
	submitUnbind(t, ctx, conn, sysCP, sysCons, uKey, "UnbClsYes", processor.OutcomeAccepted)
	if got := liveBoundToCount(t, ctx, conn, uKey, "in"); got != 0 {
		t.Fatalf("live inbound links after a real marker = %d, want 0", got)
	}
}

// TestUnbindIdentityCredentials_UngrantedActor_Denied proves the grant is what
// authorizes this, not the lane: the control actor holds the same operator
// role on the same system lane, granting a different identity-domain op.
func TestUnbindIdentityCredentials_UngrantedActor_Denied(t *testing.T) {
	t.Parallel()
	ctx, conn := setupUnbindEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "unbind-auth-default")
	sysCP, sysCons := newUnbindPipeline(t, ctx, conn, "unbind-auth-system")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "UnbAuth")
	sealForErasure(t, ctx, conn, uKey)

	submitUnbindAs(t, ctx, conn, sysCP, sysCons, unbindUngrantedActorKey, uKey, "UnbAuthNo", processor.OutcomeRejected)
	if got := liveBoundToCount(t, ctx, conn, uKey, "in"); got != 1 {
		t.Fatalf("a denied sweep must change nothing: live inbound links = %d, want 1", got)
	}
	submitUnbind(t, ctx, conn, sysCP, sysCons, uKey, "UnbAuthYes", processor.OutcomeAccepted)
}

// TestUnbindIdentityCredentials_SecondRunIsNoOp — idempotence by tombstone.
// The pattern's step runs once but the erasure target re-dispatches this op
// every reconcile pass until the residue reaches zero, so a swept subject is
// the input it sees most often. A second run must commit nothing and emit
// nothing rather than re-tombstoning what is already gone.
func TestUnbindIdentityCredentials_SecondRunIsNoOp(t *testing.T) {
	t.Parallel()
	ctx, conn := setupUnbindEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "unbind-idem-default")
	sysCP, sysCons := newUnbindPipeline(t, ctx, conn, "unbind-idem-system")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "UnbIdem")
	linkSecondCredential(t, ctx, conn, cp, cons, uKey, secondCredActorKey, "UnbIdemLink", "link-secret-unb-idem")
	sealForErasure(t, ctx, conn, uKey)

	submitUnbind(t, ctx, conn, sysCP, sysCons, uKey, "UnbIdem1", processor.OutcomeAccepted)
	firstEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credentialIndexKey(secondCredActorKey))
	if err != nil {
		t.Fatalf("KVGet credentialindex: %v", err)
	}

	reqID := submitUnbind(t, ctx, conn, sysCP, sysCons, uKey, "UnbIdem2", processor.OutcomeAccepted)
	assertTrackerNotEvent(t, ctx, conn, reqID, "identity.unbound")

	secondEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credentialIndexKey(secondCredActorKey))
	if err != nil {
		t.Fatalf("KVGet credentialindex after re-run: %v", err)
	}
	if secondEntry.Revision != firstEntry.Revision {
		t.Fatalf("re-run rewrote an already-tombstoned credentialindex (revision %d -> %d); "+
			"the sweep must skip what it already swept", firstEntry.Revision, secondEntry.Revision)
	}
}

// TestUnbindIdentityCredentials_WideSubject_ConvergesPastOnePage is the
// convergence proof, and the reason the enumeration pages on the read side.
//
// 300 credentials is one full page plus a remainder. A cursor-less single
// kv.Links call — what design §10 point 4 specified — would sweep the first
// 256 and then find a first page of nothing but tombstones on every later
// pass, because a tombstone is a soft delete and the key stays in the
// keyspace. The subject would sit at 44 live credentials forever with the
// erasure target re-dispatching a no-op. The assertion that pass 2 reaches
// zero is what that implementation cannot satisfy.
func TestUnbindIdentityCredentials_WideSubject_ConvergesPastOnePage(t *testing.T) {
	t.Parallel()
	ctx, conn := setupUnbindEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "unbind-wide-default")
	sysCP, sysCons := newUnbindPipeline(t, ctx, conn, "unbind-wide-system")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "UnbWide")
	uID := uKey[len("vtx.identity."):]

	// 300 DISTINCT inbound boundTo links, past the 256-link page limit. The
	// ids are built from a restricted alphabet rather than via GenReqID, which
	// silently drops characters outside the NanoID alphabet and would collide;
	// the distinctness assertion is what keeps a collision from quietly
	// shrinking this back to the single-page case it exists to differ from.
	const (
		wantLinks = 300
		safe      = "ABCDEFGHJKMNPQRSTUVW" // 20 chars, no I/l/O/0
	)
	seen := map[string]bool{}
	for i := 0; i < wantLinks; i++ {
		credID := "BBunbCr" + string(safe[i/len(safe)]) + string(safe[i%len(safe)]) + "HJKMNPQRSTU"
		seen[credID] = true
		testutil.SeedLink(t, ctx, conn, "lnk.identity."+credID+".boundTo.identity."+uID,
			"boundTo", "vtx.identity."+credID, uKey)
	}
	if len(seen) != wantLinks {
		t.Fatalf("fixture is not wide: %d distinct credential ids for %d links", len(seen), wantLinks)
	}
	// +1 for the credential the claim bound.
	if got := liveBoundToCount(t, ctx, conn, uKey, "in"); got != wantLinks+1 {
		t.Fatalf("fixture: live inbound links = %d, want %d", got, wantLinks+1)
	}

	sealForErasure(t, ctx, conn, uKey)

	// Convergence, asserted as convergence rather than as arithmetic: every
	// pass must strictly decrease the residue, and the residue must reach zero
	// within a bound derived from the sweep size. A stalled sweep fails on the
	// strict-decrease check at the pass where the tombstones first fill a read
	// page, which is precisely the failure a cursor-less single call produces.
	//
	// The pass cap is generous on purpose: what is being pinned is termination,
	// not a particular sweep size, so tuning SWEEP_LIMIT must not have to touch
	// this test.
	remaining := wantLinks + 1
	for pass := 1; pass <= 40; pass++ {
		submitUnbind(t, ctx, conn, sysCP, sysCons, uKey, "UnbWideP"+string(safe[pass]), processor.OutcomeAccepted)
		got := liveBoundToCount(t, ctx, conn, uKey, "in")
		if got >= remaining {
			t.Fatalf("pass %d left %d live inbound links, down from %d — a pass that retires nothing is the "+
				"tombstone stall this paged read exists to prevent, and the erasure target would re-dispatch "+
				"it forever", pass, got, remaining)
		}
		remaining = got
		if remaining == 0 {
			return
		}
	}
	t.Fatalf("still %d live inbound links after 40 passes — the sweep is decreasing but not converging", remaining)
}

// TestUnbindIdentityCredentials_OutboundSweep_RewritesOwnerArray covers the
// direction §5.4's "takes the owner from subjectKey" reads past: the subject is
// itself someone else's credential. ShredIdentityKey does not touch boundTo
// links at all, so this direction — like the inbound one — is retired only
// here; an inbound-only sweep would leave it permanently unerasable.
//
// Here the array rewrite IS both possible and load-bearing — the owner is not
// being erased, their key is alive, and their credentials array names the
// erased person in the clear.
func TestUnbindIdentityCredentials_OutboundSweep_RewritesOwnerArray(t *testing.T) {
	t.Parallel()
	ctx, conn := setupUnbindEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "unbind-out-default")
	sysCP, sysCons := newUnbindPipeline(t, ctx, conn, "unbind-out-system")

	// W is an ordinary claimed identity; the subject is a second identity
	// linked as W's SECOND credential, which is what makes the subject the
	// SOURCE of a boundTo link and puts it in W's credentials array.
	//
	// The subject is created rather than claimed: ClaimIdentity dedups on the
	// claiming actor's credentialindex, so claiming twice from this harness's
	// one consumer actor refuses — and being claimed is not what makes an
	// identity somebody else's credential anyway.
	wKey := claimFreshIdentity(t, ctx, conn, cp, cons, "UnbOutW")
	subjectKey, _ := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("UnbOutS"))
	seedIdentityCapDoc(t, ctx, conn, subjectKey, "CompleteCredentialLink")
	linkSecondCredential(t, ctx, conn, cp, cons, wKey, subjectKey, "UnbOutLink", "link-secret-unb-out")

	if got := liveBoundToCount(t, ctx, conn, subjectKey, "out"); got != 1 {
		t.Fatalf("fixture: subject's outbound links = %d, want 1", got)
	}

	sealForErasure(t, ctx, conn, subjectKey)
	reqID := submitUnbind(t, ctx, conn, sysCP, sysCons, subjectKey, "UnbOutDo", processor.OutcomeAccepted)

	if got := liveBoundToCount(t, ctx, conn, subjectKey, "out"); got != 0 {
		t.Fatalf("live outbound links = %d, want 0", got)
	}
	assertTombstoned(t, ctx, conn, credentialIndexKey(subjectKey),
		"an erased person's own credentialindex must stop resolving when they are someone else's credential")
	assertTrackerEvent(t, ctx, conn, reqID, "identity.unbound")

	// W's array must no longer name the subject, and W's own first credential
	// must survive: this sweep erases one person, not their relationships'
	// owners.
	after := readDecryptedAspectData(t, ctx, conn, wKey, "credentialBinding")
	creds, _ := after["credentials"].([]interface{})
	if len(creds) != 1 {
		t.Fatalf("W's credentials len = %d, want 1: %+v", len(creds), creds)
	}
	m, _ := creds[0].(map[string]interface{})
	if got, _ := m["actorKey"].(string); got != consumerActorKey {
		t.Fatalf("W's remaining credential = %q, want %q", got, consumerActorKey)
	}
	if got, _ := after["actorKey"].(string); got == subjectKey {
		t.Fatalf("W's singular actorKey still names the erased subject %q — the pre-array fallback readers "+
			"use must not keep pointing at a credential that no longer resolves", subjectKey)
	}
}

// TestUnbindIdentityCredentials_EmitsSweepPassEventOnEveryCommit pins the
// pass-level emission in both directions — a pass that unbound something and a
// pass that found nothing.
//
// The per-credential identity.unbound events cannot serve this purpose and are
// not meant to: they are the Gateway's retraction signal, so a pass with no hits
// correctly emits none. But the identityErasure pattern's third step is
// guardless and runs for every subject, including one a later re-dispatch
// finds already fully swept. Without a pass-level event that step rides its
// 60s deadline into the op-status probe on every erasure, advancing while
// logging a completionDomains warning against a pattern that declared them
// correctly.
func TestUnbindIdentityCredentials_EmitsSweepPassEventOnEveryCommit(t *testing.T) {
	t.Parallel()
	ctx, conn := setupUnbindEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "unbind-pass-default")
	sysCP, sysCons := newUnbindPipeline(t, ctx, conn, "unbind-pass-system")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "UnbPass")
	linkSecondCredential(t, ctx, conn, cp, cons, uKey, secondCredActorKey, "UnbPassLink", "link-secret-unb-pass")
	sealForErasure(t, ctx, conn, uKey)

	// Pass 1 — a sweep with credentials to unbind.
	swept := unbindSweepEvent(t, ctx, conn,
		submitUnbind(t, ctx, conn, sysCP, sysCons, uKey, "UnbPass1", processor.OutcomeAccepted))
	if got := swept.Payload["direction"]; got != "in" {
		t.Errorf("direction = %v, want in", got)
	}
	if got, ok := swept.Payload["swept"].(float64); !ok || got == 0 {
		t.Errorf("swept = %v, want the count this pass unbound", swept.Payload["swept"])
	}
	if swept.TargetKey != uKey {
		t.Errorf("TargetKey = %q, want the subject %s", swept.TargetKey, uKey)
	}

	// Pass 2 — nothing left. The case a per-credential emission drops, and the
	// one the pattern's guardless step hits on every ordinary erasure.
	empty := unbindSweepEvent(t, ctx, conn,
		submitUnbind(t, ctx, conn, sysCP, sysCons, uKey, "UnbPass2", processor.OutcomeAccepted))
	if got := empty.Payload["direction"]; got != "" {
		t.Errorf("direction = %v, want empty — neither direction had a live link left", got)
	}
	if got := empty.Payload["swept"]; got != float64(0) {
		t.Errorf("swept = %v, want 0", got)
	}
	if empty.TargetKey != uKey {
		t.Errorf("TargetKey = %q, want the subject %s — with no mutation to fall back on, an unset target would be empty", empty.TargetKey, uKey)
	}
}

// unbindSweepEvent reads the one identity.credentialsSwept event a sweep commit
// emitted. Fails if the commit emitted none — the failure mode the pattern's
// third step cannot survive.
func unbindSweepEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, reqID string) processor.Event {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.OutboxAspectKey(reqID))
	if err != nil {
		t.Fatalf("KVGet outbox aspect for %s: %v — the sweep emitted nothing, so the identityErasure pattern's third step can only advance by deadline", reqID, err)
	}
	aspect, err := processor.ParseOutboxAspect(entry.Value)
	if err != nil {
		t.Fatalf("parse outbox aspect: %v", err)
	}
	var found []processor.Event
	for _, ev := range aspect.Data.Events {
		if ev.EventType == "identity.credentialsSwept" {
			found = append(found, ev)
		}
	}
	if len(found) != 1 {
		t.Fatalf("got %d identity.credentialsSwept event(s), want exactly 1: %+v", len(found), aspect.Data.Events)
	}
	return found[0]
}
