package processor

import (
	"context"
	"errors"
	"fmt"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// DedupOutcome is the result of a step-2 tracker lookup.
type DedupOutcome int

const (
	// DedupNotFound means the requestId has not been committed before
	// (or the tracker was operator-tombstoned — treated the same per
	// Contract #4 §4.5).
	DedupNotFound DedupOutcome = iota
	// DedupDuplicate means a non-deleted tracker exists for this
	// requestId. The commit path must short-circuit and emit a
	// `duplicate` reply.
	DedupDuplicate
)

// DedupResult captures the lookup outcome plus the tracker payload
// (populated only when Outcome == DedupDuplicate).
type DedupResult struct {
	Outcome DedupOutcome
	Tracker *Tracker

	// TombstonedRevision is the Core KV revision of an operator-tombstoned
	// tracker (present, isDeleted: true — the Contract #4 §4.5 retry signal,
	// reported as DedupNotFound). The commit path threads it to the step-8
	// tracker write as Tracker.SupersedesRevision so the re-execution's
	// tracker supersedes the tombstoned value instead of attempting a
	// create-only write against a subject that still carries a message.
	// nil when the subject held no tracker value at all.
	TombstonedRevision *uint64
}

// CheckDedup performs the step-2 tracker lookup for envelope.RequestID.
// Per Contract #4 §4.5:
//
//   - not present              → DedupNotFound
//   - present, isDeleted=false → DedupDuplicate
//   - present, isDeleted=true  → DedupNotFound (operator-tombstoned retry path)
func CheckDedup(ctx context.Context, conn *substrate.Conn, bucket, requestID string) (DedupResult, error) {
	key := TrackerKey(requestID)
	entry, err := conn.KVGet(ctx, bucket, key)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return DedupResult{Outcome: DedupNotFound}, nil
		}
		return DedupResult{}, fmt.Errorf("dedup: KVGet %s: %w", key, err)
	}
	t, err := ParseTracker(entry.Value)
	if err != nil {
		return DedupResult{}, fmt.Errorf("dedup: parse tracker %s: %w", key, err)
	}
	if t.IsDeleted {
		rev := entry.Revision
		return DedupResult{Outcome: DedupNotFound, TombstonedRevision: &rev}, nil
	}
	return DedupResult{Outcome: DedupDuplicate, Tracker: t}, nil
}
