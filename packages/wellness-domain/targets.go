package wellnessdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's two meta.weaverTarget playbooks
// (Contract #10 §10.8): missing_release → directOp(ReleaseOrphanedBooking)
// over a booking whose class was called off, and missing_promotion →
// directOp(PromoteWaitlistedBookings) over a class holding both a free seat
// and a live waitlist. Both mirror clinic-ledger/targets.go's shape but stay
// self-contained inside wellness-domain — it already owns both the booking
// DDL and the session DDL, so no cross-package dependency is needed.
//
// Class pins the "booking" DDL: ReleaseOrphanedBooking is unique to this
// package today, but an unpinned directOp fails closed (MissingClass)
// forever the moment any OTHER installed package ever claims the same
// operationType, so it is pinned regardless (the same defensive shape
// clinic-ledger/cafe-domain already use for their own unambiguous ops).
//
// Reads declares what ReleaseOrphanedBooking's Starlark requires (ddls.go):
// the booking vertex itself (row.bookingKey — vertex_alive's UnknownBooking
// check) and its own .status aspect (row.bookingKey.status, the
// row.<column>.<literalSuffix> template — strategist.go's ONE templating
// relaxation beyond an exact row.<column> lookup). Both resolve for every
// violating row: the lens is anchored on the booking, so the column is never
// null.
//
// The two SESSION keys are OptionalReads, because the column they template off
// is nullable: `sessionKey` is the anchor the booking's .status aspect carries
// when CreateBooking stamped one, and the lens projects it null for a booking
// that has only its forSession link (lenses.go). A null column drops that entry
// from the dispatch rather than failing the gap (strategist.go), and the script
// then enumerates the link to find the session instead — so declaring these
// buys the hydration snapshot when the anchor is there without starving the
// gap for the rows that need it most. When present they serve the script's own
// SessionStillLive re-check (row.sessionKey, a plain full-key column) and the
// session's .schedule aspect (row.sessionKey.schedule), off which the script
// computes the booker's bookerSlotClaim cells to release — TombstoneSession
// never cascades onto the schedule aspect (package.go's "no cascade"
// doctrine), so it is still readable even though the session vertex itself
// is dead. Params carries only bookingKey — ReleaseOrphanedBooking takes no
// session param, unlike CancelBooking, so there is nothing else to template.
//
// Enumerations put every walk rooted at the BOOKING on the envelope, each
// hub-templated off the same row column the op's payload carries, so the walks
// the script runs are declared by their dispatcher rather than discovered at
// runtime: forSession and bookedBy, which locate the session and the booker
// when the booking's .status aspect names neither; and settles /
// settlesClassPrice inbound, the already-posted-charge lookups the script runs
// to mint a wellnessrefund marker on a studio-initiated release (a no-show fee
// for a booking already marked noShow, a class-price charge for any other).
//
// The refund marker's own follow-up walk — the charge transaction's postedTo,
// to find the account to credit — is NOT declarable here: its hub is a
// transaction key only the settles walk above resolves, so it is a class-(e)
// follow-up off a link-discovered hub, the one shape no dispatcher can name.
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{
		{
			TargetID: OrphanedBookingSettlementTarget,
			Description: "No booking or waitlist spot outlives the class it was made for. When a class is called " +
				"off, the seats and waitlist slots it still holds are released and those bookings closed " +
				"out.",
			LensRef: OrphanedBookingSettlementTarget,
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_release": {
					Action:        "directOp",
					Operation:     "ReleaseOrphanedBooking",
					Class:         "booking",
					Params:        map[string]string{"bookingKey": "row.bookingKey"},
					Reads:         []string{"row.bookingKey", "row.bookingKey.status"},
					OptionalReads: []string{"row.sessionKey", "row.sessionKey.schedule"},
					Enumerations: []pkgmgr.EnumerationSpec{
						{Hub: "row.bookingKey", Relation: "forSession", Direction: "out"},
						{Hub: "row.bookingKey", Relation: "bookedBy", Direction: "out"},
						{Hub: "row.bookingKey", Relation: "settles", Direction: "in"},
						{Hub: "row.bookingKey", Relation: "settlesClassPrice", Direction: "in"},
					},
				},
			},
		},
		waitlistPromotionTarget(),
	}
}

// waitlistPromotionTarget returns the §10.8 playbook for the waitlist-promotion
// convergence: the single missing_promotion gap →
// directOp(PromoteWaitlistedBookings) over the SESSION, which is where the
// condition lives — "this class has a free seat and someone still waiting" is
// a fact about the class, not about any one booking (lenses.go).
//
// Class pins the "booking" DDL, which is the DDL that admits the op (its
// PermittedCommands list, ddls.go) even though the row is anchored on a
// session: an unpinned directOp fails closed (MissingClass) forever the moment
// any other installed package claims the same operationType, so it is pinned
// regardless, the same defensive shape missing_release already uses.
//
// Params carries only `session` — the op takes no candidate, because it is the
// op's job to find EVERY promotable one. That is a dispatch-economics decision,
// not a convenience: a mark holds the gap for the lease (30m default) and a
// still-open gap does not re-dispatch until reclaim, so a per-booking op would
// seat one member per lease window on a class that just gained four seats.
//
// Reads declares both keys the script hard-requires: the session vertex itself
// (vertex_alive's UnknownSession check) and its .schedule aspect (the capacity
// and the SessionInPast guard), via the row.<column>.<literalSuffix> template.
// Neither is ever null on a violating row — the lens is anchored on the
// session, and a row with no readable .schedule.capacity cannot violate.
//
// Enumerations puts the ONE walk the script runs on the envelope: the
// session's forSession-in bookings, the candidate set collect_waitlist_candidates
// pages over (ddls.go), hub-templated off the same row column the payload
// carries. Its per-candidate .status follow-up reads are class-(e) reads off
// that enumeration — data-derived keys no dispatcher can name. The seat-cell
// reads claim_free_seats runs are neither: their keys are derived from the
// session's own capacity, not from the row, so they stay bounded lazy live
// reads inside the ≤MAX_SESSION_CAPACITY loop (the op's doc comment states the
// bound).
func waitlistPromotionTarget() pkgmgr.WeaverTargetSpec {
	return pkgmgr.WeaverTargetSpec{
		TargetID: WaitlistPromotionTarget,
		Description: "No member waits for a seat the class already has. When a class has room — a booking " +
			"cancelled, or its capacity raised — the people on its waitlist are given those seats, " +
			"earliest on the list first, until the room runs out.",
		LensRef: WaitlistPromotionTarget,
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_promotion": {
				Action:    "directOp",
				Operation: "PromoteWaitlistedBookings",
				Class:     "booking",
				Params:    map[string]string{"session": "row.sessionKey"},
				Reads:     []string{"row.sessionKey", "row.sessionKey.schedule"},
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "row.sessionKey", Relation: "forSession", Direction: "in"},
				},
			},
		},
	}
}
