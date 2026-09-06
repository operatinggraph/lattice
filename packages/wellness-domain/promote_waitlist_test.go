// wellness-domain integration tests for PromoteWaitlistedBookings — the
// Weaver-dispatched op that seats a class's waitlist into seats that class
// already has free. CancelBooking's own promotion covers the seat IT frees;
// these cover the seat that goes free with no cancellation behind it (a
// capacity raise, or a hole a cancellation left when nobody was waiting yet),
// which is where the live gap was found. Same harness as
// refund_marker_test.go: real install + Processor pipeline, hand-built
// envelopes carrying exactly the contextHint the target declares (targets.go).
package wellnessdomain_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	wellnessdomain "github.com/operatinggraph/lattice/packages/wellness-domain"
)

// promoteEnv builds a PromoteWaitlistedBookings envelope carrying exactly the
// contextHint the missing_promotion gap declares (targets.go): the session and
// its .schedule as required reads, and the forSession-in enumeration. Nothing
// else — the seat cells the script reads are deliberately undeclared, since a
// seat index derives from the session's capacity rather than from any lens row
// column, and this envelope is the proof that the op still works with only
// what a real dispatch hands it.
func promoteEnv(label, sessionKey, actorKey, submittedAt string) *processor.OperationEnvelope {
	return &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "PromoteWaitlistedBookings",
		Actor:         actorKey,
		SubmittedAt:   submittedAt,
		Class:         "booking",
		Payload:       json.RawMessage(`{"session":"` + sessionKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{sessionKey, sessionKey + ".schedule"},
			Enumerations: []processor.EnumerationHint{
				{Hub: sessionKey, Relation: "forSession", Direction: "in"},
			},
		},
	}
}

// bookingStatusData reads a booking's .status data bag.
func bookingStatusData(t *testing.T, ctx context.Context, conn *substrate.Conn, bookingKey string) map[string]any {
	t.Helper()
	doc := readDoc(t, ctx, conn, bookingKey+".status")
	data, _ := doc["data"].(map[string]any)
	return data
}

// requirePromoted asserts a booking's .status now reads as a seated booking:
// value booked, the given seat, and NO waitlistSlot left behind (the two are
// mutually exclusive by construction, ddls.go).
func requirePromoted(t *testing.T, ctx context.Context, conn *substrate.Conn, bookingKey string, wantSeat int) {
	t.Helper()
	data := bookingStatusData(t, ctx, conn, bookingKey)
	if got, _ := data["value"].(string); got != "booked" {
		t.Fatalf("%s .status.value = %q, want booked", bookingKey, got)
	}
	if got, _ := data["seat"].(float64); int(got) != wantSeat {
		t.Fatalf("%s .status.seat = %v, want %d", bookingKey, data["seat"], wantSeat)
	}
	if _, has := data["waitlistSlot"]; has {
		t.Fatalf("%s must not keep a waitlistSlot once promoted: %v", bookingKey, data)
	}
}

