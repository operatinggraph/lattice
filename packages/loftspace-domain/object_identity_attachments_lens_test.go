package loftspacedomain

// Rule-engine proof of objectIdentityAttachmentsReadSpec, driven through the
// `full` engine directly against an embedded NATS Core/Adjacency KV — the
// same harness landlord_units_lens_test.go uses for a Protected lens with no
// single "actorKey" parameter.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// content writes an object's `.content` aspect — the byte-plane metadata
// AttachObject persists (packages/objects-base/ddls.go).
func (f *luFixture) content(t *testing.T, objName, storeName, contentType string, size int) {
	t.Helper()
	f.unitAspect(t, objName, "content", "object", map[string]any{
		"storeName": storeName, "contentType": contentType, "size": size,
	})
}

// objectLink writes an object→owner attachment link — both the adjacency
// edge (the rule engine's MATCH) and the link's own Core KV document (the
// `data` a relationship carries, e.g. `filename`) — mirroring what
// AttachObject actually commits (objects-base ddls.go: object is the
// source, owner the target, Contract #1 §1.1).
func (f *luFixture) objectLink(t *testing.T, linkName, objName, ownerName string, data map[string]any) {
	t.Helper()
	ctx := context.Background()
	objID, ownerID := f.ids[objName], f.ids[ownerName]
	objType, ownerType := f.types[objID], f.types[ownerID]
	linkKey := "lnk." + objType + "." + objID + "." + linkName + "." + ownerType + "." + ownerID
	edgeID := linkName + "_" + objID + "_" + ownerID
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: linkName, Direction: "outbound", NodeID: objID, OtherNodeID: ownerID, OtherType: ownerType}))
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: linkName, Direction: "inbound", NodeID: ownerID, OtherNodeID: objID, OtherType: objType}))
	body := map[string]any{"key": linkKey, "class": "link", "isDeleted": false,
		"sourceVertex": "vtx." + objType + "." + objID, "targetVertex": "vtx." + ownerType + "." + ownerID,
		"localName": linkName, "data": data}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = f.coreKV.Put(ctx, linkKey, raw)
	require.NoError(t, err)
}

func (f *luFixture) projectIdentityAttachments(t *testing.T) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(objectIdentityAttachmentsReadSpec)
	require.NoError(t, err, "objectIdentityAttachmentsRead cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// TestObjectIdentityAttachmentsRead_ProjectsOwnedAttachment: an object
// attached to an identity projects one row carrying the byte-plane metadata,
// the link's own filename, and authz_anchors = [owner's bare NanoID] only.
func TestObjectIdentityAttachmentsRead_ProjectsOwnedAttachment(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLuFixture(t)
	objKey := f.vtx(t, "scan", "object")
	ownerKey := f.vtx(t, "alice", "identity")
	f.content(t, "scan", "store-abc", "application/pdf", 4096)
	f.objectLink(t, "idDocument", "scan", "alice", map[string]any{"filename": "passport.pdf"})

	rows := f.projectIdentityAttachments(t)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, objKey, v["entity_key"])
	require.Equal(t, f.ids["scan"], v["oid"])
	require.Equal(t, ownerKey, v["owner_key"])
	require.Equal(t, "idDocument", v["link_name"])
	require.Equal(t, "passport.pdf", v["filename"])
	require.Equal(t, "store-abc", v["store_name"])
	require.Equal(t, "application/pdf", v["content_type"])
	require.EqualValues(t, 4096, v["size"])
	anchors, ok := v["authz_anchors"].([]any)
	require.True(t, ok, "authz_anchors must be a list, got %T", v["authz_anchors"])
	require.Equal(t, []any{f.ids["alice"]}, anchors, "self-view only: no staff fan-out")
}

// TestObjectIdentityAttachmentsRead_ExcludesUnattachedObject: an object with
// no link to any identity projects no row — the MATCH requires the link, the
// same fail-closed absence landlordUnitsRead has for an unmanaged unit.
func TestObjectIdentityAttachmentsRead_ExcludesUnattachedObject(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLuFixture(t)
	f.vtx(t, "scan", "object")
	f.content(t, "scan", "store-abc", "application/pdf", 4096)
	// No objectLink() written.

	rows := f.projectIdentityAttachments(t)
	require.Empty(t, rows, "an object with no owner link has no identity to anchor the row on")
}

// TestObjectIdentityAttachmentsRead_TwoSlotsOnSameOwner_TwoRows: one object
// attached to one owner under two slots is two links, and DetachObject needs
// the linkName to remove one without the other — collapsing them by
// (object, owner) alone would make one undetachable independent of the
// other, unlike objects-base's own owner-fan-out objectAttachments lens,
// which collapses to one row per object precisely because IT doesn't need a
// per-slot dispatch target.
func TestObjectIdentityAttachmentsRead_TwoSlotsOnSameOwner_TwoRows(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLuFixture(t)
	f.vtx(t, "scan", "object")
	f.vtx(t, "alice", "identity")
	f.content(t, "scan", "store-abc", "application/pdf", 4096)
	f.objectLink(t, "idDocument", "scan", "alice", map[string]any{"filename": "passport.pdf"})
	f.objectLink(t, "proofOfIncome", "scan", "alice", map[string]any{})

	rows := f.projectIdentityAttachments(t)
	require.Len(t, rows, 2, "two slots on one owner are two distinguishable rows")
	byLinkName := map[string]map[string]any{}
	for _, r := range rows {
		byLinkName[r.Values["link_name"].(string)] = r.Values
	}
	require.Equal(t, "passport.pdf", byLinkName["idDocument"]["filename"])
	require.Nil(t, byLinkName["proofOfIncome"]["filename"], "an attach with no filename leaves the link payload empty, which projects null")
}

// TestObjectIdentityAttachmentsRead_Sensitive_ProjectsEncryptionEnvelope:
// the crypto-shred columns project the same nested envelope
// objectAttachmentsSpec (objects-base) already proves against the real
// AttachObject on-wire shape.
func TestObjectIdentityAttachmentsRead_Sensitive_ProjectsEncryptionEnvelope(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLuFixture(t)
	f.vtx(t, "idscan", "object")
	applicantKey := f.vtx(t, "applicant", "identity")
	f.unitAspect(t, "idscan", "content", "object", map[string]any{
		"storeName": "store-cipher", "contentType": "image/jpeg", "size": 2048,
		"digest": "SHA-256=sensitiveLensTestDigest", "sensitive": true, "governingIdentity": applicantKey,
		"encryption": map[string]any{"algo": "AES-256-GCM", "nonce": "bm9uY2U=", "wrappedCEK": "d3JhcHBlZA==", "keyId": applicantKey},
	})
	f.objectLink(t, "idDocument", "idscan", "applicant", map[string]any{})

	rows := f.projectIdentityAttachments(t)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["sensitive"])
	require.Equal(t, applicantKey, v["governing_identity"])
	enc, ok := v["encryption"].(map[string]any)
	require.True(t, ok, "encryption must project as a nested object, got %T", v["encryption"])
	require.Equal(t, "AES-256-GCM", enc["algo"])
	require.Equal(t, applicantKey, enc["keyId"])
}
