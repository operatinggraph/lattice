//go:build ignore

// lint-derive-reads-bare-vector — every op a DDL script's derive_reads
// dispatches on is proven, in its own package's test suite, by a submission
// that declares NOTHING.
//
// WHY. `derive_reads` (Contract #2 §2.5 class (g)) runs at the head of step 4,
// after authorization and before the first Core KV GET
// (step4_hydrate.go:321-327), and its result is merged into the declared read
// set the SAME way a submitter's own `contextHint` is — the descriptor floor
// (step4_hydrate.go:296-300) only DEMOTES a required read to optional, it never
// adds one, so a key the package's own derivation does not return is hydrated
// only if the submitter's `contextHint` names it. A bare `update`/`tombstone`
// mutation is auto-conditioned on the step-4 hydrated revision only for a key
// that WAS hydrated (`applyHydratedRevisions`, commit_path.go:672); a key that
// reached the script through a live, undeclared `kv.Read` instead has no step-4
// revision and stays unconditioned — "the honest limit" that function's own doc
// comment states. So the correctness of a script whose OCC guard rests on a
// derived key is exactly as good as the derivation, and nothing here proves the
// derivation runs when the caller declares nothing: every existing test either
// predates `derive_reads` and so declares the key itself, or was written
// alongside it and so also declares it, which means the derived path — the one
// path every real submitter which never learned the platform's Starlark
// grammar will actually take — has never been executed by any test. The class
// first surfaced at café `CreditCafeAccount` (2026-09-05: a debit case that
// happened to hand-declare `.balance` masked a derivation that silently
// returned nothing for it) and a second time at lease-signing
// `TombstoneSupersededLeaseServiceInstance` (2026-09-13): both ship a
// `derive_reads` with no test that omits the declaration it exists to replace.
//
// The read-drift guard (`internal/testutil/read_drift_guard.go`) is armed on
// every `testutil.CapabilityPipeline` with no opt-in, so a bare submission that
// falls through to a lazy, undeclared `kv.Read` fails the test at the read
// rather than silently passing — which is what makes a bare vector a real
// proof and not merely a shape check: PASS only happens if the derivation
// actually supplied the key.
//
// WHAT IT CHECKS. For each package in `pkgregistry.Names()`, each distinct DDL
// `Script` body (by text, so a script shared by several DDL registrations is
// read once) is parsed with `go.starlark.net/syntax`. A top-level `def
// derive_reads(...)` — matched by NAME alone, any parameter count or
// defaults, exactly how the Processor itself resolves it
// (`CompiledScript.deriveReadsProgram` -> `prog.DefinesTopLevel("derive_reads")`,
// compiled_script.go:131 — arity is never checked there either, since every
// call site invokes it with exactly one positional `op` argument regardless
// of what the def declares) — is walked for every comparison of its first
// parameter's `.operationType` — or a variable assigned from it one level
// (`ot = op.operationType`) — against a string: a literal, or an identifier
// resolving to a top-level `NAME = "literal"` module constant; and membership
// (`in` / `not in`) against a list/tuple: a literal one, or an identifier
// resolving to a top-level `NAME = ["a", "b"]` module constant. Each string
// named this way is GOVERNED. A derive_reads that calls a helper with the op
// value (or its alias) as an argument — positional OR keyword — is followed
// one level into that helper's own body for the same shape, matching the
// helper's parameter by position or by the keyword name.
//
// A derive_reads is read as governing every op in every DDL that dispatches
// through it (its `PermittedCommands`) — reported as `--list`'s "governed =
// ALL (<n> PermittedCommands)" — whenever the walk cannot fully resolve its
// own comparisons: no comparison against the op type at all, OR a comparison
// found but its other operand is not a literal / resolvable constant (a
// dynamic expression, a call, an unresolvable identifier). The second case
// matters as much as the first: a script comparing against an unresolved
// name UNDER-approves silently otherwise, reporting fewer governed ops than
// the script actually dispatches, which is a false all-clear waiting to
// happen — governing everything the DDL permits is the safe direction.
//
// For each governed op, every `packages/<dir>/*_test.go` (registry name ==
// directory name for the whole corpus today) is parsed, and only the
// `*ast.CompositeLit`s of type `OperationEnvelope` (bare or `&…`, whatever the
// import alias) sitting inside a top-level test function NAMED
// `Test*UndeclaredSubmitter*` are read for their `OperationType:` key — a
// literal match against a governed op is a VECTOR. Restricting to that name
// shape is deliberate (a cold review of this gate's first cut, 2026-09-14):
// a literal envelope declaring nothing can appear inside a test asserting
// something else entirely (a malformed-payload InvalidArgument case whose
// derive_reads short-circuits to `{}` before ever exercising the derivation
// this gate exists to prove) and still satisfy a name-blind literal scan — a
// phantom vector. Naming the test function is the author's own declaration of
// intent, mirroring the `# read-posture:`/`# derived-key:` "author declares"
// family this corpus already uses elsewhere (CLAUDE.md), and it is already
// the identity-domain precedent's own convention
// (`TestCreateUnclaimed_UndeclaredSubmitter_StillDedupes`,
// `TestCompleteCredentialLink_UndeclaredSubmitter_StillGuards`).
//
// The vector is BARE when `ContextHint` is absent from the literal or `nil`,
// or when its own composite carries `Reads`, `OptionalReads` AND
// `EgressReads` each absent, `nil`, or an empty slice literal — the three
// fields step 4 hydrates from the submitter's own declaration
// (`EgressReads` DOES hydrate: step4_hydrate.go:459-500 marks every egress
// key hydrated exactly like a declared read, so a non-empty `EgressReads` is
// exactly as much "not bare" as a non-empty `Reads`). A `ContextHint`
// carrying `Enumerations` does not disqualify a vector: a live `kv.Links`
// walk is Contract #2's orthogonal class (e) channel, declared by the caller
// and never returned by derive_reads at all, and a DDL whose every dispatch
// path performs one unconditionally (its hub is `{actor}`, always staticly
// known, so nothing stops a caller declaring it) cannot be driven through the
// read-drift-guarded test harness with a literally empty ContextHint
// regardless of what derive_reads supplies — that is not a gap in the
// derivation. Anything else in the composite (a non-empty
// Reads/OptionalReads/EgressReads, or a value that is not a literal at all)
// is a declared submission, not a vector. A governed op with zero bare
// vectors in its package is a finding.
//
// BOUNDARY (stated, not hidden). A test that builds the envelope through a
// helper call (`env := baseEnvelope(...)`) rather than the literal shape is
// invisible to this check — the mandated shape is the literal, so a new vector
// should use it directly rather than through a builder this gate cannot see
// into. A bare literal sitting inside a function NOT named
// `Test*UndeclaredSubmitter*` — a shared setup helper, a table-driven
// sub-test, an unrelated assertion that happens to submit one — is invisible
// too, by design (see WHAT IT CHECKS above); it is neither a vector nor
// reported as unmodelled, since the gate has nothing to say about a literal
// whose author never claimed it proves this property. An `OperationType:`
// value that is not a string literal is UNMODELLED (VERBOSE=1), neither a
// vector nor a finding contributor. `derive_reads` itself is trusted to run
// correctly at every OTHER call site (its per-key correctness is the
// read-declaration OCC class's other half, the shared-vertex repoint class
// and outright script bugs are not this gate's concern); this gate only
// proves that some named test exercises the declare-nothing path at all.
//
// Self-tests on every run (VERBOSE=1 for their narration) and refuses an
// all-clear over zero governed ops. `--list` prints the governed set per
// package. STRICT=1 exits non-zero on any finding; otherwise advisory.
//
// Run: STRICT=1 go run ./scripts/lint-derive-reads-bare-vector.go
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.starlark.net/syntax"

	"github.com/operatinggraph/lattice/internal/pkgregistry"
)

