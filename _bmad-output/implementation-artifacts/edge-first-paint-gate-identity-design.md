# Edge first-paint gate — a level-triggered position gate, not a correlated marker gate

**Status: ✅ RATIFIED (Winston-adjudicated, per the 2026-08-20 delegation) — build-ready for the Lattice Steward.**
**No architectural fork. No frozen-contract change** (§8). One deliberate divergence from the filed
direction, argued in §4: the row's `no-pattern: per-hydrate correlation-id` names the primitive the *refuted
marker-shaped* gate would need; this design gates on the **delivery position** instead, and the correlation
id dissolves.

Backlog row: *[Edge] The first-paint gate has no identity for the hydrate cycle it gates* (★★ M — two
defects: no per-lens identity ⇒ partial-world release; per-identity subject ⇒ a second device's burst
satisfies this device's gate). Parent: `edge-cold-signin-delivery-position-design.md` §6 Fire 4 +
its 2026-08-11 amendment (the client-side membership shape was **built and refuted**, four blocking
defects; evidence branch `fire/edge-cold-signin-position` @ `fab96db4`).

---

## 1. Problem

`personalHydrate` (control/service.go:1267) fans out to every registered Personal Lens hydrator
**sequentially and synchronously**; each `pipeline.Hydrate` publishes its rows, a keyset frame, then its own
`hydrationComplete` marker (hydrate.go). The Edge gate is a scalar: `armHydrateGate(revision)` records the
response's max revision, and `hydrationGateReady` (sync.go:619) releases on the **first** marker carrying
`revision >= target` — so whichever lens holds the max can release the gate while other lenses are still
bursting (partial-world first paint, observable since Fire 2 removed the replay that hid it), and any
marker on the per-**identity** subject — including a **second device's** cycle — satisfies it.

The refuted Fire-4 build proved the *shape* of the fix is the hard part. Its four defects, restated as
requirements (the amendment's own words):

- **R1 (liveness):** an absolute bound on first paint that does not presuppose the release condition.
- **R2 (a real bound):** the bound must not be resettable by unrelated traffic on the shared subject.
- **R3 (ordering):** the gate must be effectively armed before the burst can be consumed — the `Rehydrate`
  path consumes the burst *before* the RPC response arrives.
- **R4 (position-absence):** correctness must not be poisoned when `SyncStartSeq` is 0 (a live fail-soft
  path — nil seam or a `STREAM.INFO` error).
- Plus the row's two original defects: **R5** full-world release (every lens's frames landed), **R6**
  another device's cycle must not release this device's gate.

## 2. Grounded mechanism facts (each one load-bearing, cited to code)

1. `substrate.Conn.Publish` **waits for the JetStream store ack** (publish.go:62) — so when a hydrator
   returns, every message it published is *appended to the stream*, and when `personalHydrate`'s loop
   finishes, the **entire burst of every lens is appended**. A per-actor last-sequence read taken *after*
   the loop is ≥ every burst message's sequence.
2. `syncLastSeq` (service.go:343) reads the requesting identity's **own subject's** last sequence — the
   seam Fires 1–3 shipped; it is already read once *before* the loop (`SyncStartSeq`).
3. The client sees every delivered delta's **stream sequence** (`transport.Delta.Sequence`) and already
   maintains a **contiguous resolved floor** over them (`deliveryFloor`, sync.go): the floor advances only
   past sequences that are applied-or-terminal, holds below any Nak'd sequence, and **jumps sequences that
   were never delivered** (`release` clamps only below *held* sequences) — an evicted message cannot wedge
   it.
4. The floor is the same value the persisted cursor is built from — the position plane the parent design
   ratified as "the **single resume authority**" (§3.4).
5. Both hosts (Go host `cmd/facet/engine.go:197`, browser host `internal/edge/browser/host.go:187`) consume
   one shared `edge/sync.Manager`; a fix in the Manager is a fix for both. The EDGE.5 browser-node/wasm
   work varies the `Transport` implementation only — no seam overlap.

## 3. The shape — release on the floor reaching the burst's end position

### 3.1 Producer (control plane): one field

`personalHydrate` reads `lastSeqFn` a **second time, after the fan-out loop**, and returns it as
`PersonalHydrateResult.SyncEndSeq`. By fact 1 it bounds the whole cycle: every message of every lens's
burst — rows, keyset frames, markers — has a sequence ≤ `SyncEndSeq`. Fail-soft like its sibling: a nil
seam or read error yields 0 with a Warn, and the client falls back (§3.4). No pipeline, adapter, envelope,
or marker change — the wire deltas are untouched.

(`SyncEndSeq` may exceed the burst's true last sequence when unrelated traffic on the same subject races
the read — another device's burst, a live delta. That only moves release *later* by that traffic's
delivery, never earlier: safe direction, bounded by the subject's own volume.)

### 3.2 Client (Manager): the gate becomes a level, not an edge

`armHydrateGate` is replaced by a per-cycle gate `{generation, endSeq, revision, deadline}` armed by
`hydrate()` from the RPC response. Release condition: **the delivery floor ≥ `endSeq`** — checked (a) at
arming, against the current floor, and (b) on every floor advance in `handle()` (after the cursor
persist, so paint follows *applied* state, not merely delivered). On release: disarm, cancel the deadline
timer, fire `OnHydrationComplete(revision)` once — `revision` is the response's cycle high-water, the same
value the releasing marker carries today in the happy path.

Two arming rules the adversarial pass forced (§10 findings 1 and 3):

- **The arm-time release is legal only on the `Rehydrate` path.** There the consumer is live and the
  floor is same-session, same-numbering-space state — floor ≥ endSeq genuinely means "burst already
  applied". On the `ensureFresh`-triggered paths (cold boot, gap, operator-requested hydration) the floor
  at arm time derives from the **persisted cursor of a previous session**, and `floor ≤ syncStartSeq ≤
  endSeq` provably holds absent a stream reset — so an arm-time floor at or above `endSeq` there is not a
  release, it is **evidence the sequence spaces diverged** (stream recreation, DR restore, world wipe —
  reachable: `personalSyncGap` deliberately does no cursor validation, so a numerically-huge stale cursor
  reads as not-gapped, and the operator-requested `hydrationRequested` bit independently forces a hydrate).
  The cold-path arm therefore never releases immediately: it logs Error and degrades to §3.4's fallback
  family instead of painting an empty store. The old revision-space gate had no such failure mode, so the
  new position-space gate must refuse the comparison exactly where its precondition (monotone growth) is
  broken.

  **Amended at build, 2026-08-22 — two corrections to this bullet, both proven by test.** *(a)* The
  comparison is **`cursor >= endSeq`, not `>`**: the floor is seeded at the cursor and only ever released
  past what is already persisted, so a gate whose end position is *exactly* the cursor cannot be satisfied
  by any message of its own burst — every one sits at or below it — and left in position mode it would wait
  out a whole deadline window for nothing. Equality degrades for a different reason than divergence, with
  the same answer. *(b)* The check **cannot run at arm time at all**: `Run` calls `ensureFresh` (which arms)
  *before* it seeds `m.floor.reset(stored)`, so at cold-arm time the floor still holds the previous attach's
  state — on a first `Run` that is 0, and on a host that re-`Run`s the same Manager it is the previous
  attach's cursor. It runs in `Run`, immediately after the floor is seeded and before the consumer starts,
  which is the only moment the two numbers are comparable undisturbed, and still strictly before any
  delivery can be consumed (R3 intact).
- **Ready-evaluation, disarm, and the fire decision are one generation-checked critical section** under
  the existing `gateMu` (the callback itself runs outside the lock). Release can fire from three
  goroutines (floor advance, arming, timer); without the generation re-check inside the same section that
  observed `floor ≥ endSeq`, a replacement racing a floor advance could fire the *superseded* cycle's
  callback while the new cycle's burst is still incomplete.

Why each requirement falls out structurally:

- **R3 (ordering/race):** a *level* cannot be consumed-before-armed. On the mid-session `Rehydrate` path
  the burst is often fully applied before the response returns; the arm-time check then releases
  immediately. The refuted shape raced because markers are *edges* — events consumed once, gone if the
  gate wasn't listening. The floor is monotone state, queryable at any time.
- **R5 (full world):** the floor is *contiguous*: reaching `endSeq` requires every sequence ≤ `endSeq` —
  every lens's frames and markers — resolved (applied, terminal, or never-delivered-evicted). No
  membership set, so nothing to poison.
- **R6 (second device):** foreign messages interleaved *below* `endSeq` only add to what must be resolved
  first; foreign traffic *above* `endSeq` is irrelevant to the comparison. A foreign cycle can delay
  release marginally, never cause it.
- **R4 (position absence):** an absent position is explicit (`SyncEndSeq == 0`) and selects the fallback
  (§3.4); there is no membership state for stale attributions to drain.
- Eviction (`MaxMsgsPerSubject: 10_000`) cannot wedge the gate: never-delivered sequences are never held,
  so the floor jumps them on the next resolved delivery (fact 3).

`hydrationComplete` markers stop driving `OnHydrationComplete` **while a gate is armed** — they are
ordinary floor-advancing deltas (their arrival at the tail of each lens's segment is precisely what walks
the floor to `endSeq`). With **no** gate armed (steady-state tail), the current behaviour — any marker
fires the callback — is kept unchanged: it is how a foreign device's completed cycle re-marks an
already-painted host (both consumers are idempotent re-marks, cmd/facet/feed.go:236,
browser/feed.go:214), and narrowing it is not this row's defect. Stated so the builder does not "fix" it
in passing.

### 3.3 The deadline — a stall detector, not a total bound (R1/R2)

Armed **unconditionally at gate arming** — not conditioned on any marker or delivery
(`Config.HydrateGateDeadline`, default 30s; the burst is fully *appended* before the response, so
post-response delivery of a bounded backlog is the only thing being waited on). On expiry the timer checks
**whether the floor advanced since it was (re)armed**: if yes, re-arm and keep waiting — a slowly-applying
burst (a large world; a **browser tab whose timers and IndexedDB writes are background-throttled**, a case
the adversarial pass surfaced as capable of exceeding any fixed total bound) is making progress, not
stalled; if no, log Warn with floor-vs-endSeq, disarm, fire `OnHydrationComplete(revision)`.

So the bound is **30s of zero progress**, not 30s total. R2 holds: unrelated traffic cannot extend the
gate without also advancing the floor — foreign deliveries below `endSeq` are burst-blocking messages
that legitimately count as progress, and foreign deliveries above `endSeq` release the gate outright. The
refuted shape's idle timer failed the other way around: it *reset on* traffic while measuring idleness of
the wrong thing.

**Amended at build, 2026-08-22 — the paragraph above is a POSITION-MODE argument, and applying it to the
fallback mode is exactly the refuted shape's bug.** A fallback gate (§3.4) releases on a *marker*, so a
delivered delta is no evidence at all that its release is approaching; and because the SYNC subject is
per-**actor**, ordinary live traffic — this device's own writes, a second device's — would re-arm the
window indefinitely and the fallback gate would **never open**, while also swallowing every later marker
below its target for the life of the process. Both halves are R1 and R2 refuted on precisely the path R4's
fail-soft selects. So: **only a position-mode gate records progress. For a fallback gate the deadline is a
total bound of one window** — which is what makes the degraded mode *bounded* rather than merely different,
as §3.4 claims.

Second correction, same date: **progress is RESOLUTION, not the persisted floor moving.** `deliveryFloor`
reports no advance whenever the computed floor sits at or below what is already persisted — so a hydrate
whose *start* read fail-softed to 0 while its *end* read succeeded delivers the whole retained subject
below an already-higher cursor, advancing the floor by nothing for as long as that takes, and the window
would expire mid-burst and paint on a world that had not been delivered at all. The gate's progress signal
is therefore "a sequence this attach had not passed before resolved" (newly delivered, or newly un-held),
which is strictly stronger than "the floor moved" and still untriggerable by a duplicate. Release continues
to follow the persisted floor: **release follows applied state, progress follows resolution.**

Fail direction on a genuine stall is paint-possibly-partial — today's shipped first-marker behaviour was
also unbounded-wrong in that direction, but the pass is right that a zero-delivery release is a **new**
outcome (today's gate has no timer and fires only on a marker): accepted, rare (requires an armed gate and
a full window with no floor movement at all), and loud. Cancelled on release, shutdown, and replacement.

### 3.4 Fallback and replacement rules

- **`SyncEndSeq == 0`** (older control plane, nil seam, read error): arm today's scalar-revision gate
  (first marker ≥ revision releases) **plus the deadline**. Explicitly the degraded mode: never worse than
  shipped behaviour, and bounded where it wasn't.
- **A newer cycle replaces an armed gate** (reconnect→gap→hydrate, or `Rehydrate` racing a boot): arming
  cancels the prior gate's timer and state; only the latest cycle releases. One armed gate per Manager,
  under the existing `gateMu`.
- **A failed hydrate RPC** arms nothing (some lenses may already have published markers — with no gate
  armed they fire the steady-state callback, which is the shipped behaviour for exactly that partial
  situation today).

### 3.5 State lifetime (the gate is the only new state)

| boundary | gate `{endSeq, revision, deadline timer}` |
|---|---|
| created | at `hydrate()`'s response (cold boot, gap, operator-requested, `Rehydrate`), stamped with a new generation |
| released | floor ≥ endSeq (at arm — `Rehydrate` path only — or on advance), generation-checked under `gateMu` → disarm + cancel timer + fire once |
| deadline | timer expiry with zero floor progress since (re)arm → disarm + fire once, Warn; progress re-arms |
| replaced | a newer cycle's arming cancels it silently |
| reconnect without re-hydrate, **or a `Run` that returns on a transient failure** | gate persists — the floor is monotone across attaches (cursor-backed), endSeq stays valid |
| crash | in-memory, gone; next boot re-hydrates (new gate) or warm-resumes (no gate — host paints from the local store, existing behaviour) |
| shutdown | timer cancelled with the Manager's ctx |

**Amended at build, 2026-08-22.** The "shutdown" row is keyed on **ctx cancellation specifically**, and the
distinction is load-bearing rather than pedantic: disarming on *every* `Run` return deletes the deadline —
first paint's only liveness backstop — on exactly the transient consumer failure where a host most needs the
gate to open on what arrived. The browser host gets **one `Run` per page and no restart loop**, so that
would hang its first paint for the life of the page; `cmd/facet`'s restart loop loses it differently, because
the retry warm-resumes and arms no new gate at all. A host that does restart and re-hydrates supersedes any
surviving gate by generation, so nothing accumulates. Hence the transient-failure return shares the
reconnect row above.

Also corrected here: the callback-threading sentence below overstates the guarantee. **The release
*decision* is serialized by the generation-checked critical section; the callback *delivery* is not** — two
goroutines that decided in order can invoke the host in the opposite order. That is sound only because both
hosts' callbacks are mutex-guarded idempotent re-marks that derive nothing from the value, which the
adversarial pass verified rather than assumed.

**Accepted residuals, named at build (2026-08-22), none of them changing the shape above:**

- A `Term` whose cursor write fails on the burst's *last* message leaves the gate unconsulted (the floor
  never reached the store), so release falls to the deadline.
- A `Rehydrate`-armed gate whose `Run` is not running has no owner — the `Manager` has no `Close` — so it
  holds a timer for at most one window and may fire one idempotent re-mark into a host mid-teardown.
- On the **fallback** path R3 is not structural: `Rehydrate`'s marker can be consumed before the response
  arms the gate, and there is no arm-time equivalent of the floor check to recover it, so that cycle
  releases one deadline window late. Shipped behaviour in the same situation was to hang forever, so this
  is bounded where it was not — §3.4's own standard.

Callback threading: release fires from the consumer goroutine (floor advance), the arming goroutine
(`Rehydrate` arm-time release), or the timer goroutine (deadline) — serialized by §3.2's generation-checked
critical section. Both hosts' callbacks (`publishReady` in cmd/facet/feed.go, `markHydrated` in
internal/edge/browser/feed.go) are mutex-guarded idempotent re-marks — verified in the adversarial pass,
not assumed.

