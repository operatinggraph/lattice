package rbacdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Lenses returns the package's Lens declarations.
//
// rbac-domain owns the projection of role-derived grants — the role/permission
// graph vocabulary it also owns (Contract #6 §6.1 contribution model). Core
// retains the Capability KV bucket + key conventions + the step-3 dispatcher;
// rbac-domain declares, as install-time data, where its grants project and how
// the read side routes to them.
//
//   - capabilityRoles (actor-aggregate): for every actor holding an rbac role,
//     projects cap.roles.<actor-suffix> carrying that actor's role-derived
//     platformPermissions[] ({operationType, scope, lanes}) plus the role
//     keys held. `lanes` is per-op and optional (absent unless the granting
//     permission's PermissionSpec.Lanes set it) — Contract #6 §6.4.
//     The disjoint cap.roles.* key space (Contract #6 §6.1) keeps the
//     package's grant projection off the core cap.<actor> key, so ordinary
//     actors read their grants from cap.roles.<actor> via the registered auth
//     hook while core projects only the primordial system actors onto
//     cap.<actor>.
//
//   - capabilityRoleIndex (operation-aggregate): one record per operationType
//     listing the roles granting it (cap.role-by-operation.<op>). It feeds the
//     FR22 denial-response rolesCarryingPermission/actorRoles. Keyed by
//     operationType (IntoKey), it activates through the same generic
//     operation-aggregate path as the former core seed with no cmd/ edit.
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName:  "capabilityRoles",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "capability-kv",
			Engine:         "full",
			Spec:           capabilityRolesSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "identity",
				OutputKeyPattern: "cap.roles.{actorSuffix}",
				BodyColumns:      []string{"platformPermissions", "roles"},
				EmptyBehavior:    "delete",
				Freshness:        "auto",
				// Per-lane submission grant (Contract #2 §2.3): every ordinary
				// role-holder gets the `default` lane only ("most actors hold
				// `default` only"). A static baseline on the descriptor — not a
				// graph-derived value — so it needs no cypher change. Privileged
				// lanes (meta/urgent/system) are reserved to the protected
				// kernel actors via the core cap.<actor> lens.
				Lanes: []string{"default"},
			},
		},
		{
			CanonicalName: "capabilityRoleIndex",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        "capability-kv",
			Engine:        "full",
			Spec:          capabilityRoleIndexSpec,
			IntoKey:       []string{"operationType"},
		},
	}
}

// capabilityRolesSpec walks the actor's rbac grant topology
// (identity -[:holdsRole]-> role <-[:grantedBy]- permission) and projects the
// role-derived platform permissions plus the role keys held. Anchored on the
// bound identity so reprojection traverses adjacency from the actor on any
// holdsRole / grantedBy CDC event. The OPTIONAL MATCH yields a single
// degenerate (all-null) collect entry for an actor holding no role; the
// envelope wrapper's emptyBehavior:delete drops the key when no real grant
// remains (Contract #6 §6.8 absence = denial).
const capabilityRolesSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)<-[:grantedBy]-(perm:permission)
RETURN
  identity.key AS actorKey,
  collect(DISTINCT {
    operationType: perm.data.operationType,
    scope: perm.data.scope,
    lanes: perm.data.lanes
  }) AS platformPermissions,
  collect(DISTINCT role.key) AS roles
`

// capabilityRoleIndexSpec produces one record per operationType listing the
// roles that grant it. Keyed by operationType (not by actor) — no per-actor
// revoke→resurrect race, so the projection is unguarded. Consumed by the
// Processor denial-response builder (FR22 rolesCarryingPermission).
const capabilityRoleIndexSpec = `
MATCH (role:role)<-[:grantedBy]-(perm:permission)
RETURN
  perm.data.operationType AS operationType,
  collect(DISTINCT role.canonicalName.data.value) AS roles,
  $projectedAt AS projectedAt
`
