# Processor commit OCC: implement the deferred update-revision condition + internal bounded retry

📐 **awaiting-Andrew (ratification)**

## For Andrew (one-look)

**What it does (two lines).** Implements Contract #3 §3.2's *already-mandated* "an `update` is conditioned on
the revision read at step 4" — which the committer currently **defers** (updates commit **unconditioned** →
silent lost-updates under concurrency) — and pairs it with a **Processor-internal bounded retry**
(re-hydrate → re-execute → re-commit) so a benign concurrent conflict is absorbed in-Processor instead of
surfacing `RevisionConflict` to the client.

**Why now / the trigger.** Andrew's question (2026-06-29): *"why do we have the CAS RevisionConflict? why do we
care about the expectedLastSequence of the whole stream? with the processor running multiple lane consumers this
will be a continuous issue."* The investigation **corrects the premise** and surfaces a real latent bug — see
§1. The premise correction matters: the right fix is the opposite of what "whole-stream" implies.

**Fork / contract.** **No frozen-contract change** — part (A) *implements* frozen §3.2 (closes a deferral that
silently violates it); part (B) is transparent Processor behavior (the surfaced error code is unchanged on
retry-exhaustion). **No architectural fork.** One decision for Andrew: **must (A) and (B) ship together** (my
strong recommendation — (A) alone converts silent lost-updates into the very client-facing `RevisionConflict`
storm Andrew fears; (B) alone is inert because nothing conflicts today). I sequence them as one feature, two
increments, (B) landing in the same fire as or immediately before (A)'s enforcement flips on.

---

## 1. The premise, corrected (this is the heart of the answer)

**The substrate does NOT use a whole-stream expected sequence.** `substrate.AtomicBatch`
(`internal/substrate/batch.go:126-131`) conditions **per key** via NATS **`Nats-Expected-Last-Subject-Sequence`**:

- `create` mutation → `Nats-Expected-Last-Subject-Sequence: 0` ("this key must not exist") — **create-once**.
- `update`/`tombstone` with an expected revision → `Nats-Expected-Last-Subject-Sequence: <rev>` — **conditioned**.

There is **no** `Nats-Expected-Last-Sequence` (the whole-stream form) anywhere in the commit path. So **writes to
different keys never serialise and never conflict.** The whole-stream serialization the question worries about
does not exist; the multi-lane-consumer "continuous contention" it implies cannot arise from the stream head.

**Why the per-key CAS exists at all** (its three jobs):

1. **Create-once / uniqueness** — a vertex, aspect, link, *index vertex*, or the op **tracker** is created
   exactly once. A duplicate `create` on the same key conflicts. This is how identity-domain enforces a unique
   email/phone (deterministic **index-vertex** keys) and how the §4.3 op tracker dedups a replayed `requestId`.
2. **Lost-update prevention** — a conditioned `update` asserts the root is still at the revision the script read,
   so two writers of the *same* key serialise (the stale one conflicts) rather than blindly overwriting.
3. **Atomic all-or-nothing** across the op's keys (mutations + tracker + outbox aspect in one batch).

**What the observed `RevisionConflict` actually was.** Driving setup ops for the D1.3 live-data test,
`CreateUnclaimedIdentity` repeatedly failed with `expected=0 … wrong last sequence: 584` (a **create-once**
collision). `CreateUnclaimedIdentity` mints **email + phone uniqueness-index vertices** (deterministic keys —
`packages/identity-domain`), and the setup script **reused the same phone (+15550000000) and deterministic
emails** across identities → the second identity's index `create` collided with the first's already-committed
index key. **Create-once OCC doing its job**, not lane contention, not whole-stream. Making each email/phone
unique fixed it immediately. (A warm-up flavour — the package-install + convergence backlog churning during
bring-up — added noise, but the *determining* fix was unique index values; see §5.)

**So the honest answer:** the CAS is per-key and correct; the conflict observed was a uniqueness constraint
firing on a duplicate. **But** the investigation surfaced the genuinely important thing — §1.1.

### 1.1 The real bug the question uncovered: updates commit UNCONDITIONED

Contract #3 §3.2 (**frozen**) mandates:

> `update` → revision condition = `expectedRevision` if provided, **else the revision read at step 4 (Hydrate)**.

Step 4 **does** capture it (`step4_hydrate.go:163` — `doc.Revision = entry.Revision`). But the committer
**defers** applying it (`step8_commit.go:160-163`):

