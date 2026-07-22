package frontdesk

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// BookingsBucket is the NATS-KV read model the frontDeskBookings lens
// projects into — the **P5 query surface** for "which residents have a
// booked wellness class right now": the Café front-desk view reads THIS
// bucket (keyed by leaseAppKey, one row per active resident-rate booking)
// to badge a resident's open tab with their upcoming class, never Core KV
// (lattice-architecture.md P5).
const BookingsBucket = "front-desk-bookings"

// LeaseDetailsBucket is the NATS-KV read model the frontDeskLeaseDetails
// lens projects into — one row per leaseapp, keyed by leaseAppKey, carrying
// the applied-to unit's rent/currency/term/address. The Café front-desk view
// reads THIS bucket to show lease details (term/rent) on every open-tab
// card, not just those with a booked class (frontDeskBookings' anchor is the
// booking, so it has no row for a leaseapp with no booking).
const LeaseDetailsBucket = "front-desk-lease-details"

// VisitsBucket is the NATS-KV read model the frontDeskVisits lens projects
// into — the Inc 5 clinic tail of the unified resident context: one row per
// LIVE, scheduled, resident-confined clinic appointment (residentVisit link
// present), keyed by leaseAppKey. Deliberately carries ONLY existence + time
// — never the .schedule visit reason, and never patient/provider identity —
// front desk staff see "a visit is scheduled," not why or with whom; the
// clinic's own PHI line (packages/clinic-domain/ddls.go) draws the boundary
// at raw clinical content (.encounter), but a visit REASON is still more
// than a café/front-desk audience needs, so this lens narrows further than
// clinicAppointments does for clinic staff.
const VisitsBucket = "front-desk-visits"

// Lenses returns the package's Lens declarations. No UNION is needed here
// (unlike one-bill, which shares one bucket between two lenses to work
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
		{
			CanonicalName: "frontDeskLeaseDetails",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        LeaseDetailsBucket,
			Engine:        "full",
			Spec:          leaseDetailsSpec,
		},
		{
			CanonicalName: "frontDeskVisits",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        VisitsBucket,
			Engine:        "full",
			Spec:          visitsSpec,
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

// leaseDetailsSpec projects one row per leaseapp — anchored on the leaseapp
// itself (not the unit or a booking), so every open café tab's lease gets a
// row regardless of whether that resident has a booked class. The
// appliesToUnit walk is OPTIONAL (mirrors lease-signing's
// leaseApplicationCompleteSpec): unit is required at CreateLeaseApplication
// so a live application always resolves one, but a tombstoned unit must not
// drop the anchor — it degrades to null rent/term rather than no row.
const leaseDetailsSpec = `MATCH (l:leaseapp)
OPTIONAL MATCH (l)-[:appliesToUnit]->(u:unit)
RETURN
  l.key AS key,
  l.key AS leaseAppKey,
  u.key AS unitKey,
  u.address.data.line1 AS unitAddress,
  u.listing.data.rentAmount AS unitRent,
  u.listing.data.rentCurrency AS unitCurrency,
  u.listing.data.leaseTermMonths AS unitLeaseTermMonths`

// visitsSpec projects one row per LIVE, scheduled clinic appointment carrying
// a residentVisit link (appointment→leaseapp) — clinic-domain's
// CreateAppointment writes that link only when the supplied leaseAppKey's
// applicationFor link resolves to the same identity as the patient's own
// identifiedBy identity (packages/clinic-domain/ddls.go
// appointmentVertexTypeDDL), the same confinement shape bookingsSpec already
// uses for residentRate. An appointment with no residentVisit link (no lease
// claimed, or the claim didn't match) never projects — front-desk shows only
// a resident's OWN visit, never the clinic's full schedule. RETURN carries
// only startsAt/endsAt, deliberately excluding the .schedule visit reason —
// see VisitsBucket's doc comment.
const visitsSpec = `MATCH (a:appointment)-[:residentVisit]->(l:leaseapp)
WHERE a.status.data.value = 'scheduled'
RETURN
  a.key AS key,
  a.key AS appointmentKey,
  l.key AS leaseAppKey,
  a.schedule.data.startsAt AS startsAt,
  a.schedule.data.endsAt AS endsAt`
