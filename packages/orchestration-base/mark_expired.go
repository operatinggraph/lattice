package orchestrationbase

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// markExpiredDDL is the canonical name of the platform DDL handling the
// MarkExpired freshness op.
const markExpiredDDL = "freshnessMarker"

// freshnessExpiryAspectDDL is the canonical name of the aspect-type DDL that
// declares the freshnessExpiry marker aspect's shape and admits MarkExpired as
// its writer (so step-6's permittedCommands check, keyed on the MUTATION's
// class, admits the marker write).
const freshnessExpiryAspectDDL = "freshnessExpiry"

// MarkExpiredDDL returns the DDL meta-vertex declaration for the generic,
// type-agnostic MarkExpired freshness op (Contract #10 §10.4 temporal lane).
//
// MarkExpired{entityKey, targetId, expiredAt} is submitted by Weaver's temporal
// lane when an entity's projected freshUntil deadline fires. It RECORDS the
// lapse as a fact on the entity — which target's deadline fired, and at what
// instant — in a generic `freshnessExpiry` marker aspect:
//
//	vtx.<anytype>.<id>.freshnessExpiry = {
//	    expiredAt: <the latest instant ANY target lapsed on this entity>,
//	    byTarget:  { <targetId>: <the latest instant THAT target lapsed> },
//	}
//
// A convergence lens reads its own entry —
// `<anchor>.freshnessExpiry.data.byTarget.<targetId> >= <the stored deadline>` —
// so the projected row is a pure function of the subgraph and a re-projection
// reaches the same verdict whenever it runs. The write also bumps the entity's
// adjacency revision, so the marker both carries the verdict and triggers the
// reprojection that publishes it (FR58's eager re-open).
//
// Type-agnostic by construction: the entity's type is whatever `entityKey`
// carries (vtx.<type>.<id>) — the script names NO concrete type. The same single
// DDL serves every weaver-target anchor type (leaseapp here, anything later).
//
// The marker is PER TARGET. Several weaver targets share one anchor type and one
// marker slot, so `byTarget` isolates them: a fire for one target merges its own
// entry and preserves every sibling's. `expiredAt` is the monotone maximum over
// the merged `byTarget` and the marker's own standing value, so it never moves
// backwards no matter which target fires next.
//
// The merge is a read-modify-write, and the read is DECLARED: the marker key is
// dispatched in `contextHint.optionalReads` (Contract #2 §2.5 class (d) — its
// absence on a first lapse is a legitimate branch, never a fault). That
// declaration is also what SERIALIZES the merge. A declared key is hydrated at
// step 4, so Contract #3 §3.2 conditions the `update` on the revision it was
// read at: a concurrent second target's commit is a revision conflict the
// Processor absorbs by re-hydrating and re-executing this script against the
// merged document. On a marker step 4 recorded as known-absent the script emits
// a `create` instead, conditioned on that observed absence — a losing racer's
// retry re-hydrates, now sees the marker present, and takes the merge branch.
// Neither branch can lose a sibling target's entry.
//
// Idempotency under at-least-once: the temporal lane derives a §10.4
// deterministic requestId (schedule subject + fire instant), so a redelivery of
// the SAME firing collapses on the Contract #4 tracker; a NEW firing (a re-armed
// timer, a new fireAt) is a genuinely new op that merges the later instant. The
// op is safe to re-run: the merge keeps the maximum per target, so replaying a
// fire is a no-op in effect.
//
// Marker lifecycle: the freshnessExpiry marker is PERMANENT and merged in place
// on every fire — it is never tombstoned by this op. The footprint is bounded to
// exactly ONE marker aspect per entity regardless of how many freshness cycles
// or targets it serves (the merge rewrites the standing aspect; only the
// `byTarget` map grows, one entry per target). A marker outliving a converged
// entity is inert rather than harmless-because-unread: the recorded instant
// falls behind the entity's next deadline, so the comparison reads not-expired
// with no clearing write at all — which is exactly how a re-armed entity
// recovers.
func MarkExpiredDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     markExpiredDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"MarkExpired"},
		Description: "Generic freshness-marker DDL (Contract #10 §10.4 temporal lane). MarkExpired{entityKey, " +
			"targetId, expiredAt} RECORDS a lapsed freshness deadline on an entity of ANY type, in a generic " +
			"freshnessExpiry marker aspect (vtx.<type>.<id>.freshnessExpiry = {expiredAt, byTarget:{<targetId>:<instant>}}). " +
			"A convergence lens reads its own entry — byTarget.<targetId> >= the stored deadline — so the row is a " +
			"pure function of the subgraph; the write also bumps the entity's adjacency revision, so Refractor " +
			"reprojects and the stale-freshness gap re-opens (the eager re-open, FR58). The marker is PER TARGET: " +
			"the byTarget map isolates the several targets sharing one anchor, and expiredAt is the monotone " +
			"maximum over it. The merge reads the marker through a declared optionalReads key, which conditions " +
			"the update on the hydrated revision (Contract #3 §3.2) — a create on a known-absent marker, an " +
			"update otherwise, so concurrent fires serialize and no target's entry is lost. Type-agnostic: the " +
			"entity type is whatever entityKey carries; the script names no concrete type. Submitted under " +
			"Weaver's service-actor authority. The marker aspect's class is freshnessExpiry (its own aspect-type " +
			"DDL admits MarkExpired so step-6 permits the write).",
		Script: markExpiredDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"entityKey":{"type":"string","description":"vtx.<type>.<NanoID> — the entity whose deadline lapsed. Any vertex type; the freshnessExpiry marker aspect is written on it."},` +
			`"targetId":{"type":"string","description":"The weaver-target id whose deadline fired. Keys this lapse's entry in the marker's byTarget map, which is what the target's own lens reads."},` +
			`"expiredAt":{"type":"string","description":"RFC3339 instant the freshness deadline fired; normalized to canonical whole-second UTC and recorded as this target's byTarget entry."}},` +
			`"required":["entityKey","targetId","expiredAt"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.<type>.<NanoID> of the entity carrying the marker (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"entityKey": "Full vtx.<type>.<NanoID> key of the entity whose deadline lapsed. The freshnessExpiry marker aspect is written on this key; the entity's type is read from the key, never named by the DDL.",
			"targetId":  "The weaver-target id whose deadline fired. Required: it keys this lapse's entry in the marker's byTarget map, and that entry is what the target's convergence lens reads. It forms no KV key (the entityKey is the sole key source).",
			"expiredAt": "RFC3339 instant the freshness deadline fired. Normalized to canonical whole-second UTC so a lens compares it lexically against a stored deadline, and merged as the maximum for this target.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "MarkExpired — record a lapsed lease-application freshness window",
				Payload: map[string]any{
					"entityKey": "vtx.leaseapp.<applicationNanoID>",
					"targetId":  "leaseApplicationComplete",
					"expiredAt": "2026-06-18T14:00:00Z",
				},
				ExpectedOutcome: "Merges vtx.leaseapp.<applicationNanoID>.freshnessExpiry to " +
					"{expiredAt: 2026-06-18T14:00:00Z, byTarget: {leaseApplicationComplete: 2026-06-18T14:00:00Z}} — a create " +
					"when the marker was hydrated known-absent, an update conditioned on its hydrated revision otherwise — " +
					"emits orchestration.freshnessMarked, and returns primaryKey (the entity key). Any sibling target's " +
					"byTarget entry survives the merge, and expiredAt stays the maximum over them. The write bumps the " +
					"entity's adjacency revision, so Refractor reprojects the convergence row and the lens reads the " +
					"recorded lapse.",
			},
		},
	}
}

