---
status: 'complete'
contractsCompleted: [1, 2, 3, 4, 5, 6, 7]
contractsTotal: 7
completedDate: '2026-05-12'
project_name: 'Lattice'
user_name: 'Andrew'
date: '2026-04-11'
purpose: 'Frozen data contracts for Phase 1 implementation. AI agents implement from this document.'
relatedDocs:
  - "lattice-architecture.md"
  - "prd.md"
---

# Lattice — Data Contracts

This document defines the frozen data shapes that Phase 1 implementation depends on. It is the **single source of truth** for keys, document envelopes, DDL structure, and operation contracts. Every AI agent implementing a Phase 1 story consults this document before touching any data shape.

**Companion documents:**
- `lattice-architecture.md` — explains *why* the platform is shaped this way
- `prd.md` — explains *what* the platform must do

This document specifies **what exactly** the data looks like. Architecture explains rationale; PRD explains requirements; this document binds them to concrete shapes.

**Authoring principle (locked):**
> The platform's meta-model is expressed using the same primitives the platform offers to its users — vertices, aspects, and links. The key is an opaque address; meaning lives in the document. Validation is permissive by default; declarations enable enforcement.

---

## Contract #1 — Addressing Model & Document Envelope

### 1.1 Core KV Key Patterns

Three key shapes are valid in Core KV. No other shapes are permitted.

| Entity | Pattern | Segments | Example |
|--------|---------|----------|---------|
| Vertex | `vtx.<type>.<id>` | 3 | `vtx.identity.Hj4kPmRtw9nbCxz5vQ2y` |
| Aspect | `vtx.<type>.<id>.<localName>` | 4 | `vtx.identity.Hj4kPmRtw9nbCxz5vQ2y.email` |
| Link | `lnk.<type1>.<id1>.<localName>.<type2>.<id2>` | 6 | `lnk.lease.Lk2Pn6mQrtwzKbcXvP3T.heldBy.identity.Hj4kPmRtw9nbCxz5vQ2y` |

**Field definitions:**

- **`<type>`** — a single lowercase identifier matching `[a-z][a-z0-9]*`. The type is a coarse routing/filtering category. Fine-grained classification lives in the document's `class` field.
- **`<id>`** — a NanoID generated per the architecture's locked specification in `lattice-architecture.md` §Entity ID Generation: **20 characters drawn from a custom 58-character alphabet that excludes visually ambiguous characters** (`I`, `l`, `O`, `0`). This applies to runtime entities, `op` trackers (whose IDs match the operation's `requestId`), and `meta` meta-vertices uniformly. Deterministic readable IDs are NOT permitted in primary keys — meta-vertex discovery is by `class` + canonicalName aspect, not by key. A separate **8-character NanoID** form from the same alphabet is reserved for human-facing short codes (display references, verbal sharing) and MUST NOT be used as a primary key.
- **`<localName>`** — for aspects and links: a lowercase camelCase identifier matching `[a-z][a-zA-Z0-9]*`. Underscore prefix (`_name`) is reserved for platform-generated system metadata; business DDL must not use underscore-prefixed local names.
- **Link directionality** — every link DDL declares its canonical name and direction at **design time**, encoding the typical graph-growth pattern: the link's source side (`<typeA>.<idA>`) is the vertex that is *typically added later* in the graph's lifetime; the target side (`<typeB>.<idB>`) is the vertex that *typically pre-exists* (it was already in the graph when the source side appeared). The convention is semantic, not algorithmic — there is no auto-sort by type, by NanoID, or by `createdAt`. Examples:
  - `lnk.identity.<idA>.holdsRole.role.<idB>` — role vertices are typically seeded (by package install or earlier provisioning) before identity vertices, which are added in flight. The link points from the later-arriving identity to the pre-existing role.
  - `lnk.permission.<idA>.grantedBy.role.<idB>` — both endpoints are seeded by package install in close proximity, but the package designer picks `permission → role` as the canonical direction (reads as "permission granted by role"). Once the link DDL is authored, that direction is fixed.
  - `lnk.identity.<idA>.reportsTo.identity.<idB>` — both endpoints are type `identity`, but the manager identity pre-exists the report. The link points from the report (later-added) to the manager (pre-existing). Same-type links follow the same conceptual rule; runtime callers know which endpoint is which from the operation's semantics, not from string comparison.

  Substrate is **direction-agnostic**: `substrate.LinkKey(type1, id1, linkName, type2, id2)` constructs the key in caller-provided order; the substrate does NOT validate or re-sort. The DDL's Starlark script (or other authorized caller) is responsible for emitting endpoints in the DDL-declared direction. The link DDL's `.description` aspect SHOULD document its directional semantics for downstream consumers (FR19 self-description aspect).

  **Pre-Story-1.4 framing superseded.** Earlier drafts described this as "`<id1>` is the younger vertex (later `createdAt`), `<id2>` is the older." That formulation conflated runtime ordering with design intent and broke down for cases where the conceptual ordering doesn't match `createdAt` (e.g., a manager seeded later than a report through bulk import). The convention is a **DDL authoring rule**, not a runtime invariant — once authored, direction is encoded in the link DDL and instances inherit it.

**Parser disambiguation rule:**
- Count segments by dot-splitting the key. 3 segments → vertex. 4 segments → aspect. 6 segments → link. Any other segment count is malformed and rejected at write time.
- Vertex `<id>` is the third segment; aspect's vertex key is segments 1–3; link endpoints are segments 1–3 (younger) and 4–6 (older, after the linkName).

**Case sensitivity:**
- NATS subjects are case-sensitive. Keys are case-sensitive at storage level.
- DDL validation rejects mixed-case types and localNames at write time; legitimate paths cannot produce mixed-case keys.

**Soft-delete addressing:**
- Soft-deleted entities retain their keys. Deletion is the `isDeleted: true` flag on the document, not a key change. Every reader independently filters on `isDeleted` (Processor enforces in commit path; Refractor enforces in CDC handlers).

### 1.2 Reserved Types

Only two type names are reserved by the platform:

- **`meta`** — schema and configuration meta-entities (DDL, Lens definitions, event schemas, system configuration). Distinguished by `class` field. Low-churn, durable, replicated to every Processor's DDL cache.
- **`op`** — idempotency trackers. Key ID matches operation `requestId`. High-churn, short-lived (24h idempotency horizon). Separate from `meta` for retention/archival policy isolation.

Operator-defined DDL **must not** register vertex types named `meta` or `op`. Attempting to do so is rejected by Processor at meta-DDL commit time.

Other names that might *look* like they should be reserved but aren't:
- `lens`, `event`, `ddl`, `actor` — these are *flavors of `meta`*, distinguished by the document's `class` field (`meta.lens`, `meta.event.<name>`, `meta.ddl.vertexType`, etc.)
- Internal service actors (Processor, Loom, Weaver) — these are **regular `identity` vertices** with `class: "identity.system.<service>"`. Their root-equivalent capability is granted by graph topology, not by key prefix.

### 1.3 Document Envelope

Every Core KV value (vertex, aspect, or link) is a JSON document carrying a uniform envelope plus type-specific payload.

**Universal envelope fields (required on every document):**

```json
{
  "key": "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
  "class": "identity",
  "isDeleted": false,
  "createdAt": "2026-04-11T14:32:18.142Z",
  "createdBy": "vtx.identity.St6mP3qBn4rT8wYxK7Vc",
  "createdByOp": "vtx.op.Lk2Pn6mQrtwzKbcXvP3T",
  "lastModifiedAt": "2026-04-11T14:32:18.142Z",
  "lastModifiedBy": "vtx.identity.St6mP3qBn4rT8wYxK7Vc",
  "lastModifiedByOp": "vtx.op.Lk2Pn6mQrtwzKbcXvP3T",
  "data": {}
}
```

**Field semantics:**

| Field | Type | Mutability | Purpose |
|-------|------|------------|---------|
| `key` | string | immutable | Echo of the KV key. Useful in logs, exports, and event payloads where the key isn't always carried in the envelope. |
| `class` | string | mutable | Type/kind classification used for DDL lookup. Dot-separated hierarchical descriptor permitted (e.g., `identity.ai.onboarding-assistant`). DDL lookup is exact match against canonical name. |
| `isDeleted` | boolean | mutable | Soft-delete tombstone. Default `false`. Readers filter independently. |
| `createdAt` | string (ISO 8601) | immutable | Document creation timestamp (set by Processor at commit step 8). |
| `createdBy` | string (vertex key) | immutable | Identity vertex of the actor who created this entity. |
| `createdByOp` | string (op vertex key) | immutable | The operation tracker that committed creation. |
| `lastModifiedAt` | string (ISO 8601) | mutable | Timestamp of most recent commit affecting this document. |
| `lastModifiedBy` | string (vertex key) | mutable | Identity vertex of the actor who most recently modified. |
| `lastModifiedByOp` | string (op vertex key) | mutable | Op tracker of the most recent mutation. |
| `data` | object | mutable | Optional type-specific payload. Many entities (especially identity vertices) leave this `{}` because all interesting state lives in aspects. |

**Why we echo `key` in the document:**
The key is part of the addressing fabric but it isn't always carried alongside the document in event payloads, exports, or log lines. Echoing it makes documents self-identifying when read out of context.

**Why we DO NOT include a `revision` field:**
NATS KV maintains revision numbers as a property of the storage layer. Echoing them in the document creates an immediate consistency problem (the echoed value lags the actual revision by one). Clients that need the revision read it from the KV metadata, not from the document.

**Aspect-specific envelope extension:**

Aspects add two derived fields for traversal convenience:

```json
{
  "key": "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y.email",
  "vertexKey": "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
  "localName": "email",
  "class": "email",
  "isDeleted": false,
  ...universal envelope fields...,
  "data": { "value": "andrew@example.com", "verified": true }
}
```

| Field | Purpose |
|-------|---------|
| `vertexKey` | Pointer back to the host vertex. Derived from key segments 1–3; redundant with the key but useful for indexing and event payloads. |
| `localName` | The local addressing name (key segment 4). Used for uniqueness within the host vertex's aspect namespace. May or may not match `class`. |

**Link-specific envelope extension:**

Links add three fields:

```json
{
  "key": "lnk.lease.Lk2Pn6mQrtwzKbcXvP3T.heldBy.identity.Hj4kPmRtw9nbCxz5vQ2y",
  "youngerVertex": "vtx.lease.Lk2Pn6mQrtwzKbcXvP3T",
  "olderVertex": "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
  "localName": "heldBy",
  "class": "heldBy",
  "isDeleted": false,
  ...universal envelope fields...,
  "data": {}
}
```

| Field | Purpose |
|-------|---------|
| `youngerVertex` | Pointer to younger endpoint (key segments 1–3). |
| `olderVertex` | Pointer to older endpoint (key segments 4–6). |
| `localName` | The link's local name (key segment 4 of the link key — the middle segment between the two vertex keys). |

### 1.4 Reserved Underscore-Prefixed Local Names

Aspect and link `localName` values starting with `_` are reserved for platform-generated system metadata. Business DDL must not register classes that would naturally suggest underscore-prefixed local names, and write operations may not produce underscore-prefixed local names from Starlark scripts (Processor rejects at commit step 6).

The platform may, in future iterations, introduce conventional underscore-prefixed names. For Phase 1, the only reservation is the namespace itself; no specific names are pre-allocated.

### 1.5 DDL Lookup at Commit Time

When the Processor validates a mutation (commit step 6), it resolves the DDL for the affected entity by **class-based lookup against the DDL cache**.

**Lookup algorithm:**

1. Read the document's `class` field.
2. Determine the DDL kind from the entity being mutated (vertex / aspect / link).
3. Query the DDL cache: "find a meta-vertex with `class: 'meta.ddl.<kind>Type'` and a `canonicalName` aspect equal to the document's `class`."
4. If found → validate against the resolved schema, enforce `permittedCommands`, apply sensitivity constraints (for aspects).
5. If not found → accept the mutation with no schema validation, no `permittedCommands` enforcement, no sensitivity constraint. (Permissive-by-default.)

**Class lookup is exact match.** Hierarchical class strings (e.g., `identity.ai.onboarding-assistant`) match only DDLs with exactly that canonical name. To validate AI-specific identities under their own rules, operators register a DDL with canonical name `identity.ai.onboarding-assistant`. To use the generic identity DDL, set `class: "identity"`. Prefix matching is not part of Phase 1.

**Default class:** If a write submission omits the `class` field, the Processor uses the entity's local name (aspect/link key segment) or the type (vertex key segment) as the implicit class. This keeps the simple case trivial — `vtx.identity.<id>.email` without explicit class defaults to `class: "email"`.

**Class uniqueness:** Within each DDL kind, canonical names must be globally unique:
- Aspect-type DDLs: unique `canonicalName` across all `class: "meta.ddl.aspectType"` meta-vertices
- Link-type DDLs: unique `canonicalName` across all `class: "meta.ddl.linkType"` meta-vertices
- Vertex-type DDLs: unique `canonicalName` across all `class: "meta.ddl.vertexType"` meta-vertices
- Event-type DDLs: unique `canonicalName` across all `class: "meta.ddl.eventType"` meta-vertices

Names can collide *across* kinds (an aspect class `email` and a link class `email` could coexist; their addresses are syntactically distinct). Processor enforces uniqueness within kind at meta-DDL commit time.

### 1.6 Permissive-by-Default

**Operations authorized by the Capability Lens** can write any vertex, aspect, or link to any addressable location, subject to:
- Key shape validity (3/4/6 segments)
- Reserved type protection (`meta`, `op` cannot be registered as business types)
- Underscore-prefix protection (cannot write underscore-prefixed local names from Starlark)
- DDL constraints **only when DDL is found by class lookup**

**Declarations enable enforcement, not existence.** Writing an undeclared aspect or link does not require prior DDL authorship. The platform stores the data; downstream Lens projections that depend on schema knowledge simply don't project undeclared aspects until DDL exists.

**Consequences for FR57 (write-scope per DDL):** `permittedCommands` enforcement applies only to declared types. Undeclared writes bypass FR57's enforcement because there's no DDL to enforce from. This is consistent with the permissive model — operators who want strict write-scope register DDL with `permittedCommands` aspects.

**Consequences for sensitive aspects (PRD Item 6):** Sensitive-aspect anchoring (must attach to identity-anchored vertex) applies only when the aspect's DDL is found by class lookup and declares `sensitive: true`. Undeclared aspects have no enforced sensitivity. Operators handling PII data must register DDL with the sensitive flag.

**Consequences for the bypass test suite (NFR-S2, Phase 1 Gate 2):** The "DDL schema violation" bypass category applies to *declared* types. The other three categories (direct KV write, stream publish outside `ops.*`, Starlark I/O escape) are unchanged — they're enforced regardless of DDL state.

**Why this is the right default:**
- The Capability Lens is the platform's primary trust boundary; schema rigidity is a quality-of-life feature on top of it
- AI-driven self-improvement (FR31–34, FR53–54) requires that experimental aspects can be written before formal DDL exists
- Schema-flexible graph databases (Neo4j, ArangoDB) have demonstrated this model at scale
- Lens authors and AI agents discover schema by observing actual data, supplemented by registered DDL where available

**Cardinality, mandatoryness, target-type restrictions, and vertex-type-specific constraints** are NOT part of DDL. They are business-logic concerns enforced by Starlark scripts on the operations that mutate the affected entities. This is consistent with architectural principle P4 (Starlark enforces single-operation invariants).

### 1.7 Meta-DDL Structure

Each DDL is a thin meta-vertex of type `meta` with details expressed via its own aspects. The platform's meta-model uses the same VAL primitives as the business model.

**Vertex-type DDL example — the DDL for `identity`:**

```
vtx.meta.Hj4kPmRtw9nbCxz5vQ2y
  envelope: { class: "meta.ddl.vertexType", isDeleted: false, ... }
  data: {}

# Aspects of the DDL meta-vertex:
vtx.meta.Hj4kPmRtw9nbCxz5vQ2y.canonicalName
  envelope: { class: "canonicalName", ... }
  data: { value: "identity" }

vtx.meta.Hj4kPmRtw9nbCxz5vQ2y.vertexSchema
  envelope: { class: "vertexSchema", ... }
  data: { jsonSchema: { /* JSON Schema for the data field of identity vertices */ } }

vtx.meta.Hj4kPmRtw9nbCxz5vQ2y.description
  envelope: { class: "description", ... }
  data: { text: "A person, organization, or AI agent capable of authoring operations." }

vtx.meta.Hj4kPmRtw9nbCxz5vQ2y.permittedCommands
  envelope: { class: "permittedCommands", ... }
  data: { commands: ["CreateIdentity", "FlagIdentity", "MergeIdentity", "ClaimIdentity"] }
```

**Aspect-type DDL example — the DDL for `email`:**

```
vtx.meta.b9pn2k7qmrz9px5tvwjc
  envelope: { class: "meta.ddl.aspectType", isDeleted: false, ... }
  data: {}

vtx.meta.b9pn2k7qmrz9px5tvwjc.canonicalName     → data: { value: "email" }
vtx.meta.b9pn2k7qmrz9px5tvwjc.schema            → data: { jsonSchema: {...} }
vtx.meta.b9pn2k7qmrz9px5tvwjc.sensitive         → data: { value: true }
vtx.meta.b9pn2k7qmrz9px5tvwjc.description       → data: { text: "An email address with optional verification metadata." }
vtx.meta.b9pn2k7qmrz9px5tvwjc.permittedCommands → data: { commands: ["CreateIdentity", "UpdateIdentityContact", "ClaimIdentity"] }
```

**Link-type DDL example — the DDL for `heldBy`:**

```
vtx.meta.q9px5tvwjcfb3pn2k7mr
  envelope: { class: "meta.ddl.linkType", isDeleted: false, ... }
  data: {}

vtx.meta.q9px5tvwjcfb3pn2k7mr.canonicalName     → data: { value: "heldBy" }
vtx.meta.q9px5tvwjcfb3pn2k7mr.schema            → data: { jsonSchema: {/* schema for link data field */} }
vtx.meta.q9px5tvwjcfb3pn2k7mr.description       → data: { text: "A holder relationship — links a holding entity to the entity it holds." }
vtx.meta.q9px5tvwjcfb3pn2k7mr.permittedCommands → data: { commands: ["CreateLease", "TransferLease"] }
```

**Event-type DDL example — the DDL for `identityClaimed`:**

```
vtx.meta.w5tvwjcfb3pn2k7mrq9p
  envelope: { class: "meta.ddl.eventType", isDeleted: false, ... }
  data: {}

vtx.meta.w5tvwjcfb3pn2k7mrq9p.canonicalName     → data: { value: "identityClaimed" }
vtx.meta.w5tvwjcfb3pn2k7mrq9p.schema            → data: { jsonSchema: {...} }
vtx.meta.w5tvwjcfb3pn2k7mrq9p.description       → data: { text: "Emitted when an unclaimed identity is bound to a registered account." }
```

**Discovery and bootstrap:**
- DDL meta-vertices are NOT addressable by deterministic key (their IDs are NanoIDs).
- Discovery is by class-based lookup against the Processor's in-memory DDL cache, built at startup by scanning `vtx.meta.>` CDC and maintained incrementally via CDC updates.
- The platform ships with **primordial meta-vertices** that describe the meta-meta layer (the DDL for `meta.ddl.vertexType`, `meta.ddl.aspectType`, etc.). These are seeded by `make up` and are not authored through the write path. Their NanoIDs are fixed for any given platform version.

### 1.8 Worked Examples

**Example: Identity vertex with multiple emails**

```
# The identity vertex itself — thin
vtx.identity.St6mP3qBn4rT8wYxK7Vc
  envelope:
    key: "vtx.identity.St6mP3qBn4rT8wYxK7Vc"
    class: "identity"
    isDeleted: false
    createdAt: "2026-04-11T10:00:00Z"
    createdBy: "vtx.identity.staff-bootstrap"
    createdByOp: "vtx.op.Rm7q3pntwzkfbcxv5p9j"
    lastModifiedAt: "2026-04-11T10:00:00Z"
    lastModifiedBy: "vtx.identity.staff-bootstrap"
    lastModifiedByOp: "vtx.op.Rm7q3pntwzkfbcxv5p9j"
  data: {}

# Work email aspect — class identifies the schema
vtx.identity.St6mP3qBn4rT8wYxK7Vc.workEmail
  envelope:
    key: "vtx.identity.St6mP3qBn4rT8wYxK7Vc.workEmail"
    vertexKey: "vtx.identity.St6mP3qBn4rT8wYxK7Vc"
    localName: "workEmail"
    class: "email"
    isDeleted: false
    createdAt: "..."
    ...
  data: { value: "andrew@lattice.example", verified: true }

# Personal email aspect — same class, different localName
vtx.identity.St6mP3qBn4rT8wYxK7Vc.personalEmail
  envelope:
    class: "email"
    localName: "personalEmail"
    ...
  data: { value: "andrew@home.example", verified: false }
```

