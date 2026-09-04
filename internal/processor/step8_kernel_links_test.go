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

// The provenance a stored kernel link carries: the seeder's creation triplet,
// established once when the edge was seeded, and the lastModified triplet the
// revocation that tombstoned it left behind. A revive must preserve the first
// and displace the second.
const (
	seederStamp = "2026-01-02T03:04:05.000000006Z"
	seederActor = "vtx.identity.Bs7kWm2tXn5pQv9jRd3h"
	seederOp    = "vtx.op.Bp3nKv8wTm4xQj6rYd9s"

	revokeStamp = "2026-06-07T08:09:10.000000011Z"
	revokeActor = "vtx.identity.Rk5tWn8mXb3pQv7jFd2c"
	revokeOp    = "vtx.op.Rv2mXk6wTn9pQj4bHd7y"
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

	// A seeded-shape create is admitted, which is how the seeded edges get to
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
	// it — the deployment this heals reached it before the guard shipped. The
	// body is what buildMutationValue's tombstone arm actually stores: the
	// seeder's WHOLE prior document, its creation triplet intact, with
	// isDeleted flipped and a lastModified triplet stamped by the revoking
	// operation. A fixture that stored only the link fields would let a revive
	// that re-stamps createdAt pass, since there would be nothing to erase.
	revoked := seededLinkDoc(adminKey, roleKey, "holdsRole")
	revoked["isDeleted"] = true
	revoked["key"] = adminHoldsRole
	revoked["createdAt"] = seederStamp
	revoked["createdBy"] = seederActor
	revoked["createdByOp"] = seederOp
	revoked["lastModifiedAt"] = revokeStamp
	revoked["lastModifiedBy"] = revokeActor
	revoked["lastModifiedByOp"] = revokeOp
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

	// The heal must not rewrite the edge's history. The revive is an update,
	// and an update writes the whole value, so the creation triplet survives
	// only because preserveImmutableFields carries it over from the stored
	// document — the script cannot resupply it and this one does not try.
	// Losing it would leave the kernel's own topology claiming it was authored
	// by whoever healed it.
	for field, want := range map[string]string{
		"createdAt":   seederStamp,
		"createdBy":   seederActor,
		"createdByOp": seederOp,
	} {
		if got, _ := revived[field].(string); got != want {
			t.Errorf("revive rewrote %s: got %q, want the seeder's %q", field, got, want)
		}
	}
	// The mirror: the revive IS this operation, so the lastModified triplet
	// must have moved off the revocation that wrote the tombstone.
	if got, _ := revived["lastModifiedByOp"].(string); got == revokeOp {
		t.Errorf("lastModifiedByOp = %q — still the revoking operation, so the revive stamped nothing", got)
	}
}

// The admitted shape is a CLOSED whitelist of six top-level fields, not "these
// five must match and the rest is somebody else's problem". No platform guard
// governs a link's other fields — rejectPermissionRoleRewrites adjudicates
// vtx.permission.*/vtx.role.* roots and never sees a lnk.* key — so a tolerated
// extra field is caller bytes committed verbatim at a key this arm exists to
// hold fixed.
func TestCommit_KernelLinkReviveRefusesFieldsOutsideTheSeededShape(t *testing.T) {
	t.Parallel()

	adminKey := "vtx.identity." + kernelAdminIdentityID
	roleKey := "vtx.role." + kernelOperatorRoleID
	adminHoldsRole := kernelHoldsRoleKey(kernelAdminIdentityID, kernelOperatorRoleID)

	ctx, c := buildCommitterWithKernelLinks(t, []string{adminHoldsRole})
	commitOne(t, ctx, c, "rid-wl-seed", MutationOp{
		Op: "create", Key: adminHoldsRole,
		Document: seededLinkDoc(adminKey, roleKey, "holdsRole"),
	})

	// Each of these is a live, correctly-endpointed edge by the five compared
	// fields; each also smuggles a seventh field past them. `revokedAt` and
	// `deleted` are the dangerous ones — a future consumer reading either
	// would see a revocation the guard just admitted — and `vertexKey` is the
	// aspect-shaped field that would make the edge decode as something it is
	// not.
	for _, extra := range []struct {
		field string
		value interface{}
	}{
		{"revokedAt", "2026-09-04T00:00:00Z"},
		{"deleted", true},
		{"vertexKey", adminKey},
		{"note", "harmless-looking"},
	} {
		doc := seededLinkDoc(adminKey, roleKey, "holdsRole")
		doc[extra.field] = extra.value
		requireProtectedKey(t,
			commitOneErr(ctx, c, "rid-wl-"+extra.field, MutationOp{Op: "update", Key: adminHoldsRole, Document: doc}),
			adminHoldsRole, adminKey, "update")
	}

	// `data` is admitted but must still be a document body. A string or a
	// number there is not something any reader of these edges could interpret.
	for _, bad := range []interface{}{"a string", float64(7), []interface{}{"a", "list"}, nil} {
		doc := seededLinkDoc(adminKey, roleKey, "holdsRole")
		doc["data"] = bad
		requireProtectedKey(t,
			commitOneErr(ctx, c, "rid-wl-data", MutationOp{Op: "update", Key: adminHoldsRole, Document: doc}),
			adminHoldsRole, adminKey, "update")
	}
}

