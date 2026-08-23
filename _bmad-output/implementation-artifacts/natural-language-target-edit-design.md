# Editing an installed Weaver target in natural language — design

**Status: 🟢 Winston-adjudicated (2026-08-22), build-ready.** No frozen-contract change. No fork reached
Andrew: the one scope boundary below is **forced by the ratified apply-seam semantics**, not chosen — see §4.
**Component:** `internal/bridge` (the adapter) · `packages/capability-author` (one new lens) ·
`cmd/loupe` (edit affordance + demo posture).
**Builds on (all shipped):** [natural-language-weaver-targets-design.md](natural-language-weaver-targets-design.md)
(NL-1/NL-2) · [capability-apply-removal-refusal-design.md](capability-apply-removal-refusal-design.md)
(the refusal this design builds *on top of* rather than around) · `8f49c13b` (the `.description` aspect).

---

## For Andrew (ratify in one look)

1. **Edit rides the seam that already exists.** `contextRef` is plumbed end-to-end today — Loupe → op →
   `.request` aspect → `capabilityAuthorPending` lens → Loom pattern params → the bridge adapter's
   `Params["contextRef"]` — and read by nothing (NL-1 shipped it as a reserved field). Edit gives it its
   documented meaning: **the meta key of the target this authoring is scoped to.** No op, DDL, pattern or
   dispatch-lens change; **no change to `RequestCapabilityAuthoring`'s wire shape.**

2. **The one boundary: only console-authored targets are editable — and that is forced, not chosen.**
   `ApplyCapabilityPlan` sets `RefuseRemovals: true` **unconditionally, in both modes**
   (`capabilityapply.go:142`), and `Apply` refuses on `old \ new ≠ ∅` **before** submitting anything
   (`apply.go:217-236`). A capability proposal materialises a Definition holding **exactly one**
   weaverTarget (`capabilitymaterializer.go:628-660`), so an `upgradeExisting` aimed at a multi-artifact
   package (`clinic-domain`, `cafe-domain` — **every** built-in target owner is multi-artifact) does not
   damage it: it **refuses, 100% of the time**. Editable therefore means *the proposal can describe the
   whole package*, which is exactly the single-artifact `ai-target-<handle>` / `weaver-target-<slug>-<token>`
   packages the console itself mints. **This is the ratified 2026-08-21 posture working as designed**, not a
   gap this fire routes around.

3. **It does not trip the shelved additive-apply row.** That row's trigger is a producer emitting
   `upgradeExisting` **for a NEW artifact** (adding to a package). An edit that re-describes the *whole*
   single-artifact package is whole-Definition convergence — `old \ new = ∅` — which the seam already
   admits. The additive primitive stays shelved, correctly.

4. **Autonomy boundary unchanged: propose-only.** An edit is a proposal like any other — deterministic
   validation, human approve, operator apply, and the same `enforceAuthoredWeaverTargetScope`
   dispatch-authority gate, which is mode-agnostic (`capabilityapply.go:432`, after the mode switch). An
   edited target can no more dispatch a protected op than a new one.

5. **Demo posture (your ask): show Author + Edit, fail on POST.** Both affordances become visible in
   `LOUPE_DEMO_MODE`; the POST already 403s by the method rule (`demo.go:61-71`) with no server change. The
   403 gets a demo-aware inline message instead of today's raw error text. This is *cosmetic* un-hiding —
   `demoWriteDenied` remains the only enforcement, exactly as `api.js:20-23` says.

---

## 1. Problem & intent

NL-1/NL-2 gave the Studio a Describe panel that authors a **new** target. There is no way to say "that
target is nearly right — make it fire on 48 hours, not 24". The operator's only path is to hand-edit twelve
fields, or author a second target and disable the first. The intent: **describe the change in plain
language, against the target you are looking at.**

## 2. What already ships (verified 2026-08-22)

- **`contextRef` end-to-end**, read by nothing: `weaverauthor.go:502,550-552` → `ddls.go` `.request` →
  `lenses.go:138` (`p.request.data.contextRef AS contextRef`) → `patterns.go:36`
  (`subject.request.data.contextRef`) → `capability_author.go:224` reads only `intent`/`model`.
- **The catalog already carries every installed target**: `capabilityAuthorContext` projects
  `class = meta.weaverTarget` rows with `spec` + `description` (`lenses.go:179-191`), grouped into
  `view.WeaverTargets` (`capability_author_prompt.go:392`). The model can already *see* targets; it has
  never been asked to *change* one.
