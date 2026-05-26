package substrate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// KVEntry is the typed result of a KV read. Echoes the durable
// properties substrate clients need without exposing the underlying
// jetstream.KeyValueEntry interface.
type KVEntry struct {
	Bucket    string
	Key       string
	Value     []byte
	Revision  uint64
	Timestamp time.Time
}

// KVGet reads the named key from bucket. Returns ErrKeyNotFound if the key
// does not exist (wrapped, so callers should use errors.Is).
//
// Core KV holds logically-deleted entries by design: an envelope written
// with "isDeleted": true remains a live JetStream message and KVGet returns
// it normally (err == nil, Value contains the tombstoned envelope). This is
// intentional — the Refractor lens layer filters logical deletes; raw Core KV
// consumers that need live-only access must inspect the envelope's isDeleted
// field or consume through an appropriate Refractor lens.
//
// KVGet after a hard KVDelete (NATS tombstone) does return ErrKeyNotFound.
func (c *Conn) KVGet(ctx context.Context, bucket, key string) (*KVEntry, error) {
	kv, err := c.bucket(ctx, bucket)
	if err != nil {
		return nil, err
	}
	entry, err := kv.Get(ctx, key)
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return nil, fmt.Errorf("%w: bucket=%s key=%s", ErrKeyNotFound, bucket, key)
		}
		return nil, fmt.Errorf("substrate: KV get %s/%s: %w", bucket, key, err)
	}
	return &KVEntry{
		Bucket:    bucket,
		Key:       entry.Key(),
		Value:     entry.Value(),
		Revision:  entry.Revision(),
		Timestamp: entry.Created(),
	}, nil
}

// KVPut unconditionally writes value to key. Returns the new revision.
//
// Use KVCreate when "must not already exist" is required and KVUpdate when
// a revision-condition is required. KVPut is the right choice only for
// "I don't care about pre-state" scenarios (rare in Lattice — Processor
// always uses create-or-conditional-update inside an atomic batch).
func (c *Conn) KVPut(ctx context.Context, bucket, key string, value []byte) (uint64, error) {
	kv, err := c.bucket(ctx, bucket)
	if err != nil {
		return 0, err
	}
	rev, err := kv.Put(ctx, key, value)
	if err != nil {
		return 0, fmt.Errorf("substrate: KV put %s/%s: %w", bucket, key, err)
	}
	return rev, nil
}

// KVCreate writes value to key only if the key does not already exist.
// Returns ErrRevisionConflict if the key exists.
func (c *Conn) KVCreate(ctx context.Context, bucket, key string, value []byte) (uint64, error) {
	kv, err := c.bucket(ctx, bucket)
	if err != nil {
		return 0, err
	}
	rev, err := kv.Create(ctx, key, value)
	if err != nil {
		if IsRevisionConflict(err) {
			return 0, fmt.Errorf("%w: bucket=%s key=%s (create requires absent): %v",
				ErrRevisionConflict, bucket, key, err)
		}
		return 0, fmt.Errorf("substrate: KV create %s/%s: %w", bucket, key, err)
	}
	return rev, nil
}

// KVUpdate writes value to key only if the current revision matches
// expectedRevision. Returns ErrRevisionConflict if revisions disagree, or
// ErrKeyNotFound if the key was purged out from under the caller.
func (c *Conn) KVUpdate(ctx context.Context, bucket, key string, value []byte, expectedRevision uint64) (uint64, error) {
	kv, err := c.bucket(ctx, bucket)
	if err != nil {
		return 0, err
	}
	rev, err := kv.Update(ctx, key, value, expectedRevision)
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return 0, fmt.Errorf("%w: bucket=%s key=%s", ErrKeyNotFound, bucket, key)
		}
		if IsRevisionConflict(err) {
			return 0, fmt.Errorf("%w: bucket=%s key=%s expected=%d: %v",
				ErrRevisionConflict, bucket, key, expectedRevision, err)
		}
		return 0, fmt.Errorf("substrate: KV update %s/%s: %w", bucket, key, err)
	}
	return rev, nil
}