// TestPromoteWaitlistedBookings_SeatsLowestSlotsUpToCapacity is the load-
// bearing one: a class raised from full to roomy seats as many waiting members
// as it now has room for, EARLIEST FIRST, in a single dispatch — and stops
// exactly at capacity, leaving the last candidate on the waitlist. The
// capacity raise runs through ReassignSession, the live path that produced the
// stranded member, so the fixture is the reported bug rather than a hand-set
// aspect.
func TestPromoteWaitlistedBookings_SeatsLowestSlotsUpToCapacity(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "promotecapraise")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdpromostudio000001", "Small Studio")
	sessionKey, outcome := createSession(t, ctx, conn, cp, cons, "wdpromosession00001", studioKey, "Intro Class", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 1)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession outcome = %v, want Accepted", outcome)
	}

	seatedKey, bkOutcome := createBooking(t, ctx, conn, cp, cons, "wdpromobooking00001", sessionKey, seedIdentity(t, ctx, conn, "BBWELLPRMTWSEAT1HJKM"), "")
	if bkOutcome != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking outcome = %v, want Accepted", bkOutcome)
	}
	waitOneKey, wl1 := joinWaitlist(t, ctx, conn, cp, cons, "wdpromojoin0000001", sessionKey, seedIdentity(t, ctx, conn, "BBWELLPRMTWWTNG1HJKM"), "")
	waitTwoKey, wl2 := joinWaitlist(t, ctx, conn, cp, cons, "wdpromojoin0000002", sessionKey, seedIdentity(t, ctx, conn, "BBWELLPRMTWWTNG2HJKM"), "")
	waitThreeKey, wl3 := joinWaitlist(t, ctx, conn, cp, cons, "wdpromojoin0000003", sessionKey, seedIdentity(t, ctx, conn, "BBWELLPRMTWWTNG3HJKM"), "")
	for i, got := range []processor.MessageOutcome{wl1, wl2, wl3} {
		if got != processor.OutcomeAccepted {
			t.Fatalf("JoinWaitlist %d outcome = %v, want Accepted", i+1, got)
		}
	}

	// The class gains room: capacity 1 -> 3. Nothing else changes, and no
	// cancellation happens, so CancelBooking's own promotion never runs.
	raise, _ := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, reassignSessionEnv(t, ctx, conn, "wdpromoraise000001",
		sessionKey, studioKey, "", domainActorKey,
		map[string]any{"sessionKey": sessionKey, "studio": studioKey, "capacity": 3},
		"2026-07-08T08:00:00Z"))
	if raise != processor.OutcomeAccepted {
		t.Fatalf("ReassignSession capacity raise outcome = %v, want Accepted", raise)
	}

	promoteReq := testutil.GenReqID("wdpromopromote0001")
	promote, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		promoteEnv("wdpromopromote0001", sessionKey, domainActorKey, "2026-07-08T08:05:00Z"))
	if promote != processor.OutcomeAccepted {
		t.Fatalf("PromoteWaitlistedBookings outcome = %v, reply = %+v, want Accepted", promote, reply)
	}
	if reply.PrimaryKey != sessionKey {
		t.Fatalf("primaryKey = %q, want the session %q", reply.PrimaryKey, sessionKey)
	}

	// Two seats were free (capacity 3, one held), so the two EARLIEST
	// candidates are seated and the third stays waiting.
	requirePromoted(t, ctx, conn, waitOneKey, 2)
	requirePromoted(t, ctx, conn, waitTwoKey, 3)
	if got, _ := bookingStatusData(t, ctx, conn, waitThreeKey)["value"].(string); got != "waitlisted" {
		t.Fatalf("the third candidate must stay waitlisted (capacity ran out), got %q", got)
	}
	if got, _ := bookingStatusData(t, ctx, conn, waitThreeKey)["waitlistSlot"].(float64); int(got) != 3 {
		t.Fatalf("the third candidate must keep waitlistSlot 3, got %v", bookingStatusData(t, ctx, conn, waitThreeKey)["waitlistSlot"])
	}
	if got, _ := bookingStatusData(t, ctx, conn, seatedKey)["seat"].(float64); int(got) != 1 {
		t.Fatalf("the already-seated booking must keep seat 1, got %v", got)
	}

	for _, seat := range []int{1, 2, 3} {
		if !keyExists(t, ctx, conn, sessionKey+".seat"+strconv.Itoa(seat)) {
			t.Fatalf("seat cell %d must be claimed after promotion", seat)
		}
	}
	if keyExists(t, ctx, conn, sessionKey+".wl1") {
		t.Fatalf("wl1 must be released — its booking now holds a seat")
	}
	if keyExists(t, ctx, conn, sessionKey+".wl2") {
		t.Fatalf("wl2 must be released — its booking now holds a seat")
	}
	if !keyExists(t, ctx, conn, sessionKey+".wl3") {
		t.Fatalf("wl3 must survive — its booking is still waiting")
	}

	// Both promotions land in ONE commit, so the tracker carries the event
	// twice under the single request.
	classes := trackerEventClasses(t, ctx, conn, promoteReq)
	promoted := 0
	for _, c := range classes {
		if c == "wellness.waitlistPromoted" {
			promoted++
		}
	}
	if promoted != 2 {
		t.Fatalf("wellness.waitlistPromoted count = %d in %v, want 2 (one dispatch, both seats)", promoted, classes)
	}
}

