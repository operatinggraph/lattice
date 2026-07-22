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
// MarkExpired{entityKey, targetId?, expiredAt} is submitted by Weaver's temporal
// lane when an entity's projected freshUntil deadline fires. Its sole job is to
// RE-TOUCH the entity so Refractor reprojects its convergence row and a
// freshness predicate (validUntil > $now) re-evaluates against the new wall
// clock — re-opening a stale-freshness gap (the eager re-open, FR58). It writes
// a generic `freshnessExpiry` marker aspect on the entity:
//
//	vtx.<anytype>.<id>.freshnessExpiry = { expiredAt }
//
// Type-agnostic by construction: the entity's type is whatever `entityKey`
// carries (vtx.<type>.<id>) — the script names NO concrete type. The same single
// DDL serves every weaver-target anchor type (leaseapp here, anything later).
//
// The marker write is an UNCONDITIONED `update` (op:"update", NO
// expectedRevision): create-if-absent / overwrite-if-present. This is
// load-bearing for the eager re-open across MULTIPLE freshness cycles — a
// `create` would conflict on the existing marker on the SECOND lapse and the
// batch would be rejected, so the second lapse would not reproject and the eager
// re-open would silently become a one-shot. Every unconditioned put bumps the KV
// revision → fresh CDC → reprojection every cycle (the value carries the
// per-fire expiredAt, so it changes each fire too).
//
// Idempotency under at-least-once: the temporal lane derives a §10.4
// deterministic requestId (schedule subject + fire instant), so a redelivery of
// the SAME firing collapses on the Contract #4 tracker; a NEW firing (a re-armed
// timer, a new fireAt) is a genuinely new op that overwrites the marker with the
// new expiredAt. The op is safe to re-run (the unconditioned overwrite is
// idempotent in effect for the same fire instant).
//
// The marker aspect is read by NOTHING the lens projects — it exists only to
// trigger the anchor reprojection (the projection fan-out is a broad adjacency
// BFS that over-reprojects, never under-reprojects, so an unread anchor-aspect
// write reliably re-runs the cypher with a fresh $now).
//
// Marker lifecycle (intentional): the freshnessExpiry marker is PERMANENT and
// OVERWRITTEN in place on every fire — it is never tombstoned by this op. The
// footprint is therefore bounded to exactly ONE marker aspect per entity
// regardless of how many freshness cycles it lives through (the unconditioned
// update overwrites the standing aspect; it does not accumulate). The marker
// outliving a converged entity is harmless (it is read by nothing). Tombstoning
// the marker once an entity converges to remove the dangling aspect is a
// deliberate Phase-3 refinement, out of scope here — it would buy only cleanup,
// not correctness, and would add a convergence-edge write the current design
// does not need.
func MarkExpiredDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     markExpiredDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"MarkExpired"},
		Description: "Generic freshness-marker DDL (Contract #10 §10.4 temporal lane). MarkExpired{entityKey, " +
			"targetId?, expiredAt} re-touches an entity of ANY type by writing a generic freshnessExpiry marker " +
			"aspect (vtx.<type>.<id>.freshnessExpiry = {expiredAt}) so Refractor reprojects the entity's " +
			"convergence row and a freshness predicate (validUntil > $now) re-evaluates against the new wall " +
			"clock — re-opening a stale-freshness gap (the eager re-open, FR58). The marker write is an " +
			"UNCONDITIONED update (no expectedRevision): create-if-absent / overwrite-if-present, so every fire " +
			"bumps the KV revision and reprojects, across unbounded freshness cycles. Type-agnostic: the entity " +
			"type is whatever entityKey carries; the script names no concrete type. Submitted under Weaver's " +
			"service-actor authority. The marker aspect's class is freshnessExpiry (its own aspect-type DDL " +
			"admits MarkExpired so step-6 permits the write).",
		Script: markExpiredDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"entityKey":{"type":"string","description":"vtx.<type>.<NanoID> — the entity to re-touch. Any vertex type; the freshnessExpiry marker aspect is written on it."},` +
			`"targetId":{"type":"string","description":"Optional weaver-target id the firing came from (provenance only; not used to form any key)."},` +
			`"expiredAt":{"type":"string","description":"RFC3339 instant the freshness deadline fired; stored verbatim on the marker aspect (the per-fire value that bumps the revision)."}},` +
			`"required":["entityKey","expiredAt"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.<type>.<NanoID> of the re-touched entity (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"entityKey": "Full vtx.<type>.<NanoID> key of the entity to re-touch. The freshnessExpiry marker aspect is written on this key; the entity's type is read from the key, never named by the DDL.",
			"targetId":  "Optional weaver-target id the firing originated from. Provenance only — it is NOT used to construct any key (the entityKey is the sole key source).",
			"expiredAt": "RFC3339 instant the freshness deadline fired. Stored verbatim on the marker aspect as the per-fire value; a new fire instant changes the value and so the KV revision.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "MarkExpired — re-touch a lapsed lease application",
				Payload: map[string]any{
					"entityKey": "vtx.leaseapp.<applicationNanoID>",
					"targetId":  "leaseApplicationComplete",
					"expiredAt": "2026-06-18T14:00:00Z",
				},
				ExpectedOutcome: "Writes vtx.leaseapp.<applicationNanoID>.freshnessExpiry = {expiredAt: 2026-06-18T14:00:00Z} as " +
					"an unconditioned update (create-if-absent / overwrite-if-present), emits orchestration.freshnessMarked, and " +
					"returns primaryKey (the entity key). The marker write bumps the entity's adjacency revision, triggering " +
					"Refractor to reproject its convergence row with a fresh $now (re-opening the stale-freshness gap). Repeats " +
					"cleanly on every subsequent lapse (the unconditioned update never conflicts).",
			},
		},
	}
}

