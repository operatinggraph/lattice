# Contract #10 (Weaver) — target Lens output, target + playbook, planner

> **A part of [Contract #10 — Orchestration Surfaces](10-orchestration-surfaces.md)** (the index +
> shared revision history). Section numbers **§10.2 / §10.8** are unchanged. The §10.2 ↔ §10.8
> detection↔remediation binding lives here in full. The **Augur** AI-reasoning tier — whose `augur`
> block and `proposedOp` action are declared in §10.8 below — is specified in its own part,
> [10-orchestration-augur.md](10-orchestration-augur.md).

## 10.2 Weaver target Lens output (D4) — **FROZEN 2026-06-02** (amended 2026-06-18, 13.1)

One row **per candidate entity**, carrying a `violating` flag — **not** row-only-when-violating
(avoids Refractor retraction). Projected by the existing `nats_kv` adapter.

**Bucket — one shared, primordial, dash-named bucket** (NATS KV bucket names are stream tokens:
`[A-Za-z0-9_-]+`, **no dots**; cf. `core-kv` / `weaver-state` in `primordial.go`). All convergence
targets project into the single `weaver-targets` bucket under a disjoint `<targetId>.` key prefix —
the **same contract-contribution pattern as capability-kv** (§6.1): the bucket is core-owned/primordial,
packages project their target rows into it, no per-install bucket provisioning. (`weaver-targets` is
**NEW — joins the primordial bucket create list**, like `loom-state` §10.3.) Unlike capability-kv's
core-fixed prefixes, `<targetId>` is package-authored, so **`targetId` uniqueness across installed
targets is install-validated** (§10.8) — two packages must not collide in the shared bucket.

**Key on the entity *ID*, not the full vertex key.** A candidate entity is **always a vertex** (never
an aspect — aspects surface only as gap predicates / param columns *within* a vertex-candidate row), so
its key is always `vtx.<type>.<id>`. The dotted full key must **not** be embedded in the NATS KV key
(its dots are subject-token separators → brittle). Within a `<targetId>.` partition every candidate is
the same type, so the type segment is redundant: the entity segment is just the **NanoID**. The full
key lives in the document (`entityKey`) — document, not key, is the source of truth (standing principle).

```
bucket:  weaver-targets                              # shared, primordial
key:     <targetId>.<entityId>                       # e.g. leaseApplicationComplete.Lk2Pn6mQrtwzKbcXvP3T
value:   {
           "entityKey":   "vtx.leaseApp.<id>",       # echo of the candidate vertex key
           "violating":   true,                      # lens-projected; Weaver lane-1 watch filter
           "missing_onboarding": true,               # gap columns: missing_<gap> (snake_case bool)
           "missing_bgcheck":    false,
           "missing_payment":    true,
           "missing_signature":  true,
           "applicant":   "vtx.identity.<id>",       # param column(s) — §10.8 templates row.<field>
           "projectedAt": "2026-05-12T14:32:18.142Z" # deterministic as-of (Contract #6 semantics)
         }
```

**Convergence lens as an `actorAggregate` (Amended 2026-06-18 — 13.1, External I/O Bridge).** A
convergence target whose row must reproject on a change to a *linked* constituent — e.g. a leaseApp
that reads identity aspects **and** a service-instance vertex **across links**
(`MATCH (app)-[:applicationFor]->(id), (id)<-[:providedTo]-(inst:service)`) — MAY be projected by an
**`actorAggregate`** lens (Refractor Output descriptor, `projectionKind: "actorAggregate"`) instead of
the plain `nats_kv` projection (which reprojects only its own anchor vertex and would miss a linked
constituent flipping). **The §10.2 key shape is unchanged** (Option (b) at ratification): such a lens
declares an explicit **key column** (the bare-NanoID `<entityId>`) that the actorAggregate `BuildKey`
emits **instead of** its default `{actorSuffix}` (= `<type>.<id>`), so the row key stays
`<targetId>.<entityId>` (bare NanoID) and Weaver's `splitRowKey` accepts it unchanged. The frozen
§10.2 key + `splitRowKey` stay frozen; the change is localized to the Refractor Output-descriptor
machinery Epic 12 introduced.

