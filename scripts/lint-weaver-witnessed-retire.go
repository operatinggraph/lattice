//go:build ignore

// lint-weaver-witnessed-retire — a `surface` open-row membership is retired only
// by a leg that READ the column and found it false.
//
// THE HAZARD. internal/weaver has two column readers, and they return two
// different verdicts about the same row. boolColumnRead reports the column's
// value AND whether the row actually stated it: an explicit bool speaks for
// itself, an absent column is §10.2's retraction shape and reads as a closed
// column, and a PRESENT value of any other type is neither — it is a lens data
// fault, surfaced as a RowDataError, and the false returned beside it is a
// conservative default rather than a fact about the row. boolColumn wraps that
// call and discards the second half, so to it a present non-bool IS a false.
//
// For most consumers the conservative false is exactly right: a gap that cannot
// be read is not dispatched, a mark nothing can read a live column for is
// cleared. For ONE consumer it is wrong. The `surface` gap's open-row set counts
// the target's open WORKLOAD — how many rows an operator still has to deal with
// — and shrinking that count is a claim that a row's work is DONE. A row whose
// column projects as the string "true" has not finished anything; retiring its
// membership drops the operator's backlog figure by one per broken row, deletes
// the entry's `since` when it was the last member, and puts the count straight
// back the moment the lens projects a bool again — a number flapping at the rate
// a broken projection re-runs, describing work that never stopped being open.
//
// This class was found four times in one item: lane-1's candidate walk, lane-1's
// non-violating leg, and both of the reconciler sweep's gap-close legs. It is
// not a bug about one call site; it is that two verdicts share one name-shaped
// idiom and every leg reaching a shared membership has to be audited against the
// same one. Hence a gate.
//
// WHAT IT FLAGS. Every non-test .go file under internal/weaver is parsed with
// go/ast. A function is RETIRING if its body calls retireSurfaceMembership, or
// calls removeEntity on an `…surface` receiver (surfaceStats' whole-entity
// sweep). In a retiring function, a boolColumn call is a finding when it reads
// the column the retirement is about:
//
//   - retireSurfaceMembership(target, entity, COL) is about COL, so a boolColumn
//     call whose column argument is written the same way is that retirement's
//     evidence, and it is the wrong reader for it;
//   - surface.removeEntity(target, entity, …) is about the row as a whole, and
//     the row-level verdict is the §10.2 `violating` column, so a boolColumn
//     call reading "violating" is that retirement's evidence.
//
// A boolColumn call whose column argument is not a string literal is ALSO a
// finding, whatever the retirement names. The pairing above compares source
// text; an argument the gate cannot compare (a variable, a call, a field) may be
// the retired column under another name, and a check that quietly passes what it
// could not resolve is the lint-gates dossier's "resolved, not counted" failure.
// The finding says which of the two reasons applies, since the remedies differ.
//
// WHAT IT DOES NOT FLAG, DELIBERATELY. A retiring function may read some OTHER
// column through boolColumn — sweepCount gates its re-arm on `violating` while
// retiring a membership at its gap column, and that gate's conservative false
// means "do not dispatch", which is the safe direction and no claim about
// anyone's workload. Flagging it would push a correct call site toward
// boolColumnRead with a discarded second result, which is precisely the shape
// boolColumn exists to name.
//
// SCOPE. One package, by source text, with no type information: the readers are
// unexported methods of one engine, so a name match inside internal/weaver
// cannot collide with anything else. A retirement reached through a helper this
// gate does not follow (a closure passed elsewhere, a new indirection) is out of
// its reach — the paired test vectors, one non-bool per retiring leg, are what
// cover that, and the component doc's dossier entry says so.
//
// A SELF-TEST RUNS ON EVERY INVOCATION. A sweep that asserts a property of an
// empty set prints a clean line and reds nothing, so main runs runSelfTest first
// (synthetic sources through this file's own checkFile; verbose with --selftest,
// silent-unless-failing otherwise, exit 2 on a mismatch because a misbehaving
// gate is a different failure from a corpus violation), and the corpus run
// refuses its all-clear if it found zero retiring functions.
//
// STRICT=1 exits non-zero on any finding; unset, it reports and exits 0.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	// weaverDir is the package this gate covers: the only place the two column
	// readers and the `surface` membership set exist.
	weaverDir = "internal/weaver"

	// The three call names the walk recognises. retireFunc and removeEntityFunc
	// are the retirements; strictReader is the reader that discards the
	// readable verdict, and the one a retirement may not be decided from.
	retireFunc       = "retireSurfaceMembership"
	removeEntityFunc = "removeEntity"
	strictReader     = "boolColumn"

	// surfaceField is the receiver a removeEntity call must be made on to be
	// the `surface` membership sweep rather than some other collection's.
	surfaceField = "surface"

	// violatingColumn is the §10.2 column carrying the lens's verdict about a
	// whole row — the read a whole-entity removal is decided from.
	violatingColumn = "violating"
)

