# Contract / Planning Artifact Amendment Requests (Story 2.1)

These are planning-artifact-only corrections discovered during Story 2.1 morph work. Each is a text-only fix in `_bmad-output/planning-artifacts/epics.md`; no code impact.

---

## Request 1: epics.md AC #1 binary name

**Location:** `epics.md` Story 2.1 AC #1
**Current text:** "binary is `lattice-refractor`"
**Requested text:** "binary is `refractor`"
**Rationale:** All other Lattice binaries (`bootstrap`, `processor`, `refractor-stub`) use bare component names; no `lattice-` prefix is established convention. Winston ratified `refractor` as the binary name in handoff brief Decision #1.

## Request 2: epics.md AC #2 key prefix table

**Location:** `epics.md` Story 2.1 AC #2 — text mentioning `vtx.*`, `asp.*`, `lnk.*` prefix routing
**Current text:** Implies `asp.*` is a top-level key prefix for aspects.
**Requested text:** Replace with reference to `data-contracts.md` Contract #1 §1.5: aspects are keyed `vtx.<type>.<id>.<localName>` (4-segment `vtx.` prefix), NOT `asp.*`. Classification is by key SHAPE (segment count) and the value document's `class` field, not by raw prefix string matching. Use `substrate.ClassifyKey`.
**Rationale:** `asp.*` does not exist in the data contract. The stale text predates the contract finalization.

## Request 3 [Story 2.1b — RESOLVED]: data-contracts.md §1.7 meta-vertex key shape

**Location:** `data-contracts.md` Contract #1 §1.7 (meta-vertex pattern)
**Issue:** The handoff brief Decision #5 references `vtx.meta.lens.<NanoID>` as the lens-definition key. This is a 4-segment key, which `substrate.ClassifyKey` treats as an ASPECT, not a vertex. Story 2.1 used `vtx.lens.<NanoID>` (3-segment) instead.
**Requested clarification:** Either (a) confirm §1.7 actually uses the 3-segment shape `vtx.<reservedType>.<id>` where `meta` is a type-prefix convention (in which case the brief wording is just imprecise), or (b) define the multi-segment meta-vertex shape as a substrate extension with explicit support in `ClassifyKey`. See MORPH-DEVIATIONS Deviation 12.

**Resolution (Story 2.1b):** Option (a) confirmed by data-contracts.md §1.2 line 70: "`lens`, `event`, `ddl`, `actor` — these are *flavors of `meta`*, distinguished by the document's `class` field (`meta.lens`, `meta.event.<name>`, `meta.ddl.vertexType`, etc.)" The canonical lens key shape is `vtx.meta.<NanoID>` (3-segment vertex with type `meta`) with the document envelope's `class` field set to `"meta.lens"`. No amendment to `data-contracts.md` is needed; the Story 2.1 handoff brief Decision #5 wording was just imprecise (it said `vtx.meta.lens.<NanoID>` when it should have said `vtx.meta.<NanoID>` with class `meta.lens`). Story 2.1b corrected the implementation accordingly — see MORPH-DEVIATIONS Deviation 12 resolution. The 3-segment shape with class-based routing is also what `internal/bootstrap/primordial.go` already uses for the Capability Lens (line 322: `MakeVertexEnvelope(CapabilityLensKey, "meta.lens", ...)`), confirming the pattern across Lattice components.

---

# Epic 12 — Projection-plane integrity & capability decomposition (proposed 2026-06-07)

