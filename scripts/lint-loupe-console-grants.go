//go:build ignore

// lint-loupe-console-grants — every operation the console submits under its
// own identity is one the console's role is granted, at the lane it submits on.
//
// THE HAZARD. cmd/loupe runs as the scoped `consoleOperator` role
// (loupe-operator-auth-lift-design.md mechanism B, "never root"), whose grants
// live in ONE package — packages/console-operator — and there is no wildcard.
// Every other package grants its ops to the root-equivalent primordial
// `operator`, which the console's identity deliberately does not hold. So an op
// a Loupe handler submits through the Gateway with the operator's token is
// authorized only if console-operator names it: absent from that list, the
// CapabilityAuthorizer denies it and the console feature no-ops. Nothing fails
// on the way there — the handler's own test mocks the Gateway and never sees
// the denial, the package-local pins of the op's OWN package stay green (it did
// grant `operator`), the corpus census and CI all pass — because the gap is
// cross-package: the op's package and the console's grant package are two
// Definitions that nothing joined.
//
// The class shipped twice. RecordCapabilityInstallReceipt (2026-08-29) landed
// granted to `operator` only, so the receipt would never have been written from
// the console; the review-console ops were then granted in one sweep. Then
// handleVaultErase's StartLoomPattern (the Shred button, 2026-08-21, found
// 2026-09-13): orchestration-base grants it to `operator` alone, the live
// console identity holds consoleOperator + consumer, and the Vault erase
// ceremony has been denied since the day it shipped. Twice-seen gets a gate.
//
// THE RULE. Every `gatewayOperationRequest{…}` composite literal in cmd/loupe's
// non-test sources — the one wire shape the console submits through the
// Gateway under the operator's token — is resolved by go/ast:
//
//   - OperationType a string literal: that op must be granted to consoleOperator
//     by the compiled console-operator Definition (pkgregistry), and the grant
//     must admit the LANE the literal submits on. `Lane` absent or
//     `string(processor.LaneDefault)` is the default lane; a grant with no
//     `Lanes` admits the default lane only, and one with `Lanes` admits exactly
//     those (verify-loupe-operator-tier proves a meta-only grant does NOT fall
//     through to default). Any other Lane expression is unresolvable and a
//     finding.
//   - OperationType anything else (a parameter, a field): the site is a RELAY —
//     the op is decided by a caller — and must appear in the ledger below. A
//     relay entry names the CONSUMER whose op set it relays (the ledger derives
//     the set from that consumer's own `submitOp(ctx, "<op>", …)` literals,
//     never a hand list) and the lane; each derived op is then checked exactly
//     like a literal, and the site's own Lane, where it resolves, must equal
//     the ledger's. Or the entry marks the operator's own passthrough form,
//     whose denial the reply surfaces to the operator by design. An unledgered
//     relay is a finding, a ledger entry whose site no longer exists is a
//     finding, and a consumer that yields zero ops is a finding, so the ledger
//     cannot rot into a gate inspecting nothing (lint-flag-consumer-census's
//     shape).
//   - Every `submitOpViaGateway(…)` call's request argument must be one of
//     those literals — written inline, or an identifier bound to one in the
//     same function. A request built any other way (`var req
//     gatewayOperationRequest` + field assignments, a helper's return) is a
//     finding: the gate could not read what it submits.
//
// KNOWN GAPS ARE PINNED, NOT HIDDEN. A literal site the grant list does not
// cover today is listed in knownUngranted with the fix it waits on. The gate
// fails a NEW ungranted site, and it also fails a knownUngranted entry that has
// become granted or has disappeared — the pin must be removed in the commit
// that lands the grant, so the ledger never says "still broken" about a fixed
// site. This is the ratchet shape lint-app-op-descriptors uses for its literal
// count.
//
// OUT OF SCOPE, stated: the `ctrl.<component>.<verb>` control-plane ops Loupe
// sends over NATS request/reply (proven by control-plane-capability-authz's
// own harness, `make test-control-plane-authz`); the pkgmgr Installer's own
// direct-NATS submission during op-meta retirement (`CancelTask`, stamped with
// the installer's AdminActor by `submitDirectOp`, which bypasses the Submit
// hook by design) — that write leaves the console process but not under the
// console identity, so it is not this gate's subject; whether the op's OWN
// package accepts the submission once authorized (its script's business
// rules, the read floor) — this gate holds the grant, not the op; and whether
// a handler SURFACES a rejected reply on its success path. That last is the
// other half of the class this gate mechanizes and is not mechanized here:
// handleVaultErase answers 200 with the Processor's rejection inside the body,
// and the console renders the error object as `[object Object]`, so a denial
// reaches the operator as noise rather than as the grant gap it is. A handler
// that submits under the console identity should answer a `rejected` reply as
// an error naming the code; the pinned gap's fix carries that change too.
//
// Self-tests on every run (`--selftest` prints each vector) and refuses an
// all-clear over zero examined sites.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/pkgregistry"
)

