//go:build ignore

// lint-ceremony-throw-path — the catch beside a secret-minting submit never
// asserts the write did not land.
//
// THE HAZARD. Every vertical FE's transport wrapper (`api()` / `submitOp()`)
// throws on any non-OK response — a Gateway 5xx or a dropped connection
// included — and a Processor commit is not undone by the reply failing on the
// way back. For an ordinary op that is a stale view for a second; for a
// CEREMONY op (one whose descriptor carries a Ceremony spec — the op mints a
// secret the client shows exactly once, `internal/descriptorform`) it is the
// worst case: the identity may now be armed with a claim secret nobody holds,
// and the one message the desk reads says "Could not create guest", so nobody
// issues a fresh one. The rejected-reply branch beside it is different: a
// `status: "rejected"` reply IS a confirmed non-commit, and "Could not …" is
// exactly right there. Only the throw path is ambiguous. (Minted: LoftSpace
// RotateClaimKey, 2026-09-06; re-sighted 2026-09-13 at four ceremony catches
// across three apps that the fix never reached — the class is per-catch, and a
// fix at one catch teaches the next catch nothing.)
//
// THE RULE. In every `cmd/*-app/web/app.js`, inside any function whose body
// names a ceremony operation (the set is derived from the compiled package
// corpus — every op-meta with a Ceremony spec — never hand-listed) or calls
// `revealCeremonySecret` (the generic task-modal dispatcher shows whatever
// secret its descriptor minted), every
// `try { … } catch` whose body calls a WRITE TRANSPORT and whose catch body
// carries an ASSERTIVE failure phrase ("could not", "couldn't", "failed to",
// "did not", "didn't", "unable to", "was not created / sent / …") must also
// carry the landed-ambiguity vocabulary: "may have landed" or "not confirmed".
// The write transports are derived from the file itself, not hand-listed: a
// function is one when its body calls `fetch` (or any function) with an
// options literal whose `method` is not GET, or calls another write transport
// — `submitOp` (which hands `api` the POST literal) ← `opOrThrow` /
// `submitCatalogOp` in every app, and every function that calls one of them. A try
// that only loads the form module or builds the envelope never reaches the
// server, so "could not" is exact there; a catch that merely passes
// `e.message` through asserts nothing; a catch that rethrows is judged at the
// catch that finally narrates.
//
// The JavaScript is read with goja's parser (the same engine the FE tests
// execute under), so the rule sees try/catch structure, not indentation.
//
// Run: `go run ./scripts/lint-ceremony-throw-path.go` (`STRICT=1` to fail;
// `--list` prints every catch examined with its verdict).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/file"
	"github.com/dop251/goja/parser"

	"github.com/operatinggraph/lattice/internal/pkgregistry"
)

var listSites = false

// assertiveRe matches wording that tells the reader the write did not happen.
var assertiveRe = regexp.MustCompile(`(?i)\b(could ?n[o']t|failed to|did ?n[o']t|unable to|was not (created|sent|issued|saved|recorded|connected|reset|re-issued))\b`)

