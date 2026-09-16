// GiveNotice op vectors through the real install + Processor pipeline (design
// docs/reviews/loftspace-tenancy-notice-2026-09-15.md §1): the three hats one
// op admits (tenant / landlord / operator), every state refusal with its
// vector, the date normalization, and the create-only .notice — plus the
// EndTenancy and SignRenewal legs that read the recorded notice (§2, §4).
// External test package, mirroring end_tenancy_ops_test.go's harness.
package leasesigning_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	leasesigning "github.com/operatinggraph/lattice/packages/lease-signing"
)

// The term approveAndSignLeaseApp stamps: leaseStart 2026-08-01 (the seeded
// listing's availableFrom) + 12 months.
const (
	gnLeaseStart = "2026-08-01T00:00:00Z"
	gnLeaseEnd   = "2027-08-01T00:00:00Z"
	// gnGivenAt is the instant every notice below is submitted at, well
	// inside the term.
	gnGivenAt = "2026-12-01T10:15:00Z"
	// gnMoveOut is the bare-date form the FE sends; gnMoveOutAt is what the
	// script records (midnight UTC).
	gnMoveOut   = "2027-03-31"
	gnMoveOutAt = "2027-03-31T00:00:00Z"
)

// gnLandlord is a signed-in landlord holding the consumer role and the
// GiveNotice scope=self grant alone — the hat the manages probe admits.
const (
	gnLandlordID  = "BBnoticeLandHJKMNPQR"
	gnLandlordKey = "vtx.identity." + gnLandlordID
)

// gnEnvelope builds a GiveNotice envelope. A non-empty authTarget sends the
// scope=self authContext (target == actor, the only shape step 3 admits on a
// self grant).
func gnEnvelope(label, actor, appKey, moveOutDate, submittedAt string, hint *processor.ContextHint, authTarget string) *processor.OperationEnvelope {
	payload, _ := json.Marshal(map[string]any{"leaseAppKey": appKey, "moveOutDate": moveOutDate})
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "GiveNotice",
		Actor:         actor,
		SubmittedAt:   submittedAt,
		Class:         "leaseapp",
		Payload:       json.RawMessage(payload),
		ContextHint:   hint,
	}
	if authTarget != "" {
		env.AuthContext = &processor.AuthContext{Target: authTarget}
	}
	return env
}

// gnOperatorHint is the descriptor's declared read set for a standing
// (operator) dispatch: the application, its .tenancy and .signature as
// required reads, its .notice as the absence-tolerant one.
func gnOperatorHint(appKey string) *processor.ContextHint {
	return &processor.ContextHint{
		Reads:         []string{appKey, appKey + ".tenancy", appKey + ".signature"},
		OptionalReads: []string{appKey + ".notice"},
	}
}

// gnSelfHint is the same set plus the self-path probe the descriptor
// declares off {actor:id} — the deterministic applicationFor link keyed on
// the acting identity — and the landlord walk's enumeration.
func gnSelfHint(t *testing.T, appKey, actorKey string) *processor.ContextHint {
	t.Helper()
	h := gnOperatorHint(appKey)
	h.OptionalReads = append(h.OptionalReads, applicationForLinkKey(appKey, actorKey))
	// The descriptor's one enumeration is hub-templated on the payload, which
	// the helper cannot resolve; it is substituted here and the skip list is
	// what proves the declaration is still on the descriptor.
	hints, skipped := testutil.DeclaredEnumerationsWithSkips("GiveNotice", actorKey, leasesigning.OpMetas())
	if len(hints) != 0 || len(skipped) != 1 || skipped[0] != "{payload.leaseAppKey}" {
		t.Fatalf("GiveNotice must declare exactly the {payload.leaseAppKey} appliesToUnit walk; got hints=%v skipped=%v", hints, skipped)
	}
	h.Enumerations = []processor.EnumerationHint{{Hub: appKey, Relation: "appliesToUnit", Direction: "out"}}
	return h
}

// gnSeedSelfCap grants actorKey the consumer's GiveNotice scope=self
// permission and the consumer role — the plain signed-in resident or
// landlord, no standing path anywhere.
func gnSeedSelfCap(t *testing.T, ctx context.Context, conn *substrate.Conn, actorKey string) {
	t.Helper()
	now := time.Now().UTC()
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    "cap.identity." + actorKey[len("vtx.identity."):],
		Actor:                  actorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{actorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "GiveNotice", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "consumer")},
	})
	testutil.SeedHoldsRole(t, ctx, conn, actorKey, "vtx.role."+pkgmgr.RoleID("identity-domain", "consumer"))
}

// gnSeedLandlord seeds the landlord identity, its self cap, and its manages
// link to unitKey.
func gnSeedLandlord(t *testing.T, ctx context.Context, conn *substrate.Conn, unitKey string) {
	t.Helper()
	seedVertex(t, ctx, conn, gnLandlordKey, "identity", map[string]any{})
	gnSeedSelfCap(t, ctx, conn, gnLandlordKey)
	_, unitID, _ := substrate.ParseVertexKey(unitKey)
	testutil.SeedLink(t, ctx, conn, "lnk.identity."+gnLandlordID+".manages.unit."+unitID, "manages", gnLandlordKey, unitKey)
}

