# A capability apply may not remove what its Definition does not describe — design

**Status: ✅ Andrew-ratified (2026-08-21).** No frozen-contract change. It went to Andrew rather than
Winston-adjudication because it **narrows a capability an earlier design already ratified** —
`ai-authored-capabilities-design.md` §3.5 and its §8 Fire-2 brief both stated that `upgradeExisting` into a
vertical business package is what that fire shipped to allow — and it **defers** the primitive that would
make that promise safe. Narrowing ratified scope is a scope call, not a mechanism call.

**Ratified as branch (a) — refuse now, defer additive** (the fork below, as recommended). The §9 prose
corrections have been executed: `ai-authored-capabilities-design.md` §3.5 / §8 Fire-2 / test plan and
`capability-proposal-bundles-design.md` §12 / §13.1 are rewritten in place. The one remaining §9 item is
`internal/pkgmgr/capabilityapply.go:55-64`'s doc comment, which is **code** and therefore a build scope
line (§12, step 2). **Build-ready for the Lattice Steward.**
**Component:** `internal/pkgmgr` (the apply seam) · `cmd/loupe` (review apply endpoint) ·
`cmd/lattice-pkg` (`apply-proposal`) · `scripts/lint-conventions.go`.
**Backlog row:** Lattice lane → *Component maintenance* → *[Pkgmgr] An `upgradeExisting` capability apply
tombstones every package key the proposal omits* (★★★, M).
**Builds on (all shipped):** [ai-authored-capabilities-design.md](ai-authored-capabilities-design.md) §3.5,
§8 Fire 2 · Contract #8 §8.1/§8.6 · `retention-class-key-custody-design.md` §30 (the guard-placement
precedent inside `Apply`).
**Discovered by:** [capability-proposal-bundles-design.md](capability-proposal-bundles-design.md) §13.1.
**Author:** Winston (Designer fire, 2026-08-21).

---

## For Andrew (ratify in one look)

**What it does, in two lines.** `Installer.Apply`'s in-place branch is a **convergence** operator — the
`Definition` it is handed *is* the package's desired state, and `declaredKeys \ newKeys → tombstone`. The
capability path hands it a **partial** Definition (one approved artifact), so an `upgradeExisting` proposal
naming an installed multi-entity package tombstones every key that package declared and the proposal does
not mention. This design refuses that at the seam: a capability apply **may not remove**, the refusal is
computed atomically with the delta and names the keys, and the only way to reach `Apply` with a capability
plan becomes one function that cannot forget to set it.

**The one fork — resolved: Andrew ratified branch (a), 2026-08-21.** The row's `no-pattern:` line asked
for *additive-only (partial-Definition) package apply*. The recommendation was **refuse now, defer
additive** — branch (a), and that is what was ratified:

- **(a) Refuse and defer (recommended).** A capability apply refuses any removal. The genuinely-correct
  case still works and self-opens: a package whose *entire* content is this proposal's artifacts (an
  edited re-propose of the lens that created the package) covers `declaredKeys`, produces no removal, and
  applies. Everything else is refused with a message naming the keys and the remedy (`newPackage`). Size
  **S–M, one fire.**
- **(b) Build additive apply now.** A partial-Definition mode that suppresses the tombstone arm and unions
  `declaredKeys`. I reject it on two grounds §4 develops: it has **zero live producers** to serve (census
  A below: `internal/bridge/capability_author.go:43` pins `mode = "newPackage"` as a const, and
  `cmd/loupe/web/js/logic/weaverauthor.js:261` hardcodes the same — *nothing shipped emits
  `upgradeExisting`*), and it is **not yet sound**: an AI-added entity inside a repo-authored package is
  silently tombstoned by the next ordinary source-authored upgrade of that package, so it would ship a
  silent revert in place of a loud one. Making it sound needs an origin stamp *and* a removal verb — a
  design, not a flag.
- **(c) Remove `upgradeExisting` from the capability path entirely.** Simpler than (a) by a few lines, and
  it throws away the one case that is already correct. Rejected in §11.

**What ratification cost.** Branch (a) superseded `ai-authored-capabilities-design.md` §3.5's *"or the diff
`Installer.Upgrade` produces (for `upgradeExisting`)"*, its §8 Fire-2 sentence *"`upgradeExisting` there is
exactly what Fire 2 shipped to allow"*, and its test-plan line *"a third applies an `upgradeExisting` and
confirms the F-004 diff lands"*. All three are rewritten in place (§9). That third test was never built —
which is precisely why the defect shipped.

**No frozen-contract edit is staged.** `UpgradePackage`'s payload, semantics and step-8 guards are
untouched (§7); this is a restriction on *which Definitions the capability path may hand to `Apply`*, which
no contract specifies. `docs/contracts/08-package-install.md` already carries two other fires'
uncommitted edits — none of them mine.

---

## 1. Problem & intent

### 1.1 What the row said, and what grounding corrected

> The plan builder materializes a Definition from the proposal's artifacts alone; `Installer.Apply` DIFFS
> it against the installed package's whole `declaredKeys`, so old\new → tombstone. Correct only for a
> package whose entire lineage is that proposal.

The mechanism is exactly right. Two corrections change what to build.

**(1) "Correct only for a package whose entire lineage is that proposal" is one proposal too generous.**
Package entity keys are deterministic in `(package, kind, canonicalName)` (Contract #8 §8.1;
`installer.go:293` derives `pkgKey = PackageVertexPrefix + entityNanoID(def.Name, "package")`), so a
package built by proposal 1 (lens A) and upgraded by proposal 2 (lens B) has `old = {pkg, manifest, A…}`
and `new = {pkg, manifest, B…}` — **A is tombstoned**. The accumulation case, i.e. the only reason anyone
would want `upgradeExisting` on an AI-authored package at all, is broken too. The correct population is
narrower and sharper: *a proposal whose materialized Definition covers everything the package declares.*

**(2) "The fix is non-obvious because the source Definition is not recoverable" is half true, and the half
that is false is the half that matters.** A `pkgmgr.Definition` is indeed not reconstructible from Core KV.
But the diff does not operate on Definitions — it operates on `(key → document)` pairs, and
`diffManifest` already reads every one of them (`upgrade.go:459`, `i.getCommitted(ctx, op.Key)`). So the
old bodies *are* available, and an additive mode is mechanically expressible. It is not *sound* — for a
different reason, developed in §4 — and that is what makes deferring it a decision rather than a
limitation.

### 1.2 The mechanism, end to end

1. `CapabilityApplyPlanForProposal` (`internal/pkgmgr/capabilityapply.go:134`) reads the approved
   proposal's `.artifact` + `.target`, binds `target.mode` to the live catalog (`:198-208`), and calls
   `DefinitionForCapabilityArtifact` (`capabilitymaterializer.go:924`) — one artifact, one Definition.
2. `cmd/loupe/review.go:733` and `cmd/lattice-pkg/main.go:560` both call
   `inst.Apply(ctx, plan.Definition, pkgmgr.ApplyOptions{})`.
3. `Apply` (`apply.go:123`) finds the installed package, and — cross-version, which every
   `upgradeExisting` is by construction — takes the in-place branch: `computeDeltaAgainst`
   (`upgrade.go:231`) → `readDeclaredKeys` (`:598`) → `buildManifestBatch` → `diffManifest` (`:445`).
4. `diffManifest`'s removal arm (`:530-590`): every key in `old \ new` that is committed and is not a
   `vtx.retentionclass.*` holder becomes `{Op: "tombstone", ExpectedRevision: rev}`.
5. The batch commits. The Processor's step-8 package-scope guard **passes** — every tombstoned key is
   inside the package's own `priorDeclared`, which is exactly what the guard authorizes.

For an `upgradeExisting` at `cafe-domain` carrying one lens, the delta is: package root → update (version
changed), `.manifest` → update (its `declaredKeys` shrinks to four, and `depends`/`description` blank,
`build.go:425-433`), the new lens → create, **and every other key `cafe-domain` declares → tombstone**.
DDLs, roles, permissions, `grantedBy` links, lens specs. Live, executing, granted state.

### 1.3 Why this is a category error, not a bug in either half

