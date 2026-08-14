# A stale DDL cache after a failed Invalidate must not read as "not sensitive"

**Status: ✅ Winston-ratified — build-ready (in-fire, 2026-08-14).** Implementation-level decision, no
frozen-contract change, no architectural fork (Steward §0 decide-don't-defer). One fire, single increment.
**Component:** Processor — `internal/processor/{ddl_cache,step65_encrypt,health}.go` + `commit_path.go` wiring.
**Backlog row:** [lattice.md](../planning-artifacts/backlog/lattice.md) → Component maintenance →
*[Processor] Step 6.5 continues past what it cannot adjudicate*.
**Consumer, live:** clinic-domain's `.encounter` PHI (`appointmentEncounter`, `sensitive: true`), the fire
that gave this row its first live subject (`retention-class-key-custody-design.md` §26.3).

---

## 1. The bug, precisely

Step 6.5 (`encryptSensitiveMutations`, `step65_encrypt.go:60-74`) resolves each mutation's governing DDL via
the SAME shared resolver step 6 uses (`ddlResolver.resolveGoverningDDLChecked`). Two outcomes both return
`ok=false`:

1. **A genuine live-read fault** mid-`instanceOf`-chain-walk — already handled: `fault != nil && !ok` returns
   a terminal rejection error (`step65_encrypt.go:64-74`). Correct, and unchanged by this fire.
2. **A plain cache miss with no fault** — `DDLs.Lookup(class)` (`ddl_cache.go:278-279`) is a pure in-memory
   map read; it has no notion of "fault". A miss here falls straight to `!ok || !ref.Sensitive → continue`
   (`step65_encrypt.go:82-83`), which commits the mutation's data **as plaintext**.

Outcome 2 is CORRECT and Contract #1 §1.6-sanctioned ("Permissive-by-Default": a class with no resolvable
governing DDL is genuinely ungoverned) **only when the cache is known-accurate**. It is silently WRONG when
the cache is stale relative to what is already durably committed to Core KV — and that staleness is a real,
reachable state:

- `DDLCache` is built once at startup via `Refresh` (fail-closed: "a root that did not load refuses the
  WHOLE refresh", `ddl_cache.go:381-386`) and thereafter updated **only** by `Invalidate`, called
  synchronously from step 8 after any commit touching `vtx.meta.>` (`step8_commit.go`).
- `Invalidate`'s two error returns (`ddl_cache.go:883`, `:930`) are **not** retried and not tracked anywhere.
  The caller (`step8_commit.go`, `c.Logger.Warn("step 8: DDL cache invalidation failed (commit already
  durable)", ...)`) just logs and moves on. The meta-vertex commit — e.g. a package install marking a new
  class `sensitive: true`, or a class already in flight — is durable in Core KV; the in-process cache simply
  never learns it.
- There is no periodic re-`Refresh`. Once an `Invalidate` fails, the cache is wrong until the process
  restarts — silently, with no observable signal anywhere.

So a class whose DDL genuinely exists and says `sensitive: true` can resolve as `ok=false` at step 6.5 for a
reason that has nothing to do with Contract #1 §1.6's permissive default — the class IS declared, the
declaration just hasn't reached this process's cache — and step 6.5 currently cannot tell the two apart.

**Non-goal, RETRACTED — the OTHER arm the board row named is a real, still-open gap, not a sanctioned one.**
This section originally claimed the empty-`class` arm (`step65_encrypt.go:60`) was Contract #1 §1.6's
permissive-by-design behavior verbatim. The security review (§6) read §1.6 more carefully and found the
claim doesn't hold: §1.6's permissive clause is about a class with **no DDL declared** and about "undeclared
aspects have no enforced sensitivity" — neither addresses a mutation document that **omits or misstates**
`class` for a key whose DDL-declared localName **is** sensitive. Nothing checks a document's self-reported
`class` against its own key's localName, at step 6 or step 6.5 — so a script that omits `class`, or names an
unrelated one, on a sensitive aspect's key commits PHI plaintext regardless of cache health. This is real and
out of scope for THIS fire (it needs a genuinely new class-integrity check, not a mirror of an existing
pattern — a Designer-lane item, not an in-line Winston decision), but it is **filed**, not closed:
`lattice.md` → *[Processor] Sensitive-aspect resolution trusts a mutation's self-reported `class`, never the
key's own localName* (📐 needs designer pass). **This fire narrows the board row to the cache-staleness arm
only** — the arm this doc actually fixes — and the filed row above carries the other one forward.

## 2. The fix

**New state: a sticky "degraded" latch on `DDLCache`, set on any `Invalidate` failure.**

| State | Created | Reset | Carried across | Ordered relative to |
|---|---|---|---|---|
| `degraded` (bool + since + err + the metaRootKey that failed) | Zero value at `NewDDLCache` (not degraded) | **Never auto-clears** — deliberate; see below | N/A — in-process only, a restart runs a fresh `Refresh` and starts clean | Set by `Invalidate`'s two existing error-return paths, under the same `invalidateMu`/`mu` discipline already serializing writers. Read by step 6.5 and the heartbeat; never by step 6 (step 6 keeps its existing permissive posture unconditionally — see §3 non-goals) |

**Why sticky-until-restart, not auto-clearing on the next successful `Invalidate`.** A later, unrelated root's
successful `Invalidate` proves Core KV reads work again *now* — it does not retroactively fix the ROOT that
failed earlier, whose canonicalName (if any) is exactly the piece the cache is missing. Since step 6.5 cannot
know in advance which canonicalName a missed root would have claimed, precise per-root tracking cannot answer
"is *this* class's absence explained by the failure" — only a global, conservative "the cache is not fully
trustworthy right now" answers it, and that must stay set until the only event that actually re-establishes
full trust: a fresh `Refresh` (i.e., a restart). Prevention best-effort (retried reads inside
`Invalidate`/`Refresh` already), detect-and-recover authoritative (Health signal + fail-closed write rejection
until an operator restarts) — the platform's standing posture elsewhere (personal-lens-grant-change-trigger
design §8.2, erasure-orchestration design).

