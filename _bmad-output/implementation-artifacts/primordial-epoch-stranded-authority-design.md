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
3. Holder census — `lnk.identity.*.holdsRole.role.<id>`, the target-bounded form
   `substrate/kv.go:256-268` documents. Live holders are **recorded**; they suppress the candidate **only
   when one of them is an identity of the current epoch** (§3.2).

   > **Amended 2026-08-25, during Fire 1's build.** This step originally read *"any live holder ⇒ not
   > stranded, skip silently"*, on the §3.1 premise that an epoch rotation moves every holder at once.
   > **That premise is false, and the rule it produced silenced the detector on the exact case it exists
   > to catch.** A re-bootstrap without a wipe deletes nothing: `DecideReseed` (`primordial.go:262`) sends
   > a freshly-generated id file down the create-only seed path for the *new* keys, and `reconcile.go:155`
   > classifies every non-`vtx.meta.*` entry as `retained` — "deliberately left alone". So the prior
   > epoch's admin identity, its five service actors, **and their `holdsRole` edges into the prior operator
   > role** are all still live. The prior role is therefore *held*, by its own stranded epoch-mates, and
   > the original rule would have returned empty on every real occurrence. The holders are part of the
   > island, not evidence against it.
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
| ~~zero live `holdsRole` inbound~~ | ~~the strand test~~ | ~~an epoch rotation moves every holder at once~~ — **struck 2026-08-25, premise false; see §3 step 3** |
| **no live holder from the CURRENT epoch's identity set** | the strand test | reachability is a question about the current epoch; a prior-epoch holder is part of the island (§3.2) |
| ≥1 live `grantedBy` inbound | escalates report → failure (§4) | live authority, not dead weight |

**Not used: `createdByOp`.** §2.2 — for this class it filters out exactly the population being looked for,
and the prior epoch's op key is unknowable from the current id file. **Not used: `data.protected`.** It is
carried in the report as corroboration but never gates, because `primordial.go:688` records it as retired
as an authorization input; a predicate must not quietly revive a retired field's meaning.

A package-authored role that is named `operator`, held by nobody in the current epoch, and carrying grants
would also be reported. That is not a false positive worth suppressing — it is the same defect with a
different author, and Contract #7 §6 makes it an anomaly on its own terms: *"The **only** primordial role is
`operator`"*. Nothing enforces that; `validateCanonicalNameUniqueness` is intra-`Definition` only
(`internal/pkgmgr/canonicaluniqueness_test.go:17`), so a second live `operator`-named role is unprevented,
not impossible.

### 3.2 What counts as a holder

The current epoch's identity set is the **six** operator-holding kernel identities of the loaded primordial
table — `BootstrapIdentityID` plus the Loom / Weaver / Bridge / objmgr / privacy service actors
(`nanoid.go:555-562`). A live `holdsRole` edge from one of those means a running actor really can reach the
role, and the candidate **demotes to a notice naming the suppressing edge** — never vanishes: the
suppressing input is an ordinary `AssignRole` link create that `rejectProtectedMutations` does not cover
(`step8_commit.go:727-729` skips creates), so a silent `continue` would let one benign-looking grant
permanently mute the gate while removing none of the residue. A live edge from anything else — a prior
epoch's admin, a prior service actor — is **recorded in `Holders` and does not suppress**, because the
rotation stranded the holder and the role together.

**The Gateway is deliberately NOT in that set.** Contract #7 §7.2 (`docs/contracts/07-primordial-bootstrap.md:51,53`):
*"unlike the six above, it does **not** hold the operator role … it is internet-facing … deliberately scoped
narrow instead of root-equivalent. Six of the seven hold the operator role; the Gateway is the one
exception."* A `holdsRole` edge from the internet-facing identity into a stranded operator role is the worst
escalation reachable here (§4.1) — treating it as proof of health would make the detector read its own
worst-case input as a clean bill.

This is also why the report carries `Holders` at all: they are what confers the capability (§4.1), and an
operator reading "role X, held by identity Y" needs Y to see that Y is itself residue. A bare count would
hide the shape of the island.

