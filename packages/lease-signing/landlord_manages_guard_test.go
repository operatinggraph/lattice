// The landlord ownership probe. A signed-in landlord holds no worksAt link and
// authorizes the leasing/renewal decisions through a scope=self grant, so the
// only thing confining them is their `manages` link to the unit under the
// write. These tests drive that guard as a real actor through the pipeline: the
// same landlord, the same op, two units — one they manage, one they do not.
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
	leasesigning "github.com/operatinggraph/lattice/packages/lease-signing"
)

const (
	llLandlordID  = "BBLeaseLandArdHJKMNP"
	llLandlordKey = "vtx.identity." + llLandlordID
	llLandlordCap = "cap.identity." + llLandlordID
)

// llLandlordCapDoc is the plain signed-in landlord: the `consumer` role and
// scope=SELF grants only — no operator, no frontOfHouse, no standing scope=any
// path anywhere. Step 3 therefore denies unless authContext.target == actor,
// which is exactly the path require_manages binds.
func llLandlordCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    llLandlordCap,
		Actor:                  llLandlordKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{llLandlordKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "DecideLeaseApplication", Scope: "self"},
			{OperationType: "SetRenewalTerms", Scope: "self"},
			{OperationType: "VerifyGuarantor", Scope: "self"},
			{OperationType: "CancelRenewal", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "consumer")},
	}
}

// llSeedManages wires the landlord→unit management link the guard reads.
func llSeedManages(t *testing.T, ctx context.Context, conn *substrate.Conn, unitKey string) {
	t.Helper()
	_, unitID, _ := substrate.ParseVertexKey(unitKey)
	testutil.SeedLink(t, ctx, conn,
		"lnk.identity."+llLandlordID+".manages.unit."+unitID,
		"manages", llLandlordKey, unitKey)
}

// llTombstoneManages soft-deletes the management link, the state RemoveUnitOwner
// leaves behind (loftspace-domain is not installed in this package's fixture, so
// the link document is written directly).
func llTombstoneManages(t *testing.T, ctx context.Context, conn *substrate.Conn, unitKey string) {
	t.Helper()
	_, unitID, _ := substrate.ParseVertexKey(unitKey)
	doc, _ := json.Marshal(map[string]any{
		"class": "manages", "isDeleted": true,
		"sourceVertex": llLandlordKey, "targetVertex": unitKey,
		"localName": "manages", "data": map[string]any{},
	})
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket,
		"lnk.identity."+llLandlordID+".manages.unit."+unitID, doc); err != nil {
		t.Fatalf("tombstone manages link: %v", err)
	}
}

// llSubmitAsLandlord submits any of the four ops as the landlord acting as
// themselves (authContext.target == actor — the only shape a scope=self grant
// authorizes at all).
func llSubmitAsLandlord(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	label, opType, class string, payload map[string]any, hint *processor.ContextHint) processor.MessageOutcome {
	t.Helper()
	outcome, _ := llSubmitAsLandlordReply(t, ctx, conn, cp, cons, label, opType, class, payload, hint)
	return outcome
}

// llSubmitAsLandlordReply is the same submission, returning the reply so a test
// can assert WHICH check answered. MessageOutcome collapses every denial into
// "rejected", which is not enough where the managed and unmanaged legs both
// reject and only the error code separates them.
func llSubmitAsLandlordReply(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	label, opType, class string, payload map[string]any, hint *processor.ContextHint) (processor.MessageOutcome, *processor.OperationReply) {
	t.Helper()
	b, _ := json.Marshal(payload)
	enums := testutil.DeclaredEnumerations(opType, llLandlordKey, leasesigning.OpMetas())
	if len(enums) > 0 {
		if hint == nil {
			hint = &processor.ContextHint{}
		}
		hint.Enumerations = append(hint.Enumerations, enums...)
	}
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: opType,
		Actor:         llLandlordKey,
		SubmittedAt:   "2026-07-20T12:00:00Z",
		Class:         class,
		Payload:       json.RawMessage(b),
		ContextHint:   hint,
		AuthContext:   &processor.AuthContext{Target: llLandlordKey},
	}
	return testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
}

// llRejectReason is a rejected reply's script-failure text ("" when accepted).
// A Starlark fail() surfaces as one ScriptFailed code whatever it says, so the
// MESSAGE is the only thing that shows which check answered.
func llRejectReason(reply *processor.OperationReply) string {
	if reply == nil || reply.Error == nil {
		return ""
	}
	return reply.Error.Message
}

