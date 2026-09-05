# Personal lens delta publication — publish the rows an event touched, frame what the actor holds

**Status: ✅ RATIFIED (Winston-adjudicated, per the 2026-08-20 delegation) — 2026-09-04.** No architectural fork,
no frozen-contract change, no client change (§0.1). **§12 adversarial pass ✅ RUN + all eleven findings FOLDED**
(3 blocking, 5 major, 3 minor; none deferred). No pre-build gate is open — **the Steward may build this now.**
Author: Winston (Designer fire, 2026-09-04).
**Component:** Refractor — `internal/refractor/{pipeline,grantchange,ruleengine/full,adapter}`; `cmd/refractor` wiring.
**Backlog row:** [lattice.md](../planning-artifacts/backlog/lattice.md) → *Edge & personal lenses* →
*[Refractor] A personal lens republishes its whole actor per event* (★★, L).
**Parent:** [personal-lens-whole-actor-cost-design.md](personal-lens-whole-actor-cost-design.md) §1.3 / §12
(the filing), whose premise this design corrects (§1.1).
**Extends:** [personal-lens-retraction-design.md](personal-lens-retraction-design.md) (the keyset frame — kept
unchanged), [personal-lens-grant-change-trigger-design.md](personal-lens-grant-change-trigger-design.md) §4.1/§4.3
(`ReprojectPersonalActor` + the standing healer — amended in §4.4), [personal-lens-derivation-licence-design.md](personal-lens-derivation-licence-design.md)
§4.4 (the licence whose clock conjunct this design reuses).
**Frozen-contract change: NONE** (§7). **Architectural fork: NONE** (§0.1). **Edge client change: NONE** (§4.7).

---

## 0. For Andrew (one-look block)

**What it does (two lines).** A personal lens evaluates a whole actor per event and then publishes *every* row of
that actor to the device — the changed one and the thousands that did not change — and its standing healer
republishes every actor's whole row set on every pass. This design keeps the whole-actor *evaluation* and the
ratified keyset *frame*, and publishes only the rows the event actually reached: the engine records, per row, the
vertices it read to produce that row (*row provenance*); the write loop sends a row iff its provenance meets the
event's vertices; the healer sends the frame on every pass and the rows once a day; a grant change sends the one
anchor's row. No new server state, no wire change, no client change.

**The row's premise was wrong, and the measurement that replaces it is in §1.** The filing said the widest actor's
subject holds "under three events" because each event republishes 3,638 rows. Live: that subject holds ~3,000
events (2 upserts + 1 frame each) — 40 of 40 sampled `providedTo` instances are **tombstoned**, so the engine walks
3,638 edges to publish 2 rows. The flood is elsewhere: **one lens, `edgeCatalog`, wrote 465 MB of the stream's
512 MB in 12 h**, 39 % of all messages are healer/drain passes republishing unchanged rows, the stream is pinned at
its byte cap so *every* actor keeps ~12 h of history instead of the vault's 24 h, and the audit stream holds one
hour.

**Two calls that deserve your eye, neither a fork:**

1. **The healer's per-pass output changes** from "rows + frame" to "frame; rows once per day". The grant-change
   trigger design (§4.3) defined the sweep as re-driving each identity through `ReprojectPersonalActor`, which
   publishes both. The licence's argument rests on the healer re-asking the two *inclusion* gates, and the frame is
   the inclusion signal; the daily content pass keeps a bounded content heal for a connected device (which never
   re-hydrates) at 1/17th of today's cost. §4.4, §8 row 3.
2. **The row's second candidate — a per-(lens, actor) publication memo — is rejected again**, on the vault's
   statelessness principle the retraction design already applied (§8 row 2, the Alternative A text quoted). What
   the memo would buy over provenance is elision of the per-event frame, which is 8 % of the bytes; the frame floor
   is named as the residual with its revive trigger (§9).

### 0.1 Fork / contract check — honest answer: neither

No Gateway, D1 read-path, Vault, multi-cell or HA-NATS surface. The wire carries the same four ops with the same
fields; the client's rules are untouched (§4.7). The delta envelope is a component-level surface
(`docs/components/refractor.md`), as the retraction design established. What changes is *which* upserts a
server publishes — unobservable against any contract text. Winston-adjudicated under the 2026-08-20 delegation;
§12 has run.

---

## 1. Problem

### 1.1 The row's claim, and what the stream actually holds

