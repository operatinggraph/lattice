//go:build ignore

// lint-links-page-limit — every kv.Links(...) call states a page limit.
//
// THE HAZARD. starlark_kv.go's kv.Links builtin (internal/processor/starlark_kv.go)
// charges the WALL against the script's live-read budget (DefaultLiveReadBudget,
// live_read_budget.go, 60,000 units) by the clamped LIMIT it lists a page, not by
// len(the links actually returned): a 3-arg call — kv.Links(hubKey, relation,
// direction) — supplies no limit, so the builtin substitutes defaultLinkPageLimit
// (256) and the read costs 257 units (the list call plus one unit per listed
// slot) however many links the hub actually carries. That is one unremarkable
// read on its own, but a kv.Links call sitting inside a per-candidate loop —
// walking a role's tasks, a lease's occurrences, an account's history — turns a
// hub with a long-enough history into a script that can never finish inside the
// budget: the evaluation errors out on the wall every time, and because the
// error is a script failure rather than a domain refusal, Weaver re-dispatches
// the same operation on the same schedule, forever. The defect first minted at
// cafe-ledger's arrears_entries (2026-09-13) — a statement walk over an
// account's postedTo history with no limit, so a debtor with enough charges
// could never again generate a statement — and the census run the day after
// (2026-09-14) found 33 further sites carrying the identical shape.
//
// A stated limit is also the author's own claim about the relation's
// cardinality: "at most a handful" reads differently from "could be
// unbounded," and a script that never writes the number down cannot be
// checked against a growing production hub later.
//
// THE RULE. In every shipped package script (the compiled pkgregistry corpus —
// each DDL's Script, parsed with go.starlark.net/syntax), a CallExpr whose Fn is
// `kv.Links` (a DotExpr with X = Ident "kv", Name = "Links") must pass an
// explicit page limit — the keyword form (`limit=...`) or the 5th positional
// argument, matching the builtin's own UnpackArgs names ("hubKey", "relation",
// "direction", "cursor?", "limit?" — starlark_kv.go). A call with 4 positional
// arguments (a cursor, no limit) is the same finding: a cursor says nothing
// about the page's own size.
//
// The limit expression must be one of three shapes to be judged CLEAN:
//   - an integer literal (`kv.Links(h, "r", "out", None, 20)`);
//   - an identifier bound at module top level to an integer literal
//     (`ROLE_PAGE_LIMIT = 50` then `limit=ROLE_PAGE_LIMIT`) — a named constant
//     the author chose and can be grepped;
//   - a function parameter name (`def f(limit): ... kv.Links(h, "r", "out",
//     None, limit)`) — a helper the caller supplies the bound through.
//
// Anything else — a bare call, an arithmetic expression, an attribute lookup —
// is UNMODELLED: the recogniser cannot resolve it to a number, so it is neither
// judged clean nor reported as a finding, and is printed only under VERBOSE=1.
// An unmodelled limit still states an intent to bound the page; a missing
// argument states nothing.
//
// A BARE integer literal of 256 or more at the call site is ALSO a finding:
// 256 is starlark_kv.go's own defaultLinkPageLimit, and typing it (or a
// higher number) directly into the call is indistinguishable from passing
// nothing — it makes no claim about the relation's actual cardinality and
// buys no protection the omitted argument didn't already have, and it
// carries no name a later reviewer could grep or reconsider. A named
// module-level constant that happens to resolve to 256 or more is exempt
// from this one check (declaring and naming it is itself the "author states
// a real number" this rule is chasing, whatever number they chose); so is a
// function-parameter limit, whose value is not visible here by construction.
//
// Self-tests on every run (verbose under VERBOSE=1) and refuses an all-clear
// over zero examined scripts. `--list` prints every examined kv.Links call
// with its verdict. STRICT=1 exits non-zero on any finding; otherwise the gate
// is advisory (prints findings, exits 0).
package main