> *"If no expectedRevision is supplied, the BatchOp goes through unconditioned. … this hardening is deferred;
> only explicit overrides are carried forward."*

**Consequence:** today, two concurrent ops updating the **same key** (e.g. a user op and a convergence
`directOp`/`replyOp` writing the same aspect) **do not conflict — they silently lose-update** (last-write-wins,
no signal). With the **ConsumerSupervisor running concurrent multi-worker pumps across 4 lane consumers**
(`consumer_supervisor.go` — `workerCount`/`spec.Workers`; the `system` lane is the engines' convergence
firehose), this is a live correctness gap, currently *masked* precisely because the OCC is off.

**This reframes Andrew's instinct correctly:** the symptom he expects ("continuous RevisionConflict") is what we
*would* get **if we naively turned §3.2 on without internal retry** — every benign same-key race would bounce to
the client. The fix is to turn §3.2 on **and** absorb the benign conflict in the Processor.

---

## 2. The shape

Two coupled parts, both in `internal/processor` (P2 — the Processor is the sole Core-KV writer; nothing here
changes that). No engine reads, no contract change.

### (A) Condition `update`/`tombstone` on the hydrated revision (implement §3.2)

- In the committer (`step8_commit.go`), for an `update`/`tombstone` mutation **with no explicit
  `ExpectedRevision`**, set `HasRevision=true, Revision=<hydrated root revision>` from the step-4 snapshot.
  The hydrated revision is already on the in-memory document (`step4_hydrate.go:163`); thread it onto the
  `Mutation` (or look it up from the hydrated set by root key at commit time) so the committer stops emitting an
  unconditioned put.
- **Restore semantics preserved** (§3.2): an `update` that sets `isDeleted:false` on a tombstoned root still
  carries the tombstone's revision — the condition is "the root is at the revision I read", tombstoned or not.
- **`create` is unchanged** (already `CreateOnly`). **Explicit `expectedRevision`** (compensating ops) is
  unchanged — it still wins over the hydrated default, exactly as §3.2 reserves.
- **Multi-aspect caveat (acknowledged, not regressed):** each aspect/root has its own NATS subject-sequence, so a
  multi-key op conditions **each key on its own** hydrated revision — already what the batch does. True
  *cross-key* atomic-OCC ("all these revisions together or none") remains the parked `UpdateMetaVertex` item
  (lattice.md parking lot, ★ M+); this design does **not** need it (per-key conditioning is the §3.2 contract and
  is sufficient — the *batch* is still atomic; only the *condition* is per-key).

### (B) Processor-internal bounded retry on a retryable commit conflict

When `AtomicBatch` returns `ErrAtomicBatchRejected`, the commit path (`commit_path.go:306`) currently classifies
and **surfaces** `RevisionConflict` to the client. Insert a **bounded retry loop** *before* surfacing, that
**re-runs the op from hydration**:

```
for attempt in 1..maxAttempts:               # maxAttempts = 3 (config)
    re-hydrate (step 4)  → fresh roots + fresh revisions
    re-execute (step 5)  → Starlark against fresh state (deterministic; == a client resubmit)
    re-validate (step 6)
    commit  (step 8)
    if ok: return committed
    if conflict is NOT retryable (see discrimination): break → surface
    sleep small jittered backoff (≪ lane deadline)
surface RevisionConflict   # genuinely hot/contended key — the honest terminal
```

