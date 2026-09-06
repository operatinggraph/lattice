# Weaver targets — `missing_* ⊆ gaps` gets a holder on every path a target reaches a running Weaver

**State: `📐 awaiting-Andrew (ratification)` — 2026-09-06, after the §13 adversarial pass (3 BLOCKING + 8 MAJOR folded).**
**Board row:** `[Pkgmgr] An AI-authored weaverTarget artifact has no static holder of missing_* ⊆ gaps` (lattice.md,
Component maintenance). **Filed:** the close of the row-8 build (`e96f9126`, 2026-08-29). **Designer:** Winston,
2026-09-06, grounded at `ed55c166`. **Size:** M · **Imp:** ★★ · **Owner after ratification:** the Lattice Steward,
one fire, three increments (§11).

## 0. For Andrew — one look

**What it does.** Contract #10 §10.8 says a row column `missing_*: true` with no `gaps` entry is a config error the
Weaver alerts on, and the row-8 design that made that alert a long-floor Nak was ratified on the premise that a gate
asserts *lens-projected `missing_*` ⊆ declared gaps* — so the Nak is only ever held for a genuine authoring omission.
That gate exists today on **one** of the four paths a target reaches a running Weaver: the CI lint over compiled
packages. It reaches nothing else: a **capability artifact** (Loupe's weaver-author page, a human path live today; the
bridge's AI author, dormant) validates without ever reading its lens's columns; the **installer** checks the companion
pair but never the subset, and passes any NanoID-shaped `lensRef` through without checking that anything is installed
under it; and the **Weaver** loads the target and the lens through the same CDC feed and never compares them. This
design puts one derivation of "the keys this lens's rows carry" in a leaf package and gives the invariant three holders
that share it: **authoring preflight** (the artifact validator, with an injected installed-lens resolver on the
`SensitiveAspectResolver` pattern), **the apply-time bound** (the installer refuses a target whose `lensRef` binds
nothing, or whose lens projects an undeclared column), and **the closed-set verdict** (the Weaver's target source
indexes `meta.lens` specs it already receives and raises a level-triggered `warning` per undeclared column at load —
before any row arrives, for every path, including a lens edited after its target armed). The row-level long-floor
behaviour §10.8 promises is unchanged.

**Fork check — none.** No Gateway / read-path auth / Vault / multi-cell / HA-NATS surface; no new Core-KV state, no
new key family, no new op. One new Health issue code family on the Weaver (`warning`, never `error`).

**Contract check — one install-time refusal clause, §10.8.** The runtime gains an admission rule a package author can
observe (an install refused for a `lensRef` that binds nothing, or for an undeclared projected column). Per the
2026-09-01 exception it **lands with the build's commit** (Inc 2) and is held out of the tree until then; the text of
record is §6. Nothing is staged. Everything else builds to §10.8's existing sentences.

**The one product-shaped decision, resolved here, for your veto:** at the Weaver the undeclared column is a **standing
`warning`, not a refusal of the target** — §10.8 promises "config error → alert, never silently skipped", and refusing
the whole target would silently stop its *other* gaps' remediations (an availability regression on a live target the
moment a lens edit hot-reloads a new column). A `warning` does make that Weaver report `degraded` for as long as the
fault stands — the same status the existing row-level raise already produces once a row arrives (§4.4). Enforcement
sits where the rule is decided — authoring and install — the house rule for lifecycle hygiene (§7 row 4).

## 1. Problem, grounded

**The promise.** `docs/contracts/10-orchestration-weaver.md:152-156`: every `gaps` key must be a `missing_<gap>`
column the Lens produces; "a row column `missing_*: true` with no `gaps[col]` entry is a config error → alert, never
silently skipped". `internal/weaver/evaluator.go:311-352` keeps it: no playbook entry and no `unplannable` escalation
⇒ `alertPaced(issueKeyGapConfig(targetID, col), "warning", "GapWithoutPlaybook", …)` and `NakWithLongDelay`. The row
holds one of the lane's `laneMaxAckPending = 1024` slots (`engine.go:49`) and re-runs `clearClosedMarks` every long
floor (5 min) for as long as the column stays true; the fix is a re-author, which projects no new row.

**The premise the Nak rests on.** `weaver-decline-retry-substrate-native-design.md` §3.2 row 8, verbatim: *"Weaver
cannot distinguish a deliberate orphan column from a genuinely missing entry, so the fix is to make the deliberate case
declarable — the sanctioned form already exists (a `surface` gap …) — plus a gate asserting lens-projected `missing_*`
⊆ declared gaps. Only then is row 8's Long sound."* The gate shipped as `scripts/lint-gap-column-declaration.go`
(`69492978`, sharpened `5fee080f`).

**Where the gate does not reach — the lint's own scope statement (`lint-gap-column-declaration.go:96-110`)**, each
sentence checked in code:

| Path a `meta.weaverTarget` reaches a running Weaver | Holder of `missing_* ⊆ gaps` today | Evidence |
|---|---|---|
| A compiled package through CI | **the lint** (STRICT, `ci.yml:346`) | run 2026-09-06: `clean — 30 target(s) across 14 package(s); 31 weaver-targets lens(es) read, 50 gap column(s) checked …, 0 exempt` |
| A compiled package installed locally (`make reinstall-package`, `lattice-pkg apply`) with no CI run | **none** | the installer's gap rules are the key convention (`orchestrationguard.go:185`) and the `inflight_/maxretries_` companion pair (`:230`, `:454`); no subset rule exists in `internal/pkgmgr` (census 7) |
| A **capability artifact** — Loupe weaver-author (`cmd/loupe/weaverauthor.go:179`, human, live); the review console's fresh verdict (`cmd/loupe/review.go:597`, refuses approval on invalid at `:650`); the CLI (`cmd/lattice/capability/capability.go:317`) | **none** | `validateWeaverTargetArtifact` (`capabilitymaterializer.go:601-627`): "LensRef resolution … is a build-time concern, not checked here"; the artifact Definition carries no lens, so `validateAll` sees nothing to compare. At apply, `resolveLensRef` (`build.go:587-598`) passes any NanoID-shaped ref through **verbatim, unchecked for existence**; `validateGapCompanionPair` skips a ref that "names no lens in this batch" (`:439-446`) |
| The bridge's AI author (`cmd/bridge/main.go:288`; `internal/bridge/capability_author.go:32` authors `weaverTarget` only; dormant behind `BRIDGE_CAPABILITY_AUTHOR=real`) | **existence yes, subset no** | the adapter resolves the model's canonicalName against the live installed catalog and refuses an unknown name (`capability_author.go:1250-1262`; the model may only name a lens by canonicalName, `capability_author_prompt.go:374-379`), so the `lensRef` it records exists; it never reads that lens's columns, and the verdict call passes `nil, nil` |
| A package target bound **cross-package by NanoID** | **none** (census 2: 0 such refs today) | same two installer skips; the lint reports it as unverifiable (`checkTarget`'s `!found` arm, `:278-279`) |
| A lens **edited after its target armed** — an Output edit now hot-reloads in place (`lens-output-reactivation-design.md`, shipped `3017eac3`); an `upgradeExisting` lens proposal rewrites a spec (`capabilityapply.go:142-147` refuses removals, not updates) | the lint only if the edit went through CI in the same package | the Weaver never compares: `registry.go:613-620` routes `meta.weaverTarget` and `meta.loomPattern` specs and drops every other class, though `start` subscribes `vtx.meta.` with history (`:484-499`) and `handle` records every vertex's class (`:546-570`). `internal/weaver/reconciler.go:1109-1117` already names "a target whose LensRef resolves outside the installing batch, whose columns the gate never saw" as a shape the (companion-pair) install gate structurally cannot reach |

**A second constructible hole the same resolution closes.** `substrate.IsValidNanoID` is length-20 + alphabet
(`internal/substrate/keys/nanoid.go:90-92` → `isValidNanoIDN` `:100-110`; the alphabet at `:13` drops only `I l O 0`).
Three lens canonical names in the corpus pass it — **`appointmentReminders`, `augurDispatchPending`,
`capUpgradeRosterLens`** (census 3, run through the real function). Inside their own package `resolveLensRef`'s
canonicalName map wins first, so both live uses (`packages/clinic-reminders/targets.go:32`,
`packages/augur/targets.go:19`) are correct today. Referenced from **any other package or a Loupe draft** by
canonicalName, each passes through as a "NanoID" and installs a target bound to a meta id that does not exist — and
Loupe's propose path skips its own canonicalName lookup for exactly that shape (`weaverTargetNeedsLensResolution`,
`weaverauthor.go:333-352`). A target bound to nothing loads fine (`validateTarget` never touches `LensRef`) and
dispatches over an empty prefix forever. The install-time existence check (§4.3) is the closure. (The bridge is not
exposed: its catalog lookup is by name, and it records the id the catalog holds.)