// FreshnessExpiryAspectDDL returns the aspect-type DDL that declares the generic
// freshnessExpiry marker aspect. It exists so the Processor's step-6 validator —
// which keys permittedCommands on the MUTATION document's class — admits the
// MarkExpired-written marker (whose class is freshnessExpiry). It is NOT
// sensitive (it carries only fire timestamps and weaver-target ids, no PII), so
// it attaches to a vertex of any type (the step-6 sensitiveAspectScope rule does
// not fire). Its script is declaration-only and never executes an op (the
// freshnessMarker vertexType DDL owns the MarkExpired script); it fails closed
// if dispatched.
func FreshnessExpiryAspectDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     freshnessExpiryAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"MarkExpired"},
		Description: "Generic freshness-expiry marker aspect (Contract #10 §10.4). Stored as " +
			"vtx.<type>.<NanoID>.freshnessExpiry = {expiredAt, byTarget:{<targetId>:<instant>}}, non-sensitive, " +
			"type-agnostic (attaches to any vertex). byTarget records the latest instant each weaver target's " +
			"deadline lapsed on this entity — the entry a convergence lens reads; expiredAt is the monotone " +
			"maximum over them. Written ONLY by MarkExpired (whose freshnessMarker vertexType DDL owns the " +
			"script); this aspect-type DDL exists so step-6's permittedCommands check, keyed on the mutation's " +
			"class, admits the marker write. Declaration-only: it carries no op handler.",
		Script: freshnessExpiryAspectDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"expiredAt":{"type":"string","description":"RFC3339 instant of the latest lapse recorded on this entity — the maximum over byTarget."},` +
			`"byTarget":{"type":"object","description":"weaver-target id -> the RFC3339 instant that target's deadline last lapsed on this entity. The entry a target's convergence lens reads."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"expiredAt": "RFC3339 instant of the latest lapse recorded on this entity — the monotone maximum over byTarget.",
			"byTarget":  "weaver-target id -> the RFC3339 instant that target's deadline last lapsed. Per-target so several targets sharing one anchor do not overwrite each other's verdict.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "freshnessExpiry marker aspect",
				Payload: map[string]any{
					"expiredAt": "2026-06-18T14:00:00Z",
					"byTarget": map[string]any{
						"leaseApplicationComplete": "2026-06-18T14:00:00Z",
						"leaseExpiry":              "2026-06-18T09:00:00Z",
					},
				},
				ExpectedOutcome: "Stored as vtx.<type>.<NanoID>.freshnessExpiry; merged by MarkExpired one target entry at a time, " +
					"with expiredAt held at the maximum over byTarget.",
			},
		},
	}
}

