package locationdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations: the abstract
// `location` type and its three concrete leaves.
//
// The taxonomy (dynamic-type-taxonomy-design.md §3) is four vertexType metas
// joined by three subtypeOf links:
//
//	location (abstract — no script, no permittedCommands, names no instance)
//	  ^          ^          ^
//	unit     building   property   (concrete — each carries the shared script)
//
// Each leaf declares SubtypeOfRef "location", so the installer emits
// `lnk.meta.<leafId>.subtypeOf.meta.<locationId>` into this package's own
// atomic install batch. A lens pattern written `(l:location*)` expands against
// those links to the concrete leaf set {unit, building, property}.
//
// The three leaves share ONE Starlark body (locationDDLScript): the five ops
// are the same for every level, and the level is the key's own type segment
// plus the CreateLocation payload's locationType field.
//
// Because all three leaves admit the same five operationTypes, the Processor's
// operationType→class reverse index treats each of them as ambiguous and drops
// it (internal/processor/ddl_cache.go buildByCommand), so EVERY submitter of a
// location op must name a concrete class explicitly — `"unit"`, `"building"`,
// or `"property"`, matching the key it creates or wires. The abstract
// `"location"` is never a legal envelope class: it declares no script.
//
// Architectural rules (binding — same known-key discipline as service-domain /
// identity-domain):
//
//   - The script reads ONLY by known key. No prefix scans, no adjacency
//     lookups, no lens-output reads. WireContainedIn validates BOTH link
//     endpoints (the child + parent location) by reading each by the key the
//     caller lists in ContextHint.Reads.
//   - Both endpoints of a containedIn link MUST be alive AND keyed with an
//     admitted location type segment: a non-location vertex (or a dead one) is
//     never wired into the place graph (structured ScriptError). Endpoint-type
//     validation is at the op — a downstream cypher rule's untyped match is not
//     relied on.
//   - Canonical-name uniqueness for new location vertices is NOT enforced here.
//     The script assigns a fresh NanoID and writes; locations have no canonical
//     name (the place graph is topology, not a named registry).
//
// Location shape (Contract #6 §6.9 + Contract #1 §1.1 + D5 — root data minimal,
// the class equals the key type):
//
//	vtx.unit.<id>       class=unit       root data = {}
//	vtx.building.<id>   class=building   root data = {}
//	vtx.property.<id>   class=property   root data = {}
//	lnk.<childType>.<childId>.containedIn.<parentType>.<parentId>   class=containedIn
//
// The ADDRESS is the sole authority for a location's type: a cypher label is
// the key type (`:unit`, `:building`, `:property`, or `:location*` for the
// whole family) and never resolves against the body. Every write-path guard
// reads the key's type segment for the same reason — a class comparison cannot
// name the family, and pre-existing locations minted before the taxonomy
// landed carry a class that is not their key type.
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
//     its alive + location-type is validated. The link key is deterministic
//     from the endpoint keys.
//   - UnwireContainedIn: the deterministic containedIn link key (computed from
//     child + parent by the caller — see the key shape above).
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		locationAbstractDDL(),
		locationLeafDDL("unit"),
		locationLeafDDL("building"),
		locationLeafDDL("property"),
	}
}

// LocationTypes are the concrete location levels the abstract `location` type
// covers — the leaf canonicalNames, which are also the `<locationType>` key
// segment in `vtx.<locationType>.<NanoID>` (Contract #6 §6.9).
var LocationTypes = []string{"unit", "building", "property"}

