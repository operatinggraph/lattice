# Lens Output re-activation — design + fire brief

**Status: ✅ Winston-ratified — build-ready (2026-09-03).** No architectural fork, no contract change.
Andrew's chat directive (2026-09-03): the row was filed `📐 needs designer pass`; plan and build in one fire.
**Board row:** `[Refractor] A package upgrade's lens Output change is refused and the OLD shape keeps projecting`
(`planning-artifacts/backlog/lattice.md`).

## 1. Problem, grounded (main `cf674b0e`)

- **Mechanism.** `cmd/refractor/reload.go`'s `hotReloadRefusal` refuses any difference in the §6.13 Output
  descriptor (all ten fields) because the envelope, delete-key derivation, sweep plan and guard predicate are
  installed once, by `projection.InstallActorAggregate`, at activation, and neither swap (INTO adapter, MATCH
  rule) re-runs it. A refusal is a health error + log line; the lens keeps serving its activated spec.
  `internal/refractor/reloadpin` restates the pin at `lattice-pkg apply` time, which prints "requires
  re-activation" as a warning and reports success. Remedy on record: restart Refractor, or delete and
  re-create the lens definition.
- **Consequence.** The most common package lens edit — adding a `bodyColumns` entry — never reaches the
  running stack. Live instance: `b569fd2c` (cafe-domain 0.11.29, `freshUntil` added to
  `cafeStaleTabSettlement`, logged Done 2026-09-02 00:14) projected nothing new until an unrelated fire
  cycled Refractor at 2026-09-03 02:46 — 26 h. clinic-reminders 0.10.9 (`appointmentReminders` +2 columns)
  identically. That restart cleared both faults (health at 09:00 PDT lists no Output refusal).
