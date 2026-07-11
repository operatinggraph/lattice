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
    "class": "identity.created",
    "data": {
        "identityKey": "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
        "createdBy": "vtx.identity.St6mP3qBn4rT8wYxK7Vc"
    }
}
```

| Field | Required | Purpose |
|-------|----------|---------|
| `class` | yes | Event type. MUST be `<domain>.<eventName>` — a **domain segment is required** (the first dot-segment), `eventName` in lowerCamelCase (e.g. `identity.created`, `orchestration.taskCompleted`, `rbac.roleAssigned`). The domain segment is validated at commit step 7; a dot-free class (no domain) is rejected. Event-type DDLs (`class: "meta.ddl.eventType"`) are a **package-owned** typed contract consumers rely on, but the Processor does **not** resolve or schema-validate a class against a registered event-type DDL at commit — step 7 enforces the `<domain>.<eventName>` shape only. Events are a typed contract; consumers (Loom, Weaver) depend on schema knowledge, and the **domain** is the partition key those consumers subscribe on (`events.<domain>.>`). |
| `data` | yes | Event payload. May be `{}` for parameterless events. The Processor does **not** schema-validate `event.data` against the event DDL at commit (see the `class` note — event schemas are a package-owned contract). |

**Event domain.** Every event class names a `<domain>` as its first segment. The Processor sets a discrete **`domain`** field on the published Event document (`internal/processor/step7_events.go` `Event.domain`) from the class's first segment — the class is the single source of truth, producers do not pass `domain` separately. The subject the outbox publishes on is `events.<domain>.<eventName>`, so the domain appears in both the subject and the document. Per-domain consumers (Loom) subscribe `events.<domain>.>`; because every class carries a domain, that filter always matches.

**Event payload convention:** Events SHOULD carry vertex key references rather than full document copies. Consumers hydrate context from Lens projections rather than expecting events to carry all required state. This keeps events lean, decouples producers from consumers' evolving context needs, and prevents events from becoming an alternate source of truth.

### 3.5 Batch-Internal Consistency Rules

Batch-internal referential integrity — link endpoints and aspect host vertices — is the responsibility of the operation's **DDL script**, enforced through the known-key-reads write-path (§2.5), **not** a separate platform step-6 resolution pass:

**Endpoint/host validation is script-declared.** A `create` on a link key (`lnk.<t1>.<id1>.<name>.<t2>.<id2>`) or an aspect (`vtx.<type>.<id>.<localName>`) that must guarantee its endpoints / host vertex exist declares those vertices in `contextHint.reads`; the Processor hydrates them at step 4, and the script validates each (correct class, `isDeleted == false`, endpoint-touch) before emitting the mutation. An endpoint or host created by another mutation in the **same** MutationBatch is likewise the script's to sequence.

The Processor performs **no** independent step-6 endpoint/host resolution and emits no dangling-reference error code. A dangling link is low-harm — readers filter `isDeleted`, and an absent endpoint reads as nothing — and convergence gaps are the Weaver's detect-and-recover domain, not a fail-closed platform reject.

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
                "class": "identity.created",
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
- At step 6 batch-internal consistency: **no separate platform pass** — link-endpoint / aspect-host integrity is the DDL script's responsibility via declared reads (§3.5), with Weaver detect-and-recover as the convergence backstop; the Processor emits no `DanglingReference` code.
- At step 7: reject any event whose `class` is not `<domain>.<eventName>` (no dot, or an empty domain/eventName segment) — a domain segment is required. Set the Event document's `domain` field to the class's first segment. The Processor does **not** resolve an event-type DDL or schema-validate `event.data` at commit — event schemas are a package-owned contract; there is no `UndeclaredEventType` / `EventSchemaViolation` reject.
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

#### 3.9.1 Atomic-batch size ceiling

A single operation's atomic batch is bounded by two independent NATS limits (matched to the platform pin, **NATS 2.14** — `go.mod`, `docs/vendors.md`), enforced fail-closed by the substrate batch helpers before any publish:

- **Message-count ceiling — 1000 messages per batch.** NATS abandons an over-limit atomic batch (ADR-50, *JetStream Batch Publishing*; server `err_code 10199`). 1000 is the NATS 2.14 server **default** (`streamDefaultMaxAtomicBatchSize`), overridable via `jetstream_limits.max_batch_size`; a deployment **must not set it below 1000** (the client-side guard would become looser than the server), and the reference `deploy/nats-server.conf` sets no override. The Processor's batch is `business mutations + the idempotency tracker + (optional) the transactional-outbox aspect`, so a single operation may emit **at most 998 business mutations** (`MaxBatchMessages − 2`). A cascade that would exceed this (e.g. tombstoning a very-high-degree hub and all its links in one op) must be decomposed by the script/pattern author into multiple operations.
- **Per-value byte ceiling — `max_payload`.** Each batch member is an ordinary NATS message subject to the server's negotiated `max_payload` (NATS default **1 MiB**). The substrate rejects a mutation whose marshaled value (after commit-time provenance injection) exceeds `max_payload` minus a fixed header/provenance headroom. Large binary/document payloads belong in the off-graph Object Store (Contract #7 §7.2), **not** in a Core-KV aspect value.

Both bounds are checked in `AtomicBatch`/`PublishBatch` before the batch is published (`substrate.ErrBatchTooLarge` / `ErrValueTooLarge`). At step 8 the Processor maps either to a **terminal `BatchTooLarge` rejection** (Contract #2 §2.6) — no redelivery, since a redelivery of the same deterministic operation reproduces the identical over-limit batch. The reply's `details` carry `reason` (`mutationCount` | `valueSize`), `limit`, `actual`, and (for `valueSize`) the offending `key`.

A legitimate business operation that genuinely requires more than 998 mutations or a value above `max_payload` needs a saga/compensation decomposition; that pattern is deferred until a concrete consumer requires it.

### 3.10 Sensitive-aspect encryption at rest

An aspect whose aspect-type DDL declares `sensitive: true` (Contract #1 §sensitivity lookup; Contract #7
reserved `sensitive` aspect type) is stored in Core KV with its `data` **encrypted** (ciphertext), never
in plaintext. This is the storage-format invariant behind crypto-shredding (right-to-erasure on an
immutable ledger): destroying the per-identity key renders the ciphertext — in live KV and in the
JetStream history — permanently unrecoverable.

**Commit-path placement.** Encryption is Processor commit-path middleware, applied **after** step-6
validation and **before** the step-8 atomic commit:

1. Step 4 (hydrate) decrypts any sensitive aspect read into the Starlark context, so scripts operate on
   plaintext (Starlark never sees ciphertext or key material).
2. Step 6 (validate) validates schema / `permittedCommands` / `sensitiveAspectScope` against the
   **plaintext** mutation, exactly as for non-sensitive aspects.
3. After validation, for each mutation whose resolved DDL is `sensitive`, the Processor encrypts
   `mutation.data` with the anchoring identity's data-encryption key (DEK), replacing it with a ciphertext
   envelope `{ ct, nonce, keyId }`. If the anchoring identity has no `vtx.identity.<id>.piiKey` aspect, the
   Processor lazily provisions one (the wrapped DEK reference — never key material) and adds it to the
   **same** atomic batch.
4. Step 8 commits ciphertext (and any new `piiKey`) atomically. Plaintext sensitive `data` never lands in
   Core KV.

**Key custody.** The per-identity DEK is wrapped by an external key-management backend (the Vault); only
the **wrapped** DEK is referenced from `piiKey`, satisfying "key material never in Core KV." Encryption is
non-deterministic (random nonce) and is compatible with last-writer-wins-by-revision and `requestId`
idempotency (which key on the request, not on content).

**Readers.** Direct Core-KV readers observe ciphertext. The Refractor's default projection path copies the
ciphertext as-is — so sensitive aspects are unreadable at general lens targets without an explicit
decryption seam. Plaintext is produced only by the Processor (for Starlark), by an explicit
Vault-decrypt consumer (a trusted tool, or the read-path-authorized Secure Lens), or by the **bridge's
external-egress unwrap** (§10.5 sensitive-ref params — plaintext bounded to the in-memory adapter call).
`ShredIdentityKey` destroys the DEK, after which no consumer can decrypt.

**External-egress guard.** An operation that emits an `external.*`-domain event must not have decrypted
sensitive plaintext in the same execution (via `reads`, `optionalReads`, or a lazy `kv.Read`) — the
Processor rejects the commit. Sensitive data reaches an external event only as a **sensitive-ref**
hydrated under `contextHint.egressReads` (Contract #2 §2.5 class (f)); the ref carries the at-rest
ciphertext, never plaintext.

**Live-envelope rule.** A Vault-decrypt consumer resolves the identity's key envelope from the
**current** `piiKey` state (the aspect, or its lens projection) **at decrypt time — never from a stored
or carried copy**. A shred rewrites `piiKey` to a shredded placeholder at the source; a frozen envelope
copy in a durable plane would out-live that rewrite and defeat crypto-shredding across a Vault restart.

### 3.11 Sensitive-object (blob) encryption at rest

The blob analog of §3.10. An object (the off-graph blob plane, Contract #7 §7.2 — `vtx.object.<oid>` +
`.content` aspect + bytes in `core-objects`) created with `sensitive: true` has its **bytes** stored as
**ciphertext**, encrypted client-side before they are streamed onto the §7.2 bytes plane. This makes a
document-PII blob (a lease PDF, an ID scan, a signature image) crypto-shreddable on the same immutable
ledger and under the **same per-identity key** as §3.10's aspect-PII.

**Envelope encryption (bulk bytes never reach the Vault).** A `sensitive` object is encrypted with a random
per-object Content Encryption Key (CEK) — `ciphertext = AES-256-GCM(CEK, nonce, plaintext)` — and the
**CEK**, not the bytes, is wrapped under the governing identity's §3.10 DEK
(`wrappedCEK = Vault.WrapKey(governingIdentity, CEK)`). The Vault handles only the small CEK; the bulk
bytes never leave the uploader. There is **no new key hierarchy**: the §3.10 per-identity DEK (referenced
from `vtx.identity.<id>.piiKey`, the *wrapped* DEK, never key material) is the only secret, and
`ShredIdentityKey` already destroys it.

**Storage format.** The `.content` aspect (written through the Processor by `AttachObject`, P2) carries the
envelope alongside the existing reference metadata:

```
vtx.object.<oid>.content = {
    digest, size, contentType, storeName,                          # digest = PLAINTEXT digest (post-decrypt integrity)
    sensitive:  true,
    encryption: { algo: "AES-256-GCM", nonce, wrappedCEK, keyId }  # keyId = governing identity's piiKey reference
}
```

`wrappedCEK`/`nonce`/`keyId` are safe in plaintext in Core KV — a wrapped CEK is inert without the identity
DEK, exactly as §3.10's `{ ct, nonce, keyId }` envelope is.

**Content-addressing.** A `sensitive` object is **not** cross-identity content-addressed: its oid is
identity-salted — `oid = sha256NanoID("object:" + keyId + ":" + digest)` — so identical plaintext from two
identities yields **distinct** vertices (no shared-ownership linkage; no cross-identity PII linkage leak),
while a same-identity re-upload still dedups (deterministic oid, same governing identity). A non-sensitive
object is unchanged: `oid = sha256NanoID("object:" + digest)`, content-addressed, plaintext bytes.

**Readers (opt-in decrypt; ciphertext-safe by default).** A direct bytes reader observes ciphertext; the
default object-serve path streams ciphertext (its existing `octet-stream`/`attachment` anti-XSS posture), so
a `sensitive` object is unreadable without an explicit decrypt — no read-path authorization required for the
default path (the §3.10 posture). Plaintext is produced only by an authorized Vault-unwrap consumer (a
trusted tool, or the read-path-authorized Secure Lens): `CEK = Vault.UnwrapKey(keyId, wrappedCEK)`, then
local AES-256-GCM decrypt with GCM-tag **and** plaintext-`digest` verification.

**Erasure.** `ShredIdentityKey(identity)` destroys the §3.10 DEK; thereafter no `wrappedCEK` wrapped under
it can be unwrapped, so every one of that identity's `sensitive` blobs — in live `core-objects` and in any
backup — is permanent gibberish. The guarantee is key-destruction, not byte-deletion: a shredded blob is
inert ciphertext, reclaimed by the ordinary ownership GC (`objectLiveness` → `TombstoneObject` →
`object-store-manager`) when its ownership reaches zero — there is no blob-specific shred path.

## Revision history

| Date | Change |
|------|--------|
| 2026-06-07 | **Event-domain model (Andrew-ratified, folded into Story 8.2).** §3.4: an event `class` MUST be `<domain>.<eventName>` — a domain segment (the first dot-segment) is now **required** and validated at commit step 7; a dot-free class is rejected. The published Event document carries a discrete `domain` field set by the Processor from the class's first segment (single source of truth = the class). Subject stays `events.<domain>.<eventName>`; per-domain consumers subscribe `events.<domain>.>`. Examples re-cased from PascalCase (`identityCreated`) to `<domain>.<eventName>` (`identity.created`). No envelope-shape change beyond the additive `domain` field. |
