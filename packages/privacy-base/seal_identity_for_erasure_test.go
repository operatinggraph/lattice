package privacybase_test

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

// These tests drive the real installed DDL through the real Processor commit
// path (auth → hydrate → execute → validate → commit), so what they prove is
// what production does, not what a unit stub agrees to.

const (
	pbSealActorID  = "BBseaErasureHJKMNPQR"
	pbSealActorKey = "vtx.identity." + pbSealActorID
	pbSealCapKey   = "cap.identity." + pbSealActorID

	pbUngrantedActorID  = "BBseaNoGrantHJKMNPQR"
	pbUngrantedActorKey = "vtx.identity." + pbUngrantedActorID
)

// sealCapDoc grants SealIdentityForErasure on the system lane — the grant
// shape the Loom service actor carries in production (operator-equivalent via
// holdsRole, submitting the identityErasure pattern's second step).
func sealCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    pbSealCapKey,
		Actor:                  pbSealActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{pbSealActorKey: 1},
		Lanes:                  []string{"system"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "SealIdentityForErasure", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// sealCapDocMissingGrant is sealCapDoc's control: the SAME system lane, the
// same operator role, a privacy-base grant — everything except
// SealIdentityForErasure. Without it, a denial test would be satisfied by the
// lane check alone and would keep passing if the op's grant were deleted.
func sealCapDocMissingGrant() *processor.CapabilityDoc {
	doc := sealCapDoc()
	doc.Key = "cap.identity." + pbUngrantedActorID
	doc.Actor = pbUngrantedActorKey
	doc.ProjectedFromRevisions = map[string]uint64{pbUngrantedActorKey: 1}
	doc.PlatformPermissions = []processor.PlatformPermission{
		{OperationType: "RecordShredFinalization", Scope: "any"},
	}
	return doc
}

func setupSealEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, staffCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, sealCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, sealCapDocMissingGrant())
	return ctx, conn
}

// submitSeal publishes one SealIdentityForErasure and drives it to
// wantOutcome, returning the requestId so the caller can inspect the outbox.
//
// The declared read-set is what the identityErasure pattern's step will
// declare via StepSpec.Reads/OptionalReads: the identity root required (the
// target-existence guard), the three aspects absence-tolerant. Until the
// pattern lands, this literal is the only thing pinning that set — the §13
// coverage guard that would tie it to what the DDL actually reads cannot be
// written before a pattern declares it.
func submitSeal(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, identityKey, reqLabel string, wantOutcome processor.MessageOutcome) string {
	t.Helper()
	return submitSealAt(t, ctx, conn, cp, cons, pbSealActorKey, identityKey, reqLabel, "2026-08-07T11:00:00Z", wantOutcome)
}

func submitSealAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, actorKey, identityKey, reqLabel string, wantOutcome processor.MessageOutcome) string {
	t.Helper()
	return submitSealAt(t, ctx, conn, cp, cons, actorKey, identityKey, reqLabel, "2026-08-07T11:00:00Z", wantOutcome)
}

// submitSealAt takes the submittedAt explicitly. A shared helper that pins one
// instant would make the re-seal test's requestedAt-preservation assertion
// vacuous: both seals would stamp the same value, so deleting the preservation
// branch from the script would leave the test green.
func submitSealAt(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, actorKey, identityKey, reqLabel, submittedAt string, wantOutcome processor.MessageOutcome) string {
	t.Helper()
	reqID := testutil.GenReqID(reqLabel)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneSystem,
		OperationType: "SealIdentityForErasure",
		Actor:         actorKey,
		SubmittedAt:   submittedAt,
		Class:         "sealIdentityForErasure",
		Payload:       json.RawMessage(`{"subjectKey":"` + identityKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{identityKey},
			OptionalReads: []string{
				identityKey + ".piiKey",
				identityKey + ".erasureRequested",
				identityKey + ".mergedInto",
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, wantOutcome)
	return reqID
}

// submitShredAt is submitShred with a caller-chosen submittedAt. The shared
// helper pins one instant, so a re-shred through it would restamp the same
// shreddedAt and the cycle-discriminator test would prove nothing.
func submitShredAt(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, identityKey, reqLabel, submittedAt string, wantOutcome processor.MessageOutcome) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(reqLabel),
		Lane:          processor.LaneUrgent,
		OperationType: "ShredIdentityKey",
		Actor:         pbStaffActorKey,
		SubmittedAt:   submittedAt,
		Class:         "shredIdentityKey",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{identityKey}, Enumerations: shredEnumerations(identityKey)},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, wantOutcome)
}

