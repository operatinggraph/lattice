//go:build ignore

// lint-conventions.go — static check for Lattice code conventions (CLAUDE.md
// "Code conventions"). Run via `make lint-conventions` or
//
//	go run ./scripts/lint-conventions.go [files...]
//
// With no file arguments it scans all git-tracked .go files. With --strict (or
// STRICT=1) it exits non-zero when any violation is found; otherwise it is
// advisory (prints findings, exits 0) so it can run as a non-blocking
// PostToolUse hook.
//
// Edit-time hook mode:
//
//	go run ./scripts/lint-conventions.go --hook
//
// reads a Claude Code PostToolUse payload from stdin, scans the single file the
// edit touched (tool_input.file_path), prints any findings to stderr, and
// always exits 0 — advisory, never blocks the edit. Wire it into a (gitignored)
// .claude/settings.json PostToolUse matcher on Edit|Write|MultiEdit so the same
// checks CI enforces at STRICT also surface the moment a file is edited:
//
//	"hooks": {
//	  "PostToolUse": [{
//	    "matcher": "Edit|Write|MultiEdit",
//	    "hooks": [{ "type": "command",
//	      "command": "go run ./scripts/lint-conventions.go --hook" }]
//	  }]
//	}
//
// Checks (v0 — highest-value, lowest-false-positive):
//   - History/changelog comments — git blame + the commit message are the
//     record. This is the single most-violated rule (CLAUDE.md).
//   - `asp.` key prefix in a Go string literal — aspects are 4-segment
//     vtx.<type>.<id>.<localName>, never an asp.* prefix (Contract #1).
//   - P5 — a vertical application cmd reading Core KV directly. Architecture P5:
//     "Lenses are the only application query surface; applications never read
//     Core KV directly for queries." A cmd/<name> outside the platform/admin
//     allowlist (Loupe-the-inspector et al.) that references the core-kv bucket
//     must instead read a lens projection. The signal is the bucket, not the
//     call: an app may read a lens TARGET bucket via KVGet/KVListKeys.
//   - P7 — a discriminator-shaped aspect. Architecture P7: "a vertex's
//     type/subtype discriminator is the envelope `class`, never a `.class`/shadow
//     aspect." A package script emitting an aspect whose localName is `class` /
//     `family` / `kind` shadows the envelope class — the type belongs on the
//     vertex `class` field, discovered behind a fine-grained class by the step-6
//     instanceOf-chain resolver (Contract #1 §1.5). The signal is anchored on the
//     Starlark aspect-emit helper, so a discriminator word used as a CLI flag, a
//     string-slice element, or an aspect's `cls` arg is not flagged.
//   - Read-posture classification (Contract #2 §2.5; BLOCKING — fails
//     --strict, per the script-read-posture design §13's flip once the
//     platform + verticals sweeps closed the debt list). Every script
//     `kv.Read(` / `kv.Links(` call site in a
//     packages/ non-test file must carry a `# read-posture: (a|c|d|e|f)`
//     Starlark annotation on the call line or within the preceding lines:
//     (a) required read declared in contextHint.reads by the dispatcher (the
//     key's absence is a correctness error — annotate the site's own
//     dispatcher(s) rather than leaving it silently debt-classed once
//     declared);
//     (c) deliberately-unsnapshotted config read (annotate why);
//     (d) absence-tolerant read declared in contextHint.optionalReads by the
//     dispatcher (read-before-create / dedup);
//     (e) bounded kv.Links enumeration — the annotation names `relation=` and
//     records `epoch=` (the companion class-(a) serialization key an
//     enumerate-then-write contends, or an explicit `epoch=none (…)`
//     acceptance — best-effort; Weaver detect+recover enforces);
//     a per-element follow-up kv.Read off an enumeration is also (e);
//     (f) required read declared in contextHint.egressReads by the dispatcher
//     (sensitive-param-egress design §3.1) — fail-closed like (a), except a
//     sensitive-DDL key hydrates as a `$sensitiveRef` marker, never plaintext.
//     An UNANNOTATED call is flagged class-(b) — a declarable-but-undeclared
//     lazy read, the read posture's only debt class. Same posture as
//     TestPackage_NoScans, extended from "no raw scans" to "declare (or
//     classify) your declarable reads".
//   - authContext.target shape declaration (BLOCKING;
//     authcontext-target-validated-primitive-design.md §5.5/§8). `authContext.target`
//     is forwarded verbatim from the client and step 3 authorizes a scope=any
//     grant WITHOUT inspecting it, so it is an unchecked hint on every path the
//     platform did not validate. A guard that EXEMPTS on target presence
//     (`op.authContextTarget != ""`) is therefore forgeable by any scope=any
//     holder — and that is the shape an author writes by default. The gate does
//     not try to classify a site: it default-DENIES every `op.authContextTarget`
//     reference in a packages/ non-test file and requires the author to declare
//     which safe shape it is, mirroring the `# read-posture:` convention:
//     (ownership) the value derives an identity whose ownership of the acted-on
//     resource is then proven by a graph link — a forged target only forces a
//     stricter proof;
//     (payload-bind) the value must equal a payload field naming the subject of
//     a CREATE, where no owning link exists to probe yet — a forged target only
//     narrows what the caller may create;
//     (resource-bind) the validated target is bound to the resource the op acts
//     on (`op.authTargetValidated and op.authContextTarget == <resourceKey>`) —
//     the annotated line must itself carry `op.authTargetValidated`;
//     (selector) a non-security branch selector, no confinement rides on it;
//     (legacy-self-exempt) an exemption keyed on `== op.actor`, admitted only in
//     the files authCtxTargetLegacyFiles names.
//     There is deliberately NO shape for an exemption keyed on target presence:
//     the correct spelling of an exemption is `op.authTargetValidated`, the
//     platform bit that is true only when step 3 CHECKED the target. Every shape
//     needs a trailing `<why>` — declaring is cheap, forgetting fails closed.
//     Two residuals both gates share with the `# read-posture:` convention they
//     mirror, stated so nobody reads more into a green run than is there. (1) An
//     annotation covers the following `readPostureWindow` lines, so a reference
//     inserted INTO an already-annotated block inherits that block's
//     declaration. (2) The gate is fail-closed against FORGETTING to declare,
//     not against MIS-declaring: only (resource-bind) and (legacy-self-exempt)
//     carry a structural check, so a wrong (selector) or (ownership) passes. The
//     author's `<why>` is what a reviewer reads; the gate only guarantees one
//     was written.
//   - Workplace-exemption discharge (BLOCKING; same design §3.4.1). A validated
//     target proves the target was CHECKED, not that it IS the resource the op
//     writes — `workplace_exempt()` returning true on a grant scoped to resource
//     A does not stop the caller pairing it with a payload naming resource B.
//     Every CALL to a package's workplace_exempt() helper must therefore declare
//     what closes that gap: `# workplace-exempt: (no-validated-path) <why>` (the
//     op grants no scope=self and mints no task, so only an operator ever
//     reaches the exemption — an operational fact the annotation forces the
//     author to re-check, since a task minted for the op later invalidates it),
//     (ownership-bound) (a downstream ownership proof requires the target to own
//     the resource), or (resource-bind) (an explicit
//     `op.authContextTarget == <resourceKey>` comparison).
//   - Protected-by-default gate (Contract #6 §6.14). A non-test pkgmgr.LensSpec
//     composite literal declaring `Adapter: "postgres"` must also declare one of
//     Protected, Public, or GrantTable — a postgres business read model is
//     protected by default, and an undeclared posture must fail closed rather
//     than silently activate as a plain unguarded table. Mirrors the same gate
//     Refractor's translateSpec and pkgmgr's validateLensReadPath enforce at
//     runtime/install-time; this is the earliest (edit-time) tripwire.
//   - Per-test primordial-globals repopulation (bootstrap-primordial-globals-
//     race-design.md §4). A `bootstrap.LoadOrGenerate(` call in a `*_test.go`
//     file outside `internal/bootstrap/` and `internal/testutil/` re-populates
//     internal/bootstrap's ~64 package-level globals per test, which races
//     under t.Parallel() and silently stomps another test's in-flight ID set —
//     use `testutil.EnsurePrimordials(t)` instead (populates once per test
//     process, mirroring every production binary's boot-once lifecycle).
//     `internal/pkgmgr/installer_test.go` is exempted: it is `package pkgmgr`
//     (internal test), and testutil imports pkgmgr, so importing testutil
//     there closes an import cycle — it stays on the direct call.
//     `bootstrap.Load` (read-only, no globals repopulation) is un-gated:
//     hellolattice's live-stack load and one-shot scripts are legitimate.
//
// Markdown/docs are intentionally out of scope: they discuss the conventions
// (e.g. "never an asp.* prefix") and would false-positive. The 6-segment link
// check is deferred to v1 — naive matching collides with legitimate `"lnk."`
// key-builder prefix constants.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	historyComment = regexp.MustCompile(`//[ \t]*(Story [0-9]|Previously\b|Was:|Replaces\b|renamed from|moved from|formerly\b)`)
	aspPrefix      = regexp.MustCompile(`"asp\.`)
	coreKVRead     = regexp.MustCompile(`\bCoreKVBucket\b|"core-kv"`)
	// p7Discriminator — a package script emitting a discriminator-shaped aspect (a
	// `.class` / `.family` / `.kind` localName that shadows the envelope `class`).
	// Anchored on the Starlark aspect-emit helper so a discriminator word used
	// elsewhere (a CLI flag, a string-slice element, an aspect's `cls` arg) is not
	// flagged: it matches only when the discriminator is the *localName* — the
	// helper's localName arg is always immediately followed by the `cls` string
	// literal, regardless of whether the helper takes the vertex key as one arg or
	// two (make_aspect, make_aspect_upsert(_occ), make_update_aspect).
	p7Discriminator = regexp.MustCompile(`make_(aspect|update_aspect|aspect_upsert|aspect_upsert_occ)\(.*"(class|family|kind)",\s*"`)
	// Read-posture classification (Contract #2 §2.5). kvCall anchors a script
	// kv.Read/kv.Links CALL (a paren after the name), so prose mentions in
	// comments don't match; readPosture is the classification annotation the
	// call must carry on its line or within the preceding window.
	kvCall        = regexp.MustCompile(`kv\.(Read|Links)\(`)
	kvLinksCall   = regexp.MustCompile(`kv\.Links\(`)
	readPosture   = regexp.MustCompile(`#\s*read-posture:\s*\(([acdef])\)`)
	scriptMutates = regexp.MustCompile(`"op":\s*"(create|update|tombstone)"|make_(vtx|link|aspect|update)`)
	// lensAdapterPostgres anchors a pkgmgr.LensSpec composite literal's Adapter
	// field declaring "postgres" (Contract #6 §6.14: a postgres business read
	// model is protected by default). lensPostureFlag matches any of the three
	// postures a lens entry may declare to opt out of the fail-closed default.
	lensAdapterPostgres = regexp.MustCompile(`Adapter:\s*"postgres"`)
	lensPostureFlag     = regexp.MustCompile(`\b(Protected|Public|GrantTable):`)
	// loadOrGenerateCall anchors a bootstrap.LoadOrGenerate call site (the
	// per-test-populate hazard bootstrap-primordial-globals-race-design.md §4
	// closes via testutil.EnsurePrimordials).
	loadOrGenerateCall = regexp.MustCompile(`bootstrap\.LoadOrGenerate\(`)
	// authContext.target shape declaration (authcontext-target-validated-
	// primitive-design.md §5.5). authCtxTargetRef anchors any script reference
	// to the forwarded, unvalidated field; authCtxTargetShape is the annotation
	// the reference must carry, capturing the shape and its trailing `<why>`.
	authCtxTargetRef   = regexp.MustCompile(`\bop\.authContextTarget\b`)
	authCtxTargetShape = regexp.MustCompile(`#\s*authcontext-target:\s*\(([a-z-]+)\)(.*)$`)
	// authTargetValidatedRef is the platform bit a (resource-bind) declaration
	// must pair the comparison with — the whole point of that shape is that the
	// target was CHECKED and then bound to the acted-on resource.
	authTargetValidatedRef = regexp.MustCompile(`\bop\.authTargetValidated\b`)
	// A Starlark `def <name>(` line, and a call to one. The exemption-helper set
	// is DERIVED per file rather than listed: a helper whose body consults the
	// validated bit (or the raw target) IS a validated-target exemption, whatever
	// it is named, so a new package inventing its own `staff_exempt()` is covered
	// on the same terms as the shipped `workplace_exempt` / `require_workplace` /
	// `enforce_workplace` trio. A hardcoded name list would have been exactly the
	// fingers-crossed state this gate exists to end.
	starlarkDef          = regexp.MustCompile(`^(\s*)def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	workplaceExemptShape = regexp.MustCompile(`#\s*workplace-exempt:\s*\(([a-z-]+)\)(.*)$`)
)

