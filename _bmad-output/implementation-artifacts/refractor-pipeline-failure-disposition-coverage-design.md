# Refractor pipeline — fan-out eval-error disposition + adjacency-watch edge-arm coverage

**Status: ✅ Andrew-ratified (2026-07-02).** The §6 transient-eval-no-enqueue asymmetry is **pinned as
intended** (the retry queue is a write-target replay mechanism; an eval error's redrive is NATS
redelivery re-evaluating in full) — no follow-up code change. One fire as decomposed, `handleAdjNode`
extraction included.

## For Andrew

**What it does (2 lines).** Locks the Refractor pipeline's *failure-disposition* behaviour behind tests: the
fan-out eval-error → {infra-pause · terminal-DLQ · transient-nak} mapping (`dispositionEvalErr`, today 0%
covered) and the adjacency-watch reprojection's fail-safe edge arms (`handleAdjUpdate`, today 13.5%). The
projection pipeline is the capability-KV producer, so *how it disposes of a failed reprojection* is
auth-correctness, not just coverage %.

**No architectural fork. No frozen-contract change.** Pure test work + one behaviour-preserving extraction of
`handleAdjUpdate`'s post-fetch body into a `handleAdjNode` helper so five of its arms become fast pure-unit
tests instead of NATS-backed ones. The disposition semantics are confirmed correct-as-written against
FR16/17/18/19a (§6) — no code-behaviour change is proposed, so there is nothing to *decide* beyond ratifying the
test strategy + the extraction.

**One thing to note:** the eval-stage transient arm returns `(Nak, nil)` and does **not** enqueue to the
retry queue (unlike the *write* stage). §6 argues this is the correct, intended asymmetry (an eval-stage
transient is re-driven by NATS redelivery; the retry queue is a write-target-backoff mechanism). If you read it
as a gap instead, that's the one place this design would grow a code change — flagged, not assumed.

---

## 1. Problem + intent

**Backlog row** (`backlog/lattice.md`, Component maintenance, ★★, XS–S): *"`dispositionEvalErr` (0% — link/aspect
fan-out eval-error → terminal-DLQ / infra-pause / transient-nak mapping) and `handleAdjUpdate` (13.5% — the
not-found / tombstone / bad-key / unmarshal arms). Happy-path fan-out is e2e-covered; the error/edge arms are
not."* Surveyor-filed (2026-07-01 Refractor survey line), refs `pipeline/pipeline.go:625,921`,
`pipeline/evaluate.go`.

