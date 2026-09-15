// derive_reads bare-submitter vector (Contract #2 §2.5 class (g)) for
// MergeIdentity — the envelope below declares no Reads and no OptionalReads,
// proving the script's own derive_reads(op) is what hydrates both identity
// roots (and their .state/.credentialBinding/erasure-gate keys, and both
// directions of the duplicateOf pair-link probe) rather than a caller's own
// declaration. MergeIdentity always runs two unconditional kv.Links walks off
// the secondary (identity_has_open_tasks' assignedTo probe,
// collect_indexes_repoints' indexes probe), Contract #2's orthogonal class
// (e) channel that derive_reads never populates, so the vector declares those
// two Enumerations. edges is submitted empty: the trust gate's own edge reads
// are a separate, deliberately caller-declared channel (each edge key must be
// named by the caller, per the script's own EdgeNotFound refusal), so an
// empty edges list is the shape that needs no such declaration at all.
package identityhygiene_test

import (
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// TestMergeIdentity_UndeclaredSubmitter_Merges: a MergeIdentity declaring only
// the two mandatory Enumerations merges the two identities. derive_reads' own
// optionalReads carries both roots (and their .state), so execute()'s
// pvtx/svtx state[key] checks see the two live identities rather than
// misreading either undeclared root as absent.
func TestMergeIdentity_UndeclaredSubmitter_Merges(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "mihnodecl")

	primaryID := testutil.GenReqID("PrimNoDeclMerge")
	secondaryID := testutil.GenReqID("SecNoDeclMerge0")
	primaryKey := "vtx.identity." + primaryID
	secondaryKey := "vtx.identity." + secondaryID

	seedIdentityVertex(t, ctx, conn, primaryKey, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, secondaryKey, "unclaimed", "")

	reqID := testutil.GenReqID("MrgNoDeclMerge0")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-05-23T10:00:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(primaryKey, secondaryKey, []string{}),
		ContextHint: &processor.ContextHint{
			Enumerations: []processor.EnumerationHint{
				{Hub: secondaryKey, Relation: "assignedTo", Direction: "in"},
				{Hub: secondaryKey, Relation: "indexes", Direction: "in"},
			},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	stateData := readAspectData(t, ctx, conn, secondaryKey+".state")
	if got, _ := stateData["value"].(string); got != "merged" {
		t.Fatalf("secondary.state = %q, want merged — the derivation must hydrate both identity roots for the merge to run at all", got)
	}
	miData := readAspectData(t, ctx, conn, secondaryKey+".mergedInto")
	if got, _ := miData["value"].(string); got != primaryKey {
		t.Fatalf("secondary.mergedInto = %q, want %s", got, primaryKey)
	}
}
