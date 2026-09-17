// wellness-domain integration tests for the three seat facts a booking records
// at its claim and carries for life — the desk's walk-in admission
// (CreateBooking's past-class guard is startsAt on the member's own leg and
// endsAt on the desk leg), the claim stamp (.status.bookedAt), and the price
// snapshot (.status.priceCents) — plus ReassignSession's refusal to shrink a
// class under a claimed seat (CapacityBelowSeated). Same harness as
// refund_marker_test.go: real install + Processor pipeline, hand-built
// envelopes carrying exactly the contextHint the dispatcher declares.
package wellnessdomain_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// walkInSession builds a priced 09:00–09:30 class at a studio wired to
// building A — the building wcSeedStaff's front-of-house staffer worksAt —
// so the staff leg passes the workplace walk and the only remaining gate is
// the past-class one under test.
func walkInSession(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label string, capacity int, priceCents any) string {
	t.Helper()
	studioKey := createStudio(t, ctx, conn, cp, cons, label+"std", "Walk-in Studio")
	wfSeedStudioAt(t, ctx, conn, studioKey, wcBuildingAKey, wcBuildingAID)
	sessionKey, outcome := createSessionPriced(t, ctx, conn, cp, cons, label+"ses", studioKey, "Walk-in Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", capacity, priceCents)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createSessionPriced outcome = %v, want Accepted", outcome)
	}
	return sessionKey
}

// TestCreateBooking_DeskSeatsWalkInAfterStart is the row itself: a walk-in at
// the door, ten minutes into the class, is seated by the desk. Both desk legs
// — front-of-house staff (workplace-confined) and the operator — commit, and
// the booking records the claim: .status.bookedAt is the envelope's
// submittedAt normalized to canonical whole-second UTC (the staff leg submits
// with a +02:00 offset to prove the normalization), and .status.priceCents
// is the price the class charged at that instant.
func TestCreateBooking_DeskSeatsWalkInAfterStart(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "walkindesk")
	wcSeedStaff(t, ctx, conn)
	sessionKey := walkInSession(t, ctx, conn, cp, cons, "wdwalkindesk", 5, 1500)

	staffWalkIn := seedIdentity(t, ctx, conn, "BBWELLWALKNNSTFHJKMN")
	bookingKey, got, why := bookingEntryAs(t, ctx, conn, cp, cons, "CreateBooking", "wdwalkindeskstaff001",
		sessionKey, staffWalkIn, wcStaffKey, "", "2026-07-08T11:10:00+02:00")
	if got != processor.OutcomeAccepted {
		t.Fatalf("the desk seating a walk-in ten minutes into the class = %v (%s), want Accepted", got, why)
	}
	status := attendanceStatus(t, ctx, conn, bookingKey)
	if v, _ := status["value"].(string); v != "booked" {
		t.Fatalf("status.value = %q, want booked", v)
	}
	if v, _ := status["bookedAt"].(string); v != "2026-07-08T09:10:00Z" {
		t.Fatalf("status.bookedAt = %q, want the envelope's submittedAt normalized to canonical UTC 2026-07-08T09:10:00Z", v)
	}
	if v, _ := status["priceCents"].(float64); v != 1500 {
		t.Fatalf("status.priceCents = %v, want the 1500 the class charged at the claim", status["priceCents"])
	}

	operatorWalkIn := seedIdentity(t, ctx, conn, "BBWELLWALKNNQPRHJKMN")
	bookingKey, got, why = bookingEntryAs(t, ctx, conn, cp, cons, "CreateBooking", "wdwalkindeskoper0001",
		sessionKey, operatorWalkIn, domainActorKey, "", "2026-07-08T09:29:59Z")
	if got != processor.OutcomeAccepted {
		t.Fatalf("the operator seating a walk-in one second before the class ends = %v (%s), want Accepted", got, why)
	}
	if v, _ := attendanceStatus(t, ctx, conn, bookingKey)["bookedAt"].(string); v != "2026-07-08T09:29:59Z" {
		t.Fatalf("status.bookedAt = %q, want 2026-07-08T09:29:59Z", v)
	}
}

