# Story 6.4: "Hello Lattice" Reference Implementation (FR43, FR44, FR55, Phase 1 Gate 5)

Status: review

## Story

As a developer evaluating Lattice,
I want a canonical reference implementation that takes me from `git clone` to a complete vertical slice — one entity type, one Starlark rule, one Lens projection, one AI traversal query — in under 60 minutes,
so that I can verify the platform works as advertised and learn the development model by doing.

## Spec Deviations (read first)

**SD-1 — meta.lens spec format: MetaRootDDLScript stores `source`, CoreKVSource reads `cypherRule`.**

When `CreateMetaVertex` is submitted with `class: meta.lens`, the MetaRootDDLScript stores the `.spec` aspect with `data: {"source": <cypher>, "adapter": ..., "bucket": ..., "engine": ...}`. However, `CoreKVSource.dispatchSpec` unmarshals the aspect `data` field as a `LensSpec` struct whose `CypherRule` field has JSON key `cypherRule`, not `source`. This means a lens created via the standard `CreateMetaVertex` path will silently fail to activate — Refractor logs "cypherRule required" and discards the lens.

**Chosen path (SD-1):** Update `MetaRootDDLScript`'s `meta.lens` branch to store the `.spec` aspect data using the `LensSpec` JSON shape — with `cypherRule`, `targetType`, and `targetConfig` fields — instead of `{"source": ..., "adapter": ..., "bucket": ..., "engine": ...}`. This is a small, correct change to `internal/bootstrap/meta_ddl.go`. It unblocks milestone 4 and is architecturally clean because the spec aspect body is the canonical on-wire LensSpec shape.

The tutorial's `books-lens.yaml` payload must supply the Lens spec body as a JSON string in the `spec` field. At `CreateMetaVertex`, the Starlark writes that string's content verbatim into the `.spec` aspect data as a proper LensSpec JSON object.

Concrete payload shape expected (see Task 2 below for exact YAML):
```yaml
spec: |
  {
    "canonicalName": "books",
    "targetType": "postgres",
    "targetConfig": {"dsn": "postgres://lattice:lattice_dev@localhost:5432/lattice?sslmode=disable", "table": "books", "key": ["book_id"]},
    "cypherRule": "MATCH (b:book) RETURN b.id AS book_id, b.title AS title",
    "engine": "simple"
  }
```

If the Starlark branch is updated per SD-1 to pass through the `spec` string as parsed JSON into the aspect data, this becomes the exact body `dispatchSpec` will see in `data`.

Alternatively, the implementer may pass the spec as a pre-serialized JSON string and have the Starlark branch parse it with `json.decode` (if the Starlark runtime supports it) or store it verbatim. The safest approach: update the Starlark to accept a structured object via the payload's `spec` field (currently a string) and emit the object directly as the `data` map. This removes the serialization ambiguity.

**If updating MetaRootDDLScript is blocked for any reason, halt and file a CONTRACT-AMENDMENT-REQUEST.** Do NOT paper over the gap by patching CoreKVSource to recognise the old `source` key.

---

**SD-2 — Lens `simple` engine + Postgres: the pipeline is wired but Execute() is not on the hot path.**

`simple.Engine.Execute()` explicitly returns an error ("not wired in story 3.1a; callers still invoke simple.Evaluate directly"). The production pipeline (`internal/refractor/pipeline/pipeline.go`) still calls `simple.Evaluate` directly with a `QueryPlan`, not through the engine interface. This means the `simple` engine IS operational for Postgres projections — the pipeline's `evaluateForEntry` function routes to `simple.Evaluate` when `engineKind == "simple"`, which it is by default.

**Conclusion (SD-2):** The `simple` engine with a Postgres adapter IS production-operational for the tutorial. The "not wired" comment in `simple.Engine.Execute()` describes the engine-interface path (used by `full`); the direct `simple.Evaluate` path used by the pipeline is fully functional.

**Tutorial Lens must use `"engine": "simple"`** — not `"full"`. The `full` engine requires openCypher traversal semantics and uses ANTLR; the `simple` engine handles `MATCH (b:book) RETURN b.field AS col` projections directly with the v1 parser. This is the correct and simpler choice for a single-label vertex projection.

---

**SD-3 — AI traversal (milestone 5): standalone Go program, not a CLI subcommand.**

The spec does not specify whether the AI script is a CLI subcommand or standalone binary. The Story 5.2 pattern (`internal/aiagent/fr19_northstar_test.go`) uses `aiagent.Traverser` directly in a test. For the tutorial, a standalone Go program at `examples/hello-lattice/ai-agent.go` is the right choice:

- Mirrors Story 5.2's pattern exactly (instructive for developers reading the tutorial)
- Does not require a new CLI subcommand and avoids dragging 6.1 scope
- Can be run as `go run examples/hello-lattice/ai-agent.go` — no build step required
- Doubles as the programmatic integration test driver (milestone 5 step)

The program uses `aiagent.NewTraverser`, reads the AI agent's capability set, discovers the `CreateBook` DDL, submits a `CreateBook` operation, and confirms the book appears via `lattice query postgres`.

---

**SD-4 — Integration test harness: requires real `make up` infra (not embedded NATS).**

Milestone 4 (Lens projection) and milestone 5 (AI traversal) require a live Refractor + Postgres. The embedded-NATS harness in `internal/testutil` does not run Refractor, so Lens projection cannot be tested there. The integration test therefore requires `make up` infrastructure.

**Chosen path (SD-4):** `internal/hellolattice/hellolattice_test.go` uses build tag `//go:build integration` and requires `NATS_URL`, `POSTGRES_URL` env vars. A new `make test-hello-lattice` target runs it against live infra (after `make up`). CI already provisions the full docker-compose stack — the workflow adds `make test-hello-lattice` after `make up`. Tests that don't need Refractor (milestones 1-3) can use the embedded harness via `testutil.SetupPackageTestEnv`, but milestones 4-5 use the live stack exclusively.

**Practical split:** The integration test file has two parts:
1. `TestHelloLattice_Milestones1to3` — uses embedded NATS + bootstrap, no Refractor required.
2. `TestHelloLattice_Milestones4and5` — tagged `integration`, requires `make up`.

`make test-hello-lattice` runs both parts against live infra (simplest for CI).

---

**SD-5 — `CreateBook` capability: the tutorial actor uses `bootstrap.BootstrapIdentityKey`.**

The spec's milestone 5 says the AI agent's `CreateBook` capability comes from identity-domain or rbac-domain package mechanisms, not a new inline grant. However, for the integration test harness and tutorial demo, the implementer must demonstrate granting `CreateBook` to a new identity. This uses the existing `CreatePermission` + `AssignRole` path via the rbac-domain package (already shipped in 4.7). No new inline grants needed.

