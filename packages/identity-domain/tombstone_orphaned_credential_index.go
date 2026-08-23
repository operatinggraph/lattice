package identitydomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// tombstoneOrphanedCredentialIndexDDL is the canonical name of the vertexType
// DDL owning the TombstoneOrphanedCredentialIndex op
// (erasure-orchestration-design.md §7 residue plane).
const tombstoneOrphanedCredentialIndexDDL = "tombstoneOrphanedCredentialIndex"

// TombstoneOrphanedCredentialIndexDDL declares the cleanup verb for a
// credentialindex vertex that no walk can reach.
//
// # The residue this exists for
//
// A pre-narrowing ShredIdentityKey — every erasure submitted before the shred
// was narrowed to one mutation — tombstoned the subject's boundTo links in both
// directions and deliberately left each credential's
// vtx.credentialindex.<hash> vertex standing. Those vertices hold
// {actorKey, identityKey, boundAt} in the clear: sha256(raw sign-in id) → the
// erased person, readable by anyone who can read Core KV.
//
// Both link directions produce residue, and this op covers BOTH, because the
// leak is the same either way — a live plaintext row naming an erased person:
//
//   - INBOUND. The erased subject S is the link's TARGET: S is the owner, and
//     the index at sha256(C) reads {actorKey: C, identityKey: S}. The row maps
//     a sign-in credential onto the erased person.
//   - OUTBOUND. The erased subject S is the link's SOURCE: S is itself a
//     credential of some other identity O, and the index at sha256(S) reads
//     {actorKey: S, identityKey: O}. This is what a merged-away identity folded
//     into a survivor as its implicit self-credential looks like, and what a
//     Scenario-B identity later linked to another looks like. The row names S
//     — the erased person, in the clear, at a key derived from their destroyed
//     identity — and answers "who is S now" without decrypting anything.
//
// So the erasure discriminator below is symmetric in the two endpoints: EITHER
// endpoint having a closed write path is what makes the row residue. A row
// where both endpoints are live is not residue and is refused.
//
// The two arms do not commit the same thing, because the owner is not the same
// kind of party in them. In the inbound shape the owner is the erased subject
// and the index vertex is the whole of what this op retires — their own
// credentialBinding array is sensitive, its DEK is destroyed or about to be,
// and key destruction erases it more completely than any filter could. In the
// outbound shape the owner is a LIVE third party whose sign-in-methods array
// still lists the erased credential, so the batch also rewrites that array —
// UnbindIdentityCredentials' sweep_outbound body, applied to the one pair no
// boundTo link survives to enumerate.
//
// Nothing already built can reach either shape. UnbindIdentityCredentials
// sweeps by enumerating boundTo links, and both directions are already
// tombstoned, so its sweep emits no mutations at all;
// SealIdentityForErasureComplete's re-walk and the five-arm residue lens
// enumerate the same links and read zero on every arm. The subject therefore
// earns a .erasure attestation reading violating=false over live rows naming
// them. SealIdentityForErasureCompleteDDL's own comment names this class as
// "NOT walkable … invisible to the sweep and to this walk alike" and frames it
// as hypothetical; for every subject shredded before the narrowing landed it is
// real.
//
// This is a GENERAL residue verb — "an erased endpoint, and no live link" — not
// a one-shot migration. The pre-narrowing cascade (deleted by the narrowing) is
// the currently-known producer and the largest known population, but it is not
// the only way the shape arises: ReconcileCredentialBinding's own population —
// bindings made before the boundTo link type existed, which have an index and
// have never had a link — becomes exactly this shape the moment either endpoint
// is later sealed or shredded, and that keeps happening. Extending the
// completion seal's walk would still close nothing: the class is unwalkable in
// Starlark precisely because no link survives to enumerate from.
//
// # Why it is narrow, and what each refusal buys
//
// The grant is scope:any to operator (permissions.go), which is how the
// operator-run CLI driver is reached at all. Unqualified, that would be a bare
// "make any credential stop resolving" verb, so the script refuses everything
// that is not exactly the orphaned-residue shape:
//
//   - NotErased — at least ONE endpoint's write path must be closed (a live
//     erasureRequested marker of that class, OR a shredded piiKey envelope),
//     read the same two ways the identity DDL's write_path_closed reads them,
//     applied to the owner and to the credential alike. A row whose owner AND
//     credential are both live, unerased people is unreachable by this op, at
//     all, on any input.
//   - StillBound — a live boundTo link between this credential and this owner
//     means the ordinary UnbindIdentityCredentials sweep still reaches the pair
//     and is the correct path. This op only ever touches what that sweep cannot
//     see.
//   - OwnerMismatch — the payload must name both halves of the content it is
//     asking to remove, and the index vertex's own data must agree with both.
//     The caller declares its intent; this is never a blind key-only delete.
//   - CredentialIndexAlreadyClear — an absent or already-tombstoned index is a
//     no-op, refused rather than re-tombstoned, so a re-driven CLI sweep is
//     idempotent by refusal instead of by writing.
//
// ReconcileCredentialBinding is this op's exact opposite number and proves the
// division of labor is already the package's shape: it repairs "the link is
// missing and the owner is ALIVE" and refuses outright once either endpoint's
// write path is closed. This op repairs "the link is gone and the owner is
// ERASED". The two carve-outs are disjoint on every input, so neither can race
// the other onto the same key.
func TombstoneOrphanedCredentialIndexDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     tombstoneOrphanedCredentialIndexDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"TombstoneOrphanedCredentialIndex"},
		Description: "Erasure residue cleanup (erasure-orchestration-design.md §7). " +
			"TombstoneOrphanedCredentialIndex{credentialActorKey, identityKey} tombstones ONE " +
			"vtx.credentialindex.<hash> vertex whose boundTo link is gone and one of whose endpoints " +
			"has been erased — an erased person's plaintext {actorKey, identityKey, boundAt} row that " +
			"no boundTo walk can reach. It covers BOTH directions of the residue: the erased subject " +
			"as the row's OWNER (identityKey), and the erased subject as the row's CREDENTIAL " +
			"(actorKey — a merged-away identity folded into a survivor, or a Scenario-B identity later " +
			"linked to another), because a pre-narrowing ShredIdentityKey tombstoned boundTo in both " +
			"directions and left the index behind in both. In the OUTBOUND shape the owner is a live " +
			"third party, so the same batch also rewrites their vtx.identity.<NanoID>.credentialBinding " +
			"array to drop the erased credential (UnbindIdentityCredentials' sweep_outbound body, applied " +
			"to the one pair no link survives to enumerate), pinned to the revision that read observed so " +
			"a concurrent CompleteCredentialLink/UnlinkCredential/MergeIdentity conflicts rather than " +
			"being overwritten; in the INBOUND shape the owner IS the erased subject and their array is " +
			"deliberately left alone, since it is sensitive and its key destruction erases it outright. " +
			"Emits one identity.unbound so the Gateway's " +
			"credential-bindings bucket drops the row, exactly as the ordinary sweep would have for " +
			"this pair had a link survived to enumerate. Four refusals keep it to that shape and no " +
			"wider: NotErased (at least one endpoint's write path must be closed — a live " +
			"erasureRequested marker of that class or a shredded piiKey — so a row between two live, " +
			"unerased people is unreachable on any input), StillBound (a live boundTo link means " +
			"UnbindIdentityCredentials' ordinary sweep is the correct path, not this one), " +
			"OwnerMismatch (the index's own actorKey AND identityKey must equal the payload's — the " +
			"caller declares the content it is removing, never a blind key-only delete), and " +
			"CredentialIndexAlreadyClear (an absent or already-tombstoned index refuses rather than " +
			"re-writing, so a re-driven sweep is idempotent). This is a general erased-endpoint / " +
			"no-live-link residue verb, not a capped one-shot: the pre-narrowing population " +
			"(2026-08-07 and earlier) is the currently-known instance of the shape, and " +
			"ReconcileCredentialBinding's own never-linked corpus becomes the same shape whenever one " +
			"of its endpoints is later sealed or shredded.",
		Script: tombstoneOrphanedCredentialIndexDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"credentialActorKey":{"type":"string","description":"vtx.identity.<NanoID> — the CREDENTIAL whose orphaned credentialindex vertex is being retired (the index key is sha256NanoID of this key)."},` +
			`"identityKey":{"type":"string","description":"vtx.identity.<NanoID> — the erased OWNER the index vertex records. Must equal the index's own data.identityKey."}},` +
			`"required":["credentialActorKey","identityKey"]}`,
		// The index vertex is the row this verb is named for and the one key
		// every accepted shape writes, so it is what primaryKey names. The
		// reply-constraint admits it either way — internal/processor/
		// commit_path.go's primaryKeyInCommit accepts any mutation key in the
		// batch, so the outbound arm's owner-array rewrite neither displaces it
		// nor competes with it. The credential's own key would not qualify at
		// all: only the hash of it is ever a mutation key.
		OutputSchema: `{"type":"object","properties":{"primaryKey":{"type":"string","description":"vtx.credentialindex.<hash> — the index vertex this op tombstoned."}}}`,
		FieldDescription: map[string]string{
			"credentialActorKey": "Full vtx.identity.<NanoID> key of the credential. Its credentialindex vertex (crypto.sha256NanoID of this key) is declared in ContextHint.Reads; the boundTo link toward identityKey is declared in optionalReads and read live. Its OWN erasureRequested marker and piiKey envelope are read too — the outbound arm of the residue, where the erased subject is the credential rather than the owner.",
			"identityKey":        "Full vtx.identity.<NanoID> key of the OWNER the index vertex names. Both endpoints' erasureRequested markers and piiKey envelopes are declared in optionalReads and read live — an undeclared read still refuses, which is what makes the NotErased gate fail-closed. At least one of the two endpoints must read as erased. When this owner's OWN write path is open — the outbound arm — their credentialBinding array is read as well, also optionalReads, and rewritten to drop the erased credential; a dispatcher declares that key only for that arm, because on a shredded owner hydrating a sensitive aspect faults the operation.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "TombstoneOrphanedCredentialIndex — inbound residue: the erased subject is the OWNER",
				Payload: map[string]any{
					"credentialActorKey": "vtx.identity.<NanoID>",
					"identityKey":        "vtx.identity.<NanoID>",
				},
				ExpectedOutcome: "Tombstones vtx.credentialindex.<hash of credentialActorKey> and emits one " +
					"identity.unbound for the pair, so the erased person's plaintext credential row stops " +
					"resolving and the Gateway's credential-bindings bucket drops it.",
			},
			{
				Name: "TombstoneOrphanedCredentialIndex — outbound residue: the erased subject is the CREDENTIAL",
				Payload: map[string]any{
					"credentialActorKey": "vtx.identity.<NanoID>",
					"identityKey":        "vtx.identity.<NanoID>",
				},
				ExpectedOutcome: "Accepted on the same terms. The index at the hash of the ERASED subject's own key " +
					"records them as the credential of a live owner — a merged-away identity folded into its " +
					"survivor, or a Scenario-B identity later linked — and names them in plaintext just as the " +
					"inbound shape does. The row is retired, and because the owner here is a live third party " +
					"the same batch rewrites their credentialBinding array to drop the erased credential, so " +
					"their sign-in-methods list stops naming somebody who no longer exists.",
			},
			{
				Name: "TombstoneOrphanedCredentialIndex — both endpoints are live, unerased people",
				Payload: map[string]any{
					"credentialActorKey": "vtx.identity.<NanoID>",
					"identityKey":        "vtx.identity.<NanoID>",
				},
				ExpectedOutcome: "Rejected with NotErased. This op exists only for residue an erasure left behind; " +
					"a live person's sign-in methods are removed through UnlinkCredential, by the person.",
			},
			{
				Name: "TombstoneOrphanedCredentialIndex — the boundTo link is still live",
				Payload: map[string]any{
					"credentialActorKey": "vtx.identity.<NanoID>",
					"identityKey":        "vtx.identity.<NanoID>",
				},
				ExpectedOutcome: "Rejected with StillBound. A live link means UnbindIdentityCredentials' ordinary " +
					"sweep still enumerates the pair and retires the index and the link together; this op is " +
					"only for what that sweep cannot see.",
			},
		},
	}
}

// tombstoneOrphanedCredentialIndexDDLScript is the Starlark handler.
//
// Every helper below is a verbatim copy of the identity DDL's own
// (ddls.go's credential_index_key / credential_bound_to_key /
// marker_closes_write_path / key_shredded_closes_write_path /
// write_path_closed, and unbind_identity_credentials.go's
// required_string / parts_of). Starlark has no load(), so each script in this
// package carries its own copies — the same convention every sibling script
// here follows, not duplication to consolidate.
//
// The two GATE reads — the erasure discriminator and the boundTo link — go
// through kv.Read rather than state[...], and that difference IS the guard, for
// the reason write_path_closed's own comment gives: a state[...] lookup of a key
// no dispatcher declared reads as ABSENT, so one missing declaration would read
// "not erased" as "not erased" (harmless, it refuses) but "no live link" as "no
// live link" — silently opening StillBound, the one failure mode a fail-closed
// guard may not have. An undeclared kv.Read falls through to a live Core KV GET
// (internal/processor/starlark_kv.go), so both gates hold with no help from any
// submitter; declaring the keys only buys the step-4 snapshot with no round trip.
//
// The outbound arm's read of the owner's credentialBinding array is a third
// kv.Read and is not a gate: it is the body of a mutation, it runs only once the
// gates have already decided, and — unlike the two above — declaring it is not
// free, because the aspect is sensitive. See owner_binding_rewrite.
const tombstoneOrphanedCredentialIndexDDLScript = `
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

