package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// StrandedOperatorEpoch names one live `operator` role that no identity of the
// current primordial epoch can reach: a prior bootstrap epoch's kernel role,
// left behind in Core KV when lattice.bootstrap.json was regenerated and the
// next boot minted a fresh roleOperator NanoID
// (primordial-epoch-stranded-authority-design.md §1). Its grants are reachable
// only from an epoch that no longer boots anything, which is why the grant
// census — not the role, and not its holders — decides the report's severity
// (§4).
type StrandedOperatorEpoch struct {
	// RoleKey is the stranded role's vertex key, vtx.role.<id>.
	RoleKey string
	// GrantedBy holds the sorted vtx.permission.<id> keys whose live grantedBy
	// edge targets this role — the authority island the role still confers.
	// Empty means the role is dead weight rather than live authority.
	GrantedBy []string
	// Holders holds the sorted vtx.identity.<id> keys with a live holdsRole
	// edge into this role. Every one of them is itself a prior-epoch identity,
	// stranded by the same id-file rotation — a current-epoch holder would have
	// kept the role out of this report entirely. They are reported so a human
	// reading the finding can see the shape of the island rather than only its
	// root, and they never gate severity.
	Holders []string
	// Protected mirrors the role vertex's data.protected. It is carried for
	// corroboration when a human reads the report and never gates the
	// predicate: primordial.go records that field as retired as an
	// authorization input, and a predicate must not quietly revive a retired
	// field's meaning (§3.1).
	Protected bool
}

// StrandedOperatorEpochs scans Core KV for `operator` roles that belong to a
// bootstrap epoch other than this deployment's current one and that no
// current-epoch identity holds — authority still live in the bucket with
// nothing left able to exercise it. It is
// the cross-epoch orphan class scanKernelOrphans structurally cannot see: that
// census lists vtx.meta.> and keys on the CURRENT bootstrap op, while every
// key of a prior epoch is a vtx.role/vtx.permission carrying the PRIOR epoch's
// provenance (§2). Nothing here writes.
//
// The predicate is the §3.1 table: a role id that is not RoleOperatorID, a
// live vertex with a live `.canonicalName` aspect whose value is exactly
// "operator", and no live `holdsRole` edge from an identity of the CURRENT
// primordial epoch.
//
// Reachability is a question about the current epoch, not about holders in
// general. A re-bootstrap on a regenerated id file deletes nothing — the seed
// path is create-only, and reconcile classifies every non-vtx.meta.* entry as
// retained — so the prior epoch's admin and service identities and their
// holdsRole edges into the prior operator role are all still live. The whole
// epoch strands as one island, and its own holders are part of that island
// rather than evidence against it; suppressing on any live holder would silence
// this scan on precisely the case it exists to catch. Only an identity the
// loaded primordial table names can actually reach the role, and that alone
// makes the role live topology.
//
// Returns ErrPrimordialIDsUnloaded, before reading the graph at all, when the
// primordial identifier table has not been loaded. Both halves of the predicate
// are keyed on that table: an unloaded one (empty string) makes the id filter
// match EVERY role and leaves the current-epoch identity set empty, so nothing
// could suppress and the live kernel role would be reported as stranded — the
// inverse of the truth (§5 dossier). Mirrors SystemActorKeys.
//
// Cost is the deployment's role count: one server-side filtered listing of
// vtx.role.*.canonicalName plus one read per role. The two link enumerations
// are target-bounded subject filters (substrate/kv.go:256-268), bounded by the
// stranded role's own degree, and they run only for a candidate that already
// passed the canonicalName filter — on a single-epoch deployment, never.
func StrandedOperatorEpochs(ctx context.Context, kv jetstream.KeyValue) ([]StrandedOperatorEpoch, error) {
	if RoleOperatorID == "" {
		return nil, fmt.Errorf("%w: roleOperator", ErrPrimordialIDsUnloaded)
	}

	candidates, err := listDistinctKeys(ctx, kv, "vtx.role.*.canonicalName")
	if err != nil {
		return nil, err
	}
	currentEpochIdentities := currentEpochIdentityKeys()

	var out []StrandedOperatorEpoch
	for _, aspectKey := range candidates {
		roleKey, _, roleID, _, ok := substrate.ParseAspectKey(aspectKey)
		if !ok || roleID == RoleOperatorID {
			continue
		}
		aspect, live := readDocument(ctx, kv, aspectKey)
		if !live {
			continue
		}
		if name, _ := aspect.Data["value"].(string); name != "operator" {
			continue
		}
		role, live := readDocument(ctx, kv, roleKey)
		if !live {
			continue
		}

		holders, err := liveHolderSources(ctx, kv, roleID)
		if err != nil {
			return nil, err
		}
		if intersects(holders, currentEpochIdentities) {
			continue
		}

		grants, err := liveGrantSources(ctx, kv, roleID)
		if err != nil {
			return nil, err
		}

		protected, _ := role.Data["protected"].(bool)
		out = append(out, StrandedOperatorEpoch{
			RoleKey:   roleKey,
			GrantedBy: grants,
			Holders:   holders,
			Protected: protected,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].RoleKey < out[j].RoleKey })
	return out, nil
}