// workplaceExemptShapes are the ways a call site may discharge §3.4.1: the op
// admits no legitimately-validated target at all, a downstream ownership proof
// binds the target to the resource, an explicit comparison does, or — for a
// helper that itself calls another exemption helper — the discharge belongs to
// each of ITS call sites in turn.
var workplaceExemptShapes = map[string]bool{
	"no-validated-path": true,
	"ownership-bound":   true,
	"resource-bind":     true,
	"per-call-site":     true,
}

// authCtxTargetShapes are the declarations a packages/ script may make about an
// `op.authContextTarget` reference. An EXEMPTION is deliberately absent: the
// correct spelling of one is `op.authTargetValidated`.
var authCtxTargetShapes = map[string]bool{
	"ownership":          true,
	"payload-bind":       true,
	"resource-bind":      true,
	"selector":           true,
	"legacy-self-exempt": true,
}

// authCtxTargetLegacyFiles are the files whose confinement exemption still keys
// on `op.authContextTarget == op.actor` rather than the platform's
// `op.authTargetValidated` bit. The two predicates do NOT agree on every path —
// a scope=any caller naming its own actor key satisfies the equality and is
// exempted where the platform bit would confine it — so migrating them is a
// behavior change scoped as its own item
// (authcontext-target-validated-primitive-design.md §6), not a cleanup. The
// (legacy-self-exempt) shape is confined to this list so the next author cannot
// reach for it: a new guard has no legacy to declare.
var authCtxTargetLegacyFiles = map[string]bool{
	"packages/clinic-domain/ddls.go": true,
}

