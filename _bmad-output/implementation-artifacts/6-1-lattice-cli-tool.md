# Story 6.1: Lattice CLI Tool (FR45)

Status: ready-for-dev

## Story

As a developer or operator,
I want a unified CLI tool to submit operations, inspect graph state, query projection surfaces, and read platform health — without a browser client and without writing custom NATS code —
so that all Phase 1 workflows are accessible from the terminal.

## Spec Deviations (read first)

**Binary naming: `lattice` is a new sibling binary; `lattice-pkg` stays separate.**

The epics.md §Story 6.1 spec says "lattice CLI binary" without specifying the relationship to the existing `cmd/lattice-pkg/` binary. Two options exist:

**Option A — fold `lattice-pkg` into `lattice pkg`:**
Renames `lattice-pkg` to `lattice pkg install|uninstall|list`. Fewer binaries; cleaner surface.

**Option B — keep `lattice-pkg` as a sibling binary (CHOSEN):**
Implement `lattice` as a separate binary at `cmd/lattice/`. `lattice-pkg` stays at `cmd/lattice-pkg/` unchanged.

**Recommendation: Option B.** Rationale:
1. `lattice-pkg` is already shipped, already embedded in Makefile install targets, and already tested. Renaming it would introduce gratuitous churn in Makefile, CI, and existing operator instructions.
2. `lattice-pkg` operates substrate-direct (pre-authorized, admin-credentialed); `lattice` operates via the Processor write path with actor credentials. These are fundamentally different actors with different auth contracts. Keeping them separate clarifies the trust boundary.
3. Story 6.4 ("Hello Lattice") references `lattice` commands specifically — the tutorial does not need `lattice pkg` to be a subcommand of the main CLI.

The epics.md AC table does not list `lattice pkg` as a command group — it lists `lattice op`, `lattice graph`, etc. Option B is spec-consistent.

**No history comments in code.** The implementer MUST NOT add inline comments that record when or why code was changed — e.g., `// Story 6.1: added this`, `// Replaces old X`, `// Removed in Story Y — see epic Z`, or `// Previously did Z`. Git history is the record of change; comments explain what the code does now. This rule applies to all files touched by this story. Reviewers (Winston) will reject history comments at code-review time.

## Acceptance Criteria

1. **`lattice --help` lists all command groups (replaces epics.md AC1):**
   The `lattice` binary builds cleanly (`go build ./cmd/lattice`) and exits 0 on `lattice --help`. Help text lists all nine command groups with their subcommands:

   | Command Group | Subcommands | Purpose |
   |---|---|---|
   | `lattice op` | `submit`, `status <requestId>`, `trace <requestId>` | Submit operations, check commit status, retrieve op-tracker entry |
   | `lattice graph` | `read <key>`, `walk <startKey> [--depth N]`, `keys <prefix>` | Direct Core KV reads, traversal, prefix listing — **debug/operator only** |
   | `lattice lens` | `list`, `activate <file>`, `deactivate <key>`, `lag` | Lens lifecycle and lag inspection |
   | `lattice query` | `postgres <sql>`, `cap <actorKey>` | Postgres pass-through, Capability KV reads |
   | `lattice health` | `summary`, `component <name>`, `gates` | Health KV summary, per-component, phase-gate statuses |
   | `lattice identity` | `create-unclaimed`, `claim` | Identity-domain op submission |
   | `lattice candidates` | `list`, `merge <primary> <secondary>` | Identity-hygiene Duplicate Candidates Lens read + MergeIdentity op |
   | `lattice auth-trace` | `<requestId>` | Three-plane auth trace read (Story 3.5 carry) |
   | `lattice bootstrap` | `verify`, `inspect` | Story 1.3 kernel verification |

   `lattice --version` exits 0 and emits a version string (can be hard-coded `dev` for Phase 1).

2. **Error output contract:**
   Given any subcommand execution:
   - Errors are human-readable on stderr; include `requestId` when applicable.
   - Exit code is non-zero on error, zero on success.
   - All subcommands accept `--output json` (short `-o json`); JSON output goes to stdout, structured as `{"ok": true, "data": {...}}` on success or `{"ok": false, "error": {"code": "...", "message": "..."}}` on failure.
   - All subcommands accept `--nats-url` (env: `NATS_URL`, default `nats://localhost:4222`).
   - All subcommands accept `--config` (env: `LATTICE_CONFIG`, default `~/.lattice/config.json`).

