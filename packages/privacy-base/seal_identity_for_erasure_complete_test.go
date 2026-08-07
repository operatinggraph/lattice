// SealIdentityForErasureComplete (erasure-orchestration-design.md §7.2) — the
// erasure's terminal attestation, driven through the real Processor commit
// path so what these prove is what production does.
//
// Three carry more weight than the rest:
//
//   - TestSealIdentityForErasureComplete_FindsALiveLinkPastAPageOfTombstones
//     is the one that separates a real verification from a plausible one. A
//     tombstone is a SOFT delete, so a converged subject's first enumeration
//     page is all tombstones; a build that inspected one page and stopped
//     would attest every erasure it was asked about, including the ones with
//     live links sitting on page two. The fixture is 300 tombstoned links —
//     past the 256-key read page — with the single live one keyed to sort
//     last.
//   - TestSealIdentityForErasureComplete_RefusesAnOutstandingAsyncHalf pins
//     the obligation the residue lens handed this op by name. The lens does
//     order the two async gaps ahead of the seal gap, but a directOp gap fires
//     from a reconcile sweep as readily as from a fresh row, so a guarantee
//     that lives only in a projection's column ordering is not one.
//   - TestSealIdentityForErasureComplete_RefusesALiveCredentialInEitherDirection
//     is §13's named headline: force a live boundTo past a residue row that
//     reads zero and require the op to refuse. The whole design rests on the
//     lens deciding when to try and the op deciding whether it is true.
package privacybase_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

const (
	pbCompleteActorID  = "BBseaCmptCapHJKMNPQR"
	pbCompleteActorKey = "vtx.identity." + pbCompleteActorID

	pbCompleteUngrantedActorID  = "BBseaCmptNoGrantHJKM"
	pbCompleteUngrantedActorKey = "vtx.identity." + pbCompleteUngrantedActorID

	// pbCompleteShreddedAt is the cycle discriminator every fixture here seeds
	// onto the piiKey envelope, and the value a successful attestation must
	// copy into sealedForShreddedAt.
	pbCompleteShreddedAt = "2026-08-07T08:00:00Z"
)

// completeCapDoc grants SealIdentityForErasureComplete on the system lane —
// the grant shape the Weaver service actor carries in production, dispatching
// the identityErasureComplete target's terminal gap action (operator via
// holdsRole).
func completeCapDoc() *processor.CapabilityDoc {
	doc := sealCapDoc()
	doc.Key = "cap.identity." + pbCompleteActorID
	doc.Actor = pbCompleteActorKey
	doc.ProjectedFromRevisions = map[string]uint64{pbCompleteActorKey: 1}
	doc.PlatformPermissions = []processor.PlatformPermission{
		{OperationType: "SealIdentityForErasureComplete", Scope: "any"},
	}
	doc.Roles = []string{bootstrap.RoleOperatorKey}
	return doc
}

// completeCapDocMissingGrant is completeCapDoc's control: the same system
// lane and the same operator role, granting a different privacy-base op.
// Without it a denial test would be satisfied by the lane check alone and
// would stay green if this op's grant were deleted outright.
func completeCapDocMissingGrant() *processor.CapabilityDoc {
	doc := completeCapDoc()
	doc.Key = "cap.identity." + pbCompleteUngrantedActorID
	doc.Actor = pbCompleteUngrantedActorKey
	doc.ProjectedFromRevisions = map[string]uint64{pbCompleteUngrantedActorKey: 1}
	doc.PlatformPermissions = []processor.PlatformPermission{
		{OperationType: "SealIdentityForErasure", Scope: "any"},
	}
	return doc
}

func setupCompleteEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, staffCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, sealCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, completeCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, completeCapDocMissingGrant())
	return ctx, conn
}

func newCompletePipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:        durable,
		Instance:       "complete-" + durable,
		FilterSubjects: []string{"ops.system"},
	})
}

// submitCompleteAt publishes one SealIdentityForErasureComplete and drives it
// to wantOutcome. The declared read-set is what the identityErasureComplete
// target's directOp will declare; until that target lands this literal is the
// only thing pinning it (design §13's coverage guard still has nothing to
// compare against).
//
// submittedAt is explicit because the re-verification test needs two distinct
// instants: a shared helper pinning one would make its sealedAt-preservation
// assertion vacuous, green even against a build that restamped every time.
func submitCompleteAt(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, actorKey, subjectKey, reqLabel, submittedAt string, wantOutcome processor.MessageOutcome) string {
	t.Helper()
	reqID := testutil.GenReqID(reqLabel)
	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneSystem,
		OperationType: "SealIdentityForErasureComplete",
		Actor:         actorKey,
		SubmittedAt:   submittedAt,
		Class:         "sealIdentityForErasureComplete",
		Payload:       json.RawMessage(`{"subjectKey":"` + subjectKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{subjectKey},
			OptionalReads: []string{
				subjectKey + ".piiKey",
				subjectKey + ".erasureRequested",
				subjectKey + ".erasure",
				subjectKey + ".mergedInto",
				subjectKey + ".state",
			},
			Enumerations: []processor.EnumerationHint{
				{Hub: subjectKey, Relation: "boundTo", Direction: "in"},
				{Hub: subjectKey, Relation: "boundTo", Direction: "out"},
				{Hub: subjectKey, Relation: "indexes", Direction: "in"},
				{Hub: subjectKey, Relation: "duplicateOf", Direction: "out"},
				{Hub: subjectKey, Relation: "duplicateOf", Direction: "in"},
			},
		},
	})
	testutil.DriveOne(t, ctx, cp, cons, wantOutcome)
	return reqID
}