// TestPromoteWaitlistedBookings_ReusesFreedSeatHole proves the seat walk takes
// a HOLE, not just the tail: a cancellation that happened while nobody was
// waiting tombstones its seat cell in the middle of the range, and the
// promotion OCC-revives that exact cell rather than claiming past it — the
// claim_free_seats isDeleted branch, which no capacity-raise vector reaches.
func TestPromoteWaitlistedBookings_ReusesFreedSeatHole(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "promoteseathole")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdpromohstudio00001", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdpromohsession0001", studioKey, "Intro Class", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 3)

	createBooking(t, ctx, conn, cp, cons, "wdpromohbooking0001", sessionKey, seedIdentity(t, ctx, conn, "BBWELLPRMTWHSEA1HJKM"), "")
	middleKey, _ := createBooking(t, ctx, conn, cp, cons, "wdpromohbooking0002", sessionKey, seedIdentity(t, ctx, conn, "BBWELLPRMTWHSEA2HJKM"), "")
	createBooking(t, ctx, conn, cp, cons, "wdpromohbooking0003", sessionKey, seedIdentity(t, ctx, conn, "BBWELLPRMTWHSEA3HJKM"), "")
	if got, _ := bookingStatusData(t, ctx, conn, middleKey)["seat"].(float64); int(got) != 2 {
		t.Fatalf("fixture: the middle booking must hold seat 2, got %v", got)
	}

	// Cancelled with an EMPTY waitlist, so CancelBooking tombstones the seat
	// cell instead of handing it to anyone — the hole this test needs.
	cancel := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("wdpromohcancel00001"),
		Lane:          processor.LaneDefault,
		OperationType: "CancelBooking",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:10:00Z",
		Class:         "booking",
		Payload:       json.RawMessage(`{"bookingKey":"` + middleKey + `","session":"` + sessionKey + `"}`),
		ContextHint: &processor.ContextHint{Enumerations: testutil.DeclaredEnumerations("CancelBooking", domainActorKey, wellnessdomain.OpMetas()), Reads: []string{
			middleKey, middleKey + ".status", sessionKey + ".schedule",
			forSessionLnkKey(t, middleKey, sessionKey),
		}},
	}
	testutil.PublishOp(t, conn, cancel)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if keyExists(t, ctx, conn, sessionKey+".seat2") {
		t.Fatalf("fixture: seat 2 must be released by the cancellation")
	}

	waitKey, wl := joinWaitlist(t, ctx, conn, cp, cons, "wdpromohjoin000001", sessionKey, seedIdentity(t, ctx, conn, "BBWELLPRMTWHWTNG1HJK"), "")
	if wl != processor.OutcomeAccepted {
		t.Fatalf("JoinWaitlist outcome = %v, want Accepted", wl)
	}

	promote, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		promoteEnv("wdpromohpromote0001", sessionKey, domainActorKey, "2026-07-08T08:05:00Z"))
	if promote != processor.OutcomeAccepted {
		t.Fatalf("PromoteWaitlistedBookings outcome = %v, reply = %+v, want Accepted", promote, reply)
	}

	requirePromoted(t, ctx, conn, waitKey, 2)
	if !keyExists(t, ctx, conn, sessionKey+".seat2") {
		t.Fatalf("the freed seat cell must be re-claimed, not skipped")
	}
	if keyExists(t, ctx, conn, sessionKey+".wl1") {
		t.Fatalf("wl1 must be released once its booking holds a seat")
	}
}

// TestPromoteWaitlistedBookings_RefusesStartedSession proves the past-class
// guard: a dispatch that reaches the op after the class began is refused, so a
// stale gap evaluation can never hand out a seat nobody can use. Same
// inequality CreateBooking's SessionInPast uses, so a submission exactly ON
// startsAt is already too late.
func TestPromoteWaitlistedBookings_RefusesStartedSession(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "promotestarted")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdpromostudio000002", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdpromosession00002", studioKey, "Intro Class", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 3)
	createBooking(t, ctx, conn, cp, cons, "wdpromobooking00002", sessionKey, seedIdentity(t, ctx, conn, "BBWELLPRMTWSEAT2HJKM"), "")
	waitKey, _ := joinWaitlist(t, ctx, conn, cp, cons, "wdpromojoin0000004", sessionKey, seedIdentity(t, ctx, conn, "BBWELLPRMTWWTNG4HJKM"), "")

	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		promoteEnv("wdpromostarted0001", sessionKey, domainActorKey, "2026-07-08T09:00:00Z"))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("promoting on a class that has begun = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "SessionInPast") {
		t.Fatalf("rejection should be SessionInPast, got %+v", reply.Error)
	}
	if got, _ := bookingStatusData(t, ctx, conn, waitKey)["value"].(string); got != "waitlisted" {
		t.Fatalf("the candidate must be untouched by a refused promotion, got %q", got)
	}
	if keyExists(t, ctx, conn, sessionKey+".seat2") {
		t.Fatalf("a refused promotion must claim no seat cell")
	}
}

