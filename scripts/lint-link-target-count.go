//go:build ignore

// lint-link-target-count — a COUNT over link targets counts live vertices only.
//
// THE HAZARD. A soft-delete cascades onto no link: TombstoneLocation leaves
// every practicesAt link pointing at the dead building live, TombstoneProvider
// leaves the withProvider links, and so on for every Tombstone* op in the
// corpus. So a list built from a kv.Links page's `.targetVertex` (or
// `.sourceVertex`) holds dead vertices, even after the link's own `isDeleted`
// is filtered — the LINK is live, its endpoint is not. That is harmless where
// the consumer re-proves each element (worksAt_covers walks every candidate
// building through its own liveness test), and wrong wherever the list's
// LENGTH is the decision: an exactly-one / ambiguity / uniqueness count reads a
// decommissioned building as a live candidate, and the op no-ops — or picks the
// dead one — forever, while the read model, which joins live vertices, shows the
// right count.
//
// The class shipped twice. clinic-reminders' BackfillVisitSeriesSite was
// screened at build (its sites_for_provider re-proves each building); its
// sibling clinic-domain BackfillAppointmentSite was not, and on the shared
// stack every one of the fifteen site-less appointments belonged to a provider
// holding one live site and one live link to a building tombstoned three weeks
// earlier — `len(sites) != 1`, no-op, on every replay. Twice-seen gets a gate.
//
// THE RULE. In every shipped package script (the compiled pkgregistry corpus —
// each DDL's Script, parsed with go.starlark.net/syntax), a `len(X)` that is an
// operand of a comparison (`==` `!=` `<` `>` `<=` `>=`) where X is a LINK-TARGET
// LIST must be over a list whose elements were SCREENED for vertex liveness.
//
// A link-target list is, per function: a variable appended to with a
// `.targetVertex` / `.sourceVertex` attribute (or a one-hop alias of one bound
// in the same loop body); a variable assigned a list literal or comprehension
// whose elements are such an attribute; the result of a call to a function
// that returns one of those (transitively — a producer returning another
// producer's result is a producer); a comprehension over, or a `list` /
// `sorted` / `set` / `reversed` of, any of the above; or a plain alias of any
// of the above. A `len(...)` directly over a producer call or a comprehension
// is the same decision without the variable, and `n = len(<list>)` followed by
// a comparison on `n` is judged at the `len(`.
//
// A list is screened when THE ELEMENT BEING COUNTED passed a liveness test on
// its way in. The test is one of a closed set — a call to `vertex_live(key)` or
// `vertex_alive(state, key)` (the two predicate shapes the corpus defines; the
// subject is the last argument), or `<doc>.isDeleted` where `doc` was bound to
// `kv.Read(<subject>)` in the same loop body — and it must be ON THE ELEMENT:
// the subject expression equals the appended element or the comprehension's
// body, one alias hop allowed. It must also GUARD the element: the append sits
// in the live branch of an `if` stating the test (`if vertex_live(t):` /
// `if not doc.isDeleted:`), or in the else branch of one stating the dead side,
// or below an `if` in the loop body whose dead side leaves the iteration
// (`if not vertex_live(lk.targetVertex): continue`, `if doc == None or
// doc.isDeleted: continue`); for a comprehension, an `if` clause stating the
// live side of the body. A producer counts as screened when every list it
// returns is. What does NOT screen, and the recogniser rejects by construction:
// `lk.isDeleted` (the link's tombstone, not the endpoint's); a test on a
// different subject (`vertex_live(provider)` beside `sites.append(lk.targetVertex)`);
// a test in a sibling branch; the dead side (`if not vertex_live(x): xs.append(x)`);
// a name that merely contains `live` (`live_link_target`, `collect_live_sweep`).
//
// AUTHOR-DECLARES ESCAPE. A count whose screening the gate cannot see — the
// elements were proven live by a shape outside the recogniser, or a dead
// endpoint genuinely belongs in the tally — carries `# link-count: live-screened
// <why>` on the `len(` line or within the three lines above it. The `<why>` is
// mandatory: a bare tag is a finding, the same way a bare `# read-posture: (e)`
// is. The declaration binds to one `len(`, never to a function.
//
// OUT OF SCOPE, stated so a green run claims exactly what it checked. Not seen
// by the scan, and therefore neither passed nor failed: an emptiness test
// (`if sites:` / `if not sites:`) over a target list; `len(x) in (0, 2)` and any
// count reached other than through a comparison operator; a list built by
// `+=`, `extend`, list concatenation, a conditional expression
// (`[...] if page else []`), a dict comprehension, a tuple element
// (`[(lk.targetVertex, lk.key) ...]`), a tuple assignment, a parameter, or a
// nested def. These are blind spots, not exemptions — an author who counts
// through one of them still carries the hazard and should carry the
// declaration. Two further shapes are out of scope and NOT harmless: a count
// over a list of LINK records whose endpoint is then taken (`live = [lk for lk
// in page if not lk.isDeleted]; if len(live) != 1: ...; live[0].targetVertex`
// is the minting defect rewritten over records — the corpus's record counts
// today are sweep caps, never a pick), and a liveness test the scan cannot
// bind to the element (a predicate outside the closed set, a test two alias
// hops away). Both belong under the declaration.
//
// Self-tests on every run (`--selftest` prints each vector) and refuses an
// all-clear over zero examined scripts.
package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"go.starlark.net/syntax"

	"github.com/operatinggraph/lattice/internal/pkgregistry"
)

const (
	// declTag is the author-declares annotation; declWindow is how many lines
	// above the `len(` line it may sit, mirroring `# read-posture:`'s window.
	declTag    = "# link-count: live-screened"
	declWindow = 3
)

