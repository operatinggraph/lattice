//go:build ignore

// lint-date-field-normalized — a payload field the op-meta describes as a
// date / date-time (or whose description names it RFC3339) must be
// normalized before the script stores it.
//
// THE HAZARD. A DDL script accepts a caller-supplied instant two shapes wide
// — a bare "YYYY-MM-DD" (the FE's <input type=date>) or a full RFC3339
// instant, sometimes with a non-UTC offset — and every downstream reader
// (a lens `WHERE` compare, a convergence gap's overlap test, a Weaver
// temporal-lane parse) assumes the stored value is canonical UTC,
// whole-second, Z-suffixed RFC3339: two equal instants written in different
// shapes compare UNEQUAL as strings, and a lens comparing "the stored date"
// against "today" silently drifts by the caller's own UTC offset. The
// dossier entry "A field a self-scoped op stores as informational becomes
// load-bearing" (`docs/components/_packages.md`) names two of these misses
// by hand (`.terms.moveInDate` before lease-signing 0.35.0; `availableFrom`
// before loftspace-domain's `required_instant` landed, loftspace-domain
// 0.15.0) — both merged with the field stored verbatim, the drift caught
// only once a second op started comparing it. This gate mechanizes the
// date-format half of that check so a new date-typed field lands normalized
// the first time, not the second.
//
// THE RULE.
//
//  1. A DATE-TYPED FIELD is a property of an op-meta's InputSchema (the
//     shipped package corpus, read the same way as lint-opmeta-required-
//     fields: pkgregistry -> Definition.OpMetas) whose declared
//     `"format":"date-time"` or `"format":"date"`, OR whose `"description"`
//     contains the substring "RFC3339" (a property the schema never
//     format-tagged, spelled only in prose — the corpus has both: compare
//     loftspace-domain's `availableFrom`, which carries `format":"date-
//     time"` today, against its own pre-0.15.0 revision, which carried only
//     the RFC3339 prose — see the selftest). Both are read off the ACTUAL
//     `InputSchema` JSON string the package ships (parsed, not grepped), so
//     a schema built from concatenated Go string literals is read exactly
//     as pkgmgr/pkgregistry resolves it.
//
//  2. THE FIELD'S RAW VALUE is what a dispatch closure gets by reading it
//     off the payload: `required_string(p, "<field>")` / `optional_string(p,
//     "<field>")` / any script-defined function shaped `def f(p, name):`
//     whose body calls `getattr(p, name)` on its own two parameters (the
//     corpus's required_string/optional_string/require_number/
//     optional_number family, derived off the script itself, not a name
//     list) / a bare `getattr(p, "<field>")` / a bare `p.<field>` dot
//     access. The BINDING — the variable a `v = <one of the above>`
//     assignment produces — is what taint is tracked from; the field is
//     also considered read wherever one of these calls is nested directly
//     inside a normalizer call (`time.rfc3339_utc(required_string(p,
//     "startsAt"))`, no intermediate variable).
//
//  3. NORMALIZED means: somewhere in the dispatch closure, a value that
//     carries the field's raw reading reaches a call to `time.rfc3339_utc`
//     — directly, through one `as_rfc3339_instant(...)` wrap (the corpus's
//     bare-date-to-midnight-UTC formatter — named explicitly because,
//     unlike every other helper here, it is derived by NAME, not by shape:
//     it performs no `time.rfc3339_utc` call itself), through simple
//     reassignment / string concatenation (`s = s + "T00:00:00Z"`), or
//     through a call to a script-defined "normalizer" function. Two
//     normalizer shapes are derived from the script, not named:
//     - a REQUIRER-NORMALIZER: `def f(p, name):` whose body binds a
//     variable via a reader call on its OWN two parameters and passes
//     it (again, directly, via `as_rfc3339_instant`, or via
//     concatenation) into `time.rfc3339_utc` — the corpus's
//     `required_instant` (service-domain, loftspace-domain) and
//     `required_date_instant` (lease-signing) are all instances of this
//     one shape, not a name this gate hardcodes. Calling such a
//     function on `(payload, "<field>")` both reads AND normalizes the
//     field in one call.
//     - a WRAPPER-NORMALIZER: any script-defined one-argument function
//     whose body passes its own parameter (again, directly / via
//     `as_rfc3339_instant` / via concatenation) into `time.rfc3339_utc`
//     — clinic-domain's `normalize_follow_up_date` is the corpus's
//     instance. Calling such a function on an already-tainted value
//     satisfies normalization even with no further wrapping at the call
//     site.
//     "Somewhere in the closure" is DELIBERATELY the whole rule — this gate
//     does not additionally prove the normalized value is what gets WRITTEN
//     into the mutation (that would need to track every dict-literal /
//     `make_aspect` argument the corpus builds, which the opmeta-required-
//     fields gate does not attempt either); a field read and normalized but
//     then discarded would pass here and is out of this gate's scope.
//     Resolution reaches one hop into a script-defined helper the dispatch
//     block calls with the payload forwarded (mirrors lint-opmeta-required-
//     fields' one-hop rule: a `build_x(state, p)` / `post_entry(state, op,
//     kind)` shape that binds its own `p = op.payload`).
//
//  4. A field this gate cannot prove normalized is a finding UNLESS the
//     dispatch block carries a same-field exemption comment, literally
//     `# date-field-exempt: <field> — <reason>`, anywhere inside the
//     matched `if ot == "X":` block's own line span (never a sibling
//     block's). The reason is mandatory — an empty or missing reason does
//     not exempt. This is the ONLY way to silence a finding besides
//     actually normalizing the field: there is no package-wide or op-wide
//     suppression. A field the gate cannot resolve to a dispatch block at
//     all (the op-meta names an op no DDL script in the package dispatches)
//     is reported too — a gate that silently skipped what it could not find
//     would have no all-clear to give.
//
// Run: `go run ./scripts/lint-date-field-normalized.go` (`STRICT=1` to fail;
// `--list` prints every resolved field with its verdict; `--selftest`
// replays this gate's own two minting incidents — the pre-normalization
// revision of lease-signing's `CreateLeaseApplication`/`moveInDate` and of
// loftspace-domain's `SetListing`/`availableFrom` — and asserts each still
// fails the gate today, skipping with a printed SKIP when the checkout's
// history does not reach that far back, e.g. a depth-1 CI clone).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"go.starlark.net/syntax"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
)

