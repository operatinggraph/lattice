// Package store is the Edge node's Local VAL Store
// (edge-lattice-full-design.md §3.1): an embedded, transactional local KV
// (bbolt — pure-Go, single-file, no cgo) that mirrors Core KV's partitioned,
// keyed shape. Entries are addressed by the exact Contract #1 key strings
// (vtx.<type>.<id>, vtx.<type>.<id>.<localName>, lnk.<typeA>.<idA>.<rel>.
// <typeB>.<idB>) and carry the projected VAL fragment plus the cloud
// revision that produced it — the reconcile-by-revision cursor the Sync
// Manager (§3.2) applies against.
//
// The store also scaffolds the "local:" namespace for sovereign,
// device-only aspects (drafts, private notes) the Sync Manager never
// uploads (§3.1) — kept in a separate bbolt bucket so the mirror's apply
// path can never reach it.
//
// Two further buckets back the EDGE.2 write path on the same embedded file:
// a pending-overlay bucket (internal/edge/overlay, §3.4 — the optimistic
// value shown for a key while its authoring intent is in flight) and a
// durable intent queue (internal/edge/agent, §3.5 — operation envelopes
// queued for upload, in FIFO submission order). Neither is a confirmed
// mirror entry; both are node-local operational state.
package store

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"

	"go.etcd.io/bbolt"

	"github.com/asolgan/lattice/internal/substrate"
)

const (
	bucketVAL     = "val"     // Contract #1 keyed entries mirrored from the cloud.
	bucketLocal   = "local"   // sovereign, device-only entries — never uploaded.
	bucketMeta    = "meta"    // Sync Manager cursor + node-local bookkeeping.
	bucketPending = "pending" // overlay: optimistic values for in-flight intents (§3.4).
	bucketIntents = "intents" // agent: durable FIFO of queued operation envelopes (§3.5).

	cursorKey = "cursor"

	// manifestKeyPrefix is the reserved projection-row key namespace for a
	// Personal Lens's nats-subject deltas that are not themselves Core-KV
	// keys (edge-showcase-app-design.md §3.1: "manifest row keys ... are
	// projection-row keys, not Core-KV keys — same as my-tasks.* rows").
	// The store mirrors these verbatim alongside Contract #1 entries.
	manifestKeyPrefix = "manifest."
)

// isStorableKey reports whether key is either a valid Contract #1
// vertex/aspect/link key or a reserved manifestKeyPrefix projection-row key.
func isStorableKey(key string) bool {
	return substrate.ClassifyKey(key) != substrate.KindUnknown || strings.HasPrefix(key, manifestKeyPrefix)
}

// Entry is one Local VAL Store record: the projected fragment last applied
// for a Contract #1 key, plus the cloud revision that produced it.
type Entry struct {
	Key      string          `json:"key"`
	Revision uint64          `json:"revision"`
	Data     json.RawMessage `json:"data,omitempty"`
	Deleted  bool            `json:"deleted"`
}

// Store is the Edge node's embedded local VAL mirror.
type Store struct {
	db *bbolt.DB
}

// Open opens (creating if absent) the bbolt-backed local VAL store at path.
func Open(path string) (*Store, error) {
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
	return &Store{db: db}, nil
}

// Close closes the underlying bbolt database.
func (s *Store) Close() error {
	return s.db.Close()
}

