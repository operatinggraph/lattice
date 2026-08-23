package privacybase

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// sealIdentityForErasureCompleteDDL is the canonical name of the vertexType DDL
// owning the SealIdentityForErasureComplete op — the erasure's terminal
// attestation (erasure-orchestration-design.md §7.2).
const sealIdentityForErasureCompleteDDL = "sealIdentityForErasureComplete"

// erasureAspectDDL is the canonical name of the aspectType DDL declaring the
// vtx.identity.<NanoID>.erasure attestation. Distinct from erasureRequested:
// that one opens an erasure, this one closes it.
const erasureAspectDDL = "erasure"

// erasureCompletedEventDDL is the canonical name of the erasureCompleted
// event-type DDL. Per Contract #3 §3.4 a registered event-type DDL's
// canonicalName equals the event's own class.
const erasureCompletedEventDDL = "privacy.erasureCompleted"

// ErasureAspectDDL declares the erasure-completion attestation aspect.
//
// vtx.identity.<NanoID>.erasure = {sealedAt, sealedForShreddedAt, coverage} is
// what an auditor reads to the question "prove it" (§7.4). It is written by
// exactly one operation, and that operation walks the person's remaining links
// itself, inside the same atomic batch, before writing it — so the aspect's
// presence is evidence rather than assertion.
//
// # Why sealedForShreddedAt and not a boolean
//
// Erasure is a CYCLE, not a state. A re-shred of an already-erased identity
// bumps piiKey.shreddedAt and starts a second cycle whose residue must be
// swept and attested again. A boolean `erased: true` could not express that:
// it would read complete on cycle 1's evidence forever. The residue lens
// therefore field-diffs this aspect's sealedForShreddedAt against the LIVE
// piiKey.shreddedAt (`lenses.go`, and §5.5 on why the marker's own copy is not
// a substitute), and a cycle nobody has attested reopens the gap without
// anything being tombstoned.
//
// # What coverage counts, and what it does not
//
// coverage records the TOMBSTONED links the seal walked past on its way to
// proving no live one remains — how much of this person was erased, per class.
// It is not a residue count: a residue count in an attestation would always be
// zero, which proves nothing. It is bounded by what the walk could reach (see
// SealIdentityForErasureCompleteDDL's budget note), so it is a floor on what
// was erased rather than a census.
//
// Not sensitive: three counts and two timestamps, no PII.
//
// # What permittedCommands here does and does not enforce
//
// The same limits ErasureRequestedAspectDDL records apply verbatim — it gates
// the mutation document's CLASS, not the key, and it cannot see a tombstone at
// all. The consequence differs though, and in the safe direction: a removed
// attestation makes the residue lens reopen missing_erasureSeal and the target
// re-dispatch this op, which re-verifies and rewrites. An attestation is the
// one thing on this plane that recovers from being destroyed.
func ErasureAspectDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     erasureAspectDDL,
		Class:             "meta.ddl.aspectType",
		Sensitive:         false,
		PermittedCommands: []string{"SealIdentityForErasureComplete"},
		Description: "Erasure-completion attestation (erasure-orchestration-design.md §7.2/§7.4): " +
			"vtx.identity.<NanoID>.erasure = {sealedAt, sealedForShreddedAt, coverage}. Written only by " +
			"SealIdentityForErasureComplete, which re-walks the identity's boundTo, indexes and " +
			"duplicateOf links in the same commit and fails closed if any live one remains — so this " +
			"aspect is evidence the erasure converged, not a claim that it did. sealedForShreddedAt is " +
			"the erasure CYCLE this attestation covers: the residue lens field-diffs it against the live " +
			"piiKey.shreddedAt, so a re-shred reopens the completion gap without anything being " +
			"tombstoned. coverage counts the tombstoned links the verification walked past, per class — " +
			"a floor on what was erased, bounded by the walk's live-read budget.",
		Script: erasureAspectDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"sealedAt":{"type":"string","description":"RFC3339 instant this erasure cycle was first attested complete (preserved across a re-verification of the same cycle)."},` +
			`"sealedForShreddedAt":{"type":"string","description":"The piiKey.shreddedAt this attestation covers — the erasure cycle discriminator."},` +
			`"coverage":{"type":"object","description":"Tombstoned links the verification walk counted, per class: credentials (boundTo, both directions), indexes (inbound identityindex edges), duplicates (duplicateOf, both directions)."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"sealedAt":            "When this erasure cycle was first attested complete. Preserved when the same cycle is re-verified, so the attestation instant is the one the verification first earned.",
			"sealedForShreddedAt": "The piiKey.shreddedAt in force when the attestation was written. A re-shred bumps that stamp, the field-diff against it reopens missing_erasureSeal, and the second cycle is attested on its own evidence.",
			"coverage":            "Per-class counts of the tombstoned links the seal's own enumeration walked past while proving no live link remains. A floor on what was erased, not a census — the walk is bounded by the Processor's live-read budget.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "erasure attestation",
				Payload: map[string]any{
					"sealedAt":            "2026-08-07T00:00:00Z",
					"sealedForShreddedAt": "2026-08-07T00:00:00Z",
					"coverage":            map[string]any{"credentials": 3, "indexes": 2, "duplicates": 1},
				},
				ExpectedOutcome: "Written by SealIdentityForErasureComplete after its in-commit walk found no live boundTo, indexes or duplicateOf link on the identity. The residue lens's missing_erasureSeal closes and the row stops violating.",
			},
		},
	}
}