// ambiguityRe matches the landed-ambiguity vocabulary the fixed sites share.
var ambiguityRe = regexp.MustCompile(`(?i)may have landed|not confirmed|check whether it landed`)

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

	ceremonyOps := ceremonyOpSet()
	if len(ceremonyOps) == 0 {
		fmt.Println("lint-ceremony-throw-path: the compiled package corpus declares ZERO ceremony ops — the derivation is broken, and a gate over an empty set has no all-clear to give")
		os.Exit(1)
	}
	apps, _ := filepath.Glob("cmd/*-app/web/app.js")
	sort.Strings(apps)
	var findings []string
	var st stats
	for _, path := range apps {
		src, err := os.ReadFile(path)
		if err != nil {
			findings = append(findings, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		fs := &file.FileSet{}
		prog, err := parser.ParseFile(fs, path, parseable(string(src)), 0)
		if err != nil {
			findings = append(findings, fmt.Sprintf("%s: goja could not parse the file — the FE tests execute it under the same engine, so this is the app's own failure, not this gate's: %v", path, err))
			continue
		}
		st.files++
		transports := writeTransports(prog)
		if len(transports) == 0 {
			findings = append(findings, fmt.Sprintf("%s: no write transport found — no function in the file calls fetch (or anything) with a non-GET method, so this gate cannot tell a submit from a load and has no all-clear to give", path))
			continue
		}
		findings = append(findings, checkProgram(path, fs, prog, ceremonyOps, transports, &st)...)
	}
	if st.files == 0 {
		findings = append(findings, "lint-ceremony-throw-path: examined ZERO app files — the glob is broken, and a gate that checked nothing has no all-clear to give")
	}
	for _, f := range findings {
		fmt.Println(f)
	}
	names := make([]string, 0, len(ceremonyOps))
	for op := range ceremonyOps {
		names = append(names, op)
	}
	sort.Strings(names)
	if len(findings) == 0 {
		fmt.Printf("lint-ceremony-throw-path: clean — %d app file(s); ceremony ops {%s}; %d ceremony function(s), %d catch(es) examined\n",
			st.files, strings.Join(names, ", "), st.functions, st.catches)
		return
	}
	fmt.Printf("lint-ceremony-throw-path: %d issue(s) — %d app file(s); ceremony ops {%s}; %d ceremony function(s), %d catch(es) examined\n",
		len(findings), st.files, strings.Join(names, ", "), st.functions, st.catches)
	if strict {
		os.Exit(1)
	}
}

type stats struct {
	files, functions, catches int
}

// parseable rewrites the one syntax goja does not parse — the dynamic
// `import(...)` each app uses to load the shared descriptor-form module — into
// an ordinary call of the same length with the same argument list, so the
// rest of the file parses unchanged and every position still indexes the
// original source.
func parseable(src string) string {
	return dynamicImportRe.ReplaceAllString(src, "${1}IMPORT(")
}

var dynamicImportRe = regexp.MustCompile(`(^|[^A-Za-z0-9_$.])import\(`)

// ceremonyOpSet derives the ceremony operation names from the compiled package
// corpus: every op-meta carrying a Ceremony spec.
func ceremonyOpSet() map[string]bool {
	out := map[string]bool{}
	for _, name := range pkgregistry.Names() {
		def, ok := pkgregistry.Lookup(name)
		if !ok {
			continue
		}
		for _, m := range def.OpMetas {
			if m.Ceremony != nil {
				out[m.OperationType] = true
			}
		}
	}
	return out
}

// checkProgram walks every function in the program; those whose body names a
// ceremony op have each of their awaiting try/catch statements judged.
func checkProgram(path string, fs *file.FileSet, prog *ast.Program, ceremonyOps, transports map[string]bool, st *stats) []string {
	var findings []string
	walk(prog, func(n ast.Node) bool {
		var body *ast.BlockStatement
		var name string
		switch fn := n.(type) {
		case *ast.FunctionLiteral:
			body = fn.Body
			if fn.Name != nil {
				name = string(fn.Name.Name)
			}
		case *ast.ArrowFunctionLiteral:
			if b, ok := fn.Body.(*ast.BlockStatement); ok {
				body = b
			}
		}
		if body == nil {
			return true
		}
		if !namesCeremonyOp(body, ceremonyOps) {
			return true
		}
		st.functions++
		if name == "" {
			name = "(anonymous function)"
		}
		findings = append(findings, checkFunction(path, fs, name, body, transports, st)...)
		return true
	})
	return findings
}

// writeTransports derives the functions through which a write reaches the
// server: a call carrying an options literal whose method is not GET marks
// the enclosing function; a call to a marked function marks its caller.
// Iterates to a fixed point over the top-level function declarations.
func writeTransports(prog *ast.Program) map[string]bool {
	fns := map[string]*ast.FunctionLiteral{}
	for _, st := range prog.Body {
		if d, ok := st.(*ast.FunctionDeclaration); ok && d.Function.Name != nil {
			fns[d.Function.Name.Name.String()] = d.Function
		}
	}
	out := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for name, fn := range fns {
			if out[name] {
				continue
			}
			if callsWriteTransport(fn.Body, out) {
				out[name] = true
				changed = true
			}
		}
	}
	return out
}