var listSites = false

func main() {
	strict := os.Getenv("STRICT") == "1"
	for _, a := range os.Args[1:] {
		switch a {
		case "--selftest":
			os.Exit(runSelfTest())
		case "--list":
			listSites = true
		}
	}

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
	if st.checked == 0 {
		findings = append(findings, "lint-date-field-normalized: examined ZERO date-typed fields — the corpus walk is broken, and a gate that checked nothing has no all-clear to give")
	}
	sort.Strings(findings)
	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) == 0 {
		fmt.Printf("lint-date-field-normalized: clean — %d package(s), %d described op(s), %d date-typed field(s) checked (%d normalized, %d exempt)\n",
			st.packages, st.describedOps, st.checked, st.normalized, st.exempt)
		return
	}
	fmt.Printf("lint-date-field-normalized: %d issue(s) — %d package(s), %d described op(s), %d date-typed field(s) checked (%d normalized, %d exempt)\n",
		len(findings), st.packages, st.describedOps, st.checked, st.normalized, st.exempt)
	if strict {
		os.Exit(1)
	}
}

type stats struct {
	packages, describedOps, checked, normalized, exempt int
}

// requirer describes one script function that reads a payload field: the
// argument indexes carrying the payload and the field name.
type requirer struct {
	payloadArg, nameArg int
}

