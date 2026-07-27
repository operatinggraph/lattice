// Workplace confinement for the three staff-widened wellness writes:
// CreateStudio, CreateBooking and CancelBooking each grant `frontOfHouse` at
// scope=any (permissions.go), and scope=any carries no platform-checked target,
// so the ONLY thing keeping a front-desk hat inside its own building is the
// script's workplace walk. Every case below therefore leads with the positive
// sibling — the same call at the staffer's OWN location, which must be Accepted
// — so a Rejected is the confinement guard talking and not a broken path.
//
// The forged-target vector lives in authcontext_target_forgery_test.go: it
// targets CreateSession because that op has no scope=self path and so no
// ownership proof behind the guard. The two booking ops here DO have one, which
// is what lets them exempt the validated-target path safely.
package wellnessdomain_test

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

const (
	wcStaffID  = "BBWELLWKPLACESTFFHJK"
	wcStaffKey = "vtx.identity." + wcStaffID
	wcStaffCap = "cap.identity." + wcStaffID

	wcMemberID  = "BBWELLWKPLACEMEMBRHJ"
	wcMember2ID = "BBWELLWKPLACEMEMBRJK"

	wcBuildingAID = "BBWELLWKPLACEBLDGAHJ"
	wcBuildingBID = "BBWELLWKPLACEBLDGBHJ"

	wcBuildingAKey = "vtx.building." + wcBuildingAID
	wcBuildingBKey = "vtx.building." + wcBuildingBID
)

// wcStaffCapDoc holds exactly the scope=any grants a front-of-house hat gets —
// the same rows the operator holds, since the capability plane cannot tell
// staff from root. If confinement holds, it holds entirely inside the script.
func wcStaffCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    wcStaffCap,
		Actor:                  wcStaffKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{wcStaffKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateStudio", Scope: "any"},
			{OperationType: "CreateBooking", Scope: "any"},
			{OperationType: "CancelBooking", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")},
	}
}

// wcSeedStaff wires a front-of-house identity that worksAt building A, plus
// both buildings. The holdsRole link matters as much as the cap doc: the
// operator escape is resolved from the GRAPH, so a staffer that never holds it
// is confined, and the operator actor the other helpers submit as is not.
func wcSeedStaff(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	seedVertex(t, ctx, conn, wcStaffKey, "identity", map[string]any{})
	seedVertex(t, ctx, conn, wcBuildingAKey, "location", map[string]any{})
	seedVertex(t, ctx, conn, wcBuildingBKey, "location", map[string]any{})
	testutil.SeedLink(t, ctx, conn,
		"lnk.identity."+wcStaffID+".worksAt.building."+wcBuildingAID,
		"worksAt", wcStaffKey, wcBuildingAKey)
	testutil.SeedHoldsRole(t, ctx, conn, wcStaffKey,
		"vtx.role."+pkgmgr.RoleID("identity-domain", "frontOfHouse"))
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())
}

// wcCreateStudioAs submits CreateStudio as an arbitrary actor. An empty
// locationKey omits the optional payload field entirely — the unlocated studio
// the guard must deny to everyone but an operator.
func wcCreateStudioAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	label, name, locationKey, actorKey string) (string, processor.MessageOutcome) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	payloadMap := map[string]any{"name": name}
	var reads []string
	if locationKey != "" {
		payloadMap["location"] = locationKey
		reads = []string{locationKey}
	}
	payload, _ := json.Marshal(payloadMap)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateStudio",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "studio",
		Payload:       payload,
		ContextHint:   &processor.ContextHint{Reads: reads},
	}
	testutil.PublishOp(t, conn, env)
	return "vtx.studio." + nanoIDFromRequestID(reqID), testutil.DriveOne(t, ctx, cp, cons, "")
}

// wcCreateBookingAs mirrors createBooking's declared read posture, submitting as
// an arbitrary actor with no resident-rate lookup.
func wcCreateBookingAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	label, sessionKey, bookerKey, actorKey string) (string, processor.MessageOutcome) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	payload, _ := json.Marshal(map[string]any{"session": sessionKey, "booker": bookerKey})
	_, bookerID, _ := substrate.ParseVertexKey(bookerKey)
	optionalReads := append(wdSeatKeys(sessionKey, 20), sessionKey+".bkr"+bookerID)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateBooking",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "booking",
		Payload:       payload,
		ContextHint: &processor.ContextHint{
			Reads:         []string{sessionKey, sessionKey + ".schedule", bookerKey},
			OptionalReads: optionalReads,
		},
	}
	testutil.PublishOp(t, conn, env)
	return "vtx.booking." + nanoIDFromRequestID(reqID), testutil.DriveOne(t, ctx, cp, cons, "")
}

