package privacybase

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// sealIdentityForErasureDDL is the canonical name of the vertexType DDL owning
// the SealIdentityForErasure op (erasure-orchestration-design.md §6).
const sealIdentityForErasureDDL = "sealIdentityForErasure"

// erasureRequestedAspectDDL is the canonical name of the aspectType DDL
// declaring the vtx.identity.<NanoID>.erasureRequested marker. Step 6 keys
// permittedCommands on the MUTATION DOCUMENT's class, so this declaration
// admits a create/update whose document declares class `erasureRequested` and
// refuses that same write to any other operation. Read the limits of that in
// ErasureRequestedAspectDDL's comment before relying on it — it gates the
// CLASS, not the key, and it cannot see a tombstone at all.
const erasureRequestedAspectDDL = "erasureRequested"

// erasureRequestedEventDDL is the canonical name of the erasureRequested
// event-type DDL. Per Contract #3 §3.4 a registered event-type DDL's
// canonicalName equals the event's own class.
const erasureRequestedEventDDL = "privacy.erasureRequested"

// ErasureRequestedAspectDDL declares the erasure-request marker aspect.
//
// The marker is the anchor of the whole erasure plane: its presence means
// "this person is being forgotten", and every writer of an erasable
// representation of that person reads it and fails closed
// (erasure-orchestration-design.md §6). It is deliberately NOT
// piiKey.shredded. Those two facts differ:
//
//   - `piiKey.shredded` means the identity's DEK is dead. A retention-class
//     key shred (retention-class-key-custody-design.md) will one day shred a
//     key for reasons that have nothing to do with a person's erasure, and
//     conflating the two would silently close that person's write path.
//   - `.erasureRequested` means a person invoked a right to be forgotten.
//
// It is also the seam between two packages: piiKey is privacy-base-owned,
// while the write-path gates that consume this marker live in
// identity-domain and identity-hygiene. An explicit marker aspect is a
// contract between them; reaching across into another package's envelope
// shape would not be.
//
// Not sensitive: it carries two timestamps and no PII, so it attaches to an
// identity vertex without step 6's sensitive-aspect custody rule firing.
//
// # What permittedCommands here does and does not enforce
//
// §7's convergence rests on this marker never being removed, so it matters to
// be exact about how much of that the platform holds up.
//
//   - It DOES refuse a create/update whose document declares class
//     `erasureRequested` and whose operationType is not SealIdentityForErasure
//     (`internal/processor/step6_validate.go` resolves the governing DDL from
//     the mutation document's class).
//   - It does NOT refuse a TOMBSTONE. A tombstone mutation carries no document
//     (Contract #3 §3.3, enforced in `starlark_runner.go`), so its class is
//     empty and step 6 skips the DDL block entirely. Any package script could
//     tombstone this key.
//   - It gates the CLASS, not the KEY: a mutation at this key declaring some
//     other class falls to resolveGoverningDDL's permissive default.
//
// So non-removal is a convention held by code review, not a platform-enforced
// invariant. That is the same honest position `piiKey` already records for the
// same mechanism (`ddls.go`), and it is the trust model the whole package-script
// plane runs on: package scripts are reviewed code, not untrusted input. If the
// invariant ever needs real enforcement it is a Processor change — a key-shaped
// reserved-aspect guard — not something this declaration can buy.
func ErasureRequestedAspectDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     erasureRequestedAspectDDL,
		Class:             "meta.ddl.aspectType",
		Sensitive:         false,
		PermittedCommands: []string{"SealIdentityForErasure"},
		Description: "Erasure-request marker (erasure-orchestration-design.md §6): " +
			"vtx.identity.<NanoID>.erasureRequested = {requestedAt, shreddedAt}. Its PRESENCE is the signal " +
			"that this person is being forgotten, read as a class-(d) optionalReads by every writer that " +
			"could otherwise create a fresh erasable representation of them (ClaimIdentity, " +
			"CompleteCredentialLink, ReconcileCredentialBinding — each in EITHER link position, since the shred " +
			"erases boundTo in both directions — plus CreateUnclaimedIdentity, which stops treating a sealed " +
			"identity as a dedup incumbent so no fresh duplicateOf names it, and " +
			"identity-hygiene's MergeIdentity on either side. MergeIdentity refuses with ErasedIdentity; the " +
			"claim and link paths refuse with the generic ClaimKeyInvalid the anti-enumeration rule requires " +
			"(outcome `erased`, carried in Health KV only) and the reconcile path with " +
			"CredentialReconcileRejected: erased. " +
			"Distinct from piiKey.shredded by design: shredded means the key is dead (a retention-class " +
			"shred will one day mean it for non-erasure reasons), erasureRequested means the person " +
			"invoked erasure. Only SealIdentityForErasure may write this CLASS, and only " +
			"SealIdentityForErasure may REMOVE it: the write gate reads the class stored at the key, so a " +
			"documentless tombstone of this marker is held to the same permittedCommands list as a write.",
		Script: erasureRequestedAspectDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"requestedAt":{"type":"string","description":"RFC3339 instant the erasure request was sealed (the first SealIdentityForErasure commit; preserved verbatim across re-seals)."},` +
			`"shreddedAt":{"type":"string","description":"The piiKey.shreddedAt this request was sealed against — the cycle discriminator the residue lens field-diffs the completion seal against."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"requestedAt": "When the erasure request was first sealed. Preserved across a re-seal — the legally meaningful instant is the first one.",
			"shreddedAt":  "The piiKey.shreddedAt in force at seal time. A re-shred bumps shreddedAt, a re-seal copies the new one, and the completion seal's own sealedForShreddedAt then differs — which is how a re-triggered erasure reopens without tombstoning anything.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "erasureRequested marker",
				Payload:         map[string]any{"requestedAt": "2026-08-07T00:00:00Z", "shreddedAt": "2026-08-07T00:00:00Z"},
				ExpectedOutcome: "Written by SealIdentityForErasure on vtx.identity.<NanoID>. From this commit on, the identity write-path gates refuse every op that would create a fresh erasable representation of this person.",
			},
		},
	}
}

