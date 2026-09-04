//go:build ignore

// lint-slog-values — refuses an slog attribute VALUE whose static type would
// silently render as "{}" under the JSON handler production installs.
//
// # The class this catches
//
// internal/refractor/pipeline/results.go passed a PublishScope — an unexported
// struct with a String() method and no LogValue — as an slog attribute value.
// Production wires slog.NewJSONHandler (cmd/refractor/main.go), whose KindAny
// arm goes through encoding/json: json honours json.Marshaler and
// encoding.TextMarshaler, but never fmt.Stringer. So the shipped line read
// `"publishScope":{}`. The unit test that covered the line used a TextHandler,
// which DOES consult Stringer, and stayed green — the fixture proved the
// favourable arm, not the shipped one (docs/components/lint-gates.md's
// dossier, "a fixture that establishes the favourable ARM is an argument, not
// a test"). The fix was internal/refractor/pipeline/publishscope.go's
// `func (s PublishScope) LogValue() slog.Value`.
//
// This is the SECOND sighting of that exact class, so it is mechanized here
// (agents/steward/SKILL.md §4) rather than trusted to the next reviewer.
//
// results.go's actual call site is not a flat keyvals literal: it builds a
// local `attrs := []any{...}` slice, conditionally `append`s to it, and
// spreads it with `slog.Info(msg, attrs...)`. A checker that only walked a
// literal call.Args list finds zero attribute values at that exact call
// site and reports a clean tree over the very line this gate exists to
// catch — so "spread calls", below, traces that shape too, not only the
// flat literal form.
//
// # The rule
//
// For every call to an slog logging method —
// slog.{Debug,Info,Warn,Error,Log,DebugContext,InfoContext,WarnContext,
// ErrorContext}, the same methods on *slog.Logger, and slog.Any — every
// attribute VALUE argument (the odd positions of the key/value variadic tail,
// skipping any element whose own static type is already slog.Attr; the second
// argument of slog.Any) whose static type is a named struct type, a pointer to
// one, or a map/slice/array of one, DEFINED IN THIS MODULE, must implement at
// least one of slog.LogValuer, json.Marshaler, encoding.TextMarshaler (methods
// on the type or its pointer — either receiver counts, because the fix belongs
// to the type's author, not to whichever call site happened to pass a pointer
// or a value).
//
// A type from outside this module (error, time.Time, time.Duration, every
// basic kind, slog.Attr/slog.Value, and anything vendored) is out of scope by
// construction — the module-membership test IS what keeps the rule off those,
// so no separate carve-out list has to be kept in sync with the standard
// library. Implementing fmt.Stringer alone does not satisfy the rule for an
// in-module struct: Stringer is exactly the false comfort a TextHandler-only
// test hides (this gate's own reason for existing).
//
// # Spread calls
//
// `f(msg, attrs...)` collapses the whole variadic tail to ONE expression at
// the call site, so it is walked separately from the flat literal form:
// within one function (a nested closure gets its own, separately-scoped
// pass), this gate tracks every local []any variable's contents — a `:=`
// composite literal (or a `make([]any, 0[, cap])` pre-size, read as starting
// empty) seeds it, and every `x = append(x, ...)` on it extends it,
// including a spread-of-a-spread (`append(x, y...)` where y is itself a
// tracked variable or a literal). A spread source this gate cannot trace —
// a func parameter, a value returned from a call, anything reassigned by
// something other than append — is reported as an ADVISORY WARN naming the
// call site, never a silent pass: fail-legible over fail-open, the
// lint-lens-anchors unresolvable-Spec posture. `// slog-value:` on the warn's
// call line suppresses it the same way it suppresses a hard finding.
//
// # The escape hatch — author declares
//
// A call site that means to pass an unadorned struct (its zero-information
// JSON rendering is the intended shape, or the log line is dev-only and never
// reaches the JSON handler) declares it inline:
//
//	slog.Info("msg", "key", value) // slog-value: <reason>
//
// The comment may sit on the call's own line or on the value argument's own
// line (a multi-line call can carry it beside the argument it excuses). No
// comment, no pass — default-deny, author declares, the `# read-posture:`
// shape (docs/components/lint-gates.md "The author-declares shape").
//
// # What this gate cannot see
//
// A key/value pair where the KEY expression is not a string literal is still
// walked (the value at args[i+1] is checked), but the reported key name reads
// "(non-literal key)" — the finding still fires; only the label is degraded.
// slog.Group and slog.LogAttrs are not walked directly: a Group's members and
// LogAttrs' Attr arguments are themselves built by slog.Any/String/... calls
// elsewhere in the source, and this gate walks every CallExpr in every loaded
// file regardless of nesting, so those inner constructor calls are caught on
// their own. A value built through a helper function that returns `any` and is
// itself no wider than that return type is invisible: the type this gate reads
// is the static type at the call site, not what the value happens to hold at
// runtime.
//
// STRICT=1 exits non-zero on any finding; otherwise advisory. Self-vectors run
// on every invocation (positive AND negative), the lint-lens-anchors shape,
// because a module-wide grep of loaded findings alone cannot prove a checker
// that stopped matching would look any different from a clean one.
package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// moduleRoot scopes the "defined in this module" test. It is resolved fresh
// at the start of each checkModule call from the loaded main module's own
// path (packages.NeedModule), rather than hardcoded to Lattice's import path
// — the self-test loads a throwaway scratch module under its own path, and a
// hardcoded prefix would silently scope every one of its fixtures OUT of the
// rule, passing for the wrong reason (an empty result, not a checked one).
var moduleRoot string

