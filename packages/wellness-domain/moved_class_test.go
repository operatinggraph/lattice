package wellnessdomain_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"
	wellnessdomain "github.com/operatinggraph/lattice/packages/wellness-domain"
)

// The booker slot cells a class's span holds are the member's double-book
// guard (bookerSlotClaim, ddls.go): CreateBooking / JoinWaitlist claim them on
// the booker's own identity hub, and a time move must carry them with the
// class — left behind, the old hour stays refused with no booking to show
// for it and the new hour guards nothing. These pins prove the delta on
// ReassignSession (one class) and ReassignSessionSeries (a run), the refusal
// when a member already holds the new hour elsewhere, and the two schedule
// stamps a swap / room move record for wellness-reminders' change notices.

func TestReassignSession_MovesBookersCellsWithTheClass(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "movedclasscells")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdmvcellsstudio00001", "Flow Room")
	sessionKey, outcome := createSession(t, ctx, conn, cp, cons, "wdmvcellssession0001", studioKey, "Vinyasa Flow",
		"2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 1)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession = %v, want Accepted", outcome)
	}
	seated := seedIdentity(t, ctx, conn, "BBWELLMVCELLSEATABCD")
	waiting := seedIdentity(t, ctx, conn, "BBWELLMVCELLWAYTABCD")
	seatedBooking, o := createBooking(t, ctx, conn, cp, cons, "wdmvcellsbook0000001", sessionKey, seated, "")
	if o != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking = %v, want Accepted", o)
	}
	if _, o := joinWaitlist(t, ctx, conn, cp, cons, "wdmvcellswait0000001", sessionKey, waiting, ""); o != processor.OutcomeAccepted {
		t.Fatalf("JoinWaitlist = %v, want Accepted", o)
	}
	assertCells(t, ctx, conn, seated, "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", true, "seated member before the move")
	assertCells(t, ctx, conn, waiting, "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", true, "waitlisted member before the move")

	// 09:00–09:30 → 09:15–09:45: the 09:15 cell is shared by both spans and
	// stays; 09:00 releases; 09:30 claims.
	env := reassignSessionEnv(t, ctx, conn, "wdmvcellsmove0000001", sessionKey, studioKey, "", domainActorKey,
		map[string]any{"sessionKey": sessionKey, "studio": studioKey, "startsAt": "2026-07-08T09:15:00Z", "endsAt": "2026-07-08T09:45:00Z"},
		"2026-07-07T13:00:00Z")
	outcome2, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome2 != processor.OutcomeAccepted {
		t.Fatalf("ReassignSession time move = %v (%+v), want Accepted", outcome2, reply.Error)
	}
	for _, hub := range []string{seated, waiting} {
		if keyExists(t, ctx, conn, hub+".slot"+wdSlotCellCode("2026-07-08T09:00:00Z")) {
			t.Errorf("%s: the 09:00 cell must be released by the move", hub)
		}
		assertCells(t, ctx, conn, hub, "2026-07-08T09:15:00Z", "2026-07-08T09:45:00Z", true, "member after the move")
	}
	// The cells ARE the guard: the member can now be booked at 09:00 on
	// another class (the old hour is free) and not at 09:30 (the new hour is
	// held).
	otherStudio := createStudio(t, ctx, conn, cp, cons, "wdmvcellsstudio00002", "Other Room")
	freed, _ := createSession(t, ctx, conn, cp, cons, "wdmvcellsfreed000001", otherStudio, "Early", "2026-07-08T09:00:00Z", "2026-07-08T09:15:00Z", 5)
	if _, o := createBooking(t, ctx, conn, cp, cons, "wdmvcellsbookfree001", freed, seated, ""); o != processor.OutcomeAccepted {
		t.Fatalf("a booking on the freed 09:00 hour = %v, want Accepted", o)
	}
	held, _ := createSession(t, ctx, conn, cp, cons, "wdmvcellsheld0000001", otherStudio, "Late", "2026-07-08T09:30:00Z", "2026-07-08T09:45:00Z", 5)
	heldEnv := createBookingEnv(t, ctx, conn, "wdmvcellsbookheld001", held, seated)
	if o, r := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, heldEnv); o != processor.OutcomeRejected || r.Error == nil || !strings.Contains(r.Error.Message, "BookerConflict") {
		t.Fatalf("a booking on the newly held 09:30 hour = %v (%+v), want BookerConflict", o, r.Error)
	}

	// A cancel releases the cells on the span the class now holds.
	cancelEnv := &processor.OperationEnvelope{
		RequestID: testutil.GenReqID("wdmvcellscancel00001"), Lane: processor.LaneDefault,
		OperationType: "CancelBooking", Actor: domainActorKey, SubmittedAt: "2026-07-07T14:00:00Z", Class: "booking",
		Payload: json.RawMessage(`{"bookingKey":"` + seatedBooking + `","session":"` + sessionKey + `"}`),
		ContextHint: &processor.ContextHint{Enumerations: testutil.DeclaredEnumerations("CancelBooking", domainActorKey, wellnessdomain.OpMetas()),
			Reads: []string{seatedBooking, seatedBooking + ".status", sessionKey + ".schedule", forSessionLnkKey(t, seatedBooking, sessionKey)}},
	}
	if o, r := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, cancelEnv); o != processor.OutcomeAccepted {
		t.Fatalf("CancelBooking after the move = %v (%+v), want Accepted", o, r.Error)
	}
	assertCells(t, ctx, conn, seated, "2026-07-08T09:15:00Z", "2026-07-08T09:45:00Z", false, "seated member after the cancel")
}