## 4. Why not the correlation id the row prescribes

The amendment's requirement was *"an identity for the hydrate cycle on the wire … so membership can be
scoped to the cycle without depending on a delivery position that is sometimes absent."* That sentence
carries an assumption: that the gate is built from **markers** (edges), which then need scoping to a cycle
(the id) and a membership set (which lenses reported). Grounding the alternative showed the assumption is
the source of most of the machinery:

- A client-minted id + per-lens markers + membership-joined-at-response *does* satisfy R1–R6 — it was
  designed through before being rejected — but it needs: a new wire field on every marker, the
  `HydrationMarkerPublisher` interface widened, a pre-registration step so markers consumed before the
  response aren't lost (R3), a bounded seen-set keyed by registered ids, membership echo for adapters that
  publish no marker, and id lifetime rules across failed RPCs. Every piece is the same multi-clock,
  edge-triggered state the refuted build fumbled — done right this time, but *right* at the cost of a
  second identity plane existing only for this gate.
- The position gate needs **one response field and a timer**, rides the plane the parent design already
  named the single resume authority, and satisfies R3/R5/R6 *structurally* (monotone level, contiguity)
  rather than by carefully-ordered bookkeeping.
- The "position is sometimes absent" objection is answered on its own terms: absence is explicit,
  per-cycle, and selects a bounded fallback (§3.4) — whereas the id-shaped design still needs the same
  deadline backstop for evicted/unpublished markers anyway. Both designs end at "deadline covers the
  residual"; only one carries an identity plane to get there.