`Apply`'s in-place branch is a **convergence** operator and is right to be: for a source-authored package
the Definition *is* the whole truth, and a dropped entity must be removed. `CapabilityApplyPlanForProposal`
is a **partial** description and is right to be: a proposal describes its own artifacts and nothing else.
The defect lives in the seam that lets one be passed to the other with no declaration that it is partial.
That is what this design fixes, and it is why the fix belongs at the seam rather than inside either half.

---

## 2. Grounding ledger

| Claim | Where it is decided |
|---|---|
| The in-place branch tombstones `old \ new` | `internal/pkgmgr/upgrade.go:530-590` (emission), `:420-427` (doc) |
| …exempting only retention-class holders | `upgrade.go:558-575` |
| …and skipping keys absent from KV | `upgrade.go:551-556` |
| A key absent from `old` but live in KV is **revived by update**, not created | `upgrade.go:466-486` |
| Old key set = manifest `declaredKeys` + the manifest aspect itself | `upgrade.go:594-619` |
| Package key is deterministic in the name, so old/new land on the same package | `installer.go:291-293` |
| The manifest body carries `depends` + `description` from the Definition | `build.go:425-433` |
| The capability plan is a one-artifact Definition | `capabilitymaterializer.go:924-968` |
| Both live callers pass a bare `ApplyOptions{}` | `cmd/loupe/review.go:733`, `cmd/lattice-pkg/main.go:560` |
| `upgradeExisting` requires the package installed; `newVersion` **defaults to `"0.1.0"`** | `capabilityapply.go:185-208` |
| The step-8 package-scope guard authorizes exactly `priorDeclared` | Contract #8 §8.2/§8.6 (unchanged) |
| Guard placement precedent: refuse *before* the empty-delta and DryRun returns | `apply.go:192-197` (`enforceSecureColumnRetirement`), `:199` (empty-delta), `:205` (DryRun) |
| Typed-refusal precedent: sentinel + typed error + fields, mapped to 409 | `installer.go:711-751`, `cmd/loupe/pkg.go:375-386` |
| `declaredKeys` is also a **package-ownership** input on the security plane | `authored_dispatch_scope.go:46-95` |
| Loupe's recovery discriminator is name **+ target version** | `cmd/loupe/review.go:711-721`, `:862-878` |

**One correction to a comment I would otherwise have cited as settled.**
`capabilityapply.go:55-64` says a vertical package is deliberately off the deny-list because
*"upgradeExisting there is precisely what this fire exists to allow"*. That sentence is true about the
deny-list's intent and false about the platform's capability: what `upgradeExisting` there actually does
today is tombstone the vertical. The deny-list decision itself stands (it is about *trust*, not about this
defect); the clause explaining it needs the correction §9 lists.

---

## 3. The shape

Two mechanisms, one fire. Neither introduces state, a lens, an op, or a contract change.

### 3.1 `Apply` learns to refuse a removal it was not authorized to make

```go
// ApplyOptions
//
// RefuseRemovals makes Apply refuse — atomically with the delta computation —
// any in-place apply whose diff would tombstone a declared key. Set it whenever
// the Definition is a PARTIAL description of the package rather than its whole
// desired state: Apply's in-place branch is a convergence operator, so a partial
// Definition otherwise reads as "everything I do not mention is retired".
// Its zero value keeps the whole-Definition convergence semantics every
// source-authored install/upgrade depends on.
RefuseRemovals bool
```

Enforcement sits in `Apply` **before `enforceSecureColumnRetirement`** and **before the `DryRun` return**.
It is pure — it reads the diff summary and nothing else — so it costs a preview nothing.

*(Amended 2026-08-22, during the build.)* Two things this paragraph originally said were wrong.