// TestCreateBooking_DeskRefusesAfterEnd is the desk leg's own bound: exactly
// at endsAt the class has ended and the refusal says so. The same inequality
// the self leg applies at startsAt — at the boundary the stricter rule wins.
func TestCreateBooking_DeskRefusesAfterEnd(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "walkinended")
	wcSeedStaff(t, ctx, conn)
	sessionKey := walkInSession(t, ctx, conn, cp, cons, "wdwalkinend", 5, 1500)

	for _, tc := range []struct{ label, bookerID, actor, submittedAt string }{
		{"wdwalkinendstaff0001", "BBWELLWALKNNENDSTHJK", wcStaffKey, "2026-07-08T09:30:00Z"},
		{"wdwalkinendoper00001", "BBWELLWALKNNENDQPHJK", domainActorKey, "2026-07-08T10:00:00Z"},
	} {
		late := seedIdentity(t, ctx, conn, tc.bookerID)
		bookingKey, got, why := bookingEntryAs(t, ctx, conn, cp, cons, "CreateBooking", tc.label,
			sessionKey, late, tc.actor, "", tc.submittedAt)
		if got != processor.OutcomeRejected || !strings.HasPrefix(why, "SessionInPast:") || !strings.Contains(why, "the class has ended") {
			t.Fatalf("%s seating a walk-in at %s = %v (%s), want SessionInPast saying the class has ended", tc.actor, tc.submittedAt, got, why)
		}
		if keyExists(t, ctx, conn, bookingKey) {
			t.Fatalf("a refused walk-in minted %s", bookingKey)
		}
	}
}

// TestCreateBooking_SelfRefusedAfterStart keeps self-service on the startsAt
// rule: the member's own scope=self submission (authContext.target = the
// booker, the validated leg) is refused the moment the class has begun, with
// the refusal still saying "not in the future" — the desk's until-endsAt
// admission is the desk's alone.
func TestCreateBooking_SelfRefusedAfterStart(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "walkinself")
	sessionKey := walkInSession(t, ctx, conn, cp, cons, "wdwalkinself", 5, 1500)
	seedIdentity(t, ctx, conn, domainConsumerID)

	bookingKey, got, why := bookingEntryAs(t, ctx, conn, cp, cons, "CreateBooking", "wdwalkinselflate0001",
		sessionKey, domainConsumerKey, domainConsumerKey, domainConsumerKey, "2026-07-08T09:10:00Z")
	if got != processor.OutcomeRejected || !strings.HasPrefix(why, "SessionInPast:") || !strings.Contains(why, "not in the future") {
		t.Fatalf("a member booking themselves ten minutes into the class = %v (%s), want SessionInPast (not in the future)", got, why)
	}
	if keyExists(t, ctx, conn, bookingKey) {
		t.Fatalf("a refused self-service booking minted %s", bookingKey)
	}

	// The positive sibling: the same leg one second before the start commits,
	// stamping the claim.
	bookingKey, got, why = bookingEntryAs(t, ctx, conn, cp, cons, "CreateBooking", "wdwalkinselfearly001",
		sessionKey, domainConsumerKey, domainConsumerKey, domainConsumerKey, "2026-07-08T08:59:59Z")
	if got != processor.OutcomeAccepted {
		t.Fatalf("a member booking themselves before the start = %v (%s), want Accepted", got, why)
	}
	if v, _ := attendanceStatus(t, ctx, conn, bookingKey)["bookedAt"].(string); v != "2026-07-08T08:59:59Z" {
		t.Fatalf("status.bookedAt = %q, want 2026-07-08T08:59:59Z", v)
	}
}