// callsWriteTransport reports whether a block (nested functions included —
// a `.then` callback still sends) calls a known write transport or passes a
// non-GET method literal to any call.
func callsWriteTransport(body *ast.BlockStatement, transports map[string]bool) bool {
	found := false
	walk(body, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpression)
		if !ok {
			return !found
		}
		if id, ok := c.Callee.(*ast.Identifier); ok && transports[id.Name.String()] {
			found = true
		}
		for _, a := range c.ArgumentList {
			if carriesWriteMethod(a) {
				found = true
			}
		}
		return !found
	})
	return found
}

// carriesWriteMethod recognises `{ method: "POST" }` (any non-GET verb) in an
// options literal.
func carriesWriteMethod(e ast.Expression) bool {
	obj, ok := e.(*ast.ObjectLiteral)
	if !ok {
		return false
	}
	for _, p := range obj.Value {
		kv, ok := p.(*ast.PropertyKeyed)
		if !ok {
			continue
		}
		key := ""
		switch k := kv.Key.(type) {
		case *ast.Identifier:
			key = k.Name.String()
		case *ast.StringLiteral:
			key = k.Value.String()
		}
		if key != "method" {
			continue
		}
		if v, ok := kv.Value.(*ast.StringLiteral); ok && !strings.EqualFold(v.Value.String(), "GET") {
			return true
		}
	}
	return false
}

// namesCeremonyOp reports whether the block (nested functions included) names
// a ceremony operation — as a string literal (`operationType: "RotateClaimKey"`)
// or as the property an app reads its catalog row by
// (`state.opCatalog.RevokeIdentityClaim`) — or dispatches ceremonies
// generically: a function that calls `revealCeremonySecret` is built to
// show a minted secret, whichever descriptor reached it.
func namesCeremonyOp(body *ast.BlockStatement, ceremonyOps map[string]bool) bool {
	found := false
	walk(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.StringLiteral:
			if ceremonyOps[x.Value.String()] {
				found = true
			}
		case *ast.DotExpression:
			if ceremonyOps[x.Identifier.Name.String()] {
				found = true
			}
		case *ast.CallExpression:
			if id, ok := x.Callee.(*ast.Identifier); ok && id.Name.String() == "revealCeremonySecret" {
				found = true
			}
		}
		return !found
	})
	return found
}

// checkFunction judges every try statement in the function body (not in
// nested functions) whose body reaches a write transport and whose catch
// narrates.
func checkFunction(path string, fs *file.FileSet, fnName string, body *ast.BlockStatement, transports map[string]bool, st *stats) []string {
	var findings []string
	walkShallow(body, func(n ast.Node) bool {
		try, ok := n.(*ast.TryStatement)
		if !ok || try.Catch == nil || try.Catch.Body == nil {
			return true
		}
		if !callsWriteTransport(try.Body, transports) {
			return true
		}
		st.catches++
		text := narration(try.Catch.Body)
		line := fs.Position(try.Catch.Catch).Line
		verdict := "no assertive wording"
		switch {
		case assertiveRe.MatchString(text) && !ambiguityRe.MatchString(text):
			verdict = "ASSERTS NON-LANDING"
			findings = append(findings, fmt.Sprintf("%s:%d: the catch in %s follows an awaited submit in a function that dispatches a ceremony op, and its wording asserts the write did not happen (%q) — the transport throws on a 5xx after the Processor may already have committed and minted the secret, so say the write may have landed and what to do next (the `withheld` vocabulary: \"may have landed\" / \"not confirmed\"), or pass e.message through without asserting.",
				path, line, fnName, strings.TrimSpace(assertiveRe.FindString(text))))
		case ambiguityRe.MatchString(text):
			verdict = "landed-ambiguity vocabulary present"
		}
		if listSites {
			fmt.Printf("  %s:%d · %s · catch → %s\n", path, line, fnName, verdict)
		}
		return true
	})
	return findings
}

