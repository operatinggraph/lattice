// RecordApplicationLoss op vectors through the real install + Processor
// pipeline (design loftspace-recorded-application-loss-design.md §2.3), plus
// the state-table obligations on the two other writers of .decision
// (DecideLeaseApplication, WithdrawLeaseApplication). External test package,
// mirroring end_tenancy_ops_test.go's shape and helpers.
package leasesigning_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	leasesigning "github.com/operatinggraph/lattice/packages/lease-signing"
)

// ralEnvelope builds a RecordApplicationLoss envelope submitted at the given
// instant with the reads the leaseApplicationComplete target routes for the
// missing_lossRecorded gap — the application as a required read, its
// .decision as an optional one — plus the appliesToUnit enumeration the
// script walks, resolved from the op-meta's own declaration (permissions.go)
// with its {payload.leaseAppKey} hub bound to the application, which is the
// one substitution DeclaredEnumerations leaves to the caller.
func ralEnvelope(label, actor, appKey, submittedAt string) *processor.OperationEnvelope {
	hint := &processor.ContextHint{
		Reads:         []string{appKey},
		OptionalReads: []string{appKey + ".decision"},
	}
	hints, skipped := testutil.DeclaredEnumerationsWithSkips("RecordApplicationLoss", actor, leasesigning.OpMetas())
	for _, hub := range skipped {
		if hub != "{payload.leaseAppKey}" {
			panic("RecordApplicationLoss declares an enumeration hub this fixture cannot bind: " + hub)
		}
		hints = append(hints, processor.EnumerationHint{Hub: appKey, Relation: "appliesToUnit", Direction: "out"})
	}
	hint.Enumerations = hints
	return &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "RecordApplicationLoss",
		Actor:         actor,
		SubmittedAt:   submittedAt,
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + appKey + `"}`),
		ContextHint:   hint,
	}
}

// ralLosingApplication seeds an undecided application whose unit has since
// leased to somebody else — the exact shape missing_lossRecorded opens on.
func ralLosingApplication(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, applicantSeed string) (appKey, applicantKey, unitKey string) {
	t.Helper()
	applicantKey = seedApplicant(t, ctx, conn, applicantSeed)
	appKey = createApplication(t, ctx, conn, cp, cons, applicantKey)
	unitKey = unitKeyFor(applicantKey)
	setUnitLeasedStatus(t, ctx, conn, unitKey)
	return appKey, applicantKey, unitKey
}

func ralDecisionRevision(t *testing.T, ctx context.Context, conn *substrate.Conn, appKey string) uint64 {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, appKey+".decision")
	if err != nil {
		t.Fatalf("KVGet .decision: %v", err)
	}
	return entry.Revision
}

// TestRecordApplicationLoss_RecordsLostOnAnUndecidedApplicationWhoseUnitLeased
// is the positive vector: the loss is recorded as .decision {value: lost,
// decidedAt: the submission instant} — no reason, no .decidedProfileSnapshot
// (nobody decided this application) — and leaseapp.applicationLost names the
// application and the unit it lost.
func TestRecordApplicationLoss_RecordsLostOnAnUndecidedApplicationWhoseUnitLeased(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "recloss")

	appKey, _, unitKey := ralLosingApplication(t, ctx, conn, cp, cons, "BBrecgapA1ntHJKMNPQR")
	if keyExists(t, ctx, conn, appKey+".decision") {
		t.Fatalf("the fixture must start undecided")
	}

	env := ralEnvelope("recLoss0001", lsActorKey, appKey, "2026-09-14T13:42:00Z")
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply %+v)", outcome, reply)
	}
	if reply.PrimaryKey != appKey {
		t.Fatalf("primaryKey = %q, want the application %q", reply.PrimaryKey, appKey)
	}

	ddoc := readDoc(t, ctx, conn, appKey+".decision")
	ddata, _ := ddoc["data"].(map[string]any)
	if got, _ := ddata["value"].(string); got != "lost" {
		t.Fatalf("decision.value = %q, want lost", got)
	}
	if got, _ := ddata["decidedAt"].(string); got != "2026-09-14T13:42:00Z" {
		t.Fatalf("decision.decidedAt = %q, want the submission instant 2026-09-14T13:42:00Z", got)
	}
	if _, has := ddata["reason"]; has {
		t.Fatalf("a recorded loss carries no reason, got %v", ddata["reason"])
	}
	if got, _ := ddoc["isDeleted"].(bool); got {
		t.Fatalf("RecordApplicationLoss must never tombstone the aspect")
	}
	if keyExists(t, ctx, conn, appKey+".decidedProfileSnapshot") {
		t.Fatalf("a recorded loss stamps no .decidedProfileSnapshot — nobody decided this application")
	}

	ev := findEmittedEvent(t, ctx, conn, env.RequestID, "leaseapp.applicationLost")
	if got, _ := ev["leaseAppKey"].(string); got != appKey {
		t.Fatalf("applicationLost.leaseAppKey = %q, want %q", got, appKey)
	}
	if got, _ := ev["unitKey"].(string); got != unitKey {
		t.Fatalf("applicationLost.unitKey = %q, want %q", got, unitKey)
	}
}

