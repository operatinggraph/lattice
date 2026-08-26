# Stranded primordial-epoch authority — the orphan class the kernel census cannot reach

**Status:** ✅ **Fire 1 SHIPPED 2026-08-25** (detector + verify gate) — Winston-ratified, three cold
adversarial reviews plus a cumulative close pass, which between them overturned two ratified claims (§3
step 3, §4.1) before it landed.
**Fire 2 ✅ SHIPPED 2026-08-26** (edge revocation + reserved-name guard, both mint AND package-declared
paths + rotation e2e test; lens residue **descoped from destroy to detect+report**, with a cypher-diverged
twin ranked a failure) — three more cold adversarial reviews found the "provably inert" lens claim false
(a real historical counter-example, `c9a80312`), a missing precondition that could have bricked the
current epoch on a mismatched id file, and several other defects — all fixed before merge. See the Fire 2
fire brief + its close-pass amendment at the end of this doc.

**Component:** `internal/bootstrap` (+ `scripts/verify-kernel.go`).

**Board row:** `[Bootstrap] A re-bootstrap strands the prior epoch's operator grants`
([backlog/lattice.md](../planning-artifacts/backlog/lattice.md), Component maintenance).

**Sibling design:** [kernel-orphan-retirement-design.md](kernel-orphan-retirement-design.md) — the same
*shape* of gap (reconcile converges up, never down) over a different key family. Its Increment 1 census is
the precedent this Fire mirrors; its Increment 2 is the precedent for why the destructive half waits.

---

## 1. The gap, in three lines

`lattice.bootstrap.json` carries the deployment's primordial NanoIDs — one **epoch**. Regenerating that
file mints a fresh NanoID for *every* primordial entity (`nanoid.go:472-512` `generate()` →
`substrate.NewNanoID()` per field), so the next boot seeds a **new** `vtx.role.<newId>` and **creates new**
`holdsRole` edges into it from the new epoch's own identities (`nanoid.go:620-626`).

