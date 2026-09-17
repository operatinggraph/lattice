package clinicreminders

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// The displacement of a booked visit by its provider's later-declared
// time-off: SetProviderTimeOff writes the provider only, so a visit the new
// ranges cover would otherwise be reminded, then left for the sweep to spend
// its budget on, with the patient never told. This convergence records the
// fact ON THE VISIT — a level-triggered gap per appointment, "the provider's
// time-off has changed since this visit was last checked against it" — and
// clinic-domain's EvaluateAppointmentDisplacement writes the verdict as the
// .displacement aspect. The reminder and the sweep read that aspect and stand
// down (lenses.go / pastdue.go); the patient is told once through the
// appointmentChangeNotices displaced gap (lenses.go / changenotice.go).
//
//	lens appointmentDisplacements (weaver-target, full)  (pr.timeOff.setRef <> null AND a.displacement.checkedFor <> pr.timeOff.setRef, non-terminal, not ended)
//	playbook missing_displacement_check → directOp(EvaluateAppointmentDisplacement, appointmentKey: row.entityKey, providerKey: row.providerKey, checkedFor: row.timeOffSetRef)
//
// The per-visit gap is what pages the walk: a provider hub carries every
// appointment ever booked with them, so a synchronous walk inside
// SetProviderTimeOff would read the whole history under one script wall;
// here each dispatch reads one visit, and Weaver's own scheduling spreads
// the dispatches. A lens cannot compute the overlap itself (no iteration
// over the ranges array), but it CAN read the linked provider's setRef, which
// is all the gap needs — the op runs the overlap test.
const (
	// AppointmentDisplacementsTarget is the §10.8 TargetID == the
	// appointmentDisplacements lens's OutputKeyPattern prefix (the §10.2↔§10.8
	// binding Weaver reads).
	AppointmentDisplacementsTarget = "appointmentDisplacements"

	// displacementEvaluateOp is the clinic-domain operation this package's
	// playbook dispatches — owned by clinic-domain (its appointment vertexType
	// DDL), since it reuses that script's time_off_overlap / appointment_provider
	// helpers and writes clinic-domain's own .displacement aspect.
	displacementEvaluateOp = "EvaluateAppointmentDisplacement"
)

// appointmentDisplacementsLens is the displacement-check convergence lens.
func appointmentDisplacementsLens() pkgmgr.LensSpec {
	return pkgmgr.LensSpec{
		CanonicalName:  "appointmentDisplacements",
		Class:          "meta.lens",
		Adapter:        "nats-kv",
		Bucket:         "weaver-targets",
		Engine:         "full",
		Spec:           appointmentDisplacementsSpec,
		ProjectionKind: "actorAggregate",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "appointment",
			OutputKeyPattern: "appointmentDisplacements.{actorSuffix}",
			BodyColumns:      []string{"violating", "missing_displacement_check", "entityKey", "providerKey", "timeOffSetRef", "timeOffSetAt", "checkedFor", "displaced", "startsAt", "endsAt", "status"},
			EmptyBehavior:    "delete",
			KeyColumn:        "entityId",
		},
	}
}