const (
	loupeDir        = "cmd/loupe"
	grantPackage    = "console-operator"
	consoleRole     = "consoleOperator"
	requestTypeName = "gatewayOperationRequest"
	laneDefault     = "default"
)

// relayEntry ledgers one site whose OperationType is not a literal: the file
// and enclosing function, and either the consumer whose op set it relays at one
// lane or the passthrough marker.
type relayEntry struct {
	file, fn    string
	consumer    string // directory whose `submitOp(ctx, "<op>", …)` literals are the relayed set
	lane        string
	passthrough bool
	why         string
	// ops is filled at run time from consumer (or supplied directly by the
	// self-test); never hand-written in the ledger.
	ops []string
}

// relayLedger is every relay site in cmd/loupe. A site missing from here is a
// finding; an entry naming a site that no longer relays is a finding.
var relayLedger = []relayEntry{
	{
		file: "gatewayrelay.go", fn: "pkgmgrSubmit",
		consumer: "internal/pkgmgr", lane: "meta",
		why: "pkgmgr.Installer.Submit relays whatever the installer submits through its Submit hook — today the package-lifecycle trio — at the meta lane, where pkgLifecyclePermissions grants it to consoleOperator (mechanism C).",
	},
	{
		file: "op.go", fn: "gatewayRequestFromEnvelope", passthrough: true,
		why: "the operator's own /api/op form: the op is whatever the operator typed, and a denial comes back in the reply the form renders — the operator sees it.",
	},
}

// knownGap is a literal site the grant list does not cover, held still by the
// gate until the fix it names lands.
type knownGap struct {
	file, fn, op, lane, fix string
}

// knownUngranted pins the literal sites the grant list does not cover today.
// An entry here is a live gap this gate is holding still, not an exemption;
// `fix` states what closes it and the decision it waits on.
var knownUngranted = []knownGap{
	{
		file: "vault.go", fn: "handleVaultErase", op: "StartLoomPattern", lane: laneDefault,
		fix: "a consoleOperator grant on StartLoomPattern in packages/console-operator (with its version bump); denied today for the configured consoleOperator identity because orchestration-base grants the op to `operator` only. The grant's scope is the open decision: orchestration-base holds it at scope:any, so mirroring it lets the console start EVERY Loom pattern, and the platform path has no `specific` scope to narrow it to identityErasure.",
	},
}

// site is one resolved gatewayOperationRequest literal.
type site struct {
	file, fn string
	line     int
	op       string // "" when not a literal
	lane     string // "" when unresolvable
	literal  bool
}

// grantTable is op → the lanes consoleOperator may submit it on.
type grantTable map[string]map[string]bool

// stats accumulates what a run examined so a clean verdict is auditable.
type stats struct {
	files     int
	sites     int
	literal   int
	relays    int
	granted   int
	pinnedGap int
}