Run the rejected option's own objection back against the recommendation: *can the position gate be wrong
where the id gate would be right?* Only if the floor could reach `endSeq` without the burst being applied
— which contiguity + fact 1 exclude — or if `endSeq` could under-run the burst — excluded by the
store-acked publishes preceding the read. The reverse question (id gate wrong where position gate right)
has the four build-evidenced defects as its answer.

## 5. Read path / write path / security

No Core KV touch, no lens/DDL change, no op change. The one new datum (`SyncEndSeq`) is, like
`SyncStartSeq`, the identity's **own subject's** sequence — never the stream-wide value, so no cross-tenant
volume leak (the same rule service.go already states for the start read). The gate is client-local paint
sequencing; nothing authorizes off it.

## 6. Reconciliation with the existing mental model

- *Didn't we already handle this?* Fires 1–3 shipped the position plane for **resume**; Fire 4 was
  amended to a designer item after its marker-shaped fix was refuted. This design is Fire 4, rebuilt on
  the plane Fires 1–3 established instead of beside it.
- *Does this duplicate a pattern?* It reuses `deliveryFloor` + the hydrate RPC's existing seam — the
  "simplest extension of machinery that already exists" answer. The consolidated second-device row is
  closed by the same mechanism (R6), not by a second one.
- *New state?* One in-memory per-cycle gate (§3.5). The scalar `hydrateTarget/hydrateArmed` pair it
  replaces is strictly contained in it (the fallback mode *is* that pair plus a timer).

