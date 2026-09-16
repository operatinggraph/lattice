// EndTenancy op vectors through the real install + Processor pipeline (design
// loftspace-lease-term-and-tenancy-end-design.md §2.2), plus the one property no
// pipeline observation surfaces — the OCC pin on the hydrated .tenancy revision
// — proven at the script runner. External test package, mirroring
// renewal_ops_test.go's shape and helpers.
package leasesigning_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	leasesigning "github.com/operatinggraph/lattice/packages/lease-signing"
)

// etEnvelope builds an EndTenancy envelope submitted at the given instant with
// the given ContextHint — the Reads the tenancyEnd target routes are
// [leaseAppKey, leaseAppKey.tenancy]; vectors that probe the declaration hand
// in a narrower hint.
func etEnvelope(label, actor, appKey, submittedAt string, hint *processor.ContextHint) *processor.OperationEnvelope {
	return &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "EndTenancy",
		Actor:         actor,
		SubmittedAt:   submittedAt,
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + appKey + `"}`),
		ContextHint:   hint,
	}
}

func etDeclaredHint(appKey string) *processor.ContextHint {
	return &processor.ContextHint{Reads: []string{appKey, appKey + ".tenancy"}}
}

// etStageRenewedTenancy overwrites the leaseapp's .tenancy with the shape
// SignRenewal leaves behind (termStart + rentAmount alongside the term), so
// the vectors can prove those fields survive the end. Returns the aspect's
// revision after the write.
func etStageRenewedTenancy(t *testing.T, ctx context.Context, conn *substrate.Conn, appKey string) uint64 {
	t.Helper()
	doc := map[string]any{"class": "tenancy", "isDeleted": false, "vertexKey": appKey, "localName": "tenancy",
		"data": map[string]any{
			"leaseStart": "2026-08-01T00:00:00Z", "leaseEnd": "2028-08-01T00:00:00Z",
			"renewalOpensAt": "2028-06-02T00:00:00Z", "termStart": "2027-08-01T00:00:00Z", "rentAmount": 2650,
		}}
	b, _ := json.Marshal(doc)
	rev, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, appKey+".tenancy", b)
	if err != nil {
		t.Fatalf("stage renewed tenancy: %v", err)
	}
	return rev
}

func etTenancyRevision(t *testing.T, ctx context.Context, conn *substrate.Conn, appKey string) uint64 {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, appKey+".tenancy")
	if err != nil {
		t.Fatalf("KVGet .tenancy: %v", err)
	}
	return entry.Revision
}

// TestEndTenancy_RecordsEndedAtAsLeaseEndAndPreservesTheTerm is the positive
// vector: a lapsed term (submittedAt past leaseEnd) is recorded as ended ON ITS
// END DATE — endedAt = leaseEnd, not the submission instant — with every other
// .tenancy field (leaseStart, renewalOpensAt, the renewed term's termStart and
// rentAmount) preserved, and leaseapp.tenancyEnded emitted.
func TestEndTenancy_RecordsEndedAtAsLeaseEndAndPreservesTheTerm(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "endtenancy")

	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBendtenA1ntHJKMNPQR")
	etStageRenewedTenancy(t, ctx, conn, appKey)

	env := etEnvelope("endTen0001", lsActorKey, appKey, "2028-09-15T13:42:00Z", etDeclaredHint(appKey))
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply %+v)", outcome, reply)
	}

	tdoc := readDoc(t, ctx, conn, appKey+".tenancy")
	tdata, _ := tdoc["data"].(map[string]any)
	if got, _ := tdata["endedAt"].(string); got != "2028-08-01T00:00:00Z" {
		t.Fatalf("tenancy.endedAt = %q, want the term's own leaseEnd 2028-08-01T00:00:00Z, never the fire instant", got)
	}
	for field, want := range map[string]any{
		"leaseStart": "2026-08-01T00:00:00Z", "leaseEnd": "2028-08-01T00:00:00Z",
		"renewalOpensAt": "2028-06-02T00:00:00Z", "termStart": "2027-08-01T00:00:00Z", "rentAmount": float64(2650),
	} {
		if got := tdata[field]; got != want {
			t.Fatalf("tenancy.%s = %v, want %v preserved across the end", field, got, want)
		}
	}
	if got, _ := tdoc["isDeleted"].(bool); got {
		t.Fatalf("EndTenancy must never tombstone the aspect")
	}

	ev := findEmittedEvent(t, ctx, conn, env.RequestID, "leaseapp.tenancyEnded")
	if got, _ := ev["leaseAppKey"].(string); got != appKey {
		t.Fatalf("tenancyEnded.leaseAppKey = %q, want %q", got, appKey)
	}
	if got, _ := ev["leaseEnd"].(string); got != "2028-08-01T00:00:00Z" {
		t.Fatalf("tenancyEnded.leaseEnd = %q, want 2028-08-01T00:00:00Z", got)
	}
}

