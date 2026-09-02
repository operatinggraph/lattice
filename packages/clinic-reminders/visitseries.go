package clinicreminders

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// The clinic vertical's recurring forcing function: a patient on a standing cadence
// (chronic-care monthly check-ins, weekly PT) gets a self-rolling "next visit due"
// worklist gap instead of a per-entity @every schedule. Structurally a ROLLING
// generalization of followups.go's one-shot follow-up: the same convergence
// machinery (aspect + op + freshUntil-armed @at lens + directOp playbook), made to
// re-arm its own next deadline each time it converges instead of firing once. See
// _bmad-output/implementation-artifacts/clinic-recurring-visit-series-design.md §3
// for why this is a package-level rolling-@at series rather than a per-series
// @every schedule (state lives in the read model; timers are derived from it).
//
//	vtx.visitseries.<id>              class=visitseries   root {}
//	  .series   = {intervalDays, startAt, activeUntil?}          (write-once at Start)
//	  .progress = {nextDueAt, occurrenceCount, lastOccurrenceAt?} (rolled by AdvanceVisitSeries)
//	  .paused   = {value: bool}                                  (optional lifecycle toggle)
//	lnk.visitseries.<id>.forPatient.patient.<id>       (series → patient, later-arriving source)
//	lnk.visitseries.<id>.withProvider.provider.<id>    (series → provider, later-arriving source)
//	lnk.visitseries.<id>.atSite.building.<id>          (series → building, later-arriving source;
//	  the site the visits happen at — visitseries_site.go, the staff-visibility anchor that
//	  survives its provider being tombstoned)
//	vtx.patient.<id>.activeVisitSeriesWith<providerId>  (class visitSeriesGuard) = {seriesKey, activeUntil?}
//	  per-(patient,provider) uniqueness guard, StartVisitSeries-only (create or, once its
//	  denormalized activeUntil has passed, OCC-revive) — a paused series still holds the
//	  guard (resume it rather than starting a duplicate)
//
//	op StartVisitSeries{patientKey, providerKey, intervalDays, startAt, activeUntil?}
//	  rejects ActiveVisitSeriesExists if the pair already holds an unexpired guard
//	op PauseVisitSeries{seriesKey} / ResumeVisitSeries{seriesKey}
//	op EndVisitSeries{seriesKey}  writes .series.activeUntil = op.submittedAt (write-once
//	  in this direction too — rejects VisitSeriesAlreadyEnded if already set); the same
//	  "clean termination" activeUntil StartVisitSeries can seed up front, now reachable
//	  after the fact so a series need not be given an end date at booking time to ever get one
//	op AdvanceVisitSeries{seriesKey, dueFor, intervalDays, occurrenceCount?}  (the directOp the playbook dispatches)
//	op BackfillVisitSeriesSite{seriesKey} / SetVisitSeriesSite{seriesKey, site}  (the atSite link — visitseries_site.go)
//	lens visitSeriesDue (weaver-target, full)   (freshUntil = .progress.nextDueAt; rolls forward on each advance)
//	playbook missing_series_advance → directOp(AdvanceVisitSeries, dueFor: row.nextDueAt, intervalDays: row.intervalDays, occurrenceCount: row.occurrenceCount)
//
// nextDueAt is precomputed AT WRITE TIME (Start / Advance), never derived by the
// lens — the full engine's cypher has no date-arithmetic support (no duration()/
// date-add function), so every deadline in this codebase is a stored field the
// lens compares lexically, never computed in the cypher itself (the remindAt /
// followUpDate precedent). AdvanceVisitSeries rolls nextDueAt forward from the
// deadline it just serviced (dueFor), NOT from $now, keeping the cadence on a fixed
// grid immune to fire latency drift — the same rule followUpReminders documents.
const (
	visitSeriesVertexDDL       = "visitseries"
	visitSeriesAspectDDL       = "visitSeriesDefinition"
	visitSeriesProgressAspect  = "visitSeriesProgress"
	visitSeriesPausedAspectDDL = "visitSeriesPaused"
	visitSeriesGuardAspectDDL  = "visitSeriesGuard"

	startVisitSeriesOp   = "StartVisitSeries"
	pauseVisitSeriesOp   = "PauseVisitSeries"
	resumeVisitSeriesOp  = "ResumeVisitSeries"
	endVisitSeriesOp     = "EndVisitSeries"
	advanceVisitSeriesOp = "AdvanceVisitSeries"

	// VisitSeriesDueTarget is the §10.8 TargetID == the visitSeriesDue lens's
	// OutputKeyPattern prefix (the §10.2↔§10.8 binding Weaver reads).
	VisitSeriesDueTarget = "visitSeriesDue"
)

// visitSeriesDDLs returns the visit-series vertex type (one script owning every
// operationType, mirroring clinic-domain's appointment DDL) + its aspect-type
// write gates.
func visitSeriesDDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		visitSeriesVertexTypeDDL(),
		visitSeriesDefinitionAspectTypeDDL(),
		visitSeriesProgressAspectTypeDDL(),
		visitSeriesPausedAspectTypeDDL(),
		visitSeriesGuardAspectTypeDDL(),
		visitSeriesSiteAssignmentAspectTypeDDL(),
	}
}

func visitSeriesVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName: visitSeriesVertexDDL,
		Class:         "meta.ddl.vertexType",
		PermittedCommands: []string{
			startVisitSeriesOp, pauseVisitSeriesOp, resumeVisitSeriesOp, endVisitSeriesOp, advanceVisitSeriesOp,
			backfillVisitSeriesSiteOp, setVisitSeriesSiteOp,
		},
		Description: "Clinic recurring visit series DDL. Vertex shape: vtx.visitseries.<NanoID>, class=visitseries, " +
			"root data = {} (minimal, D5). StartVisitSeries validates patientKey/providerKey are alive + correctly " +
			"classed, rejects ActiveVisitSeriesExists if the pair already holds an active series (see the " +
			"visitSeriesGuard aspect-type DDL), then mints the series + its .series {intervalDays, startAt, " +
			"activeUntil?} + .progress {nextDueAt: startAt, occurrenceCount: 0} aspects, and writes the forPatient + " +
			"withProvider links (Contract #1 §1.1 — the series is the later-arriving source). PauseVisitSeries / " +
			"ResumeVisitSeries toggle the .paused {value} aspect (absent = not paused) — a paused series still holds " +
			"its patient+provider pair (resume it; StartVisitSeries will not treat the pair as free). " +
			"EndVisitSeries writes .series.activeUntil = op.submittedAt, rejecting VisitSeriesAlreadyEnded if a " +
			"cutoff is already set — the same clean-termination field StartVisitSeries can seed up front, now " +
			"settable after the fact so a series left open-ended can still be given an end date. " +
			"AdvanceVisitSeries is the directOp the visitSeriesDue §10.8 " +
			"playbook dispatches when missing_series_advance opens: it rolls .progress forward — lastOccurrenceAt = dueFor (the " +
			"deadline just serviced, NOT $now — keeps the cadence on a fixed grid), nextDueAt = dueFor + " +
			"intervalDays·days, occurrenceCount + 1 — re-arming the next occurrence. " +
			"BackfillVisitSeriesSite{seriesKey} is the orchestration-internal auto-remediation that fills in the " +
			"series' atSite link (visitseries → building), dispatched by the visitSeriesSiteBackfill Weaver " +
			"target's missing_series_site gap for a LIVE series carrying none — the corpus started before the " +
			"link existed, plus any series whose provider had no single site at the time. A no-op (empty " +
			"mutations/events) if the series already carries a live atSite link (another dispatch already won, " +
			"or a redelivery). Otherwise it resolves the series' own provider LIVE off its withProvider link " +
			"(series_provider) and looks up that provider's LIVE practicesAt sites (sites_for_provider — a " +
			"DECOMMISSIONED building is not one: an atSite link to a tombstoned site confers no staff " +
			"visibility, since every read walk drops a tombstoned vertex, yet still reads as \"already sited\" " +
			"to both site ops and to this gap, stranding the series unrecoverably): when EXACTLY " +
			"ONE comes back it writes the atSite link, plus the same CreateOnly .siteAssignment guard aspect " +
			"SetVisitSeriesSite writes (the two ops can otherwise race two different links onto one series when " +
			"the provider gains a second site between their reads); when ZERO or TWO-OR-MORE do, which site this series " +
			"belongs to is ambiguous and the op never guesses — it no-ops cleanly, and the series stays " +
			"missing_series_site forever, harmlessly (the gap is idempotently re-dispatched and cleanly no-ops " +
			"every time). SetVisitSeriesSite{seriesKey, site} is the human-facing manual counterpart, letting a " +
			"person CHOOSE among a provider's live sites when the exactly-one-site rule can't (two-or-more). It " +
			"is not a way to invent a practicesAt relationship that doesn't exist: site must be one of the " +
			"series' own provider's live practicesAt sites (ProviderNotAtSite otherwise), so a provider at ZERO " +
			"sites, or now tombstoned, still needs AssignProviderSite run first. Confinement is the same " +
			"operator-exempt-only enforce_workplace guard Pause/Resume/EndVisitSeries carry. A no-op if the " +
			"series already carries a live atSite link — reassigning an already-set site is out of scope. It " +
			"writes the same atSite link + CreateOnly .siteAssignment guard-aspect batch as " +
			"BackfillVisitSeriesSite (visitSeriesSiteAssignmentAspectTypeDDL), so two concurrent " +
			"callers landing on different sites can't both commit — the loser's whole batch rejects. " +
			"Reads [seriesKey] to liveness-guard the parent for every command.",
		Script: visitSeriesScript,
		InputSchema: `{"type":"object","properties":` +
			`{"patientKey":{"type":"string","description":"vtx.patient.<NanoID> the series is for (StartVisitSeries; required, validated alive). The caller MUST list it in ContextHint.Reads."},` +
			`"providerKey":{"type":"string","description":"vtx.provider.<NanoID> the series is with (StartVisitSeries; required, validated alive). The caller MUST list it in ContextHint.Reads."},` +
			`"intervalDays":{"type":"integer","description":"Days between occurrences (StartVisitSeries; required, positive)."},` +
			`"startAt":{"type":"string","description":"RFC3339 instant of the first occurrence (StartVisitSeries; required)."},` +
			`"activeUntil":{"type":"string","description":"RFC3339 instant the series stops re-arming past (StartVisitSeries; optional — absent means no end)."},` +
			`"seriesKey":{"type":"string","description":"vtx.visitseries.<NanoID> of an existing series (PauseVisitSeries/ResumeVisitSeries/EndVisitSeries/AdvanceVisitSeries/BackfillVisitSeriesSite/SetVisitSeriesSite; required, validated alive). The caller MUST list it in ContextHint.Reads."},` +
			`"site":{"type":"string","description":"vtx.building.<NanoID> clinic site the series' visits happen at (SetVisitSeriesSite; required). Must be one of the series' own provider's live practicesAt sites — a building that is not assigned to that provider, or has since been decommissioned, is REJECTED (ProviderNotAtSite), never a silent fall-through. Writes an atSite link (visitseries→building). A no-op if the series already carries one."},` +
			`"dueFor":{"type":"string","description":"The .progress.nextDueAt deadline this advance is servicing (AdvanceVisitSeries; the playbook supplies row.nextDueAt)."},` +
			`"occurrenceCount":{"type":"integer","description":"The series' current occurrence count before this advance (AdvanceVisitSeries; the playbook supplies row.occurrenceCount; defaults 0 if omitted)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.visitseries.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"patientKey":      "Full vtx.patient.<NanoID> key the series is for. StartVisitSeries validates it is alive + class=patient, writes the forPatient link, and claims a per-provider activeVisitSeriesWith guard aspect on it (ActiveVisitSeriesExists if the pair already holds an active one).",
			"providerKey":     "Full vtx.provider.<NanoID> key the series is with. StartVisitSeries validates it is alive + class=provider and writes the withProvider link.",
			"intervalDays":    "Days between occurrences. Stored on .series and re-supplied by the visitSeriesDue playbook on every AdvanceVisitSeries so the roll-forward math needs no extra read.",
			"startAt":         "RFC3339 instant of the first occurrence. Seeds .progress.nextDueAt (the first deadline anchors on startAt, not an interval offset).",
			"activeUntil":     "Optional RFC3339 instant past which the series stops re-arming (clean termination — no cancel op needed). Absent means the series never ends on its own.",
			"seriesKey":       "Full vtx.visitseries.<NanoID> key of an existing series.",
			"site":            "Full vtx.building.<NanoID> clinic site key (SetVisitSeriesSite; required, and only there — BackfillVisitSeriesSite resolves the site itself). Validated to be one of the series' own provider's LIVE practicesAt sites (ProviderNotAtSite otherwise), so it names a real, still-operating site the provider actually works at. Writes an atSite link (visitseries→building), which is what keeps the series inside its front desk's world once the provider is tombstoned.",
			"dueFor":          "The .progress.nextDueAt deadline this AdvanceVisitSeries is servicing. Stored as the new .progress.lastOccurrenceAt and used as the base the next nextDueAt rolls forward from (fixed-grid cadence, immune to dispatch latency).",
			"occurrenceCount": "The series' occurrence count going into this advance (the visitSeriesDue playbook supplies row.occurrenceCount). Stored back incremented by one; purely informational (not gate-affecting).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "StartVisitSeries — a monthly chronic-care check-in",
				Payload: map[string]any{
					"patientKey": "vtx.patient.<NanoID>", "providerKey": "vtx.provider.<NanoID>",
					"intervalDays": 30, "startAt": "2026-08-01T09:00:00Z",
				},
				ExpectedOutcome: "Validates patient + provider alive, mints vtx.visitseries.<NanoID> (root {}) + " +
					".series {intervalDays:30, startAt} + .progress {nextDueAt: startAt, occurrenceCount:0} + the " +
					"forPatient/withProvider links. Returns primaryKey (the series key).",
			},
			{
				Name:            "PauseVisitSeries — suspend a series",
				Payload:         map[string]any{"seriesKey": "vtx.visitseries.<NanoID>"},
				ExpectedOutcome: "Upserts .paused {value:true}; the visitSeriesDue lens stops projecting a due gap or an armed @at until resumed.",
			},
			{
				Name:            "ResumeVisitSeries — un-pause a series",
				Payload:         map[string]any{"seriesKey": "vtx.visitseries.<NanoID>"},
				ExpectedOutcome: "Upserts .paused {value:false}; the series resumes rolling from its current nextDueAt.",
			},
			{
				Name:    "EndVisitSeries — give an open-ended series a stop date",
				Payload: map[string]any{"seriesKey": "vtx.visitseries.<NanoID>"},
				ExpectedOutcome: "Sets .series.activeUntil = op.submittedAt (rejects VisitSeriesAlreadyEnded if a cutoff " +
					"is already set). A currently-due occurrence still gets serviced once more, exactly as a cutoff " +
					"given at StartVisitSeries time would — clean termination, no cancel-in-flight special case.",
			},
			{
				Name: "AdvanceVisitSeries — roll the series forward one occurrence",
				Payload: map[string]any{
					"seriesKey": "vtx.visitseries.<NanoID>", "dueFor": "2026-08-01T09:00:00Z",
					"intervalDays": 30, "occurrenceCount": 0,
				},
				ExpectedOutcome: "Validates the series is alive, then writes .progress {lastOccurrenceAt: dueFor, " +
					"nextDueAt: dueFor + 30 days, occurrenceCount:1}. Re-runs cleanly (idempotent in effect — the " +
					"MarkExpired / reminder-marker idiom).",
			},
			{
				Name:    "BackfillVisitSeriesSite — backfill a missing atSite link (orchestration-internal)",
				Payload: map[string]any{"seriesKey": "vtx.visitseries.<NanoID>"},
				ExpectedOutcome: "Validates the series is alive. No-ops cleanly (empty mutations/events) if it " +
					"already carries a live atSite link. Otherwise resolves its provider LIVE off the withProvider " +
					"link and looks up that provider's LIVE practicesAt sites (a decommissioned building is " +
					"skipped): when exactly one comes back, writes the atSite link + the CreateOnly " +
					".siteAssignment guard aspect, and returns primaryKey as that LINK key (the " +
					"AssignProviderSite convention — the op's mutations carry no vertex root of their own beyond " +
					"the series itself); when zero or two-or-more come back, no-ops cleanly rather than guess. " +
					"Submitted under Weaver's service-actor authority " +
					"only (the visitSeriesSiteBackfill target); no human/consumer caller.",
			},
			{
				Name:    "SetVisitSeriesSite — a staffer supplies the site BackfillVisitSeriesSite couldn't resolve",
				Payload: map[string]any{"seriesKey": "vtx.visitseries.<NanoID>", "site": "vtx.building.<NanoID>"},
				ExpectedOutcome: "Validates the series is alive and (for a non-operator caller) that the actor " +
					"worksAt a building covering one of the series' provider's LIVE sites. No-ops cleanly if the series " +
					"already carries a live atSite link — reassigning an already-set site is out of scope. " +
					"Otherwise validates site is one of that provider's live practicesAt sites (ProviderNotAtSite " +
					"if not — a decommissioned building is not one), writes the atSite link plus a CreateOnly " +
					".siteAssignment guard aspect in one atomic " +
					"batch, and returns primaryKey as that LINK key. Submitted by the operator or front-of-house " +
					"staff.",
			},
		},
	}
}

// visitSeriesDefinitionAspectTypeDDL declares the .series cadence definition —
// written once by StartVisitSeries (intervalDays, startAt never change after: no
// SetVisitSeries op; changing the cadence is pause + start a new series) and, for
// activeUntil ONLY, once more by EndVisitSeries (write-once in that direction too —
// EndVisitSeries rejects VisitSeriesAlreadyEnded if a cutoff is already set, so
// intervalDays/startAt are re-written unchanged rather than partially updated).
// NON-sensitive: dates + an interval, no PHI (the clinical reason a series exists is
// out of scope here, same posture as followUpReminder's non-PHI marker).
func visitSeriesDefinitionAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     visitSeriesAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{startVisitSeriesOp, endVisitSeriesOp},
		Description: "Visit-series cadence definition aspect (clinic-reminders). Stored as " +
			"vtx.visitseries.<NanoID>.series (class visitSeriesDefinition) = {intervalDays, startAt, activeUntil?}. " +
			"Non-sensitive. Written by StartVisitSeries (mints intervalDays/startAt, optionally activeUntil) and, " +
			"for activeUntil alone, EndVisitSeries (rejects VisitSeriesAlreadyEnded if already set — write-once each " +
			"way); this aspect-type DDL is the step-6 write gate. Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"intervalDays":{"type":"integer","description":"Days between occurrences."},` +
			`"startAt":{"type":"string","description":"RFC3339 instant of the first occurrence."},` +
			`"activeUntil":{"type":"string","description":"Optional RFC3339 instant the series stops re-arming past."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"intervalDays": "Days between occurrences, re-supplied to every AdvanceVisitSeries by the playbook.",
			"startAt":      "RFC3339 instant of the first occurrence (seeds the initial .progress.nextDueAt).",
			"activeUntil":  "Optional RFC3339 instant past which the series stops re-arming.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "visit series cadence definition",
				Payload:         map[string]any{"intervalDays": 30, "startAt": "2026-08-01T09:00:00Z"},
				ExpectedOutcome: "Stored as vtx.visitseries.<NanoID>.series; written by StartVisitSeries.",
			},
		},
	}
}

// visitSeriesProgressAspectTypeDDL declares the .progress rolling state — the field
// the visitSeriesDue lens reads for its freshUntil / missing_series_advance gate, and the ONLY
// aspect AdvanceVisitSeries writes.
func visitSeriesProgressAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     visitSeriesProgressAspect,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{startVisitSeriesOp, advanceVisitSeriesOp},
		Description: "Visit-series rolling progress aspect (clinic-reminders). Stored as " +
			"vtx.visitseries.<NanoID>.progress (class visitSeriesProgress) = {nextDueAt, occurrenceCount, " +
			"lastOccurrenceAt?}. Non-sensitive. Written by StartVisitSeries (seeds nextDueAt = startAt, " +
			"occurrenceCount = 0) and rolled forward by AdvanceVisitSeries (the directOp the visitSeriesDue §10.8 " +
			"playbook dispatches) each time an occurrence comes due. UNCONDITIONED updates (create-if-absent / " +
			"overwrite-if-present) — idempotent in effect, re-run-safe under at-least-once (the reminder-marker idiom).",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"nextDueAt":{"type":"string","description":"RFC3339 instant of the next occurrence — the lens's freshUntil / missing_series_advance gate deadline."},` +
			`"occurrenceCount":{"type":"integer","description":"Count of occurrences serviced so far (informational)."},` +
			`"lastOccurrenceAt":{"type":"string","description":"RFC3339 instant of the most recently serviced occurrence (absent until the first advance)."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"nextDueAt":        "RFC3339 instant of the next occurrence. The visitSeriesDue lens arms freshUntil = nextDueAt while future, and opens missing_series_advance once it passes.",
			"occurrenceCount":  "Count of occurrences serviced so far. Purely informational — not gate-affecting.",
			"lastOccurrenceAt": "RFC3339 instant of the most recently serviced occurrence (the dueFor AdvanceVisitSeries was given). Absent until the first advance.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "visit series rolling progress",
				Payload:         map[string]any{"nextDueAt": "2026-09-01T09:00:00Z", "occurrenceCount": 1, "lastOccurrenceAt": "2026-08-01T09:00:00Z"},
				ExpectedOutcome: "Stored as vtx.visitseries.<NanoID>.progress; seeded by StartVisitSeries, rolled by AdvanceVisitSeries.",
			},
		},
	}
}

// visitSeriesPausedAspectTypeDDL declares the optional .paused lifecycle toggle.
// Absent means not paused (the visitSeriesDue lens null-safe-tests <> true), so
// StartVisitSeries need not write it — only Pause/ResumeVisitSeries ever do.
func visitSeriesPausedAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     visitSeriesPausedAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{pauseVisitSeriesOp, resumeVisitSeriesOp},
		Description: "Visit-series pause toggle aspect (clinic-reminders). Stored as " +
			"vtx.visitseries.<NanoID>.paused (class visitSeriesPaused) = {value: bool}. Non-sensitive. Written ONLY " +
			"by PauseVisitSeries / ResumeVisitSeries; absent means not paused (the visitSeriesDue lens tests " +
			"value <> true, which is true when the aspect is absent — null-safe). While paused the lens projects " +
			"no due gap and no armed @at timer, and the current .progress.nextDueAt is preserved (resuming picks up " +
			"exactly where it left off, no missed-occurrence catch-up burst).",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"value":{"type":"boolean","description":"true = paused, false = resumed."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"value": "true = paused (the series stops projecting a due gap or an armed timer); false = resumed.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "visit series pause toggle",
				Payload:         map[string]any{"value": true},
				ExpectedOutcome: "Stored as vtx.visitseries.<NanoID>.paused; written by PauseVisitSeries / ResumeVisitSeries.",
			},
		},
	}
}

// visitSeriesGuardAspectTypeDDL declares the .activeVisitSeriesWith<providerId>
// aspect (class visitSeriesGuard) — a deterministic per-(patient,provider)
// uniqueness marker on the PRE-EXISTING patient hub, the write-path idiom
// clinic-domain's slot-claim / café's cafeOpenTabGuard establish (Cap-KV §06: the
// op's own Starlark logic licenses the check; a deterministic guard key needs no
// enumeration primitive). The local name is provider-suffixed (not a fixed
// canonical name) because a patient may hold at most one such guard PER provider,
// mirroring providerSlotClaim's per-cell local-name pattern.
func visitSeriesGuardAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     visitSeriesGuardAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{startVisitSeriesOp},
		Description: "Per-(patient,provider) active-visit-series uniqueness guard (clinic-reminders). Stored as " +
			"vtx.patient.<NanoID>.activeVisitSeriesWith<providerId> (class visitSeriesGuard) = {seriesKey}. " +
			"Non-sensitive. Written ONLY by StartVisitSeries: a class-(d) declared-optionalReads dedup key — " +
			"absent mints the guard fresh (create-only, the concurrent-race backstop); present names a prior " +
			"series, whose OWN LIVE .series/.paused aspects StartVisitSeries re-reads (class-(e) follow-up read off " +
			"the guard's data-derived seriesKey — never a denormalized copy, since paused is mutable and the " +
			".series aspect's own doc names \"pause + start a new series\" as the intended cadence-change flow) to " +
			"decide: paused, or past its .series.activeUntil → OCC-revives the guard onto the new series " +
			"(expectedRevision = the read guard's revision); otherwise still active → rejects the new " +
			"StartVisitSeries with ActiveVisitSeriesExists. Nothing else ever writes or releases this aspect — " +
			"unlike a slot-claim's per-appointment lifecycle, a visit series has no terminal/cancel op, so the next " +
			"StartVisitSeries for the same pair is the only place \"is the old one really still active\" gets " +
			"decided, and it always re-checks live.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"seriesKey":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"seriesKey": "The vtx.visitseries.<NanoID> currently holding this patient+provider pair's active-series slot.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "active visit-series guard aspect",
				Payload:         map[string]any{"seriesKey": "vtx.visitseries.<NanoID>"},
				ExpectedOutcome: "Stored as vtx.patient.<NanoID>.activeVisitSeriesWith<providerId>; claimed or OCC-revived by StartVisitSeries.",
			},
		},
	}
}

// visitSeriesScript handles all four visit-series operationTypes in one script (the
// appointment-DDL multi-command idiom).
const visitSeriesScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_upsert(vtx_key, local_name, cls, data):
    return {"op": "update", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_upsert_occ(vtx_key, local_name, cls, data, expected_revision):
    # Like make_aspect_upsert but carries an explicit expectedRevision so the
    # commit applies an OCC condition (an update with no expectedRevision commits
    # UNCONDITIONED — step8_commit.go). Mirrors clinic-domain's ddls.go.
    m = make_aspect_upsert(vtx_key, local_name, cls, data)
    m["expectedRevision"] = expected_revision
    return m

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def optional_string(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or type(v) != type(""):
        return None
    v = v.strip()
    if len(v) == 0:
        return None
    return v

def required_int(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type(0):
        fail("InvalidArgument: " + name + ": required integer")
    return v

def optional_int(p, name, default):
    if not hasattr(p, name):
        return default
    v = getattr(p, name)
    if v == None:
        return default
    if type(v) != type(0):
        fail("InvalidArgument: " + name + ": must be an integer")
    return v

def required_bool(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if type(v) != type(True):
        fail("InvalidArgument: " + name + ": required boolean")
    return v

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if parts[2] == "":
        fail("InvalidArgument: " + name + ": empty id segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def vertex_alive_of_class(state, key, want_class):
    if not vertex_alive(state, key):
        return False
    doc = state[key]
    return hasattr(doc, "class") and getattr(doc, "class") == want_class

# Front-desk workplace confinement — the frontOfHouse standing guard for the
# staff visit-series ops (Start/Pause/Resume). Mirrors clinic-domain's
# CreateAppointment confinement (ddls.go) — same helpers, resolved through the
# same withProvider -> practicesAt edge clinicAppointmentsRead anchors on — but
# these ops call enforce_workplace rather than require_workplace, so root is the
# only exemption. The appointment version's require_workplace also exempts
# a platform-validated target, safe there only because a downstream identifiedBy
# patient binding backstops the consumer self-book path. These ops carry no
# consumer/scope=self grant and no such backstop, and a scope=any caller can
# attach ANY target (the Gateway forwards authContext verbatim; step 3
# authorizes scope=any without inspecting target), so that exemption would let a
# front-desk actor forge target==actor and skip confinement entirely.
ROLE_PAGE_LIMIT = 50
MAX_ROLE_PAGES = 4
WORKPLACE_PARENT_PAGE_LIMIT = 20
MAX_PARENT_PAGES = 4
WORKPLACE_MAX_DEPTH = 8
WORKPLACE_MAX_NODES = 64

def actor_holds_operator(actor_key):
    # Root is proven from the GRAPH, mirroring the kernel's own root-grant lens
    # (internal/bootstrap/lenses.go: MATCH (identity)-[:holdsRole]->(role) WHERE
    # role.canonicalName.data.value = 'operator') — never a compile-time constant
    # (the primordial operator id is loaded at runtime, invisible to this
    # package-init script text). Paginated: a role beyond page 1 must not read
    # as "not held" — the walk follows the cursor up to MAX_ROLE_PAGES pages
    # before giving up, and giving up still denies (fail-closed).
    cursor = None
    for _page in range(MAX_ROLE_PAGES):
        # read-posture: (e) relation=holdsRole epoch=none — an identity holds few
        # roles, so this is never a keyspace scan; a role granted concurrently can
        # only widen authority, and the confined branch is the safe one.
        page, cursor = kv.Links(actor_key, "holdsRole", "out", cursor, ROLE_PAGE_LIMIT)
        for lk in page:
            if lk.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the enumeration above
            # (data-derived key — the role is unknown until it resolves).
            cn = kv.Read(lk.targetVertex + ".canonicalName")
            if cn != None and not cn.isDeleted and cn.data.get("value") == "operator":
                return True
        if cursor == None:
            return False
    return False

def worksAt_covers(actor_id, location_key):
    # Answers "does this actor worksAt this location, or any LIVE location that
    # contains it?" -- a BREADTH-first walk up the containedIn topology, testing
    # the actor's deterministic worksAt link at every node. The location itself
    # is tested first, so a staff member wired to an exact unit matches too; one
    # wired to any containing building matches everything containedIn it.
    #
    # A tombstoned link OR VERTEX is absent. kv.Read returns the tombstone
    # document rather than None (step4_hydrate routes only ErrKeyNotFound to
    # knownAbsent), and UnwireWorksAt / TombstoneLocation tombstone rather than
    # delete, so isDeleted is tested explicitly in three places: the worksAt
    # link, each containedIn link, and every location VERTEX the walk stands on.
    # The vertex test is what stops a DECOMMISSIONED location from still
    # conferring authority -- TombstoneLocation does not cascade to containedIn
    # links (location-domain), so those links stay live and only the vertex's own
    # isDeleted marks it gone, while the read side stops dead there (the full
    # engine's fetchNode yields nothing for a soft-deleted node). Transiting one
    # would grant a write the reader would never show.
    #
    # It is tested on EVERY node, the caller-supplied one included, not just on
    # ancestors: a guard where a dead ancestor confers nothing but a dead
    # starting location confers everything would be exactly the kind of
    # inconsistency the next reader copies wrongly.
    #
    # EVERY parent is followed, not one per level: containment is a DAG. A walk
    # that kept a single parent would deny a staffer wired to whichever branch it
    # happened to discard, while a read-side lens projecting a covering set
    # unions every branch of [:containedIn*0..7] (cafe-domain's and
    # wellness-domain's coveringLocations are the two that do).
    #
    # Bounded three ways so an op-time guard cannot fan out: WORKPLACE_MAX_DEPTH
    # levels (0..7, the read side's hop range), WORKPLACE_PARENT_PAGE_LIMIT
    # parents per node, and WORKPLACE_MAX_NODES distinct nodes overall, a node
    # never being enqueued twice. Exhausting a bound falls through to the final
    # 'return False' -- a DENIAL, never an escape. The node budget is the one
    # bound the read side does not share (its walk caps hops, not nodes), so a
    # containment tree wide enough to exhaust it denies a write the reader would
    # show; it is set far above any real topology, and it fails closed.
    if location_key == None:
        return False
    frontier = [location_key]
    seen = [location_key]
    for _ in range(WORKPLACE_MAX_DEPTH):
        if len(frontier) == 0:
            return False
        parents = []
        for cur in frontier:
            parts = cur.split(".")
            if len(parts) != 3:
                # Not walkable. Stops its OWN branch rather than aborting the
                # walk, so one malformed ancestor cannot deny a sibling branch
                # that would have matched. A malformed location_key still
                # denies: nothing else is queued, so the frontier empties.
                continue
            # read-posture: (e) per-candidate follow-up read off the containedIn
            # enumeration below -- the location VERTEX, so a tombstoned one
            # neither confers a match nor is walked through.
            node = kv.Read(cur)
            if node == None or node.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the same
            # enumeration (data-derived key -- the ancestor chain is not
            # knowable client-side, so it cannot be pre-declared).
            lnk = kv.Read("lnk.identity." + actor_id + ".worksAt." + parts[1] + "." + parts[2])
            if lnk != None and not lnk.isDeleted:
                return True
            # Paginated: a parent beyond page 1 must not read as "no more
            # parents" -- the walk follows the cursor up to MAX_PARENT_PAGES
            # pages before moving on, same as actor_holds_operator's role walk.
            cursor = None
            for _page in range(MAX_PARENT_PAGES):
                # read-posture: (e) relation=containedIn epoch=none -- a location has
                # at most a few parents; containment is provisioned topology, not
                # written concurrently with this op.
                page, cursor = kv.Links(cur, "containedIn", "out", cursor, WORKPLACE_PARENT_PAGE_LIMIT)
                for lk in page:
                    if lk.isDeleted:
                        continue
                    nxt = lk.targetVertex
                    if nxt in seen:
                        continue
                    if len(seen) >= WORKPLACE_MAX_NODES:
                        continue
                    # Charged to the budget at ENQUEUE, so the node count bounds the
                    # walk's reads exactly rather than to within a page, and an
                    # ancestor reachable from several branches is visited once.
                    seen.append(nxt)
                    parents.append(nxt)
                if cursor == None:
                    break
        frontier = parents
    return False

def vertex_live(key):
    # Is this vertex present AND not tombstoned? The standalone form of the
    # vertex test worksAt_covers performs inline at every node of its bounded
    # walk, for the resolvers that walk THROUGH a vertex to produce that walk's
    # input -- a provider, a studio, a lease. Those hops are invisible to
    # worksAt_covers: by the time it runs the dead vertex has already been
    # transited and only its live locations remain, so the confinement it
    # computes is the dead entity's ex-topology.
    #
    # A tombstone is a DOCUMENT, not an absence. kv.Read returns it rather than
    # None (step4_hydrate routes only ErrKeyNotFound to knownAbsent), so the
    # '== None' test alone reads a tombstoned vertex as live. Both halves are
    # required, and a None key answers False so a caller that resolved nothing
    # takes the same denying branch as one that resolved something dead.
    #
    # Distinct from vertex_alive(state, key), which answers the same question
    # from the operation's DECLARED contextHint.reads. The keys here are
    # data-derived -- resolved from a link mid-walk, so unknowable client-side
    # and undeclarable -- and only a live read can see them.
    #
    if key == None:
        return False
    # read-posture: (e) one bounded read per candidate. At the sites this exists
    # for, the key is data-derived -- resolved from a kv.Links enumeration
    # mid-walk, so unknowable client-side and undeclarable. A resolver cannot
    # see which caller it has, and some callers reach it with a payload key a
    # declared read has already proved live; there this is a redundant re-proof,
    # not a second class of access. Screening at the resolver rather than per
    # call site is what keeps the rule uniform.
    node = kv.Read(key)
    return node != None and not node.isDeleted

def sites_for_provider(provider):
    # A provider's LIVE practicesAt sites — the buildings visitSeriesRead anchors
    # its workplace read token on, so write confinement and read confinement
    # resolve through exactly the same edge, and now with exactly the same
    # liveness. provider may be None (a series whose withProvider link is
    # absent), which yields []. ALL live sites are returned: staff at any one of
    # a provider's buildings are equally entitled to that provider's series.
    #
    # Three things are screened, and each catches what the others cannot.
    #
    # The provider VERTEX, not just a non-None key: TombstoneProvider
    # soft-deletes it with no cascade onto practicesAt, so a dead provider would
    # otherwise still hand back the sites it no longer practises at.
    if not vertex_live(provider):
        return []
    # read-posture: (e) relation=practicesAt epoch=none (a site assigned
    # concurrently with this write can only WIDEN the confining set, never narrow
    # it, so the confined branch stays the safe one) — a per-candidate follow-up
    # enumeration off the provider (data-derived hub); a provider practises at a
    # handful of sites at most.
    spage, _ = kv.Links(provider, "practicesAt", "out")
    sites = []
    for lk in spage:
        # The LINK: RemoveProviderSite tombstones it rather than deleting it, so
        # a withdrawn assignment must be skipped explicitly.
        if lk.isDeleted:
            continue
        # The link's TARGET BUILDING, which is where this diverges from
        # clinic-domain's same-named helper — deliberately, and it is the tighter
        # side of the divergence its own doc records ("this walk additionally
        # drops a tombstoned BUILDING, which sites_for_provider does not").
        # TombstoneLocation soft-deletes a building with no cascade onto
        # practicesAt, so a decommissioned site otherwise stays in this list.
        #
        # It is load-bearing here in a way it is not for a pure confinement
        # helper, because this list is ALSO the whitelist SetVisitSeriesSite
        # validates its caller-chosen site against and the candidate set
        # BackfillVisitSeriesSite picks its exactly-one site from. A dead
        # building left in it would be settable — and an atSite link pointing at
        # a tombstoned building is worse than none at all: the read model's own
        # atSite comprehension drops a tombstoned vertex, so the link confers NO
        # workplace anchor (the exact stranding this whole mechanism exists to
        # prevent), while series_site() sees a LIVE LINK and makes both site ops
        # permanently no-op, and the missing_series_site gap cannot re-open
        # either, since its own OPTIONAL MATCH drops the dead target the same
        # way. Unrecoverable, from one write. So the read happens here, once, in
        # the single place both ops derive their sites from.
        #
        # Bounded: one vertex_live read per surviving link, off an enumeration
        # already bounded to a provider's handful of sites.
        if not vertex_live(lk.targetVertex):
            continue
        sites.append(lk.targetVertex)
    return sites

def series_provider(series_key):
    # The series' OWN provider, resolved from its withProvider link (never a
    # payload field — Pause/Resume carry only seriesKey). Mirrors clinic-domain's
    # appointment_provider. StartVisitSeries writes exactly one withProvider link
    # (deterministic key), so this never fans out.
    # read-posture: (e) relation=withProvider epoch=none.
    ppage, _ = kv.Links(series_key, "withProvider", "out")
    provider = None
    for lk in ppage:
        if not lk.isDeleted:
            provider = lk.targetVertex
    return provider

def series_site(series_key):
    # The site the series' visits happen at, resolved from its OWN atSite link,
    # or None when it carries none. The presence test both site ops branch on:
    # neither ever reassigns an already-set site, so a live link means "already
    # done" and both no-op cleanly on it.
    # Unlike series_provider this returns on the FIRST live link rather than the
    # last: at most one is ever written (both writers refuse when one is already
    # present), so the two are the same answer, and returning early keeps the
    # walk a single bounded read.
    # read-posture: (e) relation=atSite epoch=none — a series carries at most one
    # atSite link, so this is a single bounded enumeration off the series key the
    # caller has already proven alive, never a keyspace scan.
    apage, _ = kv.Links(series_key, "atSite", "out")
    for lk in apage:
        if not lk.isDeleted:
            return lk.targetVertex
    return None

def enforce_workplace(location_keys, what):
    # require_workplace minus the validated-target exemption, for a
    # resource-scoped op that has already checked for itself that the validated
    # target names the resource being acted on. Past that check the caller is an
    # ordinary staff member and must clear the worksAt walk like any other.
    # These ops go further: they have no consumer self path to protect and no
    # identifiedBy backstop, so they never offer the exemption at all — the
    # block comment above records why.
    #
    # location_keys is a LIST of candidate locations, and covering ANY ONE of
    # them authorizes the write: a target can legitimately sit at several places
    # at once (a provider practises at two buildings), and staff at either one
    # are equally entitled to it. An empty list -- a target whose location
    # cannot be resolved at all -- is a DENIAL for anyone but an operator, so an
    # unwired topology fails closed rather than falling open.
    if actor_holds_operator(op.actor):
        return
    _, actor_id = parts_of(op.actor, "actor", "identity")
    for loc in location_keys:
        if loc != None and worksAt_covers(actor_id, loc):
            return
    fail("AuthDenied: " + op.actor + " does not worksAt any location covering " +
         str(location_keys) + "; " + what)

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "StartVisitSeries":
        patient_key = required_string(p, "patientKey")
        parts_of(patient_key, "patientKey", "patient")
        provider_key = required_string(p, "providerKey")
        _, provider_id = parts_of(provider_key, "providerKey", "provider")
        interval_days = required_int(p, "intervalDays")
        if interval_days <= 0:
            fail("InvalidArgument: intervalDays: must be positive")
        start_at = time.rfc3339_utc(required_string(p, "startAt"))
        active_until = optional_string(p, "activeUntil")
        if active_until != None:
            active_until = time.rfc3339_utc(active_until)

        if not vertex_alive_of_class(state, patient_key, "patient"):
            fail("UnknownPatient: " + patient_key + " is absent, tombstoned, or not a patient")
        if not vertex_alive_of_class(state, provider_key, "provider"):
            fail("UnknownProvider: " + provider_key + " is absent, tombstoned, or not a provider")

        # Staff-standing workplace confinement (frontOfHouse's scope=any grant):
        # a front-desk actor may start a series only with a provider practising at
        # a building it worksAt — resolved off the PAYLOAD provider (validated
        # alive + class=provider just above). No-op for operator (root is exempt).
        enforce_workplace(sites_for_provider(provider_key), "cannot start a visit series with provider " + provider_key)

        # At most one ACTIVE series per (patient, provider) pair (Cap-KV §06 — the
        # op's own logic licenses the check). The guard is a deterministic pointer
        # on the patient hub, keyed by provider; "active" is re-derived from the
        # PRIOR series' own live .series/.paused aspects (never denormalized —
        # paused is mutable and .series' doc already documents "pause + start a
        # new series" as the intended cadence-change workflow, so a paused prior
        # series must NOT block a fresh one).
        guard_name = "activeVisitSeriesWith" + provider_id
        guard_key = patient_key + "." + guard_name
        # read-posture: (d) declared optionalReads by StartVisitSeries's
        # dispatcher (deterministic per patient+provider dedup key; mirrors
        # clinic-domain's claim_cell — kv.Read only decides which mutation verb
        # to emit, CreateOnly/expectedRevision at commit is the actual safety
        # property against a concurrent double-start/double-revive).
        guard = kv.Read(guard_key)
        guard_revision = None
        if guard != None and not guard.isDeleted:
            guard_revision = guard.revision
            prior_series_key = guard.data.get("seriesKey")
            # read-posture: (e) per-candidate follow-up read off the guard's
            # data-derived seriesKey (at most one guard per patient+provider, so
            # this is never a keyspace scan).
            prior_series = kv.Read(prior_series_key + ".series")
            # read-posture: (e) the second per-candidate follow-up read off the
            # same guard-derived seriesKey — paused is mutable, so "active" is
            # re-derived from it rather than denormalized onto the guard.
            prior_paused = kv.Read(prior_series_key + ".paused")
            prior_is_paused = prior_paused != None and not prior_paused.isDeleted and prior_paused.data.get("value") == True
            # A prior series whose .series aspect can't be read (should never
            # happen — StartVisitSeries always writes it atomically with the
            # guard) fails closed: unreadable counts as still-active rather than
            # silently allowing a duplicate.
            prior_readable = prior_series != None and not prior_series.isDeleted
            prior_active_until = prior_series.data.get("activeUntil") if prior_readable else None
            prior_ended = prior_readable and prior_active_until != None and time.rfc3339_utc(op.submittedAt) > prior_active_until
            if not prior_is_paused and not prior_ended:
                fail("ActiveVisitSeriesExists: patient " + patient_key + " already has an active visit series with provider " + provider_key)

        series_id = nanoid.new()
        series_key = "vtx.visitseries." + series_id

        series_data = {"intervalDays": interval_days, "startAt": start_at}
        if active_until != None:
            series_data["activeUntil"] = active_until
        progress_data = {"nextDueAt": start_at, "occurrenceCount": 0}

        if guard_revision != None:
            guard_mutation = make_aspect_upsert_occ(patient_key, guard_name, "visitSeriesGuard", {"seriesKey": series_key}, guard_revision)
        else:
            guard_mutation = make_aspect(patient_key, guard_name, "visitSeriesGuard", {"seriesKey": series_key})

        # forPatient / withProvider: the series (later-arriving) is the source, the
        # pre-existing patient / provider is the target (Contract #1 §1.1).
        # Sentences: "visitseries forPatient patient", "visitseries withProvider
        # provider".
        for_patient_lnk = "lnk.visitseries." + series_id + ".forPatient.patient." + patient_key.split(".")[2]
        with_provider_lnk = "lnk.visitseries." + series_id + ".withProvider.provider." + provider_id

        mutations = [
            make_vtx(series_key, "visitseries", {}),
            make_aspect(series_key, "series", "visitSeriesDefinition", series_data),
            make_aspect(series_key, "progress", "visitSeriesProgress", progress_data),
            guard_mutation,
            make_link(for_patient_lnk, series_key, patient_key, "forPatient", "forPatient", {}),
            make_link(with_provider_lnk, series_key, provider_key, "withProvider", "withProvider", {}),
        ]
        events = [{"class": "clinic.visitSeriesStarted",
                   "data": {"seriesKey": series_key, "patientKey": patient_key, "providerKey": provider_key,
                            "intervalDays": interval_days, "startAt": start_at}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": series_key}}

    if ot == "PauseVisitSeries" or ot == "ResumeVisitSeries":
        series_key = required_string(p, "seriesKey")
        parts_of(series_key, "seriesKey", "visitseries")
        if not vertex_alive(state, series_key):
            fail("UnknownVisitSeries: " + series_key + " is absent or tombstoned")
        # Staff-standing workplace confinement: a front-desk actor may pause/resume
        # only a series whose provider practises at a building it worksAt —
        # resolved off the series' OWN withProvider link (Pause/Resume carry no
        # provider payload). No-op for operator (root is exempt).
        enforce_workplace(sites_for_provider(series_provider(series_key)), "cannot change visit series " + series_key)
        paused = (ot == "PauseVisitSeries")
        mutations = [make_aspect_upsert(series_key, "paused", "visitSeriesPaused", {"value": paused})]
        events = [{"class": "clinic.visitSeriesPausedChanged", "data": {"seriesKey": series_key, "paused": paused}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": series_key}}

    if ot == "EndVisitSeries":
        series_key = required_string(p, "seriesKey")
        parts_of(series_key, "seriesKey", "visitseries")
        if not vertex_alive(state, series_key):
            fail("UnknownVisitSeries: " + series_key + " is absent or tombstoned")
        # Staff-standing workplace confinement, same as Pause/Resume: resolved off
        # the series' OWN withProvider link (EndVisitSeries carries no provider
        # payload). No-op for operator (root is exempt).
        enforce_workplace(sites_for_provider(series_provider(series_key)), "cannot end visit series " + series_key)

        series_aspect_key = series_key + ".series"
        if not vertex_alive(state, series_aspect_key):
            fail("UnknownVisitSeries: " + series_key + " has no cadence definition")
        series_doc = state[series_aspect_key]
        series_data = series_doc.data
        if series_data.get("activeUntil") != None:
            fail("VisitSeriesAlreadyEnded: " + series_key + " already has an end date")

        ended_at = time.rfc3339_utc(op.submittedAt)
        new_series_data = {"intervalDays": series_data.get("intervalDays"), "startAt": series_data.get("startAt"), "activeUntil": ended_at}
        mutations = [make_aspect_upsert_occ(series_key, "series", "visitSeriesDefinition", new_series_data, series_doc.revision)]
        events = [{"class": "clinic.visitSeriesEnded", "data": {"seriesKey": series_key, "activeUntil": ended_at}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": series_key}}

    if ot == "AdvanceVisitSeries":
        series_key = required_string(p, "seriesKey")
        parts_of(series_key, "seriesKey", "visitseries")

        # Liveness guard: never advance a paused-away/absent/tombstoned series. The
        # op hydrates [seriesKey] (ContextHint.Reads).
        if not vertex_alive(state, series_key):
            fail("UnknownVisitSeries: " + series_key + " is absent or tombstoned; no advance written")

        due_for = required_string(p, "dueFor")
        interval_days = required_int(p, "intervalDays")
        if interval_days <= 0:
            fail("InvalidArgument: intervalDays: must be positive")
        occurrence_count = optional_int(p, "occurrenceCount", 0)

        # nextDueAt rolls forward from dueFor (the deadline JUST serviced), not
        # $now — keeps the cadence on a fixed grid, immune to dispatch latency (the
        # followUpReminders idiom). intervalDays is re-supplied by the playbook
        # (row.intervalDays) so this op needs no second read of .series.
        next_due = time.rfc3339_add(due_for, str(interval_days * 24) + "h")

        progress = {"lastOccurrenceAt": due_for, "nextDueAt": next_due, "occurrenceCount": occurrence_count + 1}
        mutations = [make_aspect_upsert(series_key, "progress", "visitSeriesProgress", progress)]
        events = [{"class": "clinic.visitSeriesAdvanced",
                   "data": {"seriesKey": series_key, "occurredFor": due_for, "nextDueAt": next_due}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": series_key}}

    if ot == "BackfillVisitSeriesSite":
        # Orchestration-internal: the visitSeriesSiteBackfill target's
        # missing_series_site gap (visitseries_site.go) dispatches this for a LIVE
        # series carrying no atSite link. Mirrors clinic-domain's
        # BackfillAppointmentSite exactly, over this package's own series shape.
        # No workplace confinement: Weaver's service actor is the only caller
        # (permissions.go grants it operator-only), and there is no human whose
        # reach it could exceed.
        series_key = required_string(p, "seriesKey")
        _, series_id = parts_of(series_key, "seriesKey", "visitseries")
        if not vertex_alive(state, series_key):
            fail("UnknownVisitSeries: " + series_key + " is absent or tombstoned")

        # Already sited — another dispatch already won, or this is a redelivery.
        # No-op cleanly rather than reject.
        if series_site(series_key) != None:
            return {"mutations": [], "events": [], "response": {}}

        sites = sites_for_provider(series_provider(series_key))
        if len(sites) != 1:
            # Zero sites (no provider link, or an unassigned/dead one) or
            # two-or-more (which site this series belongs to is genuinely
            # ambiguous) — never guess. The series stays missing_series_site
            # forever, which is harmless: the gap is idempotently re-dispatched
            # and cleanly no-ops every time. SetVisitSeriesSite is the human
            # escape hatch for the two-or-more case.
            return {"mutations": [], "events": [], "response": {}}

        site_key = sites[0]
        _, site_id = parts_of(site_key, "site", "building")
        at_site_lnk = "lnk.visitseries." + series_id + ".atSite.building." + site_id
        mutations = [
            make_link(at_site_lnk, series_key, site_key, "atSite", "atSite", {}),
            # The SAME CreateOnly .siteAssignment guard SetVisitSeriesSite
            # writes, for the same reason. This op is not self-serializing just
            # because it never chooses between candidates: a provider's site
            # count can go 1 -> 2 (AssignProviderSite) between this dispatch's
            # sites_for_provider read and a concurrent SetVisitSeriesSite's, and
            # then both see no live atSite link and commit DIFFERENT,
            # non-colliding link keys — two atSite links on one series, breaking
            # the at-most-one invariant series_site()'s early return and the read
            # spec's either/or CASE both rest on. The guard's key does not vary
            # with the chosen site, so CreateOnly on it lets exactly one of the
            # two batches land. Never a conflict on the ordinary path: this line
            # is only reached when no live atSite link exists, and the guard is
            # only ever written alongside one.
            make_aspect(series_key, "siteAssignment", "visitSeriesSiteAssignment", {}),
        ]
        events = [{"class": "clinic.visitSeriesSiteBackfilled", "data": {"seriesKey": series_key, "site": site_key}}]
        # primaryKey names the LINK key itself (the AssignProviderSite idiom):
        # the write-footprint reply constraint accepts a mutation's own key or its
        # 3-segment vertex root, and this op's only mutation is a link, which has
        # no vertex root of its own.
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": at_site_lnk}}

    if ot == "SetVisitSeriesSite":
        # The STAFF manual counterpart to BackfillVisitSeriesSite. It closes the
        # "2+ sites" ambiguity directly (a human picks the right one); a provider
        # at ZERO sites, or now tombstoned, still needs AssignProviderSite run
        # first, since the chosen site is hard-validated against that provider's
        # live practicesAt set below — this is a manual override for CHOOSING
        # among a provider's live sites, never a way to invent a practicesAt
        # relationship that doesn't exist.
        series_key = required_string(p, "seriesKey")
        _, series_id = parts_of(series_key, "seriesKey", "visitseries")
        if not vertex_alive(state, series_key):
            fail("UnknownVisitSeries: " + series_key + " is absent or tombstoned")

        # The provider's live sites do double duty: the confining set, and the
        # membership whitelist the chosen site must be in. ONE enumeration, so
        # the site a caller may pick can never be one the guard did not consider.
        sites = sites_for_provider(series_provider(series_key))

        # Staff-standing workplace confinement, identical to Pause/Resume/End:
        # operator-exempt only, resolved off the series' OWN withProvider link
        # (this op carries no provider payload). Run BEFORE any other branch so
        # an unauthorized caller cannot distinguish an already-sited series from
        # a site-less one.
        enforce_workplace(sites, "cannot set the site on visit series " + series_key)

        # Already sited — reassignment is out of scope (the gap this closes is
        # "the series names no site at all", never "the site is wrong"); no-op
        # cleanly rather than reject.
        if series_site(series_key) != None:
            return {"mutations": [], "events": [], "response": {}}

        site_key = required_string(p, "site")
        if site_key not in sites:
            # Membership in the provider's live practicesAt set is the WHOLE
            # check, and it is sufficient because sites_for_provider screens that
            # set three ways as it builds it: the provider VERTEX is alive, each
            # practicesAt LINK is alive, and each target BUILDING is alive. Class
            # is settled upstream — AssignProviderSite is the only writer of a
            # practicesAt link and validates its target is a location-domain
            # building carrying a building class — so every surviving key names a
            # real, still-operating site. Re-deriving any of that here would add
            # a second, independently-drifting definition of the same thing, and
            # the one property a write-time re-derivation could contribute
            # (liveness NOW rather than at assignment time) is precisely the one
            # sites_for_provider already reads live.
            fail("ProviderNotAtSite: site: " + site_key + " is not one of the live sites the provider of " +
                 series_key + " practicesAt; assign the provider to that site first")

        _, site_id = parts_of(site_key, "site", "building")
        at_site_lnk = "lnk.visitseries." + series_id + ".atSite.building." + site_id
        # Two concurrent calls choosing DIFFERENT sites for the same still-
        # site-less series both pass the no-live-atSite check above and would both
        # commit a DIFFERENT, non-colliding link key (the target segment varies
        # with the chosen site) — CreateOnly on the link alone cannot be the lock.
        # The .siteAssignment aspect exists exactly for this: its key is the SAME
        # regardless of chosen site, so CreateOnly on it, in the SAME atomic batch
        # as the link, makes the loser's whole commit reject.
        mutations = [
            make_link(at_site_lnk, series_key, site_key, "atSite", "atSite", {}),
            make_aspect(series_key, "siteAssignment", "visitSeriesSiteAssignment", {}),
        ]
        events = [{"class": "clinic.visitSeriesSiteSet", "data": {"seriesKey": series_key, "site": site_key}}]
        # primaryKey names the LINK key itself, mirroring BackfillVisitSeriesSite.
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": at_site_lnk}}

    fail("visitseries DDL: unknown operationType: " + ot)
`

