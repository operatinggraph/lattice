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

Trackers are written with a **24-hour per-key TTL** at commit step 8, using NATS JetStream's per-message TTL feature (ADR-48; the platform's NATS version floor and its feature gates are owned by `docs/vendors.md`). After 24 hours, NATS publishes a `PURGE` marker for the tracker's key with header `Nats-Marker-Reason: MaxAge`, which Refractor and other CDC consumers observe as an explicit expiry event.

**Configuration requirements:**
- The Core KV bucket must be provisioned with `allow_msg_ttl: true` (substrate responsibility at bucket creation — Story 1.4 acceptance criterion)
- TTL value (24h) is set as a per-write parameter on the tracker's `Create()` call within the atomic batch — NOT as a bucket-wide default (other Core KV entries are durable, not TTL'd)
- The exact TTL is deployment-configurable; 24h is the architecture-locked default per the architecture document's "24h idempotency horizon" note

**Behavior after TTL expiry:**
- The tracker key is no longer present in Core KV
- Dedup check at step 2 finds nothing → if the same `requestId` is resubmitted after expiry, it executes fresh as a new operation
- This is the correct semantic: the platform's idempotency guarantee is **time-bounded to 24h**, and post-expiry resubmission is a legitimate new operation, not a duplicate

**TTL is immutable post-write:**
ADR-48 does not support modifying TTL on an existing key. A tracker's expiry clock is fixed at the moment of step 8 commit. Operations that need extended idempotency (Loom workflows that sleep for weeks) use a different dedup pattern, layered on top of (or alongside) the tracker — out of Phase 1 scope per the architecture's note.

**Operator-driven immediate retry (rare, disaster recovery):**
An operator who needs to immediately re-execute an operation that already committed (without waiting for TTL expiry) uses **NATS administrative purge** of the specific tracker key. This is a NATS operational concern, not a Lattice business semantic — no special Lattice command exists. The operator's purge action removes the tracker; subsequent resubmission with the same `requestId` proceeds as a fresh operation.

### 4.4 Dedup semantics

- The tracker is written in the step-8 atomic batch with **`Create()` at `revision=0`** — it must not
  pre-exist (a pre-existing tracker should have short-circuited at step 2). If the batch fails, no
  tracker lands, so a retry cannot false-positive dedup.
- **Step-2 dedup:** GET `vtx.op.<requestId>`. Found with `isDeleted: false` → `duplicate` reply
  carrying `originalCommittedAt` from `data.committedAt`. Not found → proceed. Found with
  `isDeleted: true` → an operator-driven retry signal: treat as not-found and proceed.
- **The TTL clock does NOT restart on redelivery** — it ticks from the original step-8 commit. On a
  crash between step 8 and step 9, redelivery finds the tracker at step 2, re-derives and re-publishes
  the events, and acks.
