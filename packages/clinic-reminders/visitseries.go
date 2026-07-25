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
//	vtx.patient.<id>.activeVisitSeriesWith<providerId>  (class visitSeriesGuard) = {seriesKey, activeUntil?}
//	  per-(patient,provider) uniqueness guard, StartVisitSeries-only (create or, once its
//	  denormalized activeUntil has passed, OCC-revive) — a paused series still holds the
//	  guard (resume it rather than starting a duplicate)
//
//	op StartVisitSeries{patientKey, providerKey, intervalDays, startAt, activeUntil?}
//	  rejects ActiveVisitSeriesExists if the pair already holds an unexpired guard
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
	visitSeriesGuardAspectDDL  = "visitSeriesGuard"

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
		visitSeriesGuardAspectTypeDDL(),
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
			"classed, rejects ActiveVisitSeriesExists if the pair already holds an active series (see the " +
			"visitSeriesGuard aspect-type DDL), then mints the series + its .series {intervalDays, startAt, " +
			"activeUntil?} + .progress {nextDueAt: startAt, occurrenceCount: 0} aspects, and writes the forPatient + " +
			"withProvider links (Contract #1 §1.1 — the series is the later-arriving source). PauseVisitSeries / " +
			"ResumeVisitSeries toggle the .paused {value} aspect (absent = not paused) — a paused series still holds " +
			"its patient+provider pair (resume it; StartVisitSeries will not treat the pair as free). " +
			"AdvanceVisitSeries is the directOp the visitSeriesDue §10.8 " +
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
			"patientKey":      "Full vtx.patient.<NanoID> key the series is for. StartVisitSeries validates it is alive + class=patient, writes the forPatient link, and claims a per-provider activeVisitSeriesWith guard aspect on it (ActiveVisitSeriesExists if the pair already holds an active one).",
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

# Front-desk workplace confinement — the frontOfHouse standing guard for the
# staff visit-series ops (Start/Pause/Resume). Mirrors clinic-domain's
# CreateAppointment confinement (ddls.go) — same helpers, resolved through the
# same withProvider -> practicesAt edge clinicAppointmentsRead anchors on — with
# ONE deliberate divergence: require_workplace here is OPERATOR-EXEMPT ONLY. The
# appointment version also exempts authContextTarget == op.actor (the consumer
# self-book path), safe there only because a downstream identifiedBy patient
# binding backstops it. These ops carry no consumer/scope=self grant and no such
# backstop, and a scope=any caller can attach ANY target (the Gateway forwards
# authContext verbatim; step 3 authorizes scope=any without inspecting target),
# so a self exemption would let a front-desk actor forge target==actor and skip
# confinement entirely — hence root is the only exemption.
WORKPLACE_ROLE_PAGE_LIMIT = 50
WORKPLACE_PARENT_PAGE_LIMIT = 20
WORKPLACE_MAX_DEPTH = 8

def actor_holds_operator(actor_key):
    # Root is proven from the GRAPH, mirroring the kernel's own root-grant lens
    # (internal/bootstrap/lenses.go: MATCH (identity)-[:holdsRole]->(role) WHERE
    # role.canonicalName.data.value = 'operator') — never a compile-time constant
    # (the primordial operator id is loaded at runtime, invisible to this
    # package-init script text).
    # read-posture: (e) relation=holdsRole epoch=none — an identity holds few
    # roles, so this is never a keyspace scan; a role granted concurrently can
    # only widen authority, and the confined branch is the safe one.
    page, _ = kv.Links(actor_key, "holdsRole", "out", None, WORKPLACE_ROLE_PAGE_LIMIT)
    for lk in page:
        if lk.isDeleted:
            continue
        # read-posture: (e) per-candidate follow-up read off the enumeration above
        # (data-derived key — the role is unknown until it resolves).
        cn = kv.Read(lk.targetVertex + ".canonicalName")
        if cn != None and not cn.isDeleted and cn.data.get("value") == "operator":
            return True
    return False