// erasureRequestedAspectDDLScript is the declaration-only Starlark for the
// erasureRequested aspect-type DDL. An aspect-type DDL declares shape and
// anchoring, not an operation handler — mirrors piiKeyDDLScript.
const erasureRequestedAspectDDLScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`

// SealIdentityForErasureDDL returns the DDL meta-vertex declaration for the
// SealIdentityForErasure op — step 2 of the identityErasure Loom pattern and
// the increment that makes the erasure's convergence guarantee mean anything
// (erasure-orchestration-design.md §6).
//
// SealIdentityForErasure{subjectKey} writes exactly one aspect —
// vtx.identity.<NanoID>.erasureRequested = {requestedAt, shreddedAt} — and
// emits privacy.erasureRequested. One mutation, always, for every identity:
// nothing here enumerates, so nothing here can refuse a person for being too
// well connected.
//
// # Why the write path has to close before residue can be counted
//
// Erasure is detected, not asserted: a residue lens counts an identity's
// remaining credential and index links, and an attestation is written only
// when that count reaches zero. A detector over an OPEN set proves nothing.
// Today the set is open — identity-domain's index_vertex_mutation
// deliberately revives a tombstoned identityindex on a CAS-guarded update,
// and its comment names the shred as the reason it must — so a contact write
// arriving after an erasure silently reopens the index the erasure just
// tombstoned. With this marker in place and the five gates reading it, no
// writer creates a fresh boundTo, credentialindex or identityindex for a
// sealed identity, the residue set is monotonically non-increasing, and
// "zero" means erased rather than "zero at projection time".
//
// # Payload field is subjectKey, not identityKey
//
// Every op the Loom engine dispatches as a `systemOp` step receives the
// payload {"subjectKey": <the instance's subject>} — the engine builds it and
// a pattern cannot reshape it (`internal/loom/engine.go`). The Weaver's
// directOp params are author-declared rather than engine-fixed, but §7.2's gap
// actions declare the same field. So the erasure step ops speak subjectKey
// while ShredIdentityKey, which predates the pattern, speaks identityKey. That
// divergence is closed on the shred's side: it accepts either name for the same
// subject and refuses a disagreeing pair, so a step submitting subjectKey and an
// operator submitting identityKey reach the same op (shred_identity_key.go).
//
// # Fail-closed on an unshredded identity
//
// The seal copies piiKey.shreddedAt as the cycle discriminator: a re-shred
// bumps shreddedAt, and the residue lens reopens the completion gap by
// field-diffing the completion seal's sealedForShreddedAt against it. Sealing
// an identity whose key was never shredded would copy a null discriminator,
// and null-versus-null then reads as "already sealed" — an erasure that
// attests completion it never earned. So the op refuses: absent envelope, or
// shredded not true, is ErasureNotShredded. RecordShredFinalization refuses on
// the same precondition (a finalization can only follow a shred), though for a
// different mechanical reason — it declares piiKey as a required read to get
// OCC conditioning on the envelope it writes, whereas this op only reads it.
//
// That read is the one soft edge here, stated plainly: piiKey is a class-(d)
// optionalReads served from the step-4 snapshot, and this op does not mutate
// it, so nothing conditions the commit on the envelope still being what was
// read. A shred committing concurrently with a seal can therefore persist the
// PREVIOUS cycle's shreddedAt into the marker. The window is narrow (the
// pattern runs these sequentially) and self-announcing (a re-shred clears the
// finalization booleans, so the residue row reopens its two async-half gaps —
// each raised as a Weaver Health issue by the identityErasureComplete target —
// rather than attesting), and there is no in-script fix — a script cannot condition on a
// key it does not mutate.
//
// # Fail-closed on a merged-away identity
//
// identity-hygiene's MergeIdentity does NOT tombstone the secondary: it sets
// .state = merged and writes .mergedInto, leaving the vertex alive. It has
// also already tombstoned that identity's inbound boundTo edges and repointed
// its identityindex edges onto the survivor. So a residue lens anchored on a
// merged-away identity counts ZERO on its first projection while every
// credential and index representing that person lives on un-erased under the
// survivor — a seal over a silent failure, reached by the ordinary sequence
// "merge, then request erasure naming the pre-merge identity". The op refuses
// with IdentityMerged and names the survivor, which is the key the request
// should have carried. §6's gate table covers only the reverse ordering
// (MergeIdentity refusing an already-sealed identity); this is the other half,
// and it belongs on the op that fixes the anchor.
//
// # Re-seal semantics
//
// The write is an unconditioned update, so the op is idempotent when it is
// submitted. requestedAt is PRESERVED from any existing marker — the first
// request is the legally meaningful instant — while shreddedAt is always
// refreshed from the current envelope, which is what lets a re-triggered
// erasure after a re-shred reopen the completion gap without anything being
// tombstoned.
//
// The marker's own shreddedAt is provenance, not the completeness test, and the
// distinction is what makes the pattern's step-2 guard safe. That step is
// guarded on {"absent": "subject.erasureRequested.data.requestedAt"}, so a
// second erasure for the same person skips it and the marker keeps naming the
// cycle the REQUEST was sealed against. Nothing authoritative reads it there:
// the residue lens field-diffs the LIVE envelope
// (erasure.sealedForShreddedAt <> piiKey.shreddedAt, lenses.go), which cannot go
// stale, and SealIdentityForErasureComplete reads shreddedAt from the envelope
// too. A marker-versus-marker completeness test would have made the guard a
// correctness hazard; the lens deliberately does not use one.
//
// Reads: subjectKey in ContextHint.Reads — the target-existence guard, which
// covers the TOMBSTONED arm. An identity key naming nothing at all never
// reaches the script's own check: a declared read that is absent is recorded
// required-absent at step 4 and any state lookup of it faults HydrationMiss,
// so the caller sees a hydration failure rather than the script's NotFound.
// Both are rejections and neither writes, but they are different errors and
// this is the one that actually fires for an absent key.
//
// subjectKey + ".piiKey", + ".erasureRequested" and + ".mergedInto" in
// ContextHint.OptionalReads — read-posture class (d), absence-tolerant: a
// never-sealed identity has no marker, an unmerged one has no mergedInto, and
// each absence is an ordinary case the script decides on rather than a fault.
func SealIdentityForErasureDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     sealIdentityForErasureDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"SealIdentityForErasure"},
		Description: "Erasure write-path closure (erasure-orchestration-design.md §6). " +
			"SealIdentityForErasure{subjectKey} writes vtx.identity.<NanoID>.erasureRequested = " +
			"{requestedAt, shreddedAt} as a single unconditioned update and emits " +
			"privacy.erasureRequested. Exactly one mutation for every identity regardless of " +
			"connectivity — this op cannot refuse a person. The marker is what the identity " +
			"write-path gates read to reject further writes, which is what makes the erased set " +
			"monotonically non-increasing and so makes a residue count provable rather than " +
			"point-in-time. Requires subjectKey in ContextHint.Reads: a tombstoned identity is " +
			"rejected by the script's own guard, an entirely absent one faults HydrationMiss before " +
			"the script runs. Rejects a merged-away identity (IdentityMerged, naming the survivor) — " +
			"its credentials and indexes already moved, so its residue is zero by construction and a " +
			"seal there would attest an erasure that erased nothing. Requires the identity's piiKey " +
			"to be present and shredded (ErasureNotShredded) because the seal copies shreddedAt as " +
			"the cycle discriminator the completion seal is field-diffed against. Idempotent: a re-seal " +
			"preserves requestedAt and refreshes shreddedAt.",
		Script: sealIdentityForErasureDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"subjectKey":{"type":"string","description":"vtx.identity.<NanoID> — the identity being sealed for erasure. Named subjectKey because Loom's systemOp step and Weaver's directOp both dispatch on that field."}},` +
			`"required":["subjectKey"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.identity.<NanoID> of the sealed identity."}}}`,
		FieldDescription: map[string]string{
			"subjectKey": "Full vtx.identity.<NanoID> key of the identity being sealed. Must exist, not be tombstoned, not be merged away, and carry a shredded piiKey envelope. Declared in ContextHint.Reads; the piiKey, erasureRequested and mergedInto aspects are declared in ContextHint.OptionalReads.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "SealIdentityForErasure — close the write path after the shred",
				Payload: map[string]any{"subjectKey": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Writes vtx.identity.<NanoID>.erasureRequested = {requestedAt, shreddedAt} and emits " +
					"privacy.erasureRequested. From this commit on, ClaimIdentity, CompleteCredentialLink, " +
					"ReconcileCredentialBinding, CreateUnclaimedIdentity's dedup match and MergeIdentity all refuse " +
					"this identity, so no new credential, index or duplicate-correlation representation of them can " +
					"be created while the erasure converges.",
			},
			{
				Name:            "SealIdentityForErasure — identity whose key was never shredded",
				Payload:         map[string]any{"subjectKey": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Rejected with ErasureNotShredded. The seal's shreddedAt discriminator would be null, and a null-versus-null field-diff reads as sealed — an erasure attesting completion it never earned.",
			},
		},
	}
}

