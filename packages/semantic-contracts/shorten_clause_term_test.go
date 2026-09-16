package semanticcontracts_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	semanticcontracts "github.com/operatinggraph/lattice/packages/semantic-contracts"
)

// scShortenLinkLister is a fixed-answer processor.ScriptLinkLister for the
// script-runner-level tests below (the TestEndTenancyScript_… /
// TestRecordApplicationLossScript_… shape): the runner never paginates past
// what one page hands back, so a single static slice is enough to prove the
// script's own read of it.
type scShortenLinkLister struct {
	links []processor.LinkDoc
}

func (l scShortenLinkLister) ListLinks(_ context.Context, _, _ string, _ int) ([]processor.LinkDoc, string, error) {
	return l.links, "", nil
}

// findClauseDDLScript returns the clause vertexType DDL's Starlark source —
// the one script ShortenClauseTerm (and every other clause op) dispatches
// through.
func findClauseDDLScript(t *testing.T) string {
	t.Helper()
	for _, d := range semanticcontracts.Package.DDLs {
		if d.CanonicalName == "clause" {
			return d.Script
		}
	}
	t.Fatal("clause vertexType DDL not found")
	return ""
}

// ShortenClauseTerm through the real Processor: a recorded notice on the
// lease caps an already-termed monthly clause's validUntil at the earlier of
// the recorded moveOutAt and the clause's own term, and completes the clause
// once its recorded due date reaches the new validUntil.