var (
	// endpointAttrs are the link-record attributes that name an endpoint
	// vertex; a list of them is a link-target list.
	endpointAttrs = map[string]bool{"targetVertex": true, "sourceVertex": true}
	// livenessPredicate is the closed set of vertex-liveness predicates: the
	// corpus defines `vertex_live(key)` (a live kv.Read + isDeleted) and
	// `vertex_alive(state, key)` (the declared-reads form), nothing else. A
	// `live` word elsewhere in a name is not a predicate — `live_link_target`
	// resolves a link's endpoint without proving it, `collect_live_sweep` and
	// `live_data` are sweeps — so the set is a prefix match, not a word match.
	livenessPredicate = regexp.MustCompile(`^vertex_(live|alive)`)
	// listWrappers re-shape a list without changing its elements, so the
	// screening state passes through them.
	listWrappers = map[string]bool{"list": true, "sorted": true, "set": true, "reversed": true}
	// declLine matches the annotation and captures its reason.
	declLine = regexp.MustCompile(regexp.QuoteMeta(declTag) + `\s*(.*)$`)
)

// listSites makes checkScript print each examined count (`--list`).
var listSites bool

// stats accumulates what a run examined so a clean verdict is auditable.
type stats struct {
	packages  int
	scripts   int
	producers int
	counts    int
}

func main() {
	strict := os.Getenv("STRICT") == "1"
	verbose := false
	for _, a := range os.Args[1:] {
		switch a {
		case "--selftest":
			verbose = true
		case "--list":
			// Print every counted comparison the corpus walk examines, with
			// its verdict, so a clean run can be audited site by site.
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
		for _, d := range def.DDLs {
			if strings.TrimSpace(d.Script) == "" {
				continue
			}
			where := fmt.Sprintf("%s: DDL %s", name, d.CanonicalName)
			findings = append(findings, checkScript(where, d.Script, &st)...)
		}
	}
	if st.scripts == 0 {
		findings = append(findings, "lint-link-target-count: examined ZERO scripts — the corpus walk is broken, and a gate that checked nothing has no all-clear to give")
	}

	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) == 0 {
		fmt.Printf("lint-link-target-count: clean — %d script(s) across %d package(s); %d link-target producer(s) resolved, %d counted-comparison(s) over link-target lists checked\n",
			st.scripts, st.packages, st.producers, st.counts)
		return
	}
	fmt.Printf("lint-link-target-count: %d issue(s) — %d script(s) across %d package(s), %d producer(s), %d count(s) checked\n",
		len(findings), st.scripts, st.packages, st.producers, st.counts)
	if strict {
		os.Exit(1)
	}
}

// listInfo is what the scan knows about one list-valued expression: whether it
// is a link-target list, and whether its elements were screened.
type listInfo struct {
	targets  bool
	screened bool
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
	lines := strings.Split(src, "\n")

	defs := map[string]*syntax.DefStmt{}
	for _, s := range f.Stmts {
		if d, ok := s.(*syntax.DefStmt); ok {
			defs[d.Name.Name] = d
		}
	}
	producers := resolveProducers(defs)
	st.producers += len(producers)

	var findings []string
	// claimed records the script line of every declaration a count has already
	// bound to, so one declaration vouches for exactly one `len(`.
	claimed := map[int]bool{}
	for _, name := range sortedKeys(defs) {
		d := defs[name]
		vars := targetVars(d.Body, producers)
		// countVars maps a name bound as `n = len(<target list>)` to the line of
		// that `len(` and its screening, so a later `n != 1` is judged at the
		// count. judged dedupes: one line, one verdict, however many operands.
		countVars := map[string]struct {
			line     int
			screened bool
		}{}
		walkStmts(d.Body, func(s syntax.Stmt, _ []*syntax.ForStmt, _ []guardFrame) {
			as, ok := s.(*syntax.AssignStmt)
			if !ok || as.Op != syntax.EQ {
				return
			}
			id, ok := as.LHS.(*syntax.Ident)
			if !ok {
				return
			}
			call, ok := lenCall(as.RHS)
			if !ok {
				return
			}
			pos, _ := call.Span()
			info := classifyList(call.Args[0], vars, producers, int(pos.Line))
			if info.targets {
				countVars[id.Name] = struct {
					line     int
					screened bool
				}{int(pos.Line), info.screened}
			}
		})
		judged := map[int]bool{}
		syntax.Walk(d, func(n syntax.Node) bool {
			if n == nil {
				return false
			}
			bin, ok := n.(*syntax.BinaryExpr)
			if !ok || !isComparison(bin.Op) {
				return true
			}
			for _, side := range []syntax.Expr{bin.X, bin.Y} {
				var line int
				var screened bool
				if call, ok := lenCall(side); ok {
					pos, _ := call.Span()
					info := classifyList(call.Args[0], vars, producers, int(pos.Line))
					if !info.targets {
						continue
					}
					line, screened = int(pos.Line), info.screened
				} else if id, ok := unparen(side).(*syntax.Ident); ok {
					cv, ok := countVars[id.Name]
					if !ok {
						continue
					}
					line, screened = cv.line, cv.screened
				} else {
					continue
				}
				if judged[line] {
					continue
				}
				judged[line] = true
				st.counts++
				if listSites {
					fmt.Printf("  count: %s: %s() script line %d: %s — screened=%v\n", where, name, line, strings.TrimSpace(lineAt(lines, line)), screened)
				}
				if screened {
					continue
				}
				findings = append(findings, judge(where, name, line, lines, claimed)...)
			}
			return true
		})
	}
	return findings
}