// gnNotice reads the recorded .notice data ("" fields when absent).
func gnNotice(t *testing.T, ctx context.Context, conn *substrate.Conn, appKey string) map[string]any {
	t.Helper()
	if !keyExists(t, ctx, conn, appKey+".notice") {
		return nil
	}
	doc := readDoc(t, ctx, conn, appKey+".notice")
	data, _ := doc["data"].(map[string]any)
	return data
}

// gnRequireNotice asserts the aspect GiveNotice records: class tenancyNotice,
// the normalized move-out, givenAt = the op's own submittedAt, givenBy = who.
func gnRequireNotice(t *testing.T, ctx context.Context, conn *substrate.Conn, appKey, wantMoveOutAt, wantGivenAt, wantGivenBy string) {
	t.Helper()
	doc := readDoc(t, ctx, conn, appKey+".notice")
	if got, _ := doc["class"].(string); got != "tenancyNotice" {
		t.Fatalf(".notice class = %q, want tenancyNotice", got)
	}
	data, _ := doc["data"].(map[string]any)
	if got, _ := data["moveOutAt"].(string); got != wantMoveOutAt {
		t.Fatalf(".notice.moveOutAt = %q, want %q", got, wantMoveOutAt)
	}
	if got, _ := data["givenAt"].(string); got != wantGivenAt {
		t.Fatalf(".notice.givenAt = %q, want the op's submittedAt %q", got, wantGivenAt)
	}
	if got, _ := data["givenBy"].(string); got != wantGivenBy {
		t.Fatalf(".notice.givenBy = %q, want %q", got, wantGivenBy)
	}
}

func gnRequireRefused(t *testing.T, outcome processor.MessageOutcome, reply *processor.OperationReply, code string) {
	t.Helper()
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want Rejected (%s); reply %+v", outcome, code, reply)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, code) {
		t.Fatalf("want a %s refusal, got %+v", code, reply.Error)
	}
}

// TestGiveNotice_TenantRecordsTheNotice is the positive vector, tenant hat:
// the application's own applicant, signed in as themselves on the consumer
// scope=self grant, gives notice for a bare YYYY-MM-DD inside the term. The
// .notice lands with the date normalized to midnight UTC, givenAt = the op's
// submittedAt, givenBy = tenant (the applicationFor probe admitted them), and
// leaseapp.noticeGiven is emitted.
func TestGiveNotice_TenantRecordsTheNotice(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "givenoticetenant")

	appKey, applicantKey, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBnoticeA1ntHJKMNPQR")
	gnSeedSelfCap(t, ctx, conn, applicantKey)

	env := gnEnvelope("giveNot0001", applicantKey, appKey, gnMoveOut, gnGivenAt, gnSelfHint(t, appKey, applicantKey), applicantKey)
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply %+v)", outcome, reply)
	}
	if reply.PrimaryKey != appKey {
		t.Fatalf("primaryKey = %q, want %q", reply.PrimaryKey, appKey)
	}
	gnRequireNotice(t, ctx, conn, appKey, gnMoveOutAt, gnGivenAt, "tenant")

	ev := findEmittedEvent(t, ctx, conn, env.RequestID, "leaseapp.noticeGiven")
	if got, _ := ev["leaseAppKey"].(string); got != appKey {
		t.Fatalf("noticeGiven.leaseAppKey = %q, want %q", got, appKey)
	}
	if got, _ := ev["moveOutAt"].(string); got != gnMoveOutAt {
		t.Fatalf("noticeGiven.moveOutAt = %q, want %q", got, gnMoveOutAt)
	}
	if got, _ := ev["givenBy"].(string); got != "tenant" {
		t.Fatalf("noticeGiven.givenBy = %q, want tenant", got)
	}
	tdoc := readDoc(t, ctx, conn, appKey+".tenancy")
	if _, has := tdoc["data"].(map[string]any)["endedAt"]; has {
		t.Fatalf("a notice is not an end: .tenancy.endedAt must stay unset until EndTenancy records it")
	}
}

// TestGiveNotice_LandlordRecordsTheNotice: the same op under the landlord
// hat — the acting identity is NOT the applicant (its applicationFor probe
// reads absent) but manages the application's own unit, so require_manages
// admits it and givenBy records landlord.
func TestGiveNotice_LandlordRecordsTheNotice(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "givenoticelandlord")

	appKey, _, unitKey := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBnoticeB2ntHJKMNPQR")
	gnSeedLandlord(t, ctx, conn, unitKey)

	env := gnEnvelope("giveNot0002", gnLandlordKey, appKey, gnMoveOut, gnGivenAt, gnSelfHint(t, appKey, gnLandlordKey), gnLandlordKey)
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply %+v)", outcome, reply)
	}
	gnRequireNotice(t, ctx, conn, appKey, gnMoveOutAt, gnGivenAt, "landlord")
}

