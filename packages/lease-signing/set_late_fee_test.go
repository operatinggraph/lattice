// SetLateFee op vectors through the real install + Processor pipeline
// (docs/reviews/loftspace-ledger-reversal-and-late-fee-2026-09-18.md
// decision 5): the landlord self leg bound by the management link on the
// application's own unit, the operator's standing leg, every state refusal
// with its vector, the amount validation, and the create-or-update .lateFee.
// External test package, mirroring give_notice_ops_test.go's harness.
package leasesigning_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// lfLandlord is a signed-in landlord holding the consumer role and the
// SetLateFee scope=self grant alone — the hat the manages probe admits.
const (
	lfLandlordID  = "BBLateFeeLandHJKMNPQ"
	lfLandlordKey = "vtx.identity." + lfLandlordID
	lfOtherID     = "BBLateFeeQthrHJKMNPQ"
	lfOtherKey    = "vtx.identity." + lfOtherID
	lfSetAt       = "2026-09-18T15:00:00Z"
)

// lfSeedSelfCap grants actorKey the consumer's SetLateFee scope=self
// permission and the consumer role — a plain signed-in resident or landlord,
// no standing path anywhere.
func lfSeedSelfCap(t *testing.T, ctx context.Context, conn *substrate.Conn, actorKey string) {
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
			{OperationType: "SetLateFee", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "consumer")},
	})
	testutil.SeedHoldsRole(t, ctx, conn, actorKey, "vtx.role."+pkgmgr.RoleID("identity-domain", "consumer"))
}

// lfSeedLandlord seeds a landlord identity, its self cap, and its manages
// link to unitKey.
func lfSeedLandlord(t *testing.T, ctx context.Context, conn *substrate.Conn, landlordKey, unitKey string) {
	t.Helper()
	seedVertex(t, ctx, conn, landlordKey, "identity", map[string]any{})
	lfSeedSelfCap(t, ctx, conn, landlordKey)
	testutil.SeedLink(t, ctx, conn, lfManagesLink(landlordKey, unitKey), "manages", landlordKey, unitKey)
}

// lfManagesLink is the deterministic management link the app's landlord
// submit declares as an OptionalRead.
func lfManagesLink(actorKey, unitKey string) string {
	return "lnk.identity." + actorKey[len("vtx.identity."):] + ".manages.unit." + unitKey[len("vtx.unit."):]
}

// lfHint is the descriptor's declared read set: the application as a
// required read, its .tenancy and .lateFee as the absence-tolerant ones, plus
// the descriptor's two walks — the actor's holdsRole (the operator exemption)
// and the application's appliesToUnit (the confinement's subject). extra
// optionalReads model the app's declaration of the landlord's manages link.
func lfHint(t *testing.T, appKey, actorKey string, extraOptional ...string) *processor.ContextHint {
	t.Helper()
	return &processor.ContextHint{
		Reads:         []string{appKey},
		OptionalReads: append([]string{appKey + ".tenancy", appKey + ".lateFee"}, extraOptional...),
		Enumerations:  declaredEnumerationsBound("SetLateFee", actorKey, appKey),
	}
}

// lfEnvelope builds a SetLateFee envelope; amount is raw JSON so a fraction
// or a string can be sent. A non-empty authTarget sends the scope=self
// authContext (target == actor, the only shape step 3 admits on a self grant).
func lfEnvelope(label, actor, appKey, amount string, hint *processor.ContextHint, authTarget string) *processor.OperationEnvelope {
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SetLateFee",
		Actor:         actor,
		SubmittedAt:   lfSetAt,
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + appKey + `","amountCents":` + amount + `}`),
		ContextHint:   hint,
	}
	if authTarget != "" {
		env.AuthContext = &processor.AuthContext{Target: authTarget}
	}
	return env
}

func lfSubmit(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	env *processor.OperationEnvelope) (processor.MessageOutcome, *processor.OperationReply) {
	t.Helper()
	return testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
}

// lfRequireFee asserts the recorded .lateFee: class leaseLateFee, the amount
// as an integer, recordedAt = the op's own submittedAt.
func lfRequireFee(t *testing.T, ctx context.Context, conn *substrate.Conn, appKey string, wantCents float64, wantRecordedAt string) map[string]any {
	t.Helper()
	doc := readDoc(t, ctx, conn, appKey+".lateFee")
	if got, _ := doc["class"].(string); got != "leaseLateFee" {
		t.Fatalf(".lateFee class = %q, want leaseLateFee", got)
	}
	data, _ := doc["data"].(map[string]any)
	if got, _ := data["amountCents"].(float64); got != wantCents {
		t.Fatalf(".lateFee.amountCents = %v, want %v", data["amountCents"], wantCents)
	}
	if got, _ := data["recordedAt"].(string); got != wantRecordedAt {
		t.Fatalf(".lateFee.recordedAt = %q, want the op's submittedAt %q", got, wantRecordedAt)
	}
	return doc
}