// judge decides one unscreened count at script line `line` (1-based): a
// declaration with a reason clears it, a bare declaration is its own finding,
// and no declaration is the finding the gate exists for. A declaration binds to
// ONE count — the first UNSCREENED `len(` at or below it claims it, and a second
// such count inside the same window is judged as undeclared (a declaration
// vouching for two subjects is a blanket, the lint-gates dossier's first entry).
func judge(where, fn string, line int, lines []string, claimed map[int]bool) []string {
	site := fmt.Sprintf("%s: %s() script line %d: %s", where, fn, line, strings.TrimSpace(lineAt(lines, line)))
	for l := line; l >= 1 && l > line-1-declWindow; l-- {
		m := declLine.FindStringSubmatch(lineAt(lines, l))
		if m == nil || claimed[l] {
			continue
		}
		claimed[l] = true
		if strings.TrimSpace(m[1]) == "" {
			return []string{fmt.Sprintf("%s — `%s` carries no reason; the declaration must say how the elements were proven live (or why a dead endpoint belongs in this count)", site, declTag)}
		}
		return nil
	}
	return []string{fmt.Sprintf("%s — this compares the LENGTH of a list of link endpoints that were never screened for vertex liveness. A tombstone cascades onto no link, so a live link to a dead vertex is counted as a live candidate and an exactly-one / ambiguity decision over it is wrong forever. Filter the list through the script's liveness predicate before counting (`[s for s in xs if vertex_live(s)]`), screen it in the producer, or declare `%s <why>` above the `len(`.", site, declTag)}
}

func lineAt(lines []string, n int) string {
	if n < 1 || n > len(lines) {
		return ""
	}
	return lines[n-1]
}

