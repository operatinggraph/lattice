# The write path's live reads cost a round trip each — collapse them, then name the budget that binds

**Status: ✅ RATIFIED — Fire 1 build-ready · Fires 2–3 ratified-and-shelved** · Andrew, 2026-08-13 ·
Designer fire 2026-08-11 · Winston
**Board row:** `[Processor] A class-(e) enumeration has no budget of its own — the 250ms wall binds first`
(lattice.md, ★★★ / M) · **Blocks:** `verticals.md` — *No resident, member, patient or tenant can actually
pay their own balance* (★★★, cross-vertical, `🚧 blocked-on` this row; clears when Fire 1 ships)

**Ratification record (2026-08-13).** **Fire 1** (Inc 1 batched page read + Inc 2a batched instanceOf
read + Inc 2b resolution memo + Inc 3 blocking lint gate) is ratified **build-ready** — it alone closes
the verticals self-pay blocker. The §8.1 fork is settled as designed: keep the lister, batch the values
(the wildcard shape cannot honor paging — verified against the pinned `nats-server@v2.14.0` source).
**Fires 2–3 are ratified-and-shelved** — sound designs whose consumers are not yet pressing. Revive
Fire 2 when a deep-walk consumer's wall cost bites (the erasure seal's up-to-160-page sweep,
`MergeIdentity` / `role_has_open_tasks` at up to 64 — the quadratic re-listing term Fire 1 does not
touch). Revive Fire 3 when the named-refusal ask is picked up (the board row's original framing;
`erasure-orchestration-design.md` residual 1) — with or after Fire 2, since its budget sizing wants the
post-collapse measured page cost. **Contract disposition:** the §2.5 charge-model correction committed at
ratification with a transitional note (the lazy-read correction is true today; the batched-call sentence
lands with Fire 1 and the note dies with it); the §2.5.1 snapshot sentence was **discarded** — a
deferred-by-choice addition that rides Fire 2's revive fire and is re-staged then. DD corrections folded
pre-ratification: the CI wall-widening moved to `internal/testutil`'s `init()` (`e2f01a16`, 2026-08-09,
before this design was authored — same 20× effect, different mechanism); Fire 1's payoff e2e must pin the
production 250 ms wall explicitly or testutil's widening vacates it; census expected-count fixes.

---

## For Andrew

**What this does, in two lines.** Every live Core-KV read the Processor's Starlark path issues today is a
*sequential* round trip, and two of those paths issue one round trip **per link**. `KVGetMulti` (shipped
2026-08-10, `d8cc803c`) already collapses exactly that shape into one atomic call — measured live on the
running stack, it is **9–14× cheaper** on the dominant term. Fire 1 adopts it on both live-read paths and
unblocks self-pay across four verticals; Fires 2–3 then remove the multi-page listing term and re-denominate
the budget in the unit that actually binds.

**The row's premise did not survive grounding, and the correction is the interesting part.** The row says a
class-(e) *enumeration* has no budget and the wall binds first. Both halves are true, but the arithmetic does
not close: the live self-pay failure is ~20 enumeration hops, and at measured live rates 20 hops is ~15–20 ms,
not 250 ms. The missing term is on the **other** live-read path — `kv.Read`'s lazy fallthrough, which
Contract #2 §2.5 describes as "one GET" and which is in fact **up to four instanceOf hops, each one a prefix
list plus a GET per key, re-walked from a freshly-constructed resolver on every single read**
(`sensitive_decrypt.go:140-152` → `step6_resolve_ddl.go:222-266,67-104`). That cost is invisible in the
contract, in the census, and in the row. §3.3 has the ledger.

**One fork, and it is small (§8.1).** `KVGetMulti` accepts a NATS wildcard as a first-class filter, so the
tempting shape is to drop the key-lister entirely and serve a whole `kv.Links` call in **one** round trip.
Measured, that shape is wrong: against a 3,919-degree hub it returned **all 3,919 entries**, ignoring the
caller's `limit` of 256, because the server's 1,024 gate runs *before* the `Batch` bound
(`nats-server@v2.14.0 stream.go:5847,5907-5910`; `filestore.go:3896-3897`) and there is no other bound to
give it. **My recommendation: keep the lister, batch the values** — two round trips per page, paging contract
untouched, and a page that is now one atomic snapshot instead of N straddling reads. The wildcard form is
kept only where there is no paging contract to honour (the instanceOf resolution, §5.2).

**Two frozen-contract edits, staged UNCOMMITTED in `main`** (`docs/contracts/02-operation-envelope.md`):

1. **§2.5, "Live-read budget"** — the sentence "`kv.Read`'s lazy fallthrough (one GET)" is **factually false
   today**, before and after this design. It is the clause the whole cost model rests on. The staged edit
   corrects it and re-states the charge model Fire 1 changes.
2. **§2.5.1** — one sentence for Fire 2's per-execution key-set snapshot. §2.5.1 already says `kv.Links` is
   *not* a serialization point and that a paged enumeration may observe an add/remove between pages, so the
   snapshot **narrows** what a caller may observe rather than promising anything new. **Discarded at
   ratification** (Fire 2 shelved) — re-staged by Fire 2's revive fire.

Neither edit changes the closed error enumeration (§2.6). §7.3 explains why Fire 3 refuses through
`ScriptFailed` + `details` rather than a new code: I checked, and no engine branches on the wire code
(`grep ScriptFailed internal/weaver internal/loom` → zero hits) — the consumer of a named refusal is the
operator surface, not a dispatcher.

---

## 1. Problem + intent

Four verticals cannot let a resident, member, patient or tenant pay their own balance. Every `*-ledger`
self-credit branch recomputes the owed balance from the account's own `postedTo` transaction history rather
than trusting a payload amount — the right design, and the reason it fails closed rather than open. Live,
Café and Wellness both reject `ScriptTimeout` 3/3 over an **8-row account**, while the same operation
submitted without a self-scope target commits. CI cannot see it: every test binary linking
`internal/testutil` runs at a 5 s wall — the widening moved from Makefile/`ci.yml` env into
`internal/testutil/script_wall_budget.go`'s package `init()` on 2026-08-09 (`e2f01a16`), so the fixture
owns the budget rather than the invocation. Same 20× effect; only the wall-asserting packages
(`internal/processor`, `internal/starlarksandbox`), which do not link testutil, still run at 250 ms.

