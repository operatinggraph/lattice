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
// The same hazard reaches staleMark's own classifier, externalDispatchGap: a
// goal gap's playbook entry names no Action (that is precisely what marks it
// as goal-mode, not what it resolves to dispatch), so classifying the entry
// itself reads every goal leg as "never makes an external call" regardless of
// what it actually dispatches. staleMark takes the RESOLVED leg for exactly
// this reason — a caller hands over what resolvePlannedAction / resolvedLegAction
// returned for the pinned or fresh leg, or the zero GapAction when no leg
// resolves — and externalDispatchGap classifies that, never a playbook entry
// or a mark's recorded string.
//
// The class was found twice: staleMark's external-vs-userTask split by action
// name (lease-signing's bgcheck/payment gaps never retried after a timeout), and
// the reclaim's collapseOnlyReclaim(rec.Action, …) over a goal leg. Twice-seen
// gets a gate.
//
// THE RULE has two parts, one per classifier.
//
// Rule 1 (collapseOnlyReclaim). Every call's first argument is a plain
// identifier — a local holding the RESOLVED dispatch action (resolvedAction,
// dispatchAction: what resolvePlannedAction / resolvedLegAction returned for the
// pinned or fresh leg). A selector (`rec.Action`, `ga.Action`, `esc.Action`), a
// call, or a literal is a finding: it is a recorded name, or a value the gate
// cannot see was resolved. A static gap resolves to its own action, so the rule
// costs it nothing.
//
// Rule 2 (staleMark's GapAction argument). Every call to staleMark passes, in
// the argument position staleMark's OWN DECLARATION gives its GapAction
// parameter — never assumed to be the last argument, since that assumption
// breaks silently the day another parameter is threaded through the call — a
// plain identifier whose EVERY assignment in the enclosing function is one
// of: (a) a call to resolvePlannedAction or resolvedLegAction (the
// identifier on the LHS of that call's result list); (b) the zero value of
// GapAction — the composite literal GapAction{} on an assignment, or a
// `var leg GapAction` declaration with no initializer; (c) an identifier
// that itself satisfies (a) — one hop, so `leg = resolved` inside an
// `if resolved, _, perr := e.resolvePlannedAction(…); perr == nil { … }` passes.
// A selector (`target.Gaps[col]`, `rec.Action`), a function parameter, a map
// index, or an identifier bound any other way (`leg := ga`, `var leg = ga`)
// is a finding — the same hazard as Rule 1, applied to the classifier
// staleMark itself resolves its verdict through. Locating that argument
// position requires staleMark's own declaration: if it is missing from the
// corpus, or carries zero or more than one GapAction-typed parameter, the
// gate cannot say which argument is the one Rule 2 is about and refuses to
// run rather than guess — inspecting the wrong argument would silently pass
// through exactly the shape this rule exists to catch.
//
// Rule 2b (externalDispatchGap's only caller). externalDispatchGap is called
// from exactly one site: inside staleMark, on staleMark's own GapAction
// parameter. Any other call is a finding — the classifier is not a public
// predicate; a second caller would have to re-derive the resolution discipline
// Rule 2 exists to enforce on staleMark's own call site.
//
// SCOPE. One package, by source text, no type information: both predicates are
// unexported functions of one engine, and Rule 2's binding analysis is a flat,
// per-function scan (it does not model closures capturing outer-scope
// variables, or reassignment of a captured binding from inside a nested
// literal) — the real call sites this gate exists for bind their leg directly
// in the function that calls staleMark, not through a closure. Two further
// shapes sit outside the scan by the same construction: a field mutation
// after resolution (`leg.Action = ga.Action`) has a selector on its
// left-hand side, which collectBindings never records, so it is invisible to
// Rule 2; and a method value (`f := e.staleMark; f(…, ga)`) matches neither
// the selector the call walk looks for nor the call counter, so it is both
// unchecked and uncounted. Neither shape appears at any of today's four call
// sites.
//
// A SELF-TEST RUNS ON EVERY INVOCATION (synthetic sources through the two
// checkers; verbose with --selftest, silent-unless-failing otherwise; exit 2 on
// a mismatch), and the corpus run refuses its all-clear if it found zero calls
// to EITHER predicate — a gate that examined nothing proves nothing.
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

	staleMarkFunc        = "staleMark"
	externalDispatchFunc = "externalDispatchGap"
	resolvePlannedFunc   = "resolvePlannedAction"
	resolvedLegFunc      = "resolvedLegAction"
	gapActionType        = "GapAction"
)