## 7. Test strategy (all in `internal/edge/sync`, fake transport — each owned by Inc 1/Inc 2 as marked)

1. **Cold path release** (Inc 2): two lenses' bursts + markers; gate releases only when the floor reaches
   `endSeq`, `OnHydrationComplete` fires once with the response revision.
2. **Partial-world regression** (Inc 2) — the refuted shape's green criterion, expressed positionally: the
   higher-revision lens's marker delivered first must NOT release while earlier-sequence frames remain
   unresolved. Mutation check: flipping the `>=` comparison or dropping the contiguity (using highest
   delivered instead of the floor) must fail this test.
3. **Rehydrate arm-after-burst** (Inc 2): burst fully applied before arming → arm-time check releases
   immediately; no hang, no double-fire.
4. **Second device** (Inc 2): foreign-cycle messages interleaved below and above `endSeq`; gate releases
   only after own-burst sequences resolve; foreign marker alone never releases an armed gate.
5. **Eviction gap** (Inc 2): deliver with sequence gaps below `endSeq` (never-delivered), floor jumps,
   gate releases.
6. **Nak holds paint** (Inc 2): a Nak'd burst message holds the floor below `endSeq` → no release until
   redelivery resolves.
7. **Deadline** (Inc 2): nothing delivered → timer fires once, Warn logged, callback fires; a release
   cancels the timer (no second fire).
