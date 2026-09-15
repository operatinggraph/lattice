//go:build ignore

// lint-workplace-staff-vector — every op whose script reaches
// require_workplace/enforce_workplace is proven, in its own package's test
// suite, by a submission from a NON-operator staff actor.
//
// WHY. A vertical's staff-widened ops (facet-staff-worlds-design.md §3.5)
// grant `frontOfHouse` (or an equivalent staff role) at scope=any — exactly
// the same shape `operator` holds, since Contract #6 only distinguishes
// `any` from `self` and the capability plane cannot tell staff from root. The
// ONLY thing confining a front-desk hat inside its own building is the
// script's own workplace walk. But `actor_holds_operator` (every package's
// own copy of the guard, facet-staff-worlds-design.md §3.5) returns BEFORE
// the worksAt walk runs, so a suite whose every vector for a governed op
// submits as the operator never executes that op's confinement reads at
// all — it proves the op works, never that the guard holds. Two sightings:
// café `Charge` (2026-09-13 — the first staff-confined vector for that op
// surfaced three read-drift rows every sibling op already carried) and
// wellness `JoinWaitlist` (2026-09-15, commit 7507be53 added the row to this
// package's own workplace_confinement_test.go — `JoinWaitlist` shares
// `prepare_booking_common` with `CreateBooking`, which WAS tested, so the
// twin op's guard had never run under anyone but the operator).
// docs/components/_packages.md §"Review keeps catching" carries the
// dossier entry this gate retires.
//
// WHAT IT CHECKS — reachability (the GOVERNED set). For every package in
// pkgregistry.Names(), each distinct DDL Script body (by text, so a script
// shared by several DDL registrations is read once) is parsed with
// go.starlark.net/syntax and its top-level `def execute(state, op):` is
// found (every shipped script names its dispatcher this, confirmed across
// the corpus — this is the SAME function the Processor's CompiledScript
// resolves the op through). Walking execute's body (following into every
// nested `if`/`for`, not only top-level statements, so a workplace call
// gated by a second condition inside an op's own branch — e.g.
// `if not workplace_exempt(): require_workplace(...)` — is still found), each
// `if` whose condition compares the op-type value (`op.operationType`, or a
// local alias assigned from it — `ot = op.operationType`, tracked exactly
// once, the way every script in this corpus writes it) against a string
// literal via `==`, membership (`in` a literal list), or an `and`/`or` chain
// of either, associates that literal (or those literals) with the `if`'s
// TRUE branch. An op is GOVERNED when its branch — walked for CallExpr nodes
// anywhere inside it, nested ifs/fors included — contains a call to
// `require_workplace(`, `enforce_workplace(`, or clinic-domain's
// `enforce_workplace_confined(` (the corpus's one deliberate exception to
// S10's byte-identical pin — its own doc comment states it is a DIFFERENT
// name carrying package-specific policy the pinned functions may not, so it
// does not diverge silently under one of the pinned names; same
// confinement-guard family — empty candidate list denies, a covered one
// returns, an uncovered one fails AuthDenied — see isWorkplaceGuardName)
// DIRECTLY, or to another top-level `def` in the SAME script (any other
// name) whose OWN body
// (one level of indirection only, not followed further — the way
// wellness-domain's booking DDL factors CreateBooking's and JoinWaitlist's
// shared guard into `prepare_booking_common`, which itself calls
// `require_workplace` unconditionally) contains such a call. A branch with no
// resolvable op-type comparison at all contributes nothing (not a
// fail-open — `require_workplace`/`enforce_workplace` are hand-authored guard
// calls this walk can always find directly if present; there is no
// "governs everything" fallback because, unlike lint-derive-reads-bare-
// vector's derive_reads, this walk is not attributing a MISSING declaration
// to overbroad defaults — it is finding an explicit, always-present call
// site).
//
// WHAT IT CHECKS — the TESTABLE set (governed minus operator-only). A
// governed op is dropped from the census — printed as `SKIP <pkg> <Op>:
// operator-only grant`, not silently, since the census must stay visible —
// when `nonOperatorAnyGrants` finds NO `pkgmgr.PermissionSpec` in the
// package's own `def.Permissions` (the same structured grant table
// `scripts/verify-package-*.go` reads role grants from) granting that op at
// `Scope: "any"` to any role other than `operator`. This is not a
// per-package prose read (clinic-domain's own permissions.go doc comment
// claims RecordEncounter's "clinical surface... stay[s] operator-only",
// which is wrong about the STRUCT below it — the PermissionSpec itself
// grants `provider` too); it is the same authoritative table the platform
// installs from. The reasoning: `actor_holds_operator` exempts the operator
// BEFORE require_workplace/enforce_workplace ever runs (workplace_exempt's
// own early return), and a `Scope: "self"` grant is a DIFFERENT path
// entirely (validated-target, never workplace-confined) — so when operator
// is the only `Scope: "any"` grantee, no actor besides the already-exempted
// operator can ever reach the confinement call at all; the guard is
// unreachable by construction, not merely untested (café `DebitAccount`:
// `GrantsTo: []string{"operator"}` alone — its own workplace_confinement_test.go
// comment, "the charge goes in as the operator, which DebitAccount grants at
// scope=any and never confines," is correct, not a gap). A governed op that
// ALREADY has evidence (proven or declared) is never routed through this
// check — the case that matters is maintenance-domain's `ResolveWorkOrder`,
// whose OWN PermissionSpec row is operator-only too, but whose staff leg is
// real: `backOfHouse` reaches it only through an orchestration-base
// EPHEMERAL task grant (`mdTechTaskGrantDoc`, not `def.Permissions`), proven
// by an inline `Test*` vector this gate's evidence scan already finds —
// SKIP fires only when NEITHER signal (a standing grant NOR observed
// evidence) says the op is reachable by a non-operator actor.
//
// WHAT IT CHECKS — evidence (the PROVEN set). For each governed, testable op, the
// package earns a pass one of two ways:
//
//  1. ANY top-level `_test.go` function in the package (any file, any name
//     — deliberately not scoped to `workplace_confinement_test.go`; see
//     BOUNDARY) resolves as a non-operator "staff cap-doc func"
//     (`isStaffCapFunc`) and, anywhere in its body, builds a
//     `{OperationType: "<Op>", Scope: "any"}` `processor.PlatformPermission`
//     literal. A func resolves as staff one of two ways, mirroring the two
//     shapes this corpus's real helpers use: (a) its own body directly
//     builds a composite literal carrying a `Roles:` key whose value's
//     source text does NOT name the operator role
//     (`bootstrap.RoleOperatorKey`, matched textually — `operatorRoleHint`)
//     — the ordinary `wcStaffCapDoc`/`fdCapDoc`/`casCapDoc`/`sasCapDoc`/
//     `mdTechCapDoc`/`lfStaffCapDoc` shape; or (b) it assigns a local from a
//     call to another registered func and mutates that value further (cafe-
//     domain's `wcMenuCapDoc`: `doc := wcStaffCapDoc(); doc.PlatformPermissions
//     = append(doc.PlatformPermissions, processor.PlatformPermission{...})`)
//     — the appended literal sits OUTSIDE any Roles-bearing literal, so its
//     verdict is inherited from the base call, recursively. The Roles
//     exclusion is the whole discriminator: `domainCapDoc`/`ledgerCapDoc`/
//     clinic-domain's misleadingly-named `clStaffCapDoc` all grant the SAME
//     scope=any ops a real staff vector would, but hold the operator role,
//     for general fixture convenience — the capability plane cannot tell
//     staff from root (Contract #6), so ONLY a non-operator-role grant is
//     evidence the confinement guard's staff leg was ever a candidate to
//     run. Scoping by filename was tried first and missed clinic-domain's
//     `correct_appointment_status_test.go`'s `casCapDoc`,
//     `set_appointment_site_test.go`'s `sasCapDoc`, and every package's
//     `integration_test.go`-embedded per-op vector — real per-op vectors
//     live under whatever name the author chose, so the rule keys on the
//     func's own declared Roles instead of its filename.
//  2. An explicit `// staff-vector: <Op> — <TestName>: <why>` declaration
//     (author-declares, mirroring `# read-posture:`/`# workplace-exempt:`)
//     anywhere in a package `_test.go` file, for an op whose staff leg is
//     proven a different way (e.g. a scope=self ownership guard that is the
//     validated-target counterpart, not `require_workplace`, but which this
//     walk still finds because the script also carries an unconditional
//     `require_workplace` fallback for the standing path).
//
// A governed op with neither ⇒ FAIL, naming the package, the op, and the
// two accepted shapes.
//
// BOUNDARY (stated, not hidden). This proves LISTING — that the package
// declares a scope=any grant for the op to a non-operator actor inside a
// file whose entire purpose is staff-confinement testing — not that a
// `Test*` function actually SUBMITS the op through the pipeline as that
// actor and asserts on the outcome. Closing that half needs a live-corpus
// walk (does a `Test*` function in the same file dispatch an
// `OperationEnvelope{OperationType: "<Op>", ...}` as an actor holding that
// capability doc?), which this gate does not attempt — every sighted
// instance of the class this gate mechanizes was a LISTING gap (the op
// absent from the file's own cap-doc literal entirely, not a listed-but-
// unexercised op), so listing is the load-bearing half; the walk-half stays
// a review judgment call, recorded here rather than silently assumed closed.
// The Roles-text discriminator is itself a proxy, not a semantic check: a
// non-operator service-account doc (a Weaver/Loom capability doc granted a
// package-local automation role, never the operator role) enumerating a
// governed op at scope=any for an unrelated dispatch reason would register
// as proof here too. No such false positive exists in the corpus today
// (every op this gate governs is a human staff write, none a service-actor
// dispatch target), but a future one dispatched through a non-operator
// service role would need a human check this gate cannot make.
//
// `--selftest` replays both minting incidents from git history (read-only
// `git show <rev>:<path>`, never checkout/reset): wellness `JoinWaitlist` at
// 7507be53^ (FAIL) and 7507be53 (PASS), and café `Charge` at 96c371e9^ (the
// commit that added workplace_confinement_test.go's own Charge row — its
// parent has no such file at all, so FAIL) and 96c371e9 (PASS). `--list`
// prints the governed set and its evidence verdict per package.
//
// STRICT=1 exits non-zero on any finding; otherwise advisory (mirrors
// lint-derive-reads-bare-vector / lint-refusal-courtesy).
//
// Run: STRICT=1 go run ./scripts/lint-workplace-staff-vector.go
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.starlark.net/syntax"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
)

