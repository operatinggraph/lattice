package weaver

import (
	"context"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// TestBackoffInterval pins the collapse-only reclaim backoff curve: count 0 and 1
// both return the base (so the FIRST reclaim fires at lease-expiry), each higher
// count doubles, and the whole curve is clamped at the cap and never decreases.
func TestBackoffInterval(t *testing.T) {
	t.Parallel()
	s := &sweeper{backoffBase: time.Minute, backoffCap: 8 * time.Minute}

	cases := []struct {
		count int
		want  time.Duration
	}{
		{count: -1, want: time.Minute},    // defensive: below 1 is the base
		{count: 0, want: time.Minute},     // first reclaim → base (fires at lease-expiry)
		{count: 1, want: time.Minute},     // second reclaim → base
		{count: 2, want: 2 * time.Minute}, // then exponential
		{count: 3, want: 4 * time.Minute},
		{count: 4, want: 8 * time.Minute},   // hits the cap
		{count: 5, want: 8 * time.Minute},   // clamped
		{count: 100, want: 8 * time.Minute}, // clamped, no overflow
	}
	for _, c := range cases {
		if got := s.backoffInterval(c.count); got != c.want {
			t.Fatalf("backoffInterval(%d) = %s, want %s", c.count, got, c.want)
		}
	}

	// Monotonic non-decreasing across a wide range.
	prev := s.backoffInterval(0)
	for n := 1; n <= 64; n++ {
		cur := s.backoffInterval(n)
		if cur < prev {
			t.Fatalf("backoffInterval not monotonic: n=%d gave %s after %s", n, cur, prev)
		}
		if cur > s.backoffCap {
			t.Fatalf("backoffInterval(%d) = %s exceeds cap %s", n, cur, s.backoffCap)
		}
		prev = cur
	}
}

// TestSweep_ReclaimBackoff_SuppressesRecentUserTask proves the phantom-churn
// suppression: an expired-lease userTask mark (triggerLoom) whose episode was
// dispatched recently (ClaimedAt within the backoff interval) is NOT reclaimed —
// no op fires, no replace, the mark stands at its original revision, and the
// reclaimsSuppressed counter records the pacing.
func TestSweep_ReclaimBackoff_SuppressesRecentUserTask(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// A 1h base makes "dispatched moments ago" deterministically inside the
	// backoff window regardless of test-host speed.
	h := newSweepHarness(t, ctx, func(c *Config) { c.ReclaimBackoffBase = time.Hour })

	const targetID = "fixtureBackoffSuppress"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionTriggerLoom, Pattern: "ghostFlow", Subject: "row.entityKey"}},
	})
	h.engine.source.mu.Lock()
	h.engine.source.patternMeta["ghostFlow"] = "vtx.meta." + testNanoID(t)
	h.engine.source.mu.Unlock()

	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	m := fixtureMark(targetID, entityID, "missing_x", "triggerLoom", pastLease())
	m.ClaimedAt = substrate.FormatTimestamp(time.Now()) // dispatched just now
	rev := h.putMark(t, ctx, key, m)
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
	})

	h.pass(ctx)

	h.requireNoOp(t)
	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil || entry.Revision != rev {
		t.Fatalf("a backed-off reclaim must leave the mark untouched (rev %d, err=%v)", rev, err)
	}
	reclaims, suppressed, _, _, _ := h.engine.sweep.metrics()
	if reclaims != 0 || suppressed != 1 {
		t.Fatalf("metrics: reclaims=%d suppressed=%d, want 0, 1", reclaims, suppressed)
	}
}

// TestSweep_ReclaimBackoff_FiresWhenAged proves the pacing only delays: once the
// userTask episode's ClaimedAt ages past the backoff interval, the very next
// sweep reclaims and re-dispatches exactly as the directOp path always has.
func TestSweep_ReclaimBackoff_FiresWhenAged(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx, func(c *Config) { c.ReclaimBackoffBase = time.Hour })

	const targetID = "fixtureBackoffAged"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionTriggerLoom, Pattern: "ghostFlow", Subject: "row.entityKey"}},
	})
	h.engine.source.mu.Lock()
	h.engine.source.patternMeta["ghostFlow"] = "vtx.meta." + testNanoID(t)
	h.engine.source.mu.Unlock()

	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	// fixtureMark stamps ClaimedAt at now-2h — past the 1h base for count 0.
	rev := h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", "triggerLoom", pastLease()))
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
	})

	h.pass(ctx)

	h.nextOp(t) // the aged userTask episode reclaims
	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil || entry.Revision == rev {
		t.Fatalf("an aged userTask reclaim must re-arm the mark with a fresh revision (old %d, err=%v)", rev, err)
	}
	reclaims, suppressed, _, _, _ := h.engine.sweep.metrics()
	if reclaims != 1 || suppressed != 0 {
		t.Fatalf("metrics: reclaims=%d suppressed=%d, want 1, 0", reclaims, suppressed)
	}
}