func main() {
	strict := os.Getenv("STRICT") == "1"
	verbose := false
	for _, a := range os.Args[1:] {
		if a == "--selftest" {
			verbose = true
		}
	}
	runSelfTest(verbose)

	grants, err := loadGrants()
	if err != nil {
		fmt.Println("lint-loupe-console-grants:", err)
		os.Exit(1)
	}
	sites, files, err := collectSites(loupeDir)
	if err != nil {
		fmt.Println("lint-loupe-console-grants:", err)
		os.Exit(1)
	}
	ledger := make([]relayEntry, len(relayLedger))
	copy(ledger, relayLedger)
	for i := range ledger {
		if ledger[i].passthrough {
			continue
		}
		ops, err := consumerOps(ledger[i].consumer)
		if err != nil {
			fmt.Println("lint-loupe-console-grants:", err)
			os.Exit(1)
		}
		ledger[i].ops = ops
	}
	var st stats
	st.files = files
	findings := check(sites, grants, ledger, knownUngranted, &st)
	findings = append(findings, unreadableSubmits(loupeDir, ledger)...)
	if st.sites == 0 {
		findings = append(findings, fmt.Sprintf("lint-loupe-console-grants: examined %d file(s) under %s but found ZERO %s literals — the extraction is broken, and a gate that checked nothing has no all-clear to give", files, loupeDir, requestTypeName))
	}

	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) == 0 {
		fmt.Printf("lint-loupe-console-grants: clean — %d submission site(s) across %d file(s) in %s: %d literal (%d granted to %s at their lane, %d pinned known gap(s)), %d ledgered relay(s)\n",
			st.sites, st.files, loupeDir, st.literal, st.granted, consoleRole, st.pinnedGap, st.relays)
		return
	}
	fmt.Printf("lint-loupe-console-grants: %d issue(s) — %d site(s) across %d file(s), %d literal, %d relay(s)\n",
		len(findings), st.sites, st.files, st.literal, st.relays)
	if strict {
		os.Exit(1)
	}
}

// loadGrants reads the compiled console-operator Definition and returns the
// ops granted to the console role with the lanes each admits.
func loadGrants() (grantTable, error) {
	def, ok := pkgregistry.Lookup(grantPackage)
	if !ok {
		return nil, fmt.Errorf("pkgregistry does not resolve %q — the console's grant package is missing from the compiled corpus, so nothing can be checked", grantPackage)
	}
	g := grantTable{}
	for _, p := range def.Permissions {
		grantsConsole := false
		for _, r := range p.GrantsTo {
			if r == consoleRole {
				grantsConsole = true
			}
		}
		if !grantsConsole {
			continue
		}
		lanes := g[p.OperationType]
		if lanes == nil {
			lanes = map[string]bool{}
			g[p.OperationType] = lanes
		}
		if len(p.Lanes) == 0 {
			lanes[laneDefault] = true
			continue
		}
		for _, l := range p.Lanes {
			lanes[l] = true
		}
	}
	if len(g) == 0 {
		return nil, fmt.Errorf("%q grants nothing to %q — the role's grant list is empty, which cannot be the console's real posture", grantPackage, consoleRole)
	}
	return g, nil
}

// consumerOps derives the op set a relay carries from its consumer's own
// source: every `<recv>.submitOp(ctx, "<op>", …)` call in the directory's
// non-test files whose operationType argument is a string literal. A
// non-literal operationType there is a finding of its own — the set would be
// open.
func consumerOps(dir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, path := range matches {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", path, err)
		}
		var bad error
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "submitOp" || len(call.Args) < 2 {
				return true
			}
			if bl, ok := call.Args[1].(*ast.BasicLit); ok && bl.Kind == token.STRING {
				seen[strings.Trim(bl.Value, "`\"")] = true
				return true
			}
			bad = fmt.Errorf("%s:%d: submitOp's operationType is not a string literal, so the op set this consumer relays through the console is open and cannot be checked", path, fset.Position(call.Pos()).Line)
			return true
		})
		if bad != nil {
			return nil, bad
		}
	}
	ops := make([]string, 0, len(seen))
	for op := range seen {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	return ops, nil
}