// TestJoinWaitlist_RefusedAfterStartOnDeskLeg proves the waitlist keeps the
// startsAt rule on every leg — a slot on a class already under way can never
// be promoted into — while the same desk submission before the start commits
// and stamps its claim (bookedAt, no priceCents: a waitlisted booking pays
// nothing until it holds a seat).
func TestJoinWaitlist_RefusedAfterStartOnDeskLeg(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "walkinwait")
	wcSeedStaff(t, ctx, conn)
	sessionKey := walkInSession(t, ctx, conn, cp, cons, "wdwalkinwait", 1, 1500)

	early := seedIdentity(t, ctx, conn, "BBWELLWALKNNWLERLHJK")
	bookingKey, got, why := bookingEntryAs(t, ctx, conn, cp, cons, "JoinWaitlist", "wdwalkinwaitearly001",
		sessionKey, early, wcStaffKey, "", "2026-07-08T08:59:00Z")
	if got != processor.OutcomeAccepted {
		t.Fatalf("the desk waitlisting a member before the start = %v (%s), want Accepted", got, why)
	}
	status := attendanceStatus(t, ctx, conn, bookingKey)
	if v, _ := status["bookedAt"].(string); v != "2026-07-08T08:59:00Z" {
		t.Fatalf("waitlisted status.bookedAt = %q, want the claim's submittedAt 2026-07-08T08:59:00Z", v)
	}
	if _, has := status["priceCents"]; has {
		t.Fatalf("waitlisted status.priceCents = %v, want absent — a waitlisted booking is priced at seating, not at the claim", status["priceCents"])
	}

	for _, tc := range []struct{ label, bookerID, actor string }{
		{"wdwalkinwaitstaff001", "BBWELLWALKNNWLSTFHJK", wcStaffKey},
		{"wdwalkinwaitoper0001", "BBWELLWALKNNWLQPRHJK", domainActorKey},
	} {
		late := seedIdentity(t, ctx, conn, tc.bookerID)
		bookingKey, got, why := bookingEntryAs(t, ctx, conn, cp, cons, "JoinWaitlist", tc.label,
			sessionKey, late, tc.actor, "", "2026-07-08T09:10:00Z")
		if got != processor.OutcomeRejected || !strings.HasPrefix(why, "SessionInPast:") || !strings.Contains(why, "not in the future") {
			t.Fatalf("%s waitlisting a member ten minutes into the class = %v (%s), want SessionInPast (not in the future) — the desk's until-endsAt admission is CreateBooking's alone", tc.actor, got, why)
		}
		if keyExists(t, ctx, conn, bookingKey) {
			t.Fatalf("a refused waitlist entry minted %s", bookingKey)
		}
	}
}

// TestCreateBooking_SnapshotsEffectivePrice pins the price a seat records at
// its claim: the class's priceCents for a standard booker, its
// residentPriceCents for a resident-rate booker when the class declares one,
// priceCents again for a resident on a class that declares none, and 0 on a
// class that carries no price at all — a seat booked free stays free.
func TestCreateBooking_SnapshotsEffectivePrice(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "pricesnapshot")
	studioKey := createStudio(t, ctx, conn, cp, cons, "wdpricesnapstudio01", "Snapshot Studio")

	standardPriced, outcome := createSessionResidentPriced(t, ctx, conn, cp, cons, "wdpricesnapsessres1", studioKey, "Resident Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 5, 1500, 1000)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createSessionResidentPriced outcome = %v, want Accepted", outcome)
	}
	noResident, outcome := createSessionPriced(t, ctx, conn, cp, cons, "wdpricesnapsessstd1", studioKey, "Standard Flow", "2026-07-08T10:00:00Z", "2026-07-08T10:30:00Z", 5, 1800)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createSessionPriced outcome = %v, want Accepted", outcome)
	}
	free, outcome := createSessionPriced(t, ctx, conn, cp, cons, "wdpricesnapsessfree", studioKey, "Free Flow", "2026-07-08T11:00:00Z", "2026-07-08T11:30:00Z", 5, nil)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createSessionPriced (free) outcome = %v, want Accepted", outcome)
	}

	resident := seedIdentity(t, ctx, conn, "BBWELLPRCSNPRESHJKMN")
	residentLease := seedLease(t, ctx, conn, "BBWELLPRCSNPLSEHJKMN", "BBWELLPRCSNPRESHJKMN", true)
	standard := seedIdentity(t, ctx, conn, "BBWELLPRCSNPSTDHJKMN")

	cases := []struct {
		name, label, session, booker, lease string
		wantRate                            string
		wantPrice                           float64
	}{
		{"standard on a class with a resident price", "wdpricesnapbk000001", standardPriced, standard, "", "standard", 1500},
		{"resident on a class with a resident price", "wdpricesnapbk000002", standardPriced, resident, residentLease, "resident", 1000},
		{"resident on a class with no resident price", "wdpricesnapbk000003", noResident, resident, residentLease, "resident", 1800},
		{"a class with no price", "wdpricesnapbk000004", free, standard, "", "standard", 0},
	}
	for _, tc := range cases {
		bookingKey, got := createBooking(t, ctx, conn, cp, cons, tc.label, tc.session, tc.booker, tc.lease)
		if got != processor.OutcomeAccepted {
			t.Fatalf("%s: createBooking outcome = %v, want Accepted", tc.name, got)
		}
		status := attendanceStatus(t, ctx, conn, bookingKey)
		if v, _ := status["rate"].(string); v != tc.wantRate {
			t.Fatalf("%s: status.rate = %q, want %s (the case must exercise the rate it names)", tc.name, v, tc.wantRate)
		}
		v, isNumber := status["priceCents"].(float64)
		if !isNumber || v != tc.wantPrice {
			t.Fatalf("%s: status.priceCents = %v, want %v", tc.name, status["priceCents"], tc.wantPrice)
		}
	}
}

