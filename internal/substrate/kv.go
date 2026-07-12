package substrate

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

// KVCreateWithTTL writes value to key only if the key does not already exist,
// arming a per-key TTL after which the server deletes the entry. Returns
// ErrRevisionConflict if the key exists.
//
// The bucket must be provisioned with LimitMarkerTTL (which enables AllowMsgTTL
// on the underlying stream); NATS enforces a 1-second TTL floor. A ttl <= 0
// falls back to a plain KVCreate (no expiry), mirroring KVPutWithTTL's posture.
//
// The write goes through kv.Create (with the KeyTTL option), never a raw
// publish: kv.Create's CAS is tombstone-aware, so create-after-delete succeeds
// while create-over-live conflicts — the OCC semantics callers rely on.
func (c *Conn) KVCreateWithTTL(ctx context.Context, bucket, key string, value []byte, ttl time.Duration) (uint64, error) {
	if ttl <= 0 {
		return c.KVCreate(ctx, bucket, key, value)
	}
	kv, err := c.bucket(ctx, bucket)
	if err != nil {
		return 0, err
	}
	rev, err := kv.Create(ctx, key, value, jetstream.KeyTTL(ttl))
	if err != nil {
		if IsRevisionConflict(err) {
			return 0, fmt.Errorf("%w: bucket=%s key=%s (create requires absent): %v",
				ErrRevisionConflict, bucket, key, err)
		}
		return 0, fmt.Errorf("substrate: KV create-with-ttl %s/%s: %w", bucket, key, err)
	}
	return rev, nil
}

// KVUpdateWithTTL writes value to key only if the current revision matches
// expectedRevision, arming a per-key TTL on the resulting entry — the
// revision-conditioned analog of KVCreateWithTTL. The new entry's TTL fully
// supersedes the prior entry's (an update IS a TTL re-arm). Returns
// ErrRevisionConflict if the revision does not match — including when a TTL
// marker or delete landed after the caller's read (either bumps the subject's
// last sequence).
//
// jetstream's kv.Update carries no TTL option, so the write composes the same
// two publish options the KV layer's Create/Update use internally: the
// per-subject revision condition and the message TTL, published to the KV
// subject. The bucket must be provisioned with LimitMarkerTTL (NATS enforces
// a 1-second TTL floor); a ttl <= 0 falls back to a plain KVUpdate (no
// expiry).
func (c *Conn) KVUpdateWithTTL(ctx context.Context, bucket, key string, value []byte, expectedRevision uint64, ttl time.Duration) (uint64, error) {
	if ttl <= 0 {
		return c.KVUpdate(ctx, bucket, key, value, expectedRevision)
	}
	msg := nats.NewMsg("$KV." + bucket + "." + key)
	msg.Data = value
	ack, err := c.js.PublishMsg(ctx, msg,
		jetstream.WithExpectLastSequencePerSubject(expectedRevision),
		jetstream.WithMsgTTL(ttl))
	if err != nil {
		if IsRevisionConflict(err) {
			return 0, fmt.Errorf("%w: bucket=%s key=%s expected=%d: %v",
				ErrRevisionConflict, bucket, key, expectedRevision, err)
		}
		return 0, fmt.Errorf("substrate: KV update-with-ttl %s/%s: %w", bucket, key, err)
	}
	return ack.Sequence, nil
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
	// The lister's feed goroutine exits (closing the channel) on ctx expiry
	// exactly as it does on completion, so a timed-out listing is otherwise
	// indistinguishable from a complete one — a silently PARTIAL key set,
	// which callers would act on as the full corpus (e.g. an installer
	// concluding a package is absent). Surface the expiry as an error.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("substrate: KV list %s: interrupted (partial result discarded): %w", bucket, err)
	}
	return keys, nil
}