// wcCreateBookingAsWithReason is wcCreateBookingAs plus the script's own failure
// text, so a rejection can be attributed to the confinement guard rather than to
// whichever other gate happened to answer first.
func wcCreateBookingAsWithReason(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	label, sessionKey, bookerKey, actorKey string) (string, processor.MessageOutcome, string) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	payload, _ := json.Marshal(map[string]any{"session": sessionKey, "booker": bookerKey})
	_, bookerID, _ := substrate.ParseVertexKey(bookerKey)
	optionalReads := append(wdSeatKeys(sessionKey, 20), sessionKey+".bkr"+bookerID)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateBooking",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "booking",
		Payload:       payload,
		ContextHint: &processor.ContextHint{
			Reads:         []string{sessionKey, sessionKey + ".schedule", bookerKey},
			OptionalReads: optionalReads,
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	failure := ""
	if reply != nil && reply.Error != nil {
		if i := strings.Index(reply.Error.Message, "fail: "); i >= 0 {
			failure = reply.Error.Message[i+len("fail: "):]
		}
	}
	return "vtx.booking." + nanoIDFromRequestID(reqID), outcome, failure
}

// wcCancelBookingAs submits CancelBooking as an arbitrary actor and returns the
// outcome plus the script's own failure text. sessionKey is the session the
// CALLER names, which is not necessarily the booking's own — that divergence is
// the ordering vector below, so the forSession link is declared OPTIONAL: in
// Reads a miss faults at hydration before the script runs, and the rejection
// would then prove nothing about the in-script order (the shape
// TestCancelBooking_ConsumerSelfScope_SessionProbeRevealsNothing uses).
func wcCancelBookingAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	label, bookingKey, sessionKey, actorKey string) (processor.MessageOutcome, string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"bookingKey": bookingKey, "session": sessionKey})
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "CancelBooking",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-07T12:10:00Z",
		Class:         "booking",
		Payload:       payload,
		ContextHint: &processor.ContextHint{
			Reads:         []string{bookingKey, bookingKey + ".status"},
			OptionalReads: []string{forSessionLnkKey(t, bookingKey, sessionKey)},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	failure := ""
	if reply != nil && reply.Error != nil {
		if i := strings.Index(reply.Error.Message, "fail: "); i >= 0 {
			failure = reply.Error.Message[i+len("fail: "):]
		}
	}
	return outcome, failure
}

// TestWorkplace_CreateStudioConfinedToTheStaffersBuilding: a studio is opened
// AT a location, so the candidate is the location the op is about to link. The
// unlocated case is the one worth pinning — an omitted location yields an empty
// candidate list, and require_workplace denies an empty list rather than falling
// open, so a staffer cannot mint a placeless studio and locate it afterwards.
func TestWorkplace_CreateStudioConfinedToTheStaffersBuilding(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wdwcstudio")
	wcSeedStaff(t, ctx, conn)

	// POSITIVE SIBLING: their own building.
	studioKey, got := wcCreateStudioAs(t, ctx, conn, cp, cons,
		"wdwcstudioa000000001", "Studio A", wcBuildingAKey, wcStaffKey)
	if got != processor.OutcomeAccepted {
		t.Fatalf("staff CreateStudio at its OWN building = %v, want Accepted "+
			"(the positive sibling — if this fails the negatives prove nothing)", got)
	}
	if !keyExists(t, ctx, conn, studioKey) {
		t.Fatalf("%s was not written by the accepted CreateStudio", studioKey)
	}

	crossKey, got := wcCreateStudioAs(t, ctx, conn, cp, cons,
		"wdwcstudiob000000001", "Studio B", wcBuildingBKey, wcStaffKey)
	if got != processor.OutcomeRejected {
		t.Fatalf("staff CreateStudio at ANOTHER building = %v, want Rejected", got)
	}
	if keyExists(t, ctx, conn, crossKey) {
		t.Errorf("the denied cross-building CreateStudio wrote %s; it must be denied before any mutation", crossKey)
	}

	placeless, got := wcCreateStudioAs(t, ctx, conn, cp, cons,
		"wdwcstudion000000001", "Nowhere Studio", "", wcStaffKey)
	if got != processor.OutcomeRejected {
		t.Fatalf("staff CreateStudio with NO location = %v, want Rejected — an empty "+
			"candidate list must deny, not fall open", got)
	}
	if keyExists(t, ctx, conn, placeless) {
		t.Errorf("the denied placeless CreateStudio wrote %s; it must be denied before any mutation", placeless)
	}
	// The operator reaches the same placeless call, so the rejection above is
	// the confinement guard and not the optional field having become required.
	if _, got := wcCreateStudioAs(t, ctx, conn, cp, cons,
		"wdwcstudion000000002", "Nowhere Studio", "", domainActorKey); got != processor.OutcomeAccepted {
		t.Fatalf("operator CreateStudio with NO location = %v, want Accepted — an unlocated "+
			"studio stays legal, it is only the staff path that is confined", got)
	}
}

