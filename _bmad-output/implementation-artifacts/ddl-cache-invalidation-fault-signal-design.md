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

## 2. The fix, as shipped

**§2 originally speced a single global `degraded` latch and a two-way step 6.5 check (`!ok` + degraded ⇒
reject). Three cold-review rounds found that shape under-protects: it left a platform-wide write outage for
non-aspect kinds, a stale-but-present cache entry, and the `instanceOf`-chain fallback all open. What shipped
is the corrected shape below — this section describes the actual mechanism, not the original speculative one;
see the fire-brief-round build note appended at the end of this file for the finding-by-finding history.**

**New state, two pieces, both on `DDLCache`:**

| State | Created | Reset | Carried across | Ordered relative to |
|---|---|---|---|---|
| `degraded` (bool + since + err + the metaRootKey that first failed) | Zero value at `NewDDLCache` | **A later `Invalidate` never clears it** (see below) — cleared only by a subsequent **`Refresh`** that loads every root cleanly (the only way `Refresh` returns without error) | In-process only; today `Refresh` runs solely at construction, so the clearing event is a restart — a future periodic `Refresh` (§3) inherits the clear for free | Set by `Invalidate`'s error-return paths; cleared by `Refresh`'s success path. Both under the same `mu` discipline as every other index write. Read by step 6.5 and the heartbeat; never by step 6 (§3 non-goals) |
| `degradedStaleRoots map[string]string` (failed root → the canonicalName its **surviving, un-reloaded** entry still serves) | Nil until first failure | A root's entry is removed the moment **that same root's own** later `Invalidate` succeeds; unaffected by any other root's outcome | Same as above | Written under `mu` from `Invalidate`'s failure and success paths; read under `mu` from `PossiblyStale` |

**Why `degraded` needs no per-root precision, but a stale *resolved* entry does.** The global latch answers
"can an *absence* (`ok=false`) be trusted as genuinely-no-DDL" — and a missed root's canonicalName is exactly
what was never read, so no per-root bookkeeping can name which class's absence a given failure explains; only
the conservative "not fully trustworthy" claim is supportable, until a full `Refresh` re-establishes it.
`degradedStaleRoots` answers a *different* question a miss can't raise at all: `Invalidate`'s failure path
returns **before** touching `byRoot`, so an **already-cached** root's old entry survives untouched — a package
upgrade flipping that DDL's `.sensitive` false→true (Contract #8 §8.1 keeps the meta-vertex NanoID across
upgrades, so this is an update, not a fresh install) leaves `Lookup` answering `ok=true` with the stale
`false`. This one *is* nameable, because the root — and therefore its still-served canonicalName — is sitting
right there in `byRoot` at the moment `Invalidate` fails on it. Keyed by root rather than name because a name
two roots claim must not be cleared by the wrong root's success, and a root whose canonicalName changed
between the failed and the successful load must withdraw the name that was flagged, not the one it now serves.

**`step65_encrypt.go`'s `degradedCacheRefusal`, three arms, aspect-scoped.** Only an `aspectType` DDL can ever
be `Sensitive` (`internal/pkgmgr/sensitivescope.go`), so the whole check is gated on `kind ==
substrate.KindAspect` — a degraded cache must never refuse a link or vertex write, which could never have been
encrypted anyway even with a perfect cache (the original speced shape got this wrong: gating on bare `!ok`
rejected every unresolvable link too, a near-total write outage with zero security benefit). Within an aspect
mutation, while `Degraded()`:

1. **`!resolved`** (the resolver found nothing at all) ⇒ refuse — the base case §2 originally speced.
2. **The mutation's own `class` does not hit `DDLs.Lookup` exactly** ⇒ refuse, even if the resolver DID return
   `ok=true` via its `instanceOf`-chain fallback to a different, fresh ancestor DDL. A stale cache is exactly
   what makes the class's own direct entry (if newly added) invisible, forcing the walk onto a coarser
   ancestor whose `Sensitive` value says nothing about the missing one — an unspeced hole three reviews'
   worth of code-tracing found live and reproduced.
3. **`PossiblyStale(ref.CanonicalName)`** ⇒ refuse — the stale-but-present case above.

Arm 2 is deliberately broader than per-name tracking: an answer that never consulted this class's own cache
entry cannot be checked against it, so every chain-resolved aspect is refused while degraded, not just the
ones provably affected. Not degraded ⇒ the existing Contract #1 §1.6 permissive-default `continue` is
completely unchanged for every arm.

