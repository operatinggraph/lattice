package loom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// failedIndexStore is the narrow substrate.Conn surface the backfill needs: the
// sentinel read, the two complete filter resolutions, and the marker write.
// Nothing here can walk the whole keyspace, enumerate through a watcher, or
// remove anything — so the pass's read bound and its writes-only-markers posture
// are compile-time properties rather than review findings. A test replaces the
// field to observe the requests the pass makes.
type failedIndexStore interface {
	KVGet(ctx context.Context, bucket, key string) (*substrate.KVEntry, error)
	KVGetMultiNoSnapshot(ctx context.Context, bucket string, keys []string) (map[string]*substrate.KVEntry, error)
	KVPut(ctx context.Context, bucket, key string, value []byte) (uint64, error)
}

// failedIndexSentinel is the body of backfill.failedIndex: when the pass that
// closed the backfill completed, so an operator reading the key knows which run
// settled it.
type failedIndexSentinel struct {
	CompletedAt string `json:"completedAt"`
}

// failedIndexBackfillSummary is one backfill pass: what it classified, what it
// indexed, and where it stopped if it did not finish.
type failedIndexBackfillSummary struct {
	// skipped reports that the pass read nothing but the sentinel, which records
	// a completed pass — every start after that one, forever.
	skipped bool
	// scanned is the number of cursor records the pass classified.
	scanned int
	// marked is the number of failed instances the pass indexed.
	marked int
	// alreadyMarked is the number of failed instances that already carried a
	// marker when the pass classified them.
	alreadyMarked int
	// remaining is the number of failed instances the pass found to need a marker
	// and never wrote, because it failed or was cancelled. Zero on a completed
	// pass, and the reason the next start runs the whole pass again.
	remaining int
	err       error
	// cancelled holds ctx.Err() when the engine's context ended the pass. It is
	// deliberately not err: nothing failed, and the instances left are simply the
	// next start's work.
	cancelled error
	elapsed   time.Duration
}

// backfillFailedIndex settles the failed index against the cursor records the
// bucket already holds, so the redrive queue the control plane enumerates
// (instance.*.failed — listInstances) covers every failed instance rather than
// only those a terminal transition has indexed. It runs once per bucket lifetime,
// gated by its own sentinel, and logs one summary line.
//
// Why it is needed at all. listInstances enumerates the index, not the cursor
// family, so a cursor reading `failed` with no marker beside it is invisible on
// the one surface an operator uses to find what to redrive. The pass grants that
// visibility; it writes nothing but markers, removes nothing, and has no verdict
// to get wrong — the record it just read is the verdict.
//
// The shape, and why it is complete. Two resolutions through
// KVGetMultiNoSnapshot: one over failedMarkerFilter for the ids already indexed,
// one over instanceCursorFilter for the cursors and their bodies. That primitive
// answers from the STREAM's own subject state — one `multi_last` under the stream
// lock, or a subject-filtered STREAM.INFO plus chunked exact-key reads past the
// 1,024-subject cap — so it cannot come back short because a subject was being
// rewritten while it ran, which a watcher-backed key listing can. No surface that
// decides an operator's queue may rest on a hint. The substrate owns the
// chunking, so this pass carries no page size of its own.
//
// Its memory bound, stated because it is the one wide read left in the package:
// every cursor body at once, ~200 measured bytes each, so single-digit MB at 10⁴
// instances — paid once per bucket lifetime, off the startup path, never on a
// request path.
//
// Idempotent, convergent, and safe beside production. A failed record that
// already carries a marker is counted and skipped; the marker written is the same
// plain PUT with the same body that transition's failed arm writes, so an engine
// failing an instance at the same moment converges rather than conflicts. A
// redrive landing between the classification and the PUT leaves a marker its
// record disagrees with — which listInstances excludes on the record's authority,
// and which that instance's next terminal batch settles either way (transition
// removes the marker on its complete arm).
//
// The sentinel (backfill.failedIndex) is what makes "once" safe, and its lifetime
// is the whole argument. It is CREATED by a pass that completed with nothing left
// owing and no error; a pass that stopped part-way writes it not at all, so the
// next start runs the whole pass again. It is NEVER removed — a bucket wipe takes
// it and the index it tracks together, which is the only state in which re-running
// is right. And because every terminal transition since settles the index itself,
// complete once is complete forever. A present sentinel reduces the pass to one
// KVGet.
//
// An unparseable cursor record is logged and skipped: the pass cannot classify
// it, and one poisoned record must not stop the rest of the bucket being indexed.
// A failing read or write ends the pass where it stands.
func (e *Engine) backfillFailedIndex(ctx context.Context) failedIndexBackfillSummary {
	s := e.settleFailedIndex(ctx)
	e.logFailedIndexBackfill(s)
	return s
}

