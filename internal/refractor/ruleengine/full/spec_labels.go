package full

import "fmt"

// LabelFacts is everything a static reader of a lens spec needs to predict the
// label set the runtime would derive a narrowed Core KV consumer filter from,
// without standing up a pipeline.
//
// The three fields are exactly the inputs pipeline.useFullEngineBranches folds
// together: it unions ReferencedLabels() across the compiled branches, drops
// each ExpansionLabels() member's raw string from that union, and unions the
// taxonomy resolver's concrete answer for it back in. A reader that has the
// abstract types' declared budgets in hand — the package installer does; the
// pipeline does not, at install time there is no resolver — can reconstruct the
// worst-case count from these three alone.
type LabelFacts struct {
	// Referenced is CompiledRule.ReferencedLabels()'s set: every vertex-type
	// label the query's patterns can bind, INCLUDING the raw text of a label
	// that carries the `*` sigil (ReferencedLabels collects label text blind to
	// the sigil).
	Referenced map[string]struct{}

	// Exhaustive is ReferencedLabels()'s second return: false means Referenced
	// is not authoritative and the runtime treats every type as relevant, which
	// takes the consumer filter broad no matter how few labels the set holds.
	Exhaustive bool

	// Expansion is CompiledRule.ExpansionLabels()'s set: the labels written
	// with the trailing `*` taxonomy-expansion sigil, each of which the runtime
	// replaces with its resolved concrete closure.
	Expansion map[string]struct{}
}

// SpecLabels statically parses ruleBody and reports its LabelFacts. It is the
// one place the two derivations are taken together, so a caller cannot pair a
// referenced-label set with an expansion set compiled from a different parse.
//
// It exists for callers outside this package that must reason about a spec's
// label arithmetic without depending on a running pipeline — chiefly the
// package installer's narrowed-filter budget gate, which reaches it through an
// injected interface rather than an import (internal/pkgmgr's CypherParser doc
// has the cycle).
func SpecLabels(ruleBody string) (LabelFacts, error) {
	compiled, err := New().Parse(ruleBody)
	if err != nil {
		return LabelFacts{}, err
	}
	cr, ok := compiled.(*CompiledRule)
	if !ok {
		// Unreachable: Engine.Parse returns *CompiledRule or an error. Reported
		// rather than asserted so a future engine that reuses this entry point
		// fails loudly instead of silently answering "no labels, exhaustive",
		// which a budget gate would read as "nothing to check".
		return LabelFacts{}, fmt.Errorf("full: SpecLabels: parse returned %T, not *full.CompiledRule", compiled)
	}
	referenced, exhaustive := cr.ReferencedLabels()
	return LabelFacts{
		Referenced: referenced,
		Exhaustive: exhaustive,
		Expansion:  cr.ExpansionLabels(),
	}, nil
}
