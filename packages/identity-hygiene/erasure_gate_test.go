// The erasure write-path gate on MergeIdentity (erasure-orchestration-design.md
// §6) — a merge touching a person sealed for erasure is refused on EITHER side,
// and the two sides fail differently enough that they need separate tests.
//
// Same pairing construction as identity-domain's erasure_gate_test.go: one
// corpus, the identical envelope submitted twice, and the only fact that
// changes between the runs is whether the marker is present. A merge has eight
// other refusals in front of this one, so a lone rejection assertion would not
// distinguish the gate from a fixture that never reached it.
//
// The marker is written straight to Core KV rather than by submitting
// SealIdentityForErasure: that op is privacy-base's, and this harness installs
// only identity-domain + identity-hygiene. What the gate consumes is the key's
// PRESENCE, which is what these fixtures reproduce.
package identityhygiene_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	identityhygiene "github.com/operatinggraph/lattice/packages/identity-hygiene"
)

func sealForErasure(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey string) {
	t.Helper()
	doc := map[string]any{
		"class":     "erasureRequested",
		"vertexKey": identityKey,
		"localName": "erasureRequested",
		"isDeleted": false,
		"data": map[string]any{
			"requestedAt": "2026-08-07T09:00:00Z",
			"shreddedAt":  "2026-08-07T08:59:00Z",
		},
	}
	raw, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey+".erasureRequested", raw); err != nil {
		t.Fatalf("seed erasureRequested on %s: %v", identityKey, err)
	}
}

// unsealForErasure hard-removes the marker. Nothing in production does this —
// its non-removal is the convention the erasure's convergence rests on — but
// it is what lets a test change exactly one fact and re-run.
func unsealForErasure(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey string) {
	t.Helper()
	if err := conn.KVDelete(ctx, testutil.HarnessCoreBucket, identityKey+".erasureRequested"); err != nil {
		t.Fatalf("remove erasureRequested from %s: %v", identityKey, err)
	}
}

func erasureMergeEnv(reqID, primaryKey, secondaryKey string) *processor.OperationEnvelope {
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-08-07T10:00:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(primaryKey, secondaryKey, []string{}),
		// Reads only. The erasure markers are NOT declared here — this DDL's
		// own derive_reads supplies them, and a test that named them by hand
		// would stay green with that derivation deleted.
		ContextHint: &processor.ContextHint{Reads: mergeReads(primaryKey, secondaryKey, []string{})},
	}
}

func assertMergeRefusedErased(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, env *processor.OperationEnvelope, wantSide string) {
	t.Helper()
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected — the erasure gate did not fire", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "ErasedIdentity: "+wantSide) {
		t.Fatalf("rejected with %+v, want the ErasedIdentity guard on the %s — a rejection from any of the eight other guards means this one never ran",
			reply.Error, wantSide)
	}
}

