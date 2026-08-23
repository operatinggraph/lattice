package weaver

import (
	"sort"
	"sync"
	"time"
)

// admissionPriorityColumn is the engine-recognized optional §10.2 row column
// carrying a dispatch priority (higher = more urgent) — a bare column, in the
// same class as freshUntilColumn, consulted only when the target declares an
// AdmissionPolicy. Absent or non-numeric reads as priority 0 (intColumn's
// existing absent/garbled default), the lowest tier.
const admissionPriorityColumn = "priority"

// admissionGrantTTL bounds how long a token reserved for a pending id may sit
// unclaimed before the bucket reclaims it. A grant is created when some OTHER
// caller's admit() drains the priority queue past this id (see tokenBucket.admit)
// — the id's own episode may never call admit() again (a legitimate close, or a
// redelivery that never arrives), which would otherwise waste that token
// forever. Sized well above any realistic redelivery interval so a live episode
// always collects its grant first.
const admissionGrantTTL = 5 * time.Minute

// admissionPendingCap bounds one bucket's pending queue — a hard ceiling so a
// runaway backlog (declared budget far below sustained violation volume)
// cannot grow the in-memory queue unbounded. Past the cap, the lowest-priority
// (oldest-among-ties) entries are evicted to make room for a fresher request;
// an evicted id simply re-enters the queue on its next redelivery, so this is
// a soft/lossy bound, never a correctness hazard (no mark, no episode state
// lives here — see AdmissionPolicy).
const admissionPendingCap = 10_000

// AdmissionPolicy is a target's optional `admission` block (Contract #10 §10.8
// Planner extension, Fire 8): declared dispatch-pacing budgets. Absent (the
// default) is unbounded — no pacing is applied, so a resolved gap dispatches
// immediately with no admission-scheduler involvement. Config + optional
// package data, never consulted for correctness (the §10.3 mark CAS-create
// remains the sole anti-storm/idempotency gate) — admission control only
// paces WHEN a plan already resolved to fire, deferring the rest to a later
// redelivery.
type AdmissionPolicy struct {
	// GlobalRate bounds the target's TOTAL dispatch rate (tokens/sec, burst
	// capacity == the rate — one second of headroom). 0/absent = unbounded on
	// this axis.
	GlobalRate float64 `json:"globalRate,omitempty"`
	// AdapterRates bounds the dispatch rate for gaps whose resolved action
	// declares a matching Adapter (GapAction.Adapter) — a rate here takes
	// precedence over GlobalRate for a gap declaring that adapter, so a
	// noisy external system can be paced independently of the target's other
	// gaps. A gap with no declared Adapter, or one absent from this map, is
	// governed by GlobalRate alone.
	AdapterRates map[string]float64 `json:"adapterRates,omitempty"`
}

// pendingAdmission is one id waiting for a token in a bucket's priority queue.
type pendingAdmission struct {
	id       string
	priority int
	since    time.Time
}

// grantedAdmission is a token already reserved for id by some OTHER caller's
// drain (tokenBucket.admit), awaiting that id's own next admit() call to
// collect it.
type grantedAdmission struct {
	at time.Time
}

// tokenBucket is a priority-fair rate limiter: tokens refill continuously at
// `rate` per second up to `capacity`, and when contended (more pending ids
// than available tokens), the highest-priority ids are served first —
// ties broken by earliest `since`. Every admit() call both contributes its
// own request and cooperatively drains the shared queue (no background
// goroutine, mirroring contractionStats/oscillationStats's inline-processing
// style): a caller whose id is drained by SOMEONE ELSE'S call collects its
// already-reserved grant on its own next call instead of re-consuming a token.
type tokenBucket struct {
	rate     float64
	capacity float64
	tokens   float64
	last     time.Time
	pending  []pendingAdmission
	granted  map[string]grantedAdmission
}

// newTokenBucket starts the bucket FULL (tokens == capacity): a freshly
// declared budget admits its first burst immediately rather than making the
// very first caller wait a full second for tokens to accrue.
func newTokenBucket(rate float64) *tokenBucket {
	return &tokenBucket{rate: rate, capacity: rate, tokens: rate, granted: make(map[string]grantedAdmission)}
}

// admit reports whether id may fire now. now is caller-supplied (never
// time.Now() internally) so tests can drive the bucket deterministically.
func (b *tokenBucket) admit(id string, priority int, now time.Time) bool {
	b.expireGrants(now)
	if _, ok := b.granted[id]; ok {
		delete(b.granted, id)
		return true
	}
	b.refill(now)

	found := false
	for _, p := range b.pending {
		if p.id == id {
			found = true
			break
		}
	}
	if !found {
		b.pending = append(b.pending, pendingAdmission{id: id, priority: priority, since: now})
		b.evictOverflow()
	}

	sort.SliceStable(b.pending, func(i, j int) bool {
		if b.pending[i].priority != b.pending[j].priority {
			return b.pending[i].priority > b.pending[j].priority
		}
		return b.pending[i].since.Before(b.pending[j].since)
	})

	n := int(b.tokens)
	if n > len(b.pending) {
		n = len(b.pending)
	}
	callerAdmitted := false
	for i := 0; i < n; i++ {
		if b.pending[i].id == id {
			callerAdmitted = true
			continue
		}
		b.granted[b.pending[i].id] = grantedAdmission{at: now}
	}
	if n > 0 {
		b.tokens -= float64(n)
		b.pending = b.pending[n:]
	}
	return callerAdmitted
}

