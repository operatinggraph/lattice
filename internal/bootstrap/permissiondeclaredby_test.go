package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
)

func TestPermissionDeclaredBy_HappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	permKey := substrate.VertexKey("permission", newNanoID(t))
	val, err := MakeVertexEnvelope(permKey, "permission",
		map[string]any{"operationType": "AttachObject", "declaredBy": "objects-base"})
	putDoc(ctx, t, kv, permKey, val, err, false)

	name, ok, err := PermissionDeclaredBy(ctx, kv, permKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "objects-base", name)
}

func TestPermissionDeclaredBy_AbsentIsNotAnError(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	_, ok, err := PermissionDeclaredBy(ctx, kv, substrate.VertexKey("permission", newNanoID(t)))
	require.NoError(t, err)
	require.False(t, ok)
}

func TestPermissionDeclaredBy_TombstonedIsNotAnError(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	permKey := substrate.VertexKey("permission", newNanoID(t))
	val, err := MakeVertexEnvelope(permKey, "permission",
		map[string]any{"operationType": "AttachObject", "declaredBy": "objects-base"})
	putDoc(ctx, t, kv, permKey, val, err, true)

	_, ok, err := PermissionDeclaredBy(ctx, kv, permKey)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestPermissionDeclaredBy_NoDeclaredByFieldIsNotAnError(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	permKey := substrate.VertexKey("permission", newNanoID(t))
	val, err := MakeVertexEnvelope(permKey, "permission", map[string]any{"operationType": "CreateMetaVertex"})
	putDoc(ctx, t, kv, permKey, val, err, false)

	_, ok, err := PermissionDeclaredBy(ctx, kv, permKey)
	require.NoError(t, err)
	require.False(t, ok, "a kernel permission carries no declaredBy at all")
}

// TestPermissionDeclaredBy_NonStringDeclaredByIsAnError pins the fix for a
// cold review's finding: a declaredBy field that IS present but is not a
// string must not be silently folded into the same "nothing to recommend"
// bucket as a genuinely absent one — that would drop a real, malformed,
// package-origin grant from the reinstall recommendations with no
// diagnostic at all.
func TestPermissionDeclaredBy_NonStringDeclaredByIsAnError(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	permKey := substrate.VertexKey("permission", newNanoID(t))
	val, err := MakeVertexEnvelope(permKey, "permission",
		map[string]any{"operationType": "AttachObject", "declaredBy": 42})
	putDoc(ctx, t, kv, permKey, val, err, false)

	_, ok, err := PermissionDeclaredBy(ctx, kv, permKey)
	require.Error(t, err)
	require.False(t, ok)
}