func lfRequireRefused(t *testing.T, ctx context.Context, conn *substrate.Conn, outcome processor.MessageOutcome, reply *processor.OperationReply, code, appKey string) {
	t.Helper()
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want Rejected (%s); reply %+v", outcome, code, reply)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, code) {
		t.Fatalf("want a %s refusal, got %+v", code, reply.Error)
	}
	if keyExists(t, ctx, conn, appKey+".lateFee") {
		t.Fatalf("a %s refusal must write no .lateFee", code)
	}
}

// TestSetLateFee_ManagingLandlordRecordsTheTerm is the positive vector: the
// landlord of the application's unit, signed in as themselves on the
// consumer scope=self grant with their manages link declared, records a
// $50.00 fee on an approved lease. .lateFee lands {amountCents: 5000,
// recordedAt: the op's submittedAt}, leaseapp.lateFeeSet is emitted, and the
// reply names the application.
func TestSetLateFee_ManagingLandlordRecordsTheTerm(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "latefeelandlord")

	appKey, _, unitKey := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBLateFeeA1ppHJKMNPQ")
	lfSeedLandlord(t, ctx, conn, lfLandlordKey, unitKey)

	env := lfEnvelope("lateFee0001", lfLandlordKey, appKey, "5000",
		lfHint(t, appKey, lfLandlordKey, lfManagesLink(lfLandlordKey, unitKey)), lfLandlordKey)
	outcome, reply := lfSubmit(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply %+v)", outcome, reply)
	}
	if reply.PrimaryKey != appKey {
		t.Fatalf("primaryKey = %q, want %q", reply.PrimaryKey, appKey)
	}
	lfRequireFee(t, ctx, conn, appKey, 5000, lfSetAt)

	ev := findEmittedEvent(t, ctx, conn, env.RequestID, "leaseapp.lateFeeSet")
	if got, _ := ev["leaseAppKey"].(string); got != appKey {
		t.Fatalf("lateFeeSet.leaseAppKey = %q, want %q", got, appKey)
	}
	if got, _ := ev["amountCents"].(float64); got != 5000 {
		t.Fatalf("lateFeeSet.amountCents = %v, want 5000", ev["amountCents"])
	}
}

// TestSetLateFee_AmendmentRewritesTheTerm — a second set on a lease that
// already carries a fee rewrites the aspect whole (the new amount, a fresh
// recordedAt) through the update arm: the aspect's revision advances rather
// than a second document appearing, and the create arm's absence condition
// is not what serialized it.
func TestSetLateFee_AmendmentRewritesTheTerm(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "latefeeamend")

	appKey, _, unitKey := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBLateFeeB2ppHJKMNPQ")
	lfSeedLandlord(t, ctx, conn, lfLandlordKey, unitKey)
	hint := lfHint(t, appKey, lfLandlordKey, lfManagesLink(lfLandlordKey, unitKey))

	if outcome, reply := lfSubmit(t, ctx, conn, cp, cons, lfEnvelope("lateFee0002", lfLandlordKey, appKey, "5000", hint, lfLandlordKey)); outcome != processor.OutcomeAccepted {
		t.Fatalf("first set = %v, want Accepted (reply %+v)", outcome, reply)
	}
	first, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, appKey+".lateFee")
	if err != nil {
		t.Fatalf("read .lateFee: %v", err)
	}

	amend := lfEnvelope("lateFee0003", lfLandlordKey, appKey, "7500", hint, lfLandlordKey)
	amend.SubmittedAt = "2026-10-01T09:00:00Z"
	if outcome, reply := lfSubmit(t, ctx, conn, cp, cons, amend); outcome != processor.OutcomeAccepted {
		t.Fatalf("amendment = %v, want Accepted (reply %+v)", outcome, reply)
	}
	lfRequireFee(t, ctx, conn, appKey, 7500, "2026-10-01T09:00:00Z")
	second, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, appKey+".lateFee")
	if err != nil {
		t.Fatalf("read .lateFee: %v", err)
	}
	if second.Revision <= first.Revision {
		t.Fatalf("the amendment must advance the aspect's revision (%d → %d)", first.Revision, second.Revision)
	}
}

