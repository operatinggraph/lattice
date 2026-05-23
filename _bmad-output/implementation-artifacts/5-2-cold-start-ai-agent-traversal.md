# Story 5.2: Cold-Start AI Agent Traversal & Operation Submission

Status: ready-for-dev

## Story

As an AI agent connecting to a Lattice deployment for the first time,
I want to discover my own identity vertex, my permitted operations, and the schemas/descriptions of those operations — purely by traversing the graph from my identity —
so that I can submit a validated intent without any deployment-specific code or out-of-band documentation.

## Acceptance Criteria

1. **Cold-start read from Capability KV (FR19):** Given an AI agent identity is seeded with a `holdsRole` link (done by `packages/identity-domain/` install via `packages/rbac-domain/` AssignRole), when the agent connects, its first read is `cap.identity.<actorId>` from Capability KV. This single read returns the full resolved capability set per Contract #6 §6.2 (key, actor, platformPermissions[], serviceAccess[], ephemeralGrants[], roles, lanes, projectedAt, projectedFromRevisions).

2. **DDL meta-vertex discovery (FR19):** Given the agent has its capability set, when the agent decides to invoke a specific operation type (a string from `cap.platformPermissions[].operationType`), the agent enumerates all keys in Core KV with prefix `vtx.meta.` using `KVListKeys`, reads each 3-segment key, retrieves the `.canonicalName` aspect, and matches against the operation type it wants to invoke. The resolved DDL meta-vertex key is `vtx.meta.<NanoID>`. The agent then reads the five self-description aspects at `vtx.meta.<NanoID>.{description,inputSchema,outputSchema,fieldDescription,examples}` to obtain sufficient knowledge to construct a valid payload.

3. **Operation submission (FR34 / NFR-S10):** Given the agent constructs an operation envelope per Contract #2 with correct `authContext`, when the agent publishes to `ops.default` (or appropriate lane), the operation flows through Processor exactly as a human-submitted operation — same step 1-10 path, same authorization at step 3, same DDL validation at step 6. No AI-specific code branch exists in the Processor.

