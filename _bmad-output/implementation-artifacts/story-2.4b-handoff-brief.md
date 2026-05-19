---
title: Story 2.4b Implementation Handoff Brief
story: 2.4b — Refractor Lattice-Native Source Plane (Durable Consumer + NATS Services)
model_tier: Opus (locked)
token_budget: ~100K (estimate; for tracking only — not a halt threshold)
session: Fresh implementation session
architecture_lead: Winston
date: 2026-05-19
predecessor: Story 2.4a (token eviction); Stories 4.6 + 4.7 (post-realignment); Story 2.1 morph
---

# Story 2.4b — Refractor Lattice-Native Source Plane: Handoff Brief

## Your Role

Migrate two Refractor source-plane surfaces from their post-morph "borrowed" patterns to Lattice-native shapes:

1. **Lens-definition source**: from `kv.Watch(ctx, "vtx.meta.>", jetstream.IncludeHistory())` (ephemeral KV watcher) to a **durable JetStream consumer** on the `KV_core-kv` backing stream filtered to `$KV.core-kv.vtx.meta.>` subjects. This preserves cross-restart sequence position, matches the rest of Refractor's CDC pattern, and stops the wasteful "replay-all-history-on-resume" behavior of KV Watch.

2. **Control plane**: from `nc.QueueSubscribe(subjects.Control(), "materializer-control", ...)` to **NATS Services framework** (`micro.AddService`) with endpoints at `lattice.ctrl.refractor.<lensId>.<op>`. Same handler logic; different transport pattern. Auth still uses the existing StubCapabilityChecker (real Capability KV read-auth is Phase 2).

This is the design-bearing half of Story 2.4. Sister story 2.4a handled the mechanical token eviction.

## 🔴 MANDATORY OPERATING RULES (READ FIRST)

- **No worktree.** Work in repo root.
- **No commits, no pushes.** Winston commits + pushes after review.
- **Planning artifacts are read-only.**
- **Token budget tracking-only.** Estimate ~100K.
- **Model tier:** Opus only.
- **Architecture binding:** Contract #1 + Contract #6 (no changes); `PHASE-1-COURSE-CORRECTION.md` §A.5 + §C5 (the audit findings); the new substrate helper `(*Conn).SubscribeKVChanges` is the centerpiece — its API is specified below in §1.
- **Andrew has authorized autonomous proceed.**

## What's in Place

- **`internal/refractor/lens/corekv_source.go`** — current implementation uses `kv.Watch(ctx, "vtx.meta.>", jetstream.IncludeHistory())`. Lens-spec mutations on Core KV trigger the watcher; the source translates the value envelope into a `lens.Rule` and dispatches via the existing pipeline-lifecycle hooks.
- **`internal/refractor/control/service.go`** — current implementation uses `nc.QueueSubscribe(subjects.Control(), "materializer-control", s.handleControlMsg)`. Six control ops dispatched in `handleControlMsg`. (Note: 2.4a left the `materializer.control` subject name unchanged; this story renames + migrates.)
- **`internal/substrate/`** — exposes `Connect`, `KVGet`, `KVPut`, `KVPutWithTTL`, `KVListKeys`, `AtomicBatch`, `PublishBatch`, and (post-4.6) `AdjacencyForNode`. Does NOT yet expose any "subscribe-to-KV-changes-via-durable-consumer" helper. This story adds it.

Tree clean post-2.4a.

## Story Scope (2.4b)

### 1. Substrate helper: `SubscribeKVChanges` (~25K tokens)

Add to `internal/substrate/`:

```go
// KVEvent describes a single KV mutation observed via SubscribeKVChanges.
type KVEvent struct {
    Bucket    string
    Key       string
    Value     []byte
    Revision  uint64
    IsDeleted bool      // true if Value-envelope's isDeleted is true (soft-delete)
    Sequence  uint64    // JetStream sequence
}

// SubscribeKVChanges creates a durable JetStream consumer on the
// KV_<bucket> backing stream filtered to <keyPrefix>. It returns a
// channel of KVEvent; closing ctx cancels the subscription and
// removes the consumer.
//
// Durable name MUST be unique within the deployment. Sequence position
// is persisted across restarts — re-creating with the same durable
// name resumes from the last-acked sequence.
//
// SubscribeKVChanges does NOT include history by default. Pass
// IncludeHistory=true to read from the start of the bucket's history;
// this is appropriate for components that need a full bootstrap-state
// view at first connect.
//
// Errors during message processing are surfaced via the returned
// channel's close. Callers should monitor for channel close to
// detect unrecoverable subscription failures.
func (c *Conn) SubscribeKVChanges(
    ctx context.Context,
    bucket string,
    keyPrefix string,
    durableName string,
    opts SubscribeKVOptions,
) (<-chan KVEvent, error)

type SubscribeKVOptions struct {
    IncludeHistory bool          // start from sequence 1; default false (start from "new")
    AckPolicy      jetstream.AckPolicy   // default AckExplicit
    MaxDeliver     int           // default 10
}
```

Implementation:
- Create-or-update a JetStream consumer on stream `KV_<bucket>` with FilterSubject `$KV.<bucket>.<keyPrefix>` (where `<keyPrefix>` translates to the JetStream subject namespace for KV backing — e.g., `vtx.meta.>` becomes `$KV.core-kv.vtx.meta.>`).
- Pull-based consumer with explicit ack. Each message → decode KV envelope → emit `KVEvent` → wait for caller to consume (channel is unbuffered) → ack.
- On ctx.Done: drain consumer, delete via `js.DeleteConsumer`, close channel.

Add unit tests:
- TestSubscribeKVChanges_HappyPath: subscribe to a prefix, write a KV value, assert event received.
- TestSubscribeKVChanges_IncludeHistory: pre-seed values, subscribe with IncludeHistory=true, assert all replayed.
- TestSubscribeKVChanges_DurableResume: subscribe, consume to seq=N, ctx.Done, restart with same durable name, write more, assert sequence continues from N+1 (no replay).
- TestSubscribeKVChanges_Tombstone: write a value, soft-delete it, assert IsDeleted: true on the event.

### 2. Refractor lens source migration (~20K tokens)

In `internal/refractor/lens/corekv_source.go`:
- Replace the `kv.Watch(ctx, "vtx.meta.>", jetstream.IncludeHistory())` call with `substrate.SubscribeKVChanges(ctx, "core-kv", "vtx.meta.", "refractor-lens-source", SubscribeKVOptions{IncludeHistory: true})`.
- Adapt the dispatch loop: instead of consuming from `<-watcher.Updates()`, consume from `<-kvEvents`. Translate `KVEvent` → existing internal types.
- The `IncludeHistory: true` flag preserves the watcher's current behavior of replaying history on startup. After Story 4.7's kernel minimization, the meta-vertex history is small (~33 entries kernel + N package-installed entries), so the replay cost is acceptable. (A future Phase 2 story might cache the meta-vertex state in Refractor and switch to IncludeHistory=false; out of scope for 2.4b.)
- Delete the now-unused KV watch path entirely. The `corekv_source.go` becomes simpler.

Verification: existing Refractor integration tests (e.g., `internal/refractor/refractor_e2e_test.go`, `refractor_capability_e2e_test.go`, `refractor_capability_multi_e2e_test.go`) all pass without modification. Tests assert behavior, not transport mechanism.

### 3. Control plane migration to NATS Services (~30K tokens)

In `internal/refractor/control/service.go`:
- Replace `nc.QueueSubscribe` with `micro.AddService`:
  ```go
  svc, err := micro.AddService(nc, micro.Config{
      Name:        "refractor-control",
      Version:     "1.0.0",
      Description: "Refractor control plane endpoints",
  })
  ```
- Register each of the 6 control ops as a service endpoint under `lattice.ctrl.refractor.<lensId>.<op>`:
  - `activate`, `pause`, `resume`, `rebuild`, `delete`, `status`
  - Each endpoint reuses the existing handler logic from `handleControlMsg` (just refactor the dispatch switch into per-op handlers).
- Auth: continue using `StubCapabilityChecker` for Phase 1. The real Capability-KV-backed checker is Phase 2.
- Lens-ID routing: each endpoint's subject embeds the lens ID. Extract from `msg.Subject()` per the NATS Services API.

