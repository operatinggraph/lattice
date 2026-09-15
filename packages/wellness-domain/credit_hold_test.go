package wellnessdomain_test

// The credit hold: a member whose wellness-ledger account carries an arrears
// episode a reminder has gone out for (.arrears.sentAt present) claims no new
// seat — CreateBooking AND JoinWaitlist, on the front-desk leg AND the
// member's own self-service leg. The .arrears fixture is seeded directly as
// state, the way cafe-domain's TestOpenTab_RefusesCreditHold seeds café's:
// wellness-ledger is not installed here, and the hold reads a recorded
// aspect, not the op that wrote it. Every refusal vector is paired with the
// positive sibling that differs from it in exactly the field under test, so a
// Rejected is the hold talking and not a broken path.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// remindedArrears is the .arrears state EvaluateWellnessArrears leaves once
// the reminder has gone out for an episode that is still open — the hold.
func remindedArrears() map[string]any {
	return map[string]any{
		"dueAt":       "2026-06-20T12:00:00Z",
		"remindedFor": "2026-06-20T12:00:00Z",
		"sentAt":      "2026-06-21T09:00:00Z",
		"evaluatedAt": "2026-06-21T09:00:00Z",
	}
}

// seedWellnessAccount seeds a wellnessaccount held for a member identity —
// the shape wellness-ledger's WellnessCreateAccount commits (the vertex and
// the heldFor link, account → identity), without that package installed.
func seedWellnessAccount(t *testing.T, ctx context.Context, conn *substrate.Conn, acctID, identityKey string) string {
	t.Helper()
	acctKey := "vtx.wellnessaccount." + acctID
	_, identityID, _ := substrate.ParseVertexKey(identityKey)
	seedVertex(t, ctx, conn, acctKey, "wellnessaccount", map[string]any{})
	seedLink(t, ctx, conn,
		"lnk.wellnessaccount."+acctID+".heldFor.identity."+identityID,
		acctKey, identityKey, "heldFor", "heldFor")
	return acctKey
}

// seedTombstonedArrears plants a .arrears document as a TOMBSTONE — present,
// isDeleted true, exactly what kv.Read hands a script for a soft-deleted key.
// No wellness-ledger op tombstones the aspect, so the shape has to be planted;
// the hold must still read it as "no episode", because a tombstone is a
// document, not an absence.
func seedTombstonedArrears(t *testing.T, ctx context.Context, conn *substrate.Conn, acctKey string, data map[string]any) {
	t.Helper()
	doc := map[string]any{
		"class": "wellnessAccountArrears", "isDeleted": true,
		"vertexKey": acctKey, "localName": "arrears", "data": data,
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, acctKey+".arrears", b); err != nil {
		t.Fatalf("seed tombstoned aspect %s.arrears: %v", acctKey, err)
	}
}

