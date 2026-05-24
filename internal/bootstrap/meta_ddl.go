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
//   - meta.lens → expects canonicalName, description, and spec fields.
//     spec must be a JSON string containing a LensSpec object with at
//     least cypherRule, targetType, and targetConfig fields; the script
//     decodes it with json.decode() and stores the resulting dict verbatim
//     as the .spec aspect data so Refractor's CoreKVSource can unmarshal
//     a LensSpec directly from the aspect.
//   - any other targetClass → fails with UnknownMetaClass.
//
// Phase 1 scope: wired through Processor for the meta lane (ops.meta.*).
// Story 5.3 adds the .compensation sixth self-description aspect and
// optional expectedRevision conflict detection to Update + Tombstone
// branches. The compensating operation contract lives entirely in the
// .compensation aspect; OperationReply carries no new fields (Guardrail 1).
//
// Read surface: known-key only. The script never enumerates; it only
// reads the existing vertex key when an Update/Tombstone op targets one.
// Caller must declare the target key in ContextHint.Reads.
//
// LOC target ≈ 200.

// MetaRootDDLScript is the Starlark source for the kernel meta-meta-DDL.
//
// Story 5.3 additions:
//   - CreateMetaVertex emits a sixth .compensation aspect alongside the
//     five Story 5.1 self-description aspects. The compensation data
//     encodes the inverse operation as template references so no new
//     OperationReply fields are needed (Guardrail 1).
//   - TombstoneMetaVertex accepts an optional expectedRevision field for
//     compensating-op conflict detection (Starlark pre-flight; the
//     substrate AtomicBatch provides the binding revision assertion).
//   - UpdateMetaVertex similarly accepts expectedRevision, and also
//     updates the .compensation aspect with the prior description so that
//     the next rollback restores the correct prior state.
//
// The rejected compensatingOperation OperationReply envelope field
// (originally proposed in epics.md §Story 5.3) is NOT implemented here.
// See Spec Deviations in the Story 5.3 brief for the rationale.
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
                # Story 5.3: sixth self-description aspect — .compensation.
                # Template variables are resolved client-side from the standard
                # commit response fields (OperationReply.Detail + .Revisions).
                # No new OperationReply fields (Guardrail 1).
                make_aspect(meta_key + ".compensation", meta_key, "compensation",
                            "compensation",
                            {"inverseOperationType": "TombstoneMetaVertex",
                             "payloadTemplate": {"metaKey": "{{detail.metaKey}}"},
                             "revisionTemplate": {"metaKey": "{{revisions[detail.metaKey]}}"}}),
            ]
            events = [{"class": "MetaVertexCreated",
                       "data": {"metaKey": meta_key, "targetClass": target_class,
                                "canonicalName": canonical_name}}]
            return {"mutations": mutations, "events": events,
                    "response": {"metaKey": meta_key}}

        if target_class == "meta.lens":
            description = required_string(p, "description")
            spec_str = required_string(p, "spec")
            spec_obj = json.decode(spec_str)
            if type(spec_obj) != type({}):
                fail("InvalidArgument: spec: must be a JSON object string")
            if "cypherRule" not in spec_obj:
                fail("InvalidArgument: spec.cypherRule: required")
            if "targetType" not in spec_obj:
                fail("InvalidArgument: spec.targetType: required (postgres|nats_kv)")
            if "targetConfig" not in spec_obj:
                fail("InvalidArgument: spec.targetConfig: required")

            meta_id = nanoid.new()
            meta_key = "vtx.meta." + meta_id
            mutations = [
                make_vtx(meta_key, "meta.lens", {}),
                make_aspect(meta_key + ".canonicalName", meta_key, "canonicalName",
                            "canonicalName", {"value": canonical_name}),
                make_aspect(meta_key + ".description", meta_key, "description",
                            "description", {"text": description}),
                make_aspect(meta_key + ".spec", meta_key, "spec", "lensSpec", spec_obj),
                make_aspect(meta_key + ".compensation", meta_key, "compensation",
                            "compensation",
                            {"inverseOperationType": "TombstoneMetaVertex",
                             "payloadTemplate": {"metaKey": "{{detail.metaKey}}"},
                             "revisionTemplate": {"metaKey": "{{revisions[detail.metaKey]}}"}}),
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
        # Story 5.3: read prior description from state for .compensation aspect.
        # Caller must declare meta_key + ".description" in ContextHint.Reads.
        # state entries are structs; the .data field is a dict (key access).
        prior_desc = ""
        desc_key = meta_key + ".description"
        if desc_key in state and state[desc_key] != None:
            d = state[desc_key]
            if hasattr(d, "data") and type(d.data) == type({}) and "text" in d.data:
                prior_desc = d.data["text"]
        force = hasattr(p, "force") and p.force == True
        mutations = [
            make_update(meta_key + ".description", {"text": desc}),
            # Story 5.3: update .compensation to reflect post-update inverse.
            # prior_desc is the concrete value read from state at execution time
            # (no template substitution needed for the description field).
            make_update(meta_key + ".compensation",
                {"inverseOperationType": "UpdateMetaVertex",
                 "payloadTemplate": {"metaKey": meta_key, "description": prior_desc},
                 "revisionTemplate": {}}),
        ]
        if hasattr(p, "expectedRevision") and not force:
            expected_rev = p.expectedRevision
            if type(expected_rev) != type(0):
                fail("InvalidArgument: expectedRevision must be an integer")
            # SC-1 (Story 5.3): only apply expectedRevision to the description
            # mutation (mutations[0]). The .compensation aspect has its own
            # independent NATS revision sequence — applying the same revision
            # would cause spurious RevisionConflict after the first update.
            mutations[0]["expectedRevision"] = expected_rev
        events = [{"class": "MetaVertexUpdated", "data": {"metaKey": meta_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"metaKey": meta_key}}

    if ot == "TombstoneMetaVertex":
        meta_key = required_string(p, "metaKey")
        if not vertex_alive(state, meta_key):
            fail("UnknownMetaVertex: " + meta_key)
        # Story 5.3: optional expectedRevision for compensating-op conflict detection.
        # force=True skips the revision assertion (last-writer-wins merge policy).
        force = hasattr(p, "force") and p.force == True
        # MF-2 (Story 5.3): also update the .compensation aspect to record
        # that this tombstone is irreversible in Phase 1 (AC3).
        mutations = [
            make_tombstone(meta_key),
            make_update(meta_key + ".compensation",
                {"inverseOperationType": "none",
                 "note": "Tombstone is irreversible in Phase 1; operator must recreate via CreateMetaVertex with prior payload."}),
        ]
        if hasattr(p, "expectedRevision") and not force:
            expected_rev = p.expectedRevision
            if type(expected_rev) != type(0):
                fail("InvalidArgument: expectedRevision must be an integer")
            # Propagate revision assertion to the substrate AtomicBatch layer
            # (CommitterImpl already handles mutation["expectedRevision"] at
            # step8_commit.go lines 131-140 — no Committer changes needed).
            mutations[0]["expectedRevision"] = expected_rev
        events = [{"class": "MetaVertexTombstoned", "data": {"metaKey": meta_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"metaKey": meta_key}}

    fail("root DDL: unknown operationType: " + ot)
`
