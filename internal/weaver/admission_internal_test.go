package weaver

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestTokenBucket_NoContentionAdmitsImmediately proves an uncontended bucket
// (plenty of tokens, one id at a time) never defers — the common case for any
// declared budget that comfortably covers steady-state volume.
func TestTokenBucket_NoContentionAdmitsImmediately(t *testing.T) {
	t.Parallel()
	b := newTokenBucket(100)
	now := time.Now()
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("row-%d", i)
		if !b.admit(id, 0, now) {
			t.Fatalf("admit(%s) = false, want true (bucket has ample headroom)", id)
		}
	}
}

// TestTokenBucket_PacesOverTime proves a rate-1 bucket admits its first
// caller immediately (the starts-full burst) and defers a second concurrent
// caller until a full second elapses, then admits it — the defining
// "paced, not instant" property Fire 8 exists for.
func TestTokenBucket_PacesOverTime(t *testing.T) {
	t.Parallel()
	b := newTokenBucket(1)
	now := time.Now()

	if !b.admit("first", 0, now) {
		t.Fatalf("first admit should consume the starting token")
	}
	if b.admit("second", 0, now) {
		t.Fatalf("second admit at the SAME instant should be deferred (no tokens left)")
	}
	if b.admit("second", 0, now.Add(500*time.Millisecond)) {
		t.Fatalf("second admit at +500ms should still be deferred (half a token accrued)")
	}
	if !b.admit("second", 0, now.Add(time.Second)) {
		t.Fatalf("second admit at +1s should be admitted (a full token has accrued)")
	}
}

// TestTokenBucket_PriorityOrderedUnderContention is the core Fire-8 proof: a
// low-priority id calling admit() during its OWN redelivery must not jump a
// higher-priority id already waiting for the same scarce token — the token
// goes to the higher priority, reserved (granted) for it to collect on its
// own next call.
func TestTokenBucket_PriorityOrderedUnderContention(t *testing.T) {
	t.Parallel()
	b := newTokenBucket(2)
	now := time.Now()

	if !b.admit("a", 0, now) {
		t.Fatalf("a should consume the first starting token")
	}
	if !b.admit("b", 0, now) {
		t.Fatalf("b should consume the second starting token")
	}
	// Tokens now exhausted; three more ids queue up, none admitted.
	if b.admit("low", 1, now) {
		t.Fatalf("low should be deferred (no tokens left)")
	}
	if b.admit("high", 10, now) {
		t.Fatalf("high should be deferred (no tokens left)")
	}
	if b.admit("mid", 5, now) {
		t.Fatalf("mid should be deferred (no tokens left)")
	}

	// Exactly one token accrues. The low-priority id's OWN retry must not win
	// it — "high" (priority 10) must be served first, even though "low" is
	// the caller asking right now.
	later := now.Add(500 * time.Millisecond) // rate=2/s * 0.5s = 1 token
	if b.admit("low", 1, later) {
		t.Fatalf("low's own retry must not consume the token ahead of a higher-priority waiter")
	}
	// "high" collects its reserved grant on its own next call, without
	// re-consuming a fresh token.
	if !b.admit("high", 10, later) {
		t.Fatalf("high should have been granted the token by low's draining call")
	}
	// "mid" (priority 5) is still waiting — no new tokens have accrued.
	if b.admit("mid", 5, later) {
		t.Fatalf("mid should still be deferred (no further tokens accrued)")
	}
	// One more token accrues; "mid" (the highest remaining priority) wins it
	// over "low".
	evenLater := later.Add(500 * time.Millisecond)
	if b.admit("low", 1, evenLater) {
		t.Fatalf("low must still lose to mid's higher priority")
	}
	if !b.admit("mid", 5, evenLater) {
		t.Fatalf("mid should have been granted the token")
	}
}

// TestTokenBucket_GrantExpiryReclaimsWastedToken proves a token reserved
// (granted) for an id that never returns to collect it — e.g. its gap closed
// and it is never redelivered again — is refunded to the bucket rather than
// wasted forever.
func TestTokenBucket_GrantExpiryReclaimsWastedToken(t *testing.T) {
	t.Parallel()
	b := newTokenBucket(1)
	now := time.Now()

	if !b.admit("winner", 0, now) {
		t.Fatalf("winner should consume the starting token")
	}
	if b.admit("abandoned", 0, now) {
		t.Fatalf("abandoned should be deferred")
	}
	// A neutral third id's sweep, one accrued token later, drains the queue
	// and grants that token to "abandoned" (priority 0, the only other
	// pending id) — but "abandoned" never calls admit() again to collect it.
	if b.admit("third", 0, now.Add(time.Second)) {
		t.Fatalf("third arrives after abandoned and must not jump the earlier-queued id")
	}
	if _, ok := b.granted["abandoned"]; !ok {
		t.Fatalf("abandoned should hold the reserved grant after third's sweep drained it")
	}

	// abandoned never returns. Past the TTL, the grant must be reclaimed and
	// its token refunded — to "third", the id that has been waiting in the
	// queue the longest, not squandered on some brand-new caller that jumps
	// the line.
	past := now.Add(time.Second).Add(admissionGrantTTL).Add(time.Second)
	if !b.admit("third", 0, past) {
		t.Fatalf("third (already queued since before the grant expired) should collect the reclaimed token")
	}
	if _, ok := b.granted["abandoned"]; ok {
		t.Fatalf("abandoned's stale grant should have been reclaimed, not left dangling")
	}
}

