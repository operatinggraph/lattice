// wellness-domain integration test proving CancelBooking mints a
// wellnessrefund marker when the booking being cancelled already carries a
// posted settlesClassPrice charge. wellness-ledger is not installed in this
// harness (wellness-domain has no dependency on it), so the charge
// transaction + its postedTo/settlesClassPrice links are seeded directly —
// the exact shape WellnessDebitAccount{priceBookingRef} would have left,
// mirroring the raw-seed idiom seedVertex/seedLink already use in
// integration_test.go for cross-package known-key reads.
package wellnessdomain_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	wellnessdomain "github.com/operatinggraph/lattice/packages/wellness-domain"
)

// seedAspect directly seeds an aspect document — the mirror of seedVertex/
// seedLink for the one shape neither covers.
func seedAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, vertexKey, localName, class string, data map[string]any) {
	t.Helper()
	key := vertexKey + "." + localName
	doc := map[string]any{
		"class": class, "isDeleted": false,
		"vertexKey": vertexKey, "localName": localName, "data": data,
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed aspect %s: %v", key, err)
	}
}

// seedPostedClassPriceCharge seeds a class-price charge against bookingKey
// exactly as WellnessDebitAccount{priceBookingRef} would have left it — a
// wellnesstransaction + .entry aspect + postedTo (→ a seeded wellnessaccount)
// + settlesClassPrice (→ the booking). wellness-ledger is not installed in
// this harness (wellness-domain has no dependency on it), so the shape is
// seeded raw. Returns the account and transaction keys.
func seedPostedClassPriceCharge(t *testing.T, ctx context.Context, conn *substrate.Conn, bookingKey, acctID, txID string, amountCents float64) (string, string) {
	t.Helper()
	_, bookID, _ := substrate.ParseVertexKey(bookingKey)

	acctKey := "vtx.wellnessaccount." + acctID
	seedVertex(t, ctx, conn, acctKey, "wellnessaccount", nil)

	txKey := "vtx.wellnesstransaction." + txID
	seedVertex(t, ctx, conn, txKey, "wellnesstransaction", nil)
	seedAspect(t, ctx, conn, txKey, "entry", "transactionEntry", map[string]any{
		"type": "debit", "amountCents": amountCents, "postedAt": "2026-07-08T08:00:00Z",
	})
	seedLink(t, ctx, conn, "lnk.wellnesstransaction."+txID+".postedTo.wellnessaccount."+acctID,
		txKey, acctKey, "postedTo", "postedTo")
	seedLink(t, ctx, conn, "lnk.wellnesstransaction."+txID+".settlesClassPrice.booking."+bookID,
		txKey, bookingKey, "settlesClassPrice", "settlesClassPrice")
	return acctKey, txKey
}

// seedPostedNoShowFeeCharge is settlesClassPrice's no-show-fee sibling —
// exactly as WellnessDebitAccount{bookingRef} would have left it (relation
// "settles", not "settlesClassPrice"). Posts to an EXISTING account (a
// booker holds one account; both a class-price and a no-show-fee charge post
// to it) rather than minting a fresh one.
func seedPostedNoShowFeeCharge(t *testing.T, ctx context.Context, conn *substrate.Conn, bookingKey, acctKey, txID string, amountCents float64) string {
	t.Helper()
	_, bookID, _ := substrate.ParseVertexKey(bookingKey)
	_, acctID, _ := substrate.ParseVertexKey(acctKey)

	txKey := "vtx.wellnesstransaction." + txID
	seedVertex(t, ctx, conn, txKey, "wellnesstransaction", nil)
	seedAspect(t, ctx, conn, txKey, "entry", "transactionEntry", map[string]any{
		"type": "debit", "amountCents": amountCents, "postedAt": "2026-07-08T09:31:00Z",
	})
	seedLink(t, ctx, conn, "lnk.wellnesstransaction."+txID+".postedTo.wellnessaccount."+acctID,
		txKey, acctKey, "postedTo", "postedTo")
	seedLink(t, ctx, conn, "lnk.wellnesstransaction."+txID+".settles.booking."+bookID,
		txKey, bookingKey, "settles", "settles")
	return txKey
}