func submitComplete(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, subjectKey, reqLabel string, wantOutcome processor.MessageOutcome) string {
	t.Helper()
	return submitCompleteAt(t, ctx, conn, cp, cons, pbCompleteActorKey, subjectKey, reqLabel, "2026-08-07T14:00:00Z", wantOutcome)
}

// pbSeedAspect writes an aspect document directly at the shape the commit path
// stores it. Seeding rather than driving the producing op keeps each fixture
// able to state exactly one precondition — the async-half tests need an
// envelope that is shredded but NOT finalized, which no sequence of real ops
// leaves behind for long.
func pbSeedAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, vertexKey, localName, class string, data map[string]any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"class":     class,
		"vertexKey": vertexKey,
		"localName": localName,
		"isDeleted": false,
		"data":      data,
	})
	if err != nil {
		t.Fatalf("marshal %s.%s: %v", vertexKey, localName, err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, vertexKey+"."+localName, raw); err != nil {
		t.Fatalf("seed %s.%s: %v", vertexKey, localName, err)
	}
}

// pbSeedEnvelope writes a shredded piiKey envelope with the two async-half
// booleans set as the caller asks. Both true is the only state that may be
// attested.
func pbSeedEnvelope(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey string, vaultDestroyed, projectionsNullified bool) {
	t.Helper()
	pbSeedAspect(t, ctx, conn, identityKey, "piiKey", "piiKey", map[string]any{
		"shredded":             true,
		"shreddedAt":           pbCompleteShreddedAt,
		"vaultKeyDestroyed":    vaultDestroyed,
		"projectionsNullified": projectionsNullified,
	})
}

// pbResidueArms is the five (relation, direction) pairs the two sweeps clear
// and this op re-verifies — the single list every fixture and every assertion
// here is derived from, so an arm cannot be added to one and forgotten in the
// other.
var pbResidueArms = []struct{ relation, direction string }{
	{"boundTo", "in"},
	{"boundTo", "out"},
	{"indexes", "in"},
	{"duplicateOf", "out"},
	{"duplicateOf", "in"},
}

// pbSweepAllResidue tombstones every live link on all five arms, standing in
// for the two sweep ops having run to completion.
//
// CreateUnclaimedIdentity really does leave two live identityindex links (the
// email and the name), so a fixture that only seeded its OWN links would not be
// converged and this op would — correctly — refuse it. That is worth stating
// rather than working around: the op catches residue the fixture author did not
// put there, which is exactly its job.
func pbSweepAllResidue(t *testing.T, ctx context.Context, conn *substrate.Conn, subjectKey string) {
	t.Helper()
	_, id, ok := substrate.ParseVertexKey(subjectKey)
	if !ok {
		t.Fatalf("ParseVertexKey %s", subjectKey)
	}
	for _, arm := range pbResidueArms {
		filter := "lnk.*.*." + arm.relation + ".identity." + id
		if arm.direction == "out" {
			filter = "lnk.identity." + id + "." + arm.relation + ".>"
		}
		cursor := ""
		for page := 0; page < 200; page++ {
			keys, next, err := conn.KVListKeysFilter(ctx, testutil.HarnessCoreBucket, filter, cursor, 256)
			if err != nil {
				t.Fatalf("KVListKeysFilter %s: %v", filter, err)
			}
			for _, k := range keys {
				pbTombstoneKey(t, ctx, conn, k)
			}
			if next == "" {
				break
			}
			cursor = next
		}
	}
}

// pbTombstoneKey flips isDeleted on a stored document, preserving the rest of
// its body exactly as the commit path's own tombstone does.
func pbTombstoneKey(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	if deleted, _ := doc["isDeleted"].(bool); deleted {
		return
	}
	doc["isDeleted"] = true
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal %s: %v", key, err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, raw); err != nil {
		t.Fatalf("tombstone %s: %v", key, err)
	}
}

// pbConverged puts an identity in the one state an attestation may be written
// over: erasure requested, key shredded, both async halves landed, and no live
// residue link anywhere. Each test then breaks exactly one of those.
func pbConverged(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label string) string {
	t.Helper()
	identityKey := createIdentity(t, ctx, conn, cp, cons, label)
	pbSweepAllResidue(t, ctx, conn, identityKey)
	pbSealForErasure(t, ctx, conn, identityKey)
	pbSeedEnvelope(t, ctx, conn, identityKey, true, true)
	return identityKey
}

// pbAttestation reads the erasure attestation's data map, failing if none was
// written.
func pbAttestation(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey string) map[string]any {
	t.Helper()
	doc := readDoc(t, ctx, conn, identityKey+".erasure")
	data, _ := doc["data"].(map[string]any)
	if data == nil {
		t.Fatalf("erasure attestation for %s has no data map: %v", identityKey, doc)
	}
	return data
}

// pbAssertNoAttestation is every refusal test's second assertion, and the one
// that makes them mean something: a rejected outcome proves the op refused,
// this proves it wrote nothing on the way out.
func pbAssertNoAttestation(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey string) {
	t.Helper()
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, identityKey+".erasure"); err == nil {
		t.Fatalf("%s.erasure was written despite the refusal", identityKey)
	}
}