- **Premise check — "the out-of-band Postgres DDL posture covers this" (Andrew's hypothesis): it does not
  apply.** The Output descriptor exists only for `projectionKind: actorAggregate` lenses, which project
  NATS-KV buckets (`EnableProjectionGuard` and the sweep enrolment refuse anything but a NATS-KV adapter). A
  KV bucket has no out-of-band schema step, and `docs/components/_packages.md` (F-004) + CLAUDE.md promise
  "no restart" for a package edit. A Postgres target's column addition is `targetConfig.columns`, not
  Output — that posture stands, untouched.
- **Premise check — "a restart re-projects": it does not.** A restart re-attaches the durable at its cursor
  (no replay); rows already written heal only through the convergence sweep's bounded deep-verify
  (`BusinessSweepInterval` 5 min, 25 actors a tick). Re-activation through the removal path deletes the
  durable, and the fresh one starts `DeliverLastPerSubject` — a full re-projection of the current corpus.
- **Premise check — the row's `no-pattern: in-place re-activation of a live lens`: false.** The remedy's
  own second option (delete + re-create the definition) *is* the pattern: `remover.remove` (the tombstone
  path, `pipelineDeleter`) followed by `activateIfNotRegistered` (the load path). Both already run on
  `CoreKVSource`'s single dispatch goroutine — the same one `reloader.update` runs on. This fire composes
  them on the update seam. Steward `📋`, not designer `📐`.

## 2. Alternatives

| # | Option | Verdict |
|---|---|---|
| A | **Delete the refusal**: an Output change re-activates the lens through the existing removal + activation paths | **Chosen** |
| B | Keep the refusal; add a `lattice lens reactivate` operator verb | Rejected — a manual step after every column addition; the filed harm survives |
| C | Keep the refusal; `refresh-*` / `reinstall-package` cycle Refractor when `ReactivationRequired` is non-empty | Rejected — a process-wide restart for one lens, installer and Refractor need not share a host, and a restart does not re-project (§1) |
| D | Not an issue: posture | Refuted (§1) |
| E | Re-install the envelope in place on the running pipeline | Rejected — the plane holders the pipeline, its health entry and its auditor carry are read from the handler and audit goroutines while a reload runs on the dispatch goroutine (reload.go's authPlane comment); activation is the one path that owns them |

B + C in combination still leave the no-re-projection gap; only A converges the rows.

## 3. Mechanism

In `reloader.update`, once the refusal set (unchanged, minus the Output clause) passes:

```
if !outputDescriptorsEqual(entry.output, newLens.Output) { rl.reactivate(entry, old, newLens); return }
```

`reactivate`, in order:

1. **Pre-flight** the new rule's pure activation checks, so a malformed edit keeps the OLD lens running — the
   one property of the refusal worth keeping: `projection.Compile(newLens)` when `IsActorAggregate` (parses
   and validates the descriptor) and `projection.CapReadWriterRefusal(newLens)`. Failure → `rl.refuse` as
   today (health error; the lens keeps its activated spec).
2. **Deactivate** — `rl.deactivate(old)`, wired in `main.go` to `rm.remove`: RemoveConsumer → cancel → wait
   for Run → forget the cap-read producer → delete the health entry → unregister. The pipeline object and
   its adapter survive (Run never closes the adapter).
3. **Truncate** — `entry.pipeline.TruncateForReactivation(ctx, keyShapeChanged)`, a new pipeline method
   with `resolveTruncate`'s semantics: a **guarded** target forces the purge, because the §6.2 watermark
   declines an equal-seq replay and without it the new shape never lands (`rebuild.go`'s own account);
   but only when `RebuildTruncateIsScoped()` (own prefix or own table) — otherwise warn and skip, mirroring
   `truncateIsSafe`, and strictly safer than `Rebuild`, which would purge an unscoped guarded bucket whole.
   Runs through `truncateTarget`, so a cap-read producer's grant sink hears every purged key.
   `keyShapeChanged` = `AnchorType` | `OutputKeyPattern` | `KeyColumn` | `EntryKeyColumn` differ
   (nil-safe): the old keys are unaddressable by the new lens — the MATCH-narrowing orphan class
   (`taxRebuildTruncate`). A content-shape change on an unguarded target does not truncate: the replay
   overwrites in place with no absence window.
   Order: after Run has returned (no in-flight old-shape write lands after the purge) and before activation
   (the registry is keyed by lens ID, so old and new cannot coexist). The purge is re-derived by the
   activation that follows; step 1 is what makes that activation's descriptor-class failures impossible.
4. **Activate** — `rl.activateForTaxonomy(newLens)` (= `activateIfNotRegistered` → `startPipeline`): fresh
   durable, `DeliverLastPerSubject` replay, rows re-projected in the new shape. A taxonomy-unknown refusal
   queues exactly as a first load would.
5. **Post-check** — if the registry has no entry and the lens is not queued in `refused`, record
   `re-activation after an Output change failed — the lens is dark` on the old reporter (its entry was
   deleted; `RecordError` re-creates it), so the failure reaches health, not only the log.

**Unchanged:** the other pins — `secureColumns`, `protected`, `grantTable`, the guarded write surface, the
authorization plane — each carries a data-lifecycle argument (stranded rows, RLS re-verify) this fire does
not adjudicate, and no package upgrade flips them. The health entry is re-created (carry-forward state
resets, as on delete + create). `reloadpin` drops the `output` pin: `lattice-pkg apply` stays silent for an
Output edit because it now applies.

## 4. State lifetime

| state | reset | carry | order |
|---|---|---|---|
| registry entry | removed by deactivate, inserted by activation | none | one goroutine |
| durable `refractor-<id>` | deleted (RemoveConsumer), recreated (RunOn) | ack floor deliberately NOT carried — the replay is the point | before cancel |
| health entry | deleted, recreated | none | after Run returns |
| target rows | purged iff resolved + scoped; else overwritten in place by the replay | old-key orphans only when unscoped (warned) | after Run returns, before activation |
| refused-for-taxonomy queue | cleared by deactivate; re-queued by activation if unknown | — | — |
| cap-read producer census | forgotten after stop; re-noted at install | — | — |

## 5. Fire brief

**Scope sentence (verbatim row):** *A package upgrade's lens Output change is refused and the OLD shape keeps
projecting.* Scope-diff gate: every increment below serves that sentence; nothing adjacent substituted.

**Touch-list (verified live at `cf674b0e`):**
- `internal/refractor/pipeline/rebuild.go:329` `resolveTruncate` · `:378-397` the rebuild's truncate step ·
  `grantchange.go:264` `truncateTarget` · `pipeline.go:817` `RebuildTruncateIsScoped` → add
  `TruncateForReactivation(ctx, requested bool) (bool, error)`.
- `cmd/refractor/reload.go:24` `reactivateRemedy` · `:67-181` `hotReloadRefusal` (drop the Output clause at
  `:114`; keep `outputDescriptorsEqual` `:183`) · `:352` `reloader` (add `deactivate func(*lens.Rule)`) ·
  `:441` `update` (the Output arm sits after the refusal check, before the kind switch).
- `cmd/refractor/main.go:2081-2108` rl wiring (`rl.deactivate = rm.remove` once `rm` exists) ·
  `cmd/refractor/delete.go:120-127` `remover` doc ("never invoked for a spec UPDATE" is no longer true).
- `internal/refractor/reloadpin/reloadpin.go:44-47` drop `output` + package doc · `reloadpin_test.go:45` ·
  `cmd/refractor/reload_test.go:309-341, 894-914` · `internal/pkgmgr/reactivation_test.go:52-70`.
- `docs/components/refractor.md:884` lifecycle step 8 (`:229` stays true).

**Precedents to mirror:** `TestReloaderUpdate_AcceptedMatchChangeReprojectsExistingEntries` (embedded-NATS
running pipeline, `mcPollUntil`), `TestReloaderUpdate_NarrowingMatchChangeRetractsTheDroppedLabelsRows`
(`ApplyTruncateScope`), `rebuild_force_truncate_internal_test.go` (`resolveTruncate` table),
`truncate_grant_change_test.go` (grant sink hears the purge), `installSinklessCapReadProducer`
(`InstallActorAggregate` wiring inside `cmd/refractor` tests).

**Increments (one fire, all this run) + green checks:**
1. Pipeline: `TruncateForReactivation` + internal tests — guarded forces; requested + scoped purges; unscoped
   declines even when guarded; the grant sink hears every purged key. `go test ./internal/refractor/pipeline/`.
2. `cmd/refractor`: the Output arm, wiring, unit tests — the ten-field table drives deactivate → truncate
   (requested iff key-shape) → activate in that order and never `HotReloadInto`; Output dropped / added
   re-activates; Output + bucket change on a guarded lens is still refused with no deactivate; a malformed
   Output keeps the old lens running; `TestPinnedFieldsMatchTheRefusalSet` minus `output`. E2E on embedded
   NATS with the REAL deleter + an activation closure: an unguarded (`emptyDoc`) actor-aggregate lens whose
   `bodyColumns` grows re-projects the stored document with no new Core KV event and zero health errors; a
   guarded (`delete`) variant proves the forced purge lands the new shape. `go test ./cmd/refractor/`.
3. `reloadpin` + `pkgmgr` tests + docs. `go test ./internal/refractor/reloadpin/ ./internal/pkgmgr/`.

**Close:** `go build ./...`, `make vet`, `golangci-lint run ./...`, every `scripts/lint-*.go` CI runs,
`go test ./internal/refractor/... ./cmd/refractor/... ./internal/pkgmgr/...`; no interface changes (Pipeline
gains a method), so the build-tagged harnesses are unaffected — `make test-lease-convergence` as the nearest
smoke. Review: one cold opus adversarial pass over the whole diff (a lifecycle seam is posture-changing).
Merge → `make cycle-refractor` → health: `lensesRegistered` unchanged, no re-activation error → live round
trip on one lens (edit its Output through the installer's own meta-write path, observe the row rewritten —
KV entry revision advances, column present, no fault — then revert, observe again; the stack ends identical
to the repo).

**Dossier classes carried in (refractor.md):** a lifted refusal — re-derive the boundary from the consumers
the refusal protected (here the guard watermark: an equal-seq replay is declined, hence the forced purge) ·
a refuted reason lives in more documents than one — grep `not hot-reloadable` / `re-activat` · a two-layer
seam green at each layer — the e2e drives `rl.update` with the real deleter, not fakes · new state needs a
lifetime — §4.

**Non-goals:** the other pins; an operator verb; purging a *tombstoned* lens's rows (they persist today; no
consumer names it); `reloadpin`'s remaining pins.

## 6. Build note

**Shipped in one fire (2026-09-03), one worktree, one code commit; SHA in the board's Done-log.** Two cold
adversarial passes (opus) ran over the diff; every finding was fixed in-fire, none filed.

**Deviations from §3, all in the direction of a narrower licence:**
- **Pre-flight is the whole guard, not a flag.** A re-activation is refused unless BOTH the running target and
  the new rule's target are `nats_kv` (the descriptor's only home), the new descriptor compiles, and the
  cap-read closure admits it. The purge's target gate was first written as a conjunct on `requested` — inert,
  because `resolveTruncate` forces a purge on any guarded adapter, and a protected Postgres adapter is
  guarded, truncatable and unscoped. Refusing by construction is what closes it.
- **Ownership, not prefix.** `KeyPrefix` scopes a listing and admits siblings (`cap.` contains
  `cap.roles.` / `cap.svc.` / `cap.ephemeral.` / `cap.role-by-operation.`); an automatic purge over the listing
  would have wiped four producers' rows on a `bodyColumns` edit to the kernel `capability` lens.
  `ApplyTruncateScope` now binds `OutputDescriptor.OwnsKey` (the `AnchorFromKey` round trip, plus the
  doc-mode parent for a perEntry descriptor) and `truncateKeys` keeps only owned keys — every truncate path
  inherits it. Census: all 30 shipped patterns round-trip, no cross-claims.
- **A clear keyed to what it wrote.** The clean-registration path cleared every writer's `LastError`, erasing
  the purge-failure diagnosis seconds after it was raised; `Reporter.ClearLastErrorIf` clears only the
  narrowed-filter fallback message it owns.
- **Teardown is consumed.** `remover.stop` returns the deleter's error (and `errLensNotRunning` when the
  registry had nothing); a stop that failed refuses the re-activation before any purge, records the failure
  on the still-present health entry, and withholds `unregister` so the operator delete op remains the retry.
- **A dark lens is a fault plus an INFRA pause** — the one pause kind the pump resumes by probing, so the next
  activation heals it; a structural pause would have outlived the restart the remedy names.
- **`replayCannotOverwrite`** widens the purge trigger to `emptyBehavior → skip` (rows for empty actors are
  never rewritten), and a `projectionKind` flip re-activates like an Output edit (`pipelineEntry.projectionKind`).
- **A descriptor ARRIVING** (plain → actor-aggregate) re-activates without a purge: the old adapter carries
  no scope, and the plain lens's rows are not addressable by the new descriptor — the outcome a tombstone
  or a restart leaves; logged as a Warn naming it.

**Review classification (close pass):** design-gap 3 (prefix-as-ownership; the over-broad clear; the pre-flight
set), implementation-bug 2 (the inert purge flag; the unchecked teardown), brief-gap 2 (`skip` empty behaviour;
`projectionKind`), convention 1 (history-narrating comments). Dossier: one entry appended, one mechanized entry
retired (`docs/components/refractor.md`).

**Live proof:** recorded below once the round trip runs (§5 Close).