// visitSeriesDueLens is the recurring visit-series convergence lens — one row per
// series, mirroring appointmentRemindersSpec/followUpRemindersSpec's freshness
// inversion (freshUntil = the deadline arms the @at; the gap OPENS once it passes)
// but re-arming a NEW freshUntil on every convergence instead of clearing to null.
func visitSeriesDueLens() pkgmgr.LensSpec {
	return pkgmgr.LensSpec{
		CanonicalName:  "visitSeriesDue",
		Class:          "meta.lens",
		Adapter:        "nats-kv",
		Bucket:         "weaver-targets",
		Engine:         "full",
		Spec:           visitSeriesDueSpec,
		ProjectionKind: "actorAggregate",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "visitseries",
			OutputKeyPattern: "visitSeriesDue.{actorSuffix}",
			BodyColumns:      []string{"violating", "missing_series_advance", "entityKey", "freshUntil", "nextDueAt", "intervalDays", "occurrenceCount", "active", "patientKey", "providerKey"},
			EmptyBehavior:    "delete",
			KeyColumn:        "entityId",
		},
	}
}

// visitSeriesDueSpec is the one-row-per-series convergence cypher.
//
// active = NOT paused AND (no activeUntil OR nextDueAt <= activeUntil) — a paused
// series or one whose next occurrence would fall past its end projects no gap and
// no armed timer (clean termination / suspension, no cancel-schedule dance).
//
// The gate:
//
//   - freshUntil = nextDueAt WHILE active AND this target has recorded no lapse
//     reaching nextDueAt (a wake-up arming Weaver's @at temporal lane). A
//     nextDueAt already in the past — a series whose cadence fell behind — is
//     projected VERBATIM so the overdue @at fires at once and records the lapse,
//     which is the only path that opens the gap.
//   - missing_series_advance = active AND
//     freshnessExpiry.data.byTarget.visitSeriesDue >= nextDueAt (the violating row
//     the playbook converges via AdvanceVisitSeries). Both operands are stored
//     graph data — the lens reads no clock, so the row is a pure function of the
//     subgraph. compareAny answers false when either is nil, so a series no timer
//     has fired on, and one carrying no nextDueAt, both read not-due.
//
// Unlike the one-shot reminders, convergence here does NOT clear the gate to
// permanently false — AdvanceVisitSeries rewrites nextDueAt to a NEW future
// deadline, so the row re-projects PENDING (not due, freshUntil re-armed) rather
// than SENT. That is the "roll" — the series never fully converges while active;
// it just keeps re-arming its own next wake-up. The marker needs no clearing
// write for that: the advance moves nextDueAt PAST the recorded instant, so the
// comparison reads not-due again on its own.
//
// '<> true' (not '= false') is the paused null-test: an absent .paused aspect
// reads null, and null <> true is true in the full engine (the remindedFor <>
// startsAt idiom) — so a series that has never been paused is correctly treated as
// not-paused without a separate absence check. '= null' (not the unsupported IS
// NULL) is the activeUntil absence test.
//
// One-row-per-anchor: forPatient / withProvider are 0..1 (StartVisitSeries writes
// exactly one of each, deterministic keys), so the OPTIONAL walks do not fan out.
const visitSeriesDueSpec = `MATCH (s:visitseries {key: $actorKey})
OPTIONAL MATCH (s)-[:forPatient]->(p:patient)
OPTIONAL MATCH (s)-[:withProvider]->(pr:provider)
RETURN
  s.key AS actorKey,
  s.key AS entityKey,
  s.series.data.intervalDays AS intervalDays,
  s.series.data.activeUntil AS activeUntil,
  s.progress.data.nextDueAt AS nextDueAt,
  s.progress.data.occurrenceCount AS occurrenceCount,
  p.key AS patientKey,
  pr.key AS providerKey,
  ((s.paused.data.value <> true) AND ((s.series.data.activeUntil = null) OR (s.progress.data.nextDueAt <= s.series.data.activeUntil))) AS active,
  CASE WHEN (s.paused.data.value <> true) AND ((s.series.data.activeUntil = null) OR (s.progress.data.nextDueAt <= s.series.data.activeUntil)) AND NOT (s.freshnessExpiry.data.byTarget.visitSeriesDue >= s.progress.data.nextDueAt) THEN s.progress.data.nextDueAt ELSE null END AS freshUntil,
  ((s.paused.data.value <> true) AND ((s.series.data.activeUntil = null) OR (s.progress.data.nextDueAt <= s.series.data.activeUntil)) AND (s.freshnessExpiry.data.byTarget.visitSeriesDue >= s.progress.data.nextDueAt)) AS missing_series_advance,
  ((s.paused.data.value <> true) AND ((s.series.data.activeUntil = null) OR (s.progress.data.nextDueAt <= s.series.data.activeUntil)) AND (s.freshnessExpiry.data.byTarget.visitSeriesDue >= s.progress.data.nextDueAt)) AS violating`

