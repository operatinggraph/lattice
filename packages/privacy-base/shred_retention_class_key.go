package privacybase

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// shredRetentionClassKeyDDL is the canonical name of the vertexType DDL owning
// the ShredRetentionClassKey op (retention-class-key-custody-design.md §4.3).
const shredRetentionClassKeyDDL = "shredRetentionClassKey"

// retentionClassKeyShreddedEventDDL is the canonical name of the
// retentionClassKeyShredded event-type DDL. Per Contract #3 §3.4 the
// canonicalName of a registered event-type DDL equals the event's own class,
// so this is the full domain-qualified class string.
const retentionClassKeyShreddedEventDDL = "privacy.retentionClassKeyShredded"

// ShredRetentionClassKeyDDL returns the DDL meta-vertex declaration for the
// ShredRetentionClassKey op — the erase-on-EXPIRY destruction verb
// (retention-class-key-custody-design.md §4.3).
//
// It is the exact sibling of ShredIdentityKey, and the difference between them
// is the whole point of retention-class custody. ShredIdentityKey destroys a
// PERSON's key on request, which is why it is reachable from the identityErasure
// pattern and ships no grant. ShredRetentionClassKey destroys a retention
// CLASS's key when the controller's retention obligation for that class has run
// out — a scheduled, per-class act of the data controller, not a subject's
// request. A record custodied on a class therefore survives its subject's
// erasure (that is §6.4's pseudonymization criterion) and dies here instead.
//
// ShredRetentionClassKey{retentionClassKey} marks vtx.retentionclass.<NanoID>
// .piiKey shredded=true and emits privacy.retentionClassKeyShredded. Like its
// sibling it records INTENT in Core KV only — the irreversible Vault.ShredKey
// destruction happens asynchronously in the privacy-worker's listener
// (internal/privacyworker), never on the synchronous commit path, so a KMS
// round-trip can never block or fail an operation commit.
//
// A class that never received a sensitive write has no piiKey aspect yet, and
// this op still writes ONE — a durable empty-wrappedDEK placeholder with
// shredded=true — for the identical reason ShredIdentityKey does:
// LocalBackend's deny-list is in-memory only (internal/vault/local.go), so
// without a Core-KV-persisted record a sensitive write arriving after a
// Processor restart would mint a brand-new unshredded class DEK and silently
// reopen the whole retention class. envelope.Shredded is honored before the
// WrappedDEK-empty validation, so the placeholder durably blocks every future
// Encrypt/Decrypt for the class regardless of restarts.
//
// The op hydrates the holder vertex root via ContextHint.Reads =
// [retentionClassKey] (the target-existence guard) and the key aspect via
// ContextHint.OptionalReads = [retentionClassKey + ".piiKey"] (read-posture
// class (d)) — declared-absence-tolerant, since a class may never have received
// a sensitive write and a declared `reads` entry would fault HydrationMiss on
// that legitimate absence.
//
// The DDL also admits RecordRetentionClassShredFinalization{retentionClassKey,
// step} — the durable progress record the two async listeners submit under the
// identity.system.privacy service actor once their irreversible work lands:
// internal/privacyworker records "vaultKeyDestroyed" after Vault.ShredKey
// succeeds, and the Refractor's destruction consumer records
// "projectionsRebuilt" once every secure lens carrying this holder's type is
// back at zero lag.
//
// Both steps are accepted, each because its producer exists. The step was
// declared before it was accepted — the lens had to grow the column first, since
// a compliance surface cannot observe a step it has no column for — and stayed
// refused meanwhile, because a step this script accepts is a step the
// finalization grant lets an operator write, so accepting one with no producer
// would have made every value it could hold a forged attestation of a rebuild
// that never ran.
//
// Why a separate finalization verb rather than widening RecordShredFinalization:
// that op validates its subject as vtx.identity.<NanoID> and its steps are the
// identity plane's (projectionsNullified — a row-nullify that keys on the row's
// own identity column, which a class-custodied row structurally does not have).
// Two subjects with two different step vocabularies in one verb would need a
// branch on the key's type segment in every guard; two verbs need none.
func ShredRetentionClassKeyDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     shredRetentionClassKeyDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"ShredRetentionClassKey", "RecordRetentionClassShredFinalization"},
		Description: "Retention-expiry destruction verb (retention-class-key-custody-design.md §4.3). " +
			"ShredRetentionClassKey{retentionClassKey} marks vtx.retentionclass.<NanoID>.piiKey " +
			"shredded=true (an unconditioned update; writes a durable empty-wrappedDEK placeholder when the " +
			"class never received a sensitive write, so the shred survives a Processor restart) and emits " +
			"privacy.retentionClassKeyShredded{retentionClassKey}. Recording intent only: the irreversible " +
			"Vault.ShredKey destruction happens asynchronously in the privacy-worker's event listener, never " +
			"on this synchronous commit path. Requires that key in ContextHint.Reads (the target-existence " +
			"guard); rejects an absent or tombstoned holder. This is the erase-on-EXPIRY half of custody: a " +
			"record custodied on a retention class survives its subject's ShredIdentityKey and becomes " +
			"unrecoverable here instead. Also admits RecordRetentionClassShredFinalization{retentionClassKey, " +
			"step: vaultKeyDestroyed|projectionsRebuilt} — the async listeners' durable progress record, the " +
			"state the retentionKeyStatus lens projects for operators.",
		Script: shredRetentionClassKeyDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"retentionClassKey":{"type":"string","description":"vtx.retentionclass.<NanoID> — the retention-class key holder whose DEK is being destroyed (or whose destruction progress is being recorded)."},` +
			`"step":{"type":"string","enum":["vaultKeyDestroyed","projectionsRebuilt"],"description":"RecordRetentionClassShredFinalization only — which async finalization step completed."}}}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.retentionclass.<NanoID> of the shredded key holder."}}}`,
		FieldDescription: map[string]string{
			"retentionClassKey": "Full vtx.retentionclass.<NanoID> key of the retention-class holder to shred. Must exist and not be tombstoned; declared in ContextHint.Reads.",
			"step":              "RecordRetentionClassShredFinalization only: vaultKeyDestroyed (privacy-worker, after Vault.ShredKey) or projectionsRebuilt (Refractor destruction consumer, after every affected secure lens is back at zero lag).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "ShredRetentionClassKey — a class whose retention obligation has expired",
				Payload: map[string]any{"retentionClassKey": "vtx.retentionclass.<NanoID>"},
				ExpectedOutcome: "Marks vtx.retentionclass.<NanoID>.piiKey shredded=true, emits " +
					"privacy.retentionClassKeyShredded, and returns primaryKey=retentionClassKey. The " +
					"privacy-worker's async listener then calls Vault.ShredKey, after which every record " +
					"custodied on this class — live in Core KV and in JetStream history — is permanently " +
					"unrecoverable, whether or not its subject was ever erased.",
			},
			{
				Name:    "ShredRetentionClassKey — a class that never received a sensitive write",
				Payload: map[string]any{"retentionClassKey": "vtx.retentionclass.<NanoID>"},
				ExpectedOutcome: "No piiKey aspect existed, so a durable placeholder is written (empty " +
					"wrappedDEK, shredded=true) instead of a real envelope — permanently blocking any future " +
					"sensitive write custodied on this class from ever encrypting successfully, even across a " +
					"Processor restart.",
			},
			{
				Name:    "RecordRetentionClassShredFinalization — the privacy-worker records key destruction",
				Payload: map[string]any{"retentionClassKey": "vtx.retentionclass.<NanoID>", "step": "vaultKeyDestroyed"},
				ExpectedOutcome: "Sets piiKey.vaultKeyDestroyed=true (+ vaultKeyDestroyedAt) on the " +
					"already-shredded envelope. Rejected (FailedPrecondition) when the envelope is not " +
					"shredded; rejected (NotFound) when no piiKey exists — a finalization can only follow a " +
					"ShredRetentionClassKey commit.",
			},
		},
	}
}