var listGoverned bool

var staffVectorDecl = regexp.MustCompile(`//\s*staff-vector:\s*([A-Za-z0-9_]+)\s*[—-]`)

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
	runSelfTest(selftestVerbose)

	var findings []string
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
		seenScripts := map[string]bool{}
		for _, d := range def.DDLs {
			if strings.TrimSpace(d.Script) == "" || seenScripts[d.Script] {
				continue
			}
			seenScripts[d.Script] = true
			for op := range governedOpsInScript(d.Script) {
				governed[op] = true
			}
		}
		if len(governed) == 0 {
			continue
		}
		governedTotal += len(governed)

		dir := filepath.Join("packages", name)
		proven, declared := scanPackageEvidence(dir)
		nonOperatorGrant := nonOperatorAnyGrants(def)

		if listGoverned {
			names := sortedKeys(governed)
			var tags []string
			for _, op := range names {
				switch {
				case proven[op]:
					tags = append(tags, op+"(capdoc)")
				case declared[op] != "":
					tags = append(tags, op+"(declared:"+declared[op]+")")
				case !nonOperatorGrant[op]:
					tags = append(tags, op+"(SKIP:operator-only)")
				default:
					tags = append(tags, op+"(MISSING)")
				}
			}
			fmt.Printf("%s: governed = %s\n", name, strings.Join(tags, ", "))
		}

		for _, op := range sortedKeys(governed) {
			if proven[op] || declared[op] != "" {
				continue
			}
			if !nonOperatorGrant[op] {
				// No PermissionSpec grants this op at Scope="any" to any role
				// other than operator — actor_holds_operator's own exemption
				// means the ONLY actor that can ever reach this op's
				// require_workplace/enforce_workplace call is already
				// operator-exempted before it, so the guard's staff leg is
				// unreachable by construction (not merely untested). Printed
				// unconditionally, not just under --list, so the census stays
				// visible without needing --list to explain a shrunk finding
				// count.
				fmt.Printf("SKIP %s %s: operator-only grant\n", name, op)
				continue
			}
			findings = append(findings, fmt.Sprintf(
				"%s: %s calls require_workplace/enforce_workplace but no non-operator staff vector proves it — "+
					"add {OperationType: %q, Scope: \"any\"} to the package's workplace_confinement_test.go "+
					"(or its confinement/forgery-test equivalent) staff cap doc, or declare "+
					"// staff-vector: %s — <TestName>: <why>",
				name, op, op, op))
		}
	}

	if governedTotal == 0 {
		findings = append(findings, "lint-workplace-staff-vector: governed set is EMPTY across the whole corpus — the walk found no require_workplace/enforce_workplace call sites to check")
	}

	sort.Strings(findings)
	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) == 0 {
		fmt.Printf("lint-workplace-staff-vector: clean — %d op(s) governed across %d package(s)\n", governedTotal, packagesExamined)
		return
	}
	fmt.Printf("lint-workplace-staff-vector: %d issue(s)\n", len(findings))
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

