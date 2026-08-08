package privacybase

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// LoomPatterns returns the package's meta.loomPattern declarations (Contract
// #10 §10.5) — the erasure spine (erasure-orchestration-design.md §5).
//
// identityErasure is the first shipped pattern built entirely from systemOp
// steps, and the first whose steps declare their own read-sets. Every other
// pattern in the corpus (packages/lease-signing, packages/capability-author)
// hands work to an async completer — a person or the bridge — and waits. This
// one performs its own, because nothing about erasing a person is waiting on
// anybody.
//
// # Why Loom owns the order
//
// The ordering is a legal obligation, not a convenience. Key destruction is the
// primary duty and is initiated first; the write path closes second, which is
// what makes the residue set monotonically non-increasing and so makes a
// residue count provable rather than point-in-time; structural cleanup follows.
// A listener chain has an order too, but it is implicit in wiring and invisible
// to an auditor, where a pattern's step list is a declared, readable one with a
// durable cursor behind it.
//
// # Why the spine is an accelerator for its tail, and where it is not
//
// Once the marker is written, a terminally failed instance does not strand an
// erasure. If step 3 dies, the identityErasureResidue lens still shows
// credential residue, the identityErasureComplete target still dispatches
// UnbindIdentityCredentials every reconcile until it reaches zero, and
// SealIdentityForErasureComplete still refuses to attest. From step 2 onward a
// dead spine degrades an erasure from prompt to eventual, never from complete to
// incomplete (§5.5).
//
// That property does NOT extend backwards over steps 1 and 2, and the difference
// matters more than the property. The convergent tail is anchored on the marker
// step 2 writes — the residue lens's own WHERE clause is
// erasureRequested.requestedAt <> null — so an instance that dies at step 1 or 2
// leaves no row, no dispatch, and no tail at all. There is exactly one live way
// to reach that today: step 1's op still carries its own unbounded in-commit
// cascade and refuses ShredBatchTooLarge above 999 mutations, so a
// well-connected person's erasure fails at cursor 0 — the very subject the paged
// sweeps at steps 3 and 4 exist to serve. Retiring that refusal is the narrowing
// of ShredIdentityKey (design §12 Fire B, build-order step 3), and until it
// lands the failed instance is the only signal.
//
// # What a COMPLETED instance does and does not mean
//
// Steps 3 and 4 sweep one bounded page each and are guardless, so the instance
// completes after one pass of each — for a wide subject, with residue still
// outstanding. `loom.patternCompleted` therefore means the spine ran, not that
// the person is erased. The residue row and the attestation are what say that.
//
// # completionDomains
//
// Two domains, not §5.1's one. A systemOp advances on its bound op's own domain
// event, correlated on the requestId the submit carried (Fire A's §5.3 probe
// traced it end to end): steps 1 and 2 emit privacy.*, step 3 emits
// identity.unbound, step 4 emits privacy.dedupFootprintSwept. A pattern mixing
// domains lists every one it completes on (Contract #10 §10.5), and a domain
// left off does not wedge the instance — the step rides its deadline into the
// op-status probe and advances with a WARN — but it does mean every erasure
// paying a StepTimeout it has no reason to.
//
// # Guards
//
// Step 1 skips an identity already shredded; step 2 skips one already sealed.
// Both read subject.<aspect>.data.<field>, the only shape the guard grammar
// admits. Steps 3 and 4 are deliberately GUARDLESS: their idempotence is
// by-tombstone, and the residue they exist to drain lives in the LINK plane,
// which a guard cannot see at all — a guard there would be theatre. The cost of
// a guardless step is one bounded re-run on total loom-state loss, which for an
// already-idempotent tombstone sweep is exactly the right trade.
//
// # Reads
//
// Every step declares the bare `subject` token, because every one of the four
// scripts runs vertex_alive(state, subjectKey) and an absent subject is a
// correctness error, not a branch. The optionalReads are derived from what each
// script actually reads that tolerates absence — and step 2's
// subject.mergedInto is the one that is load-bearing rather than hygienic: that
// script reads it from `state`, where an UNDECLARED key and an ABSENT one are
// both None, so leaving it off would silently disarm the IdentityMerged refusal
// and let the seal anchor an erasure on a merged-away identity whose residue is
// zero by construction. The class-(e) kv.Links walks in steps 1, 3 and 4 stay
// undeclarable — a step cannot express an enumeration.
func LoomPatterns() []pkgmgr.LoomPatternSpec {
	return []pkgmgr.LoomPatternSpec{
		{
			PatternID:         "identityErasure",
			SubjectType:       "identity",
			CompletionDomains: []string{"privacy", "identity"},
			Steps: []pkgmgr.StepSpec{
				{
					Kind:      "systemOp",
					Operation: "ShredIdentityKey",
					// Two conjuncts, and each exists because of a state the
					// other one gets wrong.
					//
					// A merged-away identity must never reach this step. Its
					// credentials and indexes already moved to the survivor, so
					// SealIdentityForErasure refuses it (IdentityMerged) — but
					// the seal is step 2, and by then this step has already
					// destroyed a key irreversibly for a subject the design says
					// to refuse outright. Skipping here lets the instance fail
					// at the seal, loudly, with nothing burned.
					//
					// The shreddedAt disjunct covers an envelope shredded before
					// the finalization-cycle change, which carries shredded=true
					// and no stamp. Guarding on `shredded` alone skips this step
					// forever for that identity, and the seal then refuses
					// ErasureNotShredded naming the exact remedy the guard
					// forbids — "re-run ShredIdentityKey to restamp it". A
					// re-shred is idempotent, so running it is free and is what
					// unwedges the erasure.
					//
					// `equals` is false for an absent path and `not` inverts it,
					// so an identity that never received a sensitive write — no
					// piiKey aspect at all — still RUNS: that is what writes the
					// durable placeholder that makes the shred survive a
					// Processor restart.
					Guard: map[string]any{"allOf": []any{
						map[string]any{"absent": "subject.mergedInto.data.value"},
						map[string]any{"anyOf": []any{
							map[string]any{"not": map[string]any{"equals": map[string]any{"path": "subject.piiKey.data.shredded", "value": true}}},
							map[string]any{"absent": "subject.piiKey.data.shreddedAt"},
						}},
					}},
					// No subject.mergedInto here even though the guard names it:
					// a guard is evaluated by the engine against Core KV
					// directly (evalGuard), not out of the op's hydrated state,
					// so declaring it would buy the script a KVGet it never
					// reads. Step 2's declaration of the same aspect is a
					// different thing entirely — that script does read it.
					Reads:         []string{"subject"},
					OptionalReads: []string{"subject.piiKey"},
				},
				{
					Kind:      "systemOp",
					Operation: "SealIdentityForErasure",
					// Skip when the marker is already written. A re-triggered
					// erasure of an already-sealed identity leaves the marker
					// naming the cycle the REQUEST was sealed against, which is
					// deliberate: the completeness test the residue lens applies
					// is piiKey.shreddedAt against the attestation's
					// sealedForShreddedAt — the live envelope, which cannot go
					// stale — and never the marker.
					Guard:         map[string]any{"absent": "subject.erasureRequested.data.requestedAt"},
					Reads:         []string{"subject"},
					OptionalReads: []string{"subject.mergedInto", "subject.piiKey", "subject.erasureRequested"},
				},
				{
					Kind:          "systemOp",
					Operation:     "UnbindIdentityCredentials",
					Reads:         []string{"subject"},
					OptionalReads: []string{"subject.erasureRequested"},
				},
				{
					Kind:          "systemOp",
					Operation:     "PurgeIdentityDedupFootprint",
					Reads:         []string{"subject"},
					OptionalReads: []string{"subject.erasureRequested"},
				},
			},
		},
	}
}
