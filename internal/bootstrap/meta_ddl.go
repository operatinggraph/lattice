package bootstrap

// Story 4.7 — the kernel's sole DDL. Governs all vtx.meta.* mutations
// via three operations: CreateMetaVertex, UpdateMetaVertex,
// TombstoneMetaVertex.
//
// Dispatch by `op.payload.targetClass`:
//
//   - meta.ddl.vertexType / meta.ddl.linkType / meta.ddl.aspectType /
//     meta.ddl.eventType → expects canonicalName, permittedCommands,
//     description, script aspects in op.payload.aspects; assigns a fresh
//     NanoID and writes the vertex + 4 aspects.
//   - meta.lens → expects canonicalName, description, spec aspects (and
//     optional adapter / bucket / engine); writes vertex + aspects.
//   - any other targetClass → fails with UnknownMetaClass.
//
// Phase 1 scope: this script is **not yet wired** through Processor —
// Story 5.3 routes package installs through CreateMetaVertex ops once
// the compensating-ops machinery lands. The script is seeded now so the
// kernel's read surface is internally consistent (every vtx.meta.* has
// a DDL governing it).
//
// Read surface: known-key only. The script never enumerates; it only
// reads the existing vertex key when an Update/Tombstone op targets one.
// Caller must declare the target key in ContextHint.Reads.
//
// LOC target ≈ 200.

// MetaRootDDLScript is the Starlark source for the kernel meta-meta-DDL.
const MetaRootDDLScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(key, vkey, local_name, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "vertexKey": vkey, "localName": local_name,
                         "isDeleted": False, "data": data}}

def make_update(key, data):
    return {"op": "update", "key": key,
            "document": {"isDeleted": False, "data": data}}

def make_tombstone(key):
    return {"op": "tombstone", "key": key,
            "document": {"isDeleted": True, "data": {}}}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def need_str(p, name):
    if not hasattr(p, name):
        fail("MissingSelfDescription: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("MissingSelfDescription: " + name + ": required non-empty string")
    return v.strip()

def required_dict(p, name):
    if not hasattr(p, name):
        fail("MissingSelfDescription: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type({}):
        fail("MissingSelfDescription: " + name + ": required non-empty object")
    return v

def required_list(p, name):
    if not hasattr(p, name):
        fail("MissingSelfDescription: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type([]):
        fail("MissingSelfDescription: " + name + ": required list")
    return v

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

ALLOWED_DDL_CLASSES = ["meta.ddl.vertexType", "meta.ddl.linkType",
                       "meta.ddl.aspectType", "meta.ddl.eventType"]

def is_ddl_class(c):
    for x in ALLOWED_DDL_CLASSES:
        if x == c:
            return True
    return False

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateMetaVertex":
        target_class = required_string(p, "targetClass")
        canonical_name = required_string(p, "canonicalName")
        if is_ddl_class(target_class):
            # DDL meta-vertex: requires permittedCommands + all 5 self-description aspects.
            permitted = p.permittedCommands if hasattr(p, "permittedCommands") else None
            if permitted == None or type(permitted) != type([]):
                fail("InvalidArgument: permittedCommands: required list of strings")
            for c in permitted:
                if type(c) != type(""):
                    fail("InvalidArgument: permittedCommands: each entry must be a string")
            description   = required_string(p, "description")
            script_src    = required_string(p, "script")
            input_schema  = need_str(p, "inputSchema")
            output_schema = need_str(p, "outputSchema")
            field_desc    = required_dict(p, "fieldDescription")
            examples      = required_list(p, "examples")

            meta_id = nanoid.new()
            meta_key = "vtx.meta." + meta_id
            mutations = [
                make_vtx(meta_key, target_class, {}),
                make_aspect(meta_key + ".canonicalName", meta_key, "canonicalName",
                            "canonicalName", {"value": canonical_name}),
                make_aspect(meta_key + ".permittedCommands", meta_key, "permittedCommands",
                            "permittedCommands", {"commands": permitted}),
                make_aspect(meta_key + ".description", meta_key, "description",
                            "description", {"text": description}),
                make_aspect(meta_key + ".script", meta_key, "script",
                            "script", {"source": script_src}),
                make_aspect(meta_key + ".inputSchema", meta_key, "inputSchema",
                            "inputSchema", {"schema": input_schema}),
                make_aspect(meta_key + ".outputSchema", meta_key, "outputSchema",
                            "outputSchema", {"schema": output_schema}),
                make_aspect(meta_key + ".fieldDescription", meta_key, "fieldDescription",
                            "fieldDescription", {"fieldDescriptions": field_desc}),
                make_aspect(meta_key + ".examples", meta_key, "examples",
                            "examples", {"examples": examples}),
            ]
            events = [{"class": "MetaVertexCreated",
                       "data": {"metaKey": meta_key, "targetClass": target_class,
                                "canonicalName": canonical_name}}]
            return {"mutations": mutations, "events": events,
                    "response": {"metaKey": meta_key}}

        if target_class == "meta.lens":
            description = required_string(p, "description")
            spec = required_string(p, "spec")
            adapter = ""
            bucket = ""
            engine = ""
            if hasattr(p, "adapter") and type(p.adapter) == type(""):
                adapter = p.adapter
            if hasattr(p, "bucket") and type(p.bucket) == type(""):
                bucket = p.bucket
            if hasattr(p, "engine") and type(p.engine) == type(""):
                engine = p.engine

            meta_id = nanoid.new()
            meta_key = "vtx.meta." + meta_id
            mutations = [
                make_vtx(meta_key, "meta.lens", {}),
                make_aspect(meta_key + ".canonicalName", meta_key, "canonicalName",
                            "canonicalName", {"value": canonical_name}),
                make_aspect(meta_key + ".description", meta_key, "description",
                            "description", {"text": description}),
                make_aspect(meta_key + ".spec", meta_key, "spec", "lensSpec",
                            {"source": spec, "adapter": adapter, "bucket": bucket, "engine": engine}),
            ]
            events = [{"class": "MetaVertexCreated",
                       "data": {"metaKey": meta_key, "targetClass": "meta.lens",
                                "canonicalName": canonical_name}}]
            return {"mutations": mutations, "events": events,
                    "response": {"metaKey": meta_key}}

        fail("UnknownMetaClass: " + target_class)

    if ot == "UpdateMetaVertex":
        meta_key = required_string(p, "metaKey")
        if not vertex_alive(state, meta_key):
            fail("UnknownMetaVertex: " + meta_key)
        # Update description aspect (the structural-stable target).
        desc = ""
        if hasattr(p, "description") and type(p.description) == type(""):
            desc = p.description
        mutations = [
            make_update(meta_key + ".description",
                {"text": desc}),
        ]
        events = [{"class": "MetaVertexUpdated", "data": {"metaKey": meta_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"metaKey": meta_key}}

    if ot == "TombstoneMetaVertex":
        meta_key = required_string(p, "metaKey")
        if not vertex_alive(state, meta_key):
            fail("UnknownMetaVertex: " + meta_key)
        mutations = [make_tombstone(meta_key)]
        events = [{"class": "MetaVertexTombstoned", "data": {"metaKey": meta_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"metaKey": meta_key}}

    fail("root DDL: unknown operationType: " + ot)
`
