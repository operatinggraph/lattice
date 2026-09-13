//go:build ignore

// lint-markup-escaping — every value a vertical FE writes into markup is a
// literal, an escaper call, or something derived from those.
//
// THE HAZARD. The string-building FEs (`cmd/cafe-app`, `cmd/wellness-app`,
// and two sites in `cmd/loftspace-app`) render by concatenating HTML and
// assigning `innerHTML`. Each ships a five-character escaper (`escapeHtml` /
// `esc`) and uses it at most sites — and the sites it misses are found by
// review, one at a time: a text-node-delegating escaper that never escaped a
// quote (wellness, 2026-09-13), then an `e.message` written raw into the
// empty-state div at four café panels, a cross-package class name in the
// front-desk card, and a landlord-typed `rentCurrency` reaching every
// applicant's Browse page unescaped through `money()` (2026-09-13, one pass).
// A stored XSS is one missed `esc()` away, and a test of the escaper itself
// proves nothing about who calls it.
//
// THE RULE. In every `cmd/*-app/web/app.js`, for every `X.innerHTML = E`,
// `X.innerHTML += E`, `X.outerHTML = E` and `X.insertAdjacentHTML(_, E)`, the
// expression E must be MARKUP-SAFE, where markup-safe is derived, never
// listed:
//
//   - a string, number or expression-free template literal;
//   - `a + b`, `a || b`, `a ?? b`, `c ? a : b`, `(a)` — safe when every operand
//     is; a template literal — safe when every embedded expression is;
//   - a call to the file's escaper — the function whose body maps "&" to
//     "&amp;" (derived from the file, so a renamed escaper is still found);
//   - a call to a function defined in the file — safe when every `return`
//     in its body is safe (`rosterCard`, `frontDeskCard`, `money`, …,
//     recursively; a cycle is unsafe);
//   - `.map(fn)` / `.join(s)` / `.filter(f)` / `.slice(...)` / `.trim()` /
//     `.toUpperCase()` / `.toLowerCase()` chains — safe when the receiver (or
//     `.map`'s callback return) is; `.toFixed(...)`, `.toLocaleString(...)`,
//     `.toLocaleDateString(...)`, `.toLocaleTimeString(...)`, `.padStart(...)`
//     over a number, `Number(...)`, `String(Number(...))`, `Math.*(...)`,
//     `new Date(...)` formatting — numeric shapes, safe;
//   - a local identifier — safe when its declaration's initializer and every
//     `id = …`, `id += …` and `id.push(…)` in the same function are safe;
//     `id.length` is a number.
//
// Anything else — a member read (`booking.sessionName`, `e.message`), a
// parameter, a call the scan cannot resolve — is a raw value reaching markup
// and fails at its line. An author who knows better declares it where it
// stands — on the sink's line, or on the line of the operand the scan
// stopped at (a helper's `return` that strips a key to [a-zA-Z0-9]):
// `// markup-safe: <why>` (the author-declares shape, lint-gates.md). A
// module-level `const` is read like a local; a locally bound helper
// (`const section = (title, list) => …`) like a .map callback.
//
// Run: `go run ./scripts/lint-markup-escaping.go` (`STRICT=1` to fail;
// `--list` prints every markup sink examined).
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
		findings = append(findings, "lint-markup-escaping: examined ZERO app files — the glob is broken, and a gate that checked nothing has no all-clear to give")
	}
	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) == 0 {
		fmt.Printf("lint-markup-escaping: clean — %d app file(s); %d markup sink(s) examined, %d declared markup-safe\n", st.files, st.sinks, st.declared)
		return
	}
	fmt.Printf("lint-markup-escaping: %d issue(s) — %d app file(s); %d markup sink(s) examined, %d declared markup-safe\n", len(findings), st.files, st.sinks, st.declared)
	if strict {
		os.Exit(1)
	}
}

type stats struct {
	files, sinks, declared int
}