// stats records what a run actually examined, so a clean verdict can be audited
// instead of taken on faith.
type stats struct {
	files     int
	funcs     int
	retiring  int
	retires   int
	boolReads int
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
		fmt.Fprintf(os.Stderr, "lint-weaver-witnessed-retire: cannot read %s: %v\n", weaverDir, err)
		os.Exit(2)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	fset := token.NewFileSet()
	for _, name := range names {
		path := filepath.Join(weaverDir, name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			findings = append(findings, fmt.Sprintf("%s: does not parse (%v) — this gate cannot claim to have checked it", path, err))
			continue
		}
		st.files++
		findings = append(findings, checkFile(fset, path, file, &st)...)
	}

	// A sweep that examined nothing must not report an all-clear: a moved
	// package or a renamed retirement would otherwise print the same clean line
	// a healthy corpus does.
	if st.retiring == 0 {
		findings = append(findings, fmt.Sprintf("lint-weaver-witnessed-retire: read %d file(s) and %d function(s) but found ZERO that retire a `surface` membership — the walk is broken (a renamed retirement, a moved package), and a gate that checked nothing has no all-clear to give", st.files, st.funcs))
	}

	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) == 0 {
		fmt.Printf("lint-weaver-witnessed-retire: clean — %d file(s), %d function(s), %d retiring function(s) holding %d retirement(s); %d %s call(s) inside them, none reading a retired column\n",
			st.files, st.funcs, st.retiring, st.retires, st.boolReads, strictReader)
		return
	}
	fmt.Printf("lint-weaver-witnessed-retire: %d issue(s) — %d file(s), %d function(s), %d retiring function(s)\n",
		len(findings), st.files, st.funcs, st.retiring)
	if strict {
		os.Exit(1)
	}
}

// retirement is one `surface` membership retirement found in a function body,
// and the column it is a claim about: the retire call's own column argument, or
// `violating` for the whole-entity sweep, whose verdict is the row's.
type retirement struct {
	call   string
	column string
	line   int
}

// columnRead is one boolColumn call in a function body: the source text of its
// column argument, the string literal that text resolves to (if it is one), and
// where it sits.
type columnRead struct {
	arg     string
	literal string
	isLit   bool
	line    int
}

// checkFile returns every finding for one parsed file and folds what it examined
// into st. It is the single entry point the corpus walk and the self-test both
// use, so a vector proven here is proven for the real run.
func checkFile(fset *token.FileSet, path string, file *ast.File, st *stats) []string {
	var findings []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		st.funcs++
		retires, reads := scanBody(fset, fn.Body)
		if len(retires) == 0 {
			continue
		}
		st.retiring++
		st.retires += len(retires)
		st.boolReads += len(reads)
		findings = append(findings, checkFunc(path, funcName(fn), retires, reads)...)
	}
	return findings
}

// checkFunc pairs one retiring function's retirements against its boolColumn
// reads and returns a finding for each read that could be the evidence behind a
// retirement.
func checkFunc(path, name string, retires []retirement, reads []columnRead) []string {
	var findings []string
	for _, read := range reads {
		if !read.isLit {
			findings = append(findings, fmt.Sprintf(
				"%s:%d: %s reads a column through %s(…, %s) and retires a `surface` membership (%s, line %d). This gate cannot tell whether %s is the retired column, and an unresolvable read is not a clean one: a membership retirement must be decided by %sRead's `readable` verdict, since %s reports a PRESENT non-bool as a plain false and that is not evidence the row's work is done. Read the column with %sRead and retire only when readable && !open.",
				path, read.line, name, strictReader, read.arg, retires[0].call, retires[0].line, read.arg, strictReader, strictReader, strictReader))
			continue
		}
		for _, ret := range retires {
			if !readDecidesRetirement(read, ret) {
				continue
			}
			findings = append(findings, fmt.Sprintf(
				"%s:%d: %s reads column %q through %s and retires the `surface` membership it decides at line %d (%s). %s reports a PRESENT non-bool as a plain false, so it can never witness that a column CLOSED — retiring on it shrinks the target's open-workload count on a projection fault and restores it on the next clean projection. Read the column with %sRead and retire only when readable && !open.",
				path, read.line, name, read.literal, strictReader, ret.line, ret.call, strictReader, strictReader))
			break
		}
	}
	return findings
}

