# Loom deadline probe — the expiry signal is the server's `MaxAge` marker, not any empty body

**✅ SHIPPED `1982952e` (2026-09-04) — RATIFIED (Winston-adjudicated, per the 2026-08-20 delegation) 2026-09-03.** Adversarial pass in §15
(one cold reviewer; 1 BLOCKING + 2 MAJOR found and folded). Filed from
[`loom-state-tombstone-sweep-design.md`](loom-state-tombstone-sweep-design.md) §11.2; board row
*[Loom] A re-delivered step-deadline marker fails a parked instance after 24 h*
(`_bmad-output/planning-artifacts/backlog/lattice.md`, Component maintenance). Grounded at `ae3bb648` (the sweep
`a5f4ef2e` merged; every coordinate below is post-sweep).

## For Andrew

**What it does, in two lines.** The `loom-deadline` handler runs its rejected-or-lost probe on *every*
empty-body message on `deadline.>` — the server's expiry marker and Loom's own removals alike — and the probe's
evidence (the 24 h Contract #4 tracker) can be far younger than the wait it backstops. This design makes the
probe act only on **the expiry of the arm it is probing**: the message must carry the server's
`Nats-Marker-Reason: MaxAge` (provenance — the substrate already delivers headers), the deadline key must be
absent at probe time (currency — a present key means a later step re-armed), and the terminal write is
revision-conditioned on the record the probe read (the write window). `disarmDeadline` is deleted: on the only
path that now reaches it the key is already gone.

**Fork check — none.** Mechanism inside `internal/loom`'s deadline handler and one state-store function; no new
key, bucket, declaration surface or scaling axis. The row's `no-pattern:` asked for *"an armed/disarmed deadline
state on the instance record"*; §3 re-derives the need and shows the message and the key already carry the two
facts a recorded flag would, without a clock or a replica premise. A mechanism choice, not a fork.

**Contract check — builds to, changes nothing.** Contract #10 §10.6: *"once creation commits, the deadline
**disarms** and the async wait has **no runtime timeout**"*; §10.3: `deadline.<instanceId>`'s **expiry** drives
recovery, and *"nothing in a bucket's internal key layout is a platform surface"*. The handler today can break
the first by acting on something that is not the second; this design is those sentences coming true. No
`docs/contracts/*` edit.

**One premise made explicit and pinned (review §15 #3).** The design's soundness needs `KV_loom-state` to carry
**no stream-level `MaxAge`** — the server writes a byte-identical `MaxAge` marker for an age-limit removal, so a
future "bound the bucket by age" would mint one for every legacy DEL. `verify-kernel` gains the assertion.

**Honest re-sizing after the sweep (§1.6).** The filed harm was 12,346 *permanent* DEL markers replayed by any
rebuild; the sweep converted them to one-minute purges and left zero on the dev stack. What remains is the
*mechanism* (a handler that acts on removals), whose harm now hides behind a TTL the sweep said nothing reads —
the handler reads every one — plus two ms-scale races and 25 lines of machinery that exist only because of it.
Imp ★★ (was ★★★); size S. Andrew's standing instruction (2026-09-03): built in this session via the Steward.

---

## 0. The ask, verbatim, clause by clause

Board row (filed by the sweep's close pass, `2c9d241a`):

> *The probe reads rejected-or-lost from two absences (the 24 h tracker, the outbox) while the wait it
> backstops is unbounded; a `loom-deadline` durable rebuilt from `DeliverAll` replays every disarmed running
> instance's marker and fails it.* — `no-pattern: an armed/disarmed deadline state on the instance record the
> probe can read`

Filing paragraph (`loom-state-tombstone-sweep-design.md` §11.2):

> *any re-delivery of a `deadline.<id>` marker later than 24 h fails the parked instance: a `loom-deadline`
> durable rebuilt from `DeliverAll` (12,346 markers replayed, every disarmed running instance among them), or a
> conversion pass without §3.3's guard. The structural fix is state the probe can read — an armed/disarmed
> deadline fact on the instance record, with a lifetime across redrive/replay*

| Clause | Where it is answered |
|---|---|
| "reads rejected-or-lost from two absences … while the wait it backstops is unbounded" | §1.2 — true; the absences are the right evidence *for an expiry of the current arm*, and the defect is that the probe runs on other wake-ups |
| "a durable rebuilt from `DeliverAll` replays every disarmed running instance's marker and fails it" | §1.3 (what a replay carries), §1.6 (the population after the sweep: transient, but the mechanism is intact), §5 |
| "or a conversion pass without §3.3's guard" | §5 row *PURGE+TTL*; §10 — the guard is left in place and stops being load-bearing |
| "the structural fix is state the probe can read … on the instance record" | §3 — **declined**; the facts are already on the message (provenance) and the key (currency) |
| "with a lifetime across redrive/replay" | §7 — no new state |

## 1. Grounding

### 1.1 The handler and its predicate (HEAD `ae3bb648`)

`internal/loom/engine.go:1256-1271` — `handleDeadline` acks a message with a body (the arm PUT) and runs
`onDeadline` (`:1305`) on **any** empty body. `onDeadline` re-reads the instance (`getInstance`), no-ops on a
non-running instance or an empty pending token, and asks two absences: the Contract #4 tracker via the
`lattice.op.status` RPC (`trackerExists`; `TrackerTTL = 24h`, `internal/processor/tracker.go:19`, Contract #4
§4.3) and the outbox record (`outboxExists`). Both absent ⇒ `fail` (`:1360`). `onUserTaskDeadline` (`:1380`) and
`onExternalTaskDeadline` (`:1442`) apply the same two absences to the *creation* op, and route "committed" to
`disarmDeadline` (`state.go:590-611`: a GET, then a guarded `KVPurgeWithTTL`).

### 1.2 Why the evidence is right for an expiry and wrong for anything else

Inside the window the deadline measures — armed at T with TTL 60 s, probed at T+60 s — a committed op's tracker
is a minute old and the verdict is sound. Outside it the verdict is noise: a userTask parked for its human
(`userTaskGrantTTL`, 30 days) has a tracker for 24 h and none after; a systemOp step armed *after* the marker's
step has a tracker only once it commits. The probe is sound exactly when its trigger is **the expiry of the arm
it is probing**. Two things break that today: the handler cannot tell an expiry from a removal, and it cannot tell
the current arm from a previous one.

### 1.3 Every empty-body shape on `deadline.>`, at the pinned server — pinned by a run

2026-09-03, embedded `nats-server v2.14.0` (`go.mod:12`) via `natsfixture`, bucket `LimitMarkerTTL: 1s` (the
provisioning, `internal/bootstrap/primordial.go:117-121`), an ordered consumer on `$KV.b.deadline.>` started
before the writes. Inc 1 keeps this as the mechanism fixture (§13).

```
+    1ms PUT deadline.s3 ttl=1s  -> seq=1          # an arm
+    1ms PUT deadline.s3 ttl=10s -> seq=2          # re-arm: evicts seq 1 (History:1)
+    1ms PUT deadline.s5 ttl=2s  -> seq=3;  DELETE s5 -> seq=4  op="DEL"                      # the legacy delete
+    2ms PUT deadline.s6 ttl=2s  -> seq=5;  PURGE  s6 ttl=5s -> seq=6  op="PURGE" rollup=sub   # every Loom removal since a5f4ef2e
+ 1503ms STATE msgs=3   (s3: live value rev 2; s5: DEL marker; s6: PURGE marker)   ← NO marker for evicted seq 1
+ 3005ms STATE msgs=3                                                                ← NO MaxAge marker for s5 / s6 at their 2 s
+ 6006ms STATE msgs=2   (s6's purge marker expired; subject gone, not re-marked)
+10003ms DELIVERED seq=7 deadline.s3 body=0 op="" reason="MaxAge" ttl="1s" rollup="sub"   ← the live re-arm's own expiry
+11008ms STATE msgs=1   (s3's marker expired; subject gone. s5's DEL marker remains, permanently)
```

| Shape on the subject | Headers delivered | Lifetime | Source |
|---|---|---|---|
| **The expiry** of a live TTL'd arm (the subject's last message) | `Nats-Marker-Reason: MaxAge`, `Nats-TTL: 1s`, `Nats-Rollup: sub`; no `KV-Operation` | 1 s; never re-marked | `server/filestore.go:6883-6893` (per-message TTL), `:6948-6963` (emission); `sdm.go:41-43`; ADR-43 *Limit Markers* |
| A **stream-age** removal that empties a subject | **the same headers, byte for byte** | 1 s | `filestore.go:6824-6829` — the premise in §7 |
| A client **delete** (`KVDelete`, `BatchOp{Delete}`) | `KV-Operation: DEL` only | permanent | `nats.go jetstream/kv.go:1157`; the sweep's `checkLoomStateDelete` gate now refuses this idiom in `internal/loom` |
| A client **purge with TTL** (every Loom removal now: `state.go:512-519`, `:603-611`, `:624-640`, `actuator.go`) | `KV-Operation: PURGE`, `Nats-Rollup: sub`, `Nats-TTL: 1m0s` | `tombstoneTTL` (one minute); not re-marked | `kv.go:1153-1161`; `sdm.go:41-43` |
| An arm **evicted** by a re-arm, a delete or a purge | **nothing** — the TTL entry finds the message gone | — | `filestore.go:6883-6888` (`sm == nil → ttls.Remove`) |

The last row is the `History:1` guarantee `state.go:503-507` relies on, now pinned by a run. It also bounds the
races in §5: a `MaxAge` marker is always the expiry of an arm that was **live when it expired** — the step the
instance was on at that instant, which is not necessarily the step it is on when the probe reads.

The client decodes these into `KeyValuePurge` (`MaxAge`/`Purge`, or `KV-Operation: PURGE`) and `KeyValueDelete`
(`Remove`, or `DEL`): `jetstream/kv.go:959-975`, `:1261-1275`. `docs/components/loom.md` (the *Provisioning +
index posture* paragraph) already says the expiry *"arrives … as `KeyValuePurge` / `Nats-Marker-Reason: MaxAge`
(distinct from a normal DEL)"*, and `internal/loom/doc.go` says *"its expiry (a KeyValuePurge/MaxAge marker)
trips a read-before-act probe"*. The docs describe a distinction the handler has never made.

### 1.4 The headers reach the handler today

`substrate.Message.Header func(key string) string` (`internal/substrate/consumer.go:114-121`) is installed by
`newMessage` (`:454-472`) on all three delivery paths: the plain durable loop (`:348`, `:425`) and the supervisor
pump Loom's fixed durables run on (`consumer_supervisor_pump.go:670`). Present since `c16f7391` (2026-06-28).
Two adjacent records say otherwise: the sweep design §13 item 2 (*"needs headers on `substrate.Message`, which
the consumer path does not carry; deliberately not widened into"*) and its build note §5 (*"`substrate.Message`
carries no headers — do not try to key the deadline handler on the marker reason"*). Both are wrong; Inc 3
strikes them with a pointer here so the next reader does not inherit the refusal.

### 1.5 The substrate already spells the vocabulary

`internal/substrate/kv_multi.go:50-53`: `directGetKVOpHdr = "KV-Operation"`, `directGetKVOpDelete`,
`directGetKVOpPurge`, `directGetMarkerReasonHdr = "Nats-Marker-Reason"` — unexported, used by the direct-get
marker parse. Inc 1 exports these (`KVOperationHeader`, `KVOperationDelete`, `KVOperationPurge`,
`MarkerReasonHeader`) and adds `MarkerReasonMaxAge = "MaxAge"` beside them; the sweep's `KVListTombstones` and
this handler read the same names.

### 1.6 The population, before and after the sweep — and what the sweep did to the demand

Read-only census 2026-09-03 at `2c9d241a` (pre-sweep), an ordered `HeadersOnly` `DeliverAll` consumer on
`$KV.loom-state.deadline.>` drained to `NumPending == 0` — the shape a rebuilt durable replays:

```
live deadline.> messages: 12348
  body>0=false op="DEL" reason="" -> 12348
```

At `ae3bb648` (post-sweep, the live pass converted 61,741 markers; `nats stream subjects KV_loom-state
'$KV.loom-state.deadline.>'` → *No subjects found*): **zero** replayable messages. Every Loom removal is now a
one-minute purge marker (`tombstoneTTL`, `state.go`), so a rebuild replays only markers younger than a minute,
whose instances have a tracker younger than two — the filed 24 h shape is closed *by the marker's TTL*. The sweep
design §3.1 says of that TTL: *"nothing reads a marker for its own sake (§4), so the minimum would do"* and *"a
knob invites the belief that some consumer depends on the value"*. The deadline handler reads every marker, and a
`tombstoneTTL` past 24 h — or any DEL writer on an older binary, or the §7 age limit — reopens the row silently.
The demand this design meets is the **mechanism**: a handler that acts on removals, a probe that cannot tell
which arm expired, and an unconditioned terminal write. Their harms today (§5): two ms-scale races on the live
path, and a defect whose containment is an unrelated constant.

### 1.7 When a rebuild happens

`ConsumerSupervisor.Reset` / `ResetAwaitReopen` delete and re-create the durable
(`internal/substrate/consumer_supervisor.go:211`, `:273`, `:340-352`; the control plane refuses *Pause* on
`loom-deadline`, `control.go:260`, not reset); `Remove`; a spec change; a bucket recreation; and the sweep's
start-time pass (`engine.go:304`), which republishes every legacy `deadline.>` DEL as a purge marker the durable
then delivers — its `skipRunningDeadlineMarker` guard (`tombstone_sweep.go:72`) exists only because of the
defect fixed here.

## 2. The existing pattern this extends

The handler classifies by *shape* (`len(msg.Body) != 0` ⇒ the arm, ack; `engine.go:1257`). The relay
(`actuator.go:86`) and the completion/trigger consumers (`engine.go:414`, `:744`) ack empty bodies too and are
untouched: an empty body is never destructive there. The deadline handler is the one Loom consumer where an
empty body triggers a write that can fail an instance; it is the one that must know which empty body it holds.
Reading a delivered header in a handler is `consumer.go:114-121`'s stated purpose (the Processor's
`Lattice-Reply-Inbox`); a revision-conditioned instance write is `state.redrive` (`state.go:542-560`, whose
twenty lines above say why the CAS rides the instance key, not the pin).

## 3. Re-deriving the row's `no-pattern:`

The probe must act on the expiry of the arm it probes and on nothing else. Two facts decide that: **is this
message an expiry?** and **is the arm it expired the current one?** Candidate carriers:

1. **The message + the key.** `Nats-Marker-Reason: MaxAge` is an expiry and nothing else (§1.3). And if
   `deadline.<id>` is *present* when the probe runs, a later step re-armed after this marker's arm expired
   (the marker's own emission emptied the subject; every running step arms — `submitStep`'s three `transition`
   calls all pass a TTL — and a terminal purges), so the marker is stale. No clock, no replica, no new write.
2. **A boolean on the record** (`deadlineArmed`). Refuses a replayed removal on a parked instance, but cannot
   tell a replayed removal from an expiry when the instance is on a *later armed* step — the probe runs against
   N+1's op, relayed but not yet committed ⇒ tracker absent, outbox absent ⇒ false fail. Also needs the record
   and the key to agree across `rearmDeadline`, which writes outside the batch.
3. **`deadlineExpiresAt` on the record + a due-check.** Closes 2's gap with a clock comparison across replicas
   (§10.3 *engine replicas are interchangeable*); its failure direction is a dropped *genuine* marker, gone in one
   second — the wedge §10.6 forbids most.

Carrier 1 is the design. The `no-pattern:` was solution-shaped; the need is met by reading what the substrate
already delivers and what the bucket already holds.

## 4. The shape — three increments, one fire

### 4.1 Inc 1 — provenance: the handler probes on `MaxAge` only; `disarmDeadline` is deleted

`handleDeadline` (`engine.go:1256`) classifies before it acts:

```go
// A message on deadline.> is one of three things, told apart by the headers the
// substrate delivers (consumer.go newMessage):
//   - the arm itself: a body (the deadlineMark) — nothing to do;
//   - the server's expiry marker, Nats-Marker-Reason: MaxAge — THE signal
//     (Contract #10 §10.3/§10.6): run the read-before-act probe;
//   - Loom's own removal of the key (KV-Operation DEL or PURGE: a terminal
//     transition, the tombstone sweep) — not an expiry. These are what a rebuilt
//     DeliverAll durable replays, and the probe's evidence (the 24 h tracker)
//     says nothing about a step that was never due.
if len(msg.Body) != 0 {
	return substrate.Ack
}
if msg.Header == nil {
	// Only a hand-built Message has no header source; the supervised path
	// always installs one. Refuse to guess on a destructive path.
	e.logger.Error("loom: deadline message carries no header source; ignored", "subject", msg.Subject)
	return substrate.Ack
}
if msg.Header(substrate.MarkerReasonHeader) != substrate.MarkerReasonMaxAge {
	e.logger.Debug("loom: deadline key removed, not expired; ignored", "subject", msg.Subject,
		"op", msg.Header(substrate.KVOperationHeader), "reason", msg.Header(substrate.MarkerReasonHeader))
	return substrate.Ack
}
```

**`disarmDeadline` (`state.go:590-611`) and its two callers (`engine.go:1393`, `:1458`) are deleted.** After
Inc 1 every probe runs on a subject the server has just emptied (§1.3 row 1): the arm is gone *because* it
expired, so the committed branches have nothing to remove. The function's doc comment names the case it existed
for — *"the disarm's own marker re-fires the deadline watcher, which probes and disarms again"* — and that
re-fire is exactly the non-expiry wake-up Inc 1 removes. §10.6's *disarms* is then the expiry itself: the probe
finds the creation committed and **nothing re-arms**. The branches become a log line and `return nil`:

```go
if committed {
	// CreateTask committed: the task vertex exists and the bounded creation wait
	// is over. The deadline that woke this probe has expired — that is the only
	// thing that wakes it — and is not re-armed: the human wait is unbounded
	// (§10.6). Cursor and token are untouched.
	e.logger.Info("loom: CreateTask committed; creation-deadline expired, unbounded human wait", …)
	return nil
}
```

Footprint of the deletion (§8 C2): the three `TestDisarmDeadline_*` tests (`state_internal_test.go:17-69`) go;
`TestDisarmDeadline_LeavesExpiringMarker` (`tombstone_internal_test.go:101-124`) is **retargeted to
`deleteToken`**, the surviving single-publish removal with the identical guarded shape (`state.go:624-640`) —
the sweep's pin of that shape stays; the two sweep tests that use `disarmDeadline` as a *fixture* to mint a
disarm-shaped marker (`tombstone_sweep_internal_test.go:540`, `:715`) call a test helper that does what the
function did (`KVPurgeWithTTL(deadlineKey(id), tombstoneTTL, 0)`). `TestHandleDeadline_TerminalInstanceIsASilentNoOp`
(`:804-830`) hand-builds a `Message` with nil `Header`; it gains a `Header` returning `MaxAge` for the marker
reason, so it keeps testing what its name says (the probe returns at the status check) rather than the nil arm.

`rearmDeadline` (`state.go:579`) stays: the probe's "not yet relayed" branch writes a fresh live arm whose
expiry is a genuine marker. `transition`'s terminal purge (`:512-519`) stays: it removes a live arm when a step
completes early, and the marker it writes is now acked without a read.

### 4.2 Inc 2 — currency and the write window: the deadline key's presence, then a conditioned `fail`

§5 has two rows Inc 1 does not close, both the *same* genuine marker read against a *later* state:

- **Emission → read.** The marker for step N is emitted; N's completion advances the instance to N+1 (arming
  `deadline.<id>` for N+1) before the pump delivers; the probe reads the record at N+1, derives N+1's requestId,
  finds no tracker (submitted milliseconds ago) and no outbox record (the relay purges it on publish-ack) ⇒
  `fail`. No revision guard can see this: the read itself is already late (review §15 #1).
- **Read → write.** The probe reads N (running); the advance lands; `fail` writes unconditionally (`engine.go:1208`
  → `transition`, whose instance member carries no revision, `state.go:449`) ⇒ the advanced record is flipped
  to `failed` and N+1's live arm is purged.

**Currency guard — one `KVGet deadline.<id>` before any verdict.** A `MaxAge` marker's emission emptied the
subject; if the key is present now, something re-armed since — an advance (`transition` with a TTL, every
running step) or another replica's `rearmDeadline` on the same marker — and either way the arm this marker
expired is not the current one. The probe logs at Info (*"deadline marker is stale; a later step re-armed"*)
and returns nil ⇒ Ack. If the key is absent, the current arm is the one that expired (a later step always
arms; a terminal purges and the record says so). This is the read `disarmDeadline` already performed, moved to
the front of the probe and given the meaning it always had. It runs for **every** `MaxAge` marker, before the
tracker RPC — one KV round trip that also saves the RPC on every stale marker.

**Write guard — the probe reads at a revision and its `fail` carries it.** `onDeadline`'s first read becomes
`getInstanceAtRevision` (`state.go:170`); `fail` gains `expectedRevision uint64` (0 = unconditioned, the
`KVPurgeWithTTL` convention), forwarded to `transition`, which sets `HasRevision` on the instance member when
non-zero — `redrive`'s shape (`:552`). Only the four probe-path `fail`s carry it (`engine.go:1326`, `:1360`,
`:1410`, `:1475`); `advance`'s and the trigger path's (`:528`, `:825`) are serialized by the new token's
`CreateOnly` and are unchanged. On `IsRevisionConflict` (`substrate/kv.go`) the probe logs Info (*"instance
moved on under the probe"*) and returns nil ⇒ Ack — the drop `advance` takes on a stale completion
(`engine.go:809-812`). The three writers of `instance.<id>` are `createInstance` (`state.go:284`, `CreateOnly`),
`transition` (`:449`) and `redrive` (`:552`); none bumps the revision while leaving the pending step in place
(review §15, held), so a bump under a *running* read is always another actor's advance, completion or fail, and
the marker's step is no longer pending. **One probe write stays unconditioned:** the "not yet relayed" re-arm
(`rearmDeadline`, a plain PUT). An advance landing between the currency read and that PUT overwrites step N+1's
fresh arm with the old step's TTL — not a wedge (a marker still fires for the overwriting arm) and not a
terminal, so it is excluded from the write guard and named in `onDeadline`'s doc comment. No Nak: a `MaxAge`
marker lives one second, so a Nak requests a
redelivery that will not exist, and the re-probe would find nothing to do.

### 4.3 Inc 3 — the premise assertion, docs, the stale claims, the dossier

- **`verify-kernel` + `internal/bootstrap/verify.go`** (the `KV_loom-state` block, `verify.go:274-283`): assert
  `MaxAge == 0` on the bucket's stream, with the sentence *"the deadline probe keys on the server's `MaxAge`
  marker; an age limit would mint one for every removal marker"*. §7.
- `docs/components/loom.md`: the ⌛ line (*"the `MaxAge` marker is the only wake-up; a removal of the key is
  not an expiry, and a present key means a later step re-armed"*), the userTask/externalTask *disarms*
  sentences (the expiry is the disarm; nothing re-arms), the *Provisioning + index posture* paragraph (now true;
  say the handler reads it), crash-safety invariant 3 (*the probe's terminal write is revision-conditioned on
  the record it read*), and the dossier entry below.
- `internal/loom/doc.go` (the *rejected/lost* bullet); `state.go`'s `deadlineMark` comment (the value is the
  arm; the signal is the server's marker; the key's presence is the currency test); the `onDeadline` /
  `onUserTaskDeadline` / `onExternalTaskDeadline` doc comments (*"every branch … is CAS-on-running"* becomes
  true and says how); `external_e2e_test.go:352-355` and `onboarding_e2e_test.go:407-410` (*"each probe must
  DISARM"* → exactly one probe runs; nothing re-arms).
- `loom-state-tombstone-sweep-design.md` §13 item 2's parenthetical and build-note §5's gotcha: strike, one-line
  pointer here. Its §3.3 `deadline.>` guard is **left in place**: one-shot, shipped, harmless once the handler
  ignores purges; the note says it is no longer load-bearing. Its §3.1 *"nothing reads a marker for its own
  sake"*: strike — the deadline handler reads every one, and now ignores the removal shapes.
- Dossier (`loom.md` *Review keeps catching*): **A handler that acts on "empty body" acts on every removal
  shape the bucket can produce.** An expiry, a delete and a purge are indistinguishable without the headers the
  substrate delivers; name the header. And when a probe's evidence has a shorter life than the wait it guards,
  its trigger set — *and* the currency of what it read — are the first things to audit.

## 5. The outcome table — every wake-up × every instance state

Wake-ups: the `MaxAge` marker (a genuine expiry); a Loom removal (`DEL` legacy, `PURGE` now), delivered live or
replayed. States: **terminal** · **running, in flight** (op submitted, not committed) · **running, parked**
(creation committed; unbounded wait) · **running, advanced since the marker's arm**.

| Wake-up | State | Today | After Inc 1 | After Inc 2 |
|---|---|---|---|---|
| `MaxAge` | in flight, committed, event missed | advance + alert (systemOp) / creation committed → "disarm" (a no-op read) | same; async kinds log + return | same |
| `MaxAge` | in flight, not yet relayed | re-arm | same | same |
| `MaxAge` | in flight, relayed, rejected | **fail** (correct) | same | same, conditioned |
| `MaxAge` | advanced since — completion landed **before the probe's read** | probe evaluates N+1: committed → no-op; **relayed-uncommitted → false fail, and the committed→disarm branch purges N+1's live arm** (a silent wedge if N+1 is then rejected) | the disarm purge is gone; the false fail remains | key present ⇒ stale ⇒ Ack — **closed** |
| `MaxAge` | advanced since — completion landed **between the probe's read and its write** | `fail` overwrites the advanced record; N+1's arm purged | same | CAS refused ⇒ moved-on ⇒ Ack — **closed** |
| `MaxAge` | terminal | no-op | same | same |
| `MaxAge` from a **stream age limit** on a legacy DEL | parked, evidence stale | (no age limit today) | would false-fail — **the §7 premise; asserted by `verify-kernel`** | same |
| `DEL`/`PURGE`, live delivery | terminal (the transition's purge) | probe → no-op | **ack, no read** | same |
| `DEL`/`PURGE`, live | parked (today's `disarmDeadline` re-fire) | probe → tracker fresh → "disarm" no-op | ack; the writer no longer exists | same |
| `DEL`/`PURGE` **replayed** | parked, evidence stale (a DEL from an older binary; a purge with a TTL past 24 h) | **false fail — the row** | **ack** | same |
| `DEL`/`PURGE` replayed | in flight on a later step, relayed, uncommitted | **false fail** (§3 carrier 2's gap) | **ack** | same |
| `Purge`/`Remove` reason (stream-admin API markers) | any | probe | ack — `STREAM.PURGE`/`MSG.DELETE` are on every non-bootstrap deny list (`natsperm/matrix.go`, `protectedStreamDenies`); never a signal | same |
| empty PUT, no headers | any | probe | no writer exists; a nil `Header` logs Error and acks | same |
| never-written / re-created | — | no message; a re-created arm is a body ⇒ ack | — | — |

Two replicas: both receive the same marker (a queue group is not set; `DeliverGroup` empty, `engine.go`'s
`deadlineSpec`). Replica A fails; B's CAS is refused ⇒ Ack. Replica A finds committed and returns; B does the
same. Replica A re-arms (outbox pending) and B's currency read ran *after* A's PUT ⇒ stale ⇒ Ack; if B's read ran
*before* A's PUT, B walks on and its `probeFail` commits at the still-current revision — the one pairing that
does not converge to A's outcome. It resolves to *fail*, which is what a single replica reaches one re-arm
later for an op that is still unrelayed; pre-existing, unchanged here, and stated in the probe's doc comments
rather than claimed away (review §15 close pass, #4).

## 6. Consumer table

| Reader | Reads | Change |
|---|---|---|
| `handleDeadline` (`engine.go:1256`) | body; now the two headers | §4.1 |
| `onDeadline` + the two async probes | the record (now at revision), **`deadline.<id>`** (new, first), tracker RPC, outbox | §4.2 |
| `disarmDeadline`'s GET (`state.go:604`) | `deadline.<id>` | the read moves to the probe's front; the write is deleted |
| `skipRunningDeadlineMarker` (`tombstone_sweep.go:72`) | `instance.<id>` before converting a `deadline.>` DEL | unchanged; no longer load-bearing |
| `TestOnboardingE2E_CreatedTaskDisarmsForUnboundedWait`, `TestExternalE2E_CommittedNoReply_DisarmsToUnboundedWait` | outcome (running, cursor, token, no completion/failure) | pass unchanged — they assert the promise, not the removal (review §15, held); their comments are Inc 3's |
| `TestOnboardingE2E_RejectedCreateTaskFails`, `TestExternalE2E_RejectedInstanceOpFails`, `TestExternalE2E_NotYetRelayedRearms`, `TestLoomE2E_RejectedStepFails` | the positive vectors, driven by real 2 s expiries | pass unchanged |
| `TestHandleDeadline_TerminalInstanceIsASilentNoOp` (`tombstone_sweep_internal_test.go:804`) | hand-built `Message` | gains a `MaxAge` header (§4.1) |
| the four sweep-test uses of `disarmDeadline` | a disarm-shaped marker | helper / retarget (§4.1) |
| `verify-kernel`, `bootstrap verify` | the bucket's stream config | + `MaxAge == 0` (§4.3) |
| `InspectInstance`, `cmd/loupe/weaver.go`'s presence read, `runningInstanceCounter` | the record / pin keys | unchanged |

## 7. State-lifetime table — and the one premise

**No new state.** Deleted: one function and its guard. Added: two header reads, one key read, one revision
argument. The premise the design stands on: **a `MaxAge` marker on `deadline.<id>` is the expiry of a live
arm**. Two things could break it — a server that emits the marker for other removals (pinned false at v2.14.0,
§1.3; the mechanism fixture re-checks on every vendor bump, `docs/vendors.md` NATS row) and a **stream-level
`MaxAge` on `KV_loom-state`** (`filestore.go:6824-6829` writes the identical marker for an age-limit removal that
empties a subject; `isSubjectDeleteMarker` exempts `PURGE` and reason-marked messages but **not a legacy `DEL`**,
`sdm.go:41-43`). The bucket is provisioned with no age limit (`primordial.go:109-121`); Inc 3 asserts it at boot
and in `verify-kernel`, so the day someone bounds the bucket by age the kernel gate says why not. A third,
dormant: the currency read is a `KVGet`, i.e. a direct get, and on an R>1 `loom-state` (the shelved HA-NATS
row) a follower could serve the pre-marker value and read a genuine expiry as *armed*. R1 today; the HA-NATS
design must make the currency read a non-direct get before it lands.

## 8. Executable censuses (run 2026-09-03 at `ae3bb648`; Phase 0 re-runs them)

| # | Command | Result | Pins |
|---|---|---|---|
| C1 | `grep -n 'len(msg.Body)' internal/loom/*.go` | `actuator.go:86`, `engine.go:414`, `:744`, `:1257` | only `:1257` triggers a write on an empty body |
| C2 | `grep -rn 'disarmDeadline' --include='*.go' internal/` | callers `engine.go:1393`, `:1458`; def `state.go:603`; tests `state_internal_test.go:28,50,54,69`, `tombstone_internal_test.go:116,122`, `tombstone_sweep_internal_test.go:540,715`; comments `state_internal_test.go:17`, `tombstone_internal_test.go:101`, `state.go:624` | the deletion's full footprint — four sweep-test sites the first draft missed (§15 #2) |
| C3 | `grep -rn 'handleDeadline\|deadlineSpec()' --include='*_test.go' internal/` | `tombstone_sweep_internal_test.go:804`, `:822` — one hand-built `Message`, nil `Header` | the nil arm has one caller; Inc 1 gives it a header |
| C4 | `grep -n 'newMessage(msg)' internal/substrate/consumer.go internal/substrate/consumer_supervisor_pump.go` | `consumer.go:348`, `:425`; `pump.go:670` | every delivery path installs `Header` |
| C5 | `grep -n 'e\.fail(' internal/loom/*.go \| grep -v _test` | `:528`, `:825`, `:1326`, `:1360`, `:1410`, `:1475` | four probe-path sites conditioned; two token-serialized sites unchanged |
| C6 | `grep -n 'Key: *instanceKey(' internal/loom/state.go` | `:284` (CreateOnly), `:449`, `:552` (CAS) | the record's three writers; none bumps without moving the step |
| C7 | the pre-/post-sweep header census (§1.6) | 12,348 `DEL` → 0 subjects | every replayable message was a removal, never an expiry |
| C8 | the marker-shape run (§1.3) | as pasted | the four shapes and the no-marker-on-eviction rule at the pinned server |
| C9 | `grep -n 'MaxAge' internal/bootstrap/primordial.go internal/bootstrap/platform_buckets.go` | (KV buckets set none) | the §7 premise holds today |

## 9. Contract surface — none

Builds to Contract #10 §10.3 (*"`deadline.<instanceId>` — the pending step's deadline; its **expiry** drives
§10.6's step-deadline-exceeded recovery"*; *"nothing in a bucket's internal key layout is a platform surface"*)
and §10.6 (*"once creation commits, the deadline disarms and the async wait has no runtime timeout"*; *"a late
completion after a declared failure is dropped — a bounded, alerted divergence"*). The reviewer tried to find a
sentence an observer could see change and found none (§15). Contract #4 §4.3 (the 24 h tracker) is read, not
touched.

## 10. Reconciliation with the mental model

- **Didn't we already handle this?** In three places, one symptom each: `disarmDeadline`'s GET guard breaks the
  *re-fire loop*; the sweep's `skipRunningDeadlineMarker` keeps *its* pass from replaying the harm; and the sweep's
  one-minute `tombstoneTTL` bounds the replay window to a minute — the containment the row's harm rests on today,
  a constant its own design says nothing reads. All three defend a consequence of a non-expiry wake-up. This design
  removes the wake-up; the first is deleted, the second becomes inert, the third stops being load-bearing.
- **Does it contradict the design of record?** `loom.md` and `doc.go` already *describe* the handler as keyed on
  the `MaxAge` marker (§1.3). The code was the drift.
- **Does it add state we already keep?** No state is added; the currency test reads a key that already exists
  for exactly this purpose (§10.3: *"one key always denotes the current step's clock"*).
- **The sibling ★★★ row** (*the 1 s marker TTL is the delivery window*, `📋 ready`) is the other half of one
  trigger set: this design makes the handler act only on the expiry marker; that row makes the expiry marker
  survive long enough to be acted on. Independent; not widened into. One input for it from here: the handler
  **Naks** on a probe error (`engine.go:1268`), and a Nak'd `MaxAge` marker cannot be redelivered once its second
  is up — a transient `lattice.op.status` timeout during a genuine expiry loses the signal today; the marker-TTL
  decision should size for a redelivery, not only for a restart.
- **The sweep fire** merged (`a5f4ef2e`); this design is grounded on it. Its `disarmDeadline` line is what Inc 1
  deletes; its `checkLoomStateDelete` gate stays green (this fire adds no delete idiom; the mechanism fixture
  lives in `internal/substrate`, and the classification test writes its legacy DEL through the substrate's raw
  publish with the `KV-Operation: DEL` header, as the sweep's own `legacyDelete` helper does).

## 11. Alternatives

| # | Alternative | Verdict |
|---|---|---|
| 1 | **Do not have the probe** — remove the deadline watcher | Rejected. A rejected op is invisible on `core-events`; the watcher is the only off-stream signal and §10.6 promises it. What *is* removed: `disarmDeadline` (−22 lines, one fewer round trip per creation). |
| 2 | **Do nothing** — the sweep's one-minute TTL already contains the filed harm | Rejected. The containment is a constant whose design says nothing depends on it (§1.6); the two races in §5 are live regardless of any TTL; and a DEL from an older binary or a stream age limit reopens the row without a test noticing. |
| 3 | **Raise `TrackerTTL` to ≥ 30 days** so the evidence outlives the wait | Rejected. Contract #4 §4.3 fixes 24 h as the platform-wide idempotency horizon; the probe is one reader. Does not close the later-step race either. |
| 4 | **The row's prescription** — `deadlineArmed` on the record | Rejected (§3 carrier 2): false-fails a later in-flight step; a field on 12,351 records; needs record and key to agree across `rearmDeadline`. |
| 5 | **`deadlineExpiresAt` + due-check** | Rejected (§3 carrier 3): a clock across replicas, failing toward a dropped genuine marker. |
| 6 | **`DeliverNew` for `loom-deadline`** | Rejected. Skips every expiry that fires while Loom is down; the sibling row's one-second window becomes the whole outage. |
| 7 | **Keep `disarmDeadline`, give its purge a revision** | Rejected. On every path that reaches it after Inc 1 the key is gone, so a conditioned purge is a conditioned no-op; a guard whose justification is discharged is inert machinery. |
| 8 | **Delete the write, keep the read** (`disarmDeadline`'s GET, moved to the front of the probe) | **Adopted** — it is §4.2's currency guard (review §15 #6). |
| 9 | **Inc 2's CAS alone**, keeping the empty-body predicate | Rejected. A replayed removal on a parked instance is not a race — the revision is unchanged since the disarm — so the CAS passes and the false fail commits. |
| 10 | **Inc 1 + the currency guard, no CAS** | Rejected. The read→write window remains (§5 row 5): the probe reads N, the advance lands, `fail` overwrites. Fifteen lines in `redrive`'s shape close it. |
| 11 | **Inc 1 alone** | Rejected. Leaves both §5 races, one of which today purges a live arm on a running instance (the silent wedge). |

## 12. Migration, compatibility, risks

- **Migration: none.** No record or bucket change; the durable keeps its name and position; behaviour changes at
  the next Loom cycle. Any remaining legacy DEL markers are acked either way.
- **Risk — a server that stops emitting the marker reason.** The wedge §10.6 forbids. The pin is v2.14.0 and the
  header is ADR-43's shape since 2.11; the mechanism fixture fails a vendor bump that changes it.
- **Risk — an age limit on `KV_loom-state`.** §7; `verify-kernel` refuses it with the reason.
- **Risk — the currency guard reads a key the sweep can rewrite.** The start-time pass converts a legacy DEL into
  a purge marker — a marker, not a value; `KVGet` maps DEL and PURGE alike to not-found, so the guard reads
  *absent* on both. Only a value (an arm) reads present.
- **Risk — Inc 2 acks a CAS refusal without re-probing.** Correct by §4.2's enumeration of the record's writers;
  the race test pins the post-state so a regression that failed or re-probed would be seen.
- **Risk — the sweep's `checkLoomStateDelete` gate.** No production delete idiom is added; the legacy-DEL fixture
  publishes through the raw header form the sweep's own tests use.

## 13. Test strategy (each owned in §14)

- **Mechanism fixture (Inc 1, `internal/substrate`).** §1.3 as a kept test: arm-and-expire delivers an empty body
  with `Nats-Marker-Reason: MaxAge` through `substrate.Message.Header`; a client delete delivers `KV-Operation:
  DEL` and no reason; a purge-with-TTL delivers `PURGE` and no later `MaxAge`; an arm evicted by a re-arm yields
  no marker within its TTL. Waits are on delivered messages and `NumPending`, never sleeps (CLAUDE.md).
- **Classification (Inc 1) — the negative with its positive vector.** Through the real `loom-deadline` durable on
  a fixture bucket, an instance seeded by real `createInstance`+`transition` (dossier #2), parked on a userTask
  whose `lattice.op.status` responder answers *not committed*: (a) a legacy DEL and a `KVPurgeWithTTL` on
  `deadline.<id>` ⇒ still running on its token after the durable drains (the negative); (b) a real 1 s arm expires
  ⇒ failed (the positive vector, same seed, same responder). **Mutation proof:** restore the empty-body predicate
  ⇒ (a) fails.
- **Replay (Inc 1) — the row's harm end to end.** N parked instances with disarm-shaped purge markers, responder
  absent; `ResetAwaitReopen("loom-deadline", wait)` (`consumer_supervisor.go:273`, the no-sleep barrier); after
  the rebuilt durable reports `NumPending == 0`, every instance is still running. Same mutation proof.
- **Currency (Inc 2).** Seed step N; deliver a `MaxAge`-headed marker to `handleDeadline` after a real advance
  armed N+1 (the key is present) ⇒ Ack, record untouched, N+1's arm live; then purge the key and deliver again
  ⇒ the probe runs. **Mutation proof:** drop the presence read ⇒ N+1 is failed.
- **Write window (Inc 2).** Seed step N; read at revision R; real `advance` (bumps R); the probe's `fail` with
  expected R ⇒ returns nil, record running at N+1 with its token pointer and live arm; with the fresh revision ⇒
  failed. **Mutation proof:** drop `HasRevision` ⇒ the first call commits.
- **Premise (Inc 3).** `bootstrap verify` fails on a `KV_loom-state` provisioned with `MaxAge > 0` (a fixture
  bucket), passes on the real provisioning.
- **Unchanged e2es** (§6) are the regression bar for the positive path.

## 14. Decomposition for the Steward

**One Lattice fire, size S; Inc 1 and 2 are the same probe seen from its trigger, its read and its write.**
Posture: a change on the destructive path of an orchestration engine — one cold adversarial reviewer at close is
the recommendation, not a floor (`agents/steward/SKILL.md` §4).

**Phase 0.** Re-run §8 C1–C6, C9 against the base of the day; C7 if a stack is up (the class must be
`DEL`/`PURGE` only).

**Inc 1 — provenance + the deletion.** Substrate: export the four `kv_multi.go` names, add `MarkerReasonMaxAge`.
Loom: `handleDeadline` as §4.1; delete `disarmDeadline` and its three tests; retarget
`TestDisarmDeadline_LeavesExpiringMarker` to `deleteToken`; the sweep tests' fixture helper; the `MaxAge` header
on `TestHandleDeadline_TerminalInstanceIsASilentNoOp`; the two committed branches → log + return. *Owns:* the
mechanism fixture; the classification test + mutation proof; the replay test + mutation proof.

**Inc 2 — currency + the conditioned write.** The `deadline.<id>` presence read at the front of `onDeadline`;
`getInstanceAtRevision` as its first read; `fail(…, expectedRevision)` → `transition` sets `HasRevision`; the
four probe-path sites; `IsRevisionConflict` ⇒ moved-on ⇒ nil. *Owns:* the currency test + mutation proof; the
write-window test + mutation proof.

**Inc 3 — the premise assertion, docs, dossier.** `verify.go` + `scripts/verify-kernel.go` `MaxAge == 0`;
the doc edits in §4.3; the two design-doc strikes; the dossier entry. *Owns:* the premise test.

**Gates.** `go build ./...` · `make vet` · `golangci-lint run ./...` · every `scripts/lint-*.go` (`STRICT=1
lint-conventions`, `lint-board`, the sweep's `checkLoomStateDelete` self-test) · `go test ./internal/loom/...
./internal/substrate/... ./internal/bootstrap/... -count=1` · full `go test ./... -p 4` · build-tagged harnesses
reaching `internal/loom` / `internal/substrate` (`grep -rl "^//go:build " --include=*_test.go internal/`; the
leaseconvergence harness constructs a real `Engine`). No `packages/` edit. `make verify-kernel` (live stack) for
the new assertion.

**Live close (MERGED ≠ RUNNING).** Cycle Loom on the dev stack (`pkill -x loom` then `make orchestration`);
`nats consumer info KV_loom-state loom-deadline`; reset the durable and watch it drain with **zero** `loom
instance failed` lines; one rejected step (a pattern whose systemOp is denied) still fails within `StepTimeout`;
`make verify-kernel` green with the new line. Numbers in the commit message.

## 15. Adversarial pass (2026-09-03, one cold reviewer, read-only)

1. **BLOCKING — Inc 2's CAS closed only the read→write half of the race.** The marker can be *read* after the
   advance (emission → read), and then the CAS passes and a healthy N+1 is failed. Folded: the deadline key's
   presence is the currency test, run first (§4.2); the CAS keeps the write window. §5 gained the second race
   row; §11 gained row 8 and the recommendation moved to it.
2. **MAJOR — C2/C3 were stale (the sweep merged mid-design).** Four sweep-test uses of `disarmDeadline` and one
   hand-built handler `Message` with nil `Header`. Folded into §4.1's footprint, §8, §14 Inc 1.
3. **MAJOR — the "`MaxAge` ⇒ a live arm's expiry" premise was unpinned against a stream age limit**, which
   writes the identical marker and would mint one per legacy DEL. Folded: §7 names it; Inc 3 asserts `MaxAge == 0`
   in `verify-kernel` and `bootstrap verify`; §5 has the row.
4. MINOR — constants already exist in `kv_multi.go:50-53`; promoted rather than duplicated (§1.5).
5. MINOR — every coordinate had moved with the sweep; the doc is re-grounded at `ae3bb648`.
6. MINOR — the alternatives table lacked "delete the write, keep the read"; it is now row 8 and the design.
7. MINOR — `ResetAwaitReopen` is the replay test's no-sleep barrier (§13); the two e2e comments that become
   false are Inc 3's (§4.3).
8. NIT — the `natsperm` citation read a deny list as a grant; corrected (§5).
9. NIT — Inc 1's payoff was under-claimed: today's committed→disarm branch, racing an advance, purges N+1's
   *live* arm — a silent wedge if N+1 is then rejected. Now §5 row 4.

**Held under attack (the reviewer's words):** deleting `disarmDeadline` is sound — no path runs the probe with a
live key after Inc 1; `Header` is never nil on a real delivery; the no-re-mark rules for `MaxAge`, TTL'd purge
and evicted arms all check out at v2.14.0; the two disarm e2es assert the promise, not the removal; §9's "no
contract change" is honest; `instance.<id>` has exactly three writers and none bumps the revision without moving
the step.

---

### Appendix — grounding ledger (at `ae3bb648`)

| Claim | Pinned to |
|---|---|
| The handler probes on any empty body | `internal/loom/engine.go:1256-1271` |
| The probe's evidence and its writers | `engine.go:1305-1360`, `:1380-1410`, `:1442-1475`; `processor/tracker.go:19`; Contract #4 §4.3 |
| `fail` → `transition` writes the record unconditionally | `engine.go:1208-1231`; `state.go:443-525` (`:449`) |
| `disarmDeadline`, its guard and its stated reason | `state.go:590-611` |
| Record writers | `state.go:284`, `:449`, `:552` |
| `MaxAge` marker headers; per-message TTL vs stream-age sites; no re-mark | `nats-server v2.14.0 server/filestore.go:6824-6829`, `:6883-6893`, `:6948-6963`; `sdm.go:41-43`; ADR-43 *Limit Markers* |
| No marker for an evicted / deleted / purged arm | `filestore.go:6883-6888`; §1.3 run |
| Client header shapes and decoding | `nats.go v1.52.0 jetstream/kv.go:1153-1161`, `:959-975`, `:1261-1275`, `message.go:217` |
| `Message.Header` on every delivery path | `substrate/consumer.go:114-121`, `:348`, `:425`, `:454-472`; `consumer_supervisor_pump.go:670`; `c16f7391` |
| Existing header constants | `substrate/kv_multi.go:50-53` |
| The sweep's contrary claims | `loom-state-tombstone-sweep-design.md` §3.1 ("nothing reads a marker"), §13 item 2; build note §5 |
| `redrive` is the CAS precedent; `getInstanceAtRevision` | `state.go:542-560`, `:170` |
| `loom-deadline` is `DeliverAll`, supervised, no queue group | `engine.go` `deadlineSpec` |
| Reset paths | `substrate/consumer_supervisor.go:211`, `:273`, `:340-352`; `control.go:260` |
| The sweep's guard and pass | `tombstone_sweep.go:72`, `:268-290`; `engine.go:304` |
| Bucket provisioning; verify loops | `bootstrap/primordial.go:109-121`; `bootstrap/verify.go:274-283`; `scripts/verify-kernel.go:303-340` |
| Live population | §1.6 censuses, 2026-09-03 |
| The docs already claim the distinction | `docs/components/loom.md` *Provisioning + index posture*; `internal/loom/doc.go` |

---

## 16. Build record and close pass (2026-09-04)

**Shipped in one fire: `1982952e`** (worktree `steward-lattice-deadline-provenance`, base `94a06d8a`). All three
increments; 18 files, ~+1,000/−270 (the substrate mechanism fixture and the five Loom tests are most of the
addition). Gates: build · vet · golangci (0) · all 15 `lint-*.go` · full `go test ./... -p 4` · `make
test-lease-convergence` · `make verify-kernel` from the worktree against the live NATS (`OK no stream age
limit … KV_loom-state`). Live close: §16.3.

### 16.1 What the reviews found, classified

One cold design review (§15) and one cold code review, both opus, neither the implementer.

| Finding | Class | Routed |
|---|---|---|
| The CAS closed only the read→write half of the race; emission→read needed the key's presence (§15 #1) | design-gap | fixed in design before build |
| A stream age limit forges the `MaxAge` marker (§15 #3) | design-gap (unpinned premise) | `verify-kernel` assertion + test |
| Coordinates and censuses stale after the sweep merged mid-design (§15 #2) | process — parallel-fire base skew | re-grounded at HEAD before build |
| The deleted `TestDisarmDeadline_PropagatesGenuineGetFailure` was not re-aimed at the function that inherited its fork (code #1) | brief-gap (the brief listed the deletion, not the pin it carried) | test ported: `TestDeadlineArmed_PropagatesGenuineGetFailure` |
| Every probe error Naks a marker the server removes in one second (code #2) | pre-existing; the sibling marker-TTL row's mechanism | documented in `onDeadline`; sibling row sharpened |
| "Second replica is a no-op" over-claimed a re-arm-vs-fail race (code #4) | comment accuracy | narrowed in code + §5 |
| The re-arm PUT is the one unconditioned probe write (code #8) | design omission, not a defect | §4.2 names the exclusion |
| Literal headers on the producer side of `batch.go`; a vendor comment naming reasons the pin never emits; a sync assertion on async delivery; a dead conjunct (code #5, #6, #7, #9) | implementation hygiene | fixed |
| The currency read is a direct get — unsafe on R>1 (code, watch item) | premise expiring under a shelved fork | §7 names it for the HA-NATS design |

**Dossier:** one entry appended to `docs/components/loom.md` (§4.3's). Twice-seen class: *a removal census that
deletes a function must list what its tests pinned* — this is the second time (the sweep's close pass caught the
same shape on `redrive`'s happy-path test); promotion candidate for `agents/fire-brief-template.md`'s standing
checklist #4 rather than a lint (a test's *reason* is not greppable).

### 16.2 Accounting

Every discovery above resolved in this fire or lands on an existing row: the Nak-loses-the-expiry class on
*[Bootstrap/Loom] The 1 s marker TTL is the delivery window …* (`📋 ready`, its `What` now names the Nak); the
direct-get watch item on the HA-NATS design (`🚧 shelved`, §7 here). No new row filed.

### 16.3 Live close

Loom cycled from the main checkout via `make orchestration` after the merge (`pkill -x loom`; the recipe rebuilt
and relaunched the tier). `bin/loom` mtime = the merge minute; the new instance `loom-VhgqosgUBf6BC5jHEtzC`
reports `healthy` in Health KV with `loom-deadline: running`, one running instance; `nats consumer info
KV_loom-state loom-deadline`: 0 unprocessed, 0 redelivered. `verify-kernel` from the worktree against the live
NATS printed the new line (`OK no stream age limit … KV_loom-state`). **Not observed live, and said so:** a
genuine `MaxAge` probe under the new binary — the dev stack armed no deadline in the observation window (the
consumer's last delivery predates the cycle), and the only pattern-start paths available (`lattice loom start` on
a lease-signing or identityErasure pattern) carry real side effects on the dev data, so none was forced. The
positive path is pinned by the four unchanged e2es (real 2 s expiries) and the classification test's positive
vector; the DeliverAll replay has no live population to replay (zero `deadline.>` subjects after the sweep) and is
pinned through `ResetAwaitReopen` in-repo. CI on `1982952e`: see the commit that lands this section.

---

### Deadline-provenance fire brief (build note, 2026-09-04)

**1. Scope sentence (verbatim, board row + §4).** *The deadline probe acts on every empty body, not on the
expiry it backstops.* Green bar: `handleDeadline` probes only on `Nats-Marker-Reason: MaxAge`; the probe reads
`deadline.<id>` first and acks a present key as stale; the probe's four `fail`s carry the record's revision and
ack a conflict as moved-on; `disarmDeadline` deleted; `verify-kernel` + `bootstrap verify` assert `MaxAge == 0`
on `KV_loom-state`; the mechanism fixture, classification, replay, currency, write-window and premise tests
each with their mutation proof; all gates green; Loom cycled live, `loom-deadline` reset and drained with zero
`loom instance failed` lines.

**2. Verified touch-list (design §8, run at `ae3bb648`; base `0a7f1eb7` is docs-only on top).**
`internal/loom/engine.go:1256-1271` (handler), `:1305-1360` (`onDeadline`; `fail` at `:1360`), `:1380-1410`
(`onUserTaskDeadline`; disarm `:1393`, fail `:1410`), `:1442-1475` (`onExternalTaskDeadline`; disarm `:1458`,
fail `:1475`), `:1326` (pin-missing fail), `:1208-1231` (`fail`), `:809-812` (`advance`'s drop branch — the
moved-on precedent), `:528`/`:825` (the two `fail`s left unconditioned) · `internal/loom/state.go:170`
(`getInstanceAtRevision`), `:443-525` (`transition`; instance member `:449`; deadline arm/purge `:495-519`),
`:542-560` (`redrive`, the CAS precedent — read its twenty lines above), `:579` (`rearmDeadline`, stays),
`:590-611` (`disarmDeadline`, delete), `:624-640` (`deleteToken`, the surviving twin), `:135-143`
(`deadlineMark` comment) · `internal/substrate/kv_multi.go:50-53` (constants to export), `consumer.go:114-121`
(`Header`), `kv.go` (`IsRevisionConflict`) · tests: `internal/loom/state_internal_test.go:17-69` (three
disarm tests, delete), `tombstone_internal_test.go:101-124` (retarget to `deleteToken`),
`tombstone_sweep_internal_test.go:540`, `:715` (fixture helper), `:804-830` (add the `MaxAge` header),
`external_e2e_test.go:352-355`, `onboarding_e2e_test.go:407-410` (comments) · `internal/bootstrap/verify.go:274-283`,
`scripts/verify-kernel.go:303-340` (the `KV_loom-state` assertion block) · docs: `docs/components/loom.md`
(⌛ line, two *disarms* sentences, *Provisioning + index posture*, crash-safety invariants, dossier),
`internal/loom/doc.go` (rejected/lost bullet), `loom-state-tombstone-sweep-design.md` §3.1 / §13.2 / build-note §5.

**3. Precedents to mirror.** Header read in a handler: `consumer.go:114-121`'s own stated purpose. CAS'd
instance write: `state.go:542-560` (`redrive`) — the batch member shape at `:552`. Moved-on drop:
`engine.go:809-812`. Guarded single-publish removal (the surviving twin the retargeted test pins):
`state.go:624-640`. Sweep-test fixture that publishes a legacy DEL through the raw header form: the sweep's
`legacyDelete` helper (`tombstone_sweep_internal_test.go`). Reset barrier: `ResetAwaitReopen`
(`substrate/consumer_supervisor.go:273`). The §1.3 scratch run is the mechanism fixture's shape.

**4. Increment order + green checks.** Inc 1 (provenance + deletion) → `go test ./internal/substrate/ -run
'Marker|Header' -count=1` + `go test ./internal/loom/ -count=1`, with the mutation proof (restore the empty-body
predicate → the classification and replay tests fail). Inc 2 (currency + conditioned write) → `go test
./internal/loom/ -run 'Deadline|Probe|Currency|Race' -count=1`, mutation proofs (drop the presence read →
N+1 failed; drop `HasRevision` → the stale fail commits). Inc 3 (premise + docs) → `go test ./internal/bootstrap/
-count=1`; `make verify-kernel` (live stack). Fire → `go build ./... && make vet && golangci-lint run ./... &&
STRICT=1 go run ./scripts/lint-conventions.go && go run ./scripts/lint-board.go` + every other `scripts/lint-*.go`
+ full `go test ./... -p 4` + the build-tagged harnesses reaching `internal/loom` / `internal/substrate`.

**5. In-scope gotchas + dossiers.** No `packages/` edit (no version bump). The sweep's `checkLoomStateDelete`
gate refuses `KVDelete` / `Delete: true` in `internal/loom` — the classification test's legacy DEL goes through
the raw header publish, never the substrate delete. `KVGet` maps DEL and PURGE markers to not-found, so the
currency read sees *present* only for a live arm. `Nak` is never the answer to a stale marker (it lives one
second). `Header == nil` only on a hand-built `Message`; the one existing hand-built call gets a `MaxAge` header.
**Loom dossier (verbatim, applicable):** (2) *A fixture that hand-seeds `loom-state` cannot reach the states a
real transition leaves behind* — seed through `createInstance` + `transition`; (5) *`require`/`assert` inside a
`require.Eventually` predicate fails from a non-test goroutine* — predicates return bool. **Substrate dossier:**
*the batch CAS is per-subject* — the instance member carries the revision, nothing else in the batch does.
**Standing checklist** (`agents/fire-brief-template.md`): 1 no new state (§7); 2 every citation re-verified at
`ae3bb648`; 3 every negative test carries its positive vector + mutation proof (§13) — binds Inc 1 and 2
hardest; 4 the removed function's other obligations enumerated (§4.1: none beyond the re-fire guard); 5 one
deterministic key, one writer — unchanged; 6 the precedent carries debt: the two disarm e2es use `time.Sleep`
(CLAUDE.md) — their *assertions* stand, their comments are Inc 3's, their sleeps are not this fire's to
rewrite unless the builder touches those tests for another reason.

**6. Adjacent finds.** None new beyond the design: the sibling ★★★ marker-TTL row already carries the
Nak-loses-the-signal input (§10). A builder that finds more reports it upward, never files or widens.

**7. Non-goals.** The marker TTL (sibling row); `TrackerTTL`; the sweep's guard (left in place); the two
token-serialized `fail`s; any record-shape change.
