//go:build ignore

// lint-opmeta-required-fields — a field a script REFUSES without is a field
// the op's descriptor must declare `required`.
//
// THE HAZARD. A DDL's Starlark script and the package's OpMetaSpec are two
// declarations of one operation. The script is the authority at commit time;
// the descriptor is what every descriptor-driven client (internal/descriptorform,
// Facet, each vertical FE's renderOpForm) renders the form from. When a script
// gains a refusal on an absent payload field — `required_string(p, "memo")` —
// and the InputSchema beside it still lists the field outside `required`, the
// form renders it optional, the visitor leaves it blank, and the submit fails
// with a raw InvalidArgument toast and no field-level guidance. The two
// declarations drift silently: neither the opmetas test nor the script test
// reads the other. (Minted: wellness manual-charge memo, 2026-09-06 — the
// descriptor still said optional a fire after the script started refusing.)
//
// THE RULE. For every op-meta with an InputSchema, every payload field the
// script UNCONDITIONALLY requires must appear in that InputSchema's `required`
// array. "Unconditionally requires" is read off the script, not off a helper
// name list: a REQUIRER is a script function whose body refuses (`fail(...)`)
// when its payload argument lacks its name argument — the corpus's
// `required_string(p, name)` / `require_number(p, name)` shapes
// (`if not hasattr(p, name): fail(...)`) — or one that forwards to a requirer
// with the same two arguments (`require_cents` → `require_number`). A field is
// unconditionally required by op X when a requirer is called on the op's
// payload binding (`p = op.payload`) at the TOP LEVEL of X's dispatch block
// (`if ot == "X":` — not nested under a further `if` / `for`, which makes the
// requirement conditional and the descriptor's `optional` legitimately right),
// or at the top level of a script function that block calls with the payload
// forwarded (one hop — this is how `required_status(p)` → `required_string(p,
// "status")`, a block's `build_x(state, p)` helper, and a `post_entry(state,
// op, …)` helper that binds `p = op.payload` itself are read).
//
// A field the descriptor fills itself is as present as a required one — a
// `Dispatch.ContextParams` entry or the `Dispatch.TargetField` (the same
// guarantee lint-package-standard's read-template check reads): the client
// substitutes it before submit and renders no field, so `required` is not
// where its presence is declared. A requirement the scan cannot resolve to a
// literal field name (a requirer called with a variable name) is skipped — this
// gate reports only what it can name, and a false positive would train authors
// to ignore it.
//
// Run: `go run ./scripts/lint-opmeta-required-fields.go` (`STRICT=1` to fail;
// `--list` prints every resolved requirement with its verdict).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"go.starlark.net/syntax"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
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

	var findings []string
	var st stats
	for _, name := range pkgregistry.Names() {
		def, ok := pkgregistry.Lookup(name)
		if !ok {
			findings = append(findings, fmt.Sprintf("%s: pkgregistry.Names() lists this package but Lookup does not resolve it — the corpus enumeration and the registry disagree, so this run cannot claim to have checked it", name))
			continue
		}
		st.packages++
		findings = append(findings, checkPackage(name, def, &st)...)
	}
	if st.scripts == 0 {
		findings = append(findings, "lint-opmeta-required-fields: examined ZERO scripts — the corpus walk is broken, and a gate that checked nothing has no all-clear to give")
	}
	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) == 0 {
		fmt.Printf("lint-opmeta-required-fields: clean — %d script(s) across %d package(s); %d requirer(s) derived, %d unconditional requirement(s) checked against %d described op(s)\n",
			st.scripts, st.packages, st.requirers, st.requirements, st.describedOps)
		return
	}
	fmt.Printf("lint-opmeta-required-fields: %d issue(s) — %d script(s) across %d package(s), %d requirer(s), %d requirement(s) checked against %d described op(s)\n",
		len(findings), st.scripts, st.packages, st.requirers, st.requirements, st.describedOps)
	if strict {
		os.Exit(1)
	}
}

