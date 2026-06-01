# Contract #1 — Addressing Model & Document Envelope

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

  **Pre-Story-1.4 framing superseded.** Earlier drafts described this as "`<id1>` is the younger vertex (later `createdAt`), `<id2>` is the older," and the link envelope carried `youngerVertex`/`olderVertex` fields. That formulation conflated runtime ordering with design intent and broke down for cases where the conceptual ordering doesn't match `createdAt` (e.g., a manager seeded later than a report through bulk import). The convention is a **DDL authoring rule**, not a runtime invariant — once authored, direction is encoded in the link DDL and instances inherit it. The envelope fields are now named **`sourceVertex`** (segments 1–3, the source side) and **`targetVertex`** (segments 4–6, the target side) to reflect the DDL-declared direction rather than any timestamp ordering. There is no `direction` field: direction is fully encoded by segment order in the key, so a stored copy would be redundant and risk drift.

**Parser disambiguation rule:**
- Count segments by dot-splitting the key. 3 segments → vertex. 4 segments → aspect. 6 segments → link. Any other segment count is malformed and rejected at write time.
- Vertex `<id>` is the third segment; aspect's vertex key is segments 1–3; link endpoints are segments 1–3 (source side) and 4–6 (target side, after the linkName).

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
  "sourceVertex": "vtx.lease.Lk2Pn6mQrtwzKbcXvP3T",
  "targetVertex": "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
  "localName": "heldBy",
  "class": "heldBy",
  "isDeleted": false,
  ...universal envelope fields...,
  "data": {}
}
```

| Field | Purpose |
|-------|---------|
| `sourceVertex` | Pointer to the source-side endpoint (key segments 1–3) — the DDL-declared source, typically the later-arriving vertex. |
| `targetVertex` | Pointer to the target-side endpoint (key segments 4–6) — the DDL-declared target, typically the pre-existing vertex. |
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

# DDL declares heldBy as lease → identity, so lease is the source side
lnk.lease.Lk2Pn6mQrtwzKbcXvP3T.heldBy.identity.St6mP3qBn4rT8wYxK7Vc
  envelope:
    key: "lnk.lease.Lk2Pn6mQrtwzKbcXvP3T.heldBy.identity.St6mP3qBn4rT8wYxK7Vc"
    sourceVertex: "vtx.lease.Lk2Pn6mQrtwzKbcXvP3T"
    targetVertex: "vtx.identity.St6mP3qBn4rT8wYxK7Vc"
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
