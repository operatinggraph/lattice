// Package personalinterest implements the Personal Lens's per-device
// Interest Set (personal-secure-lens-design.md §3.3, Fire PL.2): a device
// registers which entity types/anchors it cares about, so the fan-out
// pipeline can narrow the deltas it publishes to a recipient. This is
// operational subscription state, not business truth (P1) — it lives in its
// own Refractor-owned KV bucket, never Core KV, and is written only by the
// Refractor's own personal.register/.deregister control RPCs.
//
// Absence is never a denial here: a recipient with no registered device gets
// the full authorized slice. The Interest Set is a bandwidth/efficiency
// filter — the D1 security filter (Fire PL.3) is the correctness boundary.
package personalinterest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// registrationDoc is the per-device Interest Set document stored at
// "<identityId>.<deviceId>" in the personal-lens-interest KV bucket — the
// wire shape personal-secure-lens-design.md §3.3 specifies: { types, anchors,
// registeredAt, revisionCursor }.
type registrationDoc struct {
	Types          []string `json:"types,omitempty"`
	Anchors        []string `json:"anchors,omitempty"`
	RegisteredAt   string   `json:"registeredAt"`
	RevisionCursor uint64   `json:"revisionCursor,omitempty"` // populated by Fire PL.4 (hydration); unused here
	// HydrationRequestedAt is an operator-initiated warm-resume request
	// (loupe-flows-edge-depth-ux.md §3.2): edge nodes cannot self-report and
	// no connection state is observable, so this durable flag is the only way
	// to ask a device to re-hydrate on its own next SYNC attach. Cleared by
	// SetRevisionCursor — a completed hydrate always fulfills any pending
	// request, whether or not that particular cycle was the one that
	// consumed it.
	HydrationRequestedAt string `json:"hydrationRequestedAt,omitempty"`
}

// Key builds the personal-lens-interest bucket key for a device's
// registration: "<identityId>.<deviceId>".
func Key(identityID, deviceID string) (string, error) {
	if identityID == "" || deviceID == "" {
		return "", errors.New("personalinterest: identityId and deviceId must both be non-empty")
	}
	return identityID + "." + deviceID, nil
}

// Register upserts a device's Interest Set. types/anchors may both be empty
// (an unfiltered registration — the device still exists as a live consumer,
// so a future revocation flow has something to deregister; IsRelevant reads
// "no filter declared" as "admit everything", the same as no registration
// at all).
func Register(ctx context.Context, kv *substrate.KV, identityID, deviceID string, types, anchors []string, registeredAt string) error {
	key, err := Key(identityID, deviceID)
	if err != nil {
		return err
	}
	doc := registrationDoc{Types: types, Anchors: anchors, RegisteredAt: registeredAt}
	body, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("personalinterest: marshal registration for %q: %w", key, err)
	}
	if _, err := kv.Put(ctx, key, body); err != nil {
		return fmt.Errorf("personalinterest: put %q: %w", key, err)
	}
	return nil
}

// SetRevisionCursor records the high-water revision a device was hydrated
// through (personal-secure-lens-design.md §3.5, Fire PL.4 — the
// "personal.hydrate" control RPC). Read-modify-write on the existing
// registration doc, preserving its Types/Anchors filter; a device with no
// prior registration gets a bare cursor-only doc (registeredAt defaults to
// now), so hydrating an unregistered device still records progress rather
// than failing. Not itself load-bearing for correctness — the Edge decides
// warm-vs-cold hydration from its own local cursor (§3.5); this is
// server-side bookkeeping only.
func SetRevisionCursor(ctx context.Context, kv *substrate.KV, identityID, deviceID string, revision uint64, registeredAt string) error {
	key, err := Key(identityID, deviceID)
	if err != nil {
		return err
	}
	// CAS retry loop: a plain Get-then-Put would lose a concurrent writer's
	// update (e.g. a register call adding a filter racing this hydrate call
	// for the same device) — Update/Create are revision-conditioned, so a
	// conflicting concurrent write surfaces as ErrRevisionConflict and this
	// call retries against the new revision rather than silently clobbering it.
	for {
		doc := registrationDoc{RegisteredAt: registeredAt}
		entry, getErr := kv.Get(ctx, key)
		create := false
		var expectedRev uint64
		switch {
		case getErr == nil:
			if uerr := json.Unmarshal(entry.Value, &doc); uerr != nil {
				return fmt.Errorf("personalinterest: unmarshal existing %q: %w", key, uerr)
			}
			expectedRev = entry.Revision
		case errors.Is(getErr, substrate.ErrKeyNotFound):
			create = true
		default:
			return fmt.Errorf("personalinterest: get %q: %w", key, getErr)
		}
		doc.RevisionCursor = revision
		doc.HydrationRequestedAt = ""
		body, merr := json.Marshal(doc)
		if merr != nil {
			return fmt.Errorf("personalinterest: marshal cursor update for %q: %w", key, merr)
		}
		var casErr error
		if create {
			_, casErr = kv.Create(ctx, key, body)
		} else {
			_, casErr = kv.Update(ctx, key, body, expectedRev)
		}
		if casErr == nil {
			return nil
		}
		if errors.Is(casErr, substrate.ErrRevisionConflict) {
			continue
		}
		return fmt.Errorf("personalinterest: put %q: %w", key, casErr)
	}
}

