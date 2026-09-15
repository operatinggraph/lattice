//go:build ignore

// lint-refusal-courtesy — every form that dispatches an op a script can
// refuse with a STATE code declares the courtesy it gives that code.
//
// THE HAZARD. A package script gains a refusal — `fail("ItemUnavailable: …")`
// in a helper three calls below the dispatch block — and the form the author
// had open gains the courtesy that keeps a visitor from ever meeting it: the
// picker drops the row, the button hides, the input caps. Every OTHER form
// that dispatches the same op stays exactly as it was, and the next visitor
// there meets a raw refusal toast after filling the whole form. The class is
// per-form, and a fix at one form teaches the sibling nothing. Three
// sightings, each caught cold in review: the café house-tab payment cap
// (2026-09-05, one form capped the amount, its sibling did not); the wellness
// guest picker vs member picker (2026-09-13, the member picker dropped the
// ineligible row, the guest picker offered it); and café ItemUnavailable
// (2026-09-15) — where the third form was in ANOTHER client entirely: Facet's
// descriptor-driven self-order, fed by edge-manifest's `edgeEntityMenuItems`,
// which no vertical FE census would ever have walked. The dossier's prescribed
// check — "when an op gains a refusal, walk every form that dispatches it and
// give each the courtesy its sibling already has; the census spans every
// client" — is skipped at exactly the moment a lint can force it: when a
// script gains a `fail(` code, or a client gains a dispatch site.
//
// THE RULE. Three derivations and one binding:
//
//  1. AN OP'S CODES are read off the shipped package corpus (pkgregistry —
//     every distinct DDL Script body, parsed with go.starlark.net/syntax).
//     Every dispatch block (`if ot == "X":` / `op.operationType == "X"`, the
//     `or`-joined and `in [...]` forms lint-opmeta-required-fields recognises)
//     owns every `fail(...)` reachable from it: in the block body (recursively,
//     nested defs / lambdas / comprehensions included — a fail inside a nested
//     def belongs to the enclosing block), plus transitively through every
//     script `def` the block calls (call-graph closure — a helper's helper
//     counts, which is where the refusals actually live: café
//     `require_no_credit_hold` → CreditHold, `require_open_status` →
//     TabNotOpen, `require_menu_item_price` → ItemUnavailable). A fail's CODE
//     is the leftmost operand of its first argument (a bare literal, or the
//     left end of a `+` / `%` chain): a string literal yields its text up to
//     the first `:`; an IDENTIFIER is resolved as the helper's parameter
//     through the bindings each call edge establishes — a string-literal
//     argument by position or keyword (`claim_cell(hub, cc, "SlotConflict",
//     "provider")`, `require_x(key, code="Y")`), or a caller's own bound
//     parameter forwarded to the callee, through any depth of forwarding.
//     An op's code set is SCOPED to the package that describes it (the
//     OpMetaSpec owner's scripts): a bare op name is global and two
//     verticals' ledgers both dispatch `DebitAccount`, but a site dispatching
//     the described op reaches the describing package's script. An op no
//     package describes keeps the union of every package that dispatches it.
//  2. THE EXEMPT FAMILY is the codes whose courtesy is not per-form, and is
//     therefore not governed here: `InvalidArgument` (payload shape — the
//     field's own validation, bound by lint-opmeta-required-fields),
//     `WrongClass` (a site binds one class), `AuthDenied` / `PermissionDenied`
//     (the hat the whole surface renders under), `NotFound` / `Unknown[A-Z]…`
//     / `NotA[A-Z]…` (a dead or mistyped key — the read-model rows a site
//     offers already filter `isDeleted`). Everything else is a STATE code and
//     is governed. A code that is not a bare `[A-Za-z]+` token (a prose
//     message with no `Code:` prefix) is not a code and is exempt too. The
//     boundary lives HERE; widening it is a gate edit, never a declaration.
//  3. A SITE is a place a client dispatches the op. In a vertical FE
//     (`cmd/*-app/web/app.js`, read with goja's parser — the engine the FE
//     tests run under — after lint-ceremony-throw-path's `parseable`
//     rewrite of the dynamic import), a site is a TOP-LEVEL STATEMENT
//     (usually a function declaration; a top-level const/expression
//     statement otherwise) whose span names a registered op: a string
//     literal equal to the op name, a member access whose property is the
//     op name (`opCatalogCache.CreditCafeAccount`, `catalog["X"]`), or an
//     object key spelled as the op name (`SignLease: {…}` — goja represents
//     a bare key as a string literal). The `KNOWN_CATALOG_OPS` array literal
//     is the app's catalog prefetch list, not a dispatch, and is excluded;
//     comments are not in the AST and never count. Registered ops are the
//     union of every op-meta's OperationType and every op a dispatch block
//     names, so a hand-wired op with no descriptor still counts. A GENERIC
//     DISPATCHER — a top-level function that resolves a catalog row from a
//     NON-literal op name (`descriptorFor(task.operationName)`,
//     `opCatalogCache[opType]`, `state.opCatalog[name]`: a call to
//     `descriptorFor` or a bracket lookup on any identifier containing
//     `atalog`, with a non-string-literal argument) — dispatches ops the
//     scanner cannot see, so it MUST carry a comment-only line
//     `// refusal-courtesy-dispatches: <Op>[, <Op>…]` inside its span (or in
//     the block above its header) naming every op it can dispatch; those ops
//     become its op set and it is a site for each of them, carrying their
//     per-code declarations like any other site. Default-deny: a generic
//     dispatcher with no such line fails at its header; a line naming an
//     unregistered op fails; a line in a function with no non-literal lookup
//     fails (stale — a literal lookup is already a site by itself). The
//     `descriptorFor` accessor's own definition is the lookup, not a
//     dispatch through it, and is skipped. FACET is
//     one site for every op whose `OpMetaSpec.Dispatch` is non-nil:
//     edge-manifest's `edgeOpCatalog` lens projects every described op, and
//     Facet's generic renderer offers whatever that catalog holds — so the
//     descriptor IS the dispatch, and its declaration lives in the
//     `OpMetaSpec` composite literal in the owning package's Go source.
//  4. GOVERNED = the op has at least one non-exempt code AND its site count
//     (JS sites + Facet) is at least 2. One site has no sibling; the second
//     site is what arms the gate.
//
// THE DECLARATION binds where the courtesy lives, one per comment line;
// several codes sharing one verb and clause may share a line, comma-separated.
//
//	JS, anywhere inside the site's span or in the contiguous `//` block
//	directly above its first line:
//	    // refusal-courtesy: <Op>/<Code>[, <Code>…]: <verb> — <clause>
//	    // refusal-courtesy: <Op>/<Code>[, <Code>…]: see <fnName>
//	Go, inside the op's OpMetaSpec composite literal:
//	    // refusal-courtesy(facet): <Code>[, <Code>…]: <verb> — <clause>
//
// Verbs are a closed vocabulary. JS: `hide` (not rendered in the state) ·
// `disable` · `drop` (the picker omits the row) · `cap` (an input bound) ·
// `prefill` · `confirm` (asks first; the refusal is then the answer) ·
// `none` — why no courtesy · `unreachable` — why this site never reaches the
// branch · `see <fn>` (this function delegates to <fn>'s declaration; <fn>
// must be a top-level function that is itself a site for the op and carries
// a non-`see` declaration for the code — one hop, never a chain). Facet:
// `hide` · `drop` · `cap` · `none` · `unreachable` (the generic form can only
// hide, drop a row on a column the entity lens projects, or bound a control
// from the InputSchema's own `minimum`/`maximum`/`enum`). The clause is
// mandatory for every verb — it names the gate/column/filter, so a reviewer
// can open it. The em dash `—` separates verb from clause; ` - ` (a
// hyphen-minus with a space each side) is accepted as its ASCII spelling.
//
// VERDICTS (each `path:line: refusal-courtesy: …`, with the exact
// declaration to write). For a governed op: a JS site missing declarations
// for any of its codes fails ONCE at the site's first line, naming every
// missing code; a described op whose OpMetaSpec literal misses `(facet)`
// declarations fails once at the literal's line the same way. For EVERY
// declaration, governed or not (a declaration must stay true), each code it
// names is checked on its own: naming a code the op cannot raise fails
// (stale — the op's full scoped code set, exempt codes included, is what it
// is checked against); a JS declaration inside a statement that is not a
// site for that op fails; a bad verb, an empty clause, a `see` to a
// non-site or to a function whose own declaration for the code is `see`
// fails; a `(facet)` declaration on an op whose Dispatch is nil fails. An
// ungoverned op carries no missing-declaration failures.
//
// UNMODELLED fail sites are FINDINGS, not silence. A `fail(` whose leftmost
// operand is an identifier that is not a parameter bound to a string literal
// on the edge that reached it — a local variable (`code = pick(); fail(code +
// …)`), or a parameter bound at some call site to an expression — or a
// format literal whose code is an argument (`fail("%s: …" % (code, x))`)
// names no code on that edge, so the refusal it raises is invisible to every
// site; the gate reports it (STRICT fails) rather than reading the corpus as
// clean around it. The corpus carries none today.
//
// BOUNDARY (stated, not hidden). The gate's grain is the dispatching
// FUNCTION, not the form: one function feeding two pickers (wellness
// `bookMemberIn`) carries one declaration for both, and a reviewer reads
// the function to see which. Scoping is by op NAME while the Processor
// routes by (op, class): an op name shared by two describing packages under
// different classes would be attributed to whichever package's descriptor
// the registry holds — none live today, and lint-package-standard's S11
// (no operationType described by two packages) is what keeps it so. A described op whose OWN
// package's scripts never dispatch it falls back to the union (its
// describing package has no script to scope to). A dispatch block the
// walker does not recognise (dict dispatch, a block nested under a `for`)
// attributes nothing — VERBOSE=1 lists every described op no dispatch block
// names. A helper shared by several ops over-attributes its codes to an op
// that never reaches the branch; `unreachable — <why>` is the honest
// declaration there, not a silent exemption. A string literal equal to an
// op name that is not a dispatch (a label, a status value spelled like an
// op) makes its statement a site — the declaration for it is `unreachable —
// <not a dispatch>`; the gate does not read intent. A generic dispatcher's
// op set is what its `refusal-courtesy-dispatches:` line declares — the
// gate checks the names are registered, not that the list is complete; a
// dispatcher that gains an op the line omits is the author's omission, and
// the line sits beside the code that would reveal it. Facet's site is the
// descriptor, not `cmd/facet`'s own source; a hand-built form in Facet
// outside the descriptor path is not modelled.
//
// Run: `go run ./scripts/lint-refusal-courtesy.go` (`STRICT=1` to fail;
// `--list` prints the governed census (one block per op) and exits;
// `--selftest` runs the mutation battery verbosely; `VERBOSE=1` prints the
// described ops no dispatch block names).
package main

