# Contract #3 — MutationBatch and EventList (Starlark Return Contract)

The MutationBatch and EventList are the return value of a Starlark script's execution. They describe what the script wants the world to look like after the operation: state changes (mutations) and notifications (events). The Processor validates and commits them atomically.

### 3.1 Return Shape

A Starlark script returns a dict with two keys:

```python
return {
    "mutations": [ ... ],
    "events": [ ... ]
}
```

Both arrays may be empty (a no-op operation has zero mutations and zero events — useful for pure validation operations that succeed/fail without changing state).

### 3.2 MutationBatch

Each mutation declares an intended state transition on a single Core KV key.

```python
{
    "op": "create",
    "key": "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
    "document": {
        "class": "identity",
        "isDeleted": false,
        "data": {}
    }
}
```

| Field | Required For | Purpose |
|-------|--------------|---------|
| `op` | all | One of `create`, `update`, `tombstone`. See §3.3. |
| `key` | all | Full Core KV key conforming to Contract #1 patterns. |
| `document` | `create`, `update` | Document body. Includes `class`, `isDeleted`, and `data` (plus aspect/link-specific fields like `vertexKey`/`localName`/`sourceVertex`/`targetVertex`). **Provenance fields are NOT set by the script** — `createdAt`, `createdBy`, `createdByOp`, `lastModifiedAt`, `lastModifiedBy`, `lastModifiedByOp` are injected by the Processor at commit step 6 using the current operation's actor and timestamp. |
| `expectedRevision` | optional, `update` only | Revision condition for optimistic concurrency. If omitted, Processor uses the revision read during step 4 (Hydrate). Explicit override is reserved for compensating operations that need to force a specific revision check. |

### 3.3 Mutation Op Types — and why there is no `upsert`

**`create`** — assert the key did not exist before this operation. Submitted with NATS revision condition `revision=0`. If the key exists in any state (including tombstoned), the atomic batch is rejected.

**`update`** — assert the key existed before this operation and the script is modifying it. Submitted with NATS revision condition equal to either `expectedRevision` (if provided) or the revision read at step 4. The Processor accepts updates targeting tombstoned documents — setting `isDeleted: false` in the update payload implicitly restores the entity. There is no separate `restore` op.

**`tombstone`** — assert the key existed before this operation and the script is marking it deleted. The Processor sets `isDeleted: true` and updates `lastModifiedAt`/`lastModifiedBy`/`lastModifiedByOp`. The document payload is otherwise unchanged. Tombstones are permanent; keys are not reused — a new entity requires a new NanoID.

