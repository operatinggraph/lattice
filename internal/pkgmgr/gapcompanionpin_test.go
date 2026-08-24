package pkgmgr

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The installer re-states internal/weaver's gap-column vocabulary and its
// external-class action set rather than importing them — it must not depend on
// an engine. That restatement is only safe while something ties it back to the
// engine, so the two tests below read internal/weaver's own source as the source
// of truth.
//
// They read it by PATH rather than by import on purpose: every constant
// involved (inflightColumnPrefix, maxretriesColumnPrefix, gapColumnPrefix, the
// five action names) is unexported in internal/weaver, so even a test-file
// import — which would not cycle, and would not enter the production package's
// dependency tree — could not reach a single one of them. Reading the package
// source keeps the production boundary intact and still fails loudly the moment
// the engine's vocabulary or its classifier moves. It mirrors what
// internal/refractor/ruleengine/full's variable_refs_completeness_test does with
// go/ast, and what internal/processor's conformance_errorcode_table_test does
// reading its contract by path.

const weaverPackageDir = "../weaver"

// weaverStringConsts parses every non-test file of internal/weaver and returns
// its package-level untyped string constants by name.
func weaverStringConsts(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(weaverPackageDir, "*.go"))
	if err != nil {
		t.Fatalf("glob %s: %v", weaverPackageDir, err)
	}
	out := make(map[string]string)
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
					continue
				}
				lit, ok := vs.Values[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				val, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				out[vs.Names[0].Name] = val
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("no string constants found in %s — this pin is reading the wrong place", weaverPackageDir)
	}
	return out
}

// TestGapCompanionPrefixes_MatchWeaverVocabulary pins the gap-column vocabulary
// this installer re-states against internal/weaver's own constants. Without it a
// §10.2/§10.3 rename would leave validateGapCompanionPair silently looking for
// columns no lens can ever declare — a gate that passes everything.
func TestGapCompanionPrefixes_MatchWeaverVocabulary(t *testing.T) {
	t.Parallel()
	consts := weaverStringConsts(t)
	for _, tc := range []struct{ engineName, restated string }{
		{"gapColumnPrefix", gapColumnPrefix},
		{"inflightColumnPrefix", inflightColumnPrefix},
		{"maxretriesColumnPrefix", maxretriesColumnPrefix},
		{"actionTriggerLoom", actionTriggerLoom},
		{"actionAssignTask", actionAssignTask},
		{"actionDirectOp", actionDirectOp},
		{"actionProposedOp", actionProposedOp},
		{"actionSurface", actionSurface},
	} {
		canonical, ok := consts[tc.engineName]
		if !ok {
			t.Errorf("internal/weaver no longer declares a string constant %s — pkgmgr restates it as %q with nothing pinning it",
				tc.engineName, tc.restated)
			continue
		}
		if canonical != tc.restated {
			t.Errorf("%s: pkgmgr restates %q but internal/weaver's canonical value is %q",
				tc.engineName, tc.restated, canonical)
		}
	}
}