// RequestHydration marks a registered device for hydration on its next SYNC
// attach (loupe-flows-edge-depth-ux.md §3.2) — an operator-initiated warm
// resume for a device the platform cannot push to directly. CAS retry loop,
// same shape as SetRevisionCursor. Fails if the device has no existing
// registration: there is nothing to mark, and creating a phantom entry would
// put a device that never registered onto the Interest Set roster.
func RequestHydration(ctx context.Context, kv *substrate.KV, identityID, deviceID, requestedAt string) error {
	key, err := Key(identityID, deviceID)
	if err != nil {
		return err
	}
	for {
		entry, getErr := kv.Get(ctx, key)
		if getErr != nil {
			if errors.Is(getErr, substrate.ErrKeyNotFound) {
				return fmt.Errorf("personalinterest: device %q is not registered", key)
			}
			return fmt.Errorf("personalinterest: get %q: %w", key, getErr)
		}
		var doc registrationDoc
		if uerr := json.Unmarshal(entry.Value, &doc); uerr != nil {
			return fmt.Errorf("personalinterest: unmarshal existing %q: %w", key, uerr)
		}
		doc.HydrationRequestedAt = requestedAt
		body, merr := json.Marshal(doc)
		if merr != nil {
			return fmt.Errorf("personalinterest: marshal hydration request for %q: %w", key, merr)
		}
		if _, casErr := kv.Update(ctx, key, body, entry.Revision); casErr != nil {
			if errors.Is(casErr, substrate.ErrRevisionConflict) {
				continue
			}
			return fmt.Errorf("personalinterest: put %q: %w", key, casErr)
		}
		return nil
	}
}

// HydrationRequested reports whether an operator has marked deviceID for
// hydration on its next SYNC attach (RequestHydration) and the request has
// not yet been consumed by a completed hydrate (SetRevisionCursor clears it).
// No registration, or a read failure, reports false rather than erroring —
// this is a bandwidth hint consulted from personalSyncGap (an extra push
// alongside the ordinary gap check), never a correctness gate.
func HydrationRequested(ctx context.Context, kv *substrate.KV, identityID, deviceID string) (bool, error) {
	key, err := Key(identityID, deviceID)
	if err != nil {
		return false, err
	}
	entry, err := kv.Get(ctx, key)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("personalinterest: get %q: %w", key, err)
	}
	var doc registrationDoc
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return false, fmt.Errorf("personalinterest: unmarshal %q: %w", key, err)
	}
	return doc.HydrationRequestedAt != "", nil
}

// Deregister removes a device's Interest Set. Idempotent — deregistering an
// already-absent device is not an error (KV.Delete is itself idempotent).
func Deregister(ctx context.Context, kv *substrate.KV, identityID, deviceID string) error {
	key, err := Key(identityID, deviceID)
	if err != nil {
		return err
	}
	if err := kv.Delete(ctx, key); err != nil {
		return fmt.Errorf("personalinterest: delete %q: %w", key, err)
	}
	return nil
}

// IsRelevant reports whether identityID should receive a delta for the given
// anchor (personal-secure-lens-design.md §3.3 step 2, the Fire PL.2 relevance
// filter). No registered device for identityID, or any registered device with
// an empty filter, admits everything (true). Otherwise a device admits the
// delta when anchorType is among its declared Types or anchorID is among its
// declared Anchors; the union of ALL the identity's devices is checked (they
// share one subject) — any one match makes the delta relevant.
func IsRelevant(ctx context.Context, kv *substrate.KV, identityID, anchorType, anchorID string) (bool, error) {
	// One batched read (filter + values in the same round trip) replaces the
	// former list-then-get-each loop.
	entries, err := kv.GetMulti(ctx, []string{identityID + ".>"})
	if err != nil {
		return false, fmt.Errorf("personalinterest: get devices for %q: %w", identityID, err)
	}
	if len(entries) == 0 {
		return true, nil
	}
	for _, entry := range entries {
		var doc registrationDoc
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			return false, fmt.Errorf("personalinterest: unmarshal %q: %w", entry.Key, err)
		}
		if len(doc.Types) == 0 && len(doc.Anchors) == 0 {
			return true, nil
		}
		if anchorType != "" && slices.Contains(doc.Types, anchorType) {
			return true, nil
		}
		if anchorID != "" && slices.Contains(doc.Anchors, anchorID) {
			return true, nil
		}
	}
	return false, nil
}