// termedClauseFixture mints a termed monthly clause governing a fresh lease
// (validFrom/validUntil as given), optionally pre-stamping the clause's
// recorded due date, and returns the clause key and lease key.
func termedClauseFixture(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, leaseID, label, validFrom, validUntil, prevDue string) (string, string) {
	t.Helper()
	leaseKey := seedLease(t, ctx, conn, leaseID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacct"+label, leaseKey)
	clauseKey := submitCreateClause(t, ctx, conn, cp, cons, "createclause"+label, leaseKey, acctKey,
		`,"amountCents":240000,"period":"monthly","validFrom":"`+validFrom+`","validUntil":"`+validUntil+`"`,
		processor.OutcomeAccepted)
	if prevDue != "" {
		seedAspect(t, ctx, conn, clauseKey, "status", "clauseStatus", map[string]any{"state": "active", "chargeValidUntil": prevDue})
	}
	return clauseKey, leaseKey
}

// seedNotice writes the lease's .notice aspect (the LoftSpace "a tenant gives
// notice" design's GiveNotice shape — moveOutAt/givenAt/givenBy) directly,
// standing in for lease-signing's GiveNotice op (built concurrently).
func seedNotice(t *testing.T, ctx context.Context, conn *substrate.Conn, leaseKey, moveOutAt string) {
	t.Helper()
	seedAspect(t, ctx, conn, leaseKey, "notice", "tenancyNotice", map[string]any{
		"moveOutAt": moveOutAt, "givenAt": "2026-10-01T00:00:00Z", "givenBy": "tenant",
	})
}

// submitShortenClauseTerm publishes the leaseRentSettlement playbook's
// missing_termShortened shape — Reads on the clause, its .terms, the lease
// and its .notice; .status as the absence-tolerant read — and drives it to
// the wanted outcome.
func submitShortenClauseTerm(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, clauseKey, leaseAppKey string, want processor.MessageOutcome) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "ShortenClauseTerm",
		Actor:         scActorKey,
		SubmittedAt:   "2026-10-05T12:00:00Z",
		Class:         "clause",
		Payload:       json.RawMessage(`{"clauseKey":"` + clauseKey + `","leaseAppKey":"` + leaseAppKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{clauseKey, clauseKey + ".terms", leaseAppKey, leaseAppKey + ".notice"},
			OptionalReads: []string{clauseKey + ".status"},
			Enumerations:  []processor.EnumerationHint{{Hub: clauseKey, Relation: "governs", Direction: "out"}},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// TestShortenClauseTerm_MoveOutInsideTerm_CapsValidUntil is the positive
// vector: a termed clause [2026-09-08, 2027-09-08) with a recorded due date
// well before the move-out is capped at the move-out, and stays active
// because its recorded due is still ahead of the new validUntil.
func TestShortenClauseTerm_MoveOutInsideTerm_CapsValidUntil(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "shorten1")
	clauseKey, leaseKey := termedClauseFixture(t, ctx, conn, cp, cons, "BBLEASESHRTNTRMHJKMA", "shrt01",
		"2026-09-08T00:00:00Z", "2027-09-08T00:00:00Z", "2026-12-08T00:00:00Z")
	seedNotice(t, ctx, conn, leaseKey, "2027-02-20T00:00:00Z")

	submitShortenClauseTerm(t, ctx, conn, cp, cons, "shortenterm00000001", clauseKey, leaseKey, processor.OutcomeAccepted)

	terms := readData(t, ctx, conn, clauseKey+".terms")
	if got, _ := terms["validFrom"].(string); got != "2026-09-08T00:00:00Z" {
		t.Fatalf("terms.validFrom = %q, want unchanged 2026-09-08T00:00:00Z", got)
	}
	if got, _ := terms["validUntil"].(string); got != "2027-02-20T00:00:00Z" {
		t.Fatalf("terms.validUntil = %q, want the recorded moveOutAt 2027-02-20T00:00:00Z", got)
	}
	if got, _ := terms["amountCents"].(float64); got != 240000 {
		t.Fatalf("terms.amountCents = %v, want 240000 preserved through the shortening", terms["amountCents"])
	}
	status := readData(t, ctx, conn, clauseKey+".status")
	if got, _ := status["chargeValidUntil"].(string); got != "2026-12-08T00:00:00Z" {
		t.Fatalf("status.chargeValidUntil = %q, want the recorded due preserved", got)
	}
	if got, _ := status["state"].(string); got != "active" {
		t.Fatalf("status.state = %q, want active — the recorded due is still ahead of the shortened validUntil", got)
	}
}

// TestShortenClauseTerm_RecordedDuePastMoveOut_Completes — the recorded due
// date has already reached the new (shortened) validUntil: the clause
// completes, the same mark BackfillClauseTerm and DebitAccount leave for the
// identical fact.
func TestShortenClauseTerm_RecordedDuePastMoveOut_Completes(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "shorten2")
	clauseKey, leaseKey := termedClauseFixture(t, ctx, conn, cp, cons, "BBLEASESHRTNTRMHJKMB", "shrt02",
		"2026-09-08T00:00:00Z", "2027-09-08T00:00:00Z", "2027-03-08T00:00:00Z")
	seedNotice(t, ctx, conn, leaseKey, "2027-02-20T00:00:00Z")

	submitShortenClauseTerm(t, ctx, conn, cp, cons, "shortenterm00000002", clauseKey, leaseKey, processor.OutcomeAccepted)

	terms := readData(t, ctx, conn, clauseKey+".terms")
	if got, _ := terms["validUntil"].(string); got != "2027-02-20T00:00:00Z" {
		t.Fatalf("terms.validUntil = %q, want 2027-02-20T00:00:00Z", got)
	}
	status := readData(t, ctx, conn, clauseKey+".status")
	if got, _ := status["state"].(string); got != "completed" {
		t.Fatalf("status.state = %q, want completed — the recorded due already reached the shortened validUntil", got)
	}
	if _, ok := status["completedAt"]; !ok {
		t.Fatalf("completedAt must be stamped, got %v", status)
	}
	if got, _ := status["chargeValidUntil"].(string); got != "2027-03-08T00:00:00Z" {
		t.Fatalf("status.chargeValidUntil = %q, want the recorded due preserved", got)
	}
}

// TestShortenClauseTerm_MoveOutBeforeValidFrom_CollapsesToValidFrom — a
// move-out recorded before the term even starts (a renewal clause whose term
// starts after the move-out): validUntil collapses to validFrom, the
// recorded fact that the clause bills nothing, and it completes at once.
func TestShortenClauseTerm_MoveOutBeforeValidFrom_CollapsesToValidFrom(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "shorten3")
	clauseKey, leaseKey := termedClauseFixture(t, ctx, conn, cp, cons, "BBLEASESHRTNTRMHJKMC", "shrt03",
		"2027-09-08T00:00:00Z", "2028-09-08T00:00:00Z", "")
	seedNotice(t, ctx, conn, leaseKey, "2027-02-20T00:00:00Z")

	submitShortenClauseTerm(t, ctx, conn, cp, cons, "shortenterm00000003", clauseKey, leaseKey, processor.OutcomeAccepted)

	terms := readData(t, ctx, conn, clauseKey+".terms")
	if got, _ := terms["validUntil"].(string); got != "2027-09-08T00:00:00Z" {
		t.Fatalf("terms.validUntil = %q, want it collapsed to validFrom 2027-09-08T00:00:00Z", got)
	}
	status := readData(t, ctx, conn, clauseKey+".status")
	if got, _ := status["state"].(string); got != "completed" {
		t.Fatalf("status.state = %q, want completed — never charged, due defaults to validFrom which now equals validUntil", got)
	}
}