## 4. Surfacing — where the exit status moves, and where it must not

Dossier entry 1 is binding here: *"an advisory computation carries its error **in the plan**, and only its
own reporter reads it — never a `return`."*

- `reconcilePlan` gains `strandedEpochs` + `strandedScanErr`; `KernelReport` gains
  `StrandedOperatorEpochs` + `StrandedScanErr`. The scan's own failure **never** propagates through
  `ReadKernelReport`'s returned error.
- **Boot (`ReconcilePrimordial`) — advisory, always.** One `logger.Warn` per stranded epoch. A boot must
  not fail on a condition boot cannot fix.
- **`VerifyKernel` (the library) — notices only, never a failure.** §4.2 explains why this one cannot move
  an exit status.
- **`scripts/verify-kernel.go` (the operator + CI gate) — this is where green stops lying.** A stranded
  epoch with **≥1 live holder** is a **failure**. With zero live holders it is a notice. A current-epoch
  holder demotes it to a notice naming that edge (§3.2). A scan error is a notice.

### 4.1 The severity discriminator is `Holders`, not `GrantedBy`

> **Rewritten 2026-08-25 by the cold adversarial pass. The original ratified text had this exactly
> backwards** and is preserved below only so the next reader can see which way the error ran.

Capability is conferred by the `holdsRole` edge, not by the `grantedBy` edge, and the projections say so:

- `CapabilityReadWildcardGrantsLensDefinition` (`internal/bootstrap/lenses.go:358-365`) is
  `MATCH (identity)-[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator'` returning
  `(actor_id, '*', 'cap-read.root')`. It matches by **canonicalName**, not by `RoleOperatorID`, and reads
  **no `grantedBy` edge at all**. `internal/refractor/adapter/rls.go:198-201` matches that row with no
  `grant_source` filter. So a stranded `operator`-named role with live holders and **zero grants** projects
  installation-wide read of every RLS-protected table — for identities that are themselves residue.
- The mirror state is inert. A stranded role with 21 `grantedBy` edges and **zero** live holders projects
  nothing: every consumer requires the holder edge — the core write lens (`lenses.go:135`), the wildcard
  read lens (above), and rbac's `capabilityRolesSpec` (`packages/rbac-domain/lenses.go:92-93`).

So the gate keys on `len(Holders) >= 1`; the grant count rides along as detail.

> **Struck.** The ratified text argued: *"A kernel orphan is inert; this is live authority. Twenty-one
> permission vertices … hanging off a role nobody holds are a live, unreachable authority island."*
> **Unreachable is precisely what makes them inert.** What makes the island live is that it *is* reachable
> — by its own stranded epoch-mates, through `holdsRole`. The conclusion (this warrants a hard gate)
> survives; the reason inverts, and the reason is what set the threshold.

**The remedy is edge revocation, not a wipe.** The ratified text leaned on *"run `make down && make up`"*
already being printed. That is the heaviest possible remedy and it is not the right one:
`protectedRootKey` returns `""` for any key not starting `vtx.` (`step8_commit.go:1379-1385`), so the prior
epoch's `holdsRole` and `grantedBy` edges are **not** anti-brick guarded and can be tombstoned through the
Processor as ordinary link mutations. Revoking the stranded `holdsRole` edges alone kills every projection
above, without touching a byte of graph data. The role *vertex* is `protected: true` (`primordial.go:711`)
and so cannot be tombstoned — which is a fact about Fire 2, not about this remedy (§7).

### 4.2 Why `VerifyKernel` itself must never fail on this

`VerifyKernel` is not only a report surface. `cmd/lattice/bootstrap/bootstrap.go:134` calls it and `:165`
exits 1 on any failure, and **`Makefile:202` uses that exit code as `make up`'s `FRESH` oracle**. On a
stranded stack the chain runs: `bootstrap verify` exits 1 → `FRESH=0` → `Makefile:211` runs
`bootstrap probe-empty`, which exits **1 on a populated bucket** (`bootstrap.go:47`) → `Makefile:213-214`
**`rm -f lattice.bootstrap.json`** → `Makefile:233` re-seeds → **a third epoch is minted into the same
bucket.**

