package loom

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// legacySweepInterval is the gap the conversion pass leaves between two
// publishes in a family whose subjects are a CDC durable's filter — roughly 100
// conversions a second. Every conversion is a new empty-body message the durable
// delivers, and the deadline durable's handler runs one instance probe per
// delivery (~1 ms), so at this rate the durable drains faster than the pass
// feeds it and its pending count stays in single digits. That bound is the point
// of the pacing rather than politeness: the step-deadline signal Loom's
// off-stream recovery depends on is a marker that survives one second in the
// stream, so a durable that lags behind the pass would miss it.
const legacySweepInterval = 10 * time.Millisecond

// legacySweepPublishTimeout bounds one conversion publish. A stalled ack must
// not hold the pass's goroutine open for the life of the process; the pass has
// no deadline of its own, so each publish carries this one.
const legacySweepPublishTimeout = 5 * time.Second

// legacyTombstoneFamily is one ephemeral key family the conversion pass visits:
// the key filter it enumerates, whether its publishes are paced, and the
// per-key precondition it carries, if any.
type legacyTombstoneFamily struct {
	// filter is the key pattern KVListTombstones enumerates. `*` matches one
	// key token, `>` matches one or more trailing tokens.
	filter string
	// paced reports whether the family's subjects are the filter of a
	// DeliverAll durable, whose delivery cost sets the pass's rate
	// (legacySweepInterval). A family whose subjects no durable filters
	// converts unpaced — not because its conversions are free on the server
	// (every Nats-Rollup: sub publish runs the per-subject purge path and
	// walks every consumer's pending map, whatever family it lands on), but
	// because they produce no DELIVERY, so no handler can fall behind them.
	paced bool
	// guard is the family's per-key precondition, consulted immediately
	// before each publish. It reports whether to skip the marker (counted as
	// skippedRunning), and an error when the state it needs cannot be read —
	// the pass never converts a key it could not classify. Nil for a family
	// whose markers are convertible on sight.
	guard func(ctx context.Context, key string) (skip bool, err error)
}

// legacyTombstoneFamilies is the ordered family set the conversion pass visits:
// the two families no durable filters first and unpaced, then the two that are
// the filter subjects of Loom's KV-CDC durables (outbox.> feeds the outbox
// relay, deadline.> the deadline watcher), paced so neither durable lags.
//
// The instance cursor family is not on this list and is not filtered out of
// one — it is never enumerated at all, so no state a cursor subject can be in
// is reachable by the pass. `instance.*.pattern` cannot match a cursor:
// instance.<id> is a single token after the prefix (an instanceId is a dot-free
// NanoID) and `*` matches exactly one token, so the three-token pattern filter
// matches only the pin sub-key.
//
// Only deadline.> carries a guard (skipRunningDeadlineMarker): a conversion on
// that family is a delivery, and the handler it wakes is destructive on a
// running instance.
func (e *Engine) legacyTombstoneFamilies() []legacyTombstoneFamily {
	return []legacyTombstoneFamily{
		{filter: instancePrefix + "*" + patternPinSuffix},
		{filter: tokenPrefix + ">"},
		{filter: outboxPrefix + ">", paced: true},
		{filter: deadlinePrefix + ">", paced: true, guard: e.skipRunningDeadlineMarker},
	}
}

// skipRunningDeadlineMarker is deadline.>'s precondition: convert a marker
// only when its instance exists AND is terminal.
//
// Every conversion on this family is a fresh empty-body message the deadline
// durable delivers, and handleDeadline keys on exactly that. On a TERMINAL
// instance onDeadline returns at the status check and the probe costs one
// KVGet. On a RUNNING one it runs the Contract #10 §10.6 probe, whose evidence
// is the Contract #4 op tracker — and that tracker lives for TrackerTTL (24h)
// while the wait it backstops does not: a userTask parked on its human is
// bounded only by the task's own lifetime, up to 30 days. So a running
// instance whose deadline was disarmed more than a day ago has no tracker and
// no outbox record left, and the probe reads that absence as "the op was
// rejected" and FAILS the instance. Converting that instance's legacy marker
// would re-fire exactly that probe and kill a live human wait, so the marker
// is left as it is — a permanent subject is the cheaper outcome by far.
//
// A record that cannot be read is a skip too, never a conversion: an instance
// the pass cannot classify is one whose marker it has no verdict on. A read
// FAILURE is a different thing and is returned as an error, ending the family.
func (e *Engine) skipRunningDeadlineMarker(ctx context.Context, key string) (bool, error) {
	instanceID := strings.TrimPrefix(key, deadlinePrefix)
	if instanceID == "" || instanceID == key {
		return true, nil
	}
	inst, err := e.state.getInstance(ctx, instanceID)
	if err != nil {
		return false, err
	}
	return inst == nil || inst.Status == StatusRunning, nil
}