// trackerEventClasses returns the op tracker's eventClasses list for reqID —
// how an emitted event is observed from a package test, since the commit
// enriches the tracker with one entry per event in the same atomic batch
// (identity-hygiene's assertTrackerEvent precedent).
func trackerEventClasses(t *testing.T, ctx context.Context, conn *substrate.Conn, reqID string) []string {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.TrackerKey(reqID))
	if err != nil {
		t.Fatalf("tracker not found for %s: %v", reqID, err)
	}
	tr, err := processor.ParseTracker(entry.Value)
	if err != nil {
		t.Fatalf("ParseTracker: %v", err)
	}
	raw, _ := tr.Data["eventClasses"].([]interface{})
	out := make([]string, 0, len(raw))
	for _, ec := range raw {
		s, _ := ec.(string)
		out = append(out, s)
	}
	return out
}

func assertTrackerEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, reqID, eventClass string) {
	t.Helper()
	classes := trackerEventClasses(t, ctx, conn, reqID)
	for _, ec := range classes {
		if ec == eventClass {
			return
		}
	}
	t.Fatalf("%s not in tracker eventClasses: %v", eventClass, classes)
}

func assertNoTrackerEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, reqID, eventClass string) {
	t.Helper()
	classes := trackerEventClasses(t, ctx, conn, reqID)
	for _, ec := range classes {
		if ec == eventClass {
			t.Fatalf("%s must not be in tracker eventClasses: %v", eventClass, classes)
		}
	}
}

// submitCancelBooking dispatches CancelBooking with the same declared reads
// TestCancelBooking_ReleasesSeatForNextClaimant uses, and asserts Accepted.
// It submits a full day ahead of every session these tests create, which is
// well outside the late-cancellation window — submitCancelBookingAt is the
// variant that aims at the window deliberately.
func submitCancelBooking(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, reqID, bookingKey, sessionKey string) {
	t.Helper()
	submitCancelBookingAt(t, ctx, conn, cp, cons, reqID, bookingKey, sessionKey, "2026-07-07T12:10:00Z")
}