// scan is one file's state: its functions by name, the escaper's name, the
// memo of which functions return safe markup, and the source lines (for the
// `// markup-safe:` declaration).
type scan struct {
	path     string
	lines    []string
	fs       *file.FileSet
	fns      map[string]*ast.FunctionLiteral
	escaper  string
	safeFn   map[string]int // 0 unknown · 1 in progress · 2 safe · 3 unsafe
	offender map[string]ast.Expression
	globals  map[string]ast.Expression // module-level const/let initializers
	bound    map[string]int            // callback parameters proved safe by their receiver
	st       *stats
}

// declareRe matches the declaration as a TRAILING comment — after the
// statement's closing `;` / `)` / `}` / `,` — so the words inside a string
// literal on the same line do not count.
var declareRe = regexp.MustCompile(`[;)},]\s*//\s*markup-safe:\s*\S`)

func checkSource(path, src string, st *stats) ([]string, error) {
	fs := &file.FileSet{}
	prog, err := parser.ParseFile(fs, path, parseable(src), 0)
	if err != nil {
		return nil, err
	}
	st.files++
	sc := &scan{path: path, lines: strings.Split(src, "\n"), fs: fs, fns: map[string]*ast.FunctionLiteral{}, safeFn: map[string]int{}, offender: map[string]ast.Expression{}, globals: map[string]ast.Expression{}, bound: map[string]int{}, st: st}
	for _, s := range prog.Body {
		switch d := s.(type) {
		case *ast.FunctionDeclaration:
			if d.Function.Name != nil {
				sc.fns[d.Function.Name.Name.String()] = d.Function
			}
		case *ast.LexicalDeclaration:
			for _, b := range d.List {
				if id, ok := b.Target.(*ast.Identifier); ok && b.Initializer != nil {
					sc.globals[id.Name.String()] = b.Initializer
				}
			}
		}
	}
	sc.escaper = sc.findEscaper()

	// Sinks are judged per statement list — the program's own body (a
	// top-level `document.body.innerHTML = …`) and every function body; an
	// expression-bodied arrow (`(t) => (el.innerHTML = t.name)`) is judged
	// against the empty list, so only literals and module consts pass there.
	var findings []string
	findings = append(findings, sc.checkFunction(&ast.BlockStatement{List: prog.Body})...)
	walk(prog, func(n ast.Node) bool {
		switch fn := n.(type) {
		case *ast.FunctionLiteral:
			findings = append(findings, sc.checkFunction(fn.Body)...)
		case *ast.ArrowFunctionLiteral:
			switch b := fn.Body.(type) {
			case *ast.BlockStatement:
				findings = append(findings, sc.checkFunction(b)...)
			case *ast.ExpressionBody:
				findings = append(findings, sc.checkFunction(&ast.BlockStatement{List: []ast.Statement{&ast.ExpressionStatement{Expression: b.Expression}}})...)
			}
		}
		return true
	})
	return findings, nil
}

// findEscaper names the function whose body maps "&" to "&amp;" — the
// five-character escaper every string-building app ships.
func (sc *scan) findEscaper() string {
	for name, fn := range sc.fns {
		found := false
		walk(fn.Body, func(n ast.Node) bool {
			if s, ok := n.(*ast.StringLiteral); ok && s.Value.String() == "&amp;" {
				found = true
			}
			return !found
		})
		if found {
			return name
		}
	}
	return ""
}