// settleFailedIndex runs the pass and returns what it did, without logging.
func (e *Engine) settleFailedIndex(ctx context.Context) failedIndexBackfillSummary {
	started := time.Now()
	var s failedIndexBackfillSummary
	bucket := e.cfg.LoomStateBucket

	// Cancellation is checked before the first read as well as between writes, so
	// a pass the engine's context has already ended reports that it attempted
	// nothing rather than a read failure it never really made.
	if err := ctx.Err(); err != nil {
		s.cancelled = err
		s.elapsed = time.Since(started)
		return s
	}

	switch _, err := e.failedIndex.KVGet(ctx, bucket, failedIndexBackfillSentinelKey); {
	case err == nil:
		s.skipped = true
		s.elapsed = time.Since(started)
		return s
	case !errors.Is(err, substrate.ErrKeyNotFound):
		s.err = fmt.Errorf("read %s: %w", failedIndexBackfillSentinelKey, err)
		s.elapsed = time.Since(started)
		return s
	}

	markers, err := e.failedIndex.KVGetMultiNoSnapshot(ctx, bucket, []string{failedMarkerFilter})
	if err != nil {
		s.err = fmt.Errorf("resolve %q: %w", failedMarkerFilter, err)
		s.elapsed = time.Since(started)
		return s
	}
	indexed := make(map[string]struct{}, len(markers))
	for k := range markers {
		if id := instanceIDFromSubKey(k, failedMarkerSuffix); id != "" {
			indexed[id] = struct{}{}
		}
	}

	cursors, err := e.failedIndex.KVGetMultiNoSnapshot(ctx, bucket, []string{instanceCursorFilter})
	if err != nil {
		s.err = fmt.Errorf("resolve %q: %w", instanceCursorFilter, err)
		s.elapsed = time.Since(started)
		return s
	}

	// Classify first, write second. The write loop then knows exactly what it
	// owes, so an interrupted pass reports what it left instead of inferring it
	// from where it stopped reading.
	var pending []string
	for k, entry := range cursors {
		if !isInstanceRecordKey(k) {
			continue
		}
		var inst Instance
		if err := json.Unmarshal(entry.Value, &inst); err != nil {
			e.logger.Warn("loom: failed-index backfill: instance record unparseable; skipping",
				"key", k, "err", err)
			continue
		}
		s.scanned++
		if inst.Status != StatusFailed {
			continue
		}
		if _, ok := indexed[inst.InstanceID]; ok {
			s.alreadyMarked++
			continue
		}
		pending = append(pending, inst.InstanceID)
	}
	// Sorted so a pass that stops part-way stops in a reproducible place — the
	// map it classified from has no order of its own.
	sort.Strings(pending)

	for _, instanceID := range pending {
		if err := ctx.Err(); err != nil {
			s.cancelled = err
			s.remaining = len(pending) - s.marked
			s.elapsed = time.Since(started)
			return s
		}
		if _, err := e.failedIndex.KVPut(ctx, bucket, failedMarkerKey(instanceID), []byte(failedMarkerBody)); err != nil {
			s.err = fmt.Errorf("index failed instance %q: %w", instanceID, err)
			s.remaining = len(pending) - s.marked
			s.elapsed = time.Since(started)
			return s
		}
		s.marked++
	}

	body, err := json.Marshal(failedIndexSentinel{CompletedAt: substrate.FormatTimestamp(time.Now())})
	if err != nil {
		s.err = fmt.Errorf("marshal %s: %w", failedIndexBackfillSentinelKey, err)
		s.elapsed = time.Since(started)
		return s
	}
	if _, err := e.failedIndex.KVPut(ctx, bucket, failedIndexBackfillSentinelKey, body); err != nil {
		// The index is settled either way; only the gate is missing, so the next
		// start repeats a pass that has nothing left to write.
		s.err = fmt.Errorf("write %s: %w", failedIndexBackfillSentinelKey, err)
	}
	s.elapsed = time.Since(started)
	return s
}

// logFailedIndexBackfill writes the pass's one summary line. A pass the sentinel
// skipped, and one that found nothing to index, log at Debug — the steady state,
// which is every start after the first, stays quiet.
func (e *Engine) logFailedIndexBackfill(s failedIndexBackfillSummary) {
	attrs := []any{
		"scanned", s.scanned,
		"marked", s.marked,
		"alreadyMarked", s.alreadyMarked,
		"remaining", s.remaining,
		"elapsed", s.elapsed.String(),
	}
	switch {
	case s.err != nil:
		e.logger.Warn("loom: failed-index backfill", append(attrs, "error", s.err.Error())...)
	case s.cancelled != nil:
		e.logger.Info("loom: failed-index backfill", append(attrs, "cancelled", s.cancelled.Error())...)
	case s.skipped:
		e.logger.Debug("loom: failed-index backfill skipped",
			"reason", failedIndexBackfillSentinelKey+" records a completed pass")
	case s.marked == 0:
		e.logger.Debug("loom: failed-index backfill", attrs...)
	default:
		e.logger.Info("loom: failed-index backfill", attrs...)
	}
}