// visitSeriesReadLens is the PATIENT-anchored protected Postgres read model for
// the recurring-visit-series view (D1.5, mirroring clinic-domain's
// clinicAppointmentsRead). cmd/clinic-app's handleMyVisitSeries reads it as the
// patient's own view: RLS scopes the read to the verified JWT subject. Staff
// reach it two ways: cmd/clinic-app's handleStaffVisitSeries under the reserved
// WildcardAnchor grant (no separate staff projection needed — the same
// mechanism handleStaffAppointments uses against clinicAppointmentsRead), and
// cmd/facet's narrower frontOfHouse worklist pane via the workplace token below
// (facet-staff-worlds-design.md §3.5's staffReadGrants).
//
// authz_anchors carries the patient's own NanoID plus the WORKPLACE token — the
// building the series is seen at — mirroring clinicAppointmentsReadSpec exactly
// (clinic-domain/lenses.go): front-desk staff working that building read the row
// through service-location's staffReadGrants.
//
// The workplace half has TWO arms, and the CASE picks one or the other, never
// both. The first is the series' provider's practicesAt sites — the same edge
// enforce_workplace resolves for the WRITE side, so read and write confinement
// agree. The second is the series' OWN atSite building (visitseries_site.go),
// and it is what carries a series whose provider was later tombstoned, or
// unassigned from every site by RemoveProviderSite: Contract #1 filters a
// tombstoned vertex out of every graph walk, so pr binds null and the
// practicesAt comprehension yields nothing — with no fallback the row would keep
// only its patient self-anchor and drop out of every front-desk world, leaving a
// live standing cadence readable by the reserved WildcardAnchor holder alone.
// atSite is trustworthy on its own account: BackfillVisitSeriesSite and
// SetVisitSeriesSite each write it only after checking the building is one of
// the provider's live practicesAt sites at write time, so it names a real site
// independent of the provider's current status. Because the arms are exclusive,
// a series whose live provider practises at the very site it is held at carries
// that building's token once, not twice.
//
// Each arm is a pattern COMPREHENSION, not a bare array element, and the
// difference is load-bearing: a walk that finds no building yields a NULL
// element, which ProtectedAdapter.toStringSlice rejects — failing the whole
// row's upsert, so a series reaching neither a practicesAt site nor an atSite
// building would vanish for its own patient too. A comprehension yields []
// instead. A missing building must cost a row its staff visibility, never its
// existence.
//
// forPatient is a REQUIRED match (the anchor walk) so a series with no
// patient link projects NO row — fail-closed, mirroring
// clinicAppointmentsReadSpec's REQUIRED forPatient walk. withProvider stays
// OPTIONAL: a display-only neighbour, not the anchor. series_status /
// next_due_at / interval_days / occurrence_count are the same display columns
// the unprotected visitSeriesDue lens's own active/nextDueAt/etc. derive from;
// the Weaver-dispatch machinery columns (freshUntil, missing_series_advance,
// violating) are NOT projected here — this is a read model, not a convergence
// target.
func visitSeriesReadLens() pkgmgr.LensSpec {
	return pkgmgr.LensSpec{
		CanonicalName: "visitSeriesRead",
		Class:         "meta.lens",
		Adapter:       "postgres",
		Table:         "read_visit_series",
		Engine:        "full",
		Spec:          visitSeriesReadSpec,
		Protected:     true,
		IntoKey:       []string{"series_id"},
		Columns: []pkgmgr.PostgresColumn{
			{Name: "entity_key", Type: "text"},
			{Name: "patient_key", Type: "text"},
			{Name: "patient_name", Type: "text"},
			{Name: "unlinked_patient_name", Type: "text"},
			{Name: "provider_key", Type: "text"},
			{Name: "provider_name", Type: "text"},
			{Name: "provider_specialty", Type: "text"},
			{Name: "interval_days", Type: "integer"},
			{Name: "next_due_at", Type: "text"},
			{Name: "occurrence_count", Type: "integer"},
			{Name: "site_key", Type: "text"},
			{Name: "site_name", Type: "text"},
			{Name: "series_status", Type: "text"},
			{Name: "series_endable", Type: "boolean"},
		},
		// The patient's name lives on their identity's sensitive .name aspect
		// (clinic-domain's CreatePatient, retention-class-key-custody-design.md
		// F3(b)), so this read model decrypts it at projection exactly as
		// clinicPatientsRead does for email/phone. A patient with no identity —
		// the walk-in nobody holds contact details for — carries their name in
		// unlinked_patient_name instead: outside the erasure plane, so outside
		// this column.
		SecureColumns: []pkgmgr.SecureColumn{
			{Column: "patient_name", HolderTypes: []string{"identity"}, Field: "value"},
		},
	}
}

