package substrate

import "context"

// KV is a bucket-scoped handle over a substrate Conn. It binds a bucket name
// once so a caller can thread a single *KV through a read/write path instead of
// repeating the bucket argument at every call site — the shape Refractor's
// projection hot loops need (resolve coreKV / adjKV once, pass the handle down).
//
// A KV is a thin (conn, bucket) pair: every method delegates to the
// corresponding Conn.KV* helper, so error mapping (ErrKeyNotFound /
// ErrRevisionConflict / ErrBucketNotFound) and the cached jetstream.KeyValue
// handle are shared with the bucket-based API — no second implementation, and
// no jetstream type on this file's surface. Obtain one via Conn.OpenKV.
//
// A *KV is safe for concurrent use (it holds no per-call state; the underlying
// Conn serializes handle caching).
type KV struct {
	conn   *Conn
	bucket string
}

// OpenKV returns a bucket-scoped handle for an already-provisioned bucket. The
// bucket is opened (not created) eagerly so a missing/misnamed bucket fails here
// rather than on first use; provision buckets via the bootstrap path.
func (c *Conn) OpenKV(ctx context.Context, bucket string) (*KV, error) {
	if _, err := c.bucket(ctx, bucket); err != nil {
		return nil, err
	}
	return &KV{conn: c, bucket: bucket}, nil
}

// Bucket returns the bucket name this handle is bound to.
func (k *KV) Bucket() string { return k.bucket }

// Get reads key. Returns ErrKeyNotFound if absent. See Conn.KVGet.
func (k *KV) Get(ctx context.Context, key string) (*KVEntry, error) {
	return k.conn.KVGet(ctx, k.bucket, key)
}

// Create writes value to key only if absent. Returns ErrRevisionConflict if the
// key exists. See Conn.KVCreate.
func (k *KV) Create(ctx context.Context, key string, value []byte) (uint64, error) {
	return k.conn.KVCreate(ctx, k.bucket, key, value)
}

// Update writes value to key only if its current revision equals
// expectedRevision. Returns ErrRevisionConflict / ErrKeyNotFound. See
// Conn.KVUpdate.
func (k *KV) Update(ctx context.Context, key string, value []byte, expectedRevision uint64) (uint64, error) {
	return k.conn.KVUpdate(ctx, k.bucket, key, value, expectedRevision)
}

// Put unconditionally writes value to key. See Conn.KVPut.
func (k *KV) Put(ctx context.Context, key string, value []byte) (uint64, error) {
	return k.conn.KVPut(ctx, k.bucket, key, value)
}

// Delete soft-deletes key unconditionally. See Conn.KVDelete.
func (k *KV) Delete(ctx context.Context, key string) error {
	return k.conn.KVDelete(ctx, k.bucket, key)
}

// ListKeys returns all live (non-tombstone) keys. See Conn.KVListKeys.
func (k *KV) ListKeys(ctx context.Context) ([]string, error) {
	return k.conn.KVListKeys(ctx, k.bucket)
}

// Purge removes key and its history, leaving a purge marker. Purging an absent
// key returns ErrKeyNotFound. See Conn.KVPurge.
func (k *KV) Purge(ctx context.Context, key string) error {
	return k.conn.KVPurge(ctx, k.bucket, key)
}

// Status probes bucket reachability, returning nil when reachable and
// ErrBucketNotFound when the bucket is gone. See Conn.KVStatus.
func (k *KV) Status(ctx context.Context) error {
	return k.conn.KVStatus(ctx, k.bucket)
}

// WatchUpdates returns a channel of KVEvents for mutations occurring after the
// call (ephemeral, updates-only). The channel closes on ctx cancel or watcher
// stop; reconnect by calling again. See Conn.WatchKVUpdates.
func (k *KV) WatchUpdates(ctx context.Context) (<-chan KVEvent, error) {
	return k.conn.WatchKVUpdates(ctx, k.bucket)
}