// legacySweepFamilyResult is one family's outcome in a conversion pass.
type legacySweepFamilyResult struct {
	filter          string
	listed          int
	converted       int
	skippedMismatch int
	// skippedRunning counts markers the family's guard refused: a deadline
	// mark whose instance is still running, or whose record is unreadable.
	skippedRunning int
	// stoppedAtKey is the key whose conversion failed, set with err when the
	// family ended early; empty when the family ran to the end of its listing
	// and empty when the family was cancelled, which happens BEFORE a marker
	// is attempted.
	stoppedAtKey string
	err          error
	// cancelled holds ctx.Err() when the engine's context ended the family. It
	// is deliberately not err: nothing failed, and there is no key to name —
	// the markers left are simply the next start's work.
	cancelled error
}

// legacySweepSummary is one conversion pass: a result per family visited, and
// where it stopped if it did not visit them all.
type legacySweepSummary struct {
	families        []legacySweepFamilyResult
	stoppedAtFamily string
	stoppedAtKey    string
	err             error
	// cancelled holds ctx.Err() when the pass ended because the engine's
	// context did, rather than because a publish failed.
	cancelled error
	elapsed   time.Duration
}

// totalListed is the number of delete markers the pass found across every
// family it visited.
func (s legacySweepSummary) totalListed() int {
	n := 0
	for _, f := range s.families {
		n += f.listed
	}
	return n
}

// sweepLegacyTombstones converts every permanent delete marker standing on
// loom-state's four ephemeral key families — the pattern pin, the step tokens,
// the command outbox and the deadline marks — into the expiring purge marker
// this package's removals carry, and returns.
//
// Why it converts them. The bucket is history-1, so a delete marker replaces
// the value and then occupies the subject permanently: no limit ages it and
// nothing discards it, and every whole-bucket listing, every rebuilt DeliverAll
// durable and the stream's subject index carry it forever. A purge marker
// carrying tombstoneTTL expires instead, and because it is itself a
// subject-delete marker the server drops the subject rather than re-marking it,
// so the removed key leaves nothing behind.
//
// Why only delete markers. KVListTombstones returns DELETE-op entries only.
// Purge-op entries — this package's own TTL'd markers, and the server's
// end-of-life markers, which decode as purges too — are already expiring, so
// listing them would make the pass re-purge its own output on every pass.
//
// Why it cannot destroy a live key. Each conversion is conditioned on the
// revision of the marker the listing saw. A key re-created between the listing
// and the publish (a redrive re-pinning, a step re-arming its deadline) no
// longer stands at that revision, so the server refuses the publish; so does a
// subject whose marker is already gone. Both surface as a revision conflict and
// are skipped, counted and logged at Debug — the pass has no verdict to get
// wrong, only a precondition that may no longer hold. Nothing in it purges
// unconditioned, and the cursor family is never enumerated
// (legacyTombstoneFamilies).
//
// Why a conversion is nonetheless not always harmless. A conversion is a
// message, and on deadline.> that message wakes a handler that can fail a
// running instance — so that family carries a per-key guard
// (skipRunningDeadlineMarker) and converts only a terminal instance's marker.
//
// One pass per start, never a loop within one. A key listing is count-bounded
// rather than drain-bounded (docs/vendors.md, the NATS row), so a pass can come
// back short under concurrent rewrites; it converts what it listed and returns,
// and the next start converts the rest. The check is level-triggered: a start
// that lists no delete markers does nothing and says nothing above Debug.
//
// Two of the four families are paced, because their subjects are the filter of
// a DeliverAll durable that has to deliver every conversion (legacySweepInterval).
//
// Concurrent engine instances are safe: each key's conversion is
// revision-conditioned, so the first writer converts it and the second is
// refused and skips.
//
// The pass runs off Start's path, cancelled by the engine's context. A publish
// error that is not a revision conflict ends the pass where it stands — the
// summary line names the family and key — and the next start resumes from the
// bucket, which is the only record of progress there is. Cancellation ends it
// the same way but is reported apart from a failure: nothing went wrong, and no
// key was attempted at the point it stopped.
//
// The summary it logs is also returned, for a caller that asserts on it; Start
// discards it.
func (e *Engine) sweepLegacyTombstones(ctx context.Context) legacySweepSummary {
	s := e.convertLegacyTombstones(ctx)
	e.logLegacySweep(s)
	return s
}

// convertLegacyTombstones runs the pass family by family, in order, and returns
// what it did. It stops at the first family whose listing or conversion fails,
// and at the first family the engine's context ends, leaving the families after
// it for the next start.
func (e *Engine) convertLegacyTombstones(ctx context.Context) legacySweepSummary {
	started := time.Now()
	var s legacySweepSummary
	for _, fam := range e.legacyTombstoneFamilies() {
		markers, err := e.conn.KVListTombstones(ctx, e.cfg.LoomStateBucket, fam.filter)
		if err != nil {
			s.families = append(s.families, legacySweepFamilyResult{filter: fam.filter, err: err})
			s.stoppedAtFamily, s.err = fam.filter, err
			s.elapsed = time.Since(started)
			return s
		}
		res := e.convertFamily(ctx, fam, markers)
		s.families = append(s.families, res)
		if res.err != nil {
			s.stoppedAtFamily, s.stoppedAtKey, s.err = fam.filter, res.stoppedAtKey, res.err
			s.elapsed = time.Since(started)
			return s
		}
		if res.cancelled != nil {
			s.stoppedAtFamily, s.cancelled = fam.filter, res.cancelled
			s.elapsed = time.Since(started)
			return s
		}
	}
	s.elapsed = time.Since(started)
	return s
}

