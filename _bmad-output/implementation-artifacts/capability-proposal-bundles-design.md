# Co-proposed capability bundles — one proposal, a lens + the target that binds it, one atomic apply — design

**Status: 📐 awaiting-Andrew (ratification).** No frozen-contract change. It goes to Andrew rather than
Winston-adjudication for one reason: §3.2's kind allow-list exists to keep an **unratified sibling
design**'s admission rule true (`package-authority-minting-provenance-design.md` R1, also
📐 awaiting-Andrew), so the two are coupled and whichever ratifies second inherits the constraint —
an ordering only Andrew sets. The §14 adversarial pass **has been run** (three independent lenses,
four blocking findings) and is folded into the body below, not appended to it.
**Component:** cross-cutting — `packages/capability-author` (DDL + 2 lenses) · `internal/pkgmgr`
(materializer + apply plan) · Loupe (review console, then the Weaver Target Studio) · `internal/bridge`
(sequenced last).
**Backlog row:** Lattice lane → *AI-native* → *[capability-author] One authoring request cannot
co-propose a NEW lens + the target that binds it* (★★, M → **L** after the pass; see §12).
**Builds on (all shipped):** [ai-authored-capabilities-design.md](ai-authored-capabilities-design.md)
§3.1/§3.5 · [weaver-target-studio-design.md](weaver-target-studio-design.md) §6.4 ·
[natural-language-weaver-targets-design.md](natural-language-weaver-targets-design.md) §1.
**Author:** Winston (Designer fire, 2026-08-21).

---

## For Andrew (ratify in one look)

**What it does, in two lines.** A capability proposal today carries exactly **one** artifact, so "a new
lens + the weaver target that binds it" cannot be proposed, reviewed, or applied as one thing. This
makes the **proposal the bundle**: one `vtx.capabilityproposal.<id>` carries an ordered artifact list —
restricted to **at most one `lens` and at most one `weaverTarget`** — and its apply materializes them
into **one** `pkgmgr.Definition` submitted as **one** F-004 `InstallPackage` batch, at which point the
platform's existing in-Definition `canonicalName → NanoID` resolution (`build.go:583-585`) binds the
target to its co-proposed lens with **no new resolution machinery at all**.

**Three decisions I made that you should see.**