func TestReassignSession_MemberHoldingTheNewHourRefusesTheMove(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "movedclassconflict")

	studioA := createStudio(t, ctx, conn, cp, cons, "wdmvconfstudioa00001", "Room A")
	studioB := createStudio(t, ctx, conn, cp, cons, "wdmvconfstudiob00001", "Room B")
	moving, _ := createSession(t, ctx, conn, cp, cons, "wdmvconfmoving000001", studioA, "Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 5)
	elsewhere, _ := createSession(t, ctx, conn, cp, cons, "wdmvconfelsewhere001", studioB, "Barre", "2026-07-08T10:00:00Z", "2026-07-08T10:30:00Z", 5)
	member := seedIdentity(t, ctx, conn, "BBWELLMVCNFMEMBRABCD")
	for i, sk := range []string{moving, elsewhere} {
		if _, o := createBooking(t, ctx, conn, cp, cons, "wdmvconfbook000000"+string(rune('1'+i)), sk, member, ""); o != processor.OutcomeAccepted {
			t.Fatalf("CreateBooking %d = %v, want Accepted", i, o)
		}
	}
	move := func(label, startsAt, endsAt string) (processor.MessageOutcome, *processor.OperationReply) {
		env := reassignSessionEnv(t, ctx, conn, label, moving, studioA, "", domainActorKey,
			map[string]any{"sessionKey": moving, "studio": studioA, "startsAt": startsAt, "endsAt": endsAt}, "2026-07-07T13:00:00Z")
		return testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	}
	// Onto the member's other class: the whole move is refused, nothing moved.
	outcome, reply := move("wdmvconfmoveonto0001", "2026-07-08T10:00:00Z", "2026-07-08T10:30:00Z")
	if outcome != processor.OutcomeRejected {
		t.Fatalf("a move onto a member's other class = %v, want Rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "BookerConflict") || !strings.Contains(reply.Error.Message, member) {
		t.Fatalf("refusal must be BookerConflict naming %s, got %+v", member, reply.Error)
	}
	if starts, _, _, _ := sessionSchedule(t, ctx, conn, moving); starts != "2026-07-08T09:00:00Z" {
		t.Fatalf("a refused move must leave the schedule at 09:00, got %s", starts)
	}
	assertCells(t, ctx, conn, member, "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", true, "member's cells after the refused move")
	// The positive vector on the same fixture: a free hour moves.
	if outcome, reply := move("wdmvconfmovefree0001", "2026-07-08T11:00:00Z", "2026-07-08T11:30:00Z"); outcome != processor.OutcomeAccepted {
		t.Fatalf("a move onto a free hour = %v (%+v), want Accepted", outcome, reply.Error)
	}
	assertCells(t, ctx, conn, member, "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", false, "member's old cells after the move")
	assertCells(t, ctx, conn, member, "2026-07-08T11:00:00Z", "2026-07-08T11:30:00Z", true, "member's new cells after the move")
}

func TestReassignSession_StampsWhoLeadsAndWhereItMeets(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "movedclassstamps")

	opDoc := domainCapDoc()
	opDoc.PlatformPermissions = append(opDoc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "CreateInstructor", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, opDoc)
	mkInstructor := func(label, name string) string {
		reqID := testutil.GenReqID(label)
		testutil.PublishOp(t, conn, &processor.OperationEnvelope{
			RequestID: reqID, Lane: processor.LaneDefault, OperationType: "CreateInstructor",
			Actor: domainActorKey, SubmittedAt: "2026-07-07T12:00:00Z", Class: "instructor",
			Payload: json.RawMessage(`{"displayName":"` + name + `"}`),
		})
		testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
		return "vtx.instructor." + nanoIDFromRequestID(reqID)
	}
	instructorA := mkInstructor("wdmvstampinstra00001", "A")
	instructorB := mkInstructor("wdmvstampinstrb00001", "B")
	studioA := createStudio(t, ctx, conn, cp, cons, "wdmvstampstudioa0001", "Room A")
	studioB := createStudio(t, ctx, conn, cp, cons, "wdmvstampstudiob0001", "Room B")
	sessionKey, outcome, _ := createSessionWithInstructor(t, ctx, conn, cp, cons, "wdmvstampsession0001", studioA, instructorA,
		"Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 5)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession = %v, want Accepted", outcome)
	}
	stamps := func() (string, string) {
		_, _, _, data := sessionSchedule(t, ctx, conn, sessionKey)
		i, _ := data["instructorChangedAt"].(string)
		s, _ := data["studioChangedAt"].(string)
		return i, s
	}
	if i, s := stamps(); i != "" || s != "" {
		t.Fatalf("a minted class carries no change stamp, got instructor %q studio %q", i, s)
	}
	reassign := func(label, current, submittedAt string, payload map[string]any) {
		payload["sessionKey"] = sessionKey
		env := reassignSessionEnv(t, ctx, conn, label, sessionKey, payload["studio"].(string), current, domainActorKey, payload, submittedAt)
		if o, r := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env); o != processor.OutcomeAccepted {
			t.Fatalf("%s = %v (%+v), want Accepted", label, o, r.Error)
		}
	}
	// A swap stamps the instructor, not the room.
	reassign("wdmvstampswap0000001", instructorA, "2026-07-07T13:00:00Z", map[string]any{"studio": studioA, "newInstructor": instructorB})
	if i, s := stamps(); i != "2026-07-07T13:00:00Z" || s != "" {
		t.Fatalf("after a swap: instructor %q studio %q, want 2026-07-07T13:00:00Z / none", i, s)
	}
	// A name edit carries the stamp unchanged.
	reassign("wdmvstampname0000001", instructorB, "2026-07-07T13:30:00Z", map[string]any{"studio": studioA, "name": "Slow Flow"})
	if i, s := stamps(); i != "2026-07-07T13:00:00Z" || s != "" {
		t.Fatalf("after a name edit: instructor %q studio %q, want the swap's stamp carried / none", i, s)
	}
	// A room move stamps the studio and carries the instructor's.
	reassign("wdmvstamproom0000001", instructorB, "2026-07-07T14:00:00Z", map[string]any{"studio": studioA, "newStudio": studioB})
	if i, s := stamps(); i != "2026-07-07T13:00:00Z" || s != "2026-07-07T14:00:00Z" {
		t.Fatalf("after a room move: instructor %q studio %q, want 13:00 carried / 14:00", i, s)
	}
	// A clear is a change of who leads: the instructor stamp advances.
	reassign("wdmvstampclear000001", instructorB, "2026-07-07T15:00:00Z", map[string]any{"studio": studioB, "clearInstructor": true})
	if i, s := stamps(); i != "2026-07-07T15:00:00Z" || s != "2026-07-07T14:00:00Z" {
		t.Fatalf("after a clear: instructor %q studio %q, want 15:00 / 14:00 carried", i, s)
	}
}

