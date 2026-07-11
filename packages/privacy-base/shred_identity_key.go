package privacybase

import "github.com/asolgan/lattice/internal/pkgmgr"

// shredIdentityKeyDDL is the canonical name of the vertexType DDL owning the
// ShredIdentityKey op (design §2.2/§9 Fire 3, Contract #3 §3.10/§3.11).
const shredIdentityKeyDDL = "shredIdentityKey"

// keyShreddedEventDDL is the canonical name of the keyShredded event-type DDL.
// Per Contract #3 §3.4 the canonicalName of a registered event-type DDL equals
// the event's own class, so this is the full domain-qualified class string.
const keyShreddedEventDDL = "privacy.keyShredded"

// ShredIdentityKeyDDL returns the DDL meta-vertex declaration for the
// ShredIdentityKey op — the right-to-erasure trigger (design §2.2/§2.4).
//
// ShredIdentityKey{identityKey} marks the identity's piiKey envelope
// shredded=true (an unconditioned update, mirroring MarkExpired's
// create-if-absent/overwrite-if-present shape) and emits a privacy.keyShredded
// event. The op records INTENT in Core KV only — it does not itself touch the
// Vault; the async privacy-worker listener (internal/privacyworker) consumes
// the event and calls Vault.ShredKey, which is where the irreversible key
// destruction (and, for the local backend, the in-memory shredded-set +
// DEK-cache eviction) actually happens. Keeping that off the synchronous
// commit path means a KMS round-trip can never block or fail an operation
// commit (design §2.4's "guaranteed-eventual, not synchronous" posture).
//
// An identity that never received a sensitive write has no piiKey aspect yet.
// ShredIdentityKey still writes ONE — a durable placeholder envelope with an
// EMPTY wrappedDEK (no real key was ever minted) and shredded=true — rather
// than skipping the mutation. This is load-bearing, not decorative:
// LocalBackend's shredded-identity deny-list is in-memory only (a Processor
// restart empties it), so without a Core-KV-persisted record, a sensitive
// write arriving AFTER a restart would find no piiKey, mint a brand-new
// unshredded key, and silently reopen the identity to PII — defeating the
// erasure guarantee for exactly the identities that never had a key to begin
// with. The placeholder's envelope.Shredded=true is checked (and honored)
// before internal/vault's WrappedDEK-empty validation, so it durably blocks
// every future Encrypt/Decrypt for this identity regardless of process
// restarts.
//
// The op hydrates the identity vertex root via ContextHint.Reads = [identityKey]
// (the target-existence guard, mirroring MarkExpired's vertex_alive check) and
// the piiKey aspect via ContextHint.OptionalReads = [identityKey + ".piiKey"]
// (read-posture class (d), §2.5/§13) — declared-absence-tolerant since the
// identity may never have received a sensitive write; a declared `reads` entry
// would fault hydration fatally (HydrationMiss) on that legitimate absence.
//
// ShredIdentityKey also erases the identity's live dedup-hygiene footprint in
// the SAME atomic batch as the shred intent (dedup-over-encrypted-pii-design.md
// §3.5): the identity's owned identityindex vertices (enumerated via their
// inbound "indexes" links — linkage is ownership, so this needs no decryption)
// are tombstoned along with those links, and every duplicateOf pair link
// touching the identity (enumerated in BOTH directions — the identity may be
// the newer or the older side of a pair) is tombstoned. This is why the shred
// erase needs no separate worker step and no ordering window against
// Vault.ShredKey: the decrypt window the async privacy-worker races is closed
// the instant this commit lands (envelope.Shredded is checked before
// WrappedDEK-emptiness in internal/vault), so anything this commit doesn't
// erase would never be reachable by a later erase anyway — erasing here, where
// the plaintext-derived hashes are still linked, is the only decrypt-free
// window that exists. Both enumerations are class-(e) (Contract #2 §2.5.1,
// bounded kv.Links, metadata-only in ContextHint.Enumerations) and are
// declared by every ShredIdentityKey dispatcher. Re-shred is idempotent: a
// prior shred already tombstoned these links, so the enumerations return
// nothing on a second run.
//
// The DDL also admits RecordShredFinalization{identityKey, step} — the
// Fire-4b durable progress record the two async shred listeners submit under
// the identity.system.privacy service actor once their irreversible work
// lands: internal/privacyworker records step "vaultKeyDestroyed" after
// Vault.ShredKey succeeds; internal/refractor/keyshredded records step
// "projectionsNullified" after every configured nullify target succeeded.
// Each step flips one boolean on the piiKey envelope (+ an At stamp) — the
// state the shredStatus lens projects so an operator can see in-flight/stuck
// shreds. The submitters declare the piiKey in ContextHint.Reads, so the
// update is hydrated + OCC-conditioned: the two sibling records racing on
// the system lane's concurrent pump workers collapse to a transparent
// commit-path RevisionConflict retry instead of a whole-document
// last-writer-wins that would silently lose one flag. The script fail-closes
// when the envelope is absent or not shredded (a finalization can only
// follow a ShredIdentityKey commit, which always durably writes the
// envelope).
func ShredIdentityKeyDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     shredIdentityKeyDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"ShredIdentityKey", "RecordShredFinalization"},
		Description: "Right-to-erasure trigger (vault-crypto-shredding-design.md §2.2/§2.4, Contract #3 " +
			"§3.10/§3.11). ShredIdentityKey{identityKey} marks vtx.identity.<NanoID>.piiKey shredded=true " +
			"(an unconditioned update; writes a durable empty-wrappedDEK placeholder when the identity never " +
			"received a sensitive write, so the shred survives a Processor restart) and emits " +
			"privacy.keyShredded{identityKey}. Recording intent only: the irreversible Vault.ShredKey key " +
			"destruction happens asynchronously in the privacy-worker's event listener, never on this " +
			"synchronous commit path. Requires identityKey in ContextHint.Reads (the target-existence guard); " +
			"rejects an absent or tombstoned identity. Also admits " +
			"RecordShredFinalization{identityKey, step: vaultKeyDestroyed|projectionsNullified} — the async " +
			"listeners' durable progress record (Fire 4b): flips the named boolean (+ an At stamp) on the " +
			"already-shredded piiKey envelope, the state the shredStatus lens projects for operators.",
		Script: shredIdentityKeyDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"identityKey":{"type":"string","description":"vtx.identity.<NanoID> — the identity whose PII key is being shredded (or whose shred progress is being recorded)."},` +
			`"step":{"type":"string","enum":["vaultKeyDestroyed","projectionsNullified"],"description":"RecordShredFinalization only — which async finalization step completed."}},` +
			`"required":["identityKey"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.identity.<NanoID> of the shredded identity."}}}`,
		FieldDescription: map[string]string{
			"identityKey": "Full vtx.identity.<NanoID> key of the identity to shred. Must exist and not be tombstoned; declared in ContextHint.Reads (ShredIdentityKey only — RecordShredFinalization is read-free and checks the piiKey via kv.Read).",
			"step":        "RecordShredFinalization only: vaultKeyDestroyed (privacy-worker, after Vault.ShredKey) or projectionsNullified (Refractor keyshredded listener, after all nullify targets succeeded).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "ShredIdentityKey — an identity that received PII",
				Payload: map[string]any{"identityKey": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Marks vtx.identity.<NanoID>.piiKey shredded=true, emits privacy.keyShredded, and returns " +
					"primaryKey=identityKey. The privacy-worker's async listener then calls Vault.ShredKey, after which " +
					"every Encrypt/Decrypt for this identity fails with vault.ErrKeyShredded — the ciphertext already in " +
					"Core KV and JetStream history becomes permanently unrecoverable.",
			},
			{
				Name:    "ShredIdentityKey — an identity that never received PII",
				Payload: map[string]any{"identityKey": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "No piiKey aspect existed, so a durable placeholder is written (empty wrappedDEK, " +
					"shredded=true) instead of a real envelope — pre-emptively and PERMANENTLY blocking any future " +
					"sensitive write to this identity from ever encrypting successfully, even across a Processor restart.",
			},
			{
				Name:    "RecordShredFinalization — the privacy-worker records key destruction",
				Payload: map[string]any{"identityKey": "vtx.identity.<NanoID>", "step": "vaultKeyDestroyed"},
				ExpectedOutcome: "Sets piiKey.vaultKeyDestroyed=true (+ vaultKeyDestroyedAt) on the already-shredded " +
					"envelope. Rejected (FailedPrecondition) when the envelope is not shredded; rejected (NotFound) " +
					"when no piiKey exists — a finalization can only follow a ShredIdentityKey commit.",
			},
		},
	}
}