// allowMarker is the author-declares escape hatch, matched as a comment
// PREFIX so a reason may follow it freely.
const allowMarker = "slog-value:"

// slogFuncStart maps an slog logging call's method/function name onto the
// index in call.Args where the key/value variadic tail begins. Package-level
// functions and *slog.Logger methods share these offsets — the receiver is
// not one of call.Args either way.
var slogFuncStart = map[string]int{
	"Debug": 1, "Info": 1, "Warn": 1, "Error": 1,
	"DebugContext": 2, "InfoContext": 2, "WarnContext": 2, "ErrorContext": 2,
	"Log": 3,
}

// finding is one refused call site.
type finding struct {
	pos token.Position
	key string
	typ string
}

func (f finding) String() string {
	return fmt.Sprintf("%s: slog attribute %q carries %s, which renders as {} under the JSON handler — implement slog.LogValuer (or json.Marshaler / encoding.TextMarshaler)",
		f.pos, f.key, f.typ)
}

func main() {
	strict := os.Getenv("STRICT") == "1"
	for _, a := range os.Args[1:] {
		if a == "--strict" {
			strict = true
		}
		if a == "--selftest" {
			runSelfTest(true)
			return
		}
	}

	// The rule is preventive by nature — a clean tree proves nothing about a
	// checker that stopped matching (the lint-lens-anchors precedent). The
	// vectors run on every invocation, CI included.
	runSelfTest(false)

	findings, warns, err := checkModule(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, "lint-slog-values: FAIL —", err)
		os.Exit(2)
	}

	sort.Strings(warns)
	for _, w := range warns {
		fmt.Println(w)
	}

	if len(findings) == 0 {
		fmt.Printf("lint-slog-values: 0 issues, %d advisory warning(s) — every in-module struct type reaching an slog attribute value implements slog.LogValuer, json.Marshaler, or encoding.TextMarshaler (or is declared with // slog-value:)\n", len(warns))
		return
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].pos.Filename != findings[j].pos.Filename {
			return findings[i].pos.Filename < findings[j].pos.Filename
		}
		return findings[i].pos.Line < findings[j].pos.Line
	})
	for _, f := range findings {
		fmt.Println(f)
	}
	fmt.Printf("lint-slog-values: %d issue(s), %d advisory warning(s)\n", len(findings), len(warns))
	if strict {
		os.Exit(1)
	}
}

// slogIfaces bundles the three interface types the rule accepts, resolved
// once from the loaded stdlib packages so the checker never has to hand-roll
// their method signatures.
type slogIfaces struct {
	logValuer     *types.Interface
	jsonMarshaler *types.Interface
	textMarshaler *types.Interface
}

// checkModule loads every package under dir (plus the three stdlib packages
// the rule's interfaces live in) with full type info, and walks every slog
// call site in every module-owned package.
func checkModule(dir string) ([]finding, []string, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes | packages.NeedSyntax |
			packages.NeedTypesInfo | packages.NeedModule,
		Dir:   dir,
		Tests: true,
	}
	pkgs, err := packages.Load(cfg, "./...", "log/slog", "encoding/json", "encoding")
	if err != nil {
		return nil, nil, fmt.Errorf("load packages: %w", err)
	}

	root, err := mainModulePath(pkgs)
	if err != nil {
		return nil, nil, err
	}
	moduleRoot = root

	ifaces, err := resolveIfaces(pkgs)
	if err != nil {
		return nil, nil, err
	}

	var loadErrs []string
	var findings []finding
	var warns []string
	seen := map[*packages.Package]bool{}
	var walk func(p *packages.Package)
	walk = func(p *packages.Package) {
		if seen[p] {
			return
		}
		seen[p] = true
		if !strings.HasPrefix(p.PkgPath, moduleRoot) {
			return
		}
		for _, e := range p.Errors {
			loadErrs = append(loadErrs, fmt.Sprintf("%s: %s", p.PkgPath, e.Error()))
		}
		for _, f := range p.Syntax {
			findings = append(findings, checkFile(f, p.Fset, p.TypesInfo, ifaces, &warns)...)
		}
	}
	for _, p := range pkgs {
		walk(p)
	}
	if len(loadErrs) > 0 {
		return nil, nil, fmt.Errorf("the module did not load cleanly, so this gate cannot trust its own type information:\n  %s", strings.Join(loadErrs, "\n  "))
	}

	// Loading with Tests:true gives every test-carrying directory a SEPARATE
	// "pkg [pkg.test]" package variant that re-type-checks the SAME non-test
	// source alongside it — the only way to also reach slog calls that live
	// in _test.go files. That means every finding or warn in a non-test file
	// of a package with any tests is discovered twice, at identical
	// positions, which is nearly every package here. Dedupe on the rendered
	// text rather than skip the test variant outright, so _test.go coverage
	// stays intact.
	findings = dedupeFindings(findings)
	warns = dedupeStrings(warns)
	return findings, warns, nil
}

