//go:build ignore

// lint-weaver-retire-above-guard — a leg's arms are a lattice, not a list:
// every RETIRE belongs above every "cannot act" GUARD (docs/components/weaver.md
// § Review keeps catching, minted four times, most recently 2026-09-07 at three
// seams at once).
//
// THE HAZARD. A release is a RETIRE — a fact about a leg that has already RUN,
// true whatever else the row is in the middle of, so it is owed to every caller
// and belongs ABOVE every gate. An advance is an ACT — a real op fired at a real
// entity — and so it owes every "cannot act" gate lane 1 fires under, and belongs
// BELOW them. A seam that both records a boundary (the release) and dispatches
// from it (the advance) is TWO ARMS, and mis-ordering them either strands the
// retire behind a guard that never lifts, or fires the act with no guard at all.
// The evaluator's own doc comment on advanceReleasedLeg names the mechanism this
// gate mechanizes: "THE ADVANCE CARRIES ITS OWN 'CANNOT ACT' GATES, and that is
// the whole reason the release/advance pair is safe to write at a seam that sits
// above them... Holding them HERE rather than at each release site is what makes
// an ungated route impossible to write."
//
// THE RULE has three parts.
//
// Rule 1 (the dispatch-seam allowlist). fireEpisode is the single act that
// starts an episode. It is called ONLY from the seams declared in
// declaredSeams below — dispatchGap (lane 1), advanceReleasedLeg (the
// released-leg advance), escalateGap (the escalation episode) and sweepCount
// (the sweep's operator re-arm). Any other caller is a finding: a new dispatch
// seam must be declared here, which forces its guards to be reviewed against
// Rules 2 and 3 before it can pass. fireEpisode's own declaration is ignored,
// and so is every *_test.go file (out of this gate's scope, like the
// classify-by-shape precedent).
//
// Rule 2 (the advance carries its own gates). Inside advanceReleasedLeg's own
// body, BEFORE its first ACT (its first call to any of planGap, escalateGap,
// fireEpisode), there must be BOTH: (a) an `if` whose condition calls
// boolColumn with the string literal "violating", and whose body returns; and
// (b) an `if` whose condition calls gapSuppressedWithCount, and whose body
// returns. Missing either is a finding. So is either gate existing only BELOW
// the first act — presence alone proves nothing, since a gate written after
// the act it was meant to guard has already let the act run; position is
// checked by comparing token offsets, never by presence alone.
//
// Rule 3 (retire above act — the advance is reachable only from a release
// arm). Every call to advanceReleasedLeg outside its own declaration must sit
// lexically inside an `if` statement whose condition calls a release —
// releaseCompletedLeg or releaseAdvancedProposalLeg. This is the retire/act
// split at the call site: the release decides the boundary and only inside
// that decision does the advance get to run. "Lexically inside" is checked by
// token-offset containment against the guarding `if`'s Body, so an advance
// nested arbitrarily deep inside that Body (behind its own nested `if`, its
// own `return`) still counts, while an advance reached from anywhere else —
// bare, or behind an unrelated condition — does not.
//
// SCOPE. One package, by source text, no type information, exactly like
// lint-weaver-classify-by-shape: a call's receiver is read by its trailing
// selector name only (calleeName), so a method value or a closure that
// captures the call indirectly is invisible to this gate, as it is to that
// one — the real seams this gate exists for call the function directly, not
// through either indirection.
//
// A SELF-TEST RUNS ON EVERY INVOCATION (synthetic sources through the three
// checkers; verbose with --selftest, silent-unless-failing otherwise; exit 2
// on a mismatch), and the corpus run refuses its all-clear if it found zero
// calls to fireEpisode OR zero calls to advanceReleasedLeg outside its own
// declaration — a gate that examined nothing proves nothing.
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
	weaverDir = "internal/weaver"

	fireEpisodeFunc         = "fireEpisode"
	advanceReleasedLegFunc  = "advanceReleasedLeg"
	planGapFunc             = "planGap"
	escalateGapFunc         = "escalateGap"
	boolColumnFunc          = "boolColumn"
	gapSuppressedWithCntFn  = "gapSuppressedWithCount"
	releaseCompletedLegFunc = "releaseCompletedLeg"
	releaseAdvancedPropFunc = "releaseAdvancedProposalLeg"

	violatingLiteral = `"violating"`
)

