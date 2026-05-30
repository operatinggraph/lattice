package bootstrap

// MetaRootDDLScript — the kernel's sole DDL. Governs all vtx.meta.*
// mutations via three operations: CreateMetaVertex, UpdateMetaVertex,
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
// CreateMetaVertex emits a .compensation aspect alongside the five
// self-description aspects. The compensation data encodes the inverse
// operation as template references so no new OperationReply fields are
// needed (Guardrail 1).
//
// TombstoneMetaVertex and UpdateMetaVertex each accept an optional
// expectedRevision field for conflict detection.
//
// UpdateMetaVertex mutates only the self-description aspects present in the
// payload, preserving the meta-vertex's metaKey identity and canonicalName.
// For DDL classes the updatable set is {description, script,
// permittedCommands, inputSchema, outputSchema, fieldDescription, examples};
// for meta.lens it is {description, spec}. canonicalName is immutable and
// ignored if supplied; an empty update (no updatable field) is rejected.
// .compensation captures the prior values of exactly the changed fields so
// the next rollback restores the correct prior state. expectedRevision, when
// present, is applied to the first present field in canonical order only —
// each aspect has its own NATS revision sequence, so multi-aspect atomic OCC
// is a Phase-2 limitation. The caller must declare meta_key+".<field>" for
// each field it updates in ContextHint.Reads.
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