// TestRecordApplicationLoss_AnyRecordedDecisionIsANoOp: an at-least-once
// re-dispatch of an already-lost application, and a landlord decision (either
// value) that landed before the dispatch, are each Accepted with ZERO
// mutations — the .decision revision does not move, no event is emitted, and
// the reply carries no primaryKey (an empty write footprint has nothing to
// validate one against). A refusal here would burn Weaver's retry budget on a
// race the lens has already closed.
func TestRecordApplicationLoss_AnyRecordedDecisionIsANoOp(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "reclossidem")

	// lost: the re-dispatch.
	lostApp, _, _ := ralLosingApplication(t, ctx, conn, cp, cons, "BBrecgapB2ntHJKMNPQS")
	first := ralEnvelope("recLoss0002a", lsActorKey, lostApp, "2026-09-14T13:00:00Z")
	testutil.PublishOp(t, conn, first)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// declined: the landlord refused it before the unit leased to a sibling.
	declinedApp, _, declinedUnit := ralLosingApplication(t, ctx, conn, cp, cons, "BBrecgapC3ntHJKMNPQT")
	decide(t, ctx, conn, cp, cons, "recLossDecl", declinedApp, "declined", declinedUnit, "2026-09-14T12:00:00Z", processor.OutcomeAccepted)

	// approved: the winner, whose own unit is the leased one.
	approvedApp, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBrecgapD4ntHJKMNPQU")
	setUnitLeasedStatus(t, ctx, conn, unitKeyFor("vtx.identity.BBrecgapD4ntHJKMNPQU"))

	for _, tc := range []struct{ name, appKey, want string }{
		{"already lost", lostApp, "lost"},
		{"landlord declined", declinedApp, "declined"},
		{"landlord approved (the winner)", approvedApp, "approved"},
	} {
		before := ralDecisionRevision(t, ctx, conn, tc.appKey)
		env := ralEnvelope("recLoss0002"+tc.want, lsActorKey, tc.appKey, "2026-09-15T00:00:00Z")
		outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
		if outcome != processor.OutcomeAccepted {
			t.Fatalf("%s: outcome = %v, want Accepted (idempotent), reply %+v", tc.name, outcome, reply)
		}
		if len(reply.Revisions) != 0 {
			t.Fatalf("%s: a no-op RecordApplicationLoss must commit no keys, got revisions %v", tc.name, reply.Revisions)
		}
		if reply.PrimaryKey != "" {
			t.Fatalf("%s: a no-op carries no primaryKey, got %q", tc.name, reply.PrimaryKey)
		}
		if got := ralDecisionRevision(t, ctx, conn, tc.appKey); got != before {
			t.Fatalf("%s: a recorded decision must not be rewritten (revision %d → %d)", tc.name, before, got)
		}
		ddoc := readDoc(t, ctx, conn, tc.appKey+".decision")
		if got, _ := ddoc["data"].(map[string]any)["value"].(string); got != tc.want {
			t.Fatalf("%s: decision.value = %q after the no-op, want %q untouched", tc.name, got, tc.want)
		}
	}
}