The same wall is the erasure spine's ceiling. `erasure-orchestration-design.md` §residuals-1 records that
`SealIdentityForErasureComplete` dies as `ScriptTimeout` rather than its own named
`ErasureVerificationUnreachable`, and that `TestUnbindIdentityCredentials_WideSubject_ConvergesPastOnePage`
— an **existing** sweep at 300 links — failed the wall under parallel load and passed in isolation.

Both are the same underlying fact: **the write path's live reads are sequential, one round trip each, and two
of its paths issue one per link.** The intent here is not to widen a budget or improve an error message. It is
to make the reads cost what they should, and only then to name the bound honestly.

## 2. What exists today (the pattern to extend, not replace)

- **`kv.Read`** (`starlark_kv.go:64-137`) — cache-first against the step-4 hydrated snapshot; a key not
  declared in `contextHint` falls through to a live single-key read via `sc.KVReader`.
- **`kv.Links`** (`starlark_kv.go:149-235`) — Contract #2 §2.5.1's one sanctioned relaxation of the
  known-key-reads-only write path: a bounded, paged, server-side-subject-filtered enumeration of a hub's
  links in one direction. Backed by `connLinkLister` (`:298-336`), the **only** non-test implementation of
  `ScriptLinkLister` (`script_context.go:132-134`).
- **`liveReadBudgetTracker`** (`live_read_budget.go:26-47`) — a shared-by-pointer per-execution counter,
  default 60,000, charged at each round trip. The established shape for per-execution Processor state
  (`deferredMissTracker`, `sensitiveReadTracker` sit beside it on `ScriptContext`).
- **`KVGetMulti`** (`substrate/kv_multi.go:133-194`) — the batched, atomic, multi-subject counterpart to
  `KVGet`, shipped 2026-08-10. One raw `multi_last` Direct-Get round trip computed under the stream's read
  lock. **Already adopted by step-4 hydration** (`step4_hydrate.go:252`) and `personalinterest.IsRelevant`.
  Accepts exact keys *and* NATS wildcard filters as first-class inputs.

Everything below is an extension of the last item onto the two paths step 4 already uses it on. No new
mechanism is proposed in Fire 1.

## 3. Grounding ledger

Every row cites the code that **does** the thing.

### 3.1 The enumeration path

| # | Fact | Citation |
|---|---|---|
| G1 | One `kv.Links` call = one `KVListKeysFilter` + **one sequential `KVGet` per listed key**. Never `KVGetMulti`. | `starlark_kv.go:304-336` (`:317` is the per-key GET) |
| G2 | `KVListKeysFilter` **drains the entire matched set** into memory on every call, then pages **client-side**. There is no server-side cursor: page 7 costs exactly what page 1 costs. | `substrate/kv.go:287-305`, `pageFilteredKeys` `:321-338` |
| G3 | `ListKeysFiltered` is one ephemeral **ordered consumer** — a single `$JS.API.CONSUMER.CREATE` round trip, headers-only, last-per-subject — and blocks until the whole matched set is delivered. Fixed cost small; delivery cost ∝ **matched** keys, not stream size. | `nats.go@v1.52.0 jetstream/kv.go:1432-1433,1215-1359,1305-1314,1344` |
| G4 | The budget charges the clamped **`limit`** per page, with a comment asserting "one charge unit == at most one round trip". Fire 1 makes that comment false; §5.1 says what replaces it. | `starlark_kv.go:214-224` |
| G5 | The page `limit` is applied at the **key** level, before any `isDeleted` test — tombstoned links occupy page slots. Preserved unchanged by this design. | `starlark_kv.go:226-229`, `parseLinkDoc :341-347`; independently recorded as G3 of `credential-binding-plane-lifecycle-design.md` |

### 3.2 `KVGetMulti`, and what it can and cannot be bent into

| # | Fact | Citation |
|---|---|---|
| G6 | `KVGetMulti` takes exact keys **or wildcard filters**; returns live entries keyed by bare key, in one atomic snapshot under the stream read lock. A soft-tombstoned envelope **is** included; an absent/hard-deleted key is simply missing from the map — exactly the `ErrKeyNotFound → skip` disposition `ListLinks` already implements. | `substrate/kv_multi.go:133-194,248-268` |
| G7 | The **1,024 limit is on MATCHED subjects, not on requested filters**, and it is evaluated in `MultiLastSeqs` **before** anything else bounds the response. | `nats-server@v2.14.0 server/stream.go:5809,5847`; `filestore.go:3896-3897`; 413 at `stream.go:5850-5851` |
| G8 | `JSApiMsgGetRequest` does carry a `Batch` bound — but it caps how many of the **already-resolved-and-1,024-gated** sequences are sent (`sent++; if req.Batch > 0 …`). **`Batch` therefore cannot rescue a wide hub.** | `server/jetstream_api.go:673-694`; `server/stream.go:5907-5910` |
| G9 | Over the cap, `KVGetMulti` falls back to a stability-verified ephemeral-consumer double drain — correct, and far more expensive. | `kv_multi.go:163-181,261-266` |

### 3.3 The cost the row does not name: `kv.Read`'s lazy fallthrough is not one GET