// locationAbstractDDL declares the abstract `location` type: the taxonomy
// parent every concrete level is a subtypeOf. It carries no Script and no
// PermittedCommands — no instance ever keys `vtx.location.<id>` and no
// document ever carries `class: "location"` (dynamic-type-taxonomy-design.md
// §3.2, enforced by the step-6 abstract key-segment and class gates).
//
// It keeps the same self-description aspects a concrete type carries (§3.2 —
// "identical shape, plus an explicit marker"): the schemas below describe the
// location op family this type is the parent of, which its three leaves
// implement.
//
// LeafBudget is 5 — this type's promise to every lens author that
// `(l:location*)` will not expand past five concrete types. Five is §10.1's own
// modeled growth: the three levels shipped here (unit, building, property) plus
// the room and hallway rows that section's table projects. A consuming lens is
// priced against it at THAT lens's own install (pkgmgr's checkLensLabelCap):
// K + 5 ≤ 8 leaves K ≤ 3, so a lens may name three concrete labels of its own
// beside this one and still narrow its Core KV consumer — §10.1's "narrowed,
// zero headroom" row. The number is declared rather than omitted because an
// abstract type that declares none takes the WHOLE label cap as its budget,
// which leaves a consuming lens room for no other concrete label at all.
func locationAbstractDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName: "location",
		Class:         "meta.ddl.vertexType",
		Abstract:      true,
		LeafBudget:    5,
		Description: "Abstract location type — the taxonomy parent of the three concrete location levels " +
			"unit, building and property (each joined by a subtypeOf link). It names no instance: no vertex " +
			"is keyed vtx.location.<NanoID> and no document carries class=location. It declares no script " +
			"and no permittedCommands; the five location operations (CreateLocation, TombstoneLocation, " +
			"WireContainedIn, UnwireContainedIn, SetLocationPresentation) are declared on each concrete leaf, " +
			"and a submitter names the leaf that matches the key it acts on. A lens pattern written " +
			"(l:location*) expands against the subtypeOf links into {unit, building, property}, which is what " +
			"lets one label speak for the whole place graph without hardcoding the levels.",
		InputSchema:      locationInputSchema,
		OutputSchema:     locationOutputSchema,
		FieldDescription: locationFieldDescription(),
		Examples:         locationExamples(),
	}
}

// locationLeafDDL declares one concrete location level (unit / building /
// property) as a subtypeOf the abstract `location` type. All three share
// locationDDLScript — the five ops are identical at every level, and the level
// is the key's own type segment.
func locationLeafDDL(locationType string) pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     locationType,
		Class:             "meta.ddl.vertexType",
		SubtypeOfRef:      "location",
		PermittedCommands: []string{"CreateLocation", "TombstoneLocation", "WireContainedIn", "UnwireContainedIn", "SetLocationPresentation"},
		Description: "Location domain DDL for the `" + locationType + "` level, a subtypeOf the abstract " +
			"`location` type. Vertex shape: vtx." + locationType + ".<NanoID>, class=" + locationType + ", " +
			"root data = {} (minimal, D5) — the class equals the key type. The three location levels " +
			"{unit, building, property} (Contract #6 §6.9) share one script; the level is the key type " +
			"segment plus CreateLocation's locationType payload field. Containment is the containedIn LINK " +
			"(location→location, transitive — unit→building→property). containedIn direction: the contained " +
			"CHILD is the later-arriving source, the container PARENT is the pre-existing target (Contract #1 " +
			"§1.1); the sentence reads 'unit containedIn building'. CreateLocation mints a location vertex of " +
			"the requested type, and — when supplied — writes an optional client-facing display name as the " +
			"location's .presentation aspect (display-name-convention-design.md class 2: nameable business " +
			"vertices ride the proven service-domain {name, description?, icon?, category?} shape; a bare " +
			"NanoID is never a primary label). SetLocationPresentation sets/replaces the .presentation aspect " +
			"on an existing live location (the live-world editor for a name CreateLocation didn't set). " +
			"TombstoneLocation soft-deletes one. WireContainedIn writes the containedIn link only after " +
			"validating BOTH endpoints are alive AND keyed with an admitted location type segment (a " +
			"non-location vertex is never wired into the place graph). UnwireContainedIn tombstones the link " +
			"by its deterministic key.",
		Script:           locationDDLScript,
		InputSchema:      locationInputSchema,
		OutputSchema:     locationOutputSchema,
		FieldDescription: locationFieldDescription(),
		Examples:         locationExamples(),
	}
}