// TestWorkplace_StaffBookAndCancelConfinedToTheirBuilding walks both booking
// ops over the two-hop resolution (session -atStudio-> studio -locatedAt->
// location) that carries the confinement.
func TestWorkplace_StaffBookAndCancelConfinedToTheirBuilding(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wdwcbooking")
	wcSeedStaff(t, ctx, conn)

	studioA := createStudio(t, ctx, conn, cp, cons, "wdwcbkstudioa0000001", "Studio A")
	studioB := createStudio(t, ctx, conn, cp, cons, "wdwcbkstudiob0000001", "Studio B")
	wfSeedStudioAt(t, ctx, conn, studioA, wcBuildingAKey, wcBuildingAID)
	wfSeedStudioAt(t, ctx, conn, studioB, wcBuildingBKey, wcBuildingBID)

	sessionA, _ := createSession(t, ctx, conn, cp, cons, "wdwcbksessiona000001",
		studioA, "Morning Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	sessionB, _ := createSession(t, ctx, conn, cp, cons, "wdwcbksessionb000001",
		studioB, "Evening Flow", "2026-07-08T18:00:00Z", "2026-07-08T18:30:00Z", 20)
	member := seedIdentity(t, ctx, conn, wcMemberID)

	// POSITIVE SIBLING: booking a member into a class at their own building.
	bookingA, got := wcCreateBookingAs(t, ctx, conn, cp, cons,
		"wdwcbkbooka000000001", sessionA, member, wcStaffKey)
	if got != processor.OutcomeAccepted {
		t.Fatalf("staff CreateBooking at its OWN building = %v, want Accepted "+
			"(the positive sibling — if this fails the negatives prove nothing)", got)
	}

	bookingB, got := wcCreateBookingAs(t, ctx, conn, cp, cons,
		"wdwcbkbookb000000001", sessionB, member, wcStaffKey)
	if got != processor.OutcomeRejected {
		t.Fatalf("staff CreateBooking at ANOTHER building = %v, want Rejected", got)
	}
	if keyExists(t, ctx, conn, bookingB) {
		t.Errorf("the denied cross-building CreateBooking wrote %s; it must be denied before any mutation", bookingB)
	}

	// Cancelling at their own building is the positive sibling for the cancel
	// half; the seat must actually come back, so this is not a no-op accept.
	if got, why := wcCancelBookingAs(t, ctx, conn, cp, cons,
		"wdwcbkcancela0000001", bookingA, sessionA, wcStaffKey); got != processor.OutcomeAccepted {
		t.Fatalf("staff CancelBooking at its OWN building = %v (%s), want Accepted", got, why)
	}
	if keyExists(t, ctx, conn, sessionA+".seat1") {
		t.Errorf("seat1 must be released by the accepted staff CancelBooking")
	}

	// A booking at the other building, made by the operator, stays out of the
	// staffer's reach.
	operatorBookingB, outcome := createBooking(t, ctx, conn, cp, cons,
		"wdwcbkopbookb0000001", sessionB, member, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("operator CreateBooking at building B = %v, want Accepted", outcome)
	}
	got, why := wcCancelBookingAs(t, ctx, conn, cp, cons,
		"wdwcbkcancelb0000001", operatorBookingB, sessionB, wcStaffKey)
	if got != processor.OutcomeRejected {
		t.Fatalf("staff CancelBooking at ANOTHER building = %v, want Rejected", got)
	}
	if !strings.Contains(why, "does not worksAt") {
		t.Errorf("cross-building CancelBooking was rejected by something other than the "+
			"workplace guard: %q", why)
	}
	if !keyExists(t, ctx, conn, operatorBookingB) {
		t.Errorf("the denied cross-building CancelBooking tombstoned %s; it must be denied before any mutation", operatorBookingB)
	}
}