| # | Fact | Citation |
|---|---|---|
| G10 | Every lazy `kv.Read` calls `decryptSensitiveDoc`, which constructs a **fresh `&ddlResolver{}` per read** and wires it with live Core-KV readers. Nothing is memoized across the reads of one execution. | `sensitive_decrypt.go:140-152` (`:144` is the fresh construction) |
| G11 | When the doc's class is not an exact `DDLs.Lookup` hit, the resolver walks up to `maxInstanceOfHops = 4` hops, and each hop may also do a `classOf` read. | `step6_resolve_ddl.go:20,222-266` |
| G12 | Each hop's `LiveInstanceOfTargets` is **itself a list-then-sequential-get**: one `KVListKeysPrefix` over `lnk.<t>.<id>.instanceOf.` plus one `KVGet` per matched key. | `step6_resolve_ddl.go:67-104` (`:76` list, `:89` per-key GET) |
| G13 | Contract #2 §2.5 states the lazy fallthrough is "**one GET**". Per G10–G12 that is false today, independently of this design. It is the clause the whole live-read cost model rests on. | `docs/contracts/02-operation-envelope.md` §2.5 "Live-read budget" |
| G14 | The instanceOf reads **are** charged to the same live-read budget — so the budget's *number* is not lying, only its documented *composition* is. | `step6_resolve_ddl.go:72,80` |

### 3.4 The wall

| # | Fact | Citation |
|---|---|---|
| G15 | The production wall is **250 ms** (`defaultScriptWallBudgetMs`), overridable by `PROCESSOR_SCRIPT_WALL_MS`; test binaries linking `internal/testutil` are widened to 5 s by that package's `init()` (`script_wall_budget.go:30-50`, since `e2f01a16` 2026-08-09). | `starlark_runner.go:20,28-37` |
| G16 | `Budget.Wall` covers **Init + Call** — compile is excluded, but `Init` re-runs the module's whole top level on every execution. | `starlarksandbox/sandbox.go:25-29` |
| G17 | A wall breach reaches the wire as `ScriptFailed`; `ScriptTimeout` exists only as an internal detail string. The wire enumeration is closed. | `opwire/opwire.go:166`; `script_context.go:247`; `starlark_runner.go:76` |
| G18 | **No engine branches on the wire error code.** `grep -rl ScriptFailed internal/weaver internal/loom` → zero files; `grep -rn '\.Error\.Code' internal/weaver` → zero hits. Weaver's dispatch is mark-lease/anti-storm, not code-driven. | live grep, this fire |

## 4. Measurement (live, this fire — the fork input)

Read-only spike against the **running** stack (`lattice-nats` + `lattice-postgres` containers, with
`bin/{processor,refractor,weaver,loom,loupe,gateway}` all live on the host — verified by `ps`), 2026-08-11.
`core-kv` held **9,720** link keys. 12 iterations per cell; **milliseconds, mean** unless noted. The spike
issued only `KVListKeysFilter` / `KVGet` / `KVGetMulti` and wrote nothing.

| hub (degree) | A: list, 1 page @256 | B: sequential `KVGet` × page | C: **1 `KVGetMulti`**, same keys | D: 1 `KVGetMulti`, wildcard filter | E: **today**, A+B end to end |
|---|---|---|---|---|---|
| `clinicaccount` `postedTo` in (40) | 9.05 | 8.69 | **2.77** | 3.01 → 40 rows | **19.59** (max 31.2) |
| `identity` `assignedTo` in (12) | 1.91 | 3.13 | **0.43** | 0.69 → 12 rows | **5.06** |
| `role` `grantedBy` in (174) | 14.62 | 58.21 | **4.26** | 4.56 → 174 rows | **48.22** |
| `meta` `instanceOf` in (3,919) | 31.00 | 82.34 (256 keys) | **9.31** | 72.71 → **all 3,919 rows** | **135.10** (max **254.37**) |

Fixed-cost isolation: a filter matching **zero** keys lists in **0.62 ms**; one absent-key `KVGet` is
**0.16 ms**; an empty `KVGetMulti` is **0.22 ms**.

**What these numbers establish.**

1. **A single 256-key page of a single `kv.Links` call costs 135 ms mean and 254 ms max, end to end.** One
   page can exhaust the entire production wall by itself.
2. **`KVGetMulti` collapses the dominant term 9–14×** (58.2 → 4.3; 82.3 → 9.3; 8.7 → 2.8).
3. **The lister's fixed cost is sub-millisecond** (0.62 ms on an empty match) — so the lister is *not* what
   to remove. Its cost scales with **matched** keys, and it re-pays that cost on **every page** (31 ms per
   page against the 3,919-degree hub, whichever page it is). That is G2 made visible, and it is Fire 2.
4. **The wildcard variant ignores `limit`** — it returned all 3,919 rows where the caller asked for 256,
   via the 413 fallback drain (G7–G9). It cannot serve `kv.Links`' paging contract. §8.1.

**What they do NOT establish — and the Phase-0 obligation.** The census counts the self-pay branch at ~20
enumeration hops. At these rates that is ~15–20 ms, **not** 250 ms. So the enumeration path is *not* the
whole story, and I did not measure the split. Per G10–G12 the unnamed term is the nine lazy `kv.Read`s that
branch issues (one `applicationFor` link + eight transaction `.entry` aspects), each of which may re-walk up
to four instanceOf hops from a freshly-constructed resolver, each hop a **consumer-creating prefix list**.
That is a plausible multiple of the census figure and it is exactly the shape that costs milliseconds, but
it is a hypothesis until measured. **Phase 0 of Fire 1 measures the real split** (§9.1) and, if it comes back
different, the increment order in §5 is what changes — not the fixes, both of which remove real round trips
either way.

## 5. The shape

### Fire 1 — collapse the round trips on both live-read paths

**Inc 1 — `connLinkLister.ListLinks` reads its page with one `KVGetMulti`.**
Replace the `for _, key := range keys { conn.KVGet(...) }` loop (`starlark_kv.go:310-334`) with a single
`conn.KVGetMulti(ctx, bucket, keys)`, then build `LinkDoc`s from the returned map. Per page:
**1 + N round trips → 2.**