// TestEndTenancy_NotYetEnded_RefusedNamingTheUTCDate: write-path honesty — a
// submission ahead of leaseEnd is refused whatever dispatched it, and the
// refusal names the end by its UTC calendar date (the slice every .tenancy
// stamp renders by), never a local-zone rendering of the instant.
func TestEndTenancy_NotYetEnded_RefusedNamingTheUTCDate(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "endtennotyet")

	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBendtenB2ntHJKMNPQR")
	before := etTenancyRevision(t, ctx, conn, appKey)

	// leaseEnd is 2027-08-01T00:00:00Z (leaseStart 2026-08-01 + 12 months);
	// one second short of it is still inside the term.
	env := etEnvelope("endTen0002", lsActorKey, appKey, "2027-07-31T23:59:59Z", etDeclaredHint(appKey))
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "NotYetEnded") {
		t.Fatalf("want a NotYetEnded refusal, got %+v", reply.Error)
	}
	if !strings.Contains(reply.Error.Message, "2027-08-01 (UTC)") {
		t.Fatalf("the refusal must name the end's UTC calendar date, got %q", reply.Error.Message)
	}
	if got := etTenancyRevision(t, ctx, conn, appKey); got != before {
		t.Fatalf("a refused EndTenancy must not touch .tenancy (revision %d → %d)", before, got)
	}
	tdoc := readDoc(t, ctx, conn, appKey+".tenancy")
	if _, has := tdoc["data"].(map[string]any)["endedAt"]; has {
		t.Fatalf("a refused EndTenancy must not record endedAt")
	}
}

// TestEndTenancy_AtLeaseEnd_IsEnded pins which side of the boundary the equal
// instant falls on: the timer fires AT leaseEnd, so a submission stamped
// exactly leaseEnd is the ordinary end, not NotYetEnded.
func TestEndTenancy_AtLeaseEnd_IsEnded(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "endtenboundary")

	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBendtenC3ntHJKMNPQR")
	env := etEnvelope("endTen0003", lsActorKey, appKey, "2027-08-01T00:00:00Z", etDeclaredHint(appKey))
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	tdoc := readDoc(t, ctx, conn, appKey+".tenancy")
	if got, _ := tdoc["data"].(map[string]any)["endedAt"].(string); got != "2027-08-01T00:00:00Z" {
		t.Fatalf("endedAt = %q, want 2027-08-01T00:00:00Z", got)
	}
}