// TestWorkplace_StaffCannotCancelElsewhereByNamingTheirOwnSession pins the
// guard ORDER. CancelBooking's location comes from the session the CALLER
// supplies, so confining before that session is bound to this booking would let
// a staffer name a class at their own building and cancel a seat anywhere.
// require_matching_session answers first, which is what closes it.
func TestWorkplace_StaffCannotCancelElsewhereByNamingTheirOwnSession(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wdwcorder")
	wcSeedStaff(t, ctx, conn)

	studioA := createStudio(t, ctx, conn, cp, cons, "wdwcorstudioa0000001", "Studio A")
	studioB := createStudio(t, ctx, conn, cp, cons, "wdwcorstudiob0000001", "Studio B")
	wfSeedStudioAt(t, ctx, conn, studioA, wcBuildingAKey, wcBuildingAID)
	wfSeedStudioAt(t, ctx, conn, studioB, wcBuildingBKey, wcBuildingBID)

	sessionA, _ := createSession(t, ctx, conn, cp, cons, "wdwcorsessiona000001",
		studioA, "Morning Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	sessionB, _ := createSession(t, ctx, conn, cp, cons, "wdwcorsessionb000001",
		studioB, "Evening Flow", "2026-07-08T18:00:00Z", "2026-07-08T18:30:00Z", 20)
	member := seedIdentity(t, ctx, conn, wcMemberID)

	bookingB, outcome := createBooking(t, ctx, conn, cp, cons,
		"wdwcorbookb00000001", sessionB, member, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("operator CreateBooking at building B = %v, want Accepted", outcome)
	}

	// POSITIVE SIBLING: the staffer's own building answers Accepted, so the
	// substitution below is not simply "this staffer can cancel nothing".
	bookingA, outcome := createBooking(t, ctx, conn, cp, cons,
		"wdwcorbooka00000001", sessionA, member, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("operator CreateBooking at building A = %v, want Accepted", outcome)
	}
	if got, why := wcCancelBookingAs(t, ctx, conn, cp, cons,
		"wdwcorcancelown00001", bookingA, sessionA, wcStaffKey); got != processor.OutcomeAccepted {
		t.Fatalf("staff CancelBooking at its OWN building = %v (%s), want Accepted", got, why)
	}

	// THE SUBSTITUTION: building B's booking, building A's session key.
	got, why := wcCancelBookingAs(t, ctx, conn, cp, cons,
		"wdwcorcancelsub00001", bookingB, sessionA, wcStaffKey)
	if got != processor.OutcomeRejected {
		t.Fatalf("staff CancelBooking naming its OWN session for ANOTHER session's booking = %v, "+
			"want Rejected — the supplied session must be bound to the booking before it can "+
			"carry the confinement", got)
	}
	// It must die on the binding, not on hydration and not on the workplace
	// walk having been handed building A: WrongSession is the guard that makes
	// the order safe.
	if !strings.Contains(why, "WrongSession") {
		t.Errorf("the substituted session was rejected by something other than the booking's "+
			"own session binding: %q", why)
	}
	if !keyExists(t, ctx, conn, bookingB) {
		t.Errorf("the substituted CancelBooking tombstoned %s; it must be denied before any mutation", bookingB)
	}
}

// TestWorkplace_ConsumerSelfServiceIsUnconfined: the two guards are
// complementary, not alternatives. A member holds no worksAt link at all, and
// confining them by a rule written for staff would deny every self-service
// write — so require_workplace returns early on op.authTargetValidated and the
// member's own ownership proof binds that path instead.
func TestWorkplace_ConsumerSelfServiceIsUnconfined(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wdwcself")
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	seedVertex(t, ctx, conn, domainConsumerKey, "identity", map[string]any{})
	seedVertex(t, ctx, conn, wcBuildingAKey, "location", map[string]any{})

	studioA := createStudio(t, ctx, conn, cp, cons, "wdwcslfstudioa000001", "Studio A")
	wfSeedStudioAt(t, ctx, conn, studioA, wcBuildingAKey, wcBuildingAID)
	sessionA, _ := createSession(t, ctx, conn, cp, cons, "wdwcslfsessiona00001",
		studioA, "Morning Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)

	bookingKey, outcome := wcSelfBooking(t, ctx, conn, cp, cons, "wdwcslfbook000000001", sessionA)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("consumer scope=self CreateBooking at a LOCATED studio = %v, want Accepted — "+
			"a member holds no worksAt link, and the staff rule must not reach them", outcome)
	}
	if !keyExists(t, ctx, conn, bookingKey) {
		t.Fatalf("%s was not written by the accepted self-service booking", bookingKey)
	}
}

// wcSelfBooking submits CreateBooking on the consumer's scope=self path — the
// authContext.target the platform validates is what exempts it.
func wcSelfBooking(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, sessionKey string) (string, processor.MessageOutcome) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	payload, _ := json.Marshal(map[string]any{"session": sessionKey, "booker": domainConsumerKey})
	optionalReads := append(wdSeatKeys(sessionKey, 20), sessionKey+".bkr"+domainConsumerID)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateBooking",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "booking",
		Payload:       payload,
		AuthContext:   &processor.AuthContext{Target: domainConsumerKey},
		ContextHint: &processor.ContextHint{
			Reads:         []string{sessionKey, sessionKey + ".schedule", domainConsumerKey},
			OptionalReads: optionalReads,
		},
	}
	testutil.PublishOp(t, conn, env)
	return "vtx.booking." + nanoIDFromRequestID(reqID), testutil.DriveOne(t, ctx, cp, cons, "")
}