// narration concatenates every string and template literal in a catch body,
// nested functions excluded — the words the person reads. goja keeps a
// non-ASCII literal as UTF-16 behind a marker byte; String() is the UTF-8 view.
func narration(body *ast.BlockStatement) string {
	var parts []string
	walkShallow(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.StringLiteral:
			parts = append(parts, s.Value.String())
		case *ast.TemplateLiteral:
			for _, el := range s.Elements {
				parts = append(parts, el.Parsed.String())
			}
		}
		return true
	})
	return strings.Join(parts, " ")
}

// walk visits every node reachable from root, depth-first; fn returning false
// prunes the subtree. goja's ast ships no visitor, so this one is reflective:
// it follows exported pointer, slice and interface fields whose values are
// ast nodes. A function literal's DeclarationList aliases bindings already in
// its body and is skipped so nothing is visited twice.
func walk(root ast.Node, fn func(ast.Node) bool) {
	walkValue(reflect.ValueOf(root), fn, false)
}

// walkShallow is walk without descending into nested function bodies — the
// statements of one function only.
func walkShallow(root ast.Node, fn func(ast.Node) bool) {
	walkValue(reflect.ValueOf(root), fn, true)
}

var nodeType = reflect.TypeOf((*ast.Node)(nil)).Elem()

func walkValue(v reflect.Value, fn func(ast.Node) bool, shallow bool) {
	if !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return
		}
		walkValue(v.Elem(), fn, shallow)
	case reflect.Ptr:
		if v.IsNil() {
			return
		}
		if v.Type().Implements(nodeType) {
			n := v.Interface().(ast.Node)
			if isFunction(n) && shallow && !isRoot(v) {
				return
			}
			if !fn(n) {
				return
			}
		}
		walkValue(v.Elem(), fn, shallow)
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() || f.Name == "DeclarationList" {
				continue
			}
			walkValue(v.Field(i), fn, shallow)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			walkValue(v.Index(i), fn, shallow)
		}
	}
}

func isFunction(n ast.Node) bool {
	switch n.(type) {
	case *ast.FunctionLiteral, *ast.ArrowFunctionLiteral:
		return true
	}
	return false
}

// isRoot marks the value walkShallow started from so a function-literal root
// is entered while nested ones are not.
var rootPtr uintptr

func isRoot(v reflect.Value) bool { return v.Pointer() == rootPtr }

func init() {
	// walkShallow is only ever rooted at a BlockStatement in this gate; the
	// root check exists so a future caller rooting it at a function literal
	// gets the body rather than nothing.
	rootPtr = 0
}