// listGoverned makes main print the governed set per package (`--list`).
var listGoverned bool

// verboseUnmodelled prints UNMODELLED sites the recognisers could not resolve.
var verboseUnmodelled bool

// undeclaredSubmitterFunc names the test-function shape a bare vector must
// sit inside to count (B3: a name-blind literal scan admits phantom
// vectors — a literal that declares nothing inside a test asserting an
// unrelated, earlier-short-circuited refusal).
var undeclaredSubmitterFunc = regexp.MustCompile(`^Test.*UndeclaredSubmitter.*$`)

func main() {
	strict := os.Getenv("STRICT") == "1"
	selftestVerbose := false
	for _, a := range os.Args[1:] {
		switch a {
		case "--list":
			listGoverned = true
		case "--selftest":
			selftestVerbose = true
		}
	}
	if os.Getenv("VERBOSE") == "1" {
		verboseUnmodelled = true
	}
	runSelfTest(selftestVerbose)

	var findings []string
	var unmodelled []string
	governedTotal := 0
	packagesExamined := 0

	for _, name := range pkgregistry.Names() {
		def, ok := pkgregistry.Lookup(name)
		if !ok {
			findings = append(findings, fmt.Sprintf("%s: pkgregistry.Names() lists this package but Lookup does not resolve it", name))
			continue
		}
		packagesExamined++

		governed := map[string]bool{}
		governAllReason := map[string]string{} // script identity (by CanonicalName) -> reason, for --list narration
		seenScripts := map[string]bool{}
		for _, d := range def.DDLs {
			if strings.TrimSpace(d.Script) == "" || seenScripts[d.Script] {
				continue
			}
			seenScripts[d.Script] = true
			f, err := syntax.Parse("script.star", d.Script, 0)
			if err != nil {
				continue // the Processor would refuse this script too
			}
			dr := findDeriveReads(f.Stmts)
			if dr == nil {
				continue
			}
			ops, resolved := governedOps(f.Stmts, dr)
			if !resolved {
				// derive_reads compares against nothing, or against something
				// this walk cannot resolve to a literal/constant: it governs
				// every op this DDL (and any sibling sharing the script)
				// dispatches, the safe direction over silent under-approval.
				for _, dd := range def.DDLs {
					if dd.Script == d.Script {
						for _, op := range dd.PermittedCommands {
							ops[op] = true
						}
						if len(dd.PermittedCommands) > 0 {
							governAllReason[dd.CanonicalName] = fmt.Sprintf("ALL (%d PermittedCommands) — derive_reads has an unresolved or absent operationType comparison", len(dd.PermittedCommands))
						}
					}
				}
			}
			for op := range ops {
				governed[op] = true
			}
		}
		if len(governed) == 0 {
			continue
		}
		governedTotal += len(governed)

		if listGoverned {
			names := sortedKeys(governed)
			suffix := ""
			if len(governAllReason) > 0 {
				var reasons []string
				for _, r := range governAllReason {
					reasons = append(reasons, r)
				}
				sort.Strings(reasons)
				suffix = fmt.Sprintf(" [%s]", strings.Join(reasons, "; "))
			}
			fmt.Printf("%s: governed = %s%s\n", name, strings.Join(names, ", "), suffix)
		}

		dir := filepath.Join("packages", name)
		testFiles, _ := filepath.Glob(filepath.Join(dir, "*_test.go"))
		sort.Strings(testFiles)

		bareVectors := map[string]bool{}
		for _, tf := range testFiles {
			bare, um := scanTestFile(tf, governed)
			for op := range bare {
				bareVectors[op] = true
			}
			unmodelled = append(unmodelled, um...)
		}

		for _, op := range sortedKeys(governed) {
			if !bareVectors[op] {
				findings = append(findings, fmt.Sprintf(
					"%s: %s is dispatched by its script's derive_reads but no Test*UndeclaredSubmitter* function submits it with an empty contextHint — add one OperationEnvelope{…} vector (Reads/OptionalReads/EgressReads all absent) whose assertion is the op's effect (or its named refusal), in a test function named Test*UndeclaredSubmitter*",
					name, op))
			}
		}
	}

	if verboseUnmodelled {
		for _, u := range unmodelled {
			fmt.Println("UNMODELLED " + u)
		}
	}

	if governedTotal == 0 {
		findings = append(findings, "lint-derive-reads-bare-vector: governed set is EMPTY across the whole corpus — the walk found no derive_reads to check, and a gate that checked nothing has no all-clear to give")
	}

	sort.Strings(findings)
	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) == 0 {
		fmt.Printf("lint-derive-reads-bare-vector: clean — %d op(s) governed across %d package(s), %d unmodelled site(s) (VERBOSE=1 lists them)\n",
			governedTotal, packagesExamined, len(unmodelled))
		return
	}
	fmt.Printf("lint-derive-reads-bare-vector: %d issue(s)\n", len(findings))
	if strict {
		os.Exit(1)
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// findDeriveReads locates a top-level `def derive_reads(...)` by NAME alone —
// the Processor's own CompiledScript.deriveReadsProgram resolves it the same
// way (prog.DefinesTopLevel("derive_reads"), compiled_script.go:131), with no
// arity check: every call site invokes it with exactly one positional `op`
// argument regardless of how many parameters (with defaults) the def
// declares.
func findDeriveReads(stmts []syntax.Stmt) *syntax.DefStmt {
	for _, s := range stmts {
		if d, ok := s.(*syntax.DefStmt); ok && d.Name.Name == "derive_reads" {
			return d
		}
	}
	return nil
}

func defByName(stmts []syntax.Stmt, name string) *syntax.DefStmt {
	for _, s := range stmts {
		if d, ok := s.(*syntax.DefStmt); ok && d.Name.Name == name {
			return d
		}
	}
	return nil
}

func paramName(d *syntax.DefStmt, idx int) string {
	if idx >= len(d.Params) {
		return ""
	}
	switch p := d.Params[idx].(type) {
	case *syntax.Ident:
		return p.Name
	case *syntax.BinaryExpr:
		if id, ok := p.X.(*syntax.Ident); ok {
			return id.Name
		}
	}
	return ""
}

// otScope tracks, within one derive_reads (or one helper it calls with the op
// or an ot-alias), which identifiers denote the op value and which denote
// op.operationType (one level of `alias = op.operationType` is followed).
type otScope struct {
	opNames []string
	otNames []string
}

func (s *otScope) isOp(e syntax.Expr) bool {
	id, ok := unparen(e).(*syntax.Ident)
	if !ok {
		return false
	}
	for _, n := range s.opNames {
		if n == id.Name {
			return true
		}
	}
	return false
}

// isOT reports whether e denotes op.operationType: either a direct
// `<opAlias>.operationType` access or a tracked alias identifier.
func (s *otScope) isOT(e syntax.Expr) bool {
	e = unparen(e)
	if dot, ok := e.(*syntax.DotExpr); ok && dot.Name.Name == "operationType" {
		if s.isOp(dot.X) {
			return true
		}
	}
	if id, ok := e.(*syntax.Ident); ok {
		for _, n := range s.otNames {
			if n == id.Name {
				return true
			}
		}
	}
	return false
}

// moduleConsts holds top-level `NAME = "literal"` and `NAME = [...]`/`(...)`
// (of string literals) bindings, so a comparison against a named constant
// resolves the same as one against an inline literal.
type moduleConsts struct {
	strs  map[string]string
	lists map[string][]string
}

func collectModuleConsts(stmts []syntax.Stmt) *moduleConsts {
	mc := &moduleConsts{strs: map[string]string{}, lists: map[string][]string{}}
	for _, s := range stmts {
		as, ok := s.(*syntax.AssignStmt)
		if !ok || as.Op != syntax.EQ {
			continue
		}
		id, ok := as.LHS.(*syntax.Ident)
		if !ok {
			continue
		}
		rhs := unparen(as.RHS)
		if lit, ok := stringLiteral(rhs); ok {
			mc.strs[id.Name] = lit
			continue
		}
		var elts []syntax.Expr
		switch l := rhs.(type) {
		case *syntax.ListExpr:
			elts = l.List
		case *syntax.TupleExpr:
			elts = l.List
		default:
			continue
		}
		var items []string
		ok = true
		for _, el := range elts {
			s, isStr := stringLiteral(el)
			if !isStr {
				ok = false
				break
			}
			items = append(items, s)
		}
		if ok && len(items) > 0 {
			mc.lists[id.Name] = items
		}
	}
	return mc
}

// resolveString resolves e to a string: a literal, or an identifier bound at
// module top level to one.
func (mc *moduleConsts) resolveString(e syntax.Expr) (string, bool) {
	if s, ok := stringLiteral(e); ok {
		return s, true
	}
	if id, ok := unparen(e).(*syntax.Ident); ok {
		if s, ok := mc.strs[id.Name]; ok {
			return s, true
		}
	}
	return "", false
}

// resolveStringList resolves e to a list of strings: an inline list/tuple of
// string literals, or an identifier bound at module top level to one.
func (mc *moduleConsts) resolveStringList(e syntax.Expr) ([]string, bool) {
	if items := stringListLiterals(e); items != nil {
		return items, true
	}
	if id, ok := unparen(e).(*syntax.Ident); ok {
		if items, ok := mc.lists[id.Name]; ok {
			return items, true
		}
	}
	return nil, false
}

// governedOps walks derive_reads (and, one level deep, any top-level helper it
// calls passing the op value or an ot-alias, by position or by keyword)
// collecting every string compared against op.operationType. The second
// return is false when the walk cannot fully account for the comparisons it
// found: no comparison against the op type at all, or one whose other operand
// did not resolve to a literal or a module constant — the caller falls back
// to governing every op the DDL permits rather than silently under-report.
func governedOps(fileStmts []syntax.Stmt, dr *syntax.DefStmt) (map[string]bool, bool) {
	ops := map[string]bool{}
	sawComparison := false
	unresolved := false
	mc := collectModuleConsts(fileStmts)

	scope := &otScope{opNames: []string{paramName(dr, 0)}}
	walkForCompares(dr.Body, scope, mc, ops, &sawComparison, &unresolved)

	// One level: a call inside derive_reads passing the op value (or an
	// ot-alias) as an argument — positional or keyword — to a top-level
	// helper, followed into that helper's own body with its corresponding
	// parameter seeded.
	syntax.Walk(dr, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fn.(*syntax.Ident)
		if !ok || id.Name == "derive_reads" {
			return true
		}
		helper := defByName(fileStmts, id.Name)
		if helper == nil {
			return true
		}
		posIdx := 0
		for _, a := range call.Args {
			var argExpr syntax.Expr
			var hName string
			if bin, ok := a.(*syntax.BinaryExpr); ok && bin.Op == syntax.EQ {
				// Keyword argument: match the helper's parameter by NAME.
				if kwID, ok := bin.X.(*syntax.Ident); ok {
					hName = kwID.Name
					argExpr = bin.Y
				}
			} else {
				// Positional argument: match by index among positional args.
				argExpr = a
				hName = paramName(helper, posIdx)
				posIdx++
			}
			if argExpr == nil || hName == "" {
				continue
			}
			if scope.isOp(argExpr) || scope.isOT(argExpr) {
				var hScope *otScope
				if scope.isOT(argExpr) {
					hScope = &otScope{otNames: []string{hName}}
				} else {
					hScope = &otScope{opNames: []string{hName}}
				}
				walkForCompares(helper.Body, hScope, mc, ops, &sawComparison, &unresolved)
			}
		}
		return true
	})

	resolved := sawComparison && !unresolved
	return ops, resolved
}