type stats struct {
	packages, scripts, requirers, requirements, describedOps int
}

// requirement is one field an op's script refuses without, with where the
// refusal was resolved from.
type requirement struct {
	op, field, via string
}

// checkPackage resolves every unconditional requirement across the package's
// scripts and checks each against the op-meta that describes its op.
func checkPackage(pkg string, def pkgmgr.Definition, st *stats) []string {
	schemas := map[string]map[string]bool{}  // opType → fields guaranteed present (required, contextParam, targetField)
	declared := map[string]map[string]bool{} // opType → declared properties
	for _, m := range def.OpMetas {
		if m.InputSchema == "" {
			continue
		}
		guaranteed := requiredFields(m.InputSchema)
		if m.Dispatch != nil {
			for field := range m.Dispatch.ContextParams {
				guaranteed[field] = true
			}
			if m.Dispatch.TargetField != "" {
				guaranteed[m.Dispatch.TargetField] = true
			}
		}
		schemas[m.OperationType] = guaranteed
		declared[m.OperationType] = declaredFields(m.InputSchema)
	}
	st.describedOps += len(schemas)

	var findings []string
	for _, d := range def.DDLs {
		if strings.TrimSpace(d.Script) == "" {
			continue
		}
		where := fmt.Sprintf("%s: DDL %s", pkg, d.CanonicalName)
		reqs, err := scriptRequirements(d.Script, st)
		if err != nil {
			// The Processor would refuse this script too; the parse error is
			// the package's own test failure, not this gate's finding.
			continue
		}
		st.scripts++
		for _, r := range reqs {
			st.requirements++
			required, described := schemas[r.op]
			verdict := "undescribed op (no InputSchema) — skipped"
			if described {
				switch {
				case required[r.field]:
					verdict = "ok"
				case !declared[r.op][r.field]:
					verdict = "MISSING — field not declared at all"
					findings = append(findings, fmt.Sprintf("%s: %s refuses without payload field %q (%s), but the op-meta's InputSchema does not declare the field — a descriptor-driven form cannot even offer it, so every submit fails with the script's InvalidArgument. Declare it and list it under required.",
						where, r.op, r.field, r.via))
				default:
					verdict = "OPTIONAL — script requires, schema does not"
					findings = append(findings, fmt.Sprintf("%s: %s refuses without payload field %q (%s), but the op-meta's InputSchema lists it outside `required` — the form renders it optional and a blank submit fails with a raw InvalidArgument and no field-level guidance. List it under required (and pin it in the opmetas test).",
						where, r.op, r.field, r.via))
				}
			}
			if listSites {
				fmt.Printf("  %s · %s requires %q via %s → %s\n", where, r.op, r.field, r.via, verdict)
			}
		}
	}
	return findings
}

// requirer describes one script function that refuses when a payload lacks a
// field: payloadArg / nameArg are the parameter indexes that carry the payload
// and the field name.
type requirer struct {
	payloadArg, nameArg int
}

// scriptRequirements parses one script and returns every unconditional
// requirement its dispatch blocks state.
func scriptRequirements(src string, st *stats) ([]requirement, error) {
	f, err := syntax.Parse("script.star", src, 0)
	if err != nil {
		return nil, err
	}
	defs := map[string]*syntax.DefStmt{}
	for _, s := range f.Stmts {
		if d, ok := s.(*syntax.DefStmt); ok {
			defs[d.Name.Name] = d
		}
	}
	requirers := deriveRequirers(defs)
	st.requirers += len(requirers)

	var out []requirement
	for _, d := range defs {
		// A dispatch function with no payload binding of its own still
		// forwards `op` to helpers that bind one (the op hop below).
		payloadIdent := payloadBinding(d)
		walkDispatch(d.Body, func(op string, body []syntax.Stmt) {
			for _, r := range topLevelRequirements(body, payloadIdent, requirers, defs, 0) {
				r.op = op
				out = append(out, r)
			}
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].op != out[j].op {
			return out[i].op < out[j].op
		}
		return out[i].field < out[j].field
	})
	return out, nil
}