const locationInputSchema = `{"type":"object","properties":` +
	`{"locationType":{"type":"string","enum":["unit","building","property"],"description":"The location level (unit|building|property); sets the vtx.<locationType>.<NanoID> key prefix and the vertex class (CreateLocation)."},` +
	`"locationId":{"type":"string","description":"Optional bare NanoID for the new location vertex (CreateLocation); absent → minted."},` +
	`"presentation":{"type":"object","description":"Optional client-facing display metadata {name, description?, icon?, category?} written as the location's .presentation aspect (CreateLocation and SetLocationPresentation). Absent on CreateLocation → no aspect (undescribed location). Required non-empty on SetLocationPresentation.",` +
	`"properties":{"name":{"type":"string"},"description":{"type":"string"},"icon":{"type":"string"},"category":{"type":"string"}}},` +
	`"locationKey":{"type":"string","description":"vtx.<locationType>.<NanoID> of an existing location (TombstoneLocation and SetLocationPresentation; required, validated alive + an admitted location type segment)."},` +
	`"child":{"type":"string","description":"vtx.<locationType>.<NanoID> of the contained (child) location — the containedIn link source (WireContainedIn; required, validated alive + an admitted location type segment)."},` +
	`"parent":{"type":"string","description":"vtx.<locationType>.<NanoID> of the container (parent) location — the containedIn link target (WireContainedIn; required, validated alive + an admitted location type segment)."},` +
	`"linkKey":{"type":"string","description":"lnk.<childType>.<childId>.containedIn.<parentType>.<parentId> of an existing containedIn link (UnwireContainedIn; required, validated alive)."}},` +
	`"required":[]}`

const locationOutputSchema = `{"type":"object","properties":` +
	`{"primaryKey":{"type":"string","description":"The principal Core KV key the operation wrote: the location vertex key for CreateLocation/TombstoneLocation, or the containedIn link key for WireContainedIn/UnwireContainedIn. Absent on idempotent no-op replays (nothing committed)."}}}`

func locationFieldDescription() map[string]string {
	return map[string]string{
		"locationType": "The location level, one of {unit, building, property}. Determines the vtx.<locationType>.<NanoID> key prefix for CreateLocation; the vertex class equals that same type.",
		"locationId":   "Optional bare NanoID (no dots / key segments) for the new location vertex (vtx.<locationType>.<locationId>). Absent → minted with nanoid.new().",
		"presentation": "Optional client-facing display metadata {name, description?, icon?, category?}. On CreateLocation it is written verbatim as the location's .presentation aspect when supplied (absent → no aspect, an undescribed location — degrade-gracefully). On SetLocationPresentation it is required (a non-empty object with at least one field). Never plaintext PII — locations are non-identity business vertices (display-name-convention-design.md D2).",
		"locationKey":  "Full vtx.<locationType>.<NanoID> key of an existing location vertex — the tombstone target (TombstoneLocation) or the presentation subject (SetLocationPresentation), validated alive + an admitted location type segment.",
		"child":        "Full vtx.<locationType>.<NanoID> key of the contained (child) location. WireContainedIn validates it is alive + an admitted location type segment and writes it as the containedIn link SOURCE (the child is the later-arriving vertex, Contract #1 §1.1).",
		"parent":       "Full vtx.<locationType>.<NanoID> key of the container (parent) location. WireContainedIn validates it is alive + an admitted location type segment and writes it as the containedIn link TARGET.",
		"linkKey":      "Full lnk.<childType>.<childId>.containedIn.<parentType>.<parentId> key of an existing containedIn link to tombstone (UnwireContainedIn).",
	}
}

