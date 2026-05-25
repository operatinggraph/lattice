package adjacency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/refractor/subjects"
)

// EdgeEntry is one graph edge stored in the adjacency list for a node.
//
// OtherType is the Contract #1 vertex-type segment of the OTHER endpoint.
// When set, executors can reconstruct the OTHER endpoint's full vertex key
// (vtx.<OtherType>.<OtherNodeID>) for a Core KV point read without needing
// to scan the bucket. OtherType is empty for legacy Materializer-style
// edge events (which carry no type information); executors must fall
// back to coreKV lookup by NodeID-only in that case.
type EdgeEntry struct {
	CoreKvKey   string `json:"coreKvKey"`
	EdgeID      string `json:"edgeId"`
	Name        string `json:"name"`
	Direction   string `json:"direction"`
	OtherNodeID string `json:"otherNodeId"`
	OtherType   string `json:"otherType,omitempty"`
}

// AdjValue is the JSON structure stored at key adj.<nodeId> in the Adjacency KV.
type AdjValue struct {
	Edges []EdgeEntry `json:"edges"`
}

// CoreKVEvent is the parsed payload of an incoming Core KV edge event.
//
// OtherType mirrors EdgeEntry.OtherType — see that comment. The
// adjacency builder propagates this field through to the persisted
// EdgeEntry verbatim.
type CoreKVEvent struct {
	CoreKvKey   string `json:"coreKvKey"`
	EdgeID      string `json:"edgeId"`
	Name        string `json:"name"`
	Direction   string `json:"direction"`
	NodeID      string `json:"nodeId"`      // the node to index under (determines the adj key)
	OtherNodeID string `json:"otherNodeId"` // the other endpoint (bare NodeID)
	OtherType   string `json:"otherType,omitempty"`
	IsDeleted   bool   `json:"isDeleted"`
}

// Build processes a CoreKVEvent and updates adj.<NodeID> in kv using CAS-with-retry.
// ctx is propagated to all KV calls so the caller can cancel during shutdown.
func Build(ctx context.Context, kv jetstream.KeyValue, evt CoreKVEvent) error {
	key := subjects.AdjKey(evt.NodeID)
	edge := EdgeEntry{
		CoreKvKey:   evt.CoreKvKey,
		EdgeID:      evt.EdgeID,
		Name:        evt.Name,
		Direction:   evt.Direction,
		OtherNodeID: evt.OtherNodeID,
		OtherType:   evt.OtherType,
	}
	return upsertEdge(ctx, kv, key, edge, evt.IsDeleted)
}

func upsertEdge(ctx context.Context, kv jetstream.KeyValue, key string, edge EdgeEntry, remove bool) error {
	for {
		var current AdjValue
		var rev uint64

		entry, err := kv.Get(ctx, key)
		switch {
		case errors.Is(err, jetstream.ErrKeyNotFound):
			current = AdjValue{}
			rev = 0
		case err != nil:
			return fmt.Errorf("adjacency: get %s: %w", key, err)
		default:
			rev = entry.Revision()
			if jsonErr := json.Unmarshal(entry.Value(), &current); jsonErr != nil {
				return fmt.Errorf("adjacency: unmarshal %s: %w", key, jsonErr)
			}
		}

		if remove {
			current.Edges = removeEdge(current.Edges, edge.EdgeID)
		} else {
			current.Edges = upsertEntry(current.Edges, edge)
		}

		data, err := json.Marshal(current)
		if err != nil {
			return fmt.Errorf("adjacency: marshal %s: %w", key, err)
		}

		if rev == 0 {
			_, err = kv.Create(ctx, key, data)
			if err == nil {
				return nil
			}
			if errors.Is(err, jetstream.ErrKeyExists) {
				continue
			}
			return fmt.Errorf("adjacency: create %s: %w", key, err)
		}

		_, err = kv.Update(ctx, key, data, rev)
		if err == nil {
			return nil
		}
		if errors.Is(err, jetstream.ErrKeyExists) {
			continue
		}
		return fmt.Errorf("adjacency: update %s: %w", key, err)
	}
}

// upsertEntry adds edge to the list or replaces the existing entry with the same EdgeID.
func upsertEntry(edges []EdgeEntry, edge EdgeEntry) []EdgeEntry {
	for i, e := range edges {
		if e.EdgeID == edge.EdgeID {
			edges[i] = edge
			return edges
		}
	}
	return append(edges, edge)
}

// removeEdge returns a slice with the entry matching edgeID removed.
func removeEdge(edges []EdgeEntry, edgeID string) []EdgeEntry {
	out := edges[:0]
	for _, e := range edges {
		if e.EdgeID != edgeID {
			out = append(out, e)
		}
	}
	return out
}
