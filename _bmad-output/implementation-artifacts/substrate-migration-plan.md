# Refractor substrate inner-package migration — build brief

**Status:** building (Andrew ruled **Path B**; executing in worktree `refractor-substrate-migration`).
**Backlog item:** Refractor substrate inner-package migration. See `backlog.md`.
**Mandate (Andrew, verbatim intent):** *"I want no raw NATS calls from Refractor. Except micro.service
for now. Path B is the right one... when we extracted the ConsumerSupervisor out of Refractor I thought
we would make Refractor use it — that was the whole point. ... nothing has been shipped... if you need to
drop and recreate a stream in my local stack to set the correct DeliverPolicy — go for it. ... make fewer
stages if you want."*

**End-state assertion:** `grep -rn 'nats-io/nats.go\|jetstream' internal/refractor/ --include='*.go'`
(non-test) returns **only** `control/service.go` (the micro.Service control-plane responder — explicitly
allowed "for now").

## Grounding correction to the original scoping note
A code survey at build time changed the picture materially — the heaviest item is **already done**:

- **Rule (per-lens) consumers already run on `ConsumerSupervisor`.** `pipeline.RunOn(conn, ConsumerSpec)`
  builds a per-pipeline supervisor; `pipeline.Run` calls `supervisor.Add(spec)` with the supervised
  handler/Classify/Probe/HealthSink. Pause / Resume / Reset (rebuild) / `PendingForConsumer` all already
  route through the supervisor. So "make Refractor's rule consumers use the supervisor" — Andrew's "whole
  point" — shipped already.
- **`consumer.Manager` is now vestigial.** It survives only at `cmd/refractor/main.go` to (a) `Add` the
  durable up-front and (b) hand a raw `jetstream.Consumer` to the `LagPoller` + `hb.LagProvider`. The
  supervisor already creates the durable idempotently and exposes `PendingForConsumer`. → **retire it.**
- **`consumer.Bootstrapper`** (the adjacency-index consumer) is the **last hand-rolled loop**
  (`js.CreateOrUpdateConsumer` + own drain + per-message lag=0 "Ready" signal). → migrate to the substrate
  durable-consumer primitive.

## Decisions (made at build time; review may overturn)
- **D1 — substrate KV handle type.** Refractor's read path threads `jetstream.KeyValue` *handles*
  (`adjKV`, `coreKV`, `targetKV`) through hot loops (evaluator / executor / projection / adjacency /
  adapter), calling `.Get/.Create/.Update/.Delete/.WatchAll` and branching on `jetstream.ErrKeyNotFound`/
  `ErrKeyExists`. Add a substrate-owned bucket-handle type — `substrate.KV` (obtained via
  `conn.OpenKV(ctx, bucket) (*KV, error)`) — exposing exactly the methods Refractor needs, returning
  substrate types + substrate sentinel errors. Refractor threads `*substrate.KV` instead of
  `jetstream.KeyValue` — **lowest-churn**: signatures change type, call sites keep `.Get(ctx, key)` shape.
  Rejected: threading `(conn, bucketName)` everywhere (higher churn, re-resolves the handle, doesn't fit
  the handle-shaped hot loop). The existing bucket-based `conn.KV*` methods stay (they delegate to the same
  cached handle); the handle type is additive and jetstream-free on its surface.
- **D2 — `NumPending` on `substrate.Message`.** The Bootstrapper's "Ready when lag=0" signal reads
  `msg.Metadata().NumPending` per message. Add `NumPending uint64` to `substrate.Message` (populated from
  `msg.Metadata()` in `newMessage`, alongside the existing `NumDelivered`). Additive, generally useful, no
  behavior change for existing handlers.
- **D3 — Bootstrapper → `conn.RunDurableConsumer`.** The adjacency consumer is a fire-and-forget index
  builder (DeliverAll, explicit ack, no queue group, no pause/probe/health) — a clean fit for the
  `RunDurableConsumer` primitive, not the full supervisor. Its handler does `adjacency.Build` + the
  link-envelope bridge + signals Ready when `msg.NumPending == 0` (D2). Disposition maps to substrate
  `Decision` (Ack / Nak / Term).
- **D4 — DLQ / audit stream provisioning → substrate helper.** `failure/dlq.go` and `health/audit_writer.go`
  call `js.CreateOrUpdateStream` ad-hoc at rule startup. Add a minimal substrate `EnsureStream` helper
  (substrate-owned config struct, no jetstream leak) and route both through it. (Considered moving to
  bootstrap provisioning; deferred — these streams are per-rule/lazy, and a substrate helper keeps the
  call local without a bootstrap-version bump. Revisit if review prefers bootstrap.)
- **D5 — retire `consumer.Manager`.** Delete the package; `cmd/refractor` stops calling `manager.Add` /
  `manager.Consumer`. The supervisor already owns durable creation.
- **D6 — `LagPoller` + `hb.LagProvider` → `PendingForConsumer`.** The LagPoller takes a substrate
  lag-source (the pipeline's supervisor via a `pipeline.Pending(ctx)` accessor) instead of a raw
  `jetstream.Consumer`. `hb.LagProvider` reads pending from each pipeline's supervisor.

## Waves (each independently builds + tests green)
1. **Substrate additions (pure-additive, isolated):** `substrate.KV` handle type + `conn.OpenKV`;
   `NumPending` on `Message`; `EnsureStream` helper; a substrate watch surface for the adjacency
   updates-watch (reuse `SubscribeKVChanges` if its semantics fit, else a typed `KV.Watch`). Unit-tested in
   `internal/substrate`. No refractor change.
2. **Consumers (Andrew's core ask):** retire `consumer.Manager`; migrate `Bootstrapper` →
   `RunDurableConsumer`; rewire `LagPoller` + `hb.LagProvider` → `PendingForConsumer`.
3. **Read-path KV-handle substitution:** swap `jetstream.KeyValue` → `*substrate.KV` and
   `jetstream.Err*` → `substrate.Err*` across adapter / adjacency / ruleengine{simple,full} / projection /
   pipeline; migrate the pipeline adjacency updates-watch.
4. **Provisioning + failure sentinels:** DLQ / audit → `EnsureStream`; `failure/classify.go` +
   `failure/retry.go` sentinels + JS handle.
5. **Assert + gates + 3-layer review + commit.** End-state grep clean; full gates; adversarial review
   (touches the projection hot path → full 3-layer); ff-merge to `main`; CI green.

## Verification (per wave + final)
`cd <worktree> && go build ./... && make vet && golangci-lint run ./... && go test ./internal/refractor/... ./internal/substrate/...`
plus `make verify-package-*` if any stream/DDL provisioning shifts. Contract #1 key shapes and Contract #6
§6.2 guarded-write invariants must be byte-identical (pure refactor, no contract change). Local stack
streams may be dropped/recreated to set the correct DeliverPolicy (Andrew's explicit OK).