import (
	"fmt"
	"go/ast"
	goparser "go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	jsast "github.com/dop251/goja/ast"
	"github.com/dop251/goja/file"
	jsparser "github.com/dop251/goja/parser"
	"go.starlark.net/syntax"

	"github.com/operatinggraph/lattice/internal/pkgregistry"
)

const gate = "lint-refusal-courtesy"

var (
	jsVerbs    = map[string]bool{"hide": true, "disable": true, "drop": true, "cap": true, "prefill": true, "confirm": true, "none": true, "unreachable": true}
	facetVerbs = map[string]bool{"hide": true, "drop": true, "cap": true, "none": true, "unreachable": true}
)

// exemptExact is the closed list of codes whose courtesy is not per-form
// (header, rule 2); exemptPrefix covers the Unknown* / NotA* families.
var (
	exemptExact  = map[string]bool{"InvalidArgument": true, "WrongClass": true, "AuthDenied": true, "PermissionDenied": true, "NotFound": true}
	exemptPrefix = regexp.MustCompile(`^(Unknown|NotA)[A-Z]`)
	codeToken    = regexp.MustCompile(`^[A-Za-z]+$`)
)

func exempt(code string) bool {
	return exemptExact[code] || exemptPrefix.MatchString(code) || !codeToken.MatchString(code)
}