- **Semantics are unchanged, by construction.** `KVGetMulti` includes soft-tombstoned envelopes (G6), which
  is what G5 requires; a key missing from the map is precisely today's `ErrKeyNotFound → continue`; the
  cursor, the clamped `limit`, and the "limit counts tombstones" behaviour are all in the *lister*, which
  this increment does not touch.
- **It is strictly more correct.** Today's N sequential GETs can straddle a concurrent write; one
  `KVGetMulti` is one point-in-time snapshot. A set guard reading a hub's neighbours now reads a set that
  provably existed simultaneously. `KVGetMulti` — not `KVGetMultiNoSnapshot` — is required here for exactly
  the reason its own doc gives: this read set *backs a decision*.
- **Cap boundary.** `maxLinkPageLimit` is 1,024 and G7's gate is `len(seqs) > 1024`, so a maximal legal page
  sits exactly on the boundary. **Chunk the page at 512 keys** — at most one extra round trip on the largest
  legal page, and no dependence on a strict-inequality boundary in vendored server code.
- **`Revision` must survive.** `LinkDoc.Revision` comes from `entry.Revision` today; the build confirms
  `KVGetMulti`'s `*KVEntry` carries the same value off `Nats-Sequence`, with a test pinning it.

**Inc 2 — the lazy-read resolution path (G10–G12), two pieces.**

- **(a) `LiveInstanceOfTargets` reads its edges with one `KVGetMulti`** over the wildcard
  `lnk.<t>.<id>.instanceOf.>`. The wildcard is safe *here* precisely where it was unsafe in Inc 1: there is
  no paging contract to honour, and the set is structurally tiny — the resolver already refuses to resolve
  on more than one live edge (`soleTarget`, `step6_resolve_ddl.go:309-314`). One round trip replaces
  `1 + N`, and it removes a **consumer-creating list call per hop per read**.
- **(b) A per-execution governing-DDL resolution memo.** G10 is the sharper defect: the resolver is rebuilt
  per read, so an execution reading nine aspects re-walks nine times with no shared state. Memoize the walk
  on a `ddlResolutionMemo` carried on `ScriptContext` beside `LiveReads`/`SensitiveReads`, keyed on the
  **walk node** (`cur`), not on the originating read.
  - **Why the walk node and not the class or the root:** the walk is entirely determined by its current
    node. Keying on the *root* would memoize nothing useful in the self-pay case — eight transactions are
    eight distinct roots — while keying on the walk node collapses hops 2..4, which every one of those eight
    shares once they reach the type vertex. Keying on **class** would be wrong: two vertices of the same
    class can resolve differently if their instanceOf edges differ.
  - Negative results (`ok=false`) are memoized too, or a corpus of unresolvable classes re-pays every read.

**State-lifetime table for the one new stateful mechanism (Inc 2b).**

| Boundary | Rule |
|---|---|
| Created | Lazily, on the first resolution in an execution; owned by `ScriptContext`, mirroring `liveReadBudgetTracker`'s shared-by-pointer construction (`starlark_runner.go:84-93`) |
| Reset | **Never** within one execution — the whole point is intra-execution reuse |
| Carried | **Not** across executions, not across a redelivery, not across a `derive_reads` pre-pass into the main pass. One `ScriptContext`, one memo; it dies with the context |
| Ordered | Written after the batch and working-set layers have been consulted, so it caches only the *live-read* answer; the batch/working-set layers (`step6_resolve_ddl.go:279-285`) are re-consulted per call and are never memoized — they change as mutations accumulate |
| Replay | A redelivered operation builds a new `ScriptContext` and therefore a fresh memo, so the memo cannot make a replay observe a stale graph |
| Tombstone | The memo holds the *resolution*, not the document. A tombstone landing mid-execution is already outside `kv.Links`' guarantees (§2.5.1 "not a serialization point") and the resolver's fail-open-to-permissive-default behaviour is unchanged |
| Bound | Bounded by `maxInstanceOfHops × distinct roots touched`; the live-read budget already bounds the roots |
| Nil | Nil-safe = "no memoization", exactly `liveReadBudgetTracker`'s nil-receiver convention, so an unwired test harness behaves as before |

**Inc 3 — the lint gate that binds the next author.** A convention established without a gate is
fingers-crossed. Fire 1 removes every list-then-sequential-get in the Processor's live-read path, so the
migration leaves **zero debt** there and the gate ships **blocking**, not warn-first:

- **Scope:** `internal/processor/**` and `internal/substrate/**` (the platform read path this design
  cleans). *Not* the wider ~85-site corpus — that is the standing `[Perf] Convert the ~85-site
  ListKeysPrefix/list-then-get corpus to KVGetMulti` row (★★ L), whose migration is what would let this
  gate widen. §10 says so explicitly so a later fire does not re-derive it.
- **Shape — default-deny + author declares, mirroring the shipped `# read-posture: (a|c|d|e|f)` convention**
  (`scripts/lint-conventions.go:132,317,493`): a `KVListKeysPrefix`/`KVListKeysFilter` whose result feeds a
  `KVGet` in a loop is a finding unless the site carries `// kv-batch: (single|ordered|bounded-1)` naming
  why a batch is not applicable. The gate does not classify; the author declares, and forgetting fails
  closed.

### Fire 2 — the multi-page listing term (G2) *(ratified-and-shelved; revive: a deep-walk consumer's wall cost — erasure seal / MergeIdentity / role_has_open_tasks)*

After Fire 1 a page costs two round trips, but a *deep* walk still re-drains the hub's whole matched key set
on every page — 31 ms per page measured against a 3,919-degree hub, whichever page. The erasure seal pages up
to 160 times, `MergeIdentity` and `role_has_open_tasks` up to 64. That term is quadratic in pages and Fire 1
does not touch it.

**Shape: a per-execution enumeration key-set snapshot, keyed by the constructed subject filter.** The first
`kv.Links` call for a filter lists once and keeps the sorted key set on the `ScriptContext`; subsequent pages
of the same filter cursor-slice the held set and pay only their `KVGetMulti`. A P-page walk goes from
`P × (list + get)` to `1 × list + P × get`.

