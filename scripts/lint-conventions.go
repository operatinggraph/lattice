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
)

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
