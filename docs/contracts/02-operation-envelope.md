# Contract #2 — Operation Envelope

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
| `contextHint` | optional | object with `reads: string[]`, optional `optionalReads: string[]`, optional `egressReads: string[]`, and optional `enumerations: {hub, relation, direction}[]` | immutable | JIT Hydration directive — declared read set. `reads` lists Core KV keys the script reads whose **absence is a correctness error** (a missing key faults `HydrationMiss`, fail-closed). `optionalReads` lists declared keys whose **absence is a legitimate branch** (read-before-create / dedup) — hydrated if present, recorded as the absent sentinel if missing (never `HydrationMiss`). `egressReads` lists keys read **for external egress** — hydrated fail-closed like `reads`, except a `sensitive: true` aspect (Contract #3 §3.10) hydrates as a **sensitive-ref** (its at-rest ciphertext + the aspect key, Processor-authenticated per the §3.10 ref-provenance rule — never the key envelope, per the §3.10 live-envelope rule), never decrypted plaintext; a non-sensitive key hydrates normally; a key in both `reads` and `egressReads` is an envelope validation error. `enumerations` declares `kv.Links` link-enumerations (§2.5.1) as **metadata** — bounded, paged, **not** hydrated — for the Edge mirror-coverage gate + static classification. Processor pre-fetches `reads`/`optionalReads`/`egressReads` at commit step 4, one Core KV GET per distinct key; their **summed** length MUST NOT exceed **1000** (§2.5 "Declared-read ceiling") or the envelope is rejected `EnvelopeMalformed`. If a read is absent from `contextHint`, the Processor falls back to lazy on-demand reads (latency penalty under load, and not Edge-predictable). See §2.5. |

**`actor` form:** Full vertex key including the `vtx.` prefix. Short forms (`identity.<id>`) are reserved for HTTP headers in Phase 2 (Gateway translates to full key before envelope submission).

**Optional field — `class` (optional, `omitempty`):**

Story 1.6 introduced an optional top-level `class` field on the operation envelope to let the Hydrator resolve the operation's DDL during the window before the full DDL cache could derive class from `operationType`. Story 1.7 brought the DDL cache forward; the operationType→class reverse index it anticipated is now **built** (the DDL cache's `byCommand` map): when an envelope omits `class`, the Hydrator resolves the DDL from `operationType` — the single **vertexType** DDL whose `permittedCommands` admit it. The index is integrity-safe by two disciplines: it is built from **vertexType (script-bearing) DDLs only** (an aspectType DDL lists an op in `permittedCommands` purely as a step-6 write gate, never as the executing script, so it is never a class-inference target), and a **global ambiguity guard** drops any op admitted by more than one vertexType DDL (such an op still requires an explicit `class` — the inference never guesses). Resolution is **auth-neutral**: authorization (step 3) precedes class resolution (step 4) and keys on `operationType` + actor + authContext, never on `class`, so making `class` optional cannot widen the auth surface; the inferred class is exactly the DDL `permittedCommands` step 6 already enforces.

`class` is therefore **engine-optional**: engine-dispatched ops (Loom/Weaver) and clients MAY omit it and rely on operationType→class derivation; an explicit `class` (or `payload.class`) still takes precedence when supplied.

**Discriminator class vs. script-authority DDL (the `instanceOf` type model).** The envelope `class` selects the operation's **script DDL** (which Starlark runs) — resolved here from `operationType`. The **resulting vertex's** stored `class` is its **type/subtype discriminator** (P7), which may be a fine-grained dotted string (`service.backgroundCheck.instance`) that is *not* itself a registered DDL. These are legitimately distinct: an op may write a vertex whose class differs from the op's resolved script-DDL canonical name. The **write-gate** (`permittedCommands`, commit step 6) resolves *that vertex's* governing DDL by its own class — by exact lookup, falling back to its `instanceOf` type authority per Contract #1 §1.5 step 5. Authorization (step 3) keys on `operationType` + actor + `authContext` and is unaffected by either class, so the discriminator never enters the auth path.

