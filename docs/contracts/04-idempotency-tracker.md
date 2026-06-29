# Contract #4 — Idempotency Tracker (`vtx.op.<requestId>`)

The idempotency tracker is the artifact that makes operation-level idempotency work. Every committed operation produces a tracker in Core KV at key `vtx.op.<requestId>`, written atomically with the operation's mutations at commit step 8. The tracker is the linchpin of the dedup check at step 2: its presence means "this operation already committed."

### 4.1 Tracker Shape

```json
{
  "key": "vtx.op.Rm7q3pntwzkfbcxv5p9j",
  "class": "op",
  "isDeleted": false,
  "createdAt": "2026-04-11T14:32:18.215Z",
  "createdBy": "vtx.identity.St6mP3qBn4rT8wYxK7Vc",
  "createdByOp": "vtx.op.Rm7q3pntwzkfbcxv5p9j",
  "lastModifiedAt": "2026-04-11T14:32:18.215Z",
  "lastModifiedBy": "vtx.identity.St6mP3qBn4rT8wYxK7Vc",
  "lastModifiedByOp": "vtx.op.Rm7q3pntwzkfbcxv5p9j",
  "data": {
    "operationType": "CreateIdentity",
    "lane": "default",
    "submittedAt": "2026-04-11T14:32:18.142Z",
    "committedAt": "2026-04-11T14:32:18.215Z",
    "mutationKeys": [
      "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
      "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y.email"
    ],
    "eventClasses": ["identityCreated"],
    "status": "committed"
  }
}
```