// currentEpochIdentityKeys returns the vertex keys of every identity this
// deployment's loaded primordial table names — the primordial admin plus the
// internal service actors (nanoid.go's populate assigns all of them from one
// validated table, so a set built while RoleOperatorID is loaded is complete).
//
// This set is what "reachable" means for a role. An identity outside it is
// either an ordinary graph identity or a prior epoch's stranded actor, and
// neither answers the question this scan asks.
//
// Each id is re-checked before use because substrate.VertexKey panics on a
// malformed NanoID, and a reporting scan must not be able to take a boot down.
func currentEpochIdentityKeys() map[string]bool {
	ids := []string{
		BootstrapIdentityID,
		LoomIdentityID,
		WeaverIdentityID,
		BridgeIdentityID,
		ObjmgrIdentityID,
		PrivacyIdentityID,
		GatewayIdentityID,
	}
	keys := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !substrate.IsValidNanoID(id) {
			continue
		}
		keys[substrate.VertexKey("identity", id)] = true
	}
	return keys
}

// intersects reports whether any of keys is a member of set.
func intersects(keys []string, set map[string]bool) bool {
	for _, k := range keys {
		if set[k] {
			return true
		}
	}
	return false
}

// liveGrantSources returns the sorted permission vertex keys whose live
// grantedBy edge targets roleID.
func liveGrantSources(ctx context.Context, kv jetstream.KeyValue, roleID string) ([]string, error) {
	return liveLinkSources(ctx, kv, "lnk.permission.*.grantedBy.role."+roleID, "permission")
}

// liveHolderSources returns the sorted identity vertex keys with a live
// holdsRole edge into roleID.
func liveHolderSources(ctx context.Context, kv jetstream.KeyValue, roleID string) ([]string, error) {
	return liveLinkSources(ctx, kv, "lnk.identity.*.holdsRole.role."+roleID, "identity")
}

// liveLinkSources enumerates a target-bounded link filter and returns the
// sorted vertex keys of the live edges' SOURCES.
//
// The source is derived from the link KEY, never from a body field: under
// Contract #1 §1.1 the source is the key's first (type, id) pair, so the key is
// the authoritative statement of what the edge relates, and a body that
// disagreed with it would be corruption rather than a second opinion.
func liveLinkSources(ctx context.Context, kv jetstream.KeyValue, filter, sourceType string) ([]string, error) {
	keys, err := listDistinctKeys(ctx, kv, filter)
	if err != nil {
		return nil, err
	}
	var sources []string
	for _, key := range keys {
		_, sourceID, _, _, _, ok := substrate.ParseLinkKey(key)
		if !ok {
			continue
		}
		if _, live := readDocument(ctx, kv, key); !live {
			continue
		}
		sources = append(sources, substrate.VertexKey(sourceType, sourceID))
	}
	return sortedUnique(sources), nil
}

// listDistinctKeys returns the sorted, de-duplicated keys matching a KV
// subject filter.
//
// Two guards it never omits. The lister's feed goroutine closes its channel on
// ctx expiry exactly as it does on completion, so a timed-out listing is
// indistinguishable from a complete one — and a truncated set read as "no
// stranded epoch" is the exact wrong answer, so the context error is returned
// and the partial result discarded. And the pinned NATS KV lister may report
// duplicate keys, which would double-count a role's grants; keys are unique in
// the store, so de-duplicating the sorted enumeration is exact
// (substrate/kv.go:308-317).
func listDistinctKeys(ctx context.Context, kv jetstream.KeyValue, filter string) ([]string, error) {
	lister, err := kv.ListKeysFiltered(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("list %q: %w", filter, err)
	}
	defer lister.Stop()
	var collected []string
	for k := range lister.Keys() {
		collected = append(collected, k)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("list %q: interrupted (partial result discarded): %w", filter, err)
	}
	return sortedUnique(collected), nil
}

// sortedUnique sorts a key slice and drops adjacent duplicates.
func sortedUnique(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	out := keys[:1]
	for _, k := range keys[1:] {
		if k != out[len(out)-1] {
			out = append(out, k)
		}
	}
	return out
}

// storedDocument is the slice of a stored envelope this scan reads: whether it
// carries a soft tombstone, and its data payload.
type storedDocument struct {
	IsDeleted bool           `json:"isDeleted"`
	Data      map[string]any `json:"data"`
}

// readDocument reads one key and reports whether it is live: present, parseable
// and carrying no soft tombstone. An absent or unparseable key answers "not
// live" with an empty document — this scan reports a defect, so a key it cannot
// read must never be able to manufacture one.
//
// A tombstoned document is returned with its data intact alongside live=false,
// so a caller reading a field cannot accidentally satisfy the tombstone check
// by finding the field empty. Deciding on the tombstone is each caller's own
// step.
func readDocument(ctx context.Context, kv jetstream.KeyValue, key string) (doc storedDocument, live bool) {
	stored, err := kv.Get(ctx, key)
	if err != nil {
		return storedDocument{}, false
	}
	if err := json.Unmarshal(stored.Value(), &doc); err != nil {
		return storedDocument{}, false
	}
	return doc, !doc.IsDeleted
}
