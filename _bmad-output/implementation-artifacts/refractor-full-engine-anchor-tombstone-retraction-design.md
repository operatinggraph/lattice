# Design — Full-engine anchor-tombstone retraction (Refractor)

**Status: ✅ Andrew-ratified (2026-06-27)** · Designer fire (Winston, architect hat) · 2026-06-27
**Scope confirmed by Andrew:** close the **root-tombstone** case now (Increments 1+2); the general
WHERE-flip / aspect-deletion retraction stays deferred to the *"Negative / filter-retraction projection"*
backlog row. Grounding confirmed the retraction key-tracking machinery already exists and is used by 2 of
the 3 projection paths (simple-engine `deleteResult`; actor-aware `evaluate.go:88`) — the plain full-engine
path is the lone fall-through, fixed here to parity. No frozen-contract change.
**Component:** Refractor (`internal/refractor/ruleengine/full` + `internal/refractor/pipeline`)
**Backlog row:** Lattice lane → *Read-model / projection maturity* → "Full-engine lens re-projects a
tombstoned vertex when its keyed aspect survives" (PO-routed, Clinic; ★★, S–M).
**Contract change:** none (build-to; component-doc update only).
**Demand:** Vertical PO (Clinic), filed 2026-06-27, **LIVE-verified** on the shared stack.

---

## For Andrew (one-look ratification)

**What it does (two lines).** When a vertex that is a full-engine projection lens's *anchor* is
soft-deleted, the Refractor today emits **no retraction** — the lens row lingers forever (a tombstoned
provider stays in the booking roster, a tombstoned appointment stays `scheduled`). This makes the
full-engine plain-projection path retract the anchor's row on a root tombstone, exactly mirroring the
**simple engine** (`deleteResult`) and the **actor-aware capability path** (`evaluate.go:88`) that
*already* do this — closing the gap for every vertical's read model (clinic, loftspace, lease-signing,
objects-base), not just Clinic.

**No frozen-contract change.** Lens projection semantics are documented in `docs/components/refractor.md`
(a component doc, freely editable), not in any `docs/contracts/*` frozen contract. The fix *builds to*
the existing contracts (Contract #1 universal tombstone-filter; Contract #6 capability-KV delete the
auth path already implements). Nothing under `docs/contracts/` is touched.

**No architectural fork.** This is a localized correctness fix on an established seam — no Gateway / D1 /
Vault / multi-cell surface. There is exactly **one scope decision** for your call (not a fork):

> **Decision — how far to chase retraction.** Increment 1+2 close the *root-tombstone* case the PO
> filed (and every analogous case across verticals) completely. A **strictly more general** gap exists:
> an anchor that drops out of the matched set *without* a root tombstone — e.g. its keyed `.profile`
> aspect is deleted, or a `WHERE` predicate flips — also leaves a stale row, because the full engine is
> upsert-only with no per-anchor retraction. **My recommendation: ship Increment 1+2 now (they fix the
> grounded, observed bug) and explicitly defer the general WHERE-flip / aspect-deletion retraction to
> the existing _"Negative / filter-retraction projection"_ backlog row** — that row already owns the
> "emit-only-when-violating / true retraction" problem and is the correct home for it. Folding it in
> here would balloon an S–M correctness fix into the open-ended negative-projection feature. Increment 3
> below is written as the *bridge* to that row, **not** built in this fire's scope. If you'd rather I
> design the general retraction through now, say so and I'll extend §7.3 into a full increment.

**One grounding correction worth your eye.** The backlog row hypothesised the tombstoned row is
*re-upserted because the surviving `.profile` aspect keeps the `WHERE` passing*. Grounding the code shows
that's **not** the mechanism: the full engine's `fetchNode` (`executor.go:484`) **does** filter the
tombstoned anchor, and `ExecuteWith` ignores the CDC event's props entirely (it re-scans Core KV). The
real cause is narrower and cleaner — the engine returns **zero rows** for the tombstoned anchor (proven
by `TestExec_SoftDeleteFiltered`, which asserts `require.Empty`) but **never a Delete**, so the prior
row is never retracted. The fix is therefore a *retraction emission*, not a `WHERE` change — which is
why it mirrors the simple engine so cleanly.