// submitCancelBookingAt is submitCancelBooking with the submission instant
// under the caller's control — what the late-cancellation-window tests need,
// since the window is measured from the session's own startsAt.
func submitCancelBookingAt(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, reqID, bookingKey, sessionKey, submittedAt string) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CancelBooking",
		Actor:         domainActorKey,
		SubmittedAt:   submittedAt,
		Class:         "booking",
		Payload:       json.RawMessage(`{"bookingKey":"` + bookingKey + `","session":"` + sessionKey + `"}`),
		ContextHint: &processor.ContextHint{Enumerations: testutil.DeclaredEnumerations("CancelBooking", domainActorKey, wellnessdomain.OpMetas()), Reads: []string{
			bookingKey, bookingKey + ".status", sessionKey + ".schedule",
			forSessionLnkKey(t, bookingKey, sessionKey),
		}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

// TestCancelBooking_MintsRefundMarkerWhenAlreadyCharged proves the core
// refund-marker mechanism: cancelling a booking whose class-price charge
// already posted mints a wellnessrefund marker (root {} + .detail aspect)
// carrying the exact accountKey/amountCents the original charge posted,
// plus a reverses link back to it — the marker wellness-ledger's
// wellnessRefundSettlement lens converges into a WellnessCreditAccount.
func TestCancelBooking_MintsRefundMarkerWhenAlreadyCharged(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "refundmarker")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdrefundstudio000001", "Priced Studio")
	sessionKey, outcome := createSessionPriced(t, ctx, conn, cp, cons, "wdrefundsession000001", studioKey, "Priced Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 5, 1500)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createSessionPriced outcome = %v, want Accepted", outcome)
	}

	bookerKey := seedIdentity(t, ctx, conn, "BBWELLREFUNDBKR1HJKM")
	bookingKey, outcome := createBooking(t, ctx, conn, cp, cons, "wdrefundbooking000001", sessionKey, bookerKey, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createBooking outcome = %v, want Accepted", outcome)
	}

	acctKey, txKey := seedPostedClassPriceCharge(t, ctx, conn, bookingKey,
		"BBWELLREFUNDACT1HJKM", "BBWELLREFUNDTXN1HJKM", 1500.0)
	_, txID, _ := substrate.ParseVertexKey(txKey)

	cancelReqID := testutil.GenReqID("wdrefundcancel000001")
	submitCancelBooking(t, ctx, conn, cp, cons, cancelReqID, bookingKey, sessionKey)

	refundKey := "vtx.wellnessrefund." + nanoIDFromRequestID(cancelReqID)
	if !keyExists(t, ctx, conn, refundKey) {
		t.Fatalf("wellnessrefund marker must exist: %s", refundKey)
	}
	detail := readDoc(t, ctx, conn, refundKey+".detail")
	data, _ := detail["data"].(map[string]any)
	if data["accountKey"] != acctKey {
		t.Fatalf("refund detail accountKey = %v, want %v", data["accountKey"], acctKey)
	}
	if data["amountCents"] != 1500.0 {
		t.Fatalf("refund detail amountCents = %v, want 1500", data["amountCents"])
	}
	if data["bookingKey"] != bookingKey {
		t.Fatalf("refund detail bookingKey = %v, want %v", data["bookingKey"], bookingKey)
	}
	if data["className"] != "Priced Flow" {
		t.Fatalf("refund detail className = %v, want Priced Flow (snapshotted off the cancelled booking's own .status)", data["className"])
	}
	if data["classStartsAt"] != "2026-07-08T09:00:00Z" {
		t.Fatalf("refund detail classStartsAt = %v, want 2026-07-08T09:00:00Z", data["classStartsAt"])
	}

	_, refundID, _ := substrate.ParseVertexKey(refundKey)
	reversesLnk := "lnk.wellnessrefund." + refundID + ".reverses.wellnesstransaction." + txID
	if !keyExists(t, ctx, conn, reversesLnk) {
		t.Fatalf("reverses link must exist: %s", reversesLnk)
	}
	assertTrackerEvent(t, ctx, conn, cancelReqID, "wellness.classPriceRefundQueued")
	assertNoTrackerEvent(t, ctx, conn, cancelReqID, "wellness.lateCancelForfeited")
}

// TestCancelBooking_NoRefundMarkerWhenNeverCharged proves the negative case:
// cancelling a priced booking that was NEVER charged mints no wellnessrefund
// marker at all — the tombstoned-booking self-resolution the design
// relies on (wellnessClassPriceSettlement's own MATCH stops matching a
// cancelled booking, so no charge — and therefore no refund — ever happens).
func TestCancelBooking_NoRefundMarkerWhenNeverCharged(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "refundmarkernone")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdnorefundstudio00001", "Priced Studio Two")
	sessionKey, outcome := createSessionPriced(t, ctx, conn, cp, cons, "wdnorefundsession0001", studioKey, "Priced Flow Two", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 5, 1500)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createSessionPriced outcome = %v, want Accepted", outcome)
	}

	bookerKey := seedIdentity(t, ctx, conn, "BBWELLNXREFUNDBKRHJK")
	bookingKey, outcome := createBooking(t, ctx, conn, cp, cons, "wdnorefundbooking0001", sessionKey, bookerKey, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createBooking outcome = %v, want Accepted", outcome)
	}

	cancelReqID := testutil.GenReqID("wdnorefundcancel00001")
	submitCancelBooking(t, ctx, conn, cp, cons, cancelReqID, bookingKey, sessionKey)

	refundKey := "vtx.wellnessrefund." + nanoIDFromRequestID(cancelReqID)
	if keyExists(t, ctx, conn, refundKey) {
		t.Fatalf("no wellnessrefund marker should exist for a never-charged booking: %s", refundKey)
	}
}

