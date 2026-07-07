# Architecture review — Weaver (2026-07-06)

**Scope:** Weaver only (`internal/weaver/**` incl. `control/` + `planner/`, `cmd/weaver`,
`cmd/lattice/weaver`, plus `internal/pkgmgr`'s weaver-target authoring surface and Weaver's seams) — a
scoped follow-up to the [2026-07-02 full-platform review](arch-review-2026-07-02.md), which rated Weaver
"healthy (healthiest engine; documentary debt)". Four days of heavy shipping landed since (planner-mandate
Fires 5–8 + Fire 6 R1 + Fire 9 Inc1, FR30 control-plane capability auth, the external-gap reclaim, the
Contract #10 shard), so this re-audit checks both the new code and the fate of the 07-02 findings.

**Method:** read-only; three independent sub-agents (purpose/charter/docs · contract conformance ·
invariants/tests/seams), each reporting with file:line evidence, synthesized by the lead. Every
load-bearing claim below was re-verified against the pinned source before entering this report (the
ratify-commit archaeology, the claimId mint, the `cmd/weaver` checker wiring, the past-`freshUntil`
publish, the validator-mirror gaps, and the `aggregateStatus` severity set were each checked directly).
Nothing was executed against the running stack; no tests were run.

---

## Verdict: **healthy** — the platform's best-conformed engine; documentation drifted, and the gap is widening

The code got stronger since 07-02 and audits clean everywhere it matters: P2 is exemplary (still the only
engine with zero Core-KV point reads — survived Fires 5–8), every auth/config surface fails closed, the
FR30 control plane now enforces real capability authorization by default, the planner mandate is
implemented to the letter of its ratified contract text, reserved-key disjointness is proven end-to-end,
and the heartbeat is honestly derived (Weaver was never one of the 07-02 false-green engines). **No ★★★
findings.** The candidate ★★★ — a lost unratified contract edit implemented in code — was disproven by
commit archaeology (§ "The staged-edit question" below).

The debt is concentrated in `docs/components/weaver.md` + `docs/components/augur.md` and a short tail of
small mechanism gaps. The doc problem is getting *worse* with velocity: weaver.md was last updated at
`e0737db` (2026-07-05), and **14 Weaver-touching commits have landed since without the same-commit doc
update the page's own header mandates** — including a security-plane posture change. Both doc
self-contradictions flagged on 07-02 are still open. The staleness has real cost: the FR30 falsehood
seeded this very audit's working premise ("Weaver still allow-alls its control plane") twice before the
code disproved it.

### What is healthy (verified, not assumed)

- **P2 / read-path discipline.** Every KV write in non-test code targets `weaver-state` or Health KV;
  every read targets `weaver-state` or `weaver-targets`; the registry hydrates via a durable CDC consume
  of `vtx.meta.>` ([registry.go:401](../../internal/weaver/registry.go)), never a point read; the only
  stream publishes are `ops.<lane>` ([actuator.go:99](../../internal/weaver/actuator.go)) and
  `schedule.>`. The planner State is a pure row derivation — the goal-regression work explicitly avoided
  a live aspect read ([registry.go:129](../../internal/weaver/registry.go) "no new Weaver Core-KV read").
  Transport backstop: the weaver NATS user denies `$KV.core-kv.>` + the stream-admin verbs
  ([nats-server.conf:56-65](../../deploy/nats-server.conf)); the core-kv KVPut denial is natsperm-pinned
  ([conf_test.go:198](../../internal/natsperm/conf_test.go)).
- **FR30 control-plane capability auth is live and default.** `cmd/weaver` wires the shared
  `controlauth.CapabilityKVChecker` ([main.go:107](../../cmd/weaver/main.go)) with
  `LATTICE_AUTH_MODE=capability` as the default ([main.go:170](../../cmd/weaver/main.go)) and a JWT
  actor verifier ([main.go:111,139](../../cmd/weaver/main.go)); a wiring failure aborts startup. The
  checker has no fail-open branch ([checker.go:37-42,125-155](../../internal/controlauth/checker.go));
  outcome-level proof exists (`internal/controlplaneauthz` operator-allowed/intruder-denied/anonymous-denied).
  Weaver is at parity with the Refractor posture the 07-06 Refractor review called "enforced end-to-end".
- **Fail-closed spot checks all pass:** unknown playbook action → `PlaybookConfigError` error; malformed
  `freshUntil` → `RowDataError` + no schedule; reserved `fired` targetId refused loudly; non-bool
  `inflight_<g>` → surfaced but dispatchable (safe side); admission denial → `NakWithDelay` with no mark,
  no plan, no issue; oscillation → freeze both targets + one `TargetOscillation` issue, report-once;
  registry install-validation rejects loudly; `seedDisabledTargets` failure aborts `Start`; empty
  `ActorKey` refuses startup.
- **Reserved-key disjointness proven end-to-end:** `substrate.Alphabet` has no underscore
  ([nanoid.go:13](../../internal/substrate/nanoid.go)); entity segments must be valid NanoIDs; gap
  columns must start `missing_`; the sweep and gauges skip `__control`/`__count`/`__effect`; `t1.`
  prefix deletes can't match `t10.`.
- **Contract conformance is build-to almost everywhere** — the frozen text often names the exact
  constants/functions the code carries (`markTTLBackstopFactor`, `resolveParam`, `splitRowKey`). All
  three reserved key shapes (`__control`/`__count`/`__effect`) are contract-blessed (ratified
  2026-07-04) and implemented to the letter. §10.4 is clean end-to-end. Contract #4 requestId
  derivations verified at every site, namespaced-disjoint. Contract #5: key/doc shape, honest §5.3
  aggregation ([health.go:326-340](../../internal/weaver/health.go)), and §5.6 heartbeat TTL now
  implemented (`KVPutWithTTL`) — retiring the 07-02 "no component implements §5.6" claim for Weaver.
- **Charter holds:** zero domain literals in non-test code (all engine-recognized columns are generic
  `missing_`/`inflight_`/`maxretries_`/`freshUntil`/`priority` conventions); no adapter handles; no
  cypher runtime; external I/O only via triggerLoom→externalTask→bridge, exactly as chartered
  (brainstorm #122).
- **Test posture is strong:** 33 test files (+3 control, +1 planner), ~230 test funcs, no flaky
  markers; the e2e drives an embedded real NATS through registry replay, dispatch OCC, mid-flight-kill
  restart, sweep orphan legs, and the full temporal loop. Most audit-candidate gaps turned out covered
  (schedule-publish failure, `__effect` GC legs, admission grant-TTL refund, oscillation report-once,
  exhausted-budget escalation from both legs, pinned-leg reuse).

---

## The staged-edit question — resolved benign

`weaver.md:471` says the Fire-6 Increment-3 revision ("installed catalog" → per-gap `actions`) had its
"contract text staged uncommitted awaiting Andrew (2026-07-05)" — yet today's tree is clean. The
archaeology shows **no governance loss**:

| When (2026-07) | Commit | What |
|---|---|---|
| 04 | `ad76e21` | Proposal committed with PROPOSED markers (§10.3 reserved shapes + §10.8 planner extension) |
| 04 | `ba1c66d` | Andrew ratifies the planner mandate (flips PROPOSED→ratified) |
| 05 19:30 | `8c50ce1` | **Ratify session** — "All three staged Weaver proposals ratified in one session", incl. the per-gap `actions` catalog revision |
| 05 20:14 | `2752dcb` | Contract #10 sharded; §10.8 moved byte-lossless (shard text matches the ratify diff verbatim) |

The frozen §10.8 now carries the per-gap text ("bounded goal regression over the gap's **declared
`actions` catalog** … a global ops-derived auto-catalog is **reserved**, not implied",
[10-orchestration-weaver.md:291-296](../contracts/10-orchestration-weaver.md)), and the code dispatches
from exactly that (`resolveGoalAction` synthesizes over `ga.Actions`,
[strategist.go:419-475](../../internal/weaver/strategist.go)). The weaver.md parenthetical is simply a
stale pre-ratification marker (→ finding W1).

---

## Findings (ranked)

### W1 · ★★ · docs · weaver.md + augur.md contradict shipped code in ~12 spots — one doc-only fire

The two component pages a designer or auditor grounds on assert falsehoods about shipped behavior,
including on the security plane. Worst first:

1. **FR30 section is false** — `weaver.md:766-770`: "ships a `StubCapabilityChecker` (allow-all…) …
   Full Capability-KV integration … build-pending". Reality: the real checker + JWT actor verification
   are default-wired ([main.go:107,111,138-139,170](../../cmd/weaver/main.go)). This seeded this
   audit's own wrong premise — the exact failure mode component docs exist to prevent.
2. **claimId "always empty"** (`weaver.md:829`) — false; a NanoID claimId is minted at every mark
   CAS-create ([state.go:121-135](../../internal/weaver/state.go)) and consumed by the stable
   taskId/instanceId derivations ([actuator.go:148-162](../../internal/weaver/actuator.go)). The doc
   contradicts itself (:830 describes claimId preservation). **Flagged 2026-07-02; still open.**
3. **"a past `freshUntil` never schedules"** (`weaver.md:156-158`) — false; a past instant is published
   verbatim and fires immediately, deliberately ([temporal.go:140-148](../../internal/weaver/temporal.go)).
   Two code comments still say "future" while the body doesn't (`temporal.go:88`, `evaluator.go:71`).
   **Flagged 2026-07-02; still open.**
4. **The Fire-3 planner section is frozen in time** — `weaver.md:302-303,320-321`: "imported by nothing
   yet … the Strategist does not call this package" vs `planner.Synthesize` live in the Strategist
   ([strategist.go:449](../../internal/weaver/strategist.go)) — the doc's own later sections say so.
5. **Lane-2 contradiction** — `:845` "Lane-2 on-demand evaluation (built, unexercised)" vs `:836/:792`
   "Phase 3"; no `events.>` consumer exists anywhere in `internal/weaver` — :845 is the false one.
6. **Stale staged-uncommitted parenthetical** (`:471`) — see the archaeology above.
7. **Fire 9 Inc1 undocumented** — the exhausted-budget → Augur escalation / `GapBudgetExhausted`
   standing issue + `augur.model` threading shipped
   ([evaluator.go:833-909](../../internal/weaver/evaluator.go),
   [strategist.go:544-549](../../internal/weaver/strategist.go)); weaver.md still says "Fire 9 …
   remains" (:620, :661, :838) and its suppression section still ends at the pre-escalation terminal;
   `augur.md:170` still calls the `exhausted` trigger "📋 Designed, follow-on" — now false.
8. **External-gap reclaim undocumented + two overbroad sentences** — the stale-external-gap prompt
   reclaim with **fresh** claimId shipped ([evaluator.go:442-489](../../internal/weaver/evaluator.go),
   [reconciler.go:564-572](../../internal/weaver/reconciler.go)); `weaver.md:830`'s "claimId … preserved
   across **every** reclaim" is now overbroad, and the backoff class is **three** collapse-only actions
   (proposedOp included, [reconciler.go:487-507](../../internal/weaver/reconciler.go)), not "two"
   (`engine.go:76-77`'s comment shares the undercount).
9. **Module-boundary line false as written** — `:819/:780` "imports only `substrate/*`" vs non-test
   imports of `internal/guardgrammar`, `internal/healthkv`, `internal/controlauth` (+ `cmd/weaver`'s
   `bootstrap`/`pkgmgr`); `boundary_test.go:34-77` pins the true, narrower rule (no other engine,
   transitively; no raw NATS imports). The doc contradicts itself (:309 names guardgrammar).
10. **Health row under-lists shipped metrics** (`:833` — missing `sweepReclaimsSuppressed`,
    `effectMismatches`, `plannerShadow.*`, `contractionTrajectory`, `admissionAdmitted/Deferred`,
    all emitted in [health.go:207-237](../../internal/weaver/health.go)); In/Out table omits the
    sweep-schedule legs; Overview `:36` "only direct write is weaver-state" undercounts Health KV.
11. **Fire-7 row understates the oscillation auto-freeze** — "zero dispatch-decision change" vs
    freeze-both-targets via the `__control` seam ([oscillation.go:62-65](../../internal/weaver/oscillation.go),
    [control.go:238-247](../../internal/weaver/control.go)).
12. **augur.md's capture loop describes the retired shape** — `:58/:130` `pattern` + "triggerLoom of the
    `augurReasoning` externalTask" vs the directOp dispatch with `op`/`adapter`/`replyOp` defaults and
    **no `Pattern` field** on `AugurPolicy` ([registry.go:285-292](../../internal/weaver/registry.go),
    [strategist.go:477-487](../../internal/weaver/strategist.go)) — same family as the open
    contract-side row (W5).

### W2 · ★★ · code (security hygiene) · three control packages keep a silent allow-all fallback

All three control planes (weaver / refractor / loom) still carry a **package-local**
`StubCapabilityChecker` reachable through a silent nil-fallback:
[weaver control/service.go:120-122](../../internal/weaver/control/service.go) seeds the stub when
`capability == nil`; refractor's Service **constructs** with the stub and relies on a later setter;
loom mirrors weaver. The local stub Info-logs with **no Health signal** — unlike the sanctioned
`AuthModeStub` inside the real checker, which Warn-logs and raises the periodic `stub-control-active`
alert ([checker.go:157-169](../../internal/controlauth/checker.go)). Unreachable in production today
(every binary wires a real checker and aborts startup on failure), and the transport bounds who can
reach the subjects — but a future wiring regression would fail **open and quiet**. Fix: delete the three
local stubs (dev/test is already served by `AuthModeStub`) or make the nil-fallback refuse; sweep the
stale "Epic 3 wires the real check" / "in production, a stub allow-all" comments in those packages
(`weaver/control/capability.go:10-13`, `weaver/control/service.go:97-99`, refractor + loom twins).

### W3 · ★★ · code · pkgmgr ↔ engine validation parity has three holes

The install-time mirror doctrine is "a package that would fail the engine's CDC-load validation fails
loudly at install instead." Three splits violate it:

- **pkgmgr accepts / engine rejects:** the engine token-validates augur `model`
  ([registry.go:912](../../internal/weaver/registry.go)); the pkgmgr mirror validates only
  `{op, adapter, replyOp}` ([orchestrationguard.go:159](../../internal/pkgmgr/orchestrationguard.go)) —
  a dotted/spaced `model` installs clean, then the **whole target** is rejected at CDC load.
- **pkgmgr rejects / engine accepts:** pkgmgr restricts surface-gap `issueSeverity` to
  `{warning,error}` ([orchestrationguard.go:223-225](../../internal/pkgmgr/orchestrationguard.go));
  the engine passes it verbatim to the issue cache
  ([evaluator.go:200-209](../../internal/weaver/evaluator.go)) while `aggregateStatus` recognizes only
  `error`/`warning` ([health.go:326-340](../../internal/weaver/health.go)) — a raw-op-authored target
  with `issueSeverity:"critical"` yields a heartbeat carrying an issue while reporting `healthy`,
  breaking the "issues empty iff healthy" rule the code itself states.
- **Audit-trail hole in the AI-authored surface:** the capability-materializer's unknown-field
  rejection inspects **top-level keys only**
  ([capabilitymaterializer.go:325-338](../../internal/pkgmgr/capabilitymaterializer.go)); a gaps entry
  smuggling `goal`/`actions` is silently dropped by `json.Unmarshal` and materializes as a plain gap —
  no privilege escalates (the restricted artifact shape holds), but the §5 stored-invalid audit trail
  the check exists for is bypassed. Same class for nested `StepArtifact` keys.

### W4 · ★★ · code or contract · cross-package targetId uniqueness: frozen text says install-validated; built is runtime keep-first — and the interleaving hazard isn't actually prevented

Frozen §10.8: "**targetId uniqueness across installed targets is install-validated** … two packages must
not collide in the shared bucket" ([10-orchestration-weaver.md:20-21,147-150](../contracts/10-orchestration-weaver.md)).
Built: pkgmgr checks only within-package duplicates and says so
([orchestrationguard.go:74-75](../../internal/pkgmgr/orchestrationguard.go) "cross-package collision is
caught at runtime"); the runtime catch is registry keep-first + `TargetRejected` Health alert
([registry.go:551-557](../../internal/weaver/registry.go)). That alert governs only the **registry** —
the colliding package's **lens** still projects rows under the same `<targetId>.` prefix into the shared
`weaver-targets` bucket, so the surviving target's filtered consumer ingests foreign rows: exactly the
interleaving the frozen sentence says install-validation exists to prevent. Either build the install-time
check (kernel scan for existing `meta.weaverTarget` targetIds) or stage a contract amendment to the
as-built posture.

### W5 · ★★ · contract text · the open reconciliation row undercounts — 3 of the 07-02 five spots remain, plus a small new cluster

The open board row `contract-10-weaver-text-reconciliation` says 2 of 5 spots remain. It's 3:

- **Augur block** (`10-orchestration-augur.md:27-38` + the §10.8 example): still specs `pattern` +
  triggerLoom + "resolves to an installed meta.loomPattern" validation; the engine has no `Pattern`
  field and dispatches a directOp — a package author's `pattern` is silently dropped. (Known, tracked.)
- **§10.2 read-path sentence**: "read only by Weaver … never on the read-path" vs five P5 readers in
  `cmd/loftspace-app`. (Known, tracked.)
- **§10.8 anti-storm cross-ref** — **not reconciled, contrary to the row's claim**: §10.8:233-235 still
  says triggerLoom/assignTask are "documented rare-double", while §10.3 explicitly supersedes that
  disposition ([10-orchestration-substrate.md:245-247](../contracts/10-orchestration-substrate.md)); the
  code follows §10.3 (claimId-seeded collapse).

New small reconciliations to fold into the same staged edit: the reclaim **backoff** + conditional TTL
widening are unrecorded in §10.3 (which calls the TTL factor "a constant";
[reconciler.go:523-531](../../internal/weaver/reconciler.go) widens it for paced reclaims); the mark's
"(+ plan hash)" clause is dormant (no plan hash exists; the mark carries the ref only);
`inflight_*`/`maxretries_*` are §10.2-recognized columns blessed only by a §10.3 aside (`freshUntil` and
`priority` both earned §10.2 riders); stale "build-pending" banners (Fires 1–8 + 9-Inc1 shipped);
"K config-tunable" vs the constant ([state.go:396](../../internal/weaver/state.go)); "pure function of
(row, catalog, `__effect` window)" overstates goal-synthesis inputs (synthesis consumes no `__effect`;
only candidate ranking does); the oscillation **auto-freeze** is contract-silent (engine-autonomous
disable deserves a sentence).

### W6 · ★ · code · residual hardening pins

- Health issues omit Contract #5 §5.5's required `since` timestamp
  ([health.go:39-43](../../internal/weaver/health.go) vs
  [05-health-kv.md:122-127](../contracts/05-health-kv.md)) — likely platform-wide; verified for Weaver.
- Two natsperm pins absent: weaver isn't in the weaver-targets denied-put set
  ([conf_test.go:256](../../internal/natsperm/conf_test.go)) nor the `KV_core-kv` stream-admin
  side-channel subtest (:478), though the conf carries the denies.
- Five untested arms (none security-critical): `seedDisabledTargets` error abort; disable/enable
  partial-failure ordering (the fail-safe-to-inert sequences are comment-specified, unpinned — and the
  `Pause`/`Resume` bool returns are silently discarded, benign because the `__control` marker is the
  authority); `releaseCompletedLeg` revision-conflict skip; `freezeOscillatingPair` Disable-failure leg.
- `effectsCatalog()` is dead scaffolding ([registry.go:1203-1231](../../internal/weaver/registry.go),
  zero non-test consumers) whose comment still cites the superseded global-catalog framing the contract
  now marks RESERVED — delete or re-comment.
- Least-privilege nit: the weaver NATS user may publish its own `lattice.ctrl.weaver.>` subjects
  ([nats-server.conf:60](../../deploy/nats-server.conf)) — the responder needs only subscribe +
  allow_responses.
- One history-narration comment ([reconciler.go:389](../../internal/weaver/reconciler.go) "moved ahead
  of … (was below …)") — banned by house rules.

### W7 · ★ · feature gap · the `admission` block has no package-authoring path

The §10.2 admission rider (dispatch pacing) is engine-complete, but `internal/pkgmgr` has no authoring
surface for it — only a raw-JSON-installed target can declare admission today, and unlike the
`Candidates` gap (which weaver.md:838 explicitly names as an unbuilt pkgmgr surface) this one is
unacknowledged. Named consumer for the tail: any vertical target pacing a vendor adapter (LoftSpace's
bgcheck/payment external-call gaps are the reference shape).

---

## Prior-record corrections

- **07-02 "exhausted trigger + `augur.model` parsed-but-dead" → CLOSED** by today's Fire 9 Inc1
  (board Done log already records it). The engine now escalates a spent budget to the Augur or raises
  `GapBudgetExhausted` from **both** dispatch legs.
- **07-02 "no component implements the §5.6 heartbeat TTL" → stale for Weaver**
  (`KVPutWithTTL`, [health.go:276](../../internal/weaver/health.go)).
- **07-02 "three engines ride green heartbeats with error issues" never included Weaver** — status is
  derived (`aggregateStatus`), directly tested.
- **The open board row `contract-10-weaver-text-reconciliation` needs its count corrected** (3 spots
  remain, not 2 — see W5).

## Seams (Weaver-side)

| Seam | Counterpart | State |
|---|---|---|
| `weaver-targets` | bootstrap provisions ([primordial.go:106-109](../../internal/bootstrap/primordial.go)); Refractor lens targets write; Weaver reads | ✅ agree; the reserved-bucket guard deliberately excludes it as a legitimate multi-package lens target while protecting `weaver-state` ([bucketguard.go:33-41](../../internal/pkgmgr/bucketguard.go)); refractor-writes natsperm-pinned. Residue: the shared-prefix interleaving on targetId collision (W4) |
| `core-schedules` | bootstrap provisions with `AllowMsgSchedules` + `MaxMsgsPerSubject:1` ([primordial.go:219-230](../../internal/bootstrap/primordial.go)) | ✅ agree; `weaver-temporal` and `weaver-sweep` filters disjoint by construction; bridge is the other tenant |
| `ops.<lane>` → Processor | §2.1 class inference | ✅ agree; Weaver omits `class`, the Processor resolves via the reverse index and fails **loud** on ambiguity; service-actor no-authContext submits are the sanctioned posture (parity-tested processor-side) |
| Health KV | Contract #5 consumers | ✅ agree; honest aggregation; §5.6 TTL live; `health.weaver.consumer-state.<name>` is sanctioned as schema-doc Category C, not frozen-#5 body — worth a line when #5 hardens (folded into W5's list) |
| `events.weaver.>` | the Chronicler's F2 premise | ⚠️ producer does not exist — nothing publishes it and the weaver NATS user has **no** `events.>` grant; known + tracked since 07-02 (Chronicler-side prerequisite, not a Weaver defect) |
| capability-materializer | pkgmgr validation reuse | ✅ restriction holds (no goal/candidates/augur/mode/admission surface; top-level unknowns hard-fail) — with W3's nested-field audit-trail caveat |

## Out-of-scope observations (not Weaver rows)

- `docs/contracts/10-orchestration-surfaces.md:62` carries "(UNCOMMITTED — pending Andrew's review)"
  **inside a committed file** (the Loom-lane row the ratify commit deliberately left pending). Under the
  house convention "pending proposal = uncommitted diff", a committed file self-describing as
  uncommitted is contradictory — Loom-lane cleanup.
- The §5.5 `since` omission (W6) is probably platform-wide; only Weaver was verified here.

## Proposed board rows — NOT filed (Andrew's say-so required)

Lane: **lattice** (Weaver is platform). Dedup note: `[Weaver] inflight_<g>-as-external-gap-marker
unenforced` (★ S) already exists and is not duplicated; `contract-10-weaver-text-reconciliation` already
exists — W5 proposes replacing its What text rather than a new row.

| Item | What | Imp | Size | 📋 |
|---|---|---|---|---|
| **[Weaver] weaver.md + augur.md truth reconciliation** | The two component docs contradict shipped code in ~12 spots — worst: the FR30 section still claims an allow-all stub "build-pending" (the real capability checker + JWT actor verify are default-wired); claimId "always empty" and past-`freshUntil` "never schedules" (both false, both flagged 07-02, still open); the Fire-3 planner section says the Strategist never calls the planner; lane-2 "built" vs "Phase 3"; "imports only substrate/*"; exhausted-escalation, external-gap fresh-claimId reclaim + the 3-action backoff class undocumented; augur.md still specs the retired pattern/triggerLoom capture. Doc-only fire. | ★★ | S | 📋 |
| **[Platform] delete the control-plane local allow-all stubs** | All three control packages (weaver/refractor/loom) keep a package-local StubCapabilityChecker behind a silent nil-fallback (weaver seeds it on nil; refractor constructs with it), Info-logging with no Health signal — unlike the sanctioned AuthModeStub (Warn + stub-control-active alert). Unreachable in prod today, but a wiring regression fails open+quiet. Delete the local stubs (AuthModeStub already serves dev/test) or make nil refuse; sweep the stale "Epic 3"/"in production, a stub" comments in those packages. | ★★ | S | 📋 |
| **[Weaver/pkgmgr] validator-mirror parity (model · issueSeverity · nested unknowns)** | Three install/runtime validation splits: pkgmgr's augur mirror omits the `model` token the engine validates (a bad model installs clean, whole target rejected at CDC load — mirror doctrine is fail-at-install); surface `issueSeverity` is pkgmgr-restricted to warning/error but engine-unvalidated, and aggregateStatus never escalates an unknown severity — an issue-carrying heartbeat can report healthy; the capability-materializer's unknown-field rejection doesn't recurse into gap/step entries, silently downgrading smuggled goal/actions keys instead of stored-invalid. | ★★ | S | 📋 |
| **[Weaver] cross-package targetId uniqueness — build the install check or amend §10.8** | Frozen §10.8 says targetId uniqueness is install-validated across installed targets; built is a package-local check + runtime registry keep-first with a TargetRejected alert. The alert governs only the registry: a colliding package's lens still projects rows under the same `<targetId>.` prefix into the shared weaver-targets bucket, so the surviving target's consumer ingests foreign rows — the interleaving the frozen sentence exists to prevent. Build the install-time kernel scan for existing meta.weaverTarget targetIds, or stage the contract amendment to the as-built posture. | ★★ | S–M | 📋 |
| **contract-10-weaver-text-reconciliation** *(replace existing row's What)* | Contract #10 Weaver drift — 3 of the 07-02 five spots remain (not 2): the augur block still specs `pattern`+triggerLoom vs the engine's op/adapter/replyOp+directOp (a package's `pattern` field is silently dropped); §10.2 still says weaver-targets is "read only by Weaver" vs its P5 app-read reality; §10.8's anti-storm cross-ref still says "documented rare-double" though §10.3 superseded it. Fold in: reclaim-backoff + conditional TTL widening unrecorded; dormant plan-hash clause; inflight_/maxretries_ absent from §10.2's conventions; stale build-pending banners; oscillation auto-freeze contract-silent. Stage ONE uncommitted edit for Andrew. | ★★ | S | 📋 |
| **[Weaver] residual hardening pins** | Small residuals: health issues omit the Contract #5 §5.5 `since` field (likely platform-wide; verified for Weaver); two natsperm pins absent (weaver in the weaver-targets denied-put set; weaver in the KV_core-kv stream-admin subtest); five untested arms (seedDisabledTargets error abort, disable/enable partial-failure ordering + silent Pause/Resume bool discards, releaseCompletedLeg revision-conflict skip, freezeOscillatingPair failure leg); effectsCatalog() dead scaffolding (zero consumers — delete or re-comment RESERVED); trim the weaver user's own-ctrl publish grant; one history-narration comment in reconciler.go. | ★ | S | 📋 |
| **[Weaver/pkgmgr] admission-block authoring surface** | The §10.2 admission rider (dispatch pacing) is engine-complete but has no pkgmgr authoring path — only a raw-JSON-installed target can declare it, and unlike Candidates the gap is unacknowledged in the doc ledger. Add WeaverTargetSpec.Admission + install validation mirroring the engine. Consumer: any vertical target pacing a vendor adapter (LoftSpace bgcheck/payment external-call gaps are the reference shape). | ★ | S | 📋 |

## Unverified

- nats-server's overdue-`@at` fire-immediately behavior is taken from the code's own comment
  ([temporal.go:141-143](../../internal/weaver/temporal.go)), not re-checked against NATS 2.14 upstream;
  the W1 finding stands on the doc-vs-code mismatch regardless.
- The `orchestration-base` CreateTask script's kv.Read no-op branch (Weaver-side payload verified; the
  script is relied on via its colocated test).
- Whether the §5.5 `since` gap extends beyond Weaver.
- Runtime behavior of any path (no tests were run; read-only audit).