import (
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"

	"go.starlark.net/syntax"

	"github.com/operatinggraph/lattice/internal/pkgregistry"
)

// engineDefaultLinkPageLimit mirrors internal/processor/starlark_kv.go's
// defaultLinkPageLimit (256) — the value the kv.Links builtin substitutes when
// no limit is given at all. An explicit limit at or above it makes the same
// no-claim the omitted argument would have made.
const engineDefaultLinkPageLimit = 256

// listSites makes checkScript print every examined call with its verdict
// (`--list`).
var listSites bool

// verbose prints unmodelled limit expressions the recogniser could not
// resolve to a number, so a run can be audited for blind spots.
var verboseUnmodelled bool

// stats accumulates what a run examined so a clean verdict is auditable.
type stats struct {
	packages int
	scripts  int
	calls    int
}

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
	if os.Getenv("VERBOSE") == "1" {
		verboseUnmodelled = true
	}
	runSelfTest(verbose)

	var findings []string
	var st stats
	for _, name := range pkgregistry.Names() {
		def, ok := pkgregistry.Lookup(name)
		if !ok {
			findings = append(findings, fmt.Sprintf("%s: pkgregistry.Names() lists this package but Lookup does not resolve it — the corpus enumeration and the registry disagree, so this run cannot claim to have checked it", name))
			continue
		}
		st.packages++
		// A package's Script constant is often shared verbatim across several
		// DDL registrations that dispatch off the same script body (wellness-
		// domain's sessionDDLScript backs both "session" and "sessionseries",
		// for instance, because ReassignSessionSeries dispatches through the
		// session script's own def statements). Examining it once per DDL
		// would report the identical call N times under N different DDL
		// names — the same finding, not N findings — so this walk keys on the
		// script TEXT within the package and examines each body once.
		seen := map[string]bool{}
		for _, d := range def.DDLs {
			if strings.TrimSpace(d.Script) == "" || seen[d.Script] {
				continue
			}
			seen[d.Script] = true
			where := fmt.Sprintf("%s: DDL %s", name, d.CanonicalName)
			findings = append(findings, checkScript(where, d.Script, &st)...)
		}
	}
	if st.scripts == 0 {
		findings = append(findings, "lint-links-page-limit: examined ZERO scripts — the corpus walk is broken, and a gate that checked nothing has no all-clear to give")
	}

	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) == 0 {
		fmt.Printf("lint-links-page-limit: clean — %d script(s) across %d package(s); %d kv.Links call(s) checked\n",
			st.scripts, st.packages, st.calls)
		return
	}
	fmt.Printf("lint-links-page-limit: %d issue(s) — %d script(s) across %d package(s), %d call(s) checked\n",
		len(findings), st.scripts, st.packages, st.calls)
	if strict {
		os.Exit(1)
	}
}