// readRevision returns the Core KV revision of a key — the strongest proof a
// no-op left the document untouched: content equality alone would not catch
// an update that rewrites the SAME values (still a wasted, racy write with
// an OCC pin that could conflict a genuine concurrent writer for nothing).
func readRevision(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) uint64 {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	return entry.Revision
}

// TestShortenClauseTerm_ValidUntilAlreadyAtOrBeforeMoveOut_NoOp — the term
// already ends at or before the recorded move-out (an earlier pass already
// shortened it, or it never overran): idempotent no-op, zero mutations —
// pinned at the KV revision, not just content, so a regression that rewrites
// the same values still fails this test even though the committed data looks
// unchanged. Deleting the no-op branch entirely (proven by revert in a /tmp
// copy) fails this on the revision check alone.
func TestShortenClauseTerm_ValidUntilAlreadyAtOrBeforeMoveOut_NoOp(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "shorten4")
	clauseKey, leaseKey := termedClauseFixture(t, ctx, conn, cp, cons, "BBLEASESHRTNTRMHJKMD", "shrt04",
		"2026-09-08T00:00:00Z", "2027-02-20T00:00:00Z", "2026-12-08T00:00:00Z")
	seedNotice(t, ctx, conn, leaseKey, "2027-02-20T00:00:00Z")
	before := readData(t, ctx, conn, clauseKey+".terms")
	beforeStatus := readData(t, ctx, conn, clauseKey+".status")
	beforeTermsRev := readRevision(t, ctx, conn, clauseKey+".terms")
	beforeStatusRev := readRevision(t, ctx, conn, clauseKey+".status")

	submitShortenClauseTerm(t, ctx, conn, cp, cons, "shortenterm00000004", clauseKey, leaseKey, processor.OutcomeAccepted)

	after := readData(t, ctx, conn, clauseKey+".terms")
	if after["validUntil"] != before["validUntil"] {
		t.Fatalf("a no-op must leave .terms untouched, got %v want %v", after, before)
	}
	afterStatus := readData(t, ctx, conn, clauseKey+".status")
	if afterStatus["state"] != beforeStatus["state"] || afterStatus["chargeValidUntil"] != beforeStatus["chargeValidUntil"] {
		t.Fatalf("a no-op must leave .status untouched, got %v want %v", afterStatus, beforeStatus)
	}
	if got := readRevision(t, ctx, conn, clauseKey+".terms"); got != beforeTermsRev {
		t.Fatalf(".terms revision = %d, want unchanged %d — a no-op must not write, even a same-valued one", got, beforeTermsRev)
	}
	if got := readRevision(t, ctx, conn, clauseKey+".status"); got != beforeStatusRev {
		t.Fatalf(".status revision = %d, want unchanged %d — a no-op must not write, even a same-valued one", got, beforeStatusRev)
	}
}

