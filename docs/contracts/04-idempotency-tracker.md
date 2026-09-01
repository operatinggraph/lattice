# Contract #4 — Idempotency Tracker (`vtx.op.<requestId>`)

The idempotency tracker is the artifact that makes operation-level idempotency work. Every committed operation produces a tracker in Core KV at key `vtx.op.<requestId>`, written **atomically with the operation's mutations**. Its presence means "this operation already committed," and the dedup check consults it **before** any execution.

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
| `committedAt` | yes | Commit timestamp. Authoritative for ordering. |
| `mutationKeys` | yes | Full list of Core KV keys mutated by this operation. Enables traceability ("what did this operation touch?") without re-reading `core-operations` or replaying. Includes keys for `create`, `update`, and `tombstone` mutations alike. |
| `eventClasses` | yes | List of event class names emitted (e.g., `["identityCreated", "emailVerificationRequested"]`). Enables traceability of which events fired. |
| `status` | yes | Always `"committed"` for any tracker present in Core KV; other values are reserved. |

**What the tracker does NOT carry:**
- The original `payload` field from the operation envelope. Payloads may be large, may contain sensitive data, and are recoverable from `core-operations` JetStream (the immutable ledger). The tracker's job is "did this commit happen?" not "what was originally requested?"
- The `actor` field separately — it's already in the standard `createdBy` envelope field.
- The `contextHint.reads` — runtime information, not part of the operation's outcome.

### 4.3 Retention via NATS Per-Key TTL

Trackers are written with a **24-hour per-key TTL** (version gates: `docs/vendors.md`). At expiry a `PURGE` marker for the tracker's key carrying header `Nats-Marker-Reason: MaxAge` appears on the change feed, which CDC consumers observe as an explicit expiry event.

- The TTL applies to trackers only — every other Core KV entry is durable, never TTL'd.
- The exact TTL is deployment-configurable; 24h is the architecture-locked default.

**Behavior after TTL expiry:**
- The tracker key is no longer present in Core KV
- The dedup check finds nothing → if the same `requestId` is resubmitted after expiry, it executes fresh as a new operation
- This is the correct semantic: the platform's idempotency guarantee is **time-bounded to 24h**, and post-expiry resubmission is a legitimate new operation, not a duplicate

**TTL is immutable post-write:**
A tracker's expiry clock is fixed at the moment of commit and cannot be extended. A flow that needs idempotency beyond the horizon (e.g. a pattern that sleeps for weeks) layers its own dedup on top of (or alongside) the tracker.

**Operator-driven immediate retry (rare, disaster recovery):**
An operator who needs to immediately re-execute an operation that already committed (without waiting for TTL expiry) uses **NATS administrative purge** of the specific tracker key — no special Lattice command exists. The purge removes the tracker; subsequent resubmission with the same `requestId` proceeds as a fresh operation.

### 4.4 Dedup semantics

- The tracker commits **in the same atomic batch** as the mutations and asserts non-existence — a
  failed commit lands no tracker, so a retry can never false-positive dedup.
- **Dedup:** the tracker found with `isDeleted: false` → `duplicate` reply carrying
  `originalCommittedAt` from `data.committedAt`. Not found → proceed. Found with `isDeleted: true` →
  an operator-driven retry signal: treat as not-found and proceed.
- **The TTL clock does NOT restart on redelivery** — it ticks from the original commit. A redelivered
  commit re-publishes its events and never double-acts or extends the horizon.
