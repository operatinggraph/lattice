package pipeline

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRuleState_RoundTripCarriesEveryField gates the hand-maintained round trip
// every rule publication makes: Pipeline fields → ruleState (Pipeline.ruleState)
// → Pipeline fields (publishRuleState). Two lists, written by hand, that nothing
// held in step.
//
// A ruleState field added with a line in one list and not the other reads as its
// ZERO VALUE on every event, with nothing failing anywhere — no error, no log,
// no malformed snapshot. And the zero value is the ADMITTING answer for the
// fields that gate: personalClockRefusal's "" licenses the narrowing,
// anchorHopsPerBranchRefusal's "" beside a nil anchorHopsPerBranch is a union
// over no walks returned as a real anchor set, walkScopeRefusal's "" is a scope
// that was never refused. So the omission is fail-OPEN, on the security plane,
// and it has now been reasoned about three times in three different fields'
// doc comments rather than gated once.
//
// This is the gate. It discovers the universe of fields from the SOURCE at test
// time rather than from a maintained list, so a newly added field fails by name
// until its author decides which of the two things it is: carried through the
// trip, or allow-listed with a reason. There is no third state, and "nobody
// thought about it" is not one of them.
//
// It reads the two lists STRUCTURALLY, and checks more than presence: each side
// must name the SAME Pipeline field. `personalClockRefusal: p.plainNarrowingBlocked`
// is a copy-paste that both lists would otherwise report as complete, and it
// would publish one field's value under another's name for the life of the
// process.
//
// It does not replace the behavioural pins beside it —
// TestRuleState_BranchAnchorHopsSurvivePublication (which additionally pins that
// a REFUSED set publishes no graphs) and the personalClockRefusal vectors in
// anchor_derivation_personal_licence_internal_test.go (which pin the value the
// publication derives, not merely that it survives). Those pin BEHAVIOUR for the
// fields that have already burned us; this pins COMPLETENESS for every field
// there is. Keep all three.
func TestRuleState_RoundTripCarriesEveryField(t *testing.T) {
	file := parseRuleStateSource(t)

	declared := ruleStateDeclaredFields(t, file)
	require.Greater(t, len(declared), 15,
		"only %d ruleState fields were discovered — the source walk has stopped finding the struct, and a gate that finds nothing passes everything", len(declared))

	intoSnapshot := ruleStateSnapshotSources(t, file)
	intoPipeline := ruleStatePublishTargets(t, file)

	t.Run("AllowListIsNotStale", func(t *testing.T) {
		inStruct := map[string]bool{}
		for _, f := range declared {
			inStruct[f] = true
		}
		var stale []string
		for name := range ruleStateRoundTripExempt {
			if !inStruct[name] {
				stale = append(stale, name)
			}
		}
		sort.Strings(stale)
		require.Emptyf(t, stale,
			"ruleStateRoundTripExempt names fields ruleState no longer declares: %v.\n"+
				"The field was renamed or removed; delete the stale entry, or the allow-list keeps excusing a "+
				"field that does not exist while its replacement goes ungated.", stale)
	})

	for _, field := range declared {
		if _, exempt := ruleStateRoundTripExempt[field]; exempt {
			continue
		}
		from, taken := intoSnapshot[field]
		require.Truef(t, taken, "%s", ruleStateRoundTripFailure(field, "Pipeline.ruleState", "reads it into the snapshot"))
		to, published := intoPipeline[field]
		require.Truef(t, published, "%s", ruleStateRoundTripFailure(field, "publishRuleState", "writes it back to the Pipeline"))
		require.Equalf(t, from, to,
			"ruleState.%[1]s round-trips through TWO DIFFERENT Pipeline fields — Pipeline.ruleState reads p.%[2]s, publishRuleState writes p.%[3]s.\n"+
				"One of the two is a copy-paste. Whichever it is, the effect is that a publication stores one field's value "+
				"under another's name for the life of the process, and every gate reading %[1]s answers about the wrong rule.",
			field, from, to)
	}
}

// ruleStateRoundTripFailure is the whole product of this test: an author who has
// never seen this rule has to be able to act on it without reading the test.
func ruleStateRoundTripFailure(field, fn, what string) string {
	return fmt.Sprintf(`ruleState.%[1]s is DROPPED — %[2]s never %[3]s.

The round trip Pipeline fields -> ruleState -> Pipeline fields is two hand-maintained
lists in rulestate.go, and a field with a line in only one of them reads as its ZERO
VALUE on every event. Nothing else fails when this happens: the snapshot stays
well-formed, the publication succeeds, and the value is simply gone.

Fix it one of two ways:

  1. Add the missing line — %[2]s, in internal/refractor/pipeline/rulestate.go,
     beside the neighbours that already carry their field. This is the right answer for
     anything DERIVED from the compiled rule: useFullEngineBranches derives it per
     publication precisely so a hot reload cannot leave the previous body's value armed,
     and a field that does not survive the trip is derived and then discarded.

  2. Add %[1]q to ruleStateRoundTripExempt in this file, with a one-line reason — but
     only if the field genuinely is NOT part of the published rule, the way the
     generation counter is not. "It is always recomputed anyway" is not a reason: the
     gates read the snapshot, not the recomputation.

Do not delete or weaken this assertion. It gates a fail-OPEN class: for these fields the
zero value is the admitting answer — an empty refusal string licenses, an empty graph set
is a union over no walks returned as a real anchor set — so the omission grants rather
than denies, on the plane where a wrong grant is a row a device may no longer read.`,
		field, fn, what)
}