// payloadBinding returns the identifier a function binds to `op.payload`
// (or `op.params`) at its top level, or "" when it binds none.
func payloadBinding(d *syntax.DefStmt) string { return payloadBindingOf(d, "op") }

// payloadBindingOf is payloadBinding with the operation parameter named.
func payloadBindingOf(d *syntax.DefStmt, opName string) string {
	for _, s := range d.Body {
		as, ok := s.(*syntax.AssignStmt)
		if !ok || as.Op != syntax.EQ {
			continue
		}
		id, ok := as.LHS.(*syntax.Ident)
		if !ok {
			continue
		}
		dot, ok := as.RHS.(*syntax.DotExpr)
		if !ok {
			continue
		}
		if x, ok := dot.X.(*syntax.Ident); ok && x.Name == opName && (dot.Name.Name == "payload" || dot.Name.Name == "params") {
			return id.Name
		}
	}
	return ""
}

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

// topLevelRequirements collects the requirer calls at the top level of a
// statement list whose payload argument is `payload`, following one hop into
// a script-defined function the list calls with the payload forwarded.
func topLevelRequirements(stmts []syntax.Stmt, payload string, requirers map[string]requirer, defs map[string]*syntax.DefStmt, depth int) []requirement {
	var out []requirement
	for _, s := range stmts {
		for _, c := range topLevelCalls(s) {
			fn, ok := c.Fn.(*syntax.Ident)
			if !ok {
				continue
			}
			if r, ok := requirers[fn.Name]; ok {
				if field, ok := resolveField(c, r, payload); ok {
					out = append(out, requirement{field: field, via: fn.Name})
				}
				continue
			}
			if depth > 0 {
				continue
			}
			// One hop: a helper the block calls with the payload forwarded —
			// its own top-level requirer calls on that parameter count.
			d, ok := defs[fn.Name]
			if !ok {
				continue
			}
			for i, a := range c.Args {
				id, ok := a.(*syntax.Ident)
				if !ok || i >= len(d.Params) {
					continue
				}
				pid, ok := d.Params[i].(*syntax.Ident)
				if !ok {
					continue
				}
				inner := ""
				switch id.Name {
				case payload:
					inner = pid.Name
				case "op":
					// The helper takes the operation itself and binds its
					// own payload (`p = op.payload`) — the corpus's
					// post_entry / prepare_* shape.
					inner = payloadBindingOf(d, pid.Name)
				}
				if inner == "" {
					continue
				}
				for _, r := range topLevelRequirements(d.Body, inner, requirers, defs, depth+1) {
					r.via = fn.Name + " → " + r.via
					out = append(out, r)
				}
			}
		}
	}
	return out
}

// topLevelCalls returns the calls one statement makes unconditionally: an
// expression statement, an assignment's right-hand side, or a return value.
func topLevelCalls(s syntax.Stmt) []*syntax.CallExpr {
	switch x := s.(type) {
	case *syntax.ExprStmt:
		return callsIn(x.X)
	case *syntax.AssignStmt:
		return callsIn(x.RHS)
	case *syntax.ReturnStmt:
		if x.Result != nil {
			return callsIn(x.Result)
		}
	}
	return nil
}

// callsIn returns the call expressions an expression is built from — the call
// itself, or the calls nested in its arguments (`int(required_string(p, "n"))`).
func callsIn(e syntax.Expr) []*syntax.CallExpr {
	var out []*syntax.CallExpr
	syntax.Walk(e, func(n syntax.Node) bool {
		if c, ok := n.(*syntax.CallExpr); ok {
			out = append(out, c)
		}
		return true
	})
	return out
}