// TestRecordApplicationLoss_UnitNotLeased_Refused: write-path honesty — the op
// re-verifies the premise from state rather than trusting the dispatching row,
// so an operator cannot mark an application lost against a unit that is
// available (or that has no listing at all). The refusal names the unit and
// the listing's status.
func TestRecordApplicationLoss_UnitNotLeased_Refused(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "reclossavail")

	// An available unit: the listing carries status = available.
	applicantKey := seedApplicant(t, ctx, conn, "BBrecgapE5ntHJKMNPQV")
	appKey := createApplication(t, ctx, conn, cp, cons, applicantKey)
	unitKey := unitKeyFor(applicantKey)
	listing := map[string]any{
		"class": "listing", "isDeleted": false, "vertexKey": unitKey, "localName": "listing",
		"data": map[string]any{"availableFrom": "2026-08-01T00:00:00Z", "leaseTermMonths": 12, "rentAmount": 2400, "rentCurrency": "USD", "status": "available"},
	}
	lb, _ := json.Marshal(listing)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, unitKey+".listing", lb); err != nil {
		t.Fatalf("set unit .listing available: %v", err)
	}
	env := ralEnvelope("recLoss0003a", lsActorKey, appKey, "2026-09-14T13:42:00Z")
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("available unit: outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "UnitNotLeased") || !strings.Contains(reply.Error.Message, unitKey) || !strings.Contains(reply.Error.Message, "available") {
		t.Fatalf("available unit: want a UnitNotLeased refusal naming the unit and its status, got %+v", reply.Error)
	}
	if keyExists(t, ctx, conn, appKey+".decision") {
		t.Fatalf("a refused RecordApplicationLoss must write no .decision")
	}

	// A unit with no .listing aspect at all: nothing says leased.
	bareApplicant := seedApplicant(t, ctx, conn, "BBrecgapF6ntHJKMNPQW")
	bareApp := createApplication(t, ctx, conn, cp, cons, bareApplicant)
	bareUnit := unitKeyFor(bareApplicant)
	if err := conn.KVDelete(ctx, testutil.HarnessCoreBucket, bareUnit+".listing"); err != nil {
		t.Fatalf("delete unit .listing: %v", err)
	}
	env = ralEnvelope("recLoss0003b", lsActorKey, bareApp, "2026-09-14T13:42:00Z")
	outcome, reply = testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("no listing: outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "UnitNotLeased") {
		t.Fatalf("no listing: want a UnitNotLeased refusal, got %+v", reply.Error)
	}
	if keyExists(t, ctx, conn, bareApp+".decision") {
		t.Fatalf("a refused RecordApplicationLoss must write no .decision")
	}
}

// TestRecordApplicationLoss_NoUnit_Refused: the unit comes from the
// application's OWN appliesToUnit link, never a payload field. With that link
// tombstoned there is no unit to have lost, and the op refuses NoUnit rather
// than recording a loss against nothing.
func TestRecordApplicationLoss_NoUnit_Refused(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "reclossnounit")

	appKey, _, unitKey := ralLosingApplication(t, ctx, conn, cp, cons, "BBrecgapG7ntHJKMNPQX")
	linkKey := appliesToUnitLinkKey(appKey, unitKey)
	link := readDoc(t, ctx, conn, linkKey)
	link["isDeleted"] = true
	lb, _ := json.Marshal(link)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, linkKey, lb); err != nil {
		t.Fatalf("tombstone appliesToUnit link: %v", err)
	}

	env := ralEnvelope("recLoss0004", lsActorKey, appKey, "2026-09-14T13:42:00Z")
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "NoUnit") {
		t.Fatalf("want a NoUnit refusal, got %+v", reply.Error)
	}
	if keyExists(t, ctx, conn, appKey+".decision") {
		t.Fatalf("a refused RecordApplicationLoss must write no .decision")
	}
}