3. **`lattice config set-credential` writes credentials file:**
   Given `lattice config set-credential --actor-key vtx.identity.<NanoID> --nats-url nats://localhost:4222`:
   - Credentials are written to `~/.lattice/credentials.json` (or `$XDG_CONFIG_HOME/lattice/credentials.json` if `XDG_CONFIG_HOME` is set).
   - File is created if absent; existing entries are merged (not overwritten wholesale).
   - File permissions are `0600`.
   - Phase 1: file-based only. KMS-backed storage is Phase 2+.

4. **`lattice op submit` uses standard Processor write path:**
   Given `lattice op submit --lane default --operation-type <type> --actor <actorKey> --payload @file.json` (or `--payload -` for stdin):
   - CLI constructs an `OperationEnvelope` using `processor.OperationEnvelope` fields: generates a `requestId` (NanoID), sets `lane`, `operationType`, `actor`, `submittedAt` (RFC3339 UTC), and `payload`.
   - CLI publishes to `ops.<lane>` NATS subject directly (same path as any other submitter).
   - On success (reply `status: accepted`): prints `requestId` and `opTrackerKey`; exits 0.
   - On rejection: prints the `error.code` and `error.message`; exits non-zero.
   - No CLI-specific code path exists in the Processor. `grep -rn "cli\|CLI\|lattice-cli" internal/processor/` must return zero hits after this story.

5. **`lattice op status <requestId>` reads op tracker from Core KV:**
   Given a requestId:
   - Reads `vtx.op.<requestId-NanoID>` from Core KV (the `OpTrackerKey` from the commit reply — see `processor.TrackerKey`).
   - Prints the tracker document; `--output json` emits the raw document.
   - Returns non-zero exit code if the key is missing or not yet committed.

6. **`lattice op trace <requestId>` delegates to auth trace:**
   Given a requestId:
   - Reads `health.processor.<instance>.auth-trace.<requestId>` from Health KV.
   - Prints the three-plane `AuthTraceRecord` document.
   - If no record exists (op was allowed and `TraceAllowDecisions` was OFF, or TTL expired), prints a clear message and exits 0.
   - Note: this subcommand is distinct from `lattice auth-trace <requestId>` below. `op trace` is per-operation; `auth-trace` is operator-level access to the three-plane record with full JSON detail.

7. **`lattice graph` commands are debug-only and labeled as such:**
   Given any `lattice graph` subcommand:
   - Help text includes: "WARNING: lattice graph is a debug and operator surface. It is NOT a sanctioned client read path (architecture rule: Adjacency KV is Refractor-private; all production reads must go through a Lens). Do not use these commands in client applications or scripts."
   - `read <key>` reads the key from Core KV directly and prints the raw JSON value.
   - `walk <startKey> [--depth N]` uses `aiagent.Traverser` (existing package) to enumerate connected vertices; default depth 3; max depth 10.
   - `keys <prefix>` uses `substrate.Conn.KVListKeys` to list all Core KV keys matching the prefix.
   - No adjacency KV reads. No Refractor-private bucket reads.

8. **`lattice lens` commands submit through Processor or read Health KV:**
   - `list` enumerates `vtx.meta.*` keys in Core KV, filters for `class: "meta.lens"`, prints `key | canonicalName | isDeleted`.
   - `activate <file>` reads a lens definition JSON file and submits `CreateMetaVertex` with `class: meta.lens` to the meta lane; prints the resulting `metaKey`.
   - `deactivate <key>` submits `TombstoneMetaVertex` for the given meta-vertex key; lane is meta.
   - `lag` reads `health.refractor.*` keys from Health KV and prints per-lens projection lag.

9. **`lattice query cap <actorKey>` reads Capability KV:**
   Given `lattice query cap vtx.identity.<NanoID>`:
   - Reads `cap.identity.<NanoID>` from Capability KV bucket.
   - Prints the capability document; `--output json` emits the raw doc.

   **`lattice query postgres <sql>` is a thin pass-through:**
   - Reads `POSTGRES_URL` from env (or `--postgres-url` flag); executes the SQL; prints results as a table (default) or JSON array (`--output json`).
   - No query rewriting, no ORM semantics, no SQL wrapping.
   - Phase 1: read-only queries only; DML statements return an error with a message directing the operator to use `lattice op submit`.

10. **`lattice health summary/component/gates`:**
    - `summary` reads all `health.*` keys from Health KV and prints a summary table.
    - `component <name>` reads `health.<name>.*` keys and prints component-level detail.
    - `gates` reads `health.gates.phase1.*` keys and prints phase-gate statuses.