// TestEndTenancy_Idempotent_SecondCallWritesNothing: an at-least-once
// re-dispatch of an already-ended term is Accepted with ZERO mutations — the
// .tenancy revision does not move, no event is emitted, and the reply carries
// no primaryKey (an empty write footprint has nothing to validate one against).
func TestEndTenancy_Idempotent_SecondCallWritesNothing(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "endtenidem")

	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBendtenD4ntHJKMNPQR")
	first := etEnvelope("endTen0004a", lsActorKey, appKey, "2027-09-01T00:00:00Z", etDeclaredHint(appKey))
	testutil.PublishOp(t, conn, first)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	after := etTenancyRevision(t, ctx, conn, appKey)

	second := etEnvelope("endTen0004b", lsActorKey, appKey, "2027-10-01T00:00:00Z", etDeclaredHint(appKey))
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, second)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("second EndTenancy: outcome = %v, want Accepted (idempotent), reply %+v", outcome, reply)
	}
	if got := etTenancyRevision(t, ctx, conn, appKey); got != after {
		t.Fatalf("an already-ended term must not be rewritten (revision %d → %d)", after, got)
	}
	if len(reply.Revisions) != 0 {
		t.Fatalf("a no-op EndTenancy must commit no keys, got revisions %v", reply.Revisions)
	}
	tdoc := readDoc(t, ctx, conn, appKey+".tenancy")
	if got, _ := tdoc["data"].(map[string]any)["endedAt"].(string); got != "2027-08-01T00:00:00Z" {
		t.Fatalf("endedAt = %q after the re-dispatch, want the original 2027-08-01T00:00:00Z (a later submission never moves it)", got)
	}
}

// TestEndTenancy_NoTenancy_Rejected: an undecided application has no .tenancy.
// The submitter declares the aspect as a REQUIRED read (the target's
// row.entityKey.tenancy), so its absence is the step-4 fail-closed miss the
// declaration asks for — HydrationMiss on the .tenancy key — raised the moment
// the script names the key; the script's own NoTenancy branch is the answer
// for a submitter that left the key undeclared (the vector below).
func TestEndTenancy_NoTenancy_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "endtennoten")

	applicantKey := seedApplicant(t, ctx, conn, "BBendtenE5ntHJKMNPQR")
	appKey := createApplication(t, ctx, conn, cp, cons, applicantKey)

	env := etEnvelope("endTen0005", lsActorKey, appKey, "2030-01-01T00:00:00Z", etDeclaredHint(appKey))
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "HydrationMiss") || !strings.Contains(reply.Error.Message, appKey+".tenancy") {
		t.Fatalf("want a HydrationMiss on the declared .tenancy read, got %+v", reply.Error)
	}
	if keyExists(t, ctx, conn, appKey+".tenancy") {
		t.Fatalf("a refused EndTenancy must mint no .tenancy")
	}
}

// TestEndTenancy_UndeclaredTenancyRead_RefusedNotLazilyRead is the read-posture
// vector: the .tenancy EXISTS and the term HAS lapsed, but the submitter did
// not declare the aspect. A lazy kv.Read would find it and end the term; the
// op reads only from hydration and refuses NoTenancy instead, so the OCC pin
// below always rests on a declared read's revision.
func TestEndTenancy_UndeclaredTenancyRead_RefusedNotLazilyRead(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "endtenundecl")

	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBendtenF6ntHJKMNPQR")
	before := etTenancyRevision(t, ctx, conn, appKey)

	env := etEnvelope("endTen0006", lsActorKey, appKey, "2030-01-01T00:00:00Z",
		&processor.ContextHint{Reads: []string{appKey}})
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want Rejected — an undeclared .tenancy must not be read on demand", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "NoTenancy") {
		t.Fatalf("want a NoTenancy refusal, got %+v", reply.Error)
	}
	if got := etTenancyRevision(t, ctx, conn, appKey); got != before {
		t.Fatalf("the undeclared submission must not touch .tenancy (revision %d → %d)", before, got)
	}

	// An EMPTY contextHint is refused one check earlier: the application
	// itself is not in hydration either.
	empty := etEnvelope("endTen0006b", lsActorKey, appKey, "2030-01-01T00:00:00Z", &processor.ContextHint{})
	outcome, reply = testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, empty)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("empty contextHint: outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "UnknownLeaseApplication") {
		t.Fatalf("empty contextHint: want an UnknownLeaseApplication refusal, got %+v", reply.Error)
	}
}