- **Soundness.** §2.5.1 already states `kv.Links` is *not* snapshot-isolated, is *not* a serialization
  point, that a paged enumeration **may** observe an add/remove between pages, and that a guard over the
  returned set **MUST** additionally contend a shared OCC-guarded key. The snapshot therefore **narrows**
  the observable set — it removes a permitted non-determinism rather than adding a guarantee anyone relies
  on. **Bodies are still read fresh per page**, so liveness/tombstone state is never stale.
- **State-lifetime table:** created on the first call per filter; never reset within an execution; never
  carried across executions or a redelivery; discarded with the `ScriptContext`; sized-capped (a hub whose
  key set exceeds the cap falls back to today's per-page listing rather than holding it, so memory is
  bounded by the cap, not by degree); nil-safe = today's behaviour.
- **Contract:** one sentence in §2.5.1 recording the snapshot. Staged uncommitted; **discard it if Fire 2 is
  not ratified.**

### Fire 3 — the budget that binds is the budget that is named *(ratified-and-shelved; revive: the named-refusal ask, with or after Fire 2)*

Only now are the post-fix costs stable enough to denominate a budget in them.

- **Two quantities, both named.** Keep the existing 60,000 ceiling as what it has always actually been — a
  **links-examined** ceiling, charged at the clamped `limit` (G4), which bounds *work* and is unaffected by
  batching. Add a **round-trip** counter alongside it, charged `2` per page and `1` per batched
  resolution — the quantity that is proportional to wall time. §3.3's whole failure was reading one
  quantity as if it were the other; the fix is to stop conflating them, not to re-scale one.
- **The named refusal.** A round-trip-budget breach aborts with `ScriptFailed` **plus**
  `details: {reason: "EnumerationBudgetExceeded", roundTrips, budget, filter}` — a refusal that says what
  was too wide, instead of a bare "script exceeded wall budget 250ms" that names nothing.
- **No closed-enum change (G17–G18).** A new §2.6 code would only pay off if a dispatcher branched on it;
  none does. The consumer of a named refusal is the operator surface and the `surface` gap, both of which
  read `details`. This also settles `erasure-orchestration-design.md` residual 1's stated need — the seal's
  wide-subject refusal becomes self-describing.
- **Sizing** comes from the Fire-1/2 measurements, not from a guess: set the round-trip budget so it trips
  *before* the wall at the measured per-round-trip cost, with the margin written down and re-derivable.

## 6. Reconciliation with the existing mental model

**"Didn't we already fix this with `KVGetMulti`?"** Partly — for the paths that were looked at. `d8cc803c`
adopted it in step-4 hydration and `personalinterest.IsRelevant`; the ~85-site corpus row tracks the rest.
Neither the census in that row (`adjacency-per-edge-index-design.md` §14.7: `cmd/loupe`, the four vertical
apps, the pkgmgr installer, weaver/loom state scans, the rule-engine anchor scans) nor the shipped adoption
includes `connLinkLister` or `LiveInstanceOfTargets` — the two sites on the **Starlark write path**, which
are the ones bound by a 250 ms wall. That is the gap this design closes, and §10 records the boundary so the
two do not collide.

**"Doesn't the live-read budget already bound this?"** It bounds it at 60,000 *page slots*. The self-pay
branch spends about 60 of those and dies anyway. The budget was sized against the platform's worst-case
enumeration fan-out (`live_read_budget.go:3-26`, `MergeIdentity` at ≈49,946) — a real ceiling for a real
hazard, just not the one that binds. Two ceilings, two quantities; Fire 3 stops pretending they are one.

**"Does this contradict §2.5.1?"** No. Fire 1 changes no observable semantics at all. Fire 2 narrows a
non-determinism §2.5.1 explicitly permits but nothing relies on — and §2.5.1's own "MUST additionally contend
a shared OCC-guarded key" is what makes that true.

**"Does this introduce new state, and do we keep that state somewhere already?"** Two pieces
(Inc 2b's memo, Fire 2's snapshot), both per-execution, both on `ScriptContext`, both mirroring
`deferredMissTracker` / `sensitiveReadTracker` / `liveReadBudgetTracker` — the same shared-by-pointer,
nil-safe, dies-with-the-context shape. Each has a lifetime table above.

**"Is P2/P5 respected?"** Yes, and nothing here goes near either. These are the Processor's *own* Core-KV
reads on the write path — the sanctioned location for a write-path read (P2: the Processor is the sole
writer and the sanctioned Core-KV reader). No engine gains a Core-KV read; no application reads Core KV.

## 7. Contract surface

| § | Change | Why | Affected consumers |
|---|---|---|---|
| §2.5 "Live-read budget" | **Correct a false statement** — the lazy fallthrough is not "one GET" (G10–G13) — and re-state the charge model after Fire 1 | The clause is the platform's documented cost model for the write path; it is wrong today, before this design | Every op author reasoning about read cost; the `read-posture` lint's rationale |
| §2.5.1 | One sentence for Fire 2's per-execution key-set snapshot — **discarded at ratification (Fire 2 shelved); re-staged by the revive fire** | An observable narrowing, even a permitted one, belongs in the contract | `kv.Links` authors; the Edge mirror-coverage gate (unaffected — `enumerations` metadata is untouched) |
| §2.6 | **No change** | G17–G18: no dispatcher branches on the wire code, so a new closed-enum value buys nothing | — |

The §2.5 edit **committed at ratification** (2026-08-13) with a transitional note that dies with Fire 1;
the §2.5.1 edit was discarded with Fire 2's shelving.

## 8. Alternatives considered

### 8.1 The fork: drop the lister and serve `kv.Links` from one wildcard `KVGetMulti` — **rejected, measured**

The elegant shape. `KVGetMulti` accepts `lnk.<t>.<id>.<rel>.>` and `lnk.*.*.<rel>.<t>.<id>` — exactly the two
filters `kv.Links` already builds — and returns **bodies**, so the lister and the GETs both disappear: one
round trip for a whole call.