11. **`lattice identity create-unclaimed`:**
    Submits `CreateUnclaimedIdentity` operation on the default lane with a minimal actor-provided payload. Prints `requestId`, `opTrackerKey`, and the one-time `claimKey` from `OperationReply.Detail` (if present).

    **`lattice identity claim`:**
    Submits `ClaimIdentity` operation. Reads payload from `--payload @file.json` or stdin. Prints reply.

12. **`lattice candidates list`:**
    Reads keys from the `duplicate-candidates` KV bucket. Prints `key | primaryKey | secondaryKey | score` for each candidate entry. `--output json` emits the array.

    **`lattice candidates merge <primary> <secondary>`:**
    Submits `MergeIdentity` operation. Per Story 4.6 R3: the `edges` parameter is required — the CLI reads the identity-hygiene duplicate-candidates entry for `<secondary>` to enumerate inbound/outbound edges and includes them in the op payload. Operator must have identity-hygiene package installed (enforced by `MergeIdentity` Capability check, not by the CLI).

13. **`lattice auth-trace <requestId>`:**
    Reads `health.processor.<instance>.auth-trace.<requestId>` from Health KV. Prints all three planes. This is the Story 3.5 carry (`LookupAuthTrace` helper is already available in `internal/processor/`). The CLI wraps this existing helper.
    Note: `--instance` flag (default: `default`) selects the processor instance name.

14. **`lattice bootstrap verify`:**
    Runs the equivalent of `scripts/verify-kernel.go` — asserts primordial Core KV state. Exits 0 if green, non-zero with a summary of failures otherwise.

    **`lattice bootstrap inspect`:**
    Reads and prints selected primordial entries (kernel root DDL, operator role, meta-permission vertices) in a human-readable table. Does not modify state.

15. **Test coverage:**
    - One end-to-end test: `TestCLI_HelpExits0` — builds the `lattice` binary and asserts `lattice --help` exits 0. Lives in `cmd/lattice/cli_e2e_test.go`.
    - Per-command-group happy-path unit tests using stubbed `substrate.Conn` where feasible. At minimum one test per command group (9 groups = 9 happy-path tests). Lives in `cmd/lattice/<group>/<group>_test.go`.
    - Full "Hello Lattice" e2e slice via CLI only is deferred to Story 6.4 (Phase 1 Gate 5). **Do NOT write the full e2e in this story.**
    - `make test-cli` target (pattern: `go test ./cmd/lattice/... -v -p 1 -count=1`).

16. **`make up` integration:**
    `make up` builds the `lattice` binary to `bin/lattice` (alongside existing `bin/lattice-pkg`). The `Makefile` `build` target adds `go build -o bin/lattice ./cmd/lattice`.

## Architectural Guardrails (non-negotiable)

These five constraints are hard stops. If any implementation path leads toward violating one, stop and file a `CONTRACT-AMENDMENT-REQUEST.md` rather than proceeding.

**Guardrail 1 — No new Processor read surface.**
The CLI submits operations via `ops.<lane>` subjects, exactly as any other submitter. It does NOT introduce new ContextHint fields, new Processor endpoints, or Processor-side CLI-detection hooks. `grep -rn "cli\|CLI" internal/processor/` must return zero production-code hits at story close.

**Guardrail 2 — No CLI-specific bypass.**
The CLI obeys Capability KV exactly like any other actor. `lattice op submit` resolves auth through the actor's capability document in the standard Processor commit path (steps 1-10). The CLI does not pass special headers, flags, or subjects that short-circuit authorization.

**Guardrail 3 — `lattice graph` is debug-only, not a client read path.**
`lattice graph` reads Core KV directly (`substrate.Conn.KVGet`, `KVListKeys`). This is an operator debug surface only. Per lattice-architecture.md:94 — Adjacency KV is Refractor-private. The CLI must never read the Adjacency KV bucket directly. Help text and `--help` output for every `lattice graph` subcommand must carry the debug-only warning (AC7 above).

**Guardrail 4 — `lattice query postgres` is a thin pass-through.**
No SQL rewriting, no ORM layer, no object mapping, no query generation. The CLI accepts a SQL string from the user and executes it unchanged (read-only guard is the only transformation). Do not wrap Postgres semantics.

**Guardrail 5 — No new inline permissions or capabilities.**
If implementing a CLI command requires a privilege that the existing rbac-domain / identity-domain / identity-hygiene packages do not already grant, do NOT mint a new permission inline. File a `CONTRACT-AMENDMENT-REQUEST.md` named with the specific operation and missing grant. The story closes with the CLI limited to what existing packages authorize.

## Tasks / Subtasks