// lenCall reports whether e is a one-argument `len(...)` call.
func lenCall(e syntax.Expr) (*syntax.CallExpr, bool) {
	e = unparen(e)
	c, ok := e.(*syntax.CallExpr)
	if !ok || len(c.Args) != 1 {
		return nil, false
	}
	id, ok := c.Fn.(*syntax.Ident)
	if !ok || id.Name != "len" {
		return nil, false
	}
	return c, true
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

func isComparison(t syntax.Token) bool {
	switch t {
	case syntax.EQL, syntax.NEQ, syntax.LT, syntax.GT, syntax.LE, syntax.GE:
		return true
	}
	return false
}

// resolveProducers finds every top-level def that returns a link-target list,
// iterating to a fixpoint so a def returning another producer's call is one
// too. The map value is whether every returned list is screened.
func resolveProducers(defs map[string]*syntax.DefStmt) map[string]listInfo {
	producers := map[string]listInfo{}
	// Each pass can only re-read producers the previous pass settled, so
	// len(defs)+1 passes reach the fixpoint; the bound is a guard, not a limit.
	for pass, changed := 0, true; changed && pass <= len(defs); pass++ {
		changed = false
		for name, d := range defs {
			vars := targetVars(d.Body, producers)
			info, ok := producerInfo(d, vars, producers)
			if !ok {
				continue
			}
			if prev, seen := producers[name]; !seen || prev != info {
				producers[name] = info
				changed = true
			}
		}
	}
	return producers
}

// producerInfo classifies a def by its return statements: a producer returns a
// link-target list on at least one path, and is screened only if every such
// path is.
func producerInfo(d *syntax.DefStmt, vars bindings, producers map[string]listInfo) (listInfo, bool) {
	var found bool
	screened := true
	syntax.Walk(d, func(n syntax.Node) bool {
		if n == nil {
			return false
		}
		if inner, ok := n.(*syntax.DefStmt); ok && inner != d {
			return false
		}
		r, ok := n.(*syntax.ReturnStmt)
		if !ok || r.Result == nil {
			return true
		}
		info := classifyList(r.Result, vars, producers, int(r.Return.Line))
		if info.targets {
			found = true
			screened = screened && info.screened
		}
		return true
	})
	return listInfo{targets: true, screened: screened}, found
}

// binding is one assignment (or append) to a variable, at the script line it
// happens on, so a later read resolves to the binding in force at its position.
type binding struct {
	line int
	info listInfo
}

// bindings is every variable's link-target bindings in source order.
type bindings map[string][]binding

// at returns the binding in force for name at a read on line `line`: the last
// binding on an earlier line. A read on the binding's own line (the RHS of
// `xs = [x for x in xs if live(x)]`) resolves to the prior binding, which is
// what a self-referential re-screen means.
func (b bindings) at(name string, line int) (listInfo, bool) {
	var out listInfo
	found := false
	for _, bd := range b[name] {
		if bd.line >= line {
			break
		}
		out, found = bd.info, true
	}
	return out, found
}

// set records a binding for name at line, merging with a same-line binding
// (two appends in one statement line) by requiring both to be screened.
func (b bindings) set(name string, line int, info listInfo) {
	bs := b[name]
	if n := len(bs); n > 0 && bs[n-1].line == line {
		bs[n-1].info.screened = bs[n-1].info.screened && info.screened
		return
	}
	b[name] = append(bs, binding{line: line, info: info})
}

// guardFrame is one `if` on the path from a function body to a statement: the
// statement sits in its live (True) branch or its else branch.
type guardFrame struct {
	stmt   *syntax.IfStmt
	inTrue bool
}

// targetVars scans one function body in source order for variables holding
// link-target lists: each assignment or append is a binding at its own line,
// classified against the bindings in force above it. The scan is flat — it does
// not model control flow — so a name re-bound on one branch reads as re-bound
// for everything below; the shipped scripts bind their candidate lists once
// and re-screen them in place, which this reads exactly.
func targetVars(body []syntax.Stmt, producers map[string]listInfo) bindings {
	vars := bindings{}
	walkStmts(body, func(s syntax.Stmt, loops []*syntax.ForStmt, guards []guardFrame) {
		switch st := s.(type) {
		case *syntax.AssignStmt:
			if st.Op != syntax.EQ {
				return
			}
			id, ok := st.LHS.(*syntax.Ident)
			if !ok {
				return
			}
			line := int(id.NamePos.Line)
			info := classifyList(st.RHS, vars, producers, line)
			if !info.targets {
				return
			}
			vars.set(id.Name, line, info)
		case *syntax.ExprStmt:
			// `X.append(<endpoint>)` — X becomes a link-target list, screened
			// only if THIS element was proven live on the way to the append.
			// An append onto a list already bound unscreened stays unscreened.
			call, ok := st.X.(*syntax.CallExpr)
			if !ok || len(call.Args) != 1 {
				return
			}
			dot, ok := call.Fn.(*syntax.DotExpr)
			if !ok || dot.Name.Name != "append" {
				return
			}
			recv, ok := dot.X.(*syntax.Ident)
			if !ok {
				return
			}
			pos, _ := call.Span()
			line := int(pos.Line)
			var loop *syntax.ForStmt
			if len(loops) > 0 {
				loop = loops[len(loops)-1]
			}
			env := loopEnv(loop, pos)
			element := env.normalize(exprString(call.Args[0]))
			if !isEndpointAttr(call.Args[0]) && !isEndpointAttr(env.aliasExpr(call.Args[0])) {
				return
			}
			info := listInfo{targets: true, screened: appendScreened(element, pos, loop, guards, env)}
			if prev, seen := vars.at(recv.Name, line); seen {
				info.screened = info.screened && prev.screened
			}
			vars.set(recv.Name, line, info)
		}
	})
	return vars
}

// walkStmts visits every statement in a function body (not descending into
// nested defs), handing each the enclosing for-loops and the chain of `if`s
// whose branches contain it, innermost last.
func walkStmts(stmts []syntax.Stmt, visit func(syntax.Stmt, []*syntax.ForStmt, []guardFrame)) {
	var walk func([]syntax.Stmt, []*syntax.ForStmt, []guardFrame)
	walk = func(ss []syntax.Stmt, loops []*syntax.ForStmt, guards []guardFrame) {
		for _, s := range ss {
			visit(s, loops, guards)
			switch st := s.(type) {
			case *syntax.ForStmt:
				walk(st.Body, append(append([]*syntax.ForStmt{}, loops...), st), nil)
			case *syntax.IfStmt:
				walk(st.True, loops, append(append([]guardFrame{}, guards...), guardFrame{stmt: st, inTrue: true}))
				walk(st.False, loops, append(append([]guardFrame{}, guards...), guardFrame{stmt: st, inTrue: false}))
			case *syntax.WhileStmt:
				walk(st.Body, loops, guards)
			}
		}
	}
	walk(stmts, nil, nil)
}

// scopeEnv is what a loop body has bound before a given position: simple
// aliases (`t = lk.targetVertex`) and kv.Read bindings (`doc = kv.Read(t)`),
// so a liveness test phrased on the alias or on the read document resolves to
// the element it is about.
type scopeEnv struct {
	aliases map[string]string
	reads   map[string]string
}

// loopEnv collects the aliases and kv.Read bindings made in loop's body before
// pos. A nil loop yields an empty environment.
func loopEnv(loop *syntax.ForStmt, pos syntax.Position) scopeEnv {
	env := scopeEnv{aliases: map[string]string{}, reads: map[string]string{}}
	if loop == nil {
		return env
	}
	var collect func([]syntax.Stmt)
	collect = func(ss []syntax.Stmt) {
		for _, s := range ss {
			start, _ := s.Span()
			if !before(start, pos) {
				return
			}
			switch st := s.(type) {
			case *syntax.AssignStmt:
				id, ok := st.LHS.(*syntax.Ident)
				if !ok || st.Op != syntax.EQ {
					continue
				}
				if c, ok := unparen(st.RHS).(*syntax.CallExpr); ok && isKVRead(c) && len(c.Args) >= 1 {
					env.reads[id.Name] = env.normalize(exprString(c.Args[0]))
					continue
				}
				env.aliases[id.Name] = env.normalize(exprString(st.RHS))
			case *syntax.IfStmt:
				collect(st.True)
				collect(st.False)
			}
		}
	}
	collect(loop.Body)
	return env
}

// normalize resolves a one-hop alias so `t` and `lk.targetVertex` compare
// equal once `t = lk.targetVertex` has been seen.
func (e scopeEnv) normalize(s string) string {
	if a, ok := e.aliases[s]; ok {
		return a
	}
	return s
}

// aliasExpr returns the expression an identifier aliases, parsed back from its
// rendering, so an appended alias of an endpoint still reads as an endpoint.
func (e scopeEnv) aliasExpr(x syntax.Expr) syntax.Expr {
	id, ok := unparen(x).(*syntax.Ident)
	if !ok {
		return x
	}
	src, ok := e.aliases[id.Name]
	if !ok {
		return x
	}
	parsed, err := syntax.ParseExpr("alias", src, 0)
	if err != nil {
		return x
	}
	return parsed
}

func before(a, b syntax.Position) bool {
	return a.Line < b.Line || (a.Line == b.Line && a.Col < b.Col)
}

// appendScreened decides whether the element appended at pos was proven live
// on the way there: an enclosing `if` whose taken branch is the live side of a
// test on that element, or an earlier `if` in the loop body whose dead-side
// test on the element leaves the loop iteration (`continue` / `return` /
// `fail`).
func appendScreened(element string, pos syntax.Position, loop *syntax.ForStmt, guards []guardFrame, env scopeEnv) bool {
	for _, g := range guards {
		for _, t := range livenessTests(g.stmt.Cond, env) {
			if t.subject == element && t.live == g.inTrue {
				return true
			}
		}
	}
	if loop == nil {
		return false
	}
	return skippedDeadBefore(loop.Body, element, pos, env)
}

// skippedDeadBefore reports whether a statement before pos in ss is an `if`
// whose condition holds a dead-side test on element and whose live branch
// leaves the iteration.
func skippedDeadBefore(ss []syntax.Stmt, element string, pos syntax.Position, env scopeEnv) bool {
	for _, s := range ss {
		start, _ := s.Span()
		if !before(start, pos) {
			return false
		}
		st, ok := s.(*syntax.IfStmt)
		if !ok {
			continue
		}
		if leavesIteration(st.True) {
			for _, t := range livenessTests(st.Cond, env) {
				if t.subject == element && !t.live {
					return true
				}
			}
		}
		// Only an `if` that spans pos is on the path to the append — a skip
		// inside a sibling `if` (or a sibling branch) guards nothing here.
		for _, branch := range [][]syntax.Stmt{st.True, st.False} {
			if containsPos(branch, pos) && skippedDeadBefore(branch, element, pos, env) {
				return true
			}
		}
	}
	return false
}

// containsPos reports whether pos lies within one of ss's statements.
func containsPos(ss []syntax.Stmt, pos syntax.Position) bool {
	for _, s := range ss {
		start, end := s.Span()
		if !before(pos, start) && before(pos, end) {
			return true
		}
	}
	return false
}

// leavesIteration reports whether a branch ends the current iteration or the
// script: a `continue`, a `return`, or a `fail(...)` call.
func leavesIteration(ss []syntax.Stmt) bool {
	for _, s := range ss {
		switch st := s.(type) {
		case *syntax.BranchStmt:
			if st.Token == syntax.CONTINUE {
				return true
			}
		case *syntax.ReturnStmt:
			return true
		case *syntax.ExprStmt:
			if c, ok := st.X.(*syntax.CallExpr); ok {
				if id, ok := c.Fn.(*syntax.Ident); ok && id.Name == "fail" {
					return true
				}
			}
		}
	}
	return false
}

// livenessTest is one liveness verdict a condition states about a subject:
// `live` is true when the condition holding means the subject is alive
// (`vertex_live(x)`, `not doc.isDeleted`), false when it means the subject is
// dead (`not vertex_live(x)`, `doc.isDeleted`).
type livenessTest struct {
	subject string
	live    bool
}

// livenessTests extracts the liveness tests a condition states: predicate
// calls from the closed set (a name starting `vertex_live` / `vertex_alive`,
// the subject being the LAST argument — `vertex_alive(state, key)` and
// `vertex_live(key)` both end with it), and `<doc>.isDeleted` where doc is
// bound to `kv.Read(<subject>)` in env. `not` flips polarity; `and` passes
// both sides through; a live-side test under `or` is dropped, because "A or
// B" holding proves neither. `lk.isDeleted` on a link record is not a test:
// the link is not the subject.
func livenessTests(cond syntax.Expr, env scopeEnv) []livenessTest {
	var out []livenessTest
	var walk func(e syntax.Expr, live bool, underOr bool)
	walk = func(e syntax.Expr, live bool, underOr bool) {
		switch x := unparen(e).(type) {
		case *syntax.UnaryExpr:
			if x.Op == syntax.NOT {
				walk(x.X, !live, underOr)
			}
		case *syntax.BinaryExpr:
			switch x.Op {
			case syntax.AND:
				walk(x.X, live, underOr)
				walk(x.Y, live, underOr)
			case syntax.OR:
				walk(x.X, live, true)
				walk(x.Y, live, true)
			}
		case *syntax.CallExpr:
			if !isLivenessPredicate(x) || len(x.Args) == 0 {
				return
			}
			if underOr && live {
				return
			}
			out = append(out, livenessTest{subject: env.normalize(exprString(x.Args[len(x.Args)-1])), live: live})
		case *syntax.DotExpr:
			if x.Name.Name != "isDeleted" {
				return
			}
			id, ok := x.X.(*syntax.Ident)
			if !ok {
				return
			}
			subject, ok := env.reads[id.Name]
			if !ok {
				return
			}
			// `doc.isDeleted` true means dead: the polarity is inverted
			// relative to a predicate call.
			if underOr && !live {
				return
			}
			out = append(out, livenessTest{subject: subject, live: !live})
		}
	}
	walk(cond, true, false)
	return out
}

// classifyList decides whether e, read at script line `line`, evaluates to a
// link-target list, and whether its elements were screened.
func classifyList(e syntax.Expr, vars bindings, producers map[string]listInfo, line int) listInfo {
	e = unparen(e)
	switch x := e.(type) {
	case *syntax.Ident:
		if info, ok := vars.at(x.Name, line); ok {
			return info
		}
	case *syntax.ListExpr:
		for _, el := range x.List {
			if isEndpointAttr(el) {
				return listInfo{targets: true}
			}
		}
	case *syntax.CallExpr:
		if id, ok := x.Fn.(*syntax.Ident); ok {
			if info, ok := producers[id.Name]; ok {
				return info
			}
			// A re-shaping wrapper keeps the elements and their screening.
			if listWrappers[id.Name] && len(x.Args) >= 1 {
				return classifyList(x.Args[0], vars, producers, line)
			}
		}
	case *syntax.Comprehension:
		if x.Curly {
			return listInfo{}
		}
		var source listInfo
		var loopVar string
		var tests []livenessTest
		env := scopeEnv{aliases: map[string]string{}, reads: map[string]string{}}
		for _, c := range x.Clauses {
			switch cl := c.(type) {
			case *syntax.ForClause:
				if loopVar == "" {
					if id, ok := cl.Vars.(*syntax.Ident); ok {
						loopVar = id.Name
					}
					source = classifyList(cl.X, vars, producers, line)
				}
			case *syntax.IfClause:
				tests = append(tests, livenessTests(cl.Cond, env)...)
			}
		}
		element := exprString(x.Body)
		screenedHere := false
		for _, t := range tests {
			if t.live && t.subject == element {
				screenedHere = true
			}
		}
		bodyIsEndpoint := isEndpointAttr(x.Body)
		bodyIsElement := false
		if id, ok := unparen(x.Body).(*syntax.Ident); ok && id.Name == loopVar {
			bodyIsElement = true
		}
		switch {
		case bodyIsEndpoint:
			// `[lk.targetVertex for lk in page if ...]` — a fresh target list;
			// screened only by a live-side test on that same element here.
			return listInfo{targets: true, screened: screenedHere}
		case bodyIsElement && source.targets:
			// `[s for s in sites if ...]` — the source's elements, re-screened
			// here or inheriting the source's screening.
			return listInfo{targets: true, screened: source.screened || screenedHere}
		}
	}
	return listInfo{}
}

// isEndpointAttr reports whether e is `<x>.targetVertex` / `<x>.sourceVertex`.
func isEndpointAttr(e syntax.Expr) bool {
	dot, ok := unparen(e).(*syntax.DotExpr)
	return ok && endpointAttrs[dot.Name.Name]
}

// isLivenessPredicate reports whether c calls a predicate from the closed set.
func isLivenessPredicate(c *syntax.CallExpr) bool {
	id, ok := c.Fn.(*syntax.Ident)
	return ok && livenessPredicate.MatchString(id.Name)
}

// isKVRead reports whether c is `kv.Read(...)`.
func isKVRead(c *syntax.CallExpr) bool {
	dot, ok := c.Fn.(*syntax.DotExpr)
	if !ok || dot.Name.Name != "Read" {
		return false
	}
	id, ok := dot.X.(*syntax.Ident)
	return ok && id.Name == "kv"
}

// exprString renders an expression canonically enough that two spellings of
// one subject compare equal: identifiers, attributes, indexes, calls, literals
// and binary operators. Anything else renders as an opaque token that matches
// nothing.
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
	case *syntax.BinaryExpr:
		return exprString(x.X) + x.Op.String() + exprString(x.Y)
	}
	return fmt.Sprintf("<%T>", e)
}

