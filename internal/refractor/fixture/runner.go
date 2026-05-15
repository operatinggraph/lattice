package fixture

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/engine"
)

// RunFixture seeds adjKV and coreKV from fix, evaluates each input in list order,
// writes results to targetKV via the NatsKVAdapter, then asserts all expected
// outputs in fix.Expect.NatsKV.
//
// All three KV handles must be pre-created and empty before calling RunFixture.
// Callers are responsible for testing.Short() skips when in-memory NATS is required.
func RunFixture(t *testing.T, fix *Fixture, adjKV, coreKV, targetKV jetstream.KeyValue) {
	t.Helper()
	ctx := context.Background()

	// Step 1: Compile query plan.
	ast, err := engine.Parse(fix.Rule.Match)
	require.NoError(t, err, "fixture: parse match query")
	plan, err := engine.Compile(ast, fix.Rule.Into.Key)
	require.NoError(t, err, "fixture: compile query plan")

	// Step 2: Seed adjacency KV before delivering any inputs so edge lookups
	// during evaluation succeed regardless of input order.
	for _, adjEntry := range fix.Adjacency {
		for _, edge := range adjEntry.Edges {
			evt := adjacency.CoreKVEvent{
				CoreKvKey:   edge.CoreKvKey,
				EdgeID:      edge.EdgeID,
				Name:        edge.Name,
				Direction:   edge.Direction,
				NodeID:      adjEntry.NodeID,
				OtherNodeID: edge.OtherNodeID,
				IsDeleted:   false,
			}
			require.NoError(t, adjacency.Build(adjKV, evt),
				"fixture: seed adjacency node_id=%s edgeId=%s", adjEntry.NodeID, edge.EdgeID)
		}
	}

	// Build adapter using the rule's key order.
	adpt, err := adapter.New(targetKV, []string(fix.Rule.Into.Key))
	require.NoError(t, err, "fixture: create nats_kv adapter")

	// Steps 3+4: For each input (in list order): seed Core KV, evaluate, write.
	for _, input := range fix.Inputs {
		data, err := json.Marshal(input.Payload)
		require.NoError(t, err, "fixture: marshal payload key=%s", input.Key)

		_, err = coreKV.Put(ctx, input.Key, data)
		require.NoError(t, err, "fixture: put core kv key=%s", input.Key)

		label, ok := parseCoreKVLabel(input.Key)
		if !ok {
			t.Fatalf("fixture: input key %q does not match node_<label>_<id> format", input.Key)
		}

		isDeleted, _ := input.Payload["isDeleted"].(bool)
		entry := engine.NodeEntry{
			CoreKVKey:  input.Key,
			NodeLabel:  label,
			IsDeleted:  isDeleted,
			Properties: input.Payload,
		}

		results, err := engine.Evaluate(ctx, plan, entry, adjKV, coreKV)
		require.NoError(t, err, "fixture: evaluate key=%s", input.Key)

		for _, result := range results {
			if result.Delete {
				err = adpt.Delete(ctx, result.Keys)
			} else {
				err = adpt.Upsert(ctx, result.Keys, result.Row)
			}
			require.NoError(t, err, "fixture: write result key=%s delete=%v", input.Key, result.Delete)
		}
	}

	// Step 5: Assert expected outputs.
	for _, expected := range fix.Expect.NatsKV {
		if expected.Deleted {
			assertKeyDeleted(t, ctx, targetKV, expected.Key)
		} else {
			assertKeyValue(t, ctx, targetKV, expected.Key, expected.Value)
		}
	}
}

// parseCoreKVLabel extracts the node label from a Core KV key (format: node_<label>_<id>).
// Mirrors pipeline.parseCoreKVKey — kept local to avoid cross-package coupling.
func parseCoreKVLabel(key string) (nodeLabel string, ok bool) {
	parts := strings.SplitN(key, "_", 3)
	if len(parts) < 3 || parts[0] != "node" || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// assertKeyDeleted verifies that key is absent or tombstoned in kv.
func assertKeyDeleted(t *testing.T, ctx context.Context, kv jetstream.KeyValue, key string) {
	t.Helper()
	entry, err := kv.Get(ctx, key)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return // correctly absent
	}
	if err != nil {
		t.Fatalf("fixture: key %q: unexpected error checking deletion: %v", key, err)
	}
	if entry.Operation() == jetstream.KeyValueDelete {
		return // correctly tombstoned
	}
	t.Errorf("fixture: key %q: expected deleted, got value %q", key, string(entry.Value()))
}

// assertKeyValue verifies that key in kv contains exactly the expected JSON value.
func assertKeyValue(t *testing.T, ctx context.Context, kv jetstream.KeyValue, key string, expected map[string]any) {
	t.Helper()
	entry, err := kv.Get(ctx, key)
	require.NoError(t, err, "fixture: key %q not found in target KV", key)
	var actual map[string]any
	require.NoError(t, json.Unmarshal(entry.Value(), &actual), "fixture: key %q: unmarshal value", key)
	assert.Equal(t, expected, actual, "fixture: key %q value mismatch", key)
}