// walkForCompares collects string literals/module-constants compared against
// op.operationType (or its alias) via ==, !=, in, not in, including
// `and`/`or`-chained comparisons, and follows one-level
// `alias = op.operationType` assignments. sawComparison records whether ANY
// comparison against the op type was found at all; unresolved records
// whether one was found whose other operand could not be resolved to a
// literal or module constant.
func walkForCompares(body []syntax.Stmt, scope *otScope, mc *moduleConsts, ops map[string]bool, sawComparison, unresolved *bool) {
	for _, s := range body {
		syntax.Walk(s, func(n syntax.Node) bool {
			if as, ok := n.(*syntax.AssignStmt); ok && as.Op == syntax.EQ {
				if id, ok := as.LHS.(*syntax.Ident); ok && scope.isOT(as.RHS) {
					scope.otNames = append(scope.otNames, id.Name)
				}
			}
			bin, ok := n.(*syntax.BinaryExpr)
			if !ok {
				return true
			}
			switch bin.Op {
			case syntax.EQL, syntax.NEQ:
				var other syntax.Expr
				switch {
				case scope.isOT(bin.X):
					other = bin.Y
				case scope.isOT(bin.Y):
					other = bin.X
				default:
					return true
				}
				*sawComparison = true
				if lit, ok := mc.resolveString(other); ok {
					ops[lit] = true
				} else {
					*unresolved = true
				}
			case syntax.IN, syntax.NOT_IN:
				if !scope.isOT(bin.X) {
					return true
				}
				*sawComparison = true
				if items, ok := mc.resolveStringList(bin.Y); ok {
					for _, lit := range items {
						ops[lit] = true
					}
				} else {
					*unresolved = true
				}
			}
			return true
		})
	}
}

