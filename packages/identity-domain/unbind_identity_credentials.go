package identitydomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// unbindIdentityCredentialsDDL is the canonical name of the vertexType DDL
// owning the UnbindIdentityCredentials op (erasure-orchestration-design.md
// §5.4, step 3 of the identityErasure pattern).
const unbindIdentityCredentialsDDL = "unbindIdentityCredentials"

// UnbindIdentityCredentialsDDL declares the credential-plane sweep of an
// erasure.
//
// ShredIdentityKey writes exactly one mutation and never touches the
// credential plane, so its cost cannot grow with the subject's connectivity
// (design §4.1/§10); this op is the credential-plane sweep that a person's
// erasure still needs — one bounded page per commit, re-dispatched until the
// residue reaches zero.
//
// # What it removes, and what it deliberately does not
//
// For each live boundTo link on the subject it tombstones the credential's
// `vtx.credentialindex.<hash>` vertex and the link itself, and emits one
// identity.unbound event. Both link directions are swept: the subject is the
// TARGET of every credential bound to it, and is itself the SOURCE when it is
// a credential of someone else (a merged-away identity folded into its
// survivor, or a Scenario-B identity later linked to another) — sweeping only
// the inbound direction would leave that second case permanently unerasable,
// since ShredIdentityKey covers neither.
//
// It does NOT rewrite the SUBJECT's own credentialBinding array, and cannot:
// that aspect is sensitive, so it is decrypted on read and encrypted on write
// under the subject's own DEK, and internal/vault refuses both once the
// envelope is shredded. This op's precondition is the erasure seal, and the
// seal's precondition is a shredded piiKey, so the subject's DEK is always
// dead by the time this runs — touching that aspect would fault in hydrate on
// every redelivery, leaving the person unerasable.
//
// That is not a gap, because the subject's own array is already erased: it is
// ciphertext under a destroyed key. The readable copy of an erased person's
// credential list is the Gateway's credential-bindings bucket, and the
// identity.unbound emission is that plane's only row-set shrink (Contract #11
// §11.4). The array rewrite survives where it is both possible and needed —
// the OWNER of a binding in which the subject is itself the credential, whose
// own key is alive and whose array names the erased person in the clear.
//
// # Why it refuses an unsealed subject
//
// The grant is scope:any to operator (permissions.go), which is how a service
// actor is reached at all. Without a precondition that would be a bare
// "tombstone anyone's sign-in methods" verb. Requiring the erasureRequested
// marker means the op confers no authority a completed seal has not already
// exercised — the same fail-closed justification privacy-base's
// SealIdentityForErasure grant rests on — and costs the pattern nothing,
// since §5.1 orders the seal at step 2 and this op at step 3. The marker's
// CLASS is checked, not merely its key, so a foreign write at that key cannot
// arm this verb.
func UnbindIdentityCredentialsDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     unbindIdentityCredentialsDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"UnbindIdentityCredentials"},
		Description: "Erasure credential-plane sweep (erasure-orchestration-design.md §5.4). " +
			"UnbindIdentityCredentials{subjectKey} tombstones one bounded page of the subject's " +
			"boundTo links and each credential's credentialindex vertex, and emits one " +
			"identity.unbound per credential so the Gateway's credential-bindings bucket drops the " +
			"row, plus one identity.credentialsSwept per commit unconditionally — the pass-level record the " +
			"identityErasure pattern's guardless third step advances on, since a pass that unbinds nothing " +
			"emits no identity.unbound and would otherwise ride a step deadline. " +
			"Both link directions are swept — the subject is the target of every credential " +
			"bound to it and the source when it is itself someone else's credential — inbound " +
			"first, outbound only once inbound is exhausted, so one commit never exceeds " +
			"2*SWEEP_LIMIT+1 mutations and the op can never refuse a well-connected person — not on " +
			"batch size and not on the Starlark wall budget either. Idempotent: " +
			"already-tombstoned links are skipped, so a re-run over a fully swept subject is a " +
			"no-op with no mutations. Requires the subject to carry a live erasureRequested marker " +
			"of that class (ErasureNotSealed) — this is machinery for an erasure already sealed, " +
			"not a way to strip a person's sign-in methods. Does not rewrite the subject's own " +
			"credentialBinding array: that aspect is sensitive and the subject's DEK is shredded " +
			"by the time this runs, so it is already erased by key destruction; the owner's array " +
			"IS rewritten on the outbound direction, where the owner's key is alive.",
		Script: unbindIdentityCredentialsDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"subjectKey":{"type":"string","description":"vtx.identity.<NanoID> — the identity whose credential bindings are being swept. Named subjectKey because Loom's systemOp step and Weaver's directOp both dispatch on that field."}},` +
			`"required":["subjectKey"]}`,
		// Deliberately empty. The reply-constraint requires a script-named
		// primaryKey to lie inside the operation's write footprint
		// (internal/processor/commit_path.go), and every key this op writes
		// belongs to a CREDENTIAL, a link, or another person's aspect — never to
		// the subject. Naming the subject would be the write path used as a read
		// channel; naming one swept credential would pick an arbitrary member of
		// a page. Nothing needs one: Loom correlates a systemOp on its pending
		// token and Weaver on the dispatch, and the observable outcome is the
		// identity.unbound events plus the shrinking residue.
		OutputSchema: `{"type":"object","properties":{}}`,
		FieldDescription: map[string]string{
			"subjectKey": "Full vtx.identity.<NanoID> key of the identity being swept. Must exist, not be tombstoned, and carry a live erasureRequested marker. Declared in ContextHint.Reads; the erasureRequested marker is read live and the boundTo walks are declared as ContextHint.Enumerations.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "UnbindIdentityCredentials — sweep one page of a sealed identity's credentials",
				Payload: map[string]any{"subjectKey": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Tombstones up to 64 boundTo links and their credentialindex vertices, emits one " +
					"identity.unbound per credential. A subject with more " +
					"credentials than one page keeps a non-empty residue, which the erasure target re-dispatches " +
					"this op against until it reaches zero.",
			},
			{
				Name:            "UnbindIdentityCredentials — identity that was never sealed for erasure",
				Payload:         map[string]any{"subjectKey": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Rejected with ErasureNotSealed. This op removes every one of a person's sign-in methods with no last-credential guard, which is only ever correct for an erasure already sealed.",
			},
		},
	}
}

// unbindIdentityCredentialsDDLScript is the Starlark handler.
//
// The enumeration pages on the READ side while capping the MUTATION side at
// one page, and those two facts are not the same bound. Design §10 point 4
// argued a single cursor-less kv.Links call would suffice because "the cursor
// lives in the world (the remaining live links), not in the script" — that is
// false against this substrate. A tombstone is a soft delete: the key stays in
// the keyspace and kv.Links keeps returning it with isDeleted set
// (internal/processor/starlark_kv.go's connLinkLister skips only a HARD
// delete). So after the first sweep the first page is entirely tombstones, and
// a cursor-less single call would find zero live links and stall there
// forever, silently, for any subject with more than one page of credentials —
// while the erasure target re-dispatched it without end. Paging until a page's
// worth of LIVE links is in hand keeps the property §10 was actually reaching
// for (a mutation count that cannot be grown by the subject's connectivity)
// and makes convergence true rather than assumed.
const unbindIdentityCredentialsDDLScript = `
BOUND_TO_PAGE_LIMIT = 256
MAX_BOUND_TO_PAGES = 64
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