8. **Fallback** (Inc 2): `SyncEndSeq == 0` → scalar-gate behaviour preserved (existing tests keep
   passing against the fallback) + deadline armed.
9. **Replacement** (Inc 2): second arming cancels the first; only the newer cycle fires — including the
   race shape: a floor advance observing the old gate's condition concurrently with replacement must not
   fire the old callback (generation check).
9a. **Stale-space anomaly** (Inc 2): cold-path arm with floor > endSeq → no release, Error logged,
    deadline-only mode; the same inequality on the `Rehydrate` path releases immediately.
9b. **Stall-detector deadline** (Inc 2): floor progress across a window re-arms; a full window with zero
    progress releases with Warn; foreign above-endSeq delivery releases outright.
10. **Producer** (Inc 1): `personalHydrate` returns `SyncEndSeq` ≥ the sequence of the last published
    burst message; error/nil-seam paths yield 0 + Warn (mirror the existing start-seq tests).

## 8. Contract surface + fork check

`PersonalHydrateResult` is internal control wire (`controlwire.go`), not a frozen contract; the SYNC delta
envelope is untouched. No new component, no engine/Processor/Core-KV involvement; the enforcement point
(the Manager both hosts share) is where the shipped gate already lives. **No fork, no contract change ⇒
Winston-adjudicated**; the one filed-direction divergence (§4) is a mechanism-level choice
(implementation altitude), argued from build evidence and code, not a product/contract/architecture fork.

## 9. Decomposition for the Steward (one fire, 2 increments, S–M)

1. **Inc 1 — control plane:** the second `lastSeqFn` read + `SyncEndSeq` on the result. Owns test 10.
   Mechanical — standard depth.
2. **Inc 2 — Manager gate:** the level-triggered gate, deadline, fallback, replacement; `callHydrate`
   surfaces the new field; marker handling per §3.2. Owns tests 1–9. **Posture-changing (first-paint
   correctness both hosts) — full review depth**, and the reviewer should hold it against the refuted
   branch's four defects (R1–R4) explicitly.

Build Inc 1 first (the client change is inert against an old control plane by design — the fallback — so
the increments are independently shippable in this order).

## 10. Adversarial pass — one cold external reviewer (read-only, grounded in sync.go / service.go / hydrate.go / publish.go / consumer.go + the refuted amendment)