// Multi-parent containment. A location may sit under more than one parent (a
// shared room belonging to two buildings), and the read-side half of this guard
// unions every branch — `[:containedIn*0..7]` in wellnessSessionsSpec and
// wellnessMembersSpec. The write side has to compute the same covering set, so
// staff at EITHER parent may write at the shared room.
//
// The pair is what makes this discriminating, and it is deliberately a pair:
// kv.Links returns the room's two containedIn targets in an order the test does
// not control, so asserting one staffer alone could pass against a walk that
// followed a single parent and happened to keep the right one. A walk that keeps
// one parent per level can accept AT MOST one of these two callers, whichever
// branch it retained — so the two together red it under either ordering.
const (
	wcStaffBID  = "BBWELLWKPLACESTFFBHJ"
	wcStaffBKey = "vtx.identity." + wcStaffBID

	wcStaffCID  = "BBWELLWKPLACESTFFCHJ"
	wcStaffCKey = "vtx.identity." + wcStaffCID

	wcBuildingCID  = "BBWELLWKPLACEBLDGCHJ"
	wcBuildingCKey = "vtx.building." + wcBuildingCID

	wcSharedRoomID  = "BBWELLWKPLACERMXYZHJ"
	wcSharedRoomKey = "vtx.location." + wcSharedRoomID
)

// wcSeedStaffAt wires an additional front-of-house identity at one building,
// with the same scope=any grants wcSeedStaff gives — so the only thing that can
// separate two such callers is the workplace walk itself.
func wcSeedStaffAt(t *testing.T, ctx context.Context, conn *substrate.Conn,
	staffKey, staffID, buildingKey, buildingID string) {
	t.Helper()
	seedVertex(t, ctx, conn, staffKey, "identity", map[string]any{})
	testutil.SeedLink(t, ctx, conn,
		"lnk.identity."+staffID+".worksAt.building."+buildingID,
		"worksAt", staffKey, buildingKey)
	testutil.SeedHoldsRole(t, ctx, conn, staffKey,
		"vtx.role."+pkgmgr.RoleID("identity-domain", "frontOfHouse"))
	doc := wcStaffCapDoc()
	doc.Key = "cap.identity." + staffID
	doc.Actor = staffKey
	doc.ProjectedFromRevisions = map[string]uint64{staffKey: 1}
	testutil.SeedCapDoc(t, ctx, conn, doc)
}

func TestWorkplace_SharedRoomCoveredByEveryContainmentParent(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wdwcmultiparent")
	wcSeedStaff(t, ctx, conn) // staff A worksAt building A; buildings A + B exist

	seedVertex(t, ctx, conn, wcBuildingCKey, "location", map[string]any{})
	// The shared room sits under BOTH building A and building B — and under
	// neither C.
	seedVertex(t, ctx, conn, wcSharedRoomKey, "location", map[string]any{})
	testutil.SeedLink(t, ctx, conn,
		"lnk.location."+wcSharedRoomID+".containedIn.building."+wcBuildingAID,
		"containedIn", wcSharedRoomKey, wcBuildingAKey)
	testutil.SeedLink(t, ctx, conn,
		"lnk.location."+wcSharedRoomID+".containedIn.building."+wcBuildingBID,
		"containedIn", wcSharedRoomKey, wcBuildingBKey)

	wcSeedStaffAt(t, ctx, conn, wcStaffBKey, wcStaffBID, wcBuildingBKey, wcBuildingBID)
	wcSeedStaffAt(t, ctx, conn, wcStaffCKey, wcStaffCID, wcBuildingCKey, wcBuildingCID)

	// Staff at parent A.
	studioA, got := wcCreateStudioAs(t, ctx, conn, cp, cons,
		"wdwcmultiparenta0001", "Shared Room Studio A", wcSharedRoomKey, wcStaffKey)
	if got != processor.OutcomeAccepted {
		t.Errorf("staff at containment parent A CreateStudio in the shared room = %v, want Accepted — "+
			"the room is containedIn their building, and the read side unions every branch", got)
	} else if !keyExists(t, ctx, conn, studioA) {
		t.Errorf("%s was not written by the accepted CreateStudio", studioA)
	}

	// Staff at parent B. One of these two is the branch a single-parent walk
	// discards; which one depends on kv.Links ordering, so both must pass.
	studioB, got := wcCreateStudioAs(t, ctx, conn, cp, cons,
		"wdwcmultiparentb0001", "Shared Room Studio B", wcSharedRoomKey, wcStaffBKey)
	if got != processor.OutcomeAccepted {
		t.Errorf("staff at containment parent B CreateStudio in the shared room = %v, want Accepted — "+
			"a walk that keeps one parent per level accepts at most one of these two callers", got)
	} else if !keyExists(t, ctx, conn, studioB) {
		t.Errorf("%s was not written by the accepted CreateStudio", studioB)
	}

	// NEGATIVE SIBLING: the widened walk still confines. Building C is not a
	// parent of the shared room, so its staffer is denied — proving the two
	// acceptances above are the covering set and not the guard falling open.
	studioC, got, why := wcCreateStudioAsWithReason(t, ctx, conn, cp, cons,
		"wdwcmultiparentc0001", "Shared Room Studio C", wcSharedRoomKey, wcStaffCKey)
	if got != processor.OutcomeRejected {
		t.Fatalf("staff at an UNRELATED building CreateStudio in the shared room = %v, want Rejected — "+
			"following every parent must not become following every location", got)
	}
	if !strings.Contains(why, "does not worksAt") {
		t.Errorf("staff C was refused with %q, want the confinement guard's own message — "+
			"a rejection from any other check would leave multi-parent coverage unproven", why)
	}
	if keyExists(t, ctx, conn, studioC) {
		t.Errorf("the denied CreateStudio wrote %s; it must be denied before any mutation", studioC)
	}
}