func pbCoverage(t *testing.T, data map[string]any, class string) int {
	t.Helper()
	cov, _ := data["coverage"].(map[string]any)
	if cov == nil {
		t.Fatalf("attestation carries no coverage map: %v", data)
	}
	n, ok := cov[class].(float64)
	if !ok {
		t.Fatalf("coverage has no %s count: %v", class, cov)
	}
	return int(n)
}

// TestSealIdentityForErasureComplete_AttestsAConvergedErasure — the ordinary
// path, over a footprint that really was swept: every residue link present and
// tombstoned, on all three relations.
//
// The coverage counts are the assertion that matters beyond "it wrote
// something". They record what was ERASED, per class, which is the only thing
// an attestation can usefully carry — a residue count in an attestation is
// always zero and proves nothing. A build that walked the arms but summed them
// into the wrong class, or that reported the live count it was checking for,
// fails here.
func TestSealIdentityForErasureComplete_AttestsAConvergedErasure(t *testing.T) {
	ctx, conn := setupCompleteEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "complete-ok-default", v)
	sysCP, sysCons := newCompletePipeline(t, ctx, conn, "complete-ok-system")

	identityKey := pbConverged(t, ctx, conn, cp, cons, "CompleteOk")
	identityID := strings.TrimPrefix(identityKey, "vtx.identity.")

	// One credential (inbound boundTo), two owned index vertices, one
	// duplicateOf pair on each side — all tombstoned, as the two sweeps leave
	// them.
	pbSeedResidueLink(t, ctx, conn, "lnk.identity.BBcredACmptHJKMNPQRS.boundTo.identity."+identityID,
		"boundTo", "vtx.identity.BBcredACmptHJKMNPQRS", identityKey, true)
	pbSeedResidueLink(t, ctx, conn, "lnk.identityindex.BBidxACmptHJKMNPQRST.indexes.identity."+identityID,
		"indexes", "vtx.identityindex.BBidxACmptHJKMNPQRST", identityKey, true)
	pbSeedResidueLink(t, ctx, conn, "lnk.identityindex.BBidxBCmptHJKMNPQRST.indexes.identity."+identityID,
		"indexes", "vtx.identityindex.BBidxBCmptHJKMNPQRST", identityKey, true)
	pbSeedResidueLink(t, ctx, conn, "lnk.identity."+identityID+".duplicateOf.identity.BBdupUtCmptHJKMNPQRS",
		"duplicateOf", identityKey, "vtx.identity.BBdupUtCmptHJKMNPQRS", true)
	pbSeedResidueLink(t, ctx, conn, "lnk.identity.BBdupNnCmptHJKMNPQRS.duplicateOf.identity."+identityID,
		"duplicateOf", "vtx.identity.BBdupNnCmptHJKMNPQRS", identityKey, true)

	reqID := submitComplete(t, ctx, conn, sysCP, sysCons, identityKey, "CompleteOkDo", processor.OutcomeAccepted)

	data := pbAttestation(t, ctx, conn, identityKey)
	if got := data["sealedForShreddedAt"]; got != pbCompleteShreddedAt {
		t.Errorf("sealedForShreddedAt: got %v, want %s — the attestation must name the cycle it covers", got, pbCompleteShreddedAt)
	}
	if got, _ := data["sealedAt"].(string); got != "2026-08-07T14:00:00Z" {
		t.Errorf("sealedAt: got %q, want the submittedAt of the commit that earned it", got)
	}
	if got := pbCoverage(t, data, "credentials"); got != 1 {
		t.Errorf("coverage.credentials: got %d, want 1", got)
	}
	// Four, not two: CreateUnclaimedIdentity really builds an email and a name
	// identityindex of its own, which pbConverged swept alongside the two
	// seeded here. Counting them is the point — coverage records what the walk
	// found, not what the fixture author remembered to put there.
	if got := pbCoverage(t, data, "indexes"); got != 4 {
		t.Errorf("coverage.indexes: got %d, want 4 — two seeded here plus the email and name indexes CreateUnclaimedIdentity built", got)
	}
	if got := pbCoverage(t, data, "duplicates"); got != 2 {
		t.Errorf("coverage.duplicates: got %d, want 2 — one pair on each side", got)
	}
	assertOutboxEventClass(t, ctx, conn, reqID, "privacy.erasureCompleted")
}

// pbSeedResidueLink seeds one residue link at the real key shape, live or
// tombstoned. Tombstoned links are what a converged subject is made of, so
// almost every fixture here needs them.
func pbSeedResidueLink(t *testing.T, ctx context.Context, conn *substrate.Conn,
	linkKey, relation, sourceKey, targetKey string, tombstoned bool) {
	t.Helper()
	testutil.SeedLink(t, ctx, conn, linkKey, relation, sourceKey, targetKey)
	if !tombstoned {
		return
	}
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, linkKey)
	if err != nil {
		t.Fatalf("read seeded link %s: %v", linkKey, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal seeded link %s: %v", linkKey, err)
	}
	doc["isDeleted"] = true
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal tombstoned link %s: %v", linkKey, err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, linkKey, raw); err != nil {
		t.Fatalf("tombstone link %s: %v", linkKey, err)
	}
}