**Nothing is re-pointed and nothing is removed.** The seed path is create-only, and `reconcile.go:155`
classifies every non-`vtx.meta.*` entry as `retained` — "deliberately left alone". So if Core KV was not
wiped, the **whole previous epoch survives intact**: its `vtx.role.<oldId>`, its `grantedBy` permissions,
its six operator-holding identities, and their `holdsRole` edges still pointing at the old role. What the
rotation destroys is not the old epoch — it is every *current* actor's ability to reach it. `bootstrap
verify` and `verify-kernel` both report green throughout.

Measured on a live deployment (the board row): **21 grants reachable only from the dead role** —
`AttachObject`, the ledger creates, the erasure set.

> **Amended 2026-08-25.** This section originally said the next boot *"points every `holdsRole` edge at"*
> the new role and that the previous role *"now has no holder at all"*. Both are false and they are the
> premise the whole design turned on — see §3 step 3 and §4.1 for what they cost.

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
identifies the **epoch**, and rotating it mints a fresh kernel identity set *alongside* the old one, which
nothing removes.

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
   `substrate/kv.go:256-268` documents. Live holders are **recorded, never suppressing**: they are
   partitioned into unaccounted-for holders and current-epoch operator-holders, and that partition sets the
   finding's rank (§3.2, §4.1). Nothing is dropped.

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
| holders partitioned by the CURRENT epoch's verified operator-holders | **ranks the finding — never filters it** | reachability is a question about the current epoch; a prior-epoch holder is part of the island (§3.2) |
| ~~≥1 live `grantedBy` inbound~~ | ~~escalates report → failure (§4)~~ | ~~live authority, not dead weight~~ — **struck 2026-08-25, inverted; see §4.1** |
| ≥1 live `grantedBy` inbound **together with any holder** | escalates to failure (§4.1) | `capabilityRolesSpec` matches *any* role, so the dead epoch's grants land in that holder's capability document |

**Nothing in this table filters except the first three rows.** Every candidate that clears them is reported;
the holder partition sets its rank. An earlier draft made reachability a suppressor — see §3 step 3 for why
that silenced the detector on its own target class, and §3.2 for why a silent drop is additionally
unauditable.

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
(`nanoid.go:555-562`) — **intersected with the identities that actually hold the current operator role**, by
a live `lnk.identity.*.holdsRole.role.<RoleOperatorID>` edge with both endpoints live. The ID table alone is
not enough: membership in it is a *name*, not a *fact about the graph*, and a primordial identity whose
current-role edge was tombstoned (an ordinary link mutation — §4.1) would otherwise be credited with
authority it no longer has, while drawing `cap-read.root` purely from the stranded role. This is
`SystemActorKeys`' question (`system_actors.go:60-77`), asked the same way.

A live `holdsRole` edge from one of those verified holders means a running actor really can reach the
role, and the candidate **demotes to a notice naming that edge** — never vanishes: the
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
- **But grants stop being mere detail the moment ANY holder exists**, including an accounted-for one.
  `capabilityRolesSpec` is `MATCH (identity {key: $actorKey}) OPTIONAL MATCH
  (identity)-[:holdsRole]->(role:role)<-[:grantedBy]-(perm:permission)` — **no canonicalName filter, no id
  filter, any role**. So a current-epoch identity holding a *prior* epoch's operator role has that role's
  every permission materialized into its `cap.roles.<actor>` document, and those grants are by construction
  not a subset of the current role's — that is §1's whole premise. Reached by one `AssignRole`.

So the ranks are: **failure** on any unaccounted-for holder, or on any holder together with ≥1 live grant;
**notice** when the only holders are identities *verified* to hold the current operator role and the role
confers no grants; **inert** when nothing live holds it. A finding whose counts are a declared lower bound
(unreadable edges) never ranks inert — unknown is not the same as empty.

> **Amended 2026-08-25 by the close pass.** The first rewrite of this section keyed severity on
> `len(Holders) >= 1` alone and called a current-epoch holder benign, reasoning *"they already hold the
> current operator role, so the wildcard lens grants them nothing they did not have."* **That generalized
> from one lens again** — true of the wildcard *read* lens, false of `capabilityRolesSpec` on the write
> plane. The lesson is now dossier entry 6 in `docs/components/bootstrap.md`, and this section is its first
> repeat offence: a claim about what residue confers must enumerate *every* consumer that walks the edge,
> not the one that prompted the question.

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
that generated its id file and seeded an empty bucket in the same job — and it runs **before** any
`verify-package-*` install, so the bucket is single-epoch: exactly one operator role, and it is the current
one. The end-to-end pin is the pre-existing `TestVerifyKernel_FreshlySeededPasses`
(`verify_kernel_test.go:60`), which drives a full seeded kernel through `VerifyKernel` and asserts no
failures; §6.1 pins the scanner, one layer below.

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
2. `TestStrandedOperatorEpochs_RotatedIdFileStrandsPriorRole` — a prior epoch hand-planted beside a real
   current one, scanned: exactly the prior role, with its grants and its prior-epoch holder. **The fixture
   must be the real post-rotation state** — the prior admin identity and its live `holdsRole` edge into the
   prior role included. A fixture that omits them encodes the struck §3.1 premise and passes vacuously.

   *Scope note (2026-08-25): this deliberately does NOT drive a real `LoadOrGenerate` rotation and re-seed.
   The end-to-end vector — rotate the id file, run the seeder twice against one bucket — is exercised
   nowhere in the suite. The hand-planted fixture is faithful in shape (same envelope helpers) and is what
   pins the predicate; the end-to-end rotation is Fire 2's to build, alongside the verb that acts on it.*
3. `TestStrandedOperatorEpochs_CurrentEpochHolderDemotesToNotice` — a non-current `operator` role held by the
   **current** epoch's admin identity demotes to a notice naming that edge (§3.2), never vanishes.
3a. `TestStrandedOperatorEpochs_PriorEpochHolderDoesNotSuppress` — the same role held only by an identity
   outside the current epoch's set is **reported**, with that holder listed. This is the §3 step-3
   amendment's own pin: it fails against the pre-amendment predicate.
4. A soft-deleted role is silent; a live role whose edges are all `isDeleted` **reports in the notice
   class**. The shipped test name must state that outcome rather than calling both cases "silent" — the
   second one reports.
5. `TestStrandedOperatorEpochs_ForeignRoleNameIsSilent` — a `vtx.role.*` with no current-epoch holder,
   carrying grants, whose canonicalName is not `operator`, is never reported.
5a. `TestStrandedOperatorEpochs_DeadEndpointsAreNotLiveEdges` (subtests cover the tombstoned holder edge,
   the tombstoned permission vertex, and the tombstoned current-epoch identity) and
   `TestStrandedOperatorEpochs_UnloadedPrimordialTableRefuses` — the link-liveness and unloaded-table pins.
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
10. `ReconcilePrimordial` stays advisory — seed a prior epoch, reconcile, `require.NoError`, four outcomes
   unchanged. §4's headline promise is otherwise unpinned, and §4.2 is why that matters.

**Fire 2's own green checks:**

11. `TestPlanStrandedEpochRetirement_HoldersAndGrants` — a `StrandedOperatorEpoch` fixture with entries in
    all three of `Holders`/`ReachableVia`/`GrantedBy` produces exactly one `RevokeRole` op per `Holders`
    entry, one `RevokeRole` op per `ReachableVia` entry (actor key correctly parsed back out of the link
    key), and one `RevokePermission` op per `GrantedBy` entry — no more, no fewer.
12. `TestPlanStrandedEpochRetirement_ReachableViaMustBeAHoldsRoleLinkIntoTheRole` — a malformed or
    mistargeted `ReachableVia` entry (wrong relation, wrong target role) is a hard error, never a silent
    skip — the invariant `StrandedOperatorEpochs` establishes must not be trusted blindly across the
    package's own internal boundary.
13. `TestPlanStrandedEpochRetirement_EmptyEpochProducesNoOps` — an epoch with all three lists empty
    (the `StrandedSeverityInert` case) produces zero ops, not an error.
14. `TestStrandedCapabilityLenses_SingleEpochBucketIsSilent` — the no-false-red pin, mirroring §6.1.
15. `TestStrandedCapabilityLenses_PriorEpochLensIsReported` — a hand-planted prior-epoch lens vertex
    (any one of the four canonicalNames) alongside the real current-epoch four is reported; the current
    four are never reported (excluded by id, mirroring `StrandedOperatorEpochs`' `RoleOperatorID` exclusion).
16. `TestStrandedCapabilityLenses_ForeignCanonicalNameIsSilent` — a `meta.lens` vertex with a canonicalName
    outside the four is never reported, mirroring §6.5's `ForeignRoleNameIsSilent`.
17. `TestStrandedCapabilityLenses_UnloadedPrimordialTableRefuses` — mirrors §6.9: an unloaded table must
    refuse before scanning, or the exclusion-by-id matches nothing and every live capability lens —
    including the current epoch's own — reports as stranded.
17b. `TestVerifyKernel_StrandedCapabilityLensReturnsNoFailures` — mirrors §6.6/#7: a stranded lens is a
    notice, never a failure, in `VerifyKernel`'s output.
18. `TestRotation_SecondSeedAfterIDFileRotationStrandsThePriorRole` — the real end-to-end vector Fire 1's
    §6.2 scope note deferred: `LoadOrGenerate` a fresh id file, seed; `LoadOrGenerate` a SECOND fresh id
    file (simulating a regenerated `lattice.bootstrap.json`) against the SAME bucket, seed again (no
    wipe); `StrandedOperatorEpochs` finds exactly the first epoch's role, with its holders and grants,
    using the real seed path rather than a hand-planted fixture.
19. `TestStarlark_Rbac_CreateRole_RejectsOperatorName` — `CreateRole{name: "operator"}` fails with
    `ReservedRoleName`, proven by reverting the guard and watching the test fail (the standing checklist's
    #3): a `CreateRole{name: "operator"}` call must otherwise succeed (the positive vector, already pinned
    by an existing test) before the negative one means anything.

Gates: `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run
./scripts/lint-conventions.go`, `go test ./internal/bootstrap/... ./internal/substrate/...
./packages/rbac-domain/... ./cmd/lattice/...`, full `go test ./...` with `POSTGRES_TEST_DSN` set
(REMOTE.md §3).

## 7. Fire 2 — ✅ direction ratified (Andrew, 2026-08-25): edge revocation, lens residue included

Andrew picked **edge revocation** over re-pointing and over a role-vertex tombstone, with the prior
epoch's lens residue in scope. What Fire 2 builds:

1. **Revoke the stranded epoch's edges.** Tombstone the stranded role's inbound `holdsRole` and
   `grantedBy` links via ordinary Processor link mutations — `protectedRootKey` returns `""` for any key
   not starting `vtx.` (`step8_commit.go:1379-1385`), so no guard exemption is needed. This alone kills
   every dangerous projection (§4.1): the wildcard read grant, the core write lens, and rbac's
   `capabilityRoles` all require the `holdsRole` edge.
2. **Retire the prior epoch's four capability lens definitions** (`capability`, `capabilityRead`,
   `capabilityReadGrants`, `capabilityReadWildcardGrants` — §5). The larger, longer-lived surface:
   Refractor discovers lenses by graph scan with no duplicate-canonicalName guard, and kernel reconcile
   cannot reach a prior epoch's ids. Refractor removes a lens on its `.spec` tombstone
   (`lens/corekv_source.go:519-528`) — the granularity every consumer honours, per the sibling
   kernel-orphan-retirement design's census.

   > **Corrected 2026-08-25 by Fire 2's Phase-0 grounding — this item does not ship as written; it is
   > infeasible under the kernel's own protection guard, not merely undesigned.** All four capability
   > lenses seed with `data.protected: true`
   > (`primordial.go:643,656,672,691` — `MakeVertexEnvelope(<Lens>Key, "meta.lens", map[string]any{"protected": true})`).
   > `rejectProtectedMutations` (`internal/processor/step8_commit.go:721-742`) is "the AUTHORITATIVE
   > commit-time kernel-protection guard": for every update/tombstone mutation it derives the 3-segment
   > root and refuses the WHOLE operation with `*ProtectedKeyError` if that root carries
   > `data.protected == true` — no `force` parameter bypasses it, and `protectedRootKey` maps
   > `vtx.meta.<id>.spec` back to `vtx.meta.<id>`, so tombstoning the `.spec` aspect is refused exactly
   > like tombstoning the root. The Starlark `TombstoneMetaVertex` op (`meta_ddl.go:386-445`) — which
   > DOES cascade a tombstone across the root plus every aspect suffix including `.spec` for a
   > `meta.lens`, and would otherwise be the exact mechanism — carries its own `is_protected` check
   > first (`meta_ddl.go:114-129,390-391`) for the identical reason. This is not a gap in Refractor's
   > removal mechanism (confirmed live and correct:
   > `TestCoreKVSource_SpecTombstone_FiresRemoveCallback`); it is that **nothing can ever submit the
   > tombstone mutation this design called for**, for a protected lens, current epoch or prior, without
   > a guard exemption.
   >
   > This is the SAME situation §7 item 4 already names for the stranded role vertex ("the role carries
   > `protected: true` and tombstoning it needs a guard exemption Andrew did not grant") — extended here
   > to a class Andrew was not asked about explicitly, because Phase-0 grounding is what surfaces it.
   > Applying his own stated reasoning for the role vertex ("a guard exemption spent for zero
   > live-harm reduction") to the lens case: **once item 1 ships, a stranded lens's live-harm
   > reduction is ALSO zero, not merely small.** All three consumer lenses this doc's §4.1 traces
   > (`CapabilityLens`, `CapabilityReadWildcardGrantsLens`, rbac-domain's `capabilityRolesSpec`) reach
   > the stranded role's authority only THROUGH a live `holdsRole` or `grantedBy` edge into it — item 1
   > tombstones every one of those. With zero live edges into the stranded role, the stranded epoch's
   > copy of each lens computes the cypher rule over the SAME graph as the current epoch's copy and
   > returns the IDENTICAL result set (neither cypher rule is scoped to a specific role id — both match
   > by canonicalName, `lenses.go:135,358-365` — so "which lens vertex answers" is not observable in the
   > output once no holder or grant distinguishes them). Redundant compute, not a security gap.
   >
   > **The one residual risk is prospective, not present:** a FUTURE change that narrows one of the
   > CURRENT epoch's capability-lens cypher rules leaves an un-narrowed stranded twin projecting the
   > OLD, broader rule forever, since kernel reconcile cannot reach a prior epoch's meta ids (§2, §5).
   > That is real but conditional — it only bites a future kernel-cypher change, and only if that
   > change's author does not know to check for a surviving twin. **Resolution: this item ships as
   > detect-and-report, not destroy** — `StrandedCapabilityLenses` (new, mirroring
   > `StrandedOperatorEpochs`'s shape) reports any live `vtx.meta.*` vertex named by one of the four
   > capability-lens canonicalNames whose id this deployment's primordial table does not name, as a
   > NOTICE (never a failure — it is provably inert post-item-1) in `verify-kernel`'s output. The
   > destructive half (a guard exemption for `rejectProtectedMutations`/`is_protected` scoped to
   > provably-non-current-epoch protected meta-vertices) is `🔭 flag-for-Andrew` on the board row,
   > exactly like the role-vertex tombstone — a genuine security-posture spend, not build-note scope.
   > The dossier note for "anyone narrowing a capability-lens cypher" (docs/components/bootstrap.md)
   > is this fire's mechanized memory of the residual risk.
3. **Restore the stranded grants against the CURRENT role via the package plane.** Revocation removes
   the hazard; it does not give current actors the 40 permissions back. Packages declare grants by role
   canonicalName (`GrantsTo: ["operator"]`) and the installer resolves that to the current role id at
   apply (`installer.go:262`, `resolveGrants` `:597`), and `PermissionProvenanceUnstamped`'s own doc
   comment names "upgrading the declaring package" as the healing verb — so re-applying the owning
   packages re-mints the grants where they belong. **Fire 2's Phase-0 grounds the exact vehicle** (a
   version-bump re-apply per package, vs. driving `permissionreconcile`'s machinery) — a same-version
   plain install no-ops by design, so the vehicle question is real and is build-note scope, not a fork.
   This arm is what unblocks the two verticals rows riding this item (café `CreateAccount`, LoftSpace
   `AttachObject`).

   > **Grounded 2026-08-25.** `internal/pkgmgr/permissionreconcile.go` is PURE classification — it
   > reads a live/declared population and returns findings; it holds no write or repair path of its
   > own (confirmed by full read: `ReconcilePermissions`/`ReconcileGrantLinks` are functions of their
   > arguments, no I/O). "Driving `permissionreconcile`'s machinery" is therefore not an available
   > vehicle at all — there is nothing there to drive. **A version bump is also not the right shape**:
   > the affected packages' OWN declared content (their Go source, DDLs, `GrantsTo` canonical names)
   > has not changed — what changed is external, kernel-side (the epoch rotated underneath them) — so
   > bumping `Version` would be a false claim about why the package is being re-applied, and would
   > trip `lint-package-version.go` for a commit that edits no package semantics. The actual vehicle:
   > **`lattice-pkg install --force <path>`** (no version bump), the exact "same-version edit lands via
   > `--force`" case the Makefile's own `reinstall-package` target documents — `--force` reaches
   > `resolveGrants`, which reads the CURRENTLY LOADED primordial table's `RoleOperatorID` fresh on
   > every invocation, so the resulting `grantedBy` edge key (which embeds the role id) is necessarily
   > NEW and gets created regardless of whether any byte of the package changed.
   >
   > **Which packages to reinstall is derived from the stranded finding itself, not hardcoded.** Each
   > `vtx.permission.*` vertex in a `StrandedOperatorEpoch.GrantedBy` list carries `data.declaredBy`
   > when it is package-origin (the same field `permissionreconcile.go`'s `LivePermission.DeclaredBy`
   > classifies on) — reading it directly names the owning package with no registry cross-reference
   > needed. The retirement tool prints the derived, de-duplicated `make reinstall-package
   > PKG=packages/<name>` line(s) for the operator to run, rather than shelling out to a sibling binary
   > itself — mirroring the existing `bootstrap verify` convention of diagnosing precisely and
   > printing the exact remedy command (`cmd/lattice/bootstrap/bootstrap.go:164`,
   > `"Suggestion: run \`make down && make up\`..."`) rather than a CLI tool executing another program.
   > On the deployment the board row measured, this resolves to `packages/cafe-ledger`,
   > `packages/objects-base`, `packages/privacy-operator-grant`, `packages/identity-domain` — but the
   > tool derives that set live, so it stays correct if a different package set is affected elsewhere.
4. **What deliberately stays:** the stranded role vertex and its permission vertices, **and (added by
   item 2's correction above) the stranded epoch's four capability lens vertices.** All carry
   `protected: true` and tombstoning any of them needs a guard exemption Andrew did not grant; each is
   inert once item 1 ships (the role: unheld and grant-less; the lenses: computing the same result the
   current epoch's own lenses already compute — item 2's correction), and `verify-kernel` keeps
   reporting all of them as **notices** (never a failure) — an honest record of the residue, not a
   defect.

Also consolidated into Fire 2 (per the build note's §6): the end-to-end rotation test vector (§6.2's
scope note — rotate the id file, run the seeder twice against one bucket) and the §3.1
`CreateRole`-may-name-`operator` prevention.

**Rejected at ratification:** re-pointing the surviving grants at the current role — it would *grant*
the current role every permission the dead one accumulated, an escalation path wearing a cleanup's
clothes — and the role-vertex tombstone, a guard exemption spent for zero live-harm reduction.

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
| `internal/bootstrap/verify.go` | `266-295` the `ReadKernelReport` switch | notices only — never a failure (§4.2) |
| `scripts/verify-kernel.go` | `327-360` the report + orphan blocks | the failure/notice split, printed (§4.1) |
| `internal/bootstrap/strandedepoch_test.go` | new | §6.1–6.5, 6.9 |
| `internal/bootstrap/strandedplan_test.go` | new | §6.8, §6.10 — the `strandedScan` seam and the boot-posture pins |
| `internal/bootstrap/verify_kernel_test.go` | `1-257` | §6.6 |
| `docs/components/bootstrap.md` | `167` dossier | close-pass entries |

Rotted leads corrected during grounding: a scout reported `scanKernelOrphans` already reaching
`vtx.role.*`/`vtx.permission.*` — **false**, `reconcile.go:204` filters `vtx.meta.>` and `:303` keys on the
current `BootstrapOpKey` (§2). The design's premise stands on the verified reading.

> **Blast-radius correction (2026-08-25, cold pass).** This touch-list named only the files the fire
> *edits*, and the scope-diff gate below passed on that basis — "every touch is detection or reporting."
> True of the edits, **false of the blast radius**: `VerifyKernel`'s failure slice is consumed by
> `cmd/lattice/bootstrap/bootstrap.go:134,165`, whose exit code is `Makefile:202`'s `FRESH` oracle, so a
> new failure class there changes `make up`'s control flow (§4.2). **Neither file was in the touch-list.**
> The gate should have been run over the *consumers* of every value the fire changes, not over the files
> it opens. That is the lesson this fire mints for the dossier, not a note.

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
and the still-projecting capability lenses — is the same root cause and consolidates into this item's Fire 2
(§5). (c) The destructive verb itself: **needs Andrew** (§7).

**Where (b) and (c) resolve.** Both ride **one** out, and it is an actual row: this item's own row in
`backlog/lattice.md`, flipped to `🔭 flag-for-Andrew` at close, naming Fire 2 as the verb *plus* the lens
residue. *(Corrected at close: this section previously read "flagged, not filed as work" while the row was
still stamped `🏗️ … next: Fire 1`. A "flagged" claim with no row in that state is precisely the false record
`agents/steward/SKILL.md` §4 forbids — the close pass caught it here, in the section whose job is to prevent
it.)* The §3.1 `CreateRole` vector — a runtime role may take the canonicalName `operator`, since
`validateCanonicalNameUniqueness` is intra-`Definition` only — is **detected** by what shipped, and its
*prevention* consolidates into the same Fire 2 row rather than a second one.

**7. Non-goals.** No writes of any kind. No change to `reconcile.go`'s four outcomes, the `vtx.meta.>`
census, or any seeding path. No new board row.

**Scope-diff gate:** parts 2–4 traced item-by-item to part 1; every touch is detection or reporting. No
adjacent mechanism substituted. Dependencies re-verified both ways: `ListKeysFiltered` wildcard support is
load-bearing and confirmed against the pin; nothing listed proved inert.

---

### Fire 2 fire brief (build note, 2026-08-25)

**1. Scope sentence.** Revoke a stranded epoch's `holdsRole`/`grantedBy` edges (§7 item 1), restore the
permissions those edges carried against the CURRENT operator role via the package plane (§7 item 3),
prevent a future `CreateRole` from ever minting a second `operator`-named role (§7's consolidated §3.1
item), prove the real end-to-end rotation vector (§7's consolidated §6.2 scope note), and — per item 2's
correction above — REPORT (never destroy) the prior epoch's four capability lens vertices as residue.
Green bar: §6 below.

**2. Verified touch-list** (every anchor re-checked live 2026-08-25):

| File | Anchor | Edit |
|---|---|---|
| `internal/bootstrap/strandedepoch.go` | `59-118` `StrandedOperatorEpoch` fields | none — `Holders`/`ReachableVia`/`GrantedBy` already carry everything a plan needs (vertex keys for the first and third, holdsRole LINK keys for the second) |
| `internal/bootstrap/strandedretire.go` | new | `RevocationOp`, `PlanStrandedEpochRetirement(epoch) ([]RevocationOp, error)` — pure, no I/O |
| `internal/bootstrap/lensresidue.go` | new | `StrandedCapabilityLens`, `StrandedCapabilityLenses(ctx, kv)` — mirrors `strandedepoch.go`'s shape, reuses its unexported `walkDistinctKeys`/`readDocument`/`docState` helpers (same package) |
| `internal/bootstrap/reconcile.go` | `42-81` `reconcilePlan` · `107-183` `planReconcile` · `373-483` `ReconcilePrimordial` · `490-509` `KernelReport` · `516-522` `ReadKernelReport` | add `strandedLenses`/`strandedLensScanErr` alongside the existing `strandedEpochs`/`strandedScanErr` fields, same advisory wiring |
| `internal/bootstrap/verify.go` | the `ReadKernelReport` switch (§4.2 of this doc) | print `StrandedCapabilityLenses` findings as notices, never a failure |
| `scripts/verify-kernel.go` | the report + orphan blocks | same notice-only treatment |
| `packages/rbac-domain/ddls.go` | `261-269` `CreateRole` branch | one guard: `name == "operator"` → `fail("ReservedRoleName: ...")`, before the existing NanoID mint |
| `cmd/lattice/bootstrap/retire.go` | new | `newRetireStrandedEpochCommand` — verifies `CurrentEpochOperatorReachable` first, calls `StrandedOperatorEpochs`, submits `PlanStrandedEpochRetirement`'s ops via `output.SubmitOp` after a pre-submit `linkIsLive` check (the close pass below corrects this cell's original `Error.Code == "UnknownLink"` claim — that code never appears on the wire), then reads `declaredBy` off each revoked `GrantedBy` permission, validates it against `pkgregistry`, and prints de-duplicated `make reinstall-package PKG=packages/<name>` lines |
| `cmd/lattice/bootstrap/bootstrap.go` | `16-30` `NewCommand` | add `defaultActor *string` param (mirrors `op.NewCommand`'s signature) and register the new subcommand |
| `cmd/lattice/root.go` | `81` `bootstrap.NewCommand(&flagNATSURL, &flagOutput)` | add `&flagActorKey`, matching `op.NewCommand`'s call one line above |
| `internal/bootstrap/strandedretire_test.go` | new | §6.11–6.13 |
| `internal/bootstrap/lensresidue_test.go` | new | §6.14–6.17 |
| `internal/bootstrap/rotation_e2e_test.go` | new | §6.18 — the real `LoadOrGenerate`+seed-twice vector |
| `packages/rbac-domain/starlark_test.go` | alongside `TestStarlark_Rbac_RevokeRole` (`306`) | `TestStarlark_Rbac_CreateRole_RejectsOperatorName` |
| `docs/components/bootstrap.md` | the dossier (§4.2 cites it) | new entry: a future capability-lens cypher narrowing must check `StrandedCapabilityLenses`' report for a surviving twin |

**3. Precedents to mirror.**

- Tombstone-by-known-key, revive-aware grant/revoke shape — `packages/rbac-domain/ddls.go:387-400`
  (`RevokeRole`) and `:433-446` (`RevokePermission`) — ALREADY the exact ops this fire needs; no new
  Starlark verb. Both require the link key declared in `ContextHint.Reads` (read-posture, Contract #2
  §2.5), computed identically to the script's own construction via `substrate.LinkKey(type1, id1,
  linkName, type2, id2)` (`internal/substrate/keys.go:50`) — never hand-formatted, so the CLI's declared
  read cannot drift from what the script actually looks up.
- Generic op submission from a CLI — `cmd/lattice/op/op.go`'s `newSubmitCommand` /
  `output.SubmitOp(ctx, conn, env)` (`cmd/lattice/output/submit.go:27`) — reused directly, not
  reimplemented; `flagActorKey` (root.go:33, loaded from a credential file) is the existing mechanism for
  an authenticated, sufficiently-privileged caller.
- Bounded target-reverse-link enumeration — `StrandedOperatorEpochs` itself (already shipped) is the
  precedent `StrandedCapabilityLenses` mirrors one level up (subject wildcard `vtx.meta.*.canonicalName`
  instead of `vtx.role.*.canonicalName`), reusing the same package-private helpers.
- Diagnose-and-print-the-remedy-command, never auto-exec another binary —
  `cmd/lattice/bootstrap/bootstrap.go:164` (`verify`'s `"Suggestion: run \`make down && make up\`..."`).
- Real two-epoch seeding for the e2e test — `internal/bootstrap/reconcile_test.go:31`
  `newReconcileSeeder` over `natsfixture.Server(t)`, calling `LoadOrGenerate` + the seed path twice
  against one bucket, no wipe between (Fire 1's §6.2 scope note names exactly this gap).

**4. Increment order.**

1. `strandedretire.go` + unit tests (§6.11–6.13, pure — no NATS). Green: `go test ./internal/bootstrap/
   -run TestPlanStrandedEpochRetirement -count=1`.
2. `lensresidue.go` + unit tests (§6.14–6.17, mirrors Fire 1's own natsfixture shape). Green: `go test
   ./internal/bootstrap/ -run TestStrandedCapabilityLenses -count=1`.
3. `reconcile.go`/`verify.go`/`verify-kernel.go` wiring for the lens-residue notice (§6.17b). Green:
   `go test ./internal/bootstrap/ -count=1` + `go vet ./scripts/...`.
4. `packages/rbac-domain/ddls.go` CreateRole guard + its test. Green: `go test ./packages/rbac-domain/
   -run TestStarlark_Rbac_CreateRole -count=1`.
5. `rotation_e2e_test.go` — the real rotation vector. Green: `go test ./internal/bootstrap/ -run
   TestRotation -count=1`.
6. `cmd/lattice/bootstrap/retire.go` + wiring into `bootstrap.NewCommand`/`root.go`. Green: `go build
   ./...` + any CLI-level test this increment adds.
7. Full gates: `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run
   ./scripts/lint-conventions.go`, `go test ./internal/bootstrap/... ./packages/rbac-domain/...
   ./cmd/lattice/...`, full `go test ./...`.

**5. In-scope gotchas — `docs/components/bootstrap.md` dossier, copied verbatim, plus this fire's own.**

- *A read-only computation added to `planReconcile` inherits the failure posture of the repair path it
  shares* → §4: the lens-residue scan is advisory exactly like the role scan; its error carries in the
  plan, never a `return`.
- *`BootstrapOpKey` identifies the deployment, not the binary generation* → not triggered by this fire's
  edits (no provenance-keyed verb added), but the CreateRole guard's own behaviour under a rollback is
  worth stating: an older binary lacking the guard would accept a second `operator` role again — this
  fire does not attempt to close that gap (it is a binary-version question, out of scope per §12.4 of
  the sibling design), only to close the gap for a binary that HAS the fix.
- *This fire's own, new*: **a Starlark op's read posture is a correctness error, not a style
  preference** — `RevokeRole`/`RevokePermission` read `state[lnk_key]`; the CLI's `ContextHint.Reads`
  MUST name that exact key or the op fails, and the failure mode (a script reading an undeclared key)
  is a Processor-level rejection, not a silent no-op — verify this against a real submission in
  increment 6, not just a unit test of the planning function.
- *This fire's own, new*: **a re-run against an already-revoked edge must not report failure** — but
  `Error.Code == "UnknownLink"` is not how to detect it: a generic Starlark `fail()` collapses to the
  generic `ErrCodeScriptFailed` on the wire (`commit_path.go`'s `classifyStepError`), with the specific
  reason buried in a details map no consumer in this repo parses. The shipped mechanism checks the link's
  own live state via a direct KV read BEFORE submitting, never after a rejection.

Standing checklist, the ones that bite this fire: **(1) new state needs a lifetime** — `RevocationOp`
carries no state of its own (it's a pure derivation, re-run idempotently), so this is N/A by construction,
which is itself worth confirming rather than assuming. **(2) every census is a premise** — the "40
permissions" / "21 grants" figures are the board's live measurement on a specific deployment; no test
pins those numbers, only the mechanism. **(5) one deterministic key, one writer** — `RevokeRole` and a
package's own `--force` reinstall both eventually touch the SAME grantedBy edge key space but never the
SAME key: revoke targets the STRANDED role's edge, reinstall creates the CURRENT role's edge — different
role ids, different keys, no collision.

**6. Adjacent finds.** (a) The protected-lens infeasibility (§7 item 2's correction) — absorbed as a
scope correction in this same brief, not filed separately. (b) `permissionreconcile.go` having no write
path — absorbed the same way (it changes the vehicle, not the scope). (c) The guard exemption needed to
ever retire a protected meta-vertex (lens OR the role vertex) generically — this is the SAME `🔭
flag-for-Andrew` the role vertex already carries (§7 item 4); not a new row.

**7. Non-goals.** No guard exemption to `rejectProtectedMutations`/`is_protected` (flagged for Andrew,
not built). No automatic package reinstall (the tool prints the command; an operator runs it). No change
to `reconcile.go`'s four outcomes for the role/grant scan, the `vtx.meta.>` orphan census, or any seeding
path.

**Scope-diff gate:** parts 2–4 traced item-by-item to part 1; every touch is revocation, restoration
recommendation, the CreateRole guard, the rotation test, or lens-residue reporting — none destroys a
protected vertex. Dependencies re-verified both ways: `substrate.LinkKey`/`ParseLinkKey` round-trip
confirmed against `strandedepoch.go`'s own use; `RevokeRole`/`RevokePermission`'s `ContextHint.Reads`
requirement confirmed by reading the scripts, not assumed.

### Fire 2 close pass — three cold adversarial reviews, before admit (2026-08-26)

Three independent, cold reviewers (security/threat-completeness, edge-cases/failure-modes,
conventions/design-fidelity) found real defects in the shape described above, before it ever merged. Their
findings and the resulting fixes, ranked by what they actually changed:

1. **The lens-residue "provably inert" claim (item 2's correction, above) was wrong, with a real
   precedent.** A stranded lens's stored cypher is frozen at ITS seeder's version; commit `c9a80312`
   (2026-07-02) rewrote exactly `CapabilityLensDefinition`/`CapabilityReadWildcardGrantsLensDefinition`'s
   cypher from matching `identity.data.protected` directly to matching via `holdsRole`→`operator`
   topology. A lens stranded by a pre-`c9a80312` binary reads no `holdsRole`/`grantedBy` edge at all and is
   untouched by item 1's revocation — it keeps projecting installation-wide root to every
   `data.protected` identity regardless. **Fix:** `StrandedCapabilityLenses` now reads the twin's stored
   `.cypherRule` aspect and compares it to the CURRENT definition's; a mismatch (or an unreadable cypher)
   ranks `StrandedLensSeverityDiverged`, which `scripts/verify-kernel.go` escalates to a failure — an
   identical cypher stays the inert notice the original argument correctly described for THAT case only.
   `internal/bootstrap.VerifyKernel` itself stays notice-only in every case (§4.2's `make up` FRESH-oracle
   reasoning applies to lenses exactly as it does to the role: a failure there cannot be repaired by
   discard-and-remint and would only strand a second epoch).
2. **No check that the loaded `lattice.bootstrap.json` corresponds to the target deployment.** A
   mismatched id file (wrong deployment, stale copy, wrong `--nats-url`) makes the scan see the
   deployment's real, live, CURRENT operator role as "stranded" — indistinguishable from a genuine one
   from inside the scan alone — and the tool would have revoked every current holdsRole/grantedBy edge
   into it. **Fix:** new `internal/bootstrap.CurrentEpochOperatorReachable(ctx, kv)`, run as the tool's
   first check; refuses outright if the loaded table's own role is not verifiably live and held.
3. **`epoch.UnreadableEdges` was read by nothing.** Per `strandedepoch.go`'s own doc comment this means
   the edge lists are a lower bound, yet the tool revoked what it could see and exited 0. **Fix:** a
   non-zero count is now printed and marks the run failed — it can revoke everything it read, but it can
   no longer claim the epoch is fully neutralized.
4. **The 30s scan timeout was reused as the whole run's budget**, including every per-op liveness check
   and the reinstall-recommendations pass, while each submission already had its own 10s. On the
   deployment this item's own board row measured (dozens of ops), the shared budget could expire mid-run,
   turning healthy checks into `failed` and silently dropping the ENTIRE reinstall-recommendations output
   — the arm that unblocks the two vertical rows riding this item. **Fix:** every phase (each liveness
   check, each submission, the final re-scan, the recommendations pass) now gets its own fresh timeout, and
   a recommendations-read failure marks the run failed rather than exiting silently.
5. **The originally-planned idempotency mechanism (`Error.Code == "UnknownLink"`) does not exist on the
   wire.** Grounded before this close pass, not after: a generic Starlark `fail()` collapses to the
   generic `ErrCodeScriptFailed` (`internal/processor/commit_path.go`'s `classifyStepError`), with the
   specific reason buried in a details map no consumer in this repo parses. What shipped instead — a
   pre-submit `linkIsLive` check against Core KV directly — was the corrected design throughout; this
   entry documents it explicitly since an earlier revision of this table described the disproved
   approach. The pre-check has its own TOCTOU window (a concurrent revoker, or a reply timeout on an op
   that actually committed — `output.SubmitOp` publishes before it waits); a submission error is now
   followed by a re-read of the same link before it is called a failure, and the whole run re-scans for
   remaining live authority at the end rather than trusting submission replies alone.
6. **Revoking the submitting actor's own edge first could deny every submission after it.** An operator
   whose credential predates the rotation is themself one of the stranded epoch's `Holders`. **Fix:**
   `orderSubmittingActorLast` reorders each epoch's planned ops so the actor's own `RevokeRole` (if
   present) runs last.
7. **The runtime `CreateRole` guard covers one of two live role-mint paths.** A package's OWN
   `Definition.Roles` mints a role with no reserved-name check at all (`validateCanonicalNameUniqueness`
   deliberately excludes roles), and `resolveGrants`'s `i.RoleIDs` map keys by canonical name — a package
   declaring a role named `operator` would both mint a second root-equivalent role AND hijack that same
   install's own `GrantsTo: ["operator"]` resolution onto it. **Fix:** `internal/pkgmgr`'s
   `validateNoReservedRoleName`, wired into `validateAll` (Install/Upgrade/Apply's shared pre-flight,
   pure, pre-KV) alongside `validateCanonicalNameUniqueness`. Editing `internal/pkgmgr/definition.go`
   tripped `lint-package-version.go`'s conservative `ReadGrantDomains` heuristic for one package
   (`edge-manifest`, the only one declaring it) even though this specific change cannot alter a generated
   read-grant walk; version-bumped it (`0.17.3`→`0.17.4`) rather than override a gate built to fail closed
   on exactly this kind of "probably inert" call.
8. **Unvalidated graph data (`declaredBy`) was printed as a copy-pasteable shell command.** `declaredBy`
   is ordinary, rewritable permission data, not a value this tool minted. **Fix:** every recommended
   package name is checked against `pkgregistry` (the same compiled, trusted registry `lattice-pkg
   install` itself is bound to) before it is printed.
9. **`StrandedCapabilityLenses`'s own listing (`vtx.meta.*.canonicalName`) enumerates the WHOLE meta-root
   population** — hundreds, growing with every installed package — not the tens-sized `vtx.role.*`
   population its role-plane sibling bounds itself to, and it had been wired into `planReconcile`, i.e.
   onto every process boot. This is docs/components/bootstrap.md's own dossier entry 2's regression,
   reintroduced by a sibling scan in the same fire that documented it. **Fix:** removed from
   `reconcilePlan`/`ReconcilePrimordial`'s boot path entirely; it now runs only where its callers already
   accept a slower, occasional check — `scripts/verify-kernel.go` directly, and `VerifyKernel` (used by
   `bootstrap verify` / `make up`, never by boot itself) — each via its own standalone call, not through
   `KernelReport`.
10. Also fixed from the same passes: a stale pre-existing `packages/rbac-domain/ddls.go` comment claiming
    `RevokeRole`/`RevokePermission` read their actor/role vertices (they read only the link key);
    `StrandedCapabilityLenses` did not check the candidate vertex's `class`, so a non-lens `vtx.meta.*`
    sharing a reserved canonicalName could in principle be misreported; a `dryRun` early-return could
    swallow a plan-error `failed` flag and exit 0.

Not fixed, by the reviewers' own assessment: an ordinary `AssignRole` can re-grant the SURVIVING stranded
role after this tool runs (the role vertex deliberately stays, per item 4) — a package script cannot read
`RoleOperatorID` to guard against this itself, so it remains a detection-and-re-run matter, not a
prevention one; and the holder census's `identity`-only source-type filter (Fire 1's own scope, not
widened here) has no constructed exploit but rests entirely on the capability lenses' own type filter.

### Fire 2 close pass, round two — a fourth cold verification pass, before admit (2026-08-26)

A fourth cold reviewer was asked to VERIFY the ten fixes above rather than re-discover findings from
scratch. Nine verified clean. One did not — proven with a working exploit against the shipped code, fixed
before merge, together with everything else the verification pass raised:

1. **HIGH — item 2's fix (`CurrentEpochOperatorReachable`) was self-validating and did not stop the
   brick.** The check asked "is MY loaded role live, and does at least one of MY loaded table's own six
   identities hold it" — both halves keyed on the SAME loaded file. A deployment's own PRIOR epoch's
   `lattice.bootstrap.json` is an equally self-consistent answer to that question FOREVER: the seed path
   is create-only, so the prior role stays live and the prior epoch's own primordial identities still hold
   it — that is the literal definition of a stranded epoch with holders, not a distinguishing feature of a
   current one. The reviewer proved this by loading the immediately-prior id file against an unwiped
   bucket (arguably the single most likely mistake in practice, since an operator running this tool
   typically still has both files on hand) and showing the check passed while `StrandedOperatorEpochs`
   then named the deployment's REAL, current epoch's role as stranded. **Fix:** the check now also asks
   "does any OTHER live role named `operator` exist that is NEWER than mine" — and "newer" cannot be read
   from the envelope's own `createdAt` field (every primordial entry in every epoch is stamped with the
   same fixed `BootstrapTime` constant, deliberately, so a reconcile comparison is not defeated by
   wall-clock noise — investigated and ruled out before landing on the real fix). It reads the NATS
   JetStream KV entry's own server-assigned `Created()` time instead — set at the moment each key was
   actually appended to THIS bucket's stream, never controlled by the application layer and therefore not
   reproducible by replaying an old envelope. `anyNewerLiveOperatorRole` mirrors `StrandedOperatorEpochs`'s
   own bounded `vtx.role.*.canonicalName` listing to ask it. Proven against both a hand-seeded two-role
   fixture and — the stronger proof — the real `LoadOrGenerate`+`SeedPrimordial` path run twice
   (`TestRotation_SecondSeedAfterIDFileRotationStrandsThePriorRole`): epoch B reads reachable; loading
   epoch A's own real, still-live id file back does not. This is not an absolute guarantee (two epochs
   written within the same clock resolution, or a bucket whose only live epoch is already the wrong one,
   are not caught) — it closes the proven, practical mistake without an oracle Core KV does not have today
   (confirmed: no running component's Health KV heartbeat carries an epoch identifier either).
2. **LOW — self-revoke ordering was per-epoch, not across the whole run.** With two or more stranded
   epochs live at once and a submitting actor whose authority derives from one of them, that epoch's
   self-revoke could still fire before a later epoch's ops were processed. **Fix:** every epoch's ops are
   now collected before `orderSubmittingActorLast` runs once, over the combined list.
3. **LOW — an unknown-registry `declaredBy` name warned but did not mark the run's recommendations
   incomplete**, the opposite posture from an unreadable `declaredBy` three lines below it. **Fix:** both
   causes now count toward the same "recommendations may be incomplete" returned error.
4. **LOW — `validateNoReservedRoleName` had no test proving it is reached from `validateAll`**, only that
   the function rejects the name in isolation. **Fix:**
   `TestValidateAll_RejectsReservedRoleName`, mirroring `packagename_test.go`'s own `validateAll()`-level
   proof for `validatePackageName`.

The reviewer also flagged that this close-pass section did not exist in the git WORKTREE it was told to
check — correct, and expected: design docs are edited directly in `main`, never in a fire's worktree (this
repo's own isolation rule), so a worktree's copy of a design doc is a point-in-time snapshot from
whenever the fire branched, not a live view of Winston's concurrent doc edits in `main`. Not a defect in
the shipped code; noted here only so the next reader of this doc's history understands the artifact.