func TestReassignSessionSeries_MovesARegularsCellsAcrossTheRun(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "movedseriescells")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdmvsercellstudio001", "Flow Room")
	seriesKey, sessionKeys, outcome := createSessionSeriesLed(t, ctx, conn, cp, cons,
		"wdmvsercellcreate001", studioKey, "", "Evening Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20, 7, 3)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSessionSeries = %v, want Accepted", outcome)
	}
	regular := seedIdentity(t, ctx, conn, "BBWELLMVSERREGULARAB")
	for i, sk := range sessionKeys {
		if _, o := createBooking(t, ctx, conn, cp, cons, "wdmvsercellbook0000"+string(rune('1'+i)), sk, regular, ""); o != processor.OutcomeAccepted {
			t.Fatalf("CreateBooking on occurrence %d = %v, want Accepted", i, o)
		}
	}
	// Every class one week later: occurrence i lands on the cells occurrence
	// i+1 vacates — the regular's hub writes only the difference (the first
	// week releases, a fourth week claims), so the shift is accepted.
	got, why := reassignSeries(t, ctx, conn, cp, cons, "wdmvsercellmove00001", seriesKey, studioKey, sessionKeys[0], "2026-07-08T09:00:00Z",
		"2026-07-15T09:00:00Z", "2026-07-15T09:30:00Z", "2026-07-07T12:00:00Z")
	if got != processor.OutcomeAccepted {
		t.Fatalf("ReassignSessionSeries by its own interval = %v (%s), want Accepted", got, why)
	}
	assertCells(t, ctx, conn, regular, "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", false, "the regular's first week after the shift")
	for _, week := range []string{"2026-07-15", "2026-07-22", "2026-07-29"} {
		assertCells(t, ctx, conn, regular, week+"T09:00:00Z", week+"T09:30:00Z", true, "the regular's "+week)
	}
	// A member seated on one occurrence alone moves with that occurrence only.
	once := seedIdentity(t, ctx, conn, "BBWELLMVSERUNCEABCDE")
	if _, o := createBooking(t, ctx, conn, cp, cons, "wdmvsercellbookonce1", sessionKeys[1], once, ""); o != processor.OutcomeAccepted {
		t.Fatalf("CreateBooking (once) = %v, want Accepted", o)
	}
	got, why = reassignSeries(t, ctx, conn, cp, cons, "wdmvsercellmove00002", seriesKey, studioKey, sessionKeys[0], "2026-07-15T09:00:00Z",
		"2026-07-15T18:00:00Z", "2026-07-15T18:30:00Z", "2026-07-07T12:30:00Z")
	if got != processor.OutcomeAccepted {
		t.Fatalf("ReassignSessionSeries to the evening = %v (%s), want Accepted", got, why)
	}
	assertCells(t, ctx, conn, once, "2026-07-22T09:00:00Z", "2026-07-22T09:30:00Z", false, "the once-member's morning")
	assertCells(t, ctx, conn, once, "2026-07-22T18:00:00Z", "2026-07-22T18:30:00Z", true, "the once-member's evening")
	assertCells(t, ctx, conn, once, "2026-07-15T18:00:00Z", "2026-07-15T18:30:00Z", false, "the once-member holds no other week")
	for _, week := range []string{"2026-07-15", "2026-07-22", "2026-07-29"} {
		assertCells(t, ctx, conn, regular, week+"T09:00:00Z", week+"T09:30:00Z", false, "the regular's morning "+week)
		assertCells(t, ctx, conn, regular, week+"T18:00:00Z", week+"T18:30:00Z", true, "the regular's evening "+week)
	}
}

