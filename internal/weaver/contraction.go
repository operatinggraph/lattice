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

// surfaceColumn is one (target, gap column)'s open-row membership together with
// the issue identity its Health entry is written under. The package's declared
// issueCode/issueSeverity are recorded at the ADD because the removal legs hold
// no *Target to read them from — retireClosedGapIssues is handed a
// (target, entity, column) triple, and the tombstone leg an empty row.
type surfaceColumn struct {
	code     string
	severity string
	members  map[string]struct{}
}

// surfaceReflection is one column's state after a membership change: what the
// entry must now say, or — at count 0 — that it must retire. Engine.reflectSurface
// is its only consumer.
type surfaceReflection struct {
	column   string
	code     string
	severity string
	count    int
}

// surfaceStats tracks, per (target, gap column), the set of entity ids this
// engine instance observes holding that column open for a `surface` gap
// (FR29). It is the membership behind the ONE counted Health entry each such
// column raises at issueKeyGapOpen — a `surface` gap's population is the
// target's open business backlog, not a set of faults, so it is held here as a
// struct{} set rather than as one issue per row in the cache's per-row budget.
// Purely in-memory, mirroring contractionStats.
//
// Lifetime:
//
//	created       | lazily, at a (target, column)'s first observed-open row
//	added         | in dispatchGap's actionSurface arm, on a TRANSITION to open only — a repeat
//	              | delivery of an already-open row is a no-op (contractionStats.observe's rule)
//	removed       | (a) the entity's column observed false, via retireClosedGapIssues — the leg
//	              | clearClosedMarks' candidate walk and the sweep both reach; (b) the entity
//	              | tombstoned, via removeEntity across EVERY column set of the target: on an
//	              | empty body markCandidateColumns yields the playbook's keys alone, so a column
//	              | the playbook has since dropped never yields and a membership recorded while it
//	              | was still declared would leak with the entity gone; (c) the target leaving, via
//	              | removeTarget at Revoke and at reconcileConsumers' removal
//	issue entry   | rewritten on EVERY membership change, add and remove alike, so the count an
//	              | operator reads is the count this instance holds; setLocked preserves an
//	              | existing `since`, so a rewrite restates the count without disturbing the
//	              | entry's age. Only the remove that EMPTIES a column retires the entry, and that
//	              | retirement deletes the `since` — so the entry's age means "when this column
//	              | last went from no open rows to some"
//	ordering      | none is promised: the set is a membership, not a sample
//	crash/restart | empty, like the latch it replaces. Rebuilt from whatever lane 1 delivers
//	              | afterwards, so the count is a LOWER BOUND until every open row re-projects: a
//	              | lane-1 durable that survives resumes from its persisted ack floor. What
//	              | re-derives it in full is a durable created from scratch (a cold boot, a newly
//	              | registered target) or Engine.ReplayTarget, whose DeliverLastPerSubject
//	              | re-presents the target's whole current row set
//	reconnect     | untouched — in-memory, no substrate dependency
//	upgrade       | starts empty; the count climbs back as rows redeliver
//	loss          | degrades to a missing or low count, never to a wrong verdict — nothing gates
//	              | on it, exactly as for contractionStats
//
// The count is also PER INSTANCE: lane-1 durables are one per target under a
// shared name prefix, so with more than one Weaver a target's rows shard across
// instances and each instance's heartbeat carries the count it observes. That is
// why Loupe reports Weaver heartbeats per instance rather than merging them, and
// why the contract clause states both bounds on the wire.
type surfaceStats struct {
	mu      sync.Mutex
	columns map[string]map[string]*surfaceColumn // targetId -> gapColumn -> membership
}

func newSurfaceStats() *surfaceStats {
	return &surfaceStats{columns: make(map[string]map[string]*surfaceColumn)}
}

// add records entityID as holding (targetID, col) open, under the package's
// declared code and severity. It reports the column's state and whether this
// delivery CHANGED the membership — only a change may rewrite the Health entry,
// so a CDC redelivery of an already-open row writes nothing.
//
// code and severity are refreshed on every add, not only on the first: a
// package re-author that changes either takes effect on the next delivery
// through the entry's rewrite, rather than being pinned to whatever the first
// open row of the process carried.
func (s *surfaceStats) add(targetID, col, entityID, code, severity string) (surfaceReflection, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cols := s.columns[targetID]
	if cols == nil {
		cols = make(map[string]*surfaceColumn)
		s.columns[targetID] = cols
	}
	sc := cols[col]
	if sc == nil {
		sc = &surfaceColumn{members: make(map[string]struct{})}
		cols[col] = sc
	}
	sc.code, sc.severity = code, severity
	if _, was := sc.members[entityID]; was {
		return surfaceReflection{}, false
	}
	sc.members[entityID] = struct{}{}
	return reflectionOf(col, sc), true
}

// remove drops entityID from (targetID, col) and reports the column's remaining
// state. changed is false when the entity held no membership there — which is
// the ordinary case for every non-surface gap, since every gap-close leg calls
// through retireClosedGapIssues whether the column is a surface one or not.
func (s *surfaceStats) remove(targetID, col, entityID string) (surfaceReflection, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.columns[targetID][col]
	if sc == nil {
		return surfaceReflection{}, false
	}
	if _, was := sc.members[entityID]; !was {
		return surfaceReflection{}, false
	}
	delete(sc.members, entityID)
	out := reflectionOf(col, sc)
	s.pruneLocked(targetID, col, sc)
	return out, true
}

// removeEntity drops entityID from EVERY column set of targetID, reporting one
// reflection per column whose membership actually changed. This is the entity
// TOMBSTONE leg: an empty row names no columns, and the playbook may have
// dropped one since the membership was recorded, so no per-column walk can
// reach them all — the in-memory analogue of the `gap:` prefix clear the same
// leg runs.
func (s *surfaceStats) removeEntity(targetID, entityID string) []surfaceReflection {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []surfaceReflection
	for col, sc := range s.columns[targetID] {
		if _, was := sc.members[entityID]; !was {
			continue
		}
		delete(sc.members, entityID)
		out = append(out, reflectionOf(col, sc))
		s.pruneLocked(targetID, col, sc)
	}
	return out
}

// removeTarget drops the target's whole membership. The caller's own prefix
// clear over issueKeyTargetPrefixes retires the Health entries; this retires the
// state that would otherwise re-raise them if the target came back.
func (s *surfaceStats) removeTarget(targetID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.columns, targetID)
}

// count reports how many entities hold (targetID, col) open — the membership
// behind the entry's message, for tests and for a caller that needs the number
// without changing it.
func (s *surfaceStats) count(targetID, col string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.columns[targetID][col]
	if sc == nil {
		return 0
	}
	return len(sc.members)
}

// pruneLocked drops an emptied column, and the target once its last column
// goes, so a target whose backlog drains leaves no map entries behind.
func (s *surfaceStats) pruneLocked(targetID, col string, sc *surfaceColumn) {
	if len(sc.members) > 0 {
		return
	}
	delete(s.columns[targetID], col)
	if len(s.columns[targetID]) == 0 {
		delete(s.columns, targetID)
	}
}

func reflectionOf(col string, sc *surfaceColumn) surfaceReflection {
	return surfaceReflection{column: col, code: sc.code, severity: sc.severity, count: len(sc.members)}
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