// TestCancelBooking_LateCancelForfeitsClassPrice is the late-cancellation
// window's own proof, at the case that motivated it: a member cancelling on
// the way out the door, minutes before the class. The cancellation still
// succeeds and still frees the seat — but no wellnessrefund marker mints, so
// the class-price charge stands unreversed exactly as a no-show's does, and
// wellness.lateCancelForfeited is emitted in place of the refund event.
func TestCancelBooking_LateCancelForfeitsClassPrice(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "latecancelforfeit")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdlatestudio000001", "Late Studio")
	sessionKey, outcome := createSessionPriced(t, ctx, conn, cp, cons, "wdlatesession000001", studioKey, "Late Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 5, 1500)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createSessionPriced outcome = %v, want Accepted", outcome)
	}

	bookerKey := seedIdentity(t, ctx, conn, "BBWELLLATECANCLBKR1H")
	bookingKey, outcome := createBooking(t, ctx, conn, cp, cons, "wdlatebooking000001", sessionKey, bookerKey, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createBooking outcome = %v, want Accepted", outcome)
	}

	_, txKey := seedPostedClassPriceCharge(t, ctx, conn, bookingKey,
		"BBWELLLATEACCT1HJKMN", "BBWELLLATETXN1HJKMNP", 1500.0)

	// One minute before the 09:00 start — the row's own example.
	cancelReqID := testutil.GenReqID("wdlatecancel000001")
	submitCancelBookingAt(t, ctx, conn, cp, cons, cancelReqID, bookingKey, sessionKey, "2026-07-08T08:59:00Z")

	if keyExists(t, ctx, conn, bookingKey) {
		t.Fatalf("a late cancellation still cancels — the booking must be tombstoned")
	}
	if keyExists(t, ctx, conn, sessionKey+".seat1") {
		t.Fatalf("a late cancellation still frees the seat — seat1 must be released")
	}
	refundKey := "vtx.wellnessrefund." + nanoIDFromRequestID(cancelReqID)
	if keyExists(t, ctx, conn, refundKey) {
		t.Fatalf("no wellnessrefund marker may mint for a late cancellation: %s", refundKey)
	}
	if keyExists(t, ctx, conn, refundKey+".detail") {
		t.Fatalf("no wellnessrefund detail aspect may be written for a late cancellation: %s", refundKey+".detail")
	}
	if !keyExists(t, ctx, conn, txKey) {
		t.Fatalf("the forfeited class-price charge must stand: %s", txKey)
	}
	assertTrackerEvent(t, ctx, conn, cancelReqID, "wellness.lateCancelForfeited")
	assertNoTrackerEvent(t, ctx, conn, cancelReqID, "wellness.classPriceRefundQueued")
}

