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
- **`<id>`** — a NanoID generated per the architecture's locked specification in `lattice-architecture.md` §Entity ID Generation: **20 characters drawn from a custom 58-character alphabet that excludes visually ambiguous characters** (`I`, `l`, `O`, `0`). This applies to runtime entities, `op` trackers (whose IDs match the operation's `requestId`), and `meta` meta-vertices uniformly. Deterministic readable IDs are NOT permitted in primary keys — meta-vertex discovery is by `class` + canonicalName aspect, not by key. A separate **8-character NanoID** form from the same alphabet is reserved for human-facing short codes (display references, verbal sharing) and MUST NOT be used as a primary key. Substrate tests MUST include collision-rate validation against the published alphabet and length spec.
- **`<localName>`** — for aspects and links: a lowercase camelCase identifier matching `[a-z][a-zA-Z0-9]*`. Underscore prefix (`_name`) is reserved for platform-generated system metadata; business DDL must not use underscore-prefixed local names.
- **Link directionality** — every link DDL declares its canonical name and direction at **design time**, encoding the typical graph-growth pattern: the link's source side (`<typeA>.<idA>`) is the vertex that is *typically added later* in the graph's lifetime; the target side (`<typeB>.<idB>`) is the vertex that *typically pre-exists* (it was already in the graph when the source side appeared). The convention is semantic, not algorithmic — there is no auto-sort by type, by NanoID, or by `createdAt`. Example:
  - `lnk.identity.<idA>.reportsTo.identity.<idB>` — both endpoints are type `identity`, but the manager identity pre-exists the report: the link points from the report (later-added) to the manager (pre-existing). Same-type links follow the same conceptual rule; runtime callers know which endpoint is which from the operation's semantics, not from string comparison. The name reads as a sentence, "source reportsTo target"; once the link DDL is authored, its direction is fixed.

  Substrate is **direction-agnostic**: `substrate.LinkKey(type1, id1, linkName, type2, id2)` constructs the key in caller-provided order; the substrate does NOT validate or re-sort. The DDL's Starlark script (or other authorized caller) is responsible for emitting endpoints in the DDL-declared direction. The link DDL's `.description` aspect SHOULD document its directional semantics for downstream consumers (FR19 self-description aspect).

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

The `key` echo makes documents self-identifying when read out of context (event payloads, exports,
logs). Documents carry **no `revision` field** — clients read revision from KV metadata (an echoed
copy would lag the actual revision by one).

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

There is **no `direction` field**: direction is fully encoded by segment order in the key (the
`sourceVertex`/`targetVertex` fields reflect the DDL-declared direction); a stored copy would be
redundant and risk drift.

### 1.4 Reserved Underscore-Prefixed Local Names

Aspect and link `localName` values starting with `_` are reserved for platform-generated system metadata. Business DDL must not register classes that would naturally suggest underscore-prefixed local names, and write operations may not produce underscore-prefixed local names from Starlark scripts (Processor rejects at commit step 6).

### 1.5 DDL Lookup at Commit Time

When the Processor validates a mutation (commit step 6), it resolves the DDL for the affected entity by **class-based lookup against the DDL cache**.

**Lookup algorithm:**

1. Read the document's `class` field.
2. Determine the DDL kind from the entity being mutated (vertex / aspect / link).
3. Query the DDL cache: "find a meta-vertex with `class: 'meta.ddl.<kind>Type'` and a `canonicalName` aspect equal to the document's `class`."
4. If found → validate against the resolved schema, enforce `permittedCommands`, apply sensitivity constraints (for aspects).
5. **If not found, resolve the *type authority* by `instanceOf` chain** (vertex mutations only): follow the mutated vertex's live `lnk.<root>.instanceOf.<type>.<id>` link to its target, bounded to `MAX_INSTANCEOF_HOPS` (4) with a visited-set cycle guard. The target may be read from the current atomic batch, the hydrated working set, or on demand; tombstoned `instanceOf` links are ignored. Terminate when the target **is** a `vtx.meta.*` **vertexType** DDL (the type authority) or is a vertex whose own `class` resolves to a vertexType DDL — and validate the mutation against **that** DDL's `permittedCommands`. This lets a fine-grained discriminator class (`service.backgroundCheck.instance`) inherit its governing DDL from the single type it is an instance of, without registering a DDL per subtype. The walk is **fail-open**: a missing/cyclic/over-bound chain falls through to step 6, never to a *wrong* DDL.
6. If neither an exact match (step 3–4) nor an `instanceOf` type authority (step 5) is found → accept the mutation with no schema validation, no `permittedCommands` enforcement, no sensitivity constraint. (Permissive-by-default.)

