package adjacency

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEvictStalestFloors_ChoosesTheVictimWithoutASentinelID pins the eviction's
// victim selection directly, which no Build-level test can do cheaply or
// exhaustively: the interesting inputs are an EdgeID that collides with a
// zero-value sentinel, a tie at equal sequence, and repetition enough to expose
// a dependence on Go's randomised map iteration order.
//
// The empty EdgeID is the case that motivates the explicit `found` flag. An
// EdgeID is an arbitrary string and "" is a legal map key, so a selector that
// used "" to mean "no candidate chosen yet" would both let the first key
// iteration happened to visit win, and leave a floor stored under "" impossible
// to evict at all — an unbounded document, which is precisely what the cap
// exists to prevent.
func TestEvictStalestFloors_ChoosesTheVictimWithoutASentinelID(t *testing.T) {
	t.Run("an empty EdgeID is an ordinary key, not a sentinel", func(t *testing.T) {
		// Repeat: a selector keyed on "" would pick correctly on SOME map
		// iteration orders, so a single pass proves nothing.
		for range 200 {
			floors := map[string]uint64{"": 900}
			for i := range MaxRemovalFloors {
				floors[fmt.Sprintf("edge-%04d", i)] = uint64(i + 1)
			}
			evictStalestFloors(floors)

			require.Len(t, floors, MaxRemovalFloors)
			assert.Contains(t, floors, "", "a floor at seq 900 is not the stalest and must survive")
			assert.NotContains(t, floors, "edge-0000", "seq 1 is the stalest and is the one that goes")
		}
	})

	t.Run("an empty EdgeID at the lowest sequence is evictable", func(t *testing.T) {
		floors := map[string]uint64{"": 1}
		for i := range MaxRemovalFloors {
			floors[fmt.Sprintf("edge-%04d", i)] = uint64(i + 2)
		}
		evictStalestFloors(floors)

		require.Len(t, floors, MaxRemovalFloors)
		assert.NotContains(t, floors, "", "nothing about the empty key makes a floor permanent")
	})

	t.Run("a tie at equal sequence breaks on the EdgeID, the same way every time", func(t *testing.T) {
		for range 200 {
			floors := map[string]uint64{"aaa": 1, "bbb": 1}
			for i := range MaxRemovalFloors - 1 {
				floors[fmt.Sprintf("edge-%04d", i)] = uint64(i + 2)
			}
			evictStalestFloors(floors)

			require.Len(t, floors, MaxRemovalFloors)
			assert.NotContains(t, floors, "aaa", "the lower EdgeID loses the tie, deterministically")
			assert.Contains(t, floors, "bbb")
		}
	})

	t.Run("a map at or under the cap is left exactly as it is", func(t *testing.T) {
		floors := map[string]uint64{}
		for i := range MaxRemovalFloors {
			floors[fmt.Sprintf("edge-%04d", i)] = uint64(i + 1)
		}
		want := make(map[string]uint64, len(floors))
		for k, v := range floors {
			want[k] = v
		}

		evictStalestFloors(floors)
		assert.Equal(t, want, floors)
	})
}

// TestMaxRemovalFloorsIsAtLeastOne pins the one value of the cap that would
// silently disable half the guard. At 0 the eviction loop drops the floor
// removeEdge has just recorded, so every stale create would resurrect its edge
// while the cap still read as a deliberate bound.
func TestMaxRemovalFloorsIsAtLeastOne(t *testing.T) {
	require.GreaterOrEqual(t, MaxRemovalFloors, 1,
		"a cap of 0 evicts the floor it was just handed and turns the stale-create guard off")
}
