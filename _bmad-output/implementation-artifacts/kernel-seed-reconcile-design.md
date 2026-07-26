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

## 5. Scope — the whole primordial set, minus the op tracker

The principle: **the binary is the authority on kernel content; Core KV is its materialization.** That is
already the premise of `scripts/verify-kernel.go`, which asserts Core KV matches what the binary expects —
today a mismatch merely fails, with `make down && make up` as the only offered remedy.

The one exclusion is the **bootstrap op tracker** (`BootstrapOpKey`). It is the seeded sentinel that
`PrimordialSeeded` / `DecideReseed` probe and the two-phase-commit marker (Contract #7 §7.4) — seeding-state
machinery, not kernel content. Reconcile leaves it untouched so that state machine keeps a single authority.

Reconciling the rest is safe because it cannot legitimately drift: every other primordial **root** carries
`data.protected: true`, and the Processor's commit-time guard
(`rejectProtectedMutations`, `internal/processor/step8_commit.go`) rejects any update/tombstone whose root is
protected. The op path is closed against these keys, so a difference is by definition an older binary.

### Two unprotected roots found while grounding

The claim "every primordial root is protected" is **not** currently true, and the exceptions are worth
naming:

- the **bootstrap op tracker** (`MakeBootstrapOpEnvelope`, `envelope.go:75`) — excluded from reconcile
  anyway;
- the **5 aspect-type meta roots** (`seedAspectTypeMeta`, `primordial.go:1021`) are built with
  `map[string]any{}` — no `protected` flag — while every other kernel meta root has one. These are kernel
  DDLs (`meta.ddl.aspectType`) that a package uninstall or upgrade could in principle tombstone or update.

That gap is *adjacent* to this fire, not part of it — filed as its own board row rather than folded in.

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

## 7. Visibility on an already-running stack

The Processor's `DDLCache` is built by `Refresh` at startup and invalidated only in-commit
(`internal/processor/ddl_cache.go:50-52`; step-8 `Invalidate`). It does **not** watch Core KV. So a
bootstrap-direct kernel write reaches a *running* Processor only when that Processor restarts.

Boot order makes this a non-issue for `make up` — bootstrap precedes the engines. Applying a kernel fix to an
already-running stack requires cycling the Processor binary, which is a single-binary cycle against the live
stack, not a teardown. Stated here rather than worked around: adding a kernel-DDL KV watch to the Processor
would be a real design change with its own trust implications, and no consumer needs it yet.

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

## 9. Build order

1. **Inc 1** — `internal/bootstrap/reconcile.go`: `ReconcilePrimordial` + result counts, semantic comparison,
   OCC update, op-tracker exclusion. Tests: create-missing, update-changed, no-write-when-equal, the
   **fixpoint** assertion (two reconciles in a row ⇒ second writes nothing), op-tracker untouched.
2. **Inc 2** — wire it: `SeedPrimordial`'s seeded branch reconciles instead of skipping;
   `cmd/bootstrap/main.go` calls `SeedPrimordial` unconditionally.
3. **Inc 3** — live proof on the shared dev stack: re-run `bin/bootstrap`, cycle `bin/processor`, then
   re-run the lease-signing upgrade that is currently stuck and confirm the tombstone commits.

**Green checks:** `go build ./...`, `make vet`, `golangci-lint run ./...`, `go test ./internal/bootstrap/...`,
`make verify-kernel`, plus the full suite (the change is in the boot path every test fixture uses).

## 10. Gotchas

- **Never stamp wall-clock provenance in a reconciled envelope** (§4) — it turns the fixpoint into an
  every-boot rewrite.
- `internal/testutil/pipeline.go:392` calls `SeedPrimordial` on a fresh embedded bucket every time; the
  reconcile branch must be a genuine no-op there (unseeded ⇒ atomic batch, unchanged).
- `DecideReseed` must keep its current meaning. Reconcile is *not* a re-seed: it does not reopen the
  two-phase window and does not touch `lattice.bootstrap.json`.
- The per-key fallback (`seedPrimordialPerKey`) stays as the concurrent-bootstrap path; reconcile is a
  distinct branch, not a replacement for it.