// checkFunction judges every markup sink in one function body (nested
// functions are judged on their own visit).
func (sc *scan) checkFunction(body *ast.BlockStatement) []string {
	var findings []string
	walkShallow(body, func(n ast.Node) bool {
		var value ast.Expression
		var at ast.Node
		switch x := n.(type) {
		case *ast.AssignExpression:
			prop := ""
			switch l := x.Left.(type) {
			case *ast.DotExpression:
				prop = l.Identifier.Name.String()
			case *ast.BracketExpression:
				if lit, ok := l.Member.(*ast.StringLiteral); ok {
					prop = lit.Value.String()
				}
			}
			if prop != "innerHTML" && prop != "outerHTML" {
				return true
			}
			// goja parses `+=` as an AssignExpression whose Operator is the
			// base PLUS, so the compound form is matched by that token.
			if x.Operator != token.ASSIGN && x.Operator != token.PLUS {
				return true
			}
			value, at = x.Right, x
		case *ast.CallExpression:
			dot, ok := x.Callee.(*ast.DotExpression)
			if !ok || dot.Identifier.Name.String() != "insertAdjacentHTML" || len(x.ArgumentList) != 2 {
				return true
			}
			value, at = x.ArgumentList[1], x
		default:
			return true
		}
		sc.st.sinks++
		line := sc.fs.Position(at.Idx0()).Line
		if sc.declaredAt(line) {
			sc.st.declared++
			if listSites {
				fmt.Printf("  %s:%d · declared markup-safe\n", sc.path, line)
			}
			return true
		}
		if off, ok := sc.safe(value, body); !ok {
			findings = append(findings, fmt.Sprintf("%s:%d: a raw value reaches the markup written here — `%s` (line %d) is neither a literal, an %s(...) call, nor derived from those, so a person-typed or cross-package string in it renders as HTML. Escape it, or declare why it cannot carry markup with `// markup-safe: <why>` on the sink's line.",
				sc.path, line, sc.describe(off), sc.fs.Position(off.Idx0()).Line, sc.escaperName()))
		} else if listSites {
			fmt.Printf("  %s:%d · every operand safe\n", sc.path, line)
		}
		return true
	})
	return findings
}

func (sc *scan) escaperName() string {
	if sc.escaper == "" {
		return "escaper"
	}
	return sc.escaper
}

// describe renders an offending expression briefly for the finding.
func (sc *scan) describe(e ast.Expression) string {
	switch x := e.(type) {
	case *ast.Identifier:
		return x.Name.String()
	case *ast.DotExpression:
		return sc.describe(x.Left) + "." + x.Identifier.Name.String()
	case *ast.CallExpression:
		return sc.describe(x.Callee) + "(...)"
	case *ast.BracketExpression:
		return sc.describe(x.Left) + "[...]"
	case *ast.ThisExpression:
		return "this"
	}
	return fmt.Sprintf("%T", e)
}

// safe classifies an expression; on failure it returns the first operand
// that is not safe. An operand whose own source line carries
// `// markup-safe: <why>` is accepted where it stands (a `return` inside a
// helper such as a domId() that strips to [a-zA-Z0-9]).
func (sc *scan) safe(e ast.Expression, fnBody *ast.BlockStatement) (ast.Expression, bool) {
	off, ok := sc.classify(e, fnBody)
	if !ok && off != nil && sc.declaredAt(sc.fs.Position(off.Idx0()).Line) {
		sc.st.declared++
		return nil, true
	}
	return off, ok
}

func (sc *scan) declaredAt(line int) bool {
	return line >= 1 && line <= len(sc.lines) && declareRe.MatchString(sc.lines[line-1])
}