// TestShortenClauseTerm_LivelockRedispatch_NoOpAfterCollapse is the
// end-to-end pin for the collapsed-term case: a move-out recorded before the
// term's own start collapses validUntil to validFrom on the first dispatch —
// still well after moveOutAt, so the no-op guard must compare against the
// computed cap (new_until), never against the raw moveOutAt, or it would
// never recognize the term as settled. The SECOND dispatch, against the
// now-collapsed term, must be the clean no-op — pinned at the KV revision.
func TestShortenClauseTerm_LivelockRedispatch_NoOpAfterCollapse(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "shorten9")
	clauseKey, leaseKey := termedClauseFixture(t, ctx, conn, cp, cons, "BBLEASESHRTNTRMHJKMK", "shrt09",
		"2027-09-08T00:00:00Z", "2028-09-08T00:00:00Z", "")
	seedNotice(t, ctx, conn, leaseKey, "2027-02-20T00:00:00Z")

	submitShortenClauseTerm(t, ctx, conn, cp, cons, "shortenterm00000009", clauseKey, leaseKey, processor.OutcomeAccepted)
	collapsed := readData(t, ctx, conn, clauseKey+".terms")
	if got, _ := collapsed["validUntil"].(string); got != "2027-09-08T00:00:00Z" {
		t.Fatalf("fixture sanity: want the term collapsed to validFrom 2027-09-08T00:00:00Z, got validUntil %q", got)
	}
	termsRevAfterCollapse := readRevision(t, ctx, conn, clauseKey+".terms")
	statusRevAfterCollapse := readRevision(t, ctx, conn, clauseKey+".status")

	submitShortenClauseTerm(t, ctx, conn, cp, cons, "shortentermRedispatch1", clauseKey, leaseKey, processor.OutcomeAccepted)

	if got := readRevision(t, ctx, conn, clauseKey+".terms"); got != termsRevAfterCollapse {
		t.Fatalf(".terms revision = %d, want unchanged %d — the re-dispatch against a collapsed term must be a clean no-op, not a livelocked rewrite", got, termsRevAfterCollapse)
	}
	if got := readRevision(t, ctx, conn, clauseKey+".status"); got != statusRevAfterCollapse {
		t.Fatalf(".status revision = %d, want unchanged %d — a livelock would re-stamp completedAt on every reclaim", got, statusRevAfterCollapse)
	}
}

// TestShortenClauseTerm_Untermed_Rejected — an untermed clause has no term to
// shorten; BackfillClauseTerm runs first.
func TestShortenClauseTerm_Untermed_Rejected(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "shorten5")
	leaseKey := seedLease(t, ctx, conn, "BBLEASESHRTNTRMHJKME")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctshrt05", leaseKey)
	clauseKey := submitCreateClause(t, ctx, conn, cp, cons, "createclauseshrt05", leaseKey, acctKey,
		`,"amountCents":240000,"period":"monthly"`, processor.OutcomeAccepted)
	seedNotice(t, ctx, conn, leaseKey, "2027-02-20T00:00:00Z")

	submitShortenClauseTerm(t, ctx, conn, cp, cons, "shortenterm00000005", clauseKey, leaseKey, processor.OutcomeRejected)

	terms := readData(t, ctx, conn, clauseKey+".terms")
	if _, ok := terms["validUntil"]; ok {
		t.Fatalf("a refused shorten must not mint a term, got %v", terms)
	}
}

// TestShortenClauseTerm_NoNotice_Rejected — the lease carries no .notice
// aspect: nothing to shorten to.
func TestShortenClauseTerm_NoNotice_Rejected(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "shorten6")
	clauseKey, leaseKey := termedClauseFixture(t, ctx, conn, cp, cons, "BBLEASESHRTNTRMHJKMF", "shrt06",
		"2026-09-08T00:00:00Z", "2027-09-08T00:00:00Z", "")

	submitShortenClauseTerm(t, ctx, conn, cp, cons, "shortenterm00000006", clauseKey, leaseKey, processor.OutcomeRejected)

	terms := readData(t, ctx, conn, clauseKey+".terms")
	if got, _ := terms["validUntil"].(string); got != "2027-09-08T00:00:00Z" {
		t.Fatalf("a refused shorten must leave .terms untouched, got validUntil %q", got)
	}
}

// TestShortenClauseTerm_ClauseGovernsAnotherLease_Rejected — the clause's own
// governs link names a DIFFERENT lease than the one whose notice is being
// read: refused, never trusts the payload's leaseAppKey blindly.
func TestShortenClauseTerm_ClauseGovernsAnotherLease_Rejected(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "shorten7")
	clauseKey, actualLeaseKey := termedClauseFixture(t, ctx, conn, cp, cons, "BBLEASESHRTNTRMHJKMG", "shrt07",
		"2026-09-08T00:00:00Z", "2027-09-08T00:00:00Z", "")
	otherLeaseKey := seedLease(t, ctx, conn, "BBLEASESHRTNTRMHJKMH")
	seedNotice(t, ctx, conn, otherLeaseKey, "2027-02-20T00:00:00Z")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("shortenterm00000007"),
		Lane:          processor.LaneDefault,
		OperationType: "ShortenClauseTerm",
		Actor:         scActorKey,
		SubmittedAt:   "2026-10-05T12:00:00Z",
		Class:         "clause",
		Payload:       json.RawMessage(`{"clauseKey":"` + clauseKey + `","leaseAppKey":"` + otherLeaseKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{clauseKey, clauseKey + ".terms", otherLeaseKey, otherLeaseKey + ".notice"},
			OptionalReads: []string{clauseKey + ".status"},
			Enumerations:  []processor.EnumerationHint{{Hub: clauseKey, Relation: "governs", Direction: "out"}},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	terms := readData(t, ctx, conn, clauseKey+".terms")
	if got, _ := terms["validUntil"].(string); got != "2027-09-08T00:00:00Z" {
		t.Fatalf("a refused shorten must leave .terms untouched, got validUntil %q", got)
	}
	_ = actualLeaseKey
}