// visitSeriesReadSpec is the PATIENT-anchored protected Postgres read model's
// cypher (D1.5). Same nextDueAt derivation as visitSeriesDueSpec, minus the
// freshUntil/missing_series_advance/violating dispatch columns.
//
// series_status is the raw three-state read a client renders directly
// (mirroring clinic-domain's own appointmentStatus idiom) rather than the
// fused `active` boolean visitSeriesDueSpec still carries for Weaver's own
// convergence question ("should this trigger next"), a different question
// this read model does not need to answer: a series that reached its
// natural end (never paused, but its next occurrence would fall past
// activeUntil) is "ended", not the same state as one a human explicitly
// paused — collapsing them left a naturally-ended series showing a Resume
// button that submitted and changed nothing observable (verticals.md).
// Precedence is sequential, not layered: a series paused before it ran out
// still reads "paused" even if activeUntil has since passed, because
// "ended" only ever tests the NOT PAUSED branch — matching the intent "did
// this run its course on its own", not "is today past the cutoff".
//
// series_endable is the EndVisitSeries op-meta's VisibleWhen gate (visitSeriesOpMetas
// below) — offer the "End series" affordance whenever series_status is NOT already
// "ended", active or paused alike (OpVisibleWhenSpec is single-condition equality
// only, so a positive "endable" flag stands in for the "not ended" test the button
// actually needs — the descriptor's Facet/edge-manifest consumer, unlike this hand-
// authored cmd/clinic-app, gates purely off the row's own columns).
const visitSeriesReadSpec = `MATCH (s:visitseries)
MATCH (s)-[:forPatient]->(p:patient)
OPTIONAL MATCH (s)-[:withProvider]->(pr:provider)
OPTIONAL MATCH (s)-[:atSite]->(site:building)
OPTIONAL MATCH (p)-[:identifiedBy]->(pid:identity)
RETURN
  nanoIdFromKey(s.key)          AS series_id,
  s.key                         AS entity_key,
  p.key                         AS patient_key,
  pid.name.data                 AS patient_name,
  p.demographics.data.fullName  AS unlinked_patient_name,
  pr.key                        AS provider_key,
  pr.profile.data.fullName      AS provider_name,
  pr.profile.data.specialty     AS provider_specialty,
  s.series.data.intervalDays    AS interval_days,
  s.progress.data.nextDueAt     AS next_due_at,
  s.progress.data.occurrenceCount AS occurrence_count,
  site.key                      AS site_key,
  site.site.data.name           AS site_name,
  CASE
    WHEN (s.paused.data.value <> true) AND (s.series.data.activeUntil <> null) AND (s.progress.data.nextDueAt > s.series.data.activeUntil) THEN "ended"
    WHEN (s.paused.data.value <> true) THEN "active"
    ELSE "paused"
  END AS series_status,
  NOT ((s.paused.data.value <> true) AND (s.series.data.activeUntil <> null) AND (s.progress.data.nextDueAt > s.series.data.activeUntil))
                                 AS series_endable,
  [nanoIdFromKey(p.key)]
    + (CASE WHEN (pr)-[:practicesAt]->(pb:building)
            THEN [(pr)-[:practicesAt]->(b:building) | nanoIdFromKey(b.key)]
            ELSE [(s)-[:atSite]->(sb:building) | nanoIdFromKey(sb.key)] END)
                                 AS authz_anchors
`