// wcCreateStudioAsWithReason is wcCreateStudioAs plus the script's own failure
// text, so a negative can assert WHICH check answered. A bare Rejected would
// stay green if some future check ahead of the confinement guard denied for an
// unrelated reason, which would let multi-parent coverage rot silently.
func wcCreateStudioAsWithReason(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	label, name, locationKey, actorKey string) (string, processor.MessageOutcome, string) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	payload, _ := json.Marshal(map[string]any{"name": name, "location": locationKey})
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateStudio",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "studio",
		Payload:       payload,
		ContextHint:   &processor.ContextHint{Reads: []string{locationKey}},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	failure := ""
	if reply != nil && reply.Error != nil {
		if i := strings.Index(reply.Error.Message, "fail: "); i >= 0 {
			failure = reply.Error.Message[i+len("fail: "):]
		}
	}
	return "vtx.studio." + nanoIDFromRequestID(reqID), outcome, failure
}

const (
	wcDeadFloorID  = "BBWELLWKPLACEDEADFLR"
	wcDeadFloorKey = "vtx.location." + wcDeadFloorID

	wcAtticID  = "BBWELLWKPLACEATTCRMH"
	wcAtticKey = "vtx.location." + wcAtticID
)

// TestWorkplace_TombstonedAncestorConfersNothing pins the vertex-liveness half of
// the walk. `TombstoneLocation` does NOT cascade to a location's containedIn
// links, so decommissioning a floor leaves its links live and only its own
// isDeleted marks it gone. The read side stops at such a node (the full engine's
// fetchNode yields nothing for a soft-deleted vertex), so a write-side walk that
// transited one would hand out authority over everything beneath a floor the
// operator had just retired — write authority outliving the read it mirrors.
//
// The positive sibling is the same call over a LIVE floor, so a denial is the
// tombstone talking and not the extra hop failing to resolve at all.
func TestWorkplace_TombstonedAncestorConfersNothing(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wdwctombstone")
	wcSeedStaff(t, ctx, conn) // staff A worksAt building A

	// attic -containedIn-> floor -containedIn-> building A, staff A at building A.
	seedVertex(t, ctx, conn, wcAtticKey, "location", map[string]any{})
	seedVertex(t, ctx, conn, wcDeadFloorKey, "location", map[string]any{})
	testutil.SeedLink(t, ctx, conn,
		"lnk.location."+wcAtticID+".containedIn.location."+wcDeadFloorID,
		"containedIn", wcAtticKey, wcDeadFloorKey)
	testutil.SeedLink(t, ctx, conn,
		"lnk.location."+wcDeadFloorID+".containedIn.building."+wcBuildingAID,
		"containedIn", wcDeadFloorKey, wcBuildingAKey)

	// POSITIVE SIBLING: while the floor is alive, the two-hop chain authorizes.
	live, got := wcCreateStudioAs(t, ctx, conn, cp, cons,
		"wdwctombstonelive001", "Attic Studio", wcAtticKey, wcStaffKey)
	if got != processor.OutcomeAccepted {
		t.Fatalf("staff CreateStudio in the attic under a LIVE floor = %v, want Accepted "+
			"(the positive sibling — if this fails the negative proves nothing)", got)
	}
	if !keyExists(t, ctx, conn, live) {
		t.Fatalf("%s was not written by the accepted CreateStudio", live)
	}

	// Decommission the floor. Its containedIn links stay live, exactly as
	// TombstoneLocation leaves them.
	seedTombstonedVertex(t, ctx, conn, wcDeadFloorKey, "location")

	dead, got, why := wcCreateStudioAsWithReason(t, ctx, conn, cp, cons,
		"wdwctombstonedead001", "Attic Studio", wcAtticKey, wcStaffKey)
	if got != processor.OutcomeRejected {
		t.Fatalf("staff CreateStudio in the attic under a TOMBSTONED floor = %v, want Rejected — "+
			"a retired location must not keep conferring authority over what it contained", got)
	}
	if !strings.Contains(why, "does not worksAt") {
		t.Errorf("the tombstoned-ancestor denial said %q, want the confinement guard's message", why)
	}
	if keyExists(t, ctx, conn, dead) {
		t.Errorf("the denied CreateStudio wrote %s; it must be denied before any mutation", dead)
	}
}