// checkScript parses one script and returns its findings. `where` prefixes
// every finding; st counts what was examined.
func checkScript(where, src string, st *stats) []string {
	f, err := syntax.Parse("script.star", src, 0)
	if err != nil {
		// The Processor would refuse this script too; the parse error is the
		// package's own test failure, not this gate's finding.
		return nil
	}
	st.scripts++

	moduleConsts := topLevelIntConsts(f.Stmts)

	var findings []string
	for _, name := range sortedDefNames(f.Stmts) {
		d := defByName(f.Stmts, name)
		paramNames := paramNameSet(d)
		syntax.Walk(d, func(n syntax.Node) bool {
			if n == nil {
				return false
			}
			call, ok := n.(*syntax.CallExpr)
			if !ok || !isKVLinks(call) {
				return true
			}
			st.calls++
			pos, _ := call.Span()
			line := int(pos.Line)
			hub, relation, direction := "?", "?", "?"
			posArgs, kwArgs := splitArgs(call.Args)
			if len(posArgs) > 0 {
				hub = exprString(posArgs[0])
			}
			if len(posArgs) > 1 {
				relation = exprString(posArgs[1])
			}
			if len(posArgs) > 2 {
				direction = exprString(posArgs[2])
			}
			callDesc := fmt.Sprintf(`kv.Links(%s, %s, %s)`, hub, relation, direction)
			siteText := fmt.Sprintf("%s: line %d: %s", where, line, callDesc)

			var limitExpr syntax.Expr
			if kw, ok := kwArgs["limit"]; ok {
				limitExpr = kw
			} else if len(posArgs) >= 5 {
				limitExpr = posArgs[4]
			}

			if limitExpr == nil {
				if listSites {
					fmt.Printf("  call: %s — no page limit\n", siteText)
				}
				findings = append(findings, fmt.Sprintf(
					"%s passes no page limit — starlark_kv.go charges the CLAMPED limit (default 256) against the script's live-read budget regardless of how many links the hub actually carries; inside a per-candidate loop this turns a long-history hub into a permanently rejected evaluation. Pass an explicit 5th positional argument or `limit=` keyword: a small integer literal or a named `_PAGE_LIMIT` constant for a single-page read, or a bounded cursor loop (`for _page in range(MAX_*_PAGES): page, cursor = kv.Links(..., cursor, *_PAGE_LIMIT)`) if more than one page must ever be read.",
					siteText))
				return true
			}

			val, kind := resolveLimit(limitExpr, moduleConsts, paramNames)
			switch kind {
			case limitParam:
				if listSites {
					fmt.Printf("  call: %s — limit=%s (helper parameter)\n", siteText, exprString(limitExpr))
				}
			case limitLiteral:
				if listSites {
					fmt.Printf("  call: %s — limit=%s (=%d)\n", siteText, exprString(limitExpr), val)
				}
				// Only a BARE literal at the call site is judged against the
				// engine default: it carries no name a later reviewer could
				// grep or reconsider, so it reads as "never thought about
				// it," the same as omitting the argument outright. A named
				// module constant that happens to resolve to 256 (or higher)
				// at least required the author to declare and name it, so
				// resolving through one is out of scope for this check
				// (limitConst below).
				if val >= engineDefaultLinkPageLimit {
					findings = append(findings, fmt.Sprintf(
						"%s passes a bare literal limit of %d, at or above starlark_kv.go's own defaultLinkPageLimit (%d) — stating the engine default explicitly makes no claim about this relation's actual cardinality and buys none of the protection a real bound would; state the relation's true cardinality as a smaller literal or name it in a `_PAGE_LIMIT` constant instead.",
						siteText, val, engineDefaultLinkPageLimit))
				}
			case limitConst:
				if listSites {
					fmt.Printf("  call: %s — limit=%s (=%d, named constant)\n", siteText, exprString(limitExpr), val)
				}
			case limitUnmodelled:
				if verboseUnmodelled || listSites {
					fmt.Printf("  call: %s — limit=%s (UNMODELLED, not judged)\n", siteText, exprString(limitExpr))
				}
			}
			return true
		})
	}
	return findings
}

// limitKind classifies a resolved limit expression.
type limitKind int

const (
	limitUnmodelled limitKind = iota
	limitLiteral
	limitConst
	limitParam
)

// resolveLimit decides what a limit expression means: a direct integer
// literal, a top-level constant that resolves to one, a function parameter
// name (value unknown here by construction), or unmodelled.
func resolveLimit(e syntax.Expr, moduleConsts map[string]int64, params map[string]bool) (int64, limitKind) {
	e = unparen(e)
	if lit, ok := e.(*syntax.Literal); ok && lit.Token == syntax.INT {
		if v, ok := literalInt(lit); ok {
			return v, limitLiteral
		}
	}
	if id, ok := e.(*syntax.Ident); ok {
		if v, ok := moduleConsts[id.Name]; ok {
			return v, limitConst
		}
		if params[id.Name] {
			return 0, limitParam
		}
	}
	return 0, limitUnmodelled
}