**What the artifact path projects, precisely.** A `lens` artifact carries `canonicalName/adapter/bucket/table/spec`
only (`capabilitymaterializer.go:118-124`) and materializes with `Engine: "full"` and **no Output descriptor**
(`:911-925`) — a **plain** lens. On the plain path every RETURN item lands in the row map under its alias or
`projectionAutoAlias` name (`ruleengine/full/executor.go:1800-1804`, `:2030-2038`: a bare variable → its name, a
property access → its key, anything else → `_col<i>`; the key column is copied into the body, not moved), and the
NATS-KV adapter writes `json.Marshal(row)` (`adapter/natskv.go:379`), stamping `projectionSeq` on the guarded path.
So for an artifact target the `missing_*` population is the parsed RETURN item names — which is why the lint, which
refuses to read cypher, reports every plain weaver-targets lens as UNREADABLE-BY-THIS-GATE (zero in the corpus today;
every artifact lens would be one).

## 2. The invariant, its consumer, and the governed set

Derived from the consumer that decides the outcome (the Strategist's gap scan: keys with the `missing_` prefix whose
value is true, §10.2), the governed set for a target is **every `missing_*` key that can appear in a row under its
prefix**. The derivation is a **whitelist over lens shape** — the weaver dossier's "classify by whitelist, not
blacklist, when the vocabulary can grow": a shape not on the list is *unreadable*, never *empty*.

