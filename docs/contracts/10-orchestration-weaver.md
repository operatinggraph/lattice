# Contract #10 (Weaver) — target Lens output, target + playbook, planner

> **A shard of [Contract #10 — Orchestration Surfaces](10-orchestration-surfaces.md)** — §10.2 /
> §10.8 keep their canonical numbers; the detection↔remediation binding lives here in full. The
> **Augur** tier is its own part, [10-orchestration-augur.md](10-orchestration-augur.md).

## 10.2 Weaver target Lens output (D4) — **FROZEN 2026-06-02** (amended 2026-06-18, 13.1)

One row **per candidate entity**, carrying a `violating` flag — **not** row-only-when-violating
(avoids Refractor retraction). Projected by the existing `nats_kv` adapter.

**Bucket — one shared, primordial, dash-named bucket.** All convergence targets project into the
single `weaver-targets` bucket under a disjoint `<targetId>.` key prefix — the **same
contract-contribution pattern as capability-kv** (§6.1): core-owned/primordial bucket, packages
project their target rows into it. Unlike capability-kv's core-fixed prefixes, `<targetId>` is
package-authored, so **`targetId` uniqueness across installed targets is install-validated** (§10.8).

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
`<targetId>.<entityId>` (bare NanoID) and Weaver's `splitRowKey` accepts it unchanged.

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
- **dispatch-suppression companions `inflight_<g>` / `maxretries_<g>`** (optional, engine-recognized,
  per-gap) — a Lens may project, per gap `g`, an `inflight_<g>` bool (a remediation is already in flight
  → suppress re-dispatch) and/or an integer `maxretries_<g>` cap (the retry budget the §10.3
  dispatch-count is bounded by). Both are read to alter dispatch, **not** gaps — they never carry the
  `missing_` prefix, so the gap scan ignores them; an absent/non-bool `inflight_<g>` and an
  absent/non-positive `maxretries_<g>` both read to the **safe (dispatchable)** side, so a missing or
  garbled input never silently wedges a real gap. One bounded exception: a **`directOp`** gap whose row
  declares **neither** companion falls back to the engine's default retry budget (3 dispatches), then
  raises the §10.8 `GapBudgetExhausted` standing issue — a loud stop, never a silent park (reclaim is
  otherwise unpaced for `directOp`, so an undeclared budget would re-fire a rejected op every lease
  expiry forever). A declared `maxretries_<g>`, however small, always wins; a gap declaring
  `inflight_<g>` alone keeps its own §10.3 pacing; other actions are untouched.
  See §10.8 (dispatch suppression) and §10.3 (`__count`).
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
- **`priority`** (optional, engine-recognized convention) — an integer, higher = more urgent. Consulted **only** when the row's
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
  dispatches are broken by `priority` — higher first, absent/non-numeric = 0. A restart resets accrued
  tokens (process-local pacing, never a correctness concern). A free-form param column by storage,
  named by convention like `freshUntil`; every target without an `admission` block ignores it entirely.

**No read-path authz anchor in the bucket.** `weaver-targets` rows are unscoped NATS-KV: an app
reading them as an ordinary P5 lens read-model scopes them in its own query, and read-path auth (D1)
is enforced where a target Lens is *also* projected to the protected Postgres read-path. A
*remediation*'s scoping rides the param columns, and each remediation op the Actuator submits carries
its own `authContext`.

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
  "augur": { "escalate": ["unplannable", "exhausted"], … }
             // the Augur escalation block — canonical shape + validation:
             // 10-orchestration-augur.md (minimal block = just `escalate`)
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
| `directOp` | `{ operation, target?, params?, reads?, optionalReads? }` | **📐 PROPOSED — UNRATIFIED (2026-08-29): `optionalReads?` is new.** It is the dispatched op's `contextHint.optionalReads` (Contract #2 §2.5's absence-tolerant half) — same template grammar as `reads?`, for a key whose absence is a legitimate branch in the script rather than a correctness error. Additive + `omitempty`: a `directOp` that omits it dispatches exactly as before. **`directOp` only** — every other action's `optionalReads` is the engine's own to set (`assignTask` derives its stable task dedup key), so a package-declared value on another action is a second writer to one field and is refused at install and at load. A `row.<column>` entry that resolves null/absent **drops that entry** rather than failing the gap: the rows where such a column is null are precisely the rows an absence-tolerant declaration was written for. submit `operation` directly as a remediation op. `reads?` is the dispatched op's `contextHint.reads` — bare vertex keys, each a literal or `row.<column>` — so an op that must hydrate its candidate vertex (e.g. `TombstoneObject` reading the object's `linkEpoch`) gets the key straight from the lens row. Additive + `omitempty`: a `directOp` that omits it dispatches read-free exactly as before. A clause-billing target is a canonical consumer: `operation` is the literal `DebitAccount`, `target`/`params`/`reads` row-templated (the amount as a numeric param column; clause + account keys routed into `reads` for hydration). |
| `proposedOp` | *(none — sourced from the row)* | **Additive, opt-in (Augur dispatch, Fire 2b).** Dispatch the **row-carried** `proposedAction` + `proposedParams` (materialised into a `GapAction`) after a **dispatch-time deterministic re-validation** (action ∈ the escalation catalog `{triggerLoom, assignTask, directOp}` · live-registry resolution via the existing `buildPlan` · **default-deny scope** to the row's TRUSTED candidate `candidateKey` · op ∈ Weaver's service-actor authority). Unlike the three static actions, the op + params are *data per row*, not playbook config; the proposed op carries a **proposal-scoped deterministic requestId** so a sweep re-dispatch collapses on the Contract #4 tracker (at-most-once). Used **only** by the `augur` package's primordial `augurDispatch` convergence target (see "Augur dispatch" below); wiring `proposedOp` to a row whose source is not a §5-validated approved proposal is a package bug. The `directOp`-must-be-literal guard stays intact for ordinary playbooks — `proposedOp` is the gated sibling for the one §5-validated dynamic-op surface. |
| `surface` | `{ issueCode, issueSeverity? }` | **Additive (FR28/FR29 Fire 3).** Dispatch **nothing** — no op, no mark, no OCC, no episode. While the gap column stays true, raises a Contract #5 §5.5 `issues[]` entry keyed `issueCode` at `issueSeverity` (default `warning`); the issue clears via the ordinary level-reconciled mark-clearing pass once the row stops naming the column. `issueCode` is required; `issueSeverity` ∈ `{warning, error}`. Manual-intervention-only — the sibling of `triggerLoom`/`assignTask`/`directOp`/`proposedOp` for a gap the playbook author wants surfaced, never remediated. Used by `orchestration-base`'s primordial `unroutedTasks` target (`missing_claim` → `{action:"surface", issueCode:"UnroutedTasks"}` — an open role-queued task left unclaimed past its own `expiresAt`). |