Update `internal/refractor/control/service_test.go`:
- Tests use `nats.Conn.Request(subj, payload, timeout)` against the new subjects.
- All 6 ops have happy-path + auth-denied tests.
- Add a new test: `TestNATSServicesIntrospection` that calls `$SRV.PING.refractor-control` and asserts the service is discoverable (NATS Services standard introspection).

### 4. Subject rename (~10K tokens)

In `internal/refractor/subjects/subjects.go`:
- `Control() string` returns `"materializer.control"` today. After 2.4a it stayed that way. Now rename: `Control() string` → `"lattice.ctrl.refractor.>"` (a wildcard pattern that the service framework subscribes under). Actually micro.AddService takes care of subject construction; the `subjects` package's `Control()` becomes unused. Delete it.
- Subjects package shrinks; that's fine.

### 5. Deployment-grep audit cleanup (~5K tokens)

Re-run `grep -rni "materializer" internal/ cmd/` after migration. Expected residual: only morph-provenance comments, `internal/spike/`, and Materializer-domain test fixtures. The control-plane queue group name `"materializer-control"` (which 2.4a left in place) is gone (the QueueSubscribe is gone).

### 6. Verification (~10K tokens)

Standard build/lint/test gates. Plus:
- **Manual restart test**: `make up`, run a `MutationOp` that creates a new lens via the (post-4.7) installer, observe Refractor projects, then `make down`/`make up`, observe Refractor resumes WITHOUT replaying every KV history entry (the durable consumer's sequence position held).
- **NATS Services introspection**: `nats micro list` (or via the Go API) shows `refractor-control` v1.0.0.

## Architectural Decisions Already Made (Winston)

1. **`SubscribeKVChanges` is the substrate seam.** All future code that needs to react to Core KV mutations uses this helper, not raw `kv.Watch` or `js.CreateOrUpdateConsumer`. This codifies the "durable JetStream consumer on the backing stream" pattern as the Lattice-native shape.

2. **IncludeHistory option for the lens-source migration.** Today's watcher replays history on resume; the durable consumer's natural behavior is "start from new sequences only". To preserve current behavior (Refractor recovering full meta-vertex state on restart), the lens source passes `IncludeHistory: true`. Future Phase 2 work can introduce stateful caching of meta-vertices in Refractor and switch to `IncludeHistory: false`.

3. **Durable consumer per Refractor instance, not per lens.** The Refractor's lens source uses ONE durable consumer reading all `vtx.meta.*` mutations (matches today's single-watcher pattern). Per-lens consumers would multiply consumer overhead and reduce JetStream catalog clarity. Phase 2 (multi-cell) revisits.

4. **Durable name = `refractor-lens-source`** (singular, instance-shared). Multi-instance Refractor (Phase 3 multi-cell) revisits naming to include cell ID.

5. **NATS Services framework introduces a new dependency surface.** Confirm `nats.go/micro` is the import path; current dependency is already on `nats.go` so this is a sub-package, not a new go.mod entry.

