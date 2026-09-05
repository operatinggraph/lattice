# An anchor-partitioned plain lens retracts within its partition — the target diff scopes to the anchors an evaluation covered

**Status: ✅ RATIFIED (Winston-adjudicated, per the 2026-08-20 delegation) — 2026-09-05.** Designer fire
2026-09-05 · Stream 2 (Lattice) · Component: **Refractor** (`internal/refractor/ruleengine/full`,
`internal/refractor/pipeline`, `internal/refractor/adapter`) · Board row: *[Refractor] A neighbour-keyed
plain lens that genuinely partitions by anchor is still refused* (`backlog/lattice.md`, Read-model /
projection maturity). No architectural fork, no frozen-contract edit (§6). Adversarial pass run
2026-09-05 and folded (§13: 13 findings, 2 blocking — both changed §3). Size **M** (two increments).

> **Rewritten after the adversarial pass.** Everything below is the corrected design; §13 records what the
> pass refuted. The refuted text is gone, not banner-covered.

---

## 0. For Andrew

**What it does, in two lines.** Eight plain lenses — five business read models keyed on an
`(anchor, neighbour)` pair, `landlordLeaseApplicationsRead` first among them, and three grant tables —
carry `DiffRetraction`, and that one declaration makes *every* event on them a whole-corpus rescan plus a
whole-target key listing, their own anchor's events included, and bars them from the neighbour-anchor
derivation before its partition conjunct is ever asked. This design derives from the compiled rule that
such a lens's rows **partition by anchor** (one key column identifies the anchor; the others may bind
neighbours) and scopes the target diff to the partitions an evaluation covered — which is what lets the
five business lenses seed on their anchor's events and narrow on their neighbours' like every other plain
lens. The three grant tables keep their whole diff, for a reason §3.7 names.

**Fork / contract check — honest answer: neither.**

- **Architectural fork: none.** A mechanism-level widening of one closure predicate and one retraction
  transport inside Refractor, resolved here (§8 prices the alternatives, "delete the transport" first).
  No Gateway / D1 / Vault / multi-cell / HA question.
- **Frozen-contract change: none.** No `docs/contracts/*` sentence mentions `DiffRetraction`, the
  narrowing licence, or which lenses partition (§6 pastes the grep, re-run by the reviewer). The design
  **builds to** Contract #6 §6.14: a protected table's retraction is *"a seq-guarded soft tombstone
  (`is_deleted = true`)"* — the same `Delete` the adapter already writes, reached by a scoped listing
  instead of a whole one. Nothing is staged uncommitted.
- **What is worth your sixty seconds.** §3.1's soundness argument, because the predicate it widens
  authorises `Delete`s on RLS-protected tables. It is `ProjectsOneRowPerAnchor`'s own argument minus one
  conjunct; the adversarial pass attacked it through the four executor mechanisms it rests on and could
  not break it (§13, "could not break"); §5 pins it by census and by mutation.

**What I have to correct in the row.** Three of its four clauses (§1.3): the refusal that binds is not
the partition conjunct — it is the `DiffRetraction` declaration, two conjuncts earlier, and the partition
conjunct has never once been asked of this lens; the whole-corpus rescan is not the *fallback* for
neighbour events, it is the *only* evaluation the lens ever runs, its own anchor's events included; and
the with-alias design's §8 note that "no consumer is waiting" was already false when written —
verticals.md's row 29 is blocked on this one.

---

## 1. Problem and intent

### 1.1 The row, and the demand behind it

> *[Refractor] A neighbour-keyed plain lens that genuinely partitions by anchor is still refused —
> `landlordLeaseApplicationsRead`'s composite `(app_id, landlord_id)` key binds the non-anchor half and
> falls back to whole-corpus rescan even though its rows partition cleanly (bucket G,
> `with-alias-anchor-closure-design.md` §2.1). Blocks verticals.md's landlord-visibility row.
> no-pattern: partitionability derivation for a neighbour-keyed lens's key*

The consumer waiting is verticals.md row 29: *"A submitted application is invisible to applicant AND
landlord, and the app calls the projection healthy … the applicant lens licence-fixed since, landlord lens
not — 🚧 blocked-on: lattice.md `landlordLeaseApplicationsRead` partitionability row."* The applicant half
was unblocked by the with-alias design (bucket F); the landlord half is bucket G, which that design left
"correctly refused" and this one takes up.

### 1.2 The mechanism, re-derived from code

`landlordLeaseApplicationsRead` (`packages/lease-signing/lenses.go:345-352`) anchors on `app:leaseapp`,
requires four hops (`applicationFor`, `appliesToUnit`, inbound `manages`), keys on `(app_id, landlord_id)`
with `app_id = nanoIdFromKey(app.key)` and `landlord_id = nanoIdFromKey(landlord.key)`, and declares
`DiffRetraction: true` because — as its own comment says — *"Fire 2's `AnchorProjectionKey` can never derive
this composite key read-free."* That declaration has **three** consumers, and each of them refuses the
lens something:

| Consumer | Site | What `DiffRetraction` costs the lens |
|---|---|---|
| **Seeding** | `pipeline/rulestate.go:385-403` `seedAnchorFor`: *"DiffRetraction off — that retraction diffs the target's FULL live key set against the evaluation's row set, so a single-anchor row set would read as 'every other anchor's rows are gone'"* | an event on the lens's **own anchor** — a leaseapp created, signed, decided — evaluates the **whole corpus** instead of that one leaseapp |
| **The derivation index** | `pipeline/anchor_derivation_plain.go:207-231` `plainDerivationIndexRefusal`: *"it uses target-diff retraction, whose whole-key-set semantics a per-anchor seeded row set would misread"* | a **neighbour** event (a unit's listing, a landlord's `manages` link, the applicant's identity) is never narrowed to the leaseapps it reaches; the licence — and its partition conjunct — is never asked |
| **The retraction itself** | `pipeline/evaluate.go:1451` `applyDiffRetraction` → `diffRetractionListing` (`:1488`): one `ListKeys` of the **whole target** per event | every event lists every row of the table and diffs it against the whole fresh row set |

So the order of conjuncts decides the row's diagnosis: `plainDerivationStaticallyEligible` asks
`ProjectsOneRowPerAnchor` **last** (`anchor_derivation_plain.go:458`), and the index refusal on
`diffRetraction` runs before the licence is consulted at all. The partition conjunct's refusal string —
*"its rows do not partition by anchor"* — has never been produced for this lens.

**Live, 2026-09-05** (`refractor.log`, lens `CUfpxjSYUzznjnF2CUfp`, 10:35 restart onward):

```
plain anchor derivation cannot act on this lens; using today's unseeded evaluation
  reason: "it uses target-diff retraction, which would read a per-anchor row set as every OTHER anchor's rows being gone"
divergence audit enrolled  anchorLabel=leaseapp interval=15m
diff retraction installed
```

Every `pipeline: processed` line the lens has emitted since 2026-09-04 is one whole-corpus evaluation —
`vtx.leaseapp.3hWU…` created 05:35:17.9, its `applicationFor` link 05:35:19.5, its `appliesToUnit` link
05:35:21.2: **≈1.6 s per event on a 60-leaseapp corpus** (`read_lease_applications` 60 rows,
`read_landlord_lease_applications` 47), three of those events naming the very leaseapp the lens is
anchored on. The consumer's `num_pending` is 0 now; the 9-minute invisibility verticals.md observed is
this cost under a burst, on a lens that cannot narrow to the application that changed.

### 1.3 What the row got wrong, and what it got right

1. **"Falls back to whole-corpus rescan"** — not a fallback: the whole rescan is the lens's only
   evaluation, on every event, because `seedAnchorFor` refuses a `DiffRetraction` lens outright. The row
   describes the neighbour case; the anchor case is worse and is where the verticals symptom lives (a new
   application is an anchor event).
2. **"Is still refused"** on partitionability — the partition conjunct is not what refuses it; the
   `DiffRetraction` conjunct two steps earlier is. Lifting partitionability alone would move nothing.
3. **"Its rows partition cleanly"** — true, and the strongest fact in the row: `app_id` identifies the
   anchor and is a grouping item of both the `WITH` and the `RETURN`, so every row belongs to exactly one
   leaseapp and is computed from that leaseapp's own bindings (§3.1). The divergence audit already seeds
   this lens at one leaseapp every fifteen minutes (`audit.go:801`) and compares every row the seeded
   recompute produces — which proves no produced row is truncated, and says nothing about completeness
   (§2.3; the pass's finding 5). The completeness half is §3.1's argument, pinned by §5.
4. **"No consumer is waiting"** (with-alias §8) — verticals.md row 29 was filed 2026-09-03 and points here.

### 1.4 Intent

A lens whose rows partition by its anchor should be evaluated, narrowed and retracted **per anchor**, the
way every closed lens already is — with the retraction scoped to the partition instead of derived read-free
from the key, because for these lenses the key cannot be derived without a walk. The declaration stays; its
transport changes shape where the rule proves it may and the target can carry it.

---

## 2. Grounding ledger

### 2.1 C1 — the census: 8 lenses move, and the population is closed

Run during this fire against `34ce301c`, through the real resolver (a scratch `ScratchPartitionShape` in
`ruleengine/full`, the predicate of §3.1 verbatim, driven by `forEachCorpusCypher` over the 65-lens plain
corpus — the same enumeration and the same `closureKeyColumns` threading the with-alias census uses; the
reviewer re-ran it):

```
go test ./internal/refractor/ -run TestScratchPartitionCensus -count=1 -v
```

| Bucket | Count | Names |
|---|---|---|
| `ProjectsOneRowPerAnchor` **and** partitioned (unchanged: the predicate is a superset) | 54 | the with-alias census's 51 bucket-A lenses + its 3 bucket-F lenses |
| **refused today, partitioned** — the payoff set | **8** | `landlordLeaseApplicationsRead` (app_id ∣ landlord_id), `landlordUnitsRead` (unit_id ∣ landlord_id), `objectIdentityAttachmentsRead` (oid_id ∣ owner_id, link_name), `providerSites` (provider_id ∣ site_id), `duplicateCandidates` (secondaryId ∣ primaryId), `providerIdentityReadGrants` (actor_id ∣ anchor_id), `staffReadGrants` (actor_id ∣ anchor_id), `patientIdentityReadGrants` (anchor_id ∣ actor_id) |
| refused today, **still** refused | 3 | `wellnessMemberAccounts` (anchor `bk:booking`, key `id.key` — rows partition by *identity*, not by the anchor), `capabilityRoleIndex` (key `operationType` binds `perm`, no column identifies `role`), `opCatalog` (key is the anchor's root field, not its key) |
| | **65** | TOTAL, the same floor the with-alias census pins |

Three facts the table asserts and the build's census (§5) must pin: **all 8 admitted lenses are exactly
the `DiffRetraction: true` lenses whose closure is refused**; **no lens the shipped conjunct admits is
refused by the new one** (54 = 54); and the 3 refusals. The identifying column is before `∣`, the
neighbour-bound key columns after it. Of the 12 `DiffRetraction` declarations in the corpus
(`grep -rn "DiffRetraction: *true" packages`), the other four are `clinicPatientsRead`,
`consoleOperatorReadGrants`, `demoOperatorReadGrants` (closed **and** identifying already — the
declaration is not there for closure, §3.7) and `wellnessMemberAccounts` (not partitioned).

### 2.2 C2 — the consumers of the declaration

`p.diffRetraction` / `Into.DiffRetraction` is read at: `seedAnchorFor` (`rulestate.go:400`),
`plainDerivationIndexRefusal` (`anchor_derivation_plain.go:223`), `noteStaticPlainDerivationRefusal`
(`:1025`), `evaluateForEntryRaw`'s tail (`evaluate.go:346`), `PlainRetractionTransport`
(`retraction_transport.go:111`), `SetDiffRetraction` (`pipeline.go:1100`, the `KeyLister` activation
refusal), `SetDiffRetractionPrefix` (`:1121`), the activation guard (`cmd/refractor/main.go:1608-1627`),
the hot-reload refusals (`reload.go:147`, `:820`), `admitRetractionTransport` / `siblingLensOf`
(`retractiontransport.go:72,143,149`), `SharedTargetDiffRefusal` (`projection/sharedtarget.go`), the
declaration validators (`internal/pkgmgr/bucketguard.go:165-177`, `lens/corekv_source.go:1460`) and
serializers (`pkgmgr/build.go:508,552`, `corekv_source.go:1492,1526`), the audit-enrolment comment
(`audit.go:1046-1056`), the rebuild-truncate comment (`cmd/refractor/main.go:266-273`) and the enum docs
(`docs/observability/health-kv-schema.md:583`). The first four decide behaviour and each asks the same
question — *is the evaluation's row set the whole truth?* — which the partition conjunct answers per
anchor. §3.3 rewrites those four; §10 classifies the rest.

### 2.3 C3 — what the audit already proves, and what it does not

`auditAnchor` (`audit.go:791-847`) already runs `executeFullForAudit(ctx, rs, key, props, key)` — a
**seeded** evaluation at one anchor — on every enrolled lens, `DiffRetraction` lenses included
(`auditEnrolment`'s own comment: *"a DiffRetraction lens's seeded evaluation is read exactly like any
other plain lens's"*), and compares each produced row (`missing` / `stale`). For a partitioned lens that
comparison is exact for the reason §3.1 states. Its should-not-exist direction declines for every one of
the 8 (`AnchorProjectionKey` ok=false → *"not checked in this direction"*, `:841-847`) — so the audit
cannot see a seeded evaluation producing **fewer** rows than the whole one, which is exactly the outcome
§3.2 turns into a `Delete`. That is why §3.5 gives the audit the partition listing **in the same
increment** as the seeding: detection of under-production ships with the mechanism that could cause it.

### 2.4 C4 — the listing primitives, per adapter

| Adapter | Whole listing | Scoped listing today | Partition listing this design adds |
|---|---|---|---|
| `PostgresAdapter` | `ListKeys` — `SELECT <keys> FROM t [WHERE NOT is_deleted]` (`postgres.go:370`) | none | `ListKeysWhere(fixed)`: the same SELECT with `AND k = $n` per fixed column, values as bound parameters; a PK-prefix scan when the identifying column leads `IntoKey` (the PK is `IntoKey` in order, `rls.go:163`) |
| `ProtectedAdapter` | forwards `ListKeys` / `GetRow` (`read_path_adapters.go:423,444`) | — | forwards `ListKeysWhere` |
| `NatsKVAdapter` | `ListKeys` = every key, mapped by segment count (`natskv.go:692`) | `ListKeysPrefix(prefix)` → `prefix>` subject filter | `ListKeysWhere(fixed)` = the **same** listing the whole diff uses (whole, or the lens's prefix), filtered in Go to the rows whose fixed columns match — never a subject filter built from a value (§3.2 says why) |
| `GrantWriterAdapter` | `ListGrantsBySource` (`read_path_adapters.go:226`) — source-scoped, refuses without one | — | **not implemented** (§3.7) — and that absence is one of the two things that keep the grant tables on the whole diff |

### 2.5 C5 — the precedent: a per-actor prefix diff, tombstones first, fail-closed

`multiEntryRetractions` (`evaluate.go:975`, cap-read-per-anchor-grant-keys-design.md §4.2) lists one
actor's child keys under its prefix, tombstones every one the fresh set no longer carries with
`FailClosed` set (`:1043` — a failed tombstone aborts the batch so sibling upserts cannot land past it),
returns the tombstones **ahead** of the fresh rows so `writeResults`' sequential dispatch lands them first,
and skips a candidate already carrying `isDeleted`. §3.2's partition diff is that function with the
partition predicate in place of the prefix and the anchor set in place of the actor — all three
properties carried.

### 2.6 C6 — the cost, measured

Whole-corpus evaluation of this lens: **≈1.6 s per event** at 60 leaseapps (§1.2's log intervals; each
includes the evaluation, three decrypts per row, the whole-target listing and the writes). §11 asks the
build to record `latencyBuf` before and after. Table sizes on the dev stack (`psql`, 2026-09-05):

```
read_landlord_lease_applications  47 rows / 47 distinct app_id / 1 tombstone
read_lease_applications           60 / 60 / 3
read_landlord_units               33 / 16 distinct unit_id / 22 tombstones
actor_read_grants                738 / 193 distinct anchor_id
read_object_identity_attachments  0
read_clinic_patients              2
```

The two NATS-KV lenses' listing cost is unchanged by design (§2.4: the same listing, filtered); the win
on every lens is the evaluation, not the listing.

### 2.7 C7 — the original objection to a scoped diff, and why the partition is not it

`docs/components/refractor.md:899-905` records why the target diff was made whole rather than scoped:
*"sidesteps the ambiguity a per-vertex-scoped diff would hit (an `identity` endpoint can be either the
applicant or the managing landlord role in `read_landlord_lease_applications`, with no single stable id to
scope a prefix-list by)."* That objection is about scoping by the **triggering vertex**, whose pattern
role is ambiguous. This design scopes by the **anchor**: an anchor-typed event names its own partition, a
neighbour event names the anchors the scan-root walk derives (each its own partition), and the identity's
role never enters the predicate. The objection stands against the shape it was written for and does not
reach this one.

### 2.8 C8 — the walk exists, the index is complete, the mode is `act`

`plain_scanroot_corpus_census_test.go:153` pins `landlordLeaseApplicationsRead` as `rootIndexed` (its
pattern graph is complete; the `[(u)-[:containedIn]->(b:building) | …]` comprehension is a non-binding
hop the index walks, `hopindex.go:270-289`), `closureRefused`. `label_derivation_corpus_census_test.go:190`
pins its consumer filter to five labels. The deployment's derivation mode defaults to `act`
(`anchor_derivation_mode.go:109`). Nothing else in the licence chain refuses it: audit enrolled and
reaching verdicts (§1.2), `RowReader` present, one seed label, no `$now`.

### 2.9 C9 — the rebuild argument the grant tables rest on (the reason for §3.7)

`cmd/refractor/main.go:266-273`: *"The lens declares DiffRetraction. The shared grant table implements no
`adapter.Truncater` at all, so the truncate is declined … a diffRetraction lens never seeds its
evaluation, so every replayed event recomputes the lens's complete row set, and `applyDiffRetraction`
deletes every key the target still carries that the fresh set no longer produces."* That is not a comment
about a convenience; it is the **only** shrink path a grant table has on a taxonomy rebuild (a label
narrowing drops anchors whose events the narrowed filter never delivers again). A business Postgres table
and the two owned NATS-KV buckets are `Truncater`s with no prefix (`RebuildTruncateIsScoped` true), so
their rebuild starts from empty and converges by replay; the grant writer's does not, and its whole diff
on every event is what heals it. §3.7 keeps it.

### 2.10 C10 — permission envelope

The Postgres listing runs on the projector pool, which bypasses RLS as a superuser exactly as `ListKeys`
and `GetRow` already do (`postgres.go:466-474`'s doc). The NATS-KV arm issues **no new subscribe**: it is
the listing the lens already performs. `natsperm/matrix.go`'s Refractor row is unchanged.

---

## 3. The shape

### 3.1 The partition conjunct — `PartitionsByAnchor`

A compiled rule **partitions by its anchor** when, with every key column resolved back through the
`WITH` environments to pattern variables (the with-alias design's `resolveThroughWithAliases`, unchanged):

1. **every** key column is a non-aggregating expression over pattern variables — no `collect` / `count` /
   `max` / `min` anywhere in the resolved expression (the four names `aggregate.go:111-117` recognises, so
   nothing the executor aggregates escapes the list), no pattern form (existence test, comprehension), no
   unmodelled node; and
2. **at least one** key column *identifies* the anchor: its own Contract #1 key, or `nanoIdFromKey` over
   it (`exprIdentifiesVariable`, unchanged).

Formally: `anchorProjectionShape` with its `exprReferencesOnlyVariable(resolved, anchorVar)` conjunct
replaced by `exprReferencesOnlyPatternVariables(resolved)` — the same walk, any variable admitted — and
`ProjectsOneRowPerAnchor`'s identification loop kept. The shipped conjunct is the special case where the
set of non-anchor key columns is empty, so `PartitionsByAnchor ⊇ ProjectsOneRowPerAnchor` by
construction, and §2.1's census holds it (54 = 54). **Partition-only** — the set this design acts on — is
`PartitionsByAnchor ∧ ¬ProjectsOneRowPerAnchor`: the 8.

**Why a seeded evaluation is exact for such a rule.** The executor groups every `WITH`/`RETURN` by its
non-aggregating items (`executor.go:1775` `projectItems`); an admitted identifying column resolves through
non-aggregating items only (conjunct 1 refuses an aggregate anywhere in the resolved chain, and
`substituteAliases` (`withalias.go:120-160`) substitutes an aggregating item's own call into the resolved
expression, so it cannot hide behind an alias), so an anchor-identifying value is part of the grouping key
at **every** boundary the column crosses. The `redundant` analysis (`grouping.go:29-56`) drops only an
earlier clause's *aggregating* alias from a key, never an identifying one. A group is therefore confined to
one root binding: no aggregate in any row spans two anchors, and a `DISTINCT` on a boundary cannot merge
rows from two roots. Every non-anchor binding in a row (`landlord` via `u` via `app`) is a function of the
root's binding chain under the pattern, identical whether the root's candidate set is the whole type or
the one seeded vertex: the seed is spent on the first scan-built candidate set and cleared
(`seed_nodes.go:36-42`, `:160-168`), so every later `MATCH`, comma-sibling and comprehension scans as it
would unseeded, and `pointCandidate` (`:120-145`) applies the same label and property filters as the scan.
A deferred `OPTIONAL MATCH` branch (`branchgroups.go:38-46`) is pinned into the product whenever a
non-aggregating item reads it, so a key column can never evaluate on a base row that lacks its binding.
The grammar has no `LIMIT` / `SKIP` / `ORDER BY` / `UNION` (`ast.go` carries none), so no clause can make a
row depend on the size of the root set.

**What the conjunct does not give, and why the design does not need it.** A partition lens's key still
cannot be derived read-free from the anchor (`landlord_id` needs the `manages` walk), so
`AnchorProjectionKey` / `AnchorDeleteResult` keep answering ok=false and the read-free presence check and
root-tombstone shortcut keep declining. The partition replaces them with a **read** of the partition
(§3.2), which is what `DiffRetraction` already pays for — whole.

**The engine surface:** `(*CompiledRule).PartitionsByAnchor() (identifying []string, ok bool)` beside
`ProjectsOneRowPerAnchor`, sharing `anchorProjectionShape`'s body through one parameterised resolver so
the two can never disagree about a column's provenance; and
`(*CompiledRule).PartitionPredicate(eventKey, eventType string) (fixed map[string]any, ok bool)` — the
identifying columns evaluated against a read-free binding of the anchor (`.key` forms need only the key,
never the body: a root-tombstoned anchor still has one), refusing on a nil or node-valued result exactly as
`AnchorProjectionKey` does, and refusing a value that is not a string over the platform key alphabet.

### 3.2 The transport — a partition-scoped target diff

A new optional adapter interface:

```go
// PartitionKeyLister lists the live keys of this lens's OWN rows whose key
// columns match every entry of fixed — a subset of keyOrder. The listing is
// exactly the partition inside the lens's ownership scope: nothing outside
// that scope is ever returned, nothing outside the partition is ever diffed.
type PartitionKeyLister interface {
	ListKeysWhere(ctx context.Context, fixed map[string]any) ([]map[string]any, error)
}
```

- `PostgresAdapter`: `buildListKeysSQL` plus `AND <k> = $n` per fixed column, values bound as parameters
  (no rendering into SQL or into a subject), the tombstone clause on the same condition. `ProtectedAdapter`
  forwards. An empty `fixed` is refused — the caller asked to scope, and answering with the whole table is
  the failure the `ListKeysPrefix` doc already names.
- `NatsKVAdapter`: `ListKeysWhere` runs the **same** listing the whole arm runs — `ListKeys` on an owned
  bucket, `ListKeysPrefix(p.diffRetractionPrefix)` on a shared one (the prefix is threaded at activation
  as today) — and filters the mapped rows in Go by equality on the fixed columns. Two things this buys
  over a subject filter, both from the pass (finding 6): the ownership proof the whole arm rests on
  (*"THE PREFIX IS THE ONLY PROTECTION A LISTED KEY GETS"*, `evaluate.go:1477-1487`) is inherited rather
  than re-derived, and no value ever becomes a subject token (`buildKey` renders values unescaped,
  `natskv.go:306-316`; a `*` or `>` in a filter would *widen* it). The cost is today's listing.
- `GrantWriterAdapter`: not implemented (§3.7).

The pipeline hook, `applyPartitionDiffRetraction(ctx, anchors []string, results)`:

```
for each anchor key a in anchors:
    fixed, ok := rs.cr.PartitionPredicate(a, typeOf(a));  !ok → return error (the event fails; never a wider wipe)
    existing := lister.ListKeysWhere(ctx, fixed)
    for each ex in existing not in results.Keys:
        _, live := reader.GetRow(ctx, ex);  !live → skip (already tombstoned — no rewrite)
        tombstones = append(tombstones, Delete{Keys: ex, FailClosed: true})
return append(tombstones, results...)     // tombstones first, then upserts (§2.5's ordering)
```

An anchor whose predicate cannot be evaluated fails the **whole event** rather than diffing a wider set —
the disposition `derivedRowIsLive` already takes for a probe it cannot answer. `FailClosed` on every
tombstone is carried from the precedent: a failed partition tombstone aborts the batch and the event is
redelivered, rather than the sibling upserts landing past a row that should be gone.

### 3.3 Where the decisions change

**One armed flag, bound at activation, re-checked against the live rule.** `p.partitionRetraction` is set
by `SetPartitionRetraction` **only** when all of: `p.diffRetraction`; the target adapter implements
`PartitionKeyLister` and `RowReader`; the lens is **business plane** (`projection.IsAuthPlane(r)` false,
passed in by the activation gate the way `InstallAudit` and `retractionTransportRefusal` take it — never
read off `p.authPlane`, which is recorded later); and the compiled rule is **partition-only** (§3.1). The
rule half is re-derived at every install into `rs.partition` (§4), and every consumer below reads
`p.partitionArmed(rs) := p.partitionRetraction && rs.partition.only`, so a MATCH reload that stops
partitioning disarms seeding on the next event with no reload refusal. Activation refuses (`refuseLens`,
the `DiffRetraction` guard's disposition) a partition-only business lens whose adapter lacks the lister —
mirroring `SetDiffRetraction`'s `KeyLister` refusal (`pipeline.go:1100-1108`): a lens must never run
seeded with nothing able to scope its diff.

| Decision | Today | After |
|---|---|---|
| `seedAnchorFor` (`rulestate.go:400`) | `if p.diffRetraction { return "" }` | `if p.diffRetraction && !p.partitionArmed(rs) { return "" }` — a partition-armed lens seeds on its anchor's events like any closed lens; a grant table, a closed `DiffRetraction` lens and `wellnessMemberAccounts` are refused exactly as today |
| `plainDerivationIndexRefusal` (`anchor_derivation_plain.go:223`) and its logging twin (`:1025`) | refuses on `p.diffRetraction` | refuses on `p.diffRetraction && !p.partitionArmed(rs)`; the reason names which of the four conditions refused |
| `plainDerivationStaticallyEligible`'s last conjunct (`:458`) | `ProjectsOneRowPerAnchor` | `PartitionsByAnchor` — the refusal string already says *"its rows do not partition by anchor"* and now means it (a `DiffRetraction` lens reaches it only when armed; every other lens's verdict is unchanged, §2.1) |
| `evaluatePlainNeighbourEvent` / `evaluateSeededMultiPosition` → `plainDerivationDecide` (`:804`) | return `results` | return `(results, acted bool)` — `acted` is true iff the derivation substituted per-anchor re-entries; false on every declined path |
| `evaluateForEntryRaw`'s tail (`evaluate.go:330-355`) | `if ok && !contains → Delete; else if !ok && p.diffRetraction → applyDiffRetraction` | `if ok && … (unchanged); else if !ok && p.diffRetraction:` **(a)** `acted` → **no diff on this frame** — each re-entry already diffed its own partition and this frame's `results` is the union of K partitions, which a whole listing must never be compared against; **(b)** `seed != ""` → `applyPartitionDiffRetraction({seed})` (an anchor event, or a derived anchor's re-entry, whose seed is that anchor); **(c)** otherwise → `applyDiffRetraction()` — the evaluation was whole. Defensively, `acted && !p.partitionArmed(rs)` fails the event: it is unreachable (the index refuses first) and a whole diff over a partial set is the one outcome this design forbids. |

The pass's first finding is case (a): without it, a licensed neighbour event's outer frame — `seed == ""`,
`results` = K anchors' rows — would list the whole target and tombstone every other anchor's rows. The
derived path's re-entries carry their anchor as the seed and take case (b) each; the outer frame takes
(a). `evaluateSeededMultiPosition`'s **declined** narrow call has `seed != ""` and `acted == false` and
takes (b) for its own partition — exact, because that call evaluates the vertex *as the anchor* and the
partition is the anchor's; its **acted** call takes (a).

The whole-target diff stays exactly where the evaluation was whole: an unlicensed neighbour event on a
partition-armed lens, and every event on a lens that is not armed. That is what §2.7's objection was about
and it is still answered by the whole listing. `off` mode costs nothing new: seeding on anchor events is
not derivation and is armed by the partition alone.

### 3.4 The anchor's root tombstone

`AnchorDeleteResult` declines (the key needs a walk), so the tombstone falls through to the seeding
decision (`evaluate.go:262-275` → `:285`), which now seeds the tombstoned key; the seeded evaluation
returns zero rows (`executor.go:1095` — `fetchNode` returns nil for an `isDeleted` root), and the tail's
case (b) tombstones every row of the partition. One path for both the live drop-out and the tombstone.
`PartitionPredicate` needs only the key, so the tombstone's body (preserved by `step8_commit.go`, absent
only for a hard-purged key the CDC never delivers) is never on the path.

### 3.5 The audit's should-not-exist direction — same increment

`auditAnchor` (`audit.go:841-847`, and the tombstoned arm at `:773-797`) asks `AnchorProjectionKey` and,
when it declines, *"the anchor is simply not checked in this direction."* For a partition-armed lens it
instead lists the partition through the same `PartitionKeyLister`: a tombstoned anchor with any live key
in it, or a live anchor whose partition holds a key the recompute did not produce, books
`AuditClassRetained`. The audit stays detect-only; this widens the direction it can speak in on exactly
the lenses whose seeding this design arms, so a seeded evaluation that under-produces is *named* by the
standing detector rather than silently converted into tombstones (§2.3).

### 3.6 What the operator sees

`PlainRetractionTransport` publishes a fifth value, `RetractionTransportDiffRetractionPartition =
"diffRetraction-partition"`, when `p.diffRetraction` holds and the lens is partition-armed; the heartbeat
copies any transport string verbatim (`derivationstatus.go:70-76`) and alerts only on `none` /
`unclassified` (`lattice_heartbeater.go:2561-2571`), so no consumer changes — nothing in `cmd/loupe` or
`scripts/lint-*.go` reads the enum (grepped 2026-09-05). The two documents that enumerate the value set
gain the fifth (§10). The census pins for the five business lenses move to it. `DerivationEligible` /
`DerivationArmed` become true for them by the same path every other licensed lens takes.

### 3.7 What stays as it is, and why

- **The three auth-plane grant lenses** (`providerIdentityReadGrants`, `staffReadGrants`,
  `patientIdentityReadGrants`) partition but stay on the whole, `grant_source`-scoped diff — by **three**
  independent exclusions (the pass's finding 2 showed a rule-only conjunct would have armed them):
  `SetPartitionRetraction` refuses the auth plane; `GrantWriterAdapter` implements no `PartitionKeyLister`;
  and the activation gate never calls `SetPartitionRetraction` for `IsAuthPlane(r)`. The reason is §2.9:
  the whole diff on every event is the grant table's only shrink path on a taxonomy rebuild, because its
  target cannot be truncated. A follow-on that gives the grant writer a rebuild-time whole diff (or a
  truncate) can then admit them with the predicate already in place; §12 files it. Cost of leaving it:
  one `ListGrantsBySource` (≤ 738 rows live) per event on three lenses — no measured harm.
- **The closed `DiffRetraction` lenses** (`clinicPatientsRead`, `consoleOperatorReadGrants`,
  `demoOperatorReadGrants`) are excluded by the partition-**only** conjunct (§3.1) and, for the two grant
  tables, by the plane as well. `clinicPatientsRead`'s declaration is a ratified *continuous healer* of
  the lost-anchor-event channel (secure-plain-lens-retraction-and-audit-design.md §4.3, and its `:364`:
  *"seeding disabled … stays until the deferred repair lands"*), which only a whole diff heals; the
  partition transport applies where `AnchorProjectionKey` structurally declines — the gap
  `DiffRetraction` was introduced for (`refractor.md:890`) — and nowhere else. §8 row 5.
- **`wellnessMemberAccounts`** stays whole-diff, whole-rescan: its rows partition by the identity, not the
  booking it anchors on. The fix there is a package edit (anchor on `id:identity`, walk `bookedBy` inbound
  as an existence test) that makes it closed outright — a verticals-lane row, filed with this design; §8
  row 2.
- **`ValidateUnanchoredForDiffRetraction`** is unchanged and still required: the whole diff still runs on
  unlicensed neighbour events, and it is exact only for an unanchored query.
- **Hot reload** keeps refusing a `DiffRetraction` flip (`reload.go:147`) and a prefix change (`:820`).
  Partition-capability's rule half is re-derived at every install (§4); its adapter/plane half is bound
  before `Run` like the diff itself and cannot change under a reload that is admitted.

---

## 4. State

**New state:** `ruleState.partition` — `{only bool; identifying []string}` derived at `installRule` beside
`seedAnchorLabels` (`ruleinstall.go:425-438`) from the compiled rule alone; and `p.partitionRetraction`
(bool), bound before `Run` by `SetPartitionRetraction` from the adapter, the plane and the rule at
activation.

| Boundary | `rs.partition` | `p.partitionRetraction` |
|---|---|---|
| created | every rule install / MATCH hot-reload, from the compiled rule (no event, no adapter) | activation, after `SetDiffRetraction` and the shared-target scoping, before `Run` |
| reset | unconditionally on the next install — a reload must never leave a previous body's verdict standing (as `seedAnchorLabels`, `personalClockRefusal`) | never while running — like `diffRetraction`, an edit that would change its inputs is refused or re-activates |
| carried | in the copy-on-write `ruleState` snapshot every predicate reads; `TestRuleState_RoundTripCarriesEveryField` discovers the field and fails the build until it is carried | a pipeline field read under the same discipline as `diffRetraction` |
| ordered | a pure function of the rule; the seed and the tail read the **same** snapshot for one event | — |
| crash / replay / rebuild | nothing persisted; a rebuild's replay seeds every anchor event and diffs its partition against a table `Truncate` emptied (`RebuildTruncateIsScoped` true for an owned target, §2.9), converging like the closed lenses' replay | — |

**The retraction state table** (the outcome column is what the target holds after the event; "armed" =
`p.partitionArmed(rs)`):

| Case | Evaluation | Diff | Outcome |
|---|---|---|---|
| never matched, anchor event, armed | seeded, 0 rows | partition listing empty | nothing written (no spurious tombstone) |
| matched, anchor event, armed | seeded, *n* rows | partition = fresh | *n* upserts |
| matched-then-shrunk (a co-manager's `manages` removed — neighbour event, licensed), armed | derived anchors, each re-entered and seeded; outer frame `acted` | each re-entry: its partition; outer frame: **none** | one tombstone, the rest upserted, every other anchor's rows untouched |
| matched-then-shrunk, neighbour event **unlicensed**, armed | whole corpus | whole target | today's behaviour, exact |
| matched-then-gone (a required link removed on the anchor — anchor-side link event), armed | seeded, 0 rows | partition = all rows | every row tombstoned |
| anchor root-tombstoned, armed | seeded on the tombstoned key, 0 rows | partition = all rows | every row tombstoned (§3.4) |
| re-run of any of the above | idempotent: a tombstoned key is skipped, an equal upsert is guard-declined | — | unchanged |
| predicate not derivable for an anchor (never for a `.key` form; the guard exists for a future identifier) | — | — | the event fails and is redelivered; nothing deleted |
| a partition tombstone's write fails | — | — | `FailClosed`: the batch aborts, the event is redelivered; no sibling upsert lands past it |
| **never-written**: a partition-only rule on a target with no lister (the grant writer), or on the auth plane, or a closed `DiffRetraction` lens | **not armed** ⇒ whole corpus | whole | today's behaviour; the transport publishes `diffRetraction`, not `-partition` |
| a MATCH reload makes the rule stop partitioning | `rs.partition.only` false ⇒ not armed on the next event | whole | today's behaviour, no reload refusal |

---

## 5. Executable censuses (the build's Phase 0 re-runs these)

1. **The partition census** — `TestPlainPartitionCensus` in `internal/refractor` (next to
   `plain_with_alias_closure_census_test.go`, same `forEachCorpusCypher` + `closureKeyColumns`), pinning
   per lens `{oneRowPerAnchor, partitions, identifying, diffRetraction}` for all 65 with a floor of 65,
   and asserting the three memberships of §2.1: the 8 partition-only names, `partitions ⊇ oneRowPerAnchor`
   (a lens the old conjunct admits and the new refuses fails by name), and the 3 refusals. Expected
   result: §2.1's table (54 / 8 / 3).
2. **The `DiffRetraction` population** — `grep -rn "DiffRetraction: *true" packages | grep -v _test` →
   12 sites (§2.1 names them); the census asserts every partition-only lens is one of them.
3. **The transport census** (`plain_retraction_transport_corpus_census_test.go`) — the five business
   lenses move to `diffRetraction-partition`; the three auth-plane ones and the three closed ones stay
   `diffRetraction`; nothing else moves. Floor unchanged.
4. **The consumer census of the declaration** — `grep -rn "diffRetraction\|DiffRetraction"
   internal/refractor internal/pkgmgr cmd/refractor docs --include=*.go --include=*.md | grep -v _test`
   → the sites of §2.2; Phase 0 re-runs it and the build note classifies each as decision / publication /
   refusal / validator / comment.
5. **Mutation pin (correctness gate, Increment 1):** `TestPartitionsByAnchor_RefusesWithoutIdentifyingColumn`
   — the landlord cypher with `app_id` replaced by a literal, and with `nanoIdFromKey(u.key)`, is refused;
   with `count(…)` in a key column is refused; `WITH *` is refused; the real cypher is admitted with
   `identifying = [app_id]`, and `objectIdentityAttachmentsRead`'s `type(r)` key column is admitted as a
   function over a relationship variable.

---

## 6. Contract surface

None changes. `grep -n -i "retract\|DiffRetraction\|partition\|narrow" docs/contracts/*.md` (2026-09-05,
re-run by the reviewer) returns Contract #6 §6.13's per-entry prefix diff, §6.14's grant-table soft
tombstone, Weaver's "targets never retract" note and unrelated uses of "partition" (event domains, key
namespaces). The design **builds to** Contract #6 §6.14 — *"a revoke is a seq-guarded soft tombstone
(`is_deleted = true`), not a hard `DELETE`"* — every `Delete` here is the same guarded statement
`buildDeleteSQL` already emits for a protected table. Which listing produced the key is mechanism.

---

## 7. Reconciliation with the existing mental model

- *Didn't the with-alias design already handle this?* It resolved the **name** half — a `WITH` no longer
  hides which variable a key column binds — and stopped, correctly, at a key column that *really* binds a
  neighbour. This is the partitionability half its §5.1 and §8 named as separate; both are now pointed
  here in place (§10).
- *Isn't `DiffRetraction` the mechanism for exactly these lenses?* It is the transport; its **scope** was
  whole because nothing could prove a narrower one exact (§2.7). The partition conjunct is that proof, and
  the transport keeps its declaration, its activation guard and its whole-listing arm.
- *Does this duplicate the audit's seeded recompute?* No — it reuses the fact the audit relies on and
  gives the audit the direction it lacks (§3.5). One predicate, three consumers (seed, licence, audit).
- *Does this add state we keep elsewhere?* `rs.partition` sits beside `seedAnchorLabels` and is derived the
  same way; `p.partitionRetraction` sits beside `diffRetraction` and is bound the same way. No persistence,
  no new bucket, one new enum value.
- *Why not fold the conjunct into `AnchorProjectionKey`'s ok?* Because its consumers want a **key**, and a
  partition lens has none read-free; folding it in would hand the presence check a partial key — the
  exact wrong-Delete `anchorProjectionShape`'s comment refuses.

---

## 8. Alternatives

| # | Alternative | Verdict |
|---|---|---|
| 1 | **Do not have the transport at all — delete `DiffRetraction` from the 8 lenses and make them closed.** The two landlord lenses could collapse to one row per anchor with `authz_anchors = collect(landlords)` (RLS unions the array natively, §6.14), which would make them bucket-A lenses needing nothing here. But `providerSites`, `duplicateCandidates` and `objectIdentityAttachmentsRead` are keyed on a **relationship instance** by product shape (a pair, an attachment link), and the three grant tables' `(actor, anchor, source)` row is the RLS contract itself — six of eight cannot be rewritten into closure. Two package rewrites plus a platform mechanism for the rest is more machinery than the mechanism alone. **Rejected; the landlord collapse remains a fine package edit and is not required.** |
| 2 | **Rewrite `wellnessMemberAccounts` instead of generalising the predicate further** (partition by a non-anchor variable). A "partition by *any* identifying variable" conjunct would admit it, but the seed is the anchor pattern by engine construction, so the lens would still not seed. The package fix — anchor on the identity — is the right shape and belongs on the verticals lane. **Rejected as platform work; filed as a verticals row.** |
| 3 | **Derive the composite key read-free through the adjacency index** (walk `appliesToUnit` → `manages` from the anchor in `AnchorProjectionKey`). Re-implements the pattern's semantics per lens outside the executor, is exact only for lenses whose non-anchor key columns are one hop away, and still needs a read. The partition listing is one read and exact for every shape §3.1 admits. **Rejected.** |
| 4 | **Scope the diff to the evaluated anchors without a partition predicate — trust the seed.** Unsound: on `wellnessMemberAccounts` a booking-seeded evaluation yields one identity's row, and "delete everything else with this booking's…" has no column to scope on; the predicate is what makes the scope a *set the rule owns*. This design **is** row 4 plus the proof. |
| 5 | **Also let the closed `DiffRetraction` lenses seed** (their partition is their single row). Would remove the "continuous healer" the secure design kept for `clinicPatientsRead` on purpose (§3.7), for a lens with 2 rows and no measured cost. **Rejected; excluded by conjunct.** |
| 6 | **Skip the `derivedRowIsLive` probe for partition tombstones** (they come from a listing). Saves one `GetRow` per tombstone on the derived path. The probe's non-`RowReader` arm drops a Delete silently (`anchor_derivation_plain.go:719-724`) — fail-safe for a walk-derived Delete, an under-retraction for a listing-derived one — but that arm is unreachable behind the licence's `RowReader` conjunct, and `SetPartitionRetraction` requires `RowReader` too. Tombstones are rare. **Rejected; the difference is named here rather than hidden behind "uniformity".** |
| 7 | **Include the auth-plane grant tables now.** The predicate admits them and `GrantWriterAdapter` could list `WHERE actor_id = $1 AND grant_source = $2` trivially. But §2.9: their whole diff on every event is the only shrink path an un-truncatable target has on a rebuild. **Deferred to its own row (§12), with the reason and the measurement attached.** |
| 8 | **Build the NATS-KV arm as a server-side subject filter** (`KVListKeysFilter` with `*` at unfixed segments). Needs the `kvStore` interface widened and four test doubles updated, creates an ephemeral consumer per event, and renders values into subject tokens with no escaping — a `*`/`>` in a value widens the delete scope. Filtering the owned listing in Go is exact, inherits the ownership prefix, and costs what today costs. **Rejected (the pass's findings 6 and 8).** |
| 9 | **Do nothing.** The 8 lenses pay O(corpus) per event — 1.6 s at 60 leaseapps, growing with every leaseapp, unit and grant — and the verticals row stays blocked. **Rejected.** |

**The dead-scaffolding test:** Increment 1's consumer is live today (five business lenses, one of them
blocking a filed verticals row, all in `act` mode with enrolled audits); nothing is stubbed.

---

## 9. Migration, compatibility, risks

- **No package edit.** The declarations, the cypher and the tables are unchanged; behaviour changes at the
  next Refractor cycle. No version bump, no reload refusal (the `DiffRetraction` flag does not flip).
- **Index shape.** Postgres's partition listing is a PK-prefix scan when the identifying column leads
  `IntoKey` — true for the three business Postgres lenses. A future lens whose identifying column is not
  the leading key column is correct and does a full-PK scan; the build logs it at activation.
- **Risk — a wrong partition predicate is a wrong `Delete` on an RLS table.** Bounded four ways: the
  conjunct is `.key`-forms only (an unrecognised identifier refuses, fail-closed); the predicate's value is
  platform-owned (the anchor key the CDC event carries, never a payload field); an unevaluable predicate
  fails the event; and the value never reaches a subject or a SQL string un-bound. §5's mutation pin holds
  the first; a fixture that mutates the predicate to a wrong column and asserts no sibling row is touched
  holds the third.
- **Risk — the whole-diff arm regresses.** It is untouched code reached under the same condition as today
  minus the seeded and acted cases; the transport census, the existing diff fixtures and §11's
  unlicensed-neighbour fixture pin it.
- **Risk — the derived path's cap.** A neighbour event reaching more anchors than
  `plainDerivedAnchorCap` falls back to the whole rescan and whole diff, exactly as any other lens.
- **Risk — the rebuild argument.** Business Postgres tables and the two NATS-KV buckets truncate on
  rebuild (`RebuildTruncateIsScoped`); the replay seeds and partition-diffs. The grant tables, whose
  rebuild rests on the whole diff, are not armed (§2.9, §3.7).

---

## 10. Consumer table (§2.3 checklist B/C — run once per increment)

| Site | Reads | Increment 1 | Increment 2 |
|---|---|---|---|
| `seedAnchorFor` (`rulestate.go:400`) | `p.diffRetraction` | conjunct becomes `&& !p.partitionArmed(rs)` | — |
| `plainDerivationIndexRefusal` (`:223`), `noteStaticPlainDerivationRefusal` (`:1025`) | same | same; reason strings name the refusing condition | — |
| `plainDerivationStaticallyEligible` (`:458`) | `ProjectsOneRowPerAnchor` | `PartitionsByAnchor` | — |
| `plainDerivationDecide` and its two callers | results | `(results, acted)` | — |
| `evaluateForEntryRaw` tail (`evaluate.go:330-355`) | `p.diffRetraction`, seed | cases (a)/(b)/(c) + the defensive refusal | — |
| `evaluatePlainDerivedAnchors` | Delete probe | unchanged (probe re-reads partition tombstones; §8 row 6) | — |
| `auditAnchor` should-not-exist (`audit.go:773-797`, `:841-847`) | `AnchorProjectionKey` | partition-listing arm for armed lenses | — |
| `auditEnrolment` comment (`audit.go:1046-1056`) | prose | rewritten: the `retained` direction now exists for armed lenses | — |
| `SetDiffRetraction` (`pipeline.go:1100`), activation guard (`main.go:1608-1627`) | `KeyLister` | `SetPartitionRetraction` added after it, with its own refusal; called by the activation gate for business-plane lenses only | — |
| `admitRetractionTransport` (`retractiontransport.go:130-185`) | plane, siblings, prefix | calls `SetPartitionRetraction` after the shared-target scoping (the prefix must be bound first — the NATS-KV `ListKeysWhere` reads it) | — |
| `PlainRetractionTransport` (`retraction_transport.go:111`), `copyLensRetractionTransport` | transport enum | new value | — |
| `hotReloadRefusal` (`reload.go:147`, `:820`) | flag / prefix flip | unchanged | — |
| `SharedTargetDiffRefusal`, `siblingLensOf`, `SetDiffRetractionPrefix` | flag + prefix | unchanged (the partition listing on a shared bucket goes through the prefix listing) | — |
| `ValidateUnanchoredForDiffRetraction` | `$actorKey` | unchanged, still enforced | — |
| `pkgmgr/bucketguard.go:165-177`, `corekv_source.go:1460,1492,1526`, `pkgmgr/build.go:508,552` | declaration validate / serialize | unchanged (no declaration surface changes) | — |
| `cmd/refractor/main.go:266-273` (rebuild-truncate argument) | prose, load-bearing | rewritten: *"a diffRetraction lens never seeds"* becomes *"an un-truncatable diffRetraction target is never partition-armed, so its replay recomputes the whole row set"* | — |
| `rulestate.go:385-389`, `refractor.md:890-915`, the eight lens declarations' "can never derive … read-free" notes | prose | rewritten to the new posture (the `read-free` claim stays true; "never seeds" does not) | — |
| `with-alias-anchor-closure-design.md` §8 bullet 1 and §10's "the partitionability half stands" row; `plain-lens-neighbour-anchor-derivation-design.md` §5.1's "a separate design and not this one" | superseded design text | **pointed here in place by this fire** (docs-only, same commit) | — |
| `docs/observability/health-kv-schema.md:583`, `secure-plain-lens-retraction-and-audit-design.md:455` | the enum's documented value set | gain `diffRetraction-partition` | — |
| `cmd/loupe`, `scripts/lint-*.go` | transport enum | no reader (grepped 2026-09-05) | — |
| `NatsKVAdapter`'s `kvStore` interface and its four test doubles | — | unchanged (§8 row 8) | — |
| **Increment 2** — the census pins moved by Increment 1 are re-pinned; the component doc's dossier entry (§13) lands | | | pins + docs |

---

## 11. Test strategy

- **Engine (Increment 1):** `PartitionsByAnchor` vectors — the landlord cypher (admitted, `[app_id]`),
  `landlordUnitsRead`, `objectIdentityAttachmentsRead` (`type(r)` admitted), `duplicateCandidates`;
  refused: `wellnessMemberAccounts`, `capabilityRoleIndex`, a literal key, an aggregate key, `WITH *`, an
  aggregate hidden behind a `WITH` alias; `PartitionPredicate` on a tombstoned root body, on a nil key,
  and on a value outside the key alphabet.
- **Adapter (Increment 1):** `ListKeysWhere` on Postgres (soft-tombstone excluded; leading and
  non-leading fixed column both correct; empty `fixed` refused) and NATS-KV (composite key filtered from
  the whole listing; from the prefix listing on a shared bucket with a sibling's same-segment-count key
  present and never returned; a value carrying `*` never widens anything) — mirroring `ListKeysPrefix`'s
  tests.
- **Pipeline (Increment 1), NATS-backed:** the state table of §4 row by row on a landlord-shaped fixture
  with two co-managers and **two** applications: create (2 upserts), one `manages` removed via a
  **licensed neighbour event** (1 tombstone, 1 upsert, the other application's rows untouched — the
  finding-1 assertion, with the outer frame's whole diff proven not to run by a listing-call counter on
  the adapter double), the application tombstoned (2 tombstones), redelivery idempotent, a failing
  tombstone aborting the batch; the unlicensed neighbour event (audit suppressed) still taking the whole
  diff; a grant-table fixture asserting `seedAnchorFor` returns "" and the whole source-scoped diff runs
  (the finding-2 assertion); a closed `DiffRetraction` fixture asserting the same. Plus the mutation
  fixture of §9.
- **Audit (Increment 1):** tombstoned anchor with a live partition key → `retained`; live anchor with an
  extra key → `retained`; clean partition → nothing; a non-armed lens unchanged.
- **Corpus:** §5's censuses; the transport census's pins updated.
- **Live (close pass):** cycle `bin/refractor`, submit `CreateLeaseApplication` on the dev stack, and
  read `refractor.log` for the lens: the derivation-refusal line gone, `pipeline: processed` for the
  leaseapp's own events at seeded cost (record `latencyBuf` before/after); then withdraw a co-manager's
  `manages` link and read `read_landlord_lease_applications` for the single tombstone with every other
  row's `projection_seq` unchanged. Prove by the positive verdict (`DerivationArmed=true` on the heartbeat,
  a `Delete` outcome in the log), never by a refusal's absence.

---

## 12. Decomposition for the Steward

**Increment 1 — the conjunct, the listing, the scoped diff, the seeding, the audit direction
(posture-changing; full-depth review on the retraction seam).** `PartitionsByAnchor` +
`PartitionPredicate`; `PartitionKeyLister` on Postgres, Protected, NATS-KV; `rs.partition` at install;
`SetPartitionRetraction` + the activation-gate call; the decision sites of §3.3 including `acted`; the
audit's partition arm (§3.5); the transport value; §5's censuses 1–5 and §11's engine / adapter /
pipeline / audit tests; the comment rewrites of §10 (the rebuild argument first); the component doc's
target-diff paragraph rewritten and the two enum docs updated. Owns every test named in §11.

**Increment 2 — close (S).** The live close pass of §11; the census pins re-run at the merge base; the
dossier entry for `docs/components/refractor.md` (§13); the build note.

**Filed, not built here:** (a) *[Refractor] auth-plane grant tables — partition-scoped seeding*: the
predicate admits `providerIdentityReadGrants`, `staffReadGrants`, `patientIdentityReadGrants`; blocked on
a rebuild-time whole diff (or a truncate) for the grant writer, which today's per-event whole diff is
standing in for (§2.9); needs `GrantWriterAdapter.ListKeysWhere` and the gate's plane conjunct lifted.
(b) *verticals: `wellnessMemberAccounts` anchors on the booking it does not key on* — re-anchor on the
identity and drop `DiffRetraction` (§8 row 2). Both filed as one line each in this design's commit.

---

## 13. Checklist walk (`agents/designer/SKILL.md` §2.3) and the adversarial pass

**A — demand.** Mechanism grounded before premise (§1.2: three consumers, not one); the row's numbers
re-measured in the units of the wire (§2.6); the "refused twice" precedent in with-alias §8 re-read and
found to describe a *different* conjunct order than the one that binds; the reassuring negative "no
consumer is waiting" falsified (§1.3.4); the lint corpus grepped for the enum (none).
**B — channels.** Transport named per adapter with the code that carries it (§2.4); retraction has a
transport in both directions (grow = upsert, shrink = partition tombstone) and per target the write guard
is the one already there; the replaced write — the whole listing on a scoped evaluation — is removed in
the same design (§3.3 (a)/(b)); the machinery bent (the executor's seed) was read for whether it bends
(§3.1); the permission envelope cited (§2.10); no new import edge; no new substrate interface.
**C — censuses.** Run, pasted, floored, corrected after the pass (§2.1: 54/8/3, not 51/8/3); the
declaration counted by grep and by the census both (12 sites); the consumer table run for the increment
that changes shape and widened past `internal/refractor` after the pass (§10).
**D — predicates.** State table before predicate (§4) with never-written, re-run, failure and reload
rows; omission fails closed (an unevaluable predicate fails the event; a missing lister or the wrong
plane means *not armed*, never *list whole and hope*); the borrowed predicate's original tolerance
checked (the audit's read-only use of the seeded recompute is one-directional — §2.3 — so the licence
carries §3.1's argument and the audit gains the missing direction in the same increment); the guard's
justification re-evaluated against this design (§2.7, §2.9); no caller-owned value in the predicate.
**E — state, cost.** Lifetime tables (§4); cost measured with units (§2.6); no new budget, clock or
consumer.
**F — shape.** Row one of §8 is deletion; the doc's own objection to a scoped diff quoted verbatim before
departing from it (§2.7); "mirrors X" read twenty lines above X (`multiEntryRetractions`'s `FailClosed`
and tombstone-skip both carried, §2.5/§3.2).

### The adversarial pass (2026-09-05, one cold reviewer, read-only, worktree census re-run)

Thirteen findings; what each changed:

1. **BLOCKING — the outer frame of a licensed neighbour event still ran the whole diff against K
   partitions' rows.** `evaluateForEntryRaw`'s switch falls through to the tail with `seed == ""`; the
   derived path's results are K anchors' rows; the whole listing would have tombstoned every other
   anchor's rows — reachable the moment the index conjunct lifted. **Folded:** `plainDerivationDecide`
   reports `acted`; the tail's case (a) runs no diff on an acted frame; the defensive refusal for
   `acted && !armed`; the finding-1 pipeline fixture with a listing-call counter (§3.3, §11).
2. **BLOCKING — a rule-only conjunct armed seeding on the five `GrantTable` `DiffRetraction` lenses,
   whose adapter has no partition lister; the seeded row set would have met `ListGrantsBySource`'s
   whole source — a mass revoke on `actor_read_grants`.** §4's never-written row had asserted the
   opposite. **Folded:** `p.partitionRetraction` bound at activation from adapter + plane + rule, three
   independent exclusions for the auth plane, the activation refusal, the grant-table fixture (§3.3,
   §3.7, §11); and §2.9 records the rebuild argument that makes the hold-out a reason rather than a
   posture.
3. **MAJOR — §3.7 said the closed `DiffRetraction` lenses were untouched while §3.3's conjunct armed
   them, silently reversing the secure design's ratified `clinicPatientsRead` decision on a PHI table.**
   **Folded:** the partition-**only** conjunct (§3.1) and the closed-lens fixture.
4. **MAJOR — the census table did not match the test output** (54 lenses satisfy `ProjectsOneRowPerAnchor`,
   not 51; the buckets summed to 62; `capabilityRoleIndex`'s stated reason was not the predicate's).
   **Folded:** §2.1 rewritten from the output; §5's pins take the corrected numbers.
5. **MAJOR — the audit was cited as live proof of §3.1 in the one direction that cannot falsify it**
   (should-exist only; should-not-exist declines for all 8). **Folded:** §1.3.3 and §2.3 restated; §3.5
   moved into Increment 1 as the detector for under-production.
6. **MAJOR — a subject-filter NATS-KV listing dropped the ownership proof the whole arm rests on and
   rendered values into subject tokens unescaped.** **Folded:** the NATS-KV arm filters the owned
   (whole or prefix) listing in Go (§2.4, §3.2, §8 row 8); `PartitionPredicate` refuses a value outside
   the key alphabet.
7. **MAJOR — the consumer table missed the activation guards, the prefix binding, the reload
   prefix refusal, the pkgmgr validators/serializers, `sharedtarget.go` and the enum docs**, and the
   census grep's scope excluded them. **Folded:** §2.2, §5 census 4 and §10 widened.
8. **MAJOR — the NATS-KV arm as specified was unreachable (`kvStore` declares no filter listing) and its
   "server-side, bounded" cost claim was untested** (an ephemeral consumer per call, drained client-side).
   **Folded:** dissolved by finding 6's fix — no interface widening, today's listing cost.
9. **MINOR — the pseudo-code dropped `FailClosed`.** Folded (§3.2, §2.5, a §4 row, a §11 fixture).
10. **MINOR — §8 row 6 called the probe "harmless" on uniformity grounds without naming that its
    non-`RowReader` arm under-retracts a listing-derived tombstone.** Folded (row 6 names it and why it is
    unreachable).
11. **MINOR — a dead single-column-key arm in §3.2** (no admitted lens has one; a single identifying key
    *is* closure). Removed.
12. **MINOR — the superseded sentences in two prior designs and the two enum-documenting files were not
    named.** Folded (§10; the two prior designs are pointed here in this fire's commit).
13. **MINOR — five line-number drifts.** Corrected.

**What the pass tried to break and could not** (the evidence the ratification rests on): an aggregate
hidden behind a `WITH` alias (`substituteAliases` inlines the call); grouping-key erasure by the
`redundant` analysis (drops aggregating aliases only); `stagePlan` branch decomposition (a non-aggregating
key column pins its branch into the product; the anchor clause is pinned separately); a whole-corpus scan
surviving the seed (the seed is spent on the first scan and cleared; `pointCandidate` filters as the scan
does); a row depending on the root set's size (no `LIMIT`/`SKIP`/`ORDER BY`/`UNION` in the grammar); a
`$now`-bearing or lossy identifying column (`exprIdentifiesVariable` admits `.key` forms only); the one
live lens (its `WITH` groups by `entityKey` **and** `landlordKey`, the comprehension is non-key); and the
blast radius of the licence conjunct swap (every `closed=false` lens but `capabilityRoleIndex` declares
`DiffRetraction`, and that one is `partition=false`). **Not verified by mutation:** the reviewer did not
construct a counterexample cypher; §5's mutation pin is the build's, and its vectors are written from the
attacks above.

**Dossier candidate for `docs/components/refractor.md`** (the build's close pass appends it, the second
sighting of the "lifted refusal reveals the conjunct behind it" class): *lifting a conjunct that was
keeping a path unreachable — here `plainDerivationIndexRefusal`'s `diffRetraction` — re-arms every
downstream consumer of the state that path produces; enumerate the consumers of the RESULT (the tail's
diff over `results`), not only the consumers of the flag.*
