package identitydomain

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natstest"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// identityAnchorsSpec is verified here against the same in-memory full-engine
// harness the executor tests use (internal/refractor/ruleengine/full), so the
// literal production spec — not a paraphrase — is what proves the
// provider-binding (identifiedBy-inverse) anchor persona-worlds-design.md
// §4.1 names. The Osei case is the regression this guards: a provider holds
// neither residesIn nor worksAt (she reaches her building via
// identity <- identifiedBy - provider -> practicesAt), so before this walk
// the lens produced no anchors at all and the whoami hats surface could not
// see her provider hat.

func startAnchorsKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
	t.Helper()
	_, nc := natstest.Server(t)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-anchors-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-anchors-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-anchors-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-anchors-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

func putAnchorsVertex(t *testing.T, kv *substrate.KV, class, id string, extra map[string]any) string {
	t.Helper()
	key := "vtx." + class + "." + id
	props := map[string]any{"key": key, "class": class}
	for k, v := range extra {
		props[k] = v
	}
	data, err := json.Marshal(props)
	require.NoError(t, err)
	_, err = kv.Put(context.Background(), key, data)
	require.NoError(t, err)
	return key
}

// putAnchorsEdge writes the inbound + outbound adjacency for one link,
// mirroring the executor test harness's putEdge (source <name> target where
// the source is the later-arriving vertex, Contract #1 §1.1).
func putAnchorsEdge(t *testing.T, adjKV *substrate.KV, name, fromType, fromID, toType, toID string) {
	t.Helper()
	ctx := context.Background()
	edgeID := name + "_" + fromID + "_" + toID
	linkKey := "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name,
		Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType,
	}))
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name,
		Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType,
	}))
}

func execAnchors(t *testing.T, actorKey string, adjKV, coreKV *substrate.KV) []any {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(identityAnchorsSpec)
	require.NoError(t, err)
	out, err := eng.ExecuteWith(context.Background(),
		cr,
		ruleengine.EventContext{Parameters: map[string]any{"actorKey": actorKey}},
		adjKV, coreKV)
	require.NoError(t, err)
	require.Len(t, out, 1)
	anchors, ok := out[0].Values["anchors"].([]any)
	require.True(t, ok, "anchors column missing or not a list: %#v", out[0].Values)
	return anchors
}

// anchorWith returns the first anchor map carrying the given relation, or nil.
func anchorWith(anchors []any, relation string) map[string]any {
	for _, a := range anchors {
		if m, ok := a.(map[string]any); ok && m["relation"] == relation {
			if m["key"] != nil { // skip the degenerate {key:null} OPTIONAL-MATCH entry
				return m
			}
		}
	}
	return nil
}

// TestIdentityAnchors_ProviderBindingAnchor is the Osei regression: a provider
// identity with only an identifiedBy binding (no residence, no workplace) must
// still project a relation-stamped identifiedBy anchor carrying the bound
// provider entity's key.
func TestIdentityAnchors_ProviderBindingAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startAnchorsKVs(t)
	identityID := "K1bqP7UH8wYRfRTUdY5d"
	providerID := "6gerUBMpr5voBfo3dbS7"
	identityKey := putAnchorsVertex(t, coreKV, "identity", identityID, nil)
	providerKey := putAnchorsVertex(t, coreKV, "provider", providerID, nil)
	// provider identifiedBy identity (the later-arriving provider is the source).
	putAnchorsEdge(t, adjKV, "identifiedBy", "provider", providerID, "identity", identityID)

	anchors := execAnchors(t, identityKey, adjKV, coreKV)
	got := anchorWith(anchors, "identifiedBy")
	require.NotNil(t, got, "identifiedBy anchor missing: %#v", anchors)
	require.Equal(t, providerKey, got["key"])
	// A provider carries neither a residence nor a workplace: no real anchor of
	// either relation should survive.
	require.Nil(t, anchorWith(anchors, "residesIn"))
	require.Nil(t, anchorWith(anchors, "worksAt"))
}