// nonOperatorAnyGrants reads def.Permissions (pkgmgr.PermissionSpec — the
// same structured grant table scripts/verify-package-*.go tables enumerate
// role grants from) and returns, per op, whether ANY PermissionSpec entry
// grants it at Scope="any" to a role OTHER than "operator". A governed op
// with no such entry has no staff leg to test: actor_holds_operator exempts
// the operator before require_workplace/enforce_workplace ever runs, and
// Contract #6's scope=self path is validated-target (workplace_exempt's
// early return), never workplace-confined — so if operator is the only
// scope=any grantee, no actor besides the already-exempted operator can ever
// reach the confinement call. The guard is unreachable by construction, not
// merely untested.
func nonOperatorAnyGrants(def pkgmgr.Definition) map[string]bool {
	out := map[string]bool{}
	for _, p := range def.Permissions {
		if p.Scope != "any" {
			continue
		}
		for _, role := range p.GrantsTo {
			if role != "operator" {
				out[p.OperationType] = true
				break
			}
		}
	}
	return out
}

// ---------- Starlark side: governed-op reachability ----------

// governedOpsInScript parses one DDL script and returns the op-type literals
// whose execute() branch reaches require_workplace/enforce_workplace, either
// directly or through one level of same-script helper indirection.
func governedOpsInScript(script string) map[string]bool {
	governed := map[string]bool{}
	f, err := syntax.Parse("script.star", script, 0)
	if err != nil {
		return governed // the Processor would refuse this script too
	}
	exec := defByName(f.Stmts, "execute")
	if exec == nil {
		return governed
	}
	opParam := ""
	for _, p := range exec.Params {
		if id, ok := p.(*syntax.Ident); ok && id.Name == "op" {
			opParam = id.Name
			break
		}
	}
	if opParam == "" {
		return governed
	}
	helperReach := computeHelperReach(f.Stmts)
	mc := collectModuleConsts(f.Stmts)
	scope := &otScope{opNames: []string{opParam}}
	walkExecuteBody(exec.Body, scope, mc, helperReach, governed)
	return governed
}