def is_protected(state, key):
    # A primordial/kernel meta-vertex carries data.protected == True in its
    # root document. Such entities (the meta-root DDL itself, the
    # Install/Uninstall DDLs, the Capability lenses, ...) must never be
    # tombstoned or updated — doing so could disable auth or the kernel
    # (1.5.2 kernel-protection residual). The caller must declare meta_key
    # in ContextHint.Reads, which vertex_alive already requires, so the
    # root doc is in state.
    if key not in state or state[key] == None:
        return False
    doc = state[key]
    if not hasattr(doc, "data") or type(doc.data) != type({}):
        return False
    if "protected" not in doc.data:
        return False
    return doc.data["protected"] == True

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
                # .compensation encodes the inverse operation as template
                # references resolved client-side (Guardrail 1 — no new
                # OperationReply fields).
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
        if is_protected(state, meta_key):
            fail("ProtectedMetaVertex: " + meta_key)

        # The meta-vertex class selects the updatable field set. DDL classes
        # expose all self-description aspects; meta.lens exposes description
        # and spec. canonicalName is the stable logical identity and is never
        # mutated here (ignored if supplied). compensation is script-managed.
        target_class = ""
        root = state[meta_key]
        if hasattr(root, "class"):
            target_class = getattr(root, "class")
        is_lens = target_class == "meta.lens"

        # Read the prior value of an aspect's data field from state for the
        # .compensation payloadTemplate. Caller must declare meta_key+".<field>"
        # in ContextHint.Reads. state entries are structs; .data is a dict.
        def prior_data_field(suffix, data_field):
            akey = meta_key + suffix
            if akey not in state or state[akey] == None:
                return None
            d = state[akey]
            if not hasattr(d, "data") or type(d.data) != type({}):
                return None
            if data_field not in d.data:
                return None
            return d.data[data_field]

        # Capture a changed field's prior value for .compensation, failing
        # the forward op if it is missing — a null prior would bake an
        # un-submittable rollback (the inverse UpdateMetaVertex would reject
        # the field). A missing prior means the aspect key was not declared
        # in ContextHint.Reads (or the vertex is malformed).
        def capture_prior(field, suffix, data_field):
            pv = prior_data_field(suffix, data_field)
            if pv == None:
                fail("InvalidArgument: " + field +
                     ": prior value unavailable for compensation; declare " +
                     meta_key + suffix + " in ContextHint.Reads")
            return pv

        # Canonical field order. expectedRevision (OCC) is applied to the
        # first present field in this order; .compensation is excluded
        # because it has an independent NATS revision sequence.
        mutations = []
        prior_payload = {"metaKey": meta_key}

        def add_string_field(field, suffix, data_field):
            if not hasattr(p, field):
                return
            v = getattr(p, field)
            if v == None or type(v) != type("") or len(v.strip()) == 0:
                fail("InvalidArgument: " + field + ": required non-empty string")
            v = v.strip()
            mutations.append(make_update(meta_key + suffix, {data_field: v}))
            prior_payload[field] = capture_prior(field, suffix, data_field)

        def add_list_field(field, suffix, data_field, elem_string=False):
            if not hasattr(p, field):
                return
            v = getattr(p, field)
            if v == None or type(v) != type([]):
                fail("InvalidArgument: " + field + ": required list")
            if elem_string:
                for c in v:
                    if type(c) != type(""):
                        fail("InvalidArgument: " + field + ": each entry must be a string")
            mutations.append(make_update(meta_key + suffix, {data_field: v}))
            prior_payload[field] = capture_prior(field, suffix, data_field)

        def add_dict_field(field, suffix, data_field):
            if not hasattr(p, field):
                return
            v = getattr(p, field)
            if v == None or type(v) != type({}):
                fail("InvalidArgument: " + field + ": required object")
            mutations.append(make_update(meta_key + suffix, {data_field: v}))
            prior_payload[field] = capture_prior(field, suffix, data_field)

        # description applies to both DDL and lens classes.
        add_string_field("description", ".description", "text")

        if is_lens:
            # spec is validated exactly as the Create-lens branch does and the
            # decoded dict is stored verbatim as the .spec aspect data so
            # Refractor's CoreKVSource can unmarshal a LensSpec directly.
            if hasattr(p, "spec"):
                spec_str = p.spec
                if spec_str == None or type(spec_str) != type("") or len(spec_str.strip()) == 0:
                    fail("InvalidArgument: spec: required non-empty string")
                spec_obj = json.decode(spec_str.strip())
                if type(spec_obj) != type({}):
                    fail("InvalidArgument: spec: must be a JSON object string")
                if "cypherRule" not in spec_obj:
                    fail("InvalidArgument: spec.cypherRule: required")
                if "targetType" not in spec_obj:
                    fail("InvalidArgument: spec.targetType: required (postgres|nats_kv)")
                if "targetConfig" not in spec_obj:
                    fail("InvalidArgument: spec.targetConfig: required")
                mutations.append(make_update(meta_key + ".spec", spec_obj))
                # The .spec aspect stores the decoded dict verbatim, while the
                # spec payload field is a JSON string. Re-encode the prior dict
                # so the compensating UpdateMetaVertex can resubmit it.
                prior_spec = None
                spec_key = meta_key + ".spec"
                if spec_key in state and state[spec_key] != None:
                    sd = state[spec_key]
                    if hasattr(sd, "data") and type(sd.data) == type({}):
                        prior_spec = json.encode(sd.data)
                if prior_spec == None:
                    fail("InvalidArgument: spec: prior value unavailable for compensation; declare " +
                         spec_key + " in ContextHint.Reads")
                prior_payload["spec"] = prior_spec
        else:
            add_string_field("script", ".script", "source")
            add_list_field("permittedCommands", ".permittedCommands", "commands", True)
            add_string_field("inputSchema", ".inputSchema", "schema")
            add_string_field("outputSchema", ".outputSchema", "schema")
            add_dict_field("fieldDescription", ".fieldDescription", "fieldDescriptions")
            add_list_field("examples", ".examples", "examples")

        if len(mutations) == 0:
            fail("InvalidArgument: UpdateMetaVertex: no updatable fields provided")

        # .compensation lets a rollback restore the prior value of exactly the
        # fields this op changed. payloadTemplate carries metaKey plus the
        # prior value of each changed field; revisionTemplate stays empty.
        mutations.append(make_update(meta_key + ".compensation",
            {"inverseOperationType": "UpdateMetaVertex",
             "payloadTemplate": prior_payload,
             "revisionTemplate": {}}))

        force = hasattr(p, "force") and p.force == True
        if hasattr(p, "expectedRevision") and not force:
            expected_rev = p.expectedRevision
            if type(expected_rev) != type(0):
                fail("InvalidArgument: expectedRevision must be an integer")
            # Apply expectedRevision to the first field mutation only
            # (mutations[0]). Each aspect has an independent NATS revision
            # sequence, so multi-aspect atomic OCC is not expressible here;
            # asserting the same revision across aspects would cause spurious
            # RevisionConflict. Multi-aspect atomic OCC is a Phase-2 item.
            mutations[0]["expectedRevision"] = expected_rev
        events = [{"class": "MetaVertexUpdated", "data": {"metaKey": meta_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"metaKey": meta_key}}

    if ot == "TombstoneMetaVertex":
        meta_key = required_string(p, "metaKey")
        if not vertex_alive(state, meta_key):
            fail("UnknownMetaVertex: " + meta_key)
        if is_protected(state, meta_key):
            fail("ProtectedMetaVertex: " + meta_key)
        # force=True skips the revision assertion (last-writer-wins merge policy).
        force = hasattr(p, "force") and p.force == True

        # Cascade the tombstone to the root vertex and every aspect key of
        # the meta-vertex's class, so a delete leaves no live aspects behind
        # (no orphaned description/script/spec keys). The class of the
        # hydrated root selects the aspect set. compensation is tombstoned
        # like any other aspect: no Go code reads .compensation from Core KV
        # post-commit (the compensating-op contract is resolved client-side
        # from the forward op's reply), so removing it yields a fully
        # coherent delete.
        target_class = ""
        root = state[meta_key]
        if hasattr(root, "class"):
            target_class = getattr(root, "class")
        is_lens = target_class == "meta.lens"

        if is_lens:
            # Union of the DDL-created lens aspects (.description,
            # .compensation) and the primordial-seeded lens aspects
            # (.targetBucket, .cypherRule, .outputSchema) so a delete leaves
            # no live orphan regardless of how the lens was created.
            # Tombstoning an aspect the lens never had writes a harmless
            # isDeleted entry (never read, evicted from cache) — strictly
            # safer than leaving a live aspect under a dead root.
            aspect_suffixes = [".canonicalName", ".description", ".spec",
                               ".compensation", ".targetBucket", ".cypherRule",
                               ".outputSchema"]
        else:
            aspect_suffixes = [".canonicalName", ".permittedCommands",
                               ".description", ".script", ".inputSchema",
                               ".outputSchema", ".fieldDescription",
                               ".examples", ".compensation"]

        # The root tombstone is mutations[0] so expectedRevision (when
        # present) lands on the root only. Aspect tombstones are
        # unconditional and carry no revision assertion — each aspect has an
        # independent NATS revision sequence, so asserting a shared revision
        # would cause spurious conflicts.
        mutations = [make_tombstone(meta_key)]
        for suffix in aspect_suffixes:
            mutations.append(make_tombstone(meta_key + suffix))

        if hasattr(p, "expectedRevision") and not force:
            expected_rev = p.expectedRevision
            if type(expected_rev) != type(0):
                fail("InvalidArgument: expectedRevision must be an integer")
            # Propagate revision assertion to the substrate AtomicBatch layer
            # (CommitterImpl already handles mutation["expectedRevision"] at
            # step8_commit.go — no Committer changes needed).
            mutations[0]["expectedRevision"] = expected_rev
        events = [{"class": "MetaVertexTombstoned", "data": {"metaKey": meta_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"metaKey": meta_key}}

    fail("root DDL: unknown operationType: " + ot)
`
