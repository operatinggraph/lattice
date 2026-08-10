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
//   - Review-finding-label comments — a bare, trailing reviewer-finding
//     label such as `(A2)`, left over from a review-fix round instead of
//     cleaned up into a description of current behavior. Restricted to a
//     letter subset that never collides with this tree's own vocabulary
//     (architecture principles, data-placement, the derived-key ban); see
//     reviewFindingLabel's doc for the survey behind the shape.
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
//   - Hand-rolled embedded NATS fixture. internal/natsfixture owns the embedded
//     NATS + JetStream fixture. A hand-rolled copy inherits nats.go's default
//     connect options, and that default pins the WHOLE initial handshake to a
//     2s deadline with no retry — so any multi-second host stall (memory
//     pressure, a saturated box, a Docker stack alongside `go test -p 4`) fails
//     whichever package happened to be connecting, typically one the author
//     never touched, with `read tcp 127.0.0.1:A->B: i/o timeout`. The signal is
//     the server constructor; natsfixture itself is exempt.
//   - Read-posture classification (Contract #2 §2.5; BLOCKING — fails
//     --strict, per the script-read-posture design §13's flip once the
//     platform + verticals sweeps closed the debt list). Every script
//     `kv.Read(` / `kv.Links(` call site in a
//     packages/ non-test file must carry a `# read-posture: (a|c|d|e|f)`
//     Starlark annotation on the call line or in the comment block directly
//     above it (annotationSpans — a declaration binds to the statement it
//     introduces and that statement's block, never to a later sibling):
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
//     (selector) a non-security branch selector, no confinement rides on it.
//     There is deliberately NO shape for an exemption, keyed on the target's
//     presence OR on its equality with `op.actor`: a scope=any caller chooses
//     both, so neither proves the platform checked anything. The correct
//     spelling of an exemption is `op.authTargetValidated`, the platform bit
//     that is true only when step 3 CHECKED the target. Every shape needs a
//     trailing `<why>` — declaring is cheap, forgetting fails closed.
//     What a green run does NOT claim, shared with the `# read-posture:`
//     convention these mirror: the gates are fail-closed against FORGETTING to
//     declare, not against MIS-declaring — only (resource-bind) carries a
//     structural check, so a wrong (selector) or (ownership) passes. The
//     author's `<why>` is what a reviewer reads; the gate only guarantees one
//     was written. A declaration reaches only the statement it introduces and
//     that statement's own block (annotationSpans), so it can never spread to a
//     sibling its author never saw — but a reference inserted INSIDE the
//     annotated guard still inherits it, which is the declaration's own scope
//     and the one place inheritance is intended.
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
//   - NanoID-alphabet in test seed data (internal/substrate/keys.Alphabet,
//     Contract #1; BLOCKING). A *_test.go string literal embedded in a
//     vtx./lnk. key — vtx.<type>.<id>[.<localName>], or a fully-inlined
//     6-segment lnk.<typeA>.<idA>.<relation>.<typeB>.<idB> — or a bare 20-char
//     literal assigned to an id-suffixed identifier (the seed-ID call-site
//     convention: `ctClaimantID = "…"`, concatenated into a key elsewhere in
//     the file), whose 20-char id contains a character outside the canonical
//     alphabet (excludes I, l, O, 0) silently drops the fixture from any
//     labeled-prefix seed scan that validates the alphabet (KindUnknown) —
//     invisible until something downstream stops tolerating it. Declare
//     `// nanoid-alphabet: (reject) <why>` for a deliberately-invalid id used
//     to prove key/id validation rejects it.
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
	// reviewFindingLabel anchors a bare parenthesized reviewer-finding label
	// trailing a comment — the shape a review-fix round leaves behind when a
	// fix note ends "... (A2)" instead of being cleaned up into a
	// description of current behavior. Two constraints, both load-bearing
	// per a full-repo survey (neither alone reaches zero false positives):
	// the letter is restricted to {A,B,C,E,F}, deliberately excluding the
	// letters this tree already uses as stable vocabulary in the same
	// parenthesized-after-a-word shape — D (data-placement, e.g. `(D5)`),
	// G (the derived-key ban, `(G2)`), and P (architecture principles, e.g.
	// `(P5)`); and the label must be the LAST thing on the line. Every
	// existing (legitimate) use of a kept letter — Loupe Fire numbers
	// (`(F14)`, `(F20)`, …), an acceptance-criteria-shaped gloss (`(C2)`),
	// a taxonomy callback direction (`(B4)`/`(B5)`), a fault-injection case
	// (`(E5)`), test-actor mnemonics (`(A1)`/`(A2)`/`(A3)`) — sits
	// mid-sentence with more comment text after it, including several from
	// this very tree's own review-fix rounds (e.g. "covers A2: ...",
	// "covers B1: ...") that name a finding while still fully describing
	// current behavior; a bare trailing label with nothing earning its
	// keep after it is the leftover this check exists to catch, and no
	// legitimate use of this letter range takes that shape today.
	reviewFindingLabel = regexp.MustCompile(`(?:^|\s)(?://|#).*\([ABCEF][0-9]{1,2}\)\.?\s*$`)
	aspPrefix          = regexp.MustCompile(`"asp\.`)
	coreKVRead         = regexp.MustCompile(`\bCoreKVBucket\b|"core-kv"`)
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
	readPosture   = regexp.MustCompile(`#\s*read-posture:\s*\(([acdef])\)(.*)$`)
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
	// embeddedNATSCtor anchors a hand-rolled embedded NATS server fixture, by
	// either construction route: natsserver.NewServer + Start, or the upstream
	// test package's RunServer. internal/natsfixture owns the hardened fixture
	// (see its package doc) — a hand-rolled copy re-acquires the defects it
	// encodes against, and its callers then dial with nats.go's 2s default
	// whole-handshake deadline and no retry.
	embeddedNATSCtor = regexp.MustCompile(`\b(?:natsserver|natstest|natssrv|server|test)\.(?:NewServer|RunServer)\(`)
	// bareConnectCall anchors a direct nats.Connect in a test. natsfixture.Connect
	// is the fixture path; a direct call inherits nats.go's 2s whole-handshake
	// deadline with no retry. natsConnectShape is the declaration a deliberate
	// direct call must carry, capturing the class and its trailing `<why>`.
	bareConnectCall  = regexp.MustCompile(`\bnats\.Connect\(`)
	natsConnectShape = regexp.MustCompile(`//\s*nats-connect:\s*\(([a-z-]+)\)(.*)$`)
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
	// NanoID-alphabet enforcement in test seed data (internal/substrate/keys.
	// Alphabet, Contract #1). vtxEmbeddedID anchors the id at segment 3 of a
	// "vtx.<type>.<id>[.<localName>]" literal — always right after
	// "vtx.<type>.", so a later aspect localName (any length) is never
	// mistaken for the id even when it happens to be exactly 20 characters.
	vtxEmbeddedID = regexp.MustCompile(`"vtx\.[A-Za-z][A-Za-z0-9]*\.([A-Za-z0-9]{20})(?:[".])`)
	// lnkEmbeddedID anchors both ids of a fully-inlined 6-segment
	// "lnk.<typeA>.<idA>.<relation>.<typeB>.<idB>" literal (Contract #1). The
	// concatenated form (`"lnk.task." + id1 + ".assignedTo.identity." + id2`)
	// is caught instead via idSeedAssign, on each id's own seed declaration.
	lnkEmbeddedID = regexp.MustCompile(`"lnk\.[A-Za-z][A-Za-z0-9]*\.([A-Za-z0-9]{20})\.[A-Za-z][A-Za-z0-9]*\.[A-Za-z][A-Za-z0-9]*\.([A-Za-z0-9]{20})"`)
	// idSeedAssign anchors a const/var/short-var assignment of a bare 20-char
	// literal to an identifier (captured whole — a regex character class
	// can't require "ends in id" on a name that may be exactly two
	// characters, e.g. the bare identifier `id`, so the suffix test runs in
	// Go code). This is the seed-ID call-site convention (`ctClaimantID =
	// "…"`, `taskID := "…"`) used to build a key via concatenation elsewhere
	// in the file. Deliberately narrower than "any 20-char literal": a
	// struct-literal field (`id: "…"`, a table-driven test row) is not an
	// assignment and is out of scope — that is exactly the shape a
	// validator's own negative-test vectors use
	// (internal/substrate/keys/nanoid_test.go's TestIsValidNanoID), so they
	// need no annotation to pass clean.
	idSeedAssign        = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*:?=\s*"([A-Za-z0-9]{20})"`)
	nanoidAlphabetShape = regexp.MustCompile(`//\s*nanoid-alphabet:\s*\(([a-z-]+)\)(.*)$`)

	// derivedKeyCall / derivedKeyShape — the G2 call-site ban
	// (client-ceremony-op-descriptors-design.md §6). A content-addressed id
	// derivation in a SUBMITTER is how a caller used to name a key it could not
	// otherwise express; Contract #2 §2.5 class (g) makes that the owning DDL's
	// job, so a surviving call site must say what it derives.
	//
	// Matched on the BARE symbol rather than `substrate.SHA256NanoID`: an import
	// alias (`sub.SHA256NanoID`) and a dot-import both evade a package-qualified
	// pattern, and neither is exotic in a file that already imports substrate.
	//
	// The two banned entry points are the ones that can reproduce an index key:
	// SHA256NanoID, and NanoIDFromPCG, which IS SHA256NanoID's body once seeded
	// from a digest. substrate.DeriveNanoID is deliberately NOT banned — it
	// expands the digest across the alphabet instead of seeding a PCG, so it
	// yields a DIFFERENT id and cannot forge a key any script probes. Banning it
	// would flag every Contract #4 requestId derivation in the tree, which is
	// the annotate-the-noise failure that stops a default-deny gate being read.
	derivedKeyCall  = regexp.MustCompile(`\b(SHA256NanoID|NanoIDFromPCG)\(`)
	derivedKeyShape = regexp.MustCompile(`//\s*derived-key:(.*)$`)
)

