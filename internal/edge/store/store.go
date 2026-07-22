// Package store is the Edge node's Local VAL Store
// (edge-lattice-full-design.md §3.1): an embedded, transactional local KV
// mirroring Core KV's partitioned, keyed shape. Entries are addressed by the
// exact Contract #1 key strings (vtx.<type>.<id>, vtx.<type>.<id>.<localName>,
// lnk.<typeA>.<idA>.<rel>.<typeB>.<idB>) and carry the projected VAL fragment
// plus the cloud revision that produced it — the reconcile-by-revision cursor
// the Sync Manager (§3.2) applies against.
//
// The Store interface is the contract every host's local storage satisfies:
// the bbolt implementation (bolt.go) backs the trusted Go hosts, while a
// browser host backs the same interface with IndexedDB. Store's semantics —
// not any one engine — are what internal/edge/{overlay,sync,agent,vault} are
// written against, and the storetest conformance harness is what defines them.
//
// Beyond the mirror, a Store holds three node-local namespaces: the "local:"
// namespace for sovereign, device-only aspects (drafts, private notes) the
// Sync Manager never uploads (§3.1); a pending-overlay namespace
// (internal/edge/overlay, §3.4 — the optimistic value shown for a key while
// its authoring intent is in flight); and a durable intent queue
// (internal/edge/agent, §3.5 — operation envelopes queued for upload, in FIFO
// submission order). None is a confirmed mirror entry; all are node-local
// operational state.
package store

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/operatinggraph/lattice/internal/substrate/keys"
)

// ErrUnstorableKey is returned by ApplyUpsert/ApplyDelete when key is neither a
// valid Contract #1 vertex/aspect/link key nor a reserved manifest-prefix
// projection-row key. It is a deterministic classification failure — the same
// key will never become storable on redelivery — so a consumer applying an
// at-least-once delta feed (internal/edge/sync) must treat it as terminal
// (Term the message) rather than Nak-and-redeliver into a hot loop. Every Store
// implementation wraps this sentinel so callers can errors.Is against it
// regardless of the backing engine.
var ErrUnstorableKey = errors.New("edge/store: key is not a Contract #1 or manifest key")

// manifestKeyPrefix is the reserved projection-row key namespace for a
// Personal Lens's nats-subject deltas that are not themselves Core-KV keys
// (edge-showcase-app-design.md §3.1: "manifest row keys ... are projection-row
// keys, not Core-KV keys — same as my-tasks.* rows"). A Store mirrors these
// verbatim alongside Contract #1 entries.
const manifestKeyPrefix = "manifest."

// isStorableKey reports whether key is either a valid Contract #1
// vertex/aspect/link key or a reserved manifestKeyPrefix projection-row key.
func isStorableKey(key string) bool {
	return keys.ClassifyKey(key) != keys.KindUnknown || strings.HasPrefix(key, manifestKeyPrefix)
}

// Entry is one Local VAL Store record: the projected fragment last applied
// for a Contract #1 key, plus the cloud revision that produced it.
type Entry struct {
	Key      string          `json:"key"`
	Revision uint64          `json:"revision"`
	Data     json.RawMessage `json:"data,omitempty"`
	Deleted  bool            `json:"deleted"`
	// Sources maps each Personal Lens rule ID currently asserting this key to
	// the revision at which it last did so (personal-lens-retraction-
	// design.md §3.3) — the refcount that lets one lens's keyset frame retract
	// its own attribution while a same-key overlapping lens (e.g.
	// edgeTasksQueued / edgeTasks both projecting manifest.task.<id>) still
	// asserts the key. Nil/empty for an upsert applied with an empty lens
	// (pre-R2 wire back-compat) — such a key is never attribution-tracked and
	// can only be retracted by an explicit ApplyDelete, exactly as before.
	Sources map[string]uint64 `json:"sources,omitempty"`
}

// PendingEntry is one optimistic-overlay record (edge-lattice-full-design.md
// §3.4): the local-only value to show for Key while RequestID's operation is
// still in flight. BaseRevision is the confirmed VAL entry's revision for Key
// at the moment the overlay was applied (0 if the key had never been
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

// IntentRecord is one durably-queued outbound operation (edge-lattice-full-
// design.md §3.5): Envelope is the marshaled processor.OperationEnvelope the
// agent package submits on drain. Seq is the store-assigned FIFO order —
// ListIntents returns records in Seq order regardless of insertion timing
// across restarts.
type IntentRecord struct {
	Seq      uint64          `json:"seq"`
	Envelope json.RawMessage `json:"envelope"`
}