// isWorkplaceGuardName names the confinement-guard entry points this walk
// treats as terminal. require_workplace/enforce_workplace are pinned
// byte-identical corpus-wide (S10); clinic-domain's
// enforce_workplace_confined is the corpus's one deliberate exception — its
// own doc comment states it is a DIFFERENT name carrying package-specific
// policy (it drops the pinned functions' redundant actor_holds_operator
// recheck for a Starlark-wall-budget reason), precisely so it does not
// diverge silently under one of the pinned names. It is the same
// confinement-guard family — an empty candidate list denies, a covered one
// returns, an uncovered one fails AuthDenied — so a branch reaching it is
// governed exactly as one reaching require_workplace/enforce_workplace is.
func isWorkplaceGuardName(name string) bool {
	return name == "require_workplace" || name == "enforce_workplace" || name == "enforce_workplace_confined"
}

func defByName(stmts []syntax.Stmt, name string) *syntax.DefStmt {
	for _, s := range stmts {
		if d, ok := s.(*syntax.DefStmt); ok && d.Name.Name == name {
			return d
		}
	}
	return nil
}

// computeHelperReach precomputes, for every top-level def other than
// execute/require_workplace/enforce_workplace, whether that helper's OWN
// body (one level — not followed into any helper IT calls) directly calls
// require_workplace or enforce_workplace.
func computeHelperReach(stmts []syntax.Stmt) map[string]bool {
	reach := map[string]bool{}
	for _, s := range stmts {
		d, ok := s.(*syntax.DefStmt)
		if !ok {
			continue
		}
		if d.Name.Name == "execute" || isWorkplaceGuardName(d.Name.Name) {
			continue
		}
		reach[d.Name.Name] = bodyCallsWorkplaceDirect(d.Body)
	}
	return reach
}

// bodyCallsWorkplaceDirect reports whether any statement in stmts contains a
// direct CallExpr to require_workplace/enforce_workplace, walked through
// every nested if/for (not into a nested def — Starlark defs in this corpus
// are always top-level).
func bodyCallsWorkplaceDirect(stmts []syntax.Stmt) bool {
	found := false
	for _, s := range stmts {
		syntax.Walk(s, func(n syntax.Node) bool {
			if found {
				return false
			}
			call, ok := n.(*syntax.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fn.(*syntax.Ident); ok && isWorkplaceGuardName(id.Name) {
				found = true
				return false
			}
			return true
		})
		if found {
			break
		}
	}
	return found
}

// branchReachesWorkplace reports whether stmts (an op branch's TRUE body)
// reaches require_workplace/enforce_workplace directly or through one level
// of same-script helper indirection (helperReach).
func branchReachesWorkplace(stmts []syntax.Stmt, helperReach map[string]bool) bool {
	found := false
	for _, s := range stmts {
		syntax.Walk(s, func(n syntax.Node) bool {
			if found {
				return false
			}
			call, ok := n.(*syntax.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fn.(*syntax.Ident)
			if !ok {
				return true
			}
			if isWorkplaceGuardName(id.Name) {
				found = true
				return false
			}
			if helperReach[id.Name] {
				found = true
				return false
			}
			return true
		})
		if found {
			break
		}
	}
	return found
}

// otScope tracks which identifiers denote the op value and which denote
// op.operationType (one level of `alias = op.operationType` is followed,
// the only shape this corpus writes).
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
		okAll := true
		for _, el := range elts {
			str, isStr := stringLiteral(el)
			if !isStr {
				okAll = false
				break
			}
			items = append(items, str)
		}
		if okAll && len(items) > 0 {
			mc.lists[id.Name] = items
		}
	}
	return mc
}

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