// TestPromotion_SnapshotsPriceAtSeating: a waitlisted booking is priced when
// it is SEATED, not when it joined. The class is repriced between the join and
// the promotion, and the promoted booking's snapshot is the price at seating
// — with its own JoinWaitlist claim stamp carried, never re-stamped.
func TestPromotion_SnapshotsPriceAtSeating(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "promopricesnapshot")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdpromopricestudio1", "Promo Price Studio")
	sessionKey, outcome := createSessionPriced(t, ctx, conn, cp, cons, "wdpromopricesession", studioKey, "Promo Price Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 1, 1500)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createSessionPriced outcome = %v, want Accepted", outcome)
	}
	if _, got := createBooking(t, ctx, conn, cp, cons, "wdpromopricebook001", sessionKey, seedIdentity(t, ctx, conn, "BBWELLPRMPRCSEATHJKM"), ""); got != processor.OutcomeAccepted {
		t.Fatalf("createBooking outcome = %v, want Accepted", got)
	}
	waitKey, got := joinWaitlist(t, ctx, conn, cp, cons, "wdpromopricejoin001", sessionKey, seedIdentity(t, ctx, conn, "BBWELLPRMPRCWATTHJKM"), "")
	if got != processor.OutcomeAccepted {
		t.Fatalf("joinWaitlist outcome = %v, want Accepted", got)
	}

	// Repriced AND given room in one edit, after the join: the seating that
	// follows reads 2000, the price at that instant.
	edit, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, reassignSessionEnv(t, ctx, conn, "wdpromopriceraise01",
		sessionKey, studioKey, "", domainActorKey,
		map[string]any{"sessionKey": sessionKey, "studio": studioKey, "capacity": 2, "priceCents": 2000},
		"2026-07-08T08:00:00Z"))
	if edit != processor.OutcomeAccepted {
		t.Fatalf("ReassignSession reprice+raise outcome = %v (%+v), want Accepted", edit, reply)
	}
	promote, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		promoteEnv("wdpromopricepromo01", sessionKey, domainActorKey, "2026-07-08T08:05:00Z"))
	if promote != processor.OutcomeAccepted {
		t.Fatalf("PromoteWaitlistedBookings outcome = %v, reply = %+v, want Accepted", promote, reply)
	}

	requirePromoted(t, ctx, conn, waitKey, 2, "2026-07-08T08:05:00Z")
	if v, _ := bookingStatusData(t, ctx, conn, waitKey)["priceCents"].(float64); v != 2000 {
		t.Fatalf("promoted .status.priceCents = %v, want 2000 — the price at SEATING, not the 1500 at the join", bookingStatusData(t, ctx, conn, waitKey)["priceCents"])
	}
}