// TestCancelBooking_RefundWindowBoundary pins both sides of the two-hour
// cutoff to the minute. Exactly on the mark forfeits — the same
// at-the-boundary-the-stricter-rule-wins inequality as the SessionStarted
// guard — while one minute earlier still refunds in full.
func TestCancelBooking_RefundWindowBoundary(t *testing.T) {
	cases := []struct {
		name        string
		suffix      string
		bookerID    string
		acctID      string
		txID        string
		submittedAt string
		wantRefund  bool
	}{
		{
			name: "exactly on the two-hour mark forfeits", suffix: "A",
			bookerID: "BBWELLBNDACANCLBKRHJ", acctID: "BBWELLBNDAACCTHJKMNP", txID: "BBWELLBNDATXNHJKMNPQ",
			submittedAt: "2026-07-08T07:00:00Z", wantRefund: false,
		},
		{
			name: "one minute outside the window still refunds", suffix: "B",
			bookerID: "BBWELLBNDBCANCLBKRHJ", acctID: "BBWELLBNDBACCTHJKMNP", txID: "BBWELLBNDBTXNHJKMNPQ",
			submittedAt: "2026-07-08T06:59:00Z", wantRefund: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, conn := setupDomainEnv(t)
			cp, cons := newDomainPipeline(t, ctx, conn, "refundboundary"+tc.suffix)

			studioKey := createStudio(t, ctx, conn, cp, cons, "wdbndstudio"+tc.suffix, "Boundary Studio")
			sessionKey, outcome := createSessionPriced(t, ctx, conn, cp, cons, "wdbndsession"+tc.suffix, studioKey, "Boundary Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 5, 1500)
			if outcome != processor.OutcomeAccepted {
				t.Fatalf("createSessionPriced outcome = %v, want Accepted", outcome)
			}

			bookerKey := seedIdentity(t, ctx, conn, tc.bookerID)
			bookingKey, outcome := createBooking(t, ctx, conn, cp, cons, "wdbndbooking"+tc.suffix, sessionKey, bookerKey, "")
			if outcome != processor.OutcomeAccepted {
				t.Fatalf("createBooking outcome = %v, want Accepted", outcome)
			}

			seedPostedClassPriceCharge(t, ctx, conn, bookingKey, tc.acctID, tc.txID, 1500.0)

			cancelReqID := testutil.GenReqID("wdbndcancel" + tc.suffix)
			submitCancelBookingAt(t, ctx, conn, cp, cons, cancelReqID, bookingKey, sessionKey, tc.submittedAt)

			refundKey := "vtx.wellnessrefund." + nanoIDFromRequestID(cancelReqID)
			if got := keyExists(t, ctx, conn, refundKey); got != tc.wantRefund {
				t.Fatalf("wellnessrefund marker exists = %v, want %v (submitted %s against a 09:00 start)", got, tc.wantRefund, tc.submittedAt)
			}
			if tc.wantRefund {
				assertTrackerEvent(t, ctx, conn, cancelReqID, "wellness.classPriceRefundQueued")
				assertNoTrackerEvent(t, ctx, conn, cancelReqID, "wellness.lateCancelForfeited")
			} else {
				assertTrackerEvent(t, ctx, conn, cancelReqID, "wellness.lateCancelForfeited")
				assertNoTrackerEvent(t, ctx, conn, cancelReqID, "wellness.classPriceRefundQueued")
			}
		})
	}
}

// TestCancelBooking_LateWaitlistCancelUnaffected proves the window reaches
// only what a booking could actually forfeit. A waitlisted booking never
// carried a class-price charge (wellness-ledger's classPriceSettlementSpec
// posts only once status is 'booked'), so leaving the waitlist minutes before
// the class releases its slot exactly as it always did — nothing forfeited,
// no forfeiture event.
func TestCancelBooking_LateWaitlistCancelUnaffected(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "latewaitlistleave")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdlatewlstudio00001", "Late Waitlist Studio")
	sessionKey, outcome := createSessionPriced(t, ctx, conn, cp, cons, "wdlatewlsession0001", studioKey, "Late Waitlist Flow", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 1, 1500)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createSessionPriced outcome = %v, want Accepted", outcome)
	}

	seatedKey := seedIdentity(t, ctx, conn, "BBWELLLATEWLSEAT1HJK")
	if _, outcome := createBooking(t, ctx, conn, cp, cons, "wdlatewlbooking0001", sessionKey, seatedKey, ""); outcome != processor.OutcomeAccepted {
		t.Fatalf("createBooking outcome = %v, want Accepted", outcome)
	}

	waitlistedKey := seedIdentity(t, ctx, conn, "BBWELLLATEWLBKR1HJKM")
	waitlistBookingKey, outcome := joinWaitlist(t, ctx, conn, cp, cons, "wdlatewljoin000001", sessionKey, waitlistedKey, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("joinWaitlist outcome = %v, want Accepted", outcome)
	}

	cancelReqID := testutil.GenReqID("wdlatewlcancel00001")
	submitCancelBookingAt(t, ctx, conn, cp, cons, cancelReqID, waitlistBookingKey, sessionKey, "2026-07-08T08:59:00Z")

	if keyExists(t, ctx, conn, waitlistBookingKey) {
		t.Fatalf("the waitlisted booking must be tombstoned after leaving the waitlist")
	}
	if keyExists(t, ctx, conn, sessionKey+".wl1") {
		t.Fatalf("wl1 must be released when the waitlisted booker leaves, late window or not")
	}
	if !keyExists(t, ctx, conn, sessionKey+".seat1") {
		t.Fatalf("seat1 (the seated booker's) must be untouched — a waitlisted cancel promotes nobody")
	}
	refundKey := "vtx.wellnessrefund." + nanoIDFromRequestID(cancelReqID)
	if keyExists(t, ctx, conn, refundKey) {
		t.Fatalf("a waitlisted booking has no charge, so no refund marker either: %s", refundKey)
	}
	assertNoTrackerEvent(t, ctx, conn, cancelReqID, "wellness.lateCancelForfeited")
	assertTrackerEvent(t, ctx, conn, cancelReqID, "wellness.waitlistLeft")
}