// nanoidAlphabetShapes are the declarable exceptions to the NanoID-alphabet
// gate: (reject) is the only one — a deliberately invalid id used to prove
// key/id validation rejects it, mirroring nats-connect's (reject) class.
var nanoidAlphabetShapes = map[string]bool{
	"reject": true,
}

// forbiddenNanoIDChars are the visually-ambiguous characters the canonical
// NanoID alphabet excludes (internal/substrate/keys.Alphabet, Contract #1:
// A-Z, a-z, 0-9 minus I, l, O, 0). Every id candidate checkNanoIDAlphabet
// extracts is pre-filtered to [A-Za-z0-9] by its own regex, so these four
// characters are the only way one can be invalid.
const forbiddenNanoIDChars = "IlO0"

// invalidNanoIDChars returns the distinct forbidden characters in id, in
// order of first appearance, or nil if id is alphabet-compliant.
func invalidNanoIDChars(id string) []byte {
	var bad []byte
	for i := 0; i < len(id); i++ {
		c := id[i]
		if strings.IndexByte(forbiddenNanoIDChars, c) < 0 {
			continue
		}
		dup := false
		for _, b := range bad {
			if b == c {
				dup = true
				break
			}
		}
		if !dup {
			bad = append(bad, c)
		}
	}
	return bad
}

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
// `op.authContextTarget` reference. An EXEMPTION is deliberately absent, in
// either spelling: neither the target's presence nor its equality with
// `op.actor` proves the platform checked it, and a scope=any caller chooses
// both. The correct spelling of an exemption is `op.authTargetValidated`, and
// every confinement guard in the corpus now uses it.
var authCtxTargetShapes = map[string]bool{
	"ownership":     true,
	"payload-bind":  true,
	"resource-bind": true,
	"selector":      true,
}

