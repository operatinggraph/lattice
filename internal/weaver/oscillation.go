package weaver

import (
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/guardgrammar"
)

// oscillationWindowSize bounds how many recent dispatches against one aspect
// path the oscillation detector remembers per path — enough to recognize a
// repeating two-target alternation without an unbounded ring (design
// weaver-planner-mandate-design.md §3.4).
const oscillationWindowSize = 8

// oscillationMinAlternations is how many trailing STRICT A,B,A,B… dispatches
// (by targetID) must be observed before two targets are judged to be
// fighting over the same aspect path — a single back-and-forth (2 events) is
// ordinary sequential remediation by two independent owners; only a
// REPEATING pattern is a fight. 4 trailing events = 2 full round-trips
// (A→B→A→B).
const oscillationMinAlternations = 4

// oscillationEvent is one fresh-dispatch episode observed to touch a tracked
// aspect path.
type oscillationEvent struct {
	targetID string
	at       time.Time
}

// oscillationStats tracks, per aspect path (guardgrammar.Path — the same key
// an op's declared `.effects` addresses), the recent dispatching targets.
// Purely in-memory and diagnostic like shadowStats/contractionStats: a
// restart resets it (a restart also resets the population an alternation is
// judged over, so nothing load-bearing is lost). A path whose trailing
// window strictly alternates between exactly two targets names a fight; the
// caller (Engine.bumpOscillation) freezes both and the path's ring is
// cleared so the same fight is not reported twice.
type oscillationStats struct {
	mu   sync.Mutex
	seen map[guardgrammar.Path][]oscillationEvent
}

func newOscillationStats() *oscillationStats {
	return &oscillationStats{seen: make(map[guardgrammar.Path][]oscillationEvent)}
}

// record appends one dispatch and reports the fighting pair if the trailing
// window now shows a confirmed alternation. ok=false means no fight is (yet)
// detected for this path. A detected pair's ring is cleared so the fight
// reports once, not once per subsequent dispatch against the same path.
func (o *oscillationStats) record(path guardgrammar.Path, targetID string, at time.Time) (targetA, targetB string, ok bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	ring := append(o.seen[path], oscillationEvent{targetID: targetID, at: at})
	if len(ring) > oscillationWindowSize {
		ring = ring[len(ring)-oscillationWindowSize:]
	}
	a, b, alternating := trailingAlternation(ring, oscillationMinAlternations)
	if !alternating {
		o.seen[path] = ring
		return "", "", false
	}
	delete(o.seen, path)
	return a, b, true
}

// trailingAlternation reports whether the last `count` events of ring
// strictly alternate between exactly two distinct targetIDs (…A,B,A,B — or
// …B,A,B,A). Fewer than `count` events, or two consecutive events from the
// SAME target, report false.
func trailingAlternation(ring []oscillationEvent, count int) (a, b string, ok bool) {
	if len(ring) < count {
		return "", "", false
	}
	trail := ring[len(ring)-count:]
	a, b = trail[0].targetID, trail[1].targetID
	if a == b {
		return "", "", false
	}
	for i, ev := range trail {
		want := a
		if i%2 == 1 {
			want = b
		}
		if ev.targetID != want {
			return "", "", false
		}
	}
	return a, b, true
}
