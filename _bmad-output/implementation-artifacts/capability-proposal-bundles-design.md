# Co-proposed capability bundles — one proposal, N artifacts, one atomic apply — design

**Status: 📐 awaiting review (Winston-adjudicated per the 2026-08-20 delegation — no fork, no frozen-contract change; the §14 adversarial pass is this design's own gate and must be recorded before it is build-ready).**
**Component:** cross-cutting — `packages/capability-author` (DDL + 2 lenses) · `internal/pkgmgr` (materializer + apply plan) · `internal/bridge` (the `capabilityAuthor` adapter's result shape) · Loupe (Weaver Target Studio + review console, Stream 3).
**Backlog row:** Lattice lane → *AI-native* → *[capability-author] One authoring request cannot co-propose a NEW lens + the target that binds it* (★★, M).
**Builds on (all shipped):** [ai-authored-capabilities-design.md](ai-authored-capabilities-design.md) §3.1/§3.5 (the proposal vertex + the F-004 apply path) · [weaver-target-studio-design.md](weaver-target-studio-design.md) §6.4 (`SubmitCapabilityProposal`, the operator lane) · [natural-language-weaver-targets-design.md](natural-language-weaver-targets-design.md) §1 (the row's origin).
**Author:** Winston (Designer fire, 2026-08-21).

---

## For Andrew (ratify in one look)

**What it does, in two lines.** A capability proposal today carries exactly **one** artifact, so a
"new lens + the weaver target that binds it" intent cannot be proposed, reviewed, or applied as one
thing. This makes the **proposal the bundle**: one `vtx.capabilityproposal.<id>` carries an ordered
**list** of artifacts, and its apply materializes them into **one** `pkgmgr.Definition` submitted as
**one** F-004 `InstallPackage` batch — at which point the platform's existing in-Definition
`canonicalName → NanoID` resolution (`build.go:583`) binds the target to its co-proposed lens with
**no new resolution machinery at all**.

**No frozen-contract change; no architectural fork.** Every touched entity is `capability-author`
package data (version-bumped) plus `internal/pkgmgr`/`internal/bridge` internals; the apply path
stays the unmodified F-004 op; the replyOp envelope (`{externalRef, status, result}`, Contract #10
§10.5) is unchanged — only the adapter-defined JSON *inside* `result` gains a list. Contract #3
§3.9.1's batch ceilings are respected by an explicit, named bundle cap (§3.5).

**The row's `no-pattern:` prescription is half wrong, and the correction shrank the design.** The row
asked for *"multi-artifact proposal **+ cross-artifact id resolution**"*. Cross-artifact id resolution
**already ships, twice**: `resolveLensRef`'s in-Definition `lensByCanonical` branch
(`internal/pkgmgr/build.go:583-585`), and the exported deterministic derivation
`pkgmgr.LensID(packageName, canonicalName)` (`internal/pkgmgr/installer.go:457-465`) — package entity
keys are version-independent and deterministic in `(package, kind, canonicalName)` per Contract #8
§8.1. The genuinely missing primitive is **one reviewable, appliable unit spanning N artifacts**. §11
prices the "just compute the id" alternative and rejects it on atomicity, not on plumbing.

**Two things you should know that I found while grounding, neither of which this design fixes.**

1. **A ★★★ destructive defect in the shipped apply path, filed as its own row (§13.1).** An approved
   proposal with `target.mode: "upgradeExisting"` naming an installed **multi-entity** package
   (`cafe-domain` and friends — the deny-list's doc comment says that case *"is precisely what this
   fire exists to allow"*) builds a **one-artifact** Definition and hands it to `Installer.Apply`,
   which diffs it against the package's full `declaredKeys` and **tombstones every key the one-artifact
   Definition does not contain** (`upgrade.go` `diffManifest`, *"a key only in the old set →
   tombstone"*). An independent census briefed to falsify this came back **STANDS**: no merge step, no
   subset guard, Processor step-8's package-scope guard passes the batch (the keys are squarely
   *inside* `priorDeclared`), and no test installs a real multi-entity package and then applies an
   `upgradeExisting` proposal at it. This design neither uses nor worsens `upgradeExisting` (the
   Studio has always emitted `newPackage` only), and it is deliberately **not** folded in — different
   root cause, different fix shape. It gates any future `upgradeExisting` work.
2. **A live operator dead end this design closes.** The Studio's co-authored `{target + lens}` bundle
   already ships and already produces **two** proposals — deliberately targeting **two different
   package names** (`weaver-target-<slug>-<tok>` / `weaver-lens-<slug>-<tok>`,
   `cmd/loupe/web/js/logic/weaverauthor.js:237`) so their `newPackage` applies do not collide. The
   target's `lensRef` is the co-authored lens's canonicalName, which `resolveWeaverTargetLensRefs`
   deliberately skips for that bundle — so the target proposal is **unappliable by construction** and
   surfaces as a 409 at the final apply click. The shipped doc comment calls it *"an accepted,
   unchanged limitation of that path"* (`cmd/loupe/weaverauthor.go:349-359`). It is live today for
   every operator who authors a target and its lens together.

Everything else is resolved in the body.

---

## 1. Problem & intent

### 1.1 What the row said, and what grounding corrected

The row (filed off natural-language-weaver-targets §1) reads:

> `RecordCapabilityProposal` records one `{kind,content}` per request and single-artifact apply
> resolves `lensRef` only by NanoID or same-Definition name — so a new-lens-plus-target intent can't
> produce both atomically. NL v1 binds an existing lens instead.

Both clauses are factually correct. The framing is not: it presents the design as an **AI-path**
limitation whose missing primitive is **id resolution**. Grounding falsifies both halves.

| Row's premise | Ground truth | Citation |
|---|---|---|
| It is an AI-path (`RecordCapabilityProposal`) limitation | The **operator** path has the identical gap, live and shipped: `SubmitCapabilityProposal` also records one `{kind, content}`, and the Studio's Propose loops it once per artifact | `packages/capability-author/ddls.go:504-600`; `cmd/loupe/weaverauthor.go:456-491` |
| The missing primitive is cross-artifact id resolution | In-Definition `canonicalName → NanoID` resolution ships and is exercised by every hand-authored package that declares a lens and a target over it | `internal/pkgmgr/build.go:227-251, 579-590` |
| …and a co-proposed lens's id is unknowable before apply | Package entity NanoIDs are **deterministic and version-independent** in `(package, "lens:"+canonicalName)`, and the derivation is *exported* as `pkgmgr.LensID` | `internal/pkgmgr/installer.go:305-307, 443-465`; Contract #8 §8.1 |

The real missing primitive is **atomicity of the unit**: there is no object in the system that means
*"these N artifacts are one capability, reviewed together, installed together or not at all."*

### 1.2 The intent

- An operator authors a violation lens and the weaver target that converges on it in the Studio, hits
  **Propose once**, reviews **one** queue row, approves **once**, applies **once**, and both land in
  one Processor commit or neither does.
- The AI authoring path (`RequestCapabilityAuthoring` → model → `RecordCapabilityProposal`) can author
  the same shape, so an NL intent that needs a new lens stops being *"sketched in the rationale,
  finished by the operator by hand"* (natural-language-weaver-targets §1's Phase-0 scoping).

## 2. What already ships (the grounding ledger)

Every row cites the code that **does** the thing, never a comment that describes it.

| Mechanism | Where | Status for this design |
|---|---|---|
| `Definition` is a general multi-entity container (`DDLs`, `Lenses`, `Permissions`, `Roles`, `WeaverTargets`, `LoomPatterns`, `OpMetas`, …) | `internal/pkgmgr/definition.go:154+` | **Reused unchanged.** The cardinality-of-1 is a decision inside `DefinitionForCapabilityArtifact`'s per-kind helpers, not a limit of the type. |
| One atomic batch spanning every entity kind in a Definition, committed by one `InstallPackage` op | `internal/pkgmgr/build.go:46-52` (`buildInstallBatch`) | **Reused unchanged.** |
| In-Definition `canonicalName → in-batch lens NanoID` map, consulted before the NanoID fallthrough | `build.go:227-233`, `resolveLensRef` `build.go:579-590` | **Reused unchanged — this is the whole cross-reference answer.** |
| Deterministic, version-independent entity NanoIDs | `installer.go:293-320`, exported `LensID`/`RoleID`/`DDLID` `installer.go:447-470` | Grounds §11's rejected alternative. |
| `ReviewCapabilityProposal` / `MarkCapabilityProposalApplied` are **proposal-keyed**, never artifact-keyed — they touch only `.review` + links | `packages/capability-author/ddls.go:751-923` | **Unchanged by this design.** Strong evidence the proposal is already the review unit. |
| `.target` (`{mode, packageName, baseVersion, newVersion}`) is already **one aspect per proposal**, not per artifact | `ddls.go:729-733` | **Unchanged.** The data model already treats "where this lands" as a bundle-level fact. |
| Per-artifact deterministic validation, shared by record-time and apply-time | `ValidateCapabilityArtifact` `capabilitymaterializer.go:310-450`; the `<kind>ArtifactDefinition` helpers | **Reused per artifact.** |
| Apply-time dispatch-authority containment for an authored weaverTarget (protected op / protected pattern / protected-or-secure lens binding) | `internal/pkgmgr/authored_dispatch_scope.go:269-404`, called at `capabilityapply.go:223` over `def.WeaverTargets` (already a slice) | **Reused; §4 re-derives its soundness under bundling.** |
| Platform-protected-package deny-list at three chokepoints | `capabilityapply.go:79-88`; `cmd/loupe/review.go:705`, `:995` | **Unchanged — bundle-level by construction (one `.target.packageName`).** |
| A lens may project a whole nested object/array as one column | `ruleengine/full/values.go:23-98`, `executor.go:996-1005`; precedents `packages/identity-domain/lenses.go:154` (`jsonb`), `packages/capability-author/lenses.go:150` (`m.spec.data AS spec`, nats-kv) | **Reused for the new `artifacts` column.** `capabilityProposals` is a **nats-kv** lens, so no Postgres column typing is involved. |
| Atomic-batch ceilings: 998 business mutations, `max_payload` (1 MiB default) per value, both fail-closed and terminal | Contract #3 §3.9.1 | Bounds §3.5's bundle cap. |

**What does not exist:** any object meaning "these artifacts are one capability"; any bundle-level
validation; any apply that materializes more than one artifact.

## 3. The shape

### 3.1 The bundle IS the proposal

The unit of *review* and the unit of *apply* must be the same object, or a reviewer can approve the
target and reject the lens it binds. Review and apply are already **proposal**-keyed (§2). Therefore
the bundle is the proposal, and the change is confined to the one aspect that is per-artifact.

```
vtx.capabilityproposal.<NanoID>                                     # unchanged
  .request      { requesterId, intent, contextRef }                 # unchanged
  .artifact     { artifacts: [ {kind, content}, … ] }               # ← the ONLY data-model change
  .target       { mode, packageName, baseVersion, newVersion }      # unchanged — already bundle-level
  .rationale    { text }                                            # unchanged — one rationale per bundle
  .confidence   { score }                                           # unchanged
  .validation   { state, report, deltaPreview, checkedAt }          # unchanged — one verdict per bundle
  .provenance   { source, model, promptHash, catalogHash, … }       # unchanged
  .review       { state, invalidReason, reviewedAt, appliedAt, … }  # unchanged
  lnk.capabilityproposal.<id>.{requestedBy,reviewedBy,appliedAs}.…  # unchanged
```

The aspect key stays 4-segment (`vtx.<type>.<id>.<localName>`, Contract #1 §1). The artifact list
lives **inside one aspect body**, never as `artifact0`/`artifact1` siblings — an unbounded localName
family would be unprojectable by a flat lens and would break the 1:1 row shape every consumer reads.

**Ordering.** The list is ordered and the order is preserved end to end. Nothing in the apply depends
on it (`resolveLensRef` runs in memory over the whole Definition before any mutation is emitted), but
the review UI renders in author order and the deterministic-report indices reference it.

**Compatibility.** `.artifact.data.kind` / `.content` are **never written again** and **never removed
from the lens**: the `capabilityProposals` projection keeps both legacy columns (they read `null` on
every new row) and gains an `artifacts` column, so an already-recorded proposal from a pre-upgrade
binary still renders and still applies. One exported helper does the shape-tolerant read for every Go
consumer:

```go
// pkgmgr.ArtifactsFromAspectData returns the bundle an .artifact aspect body
// carries, accepting both the current {artifacts:[…]} shape and the pre-bundle
// {kind, content} single-artifact shape a proposal recorded before this
// version still holds. REMOVAL TRIGGER: delete the legacy branch once no
// deployment holds a proposal whose .artifact body lacks `artifacts` — the
// census in §6.C is the check.
func ArtifactsFromAspectData(data map[string]any) ([]CapabilityArtifact, error)
```

This is the *only* compatibility surface, and it is a genuine two-shape read rather than a torn row:
a writer emits exactly one shape, and the two shapes are distinguished by the presence of a key, not
by a heuristic.

### 3.2 The regression this change would cause if the lens were not fixed in the same increment

`capabilityAuthorPending` — the weaver-target lens that drives AI authoring — declares its gap as:

```
((p.claim.data.claimedAt = null) AND (p.artifact.data.kind = null)) AS missing_authoring
```
(`packages/capability-author/lenses.go:111-117`)

An **operator**-submitted proposal mints no claim (`SubmitCapabilityProposal` writes no `.claim`), so
`claimedAt` is null; the only conjunct keeping `missing_authoring` false is `artifact.data.kind` being
non-null. Under the new body that field is null on every new row, so **every operator-authored
proposal would immediately raise `missing_authoring`**, the `capabilityAuthorDispatch` target would
fire a `capabilityAuthor` Loom episode at it, a model call would be spent, and
`RecordCapabilityProposal` would then attempt `create` on the already-live `.artifact` key
(`make_aspect` is unconditionally `"op": "create"`, `ddls.go:379-382`) — a CreateOnly conflict that
rejects the replyOp batch and leaves the Loom instance parked forever.

The corrected predicate is three conjuncts, and the third is the legacy arm:

```
((p.claim.data.claimedAt = null)
  AND (p.artifact.data.kind = null)
  AND (p.artifact.data.artifacts = null)) AS missing_authoring
```

This is a **mandatory, same-increment** change with its own pinned test (§10). It is recorded here
because it is exactly the class the dossier calls a *torn conjunctive consumer*: the predicate's
correctness depended on a field the data-model change removes, and nothing in the type system says so.

### 3.3 Write path (P2) — three ops, two changed

| Op | Change |
|---|---|
| `RequestCapabilityAuthoring` | **None.** It mints the proposal vertex write-ahead and records the intent; it never touches `.artifact`. |
| `SubmitCapabilityProposal` (operator lane) | Payload `kind`/`content` → **`artifacts: [{kind, content}, …]`**, required non-empty. Per-artifact: `kind` must be in `ENABLED_KINDS`; the bundle-level `validation.state` must be `"valid"` (the caller-supplied verdict, unchanged posture) or the proposal records `invalid` with a named reason. Malformed payload still `fail()`s synchronously — there is no bridge Ack to protect. Still performs **no `kv.Read`** (the `CreateOnly` conditioning on the vertex create is the dedup), so its §2.5 read posture is unchanged: nothing to declare. |
| `RecordCapabilityProposal` (bridge replyOp) | Decodes `result` into an object and reads **`artifacts`** as a list instead of scalar `kind`/`content`. Every failure remains a **verdict, never a `fail()`** — the bridge has already Acked, so a non-list / empty-list / over-cap / unknown-kind bundle records `review.state = "invalid"` with `invalidReason`. Its two reads (`…claim.…target`, `…proposal.request`) and their `# read-posture:` annotations are **unchanged**. |
| `ReviewCapabilityProposal`, `MarkCapabilityProposalApplied` | **None** — proposal-keyed, artifact-blind (§2). |

**Empty means refuse, everywhere.** An `artifacts` list that is absent, not a list, or empty is a
refusal on the operator path and an `invalid` verdict on the bridge path. It is never treated as
"no artifacts to check, therefore fine" — the dossier's fail-open-empty-list class.

### 3.4 Read path (P5) — two lens edits

- **`capabilityProposals`** (nats-kv, bucket `capability-proposals`): add
  `p.artifact.data.artifacts AS artifacts`; **keep** `kind` and `content` for the legacy rows (§3.1).
  Projecting a nested array as one column is the shipped `m.spec.data AS spec` /
  `u.credentialBinding.data AS binding` pattern (§2); on a **nats-kv** adapter the whole row is one
  marshaled JSON document, so there is no column type to declare and no `UNWIND` involved (the engine
  refuses fan-out; this design never asks for a row per artifact).
- **`capabilityAuthorPending`**: the three-conjunct predicate of §3.2, on **both** the
  `missing_authoring` and the `violating` columns (they are the same expression today and must stay
  identical).

`cmd/loupe/review.go`'s `capabilityProposalCols` gains `Artifacts []CapabilityArtifact` beside the
retained `Kind`/`Content`.

### 3.5 Apply path — one Definition, `resolveLensRef` untouched

```go
// Replaces the single-artifact entry point. The per-kind helpers are reused
// verbatim; this merges their one-element slices.
func DefinitionForCapabilityArtifacts(artifacts []CapabilityArtifact, name, version string) (Definition, error)
```

It calls the existing `DefinitionForCapabilityArtifact` once per artifact and appends each result's
populated slice into one `Definition{Name, Version, …}`. `CapabilityApplyPlanForProposal` reads the
bundle through `ArtifactsFromAspectData` and calls it once. **Nothing downstream changes**:
`Installer.Apply` → `buildManifestBatch` mints `lensNanoIDs` and `weaverTargetNanoIDs`, builds
`lensByCanonical` from `def.Lenses`, and `resolveLensRef` binds the target's canonicalName-valued
`lensRef` to the co-proposed lens's in-batch NanoID — the exact code path every hand-authored package
already takes. One `InstallPackage` op; one atomic batch; both artifacts or neither.

**Bundle-level validation** — the one genuinely new check, run at record time *and* re-run at approve
time (the same drift argument that already justifies `freshCapabilityVerdict`):

```go
func ValidateCapabilityBundle(artifacts []CapabilityArtifact, parser CypherParser,
    requesterHeld []HeldPermission, sensitiveAspects SensitiveAspectResolver) (BundleValidationReport, error)
```

1. **Per artifact** — the unchanged `ValidateCapabilityArtifact`, indexed into the report so a
   reviewer sees *which* artifact failed.
2. **Cap, fail-closed, both clauses stated with their bound.** At most **8 artifacts**, and at most
   **256 KiB** of total `content` bytes. The count keeps the review screen legible and the byte budget
   keeps the `.artifact` aspect value an order of magnitude under Contract #3 §3.9.1's `max_payload`
   ceiling *including* the op envelope that carries the same bytes. Exceeding either is a named
   refusal, never a substrate `valueSize` rejection the operator has to decode.
3. **Uniqueness within the bundle** — no two lenses share a `canonicalName`, no two targets share a
   `targetId`, no two grants share `(operationType, scope)`. Two artifacts minting the same
   deterministic entity key is the dossier's *two writers of one deterministic key* class; here the
   second `create` would conflict inside the batch and reject the whole apply with a substrate-level
   message. Refuse it at authoring time with a legible one.
4. **Cross-artifact `lensRef` resolvability** — every `weaverTarget` artifact's `lensRef` must be a
   valid NanoID **or** match a `lens` artifact's `canonicalName` **in this bundle**. This is
   `resolveLensRef`'s own rule, evaluated at record/approve time instead of being discovered as a 409
   at the final apply click — the exact papercut `resolveWeaverTargetLensRefs`' doc comment describes.
   It is a *pre-check* of an existing gate, not a replacement for it: `resolveLensRef` still runs and
   still refuses.

### 3.6 The Loupe half (Stream 3)

- **Studio → one proposal.** `exportBundle` (`web/js/logic/weaverauthor.js:278`) emits one
  `{artifacts:[…], target, rationale, validation}` instead of N self-targeted artifacts;
  `buildApplyTarget` becomes **bundle-level** — one derived `packageName`
  (`weaver-capability-<slug>-<tok>`), replacing the per-kind `applyPackagePrefix` split that exists
  only because the two artifacts had to avoid colliding as separate packages. `weaverAuthorPropose`
  submits **one** `SubmitCapabilityProposal`, so the per-artifact success/failure result array
  collapses to one reply and the torn-bundle outcome (lens proposal lands, target proposal fails)
  ceases to exist.
- **`resolveWeaverTargetLensRefs` keeps its job** for the target-only bundle (a canonicalName naming
  an *already-installed* lens must still be resolved to a NanoID, because `lensByCanonical` only ever
  contains this Definition's own lenses). Its co-authored-bundle early return stops being a dead end
  and becomes correct-by-design: the canonicalName now resolves in-Definition.
- **Review console** renders N artifacts in the detail panel with per-artifact verdicts, and the queue
  row shows a count chip. `prettyContent` is unchanged per artifact (`content` remains a JSON string).
- **"Load into Author"** hydrates the whole bundle back into the Studio (target + lens), which is what
  it always wanted to do.

## 4. Security — what bundling does and does not widen

**It does not widen the authority surface.** Everything a bundle can install was already installable
in two steps: propose a `lens` artifact → approve → apply → propose a `weaverTarget` binding it by
NanoID → approve → apply. The install-side deny-list is bundle-level by construction (one
`.target.packageName` per proposal), and `enforceAuthoredWeaverTargetScope` already takes
`def.WeaverTargets` as a slice and iterates it.

**It improves review.** The two-step path shows a reviewer a target's cypher-free gap spec **without**
the lens whose rows those gaps will read. A bundle puts both in front of them at once.

**The one guard whose soundness must be re-derived under bundling.**
`refuseProtectedLensBinding` (`authored_dispatch_scope.go:373-404`) returns `nil` — admits — for any
`lensRef` that is not a valid NanoID, and its own doc comment justifies that with *"the lensRef of a
single-artifact target is the installed lens's NanoID (resolveLensRef passes only a NanoID through
for a single-artifact Definition)."* **Bundling falsifies that premise**: a co-proposed target's
`lensRef` is a canonicalName, so the guard's early return now fires on exactly the new shape. Is the
result still fail-closed? Yes, by two independent structural facts, both of which must be *pinned*
rather than trusted:

| Case | Outcome | Why |
|---|---|---|
| `lensRef` is a NanoID | Guard runs as today | unchanged branch |
| `lensRef` is a canonicalName matching **no** bundle lens | Apply refuses | `resolveLensRef` errors at `build.go:589`; there is no path from a canonicalName to an *installed* lens, so a protected installed lens is unreachable by name |
| `lensRef` is a canonicalName matching a **bundle** lens | Guard's check is vacuous **but cannot be violated** | `LensArtifactContent` (`capabilitymaterializer.go:117-123`) exposes exactly `{canonicalName, adapter, bucket, table, spec}`; `unknownLensFields` (`:740-766`) default-denies every other key; `lensArtifactDefinition` (`:899-913`) sets no `TargetConfig`. An AI/operator-authored lens **structurally cannot** declare `protected` or `secureColumns` — the two properties the guard exists to detect. |

This is a guarantee that holds by the **shape of a type**, which the dossier says must be made
**mechanism-dependent** in the same design. Two required deliverables, both in the platform fire:

- **Extend the guard to resolve in-bundle canonicalNames** against the bundle's own `LensSpec`s and
  run the protected/secure check over the resolved spec, so the check is *performed* rather than
  argued. Over a `LensArtifactContent`-derived lens it always passes today; it will keep passing only
  as long as the type stays narrow, which is the point.
- **A pinning test** asserting `LensArtifactContent`'s field set and `knownLensFields`' contents, whose
  failure message names this section: *widening the lens artifact to carry a target-config posture
  re-opens the protected-lens binding question.*

**Also re-derived, and unchanged:** the `grant` kind's scope-escalation guard
(`validateGrantArtifact` → `requesterHolds`) is per-artifact and per-`(operationType, scope)`; N grants
in a bundle are N independent subset checks against the **same** requester's held set, so a bundle
cannot compose two individually-permitted grants into one that exceeds the requester's scope (the
predicate is a subset test, and a union of subsets of a set is a subset of that set).

## 5. State-lifetime table (the one new stateful thing)

The only new state is the artifact **list** inside `.artifact`. It is Core-KV state, not in-process
state, so its lifetime is the aspect's — but the boundaries still get written down, because the
neighbouring aspects each have an answer and the new one must too.

| Boundary | The artifact list |
|---|---|
| Created | Exactly once, by `SubmitCapabilityProposal` (operator) or `RecordCapabilityProposal` (bridge replyOp) — `make_aspect` `create`, so a second write conflicts rather than overwriting. |
| Reset | Never. There is no edit-in-place op; a revised bundle is a **new proposal**, exactly as a revised single artifact is today. |
| Carried | Across `ReviewCapabilityProposal` and `MarkCapabilityProposalApplied` unchanged — both write only `.review` and links. |
| Ordered | Author order, preserved verbatim through the aspect, the lens column, the review UI, and `DefinitionForCapabilityArtifacts`. No consumer depends on order for correctness. |
| Replay / redelivery | Collapsed by the Contract #4 requestId tracker before the script re-runs; the `create` conditioning is the backstop. Unchanged. |
| Tombstone | The proposal vertex's tombstone; the body is preserved (Lattice tombstones preserve bodies), and every reader already filters `isDeleted` — `readAspectData` (`capabilityapply.go:262-281`) errors on a deleted aspect. Unchanged. |
| Upgrade (pre-bundle rows) | Read through `ArtifactsFromAspectData`'s legacy branch; never rewritten in place (no migration op, no backfill). |

## 6. Executable censuses

Each ships as the command that derives it plus the expected result, so the build's Phase 0 re-runs it
mechanically instead of trusting this prose.

**A. Every reader of the `.artifact` aspect body / the `kind`+`content` lens columns** — the set the
bundle change must cover. Do **not** exclude `_test.go`: the fixtures are the corpus that proves the
compat branch.

```bash
grep -rn "artifact\.data\.\(kind\|content\)\|\"artifact\"\|ArtifactKind\|cols\.Kind\|cols\.Content\|p\.kind\|p\.content" \
  --include="*.go" --include="*.js" --include="*.md" . | grep -v "^./_bmad-output"
```
*Expected at design time:* the DDL script (2 write sites, 1 read), `capabilityProposals` +
`capabilityAuthorPending` lens specs, `capabilityapply.go`'s `typedStringField(artifact, …)`,
`cmd/loupe/review.go`'s `capabilityProposalCols`, `cmd/loupe/web/js/{logic,views}/review.js`,
`cmd/lattice/capability/capability.go`, `internal/bridge/capability_author*.go`, plus the package's
own test corpus. **Classify each hit** — a lens RETURN alias and a wire-struct tag are declarations;
only a `.data.kind` dereference is a reader.

**B. Every construction site of a `CapabilityAuthorProposal`** — the writers that must emit a list.

```bash
grep -rn "CapabilityAuthorProposal{" --include="*.go" .
```
*Expected:* `internal/bridge/capability_author.go` (the real adapter) and
`internal/bridge/fake_capability_author.go` (the CI double), plus tests. If this returns more than
two non-test sites, the bundle shape has a third writer nobody wrote down.

**C. The pre-bundle proposal population** (the compat branch's removal trigger — and the check that
this design needs no migration at all).

```bash
# against a live stack, per environment:
nats kv ls capability-proposals | wc -l          # rows in the read model
# and, authoritative, over Core KV:
#   count vtx.capabilityproposal.*.artifact keys whose body has no "artifacts"
```
*Expected on any stack that has not run `make install-ai`:* **zero** — `capability-author` is not in
`install-packages` and not in the `up-full` chain (`Makefile:628-646`, `:1173-1192`, `:709-724`), and
no seed or fixture creates a `vtx.capabilityproposal.*` outside tests. The compat branch is therefore
insurance for `install-ai` stacks (the demo box), not a migration.

**D. `weaverTarget` artifacts whose `lensRef` is a canonicalName** — the population the new
cross-artifact check governs, and the pinning input for §4's table.

```bash
grep -rn "LensRef" --include="*.go" internal/pkgmgr cmd/loupe | grep -v "_test.go"
```

## 7. Contract surface

| Contract | Touched? |
|---|---|
| **#1 Addressing** | **Built to, not changed.** `.artifact` stays a 4-segment aspect; no new link relations; no new vertex type. |
| **#2 §2.5 Read posture** | **Built to, not changed.** `SubmitCapabilityProposal` still reads nothing; `RecordCapabilityProposal`'s two reads and both `# read-posture:` annotations are unchanged (the bundle rides in the payload, not in a new key). |
| **#3 §3.9.1 Batch ceilings** | **Built to.** §3.5's two-clause cap keeps the batch far inside 998 mutations and the `.artifact` value an order of magnitude inside `max_payload`. |
| **#6 Capability KV** | Untouched — the `grant` kind's scope check is unchanged and per-artifact. |
| **#8 Package install** | **Built to.** The apply is the unmodified F-004 `InstallPackage`; §8.1's deterministic version-independent keys are what make in-Definition resolution work. |
| **#10 §10.5 externalTask/replyOp** | **Built to.** The replyOp envelope `{externalRef, status, result}` is unchanged; `result` is an adapter-defined opaque string (`internal/bridge/adapter.go:56-61`) whose JSON body gains a list. |

**No frozen-contract edit is staged by this design.**

## 8. Reconciliation with the existing mental model

- ***Didn't the Studio already do co-authoring?*** It emits a two-artifact bundle, yes — and then
  splits it into two proposals against two different package names, the second of which is
  unappliable. The bundle *shape* exists in the browser and is destroyed at the server boundary. This
  design carries it through.
- ***Doesn't this duplicate the package-authoring path?*** No — it **uses** it. The whole apply is
  `buildInstallBatch` over a Definition, which is what a hand-authored package already does. The
  design adds no resolution, no batching, and no install machinery.
- ***Does it introduce new state?*** One list inside one existing aspect (§5). No new vertex type, no
  new link, no new bucket, no in-process registry, no cache.
- ***Doesn't `RequestCapabilityAuthoring` pre-mint one proposal id and hand it to the UI?*** Yes — and
  that is an argument **for** this shape. One request → one proposal id → one queue row → N artifacts.
  An N-proposals-plus-bundle-link design would have to break that property.
- ***Is `upgradeExisting` in scope?*** No. The Studio has always emitted `newPackage`
  (`buildApplyTarget`), and §13.1's defect makes `upgradeExisting` unsafe for the *existing*
  single-artifact path too. This design changes neither.

## 9. Migration / compatibility

There is **no migration**: no backfill op, no rewrite of existing aspects, no version gate on boot.
A pre-bundle proposal keeps its `{kind, content}` body, still projects through the retained legacy
lens columns, still renders in the console, and still applies through
`ArtifactsFromAspectData`'s legacy branch. The `capability-author` package version bumps
(`0.10.0 → 0.11.0`, `package.go:128` + `manifest.yaml:2`); the DDL script and both lens specs change,
so an `install-ai` stack picks them up via an ordinary `UpgradePackage` diff-apply of the package's
**own** full Definition (the normal path — not the §13.1 one-artifact shape).

Refractor re-activates the two edited lenses from their `.spec` CDC event with no restart
(hot-reload), and the Weaver re-registers `capabilityAuthorDispatch` from the target's unchanged spec.

## 10. Test strategy

Every test below is **owned by a named increment** (§12) — no unowned tests.

| Test | Proves | Increment |
|---|---|---|
| `TestValidateCapabilityBundle_*` — per-artifact indexing, cap (count + bytes), duplicate canonicalName/targetId/grant-tuple, unresolvable `lensRef` | §3.5's four rules, each with a negative **and** a positive vector | A |
| `TestDefinitionForCapabilityArtifacts_LensPlusTarget` — asserts the merged Definition and then drives `buildManifestBatch`, asserting the target's emitted `.spec.lensRef` **equals** `pkgmgr.LensID(pkg, canonicalName)` | the cross-reference actually resolves; a mutation test that renames the lens must turn this red | A |
| `TestCapabilityApply_BundleIsAtomic` — one `InstallPackage` op, one batch, both metas present; and an invalid second artifact refuses the whole plan | atomicity in both directions | A |
| `TestCapabilityAuthorPending_OperatorProposalRaisesNoGap` — an operator-submitted bundle projects `missing_authoring = false` | §3.2's regression, pinned | A |
| `TestArtifactsFromAspectData_LegacyShape` — a `{kind, content}` body yields a one-element bundle; an absent/empty/non-list `artifacts` is a refusal, not an empty success | the compat branch and the empty-list polarity | A |
| `TestLensArtifactContent_FieldSetPinned` — `LensArtifactContent`'s fields and `knownLensFields` are exactly the five | §4's structural fail-closed becomes mechanism-dependent | A |
| `TestRefuseProtectedLensBinding_InBundleCanonicalName` — a bundle-resolved canonicalName is checked, not skipped | §4's guard extension | A |
| `TestRecordCapabilityProposal_BundleDecodeFailuresAreVerdicts` — non-list / empty / over-cap / unknown-kind each record `invalid` with a reason and **never** `fail()` | the post-Ack invariant | A |
| goja `exportBundle` / `buildApplyTarget` unit tests; a Loupe handler test asserting **one** `SubmitCapabilityProposal` submission for a two-artifact bundle | §3.6 | B |
| Ephemeral-stack e2e: `install-ai` → Studio-shaped bundle submit → approve → apply → both metas live and the Weaver target converges over the co-installed lens | the whole loop | B |

## 11. Risks & alternatives considered

**Alt 1 — "just compute the lens's NanoID at propose time" (no bundling).** `pkgmgr.LensID` is
exported and deterministic, so the Studio could stamp the target's `lensRef` with the co-authored
lens's future id and keep two independent proposals. This is genuinely cheaper and it is the
alternative the row's own framing points at. **Rejected on atomicity, not on plumbing:** the two
proposals remain independently reviewable and independently appliable, so a reviewer who rejects the
lens and approves the target installs a weaver target bound to a `lensRef` that resolves to nothing —
a silently non-converging target with a success signal on its apply. It also requires the two artifacts
to target the **same** `packageName`, at which point the second `newPackage` apply refuses because the
first already installed that name (`capabilityapply.go:198-201`) — so this alternative additionally
needs `upgradeExisting`, which §13.1 shows is destructive today. It solves the id and leaves the unit
broken.

**Alt 2 — N proposal vertices plus a `bundledWith` link.** Keeps `.artifact` untouched. **Rejected:**
review, apply, and mark-applied are all proposal-keyed (§2), so all three ops would need
bundle-awareness (versus zero under the chosen shape), a reviewer could still split the verdict unless
a new all-or-nothing guard were built, and `RequestCapabilityAuthoring`'s one-id-handed-back-to-the-UI
property would break. More surface, weaker guarantee.

**Alt 3 — per-artifact aspects (`artifact0`, `artifact1`, …).** **Rejected:** an unbounded localName
family is unprojectable by a flat lens (the rule engine refuses fan-out —
`ruleengine/full/visitor.go:146`), so the read model would need a row-per-artifact shape no engine can
express, and every consumer's 1:1 row assumption would break. This is the *check expressibility before
classifying a read-model gap as package work* rule applied before, not after, the shape hardened.

**Alt 4 — rewrite the N consumers instead of adding a platform mechanism.** The mandatory demand-side
alternative. The consumer census is genuinely small (two authoring lanes), but "rewriting" them means
teaching each one to sequence two proposals with a computed id — which is Alt 1 with more places to get
it wrong. It does not clear the bar because the missing thing is a *guarantee*, not a convenience.

**Risk — a bundle makes a bad artifact easier to slip past review by burying it.** Real, and the
mitigation is presentational: the review detail renders each artifact separately with its own verdict
chip, and the queue row shows the count. The `grant` kind's scope subset check and the dispatch-scope
guard are per-artifact regardless of presentation.

**Risk — an operator authors a bundle whose two artifacts are individually valid and jointly
nonsense** (a target binding a lens that projects the wrong rows). Unchanged trust model: this is the
same risk a hand-authored package carries, and the Trial panel over born-disabled targets is the
containment.

## 12. Decomposition for the Steward

**Fire A — the platform half (Lattice lane, M).** `internal/pkgmgr`
(`CapabilityArtifact`, `ArtifactsFromAspectData`, `DefinitionForCapabilityArtifacts`,
`ValidateCapabilityBundle`, the `refuseProtectedLensBinding` extension, `CapabilityApplyPlanForProposal`);
`packages/capability-author` version bump (`SubmitCapabilityProposal` + `RecordCapabilityProposal`
scripts, both lens specs, `InputSchema`/`FieldDescription`/examples); `internal/bridge`
(`CapabilityAuthorProposal.Artifacts`, the real adapter's tool schema + per-artifact validator loop,
`FakeCapabilityAuthor`); `cmd/lattice-pkg` + `cmd/lattice/capability` read sites; all Increment-A tests
in §10. **Posture-changing** (it touches a security guard's premise and a weaver-dispatch predicate) —
this one gets the Steward's full review depth, including a security-plane pass.

**Fire B — the console half (Loupe lane, M, `blocked-on:` Fire A).** `exportBundle` /
`buildApplyTarget` / `proposeIntent` / `weaverAuthorPropose` collapse to one bundle-level submission;
review queue + detail render N artifacts; "Load into Author" hydrates the bundle; goja + handler tests
and the e2e. Review depth is the Steward's sizing; the UX is Winston-adjudicated under the 2026-07-02
lane delegation.

The split is by **lane** (separate build locks), not by convenience: Fire A is independently green and
independently shippable, and its consumers are the CLI review/apply path and the AI adapter, both of
which exist in-tree — it is not dead scaffolding waiting on B. B should follow immediately; A's operator
value is not realized until it does.

## 13. Discovered while grounding — filed separately, not fixed here

### 13.1 `upgradeExisting` capability apply tombstones the rest of the package (★★★, new row)

Stated in full in the *For Andrew* block. Root cause: `CapabilityApplyPlanForProposal` materializes a
Definition from the **proposal's artifacts alone** and hands it to `Installer.Apply`, whose
installed-package branch is a **diff** against the package's whole `declaredKeys`. For a package whose
lineage is that proposal path alone the diff is correct; for a human-authored multi-entity package it
tombstones everything the proposal does not re-declare. Not a fork and not this design's scope — but
it is the reason `upgradeExisting` stays out of §12, and any fix is its own designer pass (the shape is
not obvious: the installed package's *source* Definition is not recoverable from KV, only its key set,
so "merge the old manifest in" is not available and an additive-only apply mode is the likely answer).

### 13.2 `refuseProtectedLensBinding`'s doc comment will be stale the moment Fire A lands

Its justification quotes the single-artifact premise verbatim. Fire A rewrites the comment along with
the guard; recorded here so the rewrite is a scope line item and not a review finding.

## 14. Adversarial pass (this design's own gate)

*(To be recorded before this design is build-ready — see the Status banner. Findings and their
dispositions land in this section; a finding that reshapes an increment rewrites §12 in place rather
than appending a banner over it.)*
