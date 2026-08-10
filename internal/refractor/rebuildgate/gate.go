// Package rebuildgate bounds lens rebuilds across every path that starts one.
//
// A lens rebuild is a durable JetStream delete-recreate plus the pump's reopen
// against it (substrate.ConsumerSupervisor.ResetAwaitReopen) — consumer
// management work that a whole corpus can ask the server for at once. Two
// concurrent rebuilds cost the server the same whether a taxonomy sweep, an
// operator control RPC or anything else asked for them, so the bound has to be
// GLOBAL — one gate shared by every starter — rather than one bound per path,
// which would leave the sum unbounded.
//
// What is bounded is the RATE of consumer-management demand, and with it the
// fairness of who gets served: a sweep that issues its whole fan-out at once
// puts every other lens's rebuild, and every ordinary reconnect, behind it. This
// is a concurrency and fairness control. It is not a memory control and makes no
// claim to be one.
//
// Nothing here knows what a lens, a taxonomy or a rebuild is: a Gate is a
// keyed serializer with a global concurrency ceiling, and the callers supply the
// meaning of the key.
package rebuildgate

import (
	"context"
	"sync"
)

// Gate bounds how many rebuilds run concurrently across every path that starts
// one, and serializes them per key.
//
// Two properties, both relied on by callers:
//
//   - Per-key mutual exclusion. At most one fn per key runs at a time.
//   - Global bound. At most limit fn bodies run at a time in total, across all
//     keys.
//
// A Gate is safe for concurrent use by any number of goroutines. The zero value
// is NOT usable and does not fail gracefully: its keys map is nil, so the first
// Do panics with "assignment to entry in nil map" in acquireKey. Construct one
// with New.
type Gate struct {
	// slots is the global ceiling, held as a counting semaphore. A send takes a
	// slot, a receive returns it.
	slots chan struct{}

	// mu guards keys. It is held only for the bookkeeping around a key's
	// mutex — never across a wait — so it is never a queue of its own.
	mu   sync.Mutex
	keys map[string]*keyLock
}

// keyLock is one key's mutex plus the count of callers that currently hold or
// want it. The mutex is a 1-buffered channel rather than a sync.Mutex because a
// caller must be able to abandon the wait when its context is cancelled, which
// sync.Mutex cannot express.
//
// refs is what keeps the map from growing without bound: a caller registers its
// interest under Gate.mu BEFORE it starts waiting, so the last holder to leave
// can drop the entry knowing no waiter is still pointing at it.
type keyLock struct {
	ch   chan struct{}
	refs int
}

// New returns a Gate admitting at most limit concurrent fn bodies across all
// keys. A limit of zero or less is raised to 1: an un-configured or
// mis-configured gate must come out TIGHTER than intended, never unbounded —
// unbounded is the failure this type exists to prevent, and it is not something
// a caller should be able to reach by passing a zero value.
func New(limit int) *Gate {
	if limit <= 0 {
		limit = 1
	}
	return &Gate{
		slots: make(chan struct{}, limit),
		keys:  make(map[string]*keyLock),
	}
}

// Do runs fn once, under this gate's per-key exclusion and global bound, and
// returns fn's error unchanged.
//
// Do NEVER coalesces. A second Do for a key already in flight waits for the
// first to finish and then runs its OWN fn; it does not join the first call and
// report its result. Joining would be wrong in both directions the callers
// actually exercise: a taxonomy rebuild that joined an operator rebuild started
// BEFORE the taxonomy changed would report success for a pass that never saw
// the change, and a truncate=true rebuild that joined a truncate=false one would
// report success for a truncation that never happened. Callers that want
// coalescing must do it above the gate, where they can see what the two requests
// mean; the gate only serializes.
//
// LOCK ORDER IS LOAD-BEARING: the per-key lock is taken FIRST, the global slot
// SECOND, and no holder of a slot ever waits for a key. Taking them the other
// way round deadlocks — a caller holding the last slot and waiting for key K,
// while K's holder waits for a slot, is a cycle neither side can break. With
// this order every slot-holder already owns its key and can only be waiting on
// fn, so slots always drain.
//
// If ctx is cancelled while WAITING for either the key or the slot, Do returns
// ctx.Err(), releases whatever it had already taken, and does not run fn. What
// ctx bounds is precisely the waiting: an acquisition that needs no wait is
// taken outright, so a caller arriving with an already-cancelled ctx at an idle
// gate still runs fn. Do is a queue with an escape hatch, not a second
// cancellation check standing in front of the caller's own work — the caller
// decides whether its work is still wanted; the gate decides only whether it has
// to queue for it. Once fn starts, ctx is fn's business: the gate neither
// cancels nor interrupts it.
//
// A panic in fn still releases the key and the slot (both are deferred), so a
// panicking caller leaves the gate usable for everyone else.
func (g *Gate) Do(ctx context.Context, key string, fn func() error) error {
	releaseKey, err := g.acquireKey(ctx, key)
	if err != nil {
		return err
	}
	defer releaseKey()

	select {
	case g.slots <- struct{}{}:
	default:
		select {
		case g.slots <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer func() { <-g.slots }()

	return fn()
}

// acquireKey takes key's mutex, returning the function that hands it back.
// The returned release must be called exactly once, and only when err is nil.
func (g *Gate) acquireKey(ctx context.Context, key string) (release func(), err error) {
	g.mu.Lock()
	kl := g.keys[key]
	if kl == nil {
		kl = &keyLock{ch: make(chan struct{}, 1)}
		g.keys[key] = kl
	}
	// Registered before the wait begins, so the current holder cannot retire
	// this entry from the map while this caller is queued behind it — which
	// would silently split one key's exclusion across two keyLocks.
	kl.refs++
	g.mu.Unlock()

	// The uncontended arm is taken outright. A free key is not a queue, so a
	// cancelled ctx has nothing to abandon here — and taking it unconditionally
	// is also what makes "which wait did this caller give up on" answerable
	// rather than a coin flip when both arms of a select are ready.
	select {
	case kl.ch <- struct{}{}:
	default:
		select {
		case kl.ch <- struct{}{}:
		case <-ctx.Done():
			g.dropRef(key, kl)
			return nil, ctx.Err()
		}
	}

	return func() {
		<-kl.ch
		g.dropRef(key, kl)
	}, nil
}

// dropRef retires key's lock once nothing holds or wants it, so a gate used
// over a long-lived corpus does not accumulate one entry per key ever rebuilt.
func (g *Gate) dropRef(key string, kl *keyLock) {
	g.mu.Lock()
	kl.refs--
	if kl.refs == 0 && g.keys[key] == kl {
		delete(g.keys, key)
	}
	g.mu.Unlock()
}