// loadOrGenerateExemptFile is the one test file that legitimately keeps the
// direct bootstrap.LoadOrGenerate call: internal/pkgmgr/installer_test.go is
// `package pkgmgr` (internal test), and internal/testutil imports pkgmgr
// (install_phase1_packages.go), so testutil.EnsurePrimordials(t) would close
// an import cycle there.
const loadOrGenerateExemptFile = "internal/pkgmgr/installer_test.go"

// readPostureWindow is how many lines above a kv.Read/kv.Links call the
// `# read-posture:` annotation may sit (the call's own comment block).
const readPostureWindow = 8

// platformCmds are the platform / admin / debug-inspector binaries that
// legitimately touch Core KV — the platform components ARE the system, and P5
// carves out admin/debug inspection (Loupe, the lattice CLI). Any OTHER
// cmd/<name> is a vertical application, which P5 forbids from reading Core KV
// directly: it must read a lens projection in a read-model target.
var platformCmds = map[string]bool{
	"bootstrap": true, "bridge": true, "chronicler": true, "lattice": true, "lattice-pkg": true,
	"loom": true, "loupe": true, "object-store-manager": true,
	"processor": true, "refractor": true, "weaver": true,
}

// verticalAppCmd returns the app name when path is a non-test .go file under a
// cmd/<name> that is NOT a platform binary, else "". Such a cmd is an
// application query surface bound by P5.
func verticalAppCmd(path string) string {
	if strings.HasSuffix(path, "_test.go") {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, p := range parts {
		if p == "cmd" && i+1 < len(parts) {
			if platformCmds[parts[i+1]] {
				return ""
			}
			return parts[i+1]
		}
	}
	return ""
}

type finding struct {
	file string
	line int
	msg  string
	// warn marks an advisory finding (the read-posture checks, which land
	// warn-first per script-read-posture-design §7): printed, surfaced in the
	// hook, but never fails --strict.
	warn bool
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--hook" {
		runHook()
		return
	}

	if failures := selfTest(); len(failures) > 0 {
		for _, f := range failures {
			fmt.Fprintln(os.Stderr, "lint-conventions self-test: "+f)
		}
		fmt.Fprintf(os.Stderr, "lint-conventions: %d self-test failure(s) — the gate does not behave as documented\n", len(failures))
		os.Exit(2)
	}

	strict := os.Getenv("STRICT") == "1"
	var files []string
	for _, a := range os.Args[1:] {
		if a == "--strict" {
			strict = true
			continue
		}
		files = append(files, a)
	}
	if len(files) == 0 {
		files = trackedGoFiles()
	}

	var findings []finding
	for _, f := range files {
		if !strings.HasSuffix(f, ".go") {
			continue
		}
		findings = append(findings, scanFile(f)...)
	}

	var issues, warnings int
	for _, fd := range findings {
		if fd.warn {
			warnings++
			fmt.Printf("%s:%d: warn: %s\n", fd.file, fd.line, fd.msg)
			continue
		}
		issues++
		fmt.Printf("%s:%d: %s\n", fd.file, fd.line, fd.msg)
	}
	if len(findings) == 0 {
		fmt.Println("lint-conventions: 0 issues")
		return
	}
	fmt.Printf("lint-conventions: %d issue(s), %d advisory warning(s)\n", issues, warnings)
	if strict && issues > 0 {
		os.Exit(1)
	}
}

