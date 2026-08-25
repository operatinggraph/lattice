package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// StrandedOperatorEpoch names one live `operator` role that no identity holds:
// a prior bootstrap epoch's kernel role, left behind in Core KV when
// lattice.bootstrap.json was regenerated and the next boot minted a fresh
// roleOperator NanoID (primordial-epoch-stranded-authority-design.md §1). Its
// grants stay reachable from nothing, which is why the grant census — not the
// role itself — is what decides the report's severity (§4).
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
// bootstrap epoch other than this deployment's current one, hold no live
// `holdsRole` edge, and are therefore reachable by no identity at all. It is
// the cross-epoch orphan class scanKernelOrphans structurally cannot see: that
// census lists vtx.meta.> and keys on the CURRENT bootstrap op, while every
// key of a prior epoch is a vtx.role/vtx.permission carrying the PRIOR epoch's
// provenance (§2). Nothing here writes.
//
// The predicate is the §3.1 table: a role id that is not RoleOperatorID, a
// live vertex with a live `.canonicalName` aspect whose value is exactly
// "operator", and zero live inbound `holdsRole` edges. A held role is
// somebody's live role whatever it is named, so any live holder ends the
// candidate silently.
//
// Returns ErrPrimordialIDsUnloaded, before reading the graph at all, when the
// primordial identifier table has not been loaded. The predicate excludes the
// current epoch's own role by comparing ids, so an unloaded table (empty
// string) matches EVERY role and would report the live kernel role as
// stranded — the inverse of the truth (§5 dossier). Mirrors SystemActorKeys.
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

		held, err := hasLiveLink(ctx, kv, "lnk.identity.*.holdsRole.role."+roleID)
		if err != nil {
			return nil, err
		}
		if held {
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
			Protected: protected,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].RoleKey < out[j].RoleKey })
	return out, nil
}

// hasLiveLink reports whether the subject filter matches at least one link key
// whose stored envelope is present, parseable and not tombstoned. It stops at
// the first live edge: the holder question is existential, and a role with a
// thousand holders costs the same as a role with one.
func hasLiveLink(ctx context.Context, kv jetstream.KeyValue, filter string) (bool, error) {
	keys, err := listDistinctKeys(ctx, kv, filter)
	if err != nil {
		return false, err
	}
	for _, key := range keys {
		if _, live := readDocument(ctx, kv, key); live {
			return true, nil
		}
	}
	return false, nil
}

// liveGrantSources returns the sorted permission vertex keys whose live
// grantedBy edge targets roleID.
//
// The permission is derived from the link KEY, never from a body field: under
// Contract #1 §1.1 the source is the key's first (type, id) pair, so the key
// is the authoritative statement of which permission the edge grants and a
// body that disagreed with it would be corruption, not a second opinion.
func liveGrantSources(ctx context.Context, kv jetstream.KeyValue, roleID string) ([]string, error) {
	keys, err := listDistinctKeys(ctx, kv, "lnk.permission.*.grantedBy.role."+roleID)
	if err != nil {
		return nil, err
	}
	var grants []string
	for _, key := range keys {
		_, permID, _, _, _, ok := substrate.ParseLinkKey(key)
		if !ok {
			continue
		}
		if _, live := readDocument(ctx, kv, key); !live {
			continue
		}
		grants = append(grants, substrate.VertexKey("permission", permID))
	}
	return sortedUnique(grants), nil
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