// bookingEntryAs submits CreateBooking or JoinWaitlist as an arbitrary actor
// with the declared read posture createBooking/joinWaitlist use, optionally
// under a self-scope target, and returns the outcome plus the script's own
// failure text so a rejection is attributable to one guard.
func bookingEntryAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	opType, label, sessionKey, bookerKey, actorKey, authTarget, submittedAt string) (string, processor.MessageOutcome, string) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	payload, _ := json.Marshal(map[string]any{"session": sessionKey, "booker": bookerKey})
	_, bookerID, _ := substrate.ParseVertexKey(bookerKey)
	var optionalReads []string
	if opType == "CreateBooking" {
		optionalReads = wdSeatKeys(sessionKey, 20)
	} else {
		optionalReads = wdWaitlistKeys(sessionKey, 20)
	}
	optionalReads = append(optionalReads, sessionKey+".bkr"+bookerID)
	sched := readDoc(t, ctx, conn, sessionKey+".schedule")
	schedData, _ := sched["data"].(map[string]any)
	startsAt, _ := schedData["startsAt"].(string)
	endsAt, _ := schedData["endsAt"].(string)
	optionalReads = append(optionalReads, wdSlotClaimKeys(t, bookerKey, startsAt, endsAt)...)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: opType,
		Actor:         actorKey,
		SubmittedAt:   submittedAt,
		Class:         "booking",
		Payload:       payload,
		ContextHint: &processor.ContextHint{Enumerations: bookingEnumerations(t, opType, actorKey, bookerKey),
			Reads:         []string{sessionKey, sessionKey + ".schedule", bookerKey},
			OptionalReads: optionalReads,
		},
	}
	if authTarget != "" {
		env.AuthContext = &processor.AuthContext{Target: authTarget}
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

// holdRefused asserts a booking entry was refused by the credit hold itself —
// its own text, naming the day the reminder's outbox event was committed
// (sentAt[:10]) — and that nothing was minted.
func holdRefused(t *testing.T, ctx context.Context, conn *substrate.Conn, bookingKey string, outcome processor.MessageOutcome, why, leg string) {
	t.Helper()
	if outcome != processor.OutcomeRejected {
		t.Fatalf("%s: outcome = %v, want Rejected (CreditHold)", leg, outcome)
	}
	if !strings.HasPrefix(why, "CreditHold: this member owes a balance a reminder went out for on 2026-06-21;") {
		t.Fatalf("%s: rejected by something other than the credit hold, or with the wrong date: %q", leg, why)
	}
	if keyExists(t, ctx, conn, bookingKey) {
		t.Fatalf("%s: a held entry minted %s; it must be refused before any mutation", leg, bookingKey)
	}
}

// TestBookingEntry_RefusesCreditHold_StaffLeg is the front-desk leg, as a
// frontOfHouse hat that is NOT the operator — the confined path, whose guard
// reads the walk the operator short-circuits. The positive vectors come
// first, one per field the hold could mis-read: a member with no account, a
// member overdue but not yet reminded (dueAt without sentAt), and a member
// whose episode ended ({evaluatedAt} alone) all book and waitlist. The
// reminded member is refused on BOTH ops.
func TestBookingEntry_RefusesCreditHold_StaffLeg(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "credithold")
	wcSeedStaff(t, ctx, conn)

	studio := createStudio(t, ctx, conn, cp, cons, "wdholdstudio00000001", "Hold Room")
	wfSeedStudioAt(t, ctx, conn, studio, wcBuildingAKey, wcBuildingAID)
	morning, _ := createSession(t, ctx, conn, cp, cons, "wdholdsession0000001",
		studio, "Morning Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	evening, _ := createSession(t, ctx, conn, cp, cons, "wdholdsession0000002",
		studio, "Evening Flow", "2026-07-08T18:00:00Z", "2026-07-08T18:30:00Z", 20)

	// No account at all: nothing to hold on.
	noAcct := seedIdentity(t, ctx, conn, "BBWELLHQLDNQACCTHJKM")
	if _, got, why := bookingEntryAs(t, ctx, conn, cp, cons, "CreateBooking", "wdholdnoacctbook0001",
		morning, noAcct, wcStaffKey, "", "2026-07-07T12:00:00Z"); got != processor.OutcomeAccepted {
		t.Fatalf("staff CreateBooking for a member with no account = %v (%s), want Accepted", got, why)
	}
	if _, got, why := bookingEntryAs(t, ctx, conn, cp, cons, "JoinWaitlist", "wdholdnoacctwait0001",
		evening, noAcct, wcStaffKey, "", "2026-07-07T12:00:00Z"); got != processor.OutcomeAccepted {
		t.Fatalf("staff JoinWaitlist for a member with no account = %v (%s), want Accepted", got, why)
	}

	// Overdue, not yet reminded: dueAt without sentAt is not a hold.
	overdue := seedIdentity(t, ctx, conn, "BBWELLHQLDQVERDUEHJK")
	overdueAcct := seedWellnessAccount(t, ctx, conn, "BBWELLHQLDQVERDUEACC", overdue)
	seedAspect(t, ctx, conn, overdueAcct, "arrears", "wellnessAccountArrears", map[string]any{
		"dueAt": "2026-06-20T12:00:00Z", "evaluatedAt": "2026-07-06T03:00:00Z",
	})
	if _, got, why := bookingEntryAs(t, ctx, conn, cp, cons, "CreateBooking", "wdholdoverduebook001",
		morning, overdue, wcStaffKey, "", "2026-07-07T12:00:00Z"); got != processor.OutcomeAccepted {
		t.Fatalf("staff CreateBooking on an overdue-but-unreminded account = %v (%s), want Accepted", got, why)
	}
	if _, got, why := bookingEntryAs(t, ctx, conn, cp, cons, "JoinWaitlist", "wdholdoverduewait001",
		evening, overdue, wcStaffKey, "", "2026-07-07T12:00:00Z"); got != processor.OutcomeAccepted {
		t.Fatalf("staff JoinWaitlist on an overdue-but-unreminded account = %v (%s), want Accepted", got, why)
	}

	// Owes nothing: the episode ended and {evaluatedAt} alone remains.
	square := seedIdentity(t, ctx, conn, "BBWELLHQLDSQUAREHJKM")
	squareAcct := seedWellnessAccount(t, ctx, conn, "BBWELLHQLDSQUAREACCT", square)
	seedAspect(t, ctx, conn, squareAcct, "arrears", "wellnessAccountArrears", map[string]any{
		"evaluatedAt": "2026-07-06T03:00:00Z",
	})
	if _, got, why := bookingEntryAs(t, ctx, conn, cp, cons, "CreateBooking", "wdholdsquarebook0001",
		morning, square, wcStaffKey, "", "2026-07-07T12:00:00Z"); got != processor.OutcomeAccepted {
		t.Fatalf("staff CreateBooking on a square account = %v (%s), want Accepted", got, why)
	}

	// A TOMBSTONED reminded episode is no episode: present, isDeleted, and
	// still carrying sentAt — the '== None' test alone would read it as live.
	buried := seedIdentity(t, ctx, conn, "BBWELLHQLDBURYDHJKMN")
	buriedAcct := seedWellnessAccount(t, ctx, conn, "BBWELLHQLDBURYDACCTH", buried)
	seedTombstonedArrears(t, ctx, conn, buriedAcct, remindedArrears())
	if _, got, why := bookingEntryAs(t, ctx, conn, cp, cons, "CreateBooking", "wdholdburiedbook0001",
		morning, buried, wcStaffKey, "", "2026-07-07T12:00:00Z"); got != processor.OutcomeAccepted {
		t.Fatalf("staff CreateBooking over a tombstoned .arrears = %v (%s), want Accepted", got, why)
	}

	// Reminded and still owing: the hold, on both writers of a new claim.
	held := seedIdentity(t, ctx, conn, "BBWELLHQLDHELDMBRHJK")
	heldAcct := seedWellnessAccount(t, ctx, conn, "BBWELLHQLDHELDACCTHJ", held)
	seedAspect(t, ctx, conn, heldAcct, "arrears", "wellnessAccountArrears", remindedArrears())
	bk, got, why := bookingEntryAs(t, ctx, conn, cp, cons, "CreateBooking", "wdholdheldbook000001",
		morning, held, wcStaffKey, "", "2026-07-07T12:00:00Z")
	holdRefused(t, ctx, conn, bk, got, why, "staff CreateBooking on a reminded account")
	wl, got, why := bookingEntryAs(t, ctx, conn, cp, cons, "JoinWaitlist", "wdholdheldwait000001",
		evening, held, wcStaffKey, "", "2026-07-07T12:00:00Z")
	holdRefused(t, ctx, conn, wl, got, why, "staff JoinWaitlist on a reminded account")
	if keyExists(t, ctx, conn, morning+".bkr"+"BBWELLHQLDHELDMBRHJK") || keyExists(t, ctx, conn, evening+".bkr"+"BBWELLHQLDHELDMBRHJK") {
		t.Fatal("a held entry must claim no double-book guard either")
	}

	// An .arrears document of the wrong class is a fault, never state to
	// decide a hold on — in either direction.
	wrong := seedIdentity(t, ctx, conn, "BBWELLHQLDWRNGCLSHJK")
	wrongAcct := seedWellnessAccount(t, ctx, conn, "BBWELLHQLDWRNGCLSACC", wrong)
	seedAspect(t, ctx, conn, wrongAcct, "arrears", "somethingElse", map[string]any{"evaluatedAt": "2026-07-06T03:00:00Z"})
	if _, got, why := bookingEntryAs(t, ctx, conn, cp, cons, "CreateBooking", "wdholdwrongclass0001",
		morning, wrong, wcStaffKey, "", "2026-07-07T12:00:00Z"); got != processor.OutcomeRejected || !strings.HasPrefix(why, "InvalidState:") {
		t.Fatalf("staff CreateBooking over a wrong-class .arrears = %v (%s), want an InvalidState rejection", got, why)
	}
	if _, got, why := bookingEntryAs(t, ctx, conn, cp, cons, "JoinWaitlist", "wdholdwrongclass0002",
		evening, wrong, wcStaffKey, "", "2026-07-07T12:00:00Z"); got != processor.OutcomeRejected || !strings.HasPrefix(why, "InvalidState:") {
		t.Fatalf("staff JoinWaitlist over a wrong-class .arrears = %v (%s), want an InvalidState rejection", got, why)
	}
}

