package processor

// DefaultLiveReadBudget bounds the total live Core KV round trips one script
// execution may issue via kv.Read's lazy fallthrough (one GET) and kv.Links
// (one list call + one GET per page slot, charged at the clamped `limit` —
// see starlark_kv.go), when step 4 does not override it. The declared-read
// ceiling (opwire.MaxDeclaredReads) bounds contextHint's pre-fetch only —
// §2.5 "What the ceiling does not cover" explicitly disclaims class-(e) live
// reads, which were otherwise unbounded at the Processor.
//
// Sized with headroom above the platform's own worst case, MergeIdentity
// (packages/identity-hygiene/ddls.go), which runs three enumeration/follow-up
// passes in one execution:
//   - identity_has_open_tasks: 64 pages of 256 links, each page charging
//     1+256, plus one kv.Read follow-up per non-deleted link:
//     64*(1+256) + 64*256 = 32,832
//   - collect_indexes_repoints: 64 pages of 256 links, no follow-up inside
//     the enumeration itself: 64*(1+256) = 16,448
//   - the idx_repoints post-loop's 2 kv.Read calls per candidate is NOT
//     bounded by the same page caps — it is bounded by MergeIdentity's own
//     `total_muts > 999` pre-flight (ddls.go), which fails the script before
//     this loop runs once len(idx_repoints)*3 alone would exceed 999, i.e.
//     at most ~333 candidates * 2 = 666
//
// Total worst case ≈ 49,946 — comfortably under the 60,000 default.
const DefaultLiveReadBudget = 60_000

// liveReadBudgetTracker bounds the total live Core KV round trips one script
// execution issues, shared by pointer across the kv builtins for one
// execution (like deferredMissTracker / sensitiveReadTracker).
type liveReadBudgetTracker struct {
	budget int
	spent  int
}

// charge records n more live Core KV round trips and reports whether the
// execution is still within budget. Nil-safe (reports unlimited) so a test
// harness that builds a ScriptContext without wiring one behaves as it did
// before this budget existed; production wiring (step4_hydrate.go) and the
// runner's own default (starlark_runner.go) never leave it nil.
func (t *liveReadBudgetTracker) charge(n int) bool {
	if t == nil {
		return true
	}
	t.spent += n
	return t.spent <= t.budget
}