func locationExamples() []pkgmgr.ExampleSpec {
	return []pkgmgr.ExampleSpec{
		{
			Name:    "CreateLocation — mint a building",
			Payload: map[string]any{"locationType": "building"},
			ExpectedOutcome: "Mints vtx.building.<NanoID> (class=building, root data {}). Returns primaryKey " +
				"(the location key). Accepts an optional caller-supplied bare-NanoID locationId. The envelope " +
				"class must be the concrete leaf `building`.",
		},
		{
			Name:    "CreateLocation — mint a named building",
			Payload: map[string]any{"locationType": "building", "presentation": map[string]any{"name": "Riverside Building", "icon": "building"}},
			ExpectedOutcome: "Mints vtx.building.<NanoID> plus its .presentation aspect {name: \"Riverside Building\", " +
				"icon: \"building\"} (class 2 display source). Returns primaryKey (the location key). An absent or " +
				"empty presentation writes no aspect (the location stays undescribed and renders a typed fallback).",
		},
		{
			Name:    "SetLocationPresentation — name an existing unit",
			Payload: map[string]any{"locationKey": "vtx.unit.<unitNanoID>", "presentation": map[string]any{"name": "Unit 1"}},
			ExpectedOutcome: "Writes/replaces the .presentation aspect {name: \"Unit 1\"} on the live location. Returns " +
				"primaryKey (the location key). Rejects with ScriptError if the location is absent, tombstoned, not " +
				"keyed with an admitted location type segment, or the presentation object is empty.",
		},
		{
			Name:    "WireContainedIn — place a unit inside a building",
			Payload: map[string]any{"child": "vtx.unit.<unitNanoID>", "parent": "vtx.building.<buildingNanoID>"},
			ExpectedOutcome: "Validates both the unit (child) and building (parent) are alive + keyed with an admitted " +
				"location type segment, then writes lnk.unit.<unitNanoID>.containedIn.building.<buildingNanoID> " +
				"(class=containedIn, source=child, target=parent). Returns primaryKey (the link key). Idempotent: a " +
				"replay where the link already exists alive commits nothing and omits primaryKey. Rejects with " +
				"ScriptError if either endpoint is absent, dead, or not a location key.",
		},
		{
			Name:    "UnwireContainedIn — detach a unit from its building",
			Payload: map[string]any{"linkKey": "lnk.unit.<unitNanoID>.containedIn.building.<buildingNanoID>"},
			ExpectedOutcome: "Tombstones the containedIn link. Returns primaryKey (the link key). Rejects with " +
				"ScriptError if the link is absent or already dead.",
		},
	}
}