---

## 1. Problem & intent

### 1.1 The observed defect (PO-routed, live)

The Vertical PO (Clinic) verified on the shared stack: `TombstoneProvider` and `TombstoneAppointment`
both commit (`isDeleted: true` confirmed in Core KV via Loupe `/api/vertex`), yet:

- the tombstoned **provider** stays in `/api/providers` (the `clinicProviders` roster), and
- the tombstoned **appointment** stays `scheduled` in `/api/appointments` (the `clinicAppointments`
  read model).

A patient can still pick a deleted doctor; a cancelled-by-deletion appointment still shows as booked.
This **blocks** the Clinic vertical item *"tombstoned entities linger."*

A P5-clean app **cannot** paper over this: the lens read-model rows carry no liveness field, and reading
Core KV to learn a row's liveness would violate P5 (lenses are the only application query surface). The
fix has to live in the platform — the Refractor — where retraction belongs.

### 1.2 Root cause (grounded in the engine, not the hypothesis)

Two projection engines exist (`docs/components/refractor.md`): the **simple** engine (legacy) and the
**full** openCypher engine (canonical for new lenses — all clinic/loftspace/lease lenses are `full`).

The simple engine retracts correctly. `Evaluate` (`simple/evaluator.go:67`) routes an anchor whose
`isDeleted` is true to `deleteResult` (`evaluator.go:104, :314`), which synthesises a **Delete** keyed
by the anchor's key column. `TestPipeline_Delete` covers it.

The full engine does **not**. `ExecuteWith` (`full/executor.go:56`) ignores the CDC event's
`NodeKey`/`NodeProps` and re-derives state by **re-scanning Core KV** via `seedNodes` → `fetchNode`
(`executor.go:401, :467`). `fetchNode` correctly returns `nil` for a soft-deleted vertex
(`executor.go:484`), so the tombstoned anchor is simply **absent** from the result set — the engine
returns **zero rows for it**, never a Delete. `TestExec_SoftDeleteFiltered` (`executor_test.go:182`)
pins exactly this: a tombstoned anchor yields `require.Empty(results)`.

The pipeline then writes nothing for the absent anchor (`pipeline.go:567` → `writeResults`): an upsert
path with no row to upsert and no Delete to apply. **The prior row stays in the lens target forever.**

The **actor-aware** full-engine path already solved this for capability/auth lenses
(`pipeline/evaluate.go:84-95`): `if entry.IsDeleted && p.actorEnumerator != nil` → emit a Delete against
the cap-KV key (`actorDeleteKeyFor`). The **plain projection** full-engine path (no `ActorEnumerator`)
has no equivalent — it falls straight through to `executeFullForActor` and re-scans. **That missing
branch is the whole bug.**

### 1.3 Blast radius

This is not a clinic-only defect — it is latent in **every full-engine plain-projection lens**. The
full-engine lenses that are *not* actor-aware (no `ActorEnumerator`, so unprotected by the `:88`
shortcut) include `clinic-domain` (providers/appointments/patients), `loftspace-domain`,
`lease-signing`, `objects-base`, and the non-auth `orchestration-base` projections. Each leaks a stale
row when its anchor is tombstoned. The auth lenses (`rbac-domain`, `service-location`,
`identity-hygiene`, the capability lens) are already covered by the actor-aware shortcut. **One fix,
every vertical read model corrected.**

### 1.4 Intent

Make the full-engine plain-projection path obey the same invariant the simple engine and the actor-aware
path already obey: **a soft-deleted anchor retracts its projected row.** Minimal, localized, mirrors two
existing precedents, no new concepts.

---

## 2. The shape

### 2.1 Where it lives (the seam)

The fix is the **non-actor twin** of the actor-aware shortcut at `pipeline/evaluate.go:84`. Today:

```go
// Actor tombstone shortcut (auth lenses only).
if entry.IsDeleted && p.actorEnumerator != nil {
    delKey := p.actorDeleteKeyFor(entry.CoreKVKey)
    return []simple.EvalResult{{Delete: true, Keys: map[string]any{"key": delKey}, Row: nil}}, nil
}
```