// declaredSeams is Rule 1's allowlist: the only functions permitted to call
// fireEpisode, each with a one-line note on what it is. A new dispatch seam
// cannot appear silently — it fails this gate until it is added here, which is
// the point where its guards get reviewed against Rules 2 and 3.
var declaredSeams = map[string]string{
	"dispatchGap":          "lane 1 — the CDC row handler's per-gap dispatch",
	advanceReleasedLegFunc: "the released-leg advance, gated on its own violating/suppression checks (Rule 2)",
	escalateGapFunc:        "the escalation episode shared by every door into the Augur reasoning tier",
	"sweepCount":           "the sweep's operator re-arm (arm (n)) of a markless, zero-count gap",
}

type stats struct {
	files          int
	fireEpisode    int // Rule 1: fireEpisode calls outside its own declaration
	advanceOutside int // Rule 3: advanceReleasedLeg calls outside its own declaration
}

// parsedFile pairs one corpus file's path with its parsed AST, threaded
// between main and findUniqueFuncDecl.
type parsedFile struct {
	path string
	file *ast.File
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

	entries, err := os.ReadDir(weaverDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint-weaver-retire-above-guard: cannot read %s: %v\n", weaverDir, err)
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
	var files []parsedFile
	for _, n := range names {
		path := filepath.Join(weaverDir, n)
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "lint-weaver-retire-above-guard: parse %s: %v\n", path, perr)
			os.Exit(2)
		}
		files = append(files, parsedFile{path, file})
	}

	// Rule 2 is asked of ONE function, found by name in the corpus. A missing or
	// duplicated declaration leaves the gate unable to say whose body it is
	// checking, so it refuses rather than guessing which advanceReleasedLeg it
	// means.
	advanceDecl, aPath, aerr := findUniqueFuncDecl(files, advanceReleasedLegFunc)
	if aerr != nil {
		fmt.Fprintf(os.Stderr, "lint-weaver-retire-above-guard: cannot locate %s's declaration: %v — refusing the run\n",
			advanceReleasedLegFunc, aerr)
		os.Exit(2)
	}

	var findings []string
	var st stats

	for _, pf := range files {
		st.files++
		findings = append(findings, checkFireEpisodeSeams(fset, pf.path, pf.file, &st)...)
		findings = append(findings, checkAdvanceGuardedByRelease(fset, pf.path, pf.file, &st)...)
	}
	findings = append(findings, checkAdvanceGates(fset, aPath, advanceDecl)...)

	if st.fireEpisode == 0 {
		fmt.Fprintf(os.Stderr, "lint-weaver-retire-above-guard: found no %s call in %s — the gate examined nothing, refusing the all-clear\n",
			fireEpisodeFunc, weaverDir)
		os.Exit(2)
	}
	if st.advanceOutside == 0 {
		fmt.Fprintf(os.Stderr, "lint-weaver-retire-above-guard: found no %s call (outside its own declaration) in %s — the gate examined nothing, refusing the all-clear\n",
			advanceReleasedLegFunc, weaverDir)
		os.Exit(2)
	}

	if len(findings) == 0 {
		fmt.Printf("lint-weaver-retire-above-guard: clean — %d file(s), %d %s call(s) from a declared seam, "+
			"%s's own gates ordered above its first act, %d %s call(s) reached only from a release arm\n",
			st.files, st.fireEpisode, fireEpisodeFunc, advanceReleasedLegFunc, st.advanceOutside, advanceReleasedLegFunc)
		return
	}
	for _, f := range findings {
		fmt.Println(f)
	}
	fmt.Printf("lint-weaver-retire-above-guard: %d finding(s) — a release is a retire and belongs above the gates, "+
		"an advance is an act and belongs below them (docs/components/weaver.md § Review keeps catching)\n", len(findings))
	if strict {
		os.Exit(1)
	}
}

// calleeName reads the name at the end of a call's Fun expression, whether the
// call is a bare function (*ast.Ident) or a method on a receiver
// (*ast.SelectorExpr) — every function this gate names is called as
// `e.<name>(...)` or `s.engine.<name>(...)`, but the classification does not
// depend on the receiver, matching this file's no-type-information scope.
func calleeName(fun ast.Expr) (string, bool) {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name, true
	case *ast.SelectorExpr:
		return f.Sel.Name, true
	}
	return "", false
}

