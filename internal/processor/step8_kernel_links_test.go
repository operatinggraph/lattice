package processor

// The kernel's authorization topology is twelve links, and until the guard's
// link arm existed every one of them was one ordinary RevokeRole away from
// removing the operator grant behind a kernel operation, or an engine's own
// root-equivalence — with nothing on any path that put it back. These tests
// pin what the arm refuses, and just as load-bearingly what it must NOT refuse:
// an ordinary identity's grant, a stranded prior epoch's edges, and the one
// update shape that heals a deployment already bricked.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

const (
	kernelAdminIdentityID = "Kd4rTm7pXb2wQn9jFv3s"
	kernelOperatorRoleID  = "Rp8kWn3tYb5mQx2vJd7h"
	kernelPermissionID    = "Pm5tXk9wRb3nQv7jHd2y"
	ordinaryIdentityID    = "Nb6vKt4wRm8pXj2qYd5c"
	strandedRoleID        = "Sv9jKm3tWb6nQx4pRd8y"
)

// kernelHoldsRoleKey / kernelGrantKey are the two shapes the kernel seeds, built
// through substrate.LinkKey so a test key that could never exist in Core KV
// fails here rather than passing a guard that parses it.
func kernelHoldsRoleKey(identityID, roleID string) string {
	return substrate.LinkKey("identity", identityID, "holdsRole", "role", roleID)
}

func kernelGrantKey(permissionID, roleID string) string {
	return substrate.LinkKey("permission", permissionID, "grantedBy", "role", roleID)
}

// seededLinkDoc is the body the kernel seeds at a link key: live, both
// endpoints spelled out, relation as both class and localName.
func seededLinkDoc(sourceKey, targetKey, relation string) map[string]interface{} {
	return map[string]interface{}{
		"class":        relation,
		"isDeleted":    false,
		"sourceVertex": sourceKey,
		"targetVertex": targetKey,
		"localName":    relation,
		"data":         map[string]interface{}{},
	}
}

// buildCommitterWithKernelLinks assembles a committer whose protected-key guard
// holds kernelLinks, over a fresh embedded NATS + Core KV harness.
func buildCommitterWithKernelLinks(t *testing.T, kernelLinks []string) (context.Context, *CommitterImpl) {
	t.Helper()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return ctx, NewCommitter(conn, testCoreBucket, cache, testLogger(), time.Now, kernelLinks)
}

// requireProtectedKey asserts err is the kernel-protection refusal naming root.
func requireProtectedKey(t *testing.T, err error, key, root, op string) {
	t.Helper()
	var pe *ProtectedKeyError
	if !errors.As(err, &pe) {
		t.Fatalf("%s %s: err = %v, want *ProtectedKeyError", op, key, err)
	}
	if pe.Key != key {
		t.Errorf("ProtectedKeyError.Key = %s, want %s", pe.Key, key)
	}
	if pe.Root != root {
		t.Errorf("ProtectedKeyError.Root = %s, want %s", pe.Root, root)
	}
	if pe.Op != op {
		t.Errorf("ProtectedKeyError.Op = %s, want %s", pe.Op, op)
	}
}

