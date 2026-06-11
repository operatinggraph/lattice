# Story 8.4 — Blind Hunter (Adversarial, Diff-Only) Review

Scope: uncommitted working-tree diff only (`git diff HEAD` + new untracked `.go`
files). Planning artifacts under `_bmad-output/` ignored per instructions.

Files in scope:
- `internal/substrate/consumer.go`, `internal/substrate/keys.go`
- `internal/substrate/consumer_supervisor.go` (new)
- `internal/substrate/consumer_supervisor_pump.go` (new)
- `internal/substrate/consumer_supervisor_spec.go` (new)
- `internal/substrate/consumer_supervisor_test.go` (new)
- `internal/substrate/nak_with_delay_test.go` (new)
- `internal/refractor/pipeline/pipeline.go`, `pipeline_test.go`
- `internal/refractor/pipeline/supervisor_adapt.go` (new)
- `cmd/refractor/main.go`
- `internal/refractor/e2e_supervisor_helper_test.go` (new)
- `internal/refractor/refractor_*_e2e_test.go` (mechanical rewires)

---

## Findings

### 1. [Major] `awaitStarted` blocks Pause/Resume for up to 2s if `RunOn`/`Run` never succeeds — no escape hatch for callers
**File:** `internal/refractor/pipeline/pipeline.go:696-704` (new `awaitStarted`), `pipeline.go:686-694` / `707-717` (`Pause`/`Resume`)

```go
func (p *Pipeline) awaitStarted(ctx context.Context) {
	select {
	case <-p.started:
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
	}
}
```

If `Run` returns early because `p.supervisor.Add(ctx, spec)` errors (`pipeline.go:317-320`), `p.started` is **never closed**. Every subsequent `Pause(ctx)` / `Resume(ctx)` call from the control plane will then block for up to 2 seconds (or until the *caller's* `ctx` is done) before falling through to `p.supervisor.Pause/Resume`, which is a no-op anyway because the consumer was never registered (`s.managed[name]` doesn't exist — see `Pause`/`Resume` in `consumer_supervisor.go:208-231`, both `return` silently if `!exists`).

This is not catastrophic (the operator gets a slow no-op rather than an error), but every control-plane Pause/Resume RPC against a rule whose `Run` failed to start now eats a guaranteed multi-second stall with zero diagnostic signal — previously `Pause`/`Resume` were synchronous, fire-and-forget, with no such window. There is no log line anywhere indicating "consumer never started" to explain the latency to an operator debugging a slow control-plane response.

**Evidence:** `p.started = make(chan struct{})` is only closed at `pipeline.go:323`, strictly after the `Add` success path; the early-return at `pipeline.go:291-294` (`p.supervisor == nil`) and `pipeline.go:317-320` (`Add` error) both skip the close.

---

### 2. [Major] `RunOn` can be called more than once, silently leaking the previous `ConsumerSupervisor` and its registered consumer/pump
**File:** `internal/refractor/pipeline/pipeline.go:258-261` (new `RunOn`)

```go
func (p *Pipeline) RunOn(conn *substrate.Conn, cfg substrate.ConsumerSpec) {
	p.supervisor = substrate.NewConsumerSupervisor(conn)
	p.consumerCfg = cfg
}
```

`RunOn` unconditionally allocates a **new** `*ConsumerSupervisor` and overwrites `p.supervisor`/`p.consumerCfg`. There is no guard against calling `RunOn` twice on the same `*Pipeline`. If a caller does (e.g. a future hot-reload-of-config path, or a test bug), the first supervisor — and any pump goroutine started against it via a prior `Run` — has no reference left and is never `Stop()`'d: `Run`'s deferred cleanup (`<-ctx.Done(); p.supervisor.Stop()`) is keyed off `p.supervisor`, which now points at the *new* instance. The old supervisor's pump goroutine(s) and durable registration leak for the lifetime of the process (or until the shared `ctx` is cancelled, which doesn't stop them since `Add`'s pump uses `context.Background()`-derived `pumpCtx`, decoupled from the caller's `ctx` — see `consumer_supervisor.go:77`).

No test in the diff exercises double-`RunOn`, so this is currently latent, but the new exported method offers zero protection against a legitimate-looking misuse (e.g. retrying `RunOn` after a config validation error elsewhere in `main.go`).

---

### 3. [Minor] `dispositionEvalErr` / `writeResults` Transient path drops the underlying error entirely
**File:** `internal/refractor/pipeline/pipeline.go` (`dispositionEvalErr`, around line 732-743; `writeResults` final `Nak` branch around line 777-781)

