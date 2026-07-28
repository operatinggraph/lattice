package processor

import "testing"

// TestLiveReadBudgetTracker_ChargeWithinBudget — spends stay reported "ok" up
// to and including the exact budget.
func TestLiveReadBudgetTracker_ChargeWithinBudget(t *testing.T) {
	tr := &liveReadBudgetTracker{budget: 10}
	for i := 0; i < 5; i++ {
		if !tr.charge(2) {
			t.Fatalf("charge %d: got false, want true (spent=%d budget=%d)", i, tr.spent, tr.budget)
		}
	}
	if tr.spent != 10 {
		t.Fatalf("spent = %d, want 10", tr.spent)
	}
}

// TestLiveReadBudgetTracker_ChargeOverBudget — a charge that pushes spent past
// budget reports false, and the breach is sticky (a later charge, even of 0,
// still reports false).
func TestLiveReadBudgetTracker_ChargeOverBudget(t *testing.T) {
	tr := &liveReadBudgetTracker{budget: 5}
	if !tr.charge(5) {
		t.Fatalf("charge(5) at budget=5: got false, want true")
	}
	if tr.charge(1) {
		t.Fatalf("charge(1) over budget: got true, want false")
	}
	if tr.charge(0) {
		t.Fatalf("charge(0) after breach: got true, want false (breach is sticky)")
	}
}

// TestLiveReadBudgetTracker_NilIsUnlimited — a nil tracker (a test harness
// that never wires one) never trips, so pre-existing tests built without this
// field are unaffected.
func TestLiveReadBudgetTracker_NilIsUnlimited(t *testing.T) {
	var tr *liveReadBudgetTracker
	if !tr.charge(1_000_000) {
		t.Fatalf("nil tracker: charge got false, want true (unlimited)")
	}
}

// TestDefaultLiveReadBudget_CoversMergeIdentityWorstCase pins the arithmetic
// live_read_budget.go's doc comment claims for the platform's own worst case
// (packages/identity-hygiene/ddls.go's MergeIdentity), so a future edit to
// either the identity-hygiene page-size constants or DefaultLiveReadBudget
// that breaks the "known worst case fits the default" claim fails loudly here
// instead of only at runtime against a real large MergeIdentity.
func TestDefaultLiveReadBudget_CoversMergeIdentityWorstCase(t *testing.T) {
	const (
		taskPages   = 64  // identity-hygiene MAX_IDENTITY_TASK_PAGES
		taskLimit   = 256 // identity-hygiene IDENTITY_TASK_PAGE_LIMIT
		indexPages  = 64  // identity-hygiene MAX_INDEXES_PAGES
		indexLimit  = 256 // identity-hygiene INDEXES_PAGE_LIMIT
		maxTotalMut = 999 // identity-hygiene's total_muts > 999 pre-flight
	)
	// identity_has_open_tasks: each page charges (1 list + limit), plus one
	// kv.Read follow-up per link in the page.
	openTasks := taskPages*(1+taskLimit) + taskPages*taskLimit
	// collect_indexes_repoints: each page charges (1 list + limit); no
	// follow-up inside the enumeration itself.
	indexRepoints := indexPages * (1 + indexLimit)
	// idx_repoints post-loop (2 kv.Read per candidate) is bounded by the
	// total_muts pre-flight, not by the page caps above: the script fails
	// before this loop runs once len(idx_repoints)*3 alone would exceed
	// maxTotalMut, so at most maxTotalMut/3 candidates ever reach it.
	idxRepointsFollowup := (maxTotalMut / 3) * 2

	worstCase := openTasks + indexRepoints + idxRepointsFollowup
	if worstCase > DefaultLiveReadBudget {
		t.Fatalf("MergeIdentity worst-case live-read cost %d exceeds DefaultLiveReadBudget %d — a legitimate merge at full fan-out would now be rejected", worstCase, DefaultLiveReadBudget)
	}
}