func markerData(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey string) map[string]any {
	t.Helper()
	doc := readDoc(t, ctx, conn, identityKey+".erasureRequested")
	data, _ := doc["data"].(map[string]any)
	if data == nil {
		t.Fatalf("erasureRequested doc for %s has no data map: %v", identityKey, doc)
	}
	return data
}

// TestSealIdentityForErasure_WritesMarkerAndEmits — the happy path. A shredded
// identity is sealed: exactly one aspect is written, it carries the piiKey's
// own shreddedAt as the cycle discriminator, and privacy.erasureRequested is
// emitted so the Loom step advances on its domain event rather than a
// deadline.
func TestSealIdentityForErasure_WritesMarkerAndEmits(t *testing.T) {
	ctx, conn := setupSealEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "seal-ok-default", v)
	urgentCP, urgentCons := newUrgentPipeline(t, ctx, conn, "seal-ok-urgent", v)
	sysCP, sysCons := newSystemPipeline(t, ctx, conn, "seal-ok-system", v)

	identityKey := createIdentity(t, ctx, conn, cp, cons, "SealOkIdent")
	recordPII(t, ctx, conn, cp, cons, identityKey, "SealOkPII")
	submitShred(t, ctx, conn, urgentCP, urgentCons, identityKey, "SealOkShred", processor.OutcomeAccepted)

	shreddedAt, _ := piiKeyData(t, ctx, conn, identityKey)["shreddedAt"].(string)
	if shreddedAt == "" {
		t.Fatal("precondition: the shred should have stamped piiKey.shreddedAt")
	}

	reqID := submitSeal(t, ctx, conn, sysCP, sysCons, identityKey, "SealOkSeal", processor.OutcomeAccepted)

	data := markerData(t, ctx, conn, identityKey)
	if got, _ := data["shreddedAt"].(string); got != shreddedAt {
		t.Errorf("marker shreddedAt = %q, want the piiKey's own %q — the field-diff the completion seal is judged by depends on it", got, shreddedAt)
	}
	if at, _ := data["requestedAt"].(string); at == "" {
		t.Error("marker requestedAt not stamped")
	}
	assertOutboxEventClass(t, ctx, conn, reqID, "privacy.erasureRequested")
}

// TestSealIdentityForErasure_UnshreddedIdentity_Rejected — an identity holding
// a live, never-shredded envelope cannot be sealed. This is the fail-closed
// case that matters: a seal here would copy a null discriminator, and
// null-versus-null later reads as "already sealed" — an erasure attesting a
// completion it never earned.
//
// The positive vector runs first (the identical seal on a SHREDDED identity is
// accepted, above), so this rejection cannot be passing for the wrong reason.
func TestSealIdentityForErasure_UnshreddedIdentity_Rejected(t *testing.T) {
	ctx, conn := setupSealEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "seal-unshred-default", v)
	sysCP, sysCons := newSystemPipeline(t, ctx, conn, "seal-unshred-system", v)

	identityKey := createIdentity(t, ctx, conn, cp, cons, "SealUnshredId")
	recordPII(t, ctx, conn, cp, cons, identityKey, "SealUnshredPII") // mints an UNSHREDDED piiKey

	submitSeal(t, ctx, conn, sysCP, sysCons, identityKey, "SealUnshredSeal", processor.OutcomeRejected)

	if kvExists(t, ctx, conn, identityKey+".erasureRequested") {
		t.Error("a rejected seal must not have written the marker — the write-path gates read its mere presence")
	}
}

// TestSealIdentityForErasure_NoPiiKey_Rejected — no envelope at all is the
// same refusal for the same reason. Seeded directly: CreateUnclaimedIdentity
// mints a piiKey through its sensitive writes, which is exactly what this case
// must avoid.
func TestSealIdentityForErasure_NoPiiKey_Rejected(t *testing.T) {
	ctx, conn := setupSealEnv(t)
	v := testutil.TestVault(t)
	sysCP, sysCons := newSystemPipeline(t, ctx, conn, "seal-nopii-system", v)

	const identityKey = "vtx.identity.BBseaNoPiiHJKMNPQRST"
	seedVertex(t, ctx, conn, identityKey, "identity", map[string]any{}, false)

	submitSeal(t, ctx, conn, sysCP, sysCons, identityKey, "SealNoPiiSeal", processor.OutcomeRejected)

	if kvExists(t, ctx, conn, identityKey+".erasureRequested") {
		t.Error("a rejected seal must not have written the marker")
	}
}