// TestGiveNotice_OperatorRecordsTheNotice_RFC3339Normalized: the standing
// operator grant admits the op with neither link and records givenBy =
// operator; an RFC3339 moveOutDate is a DATE-ONLY fact — read as its UTC
// calendar day and recorded as that day's midnight UTC, the clock part
// dropped ("2027-03-31T15:00:00+02:00" is 13:00Z on 03-31, recorded
// 2027-03-31T00:00:00Z; "2027-04-01T00:00:00-07:00" is 07:00Z on 04-01,
// recorded 2027-04-01T00:00:00Z — never a stray seven hours the rent clause
// would bill a period for).
func TestGiveNotice_OperatorRecordsTheNotice_RFC3339Normalized(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "givenoticeoperator")

	for i, fx := range []struct{ in, want string }{
		{"2027-03-31T15:00:00+02:00", "2027-03-31T00:00:00Z"},
		{"2027-04-01T00:00:00-07:00", "2027-04-01T00:00:00Z"},
		{"2027-04-01T23:59:59Z", "2027-04-01T00:00:00Z"},
	} {
		appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBnoticeC3ntHJKMNPQ"+string(rune('R'+i)))
		env := gnEnvelope("giveNot0003"+string(rune('A'+i)), lsActorKey, appKey, fx.in, gnGivenAt, gnOperatorHint(appKey), "")
		outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
		if outcome != processor.OutcomeAccepted {
			t.Fatalf("%s: outcome = %v, want Accepted (reply %+v)", fx.in, outcome, reply)
		}
		gnRequireNotice(t, ctx, conn, appKey, fx.want, gnGivenAt, "operator")
	}
}

// TestGiveNoticeScript_PinsTheTenancyRevisionAsTheSerializationPoint proves
// the GiveNotice / SignRenewal serialization at the script runner, where a
// mutation's expectedRevision is observable: beside the create-only .notice,
// the batch carries a NO-CHANGE update of .tenancy (the aspect's own data
// back verbatim) pinned to EXACTLY the revision the declared read hydrated.
// Each op reads the other's aspect but writes only its own, and the commit
// path conditions only the keys a batch mutates — so without this shared key
// a SignRenewal hydrated before the notice commits would still land an
// extended term on a lease under notice. With it, whichever commits second
// RevisionConflicts.
func TestGiveNoticeScript_PinsTheTenancyRevisionAsTheSerializationPoint(t *testing.T) {
	const appKey = "vtx.leaseapp.BBnoticeScrHJKMNPQRS"
	const hydratedRevision = uint64(11)
	var script string
	for _, d := range leasesigning.Package.DDLs {
		if d.CanonicalName == "leaseapp" {
			script = d.Script
		}
	}
	if script == "" {
		t.Fatal("leaseapp vertexType DDL not found")
	}
	tenancyData := map[string]any{"leaseStart": "2026-08-01T00:00:00Z", "leaseEnd": "2027-08-01T00:00:00Z", "renewalOpensAt": "2027-06-02T00:00:00Z", "rentAmount": float64(2400)}
	result, err := processor.NewStarlarkRunner(0, 0).Run(context.Background(), processor.ScriptContext{
		Operation: &processor.OperationEnvelope{
			RequestID:     "Hj4kPmRtw9nbCxz5vQ2z",
			Lane:          processor.LaneDefault,
			OperationType: "GiveNotice",
			Actor:         lsActorKey,
			SubmittedAt:   gnGivenAt,
			Payload:       json.RawMessage(`{"leaseAppKey":"` + appKey + `","moveOutDate":"` + gnMoveOut + `"}`),
			ContextHint:   gnOperatorHint(appKey),
		},
		Hydrated: map[string]processor.VertexDoc{
			appKey: {Key: appKey, Class: "leaseapp", Data: map[string]any{}, Revision: 3},
			appKey + ".tenancy": {Key: appKey + ".tenancy", Class: "tenancy", VertexKey: appKey, LocalName: "tenancy",
				Data: tenancyData, Revision: hydratedRevision},
			appKey + ".signature": {Key: appKey + ".signature", Class: "signature", VertexKey: appKey, LocalName: "signature",
				Data: map[string]any{"signedAt": "2026-07-15T00:00:00Z"}, Revision: 5},
		},
		DDLLookup:    map[string]processor.MetaVertex{},
		ScriptSource: script,
		ScriptClass:  "leaseapp",
	})
	if err != nil {
		t.Fatalf("GiveNotice script: %v", err)
	}
	if len(result.Mutations) != 2 {
		t.Fatalf("want exactly two mutations (the .notice create + the .tenancy pin), got %d: %+v", len(result.Mutations), result.Mutations)
	}
	var notice, tenancy *processor.MutationOp
	for i := range result.Mutations {
		switch result.Mutations[i].Key {
		case appKey + ".notice":
			notice = &result.Mutations[i]
		case appKey + ".tenancy":
			tenancy = &result.Mutations[i]
		}
	}
	if notice == nil || notice.Op != "create" {
		t.Fatalf("the .notice write must be a create, got %+v", notice)
	}
	if tenancy == nil || tenancy.Op != "update" {
		t.Fatalf("the .tenancy serialization write must be an update, got %+v", tenancy)
	}
	if tenancy.ExpectedRevision == nil || *tenancy.ExpectedRevision != hydratedRevision {
		t.Fatalf("the .tenancy update must pin the hydrated revision %d, got %v", hydratedRevision, tenancy.ExpectedRevision)
	}
	data, _ := tenancy.Document["data"].(map[string]any)
	if len(data) != len(tenancyData) {
		t.Fatalf(".tenancy data must be carried back unchanged, got %v", data)
	}
	for k, want := range tenancyData {
		// The runner hands numbers back as Starlark ints; compare by value.
		if fmt.Sprint(data[k]) != fmt.Sprint(want) {
			t.Fatalf(".tenancy.%s = %v, want %v unchanged (the write is a serialization point, not a data write)", k, data[k], want)
		}
	}
}