// TestRecordApplicationLoss_NonOperatorDenied: the grant is operator-only. A
// signed-in landlord (the consumer role, scope=self grants on their own
// decisions) holds no RecordApplicationLoss permission at all, so step 3
// denies before the script runs — recording a loss is never a person-facing
// action.
func TestRecordApplicationLoss_NonOperatorDenied(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "reclossdenied")
	llSetupLandlord(t, ctx, conn)

	appKey, _, unitKey := ralLosingApplication(t, ctx, conn, cp, cons, "BBrecgapH8ntHJKMNPQY")
	llSeedManages(t, ctx, conn, unitKey)

	env := ralEnvelope("recLoss0005", llLandlordKey, appKey, "2026-09-14T13:42:00Z")
	env.AuthContext = &processor.AuthContext{Target: llLandlordKey}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || reply.Error.Code != processor.ErrCodeAuthDenied {
		t.Fatalf("want an AuthDenied rejection (no grant), got %+v", reply.Error)
	}
	if keyExists(t, ctx, conn, appKey+".decision") {
		t.Fatalf("a denied RecordApplicationLoss must write no .decision")
	}
}

// TestRecordApplicationLoss_LostIsTerminalForDecide_NotForWithdraw is the
// state-table obligation on the other two writers of .decision. A recorded
// loss is a recorded decision: DecideLeaseApplication refuses DecisionFinal on
// it (the landlord cannot decide an application whose unit went to someone
// else, even after that unit relists), while WithdrawLeaseApplication still
// accepts it — Withdraw refuses only an approved (executed) lease, and
// withdrawing a lost application is how the applicant frees the
// per-(applicant, unit) guard to re-apply once the unit relists. (SignLease's
// own refusal of a lost application is TestSignLease_RefusesLostApplication.)
func TestRecordApplicationLoss_LostIsTerminalForDecide_NotForWithdraw(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "reclossterminal")

	appKey, applicantKey, unitKey := ralLosingApplication(t, ctx, conn, cp, cons, "BBrecgapJ9ntHJKMNPQZ")
	env := ralEnvelope("recLoss0006", lsActorKey, appKey, "2026-09-14T13:42:00Z")
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	before := ralDecisionRevision(t, ctx, conn, appKey)

	// The unit relists; the landlord tries to decide the lost application (a
	// decline needs no signature, so the DecisionFinal guard is the first
	// refusal reached).
	setListingAspect(t, ctx, conn, unitKey, "2026-08-01T00:00:00Z", 12, 2400)
	hint := decideReadsFor(appKey, unitKey)
	hint.Enumerations = testutil.DeclaredEnumerations("DecideLeaseApplication", lsActorKey, leasesigning.OpMetas())
	decideEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("recLossDecl6"),
		Lane:          processor.LaneDefault,
		OperationType: "DecideLeaseApplication",
		Actor:         lsActorKey,
		SubmittedAt:   "2026-09-16T00:00:00Z",
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + appKey + `","decision":"declined"}`),
		ContextHint:   hint,
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, decideEnv)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("decide after a recorded loss: outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "DecisionFinal") || !strings.Contains(reply.Error.Message, "lost") {
		t.Fatalf("want a DecisionFinal refusal naming the recorded loss, got %+v", reply.Error)
	}
	if got := ralDecisionRevision(t, ctx, conn, appKey); got != before {
		t.Fatalf("a refused decision must not rewrite the recorded loss (revision %d → %d)", before, got)
	}
	ddoc := readDoc(t, ctx, conn, appKey+".decision")
	if got, _ := ddoc["data"].(map[string]any)["value"].(string); got != "lost" {
		t.Fatalf("decision.value = %q after the refused decision, want lost preserved", got)
	}

	withdraw(t, ctx, conn, cp, cons, "recLossWithdraw", appKey, unitKey, applicantKey, processor.OutcomeAccepted)
	if keyExists(t, ctx, conn, appKey) {
		t.Fatalf("a lost application is withdrawable — the leaseapp should be tombstoned")
	}
	if keyExists(t, ctx, conn, guardLinkKey(applicantKey, unitKey)) {
		t.Fatalf("withdrawing a lost application must free the per-(applicant, unit) guard so the applicant can re-apply")
	}
}

