// Package capabilityread reads D1's read-path Capability KV projection
// (docs/contracts/06-capability-kv.md §6.14; personal-secure-lens-design.md
// §3.4, Fire PL.3) to answer "may this actor read this anchor?" — the
// correctness boundary the Personal Lens's fan-out filter sits behind.
//
// Every domain that grants read access projects its own
// "cap-read.<domain>.<actor>" slice (core's own base lens omits the domain
// segment: "cap-read.<actor>"); the actor's effective readable set is the
// union over every slice. Package names are not enumerable statically (each
// vertical owns its own read-grant lens), so IsReadable discovers the
// domain-specific slices with a wildcarded KV key-listing filter rather than
// a fixed key list.
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

// readableAnchor mirrors one entry of §6.14's readableAnchors[]. anchorType is
// audit-only metadata — the contract's representation note is explicit that
// the membership match is NanoID-to-NanoID only, never on type.
type readableAnchor struct {
	AnchorType string   `json:"anchorType"`
	AnchorID   string   `json:"anchorId"`
	Via        []string `json:"via"`
}

// readDoc is the per-lens "cap-read.<source>.<actor>" document shape (§6.14).
type readDoc struct {
	IsDeleted       bool             `json:"isDeleted"`
	ReadableAnchors []readableAnchor `json:"readableAnchors"`
}

func baseKey(actorSuffix string) string      { return "cap-read." + actorSuffix }
func domainFilter(actorSuffix string) string { return "cap-read.*." + actorSuffix }

// IsReadable reports whether the actor (actorType, actorID — a Contract #1
// vertex key's two components, e.g. "identity", "Hj4kPmRtw9nbCxz5vQ2y") may
// read anchorID (the resource's bare NanoID, per §6.14's representation
// note). It unions the base "cap-read.<actor>" lens with every
// "cap-read.<domain>.<actor>" domain lens.
//
// Fail-closed throughout: no contributing slice for the actor, every slice
// soft-tombstoned (isDeleted:true, §6.8), or none whose readableAnchors
// contains anchorID — all deny (false, nil). Only a KV/parse error not
// attributable to plain absence propagates as an error.
//
// actorType/actorID feed directly into the NATS-KV wildcard filter
// (domainFilter) — the sole caller today (the Personal Lens envelope) only
// ever passes values substrate.ParseVertexKey has already validated against
// Contract #1's vertex-key alphabet, so a NATS subject metacharacter can
// never reach here in practice. IsReadable still rejects one containing "."
// / "*" / ">" itself (as an error, not a silent deny) so a future caller that
// skips that pre-validation fails loudly instead of building a filter that
// matches a different, unintended key shape.
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
	actorSuffix := actorType + "." + actorID

	keys := []string{baseKey(actorSuffix)}
	domainKeys, _, err := kv.ListKeysFilter(ctx, domainFilter(actorSuffix), "", 0)
	if err != nil {
		return false, fmt.Errorf("capabilityread: list domain slices for %q: %w", actorSuffix, err)
	}
	keys = append(keys, domainKeys...)

	for _, key := range keys {
		entry, err := kv.Get(ctx, key)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				continue
			}
			return false, fmt.Errorf("capabilityread: get %q: %w", key, err)
		}
		var doc readDoc
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			return false, fmt.Errorf("capabilityread: unmarshal %q: %w", key, err)
		}
		if doc.IsDeleted {
			continue
		}
		for _, a := range doc.ReadableAnchors {
			if a.AnchorID == anchorID {
				return true, nil
			}
		}
	}
	return false, nil
}