// sealIdentityForErasureDDLScript handles SealIdentityForErasure. Starlark has
// no load(), so it carries its own copies of the required_string /
// vertex_alive / parts_of helpers, exactly as shredIdentityKeyDDLScript does.
const sealIdentityForErasureDDLScript = `
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

def live_data(doc):
    if doc == None or doc.isDeleted:
        return None
    if doc.data == None:
        return {}
    return dict(doc.data)

def any_data(doc):
    # live_data's tombstone-blind sibling. Used only where a tombstoned
    # document's BODY is still the truth we want -- a tombstone preserves the
    # stored document whole (Contract #3 §3.3), so a marker that was somehow
    # removed still carries the original request instant, and reviving it must
    # not silently restamp that instant to now.
    if doc == None:
        return None
    if doc.data == None:
        return {}
    return dict(doc.data)

def read_aspect_value(state, key):
    if key not in state:
        return None
    doc = state[key]
    if doc == None:
        return None
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return None
    if doc.data != None and "value" in doc.data:
        return doc.data["value"]
    return None

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "SealIdentityForErasure":
        identity_key = required_string(p, "subjectKey")
        parts_of(identity_key, "subjectKey", "identity")

        if not vertex_alive(state, identity_key):
            fail("NotFound: subjectKey " + identity_key + " is absent or tombstoned")

        # A merged-away identity keeps a LIVE vertex (MergeIdentity writes
        # .state=merged and .mergedInto rather than tombstoning), but its
        # credentials and indexes already moved to the survivor. Sealing it
        # would anchor the residue count on a vertex whose residue is zero by
        # construction, and attest an erasure that erased nothing. Name the
        # survivor: that is the key the request should have carried.
        # read-posture: (d) declared in contextHint.optionalReads -- absent for
        # every unmerged identity, which is the ordinary case.
        merged_into = read_aspect_value(state, identity_key + ".mergedInto")
        if merged_into != None:
            fail("IdentityMerged: " + identity_key + " was merged into " + merged_into + "; request the erasure against the surviving identity, whose credentials and indexes this one's were folded into")

        # read-posture: (d) declared in contextHint.optionalReads by every
        # dispatcher (the identityErasure pattern's step, and any operator
        # submit). Absence-tolerant: an identity that never received a
        # sensitive write still has an envelope after its shred --
        # ShredIdentityKey always writes one, placeholder or not -- so absence
        # here means no shred has happened, which is the ErasureNotShredded
        # refusal below rather than a hydration fault.
        envelope = live_data(kv.Read(identity_key + ".piiKey"))
        if envelope == None or envelope.get("shredded") != True:
            fail("ErasureNotShredded: " + identity_key + " has no shredded piiKey envelope; seal the erasure only after ShredIdentityKey has committed")

        shredded_at = envelope.get("shreddedAt")
        if shredded_at == None or type(shredded_at) != type("") or len(shredded_at) == 0:
            fail("ErasureNotShredded: " + identity_key + " has a shredded piiKey envelope with no shreddedAt stamp, so the seal has no cycle discriminator to record; re-run ShredIdentityKey to restamp it (a shred predating the finalization-cycle change wrote no stamp, and re-shredding is idempotent)")

        marker_key = identity_key + ".erasureRequested"

        # read-posture: (d) same class, same declaration site. The FIRST
        # request instant is the one that matters legally, so a re-seal
        # preserves it and refreshes only the cycle discriminator. Read
        # tombstone-blind on purpose: nothing removes this marker today, but if
        # something ever did, reviving it must not restamp the request instant.
        existing = any_data(kv.Read(marker_key))
        requested_at = op.submittedAt
        if existing != None:
            prior = existing.get("requestedAt")
            if prior != None and type(prior) == type("") and len(prior) > 0:
                requested_at = prior

        mutations = [{"op": "update", "key": marker_key,
            "document": {"class": "erasureRequested", "vertexKey": identity_key,
                         "localName": "erasureRequested", "isDeleted": False,
                         "data": {"requestedAt": requested_at, "shreddedAt": shredded_at}}}]

        events = [{"class": "privacy.erasureRequested",
                   "data": {"identityKey": identity_key, "requestedAt": requested_at, "shreddedAt": shredded_at}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": identity_key}}

    fail("sealIdentityForErasure DDL: unknown operationType: " + ot)
`

