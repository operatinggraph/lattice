# Story 9.4: Weaver control-API / CLI (FR30)

Status: done

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As an operator,
I want to list, disable, and revoke convergence targets,
so that I can manage convergence without a console (Phase 2 has no UI).

## Acceptance Criteria

1. **Given** installed `meta.weaverTarget` targets registered in the running Weaver engine's
   registry (`internal/weaver/registry.go` `targetSource`)
   **When** an operator invokes the Weaver control API/CLI (mirroring the Refractor control
   plane — NATS Services / `nats-io/nats.go/micro`, request-reply, per Contract-equivalent
   conventions in `internal/refractor/control/service.go`)
   **Then** `list` returns every currently-registered target: `targetId`, `lensRef`, the set of
   playbook gap columns (`gaps` keys), and the target's current control state (`active` |
   `disabled`), the latter derived from the lane-1 consumer's pause state
   (`consumerStateCache`/`health.weaver.<instance>.consumer.weaver-target-<targetId>`) and the
   weaver-state dispatch-skip marker (AC #3).

2. **Given** a registered target `targetId`
   **When** an operator invokes `disable <targetId>`
   **Then** the engine (a) calls `substrate.ConsumerSupervisor.Pause(ctx, "weaver-target-<targetId>")`
   — the existing FR30-cited primitive — pausing the target's lane-1 KV-CDC consumer (no further
   row delivery; `PauseManual`, cleared only by an operator `Resume`/`enable`), and (b) writes a
   **dispatch-skip marker** to `weaver-state` at key `<targetId>.__control` (AC #3 shape) so that
   (i) `handleRow`'s dispatch leg (mark-create + Actuator submit) and (ii) `scheduleFreshness`'s
   `@at` scheduling leg — both reachable for `<targetId>` via the **shared** `weaver-temporal`
   lane-3 consumer, which `Pause` cannot reach per-target — short-circuit for `<targetId>` even
   for an already-in-flight/redelivered message
   **And** disabling a target does **not** remove its `meta.weaverTarget` registration, does not
   call `reconcileConsumers`'s removal path, and does not touch the target's Lens definition —
   the target stays "installed", just inert.

3. **Given** the §10.3 `weaver-state` bucket's existing `<targetId>.<entityId>.<gapColumn>`
   in-flight-mark key shape (`internal/weaver/state.go`)
   **When** the dispatch-skip marker is introduced
   **Then** it is written at the reserved per-target key `<targetId>.__control`
   (`__control` is not a valid NanoID — entityIDs are NanoIDs, Contract #1 — so this key can
   never collide with a real entity's mark segment) with a small JSON body
   `{"disabled": true, "disabledAt": "<RFC3339>"}`; `enable`/`resume` deletes this key (treating
   a missing key as `active`, mirroring `markStore.delete`'s missing-key-is-success posture).

4. **Given** a registered target `targetId`
   **When** an operator invokes `revoke <targetId>`
   **Then** the engine (a) calls `substrate.ConsumerSupervisor.Remove(ctx, "weaver-target-<targetId>")`
   (lane-1 durable deleted, per the existing `reconcileConsumers` removal semantics) and deletes
   the consumer's health-sink entry, (b) deletes every `weaver-state` key with prefix
   `<targetId>.` — every `<targetId>.<entityId>.<gapColumn>` in-flight mark **and** the
   `<targetId>.__control` dispatch-skip marker, via a new `markStore.deleteByTargetPrefix`
   helper (`KVListKeys` + filter-by-prefix + `KVDelete`, mirroring the 9.2 sweeper's scan style),
   and (c) clears any standing `issueCache` entries keyed for `<targetId>` (`target:`,
   `consumer:`, `data:<targetId>.*`, `timer:<targetId>`)
   **And** `revoke` does **not** mutate the `meta.weaverTarget` vertex/spec — unregistering the
   Lens definition is an op-path (`lattice lens deactivate` / `TombstoneMetaVertex`) concern, out
   of this story's scope
   **And** because `revoke` does not unregister the target from `targetSource`, a target that is
   `revoke`d while its `meta.weaverTarget` vertex still exists and is still registered **will have
   its lane-1 consumer re-`Add`ed by the next `reconcileConsumers` pass** (the next registry
   load/update callback, or the next process restart's seeded reconcile) — `revoke`'s durable-delete
   + mark-clear is an **immediate operator-convenience cleanup**, not a standing suppression; a
   durable revoke requires the operator to also retire the target's Lens (out of scope). This
   bound is documented in `docs/components/weaver.md` and the CLI help text.

5. **Given** the `nats-io/nats.go/micro` dependency (currently used only by
   `internal/refractor/control`) and `internal/weaver`'s `boundary_test.go` (`TestModuleBoundary_OnlySubstrate`,
   `TestModuleBoundary_NoRawNATS`, both scoped to `go list -deps`/`go list -f .Imports` on
   `github.com/operatinggraph/lattice/internal/weaver` specifically)
   **When** the control service is implemented
   **Then** it lives in a **new sibling package `internal/weaver/control`** (NOT inside
   `internal/weaver` itself) that may import `nats-io/nats.go/micro` directly — `internal/weaver`
   itself never imports `internal/weaver/control`, so the existing boundary tests are unaffected
   and need no modification
   **And** `internal/weaver/control` depends on a small set of new exported `*weaver.Engine`
   methods (`ListTargets`, `Disable`, `Enable`, `Revoke` — AC #6) through which it drives the
   engine; it holds no direct reference to `substrate.ConsumerSupervisor`/`markStore`/registry
   internals.

6. **Given** AC #5's package split
   **When** `internal/weaver/engine.go` is extended
   **Then** four new exported methods are added to `*Engine`, each safe to call concurrently
   with the running engine (same `e.mu`-guarded reconcile path as `reconcileConsumers`):
   - `ListTargets(ctx) ([]TargetSummary, error)` — snapshot of `targetSource.targetIDs()` +
     `target(id)` (targetId, lensRef, gap columns) joined with `consumerStateCache` state and the
     `<targetId>.__control` marker read;
   - `Disable(ctx, targetID) error` — `supervisor.Pause` + write the `__control` marker; returns
     an error (mapped to a control-response error string, not a panic) if `targetID` is not
     currently registered;
   - `Enable(ctx, targetID) error` — `supervisor.Resume` + delete the `__control` marker; same
     not-registered error;
   - `Revoke(ctx, targetID) error` — `supervisor.Remove` + health-sink delete +
     `markStore.deleteByTargetPrefix` + issue-cache clears; **not** an error if `targetID` is not
     currently registered (a revoke of an already-torn-down/unknown target is idempotent —
     mirrors `ConsumerSupervisor.Remove`'s no-op-if-unmanaged posture).

7. **Given** AC #6's new methods
   **When** `handleRow` (evaluator) and `scheduleFreshness` (temporal) run for a row whose
   `targetId` carries the `<targetId>.__control` `{"disabled":true}` marker
   **Then** both legs **Ack and skip** (no mark CAS-create, no Actuator submit, no `@at`
   schedule-publish) for that target's rows — mirroring the existing
   `if _, ok := e.source.target(targetID); !ok { ... Ack }` early-exit shape already present in
   both handlers — and the level-reconciled **mark-clearing** leg of `handleRow` (§10.3, lands
   on every row regardless of `violating`) is **unaffected** (a disabled target's
   already-resolved gaps still clear their marks; only NEW dispatch is skipped) — this preserves
   the 9.1/9.2/9.3 regression net's mark-clearing assertions for a target that later transitions
   from disabled back to enabled with stale marks.

8. **Given** `internal/weaver/control`'s NATS Services registration
   **When** the control service starts
   **Then** it registers a `micro.Service` named `weaver-control` with one endpoint per op
   (`list`, `disable`, `enable`, `revoke`) under wildcard subjects
   `lattice.ctrl.weaver.*.<op>` (5-token subject, mirroring
   `internal/refractor/control.lensIDFromSubject`'s parse: `lattice.ctrl.weaver.<targetId>.<op>`),
   with one structural exception: **`list` takes no `targetId`** — it is registered on the
   4-token subject `lattice.ctrl.weaver.list` (no wildcard token) and returns ALL targets; the
   per-op dispatch and subject-token parsing in `internal/weaver/control/service.go` documents
   this asymmetry explicitly (do not force a synthetic `targetId` placeholder for `list`)
   **And** all endpoints share the framework's default queue group (multiple Weaver instances
   load-balance control requests — though in Phase 2 a single instance is typical)
   **And** `internal/weaver/control` exposes a `StartNATSListener(ctx, nc *nats.Conn) error`
   mirroring `internal/refractor/control.Service.StartNATSListener`'s lifecycle (stops on
   `ctx.Done()`).

9. **Given** `cmd/weaver/main.go`
   **When** the engine starts
   **Then** `cmd/weaver` constructs `control.NewService(engine)` and calls
   `StartNATSListener(ctx, conn.RawConn())` (or the substrate accessor that exposes the
   underlying `*nats.Conn` — `cmd/weaver` is NOT subject to the `internal/weaver` boundary test,
   only `internal/weaver` package itself is) alongside the existing `engine.Start(ctx)` — both
   run for the process lifetime and stop on the same `ctx` cancellation.

10. **Given** `cmd/lattice`'s 9 existing command groups (`cmd/lattice/{config,op,graph,lens,query,
    health,identity,candidates,authtrace,bootstrap}`, each `NewCommand(natsURL, outputFmt, ...)
    *cobra.Command` registered in `cmd/lattice/root.go`)
    **When** the operator CLI is extended
    **Then** a new command group `cmd/lattice/weaver/weaver.go` (package `weaver`, `NewCommand
    (natsURL, outputFmt *string) *cobra.Command`) is added with four subcommands: `list`,
    `disable <targetId>`, `enable <targetId>`, `revoke <targetId>` — each a `micro` client
    request to `lattice.ctrl.weaver.<targetId>.<op>` (or `lattice.ctrl.weaver.list` for `list`),
    rendering the JSON `ControlResponse`-equivalent either as `--output json` (raw passthrough)
    or a human-readable table/line, following the exact `output.Connect` /
    `output.PrintJSON` / `output.PrintJSONError` conventions used by `cmd/lattice/lens` and
    `cmd/lattice/health`
    **And** the command group is registered in `cmd/lattice/root.go` as
    `rootCmd.AddCommand(weaver.NewCommand(&flagNATSURL, &flagOutput))` (no actor-key dependency —
    control ops are not Processor-submitted ops, so no `defaultActor` param, mirroring `health`'s
    signature, not `lens`'s)
    **And** a connection/request error (no Weaver instance running / no responder) surfaces as
    `output.PrintJSONError("ControlError", ...)` under `--output json` or a plain `error: ...`
    otherwise — never a panic or a hang past a bounded request timeout (`output.DefaultTimeout`).

11. **Given** the FR30 "operator-facing only, no console dependency" requirement and the Phase 2
    `StubCapabilityChecker`/`StubAuthorizer` posture (`internal/refractor/control/capability.go`,
    `internal/processor`)
    **When** `internal/weaver/control` is implemented
    **Then** it includes the SAME stub-authorize-and-log posture (a `CapabilityChecker` interface
    + `StubCapabilityChecker` that allow-all + `slog.Info`s every call), called at the top of
    `dispatchEndpoint` before any op — full Capability-KV integration is explicitly deferred
    (matches Refractor's 2.1 stub posture, not a new gap introduced by this story).

12. **Given** the existing `internal/weaver/weaver_e2e_test.go` suite (9.1/9.2/9.3 regression net)
    and `boundary_test.go`
    **When** this story lands
    **Then** (a) `boundary_test.go` is unmodified and continues to pass (AC #5); (b) the existing
    e2e scenarios (mid-flight-kill, sweep/reclaim, temporal fire/replace/restart) are unmodified
    and pass; (c) a NEW e2e (or `internal/weaver` package-internal test, per the existing
    `*_internal_test.go` convention) scenario proves: disable a target with an in-flight
    violating row → no new mark/op is created on redelivery, but a row whose gap closes while
    disabled still clears its pre-existing mark (AC #7); enable un-disables and dispatch resumes
    on the next row delivery; revoke deletes the durable + all `<targetId>.*` weaver-state keys
    and a subsequent `list` no longer reports the consumer as managed (though the target may
    still be `targetIDs()`-registered per AC #4's documented bound); and (d)
    `internal/weaver/control` carries its own unit/integration test(s) for the
    `list`/`disable`/`enable`/`revoke` micro endpoints (request/response round-trip over an
    embedded NATS server, following `internal/refractor/control/service_test.go`'s pattern) and
    for `cmd/lattice/weaver`'s CLI subcommands (following `cmd/lattice/lens/lens_test.go`'s
    pattern).

## Tasks / Subtasks

- [x] **Task 1 — `markStore.deleteByTargetPrefix` + dispatch-skip marker primitives** (AC #3, #4)
  - [x] Add `markStore.deleteByTargetPrefix(ctx, targetID) (deleted int, err error)` to
    `internal/weaver/state.go`: `KVListKeys`, filter keys with prefix `targetID+"."`, `KVDelete`
    each (tolerate `ErrKeyNotFound` mid-scan — mirrors `reconciler.go`'s sweep-scan tolerance).
  - [x] Add `markStore.setDisabled(ctx, targetID, disabled bool) error`: `disabled=true` →
    `KVPut` at `<targetID>.__control` with `{"disabled":true,"disabledAt":<now>}`; `disabled=false`
    → `KVDelete` (missing-key-is-success).
  - [x] Add `markStore.isDisabled(ctx, targetID) (bool, error)`: `KVGet` at
    `<targetID>.__control`; `ErrKeyNotFound` → `(false, nil)`.
  - [x] Unit tests in `internal/weaver/state_internal_test.go` (new or extend existing) for all
    three: prefix-delete only matches `<targetID>.` (not a different target sharing a numeric
    prefix — use targetIds like `t1`/`t10` in the test to prove no accidental prefix overlap,
    since `"t1." `is not a prefix of `"t10."` — confirm `.` separator makes this safe by
    construction, but test it anyway), set/clear/idempotent-clear, and that `__control` round-trips
    through `singleTokenPattern`-style validation (it must never collide with an `entityID`
    NanoID segment — NanoIDs use `substrate.Alphabet`, which the dev should confirm does NOT
    include `_`, so `__control` can never be produced by `substrate.NewNanoID()`; if `_` IS in
    the alphabet, treat this as a structural finding and escalate — but do not let it block the
    story, since target row entityIDs are sourced from the projecting Lens, not from
    `NewNanoID()`, and a colliding entityID would be a pathological Lens bug independent of this
    story).

- [x] **Task 2 — Dispatch-skip guards in `handleRow` / `scheduleFreshness`** (AC #2, #7)
  - [x] In `internal/weaver/evaluator.go` `handleRow`, after resolving `target` and BEFORE the
    dispatch leg (but AFTER/alongside the existing level-reconciled mark-CLEARING leg, which must
    run regardless — AC #7), read `e.marks.isDisabled(ctx, targetID)`; if disabled, skip
    mark-create + Actuator submit for this row (Ack). Place the check so it does not interfere
    with the existing redelivery/`NumDelivered` semantics (9.1 post-review guards) — it is an
    ADDITIVE early-return on the dispatch branch only.
  - [x] In `internal/weaver/temporal.go` `scheduleFreshness`, at entry (before the `freshUntil`
    parse), read `e.marks.isDisabled(ctx, targetID)`; if disabled, return `true` (treat as
    "nothing to schedule", same as the absent-column case) without calling
    `e.act.scheduleTimer`.
  - [x] Both reads are per-row KV `Get`s on `weaver-state` (small bucket, low cardinality —
    one key per target, not per entity) — acceptable cost; do NOT cache in memory (the existing
    "durable dispatch state lives ONLY in weaver-state" doctrine, `engine.go` doc comment).

- [x] **Task 3 — `*Engine` control methods** (AC #6)
  - [x] New file `internal/weaver/control.go` (inside `internal/weaver`, exported surface only —
    the implementation package `internal/weaver/control` is separate, Task 5):
    `TargetSummary` struct (`TargetID`, `LensRef string`, `Gaps []string`, `State string` —
    `"active"`/`"disabled"`), `ListTargets`, `Disable`, `Enable`, `Revoke` per AC #6. `Gaps` is
    the sorted list of `Target.Gaps` map keys (deterministic JSON output).
  - [x] `Disable`/`Enable`/`Revoke` call `e.supervisor.{Pause,Resume,Remove}` with the
    `laneConsumerPrefix+targetID` durable name (existing constant); `Revoke` also calls
    `newConsumerHealthSink(...).delete(ctx)` (mirrors `reconcileConsumers`'s removal branch) and
    `e.marks.deleteByTargetPrefix` and clears `e.issues` keys `issueKeyConsumer(targetID)`,
    `"target:"+<ownerVertexId-if-resolvable, else skip>` (the issue is keyed by vertex id, not
    targetId, for `target:` — confirm via `registry.go`; if not resolvable from `targetID` alone,
    document the residual and accept it — it self-clears on the next successful
    load/reject cycle), `issueKeyData(targetID, freshUntilColumn)`, `issueKeyTimer(targetID)`.
  - [x] `Disable`/`Enable` return `fmt.Errorf("weaver: target %q not registered", targetID)` if
    `e.source.target(targetID)` is `(_, false)`. `Revoke` does NOT early-return on
    not-registered (AC #6).
  - [x] `ListTargets` takes `e.mu` is NOT required (read-only snapshot of already-thread-safe
    `targetSource`/`consumerStateCache`/`markStore`); confirm no lock ordering issue with
    `reconcileConsumers`'s `e.mu`.
  - [x] Internal tests in `internal/weaver/control_internal_test.go`: each method against a
    constructed `Engine` (the existing e2e harness's `provision`-style setup) — registered
    target → `ListTargets` reports it `active`; `Disable` → state `disabled`, lane-1 consumer
    paused (assert via `consumerStateCache`), `__control` marker present; `Enable` reverses;
    `Revoke` → durable gone (assert via supervisor or absence of further row delivery), marks
    gone, `ListTargets` no longer shows it managed (per AC #4's documented "still registered"
    caveat — `ListTargets` reflects `targetIDs()` membership separately from consumer-managed
    state, so decide and document which `ListTargets.State` value a revoked-but-still-registered
    target reports, e.g. `"revoked"` as a third state, or fall back to `"active"` since its
    consumer will be re-Added on the next reconcile — PICK ONE, document in `TargetSummary.State`
    godoc).

- [x] **Task 4 — e2e regression coverage** (AC #12b/c)
  - [x] New scenario(s) in `internal/weaver/weaver_e2e_test.go` (or a new
    `weaver_control_e2e_test.go` alongside it, same package/harness): disable-during-in-flight,
    enable-resumes-dispatch, revoke-clears-state — per AC #12c. Reuse the existing `provision`
    harness; do not restructure it.
  - [x] Confirm existing 9.1/9.2/9.3 scenarios still pass unmodified.

- [x] **Task 5 — `internal/weaver/control` package (NATS Services)** (AC #5, #8, #11)
  - [x] New package `internal/weaver/control/`: `service.go` (Service struct wrapping a
    `*weaver.Engine`-shaped interface — define a minimal local interface
    `type engineControl interface { ListTargets(...); Disable(...); Enable(...); Revoke(...) }`
    so `internal/weaver/control` depends on `internal/weaver` only for the four methods + the
    `TargetSummary` type, not the whole `Engine`), `capability.go` (`CapabilityChecker` +
    `StubCapabilityChecker`, mirroring `internal/refractor/control/capability.go` verbatim in
    spirit).
  - [x] `ControlRequest`/`ControlResponse` JSON shapes: `ControlResponse{ Targets
    []weaver.TargetSummary, Disable *DisableResult, Enable *EnableResult, Revoke *RevokeResult,
    Error string }` (mirrors Refractor's per-op-result-field pattern).
  - [x] `subjectPrefix = "lattice.ctrl.weaver"`; `list` registered on exact subject
    `lattice.ctrl.weaver.list`; `disable`/`enable`/`revoke` on wildcard
    `lattice.ctrl.weaver.*.<op>` with `targetIDFromSubject` (5-token parse, mirrors
    `lensIDFromSubject`).
  - [x] `StartNATSListener(ctx, nc *nats.Conn) error` — `micro.AddService(nc, micro.Config{Name:
    "weaver-control", ...})`, 4 endpoints, default queue group, stop on `ctx.Done()`.
  - [x] Unit/integration tests in `internal/weaver/control/service_test.go` (embedded
    `natstest.RunServer`, per the 9.3 precedent for embedded-NATS test harnesses) — round-trip
    each op against a fake `engineControl` implementation (no real `*weaver.Engine` needed for
    THIS package's tests — `internal/weaver`'s own tests, Task 3, cover the real engine wiring).

- [x] **Task 6 — `cmd/weaver` wiring** (AC #9)
  - [x] `cmd/weaver/main.go`: after constructing `engine`, construct
    `controlSvc := control.NewService(engine)` (the `*weaver.Engine` satisfies
    `control.engineControl` structurally — Go interface satisfaction, no explicit assertion
    needed unless the dev adds a compile-time `var _ control.engineControl = (*weaver.Engine)(nil)`
    check, which IS recommended for a clear compile error if the interface drifts) and
    `controlSvc.StartNATSListener(ctx, <raw *nats.Conn accessor>)`. Identify the
    `substrate.Conn` accessor for the raw `*nats.Conn` (grep for an existing exported
    accessor — e.g. check if `substrate.Conn` already exposes one for another `cmd/*` binary's
    use, such as `cmd/refractor/main.go`'s `controlSvc.StartNATSListener(ctx, nc)` call — `nc`
    there is likely the raw conn obtained alongside `substrate.Connect`, not derived from
    `*substrate.Conn`; if `cmd/weaver` only holds a `*substrate.Conn`, either (a) add a minimal
    exported accessor to `internal/substrate` — e.g. `func (c *Conn) NATSConn() *nats.Conn` — or
    (b) have `cmd/weaver` independently dial a second raw `*nats.Conn` for the control service,
    mirroring however `cmd/refractor` obtains its `nc`. Check `cmd/refractor/main.go` for the
    actual pattern before choosing; prefer (a) if `cmd/refractor` already has such an accessor,
    for consistency).
  - [x] Both `engine.Start(ctx)` and `controlSvc.StartNATSListener(ctx, nc)` run for the process
    lifetime; `StartNATSListener` is non-blocking (registers + returns), so it is called BEFORE
    the blocking `engine.Start(ctx)`.

- [x] **Task 7 — `cmd/lattice/weaver` CLI command group** (AC #10)
  - [x] New package `cmd/lattice/weaver/weaver.go`: `NewCommand(natsURL, outputFmt *string)
    *cobra.Command` with `list`, `disable <targetId>`, `enable <targetId>`, `revoke <targetId>`
    subcommands. Each builds a `micro`-client request (the `nats.Conn.Request` /
    `micro`-framework client call — check whether `nats-io/nats.go/micro` ships a client helper
    or whether a plain `nc.RequestWithContext(subject, nil, ...)` suffices, since `micro`
    endpoints respond to plain NATS requests on their subject regardless of client-side
    framework use — the Refractor control plane has no existing CLI client to mirror, so this is
    new ground; a plain `*nats.Conn.RequestWithContext` is almost certainly sufficient and
    simplest).
  - [x] `cmd/lattice/weaver/weaver_test.go` mirroring `cmd/lattice/lens/lens_test.go`'s
    structure/conventions.
  - [x] Register in `cmd/lattice/root.go`: `rootCmd.AddCommand(weaver.NewCommand(&flagNATSURL,
    &flagOutput))`.

- [x] **Task 8 — Docs** (house rule: new docs → `/docs`, not `_bmad-output/`)
  - [x] `docs/components/weaver.md`: new `## Control plane (FR30)` section documenting
    `list`/`disable`/`enable`/`revoke`, the `lattice.ctrl.weaver.*` subject scheme, the
    `<targetId>.__control` dispatch-skip marker, and the AC #4 revoke-vs-reconcile bound
    (revoke is immediate-cleanup, not standing suppression — full removal requires also
    retiring the target's Lens). Update the "Phase 2 implementation status" header to include
    9.4. Update "What this component will own" table with `internal/weaver/control/` and
    `cmd/lattice/weaver/`.

- [x] **Task 9 — Verification gates**
  - [x] `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`, `make
    test-bypass` (Gate 2, all BLOCKED), `make test-capability-adversarial` (Gate 3, all
    DEFENDED — this story touches no security-plane code, but the gate must stay green), `go
    test -count=1 ./internal/weaver/... ./internal/weaver/control/... ./internal/substrate/...
    ./cmd/lattice/... ./cmd/weaver/...`.

## Dev Notes

### Architectural grounding (read before starting)

- **Mirror, don't reinvent.** `internal/refractor/control/service.go` and `capability.go` are
  the primary grounding — the Weaver control service's `Service` struct shape, `ControlRequest`/
  `ControlResponse` envelope pattern, per-op `*Result` structs (`PauseResult{Paused:bool}` etc.),
  `dispatchEndpoint`/`<x>IDFromSubject` subject parsing, `StartNATSListener` lifecycle, and the
  `CapabilityChecker`/`StubCapabilityChecker` stub posture should all be mirrored at the
  structure/naming level for Weaver. Differences: Weaver has no `validate`/`rebuild`/`health`
  ops in this story's scope (health is already served by Contract #5 heartbeat + `lattice
  health component weaver`, not a new control endpoint) — only `list`/`disable`/`enable`/
  `revoke`. `list` is the one structural deviation (no per-target subject token — see AC #8).

- **The `nats-io/nats.go/micro` boundary.** `internal/refractor/control` is currently the
  ONLY importer of `nats-io/nats.go/micro` in the repo. `internal/weaver`'s
  `boundary_test.go` forbids `internal/weaver` (the package, by `go list -deps`/`go list -f
  .Imports`) from importing `nats-io/*` or `internal/{processor,loom,refractor}`. **A new
  sibling package `internal/weaver/control` is OUTSIDE that scope** — `go list -deps
  github.com/operatinggraph/lattice/internal/weaver` does not include
  `github.com/operatinggraph/lattice/internal/weaver/control` unless `internal/weaver` itself imports
  it, which it must NOT (the dependency runs the other way: `internal/weaver/control` imports
  `internal/weaver` for the four new `*Engine` methods + `TargetSummary`). This keeps
  `boundary_test.go` untouched and green. Do not be tempted to add a `substrate`-level micro
  wrapper — it is unnecessary machinery for a Sonnet-sized, well-bounded story, and Refractor's
  precedent already proves a `control` sub-package importing `micro` directly is the
  established pattern (Refractor's `control` package is `internal/refractor/control`, a sibling
  of `internal/refractor/{pipeline,lens,health,...}`, not inside any of those).

- **`Pause`/`Resume`/`Remove` already cite this story.** `internal/substrate/consumer_supervisor.go`
  `Pause` and `Resume` docstrings literally say "(operator control surface; FR30 / 9.4
  disable)" and "(FR31)" — these primitives exist and are unused until now. `Pause` is
  idempotent, sets `PauseManual` (cleared only by `Resume`), and persists through the spec's
  `HealthSink.SetPaused` (→ `consumerHealthSink` → `health.weaver.<instance>.consumer.<name>` =
  `{status:"paused", pauseReason:"manual"}`). On restart, `supervisor.Add` →
  `restoreState` → `Load` → `PauseManual` → `waitWhilePaused` — **the lane-1 pause survives a
  Weaver restart with ZERO new persistence work**, confirmed by reading
  `consumer_supervisor_pump.go`'s `restoreState` switch. This is why AC #2 needs ONLY the new
  `<targetId>.__control` marker for the **dispatch-skip** half (lane-3 shared-consumer +
  `list`'s queryable state), not for the lane-1 pause-restore half.

- **Why lane-3 needs its own skip.** `internal/weaver/temporal.go`'s `weaver-temporal` durable
  is a SINGLE FIXED consumer shared across ALL targets (`temporalConsumerName`, filtered
  `schedule.weaver.timer.fired.>`). `supervisor.Pause("weaver-target-<targetId>")` only pauses
  that target's lane-1 row consumer — it cannot pause "just this target's slice" of the shared
  temporal consumer. `scheduleFreshness` (called from `handleRow`, lane-1) would itself stop
  being invoked once lane-1 is paused for `<targetId>` — BUT any timer ALREADY scheduled before
  disable will still fire and reach `handleFiredTimer` (lane-3, unpaused, shared). AC #7's
  `scheduleFreshness` guard prevents NEW schedules from being published while disabled (belt);
  `handleFiredTimer` itself is NOT required to re-check `__control` in this story (suspenders) —
  **a fired timer for a disabled target still converts to `MarkExpired` and is submitted**. This
  is an ACCEPTED, documented bound (not a contradiction of "Weaver stops acting on it" — a
  `MarkExpired` op flips a freshness column, which is itself subject to AC #2's dispatch-skip on
  the NEXT row delivery's mark-create). If review finds this insufficient, the fix is a second
  `isDisabled` check in `handleFiredTimer`, deferred here for scope (flag as Question 4).

- **`__control` key collision safety (AC #3).** `weaver-state` keys are
  `<targetId>.<entityId>.<gapColumn>`; `entityId` is the row's NanoID
  (`substrate.NanoIDLength`-character string over `substrate.Alphabet`, Contract #1). `__control`
  is a fixed, non-NanoID-shaped literal (`_` characters; NanoIDs use the canonical Lattice
  alphabet — confirm in `internal/substrate` whether `_` is a member; even if so, `__control` is
  longer/shaped differently than `NanoIDLength` and is a CONSTANT, not derived — so a genuine
  collision would require a Lens to literally project an entity row whose `entityKey`'s NanoID
  segment is the literal string `__control`, which `substrate.NewNanoID()` never produces. This
  is the same class of reserved-segment convention as `firedToken` ("fired") in
  `temporal.go` — precedent exists for a reserved literal token in an otherwise-open key/subject
  space.

- **`TargetSummary.State` for a revoked-but-still-registered target (Task 3).** AC #4 documents
  that `revoke` does not unregister from `targetSource`, so `targetIDs()` still returns the
  target and the next `reconcileConsumers` re-`Add`s its consumer. `ListTargets` must report
  SOME state for this window. Two options: (a) a third `TargetSummary.State` value `"revoked"`
  read from... nothing durable (revoke leaves no marker — `__control` is disable's marker, and
  revoke explicitly deletes it) — so `"revoked"` would only be knowable instantaneously, not
  after a reconcile re-adds the consumer, making it a misleading/stale value; OR (b) `ListTargets`
  simply reports `"active"` once the consumer is re-managed (the natural/honest answer — revoke's
  effect was transient). **Recommendation: (b)** — do not invent a `"revoked"` state; `revoke`'s
  CLI response (`RevokeResult{Revoked:true}`) is the operator's confirmation that the cleanup
  ran, and a subsequent `list` reflects current reality (which may show the target `"active"`
  again once reconciled — and the docs (Task 8) explain why). This keeps `TargetSummary.State`
  a clean two-value enum (`active`/`disabled`) matching AC #1's literal text. Flagged as Question
  2 for Winston to confirm.

### Key files (read these; this is the FULL touch-set)

**New:**
- `internal/weaver/control.go` — `TargetSummary`, `ListTargets`, `Disable`, `Enable`, `Revoke`
  on `*Engine`.
- `internal/weaver/control_internal_test.go`
- `internal/weaver/weaver_control_e2e_test.go` (or extend `weaver_e2e_test.go`)
- `internal/weaver/control/service.go`, `capability.go`, `service_test.go` — the NATS Services
  control plane (new package, mirrors `internal/refractor/control/`).
- `cmd/lattice/weaver/weaver.go`, `weaver_test.go` — CLI command group.

**Modified:**
- `internal/weaver/state.go` — `markStore.deleteByTargetPrefix`, `setDisabled`, `isDisabled`.
- `internal/weaver/evaluator.go` — `handleRow` dispatch-skip guard (AC #2/#7).
- `internal/weaver/temporal.go` — `scheduleFreshness` dispatch-skip guard (AC #2/#7).
- `cmd/weaver/main.go` — wire `internal/weaver/control.Service` alongside `engine.Start`.
- `cmd/lattice/root.go` — register the new `weaver` command group.
- `internal/substrate/*` — possibly a minimal raw-`*nats.Conn` accessor IF `cmd/weaver` needs
  one and none exists (Task 6 — check `cmd/refractor/main.go`'s pattern first).
- `docs/components/weaver.md` — new Control plane (FR30) section + status header.

**Untouched (regression net / do not disturb):**
- `internal/weaver/{engine,registry,strategist,actuator,health,health_sink,reconciler}.go`'s
  existing logic (only `evaluator.go`/`temporal.go` get additive guards; `engine.go` gets new
  exported methods in the new `control.go` file, not edits to existing methods).
- `internal/weaver/boundary_test.go` (must remain green, unmodified).
- `internal/refractor/*`, `internal/loom/*`, `internal/processor/*`, `docs/contracts/*`,
  `_bmad-output/planning-artifacts/*`.

### Project Structure Notes

- `internal/weaver` stays the engine; `internal/weaver/control` is a NEW sibling package (Go
  import path `github.com/operatinggraph/lattice/internal/weaver/control`) — same directory tree,
  different package, one-directional dependency (`control` → `weaver`, never the reverse).
  This mirrors `internal/refractor/control` being a sibling of `internal/refractor/{pipeline,
  lens,...}`.
- `cmd/lattice/weaver` is a NEW command-group package (10th group), following the exact
  `cmd/lattice/{lens,health}` file/test/registration conventions.
- No bootstrap/provisioning changes; no new buckets (the `__control` marker lives in the
  EXISTING `weaver-state` bucket); no new streams.

### Previous story intelligence (9.1/9.2/9.3 all done)

- **Additive-guard discipline.** 9.2 and 9.3 both landed new legs (sweep, temporal) as additive
  call-sites into `handleRow` / new files, without restructuring `handleRow`'s existing
  decision tree — review found the handler/reconcile seams are exactly where regressions
  cluster. Task 2's dispatch-skip checks MUST be the same style: an early-return guard, not a
  rewritten dispatch branch.
- **Issue-key hygiene.** The `issueCache` uses prefix-keyed issue codes (`target:`, `consumer:`,
  `data:`, `timer:`, `sweep:`, `pendingSpec:`) — `Revoke`'s issue-clear set (Task 3) must use the
  SAME helper functions (`issueKeyConsumer`, `issueKeyData`, `issueKeyTimer`) rather than
  reconstructing key strings, to stay consistent if those helpers' formats ever change.
- **No history comments.** Per CLAUDE.md, do not write `// Story 9.4 adds...` anywhere in
  `internal/weaver/*` or `cmd/*` — comments describe current behavior only. Contract references
  ("FR30", "Contract #10 §10.3") are fine (existing precedent throughout `internal/weaver`).
- **Embedded-NATS test harness** (`natstest.RunServer`, JetStream on) is the proven pattern for
  `internal/weaver/control/service_test.go`'s round-trip tests (9.3 probe confirmed
  `core-schedules` + scheduler work in-process; `nats-io/nats.go/micro` is a pure client-side
  framework over the same connection, so it needs nothing the embedded server doesn't already
  provide).
- **Gate 3 (`test-capability-adversarial`) — why it's still in scope.** This story touches no
  Capability-KV / auth-plane code, but Gate 3 is a blanket repo-wide adversarial suite (Task 9)
  — it must stay green as a regression check, not because this story changes its target surface.

### References

- [Source: _bmad-output/planning-artifacts/epics/phase-2-epics.md#Story 9.4]
- [Source: internal/refractor/control/service.go] — primary control-plane grounding (Service
  struct, ControlRequest/Response, dispatchEndpoint, lensIDFromSubject, StartNATSListener).
- [Source: internal/refractor/control/capability.go] — CapabilityChecker/StubCapabilityChecker
  stub posture.
- [Source: internal/substrate/consumer_supervisor.go] — `Pause`/`Resume`/`Remove`, both
  explicitly FR30/9.4-labeled in docstrings.
- [Source: internal/substrate/consumer_supervisor_pump.go] — `restoreState` (pause persists
  across restart via HealthSink).
- [Source: internal/weaver/health_sink.go] — `consumerHealthSink` (SetActive/SetPaused/Load/
  delete), `health.weaver.<instance>.consumer.<name>` key shape.
- [Source: internal/weaver/state.go] — `markStore`, §10.3 `<targetId>.<entityId>.<gapColumn>`
  key shape, `markTTLBackstopFactor`.
- [Source: internal/weaver/registry.go] — `targetSource.target(id)`/`targetIDs()`, `Target`/
  `GapAction` shapes, `singleTokenPattern`.
- [Source: internal/weaver/engine.go] — `laneConsumerPrefix`, `reconcileConsumers`,
  `supervisedHandler`.
- [Source: internal/weaver/evaluator.go] — `handleRow`.
- [Source: internal/weaver/temporal.go] — `scheduleFreshness`, `handleFiredTimer`,
  `temporalConsumerName` (shared/fixed durable).
- [Source: internal/weaver/health.go] — `issueCache`, `consumerStateCache`, issue-key helpers.
- [Source: internal/weaver/boundary_test.go] — module-boundary scope (package-specific
  `go list`, not directory-tree-wide).
- [Source: cmd/weaver/main.go] — engine wiring pattern for Task 6.
- [Source: cmd/refractor/main.go] — `controlSvc.StartNATSListener(ctx, nc)` call site, pattern
  for obtaining the raw `*nats.Conn` (Task 6 must check this).
- [Source: cmd/lattice/root.go, cmd/lattice/lens/lens.go, cmd/lattice/health/health.go] — CLI
  command-group conventions (Task 7).
- [Source: cmd/lattice/output/{connect,output,submit}.go] — `output.Connect`/`PrintJSON`/
  `PrintJSONError`/`DefaultTimeout`.
- [Source: docs/components/weaver.md] — status header, "What this component will own", In/Out
  tables (Task 8).
- [Source: _bmad-output/implementation-artifacts/9-1-weaver-target-lens-violation-lane.md,
  9-2-weaver-mark-ttl-reconciler.md, 9-3-weaver-temporal-lane.md] — prior completion notes,
  additive-guard discipline, issue-key conventions.

## Dev Agent Record

### Agent Model Used

Claude (Amelia dev-story sub-agent), Sonnet 4.6.

### Debug Log References

- `go build ./...` — clean (whole repo).
- `make vet` — clean.
- `golangci-lint run ./...` — 0 issues.
- `make verify-kernel` — ALL ASSERTIONS PASSED.
- `make test-bypass` — Gate 2 PASSED (4/4 BLOCKED); `internal/bypass` suite also includes Gate 3
  (TestCapAdv*/TestGate3_Report), all PASS.
- `make test-capability-adversarial` — Gate 3 PASSED (4/4 cleared — 3 DEFENDED, 1
  ACCEPTED-WINDOW).
- `go test -count=1 ./internal/weaver/... ./internal/substrate/... ./cmd/...` — all packages
  `ok` (`internal/weaver` 71.9s, `internal/weaver/control` 0.69s, `internal/substrate` 7.7s,
  `cmd/lattice/*` and `cmd/lattice/weaver` all `ok`).

### Completion Notes List

- All 7 pre-adjudicated open questions (package split, 2-value State enum, Revoke as strict
  superset of Disable, `handleFiredTimer` disabled-skip, subject scheme + CLI signature,
  in-memory disabled-set cache pattern, enable/disable naming) implemented exactly as Winston's
  amendments specified.
- **AC #3 reserved-key safety**: `controlKey(targetID) = targetID + ".__control"` has exactly
  one dot (vs. a real mark's two dots `<targetId>.<entityId>.<gapColumn>`); `substrate.Alphabet`
  contains no `_`, so `__control` can never be produced by `substrate.NewNanoID()`. Both
  properties are asserted in `state_internal_test.go`. The reconciler sweep explicitly skips
  `__control`-suffixed keys (`TestSweep_ControlMarkerSurvives`).
- **Task 1**: `markStore.deleteByTargetPrefix`, `setDisabled`, `isDisabled`, `controlKey` added
  to `state.go`; the explicit `t1`/`t10` prefix-overlap case is proven by
  `TestDeleteByTargetPrefix_OnlyMatchesOwnTarget` (deletes exactly the 2 keys under `t1.`,
  leaves `t10.`'s 2 keys untouched).
- **Task 2**: dispatch-skip guards added to `handleRow` (after `clearClosedMarks`, before the
  dispatch leg), `scheduleFreshness` (at entry), and — per the Winston amendment beyond the
  story's original Task 2 scope — `handleFiredTimer` in `temporal.go` (skip the `MarkExpired`
  submit when the target is disabled). All three read the in-memory `disabledTargetSet`
  (`e.isTargetDisabled`), not a per-message KV `Get`, per the Q6 amendment (durable truth is the
  `__control` marker; in-memory cache seeded at `Start` via `seedDisabledTargets`, updated
  synchronously by Disable/Enable/Revoke).
- **Task 3**: `internal/weaver/control.go` — `TargetSummary` (2-value `State` enum,
  `active`/`disabled`), `ListTargets`, `Disable`, `Enable`, `Revoke`. `Revoke` is a strict
  superset of `Disable` (Q3 amendment): removes the durable + health-sink entry, deletes every
  `<targetID>.*` weaver-state key (marks + `__control`), clears issue-cache entries via
  `issueKeyConsumer`/`issueKeyData`/`issueKeyTimer`/`"target:"+ownerVertexID` (new
  `registry.ownerVertexID` accessor), then re-writes the `__control` disabled marker so a later
  reconcile re-add stays inert until an explicit Enable. `ListTargets` reports a
  revoked-but-still-registered target as `"disabled"` (per the Q2/Q3 resolution — no third
  `"revoked"` state).
- **Task 4**: AC #12c scenarios implemented as package-internal tests in
  `control_internal_test.go` using the existing `handlerHarness`-style direct `handleRow`
  invocation (the e2e `weaver_e2e_test.go` harness was deliberately NOT modified, since
  `Disable` pauses the lane-1 consumer entirely — no row delivery occurs while paused, so the
  "redelivery while disabled" and "mark still clears while disabled" scenarios are only
  meaningfully testable by driving `handleRow` directly with the in-memory disabled-set toggled
  via `Disable`/`Enable`/`Revoke`, which is exactly the dispatch-skip-guard behavior AC #12c
  targets). Three new tests: `TestHandleRow_DisabledSkipsDispatchButClearsMarks` (no new mark/op
  for a new violating entity while disabled; a pre-existing mark for a different entity still
  clears when its gap closes — proving `clearClosedMarks` is unaffected by the disabled-skip
  guard), `TestHandleRow_EnableResumesDispatch` (Disable suppresses, Enable resumes dispatch on
  the next row delivery), `TestHandleRow_RevokeRemovesDurableAndConsumerGone` (Revoke removes
  the durable + mark; `ListTargets` still lists the target as `disabled`, per AC #4's documented
  "still registered" bound). Full `go test ./internal/weaver/... -count=1` (70–72s) confirms the
  9.1/9.2/9.3 e2e suite (`weaver_e2e_test.go`, unmodified) and `boundary_test.go` still pass.
- **Task 5**: new package `internal/weaver/control/` — `capability.go`
  (`CapabilityChecker`/`StubCapabilityChecker`, allow-all + log, mirrors
  `internal/refractor/control/capability.go`), `service.go` (`Service`, `ControlRequest`/
  `ControlResponse`, `*Result` types, `StartNATSListener` registering `weaver-control-list` on
  the exact subject `lattice.ctrl.weaver.list` and `weaver-control-<op>` on
  `lattice.ctrl.weaver.*.<op>` for disable/enable/revoke, `targetIDFromSubject` 5-token parse),
  `service_test.go` (fake `engineControl`, embedded NATS, 9 tests covering all 4 ops + error
  paths + unknown-op + already-started).
- **Task 6**: `cmd/weaver/main.go` — added a compile-time `engineControl` interface-satisfaction
  check, constructs `control.NewService(engine, nil, logger)` and calls
  `controlSvc.StartNATSListener(ctx, conn.NATS())` before the blocking `engine.Start(ctx)`.
  `*substrate.Conn.NATS()` already existed (used by `cmd/refractor/main.go`'s equivalent
  call) — no `internal/substrate` changes needed, no CONTRACT-AMENDMENT-REQUEST.
- **Task 7**: new package `cmd/lattice/weaver/` — `weaver.go` (`NewCommand`, `list`/`disable`/
  `enable`/`revoke` subcommands reusing `internal/weaver/control`'s exported
  `ControlResponse`/`ListSubject()`/`TargetSubject()`), `weaver_test.go` (fake engine + embedded
  NATS responder, 6 tests). Registered in `cmd/lattice/root.go` (now 11 command groups).
- **Task 8**: `docs/components/weaver.md` — status header and "Phase 2 implementation status"
  updated to 9.1–9.4 with the control-plane row marked shipped; new "## Control plane (FR30,
  Story 9.4)" section documenting the subject scheme, the `__control` dispatch-skip marker +
  in-memory cache, `disable`/`enable`, `revoke`'s strict-superset semantics and
  revoke-vs-reconcile bound, and the capability stub; "What this component will own" and the
  In/Out contracts table updated with `internal/weaver/control/` and `cmd/lattice/weaver/`.
  `docs/components/scheduling.md` was not touched (no 9.4-relevant content; its prior-story
  diff was already committed).
- **Task 9**: all verification gates green — see Debug Log References. No deviations; no
  CONTRACT-AMENDMENT-REQUEST needed.
- A stray `weaver` build artifact at the repo root (produced by an earlier `go build ./...`)
  was removed before finishing — not a deliverable.

### Code-review fix-forward (post-CR triage, Winston batch)

Adjudicated batch of the three 9.4 CR layers (`9-4-cr-blind-hunter.md`,
`9-4-cr-edge-case-hunter.md`, `9-4-cr-acceptance-auditor.md`) implemented in one fix-forward pass:

1. **Revoke re-add bug (HIGH, BH-1).** `Revoke` now drops `e.targets[targetID]` under `e.mu` so
   the next `reconcileConsumers` re-Adds an (inert) consumer for the still-registered target; and
   `Enable` now re-runs `reconcileConsumers` so enable-after-revoke restores the consumer
   immediately. Without this a revoked→enabled target was dead until restart.
   (`internal/weaver/control.go`; test `TestRevokeEnable_ReAddsConsumerViaReconcile`.)
2. **Disabled-skip narrowed to remediation only (ECH-5 — corrects the earlier Q4 ruling).** The
   disabled state now suppresses ONLY remediation, not violation-detection bookkeeping:
   `handleFiredTimer` STILL submits `MarkExpired` (state-recording, already §9.3-guarded);
   `scheduleFreshness` STILL arms/re-arms timers while disabled; the `handleRow` disabled-skip
   moved to AFTER `clearClosedMarks` + `scheduleFreshness`, gating only the mark-create +
   Strategist/Actuator dispatch block. (`internal/weaver/temporal.go`, `evaluator.go`; test
   `TestDisabled_FreshnessExpiryRecordedNoRemediation`; AC#12c tests + weaver.md updated.)
3. **Fail-safe-to-inert ordering (BH-2, ECH-3, ECH-4).** `Disable` writes the `__control` marker +
   in-memory set FIRST, then `Pause`; `Enable` `Resume`s FIRST, then deletes the marker + clears the
   set. Every partial-failure/restart window lands on "still disabled (inert)". The marker is the
   restart authority for the remediation-skip; HealthSink pause-restore is independent
   (lane-1 pumping only). Documented in weaver.md. (`internal/weaver/control.go`.)
4. **`__control` marker leak on genuine uninstall (ECH-1).** `reconcileConsumers`'s removal branch
   now deletes the target's `<targetId>.__control` marker and prunes the in-memory set, so a
   re-install of the same targetId does not silently come up disabled.
   (`internal/weaver/engine.go`; test `TestReconcileRemove_DeletesControlMarker`.)
5. **`countInFlight` skips reserved markers (ECH-2).** The `marksInFlight` gauge now skips
   `__control` keys (same reserved-suffix guard as the sweep). (`internal/weaver/state.go`.)
6. **Control-handler timeout + error surfacing (BH-3, BH-6).** Control handlers now derive a
   bounded `context.WithTimeout(…, 5s)` instead of `context.Background()`, so a blocked KV op fails
   the request instead of wedging the responder; `respondMicro` always sends a reply (an error
   envelope) on marshal failure; the dead `ControlRequest` type was removed.
   (`internal/weaver/control/service.go`.)
7. **CLI targetId validation (ECH-6).** `disable`/`enable`/`revoke` reject an empty or
   dot-containing targetId client-side (clear error) instead of building an unroutable subject that
   hangs to a "no responders" timeout. (`cmd/lattice/weaver/weaver.go`.)
8. **Hygiene (BH-4, BH-5, AA-F3).** Fixed the lying `controlKeySuffix` collision-safety comment
   (the safety is the `missing_` gap-column prefix + dot-free targetId + reserved suffix, not the
   entityId NanoID); de-duped the double key-read in `seedDisabledTargets` (new `isDisabledKey`);
   added an `s.mu` guard around the `microSvc` check in `StartNATSListener` to match Refractor.
   (`internal/weaver/state.go`, `control.go`, `control/service.go`.)

Accepted-without-change per the batch: 2-value State enum (Q2), revoke-as-strict-superset writing
the marker (Q3), subject scheme + CLI location (Q5), in-memory-set-rebuilt-from-durable (Q6),
enable/disable naming (Q7).

Gates after the batch: `go build ./...`, `make vet`, `golangci-lint run ./...` (0 issues),
`make verify-kernel` (ALL ASSERTIONS PASSED), `make test-bypass` (Gate 2 4/4 BLOCKED + Gate 3),
`make test-capability-adversarial` (4/4 cleared), `go test -count=1 ./internal/weaver/...
./internal/weaver/control/... ./internal/substrate/... ./cmd/...` — all green (weaver 73.0s). The
9.1/9.2/9.3 e2e + `boundary_test.go` regression net stays green.

### File List

**New:**
- `internal/weaver/control.go`
- `internal/weaver/control_internal_test.go`
- `internal/weaver/state_internal_test.go`
- `internal/weaver/control/capability.go`
- `internal/weaver/control/service.go`
- `internal/weaver/control/service_test.go`
- `cmd/lattice/weaver/weaver.go`
- `cmd/lattice/weaver/weaver_test.go`
- `internal/weaver/export_test.go` (carried from 9.3; unchanged this story — included for
  completeness of the touch-set, no new edits)

**Modified:**
- `internal/weaver/state.go` — `controlKeySuffix`, `controlMark`, `controlKey`,
  `setDisabled`/`isDisabled`/`deleteByTargetPrefix`.
- `internal/weaver/reconciler.go` — sweep skips `__control`-suffixed keys.
- `internal/weaver/evaluator.go` — `handleRow` dispatch-skip guard.
- `internal/weaver/temporal.go` — `scheduleFreshness` and `handleFiredTimer` dispatch-skip
  guards.
- `internal/weaver/engine.go` — `disabledTargetSet`, `Engine.disabled`, `isTargetDisabled`,
  `seedDisabledTargets` wiring in `Start`.
- `internal/weaver/registry.go` — `ownerVertexID` accessor.
- `internal/weaver/reconciler_internal_test.go` — `TestSweep_ControlMarkerSurvives`.
- `cmd/weaver/main.go` — control service wiring.
- `cmd/lattice/root.go` — register `weaver` command group (11 groups).
- `docs/components/weaver.md` — Control plane (FR30) section + status updates.

## Questions for Winston (non-blocking — drafted around contract-compliant defaults)

1. **`internal/weaver/control` package split (AC #5, Dev Notes).** Confirmed via
   `boundary_test.go` reading: `go list -deps github.com/operatinggraph/lattice/internal/weaver` and
   `go list -f .Imports` on that SAME package do not cover a new sibling package
   `internal/weaver/control` unless `internal/weaver` imports it (which it must not — dependency
   runs `control` → `weaver`). This mirrors `internal/refractor/control` being a sibling of
   `internal/refractor/{pipeline,lens,...}`. No substrate-level micro wrapper needed. Confirm
   this reading is correct before dev starts (it is load-bearing for the whole control-plane
   package placement).

2. **`TargetSummary.State` for revoke-then-still-registered (AC #4, Task 3, Dev Notes).**
   Recommended: keep `State` a 2-value enum (`active`/`disabled`); `revoke` does not produce a
   durable "revoked" state — `list` after a revoke of a still-registered target may show
   `"active"` again once `reconcileConsumers` re-adds it. The `RevokeResult{Revoked:true}` ack
   is the operator's confirmation the cleanup ran. Alternative: add a transient `"revoked"`
   value that is honest only until the next reconcile (rejected as misleading-after-the-fact).
   Confirm (b)/2-value enum.

3. **AC #4's revoke-vs-reconcile bound — is this acceptable, or does revoke need a standing
   suppression?** As scoped, `revoke` is immediate-cleanup (durable delete + mark clear), and a
   still-Lens-registered target's consumer is re-`Add`ed on the next reconcile pass — full
   "this target stops mattering" requires the operator to ALSO retire the Lens
   (`lattice lens deactivate` / `TombstoneMetaVertex` on the `meta.weaverTarget` vertex, out of
   this story). Two alternatives if this bound is unacceptable: (a) `revoke` ALSO sets the
   `__control` disabled marker (so even if the consumer is re-added, dispatch stays skipped until
   `enable`) — cheap, additive, and arguably the RIGHT default (a `revoke`d target that gets
   re-added inert is safer than one that resumes acting); (b) `reconcileConsumers` consults a new
   "revoked targets" set and refuses to re-Add until the target is re-registered via a NEW
   `meta.weaverTarget` spec write (a CDC-observable "re-install" event) — more invasive, not
   recommended for this story's size. **Recommendation: fold (a) into AC #4/#6** — `Revoke`
   additionally calls `setDisabled(ctx, targetID, true)` AFTER the mark-prefix-delete (so the
   `__control` marker survives the prefix-delete — order matters, or re-write it after). This
   makes `revoke` a strict superset of `disable` + cleanup, which is both safer and simpler to
   reason about. Flag for explicit confirmation since it changes AC #6's literal "not an error if
   unregistered" Revoke into one that ALSO leaves a `__control` marker for a target that may not
   exist in `targetSource` at all (harmless — the marker is inert until/unless the targetId is
   ever re-registered, at which point it correctly starts that target disabled, which seems
   correct-by-construction for "I revoked this, don't act on it if it comes back").

4. **`handleFiredTimer` disabled-check (Dev Notes "Why lane-3 needs its own skip").** As scoped,
   a timer scheduled before `disable` still fires and submits `MarkExpired` via the shared
   `weaver-temporal` consumer (AC #2's guard only stops NEW schedule-publishes). This is
   documented as accepted/bounded. If "Weaver stops acting on it" should be read more strictly
   (zero ops submitted for a disabled target, including in-flight timer conversions), add a
   second `isDisabled` check in `handleFiredTimer` (Task 2 extension) — small, but touches the
   9.3 regression-net file. Recommend accepting the documented bound for this story; flag if you
   want the stricter behavior.

5. **Raw `*nats.Conn` accessor for `cmd/weaver` (Task 6).** Unresolved until the dev reads
   `cmd/refractor/main.go`'s actual `StartNATSListener(ctx, nc)` call site — if `cmd/refractor`
   already obtains a raw `*nats.Conn` independently of `substrate.Connect`, `cmd/weaver` mirrors
   that exactly (no substrate change). If `cmd/refractor` derives `nc` from its
   `*substrate.Conn`, a minimal `func (c *Conn) NATSConn() *nats.Conn` accessor on
   `internal/substrate.Conn` is the smallest fix — flag if a NEW exported substrate accessor
   feels like it deserves its own one-line CAR-style note in `cmd/weaver/CONTRACT-AMENDMENT-REQUEST.md`
   (existing file from 9.2/9.3) vs. just being self-evidently mechanical (no contract
   implications — it exposes a handle substrate already holds).

6. **`enable`/`resume` naming (epic AC says only list/disable/revoke).** This story adds `enable`
   as the recommended inverse of `disable` (Dev Notes / AC #2,3,6). Named `enable` (not `resume`)
   for symmetry with `disable` (both are CLI-facing target lifecycle verbs); `supervisor.Resume`
   is the underlying primitive name but the CLI/control verb is `enable`. Confirm naming, or
   prefer `resume` for exact `Pause`/`Resume` primitive-name symmetry instead.
