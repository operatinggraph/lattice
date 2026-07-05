package capabilitykv

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/asolgan/lattice/internal/substrate"
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

// MergeDocs folds extra's grant-bearing fields into base (deny-closed union —
// Contract #6 §6.1 system-actor platform-path carve-out). platformPermissions
// concatenate (a permission is granted iff SOME source grants it); lanes and
// roles union (dedup). projectedFromRevisions merges for auth-trace
// provenance (both source keys recorded). base is never mutated; a new doc is
// returned.
func MergeDocs(base, extra *Doc) *Doc {
	merged := *base
	merged.PlatformPermissions = append(
		append([]PlatformPermission{}, base.PlatformPermissions...),
		extra.PlatformPermissions...)
	merged.Lanes = unionStrings(base.Lanes, extra.Lanes)
	merged.Roles = unionStrings(base.Roles, extra.Roles)
	if len(extra.ProjectedFromRevisions) > 0 {
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