- [ ] Task 1 — Scaffold `cmd/lattice/` binary (AC1, AC16)
  - [ ] 1.1 Create `cmd/lattice/main.go` — thin entry point that delegates to the root command
  - [ ] 1.2 Create `cmd/lattice/root.go` — root command wiring: `--nats-url`, `--config`, `--output` persistent flags; credential loading from `~/.lattice/credentials.json`
  - [ ] 1.3 Create `cmd/lattice/version.go` — `--version` flag handling; hard-codes `dev` for Phase 1
  - [ ] 1.4 Register all 9 command groups as sub-packages in root
  - [ ] 1.5 Add `go build -o bin/lattice ./cmd/lattice` to `Makefile` `build` target
  - [ ] 1.6 Add `test-cli` Makefile target: `go test ./cmd/lattice/... -v -p 1 -count=1`

- [ ] Task 2 — CLI framework decision (AC1)
  - [ ] 2.1 Check for existing CLI library in `go.mod` — none present (project uses stdlib `flag` for daemons). Evaluate: stdlib `flag` is sufficient for simple daemons but awkward for a multi-level command tree.
  - [ ] 2.2 Decision: use `cobra` (`github.com/spf13/cobra`). Justification: multi-level subcommands (`lattice op submit`, `lattice graph walk`) are clean with cobra; persistent flags (nats-url, output) propagate naturally; `lattice --help` and per-group `--help` come for free. Add to `go.mod`. No other cobra-style library is in the project; this is the first and only CLI binary.
  - [ ] 2.3 Add cobra to `go.mod` and `go.sum` via `go get github.com/spf13/cobra@latest`

- [ ] Task 3 — `lattice config` subcommand (AC3)
  - [ ] 3.1 Create `cmd/lattice/config/config.go` — `set-credential` subcommand
  - [ ] 3.2 Write credentials to `~/.lattice/credentials.json` (XDG_CONFIG_HOME override); create dir if missing; `chmod 0600`
  - [ ] 3.3 Credential file shape: `{"credentials": [{"actorKey": "vtx.identity.<NanoID>", "natsURL": "nats://..."}]}`
  - [ ] 3.4 Unit test: `TestSetCredential_CreatesFile`, `TestSetCredential_MergesExisting`

- [ ] Task 4 — `lattice op` subcommand group (AC4, AC5, AC6)
  - [ ] 4.1 Create `cmd/lattice/op/op.go` — register `submit`, `status`, `trace` subcommands
  - [ ] 4.2 `submit`: parse `--lane`, `--operation-type`, `--actor`, `--payload` (file path or `-` for stdin), optional `--context-hint-reads` (comma-separated); construct `processor.OperationEnvelope` with NanoID requestId and RFC3339 UTC submittedAt; publish to `ops.<lane>` via `substrate.Conn`; print reply
  - [ ] 4.3 `status <requestId>`: compute tracker key via `processor.TrackerKey(requestId)` (form `vtx.op.<NanoID>`); read from Core KV; print
  - [ ] 4.4 `trace <requestId>`: read `health.processor.<instance>.auth-trace.<requestId>` from Health KV; print three-plane record; `--instance` flag defaults to `default`
  - [ ] 4.5 Unit tests: `TestOpSubmit_HappyPath` (stub conn), `TestOpStatus_HappyPath`, `TestOpTrace_HappyPath`, `TestOpTrace_Missing`

- [ ] Task 5 — `lattice graph` subcommand group (AC7)
  - [ ] 5.1 Create `cmd/lattice/graph/graph.go` — register `read`, `walk`, `keys` subcommands
  - [ ] 5.2 Add debug-only warning to all `Use` / `Long` cobra fields and to `--help` output
  - [ ] 5.3 `read <key>`: `substrate.Conn.KVGet` from `bootstrap.CoreKVBucket`; print raw JSON value
  - [ ] 5.4 `walk <startKey> [--depth N]`: use `aiagent.NewTraverser(conn, bootstrap.CoreKVBucket, bootstrap.CapabilityKVBucket)`; enumerate neighbors via `KVListKeys` + aspect-key enumeration; default depth 3, max 10; print vertex key tree
  - [ ] 5.5 `keys <prefix>`: `substrate.Conn.KVListKeys` on Core KV filtered by prefix; print one key per line
  - [ ] 5.6 Unit tests: `TestGraphRead_HappyPath`, `TestGraphKeys_HappyPath`

