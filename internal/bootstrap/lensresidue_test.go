package bootstrap

// Tests for StrandedCapabilityLenses — the lens-plane sibling of
// StrandedOperatorEpochs (primordial-epoch-stranded-authority-design.md §7
// item 2). Mirrors strandedepoch_test.go's fixture shape and
// naming.

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// seedLens writes a live vtx.meta.<id> vertex plus its canonicalName aspect,
// and — when cypherRule is non-empty — a .cypherRule aspect. An empty
// cypherRule fixtures the "stranded lens whose cypher could not be read"
// case; class lets a test fixture a non-meta.lens vertex sharing a reserved
// canonicalName. Mirrors seedRole's shape.
func seedLens(ctx context.Context, t *testing.T, kv jetstream.KeyValue, lensID, class, canonicalName, cypherRule string) string {
	t.Helper()
	lensKey := substrate.VertexKey("meta", lensID)
	val, err := MakeVertexEnvelope(lensKey, class, map[string]any{"protected": true})
	putDoc(ctx, t, kv, lensKey, val, err, false)

	cnKey := substrate.AspectKey(lensKey, "canonicalName")
	cnVal, cnErr := MakeAspectEnvelope(cnKey, lensKey, "canonicalName", "canonicalName",
		map[string]any{"value": canonicalName})
	putDoc(ctx, t, kv, cnKey, cnVal, cnErr, false)

	if cypherRule != "" {
		crKey := substrate.AspectKey(lensKey, "cypherRule")
		crVal, crErr := MakeAspectEnvelope(crKey, lensKey, "cypherRule", "cypherRule",
			map[string]any{"rule": cypherRule})
		putDoc(ctx, t, kv, crKey, crVal, crErr, false)
	}
	return lensKey
}

// seedCurrentCapabilityLenses plants this deployment's own four capability
// lenses exactly as primordial.go seeds them, WITH their real cypher rules —
// StrandedCapabilityLenses compares a stranded twin's stored rule against
// these, so a fixture carrying the wrong (or no) rule would prove nothing
// about that comparison.
func seedCurrentCapabilityLenses(ctx context.Context, t *testing.T, kv jetstream.KeyValue) {
	t.Helper()
	for _, l := range currentCapabilityLenses() {
		seedLens(ctx, t, kv, l.ID, "meta.lens", l.Definition.CanonicalName, l.Definition.CypherRule)
	}
}

func TestStrandedCapabilityLenses_SingleEpochBucketIsSilent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentCapabilityLenses(ctx, t, kv)

	stranded, err := StrandedCapabilityLenses(ctx, kv)
	require.NoError(t, err)
	require.Empty(t, stranded, "this deployment's own four lenses must never be reported")
}

// TestStrandedCapabilityLenses_PriorEpochLensWithIdenticalCypherIsInert pins
// the safe case: a stranded twin whose stored cypher matches the current
// definition's byte-for-byte (after the same whitespace trim the seeder
// itself applies) computes the same result set the current lens does.
func TestStrandedCapabilityLenses_PriorEpochLensWithIdenticalCypherIsInert(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentCapabilityLenses(ctx, t, kv)
	current := CapabilityReadWildcardGrantsLensDefinition()
	priorID := newNanoID(t)
	priorKey := seedLens(ctx, t, kv, priorID, "meta.lens", "capabilityReadWildcardGrants", current.CypherRule)

	stranded, err := StrandedCapabilityLenses(ctx, kv)
	require.NoError(t, err)
	require.Len(t, stranded, 1, "exactly the prior epoch's lens must be reported")
	require.Equal(t, priorKey, stranded[0].LensKey)
	require.Equal(t, "capabilityReadWildcardGrants", stranded[0].CanonicalName)
	require.False(t, stranded[0].CypherDiverges)
	require.False(t, stranded[0].CypherUnreadable)
	require.Equal(t, StrandedLensSeverityInert, stranded[0].Severity())
}

