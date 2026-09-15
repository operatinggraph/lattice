// derive_reads bare-submitter vector (Contract #2 §2.5 class (g)) for
// TombstoneSupersededLeaseServiceInstance — the envelope below declares NO
// ContextHint at all. Six of the op's seven reads are pure functions of the
// payload and derive_reads supplies them; the seventh, the ownership
// instanceOf link, needs ddl["leaseServiceInstance"].metaKey — unreachable in
// the derive_reads pre-pass (kv/ddl are fail-closed stubs there) — so it stays
// the dispatcher's own declared read. execute() reads it from the step-4
// SNAPSHOT (`instance_of_lnk not in state`), never live, and refuses by name
// when it is undeclared (scripts.go:1986-1987) rather than falling through to
// an undeclared live kv.Read of the one trust-bearing key. This is the
// dossier's second sighting of the read-declaration OCC class
// (docs/components/_packages.md) proven by its OWN named refusal, not an
// accept: the op's correctness on this key rests on the caller's own
// declaration by design, and the vector is what proves the script fails
// CLOSED instead of reading lazily when that declaration is missing.
package leasesigning_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// TestTombstoneSupersededLeaseServiceInstance_UndeclaredSubmitter_RefusedOwnershipUndeclared
// restates TestTombstoneSupersededLeaseServiceInstance_NoContextHint_Rejected's
// own property as a literal ContextHint-absent envelope, in a
// Test*UndeclaredSubmitter* function, so the static
// lint-derive-reads-bare-vector census can see it: that test builds its
// envelope through submitTombstoneSuperseded/tsHint, whose composite literal
// sits inside those helpers' own bodies and so is invisible to a literal-only
// scan restricted to Test*UndeclaredSubmitter* functions.
func TestTombstoneSupersededLeaseServiceInstance_UndeclaredSubmitter_RefusedOwnershipUndeclared(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "tomb-nodecl-lit")

	subject := seedApplicant(t, ctx, conn, "BBNDCLSUBJCTHJKMNPQR")
	older := seedServiceInstance(t, ctx, conn, cp, cons, "tsNdOld1", "BBNDCLELDERHJKMNPQR1", "backgroundCheck", subject, "2026-09-01T00:00:00Z", "completed")
	newer := seedServiceInstance(t, ctx, conn, cp, cons, "tsNdNew1", "BBNDCLNEWERHJKMNPQR1", "backgroundCheck", subject, "2026-09-02T00:00:00Z", "completed")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("tsUndeclLitOp1"),
		Lane:          processor.LaneDefault,
		OperationType: "TombstoneSupersededLeaseServiceInstance",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-09-03T00:00:00Z",
		Class:         "leaseServiceInstance",
		Payload: json.RawMessage(`{"instanceKey":"` + older + `","supersededBy":"` + newer +
			`","subjectKey":"` + subject + `"}`),
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want Rejected (reply=%+v)", outcome, reply)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidArgument") {
		t.Fatalf("want InvalidArgument naming the undeclared ownership link, got %+v", reply.Error)
	}
	if !strings.Contains(reply.Error.Message, "contextHint.reads must declare the ownership link") {
		t.Fatalf("the refusal must name the missing declaration, got %q", reply.Error.Message)
	}
	if d, _ := readDoc(t, ctx, conn, older)["isDeleted"].(bool); d {
		t.Fatalf("a refused submission must not tombstone the instance — the derivation supplies six of seven reads, but nothing must commit off the one it cannot")
	}
}
