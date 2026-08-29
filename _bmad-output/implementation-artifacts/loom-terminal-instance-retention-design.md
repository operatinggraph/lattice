# Terminal Loom instances never leave `loom-state` — bound them against the window that makes it safe

**Status: ⚠️ PARTIALLY WITHDRAWN 2026-08-29 — the pruning half is FALSIFIED and was not shipped.** Read §0
before anything else; it supersedes §2 and §3's Inc 1/Inc 3 wherever they differ. What shipped is the
heartbeat fix (§3 Inc 2) and the `RedriveInstance` repair the build found (build note). What did not is the
retention TTL and its drain. Winston (Lattice Steward, unattended remote fire).

**Superseded status line (2026-08-29, pre-review):** *"✅ Winston-ratified — build-ready. No frozen-contract
change and no architectural fork: the retention window is chosen so Contract #10 §10.9's dedup guarantee stays
true…"* — the second half of that sentence is wrong, and §0 says why.

---

## 0. What review falsified, and what shipped instead (2026-08-29)

Three cold adversarial reviewers ran against the built increment. **Two independently falsified this
design's central claim.** Recording it here, at the top, because an unamended body is a wrong instruction to
the next fire.

**The claim (§2):** pruning is safe when `R > MaxAge(core-events)`, because that makes the cursor outlive
every `patternStarted` that could still be redelivered — so Contract #10 §10.9's dedup sentence stays true
and no contract edit is needed.

**Why it is false.** `MaxAge` bounds exactly one republication source: **redelivery of a stored message** to
a rebuilt `DeliverAll` consumer. It does not bound a **re-published** one, and the platform has a live
producer of those. `StartLoomPattern` accepts an optional caller-supplied stable `instanceId`, and Weaver's
`triggerLoom` deliberately supplies a `claimId`-seeded one so re-dispatch collapses onto the existing
instance (`internal/weaver/strategist.go:208-214`, `internal/weaver/actuator.go:147,162,174`). Each reclaim
mints a *new* episode `requestId`, so the Contract #4 tracker does not collapse it: it genuinely commits and
emits a **brand-new** `patternStarted`, with today's timestamp, carrying the **same** `instanceId`. Reclaims
continue for as long as the gap column stays open — which is unbounded by construction.

And this is not an inferred obligation. It is frozen contract text
(`docs/contracts/10-orchestration-substrate.md:227-230`):

> **`triggerLoom` → `StartLoomPattern`:** … `StartLoomPattern` carries the stable `claimId`-seeded
> `instanceId` on `loom.patternStarted`, and Loom's instance presence check + `createInstance` `CreateOnly`
> collapse a re-emitted trigger onto the existing instance (no new instance, hence no new userTask). **This
> dedups the whole pattern.**

So the cursor's presence backstops a horizon `MaxAge` cannot bound, and **no value of `R` can**. A failed
cursor pruned at day 8 meets the next reclaim with nothing to collapse onto, and the outcome is one of two
bad ones: the trigger consumer hot-loops on an unbounded immediate `Nak` (the pin's terminal DEL marker
permanently refuses `createInstance`'s `CreateOnly`), or — if that marker is ever "fixed" the way redrive's
was — the pattern silently re-runs from step 0 with its committed side effects re-executed.

**The grounding miss that allowed it.** §2 was built by reading `docs/contracts/10-orchestration-loom.md`
and not `docs/contracts/10-orchestration-substrate.md`, where the `triggerLoom` dedup clause and the
`loom-state` keyspace lifetimes actually live. The fire brief's own standing-checklist item 4 asked for
exactly this enumeration — *"pruning a record removes a dedup guard; enumerate every obligation the cursor's
presence was silently discharging, not just the one this design found"* — and the enumeration stopped at the
first obligation.

**Two further frozen-contract contradictions** the same review surfaced, both in
`10-orchestration-substrate.md`: `:115` states "the instance record itself persists" (a TTL contradicts it
outright), and `:130` states the redrive pin's `CreateOnly` write "is both the re-pin and the concurrency
guard" (the shipped repair removes it — and that sentence is *itself false*, describing a write that can
never commit; the same document names the exact mechanism at `:223-225`).