// FreshnessExpiryAspectDDL returns the aspect-type DDL that declares the generic
// freshnessExpiry marker aspect. It exists so the Processor's step-6 validator —
// which keys permittedCommands on the MUTATION document's class — admits the
// MarkExpired-written marker (whose class is freshnessExpiry). It is NOT
// sensitive (it carries only a fire timestamp, no PII), so it attaches to a
// vertex of any type (the step-6 sensitiveAspectScope rule does not fire). Its
// script is declaration-only and never executes an op (the freshnessMarker
// vertexType DDL owns the MarkExpired script); it fails closed if dispatched.
func FreshnessExpiryAspectDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     freshnessExpiryAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"MarkExpired"},
		Description: "Generic freshness-expiry marker aspect (Contract #10 §10.4). Stored as " +
			"vtx.<type>.<NanoID>.freshnessExpiry = {expiredAt}, non-sensitive, type-agnostic (attaches to any " +
			"vertex). Written ONLY by MarkExpired (whose freshnessMarker vertexType DDL owns the script); this " +
			"aspect-type DDL exists so step-6's permittedCommands check, keyed on the mutation's class, admits " +
			"the marker write. Declaration-only: it carries no op handler.",
		Script: freshnessExpiryAspectDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"expiredAt":{"type":"string","description":"RFC3339 instant the freshness deadline fired."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"expiredAt": "RFC3339 instant the freshness deadline fired, stored verbatim on the marker aspect.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "freshnessExpiry marker aspect",
				Payload:         map[string]any{"expiredAt": "2026-06-18T14:00:00Z"},
				ExpectedOutcome: "Stored as vtx.<type>.<NanoID>.freshnessExpiry; written by MarkExpired as an unconditioned update.",
			},
		},
	}
}

// markExpiredDDLScript handles MarkExpired. It reads the entity ROOT (the OCC
// read the op declares in ContextHint.Reads) to assert the target exists + is
// alive before writing the marker — the marker is non-sensitive, so step-6's
// sensitiveAspectScope does not fire, and without this check MarkExpired would
// happily mint a marker (and a 4-segment aspect key) on an absent/tombstoned
// parent. It names no concrete type (the root is checked generically by key) and
// is idempotent in effect for a given fire instant.
const markExpiredDDLScript = `
def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def parts_of(key, name):
    # Parse a VERTEX key: exactly 3 segments vtx.<type>.<NanoID>. Any other shape
    # (aspect/link key, stray tail) is rejected — the marker must attach to a
    # vertex root, and "<entityKey>.freshnessExpiry" must be a well-formed
    # 4-segment aspect key.
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    return parts[1], parts[2]

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
        expired_at = required_string(p, "expiredAt")
        # Validate the key shape only (type-agnostic — any type segment is
        # accepted); names no concrete type.
        parts_of(entity_key, "entityKey")

        # Target-existence guard: never write a marker (and a 4-segment aspect
        # key) onto an absent or tombstoned parent. The op hydrates [entityKey]
        # (ContextHint.Reads), so the root is in state. A stale firing whose
        # entity was deleted after the timer armed fails closed here rather than
        # leaving a dangling marker on a non-existent vertex.
        if not vertex_alive(state, entity_key):
            fail("NotFound: entityKey " + entity_key + " is absent or tombstoned; no marker written")

        marker_key = entity_key + ".freshnessExpiry"

        # UNCONDITIONED update (the mutation carries NO revision condition):
        # create-if-absent / overwrite-if-present. Every fire bumps the KV
        # revision -> fresh CDC -> reprojection, across unbounded freshness
        # cycles. A 'create' would conflict on the second lapse and silently
        # break the eager re-open.
        mutations = [
            {"op": "update", "key": marker_key,
             "document": {"class": "freshnessExpiry", "vertexKey": entity_key,
                          "localName": "freshnessExpiry", "isDeleted": False,
                          "data": {"expiredAt": expired_at}}},
        ]
        events = [{"class": "orchestration.freshnessMarked",
                   "data": {"entityKey": entity_key, "expiredAt": expired_at}}]
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