// ErasureRequestedEventDDL returns the event-type DDL declaration for
// privacy.erasureRequested (Contract #3 §3.4 typed-event model). Registered
// rather than left unregistered, for the same reason privacy.keyShredded is:
// it documents the schema the erasure plane's downstream readers bind to, and
// this event is the one that says a person's write path has closed.
func ErasureRequestedEventDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName: erasureRequestedEventDDL,
		Class:         "meta.ddl.eventType",
		Description: "Emitted by SealIdentityForErasure (erasure-orchestration-design.md §6) once " +
			"vtx.identity.<NanoID>.erasureRequested is durably written — the instant the identity's " +
			"write path closes and its residue set becomes monotonically non-increasing. Carries the " +
			"cycle discriminator (shreddedAt) so a consumer can tell a re-triggered erasure from the " +
			"original one. It is also what lets the Loom step that submits this op advance on its own " +
			"domain event rather than riding a step deadline.",
		Script: erasureRequestedEventDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"identityKey":{"type":"string","description":"vtx.identity.<NanoID> — the identity sealed for erasure."},` +
			`"requestedAt":{"type":"string","description":"When the erasure request was first sealed."},` +
			`"shreddedAt":{"type":"string","description":"The piiKey.shreddedAt this seal recorded — the erasure cycle discriminator."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"identityKey": "The identity whose erasure was sealed.",
			"requestedAt": "When the erasure request was first sealed (preserved across a re-seal).",
			"shreddedAt":  "The piiKey.shreddedAt in force at seal time; a re-shred changes it and reopens the erasure cycle.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "privacy.erasureRequested",
				Payload:         map[string]any{"identityKey": "vtx.identity.<NanoID>", "requestedAt": "2026-08-07T00:00:00Z", "shreddedAt": "2026-08-07T00:00:00Z"},
				ExpectedOutcome: "Advances the identityErasure pattern's seal step on the privacy completion domain; the write-path gates are already in force from the commit that emitted it.",
			},
		},
	}
}

// erasureRequestedEventDDLScript is the declaration-only Starlark for the
// erasureRequested event-type DDL. Events are emitted by a script's `events`
// return list, never dispatched as operations — mirrors
// keyShreddedEventDDLScript's fail-closed stub.
const erasureRequestedEventDDLScript = `
def execute(state, op):
    fail("event-type DDL: not an operation handler: " + op.operationType)
`
