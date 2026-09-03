package projection

import (
	"sort"
	"sync"
)

// The process's census of D1 read-grant producers that installed WITHOUT a
// grant-change sink (personal-lens-derivation-licence-design.md §4.3(d)
// amendment, §4.4 conjunct 1).
//
// A qualifying producer with no sink is installed deliberately, not refused.
// Refusing it would turn a host that wired no reprojector into an auth-plane
// outage on the primordial capabilityRead lens, and the sweep-without-edge
// posture is a shipped, tested invariant: the grants still land, they still
// retract, and every consumer converges on its standing healer instead. What is
// missing is only the PUSH.
//
// But "only the push" is exactly what a consumer that NARROWS is resting on. The
// personal derivation licence stops a personal lens reprojecting on out-of-
// pattern neighbours, and the argument for that rests on every input having a
// change edge. A cap-read producer whose withdrawals push nothing leaves one of
// those inputs announcing through nothing at all — for the whole coverage of
// that producer's key space, silently, with only a Warn at boot as the trace.
//
// So the Warn becomes countable here, and the licence refuses on the count. That
// is the amendment's whole shape: the INSTALL stays permissive, the CONSUMER
// that would narrow on the strength of the missing edge refuses on its own
// terms.
//
// LIFETIME. Process-level, because the question is about the process: "does
// every cap-read producer running HERE announce". Keyed by lens id and set on
// every installation, so a hot reload that adds a sink clears the entry the
// previous body wrote and one that removes it re-adds it. ForgetReadGrantProducer
// drops a deleted lens, or a producer that never runs again would refuse the
// narrowing for the life of the process. Never persisted: a restart re-derives
// it from the installations that restart performs.
var readGrantSinkCensus = struct {
	mu       sync.Mutex
	sinkless map[string]struct{}
}{
	sinkless: map[string]struct{}{},
}

// noteReadGrantProducerSink records this process's verdict for one installed
// read-grant producer. Called from the single installation site that decides it,
// with the same boolean the Warn is emitted on, so the census and the log cannot
// disagree about which lenses are sink-less.
func noteReadGrantProducerSink(ruleID string, sinkWired bool) {
	readGrantSinkCensus.mu.Lock()
	defer readGrantSinkCensus.mu.Unlock()
	if sinkWired {
		delete(readGrantSinkCensus.sinkless, ruleID)
		return
	}
	readGrantSinkCensus.sinkless[ruleID] = struct{}{}
}

// ForgetReadGrantProducer drops one lens from the census. Called from the lens
// removal path, unconditionally and idempotently for the same reason the
// reprojector's own deregistration is: a lens that no longer runs must stop
// being counted against a consumer that is deciding what to narrow.
func ForgetReadGrantProducer(ruleID string) {
	readGrantSinkCensus.mu.Lock()
	defer readGrantSinkCensus.mu.Unlock()
	delete(readGrantSinkCensus.sinkless, ruleID)
}

// ReadGrantProducersWithoutSink lists, sorted, the lens ids of every read-grant
// producer installed in this process with no grant-change sink.
//
// It returns the NAMES rather than a count because the caller that refuses on it
// — the host asserting the personal derivation licence — owes an operator the
// lens to go and look at, and because a bare count reads as a metric when it is
// really a list of specific broken edges.
func ReadGrantProducersWithoutSink() []string {
	readGrantSinkCensus.mu.Lock()
	defer readGrantSinkCensus.mu.Unlock()
	out := make([]string, 0, len(readGrantSinkCensus.sinkless))
	for id := range readGrantSinkCensus.sinkless {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
