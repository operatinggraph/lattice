# Edge sync orphans — let the server expire what no client can name

**Status: ✅ RATIFIED 2026-08-06 (Winston, under delegated authority)** — designed 2026-08-02 by the Designer fire against the
[lattice lane](../planning-artifacts/backlog/lattice.md) row *"[Edge] An orphan a purge cannot reap has
no server-side backstop"*.

## Ratification (Winston, 2026-08-06 — delegated by Andrew)

Andrew delegated this class of decision in the ratify session: *"do what is right long term, do NOT make
decisions based on how many lines of code need to be changed."*

**Ratified as designed — the mechanism is the substrate's own.** A durable that no client can name again is
unreclaimable state, and the two paths that produce one are both un-fixable client-side: a revoked
credential *correctly* fails the sign-out reap (the auth callout refuses its connection), and a crashed
host never reaps at all. A server-side backstop is the only complete answer, and `InactiveThreshold` is
NATS's own — re-verified this session in the pinned `nats.go@v1.52.0`
(`jetstream/consumer_config.go:212`). Building a janitor beside it would be inventing what the substrate
already offers, which is the wrong shape regardless of size.

**The demand is not hypothetical**: 74 orphaned SYNC durables had to be swept by hand on 2026-08-01.

**One citation corrected at ratification.** §1.1 credits commit `6c0a08c7` for that sweep. That commit is
*"docs(board): the orphan-drain row carries its verified precondition"* — the sweep itself is `26accab7`,
*"docs(board): the 74 orphaned SYNC durables are swept."* The fact is true; the pointer was wrong.

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

---

## 13. Fire brief (build note, 2026-08-11)

Compiled by the Lattice Steward at selection, from three read-only scouts over the Inc 1 / Inc 2 / Inc 3
surfaces. The whole ratified plan (Inc 1+2+3) is **one fire**.

### 1. Scope sentence

Verbatim, §2: *"Stop building a reaper. **Make expiry a property of the stream, enforced by the server**, so
no client-side liveness inference is needed at all — then use that now-authoritative expiry to reconcile the
one artifact (§1.2) that has no expiry of its own."*

**Green bar** = §11's five test groups, all green, plus the whole-repo gates.

### 2. Verified touch-list (checked live, this fire)

**Most of §5's `file:line` citations had rotted** — the facts hold, the anchors moved. Corrected here; the
design body's own numbers are left as written (they are leads, and §5's prose is still true).

| Site | Design said | Live now | What changes |
|---|---|---|---|
| `internal/substrate/stream.go` | `:19-31` `StreamSpec` | `:19-31` ✅, `EnsureStream` `:38-55` | + `ConsumerInactiveThreshold`, mapped to `jetstream.StreamConfig.ConsumerLimits.InactiveThreshold` |
| `internal/refractor/adapter/natssubject.go` | `:112` maxAge, `:139`,`:141-146` | ✅ all three exact | + `syncDurableReapMargin`, + exported threshold, set on both `EnsureStream` calls |
| `internal/substrate/subscribe.go` | `DeleteOrphanDurables` `:268` | **`:324-386`** | + `BackfillConsumerInactiveThreshold` beside it |
| `cmd/refractor/main.go` | interest KV `:283` | **`:487-491`**; `syncStream := r.Into.Stream` **`:1414`** | call the backfill once per distinct stream; start the Inc 3b reconciler |
| `internal/refractor/health/durable_janitor.go` | precedent | struct `:74-83`, ctor `:89-108`, `Run` `:112-130`, `sweep` `:133-161`, `lensIsGone` `:166-183` | mirrored, not edited |
| `internal/edge/sync/sync.go` | `DurableName` `:47` | **`:48-50`**; `registerInterest` **`:604-621`**, called **`:551-552`**; `RunDurableConsumer` **`:336-342`** | `DurableName` delegates to `subjects` |
| `cmd/facet/enginemanager.go` | `Purge` `:274/:309/:329/:333-338` | **`Purge` `:223-262`, `reapSyncDurable` `:281-347`** (mint `:307`, connect `:318`, durable `:341`, delete `:342`) | + `personal.deregister` on the same connection |
| `cmd/facet/deviceid_test.go` | `:137` | ✅ `:137-181` | extended to assert the registration is gone |
| `cmd/loupe/edge.go` | `consumerLookup` `:127-150`, GC note `:535-545` | **`:146-156`**, `readConsumer` **`:493-509`**, `edgeSyncDurable` **`:195-197`**, doc note near `:543` | the hand-copied durable name delegates; stale GC comment retires |
| `internal/refractor/subjects/` | *(not named)* | `subjects.go`, `lens_durable.go` | **new** `EdgeSyncDurable` — see find #1 |
| `internal/refractor/personalinterest/interest.go` | *(not named)* | `registrationDoc` `:28-41` (`registeredAt` RFC3339), `Register` `:57`, `Deregister` `:195-204` | read-only for this fire |

