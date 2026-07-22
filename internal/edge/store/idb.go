//go:build js

package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"syscall/js"
)

// The IndexedDB-backed Store: the browser host's persistence
// (edge-browser-node-design.md §3.3). IndexedDB is the only durable,
// transactional, origin-scoped store a browser offers, and it is available on
// a Web Worker — where the wasm engine runs.
//
// Two properties of the engine shape this implementation, and departing from
// either silently breaks the mirror:
//
//   - A transaction is active only while a request it issued is pending, and
//     auto-commits once control returns to the event loop with none
//     outstanding. So a read-modify-write must issue its write from inside the
//     read's success callback — where the transaction is guaranteed active —
//     rather than after a Go channel round-trip. (Go's wasm scheduler happens
//     to resume a blocked goroutine within the callback's own dispatch, which
//     would keep the transaction alive across a plain await; that is an
//     undocumented property of the runtime, and the LWW gate is too load-
//     bearing to rest on it. chainWrite keeps the dependency structural.)
//   - A transaction's completion, not a request's success, is the durability
//     point. Every write here awaits "complete", so what the conformance
//     harness reads back after Reopen is what actually committed.
//
// Transactions with overlapping scope run in the order they were created
// (spec), so concurrent last-writer-wins deltas on one key serialise exactly
// as bbolt's Update does.
const (
	storeVAL     = "val"     // Contract #1 keyed entries mirrored from the cloud.
	storeLocal   = "local"   // sovereign, device-only entries — never uploaded.
	storeMeta    = "meta"    // Sync Manager cursor + node-local bookkeeping.
	storePending = "pending" // overlay: optimistic values for in-flight intents (§3.4).
	storeIntents = "intents" // agent: durable FIFO of queued operation envelopes (§3.5).

	idbCursorKey     = "cursor"
	idbFrameHWPrefix = "frameHW:" // + lens ruleID, in storeMeta.

	// idbSchemaVersion is the IndexedDB database version. Bump it only
	// alongside an upgrade path in onupgradeneeded; the mirror itself is
	// disposable (an eviction or a schema reset re-hydrates from the cursor
	// gap), but the intent queue is not, so a version bump must preserve it.
	//
	// Version 2 (personal-lens-retraction-design.md §3.3): entries gained
	// Sources attribution, which a pre-2 entry never recorded and cannot be
	// safely diffed against a keyset frame — onupgradeneeded purges the
	// mirror + cursor for any oldVersion in [1, 2), same as boltSchemaVersion.
	idbSchemaVersion = 2
)

// IDBStore is the IndexedDB-backed Store the browser host runs on.
type IDBStore struct {
	db   js.Value
	name string
}

var _ Store = (*IDBStore)(nil)

// OpenIDB opens (creating if absent) the IndexedDB-backed local VAL store in
// the current origin under the given database name. The name partitions one
// browser origin's storage per identity, so two identities on one device never
// share a mirror.
func OpenIDB(name string) (*IDBStore, error) {
	idb := js.Global().Get("indexedDB")
	if !idb.Truthy() {
		return nil, errors.New("edge/store: no IndexedDB in this runtime")
	}
	req := idb.Call("open", name, idbSchemaVersion)

	upgrade := js.FuncOf(func(_ js.Value, args []js.Value) any {
		db := req.Get("result")
		names := db.Get("objectStoreNames")
		for _, s := range []string{storeVAL, storeLocal, storeMeta, storePending} {
			if !names.Call("contains", s).Bool() {
				db.Call("createObjectStore", s)
			}
		}
		// The intent queue's sequence numbers come from IndexedDB's own key
		// generator: monotonic, persisted with the store (so it does not
		// restart at 1 after a reopen — the property bbolt's NextSequence
		// gives), and numeric, so a cursor walks the queue in FIFO order
		// without the big-endian key encoding the bbolt engine needs.
		if !names.Call("contains", storeIntents).Bool() {
			opts := map[string]any{"autoIncrement": true}
			db.Call("createObjectStore", storeIntents, opts)
		}
		var oldVersion int
		if len(args) > 0 {
			oldVersion = args[0].Get("oldVersion").Int()
		}
		if oldVersion > 0 && oldVersion < idbSchemaVersion {
			// Sources-attribution migration (idbSchemaVersion's doc comment):
			// purge the mirror + cursor on the same versionchange transaction
			// that bumped the database, so a store the Sync Manager touches
			// after Open never mixes pre-attribution entries with new ones.
			tx := req.Get("transaction")
			tx.Call("objectStore", storeVAL).Call("clear")
			tx.Call("objectStore", storeMeta).Call("delete", idbCursorKey)
		}
		return nil
	})
	defer upgrade.Release()
	req.Set("onupgradeneeded", upgrade)

	db, err := awaitOpen(req)
	if err != nil {
		return nil, fmt.Errorf("edge/store: open IndexedDB %q: %w", name, err)
	}
	return &IDBStore{db: db, name: name}, nil
}