// TestReleaseOrphanedBooking_MintsRefundMarkerWhenAlreadyCharged proves a
// studio-initiated cancellation (TombstoneSession, then Weaver's
// ReleaseOrphanedBooking drain) refunds an already-posted class-price charge
// exactly as CancelBooking's own refund does — but unconditionally: unlike
// CancelBooking there is no late-cancellation forfeiture branch, because the
// booker did nothing to cause this cancellation. The session here is
// tombstoned one minute before its own start — squarely inside what would be
// CancelBooking's forfeiture window — to prove the window does not apply.
func TestReleaseOrphanedBooking_MintsRefundMarkerWhenAlreadyCharged(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "orphanrefund")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdorphanrfstudio0001", "Priced Flow Room")
	sessionKey, outcome := createSessionPriced(t, ctx, conn, cp, cons, "wdorphanrfsessio0001", studioKey, "Priced Vinyasa", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 5, 1500)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createSessionPriced outcome = %v, want Accepted", outcome)
	}

	bookerKey := seedIdentity(t, ctx, conn, "BBWELLURPHRFBKR1HJKM")
	bookingKey, outcome := createBooking(t, ctx, conn, cp, cons, "wdorphanrfbookin0001", sessionKey, bookerKey, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createBooking outcome = %v, want Accepted", outcome)
	}

	acctKey, txKey := seedPostedClassPriceCharge(t, ctx, conn, bookingKey,
		"BBWELLURPHRFACT1HJKM", "BBWELLURPHRFTXN1HJKM", 1500.0)
	_, txID, _ := substrate.ParseVertexKey(txKey)

	// One minute before the 09:00 start — squarely inside CancelBooking's
	// two-hour forfeiture window, to prove ReleaseOrphanedBooking ignores it.
	tombstoneReqID := testutil.GenReqID("wdorphanrftombst0001")
	tombstoneEnv := &processor.OperationEnvelope{
		RequestID:     tombstoneReqID,
		Lane:          processor.LaneDefault,
		OperationType: "TombstoneSession",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-08T08:59:00Z",
		Class:         "session",
		Payload:       json.RawMessage(`{"sessionKey":"` + sessionKey + `","studio":"` + studioKey + `"}`),
		ContextHint: &processor.ContextHint{Enumerations: testutil.DeclaredEnumerations("TombstoneSession", domainActorKey, wellnessdomain.OpMetas()), Reads: []string{
			sessionKey, sessionKey + ".schedule",
			atStudioLnkKey(t, sessionKey, studioKey),
		}},
	}
	testutil.PublishOp(t, conn, tombstoneEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	releaseReqID := testutil.GenReqID("wdorphanrfreleas0001")
	releaseEnv := &processor.OperationEnvelope{
		RequestID:     releaseReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ReleaseOrphanedBooking",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-08T09:05:00Z",
		Class:         "booking",
		Payload:       json.RawMessage(`{"bookingKey":"` + bookingKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{
			bookingKey, bookingKey + ".status", sessionKey,
		}},
	}
	testutil.PublishOp(t, conn, releaseEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if keyExists(t, ctx, conn, bookingKey) {
		t.Fatalf("booking must be tombstoned after ReleaseOrphanedBooking")
	}

	refundKey := "vtx.wellnessrefund." + nanoIDFromRequestID(releaseReqID)
	if !keyExists(t, ctx, conn, refundKey) {
		t.Fatalf("wellnessrefund marker must exist: %s", refundKey)
	}
	detail := readDoc(t, ctx, conn, refundKey+".detail")
	data, _ := detail["data"].(map[string]any)
	if data["accountKey"] != acctKey {
		t.Fatalf("refund detail accountKey = %v, want %v", data["accountKey"], acctKey)
	}
	if data["amountCents"] != 1500.0 {
		t.Fatalf("refund detail amountCents = %v, want 1500", data["amountCents"])
	}
	if data["bookingKey"] != bookingKey {
		t.Fatalf("refund detail bookingKey = %v, want %v", data["bookingKey"], bookingKey)
	}
	if data["className"] != "Priced Vinyasa" {
		t.Fatalf("refund detail className = %v, want Priced Vinyasa", data["className"])
	}

	_, refundID, _ := substrate.ParseVertexKey(refundKey)
	reversesLnk := "lnk.wellnessrefund." + refundID + ".reverses.wellnesstransaction." + txID
	if !keyExists(t, ctx, conn, reversesLnk) {
		t.Fatalf("reverses link must exist: %s", reversesLnk)
	}
	assertTrackerEvent(t, ctx, conn, releaseReqID, "wellness.classPriceRefundQueued")
	assertNoTrackerEvent(t, ctx, conn, releaseReqID, "wellness.lateCancelForfeited")
}

// TestReleaseOrphanedBooking_ReleasesNoShowAndRefundsBothChargeShapes proves
// the verticals.md gap fix (2026-09-02): a booking the auto-no-show sweep
// already flipped to noShow before the studio called off its class is no
// longer permanently stranded — ReleaseOrphanedBooking now accepts noShow
// (previously InvalidState), releases its seat cell (not a waitlist slot —
// noShow only ever mints from booked), and reverses BOTH an already-posted
// class-price charge (settlesClassPrice — classPriceSettlementSpec bills
// unconditional on attendance, so a no-show can carry one) and an
// already-posted no-show fee (settles) in the SAME release, each its own
// wellnessrefund marker with a memo naming which charge it reverses.
func TestReleaseOrphanedBooking_ReleasesNoShowAndRefundsBothChargeShapes(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "orphannoshow")

	studioKey := createStudio(t, ctx, conn, cp, cons, "wdorphannsstudio0001", "Priced Flow Room")
	sessionKey, outcome := createSessionPriced(t, ctx, conn, cp, cons, "wdorphannssessio0001", studioKey, "Priced Vinyasa", "2026-07-08T09:00:00Z", "2026-07-08T09:30:00Z", 5, 1500)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createSessionPriced outcome = %v, want Accepted", outcome)
	}

	bookerKey := seedIdentity(t, ctx, conn, "BBWELLURPHNSBKR1HJKM")
	bookingKey, outcome := createBooking(t, ctx, conn, cp, cons, "wdorphannsbookin0001", sessionKey, bookerKey, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("createBooking outcome = %v, want Accepted", outcome)
	}
	seat, _ := attendanceStatus(t, ctx, conn, bookingKey)["seat"].(float64)

	// Operator marks the no-show 5 minutes after the 09:00 start — same
	// timing TestSetBookingAttendance's own noShow cases use.
	testutil.PublishOp(t, conn, attendanceEnv(t, "wdorphannsattend0001", bookingKey, sessionKey, "noShow", "", domainActorKey, "2026-07-08T09:05:00Z"))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got, _ := attendanceStatus(t, ctx, conn, bookingKey)["value"].(string); got != "noShow" {
		t.Fatalf("booking status = %q, want noShow", got)
	}

	acctKey, priceTxKey := seedPostedClassPriceCharge(t, ctx, conn, bookingKey,
		"BBWELLURPHNSACT1HJKM", "BBWELLURPHNSPTX1HJKM", 1500.0)
	_, priceTxID, _ := substrate.ParseVertexKey(priceTxKey)
	noShowTxKey := seedPostedNoShowFeeCharge(t, ctx, conn, bookingKey, acctKey, "BBWELLURPHNSNTX1HJKM", 2500.0)
	_, noShowTxID, _ := substrate.ParseVertexKey(noShowTxKey)

	tombstoneReqID := testutil.GenReqID("wdorphannstombst0001")
	tombstoneEnv := &processor.OperationEnvelope{
		RequestID:     tombstoneReqID,
		Lane:          processor.LaneDefault,
		OperationType: "TombstoneSession",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-08T09:35:00Z",
		Class:         "session",
		Payload:       json.RawMessage(`{"sessionKey":"` + sessionKey + `","studio":"` + studioKey + `"}`),
		ContextHint: &processor.ContextHint{Enumerations: testutil.DeclaredEnumerations("TombstoneSession", domainActorKey, wellnessdomain.OpMetas()), Reads: []string{
			sessionKey, sessionKey + ".schedule",
			atStudioLnkKey(t, sessionKey, studioKey),
		}},
	}
	testutil.PublishOp(t, conn, tombstoneEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	releaseReqID := testutil.GenReqID("wdorphannsreleas0001")
	releaseEnv := &processor.OperationEnvelope{
		RequestID:     releaseReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ReleaseOrphanedBooking",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-08T09:40:00Z",
		Class:         "booking",
		Payload:       json.RawMessage(`{"bookingKey":"` + bookingKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{
			bookingKey, bookingKey + ".status", sessionKey,
		}},
	}
	testutil.PublishOp(t, conn, releaseEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if keyExists(t, ctx, conn, bookingKey) {
		t.Fatalf("booking must be tombstoned after ReleaseOrphanedBooking")
	}
	if seatCellKey := sessionKey + ".seat" + strconv.Itoa(int(seat)); keyExists(t, ctx, conn, seatCellKey) {
		t.Fatalf("seat cell must be released: %s", seatCellKey)
	}

	refundIDs := nanoIDsFromRequestID(releaseReqID, 2)
	priceRefundKey := "vtx.wellnessrefund." + refundIDs[0]
	noShowRefundKey := "vtx.wellnessrefund." + refundIDs[1]

	priceDetail := readDoc(t, ctx, conn, priceRefundKey+".detail")
	pdata, _ := priceDetail["data"].(map[string]any)
	if pdata["memo"] != "Class price refund" {
		t.Fatalf("class-price refund memo = %v, want %q", pdata["memo"], "Class price refund")
	}
	if pdata["amountCents"] != 1500.0 {
		t.Fatalf("class-price refund amountCents = %v, want 1500", pdata["amountCents"])
	}
	if pdata["accountKey"] != acctKey {
		t.Fatalf("class-price refund accountKey = %v, want %v", pdata["accountKey"], acctKey)
	}

	noShowDetail := readDoc(t, ctx, conn, noShowRefundKey+".detail")
	ndata, _ := noShowDetail["data"].(map[string]any)
	if ndata["memo"] != "No-show fee refund" {
		t.Fatalf("no-show-fee refund memo = %v, want %q", ndata["memo"], "No-show fee refund")
	}
	if ndata["amountCents"] != 2500.0 {
		t.Fatalf("no-show-fee refund amountCents = %v, want 2500", ndata["amountCents"])
	}
	if ndata["accountKey"] != acctKey {
		t.Fatalf("no-show-fee refund accountKey = %v, want %v", ndata["accountKey"], acctKey)
	}

	_, priceRefundID, _ := substrate.ParseVertexKey(priceRefundKey)
	if !keyExists(t, ctx, conn, "lnk.wellnessrefund."+priceRefundID+".reverses.wellnesstransaction."+priceTxID) {
		t.Fatalf("class-price reverses link must exist")
	}
	_, noShowRefundID, _ := substrate.ParseVertexKey(noShowRefundKey)
	if !keyExists(t, ctx, conn, "lnk.wellnessrefund."+noShowRefundID+".reverses.wellnesstransaction."+noShowTxID) {
		t.Fatalf("no-show-fee reverses link must exist")
	}

	assertTrackerEvent(t, ctx, conn, releaseReqID, "wellness.classPriceRefundQueued")
	assertTrackerEvent(t, ctx, conn, releaseReqID, "wellness.noShowFeeRefundQueued")
}