// findUniqueFuncDecl finds the single top-level declaration of a method or
// function named name among files, returning its FuncDecl and the path it
// lives in. Zero or more than one is an error: a rule that names one
// function's own body must know which body that is rather than guess.
func findUniqueFuncDecl(files []parsedFile, name string) (*ast.FuncDecl, string, error) {
	var decl *ast.FuncDecl
	var path string
	for _, pf := range files {
		for _, d := range pf.file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name == nil || fd.Name.Name != name {
				continue
			}
			if decl != nil {
				return nil, "", fmt.Errorf("%s is declared more than once in the corpus (%s and %s)", name, path, pf.path)
			}
			decl, path = fd, pf.path
		}
	}
	if decl == nil {
		return nil, "", fmt.Errorf("no declaration of %s found in the corpus", name)
	}
	return decl, path, nil
}

// checkFireEpisodeSeams is Rule 1: every call to fireEpisode, outside its own
// declaration, must be lexically inside a top-level function named in
// declaredSeams. It walks each top-level FuncDecl's body flat (a nested
// closure's call is attributed to the enclosing named function, per SCOPE),
// so a caller reached only through a closure still names the function that
// captured it.
func checkFireEpisodeSeams(fset *token.FileSet, path string, file *ast.File, st *stats) []string {
	var findings []string
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil || fd.Name == nil {
			continue
		}
		if fd.Name.Name == fireEpisodeFunc {
			// Its own declaration is not a call site.
			continue
		}
		funcName := fd.Name.Name
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, ok := calleeName(call.Fun)
			if !ok || name != fireEpisodeFunc {
				return true
			}
			st.fireEpisode++
			if _, declared := declaredSeams[funcName]; !declared {
				pos := fset.Position(call.Pos())
				findings = append(findings, fmt.Sprintf(
					"%s:%d: %s is called from %s, which is not a declared dispatch seam — add it to declaredSeams "+
						"only after its guards are reviewed against Rules 2 and 3, or route the dispatch through an existing seam",
					path, pos.Line, fireEpisodeFunc, funcName))
			}
			return true
		})
	}
	return findings
}

// firstCallPos returns the earliest position, among body, of a call to any of
// names — the "first act" Rule 2 orders both gates above. absent reports
// whether no such call exists; when absent, pos is token.NoPos and any gate
// found anywhere in the body is, vacuously, ordered before it (there being no
// act for it to come after).
func firstCallPos(body *ast.BlockStmt, names ...string) (pos token.Pos, found bool) {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name, ok := calleeName(call.Fun)
		if !ok || !want[name] {
			return true
		}
		if !found || call.Pos() < pos {
			pos, found = call.Pos(), true
		}
		return true
	})
	return pos, found
}

// ifNodesToSearch returns the nodes of an `if` statement a condition test
// should scan: both its Init and its Cond. A guard's call commonly sits in
// the Init of an `if …; cond {` — `if suppressed, _, _ :=
// e.gapSuppressedWithCount(...); suppressed { return }` is exactly this
// corpus's own shape — so a test that reads only Cond would find the bare
// boolean `suppressed` and never the call that produced it.
func ifNodesToSearch(ifs *ast.IfStmt) []ast.Node {
	var nodes []ast.Node
	if ifs.Init != nil {
		nodes = append(nodes, ifs.Init)
	}
	if ifs.Cond != nil {
		nodes = append(nodes, ifs.Cond)
	}
	return nodes
}

// ifCallsAny reports whether ifs's Init or Cond contains, anywhere within it,
// a call to any of names.
func ifCallsAny(ifs *ast.IfStmt, names ...string) bool {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	found := false
	for _, node := range ifNodesToSearch(ifs) {
		ast.Inspect(node, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if name, ok := calleeName(call.Fun); ok && want[name] {
				found = true
			}
			return true
		})
	}
	return found
}

// ifCallsWithStringArg reports whether ifs's Init or Cond contains a call to
// fn carrying literal (a quoted string, e.g. `"violating"`) as one of its
// arguments.
func ifCallsWithStringArg(ifs *ast.IfStmt, fn, literal string) bool {
	found := false
	for _, node := range ifNodesToSearch(ifs) {
		ast.Inspect(node, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, ok := calleeName(call.Fun)
			if !ok || name != fn {
				return true
			}
			for _, arg := range call.Args {
				if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING && lit.Value == literal {
					found = true
				}
			}
			return true
		})
	}
	return found
}

// ifBodyReturns reports whether body's own statement list contains a return —
// the shape every real guard in this corpus takes ("log; return"). Checked
// directly on the block's statements, not recursively, since a guard buried
// inside a further nested conditional inside the gate's own body is not the
// unconditional withhold Rule 2 requires.
func ifBodyReturns(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	for _, stmt := range body.List {
		if _, ok := stmt.(*ast.ReturnStmt); ok {
			return true
		}
	}
	return false
}