// TestGiveNotice_OperatorWithAuthContext_StillRecordsOperator: a scope=any
// holder whose client happened to send a self authContext (the descriptor
// says AuthContext self, and Facet sends it under every hat) is admitted on
// the standing grant — step 3 never examined the target, so nothing proved
// it — and must be recorded as the OPERATOR it is. Keying the probes on the
// target's mere presence would walk a stranger's links and record "landlord"
// off require_manages' silent return on the unvalidated path; keying them on
// op.authTargetValidated (the predicate require_manages itself binds) does
// not. No link of either kind exists for this actor.
func TestGiveNotice_OperatorWithAuthContext_StillRecordsOperator(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "givenoticeopself")

	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBnoticeV9ntHJKMNPQR")
	env := gnEnvelope("giveNot0003b", lsActorKey, appKey, gnMoveOut, gnGivenAt, gnSelfHint(t, appKey, lsActorKey), lsActorKey)
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted on the standing grant (reply %+v)", outcome, reply)
	}
	gnRequireNotice(t, ctx, conn, appKey, gnMoveOutAt, gnGivenAt, "operator")
}

// TestGiveNotice_SelfCallerWithNeitherLink_Denied: a consumer signed in as
// themselves who is neither the tenant nor a landlord of the unit reaches
// the op on a valid scope=self grant and is refused AuthDenied by the manages
// probe — ahead of the liveness check, so the denial reveals nothing about
// the application. The positive vectors above run the same probes first, so
// this is the probe answering, not a broken self path.
func TestGiveNotice_SelfCallerWithNeitherLink_Denied(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "givenoticestranger")

	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBnoticeD4ntHJKMNPQR")
	strangerKey := seedApplicant(t, ctx, conn, "BBnoticeStrHJKMNPQRS")
	gnSeedSelfCap(t, ctx, conn, strangerKey)

	env := gnEnvelope("giveNot0004", strangerKey, appKey, gnMoveOut, gnGivenAt, gnSelfHint(t, appKey, strangerKey), strangerKey)
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	gnRequireRefused(t, outcome, reply, "AuthDenied")
	if gnNotice(t, ctx, conn, appKey) != nil {
		t.Fatalf("a denied GiveNotice must write no .notice")
	}
}

// TestGiveNotice_NoTenancy: an undecided application has no .tenancy. The
// aspect is a REQUIRED declared read, so a submitter that declares it meets
// the step-4 fail-closed miss (HydrationMiss on the .tenancy key); one that
// left it undeclared is refused by the script's own NoTenancy branch — never
// served by an on-demand read.
func TestGiveNotice_NoTenancy(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "givenoticenoten")

	applicantKey := seedApplicant(t, ctx, conn, "BBnoticeE5ntHJKMNPQR")
	appKey := createApplication(t, ctx, conn, cp, cons, applicantKey)

	declared := gnEnvelope("giveNot0005a", lsActorKey, appKey, gnMoveOut, gnGivenAt, gnOperatorHint(appKey), "")
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, declared)
	if outcome != processor.OutcomeRejected || reply.Error == nil || !strings.Contains(reply.Error.Message, "HydrationMiss") || !strings.Contains(reply.Error.Message, appKey+".tenancy") {
		t.Fatalf("declared .tenancy on an undecided application: want a HydrationMiss on it, got outcome=%v %+v", outcome, reply.Error)
	}

	undeclared := gnEnvelope("giveNot0005b", lsActorKey, appKey, gnMoveOut, gnGivenAt,
		&processor.ContextHint{Reads: []string{appKey}, OptionalReads: []string{appKey + ".notice"}}, "")
	outcome, reply = testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, undeclared)
	gnRequireRefused(t, outcome, reply, "NoTenancy")
	if gnNotice(t, ctx, conn, appKey) != nil {
		t.Fatalf("a refused GiveNotice must write no .notice")
	}
}

// TestGiveNotice_LeaseNotSigned: a .tenancy with no .signature (staged
// directly — the approve floor never stamps one on an unsigned application)
// is refused LeaseNotSigned by the script when the submitter left the
// required .signature read undeclared; declared, its absence is the step-4
// HydrationMiss.
func TestGiveNotice_LeaseNotSigned(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "givenoticeunsigned")

	applicantKey := seedApplicant(t, ctx, conn, "BBnoticeF6ntHJKMNPQR")
	appKey := createApplication(t, ctx, conn, cp, cons, applicantKey)
	etStageRenewedTenancy(t, ctx, conn, appKey)

	undeclared := gnEnvelope("giveNot0006a", lsActorKey, appKey, "2027-03-31", gnGivenAt,
		&processor.ContextHint{Reads: []string{appKey, appKey + ".tenancy"}, OptionalReads: []string{appKey + ".notice"}}, "")
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, undeclared)
	gnRequireRefused(t, outcome, reply, "LeaseNotSigned")

	declared := gnEnvelope("giveNot0006b", lsActorKey, appKey, "2027-03-31", gnGivenAt, gnOperatorHint(appKey), "")
	outcome, reply = testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, declared)
	if outcome != processor.OutcomeRejected || reply.Error == nil || !strings.Contains(reply.Error.Message, "HydrationMiss") || !strings.Contains(reply.Error.Message, appKey+".signature") {
		t.Fatalf("declared .signature on an unsigned application: want a HydrationMiss on it, got outcome=%v %+v", outcome, reply.Error)
	}
	if gnNotice(t, ctx, conn, appKey) != nil {
		t.Fatalf("a refused GiveNotice must write no .notice")
	}
}

