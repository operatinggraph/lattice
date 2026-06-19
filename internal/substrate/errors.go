package substrate

import "errors"

// Sentinel errors returned by KV and AtomicBatch operations.
//
// ErrKeyNotFound is returned by KVGet when the requested key does not exist
// (and by KVUpdate/KVDelete when revision conditions reference a key the
// underlying store has no record of).
//
// ErrRevisionConflict is returned by KVCreate / KVUpdate / KVDelete when the
// caller's expected revision does not match the current revision. For
// KVCreate this means the key already exists (create-if-absent failed).
//
// ErrAtomicBatchRejected wraps NATS atomic-batch publish failures (any
// revision-condition rejection, header malformation, or stream-level
// rejection inside the batch). Callers can use errors.Is and the underlying
// error from a wrapping fmt.Errorf chain to extract specifics.
//
// ErrBucketNotFound is returned by KVStatus when the named bucket (or its
// backing stream) does not exist. It is the substrate-typed equivalent of
// jetstream.ErrBucketNotFound / ErrStreamNotFound, letting callers classify a
// missing target as a structural fault without importing jetstream.
var (
	ErrKeyNotFound         = errors.New("substrate: key not found")
	ErrRevisionConflict    = errors.New("substrate: revision conflict")
	ErrAtomicBatchRejected = errors.New("substrate: atomic batch rejected")
	ErrBucketNotFound      = errors.New("substrate: bucket not found")
)