// markExpiredDDLScript handles MarkExpired. It reads two declared keys:
//
//   - the entity ROOT (ContextHint.Reads) to assert the parent exists + is alive
//     before writing the marker — the marker is non-sensitive, so step-6's
//     sensitiveAspectScope does not fire, and without this check MarkExpired
//     would happily mint a marker (and a 4-segment aspect key) on an
//     absent/tombstoned parent;
//   - the MARKER itself (ContextHint.optionalReads) to merge this target's lapse
//     into the standing byTarget map. Absence is a legitimate branch — the first
//     lapse on the entity — so it is the absence-tolerant declaration, and its
//     hydration is what conditions the write (Contract #3 §3.2 on the update,
//     the observed absence on the create).
//
// It names no concrete type (the root is checked generically by key) and is
// idempotent in effect for a given fire instant, since the merge keeps the
// maximum per target.
const markExpiredDDLScript = `
def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def parts_of(key, name, want_type):
    # Parse a VERTEX key: exactly 3 segments vtx.<type>.<NanoID>. Any other shape
    # (aspect/link key, stray tail) is rejected — the marker must attach to a
    # vertex root, and "<entityKey>.freshnessExpiry" must be a well-formed
    # 4-segment aspect key.
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

def required_target_id(p):
    # The targetId keys this lapse's entry in the marker's byTarget map, and a
    # lens reads that entry as a property hop
    # (<anchor>.freshnessExpiry.data.byTarget.<targetId>). A dot would split the
    # hop and make the entry unreadable by the very lens the record exists for,
    # so it is refused rather than written where nothing can find it.
    v = required_string(p, "targetId")
    if v.find(".") >= 0:
        fail("InvalidArgument: targetId: must not contain '.' (it is read as a single property hop); got " + v)
    return v

def vertex_alive(state, key):
    # Generic liveness on a vertex ROOT, by key — type-agnostic (entityKey may be
    # ANY vertex type). Absent from the hydrated state, a nil doc, or an isDeleted
    # tombstone all count as not-alive. NOT a ".state" aspect check (entityKey can
    # be any type and need not carry one).
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "MarkExpired":
        entity_key = required_string(p, "entityKey")
        target_id = required_target_id(p)
        # Canonical whole-second UTC (time.rfc3339_utc — pure, no clock read) so
        # the recorded instant compares LEXICALLY against a deadline stored on
        # the graph, which is what a lens does. A malformed instant fails here.
        expired_at = time.rfc3339_utc(required_string(p, "expiredAt"))
        # Validate the key shape only (type-agnostic — any type segment is
        # accepted); names no concrete type.
        parts_of(entity_key, "entityKey", "")

        # Target-existence guard: never write a marker (and a 4-segment aspect
        # key) onto an absent or tombstoned parent. The op hydrates [entityKey]
        # (ContextHint.Reads), so the root is in state. A stale firing whose
        # entity was deleted after the timer armed fails closed here rather than
        # leaving a dangling marker on a non-existent vertex.
        if not vertex_alive(state, entity_key):
            fail("NotFound: entityKey " + entity_key + " is absent or tombstoned; no marker written")

        marker_key = entity_key + ".freshnessExpiry"

        # The marker is dispatched in ContextHint.optionalReads, so a standing
        # one is hydrated into state and an absent one is recorded known-absent
        # (it is simply not in state). The two cases take different mutation
        # kinds, and each is conditioned by the read:
        #
        #   present -> update with NO explicit expectedRevision, which the
        #              Processor conditions on the revision the key was hydrated
        #              at; a racing sibling target conflicts and is re-executed
        #              against the merged document.
        #   absent  -> create, conditioned on the observed absence; a losing
        #              racer re-hydrates, finds the marker, and merges.
        standing = None
        if marker_key in state:
            standing = state[marker_key]

        by_target = {}
        prior_expired_at = ""
        if standing != None and hasattr(standing, "data") and standing.data != None:
            existing = standing.data.get("byTarget")
            if existing != None:
                for k in existing:
                    v = existing[k]
                    if type(v) == type("") and len(v) > 0:
                        by_target[k] = v
            pe = standing.data.get("expiredAt")
            if type(pe) == type("") and len(pe) > 0:
                prior_expired_at = pe

        # Per target, keep the LATEST instant: a fire at or after the recorded
        # one advances the entry, an earlier one (a replay, a firing overtaken
        # by a later cycle) leaves it alone.
        recorded = by_target.get(target_id)
        if recorded == None or expired_at > recorded:
            by_target[target_id] = expired_at

        # expiredAt is the maximum over every recorded lapse — the entity-wide
        # "latest anything expired here". Folding the standing value into the
        # maximum makes the field monotone for any marker document, whatever
        # its byTarget holds.
        latest = prior_expired_at
        for k in by_target:
            if by_target[k] > latest:
                latest = by_target[k]

        document = {"class": "freshnessExpiry", "vertexKey": entity_key,
                    "localName": "freshnessExpiry", "isDeleted": False,
                    "data": {"expiredAt": latest, "byTarget": by_target}}
        kind = "update"
        if standing == None:
            kind = "create"
        mutations = [{"op": kind, "key": marker_key, "document": document}]
        events = [{"class": "orchestration.freshnessMarked",
                   "data": {"entityKey": entity_key, "targetId": target_id,
                            "expiredAt": expired_at}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": entity_key}}

    fail("freshnessMarker DDL: unknown operationType: " + ot)
`

// freshnessExpiryAspectDDLScript is the declaration-only Starlark for the
// freshnessExpiry aspect-type DDL. The marker aspect is written by the
// freshnessMarker vertexType DDL's MarkExpired branch; this aspect DDL is a
// step-6 gate only, never an op handler — it fails closed if dispatched.
const freshnessExpiryAspectDDLScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`