// literalInt reads a syntax.Literal's parsed integer value (Value is int64 or
// *big.Int for an INT token — never the raw text, which may carry a base
// prefix or underscores the parser already normalized).
func literalInt(lit *syntax.Literal) (int64, bool) {
	switch v := lit.Value.(type) {
	case int64:
		return v, true
	case *big.Int:
		if v.IsInt64() {
			return v.Int64(), true
		}
	}
	return 0, false
}

// topLevelIntConsts collects every module-top-level `NAME = <int literal>`
// binding — the `FOO_PAGE_LIMIT = 20` idiom the corpus uses throughout.
func topLevelIntConsts(stmts []syntax.Stmt) map[string]int64 {
	out := map[string]int64{}
	for _, s := range stmts {
		as, ok := s.(*syntax.AssignStmt)
		if !ok || as.Op != syntax.EQ {
			continue
		}
		id, ok := as.LHS.(*syntax.Ident)
		if !ok {
			continue
		}
		lit, ok := unparen(as.RHS).(*syntax.Literal)
		if !ok || lit.Token != syntax.INT {
			continue
		}
		v, ok := literalInt(lit)
		if !ok {
			continue
		}
		out[id.Name] = v
	}
	return out
}

// paramNameSet returns the names of d's parameters, bare or defaulted
// (`def f(limit):` and `def f(limit=8):` both name `limit`).
func paramNameSet(d *syntax.DefStmt) map[string]bool {
	out := map[string]bool{}
	for _, p := range d.Params {
		switch x := p.(type) {
		case *syntax.Ident:
			out[x.Name] = true
		case *syntax.BinaryExpr:
			if id, ok := x.X.(*syntax.Ident); ok {
				out[id.Name] = true
			}
		}
	}
	return out
}

// splitArgs separates a call's positional arguments from its keyword
// arguments (a keyword argument is a BinaryExpr{Op: EQ, X: Ident} per
// go.starlark.net/syntax's CallExpr.Args doc).
func splitArgs(args []syntax.Expr) (positional []syntax.Expr, keyword map[string]syntax.Expr) {
	keyword = map[string]syntax.Expr{}
	for _, a := range args {
		if bin, ok := a.(*syntax.BinaryExpr); ok && bin.Op == syntax.EQ {
			if id, ok := bin.X.(*syntax.Ident); ok {
				keyword[id.Name] = bin.Y
				continue
			}
		}
		positional = append(positional, a)
	}
	return positional, keyword
}

// isKVLinks reports whether call is `kv.Links(...)`.
func isKVLinks(call *syntax.CallExpr) bool {
	dot, ok := call.Fn.(*syntax.DotExpr)
	if !ok || dot.Name.Name != "Links" {
		return false
	}
	id, ok := dot.X.(*syntax.Ident)
	return ok && id.Name == "kv"
}