- **`upgradeExisting` is a first-class, validated mode** end-to-end: plan builder
  (`capabilityapply.go:364-413`), version preconditions (`:505-549`), protected-package deny **before** mode
  is read (`:371-376`), Loupe's review/apply already reads `targetMode/targetPackageName/targetBaseVersion/
  targetNewVersion` (`review.go:51-54`) and re-checks protection (`review.go:734-739`). **Nothing has ever
  produced the mode** — both producers hardcode `newPackage` (`capability_author.go:43`,
  `weaverauthor.js:261`).
- **Ownership is discoverable** only by scanning `vtx.package.*.manifest.declaredKeys` — no `declaredBy`
  link or aspect exists on a meta. Loupe already implements it (`lens.go:155-220`, key-generic).

## 3. The shape

### 3.1 `capabilityAuthorPackages` — one new lens (the only package change)

The bridge is **denied Core KV** (`deploy/nats-server.conf`, bridge deny `$KV.core-kv.>`), so it cannot scan
manifests. It gets them the P5 way — as a lens projection:

```
MATCH (p:package) RETURN p.key, p.manifest.data.name, p.manifest.data.version, p.manifest.data.declaredKeys
```

projected into the **existing** `capability-author-context` bucket. Key spaces are disjoint
(`vtx.package.*` vs the catalog lens's `vtx.meta.*`), so this is not a two-writers-one-key hazard.
*(Build step 1 verifies the platform admits two lenses → one bucket; if it does not, the lens gets its own
bucket plus the bridge read grant, and §3.2 is unchanged.)* `buildCatalogView` ignores rows it does not
recognise, so the prompt view is unaffected.

### 3.2 The adapter learns to edit (`internal/bridge`)

When `Params["contextRef"]` names a `vtx.meta.<id>`:

1. **Resolve the subject** from the catalog rows already read: the target's `spec` + `description`. An
   unknown/never-projected key is a **terminal, visible failure** (the NL-1 precedent — redelivery cannot
   fix a key that does not exist).
2. **Resolve the owning package** from the projected manifests: the package whose `declaredKeys` contains
   the meta key. None ⇒ kernel/bootstrap-seeded ⇒ terminal refusal naming the reason.
3. **Decide eligibility — the coverage test.** The edit is admissible only if the package's `declaredKeys`
   are a subset of the three keys this target owns (`vtx.meta.<id>`, `.spec`, `.description`). Anything else
   ⇒ terminal refusal: *"target X is owned by package Y, which also declares N other artifacts; a capability
   apply may not remove what it does not describe. Edit it in code, or Describe a new target."* This
   **pre-empts** the apply-time `ErrApplyWouldRemove` with a reason the operator can act on.
4. **Prompt in edit mode** (§3.3) and set
   `Target{Mode:"upgradeExisting", PackageName:<owner>, BaseVersion:<installed>, NewVersion:<patch+1>}`.
5. **Preserve identity.** The edited artifact MUST keep the original `targetId` and MUST NOT empty the
   description — see §5. Both are checked deterministically before recording; a violation is repaired via
   the existing correction pass, then recorded `invalid` if still wrong.

### 3.3 The edit prompt

The authoring rulebook gains an **edit preamble** used only in edit mode: the current target's `spec` +
`description` verbatim, the bound lens's **canonicalName**, and four rules the validator actually enforces —
**E1** keep `targetId` exactly as it is (a rename is a removal, and the apply will refuse it); **E2** keep a
non-empty description, updating it to describe the NEW behaviour; **E3** change only what the intent asks and
leave every other gap byte-identical; **E4** `content.lensRef` stays the named canonicalName. The template
version moves to `capability-author/v2` so `promptHash` distinguishes an edit from an author.

**E4 exists because the installed spec is not answerable as-is.** `resolveLensRef` (`build.go:579-610`)
rewrites an authored lensRef to a **NanoID** at install, so the spec the preamble prints carries an opaque
id, while the tool schema accepts only canonicalNames. Printing it without naming the lens leaves the model
two bad moves: echo the NanoID (unresolvable ⇒ invalid ⇒ the single repair call is burned on every edit) or
guess a name — and a *plausible wrong guess resolves cleanly and records `valid`*, silently re-pointing the
target at another lens's rows. So the preamble names the lens, and `editProblems` pins the **resolved**
lensRef against the installed one: an edit is not the place to re-bind a lens.

### 3.4 Loupe

- **`GET /api/weaver/target/<targetId>`** gains `editContext {metaKey, packageName, packageVersion,
  editable, reason}` — computed with the existing `buildLensPackageIndex` walk (Loupe may read Core KV; P5
  inspector exception). `editable=false` carries the human reason, so the console explains *before* a model
  call is spent.
- **The target detail page gains "Edit with AI"** → the Describe panel in edit mode, showing which target is
  being edited and its current description; Submit sends `contextRef = metaKey`. When `editable=false` the
  affordance renders **disabled with its reason** rather than vanishing.
- **The review console** labels an edit proposal (`targetMode = upgradeExisting`) as *"edits `<targetId>` in
  `<package>` `<base>` → `<new>`"*, so a reviewer is never asked to approve an in-place change that looks
  like a fresh install.
- **`buildApplyTarget`** honours an edit context for a Studio-proposed (human) edit, so Load-into-Author →
  Propose keeps the upgrade shape instead of silently minting a fresh package.

### 3.5 Demo posture

Un-hide (drop `demoHide`/`data-demo-hide`): the **Author nav link** (`index.html:205`), the Describe
**Submit** (`weaverauthor.js:107`), and the new **Edit with AI** affordance. `Run checks`/`Propose`/the Trial
arming buttons stay hidden — they are not what this ask names. The POST already 403s
(`demoWriteDenied("POST","/api/weaver/author/request") == true` today, no server change). `api()` gains the
status code on the returned error so the Describe panel can render the demo denial as a **styled
demo-notice**, not a generic red error. The stale comment at `weaverauthor.go:36-37` is corrected.

## 4. Why the boundary is forced, not chosen

Editing a **domain** package's target in place is not merely refused — it is *unsound*, and the
removal-refusal design §4 already says why: `packages/cafe-domain/*.go` is that package's second author, and
the next `lattice-pkg install` from source diffs the source Definition against the stored `declaredKeys` and
**silently tombstones** whatever the AI added or changed. Additive apply would trade a loud refusal for a
delayed silent destruction; making it sound needs a per-key origin stamp **and** an explicit removal verb —
the shelved row. Until then, "one package, one author" is the invariant, and **per-proposal packages are the
sound granularity, not a workaround**. Code-owned targets are edited in code; console-authored targets are
edited in the console.

## 5. Failure modes this design must surface (all real, all grounded)

Every row below that says *pre-empted* is a **terminal refusal in `resolveEditSubject`, before a vendor call
is spent** — the design's standing principle: a scoping failure is definitive, so it is answered here rather
than after a model call, a human review and an apply.

| Condition | Where it bites | Surfaced as |
|---|---|---|
| Target owned by a multi-artifact package | `ErrApplyWouldRemove` at apply | **Pre-empted** at §3.2 step 3, before a model call |
| Installed spec carries state the artifact schema cannot express (`augur`, `mode`, `admission`, gap `enumerations`, gap `class`) | the edit would write those blocks AWAY, invisibly — key sets are unchanged so coverage cannot see it | **Pre-empted**, naming the field. The guard is a **whitelist derived from what the assembler emits** (`editableSpecKeys`, pinned against `assembleTargetContent` in both directions), not a hand-enumerated denylist — which is how `class` was found |
| Owning manifest carries a non-empty `depends`/`description` | an edit rewrites the manifest aspect from the materialized Definition (`build.go:425-434`), blanking them | **Pre-empted**, naming the field |
| Installed `lensRef` names no projected lens (or is empty) | every possible answer is unresolvable ⇒ guaranteed `invalid` | **Pre-empted** — a preamble note would burn both vendor calls to reach a certainty |
| Owning package is platform-protected | `capabilityapply.go:371-376` at apply | **Pre-empted** via an injected `ProtectedPackagePredicate` (the `ArtifactValidator` precedent — `internal/bridge` stays pkgmgr-free; `nil` is a construction error, never a fail-open default) |
| Owner's manifest is undecodable | `apply.go:222-225` `declarationMalformed` | **Pre-empted**, and distinguished from genuinely-unowned — never the false claim "kernel- or bootstrap-seeded" |
| Model re-binds the lens | nothing — it would record `valid` and silently repoint the target | `editProblems` conjunct on the **resolved** lensRef (§3.3) |
| Model renames `targetId` | key is `entityNanoID(pkg,"weaverTarget:"+id)` (`installer.go:311`) ⇒ removal+add | Deterministic check ⇒ correction pass ⇒ `invalid` |
| Edit empties the description | `.description` emitted only when non-empty (`build.go:250-254`) ⇒ undescribed key | Deterministic check ⇒ correction pass ⇒ `invalid` |
| A key was tombstoned out of band | `applyWouldReviveError` (`apply.go:233-235`) | Apply-time 409, reason shown verbatim |
| `newVersion == installed` / `baseVersion` stale | `capabilityapply.go:526-528,545-547` | Apply-time 409; base is re-read at propose time |
| Package uninstalled between propose and apply | `ErrNotInstalled` (`:410-413`) | Apply-time 409 |

## 6. Test plan (every row an assertion, not a hope)

1. Adapter: edit mode resolves owner + version and emits `upgradeExisting`; **coverage test refuses** a
   multi-artifact owner terminally, with the package named.
2. Adapter: a model answer that renames `targetId`, or blanks the description, is caught deterministically
   (mutation-tested — the check must fail when the guard is removed).
3. Round trip on a real single-artifact package: install a `weaver-target-*` package, edit it, apply →
   `old \ new = ∅`, the spec changes, `declaredKeys` unchanged, no key tombstoned.
4. `enforceAuthoredWeaverTargetScope` still denies a protected-op `directOp` on the **edit** path.
5. Loupe: `editContext.editable=false` + reason for a domain-owned target; `true` for a console-authored one.
6. Demo: Author nav link **and** Describe Submit are present in demo mode (the first test to pin an
   affordance), **and** `POST /api/weaver/author/request` is 403 — visible ≠ permitted.