// ruleStateRoundTripExempt are the ruleState fields that are deliberately NOT
// carried through the trip, each with the reason it is not.
var ruleStateRoundTripExempt = map[string]string{
	"gen": "the publication's own counter: publishRuleState INCREMENTS p.ruleGen rather than taking the " +
		"caller's, so a snapshot's generation is an output of the publication and never an input to it. " +
		"Carrying it in would let a caller rewind the counter every gate reader uses to tell two rules apart.",
}

// parseRuleStateSource parses rulestate.go, the file that owns both halves of
// the trip. Reading the SOURCE rather than reflecting over the type is what lets
// this gate see the two hand-maintained lists at all: they are statements, not
// data, and no runtime value distinguishes a field that was carried from one
// that was assigned its own zero.
func parseRuleStateSource(t *testing.T) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "rulestate.go", nil, 0)
	require.NoError(t, err, "rulestate.go must parse — this gate reads it as source")
	return file
}

// ruleStateDeclaredFields is the universe: every field the ruleState struct
// declares, in declaration order.
func ruleStateDeclaredFields(t *testing.T, file *ast.File) []string {
	t.Helper()
	var out []string
	for _, decl := range file.Decls {
		gen, isGen := decl.(*ast.GenDecl)
		if !isGen || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, isType := spec.(*ast.TypeSpec)
			if !isType || ts.Name.Name != "ruleState" {
				continue
			}
			st, isStruct := ts.Type.(*ast.StructType)
			require.True(t, isStruct, "ruleState must be a struct type")
			for _, f := range st.Fields.List {
				for _, name := range f.Names {
					out = append(out, name.Name)
				}
			}
		}
	}
	require.NotEmpty(t, out, "the ruleState struct declaration was not found in rulestate.go")
	return out
}

// ruleStateSnapshotSources reads the first hand-maintained list: the composite
// literal Pipeline.ruleState returns, as ruleState field -> the Pipeline field
// it is read from.
func ruleStateSnapshotSources(t *testing.T, file *ast.File) map[string]string {
	t.Helper()
	fn := methodNamed(t, file, "ruleState")
	out := map[string]string{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		lit, isLit := n.(*ast.CompositeLit)
		if !isLit {
			return true
		}
		if ident, ok := lit.Type.(*ast.Ident); !ok || ident.Name != "ruleState" {
			return true
		}
		for _, elt := range lit.Elts {
			kv, isKV := elt.(*ast.KeyValueExpr)
			require.Truef(t, isKV,
				"Pipeline.ruleState builds its snapshot with a POSITIONAL element — this gate reads keyed elements, "+
					"and a positional literal silently re-binds every field when one is inserted. Use field: value.")
			key, isIdent := kv.Key.(*ast.Ident)
			require.True(t, isIdent, "a ruleState literal key must be a field name")
			out[key.Name] = pipelineFieldOf(t, kv.Value, "Pipeline.ruleState's "+key.Name)
		}
		return false
	})
	require.NotEmpty(t, out, "Pipeline.ruleState no longer returns a ruleState composite literal — this gate cannot read what it carries")
	return out
}

// ruleStatePublishTargets reads the second hand-maintained list: publishRuleState's
// assignments, as ruleState field -> the Pipeline field it is written to.
func ruleStatePublishTargets(t *testing.T, file *ast.File) map[string]string {
	t.Helper()
	fn := methodNamed(t, file, "publishRuleState")
	out := map[string]string{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, isAssign := n.(*ast.AssignStmt)
		if !isAssign || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		lhs, lhsOK := selectorOn(assign.Lhs[0], "p")
		rhs, rhsOK := selectorOn(assign.Rhs[0], "rs")
		if !lhsOK || !rhsOK {
			return true
		}
		out[rhs] = lhs
		return true
	})
	require.NotEmpty(t, out, "publishRuleState no longer assigns any rs.<field> to a p.<field> — this gate cannot read what it publishes")
	return out
}

// methodNamed finds a method on *Pipeline by name.
func methodNamed(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range file.Decls {
		fn, isFn := decl.(*ast.FuncDecl)
		if isFn && fn.Recv != nil && fn.Name.Name == name && fn.Body != nil {
			return fn
		}
	}
	t.Fatalf("rulestate.go declares no method %q — this gate reads its body as one half of the round trip", name)
	return nil
}

// pipelineFieldOf reads `p.X` and returns "X", failing on anything else: a
// snapshot field computed inline rather than read from a Pipeline field has no
// Pipeline field for publishRuleState to write back to, which is the drop this
// gate exists to catch, in a shape the equality check could not see.
func pipelineFieldOf(t *testing.T, expr ast.Expr, what string) string {
	t.Helper()
	field, ok := selectorOn(expr, "p")
	require.Truef(t, ok,
		"%s is not read from a Pipeline field. Every member of the published rule has to have somewhere to "+
			"live between publications; a value computed here is recomputed on every snapshot and can never be "+
			"published back, so it is not part of the rule state.", what)
	return field
}

// selectorOn reads `<recv>.X` and returns "X".
func selectorOn(expr ast.Expr, recv string) (string, bool) {
	sel, isSel := expr.(*ast.SelectorExpr)
	if !isSel {
		return "", false
	}
	ident, isIdent := sel.X.(*ast.Ident)
	if !isIdent || ident.Name != recv {
		return "", false
	}
	return sel.Sel.Name, true
}