// erasureAspectDDLScript is the declaration-only Starlark for the erasure
// aspect-type DDL. An aspect-type DDL declares shape and anchoring, not an
// operation handler — mirrors erasureRequestedAspectDDLScript.
const erasureAspectDDLScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`

// SealIdentityForErasureCompleteDDL returns the DDL meta-vertex declaration for
// SealIdentityForErasureComplete — the terminal gap action of the
// identityErasureComplete Weaver target (erasure-orchestration-design.md §7.2).
//
// # The lens decides when to try; this op decides whether it is true
//
// §7.3 records the trap this design walked into: adjacency-based counting lags
// its commit, so a residue lens can read zero over a link that exists. Two
// mechanisms close it and both are needed. §6's write-path gates remove the
// CREATE, so the lag-on-create case cannot arise. This op is the other one: it
// re-runs the enumerations itself, in its own atomic batch, and refuses to
// write the attestation if any arm returns a live link. A stale row can cause a
// wasted dispatch; it cannot cause a false attestation.
//
// # The five arms, and why the count must be exactly five
//
// The walk covers every relation the two sweeps clear: boundTo INBOUND and
// OUTBOUND (UnbindIdentityCredentials sweeps both — the subject owns
// credentials and is itself someone else's), indexes INBOUND, and duplicateOf
// OUTBOUND and INBOUND (PurgeIdentityDedupFootprint, one class per commit). An
// arm this op does not walk is an arm the attestation silently does not cover,
// and every arm here is a direction some real writer produces: a person is on
// either side of a duplicateOf pair, and owns credentials as readily as they
// are one.
//
// One residue class is NOT walkable and is therefore not covered: a
// credentialindex vertex carries its identity in its body and has no link to
// it, so no enumeration reaches one. UnbindIdentityCredentials tombstones each
// alongside its boundTo, which covers every credentialindex that has a link —
// one that does not is invisible to the sweep and to this walk alike.
//
// # The two async halves are re-verified here, not inherited from gap ordering
//
// The residue lens opens missing_erasureSeal only after vaultKeyDestroyed and
// projectionsNullified are both true, and its own comment names that ordering
// as the guarantee "until the seal op re-verifies them itself" — an obligation
// it hands this op by name. It is discharged here: a Vault key that is
// still live, or projections that were never nullified, refuse the attestation
// regardless of what any projection ordered. A guarantee that lives only in a
// lens's column ordering dies the first time a gap is dispatched out of order,
// and a directOp gap fires from a reconcile sweep as readily as from a fresh
// row.
//
// # What actually bounds this op is WALL TIME, and it binds long before the
// # page budget does
//
// Proving ABSENCE means paging the whole cursor on every arm: one sweep's worth
// of tombstones fills the first page, so an early return would read "converged"
// over links it never reached. That makes this op strictly more expensive than
// either sweep, which stop as soon as SWEEP_LIMIT live links are in hand — and
// it runs at the moment a subject's tombstone count is at its maximum.
//
// Two budgets bound the walk, and it is important not to confuse them:
//
//   - The LIVE-READ budget. kv.Links charges 1 for the call plus its clamped
//     page limit per page (starlark_kv.go) against a 60,000-unit budget
//     (live_read_budget.go). VERIFY_PAGE_BUDGET x (1 + VERIFY_PAGE_LIMIT) =
//     41,120, comfortably inside it; five arms at the sweeps' own 64 pages
//     would charge 82,240 and abort mid-walk, which is why the page budget here
//     is SHARED across the arms rather than allotted per-arm.
//   - The WALL budget, 250ms (starlark_runner.go). This is the one that binds.
//     connLinkLister issues one KVGet per listed key, and KVListKeysFilter
//     re-enumerates the arm's whole matching key set on every page call, so a
//     walk over N links costs N sequential round trips plus a quadratic term in
//     key names. The page budget's nominal ceiling of 40,960 links is nowhere
//     near reachable in 250ms; the practical ceiling is one to two orders of
//     magnitude lower and depends on substrate latency, not on any constant
//     here.
//
// The consequence is stated plainly because it is not what the design assumed:
// past that practical ceiling this op dies as ScriptTimeout rather than the
// named ErasureVerificationUnreachable, so the loud, erasure-specific stop is
// not the stop that fires for a genuinely wide subject. Both are refusals and
// both are fail-closed — no attestation is written either way, which is the
// property that matters — but the diagnostic an operator gets names nothing
// about erasure. VERIFY_PAGE_BUDGET therefore functions as the live-read guard
// it can actually enforce, not as the wall-clock guard the error text reads
// like.
//
// The durable fix is the one both sweeps already name: a hard delete that takes
// tombstones out of the keyspace makes a converged subject's walk proportional
// to its LIVE links, which is zero by the time this op runs. That removes this
// ceiling, the sweeps' own, and the per-pass rescan cost together.
//
// # Fail-closed on a merged-away identity, and on an unshredded one
//
// Both guards are SealIdentityForErasure's verbatim, for its reasons. A
// merged-away identity keeps a live vertex whose credentials and indexes
// already moved to the survivor, so its residue is zero by construction and an
// attestation there would attest an erasure that erased nothing. An identity
// with no shredded piiKey has no cycle discriminator to record, and a null
// sealedForShreddedAt versus a null shreddedAt reads as "already sealed".
//
// # Re-verification of the same cycle
//
// The write is an unconditioned update, so the op is idempotent. sealedAt is
// PRESERVED when the existing attestation already names this cycle — the
// instant the verification first earned is the meaningful one — while coverage
// is always rewritten from the walk just performed. A re-verification is not
// free and is not meant to be: it re-walks every arm, which is exactly what
// makes a re-dispatch after a row went briefly stale harmless.
//
// Reads: subjectKey in ContextHint.Reads — the target-existence guard, which
// covers the TOMBSTONED arm. A key naming nothing at all is recorded
// required-absent at step 4, and the fault is raised when the script first
// touches it — here inside vertex_alive — so the caller sees HydrationMiss
// rather than the script's own NotFound. Both refuse and neither writes.
//
// subjectKey + ".piiKey", + ".erasureRequested", + ".erasure", + ".mergedInto"
// and + ".state" in ContextHint.OptionalReads — read-posture class (d),
// absence-tolerant: a first attestation has no prior .erasure, an unmerged
// identity has no .mergedInto, and each absence is an ordinary case the script
// decides on rather than a fault.
//
// The five link walks are class (e) — bounded kv.Links enumerations, declared
// in ContextHint.Enumerations by the one dispatcher that submits this op: the
// identityErasureComplete target's missing_erasureSeal gap (targets.go). The
// declaration is metadata, not a hydration directive — each arm stays a paged
// live walk here, and the Processor validates the declaration's shape and
// otherwise ignores it.
func SealIdentityForErasureCompleteDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     sealIdentityForErasureCompleteDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"SealIdentityForErasureComplete"},
		Description: "Erasure completion attestation (erasure-orchestration-design.md §7.2). " +
			"SealIdentityForErasureComplete{subjectKey} re-walks the identity's boundTo (both " +
			"directions), indexes (inbound) and duplicateOf (both directions) links inside its own " +
			"commit and writes vtx.identity.<NanoID>.erasure = {sealedAt, sealedForShreddedAt, " +
			"coverage} only if every arm is clear — the lens decides when to try, this op decides " +
			"whether it is true, so a stale projection can waste a dispatch but cannot produce a false " +
			"attestation. Refuses with ErasureIncomplete on any live residue link and on an " +
			"un-destroyed Vault key or un-nullified projections (both re-verified here rather than " +
			"inherited from the lens's gap ordering); with ErasureNotRequested unless a real " +
			"erasureRequested marker arms it; with ErasureNotShredded unless the piiKey envelope " +
			"carries a shreddedAt to record as the cycle discriminator; with IdentityMerged for a " +
			"merged-away identity, whose residue is zero by construction. Stops loudly with " +
			"ErasureVerificationUnreachable rather than attesting an erasure whose links it could not " +
			"page far enough to inspect. Idempotent: re-verifying the same cycle preserves sealedAt " +
			"and rewrites coverage.",
		Script: sealIdentityForErasureCompleteDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"subjectKey":{"type":"string","description":"vtx.identity.<NanoID> — the identity whose erasure is being attested. Named subjectKey to match the Weaver directOp gap action that dispatches it, and the erasure plane's other step ops."}},` +
			`"required":["subjectKey"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.identity.<NanoID> of the attested identity. The cycle attested is read from the aspect the commit wrote — a response may carry primaryKey and nothing else."}}}`,
		FieldDescription: map[string]string{
			"subjectKey": "Full vtx.identity.<NanoID> key of the identity being attested. Must exist, not be tombstoned, not be merged away, carry an erasureRequested marker and a shredded piiKey whose Vault destruction and projection nullification have both landed, and have no live boundTo, indexes or duplicateOf link left. Declared in ContextHint.Reads; piiKey, erasureRequested, erasure and mergedInto in ContextHint.OptionalReads; the five link walks in ContextHint.Enumerations.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "SealIdentityForErasureComplete — attest a converged erasure",
				Payload: map[string]any{"subjectKey": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Walks all five residue arms, finds no live link, and writes " +
					"vtx.identity.<NanoID>.erasure = {sealedAt, sealedForShreddedAt, coverage} plus " +
					"privacy.erasureCompleted. The residue lens's missing_erasureSeal closes and the row stops violating.",
			},
			{
				Name:            "SealIdentityForErasureComplete — a credential the sweep has not reached yet",
				Payload:         map[string]any{"subjectKey": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Rejected with ErasureIncomplete naming the live link. The residue row was stale or a gap fired out of order; no attestation is written and the sweep gap re-dispatches.",
			},
		},
	}
}