type stats struct {
	files          int
	calls          int // collapseOnlyReclaim (Rule 1)
	staleMarkCalls int // staleMark (Rule 2)
	externalCalls  int // externalDispatchGap (Rule 2b)
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
	type parsed struct {
		path string
		file *ast.File
	}
	var files []parsed
	var asts []*ast.File
	for _, n := range names {
		path := filepath.Join(weaverDir, n)
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "lint-weaver-classify-by-shape: parse %s: %v\n", path, perr)
			os.Exit(2)
		}
		files = append(files, parsed{path, file})
		asts = append(asts, file)
	}

	// Rule 2's argument position is derived from staleMark's own declaration,
	// found in this same corpus, before a single call site is inspected — a
	// gate that cannot locate what it is checking must refuse the run rather
	// than fall back to a guessed position (see staleMarkGapArgIndex).
	gapIdx, gerr := staleMarkGapArgIndex(asts)
	if gerr != nil {
		fmt.Fprintf(os.Stderr, "lint-weaver-classify-by-shape: cannot locate %s's GapAction argument: %v — refusing the run\n", staleMarkFunc, gerr)
		os.Exit(2)
	}

	for _, pf := range files {
		st.files++
		findings = append(findings, checkFile(fset, pf.path, pf.file, &st)...)
		findings = append(findings, checkFileLegShape(fset, pf.path, pf.file, &st, gapIdx)...)
	}

	if st.calls == 0 {
		fmt.Fprintf(os.Stderr, "lint-weaver-classify-by-shape: found no %s call in %s — the gate examined nothing, refusing the all-clear\n",
			predicateFunc, weaverDir)
		os.Exit(2)
	}
	if st.staleMarkCalls == 0 {
		fmt.Fprintf(os.Stderr, "lint-weaver-classify-by-shape: found no %s call in %s — the gate examined nothing, refusing the all-clear\n",
			staleMarkFunc, weaverDir)
		os.Exit(2)
	}
	if st.externalCalls == 0 {
		fmt.Fprintf(os.Stderr, "lint-weaver-classify-by-shape: found no %s call in %s — the gate examined nothing, refusing the all-clear\n",
			externalDispatchFunc, weaverDir)
		os.Exit(2)
	}
	if len(findings) == 0 {
		fmt.Printf("lint-weaver-classify-by-shape: clean — %d file(s), %d %s call(s) classifying on a resolved dispatch action, "+
			"%d %s call(s) passing a resolved leg, %d %s call(s) dispatching only from staleMark's own leg\n",
			st.files, st.calls, predicateFunc, st.staleMarkCalls, staleMarkFunc, st.externalCalls, externalDispatchFunc)
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

// checkFile reports every predicateFunc (collapseOnlyReclaim) call whose first
// argument is not a plain identifier. It walks the whole file, so a call inside
// a closure or a nested block is examined exactly like a top-level one.
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

// bindKind classifies how one local identifier came to hold the value it holds
// at one assignment site, for Rule 2's per-binding check.
type bindKind int

const (
	bindOther    bindKind = iota // anything not below: a selector, an index, an unrelated call, a literal
	bindResolved                 // (a): a call to resolvePlannedAction or resolvedLegAction
	bindZero                     // (b): the zero composite literal GapAction{}
	bindRef                      // (c) candidate: an identifier reference to another local — checked one hop
)

type binding struct {
	kind bindKind
	ref  string // for bindRef: the referenced identifier's name
}

// calleeName reads the name at the end of a call's Fun expression, whether the
// call is a bare function (*ast.Ident) or a method on a receiver
// (*ast.SelectorExpr) — the two callers Rule 2 recognizes, resolvePlannedAction
// and resolvedLegAction, are methods, but the classification does not depend
// on that, matching this file's no-type-information scope.
func calleeName(fun ast.Expr) (string, bool) {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name, true
	case *ast.SelectorExpr:
		return f.Sel.Name, true
	}
	return "", false
}