// mainModulePath finds the module path of the main module the "./..." pattern
// was loaded from — the module owning the packages, as opposed to log/slog,
// encoding/json and encoding, which are loaded alongside it purely to resolve
// interface types and belong to no module of interest here.
func mainModulePath(pkgs []*packages.Package) (string, error) {
	for _, p := range pkgs {
		if p.Module != nil && p.Module.Main {
			return p.Module.Path, nil
		}
	}
	return "", fmt.Errorf("could not resolve the main module's path from the loaded packages")
}

// dedupeFindings drops a finding whose rendered text (position + key + type)
// exactly repeats one already kept, preserving first-seen order.
func dedupeFindings(in []finding) []finding {
	seen := map[string]bool{}
	var out []finding
	for _, f := range in {
		k := f.String()
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, f)
	}
	return out
}

// dedupeStrings is dedupeFindings' counterpart for the pre-rendered warn
// lines.
func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// resolveIfaces pulls slog.LogValuer, json.Marshaler and encoding.TextMarshaler
// out of the packages.Load result. Loading them explicitly (rather than
// hoping some module package happens to import all three) is what makes this
// dependable regardless of which files the module edits next.
func resolveIfaces(pkgs []*packages.Package) (slogIfaces, error) {
	var out slogIfaces
	find := func(pkgPath, name string) (*types.Interface, error) {
		for _, p := range pkgs {
			if p.PkgPath != pkgPath || p.Types == nil {
				continue
			}
			obj := p.Types.Scope().Lookup(name)
			if obj == nil {
				return nil, fmt.Errorf("%s.%s not found", pkgPath, name)
			}
			iface, ok := obj.Type().Underlying().(*types.Interface)
			if !ok {
				return nil, fmt.Errorf("%s.%s is not an interface", pkgPath, name)
			}
			return iface, nil
		}
		return nil, fmt.Errorf("package %s was not loaded", pkgPath)
	}
	var err error
	if out.logValuer, err = find("log/slog", "LogValuer"); err != nil {
		return out, err
	}
	if out.jsonMarshaler, err = find("encoding/json", "Marshaler"); err != nil {
		return out, err
	}
	if out.textMarshaler, err = find("encoding", "TextMarshaler"); err != nil {
		return out, err
	}
	return out, nil
}

// checkFile finds every function body in f (top-level funcs; a nested
// FuncLit gets its own scope, walked separately) and checks each one.
func checkFile(f *ast.File, fset *token.FileSet, info *types.Info, ifaces slogIfaces, warns *[]string) []finding {
	var out []finding
	var lines fileLines
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		out = append(out, checkFunctionBody(fd.Body, fset, info, ifaces, &lines, warns)...)
	}
	return out
}

// sliceValueInfo tracks what this gate can statically prove a local []any
// variable holds: the flattened list of element expressions contributed by
// every literal initializer and append() call this gate could trace, and
// whether that tracing stayed unbroken end to end. A single contribution this
// gate cannot read (an append whose extra elements come from an unresolvable
// spread, a reassignment from something other than append) poisons the whole
// variable — a partial elems list would silently under-report, which is the
// same "looks equivalent while you are writing it" failure the lint-gates.md
// dossier already caught once (a default-deny gate keyed on a proxy for the
// hazard, not the hazard).
type sliceValueInfo struct {
	elems    []ast.Expr
	resolved bool
}

// checkFunctionBody walks one function's (or closure's) body, tracking every
// local []any variable's contents alongside every slog call, in one
// left-to-right pass — the order slog attrs are actually built in.
//
// A nested *ast.FuncLit gets its OWN pass rather than being folded into this
// one: its local variables are a distinct scope even when a name shadows an
// outer one, and its own slog calls must be checked against ITS variables,
// not the enclosing function's.
func checkFunctionBody(body *ast.BlockStmt, fset *token.FileSet, info *types.Info, ifaces slogIfaces, lines *fileLines, warns *[]string) []finding {
	var out []finding
	sliceVars := map[types.Object]*sliceValueInfo{}

	var walk func(n ast.Node) bool
	walk = func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			if x.Body != nil {
				out = append(out, checkFunctionBody(x.Body, fset, info, ifaces, lines, warns)...)
			}
			return false
		case *ast.AssignStmt:
			trackSliceAssign(x, info, sliceVars)
		case *ast.CallExpr:
			out = append(out, checkSlogCall(x, fset, info, ifaces, lines, sliceVars, warns)...)
		}
		return true
	}
	ast.Inspect(body, walk)
	return out
}