// resolveField reads the literal field name a requirer call states for the
// given payload binding; ok is false when the call is on another payload or
// the name is not a literal.
func resolveField(c *syntax.CallExpr, r requirer, payload string) (string, bool) {
	if r.payloadArg >= len(c.Args) {
		return "", false
	}
	if id, ok := c.Args[r.payloadArg].(*syntax.Ident); !ok || id.Name != payload {
		return "", false
	}
	if r.nameArg >= len(c.Args) {
		return "", false
	}
	lit := stringLit(c.Args[r.nameArg])
	return lit, lit != ""
}

// deriveRequirers reads the requirer set off the script's own definitions: a
// base requirer refuses at its top level when `hasattr(p, name)` is false; a
// forwarding requirer calls a requirer on its own payload and name parameters
// at its top level. Iterates to a fixed point. A helper that calls a requirer
// with a LITERAL name is not a requirer — it is read by the one-hop rule, which
// collects every field it requires rather than the first.
func deriveRequirers(defs map[string]*syntax.DefStmt) map[string]requirer {
	out := map[string]requirer{}
	for name, d := range defs {
		if r, ok := baseRequirer(d); ok {
			out[name] = r
		}
	}
	for changed := true; changed; {
		changed = false
		for name, d := range defs {
			if _, done := out[name]; done {
				continue
			}
			if r, ok := forwardingRequirer(d, out); ok {
				out[name] = r
				changed = true
			}
		}
	}
	return out
}

// baseRequirer matches `def f(p, name, ...)` whose top level holds
// `if not hasattr(p, name): fail(...)` (the corpus's refusal-on-absence shape).
func baseRequirer(d *syntax.DefStmt) (requirer, bool) {
	if len(d.Params) < 2 {
		return requirer{}, false
	}
	p, ok1 := d.Params[0].(*syntax.Ident)
	n, ok2 := d.Params[1].(*syntax.Ident)
	if !ok1 || !ok2 {
		return requirer{}, false
	}
	for _, s := range d.Body {
		ifs, ok := s.(*syntax.IfStmt)
		if !ok {
			continue
		}
		u, ok := ifs.Cond.(*syntax.UnaryExpr)
		if !ok || u.Op != syntax.NOT {
			continue
		}
		c, ok := u.X.(*syntax.CallExpr)
		if !ok || len(c.Args) != 2 {
			continue
		}
		if fn, ok := c.Fn.(*syntax.Ident); !ok || fn.Name != "hasattr" {
			continue
		}
		a0, ok0 := c.Args[0].(*syntax.Ident)
		a1, ok1 := c.Args[1].(*syntax.Ident)
		if !ok0 || !ok1 || a0.Name != p.Name || a1.Name != n.Name {
			continue
		}
		if containsFail(ifs.True) {
			return requirer{payloadArg: 0, nameArg: 1}, true
		}
	}
	return requirer{}, false
}

// forwardingRequirer matches a function whose top level calls a known
// requirer with its own parameters as payload and name (`require_cents(p,
// name)` → `require_number(p, name)`).
func forwardingRequirer(d *syntax.DefStmt, known map[string]requirer) (requirer, bool) {
	params := map[string]int{}
	for i, p := range d.Params {
		if id, ok := p.(*syntax.Ident); ok {
			params[id.Name] = i
		}
	}
	for _, s := range d.Body {
		for _, c := range topLevelCalls(s) {
			fn, ok := c.Fn.(*syntax.Ident)
			if !ok {
				continue
			}
			r, ok := known[fn.Name]
			if !ok || r.payloadArg >= len(c.Args) {
				continue
			}
			pid, ok := c.Args[r.payloadArg].(*syntax.Ident)
			if !ok {
				continue
			}
			pi, ok := params[pid.Name]
			if !ok {
				continue
			}
			if r.nameArg >= len(c.Args) {
				continue
			}
			if nid, ok := c.Args[r.nameArg].(*syntax.Ident); ok {
				if ni, ok := params[nid.Name]; ok {
					return requirer{payloadArg: pi, nameArg: ni}, true
				}
			}
		}
	}
	return requirer{}, false
}