// TestIdentityAnchors_ManagesAnchor is the landlord hat's gate: an identity that
// manages a unit projects a relation-stamped `manages` anchor carrying the unit
// and the building containing it, and an identity that manages nothing projects
// none — which is what makes the LoftSpace landlord surface appear for exactly
// the sessions whose landlord console has rows.
func TestIdentityAnchors_ManagesAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startAnchorsKVs(t)
	landlordID := "Wm4tZpQ7bkRvXn2sdJy8"
	unitID := "Qa9vBnE3twLkPz6mrUdC"
	buildingID := "Ry7hKdVn4pTxZm2cbQwe"
	landlordKey := putAnchorsVertex(t, coreKV, "identity", landlordID, nil)
	unitKey := putAnchorsVertex(t, coreKV, "unit", unitID, map[string]any{
		"presentation": map[string]any{"data": map[string]any{"name": "Unit 4B"}},
	})
	buildingKey := putAnchorsVertex(t, coreKV, "location", buildingID, map[string]any{
		"presentation": map[string]any{"data": map[string]any{"name": "The Loft"}},
	})
	// identity manages unit (loftspace-domain/ownership.go), unit containedIn building.
	putAnchorsEdge(t, adjKV, "manages", "identity", landlordID, "unit", unitID)
	putAnchorsEdge(t, adjKV, "containedIn", "unit", unitID, "location", buildingID)

	anchors := execAnchors(t, landlordKey, adjKV, coreKV)
	got := anchorWith(anchors, "manages")
	require.NotNil(t, got, "manages anchor missing: %#v", anchors)
	require.Equal(t, unitKey, got["key"])
	require.Equal(t, "Unit 4B", got["name"])
	require.Equal(t, buildingKey, got["container"])
	require.Equal(t, "The Loft", got["containerName"])
	// A landlord who only manages carries no residence or workplace.
	require.Nil(t, anchorWith(anchors, "residesIn"))
	require.Nil(t, anchorWith(anchors, "worksAt"))
}

// TestIdentityAnchors_NoManagesForNonLandlord is the negative half: without a
// manages link the anchor must be absent, not a degenerate entry — otherwise
// every signed-in applicant would be offered the landlord console.
func TestIdentityAnchors_NoManagesForNonLandlord(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startAnchorsKVs(t)
	identityID := "Td3xWq8mzcVbNr5kfPhJ"
	unitID := "Bs6nLpX2yqwEtZ9dvKmR"
	identityKey := putAnchorsVertex(t, coreKV, "identity", identityID, nil)
	putAnchorsVertex(t, coreKV, "unit", unitID, nil)
	// The unit exists and the identity resides in it — but manages nothing.
	putAnchorsEdge(t, adjKV, "residesIn", "identity", identityID, "unit", unitID)

	anchors := execAnchors(t, identityKey, adjKV, coreKV)
	require.NotNil(t, anchorWith(anchors, "residesIn"), "residesIn anchor missing: %#v", anchors)
	require.Nil(t, anchorWith(anchors, "manages"))
}

// TestIdentityAnchors_PatientBindingIsDistinctByKey proves the untyped walk is
// faithful (a patient is identifiedBy-bound too) and that the bound-entity key
// is what a domain-aware caller keys the hat off — a patient's anchor carries a
// vtx.patient key, never a vtx.provider one, so the clinic FE's provider gate
// (relation identifiedBy + vtx.provider key) does not fire for a patient.
func TestIdentityAnchors_PatientBindingIsDistinctByKey(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startAnchorsKVs(t)
	identityID := "HxRZbakZ2hvRpNmNaqCQ"
	patientID := "noNa5Fc2vrkBojZ2QPAv"
	identityKey := putAnchorsVertex(t, coreKV, "identity", identityID, nil)
	patientKey := putAnchorsVertex(t, coreKV, "patient", patientID, nil)
	putAnchorsEdge(t, adjKV, "identifiedBy", "patient", patientID, "identity", identityID)

	anchors := execAnchors(t, identityKey, adjKV, coreKV)
	got := anchorWith(anchors, "identifiedBy")
	require.NotNil(t, got)
	require.Equal(t, patientKey, got["key"])
	require.Contains(t, got["key"], "vtx.patient.")
}
