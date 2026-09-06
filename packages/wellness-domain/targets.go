package wellnessdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract
// #10 §10.8): a single missing_release → directOp(ReleaseOrphanedBooking)
// gap, mirroring clinic-ledger/targets.go's shape but self-contained inside
// wellness-domain — it already owns both the booking DDL and the session
// DDL, so no cross-package dependency is needed.
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
	}
}