**Why no `upsert`:** Operation-level idempotency is guaranteed by `requestId` + tracker-in-atomic-batch + step 2 dedup (see Contract #2 §2.4 and the Processor commit path in `lattice-architecture.md`). The Processor will apply an operation's mutations **at most once** across any number of JetStream redeliveries:

- Crash before step 8 → no tracker, no mutations committed; redelivery re-executes fresh
- Crash after step 8 → tracker exists, mutations committed; redelivery's step 2 dedup short-circuits; mutations are NOT re-applied
- Multiple redeliveries → step 2 dedup short-circuits each one

`create`/`update`/`tombstone` therefore describe the script's *intent for state transition*, not retry-safe operations. The script asserts what should be true: `create` asserts "this key did not exist before"; `update` asserts "this key existed and I'm modifying it." A mismatch between the assertion and Core KV state surfaces as a `RevisionConflict` error — which is the correct outcome, because it means the script's model of the world disagrees with reality (typically: a concurrent operation with a different `requestId` changed the same state).

Silently masking that disagreement (the upsert semantic) would convert genuine data conflicts into silent data loss. The platform's preference is to fail loudly so the script author can branch explicitly.

### 3.4 EventList

Each event declares a business event to publish to `core-events` JetStream.

```python
{
    "class": "identityCreated",
    "data": {
        "identityKey": "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
        "createdBy": "vtx.identity.St6mP3qBn4rT8wYxK7Vc"
    }
}
```

| Field | Required | Purpose |
|-------|----------|---------|
| `class` | yes | Event type. Must match `canonicalName` of a registered event-type DDL (`class: "meta.ddl.eventType"`). Validated at commit step 7. Events with no registered DDL are rejected — unlike vertex/aspect/link writes, events MUST be declared. The `core-events` stream is a typed contract; consumers (Loom, Weaver) depend on schema knowledge. |
| `data` | yes | Event payload. Validated against the event DDL's schema at commit step 7. May be `{}` for parameterless events. |

**Event payload convention:** Events SHOULD carry vertex key references rather than full document copies. Consumers hydrate context from Lens projections rather than expecting events to carry all required state. This keeps events lean, decouples producers from consumers' evolving context needs, and prevents events from becoming an alternate source of truth.

### 3.5 Batch-Internal Consistency Rules

The Processor enforces internal consistency of the MutationBatch at commit step 6, before any KV writes occur:

**Endpoint resolution for link mutations:** A `create` mutation on a link key (`lnk.<t1>.<id1>.<name>.<t2>.<id2>`) requires both endpoint vertex keys to resolve. An endpoint resolves if:
- The vertex exists in Core KV (read during hydration, or detected via independent lookup), AND its `isDeleted` is `false`, OR
- The vertex is being created by another mutation in the same MutationBatch

If either endpoint fails to resolve, the entire operation is rejected with `SchemaViolation: DanglingReference`.

**Vertex resolution for aspect mutations:** Same rule — an aspect at `vtx.<type>.<id>.<localName>` requires the host vertex (`vtx.<type>.<id>`) to exist or to be created in the same batch.

**Tombstoning vertices with active aspects/links:** Tombstoning a vertex does NOT automatically tombstone its aspects or links. The Processor does not cascade. If a script wants cascade behavior, it explicitly includes tombstone mutations for the dependent aspects and links in the same batch. **Why:** cascade semantics are business-logic concerns (a vertex tombstone may want to retain historical aspects for audit), and the platform refuses to make that choice on the script's behalf. Readers filter on `isDeleted` independently; tombstoning a vertex makes its key invisible to most queries even if its aspects remain.

**Within-batch ordering:** Mutations within a MutationBatch form a set, not a sequence. The atomic batch commits them all simultaneously. Scripts must declare what should be true after the operation; they do not declare ordered procedural steps.

### 3.6 Script-Generated Keys

When a script creates new entities, it generates their NanoIDs inline and uses the full keys in subsequent mutations within the same batch.

```python
def execute(state, op):
    new_identity_id = nanoid.new()  # 20-char NanoID, custom alphabet (Contract #1)
    identity_key = "vtx.identity." + new_identity_id

    return {
        "mutations": [
            {
                "op": "create",
                "key": identity_key,
                "document": {"class": "identity", "isDeleted": False, "data": {}}
            },
            {
                "op": "create",
                "key": identity_key + ".email",
                "document": {
                    "class": "email",
                    "vertexKey": identity_key,
                    "localName": "email",
                    "isDeleted": False,
                    "data": {"value": op.payload["email"], "verified": False}
                }
            },
        ],
        "events": [
            {
                "class": "identityCreated",
                "data": {"identityKey": identity_key, "createdBy": op.actor}
            }
        ]
    }
```

The Starlark stdlib provides:
- `nanoid.new()` — returns a fresh 20-char NanoID from the substrate package's custom alphabet
- `nanoid.short()` — returns an 8-char NanoID for display codes (NOT for primary keys)

Both functions are deterministic-by-seed within a single script execution if needed for testing; the Processor seeds the generator with the operation's `requestId` to ensure replay determinism (re-executing the same operation produces the same generated IDs).

### 3.7 Architectural Boundary — Starlark Never Touches NATS

Starlark scripts are pure functions: `(state, operation) → (mutations, events)`. They have no NATS handle. They do not publish events; they declare events for the Processor to publish. They do not write to KV; they declare mutations for the Processor to apply.

This is the architectural boundary that makes Starlark execution deterministic and replayable (NFR-E4). Any I/O that scripts appear to do — generating NanoIDs, reading timestamps, computing hashes — is provided by the Starlark stdlib with deterministic seeding from the operation envelope. Scripts cannot reach outside the sandbox.

### 3.8 Implementation Notes

**For the AI agent implementing Story 1.5 (`internal/substrate`):**

- `package mutation` — Go struct definitions for `Mutation`, `MutationBatch`, `Event`, `EventList`. JSON marshaling. Enum types for `op` ∈ `{create, update, tombstone}`.

**For the AI agent implementing Story 1.6 (Processor — Starlark Sandbox & JIT Hydration):**

- The Starlark sandbox exposes `nanoid.new()` and `nanoid.short()` builtins. Seed the NanoID generator with the operation's `requestId` for determinism under replay.
- Starlark's return value is parsed as `{mutations, events}` dict. Type-check each mutation and event against the Go struct shape before proceeding to step 6.
- A script returning anything other than the expected shape is rejected with `StarlarkExecutionFailed: InvalidReturnShape`.

**For the AI agent implementing Story 1.7 (Processor — DDL Validation & Atomic Batch):**

- At step 6: for each mutation in the batch, resolve DDL by class (per Contract #1 §1.5), validate `document.data` against DDL schema, enforce `permittedCommands` (Story 1.10/FR57), apply sensitivity constraints. Inject provenance fields (`createdAt`, `createdBy`, `createdByOp`, `lastModifiedAt`, `lastModifiedBy`, `lastModifiedByOp`).
- At step 6 batch-internal consistency: resolve all link endpoints and aspect host vertices; reject the entire operation with `SchemaViolation: DanglingReference` on any unresolved reference.
- At step 7: for each event, resolve event-type DDL by `event.class`. Events without registered DDL fail with `EventSchemaViolation: UndeclaredEventType`. Validate `event.data` against schema.
- At step 8: construct the NATS atomic batch with revision conditions per `mutation.op`:
  - `create` → revision condition = 0 (create-if-absent)
  - `update` → revision condition = `expectedRevision` if provided, else hydrated revision
  - `tombstone` → revision condition = hydrated revision
  - Plus the idempotency tracker write at `vtx.op.<requestId>` with revision condition = 0
- Step 8 atomic batch failure → reply with `RevisionConflict` (or `MetaLaneCollision` if on meta lane).

### 3.9 Substrate Batch Helpers and Committed Revisions

The substrate batch helpers are cancellation-aware. Both take a `context.Context` as their first argument:

```go
func (c *Conn) AtomicBatch(ctx context.Context, ops []BatchOp) (*BatchAck, error)
func (c *Conn) PublishBatch(ctx context.Context, ops []PublishOp) (*PublishBatchAck, error)
```

The context bounds the commit round trip and is checked before each fire-and-forget publish, so an upstream deadline or `SIGTERM`-driven cancellation propagates end-to-end during a batch commit. Each call site supplies the deadline appropriate to its lane SLA (the Processor commit path wraps `ctx` with its commit timeout per attempt).

**Committed revisions.** An atomic batch lands all N messages as a contiguous block of stream sequences. For a Core KV bucket, an entry's revision equals its stream sequence, so the per-key committed revision is derived from the commit ack's last sequence and batch size:

```
firstSeq := ack.Sequence - ack.BatchSize + 1
revisions[ops[i].Key] = firstSeq + uint64(i)   // for i in 0..N-1
```

`BatchAck.Revisions` carries this map. It is populated only when the contiguous-sequence invariant holds for the ack (`BatchSize == len(ops)`); otherwise it is nil and no revisions are fabricated.

**Reply propagation.** The Processor filters these revisions to the operation's business mutation keys (excluding the idempotency tracker key) and surfaces them on the accepted reply as `OperationReply.Revisions` (per Contract #2 §2.4). Clients use this map for read-your-own-writes polling against Core KV. Events carry no revisions — `PublishBatchAck` has no revisions field because events are not KV entries.
