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
	"log/slog"
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

// KeyPrefix is the literal every D1 read-grant key in Capability KV starts
// with — the base lens's own "cap-read.<actorSuffix>.<anchorId>" and every
// domain producer's "cap-read.<domain>.<actorSuffix>.<anchorId>" alike
// (cap-read-per-anchor-grant-keys-design.md §3.1).
//
// It is exported so the READER's key construction below and the PRODUCER-side
// classification that decides which lenses announce a grant change
// (projection.InstallActorAggregate) consume the same literal. That is the
// whole point of exporting it: a producer classified by a copy of this string
// could drift from the reader that has to find the key, and the failure mode of
// that drift is a security filter whose change edge silently covers nothing.
const KeyPrefix = "cap-read."

func perAnchorBaseKey(actorSuffix, anchorID string) string {
	return KeyPrefix + actorSuffix + "." + anchorID
}
func perAnchorDomainFilter(actorSuffix, anchorID string) string {
	return KeyPrefix + "*." + actorSuffix + "." + anchorID
}

// perAnchorBaseSetFilter and perAnchorDomainSetFilter are the whole-actor
// counterparts of the two single-anchor key shapes: every base key the actor
// holds ("cap-read.<actorSuffix>.*") and every domain key
// ("cap-read.*.<actorSuffix>.*").
//
// The two can never match the same subject — a base key is four tokens and a
// domain key five, and NATS "*" matches exactly one token — so they are safe
// to pass to one multi-get, which refuses overlapping filters past its
// fast-path cap (substrate.Conn.kvGetMultiFallback).
func perAnchorBaseSetFilter(actorSuffix string) string {
	return KeyPrefix + actorSuffix + ".*"
}
func perAnchorDomainSetFilter(actorSuffix string) string {
	return KeyPrefix + "*." + actorSuffix + ".*"
}

// AnchorSet is one actor's readable-anchor membership, resolved in a single
// read and then answered from memory by Admits.
//
// It is security-plane state whose only sound lifetime is the one evaluation
// that built it: it is a snapshot of a projection four other pipelines write,
// so a set outliving its evaluation is an over-grant waiting for the next
// revocation. Nothing here caches, and nothing may hold one across
// evaluations.
type AnchorSet struct {
	anchors map[string]struct{}
}

// Admits reports whether anchorID is in the set — exactly the answer
// IsReadable computes for the same actor and anchor, by construction (see
// ReadableAnchors). Admission is exact-string equality: a stored anchor
// segment is one key token, so a string carrying a NATS subject
// metacharacter is never a member. Where IsReadable refuses such a string
// loudly, Admits denies it; both are fail-closed, and a nil set admits
// nothing.
func (s *AnchorSet) Admits(anchorID string) bool {
	if s == nil || anchorID == "" {
		return false
	}
	_, ok := s.anchors[anchorID]
	return ok
}

// ReadableAnchors resolves every anchor the actor (actorType, actorID) may
// read, in ONE read of the per-anchor grant keys, for a caller that has many
// anchors to gate against one actor and would otherwise run IsReadable — a
// point read plus a consumer-backed key listing plus a point read per listed
// key — once per anchor.
//
// The membership it returns is identical to IsReadable's answer for every
// anchor whose keys all parse, and that is the whole contract: an anchor is
// admitted when its base key OR any of its domain keys is live, and denied
// when every matching key is soft-tombstoned (isDeleted:true, §6.8) or no key
// matches at all. It reads the same two key shapes, decodes the same body,
// and applies the same tombstone rule; the union is additive, so the order
// entries come back in cannot change the answer. It takes IsReadable's
// actor-field refusals too — empty, or carrying a NATS subject metacharacter
// that would build a filter matching a different key shape. A KV failure
// propagates as an error rather than reading as an empty grant set, which
// would deny every row of the actor with no diagnosis.
//
// TWO deliberate asymmetries with IsReadable, both fail-closed, both
// consequences of answering for a whole actor rather than for one anchor:
//
//   - An UNPARSEABLE body is logged at Warn and CONTRIBUTES NOTHING, where
//     IsReadable errors. IsReadable reads only the keys of the one anchor it
//     was asked about, so its error is scoped to that anchor; this reads every
//     key the actor holds, and erroring would let one corrupt key wedge every
//     evaluation of an actor holding thousands of good ones — a projection
//     that fails identically on every redelivery, which is a worse outcome
//     than the one missing grant it stands in for. The anchor is therefore
//     denied unless another key for the SAME anchor is live: the set is never
//     more permissive than the live keys, and can be more permissive than the
//     per-row read only where that read would have ERRORED.
//   - An anchorID carrying a NATS subject metacharacter is denied by Admits,
//     where IsReadable refuses it as an error. A stored anchor segment is one
//     key token and can never contain one, so such a string is never a member;
//     IsReadable's refusal exists because it would otherwise TEMPLATE that
//     string into a filter, which this never does.
//
// The read is deliberately GetMultiNoSnapshot, not GetMulti. Three reasons,
// each independent: the per-anchor reads it replaces already blend instants
// across an actor's rows, so no simultaneity is being given up; the cap-read
// producers carry a grant-change edge that re-drives the whole actor when a
// grant lands or is withdrawn, so the window is the one that edge already
// closes; and the actors this exists for hold thousands of grant keys, past
// the 1,024-subject fast path, where GetMulti's stability-verified double
// drain fails outright under any concurrent write — for a busy actor that is
// the normal condition, not an edge case (internal/substrate/kv_multi.go's
// KVGetMultiNoSnapshot contract).
func ReadableAnchors(ctx context.Context, kv *substrate.KV, actorType, actorID string) (*AnchorSet, error) {
	if actorType == "" || actorID == "" {
		return nil, fmt.Errorf("capabilityread: actorType and actorID must both be non-empty")
	}
	if strings.ContainsAny(actorType, ".*>") || strings.ContainsAny(actorID, ".*>") {
		return nil, fmt.Errorf("capabilityread: actorType %q / actorID %q must not contain NATS subject metacharacters", actorType, actorID)
	}
	actorSuffix := actorType + "." + actorID

	entries, err := kv.GetMultiNoSnapshot(ctx, []string{
		perAnchorBaseSetFilter(actorSuffix),
		perAnchorDomainSetFilter(actorSuffix),
	})
	if err != nil {
		return nil, fmt.Errorf("capabilityread: read per-anchor grants for %q: %w", actorSuffix, err)
	}

	set := &AnchorSet{anchors: make(map[string]struct{}, len(entries))}
	for key, entry := range entries {
		var doc perAnchorEntry
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			// The key is named and the body is not: a grant document is
			// security-plane state, and a reader logging one at Warn puts it
			// wherever the logs go.
			slog.Warn("capabilityread: per-anchor grant key does not parse; the anchor is NOT admitted",
				"key", key, "actor", actorSuffix, "err", err)
			continue
		}
		if doc.IsDeleted {
			continue
		}
		// The anchor id is the key's last token in both shapes — the
		// segments ahead of it are the constant prefix, the optional domain
		// and the actor suffix the two filters pinned literally.
		anchorID := key[strings.LastIndexByte(key, '.')+1:]
		if anchorID == "" {
			continue
		}
		set.anchors[anchorID] = struct{}{}
	}
	return set, nil
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