Both aspects validate against the `email` aspect-type DDL. Both inherit `sensitive: true`. Both subject to the same `permittedCommands`. The vertex has two emails without needing two DDL definitions.

**Example: Lease held by identity (link)**

```
# The lease vertex
vtx.lease.Lk2Pn6mQrtwzKbcXvP3T
  envelope: { class: "lease", isDeleted: false, ... }
  data: {}

# Lease was created after the identity → lease is younger
lnk.lease.Lk2Pn6mQrtwzKbcXvP3T.heldBy.identity.St6mP3qBn4rT8wYxK7Vc
  envelope:
    key: "lnk.lease.Lk2Pn6mQrtwzKbcXvP3T.heldBy.identity.St6mP3qBn4rT8wYxK7Vc"
    youngerVertex: "vtx.lease.Lk2Pn6mQrtwzKbcXvP3T"
    olderVertex: "vtx.identity.St6mP3qBn4rT8wYxK7Vc"
    localName: "heldBy"
    class: "heldBy"
    isDeleted: false
    ...
  data: {}
```

**Example: Permissive write of undeclared aspect**

```
# AI agent records an observation about an identity — no DDL exists for "anomalyFlag"
vtx.identity.St6mP3qBn4rT8wYxK7Vc.anomalyFlag
  envelope:
    class: "anomalyFlag"
    ...
  data: { reason: "Duplicate phone number with another identity", confidence: 0.87 }

# Processor at commit step 6:
#   - DDL cache lookup for class "anomalyFlag" → not found
#   - No schema validation, no permittedCommands enforcement, no sensitivity check
#   - Mutation committed (operation-level Capability Lens check already passed)
#   - Lens projections that don't know about "anomalyFlag" ignore it
#   - Later, if an operator adds a DDL with canonicalName "anomalyFlag", subsequent writes will be validated
```

### 1.9 Implementation Notes

**For the AI agent implementing Story 1.5 (`internal/substrate`):**

The substrate package must export:

- `package keys` — pattern constants, parsers, builders for vtx/aspect/link keys. Functions like `BuildVertexKey(type, id) string`, `ParseAspectKey(key string) (vertexKey, localName string, err error)`, `IsVertexKey(key string) bool`. Pure functions, no NATS dependency.
- `package envelope` — Go struct definitions for the universal envelope (`Envelope`, `AspectEnvelope`, `LinkEnvelope`), JSON marshal/unmarshal with strict field validation. Constants for required field names.
- `package nanoid` — NanoID generator producing **20-character IDs from the custom 58-character alphabet** (excludes `I`, `l`, `O`, `0`) per `lattice-architecture.md` §Entity ID Generation. Primary function `New() string` returns the 20-char form for primary keys. Secondary function `NewShort() string` returns an 8-char form for human-facing display codes only (MUST NOT be used as a primary key — substrate package callers that mis-use it should be caught by lint or panic). Substrate package tests MUST include collision-rate validation against the published alphabet and length spec.
- `package classlookup` — helper to extract effective class from a document (uses explicit `class` if present, falls back to key-segment default).

**For the AI agent implementing Story 1.6 (Processor — DDL validation):**

The DDL cache is a `map[CanonicalNameKey]MetaVertexKey` where `CanonicalNameKey = struct{ Kind DDLKind; CanonicalName string }`. Built at startup by scanning `vtx.meta.>`, maintained via CDC. Lookup is O(1) hash map access.

When validating a mutation:
1. Read document's `class` (with default fallback per §1.5)
2. Determine `DDLKind` from the entity type (Vertex/Aspect/Link/Event)
3. Construct `CanonicalNameKey{DDLKind, class}`
4. Lookup in cache → if found, retrieve the meta-vertex key, then read the schema/sensitive/permittedCommands aspects (also cached)
5. If not found → return `ValidationResult{Skipped: true}` (permissive)

**For the AI agent implementing Story 1.8 (Write-Scope Enforcement):**

`permittedCommands` enforcement happens after schema validation in the same commit step. The check is:
- Read current operation's `operationType` from the operation envelope
- For each mutation in the batch where DDL was found: confirm `operationType` is in the DDL's `permittedCommands` array
- If absent or empty → unrestricted (consistent with permissive default for missing fields)
- If present and `operationType` not in list → reject entire operation with `WriteScopeViolation` error naming the operation type and DDL canonical name

---

## Contract #2 — Operation Envelope

The operation envelope is the message format a client publishes to `core-operations` JetStream. It is the only way to introduce state changes into Core KV (no exceptions — see architectural principle P2). This contract defines its shape, lane semantics, reply contract, and implementation requirements.

### 2.1 Envelope Shape

```json
{
  "requestId": "Rm7q3pntwzkfbcxv5p9j",
  "lane": "default",
  "operationType": "CreateIdentity",
  "actor": "vtx.identity.St6mP3qBn4rT8wYxK7Vc",
  "submittedAt": "2026-04-11T14:32:18.142Z",
  "payload": {
    "name": "Andrew Solgan",
    "email": "andrew@lattice.example"
  },
  "contextHint": {
    "reads": [
      "vtx.identity.St6mP3qBn4rT8wYxK7Vc",
      "vtx.meta.mP3qBn4rT8wYxK7Vc6St2"
    ]
  }
}
```

### 2.2 Field Specification

