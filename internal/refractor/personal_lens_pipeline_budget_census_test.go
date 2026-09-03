package refractor_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// pipelinesPerPersonalLensWriteStep is how many publish pipelines one personal
// lens holds open at once while it processes an event: one carrying its rows and
// one carrying its audit entries. They are deliberately separate — an audit
// failure must not decide the fate of correctly written rows — so the budget has
// to fund both.
//
// It is a floor, not a ceiling: a device hydrate or a grant-change reprojection
// opens a further row pipeline for the same lens, which is why the assertion
// below leaves headroom rather than sitting exactly on the ceiling.
const pipelinesPerPersonalLensWriteStep = 2

// TestPersonalLensPipelineBudget_FitsTheConnectionsAsyncCeiling pins the
// arithmetic the pipeline window is sized by, against the real corpus rather
// than against a remembered number.
//
// The unacknowledged-async-publish budget is PER CONNECTION and a process runs
// one substrate.Conn, so every pipeline open at that moment draws on the same
// ceiling. Crossing it is not a slow path: the publisher stalls 200ms and then
// fails the publish outright. Sizing the window per pipeline while the budget is
// per connection is exactly the mistake this test exists to catch — it fails the
// moment the corpus grows enough personal lenses to overrun the ceiling, or the
// window is raised without raising the ceiling with it.
func TestPersonalLensPipelineBudget_FitsTheConnectionsAsyncCeiling(t *testing.T) {
	// A lens with SpecBranches is visited once per branch, but the pipelines
	// belong to the LENS — one pipeline.Pipeline drives all of a lens's
	// branches through one write step — so the branch suffix is stripped before
	// counting. Counting branches would inflate the census rather than the risk.
	seen := map[string]bool{}
	forEachCorpusCypher(t, func(name, _ string, rule *lens.Rule, _, declaredPersonal bool) {
		if !declaredPersonal {
			return
		}
		if i := strings.IndexByte(name, '#'); i >= 0 {
			name = name[:i]
		}
		seen[name] = true
	})
	personal := make([]string, 0, len(seen))
	for name := range seen {
		personal = append(personal, name)
	}
	sort.Strings(personal)

	require.NotEmpty(t, personal,
		"the census found no personal lens at all — an empty enumeration would make the budget assertion below vacuously true")
	t.Logf("personal lenses in the corpus (%d): %v", len(personal), personal)

	worst := len(personal) * pipelinesPerPersonalLensWriteStep * substrate.DefaultPublishPipelineWindow
	assert.LessOrEqualf(t, worst, substrate.PublishAsyncMaxPending,
		"every personal lens writing at once holds %d × %d × %d = %d unacknowledged publishes, above the connection's ceiling of %d — raise the ceiling or lower the window, because past it a publish stalls 200ms and then fails",
		len(personal), pipelinesPerPersonalLensWriteStep, substrate.DefaultPublishPipelineWindow,
		worst, substrate.PublishAsyncMaxPending)

	// The whole-actor paths — a device attach (Hydrate) and a grant-change
	// reprojection — do NOT scale with the corpus. The control-plane hydrate
	// walks its lenses SERIALLY, one Hydrate call at a time, so one such call
	// holds one pipeline whatever the lens count. What is uncapped is how many
	// of those calls run at once: each RPC is its own handler goroutine. The
	// term is therefore (concurrent whole-actor calls) × window, drawing on
	// whatever the write steps leave.
	//
	// Reported rather than asserted: the test cannot know how many devices
	// attach at once, and the reading is what a future corpus or window change
	// should be read against.
	spare := substrate.PublishAsyncMaxPending - worst
	concurrentWholeActorCalls := spare / substrate.DefaultPublishPipelineWindow
	t.Logf("connection budget: write steps %d of ceiling %d leaves %d, i.e. %d concurrent whole-actor calls (hydrate/reprojection) at a window of %d before the ceiling is reached",
		worst, substrate.PublishAsyncMaxPending, spare,
		concurrentWholeActorCalls, substrate.DefaultPublishPipelineWindow)
}