// shredIdentityKeyDDLScript handles ShredIdentityKey. Mirrors
// orchestration-base's markExpiredDDLScript: the same required_string /
// vertex_alive helper shapes (Starlark has no load(), so every DDL script
// carries its own small copies).
const shredIdentityKeyDDLScript = `
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

INDEXES_PAGE_LIMIT = 256
MAX_INDEXES_PAGES = 64

def collect_owned_indexes(identity_key):
    # dedup-over-encrypted-pii-design.md §3.5: the identity's owned
    # identityindex vertices are enumerable via their inbound "indexes"
    # links (linkage IS ownership) without knowing the plaintext the hash
    # derives from. Enumerated via the sanctioned bounded kv.Links
    # (Contract #2 §2.5.1), direction "in" -- the identity is the indexes
    # link's TARGET (identityindex vertex is source, per Contract #1 §1.1).
    # Mirrors identity-hygiene's collect_indexes_repoints.
    hits = []
    cursor = None
    for _page in range(MAX_INDEXES_PAGES):
        # read-posture: (e) relation=indexes epoch=none (read-only guard: an
        # indexes link created concurrently with the shred slips past --
        # accepted, same posture as identity-hygiene's merge-time enumeration)
        links, cursor = kv.Links(identity_key, "indexes", "in", cursor, INDEXES_PAGE_LIMIT)
        for lk in links:
            if lk.isDeleted:
                continue
            hits.append(lk)
        if cursor == None:
            return hits
    fail("IdentityIndexFanoutTooLarge: " + identity_key + " has too many indexes links to enumerate at shred time")

DUPLICATE_OF_PAGE_LIMIT = 256
MAX_DUPLICATE_OF_PAGES = 64

def collect_duplicate_of_direction(identity_key, direction):
    hits = []
    cursor = None
    for _page in range(MAX_DUPLICATE_OF_PAGES):
        # read-posture: (e) relation=duplicateOf epoch=none (read-only
        # guard: a duplicateOf link created concurrently with the shred
        # slips past -- accepted, same posture as the indexes enumeration
        # above)
        links, cursor = kv.Links(identity_key, "duplicateOf", direction, cursor, DUPLICATE_OF_PAGE_LIMIT)
        for lk in links:
            if lk.isDeleted:
                continue
            hits.append(lk)
        if cursor == None:
            return hits
    fail("IdentityDuplicateOfFanoutTooLarge: " + identity_key + " has too many duplicateOf links (" + direction + ") to enumerate at shred time")

def collect_duplicate_of_links(identity_key):
    # dedup-over-encrypted-pii-design.md §3.5: every duplicateOf pair link
    # touching the identity is durable pair evidence that must not outlive
    # the shred. The identity may be either side of the pair (the newer
    # identity that matched an incumbent, source; or an incumbent later
    # identities matched against, target) -- both directions are enumerated.
    return collect_duplicate_of_direction(identity_key, "out") + collect_duplicate_of_direction(identity_key, "in")

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "ShredIdentityKey":
        identity_key = required_string(p, "identityKey")
        parts = identity_key.split(".")
        if len(parts) != 3 or parts[0] != "vtx" or parts[1] != "identity":
            fail("InvalidArgument: identityKey: required vtx.identity.<NanoID> (exactly 3 segments); got " + identity_key)

        if not vertex_alive(state, identity_key):
            fail("NotFound: identityKey " + identity_key + " is absent or tombstoned")

        pii_key_key = identity_key + ".piiKey"

        # kv.Read tolerates absence (-> None) -- the identity may never have
        # received a sensitive write. Either way something durable is ALWAYS
        # written: LocalBackend's shredded-identity deny-list is in-memory
        # only, so skipping the mutation when no piiKey exists would let a
        # sensitive write arriving after a Processor restart mint a fresh,
        # unshredded key and silently reopen the identity to PII.
        # read-posture: (d) declared in contextHint.optionalReads by the
        # Loupe UI's ShredIdentityKey submit (cmd/loupe/web/js/views/graph.js)
        existing = kv.Read(pii_key_key)
        if existing != None and not existing.isDeleted:
            data = dict(existing.data)
            data["shredded"] = True
            # A (re-)shred starts a NEW finalization cycle: clear any prior
            # cycle's RecordShredFinalization progress so the shredStatus lens
            # shows this shred as in-flight until its own async records land
            # (the listeners re-drive off this commit's keyShredded event).
            for stale in ["vaultKeyDestroyed", "vaultKeyDestroyedAt",
                          "projectionsNullified", "projectionsNullifiedAt"]:
                data.pop(stale, None)
        else:
            # No real key was ever minted -- an empty wrappedDEK placeholder,
            # durably shredded=true, so a future Encrypt/Decrypt attempt is
            # rejected by internal/vault's envelope.Shredded check (checked
            # BEFORE the WrappedDEK-empty validation) regardless of whether
            # the in-memory deny-list survived a restart.
            data = {"wrappedDEK": "", "keyId": identity_key, "kekVersion": "",
                    "alg": "", "createdAt": op.submittedAt, "shredded": True}
        data["shreddedAt"] = op.submittedAt

        mutations = [{"op": "update", "key": pii_key_key,
            "document": {"class": "piiKey", "vertexKey": identity_key,
                         "localName": "piiKey", "isDeleted": False, "data": data}}]

        # dedup-over-encrypted-pii-design.md §3.5: erase the live dedup-hygiene
        # footprint in this same atomic batch -- decrypt-free, linkage is
        # ownership. Idempotent: a re-shred's enumerations find nothing (the
        # links are already tombstoned from the first shred).
        idx_hits = collect_owned_indexes(identity_key)
        dup_hits = collect_duplicate_of_links(identity_key)

        # Pre-flight batch-size cap (mirrors identity-hygiene's MergeIdentity
        # MergeBatchTooLarge guard): 1 (piiKey) + 2 per owned index (vertex +
        # link) + 1 per duplicateOf link. A right-to-erasure op must never
        # silently drop the erasure half -- fail loud and early rather than
        # let step8_commit's BatchTooLarge (itself terminal on redelivery)
        # surface as an opaque, unshreddable identity.
        total_muts = 1 + len(idx_hits) * 2 + len(dup_hits)
        if total_muts > 999:
            fail("ShredBatchTooLarge: " + identity_key + " has too large a dedup-hygiene footprint (" + str(total_muts) + " mutations) to erase in one shred commit")

        for lk in idx_hits:
            idx_vertex_key = lk.sourceVertex
            # read-posture: (e) per-candidate follow-up read off the
            # enumeration above (data-derived key: the hash is not
            # dispatch-known ahead of collect_owned_indexes)
            idx_vtx = kv.Read(idx_vertex_key)
            idx_data = idx_vtx.data if idx_vtx != None and idx_vtx.data != None else {}
            mutations.append({"op": "update", "key": idx_vertex_key,
                "document": {"class": "identityindex", "isDeleted": True, "data": idx_data}})
            mutations.append({"op": "update", "key": lk.key,
                "document": {"class": "indexes", "isDeleted": True, "data": lk.data}})

        for lk in dup_hits:
            dup_class = getattr(lk, "class") if hasattr(lk, "class") else "duplicateOf"
            mutations.append({"op": "update", "key": lk.key,
                "document": {"class": dup_class, "isDeleted": True, "data": lk.data}})

        events = [{"class": "privacy.keyShredded", "data": {"identityKey": identity_key}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": identity_key}}

    if ot == "RecordShredFinalization":
        identity_key = required_string(p, "identityKey")
        parts = identity_key.split(".")
        if len(parts) != 3 or parts[0] != "vtx" or parts[1] != "identity":
            fail("InvalidArgument: identityKey: required vtx.identity.<NanoID> (exactly 3 segments); got " + identity_key)
        step = required_string(p, "step")
        if step != "vaultKeyDestroyed" and step != "projectionsNullified":
            fail("InvalidArgument: step: required vaultKeyDestroyed or projectionsNullified; got " + step)

        # The piiKey comes from the DECLARED read set (ContextHint.Reads --
        # the submitters always declare it), NOT the lazy kv.Read seam:
        # a hydrated read is OCC-conditioned by the commit path, so the two
        # sibling records (vaultKeyDestroyed / projectionsNullified racing on
        # the system lane's concurrent workers) collapse to a transparent
        # RevisionConflict retry instead of a whole-document last-writer-wins
        # that silently loses one flag. ShredIdentityKey ALWAYS durably writes
        # an envelope before its keyShredded event exists, so a declared-but-
        # absent piiKey fails hydration (HydrationMiss) -- the same "no shred
        # to record" rejection the in-script guards express.
        pii_key_key = identity_key + ".piiKey"
        if pii_key_key not in state:
            fail("NotFound: " + pii_key_key + " is absent -- RecordShredFinalization requires a prior ShredIdentityKey")
        existing = state[pii_key_key]
        if existing == None or (hasattr(existing, "isDeleted") and existing.isDeleted):
            fail("NotFound: " + pii_key_key + " is tombstoned -- RecordShredFinalization requires a prior ShredIdentityKey")
        data = dict(existing.data)
        if not data.get("shredded", False):
            fail("FailedPrecondition: " + pii_key_key + " is not shredded -- RecordShredFinalization requires a prior ShredIdentityKey")
        data[step] = True
        data[step + "At"] = op.submittedAt

        mutations = [{"op": "update", "key": pii_key_key,
            "document": {"class": "piiKey", "vertexKey": identity_key,
                         "localName": "piiKey", "isDeleted": False, "data": data}}]
        return {"mutations": mutations, "events": [], "response": {"primaryKey": identity_key}}

    fail("shredIdentityKey DDL: unknown operationType: " + ot)
`