> **✅ RATIFIED 2026-06-13 (Andrew, planning lead).** Requests 4, 4b, 5, 6, 7 are ratified and the
> amendments are **applied to the frozen contracts ahead of implementation**: Contract #6 §6.1/§6.2/§6.3/§6.6/§6.8/§6.13
> ([docs/contracts/06-capability-kv.md](../../docs/contracts/06-capability-kv.md)) and Contract #10 §10.1
> ([docs/contracts/10-orchestration-surfaces.md](../../docs/contracts/10-orchestration-surfaces.md)). The companion
> D-CONSUMER amendment (Story 12.5, Contract #2 §2.8) is ratified + applied via
> [cmd/processor/CONTRACT-AMENDMENT-REQUEST.md](../processor/CONTRACT-AMENDMENT-REQUEST.md). `projectionSeq`
> ratified as the field name. The contracts now *describe the post-Epic-12 shapes ahead of the code*; each
> shape carries its landing-story tag, and the per-story implementation lands the code against the
> already-amended contract (inverting the usual same-operation rule, by planning-lead choice). The
> `lattice-architecture.md` god-cypher open item is left for the planning lead to mark resolved when
> 12.6/12.7 actually land (it is not resolved yet — the god-cypher still exists in code).

Source: Winston architecture session on `_bmad-output/planning-artifacts/refractor-lens-decomposition-brief.md`; rationale in `docs/decisions/projection-plane-decomposition.md`. Contract #6 §6.1/§6.2/§6.3/§6.8/§6.13 are FROZEN — these were amendment *requests*, now ratified by the planning lead and applied.

## Request 4 [Story 12.1 — D-INTEGRITY]: monotonic `projectionSeq` write-ordering guard on the capability plane

**Location:** Contract #6 §6.2 (document shape) + §6.3 (field spec) + §6.8 ("No Entry = No Access").

**Problem.** `internal/refractor/adapter/natskv.go` `Upsert`/`Delete` write unconditionally (last-writer-wins). The pipeline retry queue replays a **captured row** (`pipeline.go` `enqueueRetry` → `a.Upsert(capturedResult.Keys, capturedResult.Row)`), so a stale "open-era" projection can land after a close-`Delete` and **resurrect a revoked ephemeral grant on the security plane** (`cap.ephemeral.<actor>`) — no further CDC event re-deletes it. Confirmed reachable (see decision record).

**Requested amendment:**
1. Add an ordering field: **`projectionSeq`** (integer) = the JetStream stream sequence of the triggering CDC message. Required on the actor-aggregate classes `cap.<actor>`, `cap.ephemeral.<actor>`, `my-tasks.<actor>`. **`cap.role-by-operation.<op>` is excluded** — it is an operation-aggregate (keyed by `operationType`, not actor), with a different resurrection profile (party review, finding #7).
2. Define write-ordering semantics: a projection write to a guarded key is **rejected as an idempotent no-op when `incoming.projectionSeq ≤ stored.projectionSeq`**. The compare-and-set is **atomic against the target key's KV revision** (`Update`/`ExpectedRevision`) with a **bounded re-read-on-conflict loop** — load-bearing because the Refractor retry queue replays writes from a **separate goroutine** (`failure/retry.go:102`) concurrently with the main consumer.
3. §6.8: a **`Delete` on a guarded key is a soft tombstone** carrying `projectionSeq` + `isDeleted:true` (the high-water mark must survive physical absence). Absence and tombstone remain equivalent for auth (both deny) — no step-3 behavior change.
4. **Adapter-interface impact (implementation note, not contract):** `adapter.Adapter.Upsert/Delete` gain the ordering token (or an `EvalResult`-shaped arg); the **Postgres adapter is exempt** (pass-through, no guard); only `NatsKVAdapter` enforces. `EvalResult` gains a `projectionSeq` field so the retry-queue capture replays the *original* (lower) seq.
5. **Rebuild interaction (party review, finding #4):** `Rebuild(truncate=false)` replays historical lower-seq events that the guard would reject against live high-seq watermarks (rebuild silently restores nothing). Resolution: guarded buckets either force `truncate=true` (watermark cleared with data) or rebuild bypasses the guard for the replay — defined and tested in Story 12.1b.

**Rationale.** `projectedAt` is anchor-provenance-derived and is identical across open/close reprojections of the same actor (the actor vertex is unchanged when a task closes), so it cannot order these writes; `projectedFromRevisions` is incomplete (actor+lens only) and ambiguous under source-set shrink. The substrate's stream sequence is a total order that is plan-independent and deterministic-replay-safe. See decision record for the rejected alternatives (brief options b/c).

## Request 4b [Story 12.1a — D-INTEGRITY]: my-tasks tombstone consumer obligation (Contract #10 §10.1)

**Location:** Contract #10 §10.1 (`my-tasks` projection shape).

**Problem.** Story 12.1a changes the `my-tasks` delete from hard-delete (key vanishes) to **soft tombstone** (`{isDeleted:true, projectionSeq}`). Today the only reader is the E2E test; when a real UI/query consumes the `my-tasks` bucket it must skip tombstones or a user sees ghost tasks they already completed (party review, finding #11 — Sally).

**Requested amendment:** record in §10.1 that **`my-tasks` consumers MUST treat an `isDeleted:true` document as absence** (skip it). Forward obligation; no current production reader.

## Request 5 [Story 12.3 — D-PIPELINE]: `projectionKind` meta-lens aspect + declarative Output descriptor

**Location:** Contract #6 §6.13 (Implementation Notes / meta-lens aspect inventory).

**Problem.** A per-actor aggregating lens cannot be added without a core edit (a `case` in `cmd/refractor/main.go` + a wrapper in `internal/refractor/capabilityenv/`) — contradicting the package-layering rule.

**Requested amendment:** define a new optional `meta.lens` aspect **`projectionKind`** with value `"actorAggregate"`, plus a constrained **Output descriptor** (lens-definition aspects) that replaces the Go wrappers: `anchorType`, `outputKeyPattern` (constrained pattern, e.g. `cap.ephemeral.{actorSuffix}`), `bodyColumns`, `emptyBehavior` (`delete`|`softDelete`|`emptyDoc`|`skip`), `realnessFilter` (`{field}` — drop degenerate collect artifacts), `freshness: auto`. When present, Refractor compiles a `ProjectionPlan{Execution, Invalidation, Output}` and drives the lens generically (compiled reverse-traversal invalidation replaces the broad `ActorEnumerator` BFS). An auth-plane actor-aggregate lens whose MATCH uses an uncompilable construct **fails activation** (fail closed); non-security lenses fall back to broad BFS with a warning.

**Rationale.** All four built-in wrappers reduce to this descriptor + the compiled plan; the machinery (simple-engine reverse-traversal + full-engine AST) already exists. See decision record D-PIPELINE.

## Request 6 [Story 12.3 — D-PIPELINE]: widen `projectedFromRevisions` to the full contributing source set

**Location:** Contract #6 §6.3 (`projectedFromRevisions` field).

**Problem.** The field currently stamps only the actor + lens-def revisions (`capabilityenv/envelope.go:99-110`), so it does not reflect the tasks/roles/links the projection actually read — the coherence-window detection the bypass suite relies on is incomplete.

**Requested amendment:** `projectedFromRevisions` MUST cover the source set the compiled plan read for the projection — actor + contributing roles/tasks/services/links. **Scope (party review, finding #10):** v1 covers sources that *contributed a binding*. Covering sources that were *read-then-excluded* (e.g. a now-closed task) requires the full executor to report every Core-KV key it touched-then-dropped (executor instrumentation) — Story 12.3 must state whether that is in-scope or a follow-up. This is the coherence/debug datum and is distinct from the `projectionSeq` ordering guard (Request 4).

**Note:** `projectionKind` (Request 5) covers `actorAggregate`. `capabilityRoleIndex` needs either a second kind (e.g. `operationAggregate`) or a documented bespoke path — it is **not** an `actorAggregate` (Story 12.4; party review finding #7).

## Request 7 [Story 12.6/12.7 — D-PROJECTION]: disjoint-key conventions for decomposed grant projections

**Location:** Contract #6 §6.1 (Bucket and Key Pattern) + the multi-Lens / contract-contribution note.

**Requested amendment:** register the new disjoint key prefixes produced by package-owned grant lenses as the god-cypher decomposes — `cap.roles.<actor>` (rbac-domain role/permission grants, Story 12.6) and a service-access disjoint key (working name `cap.svc.<actor>`, Story 12.7) — mirroring the existing `cap.ephemeral.<actor>` contribution (§6.6 amendment). Record that the bootstrap `capability` cypher shrinks to the primordial-identity anchor (or retires), with core owning only the bucket + key conventions + the step-3 dispatcher. Mark the `lattice-architecture.md` god-cypher open item resolved (planning-lead action).

**Note on `service-location` (Story 12.7).** The `service-location` package **does not exist** — it is specified only as a concept (`packages/service-location/CONCEPT.md`, authored 2026-06-07). 12.7 is two-path: implement `capabilityServiceAccess` → `cap.svc.<actor>` if the package exists, **else just delete** the service/location MATCHes from the bootstrap cypher and leave the `cap.svc.*` key space registered-but-empty for a future service package. The contract amendment should register the `cap.svc.*` prefix regardless (so a later package projects into it with no core/contract churn).

---

# Contract #10 Amendment Request (13.1 — External I/O Bridge package)

Part of the **External I/O Bridge** amendment package (ratify together with the sibling requests in
`cmd/{loom,weaver}/CONTRACT-AMENDMENT-REQUEST.md`; umbrella =
`_bmad-output/planning-artifacts/sprint-change-proposal-2026-06-18.md`). **STATUS: RATIFIED 2026-06-18 (Andrew), Option (b) — APPLIED 2026-06-18**: §10.2 of
`docs/contracts/10-orchestration-surfaces.md` amended (a convergence lens MAY be an `actorAggregate`; the frozen
§10.2 key + `splitRowKey` stay unchanged via an explicit bare-NanoID key column — Option (b)) and the 13.1
revision-history entry added. Raised **before** implementation; the External I/O Bridge
epic builds to the ratified text. This is a **gating** request: the convergence lens (Epic 14, Story 14.4) **cannot be built**
until the key-shape decision below is settled.

This file's block carries the **Contract #10 §10.2** touch (the convergence target-lens key shape vs the
actorAggregate projection key). The companion amendments touch Contract #10 §10.5/§10.6 (`cmd/loom`)
+ §10.3/§10.8 (`cmd/weaver`). The `external` event domain needs **no** Contract #3 amendment (an
ordinary domain under the open `<domain>.<eventName>` model; realized via a package event-type DDL +
the bridge consumer).

## Request E5: §10.2 + the actorAggregate projection — reconcile the convergence-lens key shape (the M2 seam — GATING)

**Location:** §10.2 "Weaver target Lens output" — the **key shape** (`<targetId>.<entityId>`, where
`<entityId>` is a **bare NanoID**) — vs. the **actorAggregate** projection's forced output key
(`{actorSuffix}` = `<type>.<id>`, the post-`vtx.`-prefix actor suffix) defined by Refractor's Output
descriptor (Request 5 above, `projectionKind: "actorAggregate"`) and rendered by `OutputDescriptor.BuildKey`
(`internal/refractor/projection/output.go:155-163`), vs. Weaver's `splitRowKey`
(`internal/weaver/evaluator.go:505-518`), which **rejects** a non-bare-NanoID entity segment and **drops
the row**.

**Current text (§10.2, the key shape — read as normative):**

> **Key on the entity *ID*, not the full vertex key.** A candidate entity is **always a vertex** … so its
> key is always `vtx.<type>.<id>`. The dotted full key must **not** be embedded in the NATS KV key … Within
> a `<targetId>.` partition every candidate is the same type, so the type segment is redundant: the entity
> segment is just the **NanoID**. …
>
> ```
> bucket:  weaver-targets
> key:     <targetId>.<entityId>                       # e.g. leaseApplicationComplete.Lk2Pn6mQrtwzKbcXvP3T
> ```

**Current text (§10.2 ↔ §10.8 binding):**

> **`targetId` is the single binding token:** it is *both* this vertex's id *and* the `weaver-targets` key
> prefix the `lensRef`'d Lens projects rows under (`<targetId>.<entityId>`). They must match …

**Current behaviour (the incompatibility):**
- `OutputDescriptor.BuildKey` (`output.go:157-162`) renders an actorAggregate key by substituting
  `{actorSuffix}` with **the actor key minus its `vtx.` prefix** — i.e. `<type>.<id>` (e.g.
  `leaseApp.Lk2Pn6mQrtwzKbcXvP3T`), with **no `IntoKey` / escape** to emit a bare NanoID.
- Weaver's `splitRowKey` (`evaluator.go:508-518`) splits at the **first** `.`, then requires
  `substrate.IsValidNanoID(entityID)` (lines 514-516); a `<type>.<id>` entity segment (which itself
  contains a `.`) **fails** that check → `ok=false` → **the row is dropped** → the gap **never converges**.

So a convergence lens that needs to be an **actorAggregate** (for multi-vertex reprojection) emits a key
shape §10.2 + `splitRowKey` reject — the M2 incompatibility. This is **promoted to a gating decision**
(was "small; possibly a §10.2 clarification" in §4C of the proposal): the convergence lens cannot be
built until it is resolved.

**Requested text — a convergence target lens MAY be an `actorAggregate`** (needed because a leaseApp
convergence target now reads identity aspects + a service-instance vertex **across links**, which the
plain projection — reprojecting only its own anchor — cannot fan out to; only actorAggregate's
link-walking enumerator reprojects on any constituent change). Resolve the key incompatibility by **one
of two options:**

**Option (a) — Weaver `splitRowKey` accepts `<type>.<id>`; re-spec §10.2's `<entityId>`.** Change
`splitRowKey` (`evaluator.go`) to accept a `<type>.<id>` entity segment (split at the **last** `.`, or
validate `<type>` + `<id>` as a 2-segment suffix) and re-spec §10.2's key as
`<targetId>.<entityKey-suffix>` where the entity segment **MAY** carry the `<type>.<id>` shape an
actorAggregate emits (the bare-NanoID form stays valid for plain projections). `entityKey` in the
document remains the source of truth, unchanged.
- *Pro:* no projection change; actorAggregate's existing `{actorSuffix}` output works as-is; one
  well-scoped engine + contract edit.
- *Con:* widens the §10.2 key shape (the "entity segment is just the NanoID, dots are subject-token
  separators → brittle" reasoning was deliberate); `splitRowKey` must handle both shapes; touches the
  frozen §10.2 key spec that several Weaver call sites assume.

**Option (b) — the actorAggregate projection honors an explicit key column for `weaver-targets` lenses.**
Extend the Output descriptor (Request 5's `projectionKind: "actorAggregate"` machinery) so a
`weaver-targets`-destined actorAggregate lens may declare an explicit **key column** (the bare-NanoID
`<id>`) that `BuildKey` emits **instead of** `{actorSuffix}`, keeping §10.2's `<targetId>.<entityId>`
bare-NanoID key intact.
- *Pro:* §10.2 and `splitRowKey` are **untouched** (the frozen key shape and its brittleness reasoning
  stand); the change is localized to the projection descriptor that Epic 12 already owns.
- *Con:* a new descriptor knob (key-column override) on the actorAggregate Output path; the projection
  must thread a non-`{actorSuffix}` key for this lens class only (a special case in an otherwise uniform
  key-rendering path).

**Recommendation: Option (b).** It leaves the **frozen** §10.2 key contract and `splitRowKey`
**unchanged** (no widening of a frozen shape, no two-shape branch in multiple Weaver call sites), and
lands the change in the **Refractor Output-descriptor machinery that Epic 12 just introduced** for
exactly this kind of declarative projection control (Request 5 above, `projectionKind`/Output descriptor)
— the smaller, better-contained blast radius. The cost is one descriptor knob (an explicit key-column
override) versus Option (a)'s widening of the §10.2 key shape that the entity-ID-discipline reasoning
(dots are subject-token separators) was specifically chosen to avoid. **Andrew decides between (a) and
(b) at ratification; the convergence lens (Epic 14, Story 14.4) does not start until it is settled.**

**Rationale:** a leaseApp convergence target now reads **identity aspects + a service-instance vertex
across links** (`MATCH (app)-[:applicationFor]->(id), (id)<-[:providedTo]-(inst:service)`), which requires
actorAggregate **fan-out** — the plain `nats_kv` projection only reprojects its own anchor vertex, so a
change to a *linked* constituent (the service instance flipping to `complete`) would not retrigger the
convergence row. But actorAggregate's forced `{actorSuffix}` = `<type>.<id>` key collides with §10.2's
bare-NanoID `<entityId>` **and** `splitRowKey`'s NanoID validation (which silently drops the row). Either
the consumer (Weaver `splitRowKey` + §10.2) widens to accept the type segment, or the producer (the
actorAggregate projection) is steered to emit the bare-NanoID key for this lens class — both reconcile
the seam; (b) does so without touching a frozen contract. **Security-adjacent** (a non-auth
`weaver-targets` lens, so the actorAggregate BFS fallback is safe — proposal §4 Refractor note) — but
because it touches the frozen §10.2 contract under Option (a), full 3-layer adversarial review applies
when the convergence lens lands.

## Request E6: actorAggregate projection — SCALAR body columns for a §10.2 convergence lens (discovered Story 14.4 — BLOCKING)

> **STATUS: OPEN — raised during Story 14.4 implementation (2026-06-18).** Request E5 (Option (b),
> ratified + applied via Story 14.2) closed the convergence-lens **key** seam (the bare-NanoID
> `<targetId>.<entityId>` key, via `OutputDescriptor.KeyColumn` / `BuildKey`). This request covers the
> **body** seam E5 did not address: the §10.2 row's **scalar** columns. **The 14.4 convergence lens
> cannot project a Weaver-readable row until this is resolved.** Surfaced, not implemented (Story 14.4 is
> package content; this is a Refractor `internal/` change — a CONTRACT-AMENDMENT-REQUEST, not an in-flight
> edit).

**Location:** Contract #6 §6.13 (the actorAggregate Output descriptor) + `internal/refractor/projection/output.go`
(`OutputDescriptor.EnvelopeFn` ~39-110, `RealnessFiltered` ~204-235) + `driver.go` (~58-99).

**Problem.** The §10.2 convergence row carries **scalar** columns: `violating` (bool), each `missing_<gap>`
(bool), and the `row.<col>` param columns the §10.8 playbook templates (`entityKey`, `applicant` — strings).
Weaver reads them as scalars: `boolColumn` (`internal/weaver/evaluator.go:452-464`) requires a Go **`bool`**
(a non-bool value is logged as a `RowDataError` and treated as not-actionable), and the param columns are
resolved as strings (`strategist.go` `resolveStringParam`).

But the actorAggregate `EnvelopeFn` runs **every** declared `BodyColumns` entry through `RealnessFiltered`,
which does `collect.([]any)` and returns `nil` for any non-list value (`output.go:216-219`); the driver then
coerces `nil` → `[]` (`driver.go` / `output.go:62-66`). So a scalar body column projects into the
`weaver-targets` document as **`[]`** (an empty list), not the scalar. Confirmed empirically against the
live `InstallPackage` → `InstallActorAggregate` → pipeline path: a lens returning `true AS violating, true
AS missing_signature, id.key AS applicant, app.key AS entityKey` projects
`{"violating":[],"missing_signature":[],"applicant":[],"entityKey":[],...}`. Weaver's `boolColumn` then
reads `[]` (not a bool) → never dispatches; the playbook's `row.applicant` resolves to a list → a data error.

This is because the actorAggregate Output path was built for the **roster** lenses (`my-tasks`,
`capabilityEphemeral`), whose body columns are always `collect(DISTINCT {...})` lists; the realness filter
exists to drop the degenerate null-collect artifact. **No body column has ever been a scalar.** 14.2 (E5)
proved only the **key** column with a roster-list body; the scalar-body path was never exercised.

**No package-only workaround exists.** A plain (non-actorAggregate) lens writes the RETURN columns verbatim
(scalars survive — `pipeline.go:122` "a nil EnvelopeFn writes the row verbatim") **but** reprojects only on
its own anchor's change — it would NOT reproject when a *linked* service `.outcome` flips, which is the exact
reason §10.2 (E5) mandates actorAggregate for a convergence lens ("the plain projection … would miss a
linked constituent flipping"). So the lens needs actorAggregate's link-walking reprojection **and** scalar
body columns — and the two are mutually exclusive in the current code.

**Requested amendment — a per-column "scalar / passthrough" mode on the actorAggregate Output descriptor.**
Add a descriptor field (working name **`scalarColumns`**, or a per-column kind) naming the `BodyColumns`
that are **scalar passthroughs**: such a column is projected **verbatim** (the RETURN value as-is), bypassing
`RealnessFiltered`. The list-collect columns (rosters) keep today's realness behavior. The empty-actor
delete path (`EmptyBehavior` / `RealnessFilter`) must still work — for a convergence lens the realness signal
should be a designated scalar (e.g. `entityKey` non-null when the anchor is alive), so the actor-disappearance
retract still fires on the bare-NanoID key.

**Scope notes.**
- The lens **cypher** is correct and proven **one-row-per-anchor** at the rule-engine level (the §0.C guard
  is satisfied): `internal/refractor/ruleengine/full` runs it green in both the all-gaps-open and
  outcome-flipped directions. The gap is purely the **projection envelope** coercion, not the cypher.
- The `leaseApplicationComplete` lens declaration shipped in `packages/lease-signing` is already in the
  Option-(b) shape (keyColumn set, scalar body columns named); it is ready the moment `scalarColumns` lands.
  No package change is needed when it does — only the descriptor field + the EnvelopeFn branch.
- **Security-adjacent but non-auth** (the `weaver-targets` lens is off the read-path), so the same review
  posture as E5 applies. This is a localized change to the Epic-12 Output-descriptor machinery (the same
  surface E5/Request 5 introduced), not a frozen-contract widening.

**Rationale.** §10.2 makes the convergence lens an actorAggregate (E5) for linked-constituent reprojection,
**and** specifies scalar `violating`/`missing_*`/param columns that Weaver reads as scalars. The current
actorAggregate Output path can deliver one but not the other. E5 closed the key seam; E6 closes the body
seam — together they make a §10.2 convergence lens projectable end-to-end through Refractor.
