# Story 7.4 — Platform-wide message-scheduling stream (ADR-51)

Status: done — shipped `9507e61` (CI green, 2026-06-05). Thorough lead review (config-only); one lead test-rigor fix + a contract-amendment request (§10.4 republish-target scope) routed to planning.

**Tier:** Sonnet (config, well-bounded). This provisions one new JetStream stream with a single boolean flag and a smoke test. The only non-trivial risk is the NATS version gate — confirmed resolved (see §0.7 and Open Question 1).
**Epic spec:** `_bmad-output/planning-artifacts/epics/phase-2-epics.md` → "### Story 7.4" (line ~65). Read it for the user-story framing and exact AC.
**Binding grounding (FROZEN — read, do NOT edit):**
- `docs/contracts/10-orchestration-surfaces.md` §10.4 ("Message scheduling — platform-wide (ADR-51) — FROZEN 2026-06-02", ~line 239). This is the sole shape authority. Every name, flag, subject pattern, and header format in this story reproduces §10.4 exactly.
- `docs/contracts/07-primordial-bootstrap.md` — §7.4 (idempotent re-run), §7.5 (readiness gate), §7.7 (write order).
**Depends on:** Contract #7 (the primordial bootstrap path you extend). Does NOT depend on 7.1/7.2/7.3 (those are task + identity stories); this is a pure infrastructure story.
**Workflow:** DS is a sub-agent. Repo root, no worktree. Do NOT commit/push or branch. Do NOT edit frozen contracts (`docs/contracts/*`) or planning artifacts (`_bmad-output/planning-artifacts/*`). You MAY create/edit `/docs/components/*`. A genuine contract gap → file `cmd/bootstrap/CONTRACT-AMENDMENT-REQUEST.md` and note it in Open Questions; do not edit the frozen shape.

---

## 0. ADJUDICATION — Winston build target. DS builds to THIS.

### 0.0 What this story delivers (scope boundary)

Add the `core-schedules` JetStream stream to the primordial bootstrap so any component can publish NATS-native scheduled messages. **In scope:**