// visitSeriesDueTarget returns the §10.8 playbook: the single missing_series_advance gap →
// directOp(AdvanceVisitSeries) over the series, supplying dueFor + intervalDays +
// occurrenceCount from the row so the op needs no second read.
func visitSeriesDueTarget() pkgmgr.WeaverTargetSpec {
	return pkgmgr.WeaverTargetSpec{
		TargetID: VisitSeriesDueTarget,
		Description: "An active recurring visit series always has a future next-visit date. When a visit comes due " +
			"the series records that occurrence and rolls forward to the next; paused or ended series stand " +
			"still.",
		LensRef: "visitSeriesDue",
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_series_advance": {
				Action:    "directOp",
				Operation: advanceVisitSeriesOp,
				Params: map[string]string{
					"seriesKey":       "row.entityKey",
					"dueFor":          "row.nextDueAt",
					"intervalDays":    "row.intervalDays",
					"occurrenceCount": "row.occurrenceCount",
				},
				Reads: []string{"row.entityKey"},
			},
		},
	}
}

// visitSeriesPermissions grants the seven visit-series ops at scope=any.
// StartVisitSeries / PauseVisitSeries / ResumeVisitSeries / EndVisitSeries — the
// four ops the front-desk Follow-ups tab submits (cmd/clinic-app/web/app.js) —
// plus SetVisitSeriesSite also grant `frontOfHouse`: the script's standing workplace guard confines a non-operator
// caller to a series whose provider practises at a building it worksAt (mirrors
// clinic-domain's CreateAppointment / RescheduleAppointment). Unlike those, the
// guard here is OPERATOR-EXEMPT ONLY — these ops carry no consumer/scope=self
// grant and no identifiedBy ownership backstop, so a forged authContextTarget ==
// actor cannot be exempted (see enforce_workplace in visitSeriesScript).
// SetVisitSeriesSite takes no consumer/patient scope either: the clinic site is a
// staff correction, not a patient-editable field (clinic-domain's own
// SetAppointmentSite grant makes the same call). It stops short of that op's
// third `provider` binder because this package has no provider-self machinery at
// all — every staff op here is operator-or-workplace, and adding a lone
// provider-bound leg for the site correction would be a new authority shape, not
// a mirror.
// AdvanceVisitSeries and BackfillVisitSeriesSite stay operator-only — both are
// Weaver's directOps, dispatched under the operator service-actor (the
// reminder-op idiom), never a front-desk action.
func visitSeriesPermissions() []pkgmgr.PermissionSpec {
	frontDeskOps := map[string]bool{
		startVisitSeriesOp: true, pauseVisitSeriesOp: true, resumeVisitSeriesOp: true,
		endVisitSeriesOp: true, setVisitSeriesSiteOp: true,
	}
	operatorNotes := map[string]string{
		backfillVisitSeriesSiteOp: "Grants the operator the right to submit BackfillVisitSeriesSite operations " +
			"(orchestration-internal: this package's own visitSeriesSiteBackfill directOp playbook, dispatched by " +
			"Weaver's service actor for a live series carrying no atSite link — the AdvanceVisitSeries grant idiom).",
	}
	ops := []string{
		startVisitSeriesOp, pauseVisitSeriesOp, resumeVisitSeriesOp, endVisitSeriesOp,
		setVisitSeriesSiteOp, advanceVisitSeriesOp, backfillVisitSeriesSiteOp,
	}
	perms := make([]pkgmgr.PermissionSpec, 0, len(ops))
	for _, op := range ops {
		if frontDeskOps[op] {
			perms = append(perms, pkgmgr.PermissionSpec{
				OperationType: op,
				Scope:         "any",
				Note: "Grants the operator and front-of-house staff the right to submit " + op +
					" operations (clinic recurring visit series). The script's standing workplace guard confines a " +
					"non-operator caller to a series whose provider practises at a building it worksAt.",
				GrantsTo: []string{"operator", "frontOfHouse"},
			})
			continue
		}
		note, ok := operatorNotes[op]
		if !ok {
			note = "Grants the operator the right to submit " + op + " operations (clinic recurring visit series)."
		}
		perms = append(perms, pkgmgr.PermissionSpec{
			OperationType: op,
			Scope:         "any",
			Note:          note,
			GrantsTo:      []string{"operator"},
		})
	}
	return perms
}