// TestGiveNotice_TenancyEnded: once EndTenancy has recorded endedAt there is
// no term left to give notice on; the refusal names the end's UTC calendar
// date.
func TestGiveNotice_TenancyEnded(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "givenoticeended")

	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBnoticeG7ntHJKMNPQR")
	end := etEnvelope("giveNotEnd07", lsActorKey, appKey, "2027-09-01T00:00:00Z", etDeclaredHint(appKey))
	testutil.PublishOp(t, conn, end)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	env := gnEnvelope("giveNot0007", lsActorKey, appKey, "2027-09-15", "2027-09-02T00:00:00Z", gnOperatorHint(appKey), "")
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	gnRequireRefused(t, outcome, reply, "TenancyEnded")
	if !strings.Contains(reply.Error.Message, "2027-08-01 (UTC)") {
		t.Fatalf("the refusal must name the end's UTC calendar date, got %q", reply.Error.Message)
	}
}

// TestGiveNotice_DateRules pins the three date refusals and the two admitted
// boundaries against one signed term [2026-08-01, 2027-08-01):
//   - a move-out before the UTC calendar day of submittedAt → MoveOutBeforeToday
//     (the day is sliced from the op's own stamp; the refusal names both dates);
//   - a move-out on or before leaseStart → MoveOutBeforeStart;
//   - a move-out on or after leaseEnd → MoveOutAfterEnd (the term ends on its
//     own date; nothing to record);
//   - a SAME-DAY move-out (the tenant who already left records today) and a
//     move-out the day before leaseEnd are admitted.
func TestGiveNotice_DateRules(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "givenoticedates")

	refusals := []struct {
		label, moveOut, submittedAt, code, names string
	}{
		{"beforeToday", "2026-11-30", "2026-12-01T00:00:01Z", "MoveOutBeforeToday", "2026-12-01 (UTC)"},
		{"beforeStart", "2026-08-01", "2026-07-01T09:00:00Z", "MoveOutBeforeStart", "2026-08-01 (UTC)"},
		{"afterEnd", "2027-08-01", "2026-12-01T09:00:00Z", "MoveOutAfterEnd", "2027-08-01 (UTC)"},
		{"pastEnd", "2027-09-01", "2026-12-01T09:00:00Z", "MoveOutAfterEnd", "2027-08-01 (UTC)"},
	}
	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBnoticeH8ntHJKMNPQR")
	for _, r := range refusals {
		env := gnEnvelope("giveNot08"+r.label, lsActorKey, appKey, r.moveOut, r.submittedAt, gnOperatorHint(appKey), "")
		outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
		gnRequireRefused(t, outcome, reply, r.code)
		if !strings.Contains(reply.Error.Message, r.names) {
			t.Fatalf("%s: the refusal must name %s, got %q", r.label, r.names, reply.Error.Message)
		}
		if gnNotice(t, ctx, conn, appKey) != nil {
			t.Fatalf("%s: a refused GiveNotice must write no .notice", r.label)
		}
	}

	// Same-day: submitted late on the move-out day itself (west-of-Greenwich
	// clocks would still call it "today"; the op slices its own UTC stamp).
	sameDay := gnEnvelope("giveNot08same", lsActorKey, appKey, "2026-12-01", "2026-12-01T23:59:59Z", gnOperatorHint(appKey), "")
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, sameDay)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("same-day move-out: outcome = %v, want Accepted (reply %+v)", outcome, reply)
	}
	gnRequireNotice(t, ctx, conn, appKey, "2026-12-01T00:00:00Z", "2026-12-01T23:59:59Z", "operator")

	// The day before leaseEnd is the latest admitted move-out.
	appKey2, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBnoticeJ9ntHJKMNPQS")
	lastDay := gnEnvelope("giveNot08last", lsActorKey, appKey2, "2027-07-31", gnGivenAt, gnOperatorHint(appKey2), "")
	outcome, reply = testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, lastDay)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("move-out the day before leaseEnd: outcome = %v, want Accepted (reply %+v)", outcome, reply)
	}
	gnRequireNotice(t, ctx, conn, appKey2, "2027-07-31T00:00:00Z", gnGivenAt, "operator")
}

// TestGiveNotice_MalformedMoveOutDate_NamesTheField: a moveOutDate that is
// neither a bare YYYY-MM-DD nor an RFC3339 instant is refused InvalidArgument
// naming the field, before any state is read.
func TestGiveNotice_MalformedMoveOutDate_NamesTheField(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "givenoticebaddate")

	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBnoticeK1ntHJKMNPQR")
	for i, bad := range []string{"2027-3-31", "31/03/2027", "2027-03-31 10:00"} {
		env := gnEnvelope("giveNot09"+string(rune('A'+i)), lsActorKey, appKey, bad, gnGivenAt, gnOperatorHint(appKey), "")
		outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
		gnRequireRefused(t, outcome, reply, "InvalidArgument: moveOutDate")
	}
	missing := gnEnvelope("giveNot09none", lsActorKey, appKey, "", gnGivenAt, gnOperatorHint(appKey), "")
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, missing)
	gnRequireRefused(t, outcome, reply, "InvalidArgument: moveOutDate")
	if gnNotice(t, ctx, conn, appKey) != nil {
		t.Fatalf("a refused GiveNotice must write no .notice")
	}
}