// awaitOpen blocks until an open request succeeds or fails. Unlike an ordinary
// request an open can also fire "blocked" — another connection still holds an
// older version open — and that fires *instead of* success or error, so an
// await that watches only those two stalls forever with no event to wake it.
// A fresh open never blocks, so this is reachable only once idbSchemaVersion
// moves past 1: precisely the case where a second tab is holding the database.
// Failing is what lets the host retry or tell the user; hanging is not.
func awaitOpen(req js.Value) (js.Value, error) {
	resCh := make(chan js.Value, 1)
	errCh := make(chan error, 1)

	onSuccess := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		resCh <- req.Get("result")
		return nil
	})
	defer onSuccess.Release()
	onError := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		errCh <- domError(req.Get("error"))
		return nil
	})
	defer onError.Release()
	onBlocked := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		errCh <- errors.New("blocked: another connection holds an older version of this database open")
		return nil
	})
	defer onBlocked.Release()

	req.Set("onsuccess", onSuccess)
	req.Set("onerror", onError)
	req.Set("onblocked", onBlocked)

	select {
	case res := <-resCh:
		return res, nil
	case err := <-errCh:
		return js.Undefined(), err
	}
}

// Close releases the underlying IndexedDB connection.
func (s *IDBStore) Close() error {
	s.db.Call("close")
	return nil
}