// TestSetLateFee_TenantRefused — the application's own applicant, holding
// the same consumer self grant every consumer holds, is refused AuthDenied by
// the manages probe: a tenant does not set the terms of their own lease.
func TestSetLateFee_TenantRefused(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "latefeetenant")

	appKey, applicantKey, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBLateFeeC3ppHJKMNPQ")
	lfSeedSelfCap(t, ctx, conn, applicantKey)

	outcome, reply := lfSubmit(t, ctx, conn, cp, cons, lfEnvelope("lateFee0004", applicantKey, appKey, "5000",
		lfHint(t, appKey, applicantKey), applicantKey))
	lfRequireRefused(t, ctx, conn, outcome, reply, "AuthDenied", appKey)
}

// TestSetLateFee_OtherLandlordRefused — a landlord who manages a DIFFERENT
// unit, their manages link to that unit declared, is refused AuthDenied: the
// probe reads the link to the application's OWN unit and nothing else.
func TestSetLateFee_OtherLandlordRefused(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "latefeeother")

	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBLateFeeD4ppHJKMNPQ")
	_, _, otherUnit := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBLateFeeE5ppHJKMNPR")
	lfSeedLandlord(t, ctx, conn, lfOtherKey, otherUnit)

	outcome, reply := lfSubmit(t, ctx, conn, cp, cons, lfEnvelope("lateFee0005", lfOtherKey, appKey, "5000",
		lfHint(t, appKey, lfOtherKey, lfManagesLink(lfOtherKey, otherUnit)), lfOtherKey))
	lfRequireRefused(t, ctx, conn, outcome, reply, "AuthDenied", appKey)
}

// TestSetLateFee_UndeclaredManagesLinkStillBinds — the manages probe is
// require_manages's own class-(e) follow-up off the application's
// appliesToUnit walk (the DecideLeaseApplication posture), so it runs
// whatever the submitter declared: the non-managing landlord is refused
// AuthDenied without the declaration, and the managing landlord is admitted
// without it — the app's declared OptionalRead serves the read from the
// snapshot, it never decides the bind.
func TestSetLateFee_UndeclaredManagesLinkStillBinds(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "latefeeundecl")

	appKey, _, unitKey := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBLateFeeF6ppHJKMNPQ")
	lfSeedLandlord(t, ctx, conn, lfLandlordKey, unitKey)
	seedVertex(t, ctx, conn, lfOtherKey, "identity", map[string]any{})
	lfSeedSelfCap(t, ctx, conn, lfOtherKey)

	outcome, reply := lfSubmit(t, ctx, conn, cp, cons, lfEnvelope("lateFee0006", lfOtherKey, appKey, "5000",
		lfHint(t, appKey, lfOtherKey), lfOtherKey))
	lfRequireRefused(t, ctx, conn, outcome, reply, "AuthDenied", appKey)

	outcome, reply = lfSubmit(t, ctx, conn, cp, cons, lfEnvelope("lateFee0007", lfLandlordKey, appKey, "5000",
		lfHint(t, appKey, lfLandlordKey), lfLandlordKey))
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("the managing landlord without the declaration = %v, want Accepted (reply %+v) — the bind is the script's own read, not the declaration", outcome, reply)
	}
	lfRequireFee(t, ctx, conn, appKey, 5000, lfSetAt)
}

// TestSetLateFee_OperatorRecordsTheTerm — the standing operator grant admits
// the op with no target and no manages link at all.
func TestSetLateFee_OperatorRecordsTheTerm(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "latefeeoperator")

	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBLateFeeG7ppHJKMNPQ")

	outcome, reply := lfSubmit(t, ctx, conn, cp, cons, lfEnvelope("lateFee0008", lsActorKey, appKey, "5000",
		lfHint(t, appKey, lsActorKey), ""))
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply %+v)", outcome, reply)
	}
	lfRequireFee(t, ctx, conn, appKey, 5000, lfSetAt)
}

