# Loom Control Plane — design + story brief

**Status:** done — implemented, 2-lens design review + 3-layer code review applied (no blockers; 6 fix-forward items resolved), all local gates green (build / vet / golangci-lint / loom+substrate+cli tests / verify-kernel).
**Backlog item:** Loom control plane (Loupe blocker #1) — see `_bmad-output/planning-artifacts/backlog.md`
**Type:** new control surface on the Loom engine; mirrors the proven `internal/weaver/control` pattern.

---

## Goal

Give Loom the same operator control surface Refractor and Weaver already expose, so a client (the CLI,
and later **Loupe**) can observe and steer Loom over NATS. Today only Refractor (`lattice.ctrl.refractor.*`)
and Weaver (`lattice.ctrl.weaver.*`) have `micro.Service` responders; Loom has none. This is the single
hard blocker for Loupe's "drive each component's control plane" capability.

## Scope (v1) — read-mostly + safe toggles only

| Op | Subject | Kind | Behaviour |
|----|---------|------|-----------|
| `list` | `lattice.ctrl.loom.list` (exact) | read-only | List Loom instances (instanceId, patternRef, subjectKey, cursor, status, retryCount). |
| `consumers` | `lattice.ctrl.loom.consumers` (exact) | read-only | List the engine's managed consumers + each one's pause state (`running` / `pausedManual` / `pausedStructural` / `pausedInfra`). |
| `inspect` | `lattice.ctrl.loom.<instanceId>.inspect` | read-only | One instance + its current step resolved from the **pinned** pattern (`instance.<id>.pattern`). Missing pin → graceful error, never a panic. |
| `pause` | `lattice.ctrl.loom.<consumerName>.pause` | mutate (safe) | `ConsumerSupervisor.Pause` (sets `PauseManual`; idempotent; persists to health-kv). |
| `resume` | `lattice.ctrl.loom.<consumerName>.resume` | mutate (safe) | `ConsumerSupervisor.Resume` (clears manual/structural pause; idempotent). |

**Subject shape:** `lattice.ctrl.loom.list` / `…consumers` (exact) + `lattice.ctrl.loom.<name>.<op>`
(5 tokens, `<name>` dot-free). Mirrors Weaver's exact-plus-wildcard registration
(`internal/weaver/control/service.go:150-166`).

### Explicitly EXCLUDED from v1 (binding — do not implement)

- **`fail` / `cancel` an instance** — forcibly terminating a live instance orphans any in-flight
  `outbox.<token>` record and parked `token.<token>` pointer (the relay may be mid-publish). This is
  operator-manual-recovery territory and needs its own atomicity story. **Not exposed.**
- **"pause the whole engine"** as one op — pausing all durables is not a graceful drain; a completion
  arriving while the relay is paused can dangle. Operators pause individual consumers explicitly.
- The control plane **never submits operations on Loom's behalf** — it reads state and toggles consumer
  pause only. No actor delegation into the handlers.

## Design

### New files (mirror `internal/weaver/control`)
- `internal/loom/control/service.go` — the `micro.Service` responder: `NewService(engine engineControl, cap CapabilityChecker, logger *slog.Logger)`, `StartNATSListener(ctx, nc)`, endpoint registration, per-request 5s timeout, subject parsing (`nameFromSubject`), capability check per request (stub).
- `internal/loom/control/capability.go` — `CapabilityChecker` interface + `StubCapabilityChecker` (allow-all + log), copied from `internal/weaver/control/capability.go`. **Carry (same as Weaver):** real Capability-KV integration of the control plane is deferred (Phase 3).
- `internal/loom/control/service_test.go` — table tests (below).

### Engine surface (`internal/loom`) — the `engineControl` interface the control pkg defines, implemented by `*loom.Engine`
Add the minimal read/control methods (no new mutation paths beyond the supervisor toggles):
- `ListInstances(ctx) ([]InstanceSummary, error)` — scan `instance.*` keys (dot-free suffix only — reuse the `runningInstanceCounter` key-filter discipline, `internal/loom/health.go`), unmarshal `Instance` (`internal/loom/state.go:54-62`), map to `InstanceSummary`. Returns all instance records with their status (running + any retained terminals), not just running.
- `ListConsumers(ctx) ([]ConsumerStatus, error)` — return the managed consumers + pause state from the Loom-side consumer-state cache that already feeds the heartbeat (`internal/loom/health.go:82-96`). If no cache accessor exists, add one (read-only).
- `InspectInstance(ctx, instanceID) (InstanceDetail, error)` — `getInstance` + read `instance.<id>.pattern`, resolve `pattern.steps[cursor]`. Missing instance → not-found error; missing pin on a live instance → explicit "definition pin missing" error (it is an invariant break, surfaced not panicked).
- `PauseConsumer(ctx, name) error` / `ResumeConsumer(ctx, name) error` — **validate `name` is a currently-managed consumer first** (reject unknown names with a clear error; do not blindly call the supervisor on an arbitrary name), then `supervisor.Pause/Resume`.

`*loom.Engine` already holds the `ConsumerSupervisor` (`internal/loom/engine.go`) and the loom-state
accessors — these methods are thin wrappers. Keep them read-only except the two supervisor toggles.

### CLI — `cmd/lattice/loom/loom.go` (mirror `cmd/lattice/weaver/weaver.go`)
Command group `loom` with: `list`, `consumers`, `inspect <instanceId>`, `pause <consumerName>`,
`resume <consumerName>`. Plain NATS request to the control subjects (NOT JetStream), table or `--json`
output. Reuse the weaver CLI's `validate*` (dot-free, non-empty token) + `request()` helpers' shape.

### Wiring — `cmd/loom/main.go`
After engine construction, before `engine.Start()` blocks: construct `control.NewService(engine, nil, logger)`
and `StartNATSListener(ctx, conn.NATS())`; log "loom control service started". Mirror `cmd/weaver/main.go:116-120`.

## Safety boundaries (binding)
1. `list` / `consumers` / `inspect` are **read-only** — no KV writes.
2. `pause` / `resume` go **only** through `ConsumerSupervisor.Pause/Resume` (idempotent, thread-safe, health-persisted). No other mutation.
3. Validate every `<name>` token: non-empty, dot-free (mirror `validateTargetID`). `pause`/`resume` additionally validate the name is a **managed consumer** before acting.
4. No `fail`/`cancel`/drain. No op-submission from handlers.
5. `inspect` must never panic on a missing/corrupt pin — surface a typed error.

## Test plan
- `internal/loom/control/service_test.go`: each op happy-path + error-path against a fake `engineControl`; unknown-consumer rejection on pause/resume; bad-subject (wrong token count, dotted name) rejection; capability-stub invoked per request; `inspect` missing-pin → typed error (not panic).
- `cmd/lattice/loom/loom_test.go`: command wiring + JSON/table render (mirror weaver CLI test).
- Engine-method tests (in `internal/loom`): `ListInstances` over seeded loom-state; `PauseConsumer`/`ResumeConsumer` validate-then-toggle; `InspectInstance` happy + missing-pin.
- Smoke: `cmd/loom` starts the listener (no port/subject clash with weaver/refractor).

## Verification gates (before done)
`go build ./...` · `make vet` · `golangci-lint run ./...` · `go test ./internal/loom/... ./cmd/lattice/loom/...` · `make verify-kernel` (must stay green — no bootstrap/identity change here) · `make test-bypass` + `make test-capability-adversarial` (unaffected, confirm still green).

## Out of scope / deferred
- Destructive instance ops (fail/cancel) → own story. **Forward note (from review):** a safe future `cancel` is `assert status==running` then reuse the existing `e.fail(ctx, inst, oldToken, reason)` path (`internal/loom/engine.go` ~1061), which goes through the atomic `transition` (instance-flip + pin-delete + token-delete + deadline-disarm; outbox collapses idempotently). Do NOT implement a naive key-deletion cancel (that orphans `outbox.<token>`/`token.<token>`).
- Real Capability-KV auth on the control plane → Phase 3 (same carry as Weaver/Refractor).
- A durable `loom.*` read model (Refractor lens over the lifecycle events) → backlog "Loom/Weaver control-API surfacing".

---

## Design-review corrections (BINDING — fold all in; they supersede conflicting prose above)

Two independent design-review lenses (architecture/feasibility + adversarial/safety) ran against the real code. Required changes:

**C1 [BLOCKER] `InspectInstance` must be terminal-safe and never panic.** Terminal instances set `inst.Cursor == len(pattern.Steps)` (`engine.go` ~496/~777) and the pin (`instance.<id>.pattern`) is **deleted** in the terminal batch (`state.go:276-287`). So:
- Branch on status **first**. For a **terminal** instance (`complete`/`failed`): return the instance summary with `currentStep: null` + a `terminal: true` marker; do **not** read the pin, do **not** treat the absent pin as an error.
- For a **running** instance: read the pin, **bounds-check `cursor < len(steps)`** before indexing (return a typed "cursor out of range" error otherwise — mirror the guard at `engine.go` ~1179), and only here is a missing pin the `errPatternPinMissing` invariant-break error.
- Tests (required rows): terminal instance, running w/ `cursor==len(steps)`, running w/ missing pin, missing instance — all typed error / structured output, never panic.

**C2 [BLOCKER] `pause` must exclude the dispatch/backstop-critical consumers.** `pause loom-outbox-relay` halts ALL op dispatch (in-flight steps stall; deadlines re-arm forever); `pause loom-deadline` disables the only stuck-instance failure backstop. These are engine-wide hazards, not "safe toggles". Therefore:
- `PauseConsumer` is allowed **only** on `loom-trigger` and the per-domain completion consumers (`loom-<domain>`); reject `loom-outbox-relay` and `loom-deadline` with a typed error explaining why (dispatch/crash-safety critical).
- For a per-domain consumer, the success response carries a note: "in-flight instances awaiting this domain will stall until resume."
- `ResumeConsumer` is unrestricted over **managed** names (resume is always safe; it can recover any pause state, including one set out-of-band).
- `consumers` (the read-only list op) still shows ALL managed consumers + state, including relay/deadline.

**C3 [SHOULD-FIX] Authoritative managed-name validation (not the lazy cache).** `ConsumerSupervisor.Pause/Resume` are **silent no-ops on an unknown name** (`consumer_supervisor.go:208-237`), and the consumer-state cache (`e.states`) is lazily populated, so it is NOT a reliable allow-list. Add a thread-safe **`ConsumerSupervisor.IsManaged(name) bool` + `ManagedNames() []string`** (read the `managed` map under its lock) and validate against those in `PauseConsumer`/`ResumeConsumer` — reject unknown names with a typed "consumer %q not managed" error (mirror Refractor's `pauseRule` "rule %q not registered", `refractor/control/service.go:455-461`). **Enforce in the engine method, not just the CLI** (Loupe + raw NATS are other clients); assert this in a test at the engine layer.

**C4 [SHOULD-FIX] `ListConsumers` = authoritative names + cached state.** Iterate `supervisor.ManagedNames()` for the name set (current + complete) and join each one's state from `e.states` snapshot (`internal/loom/health.go` cache; absent → `"running"`). Add `func (e *Engine) ListConsumers(...)`. This sidesteps the lazy-cache-missing-names problem.

**C5 [SHOULD-FIX] `InspectInstance` read-tearing.** Instance + pin are two reads; a concurrent terminal transition can delete the pin between them. Use the **instance status as the authority**: if `getInstance` says `running` but the pin read returns `ErrKeyNotFound`, **re-read the instance once** — if now terminal, report terminal (per C1), not an invariant break; only `running`+no-pin after re-read is the genuine error. Mark inspect step-resolution as best-effort/eventually-consistent in its response contract.

**C6 [SHOULD-FIX] `ListInstances` robustness.** Reuse the dot-free instance-key filter discipline from `runningInstanceCounter` (`health.go:113-121`) — skip keys whose suffix-after-`instance.` contains a `.` (those are `.pattern` pins). **Extract the predicate to a shared helper** rather than copy-pasting (avoid divergent copies). Tolerate per-key read failures by **skipping** that record (mirror `health.go:122-124`), not failing the whole list. Terminal records persist (only the pin is deleted), so `list` returns running + retained terminals with their `status`.

**C7 [SHOULD-FIX] Pause-survives-restart contract.** A manual pause persists across engine restart via health-kv (sticky until `resume`). The CLI `pause` output + the op response must say so ("persists across restart until `resume`"). (Instance-id stability governs whether the restore actually fires — an existing property; note it, don't fix it here.)

**C8 [SHOULD-FIX] Subject parsing is necessary-not-sufficient.** The 5-token parse (mirror `targetIDFromSubject`) does not by itself reject crafted names; safety vs `{"*", ">", "list", "consumers", "../x", unicode}` rests on the downstream **not-found** (inspect) and **not-managed** (pause/resume) guards — those are mandatory, not optional. Register **two** exact endpoints (`list` AND `consumers`) — do not copy Weaver's single-exact loop and drop `consumers`. Test rows for each crafted name.

**C9 [NIT] Wiring guard + copy hygiene.** Add the compile-time interface guard in `cmd/loom/main.go` (anonymous-interface literal listing the `engineControl` methods, mirroring `cmd/weaver/main.go:44-49`) so engine-signature drift fails at compile time. On copying `capability.go`, change the stub log literal from `"weaver"` to `"loom"`. Start the listener before `engine.Start` (it may serve an empty set briefly until `reconcileConsumers` runs — acceptable, mirrors Weaver).

**C10 [NIT] Auth posture.** Stub allow-all is consistent with the already-shipped Refractor/Weaver control planes (same `StubCapabilityChecker`); the relay/deadline DoS amplification is mitigated by C2 (they're not pausable). Real Capability-KV auth stays the shared Phase-3 carry.