// ApplyUpsert applies an inbound "upsert" delta under last-writer-wins-by-
// revision; a stale/duplicate/reordered delta is dropped (applied=false, no
// error). See the Store interface doc for the lens attribution + frameHW
// guard semantics.
func (s *IDBStore) ApplyUpsert(key, lens string, revision uint64, data json.RawMessage) (applied bool, err error) {
	if !isStorableKey(key) {
		return false, fmt.Errorf("edge/store: ApplyUpsert: %q: %w", key, ErrUnstorableKey)
	}
	if lens == "" {
		// Not applyDelta: that helper overwrites with a wholly fresh Entry,
		// which would silently drop any Sources attribution an earlier
		// attributed upsert already recorded on this key (bolt.go's
		// equivalent path preserves it via `next := cur`; engine parity
		// matters even though no wire producer sends lens="" today).
		tx, val := s.tx(storeVAL, "readwrite")
		var decodeErr error
		chainWrite(val.Call("get", key), func(res js.Value) {
			var cur Entry
			ok := res.Truthy()
			if ok {
				if uErr := json.Unmarshal([]byte(res.String()), &cur); uErr != nil {
					decodeErr = fmt.Errorf("edge/store: decode entry %q: %w", key, uErr)
					return
				}
				if revision < cur.Revision {
					return // stale/duplicate — drop, not applied.
				}
			}
			next := cur
			next.Key = key
			next.Revision = revision
			next.Data = data
			next.Deleted = false
			applied = true
			v, mErr := json.Marshal(next)
			if mErr != nil {
				decodeErr = fmt.Errorf("edge/store: encode entry %q: %w", key, mErr)
				return
			}
			val.Call("put", string(v), key)
		})
		if txErr := awaitTx(tx); txErr != nil {
			return false, fmt.Errorf("edge/store: ApplyUpsert %q: %w", key, txErr)
		}
		if decodeErr != nil {
			return false, decodeErr
		}
		return applied, nil
	}

	tx, stores := s.txMulti([]string{storeVAL, storeMeta}, "readwrite")
	val, meta := stores[storeVAL], stores[storeMeta]
	var decodeErr error
	chainWrite(val.Call("get", key), func(getRes js.Value) {
		var cur Entry
		ok := getRes.Truthy()
		if ok {
			if uErr := json.Unmarshal([]byte(getRes.String()), &cur); uErr != nil {
				decodeErr = fmt.Errorf("edge/store: decode entry %q: %w", key, uErr)
				return
			}
		}
		chainWrite(meta.Call("get", idbFrameHWPrefix+lens), func(hwRes js.Value) {
			var hw uint64
			hasHW := hwRes.Truthy()
			if hasHW {
				if uErr := json.Unmarshal([]byte(hwRes.String()), &hw); uErr != nil {
					decodeErr = fmt.Errorf("edge/store: decode frameHW %q: %w", lens, uErr)
					return
				}
			}
			_, attributed := cur.Sources[lens]
			if hasHW && revision < hw && !attributed {
				return // resurrection guard — dropped whole, not applied.
			}

			bodyWins := !ok || revision >= cur.Revision
			sourceRev, attributedNow := cur.Sources[lens]
			sourceWins := !attributedNow || revision >= sourceRev
			if !bodyWins && !sourceWins {
				return // stale/duplicate on every axis — drop, not applied.
			}

			next := cur
			next.Key = key
			if bodyWins {
				next.Revision = revision
				next.Data = data
				next.Deleted = false
			}
			if sourceWins {
				if next.Sources == nil {
					next.Sources = make(map[string]uint64, 1)
				}
				next.Sources[lens] = revision
			}
			applied = bodyWins
			v, mErr := json.Marshal(next)
			if mErr != nil {
				decodeErr = fmt.Errorf("edge/store: encode entry %q: %w", key, mErr)
				return
			}
			val.Call("put", string(v), key)
		})
	})
	if txErr := awaitTx(tx); txErr != nil {
		return false, fmt.Errorf("edge/store: ApplyUpsert %q: %w", key, txErr)
	}
	if decodeErr != nil {
		return false, decodeErr
	}
	return applied, nil
}

// ApplyDelete tombstones key under the same last-writer-wins-by-revision gate
// as ApplyUpsert, clearing every lens's attribution.
func (s *IDBStore) ApplyDelete(key string, revision uint64) (applied bool, err error) {
	if !isStorableKey(key) {
		return false, fmt.Errorf("edge/store: ApplyDelete: %q: %w", key, ErrUnstorableKey)
	}
	return s.applyDelta(key, Entry{Key: key, Revision: revision, Deleted: true})
}

// ApplyKeySet applies an inbound "keyset" frame: see the Store interface doc
// for the frame high-water guard + per-key attribution-prune semantics.
func (s *IDBStore) ApplyKeySet(lens string, revision uint64, keys []string) (prunedKeys []string, applied bool, err error) {
	keep := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		keep[k] = struct{}{}
	}
	tx, stores := s.txMulti([]string{storeVAL, storeMeta}, "readwrite")
	val, meta := stores[storeVAL], stores[storeMeta]

	var decodeErr error
	chainWrite(meta.Call("get", idbFrameHWPrefix+lens), func(hwRes js.Value) {
		var hw uint64
		hasHW := hwRes.Truthy()
		if hasHW {
			if uErr := json.Unmarshal([]byte(hwRes.String()), &hw); uErr != nil {
				decodeErr = fmt.Errorf("edge/store: decode frameHW %q: %w", lens, uErr)
				return
			}
		}
		if hasHW && revision < hw {
			return // stale/duplicate frame — drop whole, not applied.
		}
		applied = true
		v, mErr := json.Marshal(revision)
		if mErr != nil {
			decodeErr = fmt.Errorf("edge/store: encode frameHW %q: %w", lens, mErr)
			return
		}
		meta.Call("put", string(v), idbFrameHWPrefix+lens)
		walkCursor(val, func(e Entry) (next Entry, write bool) {
			srcRev, attributed := e.Sources[lens]
			if !attributed || srcRev > revision {
				return e, false
			}
			if _, present := keep[e.Key]; present {
				return e, false
			}
			delete(e.Sources, lens)
			if len(e.Sources) == 0 {
				e.Deleted = true
				e.Revision = revision
				e.Data = nil
				prunedKeys = append(prunedKeys, e.Key)
			}
			return e, true
		}, &decodeErr)
	})
	if txErr := awaitTx(tx); txErr != nil {
		return nil, false, fmt.Errorf("edge/store: ApplyKeySet %q: %w", lens, txErr)
	}
	if decodeErr != nil {
		return nil, false, decodeErr
	}
	return prunedKeys, applied, nil
}

