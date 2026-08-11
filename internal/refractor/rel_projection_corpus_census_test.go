// Relationship-binding corpus census — relationship-data-projection-design.md
// §10.3, and the Refractor dossier's standing obligation on any NEW per-lens
// analysis: run the analysis over the installed corpus and pin what it says,
// rather than deriving the population from a grep of cypher text.
//
// The analysis being censused is the relationship-binding gate
// (ruleengine/full/relbinding.go), which refuses at PARSE every use of a
// relationship that has no value behind it — a variable on a variable-length
// hop, a dereference other than `.key` or `.data`, a bare use of the variable
// as a value, and a reference to a name a WITH stopped carrying. A lens it
// refuses does not compile, so it
// stops projecting entirely; that is exactly why the whole corpus is run
// through it here rather than the two lenses anyone expects to be involved.
//
// The design's own census said two lenses in the whole repo bind a
// relationship variable, both in packages/objects-base, both untyped `-[r]->`.
// This is that claim, executed.
//
// The pinned table carries only the lenses that BIND a relationship, because
// every other lens's verdict is the same empty one — and the population
// assertion below is what keeps that from being a hole: it names the binders
// as a list, so a lens that quietly stops binding (or starts) fails here
// instead of reading as a table of unchanged rows.
//
// WHEN THIS FAILS ON A LENS YOU ADDED OR EDITED: a name joining the population
// is a lens that binds a relationship — check what it reads off it, priced by
// the table below, and remember that naming a relationship multiplies the
// clause's rows per link even when nothing reads it. Then record it. A refusal means the lens does not compile at
// all, and the message names the variable and the reason.
package refractor_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// corpusRelBindings pins the relationship variables every binding cypher in
// the corpus declares, as `<variable>:<relation>[<reads>]`.
//
// The hop shape matters because an untyped `*` binds whichever relation the
// walk crossed, which is the form that makes `type(r)` worth projecting. The
// reads matter because they are this feature's COST: `type` and `key` come out
// of the adjacency entry the walk already holds and are free, while `data` is
// one Core-KV point-read per traversed edge. A lens acquiring `data` is a lens
// whose read surface grew, and this is where that shows up.
var corpusRelBindings = map[string]string{
	"objectAttachments": "r:*[data type]",
	"objectLiveness":    "r:*[]",
}

// corpusRelBindingVerdicts derives the verdict for every executable cypher the
// corpus ships. A cypher that binds no relationship reads "-".
func corpusRelBindingVerdicts(t *testing.T) map[string]string {
	t.Helper()
	eng := full.New()
	got := map[string]string{}
	forEachCorpusCypher(t, func(name, spec string, _ *lens.Rule, _, _ bool) {
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err,
			"%s must parse — a lens the relationship-binding gate refuses does not compile, "+
				"so it stops projecting altogether", name)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.Truef(t, isFull, "%s must compile to the full engine", name)

		parts := []string{}
		for _, rb := range fullCR.RelBindings() {
			relation := rb.Type
			if relation == "" {
				relation = "*"
			}
			parts = append(parts, fmt.Sprintf("%s:%s[%s]", rb.Variable, relation, strings.Join(rb.Reads, " ")))
		}
		verdict := "-"
		if len(parts) > 0 {
			verdict = strings.Join(parts, " ")
		}
		_, duplicate := got[name]
		require.Falsef(t, duplicate, "two corpus cyphers share the name %q", name)
		got[name] = verdict
	})
	return got
}

// TestCorpusRelBindings_PinnedVerdicts is the census. Every cypher in the
// corpus is run through the gate, and the ones that bind a relationship carry
// the variable and hop shape they bind it with.
func TestCorpusRelBindings_PinnedVerdicts(t *testing.T) {
	got := corpusRelBindingVerdicts(t)
	require.Greaterf(t, len(got), 100,
		"the corpus enumeration collapsed to %d cyphers — this census is only worth what it covers", len(got))

	for name, want := range corpusRelBindings {
		have, present := got[name]
		require.Truef(t, present,
			"pinned lens %q is no longer installed — remove its row if the lens was retired", name)
		require.Equalf(t, want, have,
			"%s binds or reads a different relationship surface than pinned. What it binds decides what it "+
				"can project off the relationship; what it READS decides what that costs — a `data` read is "+
				"one Core-KV point-read per traversed edge", name)
	}
}

// TestCorpusRelBindings_BindingLensesAreTheKnownPopulation names the lenses
// that bind a relationship at all, as a list rather than as a property of the
// table above. It is the assertion that makes the table's absences mean
// something: without it, a lens that stopped binding would leave every pinned
// row intact and the census would read as unchanged.
func TestCorpusRelBindings_BindingLensesAreTheKnownPopulation(t *testing.T) {
	got := corpusRelBindingVerdicts(t)
	binders := []string{}
	for name, verdict := range got {
		if verdict != "-" {
			binders = append(binders, name)
		}
	}
	sort.Strings(binders)
	require.Equal(t, []string{
		"objectAttachments",
		"objectLiveness",
	}, binders,
		"the population of lenses that bind a relationship variable has changed. A lens joining this list "+
			"projects off a relationship — price what it reads (a `data` read is a point-read per "+
			"traversed edge) and check the clause's aggregates still mean what they did, then record it")
}