// TestShortenClauseTerm_UndeclaredReads_Rejected — an empty ContextHint (no
// declared reads at all): the op refuses rather than lazily reading .terms or
// .notice, the same fail-closed posture BackfillClauseTerm's undeclared
// .status vector pins.
func TestShortenClauseTerm_UndeclaredReads_Rejected(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "shorten8")
	clauseKey, leaseKey := termedClauseFixture(t, ctx, conn, cp, cons, "BBLEASESHRTNTRMHJKMJ", "shrt08",
		"2026-09-08T00:00:00Z", "2027-09-08T00:00:00Z", "")
	seedNotice(t, ctx, conn, leaseKey, "2027-02-20T00:00:00Z")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("shortenterm00000008"),
		Lane:          processor.LaneDefault,
		OperationType: "ShortenClauseTerm",
		Actor:         scActorKey,
		SubmittedAt:   "2026-10-05T12:00:00Z",
		Class:         "clause",
		Payload:       json.RawMessage(`{"clauseKey":"` + clauseKey + `","leaseAppKey":"` + leaseKey + `"}`),
		ContextHint:   &processor.ContextHint{},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	terms := readData(t, ctx, conn, clauseKey+".terms")
	if got, _ := terms["validUntil"].(string); got != "2027-09-08T00:00:00Z" {
		t.Fatalf("a refused shorten must leave .terms untouched, got validUntil %q", got)
	}
}

// TestShortenClauseTerm_StatusDeclaredButNotOptional_Rejected — the clause's
// .status aspect genuinely EXISTS in KV, and clause/terms/lease/notice are
// all declared, but the dispatch omits .status from OptionalReads: the op
// refuses rather than treat "not hydrated" as "never charged" (the
// TestBackfillClauseTerm_UndeclaredStatus_Rejected shape — a due date read
// from nothing would rewind a charged clause's completion state).
func TestShortenClauseTerm_StatusDeclaredButNotOptional_Rejected(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "shorten10")
	clauseKey, leaseKey := termedClauseFixture(t, ctx, conn, cp, cons, "BBLEASESHRTNTRMHJKML", "shrt10",
		"2026-09-08T00:00:00Z", "2027-09-08T00:00:00Z", "2026-12-08T00:00:00Z")
	seedNotice(t, ctx, conn, leaseKey, "2027-02-20T00:00:00Z")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("shortenterm0000000A"),
		Lane:          processor.LaneDefault,
		OperationType: "ShortenClauseTerm",
		Actor:         scActorKey,
		SubmittedAt:   "2026-10-05T12:00:00Z",
		Class:         "clause",
		Payload:       json.RawMessage(`{"clauseKey":"` + clauseKey + `","leaseAppKey":"` + leaseKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:        []string{clauseKey, clauseKey + ".terms", leaseKey, leaseKey + ".notice"},
			Enumerations: []processor.EnumerationHint{{Hub: clauseKey, Relation: "governs", Direction: "out"}},
			// Deliberately no OptionalReads for .status, though it exists.
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	terms := readData(t, ctx, conn, clauseKey+".terms")
	if got, _ := terms["validUntil"].(string); got != "2027-09-08T00:00:00Z" {
		t.Fatalf("a refused shorten must leave .terms untouched, got validUntil %q", got)
	}
}

