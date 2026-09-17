package servicelocation

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations.
//
// Single DDL `serviceLocation` (link-op class) handles all ten link ops that
// wire the residence-based service-access topology plus the workplace spine:
//
//	WireResidesIn, UnwireResidesIn               # identity → location
//	WireWorksAt, UnwireWorksAt                   # identity → location
//	WireAvailableAt, UnwireAvailableAt           # service-template → location
//	WireUnavailableAt, UnwireUnavailableAt       # service-template → location
//	WirePermitsOperation, UnwirePermitsOperation # service → op-meta
//
// Architectural rules (binding — same known-key discipline as location-domain /
// service-domain):
//
//   - The script reads by known key. No prefix scans, no lens-output reads.
//     Each wire op validates its link endpoints by reading each by the key
//     the caller lists in ContextHint.Reads. The one adjacency read is
//     WireResidesIn's bounded `# read-posture: (e)` walk over the identity's
//     own residesIn links (RESIDES_IN_MAX_PAGES pages of
//     RESIDES_IN_PAGE_LIMIT), which serves the link's state whenever the
//     snapshot does not carry the link key.
//   - Endpoint validation is AT THE OP (not the lens's untyped match):
//     residesIn and worksAt targets MUST be keyed with an admitted location
//     type segment (unit / building / property — the key is the authority, not
//     the root class); availableAt / unavailableAt source MUST be a service
//     TEMPLATE (a service whose ENVELOPE class ends in `.template`) and target
//     MUST likewise be a location key; permitsOperation source MUST be
//     class=service and target MUST be an op-meta vertex (vtx.meta.<id>
//     carrying a data.operationType). A dead or wrong-typed endpoint is never
//     wired (structured ScriptError).
//   - residesIn cardinality: MULTIPLE allowed — an identity may reside in many
//     locations; the lens's fresh-var exclusion is residence-set-aware.
//   - worksAt cardinality: MULTIPLE allowed — one person may work at several
//     buildings. worksAt is PURE TOPOLOGY: it feeds lens reachability and
//     workplace-anchored read-grant derivation, and is deliberately NOT an
//     input to the capabilityServiceAccess join — availableAt / residesIn stay
//     the only authorization-bearing edges there. Staff authority comes from
//     role grants (cap.roles), never from where someone works.
//
// Link direction follows Contract #1 §1.1 (the later-arriving vertex is the
// SOURCE) and reads as a sentence:
//
//	lnk.<idType>.<idId>.residesIn.<locType>.<locId>            # "identity residesIn location"
//	lnk.<idType>.<idId>.worksAt.<locType>.<locId>              # "identity worksAt location"
//	lnk.service.<tplId>.availableAt.<locType>.<locId>          # "service availableAt location"
//	lnk.service.<tplId>.unavailableAt.<locType>.<locId>        # "service unavailableAt location"
//	lnk.service.<svcId>.permitsOperation.meta.<opId>           # "service permitsOperation operation"
//
// Caller's ContextHint.Reads MUST include:
//   - WireResidesIn / WireWorksAt: BOTH endpoints (the identity + the location).
//   - WireAvailableAt / WireUnavailableAt: the service template (its root
//     envelope class is the discriminator the template guard reads) + the location.
//   - WirePermitsOperation: the service + the op-meta vertex.
//   - the Unwire* ops: the deterministic link key (computed from the endpoints
//     by the caller — see the key shapes above).
//
// Every WireResidesIn dispatcher ALSO declares the walk the script runs, in
// ContextHint.Enumerations: `{hub: <identity>, relation: residesIn, direction:
// out}` (Contract #2 §2.5.1) — the seeds included, because a first wire always
// walks (below).
//
// Every Wire* op reads its deterministic link key from ContextHint.OptionalReads
// (Contract #2 §2.5) — optional, not required, because a first wire
// legitimately finds it absent. A snapshot that carries the key shows the
// script the link's state directly (alive → no-op, tombstoned → revive with an
// update). For WireWorksAt / WireAvailableAt / WireUnavailableAt /
// WirePermitsOperation the declaration is what tells a tombstoned link apart
// from an absent one: a caller that omits it can wire a link once and, after an
// Unwire*, never re-wire it (the script would emit a create against a key that
// already exists at a later revision → RevisionConflict). WireResidesIn does
// not depend on the declaration: whenever its snapshot lacks the link key — the
// key undeclared, or declared and absent (a known-absent optional read is never
// in the snapshot) — it walks the identity's own residesIn links, paged to a
// bound, and finds the same three states there, reviving a tombstone pinned to
// the page entry's revision. So a first wire always walks (normally one page,
// limit+1 budget units) and only a declared re-wire of an existing or
// tombstoned link is answered by the snapshot alone. The walk is bounded at
// RESIDES_IN_MAX_PAGES × RESIDES_IN_PAGE_LIMIT subjects; past that it falls
// back to the create, which is create-once: an absent target still commits
// (a declared submitter's additionally absence-conditioned), and a tombstoned
// target the bounded walk never reached rejects RevisionConflict — an outcome
// confined to identities carrying more residesIn subjects than the bound, and
// never a silent overwrite. A convergence dispatcher composes no link keys, so
// this is what lets a lease's approval re-wire a residence its earlier end
// unwired.
//
// A ReassignLeaseUnit of an approved lease is an operator repair verb that
// leaves the OLD unit's residence wired; the operator releases it by hand with
// UnwireResidesIn (the new unit's residence is wired by the lease's own gap).
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{serviceLocationDDL()}
}

func serviceLocationDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName: "serviceLocation",
		Class:         "meta.ddl.vertexType",
		PermittedCommands: []string{
			"WireResidesIn", "UnwireResidesIn",
			"WireWorksAt", "UnwireWorksAt",
			"WireAvailableAt", "UnwireAvailableAt",
			"WireUnavailableAt", "UnwireUnavailableAt",
			"WirePermitsOperation", "UnwirePermitsOperation",
		},
		Description: "Service-location scheme DDL. Wires the residence-based service-access topology " +
			"(the cap.svc grant source) plus the workplace spine as LINKS: residesIn (identity→location: " +
			"where an actor lives), worksAt (identity→location: where an actor works — pure topology, NOT a " +
			"cap.svc input), " +
			"availableAt (service-template→location: where an offering is available), unavailableAt " +
			"(service-template→location: an explicit exclusion override), permitsOperation " +
			"(service→op-meta: which operations a service exposes). All links: the later-arriving vertex is " +
			"the source, the pre-existing one is the target (Contract #1 §1.1); the sentence reads 'identity " +
			"residesIn location', 'service availableAt location'. Each Wire op validates its endpoint classes " +
			"at the op (residesIn target=an admitted location type; availableAt/unavailableAt source=a service template " +
			"[its envelope class ends in .template], target=location; permitsOperation source=service, " +
			"target=an op-meta vertex). residesIn and worksAt cardinality are multiple. Each Unwire op tombstones the link " +
			"by its deterministic key, and a later Wire of the same endpoints revives it.",
		Script: serviceLocationDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"identity":{"type":"string","description":"vtx.identity.<NanoID> of the person — the residesIn link source (WireResidesIn) or the worksAt link source (WireWorksAt); required, validated alive."},` +
			`"location":{"type":"string","description":"vtx.<locType>.<NanoID> of the location (WireResidesIn target / WireWorksAt target / WireAvailableAt target / WireUnavailableAt target; required, validated alive + an admitted location type segment)."},` +
			`"service":{"type":"string","description":"vtx.service.<NanoID> of the service — a template for WireAvailableAt/WireUnavailableAt (validated alive + a template, envelope class ends in .template), or any service for WirePermitsOperation (validated alive + a service.* envelope class)."},` +
			`"operation":{"type":"string","description":"vtx.meta.<NanoID> of the op-meta vertex the service exposes — the permitsOperation link target (WirePermitsOperation; required, validated alive + carries data.operationType)."},` +
			`"linkKey":{"type":"string","description":"The deterministic 6-segment link key of an existing link to tombstone (the Unwire ops; required, validated alive)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"The link key the operation wrote (Wire ops) or tombstoned (Unwire ops). Absent on idempotent no-op replays (nothing committed)."}}}`,
		FieldDescription: map[string]string{
			"identity":  "Full vtx.identity.<NanoID> key of the person. WireResidesIn / WireWorksAt validate it is alive and write it as the link SOURCE (the identity is the later-arriving vertex, Contract #1 §1.1).",
			"location":  "Full vtx.<locType>.<NanoID> key of the location. Validated alive + an admitted location type segment; written as the link TARGET for residesIn / worksAt / availableAt / unavailableAt.",
			"service":   "Full vtx.service.<NanoID> key. For availableAt/unavailableAt it MUST be a service template (its envelope class ends in .template); for permitsOperation it MUST be a service.* envelope class. Written as the link SOURCE.",
			"operation": "Full vtx.meta.<NanoID> key of the op-meta vertex the service exposes. WirePermitsOperation validates it is alive and carries a data.operationType, then writes it as the permitsOperation link TARGET.",
			"linkKey":   "Full 6-segment link key of an existing link to tombstone (the Unwire ops).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "WireResidesIn — place a resident in a unit",
				Payload: map[string]any{"identity": "vtx.identity.<idNanoID>", "location": "vtx.unit.<unitNanoID>"},
				ExpectedOutcome: "Validates the identity (alive) and the unit (alive + an admitted location type segment), then writes " +
					"lnk.identity.<idNanoID>.residesIn.unit.<unitNanoID> (class=residesIn, source=identity, target=location). " +
					"Returns primaryKey (the link key). Idempotent: a replay where the link already exists alive commits " +
					"nothing and omits primaryKey. A link previously tombstoned by UnwireResidesIn is REVIVED (a resident who " +
					"moved out can move back into the same unit) whether or not the submitter listed the link key in " +
					"contextHint.optionalReads: a snapshot without it is served by a bounded walk of the identity's own " +
					"residesIn links, the revive pinned to the tombstone's revision. The walk is bounded (RESIDES_IN_MAX_PAGES " +
					"pages of RESIDES_IN_PAGE_LIMIT); an identity carrying more residesIn subjects than that falls back to a " +
					"create-once create — an absent link still commits, a tombstoned one past the bound rejects RevisionConflict. " +
					"residesIn cardinality is multiple (an " +
					"identity may reside in many locations). An operator who moves an approved lease to another unit with " +
					"ReassignLeaseUnit releases the old unit's residence by hand with UnwireResidesIn; nothing automates it.",
			},
			{
				Name:    "WireWorksAt — place a staff member at their workplace",
				Payload: map[string]any{"identity": "vtx.identity.<staffNanoID>", "location": "vtx.building.<buildingNanoID>"},
				ExpectedOutcome: "Validates the identity (alive) and the building (alive + an admitted location type segment), then writes " +
					"lnk.identity.<staffNanoID>.worksAt.building.<buildingNanoID> (class=worksAt, source=identity, target=location). " +
					"Returns primaryKey (the link key). Idempotent: a replay where the link already exists alive commits nothing " +
					"and omits primaryKey; a link previously tombstoned by UnwireWorksAt is REVIVED. worksAt cardinality is " +
					"multiple. The link is pure topology — it grants no service access " +
					"(it is not a capabilityServiceAccess input); it scopes where a staff actor's world and read grants derive from.",
			},
			{
				Name:    "WireAvailableAt — make a laundry service available at a building",
				Payload: map[string]any{"service": "vtx.service.<laundryTplNanoID>", "location": "vtx.building.<buildingNanoID>"},
				ExpectedOutcome: "Validates the service is alive + a template (its envelope class ends in .template) and the " +
					"building is alive + an admitted location type segment, then writes lnk.service.<laundryTplNanoID>.availableAt.building.<buildingNanoID> " +
					"(class=availableAt, source=service, target=location). Returns primaryKey. Rejects with ScriptError if the " +
					"service is not a template or the location is not a location key.",
			},
			{
				Name:    "WireUnavailableAt — exclude the laundry service from a penthouse",
				Payload: map[string]any{"service": "vtx.service.<laundryTplNanoID>", "location": "vtx.unit.<penthouseNanoID>"},
				ExpectedOutcome: "Validates the service template + the location, then writes " +
					"lnk.service.<laundryTplNanoID>.unavailableAt.unit.<penthouseNanoID> (class=unavailableAt). A closer " +
					"unavailableAt beats a higher-up availableAt in the cap.svc lens (multi-level exclusion). Returns primaryKey.",
			},
			{
				Name:    "WirePermitsOperation — expose the BookLaundry op on the laundry service",
				Payload: map[string]any{"service": "vtx.service.<laundryNanoID>", "operation": "vtx.meta.<bookLaundryOpNanoID>"},
				ExpectedOutcome: "Validates the service (alive + class=service) and the op-meta (alive + carries a " +
					"data.operationType), then writes lnk.service.<laundryNanoID>.permitsOperation.meta.<bookLaundryOpNanoID> " +
					"(class=permitsOperation). The lens projects the op-meta's operationType into serviceAccess[].allowedOperations. " +
					"Returns primaryKey.",
			},
			{
				Name:            "UnwireResidesIn — move a resident out",
				Payload:         map[string]any{"linkKey": "lnk.identity.<idNanoID>.residesIn.unit.<unitNanoID>"},
				ExpectedOutcome: "Tombstones the residesIn link. Returns primaryKey (the link key). Rejects with ScriptError if the link is absent or already dead.",
			},
		},
	}
}