// Store is the Edge node's local VAL mirror plus its node-local operational
// namespaces.
//
// The conformance harness in internal/edge/store/storetest defines the
// behaviour an implementation must exhibit; it is the gate a new backing
// engine passes before the semantics packages are pointed at it.
type Store interface {
	// ApplyUpsert applies an inbound "upsert" delta (edge-lattice-full-
	// design.md §3.2) under last-writer-wins-by-revision: the body/Revision
	// lands iff revision is greater than or equal to the currently-stored
	// revision for key (a stale/duplicate/reordered delta — an at-least-once
	// feed can reorder — is dropped). Returns applied=false for a dropped
	// delta, with no error. key must be a valid Contract #1 vertex/aspect/link
	// key, or carry the reserved manifest prefix.
	//
	// lens is the producing Personal Lens's rule ID (personal-lens-
	// retraction-design.md §3.1/§3.3), or "" for a pre-R2 wire producer with
	// no attribution to report. A non-empty lens additionally, and
	// independently of the body LWW outcome, records this lens's assertion of
	// key at revision (Entry.Sources[lens] = revision) iff revision is at or
	// above whatever this lens last asserted for key — so an upsert that
	// loses the body LWW race still updates its own source's attribution.
	// This attribution write is subject to one more gate: if a prior
	// ApplyKeySet frame from lens has advanced past revision (§3.3's
	// frameHW[lens]) and key is not currently attributed to lens, the whole
	// delta — body and attribution alike — is dropped as applied=false: a
	// Nak'd-then-redelivered stale upsert must not resurrect a key a newer
	// frame already retracted by omission.
	ApplyUpsert(key, lens string, revision uint64, data json.RawMessage) (applied bool, err error)
	// ApplyDelete applies an inbound "delete" delta: tombstones the local key
	// under the same last-writer-wins-by-revision gate as ApplyUpsert, and
	// clears every lens's attribution for key (the legacy, lens-agnostic
	// retraction path — personal-lens-retraction-design.md §3.3 keeps it only
	// for wire back-compat; nothing emits "delete" for a Personal Lens key
	// after R1, which publishes "keyset" frames instead). Returns
	// applied=false for a dropped (stale/duplicate) delete.
	ApplyDelete(key string, revision uint64) (applied bool, err error)
	// ApplyKeySet applies an inbound "keyset" frame (personal-lens-retraction-
	// design.md §3.1/§3.3): lens asserts that, as of revision, it projects
	// exactly keys for this identity. Guarded by a per-lens frame high-water
	// mark — a frame older than the last one applied for lens is dropped
	// whole (applied=false, no prunedKeys), the same redelivery-safety ApplyUpsert's
	// revision gate gives the body. Otherwise: for every stored key currently
	// attributed to lens at a source revision at or below revision, and
	// absent from keys, lens's attribution is removed; a key whose Sources
	// thereby empties is tombstoned exactly as ApplyDelete would (Deleted set,
	// Data cleared, Revision stamped to revision) and returned in prunedKeys
	// so the caller can fire its own change notification. A key present in
	// keys that the store does not hold is ignored — its row arrives as a
	// separate upsert.
	ApplyKeySet(lens string, revision uint64, keys []string) (prunedKeys []string, applied bool, err error)
	// PruneDeadLensAttributions removes every stored key's attribution for any
	// lens not present in liveLenses (personal-lens-retraction-design.md
	// §3.4's hydrate-response lens-set prune: a lens dropped from the DDL, or
	// re-minted under a new rule ID, has no emitter left to heal its stranded
	// attributions, so a completed hydrate is what closes the gap). A key
	// whose Sources thereby empties is tombstoned exactly as ApplyKeySet's
	// prune and returned in prunedKeys.
	PruneDeadLensAttributions(liveLenses []string) (prunedKeys []string, err error)
	// Get returns the currently-stored entry for key, or ok=false if the
	// store holds nothing for it (never hydrated, or evicted by local GC).
	Get(key string) (entry Entry, ok bool, err error)
	// ScanPrefix returns every confirmed VAL entry whose key has the given
	// prefix, in key order. Contract #1 keys sort lexically by
	// type/id/relation, so a link-key prefix ("lnk.<type>.<id>.<relation>.",
	// the overlay package's UI-discovery use, §3.4) returns exactly that
	// hub+relation's confirmed links. Bounded by the local mirror's size —
	// O(user activity), the vault's design intent, never O(total entities).
	ScanPrefix(prefix string) ([]Entry, error)

	// PutPending writes (or replaces) key's pending overlay.
	PutPending(entry PendingEntry) error
	// GetPending returns key's pending overlay, or ok=false if none is active.
	GetPending(key string) (entry PendingEntry, ok bool, err error)
	// DeletePending removes key's pending overlay, if any (a no-op if absent).
	DeletePending(key string) error
	// ListPending returns every currently-active pending overlay. Bounded by
	// the number of outstanding local intents (the user's own in-flight
	// edits), not the mirror's total size.
	ListPending() ([]PendingEntry, error)

	// EnqueueIntent durably appends envelope to the intent queue and returns
	// its assigned sequence number.
	EnqueueIntent(envelope json.RawMessage) (seq uint64, err error)
	// ListIntents returns every queued intent in FIFO (Seq) order.
	ListIntents() ([]IntentRecord, error)
	// DeleteIntent removes a queued intent by its assigned sequence number (a
	// no-op if already absent) — called once the cloud has authoritatively
	// decided the intent's fate (accepted, duplicate, or rejected).
	DeleteIntent(seq uint64) error

	// PutLocal writes a sovereign, device-only entry under the given name (the
	// "local:" namespace, §3.1) — never applied by ApplyUpsert/ApplyDelete and
	// never read back by anything that would upload it. name is caller-chosen
	// (not a Contract #1 key); no revision is tracked, since nothing
	// reconciles this namespace against the cloud.
	PutLocal(name string, data json.RawMessage) error
	// GetLocal reads back a sovereign local-only entry, or ok=false if absent.
	GetLocal(name string) (data json.RawMessage, ok bool, err error)

	// Cursor returns the Sync Manager's last-applied stream sequence, or
	// ok=false on a fresh store (no cursor persisted yet — the node should
	// hydrate, §3.3).
	Cursor() (seq uint64, ok bool, err error)
	// SetCursor persists the Sync Manager's last-applied stream sequence, so a
	// brief disconnect can resume the durable consumer from it (§3.2).
	SetCursor(seq uint64) error

	// Close releases the store's underlying resources.
	Close() error
}
