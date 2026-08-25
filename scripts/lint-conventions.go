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
//   - nats.go connection-state handler slots set outside internal/substrate.
//     SetDisconnectErrHandler / SetDisconnectHandler / SetReconnectHandler /
//     SetClosedHandler each hold exactly ONE callback on a *nats.Conn, and
//     every Lattice binary shares one connection across its components — so a
//     second registrant silently unregisters the first, and the component
//     that lost its slot keeps serving as though the connection never
//     dropped. Nothing about that failure is observable at runtime: no error,
//     no log, just an edge that stops arriving. substrate.Conn owns the slots
//     and fans out through OnConnectionStateChange, which any number of
//     listeners may hold. internal/substrate itself is exempt (it is the
//     owner).
//   - A package-qualified pkgmgr.NewInstaller call outside its sanctioned
//     callers. Installer.SpecParser stays nil unless the caller wires it by
//     hand, and a nil SpecParser silently disables the install-time lens
//     label-cap gate (internal/pkgmgr/lenslabelcap.go) — a fixture or entry
//     point that constructs the installer directly proves nothing about that
//     gate. testutil.NewInstaller (internal/testutil) wires SpecParser once;
//     internal/pkgmgr itself, cmd/lattice-pkg, and cmd/loupe are the other
//     sanctioned callers.
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
//   - Undeclared MaxReconnects on a cmd/** substrate.Connect (BLOCKING).
//     substrate.Connect only threads MaxReconnects into the underlying NATS
//     options when the field is nonzero (internal/substrate/conn.go), so a
//     call that never mentions it silently inherits nats.go's own default —
//     60 reconnect attempts, ~2s apart — instead of a value anyone chose.
//     Once that budget is exhausted the *nats.Conn closes PERMANENTLY
//     (nats.go never retries a closed connection), so a long-running
//     component keeps running as a zombie holding a dead NATS handle: the
//     confirmed cause of Refractor staying up for two hours, holding only its
//     pprof socket, after an nats-server restart every other component
//     reconnected through. The gate does not judge the value — set
//     `MaxReconnects: -1` for a daemon that must reconnect indefinitely, or
//     an explicit small finite value for a short-lived CLI that should fail
//     fast — only the omission.
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
	// historyPhrases are the change-narration phrases banned anywhere in a
	// comment (CLAUDE.md's "no history / changelog comments" rule), kept in their
	// own literal so the alternation reads as a list rather than as noise inside
	// a longer pattern.
	//
	// Phrases are admitted on evidence: a candidate is measured across the whole
	// tree first, and one whose hits are dominated by legitimate prose is
	// rejected rather than gated. "used to" (96 hits) and "no longer" (255) were
	// rejected on exactly that test.
	//
	// The list covers two grammars. The first narrates a named PRIOR state in the
	// past tense. The second anchors on the change being authored right now —
	// "this fire" (72 hits), "this fix" (14), "this change" (3) — which fails the
	// rule for the reason the rule exists: a reader who has no idea a change ever
	// happened cannot resolve the referent. Measured alongside them and rejected:
	// "this run" (25) and "this pass" (37), both dominated by runtime prose (a CLI
	// invocation, a reconcile pass); and "this commit" (3), where "commit path"
	// and "commit step" are core Processor vocabulary and RE2 has no lookahead to
	// separate them, so one false positive in three is not worth two sites.
	// "this increment" (35) and "pre-fix" (11) are the same grammar under other
	// words and are admitted with them. Sentence-initial capitalisation is
	// matched: the tree carries none today, and a gate one shift key evades is
	// not a gate.
	//
	// A HYPHEN after the word is excluded, because `\b` matches one and
	// "fire-and-forget" is house vocabulary here (20+ comments across
	// internal/loom, internal/substrate, internal/refractor/pipeline,
	// cmd/refractor, internal/processor) — "this fire-and-forget path" is a
	// legitimate sentence and a blocking gate must not refuse it.
	//
	// Three false-positive shapes remain known and accepted, each costing a
	// reword rather than a wrong green: "fire" as a VERB after "this" ("should
	// this fire next" — say "trigger"); "change" as a verb inside a quoted
	// question ("did this change" — name the question instead); and the domain
	// nouns in "this change request" and "by this increment". RE2 has no
	// lookahead, and a special case carved into a lint gate is a worse liability
	// than a rewording.
	historyPhrases = `Previously\b|Was:|Replaces\b|renamed from|moved from|formerly\b|Before (?:the|this) (?:fix|change|patch|rewrite|gate)\b|[Tt]his (?:fire|fix|change|increment)(?:[^-\w]|$)|[Pp]re-fix\b`
	// historyComment flags a comment carrying one of those phrases. The
	// changelog-tag shape stays anchored to a comment's lead, unlike the rest: a
	// whole-tree measurement unanchored produced 86 hits, nearly all a
	// traceability citation mid-sentence, which is an accepted convention here
	// rather than a narrated change. Every other phrase matches anywhere on the
	// line, so a clause buried mid-sentence is not invisible to the gate.
	historyComment = regexp.MustCompile(`//[ \t]*Story [0-9]|(?://|/\*).*\b(` + historyPhrases + `)`)
	// historyPhraseOnly matches the same phrases against comment TEXT that has
	// already had its `//` markers stripped — the joined-block form below.
	historyPhraseOnly = regexp.MustCompile(`\b(` + historyPhrases + `)`)
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
	// deadConjunct — a boolean literal wired into a condition so the branch it
	// guards can never run (`if false && x`) or always runs (`if true || x`).
	// The shape has one real source: a mutation planted to prove a test fails
	// without the arm it disables. That is a legitimate thing to do and an
	// illegitimate thing to leave behind, and the window between planting and
	// restoring is exactly when a parallel commit picks the tree up — which is
	// how a guard shipped inert, with a test asserting against the disabled
	// behaviour and passing. Nothing else writes this: a genuinely constant
	// condition is written as the constant.
	// The literal must be a standalone OPERAND, so the preceding token is
	// pinned: `== false &&` and `!= true ||` are comparisons whose result feeds
	// the conjunction, and the corpus is full of them.
	deadConjunct = regexp.MustCompile(`(?:\bif\s+|&&\s+|\|\|\s+|\(\s*)(?:false\s*&&|true\s*\|\|)|(?:&&\s*false|\|\|\s*true)\s*(?:\)|\{|$)`)
	coreKVRead   = regexp.MustCompile(`\bCoreKVBucket\b|"core-kv"`)
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
	// weaverTargetIDField anchors a pkgmgr.WeaverTargetSpec composite literal by
	// its one always-present field; weaverTargetDescField is the prose an
	// operator-facing surface reads the target by.
	weaverTargetIDField   = regexp.MustCompile(`(?m)^[ \t]*TargetID:[ \t]`)
	weaverTargetDescField = regexp.MustCompile(`\bDescription:`)
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
	// connStateHandlerCall anchors a nats.go connection-state handler being
	// set on a *nats.Conn. Each of these is a single slot, so a second setter
	// anywhere in a process silently unregisters the first — a failure with no
	// runtime symptom at all. substrate.Conn owns them and multiplexes through
	// OnConnectionStateChange.
	connStateHandlerCall = regexp.MustCompile(`\.Set(?:DisconnectErr|Disconnect|Reconnect|Closed)Handler\(`)
	// Primordial-actor guard (declared-read-scope-authorization-design.md §12).
	// externalEventEmit anchors a script EMITTING an external.<adapter> business
	// event — the moment payload-named subject data leaves the platform, and the
	// hazard this gate binds. Anchored on the `"class":` field rather than the
	// bare prefix so the Go-side package prose that merely NAMES an
	// `external.notification` event does not match; both the
	// `"external." + adapter` and the `"external.<literal>"` spellings do.
	externalEventEmit = regexp.MustCompile(`"class":\s*"external\.`)
	// opBranchHeader anchors a DDL script's per-operationType dispatch branch,
	// the unit this gate reasons over: a guard in one branch says nothing about
	// its siblings.
	opBranchHeader = regexp.MustCompile(`(?m)^([ \t]*)if ot == "([A-Za-z0-9_]+)"`)
	// topLevelDef anchors a script's own top-level `def` — the marker that an
	// emission has left its dispatch branch and moved into a helper the gate
	// cannot attribute (it does not trace calls).
	topLevelDef = regexp.MustCompile(`(?m)^def `)
	// primordialActorGuard is the ONE guard spelling this gate accepts. Anchored
	// end to end on its own line so a mention in a comment, a guard buried in a
	// compound condition (`if False and op.actor != ...`), or any other
	// text that merely CONTAINS the global cannot pass for the check. The
	// canonical form is the only one in the corpus, and pinning it is what makes
	// the gate a structural test rather than a string search.
	primordialActorGuard = regexp.MustCompile(`(?m)^([ \t]*)if op\.actor != primordialActor\["[a-z]+"\]:[ \t]*$`)
	// primordialActorAssign anchors an ASSIGNMENT to the predeclared global —
	// the shadowing bypass. starlark-go binds a function-local name over a
	// predeclared one for the whole function, so `primordialActor = {...}` turns
	// every guard below it into a self-comparison. The trailing [^=] keeps `==`
	// out; a `!=` never reaches the `=` because the `!` fails the character class
	// before it.
	primordialActorAssign = regexp.MustCompile(`(?m)^[ \t]*primordialActor[ \t]*(\[[^\]]*\])?[ \t]*=[^=]`)
	// actorGuardShape is the annotation a script carries to document the guard,
	// or to declare that a shared emission helper is discharged by its callers.
	actorGuardShape = regexp.MustCompile(`#\s*actor-guard:\s*\(([a-z-]+)\)(.*)$`)
	// branchDataAccess anchors where a branch first TOUCHES the subject its
	// payload named — a lazy Core KV read, a bounded link enumeration, or the
	// helper that resolves the subject's own aspects into adapter params. The
	// guard must precede this, not merely the emission: a guard that runs after
	// the read has already let an arbitrary caller use the op as a read (and
	// decrypt) oracle, even when nothing is ultimately sent.
	branchDataAccess = regexp.MustCompile(`kv\.Read\(|kv\.Links\(|resolve_subject_params\(`)
	// permissionOperationType / permissionScope read a pkgmgr.PermissionSpec
	// entry's declared grant scope out of the owning package's own sources.
	permissionOperationType = regexp.MustCompile(`OperationType:\s*"([A-Za-z0-9_]+)"`)
	permissionScope         = regexp.MustCompile(`Scope:\s*"([a-z]+)"`)
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
	derivedKeyCall = regexp.MustCompile(`\b(SHA256NanoID|NanoIDFromPCG)\(`)
	// derivedKeySymbol matches either symbol as a bare identifier, word-bounded
	// on BOTH sides so a longer name that merely contains one
	// (TestSHA256NanoID_Golden, NanoIDFromPCGSeed) is not a hit. Used to catch
	// the form derivedKeyCall cannot see: taking the function as a VALUE and
	// calling it through the binding on some other line.
	derivedKeySymbol = regexp.MustCompile(`\b(SHA256NanoID|NanoIDFromPCG)\b`)
	derivedKeyShape  = regexp.MustCompile(`//\s*derived-key:(.*)$`)

	// grantChangePostureImport matches the import spec for the capabilityread
	// package, capturing any local alias. A gate whose whole job is
	// default-deny must not be evadable by `import cr ".../capabilityread"`, so
	// the qualifier it looks for is RESOLVED from the file's own imports rather
	// than hardcoded. The dot-import form is captured too — its calls carry no
	// qualifier at all.
	//
	// The leading `import` keyword is CONSUMED, never captured. Both single-line
	// forms are legal and gofmt-stable — `import "path"` and `import cr "path"`
	// — and an optional-alias group with nothing to consume the keyword
	// mis-reads the first as an alias literally named "import" (so the gate
	// looks for `import.IsReadable(` and matches nothing) and fails the second
	// outright (so the whole FILE goes unscanned). Both are silent
	// un-gatings of a default-deny check, which is worse than the evasion the
	// resolver was added to close.
	grantChangePostureImport = regexp.MustCompile(`^\s*(?:import\s+)?(?:([A-Za-z_][A-Za-z0-9_]*|\.)\s+)?"github\.com/operatinggraph/lattice/internal/refractor/capabilityread"`)
	// grantChangePostureBare anchors a dot-imported (unqualified) call.
	grantChangePostureBare = regexp.MustCompile(`(^|[^.\w])IsReadable\(`)
	// grantChangePostureShape is the author's declaration of how THIS consumer
	// learns that a grant it already admitted has changed. The capture is the
	// justification a none-justified declaration owes.
	grantChangePostureShape = regexp.MustCompile(`//\s*grant-change-posture:\s*\(([a-z-]+)\)(.*)$`)

	// substrateConnectCall anchors a substrate.Connect( call in a cmd/** binary.
	// maxReconnectsField is the ConnectOpts field that call must set — the
	// omission this gate exists to catch: substrate.Connect only threads
	// MaxReconnects into the underlying nats.Option when the field is nonzero
	// (internal/substrate/conn.go), so a caller that never mentions it silently
	// inherits nats.go's own default (60 reconnect attempts, ~2s apart) instead
	// of a value anyone chose. After that default is exhausted the *nats.Conn
	// closes permanently — nats.go never retries a closed connection — so a
	// long-running component runs on as a zombie holding a dead NATS handle
	// (the confirmed cause of Refractor's two-hour outage after an nats-server
	// restart, one component of thirteen that never reconnected).
	substrateConnectCall = regexp.MustCompile(`\bsubstrate\.Connect\(`)
	maxReconnectsField   = regexp.MustCompile(`\bMaxReconnects\s*:`)
	// substrateConnectOptsAssign anchors a ConnectOpts built in its own
	// statement rather than inlined at the call (e.g. `connOpts :=
	// substrate.ConnectOpts{...}; substrate.Connect(ctx, connOpts)`) — the one
	// such shape in the tree today (cmd/facet/engine.go, whose ConnectOpts also
	// carries a TokenHandler built from local variables, which the inline shape
	// cannot express as cleanly). checkMaxReconnectsDeclared resolves the
	// variable back to this assignment rather than false-flagging the call for
	// not spelling MaxReconnects inline.
	substrateConnectOptsAssign = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*(?::=|=)\s*substrate\.ConnectOpts\s*\{`)
	// pkgmgrNewInstallerCall anchors a package-qualified pkgmgr.NewInstaller
	// call. internal/pkgmgr's own callers (including IsPackageInstalled) invoke
	// the unqualified NewInstaller and so never match this — the qualified form
	// only appears outside the package, which is exactly where SpecParser is
	// left nil unless the caller wires it by hand.
	pkgmgrNewInstallerCall = regexp.MustCompile(`\bpkgmgr\.NewInstaller\(`)
	// capabilityPlanConverge anchors a capability plan's Definition handed
	// straight to an unconditional convergence verb. A capability Definition
	// describes the proposal's own artifact and nothing else about the package
	// it names, while Apply's in-place branch and Upgrade both converge the
	// package onto whatever they are given — so applying one directly retires or
	// undeclares every key the proposal never mentioned.
	// Installer.ApplyCapabilityPlan is the entry point that carries the options
	// which make that safe (RefuseRemovals in both modes, RequireInstalled on
	// an upgrade), and MaterializedDefinition exists for INSPECTION — logging,
	// diffing, asserting — which its own doc comment states.
	//
	// Three shapes, one pattern. Upgrade is covered as well as Apply because it
	// is exported, takes no ApplyOptions at all, and converges unconditionally —
	// so it is the more destructive of the two doors, not the safer one. And the
	// argument matched is any `plan.` expression, not just the accessor: the
	// regression this rule exists to stop is an author re-exporting the
	// Definition field the accessor replaced and passing `plan.Definition`
	// again, which a pattern naming only MaterializedDefinition() would watch
	// sail past.
	//
	// Stated residual, matching this file's pragmatic-scanner posture: assigning
	// the value to a local and passing the local evades this regex. That is a
	// deliberate two-step rather than the shape an author reaches for by
	// default, and it is not worth a smarter pattern.
	capabilityPlanConverge = regexp.MustCompile(`\.(?:Apply|Upgrade)\([^\n]*\b(?:plan\.|MaterializedDefinition\(\))`)

	// sentinellessRefusal anchors a refusal built with fmt.Errorf that wraps no
	// sentinel. A refusal names a deterministic package state — it fails
	// identically on every retry — so every consumer has to be able to
	// recognize it by class. cmd/loupe's packageApplyStatus switches on
	// errors.Is and falls through to 502 for anything it cannot name, and 502
	// is the code its own front end treats as a transport blip worth retrying.
	// So a refusal without a sentinel tells the operator to retry a state that
	// will never change, and the message they were meant to read never reaches
	// them. Wrap a sentinel with %w and add it to the 409 arm.
	//
	// Not every refusal names a deterministic state, so the rule is a
	// default-deny with a declared exemption rather than a ban: a refusal
	// whose condition is TRANSIENT — a torn multi-key read, a lost
	// connection — is one a retry can legitimately clear, and 502 is the
	// honest answer for it. Declare that with `refusal-sentinel: (transient)`
	// and a reason, on the line or in the comment block above it, the same
	// shape read-posture and grant-change-posture use. The declaration is the
	// point: it makes the author say which kind of refusal they wrote.
	sentinellessRefusal = regexp.MustCompile(`fmt\.Errorf\("[^"]*\brefus(?:e|ed|es|ing)\b[^"]*"`)
	refusalSentinelDecl = regexp.MustCompile(`refusal-sentinel:\s*\(transient\)\s*\S`)

	// kvListAssign / kvGetCall / kvBatchShape — Fire 1 Inc 3's list-then-get
	// gate (script-live-read-round-trip-collapse-design.md), scoped to
	// internal/processor/** + internal/substrate/**: a KVListKeysPrefix/
	// KVListKeysFilter result fed to a per-key KVGet inside a range loop is
	// one round trip per matched key (the shape Fire 1 removed on both the
	// live-read paths it touched — KVGetMulti batches the value reads
	// instead). kvListAssign captures the LHS keys variable off either
	// call's short-var-decl/assignment; kvGetCall anchors the per-key GET
	// (deliberately NOT matching KVGetMulti — the "(" right after "KVGet"
	// excludes it); kvBatchShape is the declaration a site that genuinely
	// cannot batch must carry.
	kvListAssign = regexp.MustCompile(`(?m)^[ \t]*([A-Za-z_][A-Za-z0-9_]*)\s*(?:,\s*[A-Za-z_][A-Za-z0-9_]*)*\s*:?=\s*[^=\n]*\.(?:KVListKeysPrefix|KVListKeysFilter)\(`)
	kvGetCall    = regexp.MustCompile(`\.KVGet\(`)
	kvBatchShape = regexp.MustCompile(`//\s*kv-batch:\s*\(([a-z-]+)\)(.*)$`)
)

// kvBatchShapes are the declarable reasons a listed key set is read one key
// at a time instead of batched with KVGetMulti.
var kvBatchShapes = map[string]bool{
	// (single): the loop reads at most one key in practice (an already-bounded
	// set, e.g. a page known to hold 0 or 1 entries) — a batch call would cost
	// the same round trip for no benefit.
	"single": true,
	// (ordered): the loop's per-key reads must observe each other's writes in
	// sequence (a dependent chain) — KVGetMulti's one-snapshot semantics
	// would change what the loop can see.
	"ordered": true,
	// (bounded-1): the matched set is provably at most one key by construction
	// (a uniqueness-guarded lookup), so "batch" has no plural case to serve.
	"bounded-1": true,
}

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
// actorGuardShapes are the declarations a packages/ script may make about the
// primordial-actor guard. Deliberately only two, and only one of them
// discharges anything: (primordial) DOCUMENTS the canonical guard — the gate
// still requires the guard statement itself, so the annotation can never stand
// in for it — while (caller-guarded) is the escape hatch for a shared emission
// helper the gate cannot attribute to a single dispatch branch, and asserts
// that every caller pins the submitter first.
var actorGuardShapes = map[string]bool{
	"primordial":     true,
	"caller-guarded": true,
}

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

// pkgmgrPkg owns the pkgmgr.Installer, so it is the one place allowed to call
// its own NewInstaller unqualified (see pkgmgrInstallerScoped).
const pkgmgrPkg = "internal/pkgmgr/"

// substratePkg owns the *nats.Conn and its single-slot connection-state
// handlers, so it is the one place allowed to set them; every other component
// registers through substrate.Conn.OnConnectionStateChange.
const substratePkg = "internal/substrate/"

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

// historyBlockHits reports the line each change-narration phrase STARTS on,
// matched over a whole comment block rather than one physical line.
//
// A wrapped doc comment breaks its sentences at whatever column the margin
// falls on, so a line-anchored match reads "…the default — every target
// installed before this" and "fire) is frozen table-only behavior…" as two
// clean lines and lets the narration straight through. Joining the block first
// is what makes the phrase visible; the line-anchored pass still runs beside
// this one, because it is the pass that sees a trailing comment on a code line.
//
// Only comment-ONLY lines join. A run of trailing comments on consecutive code
// lines is not one sentence, and joining those would manufacture a phrase out
// of two unrelated remarks.
//
// A `/* … */` block counts as one such run. The tree writes `//` almost without
// exception, but a gate the other comment syntax walks straight through is not
// a gate, and the block form is exactly where wrapped prose lives.
func historyBlockHits(lines []string) map[int]bool {
	hits := map[int]bool{}
	for i := 0; i < len(lines); i++ {
		if !isGoCommentLine(lines[i]) && !opensBlockComment(lines[i]) {
			continue
		}
		var segs []string
		var at []int
		for i < len(lines) {
			line := lines[i]
			if opensBlockComment(line) {
				// Consume through the closing marker; the run ends there
				// whether or not the next line is a comment too.
				for i < len(lines) {
					segs = append(segs, goCommentBody(lines[i]))
					at = append(at, i+1)
					closed := strings.Contains(stripBlockOpen(lines[i]), "*/")
					i++
					if closed {
						break
					}
				}
				continue
			}
			if !isGoCommentLine(line) {
				break
			}
			segs = append(segs, goCommentBody(line))
			at = append(at, i+1)
			i++
		}
		joined := strings.Join(segs, " ")
		for _, loc := range historyPhraseOnly.FindAllStringIndex(joined, -1) {
			off := 0
			for k, s := range segs {
				// The +1 is the separator this join inserted; a match landing
				// on it belongs to the segment that follows.
				if off+len(s) > loc[0] {
					hits[at[k]] = true
					break
				}
				off += len(s) + 1
			}
		}
	}
	return hits
}

// isGoCommentLine reports whether a line is nothing but a Go line comment.
func isGoCommentLine(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "//")
}

// opensBlockComment reports whether a line begins a `/* … */` comment, with no
// code ahead of it on the same line.
func opensBlockComment(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "/*")
}

// stripBlockOpen drops a leading `/*` so a one-line `/* … */` is not read as
// closing before it opened.
func stripBlockOpen(line string) string {
	return strings.TrimPrefix(strings.TrimSpace(line), "/*")
}

// goCommentBody strips a line's comment markers and surrounding space, leaving
// the prose the block walk joins. It handles the `//` form, the `/*` opener, a
// `*/` closer, and the leading `*` that gofmt puts on a block's inner lines.
func goCommentBody(line string) string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "//")
	t = strings.TrimPrefix(t, "/*")
	t = strings.TrimSuffix(strings.TrimSpace(t), "*/")
	if strings.HasPrefix(t, "*") {
		t = strings.TrimPrefix(t, "*")
	}
	return strings.TrimSpace(t)
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

// isUnderCmd reports whether path has a "cmd" path segment — every binary,
// platform or vertical, whose connection lifecycle the max-reconnects gate
// covers (unlike verticalAppCmd, it does not exclude platform binaries: the
// omission this gate catches hit refractor, a platform binary).
func isUnderCmd(path string) bool {
	for _, p := range strings.Split(filepath.ToSlash(path), "/") {
		if p == "cmd" {
			return true
		}
	}
	return false
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
		files = trackedGoFiles(strict)
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
	// Weaver-target prose is a packages/ authoring rule: the literals live only
	// there, and internal/pkgmgr's own fixtures deliberately exercise the
	// description-less shape the installer still supports.
	if !isTest && (strings.HasPrefix(slash, "packages/") || strings.Contains(slash, "/packages/")) {
		out = append(out, checkWeaverTargetDescribed(path, string(data))...)
	}
	// max-reconnects scope: every non-test cmd/** binary. A test's
	// substrate.Connect targets an ephemeral embedded fixture torn down at test
	// end, where reconnect budgets are meaningless; a shipped binary's connection
	// is expected to outlive transient NATS trouble (or say explicitly that it
	// should not).
	if !isTest && isUnderCmd(slash) {
		out = append(out, checkMaxReconnectsDeclared(path, string(data))...)
	}
	// internal/spike holds standalone `main` benchmarks with no *testing.T, so the
	// fixture (which is t-bound by construction) is not available to them.
	embeddedNATSScoped := !strings.HasPrefix(slash, natsfixturePkg) && !strings.HasPrefix(slash, "internal/spike/")
	// natsfixture proves the bare default's behavior, so it dials directly.
	bareConnectScoped := isTest && !strings.HasPrefix(slash, natsfixturePkg)
	// pkgmgrInstallerScoped restricts pkgmgr.NewInstaller to its sanctioned
	// callers: internal/pkgmgr itself (including IsPackageInstalled, which
	// calls the unqualified form and so never matches pkgmgrNewInstallerCall
	// anyway), the two production entry points that wire SpecParser by hand
	// (cmd/lattice-pkg, cmd/loupe), and internal/testutil, whose NewInstaller
	// wraps pkgmgr.NewInstaller with SpecParser already wired.
	pkgmgrInstallerScoped := !strings.HasPrefix(slash, pkgmgrPkg) &&
		!strings.HasPrefix(slash, "internal/testutil/") &&
		!strings.HasPrefix(slash, "cmd/lattice-pkg/") &&
		!strings.HasPrefix(slash, "cmd/loupe/")
	// internal/pkgmgr owns both sides of this seam — ApplyCapabilityPlan hands
	// the plan's own definition to Apply, which is the sanctioned call — so the
	// rule binds every caller outside it. scripts/lint-*.go are excluded on the
	// same idiom (and for the same reason) as derivedKeyScoped/historyScoped
	// above: their own self-test fixtures are string literals carrying the
	// banned shape.
	materializedDefinitionScoped := !strings.HasPrefix(slash, pkgmgrPkg) &&
		!strings.HasPrefix(slash, "scripts/lint-")
	// The refusal-sentinel rule binds internal/pkgmgr, which is where the
	// package-state refusals live and where cmd/loupe's status mapper reads
	// them from. Tests are exempt: a fixture asserting on a refusal builds
	// throwaway errors that no consumer maps.
	refusalSentinelScoped := strings.HasPrefix(slash, pkgmgrPkg) &&
		!strings.HasSuffix(slash, "_test.go")
	// internal/substrate OWNS the connection-state handler slots and is the
	// one place allowed to set them; everyone else registers through
	// OnConnectionStateChange. Scoped to non-test files: a test that builds
	// its own throwaway *nats.Conn and watches it directly is nobody else's
	// signal to lose.
	connStateScoped := !isTest && !strings.HasPrefix(slash, substratePkg)
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
	// derivedKeyScoped — G2 bans the derivation at SUBMITTER call sites. The
	// exclusions are exactly two kinds, and no wider: the narrow ownership
	// allowlist (derivationOwners — the trees that DEFINE or EXPOSE the
	// primitive, each carrying its own reason) and packages/, the DDL side,
	// which is precisely where a derivation is supposed to happen.
	// scripts/lint-*.go are excluded because their own self-test fixtures are
	// string literals containing the banned call.
	//
	// scripts/ is otherwise IN scope: seed/demo scripts submit real operations,
	// and one of them (seed-showcase) carried a live hand-ported derivation that
	// a blanket scripts/ exclusion hid. internal/ is in scope for the same
	// reason: internal/gateway and internal/objectmanager are submitters, and a
	// tree-wide boolean over internal/ amnestied them for holding the same
	// import as the package that defines the primitive.
	derivedKeyScoped := !derivationOwned(slash) && !strings.HasPrefix(slash, "packages/") &&
		!strings.HasPrefix(slash, "scripts/lint-")
	var derivedKeyLines []string
	if derivedKeyScoped {
		derivedKeyLines = strings.Split(string(data), "\n")
	}
	// grant-change-posture scope: every non-test .go file. Deliberately not
	// narrowed to the package that holds today's single call site — the whole
	// point is to bind the NEXT consumer of the D1 read-grant projection,
	// wherever it is written. This file is excluded because its own self-test
	// fixtures below carry the very call shape the gate denies.
	grantChangeScoped := !isTest && !strings.HasPrefix(slash, "scripts/lint-")
	var grantChangeLines []string
	var grantChangeCall *regexp.Regexp
	if grantChangeScoped {
		grantChangeLines = strings.Split(string(data), "\n")
		grantChangeCall = grantChangeCallPattern(grantChangeLines)
		grantChangeScoped = grantChangeCall != nil
	}
	// The file's own validated-target exemption helpers, derived from the
	// script text rather than a hardcoded name list (see exemptionHelpers).
	var exemptHelpers map[string]bool
	// Which annotation, if any, covers each line — one map per annotation kind,
	// each resolved against the annotated statement's own block
	// (see annotationSpans).
	var postureAt, authCtxAt, workplaceAt, actorGuardAt map[int]annotation
	if postureScoped {
		out = append(out, checkPrimordialActorGuard(path, string(data))...)
		exemptHelpers = exemptionHelpers(data)
		lines := strings.Split(string(data), "\n")
		postureAt = annotationSpans(lines, readPosture)
		authCtxAt = annotationSpans(lines, authCtxTargetShape)
		workplaceAt = annotationSpans(lines, workplaceExemptShape)
		actorGuardAt = annotationSpans(lines, actorGuardShape)
	}
	// kv-batch scope: the Starlark write path's own list-then-get sites Fire
	// 1 collapsed (script-live-read-round-trip-collapse-design.md §5 Inc 3)
	// — internal/processor + internal/substrate, non-test. NOT the wider
	// ~85-site corpus (cmd/loupe, the vertical apps, the pkgmgr installer,
	// weaver/loom state scans, the rule-engine anchor scans): that is the
	// standing `[Perf]` row's own migration to do before this gate could
	// widen (design §10).
	kvBatchScoped := !isTest && (strings.HasPrefix(slash, "internal/processor/") || strings.HasPrefix(slash, "internal/substrate/"))
	if kvBatchScoped {
		out = append(out, checkListThenGet(path, string(data), annotationSpans(strings.Split(string(data), "\n"), kvBatchShape))...)
	}
	// A lint script's source is fixture text for this check: these scripts detect
	// change-narration comments and DOCUMENT the shapes they detect, so their own
	// comments quote the banned phrases legitimately (lint-board's header lists
	// `"Was:"` among the cell shapes it fails). Same exemption idiom, and the same
	// reason, as derivedKeyScoped/grantChangeScoped above.
	historyScoped := !strings.HasPrefix(slash, "scripts/lint-")
	var historyBlock map[int]bool
	if historyScoped {
		historyBlock = historyBlockHits(strings.Split(string(data), "\n"))
	}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	ln := 0
	// transientRefusalDeclared carries a `refusal-sentinel: (transient)`
	// declaration from the comment block above a statement onto that statement,
	// and is cleared by the first line that is neither a comment nor the
	// declaration itself — so a declaration cannot reach past the refusal it
	// sits on and amnesty a later one.
	transientRefusalDeclared := false
	for sc.Scan() {
		ln++
		line := sc.Text()
		// Read the declaration BEFORE clearing it: the refusal it covers is
		// itself a non-comment line, so clearing first would drop the
		// declaration on the very statement it was written for.
		declaredTransient := transientRefusalDeclared || refusalSentinelDecl.MatchString(line)
		switch {
		case refusalSentinelDecl.MatchString(line):
			transientRefusalDeclared = true
		case !isCommentLine(line) && strings.TrimSpace(line) != "":
			transientRefusalDeclared = false
		}
		if historyScoped && (historyComment.MatchString(line) || historyBlock[ln]) {
			out = append(out, finding{file: path, line: ln, msg: "history/changelog comment — git blame + the commit message are the record"})
		}
		if reviewFindingLabel.MatchString(line) {
			out = append(out, finding{file: path, line: ln, msg: "review-finding-label comment — findings live in the review record and the commit message, not code comments"})
		}
		if historyScoped && !isCommentLine(line) && deadConjunct.MatchString(line) {
			out = append(out, finding{file: path, line: ln, msg: "boolean literal wired into a condition (`if false && …` / `if true || …`) — the branch can never run, or always runs. This is a planted revert-proof mutation left behind; restore the condition. A genuinely constant condition is written as the constant"})
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
		if connStateScoped && connStateHandlerCall.MatchString(line) {
			out = append(out, finding{file: path, line: ln, msg: "nats.go connection-state handler set outside internal/substrate — each of these is a SINGLE slot on the *nats.Conn, so this silently unregisters whatever substrate (or another component) already set, and the loser keeps serving as though the connection never dropped, with no error and no log; register through substrate.Conn.OnConnectionStateChange(func(connected bool)) instead, which any number of listeners may hold"})
		}
		if embeddedNATSScoped && embeddedNATSCtor.MatchString(line) {
			out = append(out, finding{file: path, line: ln, msg: "hand-rolled embedded NATS fixture — a bare nats.Connect inherits nats.go's 2s whole-handshake deadline with no retry, so a host stall fails a random untouched package with `read tcp ...: i/o timeout`; use natsfixture.Server(t) / natsfixture.StartServer(t) (internal/natsfixture)"})
		}
		if pkgmgrInstallerScoped && pkgmgrNewInstallerCall.MatchString(line) {
			out = append(out, finding{file: path, line: ln, msg: "pkgmgr.NewInstaller call outside its sanctioned callers — SpecParser stays nil unless the caller wires it by hand, and a nil SpecParser silently disables the install-time lens label-cap gate (internal/pkgmgr/lenslabelcap.go); use testutil.NewInstaller(conn, adminActor) (internal/testutil), which wires it"})
		}
		if materializedDefinitionScoped && capabilityPlanConverge.MatchString(line) {
			out = append(out, finding{file: path, line: ln, msg: "capability-apply: a capability plan's Definition passed to Installer.Apply/Upgrade — a capability Definition describes one artifact, and both verbs converge the package onto whatever they are given, so this retires or undeclares every declared key the proposal never mentioned; apply a plan with inst.ApplyCapabilityPlan(ctx, plan), which sets RefuseRemovals (both modes) and RequireInstalled (upgradeExisting). MaterializedDefinition() is for inspection"})
		}
		if refusalSentinelScoped && sentinellessRefusal.MatchString(line) && !strings.Contains(line, "%w") &&
			!declaredTransient {
			out = append(out, finding{file: path, line: ln, msg: "refusal-sentinel: a refusal built with fmt.Errorf and no wrapped sentinel — a refusal names a deterministic package state, so every consumer must recognize it by class; cmd/loupe's packageApplyStatus falls through to 502 for an error it cannot name, which is the code its front end retries. Wrap a sentinel with %w and add it to the 409 arm"})
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
		if grantChangeScoped {
			out = append(out, checkGrantChangePosture(path, ln, line, grantChangeCall, grantChangeLines)...)
		}
		if postureScoped {
			out = append(out, checkReadPosture(path, ln, line, postureAt[ln], fileMutates)...)
			out = append(out, checkAuthContextTarget(path, ln, line, authCtxAt[ln])...)
			out = append(out, checkWorkplaceExempt(path, ln, line, workplaceAt[ln], exemptHelpers)...)
			out = append(out, checkActorGuardAnnotations(path, ln, line, actorGuardAt[ln])...)
		}
	}
	return out
}

// lensScanWindow bounds how far checkLensProtectedByDefault walks backward/
// forward from an Adapter match to find its composite literal's enclosing
// braces — a safety cap against pathological input, well beyond any real
// LensSpec entry's size.
const lensScanWindow = 8000

// checkPrimordialActorGuard flags a `Scope:"any"` operation whose script sends
// an `external.<adapter>` event without first pinning `op.actor` to a trusted
// platform engine (declared-read-scope-authorization-design.md §12).
//
// The hazard is a mismatch between what a grant admits and what an op assumes.
// These ops are each dispatched by exactly ONE engine — Loom relays the
// externalTask instanceOps, Weaver submits the directOps — but the grant that
// carries them is operator/`Scope:"any"`, i.e. every operator-role holder. The
// op then takes the keys it acts on from its own payload, reads their data, and
// copies it into an `external.<adapter>` event body, which leaves the platform.
// So an actor the op never contemplated can name any subject it likes and have
// that subject's data delivered to a vendor. Shape validation and a liveness
// check do not touch this: both answer "is the subject well-formed and alive",
// never "may THIS submitter dispatch for it".
//
// The trigger is the EGRESS, not any particular helper. An earlier revision
// anchored on `resolve_subject_params(`, which bound only the two ops that
// happen to call that helper; the other five hand-assemble the same event body
// from the same payload-named keys and went unbound — including the richest
// exposure of the seven (lease-signing's docGen instanceOp, which ships the
// application's signature, its applicant, the unit's address and the lease
// terms). `"class": "external.` is the shape the hazard actually has, and
// retargeting to it is what surfaced the three reminder ops the original
// census missed entirely.
//
// What discharges it is a STRUCTURAL guard, not a mention. The gate requires
// the canonical statement
//
//	if op.actor != primordialActor["<engine>"]:
//	    fail(...)
//
// on its own non-comment line, with a `fail(` in its body, positioned before the
// earlier of the branch's first data access (branchDataAccess) and its emission.
// Guarding only the emission would be too weak: a guard that runs after the
// subject has been read still lets an arbitrary caller use the op as a read —
// and, for a sensitive aspect, a decrypt — oracle even when nothing is sent.
//
// Requiring only that the branch CONTAIN the text `primordialActor[` — what the
// earlier revision did — is satisfied by a comment, by a dead `if False and …`
// branch, by a guard placed after the data has been read or sent, by a guard
// whose body does not refuse, and by a local `primordialActor = {...}` that
// shadows the predeclared global for the whole function (starlark-go binds a
// function-local assignment over a predeclared name). Each is checked here and
// pinned by its own self-test case; the shadow check is file-wide, since the
// shadowing assignment need not sit in the branch it defeats.
//
// KNOWN LIMITATION — this gate does NOT trace call graphs. Branch attribution is
// textual: an emission is attributed to the nearest preceding `if ot == "<Op>":`
// header at the same indent. A helper `def` that emits and is called from
// several branches is attributed to whichever branch precedes its definition,
// not to its callers, so a helper called from BOTH a guarded and an unguarded
// branch is judged by the wrong one. Two mitigations, both fail-closed: an
// emission separated from its branch header by a top-level `def` is treated as
// living in a helper and demands the `(caller-guarded)` declaration below, and
// an emission with no preceding branch header at all does the same. A script
// author must therefore keep the emission in its own dispatch branch, or say in
// writing why the guard lives at the call sites. Closing the gap properly needs
// a real Starlark parser, which is out of proportion to a corpus where every
// emission today sits directly in its own branch.
//
// Fail-closed throughout, per this file's default-deny doctrine: an emission
// whose branch cannot be identified is reported, and an operation whose declared
// Scope cannot be read (see packageOpScopes) is treated as "any".
func checkPrimordialActorGuard(path, src string) []finding {
	var out []finding
	lines := strings.Split(src, "\n")
	declared := annotationSpans(lines, actorGuardShape)

	// The shadow check is file-wide and unconditional: a local rebinding of
	// `primordialActor` anywhere in a script makes EVERY guard in that script a
	// comparison against a script-chosen value, so it is reported even when the
	// file's own guards are otherwise well-formed.
	for _, loc := range primordialActorAssign.FindAllStringIndex(src, -1) {
		ln := 1 + strings.Count(src[:loc[0]], "\n")
		if isCommentLine(lines[ln-1]) {
			continue
		}
		out = append(out, finding{file: path, line: ln,
			msg: "primordial-actor: `primordialActor` is ASSIGNED here, which shadows the predeclared platform " +
				"global for the rest of the function (starlark-go binds a function-local name over a predeclared " +
				"one). Every `op.actor != primordialActor[...]` comparison in this script then tests a value the " +
				"script itself chose, so the guard proves nothing. Rename the local"})
	}

	for _, m := range externalEventEmit.FindAllStringIndex(src, -1) {
		pos := m[0]
		ln := 1 + strings.Count(src[:pos], "\n")
		if isCommentLine(lines[ln-1]) {
			continue
		}
		shape, why, hasDecl := declaredActorGuard(declared, ln)
		if hasDecl && shape == "caller-guarded" && strings.TrimSpace(why) != "" {
			continue
		}
		op, branchStart, branchEnd, ok := enclosingOpBranch(src, pos)
		// A top-level `def` between the branch header and the emission means the
		// emission is in a helper that merely FOLLOWS that branch, not in it.
		inHelper := ok && topLevelDef.MatchString(src[branchStart:pos])
		if !ok || inHelper {
			out = append(out, finding{file: path, line: ln,
				msg: "primordial-actor: an `external.<adapter>` emission that does not sit directly inside its own " +
					"`if ot == \"<Op>\":` dispatch branch — this gate attributes an emission textually and does not " +
					"trace calls, so it cannot tell which operations reach this one or whether each of them pins its " +
					"submitter. Move the emission into its operation's branch, or declare " +
					"`# actor-guard: (caller-guarded) <why every caller pins op.actor first>` " +
					"(declared-read-scope-authorization-design.md §12)"})
			continue
		}
		if !packageOpScopeIsAny(path, op) {
			continue
		}
		// The guard must precede the earlier of the branch's first data access
		// and the emission — a guard that runs after the subject has been read
		// still leaves the op usable as a read/decrypt oracle by any caller.
		branch := src[branchStart:branchEnd]
		limit := pos - branchStart
		if da := branchDataAccess.FindStringIndex(branch); da != nil && da[0] < limit {
			limit = da[0]
		}
		if guardedBefore(branch, limit) {
			continue
		}
		out = append(out, finding{file: path, line: ln,
			msg: "primordial-actor: " + op + " is granted at Scope:\"any\" and sends an `external.<adapter>` event " +
				"built from keys its own payload names, but no `if op.actor != primordialActor[\"<engine>\"]:` guard " +
				"with a fail() body precedes the emission in this branch. Scope:\"any\" admits every holder of the " +
				"granted role, not just the one engine that dispatches this op, so any of them can name an arbitrary " +
				"subject and have that subject's data forwarded to the vendor. Add the guard as the branch's first " +
				"statement — `if op.actor != primordialActor[\"loom\"]: fail(\"AuthDenied: ...\")` — and annotate it " +
				"`# actor-guard: (primordial) <why>` (declared-read-scope-authorization-design.md §12). A mention of " +
				"primordialActor in a comment, in a dead branch, or AFTER the emission does not count, and neither " +
				"does shape or liveness validation: none of them answers who may dispatch for the subject"})
	}
	return out
}

// declaredActorGuard resolves the `# actor-guard:` annotation covering line ln,
// validating the shape vocabulary. An unknown shape or a missing `<why>` is
// reported by checkActorGuardAnnotations, so this only reports what was found.
func declaredActorGuard(spans map[int]annotation, ln int) (shape, why string, ok bool) {
	a, found := spans[ln]
	if !found || a.shape == "" {
		return "", "", false
	}
	return a.shape, a.why, true
}

// checkActorGuardAnnotations keeps the `# actor-guard:` vocabulary closed, the
// same way the authcontext-target and read-posture gates keep theirs. An
// annotation is the one thing that can discharge the egress rule, so a typo in
// the shape must be a finding rather than a silently inert comment.
func checkActorGuardAnnotations(path string, ln int, line string, declared annotation) []finding {
	if !actorGuardShape.MatchString(line) {
		return nil
	}
	shape, why := declared.shape, declared.why
	if shape == "" {
		shape, why = "", ""
		if m := actorGuardShape.FindStringSubmatch(line); m != nil {
			shape, why = m[1], m[2]
		}
	}
	if !actorGuardShapes[shape] {
		return []finding{{file: path, line: ln,
			msg: "actor-guard: unknown shape (" + shape + ") — the declared shapes are (primordial), documenting " +
				"the canonical `op.actor != primordialActor[...]` guard, and (caller-guarded), asserting that every " +
				"caller of a shared emission helper pins the submitter first"}}
	}
	if strings.TrimSpace(why) == "" {
		return []finding{{file: path, line: ln,
			msg: "actor-guard: a (" + shape + ") declaration must state its `<why>` — what pins the submitter, in " +
				"the author's own words"}}
	}
	return nil
}

// guardedBefore reports whether `branch` contains the canonical primordial-actor
// guard, with a fail() in its body, at an offset before `emitAt`.
//
// The fail() requirement is what separates a guard from a branch that merely
// mentions the global: a comparison whose body does not refuse lets execution
// fall straight through to the emission.
func guardedBefore(branch string, emitAt int) bool {
	for _, loc := range primordialActorGuard.FindAllStringSubmatchIndex(branch, -1) {
		if loc[0] >= emitAt {
			continue
		}
		if guardBodyRefuses(branch, loc[1], len(branch[loc[2]:loc[3]])) {
			return true
		}
	}
	return false
}

// guardBodyRefuses reports whether the block opened at bodyStart (just past the
// guard's `:`) refuses — its first non-blank, non-comment line is indented
// deeper than the guard and calls fail().
func guardBodyRefuses(branch string, bodyStart, guardIndent int) bool {
	for _, line := range strings.Split(branch[bodyStart:], "\n") {
		if strings.TrimSpace(line) == "" || isCommentLine(line) {
			continue
		}
		if indentWidth(line) <= guardIndent {
			return false
		}
		return strings.Contains(line, "fail(")
	}
	return false
}

// enclosingOpBranch returns the operationType and the source span of the
// `if ot == "<Op>":` dispatch branch containing pos.
//
// The branch is bounded by the next branch header at the SAME indent (a sibling
// dispatch arm) or the end of the source — the same delimiting the packages'
// own dispatch-read guard tests use. Indent-equality is what keeps a nested
// `if ot ==` inside a helper from prematurely closing the arm.
func enclosingOpBranch(src string, pos int) (op string, start, end int, ok bool) {
	var (
		headerStart int
		headerOp    string
		headerIndet string
		found       bool
	)
	for _, loc := range opBranchHeader.FindAllStringSubmatchIndex(src[:pos], -1) {
		headerStart = loc[0]
		headerIndet = src[loc[2]:loc[3]]
		headerOp = src[loc[4]:loc[5]]
		found = true
	}
	if !found {
		return "", 0, 0, false
	}
	rest := src[headerStart:]
	relEnd := len(rest)
	for _, loc := range opBranchHeader.FindAllStringSubmatchIndex(rest, -1) {
		if loc[0] == 0 {
			continue
		}
		if src[headerStart+loc[2]:headerStart+loc[3]] == headerIndet {
			relEnd = loc[0]
			break
		}
	}
	if headerStart+relEnd <= pos {
		// pos fell past this arm's close — it belongs to no dispatch branch.
		return "", 0, 0, false
	}
	return headerOp, headerStart, headerStart + relEnd, true
}

// packageOpScopes indexes one package directory's declared permission scopes by
// operation type, read from the pkgmgr.PermissionSpec literals in its own
// non-test sources. Cached per directory: the scan is per-file but the answer
// is per-package.
var packageOpScopes = map[string]map[string]string{}

// packageOpScopeIsAny reports whether the package owning `path` declares `op`
// at Scope:"any".
//
// Unreadable directory, unparseable sources, or an operation with no permission
// entry all answer TRUE. That is the fail-closed direction for this gate: the
// alternative would let a package silently exempt itself by putting its
// permissions somewhere the scan cannot see, and it is also what makes the
// gate's own self-test fixtures (whose paths have no directory on disk) subject
// to the rule they pin.
func packageOpScopeIsAny(path, op string) bool {
	dir := filepath.Dir(path)
	scopes, cached := packageOpScopes[dir]
	if !cached {
		scopes = map[string]string{}
		entries, err := os.ReadDir(dir)
		if err == nil {
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
					continue
				}
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					continue
				}
				collectPermissionScopes(string(data), scopes)
			}
		}
		packageOpScopes[dir] = scopes
	}
	declared, ok := scopes[op]
	return !ok || declared == "any"
}

// collectPermissionScopes records each `OperationType: "<op>"` field's
// neighbouring `Scope: "<scope>"` from a permission-spec source. The scope is
// taken from the text between this OperationType and the next one, so entries
// in one slice literal cannot borrow each other's scope regardless of
// formatting. An entry declaring the same op twice keeps the WIDEST reading:
// "any" is never overwritten by a narrower later entry.
func collectPermissionScopes(src string, into map[string]string) {
	locs := permissionOperationType.FindAllStringSubmatchIndex(src, -1)
	for i, loc := range locs {
		op := src[loc[2]:loc[3]]
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		scope := ""
		if m := permissionScope.FindStringSubmatch(src[loc[1]:end]); m != nil {
			scope = m[1]
		}
		if scope == "" {
			continue
		}
		if prev, ok := into[op]; ok && (prev == "any" || scope != "any") {
			continue
		}
		into[op] = scope
	}
}

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

// weaverTargetScanWindow bounds how far checkWeaverTargetDescribed walks
// backward/forward from a TargetID: field to find its literal's braces — a
// safety cap, well beyond any real WeaverTargetSpec entry (the largest in the
// corpus, lease-signing's leaseApplicationComplete, is under 6000 bytes).
const weaverTargetScanWindow = 8000

// checkWeaverTargetDescribed flags a pkgmgr.WeaverTargetSpec literal under
// packages/ that declares no Description. The installer keeps the field
// optional — an aspect is emitted only when it is set — so nothing else
// refuses a nameless target; but a target with no prose reaches an operator as
// a bare KV token on the Weaver roster and as an unlabelled row in the review
// queue, with the invariant it keeps true recoverable only by reading its lens
// cypher.
//
// The literal's span is found by balanced-brace walking out from the TargetID:
// field — the same technique checkLensProtectedByDefault uses for a LensSpec
// entry, and with the same residual risk: a stray brace inside a string or
// comment within the entry throws the walk off. No target literal in this
// codebase has one (gap bodies are field lists and named consts), so an
// AST rewrite is not bought for it here.
func checkWeaverTargetDescribed(path, src string) []finding {
	var out []finding
	for _, m := range weaverTargetIDField.FindAllStringIndex(src, -1) {
		pos := m[0]
		backLimit := pos - weaverTargetScanWindow
		if backLimit < 0 {
			backLimit = 0
		}
		entryStart, balance := -1, 0
		for i := pos - 1; i >= backLimit && entryStart == -1; i-- {
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
		}
		if entryStart == -1 {
			continue
		}
		fwdLimit := entryStart + weaverTargetScanWindow
		if fwdLimit > len(src) {
			fwdLimit = len(src)
		}
		entryEnd, balance := -1, 1
		for i := entryStart + 1; i < fwdLimit && entryEnd == -1; i++ {
			switch src[i] {
			case '{':
				balance++
			case '}':
				balance--
				if balance == 0 {
					entryEnd = i
				}
			}
		}
		if entryEnd == -1 {
			continue
		}
		if !weaverTargetDescField.MatchString(src[entryStart : entryEnd+1]) {
			line := strings.Count(src[:pos], "\n") + 1
			out = append(out, finding{file: path, line: line, msg: "WeaverTargetSpec declares no Description — an installed target reaches operators as a bare targetId on the Weaver roster and an unlabelled row in the review queue; say in plain language what invariant this target keeps true"})
		}
	}
	return out
}

// maxReconnectsScanWindow bounds how far checkMaxReconnectsDeclared walks
// forward from a substrate.Connect( match to find the call's closing paren —
// a safety cap against pathological input, well beyond any real ConnectOpts
// literal's size.
const maxReconnectsScanWindow = 4000

// checkMaxReconnectsDeclared flags a substrate.Connect( call under cmd/**
// whose argument list never mentions MaxReconnects. It walks forward from the
// call's own opening paren to the matching closing paren via balanced-paren
// counting (correct regardless of how the ConnectOpts{} literal is
// formatted), then checks only that span — so a MaxReconnects field on a
// DIFFERENT, later call is never mistaken for this one's declaration.
//
// The value is deliberately not inspected: -1 (reconnect forever — the right
// answer for a long-running daemon; a closed *nats.Conn never recovers on its
// own) and a small finite value (fail fast — the right answer for a one-shot
// CLI invocation) are both acceptances. The only thing this gate refuses is
// silence, which is what let 3 of 17 cmd/** binaries inherit nats.go's own
// default (60 attempts, ~2s apart) with nobody having chosen it — the gap
// that left Refractor's lens engine running for two hours as a zombie after
// an nats-server restart it never reconnected from.
func checkMaxReconnectsDeclared(path, src string) []finding {
	var out []finding
	for _, m := range substrateConnectCall.FindAllStringIndex(src, -1) {
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
		openParen := m[1] - 1
		fwdLimit := openParen + maxReconnectsScanWindow
		if fwdLimit > len(src) {
			fwdLimit = len(src)
		}
		callEnd := -1
		balance := 0
		for i := openParen; i < fwdLimit; i++ {
			switch src[i] {
			case '(':
				balance++
			case ')':
				balance--
				if balance == 0 {
					callEnd = i
				}
			}
			if callEnd != -1 {
				break
			}
		}
		if callEnd == -1 {
			continue
		}
		callSpan := src[openParen : callEnd+1]
		if maxReconnectsField.MatchString(callSpan) {
			continue
		}
		if !strings.Contains(callSpan, "ConnectOpts") {
			// The ConnectOpts value isn't inlined at the call (e.g. `connOpts :=
			// substrate.ConnectOpts{...}` in an earlier statement, then
			// `substrate.Connect(ctx, connOpts)`) — resolve the identifier back
			// to its own assignment before concluding the field is missing.
			if ident := lastIdentifier(src[openParen+1 : callEnd]); ident != "" {
				if declSpan, ok := findConnectOptsAssign(src, pos, ident); ok && maxReconnectsField.MatchString(declSpan) {
					continue
				}
			}
		}
		line := strings.Count(src[:pos], "\n") + 1
		out = append(out, finding{file: path, line: line, msg: "max-reconnects: substrate.Connect in cmd/** does not set MaxReconnects — omitting it inherits nats.go's own default (60 reconnect attempts, ~2s apart) instead of a value anyone chose, and once that budget is exhausted the connection closes PERMANENTLY (nats.go never retries a closed connection); a long-running component then keeps running as a zombie holding a dead NATS handle. Set MaxReconnects: -1 for a daemon that must reconnect indefinitely, or an explicit small finite value (with a comment on why fail-fast is correct here) for a short-lived CLI"})
	}
	return out
}

// kvBatchScanWindow bounds how far checkListThenGet looks forward from a
// KVListKeysPrefix/KVListKeysFilter call for a `for range <keys>` loop whose
// body issues a per-key KVGet — a safety cap well beyond any real hydration
// loop's size (mirrors lensScanWindow's role for checkLensProtectedByDefault).
const kvBatchScanWindow = 4000

// checkListThenGet flags a KVListKeysPrefix/KVListKeysFilter result fed to a
// per-key KVGet inside a `for range` loop over that result — one round trip
// per matched key, the shape Fire 1 collapsed with KVGetMulti on both
// live-read paths it touched (script-live-read-round-trip-collapse-design.md
// §5 Inc 3). Scoped by the caller to internal/processor/** +
// internal/substrate/** non-test files.
//
// For each list-call assignment, it captures the LHS keys variable, then
// walks forward for a `for ... := range <keysVar>` header and, from that
// header's own opening brace, walks the loop body via balanced-brace
// counting (the same technique checkLensProtectedByDefault uses for a
// LensSpec entry) — correct regardless of the loop body's own formatting or
// nested blocks. A body containing `.KVGet(` (never `.KVGetMulti(` — the "("
// immediately after "KVGet" excludes the longer name) is the finding, unless
// the list-call's own line carries a `// kv-batch: (single|ordered|
// bounded-1) <why>` declaration.
func checkListThenGet(path, src string, kvBatchAt map[int]annotation) []finding {
	var out []finding
	for _, m := range kvListAssign.FindAllStringSubmatchIndex(src, -1) {
		pos := m[0]
		lineStart := strings.LastIndexByte(src[:pos], '\n') + 1
		if strings.HasPrefix(strings.TrimSpace(src[lineStart:pos]), "//") {
			continue
		}
		callLine := strings.Count(src[:pos], "\n") + 1
		keysVar := src[m[2]:m[3]]
		if keysVar == "" || keysVar == "_" {
			continue
		}
		if a, ok := kvBatchAt[callLine]; ok {
			if !kvBatchShapes[a.shape] {
				out = append(out, finding{file: path, line: callLine, msg: "kv-batch: unknown shape (" + a.shape + ") — the declarable reasons a listed key set is read one key at a time are (single), (ordered), (bounded-1)"})
			}
			continue
		}
		fwdLimit := m[1] + kvBatchScanWindow
		if fwdLimit > len(src) {
			fwdLimit = len(src)
		}
		window := src[m[1]:fwdLimit]
		rangeRe := regexp.MustCompile(`\bfor\s+[A-Za-z_][A-Za-z0-9_]*\s*(?:,\s*[A-Za-z_][A-Za-z0-9_]*)?\s*:?=\s*range\s+` + regexp.QuoteMeta(keysVar) + `\b`)
		rm := rangeRe.FindStringIndex(window)
		if rm == nil {
			continue
		}
		openRel := strings.IndexByte(window[rm[1]:], '{')
		if openRel == -1 {
			continue
		}
		blockStart := rm[1] + openRel
		blockEnd := -1
		balance := 1
		for i := blockStart + 1; i < len(window); i++ {
			switch window[i] {
			case '{':
				balance++
			case '}':
				balance--
				if balance == 0 {
					blockEnd = i
				}
			}
			if blockEnd != -1 {
				break
			}
		}
		if blockEnd == -1 {
			continue
		}
		if !kvGetCall.MatchString(window[blockStart:blockEnd]) {
			continue
		}
		out = append(out, finding{file: path, line: callLine, msg: "kv-batch: " + keysVar + " is listed via KVListKeysPrefix/KVListKeysFilter then read with a per-key KVGet in a loop — one round trip per matched key; batch the value reads with KVGetMulti (script-live-read-round-trip-collapse-design.md Fire 1), or declare `// kv-batch: (single|ordered|bounded-1) <why>` on this line if a batch genuinely does not apply here"})
	}
	return out
}

// lastIdentifier returns the trailing bare identifier in a substrate.Connect
// call's argument list (e.g. "ctx, connOpts" -> "connOpts"), or "" if the last
// argument is not a single bare identifier (a struct literal, a field
// selector, a function call) — those shapes are handled by the inline
// ConnectOpts{} check instead, and are never worth chasing further.
func lastIdentifier(argList string) string {
	parts := strings.Split(argList, ",")
	last := strings.TrimSpace(parts[len(parts)-1])
	for i := 0; i < len(last); i++ {
		c := last[i]
		if !(c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return ""
		}
	}
	if last == "" {
		return ""
	}
	return last
}

// findConnectOptsAssign locates the composite literal of ident's nearest
// `ident := substrate.ConnectOpts{...}` (or `=`) assignment preceding
// callPos, and returns that literal's own text span (opening `{` through its
// matching `}`, via balanced-brace counting — the same technique
// checkLensProtectedByDefault uses for a LensSpec entry).
func findConnectOptsAssign(src string, callPos int, ident string) (string, bool) {
	bestBrace := -1
	for _, m := range substrateConnectOptsAssign.FindAllStringSubmatchIndex(src, -1) {
		if m[0] >= callPos {
			break
		}
		if src[m[2]:m[3]] != ident {
			continue
		}
		bestBrace = m[1] - 1 // index of the assignment's opening "{"
	}
	if bestBrace == -1 {
		return "", false
	}
	fwdLimit := bestBrace + maxReconnectsScanWindow
	if fwdLimit > len(src) {
		fwdLimit = len(src)
	}
	end := -1
	balance := 0
	for i := bestBrace; i < fwdLimit; i++ {
		switch src[i] {
		case '{':
			balance++
		case '}':
			balance--
			if balance == 0 {
				end = i
			}
		}
		if end != -1 {
			break
		}
	}
	if end == -1 {
		return "", false
	}
	return src[bestBrace : end+1], true
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

// derivationOwner is one tree excluded from the G2 derived-key ban because it
// OWNS the primitive rather than consuming it. A path alone is not an
// exemption: each entry states why that tree is where the derivation lives.
type derivationOwner struct {
	prefix string
	reason string
}

// derivationOwners — the only packages outside packages/ that may call
// SHA256NanoID / NanoIDFromPCG without a `// derived-key:` annotation.
//
// Each entry names ONE package, and covers only that package's own files —
// never its subpackages (see derivationOwned). The reasons below are
// file-shaped for a reason: they are about derive.go and starlark_builtins.go,
// and a subpackage of either tree is an ordinary consumer that happens to sit
// beneath the definition. internal/processor alone has a dozen subpackages, so
// a subtree exemption would silently amnesty far more code than any reason here
// can account for — and several consumers (gateway, objectmanager) are
// submitters whose derivations are exactly what G2 exists to hold to an
// individually re-checkable reason.
var derivationOwners = []derivationOwner{
	{
		prefix: "internal/substrate/",
		reason: "defines the primitive — derive.go IS SHA256NanoID and NanoIDFromPCG, and derive_test.go is their golden-vector proof. A gate over the definition would demand the definition justify itself to itself.",
	},
	{
		prefix: "internal/processor/",
		reason: "exposes the primitive to Starlark — starlark_builtins.go wires crypto.sha256NanoID and nanoid.new onto substrate, so the package's derive_reads can compute the class-(g) keys G2 pushes derivations toward. This is the sanctioned computer of a derived read, not a submitter.",
	},
}

// derivationOwned reports whether path is a DIRECT member of a package that
// owns the derivation primitive: under the entry's prefix with no further "/"
// in what remains. A subpackage (internal/processor/opwire/wire.go) is not
// owned, and neither is a sibling whose name merely starts the same way —
// "internal/substrateX/f.go" fails the prefix, which ends in the separator.
func derivationOwned(path string) bool {
	for _, o := range derivationOwners {
		if !strings.HasPrefix(path, o.prefix) {
			continue
		}
		if !strings.Contains(path[len(o.prefix):], "/") {
			return true
		}
	}
	return false
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
// A second form is caught alongside the call: taking either symbol as a
// FUNCTION VALUE (`f := substrate.SHA256NanoID`, a struct-literal field, an
// argument, a return) and calling it through that binding somewhere the call
// pattern cannot see. It is default-denied on the same terms and takes the same
// annotation, because the derivation still happens — one level of indirection
// away from the reason that has to justify it.
func checkDerivedKey(path string, ln int, lines []string) []finding {
	line := lines[ln-1]
	if isCommentLine(line) {
		return nil
	}
	form := ""
	switch {
	case derivedKeyCall.MatchString(line):
		form = "call"
	case derivedKeyValueTake(line):
		form = "value"
	default:
		return nil
	}
	switch derivedKeyReason(lines, ln-1) {
	case reasonGiven:
		return nil
	case reasonMissing:
		return []finding{{file: path, line: ln, msg: "derived-key: declaration carries no reason — name what this derives and why it is not a declared read the owning package should compute"}}
	}
	if form == "value" {
		return []finding{{file: path, line: ln, msg: "derived-key: undeclared derivation taken as a function VALUE — binding SHA256NanoID/NanoIDFromPCG to a variable, field, argument or return moves the call to a line this gate cannot match, which is the whole reason the ban is written at the call site. Call it where the reason for it lives, or declare `// derived-key: <what it derives and why the package cannot>` here"}}
	}
	return []finding{{file: path, line: ln, msg: "derived-key: undeclared content-addressed id derivation in a submitter — a key derived client-side from a payload is a class-(g) declared read the owning DDL's `derive_reads` should compute (Contract #2 §2.5); a hand-ported derivation is a second implementation of the package's normalization that nothing keeps in agreement. If this derives something that is NOT a declared read (an object id, a requestId), declare `// derived-key: <what it derives and why the package cannot>`"}}
}

// derivedKeyValueTake reports whether line names either banned symbol WITHOUT
// an immediate "(" — the function-value form.
//
// It runs over the line's code only, with string literals and any trailing
// comment blanked (maskGoLiterals). That masking is deliberately NOT applied to
// derivedKeyCall: a hand-ported derivation embedded in a Go string is still a
// derivation that has to explain itself, whereas a bare symbol NAME appears in
// ordinary prose — a t.Fatalf message saying which primitive drifted, a comment
// naming the builtin — and flagging those would be the annotate-the-noise
// failure that stops a default-deny gate from being read.
func derivedKeyValueTake(line string) bool {
	code := maskGoLiterals(line)
	for _, m := range derivedKeySymbol.FindAllStringIndex(code, -1) {
		if m[1] >= len(code) || code[m[1]] != '(' {
			return true
		}
	}
	return false
}

// maskGoLiterals replaces the CONTENTS of interpreted, raw and rune literals
// with spaces and drops any trailing line comment, leaving offsets unchanged so
// a match index still points at the original column. It reads one line in
// isolation, so a raw string spanning lines is masked only on its opening line;
// that is a conservative direction for a default-deny gate — an unmasked
// continuation line can only ever produce a finding to answer, never a silent
// pass.
func maskGoLiterals(line string) string {
	out := []byte(line)
	inQuote := byte(0)
	for i := 0; i < len(out); i++ {
		c := out[i]
		switch {
		case inQuote != 0:
			if c == '\\' && inQuote != '`' && i+1 < len(out) {
				out[i], out[i+1] = ' ', ' '
				i++
				continue
			}
			if c == inQuote {
				inQuote = 0
				continue
			}
			out[i] = ' '
		case c == '"' || c == '`' || c == '\'':
			inQuote = c
		case c == '/' && i+1 < len(out) && out[i+1] == '/':
			return string(out[:i])
		}
	}
	return string(out)
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

// grantChangePostureShapes are the declared ways a consumer of the D1
// read-grant projection learns that a grant it already admitted has changed.
//
//   - subscribed: the consumer's pipeline carries the producer-side change edge
//     (projection.IsReadGrantProducer wires it), so a transition re-drives it.
//   - swept: a standing healer re-asks on its own cadence, so the staleness is
//     bounded by a sweep cycle rather than by unrelated traffic.
//   - none-justified: neither, and the author says why that is acceptable here.
var grantChangePostureShapes = map[string]bool{
	"subscribed":     true,
	"swept":          true,
	"none-justified": true,
}

// checkGrantChangePosture default-denies one executable call site of
// capabilityread.IsReadable that does not declare how it learns a grant
// changed (personal-lens-grant-change-trigger-design.md §10.1; BLOCKING).
//
// The bug it exists to prevent is the one that produced that design: the
// Personal Lens gates every row on this projection, reads it live, and had no
// change edge — so a revoked grant stayed honoured until some unrelated Core KV
// event happened to re-drive the actor. That is invisible in review, because
// the call site reads exactly the same whether or not anything re-asks it.
//
// Like every gate in this file it does not CLASSIFY — it makes the author
// declare, and forgetting fails closed. It ships blocking rather than
// warn-first, unlike the read-posture gate it borrows its mechanical shape
// from: warn-first was right there because it met a corpus of existing debt,
// and is wrong here because the census is exactly one site, so a warn over a
// clean tree would be precisely the fingers-crossed state the gate ends.
//
// A `<why>` is required on every shape, not only none-justified: "subscribed"
// with nothing after it does not say subscribed to WHAT, which is the fact the
// next reader needs. That follows the shipped authcontext-target gate rather
// than the design's looser schema sketch.
func checkGrantChangePosture(path string, ln int, line string, call *regexp.Regexp, lines []string) []finding {
	if call == nil || isCommentLine(line) || !call.MatchString(line) {
		return nil
	}
	shape, why, declared := grantChangePosture(lines, ln-1)
	if !declared {
		return []finding{{file: path, line: ln,
			msg: "grant-change-posture: undeclared capabilityread.IsReadable call site — this reads the D1 read-grant projection as a security decision, and that projection is written by a DIFFERENT pipeline with no change notification of its own. Declare how this site learns a grant changed: `// grant-change-posture: (subscribed) <which producer edge re-drives it>`, `(swept) <which standing healer re-asks>`, or `(none-justified) <why staleness is acceptable here>` (personal-lens-grant-change-trigger-design.md §10.1)"}}
	}
	if !grantChangePostureShapes[shape] {
		return []finding{{file: path, line: ln,
			msg: "grant-change-posture: unknown shape (" + shape + ") — the declared ways a consumer learns a grant changed are (subscribed), (swept), and (none-justified)"}}
	}
	if strings.TrimSpace(why) == "" {
		return []finding{{file: path, line: ln,
			msg: "grant-change-posture: (" + shape + ") declaration carries no `<why>` — name the mechanism (or the accepted staleness), so the next reader need not re-derive whether anything re-asks this gate"}}
	}
	return nil
}

// grantChangeCallPattern resolves how THIS file would spell a call to
// capabilityread.IsReadable, from its own import spec, and returns the pattern
// that matches it — or nil when the file does not import the package at all,
// which is the common case and short-circuits the whole check.
//
// Resolving rather than hardcoding "capabilityread." is the difference between
// a default-deny gate and a default-deny-unless-you-rename-the-import gate. An
// alias (`import cr ".../capabilityread"`) is the ordinary evasion; a
// dot-import removes the qualifier entirely and is matched by its own pattern.
func grantChangeCallPattern(lines []string) *regexp.Regexp {
	for _, line := range lines {
		m := grantChangePostureImport.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		switch m[1] {
		case "":
			return regexp.MustCompile(`\bcapabilityread\.IsReadable\(`)
		case ".":
			return grantChangePostureBare
		default:
			return regexp.MustCompile(`\b` + regexp.QuoteMeta(m[1]) + `\.IsReadable\(`)
		}
	}
	return nil
}

// grantChangePosture looks for the annotation on line i, else on the contiguous
// run of comment lines directly above it. A blank line or any code line ends the
// run, so a declaration never reaches past the call it was written for.
func grantChangePosture(lines []string, i int) (shape, why string, declared bool) {
	if m := grantChangePostureShape.FindStringSubmatch(lines[i]); m != nil {
		return m[1], m[2], true
	}
	for j := i - 1; j >= 0; j-- {
		trimmed := strings.TrimSpace(lines[j])
		if trimmed == "" || !strings.HasPrefix(trimmed, "//") {
			return "", "", false
		}
		if m := grantChangePostureShape.FindStringSubmatch(lines[j]); m != nil {
			return m[1], m[2], true
		}
	}
	return "", "", false
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
		{"an undeclared derivation in internal/ is denied — internal/ is not exempt", "internal/objectmanager/gc.go",
			"\toid := substrate.SHA256NanoID(\"object:\" + digest)\n",
			"undeclared content-addressed id derivation in a submitter"},
		{"an undeclared derivation in internal/gateway/ is denied", "internal/gateway/whoami.go",
			"\tindexKey := \"vtx.identityindex.\" + substrate.SHA256NanoID(\"email:\" + e)\n",
			"undeclared content-addressed id derivation in a submitter"},
		// The annotation marker sits on the FIRST of three contiguous comment
		// lines, the shape every real annotation in internal/gateway has. That
		// is this case's unique kill: it is the only derived-key case that dies
		// if the walk up the comment block stops short of the block's top.
		{"a declared derivation in internal/gateway/ passes", "internal/gateway/whoami.go",
			"\t// derived-key: the identity index key, mirroring identity-domain's\n" +
				"\t// normalize_email, because the package's version is Starlark source\n" +
				"\t// text with no Go entry point for the gateway to call.\n" +
				"\tindexKey := \"vtx.identityindex.\" + substrate.SHA256NanoID(\"email:\" + e)\n", ""},
		{"a reasonless declaration in internal/gateway/ is denied", "internal/gateway/whoami.go",
			"\t// derived-key:\n" +
				"\tindexKey := \"vtx.identityindex.\" + substrate.SHA256NanoID(\"email:\" + e)\n",
			"declaration carries no reason"},
		{"internal/substrate owns the primitive and is exempt", "internal/substrate/derive.go",
			"\treturn NanoIDFromPCG(pcg, NanoIDLength)\n", ""},
		{"internal/substrate's tests are exempt with it", "internal/substrate/derive_test.go",
			"\tgot := SHA256NanoID(s)\n", ""},
		{"internal/processor exposes the primitive to Starlark and is exempt", "internal/processor/starlark_builtins.go",
			"\treturn starlarklib.String(substrate.SHA256NanoID(string(s))), nil\n", ""},
		{"a SUBPACKAGE of an owner is NOT exempt", "internal/processor/opwire/wire.go",
			"\tk := \"vtx.identityindex.\" + substrate.SHA256NanoID(\"email:\" + e)\n",
			"undeclared content-addressed id derivation in a submitter"},
		{"a subpackage of internal/substrate is NOT exempt either", "internal/substrate/keys/derive.go",
			"\tk := \"vtx.identityindex.\" + SHA256NanoID(\"email:\" + e)\n",
			"undeclared content-addressed id derivation in a submitter"},
		{"a subpackage of an owner may still declare its reason", "internal/processor/opwire/wire.go",
			"\t// derived-key: a requestId echo, not a key any script reads\n" +
				"\tk := substrate.SHA256NanoID(\"req:\" + id)\n", ""},
		{"a package whose name merely EXTENDS an owner's is not exempt", "internal/substrateX/derive.go",
			"\treturn NanoIDFromPCG(pcg, NanoIDLength)\n",
			"undeclared content-addressed id derivation in a submitter"},
		{"taking the derivation as a variable value is denied", "cmd/some-app/keys.go",
			"\tvar derivFn = substrate.SHA256NanoID\n",
			"taken as a function VALUE"},
		{"taking it as a struct-literal field is denied", "cmd/some-app/keys.go",
			"\tb := box{f: substrate.SHA256NanoID}\n",
			"taken as a function VALUE"},
		{"passing it as an argument is denied", "cmd/some-app/keys.go",
			"\tregister(\"idx\", substrate.SHA256NanoID)\n",
			"taken as a function VALUE"},
		{"returning it is denied", "cmd/some-app/keys.go",
			"\treturn substrate.NanoIDFromPCG\n",
			"taken as a function VALUE"},
		{"a value take may declare its reason like any other", "cmd/some-app/keys.go",
			"\t// derived-key: object id minter handed to the store, not a declared read\n" +
				"\tvar derivFn = substrate.SHA256NanoID\n", ""},
		{"a value take in an owner package is exempt with it", "internal/substrate/derive.go",
			"\tvar derivFn = SHA256NanoID\n", ""},
		{"the symbol NAME inside a string literal is not a value take", "cmd/some-app/keys_test.go",
			"\tt.Fatalf(\"golden vector drifted: substrate.SHA256NanoID derivation = %q\", got)\n", ""},
		{"the symbol NAME in a trailing comment is not a value take", "cmd/some-app/keys.go",
			"\tk := indexKey(e) // mirrors substrate.SHA256NanoID\n", ""},
		{"an identifier ENDING in the symbol is not a value take", "cmd/some-app/keys.go",
			"\tv := legacySHA256NanoID\n", ""},
		{"an identifier STARTING with the symbol is not a value take", "cmd/some-app/keys.go",
			"\tSHA256NanoIDCache = nil\n", ""},
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

		{"an undeclared MaxReconnects in cmd/** is denied", "cmd/refractor/main.go",
			"\tconn, err := substrate.Connect(ctx, substrate.ConnectOpts{\n" +
				"\t\tURL: *natsURL,\n" +
				"\t})\n",
			"max-reconnects: substrate.Connect in cmd/** does not set MaxReconnects"},
		{"a declared MaxReconnects: -1 (reconnect forever) passes", "cmd/refractor/main.go",
			"\tconn, err := substrate.Connect(ctx, substrate.ConnectOpts{\n" +
				"\t\tURL:           *natsURL,\n" +
				"\t\tMaxReconnects: -1,\n" +
				"\t})\n", ""},
		{"a declared small finite MaxReconnects passes too — the gate does not judge the value", "cmd/lattice-pkg/main.go",
			"\tconn, err := substrate.Connect(context.Background(), substrate.ConnectOpts{\n" +
				"\t\tURL:           natsURL,\n" +
				"\t\tMaxReconnects: 2,\n" +
				"\t})\n", ""},
		{"the gate is scoped to cmd/**", "internal/refractor/conn.go",
			"\tconn, err := substrate.Connect(ctx, substrate.ConnectOpts{\n" +
				"\t\tURL: url,\n" +
				"\t})\n", ""},
		{"the gate skips cmd/** test files", "cmd/refractor/main_test.go",
			"\tconn, err := substrate.Connect(ctx, substrate.ConnectOpts{\n" +
				"\t\tURL: url,\n" +
				"\t})\n", ""},
		{"a declaration on one call does not cover a different, undeclared call", "cmd/lattice-pkg/main.go",
			"\tconn1, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url})\n" +
				"\tconn2, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, MaxReconnects: 2})\n",
			"max-reconnects: substrate.Connect in cmd/** does not set MaxReconnects"},
		{"prose mentioning the call in a comment is not a use of it", "cmd/refractor/main.go",
			"\t// substrate.Connect(ctx, opts) needs MaxReconnects set\n", ""},
		{"KVListKeysPrefix feeding a per-key KVGet loop is denied", "internal/processor/self_test_fixture.go",
			"func f(ctx context.Context, conn *substrate.Conn) {\n" +
				"\tkeys, err := conn.KVListKeysPrefix(ctx, bucket, prefix)\n" +
				"\tif err != nil {\n\t\treturn\n\t}\n" +
				"\tfor _, k := range keys {\n" +
				"\t\tentry, _ := conn.KVGet(ctx, bucket, k)\n" +
				"\t\t_ = entry\n" +
				"\t}\n" +
				"}\n",
			"kv-batch: keys is listed via KVListKeysPrefix/KVListKeysFilter then read with a per-key KVGet"},
		{"KVListKeysFilter (3-return form) feeding a per-key KVGet loop is denied", "internal/processor/self_test_fixture.go",
			"func f(ctx context.Context, conn *substrate.Conn) {\n" +
				"\tkeys, cursor, err := conn.KVListKeysFilter(ctx, bucket, filter, \"\", 256)\n" +
				"\t_ = cursor\n" +
				"\tif err != nil {\n\t\treturn\n\t}\n" +
				"\tfor _, k := range keys {\n" +
				"\t\tentry, _ := conn.KVGet(ctx, bucket, k)\n" +
				"\t\t_ = entry\n" +
				"\t}\n" +
				"}\n",
			"kv-batch: keys is listed via KVListKeysPrefix/KVListKeysFilter then read with a per-key KVGet"},
		{"a batched KVGetMulti in the loop is not a finding", "internal/processor/self_test_fixture.go",
			"func f(ctx context.Context, conn *substrate.Conn) {\n" +
				"\tkeys, err := conn.KVListKeysPrefix(ctx, bucket, prefix)\n" +
				"\tif err != nil {\n\t\treturn\n\t}\n" +
				"\tentries, _ := conn.KVGetMulti(ctx, bucket, keys)\n" +
				"\t_ = entries\n" +
				"}\n", ""},
		{"a declared kv-batch: (single) reason is not a finding", "internal/processor/self_test_fixture.go",
			"func f(ctx context.Context, conn *substrate.Conn) {\n" +
				"\tkeys, err := conn.KVListKeysPrefix(ctx, bucket, prefix) // kv-batch: (single) at most one key by construction\n" +
				"\tif err != nil {\n\t\treturn\n\t}\n" +
				"\tfor _, k := range keys {\n" +
				"\t\tentry, _ := conn.KVGet(ctx, bucket, k)\n" +
				"\t\t_ = entry\n" +
				"\t}\n" +
				"}\n", ""},
		{"an unknown kv-batch shape is denied", "internal/processor/self_test_fixture.go",
			"func f(ctx context.Context, conn *substrate.Conn) {\n" +
				"\tkeys, err := conn.KVListKeysPrefix(ctx, bucket, prefix) // kv-batch: (parallel) not applicable\n" +
				"\tif err != nil {\n\t\treturn\n\t}\n" +
				"\tfor _, k := range keys {\n" +
				"\t\tentry, _ := conn.KVGet(ctx, bucket, k)\n" +
				"\t\t_ = entry\n" +
				"\t}\n" +
				"}\n",
			"kv-batch: unknown shape (parallel)"},
		{"kv-batch scope excludes test files", "internal/processor/self_test_fixture_test.go",
			"func f(ctx context.Context, conn *substrate.Conn) {\n" +
				"\tkeys, err := conn.KVListKeysPrefix(ctx, bucket, prefix)\n" +
				"\tif err != nil {\n\t\treturn\n\t}\n" +
				"\tfor _, k := range keys {\n" +
				"\t\tentry, _ := conn.KVGet(ctx, bucket, k)\n" +
				"\t\t_ = entry\n" +
				"\t}\n" +
				"}\n", ""},
		// The grant-change-posture gate (personal-lens-grant-change-trigger-
		// design.md §10.1). The census behind it is exactly one call site, so
		// these fixtures are the only thing that keeps the deny path honest —
		// a clean tree and a broken gate look identical.
		{"an undeclared IsReadable call site is denied", "internal/refractor/projection/personal.go",
			"import (\n\t\"github.com/operatinggraph/lattice/internal/refractor/capabilityread\"\n)\nfunc f() {\n\tok, err := capabilityread.IsReadable(ctx, capKV, at, aid, anchor)\n}\n",
			"undeclared capabilityread.IsReadable call site"},
		{"a subscribed declaration above the call passes", "internal/refractor/projection/personal.go",
			"import (\n\t\"github.com/operatinggraph/lattice/internal/refractor/capabilityread\"\n)\nfunc f() {\n\t// grant-change-posture: (subscribed) the cap-read producer's edge re-drives it\n" +
				"\tok, err := capabilityread.IsReadable(ctx, capKV, at, aid, anchor)\n}\n", ""},
		{"a swept declaration passes", "internal/refractor/projection/personal.go",
			"import (\n\t\"github.com/operatinggraph/lattice/internal/refractor/capabilityread\"\n)\nfunc f() {\n\t// grant-change-posture: (swept) the personal convergence sweep re-asks\n" +
				"\tok, err := capabilityread.IsReadable(ctx, capKV, at, aid, anchor)\n}\n", ""},
		{"a none-justified declaration with a reason passes", "internal/refractor/projection/personal.go",
			"import (\n\t\"github.com/operatinggraph/lattice/internal/refractor/capabilityread\"\n)\nfunc f() {\n\t// grant-change-posture: (none-justified) one-shot admin check, re-run per request\n" +
				"\tok, err := capabilityread.IsReadable(ctx, capKV, at, aid, anchor)\n}\n", ""},
		{"an on-the-line declaration passes", "internal/refractor/projection/personal.go",
			"import (\n\t\"github.com/operatinggraph/lattice/internal/refractor/capabilityread\"\n)\nfunc f() {\n\tok, err := capabilityread.IsReadable(ctx, capKV, at, aid, anchor) // grant-change-posture: (subscribed) producer edge\n}\n", ""},
		{"a declaration with no why is denied", "internal/refractor/projection/personal.go",
			"import (\n\t\"github.com/operatinggraph/lattice/internal/refractor/capabilityread\"\n)\nfunc f() {\n\t// grant-change-posture: (subscribed)\n" +
				"\tok, err := capabilityread.IsReadable(ctx, capKV, at, aid, anchor)\n}\n",
			"carries no `<why>`"},
		{"an unknown shape is denied", "internal/refractor/projection/personal.go",
			"import (\n\t\"github.com/operatinggraph/lattice/internal/refractor/capabilityread\"\n)\nfunc f() {\n\t// grant-change-posture: (exempt) staff are trusted\n" +
				"\tok, err := capabilityread.IsReadable(ctx, capKV, at, aid, anchor)\n}\n",
			"unknown shape (exempt)"},
		{"a declaration separated by a blank line covers nothing", "internal/refractor/projection/personal.go",
			"import (\n\t\"github.com/operatinggraph/lattice/internal/refractor/capabilityread\"\n)\nfunc f() {\n\t// grant-change-posture: (subscribed) producer edge\n\n" +
				"\tok, err := capabilityread.IsReadable(ctx, capKV, at, aid, anchor)\n}\n",
			"undeclared capabilityread.IsReadable call site"},
		{"a declaration does NOT reach the next sibling call", "internal/refractor/projection/personal.go",
			"import (\n\t\"github.com/operatinggraph/lattice/internal/refractor/capabilityread\"\n)\nfunc f() {\n\t// grant-change-posture: (subscribed) producer edge\n" +
				"\tok, err := capabilityread.IsReadable(ctx, capKV, at, aid, a1)\n" +
				"\tok2, err := capabilityread.IsReadable(ctx, capKV, at, aid, a2)\n}\n",
			"undeclared capabilityread.IsReadable call site"},
		{"prose naming the function is not a call site", "internal/refractor/projection/personal.go",
			"import (\n\t\"github.com/operatinggraph/lattice/internal/refractor/capabilityread\"\n)\n// personalEnvelopeFn gates every row on capabilityread.IsReadable, the D1 boundary.\nfunc f() {}\n", ""},
		{"the gate reaches a NEW consumer outside the refractor packages", "cmd/loupe/handlers.go",
			"import (\n\t\"github.com/operatinggraph/lattice/internal/refractor/capabilityread\"\n)\nfunc f() {\n\tok, err := capabilityread.IsReadable(ctx, capKV, at, aid, anchor)\n}\n",
			"undeclared capabilityread.IsReadable call site"},
		{"a SINGLE-LINE unaliased import is still gated", "internal/refractor/projection/personal.go",
			"import \"github.com/operatinggraph/lattice/internal/refractor/capabilityread\"\n" +
				"func f() {\n\tok, err := capabilityread.IsReadable(ctx, capKV, at, aid, anchor)\n}\n",
			"undeclared capabilityread.IsReadable call site"},
		{"a single-line unaliased import carrying a declaration passes", "internal/refractor/projection/personal.go",
			"import \"github.com/operatinggraph/lattice/internal/refractor/capabilityread\"\n" +
				"func f() {\n\t// grant-change-posture: (swept) the personal convergence sweep re-asks\n" +
				"\tok, err := capabilityread.IsReadable(ctx, capKV, at, aid, anchor)\n}\n", ""},
		{"a SINGLE-LINE aliased import does not skip the whole file", "internal/refractor/projection/personal.go",
			"import cr \"github.com/operatinggraph/lattice/internal/refractor/capabilityread\"\n" +
				"func f() {\n\tok, err := cr.IsReadable(ctx, capKV, at, aid, anchor)\n}\n",
			"undeclared capabilityread.IsReadable call site"},
		{"a single-line DOT import does not evade the gate", "internal/refractor/projection/personal.go",
			"import . \"github.com/operatinggraph/lattice/internal/refractor/capabilityread\"\n" +
				"func f() {\n\tok, err := IsReadable(ctx, capKV, at, aid, anchor)\n}\n",
			"undeclared capabilityread.IsReadable call site"},
		{"an ALIASED import does not evade the gate", "internal/refractor/projection/personal.go",
			"import (\n\tcr \"github.com/operatinggraph/lattice/internal/refractor/capabilityread\"\n)\n" +
				"func f() {\n\tok, err := cr.IsReadable(ctx, capKV, at, aid, anchor)\n}\n",
			"undeclared capabilityread.IsReadable call site"},
		{"an aliased call carrying a declaration passes", "internal/refractor/projection/personal.go",
			"import (\n\tcr \"github.com/operatinggraph/lattice/internal/refractor/capabilityread\"\n)\n" +
				"func f() {\n\t// grant-change-posture: (subscribed) the cap-read producer edge\n" +
				"\tok, err := cr.IsReadable(ctx, capKV, at, aid, anchor)\n}\n", ""},
		{"a DOT import does not evade the gate", "internal/refractor/projection/personal.go",
			"import (\n\t. \"github.com/operatinggraph/lattice/internal/refractor/capabilityread\"\n)\n" +
				"func f() {\n\tok, err := IsReadable(ctx, capKV, at, aid, anchor)\n}\n",
			"undeclared capabilityread.IsReadable call site"},
		{"the unaliased qualifier does not fire in a file that never imports the package", "internal/refractor/projection/personal.go",
			"func f() {\n\tok, err := capabilityread.IsReadable(ctx, capKV, at, aid, anchor)\n}\n", ""},
		{"a same-named method on an unrelated receiver is not a call site", "internal/refractor/projection/personal.go",
			"import (\n\t\"github.com/operatinggraph/lattice/internal/refractor/capabilityread\"\n)\nfunc f() {\n\tok := gate.IsReadable(anchor)\n}\n", ""},
		{"a test file is out of scope", "internal/refractor/capabilityread/capabilityread_test.go",
			"import (\n\t\"github.com/operatinggraph/lattice/internal/refractor/capabilityread\"\n)\nfunc TestX(t *testing.T) {\n\tok, err := capabilityread.IsReadable(ctx, kv, at, aid, anchor)\n}\n", ""},
		// primordial-actor. The fixture directory does not exist on disk, so
		// packageOpScopeIsAny answers "any" — the fail-closed default these
		// cases are written against. `emit` is the external-egress line the
		// gate triggers on; `guard` is the only shape that discharges it.
		{"an unguarded external emission is denied", fixture,
			"    if ot == \"CreateThing\":\n" +
				"        events = [{\"class\": \"external.\" + adapter, \"data\": d}]\n",
			"primordial-actor: CreateThing"},
		{"a guarded branch passes", fixture,
			"    if ot == \"CreateThing\":\n" +
				"        # actor-guard: (primordial) only Loom dispatches this pattern\n" +
				"        if op.actor != primordialActor[\"loom\"]:\n" +
				"            fail(\"AuthDenied: nope\")\n" +
				"        events = [{\"class\": \"external.\" + adapter, \"data\": d}]\n", ""},
		{"a literal adapter name is caught too", fixture,
			"    if ot == \"CreateThing\":\n" +
				"        events.append({\"class\": \"external.notification\", \"data\": d})\n",
			"primordial-actor: CreateThing"},
		{"a guard in a SIBLING branch does not cover this one", fixture,
			"    if ot == \"GuardedOp\":\n" +
				"        if op.actor != primordialActor[\"loom\"]:\n" +
				"            fail(\"AuthDenied: nope\")\n" +
				"    if ot == \"UnguardedOp\":\n" +
				"        events = [{\"class\": \"external.\" + adapter, \"data\": d}]\n",
			"primordial-actor: UnguardedOp"},
		// The four bypass shapes a pure string-match gate admits.
		{"bypass 1 — a guard placed AFTER the emission is denied", fixture,
			"    if ot == \"CreateThing\":\n" +
				"        events = [{\"class\": \"external.\" + adapter, \"data\": d}]\n" +
				"        if op.actor != primordialActor[\"loom\"]:\n" +
				"            fail(\"AuthDenied: too late\")\n",
			"primordial-actor: CreateThing"},
		{"bypass 1b — a guard after the subject READ is denied, even before the emit", fixture,
			"    if ot == \"CreateThing\":\n" +
				"        doc = kv.Read(subject_key)\n" +
				"        if op.actor != primordialActor[\"loom\"]:\n" +
				"            fail(\"AuthDenied: read already happened\")\n" +
				"        events = [{\"class\": \"external.\" + adapter, \"data\": d}]\n",
			"primordial-actor: CreateThing"},
		{"bypass 2 — a comment mentioning the global is denied", fixture,
			"    if ot == \"CreateThing\":\n" +
				"        # guarded by primordialActor[\"loom\"] somewhere, honest\n" +
				"        events = [{\"class\": \"external.\" + adapter, \"data\": d}]\n",
			"primordial-actor: CreateThing"},
		{"bypass 3 — a dead guard branch is denied", fixture,
			"    if ot == \"CreateThing\":\n" +
				"        if False and op.actor != primordialActor[\"loom\"]:\n" +
				"            fail(\"AuthDenied: unreachable\")\n" +
				"        events = [{\"class\": \"external.\" + adapter, \"data\": d}]\n",
			"primordial-actor: CreateThing"},
		{"bypass 3b — a guard whose body does not refuse is denied", fixture,
			"    if ot == \"CreateThing\":\n" +
				"        if op.actor != primordialActor[\"loom\"]:\n" +
				"            pass\n" +
				"        events = [{\"class\": \"external.\" + adapter, \"data\": d}]\n",
			"primordial-actor: CreateThing"},
		{"bypass 4 — shadowing the global is denied", fixture,
			"    primordialActor = {\"loom\": op.actor}\n" +
				"    if ot == \"CreateThing\":\n" +
				"        if op.actor != primordialActor[\"loom\"]:\n" +
				"            fail(\"AuthDenied: self-comparison\")\n" +
				"        events = [{\"class\": \"external.\" + adapter, \"data\": d}]\n",
			"`primordialActor` is ASSIGNED here"},
		{"a subscript rebinding is shadowing too", fixture,
			"    primordialActor[\"loom\"] = op.actor\n",
			"`primordialActor` is ASSIGNED here"},
		{"a comparison is not an assignment", fixture,
			"    if ot == \"CreateThing\":\n" +
				"        if op.actor != primordialActor[\"loom\"]:\n" +
				"            fail(\"AuthDenied: nope\")\n" +
				"        ok = op.actor == primordialActor[\"loom\"]\n" +
				"        events = [{\"class\": \"external.\" + adapter, \"data\": d}]\n", ""},
		// Helper attribution + the escape hatch.
		{"an emission in a helper AFTER the branch is denied", fixture,
			"    if ot == \"CreateThing\":\n" +
				"        if op.actor != primordialActor[\"loom\"]:\n" +
				"            fail(\"AuthDenied: nope\")\n" +
				"        return dispatch(op)\n" +
				"def dispatch(op):\n" +
				"    events = [{\"class\": \"external.\" + adapter, \"data\": d}]\n",
			"does not sit directly inside its own"},
		{"an emission before any dispatch branch is denied", fixture,
			"def dispatch(op):\n" +
				"    events = [{\"class\": \"external.\" + adapter, \"data\": d}]\n",
			"does not sit directly inside its own"},
		{"a (caller-guarded) declaration discharges a shared helper", fixture,
			"def dispatch(op):\n" +
				"    # actor-guard: (caller-guarded) every ot branch pins op.actor before calling this\n" +
				"    events = [{\"class\": \"external.\" + adapter, \"data\": d}]\n", ""},
		{"a (caller-guarded) declaration with no why is denied", fixture,
			"def dispatch(op):\n" +
				"    # actor-guard: (caller-guarded)\n" +
				"    events = [{\"class\": \"external.\" + adapter, \"data\": d}]\n",
			"must state its `<why>`"},
		{"an unknown actor-guard shape is denied", fixture,
			"    # actor-guard: (trusted) the caller is fine\n" +
				"    if ot == \"CreateThing\":\n" +
				"        pass\n",
			"unknown shape (trusted)"},
		{"prose naming an external event is not an emission", fixture,
			"    // emits external.notification off its own outbox to the bridge\n" +
				"    Desc: \"external.notification off the outbox\",\n", ""},
		{"the gate is scoped to packages/", "internal/loom/engine.go",
			"    if ot == \"CreateThing\":\n" +
				"        events = [{\"class\": \"external.\" + adapter, \"data\": d}]\n", ""},
		{"the gate skips package test files", "packages/self-test/ddls_test.go",
			"    if ot == \"CreateThing\":\n" +
				"        events = [{\"class\": \"external.\" + adapter, \"data\": d}]\n", ""},
		{"a materialized capability Definition passed to Apply is denied", "cmd/loupe/review.go",
			"\tres, err := inst.Apply(ctx, plan.MaterializedDefinition(), pkgmgr.ApplyOptions{})\n",
			"capability plan's Definition passed to Installer.Apply/Upgrade"},
		{"the same value passed to the ungated Upgrade verb is denied", "cmd/loupe/review.go",
			"\tres, err := inst.Upgrade(ctx, plan.MaterializedDefinition())\n",
			"capability plan's Definition passed to Installer.Apply/Upgrade"},
		{"a re-exported plan.Definition field passed to Apply is denied", "cmd/lattice-pkg/main.go",
			"\tres, err := inst.Apply(ctx, plan.Definition, pkgmgr.ApplyOptions{})\n",
			"capability plan's Definition passed to Installer.Apply/Upgrade"},
		{"a re-exported plan.Definition field passed to Upgrade is denied", "cmd/lattice-pkg/main.go",
			"\tres, err := inst.Upgrade(ctx, plan.Definition)\n",
			"capability plan's Definition passed to Installer.Apply/Upgrade"},
		{"an ordinary source-authored Apply passes", "cmd/loupe/pkg.go",
			"\tres, err := inst.Apply(ctx, def, opts)\n", ""},
		{"inspecting the materialized Definition away from Apply passes", "cmd/bridge/capability_author_test.go",
			"\tmaterialized := plan.MaterializedDefinition()\n" +
				"\tif len(materialized.WeaverTargets) != 1 {\n\t\tt.Fatal(\"x\")\n\t}\n", ""},
		{"applying the plan through the sanctioned entry point passes", "cmd/lattice-pkg/main.go",
			"\tres, err := inst.ApplyCapabilityPlan(ctx, plan)\n", ""},
		{"the gate does not bind internal/pkgmgr, which owns both sides of the seam", "internal/pkgmgr/capabilityapply.go",
			"\tres, err := i.Apply(ctx, plan.MaterializedDefinition(), opts)\n", ""},
		{"a refusal with no wrapped sentinel is denied", "internal/pkgmgr/upgrade.go",
			"\treturn fmt.Errorf(\"pkgmgr: upgrade refused — it drops column %q\", col)\n",
			"a refusal built with fmt.Errorf and no wrapped sentinel"},
		{"a declared-transient refusal passes", "internal/pkgmgr/upgrade.go",
			"\t// refusal-sentinel: (transient) a torn multi-key read clears on retry\n" +
				"\treturn fmt.Errorf(\"pkgmgr: refusing a partial view of %q\", mk)\n", ""},
		{"a bare transient label with no reason is still denied", "internal/pkgmgr/upgrade.go",
			"\t// refusal-sentinel: (transient)\n" +
				"\treturn fmt.Errorf(\"pkgmgr: refusing a partial view of %q\", mk)\n",
			"a refusal built with fmt.Errorf and no wrapped sentinel"},
		{"a declaration does not reach a later refusal past its statement", "internal/pkgmgr/upgrade.go",
			"\t// refusal-sentinel: (transient) a torn multi-key read clears on retry\n" +
				"\treturn fmt.Errorf(\"pkgmgr: refusing a partial view of %q\", mk)\n" +
				"\tx := 1\n" +
				"\treturn fmt.Errorf(\"pkgmgr: upgrade refused — it drops column %q\", col)\n",
			"a refusal built with fmt.Errorf and no wrapped sentinel"},
		{"a refusal wrapping a sentinel passes", "internal/pkgmgr/upgrade.go",
			"\treturn fmt.Errorf(\"%w: pkgmgr: upgrade refused — it drops column %q\", ErrDropRefused, col)\n", ""},
		{"a non-refusal fmt.Errorf passes", "internal/pkgmgr/upgrade.go",
			"\treturn fmt.Errorf(\"pkgmgr: read declared keys: %v\", err)\n", ""},
		{"a refusal assertion in a pkgmgr test passes", "internal/pkgmgr/upgrade_test.go",
			"\twant := fmt.Errorf(\"pkgmgr: upgrade refused — it drops column %q\", col)\n", ""},
		{"the refusal-sentinel rule does not bind outside internal/pkgmgr", "internal/refractor/pipeline.go",
			"\treturn fmt.Errorf(\"refractor: projection refused — no lens\")\n", ""},
		{"kv-batch scope excludes the wider corpus outside internal/processor and internal/substrate", "cmd/loupe/handlers.go",
			"func f(ctx context.Context, conn *substrate.Conn) {\n" +
				"\tkeys, err := conn.KVListKeysPrefix(ctx, bucket, prefix)\n" +
				"\tif err != nil {\n\t\treturn\n\t}\n" +
				"\tfor _, k := range keys {\n" +
				"\t\tentry, _ := conn.KVGet(ctx, bucket, k)\n" +
				"\t\t_ = entry\n" +
				"\t}\n" +
				"}\n", ""},
		// The change-narration grammar that anchors on the authoring change.
		// Each rejected candidate carries the legitimate shape it was rejected
		// FOR, so a later widening cannot quietly re-admit it.
		{"narration anchored on this fire is denied", "internal/weaver/evaluator.go",
			"// The gap this fire closes: a retired column left its row error standing.\n", "history/changelog comment"},
		{"narration anchored on this fix is denied", "internal/weaver/evaluator.go",
			"\t// before this fix the handler trusted the caller's key.\n", "history/changelog comment"},
		{"narration anchored on this change is denied", "internal/weaver/evaluator.go",
			"\t// the OTHER surface this change adds is the admission block.\n", "history/changelog comment"},
		{"sentence-initial capitalisation does not evade the gate", "internal/weaver/evaluator.go",
			"// This fire ships the per-entity key.\n", "history/changelog comment"},
		{"a plural verb is not the noun phrase", "internal/edge/sync/sync.go",
			"\t// — so this fires when the SYNC stream retains nothing at all.\n", ""},
		{"this run describes an invocation, not a change", "cmd/lattice-pkg/main.go",
			"\t// invisible before this run touched anything — pre-existing residue.\n", ""},
		{"this pass describes a traversal, not a change", "internal/bootstrap/reconcile.go",
			"\t// a candidate this pass must leave entirely alone.\n", ""},
		{"this commit path is Processor vocabulary, not narration", "internal/processor/commit_path.go",
			"\t// a collision this commit path does not already treat as benign.\n", ""},
		{"narration anchored on this increment is denied", "internal/weaver/evaluator.go",
			"\t// through unevaluated: this increment builds no evaluator.\n", "history/changelog comment"},
		{"pre-fix narration is denied", "internal/weaver/evaluator.go",
			"\t// Sanity: the pre-fix mapping must get this WRONG.\n", "history/changelog comment"},
		{"a lint script quoting the banned shape is exempt", "scripts/lint-board.go",
			"\t// fails a cell carrying `Was:` or narrating this fire.\n", ""},
		// The line-anchored pass cannot see a phrase a wrapped doc comment
		// splits across the margin; the block pass is what closes that.
		{"a phrase split across two comment lines is denied", "internal/weaver/registry.go",
			"// \"\" (absent, the default — every target installed before this\n" +
				"// fire) is frozen table-only behavior, byte-identical.\n",
			"history/changelog comment"},
		{"a phrase split across a comment line break is reported on its own line", "internal/weaver/registry.go",
			"// The planner-extension posture is frozen table-only for a target\n" +
				"// that declares no Mode, byte-identical to every target before this\n" +
				"// fire, and always valid.\n",
			"history/changelog comment"},
		{"two unrelated trailing comments are not joined into a phrase", "internal/weaver/registry.go",
			"\tx := 1 // nothing to see in this\n" +
				"\ty := 2 // fire the retry and move on\n", ""},
		{"a clean wrapped comment block passes", "internal/weaver/registry.go",
			"// The planner-extension posture is frozen table-only for a target\n" +
				"// that declares no Mode, and is always valid.\n", ""},
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
				!strings.HasPrefix(fd.msg, "derived-key:") &&
				!strings.HasPrefix(fd.msg, "grant-change-posture:") &&
				!strings.HasPrefix(fd.msg, "max-reconnects:") &&
				!strings.HasPrefix(fd.msg, "primordial-actor:") &&
				!strings.HasPrefix(fd.msg, "actor-guard:") &&
				!strings.HasPrefix(fd.msg, "capability-apply:") &&
				!strings.HasPrefix(fd.msg, "refusal-sentinel:") &&
				!strings.HasPrefix(fd.msg, "kv-batch:") &&
				!strings.HasPrefix(fd.msg, "history/changelog") {
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
	failures = append(failures, historyLineSelfTest()...)
	return failures
}

// historyLineSelfTest pins WHICH line a block-joined history finding lands on.
// The table above asserts only on a finding's message, so the offset-to-line
// arithmetic in historyBlockHits — the part that is easy to get wrong by one
// segment — would survive a regression there unnoticed. A phrase split across
// the margin must be reported on the line it STARTS on, not the line that
// completes it, or the author is sent to the wrong place.
func historyLineSelfTest() []string {
	var failures []string
	cases := []struct {
		name string
		src  string
		want int
	}{
		{"a split phrase reports on the line it starts on",
			"package p\n\n// the default — every target installed before this\n// fire) is frozen table-only behavior.\nvar x = 1\n", 3},
		{"a phrase spanning three lines reports on the first",
			"package p\n\n// something something Before\n// the\n// fix the ordering held.\nvar x = 1\n", 3},
		{"a match late in a block reports on its own line, not the block's first",
			"package p\n\n// line one, clean.\n// line two, clean.\n// line three mentions this fire.\nvar x = 1\n", 5},
		{"an em-dash before the match does not shift the line",
			"package p\n\n// a — b — c — every target installed before this\n// fire) is frozen.\nvar x = 1\n", 3},
		{"narration inside a block comment is caught",
			"package p\n\n/*\nthe default — every target installed before this\nfire) is frozen table-only behavior.\n*/\nvar x = 1\n", 4},
	}
	for _, tc := range cases {
		var lines []int
		for _, fd := range scanSource("internal/weaver/registry.go", []byte(tc.src)) {
			if strings.HasPrefix(fd.msg, "history/changelog") {
				lines = append(lines, fd.line)
			}
		}
		switch {
		case len(lines) == 0:
			failures = append(failures, tc.name+": expected a history finding, got none")
		case len(lines) > 1:
			failures = append(failures, fmt.Sprintf("%s: expected one finding, got %d at lines %v", tc.name, len(lines), lines))
		case lines[0] != tc.want:
			failures = append(failures, fmt.Sprintf("%s: finding on line %d, want %d", tc.name, lines[0], tc.want))
		}
	}
	// A legitimate sentence must not be refused by a blocking gate.
	for _, clean := range []string{
		"package p\n\n// Rebuild takes this fire-and-forget path to the outbox.\nvar x = 1\n",
		"package p\n\n// a paragraph ending in the word this\n//\n// fire the retry only after the backoff.\nvar x = 1\n",
		"package p\n\n/* a plain block comment describing what the code does now. */\nvar x = 1\n",
	} {
		for _, fd := range scanSource("internal/weaver/registry.go", []byte(clean)) {
			if strings.HasPrefix(fd.msg, "history/changelog") {
				failures = append(failures, fmt.Sprintf("false positive on a legitimate comment (line %d): %q", fd.line, clean))
			}
		}
	}
	return failures
}

func trackedGoFiles(strict bool) []string {
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
	reportUntrackedGoFiles(strict)
	return files
}

// reportUntrackedGoFiles names the .go files this run did NOT scan because git
// does not track them yet.
//
// The scan set comes from `git ls-files`, so a file that has not been `git
// add`ed is invisible to every check here — and CI, which lints a committed
// tree, sees it. A local run therefore reports "0 issues" on exactly the new
// file the author most wants checked, and the first real verdict arrives as a
// red build on main. Saying which files were skipped costs one line and turns
// that silence into something the author can act on before pushing.
//
// Untracked .go files are a normal mid-edit state, so an ordinary local run
// only reports them: failing there would make the gate unusable during the work
// it is meant to support. Under STRICT — the verdict a build gate reads — the
// same banner exits 1 instead. A gate that could not see the files under review
// has no verdict to give, and a green "0 issues" over a scan set missing exactly
// the new file is worse than no verdict at all: it reads as a pass in a
// green-bar list and the first real answer arrives as a red build on main.
func reportUntrackedGoFiles(strict bool) {
	out, err := exec.Command("git", "ls-files", "--others", "--exclude-standard", "*.go").Output()
	if err != nil {
		return
	}
	var skipped []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			skipped = append(skipped, l)
		}
	}
	if len(skipped) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "lint-conventions: NOT SCANNED — %d untracked .go file(s); `git add` them for this gate to see what CI will:\n", len(skipped))
	for _, f := range skipped {
		fmt.Fprintf(os.Stderr, "  %s\n", f)
	}
	if strict {
		os.Exit(1)
	}
}
