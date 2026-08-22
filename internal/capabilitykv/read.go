package capabilitykv

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// KVGetter is the minimal NATS KV surface a Capability KV reader needs. The
// `*substrate.Conn` returned by `substrate.Connect` satisfies it; tests pass
// a fake reader that returns canned bytes for a fixed key.
type KVGetter interface {
	KVGet(ctx context.Context, bucket, key string) (*substrate.KVEntry, error)
}

// ReadAndMerge GETs each key independently and folds the present docs into
// one merged Doc (MergeDocs). A KeyNotFound on one member is an empty skip,
// not a hard deny — the caller denies on absence only when EVERY member is
// absent (doc == nil, deny-closed). A non-NotFound read error, or a parse
// failure, aborts immediately so the caller can propagate it rather than
// silently degrading the grant set. The returned key is the "+"-joined list
// of keys that were actually present (a single key, unchanged, for a
// one-element keys slice).
//
// Shared by the Processor's step-3 platform read and the control-plane
// capability checker (control-plane-capability-authz-design.md §3.3) so both
// read the identical projection through the identical key set for a given
// actor — the "read+route" half of Contract #6 §6.1/§6.4; each caller owns
// its own matcher.
func ReadAndMerge(ctx context.Context, reader KVGetter, bucket string, keys []string) (*Doc, string, error) {
	var doc *Doc
	var present []string
	for _, key := range keys {
		kvEntry, err := reader.KVGet(ctx, bucket, key)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				continue
			}
			return nil, "", fmt.Errorf("capability kv read %q: %w", key, err)
		}
		parsed, err := ParseCapabilityDoc(kvEntry.Value)
		if err != nil {
			return nil, "", fmt.Errorf("capability kv parse %q: %w", key, err)
		}
		present = append(present, key)
		if doc == nil {
			doc = parsed
		} else {
			doc = MergeDocs(doc, parsed)
		}
	}
	if doc == nil {
		return nil, "", nil
	}
	return doc, strings.Join(present, "+"), nil
}

// ReadPlatformDoc reads the platform-path projection for actor: it derives
// the key set through ClassAwarePlatformKey(systemActorKeys) (keys.go:58 — a
// system actor unions {cap.<rest>, cap.roles.<rest>}, every other actor reads
// cap.roles.<rest> alone) and delegates to ReadAndMerge. The routing is a live
// predicate over the graph-derived system-actor set
// (internal/bootstrap/system_actors.go:50), not a property of the key string,
// which is why a reader must come through here rather than concatenate the
// keys itself.
//
// PRECONDITION: this is the routing the Processor's step-3 platform entry
// applies when RbacRolesActive. Step 3 has a second derivation
// (internal/processor/step3_auth_matcher.go:158): with rbac-domain absent, the
// platform entry falls back to singleKeyList(capabilityKeyFromActor) —
// cap.<rest> for EVERY actor. Under that posture the two routings are
// INVERTED for an ordinary actor (step 3 reads its anchor; this reads its
// roles key), so a caller comparing its result against a step-3 decision is
// answering the same question only in an rbac-active deployment. Every
// production deployment runs rbac-active — cmd/{processor,loom,weaver,
// refractor} wire the class-aware derivation unconditionally — and the
// fallback is the rbac-absent bootstrap window.
//
// It applies NO gates — no reserved-op refusal, no lane check, no scope
// match — the same posture as ReadAndMerge. That is Contract #6 §6.1/§6.4's
// split: the projection is a grant set, the matcher belongs to the caller,
// because each caller asks a different question of it (step 3 authorizes one
// envelope; a capability-proposal check bounds what a grant may confer). A
// caller needing a gate applies its own.
//
// A nil doc with a nil error means every derived key was absent — deny-closed;
// what absence means is the caller's decision.
func ReadPlatformDoc(ctx context.Context, reader KVGetter, bucket string, systemActorKeys []string, actor string) (*Doc, string, error) {
	keys, err := ClassAwarePlatformKey(systemActorKeys)(actor)
	if err != nil {
		return nil, "", err
	}
	return ReadAndMerge(ctx, reader, bucket, keys)
}

// MergeDocs folds extra's grant-bearing fields into base (deny-closed union —
// Contract #6 §6.1 system-actor platform-path carve-out). It is TOTAL over the
// Doc shape: every grant-bearing field contributes from BOTH docs, so no
// source's grants can be silently dropped by a caller merging keys whose
// projections happen to carry a field the merge forgot. platformPermissions,
// serviceAccess and ephemeralGrants concatenate (a grant holds iff SOME source
// carries it, and their entries carry distinct provenance so collapsing them
// would lose it); lanes and roles union (dedup). projectedFromRevisions merges
// for auth-trace provenance (both source keys recorded). The identity and
// provenance scalars (key, actor, version, projectedAt) are base's — they name
// a projection, not a grant. base is never mutated; a new doc is returned.
//
// INVARIANT those scalars rest on: ReadAndMerge folds in key order, and
// ClassAwarePlatformKey emits [anchor, roles] (keys.go:77), so on the platform
// path base is the ANCHOR doc whenever one is present. Reverse that order and
// the key/projectedAt reaching a caller's auth trace silently become the roles
// projection's — a change with no compile error and no failing merge, so the
// ordering is load-bearing where it is written, not incidental.
func MergeDocs(base, extra *Doc) *Doc {
	merged := *base
	merged.PlatformPermissions = append(
		append([]PlatformPermission{}, base.PlatformPermissions...),
		extra.PlatformPermissions...)
	merged.ServiceAccess = append(
		append([]ServiceAccessEntry{}, base.ServiceAccess...),
		extra.ServiceAccess...)
	merged.EphemeralGrants = append(
		append([]EphemeralGrant{}, base.EphemeralGrants...),
		extra.EphemeralGrants...)
	merged.Lanes = unionStrings(base.Lanes, extra.Lanes)
	merged.Roles = unionStrings(base.Roles, extra.Roles)
	// Copied unconditionally: a merged doc that aliased base's map would let a
	// later write through either doc surface in the other, which is the one
	// way this function could mutate base.
	if base.ProjectedFromRevisions != nil || extra.ProjectedFromRevisions != nil {
		merged.ProjectedFromRevisions = make(map[string]uint64, len(base.ProjectedFromRevisions)+len(extra.ProjectedFromRevisions))
		for k, v := range base.ProjectedFromRevisions {
			merged.ProjectedFromRevisions[k] = v
		}
		for k, v := range extra.ProjectedFromRevisions {
			merged.ProjectedFromRevisions[k] = v
		}
	}
	return &merged
}

// unionStrings returns the deduplicated concatenation of a and b, preserving
// first-seen order.
func unionStrings(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, s := range b {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