The former `nudge` action is **retired** — external I/O lives in Loom + the bridge; external
remediation is `triggerLoom` of a pattern containing an `externalTask` (§10.5/§10.6).


### Augur escalation & dispatch → [10-orchestration-augur.md](10-orchestration-augur.md)

The Augur AI-reasoning tier (escalation on `unplannable` / `exhausted` → `vtx.augurProposal` → human
review → `proposedOp` dispatch) is specified in its own part,
**[10-orchestration-augur.md](10-orchestration-augur.md)**. The `augur` block shape (in the target JSON
above) and the `proposedOp` action row (in Action contracts above) are its Weaver-side hooks.

### Templating

> **📐 PROPOSED — UNRATIFIED (2026-08-29).** This section and the `directOp` action-table row above are
> amended below to match what the engine now implements. Ratify by merging; reject by dropping this
> branch. Precedent: the `reads?` field on that same row was itself a ratified §10.8 amendment
> (`docs/decisions/contract-10-revision-history.md`, 2026-06-19).

A param value takes one of **three arms**, resolved in this order — no expressions:

1. **`row.<column>`** (`subject: "row.applicant"`) — substituted with that column's value from the
   violation row. A substituted value is **never re-scanned** for a token, so a column literally
   holding `json:5` dispatches as that string.
2. **`json:<literal>`** (`limit: "json:5"`, `active: "json:true"`) — decoded into the JSON value its
   suffix encodes, so a params map typed `map[string]string` on the wire can still carry a number, a
   bool or a structured value. A value that must itself begin with the token is written as its own
   JSON string: `json:"json:foo"` resolves to `json:foo`.
