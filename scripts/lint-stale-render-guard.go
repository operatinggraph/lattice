//go:build ignore

// lint-stale-render-guard — a renderer that re-checks "am I still current"
// after an await re-checks it on EVERY path out of that await, the catch
// included, before anything it paints.
//
// THE HAZARD. The vertical FEs render async: an event fires, the renderer
// captures what it was asked for (a class, a provider, a patient, the open
// modal's task), awaits a fetch or the descriptor-form module, then paints. A
// second event during the await starts a newer render of a different subject,
// and the older one's continuation still runs — so each renderer carries a
// staleness guard: `if (generation !== rosterGeneration) return;`,
// `if ($("#avail-provider").value !== prov) return;`, `if (state.correcting
// !== a) return;`. The guard is only as good as its coverage, and the coverage
// keeps having the same hole: the CATCH beside the guarded await, and the
// outer function's own writes when only its callees were guarded. A stale
// render's failure then wipes the newer render's form, its error toast names
// the wrong subject, or — worst — its `closeX()` takes down the modal the
// operator has since opened on something else. (Minted: wellness
// studio-retired note, 2026-09-06; re-sighted 2026-09-13 at five sites across
// three apps — wellness renderRoster's own six writes, three clinic catch
// blocks that clear the mount before the guard, and LoftSpace's task-modal
// close after the submit.)
//
// THE RULES, over every `cmd/*-app/web/app.js`, per function (nested
// functions judged on their own):
//
//  1. POST-TRY GUARD. A `try` whose body awaits, followed immediately by
//     `if (<test>) return;`, has declared <test> as the guard for that await's
//     window. Its catch body must open with the same guard (text-identical)
//     before any statement that could paint — a call, or an assignment to a
//     member (`mount.innerHTML =`, `state.x =`). A catch that only returns or
//     rethrows paints nothing and needs no guard.
//
//  2. CAPTURED GENERATION. A function that captures a generation counter
//     (`const generation = ++rosterGeneration`) must, after every statement
//     that awaits, re-check it (`if (generation !== rosterGeneration) return;`)
//     before the next statement that could paint — following the await out of
//     its try body, its `if` block, or its loop body into the enclosing list,
//     and into the try's catch body as rule 1 does. Local bindings and
//     assignments to plain locals in between are not paints.
//
// Both rules read the JavaScript through goja's parser (the same engine the
// FE tests execute under), so they see the statement lists, not indentation.
//
// Run: `go run ./scripts/lint-stale-render-guard.go` (`STRICT=1` to fail;
// `--list` prints every guarded await examined with its verdict).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/file"
	"github.com/dop251/goja/parser"
	"github.com/dop251/goja/token"
)

var listSites = false