func (sc *scan) classify(e ast.Expression, fnBody *ast.BlockStatement) (ast.Expression, bool) {
	switch x := e.(type) {
	case *ast.StringLiteral, *ast.NumberLiteral, *ast.BooleanLiteral, *ast.NullLiteral:
		return nil, true
	case *ast.ArrayLiteral:
		for _, el := range x.Value {
			if off, ok := sc.safe(el, fnBody); !ok {
				return off, false
			}
		}
		return nil, true
	case *ast.TemplateLiteral:
		for _, sub := range x.Expressions {
			if off, ok := sc.safe(sub, fnBody); !ok {
				return off, false
			}
		}
		return nil, true
	case *ast.BinaryExpression:
		switch x.Operator {
		case token.PLUS, token.LOGICAL_OR, token.COALESCE, token.LOGICAL_AND:
			if off, ok := sc.safe(x.Left, fnBody); !ok {
				return off, false
			}
			return sc.safe(x.Right, fnBody)
		case token.MINUS, token.MULTIPLY, token.SLASH, token.REMAINDER:
			return nil, true // arithmetic yields a number
		}
		return e, false
	case *ast.ConditionalExpression:
		if off, ok := sc.safe(x.Consequent, fnBody); !ok {
			return off, false
		}
		return sc.safe(x.Alternate, fnBody)
	case *ast.UnaryExpression:
		if x.Operator == token.NOT || x.Operator == token.MINUS || x.Operator == token.PLUS || x.Operator == token.TYPEOF {
			return nil, true
		}
		return e, false
	case *ast.SequenceExpression:
		if len(x.Sequence) > 0 {
			return sc.safe(x.Sequence[len(x.Sequence)-1], fnBody)
		}
		return e, false
	case *ast.CallExpression:
		return sc.safeCall(x, fnBody)
	case *ast.NewExpression:
		// new Date(...) etc. — a constructed object reaching markup is
		// stringified; only a Date is a known-safe shape.
		if id, ok := x.Callee.(*ast.Identifier); ok && id.Name.String() == "Date" {
			return nil, true
		}
		return e, false
	case *ast.DotExpression:
		if x.Identifier.Name.String() == "length" {
			return nil, true
		}
		return e, false
	case *ast.Identifier:
		if sc.bound[x.Name.String()] > 0 {
			return nil, true
		}
		if off, ok := sc.safeLocal(x.Name.String(), fnBody); !ok {
			if off == nil {
				off = e
			}
			return off, false
		}
		return nil, true
	case *ast.ArrowFunctionLiteral:
		// A callback's value is judged where it is applied (.map).
		return e, false
	}
	return e, false
}

// numericMethods exist only on Number / Date, so their result carries no
// markup whatever the receiver.
var numericMethods = map[string]bool{
	"toFixed": true, "toLocaleDateString": true, "toLocaleTimeString": true,
	"toISOString": true, "getTime": true, "getFullYear": true,
}

// passThroughMethods yield their receiver's content (a string's too), so the
// receiver must be safe; `join` and `concat` also splice their arguments in.
var passThroughMethods = map[string]bool{
	"join": true, "filter": true, "slice": true, "trim": true, "toUpperCase": true, "toLowerCase": true,
	"reverse": true, "sort": true, "concat": true, "toLocaleString": true, "padStart": true, "padEnd": true,
}

// safeCall classifies a call: the escaper, a numeric formatter, a
// pass-through chain over a safe receiver, a `.map` whose callback returns
// safe markup, or a file-defined function whose every return is safe.
func (sc *scan) safeCall(c *ast.CallExpression, fnBody *ast.BlockStatement) (ast.Expression, bool) {
	switch callee := c.Callee.(type) {
	case *ast.Identifier:
		name := callee.Name.String()
		if name != "" && name == sc.escaper {
			return nil, true
		}
		switch name {
		case "Number", "parseInt", "parseFloat", "Boolean":
			return nil, true
		case "String":
			if len(c.ArgumentList) == 1 {
				return sc.safe(c.ArgumentList[0], fnBody)
			}
			return c, false
		}
		if fn, ok := sc.fns[name]; ok {
			if off, ok := sc.safeFunction(name, fn); !ok {
				if off == nil {
					off = c
				}
				return off, false
			}
			return nil, true
		}
		// A helper bound locally (`const section = (title, list) => …`) is
		// judged by what it yields, like a .map callback.
		if init := sc.localInitializer(name, fnBody); init != nil {
			if off, ok := sc.safeCallback(init, fnBody); !ok {
				return off, false
			}
			return nil, true
		}
		return c, false
	case *ast.DotExpression:
		method := callee.Identifier.Name.String()
		if id, ok := callee.Left.(*ast.Identifier); ok && id.Name.String() == "Math" {
			return nil, true
		}
		if numericMethods[method] {
			return nil, true
		}
		if passThroughMethods[method] {
			if method == "join" || method == "concat" {
				for _, a := range c.ArgumentList {
					if off, ok := sc.safe(a, fnBody); !ok {
						return off, false
					}
				}
			}
			return sc.safe(callee.Left, fnBody)
		}
		if method == "map" && len(c.ArgumentList) == 1 {
			// Over a safe receiver (a module-level const of literals) the
			// callback's element parameter is itself safe.
			if _, ok := sc.classify(callee.Left, fnBody); ok {
				sc.bindParams(c.ArgumentList[0])
				defer sc.unbindParams(c.ArgumentList[0])
			}
			if off, ok := sc.safeCallback(c.ArgumentList[0], fnBody); !ok {
				return off, false
			}
			return nil, true
		}
		return c, false
	}
	return c, false
}