// KeyShreddedEventDDL returns the event-type DDL meta-vertex declaration for
// privacy.keyShredded (Contract #3 §3.4 typed-event model). Registering it
// (rather than leaving the class unregistered, as orchestration.freshnessMarked
// does) documents the event's schema for the privacy-worker consumer + any
// future Weaver shred-finalization convergence lens (design §2.4 point 4,
// Fire 4) that subscribes events.privacy.>.
func KeyShreddedEventDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName: keyShreddedEventDDL,
		Class:         "meta.ddl.eventType",
		Description: "Emitted by ShredIdentityKey (design §2.2/§2.4) once the identity's piiKey has been " +
			"durably marked shredded=true in Core KV (a real envelope if one existed, else a placeholder). " +
			"Consumed by the privacy-worker (internal/privacyworker), which calls Vault.ShredKey(identityKey) " +
			"— the irreversible key destruction that makes every ciphertext protected by that key, live and " +
			"in JetStream history, permanently unrecoverable.",
		Script: keyShreddedEventDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"identityKey":{"type":"string","description":"vtx.identity.<NanoID> — the identity whose key was shredded."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"identityKey": "The identity whose PII key was shredded.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "privacy.keyShredded",
				Payload:         map[string]any{"identityKey": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Consumed by the privacy-worker's durable events.privacy.keyShredded listener, which calls Vault.ShredKey.",
			},
		},
	}
}

// keyShreddedEventDDLScript is the declaration-only Starlark for the
// keyShredded event-type DDL. An event-type DDL is never dispatched as an
// operation (events are emitted by a script's `events` return list, not
// executed); this mirrors piiKeyDDLScript / freshnessExpiryAspectDDLScript's
// fail-closed stub.
const keyShreddedEventDDLScript = `
def execute(state, op):
    fail("event-type DDL: not an operation handler: " + op.operationType)
`