1. **The bundle is an allow-list of two kinds, not a general N-kind bundle.** The adversarial pass found
   that a general bundle composes two new escalations that the one-artifact path structurally cannot:
   `{vertexTypeDDL, grant}` satisfies the sibling minting design's **R1** ("this batch creates a DDL
   declaring T → admit"), so **R3** — "the applying operator already holds T" — never runs; and
   `{vertexTypeDDL, weaverTarget}` collapses "a new privileged op" and "the thing that fires it forever
   under the Weaver's authority" into one approve click. Restricting the bundle to `{lens?,
   weaverTarget?}` delivers 100% of the filed demand and makes both compositions structurally
   impossible. Widening it later is a design decision with a named re-derivation (§3.2), not a config
   change.
2. **The AI-authoring half is a real, separately-sequenced fire, and it is blocked on a security row —
   not on its size.** The bridge adapter is architecturally single-kind: its system prompt says *"You
   do not write lenses"* (`capability_author_prompt.go:30-33`), its tool schema pins
   `kind: enum[weaverTarget]` with a closed content object (`:122-186`), and `assembleTargetContent`
   resolves `lensRef` **only** against the installed catalog (`capability_author.go:789-796`), so a
   co-authored lens would record `invalid`. Teaching a model to author *lenses* is the fire, and §4.2's
   filed ★★★ row is why it must not ship first: the authored-**lens** admission surface is exactly the
   surface with three open holes.
3. **Two ★★★ defects found while grounding, both filed as their own rows, neither fixed here.** §4.2
   (the authored-artifact admission surface: an authored lens can target `capability-kv` and forge
   `cap.roles.<actor>`; no sensitive-aspect gate; the dispatch-protection set misses every
   bootstrap-seeded op) and §13.1 (an `upgradeExisting` capability apply tombstones every package key
   the proposal omits). Both are **pre-existing and single-artifact reachable** — this design neither
   introduces nor worsens either, and §4 no longer claims otherwise.

**No frozen-contract edit is staged.** Everything touched is `capability-author` package data
(version-bumped twice, §9), `internal/pkgmgr`/`internal/bridge` internals, and Loupe. The replyOp
envelope (Contract #10 §10.5) is unchanged; Contract #3 §3.9.1's ceilings are respected by a cap
enforced **inside the op script** (§3.5).

**The row's `no-pattern:` prescription was half wrong, and correcting it shrank the design.** The row
asked for *"multi-artifact proposal **+ cross-artifact id resolution**"*. Cross-artifact resolution
already ships twice: `resolveLensRef`'s in-Definition `lensByCanonical` branch (`build.go:583-585`) and
the exported deterministic `pkgmgr.LensID(packageName, canonicalName)` (`installer.go:457-465`), since
package entity keys are version-independent and deterministic in `(package, kind, canonicalName)`
(Contract #8 §8.1). The genuinely missing primitive is **one reviewable, appliable unit**. §11 prices
"just compute the id" and rejects it on atomicity, not plumbing.

---

## 1. Problem & intent

### 1.1 What the row said, and what grounding corrected

> `RecordCapabilityProposal` records one `{kind,content}` per request and single-artifact apply resolves
> `lensRef` only by NanoID or same-Definition name — so a new-lens-plus-target intent can't produce both
> atomically. NL v1 binds an existing lens instead.

Both clauses are factually correct; the framing is not.

| Row's premise | Ground truth | Citation |
|---|---|---|
| An AI-path (`RecordCapabilityProposal`) limitation | The **operator** path has the identical gap, live and shipped: `SubmitCapabilityProposal` also records one `{kind, content}`, and the Studio's Propose loops it once per artifact | `packages/capability-author/ddls.go:504-600`; `cmd/loupe/weaverauthor.go:456-491` |
| The missing primitive is cross-artifact id resolution | In-Definition `canonicalName → NanoID` resolution ships and every hand-authored package that declares a lens and a target over it exercises it | `internal/pkgmgr/build.go:227-233, 579-590` |
| …and a co-proposed lens's id is unknowable before apply | Package entity NanoIDs are deterministic and version-independent; the derivation is *exported* as `pkgmgr.LensID` | `internal/pkgmgr/installer.go:305-307, 443-465` |

The real missing primitive is **atomicity of the unit**: nothing in the system means *"these artifacts
are one capability, reviewed together, installed together or not at all."*

### 1.2 The intent, and what is in scope for which fire

- **In scope now (Fires A + B):** an operator authors a violation lens and the weaver target that
  converges on it in the Studio, hits Propose **once**, reviews **one** queue row, approves **once**,
  applies **once** — both land in one Processor commit or neither does. This introduces **no new
  authoring capability**: the Studio can already propose a `lens` artifact today; only the *unit*
  changes.
- **In scope, sequenced (Fire C):** the AI authoring path emits the same bundle, so an NL intent
  needing a new lens stops being *"sketched in the rationale, finished by the operator by hand"*
  (natural-language-weaver-targets §1). Fire C is `blocked-on:` §4.2's row, because it is the fire that
  first lets a **model** author a lens.

## 2. What already ships (the grounding ledger)

Every row cites the code that **does** the thing, never a comment that describes it.

| Mechanism | Where | Status |
|---|---|---|
| `Definition` is a general multi-entity container | `internal/pkgmgr/definition.go:154+` | **Reused unchanged.** Cardinality-of-1 is a decision inside `DefinitionForCapabilityArtifact`'s per-kind helpers, not a limit of the type. |
| One atomic batch spanning every entity kind, committed by one `InstallPackage` op | `build.go:46-52`, `buildInstallBatch` `:58-436` | **Reused unchanged.** |
| In-Definition `canonicalName → in-batch lens NanoID`, consulted before the NanoID fallthrough | `build.go:227-233`, `resolveLensRef` `:579-590` | **Reused unchanged — the whole cross-reference answer.** |
| Deterministic version-independent entity NanoIDs; exported `LensID`/`RoleID`/`DDLID` | `installer.go:293-320, 447-470` | Grounds §11 Alt 1. |
| `validateAll` over a Definition: package name, lens buckets/adapters/read-path, weaver targets (TargetID shape + uniqueness), canonicalName uniqueness across DDLs ∪ Lenses ∪ OpMetas, … — and it runs at apply via `preflight` | `definition.go:30-56, 99-127`; `orchestrationguard.go:81-96`; `upgrade.go:205-223` | **Reused as the bundle check** (§3.5). |
| `ReviewCapabilityProposal` / `MarkCapabilityProposalApplied` are **proposal-keyed**, never artifact-keyed | `packages/capability-author/ddls.go:750-930` — verified to contain no `artifact`/`kind`/`content` reference | **Unchanged.** The proposal is already the review unit. |
| `.target` (`{mode, packageName, baseVersion, newVersion}`) is already **one aspect per proposal** | `ddls.go:729-733` | The *aspect* is bundle-level. The **wire shape is not** — `weaverAuthorProposeArtifact.Target` is per artifact (`cmd/loupe/weaverauthor.go:223`, `:467`), fed by a per-kind `buildApplyTarget` (`web/js/logic/weaverauthor.js:237, 280-300`). §3.6 collapses it. |
| Per-artifact deterministic validation, shared by record-time and apply-time | `ValidateCapabilityArtifact` `capabilitymaterializer.go:310-450` | **Reused per artifact.** |
| Apply-time dispatch containment for an authored target | `authored_dispatch_scope.go:269-404`, called at `capabilityapply.go:223` over `def.WeaverTargets` (already a slice) | **Reused — but §4.2 records that its protected set is incomplete.** |
| A nats-kv lens may project a nested array as one column | `ruleengine/full/values.go:23-98`, `executor.go:996-1005`; **array** precedents `packages/capability-author/lenses.go:186` (`permittedCommands`), `:190` (`examples`); object precedent `:185` (`m.spec.data AS spec`) | **Reused for the new `artifacts` column.** |
| `x = null` is true **iff** the value is Go nil; an empty `[]` is non-nil; an absent aspect and a present-aspect-absent-field both resolve to nil identically | `ruleengine/full/values.go:73-80, 84-102, 143-146` | Grounds §3.2's predicate and §3.3's failed-arm rule. |
| n-ary `AND`, LF-tolerant | `Cypher.g4:289, 687-695`; `visitor.go:482-491` | The three-conjunct predicate parses. |
| Atomic-batch ceilings: 998 business mutations; `max_payload` (1 MiB default) per value, minus header/provenance headroom | Contract #3 §3.9.1 | Bounds §3.5's cap. |

**Verified buildable:** a Definition containing **only** a `Lenses` entry and a `WeaverTargets` entry —
no DDLs, permissions, roles, or manifest fields — builds and installs. `buildInstallBatch` iterates each
slice independently and always emits the package vertex + manifest; `validateLensAdapters` requires only
`Bucket` for nats-kv (`bucketguard.go:71-113`); `validateLensReadPath` is a no-op for a posture-free
nats-kv lens (`:145`); `lensSpecLabels`/`checkLensLabelCap` no-op without a `*` sigil
(`lenslabelcap.go:114-117, 264-267`); `preflight` requires only Name/Version/AdminActor plus
`validateAll` (`upgrade.go:205-223`); and the lens's target bucket need not pre-exist —
`checkCoreBucketExists` checks only core-kv (`installer.go:772`) and the nats-kv adapter auto-creates.

## 3. The shape

### 3.1 The bundle IS the proposal

The unit of *review* and the unit of *apply* must be the same object, or a reviewer can approve the
target and reject the lens it binds. Both are already **proposal**-keyed (§2). So the bundle is the
proposal, and the change is confined to the one aspect that is per-artifact.

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

The aspect key stays 4-segment (Contract #1 §1). The list lives **inside one aspect body**, never as
`artifact0`/`artifact1` siblings — an unbounded localName family is unprojectable by a flat lens (the
rule engine refuses fan-out, `ruleengine/full/visitor.go:146`), so the read model would need a
row-per-artifact shape no engine can express.

**Ordering.** Author order, preserved end to end. Nothing in the apply depends on it (`resolveLensRef`
runs in memory over the whole Definition before any mutation is emitted); the review UI renders in it
and the validation report indexes by it.

**Compatibility.** `.artifact.data.kind` / `.content` are never written again, and are **kept in the
lens projection** so a proposal recorded by a pre-upgrade binary still renders and still applies. One
exported helper does the shape-tolerant read for every Go consumer:

```go
// pkgmgr.ArtifactsFromAspectData returns the bundle an .artifact aspect body
// carries, accepting both the current {artifacts:[…]} shape and the pre-bundle
// {kind, content} single-artifact shape a proposal recorded before this version
// still holds. An absent, non-list, or empty `artifacts` with no legacy `kind`
// is an error, never an empty success. REMOVAL TRIGGER: delete the legacy
// branch once no deployment holds a pre-bundle proposal — the §6.C census.
func ArtifactsFromAspectData(data map[string]any) ([]CapabilityArtifact, error)
```

This is a genuine two-shape read, not a torn row: a writer emits exactly one shape, and the shapes are
distinguished by the presence of a key rather than by a heuristic.

**No lint gate is proposed for the dual shape, and that is a decision, not an omission.** The house rule
is that a design establishing a convention ships its gate in the same fire. Here the two shapes are
*both valid by design* — a hypothetical future writer emitting the legacy scalar produces a proposal
that applies correctly as a one-artifact bundle. A gate would forbid a shape the reader deliberately
accepts. What *is* gated is the property §4.1 depends on: the `LensArtifactContent` field-set pinning
test (§10).

### 3.2 The kind allow-list, and the predicate regression it does not excuse

**A bundle contains at most one `lens` and at most one `weaverTarget`, and nothing else.** Every other
kind (`grant`, `loomPattern`, `vertexTypeDDL`, `opMeta`) remains single-artifact, exactly as today. The
allow-list is default-deny: an unlisted composition is refused with a named reason, at both the operator
op and the bridge replyOp.

Why an allow-list rather than a general bundle — each reason is a composition the adversarial pass
demonstrated, and each is *structurally* impossible under the allow-list rather than guarded against:

| Excluded composition | What it composes that the one-artifact path cannot |
|---|---|
| `{vertexTypeDDL, grant}` | The sibling minting design's **R1** admits a permission mint when *"this batch creates a DDL declaring T"*; a `grant` artifact's Definition provably has **no DDLs** today (`grantArtifactDefinition`, `capabilitymaterializer.go:854-865`), which is exactly why **R3** (the applier already holds T) is the binding rule. A merged Definition falsifies that premise. |
| `{vertexTypeDDL, weaverTarget}` | A new privileged operation **and** the autonomous Weaver dispatcher that fires it forever, in one approve click. `loadProtectedDispatchSets` reads the **live** catalog before the batch applies (`authored_dispatch_scope.go:59-105`), so an op declared by the same bundle is definitionally absent from the protected set. |
| `{grant, opMeta}` | `build.go:412-416` mints `lnk.permission.<id>.forOperation.meta.<opMetaID>` **only when the same Definition declares the op-meta** — a catalog-visible grant the two-step path structurally cannot produce (`build.go:404-411`: visibility *"derives from the grant topology itself"*). |

**Widening trigger.** Adding a kind to the allow-list requires re-running the in-Definition
cross-reference enumeration of §4.1 for that kind's pair, and re-deriving the minting design's R1/R3
interaction. It is a design decision with a named analysis, never a constant edit.

**The predicate regression.** `capabilityAuthorPending` — the weaver-target lens that drives AI
authoring — declares its gap as:

```
((p.claim.data.claimedAt = null) AND (p.artifact.data.kind = null)) AS missing_authoring
```
(`packages/capability-author/lenses.go:111-117`)

An **operator**-submitted proposal mints no claim, so `claimedAt` is null; the only conjunct keeping
`missing_authoring` false is `artifact.data.kind` being non-null. Under the new body that field is null,
so every operator-authored proposal would raise the gap, `capabilityAuthorDispatch` would fire a
`capabilityAuthor` Loom episode at it, a model call would be spent, and `RecordCapabilityProposal` would
then `create` an already-live `.artifact` key (`make_aspect` is unconditionally `"op": "create"`,
`ddls.go:379-382`) — a CreateOnly conflict that rejects the replyOp batch and parks the Loom instance
forever. The corrected predicate is three conjuncts, on **both** `missing_authoring` and `violating`
(they are the same expression and must stay identical):

```
((p.claim.data.claimedAt = null)
  AND (p.artifact.data.kind = null)
  AND (p.artifact.data.artifacts = null)) AS missing_authoring
