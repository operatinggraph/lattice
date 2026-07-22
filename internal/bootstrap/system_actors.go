package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// SystemActorKeys scans core-kv and returns the actor keys of the kernel-seeded
// system identities — the root-equivalent actors the Capability Lens
// primordial-identity anchor projects root grants for (the primordial admin +
// the internal service actors seeded by primordial.go). They are identified by
// the same predicate the anchor cypher uses (root-designation-topology-reconverge,
// 2026-07-03): holding the primordial `operator` role via a `holdsRole` link
// (Contract #7 §7.7), NOT `data.protected` (retired as a capability designator;
// it keeps only its anti-brick meaning).
//
// The step-3 platform read routes these actors to their core cap.<actor> doc
// (the primordial anchor) and every other actor to cap.roles.<actor> when
// rbac-domain is installed. Discovering them from the graph keeps the processor
// self-contained (it already reads core-kv) and exactly matches the set the
// anchor projects, rather than depending on the bootstrap-file key space being
// loaded into the processor process.
//
// The `holdsRole → operator` link keys (lnk.identity.<id>.holdsRole.role.<RoleOperatorID>)
// are matched directly off the key STRING (substrate.ParseLinkKey) — no
// per-identity KVGet across the whole identity population. Only the (small,
// fixed) set of candidate holdsRole-to-operator link keys found this way is
// read once each, to exclude a revoked (tombstoned) grant.
func SystemActorKeys(ctx context.Context, conn *substrate.Conn) ([]string, error) {
	keys, err := conn.KVListKeys(ctx, CoreKVBucket)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: list core-kv keys: %w", err)
	}
	var out []string
	for _, k := range keys {
		type1, id1, linkName, type2, id2, ok := substrate.ParseLinkKey(k)
		if !ok || type1 != "identity" || linkName != "holdsRole" || type2 != "role" || id2 != RoleOperatorID {
			continue
		}
		entry, gErr := conn.KVGet(ctx, CoreKVBucket, k)
		if gErr != nil {
			if errors.Is(gErr, substrate.ErrKeyNotFound) {
				continue
			}
			return nil, fmt.Errorf("bootstrap: read %s: %w", k, gErr)
		}
		var env struct {
			IsDeleted bool `json:"isDeleted"`
		}
		if jErr := json.Unmarshal(entry.Value, &env); jErr != nil {
			continue
		}
		if env.IsDeleted {
			continue
		}
		out = append(out, substrate.VertexKey("identity", id1))
	}
	sort.Strings(out)
	return out, nil
}

// PrivacyActorKey scans core-kv and returns the actor key of the kernel-seeded
// privacy-plane service actor (class "identity.system.privacy") — the actor
// the crypto-shred finalization listeners (internal/privacyworker and
// internal/refractor/keyshredded, vault-crypto-shredding-design.md Fire 4b)
// submit RecordShredFinalization under. Graph discovery mirrors
// SystemActorKeys: the hosting binaries (cmd/processor, cmd/refractor)
// deliberately do not load lattice.bootstrap.json, so the actor is found by
// its class the way the step-3 platform routing finds the protected set.
//
// Returns "" (no error) when the actor is absent — a deployment bootstrapped
// before version 15. Callers treat that as "finalization recording disabled",
// never a startup failure, so the shred path itself keeps working against an
// older kernel.
func PrivacyActorKey(ctx context.Context, conn *substrate.Conn) (string, error) {
	keys, err := conn.KVListKeys(ctx, CoreKVBucket)
	if err != nil {
		return "", fmt.Errorf("bootstrap: list core-kv keys: %w", err)
	}
	for _, k := range keys {
		vtxType, _, ok := substrate.ParseVertexKey(k)
		if !ok || vtxType != "identity" {
			continue
		}
		if strings.Count(k, ".") != 2 {
			continue
		}
		entry, gErr := conn.KVGet(ctx, CoreKVBucket, k)
		if gErr != nil {
			if errors.Is(gErr, substrate.ErrKeyNotFound) {
				continue
			}
			return "", fmt.Errorf("bootstrap: read %s: %w", k, gErr)
		}
		var env struct {
			Class     string `json:"class"`
			IsDeleted bool   `json:"isDeleted"`
		}
		if jErr := json.Unmarshal(entry.Value, &env); jErr != nil {
			continue
		}
		if !env.IsDeleted && env.Class == "identity.system.privacy" {
			return k, nil
		}
	}
	return "", nil
}