// extractOpLiterals collects every op-type literal a condition compares
// against — ==, `in` a literal list, and any and/or chain of either — the
// safe-over-narrow direction: an "and" with an unrelated second conjunct
// still associates the literal with the branch, since the branch IS
// conditionally reachable for that op.
func extractOpLiterals(e syntax.Expr, scope *otScope, mc *moduleConsts) []string {
	e = unparen(e)
	bin, ok := e.(*syntax.BinaryExpr)
	if !ok {
		return nil
	}
	switch bin.Op {
	case syntax.EQL:
		if scope.isOT(bin.X) {
			if s, ok := mc.resolveString(bin.Y); ok {
				return []string{s}
			}
		}
		if scope.isOT(bin.Y) {
			if s, ok := mc.resolveString(bin.X); ok {
				return []string{s}
			}
		}
	case syntax.IN:
		if scope.isOT(bin.X) {
			if items, ok := mc.resolveStringList(bin.Y); ok {
				return items
			}
		}
	case syntax.OR, syntax.AND:
		out := extractOpLiterals(bin.X, scope, mc)
		out = append(out, extractOpLiterals(bin.Y, scope, mc)...)
		return out
	}
	return nil
}

// walkExecuteBody recurses through execute()'s statements (if/for bodies
// included) tracking the ot-alias scope in program order, associating each
// `if` whose condition names one or more op-type literals with that if's
// TRUE branch, and marking those literals governed when the branch reaches
// require_workplace/enforce_workplace.
func walkExecuteBody(stmts []syntax.Stmt, scope *otScope, mc *moduleConsts, helperReach map[string]bool, governed map[string]bool) {
	for _, s := range stmts {
		switch st := s.(type) {
		case *syntax.AssignStmt:
			if st.Op == syntax.EQ {
				if id, ok := st.LHS.(*syntax.Ident); ok && scope.isOT(st.RHS) {
					scope.otNames = append(scope.otNames, id.Name)
				}
			}
		case *syntax.IfStmt:
			ops := extractOpLiterals(st.Cond, scope, mc)
			if len(ops) > 0 && branchReachesWorkplace(st.True, helperReach) {
				for _, o := range ops {
					governed[o] = true
				}
			}
			walkExecuteBody(st.True, scope, mc, helperReach, governed)
			walkExecuteBody(st.False, scope, mc, helperReach, governed)
		case *syntax.ForStmt:
			walkExecuteBody(st.Body, scope, mc, helperReach, governed)
		}
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

// ---------- Go side: the _test.go evidence census ----------

// operatorRoleHint matches source text naming the operator role — the
// corpus's own `bootstrap.RoleOperatorKey` constant, or (defensively) any
// identifier/selector/string literal with "operator" in it — inside a
// CapabilityDoc literal's `Roles:` field. A CapabilityDoc granting the
// operator role is a general-purpose fixture actor (domainCapDoc,
// ledgerCapDoc, clinic-domain's misleadingly-named clStaffCapDoc all hold
// it) used across many tests for setup convenience, never a non-operator
// staff-confinement vector — the capability plane cannot tell staff from
// root (Contract #6), so a scope=any grant proves nothing about workplace
// confinement unless the actor is demonstrably NOT the operator.
var operatorRoleHint = regexp.MustCompile(`(?i)operator`)

// capFuncInfo is one top-level `_test.go` function, kept with its own
// FileSet+source so a `Roles:` field's value can be read back as text.
type capFuncInfo struct {
	fset *token.FileSet
	src  []byte
	body *ast.BlockStmt
}

// collectCapFuncs parses one Go test file and records every top-level
// function's body, keyed by name (package-unique, as Go requires) — the
// registry isStaffCapFunc resolves base-call indirection against (cafe-
// domain's `wcMenuCapDoc`: `doc := wcStaffCapDoc(); doc.PlatformPermissions
// = append(doc.PlatformPermissions, processor.PlatformPermission{...}, ...)`
// — the appended literals sit OUTSIDE any Roles-bearing composite literal,
// so resolving them needs the base call's own verdict, not a structural
// literal shape).
func collectCapFuncs(path string, src []byte, funcs map[string]capFuncInfo) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint-workplace-staff-vector: parse %s: %v\n", path, err)
		return
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		funcs[fd.Name.Name] = capFuncInfo{fset: fset, src: src, body: fd.Body}
	}
}

// exprSource returns the raw source text an AST expression spans, so a
// Roles field's value (`[]string{bootstrap.RoleOperatorKey}`, or
// `[]string{"vtx.role." + pkgmgr.RoleID(...)}"`) can be pattern-matched
// without evaluating it — this corpus's Roles values are never simple
// literals, so a structural field read (as OperationType/Scope below) is
// not viable here.
func exprSource(fset *token.FileSet, src []byte, e ast.Expr) string {
	start := fset.Position(e.Pos()).Offset
	end := fset.Position(e.End()).Offset
	if start < 0 || end > len(src) || start > end {
		return ""
	}
	return string(src[start:end])
}