// refill advances the bucket's token count for elapsed wall-clock time since
// the last admit() call, capped at capacity. The first call on a fresh bucket
// only anchors `last` — no synthetic burst from an unset zero time.
func (b *tokenBucket) refill(now time.Time) {
	if b.last.IsZero() {
		b.last = now
		return
	}
	if now.Before(b.last) {
		return
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * b.rate
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	b.last = now
}

// expireGrants reclaims a token reserved for an id that never returned to
// collect it within admissionGrantTTL — otherwise a legitimately-closed gap
// (or a redelivery that never arrives) would waste that capacity forever.
func (b *tokenBucket) expireGrants(now time.Time) {
	for id, g := range b.granted {
		if now.Sub(g.at) >= admissionGrantTTL {
			delete(b.granted, id)
			b.tokens += 1
			if b.tokens > b.capacity {
				b.tokens = b.capacity
			}
		}
	}
}

// evictOverflow drops the lowest-priority, oldest-among-ties pending entries
// once the queue exceeds admissionPendingCap — a soft, lossy bound (see the
// constant's doc); the dropped id simply re-enters on its own next
// redelivery.
func (b *tokenBucket) evictOverflow() {
	if len(b.pending) <= admissionPendingCap {
		return
	}
	sort.SliceStable(b.pending, func(i, j int) bool {
		if b.pending[i].priority != b.pending[j].priority {
			return b.pending[i].priority > b.pending[j].priority
		}
		return b.pending[i].since.Before(b.pending[j].since)
	})
	b.pending = b.pending[:admissionPendingCap]
}

// admissionScheduler is the Engine's in-memory dispatch-pacing layer (design
// weaver-planner-mandate-design.md §3.4 "Admission control"), sitting between
// the Strategist's plan resolution and the Actuator's fire: purely in-memory
// and process-local, like shadowStats/contractionStats/oscillationStats — a
// restart resets every bucket, and the mark/OCC machinery (the actual
// correctness gate) is untouched, so a reset only means a fresh pacing
// window, never a duplicate or lost dispatch.
type admissionScheduler struct {
	mu       sync.Mutex
	buckets  map[string]*tokenBucket
	admitted int64
	deferred int64
}

func newAdmissionScheduler() *admissionScheduler {
	return &admissionScheduler{buckets: make(map[string]*tokenBucket)}
}

// admit resolves which bucket (if any) governs this gap dispatch and reports
// whether it may fire now. policy == nil (the target declares no `admission`
// block — every target before Fire 8) always admits without touching a
// bucket or reading the row's priority column: byte-identical to pre-Fire-8
// dispatch. Precedence mirrors the GapAction action-selection convention
// (explicit > general): an adapter-specific rate, when the resolved action
// declares a matching adapter, governs instead of the target's global rate.
func (a *admissionScheduler) admit(policy *AdmissionPolicy, targetID, id, adapter string, priority int, now time.Time) bool {
	if policy == nil {
		return true
	}
	key, rate, governed := admissionBucketKey(policy, targetID, adapter)
	if !governed {
		return true
	}
	a.mu.Lock()
	b, ok := a.buckets[key]
	if !ok {
		b = newTokenBucket(rate)
		a.buckets[key] = b
	} else if b.rate != rate {
		// A live re-install changed the declared rate: adopt it going
		// forward (capacity tracks rate 1:1); pending/granted state carries
		// over unchanged.
		b.rate = rate
		b.capacity = rate
	}
	ok = b.admit(id, priority, now)
	if ok {
		a.admitted++
	} else {
		a.deferred++
	}
	a.mu.Unlock()
	return ok
}

// admissionBucketKey resolves the bucket a gap dispatch is governed by:
// adapter != "" and policy declares a positive rate for it wins; else the
// target's positive GlobalRate; else the gap is ungoverned (governed=false —
// the caller must always admit).
func admissionBucketKey(policy *AdmissionPolicy, targetID, adapter string) (key string, rate float64, governed bool) {
	if adapter != "" {
		if r, ok := policy.AdapterRates[adapter]; ok && r > 0 {
			return targetID + "\x00" + adapter, r, true
		}
	}
	if policy.GlobalRate > 0 {
		return targetID, policy.GlobalRate, true
	}
	return "", 0, false
}

// metrics snapshots the since-start admission counters for the heartbeat.
func (a *admissionScheduler) metrics() (admitted, deferred int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.admitted, a.deferred
}