For the tutorial's simplicity: the tutorial actor submits as `bootstrap.BootstrapIdentityKey` (the primordial operator identity), which already has `CreateMetaVertex` permission. For `CreateBook`, the tutorial must grant it explicitly. The tutorial walks through this as part of milestone 3 ("Author the Starlark rule and verify the DDL is callable"). The AI agent in milestone 5 uses a separate identity with explicit `CreateBook` capability.

---

**No history comments in code.** The implementer MUST NOT add inline comments that record when or why code was changed — e.g., `// Story 6.4: added this`, `// Replaces old X`, `// Removed in Story Y`, `// Previously did Z`. Git history is the record of change; comments explain what the code does now. This rule applies to all files touched by this story. Reviewers (Winston) will reject history comments at code-review time.

---

## Acceptance Criteria

### AC1 — Milestone 1: Setup (≤ 10 min)

**Given** a developer runs `make up` on a clean machine with Docker installed
**When** they run `lattice health gates`
**Then** the output lists `health.gates.phase1.gate1` through `health.gates.phase1.gate4` as `passed: true` (gates 1-4 were closed in prior stories); `gate5` may appear as `pending` or absent.

**And** `lattice bootstrap verify` exits 0 with all primordial state confirmed.

**Integration test assertion (milestone 1):** `TestHelloLattice_Milestone1_Setup` — runs `lattice health gates` via `exec.Command`; asserts exit code 0 and at least gates 1-4 listed.

---

### AC2 — Milestone 2: Define "book" entity type (≤ 10 min)

**Given** the developer authors `examples/hello-lattice/book-ddl.yaml` (provided in the tutorial):

```yaml
operationType: CreateMetaVertex
lane: meta
actor: vtx.identity.<bootstrapActorId>
payload:
  targetClass: meta.ddl.vertexType
  canonicalName: book
  permittedCommands:
    - CreateBook
  description: |
    Book vertex DDL. A book carries title, author, isbn, and year
    aspects. The CreateBook command requires a non-empty title.
  script: |
    def execute(state, op):
        p = op.payload
        if not hasattr(p, "title") or len(p.title.strip()) == 0:
            fail("InvalidArgument: title: required non-empty string")
        book_id = nanoid.new()
        book_key = "vtx.book." + book_id
        mutations = [
            {"op": "create", "key": book_key,
             "document": {"class": "book", "isDeleted": False,
                          "data": {"title": p.title.strip()}}},
        ]
        events = [{"class": "BookCreated", "data": {"bookKey": book_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"bookKey": book_key}}
  inputSchema: |
    {"type":"object","required":["title"],"properties":{"title":{"type":"string","maxLength":500}}}
  outputSchema: |
    {"type":"object","required":["bookKey"],"properties":{"bookKey":{"type":"string"}}}
  fieldDescription:
    title: "Book title, max 500 characters. Required."
  examples:
    - name: "CreateBook — minimal"
      payload:
        title: "The Pragmatic Programmer"
      expectedOutcome: "Creates vtx.book.<NanoID>; returns bookKey."
```

**When** the developer runs:
```
lattice op submit --file examples/hello-lattice/book-ddl.yaml
```

**Then** the operation is accepted; the reply carries a `requestId` and `opTrackerKey`; the platform writes `vtx.meta.<NanoID>` to Core KV with `class: meta.ddl.vertexType`.

**And** `lattice graph read --canonical book` (or `lattice graph keys vtx.meta.` + manual `lattice graph read <key>`) shows the book DDL meta-vertex with all 9 aspects populated (canonicalName, permittedCommands, description, script, inputSchema, outputSchema, fieldDescription, examples, compensation).

**Integration test assertion (milestone 2):** `TestHelloLattice_Milestone2_DefineDDL` — submits `CreateMetaVertex` via `testutil.PublishOp` on meta lane; asserts `OutcomeAccepted`; reads resulting meta-vertex key; asserts `.canonicalName` aspect has `value: "book"`; asserts `.script` aspect non-empty.

---

### AC3 — Milestone 3: Author Starlark rule and create a book vertex (≤ 10 min)

**Given** the book DDL from AC2 is registered

**When** the developer runs:
```
lattice op submit \
  --operation-type CreateBook \
  --lane default \
  --actor vtx.identity.<bootstrapActorId> \
  --payload '{"title":"The Pragmatic Programmer"}'
```