func stringLiteral(e syntax.Expr) (string, bool) {
	lit, ok := unparen(e).(*syntax.Literal)
	if !ok || lit.Token != syntax.STRING {
		return "", false
	}
	s, ok := lit.Value.(string)
	return s, ok
}

func stringListLiterals(e syntax.Expr) []string {
	e = unparen(e)
	var elts []syntax.Expr
	switch l := e.(type) {
	case *syntax.ListExpr:
		elts = l.List
	case *syntax.TupleExpr:
		elts = l.List
	default:
		return nil
	}
	var out []string
	for _, el := range elts {
		if s, ok := stringLiteral(el); ok {
			out = append(out, s)
		}
	}
	return out
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

// --- Go side: the _test.go vector census ---

// scanTestFile parses one _test.go and returns the set of governed ops it
// submits with an empty contextHint from inside a `Test*UndeclaredSubmitter*`
// top-level function, plus any unmodelled-site descriptions found there.
// A literal outside such a function contributes to neither set — see the
// header's BOUNDARY.
func scanTestFile(path string, governed map[string]bool) (map[string]bool, []string) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint-derive-reads-bare-vector: parse %s: %v\n", path, err)
		os.Exit(2)
	}
	bare := map[string]bool{}
	var unmodelled []string
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil || !undeclaredSubmitterFunc.MatchString(fd.Name.Name) {
			continue
		}
		inspectEnvelopeLiterals(fd.Body, governed, bare, &unmodelled, path, fset)
	}
	return bare, unmodelled
}

