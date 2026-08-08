package privacybase

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// purgeIdentityDedupFootprintDDL is the canonical name of the vertexType DDL
// owning the PurgeIdentityDedupFootprint op (erasure-orchestration-design.md
// §5.4, step 4 of the identityErasure pattern).
const purgeIdentityDedupFootprintDDL = "purgeIdentityDedupFootprint"

// dedupFootprintSweptEventDDL is the canonical name of the eventType DDL for
// the sweep's emitted event.
const dedupFootprintSweptEventDDL = "privacy.dedupFootprintSwept"

// PurgeIdentityDedupFootprintDDL declares the dedup-plane sweep of an erasure.
//
// It is the sibling of identity-domain's UnbindIdentityCredentials: ShredIdentityKey
// writes exactly one mutation and never touches the dedup plane, so its cost
// cannot grow with the subject's connectivity (design §4.1/§10). This op is
// the dedup-plane sweep a person's erasure still needs — one bounded page per
// commit, re-dispatched until the residue reaches zero.
//
// # What it removes
//
// Two classes of durable, decrypt-free plaintext derivative:
//
//   - The subject's owned `vtx.identityindex.<hash>` vertices, reached through
//     their inbound `indexes` links. Each is a hash of a normalized contact
//     value, and the link naming both parties is the ownership evidence — no
//     plaintext lookup is needed to find them, and none is needed to erase them.
//   - Every `duplicateOf` link touching the subject, in BOTH directions. The
//     subject may be either side of a pair: the later-arriving identity that
//     matched an incumbent (source), or the incumbent that later identities
//     matched against (target). Sweeping one direction would leave live pair
//     evidence naming an erased person, which is the class this op exists to
//     remove.
//
// One class per commit, in that order — indexes, then duplicateOf outbound,
// then duplicateOf inbound. The classes do not cost the same: an indexes hit is
// TWO mutations (the index vertex and the link) where a duplicateOf hit is one,
// so draining all three collectors in a single commit would reach 4*SWEEP_LIMIT
// mutations. Taking one at a time caps any commit at 2*SWEEP_LIMIT, the
// magnitude UnbindIdentityCredentials measured clean against the Starlark wall
// budget.
//
// # What its event is for, and why it is unconditional
//
// It emits privacy.dedupFootprintSwept on every pass, including one that finds
// nothing left. No read model needs it — an identityindex vertex is read by the
// index probe, which reads Core KV state directly and therefore sees the
// tombstone, so unlike the credential plane's identity.unbound (Contract #11
// §11.4) there is no copy to retract. It is the dedup plane's AUDIT record: the
// per-pass account of what an erasure removed, which is the only such record
// this plane has and is what §7.4's attestation reader wants.
//
// Unconditional because it is also what advances the identityErasure pattern's
// LAST step. A step whose event is conditional on having found work cannot
// advance the instance for a subject whose dedup footprint was always empty,
// and a step that emits nothing at all rides its 60s deadline into a probe that
// advances correctly while logging "check completionDomains" against a pattern
// that declared them correctly. relation is empty and purged is zero on the
// pass that finds nothing — that pass IS the convergence signal, not a
// non-event.
//
// # What it does not do
//
// It does not tombstone the subject's own identity vertex or any aspect of it.
// The dedup footprint is what the subject's contact details left OUTSIDE the
// subject; what is on the subject is already erased by key destruction.
//
// # Why it refuses an unsealed subject
//
// Identical to UnbindIdentityCredentials'. The grant is scope:any to operator,
// which is how a service actor is reached at all, and without a precondition
// that is a bare "delete anyone's dedup index vertices" verb — a live one would
// silently break the dedup hygiene of a person nobody is erasing. Requiring the
// erasureRequested marker means the op confers no authority a completed seal has
// not already exercised, and costs the pattern nothing, since §5.1 orders the
// seal at step 2 and this op at step 4. The marker's CLASS is checked, not
// merely its key, so a foreign write at that key cannot arm this verb.
func PurgeIdentityDedupFootprintDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     purgeIdentityDedupFootprintDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"PurgeIdentityDedupFootprint"},
		Description: "Erasure dedup-plane sweep (erasure-orchestration-design.md §5.4). " +
			"PurgeIdentityDedupFootprint{subjectKey} tombstones one bounded page of the subject's " +
			"dedup-hygiene footprint: its owned identityindex vertices with their inbound indexes " +
			"links, and its duplicateOf pair links in both directions. One class per commit — indexes " +
			"first, then duplicateOf outbound, then inbound — because an indexes hit costs two " +
			"mutations to a duplicateOf hit's one, so no commit exceeds 2*SWEEP_LIMIT mutations — a " +
			"count no input can grow, which is what makes an erasure's mutation cost independent of how " +
			"connected the subject is. The READ side is not bounded the same way: a sweep always " +
			"scans from the start of the keyspace, so each pass pages over everything it has already " +
			"tombstoned, and both the cost per pass and the scan window are finite. Past roughly 16k " +
			"links on one relation the live ones fall outside the window and the op stops with " +
			"ErasureResidueUnreachable — a loud stop chosen over the silent stall that returning " +
			"empty would produce under an uncapped re-dispatch. " +
			"Idempotent: already-tombstoned rows are skipped, so a re-run " +
			"over a fully swept subject is a no-op with no mutations. Emits " +
			"privacy.dedupFootprintSwept on every pass including that one — no read model needs it " +
			"(the index probe reads Core KV and sees the tombstone), it is the dedup plane's audit " +
			"record of what each pass removed, and it is what advances the identityErasure pattern's " +
			"last step without riding a deadline. Requires the subject to carry a live erasureRequested marker of " +
			"that class (ErasureNotSealed) — this is machinery for an erasure already sealed, not a " +
			"way to break a person's dedup hygiene.",
		Script: purgeIdentityDedupFootprintDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"subjectKey":{"type":"string","description":"vtx.identity.<NanoID> — the identity whose dedup footprint is being swept. Named subjectKey because Loom's systemOp step and Weaver's directOp both dispatch on that field."}},` +
			`"required":["subjectKey"]}`,
		// Deliberately empty, for the same reason UnbindIdentityCredentials'
		// is: the reply-constraint requires a script-named primaryKey to lie
		// inside the operation's write footprint (internal/processor/commit_path.go),
		// and every key this op writes belongs to an identityindex vertex or a
		// link — never to the subject. Naming the subject would be the write
		// path used as a read channel. Nothing needs one: Loom correlates a
		// systemOp on the requestId its emitted event carries and Weaver on the
		// dispatch, and the observable outcome is the shrinking residue.
		OutputSchema: `{"type":"object","properties":{}}`,
		FieldDescription: map[string]string{
			"subjectKey": "Full vtx.identity.<NanoID> key of the identity being swept. Must exist, not be tombstoned, and carry a live erasureRequested marker. Declared in ContextHint.Reads; the erasureRequested marker is read live and the indexes/duplicateOf walks are declared as ContextHint.Enumerations.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "PurgeIdentityDedupFootprint — sweep one page of a sealed identity's dedup footprint",
				Payload: map[string]any{"subjectKey": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Tombstones up to 64 owned identityindex vertices with their indexes links, or — once those " +
					"are exhausted — up to 64 duplicateOf links in one direction, and emits " +
					"privacy.dedupFootprintSwept naming the relation and the count. A subject with more " +
					"footprint than one page keeps a non-empty residue, which the erasure target " +
					"re-dispatches this op against until it reaches zero.",
			},
			{
				Name:    "PurgeIdentityDedupFootprint — sealed identity whose footprint is already gone",
				Payload: map[string]any{"subjectKey": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Commits with no mutations and still emits privacy.dedupFootprintSwept with an empty relation " +
					"and purged=0 — the convergence signal, and what advances the identityErasure pattern's last step for " +
					"a subject that never had a dedup footprint at all.",
			},
			{
				Name:            "PurgeIdentityDedupFootprint — identity that was never sealed for erasure",
				Payload:         map[string]any{"subjectKey": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Rejected with ErasureNotSealed. This op destroys a person's dedup-hygiene index entries, which is only ever correct for an erasure already sealed.",
			},
		},
	}
}

// purgeIdentityDedupFootprintDDLScript is the Starlark handler.
//
// Like UnbindIdentityCredentials it pages on the READ side while capping the
// MUTATION side, because a tombstone is a soft delete: kv.Links keeps returning
// swept links with isDeleted set, so a cursor-less single call finds a first
// page of pure tombstones once one sweep's worth has accumulated and stalls
// there at a non-zero residue, silently, while the erasure target re-dispatches
// a no-op. The cursor lives in the keyspace, which retains what the world has
// discarded. Design §10 point 4 states the property this holds.
//
// Live-read cost: each collector charges its page limit per page (not its
// yield), so a subject wide enough to exhaust all three page budgets charges
// 3 * MAX_LINK_PAGES * LINK_PAGE_LIMIT ~= 49k units against the Processor's
// 60k default. That is the same magnitude as MergeIdentity, the other claimant
// the budget is sized for — real headroom, but not much, so raising
// SWEEP_LIMIT, either page constant, or the number of relations swept needs
// the budget checked rather than assumed.
//
// The tombstones carry no document. buildMutationValue seeds a tombstone's
// document from the PRIOR body and layers the script's over it
// (internal/processor/step8_commit.go), so the bare verb preserves class and
// data and sets isDeleted — with no per-candidate kv.Read needed to carry that
// body forward by hand. Skipping those reads is real headroom against the
// same wall budget that forced SWEEP_LIMIT below the read page size.
const purgeIdentityDedupFootprintDDLScript = `
LINK_PAGE_LIMIT = 256
MAX_LINK_PAGES = 64
SWEEP_LIMIT = 64

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

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

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def marker_closes_write_path(doc):
    # The CLASS is checked, not merely the key -- privacy-base's aspect-type
    # DDL gates the class rather than the key, so a mutation at this key
    # declaring some OTHER class falls to resolveGoverningDDL's permissive
    # default and any package script could write one. Requiring the real class
    # means only the real seal can arm this verb, and a tombstoned marker still
    # counts: nothing removes it, and a gate that reopened on a tombstone would
    # be the one failure mode a fail-closed guard may not have.
    if doc == None:
        return False
    if not hasattr(doc, "class") or getattr(doc, "class") != "erasureRequested":
        return False
    return True