// KVListKeysPrefix returns every key under the given key prefix at the
// JetStream level — a server-side subject-filtered list (`prefix>` over the
// KV stream), far lighter than KVListKeys for a bucket whose full key set is
// large but whose prefixed sub-set is bounded (e.g. `lnk.object.` — the object
// link space, bounded by the count of attached objects). prefix MUST end at a
// key-token boundary (a trailing "."), so the filter `prefix + ">"` selects all
// keys nested under it. The order is unspecified.
//
// Like KVListKeys, this does NOT filter logically-deleted envelopes: a
// SOFT-tombstoned entity (in-body "isDeleted": true, written via the Processor
// commit path) is still a live JetStream entry and IS returned — callers that
// want only live entities must KVGet each and inspect isDeleted. (The underlying
// ListKeysFiltered's IgnoreDeletes drops only NATS hard-delete markers, which
// the Processor never writes.)
func (c *Conn) KVListKeysPrefix(ctx context.Context, bucket, prefix string) ([]string, error) {
	kv, err := c.bucket(ctx, bucket)
	if err != nil {
		return nil, err
	}
	lister, err := kv.ListKeysFiltered(ctx, prefix+">")
	if err != nil {
		return nil, fmt.Errorf("substrate: KV list %s prefix %q: %w", bucket, prefix, err)
	}
	defer lister.Stop()
	var keys []string
	for k := range lister.Keys() {
		keys = append(keys, k)
	}
	// Same silent-partial hazard as KVListKeys: ctx expiry closes the feed
	// channel indistinguishably from completion.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("substrate: KV list %s prefix %q: interrupted (partial result discarded): %w", bucket, prefix, err)
	}
	return keys, nil
}

// KVListKeysFilter returns a page of keys matching an arbitrary NATS subject
// filter at the JetStream level — the general form of KVListKeysPrefix, which
// can only express a trailing `prefix>`. The filter is a key pattern over the
// KV keyspace (the substrate prepends the `$KV.<bucket>.` subject prefix), so
// `*` matches exactly one key token and `>` matches one-or-more trailing
// tokens. This lets a caller select a hub's links in EITHER direction of a
// 6-segment link key `lnk.<srcType>.<srcId>.<rel>.<tgtType>.<tgtId>`:
//   - source-bounded: `lnk.<t>.<id>.<rel>.>`        (hub id in the prefix)
//   - target-bounded: `lnk.*.*.<rel>.<t>.<id>`      (hub id in the suffix)
// Both are server-side subject filters, so the read is bounded by the hub's
// degree in that direction — never the keyspace.
//
// Paging. The matching keys are sorted lexicographically and the page is the
// keys strictly greater than cursor (empty cursor = from the start), up to
// limit keys. nextCursor is the page's last key when more keys remain, or ""
// when the filter is exhausted — the caller re-invokes with cursor=nextCursor
// until it comes back "". A non-positive limit returns every matching key in
// one page (nextCursor=""). The key list (keys only, no values) is cheap; the
// caller pages to bound the per-key value reads it does downstream.
//
// Like KVListKeysPrefix, this does NOT filter logically-deleted envelopes: a
// soft-tombstoned entity (in-body "isDeleted": true) is still a live JetStream
// entry and IS returned. Only NATS hard-delete markers are dropped (the
// underlying ListKeysFiltered's IgnoreDeletes, which the Processor never
// writes). Callers wanting only live entities KVGet each and inspect isDeleted.
func (c *Conn) KVListKeysFilter(ctx context.Context, bucket, filter, cursor string, limit int) (keys []string, nextCursor string, err error) {
	kv, err := c.bucket(ctx, bucket)
	if err != nil {
		return nil, "", err
	}
	lister, err := kv.ListKeysFiltered(ctx, filter)
	if err != nil {
		return nil, "", fmt.Errorf("substrate: KV list %s filter %q: %w", bucket, filter, err)
	}
	defer lister.Stop()
	var collected []string
	for k := range lister.Keys() {
		collected = append(collected, k)
	}
	// The keyLister has no error channel: on a context cancellation (e.g. the
	// op's wall budget firing mid-enumeration) its goroutine simply closes the
	// keys channel, so the range above ends NORMALLY with a partial set. Return
	// the context error rather than a silently-truncated page — a set guard
	// reading a partial neighbor set would pass a constraint it never checked.
	if err := ctx.Err(); err != nil {
		return nil, "", fmt.Errorf("substrate: KV list %s filter %q: %w", bucket, filter, err)
	}
	page, next := pageFilteredKeys(collected, cursor, limit)
	return page, next, nil
}

