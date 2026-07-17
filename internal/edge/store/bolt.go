//go:build !js

package store

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"go.etcd.io/bbolt"
)

// The bbolt-backed Store: pure-Go, single-file, no cgo. bbolt is mmap-based
// and so has no js/wasm build, which is why this implementation is excluded
// from that target and a browser host supplies its own Store instead.

const (
	bucketVAL     = "val"     // Contract #1 keyed entries mirrored from the cloud.
	bucketLocal   = "local"   // sovereign, device-only entries — never uploaded.
	bucketMeta    = "meta"    // Sync Manager cursor + node-local bookkeeping.
	bucketPending = "pending" // overlay: optimistic values for in-flight intents (§3.4).
	bucketIntents = "intents" // agent: durable FIFO of queued operation envelopes (§3.5).

	cursorKey = "cursor"
)

// BoltStore is the bbolt-backed Store the trusted Go hosts (cmd/edge,
// cmd/facet) run on.
type BoltStore struct {
	db *bbolt.DB
}

var _ Store = (*BoltStore)(nil)

// Open opens (creating if absent) the bbolt-backed local VAL store at path.
func Open(path string) (*BoltStore, error) {
	db, err := bbolt.Open(path, 0o600, nil)
	if err != nil {
		return nil, fmt.Errorf("edge/store: open %q: %w", path, err)
	}
	err = db.Update(func(tx *bbolt.Tx) error {
		for _, name := range []string{bucketVAL, bucketLocal, bucketMeta, bucketPending, bucketIntents} {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return fmt.Errorf("edge/store: create bucket %q: %w", name, err)
			}
		}
		return nil
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &BoltStore{db: db}, nil
}

// Close closes the underlying bbolt database.
func (s *BoltStore) Close() error {
	return s.db.Close()
}

// ApplyUpsert applies an inbound "upsert" delta under last-writer-wins-by-
// revision; a stale/duplicate/reordered delta is dropped (applied=false, no
// error).
func (s *BoltStore) ApplyUpsert(key string, revision uint64, data json.RawMessage) (applied bool, err error) {
	if !isStorableKey(key) {
		return false, fmt.Errorf("edge/store: ApplyUpsert: %q is not a Contract #1 or manifest key", key)
	}
	err = s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketVAL))
		cur, ok, err := getEntry(b, key)
		if err != nil {
			return err
		}
		if ok && revision < cur.Revision {
			return nil // stale/duplicate — drop, not applied.
		}
		applied = true
		return putEntry(b, Entry{Key: key, Revision: revision, Data: data})
	})
	return applied, err
}

// ApplyDelete tombstones key under the same last-writer-wins-by-revision gate
// as ApplyUpsert.
func (s *BoltStore) ApplyDelete(key string, revision uint64) (applied bool, err error) {
	if !isStorableKey(key) {
		return false, fmt.Errorf("edge/store: ApplyDelete: %q is not a Contract #1 or manifest key", key)
	}
	err = s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketVAL))
		cur, ok, err := getEntry(b, key)
		if err != nil {
			return err
		}
		if ok && revision < cur.Revision {
			return nil // stale/duplicate — drop, not applied.
		}
		applied = true
		return putEntry(b, Entry{Key: key, Revision: revision, Deleted: true})
	})
	return applied, err
}

// Get returns the currently-stored entry for key, or ok=false if absent.
func (s *BoltStore) Get(key string) (entry Entry, ok bool, err error) {
	err = s.db.View(func(tx *bbolt.Tx) error {
		entry, ok, err = getEntry(tx.Bucket([]byte(bucketVAL)), key)
		return err
	})
	return entry, ok, err
}

// PutPending writes (or replaces) key's pending overlay.
func (s *BoltStore) PutPending(entry PendingEntry) error {
	v, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("edge/store: encode pending %q: %w", entry.Key, err)
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(bucketPending)).Put([]byte(entry.Key), v)
	})
}

// GetPending returns key's pending overlay, or ok=false if none is active.
func (s *BoltStore) GetPending(key string) (entry PendingEntry, ok bool, err error) {
	err = s.db.View(func(tx *bbolt.Tx) error {
		v := tx.Bucket([]byte(bucketPending)).Get([]byte(key))
		if v == nil {
			return nil
		}
		if uErr := json.Unmarshal(v, &entry); uErr != nil {
			return fmt.Errorf("edge/store: decode pending %q: %w", key, uErr)
		}
		ok = true
		return nil
	})
	return entry, ok, err
}

// DeletePending removes key's pending overlay, if any.
func (s *BoltStore) DeletePending(key string) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(bucketPending)).Delete([]byte(key))
	})
}

