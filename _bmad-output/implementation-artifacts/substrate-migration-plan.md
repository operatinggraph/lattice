# Refractor substrate inner-package migration — scoping note

**Status:** scoping-note (grounded in a code survey; for Andrew's review → then a dedicated build session with the normal review loop). NOT a finished design — one real architectural fork needs Andrew's call (below).
**Backlog item:** Refractor substrate inner-package migration (one of Andrew's picked "Now" items). See `backlog.md`.
**Why:** ~21 production files in `internal/refractor/` still hold **raw** `nats.go` / `jetstream` handles; the substrate boundary is only enforced at `cmd/refractor` + a few files. Tightening it is architectural hygiene and is what makes an embeddable/local node (the Loupe → Edge path) tractable.

## The surface (grounded survey)
~21 production files, 8 operation categories. **Substrate already covers ~60%** of the raw calls — all the KV CRUD (`KVGet/Put/Create/Update/Delete/DeleteRevision/ListKeys` + TTL variants), `Decision`-based message disposition, and `Publish`. The remaining raw usage falls into:

| Category | Raw usage | Files |
|---|---|---|
| KV purge (rebuild truncate) | `kv.Purge` | `adapter/natskv.go` |
| KV status probe (health) | `kv.Status` | `adapter/natskv.go` |
| Consumer lag | `cons.Info().NumPending` | `consumer/bootstrap.go`, `health/lag_poller.go` |
| Consumer create/delete | `js.CreateOrUpdateConsumer`, `js.DeleteConsumer` (per-rule, with `DeliverLastPerSubject` + `DeliverGroup`) | `consumer/manager.go`, `consumer/bootstrap.go` |
| Stream provisioning | `js.CreateOrUpdateStream` (DLQ + audit) | `failure/dlq.go`, `health/audit_writer.go` |
| micro.Service | `micro.AddService` | `control/service.go` |
| The guarded-write CAS loop | `kv.Get/Create/Update` inside the Contract #6 §6.2 monotonic guard | `adapter/natskv.go` |

## The one architectural fork (needs Andrew's call)
The per-rule consumers in `consumer/manager.go` use `DeliverLastPerSubject` + `DeliverGroup`. Two end-states:

- **(A) Minimal helpers.** Add a thin `CreateDurableConsumer` to substrate. **Risk:** to express `DeliverLastPerSubject`/`DeliverGroup` it would take a `jetstream.ConsumerConfig` — which **leaks `jetstream` onto substrate's exported surface**, the exact thing the `ConsumerSupervisor` was built to avoid (it uses a substrate-owned `DeliverPolicy` enum + `DeliverGroup` field, no jetstream types). So this is the fast path but it dents the boundary doctrine.
- **(B) Migrate Refractor's consumers onto the existing `ConsumerSupervisor`.** The supervisor **already supports** `DeliverLastPerSubject` + `DeliverGroup` (substrate-owned), and Loom + Weaver already run on it. This is the *clean* end-state (no jetstream leak, unified consumer management across all three engines), but it's a deeper refactor of `consumer/manager.go` + `consumer/bootstrap.go` (and possibly the pipeline loop) than point-call substitution.

**Recommendation:** (B) for the consumer cluster — it's the architecturally-correct end-state and reuses proven machinery — accepting it's a larger Stage-3. (A) only if we want a fast partial win first. **Andrew decides.**

## New substrate helpers (the non-controversial ones)
Minimal, no jetstream leak, additive:
- `KVPurge(ctx, bucket, key) error` — wraps `kv.Purge` (rebuild truncate).
- `KVStatusProbe(ctx, bucket) error` — wraps `kv.Status` (adapter health probe).
- `ConsumerLag(ctx, stream, durable) (uint64, error)` — wraps `cons.Info().NumPending` (lag metric; substrate already has `PendingForConsumer` on the supervisor — confirm whether that subsumes this).
- `DeleteDurableConsumer(ctx, stream, durable) error` — idempotent (ErrConsumerNotFound → nil). (Subsumed by the supervisor's `Remove` if we go path B.)

## What stays raw (by design — outside the substrate boundary)
- **`control/service.go` micro.Service** — the control-plane facade is a NATS-Services responder, not data-plane I/O (same as Loom/Weaver control planes; substrate exposes no micro wrapper by doctrine).
- **Stream provisioning** (`failure/dlq.go`, `health/audit_writer.go`) — *better*, these one-shot `CreateOrUpdateStream` calls at rule startup arguably belong in **bootstrap provisioning** (like the other primordial streams), not created ad-hoc. A small side-cleanup to consider: move DLQ/audit stream creation to bootstrap, removing the raw call entirely rather than wrapping it.
- **The guarded-write CAS loop** in `natskv.go` — the loop *logic* (Contract #6 §6.2 monotonic guard) stays in the adapter; only its point `kv.Get/Create/Update` calls swap to substrate helpers.

## Staging (low-risk → high-risk)
1. **Stage 1 — KV CRUD substitution** (zero new helpers, zero behavior change): `health/reporter.go`, `adjacency/{builder,store}.go`, `control/service.go` validation path, the rule-engine readers. Pure call substitution to existing `substrate.KV*`.
2. **Stage 2 — KVPurge + KVStatusProbe** (2 new helpers): `adapter/natskv.go` Truncate + Probe.
3. **Stage 3 — the consumer cluster** (the fork above): `consumer/{manager,bootstrap}.go`, `health/lag_poller.go`. Path A (helpers) or Path B (ConsumerSupervisor). **Biggest risk** (`DeliverLastPerSubject`, `DeliverGroup`, lag-at-delivery, ack semantics, rebuild/reset).
4. **Stage 4 — guarded-write call substitution** (no new helpers): `natksv.go` `guardedWrite` — swap point calls, keep the loop. Verify Contract #6 §6.2 invariants under concurrent/replay.
5. **Stage 5 (optional/side-cleanup)** — move DLQ/audit stream creation to bootstrap provisioning.

Each stage is independently shippable + reviewable; Stages 1–2 are near-zero-risk quick wins, Stage 3 is the substantive one.

## Verification (per stage)
`go build ./...` · `make vet` · `golangci-lint run ./...` · the full refractor test suite (`go test ./internal/refractor/...`) · `make verify-package-*` if any DDL/stream provisioning shifts · and the end-state assertion: `grep -rn 'nats-io/nats.go\|jetstream' internal/refractor/ --include=*.go` returns **only** the by-design exceptions (`control/service.go` micro). Contract #6 §6.2 + Contract #1 key shapes must be byte-identical throughout (pure refactor, no contract change).

## Open questions for Andrew
1. **The fork:** Path A (minimal jetstream-leaking helper, fast) vs. Path B (migrate Refractor consumers onto `ConsumerSupervisor`, clean). Recommend B.
2. Does the supervisor's existing `PendingForConsumer` subsume the proposed `ConsumerLag` (then no new lag helper)?
3. Stage-5 side-cleanup: move DLQ/audit stream creation into bootstrap provisioning (removes 2 raw call sites) — in or out of this migration's scope?
4. Is the pipeline consumer loop (`pipeline/pipeline.go`) in scope, or left raw for a later pass? (It has its own loop, not on `RunDurableConsumer`.)