// loadOrGenerateExemptFile is the one test file that legitimately keeps the
// direct bootstrap.LoadOrGenerate call: internal/pkgmgr/installer_test.go is
// `package pkgmgr` (internal test), and internal/testutil imports pkgmgr
// (install_phase1_packages.go), so testutil.EnsurePrimordials(t) would close
// an import cycle there.
const loadOrGenerateExemptFile = "internal/pkgmgr/installer_test.go"

// natsfixturePkg owns the embedded-NATS fixture, so it is the one place allowed to
// construct a server directly.
const natsfixturePkg = "internal/natsfixture/"

// natsConnectClasses are the two reasons a test may dial NATS directly instead of
// through natsfixture.Connect. Both are cases where the fixture's generous
// handshake budget would be WRONG, not merely unnecessary — so they are declared,
// never assumed.
var natsConnectClasses = map[string]bool{
	// (reject): the test asserts the connect is denied/fails. Retrying it on a
	// long budget would turn a fast negative vector into a multi-minute one.
	"reject": true,
	// (probe): a liveness probe against an externally-run stack that must fail
	// fast so the test can skip when the stack is down.
	"probe": true,
}

// annotation is one classification comment: the raw line it sits on (sub-field
// checks like `relation=` / `epoch=` read that line, not the annotated
// statement), the shape or class it declares, and the author's trailing `<why>`.
type annotation struct {
	text  string
	shape string
	why   string
}

// annotationSpans resolves, for one annotation kind, which lines each
// classification comment in the file covers.
//
// An annotation binds to ONE statement: the line it trails, or — when it sits on
// its own comment line — the first code line beneath it, reachable across
// further comment lines only. A blank line ends a comment block, so an
// annotation separated from the code by one covers nothing and every reference
// below it fails closed. Coverage is that statement plus the statement's own
// indentation block: each following line indented deeper than it (blank lines
// carried along), closed by the first line at or left of its indentation.
//
// Binding to the statement is what makes the declaration mean what its author
// wrote. A fixed N-line window instead let any reference in the following N
// lines inherit a neighbouring declaration, so a reference inserted after an
// annotated guard silently acquired a claim nobody made about it — the one shape
// a default-deny gate must not admit. A new sibling statement is now undeclared,
// and undeclared is denied.
//
// Later annotations overwrite earlier ones wherever both cover a line, so a
// nested reference resolves to its nearest enclosing declaration.
func annotationSpans(lines []string, re *regexp.Regexp) map[int]annotation {
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
		anchor := i
		if isCommentLine(line) {
			anchor = -1
			for j := i + 1; j < len(lines); j++ {
				if strings.TrimSpace(lines[j]) == "" {
					break
				}
				if isCommentLine(lines[j]) {
					continue
				}
				anchor = j
				break
			}
			if anchor < 0 {
				continue
			}
		}
		indent := indentWidth(lines[anchor])
		out[anchor+1] = a
		for j := anchor + 1; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) != "" && indentWidth(lines[j]) <= indent {
				break
			}
			out[j+1] = a
		}
	}
	return out
}

// isCommentLine reports whether a line is only a comment — Starlark `#` or Go
// `//`, the two forms these embedded scripts carry.
func isCommentLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//")
}