// ListPending returns every currently-active pending overlay.
func (s *BoltStore) ListPending() ([]PendingEntry, error) {
	var entries []PendingEntry
	err := s.db.View(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(bucketPending)).ForEach(func(k, v []byte) error {
			var e PendingEntry
			if err := json.Unmarshal(v, &e); err != nil {
				return fmt.Errorf("edge/store: decode pending %q: %w", k, err)
			}
			entries = append(entries, e)
			return nil
		})
	})
	return entries, err
}

// ScanPrefix returns every confirmed VAL entry whose key has the given prefix,
// in key order.
func (s *BoltStore) ScanPrefix(prefix string) ([]Entry, error) {
	var entries []Entry
	p := []byte(prefix)
	err := s.db.View(func(tx *bbolt.Tx) error {
		c := tx.Bucket([]byte(bucketVAL)).Cursor()
		for k, v := c.Seek(p); k != nil && bytes.HasPrefix(k, p); k, v = c.Next() {
			var e Entry
			if err := json.Unmarshal(v, &e); err != nil {
				return fmt.Errorf("edge/store: decode entry %q: %w", k, err)
			}
			entries = append(entries, e)
		}
		return nil
	})
	return entries, err
}

// EnqueueIntent durably appends envelope to the intent queue and returns its
// assigned sequence number.
func (s *BoltStore) EnqueueIntent(envelope json.RawMessage) (seq uint64, err error) {
	err = s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucketIntents))
		seq, err = b.NextSequence()
		if err != nil {
			return fmt.Errorf("edge/store: next intent sequence: %w", err)
		}
		rec := IntentRecord{Seq: seq, Envelope: envelope}
		v, mErr := json.Marshal(rec)
		if mErr != nil {
			return fmt.Errorf("edge/store: encode intent %d: %w", seq, mErr)
		}
		return b.Put(seqKey(seq), v)
	})
	return seq, err
}

// ListIntents returns every queued intent in FIFO (Seq) order — bbolt iterates
// a bucket in byte order, and seqKey is big-endian precisely so that order is
// numeric.
func (s *BoltStore) ListIntents() ([]IntentRecord, error) {
	var recs []IntentRecord
	err := s.db.View(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(bucketIntents)).ForEach(func(_, v []byte) error {
			var rec IntentRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				return fmt.Errorf("edge/store: decode intent: %w", err)
			}
			recs = append(recs, rec)
			return nil
		})
	})
	return recs, err
}

// DeleteIntent removes a queued intent by its assigned sequence number.
func (s *BoltStore) DeleteIntent(seq uint64) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(bucketIntents)).Delete(seqKey(seq))
	})
}

func seqKey(seq uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, seq)
	return b
}

// PutLocal writes a sovereign, device-only entry under the given name.
func (s *BoltStore) PutLocal(name string, data json.RawMessage) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(bucketLocal)).Put([]byte(name), data)
	})
}

// GetLocal reads back a sovereign local-only entry, or ok=false if absent.
func (s *BoltStore) GetLocal(name string) (data json.RawMessage, ok bool, err error) {
	err = s.db.View(func(tx *bbolt.Tx) error {
		v := tx.Bucket([]byte(bucketLocal)).Get([]byte(name))
		if v == nil {
			return nil
		}
		ok = true
		data = append(json.RawMessage(nil), v...)
		return nil
	})
	return data, ok, err
}

// Cursor returns the Sync Manager's last-applied stream sequence, or ok=false
// on a fresh store.
func (s *BoltStore) Cursor() (seq uint64, ok bool, err error) {
	err = s.db.View(func(tx *bbolt.Tx) error {
		v := tx.Bucket([]byte(bucketMeta)).Get([]byte(cursorKey))
		if v == nil {
			return nil
		}
		if uErr := json.Unmarshal(v, &seq); uErr != nil {
			return fmt.Errorf("edge/store: Cursor: %w", uErr)
		}
		ok = true
		return nil
	})
	return seq, ok, err
}

// SetCursor persists the Sync Manager's last-applied stream sequence.
func (s *BoltStore) SetCursor(seq uint64) error {
	v, err := json.Marshal(seq)
	if err != nil {
		return fmt.Errorf("edge/store: SetCursor: %w", err)
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(bucketMeta)).Put([]byte(cursorKey), v)
	})
}

func getEntry(b *bbolt.Bucket, key string) (entry Entry, ok bool, err error) {
	v := b.Get([]byte(key))
	if v == nil {
		return Entry{}, false, nil
	}
	if err := json.Unmarshal(v, &entry); err != nil {
		return Entry{}, false, fmt.Errorf("edge/store: decode entry %q: %w", key, err)
	}
	return entry, true, nil
}

func putEntry(b *bbolt.Bucket, entry Entry) error {
	v, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("edge/store: encode entry %q: %w", entry.Key, err)
	}
	return b.Put([]byte(entry.Key), v)
}
