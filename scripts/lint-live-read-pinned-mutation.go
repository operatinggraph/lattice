//go:build ignore

// lint-live-read-pinned-mutation — a live kv.Read followed by a bare update/
// tombstone on the same key is a lost-update race.
//
// THE HAZARD. commit_path.go's applyHydratedRevisions
// (internal/processor/commit_path.go) conditions a bare (no expectedRevision)
// update/tombstone on the step-4 hydrated revision, but ONLY for a key in the
// hydrated set — the keys a dispatcher declared in contextHint.reads/
// optionalReads, or a class-(a)/(d)/(f) read the descriptor floor otherwise
// hydrates. A key a script obtains through a live kv.Read — a class-(e)
// bounded-enumeration follow-up, a class-(c) config read, or an unannotated
// class-(b) read — carries no step-4 revision, so a bare update/tombstone on
// it stays UNCONDITIONED: whatever the read observed, the write lands
// regardless of what changed in between. A live read reaches this gate
// three ways: a direct `kv.Read(K)` call anywhere in an expression; the same
// call wrapped in one enclosing call (`any_data(kv.Read(K))`); or a call to a
// READ HELPER — a top-level `def` whose own body performs a live kv.Read on a
// key built only from ITS OWN parameters (`vertex_live(key)`, `aspect_data
// (key)`), so `if not vertex_live(sess_key): continue` is a live read of
// sess_key at the call site, with the class the helper's own kv.Read carries.
//
// A second population carries the identical hazard without ever calling
// kv.Read at all: a `for lk in <a kv.Links(...) page>:` loop's entry carries
// its own `.revision` (starlark_kv.go:317), so `lk.key` is exactly as live as
// a read result, and a bare update/tombstone on it races the SAME way — the
// corpus already pins this shape once (identity-domain/ddls.go:2051,
// `"key": lk.key, "expectedRevision": lk.revision`), so it is a known,
// intentional idiom, not a novel one this gate invents.
//
// THE RULE. In every shipped package script (the compiled pkgregistry corpus
// — every distinct DDL Script body AND every def in it, top-level or nested,
// parsed once with go.starlark.net/syntax):
//
//  1. Every `kv.Read(K)` call, wherever it appears in an expression (an
//     assignment RHS — bare, tuple, or wrapped in one call — an `if`
//     condition, a call argument, a comprehension), and every call to a READ
//     HELPER, is a candidate read. Its `# read-posture: (a|c|d|e|f) …`
//     annotation is resolved by the EXACT binding rule scripts/
//     lint-conventions.go defines for the same annotation family
//     (annotationAnchor/annotationSpan, that file's ~1042-1138: an
//     annotation trailing a code line binds to that line; one on its own
//     comment line binds to the first code line beneath it across further
//     comment lines; a blank line breaks the block — the same rule,
//     Starlark-only here). Those two functions (plus isCommentLine) are
//     copied below with that provenance noted, because they live in a
//     `//go:build ignore` main package and cannot be
//     imported. Class (a)/(d)/(f) is HYDRATED — the read is out of scope.
//     Class (e)/(c), or no annotation at all (class-(b) debt), is LIVE. A
//     read's class comes ONLY from an annotation whose own anchor (not
//     merely its indentation span) is the read's own statement or line —
//     unlike the escape hatch below, a hydrated annotation above an `if`
//     does NOT hydrate a read nested inside it; that read needs its own
//     annotation.
//     1b. A `for <var> in <page>:` loop, where `<page>` is (directly, or via
//     one intervening plain-alias assignment, or via a call to a HELPER
//     whose own body returns one — one hop modelled) a `kv.Links(...)`
//     result, makes `<var>.key` a LIVE key for the duration of that loop's
//     body, on par with a read: a bare update/tombstone on it (or on a name
//     later assigned from it, e.g. `old_link_key = lk.key`) is a finding;
//     `"expectedRevision": <var>.revision` (inline, or threaded through an
//     `_occ` helper) is PINNED.
//  2. A live read's (or page-entry's) KEY is tracked forward through the
//     function's own control flow (a program-order, branch- and loop-aware
//     scan — see scanStmts): it is visible to a later mutation only on a
//     path that can actually reach it. A branch that unconditionally exits
//     (`continue`, `break`, `return`, `fail(...)`) does not hand its own
//     reads to a sibling statement after it; a mutation textually BEFORE the
//     read that would inform it is never matched to it; a read inside a
//     `for`/`while` body reaches code after the loop only if its key does
//     not depend on a name the loop body itself reassigns (a key built fresh
//     each iteration, like a hashed per-candidate index, never leaks past
//     the loop that built it) — UNLESS a name that already existed before
//     the loop is reassigned, inside the body, to that exact key (a
//     "carried" value, e.g. `target = k` after `doc = kv.Read(k)`): that
//     read (and that name's own binding) both escape the loop too, matched
//     the same way a branch disagreement is (see 4).
//  3. A mutation is an `update`/`tombstone` dict literal (inline, or reached
//     through a chain of plain `name = <value>` aliases resolved the same
//     way keys are — see 4), or a call to a MUTATION HELPER — any top-level
//     or nested `def` (no naming convention required; a def that does not
//     resolve to such a dict, through direct return, an aliased return, or
//     delegation to another such helper, is simply not relevant) whose body
//     resolves to one. A `create` is never relevant. Every reference to
//     `expectedRevision` — a literal field in the dict, or one assigned via
//     a later `m["expectedRevision"] = ...` on the same name — is resolved
//     to whether it can be a `None` at that call site: a bare parameter with
//     no default, or an explicit non-`None` argument, is PINNED; a
//     `param=None`-defaulted parameter the call OMITS, or passes an explicit
//     `None` for, is BARE (a finding, if it matches a live read) — the
//     Processor treats `expectedRevision: None` as no pin at all
//     (starlark_runner.go:368), so there is no safer reading to give an
//     omitted or `None` value the benefit of. A helper called only through
//     `.extend(...)` (returns a list, not a single mutation) is UNMODELLED,
//     VERBOSE=1.
//  4. Key equality is structural: a `+`-concatenation chain, or a
//     single-`%s` `"...%s..." % x` format expression, flattens into a list
//     of parts (an identifier/attribute/call/slice/conditional rendering, or
//     a string literal), adjacent literal parts merge, and a plain
//     `name = <expr>` assignment records `name` as an alias for `<expr>`'s
//     own resolved parts — reassigning `name` replaces the alias, so a read
//     taken before a reassignment does not match a mutation taken after it,
//     UNLESS the two branches of an `if` disagree on what `name` resolves to
//     (one reassigns it, the other does not, or reassigns it differently):
//     the disagreement itself errs toward a finding — a mutation using that
//     name is checked against BOTH branches' possible bindings, not
//     silenced by the ambiguity. A helper call's key is reconstructed by
//     substituting the call's own (already-resolved) arguments into the
//     helper's own key expression, through one helper delegating to
//     another.
//  5. A LIVE read (or page-entry) reachably followed by a BARE
//     update/tombstone on the same key is a finding. A read whose result
//     crosses into a DIFFERENT top-level/nested def before a mutation
//     observes it is unmodelled (out of scope; see BOUNDARY) — a nested def
//     (14 exist in the corpus, e.g. identity-hygiene/ddls.go:399-555,
//     identity-domain/ddls.go:1350-2452) is itself examined as its own
//     independent unit, exactly like a top-level one, but a read in one def
//     is never matched to a mutation in another; two defs sharing one name
//     (top-level or nested) is refused as a finding of its own — the
//     ambiguity a name-keyed lookup could otherwise resolve silently to
//     either one.
//
// ESCAPE HATCH. `# occ: live-unpinned <why>` on the mutation's own line, or
// on a comment block above an enclosing SIMPLE statement (an assignment, a
// bare call, an aliased dict's own `name = {...}` line), exempts that site;
// its indentation-block reach (annotationSpan) means a hatch above an `if`
// covers every mutation nested inside it, and one above an aliased dict's
// assignment covers every later use of that alias. Printed under VERBOSE=1.
// A hatch whose anchor is a `def`/`if`/`for` HEADER — waiving a whole block
// by declaring on its opening line rather than on a statement inside it — is
// refused as a finding in its own right ("hatch must sit on a simple
// statement") and grants no exemption. A hatch separated from its statement
// by a blank line binds to nothing and exempts nothing, like every other
// annotation family here. Not applied to the shipped corpus by this gate —
// every finding on `main` is fixed by pinning the read's revision (a mutation
// helper's own `_occ` sibling) or by re-annotating a genuinely hydrated key,
// never by declaring around it.
//
// BOUNDARY (unmodelled; a shape here is neither judged clean nor reported,
// and prints under VERBOSE=1 wherever a call site is visible at all). A read
// crossing into a different top-level/nested def than the one holding a
// mutation. A key assembled by anything other than a `+` chain or a
// single-`%s` `%` format (other string formatting, multi-placeholder `%` or
// `.format`) renders as one opaque leaf and matches only an
// identically-shaped opaque expression elsewhere (rendered structurally —
// CondExpr/SliceExpr/IndexExpr/CallExpr included — so two DIFFERENT such
// expressions never collide into a false match; a shape this renderer still
// does not understand at all — a comprehension, a lambda — renders unique to
// its own source position instead, for the same reason). A mutation-helper
// reached only through `.extend(...)`, or whose own return shape is a list
// rather than a single dict. A read helper exposes only the FIRST live
// kv.Read its own body resolves in terms of its parameters — a helper
// performing more than one is only partially modelled. A "carried" value
// escaping a loop (rule 2) is matched the same way a branch disagreement is
// (rule 4) — by trying every binding the loop body could have produced, not
// by resolving to one. A page-entry loop (rule 1b) is recognized only for a
// direct `kv.Links(...)` iterable, one intervening plain-alias assignment,
// or one helper hop; a page threaded through more indirection, or a
// dict-shaped page (`page["links"]`) built any other way, is not. A mutation
// dict built with a VARIABLE `"op"` value (`{"op": kind, ...}`) is not
// classified at all (dictStringField requires a literal) — never a finding,
// never judged clean.
//
// Self-tests on every run (verbose under VERBOSE=1) and refuses an all-clear
// over zero examined scripts. `--list` prints every examined live read and
// mutation with its verdict. STRICT=1 exits non-zero on any finding;
// otherwise the gate is advisory (prints findings, exits 0).
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

// listSites makes checkScript print every examined live read and mutation
// site with its verdict (`--list`).
var listSites bool

// verboseUnmodelled prints escape-hatch exemptions and every other
// unmodelled shape this gate declines to judge — a read it cannot bind
// cleanly, a helper shape it cannot resolve — so a run can be audited for
// blind spots.
var verboseUnmodelled bool

// stats accumulates what a run examined so a clean verdict is auditable.
type stats struct {
	packages  int
	scripts   int
	functions int
	liveReads int
	mutations int
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
		// A package's Script constant is often shared verbatim across
		// several DDL registrations dispatching off the same body (see
		// lint-links-page-limit.go's identical note) — key on the script
		// TEXT within the package so the identical body is examined once.
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
		findings = append(findings, "lint-live-read-pinned-mutation: examined ZERO scripts — the corpus walk is broken, and a gate that checked nothing has no all-clear to give")
	}

	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) == 0 {
		fmt.Printf("lint-live-read-pinned-mutation: clean — %d script(s) across %d package(s); %d function(s), %d live read(s), %d mutation(s) checked\n",
			st.scripts, st.packages, st.functions, st.liveReads, st.mutations)
		return
	}
	fmt.Printf("lint-live-read-pinned-mutation: %d issue(s) — %d script(s) across %d package(s), %d function(s), %d live read(s), %d mutation(s) checked\n",
		len(findings), st.scripts, st.packages, st.functions, st.liveReads, st.mutations)
	if strict {
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Annotation binding — the same rule scripts/lint-conventions.go's
// annotation / annotationSpans / annotationAnchor / annotationSpan /
// isCommentLine define (that file's ~1042-1138) for every `#`-comment
// declaration family in this repo, adapted Starlark-only (isCommentLine here
// drops the `//` arm; indentWidth is a plain leading-whitespace count, since
// the corpus is space-indented Starlark). Rewritten rather than imported
// because lint-conventions.go is itself a `//go:build ignore` main package.
// annotationAnchor / annotationSpan are the two primitives copied verbatim;
// this gate has two DIFFERENT callers over them (annotationsAtOwnLine,
// buildOccAt, below) rather than one shared `annotationSpans` wrapper,
// because they need different reach — see each's own doc comment.
// ---------------------------------------------------------------------------

// annotation is one classification comment: the raw line it sits on, the
// class/shape it declares (regex capture group 1), and its trailing `<why>`
// (capture group 2, when present).
type annotation struct {
	text  string
	shape string
	why   string
}

// annotationAnchor resolves the ONE statement an annotation on line i
// (0-based) binds to, as a 0-based index, or -1 when it binds to nothing.
func annotationAnchor(lines []string, i int) int {
	if !isCommentLine(lines[i]) {
		return i
	}
	for j := i + 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == "" {
			return -1
		}
		if isCommentLine(lines[j]) {
			continue
		}
		return j
	}
	return -1
}

// annotationSpan lists the 1-based lines an annotation anchored at the
// 0-based index anchor covers: the anchored statement plus its own
// indentation block — so an annotation above an `if` covers every mutation
// nested inside it.
func annotationSpan(lines []string, anchor int) []int {
	out := []int{anchor + 1}
	indent := indentWidth(lines[anchor])
	for j := anchor + 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) != "" && indentWidth(lines[j]) <= indent {
			break
		}
		out = append(out, j+1)
	}
	return out
}

// isCommentLine reports whether a line is only a comment — Starlark `#` (the
// only form these embedded scripts carry).
func isCommentLine(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "#")
}

// indentWidth counts leading whitespace bytes (tabs count as one column).
func indentWidth(line string) int {
	n := 0
	for _, c := range line {
		if c != ' ' && c != '\t' {
			break
		}
		n++
	}
	return n
}

// ---------------------------------------------------------------------------
// Read-posture / escape-hatch annotations this gate binds against.
// ---------------------------------------------------------------------------