```

Verified: n-ary `AND` parses across newlines, and `= null` is true iff the value is Go nil, which an
absent aspect and a present-aspect-absent-field both are — while an empty `[]` is **not** (§2). §9 states
why the predicate must ship one version *ahead* of the writers.

### 3.3 Write path (P2)

| Op | Change |
|---|---|
| `RequestCapabilityAuthoring` | **None.** Mints the proposal vertex write-ahead; never touches `.artifact`. |
| `SubmitCapabilityProposal` (operator lane) | Payload `kind`/`content` → **`artifacts: [{kind, content}, …]`**, required non-empty. Per artifact: an object with a non-empty `kind` in `ENABLED_KINDS` and a non-empty `content` — a malformed **element** refuses as loudly as a malformed list. Plus the §3.2 allow-list and the §3.5 cap, both enforced **here, in the script**. Malformed payload still `fail()`s synchronously. Still performs **no `kv.Read`** — §2.5 read posture unchanged, nothing to declare. |
| `RecordCapabilityProposal` (bridge replyOp) | Decodes `result`'s `artifacts` as a list. Every failure remains a **verdict, never a `fail()`** — the bridge has already Acked. **The `status == "failed"` arm must write `.artifact = {"artifacts": []}`** — an explicit empty list, never an omitted key or `None`: an empty `[]` is non-nil so `= null` is false and the dispatch gap stays closed, whereas nil would flip `missing_authoring` true and re-trigger the authoring loop on every failed reply. Its two reads and their `# read-posture:` annotations are unchanged. |
| `ReviewCapabilityProposal`, `MarkCapabilityProposalApplied` | **None** — verified artifact-blind (§2). |

**Empty means refuse, everywhere — and the aspect body is specified, not just the verdict.** An
`artifacts` list that is absent, not a list, or empty records `invalid`; an artifact element that is not
an object or lacks `kind`/`content` records `invalid`; and the one place an empty list is legitimately
*written* (the failed-reply arm) writes `[]` explicitly for the reason above.

### 3.4 Read path (P5) — two lens edits, and the consumers that read them

- **`capabilityProposals`** (nats-kv, `capability-proposals`): add `p.artifact.data.artifacts AS
  artifacts`; **keep** `kind` and `content` for legacy rows. On nats-kv the whole row is one marshaled
  JSON document (`adapter/natskv.go:183-232`) — no column type to declare, no per-column handling, no
  adapter-side size cap.
- **`capabilityAuthorPending`**: the three-conjunct predicate of §3.2, on both columns.

**The consumers, all of which are Fire A scope** (the adversarial pass found the first two independently
and both are silent breakages, not cosmetic):

| Consumer | Today | Under the bundle, unchanged |
|---|---|---|
| `cmd/loupe/review.go:611` — `if cols.Kind == "" { 409 "no recorded artifact yet" }` | gates approve | **Every bundle is unapprovable, forever.** |
| `cmd/loupe/review.go:551-568` `freshCapabilityVerdict` → `ValidateCapabilityArtifact(cols.Kind, cols.Content, …)` | computes the fresh verdict the op *trusts* | validates `("", "")` |
| `cmd/lattice/capability/capability.go:229-260` `freshApprovalVerdict` (same shape, called at `:164`) | ditto, CLI | `EnabledArtifactKinds[""]` → false → approve records `invalid`, permanently |
| both of the above: `if Kind == "grant"` loads `requesterHeld`; `if Kind == "opMeta"` loads the sensitive resolver | kind-conditional | must become **"if ANY artifact is …"**, or a `{grant}`-carrying bundle re-validates with `held == nil` → `requesterHolds` false → permanently unapprovable (fail-closed but silently wrong) |
| `web/js/logic/review.js:24` `if (!r.kind) return "authoring"` | queue state chip | every bundle renders "reasoning in flight" and is non-actionable. **Fix mirrors the sibling in the same file** — `augurDisplayState` (`:158-165`) keys on `reviewState` being null, not on an artifact field |
| `web/js/views/review.js:255, 260, 265, 313` | detail panel, glyph, `prettyContent`, "Load into Author" gate | renders "no artifact recorded yet" + `(empty)`; the Load gate `p.kind === "weaverTarget"` never fires |
| `cmd/lattice/capability/capability.go:110` | `KIND` column in `list` | blank for every bundle; needs a kind-summary/count |
| `internal/pkgmgr/capabilityapply.go:157-164` `typedStringField(artifact, "kind"/"content")` | apply plan | reads the bundle via `ArtifactsFromAspectData` |
| `cmd/bridge/main.go:257-266` `capabilityArtifactVerdict` | the composition root injecting the per-artifact validator into the adapter | signature becomes a bundle verdict |