func TestCommit_KernelLinkTombstoneIsProtected(t *testing.T) {
	t.Parallel()

	adminKey := "vtx.identity." + kernelAdminIdentityID
	roleKey := "vtx.role." + kernelOperatorRoleID
	permKey := "vtx.permission." + kernelPermissionID
	adminHoldsRole := kernelHoldsRoleKey(kernelAdminIdentityID, kernelOperatorRoleID)
	permGrantedBy := kernelGrantKey(kernelPermissionID, kernelOperatorRoleID)

	ctx, c := buildCommitterWithKernelLinks(t, []string{adminHoldsRole, permGrantedBy})

	// A create is exempt from the guard, which is how the seeded edges get to
	// exist at all; every refusal below is over a live edge.
	commitOne(t, ctx, c, "rid-seed-holds", MutationOp{
		Op: "create", Key: adminHoldsRole,
		Document: seededLinkDoc(adminKey, roleKey, "holdsRole"),
	})
	commitOne(t, ctx, c, "rid-seed-grant", MutationOp{
		Op: "create", Key: permGrantedBy,
		Document: seededLinkDoc(permKey, roleKey, "grantedBy"),
	})

	// The filed hazard itself: a RevokeRole-shaped tombstone of the primordial
	// admin's own operator edge. Root names the admin, not the link, so the
	// rejection reply says whose authority the mutation would have removed.
	requireProtectedKey(t,
		commitOneErr(ctx, c, "rid-revoke-role", MutationOp{Op: "tombstone", Key: adminHoldsRole}),
		adminHoldsRole, adminKey, "tombstone")

	// RevokePermission's shape against a kernel grant edge, whose source is the
	// permission the operator would lose.
	requireProtectedKey(t,
		commitOneErr(ctx, c, "rid-revoke-perm", MutationOp{Op: "tombstone", Key: permGrantedBy}),
		permGrantedBy, permKey, "tombstone")

	// A soft delete reaches the same outcome as a tombstone through an update,
	// so the arm has to refuse it by the written body, not by the op alone.
	softDeleted := seededLinkDoc(adminKey, roleKey, "holdsRole")
	softDeleted["isDeleted"] = true
	requireProtectedKey(t,
		commitOneErr(ctx, c, "rid-soft-delete", MutationOp{Op: "update", Key: adminHoldsRole, Document: softDeleted}),
		adminHoldsRole, adminKey, "update")

	// A re-pointed endpoint leaves a live edge at the key that no longer says
	// what the seeder wrote — the admin would "hold" some other role.
	repointed := seededLinkDoc(adminKey, "vtx.role."+strandedRoleID, "holdsRole")
	requireProtectedKey(t,
		commitOneErr(ctx, c, "rid-repoint", MutationOp{Op: "update", Key: adminHoldsRole, Document: repointed}),
		adminHoldsRole, adminKey, "update")

	// A re-sourced edge is the mirror hazard: the same key, a body naming some
	// other identity as the holder. The key is what every consumer looks up,
	// and the body is what it then reads.
	resourced := seededLinkDoc("vtx.identity."+ordinaryIdentityID, roleKey, "holdsRole")
	requireProtectedKey(t,
		commitOneErr(ctx, c, "rid-resource", MutationOp{Op: "update", Key: adminHoldsRole, Document: resourced}),
		adminHoldsRole, adminKey, "update")

	// A re-classed edge stays live at the key while declaring itself a
	// different relation — and a consumer that classifies an edge by its
	// `class` (the grant-link reconciler does) stops counting it as the grant
	// it is.
	reclassed := seededLinkDoc(adminKey, roleKey, "holdsRole")
	reclassed["class"] = "assignedTo"
	requireProtectedKey(t,
		commitOneErr(ctx, c, "rid-reclass", MutationOp{Op: "update", Key: adminHoldsRole, Document: reclassed}),
		adminHoldsRole, adminKey, "update")

	// A body missing a field the seeder writes is not the seeded shape either:
	// the committer writes what it is handed, so the edge would survive the
	// update stripped of its localName.
	partial := seededLinkDoc(adminKey, roleKey, "holdsRole")
	delete(partial, "localName")
	requireProtectedKey(t,
		commitOneErr(ctx, c, "rid-partial", MutationOp{Op: "update", Key: adminHoldsRole, Document: partial}),
		adminHoldsRole, adminKey, "update")

	// The positive vectors. An ordinary identity granted the same operator role
	// through AssignRole is not seeded, so RevokeRole keeps working — the arm
	// protects an exact set, not a relation.
	ordinaryKey := "vtx.identity." + ordinaryIdentityID
	ordinaryHoldsRole := kernelHoldsRoleKey(ordinaryIdentityID, kernelOperatorRoleID)
	commitOne(t, ctx, c, "rid-seed-ordinary", MutationOp{
		Op: "create", Key: ordinaryHoldsRole,
		Document: seededLinkDoc(ordinaryKey, roleKey, "holdsRole"),
	})
	if err := commitOneErr(ctx, c, "rid-revoke-ordinary", MutationOp{Op: "tombstone", Key: ordinaryHoldsRole}); err != nil {
		t.Fatalf("tombstone of a non-seeded holdsRole edge: %v", err)
	}

	// A stranded prior epoch's edge — the admin holding an operator role this
	// table does not name. `lattice bootstrap retire-stranded-epoch` revokes
	// exactly these, so a rule keyed on the endpoints rather than the exact key
	// would have refused the verb that exists to clean them up.
	strandedHoldsRole := kernelHoldsRoleKey(kernelAdminIdentityID, strandedRoleID)
	commitOne(t, ctx, c, "rid-seed-stranded", MutationOp{
		Op: "create", Key: strandedHoldsRole,
		Document: seededLinkDoc(adminKey, "vtx.role."+strandedRoleID, "holdsRole"),
	})
	if err := commitOneErr(ctx, c, "rid-revoke-stranded", MutationOp{Op: "tombstone", Key: strandedHoldsRole}); err != nil {
		t.Fatalf("tombstone of a stranded epoch's holdsRole edge: %v", err)
	}
}