def credential_index_key(actor_key):
    return "vtx.credentialindex." + crypto.sha256NanoID(actor_key)

def marker_closes_write_path(doc):
    # The CLASS is checked, not merely the key -- the same guard the identity
    # DDL's write-path gates apply. privacy-base's aspect-type DDL gates the
    # class rather than the key, so a mutation at this key declaring some OTHER
    # class falls to resolveGoverningDDL's permissive default and any package
    # script could write one. Requiring the real class means only the real seal
    # can arm this verb, and a tombstoned marker still counts: nothing removes
    # it, and a gate that reopened on a tombstone would be the one failure mode
    # a fail-closed guard may not have.
    if doc == None:
        return False
    if not hasattr(doc, "class") or getattr(doc, "class") != "erasureRequested":
        return False
    return True

def envelope_is_shredded(doc):
    if doc == None or doc.isDeleted:
        return False
    if doc.data == None:
        return False
    return doc.data.get("shredded") == True

def collect_live_sweep(identity_key, direction):
    # Pages until SWEEP_LIMIT LIVE links are in hand, the cursor runs out, or
    # the page budget is spent. The page loop is on the READ; the returned
    # slice -- and so the mutation count -- is capped at SWEEP_LIMIT and cannot
    # be grown by the subject's connectivity. See the const comment above for
    # why a cursor-less single call does not work here.
    #
    # SWEEP_LIMIT is deliberately NOT the read page limit. Design §5.4 sized
    # the commit at PAGE = 256 "matching the existing enumeration page limits"
    # -- but those bound a READ in an op that enumerates and commits once,
    # whereas here the same number sizes an ATOMIC BATCH, every pass. 2*256
    # mutations measurably exceeds the Processor's 250ms Starlark wall budget
    # on a loaded host, which would let this op refuse a well-connected person
    # by wall clock -- reintroducing through a second door exactly the refusal
    # §10 retires. A quarter of that leaves real headroom and costs only more
    # passes, which the erasure target already dispatches uncapped.
    hits = []
    cursor = None
    for _page in range(MAX_BOUND_TO_PAGES):
        # read-posture: (e) relation=boundTo epoch=none (read-only guard: a
        # boundTo link created concurrently with the sweep slips past --
        # accepted, and harmless here because the erasure seal closed the write
        # path before this op ever runs)
        links, cursor = kv.Links(identity_key, "boundTo", direction, cursor, BOUND_TO_PAGE_LIMIT)
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
        # strictly worse than a loud stop. Reachable only past ~16k tombstoned
        # boundTo links in one direction.
        fail("ErasureResidueUnreachable: " + identity_key + " has more than " +
             str(MAX_BOUND_TO_PAGES * BOUND_TO_PAGE_LIMIT) + " tombstoned " + direction +
             "-bound boundTo links ahead of its live ones; the sweep cannot page far enough to reach them")
    return hits