Findings, all folded into the body above:

- **(blocker) The arm-time release was unsound on the cold path.** The floor at cold-arm derives from a
  previous session's persisted cursor; after a SYNC-stream reset (stream recreation, DR restore, world
  wipe) a numerically-huge stale cursor reads as not-gapped (`personalSyncGap` deliberately validates
  nothing) while the operator-requested `hydrationRequested` bit still forces a hydrate — and the fresh
  `endSeq` is low-numbered, so `floor ≥ endSeq` would have painted an **empty** store instantly, a failure
  the old revision-space gate could not express. Resolved in §3.2: arm-time release is Rehydrate-only;
  a cold-path `floor > endSeq` is an anomaly → Error + deadline-only mode.
- **(material) "Deadline = today's behaviour, bounded" overstated parity** — today's gate has no timer, so
  a zero-delivery release is a new outcome, and a background-throttled browser tab can legitimately exceed
  any fixed total bound. Resolved in §3.3: the deadline is a **stall detector** (re-arms on floor
  progress; releases only after a full window of zero progress), with the residual named and accepted.
- **(material) Three-goroutine release needed a primitive, not a verification instruction** — resolved in
  §3.2: generation-stamped gate, ready-evaluation + disarm + fire-decision in one `gateMu` critical
  section.
- **(minor) A `Sequence == 0` delivery** (metadata error, `consumer.go:438` fills stream seq otherwise)
  cannot advance the floor; a burst message so delivered leaves release to the deadline — acceptable
  residual, no change.
- **(checked-OK)** `Delta.Sequence` is the **stream** sequence (`consumer.go:438`); `Publish` store-acks
  before return; `syncLastSeq` is the actor's own subject (`cmd/refractor/main.go:1680-1694`,
  `GetLastMsgForSubject`); the floor cannot be wedged by eviction; `hydrateTarget`/`hydrateArmed` have no
  readers outside sync.go; both hosts' callbacks are mutex-guarded idempotent re-marks; R5 holds
  independent of revision/publish-order correlation because `endSeq` postdates the whole loop; R2 and R3
  hold by construction.

## For Andrew (transparency, no decision required)

Winston-adjudicated: no fork, no contract edit. The one thing worth your eyes: the board row prescribed a
**per-hydrate correlation id** (the 2026-08-11 amendment's conclusion after the client-side fix was
refuted); this design concludes the id was an artifact of gating on marker *events* and ships a
**position-level gate** instead — §4 runs both directions of the argument. If you disagree with that
collapse, the id-shaped design is §4's first bullet, fully sketched and buildable.

---

## Fire brief (build note, 2026-08-22)

Compiled at selection per `agents/fire-brief-template.md`. Two read-only scouts (control-plane seam;
`internal/edge/sync` Manager) plus lead re-verification of every anchor below.

### 1. Scope sentence (verbatim, §9)

> **Inc 1 — control plane:** the second `lastSeqFn` read + `SyncEndSeq` on the result. Owns test 10.
> **Inc 2 — Manager gate:** the level-triggered gate, deadline, fallback, replacement; `callHydrate`
> surfaces the new field; marker handling per §3.2. Owns tests 1–9.

Green bar: §7's ten tests pass; `go build ./...`, `make vet`, `golangci-lint run ./...`,
`STRICT=1 go run ./scripts/lint-conventions.go`, `go test ./internal/edge/... ./internal/refractor/control/...
./cmd/facet/... -count=1` green; CI green on `main`.

### 2. Verified touch-list (`file:line`, checked live 2026-08-22)

| file:line | what |
|---|---|
| `internal/refractor/control/controlwire/controlwire.go:167` | `PersonalHydrateResult` — 4 fields today; add `SyncEndSeq` |
| `internal/refractor/control/service.go:1267` | `personalHydrate` — matches the design's citation |
| `internal/refractor/control/service.go:1296-1302` | the `SyncStartSeq` read + fail-soft; the mirror for the second read |
| `internal/refractor/control/service.go:1304-1321` | the hydrator fan-out loop; the second read goes after it |
| `internal/refractor/control/service.go:1333` | the result literal |
| `internal/refractor/control/service.go:343` / `:535` | `syncLastSeq` field + `SetSyncLastSeq` |
| `internal/edge/sync/sync.go:99-138` | `Config` — add `HydrateGateDeadline` |
| `internal/edge/sync/sync.go:159-168` | `gateMu` / `hydrateTarget` / `hydrateArmed` / `floor` |
| `internal/edge/sync/sync.go:211-271` | `deliveryFloor` — `reset`/`hold`/`release`/`commit`; **no read accessor** |
| `internal/edge/sync/sync.go:275-299` | `New` — where field defaults are applied |
| `internal/edge/sync/sync.go:325-334` | `Run`: `ensureFresh` **then** `m.floor.reset(stored)` |
| `internal/edge/sync/sync.go:361-364` | `Rehydrate` — the live path |
| `internal/edge/sync/sync.go:481` | `ensureFresh`'s `hydrate` call — the cold path |
| `internal/edge/sync/sync.go:578-604` | `hydrate` — arms at :586 |
| `internal/edge/sync/sync.go:606-630` | `armHydrateGate` / `hydrationGateReady` |
| `internal/edge/sync/sync.go:651-666` | `callHydrate` — returns `(revision, lenses, syncStartSeq)` |
| `internal/edge/sync/sync.go:697-723` | `handle` — floor advance + cursor persist + `commit` at :721 |
| `internal/edge/sync/sync.go:777-786` | the `hydrationComplete` case — **in `apply`, not `handle`** |
| `cmd/facet/feed.go:223-229`, `internal/edge/browser/feed.go:219-224` | both callbacks confirmed mutex-guarded idempotent re-marks |