// checkPackage resolves every date-typed field this package's op-metas
// declare and checks whether the package's own DDL scripts normalize it.
func checkPackage(pkg string, def pkgmgr.Definition, st *stats) []string {
	targets := map[string]map[string]string{} // op -> field -> why it is date-typed
	for _, m := range def.OpMetas {
		if m.InputSchema == "" {
			continue
		}
		fields := dateFields(m.InputSchema)
		if len(fields) == 0 {
			continue
		}
		st.describedOps++
		targets[m.OperationType] = fields
	}
	if len(targets) == 0 {
		return nil
	}

	type verdict struct {
		normalized bool
		exempt     bool
		reason     string
		sites      int
	}
	verdicts := map[string]*verdict{}
	verdictFor := func(op, field string) *verdict {
		key := op + "\x00" + field
		v := verdicts[key]
		if v == nil {
			v = &verdict{}
			verdicts[key] = v
		}
		return v
	}

	for _, d := range def.DDLs {
		if strings.TrimSpace(d.Script) == "" {
			continue
		}
		f, err := syntax.Parse("script.star", d.Script, 0)
		if err != nil {
			// The Processor would refuse this script too; the parse error is
			// the package's own test failure, not this gate's finding.
			continue
		}
		defs := map[string]*syntax.DefStmt{}
		for _, s := range f.Stmts {
			if dd, ok := s.(*syntax.DefStmt); ok {
				defs[dd.Name.Name] = dd
			}
		}
		readers := deriveReaders(defs)
		normReqs := deriveNormalizerRequirers(defs, readers)
		wraps := deriveWrapperNormalizers(defs)
		env := &scanEnv{readers: readers, normReqs: normReqs, wraps: wraps}

		for _, dd := range defs {
			payload := payloadBinding(dd)
			walkDispatch(dd.Body, func(op string, body []syntax.Stmt) {
				fields, ok := targets[op]
				if !ok {
					return
				}
				for field := range fields {
					v := verdictFor(op, field)
					v.sites++
					if blockNormalizes(env, body, payload, field, defs, 0) {
						v.normalized = true
					}
					if ex, reason := exemptionFor(d.Script, body, field); ex {
						v.exempt = true
						v.reason = reason
					}
				}
			})
		}
	}

	var findings []string
	for op, fields := range targets {
		for field, why := range fields {
			st.checked++
			v := verdicts[op+"\x00"+field]
			switch {
			case v == nil:
				findings = append(findings, fmt.Sprintf("%s: op %s field %q (%s) — no dispatch block for %s was found in any of this package's DDL scripts; the field could not be checked at all",
					pkg, op, field, why, op))
			case v.normalized:
				st.normalized++
				if listSites {
					fmt.Printf("  %s: %s.%s (%s) → normalized\n", pkg, op, field, why)
				}
			case v.exempt:
				st.exempt++
				if listSites {
					fmt.Printf("  %s: %s.%s (%s) → EXEMPT — %s\n", pkg, op, field, why, v.reason)
				}
			default:
				findings = append(findings, fmt.Sprintf("%s: op %s field %q (%s) — the dispatch closure never passes this payload field through time.rfc3339_utc (directly, via as_rfc3339_instant, or via a script-defined requirer-normalizer / wrapper-normalizer). Normalize at the mint (mirror required_instant / required_date_instant / as_rfc3339_instant+time.rfc3339_utc), or add \"# date-field-exempt: %s — <reason>\" inside the %s dispatch block if the field is genuinely not a stored date (e.g. a display-only echo).",
					pkg, op, field, why, field, op))
			}
		}
	}
	return findings
}

