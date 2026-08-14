package main

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// auditCensusGrace is how long after the instance reports ready the census
// waits before counting. Lenses activate off the Core-KV replay, one CDC event
// at a time, so at the moment the process declares itself ready the registry is
// typically still filling; counting then would report a number about the boot
// race rather than about the corpus.
//
// It is a settle window, not a completeness guarantee, and the census says so —
// which is the honest shape, because there is no moment at which the lens set is
// provably final (a lens installed by an operator an hour later is an ordinary
// event). The per-lens auditEnrolled/auditRefusal fields are the authoritative
// record; this line exists so the number is OBSERVED at boot rather than
// asserted in a design doc.
const auditCensusGrace = 30 * time.Second

// auditCensus tallies the divergence audit's enrolment decisions across the
// lens set, so the first act of the mechanism is a reported count rather than a
// predicted one (lens-projection-divergence-audit-design.md §4.4).
//
// Keyed by lens ID rather than counted incrementally: a lens can be activated
// more than once in a process's life (a MATCH hot-reload rebuilds its pipeline,
// a delete-then-reinstall re-runs activation), and a running tally would count
// the same lens twice and report a corpus larger than the one that exists.
type auditCensus struct {
	mu       sync.Mutex
	verdicts map[string]string // lensID → refusal, "" when enrolled
}

func newAuditCensus() *auditCensus {
	return &auditCensus{verdicts: map[string]string{}}
}

// record books one lens's enrolment verdict, replacing any earlier one for the
// same lens.
func (c *auditCensus) record(lensID string, enrolled bool, refusal string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if enrolled {
		c.verdicts[lensID] = ""
		return
	}
	c.verdicts[lensID] = refusal
}

// tally returns the enrolled count and the refusal reasons by frequency,
// worst-first, so the caller can name the dominant one.
func (c *auditCensus) tally() (enrolled int, refusals map[string]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	refusals = map[string]int{}
	for _, refusal := range c.verdicts {
		if refusal == "" {
			enrolled++
			continue
		}
		refusals[refusal]++
	}
	return enrolled, refusals
}

// Report logs the census once, after the settle window. A dominant refusal is
// named because that is the actionable half: a reason that turns out to cover
// most of the corpus is a grounded follow-on, where the same fact spread across
// a dozen distinct reasons is the gate working as designed.
func (c *auditCensus) Report(ctx context.Context, logger *slog.Logger) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(auditCensusGrace):
	}
	enrolled, refusals := c.tally()
	refused := 0
	for _, n := range refusals {
		refused += n
	}
	attrs := []any{
		"enrolled", enrolled,
		"refused", refused,
		"observedAfter", auditCensusGrace.String(),
	}
	if dominant, n := dominantRefusal(refusals); n > 0 {
		attrs = append(attrs, "dominantRefusal", dominant, "dominantRefusalCount", n)
	}
	logger.Info("divergence audit enrolment census", attrs...)
}

// dominantRefusal returns the most frequent refusal reason and its count. Ties
// break lexicographically so the reported reason is stable across boots rather
// than following map iteration order.
func dominantRefusal(refusals map[string]int) (string, int) {
	reasons := make([]string, 0, len(refusals))
	for reason := range refusals {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	best, bestN := "", 0
	for _, reason := range reasons {
		if refusals[reason] > bestN {
			best, bestN = reason, refusals[reason]
		}
	}
	return best, bestN
}