// readDecidesRetirement reports whether a literal-column read is the evidence a
// retirement turns on: the same column for a per-column retire, and the row's
// own `violating` verdict for the whole-entity sweep.
func readDecidesRetirement(read columnRead, ret retirement) bool {
	if ret.column == "" {
		return false
	}
	if ret.call == removeEntityFunc {
		return read.literal == violatingColumn
	}
	return read.arg == ret.column || read.literal == ret.column
}

// scanBody walks one function body and collects the retirements it makes and the
// boolColumn reads it takes. Nested function literals are walked with the body
// that carries them: a closure retiring a membership is the same claim made one
// indirection along.
func scanBody(fset *token.FileSet, body *ast.BlockStmt) ([]retirement, []columnRead) {
	var retires []retirement
	var reads []columnRead
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		line := fset.Position(call.Lparen).Line
		switch sel.Sel.Name {
		case retireFunc:
			// retireSurfaceMembership(targetID, entityID, gapColumn).
			column := ""
			if len(call.Args) == 3 {
				column = argText(call.Args[2])
			}
			retires = append(retires, retirement{call: retireFunc, column: column, line: line})
		case removeEntityFunc:
			// Only the `surface` set's sweep: another collection's removeEntity
			// says nothing about the open-workload count.
			if !isSurfaceReceiver(sel.X) {
				return true
			}
			retires = append(retires, retirement{call: removeEntityFunc, column: violatingColumn, line: line})
		case strictReader:
			// boolColumn(targetID, entityID, row, col).
			read := columnRead{line: line}
			if len(call.Args) == 4 {
				read.arg = argText(call.Args[3])
				read.literal, read.isLit = stringLit(call.Args[3])
			}
			reads = append(reads, read)
		}
		return true
	})
	return retires, reads
}

// isSurfaceReceiver reports whether a removeEntity call is made on the engine's
// `surface` membership store — `e.surface`, or any expression ending in that
// field.
func isSurfaceReceiver(x ast.Expr) bool {
	sel, ok := x.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == surfaceField
}

// argText renders an argument expression as the source text a reader compares by
// eye, which is the only comparison available without type information.
func argText(e ast.Expr) string { return types.ExprString(e) }

// stringLit returns the value of an untyped string-literal argument. Anything
// else — a variable, a field, a call — is not resolvable here, and the caller
// reports it rather than passing it.
func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return v, true
}

// funcName renders a function declaration the way the finding must name it:
// receiver-qualified, so `sweepMark` and `handleRow` are told apart from any
// same-named helper a future file adds.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return "(" + types.ExprString(fn.Recv.List[0].Type) + ")." + fn.Name.Name
}

