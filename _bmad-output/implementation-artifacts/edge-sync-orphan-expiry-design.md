# Edge sync orphans — let the server expire what no client can name

**Status: 📐 awaiting-Andrew (ratification)** — designed 2026-08-02 by the Designer fire against the
[lattice lane](../planning-artifacts/backlog/lattice.md) row *"[Edge] An orphan a purge cannot reap has
no server-side backstop"*.

## For Andrew

- **What it does.** A device's SYNC durable is reaped today only by the *client's own* sign-out purge
  (`cmd/facet`'s `engineManager.Purge`) — which cannot run when the credential was revoked (the auth
  callout correctly refuses the connection) or when the host crashed. This design stops hand-rolling a
  reaper and instead makes the **NATS server** the expiry authority: the SYNC stream declares
  `ConsumerLimits.InactiveThreshold`, so **every** consumer on it — Go host, browser tab, and any future
  host — inherits a bounded lifetime it cannot opt out of. A one-shot backfill converts the consumers
  that predate the policy. A second, structurally identical leak the same failure modes cause — the
  `personal-lens-interest` registration doc, which **nothing** garbage-collects today (`cmd/loupe/edge.go:544`
  says so in-tree) — is closed the same way, with the now-expiring durable as its liveness authority.
- **No architectural fork.** No Gateway / read-path-auth / Vault / multi-cell / HA-NATS surface is touched.
- **Frozen contracts: NO CHANGE.** Nothing under `docs/contracts/*` describes the SYNC stream, the edge
  durable, or the `personal.*` control ops (grepped — the only `durable consumer` hits are Loom's
  `loom-state` outbox relay and the Processor's `processor-main`). No uncommitted contract edit is staged.
- **The board's "seq behind cold-signin — its fork sets the value" premise is FALSE, and §7 proves it.**
  The threshold is derived from the SYNC stream's own retention horizon, not from the cold-sign-in fork:
  past `MaxAge`, a *surviving* durable and a *fresh* one deliver the **identical** message set, so reaping
  at `MaxAge + margin` costs exactly nothing under either branch of that fork. **This row is unblocked.**
- **The gate against the next author is structural, not a lint.** A host that omits a threshold inherits
  one; a host that asks for a longer one is refused by the server with
  `JSConsumerInactiveThresholdExcess` (`consumer.go:843`). Omission fails closed at the substrate, so no
  `lint-conventions` rule is needed or proposed.
- **Adversarial pass: run in this fire, findings folded (§10).** This design's own pre-build gate is
  discharged — it is build-ready on ratification.

---

## 1. Problem

### 1.1 What leaks, and why the existing reaper cannot catch it

`internal/edge/sync` binds a device to a **stable, durable** pull consumer on the `SYNC` stream, named
`edge-sync-<identityId>-<deviceId>` (`sync.go:47`, `DurableName`). The name is stable on purpose — the
Manager wants JetStream's native ack-floor resume across restarts rather than a full replay every boot
(`sync.go:38-46`).

Exactly one thing deletes it: `cmd/facet`'s `engineManager.Purge` → `conn.DeleteStreamConsumer`
(`enginemanager.go:333-338`). That path needs a **live NATS connection minted for the identity being
purged**. It therefore cannot run in the two cases that matter:

1. **A revoked credential.** `Purge` mints a token and connects (`enginemanager.go:309/329`); the auth
   callout refuses the connection — correctly — and every failure there is logged and swallowed ("a purge
   must never fail the revocation", `enginemanager.go:274`). The durable survives.
2. **A crashed host.** Nothing runs at all.

A stranded durable is unnameable afterwards. `Purge` deletes the local mirror, and the device id lives
*in* that mirror (`cmd/facet/deviceid.go`, `facet.deviceId`), so the next sign-in mints a **fresh
NanoID** and a **fresh durable name**. No client will ever again derive the orphan's name.

The consequence is measured, not hypothetical: 74 orphaned SYNC durables holding ~740k phantom pending
were swept by hand on 2026-08-01 (`6c0a08c7`), after the per-build-device-id bug that produced them was
fixed (`0b6879dc`). That fix removed the *fast* producer of orphans. It did not add a backstop, so the
slow producers — revocation and crash — still accrue forever.

### 1.2 The second leak, same cause, no reaper at all

The per-device **Interest Set** registration (`personal-lens-interest` KV, key `<identityId>.<deviceId>`,
`internal/refractor/personalinterest/interest.go:24-49`) has the same lifecycle and a **worse** story:
`Deregister` exists (`interest.go:195`, wired as the `personal.deregister` control op) and **no caller in
the tree invokes it** — `Purge` reaps the durable and leaves the registration behind. Loupe already
documents the gap: *"nothing garbage-collects a registration, so a device that vanished without a clean
deregister keeps its row forever"* (`cmd/loupe/edge.go:543-544`).

So the leak rate is **one registration doc per sign-out→sign-in cycle**, not one per real device: the
purge destroys the mirror, the new sign-in mints a new device id, and the old key is immortal. Effects,
in severity order:

- `personalinterest.IsInterested` unions **all** of an identity's registered devices to build the
  server-side push filter (`interest.go:208-216`); a dead device with an empty filter admits everything,
  so the union only ever **widens**. Fail-open in the bandwidth direction — a cost defect, not an
  authorization one (the D1 read gate is elsewhere and untouched).
- Loupe's Edge fleet roster grows monotonically with dead rows.
- The bucket grows without bound.

### 1.3 What the platform already does, and why it doesn't transfer

`internal/refractor/health/durable_janitor.go` (shipped `73050cc5`) reaps orphaned **lens** durables on
the Core KV stream. Its safety argument is the good one and it is worth restating: *deleting a durable
takes the ack floor with it, so never reason from a SET of live lenses — judge each candidate on its own
authoritative read of the one key that decides it* (`vtx.meta.<id>` absent or `isDeleted`).

**That precedent does not transfer to the SYNC plane, and copying it would be the trap.** A lens durable's
id is a `vtx.meta.<NanoID>` the platform wrote; there is one key whose read is authoritative. A device id
is **client-minted, stored only in the client's own mirror, and never written to Core KV**. The one
server-side artifact that names it — the interest registration (§1.2) — leaks in exactly the same failure
modes, so it is not a discriminator: after a revocation, *both* the durable and the doc survive, and
reading one to judge the other is circular. There is no key to read.

## 2. Intent

Stop building a reaper. **Make expiry a property of the stream, enforced by the server**, so no
client-side liveness inference is needed at all — then use that now-authoritative expiry to reconcile the
one artifact (§1.2) that has no expiry of its own.

## 3. The mechanism, grounded in the pinned vendor

Everything below is verified against **our pin — `nats-server/v2 v2.14.0`, `nats.go v1.52.0`**
(`go.mod:10-11`; `docs/vendors.md` NATS row), reading the server source, not the docs site (the published
consumer page does not cover `inactive_threshold` at all).

| Claim | Authority (pinned source) |
|---|---|
| `InactiveThreshold` applies to **durables** when explicitly set | `nats.go/jetstream/consumer_config.go:212-218` — *"Durable consumers will not be cleaned up by default, but if InactiveThreshold is set, they will be."* |
| For a **pull** consumer the clock measures **absence of pull requests**, not absence of messages | `consumer_config.go:219-222`; enforced at `nats-server/server/consumer.go:1752-1755` — *"Pull consumer. We run the dtmr all the time for this one."* → `deleteNotActive` after `dthresh` |
| A consumer with **un-acked in-flight** messages is protected | `consumer.go:2175-2210` — the timer is pushed out to `max(pending.Timestamp + ackWait) + dthresh`; an ack pulls it back to `dthresh` |
| A consumer with **waiting pull requests** is protected | `consumer.go:2155-2172` — `elapsed < dthresh` resets by delta; `checkWaitingForInterest()` resets in full |
| A stream may declare `ConsumerLimits.InactiveThreshold`, which a consumer **inherits when it declares none** | `consumer.go:662-666` — `if config.InactiveThreshold == 0 { config.InactiveThreshold = streamCfg.ConsumerLimits.InactiveThreshold }`; the value is stamped into the consumer's stored config |
| The same stream limit is a **ceiling**: a consumer asking for more is refused | `consumer.go:843-844` → `NewJSConsumerInactiveThresholdExcessError` |
| `InactiveThreshold` is **updatable** on an existing consumer | `consumer.go:2429-2483` `checkNewConsumerConfig` enumerates every non-updatable field (deliver policy, start seq/time, ack policy, replay policy, heartbeat, flow control, deliver subject, max waiting) — `InactiveThreshold` is not among them |
| A stream limit does **NOT** retro-apply to already-created consumers | `stream.go:2417-2441` — a limits change only *validates* that no existing consumer **exceeds** the new limit (`ccfg.InactiveThreshold > cfg.ConsumerLimits.InactiveThreshold`). A consumer sitting at `0` does not exceed, passes, and keeps `0` forever |

The last row is the whole reason this design has two increments rather than one.

## 4. The shape

### 4.1 Read path / write path / P-invariants

Neither is touched. This changes **JetStream consumer lifetime policy and one operational KV bucket**
(`personal-lens-interest` — explicitly operational state under P1, as `cmd/loupe/edge.go:535` already
records). No Core KV read, no Core KV write, no lens, no operation, no orchestration. P2 and P5 are
untouched; no `contextHint` or Starlark read posture is in scope.

### 4.2 Ownership

Refractor owns the SYNC stream (`internal/refractor/adapter/natssubject.go`, `ensureSyncStream`) and the
`personal-lens-interest` bucket handle (`cmd/refractor/main.go:283`). Both increments live in Refractor,
the second beside the janitor it mirrors (`internal/refractor/health/`).

### 4.3 The value, derived rather than chosen

`InactiveThreshold = syncStreamMaxAge + syncDurableReapMargin` — today **24h + 1h = 25h**, expressed in
code as that sum against the existing `syncStreamMaxAge` constant (`natssubject.go:112`), never as a bare
literal.

The derivation, which §7 turns into the proof that this is free:

> An ack floor is worth preserving only while a resume from it can deliver something a fresh consumer
> would not. Past the stream's retention horizon it cannot, so the durable's remaining value is zero.

`syncStreamMaxMsgsPerSubject = 10_000` (`natssubject.go:121`) can prune a busy actor's subject *earlier*
than `MaxAge`, which only strengthens the argument. The 1h margin is slack for clock skew and for a node
that reconnects right at the boundary.

## 5. Increments (the Steward's build order)

Each is independently shippable and green.

### Inc 1 — the SYNC stream declares the policy

- `substrate.StreamSpec` (`internal/substrate/stream.go:19-31`) gains
  `ConsumerInactiveThreshold time.Duration`, mapped to `jetstream.StreamConfig.ConsumerLimits.InactiveThreshold`
  in `EnsureStream`. Zero keeps today's behaviour, so no other `EnsureStream` caller changes.
- `ensureSyncStream` sets it on **both** of its `EnsureStream` calls (`natssubject.go:139` and `:141-146`
  — the subject-union branch and the append branch; missing one would leave the policy off on the common
  restart path).
- **Tests** (`internal/refractor/adapter`, `natsfixture` embedded server): create a durable on SYNC with
  no threshold and assert `ConsumerInfo.Config.InactiveThreshold == 25h` (inheritance is stamped);
  create one asking for 48h and assert it is refused; assert an existing 30-minute consumer (the browser
  shell's) is accepted unchanged.

After Inc 1 every **newly created or updated** consumer on SYNC self-expires. That covers every live
device — a live device re-issues `CreateOrUpdateConsumer` on its next attach (`substrate/consumer.go:149`)
— and it covers the browser host, which already sets its own 30 minutes
(`internal/edge/browser/shell/shell.mjs:45`) and stays comfortably under the ceiling.

### Inc 2 — backfill the consumers that predate the policy

Per §3's last row, a consumer already sitting at `InactiveThreshold: 0` never inherits. An **orphan** by
definition never re-attaches, so the very population this design exists for is the one Inc 1 cannot reach.

A one-pass sweep at Refractor boot, after the SYNC stream ensure: list SYNC's consumers; for each whose
`Config.InactiveThreshold == 0`, re-issue `CreateOrUpdateConsumer` with **its own read-back config plus
the threshold**.

The design property that makes this cheap to reason about, and the reason it is not a second janitor:

> **The sweep never decides whether a durable is an orphan.** It only makes every durable *capable of
> expiring* and lets the server decide. Updating a live consumer's threshold is a no-op for that consumer
> — a live pull consumer never goes inactive. There is no delete verdict, so there is no ack floor to
> lose, and none of `durable_janitor.go`'s enumeration-trust hazard applies: a short listing means fewer
> get fixed this pass, and the next boot catches them.

Notes for the builder: copy the read-back `ConsumerConfig` verbatim and change only the one field, or the
update trips `checkNewConsumerConfig`'s non-updatable set (§3). Boot-once is sufficient — nothing after
Inc 1 can create a zero-threshold consumer on SYNC — and a transient failure simply retries next boot; do
**not** add a ticker for a fixed, draining population. Add a substrate helper alongside
`DeleteOrphanDurables` (`internal/substrate/subscribe.go:268`) rather than reaching for `c.js` from
`internal/refractor`.

### Inc 3 — the registration follows the durable

Two halves, mirroring the durable's own story (an explicit reap on the clean path, a backstop for the rest):

**3a — the clean path.** `engineManager.Purge` already opens a connection and derives the durable name to
delete it (`enginemanager.go:333-338`); it issues `personal.deregister` on the same connection, in the
same swallow-every-failure posture. This is the caller `Deregister` has been waiting for since it shipped.

**3b — the backstop.** A reconciler in `internal/refractor/health/`, mirroring `DurableJanitor`'s
structure exactly: enumerate `personal-lens-interest` keys; for each, judge it on **its own** authoritative
read of the one artifact that decides it — `ConsumerInfo(SYNC, DurableName(identityId, deviceId))`.

- `ErrConsumerNotFound` **and** `registeredAt` older than a 1h grace → `Deregister`.
- A found consumer, an unparseable doc, a malformed key, or **any** read error → keep.

The grace exists only for the birth race: `hydrate()` calls `personal.register` before
`RunDurableConsumer` creates the durable (`sync.go:355-357` → `:207`), a millisecond-wide window that 1h
covers with absurd margin. A *live* device is protected by the probe itself, not by the grace.

This increment is **sequenced behind Inc 1+2 and depends on them**: before the durable has an authoritative
expiry, "durable absent" means "someone deleted it", not "this device is gone", and 3b would be inferring
liveness from an artifact with no lifetime — the circularity §1.3 rejects. After Inc 1+2 it is a real
verdict.

**Operator-visible change, called out so it is not mistaken for drift:** Loupe's Edge fleet roster loses
its dead rows. It already models "registered but no durable" as a distinct state
(`cmd/loupe/edge.go:127-150`, `consumerLookup`'s deliberate three answers), and its own doc comment
(`:543-544`) names the missing GC this closes. The roster becomes what its header claims — a registration
roster of devices that still exist.

## 6. Alternatives considered

**A. Plumb `InactiveThreshold` per consumer** — add it to `substrate.DurableConsumerConfig`, then
`transport.ConsumerConfig`, then `sync.Config`. **Rejected on three counts.** It is *default-open*: a host
that forgets the field gets no expiry and nothing errors — the exact omission-grants shape §2 of the
Designer skill forbids, where the stream-limit shape makes omission inherit. It reaches only the Go host,
leaving the browser's policy a second, independently-driftable constant. And it edits the **same seam**
`edge-cold-signin-delivery-position-design.md` is already changing (that design adds a delivery-position
field to `substrate/consumer.go:142`'s hardcoded `DeliverAllPolicy` and to the transport seam) — see §9.
The stream-level shape needs neither file.

**B. A delete-verdict janitor mirroring `DurableJanitor`** — sweep SYNC, decide orphan-ness, delete.
**Rejected:** §1.3 — there is no authoritative key for a client-minted device id, so the predicate would
be a set difference or a timestamp heuristic, and the failure mode is a deleted ack floor. The server
already implements exactly this decision, correctly, with the pending/waiting protections §3 tabulates.
Building it again in Go is strictly worse.

**C. Per-key TTL on the `personal-lens-interest` bucket** (the platform already uses per-key message TTL —
`substrate.KVPutWithTTL`, Contract #4 §4.3's idempotency tracker), re-armed on registration.
**Rejected, and the reason is a mechanism I opened rather than assumed:** the TTL would have to be re-armed
by something that recurs, and `registerInterest` is called **only from `hydrate()`** — a warm resume
returns from `ensureFresh` without it (`sync.go:249-275`). A device connected continuously for weeks would
expire its own registration mid-flight. Making it recur means inventing a heartbeat; 3b's per-key probe
needs no new traffic and reuses the janitor pattern already in the tree.

**D. Do nothing for the registration; ship Inc 1+2 only.** Defensible if Andrew wants the smaller
landing, and the increments are ordered so it is a clean stopping point. **Not recommended:** §1.2 shows
the leak rate is per sign-out cycle rather than per device, 3a is a handful of lines on a call path that
is already doing the symmetric thing for the durable, and leaving `Deregister` callerless keeps a shipped
control op dead.

## 7. Why reaping is free — the load-bearing argument

The board row parks this item behind `edge-cold-signin-delivery-position-design.md` because "its fork sets
the value". It does not, and the proof is short.

Take a node inactive for longer than the threshold. Its durable is reaped. On return it re-creates the
durable (its Gateway grant permits exactly that — `$JS.API.CONSUMER.CREATE.SYNC.<durable>.<subject>`,
`internal/gateway/natsauth/natsauth.go:359`) with `DeliverAllPolicy`, and receives **every message still
retained on its subject**.

Now take the same node with its durable *surviving*, ack floor at sequence `S`. It has acked nothing since
it went away, so everything after `S` is unacked; and everything at or before `S` that is still retained
was already acked, hence not redelivered. Because the node was away longer than `MaxAge`, retention has
pruned everything up to and including `S`. So the surviving durable also delivers **every message still
retained on its subject** — the identical set.

Past the retention horizon the two paths are indistinguishable. Setting the threshold at `MaxAge + margin`
preserves every resume that could have delivered anything and reaps only floors that had become empty.
This holds under **both** branches of the cold-sign-in fork: under the recommended reposition branch a
lost floor costs even less (the node starts at the hydration sequence rather than at the beginning), and
under the compaction branch `MaxAge` would be dropped, at which point §4.3's derivation must be re-read —
which is exactly why the constant is written as a derivation from `syncStreamMaxAge` and not as `25h`.

**Therefore the row is unblocked and the `seq behind` marker should be cleared.**

## 8. What the ack floor was silently carrying

Reaping a durable removes a component, and a removed component carries obligations nobody wrote down.
Enumerated rather than intuited:

1. **Suppressing redelivery of already-applied deltas.** Cost only. `handle` is contractually idempotent
   and applies last-writer-wins by revision (`sync.go:466-468`, `ApplyUpsert`/`ApplyDelete`), so a replay
   cannot regress state.
2. **Keeping the host's boot gate honest.** Already covered, independently of this design:
   `armHydrateGate`/`hydrationGateReady` (`sync.go:382-407`) ignore any `hydrationComplete` marker older
   than the revision the current `personal.hydrate` targeted.
3. **Cursor monotonicity.** `m.store.SetCursor(d.Sequence)` is unconditional (`sync.go:526`), so a replay
   transiently walks the cursor backwards before ending at the highest delivered sequence
   (`DeliverAll` is ascending). A crash mid-replay leaves a lower cursor, which makes the next
   `gapped()` check *more* likely to hydrate — the conservative direction. Pre-existing on every path
   that loses a durable (including today's `Purge`); not introduced here and not worsened.
4. **The Gateway grant.** Unchanged: the per-identity permission set already carries `CONSUMER.CREATE`
   for the device's own durable name, so re-creation after a reap needs no auth change.
5. **Loupe's fleet view.** Handled explicitly in Inc 3.

## 9. Reconciliation with the existing mental model

**"Didn't we already fix the orphan problem?"** Two adjacent things shipped 2026-08-01 and neither is
this. `0b6879dc` stopped *producing* orphans at a fast rate (one durable per engine build → one per
device) and made `Purge` reap on clean sign-out. `6c0a08c7` swept the 74 that had accumulated. Both are
producer-side and client-side; the two failure modes in §1.1 have never had a backstop, and the manual
sweep is not one.

**"Doesn't `DurableJanitor` cover this?"** No — it is scoped to `refractor-<NanoID>` names on the **Core
KV** stream (`subjects.ParseLensDurable`), and §1.3 explains why its key-read design cannot be pointed at
SYNC.

**"Does this collide with the in-flight edge design?"** `edge-cold-signin-delivery-position-design.md`
(📐 awaiting-Andrew) is the only other in-flight design touching this plane; the other four
(`structural-pause-recovery`, `lens-projection-divergence-audit`, `full-engine-grouping-key-reduction`,
`client-ceremony-op-descriptors`) are elsewhere. It changes the **consumer config seam**
(`substrate/consumer.go`'s `DeliverPolicy`, `transport.ConsumerConfig`); this changes the **stream config**
(`substrate/stream.go`, `ensureSyncStream`). Disjoint files, and §6-A records that avoiding that seam was
a reason for the recommendation rather than an accident. The two compose: repositioning makes a reaped
durable cheaper still.

**"Does this introduce new state?"** None. No new key, bucket, vertex, aspect, link, or lens. Inc 1 adds
one field to an existing stream config; Inc 2 writes only to consumer configs that already exist; Inc 3
deletes existing keys.

## 10. Adversarial pass (run in this fire — gate discharged)

Walked §2's reflex list against the draft, one at a time. Five findings, all folded above.

1. **"Verify the mechanism can BE reshaped."** The draft's premise — that `InactiveThreshold` reaps
   durables and that a stream limit is inherited — was an unopened hypothesis. Opening
   `nats-server@v2.14.0` produced §3's table and one **design-changing** fact: a stream limit does **not**
   retro-apply (`stream.go:2417-2441`). Without that read, Inc 2 would not exist and the design would have
   shipped a fix that provably cannot reach the orphan population it was written for.
2. **"A precedent can serve a different job."** The first shape was "mirror `DurableJanitor` onto SYNC" —
   same word (janitor), different job. A lens id is platform-written and readable; a device id is
   client-minted and unreadable. §1.3 now states the non-transfer explicitly, and §6-B records the
   rejection.
3. **"A removed component's silent obligations."** The draft asserted reaping was safe. §8 turns that into
   a checklist and finds the unconditional `SetCursor` (`sync.go:526`) — a real, pre-existing cursor
   regression on any durable loss. Conservative in direction, so it does not block, but it was found by
   the checklist, not by intuition.
4. **"Right-size to observed demand."** The draft made Inc 2 a 30-minute ticker copied from
   `DurableJanitor`. The population it drains is fixed and cannot grow after Inc 1, so a ticker is pure
   ceremony; boot-once with next-boot retry replaced it.
5. **"Check for a false fork / falsify a block premise."** The row's `seq behind cold-signin` was taken at
   face value in the draft. §7 falsifies it, and the increment order changed as a result — this ships now
   rather than after another design's ratification.

Two further checks ran clean: the **default direction** (omission inherits, excess is refused — fail-closed
at the server, §3), and **dead scaffolding** (every increment has a live consumer today: 74 measured
orphans, a callerless `Deregister`, and a monotonically growing bucket Loupe already renders).

## 11. Test strategy

- **Inc 1** — `internal/refractor/adapter`, embedded NATS via `natsfixture`: inheritance is stamped; an
  over-ceiling request is refused; the browser's 30-minute value is accepted; both `ensureSyncStream`
  branches (subject already present / subject appended) set the limit.
- **Inc 2** — a consumer created on a SYNC stream **without** the limit, then the limit applied, then the
  backfill: assert the consumer's threshold moved off zero, its `DeliverPolicy`/ack floor survived, and a
  second pass is a no-op. Assert a listing error leaves everything untouched.
- **Inc 3a** — extend `cmd/facet`'s existing `TestEngineManager_PurgeReapsTheSyncDurable`
  (`deviceid_test.go:137`) to assert the registration is gone too.
- **Inc 3b** — table test mirroring `durable_janitor_internal_test.go`: durable present → keep; absent and
  within grace → keep; absent and past grace → deregister; `ConsumerInfo` error → keep; malformed key or
  doc → keep.
- **Ephemeral-consumer note for the reviewer:** the stream limit is also the *default* for ephemerals on
  SYNC, so an ephemeral there would live 25h instead of the server's 5s. No path in the tree creates one
  (every consumer goes through substrate's durable primitives — grepped), so nothing regresses; a future
  one must set its own threshold explicitly. Called out rather than gated: there is no bare idiom in the
  tree to default-deny, so a lint rule would guard a hypothetical.

## 12. Risks

- **A live device disconnected longer than 25h loses its ack floor.** By §7 this costs nothing: it was
  going to re-hydrate anyway. The residual is that it re-creates the durable, one extra API call.
- **A clock-skewed or paused NATS server could expire early.** The 1h margin covers ordinary skew; the
  worst case is the previous bullet.
- **Inc 2's write-back trips a non-updatable field** if a builder constructs a fresh `ConsumerConfig`
  instead of copying the read-back one. Called out in §5 and pinned by the Inc 2 test's ack-floor
  assertion.
- **Inc 3b deregisters a device mid-birth** if the grace is removed or the probe is inverted. Pinned by
  the within-grace table case.