// TestEndTenancy_NonOperatorDenied: the grant is operator-only. A signed-in
// landlord (the consumer role, scope=self grants on their own decisions) holds
// no EndTenancy permission at all, so step 3 denies before the script runs —
// the term end is never a person-facing action.
func TestEndTenancy_NonOperatorDenied(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "endtendenied")
	llSetupLandlord(t, ctx, conn)

	appKey, _, unitKey := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBendtenG7ntHJKMNPQR")
	llSeedManages(t, ctx, conn, unitKey)
	before := etTenancyRevision(t, ctx, conn, appKey)

	env := etEnvelope("endTen0007", llLandlordKey, appKey, "2030-01-01T00:00:00Z", etDeclaredHint(appKey))
	env.AuthContext = &processor.AuthContext{Target: llLandlordKey}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || reply.Error.Code != processor.ErrCodeAuthDenied {
		t.Fatalf("want an AuthDenied rejection (no grant), got %+v", reply.Error)
	}
	if got := etTenancyRevision(t, ctx, conn, appKey); got != before {
		t.Fatalf("a denied EndTenancy must not touch .tenancy (revision %d → %d)", before, got)
	}
}

// TestEndTenancyScript_PinsTheHydratedTenancyRevision proves the OCC pin at the
// script runner, where the mutation's expectedRevision is observable (the
// committed document never carries it): the .tenancy update asserts EXACTLY the
// revision the declared read hydrated. The commit path never overrides an
// explicit assertion (commit_path.go applyHydratedRevisions), so a SignRenewal
// rewrite landing between this op's hydration and its commit RevisionConflicts
// instead of losing its extension to an end the term no longer has.
func TestEndTenancyScript_PinsTheHydratedTenancyRevision(t *testing.T) {
	const appKey = "vtx.leaseapp.BBendtenScrHJKMNPQRS"
	const hydratedRevision = uint64(7)
	var script string
	for _, d := range leasesigning.Package.DDLs {
		if d.CanonicalName == "leaseapp" {
			script = d.Script
		}
	}
	if script == "" {
		t.Fatal("leaseapp vertexType DDL not found")
	}
	result, err := processor.NewStarlarkRunner(0, 0).Run(context.Background(), processor.ScriptContext{
		Operation: &processor.OperationEnvelope{
			RequestID:     "Hj4kPmRtw9nbCxz5vQ2y",
			Lane:          processor.LaneDefault,
			OperationType: "EndTenancy",
			Actor:         lsActorKey,
			SubmittedAt:   "2028-01-01T00:00:00Z",
			Payload:       json.RawMessage(`{"leaseAppKey":"` + appKey + `"}`),
			ContextHint:   etDeclaredHint(appKey),
		},
		Hydrated: map[string]processor.VertexDoc{
			appKey: {Key: appKey, Class: "leaseapp", Data: map[string]any{}, Revision: 3},
			appKey + ".tenancy": {Key: appKey + ".tenancy", Class: "tenancy", VertexKey: appKey, LocalName: "tenancy",
				Data:     map[string]any{"leaseStart": "2026-01-01T00:00:00Z", "leaseEnd": "2027-01-01T00:00:00Z", "renewalOpensAt": "2026-11-02T00:00:00Z"},
				Revision: hydratedRevision},
		},
		DDLLookup:    map[string]processor.MetaVertex{},
		ScriptSource: script,
		ScriptClass:  "leaseapp",
	})
	if err != nil {
		t.Fatalf("EndTenancy script: %v", err)
	}
	if len(result.Mutations) != 1 {
		t.Fatalf("want exactly one mutation (the .tenancy rewrite), got %d: %+v", len(result.Mutations), result.Mutations)
	}
	m := result.Mutations[0]
	if m.Op != "update" || m.Key != appKey+".tenancy" {
		t.Fatalf("mutation = %s %s, want update %s.tenancy", m.Op, m.Key, appKey)
	}
	if m.ExpectedRevision == nil {
		t.Fatalf("the .tenancy rewrite must carry an explicit expectedRevision — an unconditioned update would swallow a concurrent SignRenewal extension")
	}
	if *m.ExpectedRevision != hydratedRevision {
		t.Fatalf("expectedRevision = %d, want the hydrated .tenancy revision %d", *m.ExpectedRevision, hydratedRevision)
	}
	data, _ := m.Document["data"].(map[string]any)
	if got, _ := data["endedAt"].(string); got != "2027-01-01T00:00:00Z" {
		t.Fatalf("endedAt = %q, want leaseEnd", got)
	}
}