**Two premises the scope-diff gate corrected — both load-bearing:**

1. **§3.2's arm-time floor read is unavailable on the cold path as written.** `Run` calls `ensureFresh`
   (which arms the gate) at `sync.go:325` and only *then* seeds `m.floor.reset(stored)` at `:334`, so at
   cold-arm time `m.floor` still holds the previous attach's state (zero on a first `Run`) — the stale
   cursor the anomaly check exists to catch is not yet in it. The check therefore evaluates against the
   **stored cursor at reset time**, in `Run` immediately after `m.floor.reset(stored)` and before
   `RunDurableConsumer` starts. R3 is preserved: no delivery can be consumed before the consumer starts.
   The `Rehydrate` path is unaffected — its consumer is live and its floor is same-session.
2. **`deliveryFloor` exposes no current value.** `release` returns `(0, false)` when the floor has not
   advanced, so the gate needs a `current()` accessor. It returns `persisted` — the floor that reached the
   store — which is exactly "paint follows applied state" and is the same number `handle` holds after
   `commit`, so both release paths compare like with like.

Also resolved here, not left for admit: **§3.2's "deadline-only mode"** for the cold-path anomaly is
implemented as §3.4's fallback family (scalar-revision marker gate **plus** the deadline), which is what
§3.2's own parenthetical names. Deadline-*only* would be strictly worse than shipped behaviour on the one
path the design argues must never be; the fallback family is never worse and is bounded.

### 3. Precedents to mirror

- Inc 1's second read mirrors `service.go:1296-1302` clause for clause (nil seam → Warn; error → Warn;
  else assign), with its own message naming the end read.
- Inc 1's tests mirror `internal/refractor/control/personal_hydrate_syncstartseq_test.go` — six tests,
  inline `func(context.Context, string) (uint64, error)` mocks, `bumpingHydrator` (`:20`) to prove read
  ordering relative to the fan-out.
- `Config.HydrateGateDeadline` default applied in `New` beside `stream`/`prefix`/`logger` (`sync.go:275-299`).
- Timer: `time.AfterFunc`; the package's existing timer precedent is `sync.go:517`'s `time.NewTimer`
  backoff. **No injectable clock exists in this package** — testability comes from `HydrateGateDeadline`
  being settable small, and tests wait on a channel fired from `OnHydrationComplete` (never `time.Sleep`,
  per CLAUDE.md).
- Fake transport / delta delivery: `sync_test.go:33` `fakeControlTransport`, `:498` `publishDelta`,
  `delivery_position_test.go:54` `countingTransport`.

### 4. Increment order

1. **Inc 1 — control plane.** `SyncEndSeq` on `PersonalHydrateResult`; second `lastSeqFn` read after the
   fan-out loop; `callHydrate` surfaces it (client-side plumbing only, no behaviour).
   Green: `go test ./internal/refractor/control/... ./internal/edge/... -count=1`.
2. **Inc 2 — Manager gate.** `hydrateGate` struct (generation, endSeq, revision, timer, progressed);
   `current()` on `deliveryFloor`; arm from `hydrate`; cold-path seed/anomaly check in `Run`; release on
   floor advance in `handle` after `commit`; marker handling per §3.2; stall-detector deadline; fallback
   and replacement. Green: the §7 tests + the whole package.

### 5. In-scope gotchas + the touched component's dossier (`docs/components/edge.md`, verbatim)

- **The local cursor is a FLOOR, not "the sequence that just succeeded"** — delivery is serial but a Nak'd
  frame redelivers later, so a cursor written per-success sits above the hole, and the next attach starts
  past it. Anything that makes the cursor a resume authority must keep it at or below every unresolved
  sequence.