6. **Lens-ID routing via subject path.** `lattice.ctrl.refractor.<lensId>.<op>` puts the lens ID in the subject; the endpoint extracts it. Wildcard subscription handles all lenses uniformly. Per-lens services would be infeasible (Refractor doesn't know all lens IDs at startup).

7. **Auth stays stub for Phase 1.** Real read-auth via Capability KV is Phase 2. The migration to NATS Services is orthogonal to auth strengthening.

8. **No behavior changes to the lens lifecycle.** Activation, pause, resume, rebuild, delete, status all behave identically. Only the transport changes.

9. **No new error types or contract additions.** The migration is transport-level.

10. **Comment-update sweep**: any remaining "uses kv.Watch" or "QueueSubscribe" references in comments get updated to reflect the new shape.

## Required Context — Read These Only

| File | Why |
|---|---|
| `PHASE-1-COURSE-CORRECTION.md` §A.5 (5b + 5c) + §C5 | Audit findings + scope |
| `_bmad-output/implementation-artifacts/story-2.4a-handoff-brief.md` | Predecessor — what 2.4a did, what 2.4b still owns |
| `internal/refractor/lens/corekv_source.go` | **Edit this** — kv.Watch → SubscribeKVChanges |
| `internal/refractor/control/service.go` | **Edit this** — QueueSubscribe → micro.AddService |
| `internal/refractor/control/service_test.go` | Adapt tests for new transport |
| `internal/refractor/subjects/subjects.go` | **Edit this** — delete Control() |
| `internal/substrate/` (all files) | Read-only — confirm helper extension point |
| `internal/substrate/batch.go` + `kv.go` | Read-only — pattern reference for new helper |
| `nats.go/micro` (vendored) | Skim the package's API surface; usually `micro.AddService` + `micro.Endpoint` |

**DO NOT read**: `lattice-architecture.md` (full), full `epics.md`, Materializer source, vendored ANTLR parser, Stories 1.x/3.x briefs.

## Suggested Sequence

**Phase A — Substrate helper (target ~30K tokens):**
1. Implement `SubscribeKVChanges` + `KVEvent` + `SubscribeKVOptions` in `internal/substrate/`.
2. Write 4 unit tests against embedded NATS fixture.

**Phase B — Lens source migration (target ~20K tokens):**
3. Replace `kv.Watch` in `corekv_source.go`. Adapt dispatch loop.
4. Run existing refractor integration tests; iterate.

**Phase C — Control plane migration (target ~30K tokens):**
5. Replace `QueueSubscribe` with `micro.AddService` in `control/service.go`. Refactor handlers per-op.
6. Adapt `service_test.go`.

**Phase D — Subject cleanup (target ~5K tokens):**
7. Delete `Control()` from subjects package.

**Phase E — Verification + grep audit (target ~10K tokens):**
8. Run all gates.
9. Manual restart + NATS Services introspection.

**Phase F — Closing (target ~10K tokens):**
10. Update token tracker Row 2.4b.
11. Closing summary.

## Required Verification

```bash
go build ./...
make vet
/Users/andrewsolgan/go/bin/golangci-lint run ./...
go test ./internal/substrate/... -count=1     # incl. 4 new SubscribeKVChanges tests
go test ./internal/refractor/... -count=1     # incl. control + corekv_source
make verify-kernel                            # ~33 OK
make verify-package-rbac                      # unchanged
make verify-package-identity                  # unchanged
make verify-package-identity-hygiene          # unchanged
make test-bypass                              # 4/4 BLOCKED
make test-capability-adversarial              # 4/4 DEFENDED
go test ./... -p 1 -count=1                   # all green

# Manual:
make up
nats micro list                               # refractor-control v1.0.0 visible
# Submit a lens-create op; observe Refractor activates.
make down && make up
# Confirm Refractor resumes; durable consumer position held.
```

## Deliverables Checklist

1. ✅ `internal/substrate/` — SubscribeKVChanges + KVEvent + SubscribeKVOptions + 4 unit tests
2. ✅ `internal/refractor/lens/corekv_source.go` — migrated to SubscribeKVChanges
3. ✅ `internal/refractor/control/service.go` — migrated to micro.AddService
4. ✅ `internal/refractor/control/service_test.go` — adapted
5. ✅ `internal/refractor/subjects/subjects.go` — Control() deleted
6. ✅ All gates green
7. ✅ Manual restart + NATS Services introspection verified
8. ✅ Token tracker Row 2.4b updated
9. ✅ Closing summary

## What 2.4b Is NOT

- Not a Capability Lens or full openCypher engine change
- Not auth strengthening (Phase 2)
- Not multi-cell scaling
- Not stateful Refractor caching of meta-vertices (Phase 2)

## Escalation

CAR for:
- `KV_core-kv` backing stream subject namespace differs from `$KV.<bucket>.<key>` mapping
- NATS Services framework's lens-ID-from-subject extraction conflicts with existing handler signatures
- Durable consumer sequence position doesn't survive `make down`/`make up` (would indicate stream-not-persisted-config issue)

Halt for:
- Bypass / Gate 3 vector flips
- Stuck-loop pattern
- Substrate helper signature can't be implemented without invasive nats.go API use

## Closing

1. Verify all 9 deliverables
2. Run all gates + manual checks
3. Token tracker Row 2.4b
4. Closing summary

**DO NOT commit. DO NOT push.** Winston commits + pushes after review.
