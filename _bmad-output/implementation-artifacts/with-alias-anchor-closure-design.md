# A `WITH` is not a closure refusal — resolve the key column through its aliases

**Status: ✅ Andrew-ratified 2026-09-02 — build as designed, §13's three increments in order** · Designer fire 2026-09-02 · Component: **Refractor**
(`internal/refractor/ruleengine/full`, `internal/refractor/pipeline`) · Board row: *[Refractor] A `WITH`
refuses per-anchor closure wholesale…* (`backlog/lattice.md`, Read-model / projection maturity)

---

## 0. For Andrew — ratified 2026-09-02, unchanged

Andrew ratified this as written: no scope change, no branch taken, nothing below superseded. The block
stands as the record of what was signed off.


`anchorProjectionShape` (`internal/refractor/ruleengine/full/anchor_delete.go:181-185`) refuses **any**
query carrying a `WITH`, wholesale, before it ever looks at a key column. That one predicate is the
structural half of three things: the plain-lens **narrowing licence**, the plain-lens **filter-retraction**
Delete, and the divergence audit's **should-not-exist** direction. This design replaces the wholesale
refusal with the two things the codebase already has — the general variable-scope walk (`withscope.go`)
that proves no name was stranded, and an **alias resolution** pass that substitutes a key column's RETURN
expression back through the `WITH` items to an expression over pattern variables. The existing conjuncts
(`exprReferencesOnlyVariable`, `exprIdentifiesVariable`) then run **unchanged** on the resolved expression.