// TestCancelBooking_RefundUsesSnapshot pins CancelBooking's owes test on the
// snapshot rather than the schedule: a seat claimed at 1500 on a class since
// repriced to free still owes inside the late window — kept live as forfeited,
// its snapshot carried — while the same seat cancelled outside the window
// refunds the charge that actually posted (the marker's amount is the
// transaction's own, the 1500 the ledger charged off the snapshot), whatever
// the class costs now.
func TestCancelBooking_RefundUsesSnapshot(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "cancelsnapshot")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdcancelsnapstudio1", "Snapshot Studio")
	sessionKey, outcome := createSessionPriced(t, ctx, conn, cp, cons, "wdcancelsnapsession", studioKey, "Snapshot Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 5, 1500)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createSessionPriced outcome = %v, want Accepted", outcome)
	}
	lateKey, got := createBooking(t, ctx, conn, cp, cons, "wdcancelsnaplate001", sessionKey, seedIdentity(t, ctx, conn, "BBWELLCNCLSNPLTEHJKM"), "")
	if got != processor.OutcomeAccepted {
		t.Fatalf("createBooking (late) outcome = %v, want Accepted", got)
	}
	earlyKey, got := createBooking(t, ctx, conn, cp, cons, "wdcancelsnapearly01", sessionKey, seedIdentity(t, ctx, conn, "BBWELLCNCLSNPERLHJKM"), "")
	if got != processor.OutcomeAccepted {
		t.Fatalf("createBooking (early) outcome = %v, want Accepted", got)
	}
	_, txKey := seedPostedClassPriceCharge(t, ctx, conn, earlyKey, "BBWELLCNCLSNPACCTHJK", "BBWELLCNCLSNPTXNHJKM", 1500.0)

	// The class goes free after both claims (ReassignSession's own edit path).
	edit, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, reassignSessionEnv(t, ctx, conn, "wdcancelsnapfree001",
		sessionKey, studioKey, "", domainActorKey,
		map[string]any{"sessionKey": sessionKey, "studio": studioKey, "priceCents": 0},
		"2026-07-08T06:00:00Z"))
	if edit != processor.OutcomeAccepted {
		t.Fatalf("ReassignSession reprice outcome = %v (%+v), want Accepted", edit, reply)
	}

	// Inside the window, no charge posted yet: the SNAPSHOT says this seat
	// owes 1500, so the booking is kept live as forfeited — a schedule read
	// would say the class is free and tombstone it.
	lateReq := testutil.GenReqID("wdcancelsnaplatecx1")
	submitCancelBookingAt(t, ctx, conn, cp, cons, lateReq, lateKey, sessionKey, "2026-07-08T08:59:00Z")
	if !keyExists(t, ctx, conn, lateKey) {
		t.Fatalf("a seat that snapshotted 1500 at its claim still owes inside the window — the booking must stay live as forfeited, whatever the class costs now")
	}
	forfeited := attendanceStatus(t, ctx, conn, lateKey)
	if v, _ := forfeited["value"].(string); v != "forfeited" {
		t.Fatalf("late status.value = %q, want forfeited", v)
	}
	if v, _ := forfeited["priceCents"].(float64); v != 1500 {
		t.Fatalf("forfeited status.priceCents = %v, want the 1500 snapshot carried forward", forfeited["priceCents"])
	}
	assertNoTrackerEvent(t, ctx, conn, lateReq, "wellness.classPriceRefundQueued")

	// Outside the window with the charge posted: refunded, the marker's
	// amount being what was actually charged.
	earlyReq := testutil.GenReqID("wdcancelsnapearlycx")
	submitCancelBookingAt(t, ctx, conn, cp, cons, earlyReq, earlyKey, sessionKey, "2026-07-08T06:30:00Z")
	if keyExists(t, ctx, conn, earlyKey) {
		t.Fatalf("an early cancel is tombstoned")
	}
	assertTrackerEvent(t, ctx, conn, earlyReq, "wellness.classPriceRefundQueued")
	refundKey := "vtx.wellnessrefund." + nanoIDFromRequestID(earlyReq)
	detail := readDoc(t, ctx, conn, refundKey+".detail")
	detailData, _ := detail["data"].(map[string]any)
	if v, _ := detailData["amountCents"].(float64); v != 1500 {
		t.Fatalf("refund marker amountCents = %v, want the 1500 charged (%s), not the class's current price", detailData["amountCents"], txKey)
	}
}