func main() {
	strict := os.Getenv("STRICT") == "1"
	verbose := os.Getenv("VERBOSE") == "1"
	list, selftest := false, false
	for _, a := range os.Args[1:] {
		switch a {
		case "--list":
			list = true
		case "--selftest":
			selftest = true
		}
	}
	if selftest {
		runSelfTest()
		return
	}

	c := newCorpus()
	descs := map[string]descriptor{}
	var findings []string
	for _, name := range pkgregistry.Names() {
		def, ok := pkgregistry.Lookup(name)
		if !ok {
			findings = append(findings, fmt.Sprintf("%s: pkgregistry.Names() lists this package but Lookup does not resolve it — the corpus enumeration and the registry disagree, so this run cannot claim to have checked it", name))
			continue
		}
		for _, m := range def.OpMetas {
			descs[m.OperationType] = descriptor{op: m.OperationType, pkg: name, dispatch: m.Dispatch != nil}
		}
		seen := map[string]bool{}
		for _, d := range def.DDLs {
			if strings.TrimSpace(d.Script) == "" || seen[d.Script] {
				continue
			}
			seen[d.Script] = true
			if err := c.scanScript(name, d.CanonicalName, d.Script); err != nil {
				// The Processor would refuse this script too; the parse
				// error is the package's own test failure.
				continue
			}
		}
	}
	if c.scripts == 0 {
		findings = append(findings, gate+": examined ZERO scripts — the corpus walk is broken, and a gate that checked nothing has no all-clear to give")
	}

	registered := registeredOps(c, descs)
	apps, _ := filepath.Glob("cmd/*-app/web/app.js")
	sort.Strings(apps)
	var files []*jsFile
	for _, path := range apps {
		src, err := os.ReadFile(path)
		if err != nil {
			findings = append(findings, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		f, err := scanJS(path, string(src), registered)
		if err != nil {
			findings = append(findings, fmt.Sprintf("%s: goja could not parse the file — the FE tests execute it under the same engine, so this is the app's own failure, not this gate's: %v", path, err))
			continue
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		findings = append(findings, gate+": examined ZERO app files — the glob is broken, and a gate that checked nothing has no all-clear to give")
	}

	var lits []goLiteral
	pkgs := map[string]bool{}
	for _, d := range descs {
		pkgs[d.pkg] = true
	}
	for pkg := range pkgs {
		srcs := map[string]string{}
		paths, _ := filepath.Glob(filepath.Join("packages", pkg, "*.go"))
		for _, p := range paths {
			if strings.HasSuffix(p, "_test.go") {
				continue
			}
			b, err := os.ReadFile(p)
			if err != nil {
				findings = append(findings, fmt.Sprintf("%s: %v", p, err))
				continue
			}
			srcs[p] = string(b)
		}
		got, errs := scanGoSources(srcs)
		findings = append(findings, errs...)
		lits = append(lits, got...)
	}

	res := judge(c, descs, files, lits)
	findings = append(findings, res.findings...)

	if list {
		printCensus(res)
		return
	}
	if verbose {
		var missing []string
		for op := range descs {
			if !c.dispatched[op] {
				missing = append(missing, op)
			}
		}
		sort.Strings(missing)
		for _, op := range missing {
			fmt.Printf("  UNRECOGNISED-DISPATCH %s (%s): described, but no dispatch block names it\n", op, descs[op].pkg)
		}
	}
	for _, f := range findings {
		fmt.Println(f)
	}
	summary := fmt.Sprintf("%d script(s); governed ops=%d pairs=%d js-sites=%d facet-sites=%d; %d declaration(s) read",
		c.scripts, res.governedOps, res.governedPairs, res.jsSites, res.facetSites, res.declarations)
	if len(findings) == 0 {
		fmt.Printf("%s: clean — %s\n", gate, summary)
		return
	}
	fmt.Printf("%s: %d issue(s) — %s\n", gate, len(findings), summary)
	if strict {
		os.Exit(1)
	}
}

// --- op → codes (Starlark) ---

// codeLoc is where one (op, code) was first attributed: the script's
// corpus location and the fail's line within the script body.
type codeLoc struct {
	pkg, ddl string
	line     int
}

func (l codeLoc) String() string {
	return fmt.Sprintf("packages/%s (DDL %s) script:%d", l.pkg, l.ddl, l.line)
}

// short renders the location relative to a package already named.
func (l codeLoc) short(pkg string) string {
	if l.pkg == pkg {
		return fmt.Sprintf("%s:%d", l.ddl, l.line)
	}
	return fmt.Sprintf("%s/%s:%d", l.pkg, l.ddl, l.line)
}

// corpus accumulates the op → code attribution across every script, keyed
// by the package whose script raised it.
type corpus struct {
	byPkg      map[string]map[string]map[string]codeLoc // pkg → op → code → first site (exempt codes included)
	dispatched map[string]bool                          // every op a dispatch block names
	unmodelled []string
	scripts    int
}

func newCorpus() *corpus {
	return &corpus{byPkg: map[string]map[string]map[string]codeLoc{}, dispatched: map[string]bool{}}
}

// codesOf is an op's code set: the describing package's scripts when the op
// is described (and that package dispatches it), else the union of every
// package that dispatches it — a bare op name is global, and two verticals'
// ledgers both dispatch `DebitAccount`, but a site that dispatches the
// described op reaches the describing package's script.
func (c *corpus) codesOf(op string, descs map[string]descriptor) map[string]codeLoc {
	if d, ok := descs[op]; ok {
		if codes := c.byPkg[d.pkg][op]; len(codes) > 0 {
			return codes
		}
	}
	out := map[string]codeLoc{}
	pkgs := make([]string, 0, len(c.byPkg))
	for pkg := range c.byPkg {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)
	for _, pkg := range pkgs {
		for code, loc := range c.byPkg[pkg][op] {
			if _, seen := out[code]; !seen {
				out[code] = loc
			}
		}
	}
	return out
}

// failSite is one fail(...) call: the code when its first argument's
// leftmost operand is a string literal, else the identifier that operand is
// (resolved through the caller's bindings when it is a parameter), and the
// line.
type failSite struct {
	code  string
	ident string
	why   string // what the leftmost operand is, for the unmodelled message
	line  int
}

// callEdge is one call from a def to a named function with the arguments
// that can bind the callee's parameters: positional and keyword.
type callEdge struct {
	name   string
	args   []syntax.Expr
	kwargs map[string]syntax.Expr
	line   int
}

// starFn is one script def: its parameter names, direct fails, and calls.
type starFn struct {
	params []string
	fails  []failSite
	calls  []callEdge
}

// scanScript attributes every reachable fail code to the dispatch blocks that
// reach it, under the package whose script this is; ddl names the script.
func (c *corpus) scanScript(pkg, ddl, src string) error {
	f, err := syntax.Parse("script.star", src, 0)
	if err != nil {
		return err
	}
	c.scripts++
	// Every def, top-level or nested, keyed by name; two defs sharing a name
	// both contribute (over-attribution errs toward a finding).
	fns := map[string][]*starFn{}
	var defs []*syntax.DefStmt
	for _, s := range f.Stmts {
		syntax.Walk(s, func(n syntax.Node) bool {
			if d, ok := n.(*syntax.DefStmt); ok {
				defs = append(defs, d)
			}
			return true
		})
	}
	for _, d := range defs {
		fn := &starFn{params: paramNames(d.Params)}
		for _, s := range d.Body {
			collectFails(s, fn)
		}
		fns[d.Name.Name] = append(fns[d.Name.Name], fn)
	}
	if c.byPkg[pkg] == nil {
		c.byPkg[pkg] = map[string]map[string]codeLoc{}
	}
	for _, d := range defs {
		walkDispatch(d.Body, func(op string, body []syntax.Stmt) {
			c.dispatched[op] = true
			root := &starFn{}
			for _, s := range body {
				collectFails(s, root)
			}
			for _, fs := range closure(root, fns) {
				if fs.code == "" {
					c.unmodelled = append(c.unmodelled, fmt.Sprintf("packages/%s:0: refusal-courtesy: unmodelled — DDL %s script:%d: fail() in %s's reach whose first argument's leftmost operand is %s, not a code literal and not a parameter bound to one at the call site; no code can be read, so the refusal is invisible to every site — spell the code as the leftmost string literal, or bind the helper's parameter to a literal at the call", pkg, ddl, fs.line, op, fs.why))
					continue
				}
				if c.byPkg[pkg][op] == nil {
					c.byPkg[pkg][op] = map[string]codeLoc{}
				}
				if _, ok := c.byPkg[pkg][op][fs.code]; !ok {
					c.byPkg[pkg][op][fs.code] = codeLoc{pkg: pkg, ddl: ddl, line: fs.line}
				}
			}
		})
	}
	return nil
}

// paramNames reads a def's parameter names in order (`x`, `x=default`,
// `*args`, `**kwargs`).
func paramNames(params []syntax.Expr) []string {
	out := make([]string, 0, len(params))
	for _, p := range params {
		switch x := p.(type) {
		case *syntax.Ident:
			out = append(out, x.Name)
		case *syntax.BinaryExpr:
			if id, ok := x.X.(*syntax.Ident); ok && x.Op == syntax.EQ {
				out = append(out, id.Name)
			} else {
				out = append(out, "")
			}
		case *syntax.UnaryExpr:
			if id, ok := x.X.(*syntax.Ident); ok {
				out = append(out, id.Name)
			} else {
				out = append(out, "")
			}
		default:
			out = append(out, "")
		}
	}
	return out
}

// collectFails records every fail(...) and every call to a named function
// under a statement, nested defs / lambdas / comprehensions included.
func collectFails(n syntax.Node, fn *starFn) {
	syntax.Walk(n, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fn.(*syntax.Ident)
		if !ok {
			return true
		}
		if id.Name == "fail" {
			site := failSite{line: int(call.Lparen.Line), why: "absent"}
			if len(call.Args) > 0 {
				site.code, site.ident, site.why = failCode(call.Args[0])
			}
			fn.fails = append(fn.fails, site)
			return true
		}
		edge := callEdge{name: id.Name, kwargs: map[string]syntax.Expr{}, line: int(call.Lparen.Line)}
		for _, a := range call.Args {
			if b, ok := a.(*syntax.BinaryExpr); ok && b.Op == syntax.EQ {
				if k, ok := b.X.(*syntax.Ident); ok {
					edge.kwargs[k.Name] = b.Y
					continue
				}
			}
			edge.args = append(edge.args, a)
		}
		fn.calls = append(fn.calls, edge)
		return true
	})
}

// failCode reads the code off a fail's first argument: the leftmost operand
// of a `+` / `%` chain (or the bare argument). A string literal yields its
// text up to the first `:`; an identifier yields its name for the caller's
// bindings to resolve; anything else yields neither. why describes the
// operand for the unmodelled message.
func failCode(e syntax.Expr) (code, ident, why string) {
	for {
		switch x := e.(type) {
		case *syntax.ParenExpr:
			e = x.X
			continue
		case *syntax.BinaryExpr:
			if x.Op == syntax.PLUS || x.Op == syntax.PERCENT {
				e = x.X
				continue
			}
			return "", "", "an expression"
		case *syntax.Literal:
			code := codeOfLiteral(x)
			if strings.ContainsAny(code, "%{") {
				// `fail("%s: …" % (code, x))` — the code is a format
				// argument this reader does not evaluate.
				return "", "", "the format literal " + x.Raw
			}
			return code, "", ""
		case *syntax.Ident:
			return "", x.Name, "`" + x.Name + "`"
		}
		return "", "", "an expression"
	}
}

// codeOfLiteral is a string literal's text up to the first `:`, trimmed;
// "" for a non-string literal.
func codeOfLiteral(l *syntax.Literal) string {
	if l.Token != syntax.STRING {
		return ""
	}
	s, _ := l.Value.(string)
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// frame is one function under the closure walk with the codes its
// parameters are bound to at the edge that reached it ("" = bound to a
// non-literal, so unresolvable on this edge).
type frame struct {
	fn       *starFn
	bindings map[string]string
}

// closure returns every fail reachable from root through the call graph,
// resolving a parameter-carried code through the bindings each call edge
// establishes (a literal argument by position or keyword; a caller's own
// bound parameter forwarded to the callee, any depth).
func closure(root *starFn, fns map[string][]*starFn) []failSite {
	var out []failSite
	seen := map[string]bool{}
	queue := []frame{{fn: root, bindings: map[string]string{}}}
	for len(queue) > 0 {
		fr := queue[0]
		queue = queue[1:]
		key := fmt.Sprintf("%p|%s", fr.fn, bindingsKey(fr.bindings))
		if seen[key] {
			continue
		}
		seen[key] = true
		for _, fs := range fr.fn.fails {
			if fs.ident != "" {
				if code, ok := fr.bindings[fs.ident]; ok && code != "" {
					fs.code = code
				}
			}
			out = append(out, fs)
		}
		for _, edge := range fr.fn.calls {
			for _, callee := range fns[edge.name] {
				queue = append(queue, frame{fn: callee, bindings: bindArgs(callee, edge, fr.bindings)})
			}
		}
	}
	return out
}

// bindArgs computes the callee's parameter bindings for one call edge: a
// string-literal argument binds its code; an identifier argument the caller
// has itself bound forwards that binding; anything else binds "" (present
// but unresolvable, so the callee's fail stays unmodelled on this edge).
func bindArgs(callee *starFn, edge callEdge, caller map[string]string) map[string]string {
	out := map[string]string{}
	bind := func(param string, arg syntax.Expr) {
		if param == "" {
			return
		}
		switch x := arg.(type) {
		case *syntax.Literal:
			out[param] = codeOfLiteral(x)
		case *syntax.Ident:
			out[param] = caller[x.Name]
		default:
			out[param] = ""
		}
	}
	for i, a := range edge.args {
		if i < len(callee.params) {
			bind(callee.params[i], a)
		}
	}
	for name, a := range edge.kwargs {
		bind(name, a)
	}
	return out
}

func bindingsKey(b map[string]string) string {
	keys := make([]string, 0, len(b))
	for k := range b {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k + "=" + b[k] + ";")
	}
	return sb.String()
}

// walkDispatch, opNames, isOpTypeExpr and stringLit are copied verbatim from
// scripts/lint-opmeta-required-fields.go (a `//go:build ignore` main cannot
// be imported) so the two gates read the same dispatch shapes.

// walkDispatch visits every `if <opExpr> == "X":` (and `or`-joined /
// `in [...]` forms) in a statement list, recursively through if/elif chains,
// calling fn with each op name the condition names and the block's body.
func walkDispatch(stmts []syntax.Stmt, fn func(op string, body []syntax.Stmt)) {
	for _, s := range stmts {
		ifs, ok := s.(*syntax.IfStmt)
		if !ok {
			continue
		}
		for _, op := range opNames(ifs.Cond) {
			fn(op, ifs.True)
		}
		walkDispatch(ifs.True, fn)
		walkDispatch(ifs.False, fn)
	}
}

// opNames reads the operation names a dispatch condition compares against:
// `ot == "X"`, `op.operationType == "X"`, `ot == "X" or ot == "Y"`,
// `ot in ["X", "Y"]`. Anything else names no op.
func opNames(e syntax.Expr) []string {
	switch c := e.(type) {
	case *syntax.ParenExpr:
		return opNames(c.X)
	case *syntax.BinaryExpr:
		switch c.Op {
		case syntax.OR:
			return append(opNames(c.X), opNames(c.Y)...)
		case syntax.EQL:
			if isOpTypeExpr(c.X) {
				if lit := stringLit(c.Y); lit != "" {
					return []string{lit}
				}
			}
			if isOpTypeExpr(c.Y) {
				if lit := stringLit(c.X); lit != "" {
					return []string{lit}
				}
			}
		case syntax.IN:
			if isOpTypeExpr(c.X) {
				if l, ok := c.Y.(*syntax.ListExpr); ok {
					var out []string
					for _, el := range l.List {
						if lit := stringLit(el); lit != "" {
							out = append(out, lit)
						}
					}
					return out
				}
			}
		}
	}
	return nil
}

// isOpTypeExpr recognises the corpus's operation-type spellings: the `ot`
// binding, or `op.operationType` read directly.
func isOpTypeExpr(e syntax.Expr) bool {
	switch x := e.(type) {
	case *syntax.Ident:
		return x.Name == "ot"
	case *syntax.DotExpr:
		if id, ok := x.X.(*syntax.Ident); ok {
			return id.Name == "op" && x.Name.Name == "operationType"
		}
	}
	return false
}

func stringLit(e syntax.Expr) string {
	if l, ok := e.(*syntax.Literal); ok && l.Token == syntax.STRING {
		if s, ok := l.Value.(string); ok {
			return s
		}
	}
	return ""
}

// --- descriptors ---

// descriptor is one op-meta's identity: its owning package and whether it
// carries a Dispatch (and so is offered by Facet).
type descriptor struct {
	op, pkg  string
	dispatch bool
}

// registeredOps is the set of op names a JS string / member can name: every
// described op plus every op a dispatch block names.
func registeredOps(c *corpus, descs map[string]descriptor) map[string]bool {
	out := map[string]bool{}
	for op := range descs {
		out[op] = true
	}
	for op := range c.dispatched {
		out[op] = true
	}
	return out
}

// --- JS sites + declarations ---

// declaration is one parsed `refusal-courtesy` comment line.
type declaration struct {
	line   int
	op     string   // "" for a (facet) declaration — the literal supplies it
	codes  []string // one or more codes sharing the verb + clause
	verb   string   // "see" for a delegation
	clause string   // the see target for verb "see"
	bad    string   // a parse problem, reported as a verdict
}

// splitCodes reads the grouped `CodeA, CodeB` spelling.
func splitCodes(list string) []string {
	var out []string
	for _, c := range strings.Split(list, ",") {
		out = append(out, strings.TrimSpace(c))
	}
	return out
}

// jsSite is one top-level statement and the registered ops it names.
type jsSite struct {
	name       string // the function name, or (statement@line)
	isFunction bool
	line, end  int
	ops        map[string]bool
	decls      []declaration
	// genericAt is the line of the first catalog lookup with a non-literal
	// op argument (0 when none) — the generic-dispatcher evidence.
	genericAt int
	// dispatches is every op a `refusal-courtesy-dispatches:` line names,
	// with the line it sits on.
	dispatches []dispatchesLine
}

// dispatchesLine is one parsed `// refusal-courtesy-dispatches: Op[, Op…]`.
type dispatchesLine struct {
	line int
	ops  []string
	bad  string
}

// jsFile is one parsed app.js.
type jsFile struct {
	path     string
	sites    []*jsSite // every top-level statement, site or not
	fns      map[string]*jsSite
	unbound  []declaration // declaration lines attached to no statement
	appLabel string
	// unboundDispatches are dispatcher lines attached to no statement.
	unboundDispatches []dispatchesLine
}

func (s *jsSite) markGeneric(line int) {
	if s.genericAt == 0 || line < s.genericAt {
		s.genericAt = line
	}
}

func isStringLiteral(e jsast.Expression) bool {
	_, ok := e.(*jsast.StringLiteral)
	return ok
}

// memberName is the identifier a bracket lookup is taken on: `catalog[x]`
// → catalog, `state.opCatalog[x]` → opCatalog, anything else → "".
func memberName(e jsast.Expression) string {
	switch x := e.(type) {
	case *jsast.Identifier:
		return x.Name.String()
	case *jsast.DotExpression:
		return x.Identifier.Name.String()
	}
	return ""
}

var (
	jsDeclLooseRe = regexp.MustCompile(`^\s*//\s*refusal-courtesy(\(facet\))?:\s*(.*?)\s*$`)
	dispatchesRe  = regexp.MustCompile(`^\s*//\s*refusal-courtesy-dispatches:\s*(.*?)\s*$`)
	opListRe      = regexp.MustCompile(`^[A-Za-z]+(?:,\s*[A-Za-z]+)*$`)
	jsDeclRe      = regexp.MustCompile(`^([A-Za-z]+)/([A-Za-z]+(?:,\s*[A-Za-z]+)*):\s*(.*)$`)
	goDeclRe      = regexp.MustCompile(`^//\s*refusal-courtesy\(facet\):\s*(.*?)\s*$`)
	goDeclBodyRe  = regexp.MustCompile(`^([A-Za-z]+(?:,\s*[A-Za-z]+)*):\s*(.*)$`)
	seeRe         = regexp.MustCompile(`^see\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*$`)
	verbClauseRe  = regexp.MustCompile(`^([a-z]+)\s+(?:—|-)\s+(\S.*)$`)
	commentLineRe = regexp.MustCompile(`^\s*//`)
)

// parseVerbClause reads `<verb> — <clause>` (or `see <fn>` when allowSee).
func parseVerbClause(rest string, allowSee bool) (verb, clause, bad string) {
	if m := seeRe.FindStringSubmatch(rest); m != nil {
		if !allowSee {
			return "", "", "`see` is a JS-only verb"
		}
		return "see", m[1], ""
	}
	m := verbClauseRe.FindStringSubmatch(rest)
	if m == nil {
		return "", "", "expected `<verb> — <clause>` (or `<verb> - <clause>`) with a non-empty clause"
	}
	return m[1], strings.TrimSpace(m[2]), ""
}

// scanJS parses one app source: every top-level statement, the registered
// ops its span names, and every declaration line bound to it.
func scanJS(path, src string, registered map[string]bool) (*jsFile, error) {
	fs := &file.FileSet{}
	prog, err := jsparser.ParseFile(fs, path, parseable(src), 0)
	if err != nil {
		return nil, err
	}
	f := &jsFile{path: path, fns: map[string]*jsSite{}, appLabel: appLabel(path)}
	excluded := knownCatalogLiterals(prog)
	for _, s := range prog.Body {
		site := &jsSite{
			line: fs.Position(s.Idx0()).Line,
			end:  fs.Position(s.Idx1()).Line,
			ops:  map[string]bool{},
		}
		if d, ok := s.(*jsast.FunctionDeclaration); ok && d.Function.Name != nil {
			site.name = d.Function.Name.Name.String()
			site.isFunction = true
			f.fns[site.name] = site
		} else {
			site.name = fmt.Sprintf("(statement@%d)", site.line)
		}
		walkJS(s, func(n jsast.Node) bool {
			switch x := n.(type) {
			case *jsast.StringLiteral:
				if v := x.Value.String(); registered[v] && !excluded[x] {
					site.ops[v] = true
				}
			case *jsast.DotExpression:
				if v := x.Identifier.Name.String(); registered[v] {
					site.ops[v] = true
				}
			case *jsast.CallExpression:
				if id, ok := x.Callee.(*jsast.Identifier); ok && id.Name.String() == "descriptorFor" && len(x.ArgumentList) > 0 && !isStringLiteral(x.ArgumentList[0]) {
					site.markGeneric(fs.Position(x.LeftParenthesis).Line)
				}
			case *jsast.BracketExpression:
				if strings.Contains(memberName(x.Left), "atalog") && !isStringLiteral(x.Member) {
					site.markGeneric(fs.Position(x.LeftBracket).Line)
				}
			}
			return true
		})
		// The catalog accessor's own definition is the lookup, not a
		// dispatch through it.
		if site.name == "descriptorFor" {
			site.genericAt = 0
		}
		f.sites = append(f.sites, site)
	}
	lines := strings.Split(src, "\n")
	for i, raw := range lines {
		if m := dispatchesRe.FindStringSubmatch(raw); m != nil {
			dl := dispatchesLine{line: i + 1}
			if !opListRe.MatchString(m[1]) {
				dl.bad = "expected `// refusal-courtesy-dispatches: <Op>[, <Op>…]`"
			} else {
				dl.ops = splitCodes(m[1])
			}
			if site := f.bind(dl.line, lines); site != nil {
				site.dispatches = append(site.dispatches, dl)
				for _, op := range dl.ops {
					if registered[op] {
						site.ops[op] = true
					}
				}
			} else {
				f.unboundDispatches = append(f.unboundDispatches, dl)
			}
			continue
		}
		m := jsDeclLooseRe.FindStringSubmatch(raw)
		if m == nil {
			continue
		}
		d := declaration{line: i + 1}
		switch {
		case m[1] != "":
			d.bad = "`(facet)` declarations live in the op's OpMetaSpec literal in Go, not in app.js"
		default:
			b := jsDeclRe.FindStringSubmatch(m[2])
			if b == nil {
				d.bad = "expected `<Op>/<Code>[, <Code>…]: <verb> — <clause>` or `<Op>/<Code>: see <fn>`"
			} else {
				d.op, d.codes = b[1], splitCodes(b[2])
				d.verb, d.clause, d.bad = parseVerbClause(b[3], true)
			}
		}
		if site := f.bind(d.line, lines); site != nil {
			site.decls = append(site.decls, d)
		} else {
			f.unbound = append(f.unbound, d)
		}
	}
	return f, nil
}

// bind maps a declaration line to the top-level statement whose span holds
// it, or whose header the contiguous comment block holding it sits above.
func (f *jsFile) bind(line int, lines []string) *jsSite {
	for _, s := range f.sites {
		if s.line <= line && line <= s.end {
			return s
		}
	}
	// Walk down through the comment block to the first code line.
	next := line
	for next < len(lines) && commentLineRe.MatchString(lines[next]) {
		next++
	}
	next++ // 1-based line of the first non-comment line below the block
	for _, s := range f.sites {
		if s.line == next {
			return s
		}
	}
	return nil
}

// knownCatalogLiterals returns the string literals that are elements of the
// array initializer bound to KNOWN_CATALOG_OPS — the prefetch list, not a
// dispatch.
func knownCatalogLiterals(prog *jsast.Program) map[*jsast.StringLiteral]bool {
	out := map[*jsast.StringLiteral]bool{}
	mark := func(b *jsast.Binding) {
		id, ok := b.Target.(*jsast.Identifier)
		if !ok || id.Name.String() != "KNOWN_CATALOG_OPS" {
			return
		}
		arr, ok := b.Initializer.(*jsast.ArrayLiteral)
		if !ok {
			return
		}
		for _, el := range arr.Value {
			if s, ok := el.(*jsast.StringLiteral); ok {
				out[s] = true
			}
		}
	}
	for _, s := range prog.Body {
		switch d := s.(type) {
		case *jsast.LexicalDeclaration:
			for _, b := range d.List {
				mark(b)
			}
		case *jsast.VariableStatement:
			for _, b := range d.List {
				mark(b)
			}
		}
	}
	return out
}

func appLabel(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	if len(parts) >= 2 {
		return parts[1]
	}
	return path
}

// parseable rewrites the one syntax goja does not parse — the dynamic
// `import(...)` each app uses to load the shared descriptor-form module — into
// an ordinary call of the same length with the same argument list, so the
// rest of the file parses unchanged and every position still indexes the
// original source. (Copied from scripts/lint-ceremony-throw-path.go.)
func parseable(src string) string {
	return dynamicImportRe.ReplaceAllString(src, "${1}IMPORT(")
}

var dynamicImportRe = regexp.MustCompile(`(^|[^A-Za-z0-9_$.])import\(`)

// walkJS visits every node reachable from root, depth-first; fn returning
// false prunes the subtree. goja's ast ships no visitor, so this one is
// reflective (the scripts/lint-ceremony-throw-path.go shape): it follows
// exported pointer, slice and interface fields whose values are ast nodes. A
// function literal's DeclarationList aliases bindings already in its body
// and is skipped so nothing is visited twice.
func walkJS(root jsast.Node, fn func(jsast.Node) bool) {
	walkJSValue(reflect.ValueOf(root), fn)
}

var jsNodeType = reflect.TypeOf((*jsast.Node)(nil)).Elem()

func walkJSValue(v reflect.Value, fn func(jsast.Node) bool) {
	if !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return
		}
		walkJSValue(v.Elem(), fn)
	case reflect.Ptr:
		if v.IsNil() {
			return
		}
		if v.Type().Implements(jsNodeType) {
			if !fn(v.Interface().(jsast.Node)) {
				return
			}
		}
		walkJSValue(v.Elem(), fn)
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() || f.Name == "DeclarationList" {
				continue
			}
			walkJSValue(v.Field(i), fn)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			walkJSValue(v.Index(i), fn)
		}
	}
}