**Then** the operation is accepted (Processor runs the Starlark `execute` in the book DDL's `.script` aspect); a `vtx.book.<NanoID>` vertex is written to Core KV.

**And** `lattice graph read vtx.book.<NanoID>` shows the book vertex with `class: book`, `data.title: "The Pragmatic Programmer"`.

**And** the tutorial explains: the Starlark script was submitted as part of the DDL in milestone 2 — the `UpdateMetaVertex` path (for updating an existing DDL's script) is available but not required for a fresh DDL.

**Integration test assertion (milestone 3):** `TestHelloLattice_Milestone3_CreateBook` — after milestone 2 setup, submits `CreateBook` via default lane; asserts `OutcomeAccepted`; reads the `bookKey` from the op tracker; reads `vtx.book.<NanoID>` from Core KV; asserts `class == "book"` and `data.title == "The Pragmatic Programmer"`.

---

### AC4 — Milestone 4: Author Lens projection and query Postgres (≤ 10 min)

**Given** the book DDL and at least one book vertex exist (from milestones 2-3)

**When** the developer submits `examples/hello-lattice/books-lens.yaml` via `lattice op submit --file books-lens.yaml`

**Then** the Lens meta-vertex is created at `vtx.meta.<NanoID>` with `class: meta.lens`; the Refractor picks up the new lens via CDC within ≤ 500ms (NFR-P3); the Refractor projects all existing `book` vertices to the Postgres `books` table.

**And** `lattice query postgres "SELECT * FROM books"` returns at least one row with the book created in milestone 3.

The `books-lens.yaml` file (provided in `examples/hello-lattice/`):

```yaml
operationType: CreateMetaVertex
lane: meta
actor: vtx.identity.<bootstrapActorId>
payload:
  targetClass: meta.lens
  canonicalName: books
  description: |
    Projects all book vertices to the Postgres books table.
    Each book becomes one row with book_id and title columns.
  spec: |
    {
      "canonicalName": "books",
      "targetType": "postgres",
      "targetConfig": {
        "dsn": "postgres://lattice:lattice_dev@localhost:5432/lattice?sslmode=disable",
        "table": "books",
        "key": ["book_id"]
      },
      "cypherRule": "MATCH (b:book) RETURN b.id AS book_id, b.title AS title",
      "engine": "simple"
    }
```

**Note on SD-1:** The `spec` field above must be a JSON string that the updated MetaRootDDLScript parses into the aspect data. See SD-1 in Spec Deviations above for the exact change required to `internal/bootstrap/meta_ddl.go`.

**Integration test assertion (milestone 4):** `TestHelloLattice_Milestone4_LensProjection` (build tag: `integration`, requires live Refractor + Postgres) — submits `CreateMetaVertex` for the Lens; polls `health.refractor.*` Health KV for lag=0 (bounded poll, ≤ 500ms, not a fixed sleep per NFR-P3); queries `SELECT * FROM books` via `database/sql`; asserts at least one row with `title = "The Pragmatic Programmer"`.

---

### AC5 — Milestone 5: AI traversal query (≤ 10 min)

**Given** a new identity (AI agent) is created with `CreateBook` capability in its capability document

**When** the developer runs:
```
go run examples/hello-lattice/ai-agent.go
```

**Then** the program:
1. Reads `cap.identity.<agentId>` from Capability KV via `aiagent.NewTraverser`
2. Finds `CreateBook` in `platformPermissions[]`
3. Calls `DiscoverDDL("CreateBook")` — enumerates `vtx.meta.*` keys, matches `.canonicalName == "book"` (or the permittedCommands list) to find the DDL vertex
4. Reads the DDL's `inputSchema` aspect; confirms `title` is required
5. Constructs a `CreateBook` payload: `{"title": "Hello Lattice (AI Agent)"}`
6. Submits the operation via `ops.default` using the agent's actor key
7. Prints the `bookKey` from the operation reply

**And** `lattice query postgres "SELECT * FROM books WHERE title = 'Hello Lattice (AI Agent)'"` returns one row.

**And** `lattice query cap vtx.identity.<agentId>` confirms the agent's capability set includes `CreateBook`.

**Integration test assertion (milestone 5):** `TestHelloLattice_Milestone5_AITraversal` (build tag: `integration`) — runs the ai-agent program logic directly (not via `exec.Command`); asserts the new book vertex exists in Core KV; asserts the row appears in Postgres within ≤ 500ms of submission.

---

### AC6 — Triple role: integration test, onboarding tutorial, live demo

**Given** Story 6.4 is considered complete

**When** the three roles are evaluated:

**Then**:
- **Integration test** (`make test-hello-lattice`): CI runs `TestHelloLattice_Milestone1to5` suite; all assertions pass; suite exits 0; `health.gates.phase1.gate5` is written with `{"passed": true, "completedAt": "<ISO8601>", "commit": "<git sha>"}` to Health KV at test completion.
- **Onboarding tutorial**: `README.md` (created by this story — repo currently has no README) has a "Hello Lattice (60-minute tutorial)" section with five anchored milestone sections (`#milestone-1-setup` through `#milestone-5-ai-traversal`); each milestone's section contains the exact CLI commands to run and expected output.
- **Live demo**: `examples/hello-lattice/` contains all five artefacts (`book-ddl.yaml`, `books-lens.yaml`, `ai-agent.go`, a `Makefile` with `demo` target, and a `README.md` pointing back to the root tutorial). Running `make demo` from `examples/hello-lattice/` against a live `make up` stack executes all five milestones non-interactively.

---

### AC7 — Per-milestone timing (CI)

**Given** `make test-hello-lattice` runs on CI (GitHub Actions, docker-compose stack up)
**When** each milestone test completes
**Then** each milestone completes in ≤ 12 min wall-clock; overall suite ≤ 60 min. Timing is recorded in test output via `t.Logf("milestone %d elapsed: %v", n, elapsed)`.

---

### AC8 — Gate 5 closure artifact (out-of-band)

**Given** the engineering implementation is complete (ACs 1-7 pass)
**When** Gate 5 is formally closed
**Then** `_bmad-output/planning-artifacts/gate5-external-tester-report.md` exists as an **empty report template** created by this story. It awaits completion by an external tester (not on the core Lattice team) who runs the tutorial on a clean machine and records elapsed time and any blockers.

The story is complete when the engineering surface is closed. Gate 5 closure happens after the external tester report is filled in — this is an out-of-band step outside this story's scope.

---

### AC9 — Architecture purity (no new Processor code)

**Given** Story 6.4 ships
**Then**:
- `grep -rn "hello.lattice\|helloLattice\|HelloLattice" internal/processor/` returns zero hits
- `OperationEnvelope` and `OperationReply` are unchanged
- `ContextHint` struct is unchanged
- `make test-bypass` all-DEFENDED (Gate 2 regression)
- `make test-capability-adversarial` all-BLOCKED (Gate 3 regression)
- `make verify-kernel` green
- `golangci-lint run ./...` clean

---

## Architectural Guardrails (non-negotiable)

These are hard stops. Violation → file `CONTRACT-AMENDMENT-REQUEST.md` and halt; do not proceed inline.

**Guardrail 1 — No CLI changes.**
All five tutorial milestones use only `lattice` commands shipped in Story 6.1 (`lattice health gates`, `lattice bootstrap verify`, `lattice op submit`, `lattice graph read`, `lattice graph keys`, `lattice query postgres`, `lattice query cap`, `lattice lens lag`). If a command is missing or broken, file a CONTRACT-AMENDMENT-REQUEST rather than adding or patching CLI code in this story.

**Guardrail 2 — No Processor or Refractor changes (except SD-1).**
The only permitted change to core engine code is the MetaRootDDLScript `meta.lens` branch update in `internal/bootstrap/meta_ddl.go` (SD-1). No changes to `internal/processor/`, no new pipeline code, no new engine code. If anything doesn't work end-to-end, halt; do not paper over with patches in core components.

**Guardrail 3 — "book" DDL lives in `examples/hello-lattice/`, not in bootstrap.**
The book DDL is a normal user-defined DDL submitted via `CreateMetaVertex` at tutorial runtime. It is NOT seeded primordially. Do not add book-related entries to `internal/bootstrap/primordial.go`, `internal/bootstrap/nanoid.go`, or any package DDL file.

**Guardrail 4 — No new inline permissions.**
The AI agent's `CreateBook` capability is granted via `CreatePermission` + `AssignRole` (rbac-domain package path, already operational). Do not mint permissions or grant capabilities via ad-hoc code. If the rbac-domain path is missing something, file a CONTRACT-AMENDMENT-REQUEST.

**Guardrail 5 — Deterministic assertions; no fixed sleeps.**
Any assertion that waits for a Lens projection to complete uses a bounded poll loop (e.g., `testutil.PollUntil` or an inline loop with 10ms sleep + 500ms timeout), not a fixed `time.Sleep`. If NFR-P3 (≤ 500ms projection lag) is violated in CI, surface it as a test failure, not a timeout bump.

---

## Tasks / Subtasks

- [ ] **Task 1 — Fix MetaRootDDLScript `meta.lens` spec format (SD-1, AC4)**
  - [ ] 1.1 In `internal/bootstrap/meta_ddl.go`, update the `meta.lens` branch of `execute()` to store the `.spec` aspect data as a proper `LensSpec`-shaped JSON object rather than `{"source": spec, "adapter": ..., "bucket": ..., "engine": ...}`.

    The updated `make_aspect` call for `.spec` must produce data with `cypherRule` (not `source`), `targetType`, `targetConfig`, and optionally `canonicalName` and `engine`. Accept these as structured fields in the payload rather than a raw string `spec` field. The simplest approach:

    Update the `meta.lens` payload contract: the `CreateMetaVertex` for `meta.lens` now accepts `spec` as either a JSON string (which the Starlark decodes to a dict) or structured sub-fields. Recommended: accept `spec` as a JSON string containing the full LensSpec object; decode it via `json.loads` (Starlark stdlib) and emit the decoded dict as the aspect data.

    ```python
    # In the meta.lens branch:
    spec_str = required_string(p, "spec")
    spec_obj = json.loads(spec_str)
    # Validate that cypherRule is present
    if not hasattr(spec_obj, "cypherRule") and "cypherRule" not in spec_obj:
        fail("InvalidArgument: spec.cypherRule: required in spec JSON object")
    mutations = [
        make_vtx(meta_key, "meta.lens", {}),
        make_aspect(meta_key + ".canonicalName", meta_key, "canonicalName",
                    "canonicalName", {"value": canonical_name}),
        make_aspect(meta_key + ".description", meta_key, "description",
                    "description", {"text": description}),
        make_aspect(meta_key + ".spec", meta_key, "spec", "lensSpec", spec_obj),
        # compensation aspect unchanged
        ...
    ]
    ```

    The `spec_obj` dict is emitted verbatim as the aspect's `data` field, which `dispatchSpec`'s `unwrapSpecBody` will extract and pass to `LensSpec` unmarshal. `cypherRule` will now be present.

  - [ ] 1.2 Update `internal/bootstrap/meta_ddl.go` comment block at top to document the updated `meta.lens` payload shape.
  - [ ] 1.3 Add a unit test in `internal/bootstrap/meta_ddl_test.go` (or nearest suitable test file) verifying: submit `CreateMetaVertex` with `class: meta.lens` and a valid `spec` JSON string; assert the resulting `.spec` aspect data contains `cypherRule` key.

- [ ] **Task 2 — Create `examples/hello-lattice/` directory and sample files (AC2, AC3, AC4, AC5, AC6)**
  - [ ] 2.1 Create `examples/hello-lattice/book-ddl.yaml` — the "book" DDL meta-vertex payload as shown in AC2. The `actor` field is a placeholder `{{BOOTSTRAP_ACTOR}}` with a comment explaining how to substitute the bootstrap actor key from `lattice bootstrap inspect`.
  - [ ] 2.2 Create `examples/hello-lattice/books-lens.yaml` — the "books" Lens meta-vertex payload as shown in AC4. The `spec` field contains the full JSON LensSpec object. The `dsn` in targetConfig is `${POSTGRES_URL:-postgres://lattice:lattice_dev@localhost:5432/lattice?sslmode=disable}` with a note to override via env.
  - [ ] 2.3 Create `examples/hello-lattice/ai-agent.go` (package `main`) — standalone Go program using `aiagent.NewTraverser` to perform the cold-start traversal and submit `CreateBook`. Structure:

    ```go
    package main

    import (
        "context"
        "encoding/json"
        "fmt"
        "log"
        "os"
        "time"
        "github.com/asolgan/lattice/internal/aiagent"
        "github.com/asolgan/lattice/internal/processor"
        "github.com/asolgan/lattice/internal/substrate"
    )

    func main() {
        natsURL := getEnv("NATS_URL", "nats://localhost:4222")
        actorKey := mustGetEnv("AGENT_ACTOR_KEY")   // vtx.identity.<NanoID>
        ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
        defer cancel()

        conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: natsURL, Name: "hello-lattice-agent"})
        if err != nil { log.Fatalf("connect: %v", err) }
        defer conn.Close()

        actorID := extractNanoID(actorKey)  // strip "vtx.identity." prefix

        t := aiagent.NewTraverser(conn, "core-kv", "capability-kv")

        // Step 1: read capability set
        cap, err := t.ReadCapability(ctx, actorID)
        if err != nil { log.Fatalf("ReadCapability: %v", err) }
        fmt.Printf("Agent has %d platform permissions\n", len(cap.PlatformPermissions))

        // Step 2: confirm CreateBook is present
        hasCreateBook := false
        for _, p := range cap.PlatformPermissions {
            if p.OperationType == "CreateBook" { hasCreateBook = true; break }
        }
        if !hasCreateBook { log.Fatalf("agent lacks CreateBook permission — grant it via rbac-domain AssignRole first") }

        // Step 3: discover DDL
        ddlKey, err := t.DiscoverDDL(ctx, "CreateBook")
        if err != nil { log.Fatalf("DiscoverDDL: %v", err) }
        fmt.Printf("DDL key: %s\n", ddlKey)

        // Step 4: read input schema
        aspects, err := t.ReadDDLAspects(ctx, ddlKey)
        if err != nil { log.Fatalf("ReadDDLAspects: %v", err) }
        fmt.Printf("inputSchema: %s\n", aspects.InputSchema)

        // Step 5: submit CreateBook
        reqID := substrate.NewNanoID()
        env := &processor.OperationEnvelope{
            RequestID:     reqID,
            Lane:          processor.LaneDefault,
            OperationType: "CreateBook",
            Actor:         actorKey,
            SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
            Payload:       json.RawMessage(`{"title":"Hello Lattice (AI Agent)"}`),
        }
        reply, err := conn.PublishRequest(ctx, "ops.default", env)
        if err != nil { log.Fatalf("submit CreateBook: %v", err) }
        fmt.Printf("Reply: %s\n", string(reply))
        fmt.Println("Done. Run: lattice query postgres \"SELECT * FROM books WHERE title='Hello Lattice (AI Agent)'\"")
    }
    ```

    Note: use `conn.RequestWithContext` (the NATS request-reply pattern from Story 6.1 notes) rather than a custom `PublishRequest` method — check `substrate.Conn`'s actual method name. The envelope must be marshalled to JSON and published to `ops.default`.

  - [ ] 2.4 Create `examples/hello-lattice/Makefile` with a `demo` target that runs all five milestones in sequence using the CLI commands from the tutorial. The `demo` target assumes `make up` has already been run and the bootstrap actor key is available via `BOOTSTRAP_ACTOR_KEY` env var. Include a `help` target.
  - [ ] 2.5 Create `examples/hello-lattice/README.md` — short pointer file: "See the root README.md's 'Hello Lattice (60-minute tutorial)' section for the full walkthrough."

- [ ] **Task 3 — Create root README.md with the 60-minute tutorial (AC6)**
  - [ ] 3.1 Create `/README.md` at repo root (does not currently exist). Include sections:
    - Project overview (3-4 sentences)
    - Prerequisites (`make`, Docker, Go 1.26+)
    - Quick start (`make up`, `lattice health gates`)
    - `## Hello Lattice (60-minute tutorial)` — the full tutorial with five milestone subsections
    - `## Architecture` — one-liner pointing to `docs/components/_index.md`
    - `## Development` — `make test`, `make vet`, `golangci-lint run ./...`
  - [ ] 3.2 Each milestone subsection has an anchor (`### Milestone 1: Setup`, etc.), the exact commands to run, and the expected output (as a fenced `console` block).
  - [ ] 3.3 The tutorial notes that `<bootstrapActorKey>` can be obtained via `lattice bootstrap inspect` or `lattice graph keys vtx.identity.`.

- [ ] **Task 4 — Create integration test `internal/hellolattice/hellolattice_test.go` (AC1–AC7)**
  - [ ] 4.1 Create directory `internal/hellolattice/` and file `hellolattice_test.go` with package `hellolattice_test`.
  - [ ] 4.2 Add build tag `//go:build integration` at top of file.
  - [ ] 4.3 Implement `TestHelloLattice_Milestone1_Setup`:
    - Connects to NATS via `NATS_URL` env (required)
    - Calls `lattice bootstrap verify` logic (reuse `internal/bootstrap.VerifyKernel(ctx, conn)` — the exported function authored in Story 6.1 Task 12.2)
    - Reads `health.gates.phase1.*` keys; asserts gates 1-4 each have `passed: true`
    - Records `startTime` for overall elapsed tracking
  - [ ] 4.4 Implement `TestHelloLattice_Milestone2_DefineDDL`:
    - Submits `CreateMetaVertex` on meta lane with the book DDL payload (from AC2)
    - Uses `testutil.PublishOp` + a meta-lane pipeline (FilterSubjects `ops.meta`)
    - Asserts `OutcomeAccepted`; reads resulting meta-vertex; asserts `.canonicalName` aspect `value == "book"`; captures `bookDDLKey` for use in later milestones
    - Records milestone elapsed time
  - [ ] 4.5 Implement `TestHelloLattice_Milestone3_CreateBook`:
    - Submits `CreateBook` on default lane with `{"title":"The Pragmatic Programmer"}`
    - Asserts `OutcomeAccepted`; reads book vertex from Core KV; asserts `class == "book"` and `data.title == "The Pragmatic Programmer"`
    - Records `bookKey` for later Postgres assertion
    - Records milestone elapsed time
  - [ ] 4.6 Implement `TestHelloLattice_Milestone4_LensProjection`:
    - Submits `CreateMetaVertex` on meta lane with the books Lens payload (from AC4)
    - Asserts `OutcomeAccepted`; reads Lens meta-vertex; asserts `.spec` aspect data contains `cypherRule` key (SD-1 verification)
    - Polls `SELECT * FROM books WHERE title='The Pragmatic Programmer'` via `database/sql` against `POSTGRES_URL` env, with 10ms interval and 500ms max wait (NFR-P3 bound)
    - Asserts at least one row is returned within the poll window
    - Reads `health.refractor.*` Health KV for lag; asserts lag ≤ 500ms once row appears
    - Records milestone elapsed time
  - [ ] 4.7 Implement `TestHelloLattice_Milestone5_AITraversal`:
    - Creates a new identity (`CreateUnclaimedIdentity`) and grants it `CreateBook` via `CreatePermission` + `AssignRole` (reuse patterns from `packages/rbac-domain/` test helpers)
    - Seeds a capability doc for the agent (since Refractor may not have reprojected yet — use `testutil.SeedCapDoc` or wait for Refractor reprojection via poll)
    - Instantiates `aiagent.NewTraverser` with the agent's identity; calls `ReadCapability`, `DiscoverDDL("CreateBook")`, `ReadDDLAspects`
    - Submits `CreateBook` with `{"title":"Hello Lattice (AI Agent)"}` as the agent actor
    - Asserts `OutcomeAccepted`; polls Postgres for the new row within ≤ 500ms (NFR-P3)
    - Records milestone elapsed time
  - [ ] 4.8 Implement `TestHelloLattice_WriteGate5Marker` (runs after all milestones pass):
    - Writes `health.gates.phase1.gate5` to Health KV with `{"passed": true, "completedAt": "<ISO8601>", "commit": "<git-sha-from-env-GITHUB_SHA-or-empty>"}` using `conn.KVPut`
    - Writes a brief gate5 summary to stdout (`t.Logf`)
    - Best-effort: if NATS unavailable, logs warning but does not fail
  - [ ] 4.9 Assert total elapsed ≤ 60 min (for CI compliance logging — warn if exceeded, do not fail the test on elapsed alone since CI runner speeds vary)

- [ ] **Task 5 — Add `make test-hello-lattice` Makefile target (AC6, AC7)**
  - [ ] 5.1 Add to `Makefile`:
    ```makefile
    test-hello-lattice:
    	go test -v -tags integration -p 1 -count=1 -timeout 70m \
    	  -run TestHelloLattice \
    	  ./internal/hellolattice/... \
    	  NATS_URL=$(NATS_URL) POSTGRES_URL=$(POSTGRES_URL)
    ```
  - [ ] 5.2 Add `test-hello-lattice` to the CI workflow (`.github/workflows/ci.yml`) as a step after `make up` and the standard `make test` step. The CI step sets `NATS_URL=nats://localhost:4222` and `POSTGRES_URL=postgres://lattice:lattice_dev@localhost:5432/lattice?sslmode=disable`.

- [ ] **Task 6 — Create gate5-external-tester-report.md template (AC8)**
  - [ ] 6.1 Create `_bmad-output/planning-artifacts/gate5-external-tester-report.md` as an empty template:

    ```markdown
    # Phase 1 Gate 5 — External Tester Report

    **Status:** PENDING — awaiting external tester completion

    ## Tester Information
    - Name / handle: <!-- fill in -->
    - Machine: <!-- OS, chip, RAM -->
    - Date: <!-- YYYY-MM-DD -->

    ## Tutorial Run

    | Milestone | Elapsed (min) | Notes |
    |-----------|---------------|-------|
    | 1 Setup   |               |       |
    | 2 Define DDL |            |       |
    | 3 Starlark rule |         |       |
    | 4 Lens projection |       |       |
    | 5 AI traversal |          |       |
    | **Total** |               |       |

    ## Blockers Encountered
    <!-- list any blockers; "none" if clean run -->

    ## Verdict
    - [ ] Tutorial completed end-to-end in < 60 min
    - [ ] No documentation gaps requiring external knowledge
    - [ ] Gate 5 CLOSED

    ## Feedback
    <!-- free-form notes for the Lattice team -->
    ```

- [ ] **Task 7 — Architecture purity verification (AC9)**
  - [ ] 7.1 `grep -rn "hello.lattice\|helloLattice\|HelloLattice" internal/processor/` → zero hits
  - [ ] 7.2 `OperationEnvelope` in `internal/processor/envelope.go` — shape unchanged
  - [ ] 7.3 `ContextHint` struct — unchanged
  - [ ] 7.4 `make test-bypass` → all-DEFENDED
  - [ ] 7.5 `make test-capability-adversarial` → all-BLOCKED
  - [ ] 7.6 `make verify-kernel` → green (the MetaRootDDLScript change is additive to the meta.lens path only; primordial seeding is unaffected)
  - [ ] 7.7 `golangci-lint run ./...` → clean

---

## Dev Notes

### Overview

Story 6.4 is the Phase 1 capstone — it proves the entire vertical slice works by having a developer (or CI) do it from scratch. The implementation is almost entirely **new surface** (`examples/hello-lattice/`, `internal/hellolattice/`, `README.md`) plus one small targeted fix to `MetaRootDDLScript`. No Processor changes. No new KV buckets. No new packages.

The three deliverables — integration test, tutorial, live demo — are the same code viewed through three lenses:
- The YAML files in `examples/hello-lattice/` are what the tutorial human runs
- The Go test in `internal/hellolattice/` drives those same payloads programmatically
- The `make demo` target runs the CLI commands non-interactively for screen-sharing

---

### MetaRootDDLScript Fix (SD-1 — Task 1)

The current `meta.lens` branch stores:
```python
make_aspect(meta_key + ".spec", meta_key, "spec", "lensSpec",
            {"source": spec, "adapter": adapter, "bucket": bucket, "engine": engine})
```

`CoreKVSource.dispatchSpec` reads the `.spec` aspect data and unmarshals it as `LensSpec`. `LensSpec.CypherRule` maps from JSON key `"cypherRule"`. The `"source"` key in the current data shape has no mapping in `LensSpec` — so `spec.CypherRule` is always empty and `translateSpec` returns `"cypherRule required"`.

**Fix:** Accept `spec` as a JSON string in the `CreateMetaVertex` payload for `meta.lens`, parse it via Starlark's `json.decode()`, and emit the decoded object directly as the aspect `data`. The `spec` string must be a valid `LensSpec` JSON object with at least `cypherRule`, `targetType`, and `targetConfig` fields.

```python
# Starlark (in meta.lens branch):
spec_str = required_string(p, "spec")
spec_obj = json.decode(spec_str)
if type(spec_obj) != type({}):
    fail("InvalidArgument: spec: must be a JSON object string")
if "cypherRule" not in spec_obj:
    fail("InvalidArgument: spec.cypherRule: required")
if "targetType" not in spec_obj:
    fail("InvalidArgument: spec.targetType: required (postgres|nats_kv)")
if "targetConfig" not in spec_obj:
    fail("InvalidArgument: spec.targetConfig: required")
# canonicalName in spec_obj is optional; if absent, Refractor uses the lensID
...
mutations = [
    make_vtx(meta_key, "meta.lens", {}),
    make_aspect(meta_key + ".canonicalName", meta_key, "canonicalName", "canonicalName",
                {"value": canonical_name}),
    make_aspect(meta_key + ".description", meta_key, "description", "description",
                {"text": description}),
    make_aspect(meta_key + ".spec", meta_key, "spec", "lensSpec", spec_obj),
    make_aspect(meta_key + ".compensation", meta_key, "compensation", "compensation",
                {"inverseOperationType": "TombstoneMetaVertex",
                 "payloadTemplate": {"metaKey": "{{detail.metaKey}}"},
                 "revisionTemplate": {"metaKey": "{{revisions[detail.metaKey]}}"}}),
]
```

Check whether the embedded Starlark runtime supports `json.decode()`. The library used is `go.starlark.net/starlark`. The `json` module is available via `go.starlark.net/lib/json` — verify it is loaded in the Starlark execution context (check `internal/refractor/ruleengine/` or wherever the Starlark executor is wired). If `json` is not available, the alternative is: require the `spec` payload field to be a pre-structured Go map (but that requires changing the operation payload shape, which is a bigger change). Check first before implementing.

---

### Starlark Runtime JSON Support Check

Look in `internal/processor/step6_ddl_execute.go` (or wherever the DDL Starlark script is executed) for how the Starlark thread globals are configured. Search for `starlark.StringDict` initialization and `starlib` or `starlarkjson`. If `json` is not loaded, add it:

```go
import starlarkjson "go.starlark.net/lib/json"

globals := starlark.StringDict{
    "nanoid": ...,
    "json":   starlarkjson.Module,  // add if not already present
}
```

This is a one-line addition to the globals dict, if needed.

---

### "book" DDL Starlark Script

The script from AC2 is the canonical version. Key points:
- `nanoid.new()` generates a fresh NanoID for the book vertex key
- The book vertex key shape is `vtx.book.<NanoID>` (3-segment per Contract #1 §1.5)
- The script emits a `BookCreated` event — events go to the `core-events` NATS stream; no consumer needed for the tutorial but it's good practice
- The script does NOT write aspects inline (only the vertex `data` field carries `title`) — keeping it simple. The tutorial could add `vtx.book.<NanoID>.title` as a separate aspect, but the simpler path (title in vertex data) is correct for a "Hello Lattice" DDL.

---

### "books" Lens: simple engine + Postgres

The `simple` engine parses: `MATCH (b:book) RETURN b.id AS book_id, b.title AS title`.

The simple engine's `NodeEntry.CoreKVKey` will be the full vertex key `vtx.book.<NanoID>`. The engine maps `b.id` to the `id` field of the vertex key's NanoID segment (extracted by `adjLookupID`). Actually, looking at the evaluator — the `NodeEntry.Properties` map comes from the vertex's raw JSON document. The book vertex has `data: {"title": "..."}`. The simple evaluator maps `b.title` to `Properties["title"]` and `b.id` to... check `evaluator.go` for how `id` is resolved.

Check `simple/evaluator.go` to confirm `b.id` maps to the vertex's NanoID (the third segment of `vtx.book.<NanoID>`). If the evaluator exposes the raw key under `id`, this is fine. If not, use `b.key` (which maps to `CoreKVKey`) and adjust the Postgres column name to `book_key` with the key as the value. Verify in `internal/refractor/pipeline/pipeline.go` line ~708 (the `NodeEntry` construction):

```go
entry := simple.NodeEntry{
    CoreKVKey:  ...,
    NodeLabel:  ...,
    IsDeleted:  ...,
    Properties: ...,
}
```

The `Properties` map is what `b.field` resolves against. If `data.title` is directly in `Properties["title"]`, the MATCH clause works as written. The `id` column — use `b.key` mapped to `book_id` if `b.id` is not available. The Lens spec example in AC4 uses `b.id AS book_id` — adjust to `b.key AS book_id` if needed after checking the evaluator.

---

### Integration Test Harness

The test in `internal/hellolattice/hellolattice_test.go` uses:
- `substrate.Connect(ctx, substrate.ConnectOpts{URL: os.Getenv("NATS_URL"), Name: "hellolattice-test"})` for the connection
- `testutil.CapabilityPipeline` with `FilterSubjects: []string{"ops.meta"}` for the meta-lane pipeline (Story 5.2 added this)
- `testutil.CapabilityPipeline` with default subjects for the default-lane pipeline
- Direct `database/sql` + `lib/pq` for Postgres queries in milestones 4-5

**Not using `testutil.SetupPackageTestEnv`** for the integration test (that uses embedded NATS). Instead connect directly to the live NATS at `NATS_URL`.

**Operator actor key for test submissions:** Use `bootstrap.BootstrapIdentityKey` — the primordial operator identity that already has all permissions seeded by the bootstrap + packages installation. Its value is a constant in `internal/bootstrap/primordial.go` or `nanoid.go` — find it.

---

### Granting CreateBook to the AI Agent (Milestone 5)

The integration test for milestone 5 needs to:
1. Create a new identity: submit `CreateUnclaimedIdentity` → get `vtx.identity.<agentId>`
2. Create a `CreateBook` permission: submit `CreatePermission {operationType: "CreateBook", scope: "any"}` → get `vtx.permission.<permId>`
3. Get the operator role: look up the `operator` role NanoID from Core KV (e.g., `lattice graph keys vtx.role.` and filter by `.canonicalName` == `operator`, or use `bootstrap.OperatorRoleKey` constant if it exists)
4. Grant `CreateBook` to the operator role: `GrantPermission {permKey: ..., roleKey: ...}` — or assign the agent to a role that already has the permission
5. Assign the agent to the operator role: `AssignRole {actorKey: vtx.identity.<agentId>, roleKey: vtx.role.<operatorId>}` — OR create a new `book-author` role for cleanliness

**Simplest path for the tutorial:** Assign the AI agent to the existing `operator` role (seeded by `rbac-domain` package). This gives it full operator permissions including anything granted to that role. Since `CreateBook` is a new DDL-level permission (not yet in the operator role's grants), the tutorial must also run `CreatePermission` + `GrantPermission` to the operator role.

Wait for Capability KV reprojection (Refractor updates `cap.identity.<agentId>`) before calling `ReadCapability`. Poll with a 500ms timeout. In the test harness, if Refractor is running live, this will complete within NFR-P3. If it does not, seed the cap doc manually via `testutil.SeedCapDoc` as a fallback (with a test log noting the manual seed).

---

### `lattice op submit --file` flag

Story 6.1's AC4 specifies `--payload @file.json` (for JSON) and `--payload -` (for stdin). The tutorial YAMLs use `--file` as a convenience shorthand. Verify that `lattice op submit` supports `--file <path>` OR `--payload @<path>`. If only `--payload @<path>` exists, update the tutorial YAML usage in the README accordingly. Do NOT add a new `--file` flag to the CLI (Guardrail 1). The tutorial should use whichever form is actually implemented.

---

### `DiscoverDDL` and `permittedCommands`

The `aiagent.Traverser.DiscoverDDL(ctx, operationType)` enumerates `vtx.meta.*` and matches against `.canonicalName`. The `CreateBook` operation type will match the `book` DDL's `.canonicalName` aspect (`"book"`) only if the tutorial treats `operationType == canonicalName`. The spec says `permittedCommands: ["CreateBook"]` — so the canonical name is `book`, not `CreateBook`. 

The `DiscoverDDL` method matches against `canonicalName`. For `CreateBook`, the canonical name is `book`. This means `DiscoverDDL("CreateBook")` will NOT match `canonicalName == "book"`. 

**Fix in the tutorial:** Call `DiscoverDDL("book")` (the DDL's canonical name) rather than `DiscoverDDL("CreateBook")`. The `ai-agent.go` program must discover the DDL by canonical name, then read `permittedCommands` to confirm `CreateBook` is in the list. This is the correct semantics: the canonical name is the entity type; the permitted commands are the operations. Update `ai-agent.go` Task 2.3 accordingly.

Alternatively, if `DiscoverDDL` should search by `permittedCommands` instead of `canonicalName`, that would require changing `internal/aiagent/traversal.go` — but that is a change to a shipped story's code. Since `DiscoverDDL` takes an `operationType` argument per Story 5.2's spec, the traversal should search both `canonicalName` and `permittedCommands`. Check `internal/aiagent/traversal.go` to see the current implementation. If it only searches `canonicalName`, the tutorial should pass `"book"` and separately check `permittedCommands` for `"CreateBook"`.

---

### Postgres URL

The `POSTGRES_URL` env var is not currently defined in the Makefile (only `REFRACTOR_PG_DSN` is used). The integration test uses `POSTGRES_URL`. Add to the `test-hello-lattice` Makefile target:

```makefile
POSTGRES_URL ?= postgres://lattice:lattice_dev@localhost:5432/lattice?sslmode=disable
```

The `lib/pq` driver is already available (it was added in Story 6.1 per the dev notes there — verify in `go.mod`).

---

### gate5 Health KV marker pattern

Follows the same pattern as `internal/bypass/gate3_test.go:writeGate3HealthMarker`:
- Connect to `NATS_URL` (or live connection already established in test)
- KVPut to `health-kv` bucket at key `health.gates.phase1.gate5`
- Value: `{"passed": true, "completedAt": "<RFC3339>", "commit": "<GITHUB_SHA or empty>"}`
- Best-effort (if NATS unavailable, log warning, do not fail)

---

### Files NOT to Touch

- `internal/processor/envelope.go` — do NOT add or remove fields
- `internal/processor/commit_path.go` — no new commit path steps
- `internal/processor/step*.go` — no changes to any step
- `internal/refractor/pipeline/pipeline.go` — no changes
- `internal/refractor/ruleengine/simple/evaluator.go` — no changes
- `internal/aiagent/traversal.go` — read-only; use as-is. If `DiscoverDDL` needs a `permittedCommands` search mode, file a CONTRACT-AMENDMENT-REQUEST rather than modifying the shipped traversal code inline.
- `packages/*/ddls.go`, `packages/*/lenses.go` — no changes; book DDL is not a package
- `internal/bootstrap/primordial.go` — no new primordial entries for book DDL
- `internal/bootstrap/nanoid.go` — no new NanoID vars for book DDL
- `_bmad-output/planning-artifacts/` — sub-agents must never edit planning artifacts (except creating `gate5-external-tester-report.md` which is explicitly specified in AC8)

---

### Architecture Compliance Checklist

- [ ] `grep -rn "helloLattice\|hello.lattice\|HelloLattice" internal/processor/` → zero hits
- [ ] `OperationEnvelope` struct shape unchanged (`internal/processor/envelope.go`)
- [ ] `ContextHint` struct unchanged
- [ ] `ContextHint.Reads` is still the only field
- [ ] `MetaRootDDLScript` meta.lens branch updated per SD-1 — `.spec` aspect stores `LensSpec` JSON shape (with `cypherRule`, not `source`)
- [ ] `examples/hello-lattice/` exists with all five artefacts
- [ ] `internal/hellolattice/hellolattice_test.go` exists; all five milestone tests present
- [ ] `README.md` exists at repo root with "Hello Lattice (60-minute tutorial)" section
- [ ] `_bmad-output/planning-artifacts/gate5-external-tester-report.md` exists as empty template
- [ ] `make test-hello-lattice` added to `Makefile` and `ci.yml`
- [ ] `make test-bypass` → all-DEFENDED
- [ ] `make test-capability-adversarial` → all-BLOCKED
- [ ] `make verify-kernel` → green
- [ ] `golangci-lint run ./...` → clean

---

### References

- [Source: `_bmad-output/planning-artifacts/epics.md` §Story 6.4, lines 1522–1566] — canonical AC spec
- [Source: `_bmad-output/implementation-artifacts/6-1-lattice-cli-tool.md`] — CLI command reference; brief structure template
- [Source: `_bmad-output/implementation-artifacts/5-2-cold-start-ai-agent-traversal.md`] — `aiagent.Traverser` usage pattern; north-star test design
- [Source: `_bmad-output/implementation-artifacts/5-1-ddl-self-description-aspects.md`] — DDL self-description aspect format; `CreateMetaVertex` payload shape
- [Source: `internal/bootstrap/meta_ddl.go`] — MetaRootDDLScript; meta.lens branch to update (SD-1)
- [Source: `internal/refractor/lens/corekv_source.go`] — `LensSpec` struct (cypherRule field); `dispatchSpec`; `unwrapSpecBody`
- [Source: `internal/refractor/lens/bootstrap.go`] — `BootstrapLensSpecJSON()` — shows correct LensSpec JSON shape for Postgres
- [Source: `internal/refractor/adapter/postgres.go`] — `PostgresAdapter`; `TargetPostgresConfig` shape
- [Source: `internal/refractor/ruleengine/simple/adapter.go`] — simple engine; `Execute()` not-wired note
- [Source: `internal/aiagent/traversal.go`] — `NewTraverser`, `ReadCapability`, `DiscoverDDL`, `ReadDDLAspects`
- [Source: `internal/bypass/gate3_test.go`] — gate Health KV marker write pattern
- [Source: `internal/testutil/pipeline.go`] — `CapabilityPipeline`, `FilterSubjects`, `SeedCapDoc`, `PublishOp`, `DriveOne`
- [Source: `internal/bootstrap/primordial.go`] — `BootstrapIdentityKey`; KV bucket name constants
- [Source: `packages/rbac-domain/ddls.go`] — `CreatePermission`, `GrantPermission`, `AssignRole` operation types

---

## Implementation Tier & Budget

**Model tier: Sonnet** (bounded scope; all primitives ship in prior stories; main deliverables are new files + one targeted Starlark fix).
**Estimated token budget: ~130K** (input + output — tracking only, NOT enforced per Rule 8 in WINSTON-RESUME.md). Sub-agent self-reports are typically 20-30% low vs outer telemetry; trust the task-notification `total_tokens`.

---

## Stuck-Loop Halt Criteria

Halt and surface for Winston review if any of the following occur:

- **SD-1 Starlark fix is blocked:** if `json.decode()` is unavailable in the Starlark runtime AND there is no clean way to pass the LensSpec as a structured dict through the payload, HALT — do not invent a workaround that patches CoreKVSource.
- **Milestone 4 Lens never activates:** if submitting a `meta.lens` `CreateMetaVertex` does not result in Refractor picking up the lens (after SD-1 fix), HALT after 2 debug attempts. Do not change Refractor code.
- **Same compilation error recurs after 3+ fix attempts** with no different root cause hypothesis.
- **Any `lattice op submit` path requires adding a field to `OperationEnvelope` or `OperationReply`** — stop immediately; this is a Guardrail 2 violation.
- **`make test-bypass` or `make test-capability-adversarial` flips** from DEFENDED/BLOCKED to any other status.
- **`DiscoverDDL("CreateBook")` fails to find the book DDL** because the traversal only matches `canonicalName` (not `permittedCommands`) — file a CONTRACT-AMENDMENT-REQUEST describing the traversal semantics gap; do NOT modify `internal/aiagent/traversal.go` without escalating.

Do NOT halt for token budget alone.

---

## Dev Agent Record

### Agent Model Used

claude-sonnet-4-6

### Debug Log References

D1: SD-1 — confirmed `json` module not loaded in Starlark globals; added `starlarkjson.Module` to `internal/processor/starlark_runner.go`. Required for `json.decode()` in MetaRootDDLScript.

D2: Gate4 rollback test broken by SD-1 targetConfig requirement — `buildGate4LensPayload` was missing `targetConfig`; added `{"bucket":"capability-kv","key":["key"]}`.

D3: Simple evaluator property access — `b.title` resolves against top-level `props["title"]` (raw KV document), not `props["data"]["title"]`. Book DDL updated to store `title` at top-level AND in `data.title`.

D4: `lib/pq` not in go.mod — integration test switched from `database/sql`+`lib/pq` to `github.com/jackc/pgx/v5` for Postgres queries in Milestone 4 and Milestone 5.

### Completion Notes List

- SD-1 fix applied: `MetaRootDDLScript` `meta.lens` branch now accepts `spec` as JSON string, decodes via `json.decode()`, validates `cypherRule`/`targetType`/`targetConfig`, emits decoded object as `.spec` aspect data.
- `go.starlark.net/lib/json` module added to Starlark globals in `internal/processor/starlark_runner.go`.
- All five tutorial artefacts created in `examples/hello-lattice/`: `book-ddl.yaml`, `books-lens.yaml`, `ai-agent.go`, `Makefile`, `README.md`.
- `README.md` created at repo root with full 60-minute tutorial (5 milestone sections).
- Integration test `internal/hellolattice/hellolattice_test.go` created with build tag `integration`, five milestone tests + gate5 marker writer.
- `make test-hello-lattice` added to root `Makefile` and `.github/workflows/ci.yml`.
- `_bmad-output/planning-artifacts/gate5-external-tester-report.md` created as empty template per AC8.
- `go build ./...` clean. `golangci-lint run ./...` → 0 issues. All `go test ./... -p 1` pass.
- Architecture purity: zero hits in `internal/processor/` for HelloLattice patterns; `OperationEnvelope`, `OperationReply`, `ContextHint` structs unchanged.

### File List

**Modified:**
- `internal/processor/starlark_runner.go` — added `json` module to Starlark globals
- `internal/bootstrap/meta_ddl.go` — SD-1 fix: meta.lens branch uses `json.decode()` + LensSpec shape
- `internal/bootstrap/self_description_e2e_test.go` — updated meta.lens test case; added `TestMetaLensSpec_CypherRuleInAspectData`
- `internal/aiagent/gate4_rollback_test.go` — added `targetConfig` to `buildGate4LensPayload`
- `Makefile` — added `test-hello-lattice` target
- `.github/workflows/ci.yml` — added Gate 5 step

**Created:**
- `README.md` (repo root)
- `examples/hello-lattice/book-ddl.yaml`
- `examples/hello-lattice/books-lens.yaml`
- `examples/hello-lattice/ai-agent.go`
- `examples/hello-lattice/Makefile`
- `examples/hello-lattice/README.md`
- `internal/hellolattice/hellolattice_test.go`
- `_bmad-output/planning-artifacts/gate5-external-tester-report.md`