// TestErasureGate_Merge_RejectsSealedPrimary — a merge ONTO a sealed identity
// repoints the secondary's identityindex vertices at it and writes fresh
// inbound `indexes` links: new instances of exactly the representation the
// residue count measures, arriving after the seal closed the write path.
func TestErasureGate_Merge_RejectsSealedPrimary(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "erase-prim")

	primaryKey := "vtx.identity." + testutil.GenReqID("PrimErasePrim1")
	secondaryKey := "vtx.identity." + testutil.GenReqID("SecErasePrim11")
	seedIdentityVertex(t, ctx, conn, primaryKey, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, secondaryKey, "unclaimed", "")

	sealForErasure(t, ctx, conn, primaryKey)
	assertMergeRefusedErased(t, ctx, conn, cp, cons,
		erasureMergeEnv(testutil.GenReqID("MrgErasePrimNo"), primaryKey, secondaryKey), "primary")

	// The refusal wrote nothing: the secondary is untouched, so a later merge
	// (or a later erasure of the secondary itself) still sees the real corpus.
	if got, _ := readAspectData(t, ctx, conn, secondaryKey+".state")["value"].(string); got != "unclaimed" {
		t.Fatalf("secondary.state = %q after a refused merge, want unclaimed", got)
	}

	// One fact changed, nothing else.
	unsealForErasure(t, ctx, conn, primaryKey)
	testutil.PublishOp(t, conn, erasureMergeEnv(testutil.GenReqID("MrgErasePrimOk"), primaryKey, secondaryKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got, _ := readAspectData(t, ctx, conn, secondaryKey+".state")["value"].(string); got != "merged" {
		t.Fatalf("secondary.state = %q after the unsealed merge, want merged — the rejection above was some other guard's", got)
	}
}

// TestErasureGate_Merge_RejectsSealedSecondary is the one that matters more,
// because the failure it prevents looks like progress. Merging a sealed
// identity AWAY moves its indexes and credentials onto a live survivor, so the
// residue count anchored on it falls toward zero while every correlation it
// measured is alive under another key — an erasure that would attest
// completion over a person whose record merely changed address.
func TestErasureGate_Merge_RejectsSealedSecondary(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "erase-sec")

	primaryKey := "vtx.identity." + testutil.GenReqID("PrimEraseSec11")
	secondaryKey := "vtx.identity." + testutil.GenReqID("SecEraseSec111")
	seedIdentityVertex(t, ctx, conn, primaryKey, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, secondaryKey, "unclaimed", "")

	sealForErasure(t, ctx, conn, secondaryKey)
	assertMergeRefusedErased(t, ctx, conn, cp, cons,
		erasureMergeEnv(testutil.GenReqID("MrgEraseSecNo"), primaryKey, secondaryKey), "secondary")

	if got, _ := readAspectData(t, ctx, conn, secondaryKey+".state")["value"].(string); got != "unclaimed" {
		t.Fatalf("secondary.state = %q after a refused merge, want unclaimed", got)
	}

	unsealForErasure(t, ctx, conn, secondaryKey)
	testutil.PublishOp(t, conn, erasureMergeEnv(testutil.GenReqID("MrgEraseSecOk"), primaryKey, secondaryKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got, _ := readAspectData(t, ctx, conn, secondaryKey+".state")["value"].(string); got != "merged" {
		t.Fatalf("secondary.state = %q after the unsealed merge, want merged — the rejection above was some other guard's", got)
	}
}

// TestErasureGate_Merge_DeriveReadsNamesBothSides pins the class-(g)
// derivation as TEXT. Its effect is invisible behaviourally — declared or not,
// the gate refuses identically, because the script reads through kv.Read and an
// undeclared read falls through to a live Core KV GET. What the two tests above
// prove, declaring nothing, is that the gate holds with no help from the
// submitter; what they cannot prove is that the derivation still exists, and
// losing it would silently add two Core KV round trips to every merge.
func TestErasureGate_Merge_DeriveReadsNamesBothSides(t *testing.T) {
	var script string
	for _, d := range identityhygiene.DDLs() {
		if d.CanonicalName == "identityHygiene" {
			script = d.Script
		}
	}
	if script == "" {
		t.Fatal("no `identityHygiene` DDL script found")
	}
	deriveIdx := strings.Index(script, "def derive_reads(op):")
	executeIdx := strings.Index(script, "\ndef execute(state, op):")
	if deriveIdx < 0 || executeIdx <= deriveIdx {
		t.Fatalf("cannot locate derive_reads in the identityHygiene script (derive=%d execute=%d)", deriveIdx, executeIdx)
	}
	derive := script[deriveIdx:executeIdx]
	for _, field := range []string{`"primary"`, `"secondary"`} {
		if !strings.Contains(derive, field) {
			t.Fatalf("derive_reads does not name %s — the erasure marker for that side would fall to a live Core KV read on every merge", field)
		}
	}
	if !strings.Contains(derive, `".erasureRequested"`) {
		t.Fatal("derive_reads does not name the erasure marker aspect")
	}
}