- [ ] Task 6 — `lattice lens` subcommand group (AC8)
  - [ ] 6.1 Create `cmd/lattice/lens/lens.go` — register `list`, `activate`, `deactivate`, `lag` subcommands
  - [ ] 6.2 `list`: `KVListKeys` on Core KV with `vtx.meta.` prefix; for each 3-segment key, read the vertex doc; filter `class == "meta.lens"`; print `key | canonicalName | isDeleted`
  - [ ] 6.3 `activate <file>`: parse JSON file as `CreateMetaVertex` payload; submit to meta lane via `lattice op submit` logic; print `metaKey` from `Detail["metaKey"]`
  - [ ] 6.4 `deactivate <key>`: submit `TombstoneMetaVertex` with `metaKey` to meta lane; print reply
  - [ ] 6.5 `lag`: read `health.refractor.*` keys from Health KV; print per-lens lag table
  - [ ] 6.6 Unit tests: `TestLensList_HappyPath`, `TestLensLag_HappyPath`

- [ ] Task 7 — `lattice query` subcommand group (AC9)
  - [ ] 7.1 Create `cmd/lattice/query/query.go` — register `cap` and `postgres` subcommands
  - [ ] 7.2 `cap <actorKey>`: read `cap.identity.<NanoID>` (strip `vtx.identity.` prefix if caller passes full vertex key) from Capability KV; print capability doc
  - [ ] 7.3 `postgres <sql>`: read `POSTGRES_URL` env or `--postgres-url` flag; open `database/sql` connection with `lib/pq` driver (already a go.mod dep or add it); execute query; reject DML with error message directing operator to `lattice op submit`; print rows as table or JSON array
  - [ ] 7.4 Unit tests: `TestQueryCap_HappyPath`, `TestQueryPostgres_DML_Rejected`

- [ ] Task 8 — `lattice health` subcommand group (AC10)
  - [ ] 8.1 Create `cmd/lattice/health/health.go` — register `summary`, `component`, `gates` subcommands
  - [ ] 8.2 `summary`: list all keys from Health KV; print key, value snippet, and age
  - [ ] 8.3 `component <name>`: list `health.<name>.*` keys; print full values
  - [ ] 8.4 `gates`: read `health.gates.phase1.*` keys; print gate number, `passed` bool, `completedAt`
  - [ ] 8.5 Unit tests: `TestHealthGates_HappyPath`, `TestHealthSummary_HappyPath`

- [ ] Task 9 — `lattice identity` subcommand group (AC11)
  - [ ] 9.1 Create `cmd/lattice/identity/identity.go` — register `create-unclaimed` and `claim` subcommands
  - [ ] 9.2 `create-unclaimed`: submits `CreateUnclaimedIdentity` on default lane; reads payload from `--payload @file.json` or stdin; prints reply including `Detail["claimKey"]` if present
  - [ ] 9.3 `claim`: submits `ClaimIdentity` on default lane; reads payload from `--payload @file.json` or stdin; prints reply
  - [ ] 9.4 Unit tests: `TestIdentityCreateUnclaimed_HappyPath`, `TestIdentityClaim_HappyPath`

- [ ] Task 10 — `lattice candidates` subcommand group (AC12)
  - [ ] 10.1 Create `cmd/lattice/candidates/candidates.go` — register `list` and `merge` subcommands
  - [ ] 10.2 `list`: enumerate keys from `duplicate-candidates` KV bucket; for each key, read and parse the entry; print `key | primaryKey | secondaryKey | score`
  - [ ] 10.3 `merge <primary> <secondary>`: read the duplicate-candidates entry for the secondary key to enumerate `edges`; submit `MergeIdentity` with `{primary, secondary, edges: [...]}` to default lane; print reply
  - [ ] 10.4 Unit tests: `TestCandidatesList_HappyPath`, `TestCandidatesMerge_HappyPath`

- [ ] Task 11 — `lattice auth-trace` subcommand (AC13, Story 3.5 carry)
  - [ ] 11.1 Create `cmd/lattice/authtrace/authtrace.go` — wraps `processor.LookupAuthTrace` (or reads Health KV directly if helper is not exported)
  - [ ] 11.2 `auth-trace <requestId>`: read `health.processor.<instance>.auth-trace.<requestId>` from Health KV; print three-plane record; `--instance` flag defaults to `default`
  - [ ] 11.3 Unit test: `TestAuthTrace_HappyPath`