def credential_index_key(actor_key):
    return "vtx.credentialindex." + crypto.sha256NanoID(actor_key)

def identity_id(identity_key):
    return identity_key[len("vtx.identity."):]

def credential_bound_to_key(credential_actor_key, owner_identity_key):
    # Contract #1 §1.1: the later-arriving vertex is the source, so the
    # credential is the source and the identity it binds to is the target --
    # "credential boundTo identity" reads as the sentence it is.
    return ("lnk.identity." + identity_id(credential_actor_key) +
            ".boundTo.identity." + identity_id(owner_identity_key))

def marker_closes_write_path(doc):
    # The CLASS is checked, not merely the key. privacy-base's aspect-type DDL
    # gates the class rather than the key, so a mutation at this key declaring
    # some OTHER class falls to resolveGoverningDDL's permissive default and any
    # package script could write one. Requiring the real class means only a real
    # seal can satisfy this half of the erasure test -- which matters more here
    # than anywhere else in the package, because here the marker's presence is
    # what UNLOCKS a tombstone rather than what blocks a write.
    #
    # Tombstone-tolerant, like every sibling copy: nothing removes the marker,
    # and a gate that reopened on a tombstone would be the one failure mode a
    # fail-closed guard may not have.
    if doc == None:
        return False
    if not hasattr(doc, "class") or getattr(doc, "class") != "erasureRequested":
        return False
    return True