`cmd/lattice-pkg apply-proposal` (`main.go:552-560`) needs no change of its own — it routes entirely
through `CapabilityApplyPlanForProposal`.

### 3.5 Apply path — one Definition, `resolveLensRef` untouched; validation is `validateAll`

```go
// Replaces the single-artifact entry point. The per-kind helpers are reused
// verbatim; this merges their one-element slices after the allow-list check.
func DefinitionForCapabilityArtifacts(artifacts []CapabilityArtifact, name, version string) (Definition, error)
```

`CapabilityApplyPlanForProposal` reads the bundle through `ArtifactsFromAspectData` and calls it once.
**Nothing downstream changes**: `Installer.Apply` → `buildManifestBatch` mints the lens and target
NanoIDs, builds `lensByCanonical` from `def.Lenses`, and `resolveLensRef` binds the target's
canonicalName-valued `lensRef` to the co-proposed lens's in-batch NanoID — the exact path every
hand-authored package takes. One `InstallPackage` op; one atomic batch; both artifacts or neither.

**Bundle validation** — run at record time *and* re-run at approve time (the drift argument that already
justifies `freshCapabilityVerdict`):

```go
func ValidateCapabilityBundle(artifacts []CapabilityArtifact, parser CypherParser,
    requesterHeld []HeldPermission, sensitiveAspects SensitiveAspectResolver) (BundleValidationReport, error)
```

1. **The allow-list** (§3.2), first and fail-closed.
2. **Per artifact** — the unchanged `ValidateCapabilityArtifact`, indexed into the report so a reviewer
   sees *which* artifact failed.
3. **`DefinitionForCapabilityArtifacts(...).validateAll()` on the MERGED Definition.** This is the whole
   cross-artifact structural check, and it is the *exact* set `preflight` re-applies at apply time
   (`upgrade.go:205-223`), so a bundle that validates here cannot 409 there. Deriving a rule list by
   hand instead would have been wrong twice over: `validateCanonicalNameUniqueness` shares **one**
   namespace across DDLs ∪ Lenses ∪ OpMetas (`definition.go:99-127`), and the harm of colliding there
   is not a create conflict — `DDLID(pkg,"X")` and `LensID(pkg,"X")` are *different keys* — but the
   Processor's DDL cache serving one meta-vertex per canonicalName and dropping the other.
4. **Cross-artifact `lensRef` resolvability** — call the shipped `resolveLensRef` with a
   `lensByCanonical` built exactly as `build.go:230-233` builds it. Using the same function means the
   same **exact** (unfolded) string comparison: a canonicalName differing by whitespace or case is a
   loud refusal at authoring time rather than a 409 at the apply click. It is a *pre-check* of an
   existing gate, not a replacement — `resolveLensRef` still runs at build and still refuses.
   (`build.go:580-582` admits an empty `lensRef`; `validateWeaverTargetArtifact:607-609` catches empty
   at record time, client-side. Unchanged, noted.)
5. **The cap, enforced in the Starlark op, not only here.** `ValidateCapabilityBundle` is a *caller-side*
   Go function (`capabilitymaterializer.go:213-223` says so of its sibling), so a cap enforced only here
   is bypassable: an over-large bundle would record fine, be approved, and then detonate terminally at
   apply — leaving a proposal permanently `approved` and unappliable, since `review` is
   single-transition (`ddls.go:779-781`). The count and byte checks are pure arithmetic over the
   payload, so they go **in the script**: **at most 2 artifacts** (the allow-list's own bound) and **at
   most 128 KiB of total `content` bytes**. The byte figure is derived, not asserted: each `content` is
   a JSON *string* re-escaped inside the `.artifact` body (`JSON.stringify` at
   `web/js/logic/weaverauthor.js:285`), so worst-case escaping roughly doubles it — 128 KiB of content
   is ≈256 KiB in the aspect value against a 1 MiB `max_payload` minus header/provenance headroom, i.e.
   a ~4× margin, and the same bytes ride the op payload as one NATS message under the same ceiling.

### 3.6 The Loupe halves

**Fire A (read side).** Every row of §3.4's consumer table: `capabilityProposalCols.Artifacts`, the
approve gate, `freshCapabilityVerdict`'s any-artifact resolver loading, `proposalDisplayState` (mirroring
`augurDisplayState`), `artifactSection` rendering N artifacts with per-artifact verdicts, the queue's
count chip, the "Load into Author" gate, and the CLI's `KIND` column. This is what makes Fire A
non-regressive.