// A deployment that suffered the brick before the guard existed has exactly one
// heal path: AssignRole's revive_link, an update that flips isDeleted back to
// false over the tombstone. Reseeding cannot do it (the seeder never rewrites a
// soft tombstone) and a create conflicts on a tombstoned key, so a blanket
// "kernel links are immutable" rule would have made every existing brick
// permanent. This is the one mutation the arm admits.
func TestCommit_KernelLinkReviveIsAdmitted(t *testing.T) {
	t.Parallel()

	adminKey := "vtx.identity." + kernelAdminIdentityID
	roleKey := "vtx.role." + kernelOperatorRoleID
	adminHoldsRole := kernelHoldsRoleKey(kernelAdminIdentityID, kernelOperatorRoleID)

	ctx, c := buildCommitterWithKernelLinks(t, []string{adminHoldsRole})

	// The already-revoked state, written under the guard rather than through
	// it — the deployment this heals reached it before the guard shipped.
	revoked := seededLinkDoc(adminKey, roleKey, "holdsRole")
	revoked["isDeleted"] = true
	revoked["key"] = adminHoldsRole
	body, err := json.Marshal(revoked)
	if err != nil {
		t.Fatalf("marshal revoked link: %v", err)
	}
	if _, err := c.Conn.KVPut(ctx, testCoreBucket, adminHoldsRole, body); err != nil {
		t.Fatalf("seed revoked link: %v", err)
	}

	revived := commitOne(t, ctx, c, "rid-revive", MutationOp{
		Op: "update", Key: adminHoldsRole,
		Document: seededLinkDoc(adminKey, roleKey, "holdsRole"),
	})
	if deleted, _ := revived["isDeleted"].(bool); deleted {
		t.Fatalf("revive left the edge tombstoned: %v", revived)
	}
	if got := revived["targetVertex"]; got != roleKey {
		t.Errorf("revived targetVertex = %v, want %s", got, roleKey)
	}
}

// The unset set is fail-OPEN by necessity — the fail-closed reading of a
// protected-key set is "every link is protected", which refuses every link
// write in the deployment. This documents that posture, which is what every
// fixture that does not wire the set gets, and is why the production wiring is
// pinned in cmd/processor instead.
func TestCommit_EmptyKernelLinkSetProtectsNothing(t *testing.T) {
	t.Parallel()

	adminKey := "vtx.identity." + kernelAdminIdentityID
	roleKey := "vtx.role." + kernelOperatorRoleID
	adminHoldsRole := kernelHoldsRoleKey(kernelAdminIdentityID, kernelOperatorRoleID)

	ctx, c := buildCommitterWithKernelLinks(t, nil)

	commitOne(t, ctx, c, "rid-seed-open", MutationOp{
		Op: "create", Key: adminHoldsRole,
		Document: seededLinkDoc(adminKey, roleKey, "holdsRole"),
	})
	if err := commitOneErr(ctx, c, "rid-tombstone-open", MutationOp{Op: "tombstone", Key: adminHoldsRole}); err != nil {
		t.Fatalf("unwired set refused a link tombstone: %v", err)
	}
}