// firstGuardIfPos finds the earliest `if` in body that satisfies matches
// (tested against the whole `if`, Init and Cond both) and whose body
// unconditionally returns (ifBodyReturns), and reports whether any such `if`
// exists at all.
func firstGuardIfPos(body *ast.BlockStmt, matches func(ifs *ast.IfStmt) bool) (pos token.Pos, found bool) {
	ast.Inspect(body, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		if matches(ifs) && ifBodyReturns(ifs.Body) {
			if !found || ifs.Pos() < pos {
				pos, found = ifs.Pos(), true
			}
		}
		return true
	})
	return pos, found
}

// checkAdvanceGates is Rule 2, asked of exactly one function's body — the one
// findUniqueFuncDecl located as advanceReleasedLeg. Both gates must exist and
// both must sit above the first act; each failure is reported as its own
// finding so a run that is missing one gate and has the other misplaced does
// not hide either behind the other.
func checkAdvanceGates(fset *token.FileSet, path string, fd *ast.FuncDecl) []string {
	if fd.Body == nil {
		return nil
	}
	var findings []string
	actPos, actFound := firstCallPos(fd.Body, planGapFunc, escalateGapFunc, fireEpisodeFunc)
	declPos := fset.Position(fd.Pos())

	violatingPos, violatingFound := firstGuardIfPos(fd.Body, func(ifs *ast.IfStmt) bool {
		return ifCallsWithStringArg(ifs, boolColumnFunc, violatingLiteral)
	})
	suppressPos, suppressFound := firstGuardIfPos(fd.Body, func(ifs *ast.IfStmt) bool {
		return ifCallsAny(ifs, gapSuppressedWithCntFn)
	})

	check := func(label, describe string, gatePos token.Pos, gateFound bool) {
		switch {
		case !gateFound:
			findings = append(findings, fmt.Sprintf(
				"%s:%d: %s has no `if %s { ... return }` gate — %s the advance carries its own \"cannot act\" gates",
				path, declPos.Line, advanceReleasedLegFunc, label, describe))
		case actFound && gatePos >= actPos:
			gLine := fset.Position(gatePos).Line
			aLine := fset.Position(actPos).Line
			findings = append(findings, fmt.Sprintf(
				"%s:%d: %s's %s gate (line %d) sits at or after its first act (line %d) — a retire ordered "+
					"below its own guard proves nothing; the gate must sit ABOVE every act it withholds",
				path, gLine, advanceReleasedLegFunc, label, gLine, aLine))
		}
	}
	check("violating", fmt.Sprintf("(a call to %s with %s) whose body returns —", boolColumnFunc, violatingLiteral),
		violatingPos, violatingFound)
	check("suppression", fmt.Sprintf("(a call to %s) whose body returns —", gapSuppressedWithCntFn),
		suppressPos, suppressFound)
	return findings
}

// checkAdvanceGuardedByRelease is Rule 3: every call to advanceReleasedLeg
// outside its own declaration must sit lexically inside an `if` whose
// condition calls releaseCompletedLeg or releaseAdvancedProposalLeg. Lexical
// containment is checked by comparing the call's position against the
// guarding `if`'s Body range — [Body.Pos(), Body.End()) — so a call nested
// arbitrarily deep inside that Body (behind its own `if`, its own `:=`
// init) still counts, matching every one of the corpus's four call sites,
// none of which calls advanceReleasedLeg as the direct statement of the
// guarding `if`'s own Body.
func checkAdvanceGuardedByRelease(fset *token.FileSet, path string, file *ast.File, st *stats) []string {
	var findings []string
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil || fd.Name == nil {
			continue
		}
		if fd.Name.Name == advanceReleasedLegFunc {
			// Its own declaration is not a call site.
			continue
		}
		guardIfs := collectReleaseGuardIfs(fd.Body)
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, ok := calleeName(call.Fun)
			if !ok || name != advanceReleasedLegFunc {
				return true
			}
			st.advanceOutside++
			if !withinAnyBody(call.Pos(), guardIfs) {
				pos := fset.Position(call.Pos())
				findings = append(findings, fmt.Sprintf(
					"%s:%d: %s is called with no enclosing `if %s(...)` / `if %s(...)` — an advance is an ACT "+
						"and is reachable only from the release `if` that retired the boundary it advances",
					path, pos.Line, advanceReleasedLegFunc, releaseCompletedLegFunc, releaseAdvancedPropFunc))
			}
			return true
		})
	}
	return findings
}