// TestSealIdentityForErasure_AbsentIdentity_Rejected — the target-existence
// guard. An identity key naming nothing is rejected before any mutation.
func TestSealIdentityForErasure_AbsentIdentity_Rejected(t *testing.T) {
	ctx, conn := setupSealEnv(t)
	v := testutil.TestVault(t)
	sysCP, sysCons := newSystemPipeline(t, ctx, conn, "seal-absent-system", v)

	const identityKey = "vtx.identity.BBseaAbsentHJKMNPQRS"
	submitSeal(t, ctx, conn, sysCP, sysCons, identityKey, "SealAbsentSeal", processor.OutcomeRejected)

	if kvExists(t, ctx, conn, identityKey+".erasureRequested") {
		t.Error("a rejected seal must not have written the marker")
	}
}

// TestSealIdentityForErasure_TombstonedIdentity_Rejected — same guard, the
// tombstoned arm.
func TestSealIdentityForErasure_TombstonedIdentity_Rejected(t *testing.T) {
	ctx, conn := setupSealEnv(t)
	v := testutil.TestVault(t)
	sysCP, sysCons := newSystemPipeline(t, ctx, conn, "seal-tomb-system", v)

	const identityKey = "vtx.identity.BBseaTombedHJKMNPQRS"
	seedVertex(t, ctx, conn, identityKey, "identity", map[string]any{}, true)
	seedVertex(t, ctx, conn, identityKey+".piiKey", "piiKey", map[string]any{
		"wrappedDEK": "", "shredded": true, "shreddedAt": "2026-08-07T10:00:00Z",
	}, false)

	submitSeal(t, ctx, conn, sysCP, sysCons, identityKey, "SealTombSeal", processor.OutcomeRejected)

	if kvExists(t, ctx, conn, identityKey+".erasureRequested") {
		t.Error("a tombstoned identity must not be sealable")
	}
}

// TestSealIdentityForErasure_ReSeal_PreservesRequestedAtRefreshesShreddedAt —
// the cycle semantics the whole erasure plane's re-trigger story rests on. A
// re-shred bumps piiKey.shreddedAt; a re-seal copies the NEW one so the
// completion seal's own sealedForShreddedAt no longer matches and the erasure
// reopens by field-diff — with nothing tombstoned. requestedAt is preserved,
// because the first request is the instant that matters.
func TestSealIdentityForErasure_ReSeal_PreservesRequestedAtRefreshesShreddedAt(t *testing.T) {
	ctx, conn := setupSealEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "seal-reseal-default", v)
	urgentCP, urgentCons := newUrgentPipeline(t, ctx, conn, "seal-reseal-urgent", v)
	sysCP, sysCons := newSystemPipeline(t, ctx, conn, "seal-reseal-system", v)

	identityKey := createIdentity(t, ctx, conn, cp, cons, "SealReId")
	recordPII(t, ctx, conn, cp, cons, identityKey, "SealRePII")
	submitShred(t, ctx, conn, urgentCP, urgentCons, identityKey, "SealReShred1", processor.OutcomeAccepted)
	submitSeal(t, ctx, conn, sysCP, sysCons, identityKey, "SealReSeal1", processor.OutcomeAccepted)

	first := markerData(t, ctx, conn, identityKey)
	firstRequestedAt, _ := first["requestedAt"].(string)
	firstShreddedAt, _ := first["shreddedAt"].(string)
	if firstRequestedAt == "" || firstShreddedAt == "" {
		t.Fatalf("precondition: first seal wrote %v", first)
	}

	// A second shred re-stamps shreddedAt with a later instant.
	submitShredAt(t, ctx, conn, urgentCP, urgentCons, identityKey, "SealReShred2", "2026-08-09T09:00:00Z", processor.OutcomeAccepted)
	reShreddedAt, _ := piiKeyData(t, ctx, conn, identityKey)["shreddedAt"].(string)
	if reShreddedAt == firstShreddedAt {
		t.Fatalf("precondition: the re-shred should have moved shreddedAt off %q", firstShreddedAt)
	}

	// A LATER submittedAt, so the preservation branch is the only thing that
	// can keep requestedAt where it was.
	const laterSubmit = "2026-08-09T12:00:00Z"
	submitSealAt(t, ctx, conn, sysCP, sysCons, pbSealActorKey, identityKey, "SealReSeal2", laterSubmit, processor.OutcomeAccepted)

	second := markerData(t, ctx, conn, identityKey)
	if got, _ := second["requestedAt"].(string); got != firstRequestedAt {
		t.Errorf("re-seal requestedAt = %q, want the original %q preserved", got, firstRequestedAt)
	}
	if firstRequestedAt == laterSubmit {
		t.Fatal("test is vacuous: the second seal's submittedAt equals the first's requestedAt")
	}
	if got, _ := second["shreddedAt"].(string); got != reShreddedAt {
		t.Errorf("re-seal shreddedAt = %q, want the new cycle's %q — otherwise a re-triggered erasure never reopens", got, reShreddedAt)
	}
}