def collect_live_sweep(identity_key, relation, direction):
    # Pages until SWEEP_LIMIT LIVE links are in hand, the cursor runs out, or
    # the page budget is spent. The page loop is on the READ; the returned
    # slice is capped at SWEEP_LIMIT and cannot be grown by the subject's
    # connectivity. See the const comment above for why a cursor-less single
    # call does not work against a soft-delete substrate.
    hits = []
    cursor = None
    for _page in range(MAX_LINK_PAGES):
        # read-posture: (e) relation=indexes epoch=none; relation=duplicateOf
        # epoch=none -- one collector serves both walks, so both relations the
        # dispatcher declares in contextHint.enumerations are named. Read-only guard:
        # a link created concurrently with the sweep slips past -- accepted, and
        # harmless here because the erasure seal closed the write path before
        # this op ever runs.
        links, cursor = kv.Links(identity_key, relation, direction, cursor, LINK_PAGE_LIMIT)
        for lk in links:
            if lk.isDeleted:
                continue
            hits.append(lk)
            if len(hits) >= SWEEP_LIMIT:
                return hits
        if cursor == None:
            return hits
    if len(hits) == 0:
        # The page budget was spent on nothing but tombstones and more pages
        # remain, so live links may sit beyond the window and this op cannot
        # reach them. Returning empty would read as "converged" and leave the
        # erasure target re-dispatching a no-op forever -- an invisible stall,
        # strictly worse than a loud stop.
        #
        # This is a real ceiling, and the sweep builds its own way into it. The
        # enumeration is a stable ascending scan over the keyspace, so each pass
        # drains the lexicographically-earliest live links and the tombstone
        # prefix grows by SWEEP_LIMIT every time. A subject with more than
        # MAX_LINK_PAGES * LINK_PAGE_LIMIT links on one relation therefore
        # converges normally until that prefix fills the window, then stops here
        # for good -- no pre-existing tombstones required. So "no input can
        # decline an erasure" is a statement about the MUTATION count alone: the
        # per-commit cost is bounded whatever the subject's connectivity, while
        # the READ window is what can still refuse a very wide subject, loudly
        # and at a far higher threshold. The durable fix is a hard delete: a
        # tombstone that leaves the keyspace takes this ceiling with it, and the
        # per-pass rescan cost too.
        fail("ErasureResidueUnreachable: " + identity_key + " has more than " +
             str(MAX_LINK_PAGES * LINK_PAGE_LIMIT) + " tombstoned " + relation + " links (" +
             direction + ") ahead of its live ones; the sweep cannot page far enough to reach them")
    return hits