def key_shredded_closes_write_path(doc):
    # The piiKey half, checking the class for the same reason: this key's
    # aspect-type DDL is privacy-base's, so a document declaring some other
    # class at the same key falls to the permissive default. Only a real piiKey
    # envelope counts. Tombstone-tolerant: the envelope is the record that a key
    # was destroyed, and destruction does not become untrue when the aspect is
    # deleted.
    if doc == None:
        return False
    if not hasattr(doc, "class") or getattr(doc, "class") != "piiKey":
        return False
    if doc.data == None:
        return False
    return doc.data.get("shredded", False) == True

def owner_binding_rewrite(owner_key, credential_key):
    # The outbound arm's second mutation: the owner named by the residue row is
    # a LIVE third party whose sign-in-methods array still lists the erased
    # credential, and retiring the index alone leaves that entry standing --
    # a phantom sign-in method naming somebody who no longer exists.
    # UnbindIdentityCredentials' sweep_outbound performs this same rewrite for
    # every pair a live boundTo link still reaches; this is that body applied to
    # the one pair no link survives to enumerate.
    #
    # Reached ONLY when the owner's write path is open -- see the arm
    # discriminator in execute for why that predicate, and why reading this key
    # at all depends on it.
    #
    # read-posture: (d) declared in contextHint.optionalReads by the CLI driver
    # (cmd/lattice/identity/credential_residue.go), which declares it for the
    # outbound arm and only there. Absence-tolerant, and absence is an ordinary
    # state rather than a wiring fault: an identity that never claimed through
    # ClaimIdentity carries no array at all, and a merged-away secondary's is
    # tombstoned.
    binding = kv.Read(owner_key + ".credentialBinding")
    if binding == None or binding.isDeleted or binding.data == None:
        return None
    existing = dict(binding.data)
    credentials = existing.get("credentials")
    if credentials == None or type(credentials) != type([]):
        # The pre-array record, folded into a one-element array so the filter
        # below has exactly one shape to work on.
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
        # Nothing of this credential in the owner's array: an UnlinkCredential
        # already took it out, or the row was residue the array never named.
        # The index tombstone still goes; rewriting an unchanged array would
        # burn a mutation and give a re-driven sweep a revision to bump forever.
        return None

    # The singular actorKey/boundAt pair is the pre-array record readers still
    # fall back to, so it must not keep naming the credential just removed.
    # When nothing remains to promote, both fields are OMITTED rather than set
    # to null: the aspect's schema types actorKey as a string and the fallback
    # readers test for the field's PRESENCE, so a null would satisfy the test
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

    # PINNED TO THE REVISION THIS READ OBSERVED, because the array is a shared
    # vertex with three other writers and nothing above excludes any of them:
    # CompleteCredentialLink appends an entry, UnlinkCredential removes one, and
    # identity-hygiene's MergeIdentity unions a whole set in. None of the three
    # is refused by this op's guards -- they act on the LIVE owner, whose write
    # path is open by construction on this arm.
    #
    # Unpinned, the update is not left unconditioned: step 8 conditions it on
    # its own prior-document read (internal/processor/step8_commit.go's
    # readPriorDocuments), and where a dispatcher declared the key, step 4's
    # snapshot revision is applied instead (commit_path.go's
    # applyHydratedRevisions). Neither substitutes for this pin. The step-8 read
    # happens AFTER the filter above ran, so a CompleteCredentialLink landing in
    # that window satisfies the condition and is silently overwritten by an
    # array that never saw its append -- a credential the person just added,
    # gone, with nothing left to notice it. And the step-4 route exists only
    # when a submitter chose to declare the key, which is a client's read
    # disposition, not a server policy this op may rest a shared-vertex write
    # on. Carrying this read's own revision makes the decision and the write
    # atomic under either dispatcher: a racing writer conflicts the whole batch,
    # and the operator's sweep re-runs against a residue that has not moved.
    return {"op": "update", "key": owner_key + ".credentialBinding",
            "expectedRevision": binding.revision,
            "document": {"class": "credentialBinding", "vertexKey": owner_key,
                         "localName": "credentialBinding", "isDeleted": False,
                         "data": data}}

