package clinicreminders

import "github.com/asolgan/lattice/internal/pkgmgr"

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
//
//	op StartVisitSeries{patientKey, providerKey, intervalDays, startAt, activeUntil?}
//	op PauseVisitSeries{seriesKey} / ResumeVisitSeries{seriesKey}
//	op AdvanceVisitSeries{seriesKey, dueFor, intervalDays, occurrenceCount?}  (the directOp the playbook dispatches)
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

	startVisitSeriesOp   = "StartVisitSeries"
	pauseVisitSeriesOp   = "PauseVisitSeries"
	resumeVisitSeriesOp  = "ResumeVisitSeries"
	advanceVisitSeriesOp = "AdvanceVisitSeries"

	// VisitSeriesDueTarget is the §10.8 TargetID == the visitSeriesDue lens's
	// OutputKeyPattern prefix (the §10.2↔§10.8 binding Weaver reads).
	VisitSeriesDueTarget = "visitSeriesDue"
)

// visitSeriesDDLs returns the visit-series vertex type (one script owning all four
// operationTypes, mirroring clinic-domain's appointment DDL) + its three aspect-type
// write gates.
func visitSeriesDDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		visitSeriesVertexTypeDDL(),
		visitSeriesDefinitionAspectTypeDDL(),
		visitSeriesProgressAspectTypeDDL(),
		visitSeriesPausedAspectTypeDDL(),
	}
}

func visitSeriesVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName: visitSeriesVertexDDL,
		Class:         "meta.ddl.vertexType",
		PermittedCommands: []string{
			startVisitSeriesOp, pauseVisitSeriesOp, resumeVisitSeriesOp, advanceVisitSeriesOp,
		},
		Description: "Clinic recurring visit series DDL. Vertex shape: vtx.visitseries.<NanoID>, class=visitseries, " +
			"root data = {} (minimal, D5). StartVisitSeries validates patientKey/providerKey are alive + correctly " +
			"classed, mints the series + its .series {intervalDays, startAt, activeUntil?} + .progress {nextDueAt: " +
			"startAt, occurrenceCount: 0} aspects, and writes the forPatient + withProvider links (Contract #1 §1.1 — " +
			"the series is the later-arriving source). PauseVisitSeries / ResumeVisitSeries toggle the .paused " +
			"{value} aspect (absent = not paused). AdvanceVisitSeries is the directOp the visitSeriesDue §10.8 " +
			"playbook dispatches when missing_series_advance opens: it rolls .progress forward — lastOccurrenceAt = dueFor (the " +
			"deadline just serviced, NOT $now — keeps the cadence on a fixed grid), nextDueAt = dueFor + " +
			"intervalDays·days, occurrenceCount + 1 — re-arming the next occurrence. Reads [seriesKey] to " +
			"liveness-guard the parent for all four commands.",
		Script: visitSeriesScript,
		InputSchema: `{"type":"object","properties":` +
			`{"patientKey":{"type":"string","description":"vtx.patient.<NanoID> the series is for (StartVisitSeries; required, validated alive). The caller MUST list it in ContextHint.Reads."},` +
			`"providerKey":{"type":"string","description":"vtx.provider.<NanoID> the series is with (StartVisitSeries; required, validated alive). The caller MUST list it in ContextHint.Reads."},` +
			`"intervalDays":{"type":"integer","description":"Days between occurrences (StartVisitSeries; required, positive)."},` +
			`"startAt":{"type":"string","description":"RFC3339 instant of the first occurrence (StartVisitSeries; required)."},` +
			`"activeUntil":{"type":"string","description":"RFC3339 instant the series stops re-arming past (StartVisitSeries; optional — absent means no end)."},` +
			`"seriesKey":{"type":"string","description":"vtx.visitseries.<NanoID> of an existing series (PauseVisitSeries/ResumeVisitSeries/AdvanceVisitSeries; required, validated alive). The caller MUST list it in ContextHint.Reads."},` +
			`"dueFor":{"type":"string","description":"The .progress.nextDueAt deadline this advance is servicing (AdvanceVisitSeries; the playbook supplies row.nextDueAt)."},` +
			`"occurrenceCount":{"type":"integer","description":"The series' current occurrence count before this advance (AdvanceVisitSeries; the playbook supplies row.occurrenceCount; defaults 0 if omitted)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.visitseries.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"patientKey":      "Full vtx.patient.<NanoID> key the series is for. StartVisitSeries validates it is alive + class=patient and writes the forPatient link.",
			"providerKey":     "Full vtx.provider.<NanoID> key the series is with. StartVisitSeries validates it is alive + class=provider and writes the withProvider link.",
			"intervalDays":    "Days between occurrences. Stored on .series and re-supplied by the visitSeriesDue playbook on every AdvanceVisitSeries so the roll-forward math needs no extra read.",
			"startAt":         "RFC3339 instant of the first occurrence. Seeds .progress.nextDueAt (the first deadline anchors on startAt, not an interval offset).",
			"activeUntil":     "Optional RFC3339 instant past which the series stops re-arming (clean termination — no cancel op needed). Absent means the series never ends on its own.",
			"seriesKey":       "Full vtx.visitseries.<NanoID> key of an existing series.",
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
				Name: "AdvanceVisitSeries — roll the series forward one occurrence",
				Payload: map[string]any{
					"seriesKey": "vtx.visitseries.<NanoID>", "dueFor": "2026-08-01T09:00:00Z",
					"intervalDays": 30, "occurrenceCount": 0,
				},
				ExpectedOutcome: "Validates the series is alive, then writes .progress {lastOccurrenceAt: dueFor, " +
					"nextDueAt: dueFor + 30 days, occurrenceCount:1}. Re-runs cleanly (idempotent in effect — the " +
					"MarkExpired / reminder-marker idiom).",
			},
		},
	}
}

// visitSeriesDefinitionAspectTypeDDL declares the .series cadence definition —
// written once by StartVisitSeries, never edited (no SetVisitSeries op; changing a
// cadence is pause + start a new series). NON-sensitive: dates + an interval, no PHI
// (the clinical reason a series exists is out of scope here, same posture as
// followUpReminder's non-PHI marker).
func visitSeriesDefinitionAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     visitSeriesAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{startVisitSeriesOp},
		Description: "Visit-series cadence definition aspect (clinic-reminders). Stored as " +
			"vtx.visitseries.<NanoID>.series (class visitSeriesDefinition) = {intervalDays, startAt, activeUntil?}. " +
			"Non-sensitive. Written ONLY by StartVisitSeries (whose visitseries vertexType DDL owns the script); " +
			"this aspect-type DDL is the step-6 write gate. Declaration-only: no op handler.",
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
    if parts[1] != want_type:
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

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "StartVisitSeries":
        patient_key = required_string(p, "patientKey")
        parts_of(patient_key, "patientKey", "patient")
        provider_key = required_string(p, "providerKey")
        parts_of(provider_key, "providerKey", "provider")
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

        series_id = nanoid.new()
        series_key = "vtx.visitseries." + series_id

        series_data = {"intervalDays": interval_days, "startAt": start_at}
        if active_until != None:
            series_data["activeUntil"] = active_until
        progress_data = {"nextDueAt": start_at, "occurrenceCount": 0}

        # forPatient / withProvider: the series (later-arriving) is the source, the
        # pre-existing patient / provider is the target (Contract #1 §1.1).
        # Sentences: "visitseries forPatient patient", "visitseries withProvider
        # provider".
        for_patient_lnk = "lnk.visitseries." + series_id + ".forPatient.patient." + patient_key.split(".")[2]
        with_provider_lnk = "lnk.visitseries." + series_id + ".withProvider.provider." + provider_key.split(".")[2]

        mutations = [
            make_vtx(series_key, "visitseries", {}),
            make_aspect(series_key, "series", "visitSeriesDefinition", series_data),
            make_aspect(series_key, "progress", "visitSeriesProgress", progress_data),
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
        paused = (ot == "PauseVisitSeries")
        mutations = [make_aspect_upsert(series_key, "paused", "visitSeriesPaused", {"value": paused})]
        events = [{"class": "clinic.visitSeriesPausedChanged", "data": {"seriesKey": series_key, "paused": paused}}]
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
//   - freshUntil = nextDueAt WHILE active AND nextDueAt > $now (a future wake-up
//     arming Weaver's @at temporal lane).
//   - missing_series_advance = active AND nextDueAt <= $now (the violating row the playbook
//     converges via AdvanceVisitSeries).
//
// Unlike the one-shot reminders, convergence here does NOT clear the gate to
// permanently false — AdvanceVisitSeries rewrites nextDueAt to a NEW future
// deadline, so the row re-projects PENDING (not due, freshUntil re-armed) rather
// than SENT. That is the "roll" — the series never fully converges while active;
// it just keeps re-arming its own next wake-up.
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
  CASE WHEN (s.paused.data.value <> true) AND ((s.series.data.activeUntil = null) OR (s.progress.data.nextDueAt <= s.series.data.activeUntil)) AND (s.progress.data.nextDueAt > $now) THEN s.progress.data.nextDueAt ELSE null END AS freshUntil,
  ((s.paused.data.value <> true) AND ((s.series.data.activeUntil = null) OR (s.progress.data.nextDueAt <= s.series.data.activeUntil)) AND (s.progress.data.nextDueAt <= $now)) AS missing_series_advance,
  ((s.paused.data.value <> true) AND ((s.series.data.activeUntil = null) OR (s.progress.data.nextDueAt <= s.series.data.activeUntil)) AND (s.progress.data.nextDueAt <= $now)) AS violating`