**Class lookup is exact match** at step 3. Hierarchical class strings (e.g., `identity.ai.onboarding-assistant`) match only DDLs with exactly that canonical name. To validate AI-specific identities under their own rules, operators register a DDL with canonical name `identity.ai.onboarding-assistant`. To use the generic identity DDL, set `class: "identity"`. Prefix matching is not part of Phase 1. **A fine-grained subtype class that is not itself a registered DDL** resolves its governing DDL via the step-5 `instanceOf` chain rather than by prefix — the type relationship is an explicit link, not a string convention.

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

**Consequences for FR57 (write-scope per DDL):** `permittedCommands` enforcement applies to a type that is reachable by class lookup **or** by the §1.5 step-5 `instanceOf` type-authority resolution. A **fine-grained subtype** vertex (a dotted discriminator class with no DDL of its own) is enforced via its `instanceOf` type authority — so the envelope-class discriminator (P7) does *not* turn off `permittedCommands`. A vertex that is neither a declared type nor `instanceOf`-linked to one remains undeclared and bypasses FR57's enforcement, consistent with the permissive model — operators who want strict write-scope register a DDL with `permittedCommands`, or link the subtype's instances to a type that has one. A `permittedCommands` that is absent or empty is **unrestricted** (the permissive default); when present, an `operationType` not in the list rejects the entire operation.

**Consequences for sensitive aspects (PRD Item 6):** a sensitive aspect's **key custody** is declared by its aspect-type DDL (`custody.kind`), found by the same class lookup, and the anchoring rule follows the declared kind: custody kind `identity` (the default when undeclared) requires the aspect to attach to an `identity` vertex; custody kind `retentionClass` permits any anchor and custodies the DEK on the declared retention-class holder. Undeclared aspects have no enforced sensitivity. Operators handling sensitive data must register a DDL with the sensitive flag, and — for data whose retention obligation outlives a data subject's erasure request — with a retention-class custody declaration.

**Consequences for the bypass test suite (NFR-S2, Phase 1 Gate 2):** The "DDL schema violation" bypass category applies to *declared* types. The other three categories (direct KV write, stream publish outside `ops.*`, Starlark I/O escape) are unchanged — they're enforced regardless of DDL state.

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

**Abstract vertex types.** A `meta.ddl.vertexType` meta-vertex whose root `data.abstract` is `true` declares an **abstract** vertex type: a type name that participates in the type taxonomy but has no instances. No key may use an abstract type name in any type segment, and no document may carry it as a `class`; the Processor rejects either at commit. An abstract type declares no `.script` and no `.permittedCommands`. Concrete types are joined to it by `lnk.meta.<concreteTypeMetaId>.subtypeOf.meta.<abstractTypeMetaId>`, whose transitive downward closure is the set of concrete types the abstract name covers. *(Transitional, ratified 2026-08-06: abstract types and the `subtypeOf` relation land with the dynamic-type-taxonomy build; until that fire ships nothing declares one, so every type name is concrete and this clause constrains nothing.)*

**Aspect-, link-, and event-type DDLs follow the same thin-meta-vertex shape** — the class is
`meta.ddl.aspectType` / `meta.ddl.linkType` / `meta.ddl.eventType`, with `canonicalName` / `schema` /
`description` aspects; aspect-type DDLs add `sensitive` (and custody, Contract #3 §3.10), and
aspect-/link-type DDLs may carry `permittedCommands`. An event-type DDL carries no
`permittedCommands` (events are emitted by scripts, not commanded).

**Discovery and bootstrap:**
- DDL meta-vertices are NOT addressable by deterministic key (their IDs are NanoIDs).
- Discovery is by class-based lookup against the Processor's in-memory DDL cache, built at startup by scanning `vtx.meta.>` CDC and maintained incrementally via CDC updates.
- The platform ships with **primordial meta-vertices** that describe the meta-meta layer (the DDL for `meta.ddl.vertexType`, `meta.ddl.aspectType`, etc.). These are seeded by `make up` and are not authored through the write path. Their NanoIDs are fixed for any given platform version.

### 1.8 Worked Example — two aspects, one class

`vtx.identity.<id>.workEmail` and `vtx.identity.<id>.personalEmail` both carry `class: "email"`: both
validate against the `email` aspect-type DDL, inherit its `sensitive: true`, and share its
`permittedCommands` — two emails, distinct `localName`s, one DDL. **`localName` addresses; `class`
classifies.**
