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

	"github.com/asolgan/lattice/internal/substrate"
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
	keys, err := kv.ListKeysPrefix(ctx, identityID+".")
	if err != nil {
		return false, fmt.Errorf("personalinterest: list devices for %q: %w", identityID, err)
	}
	if len(keys) == 0 {
		return true, nil
	}
	for _, key := range keys {
		entry, err := kv.Get(ctx, key)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				continue
			}
			return false, fmt.Errorf("personalinterest: get %q: %w", key, err)
		}
		var doc registrationDoc
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			return false, fmt.Errorf("personalinterest: unmarshal %q: %w", key, err)
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