- [ ] Task 12 — `lattice bootstrap` subcommand group (AC14)
  - [ ] 12.1 Create `cmd/lattice/bootstrap/bootstrap.go` — register `verify` and `inspect` subcommands
  - [ ] 12.2 `verify`: run assertions equivalent to `scripts/verify-kernel.go` — check primordial Core KV entries; exit non-zero on failures with summary; extract the assertion logic into `internal/bootstrap` if not already callable from Go
  - [ ] 12.3 `inspect`: read selected primordial entries (kernel root DDL, operator role, meta-permission vertices) from Core KV; print as table
  - [ ] 12.4 Unit test: `TestBootstrapVerify_HappyPath`

- [ ] Task 13 — End-to-end binary test (AC15)
  - [ ] 13.1 Create `cmd/lattice/cli_e2e_test.go` (package `main_test`)
  - [ ] 13.2 `TestCLI_HelpExits0`: `go build -o /tmp/lattice-test-bin ./cmd/lattice` in a temp dir; exec with `--help`; assert exit code 0 and stdout contains "lattice op"
  - [ ] 13.3 `TestCLI_VersionExits0`: exec `--version`; assert exit code 0

- [ ] Task 14 — Architecture purity verification
  - [ ] 14.1 `grep -rn "cli\|CLI" internal/processor/` → zero production-code hits
  - [ ] 14.2 `ContextHint` still has only `Reads []string`; no new fields
  - [ ] 14.3 `OperationReply` in `internal/processor/envelope.go` unchanged
  - [ ] 14.4 `golangci-lint run ./...` clean (pay attention to unused imports in cobra boilerplate)
  - [ ] 14.5 `make test-bypass` all-DEFENDED (Gate 2 regression)
  - [ ] 14.6 `make test-capability-adversarial` all-BLOCKED (Gate 3 regression)

## Dev Notes

### Overview

Story 6.1 is almost entirely a new binary — `cmd/lattice/` — that wires existing packages (`internal/processor`, `internal/substrate`, `internal/aiagent`, `internal/bootstrap`) to a CLI surface. No Processor changes. No new KV buckets. No new service or endpoint.

The key architectural constraint: every operation the CLI submits travels through the standard Processor write path. The CLI is just another NATS client that constructs `OperationEnvelope` and publishes to `ops.<lane>`.

---

### Framework: Cobra

No existing `cmd/` binary uses a CLI framework — the daemons (`processor`, `refractor`, `bootstrap`) use stdlib `flag` for their single-level flags. However, `lattice` has 9 command groups with multiple subcommands each. Cobra is the right choice here — it provides:
- Hierarchical command trees with clean help formatting
- Persistent flags (nats-url, output) that propagate to all subcommands
- Shell completion (free)
- Uniform `--help` behavior

Add cobra with `go get github.com/spf13/cobra@latest`. This is the first and only CLI library in the project.

Package layout: each command group is a separate sub-package under `cmd/lattice/` to keep `main.go` thin. The sub-packages export a `NewCommand() *cobra.Command` function that root.go imports and attaches.

```
cmd/lattice/
  main.go          — entry point, calls root.Execute()
  root.go          — root command, persistent flags, credential loading
  version.go       — --version
  config/          — config set-credential
  op/              — op submit / status / trace
  graph/           — graph read / walk / keys
  lens/            — lens list / activate / deactivate / lag
  query/           — query postgres / cap
  health/          — health summary / component / gates
  identity/        — identity create-unclaimed / claim
  candidates/      — candidates list / merge
  authtrace/       — auth-trace <requestId>
  bootstrap/       — bootstrap verify / inspect
  cli_e2e_test.go  — binary build + --help smoke test
```

---

### NATS Connection Pattern

Every subcommand that talks to NATS opens a connection, runs the operation, and closes it. Use the same `substrate.Connect` pattern as `cmd/lattice-pkg/main.go`:

```go
conn, err := substrate.Connect(ctx, substrate.ConnectOpts{
    URL:  natsURL,   // from --nats-url flag or NATS_URL env
    Name: "lattice-cli",
})
if err != nil {
    return fmt.Errorf("connect: %w", err)
}
defer conn.Close()
```

Do not cache or share the connection across subcommand invocations. CLI is a single-shot process.

---

### Credential Loading

`lattice config set-credential` writes `~/.lattice/credentials.json`. The credential file shape:

```json
{
  "credentials": [
    {
      "actorKey": "vtx.identity.<NanoID>",
      "natsURL": "nats://localhost:4222"
    }
  ]
}
```

`root.go` loads the credential file at startup and injects `actorKey` as the default `--actor` flag for `lattice op submit`, `lattice identity *`, and `lattice candidates merge`. Subcommands that do not submit operations do not need `actorKey`.

Phase 1: single credential entry in the file (first match wins). Phase 2: multi-profile support.

---

### `lattice op submit` — Envelope Construction