Two facts the design asserts, re-verified live rather than trusted:

- **The browser stays under the ceiling.** `internal/edge/browser/shell/shell.mjs:49` —
  `defaultInactiveThresholdMs = 30 * 60 * 1_000`, sent as `inactive_threshold` at `:222`. 30 min ≪ 25 h. ✅
- **No path in the tree creates an ephemeral on SYNC.** Every consumer goes through substrate's durable
  primitives or the shell's explicit config; the only bare `inactive_threshold` literals are inside the
  vendored `nats.js.mjs`. ✅ §11's ephemeral note stands as written.

### 3. Precedents to mirror

| Edit | Mirror |
|---|---|
| `BackfillConsumerInactiveThreshold` | `DeleteOrphanDurables` (`subscribe.go:324-386`) — drain the listing *fully* before per-item I/O; list error fails the call, per-item error logs `Warn` and continues; return the names acted on. **But take a STREAM name, not a bucket**: that helper and `PruneStaleDurables` both prepend `KV_`; SYNC is a plain stream, not a KV backing store. |
| Reading back a consumer config | `ConsumerSupervisor.consumerInfo` (`consumer_supervisor.go:568-584`) → `cons.Info(ctx)`; write back with `c.js.CreateOrUpdateConsumer` (`:611`) |
| Inc 3b reconciler | `DurableJanitor` end to end (`durable_janitor.go`) — grace-window delay, then sweep, then ticker; judge each candidate on **its own** authoritative read; every error keeps |
| Inc 3b test | `durable_janitor_internal_test.go:42-91` — `makeConsumer` / `quietLogger` / call `sweep(ctx)` directly and `require.ElementsMatch` the acted-on set |
| Adapter test setup | `natssubject_test.go:21-35` `startSyncServer` — `natsfixture.Server(t)` → `substrate.Wrap` → `jetstream.New` |
| Inc 3a test | `deviceid_test.go:137-181` extended in place |

### 4. Increment order + runnable green checks

**Inc 1 — the stream declares the policy.**
`StreamSpec.ConsumerInactiveThreshold` → `ConsumerLimits.InactiveThreshold`; zero keeps today's behaviour
(verified: all six other `EnsureStream` call sites leave it unset). `syncDurableReapMargin = 1 * time.Hour`
and an **exported** `SyncConsumerInactiveThreshold = syncStreamMaxAge + syncDurableReapMargin` in the adapter
package, set on **both** `natssubject.go:139` and `:141-146`.

```bash
go test ./internal/refractor/adapter/ ./internal/substrate/ -run 'Sync|Stream' -count=1
```

**Inc 2 — backfill what predates the policy.**
`(*Conn).BackfillConsumerInactiveThreshold(ctx, streamName string, threshold time.Duration, logger *slog.Logger) ([]string, error)`:
list consumer names, `Info` each, and for `Config.InactiveThreshold == 0` write back **the read-back config
with that one field changed** — never a fresh struct (§12; substrate dossier: a server-immutable field
refuses an update).

```bash
go test ./internal/substrate/ -run Backfill -count=1
```