// trackSliceAssign records what a single assignment contributes to a local
// []any variable — a `:=` composite literal starts the list; a
// `x = append(x, ...)` extends it. Anything else assigning to a tracked
// []any variable (a plain reassignment, an append whose extra elements this
// gate cannot resolve) marks it UNRESOLVED rather than dropping the
// contribution silently.
func trackSliceAssign(assign *ast.AssignStmt, info *types.Info, sliceVars map[types.Object]*sliceValueInfo) {
	if len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
		return
	}
	ident, ok := assign.Lhs[0].(*ast.Ident)
	if !ok || ident.Name == "_" {
		return
	}
	obj := info.ObjectOf(ident)
	if obj == nil || !isAnySliceType(obj.Type()) {
		return
	}
	switch assign.Tok {
	case token.DEFINE:
		switch {
		case isMakeEmptyAnySlice(assign.Rhs[0], info):
			// `attrs := make([]any, 0, cap)` — the pre-size-for-append idiom
			// this codebase's own tombstone_sweep.go uses. Zero length, so it
			// starts with no elements: the same starting point as `[]any{}`.
			sliceVars[obj] = &sliceValueInfo{resolved: true}
		default:
			if elts, ok := compositeLitElems(assign.Rhs[0]); ok {
				sliceVars[obj] = &sliceValueInfo{elems: elts, resolved: true}
			} else {
				sliceVars[obj] = &sliceValueInfo{resolved: false}
			}
		}
	case token.ASSIGN:
		si, tracked := sliceVars[obj]
		if !tracked {
			si = &sliceValueInfo{resolved: false}
			sliceVars[obj] = si
		}
		extra, ok := appendElems(assign.Rhs[0], info, sliceVars)
		if !ok {
			si.resolved = false
			return
		}
		if si.resolved {
			si.elems = append(si.elems, extra...)
		}
	}
}

// isAnySliceType reports whether t is []any ([]interface{} with zero
// methods) — the only type Go allows spreading into an slog `args ...any`
// parameter, so it is the only slice type this gate needs to track.
func isAnySliceType(t types.Type) bool {
	s, ok := t.Underlying().(*types.Slice)
	if !ok {
		return false
	}
	iface, ok := s.Elem().Underlying().(*types.Interface)
	return ok && iface.NumMethods() == 0
}

// compositeLitElems returns a `[]any{...}` literal's elements.
func compositeLitElems(e ast.Expr) ([]ast.Expr, bool) {
	cl, ok := e.(*ast.CompositeLit)
	if !ok {
		return nil, false
	}
	return cl.Elts, true
}

// isMakeEmptyAnySlice reports whether e is `make([]any, 0)` or
// `make([]any, 0, cap)` — a literal zero length, so the slice starts with no
// elements exactly like `[]any{}`. A length that is anything other than a
// literal 0 is NOT resolved as empty (it could hold zero-valued elements
// this gate would then have to reason about, and isAnySliceType's caller
// already routes here only for a var this gate is trying to track, so
// staying unresolved is the safe default over guessing).
func isMakeEmptyAnySlice(e ast.Expr, info *types.Info) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) < 2 {
		return false
	}
	ident, ok := call.Fun.(*ast.Ident)
	if !ok || ident.Name != "make" {
		return false
	}
	if _, isBuiltin := info.Uses[ident].(*types.Builtin); !isBuiltin {
		return false
	}
	bl, ok := call.Args[1].(*ast.BasicLit)
	return ok && bl.Kind == token.INT && bl.Value == "0"
}

// appendElems reads the elements a `append(x, ...)` call on the RHS adds,
// beyond x itself. When the call's own variadic tail is a spread
// (`append(x, more...)`), it recurses through resolveSpreadSource so a chain
// of appends-of-appends still resolves.
func appendElems(rhs ast.Expr, info *types.Info, sliceVars map[types.Object]*sliceValueInfo) ([]ast.Expr, bool) {
	call, ok := rhs.(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	ident, ok := call.Fun.(*ast.Ident)
	if !ok {
		return nil, false
	}
	if _, isBuiltin := info.Uses[ident].(*types.Builtin); !isBuiltin || ident.Name != "append" {
		return nil, false
	}
	if len(call.Args) < 1 {
		return nil, false
	}
	rest := call.Args[1:]
	if !call.Ellipsis.IsValid() || len(rest) == 0 {
		return rest, true
	}
	last := rest[len(rest)-1]
	spreadElems, ok := resolveSpreadSource(last, info, sliceVars)
	if !ok {
		return nil, false
	}
	out := append([]ast.Expr{}, rest[:len(rest)-1]...)
	return append(out, spreadElems...), true
}

// resolveSpreadSource returns the element expressions a `x...` variadic
// spread argument statically carries: a composite literal's own elements, or
// a tracked local []any variable's accumulated (and still-resolved) ones.
func resolveSpreadSource(e ast.Expr, info *types.Info, sliceVars map[types.Object]*sliceValueInfo) ([]ast.Expr, bool) {
	if elts, ok := compositeLitElems(e); ok {
		return elts, true
	}
	ident, ok := e.(*ast.Ident)
	if !ok {
		return nil, false
	}
	obj := info.ObjectOf(ident)
	if obj == nil {
		return nil, false
	}
	si, ok := sliceVars[obj]
	if !ok || !si.resolved {
		return nil, false
	}
	return si.elems, true
}

// checkSlogCall classifies one CallExpr and, when it is an slog logging call
// or an slog.Any construction, checks its attribute value argument(s) —
// including the `f(msg, attrs...)` spread shape (personal-lens-delta-
// publication-design's writeResults is exactly this shape), which resolves
// through sliceVars, and reports an advisory warn rather than passing
// silently when the spread source cannot be traced.
func checkSlogCall(call *ast.CallExpr, fset *token.FileSet, info *types.Info, ifaces slogIfaces, lines *fileLines, sliceVars map[types.Object]*sliceValueInfo, warns *[]string) []finding {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}

	if isSlogAny(sel, info) {
		if len(call.Args) >= 2 && !call.Ellipsis.IsValid() {
			return checkValue(call.Args[1], call, keyOf(call.Args[0]), fset, info, ifaces, lines)
		}
		return nil
	}

	start, ok := slogFuncStart[sel.Sel.Name]
	if !ok || !isSlogLoggingCall(sel, info) {
		return nil
	}
	if start > len(call.Args) {
		return nil
	}
	tail := call.Args[start:]

	if !call.Ellipsis.IsValid() {
		return checkKeyvals(tail, call, fset, info, ifaces, lines)
	}

	var out []finding
	if len(tail) == 0 {
		return out
	}
	spread := tail[len(tail)-1]
	out = append(out, checkKeyvals(tail[:len(tail)-1], call, fset, info, ifaces, lines)...)
	elems, resolved := resolveSpreadSource(spread, info, sliceVars)
	if !resolved {
		pos := fset.Position(spread.Pos())
		callLine := fset.Position(call.Pos()).Line
		if !lines.hasMarker(pos.Filename, pos.Line, allowMarker) && !lines.hasMarker(pos.Filename, callLine, allowMarker) {
			*warns = append(*warns, fmt.Sprintf("%s: warn: an slog call spreads %s, a variadic argument this gate cannot trace to its literal contents — check its attribute values by hand, or declare `// slog-value:` if it is known safe",
				pos, exprString(spread)))
		}
		return out
	}
	return append(out, checkKeyvals(elems, call, fset, info, ifaces, lines)...)
}

