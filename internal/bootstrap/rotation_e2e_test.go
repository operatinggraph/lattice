package bootstrap

// TestRotation_SecondSeedAfterIDFileRotationStrandsThePriorRole drives the
// real end-to-end vector Fire 1's §6.2 scope note deferred: rather than a
// hand-planted "prior epoch" fixture (strandedepoch_test.go's own shape),
// this actually calls LoadOrGenerate + the real seed path twice against one
// bucket, with no wipe between — the literal sequence a re-bootstrap runs
// (primordial-epoch-stranded-authority-design.md §1).

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
)

func TestRotation_SecondSeedAfterIDFileRotationStrandsThePriorRole(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	seeder, kv := newReconcileSeeder(ctx, t)
	// This test calls LoadOrGenerate twice more below, each time overwriting
	// the package-level primordial globals wholesale (unlike every other test
	// in this file, which mutates at most one field and restores it via
	// t.Cleanup). Leaving epoch B's globals live for whatever test runs next
	// in this binary would be a silent cross-test dependency, so repopulate a
	// fresh, internally-consistent set on the way out — every other test
	// already establishes its own via populateForTest/newPerKeySeeder before
	// reading anything, so this only needs to leave the globals coherent, not
	// bit-identical to what they were before this test ran.
	t.Cleanup(func() { populateForTest(t) })

	// Epoch A: a fresh id file, generated and seeded for real.
	pathA := filepath.Join(t.TempDir(), "lattice.bootstrap.json")
	freshA, err := LoadOrGenerate(pathA)
	require.NoError(t, err)
	require.True(t, freshA, "no file existed yet — this must be a fresh generation")

	epochARoleID := RoleOperatorID
	epochAHolderKeys := sortedCopy([]string{
		substrate.VertexKey("identity", BootstrapIdentityID),
		substrate.VertexKey("identity", LoomIdentityID),
		substrate.VertexKey("identity", WeaverIdentityID),
		substrate.VertexKey("identity", BridgeIdentityID),
		substrate.VertexKey("identity", ObjmgrIdentityID),
		substrate.VertexKey("identity", PrivacyIdentityID),
	})
	require.NoError(t, seeder.SeedPrimordial(ctx), "epoch A's real seed path")

	// Sanity: a single-epoch bucket must be silent before epoch B ever exists.
	stranded, err := StrandedOperatorEpochs(ctx, kv)
	require.NoError(t, err)
	require.Empty(t, stranded, "epoch A alone, freshly seeded, must not report itself as stranded")

	// The expected grant set is read live off epoch A's own role, right after
	// its real seed, rather than hardcoded to a specific permission count:
	// the kernel grants the operator role more than the three meta-permission
	// vertices (package-lifecycle permissions land on it too), and any fixed
	// count here would be exactly the premise the standing checklist warns
	// against — reproducible from a live read, never assumed.
	epochAGrantEdges, _, err := liveEdgesInto(ctx, kv, "lnk.permission.*.grantedBy.role."+epochARoleID, "permission")
	require.NoError(t, err)
	require.NotEmpty(t, epochAGrantEdges, "epoch A's seed must have granted at least the kernel meta-permissions")
	epochAGrantKeys := make([]string, 0, len(epochAGrantEdges))
	for _, e := range epochAGrantEdges {
		epochAGrantKeys = append(epochAGrantKeys, e.sourceKey)
	}
	epochAGrantKeys = sortedCopy(epochAGrantKeys)

	// Epoch B: a SECOND fresh id file — simulating lattice.bootstrap.json
	// being regenerated — loaded and seeded against the SAME bucket, no wipe.
	pathB := filepath.Join(t.TempDir(), "lattice.bootstrap.json")
	freshB, err := LoadOrGenerate(pathB)
	require.NoError(t, err)
	require.True(t, freshB)
	require.NotEqual(t, epochARoleID, RoleOperatorID,
		"a second fresh generation must mint a genuinely new operator role id")

	require.NoError(t, seeder.SeedPrimordial(ctx), "epoch B's real seed path, against epoch A's bucket")
	epochBRoleID := RoleOperatorID

	// CurrentEpochOperatorReachable over the real seed path: epoch B (just
	// seeded) must read as reachable, and loading epoch A's id file back —
	// simulating an operator who still has the pre-rotation
	// lattice.bootstrap.json on hand, the realistic mistake a cold review
	// proved the naive "is my role live and held" check does not catch —
	// must NOT, even though epoch A's role and its own holders are still
	// completely live.
	reachable, err := CurrentEpochOperatorReachable(ctx, kv)
	require.NoError(t, err)
	require.True(t, reachable, "epoch B, just seeded, must read as the reachable current epoch")

	RoleOperatorID = epochARoleID
	reachable, err = CurrentEpochOperatorReachable(ctx, kv)
	require.NoError(t, err)
	require.False(t, reachable, "loading epoch A's own (real, still-live) id file back must not read as reachable once epoch B exists")
	RoleOperatorID = epochBRoleID

	// Epoch B is now current. Epoch A's role must report as stranded, with
	// exactly the holders and grants the real seed path — not a hand-planted
	// fixture — actually wrote for it.
	stranded, err = StrandedOperatorEpochs(ctx, kv)
	require.NoError(t, err)
	require.Len(t, stranded, 1, "exactly epoch A's role must be reported, never epoch B's own")
	require.Equal(t, substrate.VertexKey("role", epochARoleID), stranded[0].RoleKey)
	require.Equal(t, epochAHolderKeys, sortedCopy(stranded[0].Holders),
		"epoch A's own six primordial identities, unaccounted for by epoch B, are the reported holders")
	require.Empty(t, stranded[0].ReachableVia)
	require.Equal(t, epochAGrantKeys, sortedCopy(stranded[0].GrantedBy),
		"epoch A's three kernel meta-permission grants must still be reported live")
	require.Equal(t, StrandedSeverityLiveAuthority, stranded[0].Severity())

	// The lens-plane sibling finding, over the same two real seeds.
	strandedLenses, err := StrandedCapabilityLenses(ctx, kv)
	require.NoError(t, err)
	gotNames := make([]string, 0, len(strandedLenses))
	for _, l := range strandedLenses {
		gotNames = append(gotNames, l.CanonicalName)
	}
	sort.Strings(gotNames)
	require.Equal(t, []string{"capability", "capabilityRead", "capabilityReadGrants", "capabilityReadWildcardGrants"},
		gotNames, "epoch A's own four capability lenses must all be reported, epoch B's own four never")
}