3. Otherwise a **plain string literal** (`pattern: "onboarding"`), passed through byte-for-byte.

Arm 2 is admitted in the **`params` bag only**. Every other authored value — `subject`, `pattern`,
`operation`, `assignee`, `target`, and each `reads` / `optionalReads` / `enumerations[].hub` entry —
is a key, an operationType or a pattern ref, always a string, and **refuses the token**. That split is
a trust boundary, not a convenience: admission gates upstream of dispatch (the authored-capability
dispatch-scope check, the Augur proposal scope check) compare those fields as **raw authored
strings**, so a field that decoded at dispatch would be one the gate never saw.

A `json:` suffix that does not decode, the literal `null`, an empty string, and an integer whose
decimal spelling `float64` cannot hold exactly are **config errors**: the defect is authored and
row-independent, so it is refused **at install and at engine load**, not merely at dispatch.

The Strategist substitutes `row.<column>` with that
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

### Flow & anti-storm

Lane-1 sees a `violating` row → for **every** currently-true `missing_*` gap **not already
in-flight**, the Strategist looks up `gaps[col]` and the Actuator executes:

- **In-flight mark** in `weaver-state`, keyed **`<targetId>.<entityId>.<gapColumn>`** (entity *ID*,
  not the dotted full key — §10.2). Set via **KV create (CAS-on-absent)** — *that* create **is** the
  anti-storm OCC: concurrent evaluations race the create, the loser drops, the winner dispatches.
  Value shape, lease/TTL, level-reconciled clearing, episode tagging, and per-action re-fire
  idempotency are **§10.3's** (`weaver-state` mark + reclaim), stated once there.
- **Mark clears** on **gap-close**, **planned-leg completion** (the pinned leg's declared `effects`
  all hold in the current row), or **lease expiry** — all **level-reconciled, never edge-triggered**
  (§10.3). Async remediations close their gap when their downstream work lands and the Lens
  re-projects `false`.
- **Gaps fire in parallel** — independent remediations run concurrently.
- **Gap *dependencies* are encoded in the target Lens predicates, not in Weaver.** If bgcheck needs
  onboarding first, the Lens makes `missing_bgcheck` true only once onboarding is done
  (`missing_bgcheck = onboarded AND NOT EXISTS(recent check)`). A dependent gap simply isn't `true`
  until its prerequisite closes, so parallel firing is always safe. Weaver stays a generic parallel
  dispatcher; ordering is declarative.