// shredRetentionClassKeyDDLScript handles ShredRetentionClassKey and
// RecordRetentionClassShredFinalization. Mirrors shredIdentityKeyDDLScript —
// the same required_string / vertex_alive / parts_of helper shapes (Starlark
// has no load(), so every DDL script carries its own small copies).
//
// It carries NO subject_of equivalent: ShredIdentityKey accepts both
// identityKey and subjectKey because a Loom systemOp step submits the payload
// field the engine chose, and the identityErasure pattern binds that op. No
// pattern binds this one — a retention expiry is scheduled per class, not
// driven by a subject's erasure — so the single documented name is the only
// name, and inventing an alias for a submitter that does not exist would be
// vocabulary nobody can account for.
const shredRetentionClassKeyDDLScript = `
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

def parts_of(key, name, want_type):
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

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "ShredRetentionClassKey":
        holder_key = required_string(p, "retentionClassKey")
        parts_of(holder_key, "retentionClassKey", "retentionclass")

        if not vertex_alive(state, holder_key):
            fail("NotFound: retentionClassKey " + holder_key + " is absent or tombstoned")

        pii_key_key = holder_key + ".piiKey"

        # kv.Read tolerates absence (-> None) -- the class may never have
        # received a sensitive write. Either way something durable is ALWAYS
        # written: LocalBackend's shredded deny-list is in-memory only, so
        # skipping the mutation when no piiKey exists would let a sensitive
        # write arriving after a Processor restart mint a fresh, unshredded
        # class DEK and silently reopen every record the class holds.
        # read-posture: (d) declared in contextHint.optionalReads by every
        # dispatcher of this op.
        existing = kv.Read(pii_key_key)
        if existing != None and not existing.isDeleted:
            data = dict(existing.data)
            data["shredded"] = True
            # A (re-)shred clears the prior cycle's progress so the
            # retentionKeyStatus lens shows this shred as in-flight until its
            # own async records land (the listeners re-drive off this commit's
            # event). This orders the COMMITTED state, not every in-flight
            # submitter: a finalization from the previous cycle that loses the
            # OCC race against this write is transparently retried by the
            # commit path, re-hydrates the post-re-shred document, passes the
            # shredded guard, and stamps the new cycle with the old cycle's
            # work. The outcome is harmless today -- Vault.ShredKey is
            # idempotent, so what it attests is true of both cycles -- but the
            # row carries no cycle discriminator to tell them apart, unlike the
            # identity plane's sealedForShreddedAt. Filed, not papered over.
            for stale in ["vaultKeyDestroyed", "vaultKeyDestroyedAt",
                          "projectionsRebuilt", "projectionsRebuiltAt"]:
                data.pop(stale, None)
        else:
            # No real key was ever minted -- an empty wrappedDEK placeholder,
            # durably shredded=true, so a future Encrypt/Decrypt attempt is
            # rejected by internal/vault's envelope.Shredded check (checked
            # BEFORE the WrappedDEK-empty validation) regardless of whether
            # the in-memory deny-list survived a restart.
            data = {"wrappedDEK": "", "keyId": holder_key, "kekVersion": "",
                    "alg": "", "createdAt": op.submittedAt, "shredded": True}
        data["shreddedAt"] = op.submittedAt

        mutations = [{"op": "update", "key": pii_key_key,
            "document": {"class": "piiKey", "vertexKey": holder_key,
                         "localName": "piiKey", "isDeleted": False, "data": data}}]

        events = [{"class": "privacy.retentionClassKeyShredded", "data": {"retentionClassKey": holder_key}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": holder_key}}

    if ot == "RecordRetentionClassShredFinalization":
        holder_key = required_string(p, "retentionClassKey")
        parts_of(holder_key, "retentionClassKey", "retentionclass")
        step = required_string(p, "step")
        # Both steps have a producer now. vaultKeyDestroyed comes from
        # internal/privacyworker after Vault.ShredKey; projectionsRebuilt comes
        # from internal/refractor/classkeyshredded, once every secure lens
        # declaring this holder's type has rebuilt to zero lag. Each producer
        # submits only after its own irreversible work landed; the actor guard
        # below is what keeps anyone else from submitting in its place.
        if step != "vaultKeyDestroyed" and step != "projectionsRebuilt":
            fail("InvalidArgument: step: required vaultKeyDestroyed or projectionsRebuilt; got " + step)

        # Both finalization steps are ATTESTATIONS on a compliance surface, and
        # the verb's own grant is too wide to be their only gate: it is
        # scope:any granted to operator (permissions.go), so any operator could
        # stamp either step one second after the destruction commits -- the
        # shredded precondition below is true by construction at that moment.
        # Pin the writer to the privacy service actor instead. Its vertex is
        # declared in contextHint.reads by every submitter of this verb, so an
        # undeclared or mismatched actor fails closed here rather than being
        # believed. "class" is a reserved word in Starlark, hence getattr.
        #
        # What this does NOT do: close actor impersonation on the ops lane. The
        # envelope's actor is self-asserted by whoever can publish there, and
        # constraining that is a platform-wide seam, not this script's. The
        # guarantee is narrower than "only the privacy worker can write this" --
        # it is "only the privacy service actor identity can" -- and saying so
        # is the point, because a comment claiming the stronger property is what
        # stops the next reader from looking.
        actor_doc = state[op.actor] if op.actor in state else None
        if actor_doc == None or (hasattr(actor_doc, "isDeleted") and actor_doc.isDeleted):
            fail("PermissionDenied: RecordRetentionClassShredFinalization requires the identity.system.privacy service actor; op.actor was not declared in contextHint.reads or is absent")
        if getattr(actor_doc, "class", "") != "identity.system.privacy":
            fail("PermissionDenied: RecordRetentionClassShredFinalization may be recorded only by the identity.system.privacy service actor")

        # The piiKey comes from the DECLARED read set (ContextHint.Reads --
        # the submitters always declare it), NOT the lazy kv.Read seam: a
        # hydrated read is OCC-conditioned by the commit path, so the two
        # sibling records (vaultKeyDestroyed / projectionsRebuilt racing on the
        # system lane's concurrent workers) collapse to a transparent
        # RevisionConflict retry instead of a whole-document last-writer-wins
        # that silently loses one flag. ShredRetentionClassKey ALWAYS durably
        # writes an envelope before its event exists, so a declared-but-absent
        # piiKey would HydrationMiss the moment the script touches it --
        # deferred past hydration, but still the same "no shred to record"
        # rejection the in-script guards express.
        pii_key_key = holder_key + ".piiKey"
        if pii_key_key not in state:
            fail("NotFound: " + pii_key_key + " is absent -- RecordRetentionClassShredFinalization requires a prior ShredRetentionClassKey")
        existing = state[pii_key_key]
        if existing == None or (hasattr(existing, "isDeleted") and existing.isDeleted):
            fail("NotFound: " + pii_key_key + " is tombstoned -- RecordRetentionClassShredFinalization requires a prior ShredRetentionClassKey")
        data = dict(existing.data)
        if not data.get("shredded", False):
            fail("FailedPrecondition: " + pii_key_key + " is not shredded -- RecordRetentionClassShredFinalization requires a prior ShredRetentionClassKey")
        data[step] = True
        data[step + "At"] = op.submittedAt

        mutations = [{"op": "update", "key": pii_key_key,
            "document": {"class": "piiKey", "vertexKey": holder_key,
                         "localName": "piiKey", "isDeleted": False, "data": data}}]
        return {"mutations": mutations, "events": [], "response": {"primaryKey": holder_key}}

    fail("shredRetentionClassKey DDL: unknown operationType: " + ot)
`