// TestSweep_ReclaimBackoff_DirectOpNeverSuppressed proves the action gate: a
// directOp reclaim — where re-dispatch IS the intended bounded retry — fires
// even when its episode was dispatched moments ago, so the backoff never slows
// the external-retry path.
func TestSweep_ReclaimBackoff_DirectOpNeverSuppressed(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx, func(c *Config) { c.ReclaimBackoffBase = time.Hour })

	const targetID = "fixtureBackoffDirectOp"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	m := fixtureMark(targetID, entityID, "missing_x", "directOp", pastLease())
	m.ClaimedAt = substrate.FormatTimestamp(time.Now()) // dispatched just now
	h.putMark(t, ctx, key, m)
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
	})

	h.pass(ctx)

	h.nextOp(t) // directOp re-dispatches despite the recent ClaimedAt
	reclaims, suppressed, _, _, _ := h.engine.sweep.metrics()
	if reclaims != 1 || suppressed != 0 {
		t.Fatalf("metrics: reclaims=%d suppressed=%d, want 1, 0 (directOp must never back off)", reclaims, suppressed)
	}
}

// TestSweep_ReclaimBackoff_ProposedOpSuppressed proves the Fire 2b fix (a
// review-caught gap): the Augur's augurDispatch target's mark always records
// the OUTER static playbook action ("proposedOp"), never the inner
// materialised action (here directOp) — so the backoff gate must key on
// "proposedOp" itself, not on the inner action, or a stuck dispatched-flip
// would reclaim unpaced every mark-lease forever instead of backing off
// (design augur-dispatch-pickup §3.4's explicit "paced by the existing
// backoffInterval" claim).
func TestSweep_ReclaimBackoff_ProposedOpSuppressed(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx, func(c *Config) { c.ReclaimBackoffBase = time.Hour })

	const targetID = "augurDispatch"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_dispatch": {Action: actionProposedOp}},
	})

	handle := testNanoID(t)
	key := markKey(targetID, handle, "missing_dispatch")
	// The mark's recorded Action is the OUTER "proposedOp" literal — even
	// though the row below materialises to an inner directOp.
	m := fixtureMark(targetID, handle, "missing_dispatch", actionProposedOp, pastLease())
	m.ClaimedAt = substrate.FormatTimestamp(time.Now()) // dispatched just now
	rev := h.putMark(t, ctx, key, m)
	h.putRow(t, ctx, targetID, handle, dispatchRow(dpCandidate, "vtx.meta.SomeTargetHJKMNPQRS1", "directOp", map[string]any{
		"operation": "SetAvailability",
		"target":    dpCandidate,
		"params":    map[string]any{"identity": dpCandidate, "available": true},
	}))

	h.pass(ctx)

	h.requireNoOp(t)
	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil || entry.Revision != rev {
		t.Fatalf("a backed-off proposedOp reclaim must leave the mark untouched (rev %d, err=%v)", rev, err)
	}
	reclaims, suppressed, _, _, _ := h.engine.sweep.metrics()
	if reclaims != 0 || suppressed != 1 {
		t.Fatalf("metrics: reclaims=%d suppressed=%d, want 0, 1 (proposedOp must back off like a userTask reclaim)", reclaims, suppressed)
	}
}

// TestSweep_ReclaimBackoff_MarkTTLOutlastsBackoff proves the survival fix: when a
// userTask reclaim paces the next attempt far past the default TTL backstop, the
// re-armed mark's per-key TTL is widened to outlast that backoff window — so the
// mark is always reclaimed before it can TTL-expire into a markless open gap
// (which a CDC redelivery would re-dispatch with a fresh claimId, minting a
// duplicate task).
func TestSweep_ReclaimBackoff_MarkTTLOutlastsBackoff(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx, func(c *Config) { c.ReclaimBackoffBase = time.Hour })

	const targetID = "fixtureBackoffTTL"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionTriggerLoom, Pattern: "ghostFlow", Subject: "row.entityKey"}},
	})
	h.engine.source.mu.Lock()
	h.engine.source.patternMeta["ghostFlow"] = "vtx.meta." + testNanoID(t)
	h.engine.source.mu.Unlock()

	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	// Seed the dispatch-count to 3, so the NEXT backoff (count 4 = base*2^3 = 8h)
	// is far past the default markTTLBackstopFactor*lease (60m) backstop.
	for i := 0; i < 3; i++ {
		if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil {
			t.Fatalf("seed dispatch-count: %v", err)
		}
	}
	// ClaimedAt aged past backoffInterval(3) = 4h, so this reclaim is NOT paced.
	m := fixtureMark(targetID, entityID, "missing_x", "triggerLoom", pastLease())
	m.ClaimedAt = substrate.FormatTimestamp(time.Now().Add(-5 * time.Hour))
	h.putMark(t, ctx, key, m)
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
	})

	h.pass(ctx)
	h.nextOp(t) // the aged reclaim fired

	stream, err := h.conn.JetStream().Stream(ctx, "KV_weaver-state")
	if err != nil {
		t.Fatalf("open weaver-state stream: %v", err)
	}
	raw, err := stream.GetLastMsgForSubject(ctx, "$KV.weaver-state."+key)
	if err != nil {
		t.Fatalf("read raw reclaimed mark message: %v", err)
	}
	// The reclaim bumped the count to 4, so the next backoff (and the armed TTL)
	// is sized for count 4.
	wantTTL := (h.engine.sweep.backoffInterval(4) + 2*h.engine.sweep.interval).String()
	if got := raw.Header.Get("Nats-TTL"); got != wantTTL {
		t.Fatalf("reclaimed userTask mark Nats-TTL = %q, want %q (TTL must outlast the next backoff window)", got, wantTTL)
	}
	defaultTTL := markTTLBackstopFactor * h.engine.cfg.MarkLease
	if h.engine.sweep.backoffInterval(4)+2*h.engine.sweep.interval <= defaultTTL {
		t.Fatalf("test misconfigured: widened TTL must exceed the default backstop %s", defaultTTL)
	}
}