Old code:
```go
if nakErr := msg.Nak(); nakErr != nil { ... }
return failure.CatTransient, err   // err is the original failure, propagated to caller of drain()
```

New code:
```go
return substrate.Nak, nil   // dispositionEvalErr — err silently dropped
...
return substrate.Nak, nil   // writeResults — writeErr silently dropped
```

The original `err`/`writeErr` is logged once via `slog.Error` immediately before the return, so the information isn't *lost* from logs, but the returned `(Decision, error)` tuple from `p.handle` no longer carries it. `consumer_supervisor_pump.go:processMsg` only inspects the error when `class` is `ClassInfra`/`ClassStructural` (it calls `classify(spec, herr)` only in that branch via the `disposed=false` path) — for a `ClassTransient` return with a non-nil error, `processMsg` would still call `classify(spec, herr)` first (line `processMsg`: `class := classify(spec, herr)`... wait — actually `herr` is checked `if herr != nil` *before* the decision is applied). Concretely: `dispositionEvalErr` previously returned `(failure.CatTransient, err)`; if it instead returned `(substrate.Nak, err)` (non-nil error) **with** `ClassTransient` from `classify`, `processMsg` would still fall through to `applyDecision(decision, ...)` per the comment "Transient/Terminal handler error: fall back to the returned Decision" (`consumer_supervisor_pump.go:266-277`) — i.e. returning the error here would have been **harmless and arguably more informative** for `Classify`/observability hooks. The author chose to return `nil` instead, which is a behavior-narrowing choice not called out anywhere as deliberate. Low impact today (no `Classify` hook currently inspects transient errors), but it's a quiet API contract change buried in a "mechanical" rewire.

---

### 4. [Minor] `TestSupervisor_ManualPause_SurvivesRestart` publishes a message on a subject the consumer's filter does not match
**File:** `internal/substrate/consumer_supervisor_test.go:159-160`

```go
publishDurableTestMsg(ctx, t, c, bucket, "sup-manual-msg-vtx.meta.x", []byte(`{"v":1}`))
publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.restored", []byte(`{"v":1}`))
```