- **Liveness invariant (the engine's operating law).** No violating row may sit indefinitely with
  nothing owed on it: every `violating` row is eventually **discharged** (its Lens re-projects it
  clean), **excluded** (its target disabled/revoked — operator verb or the oscillation freeze — or
  the row superseded/deleted), or **escalated** (budget exhaustion → `surface`/Augur: a human now
  owns it). The reconciler sweep, per-gap retry budgets, contraction trajectory, oscillation
  detector, and admission fairness are jointly the *enforcement* of this one invariant, not
  independent features. A target shape under which a gap can stay open forever without escalating is
  a target-authoring bug, not an engine tolerance.

Target + playbook are **package data**; the Weaver engine is a generic dispatcher.

### Planner extension — selection & synthesis (Ratified 2026-07-04 — build-pending)

> **Ratified 2026-07-04 (Andrew).** **Everything in this subsection is additive and opt-in**: a
> target carrying none of the new fields behaves **byte-identically** to the frozen shapes above.
> Nothing here changes the action table, templating, anti-storm, or the augur block; external I/O
> stays Loom + bridge (Weaver never holds an adapter). Full design + build record:
> `_bmad-output/implementation-artifacts/weaver-planner-mandate-design.md`.

**Op-DDL `effects` (additive).** An op DDL MAY declare `effects: [<guard>…]` — §10.5 guard-grammar
predicates (atoms + combinators, the two subject-path shapes, pinned absence semantics; the Starlark
escape hatch stays RESERVED) that the op's commit entails on its target subject. Install-time validation
rejects wholesale on a malformed guard (same doctrine as pattern load).

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
- **Synthesis (`goal`) is bounded goal regression** over the gap's **declared `actions` catalog** — a
  closed, package-authored set (a global ops-derived auto-catalog is **reserved**: an op effect alone
  carries no dispatch binding) — a pure function of (row, catalog) with canonical tie-breaking
  (candidate *selection* additionally reads the `__effect` close-rate window §10.3; goal *synthesis*
  does not). **`goalColumns`** (per-gap, optional; never shared across gaps) bridges a goal that
  addresses an **aspect** path: a §10.2 row flattens an aspect-projected column onto a bare root name,
  so an aspect-shaped goal would mis-resolve an already-met goal as unmet without the map.
  Install-validated: each entry must parse under §10.5, must be aspect-qualified (a root-shaped entry
  is rejected as redundant), values unique, and every path referenced by the same gap's `goal`; a
  column absent from the map keeps `subject.data.<column>`. The mirror-image mistake is rejected too:
  a `candidates[].pre` may only address a **root** path (`pre` has no analogous bridge). No new Weaver
  Core-KV read either way. **Execution is per-leg:** each episode dispatches **`plan.Steps[0]`'s
  declared action binding** through the ordinary actuator path, and the mark pins that leg; **the pin
  releases once the leg's declared `effects` all hold in the current row** (a pure row predicate,
  through the `goalColumns` bridge), so a reclaim re-dispatches the pinned leg while incomplete and
  re-plans **only past a completed leg** — level-triggered advance, the graph is the program counter;
  a mid-chain regression re-enters the plan at the regressed leg. **Pin-release is the pinned leg's
  `__effect` close-credit and resets the gap's dispatch count** (per-leg budget semantics; the
  level-reconciled gap-close credits the final leg). The compile-to-a-linear-pattern
  (**`plan-<hash>`**) → `triggerLoom` shape is **RESERVED for op-only single-actor plans**; it is not
  built until such a consumer exists. Dispatch-time re-validation mirrors `proposedOp` **per leg**
  (action vocabulary · live-registry resolution · Weaver-authority).
- **The mark pins the choice per leg:** the §10.3 mark's `action` carries the chosen actionRef at
  CAS-create, and a sweep reclaim re-dispatches the **pinned** leg verbatim — no re-rank, no re-plan —
  until the leg's declared `effects` hold, at which point the mark closes and the next episode
  re-synthesizes from the advanced state. For single-step selection (`candidates`) this degenerates to
  the episode-lifetime pin (one leg = one gap-close). Replanning happens only at **leg boundaries**
  (effects-hold) and **gap boundaries** (close→reopen), both minting a fresh mark ⇒ fresh `claimId`;
  the deterministic-requestId / reclaim-collapse machinery is unchanged within a leg.
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
- **Diagnostics + engine-autonomous freeze.** Weaver keeps in-memory, heartbeat-surfaced diagnostics
  (per-target contraction trajectory; an oscillation detector joining dispatched `actionRef`s to the
  aspect paths their `effects` assert — inventory: the Health schema doc). On a **confirmed two-target
  fight** over one contested path, the detector **freezes both targets** via the §10.3 `__control`
  disable seam and raises one `TargetOscillation` issue naming the causal pair — a freeze-and-alert
  safety stop, **never a new dispatch**, and the one place the engine disables a target autonomously.
- **Goal-first authoring (binding rules).** Dependency-gating ("a dependent gap simply isn't `true`
  until its prerequisite closes") remains the norm for fixed, singly-dispatched procedures. For a
  **genuine chain** — ≥2 legs, or per-entity variability — the lens author MAY instead declare **one
  gap** carrying `goal` + `actions` and let synthesis derive each row's chain. Rules: goal atoms
  address **row facts** — an aspect-projecting column bridges via `goalColumns`; a **walk-computed**
  column stays root-named and its closing action declares the **same root path** in `effects`.
  Conditional legs live in the **goal** (`anyOf` with a data disjunct), optionally mirrored by the
  action's `pre`. **Terminal-leg rule (MUST):** an action whose op closes the gap's anchor MUST
  declare a `pre` entailing the **remainder of the goal**, mirrored in that op's own write guard —
  otherwise op-defined completion can outrun goal-defined completion and silently skip legs. **An op
  MUST NOT rely on the planner for write-safety** — write paths always carry their own guards. A
  single-step gap stays a frozen-table `action`.

---