var (
	// readPosture mirrors lint-conventions.go's own regex verbatim (Contract
	// #2 §2.5): the classification a script's kv.Read/kv.Links call must
	// carry.
	readPosture = regexp.MustCompile(`#\s*read-posture:\s*\(([acdef])\)(.*)$`)
	// occLiveUnpinned is this gate's own escape hatch: `# occ: live-unpinned
	// <why>` on a mutation exempts it. Capture group 1 is the constant
	// "live-unpinned" (so buildOccAt's shared `annotation{shape: m[1]}`
	// construction, mirroring readPosture's own, needs no special-casing);
	// group 2 is the `<why>`.
	occLiveUnpinned = regexp.MustCompile(`#\s*occ:\s*(live-unpinned)\b(.*)$`)
)

// hydratedClasses are the read-posture classes the descriptor floor /
// applyHydratedRevisions already hydrates — a bare update/tombstone on a key
// obtained through one of these reads is engine-conditioned and out of
// scope.
var hydratedClasses = map[string]bool{"a": true, "d": true, "f": true}

// ---------------------------------------------------------------------------
// Key canonicalization — a `+`-concatenation chain, or a single-`%s` `%`
// format expression, flattens to a list of parts (a literal string value, or
// the rendered text of a non-literal leaf), with adjacent literal parts
// merged. Two expressions name the same key when their part lists match.
// ---------------------------------------------------------------------------

type kpart struct {
	lit  bool
	s    string      // the literal's string value if lit; otherwise the leaf's rendered text
	expr syntax.Expr // the original expression this leaf came from (nil for lit); lets keyReferencesAny walk it for an Ident, rather than string-matching its rendered text
}

type kparts []kpart

func canonicalizeExpr(e syntax.Expr) kparts {
	e = unparen(e)
	if bin, ok := e.(*syntax.BinaryExpr); ok {
		switch bin.Op {
		case syntax.PLUS:
			return mergeLits(append(canonicalizeExpr(bin.X), canonicalizeExpr(bin.Y)...))
		case syntax.PERCENT:
			if parts, ok := canonicalizePercentFormat(bin); ok {
				return parts
			}
		}
	}
	if lit, ok := e.(*syntax.Literal); ok && lit.Token == syntax.STRING {
		if s, ok := lit.Value.(string); ok {
			return kparts{{lit: true, s: s}}
		}
	}
	return kparts{{lit: false, s: exprString(e), expr: e}}
}

// canonicalizePercentFormat handles `"...%s..." % x` — a single `%s`
// placeholder and no other `%` directive — as the literal prefix/suffix
// around x's own canonical parts. Anything else ('%d', two placeholders, a
// tuple RHS) is left unmodelled (ok=false): the leaf-rendering fallback in
// canonicalizeExpr treats the whole expression as one opaque part instead.
func canonicalizePercentFormat(bin *syntax.BinaryExpr) (kparts, bool) {
	lit, ok := unparen(bin.X).(*syntax.Literal)
	if !ok || lit.Token != syntax.STRING {
		return nil, false
	}
	s, ok := lit.Value.(string)
	if !ok || strings.Count(s, "%s") != 1 {
		return nil, false
	}
	if strings.ContainsRune(strings.Replace(s, "%s", "", 1), '%') {
		return nil, false
	}
	halves := strings.SplitN(s, "%s", 2)
	var out kparts
	if halves[0] != "" {
		out = append(out, kpart{lit: true, s: halves[0]})
	}
	out = append(out, canonicalizeExpr(bin.Y)...)
	if halves[1] != "" {
		out = append(out, kpart{lit: true, s: halves[1]})
	}
	return mergeLits(out), true
}

func mergeLits(in kparts) kparts {
	var out kparts
	for _, p := range in {
		if p.lit && len(out) > 0 && out[len(out)-1].lit {
			out[len(out)-1].s += p.s
			continue
		}
		out = append(out, p)
	}
	return out
}

func keysEqual(a, b kparts) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].lit != b[i].lit || a[i].s != b[i].s {
			return false
		}
	}
	return true
}

// substitute replaces every non-literal part whose rendered text names a key
// in bindings with that binding's own parts, then re-merges adjacent
// literals. A part with no binding is left as-is (an opaque leaf — a fixed
// non-parameter expression, or a variable in the calling function's own
// scope once substitution reaches the top).
func substitute(tpl kparts, bindings map[string]kparts) kparts {
	var out kparts
	for _, p := range tpl {
		if p.lit {
			out = append(out, p)
			continue
		}
		if repl, ok := bindings[p.s]; ok {
			out = append(out, repl...)
			continue
		}
		out = append(out, p)
	}
	return mergeLits(out)
}

// resolveKey is THE key-canonicalization entry point for any expression
// encountered during a live scan: canonicalize its shape, then resolve every
// leaf through the current scope's plain-alias bindings — so
// `dkey = pkey + ".demographics"; kv.Read(dkey)` and `pkey + "." + "demographics"`
// resolve to the identical part list.
func resolveKey(e syntax.Expr, sc *scope) kparts {
	return substitute(canonicalizeExpr(e), sc.bindings)
}