// TestGiveNotice_OnceOnly: the .notice is create-only. A second GiveNotice
// that declares the aspect finds it live and is refused NoticeAlreadyGiven,
// naming the recorded move-out; one that left it undeclared collides on the
// create and is rejected at commit. Either way the first notice stands.
func TestGiveNotice_OnceOnly(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "givenoticeonce")

	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBnoticeM2ntHJKMNPQR")
	first := gnEnvelope("giveNot0010a", lsActorKey, appKey, gnMoveOut, gnGivenAt, gnOperatorHint(appKey), "")
	testutil.PublishOp(t, conn, first)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	declared := gnEnvelope("giveNot0010b", lsActorKey, appKey, "2027-04-30", "2026-12-02T10:00:00Z", gnOperatorHint(appKey), "")
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, declared)
	gnRequireRefused(t, outcome, reply, "NoticeAlreadyGiven")
	if !strings.Contains(reply.Error.Message, "2027-03-31 (UTC)") {
		t.Fatalf("the refusal must name the recorded move-out, got %q", reply.Error.Message)
	}

	undeclared := gnEnvelope("giveNot0010c", lsActorKey, appKey, "2027-04-30", "2026-12-02T10:00:00Z",
		&processor.ContextHint{Reads: []string{appKey, appKey + ".tenancy", appKey + ".signature"}}, "")
	outcome, reply = testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, undeclared)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("undeclared second notice: outcome = %v, want Rejected (the create collides), reply %+v", outcome, reply)
	}
	gnRequireNotice(t, ctx, conn, appKey, gnMoveOutAt, gnGivenAt, "operator")
}

// TestGiveNotice_UnderOpenRenewal_Admitted: a notice given while a renewal
// cycle is open is admitted — the op walks no renewals; the tenant declines
// by leaving. The cycle stays open here (renewalComplete closes it on the
// recorded endedAt; SignRenewal refuses NoticeGiven — the vector below).
func TestGiveNotice_UnderOpenRenewal_Admitted(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "givenoticerenewal")

	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBnoticeN3ntHJKMNPQR")
	renewalKey := openRenewalHelper(t, ctx, conn, cp, cons, appKey)

	env := gnEnvelope("giveNot0011", lsActorKey, appKey, gnMoveOut, gnGivenAt, gnOperatorHint(appKey), "")
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted under an open cycle (reply %+v)", outcome, reply)
	}
	gnRequireNotice(t, ctx, conn, appKey, gnMoveOutAt, gnGivenAt, "operator")
	rn := readDoc(t, ctx, conn, renewalKey)
	if got, _ := rn["data"].(map[string]any)["status"].(string); got != "open" {
		t.Fatalf("renewal status = %q after the notice, want open — the op walks no renewals", got)
	}
}

// --- EndTenancy reads the notice (§2) -------------------------------------

// etNoticeHint is the tenancyEnd target's dispatch shape: the two required
// reads plus .notice as the absence-tolerant one.
func etNoticeHint(appKey string) *processor.ContextHint {
	h := etDeclaredHint(appKey)
	h.OptionalReads = []string{appKey + ".notice"}
	return h
}

// TestEndTenancy_WithNotice_EndsOnTheMoveOut: a recorded notice before
// leaseEnd makes the move-out the term's end. A submission between the
// notice and the move-out is refused NotYetEnded naming the MOVE-OUT date;
// at the move-out the term ends with endedAt = moveOutAt, every other
// .tenancy field preserved, and the event carries both leaseEnd and endedAt.
func TestEndTenancy_WithNotice_EndsOnTheMoveOut(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "endtennotice")

	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBnoticeP4ntHJKMNPQR")
	etStageRenewedTenancy(t, ctx, conn, appKey)
	// The staged renewed term runs to 2028-08-01; the notice cuts it to
	// 2027-11-30.
	notice := gnEnvelope("endTenNot12a", lsActorKey, appKey, "2027-11-30", "2027-06-01T00:00:00Z", gnOperatorHint(appKey), "")
	testutil.PublishOp(t, conn, notice)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	early := etEnvelope("endTenNot12b", lsActorKey, appKey, "2027-11-29T23:59:59Z", etNoticeHint(appKey))
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, early)
	gnRequireRefused(t, outcome, reply, "NotYetEnded")
	if !strings.Contains(reply.Error.Message, "2027-11-30 (UTC)") {
		t.Fatalf("NotYetEnded must name the move-out, not leaseEnd; got %q", reply.Error.Message)
	}

	atMoveOut := etEnvelope("endTenNot12c", lsActorKey, appKey, "2027-11-30T00:00:00Z", etNoticeHint(appKey))
	outcome, reply = testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, atMoveOut)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("at the move-out: outcome = %v, want Accepted (reply %+v)", outcome, reply)
	}
	tdoc := readDoc(t, ctx, conn, appKey+".tenancy")
	tdata, _ := tdoc["data"].(map[string]any)
	if got, _ := tdata["endedAt"].(string); got != "2027-11-30T00:00:00Z" {
		t.Fatalf("endedAt = %q, want the recorded move-out 2027-11-30T00:00:00Z", got)
	}
	for field, want := range map[string]any{
		"leaseStart": "2026-08-01T00:00:00Z", "leaseEnd": "2028-08-01T00:00:00Z",
		"renewalOpensAt": "2028-06-02T00:00:00Z", "termStart": "2027-08-01T00:00:00Z", "rentAmount": float64(2650),
	} {
		if got := tdata[field]; got != want {
			t.Fatalf("tenancy.%s = %v, want %v preserved across the early end", field, got, want)
		}
	}
	ev := findEmittedEvent(t, ctx, conn, atMoveOut.RequestID, "leaseapp.tenancyEnded")
	if got, _ := ev["endedAt"].(string); got != "2027-11-30T00:00:00Z" {
		t.Fatalf("tenancyEnded.endedAt = %q, want 2027-11-30T00:00:00Z", got)
	}
	if got, _ := ev["leaseEnd"].(string); got != "2028-08-01T00:00:00Z" {
		t.Fatalf("tenancyEnded.leaseEnd = %q, want the term's own 2028-08-01T00:00:00Z", got)
	}
}

