package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// grantLensSpec is the shape of the base capabilityReadGrants GrantTable lens: a
// plain (non-actor-aware) full-engine projection with a 3-column composite key.
// Its first RETURN item is a nanoIdFromKey(...) function call — so the legacy
// "first RETURN item is the key" path produced a one-column key that the
// GrantWriterAdapter rejects (anchor_id absent). With KeyColumns threaded, the
// pipeline must hand the adapter all three composite columns.
const grantLensSpec = `
MATCH (identity:identity)
RETURN
  nanoIdFromKey(identity.key) AS actor_id,
  nanoIdFromKey(identity.key) AS anchor_id,
  'cap-read'                  AS grant_source
`

// TestEvaluateForEntry_CompositeKeyProducer_DeliversAllKeyColumns is the
// pipeline-level proof of the producer fix (the D1.3 unblock): a composite-key
// plain full lens, with KeyColumns threaded as cmd/refractor does at activation,
// delivers a Keys map carrying every key column — the shape GrantWriterAdapter
// requires to populate actor_read_grants. The companion control asserts the
// legacy un-threaded path delivers only the first column (the bug it closes).
func TestEvaluateForEntry_CompositeKeyProducer_DeliversAllKeyColumns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const identityID = "Tidentity01aaaaaaaaa"
	identityKey := "vtx.identity." + identityID
	writeCollisionVertex(t, coreKV, identityKey, "identity", map[string]any{"name": "alice"})

	liveEntry := ruleengine.NodeEntry{
		CoreKVKey:  identityKey,
		NodeLabel:  "identity",
		IsDeleted:  false,
		Properties: map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"},
	}

	newPipeline := func(keyCols []string) *Pipeline {
		eng := full.New()
		cr, err := eng.Parse(grantLensSpec)
		require.NoError(t, err)
		// Mirror cmd/refractor activation: set the output key columns.
		cr.(*full.CompiledRule).KeyColumns = keyCols
		return &Pipeline{
			ruleID:     "rule-grants",
			coreKV:     coreKV,
			adjKV:      adjKV,
			engineKind: ruleengine.EngineFull,
			fullEngine: eng,
			fullCR:     cr,
		}
	}

	// Threaded composite key columns → all three delivered.
	results, err := newPipeline([]string{"actor_id", "anchor_id", "grant_source"}).
		evaluateForEntry(ctx, liveEntry)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.False(t, results[0].Delete, "a live identity must upsert a grant")
	require.Equal(t, identityID, results[0].Keys["actor_id"], "actor_id (bare NanoID)")
	require.Equal(t, identityID, results[0].Keys["anchor_id"],
		"anchor_id present — the column the GrantWriterAdapter errored on before the fix")
	require.Equal(t, "cap-read", results[0].Keys["grant_source"], "grant_source present")
	require.Len(t, results[0].Keys, 3, "the composite key is complete")

	// Legacy control: un-threaded → only the first RETURN item (the bug).
	legacy, err := newPipeline(nil).evaluateForEntry(ctx, liveEntry)
	require.NoError(t, err)
	require.Len(t, legacy, 1)
	require.Len(t, legacy[0].Keys, 1, "the un-threaded fallback keys on the first item only")
	require.NotContains(t, legacy[0].Keys, "anchor_id", "anchor_id absent — what broke the producer")
}

// TestEvaluateForEntry_CompositeKeyGrant_RetractsOnTombstone is the Fire-2
// pipeline proof: when the identity anchor is soft-deleted, the plain
// full-engine grant lens emits a composite-keyed Delete (every key column the
// GrantWriterAdapter needs to RevokeGrant), instead of the upsert-only re-scan
// that left the self-grant lingering in actor_read_grants.
func TestEvaluateForEntry_CompositeKeyGrant_RetractsOnTombstone(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const identityID = "identityAAAAAAAAAAAA"
	identityKey := "vtx.identity." + identityID

	eng := full.New()
	cr, err := eng.Parse(grantLensSpec)
	require.NoError(t, err)
	cr.(*full.CompiledRule).KeyColumns = []string{"actor_id", "anchor_id", "grant_source"}
	p := &Pipeline{
		ruleID:     "rule-grants",
		coreKV:     coreKV,
		adjKV:      adjKV,
		engineKind: ruleengine.EngineFull,
		fullEngine: eng,
		fullCR:     cr,
	}

	tombstone := ruleengine.NodeEntry{
		CoreKVKey:  identityKey,
		NodeLabel:  "identity",
		IsDeleted:  true,
		Properties: map[string]any{"isDeleted": true},
	}
	results, err := p.evaluateForEntry(ctx, tombstone)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.True(t, results[0].Delete, "a tombstoned identity must retract its self-grant")
	require.Nil(t, results[0].Row, "a Delete carries no row")
	require.Equal(t, map[string]any{
		"actor_id":     identityID,
		"anchor_id":    identityID,
		"grant_source": "cap-read",
	}, results[0].Keys, "the Delete carries the full composite key GrantWriterAdapter.RevokeGrant needs")
}
