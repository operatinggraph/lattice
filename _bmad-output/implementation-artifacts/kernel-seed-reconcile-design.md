# Kernel-seed reconcile — the kernel a long-lived Core KV actually runs

**Status:** ✅ Winston-ratified — build-ready (implementation decision; no frozen-contract change, no
architectural fork). Contract #7 §7.4's idempotence requirement is *preserved* — see §4.

**Component:** `internal/bootstrap` (+ `cmd/bootstrap`).

---

## 1. The problem

`SeedPrimordial` writes the kernel exactly once and never again:

- `internal/bootstrap/primordial.go:316-325` — "Idempotent re-run guard: if the primordial set is already
  present in this Core KV, skip the whole batch." The probe is the bootstrap op tracker key.
- `internal/bootstrap/primordial.go:349` — every entry is written `CreateOnly: true`.

The consequence is not "the kernel is stable"; it is **the kernel is frozen at whatever binary first seeded
the bucket**. Every later fix to a kernel DDL is invisible to any Core KV that has already been seeded, and
the only remedy is a full wipe + fresh bootstrap — which is exactly what a shared dev stack and a demo box
cannot casually do.

### The live proof

`UpgradePackageDDLScript` gained a tombstone branch in `6b68fde4` (2026-07-22):

```starlark
if mop == "tombstone":
    out_mut = {"op": mop, "key": key}
```

placed *before* the `document` check, so a tombstone mutation is not required to carry a body
(`internal/bootstrap/install_ddl.go:233-238`).