// TestStrandedCapabilityLenses_PriorEpochLensWithDivergedCypherIsAFailure
// pins the dangerous case a cold adversarial review found this design had
// argued away: a stranded lens seeded by a binary whose cypher rule has
// since been narrowed (the real, historical shape of commit c9a80312,
// 2026-07-02) reads no holdsRole/grantedBy edge at all and is untouched by
// edge revocation — it must rank as a failure, not the inert default.
func TestStrandedCapabilityLenses_PriorEpochLensWithDivergedCypherIsAFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentCapabilityLenses(ctx, t, kv)
	priorID := newNanoID(t)
	priorKey := seedLens(ctx, t, kv, priorID, "meta.lens", "capabilityReadWildcardGrants",
		"MATCH (identity:identity) WHERE identity.data.protected = true RETURN identity.id AS actor_id")

	stranded, err := StrandedCapabilityLenses(ctx, kv)
	require.NoError(t, err)
	require.Len(t, stranded, 1)
	require.Equal(t, priorKey, stranded[0].LensKey)
	require.True(t, stranded[0].CypherDiverges)
	require.Equal(t, StrandedLensSeverityDiverged, stranded[0].Severity())
}

// TestStrandedCapabilityLenses_UnreadableCypherIsALowerBoundNotAnAllClear
// mirrors StrandedOperatorEpoch.UnreadableEdges's posture: an unread fact is
// never ranked as safe.
func TestStrandedCapabilityLenses_UnreadableCypherIsALowerBoundNotAnAllClear(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentCapabilityLenses(ctx, t, kv)
	priorID := newNanoID(t)
	seedLens(ctx, t, kv, priorID, "meta.lens", "capabilityReadWildcardGrants", "")

	stranded, err := StrandedCapabilityLenses(ctx, kv)
	require.NoError(t, err)
	require.Len(t, stranded, 1)
	require.True(t, stranded[0].CypherUnreadable)
	require.Equal(t, StrandedLensSeverityDiverged, stranded[0].Severity())
}

func TestStrandedCapabilityLenses_ForeignCanonicalNameIsSilent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentCapabilityLenses(ctx, t, kv)
	seedLens(ctx, t, kv, newNanoID(t), "meta.lens", "someOtherPackageLens", "")

	stranded, err := StrandedCapabilityLenses(ctx, kv)
	require.NoError(t, err)
	require.Empty(t, stranded, "a lens named outside the reserved four must never be reported")
}

// TestStrandedCapabilityLenses_WrongClassIsSilent pins the class discriminator
// a cold review found missing: a vtx.meta.* vertex sharing a reserved
// canonicalName but not classed meta.lens (a DDL/entity-type/op-meta) must
// never be reported as a stranded LENS.
func TestStrandedCapabilityLenses_WrongClassIsSilent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentCapabilityLenses(ctx, t, kv)
	seedLens(ctx, t, kv, newNanoID(t), "meta.ddl.vertexType", "capability", "")

	stranded, err := StrandedCapabilityLenses(ctx, kv)
	require.NoError(t, err)
	require.Empty(t, stranded, "a non-meta.lens vertex must never be reported as a stranded lens")
}

func TestStrandedCapabilityLenses_TombstonedPriorLensIsSilent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentCapabilityLenses(ctx, t, kv)

	lensKey := substrate.VertexKey("meta", newNanoID(t))
	val, err := MakeVertexEnvelope(lensKey, "meta.lens", map[string]any{"protected": true})
	putDoc(ctx, t, kv, lensKey, val, err, true)
	cnKey := substrate.AspectKey(lensKey, "canonicalName")
	cnVal, cnErr := MakeAspectEnvelope(cnKey, lensKey, "canonicalName", "canonicalName",
		map[string]any{"value": "capability"})
	putDoc(ctx, t, kv, cnKey, cnVal, cnErr, false)

	stranded, err := StrandedCapabilityLenses(ctx, kv)
	require.NoError(t, err)
	require.Empty(t, stranded, "a tombstoned root is residue already retired, not a fresh finding")
}

// TestStrandedCapabilityLenses_UnloadedPrimordialTableRefuses mirrors
// TestStrandedOperatorEpochs_UnloadedPrimordialTableRefuses: an unloaded id
// would exclude nothing, so every live capability lens — including this
// deployment's own four — would report as stranded. The refusal must land
// before the graph is touched; a real, populated kv (not nil) is what proves
// that, since an empty result over real content demonstrates the scan never
// matched anything rather than merely having nothing to match.
func TestStrandedCapabilityLenses_UnloadedPrimordialTableRefuses(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)
	seedCurrentCapabilityLenses(ctx, t, kv)

	loaded := CapabilityLensID
	t.Cleanup(func() { CapabilityLensID = loaded })
	CapabilityLensID = ""

	stranded, err := StrandedCapabilityLenses(ctx, kv)
	require.ErrorIs(t, err, ErrPrimordialIDsUnloaded)
	require.Empty(t, stranded)
}
