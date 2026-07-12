package clinicdomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// Canonical names for the multi-site DDLs. clinicSite (vertexType) owns
// SetSiteProfile, which writes the .site aspect (class clinicSiteProfile)
// onto a location-domain vtx.building — mirroring loftspace-domain's
// loftspaceListing aspect-contribution pattern exactly. clinicSiteAssignment
// (vertexType) owns AssignProviderSite / RemoveProviderSite, the provider→
// building practicesAt LINK — mirroring loftspace-domain's loftspaceOwnership
// link-contribution pattern (ownership.go) exactly, including its
// create/revive-CAS/no-op idempotency. Neither package owns a vertex type
// here; both contribute onto location-domain's building (this package now
// depends on location-domain).
const (
	clinicSiteDDL           = "clinicSite"
	clinicSiteProfileAspect = "clinicSiteProfile"
	clinicSiteAssignmentDDL = "clinicSiteAssignment"
)

// clinicSiteVertexTypeDDL declares SetSiteProfile — writes/replaces the
// vtx.building.<id>.site aspect {name} after validating the building is alive
// + class=location (location-domain's shared discriminator). This package
// introduces no vertex type here; it contributes the aspect on top of
// location-domain's building, the same cross-package pattern loftspace-domain
// uses for its .listing/.address aspects.
func clinicSiteVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     clinicSiteDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"SetSiteProfile"},
		Description: "Clinic multi-site profile DDL. SetSiteProfile validates the target vtx.building.<NanoID> is " +
			"alive + class=location (location-domain's shared discriminator; the caller lists it in ContextHint.Reads), " +
			"then REPLACES its .site aspect (class clinicSiteProfile) with {name (required)} — a clinic site/branch " +
			"display name for the site directory and (a later increment's) site-scoped booking picker. This package " +
			"introduces no vertex type here; it contributes the aspect on top of location-domain's building, mirroring " +
			"loftspace-domain's loftspaceListing aspect-contribution pattern.",
		Script: clinicSiteDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"buildingKey":{"type":"string","description":"vtx.building.<NanoID> of an existing location-domain building (SetSiteProfile; required, validated alive + class=location)."},` +
			`"name":{"type":"string","description":"The clinic site/branch display name (SetSiteProfile; required)."}},` +
			`"required":["buildingKey","name"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"The vtx.building.<NanoID> whose .site aspect was written."}}}`,
		FieldDescription: map[string]string{
			"buildingKey": "Full vtx.building.<NanoID> key of an existing location-domain building. Validated alive + class=location. Must be listed in ContextHint.Reads.",
			"name":        "The clinic site/branch display name. Stored on the .site aspect (SetSiteProfile; required — a full replace, so a re-run must resupply it).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "SetSiteProfile — name a clinic site",
				Payload: map[string]any{"buildingKey": "vtx.building.<NanoID>", "name": "Downtown Clinic"},
				ExpectedOutcome: "Validates the building is alive + class=location, then writes vtx.building.<NanoID>.site " +
					"(class clinicSiteProfile) = {name: \"Downtown Clinic\"}. Returns primaryKey (the building key). Rejects " +
					"an absent / dead / non-location building.",
			},
		},
	}
}

// clinicSiteProfileAspectTypeDDL declares the .site aspect (class
// clinicSiteProfile) — the step-6 write gate for SetSiteProfile.
// Declaration-only (the script lives on the clinicSite vertexType DDL).
// Non-sensitive: it attaches to a location-domain building, not an identity.
func clinicSiteProfileAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     clinicSiteProfileAspect,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"SetSiteProfile"},
		Description: "Clinic site profile aspect. Stored as vtx.building.<NanoID>.site (class clinicSiteProfile) = " +
			"{name}. Non-sensitive (attaches to a location-domain building, not an identity). Written ONLY by " +
			"SetSiteProfile (whose clinicSite vertexType DDL owns the script); this aspect-type DDL is the step-6 " +
			"write gate. Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"name":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"name": "The clinic site/branch display name.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "clinic site profile aspect",
				Payload:         map[string]any{"name": "Downtown Clinic"},
				ExpectedOutcome: "Stored as vtx.building.<NanoID>.site; written by SetSiteProfile.",
			},
		},
	}
}

// clinicSiteDDLScript handles SetSiteProfile. Known-key read only (the
// building the caller lists in ContextHint.Reads); unconditioned full-replace
// upsert (mirrors SetProviderProfile), so a re-run must resupply name.
const clinicSiteDDLScript = `
def make_aspect_upsert(vtx_key, local_name, cls, data):
    return {"op": "update", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

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
    if key not in state:
        return None
    doc = state[key]
    if doc == None or not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

def require_live_building(state, key):
    if not vertex_alive(state, key):
        fail("UnknownBuilding: buildingKey: " + key + " is absent or tombstoned")
    cls = class_of(state, key)
    if cls != "location":
        fail("NotALocation: buildingKey: " + key + " has class " + str(cls) + ", required location")

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "SetSiteProfile":
        building_key = required_string(p, "buildingKey")
        name = required_string(p, "name")
        require_live_building(state, building_key)
        mutations = [make_aspect_upsert(building_key, "site", "clinicSiteProfile", {"name": name})]
        events = [{"class": "clinic.siteProfileSet", "data": {"buildingKey": building_key, "name": name}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": building_key}}

    fail("clinicSite DDL: unknown operationType: " + ot)
`

// clinicSiteAssignmentVertexTypeDDL declares AssignProviderSite /
// RemoveProviderSite — the provider→building practicesAt LINK
// (lnk.provider.<providerID>.practicesAt.building.<buildingID>, class
// "practicesAt"). Mirrors loftspace-domain's loftspaceOwnership DDL exactly
// (ownership.go): both endpoints pre-exist, so per Contract #1 §1.1 the
// acting provider (being assigned to the site) is the source, the building
// the target. Reads as "this provider practicesAt this building." A provider
// may practice at MANY sites and a site may host MANY providers — the
// deterministic per-pair key is the uniqueness constraint (no list needed),
// so AssignProviderSite reads the one key on demand and creates / revives /
// no-ops; RemoveProviderSite is the reversible complement.
func clinicSiteAssignmentVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     clinicSiteAssignmentDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"AssignProviderSite", "RemoveProviderSite"},
		Description: "Clinic provider-site assignment DDL. Owns AssignProviderSite + RemoveProviderSite, which write / " +
			"tombstone the provider→building LINK lnk.provider.<providerID>.practicesAt.building.<buildingID> (class " +
			"\"practicesAt\", reads as \"provider practicesAt building\"; source = the provider, target = the building — " +
			"the acting provider is the later-arriving fact, mirroring loftspace-domain's AssignUnitOwner). " +
			"AssignProviderSite validates the provider is an alive vtx.provider and the building an alive " +
			"vtx.building of class=location (both listed in ContextHint.Reads), then reads the deterministic per-pair " +
			"link key ON DEMAND (kv.Read) and creates it (absent), revives it via CAS (tombstoned by a prior " +
			"RemoveProviderSite), or no-ops (already live — idempotent at-least-once). RemoveProviderSite tombstones " +
			"the same link (idempotent: absent / already-tombstoned → clean no-op). A provider may practice at many " +
			"sites; a site may host many providers. The link needs no link-type DDL (it resolves to the step-6 " +
			"permissive default). This package introduces no vertex type; it contributes the link on top of its own " +
			"provider and location-domain's building.",
		Script: clinicSiteAssignmentDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"provider":{"type":"string","description":"vtx.provider.<NanoID> of the provider (required; validated alive). Listed in ContextHint.Reads."},` +
			`"building":{"type":"string","description":"vtx.building.<NanoID> of an existing location-domain building (required; validated alive + class=location). Listed in ContextHint.Reads."}},` +
			`"required":["provider","building"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"The practicesAt link key written (lnk.provider.<providerID>.practicesAt.building.<buildingID>) on create / revive / tombstone; omitted on an idempotent no-op."}}}`,
		FieldDescription: map[string]string{
			"provider": "Full vtx.provider.<NanoID> key of the provider. AssignProviderSite validates it is alive and uses it as the practicesAt link's source; RemoveProviderSite reconstructs the link key from it. MUST be listed in ContextHint.Reads (AssignProviderSite).",
			"building": "Full vtx.building.<NanoID> key of the location-domain building. AssignProviderSite validates it is alive + class=location and uses it as the practicesAt link's target. MUST be listed in ContextHint.Reads (AssignProviderSite).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "AssignProviderSite — record that a provider practices at a site",
				Payload: map[string]any{
					"provider": "vtx.provider.<providerNanoID>",
					"building": "vtx.building.<buildingNanoID>",
				},
				ExpectedOutcome: "Validates the provider is alive and the building an alive class=location building, then " +
					"reads lnk.provider.<providerNanoID>.practicesAt.building.<buildingNanoID> on demand and creates it " +
					"(class \"practicesAt\"). Returns primaryKey (the link). Re-running is idempotent: already-live → clean " +
					"no-op; a link a prior RemoveProviderSite tombstoned is revived (CAS-guarded). Rejects a dead / wrong-" +
					"class provider or building.",
			},
			{
				Name: "RemoveProviderSite — revoke a provider's assignment to a site",
				Payload: map[string]any{
					"provider": "vtx.provider.<providerNanoID>",
					"building": "vtx.building.<buildingNanoID>",
				},
				ExpectedOutcome: "Reconstructs the practicesAt link key, reads it on demand, and tombstones it. Idempotent: " +
					"an absent / already-tombstoned link → clean no-op (empty response).",
			},
		},
	}
}