// TestSetLateFee_UndecidedApplicationRefused — an application with no
// .tenancy (never approved) is refused NotApproved: a fee is a term of a
// lease, not of an offer. Run as the operator so the refusal is the state
// check's, not the bind's.
func TestSetLateFee_UndecidedApplicationRefused(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "latefeeundecided")

	applicantKey := seedApplicant(t, ctx, conn, "BBLateFeeH8ppHJKMNPQ")
	appKey := createApplication(t, ctx, conn, cp, cons, applicantKey)

	outcome, reply := lfSubmit(t, ctx, conn, cp, cons, lfEnvelope("lateFee0009", lsActorKey, appKey, "5000",
		lfHint(t, appKey, lsActorKey), ""))
	lfRequireRefused(t, ctx, conn, outcome, reply, "NotApproved", appKey)
}

// TestSetLateFee_EndedTenancyRefused — a tenancy whose endedAt is recorded is
// refused TenancyEnded, naming the end's UTC calendar day.
func TestSetLateFee_EndedTenancyRefused(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "latefeeended")

	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBLateFeeJ9ppHJKMNPQ")
	tdoc := readDoc(t, ctx, conn, appKey+".tenancy")
	tdata, _ := tdoc["data"].(map[string]any)
	tdata["endedAt"] = "2027-08-01T00:00:00Z"
	seedAspect(t, ctx, conn, appKey, "tenancy", "tenancy", tdata)

	outcome, reply := lfSubmit(t, ctx, conn, cp, cons, lfEnvelope("lateFee0010", lsActorKey, appKey, "5000",
		lfHint(t, appKey, lsActorKey), ""))
	lfRequireRefused(t, ctx, conn, outcome, reply, "TenancyEnded", appKey)
	if !strings.Contains(reply.Error.Message, "2027-08-01") {
		t.Fatalf("the refusal names the end's UTC day, got %q", reply.Error.Message)
	}

	// A malformed endedAt (a number, a boolean) is still the recorded fact
	// that the tenancy ended — the refusal never falls open on its shape.
	for i, malformed := range []any{20270801, true, ""} {
		bad := map[string]any{}
		for k, v := range tdata {
			bad[k] = v
		}
		bad["endedAt"] = malformed
		seedAspect(t, ctx, conn, appKey, "tenancy", "tenancy", bad)
		outcome, reply := lfSubmit(t, ctx, conn, cp, cons, lfEnvelope("lateFee0010"+string(rune('A'+i)), lsActorKey, appKey, "5000",
			lfHint(t, appKey, lsActorKey), ""))
		lfRequireRefused(t, ctx, conn, outcome, reply, "TenancyEnded", appKey)
	}
}

// TestSetLateFee_AmountRefusals — zero, negative, fractional, string, absent
// and over-the-ceiling amounts are each refused InvalidArgument with nothing
// written; the ceiling itself and a whole-cent float are admitted.
func TestSetLateFee_AmountRefusals(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "latefeeamount")

	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBLateFeeK1ppHJKMNPQ")

	for i, amount := range []string{"0", "-500", "50.5", `"5000"`, "null"} {
		outcome, reply := lfSubmit(t, ctx, conn, cp, cons, lfEnvelope("lateFee0011"+string(rune('A'+i)), lsActorKey, appKey, amount,
			lfHint(t, appKey, lsActorKey), ""))
		lfRequireRefused(t, ctx, conn, outcome, reply, "InvalidArgument: amountCents", appKey)
	}
	// One million dollars is the ceiling: a cent over is refused, the
	// ceiling itself admitted.
	outcome, reply := lfSubmit(t, ctx, conn, cp, cons, lfEnvelope("lateFee0011X", lsActorKey, appKey, "100000001",
		lfHint(t, appKey, lsActorKey), ""))
	lfRequireRefused(t, ctx, conn, outcome, reply, "InvalidArgument: amountCents: at most 100000000", appKey)
	outcome, reply = lfSubmit(t, ctx, conn, cp, cons, lfEnvelope("lateFee0011Y", lsActorKey, appKey, "100000000",
		lfHint(t, appKey, lsActorKey), ""))
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("the ceiling itself = %v, want Accepted (reply %+v)", outcome, reply)
	}
	lfRequireFee(t, ctx, conn, appKey, 100000000, lfSetAt)
	// A whole-cent float is the integer it names.
	outcome, reply = lfSubmit(t, ctx, conn, cp, cons, lfEnvelope("lateFee0012", lsActorKey, appKey, "5000.0",
		lfHint(t, appKey, lsActorKey), ""))
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("5000.0 = %v, want Accepted (reply %+v)", outcome, reply)
	}
	lfRequireFee(t, ctx, conn, appKey, 5000, lfSetAt)
}