// The batch ceiling is refused by name, projected from the membership before
// a single claim read: 123 members clear of their old cells on a one-hour
// class is 984 cell mutations beside the studio's 8 and the schedule — over
// the 990 one operation may commit — and
// the desk reads MoveTooLarge naming the count, not a commit-time fault or a
// script timeout. The positive vector is the same class one member lighter.
func TestReassignSession_MoveTooLargeIsRefusedByName(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "movedclasstoolarge")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdmvbigstudio0000001", "Hall")
	sessionKey, outcome := createSession(t, ctx, conn, cp, cons, "wdmvbigsession000001", studioKey, "Big Flow",
		"2026-07-08T09:00:00Z", "2026-07-08T10:00:00Z", 130)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("CreateSession = %v, want Accepted", outcome)
	}
	lastBooking := ""
	for i := 0; i < 123; i++ {
		member := seedIdentity(t, ctx, conn, "BBWELLMVBGMEMBER"+nanoDigits(i)+"x")
		bk, o := createBooking(t, ctx, conn, cp, cons, "wdmvbigbook"+strconv.Itoa(100000+i), sessionKey, member, "")
		if o != processor.OutcomeAccepted {
			t.Fatalf("CreateBooking %d = %v, want Accepted", i, o)
		}
		lastBooking = bk
	}
	move := func(label string) (processor.MessageOutcome, *processor.OperationReply) {
		env := reassignSessionEnv(t, ctx, conn, label, sessionKey, studioKey, "", domainActorKey,
			map[string]any{"sessionKey": sessionKey, "studio": studioKey, "startsAt": "2026-07-08T11:00:00Z", "endsAt": "2026-07-08T12:00:00Z"}, "2026-07-07T13:00:00Z")
		return testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	}
	outcome, reply := move("wdmvbigmove00000001")
	if outcome != processor.OutcomeRejected || reply.Error == nil || !strings.Contains(reply.Error.Message, "MoveTooLarge") || !strings.Contains(reply.Error.Message, "123 members") {
		t.Fatalf("123 members clear of their cells = %v (%+v), want MoveTooLarge naming 123 members", outcome, reply.Error)
	}
	if starts, _, _, _ := sessionSchedule(t, ctx, conn, sessionKey); starts != "2026-07-08T09:00:00Z" {
		t.Fatalf("a refused move must leave the schedule at 09:00, got %s", starts)
	}
	// One member lighter fits.
	cancelEnv := &processor.OperationEnvelope{
		RequestID: testutil.GenReqID("wdmvbigcancel0000001"), Lane: processor.LaneDefault,
		OperationType: "CancelBooking", Actor: domainActorKey, SubmittedAt: "2026-07-07T13:30:00Z", Class: "booking",
		Payload: json.RawMessage(`{"bookingKey":"` + lastBooking + `","session":"` + sessionKey + `"}`),
		ContextHint: &processor.ContextHint{Enumerations: testutil.DeclaredEnumerations("CancelBooking", domainActorKey, wellnessdomain.OpMetas()),
			Reads: []string{lastBooking, lastBooking + ".status", sessionKey + ".schedule", forSessionLnkKey(t, lastBooking, sessionKey)}},
	}
	if o, r := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, cancelEnv); o != processor.OutcomeAccepted {
		t.Fatalf("CancelBooking = %v (%+v), want Accepted", o, r.Error)
	}
	if outcome, reply := move("wdmvbigmove00000002"); outcome != processor.OutcomeAccepted {
		t.Fatalf("122 members = %v (%+v), want Accepted", outcome, reply.Error)
	}
}