// TestSignRenewal_EndedTenancy_Refused is EndTenancy's state-table obligation
// on the other writer of .tenancy: SignRenewal rewrites the aspect whole, so a
// signed renewal on an ended term would silently drop endedAt and read the
// term live again — undoing the relist the end drove. It refuses TenancyEnded
// instead, naming the end by its UTC calendar date, and the renewal stays
// unsigned.
func TestSignRenewal_EndedTenancy_Refused(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "signrenewalended")

	appKey, applicantKey, unitKey := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBendtenH8ntHJKMNPQR")
	setProfile(t, ctx, conn, cp, cons, "endtenSignProf", appKey, unitKey, map[string]any{
		"annualIncome": 40000, "employmentStatus": "employed",
	}, processor.OutcomeAccepted)
	renewalKey := openRenewalHelper(t, ctx, conn, cp, cons, appKey)
	setRenewalTerms(t, ctx, conn, cp, cons, "endtenTerms01", renewalKey, 2500, 12, processor.OutcomeAccepted)

	// The term ends (an operator's by-hand end while the cycle is still open
	// — the op does not walk renewals; the lens's open-renewal hold is a
	// dispatch gate only).
	end := etEnvelope("endTenH8sign", lsActorKey, appKey, "2027-09-01T00:00:00Z", etDeclaredHint(appKey))
	testutil.PublishOp(t, conn, end)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	before := etTenancyRevision(t, ctx, conn, appKey)

	payload, _ := json.Marshal(map[string]any{"renewalKey": renewalKey, "leaseApp": appKey, "applicant": applicantKey})
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("endtenSign01"),
		Lane:          processor.LaneDefault,
		OperationType: "SignRenewal",
		Actor:         lsActorKey,
		SubmittedAt:   "2027-09-02T00:00:00Z",
		Class:         "renewal",
		Payload:       json.RawMessage(payload),
		ContextHint: &processor.ContextHint{
			Reads: []string{
				renewalKey,
				renewsLinkKey(renewalKey, appKey),
				applicationForLinkKey(appKey, applicantKey),
				appKey + ".tenancy",
			},
			OptionalReads: []string{renewalKey + ".terms", appKey + ".applicationSignals", renewalKey + ".guarantorVerification", appKey + ".notice"},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("SignRenewal on an ended term: outcome = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "TenancyEnded") {
		t.Fatalf("want a TenancyEnded refusal, got %+v", reply.Error)
	}
	if !strings.Contains(reply.Error.Message, "2027-08-01 (UTC)") {
		t.Fatalf("the refusal must name the end's UTC calendar date, got %q", reply.Error.Message)
	}
	if got := etTenancyRevision(t, ctx, conn, appKey); got != before {
		t.Fatalf("a refused SignRenewal must not rewrite .tenancy (revision %d → %d)", before, got)
	}
	tdoc := readDoc(t, ctx, conn, appKey+".tenancy")
	if got, _ := tdoc["data"].(map[string]any)["endedAt"].(string); got != "2027-08-01T00:00:00Z" {
		t.Fatalf("endedAt = %q after the refused signing, want 2027-08-01T00:00:00Z preserved", got)
	}
	if keyExists(t, ctx, conn, renewalKey+".renewalSignature") {
		t.Fatalf("a refused SignRenewal must not write .renewalSignature")
	}
}