// TestSealIdentityForErasureComplete_RefusesALiveCredentialInEitherDirection
// is §13's named headline. The subject is otherwise fully converged — the
// residue row would read zero on every other arm — and one live boundTo makes
// the attestation unwritable.
//
// BOTH directions, because UnbindIdentityCredentials sweeps both: the subject
// owns credentials and is itself someone else's. An implementation that walks
// only the inbound arm passes every other test in this file and fails here —
// which is the only reason the outbound case is written out separately rather
// than folded into the inbound one.
func TestSealIdentityForErasureComplete_RefusesALiveCredentialInEitherDirection(t *testing.T) {
	for _, tc := range []struct {
		name, label string
		outbound    bool
	}{
		{"inbound — a credential this person owns", "CompleteBoundIn", false},
		{"outbound — a credential this person is bound to", "CompleteBoundOut", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, conn := setupCompleteEnv(t)
			v := testutil.TestVault(t)
			cp, cons := newDefaultPipeline(t, ctx, conn, "cmp-bt-default-"+tc.label, v)
			sysCP, sysCons := newCompletePipeline(t, ctx, conn, "cmp-bt-system-"+tc.label)

			identityKey := pbConverged(t, ctx, conn, cp, cons, tc.label)
			identityID := strings.TrimPrefix(identityKey, "vtx.identity.")
			otherKey := "vtx.identity.BBotherBoundHJKMNPQR"
			otherID := "BBotherBoundHJKMNPQR"

			linkKey := "lnk.identity." + otherID + ".boundTo.identity." + identityID
			source, target := otherKey, identityKey
			if tc.outbound {
				linkKey = "lnk.identity." + identityID + ".boundTo.identity." + otherID
				source, target = identityKey, otherKey
			}
			pbSeedResidueLink(t, ctx, conn, linkKey, "boundTo", source, target, false)

			submitComplete(t, ctx, conn, sysCP, sysCons, identityKey, tc.label+"Do", processor.OutcomeRejected)
			pbAssertNoAttestation(t, ctx, conn, identityKey)
		})
	}
}

// TestSealIdentityForErasureComplete_RefusesALiveDedupLink covers the three
// arms PurgeIdentityDedupFootprint sweeps — indexes inbound, and duplicateOf in
// both directions. Each direction is its own case because a subject is on
// either side of a pair: the newcomer that matched an incumbent, and the
// incumbent later identities matched against.
func TestSealIdentityForErasureComplete_RefusesALiveDedupLink(t *testing.T) {
	for _, tc := range []struct {
		name, label, relation string
		outbound              bool
	}{
		{"an owned identityindex still indexing this person", "CompleteIdx", "indexes", false},
		{"a duplicateOf pair this person is the newcomer of", "CompleteDupOut", "duplicateOf", true},
		{"a duplicateOf pair this person is the incumbent of", "CompleteDupIn", "duplicateOf", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, conn := setupCompleteEnv(t)
			v := testutil.TestVault(t)
			cp, cons := newDefaultPipeline(t, ctx, conn, "cmp-dd-default-"+tc.label, v)
			sysCP, sysCons := newCompletePipeline(t, ctx, conn, "cmp-dd-system-"+tc.label)

			identityKey := pbConverged(t, ctx, conn, cp, cons, tc.label)
			identityID := strings.TrimPrefix(identityKey, "vtx.identity.")

			otherType, otherID := "identity", "BBotherDedupHJKMNPQR"
			if tc.relation == "indexes" {
				otherType, otherID = "identityindex", "BBotherNdxHJKMNPQRST"
			}
			otherKey := "vtx." + otherType + "." + otherID

			linkKey := "lnk." + otherType + "." + otherID + "." + tc.relation + ".identity." + identityID
			source, target := otherKey, identityKey
			if tc.outbound {
				linkKey = "lnk.identity." + identityID + "." + tc.relation + "." + otherType + "." + otherID
				source, target = identityKey, otherKey
			}
			pbSeedResidueLink(t, ctx, conn, linkKey, tc.relation, source, target, false)

			submitComplete(t, ctx, conn, sysCP, sysCons, identityKey, tc.label+"Do", processor.OutcomeRejected)
			pbAssertNoAttestation(t, ctx, conn, identityKey)
		})
	}
}

// TestSealIdentityForErasureComplete_RefusesAnOutstandingAsyncHalf discharges
// the obligation the residue lens named when it ordered the two async gaps
// ahead of the seal gap: "the increment that builds
// SealIdentityForErasureComplete inherits that obligation".
//
// The point is precisely that the lens's ordering is NOT the guarantee. A
// directOp gap fires from a reconcile sweep as readily as from a fresh row,
// and a stale row can carry a seal gap open while a Vault key is still live.
// A build that trusted the projection's ordering passes every other test here
// and writes an attestation over an undestroyed key.
func TestSealIdentityForErasureComplete_RefusesAnOutstandingAsyncHalf(t *testing.T) {
	for _, tc := range []struct {
		name, label                          string
		vaultDestroyed, projectionsNullified bool
	}{
		{"the Vault still holds the key", "CompleteVault", false, true},
		{"decrypted renderings were never nullified", "CompleteProj", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, conn := setupCompleteEnv(t)
			v := testutil.TestVault(t)
			cp, cons := newDefaultPipeline(t, ctx, conn, "cmp-async-default-"+tc.label, v)
			sysCP, sysCons := newCompletePipeline(t, ctx, conn, "cmp-async-system-"+tc.label)

			// Swept first, so the ONLY thing wrong with this subject is the
			// async half under test. Without the sweep the fixture is also
			// refusable on the indexes arm, and the test would pass for that
			// reason instead — green even against a build with no async check
			// at all, as long as the walk still ran.
			identityKey := createIdentity(t, ctx, conn, cp, cons, tc.label)
			pbSweepAllResidue(t, ctx, conn, identityKey)
			pbSealForErasure(t, ctx, conn, identityKey)
			pbSeedEnvelope(t, ctx, conn, identityKey, tc.vaultDestroyed, tc.projectionsNullified)

			submitComplete(t, ctx, conn, sysCP, sysCons, identityKey, tc.label+"Do", processor.OutcomeRejected)
			pbAssertNoAttestation(t, ctx, conn, identityKey)

			// The positive vector: the same subject with both halves landed
			// must attest, so the refusal above cannot be anything else.
			pbSeedEnvelope(t, ctx, conn, identityKey, true, true)
			submitComplete(t, ctx, conn, sysCP, sysCons, identityKey, tc.label+"Yes", processor.OutcomeAccepted)
		})
	}
}