The tracker uses the universal envelope (Contract #1 §1.3). Provenance fields are self-referential: `createdByOp` and `lastModifiedByOp` both point to the tracker itself. This is by design — the tracker IS the op record, and provenance fields throughout the platform always reference an op tracker.

### 4.2 Field Specification — `data` payload

| Field | Required | Purpose |
|-------|----------|---------|
| `operationType` | yes | Echo from operation envelope. Allows querying "all CreateIdentity operations" without re-reading `core-operations`. |
| `lane` | yes | Echo from operation envelope. |
| `submittedAt` | yes | Client-side timestamp from envelope. |
| `committedAt` | yes | Step 8 commit timestamp (Processor-side). Authoritative for ordering. |
| `mutationKeys` | yes | Full list of Core KV keys mutated by this operation. Enables traceability ("what did this operation touch?") without re-reading `core-operations` or replaying. Includes keys for `create`, `update`, and `tombstone` mutations alike. |
| `eventClasses` | yes | List of event class names emitted (e.g., `["identityCreated", "emailVerificationRequested"]`). Enables traceability of which events fired. |
| `status` | yes | Currently always `"committed"` for any tracker present in Core KV. Reserved for future states (e.g., `"replaying"`) — Phase 1 only emits `"committed"`. |

**What the tracker does NOT carry:**
- The original `payload` field from the operation envelope. Payloads may be large, may contain sensitive data, and are recoverable from `core-operations` JetStream (the immutable ledger). The tracker's job is "did this commit happen?" not "what was originally requested?"
- The `actor` field separately — it's already in the standard `createdBy` envelope field.
- The `contextHint.reads` — runtime information, not part of the operation's outcome.

### 4.3 Retention via NATS Per-Key TTL

Trackers are written with a **24-hour per-key TTL** at commit step 8, using NATS JetStream's per-message TTL feature (ADR-48, introduced in NATS 2.11; Lattice's platform floor is **NATS 2.14** — pinned in `go.mod` / `docker-compose.yml` — which subsumes per-key TTL (2.11+), atomic batch (2.12+), and recurring `@every`/cron message schedules (2.14+; Contract #10 §10.4)). After 24 hours, NATS publishes a `PURGE` marker for the tracker's key with header `Nats-Marker-Reason: MaxAge`, which Refractor and other CDC consumers observe as an explicit expiry event.

**Configuration requirements:**
- The Core KV bucket must be provisioned with `allow_msg_ttl: true` (substrate responsibility at bucket creation — Story 1.4 acceptance criterion)
- TTL value (24h) is set as a per-write parameter on the tracker's `Create()` call within the atomic batch — NOT as a bucket-wide default (other Core KV entries are durable, not TTL'd)
- The exact TTL is deployment-configurable; 24h is the architecture-locked default per the architecture document's "24h idempotency horizon" note

**Stream 0 spike validation (Story 1.1 acceptance criterion):**
The NATS atomic batch spike must validate that per-key TTL on a single write **within an atomic batch** behaves correctly — i.e., the tracker's TTL clock starts at commit time, the PURGE marker fires at the expected interval, and the marker is delivered to CDC consumers. If TTL within atomic batches has unexpected semantics, this is a blocking finding that requires architectural change before Stream 1 proceeds.

**Behavior after TTL expiry:**
- The tracker key is no longer present in Core KV
- Dedup check at step 2 finds nothing → if the same `requestId` is resubmitted after expiry, it executes fresh as a new operation
- This is the correct semantic: the platform's idempotency guarantee is **time-bounded to 24h**, and post-expiry resubmission is a legitimate new operation, not a duplicate

**TTL is immutable post-write:**
ADR-48 does not support modifying TTL on an existing key. A tracker's expiry clock is fixed at the moment of step 8 commit. Operations that need extended idempotency (Loom workflows that sleep for weeks) use a different dedup pattern, layered on top of (or alongside) the tracker — out of Phase 1 scope per the architecture's note.

**Operator-driven immediate retry (rare, disaster recovery):**
An operator who needs to immediately re-execute an operation that already committed (without waiting for TTL expiry) uses **NATS administrative purge** of the specific tracker key. This is a NATS operational concern, not a Lattice business semantic — no special Lattice command exists. The operator's purge action removes the tracker; subsequent resubmission with the same `requestId` proceeds as a fresh operation.

### 4.4 Dedup Lifecycle

```
T+0:    Operation submitted, requestId=R
T+1ms:  Processor begins commit path
T+15ms: Step 8 atomic batch — tracker[R] written with TTL=24h
T+20ms: Step 9 events published
T+25ms: Step 10 ack to JetStream
        ─────────────────────────────────
        Tracker exists for 24h.
        Any resubmit with requestId=R is detected at step 2 → status: "duplicate"
        ─────────────────────────────────
T+24h:  NATS publishes PURGE marker for tracker[R]
T+24h+ε: Refractor sees marker, removes tracker[R] from op-history Lens projections (Phase 2+)
        ─────────────────────────────────
T+24h+1ms: Resubmit with requestId=R → step 2 dedup finds nothing → fresh execution
```

### 4.5 Implementation Notes

**For the AI agent implementing Story 1.4 (Dev Harness & Operation Envelope Schema):**

- Core KV bucket creation must include `allow_msg_ttl: true` configuration
- Document the bucket-creation pattern in the dev harness scripts for reproducibility

**For the AI agent implementing Story 1.6 (Processor — DDL Validation & Atomic Batch):**

- At step 8, the tracker write is included in the atomic batch alongside business mutations
- The tracker write uses `Create()` with `revision=0` (tracker must not pre-exist; if it does, the operation should have been short-circuited at step 2)
- The tracker write specifies `TTL=24h` (configurable, deployment-scoped; default 24h)
- If the atomic batch fails for any reason, the tracker is not committed → no idempotency entry → no risk of false-positive dedup on retry
- After successful atomic commit, the tracker's TTL clock has started — its lifecycle is governed by NATS, not by Processor code

**For the AI agent implementing Story 1.7 (Processor — Event Publication & Fault Injection):**

- Fault injection tests should include the case "Processor crashes between step 8 commit (tracker + mutations) and step 9 (event publish)." On redelivery, step 2 finds the tracker, the path re-derives events and re-publishes them, and acks. The tracker's TTL clock does NOT restart on retry — it ticks from the original step 8 commit time.

**For the AI agent implementing Story 1.4 (Processor — Consume, Dedup, Auth Stub):**

- Step 2 dedup: `GetCoreKV("vtx.op." + envelope.requestId)`. If found and `isDeleted: false` → return `duplicate` reply with `originalCommittedAt` from `data.committedAt`. If not found → proceed to step 3.
- If found with `isDeleted: true`: this is an operator-driven retry signal (see §4.3). Treat as not-found and proceed. (Note: with NATS TTL handling natural retention, the `isDeleted: true` path is reserved for the rare operator-tombstone-then-resubmit pattern. NATS administrative purge is the more common retry mechanism.)
