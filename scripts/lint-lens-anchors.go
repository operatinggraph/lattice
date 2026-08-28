//go:build ignore

// lint-lens-anchors — structural guard for the read-grant / lens
// dual-enumeration seam (the "footgun"). A non-self-anchored Personal
// (nats-subject) Lens projects a row keyed on some vertex OTHER than the
// recipient identity, and Refractor's D1 readableAnchors gate
// (internal/refractor/projection/personal.go → capabilityread.IsReadable)
// SILENTLY drops that row unless the actor's cap-read.* slices grant the
// anchor's bare NanoID. Nothing at runtime reports the drop.
//
// The walk is DECLARED once, as `pkgmgr.AnchorWalk`, and pkgmgr compiles both
// artifacts from it (internal/pkgmgr/anchorwalk.go): the lens's own reachability
// prefix and the whole read-grant producer. So the invariant this gate enforces
// is the declaration itself:
//
//   - Every Personal lens either anchors on the ACTOR (its anchor variable
//     bound `{key: $actorKey}` — covered by the platform base cap-read
//     self-grant) or declares `Walks`.
//   - A package that ships Personal lenses does not ALSO hand-author a Path-B
//     cap-read producer (`ProjectionKind: "actorAggregate"` writing a
//     `cap-read.*` key). Those are generated per declared ReadGrantDomain; a
//     hand-authored one beside its own lenses restates every walk it grants,
//     which is the footgun itself. A cap-read slice with no lens counterpart
//     (the kernel's own self-anchor base is this shape) stays legal.
//
// Path A producers (`GrantTable: true`, Postgres `actor_read_grants`) are a
// different mechanism — a row→anchor comprehension meeting actor→anchor grants
// at the anchor vocabulary, not one walk duplicated — and are untouched here.
//
// pkgmgr's expansion pass rejects the missing-Walks violation at package-build
// time too (and a registry-wide test compiles every shipped package), but only
// this gate catches BOTH across every package in CI without an install — which
// is what binds the NEXT author.
//
// A second, independent invariant lives in this same gate: a variable-length
// relationship hop that carries a FINITE upper bound is refused wherever it
// sits inside a negated pattern (`NOT (...)`, including `WHERE NOT (...)` /
// `AND NOT (...)`). Polarity flips the direction a shallow bound is unsound in:
// on a positive pattern a too-shallow bound is fail-CLOSED (it drops a
// service), but inside a negation the same edit is fail-OPEN (it drops an
// EXCLUSION, which grants access). An open range (`*0..`, `*1..`) inside a
// negated pattern is fine and is never flagged — the executor's own clamp
// (`maxVarLengthHops`, internal/refractor/ruleengine/full/executor.go) already
// bounds it, which is what makes the open form sound; a finite bound at or
// above that same clamp is equivalent to open and is likewise left alone. A
// ranged hop inside a POSITIVE pattern is never flagged either, regardless of
// its bound — the asymmetry is the point.
//
// Runs per package under packages/. STRICT=1 exits non-zero on any issue.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type lens struct {
	name           string // CanonicalName
	adapter        string
	personal       bool
	hasWalks       bool
	projectionKind string
	hasOutput      bool
	outputKey      string // Output.OutputKeyPattern, "" when not statically resolvable
	grantTable     bool
	spec           string // resolved cypher (the presentation tail only, with Walks)
	pos            token.Position
}

var reAnchor = regexp.MustCompile(`(\w+)\.key\s+AS\s+anchor\b`)

