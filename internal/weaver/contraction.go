package weaver

import "sync"

// contractionWindowSize bounds how many periodic samples a target's
// violating-row trajectory ring keeps (design
// weaver-planner-mandate-design.md §3.4) — enough to classify a trend
// without an unbounded history.
const contractionWindowSize = 5

// Trajectory classifications (contraction heartbeat metric values).
const (
	trajectoryShrinking = "shrinking"
	trajectoryDiverging = "diverging"
	trajectorySteady    = "steady"
)

// contractionStats tracks, per target, the CURRENT count of rows this engine
// instance has observed to be violating (incremental — updated from every
// lane-1 CDC delivery, never a KV scan) plus a bounded ring of periodic
// samples the reconciler sweep appends on its own cadence (design §3.4: "over
// a sweep-cadence window"). Purely in-memory and diagnostic, mirroring
// shadowStats.
//
// A restart empties it, and what rebuilds it is only what lane-1 delivers
// afterwards. A lane-1 durable that SURVIVES the restart resumes from its
// persisted ack floor, so a violating row that was already acked and has not
// re-projected is never re-counted — the count is a lower bound on the true one
// until every violating row projects again. The two things that re-derive it in
// full are a durable created from scratch (a cold boot, a newly registered
// target) and Engine.ReplayTarget, which recreates one target's lane-1 durable
// so DeliverLastPerSubject re-presents that target's whole current row set.
//
// Best-effort with an honest bound is the right posture here because nothing
// gates on the number: it feeds the heartbeat's trajectory metric and no
// decision. Machinery to make it exact would be a per-target enumeration paid
// on every boot, which is precisely the cost the replay verb exists to pay only
// when an operator asks for it.
type contractionStats struct {
	mu      sync.Mutex
	known   map[string]struct{} // "<targetId>.<entityId>" currently counted as violating
	current map[string]int      // targetId -> current violating-row count
	samples map[string][]int    // targetId -> bounded ring of sweep-cadence samples
}

func newContractionStats() *contractionStats {
	return &contractionStats{
		known:   make(map[string]struct{}),
		current: make(map[string]int),
		samples: make(map[string][]int),
	}
}

// observe records one row delivery's violating state — called on EVERY
// lane-1 delivery (mirrors clearClosedMarks/scheduleFreshness's "violating or
// not" cadence), including the tombstone case (violating=false). Only a
// TRANSITION changes the target's current count; a repeat delivery of the
// same state (a CDC redelivery, or a row that never changes) is a no-op. A
// row is added to `known` only once observed violating — never on a
// non-violating first sighting — so `known` stays bounded to currently-
// violating rows, not every row ever delivered.
func (c *contractionStats) observe(targetID, entityID string, violating bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := targetID + "." + entityID
	_, was := c.known[key]
	if was == violating {
		return
	}
	if violating {
		c.known[key] = struct{}{}
		c.current[targetID]++
	} else {
		delete(c.known, key)
		c.current[targetID]--
	}
}

// sample appends the current violating-row count for every id in targetIDs
// to its trajectory ring, capped at contractionWindowSize (oldest evicted
// first) — the reconciler sweep's cadence call (design §3.4's "sweep-cadence
// window").
func (c *contractionStats) sample(targetIDs []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range targetIDs {
		ring := append(c.samples[id], c.current[id])
		if len(ring) > contractionWindowSize {
			ring = ring[len(ring)-contractionWindowSize:]
		}
		c.samples[id] = ring
	}
}

// snapshot classifies every target's trajectory ring — shrinking (net
// decrease across a non-increasing window), diverging (net increase across a
// non-decreasing window), or steady (anything else, including a window too
// short to judge) — in a fresh map for the heartbeat to serialize.
func (c *contractionStats) snapshot() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.samples))
	for id, ring := range c.samples {
		out[id] = classifyTrajectory(ring)
	}
	return out
}

// classifyTrajectory is the pure classification rule: fewer than 2 samples
// is "steady" (no trend derivable yet — the least alarming default); a
// window that never increases step-to-step and ends below where it started
// is "shrinking"; a window that never decreases step-to-step and ends above
// where it started is "diverging"; everything else (flat, or reversing
// direction mid-window) is "steady".
func classifyTrajectory(ring []int) string {
	if len(ring) < 2 {
		return trajectorySteady
	}
	nonIncreasing, nonDecreasing := true, true
	for i := 1; i < len(ring); i++ {
		if ring[i] > ring[i-1] {
			nonIncreasing = false
		}
		if ring[i] < ring[i-1] {
			nonDecreasing = false
		}
	}
	first, last := ring[0], ring[len(ring)-1]
	if nonIncreasing && last < first {
		return trajectoryShrinking
	}
	if nonDecreasing && last > first {
		return trajectoryDiverging
	}
	return trajectorySteady
}