// TestBookingEntry_RefusesCreditHold_SelfLeg proves the hold binds the
// member's own self-service entry exactly as it binds the desk's: the debt is
// the member's, and the target == payload.booker compare that exempts the
// self leg from workplace confinement exempts it from nothing here. The
// dueAt-only positive runs first so the refusal is the hold's and not the
// leg's.
func TestBookingEntry_RefusesCreditHold_SelfLeg(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "creditholdself")

	studio := createStudio(t, ctx, conn, cp, cons, "wdholdselfstudio0001", "Hold Room")
	morning, _ := createSession(t, ctx, conn, cp, cons, "wdholdselfsession001",
		studio, "Morning Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 20)
	evening, _ := createSession(t, ctx, conn, cp, cons, "wdholdselfsession002",
		studio, "Evening Flow", "2026-07-08T18:00:00Z", "2026-07-08T18:30:00Z", 20)
	seedIdentity(t, ctx, conn, domainConsumerID)
	acctKey := seedWellnessAccount(t, ctx, conn, "BBWELLHQLDSELFACCTHJ", domainConsumerKey)

	// Overdue, not yet reminded: the member books and waitlists.
	seedAspect(t, ctx, conn, acctKey, "arrears", "wellnessAccountArrears", map[string]any{
		"dueAt": "2026-06-20T12:00:00Z", "evaluatedAt": "2026-07-06T03:00:00Z",
	})
	if _, got, why := bookingEntryAs(t, ctx, conn, cp, cons, "CreateBooking", "wdholdselfbook000001",
		morning, domainConsumerKey, domainConsumerKey, domainConsumerKey, "2026-07-07T12:00:00Z"); got != processor.OutcomeAccepted {
		t.Fatalf("self-service CreateBooking on an overdue-but-unreminded account = %v (%s), want Accepted", got, why)
	}
	if _, got, why := bookingEntryAs(t, ctx, conn, cp, cons, "JoinWaitlist", "wdholdselfwait000001",
		evening, domainConsumerKey, domainConsumerKey, domainConsumerKey, "2026-07-07T12:00:00Z"); got != processor.OutcomeAccepted {
		t.Fatalf("self-service JoinWaitlist on an overdue-but-unreminded account = %v (%s), want Accepted", got, why)
	}

	// The reminder goes out; the same member now claims nothing new. Two
	// fresh sessions, so the refusal is not the double-book guard's.
	seedAspect(t, ctx, conn, acctKey, "arrears", "wellnessAccountArrears", remindedArrears())
	later, _ := createSession(t, ctx, conn, cp, cons, "wdholdselfsession003",
		studio, "Later Flow", "2026-07-09T09:00:00Z", "2026-07-09T09:30:00Z", 20)
	latest, _ := createSession(t, ctx, conn, cp, cons, "wdholdselfsession004",
		studio, "Latest Flow", "2026-07-09T18:00:00Z", "2026-07-09T18:30:00Z", 20)
	bk, got, why := bookingEntryAs(t, ctx, conn, cp, cons, "CreateBooking", "wdholdselfbook000002",
		later, domainConsumerKey, domainConsumerKey, domainConsumerKey, "2026-07-07T12:00:00Z")
	holdRefused(t, ctx, conn, bk, got, why, "self-service CreateBooking on a reminded account")
	wl, got, why := bookingEntryAs(t, ctx, conn, cp, cons, "JoinWaitlist", "wdholdselfwait000002",
		latest, domainConsumerKey, domainConsumerKey, domainConsumerKey, "2026-07-07T12:00:00Z")
	holdRefused(t, ctx, conn, wl, got, why, "self-service JoinWaitlist on a reminded account")
}