// PruneDeadLensAttributions removes every stored key's attribution for any
// lens absent from liveLenses; see the Store interface doc.
func (s *IDBStore) PruneDeadLensAttributions(liveLenses []string) (prunedKeys []string, err error) {
	live := make(map[string]struct{}, len(liveLenses))
	for _, l := range liveLenses {
		live[l] = struct{}{}
	}
	tx, val := s.tx(storeVAL, "readwrite")
	var decodeErr error
	walkCursor(val, func(e Entry) (next Entry, write bool) {
		dead := false
		for l := range e.Sources {
			if _, ok := live[l]; !ok {
				dead = true
				break
			}
		}
		if !dead {
			return e, false
		}
		for l := range e.Sources {
			if _, ok := live[l]; !ok {
				delete(e.Sources, l)
			}
		}
		if len(e.Sources) == 0 {
			e.Deleted = true
			e.Data = nil
			prunedKeys = append(prunedKeys, e.Key)
		}
		return e, true
	}, &decodeErr)
	if txErr := awaitTx(tx); txErr != nil {
		return nil, fmt.Errorf("edge/store: PruneDeadLensAttributions: %w", txErr)
	}
	if decodeErr != nil {
		return nil, decodeErr
	}
	return prunedKeys, nil
}

// walkCursor walks every record in objStore (already scoped to an active
// transaction), decoding each as an Entry and calling decide. When decide
// reports write=true, the record is overwritten in place with next before
// the cursor advances — the write's own request must complete first (like
// every other request here) to keep the transaction active across the walk.
func walkCursor(objStore js.Value, decide func(e Entry) (next Entry, write bool), errOut *error) {
	req := objStore.Call("openCursor")
	var onSuccess js.Func
	onSuccess = js.FuncOf(func(_ js.Value, _ []js.Value) any {
		cur := req.Get("result")
		if !cur.Truthy() {
			onSuccess.Release()
			return nil
		}
		var e Entry
		if uErr := json.Unmarshal([]byte(cur.Get("value").String()), &e); uErr != nil {
			*errOut = fmt.Errorf("edge/store: decode entry: %w", uErr)
			onSuccess.Release()
			return nil
		}
		next, write := decide(e)
		if !write {
			cur.Call("continue")
			return nil
		}
		v, mErr := json.Marshal(next)
		if mErr != nil {
			*errOut = fmt.Errorf("edge/store: encode entry %q: %w", next.Key, mErr)
			onSuccess.Release()
			return nil
		}
		chainWrite(cur.Call("update", string(v)), func(_ js.Value) {
			cur.Call("continue")
		})
		return nil
	})
	req.Set("onsuccess", onSuccess)
}

// applyDelta is the shared last-writer-wins gate: it lands next iff its
// revision is at or above the stored one. The comparison and the write both
// happen inside the read's success callback, so the transaction cannot commit
// between deciding and writing.
func (s *IDBStore) applyDelta(key string, next Entry) (applied bool, err error) {
	tx, os := s.tx(storeVAL, "readwrite")
	var decodeErr error
	chainWrite(os.Call("get", key), func(res js.Value) {
		if res.Truthy() {
			var cur Entry
			if uErr := json.Unmarshal([]byte(res.String()), &cur); uErr != nil {
				decodeErr = fmt.Errorf("edge/store: decode entry %q: %w", key, uErr)
				return
			}
			if next.Revision < cur.Revision {
				return // stale/duplicate — drop, not applied.
			}
		}
		v, mErr := json.Marshal(next)
		if mErr != nil {
			decodeErr = fmt.Errorf("edge/store: encode entry %q: %w", key, mErr)
			return
		}
		applied = true
		os.Call("put", string(v), key)
	})
	if txErr := awaitTx(tx); txErr != nil {
		return false, fmt.Errorf("edge/store: applyDelta %q: %w", key, txErr)
	}
	if decodeErr != nil {
		return false, decodeErr
	}
	return applied, nil
}