func llSetupLandlord(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	testutil.SeedCapDoc(t, ctx, conn, llLandlordCapDoc())
	seedVertex(t, ctx, conn, llLandlordKey, "identity", map[string]any{})
	testutil.SeedHoldsRole(t, ctx, conn, llLandlordKey,
		"vtx.role."+pkgmgr.RoleID("identity-domain", "consumer"))
}

// TestLandlord_DecideConfinedToManagedUnit: the landlord decides applications
// on the unit they manage and is denied on the one they do not. The positive
// leg runs FIRST so a Rejected on the negative is the manages probe talking,
// not a broken scope=self path.
func TestLandlord_DecideConfinedToManagedUnit(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "llDecide")
	llSetupLandlord(t, ctx, conn)

	mine := seedApplicant(t, ctx, conn, "BBDecideMineHJKMNPQR")
	theirs := seedApplicant(t, ctx, conn, "BBDecideThemHJKMNPRS")
	appMine := createApplication(t, ctx, conn, cp, cons, mine)
	appTheirs := createApplication(t, ctx, conn, cp, cons, theirs)
	unitMine, unitTheirs := unitKeyFor(mine), unitKeyFor(theirs)
	llSeedManages(t, ctx, conn, unitMine)

	if got := llSubmitAsLandlord(t, ctx, conn, cp, cons, "llDec1", "DecideLeaseApplication", "leaseapp",
		map[string]any{"leaseAppKey": appMine, "decision": "declined", "unit": unitMine},
		decideReadsFor(appMine, unitMine)); got != processor.OutcomeAccepted {
		t.Fatalf("landlord decides on the unit they MANAGE = %v, want Accepted "+
			"(the positive sibling — if this fails the negative proves nothing)", got)
	}
	if got := llSubmitAsLandlord(t, ctx, conn, cp, cons, "llDec2", "DecideLeaseApplication", "leaseapp",
		map[string]any{"leaseAppKey": appTheirs, "decision": "declined", "unit": unitTheirs},
		decideReadsFor(appTheirs, unitTheirs)); got != processor.OutcomeRejected {
		t.Fatalf("landlord decides on a unit they do NOT manage = %v, want Rejected", got)
	}
	// A decision is TERMINAL, so one written here would be unrecoverable.
	if keyExists(t, ctx, conn, appTheirs+".decision") {
		t.Errorf("the denied decide wrote %s.decision; it must be denied before any mutation", appTheirs)
	}
}

// llTombstoneVertex soft-deletes a vertex in place, the state
// WithdrawLeaseApplication leaves behind: the document stays, isDeleted flips,
// and the leaseapp's own links (renews, appliesToUnit) are deliberately NOT
// cascaded.
func llTombstoneVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class string) {
	t.Helper()
	doc, _ := json.Marshal(map[string]any{
		"class": class, "isDeleted": true, "data": map[string]any{},
	})
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, doc); err != nil {
		t.Fatalf("tombstone vertex %s: %v", key, err)
	}
}

// TestLandlord_RenewalOpsDenyOnAWithdrawnApplication covers the OTHER consumer
// of the resolver liveness rule. require_workplace re-reads its candidate
// locations inside worksAt_covers, so a dead one is caught downstream even if a
// resolver hands it over; require_manages does not — it tests the manages LINK
// and nothing else. A landlord's management link outlives the application, so
// renewal_unit walking through a withdrawn leaseapp is the whole authorization.
//
// The landlord's manages link and the renewal itself stay live throughout. The
// only thing that changes between the two calls is the leaseapp's isDeleted.
func TestLandlord_RenewalOpsDenyOnAWithdrawnApplication(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "llWithdrawn")
	llSetupLandlord(t, ctx, conn)

	appKey, _, unitKey := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBRenGoneAntHJKMNPTV")
	llSeedManages(t, ctx, conn, unitKey)
	renewalKey := openRenewalHelper(t, ctx, conn, cp, cons, appKey)

	termsHint := &processor.ContextHint{
		Reads:         []string{renewalKey},
		OptionalReads: []string{renewalKey + ".renewalSignature"},
	}

	// POSITIVE SIBLING: the two-hop walk resolves and the manages link answers.
	if got := llSubmitAsLandlord(t, ctx, conn, cp, cons, "llGone1", "SetRenewalTerms", "renewal",
		map[string]any{"renewalKey": renewalKey, "rentAmount": 2400, "termMonths": 12},
		termsHint); got != processor.OutcomeAccepted {
		t.Fatalf("landlord sets terms while the application is LIVE = %v, want Accepted "+
			"(the positive sibling — if this fails the negative proves nothing)", got)
	}

	// Withdraw the application. Its renews + appliesToUnit links stay live, so
	// the walk still has a path to the unit — only the vertex is gone.
	llTombstoneVertex(t, ctx, conn, appKey, "leaseapp")

	got, reply := llSubmitAsLandlordReply(t, ctx, conn, cp, cons, "llGone2", "SetRenewalTerms", "renewal",
		map[string]any{"renewalKey": renewalKey, "rentAmount": 9999, "termMonths": 24},
		termsHint)
	if got != processor.OutcomeRejected {
		t.Fatalf("landlord sets terms on a renewal whose application is WITHDRAWN = %v, want Rejected — "+
			"a withdrawn application must not keep authorizing its renewal legs", got)
	}
	if why := llRejectReason(reply); !strings.Contains(why, "no unit resolves") {
		t.Errorf("the withdrawn-application denial said %q, want the resolver's no-unit denial", why)
	}
	// The rejected call named a DIFFERENT rent, so the terms standing after it
	// prove nothing was written.
	terms := readDoc(t, ctx, conn, renewalKey+".terms")
	if d, _ := terms["data"].(map[string]any); d["rentAmount"] != float64(2400) {
		t.Errorf("the denied SetRenewalTerms moved rentAmount to %v; it must be denied before any mutation", d["rentAmount"])
	}
}