// safeCallback judges what a `.map` callback yields.
func (sc *scan) safeCallback(e ast.Expression, fnBody *ast.BlockStatement) (ast.Expression, bool) {
	switch fn := e.(type) {
	case *ast.ArrowFunctionLiteral:
		switch b := fn.Body.(type) {
		case *ast.ExpressionBody:
			return sc.safe(b.Expression, fnBody)
		case *ast.BlockStatement:
			return sc.safeReturns(b)
		}
	case *ast.FunctionLiteral:
		return sc.safeReturns(fn.Body)
	case *ast.Identifier:
		if f, ok := sc.fns[fn.Name.String()]; ok {
			if off, ok := sc.safeFunction(fn.Name.String(), f); !ok {
				if off == nil {
					off = e
				}
				return off, false
			}
			return nil, true
		}
	}
	return e, false
}

// safeFunction memoises whether every return of a file-defined function is
// safe markup, keeping the offending operand; a recursion cycle is unsafe.
func (sc *scan) safeFunction(name string, fn *ast.FunctionLiteral) (ast.Expression, bool) {
	switch sc.safeFn[name] {
	case 1, 3:
		return sc.offender[name], false
	case 2:
		return nil, true
	}
	sc.safeFn[name] = 1
	// A memoised verdict must not depend on the caller's bound callback
	// parameters, so the binding is suspended while the function is judged.
	saved := sc.bound
	sc.bound = map[string]int{}
	off, ok := sc.safeReturns(fn.Body)
	sc.bound = saved
	if ok {
		sc.safeFn[name] = 2
	} else {
		sc.safeFn[name] = 3
		sc.offender[name] = off
	}
	return off, ok
}

// safeReturns judges every `return` in a block (nested functions excluded);
// a block with no return yields undefined, which renders as nothing.
func (sc *scan) safeReturns(body *ast.BlockStatement) (ast.Expression, bool) {
	var off ast.Expression
	ok := true
	walkShallow(body, func(n ast.Node) bool {
		if !ok {
			return false
		}
		if r, isRet := n.(*ast.ReturnStatement); isRet && r.Argument != nil {
			if o, good := sc.safe(r.Argument, body); !good {
				off, ok = o, false
			}
		}
		return ok
	})
	return off, ok
}

// safeLocal judges an identifier by everything that feeds it in the
// function — its declaration's initializer and every `id = …`, `id += …`,
// `id.push(…)` — or, for a name the function does not declare, by its
// module-level `const` initializer. A parameter, or a name declared nowhere
// the scan reads, is unsafe. The first unsafe feed is returned.
func (sc *scan) safeLocal(name string, fnBody *ast.BlockStatement) (ast.Expression, bool) {
	declared := false
	var off ast.Expression
	judge := func(e ast.Expression) {
		if off != nil || e == nil {
			return
		}
		if o, ok := sc.safe(e, fnBody); !ok {
			off = o
			if off == nil {
				off = e
			}
		}
	}
	// Deep, not shallow: `rows.forEach((r) => { html += r.name; })` feeds
	// the outer local from inside a closure.
	walk(fnBody, func(n ast.Node) bool {
		if off != nil {
			return false
		}
		switch x := n.(type) {
		case *ast.LexicalDeclaration:
			for _, b := range x.List {
				if id, ok := b.Target.(*ast.Identifier); ok && id.Name.String() == name {
					declared = true
					judge(b.Initializer)
				}
			}
		case *ast.VariableStatement:
			for _, b := range x.List {
				if id, ok := b.Target.(*ast.Identifier); ok && id.Name.String() == name {
					declared = true
					judge(b.Initializer)
				}
			}
		case *ast.AssignExpression:
			switch l := x.Left.(type) {
			case *ast.Identifier:
				if l.Name.String() == name {
					judge(x.Right)
				}
			case *ast.BracketExpression:
				// `parts[i] = …` feeds the array like a push.
				if id, ok := l.Left.(*ast.Identifier); ok && id.Name.String() == name {
					judge(x.Right)
				}
			}
		case *ast.CallExpression:
			if dot, ok := x.Callee.(*ast.DotExpression); ok && dot.Identifier.Name.String() == "push" {
				if id, ok := dot.Left.(*ast.Identifier); ok && id.Name.String() == name {
					for _, a := range x.ArgumentList {
						judge(a)
					}
				}
			}
		}
		return off == nil
	})
	if !declared {
		if init, ok := sc.globals[name]; ok {
			return sc.safe(init, fnBody)
		}
		return nil, false
	}
	return off, off == nil
}