// collectReleaseGuardIfs returns every `if` in body whose condition calls
// releaseCompletedLeg or releaseAdvancedProposalLeg.
func collectReleaseGuardIfs(body *ast.BlockStmt) []*ast.IfStmt {
	var out []*ast.IfStmt
	ast.Inspect(body, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		if ifCallsAny(ifs, releaseCompletedLegFunc, releaseAdvancedPropFunc) {
			out = append(out, ifs)
		}
		return true
	})
	return out
}

// withinAnyBody reports whether pos lies within any of ifs' Body ranges.
func withinAnyBody(pos token.Pos, ifs []*ast.IfStmt) bool {
	for _, ifs := range ifs {
		if ifs.Body == nil {
			continue
		}
		if pos >= ifs.Body.Pos() && pos < ifs.Body.End() {
			return true
		}
	}
	return false
}

// runSelfTest proves all three checkers on synthetic sources before the
// corpus is ever read, so the self-test means something even while
// internal/weaver is mid-edit.
func runSelfTest(verbose bool) {
	failed := false
	if runRule1SelfTest(verbose) {
		failed = true
	}
	if runRule2SelfTest(verbose) {
		failed = true
	}
	if runRule3SelfTest(verbose) {
		failed = true
	}
	if failed {
		os.Exit(2)
	}
}

func parseSelfTestSrc(name, src string) *ast.File {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name+".go", src, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint-weaver-retire-above-guard: self-test %s does not parse: %v\n", name, err)
		os.Exit(2)
	}
	return file
}

func runRule1SelfTest(verbose bool) (failed bool) {
	cases := []struct {
		name string
		src  string
		want int
	}{
		{"rule1-clean-declared-seams", `package weaver
func (e *Engine) dispatchGap() { e.fireEpisode() }
func (e *Engine) advanceReleasedLeg() { e.fireEpisode() }
func (e *Engine) escalateGap() { e.fireEpisode() }
func (s *sweeper) sweepCount() { s.engine.fireEpisode() }
func (e *Engine) fireEpisode() {}`, 0},
		{"rule1-undeclared-caller", `package weaver
func (e *Engine) reclaim() { e.fireEpisode() }
func (e *Engine) fireEpisode() {}`, 1},
		{"rule1-undeclared-caller-nested-closure", `package weaver
func (e *Engine) reclaim() {
	g := func() { e.fireEpisode() }
	g()
}
func (e *Engine) fireEpisode() {}`, 1},
	}
	fset := token.NewFileSet()
	for _, c := range cases {
		file, err := parser.ParseFile(fset, c.name+".go", c.src, 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "lint-weaver-retire-above-guard: self-test %s does not parse: %v\n", c.name, err)
			os.Exit(2)
		}
		var st stats
		got := len(checkFireEpisodeSeams(fset, c.name+".go", file, &st))
		if got != c.want {
			failed = true
			fmt.Fprintf(os.Stderr, "lint-weaver-retire-above-guard: self-test %s: %d finding(s), want %d\n", c.name, got, c.want)
		} else if verbose {
			fmt.Printf("self-test %s: ok (%d finding(s))\n", c.name, got)
		}
	}
	return failed
}

