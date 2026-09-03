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
	"strings"
	"time"

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

// ParseKey splits a personal-lens-interest key back into the identity and
// device ids Key joined, on the FIRST dot: a device id may contain dots, an
// identity id may not.
//
// That asymmetry holds because a control-plane ActorVerifier rebinds the
// request's identityId to the verified actor's bare NanoID before the
// registration is written. With no verifier configured — the dev/e2e posture
// the Refractor deliberately preserves — the id is self-asserted and only
// checked non-empty, so a dotted identity id would mis-split here. Callers
// must therefore treat a parsed key as an attribution, not a proof: it is
// good enough to render a roster row, and anything that DELETES on the
// strength of it owes its own authoritative check on the artifact the ids
// name.
//
// Fail-closed on any deviation from the shape Key produces: no dot, or an
// empty half, is not a key this package wrote.
func ParseKey(key string) (identityID, deviceID string, ok bool) {
	id, dev, found := strings.Cut(key, ".")
	if !found || id == "" || dev == "" {
		return "", "", false
	}
	return id, dev, true
}

// ParsedRegisteredAt reads the registeredAt instant out of a raw registration
// document's bytes. It exists so a caller outside this package can age a
// registration without declaring a second Go struct for the same JSON shape —
// registrationDoc stays the single spelling of the wire format.
//
// An unparseable document and an unparseable/absent timestamp are distinct
// errors from the caller's point of view only in the message; both mean the
// document did not answer, and a caller deciding whether to remove the
// registration must read either as "keep".
func ParsedRegisteredAt(body []byte) (time.Time, error) {
	var doc registrationDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return time.Time{}, fmt.Errorf("personalinterest: unmarshal registration: %w", err)
	}
	if doc.RegisteredAt == "" {
		return time.Time{}, errors.New("personalinterest: registration carries no registeredAt")
	}
	at, err := time.Parse(time.RFC3339, doc.RegisteredAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("personalinterest: parse registeredAt %q: %w", doc.RegisteredAt, err)
	}
	return at, nil
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
// created reports whether this call CREATED the registration rather than
// updating one that already existed. That distinction is the caller's business,
// not bookkeeping: a created row carries no types and no anchors, and an
// unfiltered registration is what IsRelevant reads as admit-everything — so the
// creating call WIDENS what the identity's personal lenses publish, exactly as
// a register with an empty filter would, and owes the same announcement on the
// Interest Set change edge. An update leaves the filter untouched and changes
// nothing IsRelevant answers.
func SetRevisionCursor(ctx context.Context, kv *substrate.KV, identityID, deviceID string, revision uint64, registeredAt string) (created bool, err error) {
	key, err := Key(identityID, deviceID)
	if err != nil {
		return false, err
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
				return false, fmt.Errorf("personalinterest: unmarshal existing %q: %w", key, uerr)
			}
			expectedRev = entry.Revision
		case errors.Is(getErr, substrate.ErrKeyNotFound):
			create = true
		default:
			return false, fmt.Errorf("personalinterest: get %q: %w", key, getErr)
		}
		doc.RevisionCursor = revision
		doc.HydrationRequestedAt = ""
		body, merr := json.Marshal(doc)
		if merr != nil {
			return false, fmt.Errorf("personalinterest: marshal cursor update for %q: %w", key, merr)
		}
		var casErr error
		if create {
			_, casErr = kv.Create(ctx, key, body)
		} else {
			_, casErr = kv.Update(ctx, key, body, expectedRev)
		}
		if casErr == nil {
			return create, nil
		}
		if errors.Is(casErr, substrate.ErrRevisionConflict) {
			continue
		}
		return false, fmt.Errorf("personalinterest: put %q: %w", key, casErr)
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

// DeregisterRevision removes a device's Interest Set only while the document
// is still at expectedRevision, surfacing substrate.ErrRevisionConflict
// otherwise. It is the deregister a caller owes whenever it READ the
// registration in order to decide the removal was warranted: between that
// read and this call the device can re-register itself, and an unconditional
// delete would silently discard the registration it had just written.
//
// A caller holding no such read — a sign-out purge destroying the device
// anyway — wants Deregister.
func DeregisterRevision(ctx context.Context, kv *substrate.KV, identityID, deviceID string, expectedRevision uint64) error {
	key, err := Key(identityID, deviceID)
	if err != nil {
		return err
	}
	if err := kv.DeleteRevision(ctx, key, expectedRevision); err != nil {
		return fmt.Errorf("personalinterest: delete %q at revision %d: %w", key, expectedRevision, err)
	}
	return nil
}

// Registration is one device's declared Interest Set filter — the only part
// of the stored document the relevance decision reads. The registeredAt /
// cursor bookkeeping is deliberately absent: a caller holding these is
// deciding what to publish, and nothing about that decision depends on when
// the device registered or how far it has been hydrated.
type Registration struct {
	Types   []string
	Anchors []string
}

// Registrations reads every device registration an identity holds, in ONE
// batched read (filter + values in the same round trip), for a caller that
// has many anchors to decide against one identity and would otherwise run
// IsRelevant — and therefore this read — once per anchor. The answer feeds
// RelevantIn, which is the whole of the decision.
//
// An empty result is a real, admitting state, not an error: an identity with
// no registered device receives everything it is authorized to see. A
// document that will not parse propagates as an error instead, so a caller
// can tell "no filter declared" from "the filter could not be read".
func Registrations(ctx context.Context, kv *substrate.KV, identityID string) ([]Registration, error) {
	entries, err := kv.GetMulti(ctx, []string{identityID + ".>"})
	if err != nil {
		return nil, fmt.Errorf("personalinterest: get devices for %q: %w", identityID, err)
	}
	regs := make([]Registration, 0, len(entries))
	for _, entry := range entries {
		var doc registrationDoc
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			return nil, fmt.Errorf("personalinterest: unmarshal %q: %w", entry.Key, err)
		}
		regs = append(regs, Registration{Types: doc.Types, Anchors: doc.Anchors})
	}
	return regs, nil
}

// RelevantIn is the relevance decision itself, over registrations already
// read (personal-secure-lens-design.md §3.3 step 2, the Fire PL.2 relevance
// filter). No registration, or any registration with an empty filter, admits
// everything (true). Otherwise a registration admits the delta when
// anchorType is among its declared Types or anchorID is among its declared
// Anchors; the union of ALL the identity's devices is checked (they share one
// subject) — any one match makes the delta relevant.
//
// It reads nothing, so it holds no posture of its own: staleness is a
// property of when the registrations were read, and belongs to that call.
func RelevantIn(regs []Registration, anchorType, anchorID string) bool {
	if len(regs) == 0 {
		return true
	}
	for _, reg := range regs {
		if len(reg.Types) == 0 && len(reg.Anchors) == 0 {
			return true
		}
		if anchorType != "" && slices.Contains(reg.Types, anchorType) {
			return true
		}
		if anchorID != "" && slices.Contains(reg.Anchors, anchorID) {
			return true
		}
	}
	return false
}

// IsRelevant reports whether identityID should receive a delta for the given
// anchor: one read of the identity's registrations, decided by RelevantIn.
// Reach for Registrations + RelevantIn instead whenever more than one anchor
// is decided against the same identity — this reads the whole registration
// set per call.
func IsRelevant(ctx context.Context, kv *substrate.KV, identityID, anchorType, anchorID string) (bool, error) {
	regs, err := Registrations(ctx, kv, identityID)
	if err != nil {
		return false, err
	}
	return RelevantIn(regs, anchorType, anchorID), nil
}
