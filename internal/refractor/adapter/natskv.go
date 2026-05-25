package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// Compile-time check that NatsKVAdapter satisfies Adapter.
var _ Adapter = (*NatsKVAdapter)(nil)

// NatsKVAdapter writes materialized rows to a NATS KV bucket.
type NatsKVAdapter struct {
	kv       jetstream.KeyValue
	keyOrder []string // ordered key field names; used for deterministic composite key construction
}

// New creates a NatsKVAdapter that writes to kv.
// keyOrder must match the rule's into.key field list and determines the order
// in which key values are concatenated to form the KV key
// (e.g. ["account_id","agreement_id"] → "acct-001.abc123").
func New(kv jetstream.KeyValue, keyOrder []string) (*NatsKVAdapter, error) {
	if len(keyOrder) == 0 {
		return nil, errors.New("natskv: keyOrder must not be empty")
	}
	return &NatsKVAdapter{kv: kv, keyOrder: keyOrder}, nil
}

// buildKey concatenates key field values in keyOrder order, joined with ".".
// Lattice key shape convention (Contract #1) uses "." as the segment
// separator throughout — vtx.<type>.<id>.<aspect>, lnk.<…>, cap.identity.<id>.
// Returns an error if any key field is absent from keys.
func (a *NatsKVAdapter) buildKey(keys map[string]any) (string, error) {
	parts := make([]string, len(a.keyOrder))
	for i, field := range a.keyOrder {
		val, ok := keys[field]
		if !ok {
			return "", fmt.Errorf("natskv: key field %q absent from keys map", field)
		}
		parts[i] = fmt.Sprintf("%v", val)
	}
	return strings.Join(parts, "."), nil
}

// Upsert serializes row to JSON and writes it to the KV bucket under the constructed key,
// creating or overwriting unconditionally (idempotent).
func (a *NatsKVAdapter) Upsert(ctx context.Context, keys map[string]any, row map[string]any) error {
	key, err := a.buildKey(keys)
	if err != nil {
		return fmt.Errorf("natskv upsert: %w", err)
	}
	data, err := json.Marshal(row)
	if err != nil {
		return fmt.Errorf("natskv upsert: marshal row: %w", err)
	}
	if _, err := a.kv.Put(ctx, key, data); err != nil {
		return fmt.Errorf("natskv upsert: put %s: %w", key, err)
	}
	return nil
}

// Delete writes a tombstone document with `isDeleted: true` and a `projectedAt`
// timestamp instead of physically deleting the KV entry. Soft-delete semantics per
// Contract #1 ensure downstream auth-freshness readers see a current timestamp and
// can correctly classify the deletion. Deleting a non-existent key is idempotent.
func (a *NatsKVAdapter) Delete(ctx context.Context, keys map[string]any) error {
	key, err := a.buildKey(keys)
	if err != nil {
		return fmt.Errorf("natskv delete: %w", err)
	}
	tombstone := map[string]any{
		"isDeleted":   true,
		"projectedAt": time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(tombstone)
	if err != nil {
		return fmt.Errorf("natskv delete: marshal tombstone: %w", err)
	}
	if _, err := a.kv.Put(ctx, key, data); err != nil {
		return fmt.Errorf("natskv delete: put tombstone %s: %w", key, err)
	}
	return nil
}

// Probe checks whether the NATS KV bucket is reachable by calling kv.Status.
// Returns nil if the bucket is accessible; returns an infrastructure or structural
// error that failure.Classify can route appropriately.
func (a *NatsKVAdapter) Probe(ctx context.Context) error {
	_, err := a.kv.Status(ctx)
	return err
}

// Close is a no-op; the NATS KV handle lifecycle is managed by the caller.
func (a *NatsKVAdapter) Close() error {
	return nil
}