func unparen(e syntax.Expr) syntax.Expr {
	for {
		p, ok := e.(*syntax.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// exprString renders an expression well enough for a finding message —
// identifiers, literals, attributes, calls, and binary/unary operators.
// Anything else renders as an opaque placeholder.
func exprString(e syntax.Expr) string {
	switch x := unparen(e).(type) {
	case *syntax.Ident:
		return x.Name
	case *syntax.Literal:
		return x.Raw
	case *syntax.DotExpr:
		return exprString(x.X) + "." + x.Name.Name
	case *syntax.IndexExpr:
		return exprString(x.X) + "[" + exprString(x.Y) + "]"
	case *syntax.CallExpr:
		args := make([]string, 0, len(x.Args))
		for _, a := range x.Args {
			args = append(args, exprString(a))
		}
		return exprString(x.Fn) + "(" + strings.Join(args, ",") + ")"
	case *syntax.UnaryExpr:
		return x.Op.String() + exprString(x.X)
	case *syntax.BinaryExpr:
		return exprString(x.X) + x.Op.String() + exprString(x.Y)
	}
	return fmt.Sprintf("<%T>", e)
}

func sortedDefNames(stmts []syntax.Stmt) []string {
	var out []string
	for _, s := range stmts {
		if d, ok := s.(*syntax.DefStmt); ok {
			out = append(out, d.Name.Name)
		}
	}
	sort.Strings(out)
	return out
}

func defByName(stmts []syntax.Stmt, name string) *syntax.DefStmt {
	for _, s := range stmts {
		if d, ok := s.(*syntax.DefStmt); ok && d.Name.Name == name {
			return d
		}
	}
	return nil
}

// runSelfTest proves the gate on synthetic scripts through checkScript — the
// same entry the corpus walk uses. Vectors mirror the shipped defect shape
// (a 3-arg site, cafe-ledger's arrears_entries 2026-09-13) and a
// mutation-style check that the fixed shape reverts to a finding when the
// limit is dropped.
func runSelfTest(verbose bool) {
	pass := true
	check := func(cond bool, desc string) {
		switch {
		case !cond:
			fmt.Fprintln(os.Stderr, "lint-links-page-limit selftest: FAIL —", desc)
			pass = false
		case verbose:
			fmt.Println("selftest: PASS —", desc)
		}
	}
	run := func(src string) ([]string, stats) {
		var st stats
		return checkScript("selftest", src, &st), st
	}
	has := func(fs []string, sub string) bool {
		for _, f := range fs {
			if strings.Contains(f, sub) {
				return true
			}
		}
		return false
	}

	// Vector 1 — the shipped defect: a 3-arg call, no limit at all.
	f, st := run(`
def execute(state, op):
    page, _ = kv.Links(op.payload["acct"], "postedTo", "in")
    return {}
`)
	check(len(f) == 1 && has(f, "passes no page limit") && has(f, "line 3"), "a bare 3-arg kv.Links call is one finding at its line")
	check(st.calls == 1, "the call is counted")

	// Vector 2 — a 4-arg call (cursor, no limit) is the same finding.
	f, _ = run(`
def execute(state, op):
    cursor = None
    page, cursor = kv.Links(op.payload["acct"], "postedTo", "in", cursor)
    return {}
`)
	check(len(f) == 1 && has(f, "passes no page limit"), "a 4-arg call (cursor, no limit) is a finding")

	// Vector 3 — an integer literal in the 5th positional slot is clean.
	f, _ = run(`
def execute(state, op):
    page, _ = kv.Links(op.payload["acct"], "postedTo", "in", None, 20)
    return {}
`)
	check(len(f) == 0, "an integer literal 5th positional argument is clean")

	// Vector 4 — a keyword limit= argument is clean.
	f, _ = run(`
def execute(state, op):
    page, _ = kv.Links(op.payload["acct"], "postedTo", "in", limit=20)
    return {}
`)
	check(len(f) == 0, "a keyword limit= argument is clean")

	// Vector 5 — a top-level named constant is clean.
	f, _ = run(`
ARREARS_PAGE_LIMIT = 20

def execute(state, op):
    page, cursor = kv.Links(op.payload["acct"], "postedTo", "in", None, ARREARS_PAGE_LIMIT)
    return {}
`)
	check(len(f) == 0, "a top-level `FOO_PAGE_LIMIT = 20` constant used as the limit is clean")

	// Vector 6 — a helper's own `limit` parameter is clean (value unknown here
	// by construction).
	f, _ = run(`
def list_page(hub, relation, direction, cursor, limit):
    return kv.Links(hub, relation, direction, cursor, limit)
`)
	check(len(f) == 0, "a function-parameter limit is clean; its value is not visible here by construction")

	// Vector 7 — a bare literal at the engine default (256) is a finding.
	f, _ = run(`
def execute(state, op):
    page, _ = kv.Links(op.payload["acct"], "postedTo", "in", None, 256)
    return {}
`)
	check(len(f) == 1 && has(f, "at or above starlark_kv.go's own defaultLinkPageLimit"), "a bare literal limit of 256 is a finding")

	// Vector 8 — a bare literal above the default is also a finding.
	f, _ = run(`
def execute(state, op):
    page, _ = kv.Links(op.payload["acct"], "postedTo", "in", None, 500)
    return {}
`)
	check(len(f) == 1 && has(f, "500"), "a bare literal limit of 500 is a finding")

	// Vector 9 — a NAMED constant resolving to >= 256 is exempt: declaring
	// and naming it is the "author states a real number" this rule chases,
	// whatever number they chose — the check is only for a bare literal that
	// carries no name at all.
	f, _ = run(`
BIG_PAGE_LIMIT = 300

def execute(state, op):
    page, _ = kv.Links(op.payload["acct"], "postedTo", "in", None, BIG_PAGE_LIMIT)
    return {}
`)
	check(len(f) == 0, "a top-level constant resolving to >= 256 is exempt from the bare-literal check")

	// Vector 10 — one below the default (255) is clean.
	f, _ = run(`
def execute(state, op):
    page, _ = kv.Links(op.payload["acct"], "postedTo", "in", None, 255)
    return {}
`)
	check(len(f) == 0, "an explicit limit of 255 (just under the default) is clean")

	// Vector 11 — an unmodelled expression (arithmetic) is neither a finding
	// nor a clean verdict; it is skipped, visible only under VERBOSE=1.
	f, st = run(`
def execute(state, op):
    n = op.payload["n"]
    page, _ = kv.Links(op.payload["acct"], "postedTo", "in", None, n * 2)
    return {}
`)
	check(len(f) == 0 && st.calls == 1, "an arithmetic limit expression is unmodelled — not a finding, still counted as examined")

	// Vector 12 — mutation-style: the fixed shape (a named constant) is clean,
	// and dropping the limit argument on the identical call must fire again.
	fixed := `
ARREARS_PAGE_LIMIT = 20

def execute(state, op):
    page, cursor = kv.Links(op.payload["acct"], "postedTo", "in", None, ARREARS_PAGE_LIMIT)
    return {}
`
	mutated := `
ARREARS_PAGE_LIMIT = 20

def execute(state, op):
    page, cursor = kv.Links(op.payload["acct"], "postedTo", "in")
    return {}
`
	f, _ = run(fixed)
	check(len(f) == 0, "mutation vector baseline: the shipped fix (arrears_entries idiom) is clean")
	f, _ = run(mutated)
	check(len(f) == 1 && has(f, "passes no page limit"), "mutation vector: dropping the limit argument on the identical call makes the gate fire again")

	// Vector 13 — a call unrelated to kv.Links (a same-named method on a
	// different receiver) is not examined.
	f, st = run(`
def execute(state, op):
    page = other.Links(op.payload["acct"], "postedTo", "in")
    return {}
`)
	check(len(f) == 0 && st.calls == 0, "a Links() call on a receiver other than kv is not kv.Links and is not counted")

	// Vector 14 — two findings across two calls in one script both surface.
	f, _ = run(`
def a(state, op):
    page, _ = kv.Links(op.payload["x"], "heldFor", "out")
    return {}

def b(state, op):
    page, _ = kv.Links(op.payload["y"], "postedTo", "in")
    return {}
`)
	check(len(f) == 2, "two unlimited calls across two functions both surface as findings")

	// Vector 15 — a parse failure is not a finding and not an examined script.
	f, st = run("def execute(state, op:\n    return {}\n")
	check(len(f) == 0 && st.scripts == 0, "an unparseable script is neither examined nor reported")

	if !pass {
		fmt.Fprintln(os.Stderr, "lint-links-page-limit: self-test FAILED — the gate does not prove its own vectors; fix the gate before trusting any corpus verdict")
		os.Exit(2)
	}
	if verbose {
		fmt.Println("selftest: all vectors passed")
	}
}