// isSelfAnchored reports whether v is bound with `{key: $actorKey}` — the actor
// itself, covered by the base self-grant.
func isSelfAnchored(cypher, v string) bool {
	re := regexp.MustCompile(`\(\s*` + regexp.QuoteMeta(v) + `\s*:\s*[A-Za-z0-9_]+\s*\{[^}]*key\s*:\s*\$actorKey`)
	return re.MatchString(cypher)
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

	// The negated-range-bound rule is preventive: the shipped corpus contains no
	// violating lens, so a clean run over packages/ proves nothing on its own and
	// a rule that silently stopped matching would read as green forever. The
	// vectors therefore run on EVERY invocation, so CI executes the positive one
	// too — the shape lint-conventions.go's own main() takes.
	runSelfTest(false)

	dirs, _ := filepath.Glob("packages/*")
	issues, warns := 0, 0
	for _, dir := range dirs {
		fi, err := os.Stat(dir)
		if err != nil || !fi.IsDir() {
			continue
		}
		i, w := checkPackage(dir)
		issues += i
		warns += w
	}

	if issues == 0 && warns == 0 {
		fmt.Println("lint-lens-anchors: 0 issues — every non-self-anchored Personal lens declares its read-grant Walks, and no negated pattern narrows a variable-length hop below the engine's clamp")
		return
	}
	fmt.Printf("lint-lens-anchors: %d issue(s), %d advisory warning(s)\n", issues, warns)
	if strict && issues > 0 {
		os.Exit(1)
	}
}

// checkPackage parses one package directory and enforces both invariants over
// its lens declarations.
func checkPackage(dir string) (issues, warns int) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		// A package that does not parse is another gate's problem, not ours.
		return 0, 0
	}

	consts := map[string]string{}
	var lenses []lens
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			collectConsts(f, consts)
		}
	}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			lenses = append(lenses, collectLenses(fset, f, consts)...)
		}
	}

	// A hand-authored Path-B producer is only a DUPLICATED walk if this package
	// also ships Personal lenses whose walks it would be restating. A package
	// with none has no walk to duplicate — the carve-out for a cap-read slice
	// with no lens counterpart (the kernel's own self-anchor base is this shape)
	// stays legal.
	shipsPersonalLens := false
	for _, l := range lenses {
		if l.adapter == "nats-subject" && l.personal {
			shipsPersonalLens = true
			break
		}
	}

	for _, l := range lenses {
		if shipsPersonalLens && l.projectionKind == "actorAggregate" && !l.grantTable && l.hasOutput {
			switch {
			case strings.HasPrefix(l.outputKey, "cap-read."):
				fmt.Printf("%s: lens %s hand-authors a Path-B cap-read producer (%s) beside this package's own Personal lenses — declare a pkgmgr.ReadGrantDomain and let their Walks compile it; a hand-authored producer restates every walk it grants, which is the dual-enumeration footgun\n",
					posOf(l), l.name, l.outputKey)
				issues++
				continue
			case l.outputKey == "":
				// Fail closed: a pattern this gate cannot read statically may
				// well be a cap-read key.
				fmt.Printf("%s: lens %s is an actorAggregate producer whose OutputKeyPattern is neither a string literal nor a string const, so this gate cannot tell whether it writes a cap-read.* slice — spell it as one so the Path-B rule stays checkable\n",
					posOf(l), l.name)
				issues++
				continue
			}
		}

		if l.adapter != "nats-subject" || !l.personal {
			continue
		}
		if l.hasWalks {
			continue // non-self by construction, and its producer is compiled
		}
		m := reAnchor.FindStringSubmatch(l.spec)
		if m == nil {
			fmt.Printf("%s: warn: Personal lens %s declares no Walks and has no `<var>.key AS anchor` — cannot verify its read-grant coverage\n", posOf(l), l.name)
			warns++
			continue
		}
		if isSelfAnchored(l.spec, m[1]) {
			continue // self-anchored — base cap-read self-grant covers it
		}
		fmt.Printf("%s: Personal lens %s anchors on `%s` (not the actor) but declares no Walks — pkgmgr cannot compile its read-grant producer, so Refractor's D1 gate would silently drop every row it projects (the 'forgot the slice' dual-enumeration bug). Declare Walks with the actor→anchor chain and a GrantDomain.\n",
			posOf(l), l.name, m[1])
		issues++
	}

	for _, l := range lenses {
		i, w := checkNegatedRangeBound(l)
		issues += i
		warns += w
	}
	return issues, warns
}