def sweep_inbound(subject_key, hits):
    # The subject is the OWNER: each link's source is one of its credentials.
    # The subject's own credentialBinding array is NOT rewritten -- it is
    # sensitive, and by the time this op runs the subject's DEK is shredded, so
    # the array is already erased by key destruction and reading it would fault
    # in hydrate. The identity.unbound emission is what shrinks the readable
    # copy, in the Gateway's credential-bindings bucket.
    mutations = []
    events = []
    for lk in hits:
        credential_key = lk.sourceVertex
        mutations.append({"op": "tombstone", "key": credential_index_key(credential_key)})
        mutations.append({"op": "tombstone", "key": lk.key})
        events.append({"class": "identity.unbound", "data": {
            "identityKey": subject_key,
            "actorKey": credential_key,
        }})
    return mutations, events

def sweep_outbound(subject_key, hits):
    # The subject IS the credential: each link's target is an owner whose key is
    # alive and whose credentialBinding array names the subject in the clear.
    # That array is the one this op must rewrite -- UnlinkCredential's body,
    # applied to the owner, minus the last-credential guard (a credential being
    # erased must stop authenticating whether or not it was the owner's last).
    mutations = []
    events = []
    if len(hits) > 0:
        # One key for every outbound link -- the subject is the credential in
        # all of them -- so it is tombstoned once rather than per link.
        mutations.append({"op": "tombstone", "key": credential_index_key(subject_key)})
    for lk in hits:
        owner_key = lk.targetVertex
        mutations.append({"op": "tombstone", "key": lk.key})
        rewrite = owner_binding_rewrite(owner_key, subject_key)
        if rewrite != None:
            mutations.append(rewrite)
        events.append({"class": "identity.unbound", "data": {
            "identityKey": owner_key,
            "actorKey": subject_key,
        }})
    return mutations, events

