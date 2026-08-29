# Capability-proposal install receipt — binding a proposal to the install it actually produced

**Status: ✅ Winston-ratified — build-ready (2026-08-29).** Shape resolved by the designer triage of
2026-08-27 ([lattice-designer-triage-2026-08-27.md](../../docs/reviews/lattice-designer-triage-2026-08-27.md)
§3), which dissolved the row's `📐 needs designer pass` label to `📋 ready`: no platform primitive is
missing. This doc restates that resolved shape as the build's design of record and carries the fire brief.

Board row: `[Loupe] A newPackage proposal is closed over a same-named package it never wrote`
(`backlog/lattice.md`, component-maintenance table) — ★★, M.

## 1. The defect

`targetInstall` (`cmd/loupe/review.go:955`) answers one question — *did THIS proposal's install
commit?* — and it is the single source of that answer for **both** halves of the two-commit apply:

- the apply endpoint's 409 (`review.go:753`), which tells an operator "already installed … close it
  with mark-applied rather than re-applying", and
- the mark-applied recovery endpoint (`review.go:1046`), which relays
  `MarkCapabilityProposalApplied` and stamps a durable `appliedAs` link into the named package.

It answers it by a heuristic: a live `vtx.package.*` whose `.manifest.name` equals the proposal's
`target.packageName` **and** whose installed version equals `targetInstallVersion(cols)`. Version
matching already carries the load the in-code comment describes — it is what keeps a never-applied
`upgradeExisting` proposal from reading as recoverable. What neither half can distinguish is
**provenance**: for a `newPackage` proposal, a package at that name and version installed by anything
else — another proposal, an operator's `lattice-pkg install`, a second proposal declaring the same
target — satisfies the predicate exactly. The proposal is then closed over an artifact it never
wrote, with `review.state=applied`, an `appliedAs` link to that foreign package, and an
`appliedByOp` audit pointer of `recovered:<name>@<version>`.

The path is **live**: the Studio's `SubmitCapabilityProposal` is operator-granted and ungated (the
dormant `BRIDGE_CAPABILITY_AUTHOR` flag gates only the model-backed producer).

**No primitive is missing.** The durable receipt is computed and discarded. `Installer.Install`
submits the op and reads `reply.Status` alone (`internal/pkgmgr/installer.go:213–223`); the reply's
`RequestID` and `OpTrackerKey` — the Processor's own Contract #4 record that *this* commit happened —
are dropped on the floor. `submitUpgradeOp` (`internal/pkgmgr/upgrade.go:262`) discards the whole
reply, returning only `error`. Downstream, the "audit pointer" both apply paths stamp is a *string
reconstructed from the same name+version* the heuristic already failed to distinguish
(`review.go:781`, `cmd/lattice-pkg/main.go:566`).

## 2. The shape

Three moves, in dependency order.

1. **Thread the real receipt.** `InstallResult`, `UpgradeResult` and `ApplyResult` carry the
   Processor reply's `RequestID` and `OpTrackerKey`. `submitUpgradeOp` returns the reply.
2. **A narrow new op stamps it durably.** `RecordCapabilityInstallReceipt`, added to the
   `capabilityproposal` DDL's `PermittedCommands`, writes a **no-TTL, create-only** aspect
   `vtx.capabilityproposal.<id>.install` = `{packageKey, installRequestId, opTrackerKey,
   recordedAt}`. It mirrors `MarkCapabilityProposalApplied`'s grant shape and re-uses its three
   guards verbatim (approved-only `.review`; `.target.packageName` cross-checked against the live
   `<packageKey>.manifest`; `vtx.package.<id>` key shape). Both apply paths submit it **immediately
   after the apply commits and before** `MarkCapabilityProposalApplied`.
3. **`targetInstall` reads the receipt first.** A live `.install` aspect names the package key
   directly; the name+version heuristic stays as the legacy fallback for proposals that predate the
   receipt or whose receipt op did not land.

### 2.1 Why create-only, and what the write-once key buys