func containsFail(stmts []syntax.Stmt) bool {
	found := false
	for _, s := range stmts {
		syntax.Walk(s, func(n syntax.Node) bool {
			if c, ok := n.(*syntax.CallExpr); ok {
				if fn, ok := c.Fn.(*syntax.Ident); ok && fn.Name == "fail" {
					found = true
				}
			}
			return !found
		})
	}
	return found
}

// requiredFields reads the `required` array out of an op-meta's InputSchema;
// an unparseable schema yields none (lint-package-standard reports that).
func requiredFields(schema string) map[string]bool {
	out := map[string]bool{}
	var parsed struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal([]byte(schema), &parsed); err != nil {
		return out
	}
	for _, f := range parsed.Required {
		out[f] = true
	}
	return out
}

// declaredFields reads the property names an InputSchema declares.
func declaredFields(schema string) map[string]bool {
	out := map[string]bool{}
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(schema), &parsed); err != nil {
		return out
	}
	for f := range parsed.Properties {
		out[f] = true
	}
	return out
}

// runSelfTest pins the scan against a fixture: a base requirer, a forwarding
// requirer, a literal-name helper read by the one-hop rule (required_status),
// an unconditional requirement, a conditional one (nested if — must NOT
// count), a one-hop helper requirement, an optional helper (no fail — must NOT
// be a requirer), and `in [...]` / `or` dispatches.
func runSelfTest(verbose bool) {
	const fixture = `
def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    return getattr(p, name)

def optional_string(p, name):
    if not hasattr(p, name):
        return ""
    return getattr(p, name)

def require_number(p, name):
    if not hasattr(p, name):
        fail("required")
    return getattr(p, name)

def require_cents(p, name):
    v = require_number(p, name)
    return int(v)

def required_status(p):
    s = required_string(p, "status")
    return s

def build_thing(state, q):
    note = required_string(q, "note")
    return note

def post_entry(state, o, kind):
    q = o.payload
    acct = required_string(q, "accountKey")
    return acct

def execute(state, op):
    ot = op.operationType
    p = op.payload
    if ot == "Alpha":
        name = required_string(p, "name")
        memo = optional_string(p, "memo")
        cents = require_cents(p, "amountCents")
        st = required_status(p)
        if memo:
            required_string(p, "onlyWhenMemo")
        build_thing(state, p)
    if ot in ["Beta", "Gamma"]:
        n = int(required_string(p, "count"))
    if ot == "Delta" or ot == "Epsilon":
        required_string(p, "shared")
    if ot == "Zeta":
        return post_entry(state, op, "debit")
`
	var st stats
	reqs, err := scriptRequirements(fixture, &st)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint-opmeta-required-fields: SELFTEST parse error: %v\n", err)
		os.Exit(2)
	}
	got := map[string]bool{}
	for _, r := range reqs {
		got[r.op+"."+r.field] = true
	}
	want := []string{"Alpha.name", "Alpha.amountCents", "Alpha.status", "Alpha.note", "Beta.count", "Gamma.count", "Delta.shared", "Epsilon.shared", "Zeta.accountKey"}
	for _, w := range want {
		if !got[w] {
			fmt.Fprintf(os.Stderr, "lint-opmeta-required-fields: SELFTEST expected requirement %s missing (got %v)\n", w, reqs)
			os.Exit(2)
		}
	}
	for _, bad := range []string{"Alpha.memo", "Alpha.onlyWhenMemo"} {
		if got[bad] {
			fmt.Fprintf(os.Stderr, "lint-opmeta-required-fields: SELFTEST %s must not be reported as unconditional\n", bad)
			os.Exit(2)
		}
	}
	if st.requirers != 3 {
		fmt.Fprintf(os.Stderr, "lint-opmeta-required-fields: SELFTEST derived %d requirers, want 3 (required_string, require_number, require_cents)\n", st.requirers)
		os.Exit(2)
	}
	if verbose {
		fmt.Printf("lint-opmeta-required-fields: selftest ok — %d requirements resolved\n", len(reqs))
	}
}