// runSelfTest drives synthetic sources through checkFile — the same entry point
// the corpus walk uses — so every rule this file documents has a proven positive
// vector and a paired negative one. This file carries `//go:build ignore`, which
// keeps it out of `go test`'s package builds, so this doubles as the colocated
// test: `go run ./scripts/lint-weaver-witnessed-retire.go --selftest`. It also
// runs unconditionally from main, where verbose is false: only failures print,
// and any mismatch exits 2 — a gate that does not behave as documented is a
// different failure from a corpus violation, and the counts it prints in the
// clean line are worthless if the walk beneath them is broken.
func runSelfTest(verbose bool) {
	pass := true
	check := func(cond bool, desc string) {
		switch {
		case !cond:
			fmt.Fprintln(os.Stderr, "lint-weaver-witnessed-retire selftest: FAIL —", desc)
			pass = false
		case verbose:
			fmt.Println("selftest: PASS —", desc)
		}
	}
	run := func(src string) ([]string, stats) {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "synthetic.go", "package weaver\n"+src, 0)
		if err != nil {
			check(false, fmt.Sprintf("the synthetic source must parse: %v", err))
			return nil, stats{}
		}
		var st stats
		return checkFile(fset, "synthetic.go", file, &st), st
	}
	joined := func(f []string) string { return strings.Join(f, "\n") }

	// Vector 1 — the shape the gate exists for: a per-column retirement decided
	// by boolColumn on that same column, named so the pairing is what matches
	// rather than the unresolvable-argument arm below.
	f, st := run(`
func (e *Engine) walk(targetID, entityID string, row map[string]any) {
	if !e.boolColumn(targetID, entityID, row, "missing_claim") {
		e.retireSurfaceMembership(targetID, entityID, "missing_claim")
	}
}`)
	check(len(f) == 1 && strings.Contains(joined(f), "walk") && strings.Contains(joined(f), `reads column "missing_claim"`),
		fmt.Sprintf("a retirement decided by boolColumn on its own column is flagged (got: %s)", joined(f)))
	check(st.retiring == 1 && st.retires == 1 && st.boolReads == 1,
		fmt.Sprintf("the flagged function counts as one retiring function with one read (got %d/%d/%d)", st.retiring, st.retires, st.boolReads))

	// Vector 2 — the repair: the same leg through boolColumnRead is clean, and
	// the gate must not match boolColumnRead by prefix.
	f, _ = run(`
func (e *Engine) walk(targetID, entityID, col string, row map[string]any) {
	open, readable := e.boolColumnRead(targetID, entityID, row, col)
	if readable && !open {
		e.retireSurfaceMembership(targetID, entityID, col)
	}
}`)
	check(len(f) == 0, fmt.Sprintf("the boolColumnRead repair is clean (got: %s)", joined(f)))

	// Vector 3 — the whole-entity sweep is a claim about the row, so its
	// evidence is `violating`.
	f, _ = run(`
func (e *Engine) row(targetID, entityID string, row map[string]any) {
	if !e.boolColumn(targetID, entityID, row, "violating") {
		e.surface.removeEntity(targetID, entityID, e.surfaceReflector(targetID))
	}
}`)
	check(len(f) == 1 && strings.Contains(joined(f), removeEntityFunc) && strings.Contains(joined(f), violatingColumn),
		fmt.Sprintf("a whole-entity sweep decided by boolColumn(\"violating\") is flagged (got: %s)", joined(f)))

	// Vector 4 — a boolColumn read of a DIFFERENT literal column beside a
	// per-column retirement is the sweep's re-arm gate, and is not a finding:
	// its conservative false means "do not dispatch", which claims nothing about
	// anyone's open workload.
	f, _ = run(`
func (s *sweeper) count(targetID, entityID, gapColumn string, row map[string]any) {
	open, readable := s.engine.boolColumnRead(targetID, entityID, row, gapColumn)
	if !open {
		if readable {
			s.engine.retireSurfaceMembership(targetID, entityID, gapColumn)
		}
		return
	}
	if !s.engine.boolColumn(targetID, entityID, row, "violating") {
		return
	}
}`)
	check(len(f) == 0, fmt.Sprintf("a boolColumn read of another literal column is not a finding (got: %s)", joined(f)))

	// Vector 5 — a column argument this gate cannot resolve is reported as
	// unresolvable, never silently passed.
	f, _ = run(`
func (e *Engine) walk(targetID, entityID, col string, row map[string]any) {
	other := col
	if !e.boolColumn(targetID, entityID, row, other) {
		e.retireSurfaceMembership(targetID, entityID, col)
	}
}`)
	check(len(f) == 1 && strings.Contains(joined(f), "cannot tell whether other is the retired column"),
		fmt.Sprintf("an unresolvable column argument is reported as unresolvable (got: %s)", joined(f)))

	// Vector 6 — a removeEntity on some OTHER collection is not this
	// membership's retirement, and does not make its function retiring.
	f, st = run(`
func (e *Engine) other(targetID, entityID string, row map[string]any) {
	if !e.boolColumn(targetID, entityID, row, "violating") {
		e.contraction.removeEntity(targetID, entityID)
	}
}`)
	check(len(f) == 0 && st.retiring == 0,
		fmt.Sprintf("removeEntity on another collection is out of scope (got %d finding(s), %d retiring: %s)", len(f), st.retiring, joined(f)))

	// Vector 7 — a function with no retirement at all is out of scope however it
	// reads its columns.
	f, st = run(`
func (e *Engine) openGapColumns(targetID, entityID string, row map[string]any, col string) bool {
	return e.boolColumn(targetID, entityID, row, col)
}`)
	check(len(f) == 0 && st.retiring == 0 && st.funcs == 1,
		fmt.Sprintf("a non-retiring function's boolColumn use is out of scope (got: %s)", joined(f)))

	// Vector 8 — a retirement inside a closure is the same claim one
	// indirection along, and is walked with the body carrying it.
	f, _ = run(`
func (e *Engine) walk(targetID, entityID, col string, row map[string]any) {
	e.withRow(func() {
		if !e.boolColumn(targetID, entityID, row, col) {
			e.retireSurfaceMembership(targetID, entityID, col)
		}
	})
}`)
	check(len(f) == 1, fmt.Sprintf("a retirement inside a closure is in scope (got: %s)", joined(f)))

	if !pass {
		fmt.Fprintln(os.Stderr, "lint-weaver-witnessed-retire: self-test failure(s) — the gate does not behave as documented")
		os.Exit(2)
	}
	if verbose {
		fmt.Println("selftest: all vectors passed")
	}
}