// Get returns the currently-stored entry for key, or ok=false if absent.
func (s *IDBStore) Get(key string) (entry Entry, ok bool, err error) {
	v, ok, err := s.getJSON(storeVAL, key)
	if err != nil || !ok {
		return Entry{}, false, err
	}
	if uErr := json.Unmarshal(v, &entry); uErr != nil {
		return Entry{}, false, fmt.Errorf("edge/store: decode entry %q: %w", key, uErr)
	}
	return entry, true, nil
}

// ScanPrefix returns every confirmed VAL entry whose key has the given prefix,
// in key order.
func (s *IDBStore) ScanPrefix(prefix string) ([]Entry, error) {
	var entries []Entry
	err := s.scan(storeVAL, prefix, func(_ string, v []byte) error {
		var e Entry
		if err := json.Unmarshal(v, &e); err != nil {
			return fmt.Errorf("edge/store: decode entry: %w", err)
		}
		entries = append(entries, e)
		return nil
	})
	return entries, err
}

// PutPending writes (or replaces) key's pending overlay.
func (s *IDBStore) PutPending(entry PendingEntry) error {
	v, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("edge/store: encode pending %q: %w", entry.Key, err)
	}
	return s.put(storePending, entry.Key, string(v))
}

// GetPending returns key's pending overlay, or ok=false if none is active.
func (s *IDBStore) GetPending(key string) (entry PendingEntry, ok bool, err error) {
	v, ok, err := s.getJSON(storePending, key)
	if err != nil || !ok {
		return PendingEntry{}, false, err
	}
	if uErr := json.Unmarshal(v, &entry); uErr != nil {
		return PendingEntry{}, false, fmt.Errorf("edge/store: decode pending %q: %w", key, uErr)
	}
	return entry, true, nil
}

// DeletePending removes key's pending overlay, if any.
func (s *IDBStore) DeletePending(key string) error {
	return s.delete(storePending, js.ValueOf(key))
}

// ListPending returns every currently-active pending overlay.
func (s *IDBStore) ListPending() ([]PendingEntry, error) {
	var entries []PendingEntry
	err := s.scan(storePending, "", func(_ string, v []byte) error {
		var e PendingEntry
		if err := json.Unmarshal(v, &e); err != nil {
			return fmt.Errorf("edge/store: decode pending: %w", err)
		}
		entries = append(entries, e)
		return nil
	})
	return entries, err
}

// EnqueueIntent durably appends envelope to the intent queue and returns its
// assigned sequence number.
func (s *IDBStore) EnqueueIntent(envelope json.RawMessage) (seq uint64, err error) {
	tx, os := s.tx(storeIntents, "readwrite")
	// The key generator assigns the sequence, so the record is written twice:
	// "add" to learn the key, then "put" to store the record carrying it. Both
	// requests are in one transaction, so a reader never observes the record
	// without its Seq.
	var chainErr error
	chainWrite(os.Call("add", ""), func(res js.Value) {
		seq = uint64(res.Float())
		rec := IntentRecord{Seq: seq, Envelope: envelope}
		v, mErr := json.Marshal(rec)
		if mErr != nil {
			chainErr = fmt.Errorf("edge/store: encode intent %d: %w", seq, mErr)
			return
		}
		os.Call("put", string(v), res)
	})
	if txErr := awaitTx(tx); txErr != nil {
		return 0, fmt.Errorf("edge/store: EnqueueIntent: %w", txErr)
	}
	if chainErr != nil {
		return 0, chainErr
	}
	return seq, nil
}