// dateFields reads the property names an InputSchema declares as date /
// date-time typed, or RFC3339 by description, mapped to why.
func dateFields(schema string) map[string]string {
	var parsed struct {
		Properties map[string]struct {
			Format      string `json:"format"`
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(schema), &parsed); err != nil {
		return nil
	}
	out := map[string]string{}
	for name, p := range parsed.Properties {
		switch {
		case p.Format == "date-time":
			out[name] = `format="date-time"`
		case p.Format == "date":
			out[name] = `format="date"`
		case strings.Contains(p.Description, "RFC3339"):
			out[name] = "description mentions RFC3339"
		}
	}
	return out
}

// exemptionFor reports whether the dispatch block's own source lines carry
// `# date-field-exempt: <field> — <reason>`, and the stated reason.
func exemptionFor(src string, body []syntax.Stmt, field string) (bool, string) {
	start, end := blockLineRange(body)
	if start > end {
		return false, ""
	}
	lines := strings.Split(src, "\n")
	re := regexp.MustCompile(`#\s*date-field-exempt:\s*` + regexp.QuoteMeta(field) + `\s*[—-]\s*(\S.*)$`)
	for i := start; i <= end && i >= 1 && i-1 < len(lines); i++ {
		if m := re.FindStringSubmatch(strings.TrimSpace(lines[i-1])); m != nil {
			return true, strings.TrimSpace(m[1])
		}
	}
	return false, ""
}

// blockLineRange returns the 1-based [start,end] source line span every
// statement in body spans, recursively.
func blockLineRange(body []syntax.Stmt) (start, end int) {
	start, end = 1<<30, 0
	for _, s := range body {
		syntax.Walk(s, func(n syntax.Node) bool {
			if n == nil {
				return true
			}
			a, b := n.Span()
			if a.Line > 0 && int(a.Line) < start {
				start = int(a.Line)
			}
			if b.Line > 0 && int(b.Line) > end {
				end = int(b.Line)
			}
			return true
		})
	}
	return start, end
}

// scanEnv is the derived vocabulary one script's normalization is checked
// against: readers give a field's raw value, normReqs give it already
// normalized (both shaped `f(payload, name)`), wraps normalize a single
// already-tainted argument in place.
type scanEnv struct {
	readers  map[string]requirer
	normReqs map[string]requirer
	wraps    map[string]bool
}

// fieldMatcher recognises the "name" argument of a reader/normalizer call:
// a literal field name at a dispatch site, or an identifier bound to a
// script-defined function's own `name` parameter when deriving that
// function's shape generically.
type fieldMatcher func(e syntax.Expr) bool

func literalMatch(field string) fieldMatcher {
	return func(e syntax.Expr) bool { return stringLit(e) == field }
}

func identMatch(name string) fieldMatcher {
	return func(e syntax.Expr) bool {
		id, ok := e.(*syntax.Ident)
		return ok && id.Name == name
	}
}

// isGetattrRead reports whether call is a bare `getattr(payload, "<field>")`
// (nameMatch decides what counts as "<field>") — always a RAW read, never a
// normalizer, regardless of which requirer map the caller is checking.
func isGetattrRead(c *syntax.CallExpr, payload string, nameMatch fieldMatcher) bool {
	fn, ok := c.Fn.(*syntax.Ident)
	if !ok || fn.Name != "getattr" || len(c.Args) != 2 {
		return false
	}
	id, ok := c.Args[0].(*syntax.Ident)
	return ok && id.Name == payload && nameMatch(c.Args[1])
}

// isNamedCall reports whether call invokes one of the script-defined
// functions in m — a reader (raw value) or a normalizer-requirer (already
// normalized), depending on which map the caller passes — shaped
// `f(payload, name)` per m's recorded argument positions.
func isNamedCall(c *syntax.CallExpr, payload string, nameMatch fieldMatcher, m map[string]requirer) bool {
	fn, ok := c.Fn.(*syntax.Ident)
	if !ok {
		return false
	}
	r, ok := m[fn.Name]
	if !ok || r.payloadArg >= len(c.Args) || r.nameArg >= len(c.Args) {
		return false
	}
	id, ok := c.Args[r.payloadArg].(*syntax.Ident)
	return ok && id.Name == payload && nameMatch(c.Args[r.nameArg])
}

func isRfc3339UtcCall(c *syntax.CallExpr) bool {
	dot, ok := c.Fn.(*syntax.DotExpr)
	if !ok {
		return false
	}
	id, ok := dot.X.(*syntax.Ident)
	return ok && id.Name == "time" && dot.Name.Name == "rfc3339_utc"
}

// isTainted reports whether expr e's value is known to carry the target
// field's reading (raw, or already run through a wrapper/as_rfc3339_instant
// normalizer, or simple string concatenation of one). dotField is the
// literal field name for a `payload.field` dot-access check — pass "" in
// generic (function-derivation) mode, where dot access cannot name a
// dynamic field at all.
func (e *scanEnv) isTainted(x syntax.Expr, tainted map[string]bool, payload string, nameMatch fieldMatcher, dotField string) bool {
	switch v := x.(type) {
	case *syntax.Ident:
		return tainted[v.Name]
	case *syntax.ParenExpr:
		return e.isTainted(v.X, tainted, payload, nameMatch, dotField)
	case *syntax.DotExpr:
		if dotField == "" {
			return false
		}
		if id, ok := v.X.(*syntax.Ident); ok && id.Name == payload && v.Name.Name == dotField {
			return true
		}
	case *syntax.BinaryExpr:
		if v.Op == syntax.PLUS {
			return e.isTainted(v.X, tainted, payload, nameMatch, dotField) || e.isTainted(v.Y, tainted, payload, nameMatch, dotField)
		}
	case *syntax.CallExpr:
		if fn, ok := v.Fn.(*syntax.Ident); ok {
			if fn.Name == "as_rfc3339_instant" && len(v.Args) == 1 {
				return e.isTainted(v.Args[0], tainted, payload, nameMatch, dotField)
			}
			if e.wraps[fn.Name] && len(v.Args) >= 1 {
				return e.isTainted(v.Args[0], tainted, payload, nameMatch, dotField)
			}
		}
		if isGetattrRead(v, payload, nameMatch) || isNamedCall(v, payload, nameMatch, e.readers) || isNamedCall(v, payload, nameMatch, e.normReqs) {
			return true
		}
	}
	return false
}

// exprNormalizes reports whether expr x itself constitutes evidence the
// field has been normalized somewhere within it: a time.rfc3339_utc call on
// a tainted argument, a direct normReqs call naming the field, or a wraps
// call on a tainted argument — searched anywhere in x's subtree (a dict /
// list literal value counts).
func (e *scanEnv) exprNormalizes(x syntax.Expr, tainted map[string]bool, payload string, nameMatch fieldMatcher, dotField string) bool {
	found := false
	syntax.Walk(x, func(n syntax.Node) bool {
		if found {
			return false
		}
		c, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		if isRfc3339UtcCall(c) && len(c.Args) >= 1 && e.isTainted(c.Args[0], tainted, payload, nameMatch, dotField) {
			found = true
			return false
		}
		if isNamedCall(c, payload, nameMatch, e.normReqs) {
			found = true
			return false
		}
		if fn, ok := c.Fn.(*syntax.Ident); ok && e.wraps[fn.Name] && len(c.Args) >= 1 && e.isTainted(c.Args[0], tainted, payload, nameMatch, dotField) {
			found = true
			return false
		}
		return true
	})
	return found
}

// scan performs the sequential, non-scoped taint walk described in the
// header's rule 3: seeds tainted[v] whenever v is assigned a tainted
// expression, and reports true the moment any statement's expression is
// itself normalizing evidence.
func (e *scanEnv) scan(stmts []syntax.Stmt, payload string, nameMatch fieldMatcher, dotField string, tainted map[string]bool) bool {
	for _, s := range stmts {
		switch st := s.(type) {
		case *syntax.AssignStmt:
			if st.Op != syntax.EQ {
				continue
			}
			if e.exprNormalizes(st.RHS, tainted, payload, nameMatch, dotField) {
				return true
			}
			if id, ok := st.LHS.(*syntax.Ident); ok {
				if e.isTainted(st.RHS, tainted, payload, nameMatch, dotField) {
					tainted[id.Name] = true
				}
			}
		case *syntax.ExprStmt:
			if e.exprNormalizes(st.X, tainted, payload, nameMatch, dotField) {
				return true
			}
		case *syntax.ReturnStmt:
			if st.Result != nil && e.exprNormalizes(st.Result, tainted, payload, nameMatch, dotField) {
				return true
			}
		case *syntax.IfStmt:
			if e.exprNormalizes(st.Cond, tainted, payload, nameMatch, dotField) {
				return true
			}
			if e.scan(st.True, payload, nameMatch, dotField, tainted) {
				return true
			}
			if e.scan(st.False, payload, nameMatch, dotField, tainted) {
				return true
			}
		case *syntax.ForStmt:
			if e.scan(st.Body, payload, nameMatch, dotField, tainted) {
				return true
			}
		}
	}
	return false
}

// blockNormalizes reports whether the dispatch block body normalizes field
// somewhere in its closure — the block itself, or one hop into a
// script-defined helper the block calls with the payload (or the op itself)
// forwarded, mirroring lint-opmeta-required-fields' one-hop rule.
func blockNormalizes(env *scanEnv, body []syntax.Stmt, payload, field string, defs map[string]*syntax.DefStmt, depth int) bool {
	if env.scan(body, payload, literalMatch(field), field, map[string]bool{}) {
		return true
	}
	if depth > 0 {
		return false
	}
	for _, c := range topLevelCalls(body) {
		fn, ok := c.Fn.(*syntax.Ident)
		if !ok {
			continue
		}
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
				inner = payloadBindingOf(d, pid.Name)
			}
			if inner == "" {
				continue
			}
			if blockNormalizes(env, d.Body, inner, field, defs, depth+1) {
				return true
			}
		}
	}
	return false
}