// appointmentDisplacementsSpec is the one-row-per-appointment displacement-check
// cypher. It projects NO freshUntil and arms NO timer: the gap is a level
// state over two recorded facts — the provider's .timeOff.setRef (the
// writing op's requestId, stamped by SetProviderTimeOff on every write, a
// clear included — an opaque per-save key, so two saves inside one
// wall-second are two events where a whole-second setAt would be one) and
// the visit's own .displacement.checkedFor (the setRef it was last evaluated
// against, written by whichever of the three writers ran last).
//
// The lifecycle for one appointment:
//
//   - CreateAppointment / RescheduleAppointment write .displacement
//     {displaced: false, checkedFor: <the provider's setRef, when it carries
//     one>, at} themselves, having just proved the visit clear of the current
//     ranges — so a new booking, or a move, reads checkedFor = setRef and never
//     dispatches.
//   - SetProviderTimeOff mints a fresh setRef. Every live visit of that
//     provider now reads checkedFor <> setRef → missing_displacement_check
//     opens on each; Weaver dispatches EvaluateAppointmentDisplacement per
//     visit, which records {displaced, checkedFor: <the live setRef>, at,
//     from?, to?} → checkedFor = setRef → closed. One dispatch per (time-off
//     write × live future visit), whatever the verdict. An evaluation in
//     flight against one write cannot close the gap on the next: the next
//     write's setRef differs, so the verdict it records reads stale and is
//     re-dispatched.
//   - A visit no writer has recorded a verdict for (no .displacement) reads
//     `null <> setRef` true and is evaluated once the provider carries a
//     setRef — the first time-off write after such a visit checks it.
//
// The four-term gate:
//
//   - pr.timeOff.data.setRef <> null — the provider has recorded a time-off
//     write. A provider with no .timeOff, or one whose .timeOff carries no
//     setRef, triggers nothing: there is no event to check against, and no
//     setRef to template into the dispatch. This conjunct is what makes
//     providerKey and timeOffSetRef non-null on every violating row, which is
//     what licenses the playbook's Params off them.
//   - a.displacement.data.checkedFor <> pr.timeOff.data.setRef — the visit was
//     last evaluated against an OLDER write (or never). `<>` is the engine's
//     two-valued null test, so an absent checkedFor opens the gap and equal
//     refs close it.
//   - nonTerminalAppointment — a cancelled / completed / no-show visit has
//     nothing to displace; the op reads the same list and is the empty batch.
//   - NOT (freshnessExpiry.data.byTarget.pastDueAppointments >= endsAt) — the
//     visit is OVER, a recorded fact from the sibling pastDueAppointments
//     target's fired @at on this same anchor (exactly the conjunct
//     appointmentReminders reads). A past visit is not re-checked, and the
//     closed column retires any GapBudgetExhausted latch. While nothing has
//     fired, NOT(false) leaves the gap open — the default a live visit needs.
//
// The gap closes on the op's write (checkedFor = setRef), on the visit going
// terminal, and on the recorded end. Every operand is stored graph data; the
// lens reads no clock. withProvider is the same 0..1 hop the sibling deadline
// lenses walk (CreateAppointment writes exactly one), so the OPTIONAL walk
// does not fan out; a visit with the walk missing projects providerKey and
// timeOffSetRef null and, by the first conjunct, a closed gap. timeOffSetAt
// (the write's human-readable instant) / displaced / checkedFor / startsAt /
// endsAt / status are INFORMATIONAL (operator/FE observability); entityKey,
// providerKey and timeOffSetRef are load-bearing for dispatch (the target's
// Params template off them). violating repeats
// the gap expression verbatim — the engine has no column references in
// RETURN. Built with fmt.Sprintf so the shared nonTerminalAppointment
// fragment and the sibling target id come from their constants; the cypher
// has no negated relationship pattern, only scalar NOT comparisons.
var appointmentDisplacementsSpec = fmt.Sprintf(`MATCH (a:appointment {key: $actorKey})
OPTIONAL MATCH (a)-[:withProvider]->(pr:provider)
RETURN
  a.key AS actorKey,
  a.key AS entityKey,
  pr.key AS providerKey,
  pr.timeOff.data.setRef AS timeOffSetRef,
  pr.timeOff.data.setAt AS timeOffSetAt,
  a.displacement.data.checkedFor AS checkedFor,
  a.displacement.data.displaced AS displaced,
  a.schedule.data.startsAt AS startsAt,
  a.schedule.data.endsAt AS endsAt,
  a.status.data.value AS status,
  ((pr.timeOff.data.setRef <> null) AND (a.displacement.data.checkedFor <> pr.timeOff.data.setRef) AND %[1]s AND NOT (a.freshnessExpiry.data.byTarget.%[2]s >= a.schedule.data.endsAt)) AS missing_displacement_check,
  ((pr.timeOff.data.setRef <> null) AND (a.displacement.data.checkedFor <> pr.timeOff.data.setRef) AND %[1]s AND NOT (a.freshnessExpiry.data.byTarget.%[2]s >= a.schedule.data.endsAt)) AS violating`,
	nonTerminalAppointment, PastDueAppointmentsTarget)

// appointmentDisplacementsTarget returns the §10.8 playbook for the
// displacement check: the single missing_displacement_check gap →
// directOp(EvaluateAppointmentDisplacement) over the appointment.
func appointmentDisplacementsTarget() pkgmgr.WeaverTargetSpec {
	return pkgmgr.WeaverTargetSpec{
		TargetID: AppointmentDisplacementsTarget,
		Description: "Every upcoming visit is re-checked against its provider's time-off whenever that time-off " +
			"changes, and the visit records whether the provider can still see the patient.",
		LensRef: "appointmentDisplacements",
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_displacement_check": {
				Action:    "directOp",
				Operation: displacementEvaluateOp,
				// row.providerKey and row.timeOffSetRef are non-null on every
				// violating row: the gap's own first conjunct is
				// pr.timeOff.data.setRef <> null (a null setRef, or a missing
				// withProvider walk, closes the gap), so the dispatch never
				// meets the strategist's null-column refusal.
				Params: map[string]string{"appointmentKey": "row.entityKey", "providerKey": "row.providerKey", "checkedFor": "row.timeOffSetRef"},
				// The op reads the visit's root (liveness) and .schedule (the
				// span the overlap test runs over), and the provider's .timeOff
				// (the ranges, and the live setRef it records) on every
				// dispatch, so all three are REQUIRED — the .timeOff exists on
				// every violating row by the gap's first conjunct, and
				// SetProviderTimeOff only ever upserts it. The provider ROOT is
				// not declared: the op never reads it (providerKey is matched
				// against the withProvider walk, not hydrated).
				// row.providerKey.timeOff is the row.<column>.<aspect> derived
				// form on a non-anchor column (the cafe row.tabKey.status
				// idiom). .status is OPTIONAL because a never-set status is a
				// live scheduled visit; the op's own .displacement is OPTIONAL
				// because a visit no writer has recorded a verdict for carries
				// none — and declaring it hands the Processor the hydrated
				// revision its bare update is conditioned on, so a booking
				// writer landing concurrently re-executes the evaluation
				// rather than being overwritten by it.
				Reads:         []string{"row.entityKey", "row.entityKey.schedule", "row.providerKey.timeOff"},
				OptionalReads: []string{"row.entityKey.status", "row.entityKey.displacement"},
				// The one walk the op runs, nameable up front off the row's
				// anchor: appointment_provider resolves the visit's own
				// provider off its withProvider link (exactly one, bounded).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "row.entityKey", Relation: "withProvider", Direction: "out"},
				},
			},
		},
	}
}