func sortedKeys(m map[string]*syntax.DefStmt) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// runSelfTest proves the gate on synthetic scripts through checkScript — the
// same entry the corpus walk uses, so a vector proven here is proven for the
// real run. Each vector is a mutation of the shipped BackfillAppointmentSite
// shape or one of the recogniser's declared arms.
func runSelfTest(verbose bool) {
	pass := true
	check := func(cond bool, desc string) {
		switch {
		case !cond:
			fmt.Fprintln(os.Stderr, "lint-link-target-count selftest: FAIL —", desc)
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

	const producer = `
def sites_for_provider(provider):
    spage, _ = kv.Links(provider, "practicesAt", "out")
    sites = []
    for lk in spage:
        if not lk.isDeleted:
            sites.append(lk.targetVertex)
    return sites
`
	// Vector 1 — the shipped defect: an unscreened producer's list counted.
	f, st := run(producer + `
def execute(state, op):
    sites = sites_for_provider(op.payload["p"])
    if len(sites) != 1:
        return {"mutations": []}
    return {"mutations": [sites[0]]}
`)
	check(len(f) == 1 && has(f, "execute() script line 12") && has(f, "never screened"), "unfixed BackfillAppointmentSite shape is one finding at the len( line")
	check(st.producers == 1 && st.counts == 1, "the vector's producer and count are both counted in stats")

	// Vector 2 — the shipped fix: a comprehension screens at the count site.
	f, _ = run(producer + `
def execute(state, op):
    sites = [s for s in sites_for_provider(op.payload["p"]) if vertex_live(s)]
    if len(sites) != 1:
        return {"mutations": []}
    return {"mutations": [sites[0]]}
`)
	check(len(f) == 0, "the fixed shape (comprehension with vertex_live) is clean")

	// Vector 3 — the sibling's shape: the producer screens in its loop.
	f, _ = run(`
def sites_for_provider(provider):
    spage, _ = kv.Links(provider, "practicesAt", "out")
    sites = []
    for lk in spage:
        if lk.isDeleted:
            continue
        if not vertex_live(lk.targetVertex):
            continue
        sites.append(lk.targetVertex)
    return sites

def execute(state, op):
    sites = sites_for_provider(op.payload["p"])
    if len(sites) != 1:
        return {}
    return {}
`)
	check(len(f) == 0, "a producer screening with `if not vertex_live(...): continue` before the append is clean")

	// Vector 4 — a link-tombstone filter alone is not vertex screening.
	f, _ = run(`
def execute(state, op):
    page, _ = kv.Links(op.payload["k"], "atSite", "out")
    sites = [lk.targetVertex for lk in page if not lk.isDeleted]
    if len(sites) > 1:
        return {}
    return {}
`)
	check(len(f) == 1 && has(f, "script line 5"), "a comprehension filtering only lk.isDeleted is a finding")

	// Vector 5 — the author-declares escape, with and without a reason.
	f, _ = run(`
def execute(state, op):
    page, _ = kv.Links(op.payload["k"], "atSite", "out")
    sites = [lk.targetVertex for lk in page if not lk.isDeleted]
    # link-count: live-screened every atSite target was proven alive by the caller's declared reads
    if len(sites) > 1:
        return {}
    return {}
`)
	check(len(f) == 0, "a declaration with a reason within the window clears the count")
	f, _ = run(`
def execute(state, op):
    page, _ = kv.Links(op.payload["k"], "atSite", "out")
    sites = [lk.targetVertex for lk in page if not lk.isDeleted]
    # link-count: live-screened
    if len(sites) > 1:
        return {}
    return {}
`)
	check(len(f) == 1 && has(f, "carries no reason"), "a bare declaration is its own finding")
	f, _ = run(`
def execute(state, op):
    page, _ = kv.Links(op.payload["k"], "atSite", "out")
    # link-count: live-screened too far above to bind
    sites = [lk.targetVertex for lk in page if not lk.isDeleted]
    x = 1
    y = 2
    z = 3
    if len(sites) > 1:
        return {}
    return {}
`)
	check(len(f) == 1 && has(f, "never screened"), "a declaration outside the three-line window does not bind")

	// Vector 5b — one declaration binds to one count: a second `len(` in the
	// same window is judged on its own.
	f, _ = run(`
def execute(state, op):
    page, _ = kv.Links(op.payload["k"], "atSite", "out")
    sites = [lk.targetVertex for lk in page if not lk.isDeleted]
    rooms = [lk.targetVertex for lk in page if not lk.isDeleted]
    # link-count: live-screened the caller's declared reads proved every site
    if len(sites) > 1:
        return {}
    if len(rooms) > 1:
        return {}
    return {}
`)
	check(len(f) == 1 && has(f, "script line 9"), "a declaration vouches for the first count only; the second in its window is a finding")

	f, st = run(`
def execute(state, op):
    page, _ = kv.Links(op.payload["k"], "identifiedBy", "in")
    hits = []
    for lk in page:
        if not lk.isDeleted:
            hits.append(lk)
    if len(hits) >= 500:
        fail("too many")
    if len(hits) == 0:
        fail("none")
    return {}
`)
	check(len(f) == 0 && st.counts == 0, "counting link records themselves is out of scope")

	// Vector 7 — a producer returning a list literal, counted directly.
	f, _ = run(`
def appt_site(appt_id):
    apage, _ = kv.Links("vtx.appointment." + appt_id, "atSite", "out")
    for lk in apage:
        if not lk.isDeleted:
            return [lk.targetVertex]
    return []

def execute(state, op):
    if len(appt_site(op.payload["a"])) == 1:
        return {}
    return {}
`)
	check(len(f) == 1 && has(f, "script line 10"), "a `return [lk.targetVertex]` producer counted directly is a finding")

	// Vector 8 — transitive producer, unscreened through two hops.
	f, st = run(producer + `
def appointment_sites(appt_id, provider):
    sites = sites_for_provider(provider)
    if sites:
        return sites
    return []

def execute(state, op):
    sites = appointment_sites(op.payload["a"], op.payload["p"])
    if len(sites) != 1:
        return {}
    return {}
`)
	check(len(f) == 1 && has(f, "execute()"), "a producer returning another producer's list carries its screening state")
	check(st.producers == 2, "both hops resolve as producers")

	// Vector 8b — transitive producer, screened at the inner hop.
	f, _ = run(`
def inner(provider):
    spage, _ = kv.Links(provider, "practicesAt", "out")
    return [lk.targetVertex for lk in spage if not lk.isDeleted and vertex_alive(state, lk.targetVertex)]

def outer(provider):
    return inner(provider)

def execute(state, op):
    if len(outer(op.payload["p"])) != 1:
        return {}
    return {}
`)
	check(len(f) == 0, "screening at the inner hop clears the outer count; vertex_alive(state, key) tests its last argument")

	// Vector 9 — emptiness over a target list is out of scope.
	f, st = run(producer + `
def execute(state, op):
    sites = sites_for_provider(op.payload["p"])
    if sites:
        return {}
    if not sites:
        return {}
    return {}
`)
	check(len(f) == 0 && st.counts == 0, "an emptiness test is out of scope, by construction")

	// Vector 10 — inline kv.Read of the endpoint + isDeleted in the loop screens.
	f, _ = run(`
def execute(state, op):
    page, _ = kv.Links(op.payload["k"], "practicesAt", "out")
    sites = []
    for lk in page:
        if lk.isDeleted:
            continue
        doc = kv.Read(lk.targetVertex)
        if doc == None or doc.isDeleted:
            continue
        sites.append(lk.targetVertex)
    if len(sites) != 1:
        return {}
    return {}
`)
	check(len(f) == 0, "an inline kv.Read of the endpoint followed by an isDeleted test screens the append")

	// Vector 11 — two appends, one unguarded: the list is unscreened.
	f, _ = run(`
def execute(state, op):
    page, _ = kv.Links(op.payload["k"], "practicesAt", "out")
    sites = []
    for lk in page:
        if vertex_live(lk.targetVertex):
            sites.append(lk.targetVertex)
    other, _ = kv.Links(op.payload["k"], "atSite", "out")
    for lk in other:
        sites.append(lk.targetVertex)
    if len(sites) != 1:
        return {}
    return {}
`)
	check(len(f) == 1, "a second unguarded append leaves the list unscreened")

	// Vector 12 — a self-referential re-screen reads the prior binding.
	f, _ = run(producer + `
def execute(state, op):
    sites = sites_for_provider(op.payload["p"])
    sites = [s for s in sites if vertex_live(s)]
    if len(sites) != 1:
        return {}
    return {}
`)
	check(len(f) == 0, "`sites = [s for s in sites if vertex_live(s)]` re-screens the same variable")

	// Vector 13 — a comparison the other way round, and a sourceVertex list.
	f, _ = run(`
def execute(state, op):
    page, _ = kv.Links(op.payload["k"], "holdsRole", "in")
    holders = [lk.sourceVertex for lk in page if not lk.isDeleted]
    if 1 < len(holders):
        return {}
    return {}
`)
	check(len(f) == 1, "`1 < len(xs)` over a sourceVertex list is a finding")

	// Vectors 15–22 — the recogniser binds the test to the ELEMENT, the branch
	// and the predicate set; each of these carries the hazard and must fail.
	evasions := []struct{ desc, src string }{
		{"a liveness test on a different subject (the provider) does not screen the appended target", `
def execute(state, op):
    page, _ = kv.Links(op.payload["p"], "practicesAt", "out")
    sites = []
    for lk in page:
        if not vertex_live(op.payload["p"]):
            continue
        sites.append(lk.targetVertex)
    if len(sites) != 1:
        return {}
    return {}
`},
		{"a test on the source endpoint does not screen the target endpoint", `
def execute(state, op):
    page, _ = kv.Links(op.payload["p"], "practicesAt", "out")
    sites = []
    for lk in page:
        owner_ok = vertex_live(lk.sourceVertex)
        if not owner_ok:
            continue
        sites.append(lk.targetVertex)
    if len(sites) != 1:
        return {}
    return {}
`},
		{"a comprehension test on the whole list, not the element, does not screen", `
def execute(state, op):
    page, _ = kv.Links(op.payload["p"], "practicesAt", "out")
    raw = [lk.targetVertex for lk in page if not lk.isDeleted]
    sites = [s for s in raw if vertex_live(raw)]
    if len(sites) != 1:
        return {}
    return {}
`},
		{"a test in a sibling branch does not screen the other branch's append", `
def execute(state, op):
    page, _ = kv.Links(op.payload["p"], "practicesAt", "out")
    primary = []
    sites = []
    for lk in page:
        t = lk.targetVertex
        if op.payload["kind"] == "x":
            if not vertex_live(t):
                continue
            primary.append(t)
        else:
            sites.append(lk.targetVertex)
    if len(sites) != 1:
        return {}
    return {}
`},
		{"the dead side of a guard does not screen (a forgotten continue)", `
def execute(state, op):
    page, _ = kv.Links(op.payload["p"], "practicesAt", "out")
    sites = []
    for lk in page:
        if not vertex_live(lk.targetVertex):
            sites.append(lk.targetVertex)
    if len(sites) != 1:
        return {}
    return {}
`},
		{"a name that merely contains `live` is not a predicate", `
def execute(state, op):
    page, _ = kv.Links(op.payload["p"], "locatedAt", "out")
    sites = []
    for lk in page:
        unit = live_link_target(lk.targetVertex, "locatedAt")
        sites.append(lk.targetVertex)
    if len(sites) != 1:
        return {}
    return {}
`},
		{"a kv.Read of some other key plus the link's own isDeleted is not screening", `
def execute(state, op):
    page, _ = kv.Links(op.payload["p"], "practicesAt", "out")
    sites = []
    for lk in page:
        prov = kv.Read(op.payload["p"])
        if lk.isDeleted:
            continue
        sites.append(lk.targetVertex)
    if len(sites) != 1:
        return {}
    return {}
`},
		{"`n = len(xs)` then `n != 1` is judged at the len(", `
def execute(state, op):
    page, _ = kv.Links(op.payload["p"], "practicesAt", "out")
    sites = [lk.targetVertex for lk in page if not lk.isDeleted]
    n = len(sites)
    if n != 1:
        return {}
    return {}
`},
		{"a `sorted(...)` of an unscreened list is still unscreened", `
def execute(state, op):
    page, _ = kv.Links(op.payload["p"], "practicesAt", "out")
    sites = sorted([lk.targetVertex for lk in page if not lk.isDeleted])
    if len(sites) != 1:
        return {}
    return {}
`},
	}
	for _, v := range evasions {
		f, _ = run(v.src)
		check(len(f) == 1 && has(f, "never screened"), v.desc)
	}

	// Vectors 23–27 — screening shapes the recogniser must accept.
	accepted := []struct{ desc, src string }{
		{"a guard's else branch after the dead-side test screens", `
def execute(state, op):
    page, _ = kv.Links(op.payload["p"], "practicesAt", "out")
    sites = []
    for lk in page:
        if not vertex_live(lk.targetVertex):
            pass
        else:
            sites.append(lk.targetVertex)
    if len(sites) != 1:
        return {}
    return {}
`},
		{"a live-side guard with the append inside it screens", `
def execute(state, op):
    page, _ = kv.Links(op.payload["p"], "practicesAt", "out")
    sites = []
    for lk in page:
        if not lk.isDeleted and vertex_live(lk.targetVertex):
            sites.append(lk.targetVertex)
    if len(sites) != 1:
        return {}
    return {}
`},
		{"a test on a one-hop alias of the endpoint screens the aliased append", `
def execute(state, op):
    page, _ = kv.Links(op.payload["p"], "practicesAt", "out")
    sites = []
    for lk in page:
        t = lk.targetVertex
        if not vertex_live(t):
            continue
        sites.append(t)
    if len(sites) != 1:
        return {}
    return {}
`},
		{"kv.Read of the endpoint then `doc == None or doc.isDeleted: continue` screens", `
def execute(state, op):
    page, _ = kv.Links(op.payload["p"], "practicesAt", "out")
    sites = []
    for lk in page:
        doc = kv.Read(lk.targetVertex)
        if doc == None or doc.isDeleted:
            continue
        sites.append(lk.targetVertex)
    if len(sites) != 1:
        return {}
    return {}
`},
		{"a `sorted(...)` of a screened list stays screened, and `n = len(...)` on it is clean", `
def execute(state, op):
    page, _ = kv.Links(op.payload["p"], "practicesAt", "out")
    sites = sorted([lk.targetVertex for lk in page if vertex_live(lk.targetVertex)])
    n = len(sites)
    if n != 1:
        return {}
    return {}
`},
	}
	for _, v := range accepted {
		f, _ = run(v.src)
		check(len(f) == 0, v.desc+fmt.Sprintf(" (got %v)", f))
	}

	// Vector 28 — both operands counts on one line is one finding.
	f, _ = run(`
def execute(state, op):
    page, _ = kv.Links(op.payload["p"], "practicesAt", "out")
    sites = [lk.targetVertex for lk in page if not lk.isDeleted]
    other = [lk.sourceVertex for lk in page if not lk.isDeleted]
    if len(sites) == len(other):
        return {}
    return {}
`)
	check(len(f) == 1, "two counts on one line yield one finding")

	// Vector 14 — a parse failure is not a finding and not an examined script.
	f, st = run("def execute(state, op:\n    return {}\n")
	check(len(f) == 0 && st.scripts == 0, "an unparseable script is neither examined nor reported")

	if !pass {
		fmt.Fprintln(os.Stderr, "lint-link-target-count: self-test FAILED — the gate does not prove its own vectors; fix the gate before trusting any corpus verdict")
		os.Exit(2)
	}
	if verbose {
		fmt.Println("selftest: all vectors passed")
	}
}
