package pipeline

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// writerTokenDiscipline classifies a target write site by what orders it
// against the writes it can race. Withholding's safety argument is a claim
// about every writer of a perEntry key — that each captures its ordering token
// BEFORE the view it writes from, so an older evaluation can never carry a
// higher token than the write it races — and a writer added later with the
// opposite discipline would break the argument silently.
type writerTokenDiscipline string

const (
	// tokenBeforeEvaluation: the token is captured before the state is read,
	// so it can only ever under-state how fresh the view is. These are the two
	// writers the ordering axis is about.
	tokenBeforeEvaluation writerTokenDiscipline = "token captured before evaluation"
	// terminalAuthority: the shred's nullifier, writing at math.MaxInt64. It
	// wins against every token by construction and is never raced.
	terminalAuthority writerTokenDiscipline = "terminal authority (shred nullifier)"
	// personalPlane: a personal target, which is not a RowsReader and so never
	// arms withholding — its ordering is the device revision, not this token.
	personalPlane writerTokenDiscipline = "personal plane (never armed)"
	// unreachableForPerEntry: the raw-write replay, which a perEntry lens's
	// transient failure structurally never takes — it re-evaluates the actor
	// through Reproject instead (results.go's transientActorRetry).
	unreachableForPerEntry writerTokenDiscipline = "raw replay, unreachable for perEntry"
)

// perEntryWriterCensus is the closed set of target write sites in this package,
// by the function that encloses them, with the discipline each one keeps and
// how many calls it makes. A site that is not here fails the census below.
//
// Truncate is deliberately absent: it is a prefix purge, not a row write, and
// matches none of the call shapes the census looks for. It is accounted for by
// the design's §4.3 — a truncating rebuild leaves every key absent, so the
// replay creates each entry and there is nothing to withhold.
//
// The scan matches a plain Upsert/Delete by NAME AND ARITY on any receiver
// expression, not by a list of receiver names the package happens to use, so
// replayWrite contributes four sites rather than the two its outcome-returning
// forms alone would. A writer that calls its adapter `target`, or writes
// `p.adpt.Upsert(...)`, is caught the same way — a gate whose whole job is to
// notice a new writer must not be defeated by naming.
var perEntryWriterCensus = map[string]struct {
	discipline writerTokenDiscipline
	calls      int
}{
	"writeResults":           {tokenBeforeEvaluation, 4},
	"Reproject":              {tokenBeforeEvaluation, 4},
	"Delete":                 {terminalAuthority, 2},
	"DeleteAllForActor":      {terminalAuthority, 2},
	"Hydrate":                {personalPlane, 2},
	"ReprojectPersonalActor": {personalPlane, 2},
	"replayWrite":            {unreachableForPerEntry, 4},
}

// perEntryWriterCensusFloor is the population this census must never silently
// shrink below: the count the scan finds today. An enumeration that finds
// nothing agrees with a census of nothing, and would read as a table of
// unchanged rows rather than as a broken scan.
const perEntryWriterCensusFloor = 20

