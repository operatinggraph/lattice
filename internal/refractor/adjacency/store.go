package adjacency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// Neighbors returns the edge list for nodeID from the Adjacency KV.
// Returns an empty (non-nil) slice if the node has no adjacency entry.
// ctx is propagated to the KV read so the caller can cancel during shutdown.
func Neighbors(ctx context.Context, kv *substrate.KV, nodeID string) ([]EdgeEntry, error) {
	key := subjects.AdjKey(nodeID)
	entry, err := kv.Get(ctx, key)
	if errors.Is(err, substrate.ErrKeyNotFound) {
		return []EdgeEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("adjacency: get %s: %w", key, err)
	}
	var val AdjValue
	if err := json.Unmarshal(entry.Value, &val); err != nil {
		return nil, fmt.Errorf("adjacency: unmarshal %s: %w", key, err)
	}
	return val.Edges, nil
}