// TestWorkplace_TombstonedSeedLocationConfersNothing is the level-0 half of the
// same rule: a tombstoned location can reach the walk as its STARTING node, and
// a staffer wired directly to a retired location must not keep writing there. A
// guard where a dead ancestor confers nothing but a dead starting point confers
// everything is the inconsistency the next reader copies wrongly.
//
// This pins the walk's own vertex test. The candidate locations it receives are
// screened one layer earlier, by the resolvers — see
// TestWorkplace_TombstonedStudioConfersNothing for that half.
func TestWorkplace_TombstonedSeedLocationConfersNothing(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wdwcdeadseed")
	wcSeedStaff(t, ctx, conn) // staff A worksAt building A

	// POSITIVE SIBLING: building A is alive, so the direct level-0 match holds.
	live, got := wcCreateStudioAs(t, ctx, conn, cp, cons,
		"wdwcdeadseedlive0001", "Live Studio", wcBuildingAKey, wcStaffKey)
	if got != processor.OutcomeAccepted {
		t.Fatalf("staff CreateStudio at their own LIVE building = %v, want Accepted "+
			"(the positive sibling — if this fails the negative proves nothing)", got)
	}
	if !keyExists(t, ctx, conn, live) {
		t.Fatalf("%s was not written by the accepted CreateStudio", live)
	}

	// Retire the very building the staffer is wired to. Their worksAt link stays
	// live — only the location is gone.
	seedTombstonedVertex(t, ctx, conn, wcBuildingAKey, "location")

	dead, got, why := wcCreateStudioAsWithReason(t, ctx, conn, cp, cons,
		"wdwcdeadseeddead0001", "Dead Studio", wcBuildingAKey, wcStaffKey)
	if got != processor.OutcomeRejected {
		t.Fatalf("staff CreateStudio at their own TOMBSTONED building = %v, want Rejected — "+
			"a retired location confers nothing, whether it is an ancestor or the target itself", got)
	}
	if !strings.Contains(why, "does not worksAt") {
		t.Errorf("the tombstoned-seed denial said %q, want the confinement guard's message", why)
	}
	if keyExists(t, ctx, conn, dead) {
		t.Errorf("the denied CreateStudio wrote %s", dead)
	}
}