// TestPromoteWaitlistedBookings_DeclinesWhenFull proves the NothingToPromote
// decline on the no-free-seat half: a class that is genuinely full declines
// rather than committing an empty batch, so Weaver records the refusal instead
// of counting a no-op as a successful convergence.
func TestPromoteWaitlistedBookings_DeclinesWhenFull(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "promotefull")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdpromostudio000003", "Small Studio")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdpromosession00003", studioKey, "Intro Class", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 1)
	createBooking(t, ctx, conn, cp, cons, "wdpromobooking00003", sessionKey, seedIdentity(t, ctx, conn, "BBWELLPRMTWSEAT3HJKM"), "")
	joinWaitlist(t, ctx, conn, cp, cons, "wdpromojoin0000005", sessionKey, seedIdentity(t, ctx, conn, "BBWELLPRMTWWTNG5HJKM"), "")

	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		promoteEnv("wdpromofull0000001", sessionKey, domainActorKey, "2026-07-08T08:05:00Z"))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("promoting on a full class = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "NothingToPromote") {
		t.Fatalf("rejection should be NothingToPromote, got %+v", reply.Error)
	}
}

// TestPromoteWaitlistedBookings_DeclinesWithNoWaitlist is the other half of
// the decline: seats free, nobody waiting. Without it the full-class vector
// above could pass on an op that declined for the wrong reason.
func TestPromoteWaitlistedBookings_DeclinesWithNoWaitlist(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "promotenowaitlist")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdpromostudio000004", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdpromosession00004", studioKey, "Intro Class", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 5)
	createBooking(t, ctx, conn, cp, cons, "wdpromobooking00004", sessionKey, seedIdentity(t, ctx, conn, "BBWELLPRMTWSEAT4HJKM"), "")

	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		promoteEnv("wdpromoempty000001", sessionKey, domainActorKey, "2026-07-08T08:05:00Z"))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("promoting with an empty waitlist = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "NothingToPromote") {
		t.Fatalf("rejection should be NothingToPromote, got %+v", reply.Error)
	}
	if !strings.Contains(reply.Error.Message, "no live waitlisted booking") {
		t.Fatalf("the decline must name the empty waitlist, not the seats: %+v", reply.Error)
	}
}

// TestPromoteWaitlistedBookings_RefusesTombstonedSession proves the op fails
// closed on a class that was called off: its stranded bookings belong to
// ReleaseOrphanedBooking, which drains them, not to this op, which would
// otherwise seat members into a class that no longer exists. A session key
// that is absent outright never reaches the script at all — it is a declared
// read, so step-4 hydration refuses first — so the tombstoned session is the
// vector that actually exercises the vertex_alive branch.
func TestPromoteWaitlistedBookings_RefusesTombstonedSession(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "promotedeadsession")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdpromostudio000006", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdpromosession00006", studioKey, "Intro Class", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 5)
	createBooking(t, ctx, conn, cp, cons, "wdpromobooking00006", sessionKey, seedIdentity(t, ctx, conn, "BBWELLPRMTWSEAT6HJKM"), "")
	waitKey, _ := joinWaitlist(t, ctx, conn, cp, cons, "wdpromojoin0000007", sessionKey, seedIdentity(t, ctx, conn, "BBWELLPRMTWWTNG7HJKM"), "")

	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("wdpromokill0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "TombstoneSession",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-08T08:00:00Z",
		Class:         "session",
		Payload:       json.RawMessage(`{"sessionKey":"` + sessionKey + `","studio":"` + studioKey + `"}`),
		ContextHint: &processor.ContextHint{Enumerations: testutil.DeclaredEnumerations("TombstoneSession", domainActorKey, wellnessdomain.OpMetas()), Reads: []string{
			sessionKey, sessionKey + ".schedule", atStudioLnkKey(t, sessionKey, studioKey),
		}},
	})
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		promoteEnv("wdpromodead0000001", sessionKey, domainActorKey, "2026-07-08T08:05:00Z"))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("promoting on a called-off class = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "UnknownSession") {
		t.Fatalf("rejection should be UnknownSession, got %+v", reply.Error)
	}
	if got, _ := bookingStatusData(t, ctx, conn, waitKey)["value"].(string); got != "waitlisted" {
		t.Fatalf("the candidate must be untouched by a refused promotion, got %q", got)
	}
}