**Order.** The removal guard runs *first*, not after the secure-column retirement guard. Both are fed by
the same dropped-key set, so a partial Definition that drops a Secure Lens trips the retirement guard on
every run — and that guard answers with a remedy the caller cannot perform (declare a
`RetiredSecureColumn`, an attestation about ciphertext a proposal describing one artifact of somebody
else's package has no standing to make) at a **502**, because `undeclaredSecureColumnDropError` returned a
bare error. `clinic-domain` is off the deny-list and declares Secure Lenses, so the population is real.
The removal refusal is the harder boundary — it says the apply must not happen at all, by any attestation —
so it owns the answer. Source-authored callers leave `RefuseRemovals` false, so for them the block is a
no-op and the retirement guard sees exactly what it saw before. (`undeclaredSecureColumnDropError` also
gains a sentinel, `ErrUndeclaredSecureColumnDrop`, and a 409 arm, since its 502 is a live defect on a path
this fire now shares.)

**"Before the empty-delta return" is not load-bearing for this guard.** It was stated as holding "for the
same reason" as the retirement guard's placement, and that reason is vacuous here: a Definition that stops
describing a declared key necessarily rewrites the manifest aspect's `declaredKeys`, so its delta is never
empty. The retirement guard genuinely can face dropped columns with zero mutations; this one cannot. The
`DryRun` half of the claim stands.

The refusal is typed, mirroring `DeclaredKeysOccupiedError` field-for-field in shape and reasoning — a
distinction the code *makes* is carried in fields, never scraped from rendered prose:

```go
var ErrApplyWouldRemove = errors.New("pkgmgr: apply refused — it would drop keys its Definition does not describe")

type ApplyWouldRemoveError struct {
    PackageName     string   // the package whose apply was refused
    DeclaredKeys    int      // how many keys the installed package declares (deduplicated)
    DescribedKeys   int      // how many the submitted Definition describes (deduplicated)
    UndescribedKeys []string // sorted; old \ new, NOT the subset that would be tombstoned
    Remedy          string   // supplied by the caller that knows which next move is true
    message         string
}
func (e *ApplyWouldRemoveError) Error() string { return e.message }
func (e *ApplyWouldRemoveError) Unwrap() error { return ErrApplyWouldRemove }

// The same rule, read the other way.
var ErrApplyWouldRevive = errors.New("pkgmgr: apply refused — it would revive keys an operator tombstoned")
```

*(Amended 2026-08-22.)* `Remedy` is a field rather than fixed prose because the original message's
"propose it as `newPackage`" is false for the caller that already IS a `newPackage` proposal and lost a
race for its name — for that one the remedy is "choose a name that is free" — and because §11's rename
residual lands here too and needs naming. `Apply` states the facts; `ApplyCapabilityPlan` supplies the
remedy it owns.

`cmd/loupe/pkg.go:375`'s `packageApplyStatus` gains `ErrApplyWouldRemove` in its 409 arm: like every other
entry there, it is a deterministic package-state refusal that fails identically on every retry.

**The predicate is COVERAGE, not emission** *(amended 2026-08-22 — see the note under the table)*. It
refuses iff `old \ new` is non-empty: any key the installed manifest declares that the submitted
Definition does not describe. The state table it is evaluated against:

| Delta the in-place branch produces | `old \ new` | Outcome |
|---|---|---|
| `newPackage`, fresh install — create-only batch, no diff arm runs | n/a | admit (the flag is unreachable here by construction) |
| Covering Definition, body edits only | ∅ | admit — updates, `Tombstoned == 0` |
| Covering Definition, byte-identical bodies | ∅ | admit — empty delta, `Action: "skip"`, `Reason: "no changes"` |
| Non-covering: a declared key is dropped and live in KV | ≥1 | **refuse**, naming every key |
| Non-covering: a dropped key is a `vtx.retentionclass.*` holder | ≥1 (preserved, so no tombstone is emitted) | **refuse** — the holder is left LIVE AND UNDECLARED, a class key with nothing recording that this package custodies it |
| Non-covering: a dropped key is absent from KV entirely | ≥1 (skipped, so no tombstone is emitted) | **refuse** — nothing is removed, but the key stops being declared, and the declaration is what a repair reads |
| Non-covering: a dropped key is **already tombstoned** out-of-band | ≥1 | **refuse** — the removal is a no-op, but the Definition still fails to describe a key the package declares |
| Covering, but a DESCRIBED key is already tombstoned out-of-band | ∅ | **refuse** (`ErrApplyWouldRevive`) — the body-diff path would un-tombstone a key an operator deliberately killed, and this Definition said nothing about that decision |

**Why the predicate is coverage and not the emitted tombstone list** *(amended 2026-08-22, during the
build; the original text had the last three rows wrong and reasoned from "there is nothing to remove")*.
That reasoning covers the entity keys only. The admitted apply still runs the in-place branch, and
`build.go:425-433` takes `declaredKeys`, `depends`, `description` and `version` **from the submitted
Definition** — so a partial Definition emitting zero tombstones still replaces the manifest's
`declaredKeys` with just its own, blanks `depends` and `description`, rewrites `version`, and leaves a
dropped retention-class holder live but no longer declared, severing the package's key-custody
declaration. A cold review demonstrated both no-tombstone shapes admitted at `action="upgrade"
tombstoned=0` with `declaredKeys` narrowed to the proposal's own lens. Emission is only the loudest
symptom of the misdescription; coverage is the property.

The last row is the same rule read in the other direction, and was added for the same reason: refusing a
no-op re-tombstone of an *undescribed* key while silently resurrecting a *described* one is the rule
applied inconsistently. Source-authored convergence keeps today's revival behaviour untouched — only a
caller that has declared its Definition partial is refused.

The outcome column is deliberately uniform: **refuse the whole apply, never skip the offending keys.** A
per-key skip would commit a package into a state that matches *neither* the old Definition nor the new one,
with a success signal on it — which is a worse version of the defect. Availability cost is nil: census A
shows the refused population is empty today.

**The message must carry the remedy, because the operator's next move is not obvious.** Shape:
*"apply refused: package `cafe-domain` declares 61 keys; this Definition describes 4, so applying it would
tombstone 57 keys it does not describe (`vtx.meta.…`, `vtx.role.…`, `lnk.permission.…`, +54 more). A
capability proposal describes only its own artifacts — propose it as `newPackage` (a proposal-owned package
may bind lenses and targets across packages) rather than as an upgrade of a package it does not
describe."*

### 3.2 The capability path cannot forget to set it

An option a caller must remember is an option a caller will forget; the fail-closed answer here is
structural rather than a flag default (§11 prices inverting the default and rejects it).

- `CapabilityApplyPlan.Definition` becomes unexported (`definition`), with
  `func (p *CapabilityApplyPlan) MaterializedDefinition() Definition` for inspection. Census C shows the
  only out-of-package reader that is *not* a call to `Apply` is one assertion in
  `cmd/bridge/capability_author_test.go:239-242`; every in-package test keeps direct field access.
- `func (i *Installer) ApplyCapabilityPlan(ctx context.Context, plan *CapabilityApplyPlan) (*ApplyResult, error)`
  becomes the single sanctioned way to apply a plan. It sets the options itself:

  | Field | Value | Why |
  |---|---|---|
  | `RefuseRemovals` | **always `true`**, both modes | A capability Definition is never authorized to remove. On the `newPackage` arm it is inert *by construction* — `applyFreshInstall` runs a create-only batch with no diff — and that inertness is the point: it closes the narrow race in which the name is installed between the plan build's `IsPackageInstalled` check (`capabilityapply.go:195`) and `Apply`'s own `findInstalledPackage`, which would otherwise silently take the in-place branch. |
  | `RequireInstalled` | `true` for `upgradeExisting` | (i) If the package is uninstalled between the plan build and the apply, the honest outcome is `ErrNotInstalled`, not a fresh install landing on tombstoned keys and failing later as an occupancy refusal. **This is the option's only observable effect through this entry point, and the only thing that can pin it.** *(Amended 2026-08-22.)* The originally-stated second effect — that it is a conjunct of `Apply`'s same-version skip branch and so defeats that skip — is **unreachable here**: §3.3's `newVersion != installed` precondition refuses such a plan before one can exist, so no `ApplyCapabilityPlan` call can reach the skip it was said to defeat. An unfalsifiable justification is the inert-guard shape with the sign flipped, which §13.1 already caught once in this document; it is struck rather than left standing. The silent-`applied` harm it named is real and is closed — on the `newPackage` arm by the post-condition below, and on this arm by §3.3. |
  | `Force` | **never** | Redundant once `RequireInstalled` is set (both defeat the same skip conjunct), and it would additionally re-open the same-version diff path that §3.3 refuses for an independent reason. |
- **`ApplyCapabilityPlan` also refuses a `newPackage` plan that comes back `Skipped`** *(added 2026-08-22,
  during the build)*. `RequireInstalled` cannot be set on that arm — the package is absent by definition
  there, so it would return `ErrNotInstalled` for every legitimate `newPackage` apply — and a skip on that
  arm can only mean the name was claimed before the apply ran, by either of two branches (`Apply`'s
  same-version skip, or `Install`'s own idempotency skip mirrored through `applyFreshInstall`). Both
  installed nothing, and both return a nil error the caller stamps `MarkCapabilityProposalApplied` over.
  The refusal is typed (`ErrPackageNameClaimed`, 409) and the post-condition tests the RESULT rather than a
  branch, because two branches produce it.
- After migration, `cmd/loupe/review.go` and `cmd/lattice-pkg/main.go` call `inst.ApplyCapabilityPlan(ctx, plan)`.
- **`MaterializedDefinition()` returns a deep copy** *(added 2026-08-22)*. `Definition` is a struct of
  slices and maps, so returning it by value handed out the plan's own backing arrays: a caller could edit
  the lens body through the accessor and have `ApplyCapabilityPlan` submit the edited one, defeating the
  whole point of unexporting the field. §3.5's lint regex cannot see that shape — it involves no `Apply`
  call at all — so the accessor copies.

### 3.3 `upgradeExisting`'s preconditions become explicit, from data already stored

`CapabilityApplyPlanForProposal`'s `case "upgradeExisting"` gains three refusals. All three read fields the
proposal already carries (`.target {mode, packageName, baseVersion, newVersion}`,
`packages/capability-author/ddls.go:129`), so none needs a package version bump or a new op field.

All three comparisons fold surrounding whitespace *(amended 2026-08-22)*, following this file's own rule
in `normalizePackageName`: widening a match set is safe for a **deny** check and unsafe for a destructive
resolver, and these are deny checks. Byte-exact, `newVersion: "1.2.0 "` passed "an upgrade must move the
version" while moving nothing an operator can see. The `baseVersion` mismatch check folds too even though
widening makes it fire *less* often, because trimming cannot bridge two genuinely different versions — it
can only stop refusing a value that already names the installed one.

| Refusal | Why |
|---|---|
| `newVersion` **absent** | The shared `if version == "" { version = "0.1.0" }` default (`capabilityapply.go:185-187`) is meaningful for `newPackage` and meaningless for an upgrade: it would record `cafe-domain@1.3.0` as `0.1.0`. Absence must be declared, not defaulted. |
| `newVersion` **equals the installed version** | Loupe's two-commit recovery branch answers "did the install half commit?" by asking whether the package is live **at the target version** (`review.go:711-721`, `:862-872`). If an upgrade's target version equals the version already installed, that question is unanswerable — `installed at newVersion` means both "applied" and "never applied" — and the console 409s every such proposal as recoverable, closing it over an artifact that never landed. Refusing here makes the discriminator sound **by construction** rather than by convention (§3.4). *(The silent-`skip`-then-`applied` harm this rule would also have prevented is already closed by `RequireInstalled`, §3.2 — this rule is not carrying that load.)* |
| `baseVersion` **absent, or ≠ the installed version** | A proposal authored against `1.0.0` applied over `1.1.0` is a stale apply: the artifacts it *does* describe overwrite whatever `1.1.0` changed. The proposal already records the version it was authored against. Absence is refused rather than tolerated — there is no live producer to break (census A). *(Amended 2026-08-22: this was described as "the mode's optimistic-concurrency check" and it is not one.* The comparison runs at plan-build time against its own `findInstalledPackage`; `Apply` then repeats that lookup independently and never re-checks `baseVersion`, so there is a window between them — the same TOCTOU-inside-a-safety-guard shape §11 rejects when it dismisses dry-run-then-apply. It is a **precondition**, not a concurrency check. What actually holds the line if the version moves inside the window is the coverage refusal, which is evaluated inside the call that computes the delta and so is atomic with it. Making `baseVersion` a real OCC conjunct means carrying an expected-base-version into `ApplyOptions` and comparing it against the `existing` that the diff itself is computed from — genuinely small, and deliberately **not** taken here: it changes the shared install/upgrade options contract for one capability-path guarantee, which is the blast-radius argument §11 already used to reject inverting `RefuseRemovals`' default. Flagged for a future fire rather than assumed.) |

### 3.4 What this *does not* change, and why that is load-bearing

Loupe's two-commit recovery branch (`review.go:711-721`) 409s a proposal whose target package is already
installed **at the target version**, and its own doc comment (`:862-872`) explains that name alone cannot
separate "the install committed" from "a package of that name was already there". §3.3's `newVersion ≠
installed version` rule is what makes that version comparison a *sound* discriminator for `upgradeExisting`
rather than an accident: post-rule, `installed at newVersion` can only mean the apply committed.

***Amended 2026-08-22, during the build: as originally written this section claimed the property held with
no change to Loupe, and that was false.*** The rule lives in `CapabilityApplyPlanForProposal`, and
`reviewCapabilityApply` reaches the plan builder **only after** the recovery branch has already answered —
so in exactly the state the rule refuses, the branch short-circuits and the rule never runs. A security
review demonstrated it end to end: package live at `1.2.0`, an approved proposal at `newVersion: 1.2.0`
never applied, `POST …/apply` → `409 {"resumable": true, …}`, `POST …/mark-applied` → accepted. The precise
harm §3.3 cites as its own justification was still fully open, and the op's guards do not close it: the DDL
checks that `packageKey` is live and name-matches, never the version.

**What now enforces it:** `reviewCapabilityApply` calls `pkgmgr.ValidateCapabilityApplyTarget` **before**
the recovery classification. That function shares `checkUpgradeExistingVersions` with the plan builder
rather than restating the rules, so the two cannot drift, and it is scoped to `upgradeExisting`: the
`newPackage` arm's bypass of the catalog binding is the *point* of the recovery branch (a `newPackage`
proposal whose install committed IS a package of that name), so re-running it there would remove the only
route out of a half-commit. An unreadable `.target` returns nil and lets the plan builder — which re-reads
the same aspect and remains the authority — refuse; this call may only move a determination earlier, never
be the only one that makes it.

**A cleaner discriminator was looked for and does not exist durably.** The one artifact that records
"this exact apply committed" is the Processor's idempotency tracker for the deterministic
`UpgradePackage` requestId, and `TrackerTTL` is 24h — so it answers correctly for a day and then answers
"never applied" forever. The proposal's own `appliedAs` link and `review.state` record that the *close*
happened, which is the question already known. The ordering fix is therefore the answer, not a stopgap.

The residual §3.4 originally noted still stands for `newPackage`, unchanged and out of this fire's scope:
for that mode, name + version cannot separate "my install committed" from "someone else's package was
already there", which is what `:862-878` says.

### 3.5 The gate that binds the next author

Per the lint doctrine: the migration clears today's two call sites, and nothing stops the third. Unexporting
`definition` removes the *default* reach — an author typing `plan.` no longer sees a Definition to pass —
but `MaterializedDefinition()` re-opens it for anyone determined. So `scripts/lint-conventions.go` gains one
rule, in the file's established text-scanner idiom:

> Outside `internal/pkgmgr`, `MaterializedDefinition()` may not appear as an argument to an
> `Installer.Apply` call. Findings are blocking. Zero occurrences after the migration, so it ships
> blocking, not warn-first.

Regex over each line: `\.Apply\([^\n]*MaterializedDefinition\(\)`. **Stated residual**, matching this
file's declared pragmatic-scanner posture: assigning the accessor's result to a local first and passing the
local evades it. That is a deliberate two-step, not the thing an author reaches for by default, and the
accessor's own doc comment says what it is for.

---

## 4. What this does NOT fix: additive apply, and why deferring it is the decision

**The gap that remains.** After this fire, "add lens B to the package proposal 1 created" is refused. The
missing primitive is a **package-scoped additive apply**: add these entities, remove nothing, and record
that the package now owns them.

**It is mechanically expressible** (§1.1 correction 2): skip the removal arm, and write the manifest's
`declaredKeys` as the union of old and new. **It is not sound**, for a reason no flag addresses:

1. **A repo-authored package has a second author, and the source tree wins.** `cafe-domain` is defined by
   `packages/cafe-domain/*.go`. The next `lattice-pkg install`/`upgrade` from source diffs the *source*
   Definition against the (now unioned) `declaredKeys`, finds the AI-added lens in `old \ new`, and
   tombstones it — silently, with a success signal on the operation that destroyed it. Additive apply
   would therefore replace a loud destruction with a delayed silent one. Exempting those keys needs a
   durable per-key origin stamp (grant-provenance Inc 3's `origin` is the precedent), and exempting them
   creates a class of entity **no package edit can ever remove**, which then needs its own removal verb —
   the same obligation `vtx.retentionclass.*` holders carry (`upgrade.go:558-575`), where an explicit
   destruction verb owns the removal. That is a design.
2. **The manifest has one author too.** `depends`, `description` and `version` come from the Definition
   (`build.go:425-433`). A partial Definition blanks the first two and rewrites the third. Additive apply
   needs a rule for each — i.e. it needs to know which fields a partial author may speak for.

**Nothing is waiting on it.** Census A: every shipped producer of `target.mode` emits `newPackage` — the
bridge as a package-level const (`internal/bridge/capability_author.go:43`), the Studio as a literal with a
freshness token that deliberately never reuses a package name
(`cmd/loupe/web/js/logic/weaverauthor.js:233-261`). There is no submit affordance in `cmd/lattice`
(`capability` has `list` and `review` only). So `upgradeExisting` reaches the platform **only** through a
hand-authored op payload, and building additive apply now would be scaffolding whose consumer, and whose
UX ("which package? chosen by whom? shown to the reviewer how?"), does not exist.

**And per-proposal packages are not a workaround — they are the sound granularity.** A capability does not
need to live inside an existing package to work: `resolveLensRef` (`build.go:571-590`) passes a bare NanoID
through verbatim precisely so a weaverTarget can bind a lens an *already-installed* package owns, and
Contract #8 §8.1's deterministic ids make `pkgmgr.LensID(package, canonicalName)` computable across
packages. What the package boundary buys is authority scope and an independent uninstall — both of which
are *better* governance for an AI-authored artifact when it is its own package than when it is buried in a
human one. "One package, one author" is the principle; the coverage rule is its enforceable form.

**Filed shelved with a named trigger**, per the deferred-tail rule — visible, but not in the ready queue,
because a row whose consumer does not exist is backlog inflation: a Lattice row *[Pkgmgr] No additive
(partial-Definition) package apply*, `🗄️ shelved`, whose trigger is **the first shipped producer that
emits `target.mode: "upgradeExisting"` for a NEW artifact** (a Studio "add to an existing authored
package" action, or a bridge adapter that targets one). Until that producer is designed, this fire's
refusal is the correct behaviour rather than a stopgap, and census A (§6) is the mechanical test for
whether the trigger has fired.

---

## 5. State-lifetime table

**There is no new stateful mechanism.** No registry, cache, latch, watch, or accumulated set is introduced
— the refusal is a pure function of one already-computed `diffSummary`, evaluated inside the call that
computes it, and discarded with it. This section exists to say so explicitly rather than to leave a reader
wondering which boundary was not thought about.

The one durable value this design *reads* is the installed package's `.manifest.declaredKeys`, whose
lifetime is unchanged: created by `InstallPackage`, rewritten by every `UpgradePackage`, tombstoned by
uninstall, and — this is the property the whole design leans on — **never narrowed by a capability apply
again**.

*(Amended 2026-08-22, during the build.)* That invariant is true only under the **coverage** predicate
§3.1 now specifies. Under the predicate as originally written — refuse iff the delta emits a tombstone —
it was false: a partial Definition dropping only keys the removal arm preserves (a retention-class holder)
or skips (a key already absent from KV) emitted nothing, was admitted, and rewrote `declaredKeys` to its
own keys alone. What enforces the invariant is the guard reading `old \ new` rather than the emitted
mutation list, plus `readDeclaredKeys` reporting a declaration it could not read whole so that the same
guard fails closed instead of mistaking a short `old` set for full coverage.

---

## 6. Executable censuses

Each ships with the command and the expected result, so the build's Phase-0 re-runs it mechanically.

**A — no live producer of `upgradeExisting`** (this is the census the fork rests on; brief it to
*falsify* the claim):

```bash
grep -rn 'newPackage\|upgradeExisting' --include='*.go' --include='*.js' --include='*.yaml' --include='*.json' \
  internal/ cmd/ packages/ | grep -v '_test\.'
```

The glob is deliberately **wider than the mode's own producers** — `packages/` is included because a
Starlark op script is a producing position no `internal/`+`cmd/` sweep can see, and `*.yaml`/`*.json`
because a manifest or fixture could carry one. Expected, today: **26 lines**, of which exactly **three**
are `newPackage` in a *producing* position — `internal/bridge/capability_author.go:43` (a package-level
const), `internal/bridge/fake_capability_author.go:117`, and
`cmd/loupe/web/js/logic/weaverauthor.js:261`. The other 23 partition as: 2 `switch` arms + 2 error strings
in `internal/pkgmgr/capabilityapply.go`, 3 in `packages/capability-author/ddls.go` (:297 an op-meta
*example* payload, :313 a sample-result string, :453 a comment — descriptor prose, not a submit), and 16
doc comments. **No line anywhere assigns `"upgradeExisting"`.** Count the *producing* positions, not the
matching lines — the raw count is dominated by prose, and a design that quotes the raw count has measured
the wrong unit. Brief this census to *falsify* the claim rather than confirm it: if it ever returns a
producing occurrence of `upgradeExisting`, §4's deferral premise is dead and the row it files is live.

**B — `Installer.Apply` non-test call sites:**

```bash
grep -rn "inst\.Apply(ctx" --include='*.go' cmd/ internal/ | grep -v '_test\.'
```

Expected before: 4 (`cmd/lattice-pkg/main.go:227,560`, `cmd/loupe/review.go:733`, `cmd/loupe/pkg.go:540`).
Expected after: **2** — the two source-authored, whole-Definition installs (`main.go:227`, `pkg.go:540`).

**C — plan-Definition readers outside `internal/pkgmgr`:**

```bash
grep -rn "plan\.Definition\|MaterializedDefinition()" --include='*.go' cmd/ internal/
```

Expected before: **9** — 5 in `cmd/` (2 `Apply` calls at `review.go:733` / `main.go:560`, and 3 assertion
lines in `cmd/bridge/capability_author_test.go:239-242`) and 4 in `internal/pkgmgr`'s own tests
(`capabilityapply_test.go:383,384,641,642`), which are in-package and keep direct field access after the
unexport. Expected after: **7** — the same 4 in-package lines, plus 3 in
`cmd/bridge/capability_author_test.go` now reading `MaterializedDefinition()`, and **zero occurrences
inside an `Apply` argument anywhere.**

**D — the lint gate is clean at landing:**

```bash
go run ./scripts/lint-conventions.go
```

Expected: zero findings of the new rule (which is why it ships blocking rather than warn-first).

---

## 7. Contract surface

**None changed.** Enumerated so the claim is checkable rather than asserted:

- **Contract #8 §8.6 (`UpgradePackage`)** — payload, mutation shape, per-key OCC and the step-8
  package-scope guard are all untouched. This design changes *which Definitions the capability path is
  allowed to build an UpgradePackage delta from*, entirely client-side of the op.
- **Contract #8 §8.1 (deterministic, version-independent entity keys)** — relied on (it is why `old` and
  `new` land on the same package key at all), not altered.
- **Contract #8 §8.2 (`InstallPackage` create-only)** — the `newPackage` arm is unchanged; `RefuseRemovals`
  is inert there by construction.
- **Contract #6 (Capability KV)** — untouched; no lens or projection changes.

`docs/contracts/08-package-install.md` currently carries **uncommitted edits from two other in-flight
designs** (`package-restore-design.md`'s §8.5 revive-only note, and the authority-minting admission
section). This design adds nothing to that file, so the ratifier can treat those edits as unrelated.

**One convention worth questioning, flagged rather than worked around.** `ApplyOptions`'s zero value means
"whole-Definition convergence, removals permitted". §11 prices inverting that to fail-closed and rejects it
on blast radius, but the honest reading is that the *option* is a second-best: the ideal shape is two
distinct entry points (`Converge` / `AddOnly`) rather than one operator with a mode flag. If a second
partial-Definition producer ever appears, that split is the right refactor, and this design's `RefuseRemovals`
is exactly the seam along which to make it.

---

## 8. Reconciliation with the existing mental model

***Didn't we already guard the capability apply?*** Yes — twice, and both guards are about the *target's
trust*, not the diff's blast radius. `platformProtectedPackages` (`capabilityapply.go:64-77`) refuses the
platform-trust package names in both modes, and `enforceAuthoredWeaverTargetScope` refuses a gap that would
dispatch a privileged op. Neither has any notion of *how many keys the apply removes*, and the deny-list's
own doc comment says vertical packages are deliberately outside it. The defect is precisely in the gap
those two guards were designed to leave open.

***Doesn't the Processor's step-8 guard catch this?*** No, and it is right not to. The guard authorizes an
`UpgradePackage` to touch exactly the package's own `priorDeclared` keys — every tombstone here is inside
that set. The guard is doing its job; the batch is a well-formed convergence of a package to a state the
submitter genuinely asked for. The error is upstream, in what the submitter asked for.

***Does this contradict `retention-class-key-custody`'s removal exemption?*** No — it generalizes its
reasoning to a new axis. That exemption says *this class of key may never be tombstoned by a diff, because
a destruction verb owns its removal*. This says *this class of submitter may never tombstone anything,
because it does not describe the package*. Both refuse a removal a diff would otherwise make silently; both
are enforced at the same seam, one key-class-scoped and one caller-scoped.

***Does this introduce new state?*** No (§5).

***Is a parallel fire touching the same seam?*** Two in-flight designs touch `internal/pkgmgr` and are
`📐 awaiting-Andrew`: `package-restore-design.md` (a `RestorePackage` op and a revive path). It touches `installer.go`'s find/occupancy machinery and edits
Contract #8 §8.5; it adds its **own** `RestoreOptions` struct and `Installer.Restore` rather than a field
on `ApplyOptions`, and it modifies neither `Apply` nor `capabilityapply.go`. It does, however, cite the
same guard-ordering rule this design relies on — `apply.go`'s "a guard whose refusal the real run would
raise must run before the dry-run return" (its §5.5) — so the two agree on where a refusal belongs, and a
builder should keep the line citations in both docs true if either shifts and `capability-proposal-bundles-design.md` (which makes the proposal carry an
artifact *list*; its §12 Fire A rewrites `CapabilityApplyPlanForProposal`'s artifact read). The bundles
design and this one **collide in one function** and compose cleanly in one direction: a bundle merges N
artifacts into one Definition, which is still partial, so `ApplyCapabilityPlan` and `RefuseRemovals` apply
unchanged and the mode-precondition switch in §3.3 is untouched by the list read. **Recommended sequencing:
this fire first** — it is smaller, it is the ★★★ safety fix, and the bundles design explicitly scopes
`upgradeExisting` out (`§12`: *"the Studio has always emitted `newPackage`, and §13.1's defect makes
`upgradeExisting` unsafe for the existing single-artifact path too"*), so landing this removes that caveat
from its own body rather than adding a merge conflict to it.

---

## 9. Migration / compatibility

**Runtime.** Nothing installed changes. No package version bump, no DDL edit, no lens edit, no bootstrap
key-count change — the whole fire is Go in `internal/pkgmgr`, two call-site migrations, one lint rule.
No stack needs `make down`.

**Behavioural.** The only behaviour that changes is `upgradeExisting`, which census A shows nothing emits.
A hand-authored `upgradeExisting` proposal that would previously have applied destructively now returns a
409 naming the keys. `newPackage` — every shipped producer — is byte-identically unaffected except in the
race window §3.2 describes, where it now refuses instead of tombstoning.

**Prose the ratification must rewrite in place** (banner-only folds leave the superseded shape fully
specified for the next reader to build from). Items 1, 2, 3 and 5 are **done** — executed at ratification,
2026-08-21. Item 4 is code and is a build scope line (§12, step 2):

1. `ai-authored-capabilities-design.md` §3.5 — *"or the diff `Installer.Upgrade` produces (for
   `upgradeExisting`)"* → the diff is produced only where the proposal covers the package.
2. Same doc, §8 Fire-2 brief — *"A vertical business-domain package … is unaffected — `upgradeExisting`
   there is exactly what Fire 2 shipped to allow"* → the deny-list decision stands on trust grounds; the
   capability it describes does not exist and is refused.
3. Same doc, test plan — *"A third applies an `upgradeExisting` and confirms the F-004 diff lands"* → is
   replaced by §10's refusal test. (This test was never built; §13.1's census found no test that installs a
   multi-entity package and then applies an `upgradeExisting` proposal at it. Had it existed, it would have
   found this.)
4. `internal/pkgmgr/capabilityapply.go:55-64` — the deny-list doc comment's *"upgradeExisting there is
   precisely what this fire exists to allow"* clause (§2's correction).
5. `capability-proposal-bundles-design.md` §13.1 and §12's `upgradeExisting` caveat — pointed at this
   design once it ratifies.

---

## 10. Test strategy

Every test below is owned by the fire's step named in §12; none is unowned.

**Unit — `internal/pkgmgr` (step 1).**

- `TestApply_RefuseRemovals_RefusesShrunkenDefinition` — install a multi-entity fixture Definition (≥2
  lenses, a role, a permission with a `grantedBy` link), then `Apply` a Definition carrying one of the
  lenses with `RefuseRemovals: true`. Assert: `errors.Is(err, ErrApplyWouldRemove)`; `errors.As` yields
  the typed error; `RemovedKeys` contains the dropped lens's meta key, the role, the permission and the
  link, sorted; **and Core KV is byte-unchanged** — this is the assertion that pins the defect, not the
  error string.
- `TestApply_RefuseRemovals_AdmitsCoveringDefinition` — the same package, re-applied with a Definition
  that covers every declared key and edits one lens body: succeeds, `Updated == 1`, `Tombstoned == 0`.
  The positive vector, so the refusal test cannot pass vacuously.
- `TestApply_RefuseRemovals_DryRunRefusesIdentically` — the preview refuses with the same typed error
  rather than returning a delta that cannot commit (the `enforceSecureColumnRetirement` placement rule).
- `TestApply_RefuseRemovals_RetentionHolderIsNotARemoval` — a dropped `vtx.retentionclass.*` holder is
  *preserved*, not tombstoned (`upgrade.go:558-575`), so it must not trip the refusal. The guard reads
  the emitted mutation list, not the raw `old \ new` set, and this pins that distinction.
- `TestApply_RefuseRemovals_ZeroValueStillConverges` — `ApplyOptions{}` over the same shrinking upgrade
  tombstones as before. The source-authored path is unchanged, asserted rather than assumed.

**Unit — the plan builder (step 2).**

- `TestCapabilityApplyPlan_UpgradeExisting_RequiresMovedVersion` — table-driven over the three §3.3
  refusals (absent `newVersion`; `newVersion` == installed; absent or mismatched `baseVersion`), plus the
  admitted case, each asserting the message names the offending field.
- `TestApplyCapabilityPlan_SetsRefuseRemovals` — an `upgradeExisting` plan at a real multi-entity package,
  applied through `ApplyCapabilityPlan`, refuses. This is the **end-to-end pinning test §13.1 found
  missing**, and it is what makes the whole design falsifiable: delete the guard and it fails.
- `TestApplyCapabilityPlan_NewPackageRaceRefuses` — the name becomes installed between plan build and
  apply: the in-place branch is taken and refuses, rather than tombstoning the squatter's keys.

**E2e — `packages/capability-author/apply_test.go` (step 2).** One case on the ephemeral stack, mirroring
`TestCapAuthor_Apply_UnknownPackage_Rejected`'s pattern: install a real multi-entity package, drive a
proposal to `approved` with `mode: upgradeExisting` at it, and assert the apply refuses and the package's
declared keys are all still live.

**Loupe (step 2).** A handler test asserting `ErrApplyWouldRemove` renders **409** (not 502) and that the
body carries the refusal message. `cmd/lattice-pkg`: assert `runApplyProposal` returns the error without
submitting `MarkCapabilityProposalApplied` — the silent-`applied` stamp is half the harm.

**Gates.** `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`, every
`scripts/lint-*.go`, and `go test ./internal/pkgmgr/ ./cmd/loupe/ ./cmd/lattice-pkg/ ./cmd/bridge/
./packages/capability-author/`. A full `go test ./...` is **not** required: `RefuseRemovals`'s zero value
preserves the shared default, so the blast radius is the packages listed.

---

## 11. Risks & alternatives considered

| Alternative | Why not |
|---|---|
| **Build additive apply now** (the row's `no-pattern:` prescription) | §4: zero live producers, and not sound without an origin stamp plus a removal verb. It would replace a loud destruction with a silent one on the next source-authored upgrade. Deferred with a named trigger, not dropped. |
| **Remove `upgradeExisting` from the capability path** (refuse the mode outright) | A few lines simpler, and it discards the case that is already correct: a proposal that covers the whole package — the natural "edit the lens and re-propose" flow for a proposal-owned package. It also leaves the mode alive in the DDL and stored on proposals while being unreachable, which is the shape that produced this defect. Coverage refuses exactly what is destructive and self-opens for what is not. |
| **Invert the default: `AllowRemovals`, fail-closed** | Correct in principle and rejected on blast radius: `ApplyOptions{}`'s zero value is depended on by two source-authored call sites and ~20 tests, and flipping a shared default's meaning is exactly the change that breaks unedited consumers. The structural load here is carried by unexporting `plan.Definition` (a partial Definition cannot reach `Apply` at all), not by the flag's default. Named as the right refactor if a second partial-Definition producer appears (§7). |
| **Dry-run first, refuse if `TombstonedKeys` is non-empty, then apply for real** | Needs no `pkgmgr` change at all — and puts a TOCTOU window inside a safety guard, with the delta recomputed between the check and the act. Refusing inside the call that computes the delta is atomic and strictly cheaper. |
| **Guard it in the Starlark at record time** | Not expressible: the script would have to know the target package's declared-key count, and the package's vertex key is a Go-side `entityNanoID(name, "package")` derivation with no Starlark equivalent, so it cannot even be named in `contextHint.reads`. Unlike `ValidateCapabilityArtifact` (whose verdict the DDL copies through, and which is therefore advisory), `Apply` is the code that *builds the batch* — the refusal there is a real bound, not a caller-supplied verdict. |
| **A lint gate instead of unexporting `Definition`** | A gate alone leaves `plan.Definition` as the thing an author's editor offers them. Structural first, gate as the backstop for the accessor — both ship (§3.5). |
| **`Force: true` so a same-version re-propose diff-applies** | Destroys Loupe's name+version recovery discriminator (§3.4). Refusing a same-version `upgradeExisting` gets the same outcome and keeps the discriminator sound by construction. |

**Residual risks, named.**

- **A version *downgrade* is still permitted** (a covering proposal declaring `0.1.0` over an installed
  `0.2.0`). `go.mod` vendors no semver comparator and `internal/pkgmgr` has none, so ordering versions
  means adding a comparator to serve one refusal on a population that is empty today. `baseVersion ==
  installed` (§3.3) already blocks the stale-apply harm; the recorded version going backwards is cosmetic
  and the author declared it. Not built; stated.
- **A covering re-propose that RENAMES the artifact is refused, correctly but perhaps surprisingly.**
  Entity keys are `(package, kind, canonicalName)`-deterministic, so changing a lens's `canonicalName`
  produces a new key and drops the old one — a remove-and-add, which the guard refuses. That is the right
  answer (the rename *is* a removal), but the refusal message will name a key the author thinks they
  edited rather than deleted, so the message's remedy clause matters. Not a rule to relax.
- **The guard governs removals, not body-blanking.** A capability Definition carries no `Depends` or
  `Description`, and the manifest body takes both from the Definition (`build.go:425-433`), so a
  *covering* apply over a package that declares either would blank it via an ordinary `update` — invisible
  to a tombstone-counting guard. Empty in practice: a covering capability apply implies a package whose
  whole content is one artifact, and every such package was created by a capability `newPackage` apply,
  which declared neither field. Stated rather than guarded, because guarding it means deciding which
  manifest fields a partial author may speak for — which is §4's additive-apply design, not this one.
- **The refusal's key list can be long.** The message truncates (first four keys, then `+N more`) while
  `RemovedKeys` carries the full sorted set in the typed error, so a console renders from data.

---

## 12. Decomposition for the Steward

**One fire, two ordered steps.** They are not two fires: step 1 alone adds an option nothing sets — dead
scaffolding by the fire's own test — and step 2 is what realizes the value.

**Step 1 — `internal/pkgmgr`: the refusal.** `ApplyOptions.RefuseRemovals`; `ErrApplyWouldRemove` +
`ApplyWouldRemoveError` (mirroring `DeclaredKeysOccupiedError`, `installer.go:711-751` — read its doc
comment before writing this one); enforcement in `Apply` immediately after `enforceSecureColumnRetirement`
and before both the empty-delta and `DryRun` returns. **Registration sites — enumerate by grepping an
existing sentinel, not from this list:** `grep -rn ErrDeclaredKeysOccupied --include='*.go' cmd/ internal/`
returns exactly one error→status mapper outside `internal/pkgmgr` (`cmd/loupe/pkg.go:379`), which gains
the new sentinel in its 409 arm; `cmd/lattice-pkg` returns the error verbatim and needs no arm. Five unit
tests (§10).

**Step 2 — the seam: the capability path cannot forget.** `CapabilityApplyPlan.Definition` → unexported +
`MaterializedDefinition()`; `Installer.ApplyCapabilityPlan`; §3.3's three `upgradeExisting` preconditions;
migrate `cmd/loupe/review.go:733` and `cmd/lattice-pkg/main.go:560`; one `cmd/bridge` test assertion
migrates to the accessor; the `lint-conventions` rule; the plan-builder, e2e, Loupe and CLI tests (§10);
the five prose corrections in §9.

**Review depth.** Step 2 is **posture-changing** — it moves an authorization-adjacent seam and narrows a
previously-ratified capability — so it takes the full adversarial pass. Step 1 is a guarded refusal inside
one function with a positive-vector test; the Steward sizes it normally
(`agents/steward/SKILL.md` §4).

**Phase-0 for the fire:** re-run censuses A, B and C (§6). If A returns a *producing* occurrence of
`"upgradeExisting"`, stop — §4's deferral premise has changed and the additive row is live, not deferred.

---

## 13. The §2 reflex walk, and what it caught

Run against this draft before any adversarial pass, one reflex at a time, per the skill's own instruction
that the checklist is not a substitute for the pass and must not be recalled from memory. Recorded because
two of the three findings were mine and would otherwise be invisible to a reviewer of the finished text.

1. **"Verify the machinery can BE reshaped" — one real defect.** The draft justified refusing a
   same-version `upgradeExisting` by claiming `Apply` would otherwise take its skip branch and let
   `cmd/lattice-pkg` stamp `applied` over an artifact that never landed. `apply.go:150`'s skip condition
   is `existing.Version == def.Version && !opts.Force && !opts.RequireInstalled` — and the same design
   sets `RequireInstalled: true`, so the skip is already defeated and the stated reason could not fire.
   The rule survives on the *independent* reason (§3.4's recovery discriminator), and both §3.2 and §3.3
   were rewritten: the silent-`applied` harm is real **today** and is closed by `RequireInstalled`, not by
   the version rule. A guard justified by a branch it makes unreachable is the inert-guard failure with
   the sign flipped.
2. **"A census you produce sizes the work — check what the grep counts."** Census A's stated expected
   result was written from the narrow first sweep. Re-running it over the widened glob returned **26**
   lines, not the 14 the draft claimed, and `packages/` — included precisely because a Starlark op script
   is a producing position no `internal/`+`cmd/` sweep can see — contributes three occurrences that a
   quick reader would mistake for producers (they are DDL descriptor examples). §6 now states the
   partition and says explicitly which unit to count.
3. **"A one-clause predicate is a claim that the set has one shape."** The guard is one clause (*any
   tombstone in the emitted delta*), so §3.1 gained the seven-row state table with an explicit **outcome**
   column, which surfaced two rows the clause decides silently: a dropped `vtx.retentionclass.*` holder
   produces no tombstone and is correctly admitted, and an **already-tombstoned** declared key *is*
   re-emitted as a tombstone and is therefore refused — a no-op removal, refused deliberately so the
   guard's meaning depends on what the submitter described rather than on KV liveness.
4. **"A repo comment may be wrong."** `capabilityapply.go:55-64`'s *"upgradeExisting there is precisely
   what this fire exists to allow"* is true about the deny-list's intent and false about the platform's
   behaviour; §2 records the correction and §9 lists the edit rather than quoting the comment as a fact.
5. **"Check the other in-flight designs."** `package-restore-design.md` §5.5 turned out to reason about
   the *same* `apply.go` guard-ordering rule this design relies on. It adds its own `RestoreOptions` and
   `Installer.Restore` rather than a field on `ApplyOptions`, so there is no collision — but §8 now says
   so from having read it, not from the file list.

Reflexes that did not apply and why, so a reviewer can check the walk was complete rather than
convenient: no new Core-KV read (all reads are pre-existing and submitter-side), no retraction or
ordering token, no cardinality change, no container-level default, no auto-recovery loop, no new NATS
API call (so no `natsperm` envelope to check), no handed-down measurement, and no new stateful mechanism
(§5).

---

## 14. Fire brief (build note, 2026-08-22)

Compiled at selection by the Lattice Steward (fire branch `claude/great-lamport-bj40v0`) from two read-only
scouts. Anchors below are **live line numbers re-verified now**, not the design's.

### 1. Scope sentence (verbatim, §12)

> **One fire, two ordered steps.** Step 1 — `internal/pkgmgr`: the refusal (`ApplyOptions.RefuseRemovals`;
> `ErrApplyWouldRemove` + `ApplyWouldRemoveError`; enforcement in `Apply` after
> `enforceSecureColumnRetirement` and before both the empty-delta and `DryRun` returns). Step 2 — the seam:
> `CapabilityApplyPlan.Definition` unexported + `MaterializedDefinition()`; `Installer.ApplyCapabilityPlan`;
> §3.3's three `upgradeExisting` preconditions; migrate both capability call sites; the `lint-conventions`
> rule; the tests of §10; the §9 item-4 prose correction.

**Green bar:** `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`, every
`scripts/lint-*.go`, and `go test ./internal/pkgmgr/ ./cmd/loupe/ ./cmd/lattice-pkg/ ./cmd/bridge/
./packages/capability-author/`.

### 2. Censuses re-run live — all three match the design

| Census | Expected (§6) | Measured | Verdict |
|---|---|---|---|
| A — producers of `upgradeExisting` | 26 lines, **zero** producing occurrences | 26 lines; zero `= "upgradeExisting"` / `: "upgradeExisting"` anywhere | §4's deferral premise **holds** |
| B — `inst.Apply(ctx` non-test | 4 → 2 after | 4: `loupe/review.go:733`, `loupe/pkg.go:540`, `lattice-pkg/main.go:227`, `:560` | matches |
| C — plan-Definition readers | 9 → 7 after | 9, exactly as enumerated | matches |

### 3. Verified touch-list

| File | Anchor (live) | Edit |
|---|---|---|
| `internal/pkgmgr/apply.go` | `ApplyOptions` :11-23 (`Force`, `DryRun`, `RequireInstalled`) | add `RefuseRemovals` |
| | `Apply` :123; `findInstalledPackage` :132; skip branch :146; `computeDeltaAgainst` :~176; `enforceSecureColumnRetirement` :192; empty-delta :199; `DryRun` :205 | insert the guard at :198 (after :192-197, before :199) |
| `internal/pkgmgr/upgrade.go` | `diffSummary` :305-367 — **`tombstoned` is a COUNT; there is NO key list** | add `oldKeyCount`/`newKeyCount` ints, set in `diffManifest` |
| | `diffManifest` :445 (`oldKeys []string, newOps []installMutation`) | set the two counts |
| | tombstone emission — **exactly one site, :581** (removal arm :532-589) | unchanged; the guard reads the emitted `mutations` |
| | retention exemption :557-577 (`continue`, no mutation) · absent-from-KV skip :551-556 (`continue`) | unchanged — both are already "not a removal" by emission |
| `internal/pkgmgr/installer.go` | `ErrDeclaredKeysOccupied` :724 · `DeclaredKeysOccupiedError` :734-746 | precedent only |
| `internal/pkgmgr/capabilityapply.go` | `CapabilityApplyPlan` :18-22 · deny-list comment :55-77 · `CapabilityApplyPlanForProposal` :134-228 · `newVersion` default :182-188 · mode switch :199-210 | unexport `definition`; accessor; `ApplyCapabilityPlan`; three preconditions; §9 item-4 comment fix |
| `cmd/loupe/pkg.go` | `packageApplyStatus` :375-386 — the **single** error→status mapper outside `internal/pkgmgr` | add `ErrApplyWouldRemove` to the 409 arm |
| `cmd/loupe/review.go` | apply :733; status mapping :735; recovery discriminator :711-721; its doc :862-878 | migrate to `ApplyCapabilityPlan`; recovery branch unchanged (§3.4) |
| `cmd/lattice-pkg/main.go` | `runApplyProposal` :528-582, apply :560, `submitMarkApplied` :569 | migrate; assert no mark-applied on refusal |
| `cmd/bridge/capability_author_test.go` | :239-242 | → `MaterializedDefinition()` |
| `scripts/lint-conventions.go` | pattern `var (…)` block :222-488 | one blocking rule (§3.5) |
| `packages/capability-author/apply_test.go` | `TestCapAuthor_Apply_UnknownPackage_Rejected` :371-382 | mirror for the refusal e2e |

**Rot found:** none material. `getCommitted` is *called* at :460 (defined :1035), not :459; the skip
condition is at :146, not :150; the retention arm spans :557-577, not :558-575.

**One design detail the code does not supply as written.** §3.1 gives `ApplyWouldRemoveError` a
`RemovedKeys []string` and two counts, and §3.1's prose says the guard "reads `sum.tombstoned` and the
mutation list". `diffSummary.tombstoned` is a **count only** — no key list — and neither key-set size
reaches `Apply`. Resolution (Winston, decided at Phase 0, no scope change): derive `RemovedKeys` from the
emitted `mutations` (`Op == "tombstone"`, sorted) — sound because the census above shows **one** tombstone
emission site in the whole package, the removal arm — and carry the two sizes as new `diffSummary` fields
set inside `diffManifest`, where `oldKeys`/`newOps` both already are. Both counts include the package root
and manifest aspect; the field doc says so rather than implying entity counts.

### 4. Increment order

1. **Inc 1 — `diffSummary` key counts.** Two ints, set in `diffManifest`. Green: `go test ./internal/pkgmgr/`.
2. **Inc 2 — the refusal (step 1).** `RefuseRemovals`, the sentinel + typed error, the guard, five §10 unit
   tests. Green: `go test ./internal/pkgmgr/ -run 'TestApply'`.
3. **Inc 3 — the seam (step 2).** Unexport + accessor + `ApplyCapabilityPlan` + the three preconditions +
   both call-site migrations + the `cmd/bridge` assertion. Green:
   `go test ./internal/pkgmgr/ ./cmd/loupe/ ./cmd/lattice-pkg/ ./cmd/bridge/`.
4. **Inc 4 — the gate + the e2e + the prose.** `lint-conventions` rule, the capability-author e2e, the
   Loupe/CLI handler tests, the §9 item-4 comment. Green: `go run ./scripts/lint-conventions.go` (zero
   findings of the new rule) + `go test ./packages/capability-author/` + the full green bar.

### 5. In-scope gotchas

**Standing checklist** — the two that bite here: **#2 every census is a premise** (re-run, done above; and
the guard's predicate is written over the enumerated state table of §3.1, not one clause over a multi-shape
set); **#3 a negative test needs its positive vector proven first** (§10's `AdmitsCoveringDefinition` and
`ZeroValueStillConverges` are that vector — write them, then prove each refusal by reverting the guard and
watching its test fail). #1 does not apply (§5: no new state). #5 does not apply (no new deterministic key).
#4 applies only in the weak sense that nothing is being removed. #6 applies to the
`DeclaredKeysOccupiedError` mirror — read its doc comment before copying its shape.

**`docs/components/pkgmgr.md` dossier — the four entries this fire trips, verbatim:**

- **A new failure mode is not shipped until every surface that renders it says the right thing** — the
  message, the error's own shape, and each status/UX mapping downstream. `ErrDeclaredKeysOccupied` fell
  through `cmd/loupe`'s default arm to **502**, the code that UI's own front end treats as a transport blip
  worth retrying, for a state that fails identically forever; and the two occupancy buckets were carried
  only in prose, so a review got a real defect — every tombstoned key also reported live — past the FULL
  suite by lowercasing one word. Check: a new sentinel is grepped across every `errors.Is` status/UX
  mapping in `cmd/` before it ships; a distinction the code MAKES is carried in fields and asserted from
  fields, never scraped from the rendered message. **Adding the sentinel to the map proves nothing on its
  own**: the uninstall path listed `ErrNotInstalled` in the 409 arm and a table test went green while the
  producing call site returned a BARE `fmt.Errorf`, so the real request still 502'd. The row must be driven
  from the entry point that produces the error, not asserted against the mapping function in isolation.
- **A refusal's stated remedy must not be a move that defeats the gate — and "the verb exists and is
  granted" is NOT evidence the remedy works.** Check: trace the remedy to the OUTCOME it promises, through
  the projection or consumer that delivers it, not to the existence of its first step; and a remedy printed
  for every caller must be qualified per caller — name the states it is false in. A refusal that renders a
  command feeds that exact rendered string through the real parser in a test.
- **A local gate run and CI's gate run do not see the same tree** — every `scripts/lint-*.go` reads its scan
  set from git, so what the author has not committed is not what the gate judged. Check (mechanized):
  `lint-conventions` names the untracked `.go` files it did not scan. **Run the gates after committing.**
- **An injected dependency held in a nil-able field silently disables the gate it feeds** … second sighting,
  in the thinner shape: the RULE was covered twelve ways and the line DELIVERING it was covered zero.
  Mandated test shape here: the test that pins the guard drives the **real entry point**
  (`Apply` / `ApplyCapabilityPlan`), never the predicate helper in isolation — deleting the guard line from
  `Apply` must turn a test red.

**Consequences for this fire, stated so they are checkable:** (a) `ErrApplyWouldRemove` goes in
`packageApplyStatus`'s 409 arm **and** the Loupe test drives `reviewCapabilityApply` end-to-end, not
`packageApplyStatus(err)` directly; (b) `RemovedKeys` is asserted from the typed error's **field**, never by
substring-matching the message; (c) the remedy clause names `newPackage` — the e2e must show that a
`newPackage` proposal for the same content actually applies, so the remedy is traced to its outcome;
(d) commit before running `lint-conventions`.

`docs/components/loupe.md` carries no dossier; `_packages.md`'s three entries are package-authoring
classes that this Go-only fire does not trip (no guard wrapper, no shared-vertex repoint, no cross-package
type guard).

### 6. Adjacent finds

None out of scope surfaced at Phase 0. §9 item 4 (the `capabilityapply.go` deny-list comment) is **in**
scope, not a find. The `📐 needs designer pass` additive-apply row (§4) is already filed `🗄️ shelved` with
its named trigger, and census A confirms the trigger has **not** fired.

### 7. Non-goals (drift fence)

Additive/partial-Definition apply (§4, shelved). Inverting `ApplyOptions`'s zero value (§11). A semver
comparator for the downgrade residual (§11). Any change to `cmd/loupe/review.go`'s recovery discriminator
(§3.4), to the Processor's step-8 guard, to `UpgradePackage`'s payload, or to any `docs/contracts/*` file.
No package version bump, no DDL/lens edit, no bootstrap change.

### Scope-diff gate — PASS

Every touch in part 3 traces to a §12 step. Part 4 narrows (Inc 1 splits out the mechanical counts) and
never widens. Declared dependencies re-verified both ways: `retention-class-key-custody`'s guard-placement
precedent **is** load-bearing (it fixes where the guard sits); `package-restore-design.md` is declared
adjacent and confirmed non-colliding (it adds `RestoreOptions`/`Installer.Restore`, touching neither `Apply`
nor `capabilityapply.go`); `capability-proposal-bundles-design.md` is unbuilt, so no live coupling. No
unlisted load-bearing dependency found.
