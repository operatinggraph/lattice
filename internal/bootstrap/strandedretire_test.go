package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
)

func TestPlanStrandedEpochRetirement_HoldersAndGrants(t *testing.T) {
	roleID := newNanoID(t)
	roleKey := substrate.VertexKey("role", roleID)
	holderID := newNanoID(t)
	holderKey := substrate.VertexKey("identity", holderID)
	reachableID := newNanoID(t)
	reachableLinkKey := substrate.LinkKey("identity", reachableID, "holdsRole", "role", roleID)
	permID := newNanoID(t)
	permKey := substrate.VertexKey("permission", permID)

	epoch := StrandedOperatorEpoch{
		RoleKey:      roleKey,
		Holders:      []string{holderKey},
		ReachableVia: []string{reachableLinkKey},
		GrantedBy:    []string{permKey},
	}

	ops, err := PlanStrandedEpochRetirement(epoch)
	require.NoError(t, err)
	require.Len(t, ops, 3)

	require.Equal(t, "RevokeRole", ops[0].OperationType)
	require.Equal(t, map[string]any{"actorKey": holderKey, "roleKey": roleKey}, ops[0].Payload)
	require.Equal(t, substrate.LinkKey("identity", holderID, "holdsRole", "role", roleID), ops[0].LinkKey)

	require.Equal(t, "RevokeRole", ops[1].OperationType)
	require.Equal(t, map[string]any{"actorKey": substrate.VertexKey("identity", reachableID), "roleKey": roleKey}, ops[1].Payload)
	require.Equal(t, reachableLinkKey, ops[1].LinkKey)

	require.Equal(t, "RevokePermission", ops[2].OperationType)
	require.Equal(t, map[string]any{"permKey": permKey, "roleKey": roleKey}, ops[2].Payload)
	require.Equal(t, substrate.LinkKey("permission", permID, "grantedBy", "role", roleID), ops[2].LinkKey)
}

func TestPlanStrandedEpochRetirement_ReachableViaMustBeAHoldsRoleLinkIntoTheRole(t *testing.T) {
	roleID := newNanoID(t)
	roleKey := substrate.VertexKey("role", roleID)
	otherRoleID := newNanoID(t)
	actorID := newNanoID(t)

	tests := []struct {
		name string
		via  string
	}{
		{"wrong relation", substrate.LinkKey("identity", actorID, "assignedTo", "role", roleID)},
		{"wrong target role", substrate.LinkKey("identity", actorID, "holdsRole", "role", otherRoleID)},
		{"not a link key at all", roleKey},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			epoch := StrandedOperatorEpoch{RoleKey: roleKey, ReachableVia: []string{tc.via}}
			_, err := PlanStrandedEpochRetirement(epoch)
			require.Error(t, err)
		})
	}
}

func TestPlanStrandedEpochRetirement_EmptyEpochProducesNoOps(t *testing.T) {
	epoch := StrandedOperatorEpoch{RoleKey: substrate.VertexKey("role", newNanoID(t))}
	ops, err := PlanStrandedEpochRetirement(epoch)
	require.NoError(t, err)
	require.Empty(t, ops)
}

func TestPlanStrandedEpochRetirement_MalformedRoleKeyErrors(t *testing.T) {
	_, err := PlanStrandedEpochRetirement(StrandedOperatorEpoch{RoleKey: "vtx.role"})
	require.Error(t, err)

	_, err = PlanStrandedEpochRetirement(StrandedOperatorEpoch{RoleKey: substrate.VertexKey("identity", newNanoID(t))})
	require.Error(t, err)
}

func TestPlanStrandedEpochRetirement_MalformedHolderOrGrantKeyErrors(t *testing.T) {
	roleKey := substrate.VertexKey("role", newNanoID(t))

	_, err := PlanStrandedEpochRetirement(StrandedOperatorEpoch{
		RoleKey: roleKey, Holders: []string{"not-a-key"},
	})
	require.Error(t, err)

	_, err = PlanStrandedEpochRetirement(StrandedOperatorEpoch{
		RoleKey: roleKey, GrantedBy: []string{substrate.VertexKey("role", newNanoID(t))},
	})
	require.Error(t, err)
}