// pageFilteredKeys sorts, de-duplicates, cursor-filters, and pages a set of
// matched keys. It is the pure core of KVListKeysFilter, factored out so the
// paging invariants are unit-testable without a live KV.
//
// De-dup is load-bearing: the pinned NATS KV ListKeysFiltered "may report
// duplicate keys" on a bucket with frequent concurrent writes, and a duplicate
// at the page boundary would otherwise advance nextCursor past — and so skip — a
// distinct key on the next page (membership loss, not just a repeat). Keys are
// unique in the store, so de-dup of the sorted enumeration is exact.
//
// cursor exclusion is strict-greater-than, so the boundary key (which is itself
// returned as nextCursor) is excluded from the next page — no overlap, no gap.
// limit<=0 returns every matching key in one page (nextCursor="").
func pageFilteredKeys(keys []string, cursor string, limit int) (page []string, nextCursor string) {
	sort.Strings(keys)
	var matched []string
	for i, k := range keys {
		if i > 0 && k == keys[i-1] {
			continue // adjacent duplicate from the lister
		}
		if cursor != "" && k <= cursor {
			continue // already returned on an earlier page
		}
		matched = append(matched, k)
	}
	if limit > 0 && len(matched) > limit {
		nextCursor = matched[limit-1]
		matched = matched[:limit]
	}
	return matched, nextCursor
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

// KVPurge removes key and all of its prior revisions from bucket, leaving a
// purge marker as the latest revision (a subsequent KVGet returns
// ErrKeyNotFound). Unlike KVDelete (a soft delete-marker that preserves
// history), Purge reclaims the per-subject history — used by a rebuild's
// truncate so a guarded replay sees an absent key, not a stale watermark.
// Purging an already-absent key is a no-op (returns nil) — Purge is idempotent,
// so a whole-bucket truncate need not special-case a key deleted out from under
// it.
func (c *Conn) KVPurge(ctx context.Context, bucket, key string) error {
	kv, err := c.bucket(ctx, bucket)
	if err != nil {
		return err
	}
	if err := kv.Purge(ctx, key); err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return nil
		}
		return fmt.Errorf("substrate: KV purge %s/%s: %w", bucket, key, err)
	}
	return nil
}

// KVStatus probes whether bucket is reachable, returning nil when it is. A
// missing bucket (or backing stream) is mapped to ErrBucketNotFound so callers
// can classify it as a structural fault; any other error is wrapped verbatim
// (transient/infra). The status payload itself is discarded — this is a
// liveness probe, not a metrics read.
func (c *Conn) KVStatus(ctx context.Context, bucket string) error {
	kv, err := c.bucket(ctx, bucket)
	if err != nil {
		if errors.Is(err, jetstream.ErrBucketNotFound) || errors.Is(err, jetstream.ErrStreamNotFound) {
			return fmt.Errorf("%w: bucket=%s", ErrBucketNotFound, bucket)
		}
		return err
	}
	if _, err := kv.Status(ctx); err != nil {
		if errors.Is(err, jetstream.ErrBucketNotFound) || errors.Is(err, jetstream.ErrStreamNotFound) {
			return fmt.Errorf("%w: bucket=%s", ErrBucketNotFound, bucket)
		}
		return fmt.Errorf("substrate: KV status %s: %w", bucket, err)
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