4. **FR19 + FR34 north-star integration test:** A test harness:
   a. Spawns a brand-new AI agent identity (`vtx.identity.<aiAgentId>`)
   b. Seeds a NEW operation type meta-vertex post-bootstrap (via `CreateMetaVertex` through the Processor) with a canonical name not known to the test at write-time (resolved by reading the response's `metaKey`)
   c. Grants the agent the new operation via `CreatePermission` + `GrantPermission` + `AssignRole` (or directly via a seeded capability doc)
   d. Waits for Capability KV reprojection (by seeding a cap doc that includes the new operation in platformPermissions[])
   e. The agent reads its cap entry, discovers the new operation in platformPermissions[], reads the DDL meta-vertex via enumeration, constructs a payload conforming to the inputSchema, submits the operation, receives `OutcomeAccepted`

   All without any test-harness-side hardcoded knowledge of the new operation's canonical name or schema — the agent discovers these purely from the graph.

5. **Health KV emission on success:** On successful north-star test run, `health.fr19.cold-start-test` is written to the Health KV bucket with `{"passed": true, "testedAt": "<ISO8601>"}`.

6. **Architectural purity (NFR-S10):** There is zero AI-specific bypass in Processor. `grep -rn "isAIActor|AIAgentBypass|ai-actor-special-case" internal/processor/` returns zero hits.

## Tasks / Subtasks

- [ ] Task 1 — AI agent cold-start traversal helper package (new file `internal/aiagent/traversal.go`)
  - [ ] 1.1 Define `Traverser` struct with `conn *substrate.Conn`, `coreBucket string`, `capBucket string`
  - [ ] 1.2 Implement `ReadCapability(ctx, actorID string) (*processor.CapabilityDoc, error)` — reads `cap.identity.<actorID>` from Capability KV
  - [ ] 1.3 Implement `DiscoverDDL(ctx, operationType string) (ddlKey string, err error)` — enumerates `vtx.meta.*` keys in Core KV, for each 3-segment key reads `.canonicalName` aspect, returns the first match
  - [ ] 1.4 Implement `ReadDDLAspects(ctx, ddlKey string) (*DDLAspects, error)` — reads `.description`, `.inputSchema`, `.outputSchema`, `.fieldDescription`, `.examples` aspects; returns a `DDLAspects` struct with the parsed data fields
  - [ ] 1.5 Define `DDLAspects` struct with fields: `Description string`, `InputSchema string`, `OutputSchema string`, `FieldDescriptions map[string]string`, `Examples []ExampleEntry`; and `ExampleEntry` with `Name`, `Payload map[string]any`, `ExpectedOutcome string`
  - [ ] 1.6 Add `NewTraverser(conn *substrate.Conn, coreBucket, capBucket string) *Traverser` constructor

- [ ] Task 2 — North-star integration test (new file `internal/aiagent/fr19_northstar_test.go`)
  - [ ] 2.1 Use `testutil.SetupPackageTestEnv(t)` for harness setup (embedded NATS + bootstrap + Phase1 packages)
  - [ ] 2.2 Seed a new DDL meta-vertex via `CreateMetaVertex` op (operator cap doc for the seeder actor)
  - [ ] 2.3 Seed a cap doc for the AI agent actor that includes the new operation in `platformPermissions[]`
  - [ ] 2.4 Instantiate `aiagent.NewTraverser(conn, "core-kv", "capability-kv")` with the AI agent's actor ID
  - [ ] 2.5 Call `ReadCapability` and assert the new operation appears in `platformPermissions[]`
  - [ ] 2.6 Call `DiscoverDDL` with the discovered operation type; assert the returned ddlKey is a valid `vtx.meta.<NanoID>` key
  - [ ] 2.7 Call `ReadDDLAspects` and assert all five aspect fields are non-empty; parse `inputSchema` as JSON
  - [ ] 2.8 Construct an operation payload from the `inputSchema` (minimal valid payload satisfying required fields)
  - [ ] 2.9 Submit the operation via `testutil.PublishOp` + `testutil.DriveOne`; assert `OutcomeAccepted`
  - [ ] 2.10 Write `health.fr19.cold-start-test` to Health KV with `{"passed": true, "testedAt": "..."}` using `conn.KVPut`
  - [ ] 2.11 Assert the health key was written successfully

- [ ] Task 3 — Unit tests for traversal helpers (`internal/aiagent/traversal_test.go`)
  - [ ] 3.1 Test `ReadCapability` — happy path returns correct CapabilityDoc; missing key returns error
  - [ ] 3.2 Test `DiscoverDDL` — finds correct DDL when multiple meta-vertices exist; returns error when none match
  - [ ] 3.3 Test `ReadDDLAspects` — all five aspects parsed; missing aspect returns descriptive error

- [ ] Task 4 — Architecture purity verification (closing grepping, not a code change)
  - [ ] 4.1 Verify no new ContextHint fields were introduced
  - [ ] 4.2 Verify grep for `isAIActor|AIAgentBypass|ai-actor-special-case` in `internal/processor/` yields zero hits
  - [ ] 4.3 Verify grep for `AdjacencyReads|LinkScans|ScanPrefixes|WithAdjacencyBucket|AdjacencyForNode|keys_with_prefix` in `internal/` and `cmd/` yields zero operational hits

## Dev Notes

### Overview

Story 5.2 is the FR19 + FR34 north-star: an AI agent traverses the graph cold — no hardcoded knowledge of the deployment — and submits a validated operation. The implementation is **entirely client-side** (new `internal/aiagent/` package). No Processor changes. No new KV buckets. No new ContextHint fields.

The architectural premise: after Story 5.1 seeded all DDL meta-vertices with five self-description aspects, the graph IS the documentation. An AI agent needs only:
1. A NATS connection + credential
2. Its own actor ID

From those two things, it can discover every available operation and construct a valid payload.

**No Processor changes.** NFR-S10 is already satisfied — the Processor has no AI-specific code path. This story only adds `internal/aiagent/` (a client-side library) plus a north-star integration test.

---

### Capability KV Key Shape

Per Contract #6 §6.2 (and `processor.CapabilityDoc`):

```
Capability KV key: cap.identity.<actorId>
```

The key is constructed by the Capability Lens projection. For direct access in tests, `testutil.SeedCapDoc` writes to `HarnessCapBucket` ("capability-kv") at the `doc.Key` field. The doc's `Key` field follows the `cap.identity.<actorId>` pattern.

```go
// Reading Capability KV in the Traverser:
entry, err := conn.KVGet(ctx, capBucket, "cap.identity."+actorID)
var doc processor.CapabilityDoc
json.Unmarshal(entry.Value, &doc)
```

---

### DDL Discovery Algorithm

The epics.md §Story 5.2 specifies Phase 1 traversal as: enumerate `vtx.meta.*` keys via `KVListKeys`, then per-vertex read `.canonicalName` aspect until match.

```go
// DiscoverDDL implementation outline:
keys, _ := conn.KVListKeys(ctx, coreBucket)
for _, key := range keys {
    parts := strings.Split(key, ".")
    if len(parts) != 3 || parts[0] != "vtx" || parts[1] != "meta" {
        continue
    }
    cnKey := key + ".canonicalName"
    entry, err := conn.KVGet(ctx, coreBucket, cnKey)
    if err != nil {
        continue  // some meta-vertices lack canonicalName (e.g., aspect-type vertices with this aspect)
    }
    var cnDoc struct {
        Data struct {
            Value string `json:"value"`
        } `json:"data"`
        IsDeleted bool `json:"isDeleted"`
    }
    if json.Unmarshal(entry.Value, &cnDoc) != nil || cnDoc.IsDeleted {
        continue
    }
    if cnDoc.Data.Value == operationType {
        return key, nil
    }
}
return "", ErrDDLNotFound
```

**Important:** DDL meta-vertices carry a `.canonicalName` aspect with `data: { "value": "<name>" }` per Contract #1 §1.7. The `"value"` key is the standard shape for simple string aspects (confirmed by `packages/rbac-domain/ddls.go` and `internal/bootstrap/primordial.go` seeding).

---

### DDL Aspects Reading

After Story 5.1, every DDL meta-vertex with `class: "meta.ddl.*"` carries:

| Aspect key suffix | data field | Go type |
|---|---|---|
| `.description` | `{"text": "..."}` | string |
| `.inputSchema` | `{"schema": "..."}` | string (JSON Schema) |
| `.outputSchema` | `{"schema": "..."}` | string |
| `.fieldDescription` | `{"fieldDescriptions": {"field": "desc"}}` | map[string]string |
| `.examples` | `{"examples": [...]}` | []ExampleEntry |

```go
// ReadDDLAspects implementation outline:
for _, aspectName := range []string{"description", "inputSchema", "outputSchema", "fieldDescription", "examples"} {
    entry, err := conn.KVGet(ctx, coreBucket, ddlKey+"."+aspectName)
    // ... parse each doc struct ...
}
```

---

### North-Star Test Design

The test must prove the agent discovers the DDL **without hardcoded knowledge** of the operation's canonical name. The design:

1. **Seeder picks a unique random-ish canonical name** at test time (e.g. `"NorthStarOp" + time.Now().Format("150405")` — any unique string works since each test run gets a fresh embedded NATS).

2. **Seeder submits `CreateMetaVertex`** as the operator actor (which needs `CreateMetaVertex` permission in its cap doc). The Processor writes `vtx.meta.<NanoID>`. The test captures the `metaKey` from the tracker's `mutationKeys` field.

3. **Agent's cap doc** is seeded with the new operation in `platformPermissions[]`. Since Refractor is not running in the test harness, cap docs are seeded directly via `testutil.SeedCapDoc`. The agent's cap doc does NOT hardcode the meta-vertex key — it only hardcodes the `operationType` string (same string the seeder used).

4. **Traverser discovers the DDL** by enumerating all `vtx.meta.*` keys and matching `.canonicalName`. It does NOT use the meta-vertex key that was captured in step 2 — it finds it from the graph. This is the key assertion: the traverser returns the same key the seeder got.

5. **Traverser reads `inputSchema`** and parses the required fields. The seeder wrote `inputSchema` as a minimal JSON Schema when submitting `CreateMetaVertex`. The agent builds a payload satisfying those required fields.

6. **Agent submits the op** with its own actor key and the discovered operation type.

**DDL Starlark script for the new op:** The `CreateMetaVertex` payload must include a `script` field. For the north-star test, the script just emits an empty MutationBatch (no side effects needed — the test only cares that the Processor accepted the operation):

```python
def execute(state, op):
    return {"mutations": [], "events": []}
```

The `permittedCommands` in the DDL payload must include the new operation type.

---

### Minimal DDL Payload for CreateMetaVertex

The seeder must provide all 9 fields the Starlark meta-DDL script requires (per Story 5.1 `MissingSelfDescription` enforcement):

```go
canonicalName := "NorthStarOp" + uniqueSuffix   // e.g. timestamp
payload := map[string]any{
    "targetClass":      "meta.ddl.vertexType",
    "canonicalName":    canonicalName,
    "permittedCommands": []string{canonicalName},  // the op type == canonical name
    "description":      "North-star test DDL for FR19 cold-start traversal verification.",
    "script":           "def execute(state, op):\n    return {\"mutations\": [], \"events\": []}",
    "inputSchema":      `{"type":"object","properties":{"note":{"type":"string"}},"required":[]}`,
    "outputSchema":     `{"type":"object","properties":{}}`,
    "fieldDescription": map[string]any{"note": "Optional free-text note for the north-star test operation."},
    "examples": []any{map[string]any{
        "name":            "north-star-example",
        "payload":         map[string]any{"note": "hello lattice"},
        "expectedOutcome": "Accepted by Processor via AI agent cold-start path.",
    }},
}
```

Using `canonicalName` as the single `permittedCommands` entry means the operation type to submit equals the DDL's canonical name — keeping the test simple.

---

### Cap Doc for the AI Agent

The seeder actor (operator) needs `CreateMetaVertex` in its cap doc. The AI agent actor needs the new `canonicalName` operation in its cap doc.

```go
// Seeder cap doc — must include CreateMetaVertex
seederCapDoc := &processor.CapabilityDoc{
    Key:   "cap.identity." + seederActorID,
    Actor: "vtx.identity." + seederActorID,
    // ...
    PlatformPermissions: []processor.PlatformPermission{
        {OperationType: "CreateMetaVertex", Scope: "any"},
    },
    Lanes: []string{"default"},
}

// AI agent cap doc — built after we know the canonicalName
agentCapDoc := &processor.CapabilityDoc{
    Key:   "cap.identity." + agentActorID,
    Actor: "vtx.identity." + agentActorID,
    // ...
    PlatformPermissions: []processor.PlatformPermission{
        {OperationType: canonicalName, Scope: "any"},
    },
    Lanes: []string{"default"},
}
```

---

### Lane for CreateMetaVertex

Per Contract #2 §2.3: DDL changes must go on the `meta` lane (`ops.meta.>`). The `CapabilityPipeline` helper in `testutil/pipeline.go` subscribes to `ops.default`. For meta-lane operations we need a separate pipeline or we configure `FilterSubjects: []string{"ops.meta"}`.

Looking at `testutil.CapabilityPipeline`:
- It creates a consumer filtered to `ops.default`
- For `CreateMetaVertex` on the `meta` lane we need `FilterSubjects: []string{"ops.meta"}`

Use `testutil.CapabilityPipeline` with a different durable configured to filter `ops.meta`:

```go
// Meta-lane pipeline for seeding the new DDL:
metaCP, metaCons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
    Durable:  "ns-meta-pipeline",
    Instance: "ns-meta",
})
// But the consumer filter must be updated to ops.meta — this requires
// a custom pipeline or updating CapabilityPipeline to accept FilterSubjects.
```

**Simpler approach:** Since `testutil.CapabilityPipeline` hardcodes `FilterSubjects: []string{"ops.default"}`, use `testutil.PublishOp` with `env.Lane = processor.LaneMeta` AND configure the consumer to match. 

Alternatively, create a small helper in the test file itself that builds a CommitPath with a meta-lane consumer. Looking at `processor.EnsureConsumer`, this is straightforward.

**Simplest approach for the test:** Use TWO separate pipelines — one for meta-lane (CreateMetaVertex seeding by operator) and one for default-lane (operation submission by AI agent). Build each with `testutil.CapabilityPipeline` but pass custom `FilterSubjects`. Since `testutil.PipelineConfig` doesn't have a `FilterSubjects` field yet, we'll add it (see Task 1.x below — actually this is a minimal additive change to testutil).

Actually, re-reading the code: `testutil.CapabilityPipeline` calls `processor.EnsureConsumer` with `FilterSubjects: []string{"ops.default"}`. We need to support `ops.meta` too. 

**Implementation decision:** Add an optional `FilterSubjects []string` field to `PipelineConfig`. If empty, default to `[]string{"ops.default"}` (backward compatible). This is a one-line addition.

Revise Task list to include this:

- [ ] Task 1.x (actually Task 5) — Extend `testutil.PipelineConfig` with optional `FilterSubjects []string`; default `["ops.default"]` if empty (backward compatible, no existing tests break)

---

### File Layout

```
internal/aiagent/
  traversal.go        — Traverser, DDLAspects, ExampleEntry, constructors, methods
  traversal_test.go   — unit tests
  fr19_northstar_test.go — north-star integration test (package aiagent_test)
```

No new packages, no new KV buckets, no new Processor code. The `internal/aiagent` package imports `internal/substrate`, `internal/processor` (for CapabilityDoc only), `internal/testutil` (test files only).

---

### OperationEnvelope Lane Field

Per `processor.OperationEnvelope`:
```go
Lane          Lane   `json:"lane,omitempty"`
```

`processor.LaneMeta` = `"meta"`, `processor.LaneDefault` = `"default"`.

The seeder uses `LaneMeta`; the AI agent uses `LaneDefault`.

---

### Architecture Compliance Checklist

- No new Processor files or changes
- No new ContextHint fields (the Traverser client-side reads are direct KV GETs, not Processor reads)
- No new KV buckets
- No adjacency reads from the traversal path
- No lens-output reads beyond Capability KV (which is the one contract-defined exception)
- NFR-S10: AI agent submits through exact same Processor path as human — no bypass code

---

### References

- [Source: `_bmad-output/planning-artifacts/epics.md` §Story 5.2]
- [Source: `_bmad-output/planning-artifacts/data-contracts.md` Contract #1 §1.7, Contract #2, Contract #6 §6.2]
- [Source: `internal/processor/capability_doc.go` — CapabilityDoc Go type]
- [Source: `internal/substrate/kv.go` — KVGet, KVListKeys, KVPut]
- [Source: `internal/testutil/pipeline.go` — SetupPackageTestEnv, CapabilityPipeline, SeedCapDoc, PublishOp, DriveOne, PipelineConfig]
- [Source: `internal/testutil/embedded_nats.go` — StartEmbeddedNATS, DriveOne, GenReqID]
- [Source: `internal/processor/commit_path.go` — OutcomeAccepted, OutcomeRejected, LaneMeta, LaneDefault]
- [Source: `internal/bootstrap/nanoid.go` — AspectTypeDescriptionKey etc. (post-5.1 primordial IDs)]
- [Source: `packages/rbac-domain/testhelpers_test.go` — cap doc seeding pattern]
- [Source: `internal/bootstrap/self_description_e2e_test.go` — SetupPackageTestEnv + DDL meta-vertex enumeration pattern]

## Dev Agent Record

### Agent Model Used

claude-sonnet-4-6

### Debug Log References

### Completion Notes List

**Sub-agent run** (Sonnet, interrupted on token cap before `bmad-code-review` step):
- Authored `internal/aiagent/traversal.go` (3 cold-start primitives: `ReadCapability`, `DiscoverDDL`, `ReadDDLAspects`)
- Authored `internal/aiagent/traversal_test.go` (unit tests for each primitive)
- Authored `internal/aiagent/fr19_northstar_test.go` (full FR19 end-to-end + NFR-S10 negative test)
- Extended `internal/testutil/pipeline.go` with `PipelineConfig.FilterSubjects` so tests can wire meta-lane pipelines

**Winston correction (post-interruption code review)**: the sub-agent's
`buildPayloadFromSchema` helper produced an empty payload because the
seeded north-star DDL declared `required: []`. That degenerated the
FR19 test — the whole point is that the agent must *consume* the
inputSchema to build a valid payload. Reworked:
- Seeded DDL now declares `required: ["title", "year"]` with three
  typed properties (`title` string, `year` integer, `isbn` optional
  string) and a Starlark script that reads + type-checks all three and
  emits a `BookRegistered` event carrying the values.
- `buildPayloadFromSchema` now walks `required` + `properties.type`
  and produces typed values per property type
  (string→`"northstar-<field>"`, integer→2026, number→1.0, boolean→true).
  Empty `required` triggers `t.Fatal` with a message guarding against
  future regression to the no-op shape.

### File List

- `internal/aiagent/traversal.go` (new)
- `internal/aiagent/traversal_test.go` (new)
- `internal/aiagent/fr19_northstar_test.go` (new)
- `internal/testutil/pipeline.go` (modified: added `FilterSubjects` to `PipelineConfig`)