| Field | Required | Type | Mutability | Purpose |
|-------|----------|------|------------|---------|
| `class` | optional | string (DDL canonical name, e.g., `"identity"`) | immutable | Tells the Hydrator/Validator which DDL meta-vertex applies to this operation. Precedence: top-level `class` → `payload.class` → derived from `operationType` via the DDL cache's operationType→class reverse index (the single vertexType DDL that admits it; ambiguous/unindexed ops still require an explicit `class`). The field is `omitempty` in the wire format. |

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
| `decision` | for `accepted` | `"committed"` on a fresh commit |
| `revisions` | optional, for `accepted` | Per-key revision map (`{key: revision}`) returned by the substrate after the atomic batch. **The committed mutation key set IS the key set of this map.** Useful for client RYOW polling and for addressing any committed key. |
| `primaryKey` | optional, for `accepted` | The single principal Core KV key the operation wrote (e.g. the created identity/role/permission vertex, or a link key). The Processor **validates that `primaryKey` is within the committed write footprint** — either a committed key, or the 3-segment vertex root of a committed key (so an aspect-only update names its principal vertex, not an internal aspect). A script can only name an entity it actually wrote. Multi-key operations with no single principal entity (InstallPackage / UninstallPackage / MergeIdentity) omit it; clients read the full key set from `revisions`. |
| `error` | for `rejected` | Structured error: `code` (machine-readable), `message` (human-readable), `details` (structured context). Error codes are enumerated; see §2.6. |

There is **no `detail` field**. The reply carries only commit-trace identifiers
the Processor itself produced (`primaryKey`, `revisions`) — never arbitrary,
script-returned data. The write path is not a read channel: read-derived signals
travel on business events (e.g. `IdentityCreated.data.duplicate`), and one-time
secrets are never returned (see §2.7).

### 2.7 Closed `response` script-return schema

A Starlark operation script MAY return a top-level `response` dict to name the
operation's principal committed key. The schema is **closed**: the only permitted
key is `primaryKey` (a string).

- Any other key in `response` is a fail-closed `ScriptFailed` /
  `InvalidReturnShape` error at parse time, before commit.
- When set, the Processor validates `primaryKey` is within the committed write
  footprint — a committed key, or the 3-segment vertex root of a committed key
  (letting aspect-only updates name their principal vertex). Otherwise the
  operation is rejected with `DDLViolation`.
- Absent `response` / absent `primaryKey` is allowed (the reply simply omits
  `primaryKey`).

This makes the synchronous reply incapable of carrying arbitrary or sensitive
data. Claim secrets follow Option C: the **client** mints the secret, submits
only its `sha256` hash (`claimKeyHash`) in the op payload, and Lattice stores the
hash verbatim — the plaintext never enters Lattice and is never returned.

**The reply does NOT wait for:**
- Event publication (step 9) — fire-and-forget after atomic commit
- Projection convergence — client polls `opTrackerKey` for that
- Lens-target store write — client polls the relevant Lens for query convergence

**Why reply after step 8 rather than step 10:** Durability is guaranteed by step 8 (atomic batch with revision conditions). Events are validated *before* step 8 (step 7), so if the operation reached step 8 it produced valid events. Step 9 (publish) is retried on Processor restart via the redelivery + dedup path. The client's "is my operation done?" question is honestly answered at step 8.

### 2.5 Context Hint Semantics

#### Read posture (the declared-read norm)

Write-path Starlark execution is, **to the extent its reads are declared, a pure function of `(op payload, declared+hydrated read-set)`**. That purity is what makes the cloud commit replay-stable, lets the Edge predict an op's result locally (an op is locally predictable iff all its reads are declared and ⊆ the local mirror), and keeps Core-KV reads inside the Processor (no engine reads Core-KV business state). **Declaring every *declarable* read is therefore the norm, not merely an optimization.** A write-path read falls into one of six classes:

| Class | Description | Disposition |
|---|---|---|
| **(a) declared exact-key** | the key is known at submit time and declared in `reads`/`optionalReads`; `kv.Read` serves it from the step-4 hydrated cache | **the norm** — replay-stable, OCC-snapshotted, Edge-predictable |
| **(b) declarable-but-undeclared** | a lazy on-demand `kv.Read` of a knowable key for no reason | **deprecated debt** — should move to (a); subject to a `lint-conventions` flag |
| **(c) deliberately unsnapshotted** | a knowable key read live **on purpose** to keep it *out* of the OCC condition set (e.g. config the op must not falsely conflict on) | **sanctioned, but must be an explicit author choice** (annotated), not a slip |
| **(d) read-before-create / dedup** | a knowable key whose absence is a legitimate branch | declare in **`optionalReads`** (folds into (a) — declared, snapshotted, Edge-predictable) |
| **(e) enumeration + follow-up** | `kv.Links` (§2.5.1) and the data-dependent per-element `kv.Read`s keyed off its results — the key set is data-derived and **unbounded**, so never hydrated | **bounded paged live read, declared as *metadata*** (`contextHint.enumerations`) — the declaration gives the Edge a mirror-coverage gate + the lint a classification; it is **not** a hydration directive. Enumerate-then-write concurrency is best-effort (a companion serialization epoch, class-a); the invariant-enforcer is Weaver detect+recover. |
| **(f) egress read** | a key read **for external egress** (an externalTask param template, §10.5) — declared in **`egressReads`** | hydrated fail-closed like (a), **except** a `sensitive: true` aspect (Contract #3 §3.10) hydrates as a **sensitive-ref** (at-rest ciphertext, Processor-authenticated — MAC'd per the §3.10 ref-provenance rule — never decrypted plaintext; the script cannot leak what it never saw); a non-sensitive key hydrates normally (ref-if-sensitive, so declarers need no sensitivity knowledge). A key in both `reads` and `egressReads` is an envelope validation error. Strictly *less* information than (a) — a self-restriction, not a privilege. |

| **(g) script-derived exact-key** | a key that is a deterministic function of the op payload under *package* semantics (a normalized-contact index hash, a credential index), so the **submitter cannot express it** — declaring it would require the package's derivation and normalization in every client language | **declared by the owning DDL's `derive_reads(op)`**, resolved Processor-side at the head of step 4 before hydration. **Folds into (a)/(d)** — declared, snapshotted, Edge-predictable — and counts toward the declared-read ceiling. |

A script reading only class-(a)/(d)/(g) keys is **replay-stable**. A script performing any (b)/(c)/(e) read is **not** replay-stable — it reads live state and may branch differently on replay; for those, the Processor (deterministic id + OCC + the `CreateOnly` backstop), not replay determinism, is the idempotency authority. The posture does not eliminate non-stable reads; it keeps them **few, named, and statically visible**.

#### `reads` (fail-closed) vs `optionalReads` (absence-tolerant)

The `contextHint.reads` array declares Core KV keys the Starlark script will read. At commit step 4 (Hydrate), the Processor pre-fetches these into the working set cache.

**When provided:**
- Processor fetches every declared key into the working set — one Core KV GET per key, issued sequentially, so the declared set's size is the step-4 round-trip count (see the declared-read ceiling below)
- Working set cache is populated before Starlark execution begins
- Starlark reads hit the cache; no Core KV round-trips during script execution
- Reads of keys NOT in `contextHint` still work (fall through to on-demand fetch) but incur latency

**When absent:**
- Processor uses lazy on-demand reads during Starlark execution
- Each `kv.Read()` call from Starlark performs a Core KV fetch
- Per-operation latency increases proportional to read count
- At MVP scale (10–100 ops/sec) this is tolerable; under sustained load it becomes a bottleneck

**`optionalReads` (absence-tolerant declared reads — class (d)).** A key in `optionalReads` is hydrated exactly like a `reads` key, **except** a not-found is **not** a `HydrationMiss`: the key is recorded as *known-absent*, so `kv.Read(key)` returns `None` from the cache (no live GET). This lets the read-before-create / dedup pattern — "does this entity already exist? if not, create it" — be a **declared** read (replay-stable, OCC-snapshotted, Edge-predictable) rather than a lazy live one. The absent key resolves at the step-4 snapshot and is conditioned create-able; a concurrent create that wins between step 4 and step 8 is caught by the `CreateOnly` backstop (RevisionConflict → re-hydrate → now present → no-op).

**Authoring rule (fail-closed discipline).** A key whose **absence is a correctness error** MUST go in `reads` (fail-closed `HydrationMiss`). `optionalReads` is **only** for a read whose absence is a *legitimate branch*. `optionalReads` must never be used to soften a read the operation's correctness depends on being present.

**When the fail-closed fault is raised (first use, not step 4).** A declared `reads` / `egressReads` key that is absent at the step-4 snapshot is **recorded**, not faulted; the `HydrationMiss` is raised at the first point the operation **depends** on that key — a `kv.Read`, any `state` access that NAMES it (subscript, `in`, `.get()`), or a mutation naming it. An operation that depends on the key is rejected exactly as before, with the same error code and the same `details.missingKey`; an operation that never touches the key is unaffected by its absence, because a key the operation never consumes cannot change its result. **Enumerating `state` is deliberately not a dependence** (`for k in state`, `keys()`, `items()`, `values()`): it names no key, so faulting there would reject on "some declared read was absent" — the caller-visible answer to "does that key exist?", reachable without ever naming the probe. A scan that comes up short gets the truthful answer and handling it is the script's own job. Faulting at step 4 instead made hydration an **existence oracle**: `contextHint` is client-supplied, and step 3 authorizes on `operationType + actor + authContext` without inspecting the declared read set, so a declared-but-absent read answered "does this key exist?" for any actor holding any operation grant, over any key, before a script ran — a step ahead of the operation's own guards. Reaching the fault now requires naming the key in the payload, which is the path those guards stand on. This refines *when* the fault fires; it does **not** relax the authoring rule above.

**`derive_reads` (script-derived declared reads — class (g)).** A DDL script MAY define a top-level
`derive_reads(op)` taking `{operationType, actor, payload}` and returning `{"reads": [...], "optionalReads":
[...]}` (both optional). The Processor runs it at the **head of step 4**, after step-3 authorization and
before the first Core KV GET, and merges the returned keys into the declared set. It exists for the one
class of declarable key a submitter cannot express: a key derived from the payload by the *package's* own
semantics. Its contract:

- **Deterministic.** It runs in the step-5 sandbox with the impure modules bound as **fail-closed stubs**
  that error when called — `kv` (a derivation that reads state is a read, and must be declared as one) and
  `nanoid` (whose PCG is requestId-seeded, so a call here would collide with the main pass's first id).
  Stubs, not unbound names: the sandbox resolves globals at **compile** time, and the pre-pass compiles the
  same module the main pass does. No mutations are emitted; the return value is the entire output. Step 4
  sits inside the OCC retry loop, so a pure derivation recomputes an identical set on every attempt.
- **Validated.** Every returned entry MUST match the Contract #1 key grammar (3-segment vertex, 4-segment
  aspect, 6-segment link); anything else fails the operation closed, naming the derivation.
- **Weakest wins.** A derived entry that collides with a key the envelope already declared keeps the
  **envelope's** disposition — a derived `reads` entry never hardens a declared `optionalReads` key into a
  fail-closed one (that would fault `HydrationMiss` on exactly the dedup branch class (d) exists for).
- **`egressReads` exclusion is re-checked over the merged set.** The parse-time mutual exclusion runs before
  derivation, so the Processor MUST re-check at step 4 and fault closed naming the derivation — otherwise a
  derived key collides with an `egressReads` key, hydrates plaintext, and silently demotes its egress
  disposition.
- **Counted, then faulted at runtime.** Derived keys count toward the declared-read ceiling below; a merged
  set over the ceiling is a **step-4 runtime fault**, not `EnvelopeMalformed` — the keys are not
  envelope-supplied, the same reasoning the live-read budget follows.

An operation whose owning DDL defines no `derive_reads` is unaffected — no invocation, no cost.

**Declared-read ceiling (envelope validation).** `reads` + `optionalReads` + `egressReads`, **summed across the three classes**, MUST NOT exceed **1000** keys (`opwire.MaxDeclaredReads`); an envelope over the ceiling is rejected at parse with `EnvelopeMalformed` (terminal — a redelivery reproduces it). The bound is on the sum because the cost is the sum: step 4 pays one sequential Core KV GET per declared key whether it resolves or is recorded absent, so the declared set alone decides how many round trips an envelope buys before a script runs. `contextHint` is client-supplied and step 3 authorizes on `operationType + actor + authContext` without inspecting it, which leaves that count unpriced for any actor holding any operation grant; a per-class limit would be cleared by spreading the keys across classes.

A key repeated in a list resolves once, so the ceiling bounds round trips and not merely mentions. The value clears the largest read set the platform produces with roughly a factor of two to spare: every fixed dispatcher declares single digits, and the one producer whose set scales with data (MergeIdentity — six literal reads, four `optionalReads`, and one read per enumerated edge of the secondary identity) is held near 500 edges by its own ~998-mutation pre-flight, since re-pointing an edge costs mutations from the same §3.9.1 budget. An operation genuinely needing more declared reads than this should be decomposed, exactly as §3.9.1 already requires of an over-large cascade.

**What the ceiling does not cover.** It caps the hydration one envelope can demand — enough to keep a hostile envelope's step 4 inside the consumer's `AckWait` rather than timing out and re-paying the same hydration on every redelivery. It is **not** a general per-operation read budget: a script's class-(e) `kv.Links` enumerations and the `kv.Read`s that follow them are live reads issued during execution, invisible to envelope validation and unbounded by it. `enumerations` is likewise outside the sum, since a declared enumeration is metadata and hydrates nothing.

**Live-read budget (execution-time, not envelope validation).** A script execution's live Core KV round trips — `kv.Read`'s lazy fallthrough (one GET) and `kv.Links`' list call plus one GET per page slot (§2.5.1, charged at the clamped `limit` rather than the returned link count, so a page racing a concurrent hard-delete cannot under-charge its real cost) — are bounded by a shared per-execution counter, checked at each round trip and never reset mid-execution: a breach aborts the script rather than issuing the read. Unlike the declared-read ceiling this is not an envelope-validation rejection (no `EnvelopeMalformed`) — it is a runtime fault, since the cost is only known as the script enumerates. The default budget (`DefaultLiveReadBudget`, `internal/processor/live_read_budget.go`) is sized with headroom above the platform's own worst case — MergeIdentity's combined `identity_has_open_tasks` + `collect_indexes_repoints` enumeration plus the indexes-repoint follow-up reads, packages/identity-hygiene, ≈49,946 round trips at full fan-out — rather than at the declared-read ceiling's tighter bound; the two ceilings police different costs (one envelope-supplied keys, the other execution-time fan-out) and are not meant to match.

**Convention:** SDK tools and AI agent integrations SHOULD populate `contextHint` whenever the read set is determinable at submission time, declaring class-(d) reads in `optionalReads`. Per the read posture above, declaring every *declarable* read (classes a/d) is the norm; an undeclared knowable read (class b) is deprecated debt, and a deliberately-unsnapshotted read (class c) must be an explicit author choice. Undeclared reads still execute (lazy on-demand), so presence is not hard-enforced at the envelope layer — but it is the expected posture and is `lint-conventions`-checkable.

**Future evolution (post-Phase 1):** Static analysis of Starlark scripts can classify a script's reads (declared / declarable-undeclared / enumeration) — used to lint class-(b) debt and to derive the Edge per-op predictability flag. (Auto-derivation cannot replace declaration for class-(e) data-dependent keys, which are undeclarable by definition.) Not in scope for Phase 1. Static classification and class-(g) `derive_reads` **compose rather than compete**: the former classifies a script's reads without running it, the latter computes keys that depend on the payload's *value* and so no static pass can produce.

**`enumerations` (declared link-enumeration — class (e); metadata, not hydration).** An op that calls `kv.Links` (§2.5.1) declares each enumeration as `{ hub, relation, direction }` in `contextHint.enumerations`. This is **metadata, not a hydration directive** — a high-degree hub's link-set is *never* materialised (that would be unbounded); the enumeration still executes paged and live in the sandbox. The declaration buys (1) the **Edge mirror-coverage gate** — an op is Edge-predictable iff the declared relation is fully in the local mirror, else it degrades to pending — and (2) **static classification** for the read-posture lint. An enumerate-**then-write** should additionally declare a **companion serialization epoch** (a class-(a) scalar every mutator of the relation bumps) in `reads`: this is **best-effort contention reduction**, not a guarantee — the actual double-write invariant-enforcer is **Weaver detect + recover**, not a write-time lock.

### 2.5.1 Bounded Link Enumeration (`kv.Links`)

`contextHint.reads` + `kv.Read` cover only **known-key** reads (a single named key, single-key GET). A guard that enforces a **set** or **range** constraint needs the *set* of a vertex's neighbors — an enumeration whose membership is not known at submission time, so it cannot be pre-declared in `contextHint.reads`. The Starlark `kv` module exposes exactly one such primitive:

```
kv.Links(hubKey, relation, direction, cursor=None, limit=N) -> (page, nextCursor)
#   direction: "out" (hub is the link SOURCE) | "in" (hub is the link TARGET)
#   page:      list[linkDoc] (this page's links); nextCursor: opaque token or None when exhausted
```

It returns the Core KV canonical links incident to `hubKey` under `relation` in the requested `direction`. Each `linkDoc` carries the standard link envelope projection (`key`, `class`, `isDeleted`, `data`, `revision`, `sourceVertex`, `targetVertex`); logically-deleted (tombstoned) links are **returned** carrying `isDeleted` (the script decides), as with `kv.Read`. Order within a page is unspecified.

This is the **one sanctioned relaxation** of the otherwise known-key-reads-only write path. It is bounded, paged, lazy, and scoped:

- **Bound to the hub's vertex id in BOTH directions — a server-side subject-filtered list.** The canonical link key is `lnk.<sourceType>.<sourceId>.<relation>.<targetType>.<targetId>` (Contract #1 §1.1; source first). The hub's vertex id is a **fixed token** in either direction, so the read is bounded by the hub's degree *in that direction*, never the link space:
  - `direction:"out"` (hub is the source) → key filter `lnk.<hubType>.<hubId>.<relation>.>` (hub id in the prefix; `>` matches `<targetType>.<targetId>`).
  - `direction:"in"` (hub is the target) → key filter `lnk.*.*.<relation>.<hubType>.<hubId>` (hub id in the suffix; the two `*` wildcard `<sourceType>.<sourceId>`, one token each; `<relation>` fixed).
  Both are evaluated by NATS as a server-side subject filter (`$KV.<core-kv>.<keyFilter>`) — the server returns only matching keys, so **neither direction scans the keyspace**. The link keeps its natural §1.1 direction; the guard chooses the `direction` matching where the hub sits in that link (the inbound filter relies on NATS subject `*` wildcards being valid at any token position).
- **Paged, not fail-closed-capped.** A high-degree hub (e.g. a service template with many `instanceOf` instances pointing back) is enumerated **page by page** via the opaque `cursor`/`nextCursor` — the call **never silently truncates** and **never fail-closes a legitimately high-degree hub**. `limit` bounds each page; the guard pages until `nextCursor` is `None`. (A guard that must page a very-high-degree hub bears that cost explicitly — a visible authoring choice, not a hidden cap.)
- **Lazy.** The enumeration + per-key reads happen **only when the script calls `kv.Links`**, and only for the pages it pulls — never eagerly pre-hydrated (a wildcard/prefix filter has no exact-key form to pre-declare in `contextHint.reads`). Reads run under the per-invocation wall-budget context and count against the script timeout (NFR-P4).
- **Core KV links only.** `kv.Links` reads **only** the Core KV canonical link keyspace. It never reads the Refractor Adjacency KV (which remains Refractor-private — `lattice-architecture.md`) and never a lens/read-model (P5: applications read lenses; the write path reads its own Core KV). `hubKey` must be a 3-segment vertex key; the constructed filter is always under `lnk.` — no `vtx.`/aspect prefixes, no other bucket.
- **Not a serialization point.** `kv.Links` returns the **currently-committed** matching links; it is **not** snapshot-isolated and does **not** itself serialize concurrent writers, and a paged enumeration may observe an add/remove between pages. A guard enforcing a constraint over the returned set **MUST** additionally contend a shared OCC-guarded key (a per-hub scalar epoch both concurrent writers bump) for correctness: a concurrent mutation bumps the epoch, the step-8 OCC CAS fails, and the op re-hydrates and re-enumerates. The enumeration recovers the *set*; the OCC-guarded scalar recovers the *lock*.
- **Live read, not replay-stable.** Like `kv.Read`, `kv.Links` reads live Core KV — two runs of the same `requestId` may observe different sets. The deterministic id + the step-8 OCC commit are the idempotency authority, not replay determinism.

`kv.Links` is the bounded write-path complement to graph adjacency (the read-side/fan-out role the Refractor Adjacency KV serves). It exists so vertex→vertex relationships live in **links** (§1.1, decision #2), never denormalized as key-lists in aspect `data`. It reads links **in their natural §1.1 direction** — it does **not** require authoring a relation against the growth-order convention.

### 2.6 Error Code Enumeration (Initial Set)

The reply envelope's `error.code` is one of a closed enumeration (the `ErrorCode` type in `package envelope`). The shipped set, by commit step:

| Code | Meaning | Commit Step |
|------|---------|-------------|
| `EnvelopeMalformed` | Operation envelope failed schema validation (missing required field, invalid type, etc.) | Pre-step-1 (Processor entry) |
| `AuthInfrastructureFailure` | The step-3 capability read failed (NATS / Capability KV outage) — the auth plane is unavailable, kept distinct from `InternalError` so callers can tell "auth-plane broken" from other internal failures | Step 3 |
| `LaneUnauthorized` | Actor lacks capability to submit to declared lane | Step 3 |
| `AuthDenied` | Actor lacks capability for operationType on target entities | Step 3 |
| `AuthContextMismatch` | `authContext` declared an auth path that doesn't match the actor's capability projection (e.g., `service` set but service not in `serviceAccess[]`; `task` set but task not in `ephemeralGrants[]` or target mismatch) | Step 3 |
| `HydrationFailed` | A declared read could not be resolved while hydrating operation state (`details`: `code`, `missingKey`) | Step 4 |
| `ScriptFailed` | The operation script raised an error, attempted forbidden I/O, or exceeded its execution budget (NFR-P4) | Step 5 |
| `ClaimKeyInvalid` | Generic `ClaimIdentity` rejection — wrong-key / wrong-state / already-bound / merged all map to this single code (NFR-S6 anti-enumeration); the specific outcome surfaces only via Health KV | Step 5 (ClaimIdentity) |
| `DDLViolation` | A mutation failed DDL validation — JSON-Schema, write-scope (a mutation outside the affected DDL's `permittedCommands`), or sensitive-aspect anchoring | Step 6 |
| `ProtectedKey` | An update or tombstone whose root document carries `data.protected == true`, rejected at commit — the path-independent kernel/auth bricking guard | Step 8 |
| `RevisionConflict` | Atomic batch rejected due to concurrent revision change; retries exhausted | Step 8 |
| `BatchTooLarge` | A single operation's atomic batch exceeded the message-count ceiling (>998 business mutations) or a mutation value exceeded the payload ceiling (`max_payload`). Terminal — no redelivery. `details`: `reason` (`mutationCount`\|`valueSize`), `limit`, `actual`, `key` (valueSize only). See Contract #3 §3.9.1. | Step 8 |
| `InternalError` | Unrecoverable Processor failure not covered by above codes | Any step |
| `CellMoved` | **Reserved (Multi-cell, Phase 3 — not yet emitted):** the target vertex has migrated to another cell and this cell has drained it; the write was not applied. `details.newCell` carries the cell the caller must re-route through — a `410 Gone`-equivalent stray-write rejection (no data lost; the caller re-submits to the correct cell). | Pre-step-1 (cell-router check) |

> **Reconciled to the shipped enum.** `ScriptFailed` subsumes the former split `StarlarkExecutionFailed`/`StarlarkExecutionTimeout`; `DDLViolation` subsumes the former split `SchemaViolation`/`WriteScopeViolation`/`SensitivityViolation`. The former `EventSchemaViolation` and `MetaLaneCollision` are not wire-emitted (event-DDL validation and meta-lane-collision detection are unbuilt — cf. Contract #3 §3.4/§3.8) and are dropped from the closed set. `CellMoved` is documented but reserved for Phase-3 multi-cell (not yet emitted). A conformance test (`internal/processor` `TestConformance_ErrorCodeTable_MatchesWire`) parses this table and fails if it drifts from the emitted `ErrorCode` set again.

Each code is paired with a human-readable `message` and structured `details` appropriate to the failure mode. The enumeration is extensible — Phase 2+ may add codes; a shipped (wire-emitted) code is immutable contract.

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

`authContext.target` is a **client-supplied** field the Processor forwards unexamined on the paths that do not check it (`scope: "any"` and the service path), so an op script must never treat its mere presence as proof of anything. The Processor therefore derives a read-only boolean the script sees as **`op.authTargetValidated`**: true exactly when the auth path that matched *validated* the target — `scope: "self"` (target proven `== actor`) or the task path (target proven `==` the matched `ephemeralGrant.target`) — and false everywhere else, including the service path and the stub authorizer. It is not an envelope input: a client-supplied value is dropped, and the Processor sets it after step 3. A guard that exempts a caller from a confinement rule keys on `op.authTargetValidated`; a guard that merely needs to know which entity the caller named may still read `op.authContextTarget`.

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

**Phase 2 amendment — generic auth-hook dispatcher, one-key-per-path (Story 12.5, D-CONSUMER).** As the
bootstrap god-cypher decomposes into package-owned disjoint Capability-KV keys (Contract #6 §6.1, Epic
12), step-3 stops scanning sections of a single `cap.<actor>` document and instead **dispatches over a
data-driven registry**. The model (party-review-pinned):

- **Core owns a fixed set of matcher *kinds*** — the existing `task` (ephemeral-grant), `service`
  (service-access), and `platform` (platform-permission) logics become the seed matcher kinds,
  re-expressed with **identical** behavior. Matcher kinds are core Go; Lattice packages remain
  **data-only** and never ship matcher code.
- **A package declares, as install-time data**, which matcher kind authorizes its grant type and which
  **disjoint Capability-KV key** that path reads (+ the field mapping). The dispatch table is data, not
  a `switch` naming `task`/`service`.
- **One-key-per-path invariant (preserves the single-GET hot path):** path selection happens **before**
  the read (as today), and each path maps to **exactly one** disjoint key — so exactly one GET per
  `Authorize` call. **Two packages contributing the same path is a config error** (or requires upstream
  merge); the dispatcher never fans a single path into N reads. The denial-path `actorRoles` second
  read stays off the hot path. **The one bounded exception is the system-actor platform path:** a
  kernel-seeded system actor's platform read (an identity holding the primordial `operator` role via
  `holdsRole` — the Contract #7 §7.7 root topology) unions **two** disjoint core keys — the
  rbac-independent anchor `cap.<actor>` (privileged lanes + bootstrap ops) and the rbac-derived
  `cap.roles.<actor>` (operator-granted package ops) — because a system actor legitimately spans both
  planes and they cannot merge into one key without re-coupling the floor to the operator graph
  (Contract #6 §6.1). This exception is **core-internal to the platform path** (not a package-contributed
  fan-out — the config-error guard on *package* paths is unchanged), **deny-closed** (grant iff some
  slice grants; both absent → deny), and **bounded to the kernel-seeded root-actor set** on
  engine/background ops. The **ordinary-actor** platform path and every scoped path remain strictly one-key — the user hot
  path is untouched.

The precedence order (task → service → platform) and the forgery-resistance property below are
unchanged. The dispatch pseudocode above describes the Phase-1 single-document form; the Phase-2 form
reads the path-specific disjoint key via the registered hook. Full shape: Contract #6 §6.1/§6.13 +
`cmd/processor/CONTRACT-AMENDMENT-REQUEST.md`.

**Forgery resistance:**

`authContext` is a *hint about which auth path to check*, not a claim of authorization. An actor can submit any value in `authContext.service` — but unless that service appears in their actual `serviceAccess[]` projection (produced by the Capability Lens), the check fails. The routing-via-`authContext` does not grant access; it only selects which subsection of the capability projection to consult. Bypass test suite (Story 1.11 / Story 3.x) MUST include test cases proving that mismatched `authContext` values are rejected.

**Worked examples:**

```json
// Service operation (penthouse resident books executive cleaning)
"authContext": { "service": "vtx.service.executive-cleaning-NanoID" }

// Task-derived (manager approves lease application)
"authContext": {
  "task": "vtx.task.Rm7q3pntwzkfbcxv5p9j",
  "target": "vtx.lease.op4Nb2mPq6rTwzKxVyP7"
}

// Self-scoped platform operation (resident updates own email)
"authContext": { "target": "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y" }

// Unscoped platform operation (admin creates new DDL) — authContext omitted entirely
```

### 2.9 Implementation Notes

**For the AI agent implementing Story 1.5 (`internal/substrate`):**

- `package envelope` — Go struct definitions for `OperationEnvelope` and `OperationReply`, including the enumerated `Lane` and `Status` types and the `ErrorCode` enum. JSON parsing validates required fields but is **lenient on unknown fields** — they are ignored, not rejected (`ParseEnvelope` uses a standard decoder without `DisallowUnknownFields`), so an envelope carrying a newer optional field stays forward-compatible.
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