Firing on exactly the condition it detects would make the defect self-amplifying, once per `make up`,
breaking every existing reference to the current epoch's NanoIDs — the thing Contract #7 §7.4's
file-is-authoritative rule exists to prevent — with the verify output swallowed by `>/dev/null 2>&1`.

This is not a retreat from "green stops lying". `bootstrap verify` prints notices before its pass/fail line
(`bootstrap.go:147-154`), so the condition becomes **visible on that surface too**; what stays truthful is
the oracle's answer to the question it actually asks, which is about *freshness*, not about residue. The
hard gate lives in `scripts/verify-kernel.go`, whose only consumers are CI and an operator running
`make verify-kernel` deliberately.

**CI cannot redden on this.** `stack-gates` (`ci.yml:265`) runs `make verify-kernel` against a container
that generated its id file and seeded an empty bucket in the same job: exactly one operator role, and it is
the current one. §6's first test pins that.

## 5. Non-goals

- **No retirement, no re-pointing, no writes of any kind.** §7.
- **The rest of the prior epoch** — stranded admin/service identities, the old meta-root, the old lenses —
  is the *same* root cause (one id-file rotation) and belongs to **this item's Fire 2**, not to a new board
  row. Fire 1 reports the operator role because it is the cheapest reliable **indicator** of a rotation.

  > **Corrected 2026-08-25 by the cold pass.** This bullet originally justified the cut with *"Fire 1
  > reports the operator role because that is the authority-bearing half."* **That is false, and the
  > deferred half is the larger surface:**
  >
  > - **The prior epoch's four capability lens definitions are still running.** Refractor discovers
  >   lenses by graph scan — it watches `vtx.meta.>` filtered on envelope class `meta.lens`
  >   (`cmd/refractor/main.go:2042`, `internal/refractor/lens/corekv_source.go:596`), not by
  >   bootstrap-file id, with no duplicate-`canonicalName` guard at load. So the prior `capability`,
  >   `capabilityRead`, `capabilityReadGrants` and `capabilityReadWildcardGrants` lenses
  >   (`primordial.go:641-696`) keep projecting into `capability-kv` and `actor_read_grants`. They are
  >   also **immune to kernel reconcile**: the built-set comparison keys on the *current* epoch's ids, so
  >   a future kernel change that narrows the capability cypher leaves the prior lens projecting the old,
  >   broader rule forever — and the existing orphan census filters it out on
  >   `CreatedByOp != BootstrapOpKey` (`reconcile.go:303`), exactly as §2 describes.
  > - **The prior epoch's identities carry live projected residue** — `cap.roles.*` and
  >   `actor_read_grants` rows — and are not in `SystemActorKeys`, which is id-pinned to `RoleOperatorID`
  >   (`system_actors.go:66`), so the platform routes them as ordinary actors against a key full of grants.
  >
  > **The asymmetry underneath this whole board row:** *enforcement* matches the operator role by
  > **canonicalName** (`lenses.go:135`, `:357`; `packages/rbac-domain/lenses.go:93`), while every *audit
  > and inventory* surface matches it by **id** (`SystemActorKeys`, Loupe's review path, `console-operator`,
  > `control-authz`, the `verify-package-*` gates). A second `operator`-named role is therefore fully
  > load-bearing for authorization and invisible to everything that reports on authorization. Fire 2 is
  > scoped against that sentence, not against "tidy up the old role".
- No change to `reconcile.go`'s four outcomes, to the `vtx.meta.>` census, or to any seeding path.

## 6. Green checks

1. `TestStrandedOperatorEpochs_SingleEpochBucketIsSilent` — seed once, scan, expect empty. The
   no-false-red pin for CI.