**Inc 3a — the clean path deregisters too.**
In `reapSyncDurable` (`enginemanager.go:281-347`), after the durable delete, issue `personal.deregister` on
the **same** connection, in the same swallow-every-failure posture. The seam: the control plane binds the
actor from the verified `Lattice-Actor` header (`control/service.go:930-941` —
`controlauth.BareIdentityID(actor)`, and a mismatched `identityId` in the body is refused), and
`reapSyncDurable` already mints exactly that identity's token at `:307`. Subject is
`controlwire.ControlSubject("personal", "deregister")`.

```bash
go test ./cmd/facet/ -run PurgeReaps -count=1
```

**Inc 3b — the backstop reconciler.**
`internal/refractor/health/`, mirroring `DurableJanitor`. Enumerate `personal-lens-interest`; probe
`Consumer(syncStream, subjects.EdgeSyncDurable(id, dev))`. `ErrConsumerNotFound` **and** `registeredAt`
older than a **1 h** grace → `Deregister`. Found / unparseable doc / malformed key / **any** read error →
keep.

```bash
go test ./internal/refractor/health/ ./cmd/loupe/ -count=1
```

**Whole-fire gates** (from the worktree):

```bash
go build ./... && make vet && golangci-lint run ./... && STRICT=1 go run ./scripts/lint-conventions.go && go test ./internal/substrate/ ./internal/refractor/... ./internal/edge/... ./cmd/facet/ ./cmd/loupe/ ./cmd/refractor/ -count=1
```

### 5. In-scope gotchas

**This fire's own obligations.**

- **`EnsureStream` uses `CreateOrUpdateStream`** (`stream.go:52`) and never diffs the existing config — so
  Inc 1 *does* update the live SYNC stream in place. That is the intent; it also means Inc 1 is what makes
  Inc 2's write-backs legal (a consumer may not exceed the stream ceiling), so the order is load-bearing.
- **`ensureSyncStream` runs per nats-subject rule, not once per boot** — `NewNatsSubjectAdapter`
  (`natssubject.go:102`) is called from `buildRuleAdapter` (`main.go:1068`) inside `startPipeline`
  (`:1152`), which `activateIfNotRegistered` (`:1631`) also drives on **hot reload**. So Inc 2 must not
  hang off it naively: guard the backfill with a `main()`-local set keyed by stream name. **Its lifetime,
  stated rather than assumed:** per process; after Inc 1 nothing in the tree can create a zero-threshold
  consumer on SYNC, so one pass per stream per process is sufficient, a second is a provable no-op, and a
  hot reload that introduces a *new* stream name still gets its own first pass.
- **`Deregister` has exactly one caller today** (`control/service.go:1244`, the handler) — re-verified. Inc
  3a is the first real one, as §5 claims.
- **No Health-KV emission changes**, so the Health-KV schema-doc lockstep does not fire.
- **No package/DDL/lens edit**, so no version bump, no `make reinstall-package`, no `provision-readpath`.
- **MERGED ≠ RUNNING**: this fire changes `internal/substrate` (very wide) plus `internal/refractor/*`,
  `cmd/facet`, `cmd/loupe`. Derive the affected binaries with the §4 `go list -deps` loop; `bin/refractor`
  and `bin/facet` are both live right now (pids 75714 / 453) and must be rebuilt and cycled.

**Touched components' "Review keeps catching" dossiers — copied in, the applicable entries:**

*Substrate.*
- **A server-immutable consumer field needs delete-then-create in BOTH directions** — JetStream refuses to
  update `DeliverPolicy` *or* `OptStartSeq` on an existing consumer (`nats-server` 2.14
  `server/consumer.go:2435,:2438`). ← **directly binds Inc 2**: this is why the write-back copies the
  read-back config.
- **A vendor-behaviour claim in a comment needs a pinned `file:line`** — every new constant asserting NATS
  behaviour carries its pinned source location, or it is a hypothesis.
- **A process-local memo of server-owned state must name its invalidation boundary** — ← binds Inc 2's
  once-per-stream guard; the boundary is stated above, at the field.
- **Abandoning a consumer iterator DISCARDS messages the server already counts as delivered** — a reconnect
  path drains, only a shutdown path stops.