// TestPromoteWaitlistedBookings_NonOperatorDenied proves the Weaver-only
// posture is enforced by the GRAPH walk, not by the capability document: a
// caller holding a full PromoteWaitlistedBookings grant, a live identity, and
// a real holdsRole edge — to frontOfHouse, not to the primordial operator role
// — is still denied. Seeding the role is what makes the vector discriminating:
// the walk runs, resolves a role, reads its canonicalName and rejects it,
// rather than answering false because the actor has no links at all. Without
// this, the grant alone would let a member decide who jumps the queue.
func TestPromoteWaitlistedBookings_NonOperatorDenied(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "promotenonoperator")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdpromostudio000005", "Flow Room")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdpromosession00005", studioKey, "Intro Class", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 5)
	createBooking(t, ctx, conn, cp, cons, "wdpromobooking00005", sessionKey, seedIdentity(t, ctx, conn, "BBWELLPRMTWSEAT5HJKM"), "")
	waitKey, _ := joinWaitlist(t, ctx, conn, cp, cons, "wdpromojoin0000006", sessionKey, seedIdentity(t, ctx, conn, "BBWELLPRMTWWTNG6HJKM"), "")

	const staffActorID = "BBWELLPRMTWDENYSTAFF"
	staffActorKey := seedIdentity(t, ctx, conn, staffActorID)
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key: "cap.identity." + staffActorID, Actor: staffActorKey, Version: "1.0",
		ProjectedAt: time.Now().UTC().Format(time.RFC3339Nano), ProjectedFromRevisions: map[string]uint64{staffActorKey: 1},
		Lanes:               []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{{OperationType: "PromoteWaitlistedBookings", Scope: "any"}},
		ServiceAccess:       []processor.ServiceAccessEntry{},
		EphemeralGrants:     []processor.EphemeralGrant{},
		Roles:               []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")},
	})

	// The staffer is IN the graph and DOES hold a role — a real frontOfHouse
	// role vertex carrying its own .canonicalName, reached by a real holdsRole
	// link. Without this the walk would find no links at all and the denial
	// would prove only that an unknown actor is refused; with it, the denial is
	// the one the op is for: a role-holding staffer whose role is not operator.
	frontOfHouseRoleID := pkgmgr.RoleID("identity-domain", "frontOfHouse")
	frontOfHouseRoleKey := "vtx.role." + frontOfHouseRoleID
	seedVertex(t, ctx, conn, frontOfHouseRoleKey, "role", map[string]any{})
	seedAspect(t, ctx, conn, frontOfHouseRoleKey, "canonicalName", "canonicalName", map[string]any{"value": "frontOfHouse"})
	seedLink(t, ctx, conn, "lnk.identity."+staffActorID+".holdsRole.role."+frontOfHouseRoleID,
		staffActorKey, frontOfHouseRoleKey, "holdsRole", "holdsRole")

	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		promoteEnv("wdpromodeny0000001", sessionKey, staffActorKey, "2026-07-08T08:05:00Z"))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("a non-operator promoting = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied") {
		t.Fatalf("rejection should be AuthDenied, got %+v", reply.Error)
	}
	if got, _ := bookingStatusData(t, ctx, conn, waitKey)["value"].(string); got != "waitlisted" {
		t.Fatalf("the candidate must be untouched by a denied promotion, got %q", got)
	}
}