| Lens shape — the conjunct the runtime itself selects on | Keys a row body carries (beyond envelope metadata, none of which is `missing_`-prefixed) | Written by |
|---|---|---|
| `projectionKind == actorAggregate` **and** `Output != nil` **and** `EntryKeyColumn == ""` | `Output.BodyColumns ∪ Output.StaticEmptyColumns` | `projection/driver.go:70-72` then `:125-130`; the envelope adds `key`, the actor field, `version`, `projectedAt`, `projectedFromRevisions`, `lanes` (`:114-130`). The union is what the installer's `declaredRowBodyColumns` (`orchestrationguard.go:479-488`) and the lint's `gapColumnsOf` compute today |
| `projectionKind == actorAggregate`, `Output != nil`, **`EntryKeyColumn` set** (a per-entry list lens; 0 in the corpus, author-declarable) | the fields of each collected entry map, minus the key column — a **runtime** shape (`driver.go:222-240`) | **unreadable** by declaration; refused/reported as such |
| `projectionKind == actorAggregate` with `Output == nil` | the actor-aggregate install path is chosen on `projectionKind` alone (`cmd/refractor/main.go:1707`) and `Compile` needs a descriptor — the lens does not activate | **unreadable** (the lint's own `Output == nil` arm today, `classify:365-367`) |
| `Source.Kind == "eventStream"` (Chronicler-fed; 1 in the corpus, `loomFlowHistory` → bucket `orchestration-history`) | the keys of `Source.Project.Columns` (`definition.go:1485-1487`); no cypher | statically derivable; included so a target that binds one is judged on its real columns |
| plain (everything else: no `projectionKind`, or a kind not above) — every artifact lens | the RETURN items' names — alias, else `projectionAutoAlias` — from `cypherRule`, or from **branch 0** of `cypherBranches` when present (the two are mutually exclusive, `lens/corekv_source.go:1389-1391`; branch alias lists must be identical or registration refuses, `full.ClassifyBranchReturnColumns`, `:1593`) | `executor.go:1836/:1917/:2009` via `itemAlias`; `natskv.go:379` |

The installed form carries everything needed: `lensSpecBody` (`build.go:475-580`) writes `cypherRule`, `engine`,
`cypherBranches`, `projectionKind`, `output` and `source` into `vtx.meta.<lensId>.spec`, with `canonicalName` (Loupe's
`buildLensCanonicalIndex` reads it, `weaver.go:1390-1412`). Every corpus lens declares `Engine: "full"` (118/118,
census 6) and the simple engine no longer exists (`internal/refractor/ruleengine/` holds `full` only), so "parse with
the full engine" is the whole plain-lens derivation.

**Positional feeders.** A targetId is the row prefix, and a second lens rendering the same prefix feeds the target while
naming no `lensRef` (the lint's key-prefix rule, `definition.go:337-338`). For an actorAggregate lens the prefix is
declared (`OutputKeyPattern`); for a plain lens it is a **runtime value** (the first RETURN item's value, joined by
`natskv.go:308-320`), not derivable statically. This design's holders read the **lens the target names**; a positional
second feeder stays covered by the row-level §10.8 raise and, for same-package actorAggregate lenses, by the lint.
Stated so nobody reads the Weaver's load-time verdict as "no undeclared column can ever arrive" — it means "no
undeclared column from the lens the target names".

## 3. Grounding table

### 3.1 The existing pattern each part extends

| Part | Precedent extended | Not greenfielded |
|---|---|---|
| live state at artifact validation | `SensitiveAspectResolver` (`capabilitymaterializer_starlark.go:18-35`): injected; when a read entry names an aspect and no resolver was supplied the check **refuses** that entry (`:492-520`); built per caller from a live conn (`newLiveSensitiveAspectResolver`, `review.go:553`, and the CLI twin) | a second injected resolver, same construction; **stricter** nil posture — any non-empty `lensRef` with no resolver is invalid, unconditionally (there is no "entry that names nothing" here), which is why the lint pin in §4.2 is load-bearing |
| one parse, many facts | `full.SpecLabels` → `pkgmgr.SpecLabels` through the injected `CypherParser` ("label facts are derived from the SAME parse as the error", `capabilitymaterializer.go:91-97`); `CompiledRule.Query` keeps the AST (`ast.go:253`) | `Columns` becomes one more fact of that parse; the six wrappers/doubles copy one more field |
| the installer reads installed state | `Install` (`installer.go:103`) and `Apply` (`apply.go:144`) both open with `i.preflight(def)`; the installer already reads Core KV for manifests (`installer.go:967,1085,1889`); platform binaries are the P5 exception | one live preflight step after the pure one, on both entries |
| the Weaver indexes a non-target class from the same feed | `indexOpMeta` / `indexPattern` (`registry.go:1461`, `:1364`) | `indexLens` beside them; `lensClass` joins the `routed` set |
| a level-maintained, target-scoped Health issue | `issueKeyGapConfig` (`evaluator.go:2460`) is raised per (target, col) and shared with `clearClosedMarks`'s clear — which is why the new raise gets its **own** prefix (§4.4) | new prefix `gapUndeclared:`; `set`/`clear` by level on every (re)index |
| a gate's derived set is shared with its sibling | the lint-gates dossier: "find the sibling that derives it and diff" — `declaredRowBodyColumns` vs `gapColumnsOf` is that pair today | both collapse onto `internal/lenscolumns` (§4.1) |

### 3.2 Permission envelope

Every reader here is a platform binary or Loupe: the installer (`lattice-pkg`, Loupe's review handler), `cmd/bridge`,
`cmd/lattice`, `cmd/loupe`, `cmd/weaver`. All already hold Core-KV read; no `$JS.API.*` verb is added; `lint-conventions`'
P5 gate lists `bridge` and `lattice` among the platform binaries. No new write anywhere.

### 3.3 Import graph (`go list -deps`, run 2026-09-06)

`internal/pkgmgr`, `internal/weaver` and `internal/refractor/ruleengine/full` import none of each other. `full` pulls
`substrate`, `refractor/adjacency`, `refractor/subjects`. The design adds **no** edge among the three packages: the
parse stays injected into pkgmgr (its standing rule), is injected into the Weaver engine at the composition root
(§4.4), and the shared derivation lives in a new leaf `internal/lenscolumns` (`encoding/json` + `strings` only). The
`cmd/weaver` **binary** does gain the engine's footprint (`ruleengine`, `adjacency`, `subjects`) through that wiring —
the package boundary is what is preserved, not the link size (§7 row 8).

### 3.4 Executable censuses (run 2026-09-06 at `ed55c166`; re-run by the reviewer, all matched; the build's Phase-0 re-runs each)

| # | Census | Command | Result |
|---|---|---|---|
| 1 | corpus debt against the invariant | `go run ./scripts/lint-gap-column-declaration.go` | `clean — 30 target(s) across 14 package(s); 31 weaver-targets lens(es) read, 50 gap column(s) checked …; 0 exempt` ⇒ **zero migration debt; the install refusal is blocking from day one**. The lint also flags an empty `LensRef` as a finding, so no package target is unbound |
| 2 | NanoID-shaped `LensRef` literals in packages | `grep -rhn "LensRef:" packages --include='*.go' \| grep -v _test \| grep -c '"[A-Za-z0-9_-]\{20\}"'` | **2** — `clinic-reminders/targets.go:32` (`appointmentReminders`), `augur/targets.go:19` (`augurDispatchPending`); both same-package canonical names, resolved by the map before the NanoID passthrough |
| 3 | lens canonical names that pass `IsValidNanoID` | 20-char names from `CanonicalName: "…"` literals, filtered by the alphabet (the reviewer ran `keys.IsValidNanoID` itself) | **3 of 11**: `appointmentReminders`, `augurDispatchPending`, `capUpgradeRosterLens` |
| 4 | validator callers (each needs the resolver wired) | `grep -rn "ValidateCapabilityArtifact(" --include='*.go' cmd internal \| grep -v _test` minus the definition | **5**: `cmd/bridge/main.go:288`, `cmd/lattice/capability/capability.go:317`, `cmd/loupe/weaverauthor.go:179`, `:192`, `cmd/loupe/review.go:597` |
| 5 | `CypherParser` doubles (each copies the new field) | `grep -rln "Parse(ruleBody string) (pkgmgr.SpecLabels" --include='*.go' .` | **6 files**: `cmd/bridge/main.go`, `cmd/lattice-pkg/cypherparser.go`, `cmd/lattice/capability/cypherparser.go`, `cmd/loupe/review.go`, `internal/testutil/cypherparser.go`, `packages/capability-author/proposal_test.go` |
| 6 | engines in the corpus | `grep -rhn 'Engine:' packages --include='*.go' \| grep -v _test \| sort \| uniq -c` | **118 `"full"`, 0 other** |
| 7 | install-time gap rules in the installer | `grep -n "gapColumnPrefix\|validateGapCompanionPair(" internal/pkgmgr/orchestrationguard.go` | key convention (`:185`) + companion pair (`:230/:454`) — **no subset rule** |
| 8 | apply drivers that never re-validate | `grep -n "ValidateCapabilityArtifact" cmd/lattice-pkg/main.go` | **0** — `lattice-pkg apply-proposal` runs `ApplyCapabilityPlan` (`:560`) on the recorded verdict; the installer bound is the only gate on that driver |
| 9 | lens shapes outside the actorAggregate/plain pair | `grep -rn 'Kind: *"eventStream"' packages --include='*.go' \| grep -v _test` · `grep -rn "EntryKeyColumn:" packages --include='*.go' \| grep -v _test` | **1** eventStream (`orchestration-base` `loomFlowHistory`, bucket `orchestration-history`) · **0** entry-keyed |
| 10 | installer entry points that must carry the live step | `grep -n "^func (i \*Installer) \(Install\|Apply\)(" internal/pkgmgr/*.go` · `grep -rn "inst.Install(ctx" --include='*.go' internal cmd` | **2** entries; `Install` is called directly (`internal/testutil/install_phase1_packages.go:87`), `ApplyCapabilityPlan` routes through `Apply` (`capabilityapply.go:159`) |
| 11 | kernel/primordial targets bound by NanoID | `grep -rn "WeaverTarget" internal/bootstrap/` | bucket names only — bootstrap installs no `meta.weaverTarget` and is not an installer driver |

## 4. The shape

### 4.1 One derivation: `internal/lenscolumns` (leaf)

```go
// Spec is the subset of an installed lens spec (vtx.meta.<id>.spec, build.go lensSpecBody) or a
// pkgmgr.LensSpec that decides which keys its rows carry. JSON tags match the stored body.
type Spec struct {
    ProjectionKind string   `json:"projectionKind,omitempty"`
    Output         *Output  `json:"output,omitempty"`
    Source         *Source  `json:"source,omitempty"`
    CypherRule     string   `json:"cypherRule,omitempty"`
    CypherBranches []string `json:"cypherBranches,omitempty"`
}
type Output struct {
    BodyColumns        []string `json:"bodyColumns"`
    StaticEmptyColumns []string `json:"staticEmptyColumns,omitempty"`
    EntryKeyColumn     string   `json:"entryKeyColumn,omitempty"`
}
type Source struct{ Kind string `json:"kind"`; Project *struct{ Columns map[string]json.RawMessage `json:"columns"` } `json:"project,omitempty"` }

// Projected returns every key a row of this lens carries and which declaration each came from
// ("Output.BodyColumns" | "Output.StaticEmptyColumns" | "Source.Project.Columns" | "RETURN"), dispatching
// on the §2 whitelist in order: eventStream → actorAggregate (Output != nil, no EntryKeyColumn) → plain.
// Every other shape, a nil returnColumns on a plain lens, a parse error, or an empty RETURN returns
// ErrUnreadable with the reason — "not indexable" is a distinct verdict from "no columns".
func Projected(s Spec, returnColumns func(rule string) ([]string, error)) (Result, error)
type Result struct{ Columns map[string]string; Source string }
// Gaps filters Columns to the missing_ prefix (the Strategist's scan).
func Gaps(r Result) map[string]string
```

Plain precedence: `CypherBranches` non-empty ⇒ parse branch 0 (`cypherRule` is empty by construction); else
`cypherRule`. `pkgmgr.declaredRowBodyColumns` and the lint's `gapColumnsOf` become calls into it for the
actorAggregate shape — one derivation, diffed by construction. **The lint keeps its plain-lens UNREADABLE bucket**: it
selects a target's feeders by declared key prefix (`checkTarget:292-301`), which a plain lens does not have, so handing
it RETURN columns would make such a lens column-readable but never feeder-eligible. Reading plain lenses is the
runtime holders' job (§4.3, §4.4), where the binding is the `lensRef`, not the prefix.

**The parse seam.** `full.LabelFacts` gains `Columns []string` — the RETURN items' names in order, produced by a new
`(*CompiledRule).ReturnColumns()` that calls the **same** naming the executor uses (`itemAlias` is lifted to a function
the executor and `ReturnColumns` both call; no second derivation). `pkgmgr.SpecLabels` mirrors the field; the six
wrappers/doubles (census 5) copy it. `returnColumns` above is `func(rule) { l, err := parser.Parse(rule); return
l.Columns, err }` at every site — the parser stays injected into pkgmgr exactly as today.

### 4.2 Authoring preflight — the artifact validator

```go
// InstalledLensResolver resolves a weaverTarget artifact's lensRef to the columns the bound lens projects.
// Injected like SensitiveAspectResolver: ValidateCapabilityArtifact never touches a live substrate itself.
// found is true only for a root whose class is meta.lens (a target, pattern, DDL or op-meta id answers false —
// the same class test the installer applies, §4.3 step 2). A caller that also holds a lens NOT yet installed
// (Loupe's co-authored Check) answers a canonicalName from that request first, then from the installed catalog.
type InstalledLensResolver interface {
    ResolveLensColumns(lensRef string) (cols lenscolumns.Result, found bool, err error)
}
func ValidateCapabilityArtifact(kind string, content json.RawMessage, parser CypherParser,
    requesterHeld []HeldPermission, sensitiveAspects SensitiveAspectResolver,
    installedLenses InstalledLensResolver) (ArtifactValidationReport, error)
```

`validateWeaverTargetArtifact` gains, after the shape checks: `lensRef` non-empty (already) → resolver `nil` ⇒
**invalid** ("no installed-lens catalog was supplied to resolve lensRef %q — a weaverTarget artifact may not bind an
unverified lens") → `found=false` ⇒ invalid ("lensRef %q names no installed lens; install the lens first" — the
caller-neutral wording; Loupe's Check appends "or propose it alongside this target", the one caller for which that
remedy exists, since the bridge carries one artifact per request, `capability_author.go:28-32`) → `Projected` error ⇒
invalid naming the reason → every `missing_*` in `Gaps(result)` must be a `gaps` key, else invalid with the lint's
wording ("… add a gaps entry naming the remediation action — or, if the column is deliberately not remediated, declare
it `surface`"). An artifact target has no `augur` field (`GapActionArtifact`, `:153-166`; `unknownWeaverTargetFields`
keeps it out), so the `unplannable` exemption never applies here.

**Per caller (census 4), what fills the blank — every input labelled:**

| Caller | Resolver | Value provenance |
|---|---|---|
| `cmd/loupe/weaverauthor.go:179` (Check) | composite: `req.Lens` by canonicalName (the co-authored draft — caller-owned, but it is the very lens the author is declaring, so the check is "your target vs your lens"), then installed lenses by canonicalName **and** NanoID via the `readers` the handler already built (`weaverCoreReaders`, `weaver.go:1287`), reading the **root's class** as well as the spec (`buildLensCanonicalIndex` alone does not — it excludes patterns and targets by probing spec fields and would admit a DDL or op-meta id) | installed half platform-owned (Core KV); a lone target naming an installed lens by canonicalName resolves here, exactly as `resolveWeaverTargetLensRefs` will rebind it at propose |
| `cmd/loupe/review.go:597` (fresh verdict at approve; refuses on invalid at `:650`) | installed only, from `conn` — `newLiveInstalledLensResolver(ctx, conn)` beside `newLiveSensitiveAspectResolver` | platform-owned |
| `cmd/lattice/capability/capability.go:317` | same constructor, built only for `kind == "weaverTarget"` (the `opMeta`-only idiom at `:311`) | platform-owned |
| `cmd/bridge/main.go:288` | the composition root closes `capabilityArtifactVerdict` over the bridge's conn; the `nil, nil` comment there ("this adapter authors weaver targets and nothing else, so neither is ever consulted") is rewritten — the third dependency **is** consulted for exactly that kind | platform-owned; the `lensRef` the adapter records is the id its own catalog lookup resolved from the model's canonicalName (`capability_author.go:1250-1262`) |
| `cmd/loupe/weaverauthor.go:192` (the paired `lens` artifact) | `nil` — the lens kind never consults it | n/a |

Wiring is not optional and not left to the compiler: `scripts/lint-conventions.go` gains a pin that every
`ValidateCapabilityArtifact(` call site outside `internal/pkgmgr` passes a non-`nil` sixth argument **or** a kind
literal that is not `"weaverTarget"` (the pkgmgr dossier's "an injected dependency held in a nil-able field silently
disables the gate it feeds" — the `NewInstaller` pin, `lint-conventions.go:601,1504,1708`, is the idiom).

**Verdict provenance.** The recorded `validation.state` is caller-supplied (Loupe's propose carries the Check verdict
verbatim; the DDL copies it through), so this layer is **legibility** — the author is told at the moment they can fix
it. The **bound** is §4.3.

### 4.3 The apply-time bound — the installer

A live preflight, `i.preflightLive(ctx, def)`, called immediately after the pure `i.preflight(def)` on **both**
entries — `Install` (`installer.go:109`) and `Apply` (`apply.go:145`) — so it precedes the `DryRun` return
(`apply.go:274`): **a dry-run preview shows the refusal** rather than previewing a delta the real apply would refuse.
For each `WeaverTargets[i]`:

1. `LensRef == ""` → passes through unchanged (`resolveLensRef`'s "no lens binding declared" case; the lint flags it
   for packages, the artifact validator requires it). An in-batch canonicalName → the existing pure checks already ran
   over the Output union; additionally run the subset check over `Projected(that lens)` so an in-batch **plain** lens
   is read too.
2. Otherwise the ref is NanoID-shaped. `KVGetMulti(CoreBucket, ["vtx.meta.<ref>", "vtx.meta.<ref>.spec"])`: root
   absent or tombstoned, or `class != "meta.lens"` → **refuse** `ErrLensBindingRefused`: `pkgmgr: WeaverTarget[%d] %q:
   LensRef %q names no installed meta.lens (a canonicalName of exactly 20 alphabet characters reads as an id here —
   declare the lens in this package, or bind the installed lens's id)`. This closes §1's lookalike hole and
   `resolveLensRef`'s unchecked passthrough; `resolveLensRef` itself is unchanged.
3. Decode the `.spec` body's `data` (the `unwrapSpecBody` shape the Weaver uses) into `lenscolumns.Spec`; `Projected`
   with the installer's injected `SpecParser` → `ErrUnreadable` ⇒ **refuse** naming the reason. **`SpecParser`'s nil
   semantics change here:** today nil only disables `lenslabelcap`; after this step a nil parser makes every
   out-of-batch plain-lens binding a refusal. The `NewInstaller` pin's finding text is updated to say so.
4. Subset: every `missing_*` in `Gaps` is a `gaps` key, **unless** `t.Augur != nil && Escalate ∋ "unplannable"` (the
   lint's exemption, `escalatesUnplannable`) → else **refuse** with the lint's wording.
5. The companion-pair check (`validateGapCompanionPair`) runs over the resolved out-of-batch lens too — its "cannot see
   through" skip retires for NanoID refs (the doc comment's two-absence paragraph is rewritten: the remaining skip is a
   lens with no readable columns, which step 3 now refuses).

Cost: one `KVGetMulti` of ≤ 2 keys per out-of-batch target per install/apply (0 targets today). The refusal reaches
every driver — `lattice-pkg apply` / `apply-proposal` (`main.go:560`), Loupe's review apply (`review.go:809`), direct
`Install` callers — and Loupe's error-mapping (`cmd/loupe/pkg.go:377-393`, default 502) must carry
`ErrLensBindingRefused` as a 409-class terminal refusal (the pkgmgr dossier's "every surface that renders it").
Bootstrap is not a driver (census 11).

### 4.4 The closed-set verdict — the Weaver target source

`targetSource` gains a lens index fed by the spec events it already receives and drops:

- `lensClass = "meta.lens"` joins the **`routed` set** (`registry.go:561`) — not only `dispatchSpec`'s switch: the
  vertex-arrival arm returns before dispatching a buffered spec for an unrouted class (`:571-573` precede `:578-580`), so
  a lens whose `.spec` replays before its vertex would otherwise be dropped until the next spec write. `dispatchSpec`
  routes it to `indexLens(id, body)`: `unwrapSpecBody(body, "cypherRule")`, decode into `lenscolumns.Spec`,
  `Projected(spec, s.returnColumns)` → `s.lens[id] = lensEntry{gaps map[string]string, unreadable string}`;
  `removeSpec`/`removeVertex` delete it. (`indexOpMeta` still runs for op-meta vertices; the arm is reshaped, not
  removed.)
- `s.returnColumns func(string) ([]string, error)` is a new `Config` field (`engine.go:54`) consumed by
  `newTargetSource`, wired in `cmd/weaver/main.go` as `full.SpecLabels(...).Columns` — the composition-root injection
  pkgmgr uses. **`nil` makes every plain lens `unreadable`, never declared-empty** (`Projected`'s contract). There is
  no `NewEngine` sanctioned-caller pin today (census: `grep NewEngine scripts/lint-conventions.go` → 0); Inc 3
  **authors** one on the `NewInstaller` idiom, requiring the field at the sanctioned caller.
- **Verdict, level-triggered, on every (re)index of either side:** `evaluateBinding(targetID)` runs at `dispatchTarget`
  (load and update), at `indexLens` for every target whose `LensRef == id`, and at lens/target removal. It computes:
  - lens not indexed → *pending*; after `pendingSpecWarnAfter` (**5 min**, `registry.go:442` — the grace
    `flagOrphanedSpecs` already uses; the detection latency for a dangling binding is that window) → `warning`
    `LensRefUnresolved` at `issueKeyLensRef(targetID)`: "target %s binds lensRef %s which names no installed lens";
  - lens indexed but `unreadable != ""` → `warning` `LensColumnsUnreadable` at the same key, message = the reason;
  - readable → for each `missing_*` in `Gaps` not in `target.Gaps`, unless the target escalates `unplannable`:
    `warning` `GapUndeclaredByPlaybook` at `issueKeyGapUndeclared(targetID, col)` (`issuePrefixGapUndeclared =
    "gapUndeclared:"`), message = the lint's sentence; every key of that family for the target **not** in the current
    undeclared set is cleared. The issue set is a pure function of (target spec, lens spec) — no latch, no cadence.
- **Severity and its consequence, priced.** `aggregateStatus` (`health.go:1266-1280`) reports `unhealthy` on any
  `error` and **`degraded` on any other issue** — so every raise above holds that Weaver at `degraded` for as long as
  the fault stands. That is the status the row-level `GapWithoutPlaybook` raise already produces once a row arrives
  (`evaluator.go:328-334` chose `warning` for exactly this reason); the load-time verdict only moves it earlier. A
  nil `returnColumns` would put every plain-lens-bound target's Weaver at `degraded` fleet-wide — which is the
  reason the wiring is pinned at build time (above), not left to the runtime to report.
- Dispatch is untouched: the row-level `GapWithoutPlaybook` raise and the long floor stay exactly as §10.8 promises.
  The two families share no key (`gapConfig:` vs `gapUndeclared:`), so `clearClosedMarks`'s retirement of the former
  never touches the latter, and neither raise makes the other's absence ambiguous.

## 5. State-lifetime tables

**Weaver lens index + verdict issues** (the only new stateful mechanism; the validator and installer are pure per call):

| Boundary | `s.lens[id]` | `gapUndeclared:` / `LensRef*` issues |
|---|---|---|
| created | on a `meta.lens` `.spec` event whose vertex class is known, in **either** order — spec-before-vertex buffers in `pendingSpecs` and is dispatched on vertex arrival now that `lensClass` is routed | on the first `evaluateBinding` that finds an undeclared column / an unreadable or unresolved lens |
| boot / restart | rebuilt from the history replay (`IncludeHistory: true`); target/lens order is arbitrary, so the verdict is re-run on **either** arrival and `LensRefUnresolved` waits the 5-min grace | re-derived in full; nothing is carried across a restart (Health KV issues are the engine's own self-report, republished on the heartbeat) |
| lens spec update (hot-reload, package upgrade, `upgradeExisting` proposal) | replaced | re-evaluated for every target bound to that id — a column added after arming raises within one CDC hop; one removed clears |
| target spec update (gaps edited) | — | re-evaluated; declaring the column clears its issue on the same event |
| lens removed (tombstone / uninstall) | deleted | bound targets flip to `LensRefUnresolved` after the grace (an uninstall that leaves a dangling target is a real fault, not noise) |
| target removed | — | every `gapUndeclared:<targetId>.*` and `LensRef*` key for it cleared (`removeVertex`/`removeSpec`, beside `issueKeyTarget`) |
| `IsDeleted` events | tombstone body is preserved by the Processor; `removeSpec` keys on `evt.IsDeleted`, the same test `removeVertex` uses today | — |
| single vs many Weaver instances | per-instance index; issues are per-instance Health entries (Contract #5), identical on every instance because the input is Core KV | — |

**Installer/validator predicate — the state table before the predicate (outcome column is the verdict):**

| `lensRef` shape | in-batch canonicalName | installed `meta.lens` by id | Outcome |
|---|---|---|---|
| empty | — | — | pass through at install (unchanged); the lint flags it for packages; invalid for an artifact (already) |
| canonicalName, declared in this Definition | yes | — | subset over `Projected(that lens)`; an in-batch plain lens is now read |
| canonicalName, not declared here, not NanoID-shaped | no | — | validator: found only if a caller-supplied source (Loupe's co-authored `req.Lens`) answers, else invalid; installer: `resolveLensRef`'s existing refusal |
| 20-char alphabet lookalike (`appointmentReminders` from another package or a Loupe draft) | no | **no** | **refused** (step 2) / invalid (`found=false`) — was: silently installed bound to nothing |
| NanoID, installed, actorAggregate with Output | — | yes | subset over the Output union |
| NanoID, installed, plain | — | yes | subset over RETURN names |
| NanoID, installed, eventStream | — | yes | subset over `Source.Project.Columns` keys |
| NanoID, installed, entry-keyed or actorAggregate without Output | — | yes, unreadable | **refused**, reason named (was: nothing checked) |
| NanoID, installed, but the id is a `meta.weaverTarget` / `meta.loomPattern` / DDL / op-meta | — | wrong class | **refused** ("names no installed meta.lens") — validator answers `found=false` on the same class test |
| NanoID, absent or tombstoned | — | no | **refused** |
| NanoID, installed, rule unparseable / no RETURN | — | yes, unreadable | **refused**, reason named |
| any of the above, target escalates `unplannable` (package path only) | | | existence and readability still enforced; the subset rule is exempt (the undeclared column routes to the Augur by design) |
| `DryRun` | | | the live preflight runs; a preview that would be refused reports the refusal instead of a delta |
| re-run (same Definition applied twice) | | | idempotent — pure over the same inputs |
| never-written: a package whose lens gains a `missing_*` column via `make reinstall-package` without CI | | | the installer refuses the upgrade — the case the lint alone could not hold |

## 6. Contract surface

**Builds to** `docs/contracts/10-orchestration-weaver.md` §10.8: "`lensRef`: `<meta.lens id of the violation Lens>`"
(an existence check makes that sentence hold), and "a row column `missing_*: true` with no `gaps[col]` entry is a
config error → alert, never silently skipped … Weaver surfaces it to Health KV" (the Weaver now surfaces it at load as
well as per row; the row behaviour is unchanged). Contract #5 §5.5 issue-record shape is honoured; the Health-KV schema
doc gains the three codes in the same change (steward §4's health-emission rule).

**Changes** §10.8 — one install-time validation clause, **text of record here, landing with Inc 2's commit** (the
2026-09-01 exception: a refusal clause is held out of the tree until the runtime keeps it). Added to the "§10.2 ↔ §10.8
binding" list after the `gaps`-key bullet:

> - **A `lensRef` MUST name a Lens that exists, and the target MUST be fully declared against it.** A `lensRef` names a
>   Lens declared in the same package or an already-installed `meta.lens` by id; install refuses a binding to anything
>   else. Every `missing_*` column the named Lens projects MUST be a `gaps` key — a `surface` entry for a column
>   deliberately left unremediated — unless the target's `augur.escalate` includes `unplannable`; install refuses a
>   target that leaves one undeclared, whichever package or authoring path produced the Lens or the target. The
>   row-level rule above is unchanged: a column that still arrives undeclared (a Lens edited after install, a second
>   Lens rendering the same `<targetId>.` prefix) is the config error → alert it has always been.

Scope, stated: the refusal is over the Lens the target **names**; a positional second feeder is the row-level rule's,
as the clause's last sentence says. An absent `lensRef` names nothing and is not a binding; the clause does not promise
its refusal (§4.3 step 1). Affected consumers: package authors (zero corpus debt, census 1); Loupe's weaver-author and
review pages (they gain a refusal reason to render); the bridge's capability author (its existence check already holds;
the subset rule is new to it, and the resolver wiring is Inc 2's). Public posture: observable promises only — no file,
function or mechanism named in the clause.

## 7. Alternatives

| # | Option | Verdict |
|---|---|---|
| 1 | **Do not have this thing** — keep the runtime `GapWithoutPlaybook` warning + long floor, Loupe's observed-row `Unhandled` list (`weaver.go:855-863`), and the CI lint | Rejected. Row 8's Long was ratified on the premise that a gate holds the invariant; it holds on one of four paths and cannot read the lens shape every artifact produces. Price of the status quo per stuck row: 1 of 1024 ack slots, a `clearClosedMarks` pass per 5 min, `degraded` once a row arrives, and the author learning after apply and after the first violating row — for a human at Loupe, after the review that approved it. The 20-char lookalike installs a dead target silently. Cheap machinery (a leaf package, one resolver, one preflight step, one index) against a ratified soundness premise. |
| 2 | Validator only (no installer step) | Rejected: the recorded verdict is caller-supplied and `lattice-pkg apply-proposal` never re-validates (census 8); a forged or stale `valid` reaches Weaver. |
| 3 | Installer only (no validator) | Rejected as the whole answer, kept as the bound: the author of a Loupe draft learns at the apply click, after review — the Check endpoint exists to preflight, and the resolver costs one read. Combined with row 2 they are the pair the `SensitiveAspectResolver` precedent already ships (record-time legibility + approve-time re-validation). |
| 4 | Weaver **refuses** the target at load (`TargetRejected`) instead of warning | Rejected: contradicts §10.8's "alert, never silently skipped" by silently skipping the target's other gaps; an availability regression the moment a lens hot-reloads a new column on a live target; and the enforcement point for lifecycle hygiene is authoring/install (the skill's §2.3 D rule), not the consumer. The Weaver's job is the closed-set **verdict**. |
| 5 | Weaver check only (drop the validator + installer) | Rejected: a warning after the fact is observation, not a holder; a dangling `lensRef` would still install. |
| 6 | Refractor records the projected column set on the lens at activation (computed → recorded) | Rejected: Refractor is not a Core-KV writer (P2) and the fact is already derivable from the stored spec; a recorded copy is a second source of truth the hot-reload path must keep in step. |
| 7 | Reverse check in the `lens` artifact validator (installed targets bound to this lens) | Rejected in favour of row 4's Weaver verdict, which covers the post-edit case for every path with one mechanism instead of a second resolver that covers only artifacts. |
| 8 | Import `ruleengine/full` into `internal/weaver` and `internal/pkgmgr` directly | Rejected: pkgmgr's standing rule keeps the parser injected; the Weaver **engine package** follows the same composition-root injection (§4.4). The `cmd/weaver` binary grows by the engine's link footprint either way (§3.3); what injection preserves is the package boundary and the nil-pinned wiring, not the binary size. |
| 9 | Retire the `IsValidNanoID` passthrough in `resolveLensRef` (refuse any NanoID-shaped canonicalName) | Rejected: cross-package binding by id is a sanctioned shape (the installer's own doc); the existence check is the honest closure — it refuses the lookalike **because nothing is installed under it**, not because of its spelling. |
| 10 | Derive columns by blacklist ("not actorAggregate ⇒ plain") | Rejected by the pass: eventStream lenses carry no cypher, entry-keyed lenses carry a runtime shape, and an Output-less `actorAggregate` never activates — each would have read as empty or refused a legitimate install. The whitelist of §2 is the derivation. |

Rows 2–5 were priced in combination: 2+3 without 4 leaves the post-edit case (a lens edited after its target armed) to
the row-level raise; 4 without 2+3 leaves a dangling binding installable. The three together are the design; none
patches a gap another created.

**Dead-scaffolding test.** Inc 1 (the derivation) is consumed by the lint (the actorAggregate branch it computes today
now comes from the shared helper) and by Inc 2 in the same fire; Inc 2's consumers exist today (Loupe weaver-author is
live; the installer runs on every apply); Inc 3's consumer is the Health plane every Weaver already publishes to. No
increment waits on an absent consumer.

## 8. Reconciliation with the existing mental model

- *Didn't the lint already handle this?* It holds the invariant for compiled packages through CI and says, in its own
  header, which paths it cannot reach. This design is those paths, sharing the lint's derivation rather than adding a
  second one.
- *Isn't `validateGapCompanionPair` already the installer's gate?* It is the companion-pair rule (§10.3), skipping
  out-of-batch lenses; the subset rule was never in the installer. Inc 2 gives both rules the resolved lens.
- *Does this duplicate Loupe's `Unhandled`?* No — that reads **observed** rows on demand; the Weaver verdict reads
  **declarations** at load and reaches Health KV. Loupe's page should render the new issue family (a follow-on Loupe
  row; not this design's scope).
- *Is the AI-authoring path a dead consumer?* Dormant, not absent: the human Loupe path is live and uses the same
  validator, and the bridge's author already resolves `lensRef` existence against the catalog (§1). Nothing here is
  built for the dormant path alone.
- *Does this add state we keep elsewhere?* The Weaver index caches what Core KV already stores; it is rebuilt from
  history on every boot and never written back.

## 9. Migration, compatibility, tests, risks

**Migration.** Zero corpus debt (census 1) ⇒ the install refusal is blocking from the first build. Existing installed
targets are untouched (no reprojection, no reinstall); the Weaver's first boot after Inc 3 evaluates every installed
pair and is expected to raise nothing on the dev stack (the lint's 50/50).

**Compatibility.** `ValidateCapabilityArtifact`'s signature changes (five callers + doubles, census 4/5) — a
compile-time migration, no wire change. `SpecLabels`/`LabelFacts` gain a field (additive). `Config` for `NewEngine`
gains a field (nil-safe by construction, pinned at the sanctioned caller). `Installer.SpecParser`'s nil semantics
tighten (§4.3 step 3). Contract #5 emission gains three codes.

**Test strategy — each pinned to an increment (§11).**
- `full`: `ReturnColumns` vectors — alias; `VariableRef`; `PropertyAccess`; `_col<i>` for a literal/function; a
  `WITH … RETURN` (RETURN wins); `cypherBranches` (branch 0); pinned against the executor's naming by driving one
  query through both and comparing keys (one derivation, proven, not asserted).
- `lenscolumns`: table test over every §2 row including the three unreadable shapes; `nil` parse ⇒ `ErrUnreadable`,
  never empty; branches-over-rule precedence; `Gaps` prefix filter.
- lint: `gapColumnsOf` delegation keeps the corpus counts (31 lenses read, 50 columns — must not drop); self-test
  unchanged in verdicts.
- pkgmgr validator (the dossier's mandated shape: drive `ValidateCapabilityArtifact`, not the rule): nil resolver ⇒
  invalid with the exact nil wording; `found=false`; aggregate declared/undeclared; plain declared/undeclared; a
  `surface` entry satisfies; a wrong-class id ⇒ `found=false`; the lookalike `appointmentReminders` as `lensRef` with
  no such installed id ⇒ invalid.
- pkgmgr installer (embedded NATS via `natsfixture`; seed `vtx.meta.<id>` + `.spec` as `build.go` writes them): each
  row of §5's second table on **both** `Install` and `Apply`, `DryRun` included; the `unplannable` exemption; the
  companion-pair check now reaching an out-of-batch lens; a negative-with-positive pair for every refusal; the
  `NewInstaller` pin's updated finding text.
- Weaver `registry_source_internal_test`: lens-before-target and target-before-lens (same verdict — the spec-before-
  vertex replay for a lens is its own vector), lens update adds a column ⇒ raise, gaps update declares it ⇒ clear,
  target removal ⇒ clear, lens removal ⇒ `LensRefUnresolved` after the grace, plain lens with nil parse ⇒
  `LensColumnsUnreadable`, eventStream lens ⇒ judged on its projected columns, exempt target ⇒ nothing, and the
  dispatch path unchanged (the existing `GapWithoutPlaybook` vectors stay green untouched); `aggregateStatus` reads
  `degraded`, never `unhealthy`, with the new family present.
- Ephemeral-stack e2e (`make up`, Inc 2): `lattice-pkg apply` of a fixture package whose lens projects `missing_x`
  with no gaps entry is refused with the clause's wording; adding `surface` installs; Loupe's Check endpoint returns
  `targetValidation.valid=false` for the same draft against the installed lens.
- Lint pins (`lint-conventions`): non-nil sixth argument at every `ValidateCapabilityArtifact` call for the
  weaverTarget kind; the new `NewEngine` sanctioned-caller pin requires `ReturnColumns`.

**Risks.**
- *Install ordering for cross-package NanoID refs* — a target's package applied before its lens's package is now refused
  instead of silently dangling. Zero such refs today; artifacts always bind installed lenses; the refusal names the fix.
- *The bridge left unwired* — every AI-proposed target would record invalid (fail-closed); the lint pin makes that a
  build failure, not a runtime surprise.
- *A wiring gap at `cmd/weaver`* — every plain-lens-bound target's Weaver at `degraded`; same pin.
- *Boot cost* — ~120 lens parses per Weaver boot, milliseconds each, once.
- *Loupe's lone-target Check* — the composite resolver must answer canonicalName from the installed catalog, or a
  draft that `resolveWeaverTargetLensRefs` would rebind fine at propose reads invalid at Check. Named in §4.2's table.

## 10. Open questions — resolved

1. *Refuse or warn at the Weaver?* Warn; `degraded` priced and accepted (§0, §4.4, §7 row 4).
2. *Where does the plain-lens derivation live?* One place, injected everywhere the parser already is (§4.1).
3. *Is the lookalike hazard real?* Constructible (census 3), unexercised (census 2); closed by existence (§4.3 step 2).
4. *Does the exemption for `unplannable` transfer to artifacts?* No — they cannot declare `augur` (§4.2).
5. *Contract change or builds-to?* One clause (§6), text of record, lands with Inc 2.
6. *Does the lint gain reach?* No — its binding is positional; the runtime holders read plain lenses (§4.1).

## 11. Decomposition for the Steward

| Inc | Scope | Owns | Posture |
|---|---|---|---|
| **1 — one derivation** | `full.LabelFacts.Columns` + `(*CompiledRule).ReturnColumns()` sharing the executor's naming; `pkgmgr.SpecLabels.Columns`; the six wrappers/doubles; new `internal/lenscolumns` (whitelist of §2); `declaredRowBodyColumns` and the lint's `gapColumnsOf` delegate to it for the actorAggregate branch (corpus counts re-pinned) | the `full`, `lenscolumns` and lint tests of §9 | neutral (no behaviour change) |
| **2 — the holders** | `InstalledLensResolver` + the validator rule; five callers wired (composite in Loupe Check reading root class + spec, live constructor in review/CLI, composition-root closure in bridge); `Installer.preflightLive` on `Install` and `Apply` (existence + readability + subset + companion pair for out-of-batch lenses; runs under `DryRun`); `ErrLensBindingRefused` mapped in Loupe; `lint-conventions` pins (validator sixth argument; `NewInstaller` finding text); **the §6 clause lands in `10-orchestration-weaver.md` in this commit** | the pkgmgr validator + installer tests, the e2e, the lint pins | **posture-changing** (a new install refusal; contract-adjacent ⇒ the Steward's full adversarial depth per its §4) |
| **3 — the closed-set verdict** | Weaver `lensClass` routed + indexed, `Config.ReturnColumns` injected at `cmd/weaver` with a new `NewEngine` sanctioned-caller pin, `evaluateBinding`, the three issue codes, `docs/components/weaver.md` §health table + the Health-KV schema doc | the `registry_source_internal_test` vectors | neutral for dispatch; new Health emission (`degraded` while a fault stands) |

Increments 1→2→3 are sequential (2 and 3 consume 1; 3 does not depend on 2 but its verdict is only meaningful once
the install bound exists, so it ships last). Review depth stays the Steward's sizing.

## 12. Checklist walk (§2.3)

**A — demand.** The failure mechanism is read in code (§1's table, per path); the harm is a slot + a 5-min preamble +
`degraded` per row, and the silent dead target — counted, not asserted. "No live consumer" was run: Loupe weaver-author
is a live human path; the bridge author is dormant and named as such, and its own existence check was found and
credited. The lint's own header is the refusal whose reason was checked (it says what it cannot read; §1 confirms
each). The lookalike negative ("in-batch resolution wins") was restated as "for same-package refs" and the cross-package
member walked (§1, census 3).
**B — channels.** The transport for every derivation is the stored spec body (§2); retraction = the level-triggered
clear on every re-index (§5); the replaced skip (`validateGapCompanionPair`'s two-absence paragraph) is rewritten in the
same increment (§4.3 step 5); the permission envelope is read-only on platform binaries (§3.2); no import cycle (§3.3);
both injected functions' nil is fail-closed and lint-pinned (§4.2, §4.4); the seam both installer entries share is
named (`preflight`), not assumed.
**C — censuses.** Eleven, run and pasted (§3.4), re-run independently by the reviewer; the unit is named per row. The
classifier's error bucket (plain lenses UNREADABLE) was opened: it stays, for a structural reason now stated (§4.1),
and the three shapes a blacklist would have misread are on the whitelist (§2).
**D — predicates.** The state table precedes the predicate with an outcome column, `DryRun` and wrong-class rows
included (§5); omission fails closed (nil resolver ⇒ invalid; nil parse ⇒ unreadable); the governed set is derived
from the consumer's scan (§2); the exemption is the lint's, carried verbatim; "not found" and "not indexable" are
distinct verdicts; the new issue family has its own key so no existing clear makes its absence ambiguous (§4.4); both
holders apply the same class test so their verdicts agree (§4.2).
**E — state, time, cost.** Lifetimes at every boundary (§5); a `warning` makes the instance `degraded`, priced against
the existing raise's identical consequence; the grace is the real 5-min constant; boot cost priced.
**F — shape.** Row one of §7 is deletion; each rejection re-run against the recommendation; "mirrors X" — the
`SensitiveAspectResolver` doc comment (its per-entry nil posture is *weaker* than this design's, stated) and the twenty
lines above `resolveLensRef` were read (its own words, "a dangling control-surface reference is a config bug", are the
licence for step 2); every remedy is qualified per caller (§4.2).

## 13. Adversarial pass — findings and fold-in (2026-09-06, one cold read-only reviewer, Opus)

| # | Sev | Finding | Folded |
|---|---|---|---|
| 1 | BLOCKING | The lint selects feeders by declared key prefix; a plain lens has none, so "the lint's UNREADABLE bucket retires" was impossible and Inc 1's stated consumer false | §4.1 keeps the bucket and says why; Inc 1's consumer is the shared actorAggregate branch + Inc 2; §10 q6 |
| 2 | BLOCKING | The §2 derivation was a blacklist: `eventStream` (no cypher), entry-keyed (runtime shape) and Output-less `actorAggregate` lenses read as empty or refused a legitimate install | §2 is a whitelist keyed on the runtime's own conjuncts; `lenscolumns.Spec` carries `Source` and `EntryKeyColumn`; §7 row 10; census 9 |
| 3 | BLOCKING | `handle`'s vertex arm returns for an unrouted class before dispatching a buffered spec — a lens whose spec replays first would never index | `lensClass` joins `routed` (§4.4, §5 row 1) |
| 4 | MAJOR | The "30 s" grace does not exist; `pendingSpecWarnAfter` is 5 min | §4.4, §5 use the real constant; latency stated |
| 5 | MAJOR | The clause over-promised ("whichever … produced the Lens") beyond the named lens, and "MUST resolve" contradicted the empty-ref passthrough | §6 rewritten: over the Lens the target **names**; absence is not a binding |
| 6 | MAJOR | A `warning` holds the Weaver at `degraded`; unpriced | §0, §4.4, §7 row 1, §9 |
| 7 | MAJOR | "bootstrap's package reconcile" is not a driver; `Install` is a second entry `Apply` does not cover; the `NewEngine` pin cited does not exist | §4.3 seam = `preflight` on both entries; census 10/11; Inc 3 authors the pin |
| 8 | MAJOR | The bridge already resolves `lensRef` existence by catalog name and refuses unknown names; the doc had it as unheld and mis-cited the prompt | §1 row split (existence yes, subset no); §4.2 provenance corrected; §9 risk reworded |
| 9 | MAJOR | Loupe's canonical index reads no class — the validator would answer `found=true` for a non-lens the installer refuses | resolver contract requires root class `meta.lens` (§4.2), same test as the installer |
| 10 | MAJOR | The `SensitiveAspectResolver` nil posture is per-entry, not the unconditional refusal claimed as "verbatim" | §3.1 states the design's posture as stricter, and why the pin is load-bearing |
| 11 | MAJOR | "propose it alongside this target" is impossible for the bridge (one artifact per request) | validator wording caller-neutral; Loupe's Check appends the remedy it alone can honour (§4.2) |
| 12 | MINOR | `Installer.SpecParser` nil semantics silently tighten | §4.3 step 3 states it; the pin's finding text updated (Inc 2) |
| 13 | MINOR | Alt 8's "footprint" rationale defeated by the `cmd/weaver` wiring | §3.3, §7 row 8 corrected to the package boundary |
| 14 | MINOR | "verbatim"/"row-body keys =" imprecise: key column copied, `projectionSeq` stamped, envelope fields added | §1, §2 reworded (none `missing_`-prefixed) |
| 15 | MINOR | `cypherRule` is empty for a multi-walk lens; precedence unstated | §4.1 precedence: branches, else rule |
| 16 | MINOR | Citation drift (five pointers) | corrected in place |
| (f) | — | `DryRun` unaddressed | runs the live preflight; §4.3, §5 row |
| (g) | — | kernel targets | none; census 11 |

Verified-correct citations and all eleven censuses matched the reviewer's independent runs.