// sealIdentityForErasureCompleteDDLScript handles
// SealIdentityForErasureComplete. Starlark has no load(), so it carries its own
// copies of the required_string / parts_of / vertex_alive / live_data /
// read_aspect_value helpers, exactly as its siblings do.
//
// VERIFY_PAGE_BUDGET is shared across all five arms rather than allotted
// per-arm; see the DDL comment for why that is the load-bearing choice and not
// a micro-optimization. 160 pages x 256 links charges 41,120 of the 60,000-unit
// live-read budget (kv.Links charges 1 for the call plus the clamped limit per
// page), leaving room for the handful of kv.Read calls above it.
const sealIdentityForErasureCompleteDDLScript = `
VERIFY_PAGE_LIMIT = 256
VERIFY_PAGE_BUDGET = 160

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

def live_data(doc):
    if doc == None or doc.isDeleted:
        return None
    if doc.data == None:
        return {}
    return dict(doc.data)

def any_data(doc):
    # live_data's tombstone-blind sibling. Used only where a tombstoned
    # document's BODY is still the truth we want -- a tombstone preserves the
    # stored document whole (Contract #3 §3.3), so an attestation that was
    # somehow removed still carries the instant its verification first earned,
    # and rewriting it must not silently restamp that instant to now.
    if doc == None:
        return None
    if doc.data == None:
        return {}
    return dict(doc.data)

def aspect_value(doc):
    # The {"value": ...} single-field aspect shape, read from a LIVE kv.Read
    # rather than from state[...]. That difference is the guard: a state[...]
    # lookup of a key no dispatcher declared reads as ABSENT, so one missing
    # declaration would silently open a fail-closed gate -- the one failure mode
    # such a gate may not have (identity-domain/ddls.go says this verbatim of
    # its own merged-identity gate). An undeclared kv.Read falls through to a
    # live GET and still refuses.
    if doc == None or doc.isDeleted:
        return None
    if doc.data != None and "value" in doc.data:
        return doc.data["value"]
    return None

def marker_arms_the_seal(doc):
    # The CLASS is checked, not merely the key -- the aspect-type DDL gates the
    # class rather than the key, so a mutation at this key declaring some other
    # class falls to resolveGoverningDDL's permissive default and any package
    # script could write one. Requiring the real class means only a real
    # SealIdentityForErasure can arm this verb. Tombstone-blind, exactly as both
    # sweep ops are on the same marker: nothing removes it, and a gate that
    # reopened on a tombstone would be the one failure mode a fail-closed guard
    # may not have -- here it would let an attestation be written for a person
    # who never requested erasure.
    if doc == None:
        return False
    if not hasattr(doc, "class") or getattr(doc, "class") != "erasureRequested":
        return False
    return True

def verify_arm(identity_key, relation, direction, pages_left):
    # Pages the WHOLE cursor to prove absence, failing on the first live link.
    # An early return on a clear first page would read "converged" over a
    # subject whose live links sit behind one sweep's worth of tombstones --
    # the same soft-delete property that forced the sweeps to page.
    #
    # Returns (tombstoned_count, pages_used). The tombstoned count is the
    # coverage this arm contributes: what was erased, not what remains.
    dead = 0
    used = 0
    cursor = None
    for _page in range(VERIFY_PAGE_BUDGET):
        if used >= pages_left:
            # The budget is SHARED, so the arm that trips this is often not the
            # arm that spent it: an arm arriving with pages_left already at 0
            # fails here having read nothing, and may itself be empty. Report
            # this arm's own allowance and say where the rest went, rather than
            # naming this relation as the wide one -- a stuck erasure is
            # triaged from this string.
            fail("ErasureVerificationUnreachable: " + identity_key + " exhausted its verification " +
                 "budget at relation " + relation + " (" + direction + "), which was allowed " +
                 str(pages_left) + " of this commit's shared " + str(VERIFY_PAGE_BUDGET) +
                 " pages; earlier arms consumed the rest, so the wide relation may be one already " +
                 "walked. The erasure cannot be attested because its residue cannot be fully " +
                 "inspected, and an attestation over uninspected links is the one outcome this op " +
                 "exists to refuse")
        # read-posture: (e) relation=boundTo epoch=none; relation=indexes
        # epoch=none; relation=duplicateOf epoch=none -- one walker serves all
        # five arms, so every relation the dispatcher declares in
        # contextHint.enumerations is named here. Read-only: this op mutates
        # nothing it enumerates.
        #
        # The walk contends no shared OCC key (Contract #2 §2.5.1), so what
        # keeps a link from being created behind it is the §6 write-path gates,
        # not serialization: every creator of these three classes reads the
        # erasureRequested marker and refuses a marked identity. That is a
        # property of the current corpus held by review, and it is the only
        # thing standing between this op and an attestation over a link created
        # after its arm was walked. A gate's own commit is conditioned on its
        # own footprint and never on the marker, so a writer whose hydration
        # predates the marker's commit can still land one -- a window bounded by
        # a single op's execution, and the seal runs long after the marker.
        links, cursor = kv.Links(identity_key, relation, direction, cursor, VERIFY_PAGE_LIMIT)
        used += 1
        for lk in links:
            if not lk.isDeleted:
                fail("ErasureIncomplete: " + identity_key + " still has a live " + relation +
                     " link (" + direction + "): " + lk.key + "; the residue sweep has not converged, " +
                     "so no completion attestation may be written")
            dead += 1
        if cursor == None:
            return dead, used
    fail("ErasureVerificationUnreachable: " + identity_key + " has more " + relation + " links (" +
         direction + ") than the shared budget of " + str(VERIFY_PAGE_BUDGET) +
         " pages can reach in one commit")

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "SealIdentityForErasureComplete":
        identity_key = required_string(p, "subjectKey")
        parts_of(identity_key, "subjectKey", "identity")

        if not vertex_alive(state, identity_key):
            fail("NotFound: subjectKey " + identity_key + " is absent or tombstoned")

        # A merged-away identity's credentials and indexes moved to the
        # survivor, so its residue is zero by construction and an attestation
        # here would attest an erasure that erased nothing.
        #
        # The gate keys on .state, not on .mergedInto, which is the platform's
        # own convention (identity-domain's enforce_not_merged reads state and
        # uses mergedInto only to name the survivor). .state is written for
        # every identity, so its absence is a real signal; .mergedInto exists
        # only on a merged one, so an absent read of it is indistinguishable
        # from an unmerged identity. Both are checked, and either refuses.
        #
        # read-posture: (d) declared in contextHint.optionalReads, and read LIVE
        # rather than from state[...]: a state[...] lookup of a key no
        # dispatcher declared reads as ABSENT, so one missing declaration would
        # silently open this gate. An undeclared kv.Read falls through to a live
        # GET and still refuses. This op's dispatcher does not exist yet, which
        # is exactly when that distinction earns its keep.
        merged_into = aspect_value(kv.Read(identity_key + ".mergedInto"))
        # read-posture: (d) declared in contextHint.optionalReads, live for the
        # reason above -- .state is the authoritative merged signal, .mergedInto
        # only names the survivor.
        identity_state = aspect_value(kv.Read(identity_key + ".state"))
        if merged_into != None or identity_state == "merged":
            survivor = merged_into
            if survivor == None or survivor == "":
                survivor = "an identity this one's .mergedInto does not name"
            fail("IdentityMerged: " + identity_key + " was merged into " + survivor + "; attest the erasure against the surviving identity, whose credentials and indexes this one's were folded into")

        # read-posture: (d) declared in contextHint.optionalReads. Absence means
        # this person never requested erasure, which is the one caller this op
        # must refuse outright: it would otherwise write a completion
        # attestation for someone whose write path was never closed, over a
        # residue set nothing bounds.
        if not marker_arms_the_seal(kv.Read(identity_key + ".erasureRequested")):
            fail("ErasureNotRequested: " + identity_key + " carries no erasureRequested marker; only an identity sealed by SealIdentityForErasure has a closed write path, and a completion attestation over an open set proves nothing")

        # read-posture: (d) same class, same declaration site. The cycle
        # discriminator lives on the LIVE envelope, never on the marker's copy
        # of it (§5.5): the marker is refreshed only when SealIdentityForErasure
        # runs, and the pattern's step-2 guard skips it on a re-triggered
        # erasure, so a marker-diff would read equal after a genuine re-shred.
        envelope = live_data(kv.Read(identity_key + ".piiKey"))
        if envelope == None or envelope.get("shredded") != True:
            fail("ErasureNotShredded: " + identity_key + " has no shredded piiKey envelope; a completion attestation cannot precede the shred it attests")

        shredded_at = envelope.get("shreddedAt")
        if shredded_at == None or type(shredded_at) != type("") or len(shredded_at) == 0:
            fail("ErasureNotShredded: " + identity_key + " has a shredded piiKey envelope with no shreddedAt stamp, so the attestation has no cycle discriminator to record; re-run ShredIdentityKey to restamp it")

        # The two ASYNC halves, re-verified here rather than inherited from the
        # lens's gap ordering. The residue lens does order them ahead of the
        # seal gap, but a directOp gap fires from a reconcile sweep as readily
        # as from a fresh row, and a guarantee that lives in a projection's
        # column ordering is not one. A key the Vault still holds is not
        # destroyed, and an attestation over it would be false in the way that
        # matters most.
        if envelope.get("vaultKeyDestroyed") != True:
            fail("ErasureIncomplete: " + identity_key + " has not had its Vault key destroyed (piiKey.vaultKeyDestroyed is not true); the crypto-shred's async half is still outstanding, so the erasure is not complete")
        if envelope.get("projectionsNullified") != True:
            fail("ErasureIncomplete: " + identity_key + " has not had its projections nullified (piiKey.projectionsNullified is not true); decrypted renderings of this person may still stand in read models")

        # The five arms, in the order the residue lens counts them. The shared
        # page budget is spent down across arms: a subject wide on one relation
        # and narrow on the rest is verifiable, which per-arm allotment of the
        # same total would refuse.
        pages_left = VERIFY_PAGE_BUDGET

        bound_in, used = verify_arm(identity_key, "boundTo", "in", pages_left)
        pages_left -= used
        bound_out, used = verify_arm(identity_key, "boundTo", "out", pages_left)
        pages_left -= used
        index_in, used = verify_arm(identity_key, "indexes", "in", pages_left)
        pages_left -= used
        dup_out, used = verify_arm(identity_key, "duplicateOf", "out", pages_left)
        pages_left -= used
        dup_in, used = verify_arm(identity_key, "duplicateOf", "in", pages_left)
        pages_left -= used

        coverage = {"credentials": bound_in + bound_out,
                    "indexes": index_in,
                    "duplicates": dup_out + dup_in}

        attestation_key = identity_key + ".erasure"
        sealed_at = op.submittedAt
        first_for_cycle = True
        # read-posture: (d) same class, same declaration site. A first
        # attestation has none. Re-verifying the SAME cycle preserves the
        # instant the verification first earned; a different cycle -- a
        # re-shred -- restamps, which is what makes an erasure attestable more
        # than once.
        #
        # Read TOMBSTONE-BLIND. No aspect-type DDL can refuse a tombstone (a
        # tombstone carries no document, so step 6 never resolves this class),
        # so any package script can remove this attestation. Recovery is the
        # good part -- the residue lens reopens the gap and this op rewrites it
        # -- but a live-only read would make that recovery silently restamp the
        # erasure date to now, which is the one field here with legal meaning.
        prior = any_data(kv.Read(attestation_key))
        if prior != None and prior.get("sealedForShreddedAt") == shredded_at:
            prior_sealed_at = prior.get("sealedAt")
            if prior_sealed_at != None and type(prior_sealed_at) == type("") and len(prior_sealed_at) > 0:
                sealed_at = prior_sealed_at
                first_for_cycle = False

        mutations = [{"op": "update", "key": attestation_key,
            "document": {"class": "erasure", "vertexKey": identity_key,
                         "localName": "erasure", "isDeleted": False,
                         "data": {"sealedAt": sealed_at,
                                  "sealedForShreddedAt": shredded_at,
                                  "coverage": coverage}}}]

        # ONE completion event per erasure cycle. The op is idempotent and the
        # Weaver re-dispatches a gap it has not yet seen close, so an
        # unconditional emission would announce the same completion repeatedly
        # -- and this event's whole claim is that a person's erasure was
        # verified complete, which happens once per cycle. A re-verification
        # that finds the same cycle already attested rewrites the coverage and
        # stays quiet.
        events = []
        if first_for_cycle:
            events = [{"class": "privacy.erasureCompleted",
                       "data": {"identityKey": identity_key, "sealedAt": sealed_at,
                                "sealedForShreddedAt": shredded_at, "coverage": coverage}}]

        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": identity_key}}

    fail("sealIdentityForErasureComplete DDL: unknown operationType: " + ot)
`