// TestSealIdentityForErasureComplete_FindsALiveLinkPastAPageOfTombstones is
// the boundary-crossing proof, and the fixture size is the whole point.
//
// A converged subject's enumeration is all tombstones, because a tombstone is
// a soft delete and kv.Links keeps returning swept links with isDeleted set.
// So a build that read one page and concluded "no live link" would attest
// every erasure it was ever asked about. Only a fixture PAST the 256-key read
// page can tell the two builds apart: at 100 tombstones the single-page build
// still finds the live link and this test would pass against it.
//
// The live link's index id is keyed to sort after all 300 tombstoned ones, so
// it lands on a later page rather than by luck of ordering.
func TestSealIdentityForErasureComplete_FindsALiveLinkPastAPageOfTombstones(t *testing.T) {
	ctx, conn := setupCompleteEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "complete-page-default", v)
	sysCP, sysCons := newCompletePipeline(t, ctx, conn, "complete-page-system")

	identityKey := pbConverged(t, ctx, conn, cp, cons, "CompletePage")
	identityID := strings.TrimPrefix(identityKey, "vtx.identity.")

	// Every character of every id must lie inside the canonical NanoID
	// alphabet (no I, O, l or 0): connLinkLister SKIPS a key ParseLinkKey
	// rejects, so a stray character does not fail loudly — it silently drops
	// the link from the enumeration, and this test would then be measuring a
	// narrow subject while claiming to measure a wide one.
	const (
		wantTombstones = 300
		safe           = "ABCDEFGHJKMNPQRSTUVW" // 20 chars, no I/l/O/0
	)
	seen := map[string]bool{}
	for i := 0; i < wantTombstones; i++ {
		idxID := "BBcmpdx" + string(safe[i/len(safe)]) + string(safe[i%len(safe)]) + "HJKMNPQRSTU"
		seen[idxID] = true
		idxKey := "vtx.identityindex." + idxID
		seedVertex(t, ctx, conn, idxKey, "identityindex", map[string]any{"identityKey": identityKey}, true)
		pbSeedResidueLink(t, ctx, conn, "lnk.identityindex."+idxID+".indexes.identity."+identityID,
			"indexes", idxKey, identityKey, true)
	}
	if len(seen) != wantTombstones {
		t.Fatalf("fixture is not wide: %d distinct index ids for %d vertices", len(seen), wantTombstones)
	}

	// 'W' sorts after every 'B'-prefixed id above, so this one is reachable
	// only by a walk that pages the whole cursor.
	liveIdxID := "WWcmpvedxHJKMNPQRSTU"
	liveIdxKey := "vtx.identityindex." + liveIdxID
	seedVertex(t, ctx, conn, liveIdxKey, "identityindex", map[string]any{"identityKey": identityKey}, false)
	pbSeedResidueLink(t, ctx, conn, "lnk.identityindex."+liveIdxID+".indexes.identity."+identityID,
		"indexes", liveIdxKey, identityKey, false)

	submitComplete(t, ctx, conn, sysCP, sysCons, identityKey, "CompletePageDo", processor.OutcomeRejected)
	pbAssertNoAttestation(t, ctx, conn, identityKey)
}

// TestSealIdentityForErasureComplete_RequiresAnErasureRequestMarker — an
// identity nobody asked to erase has an OPEN residue set, so a completion
// attestation over it would be a statement about a moment rather than about a
// person.
//
// The positive vector runs first: a negative test can pass for the wrong
// reason, and this one would be satisfied by any unrelated refusal. The same
// identity, marker added, must be attestable.
func TestSealIdentityForErasureComplete_RequiresAnErasureRequestMarker(t *testing.T) {
	ctx, conn := setupCompleteEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "complete-mark-default", v)
	sysCP, sysCons := newCompletePipeline(t, ctx, conn, "complete-mark-system")

	identityKey := createIdentity(t, ctx, conn, cp, cons, "CompleteMark")
	pbSweepAllResidue(t, ctx, conn, identityKey)
	pbSeedEnvelope(t, ctx, conn, identityKey, true, true)

	submitComplete(t, ctx, conn, sysCP, sysCons, identityKey, "CompleteMarkNo", processor.OutcomeRejected)
	pbAssertNoAttestation(t, ctx, conn, identityKey)

	// A marker of the WRONG class must not arm it either: the aspect-type DDL
	// gates the class, not the key, so a mutation at this key declaring some
	// other class falls to the permissive default and any package script could
	// write one.
	pbSealForErasureAs(t, ctx, conn, identityKey, "notAnErasureRequest", false)
	submitComplete(t, ctx, conn, sysCP, sysCons, identityKey, "CompleteMarkCls", processor.OutcomeRejected)
	pbAssertNoAttestation(t, ctx, conn, identityKey)

	pbSealForErasure(t, ctx, conn, identityKey)
	submitComplete(t, ctx, conn, sysCP, sysCons, identityKey, "CompleteMarkYes", processor.OutcomeAccepted)
	if got := pbAttestation(t, ctx, conn, identityKey)["sealedForShreddedAt"]; got != pbCompleteShreddedAt {
		t.Errorf("sealedForShreddedAt: got %v, want %s", got, pbCompleteShreddedAt)
	}
}