// --- Go OpMetaSpec literals + (facet) declarations ---

// goLiteral is one OpMetaSpec composite literal and the (facet) declarations
// between its braces.
type goLiteral struct {
	file  string
	line  int
	op    string
	decls []declaration
}

// scanGoSources finds every OpMetaSpec composite literal across a package's
// sources (name → source) and reads the declarations inside each. An
// OperationType spelled as a package-level string constant is resolved.
func scanGoSources(srcs map[string]string) ([]goLiteral, []string) {
	fset := token.NewFileSet()
	type parsed struct {
		name string
		f    *ast.File
	}
	var files []parsed
	var errs []string
	consts := map[string]string{}
	names := make([]string, 0, len(srcs))
	for n := range srcs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f, err := goparser.ParseFile(fset, n, srcs[n], goparser.ParseComments)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", n, err))
			continue
		}
		files = append(files, parsed{n, f})
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
				continue
			}
			for _, sp := range gd.Specs {
				vs, ok := sp.(*ast.ValueSpec)
				if !ok || len(vs.Names) != len(vs.Values) {
					continue
				}
				for i, id := range vs.Names {
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if s, err := strconv.Unquote(lit.Value); err == nil {
							consts[id.Name] = s
						}
					}
				}
			}
		}
	}
	var out []goLiteral
	for _, p := range files {
		elems := map[*ast.CompositeLit]bool{} // untyped elements of an OpMetaSpec slice
		ast.Inspect(p.f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if at, ok := lit.Type.(*ast.ArrayType); ok && isOpMetaSpecType(at.Elt) {
				for _, el := range lit.Elts {
					if cl, ok := el.(*ast.CompositeLit); ok && cl.Type == nil {
						elems[cl] = true
					}
				}
			}
			return true
		})
		ast.Inspect(p.f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !(isOpMetaSpecType(lit.Type) || elems[lit]) {
				return true
			}
			op := ""
			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "OperationType" {
					continue
				}
				switch v := kv.Value.(type) {
				case *ast.BasicLit:
					if v.Kind == token.STRING {
						op, _ = strconv.Unquote(v.Value)
					}
				case *ast.Ident:
					op = consts[v.Name]
				}
			}
			if op == "" {
				return true
			}
			g := goLiteral{file: p.name, line: fset.Position(lit.Lbrace).Line, op: op}
			for _, cg := range p.f.Comments {
				for _, c := range cg.List {
					if c.Slash <= lit.Lbrace || c.Slash >= lit.Rbrace {
						continue
					}
					m := goDeclRe.FindStringSubmatch(c.Text)
					if m == nil {
						continue
					}
					d := declaration{line: fset.Position(c.Slash).Line}
					if b := goDeclBodyRe.FindStringSubmatch(m[1]); b == nil {
						d.bad = "expected `<Code>[, <Code>…]: <verb> — <clause>`"
					} else {
						d.codes = splitCodes(b[1])
						d.verb, d.clause, d.bad = parseVerbClause(b[2], false)
					}
					g.decls = append(g.decls, d)
				}
			}
			out = append(out, g)
			return true
		})
	}
	return out, errs
}

