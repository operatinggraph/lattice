package clinicdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract
// #10 §10.8): the clinicSiteBackfill target's single gap, the
// cafeStaleTabSettlement/missing_staleat idiom (cafe-domain/targets.go)
// applied to clinic-domain's own appointment shape.
//
//   - missing_site → directOp(BackfillAppointmentSite) (this package),
//     backfilling the atSite link CreateAppointment's own optional site
//     branch would have written for an appointment booked before a site was
//     ever supplied (the pre-existing corpus) — invisible to any other
//     convergence until this runs (lenses.go). Same Reads shape as
//     cafe-domain's own missing_staleat gap: just the row's own entityKey —
//     BackfillAppointmentSite's script needs no other declared state key.
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{
		{
			TargetID: ClinicSiteBackfillTarget,
			LensRef:  ClinicSiteBackfillTarget,
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_site": {
					Action:    "directOp",
					Operation: "BackfillAppointmentSite",
					// BackfillAppointmentSite is unique to this package's appointment
					// vertexType DDL today, but pinned regardless — the same
					// defensive shape every other directOp in cafe-domain/targets.go
					// uses (see its own missing_account doc comment).
					Class:  "appointment",
					Params: map[string]string{"appointmentKey": "row.entityKey"},
					Reads:  []string{"row.entityKey"},
				},
			},
		},
	}
}