It fails on the paging contract, and I measured the failure rather than arguing it: against the
3,919-degree hub it returned **all 3,919 rows** for a caller asking for 256, at 72.71 ms via the 413 fallback
drain. `limit` and `cursor` are simply not expressible — G8 is the reason the obvious rescue does not work,
since `Batch` is applied *after* the 1,024 gate has already refused. Serving a 3,919-degree hub as one
unbounded page would also blow the memory bound §2.5.1's paging exists to provide.

**Could a variant beat the recommendation?** Two were considered and both lose: (i) *try the wildcard, fall
back to the lister on 413* — the 413 arrives only after the server has resolved the whole matched set, so the
pathological case pays for both paths, and the cheap case is already only one round trip better than the
recommendation; (ii) *wildcard when degree is known small* — degree is not knowable without the list. The
wildcard form is therefore kept exactly where there is no paging contract and the degree is structurally
bounded: Inc 2a's instanceOf resolution.

### 8.2 Widen the wall (raise `PROCESSOR_SCRIPT_WALL_MS` in production) — rejected

It is the shape CI already has, and it is why CI cannot see any of this. NFR-P4 targets a 100 ms p99; the
250 ms budget is the headroom. Widening converts a fast refusal into a slow one and leaves the round trips
in place. The reads are 9–14× more expensive than they need to be — that is the defect, and a wider wall
hides it.

### 8.3 Give class-(e) its own wall-clock budget (the row's literal framing) — rejected as the *first* move

A separate budget changes which refusal fires, not whether the operation works. Self-pay does not need a
better error; it needs to complete. The row's ask survives as **Fire 3**, sequenced last, denominated in the
unit Fires 1–2 make stable — which is the only ordering in which its threshold can be chosen from a measured
number rather than a guess.

### 8.4 Denormalize the balance into an account aspect (a running total) — rejected

It would remove the enumeration entirely for the ledgers, but it contradicts Contract #1 §1.1 decision #2
(relationships are links, never key-lists in aspect `data`), introduces a derived total that can drift from
its transactions, and fixes exactly one of the 75 census call sites. The platform defect is that a bounded
enumeration is 14× more expensive than the substrate requires.

### 8.5 Memoize into the Starlark `state` dict rather than the `ScriptContext` — rejected

Previously rejected on the declared-read-scope design for the same reason: `state` is the script's observable
input, and putting Processor-internal caching in it makes an implementation detail script-visible and
replay-relevant. `ScriptContext` is where per-execution Processor state already lives.

## 9. Decomposition, tests, and gates

### 9.1 Fire 1 (Inc 1 + Inc 2 + Inc 3) — independently shippable, green

**Phase 0 (gate, before any edit): measure the real split.** Submit one real self-credit operation on a live
stack against a small account, with `PROCESSOR_SCRIPT_WALL_MS` at the production 250 and again widened, and
record the per-path round-trip counts. This is the §4 obligation. It is a *gate on the ordering*, not on the
fixes: if the enumeration path dominates, Inc 1 lands first; if the resolution path dominates, Inc 2 does.
Either way both ship in this fire.

**Executable censuses** (re-run mechanically at Phase 0; expected results from this fire):

```bash
grep -rn "kv\.Links(" packages/ | wc -l                    # 76 matches; 75 real calls + 1 in a comment
grep -rn "# read-posture: (e)" packages/ | wc -l           # 149, across 21 files
grep -rn "func.*ListLinks(" internal cmd | grep -v _test   # exactly 1: connLinkLister (the ScriptLinkLister interface method carries no func keyword)
grep -rn "KVGetMulti(" internal cmd | grep -v _test        # 5 sites; none on the Starlark read path
grep -rln "ScriptFailed" internal/weaver internal/loom     # 0 — G18
```

**Proofs, each owned by a named increment:**

| Test | Owner | What it pins |
|---|---|---|
| `ListLinks` issues exactly one value-read call per page (fake `Conn`, call counter) | Inc 1 | The round-trip collapse itself — mutation-verified: reverting to the per-key loop must red it |
| A page containing a soft-tombstoned link returns it with `isDeleted` set, and it occupies a page slot | Inc 1 | G5 preserved (the `credential-binding` design depends on it) |
| A key hard-deleted between list and read is skipped, not an error | Inc 1 | The `ErrKeyNotFound → continue` disposition survives the batch |
| `LinkDoc.Revision` equals the entry revision under `KVGetMulti` | Inc 1 | The `Nats-Sequence` mapping |
| A 1,024-key page is chunked and returns all 1,024 links | Inc 1 | The cap boundary (G7), which is where a strict inequality would bite |
| An execution reading N aspects that share a walk node performs one live resolution, not N | Inc 2b | The memo — mutation-verified: removing the memo must red it |
| Two vertices of the same class with different instanceOf edges resolve **differently** in one execution | Inc 2b | That the memo is keyed on the walk node, not the class — the wrong key would red only here |
| A mutation tombstoning an instanceOf link mid-execution is still honoured (batch layer consulted per call) | Inc 2b | The ordering row of the lifetime table |
| `LiveInstanceOfTargets` issues one read call for a multi-edge root, and still refuses to resolve on >1 live edge | Inc 2a | Batching did not weaken the ambiguity guard |
| The lint gate reds a newly-introduced list-then-get in `internal/processor` and greens with the annotation | Inc 3 | The gate default-denies and the declaration clears it |
| **Live e2e:** a self-credit on each of the four ledgers commits at the production 250 ms wall | Fire 1 | The payoff claim. This is the gate that closes the verticals row. **The harness must pin the wall to 250 ms explicitly** — `internal/testutil`'s `init()` widens any binary linking it to 5 s, and an e2e inheriting that widening proves nothing |

**Verification gates:** `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`, **all**
`scripts/lint-*.go`, and `go test ./internal/processor/... ./internal/substrate/...` plus a full
`go test ./...` — Inc 2b changes a resolution path shared by step 6 and step 6.5, which is a wide blast
radius through packages the fire never edits.