def sweep_indexes(hits):
    # Each link's SOURCE is meant to be one of the subject's owned identityindex
    # vertices (Contract #1 §1.1: the identityindex vertex is the source, the
    # identity the target). Both the vertex and the link go: the vertex IS the
    # hash of a contact value, and the link is the evidence tying it to this
    # person.
    #
    # No document on either tombstone -- the commit path seeds it from the prior
    # body, so class and data survive without a read per hit.
    #
    # THE SOURCE'S KEY SHAPE IS CHECKED, and it is the only thing standing
    # between this op and an arbitrary-vertex delete primitive. Three facts
    # compose: the enumeration's server filter is lnk.*.*.indexes.identity.<id>,
    # so the source TYPE is a wildcard; sourceVertex is derived faithfully from
    # whatever the key says; and a document-less tombstone carries no class, so
    # step 6 skips DDL resolution and never consults permittedCommands on the
    # key being destroyed. Nothing else on the path constrains what gets
    # tombstoned -- the indexes linkType ships permittedCommands empty by
    # design (multi-writer, open posture), so any writer able to create a link
    # could name a victim's identity root as the source and have this op
    # destroy it. No shipped op creates a non-identityindex-sourced indexes
    # link, but that is a property of the current corpus, not an invariant the
    # platform enforces, and this op holds a scope:any grant.
    #
    # A foreign source is skipped rather than fatal, and the LINK still goes:
    # the link is genuinely the subject's inbound edge and removing it is what
    # shrinks the residue, so convergence is unaffected. Refusing the whole
    # sweep instead would let one planted link make a person unerasable --
    # trading a destructive failure for a fail-open one.
    mutations = []
    for lk in hits:
        if lk.sourceVertex.startswith("vtx.identityindex."):
            mutations.append({"op": "tombstone", "key": lk.sourceVertex})
        mutations.append({"op": "tombstone", "key": lk.key})
    return mutations

