# Story 7.6 — Substrate durable-consumer primitive

Status: review

**Tier:** Opus (substrate primitive serving multiple consumers). This extracts an ack-disciplined durable-consumer surface into `internal/substrate` — the lowest, most-imported package in Lattice. The danger is not behavioral complexity; it is **surface design**: too small and the outbox can't refactor onto it (the sufficiency proof fails); too large and you've baked Refractor's pause/lag/reset machinery into the shared base (the AC explicitly forbids this). Treat "the outbox tests stay green, byte-for-byte behavior preserved" as the crux, and "the surface is the *minimal* common need" as the equally-binding second constraint.

**Epic spec:** `_bmad-output/planning-artifacts/epics/phase-2-epics.md` → "### Story 7.6: Substrate durable-consumer primitive" (line ~113). Read it for the user-story framing and the exact AC. (Note: there is currently an uncommitted edit in that epics file marking Story 7.5 won't-do — leave it alone; do not stage, revert, or modify it.)

**Binding grounding (read these — the primitive must fit the package's existing style, not duplicate it):**
- **The pattern to generalize:** `internal/processor/outbox/consumer.go` — the `Consumer` type's full lifecycle: `CreateOrUpdateConsumer` (durable bind on `KV_<bucket>` with `FilterSubject`, `DeliverAllPolicy`, `AckExplicitPolicy`) → `loop` (reopen iterator on transient error) → `drain` (ctx-cancellable `mc.Next()` with a watcher that calls `mc.Stop()` on `ctx.Done()`) → `processMsg` (ack empty/tombstone, **publish-then-tombstone-then-ack**, **nak on publish failure**, **term on poison/unparseable**). That ack/nak/term decision lives in *outbox* business logic; the bind+drain+shutdown scaffolding is what you extract.
- **The existing substrate consumer (do NOT conflate with the new one):** `internal/substrate/subscribe.go` — `(*Conn).SubscribeKVChanges(...) (<-chan KVEvent, error)`. This is a **channel-based, auto-ack** durable consumer: it acks *after the caller reads the event off the channel* (`runKVSubscription`: `out <- evt` then `msg.Ack()`). It is **not** the outbox's discipline — the outbox needs **caller-controlled ack/nak/term keyed on whether the downstream publish was confirmed**, which `SubscribeKVChanges` cannot express (it acks unconditionally once the event is consumed). The new `DurableConsumer` is therefore a **distinct, sibling primitive**, not a refactor of `SubscribeKVChanges`. Reuse its proven scaffolding patterns (the `loop`/`drain`/`mc.Stop()`-on-ctx watcher, `normalizePrefix`, `streamName := "KV_"+bucket`), do **not** reuse its ack semantics.
- **The exclusion list (read it so you know exactly what NOT to pull in):** `internal/refractor/consumer/manager.go` — `Manager.Add/Reset/Remove/Stop`, `DeliverLastPerSubjectPolicy` (ADR-15), per-rule consumer naming; `internal/refractor/health/lag_poller.go` (`LagPoller`); `internal/refractor/control/service.go` (pause/resume). **None of this enters substrate.** Refractor keeps it; Refractor's eventual migration onto this base is a *separate* deferred carry (do not attempt it here).
- `internal/substrate/conn.go` — `Conn` wraps `nats.Conn` + `jetstream.JetStream`; `JetStream()` is the escape hatch the new primitive uses (`c.js.CreateOrUpdateConsumer`). `internal/substrate/doc.go` — package design principles (Lattice-specific not generic; hide layered NATS APIs behind `Conn`; programmer errors panic, operational failures return typed sentinels).
- `docs/components/substrate.md` — §"Durable KV change consumer", §"Principles (binding)" ("Substrate exposes only operations that are architecturally common across components … JetStream-consumer-management helpers that are component-specific belong in the component"), §"What this component owns". **This story adds a new exported-surface subsection here** (docs live next to code — CLAUDE.md).
- House rules — `/Users/andrewsolgan/Documents/GitHub/Lattice/CLAUDE.md`: no history/changelog comments in code (most-violated rule); frozen contracts (`docs/contracts/*`) + planning artifacts (`_bmad-output/planning-artifacts/*`) not edited; new docs → `/docs`.

**Depends on:** Story 1.5.1 (substrate write-path / `Conn`); Story 1.5.10/1.5.11 (the outbox you refactor — `internal/processor/outbox/`). **Forward consumers (do NOT build):** Loom (Epic 8.1, `internal/loom`) and Weaver (Epic 9.1, `internal/weaver`) — neither package exists yet; their dependence on this primitive is a *design guarantee*, not code in this story.

**Workflow:** the DS is a sub-agent. Repo root, no worktree. Do **NOT** commit/push or branch. Do **NOT** edit planning artifacts or **FROZEN** contracts (`docs/contracts/*`). Do **NOT** touch `README.md` (being edited out-of-band). You MAY edit `/docs/components/substrate.md`. A genuine contract gap → file `internal/substrate/CONTRACT-AMENDMENT-REQUEST.md` and continue with a different deliverable; do not edit the frozen shape in place.

---

## 0. ADJUDICATION — Winston build target. DS builds to THIS.

### 0.0 What this story delivers (scope boundary)

Extract the outbox's ack-disciplined durable-consumer lifecycle into a **new `internal/substrate` primitive** (`DurableConsumer`), then **refactor the outbox onto it** so the outbox's existing tests stay green — that green-test pass IS the proof the surface is sufficient. **In scope:**

1. **One new substrate primitive** — a `DurableConsumer` (new file, e.g. `internal/substrate/consumer.go`) that: binds a durable JetStream consumer to a stream + filter subject (idempotent `CreateOrUpdateConsumer`), drives a ctx-cancellable message loop with reconnect-on-transient-error, hands each message **body** to a caller-supplied handler, and lets the **handler decide ack / nak / term**. Clean shutdown on `ctx.Done()`. Resume-from-last-ack across restarts (inherent to a durable consumer — do not delete it on shutdown).
2. **Outbox refactored onto it** — `internal/processor/outbox/consumer.go`'s `Consumer.Run/loop/drain` scaffolding is replaced by a `DurableConsumer`; the outbox's `processMsg` decision logic (ack-on-confirmed-publish, nak-on-publish-failure, term-on-unparseable, ack-and-skip empty/tombstone/isDeleted) becomes the **handler** passed to it. **Behavior-preserving: the existing `internal/processor/outbox/*_test.go` must pass unchanged** (or with only mechanical call-site adjustments — see A4).
3. **Doc update** — a new "Durable consumer (ack-disciplined)" subsection in `docs/components/substrate.md` documenting the surface and how it differs from `SubscribeKVChanges`.

**OUT of scope (do NOT build — later/other work):**
- **Refractor's machinery:** pause / resume / lag-poll / `Reset()` / `DeliverLastPerSubjectPolicy` / per-rule consumer management. These stay in `internal/refractor/*`. **Do not** add them to the primitive "for completeness." (AC, binding.)
- **Refractor's migration onto this base** — the existing deferred substrate-inner-package carry. Not this story. Do not touch `internal/refractor/consumer/manager.go`.
- **`internal/loom` / `internal/weaver`** — they don't exist; write **no** code there. The "no new `nats.io`/`jetstream` handles in loom/weaver" AC is a **forward design guarantee** the primitive's surface must *enable* (the primitive must be expressive enough that 8.1/9.1 never need to reach for raw `jetstream`), proven only by the surface being right — there is nothing to assert in code here. (See A5.)
- **Refactoring `SubscribeKVChanges`** onto the new primitive. It has different (auto-ack) semantics and a live caller (Refractor CDC). Leave it as-is. (If you see a clean shared-helper opportunity between the two — e.g. the `loop`/`drain`/watcher scaffolding — you MAY extract a small private helper both call, but only if it's behavior-neutral for `SubscribeKVChanges` and its tests stay green. Default: leave `SubscribeKVChanges` untouched and accept minor scaffolding duplication. Flag any shared-helper extraction in the closing summary.)
- **No new `nats.io` version bump, no new dependency.** The primitive uses the already-imported `nats.go/jetstream`.

### 0.1 A1 — The proposed `DurableConsumer` surface (the design crux; build to this shape)

The surface must be **exactly** what the outbox needs now and what Loom/Weaver will need (plain ack-disciplined durable consume + resume), and **nothing more**. The defining property — the one `SubscribeKVChanges` lacks — is **caller-controlled ack/nak/term keyed on downstream success**. Build to this shape (adjust names to fit the package, but preserve the semantics):

```go
// internal/substrate/consumer.go

// Decision is the caller's verdict on a delivered message, returned from a
// HandlerFunc. It determines the JetStream acknowledgement applied after the
// handler returns: confirmed-processed (Ack), retry-later (Nak), or
// permanently-undeliverable (Term).
type Decision int

const (
    Ack  Decision = iota // message processed; advance the durable ack floor
    Nak                  // transient failure; redeliver (at-least-once preserved)
    Term                 // poison message; never redeliver (event-loss-accepting — log loudly)
)

// Message is the minimal view of a delivered JetStream message handed to a
// HandlerFunc. Routing/identity is read from Body (read-from-body discipline),
// not from Subject; Subject is provided only for key recovery (e.g. stripping
// a "$KV.<bucket>." prefix) and diagnostics.
type Message struct {
    Subject  string
    Body     []byte
    Sequence uint64 // backing-stream sequence (diagnostics / position reasoning)
}

// HandlerFunc processes one message and returns the ack Decision. It MUST be
// idempotent: at-least-once delivery means the same message can arrive again
// after a Nak or a crash-before-ack.
type HandlerFunc func(ctx context.Context, msg Message) Decision

// DurableConsumerConfig binds a durable consumer to a stream + filter subject.
type DurableConsumerConfig struct {
    Stream        string // e.g. "KV_core-kv"
    FilterSubject string // e.g. "$KV.core-kv.vtx.op.*.events"
    Durable       string // durable name — same name resumes from last ack
    MaxDeliver    int    // redelivery bound on Nak; <=0 → unlimited (JetStream default)
    Logger        *slog.Logger
}

// RunDurableConsumer creates (idempotently) the durable consumer described by
// cfg and drives it, invoking handler for each delivered message body, until
// ctx is cancelled. It blocks until ctx is done. Re-running with the same
// cfg.Durable resumes from the last-acked sequence (the consumer is NOT
// deleted on shutdown — its persisted position is the point of "durable").
func (c *Conn) RunDurableConsumer(ctx context.Context, cfg DurableConsumerConfig, handler HandlerFunc) error
```

**Binding surface constraints:**
- **Method on `*Conn`** (mirrors `SubscribeKVChanges`), so it uses the cached `c.js` and obeys the package's "hide layered NATS APIs behind `Conn`" principle. Do NOT expose `jetstream.Msg`/`jetstream.Consumer` to the caller — that would leak the very `jetstream` handles the Loom/Weaver AC forbids.
- **`DeliverPolicy` is `DeliverAllPolicy`** (the outbox's policy — process from the start of the durable's history). It is **fixed inside the primitive**, NOT a config knob. Do **NOT** add `DeliverLastPerSubjectPolicy` or any other policy option — that is the Refractor machinery the AC excludes. (If a forward consumer genuinely needs `DeliverNew`, that's a future, justified surface addition — not speculative now. Note it as an Open Question if you believe Loom/Weaver will need it, but do not add it.)
- **`AckExplicitPolicy`** always — the whole point is caller-controlled ack.
- **The three-way `Decision` (Ack/Nak/Term)** is non-negotiable: the outbox uses all three (ack on confirmed publish + empty/tombstone, nak on publish failure, term on unparseable poison). A two-way ack/nak surface would fail the outbox refactor.
- **`Subject` is exposed but the contract is read-from-body.** The handler reads routing/identity from `Body`; `Subject` is provided **only** for mechanical key recovery (the outbox strips `"$KV.<bucket>."` to recover the Core KV key for its tombstone delete) and diagnostics. Document this discipline in the `Message` doc comment and in `substrate.md`. Do NOT design the surface so a consumer must parse the subject for identity.
- **Empty-body messages are delivered to the handler, not swallowed by the primitive.** The outbox itself decides what an empty body means (KV tombstone/PURGE → ack+skip). The primitive is policy-free about body content — it hands the bytes over and applies the returned `Decision`. (This keeps the primitive a pure transport; tombstone/isDeleted interpretation is outbox business logic.)
- **What it does NOT include (state this in the doc + closing summary):** no pause/resume, no lag polling, no `Reset()`/redelivery-policy switching, no `DeliverLastPerSubject`, no per-subject/per-rule consumer management, no channel-based delivery, no consumer deletion on shutdown. Minimal common need only.

### 0.2 A2 — The outbox refactor: behavior-preserving, tests are the sufficiency proof (AC, the crux)

The outbox refactor is what *proves* the surface. Build to this:
- **`internal/processor/outbox/consumer.go`:** delete `Consumer.Run`, `Consumer.loop`, `Consumer.drain` (the bind + reconnect + ctx-cancellable iterator scaffolding) — that responsibility moves into `RunDurableConsumer`. `Consumer.New` keeps building the `Consumer` (it still owns `streamName`, `filterSubj`, `bucket`, `subjectPrefx`, `publisher`, `logger`). Add a `Consumer.Run(ctx)` that calls `c.conn.RunDurableConsumer(ctx, cfg, c.handle)` where `cfg` carries the outbox's existing stream/filter/durable (`ConsumerName = "processor-outbox"`) and `c.handle` is `processMsg` rewritten to the `HandlerFunc` signature.
- **`processMsg` → `handle(ctx, substrate.Message) substrate.Decision`:** the exact same decision tree, returning `Decision` instead of calling `msg.Ack()/Nak()/Term()` directly:
  - empty body → `return substrate.Ack` (KV tombstone/PURGE/TTL skip).
  - unparseable aspect (`processor.ParseOutboxAspect` error) → log loudly + `return substrate.Term` (poison; event-loss risk, same as today's `msg.Term()`).
  - `aspect.IsDeleted || len(events)==0` → `return substrate.Ack`.
  - publish via `c.publisher.Publish` fails → `return substrate.Nak` (at-least-once preserved).
  - publish succeeds → tombstone the aspect via `c.conn.KVDelete` (tolerate failure, same as today), then `return substrate.Ack`.
  - Key recovery: `key := strings.TrimPrefix(msg.Subject, c.subjectPrefx)` — same as today, now off `Message.Subject`.
- **Net effect:** the outbox's *ack-on-confirmed-publish* discipline (Contract #4 §4.4 — events at-least-once, consumer-idempotent) is **byte-for-byte preserved**; only *who calls `Ack/Nak/Term`* moves (now the primitive applies the handler's `Decision`). The mapping `Ack→msg.Ack()`, `Nak→msg.Nak()`, `Term→msg.Term()` inside `RunDurableConsumer` must be exact, including: a failed `msg.Ack()` after success is logged not retried (today's behavior), and the primitive logs `Nak`/`Term`/ack failures at the same severity the outbox does today (Warn for nak-redelivery, Error for term/ack-failure).
- **Tests are the proof — they must stay green:** `internal/processor/outbox/consumer_test.go` exercises the live lifecycle against an embedded NATS (`startEmbeddedNATS`, `setup` provisioning `core-kv` + `core-events`). After the refactor these tests **pass unchanged** except for unavoidable mechanical edits (e.g. if a test reached into `Consumer.drain` directly — check; it likely only calls `Consumer.Run`). **If any outbox test needs a semantic change to pass, the surface is wrong — fix the surface, not the test.** State explicitly in the closing summary which outbox test files changed and that the change was mechanical (call-site only), with a diff-line count.

### 0.3 A3 — New substrate test coverage for the primitive (the primitive needs its own tests, not just the outbox's)

The outbox tests prove *sufficiency*; the primitive needs *direct* tests proving its three behaviors in isolation (mirror the style of `internal/substrate/subscribe_test.go` — embedded NATS via the same helper pattern):
- **Ack advances the floor / resume-from-last-ack:** publish N messages, handler returns `Ack` for the first K, cancel ctx; re-run `RunDurableConsumer` with the **same `Durable`** → it resumes at K+1, not 0 (the durable-position guarantee — the same guarantee `subscribe_test.go` asserts for `SubscribeKVChanges`).
- **Nak redelivers:** handler returns `Nak` once then `Ack` → the message is delivered at least twice and eventually acked (at-least-once).
- **Term does not redeliver:** handler returns `Term` → message is not redelivered (poison drop), and the next message still flows.
- **Clean shutdown:** `ctx` cancel unblocks `RunDurableConsumer` promptly (the `mc.Stop()`-on-`ctx.Done()` watcher) and it returns without hanging.
- **Read-from-body:** a test asserting the handler receives the body bytes and that `Message.Subject` is the raw stream subject (so the outbox's prefix-strip works).

### 0.4 A4 — Reuse the proven scaffolding; don't reinvent (anti-disaster)

The `loop`/`drain`/watcher pattern already exists **twice** in the codebase (outbox `consumer.go` and substrate `subscribe.go`'s `runKVSubscription`) and is correct. **Copy its structure into `RunDurableConsumer`** — do NOT invent a new shutdown dance:
- `CreateOrUpdateConsumer` with `Durable`, `FilterSubject`, `DeliverAllPolicy`, `AckExplicitPolicy`, `MaxDeliver` (when >0).
- Outer `loop`: reopen `cons.Messages()` on transient iterator error, with a `reconnect`-delay select on `ctx.Done()` (outbox uses `5*time.Second` — reuse that constant or a package-local equivalent; do NOT make it a config knob unless you can justify it).
- Inner `drain`: a goroutine that calls `mc.Stop()` on `ctx.Done()` (so the blocking `mc.Next()` unblocks for clean shutdown), `defer mc.Stop()`, loop `mc.Next()` → build `Message` → call handler → apply `Decision`.
- **Do NOT delete the consumer on shutdown** (the explicit `runKVSubscription` comment: deleting wipes the durable position and forces full replay). The primitive's `RunDurableConsumer` leaves the durable parked on exit.
- **No history/changelog comments** (CLAUDE.md, most-violated): the new `consumer.go` describes what it does *now*; never `// extracted from outbox`, `// was processMsg`, `// moved from …`, `// generalized from …`. git blame is the record. Likewise the outbox file: no `// now uses substrate primitive` narration.

### 0.5 A5 — The Loom/Weaver "no raw nats handles" guarantee is a *surface* property, not code here (AC, forward-looking)

The AC's third clause — *"Loom (8.1) and Weaver (9.1) consume this primitive; no new `nats.io`/`jetstream` handles appear in `internal/loom` or `internal/weaver`"* — is a **forward design guarantee**. Those packages **do not exist** (verified: `internal/loom`, `internal/weaver` are absent). You write **no** code for them and assert **nothing** about them in tests. The guarantee is satisfied *structurally* by the surface being expressive enough that a deterministic flow engine (Loom 8.1: "reconciles a per-domain durable consumer from declared bindings … on restart the durable consumer resumes; the run completes exactly once") can be built on `RunDurableConsumer` alone — durable bind + filter + ack-disciplined consume + resume is exactly Loom 8.1's stated need. **Your job: make sure the surface covers that need** (it does — bind to a domain's stream/filter, ack on cursor-advance-confirmed, resume on restart). If, while designing, you find a *concrete, named* gap that would force Loom/Weaver back to raw `jetstream`, surface it as an Open Question with the specific missing operation — do **not** speculatively add it.

### 0.6 A6 — Naming, placement, and package-principle compliance (Contract grounding; substrate doc.go)
- **Placement:** new file `internal/substrate/consumer.go` (+ `consumer_test.go`). Method on `*Conn`. Sits alongside `subscribe.go` (the sibling consumer) and `batch.go`.
- **Naming:** follow the package's existing verb/noun style (`SubscribeKVChanges`, `PublishBatch`, `AtomicBatch`). `RunDurableConsumer` + `DurableConsumerConfig` + `HandlerFunc` + `Decision`/`Message` is consistent. Avoid a generic name like `Consume` that collides conceptually with `SubscribeKVChanges`.
- **Principles (`substrate.md` §Principles, binding):** "Substrate exposes only operations that are architecturally common across components." A plain ack-disciplined durable consumer used by outbox + Loom + Weaver **is** architecturally common — this addition is justified by ≥2 real consumers (outbox now, Loom/Weaver next), which is the bar. Refractor's pause/lag/reset is component-specific and stays out — that's the same principle drawing the line. **State this justification in the closing summary** (the principle requires a design decision for substrate additions; this is it).
- **Dependency direction:** substrate imports only `nats.go` (+ stdlib). The primitive must **not** import `internal/processor` or any component. (The outbox imports substrate, never the reverse.)

### 0.7 Gates (all must pass before handing back)

`go build ./...` · `make vet` · `golangci-lint run ./...` · **`make verify-kernel`** (no kernel change expected — confirm it still passes) · `make test-bypass` (Gate 2, all BLOCKED) · `make test-capability-adversarial` (Gate 3, all DEFENDED) · **`go test ./internal/substrate/... ./internal/processor/outbox/... -count=1`** (THE central gate here — the new primitive's tests AND the unchanged outbox tests must both be green; the green outbox suite is the sufficiency proof) · `go test ./... -count=1` to catch any unexpected fallout. The outbox + substrate tests use embedded NATS (no docker stack needed). State the exact outbox test files touched and confirm their changes were call-site-mechanical only.

---

## 1. Story (user-facing)

As a **platform developer**,
I want **a minimal ack-disciplined durable-consumer primitive in `internal/substrate`**,
so that **the outbox, Loom, and Weaver share one consumer rather than each wiring raw `nats.io`.**

## 2. Acceptance Criteria (faithful to the epic AC, line ~119)

1. **Given** the existing minimal consumer pattern in `internal/processor/outbox` (durable bind, pull, ack-on-confirmed, resume-from-last-ack), **When** it is generalized into `internal/substrate` as a `DurableConsumer` (bind to a stream + filter subject, consume-with-ack, resume), **Then** the primitive exists with the surface in §0.1 — caller-controlled `Ack`/`Nak`/`Term`, read-from-body, durable resume, clean ctx shutdown.
2. **And** the **outbox is refactored onto the substrate primitive** — `internal/processor/outbox/consumer.go`'s bind/loop/drain scaffolding is replaced by `RunDurableConsumer` and its `processMsg` decision tree becomes the handler — **behavior-preserving: the existing outbox tests stay green** (the sufficiency proof; any test change is call-site-mechanical only).
3. **And** the surface is the **minimal** common need — it does **NOT** include Refractor's pause / lag-poll / reset / `DeliverLastPerSubject` machinery (that stays in Refractor; its migration onto this base remains a separate deferred carry). `DeliverPolicy` is fixed at `DeliverAllPolicy`; no policy/pause/lag/reset knobs.
4. **And** the primitive **enables** Loom (8.1) and Weaver (9.1) to consume it with **no new `nats.io`/`jetstream` handles** in `internal/loom`/`internal/weaver` — a forward design guarantee (those packages don't exist yet; no code is written for them here), satisfied by the surface being expressive enough for a durable-bind-and-resume flow engine.

## 3. Tasks / Subtasks

- [x] **T1 — Build the `DurableConsumer` primitive** (AC #1, #3; A1, A4, A6)
  - [x] New `internal/substrate/consumer.go`: `Decision` (Ack/Nak/Term), `Message` (Subject/Body/Sequence), `HandlerFunc`, `DurableConsumerConfig`, `(*Conn).RunDurableConsumer`.
  - [x] `CreateOrUpdateConsumer` with `Durable`/`FilterSubject`/`DeliverAllPolicy`/`AckExplicitPolicy`/`MaxDeliver`; copy the `loop`/`drain`/`mc.Stop()`-on-ctx-watcher scaffolding from `subscribe.go`/outbox `consumer.go`.
  - [x] Map `Decision` → `msg.Ack()`/`Nak()`/`Term()` exactly; do NOT delete the consumer on shutdown; no policy/pause/lag/reset knobs; no `jetstream` types in the exported surface.
- [x] **T2 — Refactor the outbox onto it** (AC #2; A2, A4)
  - [x] `internal/processor/outbox/consumer.go`: remove `Run`/`loop`/`drain`; rewrite `Consumer.Run` to call `c.conn.RunDurableConsumer(ctx, cfg, c.handle)`; convert `processMsg` → `handle(ctx, substrate.Message) substrate.Decision` preserving the exact ack/nak/term/skip decision tree (incl. key recovery via `Message.Subject`, tombstone-then-ack on success).
  - [x] No history/changelog comments in either file.
- [x] **T3 — Primitive tests** (AC #1; A3) — new `internal/substrate/consumer_test.go`: ack-advances-floor + resume-from-last-ack; nak-redelivers; term-no-redeliver; clean ctx shutdown; read-from-body. Embedded NATS (mirror `subscribe_test.go`).
- [x] **T4 — Sufficiency proof: outbox tests green** (AC #2; A2) — run `go test ./internal/processor/outbox/... -count=1`; confirm green; record exactly which outbox test files changed and that changes were call-site-mechanical only (diff-line count).
- [x] **T5 — Doc** (A1, A6) — add a "Durable consumer (ack-disciplined)" subsection to `docs/components/substrate.md`: the surface, the `Ack`/`Nak`/`Term` discipline, read-from-body, resume-from-last-ack, and the explicit "does NOT include pause/lag/reset/`DeliverLastPerSubject` — those are Refractor-component-specific" note. Distinguish it from `SubscribeKVChanges` (auto-ack channel vs. caller-controlled ack).
- [x] **T6 — Gates** (§0.7) — full gate list green; confirm `verify-kernel`/bypass/adversarial unaffected.

## 4. Dev Notes

### Where things live (read these first — DS does the deep reads)
- **The pattern to extract:** `internal/processor/outbox/consumer.go` (the whole file — 192 lines). `Run` (durable bind), `loop` (reconnect), `drain` (ctx-cancellable iterator + `mc.Stop()` watcher), `processMsg` (the ack/nak/term decision tree). This is your blueprint for both the primitive's scaffolding AND the outbox handler.
- **The sibling consumer (reuse scaffolding, NOT ack semantics):** `internal/substrate/subscribe.go` — `runKVSubscription` (lines ~176–228) is the cleanest existing `loop`/`drain`/watcher + "don't delete the durable on shutdown" implementation; the durable-position comment (~lines 160–175) is the rationale you must preserve. `normalizePrefix` (~line 142) if you want filter-subject validation (optional — the outbox passes a fully-formed filter).
- **Connection escape hatch:** `internal/substrate/conn.go` — `(*Conn).JetStream()` returns `c.js` for `CreateOrUpdateConsumer`. The primitive is a `*Conn` method so it uses `c.js` directly (like `AtomicBatch`/`PublishBatch` in `batch.go`).
- **The exclusion list (read to confirm what stays out):** `internal/refractor/consumer/manager.go` (`Add`/`Reset`/`Remove`/`Stop`, `DeliverLastPerSubjectPolicy`), `internal/refractor/health/lag_poller.go`, `internal/refractor/control/service.go`. **Untouched by this story.**
- **Test harness to copy:** `internal/processor/outbox/consumer_test.go` `startEmbeddedNATS`/`setup` (lines ~22–60) and `internal/substrate/subscribe_test.go` — embedded JetStream NATS, KV provisioning, durable-resume assertion pattern.
- **Doc to extend:** `docs/components/substrate.md` §"Durable KV change consumer" (~line 150) + §"Principles (binding)" (~line 186) + §"Exported surface" (~line 43).

### Key behavioral invariants to preserve (Contract #4 §4.4 — at-least-once + idempotent)
- **Ack only after confirmed downstream effect.** The outbox acks only after `publisher.Publish` succeeds (and tombstone-attempt). A crash between commit and publish is recovered by redelivery from the durable offset. The primitive must apply the handler's `Ack` **after** the handler returns `Ack`, never before — i.e. the handler runs to completion, *then* the `Decision` is applied. (Do not ack-then-handle.)
- **Nak preserves at-least-once.** Publish failure → `Nak` → JetStream redelivers. `MaxDeliver` bounds the retry; the outbox today does not set `MaxDeliver` (unlimited) — preserve that (config `<=0` → omit `MaxDeliver`, JetStream default = unlimited). Confirm the live outbox consumer config (`Run` in `consumer.go`) sets no `MaxDeliver` and match it.
- **Term drops poison.** Unparseable aspect → `Term` → no redelivery, log loudly (event-loss risk). Same severity/loudness as today.
- **Durable resume.** Same `Durable` name re-binds to the existing consumer (`CreateOrUpdateConsumer` is idempotent) and resumes at the last-acked sequence. **Never delete the consumer on shutdown.**

### Read-from-body discipline (project convention)
The handler reads routing/identity from `Message.Body`, not `Message.Subject`. `Subject` is exposed **only** for mechanical key recovery (outbox strips `"$KV.<bucket>."`) and diagnostics. Document this in the `Message` doc comment and in `substrate.md`. This is consistent with the Processor's existing discipline (the actor/op identity lives in the persisted payload, not the subject).

### Project Structure Notes
- New code lives in `internal/substrate` (consumer.go, consumer_test.go) — the package owns this primitive per the §Principles bar (architecturally common across ≥2 consumers). Outbox refactor stays in `internal/processor/outbox`. No new packages, no `internal/loom`/`internal/weaver`.
- Doc → `docs/components/substrate.md` (next to code, per CLAUDE.md — not `_bmad-output/`).
- No frozen-contract or planning-artifact edits. No kernel change → `verify-kernel` count unchanged.

### References
- [Source: `internal/processor/outbox/consumer.go`] — the lifecycle being generalized; the ack/nak/term decision tree.
- [Source: `internal/processor/outbox/publisher.go` + `consumer_test.go`] — `Publish` semantics + the embedded-NATS test harness (the sufficiency proof).
- [Source: `internal/substrate/subscribe.go`#runKVSubscription] — the `loop`/`drain`/watcher scaffolding + durable-position-preservation rationale to reuse.
- [Source: `internal/substrate/conn.go`#JetStream] — the `*Conn` escape hatch the primitive uses.
- [Source: `docs/components/substrate.md`#Principles] — the "architecturally common only" bar that justifies this addition and excludes Refractor's machinery.
- [Source: `internal/refractor/consumer/manager.go`] — the `Reset`/`DeliverLastPerSubjectPolicy` machinery the AC explicitly keeps OUT of substrate.
- [Source: `_bmad-output/planning-artifacts/epics/phase-2-epics.md`#Story-7.6 + #Story-8.1] — the AC and the forward Loom consumer whose need the surface must cover.

## Winston's Adjudication (all four RESOLVED — DS builds to these)

All four open questions accepted as recommended; no contentious calls.

1. **`MaxDeliver` → UNLIMITED, matching the live outbox. ACCEPTED.** The primitive omits `MaxDeliver` when `cfg.MaxDeliver <= 0` (JetStream default = unlimited redelivery on Nak); callers opt into a bound explicitly. **DS must verify the live outbox config** sets no `MaxDeliver` and that the refactor preserves that exactly — behavior-preservation is the gate. (Note the divergence from `SubscribeKVChanges`'s default of 10 in the closing summary so it's a conscious choice, not an oversight.)
2. **Scaffolding duplication → ACCEPT, with a follow-up. ACCEPTED.** Copy the proven `loop`/`drain`/`mc.Stop()`-watcher pattern into `consumer.go`; leave `SubscribeKVChanges` untouched (live Refractor caller + auto-ack semantics → regression risk not worth it this story). Flag the duplication explicitly in the closing summary; Winston will spin off a unify-the-scaffolding follow-up if warranted. Do NOT extract a shared helper that touches `SubscribeKVChanges` unless it is provably behavior-neutral and its tests stay green.
3. **`DeliverPolicy` → FIXED at `DeliverAllPolicy`, no knob. ACCEPTED.** Minimal surface; `DeliverAll` + durable covers Loom 8.1's stated need. Add `DeliverNew` (or any policy knob) ONLY when a concrete, named forward consumer needs it — not speculatively. If you hit such a need while designing, surface it as a new Open Question with the specific operation; do not add it.
4. **CONTRACT-AMENDMENT-REQUEST → none anticipated. ACCEPTED.** Pure internal-package refactor; `docs/contracts/*` is silent on substrate's internal consumer helpers. File `internal/substrate/CONTRACT-AMENDMENT-REQUEST.md` ONLY if a real frozen-contract gap surfaces, and continue with a different deliverable rather than editing a frozen contract.

---

## Dev Agent Record

### File List

- **Added** `internal/substrate/consumer.go` — the `DurableConsumer` primitive (`Decision`, `Message`, `HandlerFunc`, `DurableConsumerConfig`, `(*Conn).RunDurableConsumer` + private `runDurableLoop`/`drainDurable`/`newMessage`/`applyDecision`).
- **Added** `internal/substrate/consumer_test.go` — five direct primitive tests (embedded NATS).
- **Modified** `internal/processor/outbox/consumer.go` — removed `Run`/`loop`/`drain`/`processMsg` scaffolding; `Run` now delegates to `RunDurableConsumer`; `processMsg` → `handle(ctx, substrate.Message) substrate.Decision`. (20 insertions / 94 deletions.)
- **Modified** `docs/components/substrate.md` — added "Durable consumer (ack-disciplined)" subsection.
- **Modified** this story file (Tasks, Dev Agent Record, Status).

No `internal/processor/outbox/*_test.go` files changed. No frozen contract / planning-artifact edits. No `internal/refractor/*`, `internal/loom`, `internal/weaver`, or `README.md` touched. No new dependency.

### Completion Notes (closing summary)

**Final `DurableConsumer` surface (as built):**
```go
type Decision int          // Ack=0, Nak, Term
type Message struct { Subject string; Body []byte; Sequence uint64 }
type HandlerFunc func(ctx context.Context, msg Message) Decision
type DurableConsumerConfig struct { Stream, FilterSubject, Durable string; MaxDeliver int; Logger *slog.Logger }
func (c *Conn) RunDurableConsumer(ctx context.Context, cfg DurableConsumerConfig, handler HandlerFunc) error
```
Method on `*Conn`; no `jetstream.Msg`/`jetstream.Consumer` in the exported surface. `DeliverAllPolicy` + `AckExplicitPolicy` baked in (no knob). `MaxDeliver <= 0` omits the bound. Decision applied **after** the handler returns (never ack-then-handle). Consumer is never deleted on shutdown; clean ctx-cancel via the `mc.Stop()`-on-`ctx.Done()` watcher. Empty bodies are delivered to the handler.

**Outbox refactor is behavior-preserving (sufficiency proof):** the outbox's `Consumer.Run`/`loop`/`drain` scaffolding moved into `RunDurableConsumer`; `processMsg`'s exact decision tree became `handle` (empty→Ack; unparseable→Term loud; isDeleted/no-events→Ack; publish-fail→Nak; publish-ok→tombstone-then-Ack; key via `strings.TrimPrefix(msg.Subject, c.subjectPrefx)`). The `Decision`→`Ack/Nak/Term` mapping lives in the primitive's `applyDecision`, including "failed ack logged not retried." **Zero outbox test files changed** — `go test ./internal/processor/outbox/... -count=1` passes byte-for-byte unchanged (the strongest form of the sufficiency proof; the tests only call `New`/`Run`, both signature-preserved). Only `internal/processor/outbox/consumer.go` (production) changed: 20 insertions / 94 deletions.

**`MaxDeliver` finding (verified):** the live outbox `Run` set **no** `MaxDeliver` (unlimited JetStream-default redelivery on Nak) — confirmed by reading the pre-refactor `consumer.go`. The primitive preserves this exactly (`MaxDeliver <= 0` → omit). This **diverges** from `SubscribeKVChanges`'s default of 10 (which has a `maxDeliver == 0 → 10` clause); that divergence is a conscious choice for behavior-preservation, documented in `substrate.md` and called out here.

**Scaffolding duplication (flagged per Adjudication #2):** the `loop`/`drain`/`mc.Stop()`-watcher pattern is now copied a third time (outbox's old copy is gone, but `subscribe.go`'s `runKVSubscription` and `consumer.go`'s `runDurableLoop`/`drainDurable` both carry it). `SubscribeKVChanges` left **untouched** (live Refractor caller + auto-ack semantics). **No shared helper extracted** — a follow-up to unify the scaffolding once both are stable is warranted.

**Primitive direct tests (5):** `TestRunDurableConsumer_AckResumeFromLastAck`, `_NakRedelivers`, `_TermDoesNotRedeliver`, `_CleanShutdown`, `_ReadFromBody`.

**Architecturally-common justification (substrate §Principles bar):** a plain ack-disciplined durable consumer is shared by ≥2 real consumers — the outbox today, Loom (8.1) and Weaver (9.1) next — which clears the "architecturally common across components" bar for a substrate addition. Refractor's pause/lag/reset/`DeliverLastPerSubject`/per-rule machinery stays component-specific (the same principle drawing the line); none of it entered substrate.

**No CAR, no new Open Questions, no new dependency.** Adjudications #3 (`DeliverAllPolicy` fixed) and #4 (no CAR) held — no concrete forward gap surfaced that would force a policy knob.

### Debug Log — gate results & deviations

All gates run from repo root (Docker NATS/Postgres stack up; embedded-NATS for substrate/outbox).

- `go build ./...` → **PASS** (rc 0).
- `make vet` → **PASS**.
- `golangci-lint run ./...` → **PASS** — `0 issues.`
- `make verify-kernel` → **PASS** — `verify-kernel: ALL ASSERTIONS PASSED` (no kernel change; count unchanged).
- `make test-bypass` (Gate 2) → **PASS** — all Bypass #1/#3/#4 vectors **BLOCKED**.
- `make test-capability-adversarial` (Gate 3) → **PASS** — `4/4 cleared (3 DEFENDED, 1 ACCEPTED-WINDOW)`.
- `go test ./internal/substrate/... ./internal/processor/outbox/... -count=1` (central gate) → **PASS** (after one retry per Deviation 14; see below).
- `go test -p 1 ./... -count=1` → all packages **PASS** except `TestWaitForBootstrapComplete_BlocksOnServiceActorCapProjections` (`internal/bootstrap`).

**Deviation 14 (infra flake, retried once):** the embedded-NATS packages share the `/tmp/.../nats/jetstream` store dir, so a concurrent `go test ./...` collides ("`meta.inf.tmp: no such file`", "`no responders`", "`bucket name already in use`"). These hit **pre-existing** tests too (`TestSubscribeKVChanges_*`, `internal/processor`) — environmental, not a regression. Re-running the affected packages serially (`-p 1`) is fully green.

**Pre-existing unrelated failure (NOT this story):** `internal/bootstrap` `TestWaitForBootstrapComplete_BlocksOnServiceActorCapProjections` fails with `readiness gate timed out … does not contain "weaver"` (a live-stack readiness E2E expecting a weaver projection). **Verified pre-existing:** stashed the outbox change and the test failed identically. My change touches neither bootstrap nor weaver.

## Open Questions (for Winston)

1. **`MaxDeliver` default — match the live outbox (unlimited) or impose a bound?** The outbox's current `Run` sets **no** `MaxDeliver` (JetStream default = unlimited redelivery on Nak). The substrate `SubscribeKVChanges` defaults `MaxDeliver=10`. For behavior-preservation the refactored outbox must keep unlimited, so I've specified `cfg.MaxDeliver <= 0 → omit (unlimited)`. **Recommendation:** keep the primitive's default unlimited (omit when `<=0`) to preserve outbox behavior exactly; let callers opt into a bound. Flag if Winston wants the safer bounded default with the outbox explicitly opting out of it. *(Verify the live outbox config during DS — if it does set `MaxDeliver`, match that instead.)*

2. **Extract a shared `loop`/`drain` helper between `RunDurableConsumer` and `SubscribeKVChanges`, or accept duplication?** Both implement the same ctx-cancellable iterator + `mc.Stop()` watcher. A shared private helper (e.g. `drainMessages(ctx, cons, fn)`) would DRY it, but `SubscribeKVChanges` is auto-ack and has a live Refractor caller — refactoring it carries regression risk. **Recommendation:** accept the minor duplication for this story (leave `SubscribeKVChanges` untouched, copy the proven pattern into `consumer.go`); spin off a follow-up to unify the scaffolding once both are stable. Lower-risk and keeps the diff scoped to the new primitive + outbox.

3. **`DeliverPolicy` knob for forward consumers?** I've fixed `DeliverAllPolicy` (the outbox's policy) inside the primitive and excluded all policy knobs (per the AC's "minimal"). Loom 8.1 reads "reconciles a per-domain durable consumer … on restart resumes" — that's `DeliverAll` + durable, so the fixed policy covers it. **Recommendation:** keep it fixed; do not add a `DeliverPolicy` knob speculatively. If 8.1's actual implementation later needs `DeliverNew` (e.g. ignore pre-existing history on a fresh domain), add it then as a justified, named surface extension — not now. Confirm Winston agrees this is the right minimalism line.

4. **CONTRACT-AMENDMENT-REQUEST?** None anticipated — this is a pure internal-package refactor with no contract surface (`docs/contracts/*` is silent on substrate's internal consumer helpers; substrate already has a `CONTRACT-AMENDMENT-REQUEST.md` from a prior story, untouched here). **Recommendation:** no amendment needed; if the DS hits a frozen-contract gap, file `internal/substrate/CONTRACT-AMENDMENT-REQUEST.md` and continue with a different deliverable rather than editing a frozen contract.

---

## Winston — Lead Review (2026-06-05)

**Review depth: thorough lead review, NOT the full 3-layer fan-out** — justified per CLAUDE.md: a well-scoped, green, **behavior-preserving** refactor whose only genuinely new code is one small substrate file, with no security/auth-plane touch and the kernel unchanged. I read the new code line-by-line rather than spawning adversarial layers; flagging the reduced depth explicitly so it can be overridden.

- **New `internal/substrate/consumer.go` — reviewed line-by-line, sound.** Surface matches §0.1 exactly (method on `*Conn`; no `jetstream.Msg`/`Consumer` leaks; `DeliverAllPolicy` + `AckExplicitPolicy` fixed; `MaxDeliver>0` gated; three-way `Decision`; read-from-body `Message`). **Concurrency is correct:** the `drainDurable` watcher goroutine always exits (either `ctx.Done()→mc.Stop()` or the `stopped` close), `mc.Stop()` is idempotent across watcher+defer, no goroutine leak; ack is applied only **after** the handler returns (at-least-once preserved); a failed ack is logged-not-retried (handler idempotency covers the redelivery). Clean ctx-cancel shutdown.
- **Outbox refactor — behavior-preserving, strongest possible proof.** The decision tree in the new `handle()` is byte-for-byte equivalent to the old `processMsg` (empty→Ack; unparseable→Term-loud; isDeleted/no-events→Ack; publish-fail→Nak; publish-ok→tombstone-then-Ack; key recovery via `TrimPrefix(msg.Subject, subjectPrefx)`). **Zero outbox `*_test.go` files changed** — the production-consumer tests pass unchanged, which is the sufficiency proof the AC demands. `consumer.go` net −74 lines (scaffolding moved into the primitive).
- **MaxDeliver verified:** the live outbox set no `MaxDeliver` (unlimited); the primitive omits it when `<=0`, so the refactor preserves that exactly (Adjudication #1). Divergence from `SubscribeKVChanges`'s default-10 is documented.
- **Scaffolding duplication accepted** (Adjudication #2): the `loop`/`drain`/`mc.Stop()`-watcher pattern is copied into `consumer.go`; `SubscribeKVChanges` left untouched (live Refractor caller, auto-ack semantics). Now lives in two sibling files in the same package — a unify-the-scaffolding cleanup is *possible* but low-value (different ack semantics) and explicitly **not** done here. Left as an optional future cleanup, no task spun off.
- **No history/changelog comments** in any changed file (swept). No CAR. No new dependency. Dependency direction preserved (substrate imports only `nats.go`).

**Verification gates (run by Winston, all green):** `go build ./...`, `make vet`, `golangci-lint run ./...` (0 issues), `make verify-kernel` (unchanged), `make test-bypass` (Gate 2 — 4/4 BLOCKED), `make test-capability-adversarial` (Gate 3 — 4/4), and the scope tests **in isolation**: `go test ./internal/substrate/` (5 new `DurableConsumer` tests pass), `go test ./internal/processor/outbox/` (pass, unchanged). The dev's reported full-suite `go test ./...` failure (`TestWaitForBootstrapComplete_BlocksOnServiceActorCapProjections`) is the **Deviation-14 embedded-NATS shared-store collision** under concurrent packages, NOT a 7.6 regression — verified: the test passes **5/5 in isolation** (the Story 7.3 determinism fix holds) and 7.6 touches nothing in `internal/bootstrap` or `internal/refractor`.
