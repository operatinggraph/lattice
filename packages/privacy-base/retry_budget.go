package privacybase

// maxCredentialResidueRetries, maxDedupResidueRetries and
// maxErasureSealRetries cap how many times the Weaver auto-dispatches an
// identityErasureComplete gap's remediation before it stops and escalates
// (Contract #10 §10.3): identityErasureResidueSpec projects each as the
// constant maxretries_<gap> column on every convergence row, and the Weaver
// bounds its per-(target, entity, gap) dispatch-count in weaver-state against
// that cap. Every one of the three dispatched gaps is a directOp, which the
// engine classifies as external outright, so §10.3 leaves the declared cap as
// the only bound each of them has. retry_budget_pin_test.go re-derives all
// three from the sweep scripts below, so the numbers cannot drift from the ops
// they bound.
//
// THE UNIT OF A CAP IS THE GAP, AND A GAP COVERS EVERY ARM ITS OP SWEEPS. Both
// sweeps drain ONE arm per commit, in a fixed order, moving to the next only
// once the current one returns no live links:
//
//   - UnbindIdentityCredentials sweeps boundTo inbound, then outbound — TWO
//     arms (identity-domain/unbind_identity_credentials.go's
//     collect_live_sweep(subject_key, …) call sites), both folded into
//     missing_credentialResidue.
//   - PurgeIdentityDedupFootprint sweeps indexes inbound, then duplicateOf
//     outbound, then duplicateOf inbound — THREE arms
//     (purge_identity_dedup_footprint.go), all folded into
//     missing_dedupResidue.
//
// Each arm pages independently, and a DRAINED arm is not a stall: its
// collect_live_sweep returns an empty slice the moment the cursor runs out, and
// the op falls through to the next arm. Only a page budget spent entirely on
// tombstones fails ErasureResidueUnreachable. So the dispatches a gap can still
// make progress on is the SUM over its arms, not one arm's share:
//
//	cap = Σ_arms(MAX_PAGES × PAGE_LIMIT) / SWEEP_LIMIT
//
// One arm reads at most MAX_PAGES × PAGE_LIMIT = 64 × 256 = 16384 links
// (MAX_BOUND_TO_PAGES × BOUND_TO_PAGE_LIMIT in the credential sweep,
// MAX_LINK_PAGES × LINK_PAGE_LIMIT in the dedup one) and each commit tombstones
// at most SWEEP_LIMIT = 64 live ones, so an arm takes 16384 / 64 = 256
// dispatches to drain the widest fan-out it can even reach. Hence 2 × 256 = 512
// for the credential gap and 3 × 256 = 768 for the dedup gap: the point past
// which every arm has exhausted its reach and a further dispatch cannot be
// progress. A real subject's credential and dedup fan-out is single- or
// double-digit, so both caps sit ~3 orders of magnitude above any plausible
// person and are only ever reached by a sweep that is not draining.
//
// maxErasureSealRetries = 768 IS A JUDGEMENT, sized to the widest sibling. The
// terminal gap does not page — SealIdentityForErasureComplete re-verifies the
// five residue arms and both async halves and writes one attestation inside a
// single commit (targets.go's gap table) — so it has no reach of its own to
// derive a cap from, and the sizing question is the cost of being WRONG rather
// than a ceiling. Exhaustion is not self-clearing (targets.go says what that
// costs and how an operator recovers), so a short fuse would permanently park a
// live erasure over a merely transient cause: at the ~30-minute reconcile
// cadence a Vault or projection lag burns a 16-retry budget in about eight
// hours. Matching the widest sibling means no gap on this target can park while
// another one could still legitimately be converging.
//
// A usable cap also means these gaps are not the uncapped-external shape the
// reconciler paces with an exponential reclaim backoff
// (internal/weaver/reconciler.go): a paging sweep reclaiming at mark-lease
// expiry is the sweep doing its work, and the budget above is what bounds it.
const (
	maxCredentialResidueRetries = 512
	maxDedupResidueRetries      = 768
	maxErasureSealRetries       = 768
)
