package adjacency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// Neighbors returns the edge list for nodeID from the Adjacency KV, plus the
// KV revision of that node's adjacency document (0 when absent) — a caller
// building a per-evaluation footprint uses the revision to detect a
// mid-evaluation write to this node's edge list.
// Returns an empty (non-nil) slice if the node has no adjacency entry.
// ctx is propagated to the KV read so the caller can cancel during shutdown.
func Neighbors(ctx context.Context, kv *substrate.KV, nodeID string) ([]EdgeEntry, uint64, error) {
	key := subjects.AdjKey(nodeID)
	entry, err := kv.Get(ctx, key)
	if errors.Is(err, substrate.ErrKeyNotFound) {
		return []EdgeEntry{}, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("adjacency: get %s: %w", key, err)
	}
	var val AdjValue
	if err := json.Unmarshal(entry.Value, &val); err != nil {
		return nil, 0, fmt.Errorf("adjacency: unmarshal %s: %w", key, err)
	}
	return val.Edges, entry.Revision, nil
}
