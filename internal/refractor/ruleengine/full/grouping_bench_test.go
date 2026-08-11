// The measurement for the grouping-key reduction, reported rather than
// asserted: a timing assertion in the suite would be the flaky, weaker form of
// the structural gate that already pins the invariant.
//
// Both arms run the SAME parsed rule over the SAME corpus and the same KVs —
// only the analysis map differs — so the delta is the rendering term and
// nothing else. What is removed is the `rows_k ×` multiplier on the accumulated
// slices; the walk traversal, the per-element collect(DISTINCT) signatures and
// the KV reads are unchanged, and remain in both numbers.
package full

import (
	"context"
	"fmt"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

// benchmarkShape sizes a base-domain corpus to roughly anchors reachable
// anchors. The per-container fan-outs are what give each staging clause a
// non-trivial row count, which is the multiplier the reduction removes.
func benchmarkShape(prefix string, anchors int) readGrantCorpusShape {
	// 3 containers (home + 2) × (n tpl + 2n op + n item + 2 prov + 2 studios ×
	// n/2 sessions) plus the identity-rooted branches: about 19n anchors.
	n := anchors / 19
	if n < 1 {
		n = 1
	}
	return readGrantCorpusShape{
		Prefix:             prefix,
		Containers:         2,
		TplPerContainer:    n,
		OpPerTpl:           2,
		StudioPerContainer: 2,
		SessPerStudio:      n / 2,
		ProvPerContainer:   2,
		ItemPerContainer:   n,
		Tasks:              n,
		Instances:          n,
		Bookings:           n,
		Tabs:               n,
	}
}

// BenchmarkStagedReadGrantProducer measures the real generated base-domain
// producer over ~2k and ~5k anchors, with the grouping-key reduction armed and
// with it forced off (the path every evaluation took before it existed).
func BenchmarkStagedReadGrantProducer(b *testing.B) {
	spec := generatedReadGrantProducers(b)["edgeManifestReadGrants"]
	if spec == "" {
		b.Fatal("edgeManifestReadGrants not generated")
	}

	for _, anchors := range []int{2000, 5000} {
		adjKV, coreKV := startExecKVs(b)
		reg := newFixtureRegistry()
		actorKey := seedReadGrantCorpus(b, reg, adjKV, coreKV, benchmarkShape(fmt.Sprintf("b%d_", anchors), anchors))
		params := ruleengine.EventContext{Parameters: map[string]any{"actorKey": actorKey}}

		for _, arm := range []struct {
			name   string
			reduce bool
		}{{"full-key", false}, {"reduced-key", true}} {
			b.Run(fmt.Sprintf("anchors=%d/%s", anchors, arm.name), func(b *testing.B) {
				eng := New()
				cr, err := eng.Parse(spec)
				if err != nil {
					b.Fatal(err)
				}
				compiled := cr.(*CompiledRule)
				if len(compiled.groupingRedundant) == 0 {
					b.Fatal("the producer armed no reduction — both arms would measure the same path")
				}
				if !arm.reduce {
					compiled = withoutGroupingAnalysis(compiled)
				}

				ctx := context.Background()
				out, err := eng.ExecuteWith(ctx, compiled, params, adjKV, coreKV)
				if err != nil {
					b.Fatal(err)
				}
				granted, _ := out[0].Values["readableAnchors"].([]any)
				b.Logf("%d anchors granted", len(granted))

				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := eng.ExecuteWith(ctx, compiled, params, adjKV, coreKV); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