// TestShortenClauseTermScript_PinsTheHydratedRevisions proves the OCC pin at
// the script runner, where a mutation's expectedRevision is observable (the
// committed document never carries it): both the .terms and .status updates
// assert EXACTLY the revision the declared reads hydrated (the
// TestEndTenancyScript_PinsTheHydratedTenancyRevision shape). A concurrent
// BackfillClauseTerm or DebitAccount landing between this op's hydration and
// its commit must RevisionConflict, not be silently overwritten.
func TestShortenClauseTermScript_PinsTheHydratedRevisions(t *testing.T) {
	const clauseKey = "vtx.clause.BBshrtnScrHJKMNPQRST"
	const leaseKey = "vtx.leaseapp.BBshrtnLeaseScrHJKMQ"
	const termsRevision = uint64(5)
	const statusRevision = uint64(9)
	script := findClauseDDLScript(t)
	linkKey := "lnk.clause." + clauseKey[len("vtx.clause."):] + ".governs.leaseapp." + leaseKey[len("vtx.leaseapp."):]

	result, err := processor.NewStarlarkRunner(0, 0).Run(context.Background(), processor.ScriptContext{
		Operation: &processor.OperationEnvelope{
			RequestID:     "Hj4kPmRtw9nbCxz5vQ3a",
			Lane:          processor.LaneDefault,
			OperationType: "ShortenClauseTerm",
			Actor:         scActorKey,
			SubmittedAt:   "2026-10-05T12:00:00Z",
			Payload:       json.RawMessage(`{"clauseKey":"` + clauseKey + `","leaseAppKey":"` + leaseKey + `"}`),
			ContextHint: &processor.ContextHint{
				Reads:         []string{clauseKey, clauseKey + ".terms", leaseKey, leaseKey + ".notice"},
				OptionalReads: []string{clauseKey + ".status"},
			},
		},
		Hydrated: map[string]processor.VertexDoc{
			clauseKey: {Key: clauseKey, Class: "clause", Data: map[string]any{}, Revision: 1},
			clauseKey + ".terms": {Key: clauseKey + ".terms", Class: "clauseTerms", VertexKey: clauseKey, LocalName: "terms",
				Data: map[string]any{"kind": "computational", "period": "monthly", "conditioned": false, "amountCents": 240000.0,
					"validFrom": "2026-09-08T00:00:00Z", "validUntil": "2027-09-08T00:00:00Z"},
				Revision: termsRevision},
			clauseKey + ".status": {Key: clauseKey + ".status", Class: "clauseStatus", VertexKey: clauseKey, LocalName: "status",
				Data: map[string]any{"state": "active", "chargeValidUntil": "2026-12-08T00:00:00Z"}, Revision: statusRevision},
			leaseKey: {Key: leaseKey, Class: "leaseapp", Data: map[string]any{}, Revision: 1},
			leaseKey + ".notice": {Key: leaseKey + ".notice", Class: "tenancyNotice", VertexKey: leaseKey, LocalName: "notice",
				Data: map[string]any{"moveOutAt": "2027-02-20T00:00:00Z", "givenAt": "2026-10-01T00:00:00Z", "givenBy": "tenant"}, Revision: 1},
		},
		LinkLister: scShortenLinkLister{links: []processor.LinkDoc{
			{Key: linkKey, Class: "governs", SourceVertex: clauseKey, TargetVertex: leaseKey, Revision: 1},
		}},
		DDLLookup:    map[string]processor.MetaVertex{},
		ScriptSource: script,
		ScriptClass:  "clause",
	})
	if err != nil {
		t.Fatalf("ShortenClauseTerm script: %v", err)
	}
	if len(result.Mutations) != 2 {
		t.Fatalf("want exactly two mutations (.terms + .status), got %d: %+v", len(result.Mutations), result.Mutations)
	}
	var termsMut, statusMut *processor.MutationOp
	for i := range result.Mutations {
		m := &result.Mutations[i]
		switch m.Key {
		case clauseKey + ".terms":
			termsMut = m
		case clauseKey + ".status":
			statusMut = m
		}
	}
	if termsMut == nil || statusMut == nil {
		t.Fatalf("expected mutations for .terms and .status, got %+v", result.Mutations)
	}
	if termsMut.ExpectedRevision == nil || *termsMut.ExpectedRevision != termsRevision {
		t.Fatalf(".terms expectedRevision = %v, want %d", termsMut.ExpectedRevision, termsRevision)
	}
	if statusMut.ExpectedRevision == nil || *statusMut.ExpectedRevision != statusRevision {
		t.Fatalf(".status expectedRevision = %v, want %d", statusMut.ExpectedRevision, statusRevision)
	}
}