**Review depth:** Inc 2b is **posture-changing** (it adds per-execution state on a path that gates
sensitive-aspect decryption) and takes the full adversarial pass. Inc 1, 2a and 3 are sized by the Steward
per `agents/steward/SKILL.md` §4.

### 9.2 Fire 2 — the enumeration snapshot *(shelved; owned by the revive fire)*

Owns: the snapshot + its lifetime table's tests (created once per filter; not carried across executions; the
size cap falls back to per-page listing; bodies still read fresh per page — pinned by a test that mutates a
link's `isDeleted` between pages and asserts page 2 sees the new state while the key set is unchanged). Owns
the §2.5.1 contract sentence. **Posture-changing** — full pass.

### 9.3 Fire 3 — the named budget *(shelved; owned by the revive fire)*

Owns: the round-trip counter, the retained links-examined ceiling, the `details` payload, the §2.5 charge-model
edit, and a test that a walk exceeding the round-trip budget refuses with the named reason **before** the wall
fires (mutation-verified: removing the counter must produce a bare wall timeout instead). Sizing derives from
Fire 1/2's measured numbers and is recorded in the constant's doc comment the way
`DefaultLiveReadBudget`'s already is.

## 10. Boundary with the standing `[Perf]` corpus row

`[Perf] Convert the ~85-site ListKeysPrefix/list-then-get corpus to KVGetMulti` (★★ L) covers `cmd/loupe`,
the four vertical apps, the pkgmgr installer, weaver/loom state scans and the rule-engine anchor scans. This
design covers **only** the two sites on the Starlark write path (`connLinkLister.ListLinks`,
`LiveInstanceOfTargets`), which that census does not include and which are the only ones bound by the 250 ms
wall. The Fire-1 lint gate is scoped to `internal/processor/**` + `internal/substrate/**` for exactly that
reason; **widening it to the whole repo is the `[Perf]` row's closing act**, not a follow-on of this one.

## 11. Risks

- **The Phase-0 measurement contradicts §4's hypothesis.** Mitigated by design: both fixes remove real,
  measured round trips regardless of which dominates; only the order changes.
- **`KVGetMulti`'s 413 fallback on an unexpectedly wide page.** Cannot occur in Inc 1 — the page is exact
  keys, chunked at 512, so the matched count equals the requested count and is bounded by
  `maxLinkPageLimit`. Named because it is the one way this shape could inherit the expensive drain.
- **The memo caches a resolution that a concurrent write invalidates mid-execution.** Already the standing
  posture: `kv.Links`/`kv.Read` are explicitly live, non-serializing reads (§2.5.1), and the batch +
  working-set layers — which carry the in-flight truth — are re-consulted per call and never memoized.
- **Fire 2's snapshot hides a link created between pages.** Permitted by §2.5.1 and required to be
  irrelevant by its own "MUST contend a shared OCC-guarded key" clause. Written into the contract rather
  than left implicit.

## 12. Reflex checklist, run against this draft

Per `agents/designer/SKILL.md` §2, walked explicitly rather than recalled — every one of these was a
sentence in an earlier draft of this document.

| Reflex | Applied |
|---|---|
| Ground the reported **mechanism** before it becomes a premise | §3.3/§4 — the row's "~19 hops" does not close a 250 ms wall; the unnamed lazy-read term was found by opening the file |
| A handed-down **measurement** is a claim about a **quantity** | §4 — "60,000-unit budget" is *page slots*, not round trips, and not wall time; Fire 3 stops conflating them |
| Verify a mechanism can **BE** restricted/reshaped | §8.1 — "just serve the whole call from one wildcard multi-get" was the draft sentence; opening `stream.go`/`filestore.go` (G7–G8) killed it, and the live spike confirmed it returned 3,919 rows for a `limit` of 256 |
| A **permission envelope** is part of the mechanism | No new `$JS.API.*` verb is introduced. `multi_last` is a Direct-Get on the KV stream, already exercised in production by step-4 hydration under the Processor's own identity (`step4_hydrate.go:252`) |
| New state needs a **LIFETIME**, not a data structure | §5 Inc 2b and Fire 2 each ship an eight-row table, not the phrase "cache the resolution" |
| A **census's glob** is a premise | §9.1's censuses enumerate by declaration (`kv.Links(`, `func.*ListLinks(`) across `packages/` and `internal cmd`, and each ships as a re-runnable command with its expected count |
| A **lint gate** is never an optional follow-on | Inc 3, blocking, in the same fire, default-deny + author declares |
| A **payoff claim** is a soundness claim — trace the named consumer through every conjunct | §9.1's last row: the fire is not done until a self-credit commits **on all four ledgers at the production 250 ms wall**. The claim is not "the reads got faster" |
| Check the **other in-flight designs** | §6/§10 — `credential-binding-plane-lifecycle-design.md` (📐) cites `connLinkLister` as grounding only (its G3, preserved and pinned by a test here); the `[Perf]` row's census excludes both sites this design touches |
| A **guarantee held by accident of shape** | G5 — "the page limit counts tombstones" holds because the limit is applied at the key level in the *lister*, which Inc 1 does not touch. Pinned by a test rather than left to survive by luck |
| **Dead scaffolding** | Every increment has a live consumer today: Fire 1 → four verticals' self-pay; Fire 2 → the erasure sweeps/seal, `MergeIdentity`, `role_has_open_tasks`; Fire 3 → the operator surface and `erasure-orchestration-design.md` residual 1 |

## 13. Open questions

None. The one fork (§8.1) was resolved with a live measurement and ratified as recommended; the error-code
question (§7/§5 Fire 3) is resolved against G17–G18; the increment ordering has a Phase-0 gate that cannot
change the increments themselves. Ratified 2026-08-13 — Fire 1 build-ready, Fires 2–3 shelved with named
revive triggers (see the status banner).