// serviceLocationDDLScript handles the eight link ops. Known-key reads only
// (each Wire op validates its link endpoints by the keys the caller listed in
// ContextHint.Reads). Endpoint-class validation is at the op: residesIn target
// is a location, availableAt/unavailableAt source is a service TEMPLATE +
// target is a location, permitsOperation source is a service + target is an
// op-meta. The links carry empty data {} — they are pure topology the cap.svc
// lens walks.
const serviceLocationDDLScript = `
# The walk WireResidesIn makes over an identity's own residesIn links when its
# snapshot does not carry the link key: at most RESIDES_IN_MAX_PAGES pages of
# RESIDES_IN_PAGE_LIMIT subjects each, tombstones included. residesIn
# cardinality is multiple, but an identity resides in a handful of units, so
# the first page is normally the whole walk; each page is charged limit+1
# units of the live-read budget whatever it holds. Past the bound the walk
# stops answering and the wire falls back to a create: create-once, so an
# absent target still commits and a tombstoned target past the bound rejects
# RevisionConflict — the outcome is confined to identities carrying more
# residesIn subjects than the bound.
RESIDES_IN_PAGE_LIMIT = 50
RESIDES_IN_MAX_PAGES = 4

def link_document(source, target, cls, local_name, data):
    return {"class": cls, "isDeleted": False,
            "sourceVertex": source, "targetVertex": target,
            "localName": local_name, "data": data}

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": link_document(source, target, cls, local_name, data)}

def revive_link(key, source, target, cls, local_name, data):
    # Re-wire of a TOMBSTONED link. It must be an update, not a create: a
    # create asserts revision 0 (step 8) and the tombstone already sits at a
    # later revision, so a create here fails RevisionConflict. An update
    # asserts the revision the key was hydrated at, which is what a revive
    # actually means — resurrect this document from the state we just read.
    #
    # An update writes the whole document; step 8 carries the createdAt /
    # createdBy / createdByOp triplet over from the stored tombstone (the
    # script cannot supply them — the hydrated document does not expose them)
    # and re-stamps the lastModified* triplet, so the revived link keeps its
    # birth and records the revive.
    return {"op": "update", "key": key,
            "document": link_document(source, target, cls, local_name, data)}

def revive_link_at(key, source, target, cls, local_name, data, expected_revision):
    # revive_link for a tombstone the script found on a kv.Links page rather
    # than in its hydrated snapshot. The document is the same; the difference
    # is the condition. A page entry carries no step-4 revision, so a bare
    # update on its key would land unconditioned — whatever the page observed,
    # regardless of what changed in between — and the pin is the entry's own
    # revision: a racing writer of the same link (a concurrent UnwireResidesIn,
    # a second WireResidesIn) moves the revision and this update fails
    # RevisionConflict instead of overwriting it.
    return {"op": "update", "key": key,
            "document": link_document(source, target, cls, local_name, data),
            "expectedRevision": expected_revision}

def make_tombstone(key):
    return {"op": "tombstone", "key": key}

def split_key(k):
    return k.split(".")

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

# The concrete location levels this scheme admits as link endpoints (the
# <locType> key segment in vtx.<locType>.<NanoID>, Contract #6 §6.9).
# location-domain owns the vertices; this scheme references them by KEY TYPE —
# the same authority a lens label resolves against. A location vertex's class
# IS its own key type (unit / building / property), so no single class value
# names the family.
LOCATION_TYPES = ["unit", "building", "property"]

# The full set of classes a live location vertex may carry: its own key type,
# the class every location gets.
LOCATION_CLASSES = LOCATION_TYPES

def parts_of(key, name, want_type):
    # Parses a VERTEX key: exactly 3 segments vtx.<type>.<NanoID>. A non-3
    # segment key (e.g. an aspect/link key) is rejected, not silently truncated.
    # An empty want_type accepts any type and still returns it.
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

def link_parts(key, name, relation):
    # Parses a LINK key: exactly 6 segments lnk.<aType>.<aId>.<relation>.<bType>.<bId>
    # with the relation segment == relation. Any other shape is rejected.
    parts = split_key(key)
    if len(parts) != 6 or parts[0] != "lnk" or parts[3] != relation:
        fail("InvalidArgument: " + name + ": required lnk.<aType>.<aId>." + relation + ".<bType>.<bId> (exactly 6 segments); got " + key)
    return parts

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def class_of(state, key):
    # The vertex's root class, or None if absent. "class" is a Starlark
    # reserved word, so getattr with the string key is required.
    if key not in state:
        return None
    doc = state[key]
    if doc == None:
        return None
    if not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

def require_live_location(state, key, name):
    # The endpoint MUST be alive, keyed vtx.<locationType>.<NanoID> at an
    # admitted location level, AND carry a location class.
    # BOTH the key and the class are checked, and each catches what the other
    # cannot. The KEY's type segment is the type authority — it is what a lens
    # label resolves against, and it is the only thing that can say "any
    # location" across the three levels, since a location's class is its own
    # key type. The CLASS is what proves location-domain minted the vertex: a
    # foreign package writing vtx.unit.<id> with a class of its own passes the
    # key check and must still be refused.
    if not vertex_alive(state, key):
        fail("UnknownLocation: " + name + ": " + key + " is absent or tombstoned")
    lt = key_type_of(key)
    if lt not in LOCATION_TYPES:
        fail("NotALocation: " + name + ": " + key + " has type segment " + str(lt) + ", required one of unit, building, property")
    cls = class_of(state, key)
    if cls not in LOCATION_CLASSES:
        fail("NotALocation: " + name + ": " + key + " has class " + str(cls) + ", required its own location type")

def key_type_of(key):
    # The type segment of a 3-segment vtx.<type>.<NanoID> key, or None for any
    # other shape (an aspect key, a link key, a malformed string).
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        return None
    return parts[1]

def require_live_service_template(state, key, name):
    # The service availableAt/unavailableAt source MUST be a TEMPLATE: alive and
    # its ENVELOPE class ends in .template (P7 — the template/instance
    # discriminator is the envelope class service.<x>.template / .instance; there
    # is no .class shadow aspect). An instance (or any non-template) is never
    # wired with an availability assertion.
    if not vertex_alive(state, key):
        fail("UnknownService: " + name + ": " + key + " is absent or tombstoned")
    cls = class_of(state, key)
    if cls == None or not cls.startswith("service.") or not cls.endswith(".template"):
        fail("NotATemplate: " + name + ": " + key + " is not a service template (envelope class " + str(cls) + ")")

def require_live_service(state, key, name):
    # Any service vertex (template or instance): alive + a service.* envelope
    # class (P7 — a service root class is service.<x>.template / .instance).
    if not vertex_alive(state, key):
        fail("UnknownService: " + name + ": " + key + " is absent or tombstoned")
    cls = class_of(state, key)
    if cls == None or not cls.startswith("service."):
        fail("NotAService: " + name + ": " + key + " has class " + str(cls) + ", required a service.* envelope class")

def require_live_opmeta(state, key, name):
    # The permitsOperation target MUST be an op-meta vertex: alive, vtx.meta.*,
    # carrying a data.operationType (the field the lens projects into
    # allowedOperations).
    if not vertex_alive(state, key):
        fail("UnknownOperation: " + name + ": " + key + " is absent or tombstoned")
    doc = state[key]
    if not hasattr(doc, "data") or doc.data == None or "operationType" not in doc.data:
        fail("NotAnOpMeta: " + name + ": " + key + " carries no data.operationType")

def link_key_of(src_type, src_id, relation, tgt_type, tgt_id):
    return "lnk." + src_type + "." + src_id + "." + relation + "." + tgt_type + "." + tgt_id

def wire(state, src, target, relation, src_type, src_id, tgt_type, tgt_id):
    # The three-state table every Wire* op resolves, read from the step-4
    # snapshot. The link key is deterministic and the caller declares it as an
    # OPTIONAL read (it is legitimately absent on a first wire), so each state
    # gets its own mutation shape:
    #
    #   alive      → idempotent no-op (nothing committed → no primaryKey)
    #   absent     → create (conditioned on absence)
    #   tombstoned → revive as a bare update (step 8 conditions it on the
    #                revision the key was hydrated at)
    #
    # A snapshot that does not carry the key reads as absent whether the link
    # is absent or tombstoned; for a tombstone that create is a create-once
    # collision that never commits. WireResidesIn is the one op that serves
    # such a snapshot from a second source (wire_resides_in); the other four
    # Wire* ops rely on the declaration.
    lnk_key = link_key_of(src_type, src_id, relation, tgt_type, tgt_id)
    existing = state[lnk_key] if lnk_key in state else None
    if existing == None:
        return lnk_key, [make_link(lnk_key, src, target, relation, relation, {})]
    if not (hasattr(existing, "isDeleted") and existing.isDeleted):
        return lnk_key, []
    return lnk_key, [revive_link(lnk_key, src, target, relation, relation, {})]

def wire_resides_in(state, identity, location, id_type, id_id, loc_type, loc_id):
    # The same three-state table as wire(), with a second source. The snapshot
    # carries the link key only when the submitter listed it in optionalReads
    # AND the link exists (alive or tombstoned) — a declared re-wire — and then
    # it is the whole answer. Every other snapshot lacks the key: undeclared (a
    # convergence dispatcher composes no link keys), or declared and absent (a
    # known-absent optional read is never in state). So a first wire always
    # walks, and the walk is the identity's own residesIn links, paged to a
    # bound, where a tombstone is visible with its revision: the matching entry
    # alive → no-op, dead → revive pinned to the entry's revision, no entry on
    # any page → create as on a first wire. A walk that reaches its bound with
    # subjects still unread falls back to that same create-once: an absent
    # target commits, a tombstoned target past the bound rejects
    # RevisionConflict — never a blind overwrite, and never a refusal that
    # would also stop a declared submitter wiring a fresh residence.
    lnk_key = link_key_of(id_type, id_id, "residesIn", loc_type, loc_id)
    if lnk_key in state:
        return wire(state, identity, location, "residesIn", id_type, id_id, loc_type, loc_id)
    cursor = None
    for _page in range(RESIDES_IN_MAX_PAGES):
        # read-posture: (e) relation=residesIn epoch=none -- an identity resides in a handful of units, so the first page is normally the whole walk; a writer racing this walk on the same link moves the revision the revive is pinned to and the revive fails RevisionConflict rather than overwriting it; the create the walk falls back to (every page read, or the bound reached) is create-once, so a tombstone it did not see rejects RevisionConflict rather than being overwritten.
        page, cursor = kv.Links(identity, "residesIn", "out", cursor, RESIDES_IN_PAGE_LIMIT)
        for lk in page:
            if lk.key != lnk_key:
                continue
            if not lk.isDeleted:
                return lnk_key, []
            return lnk_key, [revive_link_at(lk.key, identity, location, "residesIn", "residesIn", {}, lk.revision)]
        if cursor == None:
            break
    return lnk_key, [make_link(lnk_key, identity, location, "residesIn", "residesIn", {})]

def unwire(state, lnk_key):
    existing = state[lnk_key] if lnk_key in state else None
    if existing == None or (hasattr(existing, "isDeleted") and existing.isDeleted):
        fail("UnknownLink: " + lnk_key)
    return [make_tombstone(lnk_key)]

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "WireResidesIn":
        identity = required_string(p, "identity")
        location = required_string(p, "location")
        id_type, id_id = parts_of(identity, "identity", "identity")
        loc_type, loc_id = parts_of(location, "location", "")
        if not vertex_alive(state, identity):
            fail("UnknownIdentity: identity: " + identity + " is absent or tombstoned")
        require_live_location(state, location, "location")
        lnk_key, mutations = wire_resides_in(state, identity, location, id_type, id_id, loc_type, loc_id)
        if len(mutations) == 0:
            return {"mutations": [], "events": []}
        events = [{"class": "serviceLocation.residesInWired",
                   "data": {"identity": identity, "location": location, "linkKey": lnk_key}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": lnk_key}}

    if ot == "WireWorksAt":
        identity = required_string(p, "identity")
        location = required_string(p, "location")
        id_type, id_id = parts_of(identity, "identity", "identity")
        loc_type, loc_id = parts_of(location, "location", "")
        if not vertex_alive(state, identity):
            fail("UnknownIdentity: identity: " + identity + " is absent or tombstoned")
        require_live_location(state, location, "location")
        lnk_key, mutations = wire(state, identity, location, "worksAt", id_type, id_id, loc_type, loc_id)
        if len(mutations) == 0:
            return {"mutations": [], "events": []}
        events = [{"class": "serviceLocation.worksAtWired",
                   "data": {"identity": identity, "location": location, "linkKey": lnk_key}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": lnk_key}}

    if ot == "WireAvailableAt":
        service = required_string(p, "service")
        location = required_string(p, "location")
        _, svc_id = parts_of(service, "service", "service")
        loc_type, loc_id = parts_of(location, "location", "")
        require_live_service_template(state, service, "service")
        require_live_location(state, location, "location")
        lnk_key, mutations = wire(state, service, location, "availableAt", "service", svc_id, loc_type, loc_id)
        if len(mutations) == 0:
            return {"mutations": [], "events": []}
        events = [{"class": "serviceLocation.availableAtWired",
                   "data": {"service": service, "location": location, "linkKey": lnk_key}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": lnk_key}}

    if ot == "WireUnavailableAt":
        service = required_string(p, "service")
        location = required_string(p, "location")
        _, svc_id = parts_of(service, "service", "service")
        loc_type, loc_id = parts_of(location, "location", "")
        require_live_service_template(state, service, "service")
        require_live_location(state, location, "location")
        lnk_key, mutations = wire(state, service, location, "unavailableAt", "service", svc_id, loc_type, loc_id)
        if len(mutations) == 0:
            return {"mutations": [], "events": []}
        events = [{"class": "serviceLocation.unavailableAtWired",
                   "data": {"service": service, "location": location, "linkKey": lnk_key}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": lnk_key}}

    if ot == "WirePermitsOperation":
        service = required_string(p, "service")
        operation = required_string(p, "operation")
        _, svc_id = parts_of(service, "service", "service")
        _, op_id = parts_of(operation, "operation", "meta")
        require_live_service(state, service, "service")
        require_live_opmeta(state, operation, "operation")
        lnk_key, mutations = wire(state, service, operation, "permitsOperation", "service", svc_id, "meta", op_id)
        if len(mutations) == 0:
            return {"mutations": [], "events": []}
        events = [{"class": "serviceLocation.permitsOperationWired",
                   "data": {"service": service, "operation": operation, "linkKey": lnk_key}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": lnk_key}}

    if ot == "UnwireResidesIn":
        lnk_key = required_string(p, "linkKey")
        link_parts(lnk_key, "linkKey", "residesIn")
        mutations = unwire(state, lnk_key)
        events = [{"class": "serviceLocation.residesInUnwired", "data": {"linkKey": lnk_key}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": lnk_key}}

    if ot == "UnwireWorksAt":
        lnk_key = required_string(p, "linkKey")
        link_parts(lnk_key, "linkKey", "worksAt")
        mutations = unwire(state, lnk_key)
        events = [{"class": "serviceLocation.worksAtUnwired", "data": {"linkKey": lnk_key}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": lnk_key}}

    if ot == "UnwireAvailableAt":
        lnk_key = required_string(p, "linkKey")
        link_parts(lnk_key, "linkKey", "availableAt")
        mutations = unwire(state, lnk_key)
        events = [{"class": "serviceLocation.availableAtUnwired", "data": {"linkKey": lnk_key}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": lnk_key}}

    if ot == "UnwireUnavailableAt":
        lnk_key = required_string(p, "linkKey")
        link_parts(lnk_key, "linkKey", "unavailableAt")
        mutations = unwire(state, lnk_key)
        events = [{"class": "serviceLocation.unavailableAtUnwired", "data": {"linkKey": lnk_key}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": lnk_key}}

    if ot == "UnwirePermitsOperation":
        lnk_key = required_string(p, "linkKey")
        link_parts(lnk_key, "linkKey", "permitsOperation")
        mutations = unwire(state, lnk_key)
        events = [{"class": "serviceLocation.permitsOperationUnwired", "data": {"linkKey": lnk_key}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": lnk_key}}

    fail("serviceLocation DDL: unknown operationType: " + ot)
`