// TestEndTenancy_NoticeUndeclared_FallsBackToLeaseEnd pins the documented
// fallback: the .notice EXISTS and the move-out has passed, but the submitter
// did not declare the aspect. The op reads it from hydration only, so the
// term is read as ending at leaseEnd — refused NotYetEnded before it, and
// ended AT leaseEnd once reached — never lazily read.
func TestEndTenancy_NoticeUndeclared_FallsBackToLeaseEnd(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "endtennoticeundecl")

	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBnoticeQ5ntHJKMNPQR")
	notice := gnEnvelope("endTenNot13a", lsActorKey, appKey, gnMoveOut, gnGivenAt, gnOperatorHint(appKey), "")
	testutil.PublishOp(t, conn, notice)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	before := etTenancyRevision(t, ctx, conn, appKey)

	pastMoveOut := etEnvelope("endTenNot13b", lsActorKey, appKey, "2027-04-15T00:00:00Z", etDeclaredHint(appKey))
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, pastMoveOut)
	gnRequireRefused(t, outcome, reply, "NotYetEnded")
	if !strings.Contains(reply.Error.Message, "2027-08-01 (UTC)") {
		t.Fatalf("undeclared .notice: the term reads as ending at leaseEnd; got %q", reply.Error.Message)
	}
	if got := etTenancyRevision(t, ctx, conn, appKey); got != before {
		t.Fatalf("the refused submission must not touch .tenancy (revision %d → %d)", before, got)
	}

	// The same undeclared submitter at leaseEnd ends the term THERE — the
	// fallback is the whole of what an undeclared read buys.
	atLeaseEnd := etEnvelope("endTenNot13c", lsActorKey, appKey, "2027-08-01T00:00:00Z", etDeclaredHint(appKey))
	testutil.PublishOp(t, conn, atLeaseEnd)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	tdoc := readDoc(t, ctx, conn, appKey+".tenancy")
	if got, _ := tdoc["data"].(map[string]any)["endedAt"].(string); got != gnLeaseEnd {
		t.Fatalf("endedAt = %q, want leaseEnd %s (the undeclared fallback)", got, gnLeaseEnd)
	}
}

// termEndOpFixtures is tenancy_end_lens_test.go's termEndFixtures table
// carried into the external test package (the two packages cannot share a
// variable): the same before / at / after / none rows against the same
// leaseEnd, so the script's derivation of the term's end and the lens's CASE
// are pinned over one set — direction, null arm, and the inert boundary
// (that file's comment says why "at" can never separate `<` from `<=`).
var termEndOpFixtures = []struct {
	name, moveOutAt, wantEnd string
}{
	{"before", "2027-03-31T00:00:00Z", "2027-03-31T00:00:00Z"},
	{"at", gnLeaseEnd, gnLeaseEnd},
	{"after", "2027-09-01T00:00:00Z", gnLeaseEnd},
	{"none", "", gnLeaseEnd},
}