// TestSealIdentityForErasureComplete_RequiresAShreddedEnvelope — the
// attestation copies piiKey.shreddedAt as its cycle discriminator. Without one
// there is nothing to record, and a null-versus-null field-diff reads as
// "already sealed" — an erasure attesting completion it never earned, on every
// cycle from then on.
func TestSealIdentityForErasureComplete_RequiresAShreddedEnvelope(t *testing.T) {
	ctx, conn := setupCompleteEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "complete-shred-default", v)
	sysCP, sysCons := newCompletePipeline(t, ctx, conn, "complete-shred-system")

	// No envelope at all.
	noEnvelope := createIdentity(t, ctx, conn, cp, cons, "CompleteNoEnv")
	pbSweepAllResidue(t, ctx, conn, noEnvelope)
	pbSealForErasure(t, ctx, conn, noEnvelope)
	submitComplete(t, ctx, conn, sysCP, sysCons, noEnvelope, "CompleteNoEnvDo", processor.OutcomeRejected)
	pbAssertNoAttestation(t, ctx, conn, noEnvelope)

	// An envelope that exists but was never shredded.
	unshredded := createIdentity(t, ctx, conn, cp, cons, "CompleteUnshred")
	pbSweepAllResidue(t, ctx, conn, unshredded)
	pbSealForErasure(t, ctx, conn, unshredded)
	pbSeedAspect(t, ctx, conn, unshredded, "piiKey", "piiKey", map[string]any{
		"shredded": false, "vaultKeyDestroyed": true, "projectionsNullified": true,
	})
	submitComplete(t, ctx, conn, sysCP, sysCons, unshredded, "CompleteUnshredDo", processor.OutcomeRejected)
	pbAssertNoAttestation(t, ctx, conn, unshredded)

	// Shredded, both halves landed, but no shreddedAt stamp to record.
	noStamp := createIdentity(t, ctx, conn, cp, cons, "CompleteNoStamp")
	pbSweepAllResidue(t, ctx, conn, noStamp)
	pbSealForErasure(t, ctx, conn, noStamp)
	pbSeedAspect(t, ctx, conn, noStamp, "piiKey", "piiKey", map[string]any{
		"shredded": true, "vaultKeyDestroyed": true, "projectionsNullified": true,
	})
	submitComplete(t, ctx, conn, sysCP, sysCons, noStamp, "CompleteNoStampDo", processor.OutcomeRejected)
	pbAssertNoAttestation(t, ctx, conn, noStamp)
}

// TestSealIdentityForErasureComplete_RefusesAMergedAwayIdentity — MergeIdentity
// leaves the secondary's vertex LIVE while its credentials and indexes have
// already moved to the survivor. So a residue walk anchored there counts zero
// on its first pass while every representation of that person lives on
// un-erased under another key: an attestation over a silent failure, reached by
// the ordinary sequence "merge, then request erasure naming the pre-merge
// identity".
func TestSealIdentityForErasureComplete_RefusesAMergedAwayIdentity(t *testing.T) {
	ctx, conn := setupCompleteEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "complete-merged-default", v)
	sysCP, sysCons := newCompletePipeline(t, ctx, conn, "complete-merged-system")

	identityKey := pbConverged(t, ctx, conn, cp, cons, "CompleteMerged")
	pbSeedAspect(t, ctx, conn, identityKey, "mergedInto", "mergedInto",
		map[string]any{"value": "vtx.identity.BBsurvivrCmptHJKMNPQ"})

	submitComplete(t, ctx, conn, sysCP, sysCons, identityKey, "CompleteMergedDo", processor.OutcomeRejected)
	pbAssertNoAttestation(t, ctx, conn, identityKey)
}

