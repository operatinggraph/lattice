//go:build ignore

// lint-weaver-classify-by-shape — a reclaim's class is decided by the dispatch
// it resolves to, never by a string a record carries.
//
// THE HAZARD. internal/weaver classifies an open episode as collapse-only —
// paced on the reclaims series, booked as no attempt, its claimId preserved so a
// re-dispatch collapses onto the artifact already open — through one predicate,
// collapseOnlyReclaim, whose first argument is a dispatch class: assignTask,
// triggerLoom, proposedOp. The engine also carries strings that LOOK like one
// and are not: a planned-mode mark records the leg's catalog ref (`setTerms`),
// a goal gap's playbook entry carries an empty Action, an escalation's mark
// records `directOp`. Feeding any of those to the predicate reads "false" with
// full confidence — an open human task's re-arm becomes an attempt, is booked
// into the `__effect` window, and re-fires every sweep interval unpaced. That is
// how a goal target's retry budget of six was spent by a task nobody had opened.
//
// The class was found twice: staleMark's external-vs-userTask split by action
// name (lease-signing's bgcheck/payment gaps never retried after a timeout), and
// the reclaim's collapseOnlyReclaim(rec.Action, …) over a goal leg. Twice-seen
// gets a gate.
//
// THE RULE. Every collapseOnlyReclaim call's first argument is a plain
// identifier — a local holding the RESOLVED dispatch action (resolvedAction,
// dispatchAction: what resolvePlannedAction / resolvedLegAction returned for the
// pinned or fresh leg). A selector (`rec.Action`, `ga.Action`, `esc.Action`), a
// call, or a literal is a finding: it is a recorded name, or a value the gate
// cannot see was resolved. A static gap resolves to its own action, so the rule
// costs it nothing.
//
// SCOPE. One package, by source text, no type information: the predicate is an
// unexported function of one engine. staleMark's own classifier
// (externalDispatchGap over a GapAction) is NOT covered — it reads a playbook
// entry rather than a mark, its planned-mode limitation is parked with a
// designer row, and gating it now would red a shape the design has not decided.
//
// A SELF-TEST RUNS ON EVERY INVOCATION (synthetic sources through checkFile;
// verbose with --selftest, silent-unless-failing otherwise; exit 2 on a
// mismatch), and the corpus run refuses its all-clear if it found zero calls.
//
// STRICT=1 exits non-zero on any finding; unset, it reports and exits 0.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	weaverDir     = "internal/weaver"
	predicateFunc = "collapseOnlyReclaim"
)

type stats struct {
	files int
	calls int
}

func main() {
	strict := os.Getenv("STRICT") == "1"
	verboseSelfTest := false
	for _, a := range os.Args[1:] {
		if a == "--selftest" {
			verboseSelfTest = true
		}
	}
	runSelfTest(verboseSelfTest)

	var findings []string
	var st stats

	entries, err := os.ReadDir(weaverDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint-weaver-classify-by-shape: cannot read %s: %v\n", weaverDir, err)
		os.Exit(2)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	fset := token.NewFileSet()
	for _, n := range names {
		path := filepath.Join(weaverDir, n)
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "lint-weaver-classify-by-shape: parse %s: %v\n", path, perr)
			os.Exit(2)
		}
		st.files++
		findings = append(findings, checkFile(fset, path, file, &st)...)
	}

	if st.calls == 0 {
		fmt.Fprintf(os.Stderr, "lint-weaver-classify-by-shape: found no %s call in %s — the gate examined nothing, refusing the all-clear\n",
			predicateFunc, weaverDir)
		os.Exit(2)
	}
	if len(findings) == 0 {
		fmt.Printf("lint-weaver-classify-by-shape: clean — %d file(s), %d %s call(s), every one classifying on a resolved dispatch action\n",
			st.files, st.calls, predicateFunc)
		return
	}
	for _, f := range findings {
		fmt.Println(f)
	}
	fmt.Printf("lint-weaver-classify-by-shape: %d finding(s) — a gap class is decided by the dispatch's SHAPE, never by a recorded name (docs/components/weaver.md § Review keeps catching)\n", len(findings))
	if strict {
		os.Exit(1)
	}
}

// checkFile reports every predicateFunc call whose first argument is not a
// plain identifier. It walks the whole file, so a call inside a closure or a
// nested block is examined exactly like a top-level one.
func checkFile(fset *token.FileSet, path string, file *ast.File, st *stats) []string {
	var findings []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		fn, ok := call.Fun.(*ast.Ident)
		if !ok || fn.Name != predicateFunc || len(call.Args) == 0 {
			return true
		}
		st.calls++
		pos := fset.Position(call.Pos())
		switch arg := call.Args[0].(type) {
		case *ast.Ident:
			return true
		case *ast.SelectorExpr:
			findings = append(findings, fmt.Sprintf("%s:%d: %s classifies on the recorded field %s.%s — resolve the pinned leg's dispatch action (resolvePlannedAction / resolvedLegAction) and pass that",
				path, pos.Line, predicateFunc, exprString(arg.X), arg.Sel.Name))
		default:
			findings = append(findings, fmt.Sprintf("%s:%d: %s's first argument is not a resolved-action identifier (%T) — the gate cannot see it was resolved",
				path, pos.Line, predicateFunc, call.Args[0]))
		}
		return true
	})
	return findings
}

func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	default:
		return fmt.Sprintf("%T", e)
	}
}

// runSelfTest proves the walk on synthetic sources: a recorded-field argument
// and a call argument are findings; a resolved local is not.
func runSelfTest(verbose bool) {
	cases := []struct {
		name string
		src  string
		want int
	}{
		{"resolved-local", `package weaver
func f(rec *mark) { dispatchAction := resolve(rec.Action); _ = collapseOnlyReclaim(dispatchAction, false) }`, 0},
		{"mark-field", `package weaver
func f(rec *mark) { _ = collapseOnlyReclaim(rec.Action, false) }`, 1},
		{"playbook-field", `package weaver
func f(ga GapAction) { if collapseOnlyReclaim(ga.Action, true) { return } }`, 1},
		{"call-argument", `package weaver
func f(rec *mark) { _ = collapseOnlyReclaim(actionOf(rec), false) }`, 1},
		{"nested-closure", `package weaver
func f(rec *mark) { g := func() bool { return collapseOnlyReclaim(rec.Action, false) }; _ = g }`, 1},
	}
	failed := false
	for _, c := range cases {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, c.name+".go", c.src, 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "lint-weaver-classify-by-shape: self-test %s does not parse: %v\n", c.name, err)
			os.Exit(2)
		}
		var st stats
		got := len(checkFile(fset, c.name+".go", file, &st))
		if got != c.want {
			failed = true
			fmt.Fprintf(os.Stderr, "lint-weaver-classify-by-shape: self-test %s: %d finding(s), want %d\n", c.name, got, c.want)
		} else if verbose {
			fmt.Printf("self-test %s: ok (%d finding(s))\n", c.name, got)
		}
	}
	if failed {
		os.Exit(2)
	}
}