// ListIntents returns every queued intent in FIFO (Seq) order — a cursor walks
// the numeric keys the generator assigned, so the order is numeric.
func (s *IDBStore) ListIntents() ([]IntentRecord, error) {
	var recs []IntentRecord
	err := s.scan(storeIntents, "", func(_ string, v []byte) error {
		var rec IntentRecord
		if err := json.Unmarshal(v, &rec); err != nil {
			return fmt.Errorf("edge/store: decode intent: %w", err)
		}
		recs = append(recs, rec)
		return nil
	})
	return recs, err
}

// DeleteIntent removes a queued intent by its assigned sequence number.
func (s *IDBStore) DeleteIntent(seq uint64) error {
	return s.delete(storeIntents, js.ValueOf(float64(seq)))
}

// PutLocal writes a sovereign, device-only entry under the given name.
func (s *IDBStore) PutLocal(name string, data json.RawMessage) error {
	return s.put(storeLocal, name, string(data))
}

// GetLocal reads back a sovereign local-only entry, or ok=false if absent.
func (s *IDBStore) GetLocal(name string) (data json.RawMessage, ok bool, err error) {
	v, ok, err := s.getJSON(storeLocal, name)
	if err != nil || !ok {
		return nil, false, err
	}
	return json.RawMessage(v), true, nil
}

// Cursor returns the Sync Manager's last-applied stream sequence, or ok=false
// on a fresh store.
func (s *IDBStore) Cursor() (seq uint64, ok bool, err error) {
	v, ok, err := s.getJSON(storeMeta, idbCursorKey)
	if err != nil || !ok {
		return 0, false, err
	}
	if uErr := json.Unmarshal(v, &seq); uErr != nil {
		return 0, false, fmt.Errorf("edge/store: Cursor: %w", uErr)
	}
	return seq, true, nil
}

// SetCursor persists the Sync Manager's last-applied stream sequence.
func (s *IDBStore) SetCursor(seq uint64) error {
	v, err := json.Marshal(seq)
	if err != nil {
		return fmt.Errorf("edge/store: SetCursor: %w", err)
	}
	return s.put(storeMeta, idbCursorKey, string(v))
}

// tx opens a transaction scoped to one object store and returns it alongside
// that store's handle.
func (s *IDBStore) tx(name, mode string) (tx js.Value, objStore js.Value) {
	tx = s.db.Call("transaction", js.ValueOf([]any{name}), mode)
	return tx, tx.Call("objectStore", name)
}

// txMulti opens a transaction spanning every store in names and returns each
// store's handle keyed by name — for a read-modify-write that must see and
// mutate more than one object store atomically (e.g. ApplyUpsert's frameHW
// read alongside its val write).
func (s *IDBStore) txMulti(names []string, mode string) (tx js.Value, stores map[string]js.Value) {
	anyNames := make([]any, len(names))
	for i, n := range names {
		anyNames[i] = n
	}
	tx = s.db.Call("transaction", js.ValueOf(anyNames), mode)
	stores = make(map[string]js.Value, len(names))
	for _, n := range names {
		stores[n] = tx.Call("objectStore", n)
	}
	return tx, stores
}

// put writes one JSON-encoded value and waits for the transaction to commit.
func (s *IDBStore) put(storeName, key, value string) error {
	tx, os := s.tx(storeName, "readwrite")
	os.Call("put", value, key)
	if err := awaitTx(tx); err != nil {
		return fmt.Errorf("edge/store: put %q in %q: %w", key, storeName, err)
	}
	return nil
}

// delete removes one key and waits for the transaction to commit. IndexedDB
// deleting an absent key is a no-op, matching bbolt's Delete.
func (s *IDBStore) delete(storeName string, key js.Value) error {
	tx, os := s.tx(storeName, "readwrite")
	os.Call("delete", key)
	if err := awaitTx(tx); err != nil {
		return fmt.Errorf("edge/store: delete from %q: %w", storeName, err)
	}
	return nil
}