// TestTokenBucket_EvictOverflow proves a pending queue past
// admissionPendingCap sheds its lowest-priority, oldest-among-ties entries
// rather than growing unbounded.
func TestTokenBucket_EvictOverflow(t *testing.T) {
	t.Parallel()
	b := newTokenBucket(1)
	now := time.Now()
	for i := 0; i < admissionPendingCap+1; i++ {
		b.pending = append(b.pending, pendingAdmission{
			id: fmt.Sprintf("row-%d", i), priority: i, since: now,
		})
	}
	b.evictOverflow()
	if len(b.pending) != admissionPendingCap {
		t.Fatalf("pending len after eviction = %d, want %d", len(b.pending), admissionPendingCap)
	}
	for _, p := range b.pending {
		if p.priority == 0 {
			t.Fatalf("the lowest-priority entry (priority 0) should have been evicted")
		}
	}
}

// TestTokenBucket_BurstFixture_PacedAndPriorityOrdered is the Fire-8
// acceptance fixture (design weaver-planner-mandate-design.md §8, decomposition
// table row 8: "3k-row fixture paced + priority-ordered"). 3000 synthetic gap
// dispatches contend for a 50/sec budget: the initial burst (up to capacity)
// admits from free headroom, and every subsequent wave — as tokens accrue one
// tick at a time — serves the highest still-outstanding priority first,
// draining the whole backlog only after the rate-bounded number of ticks.
func TestTokenBucket_BurstFixture_PacedAndPriorityOrdered(t *testing.T) {
	t.Parallel()
	const totalRows = 3000
	const rate = 50.0
	b := newTokenBucket(rate)
	now := time.Now()

	priority := make(map[string]int, totalRows)
	ids := make([]string, totalRows)
	for i := 0; i < totalRows; i++ {
		id := fmt.Sprintf("row-%d", i)
		ids[i] = id
		priority[id] = i % 5
	}

	admittedAt := make(map[string]int, totalRows) // id -> tick admitted (0 = initial burst)
	for _, id := range ids {
		if b.admit(id, priority[id], now) {
			admittedAt[id] = 0
		}
	}
	if len(admittedAt) != int(rate) {
		t.Fatalf("initial burst admitted %d ids, want %d (the starting capacity)", len(admittedAt), int(rate))
	}
	if got := len(b.pending); got != totalRows-int(rate) {
		t.Fatalf("pending backlog = %d, want %d", got, totalRows-int(rate))
	}

	tick := 0
	for len(admittedAt) < totalRows {
		tick++
		if tick > totalRows {
			t.Fatalf("backlog never fully drained")
		}
		now = now.Add(time.Second)
		served := make(map[string]bool)
		for _, id := range ids {
			if _, done := admittedAt[id]; done {
				continue
			}
			if b.admit(id, priority[id], now) {
				admittedAt[id] = tick
				served[id] = true
			}
		}
		if len(served) == 0 {
			t.Fatalf("tick %d served nobody — backlog stuck", tick)
		}
		minServedPriority := 999
		for id := range served {
			if priority[id] < minServedPriority {
				minServedPriority = priority[id]
			}
		}
		for _, id := range ids {
			if _, done := admittedAt[id]; done {
				continue
			}
			if priority[id] > minServedPriority {
				t.Fatalf("tick %d: %s (priority %d) still pending while priority %d was served this tick — not priority-ordered",
					tick, id, priority[id], minServedPriority)
			}
		}
	}

	wantMinTicks := int((totalRows - rate) / rate)
	if tick < wantMinTicks {
		t.Fatalf("backlog drained in %d ticks, want >= %d — draining faster than the declared rate allows", tick, wantMinTicks)
	}
}

// TestAdmissionScheduler_NilPolicyAlwaysAdmits proves the byte-identical
// default-off path: a target with no admission block never touches a bucket.
func TestAdmissionScheduler_NilPolicyAlwaysAdmits(t *testing.T) {
	t.Parallel()
	a := newAdmissionScheduler()
	now := time.Now()
	for i := 0; i < 1000; i++ {
		if !a.admit(nil, "t1", fmt.Sprintf("row-%d", i), "", 0, now) {
			t.Fatalf("row %d denied with no policy declared", i)
		}
	}
	if admitted, deferred := a.metrics(); admitted != 0 || deferred != 0 {
		t.Fatalf("metrics = (%d, %d), want (0, 0) — a nil policy must never touch the bucket layer", admitted, deferred)
	}
}