func main() {
	strict := os.Getenv("STRICT") == "1"
	verbose := false
	for _, a := range os.Args[1:] {
		switch a {
		case "--selftest":
			verbose = true
		case "--list":
			listSites = true
		}
	}
	runSelfTest(verbose)

	apps, _ := filepath.Glob("cmd/*-app/web/app.js")
	sort.Strings(apps)
	var findings []string
	var st stats
	for _, path := range apps {
		src, err := os.ReadFile(path)
		if err != nil {
			findings = append(findings, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		f, err := checkSource(path, string(src), &st)
		if err != nil {
			findings = append(findings, fmt.Sprintf("%s: goja could not parse the file — the FE tests execute it under the same engine, so this is the app's own failure, not this gate's: %v", path, err))
			continue
		}
		findings = append(findings, f...)
	}
	if st.files == 0 {
		findings = append(findings, "lint-stale-render-guard: examined ZERO app files — the glob is broken, and a gate that checked nothing has no all-clear to give")
	}
	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) == 0 {
		fmt.Printf("lint-stale-render-guard: clean — %d app file(s); %d guarded try(s) and %d generation-capturing function(s) with %d await(s) examined\n",
			st.files, st.guardedTries, st.generationFns, st.generationAwaits)
		return
	}
	fmt.Printf("lint-stale-render-guard: %d issue(s) — %d app file(s); %d guarded try(s) and %d generation-capturing function(s) with %d await(s) examined\n",
		len(findings), st.files, st.guardedTries, st.generationFns, st.generationAwaits)
	if strict {
		os.Exit(1)
	}
}

type stats struct {
	files, guardedTries, generationFns, generationAwaits int
}

// scan carries what one file's walk needs: its source (the guard comparison
// slices it by goja's byte positions) and its position table.
type scan struct {
	path string
	src  string
	fs   *file.FileSet
	st   *stats
}

func checkSource(path, src string, st *stats) ([]string, error) {
	fs := &file.FileSet{}
	prog, err := parser.ParseFile(fs, path, parseable(src), 0)
	if err != nil {
		return nil, err
	}
	st.files++
	sc := &scan{path: path, src: src, fs: fs, st: st}
	var findings []string
	walk(prog, func(n ast.Node) bool {
		var body *ast.BlockStatement
		name := "(anonymous function)"
		switch fn := n.(type) {
		case *ast.FunctionLiteral:
			body = fn.Body
			if fn.Name != nil {
				name = fn.Name.Name.String()
			}
		case *ast.ArrowFunctionLiteral:
			if b, ok := fn.Body.(*ast.BlockStatement); ok {
				body = b
			}
		}
		if body == nil {
			return true
		}
		findings = append(findings, sc.checkPostTryGuards(name, body)...)
		findings = append(findings, sc.checkGeneration(name, body)...)
		return true
	})
	return findings, nil
}

// parseable rewrites the one syntax goja does not parse — the dynamic
// `import(...)` each app uses to load the shared descriptor-form module — into
// an ordinary call of the same LENGTH, so every position still indexes the
// original source.
func parseable(src string) string {
	return dynamicImportRe.ReplaceAllString(src, "${1}IMPORT(")
}

var dynamicImportRe = regexp.MustCompile(`(^|[^A-Za-z0-9_$.])import\(`)

// text returns a node's source, whitespace-normalised, for guard comparison.
// goja's positions are 1-based byte offsets into the parsed text, which
// parseable keeps the same length as the source.
func (sc *scan) text(n ast.Node) string {
	a, b := int(n.Idx0())-1, int(n.Idx1())-1
	if a < 0 || b > len(sc.src) || a > b {
		return ""
	}
	return strings.Join(strings.Fields(sc.src[a:b]), " ")
}

func (sc *scan) line(n ast.Node) int { return sc.fs.Position(n.Idx0()).Line }

// ---- rule 1: post-try guard ------------------------------------------------

// checkPostTryGuards finds every `try { …await… } catch` immediately followed
// by an `if (<test>) return;` and requires the catch to open with that guard.
func (sc *scan) checkPostTryGuards(fn string, body *ast.BlockStatement) []string {
	var findings []string
	sc.eachList(body, func(list []ast.Statement) {
		for i, s := range list {
			try, ok := s.(*ast.TryStatement)
			if !ok || try.Catch == nil || try.Catch.Body == nil || !containsAwait(try.Body) {
				continue
			}
			if i+1 >= len(list) {
				continue
			}
			test, ok := guardTest(list[i+1])
			if !ok {
				continue
			}
			sc.st.guardedTries++
			want := sc.text(test)
			verdict := "catch opens with the guard"
			if ok, offender := sc.catchOpensWith(try.Catch.Body, want); !ok {
				verdict = "CATCH PAINTS BEFORE THE GUARD"
				findings = append(findings, fmt.Sprintf("%s:%d: the catch in %s paints before re-checking `%s` — the try it belongs to is guarded by that test on its success path, so a stale render's failure here wipes or closes what the newer render painted. Open the catch with `if (%s) return;`.",
					sc.path, sc.line(offender), fn, want, want))
			}
			if listSites {
				fmt.Printf("  %s:%d · %s · try guarded by `%s` → %s\n", sc.path, sc.line(try), fn, want, verdict)
			}
		}
	})
	return findings
}

// guardTest recognises `if (<test>) return;` (bare or braced) and returns the
// test expression.
func guardTest(s ast.Statement) (ast.Expression, bool) {
	ifs, ok := s.(*ast.IfStatement)
	if !ok || ifs.Alternate != nil {
		return nil, false
	}
	if isBareReturn(ifs.Consequent) {
		return ifs.Test, true
	}
	return nil, false
}

// isBareReturn matches `return;` — a guard leaves, it answers nothing; a
// `return value` after a try is a result, not a staleness check.
func isBareReturn(s ast.Statement) bool {
	switch x := s.(type) {
	case *ast.ReturnStatement:
		return x.Argument == nil
	case *ast.BlockStatement:
		return len(x.List) == 1 && isBareReturn(x.List[0])
	}
	return false
}

// catchOpensWith reports whether the catch body's first painting statement is
// preceded by the guard `want`; a catch that only returns or rethrows is fine.
// The offending statement is returned on failure.
func (sc *scan) catchOpensWith(body *ast.BlockStatement, want string) (bool, ast.Statement) {
	for _, s := range body.List {
		if test, ok := guardTest(s); ok && sc.text(test) == want {
			return true, nil
		}
		if !canPaint(s) {
			continue
		}
		return false, s
	}
	return true, nil
}

// canPaint reports whether a statement could write something a person sees
// or a later render reads: any call, or an assignment to a member. Local
// bindings, assignments to plain locals without calls, bare returns and
// throws cannot.
func canPaint(s ast.Statement) bool {
	switch x := s.(type) {
	case *ast.ReturnStatement:
		return x.Argument != nil && hasCallOrMemberAssign(x.Argument)
	case *ast.ThrowStatement:
		return false
	case *ast.LexicalDeclaration:
		for _, b := range x.List {
			if b.Initializer != nil && hasCallOrMemberAssign(b.Initializer) {
				return true
			}
		}
		return false
	case *ast.VariableStatement:
		for _, b := range x.List {
			if b.Initializer != nil && hasCallOrMemberAssign(b.Initializer) {
				return true
			}
		}
		return false
	case *ast.ExpressionStatement:
		if as, ok := x.Expression.(*ast.AssignExpression); ok {
			if _, local := as.Left.(*ast.Identifier); local {
				return hasCallOrMemberAssign(as.Right)
			}
			return true
		}
		return hasCallOrMemberAssign(x.Expression)
	}
	// if / try / for / switch / block: judged by what is inside.
	found := false
	walkShallow(s, func(n ast.Node) bool {
		if st, ok := n.(ast.Statement); ok && st != s {
			if canPaint(st) {
				found = true
			}
			return false
		}
		return !found
	})
	return found
}

func hasCallOrMemberAssign(e ast.Expression) bool {
	found := false
	walkShallow(e, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpression, *ast.NewExpression:
			found = true
		case *ast.AssignExpression:
			if _, local := x.Left.(*ast.Identifier); !local {
				found = true
			}
		}
		return !found
	})
	return found
}