// TestSealIdentityForErasureComplete_PreservesSealedAtWithinACycleAndRestampsAcrossOne
// — erasure is a cycle, not a state.
//
// Re-verifying the SAME cycle keeps the instant the first verification earned:
// that is the legally meaningful one, and a re-dispatch after a briefly stale
// row must not quietly move it. A re-SHRED starts a second cycle, and the
// attestation must then name the new discriminator — which is what makes the
// residue lens's field-diff reopen the gap without anything being tombstoned.
//
// The two submissions carry distinct submittedAt values on purpose: with one
// shared instant, a build that restamped every time would be indistinguishable
// from one that preserved.
func TestSealIdentityForErasureComplete_PreservesSealedAtWithinACycleAndRestampsAcrossOne(t *testing.T) {
	ctx, conn := setupCompleteEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "complete-cycle-default", v)
	sysCP, sysCons := newCompletePipeline(t, ctx, conn, "complete-cycle-system")

	identityKey := pbConverged(t, ctx, conn, cp, cons, "CompleteCycle")

	submitCompleteAt(t, ctx, conn, sysCP, sysCons, pbCompleteActorKey, identityKey,
		"CompleteCycle1", "2026-08-07T15:00:00Z", processor.OutcomeAccepted)
	first := pbAttestation(t, ctx, conn, identityKey)
	if got, _ := first["sealedAt"].(string); got != "2026-08-07T15:00:00Z" {
		t.Fatalf("first sealedAt: got %q, want 2026-08-07T15:00:00Z", got)
	}

	submitCompleteAt(t, ctx, conn, sysCP, sysCons, pbCompleteActorKey, identityKey,
		"CompleteCycle2", "2026-08-07T16:00:00Z", processor.OutcomeAccepted)
	second := pbAttestation(t, ctx, conn, identityKey)
	if got, _ := second["sealedAt"].(string); got != "2026-08-07T15:00:00Z" {
		t.Errorf("re-verifying the same cycle moved sealedAt to %q; the instant the first verification earned is the meaningful one", got)
	}

	// A re-shred: new discriminator, new cycle, new attestation instant.
	const reShreddedAt = "2026-08-07T17:30:00Z"
	pbSeedAspect(t, ctx, conn, identityKey, "piiKey", "piiKey", map[string]any{
		"shredded": true, "shreddedAt": reShreddedAt,
		"vaultKeyDestroyed": true, "projectionsNullified": true,
	})
	submitCompleteAt(t, ctx, conn, sysCP, sysCons, pbCompleteActorKey, identityKey,
		"CompleteCycle3", "2026-08-07T18:00:00Z", processor.OutcomeAccepted)
	third := pbAttestation(t, ctx, conn, identityKey)
	if got := third["sealedForShreddedAt"]; got != reShreddedAt {
		t.Errorf("sealedForShreddedAt after a re-shred: got %v, want %s — the second cycle must be attested on its own evidence", got, reShreddedAt)
	}
	if got, _ := third["sealedAt"].(string); got != "2026-08-07T18:00:00Z" {
		t.Errorf("sealedAt after a re-shred: got %q, want the new cycle's own instant", got)
	}
}

// pbAssertNoOutboxEventClass is assertOutboxEventClass's negative: the commit
// landed and deliberately announced nothing.
//
// A commit that emits no event writes no outbox aspect at all, so an absent key
// is the ordinary pass here — not the missing-aspect failure the positive
// helper treats it as.
func pbAssertNoOutboxEventClass(t *testing.T, ctx context.Context, conn *substrate.Conn, reqID, class string) {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.OutboxAspectKey(reqID))
	if err != nil {
		return
	}
	aspect, err := processor.ParseOutboxAspect(entry.Value)
	if err != nil {
		t.Fatalf("parse outbox aspect: %v", err)
	}
	for _, ev := range aspect.Data.Events {
		if ev.EventType == class {
			t.Fatalf("outbox for %s emitted %s; this commit re-verified an already-attested cycle and must announce nothing", reqID, class)
		}
	}
}

// TestSealIdentityForErasureComplete_AnnouncesOncePerCycle — the completion
// event's claim is that a person's erasure was verified complete, which happens
// once per cycle. The op is idempotent and the Weaver re-dispatches a gap until
// it sees the gap close, so an unconditional emission would announce the same
// completion on every reconcile pass and a consumer counting erasures would
// over-count without any of them being wrong.
func TestSealIdentityForErasureComplete_AnnouncesOncePerCycle(t *testing.T) {
	ctx, conn := setupCompleteEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "complete-once-default", v)
	sysCP, sysCons := newCompletePipeline(t, ctx, conn, "complete-once-system")

	identityKey := pbConverged(t, ctx, conn, cp, cons, "CompleteQnce")

	first := submitCompleteAt(t, ctx, conn, sysCP, sysCons, pbCompleteActorKey, identityKey,
		"CompleteQnce1", "2026-08-07T15:00:00Z", processor.OutcomeAccepted)
	assertOutboxEventClass(t, ctx, conn, first, "privacy.erasureCompleted")

	second := submitCompleteAt(t, ctx, conn, sysCP, sysCons, pbCompleteActorKey, identityKey,
		"CompleteQnce2", "2026-08-07T16:00:00Z", processor.OutcomeAccepted)
	pbAssertNoOutboxEventClass(t, ctx, conn, second, "privacy.erasureCompleted")

	// A re-shred is a new cycle, and a new cycle is a new announcement.
	pbSeedAspect(t, ctx, conn, identityKey, "piiKey", "piiKey", map[string]any{
		"shredded": true, "shreddedAt": "2026-08-07T17:30:00Z",
		"vaultKeyDestroyed": true, "projectionsNullified": true,
	})
	third := submitCompleteAt(t, ctx, conn, sysCP, sysCons, pbCompleteActorKey, identityKey,
		"CompleteQnce3", "2026-08-07T18:00:00Z", processor.OutcomeAccepted)
	assertOutboxEventClass(t, ctx, conn, third, "privacy.erasureCompleted")
}

