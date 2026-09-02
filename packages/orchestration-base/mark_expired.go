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
// entity is inert: the recorded instant falls behind the entity's next deadline,
// so the comparison reads not-expired with no clearing write at all — which is
// exactly how a re-armed entity recovers.
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
			"maximum over byTarget and the marker's own standing value. The merge reads the marker through a " +
			"declared optionalReads key, which conditions the update on the hydrated revision " +
			"(Contract #3 §3.2) — a create on a known-absent marker, an " +
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
				Name: "MarkExpired — record a lapsed background-check freshness window",
				Payload: map[string]any{
					"entityKey": "vtx.service.<checkNanoID>",
					"targetId":  "backgroundCheckFreshness",
					"expiredAt": "2026-06-18T14:00:00Z",
				},
				ExpectedOutcome: "Merges vtx.service.<checkNanoID>.freshnessExpiry to " +
					"{expiredAt: 2026-06-18T14:00:00Z, byTarget: {backgroundCheckFreshness: 2026-06-18T14:00:00Z}} — a create " +
					"when the marker was hydrated known-absent, an update conditioned on its hydrated revision otherwise — " +
					"emits orchestration.freshnessMarked, and returns primaryKey (the entity key). Any sibling target's " +
					"byTarget entry survives the merge, and expiredAt stays the maximum over them. The write bumps the " +
					"entity's adjacency revision, so Refractor reprojects every convergence row that reads this entity — " +
					"the check's own, and each neighbour anchored elsewhere that walks to it — and their lenses read the " +
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
			"maximum over byTarget and the marker's own standing value. Written ONLY by MarkExpired (whose freshnessMarker vertexType DDL owns the " +
			"script); this aspect-type DDL exists so step-6's permittedCommands check, keyed on the mutation's " +
			"class, admits the marker write. Declaration-only: it carries no op handler.",
		Script: freshnessExpiryAspectDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"expiredAt":{"type":"string","description":"RFC3339 instant of the latest lapse recorded on this entity — the maximum over byTarget and the aspect's own standing value."},` +
			`"byTarget":{"type":"object","description":"weaver-target id -> the RFC3339 instant that target's deadline last lapsed on this entity. The entry a target's convergence lens reads."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"expiredAt": "RFC3339 instant of the latest lapse recorded on this entity — the monotone maximum over byTarget and the aspect's own standing value.",
			"byTarget":  "weaver-target id -> the RFC3339 instant that target's deadline last lapsed. Per-target so several targets sharing one anchor do not overwrite each other's verdict.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "freshnessExpiry marker aspect",
				Payload: map[string]any{
					"expiredAt": "2026-06-18T14:00:00Z",
					"byTarget": map[string]any{
						"appointmentReminders": "2026-06-18T14:00:00Z",
						"pastDueAppointments":  "2026-06-18T09:00:00Z",
					},
				},
				ExpectedOutcome: "Stored as vtx.<type>.<NanoID>.freshnessExpiry; merged by MarkExpired one target entry at a time, " +
					"with expiredAt held at the maximum over byTarget and the aspect's own standing value.",
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

TARGET_ID_HEAD = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz_"
TARGET_ID_TAIL = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz_0123456789"

def required_target_id(p):
    # The targetId keys this lapse's entry in the marker's byTarget map, and a
    # lens reads that entry as a single cypher property hop
    # (<anchor>.freshnessExpiry.data.byTarget.<targetId>). That position takes an
    # IDENTIFIER — [A-Za-z_][A-Za-z0-9_]* — so anything else (a dot splitting the
    # hop, a dash, a space) names an entry no lens can express a read for. Held
    # to the grammar rather than written where nothing can find it.
    v = required_string(p, "targetId")
    if TARGET_ID_HEAD.find(v[0]) < 0:
        fail("InvalidArgument: targetId: must be a cypher identifier [A-Za-z_][A-Za-z0-9_]* (it is read as a single property hop); got " + v)
    for i in range(1, len(v)):
        if TARGET_ID_TAIL.find(v[i]) < 0:
            fail("InvalidArgument: targetId: must be a cypher identifier [A-Za-z_][A-Za-z0-9_]* (it is read as a single property hop); got " + v)
    return v

DAYS_IN_MONTH = [31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
DIGITS = "0123456789"

def digits_at(s, start, n):
    # The integer value of the n characters at s[start:], or None when they are
    # not all ASCII digits. Starlark cannot catch a failing builtin, so every
    # numeric read a guarded parse rests on has to be a predicate, not a cast.
    v = 0
    for i in range(start, start + n):
        d = DIGITS.find(s[i])
        if d < 0:
            return None
        v = v * 10 + d
    return v

def zone_ok(s):
    # The tail after "<date>T<hh>:<mm>:<ss>": an optional fractional part, then
    # either a Z or a ±hh:mm offset.
    i = 19
    if i < len(s) and s[i] == ".":
        i = i + 1
        n = 0
        for j in range(i, len(s)):
            if not s[j].isdigit():
                break
            n = n + 1
        if n == 0:
            return False
        i = i + n
    rest = s[i:]
    if rest == "Z" or rest == "z":
        return True
    if len(rest) != 6 or rest[3] != ":":
        return False
    if rest[0] != "+" and rest[0] != "-":
        return False
    oh = digits_at(rest, 1, 2)
    om = digits_at(rest, 4, 2)
    return oh != None and om != None and oh <= 23 and om <= 59

def rfc3339_or_none(v):
    # Normalise a STORED instant to canonical whole-second UTC for comparison,
    # or report it uncomparable. This is the guarded form of time.rfc3339_utc:
    # that builtin FAILS the whole operation on a malformed instant, and a value
    # written into the graph by something other than this op must never be able
    # to do that — so the string is validated in full first (shape, field ranges,
    # days-in-month, zone) and simply reported unusable otherwise. The caller
    # then orders as though the value were absent, and carries it through
    # verbatim rather than deleting it.
    if type(v) != type("") or len(v) < 20:
        return None
    if v[4] != "-" or v[7] != "-" or (v[10] != "T" and v[10] != "t"):
        return None
    if v[13] != ":" or v[16] != ":":
        return None
    year = digits_at(v, 0, 4)
    month = digits_at(v, 5, 2)
    day = digits_at(v, 8, 2)
    hour = digits_at(v, 11, 2)
    minute = digits_at(v, 14, 2)
    second = digits_at(v, 17, 2)
    if year == None or month == None or day == None:
        return None
    if hour == None or minute == None or second == None:
        return None
    if month < 1 or month > 12 or hour > 23 or minute > 59 or second > 60:
        return None
    last_day = DAYS_IN_MONTH[month - 1]
    if month == 2 and ((year % 4 == 0 and year % 100 != 0) or year % 400 == 0):
        last_day = 29
    if day < 1 or day > last_day:
        return None
    if not zone_ok(v):
        return None
    return time.rfc3339_utc(v)

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
        standing_data = None
        if standing != None and hasattr(standing, "data") and standing.data != None:
            standing_data = standing.data
        # A TOMBSTONED marker's per-target entries are stale by construction —
        # whatever tombstoned it declared them gone — so they do not resurrect;
        # only its expiredAt is folded on, to keep that field monotone across the
        # revival. The mutation stays an update (the key still exists) and clears
        # the tombstone.
        standing_tombstoned = standing != None and hasattr(standing, "isDeleted") and standing.isDeleted

        by_target = {}
        if standing_data != None and not standing_tombstoned:
            existing = standing_data.get("byTarget")
            if existing != None:
                # Every entry is carried through VERBATIM, including one this
                # cannot order: another target's record is not this operation's
                # to edit or drop, and an unreadable value is a repair for
                # whoever wrote it, not a deletion for whoever fires next.
                for k in existing:
                    by_target[k] = existing[k]

        prior_expired_at = ""
        if standing_data != None:
            pe = rfc3339_or_none(standing_data.get("expiredAt"))
            if pe != None:
                prior_expired_at = pe

        # Per target, keep the LATEST instant: a fire at or after the recorded
        # one advances the entry, an earlier one (a replay, a firing overtaken
        # by a later cycle) leaves it alone. The recorded value is normalised
        # before the comparison — a stored offset form orders wrongly against a
        # UTC one read byte for byte — and an uncomparable one orders as absent,
        # so the firing's own instant takes the slot.
        recorded = rfc3339_or_none(by_target.get(target_id))
        if recorded == None or expired_at > recorded:
            by_target[target_id] = expired_at

        # expiredAt is the maximum over byTarget AND the marker's own standing
        # value — the entity-wide "latest anything expired here". Folding the
        # standing value in keeps the field monotone for any marker document,
        # including one that carries no byTarget entry for a target that lapsed.
        # An entry that does not normalise is skipped here and kept above.
        latest = prior_expired_at
        for k in by_target:
            n = rfc3339_or_none(by_target[k])
            if n != None and n > latest:
                latest = n

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