// runHook reads a Claude Code PostToolUse payload from stdin and scans the one
// file the edit touched. It is advisory: any parse/read trouble is swallowed and
// it always exits 0, so a malformed payload or an unrelated tool never blocks an
// edit. Findings are fed back to the editing agent via a PostToolUse
// hookSpecificOutput.additionalContext object on stdout (ignored harmlessly by a
// harness that predates that field) and mirrored to stderr for the human.
func runHook() {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return
	}
	var payload struct {
		ToolInput struct {
			FilePath string `json:"file_path"`
		} `json:"tool_input"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return
	}
	path := payload.ToolInput.FilePath
	if path == "" || !strings.HasSuffix(path, ".go") {
		return
	}
	findings := scanFile(path)
	if len(findings) == 0 {
		return
	}

	var b strings.Builder
	var issues, warnings int
	for _, fd := range findings {
		if fd.warn {
			warnings++
			fmt.Fprintf(&b, "%s:%d: warn: %s\n", fd.file, fd.line, fd.msg)
			continue
		}
		issues++
		fmt.Fprintf(&b, "%s:%d: %s\n", fd.file, fd.line, fd.msg)
	}
	switch {
	case issues > 0:
		fmt.Fprintf(&b, "lint-conventions: %d convention issue(s) (+%d advisory warning(s)) in the file you just edited — fix the issues before commit (CI enforces STRICT).", issues, warnings)
	default:
		fmt.Fprintf(&b, "lint-conventions: %d advisory read-posture warning(s) in the file you just edited — classify or declare the reads when convenient (advisory; does not fail CI).", warnings)
	}
	msg := b.String()

	fmt.Fprintln(os.Stderr, msg)

	out, err := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "PostToolUse",
			"additionalContext": msg,
		},
	})
	if err != nil {
		return
	}
	fmt.Println(string(out))
}

func scanFile(path string) []finding {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return scanSource(path, data)
}

func scanSource(path string, data []byte) []finding {
	var out []finding
	app := verticalAppCmd(path)
	isTest := strings.HasSuffix(path, "_test.go")
	slash := filepath.ToSlash(path)
	// Read-posture classification applies to shipped package scripts only:
	// packages/ non-test .go files (Starlark sources live there as Go string
	// constants). Tests, engines, and harnesses are out of scope.
	postureScoped := !isTest && (strings.HasPrefix(slash, "packages/") || strings.Contains(slash, "/packages/"))
	fileMutates := postureScoped && scriptMutates.Match(data)
	if !isTest {
		out = append(out, checkLensProtectedByDefault(path, string(data))...)
	}
	loadOrGenerateScoped := isTest &&
		!strings.HasPrefix(slash, "internal/bootstrap/") &&
		!strings.HasPrefix(slash, "internal/testutil/") &&
		slash != loadOrGenerateExemptFile
	// The file's own validated-target exemption helpers, derived from the
	// script text rather than a hardcoded name list (see exemptionHelpers).
	var exemptHelpers map[string]bool
	if postureScoped {
		exemptHelpers = exemptionHelpers(data)
	}
	// window holds the last readPostureWindow raw lines, for locating a
	// `# read-posture:` annotation in the call's own comment block.
	var window []string
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	ln := 0
	for sc.Scan() {
		ln++
		line := sc.Text()
		if historyComment.MatchString(line) {
			out = append(out, finding{file: path, line: ln, msg: "history/changelog comment — git blame + the commit message are the record"})
		}
		if aspPrefix.MatchString(line) {
			out = append(out, finding{file: path, line: ln, msg: "`asp.` key prefix — aspects are 4-segment vtx.<type>.<id>.<localName> (Contract #1)"})
		}
		if app != "" && coreKVRead.MatchString(line) {
			out = append(out, finding{file: path, line: ln, msg: "P5 violation — application cmd/" + app + " reads Core KV directly; an application reads lens projections, never Core KV (lattice-architecture.md P5)"})
		}
		if !isTest && p7Discriminator.MatchString(line) {
			out = append(out, finding{file: path, line: ln, msg: "P7 violation — discriminator aspect (.class/.family/.kind) shadows the envelope class; the type belongs on the vertex class field, resolved behind a fine-grained class by the step-6 instanceOf chain (lattice-architecture.md P7, Contract #1 §1.5)"})
		}
		if loadOrGenerateScoped && loadOrGenerateCall.MatchString(line) {
			out = append(out, finding{file: path, line: ln, msg: "per-test bootstrap.LoadOrGenerate — re-populates internal/bootstrap's globals per test, which races under t.Parallel(); use testutil.EnsurePrimordials(t) instead (bootstrap-primordial-globals-race-design.md §4)"})
		}
		if postureScoped {
			out = append(out, checkReadPosture(path, ln, line, window, fileMutates)...)
			out = append(out, checkAuthContextTarget(path, ln, line, window)...)
			out = append(out, checkWorkplaceExempt(path, ln, line, window, exemptHelpers)...)
		}
		window = append(window, line)
		if len(window) > readPostureWindow {
			window = window[1:]
		}
	}
	return out
}