**`step65_encrypt.go`:** when resolution returns `!ok` with `fault == nil`, additionally check
`resolver.DDLs.Degraded()`. If degraded, treat it exactly like the existing fault path — terminal rejection,
same error shape/wording family — instead of falling through to `!ok || !ref.Sensitive`. Not degraded → the
existing Contract #1 §1.6 permissive-default `continue` is unchanged.

**Health (`internal/processor/health.go`):** mirror the shipped `ProcessorLaneLagging` pattern exactly
(`health.go:296-311`, the `active[...] = activeIssue{...}` + `reconcileIssues`'s since-persistence). Add a
`ddlCache` field + `AttachDDLCache(*DDLCache)` setter (mirrors `AttachCapabilityAuthorizer`,
`health.go:175-177`), wired from `MakePipeline` (`commit_path.go`) right after `ddls := NewDDLCache(...)`
(`:1069-1072`) — the same site `hb.AttachCapabilityAuthorizer(ca)` already wires from, a few lines above.
In `buildHealthDoc`, when `h.ddlCache.Degraded()` is true, add `active["DDLCacheDegraded"] = activeIssue{code:
"DDLCacheDegraded", severity: "error", message: fmt.Sprintf("DDL cache invalidation failed at %s for %s and
has not recovered (%v); sensitive-class writes may be rejected until the processor restarts", since, key,
err)}`. **Severity `error`** (→ `unhealthy` via `aggregateStatus`), not `warning`: unlike lane-lag (latency
only), this state is actively rejecting legitimate sensitive-class operations for affected submitters — a
real functional degradation, not just a slow queue.

## 3. Non-goals

- **No change to step 6's own permissive-default posture.** Step 6 does not encrypt; committing a mutation
  step 6 validated as "no governing DDL" plaintext is Contract #1 §1.6's designed behavior regardless of
  cache staleness (the write-scope gate, not the sensitivity gate) — step 6.5 is the only place a stale
  "no DDL" answer has a security consequence (plaintext where ciphertext was owed).
- **No retry-until-success for a failed `Invalidate`.** That is a larger mechanism (a durable retry queue or a
  periodic background `Refresh`) with its own state-lifetime questions; sticky-latch + loud Health signal +
  fail-closed step 6.5 is the right-sized fix for an S–M row. Named revisit trigger: if a degraded-cache
  restart cadence proves operationally painful in practice, that is the fire to add a bounded retry.
- **No change to the empty-`class` arm** (§1, confirmed Contract #1 §1.6-sanctioned).

## 4. Test strategy

| # | Proves | Shape |
|---|---|---|
| T1 | `Invalidate`'s two error paths set `Degraded()=true` with the failing root key + error; a healthy `Invalidate` before any failure leaves it false | Unit, `internal/processor/ddl_cache_test.go` — inject a failing `conn` (existing fake) |
| T2 | `Degraded()` never auto-clears on a later unrelated successful `Invalidate` | Unit, same file — mutation-tested: assert it STAYS true |
| T3 | Step 6.5: unresolved class + `Degraded()==true` ⇒ terminal rejection error (mirrors the fault-path wording family) | Unit, `step65_encrypt_test.go` — fake/degraded `DDLCache`. Mutation-tested: revert the check, confirm this test fails |
| T4 | Step 6.5: unresolved class + `Degraded()==false` (healthy cache) ⇒ unchanged permissive `continue` — the existing Contract #1 §1.6 tests must not regress | Unit, same file — assert existing empty-class / genuine-no-DDL cases still pass through |
| T5 | Heartbeat emits `DDLCacheDegraded` (severity error, status unhealthy) once degraded, `since` persists across ticks (the `openIssues` pattern), absent when healthy | Unit, `health_internal_test.go` — mirrors the existing `ProcessorLaneLagging` test shape |

## 5. Increment (single, posture-changing ⇒ full review depth)

1. `DDLCache`: add the degraded latch (fields + `Degraded() (bool, time.Time, string, error)`), set from both
   `Invalidate` error-return paths. **T1, T2.**
2. `step65_encrypt.go`: the degraded-aware rejection branch. **T3, T4.**
3. `health.go`: `ddlCache` field, `AttachDDLCache`, the `DDLCacheDegraded` issue in `buildHealthDoc`. **T5.**
4. `commit_path.go`: wire `hb.AttachDDLCache(ddls)` in `MakePipeline` (mirror `AttachCapabilityAuthorizer`
   two lines above). Check whether `MakeStubPipeline` also needs it (likely not — stub mode has no real
   Invalidate failures to observe, confirm at build time rather than assume).
5. Board row narrowed to name only the fixed arm; the empty-class arm's non-fix recorded as a decided
   non-goal (§3), not silently dropped.

*Green bar:* `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run
./scripts/lint-conventions.go`, `go test ./internal/processor/... ./cmd/processor/...`.
