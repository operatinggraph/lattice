# Protected-lens provisioning: out-of-band + verify-and-pause (retire the RLS DDL-ownership exception) — design

**Status: ✅ Andrew-ratified (2026-06-29)** · Contract #6 §6.14 edit **ratified + committed** · Designer fire
2026-06-29 (Winston) · Lattice lane, Refractor read-path (D1.3) · originated from Andrew's question "the
out-of-band DDL decision was paired with Refractor *pausing* a lens on adapter error — can we use the same
approach for protected lenses?" · **Ready for the Lattice Steward** (Fire 1 verifier+gate = full 3-layer).

---

## For Andrew (one-look ratification)

**What it does (two lines).** Today Refractor **runs the DDL** for protected + grant Postgres tables
(`CREATE TABLE` / `FORCE ROW LEVEL SECURITY` / `CREATE POLICY` in `adapter/rls.go`) — the one exception to
the standing *"Postgres table creation & maintenance is out-of-band"* principle. This design **removes that
exception**: the operator provisions those tables out-of-band like every other Postgres target, and Refractor
instead **actively verifies the security posture and pauses the lens fail-closed** if it's absent — reusing
the existing `Probe → ConsumerSupervisor` pause/resume machinery that already backs the out-of-band model for
plain tables.

**Architectural fork:** **none.** It's a posture choice (provision-vs-verify), not a new boundary. It reuses
the shipped supervisor pause machinery and the shipped `Build*TableDDL` string generators (repurposed: stop
*executing* them, keep them as the operator runbook + the verifier's expected-shape reference).

**Frozen-contract change: yes — Contract #6 §6.14 (staged UNCOMMITTED in `main`).** One sentence flips from
*"Every protected table **is created with** ENABLE+FORCE ROW LEVEL SECURITY"* to *"is **provisioned
out-of-band with** … ; Refractor **verifies** the posture at activation and **pauses fail-closed** if
absent."* The **D1 H3 guarantee is preserved verbatim** — a table whose policy was never generated still
denies all rows; it becomes a *verified precondition* instead of a *provisioned fact*. The edit is the
proposal — review the diff.

**Why it's worth doing (and why it's safe — the crux).** It restores a single consistent principle (all
Postgres DDL out-of-band) and removes Refractor's only runtime-DDL footprint. It's safe — arguably *safer* —
because the **security-load-bearing check is exactly one bit: `relforcerowsecurity = true`.** With FORCE RLS
on, **every** policy/column mistake an operator can make **fails closed** (the table over-denies — a visible
outage — it can never over-share). So the worst an out-of-band mistake produces is a *paused lens + a Health
alert*, never a silent leak. And unlike create-once provisioning (which never re-checks), a posture Probe on
the periodic heartbeat **continuously re-verifies** — catching a later `ALTER TABLE … NO FORCE ROW LEVEL
SECURITY` that today's approach would miss.

