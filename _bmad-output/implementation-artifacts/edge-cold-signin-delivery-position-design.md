# Edge cold sign-in — deliver the world, not the ledger

**Status: ✅ RATIFIED 2026-08-06 (Winston, under delegated authority) — fork resolved to REPOSITION** — designed 2026-08-01 by the Designer fire against the
[lattice lane](../planning-artifacts/backlog/lattice.md) row *"[Edge] A cold sign-in replays the actor's
retained history, not their world"*.

## Ratification (Winston, 2026-08-06 — delegated by Andrew)

**Fork resolved to reposition; compaction rejected.** The deciding argument is altitude, not cost.
Reposition changes one client's own consumer: it starts where its hydrate snapshot was taken, which is the
standard snapshot-then-delta shape every sync protocol uses, and nothing outside that consumer observes
the change. Compaction would drop `MaxAge` and collapse per-key history **on a shared stream** to fix one
client's cold start — repairing a client symptom by changing a substrate-wide retention property other
consumers depend on. §8's four objections stand; this is the fifth and the one that settles it.

**The measurement is sound, and the design honestly reverses its own row.** The row was filed proposing
compaction; §5 re-grounds it and recommends the opposite. The headline number traces word-for-word to its
demo-box commit (`0d476524`, 2026-07-31): 2,049 frames to deliver a 14-key world, `ready` at 33 s.

**Verified at ratification:** the hardcoded policy is real — `DeliverPolicy: jetstream.DeliverAllPolicy`
at `internal/substrate/consumer.go:141`, with no policy field anywhere on the consumer config struct, so
the transport seam genuinely cannot express a starting position today.

**One thing this is NOT, recorded so a builder does not conflate them.** `SetSyncFirstSeq`
(`internal/refractor/control/service.go:405-412`) is the **`syncgap` op's gap detector** —
`gapped = cursor < firstSeq`, fail-closed when unset. It reasons about a stream's first sequence to decide
whether a client's cursor has been trimmed away; it is not a delivery-position setter and gives this fire
nothing to reuse beyond the vocabulary.

**For Andrew:**

- **What it does.** A cold Edge sign-in calls `personal.hydrate` — which publishes the actor's whole
  current world — and *then* attaches a durable consumer that starts at the **beginning of the retained
  subject**, so it replays every earlier delta and every earlier hydration burst before reaching the one
  it just asked for. This design positions the consumer **at the hydration point** instead: the hydrate
  RPC returns the SYNC stream sequence it captured before publishing, and the node starts there. Measured
  live 2026-07-31: 2,049 frames for a 14-key world, `ready` at 33 s → ~38 frames, sub-second.
- **THE FORK — and it reverses the backlog row's own framing.** The row proposes **per-key subjects +
  JetStream compaction** ("needs tombstones, a keyset-frame home, and new cursor/gap semantics", size
  **L**). I designed both branches through and **recommend against compaction** (§8): it requires
  dropping `MaxAge`, which turns the SYNC stream into a permanent second copy of every actor's world —
  a read model beside the read model — and it contradicts the design-of-record's *ephemerality*
  property. The recommended shape needs **no new subject layout, no tombstones, no ACL change, no
  contract change**, and the amplification is a *delivery-position* defect, not a *retention* one.
  This drops the item from **L to M**. Your call to confirm the reversal.
- **Frozen contracts: NO CHANGE.** Nothing under `docs/contracts/*` describes the sync transport, the
  delta envelope, or the `personal.*` control ops (grepped; `personal-secure-lens-design.md` §4 already
  ruled these component-level). No uncommitted contract edit is staged by this fire.
- **One latent defect this fix unmasks is in scope** (§6, Fire 4): `personal.hydrate` fans out to every
  personal lens and each publishes its own `hydrationComplete` marker, so the Edge's gate releases on the
  first one — today that is hidden inside a 33-second replay; after Fire 2 it becomes a first-paint
  showing a partial world. Fire 4 closes it in the same initiative rather than leaving it for whoever
  notices the flicker.

---

## 1. Problem

### 1.1 The measurement