**Watch.** Weaver does a **filtered watch `<targetId>.>`** per target it manages (discovering each
target's id from the `meta.weaverTarget` registry, §10.8). Row-per-candidate (incl. non-violating)
means Weaver watches all rows under its prefix and **acts only on `violating == true`** (lane 1).

**Column conventions (the §10.2↔§10.8 contract seam):**
- `entityKey` — echo of the candidate vertex key (the value mirrors the key, as the cap-doc echoes
  `key`/`actor`).
- `violating` — **lens-projected** bool; the Lens decides what counts as needing convergence (it is
  *not* an implicit OR of the gaps). This is Weaver's lane-1 dispatch filter.
- gap columns **`missing_<gap>`** — snake_case bools. **The §10.8 `gaps` map keys bind *exactly* to
  these column names.** The Strategist's gap-detection = scan keys with the `missing_` prefix whose
  value is `true`.
- **param columns** (free-form, e.g. `applicant`) — whatever the §10.8 playbook templates reference
  (`row.<field>`); the Lens **must project every column the playbook templates name**.
- **`freshUntil`** (optional, engine-recognized convention) — an RFC3339 instant the target cypher
  computes as `resolve + window`. The engine converts it into an `@at` schedule (the time→op temporal
  lane, §10.4) and **never computes the window itself** — the freshness rule lives in the cypher, the
  engine only turns the projected deadline into a timer. A free-form param column by storage, named by
  convention so the engine/Lens seam is explicit.
- `projectedAt` — deterministic as-of provenance, **same semantics as Contract #6 §6.3** (the
  candidate's `lastModifiedAt`, not a wall-clock read). The NATS KV entry's own revision arrives free
  on each watch update, so it is **not** projected into the value.
- **`priority`** (optional, engine-recognized convention, Fire 8 — additive, lands with the fire per
  §4's deferred §10.2 rider) — an integer, higher = more urgent. Consulted **only** when the row's
  target declares an **`admission`** block, a sibling of §10.8's `mode`:
  ```
  "admission": {
    "globalRate"?: <float>,                    // tokens/sec bounding the target's TOTAL dispatch rate
    "adapterRates"?: { "<adapter>": <float> }  // per-adapter rate; overrides globalRate for a gap whose
  }                                             // resolved action declares that Adapter (§10.8 table)
  ```
  Absent (every target before this fire) is unbounded — byte-identical dispatch, no row read. When
  present, it **paces** (never gates for correctness) which already-resolved dispatches fire now vs. on
  a later redelivery: a gap deferred by admission gets **no mark, no episode, no Health issue** —
  ordinary pacing, not a fault, so the §10.3 anti-storm/idempotency machinery is untouched. Precedence
  mirrors the action-selection convention (explicit > general): a gap whose resolved action declares a
  matching `adapterRates` entry is governed by that rate instead of `globalRate`. Ties among contended
  dispatches are broken by `priority` — higher first, absent/non-numeric = 0 (this column's default).
  Purely process-local bookkeeping (mirrors the Fire-7 contraction/oscillation diagnostics): a restart
  resets every budget's accrued tokens, never a correctness concern. A free-form param column by
  storage, named by convention like `freshUntil`; every target without an `admission` block ignores it
  entirely.

**No read-path authz anchor here.** The `weaver-targets` bucket is internal operational state read
only by Weaver (a bootstrap-provisioned service actor); it is never on the read-path, and read-path
auth is Phase-3-deferred (D1). The scoping the remediation needs is carried by the **param columns**
above, and each remediation op the Actuator submits carries its own `authContext`. *If* a target Lens
is **also** projected to the Phase-3 Postgres read-path, it carries the D1 authz anchor **there** like
any protected Lens — orthogonal to this bucket.

**Retraction (per D4, settled).** Gap closes → `violating` / `missing_*` flip via **upsert**. True
entity deletion → row deleted (`IsDeleted` path). **Deferred:** true emit-only-when-violating requires
Refractor negative/filter-retraction projection. Freshness rules live in the **target cypher**
(`missing_bgcheck = NOT EXISTS(check WHERE date > now − window)`).

---


## 10.8 Weaver target + playbook (package data) — **FROZEN 2026-06-02** (amended 2026-06-18, 13.1)

A `meta.weaverTarget` meta-vertex bundles the **detection** (violation Lens, §10.2) and the
**remediation** (gap → action playbook). CDC-loaded like `meta.lens` / `meta.loomPattern`; Weaver
reconciles **one filtered watch (`weaver-targets` `<targetId>.>`) per target**.

```
meta.weaverTarget {
  "targetId": "leaseApplicationComplete",
  "lensRef":  "<meta.lens id of the violation Lens (§10.2 output)>",
  "gaps": {
    "missing_onboarding": { "action": "triggerLoom",  "pattern": "onboarding",
                            "subject": "row.applicant" },
    "missing_bgcheck":    { "action": "triggerLoom",  "pattern": "backgroundCheck",
                            "subject": "row.applicant" },
    "missing_payment":    { "action": "triggerLoom",  "pattern": "collectPayment",
                            "subject": "row.applicant" },
    "missing_signature":  { "action": "assignTask",   "operation": "SignLease",
                            "assignee": "row.applicant", "target": "row.entityKey" }
  },
  "augur": {                                     // ✅ Andrew-ratified 2026-06-27 — see "Augur escalation" below
    "escalate": ["unplannable"],                 // stuck-gap triggers escalated to AI reasoning (the Augur)
    "pattern":  "augurReasoning",                // the triggerLoom externalTask reasoning pattern
    "model":    "claude-opus-4-8"                // optional adapter model override (default: claude-opus-4-8)
    // "autoApply": { ... }                      // Fire 3 ONLY — DESIGNED, not built until Andrew ratifies auto-apply
  }
}
```

### The §10.2 ↔ §10.8 binding (the detection↔remediation seam)

- **`targetId` is the single binding token:** it is *both* this vertex's id *and* the `weaver-targets`
  key prefix the `lensRef`'d Lens projects rows under (`<targetId>.<entityId>`). They must match, and
  **`targetId` is install-validated unique** across installed targets (the bucket is shared — a
  collision would interleave two targets' rows; same install-time check class as the `gaps`-key rule below).
- **Every `gaps` key MUST be a `missing_<gap>` column** produced by the §10.2 Lens. Install-time
  validation: each `gaps` key matches the `missing_` convention. The Strategist detects gaps by
  scanning the row's keys with the `missing_` prefix whose value is `true`.
- **A row column `missing_*: true` with no `gaps[col]` entry is a config error → alert**, never
  silently skipped (FR29 "never silently drop" discipline). Weaver surfaces it to Health KV.

### Action contracts

Every action's params are resolved per row (templating below). The Actuator submits ops under
**Weaver's bootstrap-provisioned service-actor authority**.

| `action` | params | effect |
|----------|--------|--------|
| `triggerLoom` | `{ pattern, subject }` | submit `StartLoomPattern{ patternRef: pattern, subjectKey: subject }` → Loom (§10.5). `subject` must resolve to a vertex of the pattern's `subjectType`. **Auth: see below.** Also the path for **external remediation** (since 2026-06-18, 13.1): `triggerLoom` a pattern whose body is an `externalTask` (§10.5) — this **replaces the retired `nudge` action**. |
| `assignTask` | `{ operation, assignee, target }` | `CreateTask` (§10.1): `assignedTo`→`assignee`, `forOperation`→`operation`, `scopedTo`→`target`. |
| `directOp` | `{ operation, target?, params?, reads? }` | submit `operation` directly as a remediation op. `reads?` is the dispatched op's `contextHint.reads` — bare vertex keys, each a literal or `row.<column>` — so an op that must hydrate its candidate vertex (e.g. `TombstoneObject` reading the object's `linkEpoch`) gets the key straight from the lens row. Additive + `omitempty`: a `directOp` that omits it dispatches read-free exactly as before. A clause-billing target is a canonical consumer: `operation` is the literal `DebitAccount`, `target`/`params`/`reads` row-templated (the amount as a numeric param column; clause + account keys routed into `reads` for hydration). |
| `proposedOp` | *(none — sourced from the row)* | **Additive, opt-in (Augur dispatch, Fire 2b).** Dispatch the **row-carried** `proposedAction` + `proposedParams` (materialised into a `GapAction`) after a **dispatch-time deterministic re-validation** (action ∈ the escalation catalog `{triggerLoom, assignTask, directOp}` · live-registry resolution via the existing `buildPlan` · **default-deny scope** to the row's TRUSTED candidate `candidateKey` · op ∈ Weaver's service-actor authority). Unlike the three static actions, the op + params are *data per row*, not playbook config; the proposed op carries a **proposal-scoped deterministic requestId** so a sweep re-dispatch collapses on the Contract #4 tracker (at-most-once). Used **only** by the `augur` package's primordial `augurDispatch` convergence target (see "Augur dispatch" below); wiring `proposedOp` to a row whose source is not a §5-validated approved proposal is a package bug. The `directOp`-must-be-literal guard stays intact for ordinary playbooks — `proposedOp` is the gated sibling for the one §5-validated dynamic-op surface. |
| `surface` | `{ issueCode, issueSeverity? }` | **Additive (FR28/FR29 Fire 3).** Dispatch **nothing** — no op, no mark, no OCC, no episode. While the gap column stays true, raises a Contract #5 §5.5 `issues[]` entry keyed `issueCode` at `issueSeverity` (default `warning`); the issue clears via the ordinary level-reconciled mark-clearing pass once the row stops naming the column. `issueCode` is required; `issueSeverity` ∈ `{warning, error}`. Manual-intervention-only — the sibling of `triggerLoom`/`assignTask`/`directOp`/`proposedOp` for a gap the playbook author wants surfaced, never remediated. Used by `orchestration-base`'s primordial `unroutedTasks` target (`missing_claim` → `{action:"surface", issueCode:"UnroutedTasks"}` — an open role-queued task left unclaimed past its own `expiresAt`). |

> **`nudge` — RETIRED (Amended 2026-06-18 — 13.1, External I/O Bridge).** The `nudge` GapAction (and the
> `operation` field added to it in Story 10.2) is removed: external I/O moves out of Weaver (convergence
> *detection*) into **Loom + the bridge** (deterministic *execution*). Weaver's job collapses to **detect
> → `triggerLoom`**; it no longer dispatches or resolves external calls. External remediation is now
> `triggerLoom` of a pattern containing an `externalTask` (§10.5/§10.6), and the FR58 claim/idempotency
> guarantee is carried by the service-instance vertex on the bridge path (§10.3 `weaver-claims` retirement
> note). Weaver retains `triggerLoom` / `assignTask` / `directOp`.


### Augur escalation & dispatch → [10-orchestration-augur.md](10-orchestration-augur.md)

The Augur AI-reasoning tier (escalation on `unplannable` / `exhausted` → `vtx.augurProposal` → human
review → `proposedOp` dispatch) is specified in its own part,
**[10-orchestration-augur.md](10-orchestration-augur.md)**. The `augur` block shape (in the target JSON
above) and the `proposedOp` action row (in Action contracts above) are its Weaver-side hooks.

### Templating

A param value is **either a literal** (`pattern: "onboarding"`) **or the token `row.<column>`**
(`subject: "row.applicant"`) — no expressions. The Strategist substitutes `row.<column>` with that
column's value from the violation row. A `row.<column>` that resolves null/absent is a **data error**
— surface, do not fire a malformed remediation. (This is why §10.2 requires the Lens to **project
every column the playbook templates name**.) Substitution is **type-preserving**: a `row.<column>`
resolving to a JSON number (e.g. a lens-computed `amountCents`) is passed to the op as a number, not
stringified — `resolveParam` returns the row value verbatim. A monetary param is integer cents.

### `triggerLoom` authorization — `StartLoomPattern` + pattern-as-target

Starting a Loom instance is the op `StartLoomPattern` carrying **`authContext.target =
vtx.meta.loomPattern.<patternId>`** (the pattern definition vertex). Per-pattern authorization then
falls out of the existing capability scope model (Contract #6 §6.7), with **no per-pattern op type**:

- **Weaver** holds `StartLoomPattern @ scope: any` (seeded in `orchestration-base`) → may start any
  pattern. This is the only caller Phase 2 needs.
- **External / per-pattern callers** would use `scope: specific` (allowed-pattern-target list) or a
  task-scoped ephemeral grant (§10.7). **Phase-3 carry:** step-3's `matchPlatformPermission` currently
  **actively DENIES** platform `scope: specific` (returns `AuthContextMismatch`, "not implemented" —
  it is not a silent pass; Contract #6 §6.7). So **do not seed an external `scope: specific`
  `StartLoomPattern` grant in Phase 2** expecting it to authorize — it won't. The *mechanism* is specced
  now; only `scope: any` (Weaver) is **implemented and exercised** in Phase 2.

This also fills a Loom gap: §10.5/§10.6/§10.7 settled auth for the *steps within* a pattern
(userTask→ephemeral grant; systemOp→engine authority) but not the pattern *start* — `StartLoomPattern`
+ pattern-as-target is that contract.

### Flow & anti-storm

Lane-1 sees a `violating` row → for **every** currently-true `missing_*` gap **not already
in-flight**, the Strategist looks up `gaps[col]` and the Actuator executes:

- **In-flight mark** in `weaver-state`, keyed **`<targetId>.<entityId>.<gapColumn>`** (entity *ID*,
  not the dotted full key — §10.2). Set via **KV create (CAS-on-absent)** — *that* create **is** the
  anti-storm OCC: concurrent evaluations of the same gap race the create, the loser drops, the winner
  dispatches. Value shape (incl. TTL/lease, full `entityKey`) freezes in §10.3.
- **Mark clears** on **gap-close**, **planned-leg completion** (Planner extension, ratified Andrew
  2026-07-05: the pinned leg's declared `effects` all hold in the current row), or **lease
  expiry** — all **level-reconciled, not edge-triggered**
  (§10.3 weaver-state): on each watch update and reconciler sweep, Weaver compares the **current** row's
  `missing_<col>` against existing marks and deletes any whose column is now `false` (a coalescing watch
  can drop the transitional flip, so Weaver must not depend on *seeing* it). Lease expiry is enforced by
  a **NATS per-key TTL + active reconciler** (§10.3) — a dead reconciler can't wedge a gap forever.
  Async remediations (Loom — incl. an `externalTask`'s external call via the bridge) close their gap
  when their downstream work lands and the Lens re-projects `false`; `claimedAt` tags the episode so a
  stale prior-episode mark can't shadow a re-open. **Re-fire idempotency by action** is pinned in §10.3
  (`triggerLoom` / `assignTask` = documented rare-double; an `externalTask` external call dedups on the
  **deterministic** bridge result-op `requestId`, §10.3 `weaver-claims` retirement note).
- **Gaps fire in parallel** — independent remediations run concurrently.
- **Gap *dependencies* are encoded in the target Lens predicates, not in Weaver.** If bgcheck needs
  onboarding first, the Lens makes `missing_bgcheck` true only once onboarding is done
  (`missing_bgcheck = onboarded AND NOT EXISTS(recent check)`). A dependent gap simply isn't `true`
  until its prerequisite closes, so parallel firing is always safe. Weaver stays a generic parallel
  dispatcher; ordering is declarative.

Target + playbook are **package data**; the Weaver engine is a generic dispatcher.

### Planner extension — selection & synthesis (Ratified 2026-07-04 — build-pending)

> **Ratified 2026-07-04 (Andrew), both forks accepted** — Weaver re-expands its *selection* altitude
> (choosing *what* to dispatch) while the 13.1 *I/O placement* stays intact (external I/O = Loom +
> bridge; Weaver never holds an adapter), and the build is **in-place + shadow mode + per-target
> cutover**, not a parallel engine. The surface is frozen; the engine work is **build-pending** across
> the 9 fires in the design doc.
> Full design: `_bmad-output/implementation-artifacts/weaver-planner-mandate-design.md`. **Everything in
> this subsection is additive and opt-in**: a target carrying none of the new fields — and every target
> installed today — behaves **byte-identically** to the frozen shapes above. Nothing here changes the
> action table, templating, anti-storm, or the augur block; external I/O placement (13.1) is untouched.

**Op-DDL `effects` (additive).** An op DDL MAY declare `effects: [<guard>…]` — §10.5 guard-grammar
predicates (atoms + combinators, the two subject-path shapes, pinned absence semantics; the Starlark
escape hatch stays RESERVED) that the op's commit entails on its target subject. Install-time validation
rejects wholesale on a malformed guard (same doctrine as pattern load). *(Placement note for
ratification: specified here because Weaver is the consumer; may relocate to a DDL self-description
contract.)*

**`meta.weaverTarget` additions** (all install-validated, all optional):

```
"mode": "shadow" | "planned",              // target-level; ABSENT = frozen behavior, byte-identical
"gaps": {
  "missing_<g>": { "action": … }           // frozen shape — ALWAYS wins (operator override)
               | { "candidates": [ { "action": …, "pre"?: <guard>, "cost"?: int }, … ] }
               | { "goal": <guard>,        // synthesis target (per-leg execution below)
                   "goalColumns"?: { "<column>": "<aspect path>" },  // see below (Fire 6 Increment 2)
                   "actions": [ { "ref": "<unique>", <one frozen action's fields>,
                                  "pre"?: <guard>, "effects": [ <atoms> ], "cost"?: int }, … ] }
                                             // the gap's planning catalog — see below (2026-07-05)
}
```

- **Precedence per gap: explicit `action` > `candidates` > `goal`.** In `mode: "shadow"` the planner's
  choice is recorded (heartbeat counters + a per-target Health doc) and **never dispatched** — the table
  path dispatches exactly as frozen. Only `mode: "planned"` dispatches planner choices.
- **Selection (`candidates`) is deterministic:** preconditions evaluate against the §10.2 **row** (a
  `pre` referencing a column the lens does not project is an install-time error — the existing
  §10.2↔§10.8 column seam; no new Weaver Core-KV reads), ranked by (precondition satisfaction,
  windowed close-rate from `__effect` (§10.3), declared `cost`, then lexicographic actionRef). The
  `maxretries_<g>` budget bounds the **gap across candidates**.
- **Synthesis (`goal`) is bounded goal regression** over the gap's **declared `actions` catalog**
  (below — a closed, package-authored set; *revises the ratified "installed catalog (ops with `effects`
  + Loom patterns as macro-actions)" wording, 2026-07-05:* an op's DDL `effects` are the integrity
  source an entry mirrors, but an op effect alone carries no dispatch binding — no assignee, no params —
  so a global ops-derived auto-catalog is **reserved**, not implied), a pure function of (row, catalog,
  `__effect` window) with canonical tie-breaking. **`goalColumns`** (per-gap, optional — scoped to the same gap as its `goal`, never
  shared across gaps in one target) is how that "pure function of row" stays true when a `goal` addresses
  an **aspect** path (e.g. `subject.signature.data.signedAt`, matching a real op's declared `effects`): a
  §10.2 row flattens an aspect-projected column onto a bare name with no aspect tag, and the default
  row→State mapping addresses every column at its **root** path (`subject.data.<column>`), so an
  aspect-shaped goal would otherwise never see the row's own value under the right key — silently
  mis-resolving an already-met goal as unmet and synthesizing a spurious plan. `goalColumns` maps the
  affected column names to the aspect-qualified path they actually represent (install-validated: must
  parse under §10.5, must be aspect-qualified — a root-shaped entry is rejected as redundant — values
  must be unique, and every path must be referenced by the same gap's `goal`); a column absent from the
  map is unaffected, keeping `subject.data.<column>`. The mirror-image mistake is rejected too: a
  `candidates[].pre` may only address a **root** path — `pre` has no analogous bridge, so an
  aspect-shaped `pre` would be permanently unsatisfiable. No new Weaver Core-KV read either way — same
  §10.2↔§10.8 column seam `candidates`' `pre` already rides. **Execution is per-leg (revises the
  ratified compile-to-pattern clause; ratified Andrew 2026-07-05):** each episode dispatches
  **`plan.Steps[0]`'s declared action binding** (`triggerLoom` / `assignTask` / `directOp`) through the
  ordinary actuator path, and the mark pins that leg (plus the plan hash, diagnostic-only); **the pin
  releases once the leg's declared `effects` all hold in the current row** (a pure row predicate,
  evaluated through the gap's `goalColumns` bridge at the existing single-mark-read seams), so a reclaim
  re-dispatches the pinned leg while incomplete and re-plans **only past a completed leg** —
  level-triggered advance, the graph is the program counter; a mid-chain regression (e.g. a freshness
  lapse) re-enters the plan at the regressed leg. **Pin-release is the pinned leg's `__effect`
  close-credit and resets the gap's dispatch count** (per-leg budget semantics; the level-reconciled
  gap-close credits the final leg) — without these couplings, healthy chains would read as permanent
  lens/effect mismatches and waiting human legs would burn the chain budget on reclaim cadence.
  Rationale for the revision: a compiled pattern cannot express a **multi-actor** chain (§10.5 pins a
  userTask's `assignedTo`/`scopedTo` to the one instance subject — the frozen step shape carries no
  assignee) and would run a second program counter beside the level machinery. The struck
  compile-to-a-linear-`meta.loomPattern` (**`plan-<hash(canonical plan JSON)>`**) → `triggerLoom` shape
  is **RESERVED for op-only single-actor plans** (systemOp legs at machine latency, where per-leg sweep
  hops would matter); it is not built until such a consumer exists. Dispatch-time re-validation mirrors
  `proposedOp` **per leg** (action vocabulary · live-registry resolution · Weaver-authority).
- **The mark pins the choice per leg (revises the ratified episode-lifetime wording; ratified Andrew
  2026-07-05):** the §10.3 mark's `action` carries the chosen actionRef (+ plan hash) at
  CAS-create, and a sweep reclaim re-dispatches the **pinned** leg verbatim — no re-rank, no re-plan —
  until the leg's declared `effects` hold in the current row, at which point the mark closes and the
  next episode re-synthesizes from the advanced state. For single-step selection (`candidates`) this
  degenerates to exactly the prior episode-lifetime pin (one leg = one gap-close). Replanning thus
  happens only at **leg boundaries** (effects-hold) and **gap boundaries** (close→reopen), both minting
  a fresh mark ⇒ fresh `claimId`; the deterministic-requestId / reclaim-collapse machinery is unchanged
  within a leg, and stats feed new episodes only.
- **`actions`** (required alongside `goal`; install rejects a `goal` gap with an empty catalog) is the
  gap's planning catalog: each entry couples a **dispatch binding** (exactly one frozen action's fields —
  same shapes + validation as a static gap action, `row.<column>` templating included) with the
  planner-facing triple `pre?` / `effects` / `cost?` (`cost` defaults to 1; `ref`s unique per gap).
  `effects` are concrete assertions (`present`/`absent`/`equals`, or an `allOf` of those — `anyOf`/`not`
  rejected at install: they cannot become a definite fact). **`pre` and `effects` paths must be
  row-reachable** — a root column the lens projects, or an aspect path this gap's `goalColumns` maps
  (an unreachable `effects` path would make its leg permanently un-releasable; unlike `candidates[].pre`,
  an `actions[].pre` MAY address a `goalColumns`-bridged aspect path, because a goal gap's State carries
  the bridge).
- **Escalation:** "no plan derivable" flows into the existing `augur.escalate` **`unplannable`** trigger
  (its meaning extends to "no playbook entry AND no derivable plan"); no new trigger token. Budget
  exhaustion on a planned gap raises a standing Health issue at the suppression site (never a silent
  park).
- **Goal-first authoring (doctrine rider — ratified Andrew 2026-07-05).** The dependency-gating
  doctrine ("a dependent gap simply isn't `true` until its prerequisite closes") remains the norm for
  fixed, singly-dispatched procedures. When a convergence procedure is a **genuine chain — ≥2 legs, or
  per-entity variability** (legs that apply to some rows and not others) — the lens author MAY instead
  declare **one gap** carrying `goal` + `actions` and let synthesis derive each row's chain, rather than
  pre-decomposing into N gated `missing_*` columns. Authoring rules: goal atoms address **row facts** —
  a column projecting a real aspect field bridges to its effect-visible path via `goalColumns`; a
  **walk-computed** column (a fact the lens derives across links, e.g. an only-if-fresh validity) stays
  root-named and its closing action declares the **same root path** in `effects` (the two classes meet
  in planner State-space by construction). Conditional legs live in the **goal** (`anyOf` with a data
  disjunct), optionally mirrored by the action's `pre`. **Terminal-leg rule:** an action whose op
  closes the gap's anchor (flips the completion fact) MUST declare a `pre` entailing the **remainder of
  the goal**, mirrored in that op's own write guard — otherwise op-defined completion can outrun
  goal-defined completion under canonical tie-breaking and silently skip legs. Write paths always carry
  their own guards (an op MUST NOT rely on the planner for write-safety). A single-step gap stays a
  frozen-table `action` — goal-authoring one step is ceremony, not doctrine.

---

