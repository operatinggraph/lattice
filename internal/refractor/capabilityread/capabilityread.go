// Package capabilityread reads D1's read-path Capability KV projection
// (docs/contracts/06-capability-kv.md §6.14; personal-secure-lens-design.md
// §3.4, Fire PL.3) to answer "may this actor read this anchor?" — the
// correctness boundary the Personal Lens's fan-out filter sits behind.
//
// Every domain that grants read access projects its own slice (core's own
// base lens omits the domain segment) as one small guarded key per granted
// anchor — "cap-read[.<domain>].<actor>.<anchorId>" (cap-read-per-anchor-
// grant-keys-design.md §3.1). Package names are not enumerable statically
// (each vertical owns its own read-grant lens), so IsReadable discovers
// domain-specific keys with wildcarded KV key-listing filters rather than a
// fixed key list. The legacy per-actor aggregate-document shape (one
// producer, one document) is retired — every producer flipped to per-anchor
// keys (Fires 1-2) and the drained legacy documents were purged (Fire 3,
// §6 point 4).
//
// Scope: this reads only the NATS-KV union model. §6.14's Postgres-only
// WildcardAnchor root-grant escape hatch (root-equivalent identities —
// internal/refractor/adapter/rls.go) is never projected into a "cap-read.*"
// document, so a wildcard-holding identity is NOT specially admitted here —
// it would need an explicit per-anchor cap-read grant like any other actor.
// Personal Lens's stated consumer is the per-identity Edge device, not a
// service/root actor, so this is not believed to be load-bearing today.
package capabilityread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// perAnchorEntry is the §3.2 per-key document body a perEntry lens writes at
// "cap-read[.<domain>].<actorSuffix>.<anchorId>" — one guarded key per
// granted anchor. anchorId lives in the key, not the body, so admission is
// membership-by-key: the only field this reader needs is the tombstone flag.
type perAnchorEntry struct {
	IsDeleted bool `json:"isDeleted"`
}

func perAnchorBaseKey(actorSuffix, anchorID string) string {
	return "cap-read." + actorSuffix + "." + anchorID
}
func perAnchorDomainFilter(actorSuffix, anchorID string) string {
	return "cap-read.*." + actorSuffix + "." + anchorID
}

// IsReadable reports whether the actor (actorType, actorID — a Contract #1
// vertex key's two components, e.g. "identity", "Hj4kPmRtw9nbCxz5vQ2y") may
// read anchorID (the resource's bare NanoID, per §6.14's representation
// note). It checks the per-anchor base key, then every per-anchor domain
// key (cap-read-per-anchor-grant-keys-design.md §3.4) — either admits.
//
// Fail-closed throughout: no matching key for the actor+anchor, or every one
// soft-tombstoned (isDeleted:true, §6.8) — deny (false, nil). Only a KV/parse
// error not attributable to plain absence propagates as an error. A stored
// anchor entry literally equal to "*" is never treated as a wildcard match —
// admission is always exact-string equality against the caller's own
// anchorID (the Postgres-only WildcardAnchor escape hatch, §6.14 M5, has no
// NATS-KV projection and is not admitted here).
//
// actorType/actorID feed directly into the NATS-KV wildcard filter — the
// sole caller today (the Personal Lens envelope) only ever passes values
// substrate.ParseVertexKey has already validated against Contract #1's
// vertex-key alphabet, so a NATS subject metacharacter can never reach here
// in practice. IsReadable still rejects one containing "." / "*" / ">"
// itself (as an error, not a silent deny) so a future caller that skips that
// pre-validation fails loudly instead of building a filter that matches a
// different, unintended key shape. anchorID gets the identical hardening.
func IsReadable(ctx context.Context, kv *substrate.KV, actorType, actorID, anchorID string) (bool, error) {
	if anchorID == "" {
		return false, nil
	}
	if actorType == "" || actorID == "" {
		return false, fmt.Errorf("capabilityread: actorType and actorID must both be non-empty")
	}
	if strings.ContainsAny(actorType, ".*>") || strings.ContainsAny(actorID, ".*>") {
		return false, fmt.Errorf("capabilityread: actorType %q / actorID %q must not contain NATS subject metacharacters", actorType, actorID)
	}
	if strings.ContainsAny(anchorID, ".*>") {
		return false, fmt.Errorf("capabilityread: anchorID %q must not contain NATS subject metacharacters", anchorID)
	}
	actorSuffix := actorType + "." + actorID

	admitted, err := checkPerAnchorKey(ctx, kv, perAnchorBaseKey(actorSuffix, anchorID))
	if err != nil {
		return false, err
	}
	if admitted {
		return true, nil
	}
	domainAnchorKeys, _, err := kv.ListKeysFilter(ctx, perAnchorDomainFilter(actorSuffix, anchorID), "", 0)
	if err != nil {
		return false, fmt.Errorf("capabilityread: list per-anchor domain keys for %q/%q: %w", actorSuffix, anchorID, err)
	}
	for _, key := range domainAnchorKeys {
		admitted, err := checkPerAnchorKey(ctx, kv, key)
		if err != nil {
			return false, err
		}
		if admitted {
			return true, nil
		}
	}
	return false, nil
}

// checkPerAnchorKey reads back one per-anchor key and reports whether it is a
// live (non-tombstoned) grant. Absence and a soft-tombstone both read as "not
// admitted here" — the same posture the legacy aggregate-document path takes.
func checkPerAnchorKey(ctx context.Context, kv *substrate.KV, key string) (bool, error) {
	entry, err := kv.Get(ctx, key)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("capabilityread: get %q: %w", key, err)
	}
	var doc perAnchorEntry
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return false, fmt.Errorf("capabilityread: unmarshal %q: %w", key, err)
	}
	if doc.IsDeleted {
		return false, nil
	}
	return true, nil
}