// ---- rule 2: captured generation -------------------------------------------

// checkGeneration finds `const G = ++X` in the function body and requires a
// `G !== X` re-check between every await and the next paint.
func (sc *scan) checkGeneration(fn string, body *ast.BlockStatement) []string {
	g, counter := capturedGeneration(body)
	if g == "" {
		return nil
	}
	sc.st.generationFns++
	var findings []string
	isGuard := func(s ast.Statement) bool {
		test, ok := guardTest(s)
		if !ok {
			return false
		}
		bin, ok := test.(*ast.BinaryExpression)
		if !ok || (bin.Operator != token.STRICT_NOT_EQUAL && bin.Operator != token.NOT_EQUAL) {
			return false
		}
		l, lok := bin.Left.(*ast.Identifier)
		r, rok := bin.Right.(*ast.Identifier)
		if !lok || !rok {
			return false
		}
		return (l.Name.String() == g && r.Name.String() == counter) || (l.Name.String() == counter && r.Name.String() == g)
	}
	var check func(list []ast.Statement, parentOK func() (bool, ast.Statement))
	// after reports whether, starting at list[i+1], the guard comes before
	// the next paint; the enclosing list's continuation is consulted when
	// this list runs out.
	after := func(list []ast.Statement, i int, cont func() (bool, ast.Statement)) (bool, ast.Statement) {
		for j := i + 1; j < len(list); j++ {
			s := list[j]
			if isGuard(s) {
				return true, nil
			}
			if canPaint(s) {
				return false, s
			}
		}
		if cont == nil {
			return true, nil
		}
		return cont()
	}
	check = func(list []ast.Statement, cont func() (bool, ast.Statement)) {
		for i, s := range list {
			i := i
			next := func() (bool, ast.Statement) { return after(list, i, cont) }
			// Descend into block-bearing statements first so an await inside
			// a try / if / loop body is judged with this list as its
			// continuation.
			switch x := s.(type) {
			case *ast.TryStatement:
				check(x.Body.List, next)
				// A try followed by the guard is rule 1's; its catch is judged
				// there, once.
				guardedNext := i+1 < len(list) && isGuard(list[i+1])
				if x.Catch != nil && x.Catch.Body != nil && containsAwait(x.Body) && !guardedNext {
					sc.st.generationAwaits++
					if ok, off := after(x.Catch.Body.List, -1, next); !ok {
						findings = append(findings, sc.generationFinding(fn, off, g, counter))
					}
				}
				if x.Finally != nil {
					check(x.Finally.List, next)
				}
			case *ast.BlockStatement:
				check(x.List, next)
			case *ast.IfStatement:
				for _, br := range []ast.Statement{x.Consequent, x.Alternate} {
					if b, ok := br.(*ast.BlockStatement); ok {
						check(b.List, next)
					}
				}
			case *ast.ForStatement:
				if b, ok := x.Body.(*ast.BlockStatement); ok {
					check(b.List, next)
				}
			case *ast.ForOfStatement:
				if b, ok := x.Body.(*ast.BlockStatement); ok {
					check(b.List, next)
				}
			case *ast.ForInStatement:
				if b, ok := x.Body.(*ast.BlockStatement); ok {
					check(b.List, next)
				}
			case *ast.WhileStatement:
				if b, ok := x.Body.(*ast.BlockStatement); ok {
					check(b.List, next)
				}
			}
			if !statementAwaits(s) {
				continue
			}
			sc.st.generationAwaits++
			if ok, off := next(); !ok {
				findings = append(findings, sc.generationFinding(fn, off, g, counter))
			}
			if listSites {
				fmt.Printf("  %s:%d · %s · await re-checked `%s !== %s`\n", sc.path, sc.line(s), fn, g, counter)
			}
		}
	}
	check(body.List, nil)
	return findings
}