// runSelfTest pins the judgement on a fixture: the transport derivation
// (`submitOp` passes the POST literal; `api` only forwards its caller's
// options and `appGet` never writes — neither is marked), an assertive catch beside a ceremony
// submit (must fail), an assertive catch on a form-load try that reaches no
// transport (must pass — "could not" is exact there), a passthrough catch
// (must pass), a catch with the vocabulary — non-ASCII, so the UTF-16 reading
// is pinned too (must pass), an assertive catch in a function that names no
// ceremony op (must pass), and a nested arrow's catch that is not attributed
// to the enclosing ceremony function.
func runSelfTest(verbose bool) {
	const fixture = `
async function api(path, opts) {
  const res = await fetch(path, opts);
  return res.json();
}
async function appGet(path) { return api(path); }
async function submitOp(body) {
  return api("/api/op", { method: "POST", body: JSON.stringify(body) });
}
async function newGuest() {
  try {
    const reply = await submitOp({ operationType: "CreateUnclaimedIdentity", class: "identity", payload });
    if (reply.status === "rejected") { toast("Could not create guest — " + reply.error.message); return; }
  } catch (e) {
    toast("Could not create guest: " + e.message, false);
  }
}
async function rotate() {
  let handle;
  try { const { renderOpForm } = await loadForm(); handle = renderOpForm(catalog.RotateClaimKey); } catch (e) { toast("Could not re-issue: " + e.message); return; }
  let envelope;
  try { envelope = await handle.submit(); } catch (e) { toast(e.message); return; }
  try {
    await submitOp(envelope);
  } catch (e) {
    toast("Could not confirm the re-issue reached the server — it may have landed; issue a fresh one if it did.");
  }
}
async function ordinary() {
  try { await submitOp({ operationType: "Charge" }); } catch (e) { toast("Could not charge: " + e.message); }
}
async function outer() {
  const inner = async () => { try { await x(); } catch (e) { toast("failed to sync"); } };
  await submitOp({ operationType: "RevokeIdentityClaim" });
  try { await inner(); } catch (e) { toast(e.message); }
}
async function generic(desc) {
  try {
    const reply = await submitOp(desc.envelope);
    revealCeremonySecret(desc.reveal, reply);
  } catch (e) {
    toast("Could not complete: " + e.message);
  }
}
async function unrelated() {
  try { await y(); } catch (e) { toast("Could not — " + e.message); }
}
`
	fs := &file.FileSet{}
	prog, err := parser.ParseFile(fs, "fixture.js", fixture, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint-ceremony-throw-path: SELFTEST parse error: %v\n", err)
		os.Exit(2)
	}
	ops := map[string]bool{"CreateUnclaimedIdentity": true, "RotateClaimKey": true, "RevokeIdentityClaim": true}
	transports := writeTransports(prog)
	if !transports["submitOp"] || transports["appGet"] || transports["api"] {
		fmt.Fprintf(os.Stderr, "lint-ceremony-throw-path: SELFTEST write transports must include submitOp (it passes the POST literal) and neither appGet nor the pass-through api, got %v\n", transports)
		os.Exit(2)
	}
	var st stats
	findings := checkProgram("fixture.js", fs, prog, ops, transports, &st)
	if len(findings) != 2 {
		fmt.Fprintf(os.Stderr, "lint-ceremony-throw-path: SELFTEST want exactly 2 findings (newGuest's catch, and the generic dispatcher that calls revealCeremonySecret), got %d: %v\n", len(findings), findings)
		os.Exit(2)
	}
	if !strings.Contains(findings[1], "generic") {
		fmt.Fprintf(os.Stderr, "lint-ceremony-throw-path: SELFTEST second finding must be the generic dispatcher's catch: %s\n", findings[1])
		os.Exit(2)
	}
	if !strings.Contains(findings[0], "fixture.js:14") || !strings.Contains(findings[0], "newGuest") {
		fmt.Fprintf(os.Stderr, "lint-ceremony-throw-path: SELFTEST first finding must be newGuest's catch at line 14: %s\n", findings[0])
		os.Exit(2)
	}
	if st.catches != 3 {
		fmt.Fprintf(os.Stderr, "lint-ceremony-throw-path: SELFTEST want 3 catches examined (newGuest's, and rotate's submitOp try — rotate's form-load and envelope-build tries reach no transport; outer's try awaits a nested arrow that is not a transport; unrelated names no ceremony op), got %d\n", st.catches)
		os.Exit(2)
	}
	if st.functions != 4 {
		fmt.Fprintf(os.Stderr, "lint-ceremony-throw-path: SELFTEST want 4 ceremony functions (newGuest, rotate, outer, generic), got %d\n", st.functions)
		os.Exit(2)
	}
	if verbose {
		fmt.Printf("lint-ceremony-throw-path: selftest ok — %d catches examined\n", st.catches)
	}
}