def worksAt_covers(actor_id, location_key):
    # Walks the location's containedIn chain upward, testing the actor's
    # deterministic worksAt link at each level; the location itself is tested
    # first, so staff wired to an exact unit OR to a containing building both
    # match. A tombstoned link is ABSENT — kv.Read returns the tombstone doc
    # (not None), so the '<> None' cafe/clinic form would let a moved-on staff
    # member keep writing; test isDeleted explicitly.
    cur = location_key
    for _ in range(WORKPLACE_MAX_DEPTH):
        if cur == None:
            return False
        parts = cur.split(".")
        if len(parts) != 3:
            return False
        # read-posture: (e) per-candidate follow-up read off the containedIn
        # enumeration below (the ancestor chain is not knowable client-side).
        lnk = kv.Read("lnk.identity." + actor_id + ".worksAt." + parts[1] + "." + parts[2])
        if lnk != None and not lnk.isDeleted:
            return True
        # read-posture: (e) relation=containedIn epoch=none — a location has at
        # most a few parents; containment is provisioned topology, not written
        # concurrently with this op.
        page, _ = kv.Links(cur, "containedIn", "out", None, WORKPLACE_PARENT_PAGE_LIMIT)
        nxt = None
        for lk in page:
            if not lk.isDeleted:
                nxt = lk.targetVertex
        cur = nxt
    return False

def sites_for_provider(provider):
    # A provider's practicesAt sites — the buildings clinicAppointmentsRead
    # anchors its workplace read token on, so write confinement and read
    # confinement resolve through exactly the same edge. provider may be None
    # (a series whose withProvider link is absent), which yields []. ALL sites
    # are returned: staff at any one of a provider's buildings are equally
    # entitled to that provider's series.
    if provider == None:
        return []
    # read-posture: (e) relation=practicesAt epoch=none (a site assigned
    # concurrently with this write can only WIDEN the confining set, never narrow
    # it, so the confined branch stays the safe one) — a per-candidate follow-up
    # enumeration off the provider (data-derived hub); a provider practises at a
    # handful of sites at most.
    spage, _ = kv.Links(provider, "practicesAt", "out")
    sites = []
    for lk in spage:
        if not lk.isDeleted:
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

def require_workplace(location_keys, what):
    # OPERATOR-EXEMPT ONLY (see the block comment above): no authContextTarget ==
    # op.actor self exemption, because these ops have no consumer self path to
    # protect and no identifiedBy backstop to make a self exemption safe.
    # location_keys is a LIST — covering ANY one authorizes (a provider may
    # practise at several buildings, and staff at either are equally entitled);
    # an EMPTY list (a target whose location cannot be resolved) is a DENIAL for
    # anyone but an operator, so unwired topology fails CLOSED.
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
        require_workplace(sites_for_provider(provider_key), "cannot start a visit series with provider " + provider_key)

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
        require_workplace(sites_for_provider(series_provider(series_key)), "cannot change visit series " + series_key)
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

// visitSeriesPermissions grants the four visit-series ops at scope=any.
// StartVisitSeries / PauseVisitSeries / ResumeVisitSeries — the three ops the
// front-desk Follow-ups tab submits (cmd/clinic-app/web/app.js) — also grant
// `frontOfHouse`: the script's standing workplace guard confines a non-operator
// caller to a series whose provider practises at a building it worksAt (mirrors
// clinic-domain's CreateAppointment / RescheduleAppointment). Unlike those, the
// guard here is OPERATOR-EXEMPT ONLY — these ops carry no consumer/scope=self
// grant and no identifiedBy ownership backstop, so a forged authContextTarget ==
// actor cannot be exempted (see require_workplace in visitSeriesScript).
// AdvanceVisitSeries stays operator-only — it is Weaver's directOp, dispatched
// under the operator service-actor (the reminder-op idiom), never a front-desk
// action.
func visitSeriesPermissions() []pkgmgr.PermissionSpec {
	frontDeskOps := map[string]bool{startVisitSeriesOp: true, pauseVisitSeriesOp: true, resumeVisitSeriesOp: true}
	ops := []string{startVisitSeriesOp, pauseVisitSeriesOp, resumeVisitSeriesOp, advanceVisitSeriesOp}
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