// ralLinkLister answers the script's one kv.Links walk — the application's
// appliesToUnit link — at the script runner, where no substrate backs it.
type ralLinkLister struct {
	links []processor.LinkDoc
}

func (l ralLinkLister) ListLinks(_ context.Context, _, _ string, _ int) ([]processor.LinkDoc, string, error) {
	return l.links, "", nil
}

// TestRecordApplicationLossScript_WriteIsCreateOnlyUnderTheDeclaredAbsence
// proves at the script runner what no pipeline observation surfaces: with
// .decision declared as an optional read and known-absent at step 4, the
// .decision write is a CREATE — the CreateOnly assertion the commit path
// conditions on that observed absence (commit_path.go
// absentConditionedCreates) — so a landlord decision landing between this
// op's hydration and its commit conflicts instead of being overwritten with a
// loss, and the re-hydrated retry reads it as decided. An update would win
// that race silently.
func TestRecordApplicationLossScript_WriteIsCreateOnlyUnderTheDeclaredAbsence(t *testing.T) {
	const appKey = "vtx.leaseapp.BBrecgapScrHJKMNPQRS"
	const unitKey = "vtx.unit.BBrecgapScrUJKMNPQRS"
	var script string
	for _, d := range leasesigning.Package.DDLs {
		if d.CanonicalName == "leaseapp" {
			script = d.Script
		}
	}
	if script == "" {
		t.Fatal("leaseapp vertexType DDL not found")
	}
	linkKey := appliesToUnitLinkKey(appKey, unitKey)
	result, err := processor.NewStarlarkRunner(0, 0).Run(context.Background(), processor.ScriptContext{
		Operation: &processor.OperationEnvelope{
			RequestID:     "Hj4kPmRtw9nbCxz5vQ2z",
			Lane:          processor.LaneDefault,
			OperationType: "RecordApplicationLoss",
			Actor:         lsActorKey,
			SubmittedAt:   "2026-09-14T13:42:00Z",
			Payload:       json.RawMessage(`{"leaseAppKey":"` + appKey + `"}`),
			ContextHint: &processor.ContextHint{
				Reads:         []string{appKey},
				OptionalReads: []string{appKey + ".decision"},
			},
		},
		Hydrated: map[string]processor.VertexDoc{
			appKey:  {Key: appKey, Class: "leaseapp", Data: map[string]any{}, Revision: 3},
			unitKey: {Key: unitKey, Class: "location", Data: map[string]any{}, Revision: 1},
			unitKey + ".listing": {Key: unitKey + ".listing", Class: "listing", VertexKey: unitKey, LocalName: "listing",
				Data: map[string]any{"status": "leased"}, Revision: 2},
		},
		KnownAbsent: map[string]struct{}{appKey + ".decision": {}},
		LinkLister: ralLinkLister{links: []processor.LinkDoc{
			{Key: linkKey, Class: "appliesToUnit", SourceVertex: appKey, TargetVertex: unitKey, Revision: 1},
		}},
		DDLLookup:    map[string]processor.MetaVertex{},
		ScriptSource: script,
		ScriptClass:  "leaseapp",
	})
	if err != nil {
		t.Fatalf("RecordApplicationLoss script: %v", err)
	}
	if len(result.Mutations) != 1 {
		t.Fatalf("want exactly one mutation (the .decision write), got %d: %+v", len(result.Mutations), result.Mutations)
	}
	m := result.Mutations[0]
	if m.Op != "create" || m.Key != appKey+".decision" {
		t.Fatalf("mutation = %s %s, want create %s.decision — the declared absence conditions a create, never an update", m.Op, m.Key, appKey)
	}
	data, _ := m.Document["data"].(map[string]any)
	if got, _ := data["value"].(string); got != "lost" {
		t.Fatalf("decision.value = %q, want lost", got)
	}
	if got, _ := data["decidedAt"].(string); got != "2026-09-14T13:42:00Z" {
		t.Fatalf("decision.decidedAt = %q, want the submission instant", got)
	}
	if len(result.Events) != 1 || result.Events[0].Class != "leaseapp.applicationLost" {
		t.Fatalf("want one leaseapp.applicationLost event, got %+v", result.Events)
	}
}