// RetentionClassKeyShreddedEventDDL returns the event-type DDL meta-vertex
// declaration for privacy.retentionClassKeyShredded (Contract #3 §3.4 typed-
// event model), mirroring KeyShreddedEventDDL.
//
// The event has TWO independent durable consumers, exactly as
// privacy.keyShredded does: the privacy-worker destroys the Vault key, and the
// Refractor's classkeyshredded consumer rebuilds every secure lens whose
// declared holder types include retentionclass. Neither blocks the other and
// each records its own finalization step.
func RetentionClassKeyShreddedEventDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName: retentionClassKeyShreddedEventDDL,
		Class:         "meta.ddl.eventType",
		Description: "Emitted by ShredRetentionClassKey (retention-class-key-custody-design.md §4.3) once " +
			"the retention-class holder's piiKey has been durably marked shredded=true in Core KV (a real " +
			"envelope if one existed, else a placeholder). Consumed by the privacy-worker " +
			"(internal/privacyworker), which calls Vault.ShredKey(retentionClassKey) — the irreversible key " +
			"destruction that makes every record custodied on that class, live and in JetStream history, " +
			"permanently unrecoverable. A second consumer in the Refractor rebuilds the secure lenses whose " +
			"declared holder types include retentionclass, so the destruction reaches the read models rather " +
			"than waiting for an unrelated event to reproject them.",
		Script: retentionClassKeyShreddedEventDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"retentionClassKey":{"type":"string","description":"vtx.retentionclass.<NanoID> — the key holder whose DEK was shredded."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"retentionClassKey": "The retention-class holder whose DEK was shredded.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "privacy.retentionClassKeyShredded",
				Payload:         map[string]any{"retentionClassKey": "vtx.retentionclass.<NanoID>"},
				ExpectedOutcome: "Consumed by the privacy-worker's durable events.privacy.retentionClassKeyShredded listener, which calls Vault.ShredKey.",
			},
		},
	}
}

// retentionClassKeyShreddedEventDDLScript is the declaration-only Starlark for
// the retentionClassKeyShredded event-type DDL. An event-type DDL is never
// dispatched as an operation (events are emitted by a script's `events` return
// list, not executed); this mirrors keyShreddedEventDDLScript's fail-closed
// stub.
const retentionClassKeyShreddedEventDDLScript = `
def execute(state, op):
    fail("event-type DDL: not an operation handler: " + op.operationType)
`