// KVListKeys returns all keys with live (non-tombstone) entries at the
// JetStream level. The order is unspecified. Used by the Processor's DDL
// cache to enumerate `vtx.meta.>` at startup. Heavy on large buckets —
// callers must scope to buckets where the full key set is bounded (Core KV's
// meta-vertex sub-set qualifies).
//
// KVListKeys does NOT filter logically-deleted envelopes (envelopes with
// "isDeleted": true). Keys for soft-deleted entities (written via the
// Processor commit path) are included in the result. Callers that only want
// live entities must inspect the envelope's isDeleted field after KVGet, or
// consume through a Refractor lens that applies the logical-delete filter.
func (c *Conn) KVListKeys(ctx context.Context, bucket string) ([]string, error) {
	kv, err := c.bucket(ctx, bucket)
	if err != nil {
		return nil, err
	}
	lister, err := kv.ListKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("substrate: KV list %s: %w", bucket, err)
	}
	defer lister.Stop()
	var keys []string
	for k := range lister.Keys() {
		keys = append(keys, k)
	}
	return keys, nil
}

// KVPutWithTTL writes value to key with a per-message TTL. The bucket must
// have been provisioned with AllowMsgTTL (LimitMarkerTTL) enabled; otherwise
// the NATS server ignores the TTL header.
//
// The TTL is set via the NATS-TTL message header published directly to the
// KV subject ($KV.<bucket>.<key>) so that it lands as a JetStream message
// with the TTL header the server honours. This is the same mechanism the
// AtomicBatch path uses for op-tracker entries.
//
// Returns the sequence number of the new message.
func (c *Conn) KVPutWithTTL(ctx context.Context, bucket, key string, value []byte, ttl time.Duration) (uint64, error) {
	if ttl <= 0 {
		return c.KVPut(ctx, bucket, key, value)
	}
	subj := "$KV." + bucket + "." + key
	msg := nats.NewMsg(subj)
	msg.Data = value
	msg.Header.Set("Nats-TTL", ttl.String())
	pubAck, err := c.js.PublishMsg(ctx, msg)
	if err != nil {
		return 0, fmt.Errorf("substrate: KV put-with-ttl %s/%s: %w", bucket, key, err)
	}
	return pubAck.Sequence, nil
}

// KVDelete unconditionally soft-deletes key (writes a NATS KV delete
// marker). Subsequent reads return ErrKeyNotFound. This is unconditional:
// any concurrent write that occurred between the caller's last read and
// this delete will be silently overwritten. Use KVDeleteRevision when
// optimistic concurrency is required; reserve KVDelete for non-concurrent
// operational cleanup (e.g. TTL-expired entries, test teardown).
func (c *Conn) KVDelete(ctx context.Context, bucket, key string) error {
	kv, err := c.bucket(ctx, bucket)
	if err != nil {
		return err
	}
	if err := kv.Delete(ctx, key); err != nil {
		return fmt.Errorf("substrate: KV delete %s/%s: %w", bucket, key, err)
	}
	return nil
}

// KVDeleteRevision soft-deletes key only if its current revision equals
// expectedRevision. Returns ErrRevisionConflict if the revision does not
// match (a concurrent write occurred). Use this in any path that validates
// state before deciding to delete (optimistic-concurrency delete).
func (c *Conn) KVDeleteRevision(ctx context.Context, bucket, key string, expectedRevision uint64) error {
	kv, err := c.bucket(ctx, bucket)
	if err != nil {
		return err
	}
	if err := kv.Delete(ctx, key, jetstream.LastRevision(expectedRevision)); err != nil {
		if IsRevisionConflict(err) {
			return fmt.Errorf("%w: bucket=%s key=%s expected=%d: %v",
				ErrRevisionConflict, bucket, key, expectedRevision, err)
		}
		return fmt.Errorf("substrate: KV delete-revision %s/%s: %w", bucket, key, err)
	}
	return nil
}

// IsRevisionConflict reports whether err is a NATS revision-condition
// rejection. It checks both the typed jetstream.ErrKeyExists sentinel
// (current nats.go) and raw API error strings (older NATS server versions
// that emit err_code=10071 as text). All Lattice components should use
// this helper rather than duplicating the detection logic.
func IsRevisionConflict(err error) bool {
	if err == nil {
		return false
	}
	// nats.go does export jetstream.ErrKeyExists for kv.Create conflicts.
	if errors.Is(err, jetstream.ErrKeyExists) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "wrong last sequence") ||
		strings.Contains(s, "key exists") ||
		strings.Contains(s, "10071")
}
