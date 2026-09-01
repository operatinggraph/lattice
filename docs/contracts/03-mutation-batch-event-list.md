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

Both arrays may be empty (a no-op operation has zero mutations and zero events — useful for pure validation operations that succeed/fail without changing state). A return value not matching this shape is rejected fail-closed at parse (`ScriptFailed`), before commit.

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
| `document` | `create`, `update` | Document body. Includes `class`, `isDeleted`, and `data` (plus aspect/link-specific fields like `vertexKey`/`localName`/`sourceVertex`/`targetVertex`). **Provenance fields are NOT set by the script** — `createdAt`, `createdBy`, `createdByOp`, `lastModifiedAt`, `lastModifiedBy`, `lastModifiedByOp` are injected by the platform at commit from the current operation's actor; a script-supplied value is never honored. |
| `expectedRevision` | optional, `update` only | Revision condition for optimistic concurrency. If omitted, Processor uses the revision read during step 4 (Hydrate). Explicit override is reserved for compensating operations that need to force a specific revision check. |

### 3.3 Mutation Op Types

**`create`** — assert the key did not exist before this operation. If the key exists in any state (including tombstoned), the atomic batch is rejected.

**`update`** — assert the key existed before this operation at a known revision — `expectedRevision` if provided, else the revision read at step 4 (Hydrate). The Processor accepts updates targeting tombstoned documents: an update writes the whole value from the script's document, so a body that does not carry `isDeleted: true` restores the entity. This is the mechanism `UpgradePackage`'s re-add path uses to revive a key a prior uninstall tombstoned (Contract #8 §8.6). There is no separate `restore` op.

**`tombstone`** — assert the key existed before this operation (at its hydrated revision) and the script is marking it deleted. The platform sets `isDeleted: true` and updates `lastModifiedAt`/`lastModifiedBy`/`lastModifiedByOp`. The stored document is otherwise preserved whole. A tombstone mutation carries no `document`; one supplied is not honored. A tombstone can never modify, blank, or reclaim the stored body: content erasure is crypto-shredding (§3.10/§3.11). A tombstoned key is **not** freed: `create` still conflicts against it (the tombstone occupies the subject), so a **new entity** requires a new NanoID. The **same** key may be brought back to life for the **same** entity by an `update` (a body without `isDeleted: true`, per the `update` verb above) — which is what makes an entity's identity its key across a removal-and-return (Contract #8 §8.4 rule (3), §8.6).

**There is no `upsert`.** Operation-level idempotency is `requestId` + tracker-in-atomic-batch + step-2 dedup (Contract #4 §4.4) — mutations apply **at most once** across redeliveries. The three verbs describe the script's *intent for state transition*: a mismatch between the assertion and Core KV state surfaces as `RevisionConflict`, the correct outcome — the script's model of the world disagrees with reality, and masking that (the upsert semantic) would convert genuine conflicts into silent data loss.

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

**Event domain.** Every event class names a `<domain>` as its first segment. The published Event document carries a discrete **`domain`** field derived from the class's first segment — the class is the single source of truth, producers do not pass `domain` separately. The subject the outbox publishes on is `events.<domain>.<eventName>`, so the domain appears in both the subject and the document. Per-domain consumers (Loom) subscribe `events.<domain>.>`; because every class carries a domain, that filter always matches.

**Event payload convention:** Events SHOULD carry vertex key references rather than full document copies. Consumers hydrate context from Lens projections rather than expecting events to carry all required state. This keeps events lean, decouples producers from consumers' evolving context needs, and prevents events from becoming an alternate source of truth.

### 3.5 Batch-Internal Consistency Rules

Batch-internal referential integrity — link endpoints and aspect host vertices — is the responsibility of the operation's **DDL script**, enforced through the known-key-reads write path (§2.5), **not** a separate platform resolution pass:

**Endpoint/host validation is script-declared.** A `create` on a link key (`lnk.<t1>.<id1>.<name>.<t2>.<id2>`) or an aspect (`vtx.<type>.<id>.<localName>`) that must guarantee its endpoints / host vertex exist declares those vertices in `contextHint.reads`; the Processor hydrates them at step 4, and the script validates each (correct class, `isDeleted == false`, endpoint-touch) before emitting the mutation. An endpoint or host created by another mutation in the **same** MutationBatch is likewise the script's to sequence.

The platform performs **no** independent endpoint/host resolution and emits no dangling-reference error code. A dangling link is low-harm — readers filter `isDeleted`, and an absent endpoint reads as nothing — and convergence gaps are the Weaver's detect-and-recover domain, not a fail-closed platform reject. "Reads as nothing" is low-harm only for a reader that treats absence as absence. A script whose emitted link is the sole input to a projection presented as a **complete** list (a person's bound sign-in methods, an account's granted roles) MUST validate its endpoints under this section — for that reader an absent endpoint is a silent under-report, not a null.

**Tombstoning vertices with active aspects/links:** Tombstoning a vertex does NOT automatically tombstone its aspects or links — the Processor does not cascade (cascade is a business-logic choice, not the platform's). A script wanting cascade explicitly includes the dependent tombstone mutations in the same batch. Readers filter on `isDeleted` independently; tombstoning a vertex makes its key invisible to most queries even if its aspects remain.

**Within-batch ordering:** Mutations within a MutationBatch form a set, not a sequence. The atomic batch commits them all simultaneously. Scripts must declare what should be true after the operation; they do not declare ordered procedural steps.

### 3.6 Script-Generated Keys

When a script creates new entities, it generates their NanoIDs inline and reuses the full key strings
in subsequent mutations of the same batch:

```python
new_id = nanoid.new()                      # 20-char NanoID, custom alphabet (Contract #1)
identity_key = "vtx.identity." + new_id
# ...emit the vertex create, then aspects/links keyed on identity_key...
```

The Starlark stdlib provides `nanoid.new()` (20-char, primary keys) and `nanoid.short()` (8-char
display codes — NOT for primary keys). The Processor seeds the generator with the operation's
`requestId`, so replaying the same operation produces the same generated IDs.

### 3.7 Architectural Boundary — Starlark Never Touches NATS

Starlark scripts are pure functions: `(state, operation) → (mutations, events)`. They have no NATS handle. They do not publish events; they declare events for the Processor to publish. They do not write to KV; they declare mutations for the Processor to apply. Any apparent I/O — NanoIDs, timestamps, hashes — is stdlib with deterministic seeding from the operation envelope; scripts cannot reach outside the sandbox (NFR-E4). Sandbox detail: `docs/components/processor.md`.

### 3.9 Committed Revisions

**Reply propagation.** The accepted reply's `revisions` map (Contract #2 §2.4) covers **exactly the
operation's committed business mutation keys** — the idempotency tracker is excluded, and revisions
are never fabricated. Clients use the map for read-your-own-writes polling against Core KV. Events
carry no revisions — they are not KV entries.

#### 3.9.1 Atomic-batch size ceiling

A single operation's atomic batch is bounded by two independent limits (version gates:
`docs/vendors.md`), enforced fail-closed before any publish:

- **Mutation-count ceiling — at most 998 business mutations per operation.** A cascade that would
  exceed this (e.g. tombstoning a very-high-degree hub and all its links in one op) must be
  decomposed by the script/pattern author into multiple operations. A deployment **must not lower
  the underlying batch limit below 1000** messages.
- **Per-value byte ceiling — `max_payload`.** Each mutation's marshaled value (after commit-time
  provenance injection) must fit the server's negotiated `max_payload` (default **1 MiB**), minus a
  fixed headroom. Large binary/document payloads belong in the off-graph Object Store (Contract #7
  §7.2), **not** in a Core-KV aspect value.

Either breach is a **terminal `BatchTooLarge` rejection** (Contract #2 §2.6) — no redelivery, since a
redelivery of the same deterministic operation reproduces the identical over-limit batch. The reply's
`details` carry `reason` (`mutationCount` | `valueSize`), `limit`, `actual`, and (for `valueSize`)
the offending `key`.

A legitimate business operation that genuinely requires more than 998 mutations or a value above `max_payload` needs a saga/compensation decomposition; that pattern is deferred until a concrete consumer requires it.

### 3.10 Sensitive-aspect encryption at rest

An aspect whose aspect-type DDL declares `sensitive: true` (Contract #1 §sensitivity lookup; Contract #7
reserved `sensitive` aspect type) is stored in Core KV with its `data` **encrypted** (ciphertext), never
in plaintext. This is the storage-format invariant behind crypto-shredding (right-to-erasure on an
immutable ledger): destroying the **key holder's** key renders the ciphertext — in live KV and in the
JetStream history — permanently unrecoverable, at exactly the granularity that key has.

**Commit-path placement.** Encryption applies **after** validation (step 6) and **before** the
atomic commit (step 8):

1. Step 4 (hydrate) decrypts any sensitive aspect read into the Starlark context, so scripts operate on
   plaintext (Starlark never sees ciphertext or key material). A **soft-deleted** sensitive aspect is never
   decrypted, and its disposition depends on how it was declared. Under `reads` / `optionalReads` it is
   delivered with `isDeleted: true` and an **empty body** — the tombstone preserves the stored document
   whole (§3.2), so the retained ciphertext is scrubbed rather than handed on, and the script adjudicates
   the deletion through its own `isDeleted` filter exactly as it does for a non-sensitive aspect. Under
   `egressReads` the hydrate **fails**: a `$sensitiveRef` marker is a capability the bridge opens at the
   external boundary, and one over a dead aspect must not leave the Processor.
2. Step 6 (validate) validates schema / `permittedCommands` / `sensitiveAspectScope` against the
   **plaintext** mutation, exactly as for non-sensitive aspects.
3. After validation, for each mutation whose resolved DDL is `sensitive`, the Processor encrypts
   `mutation.data` with the **resolved key holder's** data-encryption key (DEK), replacing it with a
   ciphertext envelope `{ ct, nonce, keyId }`. If the resolved holder has no `<holderKey>.piiKey` aspect,
   the Processor lazily provisions one (the wrapped DEK reference — never key material) and adds it to the
   **same** atomic batch.
4. Step 8 commits ciphertext (and any new `piiKey`) atomically. Plaintext sensitive `data` never lands in
   Core KV.

**Key custody.** A sensitive aspect's `data` is encrypted under a **key holder's** data-encryption key
(DEK). The holder is a function of the aspect's **resolved aspect-type DDL** — never supplied by the
caller and never discovered by graph traversal. Two holder kinds exist:

- **`identity`** (the default when undeclared) — the holder is the aspect's own anchoring identity.
  Policy: **erase-on-request**. Its DEK is destroyed by `ShredIdentityKey`.
- **`retentionClass`** — the holder is a controller-declared retention-class vertex
  (`vtx.retentionclass.<NanoID>`), named by the DDL's `custody.retentionClass`. Policy:
  **erase-on-expiry**. Its DEK is destroyed by `ShredRetentionClassKey`, on the controller's retention
  schedule, not on a data subject's request.

**The external-egress boundary carries identity-held records only.** The bridge resolves a holder's
envelope from a lens that enumerates identity holders alone, so an egress ref for any other holder type
is refused, with the type named, at the site that authors the operation.

Every holder references only its **wrapped** DEK, from `<holderKey>.piiKey`, satisfying "key material
never in Core KV." Encryption is non-deterministic (random nonce) and is compatible with
last-writer-wins-by-revision and `requestId` idempotency (which key on the request, not on content).

**The ciphertext names its own key.** The stored envelope is `{ ct, nonce, keyId }`, where `keyId` is the
holder's vertex key and is bound as AEAD associated data. **Every decrypt resolves custody from `keyId`,
never by re-deriving it from the aspect's anchor or from a projected column.** A substituted `keyId`
therefore fails authenticated decryption rather than opening the record under another holder's key. A
consequence: **re-classifying a data class cannot orphan already-committed records** — each keeps the key
it was written under.

**Retention and erasure.** Destroying a key IS the erasure, at exactly the granularity the key has. The
two kinds are two clocks: `ShredIdentityKey` destroys a person's DEK and makes every aspect custodied by
that person unrecoverable, while a record custodied by a retention class **survives** — it becomes
pseudonymized (retained, with its subject's direct identifiers unrecoverable) rather than erased. A
retained record must not duplicate its subject's direct identifiers, or the subject's erasure is defeated
by that duplication. Conversely `ShredRetentionClassKey` makes every record in that class unrecoverable
regardless of any subject's erasure state.

**Erasure must reach the read models.** A key destruction is not complete when the key is destroyed; it is
complete when no projected read model still holds the plaintext. A Secure Lens projects `null` for a
column whose holder key is destroyed, and that null is **stable under reprojection** (a later
re-evaluation re-attempts the decrypt and fails the same way), so re-projecting the affected lenses is
convergent. Where the holder is not a vertex the lens binds — which is always true for a retention class
— the platform must **re-project** the lenses whose secure columns declare that holder type; a lazy
next-event scrub is not an erasure guarantee, because nothing enumerates or attests it.

**Readers.** Direct Core-KV readers observe ciphertext, and a general lens target receives the
ciphertext as-is — sensitive aspects are unreadable there without an explicit decryption seam. Plaintext is produced only by the Processor (for Starlark), by an explicit
Vault-decrypt consumer (a trusted tool, or the read-path-authorized Secure Lens), or by the **bridge's
external-egress unwrap** (§10.5 sensitive-ref params — plaintext bounded to the in-memory adapter call).
Destroying the holder's DEK — `ShredIdentityKey` for an `identity` holder, `ShredRetentionClassKey` for a
retention class — leaves no consumer able to decrypt.

**External-egress guard.** An operation that emits an `external.*`-domain event must not have **consumed**
readable sensitive data in the same execution — its script taking such a document from `state` or a
`kv.Read`, whether declared under `reads` / `optionalReads` or read lazily. The Processor rejects the
commit. Sensitive data reaches an external event only as a **sensitive-ref** hydrated under
`contextHint.egressReads` (Contract #2 §2.5 class (f)); the ref carries the at-rest ciphertext, never
plaintext.

The trigger is **consumption, not decryption** (keying on the decrypt would make a surplus declared
read an existence oracle — the same exposure class as Contract #2 §2.5's fault-site rule). And
"readable" is not "decrypted": a sensitive-classed aspect that was never encrypted at rest (no Vault
configured, so the write path never encrypted it) counts as consumed when read — the guard does not
depend on a crypto boundary being present to bind.

**Live-envelope rule.** A Vault-decrypt consumer resolves the **key holder's** envelope (the holder named
by the ciphertext's `keyId`) from the **current** `piiKey` state (the aspect, or its lens projection) **at
decrypt time — never from a stored or carried copy**. A shred rewrites `piiKey` to a shredded placeholder at the source; a frozen envelope
copy in a durable plane would out-live that rewrite and defeat crypto-shredding across a Vault restart.

**Ref-provenance rule.** A sensitive-ref is **authenticated at mint**: the Processor stamps every
`$sensitiveRef` it authors with a MAC binding
`{ref, requestId, ciphertext}` — `requestId` being the minting operation's, carried top-level on the
emitted event. The **external-egress unwrap consumer decrypts only through the ref-verified decrypt
RPC**, which recomputes the MAC before any decryption; because the MAC covers the ciphertext's `keyId`,
custody is **authenticated rather than re-derived**. An unverifiable ref (absent or mismatched MAC) is a
permanent data error — never decrypted, never retried. A ref is a per-execution artifact, not a durable
capability: a consumer never accepts a marker outside the event of the operation that minted it. The
wholesale decrypt RPC (no MAC) remains for the trusted-tool inspector class only. A sensitive-ref for a
**non-`identity`** holder is **refused** at hydration until the external-egress key-envelope read path
covers non-identity holders; the refusal is typed and loud, never a silent pass-through of raw
ciphertext.

**Reveal.** A decrypt request carrying no actor and no declared purpose is **denied** for a
non-`identity` holder. A retention-class record has no data subject whose grant scopes its disclosure, so
the wholesale trusted-tool decrypt RPC — which carries neither actor nor purpose — is not an
authorization path for it. The sanctioned read path is a read-path-authorized **Secure Lens**, where
custody answers "can this be decrypted at all" and the Protected/RLS/grant plane answers "which actor
sees this row."

### 3.11 Sensitive-object (blob) encryption at rest

The blob analog of §3.10. An object (the off-graph blob plane, Contract #7 §7.2 — `vtx.object.<oid>` +
`.content` aspect + bytes in `core-objects`) created with `sensitive: true` has its **bytes** stored as
**ciphertext**, encrypted client-side before they are streamed onto the §7.2 bytes plane. This makes a
document-PII blob (a lease PDF, an ID scan, a signature image) crypto-shreddable on the same immutable
ledger and under the **same §3.10 key holder** as aspect-PII. Blobs remain identity-custodied: the holder
for an object is always an identity.

**Envelope encryption (bulk bytes never reach the Vault).** A `sensitive` object is encrypted with a random
per-object Content Encryption Key (CEK) — `ciphertext = AES-256-GCM(CEK, nonce, plaintext)` — and the
**CEK**, not the bytes, is wrapped under the governing identity's §3.10 DEK
(`wrappedCEK = Vault.WrapKey(governingIdentity, CEK)`). The Vault handles only the small CEK; the bulk
bytes never leave the uploader. There is **no new key hierarchy**: that identity's §3.10 DEK (referenced
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

- 2026-07-22 — §3.3 tombstone body-preservation posture ratified.
- 2026-06-07 — §3.4 event-domain model ratified (`<domain>.<eventName>` required).