// convertFamily converts one family's listed markers, pacing between publishes
// when the family feeds a durable and consulting the family's guard, if it has
// one, before each publish. A revision conflict is counted and skipped; any
// other publish error ends the family, and with it the pass.
//
// Cancellation is checked at the top of EVERY marker, paced or not, and is
// classified apart from a failure: nothing went wrong, no key was attempted,
// and the markers left are the next start's work.
func (e *Engine) convertFamily(ctx context.Context, fam legacyTombstoneFamily, markers []substrate.KVTombstone) legacySweepFamilyResult {
	res := legacySweepFamilyResult{filter: fam.filter, listed: len(markers)}
	var pace <-chan time.Time
	if fam.paced && len(markers) > 1 {
		ticker := time.NewTicker(legacySweepInterval)
		defer ticker.Stop()
		pace = ticker.C
	}
	for i, marker := range markers {
		select {
		case <-ctx.Done():
			res.cancelled = ctx.Err()
			return res
		default:
		}
		if pace != nil && i > 0 {
			select {
			case <-ctx.Done():
				res.cancelled = ctx.Err()
				return res
			case <-pace:
			}
		}
		if fam.guard != nil {
			skip, err := fam.guard(ctx, marker.Key)
			if err != nil {
				res.stoppedAtKey, res.err = marker.Key, err
				return res
			}
			if skip {
				res.skippedRunning++
				e.logger.Debug("loom: legacy tombstone skipped",
					"key", marker.Key, "revision", marker.Revision,
					"reason", "the family's guard refused the key")
				continue
			}
		}
		err := e.purgeLegacyMarker(ctx, marker)
		switch {
		case err == nil:
			res.converted++
		case errors.Is(err, substrate.ErrRevisionConflict) || substrate.IsRevisionConflict(err):
			// The subject no longer stands at the revision the listing saw:
			// the key was re-created, or the marker is already gone. Both are
			// the same rejection, and on both the right move is to leave the
			// subject alone.
			res.skippedMismatch++
			e.logger.Debug("loom: legacy tombstone skipped",
				"key", marker.Key, "revision", marker.Revision,
				"reason", "the subject no longer carries the listed marker")
		default:
			res.stoppedAtKey, res.err = marker.Key, err
			return res
		}
	}
	return res
}

// purgeLegacyMarker converts one marker under its own bounded context.
func (e *Engine) purgeLegacyMarker(ctx context.Context, marker substrate.KVTombstone) error {
	pubCtx, cancel := context.WithTimeout(ctx, legacySweepPublishTimeout)
	defer cancel()
	return e.conn.KVPurgeWithTTL(pubCtx, e.cfg.LoomStateBucket, marker.Key, tombstoneTTL, marker.Revision)
}

// logLegacySweep writes the pass's one summary line: a per-family
// listed/converted/skippedMismatch/skippedRunning tuple, the elapsed time, and
// where the pass stopped if it did. A pass that found nothing anywhere and
// completed logs at Debug — the level-triggered no-op stays quiet.
func (e *Engine) logLegacySweep(s legacySweepSummary) {
	attrs := make([]any, 0, 2*len(s.families)+8)
	for _, f := range s.families {
		attrs = append(attrs, f.filter,
			fmt.Sprintf("listed=%d converted=%d skippedMismatch=%d skippedRunning=%d",
				f.listed, f.converted, f.skippedMismatch, f.skippedRunning))
	}
	attrs = append(attrs, "elapsed", s.elapsed.String())
	if s.err != nil {
		stopped := s.stoppedAtFamily
		if s.stoppedAtKey != "" {
			stopped += " key=" + s.stoppedAtKey
		}
		attrs = append(attrs, "stoppedAt", stopped, "error", s.err.Error())
		e.logger.Info("loom: legacy tombstone conversion pass", attrs...)
		return
	}
	if s.cancelled != nil {
		attrs = append(attrs, "cancelledAt", s.stoppedAtFamily, "cancelled", s.cancelled.Error())
		e.logger.Info("loom: legacy tombstone conversion pass", attrs...)
		return
	}
	if s.totalListed() == 0 {
		e.logger.Debug("loom: legacy tombstone conversion pass", attrs...)
		return
	}
	e.logger.Info("loom: legacy tombstone conversion pass", attrs...)
}