// maxVarLengthHops mirrors internal/refractor/ruleengine/full/executor.go's
// clamp of the same name: the ceiling the executor imposes on every
// variable-length traversal when the pattern's own upper bound is open or
// wider. A finite bound at or above it behaves identically to an open range,
// so it is not a narrowing and is not flagged.
const maxVarLengthHops = 10

// checkNegatedRangeBound refuses a variable-length relationship hop that
// carries a finite upper bound narrower than maxVarLengthHops when that hop
// sits inside a negated pattern (`NOT (...)`, `WHERE NOT (...)`,
// `AND NOT (...)`). See the polarity note in this file's package doc: the
// same edit is fail-closed on a positive pattern and fail-open on a negated
// one, so only the negated occurrence is a violation.
func checkNegatedRangeBound(l lens) (issues, warns int) {
	if l.spec == "" {
		return 0, 0
	}
	extents, unparsed := negatedExtents(l.spec)
	if unparsed {
		fmt.Printf("%s: warn: lens %s has a NOT (...) pattern this gate could not fully bound (unbalanced parens or relationship brackets) — check it by hand for a narrowing range bound inside the negation\n",
			posOf(l), l.name)
		warns++
	}
	for _, h := range rangedHops(l.spec) {
		if !h.finite || h.max >= maxVarLengthHops {
			continue // open, or already at-or-past the executor's own clamp
		}
		inNegated := false
		for _, ext := range extents {
			if h.start >= ext[0] && h.start < ext[1] {
				inNegated = true
				break
			}
		}
		if !inNegated {
			continue // positive pattern — the narrowing is fail-closed, not a violation
		}
		fmt.Printf("%s: lens %s narrows a variable-length hop %s to a finite bound inside a NOT (...) pattern — inside a negation a too-shallow bound drops an EXCLUSION, which GRANTS access (fail-open), the opposite of a positive pattern's fail-closed direction. Use the open form (e.g. `*%d..`) and let the executor's own clamp (maxVarLengthHops=%d) bound it.\n",
			posOf(l), l.name, h.text, minOf(h.text), maxVarLengthHops)
		issues++
	}
	return issues, warns
}