// inspectEnvelopeLiterals walks n for OperationEnvelope{…} composite
// literals, recording a bare vector per governed op and any unmodelled site.
func inspectEnvelopeLiterals(n ast.Node, governed map[string]bool, bare map[string]bool, unmodelled *[]string, path string, fset *token.FileSet) {
	ast.Inspect(n, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok || !isOperationEnvelope(cl.Type) {
			return true
		}
		line := fset.Position(cl.Pos()).Line
		var op string
		var opLiteral bool
		var hintEl ast.Expr
		var hasHintKey bool
		for _, el := range cl.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, _ := kv.Key.(*ast.Ident)
			if key == nil {
				continue
			}
			switch key.Name {
			case "OperationType":
				if bl, ok := kv.Value.(*ast.BasicLit); ok && bl.Kind == token.STRING {
					if s, err := strconv.Unquote(bl.Value); err == nil {
						op = s
						opLiteral = true
					}
				}
			case "ContextHint":
				hasHintKey = true
				hintEl = kv.Value
			}
		}
		if !opLiteral {
			return true
		}
		if !governed[op] {
			return true
		}
		if !hasHintKey {
			bare[op] = true
			return true
		}
		switch isBareContextHintValue(hintEl) {
		case bareYes:
			bare[op] = true
		case bareUnmodelled:
			*unmodelled = append(*unmodelled, fmt.Sprintf("%s:%d: %s — ContextHint value is not a recognized literal shape; not judged", path, line, op))
		}
		return true
	})
}

type bareVerdict int

const (
	bareNo bareVerdict = iota
	bareYes
	bareUnmodelled
)