func (sc *scan) generationFinding(fn string, offender ast.Statement, g, x string) string {
	return fmt.Sprintf("%s:%d: %s captures `%s = ++%s` and then paints here after an await without re-checking it — a slower render whose subject the person has moved on from lands its result over the newer one. Put `if (%s !== %s) return;` between the await and this statement.",
		sc.path, sc.line(offender), fn, g, x, g, x)
}

// statementAwaits reports whether a simple statement (not a block-bearing
// one, which is judged by its contents) contains an await.
func statementAwaits(s ast.Statement) bool {
	switch s.(type) {
	case *ast.TryStatement, *ast.BlockStatement, *ast.IfStatement, *ast.ForStatement, *ast.ForOfStatement, *ast.ForInStatement, *ast.WhileStatement:
		return false
	}
	found := false
	walkShallow(s, func(n ast.Node) bool {
		if _, ok := n.(*ast.AwaitExpression); ok {
			found = true
		}
		return !found
	})
	return found
}

// capturedGeneration finds `const G = ++X` at the function's top level.
func capturedGeneration(body *ast.BlockStatement) (g, x string) {
	for _, s := range body.List {
		decl, ok := s.(*ast.LexicalDeclaration)
		if !ok {
			continue
		}
		for _, b := range decl.List {
			id, ok := b.Target.(*ast.Identifier)
			if !ok {
				continue
			}
			u, ok := b.Initializer.(*ast.UnaryExpression)
			if !ok || u.Operator != token.INCREMENT || u.Postfix {
				continue
			}
			if cnt, ok := u.Operand.(*ast.Identifier); ok {
				return id.Name.String(), cnt.Name.String()
			}
		}
	}
	return "", ""
}

// ---- walking ----------------------------------------------------------------

// eachList calls fn with every statement list in the function body — the body
// itself and every nested block — without entering nested functions.
func (sc *scan) eachList(body *ast.BlockStatement, fn func([]ast.Statement)) {
	walkShallow(body, func(n ast.Node) bool {
		if b, ok := n.(*ast.BlockStatement); ok {
			fn(b.List)
		}
		return true
	})
}

func containsAwait(body *ast.BlockStatement) bool {
	found := false
	walkShallow(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.AwaitExpression); ok {
			found = true
		}
		return !found
	})
	return found
}

// walk visits every node reachable from root, depth-first; fn returning false
// prunes the subtree. goja's ast ships no visitor, so this one is reflective:
// it follows exported pointer, slice and interface fields whose values are
// ast nodes. A function literal's DeclarationList aliases bindings already in
// its body and is skipped so nothing is visited twice.
func walk(root ast.Node, fn func(ast.Node) bool) { walkValue(reflect.ValueOf(root), fn, false, true) }

// walkShallow is walk without descending into nested function bodies.
func walkShallow(root ast.Node, fn func(ast.Node) bool) {
	walkValue(reflect.ValueOf(root), fn, true, true)
}

