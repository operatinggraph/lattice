package frontdesk

import "github.com/asolgan/lattice/internal/pkgmgr"

// BookingsBucket is the NATS-KV read model the frontDeskBookings lens
// projects into — the **P5 query surface** for "which residents have a
// booked wellness class right now": the Café front-desk view reads THIS
// bucket (keyed by leaseAppKey, one row per active resident-rate booking)
// to badge a resident's open tab with their upcoming class, never Core KV
// (lattice-architecture.md P5).
const BookingsBucket = "front-desk-bookings"

// Lenses returns the package's single Lens declaration. No UNION is needed
// here (unlike one-bill, which shares one bucket between two lenses to work
// around the cypher engine's missing UNION) — front-desk re-projects only
// wellness-domain's residentRate-linked bookings; the café side of the
// "unified resident context" (open tabs) is already served by cafe-domain's
// own cafeTabSettlement lens (packages/cafe-domain/lenses.go), so the FE
// joins the two client-side by leaseAppKey — the same client-side
// composition idiom cmd/cafe-app's computeTabs and wellness-domain's
// deliberately-uncounted bookedCount already use.
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName: "frontDeskBookings",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        BookingsBucket,
			Engine:        "full",
			Spec:          bookingsSpec,
		},
	}
}

// bookingsSpec projects one row per LIVE, booked, resident-rate wellness
// booking — anchored on the residentRate link (booking→leaseapp) wellness-
// domain's CreateBooking writes only when the supplied leaseAppKey's
// applicationFor link resolves to the same identity as the booker
// (packages/wellness-domain/ddls.go bookingVertexTypeDDL). A booking with no
// residentRate link (rate=standard, or no lease claimed) simply never
// projects here — front-desk shows only a RESIDENT's own booked class, not
// every booking in the building. The OPTIONAL forSession walk keeps the row
// alive (session-name/time null-safe) even if a session were ever
// tombstoned out from under a still-live booking, the same no-WITH-drop
// shape wellness-domain's own wellnessBookingsSpec uses.
const bookingsSpec = `MATCH (b:booking)-[:residentRate]->(l:leaseapp)
WHERE b.status.data.value = 'booked'
OPTIONAL MATCH (b)-[:forSession]->(se:session)
RETURN
  b.key AS key,
  b.key AS bookingKey,
  l.key AS leaseAppKey,
  se.key AS sessionKey,
  se.schedule.data.name AS sessionName,
  se.schedule.data.startsAt AS startsAt,
  'wellness' AS source`