// ErasureCompletedEventDDL returns the event-type DDL declaration for
// privacy.erasureCompleted (Contract #3 §3.4 typed-event model). Registered for
// the reason privacy.keyShredded and privacy.erasureRequested are: it documents
// the schema the erasure plane's downstream readers bind to. This is the one
// event on the plane that says a person's erasure was verified rather than
// merely progressed, and an attestation whose only record is a KV aspect is
// invisible to anything reading the event log.
func ErasureCompletedEventDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName: erasureCompletedEventDDL,
		Class:         "meta.ddl.eventType",
		Description: "Emitted by SealIdentityForErasureComplete (erasure-orchestration-design.md §7.2) " +
			"in the same commit as the attestation it announces — the instant an identity's erasure was " +
			"verified complete by a walk of its own remaining links. Carries the cycle discriminator " +
			"(sealedForShreddedAt) so a consumer can tell which erasure cycle was attested, and the " +
			"coverage counts so a downstream ledger can record what was erased without re-reading the " +
			"aspect.",
		Script: erasureCompletedEventDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"identityKey":{"type":"string","description":"vtx.identity.<NanoID> — the identity whose erasure was attested complete."},` +
			`"sealedAt":{"type":"string","description":"When this erasure cycle was first attested complete."},` +
			`"sealedForShreddedAt":{"type":"string","description":"The piiKey.shreddedAt this attestation covers — the erasure cycle discriminator."},` +
			`"coverage":{"type":"object","description":"Per-class counts of the tombstoned links the verification walked past: credentials, indexes, duplicates."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"identityKey":         "The identity whose erasure was attested complete.",
			"sealedAt":            "When this erasure cycle was first attested complete.",
			"sealedForShreddedAt": "The erasure cycle this attestation covers; a re-shred starts a new one.",
			"coverage":            "What the verification walked past, per class — a floor on what was erased.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "privacy.erasureCompleted",
				Payload: map[string]any{
					"identityKey":         "vtx.identity.<NanoID>",
					"sealedAt":            "2026-08-07T00:00:00Z",
					"sealedForShreddedAt": "2026-08-07T00:00:00Z",
					"coverage":            map[string]any{"credentials": 3, "indexes": 2, "duplicates": 1},
				},
				ExpectedOutcome: "Announces a verified erasure. The identityErasureResidue row stops violating in the same projection that folds the attestation.",
			},
		},
	}
}

// erasureCompletedEventDDLScript is the declaration-only Starlark for the
// erasureCompleted event-type DDL. Events are emitted by a script's `events`
// return list, never dispatched as operations — mirrors
// erasureRequestedEventDDLScript's fail-closed stub.
const erasureCompletedEventDDLScript = `
def execute(state, op):
    fail("event-type DDL: not an operation handler: " + op.operationType)
`