// TestSealIdentityForErasure_UnauthorizedActor_Denied — the grant is real, and
// it is the GRANT doing the work. The denied actor differs from the permitted
// one in exactly one respect (it holds RecordShredFinalization instead of
// SealIdentityForErasure) — same system lane, same operator role — so this
// cannot be the lane check passing under another name.
func TestSealIdentityForErasure_UnauthorizedActor_Denied(t *testing.T) {
	ctx, conn := setupSealEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "seal-denied-default", v)
	urgentCP, urgentCons := newUrgentPipeline(t, ctx, conn, "seal-denied-urgent", v)
	sysCP, sysCons := newSystemPipeline(t, ctx, conn, "seal-denied-system", v)

	identityKey := createIdentity(t, ctx, conn, cp, cons, "SealDenyId")
	recordPII(t, ctx, conn, cp, cons, identityKey, "SealDenyPII")
	submitShred(t, ctx, conn, urgentCP, urgentCons, identityKey, "SealDenyShred", processor.OutcomeAccepted)

	submitSealAs(t, ctx, conn, sysCP, sysCons, pbUngrantedActorKey, identityKey, "SealDenySeal", processor.OutcomeRejected)

	if kvExists(t, ctx, conn, identityKey+".erasureRequested") {
		t.Error("a denied seal must not have written the marker")
	}
}

// TestSealIdentityForErasure_WideSubject_StillSeals — the property the whole
// decomposition exists for, at the only scale a test can reach cheaply: a
// subject carrying 300 credential edges (past the 256-link page limit that
// bounds every enumerating op in this package) seals exactly as a bare one
// does, because this op enumerates nothing. An op whose work cannot grow with
// its subject cannot refuse a person for being well connected.
func TestSealIdentityForErasure_WideSubject_StillSeals(t *testing.T) {
	ctx, conn := setupSealEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "seal-count-default", v)
	urgentCP, urgentCons := newUrgentPipeline(t, ctx, conn, "seal-count-urgent", v)
	sysCP, sysCons := newSystemPipeline(t, ctx, conn, "seal-count-system", v)

	identityKey := createIdentity(t, ctx, conn, cp, cons, "SealCountId")
	recordPII(t, ctx, conn, cp, cons, identityKey, "SealCountPII")

	// A wide footprint: 300 DISTINCT boundTo links, past the 256 page limit
	// that bounds every enumerating op in this package.
	//
	// The ids are built here rather than via testutil.GenReqID because that
	// helper is a pure function of its label AND silently drops characters
	// outside the NanoID alphabet — '0' among them — so "Cr10" and "Cr1"
	// produce the same id. Two safe-alphabet digits give 400 collision-free
	// ids. The assertion below is what makes any such collision visible
	// instead of quietly shrinking the fixture back to the bare-subject case
	// this test exists to differ from.
	const (
		wantLinks = 300
		safe      = "ABCDEFGHJKMNPQRSTUVW" // 20 chars, no I/l/O/0
	)
	seen := map[string]bool{}
	for i := 0; i < wantLinks; i++ {
		credID := "BBseaCr" + string(safe[i/len(safe)]) + string(safe[i%len(safe)]) + "HJKMNPQRSTU"
		seen[credID] = true
		seedVertex(t, ctx, conn, "lnk.identity."+credID+".boundTo.identity."+identityKey[len("vtx.identity."):],
			"boundTo", map[string]any{"boundAt": "2026-08-07T09:00:00Z"}, false)
	}
	if len(seen) != wantLinks {
		t.Fatalf("fixture is not wide: %d distinct credential ids for %d links", len(seen), wantLinks)
	}

	submitShred(t, ctx, conn, urgentCP, urgentCons, identityKey, "SealCountShred", processor.OutcomeAccepted)
	reqID := submitSeal(t, ctx, conn, sysCP, sysCons, identityKey, "SealCountSeal", processor.OutcomeAccepted)

	assertOutboxEventClass(t, ctx, conn, reqID, "privacy.erasureRequested")
	if !kvExists(t, ctx, conn, identityKey+".erasureRequested") {
		t.Error("the seal must land regardless of how connected the subject is")
	}
}