// minOf pulls the lower bound back out of a hop's raw bracket text (e.g.
// "[:containedIn*0..3]" -> 0) so the violation message can point at the exact
// open form the author should switch to, rather than a generic example.
func minOf(hopText string) int {
	m := reRangeSpec.FindStringSubmatch(hopText)
	if m == nil || m[1] == "" {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

var (
	reNotKeyword = regexp.MustCompile(`\bNOT\b`)
	reBracket    = regexp.MustCompile(`\[[^\[\]]*\]`)
	reRangeSpec  = regexp.MustCompile(`\*(\d*)(\.\.)?(\d*)`)
)

// isCypherSpace reports whether b is Cypher-insignificant whitespace.
func isCypherSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// parenGroup matches a balanced `(...)` starting at s[i], returning the index
// just past its closing paren. ok is false when s[i] is not '(' or the parens
// never balance.
func parenGroup(s string, i int) (end int, ok bool) {
	if i >= len(s) || s[i] != '(' {
		return i, false
	}
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 {
			return j + 1, true
		}
	}
	return i, false
}

// relArrowEnd matches a relationship-pattern continuation starting at s[i] —
// `-[...]->`, `<-[...]-`, `-[...]-`, `-->`, `<--` or `--` — returning the
// index just past it. Cypher's arrow always carries a dash on BOTH sides of
// the optional `[...]` (`-[...]->`, not `-[...]>`); an optional leading `<`
// and trailing `>` pick the direction, and both together is not a legal
// direction. ok is false when s[i] does not begin an arrow, or an embedded
// `[...]` never balances.
func relArrowEnd(s string, i int) (end int, ok bool) {
	j := i
	leftArrow := false
	if j < len(s) && s[j] == '<' {
		leftArrow = true
		j++
	}
	if j >= len(s) || s[j] != '-' {
		return i, false
	}
	j++
	if j < len(s) && s[j] == '[' {
		depth := 1
		j++
		for j < len(s) && depth > 0 {
			switch s[j] {
			case '[':
				depth++
			case ']':
				depth--
			}
			j++
		}
		if depth != 0 {
			return i, false
		}
	}
	if j >= len(s) || s[j] != '-' {
		return i, false
	}
	j++
	rightArrow := false
	if j < len(s) && s[j] == '>' {
		rightArrow = true
		j++
	}
	if leftArrow && rightArrow {
		return i, false
	}
	return j, true
}

// negatedExtents finds every `NOT (...)` pattern predicate in spec — a `NOT`
// keyword immediately (module whitespace) followed by a chain of one or more
// `(node)` groups linked by relationship arrows — and returns each one's byte
// range. A `NOT` not followed by `(` (e.g. a plain boolean negation) is not a
// pattern predicate and is skipped, not reported. unparsed reports whether
// some `NOT (` was found but its extent could not be bounded (unbalanced
// parens/brackets) — a shape genuinely beyond this scanner, surfaced as an
// advisory warn by the caller rather than silently dropped.
func negatedExtents(spec string) (extents [][2]int, unparsed bool) {
	for _, loc := range reNotKeyword.FindAllStringIndex(spec, -1) {
		i := loc[1]
		for i < len(spec) && isCypherSpace(spec[i]) {
			i++
		}
		if i >= len(spec) || spec[i] != '(' {
			continue
		}
		start := i
		end, ok := parenGroup(spec, i)
		if !ok {
			unparsed = true
			continue
		}
		for {
			k := end
			for k < len(spec) && isCypherSpace(spec[k]) {
				k++
			}
			nk, ok := relArrowEnd(spec, k)
			if !ok {
				break
			}
			for nk < len(spec) && isCypherSpace(spec[nk]) {
				nk++
			}
			ng, ok := parenGroup(spec, nk)
			if !ok {
				unparsed = true
				break
			}
			end = ng
		}
		extents = append(extents, [2]int{start, end})
	}
	return extents, unparsed
}

// rangedHop is one variable-length relationship hop found in a lens's cypher.
type rangedHop struct {
	text   string // the hop's own "[...]" bracket text
	start  int    // byte offset of '[' within the spec
	finite bool   // whether it carries an explicit finite upper bound
	max    int    // that bound, when finite
}

// rangedHops finds every relationship bracket in spec that carries a `*`
// variable-length marker and classifies its upper bound: `*n` is an exact
// hop count (finite, max=n); `*n..m` / `*..m` is finite with max=m; `*n..`,
// `*..` and bare `*` are open (unbounded).
func rangedHops(spec string) []rangedHop {
	var out []rangedHop
	for _, loc := range reBracket.FindAllStringIndex(spec, -1) {
		content := spec[loc[0]:loc[1]]
		if !strings.Contains(content, "*") {
			continue
		}
		m := reRangeSpec.FindStringSubmatch(content)
		if m == nil {
			continue
		}
		h := rangedHop{text: content, start: loc[0]}
		hasDots := m[2] == ".."
		switch {
		case !hasDots && m[1] != "":
			n, err := strconv.Atoi(m[1])
			h.finite = err == nil
			h.max = n
		case hasDots && m[3] != "":
			n, err := strconv.Atoi(m[3])
			h.finite = err == nil
			h.max = n
		default:
			h.finite = false // bare "*", "*n..", "*..", or "*.." — open
		}
		out = append(out, h)
	}
	return out
}

func posOf(l lens) string {
	return fmt.Sprintf("%s:%d", l.pos.Filename, l.pos.Line)
}

// collectConsts records every string const's value (backtick or quoted).
func collectConsts(f *ast.File, out map[string]string) {
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, s := range gd.Specs {
			vs, ok := s.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				if bl, ok := vs.Values[i].(*ast.BasicLit); ok && bl.Kind == token.STRING {
					if v, err := strconv.Unquote(bl.Value); err == nil {
						out[name.Name] = v
					}
				}
			}
		}
	}
}