func runRule2SelfTest(verbose bool) (failed bool) {
	cases := []struct {
		name string
		src  string
		want int
	}{
		{"rule2-clean", `package weaver
func (e *Engine) advanceReleasedLeg() int {
	if !e.boolColumn(t, id, row, "violating") {
		return 0
	}
	if suppressed, _, _ := e.gapSuppressedWithCount(t, id, row, col, act, 0); suppressed {
		return 0
	}
	pl, ref, esc, escalate, dec := e.planGap()
	if escalate {
		return e.escalateGap()
	}
	e.fireEpisode()
	return dec
}`, 0},
		{"rule2-missing-violating-gate", `package weaver
func (e *Engine) advanceReleasedLeg() int {
	if suppressed, _, _ := e.gapSuppressedWithCount(t, id, row, col, act, 0); suppressed {
		return 0
	}
	e.planGap()
	e.fireEpisode()
	return 0
}`, 1},
		{"rule2-missing-suppression-gate", `package weaver
func (e *Engine) advanceReleasedLeg() int {
	if !e.boolColumn(t, id, row, "violating") {
		return 0
	}
	e.planGap()
	e.fireEpisode()
	return 0
}`, 1},
		{"rule2-violating-gate-below-act", `package weaver
func (e *Engine) advanceReleasedLeg() int {
	if suppressed, _, _ := e.gapSuppressedWithCount(t, id, row, col, act, 0); suppressed {
		return 0
	}
	e.planGap()
	if !e.boolColumn(t, id, row, "violating") {
		return 0
	}
	e.fireEpisode()
	return 0
}`, 1},
		{"rule2-suppression-gate-below-act", `package weaver
func (e *Engine) advanceReleasedLeg() int {
	if !e.boolColumn(t, id, row, "violating") {
		return 0
	}
	e.planGap()
	if suppressed, _, _ := e.gapSuppressedWithCount(t, id, row, col, act, 0); suppressed {
		return 0
	}
	e.fireEpisode()
	return 0
}`, 1},
		{"rule2-both-gates-missing", `package weaver
func (e *Engine) advanceReleasedLeg() int {
	e.planGap()
	e.fireEpisode()
	return 0
}`, 2},
		{"rule2-gate-present-but-no-return", `package weaver
func (e *Engine) advanceReleasedLeg() int {
	if !e.boolColumn(t, id, row, "violating") {
		e.logger.Debug("withheld")
	}
	if suppressed, _, _ := e.gapSuppressedWithCount(t, id, row, col, act, 0); suppressed {
		e.logger.Debug("withheld")
	}
	e.planGap()
	e.fireEpisode()
	return 0
}`, 2},
	}
	for _, c := range cases {
		file := parseSelfTestSrc(c.name, c.src)
		var fd *ast.FuncDecl
		for _, d := range file.Decls {
			if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == advanceReleasedLegFunc {
				fd = f
			}
		}
		if fd == nil {
			fmt.Fprintf(os.Stderr, "lint-weaver-retire-above-guard: self-test %s declares no %s\n", c.name, advanceReleasedLegFunc)
			os.Exit(2)
		}
		fset := token.NewFileSet()
		got := len(checkAdvanceGates(fset, c.name+".go", fd))
		if got != c.want {
			failed = true
			fmt.Fprintf(os.Stderr, "lint-weaver-retire-above-guard: self-test %s: %d finding(s), want %d\n", c.name, got, c.want)
		} else if verbose {
			fmt.Printf("self-test %s: ok (%d finding(s))\n", c.name, got)
		}
	}
	return failed
}

func runRule3SelfTest(verbose bool) (failed bool) {
	cases := []struct {
		name string
		src  string
		want int
	}{
		{"rule3-clean-releaseCompletedLeg", `package weaver
func (e *Engine) reclaim() {
	if e.releaseCompletedLeg() {
		e.advanceReleasedLeg()
	}
}`, 0},
		{"rule3-clean-releaseAdvancedProposalLeg", `package weaver
func (e *Engine) reclaim() {
	if e.releaseAdvancedProposalLeg() {
		if fired := e.advanceReleasedLeg(); fired != 0 {
			return
		}
	}
}`, 0},
		{"rule3-clean-nested-if-with-init", `package weaver
func (e *Engine) escalateExhaustedGap() {
	if markReadable && e.releaseCompletedLeg() {
		return e.advanceReleasedLeg()
	}
}`, 0},
		{"rule3-unguarded-statement-level", `package weaver
func (e *Engine) reclaim() {
	e.advanceReleasedLeg()
}`, 1},
		{"rule3-guarded-by-unrelated-condition", `package weaver
func (e *Engine) reclaim() {
	if e.gapSuppressedWithCount() {
		e.advanceReleasedLeg()
	}
}`, 1},
		{"rule3-own-declaration-ignored", `package weaver
func (e *Engine) advanceReleasedLeg() {
	e.advanceReleasedLeg()
}`, 0},
	}
	for _, c := range cases {
		file := parseSelfTestSrc(c.name, c.src)
		fset := token.NewFileSet()
		var st stats
		got := len(checkAdvanceGuardedByRelease(fset, c.name+".go", file, &st))
		if got != c.want {
			failed = true
			fmt.Fprintf(os.Stderr, "lint-weaver-retire-above-guard: self-test %s: %d finding(s), want %d\n", c.name, got, c.want)
		} else if verbose {
			fmt.Printf("self-test %s: ok (%d finding(s))\n", c.name, got)
		}
	}
	return failed
}