**Why re-execute, not just re-commit:** a conflict means the underlying state moved, so the script must re-run
against the new state — re-committing the *stale* mutation set would be wrong (it'd reintroduce the lost-update).
Re-execution against fresh state is **exactly what a client retry does**, just without the network round-trip and
re-auth. It is **idempotent and safe** because the failed attempt committed **nothing** (atomic all-or-nothing →
no tracker, no outbox, no mutations landed), and event nonces/IDs are regenerated on the fresh execution.

**Conflict discrimination (load-bearing — retry only what retrying can fix):**

| Failing op | Meaning | Action |
|---|---|---|
| conditioned `update`/`tombstone` (rev mismatch) | the root moved under us | **RETRY** (re-hydrate gets the new rev) |
| business `create` (create-once) collision | a **uniqueness** violation (duplicate index/key) | **SURFACE** — retrying can't help; it's a semantic/domain reject, surfaced as today |
| the op **tracker** create-once collision | `requestId` **replay** (redelivery of an already-committed op) | **dedup** — already handled (`commit_path.go:306-316`: tracker-present ⇒ idempotent prior result, not a conflict) |

The classifier already extracts the failing key (`guessConflictingKey`, `step8_commit.go:205/395`); the design
keys the retry/surface decision off the failing op's kind. **Only the conditioned-update conflict is retried** —
so a duplicate-email `create` still fails fast with a clear domain error, and a replayed `requestId` still dedups.

**Bounding (quantified with its constraint):** `maxAttempts=3` with jittered backoff capped well under the
**lane deadline** the commit context already carries (`step8_commit.go:70` Timeout; `batch.go:91` "ctx bounds …
the lane SLA"). Retry never exceeds the lane's SLA — on exhaustion it surfaces `RevisionConflict` (a genuinely
hot key is the honest terminal, and the metric below makes it visible). This **generalises the existing one-shot
retry** the commit path *already* does for the §10.7 auto-complete injection (`commit_path.go:488` — "retry the
injection once with the newer revision"): same instinct, made general and bounded.

**Observability:** a `commitRetries` / `commitRetryExhausted` heartbeat counter (Contract #5 §5.4, mirroring
Weaver's `sweepReclaimsSuppressed`) so sustained hot-key contention is operator-visible rather than silent.

---

## 3. Reconciliation with the existing mental model (pre-empt "but didn't we…?")

- **"Don't we already have OCC?"** Half. `create` is conditioned (create-once); **`update` is not** — the §3.2
  hardening is explicitly deferred (`step8_commit.go:160-163`). This design lands the deferred half.
- **"Isn't this a whole-stream lock problem?"** No — `batch.go` proves per-**subject** conditioning. The premise
  that we lock the whole stream is incorrect; correcting it is half the value here.
- **"Won't conditioning updates cause the RevisionConflict storm I'm worried about?"** Only if surfaced raw —
  which is why (B) ships with (A). With internal retry, benign concurrency is absorbed; only a persistently-hot
  key surfaces, and that surfacing is *correct* (and now metered).
- **"New state?"** None. Re-hydrate/re-execute reuse the existing steps 4-8; the retry is a bounded loop around
  them. The hydrated revision is *already* read and discarded — we just stop discarding it.
- **Roadmap honesty:** cross-key *atomic* OCC (one condition spanning several keys) stays the parked
  `UpdateMetaVertex` ★ item — deliberately out of scope; per-key conditioning satisfies §3.2 and the lost-update
  case the convergence loop actually creates (per-aspect writes are per-key).

---

## 4. Alternatives considered (earn the recommendation)

1. **Status quo (updates unconditioned).** Silent lost-updates; violates frozen §3.2. **Rejected** — a
   correctness bug, not a choice.
2. **Implement §3.2 OCC but surface `RevisionConflict` (no internal retry).** Pushes retry onto *every* client
   and is exactly Andrew's "continuous issue". **Rejected** — but note this is the trap (A)-without-(B) walks into.
3. **Pessimistic per-key serialisation (Processor-side lock/queue per root key).** Avoids conflicts by
   single-writer-per-key. **Rejected:** reinvents, pessimistically, the per-key OCC the substrate already gives
   for free; adds a hot in-process lock map; and a single-instance lock does nothing across multiple Processor
   replicas, whereas optimistic retry is correct for both. *Variant that could beat?* An in-process
   **same-key coalescing** within one instance would cut *self*-contention (two workers, same key) cheaply — but
   it's a latency optimisation layered *on top of* the OCC, not a replacement, and premature until the metric in
   §2 shows self-contention is real. Filed as a possible follow-on, not built.
4. **Unbounded retry.** Livelock risk + lane-SLA breach on a truly-hot key. **Rejected** — bound to 3 within the
   lane deadline; exhaustion surfaces honestly.
5. **Reduce convergence write-amplification instead** (so hot keys are rarer). Complementary — Weaver's reclaim
   backoff (`04c7689`) already trims this; it lowers conflict *frequency* but doesn't make a conflict *correct*.
   Keep both; this design handles the conflict, that one reduces its incidence.

---

## 5. Why my live-test setup hit it — fully root-caused (a red herring)

**The triggering incident was NOT a concurrency bug or a lost-update — it was the identity uniqueness
constraint working as designed**, and the design below is motivated by an *unrelated* code-verified gap (§1.1),
not by this incident. Stated plainly so the record is exact.

Reproduced deterministically on a fresh stack and confirmed by fetching the colliding message from NATS:

- `CreateUnclaimedIdentity` mints two deterministic **dedup-index** vertices —
  `vtx.identityindex.<sha256NanoID("email:"+email)>` and `…("phone:"+phone)>` — as `create`-once
  (`packages/identity-domain/ddls.go:382-424`). They are the email/phone **uniqueness** index.
- My setup script **reused one phone (`+15550000000`) for every identity** (and deterministic emails). So the
  *first* identity (Larry) committed its phone/email index; the *second* (Linda, same phone) hit
  `create`-once on the **already-existing phone index** → `RevisionConflict`. The original "first run failed"
  was actually the **second** identity in the run colliding on the shared phone — I misattributed it to op 1.
- Smoking gun: `nats stream get KV_core-kv 584` → `vtx.identityindex.<…>` `{"contactType":"email",…}`; a
  same-phone-different-email submit fails on `586` (the **phone** index); a unique-email-unique-phone submit
  **commits**. `584`/`586` = the email/phone index sequences — exactly the original numbers. **Per-subject
  create-once, never whole-stream; the uniqueness constraint, never convergence contention; fixed by unique
  values, never by quiescing orchestration** (with orchestration *running*, unique values commit fine).

**So (B) is NOT motivated by this incident** (it wasn't an update conflict at all — it was a create-once
uniqueness reject, which (B) correctly **surfaces**, never retries). (B)'s justification is independent and
code-grounded: once (A) conditions updates per §3.2, genuine concurrent same-key *updates* (a
`directOp`/`replyOp` and a user op on the same aspect, across the concurrent lane pumps) **will** conflict, and
(B) absorbs the benign ones. The investigation's value was surfacing that (A) gap, not the red-herring incident.

---

## 6. Migration / compatibility · test strategy

- **Compatibility:** `create` and explicit-`expectedRevision` paths byte-identical. The only behavior change is
  that an unconditioned `update` becomes conditioned — which is the frozen-contract behavior, and which no
  correct caller depends on *not* having (depending on last-write-wins is the lost-update bug).
- **Sequencing:** land **(B) internal retry first (dark — nothing conflicts yet, so it's a no-op)**, then **(A)
  flip update-conditioning on** in the next fire — so the moment updates start conditioning, the absorber is
  already there. (This is the dead-scaffolding-safe order: (B) is inert-but-correct until (A); (A) without (B)
  is the storm. Ship (A) only once (B) is in `main`.)
- **Tests:** (unit) committer emits `Nats-Expected-Last-Subject-Sequence=<hydratedRev>` for a default update,
  `0` for a create, explicit override still wins; (unit) retry loop — retryable update-conflict re-hydrates and
  succeeds on attempt 2, create-once collision surfaces without retry, tracker-replay dedups, exhaustion surfaces
  after N; (e2e, ephemeral stack) two concurrent ops updating the **same aspect** — without (A) one is lost
  (regression-documents the bug), with (A)+(B) both apply (the loser re-executes against the winner's state) and
  neither client sees an error; (e2e) a genuine duplicate-email `CreateUnclaimedIdentity` still fails fast.
- **Gate 3 / adversarial:** confirm internal retry cannot be used to bypass auth (re-execution re-runs step 3)
  or double-apply (atomic all-or-nothing + tracker dedup); a self-adversarial pass on the re-execution
  idempotency is the pre-build gate (recorded before build-ready).

---

## 7. Decomposition for the Steward

- **Increment 1 — internal bounded retry (B), dark.** The retry loop + conflict discrimination + the metric,
  around the existing steps 4-8. Inert today (updates unconditioned ⇒ nothing retryable), so zero behavior change
  — proven by the retry unit tests against a fake conflicting committer. Generalises the auto-complete one-shot.
- **Increment 2 — implement §3.2 update-conditioning (A).** Thread the hydrated revision onto default
  update/tombstone commits; the lost-update e2e flips from "one lost" to "both apply". Full review (correctness
  plane). Ships only with Increment 1 already in `main`.
- **(Follow-on, not now)** same-key in-process coalescing *iff* the `commitRetries` metric shows real
  self-contention.

---

*Designer: Winston (lattice-designer) · 2026-06-29 · grounds: `internal/substrate/batch.go` (per-subject CAS),
`internal/processor/step8_commit.go` + `commit_path.go` + `step4_hydrate.go`, `internal/substrate/consumer_supervisor.go`
(concurrent lane pumps), Contract #3 §3.2 (`03-mutation-batch-event-list.md`), the live-data RevisionConflict
observed driving the D1.3 setup.*