// lensScanWindow bounds how far checkLensProtectedByDefault walks backward/
// forward from an Adapter match to find its composite literal's enclosing
// braces — a safety cap against pathological input, well beyond any real
// LensSpec entry's size.
const lensScanWindow = 8000

// checkLensProtectedByDefault flags a pkgmgr.LensSpec composite literal that
// declares `Adapter: "postgres"` but none of Protected, Public, or GrantTable
// (Contract #6 §6.14: a postgres business read model is protected by
// default, and undeclared posture must fail closed rather than silently
// activate as a plain unguarded table). For each Adapter match it walks
// backward to the entry's own opening `{` and forward to its matching `}`
// via balanced-brace counting (correct regardless of single-line vs
// multi-line literal formatting, and regardless of neighboring entries in
// the same slice), then checks only that span for a posture flag. A
// *balanced* pair of braces inside a string field (e.g. a cypher `Spec`
// literal like `MATCH (u:unit {status: "x"})`) is handled correctly — the
// walk simply treats it as (harmless) nesting. Known limitation: an ODD
// (unbalanced) brace count inside a plain string field between the Adapter
// line and the entry's true close — e.g. a stray `{` or `}` in prose or a
// JSON snippet — throws the walk off in either direction: it can overshoot
// into a later entry and borrow its posture flag (false negative, a real
// violation goes unreported) or close early on the stray brace before a
// real posture flag further down (false positive on a correctly-declared
// lens). No lens in this codebase has one today (Spec fields reference
// named consts, not inline literals with stray braces), so this is accepted
// as a non-AST scanner's residual risk rather than justifying a full
// go/parser rewrite for what CLAUDE.md's own design intent for this file is
// a pragmatic, highest-value/lowest-false-positive check.
func checkLensProtectedByDefault(path, src string) []finding {
	var out []finding
	for _, m := range lensAdapterPostgres.FindAllStringIndex(src, -1) {
		pos := m[0]
		lineStart := strings.LastIndexByte(src[:pos], '\n') + 1
		lineEnd := strings.IndexByte(src[pos:], '\n')
		if lineEnd == -1 {
			lineEnd = len(src)
		} else {
			lineEnd += pos
		}
		if strings.HasPrefix(strings.TrimSpace(src[lineStart:lineEnd]), "//") {
			continue
		}
		backLimit := pos - lensScanWindow
		if backLimit < 0 {
			backLimit = 0
		}
		entryStart := -1
		balance := 0
		for i := pos - 1; i >= backLimit; i-- {
			switch src[i] {
			case '}':
				balance++
			case '{':
				if balance == 0 {
					entryStart = i
				} else {
					balance--
				}
			}
			if entryStart != -1 {
				break
			}
		}
		if entryStart == -1 {
			continue
		}
		fwdLimit := pos + lensScanWindow
		if fwdLimit > len(src) {
			fwdLimit = len(src)
		}
		entryEnd := -1
		balance = 1
		for i := entryStart + 1; i < fwdLimit; i++ {
			switch src[i] {
			case '{':
				balance++
			case '}':
				balance--
				if balance == 0 {
					entryEnd = i
				}
			}
			if entryEnd != -1 {
				break
			}
		}
		if entryEnd == -1 {
			continue
		}
		if !lensPostureFlag.MatchString(src[entryStart : entryEnd+1]) {
			line := strings.Count(src[:pos], "\n") + 1
			out = append(out, finding{file: path, line: line, msg: "lens declares Adapter: \"postgres\" but neither Protected, Public, nor GrantTable — a postgres business read model is protected by default and undeclared posture fails closed at activation (Contract #6 §6.14)"})
		}
	}
	return out
}

// checkReadPosture classifies one script kv.Read/kv.Links call line against
// the Contract #2 §2.5 read posture (all findings BLOCKING — warn:false).
// window is the preceding raw lines; the annotation may sit there or on the
// call line itself. Comment lines (Go `//` or Starlark `#`) are skipped —
// prose ABOUT kv.Read is not a call.
func checkReadPosture(path string, ln int, line string, window []string, fileMutates bool) []finding {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
		return nil
	}
	if !kvCall.MatchString(line) {
		return nil
	}
	// Locate the nearest annotation: the call line first, then the window
	// bottom-up (the closest preceding comment wins).
	var class string
	var annotated string
	if m := readPosture.FindStringSubmatch(line); m != nil {
		class, annotated = m[1], line
	} else {
		for i := len(window) - 1; i >= 0; i-- {
			if m := readPosture.FindStringSubmatch(window[i]); m != nil {
				class, annotated = m[1], window[i]
				break
			}
		}
	}
	isLinks := kvLinksCall.MatchString(line)
	if class == "" {
		call := "kv.Read"
		if isLinks {
			call = "kv.Links"
		}
		return []finding{{file: path, line: ln, warn: false,
			msg: "read-posture: unclassified " + call + " — class-(b) debt (Contract #2 §2.5). Declare the key in contextHint reads/optionalReads/egressReads and annotate the call: `# read-posture: (a) <declared-by>` (required read declared in contextHint.reads), `(c) <why>` (config, deliberately live), `(d) <declared-by>` (declared optionalReads), `(e) relation=<rel> epoch=<key|none (…)>` (bounded enumeration / its follow-up read), or `(f) <declared-by>` (declared egressReads — sensitive-param-egress design §3.1)"}}
	}
	var out []finding
	if isLinks {
		if class != "e" {
			out = append(out, finding{file: path, line: ln, warn: false,
				msg: "read-posture: kv.Links must be class (e) — a bounded paged enumeration, declared as contextHint.enumerations metadata (Contract #2 §2.5)"})
		} else {
			if !strings.Contains(annotated, "relation=") {
				out = append(out, finding{file: path, line: ln, warn: false,
					msg: "read-posture: a class-(e) kv.Links annotation must name `relation=<rel>` (matches the dispatcher's contextHint.enumerations declaration, Contract #2 §2.5)"})
			}
			if fileMutates && !strings.Contains(annotated, "epoch=") {
				out = append(out, finding{file: path, line: ln, warn: false,
					msg: "read-posture: enumerate-then-write without a companion epoch — record `epoch=<key>` (a class-(a) serialization key every mutator of the relation bumps, declared in reads) or an explicit `epoch=none (<accepted-risk>)`; best-effort contention reduction, Weaver detect+recover enforces (Contract #2 §2.5)"})
			}
		}
	}
	return out
}