// TestStaticallyExternalGapActions_MatchWeaverClassifier pins the SET against
// internal/weaver's own classifier, not just the strings.
//
// The relation is containment, not equality: the gate refuses only where §10.3's
// rule bites, which is narrower than "the engine calls this external". directOp
// is the action whose default retry budget a declared inflight_<g> actually
// declines, so it must be IN the set and IN the engine's unconditional-external
// case clause. Every other member of that clause — proposedOp today — is
// deliberately excluded because the default-budget fallback never applied to it,
// so refusing it would refuse a shape no worse than the one the gate admits.
//
// Both directions are checked because both failure modes are real: an action
// leaving the engine's clause while staying in the set would make the installer
// refuse a gap the engine no longer treats as external (a false refusal that
// blocks an install), and directOp falling out of the set would silently disarm
// the gate entirely.
func TestStaticallyExternalGapActions_MatchWeaverClassifier(t *testing.T) {
	t.Parallel()
	consts := weaverStringConsts(t)

	idents := unconditionallyExternalCaseIdents(t)
	clause := make(map[string]bool, len(idents))
	clauseNames := make([]string, 0, len(idents))
	for _, name := range idents {
		val, ok := consts[name]
		if !ok {
			t.Fatalf("externalDispatchGap's unconditional-external case names %s, which is not a string constant of internal/weaver", name)
		}
		clause[val] = true
		clauseNames = append(clauseNames, val)
	}
	sort.Strings(clauseNames)

	restated := make([]string, 0, len(staticallyExternalGapActions))
	for action := range staticallyExternalGapActions {
		restated = append(restated, action)
	}
	sort.Strings(restated)

	// directOp must be on both sides: it is the only action whose engine-side
	// default budget a declared inflight_<g> takes away, which is the whole
	// premise of the refusal.
	if !staticallyExternalGapActions[actionDirectOp] {
		t.Errorf("%q must be in staticallyExternalGapActions — it is the action whose default retry budget "+
			"a declared inflight_<g> declines, so dropping it disarms the §10.3 gate", actionDirectOp)
	}
	if !clause[actionDirectOp] {
		t.Errorf("internal/weaver's externalDispatchGap no longer classifies %q as external outright — "+
			"the installer's static refusal no longer agrees with the engine", actionDirectOp)
	}

	// Subset: the gate may never refuse an action the engine does not classify
	// external outright.
	for _, action := range restated {
		if !clause[action] {
			t.Errorf("staticallyExternalGapActions holds %q, which internal/weaver's externalDispatchGap does NOT "+
				"classify external outright (its clause is %v) — the gate would refuse a shape the engine treats as non-external",
				action, clauseNames)
		}
	}

	// Every excluded member of the clause must be excluded for a reason stated
	// in this test, so a NEW external action cannot be silently ignored by the
	// gate: adding one to the engine fails here until someone decides whether
	// §10.3's rule bites for it.
	excludedForAReason := map[string]string{
		actionProposedOp: "no default-budget fallback applies, so inflight-only is no worse than declaring neither " +
			"(gapSuppressed's fallback is directOp-gated), and its reclaim is collapse-paced (collapseOnlyReclaim)",
	}
	for _, action := range clauseNames {
		if staticallyExternalGapActions[action] {
			continue
		}
		if _, stated := excludedForAReason[action]; !stated {
			t.Errorf("internal/weaver classifies %q external outright but the installer's gate excludes it with no "+
				"reason recorded — decide whether Contract #10 §10.3's companion-pair rule bites for it, then either "+
				"add it to staticallyExternalGapActions or record why it is excluded here", action)
		}
	}
}

// unconditionallyExternalCaseIdents returns the constant identifiers named by
// the case clause of internal/weaver's externalDispatchGap whose whole body is a
// single `return true, ...` — the actions it decides external with no row and no
// pattern index. It fails rather than returning empty if the function or that
// clause cannot be found, so a refactor of the classifier disarms nothing
// silently.
func unconditionallyExternalCaseIdents(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(weaverPackageDir, "*.go"))
	if err != nil {
		t.Fatalf("glob %s: %v", weaverPackageDir, err)
	}
	fset := token.NewFileSet()
	var found []string
	clauses := 0
	fn := false
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name.Name != "externalDispatchGap" || fd.Body == nil {
				continue
			}
			fn = true
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				cc, ok := n.(*ast.CaseClause)
				if !ok || len(cc.List) == 0 || len(cc.Body) != 1 {
					return true
				}
				ret, ok := cc.Body[0].(*ast.ReturnStmt)
				if !ok || len(ret.Results) == 0 {
					return true
				}
				if id, ok := ret.Results[0].(*ast.Ident); !ok || id.Name != "true" {
					return true
				}
				clauses++
				for _, expr := range cc.List {
					id, ok := expr.(*ast.Ident)
					if !ok {
						t.Fatalf("externalDispatchGap's unconditional-external case names a non-identifier %T", expr)
					}
					found = append(found, id.Name)
				}
				return true
			})
		}
	}
	if !fn {
		t.Fatalf("internal/weaver no longer declares externalDispatchGap — the installer's static external-class set has nothing pinning it")
	}
	if clauses != 1 {
		t.Fatalf("expected exactly one case clause in externalDispatchGap whose whole body returns external=true, found %d — "+
			"the classifier has been reshaped and this pin must be reworked, not deleted", clauses)
	}
	return found
}