// topLevelCalls returns the calls each statement in stmts makes
// unconditionally at ITS OWN level (an expression statement, an
// assignment's right-hand side, or a return value) — mirrors
// lint-opmeta-required-fields' topLevelCalls/callsIn, used here only to
// find one-hop helper calls, not to bound the normalization search itself
// (which recurses through if/for via scan).
func topLevelCalls(stmts []syntax.Stmt) []*syntax.CallExpr {
	var out []*syntax.CallExpr
	for _, s := range stmts {
		var e syntax.Expr
		switch x := s.(type) {
		case *syntax.ExprStmt:
			e = x.X
		case *syntax.AssignStmt:
			e = x.RHS
		case *syntax.ReturnStmt:
			e = x.Result
		}
		if e == nil {
			continue
		}
		syntax.Walk(e, func(n syntax.Node) bool {
			if c, ok := n.(*syntax.CallExpr); ok {
				out = append(out, c)
			}
			return true
		})
	}
	return out
}

// deriveReaders finds every script-defined function shaped `def f(p, name):`
// whose body calls `getattr(p, name)` on its own first two parameters —
// the corpus's required_string / optional_string / require_number /
// optional_number family, derived by shape rather than name.
func deriveReaders(defs map[string]*syntax.DefStmt) map[string]requirer {
	out := map[string]requirer{}
	for name, d := range defs {
		if len(d.Params) < 2 {
			continue
		}
		p0, ok0 := d.Params[0].(*syntax.Ident)
		p1, ok1 := d.Params[1].(*syntax.Ident)
		if !ok0 || !ok1 {
			continue
		}
		if bodyReadsGetattr(d.Body, p0.Name, p1.Name) {
			out[name] = requirer{0, 1}
		}
	}
	return out
}