```go
env := processor.OperationEnvelope{
    RequestID:     substrate.NewNanoID(),         // substrate.NewNanoID() per Contract #1
    Lane:          processor.Lane(lane),           // from --lane flag
    OperationType: operationType,                  // from --operation-type flag
    Actor:         actorKey,                       // from --actor flag or credential file
    SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
    Payload:       json.RawMessage(payloadBytes),  // from --payload file or stdin
}
```

Optional flags for power users:
- `--class` → sets `env.Class` (Phase 1 DDL hint; kept for compat per envelope.go comment)
- `--context-hint-reads` → sets `env.ContextHint.Reads` (comma-separated keys)

Publish to `ops.<lane>` using request-reply pattern (same as test harnesses use `testutil.PublishOp`). The reply subject is a NATS inbox auto-generated by the `conn.RequestWithContext` call.

---

### `lattice op status` — Tracker Key

The op tracker lives in Core KV. The key is computed by `processor.TrackerKey(requestID)` which returns `vtx.op.<NanoID>` (extracted from the requestID, which is itself the NanoID). Confirm by reading the existing test usages in `internal/processor/integration_test.go`.

---

### `lattice auth-trace` — Story 3.5 Carry

The auth trace key pattern (from `internal/processor/step3_auth_trace.go`):

```
health.processor.<instance>.auth-trace.<requestId>
```

The `AuthTraceRecord` struct is in `internal/processor/step3_auth_trace.go`. Read the Health KV entry, unmarshal to `AuthTraceRecord`, and print. No new helpers needed — just a direct KV read using `bootstrap.HealthKVBucket` ("health-kv").

---

### `lattice graph walk` — Traverser

`aiagent.Traverser` already exists in `internal/aiagent/traversal.go` (Story 5.2). The `walk` subcommand instantiates it and uses `KVListKeys` to enumerate neighbors by reading aspect keys of the form `<startKey>.<localName>`. A simple BFS with depth limit is sufficient:

```go
traverser := aiagent.NewTraverser(conn, bootstrap.CoreKVBucket, bootstrap.CapabilityKVBucket)
```

The walk implementation should be a new method on the traverser OR implemented inline in the `graph/graph.go` file. If implemented inline, do not add a new exported method to `aiagent.Traverser` unless it's genuinely reusable — keep the traverser's exported surface minimal.

---

### `lattice candidates merge` — `edges` Parameter

Per Story 4.6 R3, `MergeIdentity` requires an `edges` parameter listing the secondary identity's inbound and outbound edges. The CLI reads the `duplicate-candidates` bucket entry for the secondary key, which contains the edge list as seeded by the identity-hygiene Lens. The merge payload:

```json
{
  "primary": "vtx.identity.<primaryNanoID>",
  "secondary": "vtx.identity.<secondaryNanoID>",
  "edges": ["lnk.identity.<id>.holdsRole.role.<id>", "..."]
}
```

If the `duplicate-candidates` entry does not exist for the secondary, the CLI should warn and prompt the operator to add `--force` to proceed without edge migration (the Processor will reject if edges exist but are undeclared — the CLI just surfaces the risk clearly).

---

### Postgres Driver

If `lib/pq` is not already in `go.mod`, add it: `go get github.com/lib/pq`. Alternatively, use `database/sql` with a no-import driver registration and require the caller to pass a DSN — but `lib/pq` is the standard Postgres driver for Go and should be added if absent. Check `go.mod` first.

---

### `--output json` Pattern

Every subcommand wraps its output in a uniform JSON envelope when `--output json` is set:

```go
type cliOutput struct {
    OK    bool   `json:"ok"`
    Data  any    `json:"data,omitempty"`
    Error *cliError `json:"error,omitempty"`
}
type cliError struct {
    Code    string `json:"code"`
    Message string `json:"message"`
}
```

Define this shared type in `cmd/lattice/root.go` or a shared `cmd/lattice/output/output.go` file. All subcommands import it. This avoids duplicate struct definitions.

---

### `lattice bootstrap verify` — Reuse verify-kernel logic

`scripts/verify-kernel.go` is a standalone `go run` script. Its assertion logic is not currently callable from Go packages. Two options:

**Option A (preferred):** Extract assertion logic into an exported function `bootstrap.VerifyKernel(ctx context.Context, conn *substrate.Conn) ([]string, error)` in a new file `internal/bootstrap/verify.go`. Both `scripts/verify-kernel.go` and `lattice bootstrap verify` call it.

**Option B:** Duplicate the assertions inline in `cmd/lattice/bootstrap/bootstrap.go`.