// locationDDLScript handles the five location ops for all three concrete
// levels. Known-key reads only (WireContainedIn validates both link endpoints
// by the keys the caller listed in ContextHint.Reads). Both endpoints MUST be
// alive + keyed with an admitted location type segment. Root data is minimal
// {} (D5); the class equals the key's type segment.
const locationDDLScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_upsert(vtx_key, local_name, cls, data):
    # Unconditioned full-replace upsert (mirrors clinic-domain's
    # SetSiteProfile/SetProviderProfile): the aspect key is never listed in
    # ContextHint.Reads, so it reaches commit with no expectedRevision and
    # writes whether or not it already exists. A create-only mutation would
    # RevisionConflict on every location that already carries a name.
    return {"op": "update", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

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

# The concrete location levels this package admits (the <locationType> key
# segment in vtx.<locationType>.<NanoID>, Contract #6 §6.9). Each is a declared
# vertex type in its own right, a subtypeOf the abstract 'location' type; one
# script serves all three, and a location vertex's class IS its key type.
LOCATION_TYPES = ["unit", "building", "property"]

# The full set of classes a live location vertex may carry: its own key type,
# the class every location gets.
LOCATION_CLASSES = LOCATION_TYPES

def required_location_type(p):
    lt = required_string(p, "locationType")
    if lt not in LOCATION_TYPES:
        fail("InvalidArgument: locationType: must be one of unit, building, property; got " + lt)
    return lt

# The client-facing display fields a location's .presentation aspect carries
# (display-name-convention-design.md D2: locations are class-2 nameable business
# vertices and ride service-domain's proven {name, description?, icon?,
# category?} shape). Every field is individually optional; a location is a
# non-identity business vertex, so this is a plain mutable label, never PII.
PRESENTATION_FIELDS = ["name", "description", "icon", "category"]

def clean_presentation(d):
    # Returns the non-empty string fields of a presentation object as a plain
    # dict, or None when the object is absent/None/empty (degrade gracefully:
    # an undescribed location writes no aspect). A present-but-non-object value
    # is a structured ScriptError.
    if d == None:
        return None
    if type(d) != type({}):
        fail("InvalidArgument: presentation: must be an object")
    data = {}
    for field in PRESENTATION_FIELDS:
        if field in d and d[field] != None and type(d[field]) == type("") and len(d[field].strip()) > 0:
            data[field] = d[field].strip()
    if len(data) == 0:
        return None
    return data

def optional_presentation(p):
    # CreateLocation's optional presentation: absent → None (no aspect).
    if not hasattr(p, "presentation"):
        return None
    return clean_presentation(getattr(p, "presentation"))

def required_presentation(p):
    # SetLocationPresentation's presentation: a non-empty object is required.
    if not hasattr(p, "presentation"):
        fail("InvalidArgument: presentation: required")
    pres = clean_presentation(getattr(p, "presentation"))
    if pres == None:
        fail("InvalidArgument: presentation: required non-empty object with at least one of name, description, icon, category")
    return pres

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

def key_type_of(key):
    # The type segment of a 3-segment vtx.<type>.<NanoID> key, or None for any
    # other shape (an aspect key, a link key, a malformed string).
    parts = split_key(key)
    if len(parts) != 3 or parts[0] != "vtx":
        return None
    return parts[1]

def class_of(state, key):
    # The vertex's root class, or None if absent. "class" is a Starlark
    # reserved word, so it cannot be read via dotted attribute access
    # (doc.class) — getattr with the string key is required.
    if key not in state:
        return None
    doc = state[key]
    if doc == None:
        return None
    if not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

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

def require_live_location(state, key, name):
    # Endpoint validation (the load-bearing containedIn guard): the endpoint
    # MUST be alive, keyed vtx.<locationType>.<NanoID> at an admitted location
    # level, AND carry a location class. A dead, wrong-typed or wrong-classed
    # vertex is never wired into the place graph.
    #
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

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateLocation":
        lt = required_location_type(p)
        loc_id = bare_nanoid_or_mint(p, "locationId")
        loc_key = "vtx." + lt + "." + loc_id
        # Root data minimal (D5): {} on root; the class equals the key's type
        # segment, so the address is the sole authority for the type.
        mutations = [make_vtx(loc_key, lt, {})]
        # Optional client-facing display name (class-2 .presentation aspect).
        # Absent → no aspect: an undescribed location renders a typed fallback,
        # not "Unnamed" (display-name-convention-design.md renderer floor rule).
        pres = optional_presentation(p)
        if pres != None:
            mutations.append(make_aspect(loc_key, "presentation", "presentation", pres))
        events = [{"class": "location.locationCreated",
                   "data": {"locationKey": loc_key, "locationType": lt}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": loc_key}}

    if ot == "SetLocationPresentation":
        loc_key = required_string(p, "locationKey")
        location_parts(loc_key, "locationKey")
        # The subject MUST be a live location (endpoint-type validation, same
        # guard the wire op applies): a name is never pinned to a dead or
        # non-location vertex.
        require_live_location(state, loc_key, "locationKey")
        pres = required_presentation(p)
        mutations = [make_aspect_upsert(loc_key, "presentation", "presentation", pres)]
        events = [{"class": "location.presentationSet",
                   "data": {"locationKey": loc_key}}]
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
        # BOTH endpoints alive + an admitted location type segment
        # (endpoint-type validation at the op, not the lens).
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
