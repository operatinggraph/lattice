# perEntry unchanged-entry withholding — a read-grant producer writes only the entries an event changed

**Status: ✅ RATIFIED (Winston-adjudicated, per the 2026-08-20 delegation) — 2026-09-04.** No architectural fork, no
frozen-contract change (§0.1). **§12 adversarial passes ✅ RUN, both, and all findings FOLDED** — pass 1 (14 findings:
3 blocking, 4 major, 7 minor) deleted the first draft's lock; pass 2 (6 attacks on the replacement argument) broke
its presence premise and produced §4.4's two refusals and one filed adjacency bug. No pre-build gate is open — **the
Steward may build this now.** Author: Winston (Designer fire, 2026-09-04).
**Component:** Refractor — `internal/refractor/{pipeline,adapter,ruleengine,consumer}`, `health/healthwire`.
**Backlog row:** [lattice.md](../planning-artifacts/backlog/lattice.md) → *Read-model / projection maturity* →
*[Refractor/Pkgmgr] A generated read-grant producer commits its whole target per event* (★★★).
**Filed by:** [personal-lens-delta-publication-design.md](personal-lens-delta-publication-design.md) §1.2 H4 (the
audit-share attribution that moved the harm off the personal republish and onto this producer).
**Extends:** [cap-read-per-anchor-grant-keys-design.md](cap-read-per-anchor-grant-keys-design.md) §4.2 (the per-actor
prefix diff — this design is its missing fourth arm) and [capability-projection-reconciliation-design.md](capability-projection-reconciliation-design.md)
§3.1 (`Reproject`'s zero-write convergence — the predicate this design reuses).
**Frozen-contract change: NONE** (§7). **Architectural fork: NONE** (§0.1). **Size: S–M** (§11).

---

## 0. For Andrew (one-look block)

**What it does (two lines).** A `cap-read.<domain>.<actor>.<anchor>` producer re-evaluates every actor an event reaches
and then rewrites *every* entry of every one of those actors — the one grant that changed and the thousands that did
not — because the guarded adapter deliberately never skips an identical row. This design keeps the evaluation, the
prefix-diff retraction and the guard exactly as they are, and **withholds the write of a fresh entry whose stored body
already equals it** — the verdict `Reproject` has skipped on since Fire 1b. No lock, no new durable state, no wire
change, no contract change. The guard's watermark still orders every *fresh-view* writer (§4.4 proves it); the one
thing the rewrite was doing beyond that — fencing a stale-view `Reproject` on a busy actor — becomes two explicit
refusals in `Reproject`'s delete arm (§4.4, §5 tabulates every case).

**The measured harm (§1.2, live 2026-09-04 13:16–13:25 PT).** `REFRACTOR_AUDIT` is 56 minutes deep at its 512 MiB
cap; 74 % of it is one lens, `edgeManifestStaffReadGrants`. A 20-second live sample carried 7,943 audit entries — 397
per second, every one an `upsert` — from **eight** triggering CDC entities; one `providedTo` link create alone
committed 4,993 entries against a domain that holds 5,938. The `capability-kv` stream carries 19,849 live subjects and
**213.6 million deleted messages**. Not one of those writes changed a grant.

### 0.1 Fork / contract check — honest answer: neither

- **No fork.** No Gateway, read-path-auth (D1), Vault, multi-cell or HA-NATS surface changes; the D1 read gate
  (`capabilityread.IsReadable`, presence and `isDeleted` only — §2 row 9) reads what it reads today. The ordering
  argument is per key and per writer, so it holds on a multi-instance Refractor; the presence refusals read a
  per-process adjacency cursor, which under-states on a second instance and therefore refuses more — the safe direction.
- **No contract change.** The §6.2 guard is untouched in the adapter (its pin
  `TestNatsKVAdapter_Guarded_IdenticalRowStillAdvancesWatermark` stays); §6.14's two per-entry obligations hold
  verbatim (a tombstone is never withheld; the retry stays an actor re-evaluation). The one sentence a reader could
  test against the new steady state — §6.3's *"Same input → same value across replay/rebuild"* for `projectedAt` — is
  quoted in §7 with the accepted, reader-less lag it now has.
- **Winston-adjudicated** under the 2026-08-20 split; §12's pass is run and folded.

**The one call that deserves your eye, not a fork:** a withheld write no longer advances the entry's watermark, and
two cold passes established exactly what that watermark was doing. On the **ordering** axis nothing is lost: every
writer captures its token before it evaluates, so an older evaluation can never carry a higher token than the write
it races (§4.4, proven and pinned). On the **presence** axis the unconditional rewrite was an *incidental fence*: the
executor reads edges from the `refractor-adjacency` index, not Core KV, and a `Reproject` whose index view lagged
could tombstone a live entry — declined today only because a busy actor's watermark sat near the head. The design
replaces that accident with two explicit refusals in `Reproject`'s delete arm (the index must have applied the token's
sequence; no rebuild may have begun since capture), and names the two residuals it cannot close — a lost retraction
and an unguarded adjacency removal — both pre-existing, both the sweep's, the second filed as its own row (§13).

---

## 1. Problem

### 1.1 The row's claim, corrected

The row says *"plain auth-plane … 4,915 rows for ONE appointment event … ~245 rows/s … 87 % of `REFRACTOR_AUDIT`"*.
Three corrections from the code and the live stack, none of which shrinks the harm:

- It is not a plain-arm lens. `edgeManifestStaffReadGrants` is an **actorAggregate perEntry** lens
  (`ProjectionKind: "actorAggregate"`, `EntryKeyColumn: "anchorId"`, `pkgmgr/anchorwalk.go:501-520`), auth-plane,
  guarded, with an `ActorEnumerator`. Its event path is the fan-out (`evaluateFanOut` / `evaluateAspectFanOut` /
  `evaluateLinkFanOut` → `reprojectActors`, `evaluate.go:1218-1320`) or, for the actor's own vertex, the default arm
  (`evaluate.go:318`) — never the plain arm's corpus rescan. The `WITH`-scope row (§6) narrows *which actors* the
  fan-out evaluates; this row is about *what each evaluation writes*.
- The rate is **~400 entries/s**, not 245, and the share **74 %**, not 87 % — the remainder moved to `edgeCatalog`
  (18 %), the personal lens the in-flight delta-publication design is scoping. The count per event is the whole
  domain: 4,993 of 5,938 staff entries for one `providedTo` link create (§1.2 C2).
- The audit trail is the *symptom*. The mechanism is the write loop (`results.go:158-283`) handing every fresh entry to
  `NatsKVAdapter.UpsertWithOutcome`, whose guarded arm **must** commit an identical row to advance the watermark
  (`natskv.go:349-361`), and `writeAudit` then records every committed row. Three costs ride on one write: the
  audit entry, the KV revision (a stream append plus a delete under `History: 1`), and the pipelined publish itself.

### 1.2 The measured harm (live stack, 2026-09-04 13:16–13:25 PT; Refractor pid 53977)

| # | Command | Result |
|---|---|---|
| C1 | `nats stream info REFRACTOR_AUDIT` | Messages 1,818,265 · Bytes 512 MiB (AT the cap) · First `2026-09-04 12:19:58` → Last `13:16:06` — **56 min of trail** against a 7-day `MaxAge` · 3 subjects · 0 consumers |
| C1b | `nats stream subjects REFRACTOR_AUDIT` + `kv get core-kv vtx.meta.<id>.canonicalName` | `TRiLM4J9qRWHBcRbTRiL` **edgeManifestStaffReadGrants 1,347,527 (74 %)** · `ZvZ3EmZvEvM6X9XPZvZ3` edgeCatalog 320,967 (18 %, personal — not this row) · `RKF4MBBKaEEPktjxRKF4` edgeManifestReadGrants 149,994 (8 %) |
| C2 | `nats sub 'lattice.refractor.audit.>' --raw` for 20 s | **7,943 entries, 100 % `upsert`, 8 distinct `entityId`** · `lnk.service.xLRdgRrPkWjgUxaLrNys.providedTo.identity.FZJzSE5MdsKpm3eUTi2F` → 4,993 · two `instanceOf` links → 782 each · `lnk.service.VTPhqCAnmKTXmfAb9MYE.providedTo.identity.LQ28Dp37vajbdTerZvij` → 334 |
| C3 | `nats kv ls capability-kv` (19,639 keys) bucketed by `(domain, actor)` | **edgeManifestStaff: 166 actors / 5,938 entries / max 101 per actor** · edgeManifest: 61 / 12,641 / max 3,644 · edgeManifestProvider: 3 / 81 / 55 · base `cap-read.identity.*`: 227 keys |
| C4 | `nats stream info KV_capability-kv` | Subjects 19,849 · Bytes 6.6 MiB · First seq 1,132,185 @ 2026-07-28 · Last seq 214,776,191 · **Deleted 213,624,158** |
| C5 | `tail -300000 refractor.log \| grep TRiLM4J9… \| grep processed` per minute | 20–21 events/min, steady 13:18–13:22 |

Reading C2 against C3: one link create reached (essentially) every staff actor — 4,993 of 5,938 entries, 84 % of the
domain — and rewrote each. C5 × C2: ~20 events/min at ~400 entries/s ⇒ ~1,200 committed entries per event on average,
each a guarded KV Put, a stream delete and an audit publish. C4 is the accumulated cost of the same loop since July.

