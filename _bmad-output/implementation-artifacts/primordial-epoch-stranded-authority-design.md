# Stranded primordial-epoch authority — the orphan class the kernel census cannot reach

**Status:** ✅ **Winston-ratified — Fire 1 build-ready** (detector + verify gate).
**Fire 2 (the reconciliation verb) is 🔭 flagged for Andrew** — §7.

**Component:** `internal/bootstrap` (+ `scripts/verify-kernel.go`).

**Board row:** `[Bootstrap] A re-bootstrap strands the prior epoch's operator grants`
([backlog/lattice.md](../planning-artifacts/backlog/lattice.md), Component maintenance).

**Sibling design:** [kernel-orphan-retirement-design.md](kernel-orphan-retirement-design.md) — the same
*shape* of gap (reconcile converges up, never down) over a different key family. Its Increment 1 census is
the precedent this Fire mirrors; its Increment 2 is the precedent for why the destructive half waits.

---

## 1. The gap, in three lines

`lattice.bootstrap.json` carries the deployment's primordial NanoIDs — one **epoch**. Regenerating that
file mints a fresh `roleOperator` NanoID (`nanoid.go:472-512` `generate()` → `substrate.NewNanoID()` per
field), so the next boot seeds a **new** `vtx.role.<newId>` and points every `holdsRole` edge at it
(`nanoid.go:620-626`). If Core KV was not wiped, the **previous** `vtx.role.<oldId>` is still live, still
carries its `grantedBy` permissions, and now has **no holder at all**. Nothing deletes it, and
`bootstrap verify` / `verify-kernel` both report green.

Measured on a live deployment (the board row): **21 grants reachable only from the dead role** —
`AttachObject`, the ledger creates, the erasure set.

## 2. Why the existing census structurally cannot see it

`scanKernelOrphans` (`reconcile.go:203`) is the shipped kernel-orphan detector. It misses this class
**twice over**, and neither miss is a bug in it:

1. **Its listing is `vtx.meta.>`** (`reconcile.go:204`). A stranded operator role is `vtx.role.*`, its
   grants are `vtx.permission.*`, and its edges are `lnk.permission.*.grantedBy.role.*`. None are in the
   filter.
2. **Its provenance filter is `env.CreatedByOp != BootstrapOpKey → keep silent`**
   (`reconcile.go:303`). `BootstrapOpKey` is derived from the **current** epoch's `bootstrapOp` id
   (`nanoid.go:602`). Every prior-epoch key carries the **prior** epoch's op key, so widening the listing
   alone would still filter all of them out. For this class the discriminator is *inverted*: the orphan is
   precisely the thing whose provenance is **not** the current op.

`reconcile.go:155` compounds it deliberately — a non-`vtx.meta.*` entry is classified `retained`
("deliberately left alone"). Topology is the Processor's, not the seeder's. That is correct for reconcile
and is exactly why detection has to live beside it rather than inside it.

`docs/components/bootstrap.md`'s dossier entry 3 already named the root: *"`BootstrapOpKey` identifies the
deployment, not the binary generation."* This row is the other half of that sentence — the id file is what
identifies the **epoch**, and rotating it rotates every kernel identity at once.

## 3. Fire 1 — what this builds

**A detector, and the end of the false green. No writes.**

`StrandedOperatorEpochs(ctx, kv) ([]StrandedOperatorEpoch, error)` in `internal/bootstrap`:

1. One server-side filtered listing: `vtx.role.*.canonicalName`. Candidate count = **the deployment's role
   count** (tens), one key each — not the keyspace, and not the `vtx.meta.>` population whose size minted
   dossier entry 2.
2. Drop the current epoch's own role by id. For each remaining candidate: read the aspect; keep only
   `data.value == "operator"`, not tombstoned. On a single-epoch deployment this is empty, and **steps 3–4
   never run at all** — the pass costs one listing plus one GET per role.
3. Holder check — `lnk.identity.*.holdsRole.role.<id>`, the target-bounded form
   `substrate/kv.go:256-268` documents. **Any live holder ⇒ not stranded**, skip silently: a held role is
   somebody's live role, whatever it is named.
