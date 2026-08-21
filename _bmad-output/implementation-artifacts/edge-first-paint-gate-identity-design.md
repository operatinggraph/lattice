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
  endSeq` provably holds absent a stream reset — so an arm-time `floor > endSeq` there is not a release,
  it is **evidence the sequence spaces diverged** (stream recreation, DR restore, world wipe — reachable:
  `personalSyncGap` deliberately does no cursor validation, so a numerically-huge stale cursor reads as
  not-gapped, and the operator-requested `hydrationRequested` bit independently forces a hydrate). The
  cold-path arm therefore never releases immediately: `floor > endSeq` logs Error and degrades to the
  deadline-only mode (§3.4's fallback family) instead of painting an empty store. The old revision-space
  gate had no such failure mode, so the new position-space gate must refuse the comparison exactly where
  its precondition (monotone growth) is broken.
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
| reconnect without re-hydrate | gate persists — the floor is monotone across attaches (cursor-backed), endSeq stays valid |
| crash | in-memory, gone; next boot re-hydrates (new gate) or warm-resumes (no gate — host paints from the local store, existing behaviour) |
| shutdown | timer cancelled with the Manager's ctx |

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