// TestSealIdentityForErasure_MergedIdentity_Rejected — the anchor guard. A
// merged-away identity keeps a LIVE vertex (MergeIdentity writes .state and
// .mergedInto rather than tombstoning) while its credentials and indexes have
// already moved to the survivor, so a residue count anchored on it reads zero
// by construction. Sealing it would attest an erasure that erased nothing.
//
// The positive control runs first and is the SAME seal on the SAME fixture
// minus the .mergedInto aspect, so the rejection can only be the merge guard.
func TestSealIdentityForErasure_MergedIdentity_Rejected(t *testing.T) {
	ctx, conn := setupSealEnv(t)
	v := testutil.TestVault(t)
	sysCP, sysCons := newSystemPipeline(t, ctx, conn, "seal-merged-system", v)

	shredded := map[string]any{
		"wrappedDEK": "", "keyId": "", "shredded": true, "shreddedAt": "2026-08-07T10:00:00Z",
	}

	// Positive vector: identical fixture, no mergedInto → accepted.
	const liveKey = "vtx.identity.BBseaUnmergedHJKMNPQ"
	seedVertex(t, ctx, conn, liveKey, "identity", map[string]any{}, false)
	seedVertex(t, ctx, conn, liveKey+".piiKey", "piiKey", shredded, false)
	submitSeal(t, ctx, conn, sysCP, sysCons, liveKey, "SealUnmerged", processor.OutcomeAccepted)
	if !kvExists(t, ctx, conn, liveKey+".erasureRequested") {
		t.Fatal("positive control did not seal — the negative below would prove nothing")
	}

	// Negative vector: the one added fact is .mergedInto.
	const mergedKey = "vtx.identity.BBseaMergedHJKMNPQRS"
	const survivorKey = "vtx.identity.BBseaSurvivorHJKMNPQ"
	seedVertex(t, ctx, conn, mergedKey, "identity", map[string]any{}, false)
	seedVertex(t, ctx, conn, mergedKey+".piiKey", "piiKey", shredded, false)
	seedVertex(t, ctx, conn, mergedKey+".mergedInto", "mergedInto", map[string]any{"value": survivorKey}, false)

	submitSeal(t, ctx, conn, sysCP, sysCons, mergedKey, "SealMerged", processor.OutcomeRejected)

	if kvExists(t, ctx, conn, mergedKey+".erasureRequested") {
		t.Error("a merged-away identity must not be sealable — its residue is zero by construction")
	}
}

// TestSealIdentityForErasure_ShreddedWithoutStamp_Rejected — an envelope
// shredded by a build that predates the finalization-cycle stamp carries no
// shreddedAt, so there is no cycle discriminator to record. Refuse rather than
// write a marker whose field-diff would compare null with null.
func TestSealIdentityForErasure_ShreddedWithoutStamp_Rejected(t *testing.T) {
	ctx, conn := setupSealEnv(t)
	v := testutil.TestVault(t)
	sysCP, sysCons := newSystemPipeline(t, ctx, conn, "seal-nostamp-system", v)

	const identityKey = "vtx.identity.BBseaNoStampHJKMNPQR"
	seedVertex(t, ctx, conn, identityKey, "identity", map[string]any{}, false)
	seedVertex(t, ctx, conn, identityKey+".piiKey", "piiKey", map[string]any{
		"wrappedDEK": "", "shredded": true, // no shreddedAt
	}, false)

	submitSeal(t, ctx, conn, sysCP, sysCons, identityKey, "SealNoStamp", processor.OutcomeRejected)

	if kvExists(t, ctx, conn, identityKey+".erasureRequested") {
		t.Error("a shred with no cycle discriminator must not be sealable")
	}
}

// TestSealIdentityForErasure_NonIdentityTarget_Rejected — parts_of's type
// check is the only thing standing between this op and writing an erasure
// marker onto a vertex of some other type; the aspect is not sensitive, so
// step 6's custody rule would not catch it.
func TestSealIdentityForErasure_NonIdentityTarget_Rejected(t *testing.T) {
	ctx, conn := setupSealEnv(t)
	v := testutil.TestVault(t)
	sysCP, sysCons := newSystemPipeline(t, ctx, conn, "seal-badtype-system", v)

	const foreignKey = "vtx.task.BBseaTaskTargetHJKMN"
	seedVertex(t, ctx, conn, foreignKey, "task", map[string]any{}, false)
	seedVertex(t, ctx, conn, foreignKey+".piiKey", "piiKey", map[string]any{
		"wrappedDEK": "", "shredded": true, "shreddedAt": "2026-08-07T10:00:00Z",
	}, false)

	submitSeal(t, ctx, conn, sysCP, sysCons, foreignKey, "SealBadType", processor.OutcomeRejected)

	if kvExists(t, ctx, conn, foreignKey+".erasureRequested") {
		t.Error("only an identity vertex may carry the erasure marker")
	}
}
