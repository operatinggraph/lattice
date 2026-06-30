package servicelocation

import "github.com/asolgan/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations.
//
// Single DDL `serviceLocation` (link-op class) handles all eight link ops that
// wire the residence-based service-access topology:
//
//	WireResidesIn, UnwireResidesIn               # identity → location
//	WireAvailableAt, UnwireAvailableAt           # service-template → location
//	WireUnavailableAt, UnwireUnavailableAt       # service-template → location
//	WirePermitsOperation, UnwirePermitsOperation # service → op-meta
//
// Architectural rules (binding — same known-key discipline as location-domain /
// service-domain):
//
//   - The script reads ONLY by known key. No prefix scans, no adjacency
//     lookups, no lens-output reads. Each wire op validates its link endpoints
//     by reading each by the key the caller lists in ContextHint.Reads.
//   - Endpoint-class validation is AT THE OP (not the lens's untyped match):
//     residesIn target MUST be class=location; availableAt / unavailableAt
//     source MUST be a service TEMPLATE (a service whose ENVELOPE class
//     ends in `.template`) and target MUST be class=location; permitsOperation
//     source MUST be class=service and target MUST be an op-meta vertex
//     (vtx.meta.<id> carrying a data.operationType). A dead or wrong-class
//     endpoint is never wired (structured ScriptError).
//   - residesIn cardinality: MULTIPLE allowed — an identity may reside in many
//     locations; the lens's fresh-var exclusion is residence-set-aware.
//
// Link direction follows Contract #1 §1.1 (the later-arriving vertex is the
// SOURCE) and reads as a sentence:
//
//	lnk.<idType>.<idId>.residesIn.<locType>.<locId>            # "identity residesIn location"
//	lnk.service.<tplId>.availableAt.<locType>.<locId>          # "service availableAt location"
//	lnk.service.<tplId>.unavailableAt.<locType>.<locId>        # "service unavailableAt location"
//	lnk.service.<svcId>.permitsOperation.meta.<opId>           # "service permitsOperation operation"
//
// Caller's ContextHint.Reads MUST include:
//   - WireResidesIn: BOTH endpoints (the identity + the location).
//   - WireAvailableAt / WireUnavailableAt: the service template (its root
//     envelope class is the discriminator the template guard reads) + the location.
//   - WirePermitsOperation: the service + the op-meta vertex.
//   - the Unwire* ops: the deterministic link key (computed from the endpoints
//     by the caller — see the key shapes above).
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{serviceLocationDDL()}
}

func serviceLocationDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName: "serviceLocation",
		Class:         "meta.ddl.vertexType",
		PermittedCommands: []string{
			"WireResidesIn", "UnwireResidesIn",
			"WireAvailableAt", "UnwireAvailableAt",
			"WireUnavailableAt", "UnwireUnavailableAt",
			"WirePermitsOperation", "UnwirePermitsOperation",
		},
		Description: "Service-location scheme DDL. Wires the residence-based service-access topology " +
			"(the cap.svc grant source) as LINKS: residesIn (identity→location: where an actor lives), " +
			"availableAt (service-template→location: where an offering is available), unavailableAt " +
			"(service-template→location: an explicit exclusion override), permitsOperation " +
			"(service→op-meta: which operations a service exposes). All links: the later-arriving vertex is " +
			"the source, the pre-existing one is the target (Contract #1 §1.1); the sentence reads 'identity " +
			"residesIn location', 'service availableAt location'. Each Wire op validates its endpoint classes " +
			"at the op (residesIn target=location; availableAt/unavailableAt source=a service template " +
			"[its envelope class ends in .template], target=location; permitsOperation source=service, " +
			"target=an op-meta vertex). residesIn cardinality is multiple. Each Unwire op tombstones the link " +
			"by its deterministic key.",
		Script: serviceLocationDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"identity":{"type":"string","description":"vtx.identity.<NanoID> of the resident — the residesIn link source (WireResidesIn; required, validated alive)."},` +
			`"location":{"type":"string","description":"vtx.<locType>.<NanoID> of the location (WireResidesIn target / WireAvailableAt target / WireUnavailableAt target; required, validated alive + class=location)."},` +
			`"service":{"type":"string","description":"vtx.service.<NanoID> of the service — a template for WireAvailableAt/WireUnavailableAt (validated alive + a template, envelope class ends in .template), or any service for WirePermitsOperation (validated alive + a service.* envelope class)."},` +
			`"operation":{"type":"string","description":"vtx.meta.<NanoID> of the op-meta vertex the service exposes — the permitsOperation link target (WirePermitsOperation; required, validated alive + carries data.operationType)."},` +
			`"linkKey":{"type":"string","description":"The deterministic 6-segment link key of an existing link to tombstone (the Unwire ops; required, validated alive)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"The link key the operation wrote (Wire ops) or tombstoned (Unwire ops). Absent on idempotent no-op replays (nothing committed)."}}}`,
		FieldDescription: map[string]string{
			"identity":  "Full vtx.identity.<NanoID> key of the resident. WireResidesIn validates it is alive and writes it as the residesIn link SOURCE (the identity is the later-arriving vertex, Contract #1 §1.1).",
			"location":  "Full vtx.<locType>.<NanoID> key of the location. Validated alive + class=location; written as the link TARGET for residesIn / availableAt / unavailableAt.",
			"service":   "Full vtx.service.<NanoID> key. For availableAt/unavailableAt it MUST be a service template (its envelope class ends in .template); for permitsOperation it MUST be a service.* envelope class. Written as the link SOURCE.",
			"operation": "Full vtx.meta.<NanoID> key of the op-meta vertex the service exposes. WirePermitsOperation validates it is alive and carries a data.operationType, then writes it as the permitsOperation link TARGET.",
			"linkKey":   "Full 6-segment link key of an existing link to tombstone (the Unwire ops).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "WireResidesIn — place a resident in a unit",
				Payload: map[string]any{"identity": "vtx.identity.<idNanoID>", "location": "vtx.unit.<unitNanoID>"},
				ExpectedOutcome: "Validates the identity (alive) and the unit (alive + class=location), then writes " +
					"lnk.identity.<idNanoID>.residesIn.unit.<unitNanoID> (class=residesIn, source=identity, target=location). " +
					"Returns primaryKey (the link key). Idempotent: a replay where the link already exists alive commits " +
					"nothing and omits primaryKey. residesIn cardinality is multiple (an identity may reside in many locations).",
			},
			{
				Name:    "WireAvailableAt — make a laundry service available at a building",
				Payload: map[string]any{"service": "vtx.service.<laundryTplNanoID>", "location": "vtx.building.<buildingNanoID>"},
				ExpectedOutcome: "Validates the service is alive + a template (its envelope class ends in .template) and the " +
					"building is alive + class=location, then writes lnk.service.<laundryTplNanoID>.availableAt.building.<buildingNanoID> " +
					"(class=availableAt, source=service, target=location). Returns primaryKey. Rejects with ScriptError if the " +
					"service is not a template or the location is not class=location.",
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

# The location types this scheme admits as link endpoints (the <locType> key
# segment in vtx.<locType>.<NanoID>, Contract #6 §6.9). location-domain owns
# the vertices; the scheme references them by class.
LOCATION_CLASS = "location"

def vertex_parts(key, name):
    # Parses a VERTEX key: exactly 3 segments vtx.<type>.<NanoID>. A non-3
    # segment key (e.g. an aspect/link key) is rejected, not silently truncated.
    parts = split_key(key)
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    return parts[1], parts[2]

def typed_vertex_parts(key, name, want_type):
    vtype, vid = vertex_parts(key, name)
    if vtype != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return vtype, vid

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
    if not vertex_alive(state, key):
        fail("UnknownLocation: " + name + ": " + key + " is absent or tombstoned")
    cls = class_of(state, key)
    if cls != LOCATION_CLASS:
        fail("NotALocation: " + name + ": " + key + " has class " + str(cls) + ", required " + LOCATION_CLASS)

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

def wire(state, src, target, relation, src_type, src_id, tgt_type, tgt_id):
    # Idempotent wire: if the link exists alive, return ok no-op (no committed
    # key → no primaryKey). The link key is deterministic and known to the caller.
    lnk_key = "lnk." + src_type + "." + src_id + "." + relation + "." + tgt_type + "." + tgt_id
    existing = state[lnk_key] if lnk_key in state else None
    if existing != None and not (hasattr(existing, "isDeleted") and existing.isDeleted):
        return lnk_key, []
    return lnk_key, [make_link(lnk_key, src, target, relation, relation, {})]

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
        id_type, id_id = typed_vertex_parts(identity, "identity", "identity")
        loc_type, loc_id = vertex_parts(location, "location")
        if not vertex_alive(state, identity):
            fail("UnknownIdentity: identity: " + identity + " is absent or tombstoned")
        require_live_location(state, location, "location")
        lnk_key, mutations = wire(state, identity, location, "residesIn", id_type, id_id, loc_type, loc_id)
        if len(mutations) == 0:
            return {"mutations": [], "events": []}
        events = [{"class": "serviceLocation.residesInWired",
                   "data": {"identity": identity, "location": location, "linkKey": lnk_key}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": lnk_key}}

    if ot == "WireAvailableAt":
        service = required_string(p, "service")
        location = required_string(p, "location")
        _, svc_id = typed_vertex_parts(service, "service", "service")
        loc_type, loc_id = vertex_parts(location, "location")
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
        _, svc_id = typed_vertex_parts(service, "service", "service")
        loc_type, loc_id = vertex_parts(location, "location")
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
        _, svc_id = typed_vertex_parts(service, "service", "service")
        _, op_id = typed_vertex_parts(operation, "operation", "meta")
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