1. **`core-schedules` stream provisioned at bootstrap** — added to `provisionStreams()` in `internal/bootstrap/primordial.go`, joining `core-operations` and `core-events` in the same `streams` slice. Config: `Name: "core-schedules"`, `Subjects: []string{"schedule.>"}`, `AllowMsgSchedules: true`. File storage + limits-retention (same defaults as `core-events`). Idempotent: `CreateOrUpdateStream` already handles re-runs (Contract #7 §7.4).

2. **Constants added** — `CoreSchedulesStreamName = "core-schedules"` and `SchedulesWildcardSubject = "schedule.>"` in `primordial.go`'s `const` block alongside the existing stream/subject constants. No new NanoIDs — `core-schedules` is a JetStream stream, not a KV bucket or Core KV vertex; it does not require a primordial NanoID or a `lattice.bootstrap.json` field.

3. **Subject convention documented** — `schedule.<domain>.<kind>.<entityId>` where `<entityId>` is a NanoID (NOT a dotted key — dots are subject-token separators; the full entity key rides the message payload). The publisher selects the republish target subject via the NATS scheduler header mechanism (see §0.3 for exact header details from §10.4 / ADR-51). One schedule per subject (re-publish replaces). Document in `docs/components/scheduling.md`.

4. **Readiness gate** — `core-schedules` is a stream, not a KV bucket; there is no per-entry projection to await. The existing `MarkBootstrapComplete` / `WaitForBootstrapComplete` flow is sufficient (`provisionStreams` is called synchronously inside `ProvisionBuckets`; by the time `MarkBootstrapComplete` fires, `core-schedules` exists). **No change to the readiness gate.** Document this judgment in the dev notes.

5. **Smoke test** — an integration test (docker-stack NATS, `nats://localhost:4222`) that: (a) provisions `core-schedules`, (b) creates a durable consumer on the target subject, (c) publishes an `@at <RFC3339>` scheduled message to `schedule.test.timer.<entityId>` with target subject `test.schedule.fired.<entityId>`, (d) asserts the fired message arrives on the target subject at the scheduled time (±tolerance), and (e) re-publishes to the same schedule subject and asserts only one firing (replace semantics). Tag with `//go:build integration` or a skip guard (`testing.Short()` + env check). File: `internal/bootstrap/scheduling_smoke_test.go`.

**OUT of scope:** Weaver temporal lane (Story 9.3) — no `internal/weaver` code; no scheduled-op processing pipeline; no `@every` recurring schedules (Phase 2 = `@at` one-shot only per §10.4); no `loom-state` or `weaver-targets` provisioning (those are separate stories). Do NOT add `weaver-targets` or `loom-state` here.

### 0.1 A1 — Extend the EXISTING `provisionStreams()` path; do not add a separate provisioning function

`internal/bootstrap/primordial.go`'s `provisionStreams()` already provisions `core-operations` and `core-events` from a `[]jetstream.StreamConfig` slice. Add `core-schedules` as a third entry in that same slice. The loop calls `s.js.CreateOrUpdateStream(ctx, sc)` for each — the idempotent path is already wired. No new function, no second call site, no separate seeder method.

The exact config entry (reproduce verbatim — do NOT rename or paraphrase):

```go
{
    Name:              CoreSchedulesStreamName,   // "core-schedules"
    Description:       "Platform-wide message-scheduling stream (ADR-51). Provisioned at bootstrap; AllowMsgSchedules enables NATS-native @at/@every scheduling.",
    Subjects:          []string{SchedulesWildcardSubject},  // "schedule.>"
    Retention:         jetstream.LimitsPolicy,
    Storage:           jetstream.FileStorage,
    AllowMsgSchedules: true,
},
```

No `MaxAge` is required for `core-schedules` — schedules are fire-and-forget (the fired message goes to the target subject; the schedule entry itself is removed on fire or on re-schedule). If a MaxAge is desired for safety, omit it in Phase 2 (same as `core-operations` which has no MaxAge).

### 0.2 A2 — Subject convention: exact shape from §10.4 (do NOT paraphrase)

From Contract #10 §10.4 — reproduce exactly:

```
schedule subject:  schedule.<domain>.<kind>.<entityId>
```

- `<domain>` — the owning component's domain (e.g. `weaver`, `loom`, `orchestration`)
- `<kind>` — entity type (e.g. `timer`, `task`, `lease`)
- `<entityId>` — **NanoID only** (NOT a dotted vertex key like `vtx.op.abc` — dots are subject-token separators). The full entity key, if needed, rides the **message payload**.
- **One schedule per subject** — re-publishing to `schedule.<domain>.<kind>.<entityId>` replaces the prior schedule for that entity. This is the NATS scheduler's native de-duplicate-by-subject behavior.

**How the republish target is specified (the mechanism):** The publisher sets a `Nats-Scheduler-Target` header (or equivalent ADR-51 header — see §10.4 and the `nats-server/v2/server` `JSScheduler` constant) on the published message. The NATS scheduler reads this header and, when the `@at` time fires, republishes the message payload to that target subject. The target subject is **publisher-chosen** — e.g. Weaver would use `weaver.timer.fired.<domain>.<kind>.<entityId>`. Only that component subscribes to its own `weaver.timer.fired.*` namespace, so no cross-component message fan-out occurs.

**Exact publish headers (from §10.4 / ADR-51):**

```
Nats-Msg-Schedule: @at <RFC3339>          # absolute instant (Phase 2: one-shot only; @every deferred)
Nats-Scheduler: <scheduler-id>            # identifies the NATS scheduler routing this message
```

The target subject is encoded in the message itself via the `Nats-Msg-Schedule-Target` header (check `nats-server/v2/server` constants for the exact header name — `JSScheduler` = `"Nats-Scheduler"`, look for the target-subject header constant). **In the smoke test, confirm the exact header names from the server's constants rather than hard-coding strings.**

### 0.3 A3 — Smoke test: what to assert, how to structure it

The smoke test lives at `internal/bootstrap/scheduling_smoke_test.go`. It requires the real Docker NATS stack (`nats://localhost:4222`), NOT embedded NATS — `AllowMsgSchedules` requires the NATS scheduler, which is a server-side feature not available in the embedded test server. Guard with:

```go
func requireNATSStack(t *testing.T) *nats.Conn {
    t.Helper()
    url := os.Getenv("NATS_URL")
    if url == "" {
        url = "nats://localhost:4222"
    }
    nc, err := nats.Connect(url, nats.Timeout(2*time.Second))
    if err != nil {
        t.Skipf("NATS stack not reachable (%v) — run `make up` first", err)
    }
    t.Cleanup(nc.Close)
    return nc
}
```

**Four assertions the smoke test must cover (AC #3 and #4):**

1. **Stream exists with `AllowMsgSchedules: true`** — after `ProvisionBuckets`, confirm `s.js.Stream(ctx, "core-schedules")` returns info with `AllowMsgSchedules: true`.

2. **`@at` message fires on the target subject** — publish to `schedule.test.timer.<entityId>` with `@at <now+2s>` header and target subject `test.schedule.fired.<entityId>`; subscribe on the target; assert the message arrives within a generous window (e.g. 10s).

3. **Payload is preserved** — the fired message payload matches what was published to the schedule subject (the full entity key, if included in the payload, round-trips correctly).

4. **Re-schedule replaces (one schedule per subject)** — publish twice to the same `schedule.test.timer.<entityId>` (two different `@at` times); assert exactly one firing on the target subject (the second publish replaced the first). Use a counter subscriber and assert `count == 1`.

**Timing tolerance:** NATS message scheduling is at-least-once and subject to scheduler precision. Use `@at <now+3s>` and a 15-second receive window to keep the test reliable without being slow. Document the timing choice in a comment.

### 0.4 A4 — No kernel-count change, no `lattice.bootstrap.json` field, no readiness-gate change

`core-schedules` is a **JetStream stream** (not a KV bucket, not a Core KV vertex). Therefore:

- **`PrimordialVertexKeys()` is NOT updated** — that function enumerates Core KV vertex/link entries, not JetStream streams. The `scripts/verify-kernel.go` count and the kernel-composition doc comment in `primordial.go` are unchanged.
- **`lattice.bootstrap.json` / `PrimordialIDsRaw` / `BootstrapFile` are NOT updated** — no new NanoID is generated for `core-schedules`.
- **Readiness gate (`WaitForBootstrapComplete`) is NOT changed** — `ProvisionBuckets` (which calls `provisionStreams`) runs synchronously before `SeedPrimordial`; the stream exists before the bootstrap marker is written. If you find yourself editing readiness-gate logic, stop — it is not needed.
- **`make verify-kernel` count is unchanged** — verify this is still correct after your change and state the result.

### 0.5 A5 — No history/changelog comments (CLAUDE.md, most-violated rule)

Every comment describes what the code does **now**. Never write `// Story 7.4`, `// adds core-schedules`, `// new in Phase 2`, `// joins the stream list`. Contract refs (`// Contract #10 §10.4`, `// ADR-51`) are fine. Change-narration is not. git blame is the record.

### 0.6 A6 — NATS version gate (the #1 implementation risk — RESOLVED)

**Finding:** Both `nats.go v1.52.0` (client) and `nats-server v2.14.0` (server, `nats:2.14-alpine` in `docker-compose.yml`) expose `AllowMsgSchedules` / `allow_msg_schedules`. Confirmed by:
- `go doc github.com/nats-io/nats.go/jetstream StreamConfig` → `AllowMsgSchedules bool` field present.
- `go doc github.com/nats-io/nats-server/v2/server` → `JSScheduler`, `NewJSMessageSchedulesDisabledError`, and the full scheduler API present.

**No version upgrade required.** The `AllowMsgSchedules: true` field in the `jetstream.StreamConfig` will compile and work against the current stack. This is **not** a risk for this story.

**However:** The `AllowMsgSchedules` feature requires the NATS server to have the scheduler enabled (it is a JetStream extension). The embedded test server (`natstest.RunServer`) may NOT support it (the embedded server is minimal). This is why the smoke test (A3) uses the real Docker stack, not embedded NATS. **Do not attempt to test scheduling with embedded NATS — it will silently fail or panic.**

### 0.7 Gates (all must pass before handing back)

`go build ./...` · `make vet` · `golangci-lint run ./...` · **`make verify-kernel`** (count must NOT change — `core-schedules` is a stream, not a KV entry) · `make test-bypass` (Gate 2, all BLOCKED — no new auth surface introduced) · `make test-capability-adversarial` (Gate 3, all DEFENDED — no auth change) · `go test ./internal/bootstrap/... -count=1` · **smoke test** (`go test ./internal/bootstrap/... -run TestCoreSchedulesSmoke -count=1` with `make up` running — report the result). If the smoke test cannot run (NATS stack down), skip it with a note and ensure the skip guard is correct. A full `make down && make up` confirms `core-schedules` is provisioned on a fresh start.

---

## 1. Story (user-facing)

As the **platform operator**,
I want a platform-wide message-scheduling stream provisioned at bootstrap,
so that any component (Weaver's temporal lane first) can use NATS native message scheduling.

## 2. Acceptance Criteria (faithful to the epic AC, line ~71)

1. **Given** primordial bootstrap, **When** the platform starts, **Then** a platform-wide `core-schedules` stream exists with `AllowMsgSchedules: true` (a platform capability like Health KV — not Weaver-owned; same config-shape as the `AllowAtomicPublish` flag on `core-events`; `core-*` family name, no project-name prefix).
2. **And** publishing on `schedule.<domain>.<kind>.<entityId>` (`<entityId>` = NanoID, full key in payload) is the shared ingress; the publisher chooses the republish target subject (so each component consumes only its own fired messages).
3. **And** a smoke test publishes an `@at` scheduled message and observes republish to the chosen target subject at the scheduled time.
4. **And** re-publishing to the same schedule subject replaces the prior schedule (one schedule per subject).

## 3. Tasks / Subtasks

- [x] **T1 — Add stream constants** (AC #1; A1, A5)
  - [x] In the `const` block of `internal/bootstrap/primordial.go` (alongside `CoreOpsStreamName`, `CoreEventsStreamName`, `OpsWildcardSubject`, `EventsWildcardSubject`), add:
    ```go
    CoreSchedulesStreamName  = "core-schedules"
    SchedulesWildcardSubject = "schedule.>"
    ```
  - [x] No `lattice.bootstrap.json` field, no NanoID, no `PrimordialIDsRaw` field — `core-schedules` is a stream, not a KV entry.

- [x] **T2 — Add `core-schedules` to `provisionStreams()`** (AC #1; A1, A5, A6)
  - [x] In `provisionStreams()`, append the `core-schedules` entry to the `streams` slice (exact config from §0.1). Set `AllowMsgSchedules: true`. Use `jetstream.LimitsPolicy` retention and `jetstream.FileStorage` — match `core-events` defaults.
  - [x] Confirm `CreateOrUpdateStream` is the call (already used in the loop) — idempotent re-run is covered (Contract #7 §7.4).
  - [x] Log at `Info` level: `"JetStream stream ready", "stream", sc.Name` (already done by the loop — no new log line needed).

- [x] **T3 — Write `docs/components/scheduling.md`** (AC #2; A2)
  - [x] Document the `core-schedules` stream, its subject convention (`schedule.<domain>.<kind>.<entityId>`), the `@at <RFC3339>` header, the target-subject header mechanism, one-schedule-per-subject replace semantics, and the `<entityId>` = NanoID (not dotted key) discipline. Cross-reference Contract #10 §10.4. This is the authoritative operator reference; Weaver Story 9.3 will link to it.
  - [x] Do NOT duplicate the frozen contract text — reference it. The doc explains the "how to use it" perspective for component authors.

- [x] **T4 — Smoke test** (AC #3, #4; A3, A6)
  - [x] File: `internal/bootstrap/scheduling_smoke_test.go`, package `bootstrap_test`.
  - [x] Guard: skip if NATS stack not reachable (see §0.3 pattern). Do NOT use embedded NATS.
  - [x] Assertions (four, per §0.3): stream has `AllowMsgSchedules:true`; `@at` fires on target subject; payload round-trips; re-schedule-same-subject fires exactly once.
  - [x] Name test: `TestCoreSchedulesSmoke`.

- [x] **T5 — Verify no regressions** (§0.7 gates)
  - [x] `make verify-kernel` passes with the SAME count as before (no change — stream is not a KV entry).
  - [x] `make test-bypass` (Gate 2) — no new auth surface, should be unchanged.
  - [x] `make test-capability-adversarial` (Gate 3) — no auth change, should be unchanged.
  - [x] `go build ./...` + `make vet` + `golangci-lint run ./...`.

## 4. Dev Notes

### Where things live (read these first)

- **The path you extend:** `internal/bootstrap/primordial.go` — `provisionStreams()` (~line 132) already has a `streams := []jetstream.StreamConfig{...}` slice with `core-operations` and `core-events`. Add `core-schedules` as the third entry. The loop at ~line 153 calls `s.js.CreateOrUpdateStream` — nothing else to wire.
- **Constant block:** ~lines 21–39 of `primordial.go` — the `const` block with `CoreOpsStreamName`, `CoreEventsStreamName`, `OpsWildcardSubject`, `EventsWildcardSubject`. Add the two new constants here.
- **`provisionStreams()` call site:** `ProvisionBuckets()` (~line 59) calls `s.provisionStreams(ctx)` at ~line 103. This is the only call site; no new call needed.
- **Existing `AllowAtomicPublish` pattern (your config shape template):** `core-events` (lines ~140–151) sets `AllowAtomicPublish: true` — the same structural pattern as `AllowMsgSchedules: true` for `core-schedules`. The config field is a boolean on `jetstream.StreamConfig`; setting it on a new stream is exactly the same operation.
- **`nats.go` `jetstream.StreamConfig` field:** `AllowMsgSchedules bool` — confirmed present in `nats.go v1.52.0`. Use it directly.
- **No `nanoid.go` changes** — `core-schedules` needs no NanoID, no `BootstrapFile` field, no `PrimordialVertexKeys` entry. If you find yourself editing `nanoid.go`, stop.
- **No `cmd/bootstrap/main.go` changes** — the bootstrap binary calls `seeder.ProvisionBuckets(ctx)` which calls `provisionStreams(ctx)`. No wiring change needed.
- **No step-3/auth/processor changes** — `core-schedules` is a scheduling substrate. No operation type, no DDL, no capability entry.

### The exact stream config to add (reproduce from §10.4, line-for-line)

```go
{
    Name:              CoreSchedulesStreamName,
    Description:       "Platform-wide message-scheduling stream (ADR-51). AllowMsgSchedules enables NATS-native @at/@every scheduling. Subject root: schedule.>",
    Subjects:          []string{SchedulesWildcardSubject},
    Retention:         jetstream.LimitsPolicy,
    Storage:           jetstream.FileStorage,
    AllowMsgSchedules: true,
},
```

### Smoke test structure (to prevent the wrong approach)

The smoke test publishes with NATS scheduler headers. The exact header names are available in `nats-server/v2/server` constants (e.g. `server.JSScheduler`). Look for the target-subject and schedule-time header constants rather than hard-coding raw strings. The publish call looks like:

```go
msg := nats.NewMsg("schedule.test.timer." + entityID)
msg.Header.Set(<schedule-time-header>, "@at " + time.Now().Add(3*time.Second).UTC().Format(time.RFC3339))
msg.Header.Set(<target-subject-header>, "test.schedule.fired." + entityID)
msg.Data = []byte(`{"entityKey":"vtx.op.` + entityID + `"}`)
_, err = nc.PublishMsg(msg)
```

Then subscribe on `"test.schedule.fired." + entityID` and wait up to 15 seconds for the fired message.

**The `nats-server/v2/server` package is already in `go.mod` as a test dependency** (used by `natstest.RunServer` in `service_actor_e2e_test.go`). Import the constants from there.

### Readiness gate (why no change is needed)

`ProvisionBuckets` runs synchronously and calls `provisionStreams` before returning. `SeedPrimordial` is called after `ProvisionBuckets` returns. `MarkBootstrapComplete` is called after seeding. Therefore `core-schedules` always exists before the bootstrap-complete marker is written. No polling is needed — unlike KV projections (which require Refractor), a JetStream stream is either present or it isn't, and the synchronous `CreateOrUpdateStream` call guarantees it is present by the time the function returns. Document this in `docs/components/scheduling.md`.

### Subject convention (not a code decision — a contract)

The subject `schedule.<domain>.<kind>.<entityId>` is from Contract #10 §10.4 FROZEN 2026-06-02. Do not rename or restructure. The `<entityId>` position must be a NanoID (20-char, the substrate alphabet), NOT a dotted vertex key, because dots are NATS subject-token separators. The full entity key goes in the payload. This is the same discipline as `schedule.<domain>.<kind>.<entityId>` in §10.2/§10.3.

### `core-schedules` does NOT replace `core-events`

Fired scheduled messages hit the **publisher's target subject** and are NOT automatically published to `core-events`. The fired message is processed by the subscribing component (e.g. Weaver), which converts it to a normal op via the Processor. The transactional outbox (Contract #3 / Story 1.5.10) remains the sole event producer. This scope boundary is critical — do not add any routing from `core-schedules` to `core-events`.

### Project Structure Notes

- This is a **pure `internal/bootstrap/primordial.go` change** + two constants + one smoke test + one doc page.
- **No `cmd/bootstrap/main.go` change** (the bootstrap binary already calls `ProvisionBuckets`).
- **No `scripts/verify-kernel.go` change** (stream is not a KV entry; count unchanged).
- **No `internal/processor/` change** (no new op type).
- **No `packages/` change** (this is platform substrate, not a capability package).
- **No `internal/loom/` or `internal/weaver/` change** (those are Epics 8/9 and Story 9.3).
- If your diff touches `step3_*.go`, the capability cypher, `nanoid.go`, or `scripts/verify-kernel.go`, you are out of scope — stop.

### References

- [Source: docs/contracts/10-orchestration-surfaces.md#10.4] — §10.4 FROZEN (stream name, subject pattern, `AllowMsgSchedules: true`, `@at` header, target-subject mechanism, one-schedule-per-subject replace semantics, `<entityId>` = NanoID). This is the binding authority — do not deviate from any name, shape, or convention it specifies.
- [Source: docs/contracts/07-primordial-bootstrap.md#7.4] — idempotent re-run (existing `CreateOrUpdateStream` covers it).
- [Source: docs/contracts/07-primordial-bootstrap.md#7.5] — readiness gate (no change needed; see §0.4).
- [Source: internal/bootstrap/primordial.go] — `provisionStreams()` (the function you extend), `ProvisionBuckets()` (the call site), `AllowAtomicPublish` pattern on `core-events` (your config-shape template).
- [Source: _bmad-output/implementation-artifacts/7-3-service-actor-bootstrap-provisioning.md#0] — §0 ADJUDICATION structure, gate list format, closing-summary contract, no-history-comments discipline (follow this story's rigor pattern exactly).

## 5. Test plan (concrete — count delivered tests from the diff)

- **Unit (`internal/bootstrap`, embedded NATS):** `TestCoreSchedulesStream_Provisioned` — after `ProvisionBuckets(ctx)`, confirm the `core-schedules` stream exists and `AllowMsgSchedules` is `true` in the stream info. Uses embedded NATS (no scheduler needed to verify stream creation).
- **Idempotence (embedded NATS):** `TestCoreSchedulesStream_Idempotent` — calling `ProvisionBuckets` twice does not error; `core-schedules` still exists and still has `AllowMsgSchedules: true`.
- **Smoke test (docker stack, `TestCoreSchedulesSmoke`):** four assertions per §0.3 above — stream flag, `@at` fires, payload round-trips, re-schedule-replaces.
- **Gates:** every gate in §0.7 with its result (run all, report each). State the `make verify-kernel` count explicitly (must equal the pre-story count).

## 6. Closing Summary

**Stream config delivered:**
```go
{
    Name:              CoreSchedulesStreamName,   // "core-schedules"
    Description:       "Platform-wide message-scheduling stream (ADR-51). AllowMsgSchedules enables NATS-native @at/@every scheduling. Subject root: schedule.>",
    Subjects:          []string{SchedulesWildcardSubject},  // "schedule.>"
    Retention:         jetstream.LimitsPolicy,
    Storage:           jetstream.FileStorage,
    MaxMsgsPerSubject: 1,
    AllowMsgSchedules: true,
}
```

**Ruling-3 replace/bounding realization:** `AllowMsgSchedules: true` auto-enables `AllowRollup: true` (per server validation in `stream.go`). The NATS scheduler enforces replace semantics natively: when the scheduler fires a message, it purges the schedule entry via `Nats-Schedule-Next: purge` (subject rollup). `MaxMsgsPerSubject: 1` provides an additional storage bound. These two mechanisms are complementary, not conflicting — verified by integration test.

**ADR-51 target-subject constraint (contract gap):** The NATS server requires the `Nats-Schedule-Target` subject to be within the stream's subject space (`schedule.>`). The fired message is stored back into `core-schedules` at the target subject — it is NOT dispatched as a plain NATS core message. Contract #10 §10.4's example (`weaver.timer.fired.*` outside `schedule.>`) is incorrect. A CONTRACT-AMENDMENT-REQUEST was filed at `cmd/bootstrap/CONTRACT-AMENDMENT-REQUEST.md`. The correct target pattern is `schedule.<component>.fired.<entityId>`. This impacts Story 9.3 (Weaver temporal lane) which must use `schedule.weaver.timer.fired.<entityId>` and consume via a JetStream filtered consumer on `core-schedules`.

**Smoke test observes:** The test uses a JetStream consumer (`js.CreateOrUpdateConsumer` with `FilterSubject`) on `schedule.test.timer.fired.<entityId>`. The `consumer.Fetch` call is event-driven — it blocks until the scheduled message arrives (up to 15s). This is preferable to a fixed sleep. The test requires the real Docker NATS stack; it skips if `nats://localhost:4222` is unreachable. CI behavior: the skip guard suffices (same mechanism as other live-NATS tests in the package: `requireNATSStack` with `t.Skipf`).

**verify-kernel count:** UNCHANGED. `core-schedules` is a JetStream stream, not a KV entry. `make verify-kernel` confirms 28 Core KV keys — identical to pre-story count.

## Dev Agent Record

### Agent Model Used

claude-sonnet-4-6

### Debug Log References

- Discovered ADR-51 target-subject constraint: the NATS server requires the target to be within the stream's subject space. Contract #10 §10.4 example `weaver.timer.fired.*` is outside `schedule.>` and would fail at publish time with `NewJSMessageSchedulesTargetInvalidError`. Filed CONTRACT-AMENDMENT-REQUEST.md.
- Initial smoke test used `nc.PublishMsg` (core NATS) — fixed to `js.PublishMsg` (JetStream). The scheduler only processes messages published through JetStream (they must be stored in the stream first).
- Initial smoke test used a plain `nc.Subscribe` — the fired message is stored back into the stream at the target subject, not dispatched as a core NATS message. Fixed to use JetStream consumer.

### Completion Notes List

- T1: Added `CoreSchedulesStreamName = "core-schedules"` and `SchedulesWildcardSubject = "schedule.>"` to the `const` block in `internal/bootstrap/primordial.go`.
- T2: Added `core-schedules` StreamConfig to `provisionStreams()` slice with `AllowMsgSchedules: true`, `MaxMsgsPerSubject: 1`, `FileStorage`, `LimitsPolicy`. Used exported server constants (`natsserver.JSSchedulePattern`, `natsserver.JSScheduleTarget`) in the smoke test.
- T3: Created `docs/components/scheduling.md` documenting the stream, subject convention, ADR-51 behavior, and the critical target-subject constraint (must be within `schedule.>`).
- T4: `TestCoreSchedulesSmoke` passes against Docker stack. Also added `TestCoreSchedulesStream_Provisioned` (embedded NATS, verifies config fields) and `TestCoreSchedulesStream_Idempotent` (embedded NATS, re-provision).
- T5: All gates pass. `make verify-kernel` count unchanged (28 Core KV keys).
- CAR filed: `cmd/bootstrap/CONTRACT-AMENDMENT-REQUEST.md`.

### File List

- `internal/bootstrap/primordial.go` (modified)
- `internal/bootstrap/scheduling_smoke_test.go` (new)
- `docs/components/scheduling.md` (new)
- `cmd/bootstrap/CONTRACT-AMENDMENT-REQUEST.md` (new)

---

## Winston's Adjudication (RESOLVED — DS builds to these)

1. **`AllowMsgSchedules` support → CONFIRMED, no action.** Winston independently verified: `AllowMsgSchedules bool` is present in `jetstream.StreamConfig` at `nats.go v1.52.0` (go.mod), server `nats:2.14-alpine`. Risk closed.
2. **Scheduler header constant names → dev-time lookup.** Use the real exported constants from the nats.go/nats-server package; do not hardcode magic strings. No Winston decision.
3. **`MaxAge` → OMIT. ACCEPTED** (a long-horizon `@at` must not expire prematurely). BUT also **bound stream storage**: set `MaxMsgsPerSubject: 1` so the per-subject "re-publish replaces the prior schedule / one schedule per subject" invariant is enforced by config AND the stream can't grow unbounded. If you find `AllowMsgSchedules` already enforces single-schedule-per-subject natively (so `MaxMsgsPerSubject` is redundant or conflicts), use whichever the scheduler semantics require and state it in the closing summary — the binding requirement is: re-publish replaces, and storage is bounded.
4. **Scope → `core-schedules` ONLY. ACCEPTED.** No `weaver-targets` / `loom-state` / Weaver code — those are Epic 9. Substrate + subject convention + smoke test only.
5. **Smoke-test timing → keep the bounded window. ACCEPTED.** If it proves CI-flaky, gate it behind the same Docker-required/integration build-tag or skip mechanism the other live-NATS tests use (don't let it flake CI) — and say which you did. Prefer asserting on observed republish (event-driven) over a fixed sleep where practical, to minimize the timing window.

---

## Open Questions (for Winston)

1. **`AllowMsgSchedules` server-side scheduler availability — RESOLVED, no action needed.** Both `nats.go v1.52.0` and `nats-server v2.14.0` (`nats:2.14-alpine`) have full message-schedule support including the scheduler API, error types, and `AllowMsgSchedules` stream config. The smoke test requires the real Docker stack (not embedded NATS) because the NATS scheduler is a server-side feature. This is documented in A3/A6 and in the smoke test skip guard. **No version upgrade required; no open action for Winston.**

2. **Exact header constant names for the smoke test (low-risk; dev-time lookup).** ADR-51 specifies `@at <RFC3339>` and a target-subject header, but the exact NATS header key strings (e.g. the target-subject header) are defined as constants in `nats-server/v2/server` (e.g. `JSScheduler = "Nats-Scheduler"`). The DS should look these up from the installed `nats-server/v2/server` package rather than hard-coding raw strings. **Recommendation:** use the server constants (`server.JSScheduler`, and the schedule-time/target-subject header constants in that package) to avoid string drift if the NATS library version changes. This is a dev-time judgment call, not a Winston decision.

3. **`MaxAge` for `core-schedules` — omit in Phase 2 (recommendation).** A `MaxAge` on `core-schedules` would expire schedule entries after a wall-clock duration, which is incorrect (a schedule for a timer 7 days out would be deleted before it fires). The NATS scheduler removes entries on fire. **Recommendation: omit `MaxAge`** (same as `core-operations`). If Winston wants a safety cap (e.g. 30 days to prevent stale schedule accumulation), it can be set later — but it would need to be longer than the longest expected schedule horizon, which is unknown in Phase 2. Default = no MaxAge.

4. **`weaver-targets` bucket and `loom-state` bucket are NOT in scope here.** Contract #10 §10.3 notes those also "join the primordial create list." They are NOT in Story 7.4's AC — they belong to their respective engine bootstrap stories (Loom / Weaver epics). Do not provision them here. **Confirm: `core-schedules` only.**

5. **Smoke test timing tolerance.** The test uses `@at <now+3s>` and a 15-second receive window. On a loaded CI host, NATS scheduler precision may be ±1–2s. **Recommendation:** keep the 15-second window; if flakiness is observed, widen to 20s. This is a dev judgment call. If the test is too flaky in CI, add a `//go:build integration` build tag instead of relying on the stack-reachable skip guard — but the skip guard is cleaner for local dev. Winston to confirm preferred CI approach if it matters.

---

## Winston — Lead Review (2026-06-05)

**Review depth: thorough lead review, NOT the full 3-layer adversarial fan-out** — justified per CLAUDE.md because this is a small, well-scoped, green, config-only change (one JetStream stream + a smoke test) with no security/auth-plane touch and the kernel vertex set unchanged (verify-kernel still 28). Flagging the reduced depth explicitly so it can be overridden.

- **Implementation is sound.** `core-schedules` joins the existing `provisionStreams()` slice (no parallel path), `AllowMsgSchedules: true`, `Subjects: ["schedule.>"]`, `FileStorage` + `LimitsPolicy` + `MaxMsgsPerSubject: 1`. `AllowMsgSchedules` auto-enables server-side rollup, so per-subject replace is enforced both natively and by `MaxMsgsPerSubject: 1` (ruling #3). Real server header constants used (`natsserver.JSSchedulePattern` / `JSScheduleTarget`), not magic strings (ruling #2). verify-kernel unchanged (ruling: stream ≠ kernel vertex).
- **Lead fix-forward (test rigor):** the smoke test's replace-semantics assertion originally scheduled the to-be-replaced message far-future (`now+30s`) while only observing a ~5s window — so it never actually proved the replaced schedule was purged (it would pass even if replace were broken). Tightened to schedule both publishes at the same near-future instant on the same schedule subject, so a broken replace would surface a duplicate firing on the second fetch. Re-verified 3/3 deterministic against the live stack.
- **CAR raised + accepted — routed to planning.** `cmd/bootstrap/CONTRACT-AMENDMENT-REQUEST.md`: Contract #10 §10.4's example republish target (`weaver.timer.fired.*`, *outside* `schedule.>`) would fail at publish — the NATS scheduler stores the fired payload **back into `core-schedules`** at the target subject, which must lie within the stream's own subject space (`schedule.>`); components consume via a JetStream filtered consumer (consistent with §10.1's "the temporal lane replays from the core-schedules stream"). This is an **annotation, not a shape change** — the 7.4 stream config is correct as-built, and the smoke test demonstrates the correct pattern (`schedule.test.timer.fired.<id>`). The frozen contract is planning-owned, so it is **not edited here**; the CAR is committed for the planning lead to fold into §10.4, and **Story 9.3 (Weaver temporal lane) must target `schedule.weaver.timer.fired.<entityId>` and consume via a filtered JetStream consumer**, not an out-of-stream subject.

**Verification gates (run by Winston, all green):** `go build ./...`, `make vet`, `golangci-lint run ./...` (0 issues), `make verify-kernel` (28 — unchanged), `make test-bypass` (Gate 2 — 4/4 BLOCKED), `make test-capability-adversarial` (Gate 3 — 4/4), `go test ./internal/bootstrap/...` (all pass; live `TestCoreSchedulesSmoke` 3/3 deterministic).