// unreadableSubmits returns a finding for every submitOpViaGateway call whose
// request argument is neither a gatewayOperationRequest literal, nor an
// identifier bound to one in the same function, nor a call to a ledgered relay
// builder — a request the gate could not read.
func unreadableSubmits(dir string, ledger []relayEntry) []string {
	builders := map[string]bool{}
	for _, e := range ledger {
		builders[e.fn] = true
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return []string{fmt.Sprintf("lint-loupe-console-grants: %v", err)}
	}
	var findings []string
	for _, path := range matches {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return []string{fmt.Sprintf("lint-loupe-console-grants: %v", err)}
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return []string{fmt.Sprintf("lint-loupe-console-grants: %s: %v", path, err)}
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			findings = append(findings, unreadableSubmitsIn(fset, path, fn, builders)...)
		}
	}
	return findings
}

// unreadableSubmitsIn checks one function's submitOpViaGateway calls; builders
// names the ledgered relay functions whose return value is an accepted request.
func unreadableSubmitsIn(fset *token.FileSet, path string, fn *ast.FuncDecl, builders map[string]bool) []string {
	// literalBound is every identifier assigned a gatewayOperationRequest
	// literal anywhere in the function (`greq := gatewayOperationRequest{…}`).
	literalBound := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, rhs := range as.Rhs {
			lit, ok := rhs.(*ast.CompositeLit)
			if !ok {
				continue
			}
			if id, ok := lit.Type.(*ast.Ident); !ok || id.Name != requestTypeName {
				continue
			}
			if i < len(as.Lhs) {
				if id, ok := as.Lhs[i].(*ast.Ident); ok {
					literalBound[id.Name] = true
				}
			}
		}
		return true
	})
	var findings []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok || id.Name != "submitOpViaGateway" || len(call.Args) == 0 {
			return true
		}
		req := call.Args[len(call.Args)-1]
		switch x := req.(type) {
		case *ast.CompositeLit:
			if t, ok := x.Type.(*ast.Ident); ok && t.Name == requestTypeName {
				return true
			}
		case *ast.Ident:
			if literalBound[x.Name] {
				return true
			}
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok && builders[id.Name] {
				return true
			}
		}
		findings = append(findings, fmt.Sprintf("%s:%d %s(): submitOpViaGateway's request is not a %s literal written here, an identifier bound to one in this function, or a ledgered relay builder's result, so the gate cannot read which op it submits — build the request as a literal at the call.", path, fset.Position(call.Pos()).Line, fn.Name.Name, requestTypeName))
		return true
	})
	return findings
}

// collectSites parses every non-test Go file under dir and returns the
// gatewayOperationRequest literals it finds.
func collectSites(dir string) ([]site, int, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, 0, err
	}
	var sites []site
	files := 0
	for _, path := range matches {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, 0, err
		}
		files++
		found, err := sitesIn(filepath.Base(path), string(src))
		if err != nil {
			return nil, 0, fmt.Errorf("%s: %v", path, err)
		}
		sites = append(sites, found...)
	}
	return sites, files, nil
}

// sitesIn parses one file's source and resolves every gatewayOperationRequest
// literal in it: the enclosing function, the OperationType (literal or not)
// and the lane.
func sitesIn(file, src string) ([]site, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, 0)
	if err != nil {
		return nil, err
	}
	var sites []site
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			id, ok := lit.Type.(*ast.Ident)
			if !ok || id.Name != requestTypeName {
				return true
			}
			s := site{file: file, fn: fn.Name.Name, line: fset.Position(lit.Pos()).Line, lane: laneDefault}
			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				switch key.Name {
				case "OperationType":
					if bl, ok := kv.Value.(*ast.BasicLit); ok && bl.Kind == token.STRING {
						s.op = strings.Trim(bl.Value, "`\"")
						s.literal = true
					}
				case "Lane":
					s.lane = resolveLane(kv.Value)
				}
			}
			sites = append(sites, s)
			return true
		})
	}
	return sites, nil
}

// resolveLane reads the Lane field's expression: `string(processor.LaneX)` or
// a string literal resolve to the lane name; anything else is unresolvable.
func resolveLane(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			return strings.Trim(x.Value, "`\"")
		}
	case *ast.CallExpr:
		if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "string" && len(x.Args) == 1 {
			if sel, ok := x.Args[0].(*ast.SelectorExpr); ok {
				switch sel.Sel.Name {
				case "LaneDefault":
					return laneDefault
				case "LaneMeta":
					return "meta"
				case "LaneUrgent":
					return "urgent"
				case "LaneSystem":
					return "system"
				}
			}
		}
	}
	return ""
}

