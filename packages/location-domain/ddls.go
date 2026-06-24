package locationdomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations.
//
// Single DDL `location` (vertex-type class) handles all four ops for the three
// location types (unit / building / property):
//
//	CreateLocation, TombstoneLocation
//	WireContainedIn, UnwireContainedIn
//
// Architectural rules (binding — same known-key discipline as service-domain /
// identity-domain):
//
//   - The script reads ONLY by known key. No prefix scans, no adjacency
//     lookups, no lens-output reads. WireContainedIn validates BOTH link
//     endpoints (the child + parent location) by reading each by the key the
//     caller lists in ContextHint.Reads.
//   - Both endpoints of a containedIn link MUST be alive AND class=location: a
//     non-location vertex (or a dead one) is never wired into the place graph
//     (structured ScriptError). Endpoint-class validation is at the op — a
//     downstream cypher rule's untyped match is not relied on.
//   - Canonical-name uniqueness for new location vertices is NOT enforced here.
//     The script assigns a fresh NanoID and writes; locations have no canonical
//     name (the place graph is topology, not a named registry).
//
// Location shape (Contract #6 §6.9 + Contract #1 §1.1 + D5 — root data minimal,
// the type is the key segment, the class is the shared discriminator):
//
//	vtx.unit.<id>       class=location   root data = {}
//	vtx.building.<id>   class=location   root data = {}
//	vtx.property.<id>   class=location   root data = {}
//	lnk.<childType>.<childId>.containedIn.<parentType>.<parentId>   class=containedIn
//
// The containedIn link's source is the later-arriving CHILD (the contained
// vertex), the target is the pre-existing PARENT (the container) — Contract #1
// §1.1. The sentence reads "unit containedIn building", "building containedIn
// property". Containment is transitive (unit → building → property); the link
// is the topology a downstream availability lens walks.
//
// Caller's ContextHint.Reads MUST include:
//   - CreateLocation: nothing (the script mints a fresh NanoID).
//   - TombstoneLocation: the existing location vertex key.
//   - WireContainedIn: BOTH endpoints (child + parent) so each is hydrated and
//     its alive + location-class is validated. The link key is deterministic
//     from the endpoint keys.
//   - UnwireContainedIn: the deterministic containedIn link key (computed from
//     child + parent by the caller — see the key shape above).
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{locationDDL()}
}

func locationDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "location",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateLocation", "TombstoneLocation", "WireContainedIn", "UnwireContainedIn"},
		Description: "Location domain DDL. Vertex shape: vtx.<locationType>.<NanoID>, class=location, root data = {} " +
			"(minimal, D5). locationType is one of {unit, building, property} (Contract #6 §6.9): it names the key " +
			"type segment; the class is the shared discriminator `location`. Containment is the containedIn LINK " +
			"(location→location, transitive — unit→building→property). containedIn direction: the contained CHILD " +
			"is the later-arriving source, the container PARENT is the pre-existing target (Contract #1 §1.1); the " +
			"sentence reads 'unit containedIn building'. CreateLocation mints a location vertex of the requested " +
			"type. TombstoneLocation soft-deletes one. WireContainedIn writes the containedIn link only after " +
			"validating BOTH endpoints are alive AND class=location (a non-location vertex is never wired into the " +
			"place graph). UnwireContainedIn tombstones the link by its deterministic key.",
		Script: locationDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"locationType":{"type":"string","enum":["unit","building","property"],"description":"The location level (unit|building|property); sets the vtx.<locationType>.<NanoID> key prefix (CreateLocation)."},` +
			`"locationId":{"type":"string","description":"Optional bare NanoID for the new location vertex (CreateLocation); absent → minted."},` +
			`"locationKey":{"type":"string","description":"vtx.<locationType>.<NanoID> of an existing location (TombstoneLocation; required, validated alive)."},` +
			`"child":{"type":"string","description":"vtx.<locationType>.<NanoID> of the contained (child) location — the containedIn link source (WireContainedIn; required, validated alive + class=location)."},` +
			`"parent":{"type":"string","description":"vtx.<locationType>.<NanoID> of the container (parent) location — the containedIn link target (WireContainedIn; required, validated alive + class=location)."},` +
			`"linkKey":{"type":"string","description":"lnk.<childType>.<childId>.containedIn.<parentType>.<parentId> of an existing containedIn link (UnwireContainedIn; required, validated alive)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"The principal Core KV key the operation wrote: the location vertex key for CreateLocation/TombstoneLocation, or the containedIn link key for WireContainedIn/UnwireContainedIn. Absent on idempotent no-op replays (nothing committed)."}}}`,
		FieldDescription: map[string]string{
			"locationType": "The location level, one of {unit, building, property}. Determines the vtx.<locationType>.<NanoID> key prefix for CreateLocation; the class is always `location`.",
			"locationId":   "Optional bare NanoID (no dots / key segments) for the new location vertex (vtx.<locationType>.<locationId>). Absent → minted with nanoid.new().",
			"locationKey":  "Full vtx.<locationType>.<NanoID> key of an existing location vertex to tombstone.",
			"child":        "Full vtx.<locationType>.<NanoID> key of the contained (child) location. WireContainedIn validates it is alive + class=location and writes it as the containedIn link SOURCE (the child is the later-arriving vertex, Contract #1 §1.1).",
			"parent":       "Full vtx.<locationType>.<NanoID> key of the container (parent) location. WireContainedIn validates it is alive + class=location and writes it as the containedIn link TARGET.",
			"linkKey":      "Full lnk.<childType>.<childId>.containedIn.<parentType>.<parentId> key of an existing containedIn link to tombstone (UnwireContainedIn).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "CreateLocation — mint a building",
				Payload: map[string]any{"locationType": "building"},
				ExpectedOutcome: "Mints vtx.building.<NanoID> (class=location, root data {}). Returns primaryKey " +
					"(the location key). Accepts an optional caller-supplied bare-NanoID locationId.",
			},
			{
				Name:    "WireContainedIn — place a unit inside a building",
				Payload: map[string]any{"child": "vtx.unit.<unitNanoID>", "parent": "vtx.building.<buildingNanoID>"},
				ExpectedOutcome: "Validates both the unit (child) and building (parent) are alive + class=location, then " +
					"writes lnk.unit.<unitNanoID>.containedIn.building.<buildingNanoID> (class=containedIn, source=child, " +
					"target=parent). Returns primaryKey (the link key). Idempotent: a replay where the link already exists " +
					"alive commits nothing and omits primaryKey. Rejects with ScriptError if either endpoint is absent, " +
					"dead, or not class=location.",
			},
			{
				Name:    "UnwireContainedIn — detach a unit from its building",
				Payload: map[string]any{"linkKey": "lnk.unit.<unitNanoID>.containedIn.building.<buildingNanoID>"},
				ExpectedOutcome: "Tombstones the containedIn link. Returns primaryKey (the link key). Rejects with " +
					"ScriptError if the link is absent or already dead.",
			},
		},
	}
}

// locationDDLScript handles the four location ops. Known-key reads only
// (WireContainedIn validates both link endpoints by the keys the caller listed
// in ContextHint.Reads). Both endpoints MUST be alive + class=location. Root
// data is minimal {} (D5); the type is the key segment, the class is `location`.
const locationDDLScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def make_tombstone(key):
    return {"op": "tombstone", "key": key,
            "document": {"isDeleted": True, "data": {}}}

def split_key(k):
    return k.split(".")

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

# The location types this package admits (the <locationType> key segment in
# vtx.<locationType>.<NanoID>, Contract #6 §6.9). One location DDL handles all
# three; the type is the key prefix + an op payload field, not a DDL per type.
LOCATION_TYPES = ["unit", "building", "property"]

# The class every location vertex carries (the shared discriminator a
# downstream cypher rule guards on). The type segment names the level; the
# class is constant.
LOCATION_CLASS = "location"

def required_location_type(p):
    lt = required_string(p, "locationType")
    if lt not in LOCATION_TYPES:
        fail("InvalidArgument: locationType: must be one of unit, building, property; got " + lt)
    return lt

def bare_nanoid_or_mint(p, name):
    # Returns the caller-supplied id when present, else a freshly minted one.
    # The supplied id is checked for KEY-DELIMITER safety only: it is rejected
    # if it carries a dot, a wildcard ("*"/">"), or whitespace, so
    # "vtx.<type>." + id is a single well-formed 3-segment vertex key. It is NOT
    # validated as a full canonical NanoID — the only invariant enforced here is
    # that it cannot inject extra key segments.
    if not hasattr(p, name):
        return nanoid.new()
    v = getattr(p, name)
    if v == None:
        return nanoid.new()
    if type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": must be a non-empty id string")
    v = v.strip()
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def location_parts(key, name):
    # Parses a LOCATION vertex key: exactly 3 segments vtx.<locationType>.<NanoID>
    # where <locationType> is an admitted location type. A non-3-segment key
    # (e.g. an aspect/link key) or a non-location type is rejected, not silently
    # accepted.
    parts = split_key(key)
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<locationType>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] not in LOCATION_TYPES:
        fail("InvalidArgument: " + name + ": type segment must be one of unit, building, property; got " + key)
    return parts[1], parts[2]