// clinicSiteAssignmentDDLScript handles AssignProviderSite +
// RemoveProviderSite. The provider + building are validated by the keys the
// caller lists in ContextHint.Reads (AssignProviderSite); the per-pair
// practicesAt link is read ON DEMAND (kv.Read) — it may not exist yet, so a
// declared read would HydrationMiss (mirrors loftspace-domain's
// loftspaceOwnershipDDLScript exactly).
const clinicSiteAssignmentDDLScript = `
def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def make_link_revive_occ(key, source, target, cls, local_name, expected_revision):
    return {"op": "update", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": {}},
            "expectedRevision": expected_revision}

def make_link_tombstone(key, source, target, cls, local_name):
    return {"op": "update", "key": key,
            "document": {"class": cls, "isDeleted": True,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": {}}}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def vertex_parts(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx" or parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID> (exactly 3 segments); got " + key)
    if parts[2] == "":
        fail("InvalidArgument: " + name + ": empty id segment; required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[2]

def vertex_alive(state, key):
    if key not in state or state[key] == None:
        return False
    doc = state[key]
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def class_of(state, key):
    if key not in state:
        return None
    doc = state[key]
    if doc == None or not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

def require_live_building(state, key):
    if not vertex_alive(state, key):
        fail("UnknownBuilding: building: " + key + " is absent or tombstoned")
    cls = class_of(state, key)
    if cls != "location":
        fail("NotALocation: building: " + key + " has class " + str(cls) + ", required location")

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "AssignProviderSite":
        provider = required_string(p, "provider")
        provider_id = vertex_parts(provider, "provider", "provider")
        building = required_string(p, "building")
        building_id = vertex_parts(building, "building", "building")

        if not vertex_alive(state, provider):
            fail("UnknownProvider: " + provider + " is absent or tombstoned")
        require_live_building(state, building)

        # read-posture: (d) declared optionalReads at AssignProviderSite dispatch
        # (create/revive idempotency branch).
        link_key = "lnk.provider." + provider_id + ".practicesAt.building." + building_id
        existing = kv.Read(link_key)
        if existing != None and not existing.isDeleted:
            return {"mutations": [], "events": [], "response": {}}
        if existing != None:
            link_mut = make_link_revive_occ(link_key, provider, building, "practicesAt", "practicesAt", existing.revision)
        else:
            link_mut = make_link(link_key, provider, building, "practicesAt", "practicesAt", {})

        events = [{"class": "clinic.providerSiteAssigned", "data": {"provider": provider, "building": building}}]
        return {"mutations": [link_mut], "events": events,
                "response": {"primaryKey": link_key}}

    if ot == "RemoveProviderSite":
        provider = required_string(p, "provider")
        provider_id = vertex_parts(provider, "provider", "provider")
        building = required_string(p, "building")
        building_id = vertex_parts(building, "building", "building")

        # read-posture: (d) declared optionalReads at RemoveProviderSite dispatch
        # (revoke idempotency branch).
        link_key = "lnk.provider." + provider_id + ".practicesAt.building." + building_id
        existing = kv.Read(link_key)
        if existing == None or existing.isDeleted:
            return {"mutations": [], "events": [], "response": {}}

        link_mut = make_link_tombstone(link_key, provider, building, "practicesAt", "practicesAt")
        events = [{"class": "clinic.providerSiteRemoved", "data": {"provider": provider, "building": building}}]
        return {"mutations": [link_mut], "events": events,
                "response": {"primaryKey": link_key}}

    fail("clinicSiteAssignment DDL: unknown operationType: " + ot)
`