We add the symmetric plain-projection branch immediately below it: when the driving event is a
**root tombstone** *and* the event vertex is **this lens's anchor**, emit a Delete keyed by the anchor's
key column — then fall through to the existing re-scan for every other case (including a *secondary*-node
tombstone, which must re-execute so dependent rows update).

### 2.2 Key derivation — mirror `deleteResult`, owned by the engine

The delete key is "what output key did this anchor previously project to." The simple engine answers this
in `deleteResult` by reading the anchor's key column from the anchor props. The full engine's output key
is **the first `RETURN` item** (`executor.go:1012-1021` — "the first projection item as the key, mirroring
the simple engine's alias-becomes-the-key convention"). So the full engine already *knows* its key
column; we expose that knowledge.

Add one read-only method to the full `Engine`, parsing only the AST it already holds:

```go
// AnchorDeleteResult reports the projection (delete) key that the now-tombstoned
// event vertex previously projected to, for a root-tombstone CDC event on a plain
// projection lens. It mirrors the simple engine's deleteResult.
//
//   ok == false  → the event vertex is NOT this rule's anchor label (a secondary-node
//                  tombstone), or the key column cannot be resolved from the anchor's
//                  root props (a non-.key aspect-derived key — anti-pattern). The
//                  caller must fall through to a normal re-execute; no Delete is emitted.
//   ok == true   → keys is the Keys map to hand to a Delete EvalResult.
func (e *Engine) AnchorDeleteResult(
    cr ruleengine.CompiledRule, eventKey, eventType string, eventProps map[string]any,
) (keys map[string]any, ok bool)
```

Resolution rules (decided, not deferred):

1. **Anchor label = first `MATCH` pattern's first node `Label`** (`Query.Clauses[0].(*Match)
   .Patterns[0].Nodes[0].Label`). If `eventType != anchorLabel` → `ok=false` (secondary tombstone →
   re-execute). This is the precise discriminator: a `provider`/`appointment` tombstone is the anchor; a
   `patient` tombstone reaching the appointment lens via `forPatient` is *not*.
2. **Key alias = first `RETURN` item's `Alias`** (auto-aliased when bare, via the existing
   `projectionAutoAlias`). The Delete `Keys` map is `{alias: <value>}`.
3. **Key value:**
   - If the first `RETURN` item is the anchor's `key` (`<anchorVar>.key` — the `IntoKey` default and the
     shape of *every* shipped plain lens), the value is the vertex key = `eventKey`. **Robust, payload-
     independent.**
   - Else if it resolves against the tombstoned anchor's **root props** (`eventProps`), use that.
   - Else (it references an *aspect* not present in a root-tombstone payload) → `ok=false`, fall through.
     Keying a read model on a mutable aspect field is already an anti-pattern (the key churns on every
     aspect edit, breaking incremental projection), so this branch is correctness-preserving, not a
     functional loss.

This keeps RETURN/anchor parsing inside the engine (where the AST lives) and the orchestration in the
pipeline (where the actor-aware twin lives) — no logic duplicated, no layering violated.

### 2.3 Pipeline wiring

In `evaluateForEntry`, the `EngineFull` arm, **after** the existing actor-aware shortcut and **before**
the actor-enumerator fan-out / `executeFullForActor`:

```go
// Plain-projection anchor tombstone: retract the row the deleted anchor projected.
// The non-actor twin of the actor-aware shortcut above; mirrors the simple engine's
// deleteResult. A secondary-node tombstone (eventType != anchor label) returns ok=false
// and falls through to a normal re-execute so dependent rows refresh (e.g. a deleted
// patient nulls an appointment's patientName without deleting the appointment row).
if entry.IsDeleted && p.actorEnumerator == nil {
    eventType, _, _ := substrate.ParseVertexKey(entry.CoreKVKey)
    if keys, ok := p.fullEngine.AnchorDeleteResult(
        p.fullCR, entry.CoreKVKey, eventType, entry.Properties); ok {
        return []simple.EvalResult{{Delete: true, Keys: keys, Row: nil}}, nil
    }
}
```

`fullEngine`/`fullCR` are already non-nil on this arm (guarded at `evaluate.go:69`). The Delete flows
through the unchanged `writeResults` path: the **NATS-KV** adapter purges the key, the **Postgres**
adapter `DELETE`s (or soft-deletes per `targetConfig.deleteMode`) — both already handle Delete results
(`docs/components/refractor.md` "Postgres rows"; the KV adapter's purge-delete).

### 2.4 Read path (P5) & write path (P2) — unchanged, honored

- **P5 (read):** apps keep reading the lens target bucket (`clinic-providers`, `clinic-appointments`).
  This fix makes that bucket *correct* (the tombstoned row disappears); it adds no new read surface and
  no Core-KV read for apps. The Refractor's own Core-KV reads are projection inputs (ADR-16), unchanged.
- **P2 (write):** the Refractor writes only its own lens *targets* (the read-model buckets / Postgres),
  never Core KV. A Delete against a lens target is the Refractor's sanctioned write to its own
  projection — identical to how it already deletes capability-KV keys and how the simple engine already
  deletes plain-lens rows. The Processor stays the sole Core-KV writer; nothing here submits an op.
- **Contract #1 tombstone filter:** every reader filters tombstones. This fix is the Refractor *acting
  on* a source tombstone to propagate the deletion into its derived view — the projection-layer half of
  the universal "readers filter tombstones" rule.

### 2.5 Orchestration

None. No Loom pattern, no Weaver convergence lens, no `@at`/`@every`, no directOp. This is a pure CDC
reaction inside the Refractor's existing per-event evaluate→write loop — the same loop the simple engine
and the actor-aware path already drive. The precedent mirrored is **`pipeline/evaluate.go:84` (actor
tombstone shortcut)** and **`simple/evaluator.go:104` (`deleteResult`)**.

---

## 3. Contract surface

**No `docs/contracts/*` change.** Lens projection retraction semantics are not in any frozen contract;
they live in `docs/components/refractor.md`. The fix builds to:

- **Contract #1 §1.x** (universal envelope / `isDeleted` tombstone, "every reader filters tombstones") —
  honored: the Refractor propagates the source tombstone into its derived view.
- **Contract #6** (Capability-KV) — untouched; the auth path already deletes correctly, and this fix is
  the *non-auth* twin (no capability shape involved).
- **Contract #4 §4.3** (`replaying` status) and the Postgres `deleteMode` semantics — unchanged; Delete
  results already route through them.

**Doc change (allowed, in `/docs`):** `docs/components/refractor.md` gains a short note under the engine
section: *the full engine retracts a plain-projection lens row on a **root tombstone of the anchor**
(mirroring the simple engine's `deleteResult`); a tombstone of a non-anchor (secondary) node re-executes
so dependent rows refresh; retraction on a `WHERE`-predicate flip / keyed-aspect deletion without a root
tombstone is tracked by the "Negative / filter-retraction projection" backlog row.* This replaces the
implicit assumption that the full engine never deletes plain rows.

---

## 4. Migration / compatibility

- **No data migration.** Read models converge on the next anchor tombstone. Rows *already* leaked by the
  bug (a provider tombstoned before this ships) are healed by a **lens rebuild** (`Pipeline.Rebuild`,
  the existing replay path) or naturally on the next event that re-scans — the operator can force-heal
  via the standard rebuild. Worth a one-line note in the build PR; no bespoke backfill.
- **No key-shape change, no DDL change, no package change.** Purely Refractor-internal behavior.
- **Backward compatible.** The new branch fires *only* on `entry.IsDeleted && actorEnumerator == nil`
  with `ok==true`; every existing path (live anchor, secondary tombstone, actor lens, simple engine) is
  byte-for-byte unchanged. A lens whose key column the engine can't resolve falls through to today's
  exact behavior — no regression, only the strict addition of a correct Delete where there was silence.
- **Performance:** strictly *better* for the tombstone event — a single Delete replaces the prior
  full-bucket re-scan-and-reproject. No new reads.

---

## 5. Test strategy

What proves it (mirrors `TestPipeline_Delete` + `TestExec_SoftDeleteFiltered`):

1. **Engine unit (`full/executor_test.go` or a new `anchor_delete_test.go`):** extend the
   `TestExec_SoftDeleteFiltered` neighborhood with `TestAnchorDeleteResult`:
   - anchor-label root tombstone → `ok==true`, `keys == {"key": <vtxKey>}`;
   - secondary-type tombstone (event type ≠ anchor label) → `ok==false`;
   - first-RETURN-item `.key` → key = event vertex key; a root-field key alias → resolved from props;
   - an aspect-derived first item → `ok==false` (fall-through).
2. **Pipeline unit (`pipeline/…_test.go`):** a full-engine plain lens; drive a live anchor (asserts an
   upsert), then a root tombstone of that anchor (asserts a **Delete** EvalResult against the prior key,
   i.e. the target row is removed) — the direct analog of the simple engine's `TestPipeline_Delete`.
   A second case: a *secondary*-node tombstone (a linked vertex) asserts the anchor row is **re-projected
   with the neighbor field nulled, NOT deleted**.
3. **Ephemeral-stack e2e (the grounded reproduction):** against the clinic lens on the ephemeral stack
   (the `make` e2e harness the convergence suites use) — `CreateProvider` → row present in
   `clinic-providers`; `TombstoneProvider` → row **gone**. Same for `CreateAppointment` →
   `TombstoneAppointment` → gone from `clinic-appointments`. And `TombstonePatient` → the appointment row
   stays but `patientName`/`patientKey` go null. This e2e is the executable proof the PO's live finding
   is closed.
4. **Gates:** `go build ./...`, `make vet`, `golangci-lint run ./...`, STRICT `lint-conventions`,
   `go test ./internal/refractor/...`, and the relevant package e2e. No bypass/capability-gate surface
   is touched (read-model path only), so Gate 2/Gate 3 are unaffected but run per house rules.

---

## 6. Risks & alternatives

### 6.1 Risks

- **R1 — multi-MATCH / multi-label lenses.** A lens whose first `MATCH` lists several comma-separated
  patterns, or whose anchor isn't the first node, could mis-identify the anchor. *Mitigation:* the
  derivation is **conservative** — it only emits a Delete when `eventType` exactly equals the first
  node's label *and* the key resolves; any ambiguity yields `ok=false` → today's behavior (linger, not
  mis-delete). A wrong-delete is impossible; the worst case degrades to the status quo for an exotic
  lens. All shipped plain lenses are single-anchor `MATCH (x:Label)`.
- **R2 — key column not the anchor's `.key`.** Covered by the `ok=false` fall-through (§2.2.3); no shipped
  lens does this, and it's an anti-pattern.
- **R3 — adjacency staleness on a still-live anchor.** Out of scope: this fix only fires on a *tombstoned*
  anchor. Live-anchor re-projection (the full-scan path) is unchanged.
- **R4 — the general WHERE-flip / aspect-deletion retraction stays open.** Acknowledged and *bounded*:
  the PO bug is root-tombstone, fully closed by Inc 1+2. The residual is the "negative projection"
  problem, tracked by its own backlog row (see §7.3 / the For-Andrew decision). Documented in
  refractor.md so it's not a silent gap.

### 6.2 Alternatives considered

- **Alt A — root-liveness `WHERE` predicate** (the backlog's option 2: let lens authors write
  `WHERE x.isDeleted <> true`). **Rejected.** It (a) burdens every lens author to remember it, and (b)
  *doesn't actually fix the linger* — a `WHERE` that filters the tombstoned anchor still yields **zero
  rows and no Delete**, so the stale row persists. It addresses re-*upsert* (which, per §1.2, isn't even
  the mechanism) while leaving retraction — the actual bug — unsolved.
- **Alt B — full-rebuild reconcile on every event** (diff the whole target bucket against a fresh
  projection, delete orphans). **Rejected.** O(bucket) per event, a heavy reconcile loop, and a large
  new surface for an S–M bug. The targeted Delete is the right altitude.
- **Alt C — seed the anchor from the CDC event props and let `WHERE` decide.** **Rejected.** It would
  require `ExecuteWith` to trust event props over Core KV (a model change), re-introduce the "surviving
  aspect keeps WHERE passing" hazard the backlog feared, and still need a separate retraction emission.
  Mirroring `deleteResult` is simpler and already proven.
- **Chosen — Alt D: engine-seam Delete emission mirroring `deleteResult` + the actor-aware shortcut.**
  Smallest diff, two existing precedents, no new concepts, conservative-by-construction.

---

## 7. Decomposition for the Steward (fire-by-fire)

Each increment is independently shippable + green.

### 7.1 Fire 1 — the core retraction (closes the PO bug)

`Engine.AnchorDeleteResult` (engine method, AST-only, read-only) + the `evaluateForEntry` plain-projection
tombstone branch (§2.3) + engine & pipeline unit tests (§5.1, §5.2). This alone makes
`TombstoneProvider`/`TombstoneAppointment` retract. Gates green. *Independently shippable.*

### 7.2 Fire 2 — e2e proof + doc

The ephemeral-stack clinic e2e (§5.3) asserting the live PO reproduction is closed (provider +
appointment retract; patient tombstone nulls-not-deletes), plus the `docs/components/refractor.md` engine
note (§3). Flips the Clinic vertical *"tombstoned entities linger"* item unblocked. *Independently
shippable.*

### 7.3 Fire 3 — **bridge only, not in this scope** (general retraction → negative-projection row)

Documented seam, **not built here**: an anchor that drops out of the matched set without a root
tombstone (keyed-aspect deletion, `WHERE`-predicate flip) is the open "Negative / filter-retraction
projection" problem. This fire is named so the Steward knows where it goes — it should be designed under
that backlog row, not folded into this correctness fix. (Per the For-Andrew decision; build only if
Andrew redirects.)

---

## 8. Open questions — resolved

- **Q: Engine-seam vs pipeline-only?** → **Engine owns key/anchor derivation** (`AnchorDeleteResult`),
  **pipeline owns orchestration** (the `evaluate.go` branch). Mirrors how the simple engine keeps
  `deleteResult` in the engine and how the actor-aware twin keeps its Delete in the pipeline.
- **Q: How is the delete key derived when the key column isn't `.key`?** → §2.2.3: `.key` → event key
  (robust); root field → from props; aspect field → `ok=false` fall-through (anti-pattern, no regression).
- **Q: Secondary-node tombstone (deleted patient on the appointment lens)?** → `ok=false` → re-execute →
  the appointment row refreshes with `patientName=null`, row preserved. Asserted in §5.2/§5.3.
- **Q: Heal already-leaked rows?** → existing `Pipeline.Rebuild` replay; no bespoke backfill (§4).
- **Q: Does anything in `docs/contracts/*` change?** → No (§3).
- **Q: How far to chase retraction?** → Inc 1+2 close the grounded bug; the general WHERE-flip case is
  the existing negative-projection row (the For-Andrew decision).

---

## 9. Self-adversarial pass (folded in)

- *"The backlog says re-upsert; you say no-retract — which is right?"* → Grounded the code:
  `ExecuteWith` ignores event props and re-scans; `fetchNode` filters the tombstone;
  `TestExec_SoftDeleteFiltered` asserts `require.Empty`. The engine returns zero rows, not a re-upserted
  row. The fix is retraction emission. (Correction surfaced to Andrew in the header.)
- *"Could the new branch wrongly delete a live row?"* → No: gated on `entry.IsDeleted` (a real
  tombstone) AND `eventType == anchorLabel` AND a resolvable key; any miss falls through to unchanged
  behavior. A false-delete is structurally impossible.
- *"Does it regress the actor/auth lenses?"* → No: the new branch is `actorEnumerator == nil` only; the
  `:88` actor shortcut runs first and unchanged.
- *"Does it regress secondary-node refresh (appointment ← patient)?"* → No: secondary tombstones return
  `ok=false` and re-execute, which is today's correct behavior; explicitly tested.
- *"Is there a P2/P5 violation in deleting a lens row?"* → No: deleting the Refractor's own lens target
  is its sanctioned write (same as the cap-KV delete and the simple-engine plain-lens delete); no Core
  KV write, no app-side Core read.