// checkAuthContextTarget default-denies one script reference to
// `op.authContextTarget` unless the author declared its shape
// (authcontext-target-validated-primitive-design.md §5.5; all findings
// BLOCKING). window is the preceding raw lines; the annotation may sit there or
// on the reference line itself. Comment lines are skipped — prose ABOUT the
// field is not a use of it.
func checkAuthContextTarget(path string, ln int, line string, window []string) []finding {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
		return nil
	}
	if !authCtxTargetRef.MatchString(line) {
		return nil
	}
	const remedy = "The correct spelling of a confinement EXEMPTION is `op.authTargetValidated` — " +
		"the platform bit that is true only when step 3 CHECKED the target (scope=self proved " +
		"target == actor; a task grant proved its scopedTo == authContext.target). Otherwise declare " +
		"the shape on the line or in its comment block: `# authcontext-target: (ownership) <why>` " +
		"(the value derives an identity whose ownership of the acted-on resource is then proven by a " +
		"graph link), `# authcontext-target: (resource-bind) <why>` (a validated target bound to the " +
		"resource the op acts on — the same line must carry op.authTargetValidated), or " +
		"`# authcontext-target: (selector) <why>` (a non-security branch selector)."

	var shape, why string
	if m := authCtxTargetShape.FindStringSubmatch(line); m != nil {
		shape, why = m[1], m[2]
	} else {
		for i := len(window) - 1; i >= 0; i-- {
			if m := authCtxTargetShape.FindStringSubmatch(window[i]); m != nil {
				shape, why = m[1], m[2]
				break
			}
		}
	}
	if shape == "" {
		return []finding{{file: path, line: ln, warn: false,
			msg: "authcontext-target: undeclared op.authContextTarget reference — authContext.target is " +
				"forwarded verbatim from the client and step 3 authorizes a scope=any grant WITHOUT " +
				"inspecting it, so exempting on its presence is forgeable. " + remedy}}
	}
	if !authCtxTargetShapes[shape] {
		return []finding{{file: path, line: ln, warn: false,
			msg: "authcontext-target: unknown shape (" + shape + "). " + remedy}}
	}
	var out []finding
	if strings.TrimSpace(why) == "" {
		out = append(out, finding{file: path, line: ln, warn: false,
			msg: "authcontext-target: a (" + shape + ") declaration must state its `<why>` — what binds " +
				"this reference, in the author's own words"})
	}
	switch shape {
	case "resource-bind":
		if !authTargetValidatedRef.MatchString(line) {
			out = append(out, finding{file: path, line: ln, warn: false,
				msg: "authcontext-target: a (resource-bind) declaration must pair the comparison with " +
					"op.authTargetValidated on the same line — binding an UNVALIDATED target to the " +
					"acted-on resource proves nothing about who may act on it (design §3.4.1)"})
		}
	case "legacy-self-exempt":
		if !authCtxTargetLegacyFiles[repoRelPackagePath(path)] {
			out = append(out, finding{file: path, line: ln, warn: false,
				msg: "authcontext-target: (legacy-self-exempt) is admitted only in the files " +
					"authCtxTargetLegacyFiles names — a new guard has no legacy to declare. " + remedy})
		}
	}
	return out
}

// repoRelPackagePath normalizes a path to its repo-relative `packages/…` form.
// The hook (`--hook`) is handed an ABSOLUTE path by the editor while CI passes
// `git ls-files` output, and a lookup keyed on the repo-relative spelling
// otherwise misses under the hook — turning a correctly-declared site into a
// blocking finding that pushes an editing agent to "fix" code that is right.
func repoRelPackagePath(path string) string {
	slash := filepath.ToSlash(path)
	if i := strings.LastIndex(slash, "/packages/"); i >= 0 {
		return slash[i+1:]
	}
	return slash
}