// TestWorkplace_TombstonedStudioConfersNothing pins the RESOLVER's half of the
// tombstone rule. `session_locations` walks session -atStudio-> studio
// -locatedAt-> location, and CreateBooking proves only the SESSION alive — the
// studio in between is reached by a live link and nothing had ever looked at the
// vertex. TombstoneStudio soft-deletes it with no cascade onto that link or onto
// its sessions (both DDLs state the no-cascade rule), so a decommissioned studio
// went on handing back the building it used to sit in, and the confinement
// `require_workplace` computed was the dead studio's ex-topology.
//
// The operator control at the end is what makes the negative discriminating: it
// proves the session is still perfectly bookable after the tombstone, so the
// staff denial is the confinement guard talking and not a session the
// tombstone quietly broke.
func TestWorkplace_TombstonedStudioConfersNothing(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wdwcdeadstudio")
	wcSeedStaff(t, ctx, conn) // staff A worksAt building A

	studioA := createStudio(t, ctx, conn, cp, cons, "wdwcdsstudioa0000001", "Studio A")
	wfSeedStudioAt(t, ctx, conn, studioA, wcBuildingAKey, wcBuildingAID)
	sessionA, outcome := createSession(t, ctx, conn, cp, cons, "wdwcdssessiona000001",
		studioA, "Morning Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession at studio A = %v, want Accepted", outcome)
	}
	member := seedIdentity(t, ctx, conn, wcMemberID)
	member2 := seedIdentity(t, ctx, conn, wcMember2ID)

	// POSITIVE SIBLING: while the studio is alive, the two-hop chain authorizes.
	live, got := wcCreateBookingAs(t, ctx, conn, cp, cons,
		"wdwcdsbooklive000001", sessionA, member, wcStaffKey)
	if got != processor.OutcomeAccepted {
		t.Fatalf("staff CreateBooking on a session at a LIVE studio = %v, want Accepted "+
			"(the positive sibling — if this fails the negative proves nothing)", got)
	}
	if !keyExists(t, ctx, conn, live) {
		t.Fatalf("%s was not written by the accepted CreateBooking", live)
	}

	// Decommission the studio. Its locatedAt link and its session both stay live,
	// exactly as TombstoneStudio leaves them.
	seedTombstonedVertex(t, ctx, conn, studioA, "studio")

	dead, got, why := wcCreateBookingAsWithReason(t, ctx, conn, cp, cons,
		"wdwcdsbookdead000001", sessionA, member2, wcStaffKey)
	if got != processor.OutcomeRejected {
		t.Fatalf("staff CreateBooking on a session at a TOMBSTONED studio = %v, want Rejected — "+
			"a retired studio must not keep conferring the building it sat in", got)
	}
	if !strings.Contains(why, "does not worksAt") {
		t.Errorf("the tombstoned-studio denial said %q, want the confinement guard's message", why)
	}
	if keyExists(t, ctx, conn, dead) {
		t.Errorf("the denied CreateBooking wrote %s; it must be denied before any mutation", dead)
	}

	// CONTROL: the operator is exempt from confinement, so the same booking still
	// goes through — the session did not become unbookable, it became unbookable
	// BY THIS STAFFER.
	ctl, got := createBooking(t, ctx, conn, cp, cons,
		"wdwcdsbookctl0000001", sessionA, member2, "")
	if got != processor.OutcomeAccepted {
		t.Fatalf("operator CreateBooking on the same session = %v, want Accepted — the staff "+
			"denial above must be confinement, not a session the tombstone broke", got)
	}
	if !keyExists(t, ctx, conn, ctl) {
		t.Errorf("%s was not written by the accepted operator CreateBooking", ctl)
	}
}

// TestWorkplace_ContainmentCycleTerminates pins the OUTCOME for a cyclic
// topology: containment is meant to be a DAG, nothing in location-domain enforces
// acyclicity, and a walk that met a cycle must answer with the guard's own denial
// rather than raise or burn the script wall budget.
//
// What actually guarantees termination here is the DEPTH bound, not the visited
// set — deleting `nxt in seen` keeps this test green, which is worth stating
// plainly so nobody reads it as the `seen` guard's proof. `seen` bounds repeated
// WORK (a diamond would otherwise re-read a shared ancestor once per branch) and
// feeds the node budget; termination is `range(WORKPLACE_MAX_DEPTH)`'s job. Both
// bounds are load-bearing, for different reasons.
func TestWorkplace_ContainmentCycleTerminates(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wdwccycle")
	wcSeedStaff(t, ctx, conn) // staff A worksAt building A — NOT in the cycle

	// A two-node cycle reachable from neither building: attic <-> floor.
	seedVertex(t, ctx, conn, wcAtticKey, "location", map[string]any{})
	seedVertex(t, ctx, conn, wcDeadFloorKey, "location", map[string]any{})
	testutil.SeedLink(t, ctx, conn,
		"lnk.location."+wcAtticID+".containedIn.location."+wcDeadFloorID,
		"containedIn", wcAtticKey, wcDeadFloorKey)
	testutil.SeedLink(t, ctx, conn,
		"lnk.location."+wcDeadFloorID+".containedIn.location."+wcAtticID,
		"containedIn", wcDeadFloorKey, wcAtticKey)

	studio, got, why := wcCreateStudioAsWithReason(t, ctx, conn, cp, cons,
		"wdwccycle0000000001", "Cycle Studio", wcAtticKey, wcStaffKey)
	if got != processor.OutcomeRejected {
		t.Fatalf("staff CreateStudio inside a containment CYCLE = %v, want Rejected", got)
	}
	// The distinction that matters: the guard's own denial, not a ScriptTimeout.
	if !strings.Contains(why, "does not worksAt") {
		t.Errorf("the cycle was refused with %q, want the confinement guard's own message — "+
			"a timeout or a raise here would mean the visited set failed to close the loop", why)
	}
	if keyExists(t, ctx, conn, studio) {
		t.Errorf("the denied CreateStudio wrote %s", studio)
	}
}

// seedTombstonedVertex writes a vertex with isDeleted=true — the shape
// TombstoneLocation leaves behind, which is a present document rather than an
// absent key, so a guard must test the flag and not merely the read.
func seedTombstonedVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class string) {
	t.Helper()
	doc := map[string]any{"class": class, "isDeleted": true, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed tombstoned vertex %s: %v", key, err)
	}
}