// classifyRHS reads one assignment's right-hand side into a binding, used both
// for a single-target assignment (`leg = resolved`) and, once per target, for a
// multi-target assignment whose single right-hand side is a call
// (`leg, _, perr := e.resolvedLegAction(…)`).
func classifyRHS(rhs ast.Expr) binding {
	switch v := rhs.(type) {
	case *ast.CompositeLit:
		if id, ok := v.Type.(*ast.Ident); ok && id.Name == gapActionType && len(v.Elts) == 0 {
			return binding{kind: bindZero}
		}
	case *ast.Ident:
		if v.Name != "nil" {
			return binding{kind: bindRef, ref: v.Name}
		}
	case *ast.CallExpr:
		if name, ok := calleeName(v.Fun); ok && (name == resolvePlannedFunc || name == resolvedLegFunc) {
			return binding{kind: bindResolved}
		}
	}
	return binding{kind: bindOther}
}

// funcParamNames collects the names a value could arrive by as a parameter —
// receiver included, since `func (e *Engine) f(...)` binds e the same way a
// parameter binds — for Rule 2's "a function parameter is a finding" clause.
func funcParamNames(fd *ast.FuncDecl) map[string]bool {
	names := map[string]bool{}
	add := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, f := range fl.List {
			for _, n := range f.Names {
				if n.Name != "_" {
					names[n.Name] = true
				}
			}
		}
	}
	add(fd.Recv)
	if fd.Type != nil {
		add(fd.Type.Params)
	}
	return names
}

// collectBindings walks fd's body flat (nested closures included, per SCOPE)
// and records every assignment of every local identifier, so Rule 2 can ask
// "is EVERY assignment of this name one of (a)/(b)/(c)". A `var leg GapAction`
// declaration with no initializer IS clause (b)'s zero value exactly as much
// as `leg = GapAction{}` is — GapAction's zero value doesn't care which
// syntax produced it — so a *ast.DeclStmt's var specs are walked alongside
// *ast.AssignStmt, not left to fall through as an unrecorded binding.
func collectBindings(body *ast.BlockStmt) map[string][]binding {
	bindings := map[string][]binding{}
	if body == nil {
		return bindings
	}
	record := func(id *ast.Ident, b binding) {
		if id == nil || id.Name == "_" {
			return
		}
		bindings[id.Name] = append(bindings[id.Name], b)
	}
	recordPairs := func(names []*ast.Ident, values []ast.Expr) {
		if len(values) == 1 && len(names) > 1 {
			b := classifyRHS(values[0])
			for _, id := range names {
				record(id, b)
			}
			return
		}
		if len(names) == len(values) {
			for i, id := range names {
				record(id, classifyRHS(values[i]))
			}
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			lhs := make([]*ast.Ident, len(s.Lhs))
			for i, e := range s.Lhs {
				lhs[i], _ = e.(*ast.Ident)
			}
			recordPairs(lhs, s.Rhs)
		case *ast.DeclStmt:
			gd, ok := s.Decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				return true
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if len(vs.Values) == 0 {
					if id, ok := vs.Type.(*ast.Ident); ok && id.Name == gapActionType {
						for _, name := range vs.Names {
							record(name, binding{kind: bindZero})
						}
					}
					continue
				}
				recordPairs(vs.Names, vs.Values)
			}
		}
		return true
	})
	return bindings
}