The script actually installed in the shared dev stack's Core KV
(`vtx.meta.SXVe4jnwj7tHEW1wdubz.script`, canonicalName `UpgradePackage`) has **no** tombstone branch — it
goes straight from the aspect-key check to the document check. So when
`vertical-package-standard` Inc 5 (`52711a5a`) became the first change to *remove* an op-meta from a package
(lease-signing's bare `RecordIdentityPII` shadow), the client planned the removal correctly —

```
lattice-pkg install --force --dry-run packages/lease-signing
  → op=tombstone key=vtx.meta.EUayYDxpPRZYZZhZEUay, tombstoned=1
```

— and the submit was rejected by the *stored* script:

```
UpgradePackage rejected: ScriptFailed: ScriptError:
  fail: InvalidArgument: mutation requires a document dict: vtx.meta.EUayYDxpPRZYZZhZEUay
```

The fix has been in `main` for four days and does not run. That is the general defect; the stuck upgrade is
just the first caller to notice.

## 2. Why a kernel version counter is the wrong fix

The obvious shape — stamp the primordial set with a kernel/schema version, and on boot upgrade in place when
the stored version is older — **reproduces the failure it is meant to fix.** It is correct only if every
author of a kernel-DDL edit remembers to bump the counter, and a forgotten bump fails *silently*: the fix
ships, the version matches, nothing upgrades, and the next reader concludes the mechanism works.

That is the same shape as the package same-version no-op that has already bitten this codebase (an edited
package at an unchanged version installs as a no-op). Adding a second hand-maintained version register to
guard the kernel would double the surface of that bug, in the one place where a silent miss is worst.

## 3. The fix: reconcile on content, not on a version

`buildPrimordialEntries()` is **byte-deterministic**:

- every envelope is stamped with the fixed `BootstrapTime` constant (`internal/bootstrap/envelope.go:13`,
  `2026-05-13T00:00:00Z`) — there is no `time.Now()` anywhere in the entry-building path (the one
  `time.Now()` in `primordial.go:1257` is `MarkBootstrapComplete`, a Health-KV marker, not a kernel entry);
- every NanoID comes from `lattice.bootstrap.json` and is stable across runs;
- the bodies are literals and passed-in IDs — no map-iteration or random source.

So the question "is this Core KV's kernel what this binary builds?" is **directly answerable** by comparing
the stored document to the built one. No bookkeeping, nothing to forget, no drift.

At boot, for each primordial entry:

| stored state | action |
|---|---|
| missing | **create** |
| present, semantically equal | **no write** |
| present, differs | **update**, OCC-guarded on the revision read |

## 4. Idempotence is preserved — and strengthened

The guard being replaced exists to keep boot idempotent, and it must stay idempotent. Reconcile is a
**fixpoint**: a converged kernel produces zero writes. Idempotence now holds *by construction* (the
converged state is a no-op) rather than by refusing to look.

**The fixed `BootstrapTime` is what makes this terminate.** The built envelope's `lastModifiedAt` never
advances, so a key reconciled on one boot compares equal on the next. A reconcile must therefore **never**
stamp wall-clock provenance — doing so would rewrite the entire kernel on every boot forever. This is the
single most important invariant in the mechanism and is asserted by a test.

Comparison is **semantic, not raw bytes**: both sides unmarshal to `any` and compare with
`reflect.DeepEqual`, so a pure JSON key-ordering difference from an older marshaller is not a write, while
any value difference is.

## 5. Scope — kernel *definitions* only, and never over a deliberate deletion

The tempting scope is "the whole primordial set", on the principle that the binary is the authority on kernel
content. **That reasoning is wrong, and adversarial review caught it before it shipped.** It rested on the
claim that every primordial root carries `data.protected: true`, so the Processor's commit-time guard
(`rejectProtectedMutations`) closes the op path and any difference must mean an older binary. Two things
falsify it:

- **The 12 primordial links are outside that guard by design.** `protectedRootKey`
  (`internal/processor/step8_commit.go:637-643`) returns `""` for anything not vertex-rooted, with the
  comment that links "are not kernel-protected entities". None of the primordial links carries a protected
  flag either.
- **rbac-domain has a first-class op that writes exactly those keys.** `RevokeRole`
  (`packages/rbac-domain/ddls.go:376-388`) computes `lnk.<actorType>.<actorId>.holdsRole.role.<roleId>` and
  tombstones it — the precise shape of `LoomHoldsRoleLinkKey` and its siblings. `RevokePermission` does the
  same for the six `grantedBy` links.

A Lattice tombstone is **soft**: the key stays, carrying `isDeleted: true`. So a revoked grant is
byte-different from what the binary builds and *looks exactly like staleness*. A whole-set reconcile would
have rewritten it back to `isDeleted: false` — restoring root-equivalence to a decommissioned service actor,
on an ordinary boot, with nothing but an Info log. That is a privilege escalation performed by a repair tool.

The scope is therefore narrow, and each exclusion earns its place:

| stored state | action | why |
|---|---|---|
| absent | **create** | an incomplete kernel is never correct; a soft tombstone leaves the key present, so this cannot resurrect a revocation |
| `isDeleted: true` | **never write** | a deliberate act recorded by an operation, not staleness |
| differs, `vtx.meta.*` | **rewrite** | scripts, schemas and lens specs — the kernel *code* a stale bucket must not keep |
| differs, anything else | **never write** | identities, roles, permissions and links are topology whose lifecycle belongs to operations, not to the seeder |
| bootstrap op tracker | **never touched** | the sentinel `PrimordialSeeded`/`DecideReseed` probe and two-phase-commit marker (Contract #7 §7.4) — seeding-state machinery |

Everything the fire actually exists to fix lives under `vtx.meta.*`. Divergence outside that set is
**reported** (counted as `Retained`, logged at Warn) and left alone — the seeder observes topology, it does
not adjudicate it.

### A related gap found while grounding

The **5 aspect-type meta roots** (`seedAspectTypeMeta`, `primordial.go:1021`) are built with
`map[string]any{}` — no `protected` flag — while every other kernel meta root has one. They are kernel DDLs
(`meta.ddl.aspectType`) that a package uninstall or upgrade could in principle tombstone or update. The
`isDeleted` rule above means reconcile will not fight such a deletion, but the missing flag is a real hole.
Filed as its own board row rather than folded in.

## 5b. Writes are one atomic batch

The seed this repairs is a single `substrate.AtomicBatch` (`primordial.go:359`), and the reconcile keeps that
guarantee rather than degrading to a per-key loop. The reason is concrete: a meta-vertex's aspects are
separate keys written in sequence (`canonicalName, permittedCommands, description, script, …`), so a pass
that aborted midway — a context deadline on ~76 sequential round trips is entirely reachable — could leave a
DDL with a **new `.script` beside an old `.inputSchema`**, validating ops against one definition and
executing another. And that half-written state *is* reachable by a live Processor without a restart: the
in-commit `Invalidate` re-reads the meta root from Core KV on any later `vtx.meta.*` commit.

`substrate.BatchOp` carries per-op `HasRevision`/`Revision`, so rewrites are revision-conditioned on the body
the plan compared and a concurrent writer is never clobbered. A rejected batch changes nothing and re-plans
on the next boot.

**Restores are deliberately not revision-conditioned.** `CreateOnly` asserts last-sequence zero, which a
deleted key's own tombstone marker already violates — conditioning a restore would make a purged kernel
entry *permanently* unrestorable, rejected on every boot forever. There is nothing to protect on a key the
plan observed as absent, and the bytes written are the same deterministic kernel body any concurrent
bootstrapper would write. (Caught by the restore test, which failed exactly this way.)

## 6. Write path (P2) and where reconcile runs

Bootstrap writes Core KV **directly**, exactly as it already does for the initial seed. This is not a P2
violation and it cannot be routed through the Processor:

- the Processor's own DDLs are what is being repaired — routing the repair through them is circular;
- protected kernel roots are rejected at step 8 by design, so the op path is closed on purpose.

Bootstrap is the sanctioned pre-Processor kernel writer (Contract #7); reconcile is the same actor, in the
same phase, with the same authority.

**Call site.** `cmd/bootstrap/main.go` today calls `SeedPrimordial` *only* on the `freshlyGenerated` path
(`main.go:126-128`) and logs "primordial seeding skipped — already done on prior run" otherwise. Since
`SeedPrimordial` already probes the bucket internally and branches, the call becomes **unconditional** and
the branch stays inside `SeedPrimordial`: seeded ⇒ reconcile, unseeded ⇒ the existing atomic batch. One code
path, and every caller — including `internal/testutil/pipeline.go` — gets convergence.

## 7. Visibility on an already-running stack — why `reseed-kernel` is load-bearing

The Processor's `DDLCache` is filled by `Refresh` at startup; thereafter a meta root is re-read only by the
in-commit `Invalidate` (`internal/processor/ddl_cache.go:50-52`, step-8). It does not watch Core KV, so a
bootstrap-direct kernel write reaches a running Processor only when that Processor restarts.

**`make up` does not close this gap either**, and the first draft of this design wrongly assumed boot order
would. `make up` short-circuits: when NATS and Postgres are healthy, the Processor and Refractor are running,
and `lattice bootstrap verify` passes, it prints *"Kernel already up … reusing"* and **never runs
`bin/bootstrap` at all** (`Makefile:157-165`). And `bootstrap verify` asserts presence and envelope shape, not
content — a stale `UpgradePackage` script passes it cleanly, which is exactly how the reported failure
survived.

So on the stack this fire targets — healthy, running, stale — `make up` after the fix lands would do nothing.
**`make reseed-kernel` (§7c) is the remedy, not a convenience wrapper**; it is the only path that reconciles
and then cycles the Processor.

Adding a kernel-DDL KV watch to the Processor would be a real design change with its own trust implications,
and no consumer needs it yet.

## 7b. Making staleness visible — the verify-kernel freshness assertion

Reconcile repairs a stale kernel, but nothing *detects* one. `scripts/verify-kernel.go` asserts that every
kernel key is present, that its envelope carries the required fields, that `isDeleted` is false and
`createdBy` is the bootstrap identity — all of which a bucket seeded by an older binary satisfies **while
running superseded DDL scripts**. It passed on the stuck dev stack, with the broken `UpgradePackage` script
sitting right there. A presence check cannot see this class of defect by construction.

So the gate gains the assertion the reconcile makes meaningful: `bootstrap.KernelDrift` — the read-only twin
of `ReconcilePrimordial`, same comparison, no writes — reports entries that are missing or whose stored body
differs from the built one, and `verify-kernel` fails on either, naming `make reseed-kernel` as the remedy.

CI bootstraps fresh, so the converged case is the normal one and the gate stays quiet; it fires exactly on
the condition that was previously invisible.

## 7c. Operator surface

`cycle-refractor` and `cycle-loupe` already exist; the Processor had no cycle target, which is what applying
a kernel fix to a live stack needs (§7). Two Makefile targets close that:

- **`cycle-processor`** — rebuild `bin/processor` and relaunch it against the still-running stack, mirroring
  `cycle-refractor` exactly (`assert-main-checkout` guard, env sourced from the `up` recipe, `pgrep` liveness
  check, appends to `processor.log`). Not a teardown.
- **`reseed-kernel`** — rebuild `bin/bootstrap`, run the reconcile pass against the live Core KV, then
  `cycle-processor` so the repaired DDLs are actually loaded. This is the whole remedy, in one command, with
  no wipe.

## 7d. What review changed

Recording this because the corrections are the load-bearing part of the design, not footnotes. Three-layer
adversarial review (security · edge-case · acceptance) ran before merge and produced two real defects:

1. **Whole-set reconcile would have resurrected revoked grants** (found independently by two reviewers). The
   scope narrowed to §5's table, and `TestReconcilePrimordial_DoesNotResurrectARevokedGrant` now pins it.
2. **`make up` never reaches this code on a healthy stack** — the reuse short-circuit. §7 rewritten;
   `reseed-kernel` is the remedy, not a convenience.

A third correction came from the restore test itself: `CreateOnly` cannot restore a purged key (§5b).

## 8. Not in scope — named residual

Reconcile **creates and updates; it does not remove.** A kernel key that this binary no longer builds (the
retired RoleMgmt DDLs are the historical example) stays in an old bucket forever.

Removal needs an authoritative enumeration of kernel-owned keys that is distinguishable from package-written
`vtx.meta.*` — `PrimordialVertexKeys()` covers roots but not the full aspect set, and a prefix scan cannot
tell a kernel meta-vertex from a package's. That is a separate mechanism with a real failure mode (deleting a
package's DDL), so it is filed as its own row rather than guessed at here.

**Consumer of the residual:** any operator upgrading a long-lived bucket across a kernel *shrink* — the
next time the kernel drops an entity, that bucket keeps a live orphan the binary no longer knows about.
Nobody is blocked on it today; the kernel has not shrunk since the RoleMgmt move.

### `bootstrap verify` still reports a stale kernel as fresh — ✅ shipped `7628070e`

`VerifyKernel` now calls `KernelDrift` and fails on `missing`/`stale`, mirroring what `scripts/verify-kernel.go`
already did — the freshness probe `make up` reuses is self-healing. Left below for context.

`VerifyKernel` (`internal/bootstrap/verify.go`) asserts presence, envelope shape, `isDeleted`, `class` and
`vertexKey` — never content. `make up`'s reuse short-circuit calls it (§7), so `make up` will keep printing
*"Kernel already up … reusing"* over a bucket whose DDL scripts this binary no longer builds.

`KernelDrift` now makes the missing assertion a few lines of work, and wiring it into `VerifyKernel` would
make `make up` self-healing — drift would drop it out of the fast path into the bootstrap it already runs
there. **Named consumer: `make up`'s freshness probe (`Makefile:157-165`).** Not folded into this fire
because it changes the inner-loop behavior of `make up` for every developer and the demo box, which deserves
its own scope rather than riding along. Filed as its own row.

## 9. Build order

1. **Inc 1** — `internal/bootstrap/reconcile.go`: `ReconcilePrimordial` + result counts, semantic comparison,
   OCC update, op-tracker exclusion. Tests: create-missing, update-changed, no-write-when-equal, the
   **fixpoint** assertion (two reconciles in a row ⇒ second writes nothing), op-tracker untouched.
2. **Inc 2** — wire it: `SeedPrimordial`'s seeded branch reconciles instead of skipping;
   `cmd/bootstrap/main.go` calls `SeedPrimordial` unconditionally.
3. **Inc 3** — `bootstrap.KernelDrift` + the `verify-kernel` freshness assertion (§7b), and the
   `cycle-processor` / `reseed-kernel` operator targets (§7c).
4. **Inc 4** — live proof on the shared dev stack: `make reseed-kernel`, then re-run the lease-signing
   upgrade that is currently stuck and confirm the tombstone commits.

**Green checks:** `go build ./...`, `make vet`, `golangci-lint run ./...`, `go test ./internal/bootstrap/...`,
`make verify-kernel`, plus the full suite (the change is in the boot path every test fixture uses).

## 10. Gotchas

- **Never stamp wall-clock provenance in a reconciled envelope** (§4) — it turns the fixpoint into an
  every-boot rewrite.
- **"Differs from what the binary builds" is not a synonym for "stale"** (§5). A soft tombstone and an
  operation's write both differ. Widening the rewrite scope without re-deriving *why* each key class is
  safe is how this mechanism becomes a privilege-escalation path.
- `internal/testutil/pipeline.go:392` calls `SeedPrimordial` on a fresh embedded bucket every time; the
  reconcile branch must be a genuine no-op there (unseeded ⇒ atomic batch, unchanged).
- `DecideReseed` must keep its current meaning. Reconcile is *not* a re-seed: it does not reopen the
  two-phase window and does not touch `lattice.bootstrap.json`.
- The per-key fallback (`seedPrimordialPerKey`) stays as the concurrent-bootstrap path; reconcile is a
  distinct branch, not a replacement for it.