func isOpMetaSpecType(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.SelectorExpr:
		return t.Sel.Name == "OpMetaSpec"
	case *ast.Ident:
		return t.Name == "OpMetaSpec"
	}
	return false
}

// --- the judgement ---

// result is the verdict list plus the governed census.
type result struct {
	findings      []string
	census        []censusRow
	governedOps   int
	governedPairs int
	jsSites       int
	facetSites    int
	declarations  int
}

// censusRow is one governed op: its state codes with where each was
// attributed, and its sites.
type censusRow struct {
	op, pkg string
	codes   []string
	locs    map[string]codeLoc
	sites   []string
	facet   bool
}

// judge applies the rule: governed pairs need a declaration at every site;
// every declaration, governed or not, must be true.
func judge(c *corpus, descs map[string]descriptor, files []*jsFile, lits []goLiteral) result {
	var r result
	fail := func(path string, line int, msg string) {
		r.findings = append(r.findings, fmt.Sprintf("%s:%d: refusal-courtesy: %s", path, line, msg))
	}

	// Sites per op.
	jsSitesOf := map[string][]*jsSite{}
	fileOf := map[*jsSite]*jsFile{}
	for _, f := range files {
		for _, s := range f.sites {
			fileOf[s] = f
			for op := range s.ops {
				jsSitesOf[op] = append(jsSitesOf[op], s)
			}
		}
	}
	litsOf := map[string][]goLiteral{}
	for _, l := range lits {
		litsOf[l.op] = append(litsOf[l.op], l)
	}
	for op := range litsOf {
		sort.Slice(litsOf[op], func(i, j int) bool {
			a, b := litsOf[op][i], litsOf[op][j]
			if a.file != b.file {
				return a.file < b.file
			}
			return a.line < b.line
		})
	}
	facetSite := func(op string) bool { return descs[op].dispatch }

	// Every op any script dispatches, with its scoped code set.
	opSet := map[string]bool{}
	for _, byOp := range c.byPkg {
		for op := range byOp {
			opSet[op] = true
		}
	}
	ops := make([]string, 0, len(opSet))
	for op := range opSet {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	codesOf := map[string]map[string]codeLoc{}
	for _, op := range ops {
		codesOf[op] = c.codesOf(op, descs)
	}

	// Governed census + missing declarations, one finding per (site, op).
	for _, op := range ops {
		var codes []string
		for code := range codesOf[op] {
			if !exempt(code) {
				codes = append(codes, code)
			}
		}
		sort.Strings(codes)
		siteCount := len(jsSitesOf[op])
		if facetSite(op) {
			siteCount++
		}
		if len(codes) == 0 || siteCount < 2 {
			continue
		}
		r.governedOps++
		r.governedPairs += len(codes)
		r.jsSites += len(jsSitesOf[op])
		if facetSite(op) {
			r.facetSites++
		}
		row := censusRow{op: op, pkg: descs[op].pkg, codes: codes, locs: codesOf[op], facet: facetSite(op)}
		if row.pkg == "" {
			row.pkg = "undescribed"
		}
		for _, s := range jsSitesOf[op] {
			row.sites = append(row.sites, fmt.Sprintf("%s:%s:%d", fileOf[s].appLabel, s.name, s.line))
			var missing []string
			for _, code := range codes {
				if !hasDecl(s.decls, op, code) {
					missing = append(missing, code)
				}
			}
			if len(missing) > 0 {
				fail(fileOf[s].path, s.line, fmt.Sprintf("%s dispatches %s, which %d other site(s) also dispatch and whose script refuses with state codes this site declares no courtesy for — missing: %s — add one `// refusal-courtesy: %s/<Code>: <verb> — <clause>` line per code inside %s (codes may share a line as `%s/<CodeA>, <CodeB>: …`; verbs: hide | disable | drop | cap | prefill | confirm | none | unreachable; or `see <fn>` to delegate to a sibling that declares it); the script's refusals: %s",
					s.name, op, siteCount-1, strings.Join(missing, ", "), op, s.name, op, locList(missing, codesOf[op])))
			}
		}
		if facetSite(op) {
			ls := litsOf[op]
			if len(ls) == 0 {
				fail(fmt.Sprintf("packages/%s", descs[op].pkg), 0, fmt.Sprintf("%s is described with a Dispatch (so Facet's descriptor form is a site for it) and its script refuses with %s, but no OpMetaSpec composite literal with `OperationType: %q` was found in the package's Go sources — this gate cannot bind a `(facet)` declaration to it",
					op, strings.Join(codes, ", "), op))
			} else {
				var missing []string
				for _, code := range codes {
					if !hasFacetDecl(ls, code) {
						missing = append(missing, code)
					}
				}
				if len(missing) > 0 {
					fail(ls[0].file, ls[0].line, fmt.Sprintf("%s is offered by Facet's descriptor form (Dispatch is set), %d other site(s) also dispatch it, and its script refuses with state codes the descriptor declares no courtesy for — missing: %s — add one `// refusal-courtesy(facet): <Code>: <verb> — <clause>` line per code inside the OpMetaSpec literal (codes may share a line as `<CodeA>, <CodeB>: …`; verbs: hide | drop | cap | none | unreachable); the script's refusals: %s",
						op, siteCount-1, strings.Join(missing, ", "), locList(missing, codesOf[op])))
				}
			}
		}
		r.census = append(r.census, row)
	}

	// An unmodelled fail site is a finding: the refusal it hides is invisible
	// to every site, and the corpus reads clean.
	r.findings = append(r.findings, c.unmodelled...)

	// Generic dispatchers: a catalog lookup with a non-literal op must name
	// the ops it can dispatch; a dispatcher line must stay true.
	for _, f := range files {
		for _, dl := range f.unboundDispatches {
			fail(f.path, dl.line, "this `refusal-courtesy-dispatches:` line sits inside no top-level statement and above no statement header — move it inside the generic dispatcher it describes")
		}
		for _, s := range f.sites {
			if s.genericAt != 0 && len(s.dispatches) == 0 {
				fail(f.path, s.line, fmt.Sprintf("%s is a generic dispatcher — line %d resolves a catalog row from a non-literal op name, so the scanner cannot see which ops it dispatches — add `// refusal-courtesy-dispatches: <Op>[, <Op>…]` inside %s naming every op it can dispatch (it is then a site for each, and carries their per-code declarations)", s.name, s.genericAt, s.name))
			}
			for _, dl := range s.dispatches {
				if dl.bad != "" {
					fail(f.path, dl.line, "malformed dispatcher line — "+dl.bad)
					continue
				}
				if s.genericAt == 0 {
					fail(f.path, dl.line, fmt.Sprintf("stale — %s resolves no catalog row from a non-literal op name (no `descriptorFor(<expr>)` / `<…atalog…>[<expr>]`), so it is not a generic dispatcher; delete the `refusal-courtesy-dispatches:` line (a literal op name is already a site by itself)", s.name))
				}
				for _, op := range dl.ops {
					if _, ok := codesOf[op]; !ok && descs[op].op == "" {
						fail(f.path, dl.line, fmt.Sprintf("`refusal-courtesy-dispatches:` names %s, which is neither a described op nor one any script dispatches — a stale or mistyped op name", op))
					}
				}
			}
		}
	}

	// Every JS declaration must be true.
	for _, f := range files {
		for _, d := range f.unbound {
			r.declarations++
			fail(f.path, d.line, fmt.Sprintf("this declaration sits inside no top-level statement and above no statement header (a blank line breaks the block) — move it inside the site that dispatches %s, or directly above its header", d.op))
		}
		for _, s := range f.sites {
			for _, d := range s.decls {
				r.declarations++
				if d.bad != "" {
					fail(f.path, d.line, "malformed declaration — "+d.bad+"; the grammar is `// refusal-courtesy: <Op>/<Code>[, <Code>…]: <verb> — <clause>` or `// refusal-courtesy: <Op>/<Code>: see <fn>`")
					continue
				}
				if _, ok := codesOf[d.op]; !ok && descs[d.op].op == "" {
					fail(f.path, d.line, fmt.Sprintf("declaration names %s, which is neither a described op nor one any script dispatches — a stale or mistyped op name", d.op))
					continue
				}
				if !s.ops[d.op] {
					fail(f.path, d.line, fmt.Sprintf("declaration for %s/%s sits in %s, which does not name %s — a declaration binds to the site that dispatches the op; delete it here or move it", d.op, strings.Join(d.codes, ", "), s.name, d.op))
					continue
				}
				for _, code := range d.codes {
					if _, ok := codesOf[d.op][code]; !ok {
						fail(f.path, d.line, fmt.Sprintf("stale — %s's script can no longer raise %s (its codes: %s); delete the code from the declaration", d.op, code, codeList(codesOf[d.op])))
					}
				}
				if d.verb == "see" {
					target := f.fns[d.clause]
					switch {
					case target == nil:
						fail(f.path, d.line, fmt.Sprintf("`see %s` names no top-level function in this file — delegate to a sibling site, or declare the courtesy here", d.clause))
					case !target.ops[d.op]:
						fail(f.path, d.line, fmt.Sprintf("`see %s` delegates to a function that does not dispatch %s — it is not a site, so it carries no courtesy for the code", d.clause, d.op))
					default:
						for _, code := range d.codes {
							td := findDecl(target.decls, d.op, code)
							switch {
							case td == nil:
								fail(f.path, d.line, fmt.Sprintf("`see %s` delegates to a site with no declaration for %s/%s — declare it there (a non-`see` verb), or here", d.clause, d.op, code))
							case td.verb == "see":
								fail(f.path, d.line, fmt.Sprintf("`see %s` delegates to a declaration for %s that is itself `see %s` — one hop only; point at the site that declares the courtesy", d.clause, code, td.clause))
							}
						}
					}
					continue
				}
				if !jsVerbs[d.verb] {
					fail(f.path, d.line, fmt.Sprintf("unknown verb %q — the vocabulary is hide | disable | drop | cap | prefill | confirm | none | unreachable, or `see <fn>`", d.verb))
				}
			}
		}
	}

	// Every (facet) declaration must be true.
	for _, l := range lits {
		for _, d := range l.decls {
			r.declarations++
			if d.bad != "" {
				fail(l.file, d.line, "malformed (facet) declaration — "+d.bad+"; the grammar is `// refusal-courtesy(facet): <Code>[, <Code>…]: <verb> — <clause>`")
				continue
			}
			if !descs[l.op].dispatch {
				fail(l.file, d.line, fmt.Sprintf("%s's OpMetaSpec has no Dispatch, so Facet never offers it and is not a site — delete the (facet) declaration", l.op))
				continue
			}
			for _, code := range d.codes {
				if _, ok := codesOf[l.op][code]; !ok {
					fail(l.file, d.line, fmt.Sprintf("stale — %s's script can no longer raise %s (its codes: %s); delete the code from the declaration", l.op, code, codeList(codesOf[l.op])))
				}
			}
			if !facetVerbs[d.verb] {
				fail(l.file, d.line, fmt.Sprintf("unknown (facet) verb %q — the vocabulary is hide | drop | cap | none | unreachable", d.verb))
			}
		}
	}
	sort.SliceStable(r.findings, func(i, j int) bool { return findingLess(r.findings[i], r.findings[j]) })
	return r
}

// findingLess orders `path:line: …` findings by path, then numeric line,
// then text.
func findingLess(a, b string) bool {
	pa, la := splitFinding(a)
	pb, lb := splitFinding(b)
	if pa != pb {
		return pa < pb
	}
	if la != lb {
		return la < lb
	}
	return a < b
}

func splitFinding(f string) (string, int) {
	parts := strings.SplitN(f, ":", 3)
	if len(parts) < 3 {
		return f, 0
	}
	n, _ := strconv.Atoi(parts[1])
	return parts[0], n
}

func hasDecl(decls []declaration, op, code string) bool { return findDecl(decls, op, code) != nil }

// findDecl returns the well-formed declaration in decls that names op and
// code, or nil.
func findDecl(decls []declaration, op, code string) *declaration {
	for i := range decls {
		if decls[i].bad != "" || decls[i].op != op {
			continue
		}
		for _, c := range decls[i].codes {
			if c == code {
				return &decls[i]
			}
		}
	}
	return nil
}

func hasFacetDecl(lits []goLiteral, code string) bool {
	for _, l := range lits {
		for _, d := range l.decls {
			if d.bad != "" {
				continue
			}
			for _, c := range d.codes {
				if c == code {
					return true
				}
			}
		}
	}
	return false
}

func codeList(codes map[string]codeLoc) string {
	if len(codes) == 0 {
		return "none"
	}
	out := make([]string, 0, len(codes))
	for c := range codes {
		out = append(out, c)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// locList renders `Code (where script:line)` for each code, in order.
func locList(codes []string, locs map[string]codeLoc) string {
	parts := make([]string, 0, len(codes))
	for _, c := range codes {
		parts = append(parts, fmt.Sprintf("%s (%s)", c, locs[c]))
	}
	return strings.Join(parts, ", ")
}

// printCensus prints the governed population, one block per op — the
// population a declaration pass authors from.
func printCensus(r result) {
	for _, row := range r.census {
		codes := make([]string, 0, len(row.codes))
		for _, c := range row.codes {
			codes = append(codes, fmt.Sprintf("%s(%s)", c, row.locs[c].short(row.pkg)))
		}
		sites := strings.Join(row.sites, " ")
		if row.facet {
			sites = strings.TrimSpace(sites + " [facet]")
		}
		fmt.Printf("%s (%s) codes: %s  sites: %s\n", row.op, row.pkg, strings.Join(codes, " "), sites)
	}
	fmt.Printf("governed ops=%d pairs=%d js-sites=%d facet-sites=%d\n", r.governedOps, r.governedPairs, r.jsSites, r.facetSites)
}

// --- self-test ---

// runSelfTest runs the mutation battery over fixture sources, never the real
// corpus: every verdict shape and its passing twin, the exempt family, the
// transitive helper attribution, parameter-carried codes (positional,
// keyword, forwarded, and the non-literal that stays unmodelled), the
// per-package scoping of a described op's codes, the grouped grammar, the
// KNOWN_CATALOG_OPS exclusion, the `see` hop rule, the single-site ungoverned
// rule, and a comment-block-above-the-header declaration.
func runSelfTest() {
	pass := true
	check := func(name string, wantFinding bool, findings []string, needle string) {
		found := false
		for _, f := range findings {
			if strings.Contains(f, needle) {
				found = true
				break
			}
		}
		switch {
		case found != wantFinding:
			fmt.Fprintf(os.Stderr, "%s selftest: FAIL — %s (want finding=%v, needle %q; findings: %s)\n", gate, name, wantFinding, needle, strings.Join(findings, " | "))
			pass = false
		default:
			fmt.Printf("selftest: PASS — %s\n", name)
		}
	}
	missing := func(fn, op string, others int, codes string) string {
		return fmt.Sprintf("%s dispatches %s, which %d other site(s) also dispatch and whose script refuses with state codes this site declares no courtesy for — missing: %s —", fn, op, others, codes)
	}

	const script = `
def require_open(tab):
    if tab.status != "open":
        fail("TabNotOpen: " + tab.key)

def require_price(item):
    if item.available == False:
        fail("ItemUnavailable: %s" % item.key)
    return item.price

def check_item(item):
    return require_price(item)

def claim_cell(hub, conflict_code, who):
    if hub.taken:
        fail(conflict_code + ": " + who + " slot is already booked")

def require_state(key, code):
    fail(code + ": " + key)

def forward(hub, code2):
    claim_cell(hub, code2, "forwarded")

def unrelated():
    fail("NeverAttributed: nobody calls me from a block")

def execute(state, op):
    ot = op.operationType
    p = op.payload
    if ot == "Charge":
        tab = state.get(p.tabKey)
        if not tab:
            fail("UnknownTab: " + p.tabKey)
        require_open(tab)
        check_item(state.get(p.menuItemKey))
        claim_cell(tab, "SlotConflict", "tab")
        require_state(tab.key, code="KeywordCode")
        forward(tab, "Forwarded")
        def inner():
            fail("NestedCode: raised from a nested def")
        inner()
        if not hasattr(p, "amount"):
            fail("InvalidArgument: amount required")
    if ot == "Settle" or ot == "VoidCharge":
        fail("TabNotOpen: settle path")
    if ot in ["Lonely"]:
        fail("Solo: one site only")
    if ot == "Prose":
        fail("not a code at all")
    if ot == "Facetless":
        fail("Blocked: facet-less op")
    if ot == "Shared":
        fail("FixtureShared: raised by the fixture package")
`
	// The unmodelled shapes, kept apart so the clean twin stays clean: a
	// local variable as the leftmost operand, a helper's code parameter bound
	// to a non-literal, and a format literal whose code is an argument.
	const unmodelledScript = `
def claim_cell(hub, conflict_code, who):
    fail(conflict_code + ": " + who)

def execute(state, op):
    ot = op.operationType
    if ot == "Dynamic":
        code = "Opaque"
        fail(code + ": unmodelled")
    if ot == "Bound":
        dyn = pick_code(state)
        claim_cell(state, dyn, "dynamic")
        claim_cell(state, "Literal", "literal")
    if ot == "Formatted":
        fail("%s: %s" % ("FormatCode", state.key))
`
	// A second package dispatching two of the same op names: Settle (which
	// the fixture package describes) and Shared (which nobody describes).
	const otherScript = `
def execute(state, op):
    ot = op.operationType
    if ot == "Settle":
        fail("OtherCode: the other package's Settle")
    if ot == "Shared":
        fail("OtherShared: raised by the other package")
`
	descs := map[string]descriptor{
		"Charge":     {op: "Charge", pkg: "fixture", dispatch: true},
		"Settle":     {op: "Settle", pkg: "fixture", dispatch: true},
		"VoidCharge": {op: "VoidCharge", pkg: "fixture", dispatch: false},
		"Lonely":     {op: "Lonely", pkg: "fixture", dispatch: false},
		"Prose":      {op: "Prose", pkg: "fixture", dispatch: true},
		"Facetless":  {op: "Facetless", pkg: "fixture", dispatch: false},
	}
	goSrc := `package fixture

import "github.com/operatinggraph/lattice/internal/pkgmgr"

const settleOp = "Settle"

func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{
			OperationType: "Charge",
			%s
			Dispatch: &pkgmgr.OpDispatchSpec{Class: "tab"},
		},
		{
			OperationType: settleOp,
			%s
			Dispatch: &pkgmgr.OpDispatchSpec{Class: "tab"},
		},
		{
			OperationType: "VoidCharge",
			%s
		},
	}
}
`
	newFixtureCorpus := func() *corpus {
		c := newCorpus()
		if err := c.scanScript("fixture", "tab", script); err != nil {
			fmt.Fprintf(os.Stderr, "%s selftest: script parse error: %v\n", gate, err)
			os.Exit(2)
		}
		if err := c.scanScript("other", "tab", otherScript); err != nil {
			fmt.Fprintf(os.Stderr, "%s selftest: other script parse error: %v\n", gate, err)
			os.Exit(2)
		}
		return c
	}
	// run assembles one battery vector from the scripts, a JS body and the
	// three Go declaration slots, and returns the verdicts.
	run := func(js string, chargeGo, settleGo, voidGo string) []string {
		c := newFixtureCorpus()
		f, err := scanJS("cmd/fixture-app/web/app.js", js, registeredOps(c, descs))
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s selftest: js parse error: %v\n", gate, err)
			os.Exit(2)
		}
		lits, errs := scanGoSources(map[string]string{"packages/fixture/opmetas.go": fmt.Sprintf(goSrc, chargeGo, settleGo, voidGo)})
		if len(errs) > 0 {
			fmt.Fprintf(os.Stderr, "%s selftest: go parse error: %v\n", gate, errs)
			os.Exit(2)
		}
		return judge(c, descs, []*jsFile{f}, lits).findings
	}

	// Attribution.
	{
		c := newFixtureCorpus()
		var attr []string
		for op, codes := range c.byPkg["fixture"] {
			for code := range codes {
				attr = append(attr, op+"/"+code)
			}
		}
		has := func(op, code string) bool { _, ok := c.codesOf(op, descs)[code]; return ok }
		check("direct fail in the block is attributed", true, attr, "Charge/UnknownTab")
		check("one helper hop is attributed (require_open → TabNotOpen)", true, attr, "Charge/TabNotOpen")
		check("two helper hops are attributed (check_item → require_price → ItemUnavailable, a % chain)", true, attr, "Charge/ItemUnavailable")
		check("a fail in a nested def belongs to the enclosing block", true, attr, "Charge/NestedCode")
		check("a helper no block calls attributes nothing", false, attr, "NeverAttributed")
		check("an or-joined dispatch attributes to both ops", has("Settle", "TabNotOpen") && has("VoidCharge", "TabNotOpen"), []string{"ok"}, "ok")
		check("a positional literal argument binds the helper's code parameter (claim_cell → SlotConflict)", true, attr, "Charge/SlotConflict")
		check("a keyword literal argument binds the helper's code parameter (require_state code=)", true, attr, "Charge/KeywordCode")
		check("a parameter forwarded one hop binds the inner helper's code (forward → claim_cell)", true, attr, "Charge/Forwarded")
		check("the clean fixture has no unmodelled fail site", len(c.unmodelled) == 0, []string{"ok"}, "ok")
		check("dispatch blocks are recorded (in-list form)", c.dispatched["Lonely"], []string{"ok"}, "ok")

		u := newCorpus()
		if err := u.scanScript("fixture", "tab", unmodelledScript); err != nil {
			fmt.Fprintf(os.Stderr, "%s selftest: unmodelled script parse error: %v\n", gate, err)
			os.Exit(2)
		}
		var uattr []string
		for op, codes := range u.byPkg["fixture"] {
			for code := range codes {
				uattr = append(uattr, op+"/"+code)
			}
		}
		check("a local variable as the leftmost operand is unmodelled, never a code", false, uattr, "Dynamic/")
		check("the unmodelled local is a finding", true, u.unmodelled, "packages/fixture:0: refusal-courtesy: unmodelled — DDL tab script:9: fail() in Dynamic's reach whose first argument's leftmost operand is `code`")
		check("a non-literal argument leaves that edge unmodelled (claim_cell with a local)", true, u.unmodelled, "fail() in Bound's reach whose first argument's leftmost operand is `conflict_code`")
		check("twin: the literal edge to the same helper still attributes", true, uattr, "Bound/Literal")
		check("a format literal whose code is an argument is unmodelled, not exempt", true, u.unmodelled, "fail() in Formatted's reach whose first argument's leftmost operand is the format literal")
		check("the format literal attributes no code", false, uattr, "Formatted/")
		ufind := judge(u, descs, nil, nil).findings
		check("an unmodelled site is a verdict (STRICT fails on it)", true, ufind, "refusal-courtesy: unmodelled — DDL tab script:9")
		check("a described op's codes are scoped to the describing package (Settle drops OtherCode)", !has("Settle", "OtherCode") && has("Settle", "TabNotOpen"), []string{"ok"}, "ok")
		check("twin: an undescribed op keeps the union across packages (Shared)", has("Shared", "FixtureShared") && has("Shared", "OtherShared"), []string{"ok"}, "ok")
	}

	const chargeDecl = "// refusal-courtesy(facet): TabNotOpen: hide — the tab card hides Charge once .status leaves open"
	const itemDecl = "// refusal-courtesy(facet): ItemUnavailable: drop — entityRefCandidates drops rows with available === false"
	const nestedDecl = "// refusal-courtesy(facet): NestedCode: none — the nested branch is a fixture"
	const paramDecl = "// refusal-courtesy(facet): SlotConflict, KeywordCode, Forwarded: none — parameter-carried fixture codes"
	const settleDecl = "// refusal-courtesy(facet): TabNotOpen: hide — settle hides off a closed tab"
	chargeGoAll := chargeDecl + "\n" + itemDecl + "\n" + nestedDecl + "\n" + paramDecl
	// A complete JS twin: two Charge sites (function + top-level listener),
	// one Settle site; every governed pair declared.
	const jsClean = `
const KNOWN_CATALOG_OPS = ["Charge", "Settle"];
async function chargeFromDesk(tab) {
  // refusal-courtesy: Charge/TabNotOpen: hide — the desk hides the charge button off a closed tab
  // refusal-courtesy: Charge/ItemUnavailable: drop — the menu picker drops rows with available === false
  // refusal-courtesy: Charge/NestedCode: none — fixture code
  // refusal-courtesy: Charge/SlotConflict, KeywordCode, Forwarded: unreachable — parameter-carried fixture codes
  await opOrThrow({ operationType: "Charge", class: "tab", payload: { tabKey: tab.key } });
}
// refusal-courtesy: Charge/TabNotOpen: see chargeFromDesk
// refusal-courtesy: Charge/ItemUnavailable: see chargeFromDesk
// refusal-courtesy: Charge/NestedCode: see chargeFromDesk
// refusal-courtesy: Charge/SlotConflict, KeywordCode, Forwarded: see chargeFromDesk
async function chargeFromKiosk(tab) {
  const row = opCatalogCache.Charge;
  await submitCatalogOp(row, tab);
}
async function settle(tab) {
  // refusal-courtesy: Settle/TabNotOpen: disable — the settle button disables off a closed tab
  await opOrThrow({ operationType: "Settle", class: "tab", payload: { tabKey: tab.key } });
}
async function lonely() {
  await opOrThrow({ operationType: "Lonely" });
}
async function facetless() {
  await opOrThrow({ operationType: "Facetless" });
}
function descriptorFor(operationName) {
  return (state.opCatalog || {})[operationName] || null;
}
`
	got := []string(nil)
	clean := run(jsClean, chargeGoAll, settleDecl, "")
	check("a fully declared fixture is clean", false, clean, "refusal-courtesy")
	check("the catalog accessor's own definition is not a generic dispatcher", false, clean, "descriptorFor is a generic dispatcher")

	// Generic dispatchers.
	const generic = "\nasync function completeTask(task) {\n  const desc = descriptorFor(task.operationName);\n  return submit(desc, task);\n}\n"
	got = run(jsClean+generic, chargeGoAll, settleDecl, "")
	check("a descriptorFor(<expr>) function with no dispatcher line fails at its header", true, got, "app.js:32: refusal-courtesy: completeTask is a generic dispatcher — line 33 resolves a catalog row from a non-literal op name")
	check("the message shows the exact dispatcher line to write", true, got, "add `// refusal-courtesy-dispatches: <Op>[, <Op>…]` inside completeTask")
	got = run(jsClean+"\nasync function cached(opType) {\n  const row = opCatalogCache && opCatalogCache[opType];\n  return renderOpForm(row);\n}\n", chargeGoAll, settleDecl, "")
	check("an opCatalogCache[<expr>] function is a generic dispatcher too", true, got, "cached is a generic dispatcher")
	got = run(jsClean+"\nasync function viaState(name) {\n  return renderOpForm(state.opCatalog[name]);\n}\n", chargeGoAll, settleDecl, "")
	check("a state.opCatalog[<expr>] function is a generic dispatcher too", true, got, "viaState is a generic dispatcher")
	declared := strings.Replace(generic, "async function completeTask(task) {\n", "async function completeTask(task) {\n  // refusal-courtesy-dispatches: Lonely\n", 1)
	got = run(jsClean+declared, chargeGoAll, settleDecl, "")
	check("a dispatcher line makes the function a site (Lonely gains its second site, lonely now needs a declaration)", true, got, missing("lonely", "Lonely", 1, "Solo"))
	check("the dispatcher itself then needs the per-code declarations", true, got, missing("completeTask", "Lonely", 1, "Solo"))
	check("the dispatcher line itself raises no finding", false, got, "is a generic dispatcher")
	fullyDeclared := strings.Replace(jsClean, "async function lonely() {\n", "async function lonely() {\n  // refusal-courtesy: Lonely/Solo: none — fixture\n", 1) +
		strings.Replace(declared, "  // refusal-courtesy-dispatches: Lonely\n", "  // refusal-courtesy-dispatches: Lonely\n  // refusal-courtesy: Lonely/Solo: none — fixture\n", 1)
	got = run(fullyDeclared, chargeGoAll, settleDecl, "")
	check("twin: a declared dispatcher with declared siblings is clean", false, got, "refusal-courtesy")
	got = run(jsClean+strings.Replace(declared, "dispatches: Lonely", "dispatches: Lonely, Nope", 1), chargeGoAll, settleDecl, "")
	check("a dispatcher line naming an unregistered op fails", true, got, "`refusal-courtesy-dispatches:` names Nope, which is neither a described op")
	got = run(jsClean+"\nasync function plain(task) {\n  // refusal-courtesy-dispatches: Lonely\n  return submit(descriptorFor(\"Settle\"), task);\n}\n", chargeGoAll, settleDecl, "")
	check("a dispatcher line in a function with no non-literal lookup is stale", true, got, "stale — plain resolves no catalog row from a non-literal op name")
	got = run(jsClean+"\nasync function literal(tab) { return submitCatalogOp(catalog[\"Settle\"], tab); }\n", chargeGoAll, settleDecl, "")
	check("twin: catalog[\"X\"] with a literal is a plain site, no dispatcher line needed", false, got, "is a generic dispatcher")
	got = run(jsClean+"\n// refusal-courtesy-dispatches: Lonely\n\nasync function gap2() { return 1; }\n", chargeGoAll, settleDecl, "")
	check("a dispatcher line bound to no statement fails", true, got, "`refusal-courtesy-dispatches:` line sits inside no top-level statement")

	// (i) a governed JS site without a declaration.
	got = run(strings.Replace(jsClean, "  // refusal-courtesy: Charge/TabNotOpen: hide — the desk hides the charge button off a closed tab\n", "", 1), chargeGoAll, settleDecl, "")
	check("(i) a governed site with no declaration fails at the site, naming the missing code", true, got, "app.js:3: refusal-courtesy: "+missing("chargeFromDesk", "Charge", 2, "TabNotOpen"))
	check("(i) the message shows the exact declaration to write", true, got, "`// refusal-courtesy: Charge/<Code>: <verb> — <clause>`")
	got = run(strings.Replace(strings.Replace(jsClean, "  // refusal-courtesy: Charge/TabNotOpen: hide — the desk hides the charge button off a closed tab\n", "", 1), "  // refusal-courtesy: Charge/NestedCode: none — fixture code\n", "", 1), chargeGoAll, settleDecl, "")
	check("(i) every missing code for one (site, op) lands on ONE line", true, got, missing("chargeFromDesk", "Charge", 2, "NestedCode, TabNotOpen"))

	// (ii) a described op without its (facet) declaration.
	got = run(jsClean, itemDecl+"\n"+nestedDecl+"\n"+paramDecl, settleDecl, "")
	check("(ii) a described op with no (facet) declaration fails at the literal, naming the missing code", true, got, "opmetas.go:9: refusal-courtesy: Charge is offered by Facet's descriptor form (Dispatch is set), 2 other site(s) also dispatch it, and its script refuses with state codes the descriptor declares no courtesy for — missing: TabNotOpen —")
	check("(ii) the message shows the exact (facet) declaration", true, got, "`// refusal-courtesy(facet): <Code>: <verb> — <clause>`")

	// (iii) stale declarations, JS and Go.
	got = run(jsClean+"\n// refusal-courtesy: Settle/CreditHold: hide — never raised\nasync function settleTwice(tab) { await opOrThrow({ operationType: \"Settle\" }); }\n", chargeGoAll, settleDecl, "")
	check("(iii) a JS declaration naming a code the op cannot raise is stale", true, got, "stale — Settle's script can no longer raise CreditHold")
	got = run(jsClean, chargeGoAll+"\n// refusal-courtesy(facet): CreditHold: hide — never raised", settleDecl, "")
	check("(iii) a (facet) declaration naming a code the op cannot raise is stale", true, got, "stale — Charge's script can no longer raise CreditHold")
	got = run(jsClean, chargeGoAll+"\n// refusal-courtesy(facet): InvalidArgument: none — exempt but true", settleDecl, "")
	check("(iii) twin: a declaration on an exempt code the op does raise is not stale", false, got, "stale")
	got = run(jsClean, chargeGoAll, settleDecl+"\n// refusal-courtesy(facet): OtherCode: none — the other package's code", "")
	check("(iii) a declaration on a code only ANOTHER package's script raises is stale for the described op", true, got, "stale — Settle's script can no longer raise OtherCode")

	// Grouped grammar.
	grouped := strings.Replace(jsClean, "  // refusal-courtesy: Charge/TabNotOpen: hide — the desk hides the charge button off a closed tab\n  // refusal-courtesy: Charge/ItemUnavailable: drop — the menu picker drops rows with available === false\n", "  // refusal-courtesy: Charge/TabNotOpen, ItemUnavailable: hide — the desk hides both behind one guard\n", 1)
	got = run(grouped, chargeGoAll, settleDecl, "")
	check("grouped: a JS declaration naming two codes declares both", false, got, "refusal-courtesy")
	got = run(strings.Replace(grouped, "Charge/TabNotOpen, ItemUnavailable: hide", "Charge/TabNotOpen, ItemUnavailable, CreditHold: hide", 1), chargeGoAll, settleDecl, "")
	check("grouped twin: one stale code in a JS group is named by (iii)", true, got, "stale — Charge's script can no longer raise CreditHold")
	got = run(jsClean, "// refusal-courtesy(facet): TabNotOpen, ItemUnavailable, NestedCode: hide — grouped\n"+paramDecl, settleDecl, "")
	check("grouped: a (facet) declaration naming three codes declares all three", false, got, "refusal-courtesy")
	got = run(jsClean, "// refusal-courtesy(facet): TabNotOpen, ItemUnavailable, NestedCode, CreditHold: hide — grouped\n"+paramDecl, settleDecl, "")
	check("grouped twin: one stale code in a (facet) group is named by (iii)", true, got, "stale — Charge's script can no longer raise CreditHold")
	check("grouped: a grouped `see` delegates every code (chargeFromKiosk is clean)", false, clean, "chargeFromKiosk")

	// (iv) a declaration in a statement that is not a site for the op.
	got = run(jsClean+"\nasync function stray() {\n  // refusal-courtesy: Charge/TabNotOpen: hide — nothing here dispatches Charge\n  return 1;\n}\n", chargeGoAll, settleDecl, "")
	check("(iv) a declaration inside a non-site fails", true, got, "declaration for Charge/TabNotOpen sits in stray, which does not name Charge")
	got = run(jsClean+"\n// refusal-courtesy: Charge/TabNotOpen: hide — floating\n\nasync function gap() { return opOrThrow({ operationType: \"Charge\" }); }\n", chargeGoAll, settleDecl, "")
	check("(iv) a declaration separated from the header by a blank line binds to nothing", true, got, "sits inside no top-level statement and above no statement header")
	got = run(jsClean+"\nasync function typo() {\n  // refusal-courtesy: Chrage/TabNotOpen: hide — misspelt op\n  return opOrThrow({ operationType: \"Charge\" });\n}\n", chargeGoAll, settleDecl, "")
	check("(iv) a declaration naming an unregistered op fails", true, got, "names Chrage, which is neither a described op nor one any script dispatches")

	// (v) verbs, clauses, see.
	got = run(strings.Replace(jsClean, "Settle/TabNotOpen: disable — the settle", "Settle/TabNotOpen: vanish — the settle", 1), chargeGoAll, settleDecl, "")
	check("(v) an unknown JS verb fails", true, got, `unknown verb "vanish"`)
	got = run(strings.Replace(jsClean, "Settle/TabNotOpen: disable — the settle button disables off a closed tab", "Settle/TabNotOpen: disable —", 1), chargeGoAll, settleDecl, "")
	check("(v) an empty clause fails", true, got, "malformed declaration")
	got = run(strings.Replace(jsClean, "Settle/TabNotOpen: disable — the settle button", "Settle/TabNotOpen: disable - the settle button", 1), chargeGoAll, settleDecl, "")
	check("(v) twin: the ASCII ` - ` separator is accepted", false, got, "refusal-courtesy")
	got = run(jsClean, chargeGoAll, strings.Replace(settleDecl, "hide —", "disable —", 1), "")
	check("(v) a JS-only verb in a (facet) declaration fails", true, got, `unknown (facet) verb "disable"`)
	got = run(jsClean, chargeGoAll, "// refusal-courtesy(facet): TabNotOpen: see settle", "")
	check("(v) `see` in a (facet) declaration fails", true, got, "`see` is a JS-only verb")
	got = run(strings.Replace(jsClean, "Charge/TabNotOpen: see chargeFromDesk", "Charge/TabNotOpen: see nowhere", 1), chargeGoAll, settleDecl, "")
	check("(v) `see` to a function that does not exist fails", true, got, "`see nowhere` names no top-level function")
	got = run(strings.Replace(jsClean, "Charge/TabNotOpen: see chargeFromDesk", "Charge/TabNotOpen: see settle", 1), chargeGoAll, settleDecl, "")
	check("(v) `see` to a function that is not a site for the op fails", true, got, "`see settle` delegates to a function that does not dispatch Charge")
	got = run(strings.Replace(jsClean, "Charge/TabNotOpen: hide — the desk hides the charge button off a closed tab", "Charge/TabNotOpen: see chargeFromKiosk", 1), chargeGoAll, settleDecl, "")
	check("(v) a `see` chain of two hops fails", true, got, "delegates to a declaration for TabNotOpen that is itself `see")
	check("(v) twin: a one-hop `see` to a declaring sibling passes", false, clean, "`see chargeFromDesk`")

	// (vi) a (facet) declaration on an op with no Dispatch.
	got = run(jsClean, chargeGoAll, settleDecl, "// refusal-courtesy(facet): TabNotOpen: hide — VoidCharge has no Dispatch")
	check("(vi) a (facet) declaration on a Dispatch-less op fails", true, got, "VoidCharge's OpMetaSpec has no Dispatch")

	// Exempt family: UnknownTab / InvalidArgument raise no missing-declaration
	// verdicts even though Charge is governed and undeclared for them.
	check("exempt: UnknownTab needs no declaration", false, clean, "UnknownTab")
	check("exempt: InvalidArgument needs no declaration", false, clean, "InvalidArgument")
	check("exempt: a prose fail with no Code: prefix is not a code", false, clean, "Prose")

	// KNOWN_CATALOG_OPS: the array literal is not a site.
	got = run(strings.Replace(jsClean, `["Charge", "Settle"]`, `["Charge", "Settle", "Lonely"]`, 1), chargeGoAll, settleDecl, "")
	check("KNOWN_CATALOG_OPS membership does not make the const a site (Lonely stays single-site)", false, got, "Lonely")
	got = run(strings.Replace(jsClean, "const KNOWN_CATALOG_OPS", "const OTHER_LIST", 1), chargeGoAll, settleDecl, "")
	check("twin: the same array under another name IS a site", true, got, "(statement@2) dispatches Charge")

	// Single-site ungoverned: Lonely has a state code and one site — nothing
	// required; a second site arms it.
	check("a single-site op is ungoverned", false, clean, "Lonely")
	got = run(jsClean+"\nasync function lonelyAgain() { await opOrThrow({ operationType: \"Lonely\" }); }\n", chargeGoAll, settleDecl, "")
	check("a second site arms the op", true, got, missing("lonely", "Lonely", 1, "Solo"))
	check("a Dispatch-less op with one JS site is ungoverned (Facet is not its second site)", false, clean, "Facetless")
	got = run(strings.Replace(jsClean, "  // refusal-courtesy: Settle/TabNotOpen: disable — the settle button disables off a closed tab\n", "", 1), chargeGoAll, settleDecl, "")
	check("twin: Facet is the second site of a described op with one JS site (Settle is governed)", true, got, missing("settle", "Settle", 1, "TabNotOpen"))

	// Comment block above the header (chargeFromKiosk in jsClean) already
	// binds; pin the negative twin — one line of the block removed.
	got = run(strings.Replace(jsClean, "// refusal-courtesy: Charge/NestedCode: see chargeFromDesk\n", "", 1), chargeGoAll, settleDecl, "")
	check("removing one line of the block above the header exposes the site", true, got, missing("chargeFromKiosk", "Charge", 2, "NestedCode"))
	check("twin: the block above the header declares for the site below it", false, clean, "chargeFromKiosk")

	// A bracket member access is a site.
	got = run(jsClean+"\nasync function bracket(tab) { return submitCatalogOp(catalog[\"Settle\"], tab); }\n", chargeGoAll, settleDecl, "")
	check("catalog[\"Settle\"] makes a site", true, got, "bracket dispatches Settle")

	// A (facet) grammar line in app.js is refused.
	got = run(jsClean+"\nasync function wrongHome() {\n  // refusal-courtesy(facet): TabNotOpen: hide — wrong file\n  return opOrThrow({ operationType: \"Settle\" });\n}\n", chargeGoAll, settleDecl, "")
	check("a (facet) declaration in app.js is refused", true, got, "`(facet)` declarations live in the op's OpMetaSpec literal in Go")

	if !pass {
		os.Exit(2)
	}
	fmt.Printf("%s: selftest ok\n", gate)
}