### Fire 1 fire brief (build note, 2026-08-13)

**Scope sentence (verbatim, §5 Fire 1):** Inc 1 batched page read + Inc 2a batched instanceOf read + Inc 2b
resolution memo + Inc 3 blocking lint gate — collapse the round trips on both live-read paths
(`connLinkLister.ListLinks`, `LiveInstanceOfTargets`) with `KVGetMulti`.

**Verified touch-list (re-checked live, three read-only haiku scouts, this fire — all citations hold, zero
drift):**
- `internal/processor/starlark_kv.go:310-334` — the per-key `KVGet` loop (`:317`), Inc 1's target.
- `internal/processor/starlark_kv.go:214-221` — the "one charge unit == at most one round trip" comment to
  update.
- `internal/processor/starlark_kv.go:250` — `maxLinkPageLimit = 1024`.
- `internal/processor/starlark_kv.go:338-347` — `parseLinkDoc`.
- `internal/substrate/kv_multi.go:192-194` — `func (c *Conn) KVGetMulti(ctx, bucket string, keys []string) (map[string]*KVEntry, error)`.
- `internal/processor/step6_resolve_ddl.go:67-109` — `LiveInstanceOfTargets`: `:76` prefix list, `:80` charge,
  `:84-89` per-key GET loop. Inc 2a's target.
- `internal/processor/step6_resolve_ddl.go:309-314` — `soleTarget`, the ambiguity guard Inc 2a must not weaken.
- `internal/processor/step6_resolve_ddl.go:279-301` — batch → working-set → live fallback order Inc 2b must
  preserve.
- `internal/processor/step6_resolve_ddl.go:20,234` — `maxInstanceOfHops = 4`, the hop loop Inc 2b memoizes.
- `internal/processor/sensitive_decrypt.go:144` — `resolver := &ddlResolver{DDLs: ddls}`, the fresh-per-read
  construction Inc 2b's memo must sit beside, not replace.
- `internal/processor/script_context.go:64,85,91` — `DeferredMiss`/`SensitiveReads`/`LiveReads` field
  declarations, the shape Inc 2b's `ddlResolutionMemo` field mirrors.
- `internal/processor/starlark_runner.go:84-93`, `internal/processor/live_read_budget.go:31-47` — the
  nil-safe, shared-by-pointer construction pattern.
- `scripts/lint-conventions.go` — `checkReadPosture` (regex-based `kvCall`/`readPosture` matchers,
  `annotationSpans()` default-deny coverage resolution) is the shape Inc 3's gate mirrors; `postureScoped`
  (scoped to `packages/`) is the directory-scoping precedent for scoping Inc 3 to
  `internal/processor/**`+`internal/substrate/**`. Tests live in-file (`selfTest()`), no separate `_test.go`.
- Inc 3 census (non-test, in scope): `internal/processor/step6_resolve_ddl.go:76` (`KVListKeysPrefix`),
  `internal/processor/starlark_kv.go:305` (`KVListKeysFilter`) — exactly 2 list-then-get sites exist today,
  both already fixed by Inc 1/2a, so Inc 3 ships with the annotation already in place at both (or the sites
  restructured such that no bare list-then-loop pattern remains).

**Precedents mirrored:** `step4_hydrate.go:252`'s `KVGetMulti` adoption (Inc 1, 2a); `deferredMissTracker` /
`liveReadBudgetTracker`'s nil-safe shared-by-pointer `ScriptContext` field shape (Inc 2b); `checkReadPosture`'s
default-deny + author-declares regex-annotation shape (Inc 3).

**Census re-run live, this fire (all match the design's stated counts exactly):**
```
grep -rn "kv\.Links(" packages/ | wc -l                    → 76
grep -rn "# read-posture: (e)" packages/ | wc -l            → 149 (21 files)
grep -rn "func.*ListLinks(" internal cmd | grep -v _test    → 1 (connLinkLister, starlark_kv.go:304)
grep -rn "KVGetMulti(" internal cmd | grep -v _test         → 5 sites, none on the Starlark read path
grep -rl "ScriptFailed" internal/weaver internal/loom       → 0
```

**Increment order + green checks:**
1. Inc 1 — `go test ./internal/processor/... -run ListLinks` (new call-counter test); mutation-verify by
   reverting.
2. Inc 2a — `go test ./internal/processor/... -run LiveInstanceOfTargets`.
3. Inc 2b — `go test ./internal/processor/... -run ResolutionMemo`; mutation-verify; full adversarial pass
   (posture-changing per §9.1).
4. Inc 3 — `go run ./scripts/lint-conventions.go` reds a planted list-then-get, greens with annotation.
5. Fire close — `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`, all
   `scripts/lint-*.go`, `go test ./internal/processor/... ./internal/substrate/...`, full `go test ./...`.
   Live e2e (self-credit on all four ledgers at the production 250ms wall) run against a stack this fire
   brings up, since none was running at fire start.

**In-scope gotchas:** Phase-0 measurement (§9.1) is a gate on *ordering* only per the design — both fixes
ship regardless of which dominates, so this fire builds Inc 1 then Inc 2 without blocking on a separate
profiling pass. `internal/testutil`'s `init()` widens the wall to 5s for any binary linking it (`e2f01a16`) —
the live e2e must pin `PROCESSOR_SCRIPT_WALL_MS=250` explicitly or it proves nothing. No stack was running at
fire start; bring one up from the main checkout only (never a worktree), reuse for verification, leave running.

**Adjacent finds:** none yet — census matched exactly, no drift found by the scouts.

**Non-goals:** Fire 2 (multi-page listing snapshot) and Fire 3 (named round-trip budget) are ratified-and-
shelved — not this fire. The ~85-site `[Perf]` corpus row (`cmd/loupe`, vertical apps, pkgmgr installer,
weaver/loom scans, rule-engine scans) is untouched; Inc 3's gate is scoped to
`internal/processor/**`+`internal/substrate/**` only.