The filing (whole-actor cost design §1.3) reads: *"Each event re-publishes every row of the actor. The two
widest actors' SYNC subjects sit at the stream's 10,000-message per-subject cap (C5), i.e. under three events of
history, and the stream holds 736 k deleted messages against its 512 MiB cap. A device offline across three
events must re-hydrate."* The first sentence is true of the write loop (§2 row 3). The inference is false: the
composition probe (§3 C3) shows the widest subject's 10,000 messages are **3,069 distinct revisions** — 3,047 of
them carrying 3–10 messages — and the `edgeInstances` frames in that window carry **at most 2 keys**. The actor
has 3,638 `providedTo` links and ~2 live instances (§3 C5: 40/40 sampled bodies tombstoned). The engine evaluates
3,638 bindings per event (the parent design's cost, now 0.24 s) and publishes two rows.

### 1.2 The measured harm (live stack, 2026-09-04 01:20–01:35 PT, Refractor at `2d9a821e`)

| # | Harm | Measurement (§3) | Retired by |
|---|---|---|---|
| H1 | **The SYNC byte cap binds at ~12 h for every actor.** `MaxAge` is 24 h (the vault's ephemerality window); the stream sits at 512 MiB with its first sequence 12 h 21 min old. A quiet actor's device offline for 13 h re-hydrates because a *noisy lens* evicted its history. | C1: 367,196 msgs, 512 MiB, first seq 2026-09-03 13:07 → last 01:28 | Inc 1 + Inc 2 |
| H2 | **One lens is 91 % of the bytes.** `edgeCatalog` (`ZvZ3EmZvEvM6X9XPZvZ3`): 465 MB / 233,742 msgs. Its rows are ~2 KB (the descriptor vocabulary) and every actor's whole catalog (26–97 rows) is republished per reaching event. | C4 per-lens bytes; C3 (DbYG…): 4,183 upserts in 53 revisions ≈ 97 rows per evaluation | Inc 2 (live), Inc 1 (passes) |
| H3 | **39 % of all messages are whole-actor passes republishing unchanged content.** 1,555 (actor, revision) groups where ≥10 lenses published at once — the healer's pass or the drain — carry 104,941 upserts and 52,098 frames; every actor got 7–15 such passes in 12 h. | C4 `whole-actor` class; C3 (zyoC…): 270 of 288 messages are empty frames | Inc 1 |
| H4 | ~~The audit stream is one hour deep because of the republish~~ **Corrected at Inc 3 (2026-09-04): the audit trail's depth is NOT this item's harm.** `REFRACTOR_AUDIT` holds ~1.9 M entries / 512 MiB over ~1 h 45 min, but per-subject attribution (§3 C6, re-run 08:33 PT) puts 87 % on the generated auth-plane producer `edgeManifestStaffReadGrants` (`TRiLM4J9qRWHBcRbTRiL`, 1,627,110 entries; one appointment `.schedule` aspect event committed 4,915 rows in one second, 29,490 entries in two minutes ≈ 245 rows/s) and 10 % on `edgeManifestReadGrants`; the fifteen personal lenses hold ~100 entries each. The rate is the same before and after Inc 2 (C6 at 01:24 spanned one hour at the same fill). A harm filed in the republish's units was a different writer's bytes; filed on the lane as its own row. | C6 (attributed) | nothing here — T7's audit conjunct is withdrawn |
| H5 | **The per-subject count cap is an event count for a dense actor** — 3 msgs/event × ~6 events/min ⇒ 10,000 in 8 h 45 min on the widest subject. Scoping lowers it to 1–2 msgs/event; the per-event *frame* is the floor this design does not remove. | C3 (edu97…): span 8 h 45 min, 3,069 revisions | named residual, §9 |

**What is not the harm.** The consumer drain rate (the parent design's subject) is not: `edgeInstances` completes
an event in 0.24 s. The device's *store* is not: the client already drops a duplicate by revision and applies
frames exactly. The harm is bytes and message count on a capped, shared, time-boxed transport, plus the audit
amplification behind it.

### 1.3 Intent

The vault's own words (Edge Lattice/Personal Lens.md §3, *The Delta Projector*): *"It doesn't re-calculate the
whole graph; it only evaluates the specific Aspect that just moved against the active Interest Sets"* and §1:
*"pushes a tiny update packet to that user's unique stream."* The retraction design (§4) deferred the dedup lever
as *"needing exactly the server-side memory this design refuses — revive on a measured bandwidth driver"*. The
driver is measured. This design revives the lever **without** the memory: the evaluation already knows which
vertices each row was computed from.

---

## 2. Grounding ledger (verified in code this fire)

| # | Fact | Where | Bearing |
|---|---|---|---|
| 1 | The fan-out re-executes the whole personal cypher per enumerated actor, **never seeded** ("an actor reprojection … carries no proof that only one anchor's rows moved") | `pipeline/evaluate.go:1218-1284` (`reprojectActors`), `:1275-1278` | the evaluation stays whole-actor; this design scopes the *publication*, not the evaluation |
| 2 | The derivation seeds the actor walk from `(pattern position, vertex id)` — vertex/aspect via `PositionsBinding`, link via `AnchorSideSeeds` — and returns actor keys only | `pipeline/anchor_derivation.go:69-135`, `hopindex.go:949-1008` | the event's vertex set is known to the arm that calls `reprojectActors`; the positions are not needed once provenance is per row |
| 3 | `writeResults` writes **every** non-delete result through the adapter, then publishes one keyset frame per enumerated actor built from *all* results | `pipeline/results.go:33-170`, `:304`; `evaluate.go:1310-1350` (`emitPersonalFrames`) | the seam: filter what is *written*, keep what is *framed* |
| 4 | The frame is "complete, authoritative keys for (lens, actor) as of revision"; the client prunes keys attributed to the lens at `Sources[L] ≤ F` and absent from the frame; an upsert below `frameHW[L]` for an unattributed key is dropped | `adapter/natssubject.go:357-380`; `edge/store/bolt.go:123-231`; `store.go:105-145` | an unchanged row keeps its old `Sources[L]` and stays present in every frame ⇒ never pruned; scoped publication needs no client rule |
| 5 | The client's `delete` clears **every** lens's attribution (not lens-attributed), and the personal `Delete` envelope carries no `lens`; nothing emits `delete` on a personal target after R1 | `store.go:127-133`, `bolt.go:176-193`; `natssubject.go:409-425` | a per-row delete on the personal wire would be a client change — this design emits none (§8 row 1) |
| 6 | `ReprojectPersonalActor` evaluates the actor, publishes every row, captures the revision **after**, publishes the frame; it is the drain's and the sweeper's single path | `pipeline/reproject_personal.go:143-243`; `grantchange/reprojector.go:621-663` | gains a publication scope (§4.3); revision/lock/frame contract untouched |
| 7 | The sweeper walks 5 identities per 60 s tick through `ReprojectNow`; its verdict counts `Attempted`/`Failed` reprojections | `grantchange/sweeper.go:32-33`, `:272-371` | the verdict does not depend on what a reprojection *published*; frames-only passes keep the licence's healer semantics |
| 8 | The grant-change edge enqueues the **actor only**: `GrantChanged(actorKey)`; the producer's per-entry key is `cap-read.<domain>.identity.<actorId>.<anchorId>` and `anchorFromKeyPerEntry` already splits the trailing entry NanoID off | `grantchange/reprojector.go:450-470`; `pipeline/grantchange.go:30-32`, `:185-199`; `projection/output.go:392-460` | the anchor whose grant moved is one `LastIndexByte('.')` away — the edge can carry it (§4.3) |
| 9 | The D1 gate decides a row on `row["anchor"]` (a vertex key) exactly; the Interest gate on `(anchorType, anchorID)` | `projection/personal.go:243-275`; `personalinterest/interest.go:330-346` | a grant change for anchor X affects exactly the rows with `anchor == X` — scope by the alias, no engine needed |
| 10 | The executor expands bindings by cloning (`cloneBinding`), fetches every candidate other-end (`fetchNode`) before admission, null-binds OPTIONAL misses, groups projection rows by non-aggregating items, merges multi-walk branches by output key | `full/executor.go:2064-2070`, `:700-830`, `:1641-1780`, `:1895-1945`; `full/rel_traverse.go:53-215`; `pipeline/branchmerge.go:60-120` | the provenance hook points (§4.1): clone, candidate fetch, grouping, branch merge |
| 11 | `EvalFootprint` is per **evaluation** (validation); `ProjectionResult{Key, Values, Delete}` and `EvalResult{Delete, Keys, Row, ProjectionSeq, FailClosed}` carry no per-row read set | `ruleengine/ruleengine.go:73-77`, `:79-100`; `ruleengine/eval_result.go:16-38` | one new field on each, threaded through `executeFullForActorOnce` |
| 12 | `personalClockRefusal` is derived at publication and refuses a lens whose row depends on `$now`/`$projectedAt`; no shipped personal lens references either | `pipeline/ruleinstall.go:413`; `anchor_derivation_personal.go:520-556`; `personal_derivation_corpus_census_test.go` | the one input provenance cannot see; scoping is armed only when it is empty (§4.2) |
| 13 | SYNC limits: `MaxAge` 24 h, `MaxMsgsPerSubject` 10,000, `MaxBytes` 512 MiB, `DiscardOld` | `adapter/natssubject.go:114-138`, `:196-225`; C1 | the byte cap is stream-wide, so one lens's churn sets every actor's horizon (H1) |
| 14 | Every personal lens carries a `WITH` boundary; `edgeCatalog` carries one pattern comprehension (`viaServices`); the personal corpus is exactly the 15 `edge-manifest` lenses | §3 C7; `packages/edge-manifest/lenses.go:584-1210`, `:678` | provenance must survive `WITH` grouping and be recorded under comprehension reads (§4.1) — not gated per lens |
| 15 | `Reproject` refuses a personal target; a rebuild replays the KV stream per key (`DeliverLastPerSubject`) | `pipeline/reproject.go:401-420`; `rebuild.go:20-30` | the only whole-actor publishers are Hydrate, the drain and the sweeper; a rebuild is a union of scoped events and still reaches every row |
| 16 | Multi-walk personal lenses evaluate each branch for the actor and merge rows by key | `branchmerge.go:70-120` | provenance of a merged row = union of its branch rows' provenance |
| 17 | The 15 personal lenses are **licensed and deriving** live since 21:27 on 2026-09-03 | §3 C8 (refractor.log) | the event → actor narrowing is in place; this design is its row-level complement |

**Parallel-design check.** No 📐/🏗️ design touches the personal write path or the sweeper; the dirty tree at
fire start was clean. The `WITH`-scope row (`varlength-anchor-derivation-design.md` §13 Inc 2, 📋) concerns the
`edgeManifestReadGrants` *producers*, not the personal publish. The `capabilityEphemeral` `$now` row is on the
auth plane. Nothing hands work to this design; this design hands the parent's §1.3 correction back (§13).

---

## 3. Executable censuses (commands + the numbers pinned this fire)

All read-only, against the live dev stack (`nats --server=localhost:4222 --nkey=deploy/nkeys/lattice.nk`). The
two Go probes are in the fire's scratchpad and are reproduced as a test-only tool in Inc 3 (§11).

```sh
# C1 — the stream (2026-09-04 01:22 PT)
nats stream info SYNC
#   ⇒ Messages 366,778 · Bytes 512 MiB (AT the cap) · Deleted 1,138,442 · Subjects 177
#     First 218,417,665 @ 2026-09-03 13:07:36 → Last 219,922,884 @ 2026-09-04 01:22:26  (12 h 15 min, not 24 h)
#     Limits: Max Per Subject 10,000 · Max Bytes 512 MiB · Max Age 1d · Discard Old

# C2 — per-subject depth
nats stream subjects SYNC --json | <sort desc>
#   ⇒ 7 subjects at 10,000 (FZJz… LQ28… MQsm… dzst… edu97… mBLY… ocZv…), then 4,559 · 4,494 · 4,462 · 4,349 · 4,236
#     median 1,419 · sum 366,778

# C3 — composition of one subject (ordered consumer, DeliverAll, filter = the subject; bucket by op/lens/revision)
syncprobe lattice.sync.user.<actor> deploy/nkeys/lattice.nk
#   edu97ixj2CJB6auNi6L4 (the widest actor): 10,000 msgs · 4.5 MB · span 8 h 45 min
#     ops keyset 3,290 · upsert 6,710 · distinct revisions 3,069 · 3,047 revisions carry 3–10 msgs
#     edgeInstances (ZdEv…) 9,179 msgs, 3,060 frames, MAX FRAME 2 KEYS · edgeCatalog (ZvZ3…) 567 msgs, 21 frames, max 26 keys
#     the other 13 lenses: 15–30 msgs each, ALL frames, 0–1 keys
#   DbYG2W5111ow6WQD8EPC: 4,548 msgs · 7.3 MB · span 11 h 50 min · keyset 365 · upsert 4,183
#     edgeCatalog 4,203 msgs / 43 frames / max 97 keys ⇒ ~97 rows republished per evaluation; 53 revisions
#   zyoCZ9Nw6dQVLbkgqLXs: 288 msgs · keyset 270 · upsert 18 ⇒ 18 whole-actor passes × 15 empty frames

# C4 — stream-wide attribution by (actor, revision) group: ≥10 lenses at one revision = a whole-actor pass
syncprobe-all deploy/nkeys/lattice.nk
#   read 367,196 · span 12 h 21 min
#   whole-actor  groups 1,555  msgs 157,039  bytes 201,685,680  upserts 104,941  frames 52,098   (39 % msgs / 39 % bytes)
#   live         groups 21,733 msgs 210,157  bytes 310,214,830  upserts 187,913  frames 22,244   (61 % / 61 %)
#   whole-actor passes per actor: n=177 min 7 median 9 max 15
#   classifier error bar (re-run after the adversarial pass): a LIVE event reaches ≥10 lenses at one stream
#     sequence when it is an identity vertex/aspect event (fan-out relevance admits `identity` on all 15) or a link
#     whose relation ≥10 lenses traverse. On edgeInstances' processed log (62,619 events): identity vertex+aspect
#     events 18; identity-endpoint links 12,258, of which 12,252 are `providedTo` — relation-gated to the ≤3 lenses
#     that traverse it (C3: 3,047 of the widest subject's 3,069 revisions carry ≤10 msgs), the other 6 are
#     appliedToUnit/bookedBy. So the whole-actor class is healer/drain passes plus a residue bounded by the 13
#     revisions on the widest subject carrying >10 msgs in 8 h 45 min (≈ 1.5/h against ~1 healer pass/h). The
#     Inc-1/Inc-2 split below is stated to that precision; T7's acceptance does not use this classifier.
#   bytes by lens: edgeCatalog 465,011,879 (233,742 msgs) · edgeInstances 25,084,624 · edgeStaffPanes 4.9 M ·
#                  edgeIdentity 4.6 M · edgeStaffWorkOrders 2.9 M · edgeTasks 2.8 M · the other 9 ≤ 1.2 M each

# C5 — are the widest actor's 3,638 providedTo instances live?
nats kv ls core-kv | grep -E '^lnk\.service\.[^.]+\.providedTo\.identity\.edu97ixj2CJB6auNi6L4$' | awk 'NR%90==0' | head -40 \
  | xargs -P8 -I{} sh -c 'nats kv get core-kv vtx.service.{} --raw | grep -o "\"isDeleted\":[a-z]*"' | sort | uniq -c
#   ⇒ 40 "isDeleted":true   (0 live in the sample; the frames' ≤2 keys are the live remainder)

# C6 — the audit stream
nats stream info REFRACTOR_AUDIT
#   ⇒ Messages 1,991,308 · Bytes 512 MiB · First 2026-09-04 00:23:45 → Last 01:24:21   (ONE HOUR of trail)

# C7 — corpus constructs the provenance must survive (the 15 personal lenses; identity-domain has none)
grep -n "^MATCH\|WITH \|\[(\|NOT (\|exists(" packages/edge-manifest/lenses.go | grep -v "^\s*//"
#   ⇒ every tail opens with a WITH (14 sites: 584 647 809 837 887 916 937 974 1025 1054 1095 1145 1180 1210)
#     one pattern comprehension (:678 `[(op)<-[:permitsOperation]-(svc:service) | svc.key] AS viaServices`, edgeCatalog)
#     no existence predicates; required MATCH only at the two heads (:545 edgeIdentity, :726 opCatalog — plain)
#   $now / $projectedAt: none — pinned by `go test ./internal/refractor/ -run TestCorpusPersonalDerivation`

# C8 — the licence is granted live (a granted licence logs once per transition)
grep -c "personal-lens derivation licensed" refractor.log   # ⇒ 98 (restarts × 15); last transition 2026-09-03 21:27:09 for all 15
```

---

## 4. The shape

Three publishers reach a device's subject — the CDC write loop, `ReprojectPersonalActor` (drain + sweeper) and
`Hydrate`. Each gains a **publication scope**; the frame is published exactly as today. One rule, stated once:

> **A row is published iff the scope admits it; every surviving row is framed.** `ScopeAll` (hydrate, interest
> change, the daily content pass, an unlicensed lens) is today's behaviour. `ScopeNone` (the healer's ordinary pass)
> publishes the frame alone. `ScopeVertices(V)` (the CDC path) admits a row whose provenance meets `V`.
> `ScopeAnchors(A)` (a grant change) admits a row whose `anchor` alias names a NanoID in `A`. *(Build, 2026-09-04:
> `A` is a set of valid NanoIDs — blank or malformed tokens are dropped — and an `A` that is EMPTY is `ScopeAll`, never
> "admit nothing"; a row with no or an unparseable `anchor` is not admitted under `ScopeAnchors`, a branch
> `personalEnvelopeFn` already makes unreachable. The same rule binds `ScopeVertices(V)`: `V` is a set of Contract #1
> vertex keys, a non-vertex token is dropped, a `V` that is empty — originally or after the filter — is `ScopeAll`, never
> "admit nothing", and more than `MaxScopedAnchors` distinct keys is `ScopeAll`; a row carrying NO provenance is admitted
> under `ScopeVertices`, so an engine or path that records nothing reproduces today's publication.)*
> **The zero value is `ScopeAll`** — a caller that forgets to set one reproduces today's behaviour (over-publish:
> bytes, never a wrong row); because that failure is silent on the wire, T3 pins every scope-bearing call site.

### 4.1 Row provenance (engine — Inc 2)

**Definition.** A row's provenance is the set of Core-KV **vertex keys** whose root body, aspects or adjacency the
evaluation read while producing that row — the vertices bound in the row's bindings, every candidate the walk
fetched and rejected on the way to those bindings (label mismatch, tombstone, `WHERE` false, OPTIONAL miss), every
vertex a `WHERE`, projection item, pattern comprehension or existence predicate dereferenced while evaluating those
bindings, and — for a multi-walk lens — **the whole read set of every branch that produced no row for that key**.
Aspect and link keys fold to their parent / endpoint vertices, because that is the granularity the CDC arms name
(§2 row 2).

**Invariant (what makes scoping exact).** If an event on vertex *v* changes a row's content or existence, then
*v* is in the row's provenance **as evaluated after the event**. Case by case: a body/aspect change on a bound
vertex — bound ⇒ recorded; on a rejected candidate (a `WHERE` that now admits it, a tombstone) — fetched ⇒
recorded; a link create — both endpoints are bound in the new binding; a link tombstone — the near endpoint stayed
bound or was fetched (it was reached before the removed hop), and the row that *lost* its far side still carries the
near one; a link tombstone on the actor→anchor hop — the row is gone and the **frame** retracts it, exactly as today;
**a walk-owned column that nulls because one branch of a multi-walk lens stopped producing the key** (the
adversarial pass's counterexample: `edgeCatalog`'s `viaRole` after a `grantedBy` tombstone — the staff branch yields
no row for the op, the base branch's row still exists with `viaRole` null, and the base branch never read `role`) —
the staff branch's *evaluation* did read `role` (it walked `role`'s adjacency to find no permission), and that
branch's read set is unioned into every key it did not produce. A row that disappears is never the scope's concern:
the frame carries omission. The one input outside provenance is the wall clock, which the licence already names
(§4.2).

**Grain (build, 2026-09-04).** Every candidate a hop fetches lands on the HEAD binding's chain, which every row that
hop produces inherits — so the anchors an actor fans out to at hop 1 are in each other's provenance, and an event on
one of them republishes all of them. Scoping bites BELOW the actor fan-out: `edgeCatalog`'s ~97 rows differ by their
per-template heads, `edgeInstances`' two by their anchors, and a one-hop lens gains nothing. T4's per-lens withheld
counts are the measure; per-hop precision (a candidate recorded on the child that admitted it, a rejected one on the
head) is the named narrowing if Inc 3 finds the grain binding (R8).

**Mechanism** (all inside `ruleengine/full`; the edit sites are enumerated because the pass found three the first
draft missed):

- `binding` gains a provenance chain under a reserved, non-variable key (`"\x00prov"`, never a Cypher identifier):
  a `*provNode{parent *provNode; merged []*provNode; keys []string}` appended on every `cloneBinding` so a child
  inherits its parent's reads by pointer, not by copy. *(Build, 2026-09-04: `merged` carries the several ancestors a
  grouped row folds together, and the chains a binding that will project no row hands to the bindings that stand in
  for it — `provAbsorb`: a head that expands to nothing hands its chain to the binding its siblings share; a whole walk
  that dies at a hop hands its frontiers to the binding it started from, which is what an OPTIONAL null binding clones;
  a `WHERE`-excluded expansion hands its chain to its source binding; and every CLAUSE that discards a binding — a
  required `MATCH` whose `WHERE` excludes every expansion of a source binding, a comma-separated pattern inside one
  `MATCH` whose later pattern admits nothing for an expansion of the earlier one, a `WITH`'s `WHERE`, a `WITH` or
  `RETURN` `DISTINCT` — keeps one per-clause stage node, absorbs the discarded binding's chain into it and, when anything was
  discarded, links the stage into every row the clause projects. The stage, not a parent hand-off, is the smallest
  sound unit: a binding consumed by an expansion two clauses up has no surviving descendant for a parent hand-off to
  reach (the reviewer's counterexample: a city whose `country` flips under `MATCH (o)-[:in]->(c:city) WHERE c.country
  = "EU" … count(o)` reached no surviving row and the count was withheld). The deliberate imprecision: a vertex only a
  discarded row read republishes every surviving row of that clause — bytes, never a wrong row — because which survivor
  stands in for a discarded row is not knowable when an aggregate may fold any of them. The fold walks `parent` and
  `merged` post-order over a DAG that may hold a back-link to an ancestor, with a lowlink guard; a node whose fold cut
  back to something shallower than itself is not memoized.)*
- **Record sites — the closed list.** (1) `seedNodes` (`seed_nodes.go:21-130`): the point-candidate arm and the
  scan arm both `fetchNode` and reject by label/props; every fetch lands on the head binding's chain. A **scan-seeded
  pattern's row also depends on the membership of `vtx.<label>.`**, which no per-vertex set can name — so scoping
  requires the compiled anchor to be point-seeded by `$actorKey` (`HopIndex.Anchor ≥ 0`, §4.2's second conjunct;
  every corpus lens is). (2) **Traversal** (`rel_traverse.go:53-215`): the frontier node and every candidate
  other-end `fetchNode` serves — admitted, tombstoned or rejected — land on the head's chain; hop ≥ 2 frontiers on
  the same head (a rejected candidate belongs to *every* child of this head). (3) **`applyMatch`'s own `WHERE`**
  (`executor.go:626`) and **bound-head checks** (`:762-790`) evaluate under a *current-binding cursor*.
  (4) **Projection-time reads** (`projectItems` both arms, `applyWith`'s `WHERE`, `applyReturn`): the cursor is set
  while one binding's items evaluate, and `fetchNode` appends every key it serves — **memo and
  staging hits included** *(build: `fetchEdges` records nothing itself — an adjacency NodeID is a bare NanoID and names
  no vertex; its single caller, `traverseRel`, records the frontier node's own vertex key before the adjacency read)* (`executor.go:930-953` promotes a staged entry through `fetchNode`, so no read goes around
  the cursor; `resolveProperty`'s aspect and link hops route through it, `values.go:57,73`). **The cursor takes
  precedence over the head-chain rule whenever one is set**: a pattern comprehension or existence predicate runs
  `matchPath` under the cursor, so a multi-hop comprehension's inner clones (`rel_traverse.go:244`, discarded with the
  comprehension) still record onto the row that evaluated it. (5) **`projectItems`' non-aggregating arm**
  (`executor.go:1681`, `nb := binding{}`) carries the chain into `nb` — without this the whole traversal's provenance
  is discarded at the first `WITH`, which is every one of the fourteen tails (C7). (6) **Grouping** (aggregating arm,
  `:1761`): the group's output binding's chain is the union of its members' chains. (7) **Branch merge**
  (`branchmerge.go:107-125`): `executeBranches` keeps one *branch read set* per branch evaluation (every key that
  branch's executor fetched — the footprint's key set without revisions); a merged row's provenance = ∪ of its
  branch rows' provenance ∪ the read set of every branch that produced **no row for that key** — *and (build) of every
  branch that produced a row carrying no provenance of its own*, because reading "recorded nothing" as "read nothing"
  is the withholding direction; the read set is a genuine superset of any row's reads (the footprint is built from the
  node memo, which holds rejects and misses too). Shared by pointer, flattened once. This lives in
  `pipeline/branchmerge.go` (`executeBranches` / `mergeBranchRows`), not in `ruleengine/full`.
- **Where the reserved key must be stripped — three sites, each a wire or semantics leak otherwise:**
  `applyWith`'s `DISTINCT` (`executor.go:1595`) and `applyReturn`'s `DISTINCT` (`:1919`) render the **whole map**
  through `normalizeForKey` — two rows differing only in provenance would stop deduplicating; `applyReturn`'s values
  copy (`:1924-1928`) would put `"\x00prov"` into `ProjectionResult.Values` and `natssubject.go:323` would publish it
  in `data`. A `stripProvenance(row)` helper at all three, pinned by a test that grepd the wire. *(Build: `DISTINCT`
  keeps the first of a duplicate group; the dropped row's chain is absorbed into the clause's stage node, which every
  surviving row of that clause then carries, so an aggregate downstream of a `WITH DISTINCT` still names what the
  dropped member read.)*
- **Result** (`applyReturn`): `ProjectionResult.Provenance []string` — the chain flattened, deduplicated, sorted;
  keys are folded to vertex keys **at record time** (`appendProvVertexKeys`), so the chain holds vertex keys already.
  The fold is memoized per `*provNode` for every node the walk visits (post-order), so a head chain shared by many
  output rows folds once. The pipeline copies it onto `EvalResult.Provenance`.
- **Cost.** One pointer per clone, one slice append per fetch, one fold per output row over the part of its chain no
  earlier row folded. Two shapes bound it: the widest head (`edgeInstances`: 3,638 candidate fetches on one chain, 2
  output rows) and the widest row set over one head (`edgeCatalog`: ~97 rows). T2 pins cumulative allocation against
  the same executor driven from an unchained root on BOTH fixtures, within 2×.
- **The read-free executor** (`AnchorProjectionKey`, `coreKV == nil`) records nothing — it fetches nothing.

**Why provenance and not the seeds.** The derivation's `(position, id)` seed says which *actor* an event moved,
not which *row*: for `edgeCatalog` a template event seeds the `tpl` position and reaches every resident of the
template's containers, and inside each actor it is one row of ~97. Per-row provenance is the only thing that
answers "which of these rows read that vertex" without re-deriving the pattern per row (the seeded-evaluation
alternative, §8 row 1, does exactly that re-derivation and needs an engine pin, a closure analysis and a delete
transport to be exact).

### 4.2 The CDC write loop (pipeline — Inc 2)

- **Scope producers, all four:** vertex arm `{entry.CoreKVKey}`; aspect arm `{parentVtx}`; link arm
  `{srcVtx, dstVtx}`; the actor's own vertex path `{actorKey}`. The actor-own path has two cases
  (`evaluate.go:180-196, 255-345`): with no peer anchors it frames nothing and `{actorKey}` admits every row (the
  actor is bound in every binding — byte-identical to today); with `peerAnchorsEnabled` the peers' rows are framed
  (`actorsTouchedWithPeers`) and `{actorKey}` correctly admits only the peer rows binding the event identity.
- **Threading.** The scope travels with `enumeratedActors` from the arm back up through `evaluateForEntryRaw` →
  `evaluateForEntry` → the five `writeResults` call sites (`dispatch.go:170, 203, 234, 318, 342`). That is the
  signature work Inc 2 carries; the two plain-lens sites pass `ScopeAll`. *(Build: `dispositionEvalErr` takes no
  scope — it Naks, DLQs or enqueues an actor reprojection and publishes no row.)*
- `writeResults` (`results.go:33-170`): for a pipeline whose adapter is a `KeySetPublisher`, a non-delete result is
  **written iff** `scope.Admits(result)`; a declined result is neither written, audited nor counted toward the
  freshness clock — it is unchanged on the device. **Every** non-delete result still feeds `emitPersonalFrames`
  (`:304`). A plain or auth-plane pipeline is untouched (`ScopeAll` by construction).
- **Three eligibility conjuncts, read off `ruleState` per event; any failing ⇒ `ScopeAll`:**
  (i) `rs.personalClockRefusal == ""` — a lens whose row depends on `$now` / `$projectedAt` changes with no vertex
  changing (no shipped lens trips it, C7; `personal_derivation_corpus_census_test.go` is the gate); (ii) the compiled
  anchor is point-seeded by `$actorKey` — *(build)* `HopIndex.Anchor ≥ 0 && Anchor < len(Labels)` on every branch,
  the existence test because the zero-value index has `Anchor == 0`, a real position, and the sentinel alone would
  license a lens whose pattern graph was never derived in the withholding direction; a refused branch set is no answer
  and refuses. The scan-seeded case is kept for cost, not soundness: the post-event scan lists and fetches a new
  member and `fetchNode` records it first, but a scan puts the whole type on every row's provenance, so the scope would
  admit everything and the whole-actor publish is byte-identical and cheaper to decide. (iii) *(build, from the
  engine review)* no pattern position expands a label sigil (`*`): the taxonomy closure that decides which vertices
  such a position binds is resolved outside the executor and its `vtx.meta.*` vertices never enter a row's provenance,
  while an aggregate over the position changes when a type joins or leaves. No shipped personal lens uses `*`.
  The refusal reason is logged once per lens and again whenever it changes across a hot reload. The derivation *licence* is **not** a conjunct: scoping decides rows within an
  actor the enumerator or the derivation already selected, and provenance is a property of the evaluation, not of
  how the actor was found.
- **The freshness clock counts real output** (rewritten at build, 2026-09-04 — the first draft said "output, not
  rows" and that "nothing alarms on that clock"; `LensProjectionStalled` reads it, so the negative claim was a coverage
  claim, dossier fourth sighting): `lastProjectedAt` (published on the lens's health entry,
  `internal/refractor/health/lattice_heartbeater.go:1681`) advances on a committed row write, on a `Hydrate`, and on a
  SIGNALLED reprojection's frame (a drain signal, an interest change, the content cycle — the frame is the whole answer
  when the admitted set is empty). It does **not** advance on the healer's `ScopeNone` pass and **not** on the CDC
  write loop's per-event frame: a frames-only event is not output, and stamping it would turn the one signal that
  catches a lens silently withholding every row (a missed record site) into an event heartbeat. A lens whose events
  all withhold for the stall window therefore reads as stalled — the visible direction.
- **The retry replay is unreachable on a personal pipeline, and stays so by construction.** `enqueueRetry` replays a
  captured row at its **original, lower** `ProjectionSeq` (`results.go:352-375`); once a later event's frame has
  advanced the client's `frameHW`, that replay is dropped for an unattributed key — today the later event's
  whole-actor republish masked it, after scoping nothing would. No `edge-manifest` lens configures `Retry` (C7
  addendum: `grep -n Retry packages/edge-manifest/*.go` ⇒ none), so the path is unreachable; Inc 2 makes that a
  refusal at `InstallPersonalLens` (a personal lens with a retry queue is refused) and pins it in the corpus census.
  Retry / DLQ / Nak dispositions are otherwise unchanged: a declined result never enters `retryResults` or
  `terminalErrs`, and a Nak'd redelivery re-evaluates and re-scopes (idempotent).

### 4.3 `ReprojectPersonalActor(ctx, id, scope)` (pipeline + grantchange — Inc 1)

- The signature gains `scope`; the body publishes a row iff `scope.Admits`, then the frame at the after-capture
  revision under the same lock (§2 row 6). `Hydrate` keeps its own loop (`ScopeAll`, before-capture revision — but
  see §4.6).
- **The grant-change edge carries the anchor — through the injected inverse, never a new import.** `pipeline`
  cannot import `projection` (`projection/driver.go:12` and `personal.go:13` import `pipeline`; `go list -deps
  ./internal/refractor/pipeline | grep -c refractor/projection` ⇒ 0 — the first draft's `OutputDescriptor.EntryFromKey`
  call from `notifyGrantChangeSignalled` was a cycle). `SetGrantChangeSink(sink, inverse)` widens its closure to
  `func(targetKey string) (actorKey, entryID string, ok bool)`, bound at `driver.go:690` from the descriptor's
  `anchorFromKeyPerEntry` split (`output.go:443-460`); `GrantChangeSink.GrantChanged(actorKey, entryID string)`.
- **The three producers of `GrantChanged`, and what each passes:** `notifyGrantChangeSignalled`
  (`grantchange.go:185-217`) — the per-entry key's trailing NanoID (every generated cap-read producer is per-entry:
  `internal/pkgmgr/anchorwalk.go:501-521` sets `EntryKeyColumn: "anchorId"`); `truncateTarget` (`:263-272`) — the
  same, once per purged key; `notifyActorGrantChange` (`:242-247`, the out-of-band shred) — `""`, read as
  `ScopeAll`. The legacy parent-document Delete (`evaluate.go:953-957`) does not reach the sink today (its key
  fails `anchorFromKeyPerEntry`) and is the healer's, unchanged. `InterestChanged` enqueues `ScopeAll`.
- **The dirty set coalesces scope.** `dirty map[actorID]publishScope`; merge law: `All ⊔ x = All`;
  `None ⊔ x = x`; `Anchors(A) ⊔ Anchors(B) = Anchors(A ∪ B)` while `|A ∪ B| ≤ 64`, else `All`. `take` hands the scope to
  `reprojectActor`, which applies it on every registered lens. The bound and the drop accounting are unchanged.
- **Scope match for anchors:** `nanoIdFromKey(row["anchor"]) ∈ A` — the same alias the D1 gate decides on (§2
  row 9). Exact: a grant for anchor X changes the inclusion of exactly the rows anchored at X, and nothing else's
  content.

### 4.4 The standing healer (grantchange — Inc 1)

- **Every pass publishes the frame only** (`ScopeNone`). Nothing reads what a pass published: the licence's
  conjunct 3 is a *liveness* statement ("a standing healer is turning over a registry this lens is in",
  `anchor_derivation_personal.go:412-426` — deliberately not a coverage claim), the verdict counts
  `Attempted`/`Failed` from `ReprojectNow` alone (`sweeper.go:340-372`), and neither `publishProgress` nor any
  health surface distinguishes a row from a frame. The frame is the product of both inclusion gates, so the sweep
  still re-asks exactly what the licence design put it there to re-ask.
- **Once per day the cycle is a content cycle** (`ScopeAll`): the sweeper keeps `lastContentCycleStart time.Time`,
  latched at `ensurePopulation`'s re-list (`sweeper.go:524-529` — the site that actually starts a cycle; `claim()`'s
  wrap stamps the *end*), when the previous latch plus the projected cycle length reaches `PersonalContentHealInterval = 24 h` (build
  correction below); the zero value
  makes the first cycle after boot a content cycle. It is the bounded answer to a connected device that never
  re-hydrates: a row whose upsert was dropped or whose event the derivation missed is republished within a day, at
  one whole-actor republish per actor per day (the measured passes are 7–15 per 12 h).
- **What a lost signal costs — corrected at build (2026-09-04, three cold reviewers):** the frame carries inclusion
  REMOVAL only. A key the frame names but the device does not hold is ignored by the client store (*"its row arrives
  as a separate upsert"*), so a lost or failed grant-ADD signal — `TransitionUnknown`, an unclaimed key, a `maxDirty`
  drop, a failed drain reprojection (its lens raises `RecordGrantReprojectIssue`), a producer installed with no sink, a
  lens registering after its actors were swept — converges on the **content cycle** (≤ `PersonalContentHealInterval`,
  or a hydrate), where it converged within one sweep cycle before. Revocation — the over-grant direction the sweep
  exists for — still converges on the next frame. The drain keeps its no-re-enqueue posture (a persistent fault would
  spin it); the health fault is the signal. Named as R7.
- **The latch measures against the projected cycle END** (build correction): with cycle length `T = ⌈N/batch⌉ ×
  interval`, a cycle starting now is a content cycle iff the previous latch is zero or `elapsed + T ≥ interval` — the
  heal runs at least once per interval and at most once per cycle. A cycle longer than the interval (≈ 7,200 identities
  at the shipped 5/min) makes every cycle a content cycle and the frames-only saving nil; the content-cycle log carries
  the elapsed time and `T` so R4 is observable.
- **The healer's frames-only pass does not stamp `lastProjectedAt`** (build correction to §4.2's "nothing alarms on
  that clock": `LensProjectionStalled` reads it). A signalled reprojection (drain, interest change, content cycle) and
  a hydrate stamp it on their frame; a `ScopeNone` pass is not output.

### 4.5 Rebuild, reload — the replay publishes nothing; completion is one content cycle (Inc 4)

**The original clause — "a rebuild is a union of scoped events and still reaches every row" — was falsified live
on 2026-09-04 and is struck (amended 2026-09-05, Inc 4).** A rebuild resets the lens's durable to
`DeliverLastPerSubject` (`pipeline/rebuild.go`, `ResetAwaitReopen`) and replays every Core KV entry at its
*original* revision through the CDC write loop, which scopes each to `ScopeVertices` exactly as a live event. Two
facts make that union the item's largest harm, not a free rebuild: (1) under R8 (§9) an `edgeCatalog` row carries
its non-producing walks' whole read surface, so a replayed permission-grant or role link admits *every* row of the
actor — the `edgeCatalog` spec update of 11:34 PT published 26 rows × 277 revisions on the widest actor and ≈ 249 k
messages / 467 MB across 177 actors in 75 min, 93 % of everything SYNC held; (2) every replayed revision is *below*
the high-water mark a connected device already holds (the replay runs from 2026-08-26 while the live head is at
497 k), so the client's `frameHW` guard drops all of it — the flood reached no device. The design's rule is
unchanged; the scope during a rebuild is what changes: **while the pipeline's rebuild is in flight, the personal
write loop publishes neither rows nor frame for any actor** (nothing on the wire is nothing to drop), and **rebuild
completion requests one sweeper content cycle** — the mechanism the boot latch already uses (`ensurePopulation`'s
`firstSinceBoot`), reached through an injected sink on the pipeline (the §4.3 precedent), so the device receives
the rebuilt shape once, at a live revision, within one sweep cadence instead of within a day. A rule *reload*
(spec update without rebuild) republishes nothing by itself either; the next scoped event, the content pass or a
hydrate carries the changed shape.

**Bounds and exclusions the Inc 4 review pinned (2026-09-05):**

- *Every close of a silent span asks.* The flag is raised before the health write, the consumer reset and the
  truncate, so an abandoned rebuild (a reset that fails) also silences a span of CDC events; the ask is issued
  wherever the *owned* end of the span is decided — completion and abandon alike — exactly once per span, and a
  superseded finisher asks nothing.
- *A personal lens with secure columns is refused by the class-key erasure ceremony, loudly.* That ceremony
  rebuilds a target with `truncate=false` so the replay upserts null over retained rows — on a personal target the
  replay upserts nothing, and an attestation over it would be recorded over plaintext still on the SYNC stream and
  the device. No corpus personal lens declares a secure column today; the refusal is a named error at the erasure
  manager, so the first Secure Personal Lens design owns the attestation shape (its erasure must await the content
  cycle at a live revision, not the replay) rather than inheriting a false clean.
- *The request is spent by the cycle it buys, failures included.* A content cycle that fails a reprojection reports
  it through R7's fault path; it does not re-arm the request — a permanently failing actor would otherwise buy a
  content cycle every cycle, and the daily clock is the backstop.
- *"At a live revision" is bounded by the ordering token.* The content cycle publishes at `LastAppliedSeq`, which the
  replay rewinds and which a rebuild that also *narrows* the consumer filter can leave below the device's
  `frameHW` (the last matching entry older than the head the device saw) — the device drops that frame and the
  rows die on the resurrection guard until the next event or hydrate. Pre-existing (a hydrate has the same bound);
  named here because §4.5's convergence rests on it.
- *Concurrent windows are counted, not named.* A second rebuild that begins and abandons while an earlier rescan is
  live must not close the window (the flood would return and its ask would be spent at a rewound revision); the
  flag is a count of live windows, the ask fires on the 1 → 0 transition, and the health `active` write keeps its
  ownership rule.
- *The rule's second exception is pre-existing and now named.* The actor-own no-peer arm returns an empty actor
  enumeration and publishes nothing for that event (§4.2) — a row's provenance carries the actor key there, so the
  next scoped event admits it; Silent is the only exception that withholds a frame a pass would otherwise emit.
- *The standing healer's passes and the drain on a rebuilding lens are silent too (measured 2026-09-05 10:52 PT).*
  A content cycle that reached the widest actor during a live rebuild published 26 rows + a frame on the rebuilding
  lens at its rewound cursor (revision 3,381 against a live head of 497,393) while the other fourteen lenses
  published at live revisions — every device dropped the rewound pass, ≈ 9 MB per rebuild for nothing.
  `ReprojectPersonalActor` on a pipeline whose window is open therefore scopes to Silent whatever the caller asked,
  counted as attempted-and-succeeded; the window's close asks for the cycle that lands.
- *A window a restart resumes is open before the consumer delivers (measured 2026-09-05 11:52 PT).* `Run` added the
  consumer and only then read the health status that reopens an interrupted window, so the first replayed event after a
  restart was scoped `vertices`, fanned out through the enumerator over 135 actors and published 742 messages at one
  rewound revision; every later event was silent. The resumed window opens before the consumer exists; the completion
  watcher starts after it.
- *A personal target's rebuild has no stored effect at all.* `NatsSubjectAdapter` is neither a `Truncater` nor
  `Guarded`, so the truncate warns-and-skips; with the replay silent, what a personal rebuild still buys is the
  adjacency re-apply and the recomputed consumer filter — the SYNC stream is repaired by the content cycle, never
  by the rebuild.

### 4.6 Hydrate — the race scoping unmasks, and the two guards that close it (Inc 2)

`Hydrate` captures `highWater` **before** evaluating and publishes rows and frame at it (`hydrate.go:55, :98, :122`);
live CDC frames deliberately stay outside the (lens, actor) lock (`reproject_personal.go:38-43`). A live event at
`S > highWater` that frames mid-hydrate advances the client's `frameHW[L]` to `S`; every hydrate row for a key the
device does **not yet hold** then trips the resurrection guard (`bolt.go:135-141`: `revision < hw && !attributed` ⇒
dropped whole) and hydrate's own frame is dropped (`:205-208`). Today that is benign only because the same live
event republished the whole actor at `S`. After scoping it would leave a cold device with one row until the content
pass. Two guards, both required, both Inc 2:

1. **The write loop publishes `ScopeAll` for an actor whose publish slot a hydrate holds** — `writeResults` asks
   `p.hydrateInFlight(actorID)` (the `personalPublishLocks` entry taken by `Hydrate`, `pipeline.go:439-447`) per
   enumerated actor before choosing the scope. A hydrate that began after the check is covered by guard 2.
2. **Hydrate captures `highWater` only after the one event that was already past guard 1 has left** (rewritten at
   build, 2026-09-04: the first draft read `reporter.ActiveSequence()`, which is the RULE stream's cached sequence —
   `SetRuleSequence`, `health/reporter.go:110-147` — and says nothing about the CDC stream `highWater` is captured from;
   the mechanism as drafted could not have worked). The pipeline publishes the CDC sequence its consumer goroutine is
   INSIDE (`Pipeline.handlingSeq`, stamped by `handleTracked` before the handler runs and cleared by a deferred `leave`
   after the applied cursor has advanced, whatever the decision — one writer, the supervisor's sequential drain).
   After taking the slot and setting its mark, `Hydrate` reads it and, if non-zero, waits (`awaitHandlerLeft`, a
   subscribe-then-recheck on a progress channel under the same lock, bounded by the RPC's own ctx) for that handler to
   LEAVE — not for `LastAppliedSeq` to reach the sequence, because a Naked event never advances the cursor — then
   captures. An event applied before the capture is ≤ `highWater`; one that starts after the mark sees guard 1; at
   most one event can be in between, and it is the one named.

Interleavings (pinned in T3/T6): event applied before capture ⇒ `highWater ≥ S`; event in flight at capture ⇒
hydrate waits; event starts after capture ⇒ it sees the slot and publishes everything at `S` — the device holds
every row at `S`, hydrate's lower-revision rows and frame are dropped, and the live frame at `S` is authoritative.
The after-capture alternative (as `ReprojectPersonalActor` does) was rejected: an over-claiming hydrate frame would
prune a row a concurrent live event wrote — the retraction design's own reason for capturing before.

### 4.7 The client is untouched — the argument, not the assertion

For a row the client **already holds**: `ApplyUpsert` (`bolt.go:123-174`) is simply not re-sent; its `Revision`
and `Sources[L]` stay where the last real change put them. `ApplyKeySet` (`:197-231`, `collectAttributed`
`:284-303`): the frame lists the key ⇒ kept; the `frameHW[L]` advance affects only *future* upserts below it for
*unattributed* keys, and every row this design withholds is attributed. For a row the client **does not yet hold**
the only publisher below a live frame is `Hydrate`, and §4.6 closes it server-side. A changed row arrives at the
message's own sequence, above every frame that preceded it. The per-source monotonic guard and the dead-lens prune
are unaffected. `docs/components/edge.md` needs no edit.

### 4.8 Non-goals

- Narrowing the **evaluation** (the row's "sub-actor seeded evaluation") — §8 row 1; the engine cost is the parent
  design's retired harm.
- Eliding the per-event **frame** — §8 row 2 / §9 residual.
- Any change to the frame's semantics, the wire ops or fields, the D1/Interest gates, or the client store.
- `edgeCatalog`'s row size (~2 KB) — a package concern (§8 row 5), complementary.

---

## 5. New state, and its lifetime

| State | Created | Reset / dropped | Carried across | Ordered by |
|---|---|---|---|---|
| Binding provenance chain (`*provNode`) | per evaluation, per clone | with the evaluation's executor | nothing — never outlives `ExecuteWith` | n/a |
| `ProjectionResult.Provenance` / `EvalResult.Provenance` | at `applyReturn` | with the result slice (after `writeResults` / the frame) | a retry-queue capture copies the result, provenance included (the replay re-scopes identically) | n/a |
| Reprojector dirty-set scope (`map[actorID]publishScope`) | on `GrantChanged` / `InterestChanged` | on `take`; lost with the process (today's dirty set already is — the sweep covers it) | held through the registry-ready hold, merging as it waits | the merge law (§4.3); never by arrival |
| Sweeper `lastContentCycleStart` | first cycle after boot (zero ⇒ that cycle is a content cycle) | with the process — a restart costs one content pass per actor over the next cycle | n/a | the sweeper's own clock |
| Sweeper `contentCycle` (build, 2026-09-04) | latched with `lastContentCycleStart` at `ensurePopulation`'s re-list, read by every pass of that cycle | with the process; re-latched at the next re-list | held across the cycle's batches — a per-pass clock test would flip the moment the latch was stamped | the re-list |
| Sweeper `now` clock (build) | `time.Now` at construction; a test replaces it | n/a | n/a | n/a |
| Per-event scope (`ScopeVertices`) | in the fan-out arm | with the message | a Nak'd redelivery recomputes it from the same event | n/a |
| `Pipeline.handlingSeq` (build) | `enterHandling`, before every handler run | to 0 by the deferred `leave`, whatever the decision, after the applied cursor moved | nothing — it names one message | the consumer's sequential drain (one writer) |
| `Pipeline.progressChanged` (build) | lazily, by the first waiter in `awaitHandlerLeft` | closed and replaced on every move of `handlingSeq` / `lastAppliedSeq` | nothing; nil for a pipeline nobody hydrates | `progressMu` — subscribe-then-recheck under the lock the writer swaps it under |
| `actorPublishLock.hydrating` (build) | `markHydrating`, after `Hydrate` takes the slot | by the returned `unmark`, before the slot is released | one `Hydrate` call | `personalPublishMu` |
| `executor.provFolded` (build) | `newExecutor`; nil on the read-free executor | with the executor | nothing — never outlives `ExecuteWith` | n/a |
| Last-logged scope refusal reason (build) | with the pipeline | on a hot reload that changes the reason | the process | the rule lock |

No state is written to any KV, stream or file. The Refractor remembers nothing about what it sent (vault §4.1).

---

## 6. Reconciliation with the existing mental model

- *Didn't the retraction design already reject this?* It rejected a **server-side ledger** (its Alternative A) and
  deferred "a dedup/hashing optimization" because it "would require exactly the server-side memory this design
  refuses". Provenance is not memory: it is a by-product of the evaluation that already ran, discarded with it. The
  frame — that design's spine — is unchanged.
- *Does it contradict the grant-change trigger design §4.3?* It amends its per-pass output (rows + frame → frame;
  rows daily). The sweeper's job there is "re-drive each identity through `ReprojectPersonalActor`" so the two
  inclusion gates are re-asked; the frame is the product of both gates. §4.4 states the amendment; the design's
  banner gets a pointer at build.
- *Does it contradict the derivation licence?* No — it reuses its clock conjunct and stands beside its actor
  narrowing: the licence answers *which actors*, provenance answers *which rows*.
- *Is this the `EvalFootprint`?* Same reads, different grain: the footprint is one set per evaluation, consumed by
  validation on auth-plane actor-aggregate lenses only (`needsFootprintValidation`); provenance is per row, consumed
  by publication on personal lenses only. They share the read seams, not the data structure — a footprint keyed by
  row would make validation N× wider for nothing.
- *Does the parent design's measurement still stand?* Its **cost** attribution (gates, publishes, reads per binding)
  stands; its §1.3 **harm** attribution ("each event re-publishes every row") is corrected here (§1.1) and in that
  doc at build (§13).
- *New state we already keep somewhere?* The dirty set exists; it gains a value. Nothing else.

---

## 7. Contract surface

None. The delta envelope is a component-level surface (retraction design, "Frozen-contract change: NONE … the
delta envelope + frames are component-level surfaces per that design's ratified §4"). No op, field or ordering
promise changes; a device cannot observe against any text that a server omitted an upsert whose content it already
held. `docs/components/refractor.md` § *Personal Lens transport* gains the publication-scope rule and the healer's
two cadences; `docs/components/refractor.md`'s "Review keeps catching" dossier is copied into the fire brief (§11).

---

## 8. Alternatives

| # | Alternative | Verdict |
|---|---|---|
| 0 | **Do not have the thing — delete the whole-actor republish.** That is this design's row 0 *and* its recommendation: the mechanism removed is "write every result"; nothing is added on the wire, in the store or in the client. What stays is the frame, which is the retraction transport and is 8 % of today's bytes (C4: 74,342 frames of 367,196 msgs). | **Taken.** |
| 1 | **Sub-actor seeded evaluation + per-row delete** (the row's first candidate). Pin the event's pattern position in the executor, evaluate only the touched rows, emit a lens-attributed `delete` for a row that vanished. Exact only under a *closure* analysis (key columns over the pinned variable), needs a delete that the client today applies lens-agnostically (§2 row 5 — a client change), and a two-pass shape for multi-walk lenses (pin the key variable in every branch). Its payoff over provenance is the **evaluation** cost (already 0.24 s for the widest actor after the parent design) and the per-event frame. | Rejected now. Trigger: an actor whose subject is frame-bound (§9), or an evaluation cost the parent's three increments leave above 1 s. |
| 2 | **Per-(lens, actor) publication memo** (the row's second candidate): remember the last published body hash per key, send only diffs, elide the frame when the key set is unchanged. The retraction design, Alternative A, verbatim: *"Precise, but: violates the vault's named statelessness principle; O(identities × rows) mutable state with CAS churn per reprojection; the ledger itself can diverge from both the stream and the truth, demanding a second-order reconciliation (sweep-of-the-ledger); duplicates state the device durably holds."* All four hold unchanged. **In combination** with row 0 it would buy frame elision only — 8 % of bytes, and a per-subject count floor (§9) — at the cost of the invariant every personal-lens design has kept. | Rejected. |
| 3 | **Frames-only healer, no content pass.** Cheaper by one whole-actor pass per actor per day (~35 MB/day at this population). A permanently connected device never re-hydrates, so a lost upsert or a derivation miss would persist until the device's next cold start. The licence design put a standing healer behind inclusion for exactly this class of "un-signalled worst case"; content deserves the same bounded posture at a bounded cost. | Rejected (§4.4). |
| 4 | **Lower the sweep cadence instead.** Touches H3 only; the live share (61 %) is untouched, and the licence's staleness window (5 intervals) is tied to the cadence. | Rejected. |
| 5 | **Rewrite `edgeCatalog`** — split the descriptor vocabulary out of the per-actor row, or bound the catalog. A package fix that shrinks the ~2 KB row, complementary and worth filing on the Facet lane, but it does not remove the ×97 republish multiplier and leaves every other personal lens on the same write loop. | Not this design; file as a package row (§13). |
| 6 | **Raise the SYNC caps** (`MaxBytes`, `MaxMsgsPerSubject`). Hides the flood behind a bigger buffer; the audit amplification and the eviction of quiet actors' history by a noisy lens remain. | Rejected. |
| 7 | **Client-side content dedup** (drop a same-body newer revision). The bytes have already crossed the wire and the stream; the store's revision guard is the only thing it would relieve, and nothing is wrong with that guard. | Rejected. |

---

## 9. Risks and the named residual

| # | Risk | Direction | Mitigation |
|---|---|---|---|
| R1 | A read the row depends on is not recorded (a new executor read path added later without the cursor). | Stale content on the device until the daily content pass or a hydrate — under-display, never over-grant (inclusion is the frame's). | The differential exactness pin (§10, T4) runs the whole personal corpus under mutation and fails on any row whose content changed but whose provenance missed the mutated vertex; a new fetch site that bypasses `fetchNode`/`fetchEdges` fails it. |
| R2 | The provenance chain's memory on a wide head (3,638 rejected candidates recorded once, flattened per row). | Executor allocation. | Fold memoized per `*provNode` for every visited node; the allocation pin (§10, T2) on the `edgeInstances` fixture AND on a wide-head × many-rows fixture. |
| R8 (build) | The multi-walk merge's read-set substitution (§4.1 site 7) makes most `edgeCatalog` rows carry a non-producing walk's whole read surface, and the head-chain grain puts every candidate a hop fetched on every row that hop produced — so an event on anything those walks reached admits most rows. Correct (over-publish), but the retirement claimed for H2's live share was sized before this rule existed. | Bytes: the payoff on `edgeCatalog` may fall short of T7's ≥ 90 %. Never a withheld row. | T4 reports rows admitted / rows evaluated per lens and mutation kind so the fraction is a number, not a surprise; Inc 3's T7 measures on the shipped mechanism first (dossier: numbers about a replaced mechanism are not evidence). If it binds, the follow-on is per-hop precision (record a candidate on the child that admitted it, a rejected one on the head) — a narrowing with T4 as its gate. **Measured at T7 (2026-09-05): R8 binds only under the rebuild replay (§4.5, now suppressed); on the live path, 17 h post-rebuild across 177 actors, every live revision on the widest subject outside whole-actor passes carried ≤ 2 messages and the ops-event revisions admitted 0 rows — the per-hop narrowing is not built; its trigger, a live `grantedBy` / role-link write reaching every row of every holder, was measured 0 times in the window.** |
| R3 | The grant scope's anchor match by NanoID admits a row anchored at a same-id vertex of another type. | Over-publish of one row (harmless), never a skip. | NanoIDs are minted per vertex, 20 chars; a collision is a Contract #1 violation upstream. |
| R4 | The healer's content cycle never triggers because the population walk never wraps (a population larger than 5 × 1,440 = 7,200 identities per day). | The daily content heal degrades to "once per full cycle" — ~33 h at 10k identities, the cadence the trigger design already accepted for the un-signalled worst case. | Stated; the sweeper logs the content-cycle start with its cycle length. |
| R6 | A hydrate row the client's resurrection guard drops is no longer repaired by the next event's whole-actor republish. | A cold device short of rows until the content pass. | §4.6's two guards (pinned T3/T6); the exposure that remains is a hydrate whose RPC ctx expires while waiting on an in-flight event — it fails loud and the device re-attaches. |
| R5 | A device relying on the sweep's row republish to repair a store it corrupted itself. | Repaired within a day instead of ~80 min. | The store is a disposable cache; `Rehydrate` is the operator/client remedy the vault names. |
| R7 (build) | A lost or failed grant-ADD signal's row reaches a connected device only on the content cycle (§4.4). | Under-display ≤ `PersonalContentHealInterval`; never over-grant. | The drain's health fault names the failed reprojection; the frame retracts on every pass; a re-attach hydrates. |
| R9 (build) | **The silent window has no bound of its own.** `rebuildInFlight` silences every personal publication; a completion watcher whose `OutstandingForConsumer` errors forever would hold it open with the wedge detector reading "unknown". | Total publication silence on every device of that lens, unbounded. | Inc 4: the rebuild's progress is stamped at window open, so a started rebuild is never "unknown" and `evalRebuildWedged` fires on staleness; `lint-flag-consumer-census` refuses an undeclared reader of the flag. |
| R10 (build) | **A hydrate during a rebuild window publishes at a rewound revision.** `Hydrate` captures `LastAppliedSeq`, which the replay rewinds; a device re-attaching mid-rebuild has its rows and frame dropped by its own guards and `PublishHydrationComplete` still fires. | The device keeps its pre-rebuild truth (retained, never over-granting) for the rebuild's duration plus one sweep cycle — the bound every connected device has during a rebuild. A fresh device (no high-water) lands the hydrate. | Named, not built: refusing hydrates for a 75-minute replay was rejected as the worse outcome; the post-rebuild content cycle is the convergence for both. |
| **Residual** | **The per-event frame is the count floor.** After scoping, a dense actor publishes 1–2 messages per event; the widest subject's ~6 events/min is 8,640/day against a 10,000 cap sized as a backstop to the 24 h `MaxAge`. Not a byte problem (a 2-key frame is ~150 B). | A device on such an actor offline > ~28 h re-hydrates anyway (`MaxAge`); the cap binds only past ~7 events/min. | Revive trigger for row 1/row 2: a subject at the count cap whose messages are ≥ 50 % frames, measured by the Inc 3 probe. Re-deriving `syncStreamMaxMsgsPerSubject` as an *event* count is a one-constant follow-on once the count is a count of events. |

---

## 10. Test strategy

- **T1 — engine unit (`ruleengine/full`):** provenance holds (a) every bound node, including the `$actorKey`
  point-seeded head; (b) a rejected candidate (label mismatch, tombstoned other-end, `WHERE` false in `applyMatch`
  and in `applyWith`, OPTIONAL miss); (c) a projection-time aspect dereference of a bound node, served from the memo
  and from the staging map; (d) the vertices walked by a single-hop and a **multi-hop** pattern comprehension and by
  an existence predicate; (e) survival through a **non-aggregating** `WITH` and union through an aggregating one and
  through `RETURN` grouping; (f) union across multi-walk branches merged by key, **including a branch that produced no
  row for the key** (its read set is unioned); (g) the read-free executor records nothing; (h) `DISTINCT` at both
  sites and `ProjectionResult.Values` carry no reserved key (a wire-grep vector on `natssubject`'s envelope). Each
  vector asserts by vertex key, not by count.
- **T2 — engine allocation pin:** the `edgeInstances`-shaped fixture (one actor, N tombstoned + 2 live neighbours,
  N = 4,000) and an `edgeCatalog`-shaped one (a wide head, ~100 output rows) each evaluate within 2× the cumulative
  allocation of the same executor driven from an unchained root (the in-tree baseline: a root with no chain makes
  every clone inherit nothing and every read record nowhere).
- **T3 — pipeline scoping (`pipeline`):** `writeResults` under `ScopeVertices` writes exactly the admitted results,
  frames *all* results, audits and counts only the written ones; the four scope producers (vertex / aspect / link /
  actor-own with and without peers) hand the right vertex set and **each of the three personal `writeResults` call
  sites** carries its arm's own key set while the two plain arms hand `ScopeAll` (a plain lens's target publishes no
  frame); the scope is asserted under the production JSON log handler, not only the text one; `personalClockRefusal != ""` or a scan-seeded anchor ⇒
  `ScopeAll`; a non-`KeySetPublisher` adapter ⇒ unchanged behaviour byte-for-byte (a plain-lens vector runs both ways
  and compares the adapter's call list); a personal lens declaring `Retry` is refused at install; a signalled reprojection's frame advances
  `lastProjectedAt`, a `ScopeNone` pass's does not, and a frames-only CDC event's does not; **the hydrate race** — the three interleavings of §4.6 against a scripted in-flight event,
  asserting the device ends with every row.
- **T4 — differential exactness pin (corpus, `internal/refractor`):** for every personal lens **as composed**
  (its branch set through `executeBranches`/`mergeBranchRows`, never `forEachCorpusCypher`'s per-branch visit —
  `label_derivation_corpus_census_test.go:576-583` visits branches one at a time and cannot see the merge) and a
  seeded fixture graph, for each mutation kind (vertex body, aspect, link create, link tombstone, vertex tombstone)
  on each vertex of the fixture: evaluate before and after, diff the rows; assert every row whose content changed has
  the mutated vertex in its post-evaluation provenance. A named vector removes a **walk-owned column's binding**
  (`edgeCatalog`'s `grantedBy` / `holdsRole` on the staff walk) and asserts the surviving base-walk row is admitted.
  The census's error bucket (a lens the fixture cannot seed) is enumerated and asserted empty, with a floor on the
  lens count.
- **T5 — reprojector / sweeper (`grantchange`):** scope merge law table (All ⊔ x, Anchors ∪ Anchors, the 64
  bound); `GrantChanged` with and without an entry token; a non-content pass publishes only frames (adapter call
  list), a content cycle publishes rows; the first cycle after construction is a content cycle; the verdict's
  `Attempted`/`Failed` are identical across scopes.
- **T6 — e2e (`pl2Harness`, `personal_lens_*_e2e_test.go` precedent):** an aspect change on one tail vertex of a
  three-row actor publishes one upsert and one frame naming three keys; a grant landing for one anchor publishes
  that anchor's row and a frame; a sweep pass publishes frames only; a hydrate publishes everything.
- **T7 — live acceptance (Inc 3), in terms no classifier decides:** the probes re-run on the dev stack after a full
  `MaxAge` window: SYNC first sequence ≥ 24 h old (the byte cap no longer binds); `edgeCatalog` bytes per 12 h down
  by ≥ 90 %; upserts per 12 h on a quiet actor's subject (zyoC…-shaped) ≤ its row count × 1 (the daily content pass);
  ~~`REFRACTOR_AUDIT` first sequence ≥ 12 h old~~ (withdrawn at Inc 3 — the trail is another writer's, H4); and the widest
  subject's messages per revision ≤ 2 (one upsert + one frame) on ≥ 95 % of **live** revisions — the CDC path's revisions,
  those with fewer than ten lenses; a whole-actor pass (a sweep, a drain, a hydrate) is fifteen frames at one revision by
  design and is reported beside them, not inside the ratio (`make sync-census SYNC_CENSUS_ARGS="-subject …"`).

---

## 11. Decomposition for the Steward

| Inc | Scope | Retires | Tests owned | Posture |
|---|---|---|---|---|
| **1 — the healer and the drain publish what moved** | `publishScope` type (pipeline); `ReprojectPersonalActor(ctx, id, scope)` + `recordProjected` on its frame and Hydrate's; `GrantChangeSink.GrantChanged(actorKey, entryID)` through the widened injected inverse (`SetGrantChangeSink` / `driver.go:690`) and its three producers (§4.3); dirty-set scope merge; sweeper `ScopeNone` per pass + the 24 h content cycle latched at `ensurePopulation`; `docs/components/refractor.md` § transport + sweeper. No engine change. | H3 (39 % of msgs), the drain's share of H2, the corresponding audit entries | T5, T6 (grant + sweep vectors), T3's `ScopeAll`/`ScopeAnchors` arms | **posture-changing** (healer semantics) — full-depth review |
| **2 — the CDC write loop publishes what the event touched** | `binding` provenance chain + the **seven** record sites and the **three** strip sites (§4.1); per-branch read sets + the merge union; `ProjectionResult.Provenance` / `EvalResult.Provenance`; `ScopeVertices` from the four producers threaded through `evaluateForEntryRaw`/`evaluateForEntry`/`dispositionEvalErr` to the five `writeResults` sites; the two eligibility conjuncts; the personal+`Retry` install refusal; the hydrate guards (§4.6); the write loop's `recordProjected` on frame. Honest size: **L** on its own — the first draft's "one field on each of two structs" undercounted by ~15 edit sites. | H2's live share (61 %), H1 (the byte cap stops binding), H4 | T1, T2, T3, T4, T6 (aspect + hydrate vectors) | engine change — full-depth review; T4 is the gate |
| **4 — a rebuild publishes nothing per replayed event; completion is one content cycle** (added 2026-09-05 from Inc 3's measurement) | `writeResults` (personal targets) publishes no row and no frame while `RebuildInFlight()`; rebuild completion (`watchRebuildCompletion` → `SetActive`) calls an injected `RebuildCompleteSink` that asks the sweeper for its next cycle to be a content cycle (`RequestContentCycle`, the `firstSinceBoot` latch generalised); §4.5 rewritten. | the rebuild flood (§4.5: 93 % of SYNC's bytes on 2026-09-04) | T8: pl2 e2e — activate a personal lens with a multi-row actor, `Rebuild()`, assert zero SYNC messages on the actor's subject during the replay and one `ScopeAll` pass (rows + frame, revision ≥ live head) after completion; sweeper unit — `RequestContentCycle` makes the next `ensurePopulation` a content cycle exactly once | **posture-changing** (lifecycle) — opus build, cold opus review |
| **3 — measure on the shipped mechanism** | the two probes as a `-tags livecensus` tool under `scripts/`; re-run C1/C4/C6 after 24 h; correct the parent design's §1.3 and this doc's §1.2 numbers if the shipped mechanism differs; file the `edgeCatalog` row-size package row and, if the residual binds, the cap re-derivation. | the close-pass obligation the dossier names ("numbers about deleted code are not evidence") | T7 | doc + tool |

Inc 1 ships alone and green (no wire change, old and new clients indifferent); Inc 2 lands behind it; Inc 3 closes
the row. Each increment's Phase-0 re-runs C1, C4 and C7.

**Dossier entries the brief copies (`docs/components/refractor.md`):** *a widened operation silently drops the
bound its narrow predecessor carried* (the provenance chain's allocation bound, T2); *a review that replaces the
mechanism refutes the measurement that justified the old one* (Inc 3 exists for it); *a soundness claim's stated
reason is load-bearing* — §4.1's invariant is pinned by T4, not argued.

---

## 12. Adversarial pass — RUN (this fire, 2026-09-04; one cold, read-only reviewer against the code)

Eleven findings, every citation re-verified by the author before folding; none deferred.

- **BLOCKING ×3, all folded:** (1) the multi-walk merge loses a walk-owned column's provenance when a branch stops
  producing the key — `edgeCatalog`'s `viaRole` after a `grantedBy` tombstone; §4.1 gains the fourth invariant case
  and the per-branch read-set union, T4 is re-specified against the composed lens (the first draft's gate visited
  branches one at a time and could not see it). (2) The chain was discarded at every non-aggregating `WITH`
  (`executor.go:1681`), which is all fourteen tails, and the reserved key would have leaked through both `DISTINCT`
  renders and `applyReturn`'s values copy onto the wire — §4.1 now enumerates seven record sites and three strip
  sites. (3) `OutputDescriptor.EntryFromKey` called from `pipeline` was an import cycle — the injected inverse is
  widened instead (§4.3).
- **MAJOR ×5, all folded:** the freshness-clock sentence described a change as if it were the code (§4.2, now a
  stated change in both increments); Hydrate's before-capture revision against the client's resurrection guard is a
  regression scoping unmasks (§4.6, two guards, R6); the stream-wide classifier's error bar omitted identity-aspect
  and identity-endpoint-link events (re-run, C4; T7 re-specified classifier-free); `seedNodes` and `applyMatch`'s
  `WHERE` were missing record sites (§4.1, plus the point-seeded-anchor conjunct); the actor-own path's peer case
  was mis-described (§4.2).
- **MINOR ×3, all folded:** conjunct 3 restated as the liveness claim the code makes and the content-cycle latch
  moved to `ensurePopulation` (§4.4); the three `GrantChanged` producers, five `writeResults` sites and the
  return-path threading enumerated, Inc 2 re-sized to L (§4.2, §11); the comprehension cursor's precedence over the
  head-chain rule stated (§4.1 site 4).
- **Refuted by the reviewer, kept as pins:** no engine read goes around `fetchNode`/`fetchEdges` (staging promotes
  through it; `resolveProperty` routes through it); ~~nothing in the licence, the verdict or any health surface reads
  what a sweep published~~ (struck 2026-09-05: `LensProjectionStalled` reads `lastProjectedAt`, which the Inc 1 review caught
  and the frame deliberately never stamps — §4.2); every generated cap-read producer is per-entry, so the anchor token the grant scope needs
  is always present.

---

## 13. Board + doc actions

- The board row flips `🏗️ designing` → `✅ ratified (Winston-adjudicated)`, pointing here.
- `personal-lens-whole-actor-cost-design.md` §1.3: the sentence *"i.e. under three events of history"* is
  corrected in place with a pointer to §1.1 here (ratification-banner-rewrites-body; the numbers in a shipped
  design are the next reader's premise).
- The `edgeCatalog` row-size row — *[edge-manifest] `edgeCatalog` carries the whole descriptor vocabulary per row (~2 KB
  × 26–97 rows per actor)* — is filed on the Lattice lane (2026-09-05; `edge-manifest` is the platform's edge package, not a
  vertical's). The cap re-derivation is not owed: the post-rebuild fill is ≈ 66 MB/day against 512 MiB (Checkpoint 2026-09-05).
- `personal-lens-retraction-design.md` carries a pointer to §4.4 (its per-pass row republish is superseded by frames per pass
  and a daily content cycle).

---

## 14. Build notes

### Inc 1 fire brief (build note, 2026-09-04 — Lattice Steward fire, worktree `../lattice-wt-pl-delta`, branch `fire/personal-lens-delta`)

**Fire plan (Steward sizing).** The §11 increments are the item's fires: **Fire 1 = Inc 1**, Fire 2 = Inc 2, Fire 3 =
Inc 3. **Landing shape: each increment lands on `main`** — the invariant that keeps `main` correct across the
boundaries is §4's zero-value rule (`PublishScope{}` is `ScopeAll`, so every caller Inc 1 does not touch reproduces
today's publication byte-for-byte) plus §7 (no wire change; old and new clients indifferent).

**1. Scope sentence (§11 row 1, verbatim).** *`publishScope` type (pipeline); `ReprojectPersonalActor(ctx, id, scope)`
+ `recordProjected` on its frame and Hydrate's; `GrantChangeSink.GrantChanged(actorKey, entryID)` through the widened
injected inverse (`SetGrantChangeSink` / `driver.go:690`) and its three producers (§4.3); dirty-set scope merge;
sweeper `ScopeNone` per pass + the 24 h content cycle latched at `ensurePopulation`; `docs/components/refractor.md`
§ transport + sweeper. No engine change.* Green bar: T5, T6 (grant + sweep vectors), T3's `ScopeAll`/`ScopeAnchors`
arms. Posture-changing (healer semantics) — full-depth review.

**2. Verified touch-list (checked live at `e869ec57`).**
- `internal/refractor/pipeline/reproject_personal.go:143` — `ReprojectPersonalActor(ctx, identityID)`; row loop
  `:193-209` upserts every non-delete result and appends `frameKeys`; revision captured after evaluation `:177`;
  frame `:239`. Gains `scope PublishScope`; a non-delete result is **written iff `scope.Admits(result)`**, every
  non-delete result still lands in `frameKeys`; the Delete arm and the revision/lock/flush order are untouched;
  `p.recordProjected()` after a successful `PublishKeySet`.
- `internal/refractor/pipeline/hydrate.go:45-134` — no signature change (`ScopeAll` by construction); add
  `p.recordProjected()` after the successful `PublishKeySet` at `:122`.
- `internal/refractor/pipeline/grantchange.go:30-32` (`GrantChangeSink`), `:51` (`SetGrantChangeSink`'s inverse
  `func(string) (string, bool)`), `:216` (`notifyGrantChangeSignalled` → passes the inverse's entry token), `:246`
  (`notifyActorGrantChange` → passes `""`), `:264-279` (`truncateTarget` routes through `notifyGrantChange`, so it
  needs no separate edit). The legacy parent-document Delete `evaluate.go:953-957` never reaches the sink — unchanged.
- `internal/refractor/projection/driver.go:690` — binds `desc.AnchorFromKey`; bind the widened closure instead.
  `projection/output.go:392-422` (`AnchorFromKey`) and `:442-458` (`anchorFromKeyPerEntry`, the `LastIndexByte('.')`
  split — the entry token is `rest[idx+1:]`, NanoID-validated). Add an exported per-descriptor inverse that returns
  `(actorKey, entryID, ok)` with `entryID == ""` for a non-per-entry lens. `pipeline` must not import `projection`
  (verified: it does not).
- `internal/refractor/grantchange/reprojector.go:66-69` (`PersonalPipeline.ReprojectPersonalActor` — gains scope),
  `:450-471` (`GrantChanged(actorKey)` → `(actorKey, entryID)`; `entryID == ""` ⇒ `ScopeAll`), `:492-497`
  (`InterestChanged` ⇒ `ScopeAll`), `:505-517` (`enqueue` — the dirty set becomes `map[string]PublishScope`; an
  existing entry MERGES per §4.3, the `maxDirty` bound and `dropped` accounting count entries exactly as today),
  `:594-602` (`take` returns the scope), `:621-647` (`reprojectActor(ctx, actorID, scope)` applies it on every
  registered lens), `:661-663` (`ReprojectNow(ctx, actorID, scope)`).
- `internal/refractor/grantchange/sweeper.go:32-34` (add `PersonalContentHealInterval = 24 * time.Hour`),
  `:270-371` (`Sweep` — the pass's scope is decided once, before the batch loop `:357-360`, from the latched cycle
  kind), `:491-530` (`ensurePopulation` — the re-list at `:524-529` is where a cycle starts: latch
  `lastContentCycleStart` there when the previous latch is older than the interval; zero ⇒ the first cycle after
  boot is a content cycle; log the content-cycle start with the cycle length, R4), `:534-560` (`claim` unchanged).
- Scope match for anchors: `substrate.ParseVertexKey(result.Row["anchor"])`'s NanoID ∈ A — the same read the D1
  envelope makes (`projection/personal.go:271-277`) and the adapter promotes (`adapter/natssubject.go:36`).
- Every caller/test that the signatures reach (all must compile + stay green): `pipeline/reproject_personal_test.go`
  (`:168,189,235,268,349,358`), `pipeline/reproject_personal_revision_test.go:70`, `pipeline/publish_pipeline_test.go`
  (`:340,366`), `pipeline/shred_announcement_test.go` (`:87,273,301`), `pipeline/truncate_reactivation_internal_test.go:113`,
  `pipeline/truncate_grant_change_test.go` (`:107,122,135`), `grantchange/reprojector_test.go` (the fake at `:293` +
  ~30 `GrantChanged` calls), `grantchange/sweeper_test.go:170-171`, `projection/grant_change_install_test.go`,
  `cmd/refractor/personal_healer_test.go:363`, `cmd/refractor/main.go:1667`. No build-tagged test under
  `internal/refractor` or `cmd/refractor` reaches these seams (verified by grep).
- Docs: `docs/components/refractor.md:72` (Personal Lens transport — add the publication-scope rule) and `:293-310`
  (the sweeper prose inside the D1 bullet — the two cadences: frame per pass, content once per
  `PersonalContentHealInterval`); `personal-lens-grant-change-trigger-design.md:379` (§4.3 — a dated pointer to §4.4
  here). `personal-lens-whole-actor-cost-design.md:70-75` already carries the §13 correction (verified).

**3. Precedents to mirror.** `enqueue`'s coalesce-then-bound shape (`reprojector.go:505-517`); `anchorFromKeyPerEntry`'s
split (`output.go:442-458`); the anchor parse in `personal.go:271-277`; `recordProjected` at `results.go:242`; the
reprojector unit fakes (`reprojector_test.go:280-300`) for T5; the sweeper unit tests for the cycle latch; the e2e
harness of `personal_lens_grant_change_e2e_test.go` (grant vector) for T6.

**4. Increment order (inside the fire), each with its green check.**
1. `pipeline`: `PublishScope` (kinds All/None/Anchors; the zero value is All; `ScopeAnchors` of an EMPTY set is
   `ScopeAll` — an empty set must never read as "admit nothing" by accident, dossier rule) + `Admits` + `Merge`
   (the §4.3 law, bound `MaxScopedAnchors = 64`) — a table test. `go test ./internal/refractor/pipeline/ -run Scope`
2. `pipeline`: `ReprojectPersonalActor` scope + frame `recordProjected` (both sites); callers/tests updated.
   `go test ./internal/refractor/pipeline/`
3. `pipeline` + `projection`: sink widening, the exported inverse, the three producers, the driver binding.
   `go test ./internal/refractor/pipeline/ ./internal/refractor/projection/`
4. `grantchange` + `cmd/refractor`: dirty-set scope, `take`, `reprojectActor`, `ReprojectNow`, sweeper `ScopeNone` +
   content cycle. `go test ./internal/refractor/grantchange/ ./cmd/refractor/`
5. e2e T6 vectors in `internal/refractor` (grant → one anchor's row + frame; sweep pass → frames only; hydrate →
   everything). `go test ./internal/refractor/ -run 'PersonalLens'`
6. Docs (§2 last bullet). Gates: `go build ./...` · `make vet` · `golangci-lint run ./...` ·
   `STRICT=1 go run ./scripts/lint-conventions.go` · every `scripts/lint-*.go` · `go test ./internal/refractor/... ./cmd/refractor/`.

**5. In-scope gotchas.** (a) The after-capture revision and the `(lens, actor)` lock in `ReprojectPersonalActor` are
the retraction design's spine — do not move them; a `ScopeNone` pass still flushes the (empty) pipeline and
publishes the frame from ALL non-delete results. (b) `recordProjected` on a signalled reprojection's and a hydrate's frame (never a `ScopeNone` pass) is a stated CHANGE to the freshness
clock (§4.2) — say so in the doc comment; nothing alarms on it. (c) The sweep's scope is per PASS, latched at the
cycle start — not per identity, not re-read mid-batch. (d) The merge never creates a second entry: a merge into an
existing dirty entry is not counted against `maxDirty` and never increments `dropped`. (e) T5 pins that the verdict's
`Attempted`/`Failed` are identical across scopes (the licence's conjunct 3 is liveness, §4.4). (f) Every test is
revert-proven (standing checklist #3): a scope that admits everything, a latch that never flips, a producer that
passes `""` — each must fail its test. (g) No history comments (CLAUDE.md). **Dossier entries copied in
(`docs/components/refractor.md`):** *a widened operation silently drops the bound its narrow predecessor carried*
(the dirty set's bound + drop accounting must survive the value change); *a fixture that establishes the favourable
ORDER or ARM is an argument, not a test* — the sweeper test that registered the lens before `Run` was green over a
pass that never ran: write the sweep-scope test in `cmd/refractor`'s startup order, and barrier on the EFFECT (the
adapter's call list), never on pending; *a zero or empty reading that cannot be distinguished from "not measured"
must read UNREADABLE* (an empty anchor set vs. no scope — hence increment 1's empty-set rule); *a soundness claim's
stated REASON is load-bearing* — the healer's frames-only pass is justified by "nothing reads what a pass
published"; T5 pins it rather than argues it. **Standing checklist (fire-brief template, six lines):** new state
needs a LIFETIME (§5 has the table — `lastContentCycleStart` and the dirty-set scope; build to it); every census is a
premise (the caller/test list above was re-grepped live); a negative test needs its positive vector, and plumbing is
revert-proven hardest (the `entryID` thread: assert at the producer that the token equals the key's trailing NanoID,
not merely non-empty); removal needs a transport AND an observer (the rows a `ScopeNone` pass withholds are held
by the device and named by the frame — §4.7's argument, pinned by T6); one deterministic key, one writer (n/a); precedent
may carry debt (the grant-change e2e is the mirror; verify its barrier before copying it).

**6. Adjacent finds.** None outside the design's own list. The `edgeCatalog` row-size package row and the cap
re-derivation are Inc 3's (§13). Any find the build surfaces resolves per the Steward's §4 accounting before this fire
closes.

**7. Non-goals (Fire 1).** The CDC write loop (`results.go`, `dispatch.go`, `evaluate.go`'s scope producers), row
provenance and `ScopeVertices`, the hydrate guards of §4.6, the personal+`Retry` install refusal, the client, the wire,
`edgeCatalog`'s row size, the Inc 3 probes.

**Scope-diff gate.** Every touch in part 2 traces to the scope sentence; nothing widens it; no adjacent mechanism is
substituted (the write loop and provenance stay Inc 2). Dependencies both ways: Inc 1 depends on nothing unbuilt;
Inc 2 depends on Inc 1's `PublishScope` and the frame `recordProjected` sites.

### Checkpoint (2026-09-04, after Inc 1)

- **Landed on `main`:** Inc 1 — `feat(refractor): personal-lens publication scope` (the Inc 1 commit; CI on its
  push). Worktree `../lattice-wt-pl-delta`, branch `fire/personal-lens-delta`, kept for Inc 2.
- **Inc 1 review classification** (three cold reviewers, 0 blocking / 6 major / 8 minor, all folded or declined with
  reason): *design-gap* ×3 — the frame heals removal only (R7, template checklist #4 sighting), the start-measured
  latch (§4.4), the liveness-clock stamp (§4.2 → dossier sighting); *implementation-bug* ×1 — malformed tokens under
  `ScopeAnchors`; *convention/doc* ×6 — five falsified comments, two stale component-doc sentences, sibling design
  snippets; *test* ×2 — the grant-change e2e's boot-cycle premise, the `ScopeNone` pipelined vector. Declined: a
  content-interval env knob and a health field (widenings the design does not name; the log carries R4), a bounded
  retry of a failed drain reprojection (new state against §6's "nothing else"; the health fault is the signal, R7),
  a scope check on the unreachable `Delete` arm.
- **Named unreachable branch:** a personal lens installed with no read-grant gate (`capKV == nil`, never in
  `cmd/refractor`) could emit a row whose `anchor` does not parse, which `ScopeAnchors` withholds (under-display until
  the content cycle). The refusal sits inside `personalEnvelopeFn`'s `capKV != nil` branch; Inc 2's brief lists it.
- Inc 2 followed (next checkpoint).

### Checkpoint (2026-09-04, after Inc 2)

- **Landed on `main`:** Inc 2 — the engine's row provenance, the CDC write loop's `ScopeVertices`, the three
  eligibility conjuncts, the personal+`Retry` refusal, the hydrate guards, T1/T2/T3/T4/T6 (the Inc 2 merge commit;
  CI on its push). Worktree `../lattice-wt-pl-delta`, branch `fire/personal-lens-delta`, kept for Inc 3.
- **Delta-scout (two haiku scouts, at f6709e9c):** every §4.1/§4.2/§4.6 site re-verified live; one relocation — the
  branch merge (§4.1 site 7) lives in `pipeline/branchmerge.go`, not the engine. Built as two parallel opus builders
  (engine · pipeline) on disjoint files, then a third for T4/T6 on the merge.
- **Inc 2 review classification** (three cold reviewers over the build, one over the fix round; 1 blocking / 11
  major / 12 minor, all folded or declined with reason): *design-gap* ×6 — §4.6's guard-2 mechanism read the rule
  stream's sequence (a cited mechanism never opened); the parent-only hand-off for a discarded binding was unsound two
  clauses deep and the required-`MATCH … WHERE` and `WITH` discard sites were missing from §4.1's closed list; the
  freshness clock on the CDC frame (the Inc 1 dossier sighting, still standing in §4.2's own text); the label-sigil
  conjunct; the merge substitution's payoff (R8); *implementation-bug* ×4 — the fold memo keyed to the asked node; the
  scope logged as `{}` under the production JSON handler; a silent withhold on a missing actor key; a refusal log that
  never re-armed; *test* ×5 — a racy hydrate barrier, a dead conjunct helper under test, a falsified DISTINCT comment,
  a no-op assertion, the retry census one hop short; *convention* ×1 — a duplicated Contract #1 fold. The blocking
  finding (T4 absent from the first diff) was the staged third builder, landed before merge. Declined: none.
- **Dossier + gate:** the JSON-handler fixture bias is the second sighting of "a fixture on the favourable ARM" on the
  test-harness axis → mechanized as `scripts/lint-slog-values.go` (an slog attribute value of a module struct type must
  implement `slog.LogValuer` / `json.Marshaler` / `encoding.TextMarshaler`); the memo-keyed-to-the-asked-node class is
  minted on `docs/components/refractor.md`.
- **T4 result:** 15 lenses, 197 mutations each, zero violations; every lens withholds something (positive control).
  Withheld share on the two-row fixtures: `edgeCatalog` 182 / 394 decisions, the single-walk lenses 248–302 / 394.
- **Next: Inc 3** (§11 row 3) — the two probes as a `-tags livecensus` tool under `scripts/`; re-run C1/C4/C6 after
  24 h on the shipped mechanism (R8's fraction first); correct §1.2 and the parent's §1.3 if the numbers move; file the
  `edgeCatalog` row-size package row and, if the residual binds, the cap re-derivation; the cumulative close pass over
  the item's whole diff, then the row closes.

### Checkpoint (2026-09-04, after Inc 3 — tool shipped, T7 pending its window)

- **Landed on `main`:** `scripts/sync-census.go` + `make sync-census` (the §3 C3/C4 probes as one repo tool, with the T7 block,
  best-effort lens-name resolution, and `-since <window>` so an interim read excludes the older mechanism's messages) and one
  sentence in `docs/components/refractor.md`. Deviation from §11 row 3: the repo's tool convention is `//go:build ignore` +
  `go run ./scripts/<x>.go` behind a Makefile target (`purge-cap-read-legacy` is the precedent), not a `-tags livecensus`
  build tag; same effect (never compiled by `go test ./...`), one convention. Worktree `../lattice-wt-pl-delta` kept for the
  close pass.
- **Interim read on the shipped mechanism (Refractor rebuilt 06:43 PT from the Inc 2 merge; window 06:45–08:26 PT, 100 min):**
  whole-actor 304 groups / 11,919 msgs / 9.6 MB (177 actors × ≤ 2 passes: the post-restart content cycle — 61 msgs on the widest
  actor incl. 26 `edgeCatalog` upserts — then a frames-only pass of 15 messages), live 1 group / 52 msgs. `edgeCatalog` 8.1 MB in
  the window ⇒ 58 MB/12 h, against 465 MB per 12 h 21 min in C4 (−87 %, and the content cycle inside it recurs daily, not
  hourly). The window's CDC traffic is a replay of the 09-03 superseded-background-check purge (every sampled event is a
  `providedTo` link of an instance tombstoned 2026-09-03; the widest actors received 2,033 and 1,498 such events and published
  nothing for them — the correct answer, no row on the device changes). **No actor received a row-changing event in the window,
  so T7's per-event conjuncts have no live revisions to measure yet (0/0).** R8's fraction is therefore unmeasured.
- **Next (the row stays 🏗️):** re-run `make sync-census` (stream-wide, then `-subject` on the widest actor and a quiet one) at
  or after **2026-09-05 06:45 PT**, when SYNC's whole `MaxAge` window post-dates the deploy; evaluate T7's four remaining
  conjuncts; if the live-revision ratio or `edgeCatalog`'s −90 % falls short, the R8 narrowing (per-hop precision, T4 as gate)
  is the follow-on. Then the cumulative close pass over the item's whole diff (Inc 1 + Inc 2 + Inc 3), the review classification,
  and the row closes. **Filing this fire could not do:** §13's `edgeCatalog` row-size row belongs on the verticals lane and this
  stream's fire may not write that lane; the next Verticals Steward or the PO files it from this sentence (~2 KB × 26–97 rows per
  actor, the descriptor vocabulary repeated per row).

### Checkpoint (2026-09-05, T7 measured — Inc 4 opened)

- **T7 on the shipped mechanism, whole stream (18 h 28 min, 2026-09-04 13:44 → 09-05 08:12 PT):** 348,275 msgs / 512 MiB,
  cap at 99.996 %, first sequence 18.5 h old. Live 248,948 msgs / 467 MB; `edgeCatalog` 326 MB/12 h (−30 % against C4's 465 MB —
  **fails**). Widest subject (`edu97…`): 1,951 msgs, 305 revisions, live ≤ 2-msg ratio 79.8 % (**fails** ≥ 95 %).
- **Attribution (the whole shortfall is one event):** all 1,102 upserts on the widest subject landed 13:44–14:59 PT on 09-04 at
  revisions 254,705–484,865 (Core KV entries dated 08-26 → 09-04, replayed at their original sequence): the `edgeCatalog` spec
  update of 11:34:42 PT (`vtx.meta.ZvZ3….spec`, op `fVDg…`) triggered `health: rule rebuilding`, the rebuild's `DeliverLastPerSubject`
  replay ran the CDC write loop per entry, and R8 admitted all 26 rows per admitting entry. 1,406 msgs × 177 actors ≈ the 248,948
  live messages. §4.5 rewritten; Inc 4 added to §11.
- **Post-rebuild window (17 h, 09-04 15:24 → 09-05 08:23 PT, four Refractor restarts inside it):** live 421 groups / 1,751 msgs /
  2.5 MB; whole-actor 93,293 msgs / 45 MB (16,486 upserts = the restart content cycles, 12–16 frame passes per actor);
  `edgeCatalog` 25.5 MB/12 h = **−94.6 %** against C4 (holds; a restart-free day is lower). Every live revision on the widest subject
  outside whole-actor passes carried ≤ 2 messages (holds). The byte cap stops binding once the 09-04 burst ages out at 14:59 PT
  today (post-burst fill ≈ 47 MB/17 h ≈ 66 MB/day against 512 MiB); `REFRACTOR_AUDIT` is another writer's (H4, withdrawn).
- **Next:** build Inc 4 in `../lattice-wt-pl-delta` (fast-forwarded to `30d29549`), cold review, merge, cycle `bin/refractor`,
  trigger a rebuild live (a spec-touching package refresh) and re-run `make sync-census -subject` on the widest actor: zero
  messages during the replay, one content pass after. Then the cumulative close pass over Inc 1–4 and the row closes.

### Checkpoint (2026-09-05, Inc 4 SHIPPED `8a43aa9d` + 4b `1a1c6cd9` + 4c `34ce301c` — T7 live on the rebuild path; item CLOSED)

- **Landed on `main` (CI green, run 33981427129):** `ScopeSilent` from the CDC write loop while a rebuild window is open; the
  window as a count of live rebuilds, each invocation ending its window once, the 1 → 0 transition asking the injected
  `RebuildCompleteSink` (the sweeper's `RequestContentCycle`, one request = one cycle); rebuild progress stamped at window
  open (R9); the class-key erasure refusing a `Personal` target before `RebuildRule`, the label falling back to the declared
  `IsPersonalLens`; the `$now` refusal derived over the branch set; `scripts/lint-flag-consumer-census.go` (Makefile + CI);
  T8 unit vectors, a pl2 e2e with a delivered-count positive control, a real-sweeper seam e2e. Two cold reviews (Inc 4 +
  the cumulative close pass) — 8 MAJOR, all folded; M‑5 (hydrate mid-window) named as R10, m‑1 (re-arm on failure) rejected.
- **Live (Refractor cycled 10:35 PT, operator `lattice lens rebuild` of `edgeCatalog` at 10:37:14):** every event the lens
  processed through 11:32 (13,486) carried `publishScope=silent`; the widest subject received zero write-loop messages
  during the replay against 1,406 in the 2026-09-04 rebuild. The one rewound publication observed was the sweeper's boot
  content cycle on the rebuilding lens (10:52:50, 27 msgs at revision 3,381) — the §4.5 bullet above; Inc 4b gates it.
- **Post-rebuild steady state (17 h, four restarts):** live 1,751 msgs / 2.5 MB; `edgeCatalog` 25.5 MB/12 h (−94.6 % vs C4);
  every live revision on the widest subject outside whole-actor passes ≤ 2 messages; the byte cap stops binding when the
  09-04 burst ages out (≈ 66 MB/day post-burst against 512 MiB). T7 holds on the shipped mechanism.
- **Inc 4b `1a1c6cd9`:** `ReprojectPersonalActor` on a pipeline whose window is open scopes to Silent (the healer's passes and
  the drain), the reader declared in the flag ledger; unit vectors + a real-sweeper e2e holding the window open. A requested
  cycle that runs inside a later window is spent silent, and that window's own close requests again; the latch reads the
  request at cycle start, so every close is honoured by the next cycle to start.
- **Inc 4c `34ce301c`:** the resumed window opens before `Run` adds the consumer (live after the 12:14 PT cycle: the resume
  at 12:14:47, the first replayed event at 12:15:00 already `silent`) (the 11:52 restart's first replayed event was scoped
  before the window existed — 742 messages at revision 122,624 across 135 actors; every later event silent).
- **Not observed in this fire:** the window's close and the requested content cycle on the live stack (the replay was still
  running at close; both are pinned by T8's completion vectors and the seam e2e). The next reader checks `refractor.log`
  for `rebuild complete on a personal lens` on `ZvZ3…` followed by a `CONTENT cycle … (requested)` line.
- **Binaries:** `bin/refractor` cycled from `main` (`make cycle-refractor`); `bin/lattice` and `bin/loupe` rebuilt (Loupe links
  `pipeline.Hydrate` only and runs no pipeline — not cycled; the Loupe lane owns its process).