// TestLandlord_RenewalOpsConfinedToManagedUnit walks the three renewal legs.
// The unit is two hops away (renewal→renews→leaseapp→appliesToUnit→unit), so
// this is the walk under test as much as the link read.
func TestLandlord_RenewalOpsConfinedToManagedUnit(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "llRenewal")
	llSetupLandlord(t, ctx, conn)

	appMine, applicantMine, unitMine := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBRenMineAntHJKMNPST")
	appTheirs, applicantTheirs, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBRenThemAntHJKMNPTU")
	llSeedManages(t, ctx, conn, unitMine)

	// A guarantor-less profile on appMine: VerifyGuarantor now fails closed
	// (ApplicationSignalsMissing) rather than NoGuarantorToVerify when
	// .applicationSignals is absent entirely, so the managed leg below needs a
	// submitted profile to reach the NoGuarantorToVerify branch this test means
	// to contrast against AuthDenied.
	setProfile(t, ctx, conn, cp, cons, "llRenMineProf", appMine, unitMine, map[string]any{
		"annualIncome": 40000, "employmentStatus": "employed",
	}, processor.OutcomeAccepted)

	renewalMine := openRenewalHelper(t, ctx, conn, cp, cons, appMine)
	renewalTheirs := openRenewalHelper(t, ctx, conn, cp, cons, appTheirs)

	termsHint := func(rk string) *processor.ContextHint {
		return &processor.ContextHint{Reads: []string{rk}, OptionalReads: []string{rk + ".renewalSignature"}}
	}

	// SetRenewalTerms — positive sibling first.
	if got := llSubmitAsLandlord(t, ctx, conn, cp, cons, "llTerms1", "SetRenewalTerms", "renewal",
		map[string]any{"renewalKey": renewalMine, "rentAmount": 2400, "termMonths": 12},
		termsHint(renewalMine)); got != processor.OutcomeAccepted {
		t.Fatalf("landlord sets terms on a renewal for the unit they MANAGE = %v, want Accepted "+
			"(the positive sibling — it also proves the two-hop renews→appliesToUnit walk resolves)", got)
	}
	if got := llSubmitAsLandlord(t, ctx, conn, cp, cons, "llTerms2", "SetRenewalTerms", "renewal",
		map[string]any{"renewalKey": renewalTheirs, "rentAmount": 9999, "termMonths": 12},
		termsHint(renewalTheirs)); got != processor.OutcomeRejected {
		t.Fatalf("landlord sets terms on another landlord's renewal = %v, want Rejected", got)
	}
	if keyExists(t, ctx, conn, renewalTheirs+".terms") {
		t.Errorf("the denied SetRenewalTerms wrote %s.terms", renewalTheirs)
	}

	// VerifyGuarantor — the probe must answer BEFORE the leaseApp's
	// .applicationSignals is read, so an unmanaged cycle cannot be used to learn
	// who the applicant is.
	// Neither cycle has a guarantor on file, so the MANAGED one rejects too —
	// but on NoGuarantorToVerify, having passed the ownership probe. The
	// discriminator is therefore the error code, not the outcome.
	verifyHint := func(rk, ak, applicant string) *processor.ContextHint {
		return &processor.ContextHint{
			Reads:         []string{rk, renewsLinkKey(rk, ak), applicationForLinkKey(ak, applicant)},
			OptionalReads: []string{ak + ".applicationSignals"},
		}
	}
	_, unmanagedReply := llSubmitAsLandlordReply(t, ctx, conn, cp, cons, "llVerify1", "VerifyGuarantor", "renewal",
		map[string]any{"renewalKey": renewalTheirs, "leaseApp": appTheirs, "applicant": applicantTheirs},
		verifyHint(renewalTheirs, appTheirs, applicantTheirs))
	if reason := llRejectReason(unmanagedReply); !strings.Contains(reason, "AuthDenied:") {
		t.Fatalf("VerifyGuarantor on another landlord's renewal rejected with %q, want AuthDenied — "+
			"anything else means a probe answered before the ownership guard and leaked "+
			"something about a cycle this caller does not own", reason)
	}
	// The managed sibling rejects too (neither cycle has a guarantor on file),
	// but for a DIFFERENT reason — which is what proves the AuthDenied above
	// came from the ownership guard and not from the shared no-guarantor branch.
	_, managedReply := llSubmitAsLandlordReply(t, ctx, conn, cp, cons, "llVerify2", "VerifyGuarantor", "renewal",
		map[string]any{"renewalKey": renewalMine, "leaseApp": appMine, "applicant": applicantMine},
		verifyHint(renewalMine, appMine, applicantMine))
	if reason := llRejectReason(managedReply); !strings.Contains(reason, "NoGuarantorToVerify") {
		t.Fatalf("VerifyGuarantor on the landlord's OWN renewal rejected with %q, want "+
			"NoGuarantorToVerify — the managed leg must get PAST the ownership guard, "+
			"or the negative above proves nothing", reason)
	}

	// CancelRenewal — terminal, so a leak here loses the other landlord's cycle.
	if got := llSubmitAsLandlord(t, ctx, conn, cp, cons, "llCancel1", "CancelRenewal", "renewal",
		map[string]any{"renewalKey": renewalTheirs, "reason": "not mine to cancel"},
		termsHint(renewalTheirs)); got != processor.OutcomeRejected {
		t.Fatalf("landlord cancels another landlord's renewal = %v, want Rejected", got)
	}
	if got := llSubmitAsLandlord(t, ctx, conn, cp, cons, "llCancel2", "CancelRenewal", "renewal",
		map[string]any{"renewalKey": renewalMine, "reason": "declining this cycle"},
		termsHint(renewalMine)); got != processor.OutcomeAccepted {
		t.Fatalf("landlord cancels their OWN renewal = %v, want Accepted", got)
	}
}