func bodyReadsGetattr(stmts []syntax.Stmt, p, n string) bool {
	found := false
	for _, s := range stmts {
		syntax.Walk(s, func(node syntax.Node) bool {
			if found {
				return false
			}
			c, ok := node.(*syntax.CallExpr)
			if !ok {
				return true
			}
			fn, ok := c.Fn.(*syntax.Ident)
			if !ok || fn.Name != "getattr" || len(c.Args) != 2 {
				return true
			}
			id0, ok0 := c.Args[0].(*syntax.Ident)
			id1, ok1 := c.Args[1].(*syntax.Ident)
			if ok0 && ok1 && id0.Name == p && id1.Name == n {
				found = true
			}
			return true
		})
	}
	return found
}

// deriveNormalizerRequirers finds every script-defined function shaped
// `def f(p, name):` whose body both reads its own (p, name) pair (via a
// reader from readers) and passes the resulting value into
// time.rfc3339_utc, directly or via as_rfc3339_instant/concatenation — the
// corpus's required_instant and required_date_instant are both instances of
// this shape.
func deriveNormalizerRequirers(defs map[string]*syntax.DefStmt, readers map[string]requirer) map[string]requirer {
	env := &scanEnv{readers: readers}
	out := map[string]requirer{}
	for name, d := range defs {
		if len(d.Params) < 2 {
			continue
		}
		p0, ok0 := d.Params[0].(*syntax.Ident)
		p1, ok1 := d.Params[1].(*syntax.Ident)
		if !ok0 || !ok1 {
			continue
		}
		if env.scan(d.Body, p0.Name, identMatch(p1.Name), "", map[string]bool{}) {
			out[name] = requirer{0, 1}
		}
	}
	return out
}

// deriveWrapperNormalizers finds every script-defined function of at least
// one parameter whose body passes that parameter's value into
// time.rfc3339_utc, directly or via as_rfc3339_instant/concatenation —
// clinic-domain's normalize_follow_up_date is the corpus's instance.
func deriveWrapperNormalizers(defs map[string]*syntax.DefStmt) map[string]bool {
	out := map[string]bool{}
	for name, d := range defs {
		if len(d.Params) < 1 {
			continue
		}
		p0, ok := d.Params[0].(*syntax.Ident)
		if !ok {
			continue
		}
		env := &scanEnv{}
		tainted := map[string]bool{p0.Name: true}
		if env.scan(d.Body, p0.Name, func(syntax.Expr) bool { return false }, "", tainted) {
			out[name] = true
		}
	}
	return out
}

// payloadBinding returns the identifier a function binds to `op.payload`
// (or `op.params`) at its top level, or "" when it binds none.
func payloadBinding(d *syntax.DefStmt) string { return payloadBindingOf(d, "op") }

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