// TestEndTenancy_TermEndFixtureTable drives termEndOpFixtures through the
// op: a .notice is staged directly (the "at" and "after" rows are shapes
// GiveNotice refuses, so the script's own comparison is what is under test),
// EndTenancy one second before the expected end is refused NotYetEnded naming
// that end, and EndTenancy at it records endedAt = that end.
func TestEndTenancy_TermEndFixtureTable(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "endtentermendtable")

	seeds := map[string]string{"before": "BBnoticeR6ntHJKMNPQR", "at": "BBnoticeR6ntHJKMNPQS", "after": "BBnoticeR6ntHJKMNPQT", "none": "BBnoticeR6ntHJKMNPQU"}
	for _, fx := range termEndOpFixtures {
		appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, seeds[fx.name])
		if fx.moveOutAt != "" {
			doc, _ := json.Marshal(map[string]any{"class": "tenancyNotice", "isDeleted": false, "vertexKey": appKey, "localName": "notice",
				"data": map[string]any{"moveOutAt": fx.moveOutAt, "givenAt": gnGivenAt, "givenBy": "operator"}})
			if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, appKey+".notice", doc); err != nil {
				t.Fatalf("%s: stage notice: %v", fx.name, err)
			}
		}
		wantEnd, _ := time.Parse(time.RFC3339, fx.wantEnd)
		early := etEnvelope("endTenTbl"+fx.name+"a", lsActorKey, appKey, wantEnd.Add(-time.Second).UTC().Format(time.RFC3339), etNoticeHint(appKey))
		outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, early)
		gnRequireRefused(t, outcome, reply, "NotYetEnded")
		if !strings.Contains(reply.Error.Message, fx.wantEnd[:10]+" (UTC)") {
			t.Fatalf("%s: NotYetEnded must name %s, got %q", fx.name, fx.wantEnd[:10], reply.Error.Message)
		}

		atEnd := etEnvelope("endTenTbl"+fx.name+"b", lsActorKey, appKey, fx.wantEnd, etNoticeHint(appKey))
		outcome, reply = testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, atEnd)
		if outcome != processor.OutcomeAccepted {
			t.Fatalf("%s: at the end: outcome = %v, want Accepted (reply %+v)", fx.name, outcome, reply)
		}
		tdoc := readDoc(t, ctx, conn, appKey+".tenancy")
		if got, _ := tdoc["data"].(map[string]any)["endedAt"].(string); got != fx.wantEnd {
			t.Fatalf("%s: endedAt = %q, want %q", fx.name, got, fx.wantEnd)
		}
	}
}

// --- SignRenewal reads the notice (§4) ------------------------------------

// signRenewalReply submits SignRenewal with the descriptor's full declared
// read set (the .notice OptionalRead included) and returns the reply.
func signRenewalReply(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, renewalKey, appKey, applicantKey, submittedAt string) (processor.MessageOutcome, *processor.OperationReply) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"renewalKey": renewalKey, "leaseApp": appKey, "applicant": applicantKey})
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SignRenewal",
		Actor:         lsActorKey,
		SubmittedAt:   submittedAt,
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
	return testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
}

// TestSignRenewal_NoticeGiven_Refused: a lease under notice cannot be
// renewed. With terms set and the profile on file (every earlier guard
// satisfied), SignRenewal refuses NoticeGiven naming the move-out's UTC
// date, rewrites nothing, and the cycle stays open and unsigned.
func TestSignRenewal_NoticeGiven_Refused(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "signrenewalnotice")

	appKey, applicantKey, unitKey := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBnoticeS7ntHJKMNPQR")
	setProfile(t, ctx, conn, cp, cons, "noticeSignProf", appKey, unitKey, map[string]any{
		"annualIncome": 40000, "employmentStatus": "employed",
	}, processor.OutcomeAccepted)
	renewalKey := openRenewalHelper(t, ctx, conn, cp, cons, appKey)
	setRenewalTerms(t, ctx, conn, cp, cons, "noticeTerms01", renewalKey, 2500, 12, processor.OutcomeAccepted)

	notice := gnEnvelope("signRenNot15a", lsActorKey, appKey, gnMoveOut, gnGivenAt, gnOperatorHint(appKey), "")
	testutil.PublishOp(t, conn, notice)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	before := etTenancyRevision(t, ctx, conn, appKey)

	outcome, reply := signRenewalReply(t, ctx, conn, cp, cons, "signRenNot15b", renewalKey, appKey, applicantKey, "2026-12-02T00:00:00Z")
	gnRequireRefused(t, outcome, reply, "NoticeGiven")
	if !strings.Contains(reply.Error.Message, "2027-03-31 (UTC)") {
		t.Fatalf("the refusal must name the move-out's UTC calendar date, got %q", reply.Error.Message)
	}
	if got := etTenancyRevision(t, ctx, conn, appKey); got != before {
		t.Fatalf("a refused SignRenewal must not rewrite .tenancy (revision %d → %d)", before, got)
	}
	if keyExists(t, ctx, conn, renewalKey+".renewalSignature") {
		t.Fatalf("a refused SignRenewal must not write .renewalSignature")
	}
	rn := readDoc(t, ctx, conn, renewalKey)
	if got, _ := rn["data"].(map[string]any)["status"].(string); got != "open" {
		t.Fatalf("renewal status = %q, want open (the refusal leaves the cycle as it was)", got)
	}

	// The positive twin: the same cycle on a lease WITHOUT a notice signs —
	// so the refusal above is the notice talking, not another guard.
	appKey2, applicant2, unit2 := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBnoticeT8ntHJKMNPQS")
	setProfile(t, ctx, conn, cp, cons, "noticeSignProf2", appKey2, unit2, map[string]any{
		"annualIncome": 40000, "employmentStatus": "employed",
	}, processor.OutcomeAccepted)
	renewal2 := openRenewalHelper(t, ctx, conn, cp, cons, appKey2)
	setRenewalTerms(t, ctx, conn, cp, cons, "noticeTerms02", renewal2, 2500, 12, processor.OutcomeAccepted)
	outcome, reply = signRenewalReply(t, ctx, conn, cp, cons, "signRenNot15c", renewal2, appKey2, applicant2, "2026-12-02T00:00:00Z")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("SignRenewal without a notice: outcome = %v, want Accepted (reply %+v)", outcome, reply)
	}
}
