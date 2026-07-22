package capabilitykv

import (
	"errors"
	"fmt"
	"strings"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// CapabilityKeyFromActor converts `vtx.identity.<NanoID>` →
// `cap.identity.<NanoID>` per Contract #6 §6.1 + producer logic in
// `internal/refractor/capabilityenv/envelope.go:capabilityKey`. This is the
// kernel-seeded system actors' core anchor key (the rbac-independent floor).
func CapabilityKeyFromActor(actor string) (string, error) {
	rest, err := actorSuffix(actor)
	if err != nil {
		return "", err
	}
	return "cap." + rest, nil
}

// RolesKeyFromActor converts `vtx.identity.<NanoID>` →
// `cap.roles.identity.<NanoID>` — the disjoint key rbac-domain's
// capabilityRoles lens projects an ordinary actor's role-derived grants into
// (Contract #6 §6.1). It is the platform path's key for ordinary actors when
// rbac-domain is installed.
func RolesKeyFromActor(actor string) (string, error) {
	rest, err := actorSuffix(actor)
	if err != nil {
		return "", err
	}
	return "cap.roles." + rest, nil
}

func actorSuffix(actor string) (string, error) {
	if actor == "" {
		return "", errors.New("empty actor")
	}
	rest, ok := strings.CutPrefix(actor, substrate.VertexPrefix+".")
	if !ok {
		return "", fmt.Errorf("actor %q lacks %q prefix", actor, substrate.VertexPrefix+".")
	}
	return rest, nil
}

// ClassAwarePlatformKey returns a platform key-LIST derivation closure that
// routes the kernel-seeded system actors (systemActorKeys) to a UNION read of
// their core cap.<actor> anchor (the rbac-independent floor: privileged lanes
// + the 6 bootstrap ops) and cap.roles.<actor> (the rbac-derived package-op
// extension), and every other (ordinary) actor to a single cap.roles.<actor>
// GET — unchanged. This is the platform entry's class-aware derivation
// (system-actor-package-op-grants-design.md §3.1): the bounded exception to
// one-key-per-path, scoped to the fixed kernel-seeded actor set.
// systemActorKeys are the full vtx.identity.<id> actor keys of the primordial
// admin + the kernel-seeded service actors (graph-discovered by
// bootstrap.SystemActorKeys).
func ClassAwarePlatformKey(systemActorKeys []string) func(string) ([]string, error) {
	system := make(map[string]struct{}, len(systemActorKeys))
	for _, k := range systemActorKeys {
		if k != "" {
			system[k] = struct{}{}
		}
	}
	return func(actor string) ([]string, error) {
		rolesKey, err := RolesKeyFromActor(actor)
		if err != nil {
			return nil, err
		}
		if _, isSystem := system[actor]; !isSystem {
			return []string{rolesKey}, nil
		}
		anchorKey, err := CapabilityKeyFromActor(actor)
		if err != nil {
			return nil, err
		}
		return []string{anchorKey, rolesKey}, nil
	}
}