The Refractor is the **materializer**: it consumes Core-KV CDC and projects lens read models, including the
**Capability KV** (Contract #6 — projection correctness *is* auth correctness, lattice-architecture.md).
Every reprojection that fails must be disposed of *deterministically* by failure tier (FR16–FR19a): an infra
outage must **pause-and-probe** (never DLQ, never drop), a structural misconfig must **pause without
accumulating DLQ noise**, permanently-bad data must go to the **DLQ** (not wedge the lane), and a transient
must be **re-driven**. If that mapping silently regresses under a future refactor, the failure mode is invisible
and security-relevant: a mis-disposed capability reprojection can wedge a lane (under-grant / staleness) or drop
a retraction (over-grant). These two functions are the *only* fan-out-path arms with no test pinning them, so a
refactor can move them freely today. This design pins them.

There is **no new feature and no new state** here — the intent is to convert an untested, security-adjacent
control-flow surface into a pinned one, and (secondarily) to make its cheapest-to-test arms actually cheap.

## 2. Grounding — the two mechanisms as they exist today

### 2.1 `dispositionEvalErr` (`pipeline.go:625`, 0% covered)

Called from the two actor-aware fan-out entry points when the **evaluate/traversal** stage errors *before* any
write: `evalLinkFanOut` (`:593`) and `evalAspectFanOut` (`:614`). It routes the error through the single
sanctioned classifier `failure.Classify` (`internal/refractor/failure/classify.go` — "no other package performs
local classification") and maps the tier to a `(substrate.Decision, error)` the ConsumerSupervisor applies:

```
cat := failure.Classify(err)
CatInfra | CatStructural → return (Nak, err)   // leave pending; the returned err makes the supervisor pause
CatTerminal              → publishTerminalDLQ(...); return (Ack, nil)
else (CatTransient)      → return (Nak, nil)    // NATS redelivery re-runs the fan-out
```

This mirrors the inline ack/nak discipline `writeResults` (`:649`) applies on the *write* side — same
classifier, same four tiers — but for an error raised in the *evaluate* stage. It is a pure function of
(classified error) → (decision, side-effect), which makes it directly unit-addressable.

### 2.2 `handleAdjUpdate` (`pipeline.go:921`, 13.5% covered)

The adjacency-watch reprojection (ADR-16): when `adj.<nodeId>` changes, the pipeline point-reads the Core-KV
node and re-evaluates it **read-only**, with **no stream sequence** (so it never advances a guarded watermark —
Contract #6 §6.2; guarded adapters are skipped entirely, `:998`). Its control flow is a sequence of
**fail-safe early-returns**, almost all uncovered:

| # | Arm | Line | Behaviour | Why it's fail-safe |
|---|-----|------|-----------|--------------------|
| 1 | bad-key (no `adj.` prefix) | 924 | return, no read/write | malformed key can't map to a node |
| 2 | not-found (`ErrKeyNotFound`) | 933 | skip | node not arrived yet; the stream consumer will project it |
| 3 | tombstone (empty value) | 947 | skip | delete is the stream consumer's job |
| 4 | parse-fail (`ParseVertexKey` false) | 952 | return | un-parseable vertex key |
| 5 | edge event (`nodeId != ""`) | 965 | skip | bootstrapper owns edges; pipelines handle nodes |
| 6 | unmarshal-fail | 958 | warn + return | corrupt body |
| 7 | evaluate-error | 978 | warn + return | re-eval failed; adj events aren't replayable |
| 8 | guarded-key skip | 998 | log + return | stream consumer owns the guarded watermark |
| 9 | write-error | 1012 | `reporter.RecordError` + **continue** | records to Health KV, does not pause (not replayable) |

The happy tail (`:1030` re-evaluated) is what the current 13.5% touches; every branch above is dark.

### 2.3 The harness that already exists (mirror it, don't invent)

Internal `package pipeline` tests construct `&Pipeline{…}` directly with only the fields a path needs, plus
small in-file fake adapters (`guardedTruncAdapter` in `rebuild_force_truncate_internal_test.go` is the model:
implements `adapter.Adapter` + `Guarded()`, records what was called). NATS-backed state uses
`newCollisionKVs(t)` (`output_collision_test.go:311`) → real `(coreKV, adjKV, healthKV)` on an embedded server
(`jsstore.Dir(t)` for parallel-safe teardown — [[project_ci_test_parallelism]]), with `writeCollisionVertex`
seeding Contract #1 bodies and `health.New(healthKV, ruleID)` giving a **real** reporter whose state is
assertable via `GetStatus`. These tests `t.Skip` under `testing.Short()`.

**Key constraint (shapes the whole strategy):** `coreKV` is `*substrate.KV` — a **concrete struct**
(`kvhandle.go:18`), not an interface. So `handleAdjUpdate`'s **Get-driven** arms (not-found, tombstone,
value) cannot be faked; they need a real seeded KV. Everything *after* the Get (parse / unmarshal / edge /
evaluate / guarded / write) does **not** touch `coreKV` and can be pure-unit — *if* it's reachable without
going through the Get. That reachability is the one design lever (§4.2).

## 3. Contract surface

**None.** `dispositionEvalErr` and `handleAdjUpdate` are internal Refractor control flow. The behaviour they
implement is the FR16–FR19a failure-routing already documented in `docs/components/refractor.md` (`failure/`
row) and the classifier's own doc comments; no `docs/contracts/*` section is touched. This is build-to, not
change. No uncommitted contract edit accompanies this design.

## 4. The shape (test strategy)

Two test surfaces, both internal (`package pipeline`), mirroring the established harness.

### 4.1 `dispositionEvalErr` — table-driven, direct method call

`dispositionEvalErr` is pure enough to call directly on a minimally-constructed pipeline. Drive each tier with a
synthetically-classified error and assert the `(Decision, error)` tuple + side-effect:

| Input error | Expect | Side-effect asserted |
|-------------|--------|----------------------|
| `failure.Infrastructure(errBoom)` | `(Nak, non-nil err)` | err identity preserved (supervisor pause depends on it) |
| `failure.Structural(errBoom)` | `(Nak, non-nil err)` | no DLQ publish |
| `failure.Terminal(errBoom)` | `(Ack, nil)` | **DLQ published** (see below) |
| a raw `errors.New` (→ `CatTransient`) | `(Nak, nil)` | no DLQ, no err |

- The three non-terminal rows need **no NATS** — `retryConn` nil, `reporter` nil — a `&Pipeline{ruleID:…}` is
  enough. These are fast, always-run (not short-skipped).
- The **terminal** row's `publishTerminalDLQ` no-ops when `retryConn == nil` (logs "no connection"). To *prove*
  the DLQ arm rather than just the `Ack`, one NATS-backed variant wires `retryConn` from the embedded server
  (extend `newCollisionKVs` to also return the `conn`, or add a sibling helper) and subscribes to the DLQ
  subject `failure.Publish` targets, asserting a `DLQMessage{ErrorClass:"TERMINAL", FailedStage:"traversal"}`
  lands. This is the one short-skipped case in this surface.
- Assert both call sites reach it: a thin test that `evalLinkFanOut` / `evalAspectFanOut` route a forced
  evaluate error into `dispositionEvalErr` (force it with `engineKind: Full` + nil `fullEngine`, which makes
  `evaluateForEntry` return the `"engine selected but engine/compiled rule unset"` error → `CatTransient` →
  `(Nak, nil)`). This pins the *wiring* (`:593`, `:614`) as well as the mapping.

### 4.2 `handleAdjUpdate` — extract `handleAdjNode`, then unit-test the arms

**Recommended: a behaviour-preserving extraction (mirrors the codebase's own evaluate/write helper split).**
Split `handleAdjUpdate` at the point after the Core-KV read:

```go
func (p *Pipeline) handleAdjUpdate(ctx, adjKey) {
    // arm 1 (prefix)  → arm 2 (Get / not-found)  → arm 3 (empty/tombstone)
    ... then: p.handleAdjNode(ctx, nodeKey, data)
}
func (p *Pipeline) handleAdjNode(ctx, nodeKey string, data []byte) {
    // arms 4–9: ParseVertexKey · unmarshal · edge · evaluate · guarded-skip · write+RecordError
}
```

This is a **pure move** — no branch changes order, no behaviour changes — and it makes arms 4–9 (six of the
nine) pure-unit-testable because they no longer require a seeded `coreKV` read to reach. It's the same shape the
package already uses (`evaluateForEntry` / `writeResults` are extracted seams), so it's mirror-not-invent.

Resulting test split:

- **Pure-unit** (fast, always-run): arm 1 via `handleAdjUpdate("notadj.x")` on a spy adapter (assert Upsert/
  Delete never called); arms 4–9 via `handleAdjNode(ctx, nodeKey, body)`:
  - 4 parse-fail — `nodeKey="garbage"`, any body → no write.
  - 5 edge — body with `nodeId:"n1"` → no write.
  - 6 unmarshal — body `[]byte("not json")` → no write, no panic.
  - 7 evaluate-error — `engineKind: Full`, nil engine, valid node body → no write, no panic (nil reporter path
    too, per the `output_collision` nil-reporter precedent).
  - 8 guarded-skip — a guarded fake adapter + a lens/body that yields ≥1 result → **no** `Upsert`/`Delete`
    (spy records zero writes), matching Contract #6 §6.2.
  - 9 write-error — an unguarded fake adapter whose `Upsert` returns `failure.Infrastructure(err)` + a **real**
    reporter (`health.New(healthKV,…)`); assert the loop **continues** (a second result still attempted) and
    `reporter.GetStatus` shows the error recorded. (Health KV means this one arm is NATS-backed, but only for
    the reporter, not for `coreKV`.)
- **NATS-backed** (short-skipped), only the two arms that *must* exercise the real Get: arm 2 (not-found — a
  fresh `adj.<absent>` key over an empty CORE bucket → skip) and arm 3 (tombstone — seed the node then write an
  empty body / tombstone → skip). Both assert no write via a spy adapter.

**Fallback if the extraction is judged not worth the diff:** every arm is still reachable through
`handleAdjUpdate` against a `newCollisionKVs` harness by seeding CORE with the exact body each arm needs — it's
just slower (all short-skipped) and forces contrived Core-KV states (e.g. a non-`vtx.` key holding data to hit
the parse-fail arm). The recommendation stands because the extraction converts six arms from contrived-NATS to
clean-unit and costs ~10 lines of pure movement.

## 5. Reconciliation with the existing mental model

- **"Didn't we already cover the fan-out path?"** The *happy* fan-out (link/aspect create → reproject → write)
  is e2e-covered (`composite_key_producer_test`, `anchor_tombstone_test`, the ephemeral-stack convergence
  suites). What's dark is the **error/edge disposition** — the arms that only fire on a *failed* or *skippable*
  reprojection. Those never execute on the happy path, so no amount of happy-path e2e reaches them.
- **"Does this duplicate the write-path failure tests?"** No. `writeResults`' terminal/transient/infra arms
  are exercised by existing write tests; `dispositionEvalErr` is the **evaluate-stage** twin (same classifier,
  different call site + a subtly different transient policy — §6). It has its own 0%-covered code.
- **New state?** None. No new vertices/aspects/links/lenses/ops, no new Health-KV keys, no schema. The only
  production delta in the recommended path is the pure `handleAdjNode` extraction.
- **Design-of-record vs Phase-1 simplification?** N/A — this is stable, shipped control flow; nothing here is a
  "reserved for later" seam.

## 6. Disposition semantics — confirmed correct-as-written (the design's real check)

Before pinning behaviour with tests, the behaviour was audited against FR16–FR19a. Findings:

- **Infra → pause, never DLQ; Structural → pause, no DLQ accumulation** (FR16/17/19a, NFR3): `dispositionEvalErr`
  returns the error for both so the supervisor pauses; neither publishes a DLQ. ✅ correct.
- **Terminal → DLQ + Ack** (FR18/19): publishes and acks so the lane isn't wedged by permanently-bad data. ✅.
- **Transient eval-stage → `(Nak, nil)`, NO retry-queue enqueue.** This is the one asymmetry vs `writeResults`
  (which *does* enqueue transient **write** failures to the exponential-backoff retry queue). **Assessment:
  intended.** The retry queue is a *write-target* backoff mechanism (a flaky Postgres/KV target) — it re-runs a
  captured `WriteFn`, not a re-evaluation. A transient *evaluate* error (e.g. a transient Core-KV read blip
  during traversal) has no captured write to replay; the correct redrive is a full re-evaluation, which NATS
  redelivery of the original CDC message provides. Enqueuing an eval error to the write retry queue would be a
  category error. So the tests **pin** `(Nak, nil)` as correct, not as a gap. → **Flagged for Andrew** (§For
  Andrew) in case he reads the asymmetry differently; if so, that's the sole place this grows a code change.
- **Adj-watch write-error → RecordError + continue, never pause** (`:1012`): correct — adj-watch events carry no
  stream sequence and aren't JetStream-replayable, so pausing would strand them; recording to Health KV gives
  operator visibility while the stream consumer remains the source of truth for the same actors. ✅.
- **Guarded-key skip on adj-watch** (`:998`, Contract #6 §6.2): a seq-0 write would be dropped-or-resurrecting,
  so skipping is correct; the stream-sequenced reprojection owns the watermark. ✅.

No latent bug surfaced. The value is durability: these confirmations become assertions.

## 7. Risks + alternatives

- **Risk: the `handleAdjNode` extraction changes behaviour.** Mitigation: it's a pure cut with no reordering;
  the NATS-backed arm-2/arm-3 tests still drive the *full* `handleAdjUpdate` (prefix→Get→empty→node), so the
  seam itself is covered end to end, not just the extracted half.
- **Risk: over-testing internals that a refactor will churn.** Mitigation: the assertions target the *contract*
  (which decision per failure tier; fail-safe skip per edge arm), not incidental structure — a legitimate
  refactor that preserves the disposition contract keeps these green; only a behaviour regression reddens them,
  which is exactly the point.
- **Alternative A — pure e2e (drive real failures through the ephemeral stack).** Rejected: forcing an
  infra/structural/terminal *evaluate* error deterministically through a live stack is far harder and flakier
  than classifying a synthetic error at the unit boundary, and it wouldn't reach the adj-watch edge arms at all
  (they need contrived Core-KV states). Unit-at-the-seam is both cheaper and more exhaustive here.
- **Alternative B — introduce a `coreKV` interface to fake the Get arms.** Rejected (dead-scaffolding /
  simplest-extension): a KV-read interface just for two test arms is heavier than the `handleAdjNode`
  extraction and touches a hot path used everywhere; the two Get arms are cheaply covered by the existing
  `newCollisionKVs` harness. Don't grow an abstraction the production code doesn't want.
- **Alternative C — no extraction, all-NATS-backed** is the documented fallback (§4.2), not the recommendation.

## 8. Adversarial pass (self-run — discharging the gate this design implies)

A lightweight adversarial reflection (the change is XS–S internal test work; a full `bmad-party-mode` panel is
disproportionate — noted per scale-review-depth):

- *"Does calling `dispositionEvalErr` directly test the real path, or a fiction?"* — It tests the real mapping;
  §4.1's wiring test additionally proves both `evalLinkFanOut`/`evalAspectFanOut` call sites route into it, so
  the direct-call tests aren't testing an orphan.
- *"Could the guarded-skip test pass vacuously (zero results → trivially no write)?"* — Guarded arm-8 must seed
  a lens/body that yields **≥1** result so the skip is meaningful; assert `len(results)>0` was produced (or use
  a lens known to project the seeded node), else the "no write" assertion is vacuous. Written into arm-8.
- *"Does arm-9 prove *continue* and not just *one write attempted*?"* — arm-9 seeds **two** results and a
  first-write error, asserting the second is still attempted + the error recorded; a bare single-result test
  would not distinguish continue from return.
- *"Is the transient-no-enqueue really intended, or am I pinning a bug?"* — §6 grounds it in the retry-queue's
  write-target semantics and flags it for Andrew rather than asserting it silently. Not pinned blind.

Findings folded into §4 (arm-8 non-vacuity, arm-9 two-result) and §6 (the flagged asymmetry).

## 9. Fire-by-fire decomposition (for the Lattice Steward)

Small enough for **one fire** (XS–S); presented as one fire with an internal build order so it stays a single
green commit (fewer-larger-fires — [[feedback_fewer_larger_fires]]):

**Fire 1 — pin the disposition + adj-watch arms.**
1. (recommended) Extract `handleAdjNode(ctx, nodeKey, data)` from `handleAdjUpdate` — pure move, no behaviour
   change; `go build ./...` + existing pipeline tests stay green.
2. Add `disposition_internal_test.go`: the four-tier `dispositionEvalErr` table (three pure-unit + one
   NATS-backed terminal-DLQ subscribe) + the two call-site wiring tests.
3. Add `adj_watch_internal_test.go`: arm 1 + arms 4–9 pure-unit against `handleAdjNode`/`handleAdjUpdate` with
   in-file spy + guarded fake adapters and a real reporter for arm-9; arms 2–3 NATS-backed via `newCollisionKVs`
   (short-skipped).
4. Gates: `go build ./...`, `make vet`, `golangci-lint run ./...`, `go test ./internal/refractor/pipeline/...`
   (and once with `-short` to confirm the fast arms run without the embedded server). Confirm coverage:
   `dispositionEvalErr` → 100%, `handleAdjUpdate`(+`handleAdjNode`) → ~100% of the branch arms.

If the extraction (step 1) is judged not worth its diff at build time, drop it and take the all-NATS-backed
fallback (§4.2) — the fire still ships the same assertions, just slower/short-skipped. Either way it's one
commit.

## 10. Definition of done

- `dispositionEvalErr` covered at 100%: the four failure tiers each assert `(Decision, error)` + DLQ side-effect
  where applicable; both fan-out call sites route into it.
- `handleAdjUpdate` (and, if extracted, `handleAdjNode`) branch arms all covered: bad-key, not-found,
  tombstone, parse-fail, edge, unmarshal, evaluate-error, guarded-skip, and write-error (continue +
  RecordError).
- No production behaviour change (the `handleAdjNode` extraction is a pure move); all existing gates green; the
  fast (`-short`) arms run without the embedded NATS server.
- The transient-eval-no-enqueue asymmetry (§6) is either ratified as-intended or, if Andrew reads it as a gap,
  spun into a follow-up code change — not silently pinned.