// visitSeriesReadLens is the PATIENT-anchored protected Postgres read model for
// the recurring-visit-series view (D1.5, mirroring clinic-domain's
// clinicAppointmentsRead). cmd/clinic-app's handleMyVisitSeries reads it as the
// patient's own view: RLS scopes the read to the verified JWT subject. The
// clinic-wide STAFF worklist reads THIS SAME table via handleStaffVisitSeries,
// under the reserved WildcardAnchor grant (no separate staff projection
// needed — the same mechanism handleStaffAppointments uses against
// clinicAppointmentsRead).
//
// forPatient is a REQUIRED match (the anchor walk) so a series with no
// patient link projects NO row — fail-closed, mirroring
// clinicAppointmentsReadSpec's REQUIRED forPatient walk. withProvider stays
// OPTIONAL: a display-only neighbour, not the anchor. active / next_due_at /
// interval_days / occurrence_count are the same display columns the
// unprotected visitSeriesDue lens carries; the Weaver-dispatch machinery
// columns (freshUntil, missing_series_advance, violating) are NOT projected
// here — this is a read model, not a convergence target.
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
			{Name: "provider_key", Type: "text"},
			{Name: "provider_name", Type: "text"},
			{Name: "provider_specialty", Type: "text"},
			{Name: "interval_days", Type: "integer"},
			{Name: "next_due_at", Type: "text"},
			{Name: "occurrence_count", Type: "integer"},
			{Name: "active", Type: "boolean"},
		},
	}
}

// visitSeriesReadSpec is the PATIENT-anchored protected Postgres read model's
// cypher (D1.5). Same active/nextDueAt derivation as visitSeriesDueSpec, minus
// the freshUntil/missing_series_advance/violating dispatch columns.
const visitSeriesReadSpec = `MATCH (s:visitseries)
MATCH (s)-[:forPatient]->(p:patient)
OPTIONAL MATCH (s)-[:withProvider]->(pr:provider)
RETURN
  nanoIdFromKey(s.key)          AS series_id,
  s.key                         AS entity_key,
  p.key                         AS patient_key,
  p.demographics.data.fullName  AS patient_name,
  pr.key                        AS provider_key,
  pr.profile.data.fullName      AS provider_name,
  pr.profile.data.specialty     AS provider_specialty,
  s.series.data.intervalDays    AS interval_days,
  s.progress.data.nextDueAt     AS next_due_at,
  s.progress.data.occurrenceCount AS occurrence_count,
  ((s.paused.data.value <> true) AND ((s.series.data.activeUntil = null) OR (s.progress.data.nextDueAt <= s.series.data.activeUntil))) AS active,
  [nanoIdFromKey(p.key)]        AS authz_anchors
`

// visitSeriesDueTarget returns the §10.8 playbook: the single missing_series_advance gap →
// directOp(AdvanceVisitSeries) over the series, supplying dueFor + intervalDays +
// occurrenceCount from the row so the op needs no second read.
func visitSeriesDueTarget() pkgmgr.WeaverTargetSpec {
	return pkgmgr.WeaverTargetSpec{
		TargetID: VisitSeriesDueTarget,
		LensRef:  "visitSeriesDue",
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

// visitSeriesPermissions grants the four visit-series ops to the operator role
// (scope any) — StartVisitSeries/Pause/Resume are staff-driven (the clinic-domain
// Create*/SetAppointmentStatus idiom); AdvanceVisitSeries is Weaver's directOp
// (dispatched under operator service-actor authority, the reminder-op idiom).
func visitSeriesPermissions() []pkgmgr.PermissionSpec {
	ops := []string{startVisitSeriesOp, pauseVisitSeriesOp, resumeVisitSeriesOp, advanceVisitSeriesOp}
	perms := make([]pkgmgr.PermissionSpec, 0, len(ops))
	for _, op := range ops {
		perms = append(perms, pkgmgr.PermissionSpec{
			OperationType: op,
			Scope:         "any",
			Note:          "Grants the operator the right to submit " + op + " operations (clinic recurring visit series).",
			GrantsTo:      []string{"operator"},
		})
	}
	return perms
}

// visitSeriesOpMetas makes the four visit-series ops forOperation-resolvable for
// discoverability (Loupe's op-submit forms).
func visitSeriesOpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{OperationType: startVisitSeriesOp},
		{OperationType: pauseVisitSeriesOp},
		{OperationType: resumeVisitSeriesOp},
		{OperationType: advanceVisitSeriesOp},
	}
}