// TestSealIdentityForErasureComplete_ATombstonedAttestationDoesNotRestampTheDate
// — no aspect-type DDL can refuse a tombstone (a tombstone carries no document,
// so step 6 never resolves the class), so any package script can remove this
// attestation. Recovery is the good part: the residue lens reopens the gap and
// this op rewrites it. But sealedAt is the field with legal meaning, and a
// live-only read of the prior attestation would make that recovery silently
// restamp "this person was erased at" to now.
func TestSealIdentityForErasureComplete_ATombstonedAttestationDoesNotRestampTheDate(t *testing.T) {
	ctx, conn := setupCompleteEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "complete-tomb-default", v)
	sysCP, sysCons := newCompletePipeline(t, ctx, conn, "complete-tomb-system")

	identityKey := pbConverged(t, ctx, conn, cp, cons, "CompleteTomb")

	submitCompleteAt(t, ctx, conn, sysCP, sysCons, pbCompleteActorKey, identityKey,
		"CompleteTomb1", "2026-08-07T15:00:00Z", processor.OutcomeAccepted)

	pbTombstoneKey(t, ctx, conn, identityKey+".erasure")

	submitCompleteAt(t, ctx, conn, sysCP, sysCons, pbCompleteActorKey, identityKey,
		"CompleteTomb2", "2026-08-07T19:00:00Z", processor.OutcomeAccepted)

	data := pbAttestation(t, ctx, conn, identityKey)
	if got, _ := data["sealedAt"].(string); got != "2026-08-07T15:00:00Z" {
		t.Errorf("sealedAt after rewriting a tombstoned attestation: got %q, want the original 2026-08-07T15:00:00Z — the instant the verification first earned is the one with legal meaning", got)
	}
	if deleted := readIsDeleted(t, ctx, conn, identityKey+".erasure"); deleted {
		t.Error("the attestation should be live again after the rewrite")
	}
}

// TestSealIdentityForErasureComplete_RefusesAMergedStateWithNoMergedInto — the
// merged gate keys on .state, which every identity carries, rather than on
// .mergedInto, which only a merged one does. A gate that read only .mergedInto
// would pass an identity whose .state says merged but whose .mergedInto is
// absent or shaped differently, and attest an erasure over a person whose
// credentials and indexes live on under the survivor.
func TestSealIdentityForErasureComplete_RefusesAMergedStateWithNoMergedInto(t *testing.T) {
	ctx, conn := setupCompleteEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "complete-state-default", v)
	sysCP, sysCons := newCompletePipeline(t, ctx, conn, "complete-state-system")

	identityKey := pbConverged(t, ctx, conn, cp, cons, "CompleteState")

	// The positive vector first: this subject is attestable before .state says
	// merged, so the refusal below is that one fact and nothing else.
	submitCompleteAt(t, ctx, conn, sysCP, sysCons, pbCompleteActorKey, identityKey,
		"CompleteStateYes", "2026-08-07T15:00:00Z", processor.OutcomeAccepted)

	merged := pbConverged(t, ctx, conn, cp, cons, "CompleteStateNo")
	pbSeedAspect(t, ctx, conn, merged, "state", "state", map[string]any{"value": "merged"})
	submitComplete(t, ctx, conn, sysCP, sysCons, merged, "CompleteStateDo", processor.OutcomeRejected)
	pbAssertNoAttestation(t, ctx, conn, merged)
}

// TestSealIdentityForErasureComplete_RefusesAnAbsentOrTombstonedSubject — the
// two shapes a bad subjectKey takes. An entirely absent key never reaches the
// script: a declared read that is absent is recorded required-absent at step 4
// and faults HydrationMiss. A tombstoned one hydrates and the script's own
// guard refuses it.
func TestSealIdentityForErasureComplete_RefusesAnAbsentOrTombstonedSubject(t *testing.T) {
	ctx, conn := setupCompleteEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "complete-absent-default", v)
	sysCP, sysCons := newCompletePipeline(t, ctx, conn, "complete-absent-system")

	submitComplete(t, ctx, conn, sysCP, sysCons, "vtx.identity.BBabsentCmptHJKMNPQR",
		"CompleteAbsent", processor.OutcomeRejected)

	gone := pbConverged(t, ctx, conn, cp, cons, "CompleteGone")
	seedVertex(t, ctx, conn, gone, "identity", map[string]any{}, true)
	submitComplete(t, ctx, conn, sysCP, sysCons, gone, "CompleteGoneDo", processor.OutcomeRejected)
	pbAssertNoAttestation(t, ctx, conn, gone)
}

// TestSealIdentityForErasureComplete_DeniedWithoutTheGrant — the control actor
// holds the same system lane and the same operator role and a different
// privacy-base grant, so what this proves is the op's own grant rather than
// the lane check.
func TestSealIdentityForErasureComplete_DeniedWithoutTheGrant(t *testing.T) {
	ctx, conn := setupCompleteEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "complete-deny-default", v)
	sysCP, sysCons := newCompletePipeline(t, ctx, conn, "complete-deny-system")

	identityKey := pbConverged(t, ctx, conn, cp, cons, "CompleteDeny")

	submitCompleteAt(t, ctx, conn, sysCP, sysCons, pbCompleteUngrantedActorKey, identityKey,
		"CompleteDenyNo", "2026-08-07T14:00:00Z", processor.OutcomeRejected)
	pbAssertNoAttestation(t, ctx, conn, identityKey)

	// The positive vector: the identical submission under the granted actor
	// must succeed, so the denial above cannot be some unrelated refusal.
	submitComplete(t, ctx, conn, sysCP, sysCons, identityKey, "CompleteDenyYes", processor.OutcomeAccepted)
}