- **A skipped delta is invisible to gap detection** — `personal.syncgap` tests `cursor < firstSeq`, so a
  cursor that is too HIGH is not a gap. Any change to delivery positioning must argue the skip direction
  explicitly.
- **A first-paint gate is state with a LIFETIME, and the failure to design for is the gate that never
  opens** — a release rule whose liveness fallback is armed only *after* its own release precondition is
  met cannot bound the case where that precondition never arrives. Hanging first paint forever is strictly
  worse than the partial paint being fixed.
- **The SYNC subject is per-ACTOR, not per-device** — a second device signed in as the same identity
  publishes onto the same feed, so any per-device rule keyed on "what arrived on my subject" can be
  satisfied or reset by the other device.
- **On the browser, resolving a position and using it are separated by an UNBOUNDED wait.**
- **The browser host gets ONE attach per page — it has no restart loop.**

Standing checklist (`agents/fire-brief-template.md`) — the two that bite hardest here: **#1 new state needs
a LIFETIME** (§3.5 is that table; every new field must appear in it) and **#4 a demoted mechanism needs
EVERY obligation enumerated** — `hydrationComplete`-drives-the-callback is *demoted, not deleted*: its
steady-state (no gate armed) and fallback-mode duties both survive, and §3.2 says so explicitly.

Other obligations: no Core KV touch, no lens/DDL/op change, no package version bump, no frozen contract
(`controlwire` is internal control wire, §8). `SyncEndSeq` is `omitempty`, so an older client decoding a
newer control plane, and a newer client against an older one, both degrade to the §3.4 fallback.

### 6. Adjacent finds

None out of scope so far; anything the build surfaces is absorbed into this run's batch per
`agents/steward/SKILL.md` §4.

### 7. Non-goals

Narrowing the no-gate-armed steady-state marker behaviour (§3.2 says so by name); any wire/envelope change
to deltas or markers; a per-hydrate correlation id (§4); an injectable clock for the package; the EDGE.5
browser-node transport work.

---

## Close-out (2026-08-22) — BUILT, both increments, one fire

**Status: ✅ BUILT.** Inc 1 `9d1c861`; Inc 2 in the same fire. No checkpoint to resume — the item is
complete, and the board row moves to the Done log.

### Deviations from the ratified body

Four, each argued and amended **in the body where it stands** rather than only here: the cold-path
comparison is `>=` and lives in `Run` (§3.2); the deadline is a stall detector in position mode and a
total bound in fallback (§3.3); progress follows resolution, not the persisted floor (§3.3); the
"shutdown" row is ctx-cancellation specifically, and a transient `Run` return keeps the gate (§3.5).
The design's `SyncEndSeq == 0` fallback family, replacement rules, marker handling and §7 test list
shipped as written.

### Review — 3-layer adversarial (three cold reviewers, none the implementer), one fix round

Two blocking defects and a blocking test gap, all fixed pre-ship:

- **The fallback gate could never open** — the progress signal was recorded for any armed gate, so
  ordinary live traffic on the per-actor subject re-armed the window indefinitely, and the permanently
  armed gate swallowed every later marker below its target. R1 and R2 refuted on the path R4 selects.
- **`Run`'s unconditional deferred disarm deleted the liveness backstop** on a transient consumer
  failure — a hang for the whole page on the browser host, which gets one `Run` and no restart loop.
- **The `armCold`/`armLive` discriminator — the entire embodiment of §10's blocker — had no test.**
  Deleting it left the package green, and it is production-reachable because a host that re-`Run`s the
  same Manager arms while the floor still holds the previous attach's cursor.

Plus five material fixes: the progress signal was blind to a burst replayed below an already-higher
cursor; the diverged-position check needed its equality boundary; three tests pinned a fixture artifact
or passed with the position mechanism switched off.

Fourteen mutations were run against the final tree, including every one that survived the first pass, and
all are now killed by a named test — among them "force `endSeq = 0` at arming", which must fail every
position-mode test and does.

### Findings classification (`agents/steward/SKILL.md` §4)

| class | count | routed to |
|---|---|---|
| design-gap (a ratified argument that did not survive contact) | 3 | body amendments above |
| implementation-bug | 1 | fixed |
| test-gap (a mechanism with no test, or a test pinning a fixture) | 4 | fixed; two classes to the dossier |
| review-over-reach (raised, ruled against, recorded) | 3 | §3.5's accepted residuals |

Two classes reached `docs/components/edge.md`'s dossier; two existing entries gained the check that now
catches them.