// indentWidth counts a line's leading whitespace characters (a tab counts as
// one, matching exemptionHelpers) — consistent within a file, which is all the
// block walk compares.
func indentWidth(line string) int {
	return len(line) - len(strings.TrimLeft(line, " \t"))
}

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
// repoRelative converts an absolute edit path to the repo-relative form every
// scope test in scanSource is written against (`internal/…`, `packages/…`).
// Without it the hook checks a DIFFERENT set of rules than CI: prefix-scoped
// exemptions stop matching, so exempt files get flagged, and prefix-scoped
// checks stop firing, so real findings go unreported.
func repoRelative(path string) string {
	if !filepath.IsAbs(path) {
		return path
	}
	root, err := os.Getwd()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}

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
	findings := scanFile(repoRelative(path))
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
	// internal/spike holds standalone `main` benchmarks with no *testing.T, so the
	// fixture (which is t-bound by construction) is not available to them.
	embeddedNATSScoped := !strings.HasPrefix(slash, natsfixturePkg) && !strings.HasPrefix(slash, "internal/spike/")
	// natsfixture proves the bare default's behavior, so it dials directly.
	bareConnectScoped := isTest && !strings.HasPrefix(slash, natsfixturePkg)
	var connectAt map[int]annotation
	if bareConnectScoped {
		connectAt = annotationSpans(strings.Split(string(data), "\n"), natsConnectShape)
	}
	loadOrGenerateScoped := isTest &&
		!strings.HasPrefix(slash, "internal/bootstrap/") &&
		!strings.HasPrefix(slash, "internal/testutil/") &&
		slash != loadOrGenerateExemptFile
	// nanoidScoped — the NanoID-alphabet gate applies to *_test.go seed data
	// only; shipped (non-test) code generates ids via keys.NewNanoID(), never
	// hand-writes them.
	nanoidScoped := isTest
	var nanoidAt map[int]annotation
	if nanoidScoped {
		nanoidAt = annotationSpans(strings.Split(string(data), "\n"), nanoidAlphabetShape)
	}
	// derivedKeyScoped — G2 bans the derivation at SUBMITTER call sites, so it
	// is scoped to everything outside internal/. internal/ is where the
	// primitive itself lives (substrate defines it, the Processor exposes it to
	// scripts, Loupe-adjacent managers consume it); packages/ is the DDL side,
	// which is precisely where a derivation is supposed to happen.
	// derivedKeyScoped — G2 bans the derivation at SUBMITTER call sites. The
	// excluded trees are the ones that legitimately OWN the primitive:
	// internal/substrate defines it, internal/processor exposes it to scripts,
	// and packages/ is the DDL side, which is precisely where a derivation is
	// supposed to happen. scripts/lint-*.go are excluded because their own
	// self-test fixtures are string literals containing the banned call.
	//
	// scripts/ is otherwise IN scope: seed/demo scripts submit real operations,
	// and one of them (seed-showcase) carried a live hand-ported derivation that
	// a blanket scripts/ exclusion hid. The internal/ exclusion is broader than
	// this gate's purpose warrants — internal/gateway and internal/objectmanager
	// are submitters too — and is filed as its own board row rather than widened
	// here, since it needs annotations on legitimate sites this fire does not own.
	derivedKeyScoped := !strings.HasPrefix(slash, "internal/") && !strings.HasPrefix(slash, "packages/") &&
		!strings.HasPrefix(slash, "scripts/lint-")
	var derivedKeyLines []string
	if derivedKeyScoped {
		derivedKeyLines = strings.Split(string(data), "\n")
	}
	// The file's own validated-target exemption helpers, derived from the
	// script text rather than a hardcoded name list (see exemptionHelpers).
	var exemptHelpers map[string]bool
	// Which annotation, if any, covers each line — one map per annotation kind,
	// each resolved against the annotated statement's own block
	// (see annotationSpans).
	var postureAt, authCtxAt, workplaceAt map[int]annotation
	if postureScoped {
		exemptHelpers = exemptionHelpers(data)
		lines := strings.Split(string(data), "\n")
		postureAt = annotationSpans(lines, readPosture)
		authCtxAt = annotationSpans(lines, authCtxTargetShape)
		workplaceAt = annotationSpans(lines, workplaceExemptShape)
	}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	ln := 0
	for sc.Scan() {
		ln++
		line := sc.Text()
		if historyComment.MatchString(line) {
			out = append(out, finding{file: path, line: ln, msg: "history/changelog comment — git blame + the commit message are the record"})
		}
		if reviewFindingLabel.MatchString(line) {
			out = append(out, finding{file: path, line: ln, msg: "review-finding-label comment — findings live in the review record and the commit message, not code comments"})
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
		if bareConnectScoped && bareConnectCall.MatchString(line) {
			if a, ok := connectAt[ln]; !ok {
				out = append(out, finding{file: path, line: ln, msg: "bare nats.Connect in a test — it inherits nats.go's 2s whole-handshake deadline with no retry, so a host stall fails it in a package nobody touched; use natsfixture.Connect(t, url, opts...), or declare `// nats-connect: (reject|probe) <why>` if a long budget would be wrong here"})
			} else if !natsConnectClasses[a.shape] {
				out = append(out, finding{file: path, line: ln, msg: "unknown nats-connect class (" + a.shape + ") — the declared reasons a test dials directly are (reject) the connect is asserted to fail, and (probe) a fast-failing liveness check against an externally-run stack"})
			} else if strings.TrimSpace(a.why) == "" {
				out = append(out, finding{file: path, line: ln, msg: "nats-connect: (" + a.shape + ") declaration carries no `<why>` — name what makes a long handshake budget wrong here, so the next reader need not re-derive it"})
			}
		}
		if embeddedNATSScoped && embeddedNATSCtor.MatchString(line) {
			out = append(out, finding{file: path, line: ln, msg: "hand-rolled embedded NATS fixture — a bare nats.Connect inherits nats.go's 2s whole-handshake deadline with no retry, so a host stall fails a random untouched package with `read tcp ...: i/o timeout`; use natsfixture.Server(t) / natsfixture.StartServer(t) (internal/natsfixture)"})
		}
		if loadOrGenerateScoped && loadOrGenerateCall.MatchString(line) {
			out = append(out, finding{file: path, line: ln, msg: "per-test bootstrap.LoadOrGenerate — re-populates internal/bootstrap's globals per test, which races under t.Parallel(); use testutil.EnsurePrimordials(t) instead (bootstrap-primordial-globals-race-design.md §4)"})
		}
		if nanoidScoped {
			out = append(out, checkNanoIDAlphabet(path, ln, line, nanoidAt[ln])...)
		}
		if derivedKeyScoped {
			out = append(out, checkDerivedKey(path, ln, derivedKeyLines)...)
		}
		if postureScoped {
			out = append(out, checkReadPosture(path, ln, line, postureAt[ln], fileMutates)...)
			out = append(out, checkAuthContextTarget(path, ln, line, authCtxAt[ln])...)
			out = append(out, checkWorkplaceExempt(path, ln, line, workplaceAt[ln], exemptHelpers)...)
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
// declared is the `# read-posture:` annotation covering this line, resolved by
// annotationSpans. Comment lines (Go `//` or Starlark `#`) are skipped — prose
// ABOUT kv.Read is not a call.
func checkReadPosture(path string, ln int, line string, declared annotation, fileMutates bool) []finding {
	if isCommentLine(line) {
		return nil
	}
	if !kvCall.MatchString(line) {
		return nil
	}
	class, annotated := declared.shape, declared.text
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
// BLOCKING). declared is the `# authcontext-target:` annotation covering this
// line, resolved by annotationSpans. Comment lines are skipped — prose ABOUT the
// field is not a use of it.
func checkAuthContextTarget(path string, ln int, line string, declared annotation) []finding {
	if isCommentLine(line) {
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

	shape, why := declared.shape, declared.why
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
	}
	return out
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
// holding a legitimate grant for one resource can act on another. declared is
// the `# workplace-exempt:` annotation covering this line (annotationSpans).
func checkWorkplaceExempt(path string, ln int, line string, declared annotation, helpers map[string]bool) []finding {
	if isCommentLine(line) || starlarkDef.MatchString(line) {
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

	shape, why := declared.shape, declared.why
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

// checkNanoIDAlphabet flags a *_test.go string literal used to build a
// vtx./lnk. key — embedded directly, or a bare literal assigned to an
// id-suffixed identifier that seeds one via concatenation elsewhere in the
// file — whose 20-char id contains a character outside the canonical NanoID
// alphabet (all findings BLOCKING). Such an id silently drops out of any
// labeled-prefix seed scan that validates the alphabet (KindUnknown) —
// invisible until something downstream stops tolerating it (bit twice
// 2026-08-01: composite_key_producer_test.go's "…01aaaaaaaaa" and four
// mnemonic ids in packages/privacy-base/lens_cypher_test.go). declared is the
// `// nanoid-alphabet:` annotation covering this line (annotationSpans).
func checkNanoIDAlphabet(path string, ln int, line string, declared annotation) []finding {
	if isCommentLine(line) {
		return nil
	}
	var candidates []string
	for _, m := range vtxEmbeddedID.FindAllStringSubmatch(line, -1) {
		candidates = append(candidates, m[1])
	}
	for _, m := range lnkEmbeddedID.FindAllStringSubmatch(line, -1) {
		candidates = append(candidates, m[1], m[2])
	}
	if m := idSeedAssign.FindStringSubmatch(line); m != nil && strings.HasSuffix(strings.ToLower(m[1]), "id") {
		candidates = append(candidates, m[2])
	}
	var badIDs []string
	for _, id := range candidates {
		if len(invalidNanoIDChars(id)) > 0 {
			badIDs = append(badIDs, id)
		}
	}
	if len(badIDs) == 0 {
		return nil
	}
	shape, why := declared.shape, declared.why
	switch {
	case shape == "":
		var out []finding
		for _, id := range badIDs {
			out = append(out, finding{file: path, line: ln, msg: fmt.Sprintf(
				"nanoid-alphabet: id %q contains %s outside the canonical alphabet (internal/substrate/keys.Alphabet excludes I, l, O, 0 — Contract #1); a labeled-prefix seed scan silently drops this fixture as KindUnknown. Fix the id, or declare `// nanoid-alphabet: (reject) <why>` if it deliberately tests rejection",
				id, quoteBytes(invalidNanoIDChars(id)))})
		}
		return out
	case !nanoidAlphabetShapes[shape]:
		return []finding{{file: path, line: ln, msg: "nanoid-alphabet: unknown shape (" + shape + ") — the only declarable exception is (reject): a deliberately invalid id used to prove key/id validation rejects it"}}
	case strings.TrimSpace(why) == "":
		return []finding{{file: path, line: ln, msg: "nanoid-alphabet: a (reject) declaration must state its `<why>`"}}
	default:
		return nil
	}
}

// checkDerivedKey is the G2 call-site ban
// (client-ceremony-op-descriptors-design.md §6): default-deny a
// content-addressed id derivation in a submitter, with an explicit
// `// derived-key: <reason>` as the one escape.
//
// The rule is not "hashing is forbidden" — it is that a submitter deriving a
// key CANNOT be doing so correctly by construction. A declared read key that
// is a function of the payload under the *package's* semantics belongs to that
// package's `derive_reads` (Contract #2 §2.5 class (g)); a client that
// recomputes it has re-implemented the package's normalization in a second
// language, where nothing makes the two agree and nothing notices when they
// stop. What survives the ban is a derivation of something that is NOT a
// declared read — an object id, a Contract #4 requestId — and those simply
// have to say so, which is cheap and makes the exception re-checkable.
//
// The annotation binds to ONE statement: the line itself, or the contiguous
// comment block immediately above it. It deliberately does NOT use
// annotationSpans, whose indent-based scoping anchors a standalone comment to
// the first code line beneath it and then covers everything indented deeper —
// so a `// derived-key:` doc comment on a func would amnesty every derivation
// in its body, and a doc comment that merely MENTIONS the annotation while
// explaining the convention would do the same. For a gate whose whole value is
// that each exception is individually re-checkable, one comment covering a
// whole function is the failure mode, not a convenience.
//
// _test.go files are in scope deliberately: a test is exactly where a deleted
// re-port would be reintroduced, and an exempted test suite is a hole the
// gate's own clean-tree claim would not survive.
func checkDerivedKey(path string, ln int, lines []string) []finding {
	line := lines[ln-1]
	if isCommentLine(line) || !derivedKeyCall.MatchString(line) {
		return nil
	}
	switch derivedKeyReason(lines, ln-1) {
	case reasonGiven:
		return nil
	case reasonMissing:
		return []finding{{file: path, line: ln, msg: "derived-key: declaration carries no reason — name what this derives and why it is not a declared read the owning package should compute"}}
	}
	return []finding{{file: path, line: ln, msg: "derived-key: undeclared content-addressed id derivation in a submitter — a key derived client-side from a payload is a class-(g) declared read the owning DDL's `derive_reads` should compute (Contract #2 §2.5); a hand-ported derivation is a second implementation of the package's normalization that nothing keeps in agreement. If this derives something that is NOT a declared read (an object id, a requestId), declare `// derived-key: <what it derives and why the package cannot>`"}}
}

type derivedKeyVerdict int

const (
	reasonAbsent derivedKeyVerdict = iota
	reasonMissing
	reasonGiven
)

// derivedKeyReason looks for a `// derived-key:` annotation on line i, else on
// the contiguous run of comment lines directly above it. A blank line or any
// code line ends the run, so an annotation never reaches past the statement it
// was written for.
func derivedKeyReason(lines []string, i int) derivedKeyVerdict {
	if v := derivedKeyOnLine(lines[i]); v != reasonAbsent {
		return v
	}
	for j := i - 1; j >= 0; j-- {
		trimmed := strings.TrimSpace(lines[j])
		if trimmed == "" || !strings.HasPrefix(trimmed, "//") {
			return reasonAbsent
		}
		if v := derivedKeyOnLine(lines[j]); v != reasonAbsent {
			return v
		}
	}
	return reasonAbsent
}

func derivedKeyOnLine(line string) derivedKeyVerdict {
	m := derivedKeyShape.FindStringSubmatch(line)
	if m == nil {
		return reasonAbsent
	}
	if strings.TrimSpace(m[1]) == "" {
		return reasonMissing
	}
	return reasonGiven
}

// quoteBytes renders a byte slice as a comma-separated, single-quoted list
// for a finding message, e.g. []byte("l0") -> "'l', '0'".
func quoteBytes(bs []byte) string {
	parts := make([]string, len(bs))
	for i, b := range bs {
		parts[i] = "'" + string(b) + "'"
	}
	return strings.Join(parts, ", ")
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
		{"a declaration covers its own statement's block", fixture,
			"\t# authcontext-target: (ownership) target must own the resource\n" +
				"\tif op.authContextTarget != \"\":\n" +
				"\t\t_, tid = parts_of(op.authContextTarget, \"authContextTarget\", \"identity\")\n" +
				"\t\tif op.authContextTarget != actor_identity:\n" +
				"\t\t\tfail(\"x\")\n", ""},
		{"a declaration does NOT reach the NEXT sibling statement", fixture,
			"\t# authcontext-target: (ownership) target must own the resource\n" +
				"\tif op.authContextTarget != \"\":\n" +
				"\t\t_, tid = parts_of(op.authContextTarget, \"authContextTarget\", \"identity\")\n" +
				"\tother = op.authContextTarget\n",
			"undeclared op.authContextTarget reference"},
		{"a declaration does NOT reach a following sibling with no block between", fixture,
			"\t# authcontext-target: (selector) picks the amount source\n" +
				"\tis_self = op.actor\n" +
				"\tother = op.authContextTarget\n",
			"undeclared op.authContextTarget reference"},
		{"an annotation separated from the code by a blank line covers nothing", fixture,
			"\t# authcontext-target: (ownership) target must own the resource\n" +
				"\n" +
				"\tif op.authContextTarget != \"\":\n",
			"undeclared op.authContextTarget reference"},
		{"a blank line INSIDE the annotated block does not close it", fixture,
			"\t# authcontext-target: (ownership) target must own the resource\n" +
				"\tif op.authContextTarget != \"\":\n" +
				"\t\ta = 1\n" +
				"\n" +
				"\t\t_, tid = parts_of(op.authContextTarget, \"authContextTarget\", \"identity\")\n", ""},
		{"a nested declaration wins over the enclosing one", fixture,
			"\t# authcontext-target: (selector) outer branch selector\n" +
				"\tif op.authContextTarget != \"\":\n" +
				"\t\t# authcontext-target: (resource-bind) names this work order\n" +
				"\t\tbound = op.authContextTarget == wkey\n",
			"must pair the comparison with op.authTargetValidated"},
		{"a trailing declaration covers its own block but not its sibling", fixture,
			"\tif op.authContextTarget != \"\":  # authcontext-target: (ownership) owns it\n" +
				"\t\t_, tid = parts_of(op.authContextTarget, \"authContextTarget\", \"identity\")\n" +
				"\tother = op.authContextTarget\n",
			"undeclared op.authContextTarget reference"},
		{"a read-posture declaration binds to its own call", fixture,
			"\tkey = hub + \".slot\"\n" +
				"\t# read-posture: (d) declared optionalReads by the dispatcher\n" +
				"\texisting = kv.Read(key)\n", ""},
		{"a read-posture declaration does NOT reach the next sibling read", fixture,
			"\t# read-posture: (d) declared optionalReads by the dispatcher\n" +
				"\tdoc = kv.Read(key)\n" +
				"\taspect = kv.Read(key + \".tenancy\")\n",
			"unclassified kv.Read"},
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
		{"a self-equality exemption has no shape to declare, anywhere", fixture,
			"\t# authcontext-target: (legacy-self-exempt) backstopped by identifiedBy\n" +
				"\tif op.authContextTarget == op.actor:\n",
			"unknown shape (legacy-self-exempt)"},
		{"the same holds in the file that once carried the legacy guard", "packages/clinic-domain/ddls.go",
			"\t# authcontext-target: (legacy-self-exempt) backstopped by identifiedBy\n" +
				"\tif op.authContextTarget == op.actor:\n",
			"unknown shape (legacy-self-exempt)"},
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
				"def handler():\n" +
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
		{"an embedded vtx key with an invalid char is denied", "packages/self-test/ddls_test.go",
			"\tctClaimant = \"vtx.identity.BBclaimantHJKMNPQRST\"\n",
			"nanoid-alphabet: id \"BBclaimantHJKMNPQRST\" contains 'l'"},
		{"an embedded vtx key that is fully alphabet-compliant passes", "packages/self-test/ddls_test.go",
			"\tctOK = \"vtx.identity.ABCDEFGHJKLMNPQRSTUV\"\n", ""},
		{"an embedded lnk key with an invalid char in idB is denied", "packages/self-test/ddls_test.go",
			"\tctLink = \"lnk.identity.ABCDEFGHJKLMNPQRSTUV.holdsRole.role.BBclaimantHJKMNPQRST\"\n",
			"nanoid-alphabet: id \"BBclaimantHJKMNPQRST\" contains 'l'"},
		{"an id-suffixed seed assignment with an invalid char is denied", "packages/self-test/ddls_test.go",
			"\tctClaimantID = \"BBclaimantHJKMNPQRST\"\n",
			"nanoid-alphabet: id \"BBclaimantHJKMNPQRST\" contains 'l'"},
		{"a short-var id-suffixed seed assignment is denied the same way", "packages/self-test/ddls_test.go",
			"\trid := \"systemAdmin000000001\"\n",
			"nanoid-alphabet:"},
		{"a struct-literal test row is not a seed-id assignment", "packages/self-test/ddls_test.go",
			"\t\t{\"contains I\", \"IHj4kPmRtw9nbCxz5vQ2\", false},\n", ""},
		{"a declared (reject) exception on an invalid embedded key passes", "packages/self-test/ddls_test.go",
			"\t// nanoid-alphabet: (reject) proves the key parser rejects a malformed id\n" +
				"\tbadKey := \"vtx.identity.BBclaimantHJKMNPQRST\"\n", ""},
		{"a declared (reject) exception without a why is denied", "packages/self-test/ddls_test.go",
			"\t// nanoid-alphabet: (reject)\n" +
				"\tbadKey := \"vtx.identity.BBclaimantHJKMNPQRST\"\n",
			"must state its `<why>`"},
		{"an unknown nanoid-alphabet shape is denied", "packages/self-test/ddls_test.go",
			"\t// nanoid-alphabet: (trusted) staff wrote it by hand\n" +
				"\tbadKey := \"vtx.identity.BBclaimantHJKMNPQRST\"\n",
			"unknown shape (trusted)"},
		{"the nanoid-alphabet gate is scoped to _test.go files", "packages/self-test/ddls.go",
			"\tctClaimant = \"vtx.identity.BBclaimantHJKMNPQRST\"\n", ""},

		{"an undeclared SHA256NanoID in cmd/ is denied", "cmd/some-app/keys.go",
			"\tk := \"vtx.identityindex.\" + substrate.SHA256NanoID(\"email:\" + e)\n",
			"undeclared content-addressed id derivation in a submitter"},
		{"a declared derivation passes", "cmd/some-app/objects.go",
			"\t// derived-key: object id, content-addressed; not a declared read\n" +
				"\toid := substrate.SHA256NanoID(\"object:\" + digest)\n", ""},
		{"an on-the-line declaration passes", "cmd/some-app/objects.go",
			"\toid := substrate.SHA256NanoID(\"object:\" + digest) // derived-key: object id, not a read\n", ""},
		{"a reasonless declaration is denied", "cmd/some-app/objects.go",
			"\t// derived-key:\n" +
				"\toid := substrate.SHA256NanoID(\"object:\" + digest)\n",
			"declaration carries no reason"},
		{"the derived-key gate is scoped off internal/", "internal/objectmanager/gc.go",
			"\toid := substrate.SHA256NanoID(\"object:\" + digest)\n", ""},
		{"the derived-key gate is scoped off packages/", "packages/objects-base/ddls.go",
			"\toid := substrate.SHA256NanoID(\"object:\" + digest)\n", ""},
		{"the derived-key gate covers _test.go under cmd/", "cmd/some-app/objects_test.go",
			"\twant := substrate.SHA256NanoID(\"object:\" + digest)\n",
			"undeclared content-addressed id derivation in a submitter"},
		{"an import alias does not evade the ban", "cmd/some-app/keys.go",
			"\tk := \"vtx.identityindex.\" + sub.SHA256NanoID(\"email:\" + e)\n",
			"undeclared content-addressed id derivation in a submitter"},
		{"a dot-import does not evade the ban", "cmd/some-app/keys.go",
			"\tk := \"vtx.identityindex.\" + SHA256NanoID(\"email:\" + e)\n",
			"undeclared content-addressed id derivation in a submitter"},
		{"a digest-seeded NanoIDFromPCG does not evade the ban", "cmd/some-app/keys.go",
			"\tk := \"vtx.identityindex.\" + substrate.NanoIDFromPCG(pcg, 20)\n",
			"undeclared content-addressed id derivation in a submitter"},
		{"DeriveNanoID is deliberately not banned (a different id, cannot forge a key)", "cmd/some-app/attach.go",
			"\trid := substrate.DeriveNanoID(\"attach:\", input)\n", ""},
		{"an annotation does NOT carry into a function body", "cmd/some-app/objects.go",
			"// derived-key: object id, content-addressed\n" +
				"func mint(digest string) string {\n" +
				"\toid := substrate.SHA256NanoID(\"object:\" + digest)\n" +
				"\treturn oid\n}\n",
			"undeclared content-addressed id derivation in a submitter"},
		{"an annotation does NOT carry to a nested statement", "cmd/some-app/objects.go",
			"\t// derived-key: object id, content-addressed\n" +
				"\toid := substrate.SHA256NanoID(\"object:\" + digest)\n" +
				"\tif x {\n\t\tother := substrate.SHA256NanoID(\"email:\" + e)\n\t}\n",
			"undeclared content-addressed id derivation in a submitter"},
		{"a blank line breaks the annotation's reach", "cmd/some-app/objects.go",
			"\t// derived-key: object id, content-addressed\n\n" +
				"\toid := substrate.SHA256NanoID(\"object:\" + digest)\n",
			"undeclared content-addressed id derivation in a submitter"},
		{"scripts/ is in scope", "scripts/seed-demo.go",
			"\tk := \"vtx.identityindex.\" + substrate.SHA256NanoID(\"email:\" + e)\n",
			"undeclared content-addressed id derivation in a submitter"},
		{"the lint scripts' own fixtures are exempt", "scripts/lint-conventions.go",
			"\tsrc := \"substrate.SHA256NanoID(x)\"\n", ""},
	}
	var failures []string
	for _, tc := range cases {
		var hits, warned int
		var got []string
		for _, fd := range scanSource(tc.path, []byte(tc.src)) {
			if !strings.HasPrefix(fd.msg, "authcontext-target:") &&
				!strings.HasPrefix(fd.msg, "workplace-exempt:") &&
				!strings.HasPrefix(fd.msg, "read-posture:") &&
				!strings.HasPrefix(fd.msg, "nanoid-alphabet:") &&
				!strings.HasPrefix(fd.msg, "derived-key:") {
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