// visitSeriesOpMetas makes the seven visit-series ops forOperation-resolvable and
// gives the five a human triggers a full descriptor — the form, the field help,
// and the submission recipe (edge-showcase-app-design.md §3.3).
//
// Complete metadata is not the same as a rendered button: a client also has to
// resolve TargetType against something it projects. patient/visitseries carry
// PII a patient's name would put on the broadcast SYNC plane, so — unlike the
// mirror-projected types (session, provider, studio, tab, booking) — Facet
// resolves these two from the session-scoped Protected staff-worklist pane
// (cmd/facet/staff.go's /api/staff/worklist, reading visitSeriesRead below),
// never from a manifest.ent row.
//
// All five are AuthContext "standing": permissions.go grants them scope=any to
// operator + frontOfHouse, so the caller's authority is a standing role rather
// than a relationship to the target, and the client sends no authContext object
// at all (OpDispatchSpec.AuthContext's fourth case). Idiom: clinic-domain's
// SetProviderHours.
//
// Dispatch.Class is "visitseries" — the owning DDL's own CanonicalName
// (visitSeriesVertexDDL), never the vertical name — including on
// StartVisitSeries, which targets a PATIENT but mints a visitseries.
//
// Reads declare exactly what the script hydrates from `state` and no more.
// StartVisitSeries names both endpoint vertices because its own field
// documentation makes them mandatory; Pause/Resume rely on the targetField
// fallback for the series vertex, which is the only key they read.
// EndVisitSeries additionally declares its own `.series` cadence-definition
// aspect: unlike Pause/Resume it must READ before it writes (the write-once
// VisitSeriesAlreadyEnded guard, and preserving intervalDays/startAt across the
// upsert), so that key is a class-(a) declared read, not a class-(e) live one —
// its suffix off the already-target seriesKey is deterministic, unlike the
// data-derived keys below. Everything
// else these scripts reach — enforce_workplace's site walk, the dedup guard's
// prior-series .series/.paused follow-ups — is a class-(e) read off a key the
// caller cannot know in advance, which is why the read posture sanctions it
// live rather than declared. The one guard key a caller CAN derive, the
// per-(patient, provider) dedup pointer, is declared optional: it is absent on a
// first series, so requiring it would fail the common case. SetVisitSeriesSite
// declares the series vertex alone — its `site` is validated by membership in a
// class-(e) walk, not by a read of the building itself, so unlike clinic-domain's
// SetAppointmentSite it has no second key to declare.
//
// AdvanceVisitSeries and BackfillVisitSeriesSite stay bare metas. Weaver re-arms
// a due series through the first and backfills a missing site through the second;
// no human triggers either, so neither owes a descriptor.
func visitSeriesOpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{
			OperationType: startVisitSeriesOp,
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Start a visit series",
				Description: "Put a patient on a recurring visit cadence with a provider.",
				Icon:        "repeat",
				Tone:        "primary",
				SubmitLabel: "Start series",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"patientKey":{"type":"string","description":"vtx.patient.<NanoID> the series is for — auto-filled from the patient being viewed."},` +
				`"providerKey":{"type":"string","title":"Provider","x-entityRef":"provider","description":"vtx.provider.<NanoID> the series is with."},` +
				`"intervalDays":{"type":"integer","title":"Every (days)","minimum":1,"description":"Days between visits."},` +
				`"startAt":{"type":"string","format":"date-time","title":"First visit","description":"When the first visit falls due."},` +
				`"activeUntil":{"type":"string","format":"date-time","title":"Runs until","description":"When the series stops re-arming. Omit for open-ended."}},` +
				`"required":["patientKey","providerKey","intervalDays","startAt"]}`,
			FieldDescriptions: map[string]string{
				"patientKey":   "The patient this cadence is for — auto-filled by the client from the patient being viewed (dispatch.targetField), not user-entered.",
				"providerKey":  "The provider the visits are with. A front-desk caller may only name a provider practising at a building they work at.",
				"intervalDays": "How many days apart the visits fall. A patient may hold only one active series per provider at a time.",
				"startAt":      "When the first visit falls due.",
				"activeUntil":  "Optional. When the series stops re-arming. Omitted means it runs until someone ends it.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       visitSeriesVertexDDL,
				AuthContext: "standing",
				TargetField: "patientKey",
				TargetType:  "patient",
				// providerKey is picked from a name-searchable roster select, not typed —
				// staff apps have no entity-ref picker yet (internal/descriptorform/form.mjs's
				// own note), so leaving it schema-rendered would ask for a raw NanoID. The
				// caller fills it from its own picker via the companion-row template
				// (the same {context.<field>} seam lease-signing's SignRenewal/VerifyGuarantor
				// completions use), excluding it from the generic render entirely.
				ContextParams: map[string]string{"providerKey": "{context.providerKey}"},
				Reads:         []string{"{payload.patientKey}", "{payload.providerKey}"},
				OptionalReads: []string{
					"{payload.patientKey}.activeVisitSeriesWith{payload.providerKey:id}",
				},
				// The operator-role confinement probe: the workplace-exempt
				// short-circuit walks the actor's own holdsRole links to test
				// for the operator role (actor_holds_operator).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{
			OperationType: pauseVisitSeriesOp,
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Pause visit series",
				Description: "Stop a recurring visit series from falling due, without ending it.",
				Icon:        "pause",
				Tone:        "neutral",
				SubmitLabel: "Pause series",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"seriesKey":{"type":"string","description":"vtx.visitseries.<NanoID> to pause — auto-filled from the series being viewed."}},` +
				`"required":["seriesKey"]}`,
			FieldDescriptions: map[string]string{
				"seriesKey": "The series being paused — auto-filled by the client from the series being viewed (dispatch.targetField), not user-entered. A paused series stops falling due but keeps its cadence and history.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       visitSeriesVertexDDL,
				AuthContext: "standing",
				TargetField: "seriesKey",
				TargetType:  visitSeriesVertexDDL,
				VisibleWhen: &pkgmgr.OpVisibleWhenSpec{Field: "series_status", Equals: "active"},
				// The operator-role confinement probe: the workplace-exempt
				// short-circuit walks the actor's own holdsRole links to test
				// for the operator role (actor_holds_operator).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{
			OperationType: resumeVisitSeriesOp,
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Resume visit series",
				Description: "Put a paused visit series back on its cadence.",
				Icon:        "play",
				Tone:        "primary",
				SubmitLabel: "Resume series",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"seriesKey":{"type":"string","description":"vtx.visitseries.<NanoID> to resume — auto-filled from the series being viewed."}},` +
				`"required":["seriesKey"]}`,
			FieldDescriptions: map[string]string{
				"seriesKey": "The series being resumed — auto-filled by the client from the series being viewed (dispatch.targetField), not user-entered. It picks its cadence back up from where it was paused.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       visitSeriesVertexDDL,
				AuthContext: "standing",
				TargetField: "seriesKey",
				TargetType:  visitSeriesVertexDDL,
				VisibleWhen: &pkgmgr.OpVisibleWhenSpec{Field: "series_status", Equals: "paused"},
				// The operator-role confinement probe: the workplace-exempt
				// short-circuit walks the actor's own holdsRole links to test
				// for the operator role (actor_holds_operator).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{
			OperationType: endVisitSeriesOp,
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "End visit series",
				Description: "Give an open-ended visit series a stop date, so it stops re-arming.",
				Icon:        "stop-circle",
				Tone:        "destructive",
				SubmitLabel: "End series",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"seriesKey":{"type":"string","description":"vtx.visitseries.<NanoID> to end — auto-filled from the series being viewed."}},` +
				`"required":["seriesKey"]}`,
			FieldDescriptions: map[string]string{
				"seriesKey": "The series being ended — auto-filled by the client from the series being viewed (dispatch.targetField), not user-entered. Once ended, a currently-due occurrence still gets serviced once more before the series goes quiet for good.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       visitSeriesVertexDDL,
				AuthContext: "standing",
				TargetField: "seriesKey",
				TargetType:  visitSeriesVertexDDL,
				Reads:       []string{"{payload.seriesKey}.series"},
				VisibleWhen: &pkgmgr.OpVisibleWhenSpec{Field: "series_endable", Equals: true},
				// The operator-role confinement probe: the workplace-exempt
				// short-circuit walks the actor's own holdsRole links to test
				// for the operator role (actor_holds_operator).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{
			OperationType: setVisitSeriesSiteOp,
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Set visit series site",
				Description: "Record which clinic site a recurring visit series is seen at.",
				Icon:        "map-pin",
				Tone:        "primary",
				SubmitLabel: "Set site",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"seriesKey":{"type":"string","description":"vtx.visitseries.<NanoID> to site — auto-filled from the series being viewed."},` +
				`"site":{"type":"string","title":"Site","x-entityRef":"building","description":"vtx.building.<NanoID> clinic site this series is seen at."}},` +
				`"required":["seriesKey","site"]}`,
			FieldDescriptions: map[string]string{
				"seriesKey": "The series being sited — auto-filled by the client from the series being viewed (dispatch.targetField), not user-entered.",
				"site":      "The clinic site these visits happen at. Must be a site the series' provider practises at. No-op if the series already has a site; the site cannot be changed afterwards.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       visitSeriesVertexDDL,
				AuthContext: "standing",
				TargetField: "seriesKey",
				TargetType:  visitSeriesVertexDDL,
				// The series vertex alone. Unlike clinic-domain's
				// SetAppointmentSite, `site` is NOT declared: this op validates
				// it by membership in the provider's live practicesAt set — a
				// class-(e) walk off a data-derived hub — rather than by a
				// direct read of the building document, so there is no
				// site-keyed read to declare.
				Reads: []string{"{payload.seriesKey}"},
				// The operator-role confinement probe: the workplace-exempt
				// short-circuit walks the actor's own holdsRole links to test
				// for the operator role (actor_holds_operator).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{OperationType: advanceVisitSeriesOp},
		// Weaver's own site-backfill directOp. Bare, exactly like
		// AdvanceVisitSeries above: no human triggers it, so it owes no
		// descriptor — only forOperation resolvability.
		{OperationType: backfillVisitSeriesSiteOp},
	}
}