// isBareContextHintValue classifies a `ContextHint:` value: `nil`, or
// `&…ContextHint{}` with no elements, is bare; a composite with any element is
// a declared submission; anything else (a variable, a call) cannot be judged
// statically and is unmodelled.
func isBareContextHintValue(e ast.Expr) bareVerdict {
	if id, ok := e.(*ast.Ident); ok && id.Name == "nil" {
		return bareYes
	}
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
		e = u.X
	}
	cl, ok := e.(*ast.CompositeLit)
	if !ok {
		return bareUnmodelled
	}
	// Reads, OptionalReads AND EgressReads are the fields step 4 hydrates from
	// the submitter's own declaration (EgressReads DOES hydrate —
	// step4_hydrate.go:459-500 marks every egress key hydrated exactly like a
	// declared read) — a vector is bare exactly when all three are absent or
	// explicitly empty. Enumerations sits on an orthogonal channel derive_reads
	// never populates — a live kv.Links walk is Contract #2's own class (e),
	// declared by the caller — and a DDL whose every dispatch path performs
	// one unconditionally (its hub is `{actor}`, always staticly known, so
	// nothing stops a caller declaring it) cannot be driven bare-of-
	// Enumerations through the read-drift-guarded test harness at all, so its
	// presence does not disqualify a vector.
	for _, el := range cl.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			// An unkeyed element is not a shape the corpus uses for
			// ContextHint; guessing its position is unsafe, so it is neither
			// bare nor confidently declared.
			return bareUnmodelled
		}
		key, _ := kv.Key.(*ast.Ident)
		if key == nil {
			return bareUnmodelled
		}
		switch key.Name {
		case "Reads", "OptionalReads", "EgressReads":
			if !isEmptySliceValue(kv.Value) {
				return bareNo
			}
		case "Enumerations":
			// Orthogonal channel; ignored for this verdict.
		default:
			return bareUnmodelled
		}
	}
	return bareYes
}

// isEmptySliceValue reports whether a Reads/OptionalReads/EgressReads field's
// value is nil or an empty slice literal — the two shapes that declare
// nothing.
func isEmptySliceValue(e ast.Expr) bool {
	if id, ok := e.(*ast.Ident); ok && id.Name == "nil" {
		return true
	}
	if cl, ok := e.(*ast.CompositeLit); ok {
		return len(cl.Elts) == 0
	}
	return false
}

// isOperationEnvelope reports whether a composite literal's type expression
// names OperationEnvelope, however it is package-qualified (processor.…,
// opwire.…, or a dot-imported bare identifier).
func isOperationEnvelope(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.SelectorExpr:
		return t.Sel.Name == "OperationEnvelope"
	case *ast.Ident:
		return t.Name == "OperationEnvelope"
	}
	return false
}

// --- self-test ---