// bindParams / unbindParams mark a callback's parameters safe while its body
// is judged — the elements of a receiver already proved safe.
func (sc *scan) bindParams(fn ast.Expression) {
	for _, name := range paramNames(fn) {
		sc.bound[name]++
	}
}

func (sc *scan) unbindParams(fn ast.Expression) {
	for _, name := range paramNames(fn) {
		sc.bound[name]--
	}
}

func paramNames(fn ast.Expression) []string {
	var list *ast.ParameterList
	switch f := fn.(type) {
	case *ast.ArrowFunctionLiteral:
		list = f.ParameterList
	case *ast.FunctionLiteral:
		list = f.ParameterList
	}
	if list == nil {
		return nil
	}
	var out []string
	for _, b := range list.List {
		if id, ok := b.Target.(*ast.Identifier); ok {
			out = append(out, id.Name.String())
		}
	}
	return out
}

// localInitializer returns the initializer of a `const`/`let` declared in the
// function body under this name, or nil.
func (sc *scan) localInitializer(name string, fnBody *ast.BlockStatement) ast.Expression {
	var init ast.Expression
	walkShallow(fnBody, func(n ast.Node) bool {
		if init != nil {
			return false
		}
		if d, ok := n.(*ast.LexicalDeclaration); ok {
			for _, b := range d.List {
				if id, ok := b.Target.(*ast.Identifier); ok && id.Name.String() == name && b.Initializer != nil {
					init = b.Initializer
				}
			}
		}
		return init == nil
	})
	return init
}

// parseable rewrites the one syntax goja does not parse — the dynamic
// `import(...)` each app uses to load the shared descriptor-form module — into
// an ordinary call of the same length, so positions still index the source.
func parseable(src string) string {
	return dynamicImportRe.ReplaceAllString(src, "${1}IMPORT(")
}

var dynamicImportRe = regexp.MustCompile(`(^|[^A-Za-z0-9_$.])import\(`)

// walk visits every node reachable from root, depth-first; fn returning false
// prunes the subtree. goja's ast ships no visitor, so this one is reflective.
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