| Field | Required | Type | Mutability | Purpose |
|-------|----------|------|------------|---------|
| `requestId` | yes | string (20-char NanoID, custom alphabet per Contract #1) | immutable | Client-generated idempotency key. The matching `vtx.op.<requestId>` tracker is committed atomically with the operation's mutations (commit step 8). Resubmitting the same `requestId` is the dedup path. |
| `lane` | yes | string (enum: `default`, `meta`, `urgent`, `system`) | immutable | Determines JetStream subject (`ops.<lane>.>`) and consumer routing. See §2.3. |
| `operationType` | yes | string (PascalCase verb-noun) | immutable | Operation's type. Used by Starlark dispatch and by `permittedCommands` enforcement at commit step 6. Examples: `CreateIdentity`, `ClaimIdentity`, `AssignReportingChain`. |
| `actor` | yes | string (full vertex key, e.g., `vtx.identity.<NanoID>`) | immutable | Identity vertex submitting the operation. Used for Capability KV auth lookup (commit step 3) and provenance fields on resulting documents. |
| `submittedAt` | yes | string (ISO 8601) | immutable | Client-side submission timestamp. Useful for debugging and audit. **NOT** used by the Processor for ordering — JetStream sequence is authoritative. |
| `payload` | yes | object | immutable | Operation-specific data. Shape varies by `operationType`. Schema validated by Starlark dispatch (not by envelope schema; envelope is type-agnostic). May be empty `{}` for parameterless operations. |
| `contextHint` | optional | object with `reads: string[]` | immutable | JIT Hydration directive — declared read set. Lists Core KV keys the Starlark script will read. Processor pre-fetches these at commit step 4. If absent, Processor falls back to lazy on-demand reads (with latency penalty under load). See §2.5. |

**`actor` form:** Full vertex key including the `vtx.` prefix. Short forms (`identity.<id>`) are reserved for HTTP headers in Phase 2 (Gateway translates to full key before envelope submission).

**Phase-1 transitional field — `class` (optional, `omitempty`):**

Story 1.6 introduced an optional top-level `class` field on the operation envelope to let the Hydrator resolve the operation's DDL during the window before the full DDL cache could derive class from `operationType`. Story 1.7 brought the DDL cache forward; the field remains in place as a Phase-1-transitional client hint while the operationType→class reverse index matures.

| Field | Required | Type | Mutability | Purpose |
|-------|----------|------|------------|---------|
| `class` | optional (Phase-1 transitional) | string (DDL canonical name, e.g., `"identity"`) | immutable | Tells the Hydrator/Validator which DDL meta-vertex applies to this operation. Falls back to `payload.class` if absent. To be removed once the DDL cache fully covers operationType→class derivation (target: Story 1.10 or later). Clients that omit `class` today MUST supply `payload.class`. The field is `omitempty` in the wire format — clients that did not include it before Story 1.6 are unaffected. |

See `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` (Story 1.6 entry, resolved in Story 1.7) for the full disposition record.

### 2.3 Lanes and JetStream Subject Mapping

Phase 1 reserves four lanes. Operations on each lane publish to a corresponding JetStream subject prefix; the Processor's lane consumers subscribe to the matching subjects.

| Lane | JetStream Subject | Consumer Semantics | Use Case |
|------|-------------------|---------------------|----------|
| `default` | `ops.default.>` | Standard parallel consumer; bulk of operator and AI traffic | Normal business operations |
| `meta` | `ops.meta.>` | **Serialized** consumer (concurrency = 1); DDL cache invalidation synchronous with commit | DDL changes; Lens definition changes; event schema changes. Serialization prevents concurrent DDL races. |
| `urgent` | `ops.urgent.>` | Priority parallel consumer with higher weight in scheduling | Time-sensitive business operations (e.g., security overrides, emergency revocations). Operator-defined criteria — platform does not auto-promote. |
| `system` | `ops.system.>` | Parallel consumer dedicated to internal service actors | Loom/Weaver/admin tool operations. Separating these from `default` prevents internal automation from competing with user-facing operations for consumer capacity. |

**Lane authorization:** Submitting to a lane is itself capability-controlled. The Capability Lens grants per-lane submission rights. Most actors hold `default` only. `meta` requires operator/admin capability. `urgent` requires explicit grant. `system` is reserved for internal service actors. A submission to a lane the actor lacks capability for is rejected at commit step 3 (auth check) before any further processing.

**Deferred lane reservations** (post-Phase 1):
- `replay` — for the Replay tool's operations during disaster recovery; keeps replays from competing with live traffic
- Operator-custom lanes — Phase 2+ may permit DDL-driven lane registration

### 2.4 Reply Envelope

`core-operations` uses JetStream's request-reply pattern. The Processor returns a reply envelope **after commit step 8 (atomic batch commit)** — at which point the operation is durable, but events are still being published (step 9) and projections have not yet caught up.

```json
{
  "requestId": "Rm7q3pntwzkfbcxv5p9j",
  "opTrackerKey": "vtx.op.Rm7q3pntwzkfbcxv5p9j",
  "status": "accepted",
  "committedAt": "2026-04-11T14:32:18.215Z"
}
```

For errors:

```json
{
  "requestId": "Rm7q3pntwzkfbcxv5p9j",
  "opTrackerKey": null,
  "status": "rejected",
  "error": {
    "code": "AuthDenied",
    "message": "Actor lacks permission for operation type 'CreateLease' on lane 'default'",
    "details": {
      "missingPermission": "lease.create",
      "actorRole": "consumer"
    }
  }
}
```

For dedup-detected resubmits:

```json
{
  "requestId": "Rm7q3pntwzkfbcxv5p9j",
  "opTrackerKey": "vtx.op.Rm7q3pntwzkfbcxv5p9j",
  "status": "duplicate",
  "originalCommittedAt": "2026-04-11T14:32:18.215Z"
}
```

**Reply field specification:**

| Field | Required | Notes |
|-------|----------|-------|
| `requestId` | yes | Echo of submitted requestId |
| `opTrackerKey` | yes for `accepted`/`duplicate`; null for `rejected` | Vertex key of the idempotency tracker. Client polls this for Read-Your-Own-Writes convergence (per architecture's MVP RYOW mitigation). |
| `status` | yes | `accepted` (committed), `duplicate` (already committed via prior submission), `rejected` (validation/auth failure — no commit) |
| `committedAt` | for `accepted` | Timestamp of step 8 commit |
| `originalCommittedAt` | for `duplicate` | Timestamp of original commit |
| `error` | for `rejected` | Structured error: `code` (machine-readable), `message` (human-readable), `details` (structured context). Error codes are enumerated; see §2.6. |

**The reply does NOT wait for:**
- Event publication (step 9) — fire-and-forget after atomic commit
- Projection convergence — client polls `opTrackerKey` for that
- Lens-target store write — client polls the relevant Lens for query convergence

**Why reply after step 8 rather than step 10:** Durability is guaranteed by step 8 (atomic batch with revision conditions). Events are validated *before* step 8 (step 7), so if the operation reached step 8 it produced valid events. Step 9 (publish) is retried on Processor restart via the redelivery + dedup path. The client's "is my operation done?" question is honestly answered at step 8.

### 2.5 Context Hint Semantics

The `contextHint.reads` array declares Core KV keys the Starlark script will read. At commit step 4 (Hydrate), the Processor pre-fetches these into the working set cache.

**When provided:**
- Processor fetches all declared keys in parallel (NATS KV batch read)
- Working set cache is populated before Starlark execution begins
- Starlark reads hit the cache; no Core KV round-trips during script execution
- Reads of keys NOT in `contextHint` still work (fall through to on-demand fetch) but incur latency

**When absent:**
- Processor uses lazy on-demand reads during Starlark execution
- Each `kv.Read()` call from Starlark performs a Core KV fetch
- Per-operation latency increases proportional to read count
- At MVP scale (10–100 ops/sec) this is tolerable; under sustained load it becomes a bottleneck

**Convention:** SDK tools and AI agent integrations SHOULD populate `contextHint` whenever the read set is determinable at submission time. The platform does not enforce its presence.

**Future evolution (post-Phase 1):** Static analysis of Starlark scripts may auto-derive read sets, eliminating the need for callers to populate `contextHint` explicitly. Not in scope for Phase 1.

### 2.6 Error Code Enumeration (Initial Set)

The reply envelope's `error.code` is one of a closed enumeration. Phase 1 codes:

| Code | Meaning | Commit Step |
|------|---------|-------------|
| `EnvelopeMalformed` | Operation envelope failed schema validation (missing required field, invalid type, etc.) | Pre-step-1 (Processor entry) |
| `LaneUnauthorized` | Actor lacks capability to submit to declared lane | Step 3 |
| `AuthDenied` | Actor lacks capability for operationType on target entities | Step 3 |
| `AuthContextMismatch` | `authContext` declared an auth path that doesn't match actor's capability projection (e.g., `service` set but service not in `serviceAccess[]`; `task` set but task not in `ephemeralGrants[]` or target mismatch) | Step 3 |
| `StarlarkExecutionFailed` | Script raised an error or attempted forbidden I/O | Step 5 |
| `StarlarkExecutionTimeout` | Script exceeded execution time budget (NFR-P4) | Step 5 |
| `SchemaViolation` | MutationBatch failed DDL JSON Schema validation | Step 6 |
| `WriteScopeViolation` | Mutation outside declared `permittedCommands` for affected DDL | Step 6 |
| `SensitivityViolation` | Sensitive aspect attached to non-identity-anchored vertex | Step 6 |
| `EventSchemaViolation` | EventList contained event failing event DDL validation | Step 7 |
| `RevisionConflict` | Atomic batch rejected due to concurrent revision change; retries exhausted | Step 8 |
| `MetaLaneCollision` | DDL change conflicts with concurrent meta-lane mutation | Step 8 (meta lane only) |
| `InternalError` | Unrecoverable Processor failure not covered by above codes | Any step |

Each code is paired with a human-readable `message` and structured `details` appropriate to the failure mode. The enumeration is extensible — Phase 2+ may add codes; existing codes are immutable contract.

### 2.8 Auth Context

Service-scoped operations and task-derived operations require auth information beyond the basic envelope. The optional `authContext` field carries this information, declaring which auth path the Processor should follow at commit step 3.

**Envelope shape with authContext:**

```json
{
  "requestId": "Rm7q3pntwzkfbcxv5p9j",
  "lane": "default",
  "operationType": "BookExecutiveCleaning",
  "actor": "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
  "authContext": {
    "service": "vtx.service.executive-cleaning-NanoID",
    "task": null,
    "target": null
  },
  "submittedAt": "2026-05-12T14:32:18.142Z",
  "payload": { "date": "2026-05-15", "slot": "morning" },
  "contextHint": { "reads": [ ... ] }
}
```

**Field semantics:**

| Field | When populated | Purpose |
|-------|----------------|---------|
| `authContext.service` | Service-scoped operations | Vertex key of the service the operation is invoked on. Processor scans `cap.<actor>.serviceAccess[]` for matching `service`. See Contract #6 §6.3. |
| `authContext.task` | Task-derived operations (FR56) | Vertex key of the task that justifies the temporary authorization. Processor scans `cap.<actor>.ephemeralGrants[]` for matching `taskKey` plus `target` plus `expiresAt > now`. |
| `authContext.target` | (a) Task-derived operations needing scope-target match; (b) platform operations with `scope: "self"` or `scope: "specific"` | The specific entity the operation acts on. For `scope: "self"`, Processor enforces `target == actor`. |

All three fields are optional. `null`, omitted, or the entire `authContext` block absent all mean "not applicable for that path."

**Processor dispatch at step 3:**

```
if authContext.task is set:
    look up ephemeralGrants[] entry where taskKey == authContext.task
    AND the entry's operationType matches the envelope's operationType
    AND the entry's target matches authContext.target
    AND expiresAt > now
    → allow or deny (AuthDenied / AuthContextMismatch)

elif authContext.service is set:
    look up serviceAccess[] entry where service == authContext.service
    AND allowedOperations[] contains the envelope's operationType
    → allow or deny

else:
    look up platformPermissions[] entry matching the envelope's operationType
    validate scope:
        scope=any    → allow
        scope=self   → require authContext.target == actor
        scope=owned  → deferred to Phase 2
    → allow or deny
```

Task auth takes precedence over service auth, which takes precedence over platform auth. An actor may hold multiple auth paths to the same operation; they explicitly declare which path they're invoking via `authContext`. This makes the auth path inspectable at the wire level and testable in adversarial suites.

**Forgery resistance:**

`authContext` is a *hint about which auth path to check*, not a claim of authorization. An actor can submit any value in `authContext.service` — but unless that service appears in their actual `serviceAccess[]` projection (produced by the Capability Lens), the check fails. The routing-via-`authContext` does not grant access; it only selects which subsection of the capability projection to consult. Bypass test suite (Story 1.11 / Story 3.x) MUST include test cases proving that mismatched `authContext` values are rejected.

**Worked examples:**

```json
// Service operation (penthouse resident books executive cleaning)
"authContext": { "service": "vtx.service.executive-cleaning-NanoID" }

// Task-derived (manager approves lease application)
"authContext": {
  "task": "vtx.task.Rm7q3pntwzkfbcxv5p9j",
  "target": "vtx.lease.Op4Nb2mPq6rTwzKxVyP7"
}

// Self-scoped platform operation (resident updates own email)
"authContext": { "target": "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y" }

// Unscoped platform operation (admin creates new DDL) — authContext omitted entirely
```

### 2.9 Implementation Notes

**For the AI agent implementing Story 1.5 (`internal/substrate`):**

- `package envelope` — Go struct definitions for `OperationEnvelope` and `OperationReply`, including the enumerated `Lane` and `Status` types and the `ErrorCode` enum. JSON marshaling with strict required-field validation (rejects unknown fields).
- Envelope JSON Schema file committed alongside Go types — used by SDK validation and by Processor's pre-step-1 envelope check.

**For the AI agent implementing Story 1.4 (Processor — Consume, Dedup, Auth Stub):**

- Pre-step-1: validate envelope against schema; on failure, return `EnvelopeMalformed` reply without further processing.
- Step 1: consume from the configured lane subject. Each Processor instance subscribes to one or more lane subjects per its configuration.
- `meta` lane consumer is configured with `MaxAckPending=1` (serialized); other lanes are configured for parallelism per deployment sizing.
- Step 2 (dedup): read `vtx.op.<requestId>`. If found with `isDeleted: false`, return `duplicate` reply with `originalCommittedAt` from the tracker. If found with `isDeleted: true`, treat as not-found (allow resubmission — operator-driven retry path).
- Step 3 (auth): two checks happen here — (a) actor capability for the lane, (b) actor capability for the operationType on the read/write set. Both come from Capability KV lookups.

**For the AI agent implementing Story 1.7 (Processor — Event Publication & Fault Injection):**

- Reply envelope publication happens **between step 8 (commit) and step 9 (events)**. If reply publication fails (NATS reply subject closed), the operation is still durably committed — log the failure to Health KV and proceed with event publication. Client will discover the commit via polling `opTrackerKey` on next attempt with the same requestId (dedup will return the now-committed tracker).
- Event publication failures after reply are recoverable via JetStream redelivery (the `core-operations` message isn't acked until step 10).

## Contract #3 — MutationBatch and EventList (Starlark Return Contract)

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
| `document` | `create`, `update` | Document body. Includes `class`, `isDeleted`, and `data` (plus aspect/link-specific fields like `vertexKey`/`localName`/`youngerVertex`/`olderVertex`). **Provenance fields are NOT set by the script** — `createdAt`, `createdBy`, `createdByOp`, `lastModifiedAt`, `lastModifiedBy`, `lastModifiedByOp` are injected by the Processor at commit step 6 using the current operation's actor and timestamp. |
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

## Contract #4 — Idempotency Tracker (`vtx.op.<requestId>`)

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

Trackers are written with a **24-hour per-key TTL** at commit step 8, using NATS JetStream's per-message TTL feature (ADR-48, introduced in NATS 2.11; Lattice's minimum platform is NATS 2.12 for atomic batch support, 2.14 recommended). After 24 hours, NATS publishes a `PURGE` marker for the tracker's key with header `Nats-Marker-Reason: MaxAge`, which Refractor and other CDC consumers observe as an explicit expiry event.

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

## Contract #5 — Health KV Convention

> **Phase 1 schema inventory** lives at `docs/observability/health-kv-schema.md` (Story 6.2). This contract describes the convention; the schema doc enumerates emitted keys per component, reserved namespaces, and the `lattice health summary` rollup semantics.

Health KV is the operational observability plane. Every running component writes its own heartbeat to Health KV; readers (humans, CLI tooling at Phase 1; Lens projections at Phase 2+) observe component liveness and operational metrics. Health KV is a **soft convention at MVP** — Stream 7's Closed-loop Weaver auditor (deferred) is the first automated consumer, at which point the convention hardens into a hard contract.

### 5.1 Bucket and Key Pattern

**Bucket:** A dedicated NATS KV bucket separate from Core KV. Provisioned by `make up` with `allow_msg_ttl: true` enabled.

**Key pattern:**
```
health.<component>.<instance>
```

- `<component>` — canonical component name (lowercase, no dots). Phase 1 values: `processor`, `refractor`. Phase 2+ additions: `loom`, `weaver`, `gateway`.
- `<instance>` — stable identifier for the running instance. Convention: `<component-prefix>-<NanoID>` where the NanoID is generated once at instance startup (e.g., `proc-Lk2Pn6mQrtwzKbcXvP3T`). The NanoID persists across heartbeats (the same instance keeps writing to the same key); a restart generates a new NanoID and hence a new key.

**Health KV keys do NOT follow Core KV's `vtx`/`asp`/`lnk` patterns.** Health is a separate addressing space in a separate bucket. Direct KV writes to Health are explicitly sanctioned (it's the only sanctioned direct-KV-write pattern outside Refractor's own targets, per architecture P2).

### 5.2 Document Shape

```json
{
  "key": "health.processor.proc-Lk2Pn6mQrtwzKbcXvP3T",
  "component": "processor",
  "instance": "proc-Lk2Pn6mQrtwzKbcXvP3T",
  "version": "1.0",
  "status": "healthy",
  "heartbeatAt": "2026-04-11T14:32:18.142Z",
  "startedAt": "2026-04-08T14:17:00.000Z",
  "uptime": "PT72H15M",
  "metrics": {
    "ops_consumed_total": 14823,
    "ops_committed_total": 14821,
    "ops_rejected_total": 2,
    "p99_starlark_ms": 47,
    "p99_commit_path_ms": 198,
    "lane_lag": {
      "default": 0,
      "meta": 0,
      "urgent": 0,
      "system": 0
    }
  },
  "issues": []
}
```

**Field semantics:**

| Field | Required | Purpose |
|-------|----------|---------|
| `key` | yes | Echo of the Health KV key |
| `component` | yes | Canonical component name (matches `<component>` segment) |
| `instance` | yes | Canonical instance identifier (matches `<instance>` segment) |
| `version` | yes | Health document schema version. Phase 1 = `"1.0"`. Consumers can branch on this; the contract evolves freely until Stream 7. |
| `status` | yes | Component liveness/operational state. Enum: see §5.3 |
| `heartbeatAt` | yes | Timestamp of this heartbeat write. Readers compare against current time + heartbeat interval to detect staleness. |
| `startedAt` | yes | Component startup timestamp (immutable across heartbeats from the same instance). |
| `uptime` | yes | ISO 8601 duration since `startedAt`. Computed at heartbeat time. |
| `metrics` | yes | Component-specific operational counters and gauges. Baseline metrics per component are recommended (§5.4); additional metrics are component-author's discretion. |
| `issues` | yes | Array of structured issue records. Empty `[]` when `status: "healthy"`. Non-empty for `degraded` and `unhealthy`. See §5.5. |

### 5.3 Status Enumeration

| Value | Meaning |
|-------|---------|
| `starting` | Component is initializing; not yet ready to handle work |
| `healthy` | Component is operating normally; `issues` is empty |
| `degraded` | Component is functioning but with reduced capability or elevated error rates; `issues` non-empty with `severity: "warning"` entries |
| `unhealthy` | Component cannot fulfill its primary responsibility (e.g., Processor can't write to Core KV; Refractor can't project to any Lens target); `issues` non-empty with at least one `severity: "error"` entry |
| `shuttingDown` | Component received shutdown signal and is draining work; should not receive new requests |

Status transitions are component-author's discretion; the platform does not enforce specific rules about when a component should transition states. The convention: components should err on the side of being honest about degradation rather than reporting false-healthy.

### 5.4 Recommended Metrics Baseline (Phase 1 Components)

These metrics are recommended (not enforced) at MVP. Stream 7 may harden them into requirements.

**Processor:**
- `ops_consumed_total` — JetStream messages consumed (cumulative since startup)
- `ops_committed_total` — operations that reached step 8 successfully (cumulative)
- `ops_rejected_total` — operations rejected at any step before commit (cumulative)
- `p99_starlark_ms` — Starlark execution p99 latency (rolling window, recommend 5 minutes)
- `p99_commit_path_ms` — full commit path p99 latency, step 1 through step 10 (rolling window)
- `lane_lag` — per-lane JetStream consumer lag (messages behind head, by lane name)

**Refractor:**
- `lens_count_active` — number of Lens definitions currently projecting
- `cdc_lag_p99_ms_by_lens` — map of `{lensName: p99LagMs}` for each active Lens (architecture's primary liveness indicator)
- `projection_errors_total` — projection failures count (cumulative)
- `vault_calls_total` — Vault decryption calls count (cumulative; Phase 1 stub may report 0)
- `keyshredded_handled_total` — `KeyShredded` events processed (cumulative)

**Loom / Weaver / Gateway:** TBD in Phase 2; conventions will follow this pattern.

### 5.5 Issue Records

Each entry in the `issues` array:

```json
{
  "code": "VaultUnreachable",
  "severity": "error",
  "message": "Cannot reach Vault for sensitive aspect decryption; Secure Lens projections paused",
  "since": "2026-04-11T14:25:00.000Z"
}
```

| Field | Required | Purpose |
|-------|----------|---------|
| `code` | yes | Machine-readable code (PascalCase). Component-defined. |
| `severity` | yes | `warning` (degraded) or `error` (unhealthy). |
| `message` | yes | Human-readable description. |
| `since` | yes | ISO 8601 timestamp of when this issue first arose; persists across heartbeats while the issue continues. |

Issues are component-tracked: a component holds open issues in memory and includes them in each heartbeat. When an issue resolves, the component removes it from its in-memory set; the next heartbeat omits it from the `issues` array.

### 5.6 Heartbeat Cadence and TTL

**Heartbeat interval:** Default **10 seconds** per heartbeat (matches NFR-O1's "every 10 seconds" requirement). Configurable per component — Refractor under heavy CDC load may heartbeat less frequently; components with faster failure profiles may heartbeat more frequently.

**TTL on each heartbeat write:** Default `TTL = heartbeat_interval × ttl_multiplier` where `ttl_multiplier = 10`. With the 10s default heartbeat, TTL = **100 seconds**. After 100s with no heartbeat write, NATS publishes a `PURGE` marker for the component's health key; observers see "no health entry" rather than stale-looking data.

Both `heartbeat_interval` and `ttl_multiplier` are component-configurable via deployment config. The 10× multiplier is the architecture-locked default; it provides breathing room for GC pauses, brief network blips, and other transient events without false-positive component-death alarms.

**Each heartbeat OVERWRITES the previous heartbeat** (NATS KV update with no `expectedRevision`), resetting the TTL clock. Continuous heartbeating keeps the entry alive indefinitely; missed heartbeats expire it within the TTL window.

### 5.7 Reading and Writing Semantics

**Writers:** Every component writes its own heartbeat to its own key on the heartbeat interval. The only writes to Health KV are heartbeat writes; no component writes to another component's health entry.

**Readers (Phase 1):** Humans via NATS CLI (`nats kv get health <key>`), and the Lattice CLI tool (`make health` or equivalent). The console/Lens projections in FR47 and FR52 are Phase 2 — they'll project Health KV via a Lens then.

**Health KV is NOT projected via the Capability Lens at Phase 1.** Every actor with NATS cluster access can read Health KV. This is consistent with the architecture's "Health KV reads are not auth-gated at MVP" note. Phase 2+ may add capability scoping; not in Phase 1 scope.

### 5.8 Implementation Notes

**For the AI agent implementing Story 1.4 (Dev Harness):**

- Health KV bucket created at `make up` time with `allow_msg_ttl: true`
- Bucket name: `health` (or `lattice_health` if namespace prefixing is required by deployment)

**For the AI agent implementing Story 1.4 (Processor — Consume, Dedup, Auth Stub):**

The Processor instance, on startup:
1. Generate instance NanoID (20-char custom alphabet via substrate's `nanoid.new()`)
2. Construct instance ID: `proc-<NanoID>`
3. Write initial heartbeat with `status: "starting"` and instance metadata
4. Begin commit path consumer loops
5. Once consumers are running, transition to `status: "healthy"` and begin regular heartbeat cadence on the configured interval (default 10s)
6. Each heartbeat write: read current metrics from in-memory counters, construct the document, write to `health.processor.<instance-id>` with `TTL=100s` (default)
7. On `SIGTERM` / shutdown signal: transition to `status: "shuttingDown"` and write a final heartbeat before exit

**For the AI agent implementing Story 2.x (Refractor — Materializer morph):**

The same pattern applies to Refractor, with Refractor-specific metrics. The Refractor health key is `health.refractor.refr-<NanoID>`.

**For the bypass test suite (Story 1.11):**

Bypass test category #1 (direct KV write to Core KV) does NOT apply to Health KV — Health KV is the explicitly sanctioned direct-write surface. The test suite must NOT include Health KV writes as bypass attempts.

## Contract #6 — Capability KV Shape

Capability KV is what makes the architecture's O(1) authorization promise real. The Capability Lens (a Refractor projection authored as `class: "meta.lens"`) walks graph topology — actor → roles → permissions, actor → residence → services-availableAt-with-exclusions, actor → assigned-tasks → granted-operations — and writes the resolved per-actor capability set as a flat document. The Processor at commit step 3 reads a single key from Capability KV; no graph traversal in the hot path.

This contract is **security-critical** per the architecture's "Capability Lens is a security-critical projection" note. A bug here equals privilege escalation. The cypher rule (Story 3.x) and the bypass test suite (Stories 1.11 and 3.x — Capability Lens 4 attack vectors gate) are joint owners of correctness.

### Source of Truth

**The shape defined in this contract is *produced by* the Capability Lens cypher query's `RETURN` clause.** The bootstrap-seeded cypher query at `vtx.meta.lens.capability` is the *source of truth* for what gets written to Capability KV. This contract serves two derived functions:

1. **Read-side contract** — the Processor at step 3 needs to know the shape to read it correctly. This contract documents the shape the cypher RETURN produces so Processor's reader code is grounded in a stable expectation.
2. **Test oracle** — Story 3.2's contract-conformance test runs the bootstrap cypher query against a seeded graph and asserts the output structure matches the shape below. This test catches schema drift if anyone modifies the Capability Lens cypher query without updating this contract (or vice versa).

**Schema drift mitigation:** Any change to either the bootstrap cypher query OR this contract must update the other in the same operation. The contract-conformance test in Story 3.2 is the safety net.

### 6.1 Bucket and Key Pattern

**Bucket:** A dedicated NATS KV bucket separate from Core KV, Health KV, and Weaver buckets. Owned by Refractor as a Lens target store — Refractor is the sole writer; Processor reads only.

**Key patterns:**
```
cap.<actor-vertex-key-suffix>             # primary per-actor entry
cap.role-by-operation.<operationType>     # secondary role-coverage index
```

**Primary entry** — Where `<actor-vertex-key-suffix>` is the actor's vertex key with the `vtx.` prefix dropped. Examples:

```
cap.identity.Hj4kPmRtw9nbCxz5vQ2y
cap.identity.St6mP3qBn4rT8wYxK7Vc
```

Phase 1 indexes capabilities by actor (one key per actor). Each entry contains the three-section permission model (§6.2). A by-operation actor index (Phase 2 — for Gateway pre-flight checks) is a separate addressable space; not in Phase 1 scope.

**Secondary role-coverage index** — populated by a separate bootstrap Lens (`vtx.meta.lens.capabilityRoleIndex`) projecting to the same Capability KV bucket. Used exclusively by Processor's denial-response construction (Story 3.4) to populate the `rolesCarryingPermission` field of `AuthDenied` responses without graph traversal on the denial path. Each entry contains a flat list of role names whose permission grants include the operation type. Example:

```
cap.role-by-operation.BookExecutiveCleaning
  → {"roles": ["penthouseResident", "platformAdmin"], "projectedAt": "..."}
```

**Architectural note on multi-Lens pattern.** The two key spaces are produced by **two separate Lens definitions**, both seeded at primordial bootstrap (Contract #7), both projecting to the same Capability KV bucket with disjoint key prefixes. This follows Lattice's standard pattern from the architectural decisions: *each Lens has one RETURN producing one shape; multi-output patterns are expressed as additional Lenses, not as Lens-internal complexity* (lattice-architecture.md §"Multi-target Lens adapters"; brainstorming session items #38, #39, #61). The same pattern applies to Phase 2+ Personal Lens fan-out and Postgres RLS link mirroring.

### 6.2 Document Shape

```json
{
  "key": "cap.identity.Hj4kPmRtw9nbCxz5vQ2y",
  "actor": "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
  "version": "1.0",
  "projectedAt": "2026-05-12T14:32:18.142Z",
  "projectedFromRevisions": {
    "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y": 47,
    "vtx.meta.capabilityLensDefinition": 12,
    "vtx.unit.penthouse-Lk2Pn6mQrtwzKbcXvP3T": 8,
    "vtx.lease.Op4Nb2mPq6rTwzKxVyP7": 3,
    "vtx.role.penthouseResident": 5
  },
  "lanes": ["default"],

  "platformPermissions": [
    {
      "operationType": "ClaimIdentity",
      "scope": "self"
    },
    {
      "operationType": "UpdateIdentityContact",
      "scope": "self"
    }
  ],

  "serviceAccess": [
    {
      "service": "vtx.service.executive-cleaning-NanoID",
      "serviceClass": "service.cleaning.executive",
      "resolvedVia": ["vtx.unit.penthouse-Lk2Pn6mQrtwzKbcXvP3T"],
      "allowedOperations": [
        { "operationType": "BookExecutiveCleaning" },
        { "operationType": "CancelBooking" },
        { "operationType": "ViewSchedule" }
      ]
    },
    {
      "service": "vtx.service.payRent-NanoID",
      "serviceClass": "service.financial.rentPayment",
      "resolvedVia": ["vtx.lease.Op4Nb2mPq6rTwzKxVyP7"],
      "allowedOperations": [
        { "operationType": "InitiatePayment" },
        { "operationType": "ViewBalance" },
        { "operationType": "SetupAutopay" }
      ]
    }
  ],

  "ephemeralGrants": [
    {
      "source": "task",
      "taskKey": "vtx.task.Rm7q3pntwzkfbcxv5p9j",
      "operationType": "ApproveLeaseApplication",
      "target": "vtx.lease.applicant-NanoID",
      "expiresAt": "2026-05-13T14:00:00.000Z"
    }
  ],

  "roles": [
    "vtx.role.penthouseResident",
    "vtx.role.leaseholderInGoodStanding"
  ]
}
```

### 6.3 Field Specification

**Top-level envelope:**

| Field | Required | Purpose |
|-------|----------|---------|
| `key` | yes | Echo of the Capability KV key |
| `actor` | yes | Full vertex key of the actor |
| `version` | yes | Document schema version. Phase 1 = `"1.0"`. Consumers branch on this; the contract evolves under Stream 3 oversight. |
| `projectedAt` | yes | Refractor's clock when the projection was written |
| `projectedFromRevisions` | yes | Map of source-vertex-key → revision-at-projection. Enables consistency-window detection used by the bypass test suite. Includes the actor's identity vertex, the Capability Lens definition vertex, all role vertices held, any active task vertices for ephemeral grants, and any location/lease vertices referenced by `resolvedVia` paths. |
| `lanes` | yes | Array of JetStream lanes the actor may submit to. Subset of `["default", "meta", "urgent", "system"]`. |
| `platformPermissions` | yes (may be empty `[]`) | Standing operation permissions not scoped to a service. See §6.4. |
| `serviceAccess` | yes (may be empty `[]`) | Service-scoped operation permissions. The cypher rule pre-resolves availability via graph topology. See §6.5. |
| `ephemeralGrants` | yes (may be empty `[]`) | Task-derived, time-bounded, target-specific grants (FR56). See §6.6. |
| `roles` | yes (may be empty `[]`) | Vertex keys of role vertices the actor currently holds. Used by Processor for FR22 structural denial responses. |

### 6.4 platformPermissions[]

Each entry describes a system-level operation not scoped to any service.

| Field | Required | Purpose |
|-------|----------|---------|
| `operationType` | yes | Operation type (PascalCase). |
| `scope` | yes | One of `any`, `self`, `owned`, `specific`. See §6.7. |

Processor dispatch (when `authContext.service` is null AND `authContext.task` is null):
1. Scan `platformPermissions[]` for matching `operationType`
2. Validate scope:
   - `any` → allow
   - `self` → require `authContext.target == actor`
   - `specific` → require `authContext.target` exact-match on the scope's allowed targets (Phase 2)
   - `owned` → deferred to Phase 2 (requires ownership-link model)
3. → allow or deny

### 6.5 serviceAccess[]

Each entry describes the actor's resolved access to one service vertex, with the operations they may invoke on it. The cypher rule pre-resolved availability/unavailability via graph topology before writing the entry.

| Field | Required | Purpose |
|-------|----------|---------|
| `service` | yes | Vertex key of the service. |
| `serviceClass` | yes | Echo of the service vertex's `class` field. Used in structural denial responses (FR22). |
| `resolvedVia` | yes | Array of vertex keys that justify access (e.g., the unit, the building, the lease). For auditability and debuggability — answers "why does this actor have access to this service?" |
| `allowedOperations` | yes | Array of operations the actor may invoke on this service. Each entry has `operationType`. |

Processor dispatch (when `authContext.service` is set):
1. Scan `serviceAccess[]` for entry where `service == authContext.service`
2. If not found → `AuthContextMismatch`
3. Scan that entry's `allowedOperations[]` for matching `operationType`
4. If not found → `AuthDenied`
5. → allow

### 6.6 ephemeralGrants[]

Each entry describes a time-bounded, target-specific authorization derived from a task assignment (FR56).

| Field | Required | Purpose |
|-------|----------|---------|
| `source` | yes | Grant source. Phase 1: `"task"`. Reserved for future grant sources. |
| `taskKey` | yes | Vertex key of the task that justifies this grant. |
| `operationType` | yes | Operation type permitted by the grant. |
| `target` | yes | Specific entity the grant applies to (e.g., the lease application being approved). |
| `expiresAt` | yes | ISO 8601 expiry timestamp. Processor enforces `expiresAt > now` at lookup time. |

Processor dispatch (when `authContext.task` is set):
1. Scan `ephemeralGrants[]` for entry where ALL of: `taskKey == authContext.task`, `operationType == envelope.operationType`, `target == authContext.target`, `expiresAt > now`
2. If not found → `AuthContextMismatch`
3. → allow

### 6.7 Scope Enumeration

| Scope | Meaning | Phase |
|-------|---------|-------|
| `any` | Operation permitted on any target — broadest scope. | Phase 1 |
| `self` | Operation permitted only when `authContext.target == actor`. | Phase 1 |
| `specific` | Operation permitted only on a named target list (declared by the permission entry). | Phase 1 (used by ephemeral grants via `target` field) |
| `owned` | Operation permitted on vertices the actor "owns" via a defined ownership link. | Phase 2 (requires ownership-link model) |

### 6.8 "No Entry = No Access"

If Processor at step 3 fetches `cap.<actor>` and receives no document (key does not exist), the operation is denied with `AuthDenied`. **Absence of a capability projection means no access** — there is no anonymous/public capability fallback.

The Capability Lens must produce a projection for every identity that may submit operations, including AI agents and internal service actors. The bootstrap identity gets its projection at platform initialization via primordial meta-vertices (Contract #7).

This is the architecture's NFR-S2 boundary: the Capability Lens is the sole authorization surface. Anything not in the projection is denied.

### 6.9 Recommended Business Link Names

The Capability Lens cypher rule references business-graph link names to walk topology. The following names are **recommended conventions** shipped with the canonical reference implementation ("Hello Lattice", FR55). Operators may define their own link types and rewrite the cypher rule to match; the names below are not platform-reserved, only convention.

| Link name | Used between | Semantics |
|-----------|--------------|-----------|
| `containedIn` | Location vertices (unit → building, room → unit, building → property) | Physical or logical containment; transitive |
| `availableAt` | Service vertex → location vertex | Service is offered at this location (and by default, at locations contained within) |
| `unavailableAt` | Service vertex → location vertex | Explicit exclusion override; closer exclusion wins over distant availability |
| `leases` | Identity → lease vertex | Actor holds a lease; lease references a unit via `containedIn` from the unit side |
| `residesIn` | Identity → location vertex | Actor resides at this location (independent of lease — guests, family, etc.) |
| `assignedTo` | Task vertex → identity vertex | Task is assigned to the actor; grants ephemeral capability per FR56 |
| `reportsTo` | Identity → identity | Reporting chain for manager-delegated task auth per FR56 |

These are recommendations only. The cypher rule (Story 3.x) is authored against whichever link conventions a deployment standardizes on.

### 6.10 Cypher Rule — Required Behaviors (Epic 3 Acceptance Criteria)

The Capability Lens cypher rule (the data of a `vtx.meta.<id>` with `class: "meta.lens"`) is built in Epic 3. Its required behaviors, captured here so Epic 3's acceptance criteria can reference this contract:

1. **Multi-level containment exclusion.** An `unavailableAt` link at any level of an actor's containment chain wins over `availableAt` at a higher level. The rule must check the entire containment path between the actor's location and the exclusion's target, not just direct links. Test case: penthouse resident with building-level `availableAt: laundry` and penthouse-level `unavailableAt: laundry` → laundry NOT in `serviceAccess[]`.

2. **Direct and transitive availability.** A service `availableAt` a location grants access to actors at that location AND at locations contained within it. The rule walks `containedIn` from the actor's location upward, collecting availability at each level. Test case: resident of any unit in a building can access `availableAt: building` services.

3. **Operation-level overrides.** Individual operation vertices linked to a service may have their own `availableAt`/`unavailableAt` links that override service-level resolution. The rule applies operation-level filtering AFTER service-level resolution; `serviceAccess[].allowedOperations[]` reflects the result.

4. **Role specialization.** Permissions derived from `vtx.role.*` linked to the identity contribute to `platformPermissions[]` independent of location-scoped service access. An actor may have both location-derived service access AND role-derived platform permissions; both must appear in their projection.

5. **Task-derived ephemeral grants (FR56).** Tasks `assignedTo` the actor produce `ephemeralGrants[]` entries with `expiresAt` populated from the task's `dueAt` or expiry aspect. Manager delegation: tasks assigned to direct reports (via `reportsTo`) produce ephemeral grants for the manager. Two-hop traversal limit at Phase 1; deeper delegation chains are Phase 2+.

6. **Adversarial test coverage (Phase 1 Gate 3).** The Capability Lens 4 attack vectors must be tested and rejected:
   - Direct manipulation of `vtx.role.*` to grant unauthorized permissions
   - Submission with `authContext.service` referencing a service not in `serviceAccess[]`
   - Use of a `vtx.task.*` reference after its `expiresAt` has passed
   - Cross-vertex permission bleed: actor having access to service X attempting an operation on service Y

### 6.11 Service Availability Windows — Deferred

Service vertices may eventually carry temporal availability aspects — e.g., `availableFrom`/`availableUntil` aspects, recurring schedules ("laundry 6am–10pm"), holiday closures, maintenance windows. **These are explicitly OUT of Capability KV scope.**

The cypher rule at Phase 1 evaluates service availability based purely on static graph topology (the existence of `availableAt` / `unavailableAt` links at projection time). If a service is temporally closed but the graph topology still says it's available, the projection will say it's available; rejection on temporal grounds is the responsibility of the operation itself (Starlark business logic) or a Phase 2 mechanism.

The shape and Lattice integration of service availability windows requires a **separate architecture session**. This is tracked as a Phase 2 design open item — not a Phase 1 gap.

### 6.12 FR22 Denial Response — Worked Example

When the penthouse resident attempts `BookLaundryService` targeting `vtx.service.laundry-NanoID`:

```json
{
  "status": "rejected",
  "error": {
    "code": "AuthContextMismatch",
    "message": "Service not available for this actor.",
    "details": {
      "operationType": "BookLaundryService",
      "deniedService": "vtx.service.laundry-NanoID",
      "deniedServiceClass": "service.cleaning.standard",
      "actorRoles": [
        "vtx.role.penthouseResident",
        "vtx.role.leaseholderInGoodStanding"
      ],
      "availableServiceClasses": [
        "service.cleaning.executive",
        "service.financial.rentPayment"
      ]
    }
  }
}
```

The denial response is structural (per Journey 5's design): names what was denied, the actor's current roles, and what IS available. No routing or escalation guidance — that's Phase 2 (FR22 deliberately scoped to structural information for Phase 1 per the party mode decision).

### 6.13 Implementation Notes

**For the AI agent implementing Story 3.x (Capability Lens cypher rule):**

- The Lens definition is a `vtx.meta.<id>` with `class: "meta.lens"`. Its aspects include `canonicalName: "capability"`, `targetBucket: "capability-kv"`, `cypherRule: "..."`, and the schema for the output document.
- The cypher rule produces one output document per identity, keyed by `cap.<actor-vertex-suffix>`.
- The rule must handle the six behaviors enumerated in §6.10.
- Output documents must follow the shape in §6.2 exactly — Processor's parser is strict about field names and types.

**For the AI agent implementing Story 1.4 (Processor — Consume, Dedup, Auth Stub):**

Phase 1 stub implementation:
- Step 3 reads `cap.<actor-vertex-suffix>` from Capability KV
- If missing → `AuthDenied`
- If present: dispatch per Contract #2 §2.8 logic (task → service → platform path selection)
- The stub may always-allow if the deployment is configured with `LATTICE_AUTH_STUB=allow-all` for early development — but production deployments enforce strictly. The bypass test suite (Story 1.11) must run with the real Capability Lens, not the stub.

**For the bypass test suite (Stories 1.11 and 3.x):**

The Capability Lens 4 attack vectors (Phase 1 Gate 3) test against the real Lens output, not the stub. Test data: a graph that exercises each attack vector listed in §6.10 item 6.

## Contract #7 — Primordial Bootstrap

The primordial bootstrap is the set of Core KV entries that `make up` seeds into a fresh Lattice deployment before any operation can be processed. It establishes the self-describing meta-meta layer, the platform's foundational types, and the topology required for the Capability Lens to produce auth projections for system identities.

### 7.1 Bootstrap Principle

**Bootstrap establishes graph topology; the Capability Lens does the rest.** No Core KV mutations bypass the Capability Lens's role as the sole authorization surface (NFR-S2). System identities — including the bootstrap identity and internal service actor identities — receive their Capability KV entries through normal Lens projection, derived from the topology that `make up` seeds.

This is the critical design principle: every actor's auth traces back to graph topology. No actor has a "direct-seeded" Capability KV entry that doesn't follow the Lens's logic. An operator or AI agent auditing the platform sees a uniform model — even the bootstrap identity's capabilities are explainable by walking the graph from its identity vertex through its role and permission links.

### 7.2 Primordial Seeding Inventory

`make up` writes the following directly to Core KV at first initialization (the sole sanctioned non-Processor write path, and only during bootstrap):

**1. Meta-meta DDLs** — DDLs describing how DDL is described. Each is a `vtx.meta.<NanoID>` vertex with appropriate aspects:
- DDL for `meta.ddl.vertexType` (the DDL that describes what a vertex-type DDL looks like)
- DDL for `meta.ddl.aspectType`
- DDL for `meta.ddl.linkType`
- DDL for `meta.ddl.eventType`
- DDL for `meta.lens` (the DDL describing what a Lens definition looks like)

**2. Reserved type DDLs** — DDLs for the platform's foundational vertex types:
- `meta` type DDL (used by all meta-vertices)
- `op` type DDL (used by idempotency trackers)
- `identity` type DDL (used by all actor identities)
- `role` type DDL (used by role vertices in the auth graph)
- `permission` type DDL (used by permission vertices)

**3. Reserved aspect-type DDLs** — aspect types used by the meta-meta layer itself:
- `canonicalName`
- `description`
- `schema`
- `sensitive`
- `permittedCommands`
- `vertexSchema`
- `cypherRule` (used by Lens definitions)
- `targetBucket` (used by Lens definitions)
- `outputSchema` (used by Lens definitions to declare projection document shape)

**4. Reserved link-type DDLs** — link types the Capability Lens cypher rule walks:
- `holdsRole` — identity → role (identity holds role)
- `grantedBy` — permission → role (permission is granted by role)
- (additional link types the rule walks; the exact set is established by the cypher rule's authoring in Story 3.x)

**5. Capability Lens definition** — a `vtx.meta.<NanoID>` vertex with `class: "meta.lens"` carrying:
- `canonicalName: "capability"`
- `cypherRule`: the openCypher rule that walks identity → role → permission topology and (post-bootstrap) availableAt/unavailableAt/containedIn topology for service access
- `targetBucket: "capability"`
- `outputSchema`: JSON Schema for the Capability KV document (Contract #6 §6.2)

**6. Root role and root permission vertices** — the topology that produces root-equivalent capability when projected:
- One or more `vtx.role.<NanoID>` vertices with `canonicalName: "systemRoot"` (or similar)
- Permission vertices granting `scope: "any"` on all platform operation types
- Links: `grantedBy` from each permission vertex to the root role (link direction is `permission → role`; reads as "permission granted by role")

**7. System identity vertices:**
- `vtx.identity.<NanoID>` with `class: "identity.system.bootstrap"` — the identity used to author all primordial entries' provenance fields
- `vtx.identity.<NanoID>` with `class: "identity.system.processor"` — the Processor's internal service actor identity

**8. Topology links from system identities to root role:**
- `lnk.identity.<bootstrap-id>.holdsRole.role.<root-role-id>`
- `lnk.identity.<processor-id>.holdsRole.role.<root-role-id>`

(Additional internal service actor identities for Loom, Weaver, etc. are seeded by their respective stream's bootstrap procedures in Phase 2+, following the same pattern.)

**9. Bootstrap operation tracker** — a synthetic `vtx.op.<NanoID>` representing platform genesis. This tracker has **no TTL** (it's a permanent record, not subject to the 24h idempotency horizon). All primordial entities reference this tracker in their `createdByOp` field, making the entire bootstrap a "single operation" in the provenance audit trail.

**Direct Capability KV writes from `make up`:** **None.** The Capability Lens, once Refractor starts, projects `cap.identity.<bootstrap-id>` and `cap.identity.<processor-id>` from the topology above.

### 7.3 NanoID Generation and Bootstrap Config

All NanoIDs for primordial vertices are generated at first `make up` execution and persisted to `lattice.bootstrap.json` (or equivalent path determined by deployment conventions). The config file contains:

```json
{
  "platformVersion": "1.0",
  "bootstrapDate": "2026-05-12T14:32:18.142Z",
  "rootRoleKey": "vtx.role.<NanoID>",
  "bootstrapIdentityKey": "vtx.identity.<NanoID>",
  "processorIdentityKey": "vtx.identity.<NanoID>",
  "capabilityLensKey": "vtx.meta.<NanoID>",
  "bootstrapOpKey": "vtx.op.<NanoID>",
  "metaMetaDDLKeys": {
    "vertexType": "vtx.meta.<NanoID>",
    "aspectType": "vtx.meta.<NanoID>",
    "linkType": "vtx.meta.<NanoID>",
    "eventType": "vtx.meta.<NanoID>",
    "lens": "vtx.meta.<NanoID>"
  }
}
```

This config provides the deployment a stable reference set for the primordial NanoIDs across restarts. Without it, post-restart code paths that need to reference (e.g.) "the bootstrap identity" couldn't find it without a class-based Lens query (which would work, but adds startup latency).

### 7.4 Bootstrap Idempotence and Re-runs

**Re-running `make up` on an existing deployment** detects the existing `lattice.bootstrap.json` and skips re-seeding. `make up` is idempotent in the sense that running it twice produces the same end state — NOT in the sense that it rewrites primordial vertices.

If an operator wants a fresh deployment, the procedure is:
1. `make down` — clears all NATS buckets, drops Postgres data, deletes `lattice.bootstrap.json`
2. `make up` — re-seeds from scratch with new NanoIDs

This is consistent with the immutability principle: primordial keys aren't reassigned in place.

### 7.5 Readiness Gate

`make up` does NOT complete until Refractor has projected the bootstrap identity's Capability KV entry. This eliminates the startup race window where Capability KV is empty and operations would fail auth.

**`make up` sequence:**

```
1. Start NATS, provision Core KV / Health KV / Capability KV / Weaver buckets
   (all with `allow_msg_ttl: true` enabled)
2. Start Postgres, run any schema setup
3. Seed primordial Core KV entries (§7.2 inventory) using NATS direct writes
4. Persist lattice.bootstrap.json
5. Start Processor and Refractor (and other configured services)
6. Poll readiness:
   - Refractor health reports `status: "healthy"` AND `lens_count_active >= 1`
   - Capability KV contains `cap.<bootstrap-identity-suffix>` with root capability
7. Print "Lattice ready ({deploymentName})" and exit success
```

**Configurable timeout** (default: 30 seconds) on the readiness poll. If exceeded, `make up` exits with a clear error message identifying which component failed to reach readiness:

```
ERROR: Lattice did not reach ready state within 30s.
  Refractor health: status=starting, lens_count_active=0
  Capability KV: cap.identity.<bootstrap-id> not found
Suggest: check refractor logs at <path>, or `make down && make up` to retry.
```

The default 30s is generous for Phase 1's scale (a handful of bootstrap entries). Production deployments at scale (post-MVP) may need longer; the timeout is deployment-configurable.

### 7.6 What's NOT in the Primordial Bootstrap

Several things deliberately stay out of `make up`:

**No "Hello Lattice" demo data.** The canonical reference implementation (FR55) is opt-in via a separate `make hello-lattice` (or equivalent) target. Bootstrap produces a minimal, viable, empty platform; demo content is a layer on top.

**No business DDLs.** The bootstrap seeds only the meta-meta layer and platform-essential types (`meta`, `op`, `identity`, `role`, `permission`). Business types (`lease`, `unit`, `building`, `service`, etc.) are authored by operators (or by AI agents in self-improvement flows) after bootstrap completes, via the standard write path (`ops.meta.>` lane).

**No user identities.** The only identities at bootstrap are the system identities (`identity.system.bootstrap` and `identity.system.processor`). Human and AI agent identities are created post-bootstrap through the standard `CreateIdentity` flow.

**No Lens projections beyond Capability.** Other Lenses (business projections, query surfaces) are authored after bootstrap and activate via CDC.

### 7.7 Implementation Notes

**For the AI agent implementing Story 1.4 (Dev Harness):**

The `make up` target's implementation:
1. Idempotence check: if `lattice.bootstrap.json` exists, skip seeding and proceed directly to step 5 (start services + poll readiness)
2. Bucket provisioning: create `core-kv`, `health-kv`, `capability-kv`, `weaver-state`, `weaver-claims` buckets; all configured with `allow_msg_ttl: true`
3. NanoID generation: invoke substrate's `nanoid.New()` for each primordial NanoID; assemble into the bootstrap config
4. Direct KV writes: for each primordial entry in §7.2 inventory, construct the document with proper envelope fields (provenance referencing the bootstrap identity and bootstrap op tracker), write to Core KV via NATS direct write
5. Persist `lattice.bootstrap.json`
6. Start Processor, Refractor, and any other configured services
7. Readiness poll loop per §7.5
8. Exit success on readiness OR exit failure on timeout

The order of primordial writes matters for some consistency properties: write the meta-meta DDLs first, then the reserved type DDLs, then the Capability Lens definition, then root role and permissions, then system identities, then topology links. Refractor's CDC processing will handle whatever order it sees, but a logical write order makes debugging easier when bootstrap fails.

**For the AI agent implementing Story 3.x (Capability Lens cypher rule):**

The cypher rule must produce root-equivalent capability when projecting an identity that holds the root role. Concretely:
- Walk identity → `holdsRole` → role
- For role.canonicalName matching `"systemRoot"` (or the deployment's root role convention), emit `platformPermissions[]` entries with `scope: "any"` for every known operation type
- This means the cypher rule must know the operation types — Phase 1 handles this by walking inbound `grantedBy` links from the role to discover permission vertices, which carry the operation types as aspects (cypher: `MATCH (r:role)<-[:grantedBy]-(p:permission)`)
- For non-root roles, the same traversal applies but only the explicitly granted operations are emitted

The rule is uniform across system and non-system identities; root capability is established by graph topology, not by class-based special-casing.

**For the bypass test suite (Stories 1.11 and 3.x):**

Test cases that MUST be covered:
- Bootstrap identity submits operations and they succeed (validates the Lens correctly projects from topology)
- A non-bootstrap identity with the same `class: "identity.system.bootstrap"` value but without `holdsRole` topology does NOT get root capability (proves class doesn't grant access; topology does)
- Tampering with the root role vertex (e.g., removing inbound `grantedBy` links from its permissions) causes the bootstrap identity to lose corresponding capabilities on the next projection cycle (proves the auth boundary is reactive to topology changes)

---

## Document Status

**Version 1.0 complete — 2026-05-12.**

All seven contracts locked. Subsequent revisions follow the standard process: changes go through architectural review with Andrew, are recorded in the document's revision history, and propagate to downstream stories (Epic 1 substrate, Processor, Refractor, Capability Lens implementations).

**Followups tracked for Phase 2 design sessions:**
- **Service Availability Windows** — shape, recurring schedules, integration with operations. Out of Capability KV; needs a dedicated architecture session.
- **NATS Counters (ADR-49) integration** — Health KV metrics, rate limiting, audit log ordering. Mostly quality-of-life; not transformational.
- **NATS Message Scheduling (ADR-51) integration** — significantly simplifies Loom (orchestration) and Weaver (convergence) design by replacing polling loops with native scheduled dispatch. To be revisited before Stream 4 begins.