**Fire B (write side).** `exportBundle` (`web/js/logic/weaverauthor.js:278`) emits one
`{artifacts:[…], target, rationale, validation}` instead of N self-targeted artifacts;
`buildApplyTarget` becomes **bundle-level** — one derived `packageName` (`weaver-capability-<slug>-<tok>`),
replacing the per-kind `applyPackagePrefix` split (`:237`) that exists **only** because the two artifacts
had to avoid colliding as separate `newPackage` installs. The new prefix must preserve that split's
by-construction property — no `PlatformProtectedPackage` name shares it — pinned by a test, not by a
comment. `weaverAuthorPropose` submits **one** `SubmitCapabilityProposal`, so the per-artifact
success/failure result array collapses and the torn-bundle outcome ceases to exist.
`resolveWeaverTargetLensRefs` keeps its job for the target-only bundle (a canonicalName naming an
*already-installed* lens must still resolve to a NanoID, because `lensByCanonical` only ever holds this
Definition's own lenses); its co-authored-bundle early return stops being a dead end and becomes
correct-by-design. The Studio's **Check** step must also surface a lensRef/canonicalName divergence:
`buildLensContent` defaults the lens's `canonicalName` to `draft.lensRef` (`:181`), so they agree unless
the operator edits one — and an unresolvable pair should be caught before Propose, not at apply.

**Fire C (the AI side).** A model-facing `lens` content shape in the strict tool schema — which, in this
file's idiom, means a discriminated union expressed as every kind's fields present-but-empty on every
artifact plus an assembler per kind (`capability_author_prompt.go:122-186`'s own doc comment explains
why: *"a closed schema cannot describe an object whose keys the model chooses"*); a lens assembler; an
**in-bundle branch** in `assembleTargetContent`'s `lensRef` resolution, which today consults only the
installed-catalog `lensIndex` (`capability_author.go:789-796`, `capability_author_prompt.go:306-309`) and
would otherwise record a co-authored bundle `invalid` with *"lens %q is not in the catalog"*; a rewritten
system prompt (today: *"You do not write lenses"*); and a bundle-carrying repair loop (`assess`/`file`
and `correctionPrompt` are single-draft, `:386`, `:723-760`).

## 4. Security

### 4.1 What bundling does and does not compose

**Under the §3.2 allow-list, the only in-Definition cross-reference a bundle can activate is
lens↔target.** That is not an assertion — it is an enumeration of every cross-reference
`buildInstallBatch` performs, checked against the two admissible slices:

| Cross-reference | Needs | Reachable in a `{lens?, weaverTarget?}` bundle? |
|---|---|---|
| `resolveLensRef` via `lensByCanonical` (`build.go:227-251`) | a Lens + a WeaverTarget | **Yes — this is the design.** |
| `lnk.permission.<id>.forOperation.meta.<opMetaID>` (`build.go:412-416`) | a Permission + an OpMeta | No — neither kind is admissible |
| `effectsByOp` (`build.go:268-276`) | a DDL declaring `Effects` | No — no DDL kind admissible, and `VertexTypeDDLArtifactContent` exposes no `effects` anyway |
| `subtypeAbstractIDs` / taxonomy `SubtypeOfRef` | a DDL declaring `SubtypeOfRef` | No — same, and the artifact type exposes no `subtypeOfRef` |
| `resolveGrants` / `resolvePaneRoles` via `RoleIDs` | a Role or a Pane | No — neither is an enabled artifact kind at all |

The last three are inert *by the narrowness of the artifact content types*, which is an accident of
shape rather than a mechanism — so the §10 field-set pinning test covers
`VertexTypeDDLArtifactContent` alongside `LensArtifactContent`.

**Grant composition is unaffected** and was verified: `HeldPermission.covers` / `requesterHolds`
(`capabilitymaterializer.go:257-280`) are a pure per-`(operationType, scope)` subset test against one
fixed held set with no accumulated state, so N grants would be N independent subset checks — and under
the allow-list a bundle carries none. Two pre-existing caveats recorded, not introduced: `GrantsTo` is
never checked against the requester (a self-scoped operator may confer their own op on a role they do
not hold), and the whole verdict is caller-supplied rather than server-enforced
(`SubmitCapabilityProposal` copies `p.validation.state` through, `ddls.go:549-568`;
`CapabilityApplyPlanForProposal` never re-runs the materializer).

**Review improves for the shape this design ships, and the design does not claim more.** The two-step
path shows a reviewer a target's gap spec **without** the lens whose rows those gaps read; a bundle puts
both in front of them. The allow-list is what stops that argument from being extended to compositions
where bundling would *degrade* review (§3.2's table).

### 4.2 What the adversarial pass found that this design does NOT fix — filed as one ★★★ row

The pass attacked §4's earlier claim that the shipped guards are the containment. Three holes, all
**pre-existing and reachable from a single artifact today**, all in the authored-artifact *admission*
surface rather than in the proposal's cardinality. They share one root cause — **every guard enumerates
its governed set from the wrong source** — so they file as one row:
*[capability-author] Three admission holes let an authored artifact reach the auth plane*, ★★★ / L,
`no-pattern: consumer-derived admission model for authored artifacts`.

1. **An authored lens may target a shared platform projection bucket, and controls its own row key.**
   `validateLensBuckets` refuses only `reservedBucketAliases` and `bootstrap.ReservedBuckets()`
   (`bucketguard.go:45, 52-66`), and `ReservedBuckets()` is *every registry row with
   `LensTarget:false`* (`platform_buckets.go:135-143`) — so `capability-kv` (`:45-51`), `weaver-targets`
   (`:63-72`) and `orchestration-history` (`:74-82`) are all **admissible**. The key is fully
   lens-controlled (`build.go:534-543` defaults `key` to the `key` column; `natskv.go:150-160`
   `fmt.Sprintf("%v", …)`), and no write-side prefix confinement exists. A lens artifact returning
   `'cap.roles.' + <id> AS key` with an array-valued permissions column forges the document the
   Processor's step-3 write authorization reads (`step3_auth_capability.go:115-118`); one targeting
   `weaver-targets` injects violation rows an *already-installed* platform target's gaps act on.
2. **No sensitive-aspect gate on the lens kind, while the `opMeta` kind's fails closed.**
   `validateOpMetaArtifact` runs `sensitiveReadErrors` over declared reads and refuses on a nil resolver
   (`capabilitymaterializer_starlark.go:454-513`), reasoning that *"an AI-authored capability that might
   need PII egress is exactly the case that must route to human authoring."* `validateLensArtifact`
   (`capabilitymaterializer.go:873-891`) has no equivalent: a lens projecting `i.ssn.data.value` into a
   plain readable bucket is admitted. §4.3's structural narrowness makes this strictly worse, because an
   authored lens is *guaranteed* to be plain, unprotected and unencrypted.
3. **`protectedDispatchSets` misses every bootstrap-seeded operation.** It derives the protected set
   **only** from `PlatformProtectedPackage` manifests' `declaredKeys`
   (`authored_dispatch_scope.go:59-96`), but the meta-vertex trio and the package-lifecycle trio are
   seeded by `internal/bootstrap/primordial.go` (`:510, 522, 723-749`) and appear in **no** package
   manifest — verified: no `packages/*/ddls.go` or `permissions.go` declares them. So an authored
   target's `directOp` gap naming `UpdateMetaVertex` is admitted, and the Weaver dispatches it on the
   `system` lane under the primordial operator role. Related: when no protected package is installed the
   loader returns an **empty, admit-everything set** (`:100-105`), fail-open by explicit design whose
   rationale ("no operator role to escalate into") this finding falsifies.

**Consequence for this design's own deliverables.** An earlier draft proposed extending
`refuseProtectedLensBinding` to resolve in-bundle canonicalNames. That is **dropped**: by §4.3 the check
can never fire over a `LensArtifactContent`-derived lens, so it would be a guard that buys the reader's
confidence and detects nothing, while the live channel is #1 above. What remains is the pinning test —
re-motivated in §4.3.

### 4.3 The one structural property this design does depend on, and how it is pinned

`refuseProtectedLensBinding` (`authored_dispatch_scope.go:373-404`) early-returns for any non-NanoID
`lensRef`, justified by *"the lensRef of a single-artifact target is the installed lens's NanoID."*
Bundling falsifies that premise, so the outcome must be re-derived:

| `lensRef` | Outcome | Why |
|---|---|---|
| a NanoID | guard runs as today | unchanged branch |
| a canonicalName matching **no** bundle lens | apply refuses | `resolveLensRef` errors (`build.go:589`); `lensByCanonical` is built from `def.Lenses` alone (`:230-233`), so a canonicalName can never reach an *installed* lens |
| a canonicalName matching a **bundle** lens | check is vacuous, and cannot be violated | verified: `lensSpecBody` derives `protected`/`secureColumns` **only** from explicit `LensSpec` fields and **only** on the `postgres` branch (`build.go:488-490, 510-523`); the nats-kv branch emits `{bucket, key}` plus optional `diffRetraction` (`:534-547`). `postgres` is unreachable for an artifact lens (`validateLensReadPath:162-164` requires an explicit posture `LensArtifactContent` cannot declare) and so is `nats-subject` (`validateLensAdapters:103-109`). An authored lens is **always plain nats-kv**. |

This is a guarantee held by the shape of a type, so it is made mechanism-dependent: **pinning tests on
`LensArtifactContent`'s field set, `knownLensFields`, and `VertexTypeDDLArtifactContent`'s field set**,
whose failure messages name this section — *widening an artifact content type re-opens the admission
questions §4.1 and §4.2 answer.* `refuseProtectedLensBinding`'s doc comment is rewritten in the same
fire (§13.2), including its inaccurate *"left to install to resolve or reject"* clause, which is true of
a canonicalName and false of the empty ref both admit.

## 5. State-lifetime table

The only new state is the artifact **list** inside `.artifact`. It is Core-KV state, but the boundaries
still get written down, because every neighbouring aspect has an answer.

| Boundary | The artifact list |
|---|---|
| Created | Exactly once, by `SubmitCapabilityProposal` or `RecordCapabilityProposal` — `make_aspect` `create`, so a second write conflicts rather than overwriting. |
| Reset | Never. No edit-in-place op exists; a revised bundle is a **new proposal**, as a revised single artifact is today. |
| Carried | Across `ReviewCapabilityProposal` and `MarkCapabilityProposalApplied` unchanged — both write only `.review` and links. |
| Ordered | Author order, preserved through the aspect, the lens column, the review UI, and `DefinitionForCapabilityArtifacts`. No consumer depends on order for correctness. |
| Replay / redelivery | Collapsed by the Contract #4 requestId tracker before the script re-runs; the `create` conditioning is the backstop. |
| Failed reasoning reply | Written as an explicit empty `[]`, never nil (§3.3) — the one case where "empty" is a legitimate stored value, and the reason it must be `[]` and not absent. |
| Tombstone | The proposal vertex's; the body is preserved, and `readAspectData` errors on a deleted aspect (`capabilityapply.go:262-281`). |
| Upgrade (pre-bundle rows) | Read through `ArtifactsFromAspectData`'s legacy branch; never rewritten in place. |

## 6. Executable censuses

Each ships as the command plus the expected result, so Phase 0 re-runs it mechanically. **The earlier
draft's Census A was wrong** — it missed `capabilityapply.go`'s reader, both bridge writers, and the CLI
— which is exactly why a census's own command must be run before it is written down.

**A. Every reader/writer of the artifact shape.** Enumerate by declaration, not filename; tests, JS,
Markdown and YAML included.

```bash
grep -rn -E "artifact\.data|artifact\"|\bcols\.(Kind|Content)\b|\brow\.(Kind|Content)\b|\b(p|r)\.kind\b|\b(p|r)\.content\b|CapabilityAuthorProposal|ValidateCapabilityArtifact|DefinitionForCapabilityArtifact|SubmitCapabilityProposal|RecordCapabilityProposal" \
  --include='*.go' --include='*.js' --include='*.md' --include='*.yaml' . | grep -v '^\./_bmad-output'
```
*Expected:* the §3.4 consumer table's ten rows, plus `packages/capability-author/{ddls,lenses}.go`,
`internal/bridge/{capability_author.go,capability_author_prompt.go,fake_capability_author.go,capability_author_proposal.go}`,
`cmd/bridge/main.go:257-266`, `packages/capability-author/manifest.yaml`'s description block and
`package.go:128`'s `Description` (both narrate "a proposed artifact" — §13.2), and the package's test
corpus. **Classify each hit**: a lens RETURN alias, a wire-struct tag and a doc sentence are
declarations; a `.data.kind` dereference or a payload construction is a reader/writer; and a
dereference that *gates a write* (the two approve verdicts) is a reader whose failure mode is a silent
block, not a blank field.

**B. `CapabilityAuthorProposal` construction sites.**
```bash
grep -rn "CapabilityAuthorProposal{" --include='*.go' .
```
*Expected — verified at design time:* exactly two non-test sites,
`internal/bridge/capability_author.go:472` and `internal/bridge/fake_capability_author.go:113`, plus one
test site. More than two means a third writer nobody wrote down.

**C. The pre-bundle proposal population** (the compat branch's removal trigger).
```bash
nats kv ls capability-proposals | wc -l
```
*Expected on any stack that has not run `make install-ai`:* **zero** — `capability-author` is absent from
`install-packages` (`Makefile:1173-1192`) and from the `up-full` chain (`:709-724`), appearing only under
`install-ai` (`:628-646`), and no seed or fixture creates a `vtx.capabilityproposal.*` outside tests. The
compat branch is insurance for `install-ai` stacks, not a migration.

**D. Structure pins.** `packages/capability-author/structure_pins_test.go` pins
`len(Package.Lenses)==3`, `len(Package.OpMetas)==7` and the canonical names. This design edits two lens
specs in place and changes no declaration count, so it must stay green **unmodified** — a diff to that
file in the build is a signal the scope drifted.

## 7. Contract surface

| Contract | Touched? |
|---|---|
| **#1 Addressing** | Built to. `.artifact` stays a 4-segment aspect; no new link relation or vertex type. |
| **#2 §2.5 Read posture** | Built to. `SubmitCapabilityProposal` still reads nothing (verified `ddls.go:504-602`); `RecordCapabilityProposal`'s two reads and both annotations are unchanged. |
| **#3 §3.9.1 Batch ceilings** | Built to, with the cap **inside the op script** and its byte figure derived from the ceiling and the JSON-escaping factor (§3.5). |
| **#6 Capability KV** | Untouched by this design. §4.2 #1 is a pre-existing channel into it, filed. |
| **#8 Package install** | Built to. The apply is the unmodified F-004 `InstallPackage`; §8.1's deterministic keys are what make in-Definition resolution work. |
| **#10 §10.5 externalTask/replyOp** | Built to. The envelope is unchanged; `result` is an adapter-defined opaque string (`internal/bridge/adapter.go:56-61`) whose JSON body gains a list. |

**No frozen-contract edit is staged.**

## 8. Reconciliation with the existing mental model

- ***Didn't the Studio already do co-authoring?*** It emits a two-artifact bundle in the browser and
  splits it into two proposals against two different package names at the server boundary, the second of
  which is unappliable by construction — the shipped doc comment calls it *"an accepted, unchanged
  limitation of that path"* (`cmd/loupe/weaverauthor.go:349-359`). This design carries the bundle
  through.
- ***Doesn't this duplicate the package-authoring path?*** No — it **uses** it. The apply is
  `buildInstallBatch` over a Definition, and the bundle check is `validateAll` over the same Definition.
- ***Does it introduce new state?*** One list inside one existing aspect (§5). No new vertex type, link,
  bucket, registry, or cache.
- ***Doesn't `RequestCapabilityAuthoring` pre-mint one proposal id and hand it to the UI?*** Yes — an
  argument **for** this shape. One request → one id → one queue row → N artifacts. An
  N-proposals-plus-bundle-link design would break that property.
- ***Is `upgradeExisting` in scope?*** No. The Studio has always emitted `newPackage`, and §13.1's
  defect makes `upgradeExisting` unsafe for the *existing* single-artifact path too.
- ***Does this widen what an AI may author?*** Not in Fires A/B — the Studio can already propose a lens.
  Fire C is where a **model** first authors one, which is why it is blocked on §4.2.

## 9. Migration / compatibility — and the two-version ordering that closes the activation window

There is **no migration**: no backfill, no rewrite, no boot version gate. A pre-bundle proposal keeps its
body, projects through the retained legacy columns, renders, and applies via the legacy branch.

**The package ships in two versions, and the order is load-bearing.** A DDL script and a lens `.spec`
commit in one atomic Core-KV batch but activate on **different channels**: the Processor **invalidates
the `vtx.meta.*` DDL cache in-commit** (`installer.go:198-200`), so a new script is live immediately,
while the lens predicate reaches the runtime only when Refractor consumes the `.spec` CDC event and
re-activates. Ship them together and there is a window in which a `SubmitCapabilityProposal` writes
`{artifacts:[…]}` while the **old** two-conjunct predicate still evaluates — `missing_authoring` true,
`capabilityAuthorDispatch` fires a Loom episode, and `RecordCapabilityProposal`'s create-only `.artifact`
write conflicts forever. So:

- **`0.11.0` — the lens predicate only.** The three-conjunct form is correct for **both** shapes (the
  legacy `kind` conjunct is retained), so it is a no-op on today's data and safe to land alone.
- **`0.12.0` — the DDL scripts + everything else.** By the time any writer emits a list, the predicate
  that must tolerate it is already active.

Refractor re-activates an edited lens from its `.spec` CDC event with no restart. The Weaver does **not**
re-register `capabilityAuthorDispatch` — an unchanged `.spec` emits no mutation and therefore no CDC
event (`upgrade.go:522-523`) — and needs no re-registration, since the target's `lensRef` is a
version-independent NanoID. No test should pin that non-event.

## 10. Test strategy

Every test is owned by a named increment.

| Test | Proves | Inc |
|---|---|---|
| `TestValidateCapabilityBundle_AllowList` — each excluded composition of §3.2's table refused by name; each admitted one accepted | the allow-list, with a positive vector per negative | A |
| `TestValidateCapabilityBundle_MergedDefinition` — a `{lens "X", …}` bundle colliding on canonicalName is refused **here** and would also be refused by `preflight` | §3.5 point 3's "cannot 409 at apply" claim | A |
| `TestValidateCapabilityBundle_LensRefResolvability` — NanoID, in-bundle canonicalName, unresolvable name, and a **whitespace/case near-miss** each get the stated outcome | §3.5 point 4, incl. the exact-comparison rule | A |
| `TestSubmitCapabilityProposal_CapEnforcedInScript` — 3 artifacts and an over-byte bundle both refused by the **op**, not by the Go caller | §3.5 point 5 (the trust-boundary fix) | A |
| `TestDefinitionForCapabilityArtifacts_LensPlusTarget` — asserts the merged Definition, then drives `buildManifestBatch` and asserts the emitted `.spec.lensRef` **equals** `pkgmgr.LensID(pkg, canonicalName)`; renaming the lens must turn it red | the cross-reference actually resolves (mutation-verified) | A |
| `TestCapabilityApply_BundleIsAtomic` — one `InstallPackage` op, one batch, both metas; an invalid second artifact refuses the whole plan | atomicity in both directions | A |
| `TestCapabilityAuthorPending_OperatorProposalRaisesNoGap` + `..._FailedReplyRaisesNoGap` | §3.2's regression and §3.3's `[]`-not-nil rule, both pinned | A |
| `TestArtifactsFromAspectData_LegacyShape` — legacy body → one-element bundle; absent/empty/non-list/malformed-element → error, never empty success | the compat branch and the empty polarity | A |
| `TestArtifactContentFieldSetsPinned` — `LensArtifactContent`, `knownLensFields`, `VertexTypeDDLArtifactContent` | §4.1/§4.3's structural premises made mechanism-dependent | A |
| `TestApproveBundle_LoupeAndCLI` — a `{lens, weaverTarget}` bundle approves through **both** `reviewCapabilityApprove` and the CLI's `freshApprovalVerdict`; a `{grant}` bundle loads `requesterHeld`; an `{opMeta}` bundle loads the sensitive resolver | §3.4's blocking consumers, driven from the entry point that produces the error (the dossier's rule) | A |
| goja tests: `proposalDisplayState` / `artifactSection` over a bundle row and a legacy row; `exportBundle` / `buildApplyTarget` bundle-level shape; a pin that the derived prefix cannot equal a `PlatformProtectedPackage` name | §3.6 | A (first two) / B (rest) |
| Loupe handler test: a two-artifact bundle produces **one** `SubmitCapabilityProposal` submission | §3.6 Fire B | B |
| Ephemeral-stack e2e: `install-ai` → bundle submit → approve → apply → both metas live and the target converges over the co-installed lens | the whole loop | B |
| Fire C: a fake-backed adapter test asserting a `{lens, weaverTarget}` draft assembles with an **in-bundle** `lensRef` and records `pending`, plus a repair-loop round over a bundle | §3.6 Fire C | C |

## 11. Risks & alternatives considered

**Alt 1 — "just compute the lens's NanoID at propose time" (no bundling).** `pkgmgr.LensID` is exported
and deterministic, so the Studio could stamp the target's `lensRef` with the co-authored lens's future
id. **Rejected on atomicity, not plumbing:** the two proposals stay independently reviewable and
appliable, so rejecting the lens and approving the target installs a target bound to a `lensRef`
resolving to nothing — a silently non-converging target with a success signal on its apply. It also
requires both artifacts to target the **same** `packageName`, at which point the second `newPackage`
apply refuses (`capabilityapply.go:198-201`), so it additionally needs `upgradeExisting`, which §13.1
shows is destructive today. It solves the id and leaves the unit broken.

**Alt 2 — N proposal vertices plus a `bundledWith` link.** Keeps `.artifact` untouched. **Rejected:**
review, apply and mark-applied are all proposal-keyed, so all three ops would need bundle-awareness
(versus zero); a reviewer could still split the verdict unless a new all-or-nothing guard were built;
and `RequestCapabilityAuthoring`'s one-id-to-the-UI property would break.

**Alt 3 — per-artifact aspects (`artifact0`, …).** **Rejected:** an unbounded localName family is
unprojectable by a flat lens (the engine refuses fan-out), so the read model would need a shape no
engine can express — the expressibility check run *before* the shape hardened, not after.

**Alt 4 — rewrite the two consumers instead of adding a platform mechanism.** The mandatory demand-side
alternative; the consumer census really is two authoring lanes. **Rejected because "rewriting" them
means Alt 1 with more places to get it wrong** — the missing thing is a guarantee, not a convenience.

**Alt 5 — a general N-kind bundle.** The obvious generalization, and the pass's findings are why it is
rejected (§3.2). Worth stating plainly: the general case was the draft, and it opened two escalations
the restricted case cannot express. Generality was the more expensive *and* the less safe option.

**Risk — a bundle buries a bad artifact.** Real; mitigated presentationally (per-artifact verdict chips,
a count on the queue row) and structurally (the allow-list caps a bundle at two artifacts, one of each
kind).

**Risk — two individually-valid artifacts that are jointly nonsense** (a target binding a lens that
projects the wrong rows). Unchanged trust model — the same risk a hand-authored package carries; the
Trial panel over born-disabled targets is the containment.

## 12. Decomposition for the Steward

The row was filed **M**; after the pass it is **L** — Fire A grew by the whole §3.4 consumer table and
Fire C is a real fire, not a "validator loop".

**Fire A — the primitive and everything that must not regress with it (Lattice lane + a contained Loupe
read-side touch; L, posture-changing).** `internal/pkgmgr` (`CapabilityArtifact`,
`ArtifactsFromAspectData`, `DefinitionForCapabilityArtifacts`, `ValidateCapabilityBundle`,
`CapabilityApplyPlanForProposal`, the §4.3 doc-comment rewrite); `packages/capability-author` **two
version bumps** per §9 (`0.11.0` predicate, `0.12.0` scripts + the `capabilityProposals` column +
`InputSchema`/`FieldDescription`/examples + the manifest and `Description` prose of §13.2);
`cmd/bridge/main.go:257-266`'s `capabilityArtifactVerdict`; **every row of §3.4's consumer table**,
including both approve verdicts and the review console's read side; the CLI's `KIND` column; all
Increment-A tests in §10.
*Why the Loupe read side cannot wait for Fire B:* without it, `cmd/loupe/review.go:611` returns 409 for
every bundle and `proposalDisplayState` renders every bundle non-actionable — Fire A alone would ship a
console regression, not merely unrealized value. The lane crossing is contained (read-side rendering and
one gate) and mirrors the NL-1/NL-2 dispensation; if the Steward will not cross lanes, A and B ship as
one effort rather than A shipping alone.
This fire touches a security guard's premise and a weaver-dispatch predicate — **full review depth,
including a security-plane pass.**

**Fire B — the Studio write half (Loupe lane, M, `blocked-on:` A).** `exportBundle` /
`buildApplyTarget` / `proposeIntent` / `weaverAuthorPropose` collapse to one bundle-level submission; the
prefix-collision pin; the Check-step lensRef/canonicalName divergence; "Load into Author" hydrates the
bundle; goja + handler tests and the e2e. UX Winston-adjudicated (2026-07-02 lane delegation).

**Fire C — the AI authoring half (Lattice lane, M, `blocked-on:` A **and** the §4.2 row).** §3.6 Fire C's
five items. The block on §4.2 is not sizing: this is the fire that first lets a *model* author a lens,
and the authored-lens admission surface is precisely what §4.2 reports as unguarded.

**Applicable `docs/components/pkgmgr.md` dossier entries** for the Fire A brief: *"a new failure mode is
not shipped until every surface that renders it says the right thing"* (the bundle refusals need their
`errors.Is` status/UX mapping driven from the producing entry point, not asserted against the mapping
function); *"a field validated after normalization must be MATCHED after the same normalization"* (§3.5
point 4 uses `resolveLensRef`'s own exact comparison and refuses the near-miss loudly); *"two writers of
one deterministic key"*; *"a local gate run and CI's gate run do not see the same tree"* (run
`lint-package-version` after committing, with `DIFF_BASE`).

## 13. Discovered while grounding — filed separately, not fixed here

### 13.1 `upgradeExisting` capability apply tombstones the rest of the package (★★★, filed)

`CapabilityApplyPlanForProposal` materializes a Definition from the proposal's artifacts **alone** and
hands it to `Installer.Apply`, whose installed-package branch is a **diff** against the package's whole
`declaredKeys` — *"a key only in the old set → tombstone"* (`upgrade.go:445-513`, tombstone emission
`:532-590`, only retention-class holders exempt). Correct for a package whose entire lineage is that
proposal; destructive on a human-authored multi-entity package — which the deny-list's own doc comment
says is the intended target (`capabilityapply.go:55-64`). An independent census briefed to falsify it
returned **STANDS**: no merge step, no subset guard, Processor step-8's package-scope guard passes (the
keys are inside `priorDeclared`), and no test installs a real multi-entity package and then applies an
`upgradeExisting` proposal at it. Not this design's root cause and not its scope; it gates any future
`upgradeExisting` work, and its fix is non-obvious (the installed package's *source* Definition is not
recoverable from KV, only its key set, so "merge the old manifest in" is unavailable).

### 13.2 Prose that will be stale the moment Fire A lands

`refuseProtectedLensBinding`'s doc comment justifies its early return with the single-artifact premise
(and its *"left to install to resolve or reject"* clause is wrong for the empty ref both admit);
`packages/capability-author/manifest.yaml`'s description block and `package.go:128`'s `Description` both
narrate "a proposed artifact". All three are Fire A scope lines, not review findings.

## 14. Adversarial pass — run, and where it landed

Three independent lenses over the draft: **mechanism refutation**, **privilege / fail-closed /
composition**, and **consumer-census completeness**. Twelve, eleven and seven findings respectively.
Dispositions, by where they landed in the body — the superseded text was **rewritten in place**, not
banner-annotated:

| Finding | Disposition |
|---|---|
| **B** Fire A alone breaks the Loupe console (409 on every bundle) and both approve verdicts | §3.4's consumer table + §12 Fire A restructured; §10 gains `TestApproveBundle_LoupeAndCLI` |
| **B** The bridge adapter is structurally single-kind, and `assembleTargetContent` resolves `lensRef` only against the installed catalog | §1.2 rescoped; §3.6 Fire C written; §12 Fire C added and blocked |
| **B** `{vertexTypeDDL, grant}` satisfies the sibling minting design's R1, so R3 never runs | §3.2's allow-list; escalated to Andrew as the coupling in the Status banner |
| **B** Three pre-existing admission holes; the draft's §4 certified a containment that does not contain | §4 rewritten as §4.1/§4.2/§4.3; one ★★★ row filed; the vacuous guard-extension deliverable **dropped** |
| **S** Replace the hand-enumerated uniqueness rule with `validateAll` over the merged Definition | §3.5 point 3 (and the draft's stated create-conflict mechanism was wrong) |
| **S** Enumerate every in-Definition cross-reference, not just lens↔target | §4.1's table; pinning extended to `VertexTypeDDLArtifactContent` |
| **S** Census A's command did not produce Census A's expected result | §6.A rewritten and re-run; `cmd/bridge/main.go` added to scope |
| **S** The byte cap's "order of magnitude" was 4×, and JSON re-escaping was unpriced | §3.5 point 5: 128 KiB, derived |
| **S** DDL-cache-in-commit vs lens-CDC activation ordering | §9's two-version split |
| **S** The `status == "failed"` arm must write `[]`, not nil | §3.3 + a pinned test |
| **S** Approve-time resolver loading must become "if ANY artifact is grant/opMeta" | §3.4's table row |
| **S** The cap sat on the caller side of a caller-bypassable boundary | §3.5 point 5 moves it into the script |
| **M** CLI `list`'s `KIND` column; `structure_pins_test.go` confirmation; stale manifest/package prose; the `.target` "unchanged" cell; the "Weaver re-registers" non-event; the array-projection citation (`lenses.go:186/190`, not `:150`) | §2, §6.D, §9, §12, §13.2 |

Claims the pass **checked and could not refute**, recorded so they are not re-litigated: a
`{Lenses, WeaverTargets}`-only Definition builds and installs (and the lens's bucket need not pre-exist);
the three-conjunct predicate parses and evaluates as intended, with an empty `[]` correctly non-null; the
`capabilityProposals` row survives an array column; `ReviewCapabilityProposal` /
`MarkCapabilityProposalApplied` need no change; `SubmitCapabilityProposal`'s §2.5 read posture is
unchanged; the grant-composition subset argument; §4.3's structural fail-closed for `LensArtifactContent`;
Census B and Census C.