// exprString renders a spread expression for the warn message. It is
// deliberately crude (identifier / selector text, or a type name) rather
// than a full printer — this is a diagnostic label, not code the gate emits.
func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	case *ast.CallExpr:
		return exprString(v.Fun) + "(...)"
	default:
		return "a non-literal, non-variable expression"
	}
}

// isSlogAny reports whether sel.X.sel.Sel names the package-level function
// slog.Any.
func isSlogAny(sel *ast.SelectorExpr, info *types.Info) bool {
	return sel.Sel.Name == "Any" && isSlogPackageIdent(sel.X, info)
}

// isSlogLoggingCall reports whether sel is either a package-level
// slog.<Method> call, or a call to that same method name on a value whose
// type is (*)slog.Logger.
func isSlogLoggingCall(sel *ast.SelectorExpr, info *types.Info) bool {
	if isSlogPackageIdent(sel.X, info) {
		return true
	}
	selection, ok := info.Selections[sel]
	if !ok {
		return false
	}
	return isSlogLoggerType(selection.Recv())
}

// isSlogPackageIdent reports whether e is a reference to the "log/slog"
// package itself (the qualifier in a package-level slog.X(...) call).
func isSlogPackageIdent(e ast.Expr, info *types.Info) bool {
	id, ok := e.(*ast.Ident)
	if !ok {
		return false
	}
	pn, ok := info.Uses[id].(*types.PkgName)
	if !ok {
		return false
	}
	return pn.Imported().Path() == "log/slog"
}

// isSlogLoggerType reports whether t is slog.Logger or *slog.Logger.
func isSlogLoggerType(t types.Type) bool {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == "log/slog" && obj.Name() == "Logger"
}

// checkKeyvals walks an slog call's key/value variadic tail, replicating
// slog's own args-to-Attr algorithm (log/slog/attr.go's argsToAttr): an
// element whose OWN static type is already slog.Attr stands alone; otherwise
// it is a key and the NEXT element is its value.
func checkKeyvals(args []ast.Expr, call *ast.CallExpr, fset *token.FileSet, info *types.Info, ifaces slogIfaces, lines *fileLines) []finding {
	var out []finding
	i := 0
	for i < len(args) {
		if isSlogAttrType(info.TypeOf(args[i])) {
			i++
			continue
		}
		if i+1 < len(args) {
			out = append(out, checkValue(args[i+1], call, keyOf(args[i]), fset, info, ifaces, lines)...)
		}
		i += 2
	}
	return out
}

// isSlogAttrType reports whether t is slog.Attr.
func isSlogAttrType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == "log/slog" && obj.Name() == "Attr"
}

// keyOf best-effort names the key an attribute value is filed under, for the
// finding message only — a non-literal key still gets its value checked.
func keyOf(e ast.Expr) string {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "(non-literal key)"
	}
	s, err := strconv.Unquote(bl.Value)
	if err != nil {
		return "(non-literal key)"
	}
	return s
}

