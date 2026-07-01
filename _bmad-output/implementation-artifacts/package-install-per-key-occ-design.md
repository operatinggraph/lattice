# Design — Package-install per-key OCC (F-011): condition uninstall/upgrade tombstones on the read-time `KVGet` revision

**Status: ✅ Andrew-ratified 2026-07-01 (design signed off; build pending — Lattice Steward). Contract #8 §8.3/§8.6/§8.7 edits committed on ratification.**
**Author: Winston (Designer fire, 2026-07-01)**
**Backlog row:** `planning-artifacts/backlog/lattice.md` → *Component maintenance* → "[Core] UninstallPackage tombstones unconditionally (F-011 per-key OCC follow-up)".
**Grounded demand:** the F-011 follow-up filed in `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` §"UninstallPackage per-key OCC (F-011 follow-up)" (raised by the Story 1.5.5 implementation agent, 2026-05-30, status OPEN). Contract #8 §8.3 documents the current unconditional-tombstone window; §8.6 (OCC) + §8.7 (Out of scope) document the identical window on the in-place **upgrade** path.

---

## For Andrew (one-look ratification)

**What it does (two lines).** `Installer.Uninstall` (and, in Fire 2, `Installer.Upgrade`) already `KVGet`s every declared key before it tombstones/updates it — this design **captures that read's `entry.Revision` and passes it as the per-key `expectedRevision`** so a concurrent Processor write between the client's read and the atomic-batch commit is caught (fail-closed `RevisionConflict`) instead of being silently overwritten. It closes the F-011 lost-update window with **~10 lines of client plumbing and no new state** — the `UninstallPackageDDLScript` already accepts `{key, expectedRevision}` and applies the OCC.

**The finding that reshapes the fix (please sanity-check this — it inverts the amendment).** The 1.5.5 amendment's premise — *"the canonical per-subject sequence is only exposed in the committing op's `OperationReply.Revisions`, not via `KVGet`, so we must persist install-time revisions into the manifest and thread them through"* — is **false**, and provably so. A Core-KV key **is** its own JetStream subject (`$KV.core-kv.<key>`, `substrate/batch.go:176`), and `entry.Revision()` returns that subject's last stream sequence — **exactly** what `Nats-Expected-Last-Subject-Sequence` compares against. The **Processor's own shipped §3.2 update-conditioning** (`97afcd2`) proves it end-to-end: step-4 hydrate reads a key with `KVGet` and stores `doc.Revision = entry.Revision` (`step4_hydrate.go:163`); `applyHydratedRevisions` uses that as the conditional-tombstone `ExpectedRevision` (`commit_path.go:505`); `movedDefaultedKeys` re-reads with `KVGet` and treats `entry.Revision` as the authoritative per-subject revision (`commit_path.go:544`). The **hard-delete design** (a parallel 📐 awaiting-Andrew doc) independently conditions its `DEL` on the same step-4 hydrated revision. The amendment was written **2026-05-30, a month before** that OCC pattern landed; its premise is stale, not wrong-in-principle. So the heavy proposal (persist `Revisions` at install → manifest → read back at uninstall) is **unnecessary** *and* **semantically wrong** — it would condition on the *install-time* revision (the wrong window: a legitimate post-install upgrade bumps a key's revision → spurious uninstall failure), whereas the **read-time** revision is the correct OCC token. The right fix is smaller and more correct than the one on file.

**The one design decision (no architectural fork).** Whether OCC is **default-on** or **opt-in**. I make it **default-on with no opt-out**: the client always conditions on the read-time revision. There is no legitimate reason to unconditionally tombstone — a conflict simply means "a declared key raced; re-run the uninstall" (the re-run re-reads at the new revision). Fail-closed is strictly better than the silent overwrite we have today, and it costs nothing (the `KVGet` already happens). This follows the "prefer a committed stance over a forgeable interim" reflex.