**What is not the harm.** The evaluation is not (the delta design measured `edgeInstances` at 0.24 s/event; the
staff producer's fan-out breadth is the `WITH`-scope row's subject). The D1 reader is not (`IsReadable` is an exact
filtered listing, unchanged). The retraction is not (`multiEntryRetractions` already skips a tombstoned candidate
without a rewrite, `evaluate.go:941-950`). The harm is the **fresh-entry arm of the prefix diff having no
"unchanged" verdict**.

### 1.3 Intent

The per-anchor design's §4.2 named three arms for a perEntry actor's write — *tombstone dropped, upsert fresh, skip
already-tombstoned* — and the reconciliation design gave `Reproject` a fourth, *skip converged* ("a converged actor
costs zero KV writes", `reproject.go:395-398`). The CDC write loop never got it. This design gives the CDC loop the
same fourth arm and shows why nothing else is needed.

---

## 2. Grounding ledger (verified in code this fire)

| # | Fact | Where | Bearing |
|---|---|---|---|
| 1 | A fan-out event evaluates every reached actor whole (`executeFullForActor`, never seeded) and returns the concatenation; an event on the actor's own vertex reaches the same evaluation through the default arm | `evaluate.go:1218-1320` (`reprojectActors`, `:1310` "Never seeded"); `evaluate.go:318` | the write set per event = Σ actors' whole entry sets, on both paths |
| 2 | Each actor row explodes into one `EvalResult` per real entry; every entry inherits the row's provenance | `evaluate.go:829-850`; `projection/driver.go:176-260` (`EntryEnvelopeFn`) | provenance cannot narrow below the actor (§8 row 2) |
| 3 | Fresh entry body = entry fields (`anchorType`, `via`) + `key`, `actor` (`d.ActorField`), `version`, `projectedAt`; the guard adds exactly `projectionSeq` on an upsert and `{isDeleted, projectedAt, projectionSeq}` on a tombstone; `projectedAt` is the **actor** vertex's `lastModifiedAt` | `driver.go:222-246`; `natskv.go:632-646` (`guardedBody`); `evaluate.go:770-778` (`projectedAtFromProvenance(nodeProps)`) | stored-minus-`projectionSeq` and fresh are comparable field-for-field; a neighbour event leaves every entry body byte-identical |
| 4 | `multiEntryRetractions` lists the actor's children by prefix, reads the legacy parent, tombstones what the fresh set dropped, **skips an already-tombstoned candidate without a rewrite**; it runs inside `executeFullForActorOnce`, i.e. on every path that evaluates a perEntry actor | `evaluate.go:919-1010`, `:928-934` | the seam: it already holds the stored key set; it lacks the stored bodies |
| 5 | The write loop writes every non-withheld result; `committed` is read off `UpsertOutcome.Committed`; audit + freshness advance only for committed rows after the flush | `results.go:158-311` | withhold ⇒ no write, no audit, no freshness mark — the same three the personal scope already skips (`:160-165`) |
| 6 | The guarded NATS-KV arm **never** skips an identical row — "must never gain a row-content skip the way the unguarded path below has" — and reports `Committed` on every landed write; the unguarded arm does a read-before-write skip | `natskv.go:341-402`; pin `natskv_test.go:367-375` | the adapter is the wrong owner for the skip (§8 row 3); its reason is correct *for the adapter* and is re-derived one level up in §4.4 |
| 7 | `Reproject` reads each stored row back and **skips the write on `divergenceNone`** = `rowsEquivalent` (canonical JSON modulo `projectedAt`); `GetRow` strips `projectionSeq` and reports a tombstone as absent; `classifyDivergence`'s second arm ignores `projectedFromRevisions`, a field perEntry bodies deliberately do not carry | `reproject.go:355-420`, `:575-590`, `:722-760`; `natskv.go:747-780`; `driver.go:165-168` | the predicate is `rowsEquivalent`, pinned and trusted on this plane; the provenance arm is dead here and is not used |
| 8 | `Reproject`'s ordering token is `Progress().LastAppliedSeq` **captured before evaluation**; the pipeline records an applied seq only after `handle` returns Ack; a CDC event's token is its own stream sequence, and the CDC stream *is* the Core KV stream | `reproject.go:436-448` (and its doc: "Any CDC event not yet reflected in the read carries a strictly greater stream sequence"); `dispatch.go:29-35`, `pipeline.go:722-727`; `results.go:152-156` | the lemma's second premise (§4.4) |
| 9 | The D1 reader consults presence and `isDeleted` only; `projectedAt` on a `cap-read` entry has no reader (the Processor's `projectedAt` readers are on the doc-mode `cap.<actor>`: `step3_auth_trace.go:79`, `step3_denial_response.go:45`) | `capabilityread.go:117-230`; `grep -rn projectedAt internal/processor internal/refractor/capabilityread` | a withheld entry's lagging `projectedAt` is unobservable; the sweep already classifies the field as non-evidence (row 7) |
| 10 | A perEntry transient write failure **never** takes the raw-replay path — it re-evaluates the actor through `Reproject` | `results.go:236-256` (`transientActorRetry`), `:478-540` | the adapter comment's replay hazard is structurally unreachable for the governed family |
| 11 | Every adapter write site in the pipeline — **18 hits** — by writer: the CDC loop (`results.go:173,176,180,190`), `replayWrite` (`results.go:401,410` — row 10's unreachable path), `Reproject` (`reproject.go:521,548,599,649`), `Pipeline.Delete` = the `control.RowNullifier` (`pipeline.go:1667,1677`) and `DeleteAllForActor` (`pipeline.go:1795,1801`) — both called by the shred at `math.MaxInt64` (`keyshredded/manager.go:360-363`), `Hydrate` and `ReprojectPersonalActor` (`hydrate.go:135,137`; `reproject_personal.go:272,287` — personal). `Truncate` (`grantchange.go:302`) is a prefix purge, not a row write | `grep -n "UpsertWithOutcome\|DeleteWithOutcome\|adpt\.Upsert(\|adpt\.Delete(" internal/refractor/pipeline/*.go \| grep -v _test` ⇒ 18 | §5 walks every writer that can touch a perEntry key |
| 12 | The lens consumer runs **one** pump worker (`Workers` unset ⇒ `workerCount` = 1) with `AckWait` 5 min; CDC handling is sequential per lens per instance | `cmd/refractor/main.go:2404-2418`; `substrate/consumer_supervisor.go:75-80` | not a premise of §4.4 (which holds per key) — cited because the first draft leaned on it |
| 13 | A Secure decryptor is installed on any lens declaring `SecureColumns`, not shape-gated, and runs **after** `executeFullForActorOnce` | `cmd/refractor/main.go:1860-1870`; `evaluate.go:88`, `dispatch.go:210,353` | arming conjunct (iii): the compare must see the same shape the store holds |
| 14 | `KVGetMultiNoSnapshot` reads exact keys in `ceil(N/1024)` fast-path requests, each bounded by `directGetMultiDefaultTimeout`, no drain; the adapter exposes no multi-row read today | `substrate/kv_multi.go:260-330`; `grep GetMulti internal/refractor/adapter/` ⇒ none | the one new adapter seam (§4.2), with its bound named |
| 15 | `EvalResult` carries `Delete / Keys / Row / ProjectionSeq / FailClosed / Provenance` | `ruleengine/eval_result.go:16-45` | one new field |
| 16 | `healthwire.Entry` publishes per-lens counters (`PeakBindingRows`, `ErrorCount`); a new field must be carried forward or the completeness test fails by name. `lastProjectedAt` feeds `LensProjectionStalled` **only for non-auth-plane lenses** — the `CapabilityLensProvider` path owns auth-plane liveness | `healthwire.go:102-145`; `health/entry_carry_forward_completeness_test.go`; `health/lattice_heartbeater.go:494-497` | the withheld counter's surface and its gate; a frozen `lastProjectedAt` on a perEntry lens trips no stall alarm (every perEntry lens is auth-plane by construction) |
| 17 | The divergence auditor refuses actor-aggregate lenses at plan time ("its rows are the convergence sweep's to verify, not the audit's") | `audit.go:951-957` | the auditor never reaches a perEntry evaluation; the read is still gated on the costed path (§4.2) so a later widening cannot make it pay |
| 18 | The perEntry family is exactly four live lenses: the kernel `capabilityRead` base and one generated producer per declared `ReadGrantDomain`; one package declares three | `grep -rn --include='*.go' "EntryKeyColumn:" internal packages` ⇒ `bootstrap/lenses.go:227`, `pkgmgr/anchorwalk.go:517`, `projection/output.go:186` (the parser); `grep -rln ReadGrantDomains packages/*/package.go` ⇒ `edge-manifest`; C3's four prefixes | the governed set, derived from the declaration and confirmed live |
| 19 | The executor's **node** reads are live Core KV; its **edge** reads are the `refractor-adjacency` index (`fetchEdges` → `adjacency.Neighbors[Scoped]`), which only an overflow-marked node bypasses ("commit-fresh, which the document is not"). The index has its own durable (`refractor-adjacency`) with **no exposed applied cursor**; the CDC link arm pre-applies only the *triggering* link before enumerating; `Reproject` pre-applies nothing | `full/executor.go:1066`, `:1440-1478`; `adjacency/store.go:184-192`; `consumer/bootstrap.go:60-77`, `:165-185`; `evaluate.go:1128-1160`; `reproject.go:445` | an evaluation's **presence** verdict can lag Core KV; the ordering token cannot (§4.4) |
| 20 | `adjacency.upsertEdge` removes by `EdgeID` with **no ordering guard**, and a link key is its own `EdgeID` (deterministic under Contract #1, so a revoke→re-grant reuses it); a Nak'd adjacency message is redelivered | `adjacency/builder.go:150-162`; `consumer/bootstrap.go:179-183` | a redelivered older tombstone can delete a live edge — a pre-existing index bug this design inherits and files (§13); the CDC path pays it today exactly as `Reproject` would |
| 21 | The sweep refuses a tick while `RebuildInFlight()`; a `Reproject` already running when a rebuild begins is not interrupted; there is no rebuild generation counter; `LastAppliedSeq` is **assigned** on Ack (a redelivered older message moves it backwards) and reads 0 after a restart, which makes every `Reproject` write drop fail-closed | `sweep.go:496`; `rebuild.go:140-175`; `pipeline.go:722-727`; `reproject.go:513, 587` | §4.4's second refusal; two existing behaviours the design leans on, cited so nobody "fixes" them under it |
| 22 | A rebuild (and a fresh activation) replays `DeliverLastPerSubject`, ascending; an actor is fanned out to once per replayed subject in its walk | `cmd/refractor/main.go:2413`; `rebuild.go:329-343` (`resolveTruncate` forces truncate on a guarded `Truncater`) | under withholding the entry's watermark after a replay is the **first** replayed write's sequence, not the last — §4.4's premise 1 is stated accordingly |

**Parallel-design check.** The dirty tree at fire start was clean. The in-flight delta-publication design (🏗️, T7
pending) built row provenance and deliberately left both plain-lens `writeResults` sites at `ScopeAll` (§4.2 there);
this design does not touch the personal path, the scope, or the healer, and its seam (`multiEntryRetractions`) is one
the other design never opens. The `WITH`-scope row (`varlength-anchor-derivation-design.md` §13 Inc 2, 📋) narrows
the *actor set* of the same producers; the two compose and land in either order (§8 row 1). The `capabilityEphemeral`
`$now` row is doc-mode. Nothing hands work to this design; this design hands the delta design's H4 row its close.

---

## 3. Executable censuses (commands + expected results; the build's Phase 0 re-runs them)

```sh
# X1 — the governed family: perEntry declarations + live prefixes (expect: 3 sites, of which 2 declare and 1 parses; 4 prefixes)
grep -rn --include='*.go' "EntryKeyColumn:" internal packages | grep -v _test
nats --server=localhost:4222 --nkey=deploy/nkeys/lattice.nk kv ls capability-kv | grep '^cap-read\.' | cut -d. -f1-2 | sort -u
#   ⇒ bootstrap/lenses.go:227 · pkgmgr/anchorwalk.go:517 · projection/output.go:186
#   ⇒ cap-read.edgeManifest · cap-read.edgeManifestProvider · cap-read.edgeManifestStaff · cap-read.identity

# X2 — the raw-replay path is unreachable for perEntry (expect: the multiEnvelopeFn branch precedes retryResults)
sed -n 236,256p internal/refractor/pipeline/results.go

# X3 — every adapter write site in the pipeline (expect: 18, classified as §2 row 11; a 19th is a new writer §5 must place)
grep -n "UpsertWithOutcome\|DeleteWithOutcome\|adpt\.Upsert(\|adpt\.Delete(" internal/refractor/pipeline/*.go | grep -v _test | wc -l

# X4 — the guard's no-content-skip pin exists and must stay (expect: 1)
grep -c "func TestNatsKVAdapter_Guarded_IdenticalRowStillAdvancesWatermark" internal/refractor/adapter/natskv_test.go

# X5 — the guard stamps exactly projectionSeq on an upsert (expect: the guardedBody upsert arm adds one field)
sed -n 632,646p internal/refractor/adapter/natskv.go

# X6 — live before/after: audit share + rate + bucket churn (run before the fire and 30 min after deploy)
nats --server=localhost:4222 --nkey=deploy/nkeys/lattice.nk stream subjects REFRACTOR_AUDIT
( nats --server=localhost:4222 --nkey=deploy/nkeys/lattice.nk sub 'lattice.refractor.audit.>' --raw --count=1000000 > /tmp/a.txt & P=$!; sleep 20; kill $P ); wc -l /tmp/a.txt
nats --server=localhost:4222 --nkey=deploy/nkeys/lattice.nk stream info KV_capability-kv | grep -E "Last Sequence|Deleted"
#   before (13:16–13:25 PT): staff 1,347,527 (74 %) · 7,943 / 20 s · Deleted 213,624,158
#   after (T9's bar): staff share of NEW entries ≤ 5 % · < 100 / 20 s at the same event rate · Deleted growth ≤ 5 % of the before rate
```

---

## 4. The shape

One rule, stated once:

> **A perEntry fresh entry is written iff its stored body differs from the fresh one; a tombstone is always
> written.** "Differs" is `Reproject`'s own verdict: `!rowsEquivalent(stored, fresh)` — the canonical JSON of the two
> bodies modulo `projectedAt`, the stored side already stripped of the guard's `projectionSeq` (§2 row 7). An entry
> that is absent, tombstoned, unreadable or unparseable in the store is "different". Withholding applies only while
> **armed** (§4.5); disarmed, every entry is written — byte-identical to today.

### 4.1 The predicate and its carrier

`ruleengine.EvalResult` gains `Unchanged bool` — *"the target already holds this body; the write loop skips it"*. Set
only by `multiEntryRetractions`; never on a `Delete`, never on a `FailClosed` result, never by an engine. Its zero
value reproduces today (write), so a path that forgets it over-writes, never withholds — the fail direction the
personal scope's zero value takes.

### 4.2 The read (adapter — Inc 1)

`multiEntryRetractions` (`evaluate.go:919-1010`) already lists `existing` (the actor's live and tombstoned child
keys) and builds `freshKeys`. When the evaluation is a **costed** one (`recordCost`, the write path — §2 row 17) and
the pipeline is armed, it gains one batched read of **`existing ∩ freshKeys`** through a new adapter seam:

```go
// RowsReader reads back several rows at once. An absent, tombstoned or
// unparseable key is simply missing from the result — per member, never
// failing the batch (dossier: a corrupt member fails only where it is used).
type RowsReader interface {
    GetRows(ctx context.Context, keys []string) (map[string]map[string]any, error)
}
```

`NatsKVAdapter.GetRows` = `KVGetMultiNoSnapshot(bucket, keys)` over exact keys, then per entry the shaping `GetRow`
does (`isDeleted` ⇒ absent; strip `projectionSeq`). **Bound:** `ceil(N/1024)` fast-path requests, each bounded by
`directGetMultiDefaultTimeout`, no drain (§2 row 14); N ≤ the actor's entry count (C3: max 3,644 on the widest
base-domain actor ⇒ 4 requests). No snapshot is needed: each key is compared independently and §4.4 is what makes the
comparison safe, not simultaneity. **Failure:** a batch error marks nothing (every entry is written — today), logs
once per actor at Warn and increments `WithholdReadFailures` (§4.6) — a rate, never an error latch (dossier: making a
fault standing makes its severity permanent). The request set is `existing ∩ freshKeys`, both already scoped to the
actor's own prefix by `actorDeleteKeyFor`, so no sibling lens's key is ever requested (T1 pins it).

Then, per fresh result whose key the read resolved: `if rowsEquivalent(stored, fresh.Row) { fresh.Unchanged = true }`.
`rowsComparable == false` (unrenderable) ⇒ written, as `Reproject` does on a repair path.

### 4.3 The write loop (pipeline — Inc 1)

`writeResults` (`results.go:158`): immediately after the personal `admits` check, `if result.Unchanged { withheld++;
continue }` — not written, not audited, not counted by `recordProjectionWrite`, never entering `retryResults` /
`terminalErrs`. **The freshness clock DOES advance (amended at build, 2026-09-05, review finding):** a pass that
withheld ≥ 1 entry stamps `lastProjectedAt` once after the flush, as a landed row would — the pass processed the
event and confirmed the entry current, and `lastProjectedAt` is the "is this lens still projecting" clock, so a
converged lens must not read as a stalled one to `LensProjectionStalled` should a business perEntry lens ever arm
(§2 row 16's exclusion is a fact about today's corpus, not a guarantee). Audit and the write count stay silent. The `pipeline: processed` line gains `entriesWithheld=N`. Tombstones
(`Delete`) and `FailClosed` results are never `Unchanged` (§4.1), so §6.14's tombstones-first ordering and the
FailClosed abort are untouched.

`Reproject` (`reproject.go:575-590`): a result carrying `Unchanged` folds as `VerdictConverged` without its own
`GetRow` — the same predicate on the same stored row. Its per-result read-and-compare stays for results the batch did
not resolve (a read failure marks nothing, so `Reproject` degrades to today's per-row path).

`Rebuild` / `Truncate`: for the NATS-KV family (the only guarded `Truncater`, and the only family arming conjunct (ii)
admits) `resolveTruncate` forces a prefix truncate, so every key is absent and the replay creates each entry (nothing
to withhold). `Hydrate`: personal only.

### 4.4 What the watermark orders, what the rewrite was fencing, and the two refusals that replace it

The first draft added a per-actor lock spanning evaluation→write; pass 1 (§12) found it self-deadlocked three
existing callers and missed the actor's-own-vertex path, and the fold was to delete it. Pass 2 then attacked the
argument that replaced it and split it into two axes. Both are stated here as the code has them.

**The ordering axis — sound, and pinned.** Let X be an entry, *w* its stored watermark, *c* the sequence of the
event whose evaluation last **wrote** X. Under withholding *w* = *c* ≤ the sequence of X's last presence/content
change (after a replay, *c* is the *first* replayed write that created X — §2 row 22 — so the inequality is not an
equality). Every writer captures its token **before** it evaluates, and the token is at most the newest sequence the
lens had applied at capture (`LastAppliedSeq` for `Reproject`, §2 row 8; a CDC event's own sequence, which precedes
its handling). A message exists only after its commit, so no evaluation can carry a token newer than the commits it
could have read. Hence **an older evaluation can never carry a higher token than the write it races** — a
redelivered, Nak'd or queue-split CDC handle, a rebuild replay, or a second Refractor instance can only produce the
harmless direction (newer evaluation, lower token: declined, or landing a current verdict). Pass 2 tried each of
these (§12) and could not construct the inverse. T3(a) pins it on a real guarded adapter.

**The presence axis — where the rewrite was doing unstated work.** "An evaluation that reads X absent read Core KV
before X was created" is true only for the half of the walk that reads Core KV. Node properties are live point
reads; **edges come from the `refractor-adjacency` index** (§2 row 19), a separately-cursored durable with no exposed
progress, which the CDC link arm pre-applies for its own triggering link only and which `Reproject` never pre-applies.
So a `Reproject` can read X absent *after* X's creating link committed, in two ways: (S-lag) the adjacency consumer is
behind the lens's cursor — the ordinary case under load, which the hub-walk design measured at a 1.2 M-message backlog;
(S-wrong) an unguarded `removeEdge` applied a redelivered older tombstone of a reused `EdgeID` over a live edge (§2
row 20). In either case the `Reproject`'s tombstone carries a *fresh* token *t* (it captured after *c*), so *t* > *w*
and the guard lands it — **today too**, whenever the actor has had no CDC event since *c*. What today's unconditional
rewrite adds is an **incidental fence for busy actors**: every event reaching A re-stamps every entry at the head, so
a stale `Reproject` tombstone on a busy actor is usually declined. Withholding removes that fence by construction, and
`footprintValid` does not restore it (it re-reads the *index* and detects tearing, not staleness). The CDC path itself
is *not* fenced by the rewrite in either world: a CDC evaluation under a stale index tombstones X at its own, higher
sequence today exactly as it would after this design.

**The replacement — two refusals in `Reproject`'s delete arm, and one filed bug.** The accident is replaced by
intent, on the one writer the fence protected:

1. **Index-behind refusal (S-lag).** The adjacency bootstrapper (`consumer/bootstrap.go`) exposes `AppliedSeq()`
   — **the contiguous floor of what the shared adjacency durable has retired (amended at build, 2026-09-05, two
   reviewers):** a monotone maximum over Ack'd/Term'd sequences, capped one below the lowest Nak'd-and-still-owed
   sequence (a max over retirements alone would claim edge 100 applied once 101–120 had retired around its Nak),
   and raised to the stream head on the caught-up poll only when the head was read *before* the caught-up check
   (a message landing between the two reads must not be claimed). `cmd/refractor` hands every pipeline a
   `func() uint64` (the `PersonalHealerVerdictFn` bare-func precedent). **The refusal is scoped to the tombstones
   whose absence the index served (amended at build, 2026-09-05, three reviewers):** only a dropped-entry tombstone
   `multiEntryRetractions` manufactured from the prefix diff against a real evaluation — the executor's edge walk —
   carries the mark `Reproject`'s delete arm reads; the legacy-parent tombstone, the missing-actor tombstones (a
   live `fetchVertexProps` read) and every doc-mode lens's document delete are unmarked and land as today. A marked
   tombstone lands only when `adjacencyApplied ≥ seq` (the token it captured); otherwise the result folds as
   `VerdictBlocked` with class `BlockedUnknown` and reason *"adjacency index behind the reconciliation token"*, and
   the sweep's next pass retries — under a sustained index backlog that streak reaches the auth plane's error tier,
   which is the priced trade (§8 row 5c's over-grant for the backlog's duration, made visible rather than silent). A
   per-process cursor under-states on a second instance and reads 0 after a restart until the first apply or poll —
   both refuse more, never less. This puts `Reproject` where the CDC link arm already is: it never tombstones from
   a view older than what the lens has applied.
2. **Rebuild-moved refusal.** `Pipeline.Rebuild` increments a rebuild generation (one atomic, beside
   `rebuildInFlight`); `Reproject` captures it with its token and abandons every write — not only tombstones — with
   `VerdictBlocked` if it differs at write time or `RebuildInFlight()` is true. The sweep already refuses a tick during
   a rebuild (§2 row 21); this closes the `Reproject` that was already running when one began, the only writer whose
   token can sit above a replay's first write.
3. **S-wrong is filed, not absorbed** (§13): an ordering guard on `upsertEdge`'s removal is an adjacency-index fix
   with its own consumers (every lens, the enumerator, the CDC path today), and this design neither widens nor narrows
   it — a wrongly-removed edge produces the same under-grant through the next CDC event on A with or without
   withholding. Until it lands, an S-wrong tombstone from `Reproject` heals at the next CDC event on A (row 4) or at
   the sweep's next pass after the index is repaired.

**What remains (§5 rows 7r, 7s; §9).** Row 7r needs a retraction the fan-out already failed to deliver (an over-grant
the sweep already owns). Row 7s needs S-wrong. Both are pre-existing classes, both deny-direction under this design,
both healed by the machinery that heals them today. The design converts a probabilistic fence into two refusals with
stated preconditions, and says so.

### 4.5 Arming — three conjuncts, read per event off the pipeline

Withholding is **armed** for a pipeline iff: (i) `multiEnvelopeFn != nil` (the family — §2 row 10 makes the raw
replay unreachable there); (ii) the current adapter is a `RowsReader` **and** `SeqGuarded` with `Guarded()` true (a
target that cannot be read back, or is unguarded, never withholds — the unguarded arm already skips on its own);
(iii) `secureDecryptor == nil` (§2 row 13: the compare runs before decryption, so a Secure perEntry lens — none
exists — would compare ciphertext against stored plaintext and never withhold; refusing is the honest answer, logged
once at install). All three are read where `admits` reads the adapter, per call; none is cached, none can go stale.

### 4.6 Observability

`healthwire.Entry` gains `EntriesWithheld uint64` (monotone, carried forward — §2 row 16) and `WithholdReadFailures
uint64`. The lens's `pipeline: processed` line carries `entriesWithheld`. X6's three commands are the measurement; no
new tool (the stream's subject counts are already the per-lens census).

### 4.7 Non-goals (the conjunct that holds each)

- **Doc-mode actorAggregate lenses** (`cap.roles`, `cap.svc`, `cap.ephemeral`, `my-tasks`, `cap.identity`): one key
  per actor, so the amplification is ×1; and their transient path is the raw replay (`enqueueRetry`, `results.go:255`)
  the adapter comment's hazard names — held by conjunct (i).
- **Plain lenses**: no watermark, no per-actor set; the unguarded adapter already skips byte-identical rows.
- **Personal lenses**: the delta-publication design's; held by conjunct (ii) (a `KeySetPublisher` is not a `RowsReader`).
- **The actor set per event**: the `WITH`-scope row's Inc 2.

---

## 5. State table — the predicate, every writer, and the two interleavings that matter

| # | Stored entry X | Fresh | Outcome | Note |
|---|---|---|---|---|
| 1 | absent (never written) | present | **write** (create) | today |
| 2 | live, body equal modulo `projectedAt` | present | **withhold** | the design; `projectedAt` may differ (an actor-vertex edit) — the sweep already calls that converged |
| 3 | live, body differs (`via`, `anchorType`, a new field) | present | **write** | today |
| 4 | tombstoned (`isDeleted`) | present | **write** (resurrect at the new seq) | `GetRows` reports it absent — today |
| 5 | live | absent from fresh | **tombstone** (`FailClosed`, first) | today; never withheld |
| 6 | tombstoned | absent | **skip** | today (`evaluate.go:941-950`) |
| 7 | live, equal, *w* = seq of the write that created X · CDC event *e* withholds · older `Reproject` *A* (token *t*, **fresh view** read X absent) tombstones at *t* | — | **declined**: a fresh view read X absent ⇒ read before X's creating commit ⇒ *t* < that commit ≤ … and *A* captured before *w*'s write was applied ⇒ *t* ≤ *w* (§4.4 ordering axis) | same as today, without a lock; T3(a) |
| 7s | live, equal · `Reproject` *A* with a **stale index view** (S-lag or S-wrong) reads X absent, token *t* > *w* | — | **S-lag: refused** (`adjacencyApplied < t` ⇒ `VerdictBlocked`, retried next pass). **S-wrong: lands** — X tombstoned though present; heals at the next CDC event on A (row 4) or the sweep's next pass after the index is repaired | today: declined only while A is busy (the incidental fence); the CDC path lands the same tombstone today under the same stale index; S-wrong filed (§13) |
| 7r | live, **stale** (a prior retraction was never written; *w* old) · *A* (token *t* > *w*, read X absent) · re-creation *e* withholds (equal) · *A*'s tombstone lands | — | **X tombstoned though present** — under-grant until the sweep's next pass rewrites it (row 4) | requires an already-lost retraction, which today is an over-grant until the same sweep; §9 |
| 8 | any · `GetRows` fails / member unparseable | any | **write** (that member) | fail toward today, per member |
| 9 | live, equal, withheld · `RowNullifier` / `DeleteAllForActor` (shred, `MaxInt64`) | — | tombstoned ✔, and no later write beats it | terminal authority; today |
| 10 | prefix truncated (rebuild) — including a batch that read X live *before* the purge and withholds *after* it | present | X absent until the rebuild's replay recreates it (`DeliverLastPerSubject` visits every key); a MATCH-narrowing reload is additionally caught by `supersededRule` | converges by the same replay a rebuild already relies on |
| 11 | any · disarmed (§4.5) | any | **write** everything | today |
| 12 | redelivery of the same message | — | same verdict | the evaluation reads current state; idempotent |
| 13 | actor vertex absent or `isDeleted` at `fetchVertexProps` (a **live** read) | — | every child tombstoned at the writer's seq (`reprojectActors`' missing-actor branch) | today; the blast radius of any writer is the actor's whole set — cited because it is what an unfenced token reaches |
| 14 | after a rebuild / activation replay: X created by the **first** replayed message reaching A, every later one withholds | present | *w* = that first write's sequence, below X's own creating change | harmless with a fresh view (an evaluation after the replay reads X present); a `Reproject` already running when the rebuild began is the one writer whose token can exceed it — refused by the rebuild-moved check (§4.4 §2) |

**Lifetimes of the new state.**

| State | Created | Reset | Carried | Ordered |
|---|---|---|---|---|
| `EvalResult.Unchanged` | per evaluation, in `multiEntryRetractions` | never (per-batch value) | never across batches | n/a |
| `EntriesWithheld` / `WithholdReadFailures` | first withhold / first failure | never (monotone) | carried forward on the health entry (§2 row 16) | n/a |
| adjacency `AppliedSeq` (a retired-max atomic + the owed set, in the bootstrapper) | first retired message or first caught-up poll | 0 at process start (refuses until set) | per process, never durable; reports the shared durable's floor, so a restart against a drained durable reports prior work | contiguous floor: max(retired) capped below min(owed) |
| rebuild generation (one atomic beside `rebuildInFlight`) | first `Rebuild` | never | per process | monotone |

---

## 6. Reconciliation with the existing mental model

- **Didn't we already handle this?** Partly, three times, never on the CDC path: the unguarded adapter skips
  byte-identical rows (`natskv.go:377-390`); `Reproject` skips converged rows (`reproject.go:395-398`);
  `multiEntryRetractions` skips an already-tombstoned candidate (`evaluate.go:941-950`). The guarded CDC upsert is
  the one arm with no "unchanged" verdict, and the adapter comment explains why it could not grow one *there*: at the
  adapter, the watermark advance is the only ordering the adapter can see. One level up, the pipeline can see that
  the watermark already carries the last presence change (§4.4), which is all the ordering a skipped write needs.
- **Does it contradict a design of record?** No. The per-anchor design's §4.2 prescribed the prefix diff and its
  skip-tombstoned arm; the reconciliation design prescribed zero-write convergence. This is their composition on the
  path they left out. The adapter's no-skip pin stays green: nothing below the pipeline changes.
- **Does it add state we already keep?** No durable state; two counters on an entry that already carries six.
- **Why not wait for the `WITH`-scope Inc 2?** It narrows actors, not entries (§8 row 1); the entry axis is
  independent and compounding, and Inc 2 is held behind a live measurement of its own.

---

## 7. Contract surface — builds to, no change

- **Contract #6 §6.2 (amendment):** *"A guarded-key write whose `projectionSeq ≤` the stored value is rejected as an
  idempotent no-op."* Untouched — every write still carries the token and the guard still decides; a withheld write is
  no write. **§6.14:** *"retraction is an explicit per-actor prefix diff (an entry absent from a fresh evaluation is
  guard-tombstoned, tombstones-first, in the same pass)"* and *"write failures retry as actor re-evaluations, never
  raw-write replays"* — both hold verbatim (§4.3, §2 row 10).
- **§6.3 `projectedAt` (mirrored by §6.14):** *"Deterministic provenance ("as-of input state"): the anchor actor
  vertex's `lastModifiedAt` … Same input → same value across replay/rebuild."* This is the one sentence the new steady
  state bears on: a withheld entry keeps the stamp of its last *committed* projection, so after an actor-vertex edit
  its `projectedAt` lags the actor's `lastModifiedAt` until the entry's next content change or a rebuild (which
  recreates and re-stamps it). Accepted, and stated rather than paraphrased: the field has no reader on `cap-read`
  entries (§2 row 9), the contract itself classifies it as *"not a freshness ceiling"*, and the sweep has treated it
  as volatile since Fire 1b (§2 row 7). Nothing a consumer observes against the current text changes; if Andrew reads
  the sentence as a promise of currency, the touch-up is one clause on §6.3 and this design's §4.2 is unchanged.
- **Contract #5 (Health KV):** two new per-lens counters on the component's own entry — operational self-reporting,
  the sanctioned direct-KV write class.

---

## 8. Alternatives

| # | Alternative | Verdict |
|---|---|---|
| 0 | **Do not have the thing — delete the whole-set rewrite.** The mechanism removed is "write every fresh entry"; nothing new on the wire, in the store or in the contracts. What stays: the prefix diff (retraction) and the guard (ordering), which §4.4 shows already order the withheld case. | **Taken.** |
| 1 | **Do nothing here; let the `WITH`-scope Inc 2 land.** It moves the producers from the adjacency-walk fan-out to pattern-directed derivation, so a `providedTo` create reaches the actors the pattern binds (staff at that workplace) instead of the ~166 the walk reaches — the actor axis, plausibly ≥ 10×. It still writes every entry of every reached actor (row 2's provenance is per row), and an actor-vertex event (the walk's own head) still rewrites its whole set. Composes with this design; sequenced by its own hold. | Rejected as the whole answer; taken as the neighbour. |
| 2 | **Provenance-scoped writes** (the row's `no-pattern:` prescription — `ScopeVertices` on the perEntry sites). Every entry inherits the *actor row's* provenance (§2 row 2), and the event vertex is in that read set by construction ⇒ the scope admits every entry ⇒ zero narrowing. Per-element provenance inside `collect(DISTINCT …)` is engine work across a staged `WITH` and still not content-exact. The store compare is exact and needs no engine. | Rejected. |
| 3 | **A content skip in the guarded adapter.** The adapter cannot see that the watermark carries the last presence change (that is a fact about who writes what, §4.4); the pin at `natskv_test.go:367` records the last time this was ruled on, and the doc-mode family's raw replay would re-import the hazard verbatim. | Rejected; the pin stays. |
| 4 | **A per-actor lock spanning evaluation→write** (this design's first draft). Self-deadlocks `ReprojectPersonalActor`, `Hydrate` and `Reproject`, which already hold the slot before `reprojectActors` (`reproject_personal.go:213-227`, `hydrate.go:115`, `reproject.go:436-442`); misses the default arm at `evaluate.go:318`; needs a single-instance premise with a census and a lint. And unnecessary — §4.4. | Rejected (§12 F1, F2, F3, F5). |
| 5 | **A token re-check in `Reproject`'s delete arm** (§12 F6: re-read `LastAppliedSeq` before each tombstone, abandon if it moved). Guards the ordering axis, which §4.4 shows the token already orders; does nothing for the presence axis (a stale-index `Reproject` re-reads a cursor that has not moved and tombstones anyway). | Rejected; the two refusals in §4.4 guard the axis that is actually exposed. |
| 5b | **Clamp `Reproject`'s token to `min(LastAppliedSeq, adjacencyApplied)`** (pass 2's suggestion). A lower token helps only when *w* is near the head — exactly the busy-actor fence withholding removes; on a resting entry (*w* old) the clamped token still exceeds *w* and lands. Refusing outright when the index is behind is the same information used in the direction that fails closed. | Rejected in favour of §4.4's refusal 1. |
| 5c | **Disable `Reproject`'s delete arm for perEntry lenses** (leave retraction to the CDC path, which pre-applies its trigger). The sweep's deep-verify is the only healer of a lost retraction (row 7r's over-grant); removing its tombstone arm trades a bounded under-grant for a standing over-grant. | Rejected. |
| 6 | **Keep writing; dedup the audit only.** Retires the 74 % trail share and nothing else: 400 guarded Puts/s, C4's 213 M deletes and the CDC fan-out on `capability-kv` continue, for the same batched read. | Rejected. |
| 7 | **Raise / shard the audit cap.** Hides the trail depth behind a bigger buffer; the writes and the churn remain. | Rejected. |
| 8 | **Rewrite the producer** (fewer walks, a lighter body). The grant set *is* the product requirement (each entry is a D1 grant); `via`/`anchorType` are ~60 bytes; the generator's staged `WITH` already bounds evaluation. | Rejected. |
| 9 | **Route the CDC path through `Reproject`** (one read-and-compare per result). Unpipelined: N `GetRow` round trips and N unpipelined writes per actor per event — the shape the pipelined loop replaced. | Rejected; its predicate is taken. |

---

## 9. Risks and residuals

| # | Risk | Direction | Mitigation |
|---|---|---|---|
| R1 | **Row 7r**: a retraction the fan-out lost (DLQ'd `ErrActorSetTooWide`, a pruned adjacency edge) can now surface as a transient under-grant on the re-created entry instead of a standing over-grant. | deny, bounded by the sweep's rotation (`DefaultSweepInterval` 60 s × candidates per tick) | the precondition is an existing failure the sweep already heals; T3 pins both faces so the trade is a number, not a surprise. |
| R2 | The batched read adds `ceil(N/1024)` requests per actor per event against writes it replaces. | cost | C3: ≤ 4 requests on the widest actor vs 3,644 writes; T7 reports reads per withheld entry; X6 measures live. |
| R3 | A future writer of perEntry keys with a token captured *after* its evaluation would break premise 3. | §5 row 7 reopens | X3 is a Phase-0 census; T4 classifies every writer by its token discipline and fails on an unclassified hit. |
| R4 | A corrupt stored member. | that entry is rewritten; siblings withheld | §4.2 per-member semantics; T5 (dossier-mandated shape). |
| R5 | `projectedAt` lag on withheld entries (§7). | none observable | no reader; stated in the contract-surface section for the next reader. |
| R6 | The personal `edgeCatalog` share (18 %) is not retired here. | trail depth improves less than 74 % | the delta design's T7; noted, not owned. |
| R7 | **Row 7s, S-wrong**: `adjacency.upsertEdge` removes a reused `EdgeID` with no ordering guard (§2 row 20), so a redelivered older tombstone deletes a live edge and a `Reproject` reading that view tombstones a live entry. | deny; heals at the next CDC event on A or the sweep after the index is repaired | pre-existing (the CDC path lands the same tombstone today); filed as its own row (§13); not widened or narrowed by this design. |
| R8 | After a rebuild or activation the whole perEntry corpus rests at *historical* watermarks (the first replayed write per entry, §5 row 14) instead of near the head — the widest exposure for any writer whose token outruns its view. | the presence axis, at its widest | the two refusals are what bound it: S-lag cannot tombstone, a `Reproject` spanning the rebuild abandons; S-wrong is R7. |
| R9 | `LastAppliedSeq` is *assigned* on Ack, so a redelivered older message moves it backwards, and it reads 0 after a restart (every `Reproject` write drops fail-closed until the first Ack). | heal-liveness only, never safety | existing behaviour (§2 row 21), cited so the build does not "fix" it into a max under this design. |

---

## 10. Test strategy

| # | Proves | Shape | Inc |
|---|---|---|---|
| T1 | An entry whose stored body equals the fresh one is withheld: no adapter write, no audit entry, no freshness mark, no `recordProjectionWrite`; only keys under the actor's prefix are requested; a `Delete`/`FailClosed` result is never `Unchanged` | scripted `RowsReader` adapter; assert write/audit call lists and the requested key set | 1 |
| T2 | The predicate is `rowsEquivalent`: a `projectedAt`-only difference withholds; a `via`/`anchorType`/new-field difference writes; a tombstoned or absent key writes; an unrenderable body writes | table over §5 rows 1–4, 8 | 1 |
| T3 | **The ordering axis and its two residuals.** (a) Row 7: X created at *c*; a `Reproject` whose token was captured before *c* and whose evaluation read X absent issues a tombstone — asserted **declined** by the guard, X live. (b) Row 7r: X live-stale with *w* < *t*; the same `Reproject` lands its tombstone after a CDC withhold — asserted X tombstoned, then the next `Reproject` pass rewrites it. (c) Row 7s S-wrong: the adjacency doc missing X's edge while Core KV holds the link, index cursor at head — asserted the tombstone lands (today's behaviour) and the next CDC event on A rewrites X. **Mutation:** making a presence change withholdable (writing row 4 as `Unchanged`) must red (a) | embedded NATS fixture, real guarded adapter, a real adjacency bucket, ordering driven by channels | 1 |
| T4 | Every writer in §2 row 11 is classified: token-before-evaluation (CDC, `Reproject`), terminal authority (`RowNullifier`, `DeleteAllForActor`), prefix purge (`Truncate`), personal, or unreachable-for-perEntry (`replayWrite`); the hit list equals X3 and an unclassified hit fails | a test that greps the pipeline package for the four write methods and classifies each hit by enclosing function | 1 |
| T5 | A corrupt stored member is rewritten and its siblings withheld (`TestPrefetch_CorruptBodyFailsOnlyWhereItIsUsed`'s shape) | one unparseable value among N | 1 |
| T6 | Arming: each conjunct's negation writes everything (no `multiEnvelopeFn`; adapter not a `RowsReader`; adapter unguarded; `secureDecryptor` set); the refusal logs once at install | table | 1 |
| T7 | `GetRows` chunks above 1,024 keys, strips `projectionSeq`, drops tombstones, never fails the batch for one member; reads-per-withheld-entry reported | adapter unit over `natsfixture` with 1,100 keys | 1 |
| T8 | The health entry carries the two counters forward (the completeness test discovers them) and `pipeline: processed` reports `entriesWithheld`; the auditor's `executeFullForAudit` path issues no `GetRows` | existing reflective test + a log capture + a scripted adapter counting reads | 1 |
| T11 | **Index-behind refusal:** `adjacencyApplied < seq` ⇒ `Reproject`'s delete arm writes no tombstone and folds `VerdictBlocked`/`BlockedUnknown` with the stated reason; `≥ seq` ⇒ the tombstone lands; the upsert arm is unaffected; a 0 cursor (fresh process) refuses; the bootstrapper's poll sets the cursor on an empty stream | scripted cursor func + real guarded adapter | 1 |
| T12 | **Rebuild-moved refusal:** a `Reproject` captured at generation *g* whose write phase runs at *g+1* (or while `RebuildInFlight()`) abandons every write with `VerdictBlocked`; the same call at *g* writes | channel-parked `Reproject` around a `Rebuild` | 1 |
| T9 | **e2e on the real producer:** in the capability e2e harness, one `providedTo` link create against a populated `edgeManifestStaff` domain writes exactly the new/dropped entries; the audit subject gains exactly that many; a second identical event writes zero | extend the harness the `WITH`-scope T12 extends | 2 |
| T10 | Live: X6 before/after on the dev stack — staff share of *new* audit entries ≤ 5 %, entries per 20 s < 100 at the same event rate, `Deleted` growth ≤ 5 % of the before rate | manual, recorded in the build note | 2 |

---

## 11. Decomposition for the Steward

**Increment 1 — the fourth arm and the two refusals** (`adapter`, `ruleengine`, `pipeline`, `consumer`, `cmd/refractor`
wiring, `healthwire`). **Posture-changing: full review depth** — it changes what an auth-plane write loop commits and
what its reconciler may retract. Phase 0 runs X1–X5 and stops on any drift. Order: the adjacency `AppliedSeq` and the
rebuild generation, with `Reproject`'s two refusals (T11, T12) → `GetRows` (T7) → `Unchanged` +
`multiEntryRetractions` + arming (T1, T2, T5, T6) → the ordering pins (T3, T4) → the write loop and `Reproject` fold
(T1's write-side half) → counters (T8). Each step green before the next; **the refusals land before the write loop
skips anything**, so no commit on `main` ever has withholding without the fence's replacement.

**Increment 2 — proof on the real producer** (e2e + live): T9, T10; `docs/components/refractor.md` — the audit row
(line 67) gains the withheld case beside the three it lists, the *Convergence sweep* section names the CDC loop's
converged skip and §4.4's lemma in one sentence; the dossier close-pass classification. The board row closes on
T10's numbers.

Both increments are one **S–M** fire. **Sequencing:** independent of the `WITH`-scope Inc 2 and of the delta design's
close; lands in any order with either.

---

## 12. Adversarial pass — RUN (this fire, 2026-09-04; one cold, read-only reviewer against the code)

Fourteen findings; all folded. The three blocking ones and two of the majors attacked the first draft's per-actor
slot, and the fold was to delete it (§4.4, §8 row 4) after re-deriving the race it guarded:

| # | Finding | Fold |
|---|---|---|
| F1 · blocking | The promoted slot is non-reentrant; `ReprojectPersonalActor`, `Hydrate` and `Reproject` already hold it before `reprojectActors` ⇒ self-deadlock | slot deleted (§4.4) |
| F2 · blocking | The slot was acquired at `reprojectActors`, which the actor's-own-vertex path (`evaluate.go:318`) never passes through — the race stayed open for the event's actor | slot deleted; §2 row 1 now cites both paths |
| F3 · blocking | Arming conjunct (iii) was unbuildable: `countInstances` unexported on `*PersonalSweeper`, `grantchange` imports `pipeline` (cycle), and the personal sweeper returns before the census when no personal lens is registered ⇒ silently inert | census deleted; arming is three per-call conjuncts (§4.5) |
| F4 · major | X3 returned 18 hits, not 11; `pipeline.go:1667` is `Pipeline.Delete` (the `RowNullifier`), not `DeleteAllForActor`; two unguarded fallback arms unlisted | §2 row 11 re-enumerated; X3 expects 18; T4 classifies |
| F5 · major | "Sorted acquisition" was two acquisition groups, safe only by single-worker | moot (slot deleted); §2 row 12 kept as a non-premise |
| F6 · major | A lock-free token re-check in `Reproject`'s delete arm was unpriced | priced (§8 row 5) and found unnecessary: the re-derivation it prompted is §4.4 |
| F7 · major | The compare runs before `applySecureDecrypt`; a Secure perEntry lens would never withhold, silently | arming conjunct (iii) |
| F8 · minor | X1 has three sites (the parser at `output.go:186`) | corrected |
| F9 · minor | `classifyDivergence`'s provenance arm is dead for perEntry bodies | predicate is `rowsEquivalent`; §2 row 7 says why |
| F10 · minor | The contract sentence at risk is §6.3's replay/rebuild determinism, not the ones quoted | §7 quotes it and states the accepted lag |
| F11 · minor | A frozen `lastProjectedAt` is safe only because auth-plane lenses are excluded from `LensProjectionStalled` — unstated | §2 row 16 |
| F12 · minor | The auditor would pay the batched read | gated on the costed path (§4.2); §2 row 17 shows the auditor never reaches perEntry anyway |
| F13 · minor | No row for a truncating rebuild racing a decided-but-unflushed batch | §5 row 10 |
| F14 · minor | The lint declaration must not live in `pipeline` (import weight) | moot (no lint) |

**Pass 2 — six attacks on the replacement argument (a second cold reviewer, read-only).** Attack 1 (premise 3):
holds — `recordAppliedSeq` runs after Ack, `msg.Sequence` is the stream sequence (`substrate/consumer.go:468`).
Attack 1 (premise 2): **broke it** — edges are read from the adjacency index (§2 row 19), `Reproject` pre-applies
nothing, `upsertEdge`'s removal is unguarded (§2 row 20); the unconditional rewrite was an incidental fence for busy
actors ⇒ §4.4 rewritten on two axes, refusal 1, R7 filed. Attack 2 (CDC redelivery / out-of-order handles): harmless
direction confirmed; two nuances recorded (§2 row 21, R9). Attack 3 (rebuild replay): **broke premise 1's equality**
— under withholding *w* is the first replayed write, not the last ⇒ restated as an inequality, §5 row 14, refusal 2,
R8. Attack 4 (multi-instance): survives on the ordering axis; the presence axis is a shared-index property ⇒ §0.1
reworded. Attack 5 (enumerator vs executor): presence is the executor's verdict and the executor's edge reads are the
index ⇒ folded into §4.4's presence axis. Attack 6 (other writers): the whole-actor wipe's blast radius (§5 row 13);
`Reproject`'s absent-row skip protects nothing in the harmed case (noted in §4.4); `resolveTruncate` declines a
guarded non-`Truncater`, which arming conjunct (ii) already excludes (§4.3 reworded to the KV family).

**§2.3 walk, recorded.** A · the demand: mechanism grounded in `natskv.go:349-361` (not the audit writer); units named
(entries, not rows; per-event and per-second); the falsifying census (C1b/C3) widened the harm and re-attributed 18 %
to a personal lens. B · the transport: `GetRows`'s bound named; the retraction untouched; every replay path read
(`replayWrite` unreachable, `Reproject` capture-then-evaluate, shred terminal, truncate purge); the permission
envelope unchanged (reads on the lens's own bucket). C · censuses run and pasted (X1–X5), and the reviewer's re-run
corrected two of them. D · the state table precedes the predicate; the fail direction of every conjunct is over-write;
the borrowed predicate's owner (`Reproject`) does the same thing with it when wrong (rewrites). E · no new durable
state; the two counters have lifetimes. F · row 0 is deletion; a rejected alternative (the lock) was my own first
draft and is priced against the code that refuted it.

---

## 13. Board + doc actions

- The board row flips `🏗️ designing` → `✅ ratified (Winston-adjudicated)`, pointing here; its *What* cell is
  corrected to the mechanism (a perEntry actor's whole entry set rewritten under a guard that cannot skip) and to
  C1/C2's numbers.
- `personal-lens-delta-publication-design.md` §1.2 H4's "filed on the lane as its own row" resolves to this doc at
  that design's close (its row is 🏗️ and owned by a build fire; not edited here).
- **Filed this fire, component maintenance:** *[Refractor] `adjacency.upsertEdge` removes an edge by `EdgeID` with no
  ordering guard* — a redelivered older tombstone of a reused link key (`builder.go:150-162`, `bootstrap.go:179-183`)
  deletes a live edge; every executor walk, the enumerator and the CDC prefix diff then read the link as absent
  until the next apply or an overflow-arm read. Evidence and consumers in §4.4 (S-wrong); this design's row 7s is the
  first named victim. ★★ · S–M · 📋 ready.

## 14. Dossier entries this design is built against (`docs/components/refractor.md` § *Review keeps catching*)

- *A soundness claim's stated REASON is load-bearing … refuting a refusal's REASON does not establish that the whole
  refusal was wrong* → §6 / §8 row 3: the adapter's "no content skip" reason is **correct** for the adapter; the design
  re-derives the boundary from the writers the refusal protects (§4.4) and finds it one level up, rather than
  deleting the pin.
- *A widened operation silently drops the bound or budget its narrow predecessor carried* → §4.2 names `GetRows`'
  bound (`ceil(N/1024)` × `directGetMultiDefaultTimeout`, no drain) in the seam's doc.
- *A present-but-EMPTY set and a missing one … a corrupt member of a set read fails the whole set* → §4.2 per-member
  semantics, T5.
- *A two-layer seam can be green at each layer and broken across it* → T3 runs the real interleaving on a real guarded
  adapter, and its proof is a mutation.
- *A new health `Entry` field ships with no carry-forward line* → mechanized; T8 relies on the gate.
- *Making a fault STANDING makes its severity permanent* → §4.2: a read failure is a gauge, not an error latch.
- **Candidate line from this fire** (for the Steward's close pass, second sighting pending): *a serialization added
  to protect a skipped write should first be re-derived against the ordering token the write was carrying — the
  token may already encode the fact the lock would enforce.*

---

## 15. Fire brief (build note, 2026-09-05 — Steward fire `claude/bold-tesla-ras9ml`)

**Fire:** Inc 1 + Inc 2 of §11, one S–M fire, landing shape **hold the branch and merge once when green** (main never
carries withholding without §4.4's refusals — §11's order puts the refusals first inside the branch too).

### 15.1 Scope sentence (verbatim, §4)

> A perEntry fresh entry is written iff its stored body differs from the fresh one; a tombstone is always written.
> "Differs" is `Reproject`'s own verdict: `!rowsEquivalent(stored, fresh)` … An entry that is absent, tombstoned,
> unreadable or unparseable in the store is "different". Withholding applies only while **armed** (§4.5); disarmed,
> every entry is written — byte-identical to today.

Green bar: T1–T8, T11, T12 (Inc 1) and T9 (Inc 2) green; X1–X5 re-pinned; T10 is a live dev-stack measurement
(Mac-only — `agents/steward/REMOTE.md` §3) and is recorded as **pending Andrew's stack** in §15.7, not faked.

### 15.2 Verified touch-list (checked live 2026-09-05 by two read-only scouts; design citations that moved are restated)

| Site | Current anchor | What changes |
|---|---|---|
| `internal/refractor/ruleengine/eval_result.go:16-45` | `EvalResult` | `+ Unchanged bool` (§4.1; zero value = write) |
| `internal/refractor/adapter/adapter.go:81-92, 128-130` | `SeqGuarded`, `RowReader` | `+ RowsReader { GetRows(ctx, keys []string) (map[string]map[string]any, error) }` beside `RowReader` |
| `internal/refractor/adapter/natskv.go:73-83` (`kvStore`), `:754-778` (`GetRow`) | | `kvStore + GetMultiNoSnapshot`; `GetRows` = `a.kv.GetMultiNoSnapshot` (`substrate/kvhandle.go:87` → `kv_multi.go:323`) + `GetRow`'s per-member shaping (tombstone ⇒ absent, unparseable ⇒ absent, strip `projectionSeq`); the four `kvStore` doubles in `natskv_internal_test.go` / `grant_transition_internal_test.go` gain the method |
| `internal/refractor/pipeline/evaluate.go:975-1040` (`multiEntryRetractions`), call site `:933-939`, `executeFullForActorOnce :773` (`recordCost` at `:791-794`) | the seam | gains `recordCost`; when `recordCost && p.withholdingArmed(adpt)` reads `existing ∩ freshKeys` via `GetRows`, marks `Unchanged` on `rowsEquivalent`; a batch error marks nothing, warns once per actor, bumps `WithholdReadFailures`. Callers at `:246` / `:1303` pass `nil` fresh (nothing to withhold) |
| `internal/refractor/pipeline/results.go:103-109` (`admits`), write loop `:158-216`, `:271-286` (perEntry retry), `:410` (`pipeline: processed`) | write loop | `if result.Unchanged { withheld++; continue }` immediately after `admits`; `entriesWithheld` on the processed line (thread the count from `writeResults` to `handle`) |
| `internal/refractor/pipeline/reproject.go:447` (token), `:503-575` (delete arm; `DeleteWithOutcome :532`), `:578-660` (upsert arm; `GetRow` compare `:580-595`), `Verdict :55-79`, `BlockedClass :113` | `Reproject` | delete arm: **index-behind refusal** (`adjacencyApplied() < seq` ⇒ `fold.addBlocked(VerdictBlocked, BlockedUnknown, "adjacency index behind the reconciliation token")`, no write) and **rebuild-moved refusal** (generation captured beside `seq`; at write time `gen != p.rebuildGeneration() \|\| p.RebuildInFlight()` ⇒ every remaining write abandons as `VerdictBlocked`); upsert arm: `result.Unchanged` ⇒ `VerdictConverged` without `GetRow` |
| `internal/refractor/pipeline/pipeline.go:530-542` (`rebuildWindows`), `:1357-1360` (`RebuildInFlight`), `:121-126` (`multiEnvelopeFn`), `:386-389` (`secureDecryptor`), `:1238-1261` (`PeakBindingRows` accessor precedent) | | `+ rebuildGeneration atomic.Uint64` raised in `rebuild.go:258-263` `openRebuildWindowLocked`; `+ adjacencyAppliedFn func() uint64` + setter; `+ withhold counters` + accessor |
| `internal/refractor/consumer/bootstrap.go:117-123` (`handle`), `:90-115` (`pollReady`), `:165-185` (`processMsg` → Ack) | adjacency bootstrapper | `+ appliedSeq atomic.Uint64` (monotone max): stamped with `msg.Sequence` after an Ack/Term disposition; on the caught-up poll with the stream's last sequence (new `substrate.Conn` helper beside `ConsumerCaughtUp`, `substrate/consumer.go:440`, reading `Stream.Info().State.LastSeq`); `AppliedSeq()` accessor |
| `cmd/refractor/main.go:600` (bootstrapper), `:1902` (`SetSecureDecryptor`), `:1991` (`SetPeakRowsFunc`), `cmd/refractor/personal_healer.go:76` | wiring | hand every pipeline `bootstrapper.AppliedSeq` (the `PersonalHealerVerdictFn` bare-func shape); wire the withhold counters into the lag poller the way `PeakBindingRows` is |
| `internal/refractor/health/healthwire/healthwire.go:102-145`, `health/reporter.go:232,346,436` (carry-forward), `:738-761` (`SetPeakBindingRows`), `health/lag_poller.go:374` | health | `+ EntriesWithheld`, `+ WithholdReadFailures` (monotone), carried forward in all three wholesale writers; `entry_carry_forward_completeness_test.go:38-101` discovers them |
| `scripts/lint-flag-consumer-census.go:76-90` | flag ledger | declare `internal/refractor/pipeline/reproject.go#Reproject` as a `RebuildInFlight` reader (re-read the `rebuildWindows` bound: the reader refuses writes, the safe direction) |
| `docs/components/refractor.md` audit row + *Convergence sweep* section; dossier | Inc 2 | per §11 |
| `docs/observability/health-kv-schema.md` `<lensId>` block + prose | Inc 1 (same change as the emission — Contract #5 §5.4; omitted from the first cut of this table, caught by the acceptance audit) | the two counters, absent for a lens that cannot withhold |
| `internal/refractor/edge_manifest_fire2_producer_flip_e2e_test.go:51` | T9 precedent | extend with the `providedTo`-create-against-populated-domain vector |

Citations that moved (claims all hold): §2 row 4 `evaluate.go:919-1010` → `:975-1040`; row 1 `reprojectActors :1218` → `:1261`;
row 12 `main.go:2404-2418` → `:70` (`lensAckWait`) + `:2447`; row 13 `main.go:1860-1870` → `:1892-1902`, `evaluate.go:88` → `:105`,
`dispatch.go:210,353` → `:34`; row 17 `audit.go:951-957` → `:1043-1044`; row 22 `main.go:2413` → `:2445`. X1 = 3 sites ✓, X2 ✓
(`:271` precedes `:287`), X3 = 18 ✓ (the 18 enumerated by enclosing function in the scout report; T4 pins them), X4 = 1 ✓, X5 ✓.

### 15.3 Precedents to mirror

- `Unchanged` fail-direction and placement → the personal `admits` skip (`results.go:103-109`, zero value admits).
- `GetRows` shaping → `GetRow` (`natskv.go:754-778`); bound and per-member semantics → `substrate.KVGetMultiNoSnapshot`'s doc
  (`kv_multi.go:323`) and `TestPrefetch_CorruptBodyFailsOnlyWhereItIsUsed` (`ruleengine/full/prefetch_test.go:852`).
- Bare-func wiring → `PersonalHealerVerdictFn` (`pipeline/anchor_derivation_personal.go:193,286`; `cmd/refractor/personal_healer.go:76`).
- Blocked fold → `fold.addBlocked(VerdictBlocked, BlockedRetraction, …)` at `reproject.go:540-545`.
- Rebuild generation → `rebuildWindows` (`pipeline.go:530-542`), raised inside `openRebuildWindowLocked` under `rebuildWatchMu`.
- Health counter → `PeakBindingRows` end to end (`pipeline.go:1238-1261` → `main.go:1991` → `lag_poller.go:374` → `reporter.go:738-761`).
- Flag reader declaration → the seven `RebuildInFlight` readers in `lint-flag-consumer-census.go:81-89`.
- Source-classifying test (T4) → `label_derivation_corpus_census_test.go`'s population-with-floor shape (assert the hit set is exactly the classified 18; a new unclassified hit fails).

### 15.4 Increment order + green checks

Inc 1 (§11 order, each step green before the next): (1) `rebuildGeneration` + bootstrapper `AppliedSeq` + substrate helper +
`Reproject`'s two refusals — T11, T12 · (2) `RowsReader` / `GetRows` — T7 · (3) `Unchanged` + `multiEntryRetractions` read +
arming — T1, T2, T5, T6 · (4) ordering pins — T3, T4 · (5) write loop + `Reproject` fold · (6) counters + health — T8.
Inc 2: T9 e2e; `docs/components/refractor.md`; dossier classification; §15.7 checkpoint.

```sh
go build ./... && make vet && golangci-lint run ./... && STRICT=1 go run ./scripts/lint-conventions.go
STRICT=1 go run ./scripts/lint-flag-consumer-census.go && STRICT=1 go run ./scripts/lint-slog-values.go
POSTGRES_TEST_DSN=… go test ./internal/refractor/... ./internal/substrate/... ./cmd/refractor/... -count=1
go test ./internal/refractor/health/ -run TestReporter_WholesaleWriters_CarryEveryEntryFieldForward -count=1
grep -rl "^//go:build " --include=*_test.go internal/ | xargs grep -l "refractor" # build-tagged harnesses the interfaces reach
```

### 15.5 In-scope gotchas (+ the dossier entries this fire builds against — read `docs/components/refractor.md` § *Review keeps catching* in full)

- The `natskv_test.go:375` no-content-skip pin STAYS; the skip lives in the pipeline (§8 row 3).
- `LastAppliedSeq` stays *assigned* on Ack (§2 row 21) — do not "fix" it to a max.
- A tombstone / `FailClosed` result is never `Unchanged`; §6.14's tombstones-first order is untouched.
- The refusals land BEFORE the write loop skips anything (branch commit order).
- `Reproject` gains a `RebuildInFlight` read → declare it in the flag census or CI's lint-static job reds.
- New health fields → carry-forward in all three wholesale writers or `entry_carry_forward_completeness_test` fails by name.
- A new slog attr of a module struct type needs `LogValuer` (`lint-slog-values`).
- Dossier: *a widened operation drops its predecessor's bound* → `GetRows` doc names `ceil(N/1024)` × `directGetMultiDefaultTimeout`
  and the ctx it runs under; *a corrupt member of a set read fails only where it is used* → T5 mandated shape; *a two-layer seam is
  broken across it* → T3 on a real guarded adapter + real adjacency bucket, ordered by channels; *a negative test needs its positive
  vector and every fix is proven by revert* → T3's mutation, T1's "no write" beside a "writes when different" vector; *a zero
  reading indistinguishable from not-measured* → `AppliedSeq()==0` refuses (fail-closed), counters are monotone and carried;
  *making a fault standing* → read failures are a gauge, never a latch; *a fixture that arranges the favourable order is an
  argument* → T3 drives `Reproject` against a CDC withhold in production order (token captured before evaluation).
- Standing checklist (`agents/fire-brief-template.md`): lifetimes for the three new atomics are §5's table; every census re-run
  (§15.2); revert-prove T1/T3/T11/T12; the replaced mechanism's obligations are §4.4's two axes — both accounted; one writer per key
  unchanged; the mirrored `PeakBindingRows` path carries no debt this fire inherits.

### 15.6 Adjacent finds / non-goals

Scouts surfaced no new find; the S-wrong adjacency bug is already the board's *[Refractor] `adjacency.upsertEdge` … no ordering
guard* row (§13). Non-goals = §4.7 (doc-mode actorAggregate, plain, personal, the actor set per event).
Scope-diff gate: every §15.2 touch traces to §4.1–§4.6; nothing widens §4; dependencies re-verified — the `WITH`-scope Inc 2 and
the delta design's close are independent both ways (§11).

### 15.7 Checkpoint

*(amended at close)*