// TestBookingEntry_CreditHoldAnswersInOracleOrder pins where the hold sits in
// prepare_booking_common. AFTER the workplace confinement: a staffer at
// another building booking a held member is refused by the walk, never told
// about the debt. BEFORE the schedule read: a held member's entry on a class
// that has already begun is refused CreditHold, not SessionInPast — the hold
// answers before the capacity / start-time oracles a held member is not
// entitled to.
func TestBookingEntry_CreditHoldAnswersInOracleOrder(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "creditholdorder")
	wcSeedStaff(t, ctx, conn)

	studioB := createStudio(t, ctx, conn, cp, cons, "wdholdorderstudiob01", "Studio B")
	wfSeedStudioAt(t, ctx, conn, studioB, wcBuildingBKey, wcBuildingBID)
	sessionB, _ := createSession(t, ctx, conn, cp, cons, "wdholdordersessionb1",
		studioB, "Evening Flow", "2026-07-08T18:00:00Z", "2026-07-08T18:30:00Z", 20)
	held := seedIdentity(t, ctx, conn, "BBWELLHQLDQRDERMBRHJ")
	heldAcct := seedWellnessAccount(t, ctx, conn, "BBWELLHQLDQRDERACCTH", held)
	seedAspect(t, ctx, conn, heldAcct, "arrears", "wellnessAccountArrears", remindedArrears())

	// The staffer works at A; the class is at B; the member is held. The
	// walk answers, and says nothing about the hold.
	_, got, why := bookingEntryAs(t, ctx, conn, cp, cons, "CreateBooking", "wdholdorderelse00001",
		sessionB, held, wcStaffKey, "", "2026-07-07T12:00:00Z")
	if got != processor.OutcomeRejected || !strings.Contains(why, "does not worksAt") {
		t.Fatalf("staff at another building booking a held member = %v (%s), want the workplace refusal", got, why)
	}
	if strings.Contains(why, "CreditHold") {
		t.Fatalf("the workplace refusal leaked the hold: %q", why)
	}

	// The operator (unconfined) on a class that has already begun: the hold
	// answers before the past-time guard reads the schedule.
	studioA := createStudio(t, ctx, conn, cp, cons, "wdholdorderstudioa01", "Studio A")
	wfSeedStudioAt(t, ctx, conn, studioA, wcBuildingAKey, wcBuildingAID)
	past, _ := createSession(t, ctx, conn, cp, cons, "wdholdordersessionp1",
		studioA, "Dawn Flow", "2026-07-07T06:00:00Z", "2026-07-07T06:30:00Z", 20)
	_, got, why = bookingEntryAs(t, ctx, conn, cp, cons, "CreateBooking", "wdholdorderpast00001",
		past, held, domainActorKey, "", "2026-07-07T12:00:00Z")
	if got != processor.OutcomeRejected || !strings.HasPrefix(why, "CreditHold:") {
		t.Fatalf("a held member on a past class = %v (%s), want CreditHold ahead of SessionInPast", got, why)
	}
	_, got, why = bookingEntryAs(t, ctx, conn, cp, cons, "JoinWaitlist", "wdholdorderpast00002",
		past, held, domainActorKey, "", "2026-07-07T12:00:00Z")
	if got != processor.OutcomeRejected || !strings.HasPrefix(why, "CreditHold:") {
		t.Fatalf("a held member waitlisting a past class = %v (%s), want CreditHold ahead of SessionInPast", got, why)
	}
}