// resolvedOneHop answers (c)'s own requirement on the identifier it points at:
// every assignment of name is itself (a) — a call to resolvePlannedAction or
// resolvedLegAction. Exactly one hop: it does not recurse into a further
// bindRef, and a parameter fails it outright, the same as at the top level.
func resolvedOneHop(name string, params map[string]bool, bindings map[string][]binding) bool {
	if params[name] {
		return false
	}
	bs, ok := bindings[name]
	if !ok || len(bs) == 0 {
		return false
	}
	for _, b := range bs {
		if b.kind != bindResolved {
			return false
		}
	}
	return true
}

// satisfiesRule2 answers Rule 2 for one identifier used as staleMark's
// GapAction argument: every assignment of it, in the enclosing function, is
// (a), (b), or one-hop (c); being a parameter fails it regardless of any later
// reassignment, per THE RULE's explicit "a function parameter … is a finding".
func satisfiesRule2(name string, params map[string]bool, bindings map[string][]binding) bool {
	if params[name] {
		return false
	}
	bs, ok := bindings[name]
	if !ok || len(bs) == 0 {
		return false
	}
	for _, b := range bs {
		switch b.kind {
		case bindResolved, bindZero:
			continue
		case bindRef:
			if !resolvedOneHop(b.ref, params, bindings) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// staleMarkGapArgIndex finds staleMark's own declaration among the given
// files and returns the position, among a CALL's arguments (the receiver is
// not one — a method call passes it via the selector, not an Args entry), of
// the parameter typed GapAction. Rule 2 exists to hold that one argument to
// a resolved leg; inspecting a fixed position such as "the last argument"
// instead of asking the declaration works only until a future signature
// change adds a further trailing parameter, at which point it silently
// inspects the wrong one and reports the all-clear regardless. A missing
// declaration, or one with zero or more than one GapAction-typed parameter,
// leaves the gate unable to say which argument it means, so it errors rather
// than falling back to a guess.
func staleMarkGapArgIndex(files []*ast.File) (int, error) {
	var decl *ast.FuncDecl
	for _, file := range files {
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name == nil || fd.Name.Name != staleMarkFunc || fd.Type == nil {
				continue
			}
			if decl != nil {
				return 0, fmt.Errorf("%s is declared more than once in the corpus", staleMarkFunc)
			}
			decl = fd
		}
	}
	if decl == nil {
		return 0, fmt.Errorf("no declaration of %s found in the corpus", staleMarkFunc)
	}
	idx, count, pos := -1, 0, 0
	if decl.Type.Params != nil {
		for _, field := range decl.Type.Params.List {
			n := len(field.Names)
			if n == 0 {
				n = 1 // an unnamed parameter still occupies one call-argument position
			}
			if id, ok := field.Type.(*ast.Ident); ok && id.Name == gapActionType {
				count += n
				if count == 1 {
					idx = pos
				}
			}
			pos += n
		}
	}
	if count != 1 {
		return 0, fmt.Errorf("%s's declaration has %d %s-typed parameter(s), want exactly 1", staleMarkFunc, count, gapActionType)
	}
	return idx, nil
}

// checkFileLegShape reports Rule 2 and Rule 2b findings, one FuncDecl at a
// time: staleMark's own classifier (externalDispatchGap over a GapAction) is
// covered, unlike collapseOnlyReclaim's single flat walk, because reading a
// binding's history requires knowing which function it lives in — the file-wide
// ast.Inspect checkFile uses does not carry that, so this is a second walk,
// scoped per function, rather than a case added to the first.
func checkFileLegShape(fset *token.FileSet, path string, file *ast.File, st *stats, gapIdx int) []string {
	var findings []string
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		findings = append(findings, checkFuncLegShape(fset, path, fd, st, gapIdx)...)
	}
	return findings
}

func checkFuncLegShape(fset *token.FileSet, path string, fd *ast.FuncDecl, st *stats, gapIdx int) []string {
	var findings []string
	params := funcParamNames(fd)
	bindings := collectBindings(fd.Body)
	funcName := ""
	if fd.Name != nil {
		funcName = fd.Name.Name
	}

	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pos := fset.Position(call.Pos())

		switch sel.Sel.Name {
		case staleMarkFunc:
			st.staleMarkCalls++
			if gapIdx >= len(call.Args) {
				findings = append(findings, fmt.Sprintf("%s:%d: %s call has %d argument(s), too few to reach its GapAction argument at position %d — the gate cannot check what isn't there",
					path, pos.Line, staleMarkFunc, len(call.Args), gapIdx))
				return true
			}
			arg := call.Args[gapIdx]
			id, ok := arg.(*ast.Ident)
			if !ok {
				findings = append(findings, fmt.Sprintf("%s:%d: %s's GapAction argument is not a plain identifier (%T) — the gate cannot see it was resolved",
					path, pos.Line, staleMarkFunc, arg))
				return true
			}
			if !satisfiesRule2(id.Name, params, bindings) {
				findings = append(findings, fmt.Sprintf("%s:%d: %s's GapAction argument %q is not, in every one of its assignments, a resolved leg (resolvePlannedAction / resolvedLegAction, or the zero GapAction{}) — the gate cannot see it was resolved",
					path, pos.Line, staleMarkFunc, id.Name))
			}

		case externalDispatchFunc:
			st.externalCalls++
			validSite := funcName == staleMarkFunc && len(call.Args) > 0
			if validSite {
				id, ok := call.Args[0].(*ast.Ident)
				validSite = ok && params[id.Name]
			}
			if !validSite {
				findings = append(findings, fmt.Sprintf("%s:%d: %s is called outside %s, or not on %s's own parameter — it is not a public predicate, and a second caller would have to re-derive the resolution discipline Rule 2 enforces on %s's call site",
					path, pos.Line, externalDispatchFunc, staleMarkFunc, staleMarkFunc, staleMarkFunc))
			}
		}
		return true
	})
	return findings
}

