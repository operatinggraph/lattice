# Negative / filter-retraction projection — design

**Status: ✅ Andrew-ratified (2026-07-02).** Three-part ratification: (1) Fires 1+2 COLLAPSED to ONE
fire (same seam, Fire 2 depends on Fire 1, one full 3-layer review) per the fewer-larger rule; Fire 3
stays shelved-design (no live consumer). (2) This design SUBSUMES the link-/aspect-triggered
reprojection design (its own §7b gate recommended it — the enumerator-less Fire 1 covers every live
consumer); that design's security findings fold into this build: **F1** (composite-GrantTable shrink =
over-grant — covered structurally by Fire 2's `ok=false` fall-through; the build's 3-layer review must
verify it), **F3** (tombstone-race). (3) **F2 accepted as posture**: the plain/protected Postgres
adapter is unguarded last-writer-wins — matches the D1 M3 CDC-lag revocation posture; a hardening row
(seq-guard extension to protected targets) is filed on the board. Importance ★→★★ correction stands.
Note: hard-delete is shelved, so no DEL markers exist — the DEL/empty-body retraction arm stays out of
scope; the shelved hard-delete design's revive-condition points here.
· Designer: Winston (architect) · 2026-06-29
Backlog row: `planning-artifacts/backlog/lattice.md` → *Read-model / projection maturity* →
"Negative / filter-retraction projection".

**Fires 1+2 CHECKPOINT (2026-07-02, Lattice Steward, `5624392`).** The collapsed fire shipped with a full
3-layer review; docs/components/refractor.md carries the landed behavior. Build corrections to this
design's assumptions (all conservative, none semantic):
- **The "evaluation errors on an unbound variable" premise was FALSE** — `evalExpr` resolves an unbound
  `VariableRef` to silent nil (the OPTIONAL MATCH contract), so the shipped tombstone derivation could
  return `ok=true` with a nil-valued neighbor key column (a wrong partial composite key, latent since
  `679fe25`). Fixed structurally: `exprReferencesOnlyVariable` rejects any key column referencing a
  non-anchor variable; WITH-bearing queries (name-rebinding defeats the scope check) and aggregator key
  columns (read-free mode fabricates single-row values) are also never derivable; nil-valued and empty
  key maps fall through.
- **The plain link arm applies the link to adjacency itself** (both directions, link key as EdgeID)
  before re-executing — §2.1's "adjacency must already be updated (it is)" ignored the cross-consumer
  ordering race the actor path already defends against; without it a tombstone's re-execute can still
  see the removed edge and miss the retraction.
- **`seedNodes` now propagates ListKeys failures** instead of degrading to zero seeds — with retraction,
  absence is acted on, so a swallowed scan error would have retracted live rows on a substrate blip.
