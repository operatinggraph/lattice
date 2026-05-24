# Code Review Report — Story 6.1: Lattice CLI Tool

**Staged diff reviewed:** Story 6.1 uncommitted changes (staged + untracked)
**Reviewer:** bmad-code-review (Sonnet 4.6)
**Date:** 2026-05-24
**Spec file:** `_bmad-output/implementation-artifacts/6-1-lattice-cli-tool.md`

**Files in scope:**
- `cmd/lattice/main.go` — binary entry point
- `cmd/lattice/root.go` — root cobra command, credential loading
- `cmd/lattice/version.go` — version subcommand
- `cmd/lattice/output/output.go` — JSON envelope types and helpers
- `cmd/lattice/output/connect.go` — NATS connection helper
- `cmd/lattice/output/submit.go` — JetStream publish + KV tracker poll
- `cmd/lattice/op/op.go` + `op_test.go` — op submit/status/trace
- `cmd/lattice/config/config.go` + `config_test.go` — set-credential
- `cmd/lattice/graph/graph.go` + `graph_test.go` — read/walk/keys
- `cmd/lattice/lens/lens.go` + `lens_test.go` — list/activate/deactivate/lag
- `cmd/lattice/query/query.go` + `query_test.go` — cap/postgres
- `cmd/lattice/health/health.go` + `health_test.go` — summary/component/gates
- `cmd/lattice/identity/identity.go` + `identity_test.go` + `identity_debug_test.go` — create-unclaimed/claim
- `cmd/lattice/candidates/candidates.go` + `candidates_test.go` — list/merge
- `cmd/lattice/authtrace/authtrace.go` + `authtrace_test.go` — auth-trace
- `cmd/lattice/bootstrap/bootstrap.go` + `bootstrap_test.go` — verify/inspect
- `cmd/lattice/cli_e2e_test.go` — binary build smoke test
- `internal/bootstrap/verify.go` — VerifyKernel + InspectKernel
- `internal/testutil/embedded_nats.go` — StoreDir per-TempDir fix
- `Makefile` — build target + test-cli target
- `go.mod` / `go.sum` — cobra + pgx dependencies

**Note:** Parallel subagent review is not available in this session. All three layers — Blind Hunter, Edge Case Hunter, Acceptance Auditor — were run sequentially inline with full project access.

---

## Summary

**2 MUST FIX items found.** The overall implementation is structurally clean: all nine command groups are wired, no processor changes exist, the credential file uses 0600 perms, the `lattice graph` debug warning is present on all three subcommands, and the `lattice query postgres` DML guard fires before any Postgres connection is opened. The two blockers are: (1) a pervasive exit-code violation — `output.PrintJSONError` returns `nil`, and every `return output.PrintJSONError(...)` call in all command groups exits 0 instead of non-zero, violating AC2; (2) `output.SubmitOp` polls the Core KV tracker indefinitely until context timeout when the Processor rejects an operation, because rejected ops never write a tracker key — leaving every `--output json` error submission hanging for 10 seconds before surfacing "context deadline exceeded" instead of the actual rejection reason.

---

## 🔴 MUST FIX

### MF-1 — `output.PrintJSONError` returns `nil` — all `return output.PrintJSONError(...)` calls exit 0 on error in JSON mode (33 call sites across all command groups)

**Files:** `cmd/lattice/output/output.go:31-38` (source); 33 call sites across all 9 command groups
**Review layer:** Blind Hunter + Acceptance Auditor

**What's wrong:**

`output.PrintJSONError` encodes a `{"ok":false,"error":{...}}` envelope to stdout and returns `json.NewEncoder(os.Stdout).Encode(...)`, which returns `nil` on a successful write. Every call site of the form `return output.PrintJSONError("code", "msg")` returns `nil` to cobra's `RunE`, which causes cobra to exit 0.

AC2 requires non-zero exit code on error. This affects every JSON-mode error path in every command group:

```
cmd/lattice/bootstrap/bootstrap.go      3 sites
cmd/lattice/candidates/candidates.go    4 sites
cmd/lattice/config/config.go            1 site
cmd/lattice/graph/graph.go              3 sites
cmd/lattice/health/health.go            3 sites
cmd/lattice/identity/identity.go        4 sites
cmd/lattice/lens/lens.go                6 sites
cmd/lattice/op/op.go                    2 sites
cmd/lattice/query/query.go              6 sites
cmd/lattice/authtrace/authtrace.go      1 site
```

The `_ = output.PrintJSONError(...); os.Exit(1)` pattern (used in ~10 places) correctly exits non-zero; the `return output.PrintJSONError(...)` pattern (33 places) does not.

**Consequence:** In JSON mode, any error condition — connection failure, key not found, DML rejected, auth denied, parse error — will write `{"ok":false,...}` to stdout then exit 0. Scripts and pipelines using `--output json` and checking exit codes will see all errors as success.

**What to do:**

Option A (recommended — minimal blast radius): Change `output.PrintJSONError` to emit the envelope and return a sentinel error:

```go
// ErrJSONError is returned by PrintJSONError so callers that use
// `return output.PrintJSONError(...)` propagate a non-nil error to cobra.
var ErrJSONError = errors.New("command failed (see JSON output)")

func PrintJSONError(code, message string) error {
    _ = json.NewEncoder(os.Stdout).Encode(Envelope{
        OK:    false,
        Error: &EnvError{Code: code, Message: message},
    })
    return ErrJSONError
}
```

This makes every existing `return output.PrintJSONError(...)` return a non-nil error to cobra, which then calls `os.Exit(1)` via `Execute()`. No call sites need to change. The error string printed by cobra's default error handler will be "command failed (see JSON output)" on stderr — suppress it with the already-present `SilenceErrors: true` in root.go's cobra command.

Option B: Replace all 33 `return output.PrintJSONError(...)` with `_ = output.PrintJSONError(...); os.Exit(1)` — matches the existing correct pattern but requires touching all call sites.

**Which AC/Guardrail:** AC2 ("Exit code is non-zero on error, zero on success.").

---

### MF-2 — `output.SubmitOp` polls indefinitely on rejection — rejected ops hang for 10 seconds and surface "context deadline exceeded" instead of the actual error

**File:** `cmd/lattice/output/submit.go:38-60`
**Review layer:** Edge Case Hunter + Acceptance Auditor

**What's wrong:**