### What shipped

- **The `RedriveInstance` repair** — a live production defect this fire found: redrive could never re-pin
  over the terminal batch's delete marker, so the operator's only recovery path for a failed flow failed
  closed every time. Guard moved to a cursor CAS. Requires the `:130` contract edit, prepared as a proposal.
- **§3 Inc 2, the heartbeat fix** — the actual live failure in §1 fact 2: the running-instance count now
  reads the pin index with no body fetch and under a per-tick deadline, so a large bucket can no longer let
  `health.loom.<instance>` expire and make Loom read DOWN while healthy. This needed no pruning to work.
- `ErrInstanceNotFound` as a matchable sentinel.

### What did not, and why it is not merely deferred

The **retention TTL (§3 Inc 1) and the drain (§3 Inc 3) are withdrawn.** Bounding `loom-state` requires
durable dedup evidence that outlives the cursor and covers an unbounded external re-dispatch horizon. That is
a change to *what backs Contract #10 §10.9's dedup guarantee* — a new platform primitive, not an application
of a ratified pattern — so it is a designer pass, not a steward decision, and it is re-filed as one naming
the absent primitive. Note the tempting shortcuts are both unsound: leaning on the pin's permanent DEL marker
promotes an accidental NATS artifact to a correctness primitive *and* still grows without bound, and pruning
only `complete` cursors does not help because a completed instance that did not close its gap column is
re-dispatched just the same.

**§1's diagnosis stands unchanged** — the accretion is real, the heartbeat exposure was real and is fixed.
Only the remedy for the accretion was wrong.

**Board row:** `backlog/lattice.md` → *[Loom] Terminal instance records are never pruned; the heartbeat
1,024-cap breakdown is the alarm* (★★ / S–M) · resolved shape from
[lattice-designer-triage-2026-08-27 §7](../../docs/reviews/lattice-designer-triage-2026-08-27.md).

---

## 1. The defect, in three facts

1. **Nothing ever deletes `instance.<id>`.** `createInstance` writes the cursor and its `.pattern` pin as one
   CreateOnly batch (`internal/loom/state.go:245,256`); the terminal batch flips `Status` in place and deletes
   only the pin, the token and the deadline mark (`state.go:364,372-376,397-403,432-436`). The cursor itself
   has no delete site anywhere in the tree. `loom-state` therefore accretes one record per pattern instance,
   forever — 9,260+ mostly-terminal cursors on the live stack.

2. **The heartbeat pays for that accretion on every tick, with no deadline.**
   `runningInstanceCounter.count` lists the whole bucket and then *fetches every instance body* to decode
   `Status` (`internal/loom/health.go:54,64`). Past 1,024 matched subjects `KVGetMulti` silently leaves its
   fast path for an ephemeral-consumer drain (`internal/substrate/kv_multi.go:263-264,373`) — correct, but
   far slower, and documented worst-case ~480s (`kv_multi.go:176`). The counting call inherits the
   long-lived `run(ctx)` context with no per-tick timeout (`health.go:151-165`), while the health document
   it feeds carries a TTL of interval × multiplier = **100s** (`health.go:145-147`). A slow count therefore
   does not merely lag: it lets `health.loom.<instance>` expire, and Loom reads as DOWN while it is fine.
   That is a worse failure than the row reported, and it is the one that is live today.

3. **The 1,024 cap is the trip-wire, not the defect.** Both named options in the original row treated the cap
   as the thing to fix. It is a symptom of an unbounded key set; bound the key set and the cap stops being
   reachable in the steady state.

## 2. Why pruning is not obviously safe — and what makes it safe

Contract #10 §10.9 makes the cursor's *presence* a correctness mechanism, in two places:

> `instanceId` = the `StartLoomPattern` `requestId` … redelivery dedup is automatic (Loom's `loom-state
> instance.<instanceId>` cursor keyed on it → already present → skip).

> **Idempotency needs no new machinery:** … Loom dedups at-least-once event redelivery on the `instanceId`
> (the `loom-state` cursor presence).

Delete the cursor and that guard is gone. The failure it guards against is not hypothetical: the trigger
consumer is `DeliverPolicy: DeliverAll` on the events stream (`internal/loom/engine.go:303-311`), so a
`loom-trigger` consumer that is ever rebuilt replays history from the stream's beginning. Against pruned
cursors, `createInstance`'s CreateOnly would *succeed* for every replayed `patternStarted`, and Loom would
re-run each historical pattern from step 0 — real side effects, re-executed.

**The dissolving fact:** `core-events` is `LimitsPolicy` with **`MaxAge: 7 * 24 * time.Hour`**
(`internal/bootstrap/primordial.go:201-203`). A replay can therefore only reach back 7 days. So the guarantee
survives pruning exactly when

> **R > MaxAge(core-events)** — the cursor outlives every `patternStarted` event that could still be
> redelivered against it.

Under that inequality the contract's sentence stays true as written: the cursor *is* present whenever a
redelivery is possible. Nothing the contract promises changes, so **no contract edit is required** (this was
checked before choosing the shape, not after).

> **⚠️ FALSIFIED 2026-08-29 — see §0.** The paragraph above is wrong, and the rest of §2 is sound only for
> the one republication source it reasons about. `MaxAge` bounds *redelivery* of a stored message; it does
> not bound Weaver's `triggerLoom` reclaim, which **re-publishes a new** `patternStarted` carrying the same
> `claimId`-seeded `instanceId` for as long as its gap stays open. `10-orchestration-substrate.md:227-230`
> makes that collapse a frozen guarantee. No value of `R` is sufficient, and a contract edit *is* required.
> The inequality below is necessary but not sufficient; do not build on it.

**The inequality is enforced, not assumed.** A constant chosen against today's `MaxAge` goes silently wrong
the day someone changes `MaxAge`. At startup the engine reads the events stream's configured `MaxAge` and
compares it to the configured retention. If `R <= MaxAge`, or `MaxAge == 0` (unlimited retention — no window
is ever large enough), **pruning stays off** and Loom raises a health issue saying so. Fail-closed: the
accretion is a capacity problem, re-running a historical flow is a correctness catastrophe, and the cheap
`StreamInfo` read at start is what keeps the coupling audible.

**Retention R = 8 days** (`7d + 24h` margin), configurable. One window, not two: 8 days simultaneously clears
the 7-day replay horizon, is a generous redrive window for a `failed` instance
(`RedriveInstance` → `state.go:redrive`), and replaces *forever* with *eight days of throughput*.

## 3. Shape

### Inc 1 — the terminal batch stamps a retention TTL *(⚠️ WITHDRAWN 2026-08-29, §0 — built, reviewed, NOT shipped)*

> The mechanics below are accurate and were proven by test (the TTL stamps, a resume clears it, no consumer
> sees the expiry). They are withdrawn anyway: the *decision* to expire a terminal cursor at all is unsound
> while cursor presence backstops Weaver's unbounded re-dispatch. Keep this section as the record of what was
> measured; do not treat it as a build instruction. Only the `ErrInstanceNotFound` sentinel at the end of it
> shipped.

The batch already carries per-op TTL (`substrate.BatchOp.TTL` → the `Nats-TTL` header,
`internal/substrate/batch.go:56-70,155-156`), and `loom-state` is provisioned `PerKeyTTL: true`
(`internal/bootstrap/platform_buckets.go:58-62`, asserted by
`internal/bootstrap/loom_state_bucket_test.go:53`). So the terminal transition writes the same instance
record it writes today with `TTL: R` added, and nothing else about the batch changes.

Two properties make this safe rather than merely small, and **both are proven by test, not by reasoning**:

- **A resume clears the TTL.** `loom-state` is History:1 (`state.go:421`), so a later write of
  `instance.<id>` *without* a `Nats-TTL` header evicts the TTL-bearing message and the record stops expiring.
  Every non-terminal write already carries no TTL — `transition()` back to running, and `redrive()`
  (`state.go:453-470`). A redrive of an about-to-expire failed instance therefore un-expires it as a side
  effect of the write it already performs. This is the invariant a cold reviewer should attack first.
- **No consumer sees the expiry.** The two durable consumers on the `loom-state` backing stream are filtered
  to `outbox.>` and `deadline.>` (`engine.go:337,349-351`). An `instance.<id>` expiry marker publishes on
  `$KV.loom-state.instance.<id>` and matches neither, so a pruned cursor cannot be mistaken for a step
  deadline.

Only a terminal whose `PendingToken` is empty is stamped. A terminal record still naming a pending token
would strand a `token.*`/`outbox.*` peer if its cursor vanished; the terminal batch clears the token today,
so the guard should never fire — which is precisely why it is a guard and not an assumption.

Folded in (small, same seam): `InspectInstance`'s not-found becomes an exported, matchable
`ErrInstanceNotFound` sentinel instead of `fmt.Errorf` (`internal/loom/control.go:163`), and the control
service maps it. Once instances are pruned, "this id aged out" is an ordinary operator answer and must be
distinguishable from a read failure without string-matching.

### Inc 2 — count the pin index, under a deadline

The pin *is* the running index: `instance.<id>.pattern` is created with the instance and deleted in the
terminal batch, which `pinnedDomains` already relies on in as many words (`state.go:291-296`). So
`runningInstances` becomes a **count of pin keys** — `KVListKeys` + a suffix filter, **no `KVGetMulti`, no
body decode**. The 1,024 cap is not merely avoided, it is unreachable: there is no matched-subject fetch left
on this path. The count runs under a per-tick deadline derived from the heartbeat interval, so a degraded KV
costs one skipped metric rather than an expired health document.

The emitted field is unchanged (`runningInstances`), but its documented derivation is not:
`docs/observability/health-kv-schema.md` (Loom section, ~lines 280-286) says "count of `instance.<id>`
status=running, heartbeat-cadence scan" and must be corrected **in the same commit** — the standing rule that
keeps a health-emission change L2-safe.

### Inc 3 — drain the pre-existing residue once *(⚠️ WITHDRAWN 2026-08-29, §0 — never built)*

> Withdrawn with Inc 1: a drain that deletes cursors has the same unsoundness, and worse — an explicit DEL
> leaves a *permanent* marker on the instance subject (unlike a TTL expiry's self-clearing rollup marker), so
> it would break `createInstance` on both keys of the pair rather than one. Any future drain must land after
> the dedup substrate is redesigned, not before.

Inc 1 stops the bleeding; it cannot heal what is already there. The 9,260 existing terminal cursors carry no
TTL and never will. A one-shot drain runs after engine start, off the startup path, gated by the same
`R > MaxAge` check as Inc 1:

- enumerate instance record keys, fetch bodies, keep those with a terminal `Status` and an empty
  `PendingToken`;
- **delete** those whose KV entry age exceeds R (age from the entry, not from the body — `Instance` carries
  no terminal timestamp);
- bounded by an explicit deadline; a partial pass is fine because the drain is idempotent and convergent —
  the next process start continues it. Outcome (scanned / pruned / remaining) is logged once.

Delete rather than re-stamp: a periodic re-stamp would push the expiry forward on every pass and the record
would never expire at all.

**Why a startup one-shot rather than a control verb or a ticker.** A new control verb would extend the
capability plane (`internal/controlauth`) for a one-time cleanup — cost without a lasting consumer. A ticker
would re-introduce the whole-bucket enumeration this design is removing from the heartbeat. The legacy set is
finite and shrinks to nothing; a mechanism that runs when the operator cycles the binary (which the fix
requires anyway) and then finds nothing to do is the right size.

## 4. What this deliberately does not do

**`ListInstances` keeps its semantics.** Triage §7 resolved to point *both* the heartbeat counter and
`ListInstances` at the pin index. The counter, yes — its metric is running-only by definition. `ListInstances`,
no: the pin index is running-only, so serving it from pins would drop every `failed` instance from the one
surface an operator has to *discover* what to `RedriveInstance` — a capability regression, traded for latency
on an RPC that already carries a 5s handler timeout (`internal/loom/control/service.go:55`) and therefore
degrades loudly and harmlessly. Inc 1 bounds what `ListInstances` can enumerate at the source, which is the
honest fix for the cap exposure. Recorded here as a deviation from the triage's resolved shape, with its
reason.

**Fires 2+ do not exist.** This is one fire.

## 5. Risks

| Risk | Disposition |
|---|---|
| A resume/redrive does *not* clear the TTL (NATS per-message TTL semantics differ from the reading above) | The single load-bearing assumption. Test-proven against the embedded server in Inc 1, not argued. If it is false, the design falls back to an explicit TTL-clearing write in `redrive`/`transition` |
| `MaxAge` changes and silently invalidates R | The startup gate is exactly this; pruning disables itself and says so in health |
| The drain deletes a record a slow in-flight path still needs | Terminal + empty `PendingToken` + age > 8 days. A running instance is never a candidate |
| Operators lose flow history | They do not: the Chronicler's `loomFlowHistory` lens projects `events.loom.>` into `orchestration-history` independently of `loom-state` (`packages/orchestration-base/lenses.go:114-120`, `docs/components/chronicler.md:33`) |

## 6. Gates

`go build ./...` · `make vet` · `golangci-lint run ./...` · `STRICT=1 go run ./scripts/lint-conventions.go` ·
`go run ./scripts/lint-board.go` · `make verify-kernel` · `go test ./internal/loom/... ./internal/substrate/...
./internal/bootstrap/...` · full `go test ./...` with `POSTGRES_TEST_DSN` set (REMOTE.md §3 — without it the
Postgres-gated tests skip and the suite is falsely green).

Review depth: Inc 1 and Inc 3 are posture-changing (a new expiry path on durable orchestration state) →
full 3-layer adversarial, cold reviewers. Inc 2 is mechanical → lead review. One cumulative adversarial pass
over the whole diff at close.

---

### Terminal-instance retention fire brief (build note, 2026-08-29)

**1. Scope sentence (verbatim, §3).** Bound `loom-state` growth by giving terminal `instance.<id>` cursors a
retention TTL that provably exceeds the `core-events` `MaxAge` (preserving Contract #10 §10.9's
cursor-presence dedup), drain the pre-existing TTL-less terminal residue once at startup, and take the
heartbeat's running-instance count off the body-fetching whole-bucket scan onto the pin index under a
per-tick deadline.

**2. Verified touch-list — every anchor re-read live this fire; zero drift found.**

- `internal/loom/state.go:26-29` — the four key prefixes; `:39` `patternPinSuffix`; `:44-53`
  `isInstanceRecordKey`.
- `internal/loom/state.go:339-442` — `transition`: `:364` the instance PUT (Inc 1's stamp site), `:372-376`
  the pin delete, `:397-403` the token delete, `:409-413` the outbox write, `:415-430` the
  `deadlineTTL > 0` arm showing `BatchOp{TTL:}` in use, `:432-436` the deadline delete.
- `internal/loom/state.go:421` — the History:1 comment ("the new PUT evicts the prior"), the basis for
  TTL-clearing-on-resume.
- `internal/loom/state.go:453-470` — `redrive`: instance PUT (no TTL) + pin `CreateOnly`.
- `internal/loom/state.go:185-215` — `listInstances` (NOT changed by this fire; see §4).
- `internal/loom/state.go:304-337` — `pinnedDomains`, the pin-listing shape Inc 2 mirrors, incl. its
  `:291-296` "pins are deleted in the terminal batch, so listing `instance.*.pattern` yields exactly the live
  set" comment.
- `internal/loom/health.go:45-83` — `runningInstanceCounter.count`: `:54` `KVListKeys`, `:64` `KVGetMulti`
  (Inc 2 deletes this call), `:59-62` the record filter.
- `internal/loom/health.go:145-147` — `heartbeatTTL()` (interval × multiplier = 100s); `:151-165` `run`/`emit`
  showing the deadline-free tick ctx; `:19` `defaultHeartbeatEvery = 10s`; `:201` the `KVPutWithTTL` emit.
- `internal/loom/control.go:157-165` — `InspectInstance`, `:163` the `fmt.Errorf` not-found to sentinelize.
- `internal/loom/control/service.go:34-40` — the `engineControl` interface; `:55` `handlerTimeout = 5s`;
  `:294` the `InspectInstance` call site to map the sentinel at.
- `internal/loom/engine.go:130-170` — `Config` defaults + the `StepTimeout` sub-second clamp at `:152-157`
  (the clamp idiom `InstanceRetention` mirrors); `:51-52` `LoomStateBucket`.
- `internal/loom/engine.go:236-260` — engine start (where the Inc 3 drain + the Inc 1 safety gate hook in);
  `:301-311` `triggerSpec` (`DeliverAll`); `:334-343` `relaySpec` (`outbox.>`); `:345-361` `deadlineSpec`
  (`deadline.>`) — the proof no consumer sees an `instance.*` expiry.
- `internal/substrate/batch.go:56-70` — `BatchOp.TTL`; `:155-156` the `Nats-TTL` header write.
- `internal/substrate/kv.go:350-358` — `KVPutWithTTL`; `:107` `KVCreateWithTTL`; `:141` `KVUpdateWithTTL`.
- `internal/substrate/kv_multi.go:163-176,263-264,373` — the 1,024 gate, the transparent fallback, the
  ~480s worst case.
- `internal/bootstrap/primordial.go:198-203` — `core-events`, `LimitsPolicy`, `MaxAge: 7*24h`.
- `internal/bootstrap/platform_buckets.go:58-62` — `loom-state`, `PerKeyTTL: true`;
  `internal/bootstrap/loom_state_bucket_test.go:53` asserts `AllowMsgTTL` on the live bucket.
- `docs/observability/health-kv-schema.md` Loom section (~`:275-286`, example ~`:1125-1143`) — the
  `runningInstances` derivation sentence Inc 2 must correct.
- `docs/components/loom.md:29-30,275-294` — the `loom-state` keyspace description to update.

**Executable census, re-run live this fire (premise check):**
```
grep -rn "instanceKey(" internal/loom/*.go | grep -v _test | wc -l   → write/read sites of instance.<id>
grep -rn "Delete: true" internal/loom/state.go                        → 3 (pin, token, deadline) — no instance delete
grep -c "Review keeps catching" docs/components/loom.md               → 0  (see adjacent finds)
```
The middle line is the design's load-bearing negative and it holds: `loom-state` has no `instance.<id>`
delete site. (Stated per REMOTE.md §8 as a *working-tree* fact, not a history-derived one — the clone is
shallow.)

**3. Precedents to mirror.**
- Inc 1 TTL stamp → `state.go:415-430`'s own `BatchOp{TTL: deadlineTTL}` arm, same batch, same field.
- Inc 1 config clamp → `engine.go:152-157`'s `StepTimeout` sub-second clamp (same 1s `LimitMarkerTTL` floor,
  same clamp-up-rather-than-degrade posture).
- Inc 1 sentinel → the package's existing typed errors (`errPatternPinMissing`); `errors.Is` at the control
  service boundary.
- Inc 2 pin listing → `state.go:304-320`'s `pinnedDomains` filter, minus the `KVGetMulti` (keys only).
- Inc 3 drain → no single precedent for a startup reconcile in `internal/loom`; the closest shipped shape is
  the `b153d120` bootstrap revocation pass (enumerate → classify → act on a derived set, logged). Greenfield
  is honest here: Loom has never had a background maintenance pass.

**4. Increment order + runnable green checks.**
1. **Inc 1** — TTL at terminal + `InstanceRetention` config + startup `R > MaxAge` gate + sentinel.
   `go test ./internal/loom/... -run 'Retention|Terminal|Inspect|Redrive' -count=1`.
   Mutation-verify: drop the `TTL:` field → the expiry test must fail.
   **The TTL-cleared-by-resume test is mandatory and must fail without the History:1 behavior it asserts.**
2. **Inc 2** — pin-index count + per-tick deadline + health-schema doc.
   `go test ./internal/loom/... -run 'Health|Counter|Running' -count=1`; mutation-verify by pointing the
   counter back at the record scan with a poisoned body (count must stay correct via pins).
3. **Inc 3** — startup drain.
   `go test ./internal/loom/... -run 'Drain|Prune' -count=1`; a legacy TTL-less terminal record older than R
   is deleted, a running one and a young terminal one survive, and a partial (deadline-hit) pass is a no-op
   on correctness.
4. **Close** — `go build ./...` · `make vet` · `golangci-lint run ./...`
   (REMOTE.md §7: `GOTOOLCHAIN=go1.26.1 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.4`
   first, and put `$(go env GOPATH)/bin` ahead of the stale system binary) ·
   `STRICT=1 go run ./scripts/lint-conventions.go` · `go run ./scripts/lint-board.go` ·
   `go test ./internal/loom/... ./internal/substrate/... ./internal/bootstrap/... -count=1` ·
   full `go test ./... -p 4` **with `POSTGRES_TEST_DSN` exported** (REMOTE.md §3 — without it the tree is
   falsely green) · `make verify-kernel` against a native NATS.

**5. In-scope gotchas.**

*Obligations this fire trips:* a health-emission change must update `docs/observability/health-kv-schema.md`
in the SAME commit (steward §4) — Inc 2 changes the derivation, so the doc's derivation sentence moves with
it. `docs/components/loom.md`'s keyspace section states the instance record's lifetime and must move too.
No `packages/` content changes, so no manifest/`Version` bump. Build-tagged harnesses: `engineControl` gains
no method (the sentinel is an error value, not a signature change), so the tagged control-plane authz
harness is untouched — but re-check with
`grep -rl "^//go:build " --include=*_test.go internal/` before close if any interface does change.

*Substrate dossier (touched — `KVGetMulti`/`BatchOp` semantics), copied verbatim:*
- **Narrowing a JetStream consumer's filter strands its pending set** — messages pending under the old filter
  never redeliver under the new one; widen-then-drain or recreate the consumer.
- **A server-immutable consumer field needs delete-then-create in BOTH directions** — JetStream refuses to
  update `DeliverPolicy` *or* `OptStartSeq` on an existing consumer.
- **The batch CAS is per-subject** (`Nats-Expected-Last-Subject-Sequence`), not whole-stream — different-key
  writes never serialize, so don't design contention remedies for a lock that does not exist.

*Loom dossier:* **none exists** — `docs/components/loom.md` has no "Review keeps catching" section (census
above: 0). This fire's close pass creates it (adjacent find 1).

*Standing checklist (all six apply; 1, 2 and 4 are the live ones here):*
1. **New state needs a LIFETIME, not a data structure** — this fire IS a lifetime rule; write the instance
   record's state table (created / resumed / terminal / expired / redriven / drained) before editing.
2. **Every census is a premise, especially a negative one** — the "no instance delete site" and "no consumer
   filter matches `instance.*`" claims are both negatives; re-read the two `ConsumerSpec` filters and the
   vendored `nats-server` per-message-TTL behavior at the pinned 2.14 before relying on them.
3. **A negative test needs its positive vector proven first**, and every fix is proven by reverting it.
4. **Removal needs a transport AND an observer** — pruning a record removes a *dedup guard*; enumerate every
   obligation the cursor's presence was silently discharging, not just the one this design found.
5. **One deterministic key, one writer** — `createInstance`'s CreateOnly is that guard; a pruned key makes it
   succeed again. This is the §2 hazard.
6. **Precedent may carry debt** — `deadline.<id>`'s TTL idiom is being mirrored onto a record with a very
   different lifetime; verify the mirror rather than assuming it transfers.

**6. Adjacent finds** (each absorbed into THIS run's batch — no deferral rows):
1. `docs/components/loom.md` has no "Review keeps catching" dossier while ten sibling components do — the
   improvement loop has no landing site for Loom's lessons. Created at close with this item's classified
   findings.
2. `listInstances` and `runningInstanceCounter` carry near-duplicate list-filter-getmulti-decode bodies with
   divergent error postures (fail-whole vs warn-and-skip), already noted in their own comments as
   deliberately mirrored. Inc 2 removes one of the two copies; the remaining one is left alone (narrowing,
   not widening).

**7. Non-goals.** `ListInstances`' semantics (§4 — deviation from triage §7, with its reason). Any new
control-plane verb or `internal/controlauth` change. The `token.*` / `outbox.*` / `deadline.*` key shapes.
Contract #10 text (§2 establishes no edit is needed). Fires beyond this one — there are none.

---

### Build note — what Inc 1 surfaced (2026-08-29)

**A pre-existing production defect: `RedriveInstance` cannot redrive any instance that actually failed.**

Inc 1's `TestRedrive_ClearsRetentionTTL` was the first test in the tree to drive a *real* terminal
transition and then redrive the result. It failed — and not on the TTL. The atomic batch is rejected:

```
loom: redrive instance "…": substrate: atomic batch rejected: code=400 err_code=10071: wrong last sequence: 4
```

**Mechanism.** The terminal batch deletes `instance.<id>.pattern` (`state.go:372-376`), which writes a KV
delete marker on that subject. `redrive` (`state.go:453-470`) then re-creates the pin with `CreateOnly`,
whose wire form is expected-last-subject-sequence 0 — "this subject must be empty". A subject carrying a
delete marker is not empty, so the server rejects it, and because the pin rides the same `AtomicBatch` as
the status flip, the *whole* redrive is rejected. The operator's only documented recovery path for a failed
flow (the `redrive` control RPC and the `lattice loom` CLI) therefore fails closed, every time, for exactly
the instances it exists to recover.

**It is not caused by this design.** Reproduced with `instanceRetention = 0` — today's shipped behaviour,
no TTL stamped anywhere. Inc 1 only made it visible.

**Why no test caught it.** `TestRedriveInstance_HappyPath_ResumesAtCursor`
(`control_internal_test.go:398`) seeds its failed instance with `putInstance`, so the pin subject is one
that was **never created**; its comment nevertheless claims "(pin already deleted, as production leaves it
at terminal)". A never-created subject accepts `CreateOnly`; a deleted one does not. The fixture could not
reach the state the test says it is testing — standing-checklist item 3 (a negative test needs its positive
vector proven first) and item 5 (a create-only writer bricks the second write), both landing on one test.

**Fix, in this fire** (it owns the terminal batch and the pin lifecycle, so this is in-fire work, not a
filed row — steward §4). The `CreateOnly` is load-bearing: it is the race guard that makes two concurrent
redrives safe. So the guard MOVES rather than disappears — onto `instance.<id>`, which is never deleted and
therefore expresses a clean CAS: `redrive` writes the instance op revision-conditioned
(`BatchOp.HasRevision`/`Revision`) and the pin as an unconditional put. Two concurrent redrives read the
same revision; the first commits and bumps it, the second is rejected. Identical guarantee, on a key with no
delete marker. The shipped happy-path test is rebuilt to drive a real terminal transition first.

**`createInstance` was checked for the same exposure** and does not have it: it also writes both keys
`CreateOnly`, but a re-triggered `instanceId` is rejected by the *instance* record's own presence — which is
Contract #10 §10.9's dedup guard doing its job — before the pin's state matters. Its posture is unchanged by
this fire.

**Upstream corroboration for §3 Inc 1's TTL-clearing claim.** Read at the pin rather than inferred: the
server skips clamping a message TTL up to `SubjectDeleteMarkerTTL` when `MaxMsgsPer == 1`, commenting
"MaxMsgsPer=1 is an exception, because we'll only ever have one message"
(`nats-server@v2.14.0 server/stream.go:6890-6897`). A KV bucket at History:1 is exactly that case, so an
8-day retention is stored verbatim and an untagged rewrite leaves nothing behind to expire.