def write_path_closed(identity_key):
    # The identity DDL's dual discriminator, copied verbatim. True when EITHER
    # this person invoked a right to be forgotten (a live-CLASS erasureRequested
    # marker) OR their PII key has already been destroyed. Applied to BOTH
    # endpoints of the row below -- the owner and the credential alike -- so the
    # argument here is whichever endpoint is being asked about, not necessarily
    # the row's owner.
    #
    # The SECOND condition is the one this op depends on. The marker is written
    # by the erasure PATTERN's seal; a bare ShredIdentityKey submit -- what the
    # operator Shred button has always sent, and the exact population whose
    # residue this op cleans up -- writes only piiKey.shredded, with no marker
    # beside it. Gated on the marker alone, every subject this op exists for
    # would read as a live, unerased person and be refused.
    #
    # read-posture: (d) declared in contextHint.optionalReads by the CLI driver
    # (cmd/lattice/identity/credential_residue.go). Absence-tolerant, and
    # absence is the ordinary case -- no identity carries this marker until it
    # is sealed for erasure.
    if marker_closes_write_path(kv.Read(identity_key + ".erasureRequested")):
        return True
    # read-posture: (d) declared in contextHint.optionalReads by the CLI driver,
    # absence-tolerant for the same reason -- no identity carries a piiKey until
    # its first sensitive write.
    return key_shredded_closes_write_path(kv.Read(identity_key + ".piiKey"))

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "TombstoneOrphanedCredentialIndex":
        credential_actor_key = required_string(p, "credentialActorKey")
        parts_of(credential_actor_key, "credentialActorKey", "identity")
        identity_key = required_string(p, "identityKey")
        parts_of(identity_key, "identityKey", "identity")
        if credential_actor_key == identity_key:
            # The same self-loop guard ReconcileCredentialBinding applies: a
            # vertex is not its own credential, and a merge's implicit
            # self-credential index is not this op's residue class.
            fail("SelfLoop: credentialActorKey and identityKey name the same vertex " + identity_key)

        # The index vertex is hydrated, not read live: it is a deterministic
        # function of the payload, the driver names it in contextHint.reads
        # (it scanned the key to get here), and it is this op's ONLY mutation
        # key -- so step 8 applies the revision it was hydrated at as the
        # commit's OCC condition. Anything that retires or repoints the index
        # between hydrate and commit conflicts the whole batch instead of
        # letting a stale judgement land.
        index_key = credential_index_key(credential_actor_key)
        index_vtx = state[index_key] if index_key in state else None
        if index_vtx == None or (hasattr(index_vtx, "isDeleted") and index_vtx.isDeleted):
            # Refused, not accepted-as-no-op: a re-driven sweep must be
            # idempotent WITHOUT writing, and an accept here would re-tombstone
            # an already-clear vertex on every pass, bumping its revision
            # forever.
            fail("CredentialIndexAlreadyClear: " + index_key + " is absent or already tombstoned; nothing to retire")

        # Author-declares-intent. The payload must name the content it is asking
        # to remove and the vertex must agree with BOTH halves -- the owner it
        # records and the credential it indexes. Without this the verb degrades
        # to "tombstone the index of whichever credential hashes to this key",
        # and the erasure test below would be answered about an identity the
        # index never named.
        index_data = index_vtx.data if index_vtx.data != None else {}
        if index_data.get("identityKey") != identity_key:
            fail("OwnerMismatch: " + index_key + " does not record identityKey " + identity_key)
        if index_data.get("actorKey") != credential_actor_key:
            fail("OwnerMismatch: " + index_key + " does not record actorKey " + credential_actor_key)

        # The safety property, stated as a refusal: this op cannot reach a row
        # between two live people on ANY input. Everything above it is about
        # naming the right row; this is the only thing standing between the verb
        # and a live person's sign-in method.
        #
        # SYMMETRIC in the two endpoints, because the residue is. The
        # pre-narrowing shred tombstoned boundTo in BOTH directions, so an
        # erased subject leaves an orphaned index behind whether they were the
        # link's target (they are this row's identityKey) or its source (they
        # are this row's actorKey -- a merged-away identity folded into its
        # survivor as an implicit self-credential, or a Scenario-B identity
        # later linked to another). Both rows name the erased person in the
        # clear at a key derived from their destroyed identity, so both are the
        # leak this op exists to close; gating on identity_key alone would
        # silently skip half the class.
        #
        # Widening it does not widen what the verb can REACH: the OwnerMismatch
        # pair above has already forced the stored row to name both endpoints
        # exactly as the payload does, so no caller can invent a row whose
        # actorKey is some erased identity in order to reach a live person's.
        # The disjunction short-circuits, so the credential's two keys are read
        # only when the owner's arm does not already answer. The owner's half is
        # kept because it is also the arm discriminator below.
        owner_write_path_closed = write_path_closed(identity_key)
        if not (owner_write_path_closed or write_path_closed(credential_actor_key)):
            fail("NotErased: neither " + identity_key + " nor " + credential_actor_key + " has a closed write path (no live erasureRequested marker, no shredded piiKey on either endpoint); this op retires residue an erasure left behind, and a live person's credential is removed through UnlinkCredential, by the person")

        # The narrowness guard. A live link means UnbindIdentityCredentials'
        # ordinary sweep still enumerates this pair and retires the index and
        # the link together, in one batch, with the owner's array rewrite where
        # that applies -- doing it here instead would leave the link standing
        # and pointing at an index that no longer resolves.
        link_key = credential_bound_to_key(credential_actor_key, identity_key)
        # read-posture: (d) declared in contextHint.optionalReads by the CLI
        # driver. Absence-tolerant and absence is the ORDINARY case here: the
        # pre-narrowing shred tombstoned both directions, and a hard-removed
        # link reads absent. Read live for the reason the const comment gives --
        # an undeclared state[...] lookup would read absent and open this gate.
        link = kv.Read(link_key)
        if link != None and not (hasattr(link, "isDeleted") and link.isDeleted):
            fail("StillBound: " + link_key + " is live; UnbindIdentityCredentials' sweep reaches this pair and is the correct path, not this op")

        mutations = [{"op": "tombstone", "key": index_key}]

        # WHICH ARM THIS IS, decided by the owner alone. The gate above passed,
        # so at least one endpoint is erased; if the OWNER's write path is still
        # OPEN then the erased endpoint can only be the credential, which is the
        # outbound shape by definition -- a live third party carrying a sign-in
        # method that belongs to somebody who no longer exists. That array is
        # the only thing this op rewrites, and this is the only arm it rewrites
        # on. In the inbound shape the owner IS the erased subject and their
        # array is theirs to lose: it is sensitive, its DEK is destroyed or
        # about to be, and key destruction erases it far more completely than a
        # filter would.
        #
        # The predicate is the WHOLE write_path_closed and not just its
        # piiKey.shredded half, and the case that separates them is an owner
        # closed by the MARKER alone -- sealed for erasure, key not yet
        # destroyed, array still readable and writable. That owner is not a
        # third party this op is tidying up after; they are a subject mid-
        # erasure, and rewriting their array would be this op, holding a
        # scope:any operator grant, driving a fresh sensitive write straight
        # through the gate that exists to stop exactly that ("no writer may
        # create a fresh erasable representation of them" -- ddls.go's
        # write_path_closed). UnbindIdentityCredentials' own inbound sweep
        # declines the same rewrite on the same subject for the same reason.
        #
        # The guard is also what makes the read inside the rewrite safe at all.
        # credentialBinding is a SENSITIVE aspect, so any read of it decrypts
        # under the owner's DEK (internal/processor/sensitive_decrypt.go); on a
        # shredded owner the Vault answers ErrKeyShredded and the operation
        # fails outright instead of retiring the index. The inbound residue this
        # op primarily exists for is precisely a shredded owner, so ordering the
        # erasure question ahead of the array read is not defensive padding --
        # it is the only order under which both arms work.
        if not owner_write_path_closed:
            rewrite = owner_binding_rewrite(identity_key, credential_actor_key)
            if rewrite != None:
                mutations.append(rewrite)

        # identity.unbound, verbatim rather than a new event type: the outcome
        # is semantically identical to what the inbound sweep would have emitted
        # for this exact pair had a link survived to enumerate, and the
        # Gateway's credential-bindings consumer already tolerates an unbound
        # for a row it never materialized.
        #
        # response.primaryKey stays index_key with a second mutation present:
        # the reply-constraint admits any mutation key in the batch
        # (internal/processor/commit_path.go's primaryKeyInCommit), and the
        # index vertex is the row this verb is named for.
        return {
            "mutations": mutations,
            "events": [{"class": "identity.unbound", "data": {
                "identityKey": identity_key,
                "actorKey": credential_actor_key,
            }}],
            "response": {"primaryKey": index_key},
        }

    fail("tombstoneOrphanedCredentialIndex DDL: unknown operationType: " + ot)
`