var nodeType = reflect.TypeOf((*ast.Node)(nil)).Elem()

func walkValue(v reflect.Value, fn func(ast.Node) bool, shallow, root bool) {
	if !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return
		}
		walkValue(v.Elem(), fn, shallow, root)
	case reflect.Ptr:
		if v.IsNil() {
			return
		}
		if v.Type().Implements(nodeType) {
			n := v.Interface().(ast.Node)
			if shallow && !root && isFunction(n) {
				return
			}
			if !fn(n) {
				return
			}
		}
		walkValue(v.Elem(), fn, shallow, false)
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() || f.Name == "DeclarationList" {
				continue
			}
			walkValue(v.Field(i), fn, shallow, false)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			walkValue(v.Index(i), fn, shallow, false)
		}
	}
}

func isFunction(n ast.Node) bool {
	switch n.(type) {
	case *ast.FunctionLiteral, *ast.ArrowFunctionLiteral:
		return true
	}
	return false
}

// runSelfTest pins both rules on a fixture: a guarded try whose catch paints
// first (fails), one whose catch opens with the guard (passes), one whose
// catch only returns (passes); a generation function that paints straight
// after its await (fails), one that re-checks first (passes), one whose
// await is followed only by local bindings and then the guard (passes), and
// one whose catch paints (fails).
func runSelfTest(verbose bool) {
	const fixture = `
// — a non-ASCII character and a dynamic import up front pin the byte positions —
const formModule = import("/shared/form.mjs");
async function editForm(prov) {
  let renderOpForm;
  try {
    ({ renderOpForm } = await loadForm());
  } catch (e) {
    mount.innerHTML = "";
    toast("Could not load: " + e.message);
    return;
  }
  if ($("#avail-provider").value !== prov) return;
  mount.innerHTML = "x";
}
async function goodForm(a) {
  try {
    await loadForm();
  } catch (e) {
    if (state.correcting !== a) return;
    toast("nope");
    closeX();
    return;
  }
  if (state.correcting !== a) return;
  paint();
}
async function quietCatch(a) {
  try { await loadForm(); } catch (e) { return; }
  if (state.x !== a) return;
  paint();
}
let rosterGeneration = 0;
async function badRoster() {
  const generation = ++rosterGeneration;
  const r = await appGet("/x");
  body.innerHTML = r.html;
}
async function goodRoster() {
  const generation = ++rosterGeneration;
  let rows;
  try {
    const r = await appGet("/x");
    rows = r.rows || [];
  } catch (e) {
    if (generation !== rosterGeneration) return;
    body.innerHTML = "err";
    return;
  }
  if (generation !== rosterGeneration) return;
  body.innerHTML = rows.length;
  await sub(rows, generation);
  if (generation !== rosterGeneration) return;
  paint();
}
async function badCatchRoster() {
  const generation = ++rosterGeneration;
  try {
    await appGet("/x");
  } catch (e) {
    body.innerHTML = "err";
    return;
  }
  if (generation !== rosterGeneration) return;
}
`
	var st stats
	findings, err := checkSource("fixture.js", fixture, &st)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint-stale-render-guard: SELFTEST parse error: %v\n", err)
		os.Exit(2)
	}
	want := []string{"fixture.js:9: the catch in editForm paints before re-checking `$(\"#avail-provider\").value !== prov`", "fixture.js:37: badRoster", "fixture.js:61: the catch in badCatchRoster paints before re-checking `generation !== rosterGeneration`"}
	if len(findings) != len(want) {
		fmt.Fprintf(os.Stderr, "lint-stale-render-guard: SELFTEST want %d findings %v, got %d: %v\n", len(want), want, len(findings), findings)
		os.Exit(2)
	}
	for i, w := range want {
		if !strings.HasPrefix(findings[i], w) {
			fmt.Fprintf(os.Stderr, "lint-stale-render-guard: SELFTEST finding %d must start with %q: %s\n", i, w, findings[i])
			os.Exit(2)
		}
	}
	if st.guardedTries != 5 || st.generationFns != 3 {
		fmt.Fprintf(os.Stderr, "lint-stale-render-guard: SELFTEST want 5 guarded tries (two of them generation-guarded) and 3 generation functions, got %d / %d\n", st.guardedTries, st.generationFns)
		os.Exit(2)
	}
	if verbose {
		fmt.Printf("lint-stale-render-guard: selftest ok — %d guarded tries, %d generation awaits\n", st.guardedTries, st.generationAwaits)
	}
}
