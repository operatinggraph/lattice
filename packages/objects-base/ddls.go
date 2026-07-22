package objectsbase

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations.
//
// Single DDL `object` (vertex-type class) handles all three lifecycle ops for
// the generic large-object vertex: AttachObject, DetachObject, TombstoneObject.
// The object vertex is the graph side of the off-graph blob plane — it carries
// the content's reference metadata (digest, size, contentType, storeName) and
// is related to its owner(s) by links; the bytes live in the core-objects
// Object Store and never enter this path.
//
// Architectural rules (binding — same known-key discipline as service-domain /
// identity-domain):
//
//   - The script reads ONLY by known key. No prefix scans, no adjacency
//     lookups, no lens-output reads. Each op validates its target / object /
//     link endpoints by the keys the caller lists in ContextHint.Reads.
//   - Content-addressed identity (D2/D3): the object vertex id is
//     oid = crypto.sha256NanoID("object:" + digest), so identical bytes map to
//     one vertex (dedup) and the id is a valid Contract #1 NanoID (a raw hex
//     digest is not). The full digest is stored on the .content aspect for
//     integrity + collision detection (a 20-char-NanoID collision is ~2^-60 and
//     detectable: a different digest under the same oid is rejected).
//   - Type-agnostic (D7): AttachObject takes (targetKey, linkName, digest, …)
//     and never learns concrete owner types. The owner is validated alive +
//     non-protected (CC7), but its type is whatever the caller supplies.
//   - Link direction (Contract #1 §1.1): the object arrives AFTER its owner, so
//     object = source, owner = target — `object -<linkName>-> owner`.
//   - Vertex-revision + liveLinks track the link set (the v1b GC race guards,
//     §19/§21): every AttachObject / DetachObject writes the object vertex
//     (create / revive / OCC-touch) in the SAME atomic batch as its link
//     mutation — advancing linkEpoch (so a concurrent re-link moves the revision
//     and the lens-driven TombstoneObject OCC aborts) AND maintaining the
//     liveLinks count (so the objectLiveness lens reads liveness from this
//     lag-free scalar, never the lagging refractor-adjacency projection — a
//     freshly-attached object is never reaped during adjacency catch-up).
//
// Object shape (D5 — root data minimal, content metadata in the .content
// aspect, relationships are links):
//
//	vtx.object.<oid>                                   root data = {linkEpoch, liveLinks}, class=object
//	vtx.object.<oid>.content                           aspect: { digest, size, contentType, storeName }
//	lnk.object.<oid>.<linkName>.<tgtType>.<tgtId>      link: object→owner, data { filename? }
//
// Events (audit + the v1b GC trigger; package events are emitted via the
// EventList and are NOT validated against an eventType DDL at commit, CC11b):
//
//	object.attached   { objectKey, targetKey, linkName, dedup }   — every attach
//	object.detached   { objectKey, linkKey }                      — detach + replace-leg
//	object.tombstoned { objectKey, storeName? }                   — TombstoneObject (the byte-reclaim trigger)
//
// Caller's ContextHint.Reads MUST include (conditionally — a declared-but-absent
// key is a fatal hydration miss, so the client lists only keys that exist):
//   - AttachObject: the targetKey (always — it must be live); and, when they
//     already exist, vtx.object.<oid> + vtx.object.<oid>.content + the link key
//     (so dedup / revive / collision / idempotent-relink branch correctly); and,
//     for a replace, the old link + old object vertex.
//   - DetachObject: the link key (must be live) + the object vertex.
//   - TombstoneObject: the object vertex + its .content aspect.
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{objectDDL()}
}

// OpMetas makes AttachObject + DetachObject forOperation-resolvable so a future
// Loom step can bind them; TombstoneObject is GC-internal (the object-store-
// manager's owner-tombstone cascade submits it directly) and is also resolvable
// for that binding.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{OperationType: "AttachObject"},
		{OperationType: "DetachObject"},
		{OperationType: "TombstoneObject"},
	}
}

func objectDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "object",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"AttachObject", "DetachObject", "TombstoneObject"},
		Description: "Large-object vertex DDL — the graph side of the off-graph blob plane. Vertex shape: " +
			"vtx.object.<oid>, class=object, root data = {linkEpoch, liveLinks} (linkEpoch the link-set version for the reclaim CAS, liveLinks the " +
			"authoritative live-link count the objectLiveness lens reads in place of the lagging adjacency, both bumped on " +
			"every attach/detach; otherwise minimal, D5), where oid = " +
			"crypto.sha256NanoID(\"object:\" + digest) (content-addressed, D2/D3). The content's reference " +
			"metadata (digest, size, contentType, storeName) lives on the .content aspect; the bytes live in " +
			"the core-objects Object Store and never enter this path. Relationships are LINKS: " +
			"object -<linkName>-> owner (the object is the later-arriving source, the owner the pre-existing " +
			"target, Contract #1 §1.1). AttachObject mints-or-dedups the object vertex + .content and creates " +
			"the link to a live, non-protected target (type-agnostic, D7); an identical digest dedups to one " +
			"vertex, a tombstoned object is revived with the fresh upload (CC2), a digest mismatch under an " +
			"existing oid is rejected (DigestCollision). An optional replaceObjectId tombstones the prior " +
			"object's link in the same slot (the §8 \"new photo\" update). A sensitive object (Contract #3 " +
			"§3.11) is encrypted client-side before this DDL ever runs — AttachObject only records the " +
			"caller-supplied envelope (sensitive:true + encryption{algo,nonce,wrappedCEK,keyId} + " +
			"governingIdentity) and identity-salts the oid to crypto.sha256NanoID(\"object:\"+keyId+\":\"+digest), " +
			"so a sensitive object is never cross-identity content-addressed; digest stays the PLAINTEXT " +
			"digest (post-decrypt integrity), keyId must equal governingIdentity. DetachObject tombstones one link. " +
			"TombstoneObject soft-deletes the object vertex + .content under a linkEpoch stale-check (the " +
			"lens-projected expectedEpoch vs the current one) + a vertex-revision self-OCC, and emits " +
			"object.tombstoned (the byte-reclaim trigger). Every attach/detach OCC-touches the object vertex " +
			"and atomically bumps linkEpoch + maintains liveLinks, versioning the link set (the re-link CAS guard, " +
			"§20) and keeping liveness lag-free (the attach-adjacency-lag reclaim guard, §21).",
		Script: objectDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"digest":{"type":"string","description":"AttachObject: the NATS-computed content digest \"SHA-256=<base64url>\". Derives the content-addressed oid and is stored on .content for integrity."},` +
			`"size":{"type":"integer","description":"AttachObject: the object size in bytes (stored on .content)."},` +
			`"contentType":{"type":"string","description":"AttachObject: the MIME content type (stored on .content)."},` +
			`"storeName":{"type":"string","description":"AttachObject/TombstoneObject: the core-objects object name (the provisional NanoID the bytes were streamed under). Stored on .content; echoed in object.tombstoned so the byte-janitor can reclaim."},` +
			`"targetKey":{"type":"string","description":"AttachObject/DetachObject: the full vtx.<type>.<NanoID> the object links to (the owner). Validated alive + non-protected (AttachObject)."},` +
			`"linkName":{"type":"string","description":"AttachObject/DetachObject: the relationship localName ([a-z][a-zA-Z0-9]*), e.g. photoOf / signedLeaseOf. Caller-supplied, no per-linkName DDL."},` +
			`"filename":{"type":"string","description":"AttachObject (optional): the attachment filename, stored on the link (attachment-specific, not on the shared object vertex)."},` +
			`"replaceObjectId":{"type":"string","description":"AttachObject (optional): the bare oid of a prior object whose link in the same slot is tombstoned (the §8 \"new photo\" replace)."},` +
			`"sensitive":{"type":"boolean","description":"AttachObject (optional, default false): marks the object as client-side encrypted at rest (Contract #3 §3.11). True requires governingIdentity + encryption."},` +
			`"governingIdentity":{"type":"string","description":"AttachObject, required when sensitive: the full vtx.<type>.<NanoID> of the identity whose erasure right governs this document (the CEK's wrap target). Must equal encryption.keyId. Salts the oid so sensitive objects are never cross-identity content-addressed."},` +
			`"encryption":{"type":"object","description":"AttachObject, required when sensitive: {algo,nonce,wrappedCEK,keyId} — the envelope the uploader already produced client-side (Vault.WrapKey on the per-object CEK). Stored verbatim on .content; the Processor/DDL never sees key material.","properties":{"algo":{"type":"string"},"nonce":{"type":"string"},"wrappedCEK":{"type":"string"},"keyId":{"type":"string"}}},` +
			`"oid":{"type":"string","description":"DetachObject/TombstoneObject: the bare object id (the <oid> segment of vtx.object.<oid>)."},` +
			`"objectKey":{"type":"string","description":"TombstoneObject (alternative to oid): the full vtx.object.<oid> key — what the objectLiveness lens row's entityKey carries, so Weaver's directOp templates it directly into the reclaim op."},` +
			`"expectedEpoch":{"type":"integer","description":"TombstoneObject (optional): the object's data.linkEpoch the objectLiveness lens projected at orphan-detection. The soft-delete CASes it against the current epoch — a concurrent re-link bumps the epoch and aborts the reclaim (the §20 GC stale-check). The vertex KV-revision is always self-OCC'd in addition."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"AttachObject/DetachObject: the link key. TombstoneObject: the object vertex key."}}}`,
		FieldDescription: map[string]string{
			"digest":            "AttachObject: the NATS-computed content digest in the exact \"SHA-256=<base64url>\" form. The oid is crypto.sha256NanoID(\"object:\" + digest); the full digest is stored on .content for integrity and collision detection.",
			"size":              "AttachObject: object size in bytes, stored on the .content aspect.",
			"contentType":       "AttachObject: MIME content type, stored on the .content aspect; the read path streams it back as Content-Type.",
			"storeName":         "The core-objects Object Store name the bytes were streamed under (a provisional NanoID — content addressing lives at the graph layer, not the store key). Stored on .content (AttachObject) and echoed in the object.tombstoned event (TombstoneObject) so the byte-janitor can reclaim.",
			"targetKey":         "The full vtx.<type>.<NanoID> key of the owner the object links to. AttachObject validates it is alive and non-protected (never a meta/system or data.protected vertex); the type is whatever the caller supplies (type-agnostic).",
			"linkName":          "The relationship localName ([a-z][a-zA-Z0-9]*) read from the object: photoOf, signedLeaseOf, etc. Caller-supplied and validated as a delimiter-safe localName; there is no per-linkName DDL.",
			"filename":          "Optional attachment filename. Stored on the LINK (attachment-specific) — never on the shared, deduped object vertex, since owner A's resume.pdf and owner B's lease.pdf can be identical bytes.",
			"replaceObjectId":   "Optional bare oid of a prior object whose link in the same (targetKey, linkName) slot is tombstoned in the same batch — the \"here's my new photo\" replace. The old object is reclaimed by the v1b GC if that was its last link.",
			"sensitive":         "Optional, default false. True marks the object as encrypted at rest (Contract #3 §3.11) — the bytes are already ciphertext when streamed; requires governingIdentity + encryption on the same call.",
			"governingIdentity": "Required when sensitive is true: the full vtx.<type>.<NanoID> of the identity whose ShredIdentityKey call makes this blob permanently unrecoverable. Must equal encryption.keyId. Identity-salts the oid (no cross-identity dedup for sensitive objects).",
			"encryption":        "Required when sensitive is true: {algo,nonce,wrappedCEK,keyId} — the client-produced envelope (Vault.WrapKey wrapping the per-object CEK under governingIdentity's §3.10 DEK). Stored on .content verbatim; inert without the identity's DEK.",
			"oid":               "DetachObject/TombstoneObject: the bare object id — the <oid> segment of vtx.object.<oid>. Loupe learns it from an attach reply's primaryKey link, or derives it Go-side from the digest.",
			"objectKey":         "TombstoneObject (alternative to oid): the full vtx.object.<oid> key. The objectLiveness lens row's entityKey carries it, so Weaver's directOp templates it (row.entityKey) straight into the reclaim op + its reads.",
			"expectedEpoch":     "TombstoneObject optional stale-check: the object's data.linkEpoch the objectLiveness lens projected at orphan-detection. The soft-delete CASes it against the current epoch (a concurrent re-link bumps it → abort). The vertex KV-revision is self-OCC'd in addition.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "AttachObject — attach a profile photo to an identity",
				Payload: map[string]any{
					"digest":      "SHA-256=GLnInPV-CK2 KExample",
					"size":        184213,
					"contentType": "image/jpeg",
					"storeName":   "<provisional-nanoid>",
					"targetKey":   "vtx.identity.<applicantNanoID>",
					"linkName":    "photoOf",
					"filename":    "me.jpg",
				},
				ExpectedOutcome: "Derives oid = crypto.sha256NanoID(\"object:\" + digest). If absent, mints vtx.object.<oid> " +
					"(root {}) + a .content aspect {digest,size,contentType,storeName} + the link " +
					"lnk.object.<oid>.photoOf.identity.<applicantNanoID> {filename: me.jpg}. If the identical bytes " +
					"already have a live object, adds only the link (dedup) and OCC-touches the object vertex. Emits " +
					"object.attached. Returns primaryKey = the link key — except a re-attach to an already-alive " +
					"(same target+linkName) link, a harmless CC5 no-op that mutates nothing under the link key, which " +
					"omits primaryKey (the caller derives the deterministic link key itself, same as a collapsed " +
					"duplicate). Rejects an absent/protected target or a digest mismatch under an existing oid " +
					"(DigestCollision).",
			},
			{
				Name: "AttachObject — attach a sensitive (crypto-shreddable) document",
				Payload: map[string]any{
					"digest":            "SHA-256=PlaintextDigestExample",
					"size":              412903,
					"contentType":       "application/pdf",
					"storeName":         "<provisional-nanoid>",
					"targetKey":         "vtx.identity.<applicantNanoID>",
					"linkName":          "signedLeaseOf",
					"sensitive":         true,
					"governingIdentity": "vtx.identity.<applicantNanoID>",
					"encryption": map[string]any{
						"algo":       "AES-256-GCM",
						"nonce":      "<base64-nonce>",
						"wrappedCEK": "<base64-wrapped-CEK>",
						"keyId":      "vtx.identity.<applicantNanoID>",
					},
				},
				ExpectedOutcome: "The uploader already AES-256-GCM-encrypted the bytes client-side and wrapped the CEK " +
					"under governingIdentity's §3.10 DEK before calling this op. Derives the identity-salted oid = " +
					"crypto.sha256NanoID(\"object:\"+keyId+\":\"+digest); mints vtx.object.<oid> + a .content aspect " +
					"carrying {digest (plaintext), size, contentType, storeName, sensitive:true, encryption}. " +
					"ShredIdentityKey(vtx.identity.<applicantNanoID>) later makes this blob's wrappedCEK permanently " +
					"unwrappable — the same DEK erases both the aspect plane and this blob. Rejects a mismatched " +
					"encryption.keyId/governingIdentity pair or a missing encryption block.",
			},
			{
				Name: "DetachObject — remove a photo link",
				Payload: map[string]any{
					"oid":       "<objectNanoID>",
					"targetKey": "vtx.identity.<applicantNanoID>",
					"linkName":  "photoOf",
				},
				ExpectedOutcome: "Tombstones lnk.object.<oid>.photoOf.identity.<applicantNanoID> and OCC-touches the object " +
					"vertex so the v1b lens reprojects it as a possibly-orphaned candidate. Emits object.detached. Returns " +
					"primaryKey = the link key. Rejects if the link is not live.",
			},
			{
				Name: "TombstoneObject — GC reclaims an orphaned object (v1b)",
				Payload: map[string]any{
					"oid": "<objectNanoID>",
				},
				ExpectedOutcome: "Soft-deletes vtx.object.<oid> + its .content aspect under an OCC revision guard (a concurrent " +
					"re-link aborts it). Emits object.tombstoned {objectKey, storeName} — the byte-janitor consumes it to " +
					"delete the core-objects bytes. Returns primaryKey = the object vertex key. Rejects a non-live object.",
			},
		},
	}
}

// objectDDLScript handles the three object lifecycle ops. Known-key reads only.
// Content-addressed oid, idempotent-upsert with dedup / revive / collision
// branches (CC5), type-agnostic target validation (CC7), and the
// vertex-revision link-set guard (§19).
const objectDDLScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def link_ensure_alive(state, key, source, target, cls, local_name, data):
    # Returns the mutation that makes the link alive, or None if it already is.
    # A soft-tombstoned link key still physically exists (isDeleted in the body,
    # not a NATS delete marker), so a CreateOnly over it would conflict — a
    # re-attach after a detach must REVIVE the link (OCC update), not create it.
    if key not in state or state[key] == None:
        return make_link(key, source, target, cls, local_name, data)
    doc = state[key]
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return {"op": "update", "key": key, "expectedRevision": doc.revision,
                "document": {"class": cls, "isDeleted": False,
                             "sourceVertex": source, "targetVertex": target,
                             "localName": local_name, "data": data}}
    return None

def split_key(k):
    return k.split(".")

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def optional_string(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        return None
    return v.strip()

def required_int(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type(0):
        fail("InvalidArgument: " + name + ": required integer")
    return v

def optional_int(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None:
        return None
    if type(v) != type(0):
        fail("InvalidArgument: " + name + ": must be an integer")
    return v

def optional_security_flag(p, name):
    # Default False when absent/null (an opt-in flag), but a PRESENT
    # wrong-typed value fails closed rather than silently coercing to False —
    # unlike an ordinary optional field, this gates a security classification
    # (sensitive:true), so a caller/serialization bug (e.g. "true" the string)
    # must be a loud rejection, never a silent downgrade to plaintext.
    if not hasattr(p, name):
        return False
    v = getattr(p, name)
    if v == None:
        return False
    if type(v) != type(True):
        fail("InvalidArgument: " + name + ": must be a boolean")
    return v

def required_dict_string(d, container_name, field_name):
    v = d.get(field_name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + container_name + "." + field_name + ": required non-empty string")
    return v.strip()

# A delimiter-safe bare id: non-empty, carries no key delimiters / wildcards /
# whitespace, so "vtx.object." + id is a single well-formed 3-segment key. The
# committer's key-shape validation is the authoritative guard; this is an early,
# clear rejection (mirrors service-domain's bare_nanoid checks).
def required_bare_id(p, name):
    v = required_string(p, name)
    for bad in [".", "*", ">", " ", "\t", "\n", "/"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must be a bare id (no key delimiters); got " + v)
    return v

# linkName is a localName ([a-z][a-zA-Z0-9]*). The load-bearing safety property
# is that it injects no extra key segments (the committer rejects a non-6-segment
# link key); the lowercase-start is the localName nicety.
def valid_link_name(s):
    if len(s) == 0:
        return False
    for bad in [".", "*", ">", " ", "\t", "\n", "/"]:
        if bad in s:
            return False
    c0 = s[0]
    return c0 >= "a" and c0 <= "z"

def parts_of(key, name, want_type):
    parts = split_key(key)
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def present(state, key):
    return key in state and state[key] != None

def alive(state, key):
    if not present(state, key):
        return False
    doc = state[key]
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def is_tombstoned(state, key):
    if not present(state, key):
        return False
    doc = state[key]
    return hasattr(doc, "isDeleted") and doc.isDeleted

def is_protected(state, key):
    if not present(state, key):
        return False
    doc = state[key]
    if not hasattr(doc, "data") or doc.data == None:
        return False
    if "protected" not in doc.data:
        return False
    return doc.data["protected"] == True

def revision_of(state, key):
    return state[key].revision

def aspect_field(state, key, field):
    if not present(state, key):
        return None
    doc = state[key]
    if not hasattr(doc, "data") or doc.data == None:
        return None
    if field not in doc.data:
        return None
    return doc.data[field]

def next_epoch(state, obj_key):
    # The object vertex's link-set version (data.linkEpoch), bumped on every
    # link-set change. The objectLiveness lens projects it; TombstoneObject CASes
    # the lens-projected epoch against the current one so a concurrent re-link
    # (which bumps it) aborts the reclaim (§20). Starts at 1 on create.
    cur = aspect_field(state, obj_key, "linkEpoch")
    if cur == None or type(cur) != type(0):
        return 1
    return cur + 1

def cur_live_links(state, obj_key):
    # The object's authoritative live-link count (data.liveLinks), maintained in
    # the SAME atomic batch as every link mutation. The objectLiveness lens reads
    # this scalar — NOT the lagging link-projection read model — so a
    # freshly-attached object is never mis-seen as orphaned during the projection
    # catch-up window (the §21 attach-lag reclaim race). Defaults to 0 for a
    # vertex that predates the field (none in a fresh deployment).
    cur = aspect_field(state, obj_key, "liveLinks")
    if cur == None or type(cur) != type(0):
        return 0
    return cur

def write_vertex(state, obj_key, live_links):
    # OCC-touch the object vertex (self-OCC on its hydrated revision), advancing
    # linkEpoch (the re-link CAS version, §20) and writing the atomic live-link
    # count (the lag-free liveness signal, §21) in one batch with the link
    # mutation. A concurrent re-link/detach moves the revision so this touch — and
    # any concurrent tombstone — conflict (§19/§20 race guard). The count never
    # goes negative.
    if live_links < 0:
        live_links = 0
    return {"op": "update", "key": obj_key,
            "expectedRevision": revision_of(state, obj_key),
            "document": {"class": "object", "isDeleted": False,
                         "data": {"linkEpoch": next_epoch(state, obj_key),
                                  "liveLinks": live_links}}}

def execute(state, op):
    ot = op.operationType
    p = op.payload
    if ot == "AttachObject":
        return attach_object(state, p)
    if ot == "DetachObject":
        return detach_object(state, p)
    if ot == "TombstoneObject":
        return tombstone_object(state, p)
    fail("object DDL: unknown operationType: " + ot)

def attach_object(state, p):
    digest = required_string(p, "digest")
    size = required_int(p, "size")
    content_type = required_string(p, "contentType")
    store_name = required_string(p, "storeName")
    target_key = required_string(p, "targetKey")
    link_name = required_string(p, "linkName")
    if not valid_link_name(link_name):
        fail("InvalidArgument: linkName: must be a localName [a-z][a-zA-Z0-9]* with no key delimiters; got " + link_name)
    filename = optional_string(p, "filename")
    replace_oid = optional_string(p, "replaceObjectId")

    # Sensitive (Contract #3 §3.11): the bytes were already encrypted client-side
    # under a per-object CEK, wrapped by the governing identity's §3.10 DEK. The
    # script never touches key material or the Vault — it only records the
    # caller-supplied envelope and identity-salts the oid so a sensitive object
    # is never cross-identity content-addressed (the §4.1 dedup/shred tension).
    sensitive = optional_security_flag(p, "sensitive")
    key_id = None
    encryption_data = None
    if sensitive:
        governing_identity = required_string(p, "governingIdentity")
        gov_type, _ = parts_of(governing_identity, "governingIdentity", "")
        if gov_type == "meta":
            fail("ProtectedTarget: governingIdentity cannot be a meta/system vertex: " + governing_identity)
        if not hasattr(p, "encryption"):
            fail("InvalidArgument: encryption: required when sensitive is true")
        enc = getattr(p, "encryption")
        if type(enc) != type({}):
            fail("InvalidArgument: encryption: must be an object")
        algo = required_dict_string(enc, "encryption", "algo")
        nonce = required_dict_string(enc, "encryption", "nonce")
        wrapped_cek = required_dict_string(enc, "encryption", "wrappedCEK")
        key_id = required_dict_string(enc, "encryption", "keyId")
        if key_id != governing_identity:
            fail("InvalidArgument: encryption.keyId must equal governingIdentity; got " + key_id + " != " + governing_identity)
        encryption_data = {"algo": algo, "nonce": nonce, "wrappedCEK": wrapped_cek, "keyId": key_id}

    # Target validation (CC7): live, and never a meta/system or protected vertex.
    tgt_type, tgt_id = parts_of(target_key, "targetKey", "")
    if tgt_type == "meta":
        fail("ProtectedTarget: cannot attach an object to a meta/system vertex: " + target_key)
    if not alive(state, target_key):
        fail("UnknownTarget: " + target_key)
    if is_protected(state, target_key):
        fail("ProtectedTarget: target is protected: " + target_key)

    # Content-addressing (§3.3/§4.1): a sensitive object is identity-salted so
    # identical plaintext from two identities never converges to one shared
    # vertex (closes a PII linkage leak); non-sensitive stays content-addressed,
    # byte-for-byte unchanged.
    if sensitive:
        oid = crypto.sha256NanoID("object:" + key_id + ":" + digest)
    else:
        oid = crypto.sha256NanoID("object:" + digest)
    obj_key = "vtx.object." + oid
    content_key = obj_key + ".content"
    link_key = "lnk.object." + oid + "." + link_name + "." + tgt_type + "." + tgt_id

    content_data = {"digest": digest, "size": size,
                    "contentType": content_type, "storeName": store_name}
    if sensitive:
        content_data["sensitive"] = True
        content_data["governingIdentity"] = governing_identity
        content_data["encryption"] = encryption_data
    link_data = {}
    if filename != None:
        link_data["filename"] = filename

    # Decide the link mutation BEFORE writing the vertex: a new-or-revived link is
    # +1 to the live-link count; an already-alive link (idempotent re-attach) is
    # +0. link_ensure_alive returns None only when the link is already alive.
    link_mut = link_ensure_alive(state, link_key, obj_key, target_key, link_name, link_name, link_data)
    link_delta = 0
    if link_mut != None:
        link_delta = 1

    dedup = present(state, obj_key) and not is_tombstoned(state, obj_key)
    mutations = []

    if not present(state, obj_key):
        # Absent → mint the object vertex (linkEpoch starts at 1, liveLinks = the
        # one new link) + .content aspect.
        mutations.append(make_vtx(obj_key, "object", {"linkEpoch": 1, "liveLinks": link_delta}))
        mutations.append(make_aspect(obj_key, "content", "content", content_data))
    elif is_tombstoned(state, obj_key):
        # Tombstoned (a prior object reclaimed by GC) → revive the vertex + its
        # .content with the FRESH upload (new storeName). A deleted-then-re-added
        # object is always restored (CC2, no data loss). A tombstoned object had
        # zero live links (that is why it was reaped), so the revived count is
        # exactly this attach's link_delta.
        mutations.append(write_vertex(state, obj_key, link_delta))
        if present(state, content_key):
            mutations.append({"op": "update", "key": content_key,
                              "expectedRevision": revision_of(state, content_key),
                              "document": {"class": "content", "isDeleted": False,
                                           "vertexKey": obj_key, "localName": "content",
                                           "data": content_data}})
        else:
            mutations.append(make_aspect(obj_key, "content", "content", content_data))
    else:
        # Live → dedup. The .content aspect MUST be hydrated so digest-collision
        # detection is script-enforced, not merely client-cooperative; then OCC-
        # touch the vertex so its revision tracks the new link and its liveLinks
        # count rises by the link_delta.
        if not present(state, content_key):
            fail("InvalidArgument: contextHint.reads must include " + content_key + " when the object is live")
        stored_digest = aspect_field(state, content_key, "digest")
        if stored_digest != None and stored_digest != digest:
            fail("DigestCollision: oid " + oid + " already bound to a different digest")
        mutations.append(write_vertex(state, obj_key, cur_live_links(state, obj_key) + link_delta))

    # Ensure the link is alive: create when absent, revive when soft-tombstoned
    # (a re-attach after detach), no-op when already alive (graph-layer
    # idempotency — a >24h re-attach past the requestId tracker is a harmless
    # no-op, CC5 layer 2).
    if link_mut != None:
        mutations.append(link_mut)

    events = [{"class": "object.attached",
               "data": {"objectKey": obj_key, "targetKey": target_key,
                        "linkName": link_name, "dedup": dedup}}]

    # Replace leg (§8 — "here's my new photo"): tombstone the prior object's link
    # in the same slot + OCC-touch that object so the lens reprojects it as a
    # possibly-orphaned candidate; the v1b GC reclaims it iff that was its last link.
    if replace_oid != None and replace_oid != oid:
        old_link = "lnk.object." + replace_oid + "." + link_name + "." + tgt_type + "." + tgt_id
        old_obj_key = "vtx.object." + replace_oid
        if alive(state, old_link):
            mutations.append({"op": "tombstone", "key": old_link,
                              "document": {"class": link_name, "data": {}}})
            if present(state, old_obj_key):
                mutations.append(write_vertex(state, old_obj_key, cur_live_links(state, old_obj_key) - 1))
            events.append({"class": "object.detached",
                           "data": {"objectKey": old_obj_key, "linkKey": old_link}})

    # The reply-constraint (Processor commit_path.go, "response.primaryKey is
    # not within the write footprint") rejects a script-named key the batch
    # never mutated. When the link was already alive, link_mut is None and no
    # mutation touches link_key — so primaryKey must be omitted here, exactly
    # like a same-requestId duplicate collapse. The caller already has to
    # handle that (derive the deterministic link key itself); this is the
    # same fallback for the >24h-past-the-tracker re-attach case.
    response = {}
    if link_mut != None:
        response["primaryKey"] = link_key

    return {"mutations": mutations, "events": events, "response": response}

def detach_object(state, p):
    oid = required_bare_id(p, "oid")
    target_key = required_string(p, "targetKey")
    link_name = required_string(p, "linkName")
    if not valid_link_name(link_name):
        fail("InvalidArgument: linkName: must be a localName [a-z][a-zA-Z0-9]* with no key delimiters; got " + link_name)
    tgt_type, tgt_id = parts_of(target_key, "targetKey", "")
    obj_key = "vtx.object." + oid
    link_key = "lnk.object." + oid + "." + link_name + "." + tgt_type + "." + tgt_id

    if not alive(state, link_key):
        fail("UnknownLink: " + link_key + " is not a live link")

    mutations = [{"op": "tombstone", "key": link_key,
                  "document": {"class": link_name, "data": {}}}]
    # OCC-touch the object vertex so its revision tracks the link-set change and
    # its liveLinks count drops by one (the lag-free orphan signal — at zero the
    # objectLiveness lens flags the object for reclaim).
    if present(state, obj_key):
        mutations.append(write_vertex(state, obj_key, cur_live_links(state, obj_key) - 1))

    events = [{"class": "object.detached",
               "data": {"objectKey": obj_key, "linkKey": link_key}}]
    return {"mutations": mutations, "events": events,
            "response": {"primaryKey": link_key}}

def tombstone_object(state, p):
    # The object is named by its full objectKey (the objectLiveness lens row's
    # entityKey, which Weaver templates in) or, for a direct caller, a bare oid.
    obj_key = optional_string(p, "objectKey")
    if obj_key == None:
        oid = required_bare_id(p, "oid")
        obj_key = "vtx.object." + oid
    else:
        parts_of(obj_key, "objectKey", "object")
    content_key = obj_key + ".content"

    if not alive(state, obj_key):
        fail("UnknownObject: " + obj_key + " is not a live object")

    # Stale-check (the §20 GC guard): the objectLiveness lens projects the
    # object's linkEpoch at orphan-detection and Weaver templates it as
    # expectedEpoch. A concurrent re-link bumps the current epoch, so a mismatch
    # means the object was re-linked since the lens saw it orphaned → abort
    # rather than reap a live-and-relinked object. (Closes the
    # lens-projection→op-hydration window; the self-OCC below closes the
    # hydration→commit window.)
    cur_epoch = aspect_field(state, obj_key, "linkEpoch")
    expected_epoch = optional_int(p, "expectedEpoch")
    if expected_epoch != None and cur_epoch != expected_epoch:
        fail("Stale: object " + obj_key + " linkEpoch changed since orphan-detection (a concurrent re-link)")

    # Authoritative liveness backstop (§21): never reap an object that ATOMICALLY
    # still has live links. The object vertex is hydrated, so its liveLinks count
    # is the lag-free truth at op-commit time — a re-link that landed since the
    # lens projected orphaned shows here even when no expectedEpoch was supplied.
    # This makes the data-loss invariant authoritative at the op layer (not merely
    # lens-trusting) and closes the force-tombstone-a-live-object hazard: a direct
    # caller cannot reap a still-linked object, so the revive path's reset-to-this-
    # attach's-count can never undercount a live object back into reclaim.
    if cur_live_links(state, obj_key) > 0:
        fail("Stale: object " + obj_key + " still has live links — not orphaned, refusing to reap")

    # storeName for the byte-reclaim event: the lens-projected value (Weaver
    # templates it from the row, since the GC dispatch hydrates only the vertex),
    # falling back to the hydrated .content for a direct caller that read it.
    store_name = optional_string(p, "storeName")
    if store_name == None:
        store_name = aspect_field(state, content_key, "storeName")

    # Soft-delete the object vertex, self-OCC on the hydrated revision so a
    # re-link landing between hydrate and commit (which moves the revision)
    # aborts. linkEpoch is preserved so it stays monotonic across a revive.
    tomb_data = {}
    if cur_epoch != None:
        tomb_data = {"linkEpoch": cur_epoch}
    # The .content aspect is tombstoned unconditionally (it always exists for a
    # live object; a tombstone mutation needs no hydration). It rides the vertex
    # tombstone's atomic batch, so a concurrent revive that aborts the vertex
    # OCC aborts this too — they commit or fail together.
    mutations = [
        {"op": "tombstone", "key": obj_key,
         "expectedRevision": revision_of(state, obj_key),
         "document": {"class": "object", "data": tomb_data}},
        {"op": "tombstone", "key": content_key},
    ]

    ev_data = {"objectKey": obj_key}
    if store_name != None:
        ev_data["storeName"] = store_name
    events = [{"class": "object.tombstoned", "data": ev_data}]
    return {"mutations": mutations, "events": events,
            "response": {"primaryKey": obj_key}}
`