def sweep_duplicate_of(hits):
    # Only the link goes. Its endpoints are two identity vertices, and the OTHER
    # one belongs to a person nobody is erasing.
    mutations = []
    for lk in hits:
        mutations.append({"op": "tombstone", "key": lk.key})
    return mutations

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "PurgeIdentityDedupFootprint":
        subject_key = required_string(p, "subjectKey")
        parts_of(subject_key, "subjectKey", "identity")

        if not vertex_alive(state, subject_key):
            fail("NotFound: subjectKey " + subject_key + " is absent or tombstoned")

        # read-posture: (d) declared in contextHint.optionalReads by every
        # dispatcher (the identityErasure pattern's step, the erasure target's
        # directOp). Absence-tolerant, and absence is the refusal below rather
        # than a hydration fault: no identity carries this marker until it is
        # sealed for erasure, which is the ordinary case for every identity in
        # the corpus.
        if not marker_closes_write_path(kv.Read(subject_key + ".erasureRequested")):
            fail("ErasureNotSealed: " + subject_key + " carries no erasureRequested marker; this op destroys a person's dedup-hygiene index entries, which is only correct for an erasure already sealed")

        # One class per commit, in cost order. An indexes hit is two mutations
        # and a duplicateOf hit is one, so draining all three collectors here
        # would reach 4*SWEEP_LIMIT and put this op back in reach of the wall
        # budget that sized SWEEP_LIMIT in the first place. Whatever this commit
        # leaves is residue the erasure target re-dispatches against.
        relation = ""
        hits = collect_live_sweep(subject_key, "indexes", "in")
        if len(hits) > 0:
            relation = "indexes"
            mutations = sweep_indexes(hits)
        else:
            hits = collect_live_sweep(subject_key, "duplicateOf", "out")
            if len(hits) == 0:
                hits = collect_live_sweep(subject_key, "duplicateOf", "in")
            if len(hits) > 0:
                relation = "duplicateOf"
            mutations = sweep_duplicate_of(hits)

        # UNCONDITIONAL, including the pass that finds nothing: this event is
        # what advances the identityErasure pattern's last step, and a subject
        # whose dedup footprint was always empty would otherwise never emit one.
        # An empty relation with purged=0 says the sweep found nothing left,
        # which is the convergence signal, not the absence of one.
        # targetKey is carried explicitly, redundant with identityKey, because
        # step 7 otherwise derives an event's target POSITIONALLY from the
        # mutation at the same index -- which here would name whichever
        # identityindex vertex happened to be swept first, and nothing at all on
        # the pass that sweeps none. An audit record of a person's erasure
        # should name the person on every pass.
        events = [{"class": "privacy.dedupFootprintSwept",
                   "data": {"identityKey": subject_key, "targetKey": subject_key,
                            "relation": relation,
                            "purged": len(hits), "mutations": len(mutations)}}]

        # No primaryKey: nothing this op writes belongs to the subject, and the
        # reply-constraint rejects a primaryKey outside the write footprint.
        return {"mutations": mutations, "events": events, "response": {}}

    fail("purgeIdentityDedupFootprint DDL: unknown operationType: " + ot)
