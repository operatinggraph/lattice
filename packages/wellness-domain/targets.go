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
// Reads declares exactly what ReleaseOrphanedBooking's Starlark reads
// (ddls.go): the booking vertex itself (row.bookingKey — vertex_alive's
// UnknownBooking check), its own .status aspect (row.bookingKey.status, the
// row.<column>.<literalSuffix> template — strategist.go's ONE templating
// relaxation beyond an exact row.<column> lookup), the session vertex itself
// (row.sessionKey, a plain full-key column already on the row) so the
// script's own SessionStillLive re-check has something to read, and the
// session's .schedule aspect (row.sessionKey.schedule) so the script can
// compute the booker's bookerSlotClaim cells to release — TombstoneSession
// never cascades onto the schedule aspect (package.go's "no cascade"
// doctrine), so it is still readable even though the session vertex itself
// is dead. Params carries only bookingKey — ReleaseOrphanedBooking takes no
// session param, unlike CancelBooking, so there is nothing else to template.
// ReleaseOrphanedBooking also mints a wellnessrefund marker, unconditionally,
// when the released booking already carries a posted settlesClassPrice
// charge (a live kv.Links walk inside the script, not declared here — the
// same class-(e) shape CancelBooking's identical lookup uses).
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
					Action:    "directOp",
					Operation: "ReleaseOrphanedBooking",
					Class:     "booking",
					Params:    map[string]string{"bookingKey": "row.bookingKey"},
					Reads:     []string{"row.bookingKey", "row.bookingKey.status", "row.sessionKey", "row.sessionKey.schedule"},
				},
			},
		},
	}
}