// exemptionHelpers derives the file's validated-target exemption helpers from
// the script text: a Starlark `def` whose body consults `op.authTargetValidated`
// or `op.authContextTarget` short-circuits confinement on a caller-influenced
// value, and so does a def that calls one of those (iterated to a fixpoint).
// Deriving the set beats naming it — `workplace_exempt` is a convention, not a
// mechanism, and a new package's own `staff_exempt()` must be gated on the same
// terms rather than escaping because nobody remembered to add it to a list.
func exemptionHelpers(data []byte) map[string]bool {
	type def struct {
		name   string
		indent int
		body   []string
	}
	var defs []def
	var cur *def
	for _, line := range strings.Split(string(data), "\n") {
		if m := starlarkDef.FindStringSubmatch(line); m != nil {
			defs = append(defs, def{name: m[2], indent: len(m[1])})
			cur = &defs[len(defs)-1]
			continue
		}
		if cur == nil {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if len(line)-len(strings.TrimLeft(line, " \t")) <= cur.indent {
			cur = nil
			continue
		}
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
			continue
		}
		cur.body = append(cur.body, line)
	}
	out := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for _, d := range defs {
			if out[d.name] {
				continue
			}
			for _, line := range d.body {
				hit := authTargetValidatedRef.MatchString(line) || authCtxTargetRef.MatchString(line)
				if !hit {
					for name := range out {
						if regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*\(`).MatchString(line) {
							hit = true
							break
						}
					}
				}
				if hit {
					out[d.name] = true
					changed = true
					break
				}
			}
		}
	}
	return out
}

// checkWorkplaceExempt default-denies one call to one of the file's own
// validated-target exemption helpers unless the author declared what binds the
// exempted target to the acted-on resource (authcontext-target-validated-
// primitive-design.md §3.4.1; all findings BLOCKING). Such a helper returns
// true for a VALIDATED target — which proves the target was checked, NOT that
// it is the resource the op writes. Where nothing closes that gap, a caller
// holding a legitimate grant for one resource can act on another.
func checkWorkplaceExempt(path string, ln int, line string, window []string, helpers map[string]bool) []finding {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") ||
		starlarkDef.MatchString(line) {
		return nil
	}
	var called string
	for name := range helpers {
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*\(`).MatchString(line) {
			called = name
			break
		}
	}
	if called == "" {
		return nil
	}
	const remedy = "Declare what discharges it on the line or in its comment block: " +
		"`# workplace-exempt: (no-validated-path) <why>` (the op grants no scope=self and mints no " +
		"task, so op.authTargetValidated is never legitimately true and only an operator can reach " +
		"the exemption — name the grant shape you checked, since a task minted for this op later " +
		"invalidates the claim), `# workplace-exempt: (ownership-bound) <why>` (a downstream " +
		"ownership proof requires the target to own the acted-on resource), " +
		"`# workplace-exempt: (resource-bind) <why>` (an explicit op.authContextTarget == " +
		"<resourceKey> comparison binds it), or `# workplace-exempt: (per-call-site) <why>` (this " +
		"call is inside another exemption helper, so the discharge belongs to ITS call sites)."

	var shape, why string
	if m := workplaceExemptShape.FindStringSubmatch(line); m != nil {
		shape, why = m[1], m[2]
	} else {
		for i := len(window) - 1; i >= 0; i-- {
			if m := workplaceExemptShape.FindStringSubmatch(window[i]); m != nil {
				shape, why = m[1], m[2]
				break
			}
		}
	}
	if shape == "" {
		return []finding{{file: path, line: ln, warn: false,
			msg: "workplace-exempt: undeclared " + called + "() call — a validated target proves the " +
				"target was CHECKED, not that it is the resource this op acts on. " + remedy}}
	}
	if !workplaceExemptShapes[shape] {
		return []finding{{file: path, line: ln, warn: false,
			msg: "workplace-exempt: unknown shape (" + shape + "). " + remedy}}
	}
	if strings.TrimSpace(why) == "" {
		return []finding{{file: path, line: ln, warn: false,
			msg: "workplace-exempt: a (" + shape + ") declaration must state its `<why>` — what binds " +
				"the exempted target to this op's resource, in the author's own words"}}
	}
	return nil
}

// selfTest exercises the authcontext-target gate against fixtures and returns
// one message per behaviour that did not hold. It runs on every non-hook
// invocation (so CI's STRICT run covers it) and is deliberately in-memory: the
// gate's whole value is that it REDS on a reintroduced bare exemption, and a
// gate whose deny path silently stopped working reads exactly like a clean
// codebase. Cheap enough that gating it behind a flag would only invite
// skipping it.
func selfTest() []string {
	const fixture = "packages/self-test/ddls.go"
	// Every fixture names the exact finding it expects (`want`), so a case
	// cannot pass by tripping a DIFFERENT rule's finding than the one it is
	// there to pin — the classic wrong-reason green.
	const helperDef = "def workplace_exempt():\n\treturn op.authTargetValidated\n"
	cases := []struct {
		name string
		path string
		src  string
		want string // substring of the expected finding; "" = expect none
	}{
		{"bare presence exemption is denied", fixture,
			"\treturn op.authContextTarget != \"\" or actor_holds_operator(op.actor)\n",
			"undeclared op.authContextTarget reference"},
		{"bare equality exemption is denied", fixture,
			"\tif op.authContextTarget == op.actor:\n",
			"undeclared op.authContextTarget reference"},
		{"declared ownership passes", fixture,
			"\t# authcontext-target: (ownership) target must own the resource\n" +
				"\tif op.authContextTarget != \"\":\n", ""},
		{"an on-the-line declaration passes", fixture,
			"\tif op.authContextTarget != \"\":  # authcontext-target: (ownership) owns it\n", ""},
		{"declared payload-bind passes", fixture,
			"\t# authcontext-target: (payload-bind) must equal payload.applicant\n" +
				"\tif op.authContextTarget != \"\" and op.authContextTarget != applicant:\n", ""},
		{"declared selector passes", fixture,
			"\t# authcontext-target: (selector) picks the amount source\n" +
				"\tis_self = op.authContextTarget != \"\"\n", ""},
		{"a declaration covers the following lines' derived reads", fixture,
			"\t# authcontext-target: (ownership) target must own the resource\n" +
				"\tif op.authContextTarget != \"\":\n" +
				"\t\t_, tid = parts_of(op.authContextTarget, \"authContextTarget\", \"identity\")\n", ""},
		{"a declaration does NOT reach past the annotation window", fixture,
			"\t# authcontext-target: (ownership) target must own the resource\n" +
				"\tif op.authContextTarget != \"\":\n" +
				"\t\ta = 1\n\t\tb = 2\n\t\tc = 3\n\t\td = 4\n\t\te = 5\n\t\tf = 6\n\t\tg = 7\n\t\th = 8\n" +
				"\tif op.authContextTarget != \"\":\n",
			"undeclared op.authContextTarget reference"},
		{"declaration without a why is denied", fixture,
			"\t# authcontext-target: (ownership)\n\tif op.authContextTarget != \"\":\n",
			"declaration must state its `<why>`"},
		{"unknown shape is denied", fixture,
			"\t# authcontext-target: (exempt) staff are trusted\n" +
				"\tif op.authContextTarget != \"\":\n", "unknown shape (exempt)"},
		{"resource-bind without the validated bit is denied", fixture,
			"\t# authcontext-target: (resource-bind) names this work order\n" +
				"\tbound = op.authContextTarget == wkey\n",
			"must pair the comparison with op.authTargetValidated"},
		{"resource-bind paired with the validated bit passes", fixture,
			"\t# authcontext-target: (resource-bind) names this work order\n" +
				"\tbound = op.authTargetValidated and op.authContextTarget == wkey\n", ""},
		{"legacy-self-exempt outside the named files is denied", fixture,
			"\t# authcontext-target: (legacy-self-exempt) my guard is old too\n" +
				"\tif op.authContextTarget == op.actor:\n",
			"admitted only in the files authCtxTargetLegacyFiles names"},
		{"legacy-self-exempt inside a named file passes", "packages/clinic-domain/ddls.go",
			"\t# authcontext-target: (legacy-self-exempt) backstopped by identifiedBy\n" +
				"\tif op.authContextTarget == op.actor:\n", ""},
		{"legacy-self-exempt passes under an ABSOLUTE path too", "/abs/checkout/packages/clinic-domain/ddls.go",
			"\t# authcontext-target: (legacy-self-exempt) backstopped by identifiedBy\n" +
				"\tif op.authContextTarget == op.actor:\n", ""},
		{"prose about the field is not a use of it", fixture,
			"\t# a caller could forge op.authContextTarget != \"\" here\n" +
				"\t// op.authContextTarget is forwarded verbatim\n", ""},
		{"the gate is scoped to packages/", "internal/gateway/gateway.go",
			"\tac.Target = op.authContextTarget\n", ""},
		{"the gate skips package test files", "packages/self-test/ddls_test.go",
			"\tif op.authContextTarget != \"\":\n", ""},
		{"undeclared exemption-helper call is denied", fixture,
			helperDef + "\tif not workplace_exempt():\n",
			"undeclared workplace_exempt() call"},
		{"the helper def is not a call", fixture, helperDef, ""},
		{"a helper that consults NEITHER field is not an exemption", fixture,
			"def enforce_workplace(locs):\n\tfail(\"no\")\n\tenforce_workplace([])\n", ""},
		{"a differently-named helper is gated all the same", fixture,
			"def staff_exempt():\n\treturn op.authTargetValidated\n\tif not staff_exempt():\n",
			"undeclared staff_exempt() call"},
		{"a helper that CALLS an exemption helper is one too", fixture,
			helperDef +
				"def site_gate():\n" +
				"\tif workplace_exempt():  # workplace-exempt: (per-call-site) site_gate's callers discharge it\n" +
				"\t\treturn\n" +
				"def handler():\n\ta = 1\n\tb = 2\n\tc = 3\n\td = 4\n\te = 5\n\tf = 6\n\tg = 7\n" +
				"\tif not site_gate():\n\t\tfail(\"x\")\n",
			"undeclared site_gate() call"},
		{"declared no-validated-path passes", fixture,
			helperDef + "\t# workplace-exempt: (no-validated-path) scope=any only, no task mints it\n" +
				"\tif not workplace_exempt():\n", ""},
		{"declared ownership-bound passes", fixture,
			helperDef + "\t# workplace-exempt: (ownership-bound) the probe below binds the target\n" +
				"\tif not workplace_exempt():\n", ""},
		{"declared workplace resource-bind passes", fixture,
			helperDef + "\t# workplace-exempt: (resource-bind) target names this work order\n" +
				"\tif not workplace_exempt():\n", ""},
		{"an on-the-line workplace declaration passes", fixture,
			helperDef + "\tif not workplace_exempt():  # workplace-exempt: (no-validated-path) staff-only op\n", ""},
		{"workplace-exempt declaration without a why is denied", fixture,
			helperDef + "\t# workplace-exempt: (ownership-bound)\n\tif not workplace_exempt():\n",
			"declaration must state its `<why>`"},
		{"unknown workplace-exempt shape is denied", fixture,
			helperDef + "\t# workplace-exempt: (trusted) staff are fine\n\tif not workplace_exempt():\n",
			"unknown shape (trusted)"},
	}
	var failures []string
	for _, tc := range cases {
		var hits, warned int
		var got []string
		for _, fd := range scanSource(tc.path, []byte(tc.src)) {
			if !strings.HasPrefix(fd.msg, "authcontext-target:") &&
				!strings.HasPrefix(fd.msg, "workplace-exempt:") {
				continue
			}
			hits++
			got = append(got, fd.msg)
			if fd.warn {
				warned++
			}
		}
		switch {
		case tc.want == "" && hits > 0:
			failures = append(failures, fmt.Sprintf("%s: expected no finding, got %d: %s", tc.name, hits, got[0]))
		case tc.want != "" && hits == 0:
			failures = append(failures, tc.name+": expected a finding, got none")
		case tc.want != "":
			var matched bool
			for _, m := range got {
				if strings.Contains(m, tc.want) {
					matched = true
					break
				}
			}
			if !matched {
				failures = append(failures, fmt.Sprintf("%s: no finding contained %q; got %q", tc.name, tc.want, got[0]))
			}
			// The design ratified these gates as BLOCKING from day one, not
			// warn-first: a warn is counted separately and never fails --strict,
			// so a silent flip to warn:true would disarm the gate while every
			// other assertion here still passed.
			if warned > 0 {
				failures = append(failures, tc.name+": finding is advisory (warn), but this gate is BLOCKING")
			}
		}
	}
	return failures
}

func trackedGoFiles() []string {
	out, err := exec.Command("git", "ls-files", "*.go").Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			files = append(files, l)
		}
	}
	return files
}