- **Presence equality is canonical-JSON** (the identity adapters key on), erring toward linger, never a
  type-mismatch wrong Delete. A malformed link body Terminal-DLQs (the dedicated consumer's disposition)
  rather than Nak-looping every plain pipeline.
- **R1 (whole-scan amplification) BIT IN CI and is now structurally bounded** (`9daa253`): unbounded, every
  aspect/link event ran a whole-bucket re-scan per plain lens, and a meta-DDL write burst delayed the
  Hello-Lattice book projection past the 1s NFR-P3 guard (twice — not the known runner flake). A plain lens
  now reprojects only when the event's owner/endpoint vertex type is in its referenced-label set
  (`full.CompiledRule.ReferencedLabels`; non-exhaustive set → reproject-all, conservative). Simple-engine
  lenses keep the legacy ack-and-skip.
- **GrantTable interaction (audited):** both primordial grant lenses derive their full composite
  (anchor-only + literals) so §6.14 revoke-on-tombstone is preserved; the presence check additionally
  revokes the wildcard grant promptly on a `protected` flip to false (previously lingered), and a
  never-matched identity inserts a bounded deny-direction tombstone row (RevokeGrant's documented
  posture — the R2 "no-op" wording was too strong for GrantTable targets).
**Fire 3's build condition is now MET**: Vault 5b-ii shipped `landlordLeaseApplicationsRead` — a live
neighbor-keyed composite `(app_id, landlord_id)` whose manages-unassign drop F2 structurally cannot
retract (the `ok=false` fall-through, pinned by test) — and the vault design gates 5b close on exactly
that retraction. **Fire 3 (adapter `ListKeysForAnchor` + set-difference Deletes) is the next increment**
and inherits this fire's review posture (Deletes on the read path → full 3-layer). Deferred with it: the
`availableListings` live ephemeral-stack e2e (§4's PO-visible proof; the platform mechanism is pinned at
the pipeline layer by `filter_retraction_internal_test.go`).

**Convergence-lens no-filtering-WHERE guard CLOSED (2026-07-10, `61859e0`).** The review carry-out above
shipped as `(*full.CompiledRule).ValidateNoFilteringWhereForConvergence`, called from
`cmd/refractor/main.go`'s `startPipeline` beside the `DiffRetraction` guard: a **plain**
(non-actorAggregate) lens targeting `weaver-targets` now fails to activate if it carries a filtering
(non-optional `MATCH`, or any `WITH`) `WHERE`. Grounding found the stated invariant was false as
literally written — `unroutedTasks` (`packages/orchestration-base`) is a live, tested convergence lens
with a required `WHERE` — but it's `actorAggregate`, whose zero-match case retracts through the
envelope's transports rather than the plain path's presence check: the per-row realness-filter
callback for a row-producing lens whose anchor MATCH always succeeds and only an OPTIONAL-MATCH
secondary pattern is conditional (`unroutedTasks`'s shape), or the zero-row-retraction transport
(`pipeline.Pipeline.zeroRowRetraction`) for a lens whose filtering `WHERE` sits on the anchor match
itself, so the cypher returns no row at all once it stops matching (`orphanedTaskGrants`'s shape) —
either way Weaver treats a missing row and a live `violating: false` row identically, so the guard
scopes to plain lenses only and `unroutedTasks` is correctly exempt, not broken.
`docs/components/refractor.md`'s authoring-invariant prose corrected to match.

**Fire 3 CHECKPOINT (2026-07-02, Lattice Steward, full 3-layer review).** Shipped as a **lens-wide**
diff rather than the sketched per-anchor `ListKeysForAnchor(anchorKey)` scoping: `adapter.KeyLister.
ListKeys(ctx)` returns the target's **entire** live key set (no anchor parameter), and
`pipeline.applyDiffRetraction` diffs it against the re-execute's **entire** freshly-computed row set on
every trigger. This is exact, not an approximation, precisely because a `DiffRetraction` lens's query
is unanchored (no `{key: $actorKey}`): the re-execute already recomputes the complete global truth
regardless of which vertex fired it, so a per-vertex-scoped diff would have been both unnecessary and
ambiguous — `landlordLeaseApplicationsRead`'s triggering vertex can be `unit`, `identity` (the
applicant OR the managing landlord — no single stable id to scope a prefix-list by), or the `leaseapp`
anchor itself. Fixed pre-merge from the adversarial pass: the design's soundness argument ("the query
is unanchored") had **no code-level enforcement** — a future or misconfigured `DiffRetraction` lens
referencing `$actorKey` would have silently mass-retracted every other live anchor's rows on its first
event. Closed with `(*full.CompiledRule).ValidateUnanchoredForDiffRetraction`, called at activation
(`cmd/refractor/main.go`) — fails the lens closed (never activates) rather than corrupting its target.
Opt-in wiring: `pkgmgr.LensSpec.DiffRetraction` → YAML `targetConfig.diffRetraction` (postgres-only,
`bucketguard.go` rejects it on nats-kv at install time) → `lens.IntoConfig.DiffRetraction` →
`pipeline.SetDiffRetraction`. `TestPlainLens_NeighborKeyedComposite_DiffRetractionDeletes` proves the
manages-unassign scenario end-to-end through the real `handle()` dispatch. **5b-close's Fire-3 gate is
now CLEARED** (see `vault-crypto-shredding-design.md`'s matching checkpoint).

---

## For Andrew (the one-look)

**What it does (two lines).** Today a plain (non-actor) Refractor projection lens **lingers a stale
row** when an anchor stops matching the lens without a *root tombstone* — a `WHERE` predicate that
flips true→false, a keyed-aspect deletion, or a required relationship removed. This design closes that
gap by (1) making plain lenses **reproject on aspect/link-only mutations** (which they ignore today —
the entangled freshness prerequisite I found while grounding), then (2) emitting a **read-free
anchor-keyed Delete** when the anchor's own mutation drops it from the matched set — the *non-tombstone
twin* of the shipped anchor-tombstone retraction (`679fe25`, `AnchorDeleteResult`).

**No architectural fork.** A contained extension of the Refractor projection pipeline, mirroring the
machinery the **actor-aware capability path already has** (`evalAspectFanOut` / `evalLinkFanOut`) and
the **shipped anchor-tombstone retraction** (`AnchorDeleteResult`). No Gateway / read-path-auth / Vault
/ multi-cell surface.

**No frozen-contract change.** Lens **reprojection timing** and **projection completeness** are
projection-correctness behavior documented in `docs/components/refractor.md`, **not** in
`docs/contracts/*` — the same basis on which the anchor-tombstone retraction shipped "no contract
change." I read Contracts #1/#5/#6/#10: none prescribes the ack-skip-aspect behavior or the linger;
nothing to edit. (§6 below shows the search.)

**One importance correction for the record.** The row was filed **★** as a projection-maturity nicety.
Grounding promotes it to **★★**: the headline retraction can't fire for *any live lens* until the
**freshness prerequisite (Fire 1)** lands — and that prerequisite is itself a **live, untested
staleness bug**: `availableListings`, the LoftSpace/Clinic name pickers, and the service-location lens
all filter on an **aspect or a link**, and a plain lens **never reprojects on an aspect/link-only
mutation today** (`pipeline.go:491-495,504-506` ack-and-skip). So a listing whose price is edited, or a
provider renamed, is silently stale in its read model until an unrelated vertex-root event happens to
re-scan it. Fire 1 fixes that on its own.

**One scope decision for you (not a fork).** The general *neighbor-driven* / *multi-row* retraction
(an anchor dropped from a **whole-type-scan** lens by a change to a *different* vertex, or one of N
rows-per-anchor dropping) genuinely needs a **target-read diff** and has **no live consumer** (every
live multi-row lens is either a convergence target using the `violating` flag — by design — or an
actor-aggregate using `collect` + the envelope absence-delete). I **design it (Fire 3) but
sequence-defer the build** behind a real consumer (the dead-scaffolding rule). Confirm you're happy
deferring Fire 3, or say if you want it built eagerly.

---

## 1. Problem + intent

### 1.1 The backlog ask, grounded

The row asks for *"true emit-only-when-violating"* projection — a lens whose target holds **only** the
rows currently satisfying its predicate, retracting a row when the predicate stops holding. The
`docs/components/refractor.md` "Anchor-tombstone retraction" section (L168-205) names the exact residual
this is:

> Retraction when an anchor drops out of the matched set **without** a root tombstone (a keyed-aspect
> deletion, or a `WHERE` predicate that flips) is the broader "negative / filter-retraction projection"
> problem, tracked separately on the Phase-3 backlog — not handled here. *(refractor.md:202-205)*

The shipped fix (`679fe25`) handled the **root-tombstone** trigger only: when an anchor vertex is
soft-deleted, the full engine's upsert-only re-scan returns zero rows for it but never a Delete, so the
pipeline now derives the anchor's projected key read-free from the AST and emits a Delete
(`full.Engine.AnchorDeleteResult` + the `evaluateForEntry` branch at `evaluate.go:105-111`). The
**predicate-flip / keyed-aspect-deletion / required-link-removal** triggers — where the anchor stays
**alive** but stops matching — are unhandled. That is this design.

This traces to the brainstorm's foundational read-model decision (#? — Weaver-targets-are-Lenses):

> Weaver targets ARE Lenses that project "currently-violating-vertices" rows; Weaver subscribes to
> those rows. *(brainstorming-session-2026-04-08.md:236)*

The convergence (Weaver) lenses **deliberately avoid** retraction — they project **one row per
candidate** carrying a `violating` boolean that flips, never a row that appears/disappears, *because* "a
dropped convergence row reads to Weaver as an entity deletion" (refractor.md:164-166). That is a sound,
intentional contract for the **convergence** read path and **this design does not touch it.** The
beneficiary of true retraction is the **plain read-model lens** that should contain *only* matching rows
— a listing roster, a person picker, a service-access view — read by Loupe and the vertical app FEs
under P5.

### 1.2 The two-part gap (what grounding actually found)

Walking the pipeline (`internal/refractor/pipeline/pipeline.go` `HandleMessage`, L475-567) against the
live plain lenses surfaced that the retraction gap **sits on top of a freshness gap**:

1. **Plain lenses ignore aspect/link-only mutations.** The pipeline classifies each CDC event by
   Contract #1 key shape. For a plain lens (`actorEnumerator == nil`):
   - a **`KindAspect`** event → *"aspect mutation observed but no handler registered"* → **Ack**
     (`pipeline.go:491-495`);
   - a **`KindLink`** event → *"link mutation observed but no handler registered"* → **Ack**
     (`pipeline.go:504-506`).
   Only the **actor-aware** pipeline (`actorEnumerator != nil` — the capability lenses) fans out on
   aspect/link mutations via `evalAspectFanOut` / `evalLinkFanOut` (`pipeline.go:489,501,577,609`). The
   plain path re-executes **only on a vertex-root event** (`KindVertex`, L513-566).

2. **Every live plain-lens filter predicate is on an aspect or a link** — so the predicate-flip event
   is precisely an aspect/link event the plain path drops on the floor:

   | Live plain lens | Predicate | Mutation that flips it | CDC kind today |
   |---|---|---|---|
   | `loftspace-domain.availableListings` | `WHERE u.listing.data.status <> null` | listing aspect removed | **`KindAspect`** → ack-skip |
   | `loftspace-domain.applicantRoster` | `WHERE i.name.data.value <> null` | `.name` aspect removed | **`KindAspect`** → ack-skip |
   | `clinic-domain.clinicProviders` | `WHERE pr.profile.data.fullName <> null` | `.profile` aspect removed | **`KindAspect`** → ack-skip |
   | `clinic-domain.clinicPatients` | `WHERE p.demographics.data.fullName <> null` | `.demographics` removed | **`KindAspect`** → ack-skip |
   | `service-location.<svc access>` | `WHERE NOT (svc)-[:instanceOf]->(svcTpl)` | `instanceOf` link added | **`KindLink`** → ack-skip |

   (`task.data.status` / `task.data.expiresAt` predicates live inside the **actor-aggregate** `myTasks`
   / `capabilityEphemeral` lenses, not a plain per-task lens — those re-derive via `collect` + the
   envelope absence-delete and are out of scope; see §5.)

So a plain lens is doubly stale on aspect/link-only mutations: it neither **updates a changed field**
nor **retracts a dropped anchor**, because it never re-executes. The freshness half masks itself in
tests because the **whole-type-scan** lenses (`MATCH (u:unit)`, no `{key:$actorKey}` seed) re-scan all
anchors on *any* vertex-root event in the bucket (the consumer filters the whole Core-KV bucket —
`cmd/refractor/main.go:380` `subjects.CoreKVFilter`), so they eventually pick up aspect changes on the
next unrelated root event — but not promptly, and not at all if none follows. No test in the repo
asserts a plain lens reflects an aspect-only update (verified by grep).

### 1.3 Why retraction needs the freshness fix first

The retraction trigger has to be an event **about the dropped anchor** (so we can derive *which* row to
retract, read-free). For the live lenses that event is the anchor's own **aspect/link** mutation — which
the plain path drops today. So **Fire 2 (retraction) cannot fire for any live lens until Fire 1
(aspect/link reprojection) makes the anchor's own mutation re-execute the lens.** This sequencing is the
core structural finding of the design.

---

## 2. The shape

Two engines project (`ruleengine/simple` and `ruleengine/full`); the live filtered lenses all use the
**full** engine (`WHERE` is a full-engine feature — the simple engine is pure traversal with no
predicate). The full engine is **upsert-only**: `ExecuteWith` re-derives a lens's current rows by
re-scanning/seeding Core KV and ignores the CDC payload; it emits Upserts, never Deletes. Retraction is
bolted on at the **pipeline** layer, exactly where `AnchorDeleteResult` already lives.

### 2.1 Fire 1 — plain-lens aspect/link reprojection (the freshness prerequisite)

**Mirror the actor-aware precedent.** The actor path already solves "an aspect/link-only mutation must
re-execute the lens seeded from the affected vertex." Add the **plain** twin in `HandleMessage`'s
`KindAspect` / `KindLink` arms:

- **`KindAspect`** (`vtx.<type>.<id>.<local>`): resolve the **owner vertex** with
  `substrate.ParseAspectKey` (already in the substrate — `keys.go:99`), fetch its root body, and run the
  normal plain evaluate path (`evaluateForEntry`) seeded from the **owner vertex** (type + id + root
  props) instead of acking. The owner vertex is the lens anchor (or a secondary node — the engine sorts
  that out: a secondary-node event re-executes and refreshes dependent fields, an anchor event projects
  the anchor's row). This is the plain analog of `evalAspectFanOut`, minus the actor enumeration —
  there is no fan-out set; the single affected vertex *is* the re-execution seed.
- **`KindLink`** (`lnk.<tA>.<idA>.<rel>.<tB>.<idB>`): a link create/tombstone changes graph topology, so
  (a) the **adjacency** must already be updated (it is — adjacency is built by its own bootstrapper
  consumer, independent of this lens), and (b) the lens must re-execute seeded from the link's
  **endpoints**. Reproject from **both** endpoint vertices (parse the 6-segment key →
  `vtx.<tA>.<idA>` and `vtx.<tB>.<idB>`), each through `evaluateForEntry`. This is the plain analog of
  `evalLinkFanOut`. A link tombstone (empty body) and a link create are handled the same way — the
  re-execute reads current adjacency either way.

**Scoping guard — do not regress the actor path.** Both new arms run **only** when
`actorEnumerator == nil` (the existing `if p.actorEnumerator != nil { return evalAspectFanOut(...) }`
branch stays first; the new code replaces the `else` ack-skip). The actor-aware behavior is untouched.

**Cost.** For an **anchor-scoped** lens (`{key:$actorKey}` seed) the re-execute is one-anchor cheap. For
a **whole-type-scan** lens (`MATCH (u:unit)`) the re-execute re-scans the anchor type — the **same** cost
those lenses already pay on every vertex-root event today; Fire 1 makes them *promptly* fresh on aspect
changes rather than incidentally fresh on the next root event. No new asymptotic cost; it tightens an
existing eventual-consistency window. (If a high-cardinality whole-scan lens later proves hot, the
optimization is to scope its seed — a lens-authoring concern, out of scope here.)

**Idempotency / ordering.** Re-executing reads committed Core KV + adjacency, so a redelivered or
out-of-order aspect/link event re-derives the same rows (the platform's overwrite-by-reprojection
invariant — refractor.md:414). No new state; no watermark.

### 2.2 Fire 2 — anchor-self filter-retraction (the headline)

With Fire 1, the anchor's own aspect/link/root mutation re-executes the lens. Add a **post-re-execute
anchor-presence check** to the plain path of `evaluateForEntry` (full engine, `actorEnumerator == nil`,
`envelopeFn == nil` — the exact scope the shipped tombstone branch uses at `evaluate.go:105`):

```
// after the normal re-execute produces `results` (the anchor is NOT tombstoned —
// the isDeleted case is the existing AnchorDeleteResult shortcut at evaluate.go:105-111):
anchorKey := normalizeToVertex(entry.CoreKVKey)         // ParseAspectKey/ParseVertexKey → vtx.<type>.<id>
anchorType := typeOf(anchorKey)
keys, ok := p.fullEngine.AnchorProjectionKey(p.fullCR, anchorKey, anchorType, anchorRootProps)
if ok && !resultsContainKey(results, keys) {
    results = append(results, simple.EvalResult{Delete: true, Keys: keys, Row: nil})
}
```

- **`AnchorProjectionKey`** is `AnchorDeleteResult` with the "tombstone" framing dropped — the key
  derivation is **identical** (it binds the anchor vertex read-free and resolves every declared key
  column; `ok=false` if the event type ≠ the anchor label, or any key column needs a Core-KV read, or a
  key resolves to a node). I propose a tiny refactor: extract the shared read-free derivation as
  `AnchorProjectionKey(cr, vtxKey, vtxType, rootProps)` and let `AnchorDeleteResult` call it (so the
  shipped tombstone path is byte-identical and the two retraction triggers share one tested derivation).
- **The presence check is the trigger.** If the anchor still projects (predicate still true), its key is
  among `results` → no Delete. If the anchor dropped (predicate flipped / keyed aspect gone / required
  link removed), its key is **absent** → emit a Delete on that key. This works for **both** seed
  regimes: anchor-scoped (`results` is {anchor row} or {}) and whole-scan (`results` is {all matching};
  the event's anchor is isolated by its derived key). It also **subsumes** the root-tombstone case
  (a tombstoned anchor is absent from `results` too) — but the shipped `isDeleted` shortcut stays as the
  cheaper short-circuit (no re-execute needed when the anchor is already gone).

**The safety property (the keystone).** A single anchor-keyed Delete is correct **iff the lens projects
at most one row per anchor, keyed by the anchor.** That is **exactly** the condition under which
`AnchorProjectionKey` succeeds: if every key column resolves read-free from the anchor, there is exactly
**one** possible output key per anchor (the output-collision guard, `evaluate.go:266`, already enforces
"≤1 non-delete row per anchor-derived key"). So **read-free-derivable keys ⟺ one-row-per-anchor ⟺ the
retraction Delete is sound.** When the keys are *not* anchor-derivable (multi-row / neighbor-keyed),
`AnchorProjectionKey` returns `ok=false` and we **fall through to today's behavior** (linger) — never a
wrong or partial Delete. This is the same guard the shipped tombstone path relies on; Fire 2 inherits its
proof.

**Aspect-event key normalization.** `ParseVertexKey` rejects a 4-segment aspect key (`keys.go`
`splitVertexKey` requires exactly 3 segments), so the caller must normalize an aspect event
(`vtx.unit.X.listing`) to its owner vertex (`vtx.unit.X`) via `ParseAspectKey` before calling
`AnchorProjectionKey`. For the live lenses the key column is `u.key` / `i.key` / `pr.key` — the anchor
key itself — which derives from the normalized vertex key with **no body read** (read-free). A lens
whose key needs a root-body *field* and is mutated by an aspect-only event (no root body in hand) falls
through (`ok=false`) — acceptable and rare; the live lenses don't hit it.

**Idempotency.** A Delete of an absent target key is a no-op (NATS-KV delete marker / Postgres
`DELETE … WHERE key=… ` affecting 0 rows). So even a spurious retraction (e.g. an anchor that never
matched) is harmless, and a redelivered drop event re-emits the same idempotent Delete. The next event
that makes the anchor match again re-upserts it.

### 2.3 Read path (P5) / write path (P2)

Unchanged. Refractor **reads** Core KV + adjacency to project (it is the materializer, not an app —
P5's app-read rule is about `cmd/<app>`, and Refractor is exempt as the projector). It **writes** only
its **own lens target** buckets/tables (not Core KV) — P2 is the Processor's, untouched. The retraction
Delete is a lens-target write on the same path as the shipped tombstone Delete (`writeResults`). No new
op, no new vertex/aspect/link, no new lens. This is purely a **projection-engine** change.

### 2.4 Fire 3 — neighbor-driven / multi-row retraction (designed, build-deferred)

The cases Fire 2's anchor-self presence-check **cannot** cover, by construction:

- **Whole-scan + neighbor-driven drop:** anchor X drops from a `MATCH (u:unit)` lens because a *different*
  vertex changed (so the triggering event is not about X). Fire 2 only retracts the **event's** anchor.
- **Multi-row-per-anchor partial drop:** a lens projecting N rows per anchor (keys vary per neighbor)
  loses one of the N. `AnchorProjectionKey` returns `ok=false` (keys not anchor-derived), so Fire 2 falls
  through, and "the anchor still projects N-1 rows" isn't even a zero-row signal.

Both genuinely need a **target-read diff**: read the target's existing keys for the affected
anchor/lens, compute the current re-projection's keys, and Delete the set difference. Mechanism:

- Add an adapter read seam — `ListKeysForAnchor(anchorKey) []key` on the `NATS-KV` and `Postgres`
  adapters (KV: a prefix list over the lens target bucket scoped by an anchor-id key convention;
  Postgres: `SELECT key WHERE anchor_id = $1`, requiring an `anchor_id` column the producer projects).
- On each re-execute for an anchor, diff `targetKeys(anchor) − currentKeys(anchor)` → Deletes.

**Build-deferred (the dead-scaffolding rule).** *Does this increment realize value before a consumer
exists?* **No.** Every live multi-row lens is either a **convergence target** (uses `violating` — must
**not** retract, by design) or an **actor-aggregate** (uses `collect` + the envelope absence-delete —
already retracts correctly). There is **no live plain multi-row lens that should retract**, and no live
whole-scan lens whose drops are neighbor-driven (all five live filter predicates are on the anchor's
**own** aspect/link — Fire 2 covers them). So Fire 3 is **ratified-design-on-the-shelf**, built when a
real consumer arrives (a richer read model, or an Elasticsearch/analytics target). Building it now would
ship an untested adapter seam with a stubbed need — exactly the scaffolding the rule forbids.

---

## 3. Reconciliation with the existing mental model

*Didn't we already handle this?* — **Partly.** Three of the retraction paths already exist; this is the
fourth:

| Trigger | Engine path | Status |
|---|---|---|
| Anchor **root-tombstoned** (plain lens) | `AnchorDeleteResult` + `evaluate.go:105-111` | ✅ shipped `679fe25` |
| Actor vertex tombstoned (capability lens) | actor shortcut `evaluate.go:88` | ✅ shipped |
| Required relationship gone (simple engine) | `evaluateAnchor` zero-row + `deleteResult` on tombstone only | partial (tombstone only) |
| **Anchor stays alive, predicate flips / keyed-aspect gone / required link gone** | **none** | **this design (Fire 2)** |
| Anchor freshness on aspect/link-only update (plain lens) | actor path only (`evalAspectFanOut`/`evalLinkFanOut`) | **this design (Fire 1)** |

*Does this duplicate or contradict the `violating`-flag pattern?* — **No.** Convergence lenses keep
`violating` (a disappearing convergence row reads to Weaver as a deletion — intentional). This design
serves **plain read-model lenses**, where a disappearing row is exactly the desired semantics. The two
patterns coexist: convergence = "always one row, flag flips"; plain filtered = "row present iff matching".

*Does this introduce new state?* — **No.** No watermark, no per-anchor memory, no new bucket. Fire 1
re-executes from committed Core KV + adjacency (the existing overwrite-by-reprojection model). Fire 2
derives the retract key from the AST + the event vertex, read-free (no target read, no Core-KV read).
Fire 3 *would* read the target (its whole point) but is deferred.

*Is the design-of-record different from a Phase-1 simplification?* — The plain-lens ack-skip of
aspect/link events (`pipeline.go:491-495,504-506`) reads in the code as a deliberate *"no handler
registered"* placeholder, not a permanent design — the actor path's `evalAspectFanOut` is the pattern it
was always going to grow into. Fire 1 completes that, it doesn't reverse a decision.

---

## 4. Migration / compatibility · test strategy

**Compatibility.** Purely additive to projection behavior. A lens that doesn't filter (no `WHERE`, no
required relationship) never drops an anchor, so `resultsContainKey` is always true → no Delete ever
emitted → byte-identical to today. The shipped tombstone path is unchanged (its `isDeleted` shortcut
runs before the new presence check, and shares the extracted `AnchorProjectionKey`). No DDL, no key
shape, no contract, no bootstrap version bump.

**Test strategy** (mirrors the shipped anchor-tombstone tests — `anchor_delete_test.go`,
`pipeline/anchor_tombstone_test.go`):

- **Fire 1 (unit + pipeline):** a plain lens, embedded-NATS substrate fixture (`jsstore.Dir(t)` for
  CI-parallel safety — see [[project_ci_test_parallelism]]). Assert: an **aspect-only** mutation on the
  anchor reprojects the row (field refresh); a **link** create/tombstone reprojects from both endpoints;
  the **actor-aware** path is unaffected (regression guard). New `KindAspect`/`KindLink` plain arms
  table-tested for the owner/endpoint normalization.
- **Fire 2 (unit + pipeline):** `AnchorProjectionKey` table (single + composite key, read-free vs
  needs-read → `ok=false`, event-type ≠ anchor-label → `ok=false`, aspect-key normalization). Pipeline:
  upsert → **WHERE flips** (aspect removed) → Delete on the same key; **keyed-aspect deletion** → Delete;
  **required-link removal** → Delete; **multi-row / neighbor-keyed lens** → no Delete (fall-through);
  **never-matched anchor** → idempotent no-op Delete (or no Delete — either is safe; pin the chosen
  behavior).
- **Ephemeral-stack e2e (the executable PO-style proof):** on `availableListings` — `SetListing` →
  appears in the lens target → remove the `.listing` aspect → **gone** from the target. This is the
  end-to-end demonstration the row's "true emit-only-when-violating" asks for. Can land from either
  stream (it exercises LoftSpace territory) but the platform mechanism is proved by the unit+pipeline
  tests.
- **Gates:** `go build ./...`, `make vet`, `golangci-lint run`, STRICT `lint-conventions`,
  `go test ./internal/refractor/...` (with `-race` on pipeline), plus the loftspace/clinic package tests.
  Gate 2/3 are unaffected (no auth/capability-plane change) — defer to CI if the shared stack collides
  locally.

---

## 5. Risks + alternatives

### 5.1 Risks

- **R1 — whole-scan reprojection cost amplified by Fire 1.** A whole-scan lens now re-scans its type on
  *every* aspect/link event for that type, not just root events. **Bounded:** it is the same per-event
  scan those lenses already run on every vertex-root event; the event *rate* rises (aspect mutations are
  more frequent than root mutations) but each scan is unchanged, and at single-cell MVP cardinality this
  is well within budget. **Mitigation if it ever bites:** author the lens anchor-scoped, or batch — both
  out of scope. Flagged, not blocking.
- **R2 — spurious retraction of a never-matched anchor.** An aspect/link event for an anchor that this
  lens never projected → re-execute yields no row for it → presence check emits a Delete on its
  (read-free) key. **Harmless:** Delete of an absent key is a no-op. Pin the behavior in tests so it's
  intentional, not surprising.
- **R3 — aspect-tombstone visibility.** If "removing" an aspect is a soft-tombstone (body with
  `isDeleted`) rather than a hard delete, the WHERE only flips if the engine's aspect read filters the
  tombstone. This is **existing** engine behavior (how `resolveProperty` reads an aspect), not changed
  here; if a future lens needs predicate-flip on aspect *soft*-tombstones, that's an engine aspect-read
  concern, separate. Noted so it isn't conflated with this design.
- **R4 — ordering of the retract Delete with concurrent upserts.** The retract Delete and a subsequent
  re-match upsert are separate events; the platform's per-key overwrite-by-reprojection makes the last
  event win, and the keys are identical (same anchor), so there's no cross-key race. Safe.

### 5.2 Alternatives considered (earn the recommendation)

- **A1 — keep the `violating`-flag pattern for plain lenses too** (project every candidate with a
  `matching` boolean; readers filter). *Rejected:* pushes the filter to every reader, bloats the target
  with non-matching rows (no GC), and is exactly the workaround the row asks to retire for read-model
  lenses. It is correct for **convergence** (kept) but wrong for a read model a human/FE reads directly.
  Could a variant beat the recommendation? Only if readers were uniformly filter-capable and target
  bloat were free — neither holds for a P5 read model. Rejected on merit.
- **A2 — target-read diff for *everything* (skip the read-free anchor-self path, do Fire 3 universally).**
  *Rejected as the primary:* it makes **every** retraction pay a target read per event, and requires the
  adapter seam + an `anchor_id` projection convention on every filtered lens — heavyweight for the live
  cases that Fire 2 closes read-free. The use-what-exists option (extend `AnchorDeleteResult`) is
  strictly simpler for the live demand and is **already proven** by the shipped tombstone twin. Fire 3 is
  the *right* tool only for the cases Fire 2 structurally can't reach — so it's the deferred complement,
  not the primary. (This is the "simplest extension of existing machinery" discipline: I re-asked whether
  a target-diff variant could beat the read-free path for the *live* cases — it can't; it only wins where
  Fire 2 doesn't apply.)
- **A3 — detect drops by re-scanning the target on a timer (sweep).** *Rejected:* introduces new periodic
  state + a staleness window, duplicates the Weaver sweep machinery, and is strictly worse than
  event-driven retraction for the anchor-self case. A sweep is only defensible as a backstop for the
  neighbor-driven case — which Fire 3's diff handles on-event without a timer.
- **A4 — fix Fire 2 without Fire 1 (retract only on root-field flips).** *Rejected:* no live lens
  filters on a root field; without Fire 1 the headline fix retracts **nothing real**. Fire 1 is not
  optional polish — it is what makes Fire 2 reach a live consumer. (Sequencing them the other way would
  ship dead scaffolding.)

---

## 6. Contract surface (the search, so ratification is one-look)

I read every `docs/contracts/*` section that could govern projection retraction/freshness:

- **Contract #1 (addressing/envelope):** key shapes only — aspects 4-seg, links 6-seg. The design
  **honors** them (uses `ParseAspectKey`/`ParseVertexKey`; emits no new shape). No change.
- **Contract #6 (capability-kv):** governs the **capability** lens projection (auth correctness =
  projection correctness) and its revocation/delete semantics. The plain read-model lenses here are **not**
  capability lenses; §6 doesn't prescribe their reprojection timing. No change.
- **Contract #10 (orchestration surfaces):** governs convergence-target semantics (the `violating`
  contract Weaver reads). This design **explicitly preserves** it (convergence lenses unchanged). No change.
- **Contracts #5 (health), #2/#3/#4 (op/batch/idempotency), #7/#8/#9:** no clause on lens reprojection or
  projection completeness.

**Projection reprojection timing + completeness is documented in `docs/components/refractor.md`, not in a
frozen contract** — which is precisely why the anchor-tombstone retraction (`679fe25`) shipped with "no
frozen-contract change (lens retraction lives in refractor.md, not docs/contracts/*)". This design is its
twin and inherits that finding. **Doc update (Fire 1/2, in the same commits, not a contract):** rewrite
the refractor.md "Anchor-tombstone retraction" residual (L200-205) and the lens-lifecycle aspect/link
notes (L259-267) to describe the landed plain-lens aspect/link reprojection + filter-retraction.

**Conclusion: no `docs/contracts/*` edit is staged. Nothing for Andrew to ratify on the contract plane.**

---

## 7. Fire-by-fire decomposition (for the Lattice Steward)

Each fire independently shippable + green. Build **only after ✅ Andrew-ratified**.

- **Fire 1 — plain-lens aspect/link reprojection (freshness prerequisite; live value on its own).**
  Replace the `KindAspect`/`KindLink` ack-skip in the plain (`actorEnumerator == nil`) arm of
  `HandleMessage` with a reproject-from-owner/endpoints path (the plain analogs of
  `evalAspectFanOut`/`evalLinkFanOut`). Unit + pipeline tests (aspect refresh, link refresh, actor-path
  regression guard). refractor.md lifecycle notes updated. **Full 3-layer** (it broadens what events a
  lens reacts to — blast radius across every plain lens; the convergence lenses are plain too, so verify
  they don't double-fire or change `violating` timing). *Ships a real staleness fix even if Fire 2 never
  lands.*
- **Fire 2 — anchor-self filter-retraction (the headline).** Extract `AnchorProjectionKey` from
  `AnchorDeleteResult`; add the post-re-execute presence-check Delete to the plain full-engine path.
  Unit (`AnchorProjectionKey` table) + pipeline (WHERE-flip / keyed-aspect-deletion / required-link-removal
  → Delete; multi-row → fall-through; never-matched → idempotent) + the `availableListings` ephemeral-stack
  e2e. **Full 3-layer** (it emits Deletes on the read path — the highest-risk surface; verify the
  one-row-per-anchor safety guard holds and the `violating`/aggregate lenses are untouched). **Depends on
  Fire 1.**
- **Fire 3 — neighbor-driven / multi-row target-diff retraction (designed, BUILD-DEFERRED).** Adapter
  `ListKeysForAnchor` seam (NATS-KV + Postgres) + set-difference Deletes. **Do not build until a real
  consumer exists** (no live plain multi-row / neighbor-driven-drop lens today). Kept on the shelf,
  ratified as a design, sequenced behind a real read model that needs it.

---

## 8. Self-adversarial pass (folded in)

Ran an adversarial read of the design against the four Andrew-enforced reflexes (designer SKILL §2):

1. **"Resolved from context" is not a mechanism — name the transport.** Checked: Fire 2's retract key is
   not "resolved from the row" — it is derived by `AnchorProjectionKey` binding the **event vertex**
   read-free; I traced that the event carries the anchor key (after `ParseAspectKey` normalization) and
   that `availableListings`'s key column (`u.key`) needs **no** body read. The transport is named and
   verified in code (`anchor_delete.go:80-104`).
2. **A workaround that bends an invariant is a red flag — re-verify the premise.** Checked the "plain
   lenses ack-skip aspect/link events" premise directly in `pipeline.go:491-495,504-506` (not inferred);
   confirmed `FilterSubject` is the whole bucket (`main.go:380`) so the events **do** arrive and are
   dropped (not filtered out upstream). The premise is real, grounded in the ~one file.
3. **Default direction of any boundary fails closed.** This is not an authz surface, but the analog is:
   **omission must not silently mis-delete.** The retraction Delete only fires when `AnchorProjectionKey`
   succeeds (anchor-derived keys ⟺ one-row-per-anchor) — absence of that condition **falls through to
   linger** (the safe default), never a wrong Delete. The unsafe direction (deleting a row that should
   survive) is structurally impossible: a multi-row/neighbor-keyed lens returns `ok=false`.
4. **Simplest extension over a new mechanism.** Fire 2 reuses the shipped, tested `AnchorDeleteResult`
   derivation rather than a new key engine; Fire 1 mirrors the shipped `evalAspectFanOut`/`evalLinkFanOut`
   rather than a new reprojection path; Fire 3 (the only genuinely-new machinery) is **deferred** until a
   consumer earns it.

Finding folded in: the original draft framed Fire 2 as the headline and treated aspect reprojection as a
footnote — the adversarial pass surfaced that **Fire 2 retracts nothing live without Fire 1** (every live
predicate is aspect/link), so Fire 1 was promoted from footnote to the load-bearing first increment and
the importance corrected ★→★★. (This is the assumed-producer/transport blind spot —
[[feedback_designer_chain_grounding]] — caught on my own draft.)

---

## 9. Open questions — resolved

- **Q: anchor-scoped vs whole-scan — does the trigger differ?** Resolved: the **anchor-self presence
  check** (§2.2) handles both uniformly — derive the event-anchor's key, check absence in `results`.
  Whole-scan returns the full set; the event-anchor's key isolates the drop. No per-regime branching.
- **Q: which engine?** Resolved: **full engine** is the live target (`WHERE` is full-only). The simple
  engine has no predicate; its only drop is a required-relationship removal, which after Fire 1 routes
  through the same plain link-reprojection → its `deleteResult`-on-zero-rows could be added symmetrically,
  but **no live simple-engine lens needs it**, so it's a Fire-3-adjacent deferral, not Fire 2.
- **Q: do we need a contract change?** Resolved: **no** (§6 — projection timing/completeness is
  refractor.md, not a frozen contract; the tombstone twin set the precedent).
- **Q: build Fire 3 now?** Resolved: **no** (dead-scaffolding rule — no live consumer). Flagged for Andrew
  as the one scope decision.

---

## Board

Row `lattice.md` → *Read-model / projection maturity* → set **🏗️ designing → 📐 awaiting-Andrew**, link
this doc, ratification state **📐 awaiting-Andrew → ✅ Andrew-ratified**. The Lattice Steward builds
**Fire 1 → Fire 2** after ratification; **Fire 3** stays on the shelf until a real consumer arrives.