// TestAdmissionScheduler_AdapterRateTakesPrecedenceOverGlobal proves the
// GapAction-adapter-selection precedent (explicit > general): a gap whose
// resolved action declares an adapter with its OWN configured rate is governed
// by that rate, independent of (and not consuming from) the target's global
// bucket.
func TestAdmissionScheduler_AdapterRateTakesPrecedenceOverGlobal(t *testing.T) {
	t.Parallel()
	a := newAdmissionScheduler()
	policy := &AdmissionPolicy{GlobalRate: 1, AdapterRates: map[string]float64{"bridge-email": 1}}
	now := time.Now()

	if !a.admit(policy, "t1", "row-1", "bridge-email", 0, now) {
		t.Fatalf("first bridge-email dispatch should be admitted")
	}
	// The GLOBAL bucket is untouched by the adapter-governed dispatch above —
	// a fresh global-only request must still find its own full starting
	// token.
	if !a.admit(policy, "t1", "row-2", "", 0, now) {
		t.Fatalf("a gap with no adapter should draw from the still-full global bucket, not the adapter's")
	}
	// A second bridge-email dispatch at the same instant exhausts ITS OWN
	// bucket (independent of the global bucket, already spent by row-2).
	if a.admit(policy, "t1", "row-3", "bridge-email", 0, now) {
		t.Fatalf("a second bridge-email dispatch at the same instant should be deferred (its own budget is spent)")
	}
}

func TestValidateAdmissionPolicy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		policy  *AdmissionPolicy
		wantErr bool
	}{
		{"nil is valid (unbounded default)", nil, false},
		{"positive global rate", &AdmissionPolicy{GlobalRate: 10}, false},
		{"positive adapter rate", &AdmissionPolicy{AdapterRates: map[string]float64{"x": 5}}, false},
		{"negative global rate rejected", &AdmissionPolicy{GlobalRate: -1}, true},
		{"empty block rejected (declares nothing)", &AdmissionPolicy{}, true},
		{"zero adapter rate rejected", &AdmissionPolicy{AdapterRates: map[string]float64{"x": 0}}, true},
		{"negative adapter rate rejected", &AdmissionPolicy{AdapterRates: map[string]float64{"x": -5}}, true},
		{"empty adapter key rejected", &AdmissionPolicy{AdapterRates: map[string]float64{"": 5}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateAdmissionPolicy(tc.policy)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateAdmissionPolicy(%+v) err = %v, wantErr %v", tc.policy, err, tc.wantErr)
			}
		})
	}
}

// TestPlanGap_AdmissionControl proves the evaluator wiring end to end at the
// planGap seam (shared by lane-1 dispatch and the reconciler reclaim): no
// Admission block dispatches every call; a declared budget defers past its
// capacity via NakWithDelay, with no plan and no mark-worthy side effect.
func TestPlanGap_AdmissionControl(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	newBareEngine := func() *Engine {
		return &Engine{issues: newIssueCache(), admission: newAdmissionScheduler(), logger: slog.Default()}
	}
	ga := GapAction{Action: actionDirectOp, Operation: "SomeOp", Params: map[string]string{"x": "1"}}
	row := map[string]any{"entityKey": "vtx.foo.abc"}

	t.Run("no admission policy dispatches every time", func(t *testing.T) {
		t.Parallel()
		e := newBareEngine()
		target := &Target{TargetID: "t1", Gaps: map[string]GapAction{"missing_x": ga}}
		for i := 0; i < 5; i++ {
			pl, action, dec := e.planGap(ctx, target, "t1", fmt.Sprintf("entity-%d", i), "missing_x", ga, row, 1, "")
			if dec != substrate.Ack || pl == nil || action != actionDirectOp {
				t.Fatalf("call %d: got (%v, %q, %v), want (non-nil plan, %q, Ack)", i, pl, action, dec, actionDirectOp)
			}
		}
	})

	t.Run("declared budget defers past capacity", func(t *testing.T) {
		t.Parallel()
		e := newBareEngine()
		target := &Target{TargetID: "t2", Gaps: map[string]GapAction{"missing_x": ga},
			Admission: &AdmissionPolicy{GlobalRate: 1}}
		pl1, _, dec1 := e.planGap(ctx, target, "t2", "entityA", "missing_x", ga, row, 1, "")
		if dec1 != substrate.Ack || pl1 == nil {
			t.Fatalf("first dispatch should be admitted (fresh capacity): dec=%v pl=%v", dec1, pl1)
		}
		pl2, _, dec2 := e.planGap(ctx, target, "t2", "entityB", "missing_x", ga, row, 1, "")
		if dec2 != substrate.NakWithDelay || pl2 != nil {
			t.Fatalf("second dispatch should be deferred (budget exhausted): dec=%v pl=%v", dec2, pl2)
		}
		// Ordinary pacing is not a fault: it must never raise a Health issue.
		if issues := e.issues.snapshot(); len(issues) != 0 {
			t.Fatalf("admission deferral raised issues %+v, want none (this is routine pacing, not an alert)", issues)
		}
	})
}