// TestPerEntryWriterCensus_EveryTargetWriteSiteIsClassified is T4: the
// executable form of the design's §2 row 11 census.
//
// It parses this package's own non-test sources and finds every call that
// reaches the target — UpsertWithOutcome, DeleteWithOutcome, and the plain
// Upsert/Delete on an adapter variable — then classifies each by its enclosing
// function. A new write site fails until its author decides which discipline it
// keeps, which is the point: withholding is sound because of a property every
// writer has, and the only way that property stays true is if adding a writer
// forces the question.
func TestPerEntryWriterCensus_EveryTargetWriteSiteIsClassified(t *testing.T) {
	sites := targetWriteSites(t)

	total := 0
	byFunc := map[string]int{}
	for _, s := range sites {
		byFunc[s.fn]++
		total++
	}
	require.GreaterOrEqual(t, total, perEntryWriterCensusFloor,
		"the scan found %d target write sites, below the floor of %d — an empty or short enumeration "+
			"agrees with any census, so treat this as a broken scan rather than as removed writers",
		total, perEntryWriterCensusFloor)

	var unclassified []string
	for _, s := range sites {
		if _, known := perEntryWriterCensus[s.fn]; !known {
			unclassified = append(unclassified, s.fn+" ("+s.pos+")")
		}
	}
	sort.Strings(unclassified)
	require.Empty(t, unclassified, unclassifiedWriterFailure(unclassified))

	want := map[string]int{}
	known := map[writerTokenDiscipline]bool{
		tokenBeforeEvaluation:  true,
		terminalAuthority:      true,
		personalPlane:          true,
		unreachableForPerEntry: true,
	}
	for fn, entry := range perEntryWriterCensus {
		require.True(t, known[entry.discipline],
			"%s is classified with a discipline §4.4 does not define: %q", fn, entry.discipline)
		want[fn] = entry.calls
	}
	require.Equal(t, want, byFunc,
		"the census and the source disagree about how many target writes each function makes; "+
			"re-read the function that moved and update the census with the discipline its writes keep")
}

// unclassifiedWriterFailure is the whole product of this test for an author who
// has never read it.
func unclassifiedWriterFailure(unclassified []string) string {
	if len(unclassified) == 0 {
		return ""
	}
	return "these functions write to the lens target and the census does not classify them:\n  " +
		strings.Join(unclassified, "\n  ") + `

perentry-unchanged-entry-withholding-design.md §4.4 holds that a withheld write is safe because the
stored watermark already carries the entry's last presence change — and that is only true while EVERY
writer of a perEntry key captures its ordering token BEFORE the state it writes from. A writer that
captures its token AFTER evaluating can carry a token above the presence change it never saw, and its
write then beats a withheld one that was correct.

Add the function to perEntryWriterCensus with the discipline it keeps, and if none of the existing
disciplines describes it, read §4.4 before inventing one.`
}

type targetWriteSite struct {
	fn  string
	pos string
}

// targetWriteSites parses this package's non-test sources and returns every
// call site that writes a row to the lens target: the two outcome-returning
// forms by method name, and the plain Upsert/Delete when the receiver is an
// adapter variable. It is deliberately a source scan rather than a list —
// a maintained list is what the census is checking.
func targetWriteSites(t *testing.T) []targetWriteSite {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()
	var sites []targetWriteSite
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		require.NoError(t, perr)

		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, isCall := n.(*ast.CallExpr)
				if !isCall {
					return true
				}
				if !writesTheTarget(call) {
					return true
				}
				sites = append(sites, targetWriteSite{
					fn:  fn.Name.Name,
					pos: fset.Position(call.Pos()).String(),
				})
				return true
			})
		}
	}
	return sites
}

// writesTheTarget reports whether one call reaches the adapter's write surface,
// by METHOD NAME AND ARITY, on ANY receiver expression.
//
// The receiver is deliberately not part of the test. Matching a list of names
// the package happens to use today (`adpt`, `a`) would let the next writer
// escape the census by calling the variable `target`, or by writing
// `p.adpt.Upsert(...)` — a selector, not an identifier — and a gate whose whole
// job is to notice a new writer must not be defeated by naming.
//
// Arity is what keeps that width honest. adapter.Adapter's Upsert takes four
// arguments (ctx, keys, row, seq) and its Delete three (ctx, keys, seq), while
// the same-named methods this package calls on other things do not: a KV
// handle's Delete is (ctx, key) and its Put/Get are different names entirely.
// So the pair (name, arity) selects the adapter surface without naming a single
// receiver, and a false positive would appear here as an unclassified function
// — loudly — rather than as silence.
func writesTheTarget(call *ast.CallExpr) bool {
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel {
		return false
	}
	switch sel.Sel.Name {
	case "UpsertWithOutcome", "DeleteWithOutcome":
		// Named uniquely enough that the method alone selects them.
		return true
	case "Upsert":
		return len(call.Args) == 4
	case "Delete":
		return len(call.Args) == 3
	}
	return false
}