4. Grant census — `lnk.permission.*.grantedBy.role.<id>`, same form. Live edges only (`isDeleted` read
   per edge, bounded by the stranded role's own degree).

Both link reads are bounded by the *stranded* role's degree, never the keyspace — the
`cancelOpenTasksForOpMeta` shape (`internal/pkgmgr/opmetaretirement.go:65-70`).

### 3.1 The predicate, and what it deliberately does not use

| Filter | Effect | Why |
|---|---|---|
| role id ≠ current `RoleOperatorID` | excludes the live kernel role | the whole point |
| role vertex live, `.canonicalName` live | ignores already-tombstoned residue | nothing to report |
| `canonicalName == "operator"` | names the class | the role's identity is its canonicalName, not its id |
| **zero live `holdsRole` inbound** | the strand test | an epoch rotation moves every holder at once |
| ≥1 live `grantedBy` inbound | escalates report → failure (§4) | live authority, not dead weight |

**Not used: `createdByOp`.** §2.2 — for this class it filters out exactly the population being looked for,
and the prior epoch's op key is unknowable from the current id file. **Not used: `data.protected`.** It is
carried in the report as corroboration but never gates, because `primordial.go:688` records it as retired
as an authorization input; a predicate must not quietly revive a retired field's meaning.

A package-authored role that is named `operator`, held by nobody, and carrying grants would also be
reported. That is not a false positive worth suppressing — it is the same defect with a different author.

## 4. Surfacing — where the exit status moves, and where it must not

Dossier entry 1 is binding here: *"an advisory computation carries its error **in the plan**, and only its
own reporter reads it — never a `return`."*

- `reconcilePlan` gains `strandedEpochs` + `strandedScanErr`; `KernelReport` gains
  `StrandedOperatorEpochs` + `StrandedScanErr`. The scan's own failure **never** propagates through
  `ReadKernelReport`'s returned error.
- **Boot (`ReconcilePrimordial`) — advisory, always.** One `logger.Warn` per stranded epoch. A boot must
  not fail on a condition boot cannot fix.
- **`VerifyKernel` + `verify-kernel` — this is where green stops lying.** A stranded epoch with
  **≥1 live grant** is a **failure**. With zero live grants it is a notice. A scan error is a notice.

### 4.1 The fork: failure, or notice like a kernel orphan?

Resolved to **failure**, against the sibling design's precedent, on three grounds:

1. **A kernel orphan is inert; this is live authority.** An unbuilt DDL sits there. Twenty-one permission
   vertices authorizing `AttachObject`, ledger creates and the erasure set, hanging off a role nobody
   holds, are a live, unreachable authority island on the trust boundary.
2. **The row's stated defect *is* the green.** "`bootstrap verify` passes green" is what makes the
   condition invisible; a notice on a 30-line gate output does not end that.
3. **A remedy already exists and is already printed.** `verify-kernel`'s failure path ends with *"run
   `make down && make up` to re-bootstrap from clean state"*, which is precisely the fix for a bucket
   carrying a stale epoch. A red gate with no remedy would be a bad gate; this one has the remedy in the
   message.

Counter-argument, recorded: reconcile made a kernel *change* wipe-free, and this failure's only remedy is a
wipe. True — and that is Fire 2's whole subject (§7). Until Fire 2 exists, "wipe" is the honest remedy, and
saying so loudly beats saying nothing.

**CI cannot redden on this.** `stack-gates` (`ci.yml:265`) runs `make verify-kernel` against a container
that generated its id file and seeded an empty bucket in the same job: exactly one operator role, and it is
the current one. §6's first test pins that.

## 5. Non-goals

- **No retirement, no re-pointing, no writes of any kind.** §7.
- **The rest of the prior epoch** — stranded admin/service identities, the old meta-root, the old lenses —
  is the *same* root cause (one id-file rotation) and belongs to **this item's Fire 2**, not to a new board
  row. Fire 1 reports the operator role because that is the authority-bearing half.
- No change to `reconcile.go`'s four outcomes, to the `vtx.meta.>` census, or to any seeding path.

## 6. Green checks

1. `TestStrandedOperatorEpochs_SingleEpochBucketIsSilent` — seed once, scan, expect empty. The
   no-false-red pin for CI.
2. `TestStrandedOperatorEpochs_RotatedIdFileStrandsPriorRole` — seed epoch A, rotate the id file, seed
   epoch B into the same bucket, scan: exactly A's role, with A's grant count.
3. `TestStrandedOperatorEpochs_HeldRoleIsNeverStranded` — a prior-epoch role that still has one live
   `holdsRole` edge is silent.
4. `TestStrandedOperatorEpochs_TombstonedRoleAndTombstonedEdgesAreSilent` — soft-deleted role, and a
   stranded role all of whose `grantedBy` edges are `isDeleted`, drop to zero-grant (notice, not failure).
5. `TestStrandedOperatorEpochs_ForeignRoleNameIsSilent` — a holderless, granted `vtx.role.*` whose
   canonicalName is not `operator` is never reported.
6. `TestVerifyKernel_StrandedEpochWithGrantsFails` / `_WithoutGrantsIsNotice` — the exit-status split.
7. `TestReadKernelReport_StrandedScanErrNeverFailsTheBuiltSetComparison` — dossier entry 1's shape.

Gates: `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run
./scripts/lint-conventions.go`, `go test ./internal/bootstrap/... ./internal/substrate/...`, full
`go test ./...` with `POSTGRES_TEST_DSN` set (REMOTE.md §3).

## 7. 🔭 For Andrew — Fire 2 is not built, deliberately

Fire 2 is the reconciliation verb: either **retire** the stranded epoch (tombstone the role, its grants and
its edges) or **re-point** the surviving grants at the current operator role. Both are **destructive verbs
over kernel authority state**, and the sibling design already flagged its own equivalent as *"the call in
this tranche most likely to be worth taking back."* Re-pointing is strictly worse than it looks: it would
*grant* the current role every permission the dead one accumulated, which is an escalation path wearing a
cleanup's clothes.

It is also gated the same way its sibling is: Inc 2 there builds only on a **non-empty census from a
long-lived deployment**. Fire 1 is that census. Building the verb first would be scaffolding with no
measured demand — and here the measurement (21 grants) exists but the *fork* does not.

**Nothing is blocked on this.** Fire 1 ships whole; Fire 2 needs a direction, not a design.

---

### Fire 1 fire brief (build note, 2026-08-25)

**1. Scope sentence.** `bootstrap verify` and `verify-kernel` learn to see a prior-epoch operator role that
is still live, carries live grants, and has no holder — the cross-epoch orphan class `scanKernelOrphans`
structurally cannot reach. Detection and reporting only; no writes. Green bar: §6.

**2. Verified touch-list** (every anchor re-checked live against `d3ce34d`):

| File | Anchor | Edit |
|---|---|---|
| `internal/bootstrap/reconcile.go` | `62` `type reconcilePlan` · `162` `planReconcile` calls `scanKernelOrphans` · `203-264` `scanKernelOrphans` · `291-307` `kernelCandidatePasses` · `379-387` `ReconcilePrimordial` warns · `449-456` `type KernelReport` · `464-482` `ReadKernelReport` | new `strandedEpochs`/`strandedScanErr` plan fields, plan call, boot Warn, report fields |
| `internal/bootstrap/strandedepoch.go` | new | `StrandedOperatorEpoch`, `StrandedOperatorEpochs` |
| `internal/bootstrap/verify.go` | `266-295` the `ReadKernelReport` switch | failure/notice split (§4) |
| `scripts/verify-kernel.go` | `327-360` the report + orphan blocks | same split, printed |
| `internal/bootstrap/strandedepoch_test.go` | new | §6.1–6.5, 6.7 |
| `internal/bootstrap/verify_kernel_test.go` | `1-257` | §6.6 |
| `docs/components/bootstrap.md` | `167` dossier | close-pass entries |

Rotted leads corrected during grounding: a scout reported `scanKernelOrphans` already reaching
`vtx.role.*`/`vtx.permission.*` — **false**, `reconcile.go:204` filters `vtx.meta.>` and `:303` keys on the
current `BootstrapOpKey` (§2). The design's premise stands on the verified reading.

**3. Precedents to mirror.**

- Target-bounded reverse-link enumeration by subject wildcard —
  `internal/pkgmgr/opmetaretirement.go:65-70` (`lnk.task.*.forOperation.meta.<id>`), semantics documented at
  `internal/substrate/kv.go:256-268` (`lnk.*.*.<rel>.<t>.<id>`, server-side, bounded by hub degree).
  Wildcard passthrough confirmed against the pin (`nats.go@v1.52.0` `jetstream/kv.go:1230-1236`, *"Could be
  a pattern so don't check for validity as we normally do"*).
- Listing + ctx.Err() partial-result guard — `reconcile.go:204-219`.
- Live-link filtering by `isDeleted` from a parsed envelope — `system_actors.go:60-77`.
- Unloaded-primordial-table refusal — `system_actors.go:51-53` (`ErrPrimordialIDsUnloaded`).
- Advisory-scan error carried in the plan — `reconcile.go`'s `orphanScanErr` / `KernelReport.OrphanScanErr`.
- Fixture shape — `reconcile_test.go:31` `newReconcileSeeder` over `natsfixture.Server(t)`.

**4. Increment order.**

1. `strandedepoch.go` + its unit tests (§6.1–6.5). Green: `go test ./internal/bootstrap/ -run
   TestStrandedOperatorEpochs -count=1`.
2. Plan/report wiring + boot Warn + `ReadKernelReport` (§6.7). Green: `go test ./internal/bootstrap/ -run
   'TestReadKernelReport|TestKernelOrphans' -count=1`.
3. `verify.go` + `scripts/verify-kernel.go` exit-status split (§6.6). Green: `go test
   ./internal/bootstrap/ -count=1` + `go vet ./scripts/...`.
4. Full gates: `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run
   ./scripts/lint-conventions.go`, `POSTGRES_TEST_DSN=… go test ./... -p 4`.

**5. In-scope gotchas — `docs/components/bootstrap.md` dossier, copied verbatim.**

- *A read-only computation added to `planReconcile` inherits the failure posture of the repair path it
  shares.* Check: an advisory computation carries its error **in the plan**, and only its own reporter reads
  it — never a `return`. → §4. **Binding on increment 2.**
- *A boot-time scan's cost is set by the population it ENUMERATES, not the one it is about.* Check: state
  the candidate count from a **live listing** before costing a scan, and derive from the listing anything
  the listing already knows. → §3: candidates are `vtx.role.*.canonicalName` (one key per role, tens), and
  steps 3–4 do not run at all when step 2 is empty.
- *`BootstrapOpKey` identifies the deployment, not the binary generation.* Check: any verb keyed on kernel
  provenance states its behaviour under a rollback. → §3.1: this predicate is **not** keyed on provenance,
  deliberately. Rollback behaviour: two binaries sharing one id file compute the same `RoleOperatorID`, so
  an older binary sees the same single live role and reports nothing.
- *An unloaded primordial identifier reads as a value, not as "unconfigured".* → **the sharpest hazard
  here**: the predicate excludes the current role by `id != RoleOperatorID`, so an unloaded table (empty
  string) would match **every** role and report the live kernel role as stranded. `StrandedOperatorEpochs`
  refuses with `ErrPrimordialIDsUnloaded` before touching the graph, mirroring `system_actors.go:51-53`.

Standing checklist, the three that bite this fire: **(2) every census is a premise** — the "21 grants"
figure is the board's live measurement, not reproducible in this container; the code reports what it counts
and no test pins 21. **(3) a negative test needs its positive vector proven first** — §6.3/6.4/6.5 are all
negatives, so each is written against the §6.2 positive and proven by reverting its own guard. **(6)
precedent may carry debt** — `reconcile.go:204-219` does not de-dup its lister output though the pin
documents duplicates (`substrate/kv.go:309-317` de-dups for exactly this reason); the new scan de-dups.

**6. Adjacent finds.** (a) `scanKernelOrphans`'s missing de-dup, above — **absorbed**: the new scan de-dups,
and the existing one is not widened by this fire (its double-report is a repeat, not membership loss, since
it classifies per key rather than paging). (b) The rest of the prior epoch — stranded identities, meta-root,
lenses — is the same root cause and belongs to this item's Fire 2, not a new row (§5). (c) The
destructive verb itself: **needs Andrew** (§7), flagged, not filed as work.

**7. Non-goals.** No writes of any kind. No change to `reconcile.go`'s four outcomes, the `vtx.meta.>`
census, or any seeding path. No new board row.

**Scope-diff gate:** parts 2–4 traced item-by-item to part 1; every touch is detection or reporting. No
adjacent mechanism substituted. Dependencies re-verified both ways: `ListKeysFiltered` wildcard support is
load-bearing and confirmed against the pin; nothing listed proved inert.