// collectLenses finds every `[]pkgmgr.LensSpec{...}` composite literal and reads
// each element's fields, resolving the Spec const to its cypher.
func collectLenses(fset *token.FileSet, f *ast.File, consts map[string]string) []lens {
	var out []lens
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		at, ok := cl.Type.(*ast.ArrayType)
		if !ok || !isLensSpecSelector(at.Elt) {
			return true
		}
		for _, e := range cl.Elts {
			el, ok := e.(*ast.CompositeLit)
			if !ok {
				continue
			}
			l := lens{pos: fset.Position(el.Pos())}
			for _, fe := range el.Elts {
				kv, ok := fe.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				switch key.Name {
				case "CanonicalName":
					l.name = stringLit(kv.Value)
				case "Adapter":
					l.adapter = stringLit(kv.Value)
				case "ProjectionKind":
					l.projectionKind = stringLit(kv.Value)
				case "Walks":
					// An explicit `Walks: nil` or an empty slice literal
					// declares nothing.
					switch v := kv.Value.(type) {
					case *ast.Ident:
						l.hasWalks = v.Name != "nil"
					case *ast.CompositeLit:
						l.hasWalks = len(v.Elts) > 0
					default:
						l.hasWalks = true
					}
				case "Personal":
					if id, ok := kv.Value.(*ast.Ident); ok {
						l.personal = id.Name == "true"
					}
				case "GrantTable":
					if id, ok := kv.Value.(*ast.Ident); ok {
						l.grantTable = id.Name == "true"
					}
				case "Output":
					l.hasOutput = true
					l.outputKey = outputKeyPattern(kv.Value, consts)
				case "Spec":
					switch v := kv.Value.(type) {
					case *ast.Ident:
						l.spec = consts[v.Name]
					case *ast.BasicLit:
						if s, err := strconv.Unquote(v.Value); err == nil {
							l.spec = s
						}
					}
				}
			}
			out = append(out, l)
		}
		return true
	})
	return out
}

// outputKeyPattern reads OutputKeyPattern out of an
// `&pkgmgr.OutputDescriptorSpec{...}` literal, resolving a bare const
// reference. Returns "" when the expression is not statically resolvable — the
// caller treats that as UNVERIFIABLE rather than as "not a cap-read producer",
// so a computed key pattern cannot walk past the gate.
func outputKeyPattern(e ast.Expr, consts map[string]string) string {
	if u, ok := e.(*ast.UnaryExpr); ok {
		e = u.X
	}
	cl, ok := e.(*ast.CompositeLit)
	if !ok {
		return ""
	}
	for _, fe := range cl.Elts {
		kv, ok := fe.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		id, ok := kv.Key.(*ast.Ident)
		if !ok || id.Name != "OutputKeyPattern" {
			continue
		}
		if lit := stringLit(kv.Value); lit != "" {
			return lit
		}
		if ref, ok := kv.Value.(*ast.Ident); ok {
			return consts[ref.Name]
		}
		return ""
	}
	return ""
}

func isLensSpecSelector(e ast.Expr) bool {
	se, ok := e.(*ast.SelectorExpr)
	return ok && se.Sel != nil && se.Sel.Name == "LensSpec"
}

func stringLit(e ast.Expr) string {
	if bl, ok := e.(*ast.BasicLit); ok && bl.Kind == token.STRING {
		if v, err := strconv.Unquote(bl.Value); err == nil {
			return v
		}
	}
	return ""
}