Use Option A — it eliminates drift between the CLI and the script. This is a small additive change to `internal/bootstrap/`.

---

### Health KV Bucket

`bootstrap.HealthKVBucket = "health-kv"`. All `lattice health` and `lattice auth-trace` commands read from this bucket.

---

### Files NOT to Touch

- `internal/processor/envelope.go` — do NOT add CLI-specific fields to `OperationEnvelope` or `OperationReply`
- `internal/processor/commit_path.go` — no new commit path steps
- `internal/processor/step3_auth_trace.go` — read-only reference; do not add CLI helpers here
- `cmd/lattice-pkg/main.go` — `lattice-pkg` stays separate and unchanged
- `_bmad-output/planning-artifacts/` — sub-agents must never edit planning artifacts
- `packages/` — no package DDL changes in this story
- `internal/aiagent/traversal.go` — may read; should not modify unless adding a `walk`-specific helper, and only if it's a genuinely reusable primitive (prefer inline in graph.go)

---

### Architecture Compliance Checklist

- [ ] `grep -rn "cli\|CLI" internal/processor/` → zero production-code hits
- [ ] `ContextHint` still has only `Reads []string` field
- [ ] `OperationReply` struct in `envelope.go` matches prior shape exactly
- [ ] `lattice graph` help text carries the debug-only warning on all three subcommands
- [ ] `make test-cli` exits 0
- [ ] `make test-bypass` all-DEFENDED (Gate 2 regression)
- [ ] `make test-capability-adversarial` all-BLOCKED (Gate 3 regression)
- [ ] `make verify-kernel` green
- [ ] `golangci-lint run ./...` clean

---

### References

- [Source: `_bmad-output/planning-artifacts/epics.md` §Story 6.1, lines 1402–1442] — original AC
- [Source: `cmd/lattice-pkg/main.go`] — binary convention; credential loading pattern; substrate.Connect usage
- [Source: `internal/processor/envelope.go`] — `OperationEnvelope`, `OperationReply` shapes the CLI constructs/parses
- [Source: `internal/processor/step3_auth_trace.go`] — `AuthTraceRecord` and key pattern for `lattice auth-trace`
- [Source: `internal/aiagent/traversal.go`] — `Traverser` used by `lattice graph walk`
- [Source: `internal/bootstrap/primordial.go`] — KV bucket name constants (`CoreKVBucket`, `CapabilityKVBucket`, `HealthKVBucket`)
- [Source: `internal/substrate/kv.go`] — `KVGet`, `KVListKeys`, `KVPut`
- [Source: `packages/identity-hygiene/lenses.go`] — `duplicate-candidates` KV bucket name
- [Source: `_bmad-output/planning-artifacts/lattice-architecture.md:94`] — Adjacency KV is Refractor-private; direct access prohibited
- [Source: `_bmad-output/implementation-artifacts/5-2-cold-start-ai-agent-traversal.md`] — Traverser primitives
- [Source: `_bmad-output/implementation-artifacts/5-3-compensating-operation-ddl-rollback.md`] — brief structure template
- [Source: `_bmad-output/implementation-artifacts/WINSTON-RESUME.md`] — operating conventions, no-history-comment rule

---

## Implementation Tier & Budget

**Model tier: Sonnet** (bounded scope against established contracts; all underlying primitives already ship).
**Estimated token budget: ~120K** (input + output — tracking only, NOT enforced per Rule 8 in WINSTON-RESUME.md). Sub-agent self-reports are typically 20-30% low vs outer telemetry; trust the task-notification `total_tokens`.

---

## Stuck-Loop Halt Criteria

Halt and surface for Winston review if any of the following occur:
- The same compilation error or cobra wiring failure recurs after 3+ fix attempts without a different root cause hypothesis.
- Any `lattice op submit` implementation path requires adding a field to `OperationEnvelope` or `OperationReply` — stop immediately; this is a Guardrail 1 violation, not a trade-off.
- `make test-cli` fails after 2 debug attempts with non-flake root causes.
- Any test in `make test-bypass` or `make test-capability-adversarial` flips from DEFENDED/BLOCKED to a different status.
- `lattice bootstrap verify` requires changing `internal/bootstrap/` in a way that modifies existing exported types or function signatures.

Do NOT halt for token budget alone.

---

## Dev Agent Record

### Agent Model Used

<!-- to be filled by sub-agent -->

### Debug Log References

<!-- append D1, D2... entries here -->

### Completion Notes List

<!-- append on completion -->

### File List

<!-- append on completion -->