// checkValue inspects one attribute value expression's static type and
// refuses it when it is an in-module struct (directly, behind one pointer, or
// as a slice/array/map element) that implements none of the three sanctioned
// interfaces — unless the call or the value argument's own line carries the
// `// slog-value:` escape.
func checkValue(value ast.Expr, call *ast.CallExpr, key string, fset *token.FileSet, info *types.Info, ifaces slogIfaces, lines *fileLines) []finding {
	t := info.TypeOf(value)
	if t == nil {
		return nil
	}
	target := t
	switch u := t.Underlying().(type) {
	case *types.Slice:
		target = u.Elem()
	case *types.Array:
		target = u.Elem()
	case *types.Map:
		target = u.Elem()
	}

	named, ok := inModuleStruct(target)
	if !ok || implementsAny(named, ifaces) {
		return nil
	}

	pos := fset.Position(value.Pos())
	if lines.hasMarker(pos.Filename, pos.Line, allowMarker) || lines.hasMarker(pos.Filename, fset.Position(call.Pos()).Line, allowMarker) {
		return nil
	}

	return []finding{{
		pos: pos,
		key: key,
		typ: describeType(t),
	}}
}

// inModuleStruct reports whether t (after stripping at most one pointer) is a
// named struct type declared in this module.
func inModuleStruct(t types.Type) (*types.Named, bool) {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil, false
	}
	if _, ok := named.Underlying().(*types.Struct); !ok {
		return nil, false
	}
	obj := named.Obj()
	if obj.Pkg() == nil || !strings.HasPrefix(obj.Pkg().Path(), moduleRoot) {
		return nil, false
	}
	return named, true
}

// implementsAny reports whether named (or a pointer to it) implements any of
// the three sanctioned interfaces — either receiver counts, per this gate's
// header doc: the fix belongs to the type's author, not to how a given call
// site happened to pass it.
func implementsAny(named *types.Named, ifaces slogIfaces) bool {
	ptr := types.NewPointer(named)
	for _, iface := range []*types.Interface{ifaces.logValuer, ifaces.jsonMarshaler, ifaces.textMarshaler} {
		if types.Implements(named, iface) || types.Implements(ptr, iface) {
			return true
		}
	}
	return false
}

// describeType renders t the way a finding message names it — package-name
// qualified rather than the full import path, and marked out as a slice/
// array/map when it is one, so the report reads like the source.
func describeType(t types.Type) string {
	s := types.TypeString(t, func(p *types.Package) string { return p.Name() })
	return "a " + s + " value"
}

// fileLines caches a source file's lines so the allow-list check reads the
// file at most once no matter how many findings land in it.
type fileLines struct {
	cache map[string][]string
}

func (fl *fileLines) linesOf(filename string) []string {
	if fl.cache == nil {
		fl.cache = map[string][]string{}
	}
	if ls, ok := fl.cache[filename]; ok {
		return ls
	}
	b, err := os.ReadFile(filename)
	var ls []string
	if err == nil {
		ls = strings.Split(string(b), "\n")
	}
	fl.cache[filename] = ls
	return ls
}

// hasMarker reports whether line (1-based) of filename contains a comment
// starting with marker.
func (fl *fileLines) hasMarker(filename string, line int, marker string) bool {
	ls := fl.linesOf(filename)
	if line < 1 || line > len(ls) {
		return false
	}
	text := ls[line-1]
	idx := strings.Index(text, "//")
	if idx < 0 {
		return false
	}
	return strings.Contains(strings.TrimSpace(text[idx+2:]), marker)
}