The spec's `FilterSubject` is `"$KV." + bucket + ".vtx.meta.>"` (line 124). The first publish, `"sup-manual-msg-vtx.meta.x"`, does **not** start with `vtx.meta.` (it's a single non-hierarchical token `sup-manual-msg-vtx.meta.x`), so under the `vtx.meta.>` wildcard filter this message is presumably never delivered to `sup-manual` at all (depends on `publishDurableTestMsg`'s exact subject construction, which prefixes `$KV.<bucket>.`). If the helper does `$KV.<bucket>.sup-manual-msg-vtx.meta.x`, that subject does not match `$KV.<bucket>.vtx.meta.>` (first token after bucket is `sup-manual-msg-vtx`, not `vtx`). This first publish appears to be dead weight in the test — it neither contributes to nor threatens the assertion (which checks `processed == 0` then `processed > 0` after Resume), but it's a red flag that the test's filter-subject reasoning may not be what the author intended. Not a production bug; flagged because a misunderstanding of `DeliverLastPerSubject` + `FilterSubject` matching here could mask a real filter mismatch elsewhere in the new spec wiring (finding #5).

---

### 5. [Minor] No test covers `DeliverGroup` actually being applied / queue-group fan-out semantics for the new `ConsumerSpec.DeliverGroup`
**File:** `internal/substrate/consumer_supervisor.go:264-266` (`createConsumer`), `consumer_supervisor_spec.go:134-136`

```go
if spec.DeliverGroup != "" {
	cfg.DeliverGroup = spec.DeliverGroup
}
```

This is new exported behavior (NFR12 fan-out across instances, per the doc comment) wired into production (`cmd/refractor/main.go` sets `DeliverGroup: "refractor-" + r.ID`) and into every e2e test via `e2eSpec`/`specFor` (`DeliverGroup: "refractor-" + ruleID`). None of the new `consumer_supervisor_test.go` tests assert `info.Config.DeliverGroup` is actually set on the created durable, nor exercise two pumps sharing one `DeliverGroup` (the actual fan-out scenario NFR12 describes). `TestSupervisor_Reset_RecreatesWithNewFilter` checks `FilterSubject` survives a Reset but not `DeliverGroup`. If `Reset`'s delete+recreate ever dropped `DeliverGroup` (e.g. a future refactor of `createConsumer`), nothing in this diff would catch it. Pure test-coverage gap, not a confirmed bug — flagged because it's the one piece of "mechanical rewire" that is also new exported policy.

---

### 6. [Nit] Stale/awkward doc comment artifact from gofmt-driven reformat in `keys.go`
**File:** `internal/substrate/keys.go:134-141`

```go
// Key shape constants. Per Contract #1 §1.1:
//
//	vertex: vtx.<type>.<id>                                     (3 segments)
//	aspect: vtx.<type>.<id>.<localName>                         (4 segments)
//	link:   lnk.<type1>.<id1>.<localName>.<type2>.<id2>         (6 segments)
const (
```

This is purely a `gofmt`/`go vet` "doc comment" reformatting (tab-indented code block), unrelated to Story 8.4's substance, bundled into this diff. Not wrong, just scope creep — worth a one-line callout so it isn't mistaken for an intentional Contract #1 edit (Contract docs are frozen; this is a Go doc-comment in `internal/substrate`, not the contract itself, so no rule violation — just noise in the diff).

---

### 7. [Nit] `main.go` comment claims a collision constraint ("ruleID must not be 'adjacency'") that is asserted nowhere in code
**File:** `cmd/refractor/main.go:325-336`

```go
// Configure the supervised runtime: durable name refractor-<ruleID>,
// queue group = same name (NFR12), DeliverLastPerSubject (ADR-15), Core
// KV stream + filter. ruleID must not be "adjacency" (collides with the
// bootstrapper's refractor-adjacency consumer).
+		p.RunOn(conn, substrate.ConsumerSpec{
+			Name:          "refractor-" + r.ID,
```

The comment documents a real invariant (a lens with `ID == "adjacency"` would produce `Name: "refractor-adjacency"`, the same durable name the adjacency bootstrapper already uses), but nothing in `RunOn`, `validateSpec` (`consumer_supervisor.go:285-296`), or the lens-loading path (not in this diff) rejects or even logs on `r.ID == "adjacency"`. If a future lens definition is named `adjacency`, `Add`'s `CreateOrUpdateConsumer` (`consumer_supervisor.go:270`) would silently reconfigure the bootstrapper's durable out from under it — `CreateOrUpdateConsumer` is idempotent-update, not idempotent-reject. This is a documented landmine with no guard rail. Low severity because it requires a specific lens ID collision that's presumably controlled by planning/ops, but the comment itself is evidence the author was aware of the hazard and chose docs-only mitigation.

---

## Verdict Summary

| # | Severity | File:Line | Summary |
|---|----------|-----------|---------|
| 1 | Major | `pipeline.go:696-704` (`awaitStarted`), `686-717` (`Pause`/`Resume`) | If `Run` exits before `close(p.started)`, every `Pause`/`Resume` blocks up to 2s as a silent no-op, with no log signal |
| 2 | Major | `pipeline.go:258-261` (`RunOn`) | Calling `RunOn` twice silently replaces `p.supervisor`, leaking the prior supervisor's pump goroutine (decoupled `pumpCtx`, never `Stop()`'d) |
| 3 | Minor | `pipeline.go` `dispositionEvalErr` / `writeResults` Transient `Nak` branches | Original error dropped on Transient path (`return substrate.Nak, nil` vs old `(CatTransient, err)`) — quiet narrowing of the handler's error contract |
| 4 | Minor | `consumer_supervisor_test.go:159-160` | First `publishDurableTestMsg` subject likely doesn't match the consumer's `vtx.meta.>` filter — dead/confused test input |
| 5 | Minor | `consumer_supervisor.go:264-266`, e2e specs | New `DeliverGroup` field used in prod + all e2e specs but never asserted on the created durable, including across `Reset` |
| 6 | Nit | `keys.go:134-141` | Unrelated gofmt doc-comment reformat bundled into the diff (scope creep, harmless) |
| 7 | Nit | `main.go:325-336` | Comment documents a `ruleID == "adjacency"` collision hazard with no code-level guard |

**No Critical findings** — no concurrency races, deadlocks, banned history comments, or ack/nak correctness defects were found in the supervised-pump core (`consumer_supervisor.go` / `consumer_supervisor_pump.go`). The pause-reason composition, NakWithDelay floor, restore-on-startup, and Reset/reopen-trigger logic all read as correct and are reasonably well covered by `consumer_supervisor_test.go`. The two Major findings are both about edge cases in the new `Pause`/`Resume`/`RunOn` control-plane surface that the happy-path e2e tests don't exercise (Run always succeeds in tests, and `RunOn` is always called exactly once).
