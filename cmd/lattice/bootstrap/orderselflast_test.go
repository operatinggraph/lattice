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

// TestRecordIfGrant pins the verb split behind the reinstall recommendations.
//
// recordIfGrant partitions revocation ops by operationType: only a
// RevokePermission has a permKey resolving to a declaring package, so only it
// can produce a "reinstall this package" recommendation. A RevokeRole carries a
// roleKey and no package at all. Getting that branch wrong is quiet in both
// directions — admitting RevokeRole appends a key nothing can resolve, and
// dropping RevokePermission leaves an operator revoked grants with no
// recommendation to restore them, which is the whole output of the tool.
func TestRecordIfGrant(t *testing.T) {
	var got []string

	recordIfGrant(revokePermOp("vtx.permission.p1"), &got)
	require.Equal(t, []string{"vtx.permission.p1"}, got,
		"a RevokePermission's permKey is what printReinstallRecommendations resolves to an owning package")

	recordIfGrant(revokeRoleOp("vtx.identity.someone"), &got)
	require.Equal(t, []string{"vtx.permission.p1"}, got,
		"a RevokeRole contributes nothing: it carries a roleKey, not a permKey, and no declaring package")

	recordIfGrant(revokePermOp("vtx.permission.p2"), &got)
	require.Equal(t, []string{"vtx.permission.p1", "vtx.permission.p2"}, got)

	// A RevokePermission whose payload lacks permKey is skipped rather than
	// appending an empty string, which would resolve to no package and print as
	// a blank recommendation line.
	recordIfGrant(internalbootstrap.RevocationOp{OperationType: "RevokePermission", Payload: map[string]any{}}, &got)
	require.Equal(t, []string{"vtx.permission.p1", "vtx.permission.p2"}, got)
}