func keyDisplay(parts kparts) string {
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			b.WriteString("+")
		}
		if p.lit {
			fmt.Fprintf(&b, "%q", p.s)
		} else {
			b.WriteString(p.s)
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Mutation-dict classification.
// ---------------------------------------------------------------------------

// mutVerdict classifies one candidate mutation — an inline dict literal, or
// a helper's own resolved shape (in terms of the helper's own parameter
// names until substituted at a call site).
type mutVerdict struct {
	relevant  bool   // false for a "create", a non-mutation dict, or anything unresolved
	kind      string // "update" | "tombstone", meaningful only when relevant
	pinned    bool   // carries (or resolves to) an expectedRevision
	keyTpl    kparts
	condParam string // non-empty: pinned iff the named parameter is a non-None argument at the call site
}

func dictField(dict *syntax.DictExpr, name string) (syntax.Expr, bool) {
	for _, el := range dict.List {
		entry, ok := el.(*syntax.DictEntry)
		if !ok {
			continue
		}
		lit, ok := unparen(entry.Key).(*syntax.Literal)
		if !ok || lit.Token != syntax.STRING {
			continue
		}
		if s, ok := lit.Value.(string); ok && s == name {
			return entry.Value, true
		}
	}
	return nil, false
}

func dictStringField(dict *syntax.DictExpr, name string) (string, bool) {
	v, ok := dictField(dict, name)
	if !ok {
		return "", false
	}
	return literalStringOf(v)
}

func literalStringOf(e syntax.Expr) (string, bool) {
	lit, ok := unparen(e).(*syntax.Literal)
	if !ok || lit.Token != syntax.STRING {
		return "", false
	}
	s, ok := lit.Value.(string)
	return s, ok
}

// classifyDictLiteral judges one `{"op": ..., "key": ..., ...}` dict on its
// own terms — no index-assign / branch context, which the caller layers in
// (inline via dictPins, or via classifyBody's merge for a helper). The key
// is resolved through sc's CURRENT alias bindings, the same way a
// read's key is (resolveKey) — resolving the two sides through different
// rules would silently break key equality the moment either side names its
// key through an aliased local rather than inline.
//
// An inline `"expectedRevision": <expr>` field is not automatically PINNED:
// if `<expr>` is a bare reference to a `param=None`-defaulted parameter of
// the enclosing def (noneParams), the Processor treats a `None` value as no
// pin at all (starlark_runner.go:368), so whether THIS dict pins anything
// depends on what the call site passes for that parameter — resolved at the
// call site via condParam, exactly like the `if rev != None: m[...] = rev`
// shape.
func classifyDictLiteral(dict *syntax.DictExpr, sc *scope, noneParams map[string]bool) mutVerdict {
	op, ok := dictStringField(dict, "op")
	if !ok || (op != "update" && op != "tombstone") {
		return mutVerdict{}
	}
	keyExpr, ok := dictField(dict, "key")
	if !ok {
		return mutVerdict{}
	}
	v := mutVerdict{relevant: true, kind: op, keyTpl: resolveKey(keyExpr, sc)}
	revExpr, hasRev := dictField(dict, "expectedRevision")
	if !hasRev {
		return v
	}
	if id, ok := unparen(revExpr).(*syntax.Ident); ok && noneParams[id.Name] {
		v.condParam = id.Name
		return v
	}
	v.pinned = true
	return v
}

func isLiteralNone(e syntax.Expr) bool {
	id, ok := unparen(e).(*syntax.Ident)
	return ok && id.Name == "None"
}

// ---------------------------------------------------------------------------
// Helper (mutation) classification — any top-level or nested `def`, no
// naming convention required: a def that never resolves to a mutation
// dict is simply not relevant, so attempting every callee costs nothing.
// Classified once per script (memoized in helperCtx.memo) via a
// control-flow-aware scan of its own body (classifyBody / scanHelperStmts),
// so a conditionally-pinned helper and a branch-ambiguous return are
// both resolved correctly rather than by a flat whole-function pass.
// ---------------------------------------------------------------------------

type helperCtx struct {
	defs     map[string]*syntax.DefStmt
	memo     map[string]*mutVerdict
	visiting map[string]bool
}

func classifyHelper(name string, hctx *helperCtx) mutVerdict {
	if v, ok := hctx.memo[name]; ok {
		return *v
	}
	d, ok := hctx.defs[name]
	if !ok {
		v := mutVerdict{}
		hctx.memo[name] = &v
		return v
	}
	if hctx.visiting[name] {
		return mutVerdict{}
	}
	hctx.visiting[name] = true
	v := classifyBody(d, hctx)
	delete(hctx.visiting, name)
	hctx.memo[name] = &v
	return v
}

// classifyBody runs a dedicated, simplified version of the main engine's
// control-flow scan (scanHelperStmts) over d's body — threading only
// `bindings` (plain-alias resolution) and `dictPins` (per-branch pin
// tracking), no
// read-hazard reporting — and combines the verdicts of every `return`
// reached.
func classifyBody(d *syntax.DefStmt, hctx *helperCtx) mutVerdict {
	noneParams := noneDefaultParams(d)
	var verdicts []mutVerdict
	sc := newScope()
	scanHelperStmts(d.Body, sc, hctx, noneParams, &verdicts)
	return combineReturnVerdicts(verdicts)
}

func noneDefaultParams(d *syntax.DefStmt) map[string]bool {
	out := map[string]bool{}
	for _, p := range d.Params {
		if bin, ok := p.(*syntax.BinaryExpr); ok {
			if id, ok := bin.X.(*syntax.Ident); ok && isLiteralNone(bin.Y) {
				out[id.Name] = true
			}
		}
	}
	return out
}

// condTestsParamNotNone reports whether cond is exactly `<param> != None` (or
// reversed), where param carries a `param=None` default — the one
// conditional-pin shape this gate recognizes. Returns "" otherwise.
func condTestsParamNotNone(cond syntax.Expr, noneParams map[string]bool) string {
	bin, ok := unparen(cond).(*syntax.BinaryExpr)
	if !ok || bin.Op != syntax.NEQ {
		return ""
	}
	if id, ok := unparen(bin.X).(*syntax.Ident); ok && noneParams[id.Name] && isLiteralNone(bin.Y) {
		return id.Name
	}
	if id, ok := unparen(bin.Y).(*syntax.Ident); ok && noneParams[id.Name] && isLiteralNone(bin.X) {
		return id.Name
	}
	return ""
}

// scanHelperStmts walks a helper's body in program order, threading `sc`,
// and appends the verdict of every `return` reached to *verdicts. Returns
// whether the list is guaranteed to exit rather than fall through.
func scanHelperStmts(stmts []syntax.Stmt, sc *scope, hctx *helperCtx, noneParams map[string]bool, verdicts *[]mutVerdict) bool {
	for _, s := range stmts {
		switch stmt := s.(type) {
		case *syntax.AssignStmt:
			if stmt.Op != syntax.EQ {
				continue
			}
			switch lhs := stmt.LHS.(type) {
			case *syntax.Ident:
				if dict, ok := unparen(stmt.RHS).(*syntax.DictExpr); ok {
					sc.dictPins[lhs.Name] = &dictPinState{dict: dict, verdict: classifyDictLiteral(dict, sc, noneParams)}
				} else {
					delete(sc.dictPins, lhs.Name)
					sc.bindings[lhs.Name] = resolveKey(stmt.RHS, sc)
				}
			case *syntax.IndexExpr:
				markIndexPin(lhs, sc)
			}

		case *syntax.ReturnStmt:
			if stmt.Result != nil {
				*verdicts = append(*verdicts, classifyReturnExprAt(stmt.Result, sc, hctx, noneParams))
			}
			return true

		case *syntax.ExprStmt:
			if isFailCall(stmt.X) {
				return true
			}

		case *syntax.BranchStmt:
			if stmt.Token == syntax.BREAK || stmt.Token == syntax.CONTINUE {
				return true
			}

		case *syntax.IfStmt:
			condParam := condTestsParamNotNone(stmt.Cond, noneParams)
			before := sc.clone()
			trueSc := before.clone()
			trueTerm := scanHelperStmts(stmt.True, trueSc, hctx, noneParams, verdicts)
			falseSc := before.clone()
			falseTerm := scanHelperStmts(stmt.False, falseSc, hctx, noneParams, verdicts)
			merged := before.clone()
			mergeBindings(merged, trueSc, falseSc, trueTerm, falseTerm)
			mergeDictPinsHelper(merged, trueSc, falseSc, trueTerm, falseTerm, condParam)
			*sc = *merged
			if trueTerm && falseTerm {
				return true
			}

		case *syntax.ForStmt:
			bodySc := sc.clone()
			scanHelperStmts(stmt.Body, bodySc, hctx, noneParams, verdicts)

		case *syntax.WhileStmt:
			bodySc := sc.clone()
			scanHelperStmts(stmt.Body, bodySc, hctx, noneParams, verdicts)
		}
	}
	return false
}

// classifyReturnExprAt classifies one `return <expr>`, against the scope at
// that point: a direct dict literal, a name resolved through sc.dictPins, or
// a delegating call to another helper (resolved recursively, then
// re-expressed in terms of the CURRENT helper's own names by substituting
// the delegation call's actual arguments through sc).
func classifyReturnExprAt(e syntax.Expr, sc *scope, hctx *helperCtx, noneParams map[string]bool) mutVerdict {
	e = unparen(e)
	switch x := e.(type) {
	case *syntax.DictExpr:
		return classifyDictLiteral(x, sc, noneParams)
	case *syntax.Ident:
		if dp, ok := sc.dictPins[x.Name]; ok {
			return dp.verdict
		}
		return mutVerdict{}
	case *syntax.CallExpr:
		id, ok := x.Fn.(*syntax.Ident)
		if !ok {
			return mutVerdict{}
		}
		calleeDef, ok := hctx.defs[id.Name]
		if !ok {
			return mutVerdict{}
		}
		callee := classifyHelper(id.Name, hctx)
		if !callee.relevant {
			return callee
		}
		bindings := buildBindings(calleeDef, x, sc)
		callee.keyTpl = substitute(callee.keyTpl, bindings)
		return callee
	}
	return mutVerdict{}
}

// combineReturnVerdicts folds a helper's (possibly several, branching)
// return statements into one verdict: any relevant BARE branch makes the
// whole helper BARE (a caller cannot know which branch a later run takes,
// and a bare branch is the risk this gate exists to catch); a relevant
// conditionally-pinned branch is reported as such over a plain pinned one
// found elsewhere (the call site is what resolves it); otherwise a relevant
// PINNED branch wins; otherwise the helper is irrelevant.
func combineReturnVerdicts(verdicts []mutVerdict) mutVerdict {
	var relevant []mutVerdict
	for _, v := range verdicts {
		if v.relevant {
			relevant = append(relevant, v)
		}
	}
	for _, v := range relevant {
		if !v.pinned && v.condParam == "" {
			return v
		}
	}
	for _, v := range relevant {
		if v.condParam != "" {
			return v
		}
	}
	if len(relevant) > 0 {
		return relevant[0]
	}
	return mutVerdict{}
}

// markIndexPin records `name["expectedRevision"] = ...` against name's
// CURRENT dict binding, if any — the nearest preceding `name = {...}`
// assignment in this control-flow path, not any assignment of that name
// anywhere in the function.
func markIndexPin(lhs *syntax.IndexExpr, sc *scope) {
	id, ok := lhs.X.(*syntax.Ident)
	if !ok {
		return
	}
	s, ok := literalStringOf(lhs.Y)
	if !ok || s != "expectedRevision" {
		return
	}
	if dp, ok := sc.dictPins[id.Name]; ok {
		dp.verdict.pinned = true
	}
}

// resolveCondParamArg finds the actual argument call binds to calleeDef's
// parameter paramName — by keyword, else by position — or reports it absent
// (the caller relied on the default).
func resolveCondParamArg(calleeDef *syntax.DefStmt, call *syntax.CallExpr, paramName string) (syntax.Expr, bool) {
	params := paramNamesInOrder(calleeDef)
	idx := -1
	for i, p := range params {
		if p == paramName {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, false
	}
	pos, kw := splitArgs(call.Args)
	if e, ok := kw[paramName]; ok {
		return e, true
	}
	if idx < len(pos) {
		return pos[idx], true
	}
	return nil, false
}

// looksMutationShaped reports whether d's body contains ANY dict literal
// declaring an update/tombstone op, even if this gate's structured
// resolution could not classify a call to d (a list-returning helper,
// `mutations.extend(helper())`, or some other shape) — used only to decide
// whether a VERBOSE=1 "unmodelled" note is worth printing for an unresolved
// callee, so a genuinely unrelated helper (parts_of, required_string) stays
// silent.
func looksMutationShaped(d *syntax.DefStmt) bool {
	found := false
	syntax.Walk(d, func(n syntax.Node) bool {
		if found || n == nil {
			return false
		}
		if dict, ok := n.(*syntax.DictExpr); ok {
			if op, ok := dictStringField(dict, "op"); ok && (op == "update" || op == "tombstone") {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// buildBindings maps calleeDef's own parameter names to the RESOLVED
// key-parts of the actual argument expressions at one call site, resolved
// through sc (the scope at the call site): an aliased argument resolves
// through whatever `name = <expr>` bindings are visible there).
func buildBindings(calleeDef *syntax.DefStmt, call *syntax.CallExpr, sc *scope) map[string]kparts {
	bindings := map[string]kparts{}
	params := paramNamesInOrder(calleeDef)
	pos, kw := splitArgs(call.Args)
	for i, p := range params {
		if i < len(pos) {
			bindings[p] = resolveKey(pos[i], sc)
		}
	}
	for name, expr := range kw {
		bindings[name] = resolveKey(expr, sc)
	}
	return bindings
}

func paramNamesInOrder(d *syntax.DefStmt) []string {
	var out []string
	for _, p := range d.Params {
		switch x := p.(type) {
		case *syntax.Ident:
			out = append(out, x.Name)
		case *syntax.BinaryExpr:
			if id, ok := x.X.(*syntax.Ident); ok {
				out = append(out, id.Name)
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

func unparen(e syntax.Expr) syntax.Expr {
	for {
		p, ok := e.(*syntax.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// exprString renders an expression well enough for a finding message and for
// a canonical key's opaque leaves — identifiers, literals, attributes,
// calls, slices, conditionals, and binary/unary operators, each rendered
// STRUCTURALLY (through their own children), so two expressions of the same
// shape but different content (identity-hygiene's `primary`/`secondary`,
// both a CondExpr) render as different strings rather than colliding into
// the same opaque placeholder — see BOUNDARY. Anything this renderer still
// does not understand structurally (a comprehension, a lambda, ...) renders
// unique to its own source position, so it can never accidentally equal a
// different such expression elsewhere.
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
	case *syntax.CondExpr:
		return "(" + exprString(x.True) + " if " + exprString(x.Cond) + " else " + exprString(x.False) + ")"
	case *syntax.SliceExpr:
		var b strings.Builder
		b.WriteString(exprString(x.X))
		b.WriteByte('[')
		if x.Lo != nil {
			b.WriteString(exprString(x.Lo))
		}
		b.WriteByte(':')
		if x.Hi != nil {
			b.WriteString(exprString(x.Hi))
		}
		if x.Step != nil {
			b.WriteByte(':')
			b.WriteString(exprString(x.Step))
		}
		b.WriteByte(']')
		return b.String()
	case *syntax.TupleExpr:
		parts := make([]string, 0, len(x.List))
		for _, el := range x.List {
			parts = append(parts, exprString(el))
		}
		return "(" + strings.Join(parts, ",") + ")"
	case *syntax.ListExpr:
		parts := make([]string, 0, len(x.List))
		for _, el := range x.List {
			parts = append(parts, exprString(el))
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	pos, _ := e.Span()
	return fmt.Sprintf("<unmodelled:%T@%s:%d:%d>", e, pos.Filename(), pos.Line, pos.Col)
}

func firstArg(call *syntax.CallExpr) syntax.Expr {
	pos, kw := splitArgs(call.Args)
	if len(pos) > 0 {
		return pos[0]
	}
	if k, ok := kw["key"]; ok {
		return k
	}
	return nil
}

func isFailCall(e syntax.Expr) bool {
	call, ok := unparen(e).(*syntax.CallExpr)
	if !ok {
		return false
	}
	id, ok := call.Fn.(*syntax.Ident)
	return ok && id.Name == "fail"
}

func isKVRead(call *syntax.CallExpr) bool {
	dot, ok := call.Fn.(*syntax.DotExpr)
	if !ok || dot.Name.Name != "Read" {
		return false
	}
	id, ok := dot.X.(*syntax.Ident)
	return ok && id.Name == "kv"
}

// isKVLinksCall reports whether call is `kv.Links(...)` — item 1's page
// source.
func isKVLinksCall(call *syntax.CallExpr) bool {
	dot, ok := call.Fn.(*syntax.DotExpr)
	if !ok || dot.Name.Name != "Links" {
		return false
	}
	id, ok := dot.X.(*syntax.Ident)
	return ok && id.Name == "kv"
}

func callLine(call *syntax.CallExpr) int {
	if call.Lparen.IsValid() {
		return int(call.Lparen.Line)
	}
	pos, _ := call.Span()
	return int(pos.Line)
}

func dictLine(d *syntax.DictExpr) int {
	if d.Lbrace.IsValid() {
		return int(d.Lbrace.Line)
	}
	pos, _ := d.Span()
	return int(pos.Line)
}

func displayClass(shape string) string {
	if shape == "" {
		return "b"
	}
	return shape
}

// ---------------------------------------------------------------------------
// READ HELPER classification — a top-level/nested def whose body holds
// a live kv.Read on a key expressible purely in terms of its own
// parameters (vertex_live(key), aspect_data(key)). A def that reads from
// `state[key]` (vertex_alive) rather than kv.Read is not one: only a real
// live kv.Read call qualifies. Found via a simple flat forward walk (best
// effort — the helper's OWN internal shape does not need branch-precision
// the way the calling script's does; every read-helper in the corpus reads
// unconditionally near the top of its body).
// ---------------------------------------------------------------------------

type readHelperVerdict struct {
	resolved bool
	keyTpl   kparts
	rawClass string // "", "a", "c", "d", "e", "f" — as annotated on the internal kv.Read
}

func classifyReadHelper(name string, defs map[string]*syntax.DefStmt, postureAt map[int]annotation, memo map[string]*readHelperVerdict) readHelperVerdict {
	if v, ok := memo[name]; ok {
		return *v
	}
	v := readHelperVerdict{}
	if d, ok := defs[name]; ok {
		v = findReadHelperShape(d, postureAt)
	}
	memo[name] = &v
	return v
}

func findReadHelperShape(d *syntax.DefStmt, postureAt map[int]annotation) readHelperVerdict {
	paramSet := map[string]bool{}
	for _, p := range paramNamesInOrder(d) {
		paramSet[p] = true
	}
	localBindings := map[string]kparts{}
	var found *readHelperVerdict

	var scanForRead func(e syntax.Expr)
	scanForRead = func(e syntax.Expr) {
		if e == nil || found != nil {
			return
		}
		syntax.Walk(e, func(n syntax.Node) bool {
			if found != nil || n == nil {
				return false
			}
			call, ok := n.(*syntax.CallExpr)
			if !ok || !isKVRead(call) {
				return true
			}
			keyExpr := firstArg(call)
			if keyExpr == nil {
				return true
			}
			resolved := substitute(canonicalizeExpr(keyExpr), localBindings)
			if !allSlotsAreParams(resolved, paramSet) {
				return true
			}
			ln := callLine(call)
			v := readHelperVerdict{resolved: true, keyTpl: resolved, rawClass: postureAt[ln].shape}
			found = &v
			return false
		})
	}
	var walk func(stmts []syntax.Stmt)
	walk = func(stmts []syntax.Stmt) {
		for _, s := range stmts {
			if found != nil {
				return
			}
			switch st := s.(type) {
			case *syntax.AssignStmt:
				if st.Op == syntax.EQ {
					scanForRead(st.RHS)
					if id, ok := st.LHS.(*syntax.Ident); ok && found == nil {
						localBindings[id.Name] = substitute(canonicalizeExpr(st.RHS), localBindings)
					}
				}
			case *syntax.IfStmt:
				scanForRead(st.Cond)
				walk(st.True)
				walk(st.False)
			case *syntax.ForStmt:
				walk(st.Body)
			case *syntax.WhileStmt:
				walk(st.Body)
			case *syntax.ReturnStmt:
				scanForRead(st.Result)
			case *syntax.ExprStmt:
				scanForRead(st.X)
			}
		}
	}
	walk(d.Body)
	if found != nil {
		return *found
	}
	return readHelperVerdict{}
}

func allSlotsAreParams(parts kparts, paramSet map[string]bool) bool {
	for _, p := range parts {
		if !p.lit && !paramSet[p.s] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// PAGE HELPER classification (item 1) — a def whose body RETURNS a
// kv.Links(...) page (directly, or via one of its own local variables bound
// to one), so a call to it used as a for-loop's iterable makes the loop's
// entry variable exactly as live as a direct `for lk in kv.Links(...):`. One
// helper hop only (a page helper calling another page helper is not
// resolved) — see BOUNDARY.
// ---------------------------------------------------------------------------

// isLinksResultExpr reports whether e is (directly, or through a local name
// already known to be bound to one, via pageVars) a kv.Links(...) result —
// the call itself, or a tuple whose first element is.
func isLinksResultExpr(e syntax.Expr, pageVars map[string]bool) bool {
	switch x := unparen(e).(type) {
	case *syntax.CallExpr:
		return isKVLinksCall(x)
	case *syntax.Ident:
		return pageVars[x.Name]
	case *syntax.TupleExpr:
		if len(x.List) > 0 {
			return isLinksResultExpr(x.List[0], pageVars)
		}
	}
	return false
}

// classifyPageHelper reports whether def name's body returns a kv.Links(...)
// page, via a simple flat forward walk (like classifyReadHelper — the
// helper's own internal shape does not need branch precision).
func classifyPageHelper(name string, defs map[string]*syntax.DefStmt, memo map[string]bool) bool {
	if v, ok := memo[name]; ok {
		return v
	}
	d, ok := defs[name]
	if !ok {
		memo[name] = false
		return false
	}
	found := false
	localPageVars := map[string]bool{}
	var walk func(stmts []syntax.Stmt)
	walk = func(stmts []syntax.Stmt) {
		for _, s := range stmts {
			if found {
				return
			}
			switch st := s.(type) {
			case *syntax.AssignStmt:
				if st.Op == syntax.EQ && isLinksResultExpr(st.RHS, localPageVars) {
					switch lhs := st.LHS.(type) {
					case *syntax.TupleExpr:
						if len(lhs.List) > 0 {
							if id, ok := lhs.List[0].(*syntax.Ident); ok {
								localPageVars[id.Name] = true
							}
						}
					case *syntax.Ident:
						localPageVars[lhs.Name] = true
					}
				}
			case *syntax.IfStmt:
				walk(st.True)
				walk(st.False)
			case *syntax.ForStmt:
				walk(st.Body)
			case *syntax.WhileStmt:
				walk(st.Body)
			case *syntax.ReturnStmt:
				if st.Result != nil && isLinksResultExpr(st.Result, localPageVars) {
					found = true
				}
			}
		}
	}
	walk(d.Body)
	memo[name] = found
	return found
}

// isLinksPageIterable reports whether e (a for-loop's own iterable
// expression) is a kv.Links(...) page: directly, through a plain variable
// already recorded in sc.linksPageVars, through `page["links"]` off such a
// variable, or through a call to a classified page helper.
func isLinksPageIterable(e syntax.Expr, sc *scope, ctx *funcCtx) bool {
	switch x := unparen(e).(type) {
	case *syntax.Ident:
		return sc.linksPageVars[x.Name]
	case *syntax.CallExpr:
		if isKVLinksCall(x) {
			return true
		}
		if id, ok := x.Fn.(*syntax.Ident); ok {
			if _, ok := ctx.defs[id.Name]; ok {
				return classifyPageHelper(id.Name, ctx.defs, ctx.pageHelperMemo)
			}
		}
	case *syntax.IndexExpr:
		if base, ok := unparen(x.X).(*syntax.Ident); ok {
			return sc.linksPageVars[base.Name]
		}
	}
	return false
}

// linkEntryFields is linkDocToStarlark's own field set
// (internal/processor/starlark_kv.go, the function projecting a LinkDoc into
// the struct a script reads from a kv.Links page) — the closed set of
// attributes a page entry can carry.
var linkEntryFields = map[string]bool{
	"key": true, "class": true, "isDeleted": true, "data": true,
	"revision": true, "sourceVertex": true, "targetVertex": true,
}

// linkEntryUsageShaped is isLinksPageIterable's structural fallback, for a
// page threaded further than one helper hop can trace: a page's list of
// entries collected into an accumulator across a bounded cursor loop
// (collect_live_sweep-style) and handed on as a plain function PARAMETER
// (`sweep_inbound(subject_key, hits)`), or dict-collected by `.key`
// (`out[lk.key] = lk`) — provenance this gate does not chase across a
// function boundary (see BOUNDARY). Instead it asks the ONLY question that
// still needs answering: is `<var>` USED, within the loop that binds it, the
// way a link entry — and only a link entry — is used? Every `<var>.<attr>`
// access must name one of linkDocToStarlark's own seven fields, and at least
// one must be `.key` or `.revision` (positive evidence — an incidental
// `.class` or `.data` alone proves nothing). A var that never carries a
// dotted access at all is not claimed either way.
func linkEntryUsageShaped(varName string, body []syntax.Stmt) bool {
	sawEvidence := false
	allKnown := true
	for _, s := range body {
		syntax.Walk(s, func(n syntax.Node) bool {
			if n == nil {
				return false
			}
			dot, ok := n.(*syntax.DotExpr)
			if !ok {
				return true
			}
			id, ok := dot.X.(*syntax.Ident)
			if !ok || id.Name != varName {
				return true
			}
			if !linkEntryFields[dot.Name.Name] {
				allKnown = false
				return true
			}
			if dot.Name.Name == "key" || dot.Name.Name == "revision" {
				sawEvidence = true
			}
			return true
		})
	}
	return allKnown && sawEvidence
}

// ---------------------------------------------------------------------------
// The live scope threaded through the MAIN script scan: the reads visible at
// one point in the control flow (liveReadSet), the plain-alias bindings a
// key expression resolves through, and the per-name dict-literal pin
// state.
// ---------------------------------------------------------------------------

type readInfo struct {
	line     int
	desc     string // exprString of the read call — "kv.Read(k)" or "vertex_live(sess_key)"
	key      kparts
	classTag string
}

type liveReadSet []readInfo

type dictPinState struct {
	dict    *syntax.DictExpr
	verdict mutVerdict
}

type scope struct {
	reads    liveReadSet
	bindings map[string]kparts
	dictPins map[string]*dictPinState
	// linksPageVars names plain variables currently bound to a kv.Links(...)
	// page result (item 1: page-entry keys) — the LIST half of the
	// (list, cursor) tuple `kv.Links` returns, e.g. `links` in
	// `links, cursor = kv.Links(...)`.
	linksPageVars map[string]bool
}

func newScope() *scope {
	return &scope{bindings: map[string]kparts{}, dictPins: map[string]*dictPinState{}, linksPageVars: map[string]bool{}}
}

func (s *scope) clone() *scope {
	ns := &scope{
		reads:         cloneReads(s.reads),
		bindings:      make(map[string]kparts, len(s.bindings)),
		dictPins:      make(map[string]*dictPinState, len(s.dictPins)),
		linksPageVars: make(map[string]bool, len(s.linksPageVars)),
	}
	for k, v := range s.bindings {
		ns.bindings[k] = v
	}
	for k, v := range s.dictPins {
		cp := *v
		ns.dictPins[k] = &cp
	}
	for k, v := range s.linksPageVars {
		ns.linksPageVars[k] = v
	}
	return ns
}

// cloneReads copies in with len==cap, so any append a branch performs on the
// copy always allocates a new backing array rather than aliasing a sibling
// branch's copy — required for the `xOut[len(before):]`-style slicing used
// to see only that branch's OWN new entries.
func cloneReads(in liveReadSet) liveReadSet {
	out := make(liveReadSet, len(in), len(in))
	copy(out, in)
	return out
}

// mergeBindings merges trueSc/falseSc's alias bindings into merged (which
// starts as a clone of the pre-branch scope, `before`): a name inherited
// unchanged by both, or bound to the identical key by both, survives as
// that value; a name only ONE branch binds takes that branch's value
// (the other branch, ending the if unchanged, kept `before`'s own value —
// which is what `merged` already holds by construction, so nothing to do).
// A name the two branches bind to DIFFERENT values is left as `merged`
// already has it (before's own pre-if value) rather than deleted: deleting
// erred toward SILENCE — a mutation using the name afterward could never
// match ANY read, since an unresolved slot matches nothing — where the
// correct bias is toward a finding, because `before`'s value is exactly
// what a run takes on whichever branch does NOT reassign the name, so it is
// never a fabricated resolution.
func mergeBindings(merged, trueSc, falseSc *scope, trueTerm, falseTerm bool) {
	names := map[string]bool{}
	if !trueTerm {
		for k := range trueSc.bindings {
			names[k] = true
		}
	}
	if !falseTerm {
		for k := range falseSc.bindings {
			names[k] = true
		}
	}
	for name := range names {
		var tv, fv kparts
		var tok, fok bool
		if !trueTerm {
			tv, tok = trueSc.bindings[name]
		}
		if !falseTerm {
			fv, fok = falseSc.bindings[name]
		}
		switch {
		case tok && !fok:
			merged.bindings[name] = tv
		case fok && !tok:
			merged.bindings[name] = fv
		case tok && fok && keysEqual(tv, fv):
			merged.bindings[name] = tv
			// tok && fok && !keysEqual: leave merged's inherited (pre-if)
			// value in place — see the doc comment above.
		}
	}
}

// dictPinNameUnion lists (sorted, for deterministic output across runs) the
// dictPins names either non-terminating branch carries — the set both
// mergeDictPinsHelper and mergeDictPinsMain iterate.
func dictPinNameUnion(trueSc, falseSc *scope, trueTerm, falseTerm bool) []string {
	names := map[string]bool{}
	if !trueTerm {
		for k := range trueSc.dictPins {
			names[k] = true
		}
	}
	if !falseTerm {
		for k := range falseSc.dictPins {
			names[k] = true
		}
	}
	out := make([]string, 0, len(names))
	for k := range names {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// mergeDictPinsHelper merges dict-pin state inside a HELPER's own
// classification scan: two branches binding a name to the SAME dict literal
// merge (pinned only if both pin it, unless condParam recognizes the
// specific `if <param> != None:` shape); two DIFFERENT dict literals
// leave the name unknown afterward (a helper returning from inside the
// branch is how the corpus actually uses this shape, so there is no later
// "use site" to resolve it at, unlike the main engine's mergeDictPinsMain).
func mergeDictPinsHelper(merged, trueSc, falseSc *scope, trueTerm, falseTerm bool, condParam string) {
	for _, name := range dictPinNameUnion(trueSc, falseSc, trueTerm, falseTerm) {
		var t, f *dictPinState
		if !trueTerm {
			t = trueSc.dictPins[name]
		}
		if !falseTerm {
			f = falseSc.dictPins[name]
		}
		switch {
		case t != nil && f == nil:
			merged.dictPins[name] = t
		case f != nil && t == nil:
			merged.dictPins[name] = f
		case t != nil && f != nil:
			if t.dict == f.dict {
				v := t.verdict
				if t.verdict.pinned != f.verdict.pinned {
					v.pinned = false
					if condParam != "" {
						v.condParam = condParam
					}
				} else {
					v.pinned = t.verdict.pinned
				}
				merged.dictPins[name] = &dictPinState{dict: t.dict, verdict: v}
			} else {
				delete(merged.dictPins, name)
			}
		}
	}
}

// mergeDictPinsMain is mergeDictPinsHelper's counterpart in the MAIN
// script's own scan: when two branches bind a name to DIFFERENT dict
// literals, each is checked IMMEDIATELY, against its OWN branch's reads,
// rather than dropped to "unknown" — because in the main script that
// ambiguity is exactly the shape a per-branch pin needs (`m = {update}` on one arm,
// `m = {tombstone}` on the sibling): dropping it would silently stop
// checking either arm at whatever later statement uses the name.
func mergeDictPinsMain(merged, trueSc, falseSc *scope, trueTerm, falseTerm bool, ctx *funcCtx, findings *[]string) {
	for _, name := range dictPinNameUnion(trueSc, falseSc, trueTerm, falseTerm) {
		var t, f *dictPinState
		if !trueTerm {
			t = trueSc.dictPins[name]
		}
		if !falseTerm {
			f = falseSc.dictPins[name]
		}
		switch {
		case t != nil && f == nil:
			merged.dictPins[name] = t
		case f != nil && t == nil:
			merged.dictPins[name] = f
		case t != nil && f != nil:
			if t.dict == f.dict {
				v := t.verdict
				v.pinned = t.verdict.pinned && f.verdict.pinned
				merged.dictPins[name] = &dictPinState{dict: t.dict, verdict: v}
				continue
			}
			if t.verdict.relevant {
				ctx.st.mutations++
				*findings = append(*findings, checkMutationSite(ctx, dictLine(t.dict), "inline dict (via "+name+", true branch)", t.verdict, trueSc.reads)...)
			}
			if f.verdict.relevant {
				ctx.st.mutations++
				*findings = append(*findings, checkMutationSite(ctx, dictLine(f.dict), "inline dict (via "+name+", false branch)", f.verdict, falseSc.reads)...)
			}
			delete(merged.dictPins, name)
		}
	}
}

// ---------------------------------------------------------------------------
// Top-level driver.
// ---------------------------------------------------------------------------

type funcCtx struct {
	where, defName string
	postureAt      map[int]annotation
	occAt          map[int]annotation
	defs           map[string]*syntax.DefStmt
	helperMemo     map[string]*mutVerdict
	readHelperMemo map[string]*readHelperVerdict
	pageHelperMemo map[string]bool
	noneParams     map[string]bool // the CURRENT def's own param=None-defaulted parameters
	st             *stats
}

func (ctx *funcCtx) helperCtx() *helperCtx {
	return &helperCtx{defs: ctx.defs, memo: ctx.helperMemo, visiting: map[string]bool{}}
}

// collectAllDefs gathers every `def`, top-level or nested at any depth (14
// nested defs exist in the corpus, e.g. identity-hygiene/ddls.go:399-555),
// keyed by name: go.starlark.net/syntax's own Walk already recurses into a
// DefStmt's Body, so one Walk per top-level statement finds every one. A
// name reused by two defs (top-level or nested) is reported via dupeLines
// rather than silently letting the later one shadow the earlier in the map —
// every downstream lookup by name would otherwise resolve to whichever one
// happened to be seen last.
func collectAllDefs(stmts []syntax.Stmt) (defs map[string]*syntax.DefStmt, dupeLines map[string][]int) {
	defs = map[string]*syntax.DefStmt{}
	dupeLines = map[string][]int{}
	for _, s := range stmts {
		syntax.Walk(s, func(n syntax.Node) bool {
			if n == nil {
				return false
			}
			d, ok := n.(*syntax.DefStmt)
			if !ok {
				return true
			}
			if _, seen := defs[d.Name.Name]; seen {
				dupeLines[d.Name.Name] = append(dupeLines[d.Name.Name], int(d.Def.Line))
				return true
			}
			defs[d.Name.Name] = d
			return true
		})
	}
	return defs, dupeLines
}

// compoundHeaderLines is the set of 1-based lines where a `def`/`if`/`for`
// statement's own keyword sits — the shapes an escape hatch may not anchor
// to (item 4: a hatch must sit on a simple statement, not wave through a
// whole def/if/for block by declaring on its header).
func compoundHeaderLines(stmts []syntax.Stmt) map[int]bool {
	out := map[int]bool{}
	for _, s := range stmts {
		syntax.Walk(s, func(n syntax.Node) bool {
			if n == nil {
				return false
			}
			switch x := n.(type) {
			case *syntax.DefStmt:
				out[int(x.Def.Line)] = true
			case *syntax.IfStmt:
				out[int(x.If.Line)] = true
			case *syntax.ForStmt:
				out[int(x.For.Line)] = true
			}
			return true
		})
	}
	return out
}

// annotationsAtOwnLine is annotationSpans' narrower sibling for read-posture
// classification specifically: it maps ONLY the anchor line itself, never
// the anchor's wider indentation-block span. A hydrated annotation above an
// `if` must not hydrate a read nested inside that block — that read needs
// its own annotation — which is exactly the span behavior occLiveUnpinned
// (the escape hatch) still wants, so this is deliberately a DIFFERENT
// function from annotationSpans rather than a shared option on it.
func annotationsAtOwnLine(lines []string, re *regexp.Regexp) map[int]annotation {
	out := map[int]annotation{}
	for i, line := range lines {
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		a := annotation{text: line, shape: m[1]}
		if len(m) > 2 {
			a.why = m[2]
		}
		anchor := annotationAnchor(lines, i)
		if anchor < 0 {
			continue
		}
		out[anchor+1] = a
	}
	return out
}

// buildOccAt resolves the escape hatch's line coverage exactly like
// annotationSpans (block reach — the escape hatch's own requirement,
// unaffected by the narrower read-posture resolution above), except a hatch anchored to a
// def/if/for HEADER line grants no exemption at all and is reported as a
// finding of its own (item 4).
func buildOccAt(lines []string, headerLines map[int]bool, where string) (map[int]annotation, []string) {
	out := map[int]annotation{}
	var findings []string
	for i, line := range lines {
		m := occLiveUnpinned.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		a := annotation{text: line, shape: m[1]}
		if len(m) > 2 {
			a.why = m[2]
		}
		anchor := annotationAnchor(lines, i)
		if anchor < 0 {
			continue
		}
		if headerLines[anchor+1] {
			findings = append(findings, fmt.Sprintf(
				"%s: line %d: `# occ: live-unpinned` anchors to a def/if/for header at line %d, not a simple statement — a hatch must sit on the exempted statement itself (or its own comment line directly above it), not wave through the whole block it opens; no exemption granted",
				where, i+1, anchor+1))
			continue
		}
		for _, ln := range annotationSpan(lines, anchor) {
			out[ln] = a
		}
	}
	return out, findings
}

func checkScript(where, src string, st *stats) []string {
	f, err := syntax.Parse("script.star", src, 0)
	if err != nil {
		// The Processor would refuse this script too; the parse error is
		// the package's own test failure, not this gate's finding.
		return nil
	}
	st.scripts++

	lines := strings.Split(src, "\n")
	postureAt := annotationsAtOwnLine(lines, readPosture)
	headerLines := compoundHeaderLines(f.Stmts)
	occAt, findings := buildOccAt(lines, headerLines, where)

	defs, dupeLines := collectAllDefs(f.Stmts)
	for name, atLines := range dupeLines {
		findings = append(findings, fmt.Sprintf(
			"%s: def %q is declared more than once (additional line(s) %v) — a name-keyed helper/read-helper lookup would silently resolve every call to whichever one happened to be seen last",
			where, name, atLines))
	}
	var defNames []string
	for name := range defs {
		defNames = append(defNames, name)
	}
	sort.Strings(defNames)

	base := funcCtx{
		where: where, postureAt: postureAt, occAt: occAt, defs: defs,
		helperMemo: map[string]*mutVerdict{}, readHelperMemo: map[string]*readHelperVerdict{},
		pageHelperMemo: map[string]bool{}, st: st,
	}

	for _, name := range defNames {
		st.functions++
		ctx := base
		ctx.defName = name
		ctx.noneParams = noneDefaultParams(defs[name])
		findings = append(findings, checkFunction(&ctx, defs[name])...)
	}
	return dedupeFindings(findings)
}

// dedupeFindings drops a finding string this run already emitted — the same
// mutation site can be reached (and checked) more than once when the SAME
// dict-pinned name is referenced in more than one expression position (e.g.
// a membership test on the name, then its actual use in `mutations = [m]`);
// each such reference is a genuinely separate check, but a duplicate report
// of the identical hazard tells the reader nothing a single copy would not.
func dedupeFindings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, f := range in {
		if seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

func checkFunction(ctx *funcCtx, d *syntax.DefStmt) []string {
	var findings []string
	sc := newScope()
	scanStmts(d.Body, sc, ctx, &findings)
	return findings
}

// scanStmts walks stmts in program order, mutating sc in place (appends to
// sc.reads, updates sc.bindings/sc.dictPins) and appending every finding
// reached to *findings. Returns whether the list is guaranteed to exit
// (return/continue/break/fail(...)) rather than fall through to whatever
// follows it — the fact an IfStmt uses to decide whether a branch's own new
// reads reach the sibling code after it.
func scanStmts(stmts []syntax.Stmt, sc *scope, ctx *funcCtx, findings *[]string) bool {
	for _, s := range stmts {
		switch stmt := s.(type) {
		case *syntax.AssignStmt:
			if stmt.Op != syntax.EQ {
				scanExpr(stmt.LHS, sc, ctx, findings, nil)
				scanExpr(stmt.RHS, sc, ctx, findings, nil)
				continue
			}
			switch lhs := stmt.LHS.(type) {
			case *syntax.Ident:
				if dict, ok := unparen(stmt.RHS).(*syntax.DictExpr); ok {
					// Deferred: register the dict against its name now,
					// but do NOT check it as a mutation site yet — a later
					// `name["expectedRevision"] = ...` in this same
					// control-flow path still has to land before whatever
					// statement actually USES the name (a bare Ident
					// reference, handled generically in scanExpr) checks it.
					sc.dictPins[lhs.Name] = &dictPinState{dict: dict, verdict: classifyDictLiteral(dict, sc, ctx.noneParams)}
					delete(sc.linksPageVars, lhs.Name)
					scanExpr(stmt.RHS, sc, ctx, findings, dict)
				} else {
					delete(sc.dictPins, lhs.Name)
					scanExpr(stmt.RHS, sc, ctx, findings, nil)
					sc.bindings[lhs.Name] = resolveKey(stmt.RHS, sc)
					if isLinksPageIterable(stmt.RHS, sc, ctx) {
						sc.linksPageVars[lhs.Name] = true
					} else {
						delete(sc.linksPageVars, lhs.Name)
					}
				}
			case *syntax.TupleExpr:
				// item 1: `links, cursor = kv.Links(...)` (or a page-helper
				// call) marks the FIRST target as a page var.
				scanExpr(stmt.RHS, sc, ctx, findings, nil)
				if isLinksPageIterable(stmt.RHS, sc, ctx) && len(lhs.List) > 0 {
					if id, ok := lhs.List[0].(*syntax.Ident); ok {
						sc.linksPageVars[id.Name] = true
					}
				}
			case *syntax.IndexExpr:
				markIndexPin(lhs, sc)
				scanExpr(stmt.RHS, sc, ctx, findings, nil)
			default:
				scanExpr(stmt.LHS, sc, ctx, findings, nil)
				scanExpr(stmt.RHS, sc, ctx, findings, nil)
			}

		case *syntax.ExprStmt:
			scanExpr(stmt.X, sc, ctx, findings, nil)
			if isFailCall(stmt.X) {
				return true
			}

		case *syntax.ReturnStmt:
			if stmt.Result != nil {
				scanExpr(stmt.Result, sc, ctx, findings, nil)
			}
			return true

		case *syntax.BranchStmt:
			if stmt.Token == syntax.BREAK || stmt.Token == syntax.CONTINUE {
				return true
			}

		case *syntax.IfStmt:
			// Cond is scanned against `sc` itself (not a clone) — it always
			// runs regardless of which arm is taken, so a read inside it is
			// visible to both branches AND to whatever follows the whole
			// if-statement.
			scanExpr(stmt.Cond, sc, ctx, findings, nil)
			before := sc.clone()
			trueSc := before.clone()
			trueTerm := scanStmts(stmt.True, trueSc, ctx, findings)
			falseSc := before.clone()
			falseTerm := scanStmts(stmt.False, falseSc, ctx, findings)
			merged := before.clone()
			if !trueTerm {
				merged.reads = append(merged.reads, trueSc.reads[len(before.reads):]...)
			}
			if !falseTerm {
				merged.reads = append(merged.reads, falseSc.reads[len(before.reads):]...)
			}
			mergeBindings(merged, trueSc, falseSc, trueTerm, falseTerm)
			mergeDictPinsMain(merged, trueSc, falseSc, trueTerm, falseTerm, ctx, findings)
			*sc = *merged
			if trueTerm && falseTerm {
				return true
			}

		case *syntax.ForStmt:
			scanExpr(stmt.X, sc, ctx, findings, nil)
			// A loop body is its own scope for reads: it may run zero times,
			// and a read whose key depends on a name the body itself
			// reassigns (idx_key hashed fresh per candidate, or the loop's
			// own iteration variable) must not leak to code after the loop —
			// propagateLoopReads' rule, with the "carried" exception it
			// documents. bindings/dictPins never propagate past a loop at
			// all on their own (conservative default; see BOUNDARY) except
			// through that same carry mechanism.
			bodyLocal := reassignedNames(stmt.Body)
			addIdentNames(stmt.Vars, bodyLocal)
			before := sc.clone()
			bodySc := before.clone()
			// item 1: a page-entry loop seeds its own body scope with a live
			// "<var>.key" read before the body runs, so a mutation on it
			// WITHIN the same iteration (the corpus's own idiom) matches
			// directly, on top of whatever escapes the loop via
			// propagateLoopReads. isLinksPageIterable proves it from the
			// iterable's own provenance; linkEntryUsageShaped is the
			// fallback when that provenance crosses a function boundary
			// this gate does not chase (a page collected into an
			// accumulator and handed on as a plain parameter).
			if loopVar, ok := stmt.Vars.(*syntax.Ident); ok &&
				(isLinksPageIterable(stmt.X, sc, ctx) || linkEntryUsageShaped(loopVar.Name, stmt.Body)) {
				ctx.st.liveReads++
				bodySc.reads = append(bodySc.reads, readInfo{
					line:     int(stmt.For.Line),
					desc:     loopVar.Name + ".key (kv.Links page entry)",
					key:      kparts{{lit: false, s: loopVar.Name + ".key"}},
					classTag: "e",
				})
			}
			scanStmts(stmt.Body, bodySc, ctx, findings)
			propagateLoopReads(before, bodySc, sc, bodyLocal)

		case *syntax.WhileStmt:
			scanExpr(stmt.Cond, sc, ctx, findings, nil)
			bodyLocal := reassignedNames(stmt.Body)
			before := sc.clone()
			bodySc := before.clone()
			scanStmts(stmt.Body, bodySc, ctx, findings)
			propagateLoopReads(before, bodySc, sc, bodyLocal)

		case *syntax.DefStmt:
			// A nested def is classified/scanned independently by
			// checkScript's own top-level iteration over collectAllDefs
			// — skipped here so it is not reported once under its own
			// name and once again inline under its enclosing def's.
		}
	}
	return false
}

// addIdentNames adds every plain identifier reachable in e (through a
// tuple/list/paren wrapper — an assignment or for-loop target shape) to out.
func addIdentNames(e syntax.Expr, out map[string]bool) {
	switch x := e.(type) {
	case *syntax.Ident:
		out[x.Name] = true
	case *syntax.TupleExpr:
		for _, el := range x.List {
			addIdentNames(el, out)
		}
	case *syntax.ListExpr:
		for _, el := range x.List {
			addIdentNames(el, out)
		}
	case *syntax.ParenExpr:
		addIdentNames(x.X, out)
	}
}

// reassignedNames collects every plain name a `name = <expr>` assignment (or
// a `for name in ...:` loop variable) targets anywhere within stmts, at any
// nesting depth — the set a for/while body's own new reads is checked
// against before letting them reach code after the loop. It does NOT include
// the CURRENT loop's own iteration variable(s) — those are added separately
// by the ForStmt/WhileStmt caller, since stmt.Vars sits outside stmt.Body.
func reassignedNames(stmts []syntax.Stmt) map[string]bool {
	out := map[string]bool{}
	for _, s := range stmts {
		syntax.Walk(s, func(n syntax.Node) bool {
			if n == nil {
				return false
			}
			switch x := n.(type) {
			case *syntax.AssignStmt:
				if x.Op == syntax.EQ {
					addIdentNames(x.LHS, out)
				}
			case *syntax.ForStmt:
				addIdentNames(x.Vars, out)
			}
			return true
		})
	}
	return out
}

// propagateLoopReads decides which of a for/while body's newly-recorded
// reads (bodySc.reads beyond len(before.reads)) reach the code after the
// loop, appending them (and any "carry" binding that exposes one) into sc.
//
// A read whose key does not depend on any body-local name (bodyLocal,
// which includes the loop's own iteration variable) always propagates — its
// key means the same thing on every iteration and after the loop ends.
//
// A read whose key DOES depend on a body-local name is blocked UNLESS some
// name that already existed before the loop started (before.bindings) ends
// the body scan (bodySc.bindings) bound to that exact same key — a
// "carried" value (`target = k` right after `doc = kv.Read(k)`, matching
// the read k took). That read escapes too, matched the same way a branch
// disagreement is (mergeBindings): erring toward a finding rather than
// silence, and `target`'s own binding propagates out alongside it, so the
// later `make_tombstone(target)` resolves to the identical key.
func propagateLoopReads(before, bodySc, sc *scope, bodyLocal map[string]bool) {
	var carryNames []string
	for name := range before.bindings {
		carryNames = append(carryNames, name)
	}
	sort.Strings(carryNames)
	for _, r := range bodySc.reads[len(before.reads):] {
		if !keyReferencesAny(r.key, bodyLocal) {
			sc.reads = append(sc.reads, r)
			continue
		}
		for _, name := range carryNames {
			bv, ok := bodySc.bindings[name]
			if ok && keysEqual(bv, r.key) {
				sc.reads = append(sc.reads, r)
				sc.bindings[name] = bv
				break
			}
		}
	}
}

// keyReferencesAny reports whether any non-literal part of key mentions one
// of names as an IDENTIFIER — walking the leaf's own original expression
// (kpart.expr), not comparing its whole rendered text to a bare name, so
// `c["hash"]`, `lk.sourceVertex`, and `str(c)` are all correctly seen as
// referencing `c` / `lk` even though none of them render as exactly "c" or
// "lk".
func keyReferencesAny(key kparts, names map[string]bool) bool {
	for _, p := range key {
		if p.lit {
			continue
		}
		if p.expr == nil {
			if names[p.s] {
				return true
			}
			continue
		}
		found := false
		syntax.Walk(p.expr, func(n syntax.Node) bool {
			if found || n == nil {
				return false
			}
			if id, ok := n.(*syntax.Ident); ok && names[id.Name] {
				found = true
				return false
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

// scanExpr finds every read (direct kv.Read, or a read-helper call) and
// every mutation (an inline dict, a bare-Ident reference to a
// dict-pinned name, or a mutation-helper call) within e — a pure expression
// subtree, so this never crosses into a sibling statement's own scope
// (Starlark expressions never embed a statement block other than a
// LambdaExpr's single-expression body, unused in this corpus). skipDict, if
// non-nil, is the ONE outermost DictExpr this call should not itself check
// (already registered via sc.dictPins by the caller) — its children are
// still walked normally.
func scanExpr(e syntax.Expr, sc *scope, ctx *funcCtx, findings *[]string, skipDict *syntax.DictExpr) {
	if e == nil {
		return
	}
	syntax.Walk(e, func(n syntax.Node) bool {
		if n == nil {
			return false
		}
		switch x := n.(type) {
		case *syntax.DictExpr:
			if x == skipDict {
				return true
			}
			v := classifyDictLiteral(x, sc, ctx.noneParams)
			if v.relevant {
				ctx.st.mutations++
				*findings = append(*findings, checkMutationSite(ctx, dictLine(x), "inline dict", v, sc.reads)...)
			}
		case *syntax.Ident:
			if dp, ok := sc.dictPins[x.Name]; ok && dp.verdict.relevant {
				ctx.st.mutations++
				*findings = append(*findings, checkMutationSite(ctx, dictLine(dp.dict), "inline dict (via "+x.Name+")", dp.verdict, sc.reads)...)
			}
		case *syntax.CallExpr:
			handleCallExpr(x, sc, ctx, findings)
		}
		return true
	})
}

func handleCallExpr(call *syntax.CallExpr, sc *scope, ctx *funcCtx, findings *[]string) {
	if isKVRead(call) {
		checkReadCall(call, sc, ctx)
		return
	}
	if dot, ok := call.Fn.(*syntax.DotExpr); ok && dot.Name.Name == "extend" && len(call.Args) == 1 {
		if inner, ok := unparen(call.Args[0]).(*syntax.CallExpr); ok {
			if id, ok := inner.Fn.(*syntax.Ident); ok {
				if _, ok := ctx.defs[id.Name]; ok {
					hv := classifyHelper(id.Name, ctx.helperCtx())
					if !hv.relevant && verboseUnmodelled {
						fmt.Printf("  mutation: %s: %s: %s(...) at line %d via .extend(...) — list/multi-mutation-returning helper, UNMODELLED\n",
							ctx.where, ctx.defName, id.Name, callLine(inner))
					}
				}
			}
		}
	}
	id, ok := call.Fn.(*syntax.Ident)
	if !ok {
		return
	}
	calleeDef, ok := ctx.defs[id.Name]
	if !ok {
		return
	}
	rh := classifyReadHelper(id.Name, ctx.defs, ctx.postureAt, ctx.readHelperMemo)
	if rh.resolved {
		checkReadHelperCall(call, calleeDef, rh, sc, ctx)
	}
	hv := classifyHelper(id.Name, ctx.helperCtx())
	if hv.relevant {
		checkMutationHelperCall(call, calleeDef, hv, sc, ctx, findings)
	} else if !rh.resolved && verboseUnmodelled && looksMutationShaped(calleeDef) {
		fmt.Printf("  mutation: %s: %s: %s(...) at line %d — mutation-shaped helper not classified, UNMODELLED\n",
			ctx.where, ctx.defName, id.Name, callLine(call))
	}
}

func checkReadCall(call *syntax.CallExpr, sc *scope, ctx *funcCtx) {
	ln := callLine(call)
	keyExpr := firstArg(call)
	if keyExpr == nil {
		if verboseUnmodelled {
			fmt.Printf("  read: %s: %s: kv.Read(...) at line %d — no resolvable key argument, UNMODELLED\n", ctx.where, ctx.defName, ln)
		}
		return
	}
	ann := ctx.postureAt[ln]
	if hydratedClasses[ann.shape] {
		if listSites {
			fmt.Printf("  read: %s: %s: %s at line %d — class (%s) HYDRATED, out of scope\n", ctx.where, ctx.defName, exprString(call), ln, ann.shape)
		}
		return
	}
	ctx.st.liveReads++
	key := resolveKey(keyExpr, sc)
	sc.reads = append(sc.reads, readInfo{line: ln, desc: exprString(call), key: key, classTag: displayClass(ann.shape)})
	if listSites {
		fmt.Printf("  read: %s: %s: %s at line %d — LIVE (%s)\n", ctx.where, ctx.defName, exprString(call), ln, displayClass(ann.shape))
	}
}

func checkReadHelperCall(call *syntax.CallExpr, calleeDef *syntax.DefStmt, rh readHelperVerdict, sc *scope, ctx *funcCtx) {
	ln := callLine(call)
	if hydratedClasses[rh.rawClass] {
		if listSites {
			fmt.Printf("  read: %s: %s: %s at line %d — read-helper class (%s) HYDRATED, out of scope\n", ctx.where, ctx.defName, exprString(call), ln, rh.rawClass)
		}
		return
	}
	ctx.st.liveReads++
	bindings := buildBindings(calleeDef, call, sc)
	key := substitute(rh.keyTpl, bindings)
	sc.reads = append(sc.reads, readInfo{line: ln, desc: exprString(call), key: key, classTag: displayClass(rh.rawClass)})
	if listSites {
		fmt.Printf("  read: %s: %s: %s at line %d — LIVE (%s) via read-helper\n", ctx.where, ctx.defName, exprString(call), ln, displayClass(rh.rawClass))
	}
}

// checkMutationHelperCall resolves a call to a classified mutation helper at
// one call site. A conditionally-pinned helper (hv.condParam != "") is
// resolved here against the ACTUAL argument: the Processor treats
// `expectedRevision: None` as no pin at all (starlark_runner.go:368), so an
// OMITTED argument (the caller relies on the param=None default) is exactly
// as bare as an explicit `None` — neither gets the benefit of the doubt;
// only a real, non-None value is PINNED.
func checkMutationHelperCall(call *syntax.CallExpr, calleeDef *syntax.DefStmt, hv mutVerdict, sc *scope, ctx *funcCtx, findings *[]string) {
	ln := callLine(call)
	if hv.condParam != "" {
		argExpr, present := resolveCondParamArg(calleeDef, call, hv.condParam)
		switch {
		case !present:
			hv.pinned = false
			if verboseUnmodelled {
				fmt.Printf("  mutation: %s: %s: %s(...) at line %d — conditionally pinned on `%s`, called without it (relies on the None default) — treated as BARE\n",
					ctx.where, ctx.defName, calleeDef.Name.Name, ln, hv.condParam)
			}
		default:
			hv.pinned = !isLiteralNone(argExpr)
		}
		hv.condParam = ""
	}
	bindings := buildBindings(calleeDef, call, sc)
	v := mutVerdict{relevant: true, kind: hv.kind, pinned: hv.pinned, keyTpl: substitute(hv.keyTpl, bindings)}
	ctx.st.mutations++
	*findings = append(*findings, checkMutationSite(ctx, ln, calleeDef.Name.Name+"(...)", v, sc.reads)...)
}

func checkMutationSite(ctx *funcCtx, ln int, siteDesc string, v mutVerdict, reads liveReadSet) []string {
	if v.pinned {
		if listSites {
			fmt.Printf("  mutation: %s: %s: %s %s at line %d — PINNED\n", ctx.where, ctx.defName, v.kind, siteDesc, ln)
		}
		return nil
	}
	var matchedAny bool
	var findings []string
	for _, r := range reads {
		if !keysEqual(r.key, v.keyTpl) {
			continue
		}
		matchedAny = true
		if a, ok := ctx.occAt[ln]; ok && a.shape == "live-unpinned" {
			if verboseUnmodelled || listSites {
				fmt.Printf("  mutation: %s: %s: %s %s at line %d — BARE on a key matching the live (%s) read at line %d, EXEMPTED (# occ: live-unpinned%s)\n",
					ctx.where, ctx.defName, v.kind, siteDesc, ln, r.classTag, r.line, a.why)
			}
			continue
		}
		findings = append(findings, fmt.Sprintf(
			"%s: %s: %s at script line %d is a live (%s) read and the %s at line %d (%s) carries no expectedRevision — pin the read's revision (make_*_occ / \"expectedRevision\": doc.revision) or declare the key so step 4 hydrates it",
			ctx.where, ctx.defName, r.desc, r.line, r.classTag, v.kind, ln, siteDesc))
	}
	if listSites {
		if !matchedAny {
			fmt.Printf("  mutation: %s: %s: %s %s at line %d — BARE, key %s matches no live read in this function\n",
				ctx.where, ctx.defName, v.kind, siteDesc, ln, keyDisplay(v.keyTpl))
		} else if len(findings) > 0 {
			fmt.Printf("  mutation: %s: %s: %s %s at line %d — BARE, key %s: %d finding(s)\n",
				ctx.where, ctx.defName, v.kind, siteDesc, ln, keyDisplay(v.keyTpl), len(findings))
		}
	}
	return findings
}

// ---------------------------------------------------------------------------
// Self-test.
// ---------------------------------------------------------------------------

func runSelfTest(verbose bool) {
	pass := true
	check := func(cond bool, desc string) {
		switch {
		case !cond:
			fmt.Fprintln(os.Stderr, "lint-live-read-pinned-mutation selftest: FAIL —", desc)
			pass = false
		case verbose:
			fmt.Println("selftest: PASS —", desc)
		}
	}
	run := func(src string) []string {
		var st stats
		return checkScript("selftest", src, &st)
	}
	has := func(fs []string, sub string) bool {
		for _, f := range fs {
			if strings.Contains(f, sub) {
				return true
			}
		}
		return false
	}

	const makeAspectUpdate = `
def make_aspect_update(vtx_key, local_name, cls, data):
    return {"op": "update", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False, "data": data}}
`
	const makeAspectUpdateOCC = `
def make_aspect_update_occ(vtx_key, local_name, cls, data, expected_revision):
    return {"op": "update", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False, "data": data},
            "expectedRevision": expected_revision}
`
	const makeTombstone = `
def make_tombstone(key):
    return {"op": "tombstone", "key": key}
`
	const makeAspectCreate = `
def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False, "data": data}}
`

	// Vector 1 — (e) read + make_aspect_update on the same key: 1 finding.
	f := run(makeAspectUpdate + `
def execute(state, op):
    pkey = op.payload["pkey"]
    # read-posture: (e) relation=demographics epoch=none (test)
    doc = kv.Read(pkey + ".demographics")
    mutations = [make_aspect_update(pkey, "demographics", "demographics", {"x": 1})]
    return {"mutations": mutations}
`)
	check(len(f) == 1 && has(f, "carries no expectedRevision"), "(e) read + bare make_aspect_update on the same key is one finding")

	// Vector 2 — same, pinned via make_aspect_update_occ(..., doc.revision): clean.
	f = run(makeAspectUpdateOCC + `
def execute(state, op):
    pkey = op.payload["pkey"]
    # read-posture: (e) relation=demographics epoch=none (test)
    doc = kv.Read(pkey + ".demographics")
    mutations = [make_aspect_update_occ(pkey, "demographics", "demographics", {"x": 1}, doc.revision)]
    return {"mutations": mutations}
`)
	check(len(f) == 0, "the same shape pinned through make_aspect_update_occ is clean")

	// Vector 3 — (d) read + bare update: clean (hydrated, out of scope).
	f = run(makeAspectUpdate + `
def execute(state, op):
    pkey = op.payload["pkey"]
    # read-posture: (d) declared optionalReads
    doc = kv.Read(pkey + ".demographics")
    mutations = [make_aspect_update(pkey, "demographics", "demographics", {"x": 1})]
    return {"mutations": mutations}
`)
	check(len(f) == 0, "a (d)-annotated read is hydrated and out of scope even with a bare update on its key")

	// Vector 4 — unannotated read + bare update: a finding (class-(b) debt is LIVE).
	f = run(makeAspectUpdate + `
def execute(state, op):
    pkey = op.payload["pkey"]
    doc = kv.Read(pkey + ".demographics")
    mutations = [make_aspect_update(pkey, "demographics", "demographics", {"x": 1})]
    return {"mutations": mutations}
`)
	check(len(f) == 1 && has(f, "(b)"), "an unannotated read is class-(b) LIVE, and a bare update on its key is a finding")

	// Vector 5 — (e) read + inline dict pinned directly: clean.
	f = run(`
def execute(state, op):
    pkey = op.payload["pkey"]
    # read-posture: (e) relation=demographics epoch=none (test)
    doc = kv.Read(pkey + ".demographics")
    mutations = [{"op": "update", "key": pkey + ".demographics",
                  "expectedRevision": doc.revision,
                  "document": {"class": "demographics", "isDeleted": False, "data": {}}}]
    return {"mutations": mutations}
`)
	check(len(f) == 0, "an inline dict carrying expectedRevision directly is clean")

	// Vector 6 — (e) read + inline dict pinned via a later index-assign, used
	// (as an Ident) after the pin: clean.
	f = run(`
def execute(state, op):
    pkey = op.payload["pkey"]
    # read-posture: (e) relation=demographics epoch=none (test)
    doc = kv.Read(pkey + ".demographics")
    m = {"op": "update", "key": pkey + ".demographics",
         "document": {"class": "demographics", "isDeleted": False, "data": {}}}
    m["expectedRevision"] = doc.revision
    mutations = [m]
    return {"mutations": mutations}
`)
	check(len(f) == 0, "an inline dict pinned via a later m[\"expectedRevision\"] = ... assignment, then used, is clean")

	// Vector 7 — (e) read + make_tombstone(key) on the same identifier: a finding.
	f = run(makeTombstone + `
def execute(state, op):
    sess_key = op.payload["sessKey"]
    # read-posture: (e) relation=schedule epoch=none (test)
    doc = kv.Read(sess_key)
    mutations = [make_tombstone(sess_key)]
    return {"mutations": mutations}
`)
	check(len(f) == 1 && has(f, "tombstone"), "a bare make_tombstone on the same identifier the live read named is a finding")

	// Vector 8 — (e) read of k + ".schedule", make_tombstone(k): clean (different key).
	f = run(makeTombstone + `
def execute(state, op):
    k = op.payload["k"]
    # read-posture: (e) relation=schedule epoch=none (test)
    doc = kv.Read(k + ".schedule")
    mutations = [make_tombstone(k)]
    return {"mutations": mutations}
`)
	check(len(f) == 0, "a tombstone on a DIFFERENT key from the one the live read named is clean")

	// Vector 9 — (e) read + make_aspect(...) create: clean (a create is never a finding).
	f = run(makeAspectCreate + `
def execute(state, op):
    pkey = op.payload["pkey"]
    # read-posture: (e) relation=demographics epoch=none (test)
    doc = kv.Read(pkey + ".demographics")
    mutations = [make_aspect(pkey, "demographics", "demographics", {"x": 1})]
    return {"mutations": mutations}
`)
	check(len(f) == 0, "a create on the read's own key is never a finding")

	// Vector 10 — annotation 3 comment lines above the call, no blank line: bound (a (d) there is clean).
	f = run(makeAspectUpdate + `
def execute(state, op):
    pkey = op.payload["pkey"]
    # read-posture: (d) declared optionalReads
    # additional context line one
    # additional context line two
    doc = kv.Read(pkey + ".demographics")
    mutations = [make_aspect_update(pkey, "demographics", "demographics", {"x": 1})]
    return {"mutations": mutations}
`)
	check(len(f) == 0, "an annotation reaching its call across further comment lines, no blank line, still binds")

	// Vector 11 — annotation separated by a blank line: unbound, treated as unannotated, a finding.
	f = run(makeAspectUpdate + `
def execute(state, op):
    pkey = op.payload["pkey"]
    # read-posture: (d) declared optionalReads

    doc = kv.Read(pkey + ".demographics")
    mutations = [make_aspect_update(pkey, "demographics", "demographics", {"x": 1})]
    return {"mutations": mutations}
`)
	check(len(f) == 1, "a blank line between the annotation and the call breaks the bind — treated as unannotated")

	// Vector 12 — `# occ: live-unpinned` on the mutation: clean (exempted).
	f = run(makeAspectUpdate + `
def execute(state, op):
    pkey = op.payload["pkey"]
    # read-posture: (e) relation=demographics epoch=none (test)
    doc = kv.Read(pkey + ".demographics")
    # occ: live-unpinned test-vector deliberately bare
    mutations = [make_aspect_update(pkey, "demographics", "demographics", {"x": 1})]
    return {"mutations": mutations}
`)
	check(len(f) == 0, "a `# occ: live-unpinned <why>` declaration on the mutation exempts it")

	// The same hatch, but separated from the mutation by a
	// blank line: unbound, must NOT exempt.
	f = run(makeAspectUpdate + `
def execute(state, op):
    pkey = op.payload["pkey"]
    # read-posture: (e) relation=demographics epoch=none (test)
    doc = kv.Read(pkey + ".demographics")
    # occ: live-unpinned test-vector deliberately bare

    mutations = [make_aspect_update(pkey, "demographics", "demographics", {"x": 1})]
    return {"mutations": mutations}
`)
	check(len(f) == 1, "an `# occ: live-unpinned` hatch separated from the mutation by a blank line does not exempt it")

	// Vector 13 — `doc = any_data(kv.Read(k))` wrapper: still recognised, a finding with a bare update.
	f = run(makeTombstone + `
def execute(state, op):
    k = op.payload["k"]
    # read-posture: (e) relation=schedule epoch=none (test)
    doc = any_data(kv.Read(k))
    mutations = [make_tombstone(k)]
    return {"mutations": mutations}
`)
	check(len(f) == 1, "a kv.Read wrapped in one call (any_data(kv.Read(k))) is still recognised as the live read")

	// Vector 14 — (c) read + bare update: a finding (config reads are LIVE too).
	f = run(makeAspectUpdate + `
def execute(state, op):
    pkey = op.payload["pkey"]
    # read-posture: (c) config read, deliberately live
    doc = kv.Read(pkey + ".demographics")
    mutations = [make_aspect_update(pkey, "demographics", "demographics", {"x": 1})]
    return {"mutations": mutations}
`)
	check(len(f) == 1 && has(f, "(c)"), "a class-(c) config read followed by a bare update is a finding")

	// Vector 15 — a read in a different top-level function, expressed on a
	// key OTHER than that function's own parameters (so it does not qualify
	// as a read helper — read_it here takes no parameters at all, and its
	// key references a name it never binds): clean (out of scope).
	f = run(makeAspectUpdate + `
def read_it():
    # read-posture: (e) relation=demographics epoch=none (test)
    return kv.Read(pkey + ".demographics")

def execute(state, op):
    pkey = op.payload["pkey"]
    doc = read_it()
    mutations = [make_aspect_update(pkey, "demographics", "demographics", {"x": 1})]
    return {"mutations": mutations}
`)
	check(len(f) == 0, "a read in a different top-level function, on a key not expressible in terms of THAT function's own parameters, is out of scope (not a read helper)")

	// The same shape, but read_it DOES
	// take the key as its own parameter: now a genuine read-helper hop, and
	// the bare update is a finding.
	f = run(makeAspectUpdate + `
def read_it(pkey):
    # read-posture: (e) relation=demographics epoch=none (test)
    return kv.Read(pkey + ".demographics")

def execute(state, op):
    pkey = op.payload["pkey"]
    doc = read_it(pkey)
    mutations = [make_aspect_update(pkey, "demographics", "demographics", {"x": 1})]
    return {"mutations": mutations}
`)
	check(len(f) == 1, "a read-helper resolvable from its own parameters, called from a different top-level function, still reaches a bare update there")

	// Mutation-proof pair.
	fixed := run(makeAspectUpdateOCC + `
def execute(state, op):
    pkey = op.payload["pkey"]
    # read-posture: (e) relation=demographics epoch=none (test)
    doc = kv.Read(pkey + ".demographics")
    mutations = [make_aspect_update_occ(pkey, "demographics", "demographics", {"x": 1}, doc.revision)]
    return {"mutations": mutations}
`)
	check(len(fixed) == 0, "mutation-proof baseline: the pinned call is clean")
	mutated := run(makeAspectUpdate + `
def execute(state, op):
    pkey = op.payload["pkey"]
    # read-posture: (e) relation=demographics epoch=none (test)
    doc = kv.Read(pkey + ".demographics")
    mutations = [make_aspect_update(pkey, "demographics", "demographics", {"x": 1})]
    return {"mutations": mutations}
`)
	check(len(mutated) == 1, "mutation-proof: reverting to the bare helper on the identical read makes the gate fire again")

	// --- control-flow rule vectors ---

	// A read in an `if` arm whose `else` fail()s; bare tombstone after: 1.
	f = run(makeTombstone + `
def execute(state, op):
    k = op.payload["k"]
    cond = op.payload["cond"]
    if cond:
        # read-posture: (e) relation=x epoch=none (test)
        doc = kv.Read(k)
    else:
        fail("InvalidState: cond must be true")
    mutations = [make_tombstone(k)]
    return {"mutations": mutations}
`)
	check(len(f) == 1, "a read in an if-arm whose else fail()s reaches the bare tombstone after the if")

	// A read + conditional continue under a NESTED if inside a loop;
	// bare tombstone after (same loop iteration): 1.
	f = run(makeTombstone + `
def execute(state, op):
    k = op.payload["k"]
    items = op.payload["items"]
    mutations = []
    for c in items:
        if c.want:
            # read-posture: (e) relation=x epoch=none (test)
            doc = kv.Read(k)
            if doc == None:
                continue
        mutations.append(make_tombstone(k))
    return {"mutations": mutations}
`)
	check(len(f) == 1, "a read guarded by a nested conditional continue still reaches a sibling bare tombstone in the same iteration")

	// The identity-hygiene shape: read + unconditional continue in
	// one arm, bare update on the fallthrough: 0.
	f = run(`
def make_tombstone_occ(key, expected_revision):
    return {"op": "tombstone", "key": key, "expectedRevision": expected_revision}
def make_update(key, data):
    return {"op": "update", "key": key, "document": {"class": "x", "isDeleted": False, "data": data}}
def execute(state, op):
    cred_set = op.payload["credSet"]
    primary_id = op.payload["primaryId"]
    mutations = []
    for c in cred_set:
        cred_id = c["credId"]
        idx_key = "vtx.credentialindex." + c["hash"]
        if cred_id == primary_id:
            # read-posture: (e) relation=x epoch=none (test)
            idx_vtx = kv.Read(idx_key)
            if idx_vtx == None:
                continue
            mutations.append(make_tombstone_occ(idx_key, idx_vtx.revision))
            continue
        mutations.append(make_update(idx_key, {"a": 1}))
    return {"mutations": mutations}
`)
	check(len(f) == 0, "a read inside a branch that always continues never reaches the sibling fallthrough update (identity-hygiene shape)")

	// A bare update textually BEFORE the read: 0.
	f = run(makeTombstone + `
def execute(state, op):
    k = op.payload["k"]
    mutations = [make_tombstone(k)]
    # read-posture: (e) relation=x epoch=none (test)
    doc = kv.Read(k)
    return {"mutations": mutations}
`)
	check(len(f) == 0, "a bare mutation textually before the live read that would inform it is never matched")

	// --- read-helper vectors ---

	const vertexLiveHelper = `
def vertex_live(key):
    if key == None:
        return False
    # read-posture: (e) one bounded read per candidate (test)
    node = kv.Read(key)
    return node != None and not node.isDeleted
`
	f = run(vertexLiveHelper + makeTombstone + `
def execute(state, op):
    sess_key = op.payload["sessKey"]
    if not vertex_live(sess_key):
        pass
    mutations = [make_tombstone(sess_key)]
    return {"mutations": mutations}
`)
	check(len(f) == 1, "a read-helper call (vertex_live) followed by a bare tombstone on the same key is a finding")

	f = run(vertexLiveHelper + `
def make_tombstone_occ(key, expected_revision):
    return {"op": "tombstone", "key": key, "expectedRevision": expected_revision}
def execute(state, op):
    sess_key = op.payload["sessKey"]
    if not vertex_live(sess_key):
        pass
    mutations = [make_tombstone_occ(sess_key, 7)]
    return {"mutations": mutations}
`)
	check(len(f) == 0, "a read-helper call followed by a pinned tombstone on the same key is clean")

	// --- reads recognised outside `ident = kv.Read(...)` position ---

	f = run(makeTombstone + `
def execute(state, op):
    k = op.payload["k"]
    # read-posture: (e) relation=x epoch=none (test)
    if kv.Read(k) != None:
        pass
    mutations = [make_tombstone(k)]
    return {"mutations": mutations}
`)
	check(len(f) == 1, "a kv.Read used directly in an `if` condition (no assignment) is still recognised as a live read")

	// --- for-loop propagation past the loop ---

	f = run(makeTombstone + `
def execute(state, op):
    k = op.payload["k"]
    cands = op.payload["cands"]
    found = False
    for c in cands:
        # read-posture: (e) relation=x epoch=none (test)
        doc = kv.Read(k)
        if doc != None:
            found = True
    mutations = []
    if found:
        mutations = [make_tombstone(k)]
    return {"mutations": mutations}
`)
	check(len(f) == 1, "a for-body read whose key does not depend on any body-reassigned name reaches code after the loop")

	// vec/s2leak.star — a body-local key (idx_key, built from the loop's own
	// candidate) referenced directly OUTSIDE the loop must NOT match the
	// in-loop read: keyReferencesAny has to see that idx_key's underlying
	// c["hash"] mentions the loop variable `c`, not string-compare the whole
	// rendered leaf against the bare name "c".
	f = run(makeTombstone + `
def execute(state, op):
    cands = op.payload["cands"]
    for c in cands:
        idx_key = "vtx.credentialindex." + c["hash"]
        # read-posture: (e) relation=x epoch=none (test)
        doc = kv.Read(idx_key)
    mutations = [make_tombstone("vtx.credentialindex." + c["hash"])]
    return {"mutations": mutations}
`)
	check(len(f) == 0, "a for-body read on a key built from the loop variable does not reach an outside reference to that same loop variable (vec/s2leak.star)")

	// vec/s2leak2.star — the same shape through a `.attribute` access
	// (lk.sourceVertex) instead of an index expression.
	f = run(makeTombstone + `
def execute(state, op):
    links = op.payload["links"]
    for lk in links:
        # read-posture: (e) relation=x epoch=none (test)
        st = kv.Read(lk.sourceVertex + ".status")
    mutations = [make_tombstone(lk.sourceVertex + ".status")]
    return {"mutations": mutations}
`)
	check(len(f) == 0, "a for-body read on a key built via .attribute access on the loop variable does not reach an outside reference to it (vec/s2leak2.star)")

	// --- dict-pin bound to the nearest preceding assignment, per branch ---

	f = run(`
def execute(state, op):
    k = op.payload["k"]
    branch_a = op.payload["branchA"]
    # read-posture: (e) relation=x epoch=none (test)
    doc = kv.Read(k)
    if branch_a:
        m = {"op": "update", "key": k, "document": {"class": "x", "isDeleted": False, "data": {}}}
        m["expectedRevision"] = doc.revision
    else:
        m = {"op": "tombstone", "key": k}
    mutations = [m]
    return {"mutations": mutations}
`)
	check(len(f) == 1 && has(f, "tombstone"), "a pin applied in one branch does not leak to a differently-shaped dict bound to the same name in a sibling branch")

	// --- helper classification ---

	// A conditionally-pinned helper (`if rev != None: m[...] = rev` with a
	// `rev=None` default): called without the arg, called with an explicit
	// `None`, and called with a real value.
	const condPinHelper = `
def make_x_maybe_occ(key, rev=None):
    m = {"op": "update", "key": key, "document": {"class": "x", "isDeleted": False, "data": {}}}
    if rev != None:
        m["expectedRevision"] = rev
    return m
`
	f = run(condPinHelper + `
def execute(state, op):
    k = op.payload["k"]
    # read-posture: (e) relation=x epoch=none (test)
    doc = kv.Read(k)
    mutations = [make_x_maybe_occ(k)]
    return {"mutations": mutations}
`)
	check(len(f) == 1, "a conditionally-pinned helper called WITHOUT its revision argument (relying on the None default) is treated as BARE — a finding, not given the benefit of the doubt")

	f = run(condPinHelper + `
def execute(state, op):
    k = op.payload["k"]
    # read-posture: (e) relation=x epoch=none (test)
    doc = kv.Read(k)
    mutations = [make_x_maybe_occ(k, None)]
    return {"mutations": mutations}
`)
	check(len(f) == 1, "a conditionally-pinned helper called with an EXPLICIT None revision is treated as bare — a finding")

	f = run(condPinHelper + `
def execute(state, op):
    k = op.payload["k"]
    # read-posture: (e) relation=x epoch=none (test)
    doc = kv.Read(k)
    mutations = [make_x_maybe_occ(k, doc.revision)]
    return {"mutations": mutations}
`)
	check(len(f) == 0, "a conditionally-pinned helper called with a real revision value is pinned — clean")

	// vec/h2.star's direct-inline shape: `"expectedRevision": rev` written
	// straight into the dict literal, where rev is itself a None-defaulted
	// parameter — classifyDictLiteral must resolve this the same way, not
	// treat the field's mere PRESENCE as pinned.
	f = run(`
def make_y(key, rev=None):
    return {"op": "update", "key": key, "expectedRevision": rev, "document": {}}
def execute(state, op):
    k = op.payload["k"]
    # read-posture: (e) relation=x epoch=none (test)
    doc = kv.Read(k)
    mutations = [make_y(k)]
    return {"mutations": mutations}
`)
	check(len(f) == 1, "an inline `\"expectedRevision\": rev` field, where rev is a None-defaulted parameter the call omits, is BARE (vec/h2.star)")

	// A mutation helper with no `make_` naming convention is still classified.
	f = run(`
def stale_indexes_tombstone(key):
    return {"op": "tombstone", "key": key}
def execute(state, op):
    k = op.payload["k"]
    # read-posture: (e) relation=x epoch=none (test)
    doc = kv.Read(k)
    mutations = [stale_indexes_tombstone(k)]
    return {"mutations": mutations}
`)
	check(len(f) == 1, "a mutation helper with no make_ prefix (stale_indexes_tombstone) is still classified and checked")

	// A nested def containing the live read is classified as its own unit,
	// independent of its enclosing def.
	f = run(`
def execute(state, op):
    k = op.payload["k"]
    def make_tombstone_nested(key):
        return {"op": "tombstone", "key": key}
    def read_and_check():
        # read-posture: (e) relation=x epoch=none (test)
        return kv.Read(k)
    doc = read_and_check()
    mutations = [make_tombstone_nested(k)]
    return {"mutations": mutations}
`)
	check(len(f) == 0, "a nested def is classified as its own independent unit — its internal read does not leak into its enclosing def")

	// A name reused by two defs is refused as a finding in its own right.
	f = run(`
def make_tombstone(key):
    return {"op": "tombstone", "key": key}
def make_tombstone(key):
    return {"op": "tombstone", "key": key + ".dup"}
def execute(state, op):
    return {"mutations": []}
`)
	check(len(f) == 1 && has(f, "declared more than once"), "a name reused by two defs is reported as a finding, not silently shadowed in the lookup")

	// --- key-equality through plain-alias bindings ---

	f = run(makeAspectUpdate + `
def execute(state, op):
    pkey = op.payload["pkey"]
    dkey = pkey + ".demographics"
    # read-posture: (e) relation=x epoch=none (test)
    doc = kv.Read(dkey)
    mutations = [make_aspect_update(pkey, "demographics", "demographics", {"x": 1})]
    return {"mutations": mutations}
`)
	check(len(f) == 1, "a read through a plain key alias (dkey = pkey + \".demographics\") matches the equivalent concatenation at the mutation site")

	f = run(makeAspectUpdate + `
def execute(state, op):
    pkey = op.payload["pkey"]
    alias = "demographics"
    # read-posture: (e) relation=x epoch=none (test)
    doc = kv.Read(pkey + ".demographics")
    mutations = [make_aspect_update(pkey, alias, "demographics", {"x": 1})]
    return {"mutations": mutations}
`)
	check(len(f) == 1, "a direct-concatenation read matches a mutation whose helper argument is itself an alias (reverse direction)")

	f = run(makeAspectUpdate + `
def execute(state, op):
    pkey = op.payload["pkey"]
    # read-posture: (e) relation=x epoch=none (test)
    doc = kv.Read("%s.demographics" % pkey)
    mutations = [make_aspect_update(pkey, "demographics", "demographics", {"x": 1})]
    return {"mutations": mutations}
`)
	check(len(f) == 1, "a single-%s format-string key (\"%s.demographics\" % pkey) matches the equivalent concatenation")

	f = run(makeAspectUpdate + `
def execute(state, op):
    pkey = op.payload["pkey"]
    asp = "demographics"
    # read-posture: (e) relation=x epoch=none (test)
    doc = kv.Read(pkey + "." + asp)
    mutations = [make_aspect_update(pkey, "demographics", "demographics", {"x": 1})]
    return {"mutations": mutations}
`)
	check(len(f) == 1, "a read key built from a local alias for one segment (pkey + \".\" + asp) matches the equivalent literal concatenation")

	f = run(makeTombstone + `
def execute(state, op):
    a = op.payload["a"]
    b = op.payload["b"]
    k = a
    # read-posture: (e) relation=x epoch=none (test)
    doc = kv.Read(k)
    k = b
    mutations = [make_tombstone(k)]
    return {"mutations": mutations}
`)
	check(len(f) == 0, "reassigning the aliased name between the read and the mutation means they no longer name the same key")

	// vec/prisec.star — identity-hygiene's own primary/secondary shape: two
	// DIFFERENT CondExpr-bound aliases must render as different keys, not
	// collide into the same opaque "<*syntax.CondExpr>" placeholder.
	f = run(`
def execute(state, op):
    p = op.payload
    primary = p.primary if hasattr(p, "primary") else None
    secondary = p.secondary if hasattr(p, "secondary") else None
    # read-posture: (e) x
    doc = kv.Read(secondary + ".state")
    mutations = [{"op": "update", "key": primary + ".state", "document": {}}]
    return {"mutations": mutations}
`)
	check(len(f) == 0, "two different CondExpr-bound aliases (primary/secondary) render as different keys, not a false match (vec/prisec.star)")

	// vec/condexpr.star — the same structural-rendering requirement for a
	// CondExpr with swapped operands, and for two SliceExpr with different
	// bounds.
	f = run(makeTombstone + `
def execute(state, op):
    a = op.payload["a"]
    b = op.payload["b"]
    c = op.payload["c"]
    # read-posture: (e) x
    doc = kv.Read((a if c else b) + ".x")
    mutations = [make_tombstone((b if c else a) + ".x")]
    k1 = op.payload["k1"]
    k2 = op.payload["k2"]
    # read-posture: (e) x
    doc2 = kv.Read(k1[4:] + ".y")
    mutations.append(make_tombstone(k2[2:] + ".y"))
    return {"mutations": mutations}
`)
	check(len(f) == 0, "a CondExpr with swapped operands, and two SliceExpr with different bounds, each render distinctly rather than colliding (vec/condexpr.star)")

	// --- branch-binding / loop-carry disagreement errs to a finding ---

	// vec/a5.star — a name reassigned on only ONE arm of an if: the mutation
	// after the if is checked against `before`'s own (pre-if) binding rather
	// than an unresolved slot that could never match anything.
	f = run(makeTombstone + `
def make_tombstone_occ(key, rev):
    return {"op": "tombstone", "key": key, "expectedRevision": rev}
def execute(state, op):
    k = op.payload["k"]
    # read-posture: (e) x
    doc = kv.Read(k)
    if op.payload["a"]:
        k = op.payload["k2"]
    mutations = [make_tombstone(k)]
    return {"mutations": mutations}
`)
	check(len(f) == 1, "a name reassigned on only one arm of an if is still matched against its pre-if binding on the arm that leaves it unchanged (vec/a5.star)")

	// vec/carried.star — a name that existed BEFORE a loop, reassigned inside
	// it to the exact value a read just took, escapes the loop along with
	// that read (the "carry" exception) — matched by the same erring-toward-
	// finding bias as a5.star.
	f = run(makeTombstone + `
def execute(state, op):
    target = None
    for c in op.payload["items"]:
        k = c["k"]
        # read-posture: (e) x
        doc = kv.Read(k)
        if doc == None:
            continue
        target = k
    mutations = []
    if target != None:
        mutations.append(make_tombstone(target))
    return {"mutations": mutations}
`)
	check(len(f) == 1, "a name carried out of a loop (reassigned inside it to the value a read just took) escapes along with that read (vec/carried.star)")

	// --- escape hatch must anchor to a simple statement ---

	// vec/hatchdef.star — a hatch anchored to a `def` header grants no
	// exemption (both bare tombstones inside still fire) and is reported as
	// a finding of its own.
	f = run(makeTombstone + `
# occ: live-unpinned whole function waved through
def execute(state, op):
    k = op.payload["k"]
    j = op.payload["j"]
    # read-posture: (e) x
    doc = kv.Read(k)
    # read-posture: (e) x
    doc2 = kv.Read(j)
    mutations = [make_tombstone(k), make_tombstone(j)]
    return {"mutations": mutations}
`)
	check(len(f) == 3 && has(f, "not a simple statement") && has(f, "kv.Read(k)") && has(f, "kv.Read(j)"),
		"a hatch anchored to a def header is refused (a finding of its own) and grants no exemption to either tombstone (vec/hatchdef.star)")

	// A hatch anchored to a plain statement still works, including one above
	// an aliased dict's OWN assignment — its indentation-block reach covers
	// every later use of that alias, exactly as it covers a nested `if`.
	f = run(`
def execute(state, op):
    k = op.payload["k"]
    # read-posture: (e) x
    doc = kv.Read(k)
    # occ: live-unpinned deliberately bare, aliased
    m = {"op": "tombstone", "key": k}
    mutations = [m]
    return {"mutations": mutations}
`)
	check(len(f) == 0, "a hatch above an aliased dict's own assignment exempts every later use of that alias")

	// --- an annotation's class comes ONLY from its own statement's anchor ---

	// vec/inherit.star — a hydrated (d) annotation above an `if` does not
	// hydrate a DIFFERENT read nested inside that if's block; that read is
	// unannotated (class-(b), LIVE) on its own.
	f = run(makeTombstone + `
def execute(state, op):
    a = op.payload["a"]
    b = op.payload["b"]
    mutations = []
    # read-posture: (d) declared optionalReads at dispatch
    if kv.Read(a) == None:
        doc = kv.Read(b)
        mutations.append(make_tombstone(b))
    return {"mutations": mutations}
`)
	check(len(f) == 1, "a hydrated annotation above an if does not hydrate a different read nested inside its block (vec/inherit.star)")

	// --- duplicate findings are deduplicated ---

	// vec/use_before_pin.star — `m` is referenced both in a membership test
	// (an incidental use) and in `mutations = [m]`; the identical hazard
	// must be reported once, not once per reference.
	f = run(`
def execute(state, op):
    k = op.payload["k"]
    # read-posture: (e) x
    doc = kv.Read(k)
    m = {"op": "update", "key": k, "document": {}}
    if "expectedRevision" not in m:
        m["expectedRevision"] = doc.revision
    mutations = [m]
    return {"mutations": mutations}
`)
	check(len(f) == 1, "the identical finding reached through more than one reference to the same dict-pinned name is reported once, not once per reference (vec/use_before_pin.star)")

	// --- item 1: kv.Links page-entry keys ---

	// A direct `for lk in kv.Links(...):` loop: `lk.key` is a live key on par
	// with a read, so a bare tombstone on it in the same iteration is a
	// finding.
	f = run(makeTombstone + `
def execute(state, op):
    links, cursor = kv.Links(op.payload["hub"], "boundTo", "in", None, 50)
    mutations = []
    for lk in links:
        mutations.append(make_tombstone(lk.key))
    return {"mutations": mutations}
`)
	check(len(f) == 1, "a bare tombstone on a direct kv.Links(...) page entry's own .key is a finding")

	// The same shape, pinned with the entry's own .revision: clean — and
	// proves the corpus's own idiom (identity-domain/ddls.go:2051).
	f = run(`
def execute(state, op):
    links, cursor = kv.Links(op.payload["hub"], "boundTo", "in", None, 50)
    mutations = []
    for lk in links:
        mutations.append({"op": "update", "key": lk.key, "expectedRevision": lk.revision, "document": {}})
    return {"mutations": mutations}
`)
	check(len(f) == 0, "a page entry's key pinned with its own .revision (identity-domain/ddls.go:2051's idiom) is clean")

	// A name assigned FROM the page entry's key (old_link_key = lk.key),
	// then bare-updated: still a finding — the alias resolves the same way
	// any other plain alias does.
	f = run(`
def execute(state, op):
    links, cursor = kv.Links(op.payload["hub"], "boundTo", "in", None, 50)
    mutations = []
    for lk in links:
        old_link_key = lk.key
        mutations.append({"op": "update", "key": old_link_key, "document": {"isDeleted": True}})
    return {"mutations": mutations}
`)
	check(len(f) == 1, "a name assigned from a page entry's .key (old_link_key = lk.key), then bare-updated, is a finding")

	// A page threaded through one helper hop (a def returning kv.Links(...))
	// is recognized the same way.
	f = run(makeTombstone + `
def fetch_links(hub):
    links, cursor = kv.Links(hub, "boundTo", "in", None, 50)
    return links
def execute(state, op):
    mutations = []
    for lk in fetch_links(op.payload["hub"]):
        mutations.append(make_tombstone(lk.key))
    return {"mutations": mutations}
`)
	check(len(f) == 1, "a kv.Links(...) page threaded through one helper hop is still recognized as page-entry-shaped")

	if !pass {
		fmt.Fprintln(os.Stderr, "lint-live-read-pinned-mutation: self-test FAILED — the gate does not prove its own vectors; fix the gate before trusting any corpus verdict")
		os.Exit(2)
	}
	if verbose {
		fmt.Println("selftest: all vectors passed")
	}
}