**Three lenses move, measured** (§2.1's census, re-runnable): `leaseApplicationsRead`, `renewalsRead`,
`clinicPatientsRead`. Nothing else in the 65-lens plain corpus moves, and no lens that should stay refused
is admitted.

**Two of those three have no retraction transport at all today.** `leaseApplicationsRead` and
`renewalsRead` declare neither `DiffRetraction` nor an anchor-derivable key, so
`evaluate.go:320-338`'s `if ok … else if !ok && p.diffRetraction` takes **neither** branch.
`WithdrawLeaseApplication` (`packages/lease-signing/ddls.go:239-246`, granted to `operator` **and** to
`consumer` self) soft-deletes a leaseapp; the whole-corpus rescan stops upserting it and nothing deletes
the row, so the withdrawn application stays in the RLS-protected `read_lease_applications` forever and the
applicant's own FE keeps rendering it. That is a live correctness gap on a protected read model, not a
performance item.

### 0.1 Fork / contract check — honest answer: neither

- **Architectural fork: none.** This is a mechanism-level widening of one predicate inside one engine
  package, resolved in this document per `agents/designer/SKILL.md` §3 ("which licence conjunct, which
  index shape, which entry point" is not Andrew-altitude).
- **Frozen-contract change: none.** No `docs/contracts/*` file states a promise about `WITH`, about which
  lenses retract, or about the narrowing licence. `06-capability-kv.md:465-468` enumerates the
  **doc-mode / actorAggregate** retract paths, which this design does not touch. Nothing is staged
  uncommitted for you.
- **What is genuinely worth your 60 seconds:** the predicate being widened is the one that authorizes a
  `Delete` against live rows in **PHI/PII-carrying RLS tables** (`read_clinic_patients`,
  `read_lease_applications`, `read_renewals`). §4.4 argues the widening is fail-closed by composition and
  §10 pins it by mutation; §7's first row prices doing nothing. If you would rather this land behind a
  narrower first increment, §13's Increment 1 is already the licence-only slice.

### 0.2 What I have to correct — in the board row, and in three shipped comments

The row I picked up said *"the Postgres pair projects N rows per anchor… `leaseApplicationsRead` anchors on
the identity and keys rows by application, so `ProjectsOneRowPerAnchor` is false by shape."* **All three
clauses are wrong**, and the row title has been rewritten:

1. `leaseApplicationsRead` anchors on **`app:leaseapp`** — the first MATCH node
   (`packages/lease-signing/lenses.go:1078-1084`), not the identity.
2. Its `IntoKey` is `["app_id"]` and `app_id` resolves to `nanoIdFromKey(app.key)` — one row per anchor,
   keyed by the anchor, and the key **identifies** it. It satisfies the partition property on the merits.
3. It is refused because `anchorProjectionShape` returns false on the `WITH` at line 181, **before** the
   key-column loop runs. The refusal string the operator reads — *"its rows do not partition by anchor (no
   key column both resolves from the anchor alone and identifies it)"* — is **false about this lens**,
   exactly the way the varlength and untyped-hop refusal reasons were false about theirs.

The lens the row's *title* describes does exist — `landlordLeaseApplicationsRead`, whose composite
`(app_id, landlord_id)` key genuinely binds a non-anchor variable — but it is refused two conjuncts
**earlier** (Secure Lens), so the partition conjunct never speaks about it. §8 keeps that family open.

Three shipped comments also become stale and are corrected by this fire (§9): `packages/clinic-domain/
lenses.go:308-334` (whose "same shape" citation of `landlordUnitsRead` is itself inaccurate —
`landlordUnitsRead` carries no `WITH`; it is refused on its composite key), `docs/components/
edge-manifest.md:113`, and `staff-descriptor-rendering-design.md`'s package-authoring rule *"the cypher
must NOT contain a `WITH` clause"*.

---

## 1. The mechanism today

`anchorProjectionShape` (`anchor_delete.go:168-247`) answers the half of the per-anchor question that is
decidable from a compiled rule alone: *no `WITH` clause*, a labeled anchor pattern, and every key column a
RETURN alias whose expression references no variable but the anchor's. It has exactly three consumers:

| Consumer | Site | What the refusal costs |
|---|---|---|
| Plain-lens **narrowing licence** | `pipeline/anchor_derivation_plain.go:341-343` | every neighbour event re-runs the **whole-corpus** rescan instead of the derived anchor set |
| Plain-lens **filter-retraction** | `pipeline/evaluate.go:320-338` | no `Delete` when an anchor drops out of the matched set; falls to `DiffRetraction` if declared, otherwise **to nothing** |
| Divergence audit, **should-not-exist** | `pipeline/audit.go:757-767` | the `AuditClassRetained` direction is silently not asked, so a lost retraction is invisible to the auditor too |

The refusal's stated reason (`:174-180`) is precise and, as stated, correct: *"A `WITH` clause can
re-project or re-bind variables (`WITH y AS u`), so a RETURN expression's variable NAME no longer proves it
binds the anchor — the name-based scope check below would be defeated."* The gap is that it treats
"the name-based check is defeated" as "the question is unanswerable", when the package already contains
both halves of the answer.

---

## 2. Grounding ledger

### 2.1 C1 — the census: 3 lenses move, and the population is closed

**Executable, and it is the census the build ships** (`docs/components/refractor.md`'s standing rule: a new
per-lens analysis ships its corpus census in the same fire, reusing `forEachCorpusCypher`, never a grep of
cypher text). The classifier walks every parseable plain, non-aggregate, non-personal corpus rule, threads
its real `Into.Key` via `closureKeyColumns`, and buckets it:

```
go test ./internal/refractor/ -run TestPlainWithAliasClosureCensus -count=1 -v
```

Live result at `ec3058d8` (2026-09-02):

| Bucket | Count | Names |
|---|---|---|
| A — already closed **and** identifying | 51 | (unchanged by this design) |
| A2 — closed, not identifying | 1 | (unchanged) |
| B — refused, **no `WITH` involved** | 8 | `capabilityRoleIndex`, `duplicateCandidates`, `landlordUnitsRead`, `objectIdentityAttachmentsRead`, `patientIdentityReadGrants`, `providerIdentityReadGrants`, `providerSites`, `staffReadGrants` |
| **F — the `WITH` is the sole blocker** | **3** | **`leaseApplicationsRead`, `renewalsRead`, `clinicPatientsRead`** |
| G — a key column binds a non-anchor variable | 2 | `landlordLeaseApplicationsRead`, `wellnessMemberAccounts` |
| | **65** | TOTAL |

Two facts the table asserts and the test must pin: the **floor** (65 classified, so an emptied enumeration
cannot read as "nothing moved") and that **F is exactly those three names**. The two G lenses are the ones
`plain-lens-neighbour-anchor-derivation-design.md` §6 already names as refused twice — that document's
account of `wellnessMemberAccounts` ("carries `WITH DISTINCT id`, **and** every key column binds `id`/`a`
rather than its anchor variable `bk`") is confirmed: removing the `WITH` refusal leaves the second refusal
standing, which is the correct outcome.

### 2.2 C2 — the origin design named this fire

`plain-lens-neighbour-anchor-derivation-design.md` §5.1: the licence takes `AnchorProjectionKey`'s `ok`
contract whole, *"deliberately **sufficient rather than necessary**: a neighbour-keyed lens whose rows
happen to be partitionable by anchor is refused too… **Widening it needs a real partitionability
derivation, which is a separate design and not this one.**"* This is that separate design, and it is the
**narrower** half of what that sentence anticipated: it does not derive partitionability for a
neighbour-keyed lens (§8 keeps that open); it removes a refusal that was never about partitionability at
all.

### 2.3 C3 — the pattern to mirror already exists, in the same package

`withscope.go` is a **general variable-scope walk** over the clause list, with the default-deny discipline
this design needs verbatim: `varScan` refuses on any AST node it has no case for (`:196-201`), and
`withCarries` (`:144-171`) already refuses, by name, the two rebinding shapes the wholesale rejection was
written against — renaming a pattern variable (`WITH a AS b` where `a` heads a pattern) and renaming
**onto** one. Its verdict is already a conjunct of `ScanRootHopIndex`, so every lens in bucket F has
already passed it (that is what `rootIndexed` in `plain_scanroot_corpus_census_test.go:147,150` records).

**Its justification does not transfer unexamined, and the plain design already flagged that** (§5's table,
line 262): `withscope.go:16-19` licenses a `WITH` that drops the anchor's own variable *"because the anchor
is the `$actorKey` PARAMETER rather than a row column."* For a plain lens the anchor **is** a row column,
so the reason must be re-derived rather than inherited — §4.4 does that.

### 2.4 C4 — the aggregate refusal is already in place, and it is what confines the grouping

`exprReferencesOnlyVariable` (`anchor_delete.go:348-412`) refuses `collect/count/max/min` **by name**
(`:374-381`) with exactly this reasoning: *"An aggregator's value depends on the grouped row set, which the
read-free single-anchor binding fabricates."* This is load-bearing for §4.4's soundness argument and it
needs no change.

### 2.5 C5 — the memoization precedent, with the right lifetime

`CompiledRule.groupingRedundant` (`ast.go:277-286`): *"Written once by `Parse`, over the `Query` above, and
never mutated afterwards — a compiled rule is shared across concurrent evaluations… a directly constructed
`*CompiledRule` gets nil here and is unaffected."* §4.3 mirrors that lifetime exactly, including the
nil-means-today's-behaviour arm.

### 2.6 C6 — the live cost, measured, not argued

- `leaseApplicationsRead`: the `$now` refusal was removed by `d6960bda` (2026-09-02); the licence was then
  observed refusing **one conjunct further**, at the partition conjunct, live at 03:05 that day
  (`expiry-as-a-recorded-fact-design.md` §13). The Postgres pair drained **~1 msg/min at ~64k pending** at
  that fire's close — the whole-corpus rescan floor. It is **not** a Secure Lens (no `SecureColumns`,
  `packages/lease-signing/lenses.go:169-176`), so the partition conjunct is genuinely its last blocker.
- `clinicPatientsRead`: its own declaration comment prices the alternative it was forced into
  (`packages/clinic-domain/lenses.go:325-334`) — *"DiffRetraction disables both anchor seeding and the
  plain-derivation narrowing licence for this lens, so every reacting event… now re-evaluates the whole
  patient corpus plus a full `ListKeys()` of `read_clinic_patients`."*
- `renewalsRead` and `clinicPatientsRead` both declare `SecureColumns`, so the **licence** still refuses
  them on `secureDecryptor != nil` (`anchor_derivation_plain.go:329-331`). Their gain is retraction and
  audit coverage only. §11 states the payoff per lens rather than as one number.

### 2.7 C7 — the reachability of the retraction gap, and what is still a hypothesis

`WithdrawLeaseApplication` soft-deletes the leaseapp (`ddls.go:204`, `:239-246`) and is permitted to
`operator` and to `consumer` (self) (`permissions.go:115-124`). `leaseApplicationsRead` anchors on
`app:leaseapp` with no retraction transport, so its row survives the withdrawal. **This is derived from
code, and Phase 0 must confirm it live** (§14.1) — a shipped defect claimed from reading is a hypothesis
until a row is observed surviving a withdraw. For `renewalsRead` the *structural* gap is identical, but
whether any op tombstones a `renewal` vertex is **not** established here; Phase 0 answers it, and the
answer changes only how loudly §11 reports, never the shape.

---

## 3. Why the wholesale refusal is the wrong shape

The refusal is a **name-scope** answer to a **value-provenance** question. `anchorProjectionShape` needs to
know: *does this key column's value come from the anchor alone, and does it identify the anchor?* When a
`WITH` intervenes, the RETURN expression names an alias rather than a pattern variable — so the question is
not unanswerable, it is **one substitution away**. `leaseApplicationsRead` says
`app.key AS entityKey` at the `WITH` and `nanoIdFromKey(entityKey) AS app_id` at the RETURN; composing the
two gives `nanoIdFromKey(app.key)`, which the two existing conjuncts answer immediately.

The refusal is also **silent and load-bearing in the wrong direction**. Adding a `WITH` to a lens does not
fail anything: the query parses, projects, and passes every other test — it just stops retracting. That is
why `packages/edge-manifest/lens_cypher_test.go:848-859` had to be written as a *mutation* test, and why
three separate package/design comments carry hand-written "must not contain a `WITH`" warnings. A rule an
author can only learn from prose is a rule that will be broken.

---

## 4. The shape

### 4.1 Two composed steps, replacing one wholesale rejection

In `anchorProjectionShape`, the loop at `:181-185` is replaced by:

1. **`withScopeReject(q.Clauses) != ""` ⇒ refuse.** The existing walk proves (a) every clause and
   expression in the query is a shape the walk models — default-deny, so an AST node added without a case
   refuses rather than shortening a set — and (b) no `WITH` dropped a name a later clause re-references.
   It also refuses `WITH *` and both pattern-variable rename shapes.
2. **Resolve each key column through the alias chain.** A new `withAliasEnv` (§4.2) maps each `WITH`
   boundary's alias to the expression producing it, resolved against the *preceding* boundary's env. Each
   key column's RETURN expression is rewritten by substituting aliases until it references pattern
   variables only. Resolution is driven **from the key columns**, so an unmodelled sibling `WITH` item that
   no key column reaches is irrelevant; a key column that reaches one **refuses** (fail-closed).

`exprReferencesOnlyVariable` and `exprIdentifiesVariable` then run on the **resolved** expression,
unchanged. `HasAnchorOnlyKeyColumns`, `ProjectsOneRowPerAnchor` and `AnchorProjectionKey`'s per-event half
are unchanged; they inherit the widening through the shared shape, which is the point — the three consumers
can never disagree about closure.

### 4.2 The substitution, precisely

For each `WITH` in clause order, build `env_n` from `env_{n-1}`:

| `WITH` item shape | `env_n` entry | Why |
|---|---|---|
| `a` / `a AS a` where `a` is a pattern variable | **no entry** (the binding is carried under its own name) | `evalExpr` on a `VariableRef` returns the binding itself; a self-mapping would recur. Mirrors `withCarries`'s first case. |
| `<expr> AS x` | `x → resolve(<expr>, env_{n-1})` | the value's provenance, one boundary back |
| `<expr>` with no alias | `projectionAutoAlias(expr, i) → resolve(…)` | the executor's own naming, called rather than restated (as `withCarries` does) |

`resolve` handles exactly `nil`, `Literal`, `ParameterRef`, `VariableRef`, `PropertyAccess`, `FunctionCall`
— the shapes a *key* column can legitimately be — and returns "unmodelled" for everything else. It is
deliberately narrower than `varScan`: `varScan` must enumerate every reference in the query, this must
reconstruct a value.

**Depth is bounded by the clause list** (each boundary resolves only against the previous env, which is
already fully resolved), so there is no fixpoint loop and no cycle to guard.

### 4.3 Where the env lives — one new field, one lifetime

| | |
|---|---|
| **What** | `CompiledRule.withAliasEnv []map[string]Expr` (one map per `WITH`, in clause order) plus `withAliasResolved bool` |
| **Created** | once by `Parse`, over `Query`, alongside `groupingRedundant` (`ast.go:277-286`) |
| **Mutated** | never — a compiled rule is shared across concurrent evaluations |
| **Reset / carried** | nothing to reset: the field is a pure function of the immutable `Query`. A re-`Parse` (activation, re-derivation, taxonomy rebuild) recomputes it with the query it belongs to; no boundary carries it forward |
| **Absent (nil)** | a **directly constructed** `*CompiledRule` — test rules, and any future non-`Parse` construction — has `withAliasResolved == false`. `anchorProjectionShape` then **refuses any `WITH`-bearing query exactly as today** |
| **Ordering** | none: no consumer reads it before `Parse` returns |

The `withAliasResolved` bool is not redundant with a nil/empty map. A query with **no** `WITH` also yields
an empty env, and that lens must be **admitted**; a query that was never `Parse`d must be **refused**.
Collapsing the two would make an unparsed rule read as a clean no-`WITH` lens — an empty value meaning
"fine" is the fail-open shape, and it is separated here on purpose.

**Cost.** `withScopeReject` walks the whole query, and the licence runs on **every neighbour event of every
plain lens** (`anchor_derivation_plain.go:361-374` is explicit about this being the dear path). The
resolution is memoized at `Parse`; `withScopeReject` is not memoized today and **must be** for this fire —
either behind the same `Parse`-time computation (preferred: it is a pure function of `Query` with the same
lifetime) or by a `withScopeReject == ""` bit stored beside `withAliasResolved`. Increment 1 owns this;
shipping the widening without it trades a whole-corpus rescan for a per-event whole-query AST scan.

### 4.4 Why this is sound — the argument the wholesale refusal was standing in for

Four claims, each discharged by a conjunct that already exists or by a pinned test:

1. **No stranded rebind.** The hazard in `:174-180` is a RETURN name that no longer binds what it appears
   to. Step 1 refuses every shape that produces one: a dropped-then-re-referenced name (`withReReference`),
   a pattern-variable rename in either direction (`withCarries`), `WITH *`, and any unmodelled clause or
   expression. The residue — a name that is *not* stranded — is exactly the case substitution is defined
   for.
2. **A dropped anchor variable is safe here, and the reason is re-derived, not inherited.**
   `withscope.go`'s licence to drop the anchor rests on the anchor being a `$actorKey` parameter; for a
   plain lens it is a row column, so that reason does not carry. The reason that *does*: the substitution
   captures the anchor's value **at the boundary where it was still bound** (`app.key AS entityKey`), and
   step 1 guarantees no later clause re-references `app` itself. All three F lenses are this shape — the
   anchor variable is dropped by the `WITH` and never mentioned again.
3. **Aggregation cannot truncate a row.** If a resolved key column contains an aggregate,
   `exprReferencesOnlyVariable` refuses it by name (`:374-381`). So an admitted key column resolves through
   a chain of **non-aggregating** projection items only — and a non-aggregating item is, by
   `projectItems`' own grouping rule, part of its boundary's grouping key. Every aggregation boundary the
   chain passes through is therefore grouped by a key that **includes** a value identifying the anchor, so
   every aggregate in the row spans that anchor's matches alone (a wider grouping key can only split the
   anchor's rows further, which is the pre-existing last-writer-wins property below, never merge two anchors). This is the same argument `ProjectsOneRowPerAnchor`'s doc
   comment (`:272-303`) already makes for the no-`WITH` case; the substitution is what lets it be made
   through a boundary.
4. **A `WITH`'s own `WHERE` is not a hazard.** It filters already-projected rows, so it can only remove a
   row — which is precisely the condition the filter-retraction check tests for
   (`evaluate.go:311-334`: the key is derived, then a `Delete` is emitted only when the re-derived result
   set does **not** contain it). A `WHERE` that drops the anchor produces the correct retraction.

**Unchanged and deliberately so:** a lens that projects several rows per anchor under one key
(`leaseApplicationsRead` with two `appliesToUnit` units, say) still last-writer-wins on upsert. That is a
pre-existing property of the lens's own key, identical before and after; the derivation reproduces the same
row set for that anchor either way.

### 4.5 What each of the three consumers gains

| Consumer | Before | After |
|---|---|---|
| Narrowing licence | 3 F lenses refused at the partition conjunct | `leaseApplicationsRead` licensed (the other two still refuse on Secure Lens) |
| Filter-retraction | `leaseApplicationsRead`, `renewalsRead`: **nothing retracts**. `clinicPatientsRead`: `DiffRetraction`'s full `ListKeys()` per event | all three take the read-free anchor `Delete`; `clinicPatientsRead` **keeps** `DiffRetraction` as its orphan healer (§13 Inc 3, amended at build) |
| Audit should-not-exist | `AuditClassRetained` never asked on the three | asked on `leaseApplicationsRead`; and, since [secure-plain-lens-retraction-and-audit-design.md](secure-plain-lens-retraction-and-audit-design.md) (2026-09-05), on `renewalsRead` and `clinicPatientsRead` too — a Secure Lens enrols under a column mask |

---

## 5. State-lifetime table

One new stateful thing, and it is immutable — §4.3 is the table. Nothing else in this design creates a
registry, cache, latch, watch or accumulated set: the substitution is a pure function of the AST, the three
consumers keep their own state unchanged, and no new field crosses a crash, replay, reconnect, tombstone or
upgrade boundary.

---

## 6. Reconciliation

**"Didn't we already handle this?"** Half of it, twice. `withscope.go` models `WITH` scope for the
**anchored** hop index; `groupingRedundant` reasons about **grouping** through projecting clauses. Neither
was ever asked the key-column question, and `anchorProjectionShape` — written before both — still answers
it with a wholesale refusal from `AnchorDeleteResult`'s original single-clause world.

**"Does it contradict a pattern?"** No: it removes an exception to one. Every other conjunct in
`anchorProjectionShape` is a structural question answered precisely and fail-closed; the `WITH` arm is the
only one that answers a different, coarser question.

**"Does it duplicate the design of record?"** It is the widening
`plain-lens-neighbour-anchor-derivation-design.md` §5.1 names and defers (§2.2), in its narrow reading.

**"New state?"** One `Parse`-time immutable field, mirroring `groupingRedundant` (§4.3, §5).

**In-flight check, both directions** (committed docs and the working tree).

- **Collisions: none.** The tree is clean at `ec3058d8`; no uncommitted `docs/contracts/*` edit touches
  Refractor's engine.
- **Dependencies handed *to* this design:** `expiry-as-a-recorded-fact-design.md` §13 hands it the
  `leaseApplicationsRead` payoff explicitly (*"the rescan is licensed away only when the partition
  conjunct above is satisfied… Belongs to the filed designer row"*).
- **Dependencies this design hands *onward*:** the `[Refractor] varlength anchor derivation` §13 Inc 2 row
  (`📋 ready`) narrows the **`ScanRootHopIndex`** `WITH`-scope refusal for the `cap-read.edgeManifest*`
  producers. That is a *different* `WITH` conjunct (index completeness, not key closure) on a different
  predicate; the two do not overlap, and neither sequences the other. Stated so a reader does not merge
  them on the word "`WITH`".

---

## 7. Alternatives

**Row 1 is deletion, and it is a real option here.**

| # | Alternative | Verdict |
|---|---|---|
| **1** | **Do not have this** — leave the wholesale refusal, keep the three lenses as they are | **Rejected, and this is the row that prices the design.** The cost is not symmetric across the three: `clinicPatientsRead` merely pays `DiffRetraction`, but `leaseApplicationsRead` and `renewalsRead` have **no retraction at all** — a withdrawn application's row survives in a protected table indefinitely (§0, §2.7). "Do nothing" means shipping that. |
| 1b | **Delete more** — drop the per-anchor closure predicate entirely and let every plain lens use `DiffRetraction` | Rejected: `DiffRetraction` is strictly dearer (a full `ListKeys()` of the target per event, plus it disables anchor seeding and the narrowing licence — `clinic-domain/lenses.go:325-334` prices exactly this), and it needs `ValidateUnanchoredForDiffRetraction` + a `KeyLister` adapter, which not every target has. Deleting the cheap path to universalize the dear one is backwards. |
| 2 | **Rewrite the 3 consumers** — restructure each lens so it carries no `WITH` | **Rejected on the merits, and this is the mandatory demand-side row.** With a single-digit census this alternative usually wins; here it is *unavailable*. All three `WITH`s exist to host an **aggregate** whose result a later boolean expression consumes (`leaseApplicationsRead`'s `max(CASE …)` doc-pointer gate and the four `count(DISTINCT …)` journey counters; `clinicPatientsRead`'s `authz_anchors` dedup; `renewalsRead`'s readiness clone). A projection cannot consume its own aggregate in the same clause, so the `WITH` is structurally required, not stylistic. The demand-side fix does not exist. |
| 3 | **Declare `DiffRetraction` on `leaseApplicationsRead` + `renewalsRead`** — the two-line fix for the retraction half | Rejected as the *primary*, kept as the fallback if §14.1's Phase-0 premise 1 fails. It closes the correctness gap but **worsens** the ★★★ item: `DiffRetraction` disables the narrowing licence, so `leaseApplicationsRead` would be locked out of the very narrowing this row exists for. Priced in combination with #2 (which cannot help) it is strictly dominated. |
| 4 | **Widen the licence only** — keep `AnchorProjectionKey`'s `ok` contract as-is, add a separate `WITH`-aware predicate for the plain licence | Rejected. It buys nothing (the licence's own payoff is one lens) and forfeits both retraction and audit — the larger half. Worse, it creates two predicates that answer the closure question differently, which `plain_scanroot_corpus_census_test.go:317-330` exists to make impossible, and which §5.1's own "the same predicate, which is why B2's fix and B3's fix are one change" warns against. |
| 5 | **Model `WITH` rebinding in the hop-index builder too**, so the pattern graph follows renames | Rejected as scope: `withCarries` refuses pattern-variable renames outright and no corpus lens uses one. Nothing in bucket F needs it, and a rename model is where a wrong hop gets asserted. |
| 6 | **A `lint-conventions` gate: "no `WITH` in a plain lens spec"** — mechanize the prose rule instead of removing it | Rejected. It makes the three lenses *fail* rather than work, forbids a shape the engine executes correctly, and the standing doctrine applies: a new mechanism to patch a gap left by a previous mechanism means the base should be re-derived. Here the base — "answer the closure question" — is one substitution from being answerable. |

**Re-asked in the other direction** (the discipline: run each rejected alternative's objection back against
the recommendation). #4's objection is "two predicates that can disagree" — the recommendation has one.
#6's objection is "forbids a shape the engine executes correctly" — the recommendation admits exactly the
shapes it can *prove*, and refuses the rest by the same default-deny the package already uses. #1b's
objection is cost — the recommendation is the cheap path, and §4.3 removes the per-event scan it would
otherwise introduce.

---

## 8. What this does not close

- **The genuine N-rows-per-anchor family stays refused**, correctly: `landlordLeaseApplicationsRead` and
  `wellnessMemberAccounts` (bucket G) key on a non-anchor variable, and a per-anchor evaluation would
  compute a truncated row. That is the widening §5.1 called *"a real partitionability derivation"*.
  *(Superseded 2026-09-05: [anchor-partitioned-plain-lens-retraction-design.md](anchor-partitioned-plain-lens-retraction-design.md)
  is that design — the partition conjunct admits 8 lenses, `landlordLeaseApplicationsRead` among them, and
  scopes their target diff to the anchors an evaluation covered; verticals.md row 29 was the consumer.)*
- **The Secure Lens conjunct is untouched here.** `renewalsRead` and `clinicPatientsRead` gain retraction and
  audit, never narrowing. *(Superseded 2026-09-05: [secure-plain-lens-retraction-and-audit-design.md](secure-plain-lens-retraction-and-audit-design.md)
  drops that conjunct — the double-decrypt seam is closed at the shared re-entry, which never decrypts —
  and the audit's Secure refusal with it.)*
- **The 8 bucket-B lenses are unaffected**, and each is refused for a reason this design does not touch.
- **`landlordUnitsRead`'s refusal is *not* the `WITH`.** The `clinic-domain` comment that cites it as "the
  same shape" is wrong on that point; §9 corrects the comment, and the lens itself needs no change.

---

## 9. Contract + document surface

**Frozen contracts: no change.** Nothing in `docs/contracts/*` promises anything about `WITH`, about which
lenses retract, or about the narrowing licence. `06-capability-kv.md:465-468`'s retract enumeration is
doc-mode/actorAggregate and is untouched. Contract prose states observable promises, and no observable
promise moves: a lens that retracted still retracts, and three that silently did not now do.

**Documents that must change with the code** (each named, so none is left asserting a rule the runtime no
longer has):

| Doc | Today | After |
|---|---|---|
| `packages/clinic-domain/lenses.go:308-334` | "anchorProjectionShape rejects any WITH-bearing query wholesale… DiffRetraction is the alternative for exactly this shape" + a `landlordUnitsRead` citation that is wrong on the mechanism | rewritten with the `DiffRetraction` declaration when Inc 3 drops it; the `landlordUnitsRead` citation corrected to name its composite key |
| `docs/components/edge-manifest.md:113` | "It carries **no `WITH` clause** — `anchorProjectionShape`…" | restated as the *resolvable-key-column* rule |
| `docs/components/refractor.md` | — | the closure predicate's new two-step shape; a dossier entry if the close review classifies one |
| `staff-descriptor-rendering-design.md` §2.1 / §5 | package-authoring rule "the cypher must NOT contain a `WITH` clause" | superseded-in-place with a pointer here (a shipped design; the body is rewritten, not banner-only) |
| `plain-lens-neighbour-anchor-derivation-design.md` §5.1, §6 | "widening it needs a real partitionability derivation… not this one" | a pointer to this doc for the `WITH` half; the partitionability half stands |

---

## 10. Test strategy

Every test below is **owned by a named increment** in §13.

1. **The corpus census** (`internal/refractor/plain_with_alias_closure_census_test.go`, Inc 1) — §2.1's
   classifier, reusing `forEachCorpusCypher`, pinning the per-lens bucket, the exact F membership, and a
   floor on the total. Fails when a lens arrives at or leaves the licensed set.
2. **`plain_scanroot_corpus_census_test.go`'s pinned verdicts** (Inc 1) — three rows move
   `closureRefused → closureHolds`. The file's own header says that direction *"needs an argument"*; the
   argument is §4.4 and the moving rows are named in the commit, not silently re-pinned.
3. **Unit table on `anchorProjectionShape`** (Inc 1), each case a shape, not a lens: single-`WITH`
   passthrough; chained `WITH`s; `WITH` carrying the anchor binding bare (`WITH op, role`); alias
   shadowing across two boundaries; an aggregate in the resolved key (refuse); a key column resolving to a
   non-anchor variable (refuse); a dropped-then-re-referenced anchor (refuse, via `withScopeReject`);
   `WITH *` (refuse); a pattern-variable rename in both directions (refuse); an unmodelled expression node
   reached from a key column (refuse); an unmodelled node reached only by a *sibling* item (admit).
4. **The unparsed-rule arm** (Inc 1) — a directly constructed `*CompiledRule` with a `WITH` must refuse.
   This is the fail-closed half of §4.3 and it has no other guard.
5. **`packages/edge-manifest/lens_cypher_test.go:848-859` — MUTATION 1 must be rewritten** (Inc 1). Its
   `WITH op, role` opener will, correctly, **no longer** kill the retraction, so the assertion
   `require.False(t, withOK)` becomes false by design. It is replaced by a mutation that still does kill
   it — `WITH op AS o, role` (a pattern-variable rename `withCarries` refuses) — and MUTATION 2 (the
   aspect-sourced key column) stands unchanged. **This is a deliberate assertion flip, not a loosened
   test:** the mutation is re-aimed at the hazard that still exists, and the count of mutations does not
   drop.
6. **Retraction e2e for `leaseApplicationsRead`** (Inc 2, the headline) — a real
   `WithdrawLeaseApplication` against an ephemeral stack: the row is present, the withdraw commits, and
   the row leaves `read_lease_applications`. Runs red on `main` at the parent commit, which is the proof
   the gap was live.
7. **Narrowing proof for `leaseApplicationsRead`** (Inc 2) — a neighbour event evaluates the derived anchor
   set, not the corpus, asserted at the pipeline seam the sibling fires already use.
8. **`clinicPatientsRead` without `DiffRetraction`** (Inc 3) — the existing clinic retraction e2e must
   stay green with the declaration removed, and its narrowing/seeding assertions gain the licence.

---

## 11. Measurement and acceptance

| Signal | Where | Expected |
|---|---|---|
| Census bucket F | `TestPlainWithAliasClosureCensus` | exactly `leaseApplicationsRead`, `renewalsRead`, `clinicPatientsRead`; total ≥ 65 |
| `leaseApplicationsRead`'s licence verdict | refractor log | the partition refusal is **gone**; the lens is licensed (no further conjunct refuses — it is not Secure) |
| `leaseApplicationsRead` per-message cost | msg/s at steady state | the ~1 msg/min whole-corpus floor (§2.6) lifts. **The number is not predicted here** — the sibling fires' own posture: report the measurement, do not assert a target |
| A withdrawn application's row | `read_lease_applications` | **leaves** the table on `WithdrawLeaseApplication`. Red at the parent commit |
| `renewalsRead` retraction | e2e | the anchor `Delete` resolves; whether any op reaches it live is Phase 0's answer (§2.7) |
| `clinicPatientsRead` | `read_clinic_patients` | identical rows before/after; the anchor Delete is the transport, `DiffRetraction` stays as the healer (its `ListKeys()` per event is the price of the only standing observer a Secure Lens has) |
| `AuditClassRetained` | audit log | reachable on `leaseApplicationsRead` (the only non-Secure of the three); **0** on a converged lens |
| `renewalsRead` / `landlordLeaseApplicationsRead` licence | refractor log | **still refused, on Secure Lens** — assert both, so an admission that should not have happened is caught |

---

## 12. Adversarial pass

Run against the draft; three findings, all folded in.

- **A1 — the naive resolver is fail-OPEN on a dropped-then-re-referenced anchor.** A first pass resolved
  aliases and checked only that the result referenced the anchor variable. A RETURN naming an anchor
  variable some `WITH` had *dropped* resolves to `app.key` and reads as licensed, while the executor binds
  it fresh. Fixed by making `withScopeReject` step 1 rather than an afterthought (§4.1), and by re-deriving
  its justification for a row-column anchor rather than inheriting it (§4.4 claim 2).
- **A2 — a self-referential alias loops.** `WITH p` yields an item whose expression is `VariableRef{p}`
  under alias `p`; mapping `p → p` recurses. Caught by the census returning "unmodelled" for
  `clinicPatientsRead` and `wellnessMemberAccounts` on the first run — a lens that should have classified
  cleanly. Fixed in §4.2's first table row (a carried binding gets **no** env entry), and it moved
  `clinicPatientsRead` from "unresolvable" into bucket F. **Two of the three payoff lenses were invisible
  until this was fixed**, which is why §2.1's census is executable and re-run rather than reasoned.
- **A3 — a shipped mutation test asserts the behaviour being removed.**
  `TestOpCatalog_TombstonedOpMetaRetractsItsRow`'s MUTATION 1 asserts a `WITH op, role` opener loses the
  retraction. Found by grepping the consumers of the predicate rather than of the lenses. §10.5 re-aims it
  instead of deleting it; had this landed unnoticed, the build would have "fixed" a red test by weakening
  the one assertion protecting the aspect-keyed hazard beside it.

---

## 13. Decomposition for the Steward

Three increments, each independently shippable and green. **Posture-changing: Increment 1 and 2** (Inc 1
widens a predicate that authorizes `Delete`; Inc 2 turns retraction on for two protected tables) — those
get the full review pass. **Increment 3 is a declaration removal** proven by an existing e2e; size it
normally.

**Increment 1 — the predicate.** `withAliasEnv` + `withAliasResolved` computed at `Parse` (§4.3), the
memoized `withScopeReject` verdict, the two-step `anchorProjectionShape`, the census test, the
`plain_scanroot` re-pin, the unit table, the unparsed-rule arm, and the re-aimed edge-manifest mutation.
No package or lens declaration changes. Green means: the three F lenses' closure verdicts move and nothing
else does. **What Inc 1 already changes at runtime (amended 2026-09-03, build):** because the four consumers
share the predicate, the moment Inc 1 lands (a) `leaseApplicationsRead` is *licensed* for narrowing ahead of
Inc 2's proof, and (b) `clinicPatientsRead`'s **anchor-type** (patient) events take the read-free anchor
Delete at `evaluate.go:246` / `:320` instead of the whole-corpus rescan + `applyDiffRetraction` — its
`DiffRetraction` keeps running on every **neighbour** event (`AnchorProjectionKey` is `ok == false` for a
non-anchor label, so the `!ok && p.diffRetraction` arm still fires there) until Inc 3 removes the
declaration. A patient event can only stale that patient's own row, which the anchor path now retracts
precisely, so no row class loses its transport in between; `TestClinic_TombstonePatient` and the
`clinic-domain` retraction tests stay green on Inc 1 as-is.

**Increment 2 — the payoff, observed.** The `leaseApplicationsRead` withdraw-retraction e2e and the
narrowing proof, plus the live measurement of §11. Nothing to build in the engine; this increment exists
because a widened predicate with no observed consumer is a claim, not a payoff.

**Increment 3 — `clinicPatientsRead` keeps `DiffRetraction`; the declaration comment and pin are rewritten
(amended 2026-09-03 at build — the original increment dropped the field, and the close pass falsified the
claim behind it).** `DiffRetraction` was priced here as a dearer *transport*; it is also the lens's only
*continuous healer* — a whole-target key diff on every event removes a row orphaned by a missed or failed
retraction event, which the event-scoped anchor Delete and presence check never revisit — and the two
observers this design offered as compensating both refuse a Secure Lens (the audit refuses off-request
plaintext re-derivation; the sweep enrols auth-plane actor-aggregate lenses only). On a PHI table that trade
is one healer for none, so the field stays; the retraction transport the predicate now gives it is pinned by
`TestClinicPatientsRead_TombstonedPatientRetractsItsRow`, and §10.8's "existing clinic retraction e2e" did
not exist (the tombstone test proved the root, not the row). The healer gap for Secure plain lenses that
never declared `DiffRetraction` (`renewalsRead`) is pre-existing and filed as a designer row — closed by
[secure-plain-lens-retraction-and-audit-design.md](secure-plain-lens-retraction-and-audit-design.md), which
lifts both refusals and adds the business-plane retraction-transport gate.

---

## 14. Fire brief (for the Steward's Phase 0)

### 14.1 Premises to re-derive, each gating the increment that rests on it

1. **The stale-row defect is live.** Derived from code (§2.7), not observed. Confirm on the running stack:
   create a lease application, see its row in `read_lease_applications`, submit
   `WithdrawLeaseApplication`, re-read. If the row *does* leave, something retracts it that this document
   has not found — open that path before building Inc 2, and the headline changes to the narrowing alone.
2. **`renewalsRead`'s gap is structural but its reachability is unestablished.** Grep the package's ops for
   a `renewal` soft-delete before claiming a live defect for it.
3. **`leaseApplicationsRead` has no other licence blocker.** Re-run the licence against the live pipeline
   after Inc 1 and read the refusal log: the expected outcome is *no refusal*. A refusal one conjunct
   further is a finding, not a failure.
4. **The census numbers are as of `ec3058d8`.** Re-run §2.1's classifier at the fire's base commit before
   trusting bucket F's membership; the corpus moves.

### 14.2 Verified touch-list (live at `ec3058d8`)

- `internal/refractor/ruleengine/full/anchor_delete.go:168-247` (`anchorProjectionShape`; the wholesale
  loop at `:181-185` is what changes), `:250-269`, `:272-315`, `:322-333`, `:348-412` (unchanged, but read
  them — they carry the soundness argument)
- `internal/refractor/ruleengine/full/withscope.go:33-121` (`withScopeReject`), `:144-171` (`withCarries`)
- `internal/refractor/ruleengine/full/ast.go:252-292` (`CompiledRule`; `groupingRedundant`'s comment is the
  lifetime to mirror)
- `internal/refractor/plain_scanroot_corpus_census_test.go:147,150` (the two pins that move), `:317-330`
  (the three-route equality that must keep holding)
- `packages/edge-manifest/lens_cypher_test.go:809-876` (the mutation test)
- `packages/clinic-domain/lenses.go:308-342`, `packages/lease-signing/lenses.go:169-176,1078-1084`,
  `packages/lease-signing/renewal_lenses.go:100-105`
- Consumers, read-only: `pipeline/anchor_derivation_plain.go:341-343`, `pipeline/evaluate.go:311-338`,
  `pipeline/audit.go:753-767`

### 14.3 In-scope gotchas

- **`docs/components/refractor.md`'s standing rule binds this fire**: the census ships in the same commit
  as the analysis, reuses `forEachCorpusCypher`, and never reimplements the predicate it is censusing —
  it calls the real one.
- **Build-tagged harnesses**: this changes no engine/service interface, so the tagged convergence suites
  are not reached — but `go test ./...` is not the gate set (CLAUDE.md); read `ci.yml`.
- **Inc 3 edits a package declaration ⇒ a manifest + `Version` constant bump.** Inc 1 and 2 do not.
- **A refusal string that becomes unreachable is dead prose.** The partition refusal at
  `anchor_derivation_plain.go:342` stays reachable (bucket G); the wholesale-`WITH` path does not exist
  afterwards, so nothing in the tree should still describe it.

### 14.4 Non-goals

Partitionability for a neighbour-keyed lens; Secure Lens narrowing; modelling `WITH` renames in the hop
index; any change to `ScanRootHopIndex`'s own `WITH` conjuncts (that is the varlength Inc 2 row).

---

### WITH-alias closure fire brief (build note, 2026-09-03)

Compiled at `a575f7f5` by the Lattice Steward (Phase 0, `agents/fire-brief-template.md`). One brief for the
item; increment checkpoints amend this note.

**1. Scope sentence (verbatim, banner).** *"✅ Andrew-ratified 2026-09-02 — build as designed, §13's three
increments in order."* Green bar per increment is §13's; the item's acceptance is §11's table.

**2. Verified touch-list (live at `a575f7f5`; every §14.2 anchor re-checked, none rotted).**
- `internal/refractor/ruleengine/full/anchor_delete.go:168-248` `anchorProjectionShape` (wholesale loop
  `:181-185`); `:40-44` `AnchorDeleteResult` → `:66-73` `AnchorProjectionKey` (the **root-tombstone Delete
  shares the shape** — so a `WithdrawLeaseApplication` tombstone is refused at the same predicate as the
  filter-retraction; this is the mechanism behind premise 1); `:267-270`, `:304-315`, `:322-340`,
  `:348-415` (aggregate refusal `:375-381`).
- `full/withscope.go:33-121` `withScopeReject(clauses []Clause) string`; `:144-171` `withCarries`;
  `:173-175` `withReReference`; `:253-255` `varScan` default-deny; `:19-21` the `$actorKey` licence whose
  reason §4.4 re-derives. Callers today: `full/hopindex.go:242,363` only (index build, not per event) — the
  per-event caller this fire adds is what makes memoization mandatory (§4.3).
- `full/full.go:235-240` `Parse` constructs `CompiledRule` (`groupingRedundant: analyseGroupingRedundancy(...)`)
  — `withAliasEnv` / `withAliasResolved` / the memoized `withScopeReject` verdict are computed here.
  `full/ast.go:252-300` `CompiledRule` (lifetime comment `:277-286`, `:295-297`). `Expr` universe
  `ast.go:125-248`: 13 concrete types; the resolver models exactly `nil, Literal, ParameterRef, VariableRef,
  PropertyAccess, FunctionCall` and returns unmodelled for the other 7. `projectionAutoAlias`
  `full/executor.go:1869`.
- `internal/refractor/plain_scanroot_corpus_census_test.go:133` (`clinicPatientsRead`), `:150`
  (`leaseApplicationsRead`), `:169` (`renewalsRead`) — the three `closureRefused` pins that move; `:22-27`
  header; `:317-330` three-route equality; `TestScanRootCorpusCensus_PinnedVerdicts` `:387`.
  `forEachCorpusCypher` `label_derivation_corpus_census_test.go:573`.
- `packages/edge-manifest/lens_cypher_test.go:824-876` (MUTATION 1 `:850-858`, MUTATION 2 `:865-866`).
- `packages/clinic-domain/lenses.go:308-333` comment, `:341` `DiffRetraction: true`; version
  `packages/clinic-domain/package.go:139` + `manifest.yaml:2` (`0.34.19`).
- `packages/lease-signing/lenses.go:1165-1210` `leaseApplicationsReadSpec` (`MATCH (app:leaseapp)` `:1166`,
  `WITH app.key AS entityKey` `:1173-1174`, `nanoIdFromKey(entityKey) AS app_id` `:1210`); declaration
  `:207-214` (`IntoKey ["app_id"]`, no `SecureColumns`, no `DiffRetraction`).
  `renewal_lenses.go:100-128` (`SecureColumns` `:126`).
- Consumers, read-only: `pipeline/anchor_derivation_plain.go:329,341-342,512`; `pipeline/evaluate.go:246-248`
  (root tombstone) and `:320-334` (filter-retraction); `pipeline/audit.go:695,757-767`.
- Docs: `docs/components/edge-manifest.md:113`; `staff-descriptor-rendering-design.md:93`;
  `plain-lens-neighbour-anchor-derivation-design.md:433`.

**Scout drift, corrected by hand:** the `haiku` scout reported `leaseApplicationsRead` as `WITH`-less (it read
the declaration at `:207`, not the spec at `:1165`). Verified: the `WITH` is at `:1173`. Nothing else drifted.

**Premises (§14.1), re-derived live at fire start:**
1. *Stale-row defect is live* — **not yet observed, cannot be from existing state:** `read_lease_applications`
   holds 57 live rows and **0** of their `vtx.leaseapp.*` roots are tombstoned in Core KV (no withdraw has ever
   run on this stack). Mechanism confirmed from code (root tombstone → `AnchorDeleteResult` → the same
   refused shape). Inc 2 proves it by a fresh create-then-withdraw; if the row leaves, stop and open the path.
2. *`renewalsRead` reachability* — **no op tombstones a `renewal` vertex** (grep of `packages/lease-signing`):
   structural gap only, §11 reports it as such.
3. *No other licence blocker on `leaseApplicationsRead`* — live refusal at 2026-09-03 18:27 is exactly the
   partition conjunct (`reason: "its rows do not partition by anchor …"`, ruleId `gP3FBEn7iiWVt1hVgP3F`);
   the audit reaches verdicts (`lastAuditVerdictAt` set). Positive verdict to be read after Inc 1.
4. *Census as of `ec3058d8`* — the census test was never committed; Inc 1 ships it and pins F at the fire's base.

**3. Precedents to mirror.** Default-deny walk: `withscope.go:253-255`. Parse-time immutable analysis field:
`ast.go:277-286` + `full.go:235-240`. Census test shape: `TestScanRootCorpusCensus_PinnedVerdicts`
(`plain_scanroot_corpus_census_test.go:387`) over `forEachCorpusCypher`. Mutation test shape:
`lens_cypher_test.go:850-866`. Absence-gate test shape: `TestAnchorHopIndex_EmptyExpansionIsUnresolved`
(both vectors, revert-proven). Walk-completeness gate: `full/variable_refs_completeness_test.go` (discovers
`Expr` types from source — the resolver's unmodelled arm must stay a refusal, never a passthrough).

**4. Increment order + green checks.**
- **Inc 1 — the predicate** (posture-changing → `opus` builder, full review). Green:
  `go test ./internal/refractor/ruleengine/full/ -count=1` · `go test ./internal/refractor/ -run
  'TestPlainWithAliasClosureCensus|TestScanRootCorpusCensus' -count=1 -v` (F = exactly the three names, total
  ≥ 65; the three pins move `closureRefused → closureHolds`, no other row moves) · `go test
  ./packages/edge-manifest/ -run TestOpCatalog_TombstonedOpMetaRetractsItsRow -count=1` (re-aimed MUTATION 1) ·
  revert-proof: with the substitution disabled the three F verdicts return to refused · then `go build ./...`,
  `make vet`, `golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`, `go test
  ./internal/refractor/... ./packages/... -count=1`. Land on `main`, `make cycle-refractor`, read the
  positive licence verdict for `gP3FBEn7iiWVt1hVgP3F` and the still-refused (Secure) verdicts for
  `H1CvFXsBn5TFsag2H1Cv` (`renewalsRead`) and `KnMmwVvJ6To8kdsjKnMm` (`clinicPatientsRead`).
- **Inc 2 — the payoff, observed** (Winston inline + `sonnet` for the e2e). Live: seed applicant + unit,
  `CreateLeaseApplication`, row present, `WithdrawLeaseApplication`, row leaves `read_lease_applications`
  (`is_deleted` or gone). E2e in `internal/leaseconvergence` (tag `leaseshortwindow`; harness
  `harness_test.go:162,605,644,715`), red at the parent commit. Narrowing proof at the pipeline seam. Report
  msg/s + the audit's `stale` count on the lens (10 divergent rows every 15 min at fire start).
- **Inc 3 — `clinicPatientsRead` drops `DiffRetraction`** (`sonnet`). `packages/clinic-domain/lenses.go:341`
  removed + comment rewritten (§9) + `package.go:139` and `manifest.yaml:2` bumped; `DIFF_BASE=<base> go run
  ./scripts/lint-package-version.go`; `go test ./packages/clinic-domain/ -count=1`; `make reinstall-package
  PKG=packages/clinic-domain` then `make verify-package-clinic-domain` on the live stack; rows identical
  before/after. Sequenced after Inc 1 is observed live. Docs of §9's table in the same commit.

**5. In-scope gotchas.** `docs/components/refractor.md` standing rule (census calls the REAL predicate, ships
with the analysis). `go test ./...` is not the gate set — run `ci.yml`'s lint set; no interface changes, so the
tagged harnesses are not reached except `make test-lease-convergence`, which Inc 2 extends. Inc 3 is a package
edit ⇒ version bump. The wholesale-`WITH` refusal prose must vanish everywhere (`anchor_delete.go:174-180`,
`AnchorProjectionKey`'s doc "no WITH" at `:63`, the three docs). **Dossier entries that bind this fire
(`docs/components/refractor.md` "Review keeps catching"):** *a lifted refusal reveals the conjunct behind it,
and a GRANTED licence logs nothing — prove the payoff by the POSITIVE verdict, read live* · *refuting a
refusal's REASON does not establish the whole refusal was wrong — re-derive the boundary from the CONSUMERS*
(§4.4 is that derivation; the reviewer checks it against all four consumers incl. the root-tombstone Delete)
· *`len(x)==0` vs `nil` — a present-but-empty and a missing answer must not collide* (`withAliasResolved` is
the separator; test both vectors) · *an authoring gate and its runtime resolver must agree* (the resolver's
unmodelled arm refuses) · *a fixture that establishes the favourable ARM is an argument, not a test* (Inc 2's
e2e must take `evaluate.go:246` root-tombstone arm with a real `WithdrawLeaseApplication`) · *a widened
operation silently drops the bound its narrow predecessor carried* (the memoized verdict is the bound here).
**Standing checklist** (`fire-brief-template.md`): 1 state needs a lifetime (§4.3 table) · 2 every census is a
premise (re-run at base) · 3 negative test needs a positive vector; prove by revert · 4 removal needs a
transport AND an observer (Inc 3 enumerates what `DiffRetraction` was doing) · 5 one deterministic key, one
writer · 6 precedent may carry debt.

**6. Adjacent finds.** (a) Audit on `leaseApplicationsRead` reports `classes:{stale:10}` every 15 min at HEAD —
a lag symptom of the whole-corpus rescan; measured, not filed: Inc 2 reports it after the licence lands.
(b) None other.

**7. Non-goals.** §14.4 verbatim: partitionability for a neighbour-keyed lens; Secure Lens narrowing; `WITH`
renames in the hop index; `ScanRootHopIndex`'s own `WITH` conjuncts (the varlength Inc 2 row).

**Scope-diff gate:** every touch above traces to §13's three increments; nothing widened; the one dependency
(`expiry-as-a-recorded-fact` → this) is satisfied at HEAD (`d6960bda`); the onward row (varlength Inc 2) is not
load-bearing. Landing shape: **each increment lands on `main`** — Inc 1 alone is green and safe (a widened
predicate that only admits shapes it proves), Inc 2 adds a test and a measurement, Inc 3 a declaration removal
behind an observed cheap transport.

**Premise 1 observed live (2026-09-03 20:24, HEAD `9190f85a`, old binary).** Fresh `CreateLeaseApplication`
`vtx.leaseapp.EUjkWNbxboP5fkysbWs5` (applicant `vtx.identity.xd79qNNcy9MDtatPNcVZ`, unit
`vtx.unit.zNaJNVmmsQTA3AfJXu4E`): row present in `read_lease_applications` within 5 s (the lens is not
backlogged), `WithdrawLeaseApplication` accepted, root `isDeleted:true`, and the row still `is_deleted=false`
after 3 min — the stale-row defect is live, by the root-tombstone arm (`evaluate.go:246`), not by lag.

**Inc 1 observed live (2026-09-03 21:21–21:49, `595ea540`, `make cycle-refractor`).** The partition refusal is gone
from `leaseApplicationsRead`; the next conjunct was the audit's first post-restart verdict, reached at 21:39:13,
after which the licence's positive verdict is the tally line (`acted:6900 actedAnchors:3450 fellBack:0` by
21:48). Throughput on the lens's consumer: ~1 msg/s (whole-corpus rescan, 12,428 unprocessed at 21:26) →
~22 msg/s licensed (1,381 processed/min at 21:45); the backlog drained to 0 by 21:49. Both withdrawn probe
applications (`EUjkWNbxboP5fkysbWs5` from the old binary, `LCGdCpz9UuZFBufvqixV` from the new) flipped to
`is_deleted=true` in `read_lease_applications` when the consumer reached their tombstone events — a
whole-corpus rescan re-upserts every live row but never retracts a tombstoned anchor's, which is why a row can
appear in 5 s behind a 12k backlog and still need the Delete. `renewalsRead` still refuses on Secure Lens (no
audit enrolled), `clinicPatientsRead` refused on `DiffRetraction` until Inc 3. The audit's `stale:10` sample on
`leaseApplicationsRead` predates the drain; re-read at the next fire.

**Inc 2 shipped `dd4d43bf`** — `TestLeaseConvergence_WithdrawRetractsReadModelRow` (real op, real cypher and
`Into.Key`, a KV bucket standing in for the Postgres table; red at the parent predicate, 1.2 s green).

**Inc 3 shipped `352b9763`** — `clinicPatientsRead` 0.34.20 without `DiffRetraction`; live `make
reinstall-package` diff-applied 4 entities, Refractor hot-reloaded the lens INTO in place (21:48:53),
`verify-package-clinic-domain` 405 OK, `read_clinic_patients` rows byte-identical before/after.
`TestClinicPatientsRead_TombstonedPatientRetractsItsRow` pins the remaining transport on the real spec.

**Close pass (cold, whole diff `9190f85a..352b9763`): 0 blocking, 4 major, 6 minor — classified.**
- *design-gap* — the audit-coverage claim held for one lens, not three (a Secure Lens is refused enrolment;
  §4.5/§11 corrected); `DiffRetraction`'s healing obligation was never enumerated (§13 Inc 3 rewritten, the
  declaration reinstated at 0.34.21); §4.4 claim 3 overstated the grouping key (restated).
- *brief-gap* — §10.8's "existing clinic retraction e2e" did not exist; the arm is proven by Inc 2's e2e plus
  the reviewer's trace that a Secure Lens reaches the root-tombstone arm before decrypt
  (`SecureDecryptor.Apply` skips `Delete` results) and by the direct `AnchorDeleteResult` pin. §9's doc table
  was half-landed at Inc 1 (three more sites in `staff-descriptor-rendering-design.md`, one in the plain-lens
  design; fixed).
- *implementation-bug* — census bucket G's label named a cause the classifier does not test (relabelled);
  the e2e lacked `t.Parallel()` and its poll comment denied its own interval sleep (fixed).
- *convention* — one history-narration clause and one self-contradicting lens comment (fixed).
- *review-over-reach* — none.
Routed: `agents/fire-brief-template.md` standing checklist #4 sharpened (a removal's healing is a separate
obligation from its transport; run every compensating observer through its own enrolment predicate for THIS
lens). Filed: `[Refractor] A Secure plain lens has no orphan healer` — `📐 needs designer pass`.

**Checkpoint.** item complete; all increments on `main`.
