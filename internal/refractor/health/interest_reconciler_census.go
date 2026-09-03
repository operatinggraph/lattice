package health

import "sync"

// The process's census of InterestReconcilers that carry no change edge
// (personal-lens-derivation-licence-design.md §4.4c conjunct 2).
//
// The Interest Set has FOUR writers, and a personal lens's narrowing licence
// rests on all four announcing. Three of them live on control.Service, which can
// answer for itself. The fourth is this type's orphan reap, and it cannot be
// asked at the moment the licence is asserted: cmd/refractor constructs the
// reconciler inside the very activation arm that registers the first personal
// lens, several statements later and only for a deployment that has a SYNC
// stream at all. A boolean sampled at registration would answer about a process
// one line younger than the one that runs, in the fail-open direction.
//
// So the fact is kept here, at process level, and read LIVE by the licence
// through a host-injected accessor. Two properties make it honest:
//
//   - a reconciler is counted from CONSTRUCTION, not from Run. A reconciler
//     built and never given a sink is a fourth writer that will reap silently
//     whether or not anyone has started it yet, and the licence must refuse from
//     the moment that object exists;
//   - "no reconciler at all" is not a refusal. A deployment with no SYNC stream
//     has no orphan reap, so there is no fourth writer to owe an announcement.
//     An empty census is a real pass, unlike the instance census, where zero is
//     self-refuting.
//
// LIFETIME: process-level, keyed by reconciler identity so two reconcilers over
// two SYNC streams are counted separately. Never persisted; a restart re-derives
// it from the reconcilers that restart constructs. Entries are not removed —
// a reconciler is constructed once, at boot, for the life of the process, and
// nothing tears one down.
var interestReconcilerCensus = struct {
	mu       sync.Mutex
	unarmed  map[*InterestReconciler]struct{}
	observed map[*InterestReconciler]struct{}
}{
	unarmed:  map[*InterestReconciler]struct{}{},
	observed: map[*InterestReconciler]struct{}{},
}

// noteInterestReconcilerSink records this process's verdict for one reconciler,
// from the same value SetInterestChangeSink installs, so the census and the
// object cannot disagree about which reconcilers announce.
func noteInterestReconcilerSink(r *InterestReconciler, armed bool) {
	interestReconcilerCensus.mu.Lock()
	defer interestReconcilerCensus.mu.Unlock()
	interestReconcilerCensus.observed[r] = struct{}{}
	if armed {
		delete(interestReconcilerCensus.unarmed, r)
		return
	}
	interestReconcilerCensus.unarmed[r] = struct{}{}
}

// InterestReconcilersWithoutSink counts the reconcilers constructed in this
// process whose orphan reap announces nothing. Zero is the licensing answer,
// including for a deployment that built none.
func InterestReconcilersWithoutSink() int {
	interestReconcilerCensus.mu.Lock()
	defer interestReconcilerCensus.mu.Unlock()
	return len(interestReconcilerCensus.unarmed)
}

// InterestReconcilersConstructed counts every reconciler this process has built.
// It exists so a test can prove the census is REACHED — a count of unarmed
// reconcilers that is zero because nothing was ever recorded reads exactly like
// one that is zero because everything is armed.
func InterestReconcilersConstructed() int {
	interestReconcilerCensus.mu.Lock()
	defer interestReconcilerCensus.mu.Unlock()
	return len(interestReconcilerCensus.observed)
}