// nanoDigits renders n as three digits from the NanoID alphabet's '1'..'9'
// (base 9, zero excluded — '0' is outside the alphabet), for seeded keys.
func nanoDigits(n int) string {
	out := ""
	for range 3 {
		out = string(rune('1'+n%9)) + out
		n /= 9
	}
	return out
}

// A booking's .status outlives its root (CancelBooking tombstones the root
// alone; the aspect is what a later release or notice reads), so a walk over
// a session's bookings that reads .status alone counts cancelled seats: a
// cancelled waitlister would be promoted into a freed seat, and a cancelled
// member's hub would have cells claimed for a class they left. Both walks
// check the root.
func TestSessionBookingWalks_SkipACancelledBookingWhoseStatusOutlivesIt(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "walkscancelled")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdwalkstudio00000001", "Small Studio")
	sessionKey, _ := createSession(t, ctx, conn, cp, cons, "wdwalksession0000001", studioKey, "Intro", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 1)
	seated := seedIdentity(t, ctx, conn, "BBWELLWALKSEATEDABCD")
	leaver := seedIdentity(t, ctx, conn, "BBWELLWALKLEAVERABCD")
	stayer := seedIdentity(t, ctx, conn, "BBWELLWALKSTAYERABCD")
	seatedBooking, _ := createBooking(t, ctx, conn, cp, cons, "wdwalkbook0000000001", sessionKey, seated, "")
	leaverBooking, _ := joinWaitlist(t, ctx, conn, cp, cons, "wdwalkwait0000000001", sessionKey, leaver, "")
	stayerBooking, _ := joinWaitlist(t, ctx, conn, cp, cons, "wdwalkwait0000000002", sessionKey, stayer, "")
	cancel := func(label, bookingKey, at string) {
		env := &processor.OperationEnvelope{
			RequestID: testutil.GenReqID(label), Lane: processor.LaneDefault,
			OperationType: "CancelBooking", Actor: domainActorKey, SubmittedAt: at, Class: "booking",
			Payload: json.RawMessage(`{"bookingKey":"` + bookingKey + `","session":"` + sessionKey + `"}`),
			ContextHint: &processor.ContextHint{Enumerations: testutil.DeclaredEnumerations("CancelBooking", domainActorKey, wellnessdomain.OpMetas()),
				Reads: []string{bookingKey, bookingKey + ".status", sessionKey + ".schedule", forSessionLnkKey(t, bookingKey, sessionKey)}},
		}
		if o, r := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env); o != processor.OutcomeAccepted {
			t.Fatalf("%s = %v (%+v), want Accepted", label, o, r.Error)
		}
	}
	// The first waitlister leaves; their .status still reads waitlisted.
	cancel("wdwalkcancelleaver01", leaverBooking, "2026-07-07T12:10:00Z")
	if st, _ := readDoc(t, ctx, conn, leaverBooking+".status")["data"].(map[string]any); st["value"] != "waitlisted" {
		t.Fatalf("the premise: a cancelled waitlister's .status outlives the root, got %v", st["value"])
	}
	// The seat frees: the promotion must pass over the leaver to the stayer.
	cancel("wdwalkcancelseated01", seatedBooking, "2026-07-07T12:20:00Z")
	if st, _ := readDoc(t, ctx, conn, stayerBooking+".status")["data"].(map[string]any); st["value"] != "booked" {
		t.Fatalf("the stayer must be promoted, got %v", st["value"])
	}
	if st, _ := readDoc(t, ctx, conn, leaverBooking+".status")["data"].(map[string]any); st["value"] != "waitlisted" {
		t.Fatalf("a cancelled waitlister must never be promoted, got %v", st["value"])
	}
	// A move must carry the stayer's cells and leave the leaver's hub alone.
	env := reassignSessionEnv(t, ctx, conn, "wdwalkmove0000000001", sessionKey, studioKey, "", domainActorKey,
		map[string]any{"sessionKey": sessionKey, "studio": studioKey, "startsAt": "2026-07-08T11:00:00Z", "endsAt": "2026-07-08T11:30:00Z"}, "2026-07-07T13:00:00Z")
	if o, r := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env); o != processor.OutcomeAccepted {
		t.Fatalf("ReassignSession = %v (%+v), want Accepted", o, r.Error)
	}
	assertCells(t, ctx, conn, stayer, "2026-07-08T11:00:00Z", "2026-07-08T11:30:00Z", true, "the stayer's moved cells")
	assertCells(t, ctx, conn, leaver, "2026-07-08T11:00:00Z", "2026-07-08T11:30:00Z", false, "the leaver holds nothing on the new hour")
	assertCells(t, ctx, conn, seated, "2026-07-08T11:00:00Z", "2026-07-08T11:30:00Z", false, "the cancelled member holds nothing on the new hour")
}