// ApplyUpsert applies an inbound "upsert" delta (edge-lattice-full-design.md
// §3.2) under last-writer-wins-by-revision: the write lands iff revision is
// greater than or equal to the currently-stored revision for key (a
// stale/duplicate/reordered delta — JetStream delivers at-least-once and can
// reorder — is dropped). Returns applied=false for a dropped delta, with no
// error. key must be a valid Contract #1 vertex/aspect/link key, or carry
// the reserved manifestKeyPrefix.
func (s *Store) ApplyUpsert(key string, revision uint64, data json.RawMessage) (applied bool, err error) {
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

// ApplyDelete applies an inbound "delete" delta: tombstones the local key
// under the same last-writer-wins-by-revision gate as ApplyUpsert. Returns
// applied=false for a dropped (stale/duplicate) delete, with no error.
func (s *Store) ApplyDelete(key string, revision uint64) (applied bool, err error) {
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

// Get returns the currently-stored entry for key, or ok=false if the store
// holds nothing for it (never hydrated, or evicted by local GC).
func (s *Store) Get(key string) (entry Entry, ok bool, err error) {
	err = s.db.View(func(tx *bbolt.Tx) error {
		entry, ok, err = getEntry(tx.Bucket([]byte(bucketVAL)), key)
		return err
	})
	return entry, ok, err
}

// PendingEntry is one optimistic-overlay record (edge-lattice-full-design.md
// §3.4): the local-only value to show for Key while RequestID's operation is
// still in flight. BaseRevision is the confirmed VAL entry's revision for
// Key at the moment the overlay was applied (0 if the key had never been
// confirmed) — the overlay package uses it to detect supersession: once the
// confirmed entry's revision advances past BaseRevision (from this intent's
// own eventual commit or any other concurrent write), the overlay is stale.
type PendingEntry struct {
	Key          string          `json:"key"`
	RequestID    string          `json:"requestId"`
	Data         json.RawMessage `json:"data,omitempty"`
	Deleted      bool            `json:"deleted"`
	BaseRevision uint64          `json:"baseRevision"`
}

// PutPending writes (or replaces) key's pending overlay.
func (s *Store) PutPending(entry PendingEntry) error {
	v, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("edge/store: encode pending %q: %w", entry.Key, err)
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(bucketPending)).Put([]byte(entry.Key), v)
	})
}

// GetPending returns key's pending overlay, or ok=false if none is active.
func (s *Store) GetPending(key string) (entry PendingEntry, ok bool, err error) {
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

// DeletePending removes key's pending overlay, if any (a no-op if absent).
func (s *Store) DeletePending(key string) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(bucketPending)).Delete([]byte(key))
	})
}

// ListPending returns every currently-active pending overlay. Bounded by the
// number of outstanding local intents (the user's own in-flight edits), not
// the mirror's total size.
func (s *Store) ListPending() ([]PendingEntry, error) {
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

// ScanPrefix returns every confirmed VAL entry whose key has the given
// prefix, in key order. Contract #1 keys sort lexically by type/id/relation,
// so a link-key prefix ("lnk.<type>.<id>.<relation>.", the overlay package's
// UI-discovery use, §3.4) returns exactly that hub+relation's confirmed
// links. Bounded by the local mirror's size — O(user activity), the vault's
// design intent, never O(total entities).
func (s *Store) ScanPrefix(prefix string) ([]Entry, error) {
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

// IntentRecord is one durably-queued outbound operation (edge-lattice-full-
// design.md §3.5): Envelope is the marshaled processor.OperationEnvelope the
// agent package submits on drain. Seq is the store-assigned FIFO order —
// ListIntents returns records in Seq order regardless of insertion timing
// across restarts.
type IntentRecord struct {
	Seq      uint64          `json:"seq"`
	Envelope json.RawMessage `json:"envelope"`
}

// EnqueueIntent durably appends envelope to the intent queue and returns its
// assigned sequence number.
func (s *Store) EnqueueIntent(envelope json.RawMessage) (seq uint64, err error) {
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

// ListIntents returns every queued intent in FIFO (Seq) order.
func (s *Store) ListIntents() ([]IntentRecord, error) {
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

// DeleteIntent removes a queued intent by its assigned sequence number
// (a no-op if already absent) — called once the cloud has authoritatively
// decided the intent's fate (accepted, duplicate, or rejected).
func (s *Store) DeleteIntent(seq uint64) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(bucketIntents)).Delete(seqKey(seq))
	})
}

func seqKey(seq uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, seq)
	return b
}

// PutLocal writes a sovereign, device-only entry under the given name (the
// "local:" namespace, §3.1) — never applied by ApplyUpsert/ApplyDelete and
// never read back by anything that would upload it. name is caller-chosen
// (not a Contract #1 key); no revision is tracked, since nothing reconciles
// this namespace against the cloud.
func (s *Store) PutLocal(name string, data json.RawMessage) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(bucketLocal)).Put([]byte(name), data)
	})
}

// GetLocal reads back a sovereign local-only entry, or ok=false if absent.
func (s *Store) GetLocal(name string) (data json.RawMessage, ok bool, err error) {
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

// Cursor returns the Sync Manager's last-applied stream sequence, or
// ok=false on a fresh store (no cursor persisted yet — the node should
// hydrate, §3.3).
func (s *Store) Cursor() (seq uint64, ok bool, err error) {
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

// SetCursor persists the Sync Manager's last-applied stream sequence, so a
// brief disconnect can resume the durable consumer from it (§3.2).
func (s *Store) SetCursor(seq uint64) error {
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