func runSelfTest(verbose bool) {
	pass := true
	check := func(cond bool, desc string) {
		switch {
		case !cond:
			fmt.Fprintln(os.Stderr, "lint-derive-reads-bare-vector selftest: FAIL —", desc)
			pass = false
		case verbose:
			fmt.Println("selftest: PASS —", desc)
		}
	}

	governedOf := func(script string) (map[string]bool, bool) {
		f, err := syntax.Parse("selftest.star", script, 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, "lint-derive-reads-bare-vector selftest: script parse error:", err)
			os.Exit(2)
		}
		dr := findDeriveReads(f.Stmts)
		if dr == nil {
			return nil, false
		}
		return governedOps(f.Stmts, dr)
	}

	// (e) alias shape: ot = op.operationType; if ot == "X": ...
	ops, resolved := governedOf(`
def derive_reads(op):
    ot = op.operationType
    if ot == "CreateThing":
        return {"optionalReads": ["x"]}
    return {}
`)
	check(resolved && ops["CreateThing"], "alias `ot = op.operationType` then `ot == \"X\"` governs X")

	// (f) negated early-return shape.
	ops, resolved = governedOf(`
def derive_reads(op):
    if op.operationType != "CreateThing":
        return {}
    return {"optionalReads": ["x"]}
`)
	check(resolved && ops["CreateThing"] && len(ops) == 1, "`op.operationType != \"X\": return {}` governs X, and only X")

	// (g) an op not named by derive_reads is not governed.
	ops, _ = governedOf(`
def derive_reads(op):
    if op.operationType != "CreateThing":
        return {}
    return {"optionalReads": ["x"]}
`)
	check(!ops["OtherThing"], "an op derive_reads never names is not governed")

	// Conjunction shape (cafe-ledger's four-op guard).
	ops, resolved = governedOf(`
def derive_reads(op):
    ot = op.operationType
    if ot != "A" and ot != "B" and ot != "C":
        return {}
    return {"optionalReads": ["x"]}
`)
	check(resolved && ops["A"] && ops["B"] && ops["C"] && len(ops) == 3, "a chained `!=`/`and` guard governs every named op")

	// `in` membership against a literal list.
	ops, resolved = governedOf(`
def derive_reads(op):
    if op.operationType in ["A", "B"]:
        return {"optionalReads": ["x"]}
    return {}
`)
	check(resolved && ops["A"] && ops["B"], "`op.operationType in [\"A\", \"B\"]` governs both")

	// derive_reads comparing against nothing at all: not resolved, caller
	// falls back to PermittedCommands.
	_, resolved = governedOf(`
def derive_reads(op):
    return {"optionalReads": ["x"]}
`)
	check(!resolved, "a derive_reads with no operationType comparison reports resolved=false")

	// One-level helper delegation, positional.
	ops, resolved = governedOf(`
def is_governed(ot):
    return ot == "Delegated"

def derive_reads(op):
    ot = op.operationType
    if is_governed(ot):
        return {"optionalReads": ["x"]}
    return {}
`)
	check(resolved && ops["Delegated"], "one-level helper delegation, positional (ot passed to a helper comparing its own param) is followed")

	// One-level helper delegation, KEYWORD argument (S2).
	ops, resolved = governedOf(`
def is_governed(want):
    return want == "KwDelegated"

def derive_reads(op):
    ot = op.operationType
    if is_governed(want=ot):
        return {"optionalReads": ["x"]}
    return {}
`)
	check(resolved && ops["KwDelegated"], "one-level helper delegation via a KEYWORD argument is followed")

	// def derive_reads(op, extra=None): matched by name regardless of arity
	// (S2 — the Processor matches by name alone, compiled_script.go:131).
	ops, resolved = governedOf(`
def derive_reads(op, extra=None):
    if op.operationType == "MultiParam":
        return {"optionalReads": ["x"]}
    return {}
`)
	check(resolved && ops["MultiParam"], "derive_reads(op, extra=None) is matched by name; arity is not checked")

	// Module-level STRING constant resolved in a comparison (S2).
	ops, resolved = governedOf(`
CLAIM = "ClaimIdentity"

def derive_reads(op):
    if op.operationType == CLAIM:
        return {"optionalReads": ["x"]}
    return {}
`)
	check(resolved && ops["ClaimIdentity"] && len(ops) == 1, "a comparison against a module-level string constant resolves and governs it")

	// Module-level LIST constant resolved in an `in` membership test (S2).
	ops, resolved = governedOf(`
OPS = ["ListedA", "ListedB"]

def derive_reads(op):
    if op.operationType in OPS:
        return {"optionalReads": ["x"]}
    return {}
`)
	check(resolved && ops["ListedA"] && ops["ListedB"] && len(ops) == 2, "an `in` test against a module-level list constant resolves and governs every member")

	// A comparison against an UNRESOLVABLE operand ⇒ resolved=false (govern-
	// all fallback), not a silent under-approval (S2).
	_, resolved = governedOf(`
def some_dynamic():
    return "Whatever"

def derive_reads(op):
    if op.operationType == some_dynamic():
        return {"optionalReads": ["x"]}
    return {}
`)
	check(!resolved, "a comparison whose other operand is not a literal/constant reports resolved=false")

	// `in` against an unresolvable (non-list, non-constant) expression ⇒
	// resolved=false too.
	_, resolved = governedOf(`
def derive_reads(op):
    if op.operationType in some_call():
        return {"optionalReads": ["x"]}
    return {}
`)
	check(!resolved, "`in` against an unresolvable expression reports resolved=false")

	// --- Go-side vector census, via scanTestFile on a synthetic source. ---

	writeTemp := func(src string) string {
		f, err := os.CreateTemp("", "lint-derive-reads-bare-vector-selftest-*.go")
		if err != nil {
			fmt.Fprintln(os.Stderr, "selftest: tempfile:", err)
			os.Exit(2)
		}
		if _, err := f.WriteString(src); err != nil {
			fmt.Fprintln(os.Stderr, "selftest: write tempfile:", err)
			os.Exit(2)
		}
		f.Close()
		return f.Name()
	}
	governed := map[string]bool{"CreateThing": true}

	// (b) field absent entirely, inside a properly-named test func ⇒ bare.
	p := writeTemp(`package x
func TestCreateThing_UndeclaredSubmitter_Works(t *T) {
	e := processor.OperationEnvelope{OperationType: "CreateThing"}
	_ = e
}
`)
	bare, _ := scanTestFile(p, governed)
	os.Remove(p)
	check(bare["CreateThing"], "ContextHint field absent from the literal, inside a Test*UndeclaredSubmitter* func, is a bare vector")

	// (B3) the SAME literal shape, inside a test NOT named
	// Test*UndeclaredSubmitter*, contributes nothing — the phantom-vector
	// fix. This is the exact shape the cold review flagged: a
	// malformed-payload test whose derive_reads short-circuits before ever
	// exercising the derivation.
	p = writeTemp(`package x
func TestCreateThing_MalformedPayload_Rejected(t *T) {
	e := processor.OperationEnvelope{OperationType: "CreateThing"}
	_ = e
}
`)
	bare, _ = scanTestFile(p, governed)
	os.Remove(p)
	check(len(bare) == 0, "a bare literal OUTSIDE a Test*UndeclaredSubmitter* function is not a vector (phantom-vector fix, B3)")

	// (c) ContextHint: nil, inside the named shape ⇒ bare.
	p = writeTemp(`package x
func TestCreateThing_UndeclaredSubmitter_Works(t *T) {
	e := processor.OperationEnvelope{OperationType: "CreateThing", ContextHint: nil}
	_ = e
}
`)
	bare, _ = scanTestFile(p, governed)
	os.Remove(p)
	check(bare["CreateThing"], "ContextHint: nil, inside a Test*UndeclaredSubmitter* func, is a bare vector")

	// empty &ContextHint{} ⇒ bare.
	p = writeTemp(`package x
func TestCreateThing_UndeclaredSubmitter_Works(t *T) {
	e := &processor.OperationEnvelope{OperationType: "CreateThing", ContextHint: &processor.ContextHint{}}
	_ = e
}
`)
	bare, _ = scanTestFile(p, governed)
	os.Remove(p)
	check(bare["CreateThing"], "an empty &ContextHint{} is a bare vector")

	// (d) ContextHint carrying a Reads field ⇒ not bare (a finding survives).
	p = writeTemp(`package x
func TestCreateThing_UndeclaredSubmitter_Works(t *T) {
	e := processor.OperationEnvelope{OperationType: "CreateThing", ContextHint: &processor.ContextHint{Reads: []string{"k"}}}
	_ = e
}
`)
	bare, _ = scanTestFile(p, governed)
	os.Remove(p)
	check(!bare["CreateThing"], "a ContextHint with a declared Reads field is NOT a bare vector")

	// (S3) a non-empty EgressReads ⇒ NOT bare — it hydrates too
	// (step4_hydrate.go:459-500).
	p = writeTemp(`package x
func TestCreateThing_UndeclaredSubmitter_Works(t *T) {
	e := processor.OperationEnvelope{OperationType: "CreateThing", ContextHint: &processor.ContextHint{EgressReads: []string{"k"}}}
	_ = e
}
`)
	bare, _ = scanTestFile(p, governed)
	os.Remove(p)
	check(!bare["CreateThing"], "a ContextHint with a non-empty EgressReads is NOT a bare vector (S3 — EgressReads hydrates)")

	// An empty EgressReads (alongside absent Reads/OptionalReads) is still
	// bare.
	p = writeTemp(`package x
func TestCreateThing_UndeclaredSubmitter_Works(t *T) {
	e := processor.OperationEnvelope{OperationType: "CreateThing", ContextHint: &processor.ContextHint{EgressReads: []string{}}}
	_ = e
}
`)
	bare, _ = scanTestFile(p, governed)
	os.Remove(p)
	check(bare["CreateThing"], "an explicitly empty EgressReads is still bare")

	// Enumerations-only ⇒ bare: a live kv.Links walk is Contract #2's
	// orthogonal class (e) channel, never returned by derive_reads, so its
	// presence alone (with Reads/OptionalReads/EgressReads all absent) does
	// not disqualify the vector.
	p = writeTemp(`package x
func TestCreateThing_UndeclaredSubmitter_Works(t *T) {
	e := processor.OperationEnvelope{OperationType: "CreateThing", ContextHint: &processor.ContextHint{Enumerations: []processor.EnumerationHint{{Hub: "x", Relation: "holdsRole", Direction: "out"}}}}
	_ = e
}
`)
	bare, _ = scanTestFile(p, governed)
	os.Remove(p)
	check(bare["CreateThing"], "a ContextHint declaring only Enumerations (Reads/OptionalReads/EgressReads absent) is a bare vector")

	// Enumerations plus a non-empty Reads ⇒ not bare.
	p = writeTemp(`package x
func TestCreateThing_UndeclaredSubmitter_Works(t *T) {
	e := processor.OperationEnvelope{OperationType: "CreateThing", ContextHint: &processor.ContextHint{Reads: []string{"k"}, Enumerations: []processor.EnumerationHint{{Hub: "x", Relation: "holdsRole", Direction: "out"}}}}
	_ = e
}
`)
	bare, _ = scanTestFile(p, governed)
	os.Remove(p)
	check(!bare["CreateThing"], "Enumerations beside a non-empty Reads is NOT a bare vector")

	// An op not in the governed set is ignored even if bare.
	p = writeTemp(`package x
func TestCreateThing_UndeclaredSubmitter_Works(t *T) {
	e := processor.OperationEnvelope{OperationType: "SomeOtherOp"}
	_ = e
}
`)
	bare, _ = scanTestFile(p, map[string]bool{"CreateThing": true})
	os.Remove(p)
	check(len(bare) == 0, "an ungoverned op's bare literal contributes no vector")

	// A non-literal ContextHint value, inside the named shape, is UNMODELLED,
	// not bare.
	p = writeTemp(`package x
func TestCreateThing_UndeclaredSubmitter_Works(t *T) {
	h := someHelper()
	e := processor.OperationEnvelope{OperationType: "CreateThing", ContextHint: h}
	_ = e
}
`)
	bare, um := scanTestFile(p, governed)
	os.Remove(p)
	check(!bare["CreateThing"] && len(um) == 1, "a non-literal ContextHint value, inside a Test*UndeclaredSubmitter* func, is unmodelled, not a vector")

	if !pass {
		fmt.Fprintln(os.Stderr, "lint-derive-reads-bare-vector: self-test FAILED — the gate does not prove its own vectors; fix the gate before trusting any corpus verdict")
		os.Exit(2)
	}
	if verbose {
		fmt.Println("selftest: all vectors passed")
	}
}