*Refractor.*
- **New pipeline state without a declared lifetime** (registry / latch / armed flag) — reset, carry, and
  order it at replay, reconnect, tombstone, and retry, or the review will.
- **A meta sweep multiplies `Rebuild`** — any fan-out over the lens set floods consumer management. ← the
  reason Inc 2 is once-per-stream rather than once-per-lens.
- **A two-layer seam can be green at each layer and broken across it — the interposed step is where it
  dies.** ← Inc 3b spans `personalinterest` (KV) and JetStream; write the seam test with the **real**
  intervening sequence.
- **An upsert-only reprojection retracts nothing whose key drops out.**

*Edge.*
- **The SYNC subject is per-ACTOR, not per-device** — a second device on the same identity shares the feed.
  ← Inc 3b's probe must key on the **durable** (identity+device), never on subject traffic.
- **The browser host gets ONE attach per page — it has no restart loop.**
- **The local cursor is a FLOOR, not "the sequence that just succeeded"** — §8.3's unconditional
  `SetCursor` is pre-existing and out of scope; do not "fix" it here.

**The standing checklist** (`agents/fire-brief-template.md`) applies unmodified; #1 (lifetime, not data
structure), #2 (every census is a premise), #4 (removal needs a transport AND an observer — §8 is that
enumeration) and #6 (precedent may carry debt — the `KV_` prefix, above) are the live ones.

### 6. Adjacent finds

1. **The edge-sync durable-name format already has two independent copies** —
   `internal/edge/sync/sync.go:48-50` (`DurableName`) and `cmd/loupe/edge.go:195-197` (`edgeSyncDurable`,
   the same string built by hand). Loupe copied rather than imported because `internal/edge/sync` drags the
   whole edge client (store, transport, vault) into any binary that imports it. Inc 3b needs the same
   construction on the Refractor side and would mint a **third** copy.
   → **Absorbed into this fire, as Inc 3b's first step**: hoist it to `internal/refractor/subjects` — the
   package that already owns durable-name construction and parsing (`lens_durable.go`) and that
   `internal/edge/sync`, `internal/refractor/adapter`, `internal/refractor/health` and `cmd/refractor` all
   already import. `edgesync.DurableName` and Loupe's helper both delegate; no third copy is created and
   the existing drift hazard closes. This is narrowing, not widening — it is the minimum that gives Inc 3b
   its probe without a new copy.
2. **`cmd/loupe/edge.go`'s in-tree note that nothing GCs the interest registration** (near `:543`) becomes
   false the moment Inc 3b lands. → Fixed in the same increment; the design's §5 already calls the roster
   change out as operator-visible.
3. Nothing else surfaced that needs Andrew or a designer pass.

### 7. Non-goals (the drift fence)

- **No change to the consumer-config seam** — `substrate/consumer.go`'s `DeliverPolicy`/`OptStartSeq`
  handling is the cold-sign-in design's territory (§6-A, §9). This fire touches stream config and a
  consumer's `InactiveThreshold` only.
- **No delete-verdict janitor on SYNC** (§6-B) — the server is the only thing that decides expiry.
- **No per-consumer `InactiveThreshold` plumbing** through `DurableConsumerConfig` / `transport.ConsumerConfig`
  (§6-A: default-open).
- **No TTL on `personal-lens-interest`** (§6-C).
- **No fix to the unconditional `SetCursor`** (§8.3) — pre-existing, conservative in direction, out of scope.
- No contract edit; nothing under `docs/contracts/*` describes this plane (§"For Andrew", re-grepped).

### Scope-diff gate — PASSED

Parts 2–4 diffed item-by-item against part 1. Every touch traces to "make expiry a property of the stream"
(Inc 1+2) or "reconcile the one artifact that has no expiry of its own" (Inc 3). One **narrowing**
correction and one **absorption**, both recorded above; no widening, no substituted mechanism. Declared
dependencies re-verified both ways: Inc 3 genuinely depends on Inc 1+2 (§5's circularity argument), and the
board's former `seq behind cold-signin` marker is confirmed already cleared. The design states no numeric
census to re-run; its two live claims (the browser's 30 min, no ephemerals on SYNC) were re-verified above
and both hold.