// getJSON reads one JSON-encoded value, reporting ok=false for an absent key.
func (s *IDBStore) getJSON(storeName, key string) (data []byte, ok bool, err error) {
	_, os := s.tx(storeName, "readonly")
	res, err := awaitRequest(os.Call("get", key))
	if err != nil {
		return nil, false, fmt.Errorf("edge/store: get %q from %q: %w", key, storeName, err)
	}
	if !res.Truthy() {
		return nil, false, nil
	}
	return []byte(res.String()), true, nil
}

// scan walks storeName in key order, invoking fn for each record. An empty
// prefix walks the whole store; a non-empty one bounds the cursor to the
// prefix range. Contract #1 keys are ASCII, so IndexedDB's UTF-16 code-unit
// ordering is the same byte order the bbolt engine's cursor yields.
func (s *IDBStore) scan(storeName, prefix string, fn func(key string, value []byte) error) error {
	tx, os := s.tx(storeName, "readonly")

	query := js.Null()
	if prefix != "" {
		// "￿" is above every character a Contract #1 key can carry, so it
		// closes the prefix range.
		query = js.Global().Get("IDBKeyRange").Call("bound", prefix, prefix+"￿")
	}

	var fnErr error
	req := os.Call("openCursor", query)
	done := make(chan struct{}, 1)
	onSuccess := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		cur := req.Get("result")
		if !cur.Truthy() {
			done <- struct{}{}
			return nil
		}
		if fnErr == nil {
			fnErr = fn(cur.Get("key").String(), []byte(cur.Get("value").String()))
		}
		if fnErr != nil {
			done <- struct{}{}
			return nil
		}
		// Advancing from inside the success callback is what keeps the
		// transaction active across the walk.
		cur.Call("continue")
		return nil
	})
	defer onSuccess.Release()
	req.Set("onsuccess", onSuccess)

	if err := awaitTx(tx); err != nil {
		return fmt.Errorf("edge/store: scan %q: %w", storeName, err)
	}
	<-done
	return fnErr
}

// chainWrite issues req and calls decide with its result from inside the
// success callback, where the transaction is still active — so decide may
// issue further requests on it. decide must not block.
func chainWrite(req js.Value, decide func(result js.Value)) {
	var onSuccess js.Func
	onSuccess = js.FuncOf(func(_ js.Value, _ []js.Value) any {
		defer onSuccess.Release()
		decide(req.Get("result"))
		return nil
	})
	req.Set("onsuccess", onSuccess)
}

// awaitRequest blocks until req succeeds or fails. Callers that need
// durability must await the transaction instead: a successful request is not a
// committed one.
func awaitRequest(req js.Value) (js.Value, error) {
	resCh := make(chan js.Value, 1)
	errCh := make(chan error, 1)

	onSuccess := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		resCh <- req.Get("result")
		return nil
	})
	defer onSuccess.Release()
	onError := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		errCh <- domError(req.Get("error"))
		return nil
	})
	defer onError.Release()

	req.Set("onsuccess", onSuccess)
	req.Set("onerror", onError)

	select {
	case res := <-resCh:
		return res, nil
	case err := <-errCh:
		return js.Undefined(), err
	}
}

// awaitTx blocks until tx commits, errors, or aborts. Its "complete" event is
// the point the writes are durable.
func awaitTx(tx js.Value) error {
	doneCh := make(chan error, 1)

	onComplete := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		doneCh <- nil
		return nil
	})
	defer onComplete.Release()
	onError := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		doneCh <- domError(tx.Get("error"))
		return nil
	})
	defer onError.Release()
	onAbort := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		doneCh <- fmt.Errorf("transaction aborted: %w", domError(tx.Get("error")))
		return nil
	})
	defer onAbort.Release()

	tx.Set("oncomplete", onComplete)
	tx.Set("onerror", onError)
	tx.Set("onabort", onAbort)

	return <-doneCh
}

// domError renders a DOMException as a Go error.
func domError(e js.Value) error {
	if !e.Truthy() {
		return errors.New("unknown IndexedDB error")
	}
	return fmt.Errorf("%s: %s", e.Get("name").String(), e.Get("message").String())
}
