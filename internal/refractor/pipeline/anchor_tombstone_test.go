package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// providerLensSpec is a plain (non-actor-aware) full-engine projection lens of
// the shape every vertical read model uses: a single anchor, keyed on the
// anchor's vertex key, projecting a couple of body fields. It mirrors the
// clinic-providers roster that surfaced the linger bug.
const providerLensSpec = `
MATCH (p:provider {key: $actorKey})
RETURN p.key AS providerKey, p.data.fullName AS fullName
`

// TestEvaluateForEntry_PlainAnchorTombstone_Retracts is the pipeline-level proof
// of the full-engine anchor-tombstone retraction: a live anchor upserts, a root
// tombstone of that anchor emits a Delete against the prior key (closing the PO
// linger bug), and a secondary-type tombstone emits NO Delete (the anchor row is
// never wrongly removed).
func TestEvaluateForEntry_PlainAnchorTombstone_Retracts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const providerID = "Tprovider1aaaaaaaaaa"
	providerKey := "vtx.provider." + providerID
	writeCollisionVertex(t, coreKV, providerKey, "provider", map[string]any{"fullName": "Dr. Strange"})

	eng := full.New()
	cr, err := eng.Parse(providerLensSpec)
	require.NoError(t, err)

	p := &Pipeline{
		ruleID:     "rule-providers",
		coreKV:     coreKV,
		adjKV:      adjKV,
		engineKind: ruleengine.EngineFull,
		fullEngine: eng,
		fullCR:     cr,
	}

	// 1. Live anchor → a single upsert keyed on the provider's vertex key.
	liveEntry := ruleengine.NodeEntry{
		CoreKVKey:  providerKey,
		NodeLabel:  "provider",
		IsDeleted:  false,
		Properties: map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"},
	}
	results, err := p.evaluateForEntry(ctx, liveEntry)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.False(t, results[0].Delete, "a live anchor must upsert, not delete")
	require.Equal(t, providerKey, results[0].Keys["providerKey"])

	// 2. Root tombstone of the anchor → a Delete against the prior output key.
	// This is the fix: the upsert-only re-scan path would return zero rows and
	// leave the row stale; the new branch retracts it.
	tombstoneEntry := ruleengine.NodeEntry{
		CoreKVKey:  providerKey,
		NodeLabel:  "provider",
		IsDeleted:  true,
		Properties: map[string]any{"isDeleted": true, "lastModifiedAt": "2026-05-15T10:05:00Z"},
	}
	results, err = p.evaluateForEntry(ctx, tombstoneEntry)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.True(t, results[0].Delete, "a tombstoned anchor must retract its projected row")
	require.Equal(t, providerKey, results[0].Keys["providerKey"],
		"the Delete must target the same key the live anchor upserted")
	require.Nil(t, results[0].Row)

	// 3. Secondary-type tombstone (a vertex whose type is NOT the lens anchor) →
	// falls through to a normal re-execute and emits NO Delete. A patient
	// tombstone reaching a provider lens must never delete a provider row.
	secondaryEntry := ruleengine.NodeEntry{
		CoreKVKey:  "vtx.patient.Tpatient1bbbbbbbbbb",
		NodeLabel:  "patient",
		IsDeleted:  true,
		Properties: map[string]any{"isDeleted": true, "lastModifiedAt": "2026-05-15T10:06:00Z"},
	}
	results, err = p.evaluateForEntry(ctx, secondaryEntry)
	require.NoError(t, err)
	for _, r := range results {
		require.False(t, r.Delete, "a non-anchor tombstone must never emit a Delete against the anchor lens")
	}
}