// runSelfTest proves checkNegatedRangeBound end-to-end against synthetic
// lenses written to a scratch package directory and run through the same
// checkPackage entry point the real corpus uses. This file carries
// `//go:build ignore` (like every script here), which keeps it out of `go
// test`'s normal package builds, so this doubles as the colocated test: `go
// run ./scripts/lint-lens-anchors.go --selftest`. It also runs unconditionally
// from main, where verbose is false: only failures are printed, and any mismatch
// exits 2 — the gate does not behave as documented, which is a different failure
// from a corpus violation.
func runSelfTest(verbose bool) {
	dir, err := os.MkdirTemp("", "lint-lens-anchors-selftest")
	if err != nil {
		fmt.Fprintln(os.Stderr, "lint-lens-anchors selftest: FAIL — mkdtemp:", err)
		os.Exit(2)
	}
	defer os.RemoveAll(dir)

	const src = `package selftestpkg

var Lenses = []pkgmgr.LensSpec{
	{
		CanonicalName: "positiveVector_flagged",
		Adapter:       "nats-subject",
		Spec: ` + "`" + `MATCH (a) WHERE NOT ( (a)-[:containedIn*0..3]->(b) ) RETURN a` + "`" + `,
	},
	{
		CanonicalName: "sameHopPositivePattern_notFlagged",
		Adapter:       "nats-subject",
		Spec: ` + "`" + `MATCH (a)-[:containedIn*0..3]->(b) RETURN a` + "`" + `,
	},
	{
		CanonicalName: "openRangeNegated_notFlagged",
		Adapter:       "nats-subject",
		Spec: ` + "`" + `MATCH (a) WHERE NOT (a)-[:containedIn*0..]->(b) RETURN a` + "`" + `,
	},
	{
		CanonicalName: "boundAtClampNegated_notFlagged",
		Adapter:       "nats-subject",
		Spec: ` + "`" + `MATCH (a) WHERE NOT (a)-[:containedIn*0..10]->(b) RETURN a` + "`" + `,
	},
	{
		CanonicalName: "plainHopNegated_notFlagged",
		Adapter:       "nats-subject",
		Spec: ` + "`" + `MATCH (a) WHERE NOT (a)-[:containedIn]->(b) RETURN a` + "`" + `,
	},
}
`
	if err := os.WriteFile(filepath.Join(dir, "lenses.go"), []byte(src), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "lint-lens-anchors selftest: FAIL — write fixture:", err)
		os.Exit(2)
	}

	var issues, warns int
	out, err := captureStdout(func() {
		issues, warns = checkPackage(dir)
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "lint-lens-anchors selftest: FAIL — capture stdout:", err)
		os.Exit(2)
	}
	if verbose {
		fmt.Print(out)
	}

	pass := true
	check := func(cond bool, desc string) {
		switch {
		case !cond:
			fmt.Fprintln(os.Stderr, "lint-lens-anchors selftest: FAIL —", desc)
			pass = false
		case verbose:
			fmt.Println("selftest: PASS —", desc)
		}
	}

	check(strings.Contains(out, "positiveVector_flagged") && strings.Contains(out, "narrows a variable-length hop"),
		"positive vector NOT ( (a)-[:containedIn*0..3]->(b) ) is flagged as an issue")
	check(!strings.Contains(out, "sameHopPositivePattern_notFlagged"),
		"the same hop in a positive (non-negated) pattern is not flagged")
	check(!strings.Contains(out, "openRangeNegated_notFlagged"),
		"an open *0.. hop inside NOT is not flagged")
	check(!strings.Contains(out, "boundAtClampNegated_notFlagged"),
		"a *0..10 hop (at the executor's own clamp) inside NOT is not flagged")
	check(!strings.Contains(out, "plainHopNegated_notFlagged"),
		"a plain, non-ranged hop inside NOT is not flagged")
	check(issues == 1, fmt.Sprintf("exactly one issue total (got %d)", issues))
	check(warns == 0, fmt.Sprintf("zero warns total (got %d)", warns))

	if !pass {
		fmt.Fprintln(os.Stderr, "lint-lens-anchors: self-test failure(s) — the gate does not behave as documented")
		os.Exit(2)
	}
	if verbose {
		fmt.Println("selftest: all vectors passed")
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything fn printed to it.
func captureStdout(fn func()) (string, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	saved := os.Stdout
	os.Stdout = w
	outCh := make(chan string, 1)
	go func() {
		var buf strings.Builder
		io.Copy(&buf, r)
		outCh <- buf.String()
	}()
	fn()
	w.Close()
	os.Stdout = saved
	return <-outCh, nil
}