// runSelfTest pins the classification on a fixture: raw e.message (fails),
// an escaped sibling (passes), a card builder that escapes (passes) and one
// that leaks a member (fails at the builder's return), a `.map(...).join("")`
// over a safe builder (passes), a `parts.push` local (passes), a numeric
// formatter (passes), a `||` fallback over a raw member (fails), a declared
// sink (passes), a `.map` over a module-level const whose callback parameter
// is thereby safe (passes), a helper whose return line is declared (passes),
// an `innerHTML +=` sink (fails), a local fed inside a closure (fails), a
// declaration impostor inside a string literal (fails), `JSON.stringify` and
// a string `.padStart` (fail), a builder memoised while a callback parameter
// was bound then called with a raw member (fails — the memo is not
// poisoned), and an expression-bodied arrow sink (fails).
func runSelfTest(verbose bool) {
	const fixture = `
function escapeHtml(s) {
  const map = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" };
  return String(s).replace(/[&<>"']/g, (c) => map[c]);
}
function money(cents) { return "$" + (cents / 100).toFixed(2); }
function domId(key) {
  return key.replace(/[^a-zA-Z0-9]/g, ""); // markup-safe: stripped to [a-zA-Z0-9]
}
const OPTIONS = ["employed", "student"];
function goodCard(t) { return '<div>' + escapeHtml(t.name) + " " + money(t.cents) + "</div>"; }
function leakyCard(t) { return '<div class="meta">Opened ' + (t.openedAt || "?") + "</div>"; }
function render(rows, e) {
  body.innerHTML = '<div class="empty">' + e.message + "</div>";
  body.innerHTML = '<div class="empty">' + escapeHtml(e.message) + "</div>";
  grid.innerHTML = '<div class="grid">' + rows.map((r) => goodCard(r)).join("") + "</div>";
  grid.innerHTML = rows.map(leakyCard).join("");
  const parts = [];
  parts.push("<ul>");
  for (const r of rows) parts.push("<li>" + escapeHtml(r.name) + "</li>");
  parts.push("</ul>");
  list.innerHTML = parts.join("");
  badge.innerHTML = '<span>' + (booking.sessionName || "class") + "</span>";
  count.innerHTML = rows.length + " rows · " + money(12);
  raw.innerHTML = trusted.markup; // markup-safe: server-rendered fragment
  sel.innerHTML = "";
  form.innerHTML = OPTIONS.map((o) => '<option value="' + o + '">' + o + "</option>").join("");
  card.innerHTML = '<button id="x-' + domId(rows[0].key) + '">Go</button>';
  more.innerHTML += other.innerHTML;
  let html = "";
  rows.forEach((r) => { html += r.name; });
  list.innerHTML = html;
  const cells = [];
  cells[0] = t.name;
  row.innerHTML = cells.join("");
  cheat.innerHTML = e.message + "// markup-safe: not a comment";
  json.innerHTML = JSON.stringify(rows);
  pad.innerHTML = e.message.padStart(3);
}
function card(o) { return "<b>" + o + "</b>"; }
function m2(t) { el.innerHTML = card(t.name); }
const arrow = (t) => (el.innerHTML = t.name);
`
	var st stats
	findings, err := checkSource("fixture.js", fixture, &st)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint-markup-escaping: SELFTEST parse error: %v\n", err)
		os.Exit(2)
	}
	want := []string{
		"fixture.js:14: a raw value reaches the markup written here — `e.message` (line 14)",
		"fixture.js:17: a raw value reaches the markup written here — `t.openedAt` (line 12)",
		"fixture.js:23: a raw value reaches the markup written here — `booking.sessionName` (line 23)",
		"fixture.js:29: a raw value reaches the markup written here — `other.innerHTML` (line 29)",
		"fixture.js:32: a raw value reaches the markup written here — `r.name` (line 31)",
		"fixture.js:35: a raw value reaches the markup written here — `t.name` (line 34)",
		"fixture.js:36: a raw value reaches the markup written here — `e.message` (line 36)",
		"fixture.js:37: a raw value reaches the markup written here — `JSON.stringify(...)` (line 37)",
		"fixture.js:38: a raw value reaches the markup written here — `e.message` (line 38)",
		"fixture.js:41: a raw value reaches the markup written here — `o` (line 40)",
		"fixture.js:42: a raw value reaches the markup written here — `t.name` (line 42)",
	}
	if len(findings) != len(want) {
		fmt.Fprintf(os.Stderr, "lint-markup-escaping: SELFTEST want %d findings, got %d: %v\n", len(want), len(findings), findings)
		os.Exit(2)
	}
	for i, w := range want {
		if !strings.HasPrefix(findings[i], w) {
			fmt.Fprintf(os.Stderr, "lint-markup-escaping: SELFTEST finding %d must start with %q: %s\n", i, w, findings[i])
			os.Exit(2)
		}
	}
	if st.sinks != 19 || st.declared != 2 {
		fmt.Fprintf(os.Stderr, "lint-markup-escaping: SELFTEST want 19 sinks with 2 declared (the sink line and domId's return line — never the string-literal impostor), got %d / %d\n", st.sinks, st.declared)
		os.Exit(2)
	}
	if verbose {
		fmt.Printf("lint-markup-escaping: selftest ok — %d sinks\n", st.sinks)
	}
}
