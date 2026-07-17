// Package overlay is the Edge node's optimistic local-apply layer
// (edge-lattice-full-design.md §3.4, the "Edge Processor" — pure-A: this
// increment shows the caller-supplied intended value directly, with no
// local Starlark prediction; the A′ predictive path is gated on the edge
// Starlark sandbox and not built here). Apply installs the value a
// locally-triggered mutation intends for a key, visible immediately through
// Read at zero latency; the overlay is retired the instant ANY fresher
// confirmed value lands in the Local VAL Store for that key — whether from
// the authoring intent's own eventual commit or an unrelated concurrent
// write — never by local submit success alone (R3: "cleared by the
// authoritative cloud value"). Discard drops an overlay outright when its
// intent is rejected (internal/edge/agent's job).
//
// Overlay also answers the design's "UI Discovery" (§3.4 point 3): Links
// enumerates the confirmed + pending link keys incident on a hub, a
// presentation-only read over the local slice — it never proves cloud
// state and grants no authority.
package overlay

import (
	"encoding/json"
	"fmt"

	"github.com/asolgan/lattice/internal/edge/store"
	"github.com/asolgan/lattice/internal/substrate/keys"
)

// Value is what the UI should show for a key: the confirmed mirror entry,
// or an active pending overlay if one hasn't yet been superseded.
type Value struct {
	Key     string
	Data    json.RawMessage
	Deleted bool
	// Pending is true when Data/Deleted reflect an unconfirmed local
	// overlay rather than the cloud-confirmed value.
	Pending bool
}

// Overlay composes optimistic local-apply over a Local VAL Store.
type Overlay struct {
	store store.Store
}

// New builds an Overlay over st.
func New(st store.Store) *Overlay {
	return &Overlay{store: st}
}

// Apply installs the optimistic overlay for a locally-triggered mutation on
// key (§3.4 step 1): the caller's fresh requestID addresses this overlay for
// a later Discard (agent's reject path), and the store's current confirmed
// revision for key is snapshotted as the baseline Read uses to detect
// supersession. Call before queueing the intent (agent.Enqueue) so the UI
// reflects the change before the operation is even submitted, let alone
// confirmed.
func (o *Overlay) Apply(key, requestID string, data json.RawMessage, deleted bool) error {
	base := uint64(0)
	if cur, ok, err := o.store.Get(key); err != nil {
		return fmt.Errorf("edge/overlay: read baseline for %q: %w", key, err)
	} else if ok {
		base = cur.Revision
	}
	return o.store.PutPending(store.PendingEntry{
		Key:          key,
		RequestID:    requestID,
		Data:         data,
		Deleted:      deleted,
		BaseRevision: base,
	})
}

// Discard drops key's pending overlay outright, if any — the intent's op
// was rejected (agent's job to call this): the optimistic value never
// becomes cloud truth, so showing it further would misrepresent state.
func (o *Overlay) Discard(key string) error {
	return o.store.DeletePending(key)
}

// Read returns what the UI should show for key: an active pending overlay,
// or else the confirmed store entry. ok=false only if neither exists. A
// pending overlay whose baseline the confirmed entry has already advanced
// past is stale — Read retires it (best-effort; a failed cleanup here just
// means a later call re-detects the same supersession harmlessly) and falls
// through to the confirmed value.
func (o *Overlay) Read(key string) (Value, bool, error) {
	confirmed, hasConfirmed, err := o.store.Get(key)
	if err != nil {
		return Value{}, false, fmt.Errorf("edge/overlay: read confirmed %q: %w", key, err)
	}
	pending, hasPending, err := o.store.GetPending(key)
	if err != nil {
		return Value{}, false, fmt.Errorf("edge/overlay: read pending %q: %w", key, err)
	}
	if hasPending {
		if !hasConfirmed || confirmed.Revision <= pending.BaseRevision {
			return Value{Key: key, Data: pending.Data, Deleted: pending.Deleted, Pending: true}, true, nil
		}
		_ = o.store.DeletePending(key)
	}
	if !hasConfirmed {
		return Value{}, false, nil
	}
	return Value{Key: key, Data: confirmed.Data, Deleted: confirmed.Deleted}, true, nil
}

// PendingKeys lists every key with an active (not yet superseded-and-
// retired) pending overlay — internal/edge/agent's local-GC sweep (§3.5)
// uses this to proactively retire overlays a Read never revisits.
func (o *Overlay) PendingKeys() ([]string, error) {
	entries, err := o.store.ListPending()
	if err != nil {
		return nil, fmt.Errorf("edge/overlay: list pending: %w", err)
	}
	keys := make([]string, len(entries))
	for i, e := range entries {
		keys[i] = e.Key
	}
	return keys, nil
}

// Links returns the link keys incident on hub (a vtx.<type>.<id> key) via
// relation, in the given direction — "out" (hub is the link's source) or
// "in" (hub is the target) — merging any pending link creation/deletion
// overlay over the confirmed mirror. Presentation-only (§3.4 "UI
// Discovery"): reflects the local slice as currently mirrored/queued, never
// proves cloud state, and is not an authorization decision.
func (o *Overlay) Links(hub, relation, direction string) ([]string, error) {
	hubType, hubID, ok := keys.ParseVertexKey(hub)
	if !ok {
		return nil, fmt.Errorf("edge/overlay: Links: %q is not a Contract #1 vertex key", hub)
	}
	if direction != "out" && direction != "in" {
		return nil, fmt.Errorf("edge/overlay: Links: direction must be \"out\" or \"in\", got %q", direction)
	}

	candidates := make(map[string]struct{})

	if direction == "out" {
		prefix := keys.LinkPrefix + "." + hubType + "." + hubID + "." + relation + "."
		entries, err := o.store.ScanPrefix(prefix)
		if err != nil {
			return nil, fmt.Errorf("edge/overlay: scan confirmed links: %w", err)
		}
		for _, e := range entries {
			candidates[e.Key] = struct{}{}
		}
	} else {
		// "in": hub is the target side (type2/id2), which isn't a scan
		// prefix — filter the mirror's full link set, bounded by the local
		// slice's size (the user's own activity, never the whole graph).
		entries, err := o.store.ScanPrefix(keys.LinkPrefix + ".")
		if err != nil {
			return nil, fmt.Errorf("edge/overlay: scan confirmed links: %w", err)
		}
		for _, e := range entries {
			_, _, rel, t2, id2, ok := keys.ParseLinkKey(e.Key)
			if ok && rel == relation && t2 == hubType && id2 == hubID {
				candidates[e.Key] = struct{}{}
			}
		}
	}

	pendingKeys, err := o.PendingKeys()
	if err != nil {
		return nil, err
	}
	for _, k := range pendingKeys {
		t1, id1, rel, t2, id2, ok := keys.ParseLinkKey(k)
		if !ok || rel != relation {
			continue
		}
		if (direction == "out" && t1 == hubType && id1 == hubID) ||
			(direction == "in" && t2 == hubType && id2 == hubID) {
			candidates[k] = struct{}{}
		}
	}

	var links []string
	for k := range candidates {
		v, ok, err := o.Read(k)
		if err != nil {
			return nil, fmt.Errorf("edge/overlay: read link %q: %w", k, err)
		}
		if ok && !v.Deleted {
			links = append(links, k)
		}
	}
	return links, nil
}