// legShapeGapIdx is the GapAction argument position the self-test's synthetic
// staleMark call sites use. Those sources declare no staleMark of their own —
// there is nothing for staleMarkGapArgIndex to find in a single synthetic call
// snippet — so each legshape case states the position its own
// `e.staleMark(t, e2, row, col, leg, …)` calls put GapAction at, rather than
// leaving it to be inferred the way the corpus run infers it.
//
// Stating it is what keeps those cases about the CHECKER, and is also the reason
// they say nothing about the derivation: a checker proved at a position it was
// handed proves nothing about the function that decides that position. The
// derivation carries its own vectors (gapArgIndexCases), over declarations rather
// than call sites, and they are the ones that fail if the counting or the refusal
// is wrong.
const legShapeGapIdx = 4

// legShapeCheck adapts checkFileLegShape's extra gapIdx parameter to the
// self-test table's uniform check signature.
func legShapeCheck(gapIdx int) func(fset *token.FileSet, path string, file *ast.File, st *stats) []string {
	return func(fset *token.FileSet, path string, file *ast.File, st *stats) []string {
		return checkFileLegShape(fset, path, file, st, gapIdx)
	}
}

// runSelfTest proves both walks on synthetic sources before the corpus is ever
// read, so the self-test means something even while internal/weaver is
// mid-edit.
func runSelfTest(verbose bool) {
	type selfTestCase struct {
		name  string
		src   string
		want  int
		check func(fset *token.FileSet, path string, file *ast.File, st *stats) []string
	}
	cases := []selfTestCase{
		{"resolved-local", `package weaver
func f(rec *mark) { dispatchAction := resolve(rec.Action); _ = collapseOnlyReclaim(dispatchAction, false) }`, 0, checkFile},
		{"mark-field", `package weaver
func f(rec *mark) { _ = collapseOnlyReclaim(rec.Action, false) }`, 1, checkFile},
		{"playbook-field", `package weaver
func f(ga GapAction) { if collapseOnlyReclaim(ga.Action, true) { return } }`, 1, checkFile},
		{"call-argument", `package weaver
func f(rec *mark) { _ = collapseOnlyReclaim(actionOf(rec), false) }`, 1, checkFile},
		{"nested-closure", `package weaver
func f(rec *mark) { g := func() bool { return collapseOnlyReclaim(rec.Action, false) }; _ = g }`, 1, checkFile},

		{"legshape-resolved-local", `package weaver
func f(e *Engine, t, e2 string, row map[string]any, col string) bool {
	leg, _, perr := e.resolvedLegAction(t, e2, row, col)
	_ = perr
	return e.staleMark(t, e2, row, col, leg)
}`, 0, legShapeCheck(legShapeGapIdx)},
		{"legshape-zero-then-resolved", `package weaver
func f(e *Engine, t, e2 string, row map[string]any, col string) bool {
	leg := GapAction{}
	if r, _, perr := e.resolvePlannedAction(t, e2, row, col); perr == nil {
		leg = r
	}
	return e.staleMark(t, e2, row, col, leg)
}`, 0, legShapeCheck(legShapeGapIdx)},
		{"legshape-resolved-local-named-ref", `package weaver
func f(e *Engine, t, e2 string, row map[string]any, col string) bool {
	leg, ref, perr := e.resolvedLegAction(t, e2, row, col)
	_ = ref
	_ = perr
	return e.staleMark(t, e2, row, col, leg)
}`, 0, legShapeCheck(legShapeGapIdx)},
		{"legshape-param", `package weaver
func f(e *Engine, t, e2 string, row map[string]any, col string, ga GapAction) bool {
	return e.staleMark(t, e2, row, col, ga)
}`, 1, legShapeCheck(legShapeGapIdx)},
		{"legshape-index-expr", `package weaver
func f(e *Engine, t, e2 string, row map[string]any, col string, target *Target) bool {
	return e.staleMark(t, e2, row, col, target.Gaps[col])
}`, 1, legShapeCheck(legShapeGapIdx)},
		{"legshape-param-aliased", `package weaver
func f(e *Engine, t, e2 string, row map[string]any, col string, ga GapAction) bool {
	leg := ga
	return e.staleMark(t, e2, row, col, leg)
}`, 1, legShapeCheck(legShapeGapIdx)},
		{"legshape-external-outside-stalemark", `package weaver
func f(e *Engine, ga GapAction, row map[string]any) {
	ext, _, _ := e.externalDispatchGap(ga, row)
	_ = ext
}`, 1, legShapeCheck(legShapeGapIdx)},
		{"legshape-var-zero", `package weaver
func f(e *Engine, t, e2 string, row map[string]any, col string) bool {
	var leg GapAction
	return e.staleMark(t, e2, row, col, leg)
}`, 0, legShapeCheck(legShapeGapIdx)},
		{"legshape-var-aliased", `package weaver
func f(e *Engine, t, e2 string, row map[string]any, col string, ga GapAction) bool {
	var leg = ga
	return e.staleMark(t, e2, row, col, leg)
}`, 1, legShapeCheck(legShapeGapIdx)},
		{"legshape-trailing-arg-hides-unresolved", `package weaver
func f(e *Engine, t, e2 string, row map[string]any, col string, ga GapAction) bool {
	other, _, perr := e.resolvedLegAction(t, e2, row, col)
	_ = perr
	return e.staleMark(t, e2, row, col, ga, other)
}`, 1, legShapeCheck(legShapeGapIdx)},
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
		got := len(c.check(fset, c.name+".go", file, &st))
		if got != c.want {
			failed = true
			fmt.Fprintf(os.Stderr, "lint-weaver-classify-by-shape: self-test %s: %d finding(s), want %d\n", c.name, got, c.want)
		} else if verbose {
			fmt.Printf("self-test %s: ok (%d finding(s))\n", c.name, got)
		}
	}
	if runGapArgIndexSelfTest(verbose) {
		failed = true
	}
	if failed {
		os.Exit(2)
	}
}