**Frozen-contract change (staged UNCOMMITTED in `main`).** One file, `docs/contracts/08-package-install.md`, three hunks — each *replacing a note that documents the now-closed window*, no shape change to any op:
- **§8.3** (uninstall OCC "deferred" note, lines ~125–134) → the client passes each declared key's read-time `KVGet` revision as `expectedRevision`; a raced key → whole-batch `RevisionConflict`, re-run to resolve. **[Fire 1]**
- **§8.6 "OCC"** (upgrade unconditional note, lines ~233–239) → update/tombstone conditioned on the diff-read revision. **[Fire 2]**
- **§8.7 "Out of scope"** (line ~245) → strike "Per-key uninstall / upgrade OCC … deferred". **[Fire 1 strikes uninstall; Fire 2 strikes upgrade]**

Per CLAUDE.md these stay **unstaged/uncommitted** for you; my commit carries only the design doc + the board row. Because the two fires land separately, you may ratify Fire 1 (uninstall) and commit only the §8.3 + the uninstall half of §8.7 first; the §8.6/upgrade hunk waits for Fire 2. The diff is the proposal — review it. **No overlap** with the three other in-flight uncommitted contract edits (atomic-batch-ceiling touches `02`/`03`; delete-verb touches `03`; script-read-posture touches `02`) — this is the only edit to `08`.

**No architectural fork** (Gateway / D1 read-path-auth / Vault / multi-cell / HA-NATS untouched). **No auth-surface change** — this is a mechanical concurrency guard on an admin-driven op; it grants nothing, reveals nothing, and only *tightens* (a formerly-silent overwrite now fails loudly).

**Ratifying this also closes** the F-011 request in `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` (left untouched by this fire — the design doc + the §8 edits are the record).

---

## 1. Problem and intent

### 1.1 The lost-update window

`Installer.Uninstall` (`internal/pkgmgr/installer.go:613`) tombstones every Core-KV key a package's install recorded in its `.manifest` aspect's `declaredKeys` (DDL / lens / permission / grant-link / role / index keys + the manifest aspect + the package vertex). It:

1. reads the manifest, collects `declaredKeys` (+ appends the manifest key and the package vertex, `installer.go:641`);
2. loops each key and `KVGet`s it purely to test existence — **discarding the entry** (`if _, err := i.Conn.KVGet(...)`, `installer.go:661`) — skipping any already-gone key (`ErrKeyNotFound`);
3. submits one `UninstallPackage` op whose `declaredKeys` are **bare `{key}` objects with no `expectedRevision`** (`installer.go:667`);
4. the `UninstallPackageDDLScript` tombstones each key **unconditionally**.

Because no `expectedRevision` is asserted, a concurrent Processor write to a declared key **between step 2's read and the step-3 commit** is silently overwritten by the tombstone. The whole batch is still atomic (no partial/mixed state), so the *only* relaxed guarantee is **lost-update protection on a key being uninstalled**. That is exactly the F-011 window, documented verbatim in `installer.go:646-657` and Contract #8 §8.3.