// TestLandlord_ManagesRevokedDeniesNextWrite: the probe reads the link live, so
// tombstoning the management link denies the very next write. A guard that
// cached ownership at any earlier beat would keep accepting.
func TestLandlord_ManagesRevokedDeniesNextWrite(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "llRevoke")
	llSetupLandlord(t, ctx, conn)

	applicant := seedApplicant(t, ctx, conn, "BBRevokeAppHJKMNPUVW")
	appKey := createApplication(t, ctx, conn, cp, cons, applicant)
	unitKey := unitKeyFor(applicant)
	llSeedManages(t, ctx, conn, unitKey)

	if got := llSubmitAsLandlord(t, ctx, conn, cp, cons, "llRev1", "DecideLeaseApplication", "leaseapp",
		map[string]any{"leaseAppKey": appKey, "decision": "declined", "unit": unitKey},
		decideReadsFor(appKey, unitKey)); got != processor.OutcomeAccepted {
		t.Fatalf("landlord decides while managing = %v, want Accepted", got)
	}

	llTombstoneManages(t, ctx, conn, unitKey)

	// A re-submission of the SAME terminal decision is otherwise idempotent and
	// accepted, so an Accepted here would be the guard failing open, not the
	// lifecycle guard bouncing it.
	if got := llSubmitAsLandlord(t, ctx, conn, cp, cons, "llRev2", "DecideLeaseApplication", "leaseapp",
		map[string]any{"leaseAppKey": appKey, "decision": "declined", "unit": unitKey},
		decideReadsFor(appKey, unitKey)); got != processor.OutcomeRejected {
		t.Fatalf("landlord decides after the manages link was tombstoned = %v, want Rejected", got)
	}
}