// runGapArgIndexSelfTest proves staleMarkGapArgIndex itself, over DECLARATIONS:
// the position it derives for the shape the corpus actually has, the two ways
// that position moves (a field's names each occupy one, so a grouped field is not
// one position; a GapAction parameter that is not the trailing one is not the
// last position either), and the three refusals — a declaration carrying two
// GapAction parameters, one carrying none, and a corpus that declares no
// staleMark at all. Each refusal is a case where a fallback guess would name
// SOME argument, and Rule 2 would then be enforced over whatever sits there,
// reporting the all-clear about an argument it never inspected.
//
// wantIdx -1 means the derivation must return an error. Reporting failures
// rather than exiting keeps one run's output complete when several break at
// once, like the checker table above.
func runGapArgIndexSelfTest(verbose bool) (failed bool) {
	cases := []struct {
		name    string
		src     string
		wantIdx int
	}{
		{"gapidx-corpus-shape", `package weaver
func (e *Engine) staleMark(targetID, entityID string, row map[string]any, col string, leg GapAction) bool {
	return false
}`, 4},
		{"gapidx-grouped-names-shift-it", `package weaver
func (e *Engine) staleMark(targetID, entityID, col string, leg GapAction, row map[string]any, extra string) bool {
	return false
}`, 3},
		{"gapidx-unnamed-parameters", `package weaver
func (e *Engine) staleMark(string, string, GapAction) bool {
	return false
}`, 2},
		{"gapidx-two-gapactions", `package weaver
func (e *Engine) staleMark(targetID string, leg GapAction, entry GapAction) bool {
	return false
}`, -1},
		{"gapidx-no-gapaction", `package weaver
func (e *Engine) staleMark(targetID, entityID, col string, row map[string]any) bool {
	return false
}`, -1},
		{"gapidx-no-declaration", `package weaver
func f(e *Engine, t, e2 string, row map[string]any, col string, leg GapAction) bool {
	return e.staleMark(t, e2, row, col, leg)
}`, -1},
		{"gapidx-declared-twice", `package weaver
func (e *Engine) staleMark(targetID string, leg GapAction) bool { return false }
func (e *Engine) staleMark(entityID string, leg GapAction) bool { return false }`, -1},
	}
	for _, c := range cases {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, c.name+".go", c.src, 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "lint-weaver-classify-by-shape: self-test %s does not parse: %v\n", c.name, err)
			os.Exit(2)
		}
		idx, ierr := staleMarkGapArgIndex([]*ast.File{file})
		switch {
		case c.wantIdx < 0 && ierr == nil:
			failed = true
			fmt.Fprintf(os.Stderr, "lint-weaver-classify-by-shape: self-test %s: derived argument position %d, want a refusal\n", c.name, idx)
		case c.wantIdx >= 0 && ierr != nil:
			failed = true
			fmt.Fprintf(os.Stderr, "lint-weaver-classify-by-shape: self-test %s: refused (%v), want argument position %d\n", c.name, ierr, c.wantIdx)
		case c.wantIdx >= 0 && idx != c.wantIdx:
			failed = true
			fmt.Fprintf(os.Stderr, "lint-weaver-classify-by-shape: self-test %s: derived argument position %d, want %d\n", c.name, idx, c.wantIdx)
		case verbose && c.wantIdx < 0:
			fmt.Printf("self-test %s: ok (refused: %v)\n", c.name, ierr)
		case verbose:
			fmt.Printf("self-test %s: ok (argument position %d)\n", c.name, idx)
		}
	}
	return failed
}