// TestShortenClauseTermScript_CollapsedClause_NoMutationsNoEvent is the
// script-level pin for the collapsed-term no-op: dispatched against a clause
// whose term has already collapsed to validFrom (validUntil == validFrom,
// still > moveOutAt — a shape the no-op guard must catch by comparing
// against the computed cap, not the raw moveOutAt), the script returns zero
// mutations AND zero events. Deleting the no-op branch (proven by revert in
// a /tmp copy) makes both non-empty.
func TestShortenClauseTermScript_CollapsedClause_NoMutationsNoEvent(t *testing.T) {
	const clauseKey = "vtx.clause.BBshrtnScrHJKMNPQRSU"
	const leaseKey = "vtx.leaseapp.BBshrtnLeaseScrHJKMR"
	script := findClauseDDLScript(t)
	linkKey := "lnk.clause." + clauseKey[len("vtx.clause."):] + ".governs.leaseapp." + leaseKey[len("vtx.leaseapp."):]
	const collapsedAt = "2027-09-08T00:00:00Z" // validFrom == validUntil, already collapsed
	const moveOutAt = "2027-02-20T00:00:00Z"   // well before collapsedAt — validUntil > moveOutAt still holds

	result, err := processor.NewStarlarkRunner(0, 0).Run(context.Background(), processor.ScriptContext{
		Operation: &processor.OperationEnvelope{
			RequestID:     "Hj4kPmRtw9nbCxz5vQ3b",
			Lane:          processor.LaneDefault,
			OperationType: "ShortenClauseTerm",
			Actor:         scActorKey,
			SubmittedAt:   "2026-10-06T12:00:00Z",
			Payload:       json.RawMessage(`{"clauseKey":"` + clauseKey + `","leaseAppKey":"` + leaseKey + `"}`),
			ContextHint: &processor.ContextHint{
				Reads:         []string{clauseKey, clauseKey + ".terms", leaseKey, leaseKey + ".notice"},
				OptionalReads: []string{clauseKey + ".status"},
			},
		},
		Hydrated: map[string]processor.VertexDoc{
			clauseKey: {Key: clauseKey, Class: "clause", Data: map[string]any{}, Revision: 1},
			clauseKey + ".terms": {Key: clauseKey + ".terms", Class: "clauseTerms", VertexKey: clauseKey, LocalName: "terms",
				Data: map[string]any{"kind": "computational", "period": "monthly", "conditioned": false, "amountCents": 240000.0,
					"validFrom": collapsedAt, "validUntil": collapsedAt},
				Revision: 6},
			clauseKey + ".status": {Key: clauseKey + ".status", Class: "clauseStatus", VertexKey: clauseKey, LocalName: "status",
				Data: map[string]any{"state": "completed", "completedAt": "2026-10-05T12:00:00Z", "chargeValidUntil": collapsedAt}, Revision: 4},
			leaseKey: {Key: leaseKey, Class: "leaseapp", Data: map[string]any{}, Revision: 1},
			leaseKey + ".notice": {Key: leaseKey + ".notice", Class: "tenancyNotice", VertexKey: leaseKey, LocalName: "notice",
				Data: map[string]any{"moveOutAt": moveOutAt, "givenAt": "2026-10-01T00:00:00Z", "givenBy": "tenant"}, Revision: 1},
		},
		LinkLister: scShortenLinkLister{links: []processor.LinkDoc{
			{Key: linkKey, Class: "governs", SourceVertex: clauseKey, TargetVertex: leaseKey, Revision: 1},
		}},
		DDLLookup:    map[string]processor.MetaVertex{},
		ScriptSource: script,
		ScriptClass:  "clause",
	})
	if err != nil {
		t.Fatalf("ShortenClauseTerm script: %v", err)
	}
	if len(result.Mutations) != 0 {
		t.Fatalf("a re-dispatch against an already-collapsed term must produce zero mutations, got %+v", result.Mutations)
	}
	if len(result.Events) != 0 {
		t.Fatalf("a re-dispatch against an already-collapsed term must emit zero events, got %+v", result.Events)
	}
}