**Health (`internal/processor/health.go`):** mirrors the shipped `ProcessorLaneLagging` pattern exactly (the
`active[...] = activeIssue{...}` + `reconcileIssues`'s since-persistence). A `ddlCache` field +
`AttachDDLCache(*DDLCache)` setter (mirrors `AttachCapabilityAuthorizer`), wired from `MakePipeline` right
after `ddls := NewDDLCache(...)` — the same site `hb.AttachCapabilityAuthorizer(ca)` already wires from.
Severity **`error`** (→ `unhealthy` via `aggregateStatus`), not `warning`: this state actively rejects
legitimate aspect writes for affected submitters, a real functional degradation. Message wording states the
actual scope (aspect writes whose governing DDL the cache cannot vouch for), not the narrower "sensitive-class
writes" the original speced text used — arms 1+2 together refuse *any* aspect with no exact cache entry, not
only ones later confirmed sensitive.

## 3. Non-goals

- **No change to step 6's own permissive-default posture.** Step 6 does not encrypt; committing a mutation
  step 6 validated as "no governing DDL" plaintext is Contract #1 §1.6's designed behavior regardless of
  cache staleness (the write-scope gate, not the sensitivity gate) — step 6.5 is the only place a stale
  "no DDL" answer has a security consequence (plaintext where ciphertext was owed).
- **No retry-until-success for a failed `Invalidate`.** That is a larger mechanism (a durable retry queue or a
  periodic background `Refresh`) with its own state-lifetime questions; the latch + loud Health signal +
  fail-closed step 6.5 is the right-sized fix for an S–M row. Named revisit trigger: if a degraded-cache
  restart cadence proves operationally painful in practice, that is the fire to add a bounded retry — and per
  §2, a periodic `Refresh` would inherit the latch-clear for free, no new state-lifetime work needed on this
  mechanism's side.
- **No change to the empty-`class` arm** — RETRACTED as a "confirmed sanctioned" claim (§1); it is a real,
  separately-filed gap, out of scope for this fire because it needs a genuinely new mechanism.

## 4. Test strategy (as shipped)

| # | Proves | Shape |
|---|---|---|
| T1 | `Invalidate`'s error paths set `Degraded()=true` with the failing root key + error; a healthy `Invalidate` before any failure leaves it false | Unit, `ddl_cache_test.go` |
| T2 | `Degraded()` never auto-clears on a later unrelated successful `Invalidate` | Unit, same file — mutation-tested |
| T3 (arm 1) | Step 6.5: `!resolved` + degraded ⇒ terminal rejection | Unit, `step65_encrypt_test.go` — mutation-tested |
| T3b (arm 2) | An aspect resolved ONLY via the `instanceOf`-chain fallback to a fresh ancestor DDL is still refused while degraded — the exact hole three review rounds found live, reproduced with a positive control (same batch, healthy cache, passes and encrypts) | Unit, same file — mutation-tested |
| T3c (arm 3) | An aspect resolved `ok=true` whose canonicalName the latch flagged `PossiblyStale` is refused, even though `ref.Sensitive` reads `false` off the stale entry | Unit, same file — reproduces the real upgrade-then-failed-Invalidate path, not a map poke; mutation-tested |
| T4 | Non-aspect kinds (link, vertex) pass through completely unaffected while degraded — the aspect-scoping gate | Unit, same file — mutation-tested |
| T4b | An unresolved class with a healthy (non-degraded) cache is unchanged Contract #1 §1.6 permissive `continue` | Unit, same file |
| T5 | Heartbeat emits `DDLCacheDegraded` (severity error, status unhealthy) once degraded, `since` persists across ticks, absent when healthy or unattached | Unit, `health_internal_test.go` |
| T6 | A stale-flagged canonicalName clears when its OWN root's later `Invalidate` succeeds; a rename correctly withdraws the OLD flagged name, not the new one; two different roots failing both stay flagged independently | Unit, `ddl_cache_test.go`, three tests — all mutation-tested |
| T7 | A full `Refresh` that loads every root cleanly clears BOTH the process-wide latch and every stale-root flag | Unit, `ddl_cache_test.go` — closes the gap between the field doc's own claim and what the code did before this was added |

## 5. Increment, as built (single fire, three cold-review rounds, full review depth throughout)

The speced single pass (build → one review → ship) undersized the mechanism for a security-plane change. What
actually ran: build → **3 parallel cold reviews** (security / edge-case / acceptance-scope-diff) → fix round 1
(2 blocking: aspect-scoping, stale-but-present) → skeptical verify → fix round 2 (1 new blocking: the
`instanceOf`-chain bypass, found and reproduced by the verifier, not by the original three) → skeptical verify
→ Winston's own 5-line close (Refresh clears the latch, matching its own field doc's claim) → this doc rewrite.

1. `DDLCache`: the `degraded` latch + `degradedStaleRoots` (root→canonicalName), set from `Invalidate`'s
   failure paths, cleared respectively by `Refresh`'s and `Invalidate`'s own success paths. **T1, T2, T6, T7.**
2. `step65_encrypt.go`: `degradedCacheRefusal`, the three-arm aspect-scoped check (§2). **T3, T3b, T3c, T4, T4b.**
3. `health.go`: `ddlCache` field, `AttachDDLCache`, the `DDLCacheDegraded` issue in `buildHealthDoc`. **T5.**
4. `commit_path.go`: `hb.AttachDDLCache(ddls)` in `MakePipeline` — `MakeStubPipeline` delegates to it, so it's
   covered without separate wiring (confirmed at build time, not assumed).
5. Board row narrowed to name only the fixed arm; the empty-class arm re-filed as its own real gap (§1, §3),
   not folded into a false "sanctioned" claim.

*Green bar:* `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run
./scripts/lint-conventions.go`, `go test ./internal/processor/... ./cmd/processor/...`, `go test -race
./internal/processor/`. All green at every round; the fix rounds landed BECAUSE of review, not after breaking
a green bar — no gate ever caught these, only adversarial reading did.