// isStaffCapFunc resolves whether calling funcs[name] yields a non-operator
// (staff) CapabilityDoc: (1) the func's own body directly contains a
// composite literal with a `Roles:` key — its value's source text is
// checked against operatorRoleHint; or, failing that, (2) the body mutates
// some variable's `.PlatformPermissions` field (`doc.PlatformPermissions =
// append(doc.PlatformPermissions, ...)` — the append-extension idiom, used
// both by a dedicated `*CapDoc` helper like cafe-domain's `wcMenuCapDoc` AND
// inline inside an ordinary `Test*` function like maintenance-domain's
// `TestResolveWorkOrder_ForgedTargetStaysConfined`, which builds
// `doc := mdTechCapDoc()` amid a dozen unrelated setup calls before
// extending it) — the variable being mutated is traced back to ITS OWN
// originating `<var> := <otherFunc>(...)` call (by name, wherever it sits in
// the body — not simply the body's first call to any registered func, which
// a `Test*` function's leading fixture calls would satisfy before ever
// reaching the real cap-doc assignment), and that other func's verdict is
// inherited, recursively, cycle-guarded. resolved is false when neither
// shape is found — an unresolved func contributes no evidence (the safe
// direction: this corpus's real staff-cap-doc sites always resolve one of
// these two ways, so an unresolved one is more likely an unrelated helper
// than a staff vector this walk failed to recognize).
func isStaffCapFunc(funcs map[string]capFuncInfo, memo map[string]int, resolving map[string]bool, name string) (isStaff, resolved bool) {
	if v, ok := memo[name]; ok {
		return v == 1, v != 0
	}
	info, ok := funcs[name]
	if !ok || resolving[name] {
		return false, false
	}
	resolving[name] = true
	defer delete(resolving, name)

	var rolesSrc string
	ast.Inspect(info.body, func(n ast.Node) bool {
		if rolesSrc != "" {
			return false
		}
		if cl, ok := n.(*ast.CompositeLit); ok {
			for _, el := range cl.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Roles" {
					rolesSrc = exprSource(info.fset, info.src, kv.Value)
				}
			}
		}
		return true
	})
	if rolesSrc != "" {
		isStaff, resolved = !operatorRoleHint.MatchString(rolesSrc), true
		memoize(memo, name, isStaff, resolved)
		return isStaff, resolved
	}

	// No direct Roles literal: find the variable a `.PlatformPermissions =`
	// assignment mutates, then trace THAT variable's own origin.
	var mutatedVar string
	ast.Inspect(info.body, func(n ast.Node) bool {
		if mutatedVar != "" {
			return false
		}
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) == 0 {
			return true
		}
		sel, ok := as.Lhs[0].(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "PlatformPermissions" {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok {
			mutatedVar = id.Name
		}
		return true
	})
	if mutatedVar == "" {
		return false, false
	}
	var baseCallee string
	ast.Inspect(info.body, func(n ast.Node) bool {
		if baseCallee != "" {
			return false
		}
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range as.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok || id.Name != mutatedVar || i >= len(as.Rhs) {
				continue
			}
			call, ok := as.Rhs[i].(*ast.CallExpr)
			if !ok {
				continue
			}
			fid, ok := call.Fun.(*ast.Ident)
			if !ok {
				continue
			}
			if _, exists := funcs[fid.Name]; exists {
				baseCallee = fid.Name
			}
		}
		return true
	})
	if baseCallee == "" {
		return false, false
	}
	isStaff, resolved = isStaffCapFunc(funcs, memo, resolving, baseCallee)
	memoize(memo, name, isStaff, resolved)
	return isStaff, resolved
}

func memoize(memo map[string]int, name string, isStaff, resolved bool) {
	if !resolved {
		return
	}
	if isStaff {
		memo[name] = 1
	} else {
		memo[name] = 2
	}
}

// provenOpsFromFuncs collects, from every func in funcs resolved as a
// non-operator staff cap-doc builder, each
// `{OperationType: "<op>", Scope: "any"}` composite literal ANYWHERE in its
// body — inside a CapabilityDoc's PlatformPermissions field, or an
// append() call extending one built elsewhere (both shapes this corpus
// uses) — rather than requiring the literal to sit structurally inside a
// Roles-bearing literal (which the append idiom never does).
func provenOpsFromFuncs(funcs map[string]capFuncInfo) map[string]bool {
	proven := map[string]bool{}
	memo := map[string]int{}
	resolving := map[string]bool{}
	for name, info := range funcs {
		isStaff, resolved := isStaffCapFunc(funcs, memo, resolving, name)
		if !resolved || !isStaff {
			continue
		}
		ast.Inspect(info.body, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			var op, scope string
			for _, el := range cl.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, _ := kv.Key.(*ast.Ident)
				if key == nil {
					continue
				}
				if bl, ok := kv.Value.(*ast.BasicLit); ok && bl.Kind == token.STRING {
					s, err := strconv.Unquote(bl.Value)
					if err != nil {
						continue
					}
					switch key.Name {
					case "OperationType":
						op = s
					case "Scope":
						scope = s
					}
				}
			}
			if op != "" && scope == "any" {
				proven[op] = true
			}
			return true
		})
	}
	return proven
}