2. `TestStrandedOperatorEpochs_RotatedIdFileStrandsPriorRole` — seed epoch A, rotate the id file, seed
   epoch B into the same bucket, scan: exactly A's role, with A's grant count. **The fixture must be the
   real post-rotation state** — epoch A's admin identity and its live `holdsRole` edge into A's role
   included. A fixture that omits them encodes the struck §3.1 premise and passes vacuously.
3. `TestStrandedOperatorEpochs_CurrentEpochHolderSuppresses` — a non-current `operator` role held by the
   **current** epoch's admin identity is silent.
3a. `TestStrandedOperatorEpochs_PriorEpochHolderDoesNotSuppress` — the same role held only by an identity
   outside the current epoch's set is **reported**, with that holder listed. This is the §3 step-3
   amendment's own pin: it fails against the pre-amendment predicate.
4. `TestStrandedOperatorEpochs_TombstonedRoleAndTombstonedEdgesAreSilent` — soft-deleted role, and a
   stranded role all of whose edges are `isDeleted`, drop to the notice class.
5. `TestStrandedOperatorEpochs_ForeignRoleNameIsSilent` — a holderless, granted `vtx.role.*` whose
   canonicalName is not `operator` is never reported.
6. The severity split at the gate, keyed on `Holders` (§4.1): **holders + zero grants → failure**
   (the dangerous state), **grants + zero holders → notice** (the inert one), current-epoch holder →
   notice naming the edge.
7. `TestVerifyKernel_StrandedEpochReturnsNoFailures` — `VerifyKernel` must return **zero failures** on a
   stranded bucket. This is the property `make up`'s `FRESH` oracle depends on (§4.2); without this pin,
   a later fire promoting the notice back to a failure re-arms the discard-and-mint chain silently.
8. `TestReadKernelReport_StrandedScanErrNeverFailsTheBuiltSetComparison` — dossier entry 1's shape, driven
   through a stubbable scan seam rather than through the pure plan projection (a projection-only test
   passes even if `planReconcile` is rewritten to return the scan error, which would fail every boot).
9. A truncated or erroring per-key read must surface as `StrandedScanErr`, never as an empty result: the
   gate printing "no stranded epochs" because its reads timed out is the same false green this fire exists
   to end.

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

**Three things the cold pass established that reshape the fork** — read these before choosing a direction:

1. **The fork is narrower than "retire vs re-point", because the commit guard has already decided half of
   it.** `protectedRootKey` returns `""` for any key not starting `vtx.` (`step8_commit.go:1379-1385`), so
   the stranded `holdsRole` and `grantedBy` **edges are ordinary link mutations and can be tombstoned
   today**. The role **vertex** carries `protected: true` (`primordial.go:711`) and `rejectProtectedMutations`
   will refuse to tombstone it. So "revoke the edges" needs no new authority and is available now;
   "tombstone the role" needs a guard exemption first, which is a larger ask than the design originally
   implied.
2. **Revoking the edges is sufficient for the live harm.** Every projection that makes the residue dangerous
   — the wildcard read grant, the core write lens, rbac's `capabilityRoles` — requires the `holdsRole` edge
   (§4.1). The role vertex left standing is inert.
3. **Fire 2's real scope is the lens residue, not the role.** Per §5: the prior epoch's four capability lens
   definitions keep projecting, are immune to kernel reconcile, and are invisible to the existing census.
   That is the larger and longer-lived surface, and a Fire 2 that only tidied the role would leave it.

**Nothing is blocked on this.** Fire 1 ships whole; Fire 2 needs a direction, not a design.

---

### Fire 1 fire brief (build note, 2026-08-25)

**1. Scope sentence.** `bootstrap verify` and `verify-kernel` learn to see a prior-epoch operator role that
is still live and reachable from no current-epoch identity — the cross-epoch orphan class
`scanKernelOrphans` structurally cannot reach. Detection and reporting only; no writes. Green bar: §6.
*(Amended 2026-08-25: originally "carries live grants, and has no holder" — both halves were wrong, per
§3 step 3 and §4.1.)*

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