**Recommendation:** ratify; build it as a revision to the in-flight D1.3 provisioning. It also cleanly
subsumes **F2** from the prior fire (the protected adapter's missing seq-guard): the verifier asserts
`projection_seq` exists, so the adapter can honor it with no DDL.

---

## 1. Problem + intent

**The exception.** `cmd/refractor/main.go buildAdapter` calls `PostgresGrantWriter.Provision()` and
`ProvisionProtectedTable()` at lens activation; `adapter/rls.go` runs `CREATE TABLE IF NOT EXISTS`,
`ALTER TABLE … ENABLE`/`FORCE ROW LEVEL SECURITY`, and `DROP/CREATE POLICY`. This is the **only** place
Refractor issues runtime DDL. Every other Postgres target is provisioned **out-of-band** — the adapter
"issues no DDL" ([refractor.md:62](docs/components/refractor.md)), and a structural mismatch (missing column)
surfaces as a **write error → the pump pauses the lens → re-probes → auto-resumes** when the operator fixes
the table. That pause-on-trouble net is the established out-of-band story for plain tables.

**Why protected was carved out** ([refractor.md:68-74](docs/components/refractor.md)): *"Refractor owns the
provisioning so schema and policy cannot drift from the projection, and **FORCE RLS is structural rather than
a checklist item**."* The fear: a forgotten `FORCE ROW LEVEL SECURITY` = silently world-readable PII.

**The intent here (Andrew's question).** Keep the security guarantee, but deliver it the *same way* the rest
of the Postgres surface works — **out-of-band provisioning + pause-on-trouble** — so there is **one**
principle, not a security-plane exception. The move that makes this sound is to upgrade the pause net from
*passive* (write-error) to **active** (posture verification), because the RLS property is invisible to the
write path (below).

---

## 2. The shape

### 2.1 The one subtlety: a missing RLS posture produces NO write error

A structural mismatch (missing/renamed column) makes an `INSERT` fail → the existing passive pause catches
it. But a **missing/disabled RLS policy or FORCE-RLS produces no write error** — writes to an unlocked table
succeed fine; the table is just world-readable on the *read* path. So a naive "pause on adapter error" would
leave a silent **fail-open**. This is precisely why protected wasn't already out-of-band — and the gap the
design must close with an **active** check.

### 2.2 The security-load-bearing assertion is `FORCE ROW LEVEL SECURITY = on`

The verification gates three things, but they are not equal:

- **`relforcerowsecurity = true` — SECURITY-critical.** With FORCE RLS on, a *missing or wrong policy*
  **deny-alls** (over-denies, never over-shares — D1 H3). So this single bit is the only thing standing
  between "projecting PII" and "a leak." If it's off → **pause fail-closed** (never project a protected row
  into a world-readable table).
- **Expected columns present** (`authz_anchors text[]`, `projection_seq bigint`, key + body cols) —
  **functional.** Their absence would fail the write anyway; verifying up front turns a per-row write error
  into a clean activation pause + actionable message. (Also the seam that lets the adapter seq-guard — F2.)
- **A `FOR SELECT` policy present** — **functional, not a leak vector.** With FORCE RLS on, a missing policy
  is a safe outage (deny-all), not a leak; we still pause so the operator learns the read model is dark.

So the posture Probe is *fail-closed on the one bit that matters* and *fail-functional on the rest* — and
crucially **no operator mistake can produce over-sharing**, only over-denial.

### 2.3 The mechanism — reuse the supervisor's probe-before-drain path

> **⚠️ GROUNDING CORRECTION (Steward, 2026-06-30 — folded by Winston, impl-ratified).** The original
> "**no new pump logic** / start the lens infra-paused with the primitives that exist" framing below is
> **wrong on one load-bearing point**, found by grounding the pump in code before building it:
> **`ConsumerSpec` has no initial-pause field, and the pump cannot start paused at first activation.**
> `newPumpState()` ([consumer_supervisor_pump.go:59](internal/substrate/consumer_supervisor_pump.go))
> begins with an **empty** reason set; the **only** pre-first-drain seeding is `restoreState` →
> `Health.Load` ([pump.go:373-380](internal/substrate/consumer_supervisor_pump.go)), which restores a
> *persisted* paused state on a **restart** — there is none at a fresh activation. So a newly-activated
> protected lens has **zero pause reasons → drains and projects the first batch BEFORE any posture
> verification → fail-OPEN**, the exact leak this design exists to prevent. The `waitWhilePaused →
> runProbeLoop` "probe-before-drain" path at :226-228 only runs when the pump is *already* infra-paused;
> nothing puts it there at activation. **Therefore the fail-closed activation gate requires a NEW substrate
> seam**, not just adapter Probe rewiring — see the corrected mechanism + re-decomposition (Fire 0) below.
> (This is an impl-level mechanism correction — no contract/fork change; Contract #6 §6.14, already
> committed, is untouched.)

**The corrected mechanism.** The fail-closed activation gate needs the pump to be **infra-paused before its
first drain**. Add a minimal substrate seam: a `ConsumerSpec.InitialPause PauseReason` field (zero-value =
unpaused, today's behaviour for every existing consumer), seeded into `st.reasons` in `runPump` **after**
`restoreState` finds no persisted state and **before** the first drain. With `InitialPause: PauseInfra`, the
pump enters `waitWhilePaused → runProbeLoop` and **probes before draining** — exactly the inversion §2.3
relied on, now actually wired. Persisted health state (a restart) still wins (restore runs first), so this
only governs the *first, never-yet-activated* run. This is additive and backward-compatible (every current
spec leaves `InitialPause` zero → unchanged).

Then the two existing primitives compose as the design intended:

1. **Probe-before-drain when infra-paused.** `waitWhilePaused → runProbeLoop`
   ([consumer_supervisor_pump.go:226-228](internal/substrate/consumer_supervisor_pump.go)) probes BEFORE the
   first drain and only proceeds to project once the Probe passes — but **only because `InitialPause` now puts
   the pump there at activation** (the correction above).
2. **`PauseInfra` auto-clears on a passing Probe** ([spec.go:50-51](internal/substrate/consumer_supervisor_spec.go))
   — so the UX is exactly Andrew's: posture absent → lens paused (Health `CapabilityLensPaused`, error) →
   operator provisions the table out-of-band → next Probe passes → **auto-resume, no operator Resume, no
   Refractor restart.** (Infra, not `PauseStructural`, precisely because it self-heals on operator action.)

So the per-message path is unchanged; we (a) add the `ConsumerSpec.InitialPause` substrate seam (Fire 0),
(b) make the protected/grant adapters' `Probe` do posture verification instead of `pool.Ping`, and
(c) register those lenses with `InitialPause: PauseInfra`.

### 2.4 What runs (read-only catalog queries)

`VerifyProtectedTable(pool, table, keyCols, body)` and `VerifyGrantTable(pool)` — read-only, no DDL, no
writes:

- columns + types: `information_schema.columns` (assert keys, body cols, `authz_anchors` is `ARRAY`,
  `projection_seq` is `bigint`);
- FORCE RLS: `SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = $table::regclass` (assert
  both true);
- policy: `SELECT 1 FROM pg_policy WHERE polrelid = $table::regclass AND polcmd IN ('r','*')` (assert a
  SELECT-applicable policy exists).

The expected shape is the **same `BuildProtectedTableDDL` / `BuildGrantTableDDL`** that exists today — kept
as the single source of truth, now consumed by the verifier (expected columns) and the operator runbook
(§3), rather than executed.

### 2.5 Continuous re-verification (the "stronger than today" part)

Fold the posture check into the periodic Refractor heartbeat (the existing `CapabilityLensProvider` /
liveness-alert machinery, which already runs per-cycle). A protected lens whose FORCE-RLS was turned off
*after* activation raises a §5.5 `issues[]` entry and **re-pauses** (infra) → re-probes → resumes when fixed.
Create-once provisioning never re-checks; this does. Optional but cheap (it's the same read-only query on a
timer) and strictly stronger.

### 2.6 P-invariants

P2 (Processor sole Core-KV writer) — untouched (Refractor writes its own lens targets). P5 — untouched
(apps still read the RLS-locked table). The verifier reads the **Postgres catalog** (operational metadata),
never Core KV or a lens. No new keys (Contract #1 N/A). The change is *removing* writes (DDL), not adding any.

---

## 3. Contract surface + dev ergonomics

**Frozen-contract: Contract #6 §6.14 — staged UNCOMMITTED in `main`.** The enforcement bullet currently
reads *"Every protected table **is created with** `ENABLE`+`FORCE ROW LEVEL SECURITY`, so a table whose
policy was never generated denies all rows."* The edit flips the **provisioning actor** while preserving the
**guarantee**: the operator provisions out-of-band; Refractor **verifies `FORCE ROW LEVEL SECURITY` (+
columns + a SELECT policy) at activation and pauses the lens fail-closed if absent**, re-verifying on the
heartbeat. The deny-all-on-missing-policy property (D1 H3) is unchanged — it is now a *verified precondition*.
No other §changes; the §6.2-guard / authz-anchor / no-public-by-omission text is untouched. **Affected
consumers:** Refractor (Provision→Verify), the operator (now owns the RLS DDL runbook), and the §6.14 author
note about provisioning.

**Operator runbook + dev ergonomics (out-of-band ≠ manual-only).** `Build{Protected,Grant}TableDDL` stop
being *executed* but are surfaced two ways so nobody hand-writes RLS SQL:
- a **`lattice refractor emit-ddl [--lens <id> | --grant-table]`** CLI that prints the exact DDL for a lens
  (operator runs it against their DB out-of-band — the migration), and
- a **dev `make` target** (`make provision-readpath` or folded into `up-full`) that applies the same DDL to
  the dev Postgres, so the local stack is one command as today.

This keeps Refractor out of *runtime* DDL while making correct provisioning a copy-paste, not a research
project — exactly the posture of the existing `deleteMode: soft` column contract (refractor.md:62), now
extended to RLS.

---

## 4. Reconciliation with the existing mental model

- *"Didn't we deliberately make Refractor own this so FORCE RLS is structural, not a checklist item?"* Yes —
  and this keeps it structural, by **verifying** it rather than **creating** it. The anti-pattern that
  rationale guarded against (a forgettable checklist item that silently leaks) is closed harder: a forgotten
  posture **pauses the lens fail-closed**, it doesn't quietly serve. "Structural" becomes "enforced by
  refusing to run," which also covers post-creation drift the create-once path never re-checked.
- *"Does this duplicate machinery?"* No — it *reuses* the supervisor pause/probe loop (the same net plain
  out-of-band lenses already ride) and the existing `Build*TableDDL`. It **removes** the bespoke runtime-DDL
  path; net-negative surface.
- *"Does this introduce new state?"* No new persistent state. The verifier is stateless read-only catalog
  queries; the pause state already exists in the supervisor/HealthSink.
- *"Is the grant table different?"* It's the shared `actor_read_grants` referenced by every protected
  policy, so the operator provisions it **first** (runbook ordering) and `VerifyGrantTable` gates any
  grant/protected lens on its presence + shape. Same approach, one ordering note.

---

## 5. Migration / interplay with the in-flight D1.3 fires

The grant-writer + protected-adapter + `rls.go` are **already built around runtime `Provision`** (D1.1–D1.4
shipped/in-flight). This is a **swap, not a rebuild**:

- `Provision` / `ProvisionProtectedTable`: replace the `pool.Exec(stmt)` execution with the `Verify*` catalog
  reads; `Build*TableDDL` stays (now feeds the verifier + the CLI).
- `buildAdapter`: keep the same call site; on a failed verify, register the lens **infra-paused** (don't hard
  fail registration) so it self-heals.
- `GrantWriterAdapter.Probe` / `ProtectedAdapter.Probe`: change from `pool.Ping` to the posture verify (Ping
  is subsumed — a dead pool fails the verify too).
- **No production data migration:** D1.3 protected tables aren't live yet (the read boundary is still being
  built), so there are no Lattice-created tables to hand back to operators. Dev stacks switch to the `make`
  target. If any dev table exists from the old path, it already has the right shape (same `Build*DDL`), so the
  verifier passes against it unchanged.
- **F2 subsumed:** the verifier asserts `projection_seq` present → the protected adapter can seq-guard
  (the prior fire's F2) reusing the grant writer's `WHERE EXCLUDED.projection_seq > …` clause. Fold F2 into
  Fire 2 here.

---

## 6. Test strategy

- **Unit (verifier, `POSTGRES_TEST_DSN`-gated, mirrors `postgres_test.go`):** a correctly-provisioned table
  → verify passes; FORCE-RLS **off** → verify fails (the security assertion); a missing `projection_seq` /
  `authz_anchors` / key col → fails with the named-column message; no SELECT policy → fails. A non-protected
  plain table is never verified (unchanged path).
- **Pipeline/integration (embedded — pause behavior):** a protected lens activated against a table with
  **FORCE RLS off** starts **infra-paused and projects ZERO rows** (the fail-closed activation gate — assert
  no write reached the table); after the test provisions FORCE RLS, the probe loop **auto-resumes** and the
  backlog drains. A drift case: FORCE RLS removed mid-run → the heartbeat re-pauses.
- **CLI/dev:** `emit-ddl` output for a sample lens equals `BuildProtectedTableDDL`; the `make` target stands
  up a verifiable table (the integration test's fixture).
- **Regression:** plain out-of-band lenses unchanged; the existing `rls_test.go` `Build*DDL` shape tests stay
  (the strings are still the source of truth) — only their *executor* is retired.
- **Gates:** build / vet / golangci / STRICT-conventions / `go test` refractor (+ the `POSTGRES_TEST_DSN`
  integration) / Gate-3 read-path vectors once a live protected model exists.

---

## 7. Risks + alternatives

- **A — keep Lattice provisioning (status quo).** Rejected per Andrew: it's the lone runtime-DDL exception,
  it never re-checks drift, and "Lattice doesn't migrate" already makes an existing/older table a silent gap.
- **B — passive write-error pause only (no active verify).** **Rejected — this is the unsafe version:** a
  missing RLS posture throws no write error, so it would fail-**open** (silent leak). The active FORCE-RLS
  check is the non-negotiable core.
- **C — verify but hard-fail registration (don't pause).** Rejected: a hard fail needs a Refractor restart
  after the operator fixes the table; infra-pause + probe-loop self-heals (better ops UX, same safety).
- **Risk — operator burden / a typo'd table.** Mitigated by the `emit-ddl` CLI + `make` target (copy-paste,
  not hand-written) and the fail-closed pause (a mistake is a visible paused lens + Health alert, never a
  leak). This is the same burden⇄simplicity trade already accepted for plain tables, and the verify-and-pause
  net is what makes it safe to extend to the security plane.
- **Risk — verify/TOCTOU drift between probe and a later `ALTER`.** The heartbeat re-verify (§2.5) bounds the
  window to one heartbeat; create-once has an *unbounded* window, so this is strictly better. (A determined
  operator disabling FORCE RLS mid-flight is outside the threat model — trusted operator — but we still
  detect it within a heartbeat.)
- **Risk — grant table ordering** (protected policy references `actor_read_grants`). The runbook documents
  "provision the grant table first"; `VerifyGrantTable` gates every dependent lens on its presence, so a
  wrong order is a clean pause, not a broken policy.

---

## 8. Fire-by-fire decomposition (for the Lattice Steward)

> **🏗️ BUILD CHECKPOINT (Steward fire 2026-06-30).** Fire 0 + Fire 1 are **built, full-3-layer-reviewed, and
> hardened**, all gates green, on worktree branch `steward-protected-lens-oob`
> (`.claude/worktrees/protected-lens-oob`, head `59d2f98`). **Not yet on main** — see the re-sequencing below.
> The 3-layer review materially improved the security plane and corrected this decomposition:
> - **Blind Hunter — CRITICAL fail-open caught + fixed:** `relforcerowsecurity` alone is **insufficient** —
>   FORCE-without-ENABLE (`relrowsecurity=false`) leaves a table **world-readable** (verified empirically: a
>   non-owner read returns rows past a deny-all policy). `VerifyProtectedTable` now gates **both**
>   `relrowsecurity` AND `relforcerowsecurity`, plus `relkind='r'`, **exact `text[]`** (via
>   `pg_attribute`/`format_type`, not `information_schema` `'ARRAY'` which any array type satisfies), and policy
>   **posture not presence** (the deterministically-named §6.14 membership policy whose USING references
>   `authz_anchors` + the grant table — a `USING(true)` policy is rejected). New fail-closed tests prove each
>   (`EnableOff`, `ForceRLSOff`, `PermissivePolicy`, `NoPolicy`, `MissingColumn`, `Absent`).
> - **Acceptance Auditor — ACCEPT** (Fire 0/1 fidelity, fail-closed crux, §6.14 conformance, the sound
>   `Provision*`-stays-as-the-out-of-band/test/CLI-seam deviation: runtime issues no DDL; zero non-test
>   callers; no fixture relocation needed — the verticals RLS tests use `Build*DDL` directly and stay green).
> - **Edge Case Hunter — BLOCK (re-sequences the fires):** `cmd/loftspace-app/applications.go` already reads
>   `read_lease_applications` from Postgres (D1.3 Fire 2, verticals), so the protected model is **live and
>   consumed in the dev vertical** — the "not live in prod" premise below is true for *prod* but **false for
>   `make up-loftspace`**. Retiring the runtime DDL **without** a provisioning path darks that working vertical.
>   **∴ Fire 1 must co-ship the dev/operator provisioning** (Fire 2's `make provision-readpath`, built generic
>   by reusing `lens.CoreKVSource` to enumerate installed protected/grant lenses + `Build*DDL`). CI is
>   unaffected (no go-test/CI target activates a protected pg lens — confirmed), so this is a *dev-stack*
>   non-regression requirement, not a CI gate.
>
> **🏗️ FIRE 2 BUILT (Steward fire 2026-06-30, head `8f0cbd4`).** The out-of-band provisioner + soft-delete
> guard + docs are **built and all gates green** on the same branch (rebased clean onto current `main`):
> - **`lens.EmitReadPathDDL`** (`internal/refractor/lens/emit_ddl.go`) enumerates installed protected/grant
>   lens specs from Core KV (read-only) → ordered `Build*TableDDL` (grant table first, then each protected
>   table). Reuses the in-package spec helpers + the verifier's own `Build*DDL` as the single source of truth,
>   so an applied table passes `VerifyProtectedTable` by construction.
> - **`lattice lens emit-ddl`** prints that DDL (operator runbook); **`make provision-readpath`** applies it to
>   the dev Postgres (idempotent), wired into `up-full` + `up-loftspace` after install so the live vertical no
>   longer darks.
> - **Soft-delete guard (Edge Case #3)** — `translateSpec` rejects `protected`+`deleteMode:soft` at spec load
>   (the RLS table has no `is_deleted` column and the §6.14 policy doesn't filter it → soft delete would loop).
>   `public`+`soft` stays valid. (Rejected-at-load, not verify-at-activation: the combination is genuinely
>   unsupported — `Build*DDL` can't even provision `is_deleted` — so a clear load-time error beats a
>   permanently-paused lens.)
> - **`refractor.md`** §protected-provisioning rewritten to the verify-and-pause model + the runbook.
> - **Validated:** `emit-ddl` against the **LIVE** shared stack enumerated the real loftspace protected tables
>   (`read_lease_applications` + `read_landlord_lease_applications`, grant table first); DDL == `Build*DDL`;
>   applied `Build*DDL` → `VerifyProtectedTable` passes (`rls_verify_test.go`, real Postgres, 8/8); `psql -f -`
>   pipe confirmed against the container. Gates: build, vet, `golangci-lint ./...` (0), STRICT lint-conventions
>   (0), `go test` refractor+substrate+cmd/lattice incl. the `POSTGRES_TEST_DSN` verify suite — all green.
>
> **NEXT FIRE (lands it) — needs a clean/dedicated stack:** (1) **Fire-2 3-layer review** (security plane — it
> generates the RLS DDL + a fail-closed config guard); (2) **live-verify `make up-loftspace` serves rows**
> end-to-end (fresh stack: protected lens starts infra-paused → `provision-readpath` creates the table → probe
> passes → lens resumes → loftspace-app serves) — **deferred this fire because the shared stack on :7777/:5432
> is owned by a concurrent fire and `up-loftspace` would disrupt it**; (3) **ff-merge Fire 0+1+2 to main**. The
> per-link evidence is strong (Fire-0 `InitialPause` pause-before-drain test + the verify suite + live emit-ddl
> + the psql pipe all pass independently); only the one continuous `up-loftspace` flow is unrun.
>
> **✅ LANDED (Steward fire 2026-06-30/07-01, `ef108b4` on `main`, CI green).** Rebased the worktree onto
> current `main` (`8f0cbd4`→`213780b`, trivial `.PHONY` conflict) and ran the Fire-2 3-layer review. All three
> layers found real gaps, all fixed before merge:
> - **Blind Hunter + Edge Case Hunter — `EmitReadPathDDL` diverged from the loader's view of spec validity.**
>   It (a) enumerated a **tombstoned** lens spec as live — contradicted its own doc comment; `KVGet` returns a
>   tombstoned entry normally (documented raw-consumer trap) and the emitter never checked `isDeleted` — and
>   (b) built column DDL directly (`translatePostgresColumns`) without routing through `translateSpec`'s
>   `protected`+`deleteMode:soft` rejection, so a malformed spec could get a table provisioned for a lens that
>   can never activate. Fixed: an `isDeleted` envelope probe skips tombstoned specs; the soft-delete check is
>   now `validateProtectedDeleteMode`, a shared helper both `translateSpec` and `EmitReadPathDDL` call — the
>   loader and the emitter can no longer disagree on "is this spec coherent."
> - **Acceptance Auditor — the design's own Fire-2 seq-guard requirement ("F2 subsumed" / "closing the prior
>   fire's F2") was absent.** `ProtectedAdapter.Upsert` delegated to `PostgresAdapter.Upsert`, which discards
>   `projectionSeq` entirely — no guard existed. Fixed: `PostgresAdapter` gained a `guarded` mode
>   (`SetGuarded`/`Guarded`, mirroring `NatsKVAdapter`) that appends `projection_seq` to the upsert and
>   conditions `ON CONFLICT ... DO UPDATE ... WHERE EXCLUDED.projection_seq > "<table>".projection_seq` — the
>   same clause `PostgresGrantWriter.UpsertGrant` already uses. `NewProtectedAdapter` enables it
>   unconditionally; `ProtectedAdapter.Guarded()` also lets the pipeline's adjacency-watch sentinel-seq (0)
>   skip apply to protected tables, the same protection KV-guarded lenses already had.
> - New coverage: `TestProtectedAdapter_SeqGuard_Integration` (real Postgres — a stale-seq upsert leaves a
>   fresher row untouched, a newer-seq upsert applies), `TestEmitReadPathDDL_TombstonedSpec_Skipped`,
>   `TestEmitReadPathDDL_ProtectedSoftDelete_Rejected`, plus unit coverage for the guarded SQL shape
>   (`TestBuildUpsertSQL_Guarded_*`). Gates: build, vet, `golangci-lint` (0), STRICT `lint-conventions` (0),
>   `go test ./...` (all green), `POSTGRES_TEST_DSN` adapter/lens/`internal/bypass` `TestCapAdv_*` suites — all
>   green. Fast-forwarded to `main` (`ef108b4`), CI green (run 28484303731).
> - **Item (2), the continuous `make up-loftspace` live e2e, stays deferred** — the shared stack (`:7777`/
>   `:5432`) was still live under concurrent fires (loftspace-app + clinic-app both running) at merge time, and
>   tearing it down to run a fresh `up-loftspace` would have disrupted that work. The per-link evidence
>   substitutes for it: `emit-ddl` was validated against the **live** shared stack's real protected tables
>   (§ above) and `VerifyProtectedTable`/the new seq-guard test both pass against real Postgres. A follow-up
>   fire can run the one remaining continuous flow on a dedicated/ephemeral stack if a regression is ever
>   suspected; it is not required to consider Fire 0+1+2 done.

> **Re-decomposed 2026-06-30** after the §2.3 grounding correction: the fail-closed activation gate needs a
> substrate seam that does not exist today, split out as **Fire 0**. Fire 0 + Fire 1 are the security-plane
> core and **must land together** (Fire 0 alone is dead scaffolding — an unused `InitialPause` field — and
> Fire 1 without it fail-opens); ship them as **one fire with an internal build order**, under one **full
> 3-layer review**. The grant/protected adapters + `rls.go` already exist (D1.1–D1.4), so this is still a
> swap, not a rebuild — just with the substrate seam added first. **(The 3-layer review then found Fire 1 also
> needs Fire 2's dev provisioning to avoid darking the live LoftSpace vertical — see the BUILD CHECKPOINT
> above; the three now land together.)**

**Fire 0 — substrate `ConsumerSpec.InitialPause` seam (the missing fail-closed primitive).** Add
`InitialPause PauseReason` to `ConsumerSpec` (zero-value = unpaused, every existing spec unchanged); in
`runPump`, after `restoreState` finds no persisted state, seed `st.addReason(spec.InitialPause)` before the
first drain so an `InitialPause: PauseInfra` pump enters `waitWhilePaused → runProbeLoop` and **probes
before draining**. Substrate unit test: a spec with `InitialPause: PauseInfra` + a Probe that fails-then-
passes projects **zero** until the Probe passes, then drains; a spec with the zero value drains immediately
(regression guard for Loom/Weaver/Processor). Additive, backward-compatible, no security surface on its own —
but it has **no standalone value**, so it ships *inside* this fire ahead of Fire 1, never alone.

**Fire 1 — the verifier + the fail-closed activation gate (the core).** Add `Verify{Protected,Grant}Table`
(read-only catalog checks, §2.4); switch the protected/grant adapters' `Probe` to posture-verify; register
protected/grant lenses with **`InitialPause: PauseInfra`** (the Fire-0 seam) so the probe gates the first
write; remove the `pool.Exec` from `Provision`/`ProvisionProtectedTable` (keep `Build*DDL`), relocating the
fixture provisioning the existing tests rely on (`capadv_read_bypass_test.go:209`,
`read_path_adapters_test.go:136-198`, `rls_test.go:113`) to a shared out-of-band test helper that runs
`Build*DDL` — so the just-shipped D1.4 Gate-3 read-path suite stays green. Unit + pipeline pause tests (§6).
**Full 3-layer review** (security plane — this *is* the read-auth boundary). Independently shippable once
Fire 0 lands with it; D1.3 protected models aren't live in prod, so there's no production regression — but
the test-fixture relocation is real coupled work, not zero.

**Fire 2 — operator runbook + dev ergonomics + F2 seq-guard.** The `lattice refractor emit-ddl` CLI + the
`make provision-readpath` target (dev parity); and **seq-guard `ProtectedAdapter`** (now that the verifier
guarantees `projection_seq`) reusing the grant writer's monotonic clause — closing the prior fire's F2 with
no DDL. `refractor.md` §"Protected read-model provisioning" rewritten to the verify-and-pause model.

**Fire 3 — continuous re-verification (drift).** Fold the posture check into the heartbeat (§2.5) so a
post-activation FORCE-RLS removal re-pauses + raises a §5.5 issue. Small, additive; can ride Fire 1 if cheap.

**Sequencing note:** this revises the D1.3 provisioning the read boundary depends on, so it should land
**before** the first live protected read model (the Verticals D1.3 Fire that stands one up) — i.e. it
re-points an in-flight dependency, it doesn't wait behind anything.

---

## 9. Grounding index

- Exception: `cmd/refractor/main.go` buildAdapter (`gw.Provision`, `ProvisionProtectedTable`);
  `internal/refractor/adapter/rls.go` (`BuildProtectedTableDDL`, `BuildGrantTableDDL`, `Provision`,
  `ProvisionProtectedTable`, `Upsert/RevokeGrant` seq-guard pattern).
- Pause machinery: `internal/substrate/consumer_supervisor_pump.go` (`waitWhilePaused`→`runProbeLoop`,
  probe-before-drain when infra-paused); `consumer_supervisor_spec.go` (`PauseInfra` auto-clears on passing
  Probe; `ClassInfra`/`ClassStructural`); `pipeline.go:331` (`spec.Probe = currentAdapter().Probe`).
- Adapters: `internal/refractor/adapter/read_path_adapters.go` (`ProtectedAdapter`/`GrantWriterAdapter`
  `.Probe` delegate); `postgres.go:73` (`Probe = pool.Ping`).
- Contract: `docs/contracts/06-capability-kv.md` §6.14 (Enforcement bullet — the "is created with FORCE RLS"
  sentence is the edit site). D1 design `read-path-authorization-d1-design.md` (H3 fail-closed rationale).
- Doc: `docs/components/refractor.md:62,68-74` (out-of-band plain vs Refractor-owned protected — rewritten in
  Fire 2).
