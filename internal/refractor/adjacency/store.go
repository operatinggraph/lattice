package adjacency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/refractor/subjects"
)

// Neighbors returns the edge list for nodeID from the Adjacency KV.
// Returns an empty (non-nil) slice if the node has no adjacency entry.
func Neighbors(kv jetstream.KeyValue, nodeID string) ([]EdgeEntry, error) {
	ctx := context.Background()
	key := subjects.AdjKey(nodeID)
	entry, err := kv.Get(ctx, key)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return []EdgeEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("adjacency: get %s: %w", key, err)
	}
	var val AdjValue
	if err := json.Unmarshal(entry.Value(), &val); err != nil {
		return nil, fmt.Errorf("adjacency: unmarshal %s: %w", key, err)
	}
	return val.Edges, nil
}