// check judges every site against the grants, the relay ledger and the pinned
// known gaps, and reports ledger rot in both directions.
func check(sites []site, grants grantTable, ledger []relayEntry, pinned []knownGap, st *stats) []string {
	var findings []string
	ledgerHit := make([]bool, len(ledger))
	pinHit := make([]bool, len(pinned))

	for _, s := range sites {
		st.sites++
		where := fmt.Sprintf("%s/%s:%d %s()", loupeDir, s.file, s.line, s.fn)
		if !s.literal {
			// A relay's lane is the ledger's to state (a passthrough carries
			// the operator's own), so the site's Lane expression is not read.
			st.relays++
			idx := -1
			for i, e := range ledger {
				if e.file == s.file && e.fn == s.fn {
					idx = i
				}
			}
			if idx < 0 {
				findings = append(findings, fmt.Sprintf("%s: OperationType is not a string literal — this site relays an op decided elsewhere, and it is not in this gate's relayLedger. Add an entry naming the ops it relays and the lane (each is then checked against %s's grants), or mark it passthrough with the reason the operator sees a denial.", where, consoleRole))
				continue
			}
			ledgerHit[idx] = true
			e := ledger[idx]
			if e.passthrough {
				continue
			}
			if s.lane != "" && s.lane != e.lane {
				findings = append(findings, fmt.Sprintf("%s: submits at lane %q but its relayLedger entry says %q — the ledger vouches for a lane the site no longer uses; fix whichever is wrong.", where, s.lane, e.lane))
			}
			if len(e.ops) == 0 {
				findings = append(findings, fmt.Sprintf("%s: its relayLedger entry derives ZERO ops from %s — the consumer's submitOp literals were not found, so the relay is unchecked; a ledger entry that inspects nothing is not a pass.", where, e.consumer))
			}
			for _, op := range e.ops {
				findings = append(findings, judgeGrant(where+" (relayed "+op+")", op, e.lane, grants)...)
			}
			continue
		}
		st.literal++
		if s.lane == "" {
			findings = append(findings, fmt.Sprintf("%s: the Lane expression is not `string(processor.Lane…)` or a string literal, so the lane this %s submits on cannot be resolved and the grant cannot be checked against it.", where, requestTypeName))
			continue
		}
		pinIdx := -1
		for i, p := range pinned {
			if p.file == s.file && p.fn == s.fn && p.op == s.op && p.lane == s.lane {
				pinIdx = i
			}
		}
		fs := judgeGrant(where, s.op, s.lane, grants)
		switch {
		case pinIdx >= 0 && len(fs) > 0:
			// The pinned gap still stands: held, not reported.
			pinHit[pinIdx] = true
			st.pinnedGap++
		case pinIdx >= 0:
			pinHit[pinIdx] = true
			findings = append(findings, fmt.Sprintf("%s: %s at lane %s is pinned in knownUngranted but IS granted to %s now — remove the pin in the commit that landed the grant (it waited on: %s).", where, s.op, s.lane, consoleRole, pinned[pinIdx].fix))
		case len(fs) > 0:
			findings = append(findings, fs...)
		default:
			st.granted++
		}
	}

	for i, e := range ledger {
		if !ledgerHit[i] {
			findings = append(findings, fmt.Sprintf("relayLedger: %s/%s %s() relays no %s any more — remove the entry, so the ledger never vouches for a site that is gone.", loupeDir, e.file, e.fn, requestTypeName))
		}
	}
	for i, p := range pinned {
		if strings.TrimSpace(p.fix) == "" {
			findings = append(findings, fmt.Sprintf("knownUngranted: %s/%s %s() %s carries no fix — a pin must state what closes the gap and what it waits on.", loupeDir, p.file, p.fn, p.op))
		}
		if !pinHit[i] {
			findings = append(findings, fmt.Sprintf("knownUngranted: %s/%s %s() no longer submits %s at lane %s — remove the pin (it waited on: %s).", loupeDir, p.file, p.fn, p.op, p.lane, p.fix))
		}
	}
	return findings
}