// runSelfTest builds a standalone scratch module (its own go.mod, so
// packages.Load type-checks it independently of this repository) carrying
// every shape the rule and its escape hatch must tell apart, runs the real
// checkModule entry point over it, and asserts each vector by name — the
// lint-lens-anchors/lint-refractor-single-instance shape. It runs
// unconditionally from main (verbose=false prints nothing on success), so a
// checker that stopped matching fails loudly rather than reading as a clean
// corpus.
func runSelfTest(verbose bool) {
	dir, err := os.MkdirTemp("", "lint-slog-values-selftest")
	if err != nil {
		fmt.Fprintln(os.Stderr, "lint-slog-values selftest: FAIL — mkdtemp:", err)
		os.Exit(2)
	}
	defer os.RemoveAll(dir)

	goMod := "module selftest.example/m\n\ngo 1.23\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "lint-slog-values selftest: FAIL — write go.mod:", err)
		os.Exit(2)
	}

	const src = `package victim

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

// bareStruct has no LogValuer/Marshaler/TextMarshaler — a Stringer alone is
// the false comfort this gate exists to refuse.
type bareStruct struct{ n int }

func (b bareStruct) String() string { return "bareStruct" }

// logValuerStruct implements slog.LogValuer — clean.
type logValuerStruct struct{ n int }

func (l logValuerStruct) LogValue() slog.Value { return slog.IntValue(l.n) }

// jsonStruct implements json.Marshaler — clean.
type jsonStruct struct{ n int }

func (j jsonStruct) MarshalJSON() ([]byte, error) { return json.Marshal(j.n) }

// textStruct implements encoding.TextMarshaler on its POINTER — clean, per
// this gate's either-receiver rule.
type textStruct struct{ n int }

func (t *textStruct) MarshalText() ([]byte, error) { return []byte("x"), nil }

func plainInfo() {
	slog.Info("msg", "bad", bareStruct{1})
}

func pointerToBad() {
	slog.Info("msg", "bad", &bareStruct{1})
}

func loggerMethod() {
	l := slog.Default()
	l.Warn("msg", "bad", bareStruct{1})
}

func contextVariant() {
	slog.InfoContext(context.Background(), "msg", "bad", bareStruct{1})
}

func logFunc() {
	slog.Log(context.Background(), slog.LevelInfo, "msg", "bad", bareStruct{1})
}

func sliceOfBad() {
	slog.Info("msg", "bad", []bareStruct{{1}})
}

func mapOfBad() {
	slog.Info("msg", "bad", map[string]bareStruct{"a": {1}})
}

func anyCall() {
	slog.Info("msg", slog.Any("bad", bareStruct{1}))
}

func nonLiteralKey() {
	k := "bad"
	slog.Info("msg", k, bareStruct{1})
}

func cleanLogValuer() {
	slog.Info("msg", "ok", logValuerStruct{1})
}

func cleanJSON() {
	slog.Info("msg", "ok", jsonStruct{1})
}

func cleanTextPointerReceiver() {
	slog.Info("msg", "ok", textStruct{1})
	slog.Info("msg", "ok", &textStruct{1})
}

func outOfScopeStdlib() {
	slog.Info("msg", "err", errors.New("x"), "t", time.Now(), "d", time.Second, "n", 3, "s", "str")
}

func attrPassthrough() {
	slog.Info("msg", slog.String("k", "v"))
}

func excusedSameLine() {
	slog.Info("msg", "bad", bareStruct{1}) // slog-value: intentionally bare for this dev-only line
}

func excusedArgLine() {
	slog.Info("msg", "bad",
		bareStruct{1}, // slog-value: multi-line call, excuse sits on the value's own line
	)
}

// spreadLiteral reproduces the ACTUAL bug shape (internal/refractor/pipeline/
// results.go's writeResults): attrs is built up with a literal and a
// conditional append, then spread into the call with "attrs...", never as a
// flat key/value literal.
func spreadLiteral(scoped bool) {
	attrs := []any{"ruleId", "r1", "stage", "pipeline"}
	if scoped {
		attrs = append(attrs, "bad", bareStruct{1})
	}
	slog.Info("msg", attrs...)
}

func spreadClean() {
	attrs := []any{"ruleId", "r1"}
	attrs = append(attrs, "ok", logValuerStruct{1})
	slog.Info("msg", attrs...)
}

func spreadOfSpread() {
	base := []any{"a", 1}
	base = append(base, "bad", bareStruct{1})
	attrs := []any{}
	attrs = append(attrs, base...)
	slog.Warn("msg", attrs...)
}

func spreadUnresolvable(dynamic []any) {
	slog.Info("msg", dynamic...)
}

func spreadExcused(dynamic []any) {
	slog.Info("msg", dynamic...) // slog-value: dynamic caller-supplied attrs, reviewed at the call site
}

func spreadCompositeLitInline() {
	slog.Error("msg", []any{"bad", bareStruct{1}}...)
}
`
	if err := os.WriteFile(filepath.Join(dir, "victim.go"), []byte(src), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "lint-slog-values selftest: FAIL — write fixture:", err)
		os.Exit(2)
	}

	findings, warns, err := checkModule(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lint-slog-values selftest: FAIL — checkModule:", err)
		os.Exit(2)
	}
	var got []string
	for _, f := range findings {
		got = append(got, fmt.Sprintf("%s:%d %s %s", filepath.Base(f.pos.Filename), f.pos.Line, f.key, f.typ))
	}
	joined := strings.Join(got, "\n")
	if verbose {
		fmt.Println("selftest findings:\n" + joined)
		fmt.Println("selftest warns:\n" + strings.Join(warns, "\n"))
	}

	pass := true
	check := func(cond bool, desc string) {
		switch {
		case !cond:
			fmt.Fprintln(os.Stderr, "lint-slog-values selftest: FAIL —", desc)
			pass = false
		case verbose:
			fmt.Println("selftest: PASS —", desc)
		}
	}
	// Line-anchored assertions: read back which lines got flagged.
	flaggedLines := map[int]bool{}
	for _, f := range findings {
		if filepath.Base(f.pos.Filename) == "victim.go" {
			flaggedLines[f.pos.Line] = true
		}
	}
	lineOf := func(marker string) int {
		for i, l := range strings.Split(src, "\n") {
			if strings.Contains(l, marker) {
				return i + 1
			}
		}
		return -1
	}

	check(flaggedLines[lineOf(`slog.Info("msg", "bad", bareStruct{1})`)], "a bare struct value on a package-level slog.Info call is flagged")
	check(flaggedLines[lineOf(`slog.Info("msg", "bad", &bareStruct{1})`)], "a pointer to a bare struct is flagged")
	check(flaggedLines[lineOf(`l.Warn("msg", "bad", bareStruct{1})`)], "the same call on a *slog.Logger method value is flagged")
	check(flaggedLines[lineOf(`slog.InfoContext(context.Background(), "msg", "bad", bareStruct{1})`)], "the *Context variant's offset is read correctly and is flagged")
	check(flaggedLines[lineOf(`slog.Log(context.Background(), slog.LevelInfo, "msg", "bad", bareStruct{1})`)], "slog.Log's offset (ctx, level, msg, args...) is read correctly and is flagged")
	check(flaggedLines[lineOf(`slog.Info("msg", "bad", []bareStruct{{1}})`)], "a slice of a bare struct is flagged")
	check(flaggedLines[lineOf(`slog.Info("msg", "bad", map[string]bareStruct{"a": {1}})`)], "a map whose value type is a bare struct is flagged")
	check(flaggedLines[lineOf(`slog.Info("msg", slog.Any("bad", bareStruct{1}))`)], "slog.Any's second argument is flagged")
	check(flaggedLines[lineOf(`slog.Info("msg", k, bareStruct{1})`)], "a non-literal key does not stop the value from being checked")

	check(!flaggedLines[lineOf(`slog.Info("msg", "ok", logValuerStruct{1})`)], "a type implementing slog.LogValuer is clean")
	check(!flaggedLines[lineOf(`slog.Info("msg", "ok", jsonStruct{1})`)], "a type implementing json.Marshaler is clean")
	check(!flaggedLines[lineOf(`slog.Info("msg", "ok", textStruct{1})`)], "a value passed where TextMarshaler is on the POINTER receiver is still clean — either receiver counts")
	check(!flaggedLines[lineOf(`slog.Info("msg", "ok", &textStruct{1})`)], "the pointer form of the same type is clean")
	check(!flaggedLines[lineOf(`slog.Info("msg", "err", errors.New("x")`)], "error/time.Time/time.Duration/basic kinds/string are out of scope by the module-membership test")
	check(!flaggedLines[lineOf(`slog.Info("msg", slog.String("k", "v"))`)], "a standalone slog.Attr element in the keyvals tail is not misread as a key")
	check(!flaggedLines[lineOf(`slog.Info("msg", "bad", bareStruct{1}) // slog-value:`)], "the // slog-value: escape on the call's own line suppresses the finding")
	check(!flaggedLines[lineOf(`bareStruct{1}, // slog-value: multi-line call`)], "the // slog-value: escape on the value argument's own line suppresses it in a multi-line call")

	// The spread shape — attrs := []any{...}; attrs = append(attrs, ...);
	// f(msg, attrs...) — is the ACTUAL shape results.go's writeResults uses,
	// and is what the first cut of this gate (a plain call.Args walk) missed
	// entirely: a spread call's variadic tail is ONE expression, not a flat
	// keyvals list, so that cut silently found zero findings on this exact
	// case (caught only by running it against the real mutation, not by any
	// unit vector — which is why these fixtures exist now).
	check(flaggedLines[lineOf(`attrs = append(attrs, "bad", bareStruct{1})`)], "a bad value appended (inside an if) to an attrs slice that is later spread with attrs... is flagged")
	check(!flaggedLines[lineOf(`attrs = append(attrs, "ok", logValuerStruct{1})`)], "a clean value appended to a spread attrs slice stays clean")
	check(flaggedLines[lineOf(`base = append(base, "bad", bareStruct{1})`)], "a bad value survives TWO levels of spread (base...attrs, then attrs...call) — append(x, y...) chains resolve")
	check(flaggedLines[lineOf(`slog.Error("msg", []any{"bad", bareStruct{1}}...)`)], "a []any{...} literal spread inline (no intermediate variable) is flagged")

	// An UNRESOLVABLE spread (a func parameter, not traced to a literal) is a
	// WARN, not a silent pass and not a hard issue — fail-legible, matching
	// lint-lens-anchors' unresolvable-Spec posture, and distinct from
	// spreadExcused's // slog-value: escape, which suppresses the warn
	// entirely.
	warnsJoined := strings.Join(warns, "\n")
	unresolvableLine := lineOf(`slog.Info("msg", dynamic...)`)
	excusedLine := lineOf(`slog.Info("msg", dynamic...) // slog-value: dynamic caller-supplied attrs`)
	warnedAt := func(line int) bool {
		marker := fmt.Sprintf(":%d:", line)
		for _, w := range warns {
			if strings.Contains(w, marker) {
				return true
			}
		}
		return false
	}
	check(warnedAt(unresolvableLine), "an unresolvable spread source (a func parameter) produces an advisory warn naming its line")
	check(!warnedAt(excusedLine), "the // slog-value: escape on an unresolvable spread call's own line suppresses its warn too")

	// Total-count floor: every vector above that should be an ISSUE, and
	// nothing else. This is what would catch a checker that silently stopped
	// walking a whole call kind (methods, Context variants, Any, or the
	// spread shape) while every individual assertion above happened to still
	// find SOME line flagged from a different vector.
	check(len(flaggedLines) == 12, fmt.Sprintf("exactly 12 distinct victim.go lines are flagged as issues (got %d: %v)", len(flaggedLines), sortedInts(flaggedLines)))
	check(len(warns) == 1, fmt.Sprintf("exactly 1 advisory warn (the unresolvable, non-excused spread) — got %d:\n%s", len(warns), warnsJoined))

	if !pass {
		fmt.Fprintln(os.Stderr, "lint-slog-values: self-test failure(s) — the gate does not behave as documented")
		os.Exit(2)
	}
	if verbose {
		fmt.Println("selftest: all vectors passed")
	}
}

func sortedInts(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}