// TestShortenClauseTermScript_AlreadyCompletedClause_PreservesCompletedAt —
// a clause DebitAccount (or an earlier ShortenClauseTerm pass) already
// marked completed, with a real completedAt, is shortened further (a
// dispatch that reaches the script directly — the lens itself now excludes a
// completed clause from candidacy, so this proves the script's OWN
// defense-in-depth for a stale in-flight dispatch): validUntil still caps at
// the move-out, but the ORIGINAL completedAt is kept, never re-stamped to
// "now".
func TestShortenClauseTermScript_AlreadyCompletedClause_PreservesCompletedAt(t *testing.T) {
	const clauseKey = "vtx.clause.BBshrtnScrHJKMNPQRSV"
	const leaseKey = "vtx.leaseapp.BBshrtnLeaseScrHJKMS"
	script := findClauseDDLScript(t)
	linkKey := "lnk.clause." + clauseKey[len("vtx.clause."):] + ".governs.leaseapp." + leaseKey[len("vtx.leaseapp."):]
	const originalCompletedAt = "2026-11-01T09:00:00Z"

	result, err := processor.NewStarlarkRunner(0, 0).Run(context.Background(), processor.ScriptContext{
		Operation: &processor.OperationEnvelope{
			RequestID:     "Hj4kPmRtw9nbCxz5vQ3c",
			Lane:          processor.LaneDefault,
			OperationType: "ShortenClauseTerm",
			Actor:         scActorKey,
			SubmittedAt:   "2026-12-01T12:00:00Z",
			Payload:       json.RawMessage(`{"clauseKey":"` + clauseKey + `","leaseAppKey":"` + leaseKey + `"}`),
			ContextHint: &processor.ContextHint{
				Reads:         []string{clauseKey, clauseKey + ".terms", leaseKey, leaseKey + ".notice"},
				OptionalReads: []string{clauseKey + ".status"},
			},
		},
		Hydrated: map[string]processor.VertexDoc{
			clauseKey: {Key: clauseKey, Class: "clause", Data: map[string]any{}, Revision: 1},
			clauseKey + ".terms": {Key: clauseKey + ".terms", Class: "clauseTerms", VertexKey: clauseKey, LocalName: "terms",
				Data: map[string]any{"kind": "computational", "period": "monthly", "conditioned": false, "amountCents": 240000.0,
					"validFrom": "2026-09-08T00:00:00Z", "validUntil": "2027-09-08T00:00:00Z"},
				Revision: 3},
			// DebitAccount's own completion: fully billed to the ORIGINAL
			// validUntil, stamped with its own completedAt.
			clauseKey + ".status": {Key: clauseKey + ".status", Class: "clauseStatus", VertexKey: clauseKey, LocalName: "status",
				Data: map[string]any{"state": "completed", "completedAt": originalCompletedAt, "chargeValidUntil": "2027-09-08T00:00:00Z"}, Revision: 8},
			leaseKey: {Key: leaseKey, Class: "leaseapp", Data: map[string]any{}, Revision: 1},
			leaseKey + ".notice": {Key: leaseKey + ".notice", Class: "tenancyNotice", VertexKey: leaseKey, LocalName: "notice",
				Data: map[string]any{"moveOutAt": "2027-02-20T00:00:00Z", "givenAt": "2026-10-01T00:00:00Z", "givenBy": "tenant"}, Revision: 1},
		},
		LinkLister: scShortenLinkLister{links: []processor.LinkDoc{
			{Key: linkKey, Class: "governs", SourceVertex: clauseKey, TargetVertex: leaseKey, Revision: 1},
		}},
		DDLLookup:    map[string]processor.MetaVertex{},
		ScriptSource: script,
		ScriptClass:  "clause",
	})
	if err != nil {
		t.Fatalf("ShortenClauseTerm script: %v", err)
	}
	if len(result.Mutations) != 2 {
		t.Fatalf("want exactly two mutations (.terms + .status), got %d: %+v", len(result.Mutations), result.Mutations)
	}
	var termsData, statusData map[string]any
	for _, m := range result.Mutations {
		doc, _ := m.Document["data"].(map[string]any)
		switch m.Key {
		case clauseKey + ".terms":
			termsData = doc
		case clauseKey + ".status":
			statusData = doc
		}
	}
	if got, _ := termsData["validUntil"].(string); got != "2027-02-20T00:00:00Z" {
		t.Fatalf("terms.validUntil = %q, want capped at the recorded moveOutAt 2027-02-20T00:00:00Z", got)
	}
	if got, _ := statusData["state"].(string); got != "completed" {
		t.Fatalf("status.state = %q, want completed (unchanged)", got)
	}
	if got, _ := statusData["completedAt"].(string); got != originalCompletedAt {
		t.Fatalf("status.completedAt = %q, want the ORIGINAL %q preserved, never re-stamped", got, originalCompletedAt)
	}
}