// The deliberate opening in the whitelist: `data` is admitted and its CONTENTS
// are not compared. rbac-domain's grant_link carries the grant-edge provenance
// stamp into the revive arm as well as the create arm, so a revive re-stamps
// exactly as a fresh grant would — requiring `data` empty would refuse the one
// heal path the arm exists to keep open. This pins that opening as intended
// rather than as an oversight, and pins its floor: `data` may also be absent,
// which is what a body assembled without one looks like.
func TestCommit_KernelLinkReviveAdmitsAnyDataStamp(t *testing.T) {
	t.Parallel()

	adminKey := "vtx.identity." + kernelAdminIdentityID
	roleKey := "vtx.role." + kernelOperatorRoleID
	adminHoldsRole := kernelHoldsRoleKey(kernelAdminIdentityID, kernelOperatorRoleID)

	ctx, c := buildCommitterWithKernelLinks(t, []string{adminHoldsRole})
	commitOne(t, ctx, c, "rid-stamp-seed", MutationOp{
		Op: "create", Key: adminHoldsRole,
		Document: seededLinkDoc(adminKey, roleKey, "holdsRole"),
	})

	stamped := seededLinkDoc(adminKey, roleKey, "holdsRole")
	stamped["data"] = map[string]interface{}{
		"grantedByPackage": "rbac-domain",
		"grantOrigin":      "package",
		"nested":           map[string]interface{}{"anything": "at all"},
	}
	committed := commitOne(t, ctx, c, "rid-stamp-update", MutationOp{
		Op: "update", Key: adminHoldsRole, Document: stamped,
	})
	data, _ := committed["data"].(map[string]interface{})
	if got, _ := data["grantedByPackage"].(string); got != "rbac-domain" {
		t.Errorf("data stamp did not reach the stored body: %v", committed["data"])
	}

	// Absent data is the same shape with one field fewer, and the committer
	// leaves an update's document alone rather than defaulting one in.
	noData := seededLinkDoc(adminKey, roleKey, "holdsRole")
	delete(noData, "data")
	if err := commitOneErr(ctx, c, "rid-stamp-nodata", MutationOp{
		Op: "update", Key: adminHoldsRole, Document: noData,
	}); err != nil {
		t.Fatalf("a body with no data field was refused: %v", err)
	}
}

// A create at a member key gets the same shape check an update does. The
// create-only conflict stops an overwrite of a LIVE edge, but says nothing
// about the body written at an ABSENT one — an incompletely seeded kernel, a
// bucket restored short of an edge — where a create commits whatever it
// carries at a key every authorization consumer looks up by name.
func TestCommit_KernelLinkCreateTakesTheSeededShapeCheck(t *testing.T) {
	t.Parallel()

	adminKey := "vtx.identity." + kernelAdminIdentityID
	roleKey := "vtx.role." + kernelOperatorRoleID
	adminHoldsRole := kernelHoldsRoleKey(kernelAdminIdentityID, kernelOperatorRoleID)

	ctx, c := buildCommitterWithKernelLinks(t, []string{adminHoldsRole})

	// The key is absent, so nothing but this guard stands between the mutation
	// and the write. A create naming a different holder is the shape that
	// matters: it installs an edge at the admin's key that says somebody else
	// holds the role.
	resourced := seededLinkDoc("vtx.identity."+ordinaryIdentityID, roleKey, "holdsRole")
	requireProtectedKey(t,
		commitOneErr(ctx, c, "rid-create-resourced", MutationOp{
			Op: "create", Key: adminHoldsRole, Document: resourced,
		}),
		adminHoldsRole, adminKey, "create")

	// A create born tombstoned is the other one — a member key that exists and
	// confers nothing, which reseed will never repair (the seeder refuses to
	// rewrite a soft tombstone).
	born := seededLinkDoc(adminKey, roleKey, "holdsRole")
	born["isDeleted"] = true
	requireProtectedKey(t,
		commitOneErr(ctx, c, "rid-create-dead", MutationOp{
			Op: "create", Key: adminHoldsRole, Document: born,
		}),
		adminHoldsRole, adminKey, "create")

	// The seeded-shape create is left alone: re-establishing an absent kernel
	// edge is legitimate, and against a live one the create-only conflict is
	// still the adjudicator, exactly as before.
	commitOne(t, ctx, c, "rid-create-ok", MutationOp{
		Op: "create", Key: adminHoldsRole,
		Document: seededLinkDoc(adminKey, roleKey, "holdsRole"),
	})

	// And a create at a NON-member link key is untouched by the arm, whatever
	// its body — the set is exact, not a rule over the relation.
	ordinaryHoldsRole := kernelHoldsRoleKey(ordinaryIdentityID, kernelOperatorRoleID)
	odd := seededLinkDoc("vtx.identity."+ordinaryIdentityID, roleKey, "holdsRole")
	odd["revokedAt"] = "2026-09-04T00:00:00Z"
	commitOne(t, ctx, c, "rid-create-ordinary", MutationOp{
		Op: "create", Key: ordinaryHoldsRole, Document: odd,
	})
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