// judgeGrant returns the finding for one (op, lane) the console submits, or
// nothing when consoleOperator holds a grant admitting that lane.
func judgeGrant(where, op, lane string, grants grantTable) []string {
	lanes, ok := grants[op]
	if !ok {
		return []string{fmt.Sprintf("%s: submits %s under the operator's token, but packages/%s grants %s no %s — the CapabilityAuthorizer denies it and the console feature no-ops while every package-local pin stays green (the op's own package grants `operator`, which the console identity never holds). Grant it in packages/%s, or ledger why the console is meant to be refused here.", where, op, grantPackage, consoleRole, op, grantPackage)}
	}
	if !lanes[lane] {
		return []string{fmt.Sprintf("%s: submits %s at lane %q, but %s's grant admits only %s — a grant's own Lanes list is the authority and does not fall through to the default lane.", where, op, lane, consoleRole, quotedLanes(lanes))}
	}
	return nil
}

func quotedLanes(set map[string]bool) string {
	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, fmt.Sprintf("%q", l))
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// runSelfTest proves the gate on synthetic sources through sitesIn + check —
// the same path the real run uses.
func runSelfTest(verbose bool) {
	pass := true
	checkV := func(cond bool, desc string) {
		switch {
		case !cond:
			fmt.Fprintln(os.Stderr, "lint-loupe-console-grants selftest: FAIL —", desc)
			pass = false
		case verbose:
			fmt.Println("selftest: PASS —", desc)
		}
	}
	has := func(fs []string, sub string) bool {
		for _, f := range fs {
			if strings.Contains(f, sub) {
				return true
			}
		}
		return false
	}
	grants := grantTable{
		"AttachObject":   {laneDefault: true},
		"InstallPackage": {"meta": true},
	}
	type pin = knownGap
	parse := func(file, body string) []site {
		s, err := sitesIn(file, "package main\n"+body)
		if err != nil {
			checkV(false, fmt.Sprintf("synthetic source must parse: %v", err))
		}
		return s
	}
	run := func(sites []site, ledger []relayEntry, pinned []pin) ([]string, stats) {
		var st stats
		return check(sites, grants, ledger, pinned, &st), st
	}

	// Vector 1 — a granted literal at the default lane is clean.
	sites := parse("objects.go", `
func (s *server) attach() {
	greq := gatewayOperationRequest{Lane: string(processor.LaneDefault), OperationType: "AttachObject"}
	_ = greq
}`)
	f, st := run(sites, nil, nil)
	checkV(len(f) == 0 && st.granted == 1, fmt.Sprintf("a granted literal at its lane is clean (got %v)", f))

	// Vector 2 — the shipped defect: a literal op the role is not granted.
	sites = parse("vault.go", `
func (s *server) handleVaultErase() {
	_, _ = submitOpViaGateway(ctx, s.gatewayURL, operatorToken(ctx), gatewayOperationRequest{
		OperationType: "StartLoomPattern",
		Lane:          string(processor.LaneDefault),
	})
}`)
	f, _ = run(sites, nil, nil)
	checkV(len(f) == 1 && has(f, "StartLoomPattern") && has(f, "handleVaultErase") && has(f, "grants consoleOperator no StartLoomPattern"), fmt.Sprintf("an ungranted literal is a finding naming the op and the handler (got %v)", f))

	// Vector 2b — the same site pinned as a known gap is held, and the pin is
	// reported the moment the grant lands.
	pinned := []pin{{file: "vault.go", fn: "handleVaultErase", op: "StartLoomPattern", lane: laneDefault, fix: "r"}}
	f, st = run(sites, nil, pinned)
	checkV(len(f) == 0 && st.pinnedGap == 1, fmt.Sprintf("a pinned known gap is held, not reported (got %v)", f))
	grants["StartLoomPattern"] = map[string]bool{laneDefault: true}
	f, _ = run(sites, nil, pinned)
	checkV(len(f) == 1 && has(f, "remove the pin"), fmt.Sprintf("a pin whose grant has landed is a finding (got %v)", f))
	delete(grants, "StartLoomPattern")
	f, _ = run(nil, nil, pinned)
	checkV(len(f) == 1 && has(f, "no longer submits"), fmt.Sprintf("a pin whose site is gone is a finding (got %v)", f))

	// Vector 3 — lane mismatch: a meta-only grant does not admit default.
	sites = parse("x.go", `
func (s *server) install() {
	_ = gatewayOperationRequest{OperationType: "InstallPackage"}
}`)
	f, _ = run(sites, nil, nil)
	checkV(len(f) == 1 && has(f, `admits only "meta"`), fmt.Sprintf("a default-lane submission of a meta-only grant is a finding (got %v)", f))
	sites = parse("x.go", `
func (s *server) install() {
	_ = gatewayOperationRequest{OperationType: "InstallPackage", Lane: string(processor.LaneMeta)}
}`)
	f, _ = run(sites, nil, nil)
	checkV(len(f) == 0, fmt.Sprintf("the same op at the meta lane is clean (got %v)", f))

	// Vector 4 — an unresolvable lane is a finding.
	sites = parse("x.go", `
func (s *server) install() {
	_ = gatewayOperationRequest{OperationType: "AttachObject", Lane: laneFor(req)}
}`)
	f, _ = run(sites, nil, nil)
	checkV(len(f) == 1 && has(f, "cannot be resolved"), fmt.Sprintf("an unresolvable Lane expression is a finding (got %v)", f))

	// Vector 5 — relays: unledgered is a finding; ledgered ops are checked;
	// passthrough is clean; a stale ledger entry is a finding.
	sites = parse("gatewayrelay.go", `
func (s *server) pkgmgrSubmit(operationType string) {
	_ = gatewayOperationRequest{OperationType: operationType, Lane: string(processor.LaneMeta)}
}`)
	f, _ = run(sites, nil, nil)
	checkV(len(f) == 1 && has(f, "not in this gate's relayLedger"), fmt.Sprintf("an unledgered relay is a finding (got %v)", f))
	ledger := []relayEntry{{file: "gatewayrelay.go", fn: "pkgmgrSubmit", ops: []string{"InstallPackage"}, lane: "meta"}}
	f, st = run(sites, ledger, nil)
	checkV(len(f) == 0 && st.relays == 1, fmt.Sprintf("a ledgered relay whose ops are granted at its lane is clean (got %v)", f))
	ledger = []relayEntry{{file: "gatewayrelay.go", fn: "pkgmgrSubmit", ops: []string{"InstallPackage", "CreateMetaVertex"}, lane: "meta"}}
	f, _ = run(sites, ledger, nil)
	checkV(len(f) == 1 && has(f, "relayed CreateMetaVertex"), fmt.Sprintf("a ledgered relay naming an ungranted op is a finding on that op (got %v)", f))
	pass2 := parse("op.go", `
func gatewayRequestFromEnvelope(env *processor.OperationEnvelope) gatewayOperationRequest {
	return gatewayOperationRequest{Lane: string(env.Lane), OperationType: env.OperationType}
}`)
	f, _ = run(pass2, []relayEntry{{file: "op.go", fn: "gatewayRequestFromEnvelope", passthrough: true}}, nil)
	checkV(len(f) == 0, fmt.Sprintf("a ledgered passthrough with the operator's own lane is clean (got %v)", f))
	ledger = []relayEntry{{file: "op.go", fn: "gatewayRequestFromEnvelope", passthrough: true}}
	f, _ = run(sites, ledger, nil)
	checkV(len(f) == 2 && has(f, "relays no gatewayOperationRequest any more") && has(f, "not in this gate's relayLedger"), fmt.Sprintf("a stale ledger entry and the unledgered site are both reported (got %v)", f))

	// Vector 5b — a relay whose own lane resolves to something other than the
	// ledger's lane is a finding; a ledger entry deriving zero ops is a finding;
	// a pin with no fix is a finding.
	sitesLane := parse("gatewayrelay.go", `
func (s *server) pkgmgrSubmit(operationType string) {
	_ = gatewayOperationRequest{OperationType: operationType, Lane: string(processor.LaneDefault)}
}`)
	f, _ = run(sitesLane, []relayEntry{{file: "gatewayrelay.go", fn: "pkgmgrSubmit", ops: []string{"InstallPackage"}, lane: "meta"}}, nil)
	checkV(len(f) == 1 && has(f, "relayLedger entry says"), fmt.Sprintf("a relay submitting at a lane other than its ledger lane is a finding (got %v)", f))
	f, _ = run(sites, []relayEntry{{file: "gatewayrelay.go", fn: "pkgmgrSubmit", consumer: "nowhere", lane: "meta"}}, nil)
	checkV(len(f) == 1 && has(f, "derives ZERO ops"), fmt.Sprintf("a ledger entry that derived no ops is a finding (got %v)", f))
	f, _ = run(nil, nil, []pin{{file: "x.go", fn: "f", op: "X", lane: laneDefault}})
	checkV(len(f) == 2 && has(f, "carries no fix"), fmt.Sprintf("a pin with no fix is a finding (got %v)", f))

	// Vector 5c — the consumer derivation reads pkgmgr's real submitOp literals,
	// and a request the extractor cannot read is a finding.
	ops, err := consumerOps("internal/pkgmgr")
	checkV(err == nil && len(ops) == 3 && ops[0] == "InstallPackage" && ops[1] == "UninstallPackage" && ops[2] == "UpgradePackage", fmt.Sprintf("consumerOps derives the installer's trio from internal/pkgmgr (got %v, %v)", ops, err))
	fset := token.NewFileSet()
	fsrc, perr := parser.ParseFile(fset, "x.go", "package main\n"+`
func (s *server) a() {
	var req gatewayOperationRequest
	req.OperationType = "AttachObject"
	_, _ = submitOpViaGateway(ctx, s.gatewayURL, tok, req)
}
func (s *server) b() {
	greq := gatewayOperationRequest{OperationType: "AttachObject"}
	_, _ = submitOpViaGateway(ctx, s.gatewayURL, tok, greq)
	_, _ = submitOpViaGateway(ctx, s.gatewayURL, tok, gatewayOperationRequest{OperationType: "AttachObject"})
}
func (s *server) c() {
	_, _ = submitOpViaGateway(ctx, s.gatewayURL, tok, buildRequest())
	_, _ = submitOpViaGateway(ctx, s.gatewayURL, tok, gatewayRequestFromEnvelope(env))
}`, 0)
	checkV(perr == nil, fmt.Sprintf("the unreadable-submit vector must parse: %v", perr))
	var unread []string
	for _, d := range fsrc.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok {
			unread = append(unread, unreadableSubmitsIn(fset, "x.go", fn, map[string]bool{"gatewayRequestFromEnvelope": true})...)
		}
	}
	checkV(len(unread) == 2 && has(unread, "a()") && has(unread, "c()"), fmt.Sprintf("a var-declared request and a helper-built request are findings; a literal, a literal-bound identifier and a ledgered builder's result are not (got %v)", unread))

	// Vector 6 — a literal inside a nested closure still resolves to the
	// enclosing top-level function, and a non-request composite literal is
	// not a site.
	sites = parse("weaverauthor.go", `
func (s *server) propose() {
	do := func() {
		_, _ = submitOpViaGateway(ctx, gatewayOperationRequest{OperationType: "AttachObject"})
	}
	do()
	_ = otherRequest{OperationType: "NotASite"}
}`)
	checkV(len(sites) == 1 && sites[0].fn == "propose" && sites[0].op == "AttachObject", fmt.Sprintf("a literal in a closure is attributed to the enclosing function; other composite literals are ignored (got %+v)", sites))

	if !pass {
		fmt.Fprintln(os.Stderr, "lint-loupe-console-grants: self-test FAILED — the gate does not prove its own vectors; fix the gate before trusting any corpus verdict")
		os.Exit(2)
	}
	if verbose {
		fmt.Println("selftest: all vectors passed")
	}
}