// scanPackageEvidence returns, for a package directory: proven[op] true when
// a non-operator staff cap-doc func (provenOpsFromFuncs) grants
// {OperationType: op, Scope: "any"} anywhere in the package's _test.go
// files, and declared[op] naming the // staff-vector: <Op> — declaration's
// own line.
func scanPackageEvidence(dir string) (proven map[string]bool, declared map[string]string) {
	declared = map[string]string{}
	funcs := map[string]capFuncInfo{}

	allTests, _ := filepath.Glob(filepath.Join(dir, "*_test.go"))
	sort.Strings(allTests)
	for _, tf := range allTests {
		b, err := os.ReadFile(tf)
		if err != nil {
			continue
		}
		collectCapFuncs(tf, b, funcs)
		for _, line := range strings.Split(string(b), "\n") {
			m := staffVectorDecl.FindStringSubmatch(line)
			if m != nil {
				declared[m[1]] = filepath.Base(tf) + ": " + strings.TrimSpace(line)
			}
		}
	}
	return provenOpsFromFuncs(funcs), declared
}

// ---------- self-test ----------

func runSelfTest(verbose bool) {
	pass := true
	check := func(cond bool, desc string) {
		switch {
		case !cond:
			fmt.Fprintln(os.Stderr, "lint-workplace-staff-vector selftest: FAIL —", desc)
			pass = false
		case verbose:
			fmt.Println("selftest: PASS —", desc)
		}
	}

	// --- unit checks over synthetic scripts ---

	ops := governedOpsInScript(`
def require_workplace(location_keys, what):
    pass

def execute(state, op):
    ot = op.operationType
    if ot == "Direct":
        require_workplace([1], "x")
        return {}
    if ot == "Unconfined":
        return {}
    fail("UnknownOperation: " + ot)
`)
	check(ops["Direct"] && !ops["Unconfined"] && len(ops) == 1,
		"a direct require_workplace call in an op's branch governs only that op")

	ops = governedOpsInScript(`
def enforce_workplace(location_keys, what):
    pass

def prepare_common(state, op, p):
    enforce_workplace([1], "x")

def execute(state, op):
    ot = op.operationType
    if ot == "A":
        prepare_common(state, op, {})
        return {}
    if ot == "B":
        prepare_common(state, op, {})
        return {}
    if ot == "C":
        return {}
    fail("UnknownOperation: " + ot)
`)
	check(ops["A"] && ops["B"] && !ops["C"] && len(ops) == 2,
		"one level of same-script helper indirection governs every op calling that helper (prepare_booking_common shape)")

	ops = governedOpsInScript(`
def require_workplace(location_keys, what):
    pass

def dispatch_only(state, op):
    return call_something_else()

def execute(state, op):
    ot = op.operationType
    if ot == "Indirect2":
        dispatch_only(state, op)
        return {}
    fail("UnknownOperation: " + ot)
`)
	check(!ops["Indirect2"],
		"a helper that does not itself call require_workplace/enforce_workplace does not govern (no two-level indirection)")

	ops = governedOpsInScript(`
def require_workplace(location_keys, what):
    pass

def execute(state, op):
    ot = op.operationType
    if ot == "Guarded":
        if not workplace_exempt():
            require_workplace([1], "x")
        return {}
    fail("UnknownOperation: " + ot)
`)
	check(ops["Guarded"], "a require_workplace call nested inside a second condition within the branch is still found")

	ops = governedOpsInScript(`
def require_workplace(location_keys, what):
    pass

def execute(state, op):
    ot = op.operationType
    if ot == "A" or ot == "B":
        require_workplace([1], "x")
        return {}
    fail("UnknownOperation: " + ot)
`)
	check(ops["A"] && ops["B"] && len(ops) == 2, "an or-chained op-type comparison governs every named op")

	ops = governedOpsInScript(`
def enforce_workplace_confined(location_keys, what):
    pass

def execute(state, op):
    ot = op.operationType
    if ot == "SetAppointmentSite":
        enforce_workplace_confined([1], "x")
        return {}
    fail("UnknownOperation: " + ot)
`)
	check(ops["SetAppointmentSite"], "clinic-domain's enforce_workplace_confined governs directly, same as require_workplace/enforce_workplace")

	grants := nonOperatorAnyGrants(pkgmgr.Definition{Permissions: []pkgmgr.PermissionSpec{
		{OperationType: "DebitAccount", Scope: "any", GrantsTo: []string{"operator"}},
		{OperationType: "CreditCafeAccount", Scope: "any", GrantsTo: []string{"operator", "frontOfHouse"}},
		{OperationType: "CreditCafeAccount", Scope: "self", GrantsTo: []string{"consumer"}},
		{OperationType: "RecordEncounter", Scope: "any", GrantsTo: []string{"operator", "provider"}},
	}})
	check(!grants["DebitAccount"], "an op granted Scope:\"any\" to operator alone has no non-operator grant (cafe-ledger DebitAccount's real shape)")
	check(grants["CreditCafeAccount"], "an op granted Scope:\"any\" to operator PLUS a real role has a non-operator grant")
	check(grants["RecordEncounter"], "a non-operator role other than frontOfHouse (provider) still counts as a non-operator grant")

	syntheticFuncs := map[string]capFuncInfo{}
	collectCapFuncs("synthetic_test.go", []byte(`package p
import (
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
)
func staffCapDoc() *processor.CapabilityDoc {
	return &processor.CapabilityDoc{
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateStudio", Scope: "any"},
			{OperationType: "SomeSelfOp", Scope: "self"},
		},
		Roles: []string{"vtx.role.frontOfHouse"},
	}
}
func operatorCapDoc() *processor.CapabilityDoc {
	return &processor.CapabilityDoc{
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "DebitAccount", Scope: "any"},
		},
		Roles: []string{bootstrap.RoleOperatorKey},
	}
}
func wcMenuCapDoc() *processor.CapabilityDoc {
	doc := staffCapDoc()
	doc.PlatformPermissions = append(doc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "CreateMenuItem", Scope: "any"})
	return doc
}
func setupSomething(t *testing.T) *substrate.Conn { return nil }
func TestResolveWorkOrder_ForgedTargetStaysConfined(t *testing.T) {
	conn := setupSomething(t)
	_ = conn
	woKey := "vtx.workorder.X"
	_ = woKey
	doc := staffCapDoc()
	doc.PlatformPermissions = append(doc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "ResolveWorkOrder", Scope: "any"})
}
`), syntheticFuncs)
	proven := provenOpsFromFuncs(syntheticFuncs)
	check(proven["CreateStudio"] && !proven["SomeSelfOp"],
		"a non-operator Scope:\"any\" PlatformPermission literal proves its op; a Scope:\"self\" one does not")
	check(!proven["DebitAccount"],
		"a PlatformPermission literal inside a CapabilityDoc whose Roles names the operator role proves nothing — it is a general fixture doc, not a staff vector")
	check(proven["CreateMenuItem"],
		"a PlatformPermission appended to a base staff cap doc's PlatformPermissions (the wcMenuCapDoc idiom) inherits the base func's non-operator verdict")
	check(proven["ResolveWorkOrder"],
		"a PlatformPermissions mutation INLINE inside a Test* function, preceded by unrelated setup calls, still traces the mutated var to its real originating cap-doc call (not the test's first setup call)")

	// --- history replay: the two minting incidents ---

	// A shallow clone (CI's actions/checkout fetches depth 1) cannot show the
	// minting commits; the replay is then SKIPPED and says so, never counted as
	// a pass or a failure — the synthetic checks above are the proof that runs
	// everywhere, the replay is the proof against the real incidents wherever
	// history is present. An empty `git show` on a revision that IS present
	// still means "file absent at this revision", which the pre-mint café
	// check relies on.
	haveRev := func(rev string) bool {
		return exec.Command("git", "cat-file", "-e", rev+"^{commit}").Run() == nil
	}
	replay := func(rev, path string) map[string]bool {
		out, err := exec.Command("git", "show", rev+":"+path).Output()
		if err != nil {
			return map[string]bool{} // file absent at this revision
		}
		funcs := map[string]capFuncInfo{}
		collectCapFuncs(path, out, funcs)
		return provenOpsFromFuncs(funcs)
	}

	wellnessPath := "packages/wellness-domain/workplace_confinement_test.go"
	if haveRev("7507be53^") {
		pre := replay("7507be53^", wellnessPath)
		post := replay("7507be53", wellnessPath)
		check(!pre["JoinWaitlist"], "wellness JoinWaitlist at 7507be53^ (pre-fix): no staff vector — FAIL")
		check(post["JoinWaitlist"], "wellness JoinWaitlist at 7507be53 (post-fix): staff vector present — PASS")
	} else {
		fmt.Println("selftest: SKIP — history replay of 7507be53 (revision not present in this clone)")
	}

	cafePath := "packages/cafe-domain/workplace_confinement_test.go"
	if haveRev("96c371e9^") {
		prePre := replay("96c371e9^", cafePath)
		prePost := replay("96c371e9", cafePath)
		check(!prePre["Charge"], "cafe Charge at 96c371e9^ (pre-mint, file did not exist): no staff vector — FAIL")
		check(prePost["Charge"], "cafe Charge at 96c371e9 (the minting commit): staff vector present — PASS")
	} else {
		fmt.Println("selftest: SKIP — history replay of 96c371e9 (revision not present in this clone)")
	}

	if !pass {
		os.Exit(2)
	}
}