`

// DedupFootprintSweptEventDDL returns the event-type DDL declaration for
// privacy.dedupFootprintSwept (Contract #3 §3.4 typed-event model). Registered
// for the same reason privacy.keyShredded and privacy.erasureRequested are: it
// documents the schema an auditor's reader binds to, and on this plane it is
// the ONLY per-pass record of what an erasure removed — there is no read-model
// copy whose shrinkage could stand in for one.
func DedupFootprintSweptEventDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName: dedupFootprintSweptEventDDL,
		Class:         "meta.ddl.eventType",
		Description: "Emitted by PurgeIdentityDedupFootprint (erasure-orchestration-design.md §5.4) on " +
			"every commit, naming the relation that pass swept and how much of it went. Emitted on the " +
			"pass that finds nothing too, with an empty relation and purged=0: that pass is the dedup " +
			"plane's convergence signal, and it is what advances the identityErasure pattern's last step " +
			"for a subject whose footprint was already gone. A consumer counting erased footprint sums " +
			"purged across an identity's events; a consumer watching for convergence waits for one with " +
			"purged=0.",
		Script: dedupFootprintSweptEventDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"identityKey":{"type":"string","description":"vtx.identity.<NanoID> — the identity whose dedup footprint was swept."},` +
			`"targetKey":{"type":"string","description":"Same value as identityKey, carried so the event's step-7 target names the person rather than whichever index vertex was swept first."},` +
			`"relation":{"type":"string","description":"The relation this pass swept: indexes, duplicateOf, or empty when nothing live remained."},` +
			`"purged":{"type":"integer","description":"Live links this pass removed. Zero means the sweep found nothing left."},` +
			`"mutations":{"type":"integer","description":"Tombstones this pass committed — normally two per indexes hit (the index vertex and the link), one when a foreign-sourced link spares the vertex, one per duplicateOf hit."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"identityKey": "The identity whose dedup footprint was swept.",
			"targetKey":   "Same as identityKey; carried so the event's target names the person on every pass.",
			"relation":    "Which relation this pass swept, or empty when no live link remained on any of them.",
			"purged":      "Live links removed by this pass; zero is the convergence signal.",
			"mutations":   "Tombstones committed. An indexes hit normally costs two (the index vertex and the link) but one when the link's source lies outside the identityindex keyspace and the vertex is spared; a duplicateOf hit always costs one.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "privacy.dedupFootprintSwept — a page of index entries went",
				Payload:         map[string]any{"identityKey": "vtx.identity.<NanoID>", "targetKey": "vtx.identity.<NanoID>", "relation": "indexes", "purged": 64, "mutations": 128},
				ExpectedOutcome: "Advances the identityErasure pattern's fourth step on the privacy completion domain, and records for an auditor that 64 of this person's index entries were destroyed.",
			},
			{
				Name:            "privacy.dedupFootprintSwept — nothing left to sweep",
				Payload:         map[string]any{"identityKey": "vtx.identity.<NanoID>", "targetKey": "vtx.identity.<NanoID>", "relation": "", "purged": 0, "mutations": 0},
				ExpectedOutcome: "The dedup plane has converged. Advances the pattern's last step for a subject that never had a footprint, which is the case a work-conditional event could not advance at all.",
			},
		},
	}
}

// dedupFootprintSweptEventDDLScript is the declaration-only Starlark for the
// dedupFootprintSwept event-type DDL. Events are emitted by a script's `events`
// return list, never dispatched as operations — mirrors
// erasureRequestedEventDDLScript's fail-closed stub.
const dedupFootprintSweptEventDDLScript = `
def execute(state, op):
    fail("event-type DDL: not an operation handler: " + op.operationType)
`