The aspect is a **create**, never an update, so the commit batch's `CreateOnly` conditioning
(Contract #3 §3.2) rejects a second, *different* receipt for the same proposal — one deterministic
key, one writer. A redelivered *identical* submission is collapsed earlier by the Contract #4
requestId tracker. A proposal therefore binds to exactly one install, permanently, and the binding
is written by the same actor that observed the install commit.

### 2.2 The crash window degrades to today, never below it

Three commits now sit between "approved" and "applied": the install/upgrade, the receipt, the
mark-applied flip. A failure of the **receipt** op is not a failure of the apply — the package is
live and the proposal is still closable. So the receipt submission is **non-fatal**: its failure is
reported in the resumable error the apply already returns, and recovery falls back to the
name+version heuristic — i.e. exactly today's behaviour, which is the accepted class the existing
two-commit boundary already lives with. The receipt strictly *adds* precision; it never gates.

### 2.3 Read posture

The op declares `contextHint.reads` = `{proposalKey}.review`, `{proposalKey}.target`,
`{packageKey}.manifest` — the same three `MarkCapabilityProposalApplied` declares, absence of any of
which is a correctness error (class (a), not `optionalReads`). It reads nothing else: it does not
read `.install`, because create-only conditioning, not a read-before-create branch, is what makes the
write once-only.

### 2.4 P5

`targetInstall`'s new read of `vtx.capabilityproposal.<id>.install` is a Core KV read from
`cmd/loupe` — Loupe is P5's named console-inspector exception, and the very same function already
reads the `vtx.package.` subtree under that exception. No other `cmd/<app>` gains a Core KV read.

## 2.5 The receipt alone does not close the row — the no-receipt refusal does

**Added 2026-08-29, during the build, on a cold-review finding.** §2's three moves are the triage's
ratified shape, and they are necessary but **not sufficient for the board row's own headline case.**
The row is *"a `newPackage` proposal is closed over a same-named package it never wrote."* That state
is by definition a proposal **whose own apply never ran** — so it has no receipt — so `targetInstall`
takes the unchanged name+version fallback and produces the identical wrong close. The receipt is
inert in precisely its own defect's scenario; what §2 actually buys is precision in the *post-apply
recovery* window (install committed, mark-applied did not). Reachability is not exotic:
`targetInstallVersion` defaults to `"0.1.0"` when a proposal declares no `newVersion`, colliding with
the most common first version a hand-installed package carries.

So the fire also lands the enforcement the title implies. The machinery already knew the right answer
and said the opposite: `ApplyCapabilityPlan` refuses a `newPackage` whose name was claimed before the
apply ran (`ErrPackageNameClaimed` — *"This proposal's artifact did NOT land: do not mark it
applied"*), while the console's 409 pre-empted it with "close it with mark-applied", purely because
it could not tell a half-commit from a foreign install. Now it can. **For `newPackage` only**: no
receipt + a live package at the target name and version ⇒ mark-applied **refuses**, and the apply
endpoint stops advertising `resumable`. `upgradeExisting` is untouched — its package is installed
before the apply by definition, so a live same-named package carries no provenance signal there, and
`ValidateCapabilityApplyTarget`'s version preconditions already own that mode.

The cost is deliberate and fail-closed: a `newPackage` proposal that genuinely half-committed *before
receipts existed* can no longer be closed from the console. The deliberate path remains — submitting
`MarkCapabilityProposalApplied` via `lattice-pkg` with an explicit `packageKey`, an authorized act
rather than a console guess. That is the right trade, because `review.state = applied` plus the
`appliedAs` link is a **write-once, unrecoverable** transition: a wrong close cannot be undone, a
refusal can.

## 3. Non-goals (the drift fence)

- **No read-model / lens column** for the receipt. Loupe reads the aspect from Core KV under its P5
  exception; adding a `capability-proposals` lens column would widen the fire into DDL + refresh for
  a value only this one decision consumes.
- **No change to `MarkCapabilityProposalApplied`'s** `appliedAs` link or its `review.state`
  transition. **Amended 2026-08-29, during the build:** this clause originally covered that op's
  *guards* too, and the build falsified it. Mirroring its name cross-check into the receipt op
  surfaced that mark-applied reads `target.packageName` **raw** while the installer folds the
  declared name with `.strip()` (`proposal_package_name`'s own documented reason for existing). A
  proposal whose target name carried surrounding whitespace would be installable but never
  closable: the install records the folded spelling and the raw comparison always mismatches.
  **Reachability, measured rather than asserted:** that state cannot be reached through the op path
  at all — `RecordCapabilityProposal` and `SubmitCapabilityProposal` both fold `packageName` through
  the same helper when they write `.target`, so the regression test has to patch Core KV directly to
  construct it. The raw read was latent, not live: a guard that disagreed with every writer feeding
  it. Building the receipt op to the raw form would have preserved this non-goal by propagating that
  disagreement into new code; building it folded and leaving mark-applied alone would have shipped
  two close-ops that differ on the same name. Both ops now use `proposal_package_name`. It grants no
  new reach — an author who wants the folded name can always submit it directly — so it is a
  narrowing of divergence, not a widening of the guard.
- **No change to `upgradeExisting` apply semantics**, `findInstalledPackageByName`, or
  `CapabilityApplyPlanForProposal`'s `newPackage`-vs-live-catalog guard.
- **`reply.Revisions` is NOT threaded.** The triage named it alongside `RequestID`/`OpTrackerKey`,
  but nothing in this fire consumes a per-key revision map, and a field carried for an assumed future
  consumer is a seam nobody owns. Narrowing, not substitution — the two receipt fields the aspect
  actually stores are threaded.

## 4. Fire brief (build note, 2026-08-29)

Compiled Phase 0 from two read-only scouts (`haiku`) plus lead verification of every load-bearing
`file:line` below. Clone deepened (`git fetch --deepen=400`, 727 commits) before any history-derived
claim.

### 4.1 Scope sentence (verbatim, triage §3)

> "(1) thread the reply through `ApplyResult`; (2) immediately after apply, stamp `{packageKey,
> installRequestId}` as a **no-TTL aspect on the proposal's own vertex** via a narrow op mirroring
> `MarkCapabilityProposalApplied`'s grant shape; `targetInstall` reads that first, name+version stays
> the legacy fallback."

**Green bar:** `go build ./...` · `make vet` · `golangci-lint run ./...` ·
`STRICT=1 go run ./scripts/lint-conventions.go` · `STRICT=1 go run ./scripts/lint-package-standard.go` ·
`DIFF_BASE=$(git merge-base main HEAD) go run ./scripts/lint-package-version.go` — **the bare
invocation is falsely green for a whole class** (it diffs the working tree only, so it cannot see a
committed increment; CI sets `DIFF_BASE` and compares the range, which is how `internal/pkgmgr/`
changing obliges every package declaring `ReadGrantDomains` to bump) · `go test ./internal/pkgmgr/... ./packages/capability-author/... ./cmd/loupe/... ./cmd/lattice-pkg/...` ·
full `go test ./... -p 4` with `POSTGRES_TEST_DSN` set · `make verify-kernel`.

### 4.2 Verified touch-list (`file:line` read live at head)

| File | Anchor | Change |
|---|---|---|
| `internal/pkgmgr/installer.go` | `213` submit, `217–223` `switch reply.Status` reads only `.Status`; `661` `InstallResult` | capture `reply.RequestID` / `reply.OpTrackerKey` into `InstallResult` |
| `internal/pkgmgr/upgrade.go` | `262` submit inside `submitUpgradeOp`, which returns `error` only; `30` `UpgradeResult` | return the reply; carry the two fields |
| `internal/pkgmgr/apply.go` | `33–117` `ApplyResult`; constructions at `155`, `172`, `311`, `330` | add the two fields; populate on the install (`330`) and upgrade (`172`) arms |
| `packages/capability-author/ddls.go` | `159` `PermittedCommands`; `256–264` `InputSchema`; `279` `FieldDescription`; `841–923` the `MarkCapabilityProposalApplied` branch (the precedent) | new `RecordCapabilityInstallReceipt` branch + schema/field/example entries |
| `packages/capability-author/permissions.go` | `77–82` `PermissionSpec`; `99` `OpMetas` entry | mirror both for the new op |
| `packages/capability-author/package.go` | `143` `Version: "0.12.2"` | bump to `0.13.0` |
| `packages/capability-author/manifest.yaml` | `2` `version: 0.12.2` | bump to `0.13.0` (must match) |
| `cmd/loupe/review.go` | `753–764` the 409; `775–812` apply's close; `840–847` `recoveredInstallRequestID`; `955–972` `targetInstall`; `1060` recovery's pointer | submit the receipt after apply; `targetInstall` reads `.install` first |
| `cmd/lattice-pkg/main.go` | `560–580` apply-proposal's close; `605–620` `submitMarkApplied` | submit the receipt after apply |

**Rotted citations:** none. The triage's claim that `InstallPackage` "drops the reply's
`RequestID`/`OpTrackerKey`/`Revisions` after `.Status`" is exact at `installer.go:217`.
`internal/pkgmgr/capabilityapply_test.go` **does** exist (scout 1 reported it not found — corrected
by lead check); the capability-apply behaviour is additionally pinned in
`packages/capability-author/apply_test.go`.

### 4.3 Precedents to mirror

- **The op, end to end:** `MarkCapabilityProposalApplied` — DDL branch `ddls.go:841–923`,
  `PermittedCommands` `ddls.go:159`, `PermissionSpec` `permissions.go:77–82`, `OpMetas`
  `permissions.go:99`. Copy its guard sequence literally.
- **No-TTL aspect create:** `make_aspect` / the `{"op": "create", … "document": {"class", "isDeleted":
  False, "vertexKey", "localName", "data"}}` literal used throughout `ddls.go`. Core KV writes carry
  no TTL field; nothing in `packages/` sets one.
- **Op-name declaration:** `scripts/lint-conventions.go:677` `opNameShape` —
  `// op-name: (submits|receives|policy) <why>` on the submitting line. Precedent:
  `cmd/loupe/review.go:792` and `1069`; `internal/bridge/dispatch.go:87–94`.
- **Read-posture annotation:** the `# read-posture: (a) declared in contextHint.reads by the …
  dispatcher` comments already on `ddls.go`'s three `kv.Read`s in the mark-applied branch.

### 4.4 Increment order

| # | Increment | Tier | Green check |
|---|---|---|---|
| 1 | Thread `InstallRequestID`/`OpTrackerKey` through `InstallResult`/`UpgradeResult`/`ApplyResult`; `submitUpgradeOp` returns the reply | `sonnet` (mechanical) | `go test ./internal/pkgmgr/...` |
| 2 | `RecordCapabilityInstallReceipt` op — DDL branch, permissions, OpMetas, schema, version bump; tests for each guard + the create-only second write | `opus` (new durable state + a new enforcement point) | `go test ./packages/capability-author/...` + `go run ./scripts/lint-package-version.go` |
| 3 | Consumers: both apply paths submit the receipt (non-fatal); `targetInstall` prefers it | `opus` (posture change on the recovery decision) | `go test ./cmd/loupe/... ./cmd/lattice-pkg/...` |
| 4 | Full gates + 3-layer cold adversarial review (capability-plane ⇒ full depth regardless of size) | `opus` reviewers, never the implementer | the §4.1 green bar entire |

### 4.5 In-scope gotchas

`packages/` edit ⇒ **bump `manifest.yaml` version AND the `Version` constant** and they must match
(`lint-package-version.go`). New op name ⇒ **`op-name:` declaration at every `cmd/`+`internal/`
submit site** (`lint-conventions.go`). Capability-plane change ⇒ **full 3-layer review regardless of
size** (steward §4). `go test ./...` without `POSTGRES_TEST_DSN` is **falsely green** (REMOTE.md §3)
— Postgres is up on `:5433` for this fire. Build-tagged harnesses do not compile under `go test
./...`; this fire changes no engine/service interface, so none is reached — re-verify if inc 1 widens.

**"Review keeps catching" dossiers, copied in verbatim for the touched components:**

- *pkgmgr* (`docs/components/pkgmgr.md`): canonicalName and the instance key segment are different
  namespaces · an injected dependency held in a nil-able field silently disables the gate · a
  refusal's stated remedy must not be a move that defeats the gate.
- *packages* (`docs/components/_packages.md`): a declared sensitive read is decrypted BEFORE the
  script runs · a column's ABSENCE and its declared FALSE value are different inputs · a
  shared-vertex repoint needs a content-and-revision gate against every other writer.
- *processor* (`docs/components/processor.md`): a gate's negative test must first prove its positive
  vector reaches the gate · a tombstone retains the prior document, so a reader that does not filter
  `isDeleted` sees a revoked declaration as live · declaring one operationType on N sibling classes
  makes `ClassForCommand` drop it · a gate that consults the in-flight batch must resolve LAST-write-wins.

**Standing checklist** (fire-brief-template §"The standing checklist"): #1 new state needs a lifetime
— written out in §2.1/§2.2 above; #3 every fix proven by reverting it; #5 one deterministic key, one
writer — the create-only conditioning is the arbitration; #6 precedent may carry debt — the mirrored
mark-applied guards were re-read against Contract #1/#3, not copied on trust.

### 4.6 Adjacent finds

Both defects the build surfaced were fixed in this run (steward §4 — what a fire discovers, this run
fixes); neither was filed.

- **`MarkCapabilityProposalApplied` compared `target.packageName` raw** while the installer folds it.
  Fixed, with the non-goal amended where it stood (§3) and a paired padded/unpadded regression test.
- **`cmd/lattice-pkg`'s rejection-reporting line dereferenced `reply.Error` unguarded**, panicking
  the CLI on the one path whose job is to report a failure. Fixed via the nil-safe accessors, tested
  against nil reply / nil Error / empty Error plus a populated-Error positive vector.


- `recoveredInstallRequestID`'s `"recovered:"` prefix (`review.go:846`) exists precisely because the
  reconstructed pointer is a fiction. With a real receipt the recovery path can now stamp the
  **observed** `installRequestId`. **Absorbed into inc 3** — not filed.
- The happy-path `installRequestID` string (`review.go:781`, `main.go:566`) is likewise reconstructed.
  **Absorbed into inc 3.**

### 4.7 Scope-diff gate — PASS

Every touch traces to the scope sentence; the brief **narrows** it twice (no `Revisions` field, no
lens column) and widens it nowhere. `cmd/lattice-pkg` is not named in the sentence but is the second
implementation of "immediately after apply" — omitting it would leave an apply path that produces no
receipt, i.e. the sentence unimplemented on that path. Declared dependencies re-verified both ways:
no dependency on any unbuilt row; nothing this fire needs is blocked.

## 5. Checkpoint

Single-fire item. Landing shape: **hold the fire branch and merge once complete** — `main` never sees
a partial (inc 2's op is inert without inc 3's submitters, and inc 3 does not compile without inc 1).
