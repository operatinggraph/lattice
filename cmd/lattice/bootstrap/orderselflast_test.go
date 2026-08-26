package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"

	internalbootstrap "github.com/operatinggraph/lattice/internal/bootstrap"
)

func revokeRoleOp(actorKey string) internalbootstrap.RevocationOp {
	return internalbootstrap.RevocationOp{OperationType: "RevokeRole", Payload: map[string]any{"actorKey": actorKey, "roleKey": "vtx.role.x"}}
}

func revokePermOp(permKey string) internalbootstrap.RevocationOp {
	return internalbootstrap.RevocationOp{OperationType: "RevokePermission", Payload: map[string]any{"permKey": permKey, "roleKey": "vtx.role.x"}}
}

// TestOrderSubmittingActorLast_MovesSelfRevokeToTheEnd pins the fix for the
// self-decapitation hazard a cold review found: revoking the submitting
// actor's OWN holdsRole edge first would strip its authorization mid-run.
func TestOrderSubmittingActorLast_MovesSelfRevokeToTheEnd(t *testing.T) {
	self := "vtx.identity.self"
	ops := []internalbootstrap.RevocationOp{
		revokeRoleOp(self),
		revokeRoleOp("vtx.identity.other1"),
		revokePermOp("vtx.permission.p1"),
		revokeRoleOp("vtx.identity.other2"),
	}
	orderSubmittingActorLast(ops, self)

	require.Equal(t, revokeRoleOp(self), ops[len(ops)-1], "the actor's own RevokeRole must sort last")
	for _, op := range ops[:len(ops)-1] {
		require.False(t, isSelfRevokeRole(op, self), "no non-last entry may be the actor's own RevokeRole")
	}
}

// TestOrderSubmittingActorLast_NoSelfEntryLeavesOrderStable pins that ops
// with no self-entry at all are left in their original relative order
// (sort.SliceStable's guarantee, exercised here rather than assumed).
func TestOrderSubmittingActorLast_NoSelfEntryLeavesOrderStable(t *testing.T) {
	ops := []internalbootstrap.RevocationOp{
		revokeRoleOp("vtx.identity.other1"),
		revokePermOp("vtx.permission.p1"),
		revokeRoleOp("vtx.identity.other2"),
	}
	want := append([]internalbootstrap.RevocationOp(nil), ops...)
	orderSubmittingActorLast(ops, "vtx.identity.self")
	require.Equal(t, want, ops)
}

func TestIsSelfRevokeRole(t *testing.T) {
	self := "vtx.identity.self"
	require.True(t, isSelfRevokeRole(revokeRoleOp(self), self))
	require.False(t, isSelfRevokeRole(revokeRoleOp("vtx.identity.other"), self))
	require.False(t, isSelfRevokeRole(revokePermOp("vtx.permission.p1"), self),
		"a RevokePermission is never a self-revoke regardless of payload shape")
}