The **in-place upgrade** path (`Installer.Upgrade` → `diffManifest`, `internal/pkgmgr/upgrade.go:175`) has the *identical* window on its `update`/`tombstone` deltas (Contract #8 §8.6 "OCC", §8.7 "Out of scope"). It already `getCommitted`s each surviving key for the body diff (`upgrade.go:194`), so — like uninstall — the read-time revision is **already in hand** and merely discarded.

### 1.2 The kernel side is already done

The `UninstallPackageDDLScript` (`internal/bootstrap/install_ddl.go:123-165`) already accepts a `declaredKeys` entry as **either** a bare string **or** `{key, expectedRevision}`, and when an integer `expectedRevision` is present it sets `mut["expectedRevision"]` so the committed tombstone asserts it (per-key OCC). Its input schema (`install_ddl.go:262`) and the contract example (§8.3) already document the `{key, expectedRevision}` shape. **Only the client plumbing is missing** — the installer never captures a revision to pass. F-011 is a client-side gap, not a kernel gap.

### 1.3 Intent

Make uninstall (Fire 1) and upgrade (Fire 2) **condition every tombstone/update on the revision the key was read at**, so:

- a concurrent write to a declared key between the client's read and the commit **fails the whole atomic op** with a `RevisionConflict` (fail-closed) instead of silently overwriting it;
- the fix adds **no new persisted state** (no manifest change, no install-path change, no cross-op revision threading);
- the OCC token is the **semantically correct** one (read-time, not install-time), so a legitimately-upgraded key does not trigger a spurious conflict.

---

## 2. The mechanism — grounded, not assumed

### 2.1 A Core-KV key is its own subject; `KVGet.Revision` is the per-subject OCC token

Lattice Core KV is a standard NATS JetStream KV bucket. Every key `K` maps to the subject `$KV.core-kv.<K>` (`substrate/batch.go:176`, `kvBucketSubject`). The atomic-batch committer publishes each mutation to that subject and sets its OCC precondition with the header `Nats-Expected-Last-Subject-Sequence` (`batch.go:127-130`): `0` for a create-only, or the asserted revision for a conditioned update/tombstone. On the read side, `Conn.KVGet` returns `KVEntry.Revision = entry.Revision()` (`substrate/kv.go:53`) — the stream sequence of the **last** message on that key's subject.

Because a KV key has exactly one live subject, **"the last stream sequence on subject `$KV.core-kv.<K>`" is a single number**, and both `entry.Revision()` (what `KVGet` returns) and `Nats-Expected-Last-Subject-Sequence` (what the OCC header compares against) refer to it. They are the same value. This is the canonical NATS KV compare-and-set pattern (NATS 2.14, our pin per `docs/vendors.md`): `kv.Update(key, value, entry.Revision())` conditions on the revision from a prior `Get`; the atomic-batch path expresses the same precondition via the header directly.

### 2.2 In-repo proof (stronger than a doc citation)

Two independent, already-blessed usages condition on the `KVGet` revision as the per-subject OCC token — both would be broken today if the amendment's premise were true:

- **Processor §3.2 update-conditioning (`97afcd2`, shipped, CI-green).** Step-4 hydrate `KVGet`s each `contextHint.reads` key and stores `doc.Revision = entry.Revision` (`step4_hydrate.go:163`). `applyHydratedRevisions` sets the mutation's `ExpectedRevision` to that hydrated revision for every unconditioned `update`/`tombstone` (`commit_path.go:488-513`). That `ExpectedRevision` becomes the `Nats-Expected-Last-Subject-Sequence` header at commit (`batch.go:128-130`). On a conflict, `movedDefaultedKeys` re-reads with `KVGet` and compares `entry.Revision != conditionedRev` to attribute the race (`commit_path.go:544`) — i.e. it *authoritatively* treats the `KVGet` revision as the per-subject sequence.
- **Hard-delete mutation-verb design (parallel 📐 awaiting-Andrew).** Its §3.3 conditions the `DEL` on `Revision:<step-4 hydrated revision>` — the same read-time-revision OCC posture.

The uninstall/upgrade client is doing, at the client tier, exactly what the Processor already does at the commit tier: **read a key, remember its revision, condition the write on it.** There is nothing novel to invent.

### 2.3 Why read-time (not install-time) is the correct token

The OCC intent (Contract #3 §3.2, and §8.3's own words) is *"fail if the key changed between the **client's read** and the commit."* The client's read is the uninstall's existence-check loop / the upgrade's diff read — **not** the install. The amendment's persist-install-revisions proposal conditions on the *install-time* revision, which is the wrong window in two ways:

- **False positives.** A key legitimately modified after install — most obviously by a package **upgrade** (§8.6 rewrites DDL/lens meta-vertices in place, bumping their revisions) — would carry a stale install-time revision, so a later uninstall would spuriously `RevisionConflict` on a key nobody raced.
- **Heavier for no gain.** It requires capturing the install reply's `Revisions` (which `Installer.Install` currently discards, `installer.go:160-166`), persisting a `declaredRevisions` map into the `.manifest` aspect (a Contract #8 §8.1 shape change), and reading it back — all to end up with a *worse* token than the one the uninstall already reads for free.

Read-time revision has neither problem: it is captured at the exact moment the OCC window opens, and it needs no persistence.

---

## 3. The shape

### 3.1 Read path (P5) / write path (P2)

Unchanged. This is a pure Processor-mediated write: the client submits `UninstallPackage` / `UpgradePackage` ops to `ops.meta` (Contract #2), the Processor commits the atomic batch (P2 — sole Core-KV writer). No lens, no read-model, no new query surface. The only change is that the op's `declaredKeys` / delta mutations now carry an `expectedRevision` the client already read.

### 3.2 Fire 1 — uninstall (the filed F-011 item)

In `Installer.Uninstall` (`installer.go:658-669`), the existence-check loop becomes a **capture** loop:

```go
for _, k := range keys {
    entry, err := i.Conn.KVGet(ctx, CoreBucket, k)
    if err != nil {
        if errors.Is(err, substrate.ErrKeyNotFound) {
            continue // already gone — nothing to tombstone, nothing to condition
        }
        return nil, fmt.Errorf("pkgmgr: uninstall read %s: %w", k, err)
    }
    declaredEntries = append(declaredEntries, map[string]any{
        "key":              k,
        "expectedRevision": entry.Revision, // read-time per-subject sequence
    })
    tombstoned = append(tombstoned, k)
}
```

The `UninstallPackageDDLScript` already turns each `{key, expectedRevision}` into a revision-conditioned tombstone. No script change, no schema change.

**Conflict disposition.** If any declared key's current per-subject sequence at commit ≠ the asserted revision (a concurrent write landed in the window), the atomic batch is rejected — `wrong last sequence` → the Processor surfaces a conflict and the op-reply status is non-accepted. `Installer.Uninstall` maps that to a **typed `ErrUninstallConflict`** and the CLI prints an actionable message: *"a declared key was concurrently modified during uninstall; re-run `lattice-pkg uninstall <name>`"* (the re-run re-reads at the new revision and proceeds). Because the batch is atomic, a conflict leaves the package **fully installed** — never half-uninstalled. NATS does not name the failing subject in the rejection (`commit_path.go:515-528` documents this — the error carries only `wrong last sequence: N`), so v1 reports the class of failure, not the specific key; naming the raced key (an optional `movedDefaultedKeys`-style re-read) is deferred polish, not correctness.

**Retry stance.** v1 = **surface the typed conflict, operator re-runs.** An admin uninstall is interactive and rare; a silent auto-retry loop (mirroring the Processor's bounded commit-retry) buys little and hides the race. If a driver ever wants it, the Processor-side bounded-retry pattern is the precedent to copy — but that is not this fire (dead-scaffolding test: no consumer needs auto-retry today).

### 3.3 Fire 2 — upgrade (§8.6, the sibling)

The upgrade path shares the identical premise and fix. In `diffManifest` (`upgrade.go:175`):

- **`update` mutations** — `getCommitted` already `KVGet`s the surviving key for the body diff (`upgrade.go:194`). Extend `getCommitted` to also return the entry's `Revision` (it currently returns only the body `map[string]any`), and set `expectedRevision` on the emitted `update` mutation. This conditions the in-place DDL/lens update on the revision the diff read.
- **`tombstone` mutations** (old ∖ new — a key the new version drops, `upgrade.go:232-238`) — add a `KVGet` to capture the removed key's read-time revision and condition the tombstone, mirroring Fire 1's loop. (A removed key absent from KV → skip, as `diffManifest` already tolerates for the create branch.)
- **`create` mutations** — unchanged. A create is already conditioned `Nats-Expected-Last-Subject-Sequence: 0` (create-only, `batch.go:126-127`), which is its own OCC (fails if the key unexpectedly exists).

`installMutation` (`build.go:32`) gains an optional `ExpectedRevision *uint64`, threaded into the `UpgradePackage` op payload. The upgrade DDL script's per-key handling mirrors the uninstall script's `{key, expectedRevision}` acceptance (verify the `UpgradePackageDDLScript` accepts per-key `expectedRevision` on update/tombstone entries; if it does not yet, Fire 2 adds that acceptance to the script the same way §8.3's already exists — a small, symmetrical kernel edit, called out in the fire's scope).

Same conflict disposition as Fire 1 (`ErrUpgradeConflict`, re-run).

### 3.3.1 Orchestration

None. No Loom pattern, no Weaver convergence lens, no `@at`/`@every`, no directOp. This is a synchronous client → Processor op, exactly as install/uninstall/upgrade are today.

---

## 4. Contract surface

**Contract #8 (`docs/contracts/08-package-install.md`) — three hunks, staged UNCOMMITTED for Andrew.** All three *replace deferral notes describing the now-closed window*; none changes any op's payload/response shape (the `{key, expectedRevision}` shape is already in §8.3's example and the input schema):

1. **§8.3** — replace the "Per-key OCC is deferred" blockquote (lines ~125–134, which contains the stale premise) with: the client reads each declared key's revision via `KVGet` and passes it as `expectedRevision`; a raced key fails the whole atomic op with `RevisionConflict`; because the batch is atomic the package stays fully installed on conflict; re-run to resolve. **[Fire 1]**
2. **§8.6 "OCC"** — replace the unconditional-update/tombstone note (lines ~233–239) with the read-time-conditioned statement. **[Fire 2]**
3. **§8.7 "Out of scope"** — strike "Per-key uninstall / upgrade OCC (§8.3 / §8.6 window): deferred follow-up" (line ~245): uninstall struck in Fire 1, upgrade in Fire 2. **[both]**

**Contract #2 / #3 — build-to only, no change.** This design *uses* the §3.2 update-conditioning mechanism and the §2 envelope exactly as specified. `UninstallPackage`/`UpgradePackage` op shapes are unchanged.

**`cmd/processor/CONTRACT-AMENDMENT-REQUEST.md`** is **not** a frozen contract (it is a working request log). Its F-011 section is resolved *by this design* (more simply than it proposed). I leave the file untouched this fire — the design doc + the §8 edits are the record — and note it closes on ratification.

---

## 5. Reconciliation with the existing mental model

- **Didn't we already handle this?** No — the *kernel* half shipped in 1.5.5 (the script accepts `expectedRevision`), but the *client* never captured a revision to pass, so uninstall/upgrade have run unconditionally since. The gap is a client-side plumbing omission, tracked as F-011.
- **Didn't we decide `KVGet` can't give the per-subject revision?** That was the 1.5.5-era belief (2026-05-30). The §3.2 update-conditioning work (`97afcd2`, 2026-06-29) subsequently established — and shipped — precisely the opposite: the Processor conditions its own tombstones on the `KVGet`-read revision. This design brings the client into line with the platform's own now-proven pattern; it does not invent a mechanism.
- **Does this duplicate/contradict an established pattern?** It *mirrors* the established one (Processor §3.2; hard-delete design). No parallel machinery.
- **Does this introduce new state?** No. That is the headline advantage over the on-file proposal: **zero** new persisted state (no `declaredRevisions` in the manifest, no install-path change, no cross-op threading).
- **Interaction with the hard-delete design (parallel 📐).** Orthogonal. Hard-delete adds a `delete` verb (removes the key from the keyspace); this conditions whatever verb (`tombstone` today, optionally `delete` later) the uninstall emits. If hard-delete lands, a future uninstall could `delete` instead of `tombstone` — still conditioned on the read-time revision by this design. No collision; no shared edit (hard-delete touches `03`, this touches `08`).

---

## 6. Migration / compatibility & test strategy

**Compatibility.** Fully backward-compatible. The op shapes are unchanged; existing bare-string `declaredKeys` entries still parse (the script accepts both forms). A conflict is a *new* failure mode that only fires under genuine concurrency — the common (uncontended) uninstall/upgrade path is byte-for-byte the same commit as today, just with a header set.

**Tests (Fire 1).**
- *Unit* (`internal/pkgmgr/installer_test.go`): the `UninstallPackage` payload now carries `expectedRevision` matching each key's `KVGet` revision; an already-gone key is still skipped (no entry emitted).
- *Race / e2e* (ephemeral-stack): install a package, `KVGet` a declared DDL key to learn its revision, submit an `UpdateMetaVertex` to that key (bumping its revision) **after** the uninstall reads but **before** it commits — assert the uninstall returns `ErrUninstallConflict` and the package is **still fully installed** (no key tombstoned). A second, uncontended uninstall succeeds. The deterministic interleave is achievable with a Processor-commit hook or by reading-then-mutating-then-uninstalling in sequence against a shared bucket.
- *Regression:* the existing uninstall happy-path e2e (`make verify-package-*`) stays green — an uncontended uninstall commits identically.

**Tests (Fire 2).** Mirror the above for `Upgrade`: an update conditioned on the diff-read revision conflicts when the key races; the removed-key tombstone conflicts likewise; the uncontended in-place upgrade (F-004 e2e) stays green.

**Verification gates:** `go build ./...`, `make vet`, `golangci-lint run ./...`, `internal/pkgmgr` unit tests, and (per the verify-package gate-gap note) the out-of-band `make verify-package-*` that CI runs.

---

## 7. Risks & alternatives

### 7.1 Risks

- **A conflict on a *legitimate* concurrent admin action.** If an operator upgrades a package's DDL while another uninstalls the same package, the uninstall conflicts. This is *correct* — two admins racing on the same package's keys should not silently lose one's write; the re-run resolves it. Bounded by "same package, same key, same instant" — vanishingly rare for an admin op.
- **False sense of atomic snapshot.** Keys are read one-at-a-time, so different keys carry read timestamps microseconds apart. This is fine: the atomic batch's *per-key* CAS conditions each key on its own read-time revision independently; there is no requirement for a consistent cross-key snapshot (a race on *any* conditioned key fails the *whole* batch — the strongest posture).
- **Kernel-protected keys.** Orthogonal — the §8.4 commit-time `ProtectedKey` guard still rejects a tombstone of a protected root regardless of OCC. OCC and protection compose (both are fail-closed).

### 7.2 Alternatives considered

- **A. The on-file proposal — persist install-time `Revisions` into the manifest, thread through.** *Rejected.* Heavier (install-path change + a Contract #8 §8.1 manifest-shape change + cross-op threading) **and** conditions on the wrong window (install-time, so a legit post-install upgrade spuriously conflicts a later uninstall). The read-time token is both simpler and more correct — this is the "prefer the simplest extension of what exists" rule, and here the simplest extension is *also* the only correct one.
- **B. Leave it unconditional (status quo).** *Rejected.* A silent lost-update on the auth/schema plane (declared keys are DDLs, lenses, permissions, grant links) is a real correctness gap; the fix is ~10 lines and free (the `KVGet` already runs). "Acceptable for an admin op" was a 1.5.5 scoping call, not a design preference; the cost/benefit now clearly favors closing it.
- **C. Opt-in OCC (a flag).** *Rejected.* No caller wants the unconditional path; a flag is surface with no consumer. Default-on, no opt-out (§ For-Andrew decision).
- **D. Bounded client-side auto-retry on conflict.** *Deferred (dead-scaffolding test).* No consumer needs it; a typed conflict + operator re-run is honest and sufficient. The Processor's bounded-retry is the precedent to copy *if* a driver appears.

---

## 8. Fire-by-fire decomposition (for the Lattice Steward)

Each fire is independently shippable and green; Fire 1 realizes the filed F-011 value alone (Fire 2 is a strict sibling, not a dependency).

- **Fire 1 — uninstall per-key OCC (the filed F-011 item).**
  - `Installer.Uninstall`: capture `entry.Revision` in the read loop; emit `{key, expectedRevision}`; map a commit conflict to a typed `ErrUninstallConflict` with an actionable CLI message.
  - No script/schema change (the `UninstallPackageDDLScript` already accepts it).
  - Contract edit: §8.3 rewrite + strike the uninstall half of §8.7 (committed by Winston on ratification, with this fire).
  - Tests: unit (payload carries revisions) + a race e2e (conflict leaves the package fully installed) + happy-path regression.
  - *Value realized:* the filed lost-update window is closed. Self-contained.

- **Fire 2 — upgrade per-key OCC (§8.6 sibling).**
  - `getCommitted` returns the entry revision; `diffManifest` conditions `update` on it and adds a `KVGet` to condition the removed-key `tombstone`; `installMutation` gains `ExpectedRevision *uint64` threaded into the `UpgradePackage` payload.
  - Verify/extend the `UpgradePackageDDLScript` to accept per-key `expectedRevision` on update/tombstone entries (symmetrical with the uninstall script); add it if absent.
  - Contract edit: §8.6 "OCC" rewrite + strike the upgrade half of §8.7.
  - Tests: mirror Fire 1 for `Upgrade`; F-004 upgrade e2e stays green.
  - *Value realized:* the in-place-upgrade lost-update window is closed. Independent of Fire 1.

---

## 9. Open questions — resolved

1. **Read-time or install-time revision?** → **Read-time** (§2.3). Correct window; no persistence.
2. **Default-on or opt-in?** → **Default-on, no opt-out** (§ For-Andrew). No legitimate unconditional caller.
3. **Conflict = auto-retry or surface?** → **Surface a typed conflict; operator re-runs** (§3.2). Auto-retry deferred (no consumer).
4. **Name the raced key in the error?** → **No (v1);** NATS omits the failing subject, and naming it needs an extra re-read. Report the failure class; defer key-naming polish.
5. **One fire or two?** → **Two** (§8). Uninstall is the filed item and ships alone; upgrade is a strict sibling sharing the mechanism. Not one fire (the two paths have distinct call sites and distinct e2e), not more (coupled by the identical premise/fix — the "fewer, larger fires" reflex keeps them adjacent).
6. **Contract change needed?** → **Yes, three deferral-note rewrites in Contract #8 (§8.3/§8.6/§8.7)** — staged uncommitted; no op-shape change.

---

## 10. Adversarial pass (self-run, Designer-lane obligation)

This is a small, single-plane change (no orchestration, no auth surface, no new state), so a full `bmad-party-mode` is disproportionate; I ran a focused adversarial pass on the load-bearing claim and the fail-closed direction:

- **Is the premise-falsification actually sound, or am I the one who is wrong?** The claim rests on NATS KV semantics (a key = one subject; entry revision = that subject's last stream seq) **plus** two in-repo proofs that would already be broken if it were false (Processor §3.2 conditions its own tombstones on the `KVGet` revision and ships CI-green; the hard-delete design does the same). If `KVGet.Revision` did *not* match the per-subject header, `applyHydratedRevisions` would mis-condition every §3.2 update and the platform's update-conditioning would be inert — it is not. The falsification holds. *(Residual: a build-time verification against the live substrate is the Steward's Fire-1 gate — the race e2e both proves the fix and, incidentally, re-confirms the premise.)*
- **Does read-time OCC introduce a NEW failure the status quo lacked?** Yes — a `RevisionConflict` on a genuine race. But that path today is a **silent overwrite** (data loss); converting silent loss into a loud, retryable failure is the intended direction, not a regression. Fail-closed verified: absence of a race → identical commit; presence of a race → whole batch rejected, package left fully installed (atomic), never half-torn.
- **Could conditioning break the uncontended happy path?** No — an uncontended key's current per-subject seq equals its read-time revision, so the CAS passes; the commit is identical to today's plus a header. The existing `verify-package-*` e2e is the regression guard.
- **Multi-key atomicity under partial conflict.** One conflicted key rejects the entire batch (NATS abandons an atomic batch on any member's failed precondition, `batch.go`/ADR-50). So there is no "some keys tombstoned, one didn't" state — the exact property we want for uninstall.

Findings folded in: the conflict-disposition (typed error, package-stays-installed, re-run) and the "NATS doesn't name the failing key" limitation are now explicit in §3.2; the false-positive argument for read-time-over-install-time is in §2.3.