`SubmitOp` publishes to `ops.<lane>` via `conn.JetStream().Publish` (fire-and-forget, no reply subject), then polls Core KV for the tracker key. The tracker key is only written on successful commit (step 8 of the Processor's commit path). When the Processor rejects an operation — auth denied, DDL violation, revision conflict — it calls `replyTo(msg, BuildRejectedReply(...))` followed by `msg.TermWithReason(...)`, but because the message was published without a reply subject (`JetStream.Publish`), `msg.Reply()` is empty and `replyTo` is a no-op. No tracker key is written for rejected ops.

Result: the poll loop at `submit.go:44-60` never finds the tracker key. The loop runs every 50 ms until `ctx.Done()` fires (default timeout: `opReplyTimeout = 10 * time.Second` in `op.go`, `DefaultTimeout = 10 * time.Second` in `output/connect.go`). After 10 seconds the caller receives:

```
wait for reply: context deadline exceeded
```

This hides the actual rejection reason from the operator. `lattice op submit --lane default --operation-type X --actor <unauthorized>` hangs for 10 seconds and prints a timeout error rather than "auth denied: no matching permission."

This affects `op submit`, `lens activate`, `lens deactivate`, `identity create-unclaimed`, and any other command using `output.SubmitOp`. By contrast, `candidates merge` has its own `submitOp` using `conn.NATS().RequestWithContext` and correctly receives rejection replies.

The comment in `submit.go` claims "JetStream stream delivery rewrites each message's reply subject to the JetStream internal ACK address." This is incorrect for direct-publish-to-stream: the JetStream consumer preserves the original `Reply()` subject (verified by `identity_debug_test.go` line 44 which prints `m.Reply()`). The poll-based approach was chosen for a reason that doesn't hold — but the consequence is the timeout-on-rejection bug above.

**What to do:**

Replace `JetStream.Publish` with `conn.NATS().RequestWithContext` in `SubmitOp`. The Processor's `replyTo` will then send the rejection directly to the inbox:

```go
func SubmitOp(ctx context.Context, conn *substrate.Conn, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
    data, err := json.Marshal(env)
    if err != nil {
        return nil, fmt.Errorf("marshal envelope: %w", err)
    }
    subject := "ops." + string(env.Lane)
    msg, err := conn.NATS().RequestWithContext(ctx, subject, data)
    if err != nil {
        return nil, fmt.Errorf("NATS request to %s: %w", subject, err)
    }
    var reply processor.OperationReply
    if err := json.Unmarshal(msg.Data, &reply); err != nil {
        return nil, fmt.Errorf("parse reply: %w", err)
    }
    return &reply, nil
}
```

This matches the working `candidates.go` pattern. The KV tracker poll loop and the `opPollInterval` constant can then be removed from `submit.go`. Callers already handle `reply.Status == ReplyStatusRejected` and produce the correct error output.

Update `TestOpSubmit_HappyPath` in `op_test.go`: the test currently drives the Processor via `cons.Consume` while `SubmitOp` polls KV. After the fix, the test pattern remains valid (RequestWithContext still triggers the Processor consumer inline) but the test no longer needs the channel synchronization around `doneC` — the reply arrives synchronously from the request.

**Which AC/Guardrail:** AC4 ("On rejection: prints the `error.code` and `error.message`; exits non-zero."), AC2 (non-zero exit on error). Indirectly: Guardrail 2 (no bypass) — the fix uses the same auth path, just the transport changes from fire-and-forget to request-reply.

---

## 🟡 SHOULD CONSIDER

### SC-1 — `lattice graph keys` uses `--prefix` flag instead of positional `<prefix>` argument (AC7 deviation)

**File:** `cmd/lattice/graph/graph.go:164-211`
**Review layer:** Acceptance Auditor

**What's wrong:**

AC7 specifies `keys <prefix>` (positional argument), and Task 5.5 echoes `keys <prefix>`. The implementation uses `keys [--prefix <prefix>]` (optional flag). The command's `Use` string is `"keys [--prefix <prefix>]"` and the `Args` field is unset (accepts any count).

**What to do:** Change the command to accept a single optional positional argument for `prefix`, matching AC7:

```go
Use:  "keys [<prefix>]",
Args: cobra.MaximumNArgs(1),
RunE: func(cmd *cobra.Command, args []string) error {
    prefix := ""
    if len(args) == 1 {
        prefix = args[0]
    }
    ...
}
```

Remove the `--prefix` flag. Update `graph_test.go` accordingly.

**Which AC/Guardrail:** AC7 (`keys <prefix>` interface).

---

### SC-2 — `identity_debug_test.go` is a debug artifact — `fmt.Printf` diagnostics, `time.Sleep`, and a blank `nats` import

**File:** `cmd/lattice/identity/identity_debug_test.go`
**Review layer:** Blind Hunter + Edge Case Hunter

**What's wrong:**

`TestIdentityDebug` was clearly used to diagnose the request-reply behavior during development. It contains:
- Five `fmt.Printf` calls printing raw pipeline internals to test output
- `time.Sleep(50 * time.Millisecond)` (fragile synchronization)
- `_ = nats.DefaultURL` (blank identifier to keep an otherwise-unused `nats` import alive)
- The `close(ready); <-ready` idiom in `identity_test.go:54-55` is also a no-op (the closed channel is immediately readable; this provides zero synchronization)

This file will produce noisy output on every `make test-cli` run and its `time.Sleep` makes it flaky under load.

**What to do:** Delete `cmd/lattice/identity/identity_debug_test.go`. Remove the no-op `close(ready); <-ready` from `identity_test.go`.

**Which AC/Guardrail:** AC15 (test quality); general code hygiene.

---

### SC-3 — Credential write is non-atomic — partial write on crash leaves `credentials.json` truncated

**File:** `cmd/lattice/config/config.go:111-119`
**Review layer:** Edge Case Hunter

**What's wrong:**

`writeCredential` uses `os.WriteFile(path, data, 0600)`, which truncates and rewrites the file in-place. If the process crashes (OOM, SIGKILL, power loss) between `WriteFile` opening the file for write and completing the flush, the credential file is left in a truncated or partially-written state. On next startup `json.Unmarshal` will fail silently (line 99: `_ = json.Unmarshal(data, &cf)`) and all existing credentials will be lost.

**What to do:** Write atomically via a temp file in the same directory followed by `os.Rename`:

```go
tmpPath := path + ".tmp"
if err := os.WriteFile(tmpPath, data, 0600); err != nil {
    return fmt.Errorf("write temp %s: %w", tmpPath, err)
}
if err := os.Rename(tmpPath, path); err != nil {
    _ = os.Remove(tmpPath)
    return fmt.Errorf("rename credential file: %w", err)
}
```

**Which AC/Guardrail:** AC3 ("File is created if absent; existing entries are merged (not overwritten wholesale).").

---

### SC-4 — `lattice query postgres` DML guard is bypassable via SQL comments and CTEs

**File:** `cmd/lattice/query/query.go:204-213`
**Review layer:** Edge Case Hunter

**What's wrong:**

`rejectDML` trims leading whitespace and checks for uppercase keyword prefix. It does not handle:
- SQL line comments before the keyword: `-- bypass\nINSERT INTO foo VALUES (1)` → passes (after TrimSpace, starts with `--`)
- Block comments: `/* bypass */ INSERT INTO foo` → passes (starts with `/*`)
- CTEs with DML: `WITH cte AS (...) INSERT INTO ...` → passes (starts with `WITH`)
- `DO $$ BEGIN INSERT ... END $$` PL/pgSQL blocks → passes (starts with `DO`)

**What to do:** Either extend `rejectDML` to strip SQL-style line and block comments before keyword matching, or add the `WITH` and `DO` keywords to `dmlKeywords`. Minimum fix:

```go
// Strip single-line comments (-- ...) and block comments (/* ... */) from the
// head of the query before keyword matching.
func stripLeadingComments(sql string) string {
    s := strings.TrimSpace(sql)
    for {
        if strings.HasPrefix(s, "--") {
            if i := strings.Index(s, "\n"); i >= 0 {
                s = strings.TrimSpace(s[i+1:])
                continue
            }
        }
        if strings.HasPrefix(s, "/*") {
            if i := strings.Index(s, "*/"); i >= 0 {
                s = strings.TrimSpace(s[i+2:])
                continue
            }
        }
        break
    }
    return s
}
```

Also add `"WITH"` and `"DO"` to `dmlKeywords`.

**Which AC/Guardrail:** AC9 ("Phase 1: read-only queries only; DML statements return an error"); Guardrail 4 (thin pass-through with read-only guard as only transformation).

---

### SC-5 — `make up` does not build `bin/lattice` — AC16 specifies that `make up` builds the binary

**File:** `Makefile`
**Review layer:** Acceptance Auditor

**What's wrong:**

AC16: "`make up` builds the `lattice` binary to `bin/lattice`." The `Makefile` adds `go build -o bin/lattice ./cmd/lattice` to the `build` target only. The `up` target builds `bin/bootstrap` and `bin/refractor` inline but does not invoke `make build` or add `bin/lattice` explicitly. Running `make up` from a clean checkout leaves `bin/lattice` absent.

**What to do:** Add `bin/lattice` to the `up` recipe, alongside the existing inline builds:

```makefile
up:
    ...
    @echo "==> Building lattice CLI..."
    go build -o bin/lattice ./cmd/lattice
    ...
```

**Which AC/Guardrail:** AC16 ("`make up` builds the `lattice` binary to `bin/lattice`").

---

### SC-6 — "Story 3.5 carry" in `authtrace` command `Short` text exposes story provenance to end users

**File:** `cmd/lattice/authtrace/authtrace.go:27`
**Review layer:** Blind Hunter

**What's wrong:**

The cobra `Short` description for the `auth-trace` command reads:

```
"Read the three-plane auth trace record for a request (Story 3.5 carry)"
```

The brief (line 30) prohibits history references in code. While a cobra `Short` string is user-visible help text rather than an inline comment, `(Story 3.5 carry)` is opaque to operators and exposes internal sprint terminology in `lattice --help` and `lattice auth-trace --help` output.

**What to do:** Remove the story attribution from the `Short` string:

```go
Short: "Read the three-plane auth trace record for a request",
```

**Which AC/Guardrail:** Brief line 30 (no history comments); AC1 (help text clarity).

---

## 🟢 NITS

### N-1 — `credentialFilePath()` and `credentialFile`/`credential` types are duplicated between `root.go` and `config/config.go`

**Files:** `cmd/lattice/root.go:31-36,117-126`; `cmd/lattice/config/config.go:15-21,125-133`
**Review layer:** Blind Hunter

Both files define identical `credentialFile`, `credential` structs, and `credentialFilePath()` functions. If the path logic or struct fields change in one, the other will drift. Move these to a shared package (e.g., `cmd/lattice/creds/`) and import from both.

---

### N-2 — `TestIdentityClaim_HappyPath` only marshals the envelope — it does not test the `claim` submission path

**File:** `cmd/lattice/identity/identity_test.go:76-100`
**Review layer:** Acceptance Auditor

The test assembles a `ClaimIdentity` envelope, marshals it to JSON, and checks that the JSON contains `"ClaimIdentity"` and the `requestId`. It does not call `submitOp`, so the NATS publish+reply path is never exercised for `ClaimIdentity`. The test name `TestIdentityClaim_HappyPath` implies a happy-path flow but tests only envelope marshalling. Consider renaming it `TestIdentityClaim_EnvelopeShape` to avoid misleading future readers, or extend it to do a full round-trip using the embedded NATS harness.

---

### N-3 — `graph walk` calls `KVListKeys` at every BFS node — O(nodes × depth) full-scan calls

**File:** `cmd/lattice/graph/graph.go:130-138`
**Review layer:** Edge Case Hunter

The BFS closure calls `conn.KVListKeys(ctx, bootstrap.CoreKVBucket)` (a full Core KV scan) at every visited node. With depth=10 and a wide graph this produces up to 10 × fan-out full scans. On large graphs this will be slow and produce high NATS KV read traffic. The result should be fetched once before BFS begins and the filtered copy passed to the closure. (This is a `lattice graph` debug command with explicit "use with caution" warnings, so correctness is not compromised — this is a performance NIT.)

---

### N-4 — Pre-existing history comment in `internal/testutil/embedded_nats.go` file header (not introduced by Story 6.1)

**File:** `internal/testutil/embedded_nats.go:1`
**Review layer:** Blind Hunter

The file header reads `// Story 4.7 cleanup — embedded NATS + drive helpers for external test`. This violates the no-history-comments rule but was present before Story 6.1 (the story only modified lines 32-48). Not actionable in this story but flagged for completeness.

---

## Architecture / NFR Compliance Sign-off

- **Guardrail 1 — No new Processor read surface:** PASS. `grep -rn "cli\|CLI" internal/processor/` returns zero production-code hits. (Matches containing `client` in prose comments are not violations.)
- **Guardrail 2 — No CLI-specific bypass:** PASS. `lattice op submit` publishes to `ops.<lane>` with no special headers or subjects. The `--actor` flag resolves to an actor key that travels through the standard Processor auth path.
- **Guardrail 3 — `lattice graph` debug-only warning present:** PASS. The `debugWarning` constant appears in the `Long` text of all three subcommands (`read`, `walk`, `keys`) and in the parent `graph` command's `Long` field.
- **Guardrail 4 — `lattice query postgres` is thin pass-through:** PASS. `rejectDML` is the only transformation (SC-4 describes a gap in its completeness). No SQL rewriting, no ORM, no query wrapping beyond the DML guard.
- **Guardrail 5 — No new inline permissions:** PASS. No new permission grants minted. `candidates merge` enforces via the Processor's `MergeIdentity` Capability check; `lens activate/deactivate` uses `meta` lane with standard auth.
- **No new `OperationReply` fields:** PASS. `internal/processor/envelope.go` `OperationReply` struct is unchanged. No `CLIReply`, `CliHint`, or similar field present.
- **No new `ContextHint` fields:** PASS. `ContextHint` still has only `Reads []string`. The `--context-hint-reads` flag populates the existing field.
- **No `cli`/`CLI` refs in `internal/processor/`:** PASS. (See Guardrail 1.)
- **Credential file mode 0600:** PASS. `config/config.go:118` uses `os.WriteFile(path, data, 0600)`.
- **`lattice graph` debug-only warning present:** PASS. (See Guardrail 3.)
- **`lattice query postgres` DML rejected:** PASS (with caveat). `rejectDML` fires before any Postgres connection is opened. Comment bypass gap is flagged as SC-4.
- **No history comments in 6.1-changed files:** PASS with one NIT. No history comments in any file introduced by Story 6.1. Pre-existing `// Story 4.7 cleanup` in `embedded_nats.go` header is not a 6.1 introduction (N-4).
- **Binary builds and `--help` exits 0:** PASS (pending MF-1 fix — `--help` is unaffected by the exit-code bug since it doesn't enter RunE).
- **`internal/bootstrap/verify.go` — no modified exported types or signatures:** PASS. `VerifyKernel(ctx, conn) []string` and `InspectKernel(ctx, conn) ([]KernelEntry, error)` are new additions, not modifications to existing signatures.

---

## Summary Table

| ID | Severity | Title | File(s) | AC/Guardrail |
|---|---|---|---|---|
| MF-1 | 🔴 MUST FIX | `PrintJSONError` returns nil — all JSON error paths exit 0 | `output/output.go` + 33 call sites | AC2 |
| MF-2 | 🔴 MUST FIX | `SubmitOp` JetStream path hangs 10s on rejection — no tracker written for rejected ops | `output/submit.go` | AC2, AC4 |
| SC-1 | 🟡 SHOULD CONSIDER | `graph keys` uses `--prefix` flag vs positional `<prefix>` arg (AC7 deviation) | `graph/graph.go` | AC7 |
| SC-2 | 🟡 SHOULD CONSIDER | `identity_debug_test.go` is a debug artifact — `fmt.Printf`, `time.Sleep`, blank import | `identity/identity_debug_test.go` | AC15 |
| SC-3 | 🟡 SHOULD CONSIDER | Non-atomic credential write — crash truncates `credentials.json` | `config/config.go` | AC3 |
| SC-4 | 🟡 SHOULD CONSIDER | DML guard bypassed by SQL comments and CTEs | `query/query.go` | AC9, G4 |
| SC-5 | 🟡 SHOULD CONSIDER | `make up` does not build `bin/lattice` — AC16 gap | `Makefile` | AC16 |
| SC-6 | 🟡 SHOULD CONSIDER | "Story 3.5 carry" in user-facing help text | `authtrace/authtrace.go:27` | Brief line 30 |
| N-1 | 🟢 NIT | `credentialFilePath()` and credential types duplicated in `root.go` and `config/config.go` | both | — |
| N-2 | 🟢 NIT | `TestIdentityClaim_HappyPath` only tests envelope marshalling, not submission | `identity/identity_test.go` | AC15 |
| N-3 | 🟢 NIT | `graph walk` calls `KVListKeys` at every BFS node | `graph/graph.go` | — |
| N-4 | 🟢 NIT | Pre-existing history comment in `embedded_nats.go` header (not from 6.1) | `internal/testutil/embedded_nats.go:1` | Brief line 30 |

---

Review complete: **2 MUST FIX, 6 SHOULD CONSIDER, 4 NITS**