def owner_binding_rewrite(owner_key, credential_key):
    # read-posture: (e) per-candidate follow-up read off the enumeration above
    # (data-derived key: the owner is not dispatch-known ahead of the boundTo
    # walk). The owner's piiKey is read FIRST: credentialBinding is sensitive,
    # so if the owner has itself been shredded -- two people erased, one the
    # other's credential -- decrypting it would fail and take the whole sweep
    # down. A shredded owner's array is already erased by key destruction, so
    # skipping the rewrite there loses nothing.
    if envelope_is_shredded(kv.Read(owner_key + ".piiKey")):
        return None

    # read-posture: (e) per-candidate follow-up read off the enumeration above
    # (data-derived key, same class as the piiKey probe).
    binding = kv.Read(owner_key + ".credentialBinding")
    if binding == None or binding.isDeleted or binding.data == None:
        return None
    existing = dict(binding.data)
    credentials = existing.get("credentials")
    if credentials == None or type(credentials) != type([]):
        first_actor = existing.get("actorKey")
        if first_actor == None:
            return None
        credentials = [{"actorKey": first_actor, "boundAt": existing.get("boundAt")}]

    remaining = []
    removed = False
    for c in credentials:
        if c.get("actorKey") == credential_key:
            removed = True
            continue
        remaining.append(c)
    if not removed:
        # Nothing of this credential in the owner's array -- the link is being
        # tombstoned regardless, and rewriting an unchanged array would burn a
        # mutation and make a re-run non-idempotent.
        return None

    # The singular actorKey/boundAt pair is the pre-array record readers still
    # fall back to, so it must not keep naming the credential just removed.
    # When nothing remains to promote, both fields are OMITTED rather than set
    # to null: the aspect's schema types actorKey as a string, and the fallback
    # readers test for the field's presence, so a null would satisfy the test
    # and hand them a non-key.
    data = {"credentials": remaining}
    singular_actor = existing.get("actorKey")
    singular_bound = existing.get("boundAt")
    if singular_actor == credential_key:
        singular_actor = None
        singular_bound = None
        if len(remaining) > 0:
            singular_actor = remaining[0]["actorKey"]
            singular_bound = remaining[0]["boundAt"]
    if singular_actor != None:
        data["actorKey"] = singular_actor
    if singular_bound != None:
        data["boundAt"] = singular_bound

    return {"op": "update", "key": owner_key + ".credentialBinding",
            "document": {"class": "credentialBinding", "vertexKey": owner_key,
                         "localName": "credentialBinding", "isDeleted": False,
                         "data": data}}

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "UnbindIdentityCredentials":
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
            fail("ErasureNotSealed: " + subject_key + " carries no erasureRequested marker; this op removes every one of a person's sign-in methods with no last-credential guard, which is only correct for an erasure already sealed")

        # Inbound first, outbound only once inbound is exhausted. Draining both
        # in one commit would reach 4*PAGE+2 mutations and trip step 8's
        # BatchTooLarge -- a refusal, on exactly the well-connected person this
        # decomposition exists to stop refusing. Whatever this commit leaves is
        # residue the erasure target re-dispatches against.
        direction = ""
        hits = collect_live_sweep(subject_key, "in")
        if len(hits) > 0:
            direction = "in"
            mutations, events = sweep_inbound(subject_key, hits)
        else:
            hits = collect_live_sweep(subject_key, "out")
            if len(hits) > 0:
                direction = "out"
            mutations, events = sweep_outbound(subject_key, hits)

        # One identity.credentialsSwept per commit, UNCONDITIONALLY, alongside
        # the per-credential identity.unbound events above. The two answer
        # different questions and only one of them can answer this one: an
        # unbound event says a named credential stopped authenticating and is
        # what shrinks the Gateway's readable copy, so it is emitted per hit and
        # a pass with no hits emits none. That is correct for a retraction and
        # useless for a step: the identityErasure pattern's third step is
        # guardless, so it runs for every subject, including one who never
        # bound any credential and so has nothing to sweep at all — the
        # unbound event never fires for that input. A step that emits nothing
        # rides its 60s deadline into the op-status probe and advances while
        # logging "check completionDomains" against a pattern that declared
        # them correctly. The sweep-pass event is what the step advances on;
        # an empty direction with swept=0 is the convergence signal, whether
        # from a subject with no credentials to begin with or one a later
        # re-dispatch finds fully swept.
        events.append({"class": "identity.credentialsSwept", "data": {
            "identityKey": subject_key,
            "targetKey": subject_key,
            "direction": direction,
            "swept": len(hits),
        }})

        # No primaryKey: see the DDL's OutputSchema comment -- nothing this op
        # writes belongs to the subject, and the reply-constraint rejects a
        # primaryKey outside the write footprint.
        return {"mutations": mutations, "events": events, "response": {}}

    fail("unbindIdentityCredentials DDL: unknown operationType: " + ot)
`