Live on the demo box, 2026-07-31: a cold Facet sign-in for an identity whose authorized world is **14
keys** consumed **2,049 delta frames** — a **146×** amplification — and did not fire
`OnHydrationComplete` (Facet's "ready" gate) for **33 seconds**.

### 1.2 The mechanism, in code

One subject carries an actor's whole feed: `lattice.sync.user.<identityId>`
(`internal/refractor/subjects/subjects.go`, `subjects.PersonalSync`). Everything the Personal Lens plane
emits for that actor lands there — `upsert`, `delete`, per-lens `keyset` frames, and the terminal
`hydrationComplete` marker (`internal/refractor/adapter/natssubject.go`).

The backing `SYNC` stream retains `MaxAge: 24h` with `MaxMsgsPerSubject: 10_000`
(`natssubject.go`, `syncStreamMaxAge` / `syncStreamMaxMsgsPerSubject`).

The Edge attaches with `substrate.RunDurableConsumer`, whose consumer config is **hardcoded**
`DeliverPolicy: jetstream.DeliverAllPolicy` (`internal/substrate/consumer.go:142`), and the Edge
transport seam carries no policy field at all (`internal/edge/transport/transport.go`,
`ConsumerConfig{Stream, Durable, FilterSubject, Logger}`).

So on a cold start `sync.Manager.Run` (`internal/edge/sync/sync.go:183`) does:

1. `ensureFresh` → no local cursor → `hydrate()` → `personal.register` + `personal.hydrate`;
   the Refractor bulk-projects the actor's world onto the subject and publishes the terminal marker.
2. `RunDurableConsumer` — a durable with **no ack floor**, `DeliverAll` — which starts at the
   **earliest retained message on that subject** and walks forward through everything the last 24 hours
   deposited before it reaches the burst step 1 just produced.

The code already knows this. `hydrate()`'s own doc comment (`sync.go:331-337`) says it plainly:

> The per-actor subject retains every delta ever published to it, including every prior hydrate cycle's
> own terminal marker, so a cold durable consumer — one with no ack floor, i.e. exactly the case that
> just called `hydrate()` — replays them all before reaching the fresh burst this call just triggered.

…and then works *around* it: `armHydrateGate`/`hydrationGateReady` exist purely so a **stale replayed
marker** does not release the boot gate early. **A workaround that exists to survive a self-inflicted
replay is the tell** — the premise ("a cold consumer must start at the beginning") was never verified.
It is not true: the durable's start position is a per-consumer choice the Edge simply never made.

### 1.3 Why it is 146× and not 2×

The amplification is **self-inflicted and superlinear**. Each cold start's own hydration burst is
published to the retained subject, so the *next* cold start replays it too. With a world of `W` keys,
`L` personal lenses, and `n` cold starts inside the retention window, start `n` replays roughly

```
n · (W + 2L)   frames  — every prior burst — plus the live deltas in the window
```

`edge-manifest` installs **15** personal lenses (counted 2026-08-11: `Personal: true` ×15 in
`packages/edge-manifest/lenses.go`, whose own header says "fifteen Personal-Lens declarations"). With
`W=14`, `L=15`, that is ~44 frames per burst, and `n` bursts accumulate until the 10,000-message
per-subject cap clips them.

**Amended 2026-08-11 (build time): what makes a cold start frequent is no longer device-id churn.** This
section originally read that `cmd/facet`'s `engineManager.Acquire` mints a fresh device id per engine
build, so every rebuild was a cold start. `0b6879dc` (2026-08-01, *"one durable per identity, not one per
engine build"*) landed after this design was written and falsified it: `enginemanager.go` now resolves
"this host's stable, store-persisted" device id, so a rebuild resumes the durable it left. §5 already
narrows the blast radius on that ground; the sentence is corrected here so the body does not instruct the
next reader with a premise its own §5 retracts. What remains cold is a genuinely new device, and the
reaped-then-recreated durable the browser host's `InactiveThreshold` produces on every return.

**The amplification itself did not shrink — it grew.** Re-measured live 2026-08-11: the SYNC stream holds
556,687 messages / 683 MB across 116 subjects, and **all 9** edge durables are `deliver_policy=all` with no
`opt_start_seq`, **three of them pinned at exactly 10,000 pending** — the per-subject cap, i.e. ~700× for a
14-key world rather than the 146× that motivated the item.

### 1.4 It contradicts the design of record

The vault's *Edge Lattice / Personal Lens.md* §3 defines the Hydration Hook as: run the cypher for the
current state, send a bulk batch, *"After that, it reverts to only sending tiny, incremental updates"* —
and §4.2 (Ephemerality): a long-offline device *"doesn't wait for a week of backlogged messages"*, it
re-hydrates. The shipped behaviour re-hydrates **and then** waits for the backlog. The intent was always
"the hydrate supplies the world, the stream supplies the future"; only the delivery position was missing.

---

## 2. Intent

**One invariant, stated once:** *a hydrate repositions the feed. Nothing published before the hydration
snapshot is ever delivered, because the hydration burst already carries its effect.*

Everything below is the plumbing that makes that sentence true on both hosts.

---

## 3. The shape

### 3.1 Two sequence spaces — keep them straight

This surface has two monotonic counters and conflating them is the standing trap here (the Loupe lane
already filed it: *"`revisionCursor` is NOT a SYNC sequence — it is the pipeline's `LastAppliedSeq`"*,
[loupe.md](../planning-artifacts/backlog/loupe.md) 2026-07-19).

| Counter | Space | Written by | Consumed by |
|---|---|---|---|
| `deltaEnvelope.revision` / `projectionSeq` | the **CDC** stream (`Pipeline.Progress().LastAppliedSeq`) | `pipeline.Hydrate` (`hydrate.go:36`), the live projector | the Edge store's last-writer-wins-by-revision |
| the Edge **cursor** (`store.SetCursor(d.Sequence)`) | the **SYNC** stream (`meta.Sequence.Stream`, `internal/substrate/consumer.go:265`) | `sync.handle` | `personal.syncgap`'s retention-gap test; **this design's start position** |

The start position is a **SYNC** sequence. It is *not* the hydrate response's existing `Revision` field,
and this design does not overload that field.

### 3.2 `personal.hydrate` returns the position it hydrated from

`personalHydrate` (`internal/refractor/control/service.go:1023`) gains a **single read of the SYNC
stream's last sequence, taken once before the hydrator fan-out**, returned as a new
`controlwire.PersonalHydrateResult.SyncStartSeq`.

The seam mirrors the shipped one exactly: `Service.SetSyncLastSeq(func(ctx) (uint64, error))`, sibling
to `SetSyncFirstSeq` (`service.go:412`), wired in `cmd/refractor/main.go:944` off the same full-grant
stream handle (`st.State.LastSeq` where the existing call takes `st.State.FirstSeq`). One handle is
correct for the same reason the existing comment gives: every Personal Lens rule shares one SYNC stream.

**Why this is race-free, and fail-safe in the one direction that matters.**

- The read happens **before** any hydrator runs. `pipeline.Hydrate` re-executes the cypher against Core
  KV, which only moves forward, so anything *not* in the reprojection was written after the read and is
  therefore published at a SYNC sequence **greater than** `SyncStartSeq`. Nothing can fall in the crack.
- A **stale-low** value (an older cached `StreamInfo`) only makes the node start earlier and replay a
  little — harmless. A stale-**high** value would skip messages, and is unreachable: cached info is
  older, hence lower. The build must still take the value from a **freshly fetched** `StreamInfo`
  (`js.Stream(ctx, name)` round-trips `STREAM.INFO` and then `CachedInfo()` returns *that* response) —
  never a long-lived handle's cache.
- The consumer is created **after** the burst has been published. That is fine and deliberate: the
  **stream** retains the burst, not the consumer. There is no need to invert `Run`'s order or split the
  consumer's create/run lifecycle.

`SyncStartSeq` absent or `0` ⇒ the node falls back to today's `DeliverAll`. That keeps a new node
against an old control plane (and vice versa) working, and the fallback errs toward delivering *more*,
never less.

### 3.3 The delivery-position seam

Three layers, each addition purely additive with a behaviour-preserving zero value:

| Layer | Addition | Zero value |
|---|---|---|
| `internal/substrate/consumer.go` | `DurableConsumerConfig.StartSeq uint64` → `DeliverPolicy: DeliverByStartSequencePolicy, OptStartSeq: StartSeq` | `0` ⇒ `DeliverAllPolicy` — every other caller in the repo is untouched |
| `internal/edge/transport/transport.go` | `ConsumerConfig.StartSeq uint64` | `0` ⇒ as today |
| `internal/edge/browser/jstransport.go` → `shell.mjs` | `startConsumer({…, startSeq})` → `deliver_policy: 'by_start_sequence'` + `opt_start_seq` | omitted ⇒ nats.js default `all` |

**Delete-then-create is mandatory, not stylistic.** Verified against the pinned server
(`nats-server v2.14.0`, `server/consumer.go:2434-2438`, `checkNewConsumerConfig`):

```
if cfg.DeliverPolicy != ncfg.DeliverPolicy { return errors.New("deliver policy can not be updated") }
if cfg.OptStartSeq   != ncfg.OptStartSeq   { return errors.New("start sequence can not be updated") }
```

A `CreateOrUpdateConsumer` against an existing durable with a different start position **fails**. So the
Edge transport calls `substrate.DeleteStreamConsumer` (`internal/substrate/subscribe.go:256` — already
exists, already idempotent on not-found, already documented as "safe to call unconditionally on every
startup") before creating, and the browser shell calls `jsm.consumers.delete` in the same place. This
also *is* the live migration: every durable on the demo box is `DeliverAll` today, and the first boot on
the new binary simply replaces it.

**No ACL change.** `internal/gateway/natsauth.PermissionsFor` already grants
`$JS.API.CONSUMER.DELETE.SYNC.<durable>` alongside `CREATE`/`MSG.NEXT`/`INFO`. The durable name is
unchanged, the filter subject is unchanged, the subscribe grant is unchanged.

### 3.4 The Edge picks its own start position — the cursor becomes the single resume authority

`sync.Manager.Run` computes `StartSeq` from local state and passes it down:

| Path | `StartSeq` | Why |
|---|---|---|
| cold (no cursor) → hydrate | `SyncStartSeq + 1` from the hydrate response | the burst is the world; everything before it is already in the burst |
| gapped / operator-requested hydration | `SyncStartSeq + 1` | same — a gap is just a cold start with a stale cursor |
| warm, no gap | `cursor + 1` | resume exactly where the local store left off |
| any path, `SyncStartSeq == 0` | `0` ⇒ `DeliverAll` | old control plane; preserve today's behaviour |

This makes the **local cursor the single resume authority** and demotes the server-side ack floor to a
per-session delivery detail. That is a simplification, not a new mechanism: the cursor is already the
authority for gap detection, so today two sources of truth govern one position.

**The ack floor was silently carrying one other obligation — poison-message disposal.** `handle`
returns `Term` (malformed envelope, `store.ErrUnstorableKey`) **without** advancing the cursor
(`sync.go:455`, `sync.go:525`); today the server's ack floor moves past the terminated message so it
never returns. Once the start position comes from the cursor, a `Term`ed message would be re-delivered
on every boot forever. **Fire 2 must advance the cursor on `Term` as well as on `Ack`** — a permanently
disposed message is a position the node has passed. (Found by the §9 pass; it is exactly the
"a removed component was silently load-bearing" class.)

### 3.5 What is deliberately *not* changed

- **`Rehydrate`** (`sync.go:205`, the agent's `RevisionConflict` re-audit) runs mid-session against an
  already-live, already-caught-up consumer. It cannot reposition without tearing the consumer down, and
  it does not need to: there is no backlog in front of a caught-up consumer. Unchanged.
- **`armHydrateGate`** stays. After Fire 2 it can no longer see a *replayed* stale marker, but it still
  guards the real remaining case — a second `hydrate`/`Rehydrate` issued while a prior burst is in
  flight, where the earlier cycle's marker legitimately arrives after the later cycle armed its gate.
- **`personal.syncgap`** is unchanged. Its predicate (`cursor < firstSeq`) is still exactly right, and
  it still runs *before* the reposition decision.
- **Retention.** `MaxAge: 24h` + `MaxMsgsPerSubject: 10_000` stay as they are. They bound storage; they
  were never the amplifier.

### 3.6 Read path / write path

| Concern | Mechanism | Invariant |
|---|---|---|
| Read | unchanged — `SUB lattice.sync.user.<id>` on the `SYNC` stream, plus the `personal.*` control RPCs | **P5** — the Edge reads a lens projection, never Core KV |
| Write | untouched — this design writes no Core KV and submits no operation | **P2** |
| New state | **none.** `SyncStartSeq` is computed per call from stream state; the position lives in the already-persisted local cursor | **P1** — nothing new enters Core KV |

### 3.7 Security

The change can only ever cause the node to receive **less** of its own subject. The filter subject, the
subscribe grant, and the durable name are unchanged, so no data crosses an identity boundary that could
not before. `OptStartSeq` is client-chosen, so a node could name a *lower* start and replay its own
history — which the current `DeliverAll` default already does unconditionally, and which is its own
authorized data either way. No new surface, and the omission direction (`StartSeq == 0`) delivers more,
not less: **fail-safe, not fail-open**.

---

## 4. Contract surface

| Doc | Change vs. build-to | Note |
|---|---|---|
| `docs/contracts/*` | **NO CHANGE** | Grepped for `lattice.sync` / `personal.hydrate` / `hydrationComplete` / `deltaEnvelope`: zero hits. `personal-secure-lens-design.md` §4 already ruled the sync transport, the delta envelope, and the control ops **component-level** surfaces owned by `refractor.md`. No uncommitted contract edit is staged. |
| `docs/components/edge.md` | **update, same commit as the code** | The `internal/edge/sync` bullet describes cold start as "calls register/hydrate before subscribing" — it must state the reposition. Drift between page and code is a documentation bug (that page's own rule). |
| `docs/components/refractor.md` | **update** | The delta-envelope / control-op section gains `syncStartSeq` on the hydrate response. |
| `docs/vendors.md` | **update (one row)** | The NATS row gains the load-bearing fact that `DeliverPolicy`/`OptStartSeq` are **not updatable** on an existing consumer (`nats-server` 2.14 `server/consumer.go:2434`), which is *why* the Edge deletes before creating. The nats.js row already names `make test-edge-consumer-parity` as the pin; Fire 3 extends that gate. |

---

## 5. Reconciliation with the existing mental model

**"Didn't we already fix the SYNC stream?"** Two adjacent things shipped and neither touches this.
`b9b9cad3` (2026-07-30) added `MaxMsgsPerSubject: 10_000` — that **caps** the ledger, it does not stop a
consumer reading it. `533a0b71` (2026-07-28) made the `hydrationComplete` boot-gate match the hydrate
RPC's own target revision — that is `armHydrateGate`, i.e. the **workaround for** this defect. The
board row says as much: *"retention cap + boot-gate already shipped; this is the amplification."*

**"Doesn't this duplicate an established pattern?"** It *adopts* one. `substrate` already supports
positioned consumers everywhere else; the Edge seam is the one place that hardcodes `DeliverAll`. And
the control-plane addition is a verbatim mirror of the shipped `SetSyncFirstSeq` seam
(`edge-syncgap-control-rpc-design.md`) — same stream handle, same fail-closed posture, same "one handle
because every personal lens shares one SYNC stream" reasoning.

**"Does this introduce new state?"** No. The position is derived, and the only persisted value is the
cursor the store already keeps.

**"What about the sibling row — the leaked durables?"** The leaked-durable row and this one share one
mechanism and split cleanly:

- That row's clause *"each new durable also re-reads the whole retained subject"* is **subsumed here** —
  after Fire 2 a fresh durable starts at the hydration point and reads nothing historical, and the
  "durables pinned at the 10k per-subject cap" observation becomes "pinned at ~0 pending".
- What that row owns on its own is the **count**, and that half **shipped** (`0b6879dc`): the device id
  is persisted per identity and the durable is reaped on sign-out.

That leaves §1.3's frequency premise **narrower than measured**. The 2,049-frame reading was taken when
*every* engine rebuild minted a fresh device id, so every idle-reap and every process restart was a cold
start; a rebuild now resumes the durable it left. What remains cold is a genuinely new device — and the
reaped-then-recreated durable the browser host's `InactiveThreshold` produces on every return. The
amplification per cold start is unchanged, so the fork below stands on its own; only its blast radius is
smaller than the number that motivated it.

That `InactiveThreshold` is also why the Go host still has no server-side backstop for an orphan a purge
cannot reach (a revoked credential, a crashed host). The browser's 30-minute value cannot simply be
copied over: on a long-lived Go host it would convert routine next-day sign-ins into exactly the cold
start this initiative is about.

**Amended 2026-08-11 (build time) — this design does NOT gate the backstop.** The sentence here originally
read *"the fork below sets that value — the backstop is sequenced behind it."* The later
`edge-sync-orphan-expiry-design.md` (ratified 2026-08-06) answers that directly and refutes it: its §7
shows the threshold derives from the SYNC stream's own retention horizon, not from this fork — past
`MaxAge` a surviving durable and a fresh one deliver the **identical** message set, so reaping at
`MaxAge + margin` costs the same under either branch. Its "For Andrew" block states the conclusion as
*"This row is unblocked."* It also deliberately takes the **stream-level** `ConsumerLimits.InactiveThreshold`
shape precisely so it need not touch `RunDurableConsumer` or the transport seam this design edits. The two
are independent; neither sequences the other.

No other in-flight design touches `RunDurableConsumer`, the sync seam, or the personal control ops
(grepped across `_bmad-output/implementation-artifacts/`; re-verified 2026-08-11).

**Loupe, cross-lane, informational.** Loupe's Edge Fleet triage derives retention headroom from *stream*
state and the device cursor, not from consumer `num_pending`, so it needs no change — but operators will
see per-device pending collapse from ~10k to ~0, which is the panel finally being able to distinguish a
lagging device from a healthy one. Worth a Loupe-lane confirmation after Fire 2, not a Loupe-lane fire.

---

## 6. Decomposition for the Steward

Four fires, each independently shippable and green. Sizes are S unless noted; the initiative is **M**.

### Fire 1 — `personal.hydrate` returns the SYNC start position

- `controlwire.PersonalHydrateResult` gains `SyncStartSeq uint64` (`json:"syncStartSeq,omitempty"`).
- `Service.SetSyncLastSeq(fn func(ctx) (uint64, error))` + the `syncLastSeq` field, mirroring
  `SetSyncFirstSeq`/`syncFirstSeq` (`service.go:278`, `:405`).
- `personalHydrate` reads it **once, before** the hydrator loop, and returns it. Unset seam ⇒ return
  `0` and log — hydration itself must still succeed (the field is an optimisation input, and its zero
  value is today's behaviour; failing hydrate closed on it would be a regression, not a safety win).
- `cmd/refractor/main.go` wires it next to the existing `SetSyncFirstSeq` call (`:944`), from a
  **freshly fetched** `StreamInfo`.
- **Green:** control-plane unit tests — the value is read before any hydrator runs (a hydrator that
  publishes must not move it), a nil seam degrades to `0`, and the returned value is `≤` the first
  sequence any burst frame lands on.

### Fire 2 — the Go host consumes it (the amplification fix) — **M**

- `substrate.DurableConsumerConfig.StartSeq` → `DeliverByStartSequencePolicy` / `OptStartSeq`; `0`
  preserves `DeliverAllPolicy` verbatim.
- `transport.ConsumerConfig.StartSeq`; `natstransport.RunDurableConsumer` calls
  `DeleteStreamConsumer` before starting when `StartSeq > 0`.
- `sync.Manager`: `hydrate()` returns the `SyncStartSeq`; `Run` resolves the §3.4 table and passes it.
- `handle` advances the cursor on `Term` as well as `Ack` (§3.4).
- `docs/components/edge.md` + `refractor.md` + the `docs/vendors.md` NATS row, same commit.
- **Green — this is the acceptance bar for the whole initiative:** an e2e that publishes ~200 stale
  frames onto an actor's subject, then cold-starts a node, and asserts (a) the mirror holds exactly the
  hydrated world and (b) the node received **fewer than `W + 2L + 5` frames** — not 200. Plus a warm
  restart resuming at `cursor + 1`, a `Term`ed poison frame that does **not** return after a restart,
  and an upgrade case: a pre-existing `DeliverAll` durable is replaced rather than erroring.

### Fire 3 — browser-host parity

- `jstransport.go` passes `startSeq` in the `startConsumer` arg map; `shell.mjs` maps it to
  `deliver_policy: 'by_start_sequence'` + `opt_start_seq` and deletes the durable first.
- Extend `make test-edge-consumer-parity` (CI job `edge-consumer-parity`) to pin the **new** wire form
  against the granted ACL. That gate exists precisely so the two clients cannot drift
  (`docs/vendors.md`, nats.js row) — it is a required part of this fire, not a follow-on.
- **Green:** the parity job plus `shell.test.mjs` coverage of the delete-then-create ordering.

### Fire 4 — one hydration marker per hydrate, not per lens

`personalHydrate` fans out to every registered hydrator and **each** `pipeline.Hydrate` publishes its own
`hydrationComplete` (`hydrate.go:85-89`). The Edge gate releases on the first marker whose revision `≥`
the target (`hydrationGateReady`, `sync.go:379`), and the target is the **max** revision across lenses —
so whichever lens holds the max can release the gate while other lenses are still bursting. Today a
33-second replay hides it; after Fire 2 the burst is the only thing on the wire and a first paint can
render a partial world.

- **Recommended fix (client-side, smaller):** stamp `Lens` on the `hydrationComplete` envelope (every
  other attributed envelope already carries it) and have the Manager release the gate only once it has
  seen a satisfying marker for **every** rule ID in the hydrate response's existing `Lenses` field.
  No control-plane sequencing change, and it degrades safely against a pre-Fire-4 producer (no `Lens`
  ⇒ today's first-marker behaviour).
- **Green:** a two-lens hydrate where the higher-revision lens publishes first must not fire
  `OnHydrationComplete` until the second lens's frames have landed.

---

## 7. Migration, compatibility, risks

| Risk | Disposition |
|---|---|
| **Existing `DeliverAll` durables** (demo box, any running node) | Handled by design: the hydrate/warm paths delete before creating. No operator action, no downtime step. |
| **Version skew** — new node ↔ old control plane | `SyncStartSeq` absent ⇒ `0` ⇒ `DeliverAll` ⇒ exactly today. Old node ↔ new control plane: the extra field is ignored. |
| **Delete-then-create is destructive to a concurrent consumer on the same durable name** | Durable names are per `(identity, device)`; `cmd/facet` mints one device id per engine and the browser shell holds a leader lock so only the leader opens the durable. This is a real sharp edge, so Fire 2 states it in the seam's doc comment: *the caller must own the durable*. |
| **Crash between delete and create** | Safe — the next boot recreates from the local cursor, which is unaffected. |
| **A start position ahead of what the node actually applied** | Impossible on the warm path (`cursor + 1` is by definition applied+persisted) and impossible on the hydrate path (§3.2's forward-only argument). |
| **A pruned start sequence** (`cursor + 1 < firstSeq`) | Cannot reach the consumer: `personal.syncgap` runs first and forces the hydrate path, which uses `SyncStartSeq` instead. If it somehow did, JetStream starts at the first available message — the over-deliver direction. |

---

## 8. Alternatives considered

**A. Per-key subjects + JetStream compaction — the backlog row's own framing. REJECTED.**
`lattice.sync.user.<id>.<key>` with `MaxMsgsPerSubject: 1` would make the retained stream *be* the
world. Four grounded objections, any one sufficient:

1. **Compaction and `MaxAge` are contradictory here.** A key not updated in 24 h would be **deleted**
   from the stream, so the "replay the compacted world" path would silently omit every stable key.
   Compaction therefore requires `MaxAge: 0` — an unbounded, permanent per-actor copy of every world in
   JetStream: a second read model beside the lens target, which is exactly what P5 exists to avoid, and
   the direct negation of the *ephemerality* property (*Personal Lens.md* §4.2) the transport was
   designed around.
2. **A delete must then persist as a retained tombstone**, or the compacted replay resurrects a
   retracted key. That is new wire vocabulary *and* new reap semantics for it.
3. **`keyset` and `hydrationComplete` frames have no per-key home** — they are per *(actor, lens)* and
   per *hydrate*, so they would need their own subject and their own retention rule, splitting one
   ordered feed into three unordered ones.
4. **It widens the ACL.** The subscribe grant is exactly `lattice.sync.user.<identityId>`, no trailing
   wildcard (`natsauth.PermissionsFor`) — per-key subjects require `…<id>.>`, which is a widened
   trust-boundary grant and a re-verification of the six `internal/natsperm` Edge callout vectors.

And the decisive point: **it does not address the actual defect.** Even a perfectly compacted stream is
still consumed `DeliverAll`; the reason a cold node reads 2,049 frames is that nothing ever told it
where to start. Compaction shrinks the ledger; positioning stops reading it. *(Could a variant beat the
recommendation? Only if the goal were "make the SYNC stream a durable world mirror" — a different
feature, and one the read-model target already is.)*

**B. Shorter `MaxAge` / smaller `MaxMsgsPerSubject`. REJECTED.** Purely a constant factor. 50 cold
starts in one hour still replay 50 bursts, and shrinking retention *reduces* the warm-resume window
that makes incremental sync work at all — trading a real capability for a partial mitigation.

**C. Publish hydration bursts to a separate subject** (`lattice.sync.user.<id>.hydrate`) with its own
tiny retention. **REJECTED** — same ACL widening as A, splits one ordered feed into two whose relative
order is then undefined (a burst row and a concurrent live delta for the same key could arrive in either
order across subjects; today the single subject makes that ordering free), and it still leaves live
deltas replaying on the main subject.

**D. Per-session durable name** (a boot nonce, Loom's pattern) + `InactiveThreshold`, so the create is
always fresh and never conflicts. **REJECTED on a hard ground:** the ACL grants exactly
`edge-sync-<identity>-<device>` (`natsauth.PermissionsFor`), so a nonce-suffixed durable is not
authorized — it needs an ACL change and a natsperm re-verification to buy something delete-then-create
gives for free.

**E. Fix the device-id churn only** (the sibling backlog row). **Necessary but not sufficient** — see
§5. It reduces how *often* a cold start happens; it does not stop one from replaying 24 h of live
deltas, and it does nothing for a genuinely busy actor.

**Dead-scaffolding test.** Every fire has a live consumer today: Facet on the demo box hits this path on
every engine build, and Fire 3's browser host is the shipped `FACET_BROWSER_ENGINE` mode. Nothing here
is built ahead of its consumer.

---

## 9. Adversarial pass (run 2026-08-01 — DISCHARGED, no gate left open)

A self-adversarial pass against the code, run before flagging this for ratification. Findings and their
disposition — each one is folded into the design above, not deferred:

1. **`DeliverPolicy` is immutable on an existing consumer.** Assumed updatable in the first draft;
   falsified against the pinned server source (`nats-server` 2.14 `server/consumer.go:2434-2438`). Fix:
   delete-then-create, §3.3 — which also turned out to *be* the migration path for existing durables.
2. **The ack floor was silently carrying poison disposal.** `Term` does not advance the cursor today;
   moving the resume authority to the cursor would have made a malformed frame re-deliver on every boot
   forever. Fix: §3.4, in Fire 2's scope with its own test.
3. **The fix unmasks the multi-lens `hydrationComplete` race.** Real, and caused by this change becoming
   fast enough to expose it. Fix: Fire 4, in scope.
4. **Could the reposition skip a delta?** Traced end to end (§3.2). It cannot: Core KV moves forward
   only, so anything absent from the reprojection is published *after* the sequence read. Additionally
   the failure direction of a stale read is provably "deliver more".
5. **Does the browser host actually carry the field?** The transport is *named*, not assumed:
   `sync.Config` → `transport.ConsumerConfig` → `jstransport.go:71`'s arg map → `shell.mjs`
   `startConsumer({stream, durable, filterSubject})` → `jsm.consumers.add`. The chain exists and Fire 3
   extends each hop, pinned by the existing `edge-consumer-parity` gate.
6. **Does the ACL permit it?** Checked, not assumed: `CONSUMER.CREATE` (filtered form), `MSG.NEXT`,
   `INFO`, **`DELETE`**, and `$JS.ACK` are all granted per durable. No `natsperm` change, no re-run of
   the six Edge callout vectors.
7. **Does a security boundary change direction?** No — §3.7; the omission case delivers more, never
   less, and never crosses an identity.

---

## 10. Acceptance

The initiative is done when, on the demo box, a cold Facet sign-in for a `W`-key world receives
**O(W + L)** frames rather than O(n·(W + L)) — measured the same way the defect was: the frame count and
time-to-`ready` for one identity's cold start, before and after, recorded here.

---

## 11. Fire brief (build note, 2026-08-11) — the whole initiative, Fires 1–4

Compiled by the Lattice Steward at selection, from three read-only scouts + the lead's own live grounding.
One brief per ITEM; later fires of this item run a delta-scout, not a recompiled brief.

### 1. Scope sentence (verbatim, §10 + §6)

> The initiative is done when, on the demo box, a cold Facet sign-in for a `W`-key world receives
> **O(W + L)** frames rather than O(n·(W + L)) — measured the same way the defect was.

Fire 2 carries the acceptance bar: *a cold-started node receives fewer than `W + 2L + 5` frames, not 200.*

### 2. Verified touch-list — every design citation re-checked live; ALL had rotted

The design's line numbers are from 2026-08-01 and are off by 50–440 lines. Anchors below are today's.

| File | Design said | **Actual today** | What changes |
|---|---|---|---|
| `internal/refractor/control/service.go` `personalHydrate` | :1023 | **:1235** | read SYNC last-seq once before the hydrator loop |
| `…/service.go` `syncFirstSeq` field | :278 | **:329** | add `syncLastSeq` sibling |
| `…/service.go` `SetSyncFirstSeq` | :405-412 | **:503-507** | add `SetSyncLastSeq`, verbatim mirror |
| `…/control/controlwire/controlwire.go` `PersonalHydrateResult` | — | **:167** | `+ SyncStartSeq uint64` |
| `cmd/refractor/main.go` wiring | :944 | **:1384** | add `SetSyncLastSeq` off `st.CachedInfo().State.LastSeq` |
| `internal/substrate/consumer.go` `DurableConsumerConfig` | — | **:89-130** | `+ StartSeq uint64` |
| `…/consumer.go` hardcoded `DeliverAllPolicy` | :141-142 | **:165** | branch on `StartSeq > 0` |
| `…/consumer.go` `meta.Sequence.Stream` | :265 | **:296** | unchanged (reference) |
| `internal/substrate/subscribe.go` `DeleteStreamConsumer` | :256 | **:394-402** | unchanged; called by transport |
| `internal/edge/transport/transport.go` `ConsumerConfig` | verified | **:48-53** | `+ StartSeq uint64` |
| `internal/edge/sync/sync.go` `Manager.Run` | :183 | **:200-213** | resolve §3.4 table, pass `StartSeq` |
| `…/sync.go` `hydrate()` | :331-337 | **:341-354** | return `SyncStartSeq` |
| `…/sync.go` `armHydrateGate`/`hydrationGateReady` | both :379 | **:383-388 / :396-407** | Fire 4: per-rule satisfaction |
| `…/sync.go` `handle` Term paths | :455,:525 | **:472 + `classifyApplyError` :543 returned at :480,:492** | advance cursor on Term |
| `…/sync.go` `SetCursor` | — | **:526** | the single call all Term paths skip |
| `internal/edge/browser/jstransport.go` arg map | :71 | **:71-75** | `+ startSeq` |
| `internal/edge/browser/shell/shell.mjs` `startConsumer` | — | **:133, add at :140-145** | delete-then-create + `deliver_policy`/`opt_start_seq` |
| `internal/refractor/adapter/natssubject.go` `PublishHydrationComplete` | — | **:367-374** | Fire 4: `+ Lens: a.ruleID` |

### 3. Precedents to mirror

- **Control seam** → `SetSyncFirstSeq` (`service.go:503-507`) + its wiring (`cmd/refractor/main.go:1384`),
  which already takes a **freshly fetched** `StreamInfo` (`conn.JetStream().Stream(ctx,…)` then
  `CachedInfo()`), exactly the posture §3.2 mandates. `LastSeq` replaces `FirstSeq`, nothing else.
- **`Lens` on an envelope** (Fire 4) → `Upsert` (`natssubject.go:266`) and `PublishKeySet` (`:291`) already
  set `Lens: a.ruleID`. `hydrationComplete` and `delete` do not.
- **Delete-then-create** → `DeleteStreamConsumer` already documented idempotent-on-not-found; existing
  callers `cmd/processor/main.go:184`, `cmd/facet/enginemanager.go:342`.
- **Control-plane test shape** → `personal_syncgap_test.go` (the only file exercising `SetSyncFirstSeq`).

### 4. Increment order + runnable green checks

1. **Inc 1 = Fire 1** (control plane returns the position).
   `go test ./internal/refractor/control/... -count=1`
2. **Inc 2 = Fire 2** (Go host consumes it + Term advances the cursor). **The acceptance bar.**
   `go test ./internal/substrate/... ./internal/edge/... -count=1`
3. **Inc 3 = Fire 4** (one hydration marker per hydrate).
   `go test ./internal/edge/sync/... ./internal/refractor/adapter/... -count=1`
4. **Inc 4 = Fire 3** (browser parity). `make test-edge-consumer-parity`

Gates, all increments: `go build ./...`, `make vet`, `golangci-lint run ./...`,
`STRICT=1 go run ./scripts/lint-conventions.go`, `make verify-kernel`.

### 5. In-scope gotchas

**Premises re-run live this fire — three corrections, one confirmation:**

- **`L` = 15, not "≈12"** (`packages/edge-manifest/lenses.go`, `Personal: true` ×15). So Fire 2's bar for a
  14-key world is `W + 2L + 5` = **49 frames**, not ~43.
- **The live baseline is far worse than the design's 2026-07-31 reading.** All **9** SYNC durables are
  `deliver_policy=all` with no `opt_start_seq`; **three sit at exactly 10,000 pending** — pinned at
  `MaxMsgsPerSubject`, i.e. ~700× amplification for a 14-key world, not 146×. Stream: 556,687 msgs /
  683 MB / 116 subjects.
- **`RunDurableConsumer` census CONFIRMS the design:** 9 production callers + 10 test constructions, none
  setting `StartSeq`; the zero value leaves every one on `DeliverAllPolicy` verbatim.
- **The `DELETE` grant is present** — `natsauth.go:362` `$JS.API.CONSUMER.DELETE.SYNC.<durable>`. No ACL change.
- **Vendor premises re-verified at the pins** (`nats-server v2.14.0`, `nats.go v1.52.0`):
  `checkNewConsumerConfig` refuses both `deliver policy can not be updated` (`consumer.go:2435`) and
  `start sequence can not be updated` (`:2438`) ⇒ delete-then-create is mandatory, not stylistic.
  `OptStartSeq` / `DeliverByStartSequencePolicy` exist (`jetstream/consumer_config.go:129,:423`).

**Two falsified body claims — amended in this fire's commits, per the body-stays-true rule:**

- **§1.3** says `cmd/facet`'s `engineManager.Acquire` "mints a fresh device id per engine build
  (`enginemanager.go:117`)". **Falsified:** `0b6879dc` (2026-08-01) shipped *"one durable per identity, not
  one per engine build"*; `enginemanager.go:128` now reads the host's "stable, store-persisted" id. §5
  already narrows this; the body must say so where it stands.
- **§5** says the orphan-expiry backstop "is sequenced behind" this fork. **Falsified by the later ratified
  design** `edge-sync-orphan-expiry-design.md`, whose §7 proves reaping is free under *either* branch and
  states "**This row is unblocked**". Amend; do not leave a wrong sequencing instruction for the next fire.

**Dossiers.** `refractor.md` (12 entries) and `substrate.md` (6) carry dossiers; the ones that bind here:
*pipeline state lifetimes*, *consumer settlement*, *delivery vs. projection axes*, *latch timing*,
*JetStream filter narrowing*, *server-state memoization*, *vendor claims*, *reopen/retry loop closure*.
**`docs/components/edge.md` has NO dossier section** — this fire's close pass creates it.

**Standing checklist (all six bind here; 1, 3 and 4 are the sharp ones):**
1. *New state needs a LIFETIME* — `StartSeq` is per-session derived, but Fire 4's per-rule gate is a real
   accumulated set: write its table (armed / satisfied / re-armed on a second hydrate / crash).
2. *Every census is a premise* — done above.
3. *A negative test needs its positive vector proven first* — the "<49 frames" assertion is worthless unless
   the same harness first proves ~200 stale frames DO arrive without the fix.
4. *Removal needs a transport AND an observer* — the ack floor is being removed as resume authority; its
   silent second job (poison disposal) is the observer nobody had.
5. *One deterministic key, one writer* — delete-then-create requires the caller own the durable; state it in
   the seam's doc comment (§7's sharp edge).
6. *Precedent may carry debt* — `SetSyncFirstSeq` is the mirror; verify its fresh-fetch posture before copying.

### 6. Adjacent finds

- **`delete` envelopes carry no `Lens`** (`natssubject.go:332-348`) while `upsert`/`keyset` do. Absorbed into
  Inc 3 if it costs nothing there; otherwise this run's next unit.
- **`docs/components/edge.md` has no "Review keeps catching" dossier.** Absorbed — created at close.
- **`shell.mjs` already sets `inactive_threshold`** — the browser-side constant the orphan-expiry design
  wants to make a stream-level policy. Informational; that row is a separate ratified item, unblocked.

### 7. Non-goals (drift fence)

`Rehydrate`, `personal.syncgap`, `armHydrateGate`'s existence, SYNC retention (`MaxAge` /
`MaxMsgsPerSubject`), per-key subjects, compaction, any ACL/natsperm change, any `docs/contracts/*` change,
and the orphan-expiry `InactiveThreshold` work (its own ratified design).

### Scope-diff gate — PASSED

Parts 2–4 trace item-by-item to part 1; the brief **narrows** (it fixes the design's rotted anchors and
tightens `L`), never widens, and substitutes no adjacent mechanism. Declared dependencies re-verified both
ways: the ACL grant and `DeleteStreamConsumer` are load-bearing and present; the "sequenced behind
cold-signin" dependency the design asserts on the orphan row is **not** load-bearing and is dropped (the
orphan design's §7 refutes it). No frozen contract is touched — re-grepped `docs/contracts/*` for
`lattice.sync` / `personal.hydrate` / `hydrationComplete` / `deltaEnvelope`: zero hits.

### Build checkpoint (2026-08-11)

**Worktree:** `/Users/andrewsolgan/Documents/GitHub/lattice-wt-edge-coldsignin`
(branch `fire/edge-cold-signin-position`).

**Landing shape: each increment lands on `main` as it goes green** — not held to one merge. The invariant
that keeps `main` correct at every boundary is that **each increment is behaviour-preserving at its zero
value**: Fire 1's `SyncStartSeq` is inert until a host reads it; Fire 2's `StartSeq == 0` is `DeliverAll`
verbatim, so the 9 production `RunDurableConsumer` callers that never set it are untouched; Fire 4's `Lens`
on the marker degrades to today's first-marker gate when absent; Fire 3's browser host simply omits the
field until it sends it. No boundary leaves a half-wired path.

**Done:** Fire 1 — `personal.hydrate` returns the SYNC position (`6bdf9bcb`).

**Next:** Fire 2 (the acceptance bar — Go host consumes it, cursor becomes the single resume authority,
`Term` advances the cursor), then Fire 4 (one hydration marker per hydrate), then Fire 3 (browser parity).