def link_parts(key, name):
    # Parses a containedIn LINK key: exactly 6 segments
    # lnk.<childType>.<childId>.containedIn.<parentType>.<parentId> with the
    # relation segment == containedIn. Any other shape is rejected.
    parts = split_key(key)
    if len(parts) != 6 or parts[0] != "lnk" or parts[3] != "containedIn":
        fail("InvalidArgument: " + name + ": required lnk.<childType>.<childId>.containedIn.<parentType>.<parentId> (exactly 6 segments); got " + key)
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
    # The vertex's root class, or None if absent.
    if key not in state:
        return None
    doc = state[key]
    if doc == None:
        return None
    # "class" is a Starlark reserved word, so it cannot be read via dotted
    # attribute access (doc.class) — getattr with the string key is required.
    if not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

def require_live_location(state, key, name):
    # Endpoint-class validation (the load-bearing containedIn guard): the
    # endpoint MUST be alive AND class=location. A dead or non-location vertex
    # is never wired into the place graph.
    if not vertex_alive(state, key):
        fail("UnknownLocation: " + name + ": " + key + " is absent or tombstoned")
    cls = class_of(state, key)
    if cls != LOCATION_CLASS:
        fail("NotALocation: " + name + ": " + key + " has class " + str(cls) + ", required " + LOCATION_CLASS)

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateLocation":
        lt = required_location_type(p)
        loc_id = bare_nanoid_or_mint(p, "locationId")
        loc_key = "vtx." + lt + "." + loc_id
        # Root data minimal (D5): {} on root; the type is the key segment, the
        # class is the shared LOCATION_CLASS discriminator.
        mutations = [make_vtx(loc_key, LOCATION_CLASS, {})]
        events = [{"class": "location.locationCreated",
                   "data": {"locationKey": loc_key, "locationType": lt}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": loc_key}}

    if ot == "TombstoneLocation":
        loc_key = required_string(p, "locationKey")
        location_parts(loc_key, "locationKey")
        if not vertex_alive(state, loc_key):
            fail("UnknownLocation: " + loc_key)
        mutations = [make_tombstone(loc_key)]
        events = [{"class": "location.locationTombstoned", "data": {"locationKey": loc_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": loc_key}}

    if ot == "WireContainedIn":
        child = required_string(p, "child")
        parent = required_string(p, "parent")
        child_type, child_id = location_parts(child, "child")
        parent_type, parent_id = location_parts(parent, "parent")
        # A location cannot contain itself.
        if child == parent:
            fail("InvalidArgument: child and parent must differ; got " + child)
        # BOTH endpoints alive + class=location (endpoint-class validation at
        # the op, not the lens).
        require_live_location(state, child, "child")
        require_live_location(state, parent, "parent")
        # containedIn direction (Contract #1 §1.1): the contained CHILD is the
        # later-arriving source, the container PARENT is the target. Reads as
        # "child containedIn parent".
        lnk_key = "lnk." + child_type + "." + child_id + ".containedIn." + parent_type + "." + parent_id
        # Idempotent wire: if the link exists alive, return ok no-op. No
        # mutations means no committed key, so no primaryKey is returned (the
        # link key is deterministic and already known to the caller).
        existing = state[lnk_key] if lnk_key in state else None
        if existing != None and not (hasattr(existing, "isDeleted") and existing.isDeleted):
            return {"mutations": [], "events": []}
        mutations = [make_link(lnk_key, child, parent, "containedIn", "containedIn", {})]
        events = [{"class": "location.containedInWired",
                   "data": {"child": child, "parent": parent, "linkKey": lnk_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": lnk_key}}

    if ot == "UnwireContainedIn":
        lnk_key = required_string(p, "linkKey")
        link_parts(lnk_key, "linkKey")
        existing = state[lnk_key] if lnk_key in state else None
        if existing == None or (hasattr(existing, "isDeleted") and existing.isDeleted):
            fail("UnknownLink: " + lnk_key)
        mutations = [make_tombstone(lnk_key)]
        events = [{"class": "location.containedInUnwired", "data": {"linkKey": lnk_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": lnk_key}}

    fail("location DDL: unknown operationType: " + ot)
`