// runSelfTest replays this gate's own two minting incidents: the revision
// of lease-signing/scripts.go just before `as_rfc3339_instant(move_in)` was
// added (CreateLeaseApplication/moveInDate stored raw), and the revision of
// loftspace-domain/ddls.go just before `required_instant` was added
// (SetListing/availableFrom stored raw). Both were found by `git log -S
// '<marker>' --oneline -- <path> | tail -1` (the earliest commit adding the
// marker) and this replays that commit's PARENT. `git cat-file -e` gates
// each replay so a depth-1 CI clone skips it with a printed SKIP instead of
// failing.
func runSelfTest() int {
	ok := true
	replay := func(label, sha, path, scriptVar, op, field string) {
		ref := sha + ":" + path
		if exec.Command("git", "cat-file", "-e", ref).Run() != nil {
			fmt.Println("SKIP  selftest  " + label + " — " + ref + " not in this checkout's history (depth-1 clone)")
			return
		}
		blob, err := exec.Command("git", "show", ref).Output()
		if err != nil {
			fmt.Println("FAIL  selftest  " + label + " — could not read " + ref + ": " + err.Error())
			ok = false
			return
		}
		script, err := extractBacktickConst(string(blob), scriptVar)
		if err != nil {
			fmt.Println("FAIL  selftest  " + label + " — " + err.Error())
			ok = false
			return
		}
		f, err := syntax.Parse("script.star", script, 0)
		if err != nil {
			fmt.Println("FAIL  selftest  " + label + " — parse error: " + err.Error())
			ok = false
			return
		}
		defs := map[string]*syntax.DefStmt{}
		for _, s := range f.Stmts {
			if dd, ok := s.(*syntax.DefStmt); ok {
				defs[dd.Name.Name] = dd
			}
		}
		readers := deriveReaders(defs)
		env := &scanEnv{
			readers:  readers,
			normReqs: deriveNormalizerRequirers(defs, readers),
			wraps:    deriveWrapperNormalizers(defs),
		}
		found := false
		for _, dd := range defs {
			payload := payloadBinding(dd)
			walkDispatch(dd.Body, func(gotOp string, body []syntax.Stmt) {
				if gotOp != op {
					return
				}
				if blockNormalizes(env, body, payload, field, defs, 0) {
					found = true
				}
			})
		}
		pass := !found // the minting incident must FAIL the gate — i.e. NOT be found normalized
		tag := "PASS"
		if !pass {
			tag, ok = "FAIL", false
		}
		fmt.Printf("%s  selftest  %s — %s/%s at %s reports NOT normalized\n", tag, label, op, field, sha)
	}

	replay("lease-signing pre-normalization moveInDate",
		"a95d38d0ab4084642bf1cf52d2b3eb217b388ae8", "packages/lease-signing/scripts.go", "leaseAppDDLScript",
		"CreateLeaseApplication", "moveInDate")
	replay("loftspace-domain pre-normalization availableFrom",
		"b4f16e6722adac5b9271cea8bf7a5751e6c4d6cd", "packages/loftspace-domain/ddls.go", "loftspaceListingDDLScript",
		"SetListing", "availableFrom")

	if !ok {
		return 1
	}
	fmt.Println("lint-date-field-normalized: selftest clean")
	return 0
}

// extractBacktickConst does a simple scan for `<varName> = ` (optionally
// wrapped, e.g. `strings.Replace(` / `fmt.Sprintf(`) followed by a raw
// backtick string literal, returning its content up to the next backtick —
// mirrors how the script constants are actually declared across the
// corpus (a single backtick-delimited raw string, no embedded backticks).
func extractBacktickConst(src, varName string) (string, error) {
	re := regexp.MustCompile(`(?m)^(?:var|const)\s+` + regexp.QuoteMeta(varName) + `\s*=`)
	loc := re.FindStringIndex(src)
	if loc == nil {
		return "", fmt.Errorf("declaration of %s not found", varName)
	}
	rest := src[loc[1]:]
	start := strings.IndexByte(rest, '`')
	if start < 0 {
		return "", fmt.Errorf("%s: no backtick literal found after its declaration", varName)
	}
	rest = rest[start+1:]
	end := strings.IndexByte(rest, '`')
	if end < 0 {
		return "", fmt.Errorf("%s: unterminated backtick literal", varName)
	}
	return rest[:end], nil
}