// TestReassignSession_RefusesCapacityBelowSeated: a class of five with seats 1
// and 2 claimed refuses a shrink to 1, naming seat 2 — the highest claimed
// index in the removed range — and accepts a shrink to 2.
func TestReassignSession_RefusesCapacityBelowSeated(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "capbelowseated")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdcapbelowstudio001", "Shrink Studio")
	sessionKey, outcome := createSession(t, ctx, conn, cp, cons, "wdcapbelowsession01", studioKey, "Shrink Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 5)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createSession outcome = %v, want Accepted", outcome)
	}
	for i, id := range []string{"BBWELLCAPBLWSEAT1HJK", "BBWELLCAPBLWSEAT2HJK"} {
		if _, got := createBooking(t, ctx, conn, cp, cons, "wdcapbelowbook0000"+string(rune('1'+i)), sessionKey, seedIdentity(t, ctx, conn, id), ""); got != processor.OutcomeAccepted {
			t.Fatalf("createBooking %d outcome = %v, want Accepted", i+1, got)
		}
	}

	shrink, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, reassignSessionEnv(t, ctx, conn, "wdcapbelowshrink001",
		sessionKey, studioKey, "", domainActorKey,
		map[string]any{"sessionKey": sessionKey, "studio": studioKey, "capacity": 1},
		"2026-07-08T08:00:00Z"))
	if shrink != processor.OutcomeRejected {
		t.Fatalf("ReassignSession capacity 1 under two claimed seats = %v, want Rejected (CapacityBelowSeated)", shrink)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "CapacityBelowSeated: seat 2 is claimed; capacity cannot drop below 2") {
		t.Fatalf("rejection should be CapacityBelowSeated naming seat 2, got %+v", reply.Error)
	}
	if got, _ := readDoc(t, ctx, conn, sessionKey+".schedule")["data"].(map[string]any)["capacity"].(float64); got != 5 {
		t.Fatalf("schedule.capacity = %v after the refusal, want 5 untouched", got)
	}

	fit, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, reassignSessionEnv(t, ctx, conn, "wdcapbelowshrink002",
		sessionKey, studioKey, "", domainActorKey,
		map[string]any{"sessionKey": sessionKey, "studio": studioKey, "capacity": 2},
		"2026-07-08T08:01:00Z"))
	if fit != processor.OutcomeAccepted {
		t.Fatalf("ReassignSession capacity 2 over seats 1 and 2 = %v (%+v), want Accepted", fit, reply)
	}
	if got, _ := readDoc(t, ctx, conn, sessionKey+".schedule")["data"].(map[string]any)["capacity"].(float64); got != 2 {
		t.Fatalf("schedule.capacity = %v, want 2", got)
	}
}

// TestReassignSession_ShrinkWithHighClaimedIndexRefused pins the rule as the
// HIGHEST claimed index, not the seated count: seats 1 and 5 claimed on a
// class of five (seat 2..4 released by cancellations — seats are never
// compacted) refuse a shrink to 3 even though two members fit, naming seat 5.
func TestReassignSession_ShrinkWithHighClaimedIndexRefused(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "caphighindex")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdcaphighstudio0001", "Gap Studio")
	sessionKey, outcome := createSession(t, ctx, conn, cp, cons, "wdcaphighsession001", studioKey, "Gap Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 5)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createSession outcome = %v, want Accepted", outcome)
	}
	var middle []string
	for i, id := range []string{"BBWELLCAPHGHSEAT1HJK", "BBWELLCAPHGHSEAT2HJK", "BBWELLCAPHGHSEAT3HJK", "BBWELLCAPHGHSEAT4HJK", "BBWELLCAPHGHSEAT5HJK"} {
		key, got := createBooking(t, ctx, conn, cp, cons, "wdcaphighbook00000"+string(rune('1'+i)), sessionKey, seedIdentity(t, ctx, conn, id), "")
		if got != processor.OutcomeAccepted {
			t.Fatalf("createBooking %d outcome = %v, want Accepted", i+1, got)
		}
		if i >= 1 && i <= 3 {
			middle = append(middle, key)
		}
	}
	// Seats 2, 3 and 4 released with an empty waitlist, so each cell is
	// tombstoned rather than handed on: seats 1 and 5 remain claimed.
	for i, key := range middle {
		submitCancelBookingAt(t, ctx, conn, cp, cons, testutil.GenReqID("wdcaphighcancel000"+string(rune('1'+i))), key, sessionKey, "2026-07-07T12:30:00Z")
	}
	for _, n := range []string{"2", "3", "4"} {
		if keyExists(t, ctx, conn, sessionKey+".seat"+n) {
			t.Fatalf("fixture: seat %s must be released", n)
		}
	}
	if !keyExists(t, ctx, conn, sessionKey+".seat5") {
		t.Fatalf("fixture: seat 5 must still be claimed")
	}

	shrink, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, reassignSessionEnv(t, ctx, conn, "wdcaphighshrink0001",
		sessionKey, studioKey, "", domainActorKey,
		map[string]any{"sessionKey": sessionKey, "studio": studioKey, "capacity": 3},
		"2026-07-08T08:00:00Z"))
	if shrink != processor.OutcomeRejected {
		t.Fatalf("ReassignSession capacity 3 with seat 5 claimed = %v, want Rejected (CapacityBelowSeated) — two members fit, but seat 5's holder would be orphaned", shrink)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "CapacityBelowSeated: seat 5 is claimed; capacity cannot drop below 5") {
		t.Fatalf("rejection should be CapacityBelowSeated naming seat 5 (the highest claimed index, not the count), got %+v", reply.Error)
	}
}
