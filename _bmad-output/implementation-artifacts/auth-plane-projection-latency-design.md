# Bounding auth-plane projection latency — relevance-gate and pattern-direct the actor-aware fan-out

**Status: ✅ Andrew-ratified 2026-08-01** — build-ready; increments in §10 order. **§15.7's proposed
Contract #6 §6.2 tie-rule amendment: HELD by Andrew 2026-08-06** — the tie rule stays `≤`-rejects
unconditionally, the staged edit is reverted, and the resume order is §15.9's.
Designer fire, Winston, 2026-08-01.
Owning component: **Refractor** (`internal/refractor/pipeline`, `internal/refractor/projection`,
`internal/refractor/ruleengine/full`).
Board row: `backlog/lattice.md` → *[Refractor] Post-claim auth-grant latency is unbounded by design*.

---

## For Andrew

**What it does, in two lines.** `capabilityRoles` — the lens that *is* the write-side authorization
surface — today consumes **every Core-KV write in the system** and, for each one, runs an undirected
adjacency BFS and re-executes its cypher **once per actor the BFS reaches**. A booking, a listing, a
task all pay full price on the auth plane, and a single `holdsRole` link create re-projects **every
co-holder of that role**. This design applies the two already-shipped narrowing mechanisms (Fire 3's
client relevance gate, D1's server-side `FilterSubjects`) to the actor-aware arms they were deliberately
excluded from, then replaces the undirected BFS with a **pattern-directed reverse walk seeded from the
changed element**, which is what collapses the co-holder fan-out to the one actor whose grant moved.

**It also found two latent soundness holes in the *shipped* D1 narrowing** (§4.3). `ReferencedLabels`
reports `exhaustive` for a query that drops a variable across a `WITH` and re-references it unlabeled,
and the executor binds a labeled pattern node by a vertex's **body `class`/`label`**, not only by its key
type. Neither has a live instance in today's corpus, and both are load-bearing the moment a lens is
narrowed. **Increment 0 closes them and hardens what already shipped** — it is worth building even if
you reject everything else here.

**The read-your-own-grant fork (§8.1) resolved to Option A — eventually-consistent + client retry.**
Recorded as settled by the ratification itself, not by a separate instruction: Option A *is* "ship this
decomposition and change nothing else", whereas B (Gateway blocks on convergence) and C (a contract-level
SLA) each require work §10 does not contain, so ratifying §10 entails A. Option B remains available as a
**sequenced** follow-on if the post-Increment-3 measurement shows the p99 is still poor for a real
product surface — with data behind it. If that reading of the ratification is wrong, this paragraph is
the one to correct.

**No frozen-contract change.** Contract #6 states no projection-latency promise and none of its
semantics move (§7). Nothing is staged uncommitted. (Increment 2's build later surfaced a §6.2 tie-rule
question — §15.7 — staged as an uncommitted proposal; Andrew **held** it on 2026-08-06, so this statement
is true again: the contract is unchanged and the tree carries no edit.)

**What this deliberately does not claim.** It does not establish a latency SLA. It removes the two terms
that make latency *unbounded* — dependence on unrelated business write volume, and self-amplification
within an auth burst — and leaves a bounded, measurable relationship. That is also why the sibling
tooling row (`verify-claim-ceremony.go` polls to convergence and **reports** the latency instead of
asserting 5 s) stays a separate item and is the right harness for measuring these fires.

---

## 1. Problem + intent

`facet-staff-worlds-design.md` §13.8 (2026-08-01) closed the "a live claim's own consumer grant never
projects" premise as **disproven** — nothing is lost — and filed what it left behind. The measurement
that survives: on the demo box, `make test-claim-ceremony` ×5, **4/5 runs missed a 5-second grant window
while every grant was present in `capability-kv` minutes later**. Run-1's timeline is the whole story:
66 sibling pipelines processed U's `holdsRole` link event at 21:48:50; `capabilityRoles`, *which intakes
the broad Core-KV stream and evaluated ~2 events/sec through the ceremony's own burst*, reached it at
21:49:04 — **+14 s of pure queue position**.

Two things are wrong there, and they are different:

1. **The queue contains events that cannot possibly matter.** `capabilityRoles` binds exactly three
   labels; it is fed all 16k+ keys of the graph.
2. **~2 events/sec.** ~500 ms per event on a lens whose cypher is a two-hop walk. That is not the
   cypher — it is the fan-out multiplying it.

Grant visibility is therefore *queue-position luck*, and the ceiling is set by whatever else the graph
happens to be doing. Facet's `isTransientAuthLag` client retry papers over the symptom. The intent is to
remove both terms in Refractor, where they live.

## 2. Grounding ledger (verified `file:line`, this fire)

| Fact | Where |
|---|---|
| The plain arms skip an event whose type the patterns cannot bind (`plainReactsTo` / `plainVertexRelevant`) | `pipeline/pipeline.go:505`, `:532` |
| The actor-aware arms have **no** such gate — aspect, link, and vertex all dispatch unconditionally | `pipeline/pipeline.go:1186`, `:1197`, `:1243` (the KindVertex gate is explicitly `actorEnumerator == nil &&`) |
| D1's narrowed `FilterSubjects` eligibility excludes every actor-aware pipeline, on the rationale that "an actor-aware pipeline's fan-out is not bounded by its own MATCH labels" | `pipeline/pipeline.go:562-567` (comment `:550-561`) |
| A link event enumerates from **BOTH** endpoints and unions — so a `holdsRole` create enumerates from the *role*, reaching every holder | `pipeline/evaluate.go:786-800` |
| `Enumerate` is an **undirected, relation-name-blind** BFS, depth ≤ 10, actor cap 10 000 — and every actor it finds additionally pulls in its `reportsTo` manager | `pipeline/actor_enumerator.go:161-198`, `:147-159`, `:64`, `:68` |
| Every enumerated actor gets its own full cypher re-execution + write | `pipeline/evaluate.go:849-916` |
| `ReferencedLabels` treats an unlabeled node as exhaustive when the variable is labeled **anywhere** in the query — `labeledVars` is collected globally, with no `WITH` scoping | `ruleengine/full/labels.go:22-60`, `:64-84` |
| …but `projectItems` rebuilds each binding from the projection aliases alone, so a variable dropped at a `WITH` is unbound afterward and re-seeds through the **unlabeled whole-bucket scan** (which admits `KindUnknown` too) | `ruleengine/full/executor.go:1085-1098`, `:654-679` |
| A labeled pattern node also binds a vertex whose **body** `class`/`label` equals the label, regardless of its key type | `ruleengine/full/executor.go:555-573` |
| `capabilityRoles` labels = `{identity, role, permission}`, exhaustive, anchor `identity` | `packages/rbac-domain/lenses.go:80-91`, `:39-41` |
| `myTasks` is non-exhaustive — `(op)`, `(tgt)`, `(qop)`, `(qtgt)` are unlabeled | `packages/orchestration-base/lenses.go:295-334` |
| `capabilityEphemeral` is non-exhaustive — `(op)/(tgt)/(op2)/(tgt2)/(op3)/(tgt3)`, three distinct unlabeled target positions | `packages/orchestration-base/lenses.go:358`+ |
| `unroutedTasks` labels = `{task, role}`, exhaustive, anchor `task` | `packages/orchestration-base/lenses.go:66-84`, `:196-209` |
| `capabilityServiceAccess` is **auth-plane** (`cap.svc.*` in capability-kv) and reaches `instanceOf` / `unavailableAt` only through `WHERE NOT (…)` and `permitsOperation` only through a RETURN pattern-comprehension | `packages/service-location/lenses.go:133-146`, `:38-41`; `projection/plan.go:110-115` |
| The adjacency index has its **own** dedicated whole-stream consumer; a pipeline's link pre-apply is an ordering heal, not the authoritative write | `refractor/consumer/bootstrap.go:23-29`, `:69-76` |
| A convergence `SweepPlan` is installed for **every** actor-aggregate lens whose rows it can name; `sweepEnrolment` may **refuse** (no derivable prefix, no `AnchorFromKey` round-trip, no `PrefixKeyLister`) and the refusal only logs a warning | `projection/driver.go:407-433`, `:309-324` (the `sweep.go:265-266` comment "every non-auth-plane lens" is stale) |
| A KindVertex event with an **empty body** is unconditionally ack-and-skipped — actor-aware included; the real actor retraction is the **soft**-delete (`isDeleted: true`, non-empty body) | `pipeline/pipeline.go:1210-1212` (its `:1207-1209` comment is stale); `pipeline/evaluate.go:131-148` |
| Footprint validation already accepts that a sibling write to an *unrelated relation* on a shared hub is not drift (selector-scoped edge comparison) | `pipeline/evaluate.go:351-393` |
| `capabilityRoles` is exempt from footprint validation (single-binding) — so the 500 ms is **not** validation cost | `pipeline/evaluate.go:318-332` |
| Personal lenses consult **two** inputs outside the compiled pattern: the D1 read gate (`cap-read.*`, capability-kv) and the Interest Set (`personal-lens-interest`) | `projection/personal.go:130`, `:172-184`, `:186-195`; `capabilityread/capabilityread.go:73` |
| `SecureDecryptor` reads `vtx.identity.<id>.piiKey` keyed off a RETURN column, not off anything the executor bound | `pipeline/secure.go:130-137`, `:194-196` |
| `CoreKVNarrowedFilters` + the ≤8-label cap already exist and are proven non-subset-overlapping against the pin | `subjects/subjects.go:150-187`, `pipeline/pipeline.go:24-31` |
| Pin: `nats-server v2.14.0` — D1's vendor gate (plural `FilterSubjects`, per-filter pending, live-editable filters, **no cursor reset on update**) was cleared against this exact source and is unchanged here | `go.mod:10`; `refractor-footprint-reduction-design.md` §3 D1 |

## 3. Why the latency is unbounded — the three terms

Write the cost of one auth-lens message as `intake × enumerate × reproject`:

- **Term A — intake is the whole graph.** Every Core-KV write is delivered to `capabilityRoles` and
  dispatched into a fan-out. A bulk import, a busy vertical, a package reinstall — any of them push auth
  latency arbitrarily high. **This is the "no upper bound" in the row title.**
- **Term B — enumeration is undirected and relation-blind.** `Enumerate` walks adjacency from the event
  vertex ignoring relation names and direction, so it answers *"which actors are near this node"*, not
  *"which actors' projections changed"*. For a `holdsRole` link the enumeration from the **role**
  endpoint returns every holder of that role — plus each of their `reportsTo` managers.
- **Term C — reprojection is per enumerated actor.** Each returns a full cypher execution + a
  Capability-KV write. Term B's over-breadth is multiplied here. `4 × 60` co-holders is the measured
  ~2 events/sec, and it is *quadratic in a claim burst*: N concurrent claims × M co-holders.

Term A is why unrelated load hurts. Terms B+C are why the ceremony hurts **itself** — which is why
fixing only A would have left the measured 5–20 s largely intact.

## 4. The shape

### 4.1 The soundness claim — *pattern-closed output*

Everything below rests on one claim, so it gets its own name, its own limits, and its own gate:

> A lens's output for anchor `A` is **pattern-closed** when it is a function solely of the subgraph the
> compiled pattern binds starting from `A`.

For a pattern-closed lens with an **exhaustive** label set `S`, an event on a key whose label ∉ `S`
does not change any anchor's row: property reads resolve to aspects `vtx.<label∈S>.<id>.<local>`, and
traversals consume adjacency edges whose link keys `lnk.<tA>.<idA>.<rel>.<tB>.<idB>` have both endpoint
types in `S`. This is the *same* argument the shipped plain gate already makes — and the platform has
already ratified its sharper form, in footprint validation's §13.4 selector-scoped edge comparison
(`evaluate.go:351-393`): a sibling write to an unrelated relation on a shared hub is not a change to a
typed walk.

**This is not a proof from the AST alone.** It holds only on top of two *data* invariants the code does
not currently enforce, and §4.3 is the increment that makes them real. State it honestly: pattern-closed
narrowing is sound **given** Increment 0's two gates, and unsound without them — which is also true of
the D1 narrowing already shipped for the plain corpus.

### 4.2 The eligibility predicate — a conjunction, every conjunct fail-closed

| Conjunct | Why | Failing it means |
|---|---|---|
| `engineKind == EngineFull` | `ReferencedLabels` only exists for the full engine | broad filter, no gate |
| `!plainReprojectAll` (exhaustive label set) | non-exhaustive ⇒ any type may bind | broad filter, no gate |
| **`anchorType ∈ labels`** | *new, actor-aware only.* If the anchor's own type is not a pattern label, the anchor's **soft-delete** event (`isDeleted: true` on its vertex root — `evaluate.go:131-148`) would not be delivered and its row would never retract. **On the auth plane a missed retraction is an over-grant** | broad filter, no gate |
| **`patternClosedOutput`** | *new.* Excludes lenses with an input outside the compiled pattern — §4.4 | broad filter, no gate |
| **`hasSweepPlan`** | *new.* Narrowing removes an *accidental* heal (§6). A lens `sweepEnrolment` **refused** (`driver.go:309-324`, warn-only) has no standing healer, so it must not also lose incidental reprojection | broad filter, no gate |
| **`secureDecryptor == nil ∨ identityKeyType ∈ labels`** | *new.* The decryptor reads `vtx.identity.<id>.piiKey` off a RETURN column, not off a bound node (`secure.go:130-137`). A shred must still be delivered or the lens keeps projecting decrypted PII | broad filter, no gate |

`plainReprojectAll` and `plainReprojectLabels` are the values `useFullEngineBranches` already computes
from **every** compiled branch (`pipeline.go:401-464`) — one derivation, not two. Every new conjunct
defaults to its *unsafe-side* value (`false` / broad), so a lens installed through a path that forgets
to set one keeps today's behavior exactly.

### 4.3 Increment 0 — make the shipped derivation actually sound

Two latent under-approximations, both of which **shipped D1 already depends on** for the plain corpus.
Neither has a live instance today; both are load-bearing the instant a lens is narrowed, and neither is
detected by anything.

**0a — `ReferencedLabels` is not `WITH`-scoped (fix in code, fail-closed).** `labeledVars` is collected
across every `Match` clause globally (`labels.go:22-60`), so a later unlabeled `(x)` is treated as a
re-reference and leaves `exhaustive = true`. But `projectItems` rebuilds each binding from the
projection aliases alone (`executor.go:1085-1098`), so a variable dropped at a `WITH` is **unbound**
afterward and `matchPath` re-seeds it through `seedNodes`' unlabeled whole-bucket scan — which admits
`KindVertex` *and* `KindUnknown` of any type (`executor.go:654-679`). Shape:
`MATCH (a:role) … WITH <a not carried> … MATCH (a)-[:r]->(b:x)`. Today that reports a label set that
provably excludes types the executor really binds. **Fix:** scope `labeledVars` per `WITH` segment — a
variable not carried through a `WITH` stops counting as labeled downstream, so the shape sets
`exhaustive = false` and falls back to broad. Corpus census run this fire: the only `MATCH`-after-`WITH`
in `packages/**` + `internal/bootstrap/lenses.go` is `wellness-ledger/lenses.go:249-251`, where the
variable **is** carried (`WITH DISTINCT id`) — a genuine re-reference, unaffected. So the fix changes no
live lens's classification; it removes a trap.

**0b — a labeled node also binds by body `class`/`label` (gate at authoring time).** `nodeMatches`
(`executor.go:555-573`) admits a vertex whose key type ∉ `S` when its stored body carries
`class`/`label` equal to a pattern label. Reachable only through *traversal* (the anchor scan is
key-scoped, `executor.go:654-655`) — i.e. exactly the arm this design narrows. Census: package-built
vertices set `class` equal to their key type (`pkgmgr/build.go:847-851`; `docVertex("role", …)` `:72`,
`docVertex("permission", …)` `:312`), and meta vertices carry dotted classes (`meta.lens`) that cannot
collide with a bare label token. So the invariant *holds* — it is simply unwritten.

Per the ratified lint doctrine (*"lint is how agents are actually forced to do the right thing"*), the
gate ships **here, as a required fire, not as defense-in-depth**: `scripts/lint-conventions.go` gains a
`packages/**` check that **default-denies** a vertex body whose `class`/`label` is a *bare token
differing from the key's type segment*, with a declared-exception annotation for the dotted-class case,
mirroring the shipped `# read-posture: (a|c|d|e|f)` shape (`lint-conventions.go:132/:317/:493`). The
migration leaves zero debt, so the gate is **blocking from day one** — a warn-first gate over a clean
tree is exactly the fingers-crossed state the fire exists to end.

Increment 0 is independently valuable and independently shippable: it hardens narrowing that is
**already in production** for 90-odd plain lenses.

### 4.4 The lens classes that are **not** pattern-closed

- **Personal (nats-subject) lenses — excluded.** Two out-of-pattern inputs, not one: the **D1 read gate**
  (`capabilityread.IsReadable` reading `cap-read.<domain>.<actor>` from capability-kv,
  `personal.go:172-184`) and the **Interest Set** (`personalinterest.IsRelevant` on
  `personal-lens-interest`, `:186-195`). Concretely for the first: an actor granted a new *role* that
  widens their read grants gets their rows today because the broad filter delivers the `role` event and
  the BFS reaches them; under a filter narrowed from a data lens's `{booking, …}` labels that event
  never arrives, and rows the actor is now entitled to would not appear until the sweep or the next
  business event. `patternClosedOutput = false` for every personal lens, no exceptions, and a test pins
  it. Naming *both* inputs matters: a later "personal lenses are pattern-closed now that X is fixed"
  argument must have to clear both.
- **Secure lenses — conjunct, not exclusion.** Handled by §4.2's decryptor conjunct. Worth noting this
  closes a **pre-existing** gap: secure lenses are plain (`pkgmgr/bucketguard.go:149`) and therefore
  already narrowed by shipped D1, and every installed one happens to source its identity key from an
  `:identity`-labeled node — by luck, with no gate.
- **`$now`-dependent predicates — not this mechanism's problem.** `capabilityEphemeral`'s
  `expiresAt > $now` and `unroutedTasks`' `$now > expiresAt` depend on wall-clock, and are **already**
  served by the freshness-marker plane (`Freshness: auto` → Weaver convergence) plus the sweep, never by
  incidental recomputes. Narrowing removes an accident, not a mechanism.
- **`projectedFromRevisions` — benign, and named so it is not re-discovered.** The row is stamped with
  the lens's own `vtx.meta.<ruleID>` revision (`projection/freshness.go:23-47`, `driver.go:118`, `:368`)
  — a `meta` key in no auth lens's label set. It changes only when the lens definition changes, which
  rides hot-reload → `Rebuild` → consumer reset.

  **Correction (Increment 1 build, 2026-08-02).** The original second half of this bullet claimed *"a
  `vtx.meta.*` event reaches no actor through the BFS today either"*. That is **false**: `Enumerate` is
  relation- and type-blind (`pipeline/actor_enumerator.go:24-52`) and meta vertices are linked into the
  graph (`permission forOperation meta`), so a meta write does reach identities via
  `meta → permission → role → identity` and does drive a `capabilityRoles` recompute today. The
  conclusion survives on a different footing: `capabilityRoles` reads no meta **property**, and
  `ContributingSources` stamps the row from a revision *lookup* rather than from a delivered event, so
  the skip changes no projected value. The real effect is that `projectedFromRevisions` on
  already-written rows goes stale until the sweep's deep verify reaches that anchor — and
  `freshness.go:14-19` records that this datum is coherence provenance, **not** the write-ordering
  guard (`projectionSeq` is), so it carries no auth risk. Do not reuse the original premise.

### 4.5 Increment 1 — relevance-gate the actor-aware arms (client-side)

Add the exact counterpart of `plainReactsTo`/`plainVertexRelevant` to the three fan-out arms, guarded by
§4.2's predicate:

- `pipeline.go:1186` (aspect): parse the parent vertex type; ack-and-skip when eligible and the type ∉ labels.
- `pipeline.go:1197` (link): parse both endpoint types; ack-and-skip when eligible and **neither** ∈ labels.
- `pipeline.go:1243` (vertex): drop the `p.actorEnumerator == nil &&` conjunct, replacing it with the
  shared predicate so plain and actor-aware pipelines consult one gate.

Smallest change that proves the predicate on live traffic while the delivery path is untouched —
deliberately shipped **before** the server-side narrowing, so the derivation is exercised end-to-end
with a trivially revertible blast radius. Also fixes the stale `:1207-1209` comment (empty-body KindVertex
is acked, not fanned out).

### 4.6 Increment 2 — narrowed `FilterSubjects` for eligible actor-aware pipelines (server-side)

Relax `NarrowedFilterEligible` (`pipeline.go:562-567`): replace `actorEnumerator != nil` with §4.2's
predicate. The comment at `:550-561` needs rewriting, because it conflates two questions:

> *"an actor-aware pipeline's fan-out is not bounded by its own MATCH labels"* — **true of the fan-out**
> (which actors), and **false of the relevance question** (whether any actor is affected at all). D1
> answered the second with the first, which is why the whole class was excluded.

The enumerator is unaffected by the narrower filter because it reads **adjacency**, and adjacency is
maintained by its own dedicated whole-stream consumer (`consumer/bootstrap.go:23-29`), not by this
pipeline's deliveries. The per-pipeline link pre-apply is an ordering heal for links the pipeline *does*
receive; a link it no longer receives is one no eligible lens can bind.

Everything else D1 shipped applies verbatim: the ≤8-label cap, `CoreKVNarrowedFilters`' proven
non-subset overlap, the `registerWithFilterFallback` broad fallback, and `Rebuild` recomputing
`ConsumerFilter()` so a hot-reload that *widens* the label set rides a consumer reset.

Effect on the corpus: `capabilityRoles` → 9 filter subjects covering `identity`/`role`/`permission`;
`unroutedTasks` → 6 covering `task`/`role`. `capabilityEphemeral`, `myTasks`, `orphanedTaskGrants`,
`capabilityServiceAccess` stay broad (non-exhaustive), and every personal lens stays broad by §4.4.
**Term A is removed for the lens the row is about**; Increment 3 is what reaches the rest.

### 4.7 Increment 3 — pattern-directed affected-anchor derivation (Terms B + C)

Replace the undirected BFS with a walk derived from the compiled pattern, seeded by the **changed
element** rather than by a neighboring vertex.

**The hop index must be built over every pattern source.** `ReferencedLabels` already walks
`Match.Patterns`, `Match.Where`, `With.Items`/`With.Where`, and `Return.Items` — including
`Not(PatternExpr)` and `PatternComprehension` (`labels.go:85-143`). The derivation **must walk the same
set**, or it under-approximates on a live auth-plane lens: `capabilityServiceAccess`
(`service-location/lenses.go:133-146`, projecting `cap.svc.*`) reaches `instanceOf` and `unavailableAt`
**only** inside `WHERE NOT (…)` and `permitsOperation` **only** inside a RETURN pattern-comprehension —
and an `unavailableAt` create is a **revocation**. A hop index built from `Match.Patterns` alone would
silently skip it.

**3a — edge-seeded link enumeration (the quadratic-killer).** For a link event
`lnk.<tA>.<idA>.<rel>.<tB>.<idB>`:

1. Match `<rel>` (plus endpoint labels and direction) against the hop index. **No matching hop ⇒ skip —
   but only when the index is `complete`**, a flag the builder sets false the moment it meets a source
   it cannot index. An incomplete index never skips; it falls back. "Not in the index" and "not
   indexable" must not be the same answer.
2. A matching hop identifies which endpoint sits **anchor-side**. Walk the chain *backwards from that
   hop* to the anchor variable via `adjacency.Neighbors`, direction flipped and relation-name filtered
   per hop. The far endpoint's *other* edges are never traversed. A relation appearing at several hops
   yields several back-walks, **unioned** — several positions is the normal case, not a fallback.

For `capabilityRoles`: `holdsRole` matches `(identity)-[:holdsRole]->(role)`; the anchor-side endpoint is
the anchor itself; **affected = {that identity}** — one execution, not sixty. `grantedBy` matches
`(role)<-[:grantedBy]-(perm)`; walking back gives `role ←[holdsRole]— identity` = every holder, which is
**correct and necessary** (a permission newly granted to a role really does change every holder).

**3b — node-seeded reverse walk.** For a vertex/aspect event, match the node's label against the
pattern's node positions (an *unlabeled* position matches any type — which is what lets this reach the
non-exhaustive lenses Increments 1–2 cannot), then union the back-walk from every matched position.
`capabilityEphemeral` binds an unlabeled target at **three** positions (`tgt`, `tgt2`, `tgt3`) whose
back-chains are `scopedTo`+`assignedTo`, `scopedTo`+`assignedTo`+`reportsTo`, and
`scopedTo`+`queuedFor`+`holdsRole`. An event on `vtx.booking.<id>` therefore costs a handful of
relation-filtered adjacency reads (≥6 across the three chains, each pruning immediately when the typed
edge is absent) instead of a depth-10 undirected BFS plus a cypher execution and a Capability-KV write
per actor it happened to touch.

**Over-approximation is the invariant, and it is directional.** The derived set must be a **superset**
of the truly-affected anchors. Under-approximation on the auth plane is a stale grant — an over-grant if
a revocation is missed. Every shape the derivation cannot resolve — a variable-length hop *inside a
back-chain*, an unindexable pattern source, a position it cannot walk back from — **falls back to
today's `ActorEnumerator` BFS**, unchanged. Same conservative posture `ReferencedLabels` exhaustiveness
already takes, and the fallback is the shipped code path, so a derivation bug degrades to current
behavior rather than to silence.

**Relationship to §D2 Phase 2 — and the shadow measurement that decides it** *(added post-ratification,
2026-08-01, agreed with Andrew in the ratification session)*. `refractor-footprint-reduction-design.md`
§D2 Phase 2 describes this same reverse-chain derivation for the **plain**-lens corpus. Its named
trigger is **unmeasured**: the one prior attribution was wrong (the §7 crawl was an actorAggregate
AckWait live-lock, since fixed), and two shipped fires push the true ratio in opposite directions — D1
raised the *neighbor share* of a plain lens's deliveries (it now receives only its referenced labels, so
a 3-label/1-anchor lens is neighbor-labeled on two of three), while Fire 2 lowered the *cost* of each
neighbor recompute (the corpus rescan is prefix-scoped, not whole-bucket, `executor.go:654`). Guessing
between them is exactly what produced the wrong attribution the first time.

Note what this is **not**: the classic dead-scaffolding case. Phase 2's consumer *exists* — every plain
lens is live and every neighbor event rescans its anchor corpus today (`seedAnchorFor` returns "" for a
non-anchor label, `pipeline.go:488`), so Phase 2 would return value on day one. The reason to hold it is
narrower and worth stating exactly: **it is a narrowing on a correctness-critical path, and without a
before-number there is no acceptance criterion.** Under-approximation means a stale projected row; the
cost of being wrong is identical in the plain and actor-aware corpora, and only the actor-aware benefit
is measured. Build cost is not the argument — riding this derivation, Phase 2 is S.

So Increment 3 carries the measurement, and it is close to free because the derivation is already there:

- **Shadow mode on the plain arm.** Run the derivation for plain-lens events, compare the derived
  affected-anchor set against the anchor corpus the full recompute would rescan, count the delta per
  lens — and **act on neither**. No behavior change, no correctness risk, and it yields the exact
  per-lens ratio Phase 2's trigger asks for.
- **Sampled**, 1-in-N, so the instrumentation cannot cost the path it is measuring. The builder sizes N
  against the plain arm's own budget; the adjacency reads the shadow walk performs are the thing to
  bound.
- **It doubles as the differential test** §9 already requires for the actor-aware build — same
  comparison, run offline against the corpus instead of in-process.

Phase 2 then reduces to reading the ratio and flipping the plain arm from shadow to acting, with a
before-and-after. This design still does not absorb it: one primitive, one acting consumer, plus a
measurement that makes the second consumer a data-driven decision rather than a standing research task.

## 5. Read path / write path / orchestration

- **P5 (read path)** — untouched. Consumers read `cap.roles.*` / `cap.ephemeral.*` / `cap.svc.*` from
  `capability-kv` via the step-3 dispatcher and the registered auth hook. No new read surface.
- **P2 (write path)** — untouched. Refractor writes only its own lens targets; no Core-KV write, no new
  operation, no DDL change. The guarded auth-plane write path (`projectionSeq` CAS on NATS-KV) is
  unchanged, and the *set of rows written per event shrinks* — it never grows, so no new write-ordering
  window is opened.
- **Orchestration** — none. No Loom pattern, no Weaver convergence lens, no `@at`/`@every`, no directOp.
  Every change is inside the Refractor pipeline's message handler, its consumer registration, the
  compiled-rule label derivation, and one lint gate.
- **Precedent mirrored** — Fire 3 (client relevance gate) and D1 (server-side `FilterSubjects`) verbatim,
  extended to a class they excluded; §D2's reverse-chain derivation, built where both arms can reach it;
  the `# read-posture:` declared-annotation shape for the Increment-0b gate.

## 6. Reconciliation with the existing mental model

- ***Didn't the footprint campaign already narrow this?*** It narrowed **plain** lenses and explicitly
  excluded actor-aware ones at every gate — Fire 3 ("actor-aware pipelines untouched"), D1
  (`actorEnumerator != nil` disqualifies), D2 Phase 1 (`seedAnchorFor` returns "" for any envelope
  pipeline, `pipeline.go:492`). The auth plane is the one corpus that got **none** of the eight fires.
  That is why 95 lenses got 22-second boots while `capabilityRoles` still runs at 2 events/sec.
- ***Doesn't the ActorEnumerator exist precisely because labels don't bound the fan-out?*** It exists to
  answer *which actors to re-project*, and it still does. This design does not remove it; it (a) stops
  asking it about events that cannot matter and (b) gives it a pattern-directed seed where one is
  derivable. Where it is not, the BFS runs exactly as today.
- ***Are we removing a heal path?*** Yes — an **accidental** one, and this is the substitution check run
  deliberately. Today an unrelated booking event incidentally re-projects the booking's actor's
  capability row, which would repair a lost projection. The departing behavior's obligations, each with
  a named owner: **heal** → the convergence sweep, installed for every actor-aggregate lens whose rows
  it can name (`driver.go:407-433`) — and because `sweepEnrolment` can *refuse* with only a warning, §4.2
  makes having a plan a **conjunct**, so a lens cannot be narrowed *and* healer-less. **Retraction** →
  the `anchorType ∈ labels` conjunct, keyed on the soft-delete path that actually retracts
  (`evaluate.go:131-148`), not the empty-body path that is unconditionally acked (`pipeline.go:1210`).
  **`$now` flips** → the freshness plane (§4.4). Note the third leg is *`Rebuild`*, not restart: an
  ordinary Refractor restart resumes the existing durable from its ack floor (`pipeline.go:932-935`);
  only `Rebuild` resets the consumer (`:1041-1050`).
- ***Does this introduce new state?*** No. Four booleans on the pipeline set at installation, and a
  derivation computed once at activation from the compiled rule — the same lifecycle as
  `plainReprojectLabels`.
- ***Does it contradict D1?*** It corrects one sentence of D1's rationale (§4.6), reuses every mechanism
  D1 built, and — via Increment 0 — supplies the two invariants D1 has been silently assuming. The
  vendor gate D1 cleared against `nats-server v2.14.0` is unchanged; no new JetStream behavior is relied on.

## 7. Contract surface

**No change. Build to them.**

- **Contract #6 (Capability KV)** — key spaces (`cap.roles.*`, `cap.ephemeral.*`, `cap.svc.*`, §6.1
  disjoint contribution), §6.8 *absence = denial*, and §6.13 Output descriptors are all untouched: the
  same rows are written to the same keys with the same guard, only fewer redundant times. §6.8 is in
  fact *strengthened* by the `anchorType ∈ labels` conjunct, which refuses to narrow a lens whose anchor
  soft-delete could otherwise go undelivered.
- **Contract #9 (Identity Claim Flow)** — makes no statement about grant-visibility timing and gains
  none. This design deliberately does **not** add a latency promise to a frozen contract (§8.1).
- **Contract #1** — key shapes are consumed, never produced, by every derivation here. Increment 0b's
  gate enforces an invariant *about* Contract #1 vertex bodies but adds nothing to the contract text.

## 8. Risks + alternatives

### 8.1 The one question for Andrew — read-your-own-grant

| Option | What it is | Trade-off |
|---|---|---|
| **A. Eventually-consistent + client retry (recommended)** | Ship the fires; keep Facet's `isTransientAuthLag`; let the tooling row report measured latency | No new coupling. Latency falls but stays a distribution, not a guarantee. A client that does not retry can still see a 403 immediately post-claim |
| B. Gateway waits for grant convergence | After `ClaimIdentity`, block the response until `cap.roles.<actor>` shows the grant | Puts a lens round-trip on the auth hot path; a lagging Refractor becomes an authentication **outage** rather than a delay; needs a timeout policy that is itself an SLA |
| C. Contract-level SLA on auth-plane projection | Amend Contract #6 with a bound | Promises what the substrate cannot guarantee under load — the exact mistake `verify-claim-ceremony.go`'s 5 s assertion already made, at contract scope |

**Recommendation: A.** The defect is unboundedness, not asynchrony; B converts a latency problem into an
availability one, and C writes a promise we would then have to defend. If the measured p99 after
Increment 3 is still poor for a real product surface, B becomes a *sequenced* follow-on with data behind
it — not a decision to take now.

### 8.2 Alternatives considered, and why each loses

- **Dedicated intake lane / priority consumer for auth-plane lenses.** Rejected: after Increments 1–2
  the auth lens's queue contains *only* auth-relevant events, and prioritizing among events that all
  matter buys nothing. It also adds a second delivery mechanism beside the per-lens durable — and the
  per-lens ack floor **is** the resume/rebuild mechanism (footprint design §3, "deliberately not in
  scope"). *Could a variant beat the recommendation?* Only if intake were still contended after
  narrowing — which is precisely what Increment 2 removes, and what the measurement gate checks.
- **Parallel per-actor evaluation inside one message.** Rejected: it attacks Term C while leaving B's
  over-breadth intact (more CPU doing the same wasted work), and it races the guarded write's monotonic
  `projectionSeq` ordering within a single message's result set.
- **Synchronous grant write on the write path** (Processor writes `capability-kv` directly). Rejected
  outright: violates P2 and the Capability=Lens decision — projection correctness *is* auth correctness
  precisely because there is one producer.
- **Just raise the ceremony's client-side retry window.** Rejected: that is the shipped workaround, and
  it leaves latency proportional to unrelated business load — the row's actual complaint.
- **Only do Increments 0–2** (the cheap, mirror-a-shipped-mechanism half). Honestly assessed: it removes
  Term A entirely for `capabilityRoles` and hardens the shipped plain narrowing. But the measured burst
  is Terms B+C (N claims × M co-holders), so it would leave most of the 5–20 s in place, and would leave
  `capabilityEphemeral` and `capabilityServiceAccess` — the *other* auth-plane lenses, both ineligible
  by §4.2 — with no improvement at all. Increment 3 is the one that reaches them.
- **Skip Increment 0 as "no live instance".** Rejected: both holes are already load-bearing for the ~90
  plain lenses D1 narrowed in production. "No live instance" is the state a gate preserves, not a reason
  to skip one.

### 8.3 Risks

| Risk | Mitigation |
|---|---|
| A narrowing bug silently blinds an auth lens → **stale grant / over-grant** | Every conjunct fail-closed and defaulted to broad (§4.2); Increment 0 supplies the two data invariants the derivation rests on; Increment 1 (client gate, fully revertible) ships and is measured before Increment 2 touches delivery; the sweep is a conjunct, so a narrowed lens always has a healer; e2e proves revocation still retracts under a narrowed filter |
| Increment 3's derivation under-approximates on an unanticipated pattern shape | Superset invariant is explicit and tested; the hop index carries a `complete` flag and never skips when incomplete (§4.7); every unresolvable shape falls back to the shipped BFS; a differential test asserts derived ⊇ affected across the installed lens corpus (§9) |
| A lens hot-reload widens labels and the consumer keeps a stale narrow filter | Already solved by D1: `Rebuild` recomputes `ConsumerFilter()` and a widened set rides a consumer reset |
| **Reverting Increment 2 does not heal what it filtered out** | Asymmetric by construction — a JetStream filter update never rewinds the cursor (v2.14.0). Recovery from a bad narrow is `Rebuild` (consumer reset + re-projection) or the sweep, **not** a code revert. Stated as the rollback procedure in the fire brief; it is also why Increment 1 ships first, since *its* revert is symmetric |
| Personal lenses regress via an out-of-pattern input | Excluded by construction (§4.4), both inputs named, with a test pinning that a personal lens never narrows |
| Fewer incidental recomputes expose a latent lost projection | That is the sweep's job and it is a narrowing conjunct; the broader gap is the separately-filed "lens health is liveness-only" row, not incidental churn |

## 9. Test strategy + measurement

**Units.** Per-arm gate tests mirroring the shipped `vertex_relevance_internal_test.go` /
`narrowed_filter_internal_test.go` shapes: the aspect/link/vertex arms skip an out-of-label event on an
eligible actor-aware pipeline and **do not** skip on an ineligible one; each of the four new conjuncts
independently forces broad (`anchorType ∉ labels`; `patternClosedOutput == false`; no sweep plan; a
decryptor whose identity type ∉ labels); a personal lens never narrows.

**Increment 0.** A `labels.go` unit for the `WITH`-drop shape (`MATCH (a:role) … WITH <without a> …
MATCH (a)-[:r]->(b:x)`) asserting `exhaustive == false`, plus a regression pinning that
`wellness-ledger`'s carried-through `WITH DISTINCT id` stays exhaustive. A `lint-conventions` unit for
0b: a bare-token `class` mismatching the key type is denied; the annotated dotted-class case passes.

**The derivation's superset invariant (the one that matters).** A differential test over the installed
lens corpus: for a generated set of graph mutations, assert that every anchor whose projected row
differs between pre- and post-event full recomputes appears in the derived set. A shrinking bug fails
here, not in production. Include `capabilityServiceAccess` explicitly — an `unavailableAt` create must
appear in the derived set via the `WHERE NOT` hop.

**E2E (ephemeral stack).** (a) `AssignRole` on actor U projects U's grant and leaves every co-holder's
row revision **unchanged** — the direct proof of Term B+C; (b) `RevokeRole` still retracts under a
narrowed filter, and an actor soft-delete still deletes its `cap.roles.*` key (retraction is the
over-grant direction); (c) an unrelated business write produces **no** `capabilityRoles` delivery at all
(Term A, asserted on consumer delivery count, not on absence of a write); (d) `capabilityEphemeral` —
broad-filtered, non-exhaustive — still projects and retracts identically after Increment 3.

**Gates per fire.** `go build ./...` · `make vet` · `golangci-lint run ./...` · `make verify-kernel` ·
`STRICT=1 go run ./scripts/lint-conventions.go` · `go test ./internal/refractor/...`, plus the **full
`-p 4` suite** for Increments 0, 2 and 3 (Increment 0 changes a derivation every plain lens already
consumes; 2 and 3 change delivery and evaluation for a class of lenses — wide blast radius per the
shipped-Fire-2/3/4 precedent).

**Measurement — the acceptance evidence.** On the demo box, before/after each increment:
`capabilityRoles` per-event latency (already in Health KV via `LatencyRingBuffer`), its consumer pending
under a ceremony burst, and end-to-end grant visibility measured by `make test-claim-ceremony` run under
the tooling row's convergence-poll harness. The design is accepted when grant visibility is
(i) insensitive to unrelated business write volume and (ii) flat in the number of concurrent claims —
**not** when it clears a fixed number of seconds.

## 10. Decomposition for the Steward

Each increment is independently shippable and green on its own.

| Inc | Scope | Size | Notes |
|---|---|---|---|
| **0 — Harden the shipped derivation** | 0a: `WITH`-scope `labeledVars` in `ruleengine/full/labels.go` so a dropped-and-re-referenced variable sets `exhaustive = false`. 0b: `scripts/lint-conventions.go` default-denies a `packages/**` vertex body whose bare-token `class`/`label` differs from the key's type segment, with a declared-exception annotation. Units per §9 | S | Valuable standalone — hardens narrowing already live for ~90 plain lenses. Full `-p 4` suite (shared derivation). **Build first** |
| **1 — Relevance-gate the actor-aware arms** | §4.2's five conjuncts wired at `projection/driver.go` / `projection/personal.go` (all defaulting broad); the three arms at `pipeline.go:1186/:1197/:1243` consult one shared gate; fix the stale `:1207-1209` comment; units + e2e (a)/(b) | M | Client-side only, delivery untouched, revert is symmetric. Ship and measure before touching delivery |
| **2 — Narrow the eligible actor-aware consumers** | `NarrowedFilterEligible` swaps `actorEnumerator != nil` for the shared predicate; rewrite the `:550-561` comment (§4.6); e2e (c). Everything else is D1's shipped machinery | S | Full `-p 4` suite. Measurable alone: `capabilityRoles` intake drops to 9 subjects. Rollback is `Rebuild`, **not** a code revert (§8.3) |
| **3 — Pattern-directed affected-anchor derivation** | New unit under `pipeline/` deriving the affected-anchor set from the compiled pattern + the changed element (3a edge-seeded, 3b node-seeded), hop index over **every** pattern source with a `complete` flag, superset invariant + fallback to the shipped BFS; wire the three actor-aware arms; **plus the sampled plain-arm shadow measurement (§4.7) — derive, compare, count, act on neither**; differential test; e2e (a) tightened to co-holder-revision-unchanged, plus (d) | M–L | Full `-p 4` suite. Built as a standalone unit so §D2 Phase 2 can wire the plain arm later without a second derivation. The shadow counters are what turn Phase 2 into a flag flip with a before-and-after |

Deliberately **not** in scope: the plain-arm wiring of §D2 Phase 2 (trigger unmeasured — §4.7); any
change to the enumerator's caps or its `reportsTo` hop; `verify-claim-ceremony.go`'s convergence poll
(its own board row, and the harness this design is measured with); any Contract amendment.

## 11. Pre-build gates

- **Adversarial pass: RUN and DISCHARGED, 2026-08-01** (independent read-only reviewer, instructed to
  refute). Ten findings, every one grounded in `file:line` and re-verified by me before folding. The
  three that changed the design's *shape*:
  1. §4.1's soundness claim was stated as *provable from the AST* and is not — `nodeMatches` binds by
     body `class`/`label` (`executor.go:555-573`) and `ReferencedLabels` is not `WITH`-scoped
     (`labels.go:22-60` vs `executor.go:1085-1098`). Both also underpin **shipped** D1. → **Increment 0**
     now exists, and §4.1 states the claim with its real preconditions.
  2. Increment 3's "no matching hop ⇒ skip" would have silently dropped a live revocation on
     `capabilityServiceAccess`, whose hops live only in `WHERE NOT` and a RETURN comprehension. →
     §4.7's hop index walks every pattern source and carries a `complete` flag; multi-position
     back-walks union rather than fall back.
  3. The ledger's "sweep is installed for auth-plane lenses only" was false (`driver.go:407-433`;
     `sweep.go:265-266`'s comment is stale), and §6's substitution argument rested on it. → **`hasSweepPlan`
     is now a conjunct**, plus the decryptor conjunct from the same finding.
  Six further corrections were folded in place: the empty-body-vs-soft-delete retraction path
  (`pipeline.go:1210`), the swapped `myTasks`/`capabilityEphemeral` ledger ranges, the Interest Set as a
  second personal-lens out-of-pattern input, `projectedFromRevisions`, the boot-replay-vs-`Rebuild`
  correction in §6, and Increment 2's asymmetric rollback (§8.3). One finding was accepted without a
  design change (`addHierarchyManager` further amplifies Term B — added to §2/§3 as supporting evidence).
- No further deferred gate. This design is build-ready on Andrew's ratification.

---

## 13. Increment 0 fire brief (build note, 2026-08-02)

**Scope sentence (§10, verbatim).** *"0a: `WITH`-scope `labeledVars` in `ruleengine/full/labels.go` so a
dropped-and-re-referenced variable sets `exhaustive = false`. 0b: `scripts/lint-conventions.go`
default-denies a `packages/**` vertex body whose bare-token `class`/`label` differs from the key's type
segment, with a declared-exception annotation. Units per §9."*

**Shipped this fire: 0a. 0b is NOT shipped — its premise is falsified (below).**

### 13.1 Verified touch-list (checked live)

| File | Anchor | What |
|---|---|---|
| `ruleengine/full/labels.go:15-145` | `ReferencedLabels` | `labeledVars` scoped per `WITH` segment; a `carryLabeled` step keeps a label only for a bare-variable projection item, under the name the `WITH` gives it |
| `pipeline/filter_retraction_internal_test.go:487` | `TestReferencedLabels_Contract` | unchanged; new sibling `TestReferencedLabels_WithScoping` beside it |

Design citations re-verified, all live: `labels.go:22-60` (global collection), `executor.go:1085-1098`
(`projectItems` rebuilds bindings from projection aliases), `executor.go:654-679` (unlabeled seed scan),
`executor.go:1193-1201` (`projectionAutoAlias` — the alias rule the carry step mirrors),
`pipeline/pipeline.go:437` (the only non-test caller).

### 13.2 The carry rule

A `WITH` rebuilds every binding from its projection items alone, so the label survives only when the item
**is** the node binding: `WITH a` and `WITH a AS b` carry it (under `a` / `b` respectively, mirroring
`projectionAutoAlias`); `WITH a.key AS a` does not — that name now holds a scalar, and a later `(a)`
re-seeds through the whole-bucket scan. Everything else is unchanged, including the within-segment
re-reference rule and the variable-length-relationship clause.

### 13.3 Corpus census — no live lens's classification moves

Run this fire over **every** shipped lens: the 97 `full`-engine specs reachable from `pkgregistry`
(**after** `ExpandReadGrantWalks`, so the generated read-grant producers and their staged `WITH`s are
included) plus the four `internal/bootstrap` kernel lenses. The `(exhaustive, labels)` output is
**byte-identical** before and after, all 101 rows.

Two shapes were the risk and both survive: `wellness-ledger`'s `memberAccountsSpec` (`lenses.go:249-251`)
carries its variable through (`WITH DISTINCT id`), and the generated producer stages
(`pkgmgr/anchorwalk.go:534-565`) carry the labeled actor (`anchorWalkHead`, `anchorwalk.go:72`) through
every stage. Both are pinned as test cases so a future authoring change cannot silently un-pin them.

That census is what makes the deploy free of a transition step, and the transition is the part worth
naming for whoever lands a lens that *does* take a WITH-drop shape: a verdict change reaches a live
consumer by two different routes, and only one of them backfills. A MATCH hot-reload recomputes the labels
and then runs `Pipeline.Rebuild` (`cmd/refractor/reload.go:346,359-362`; `pipeline.go:1031-1041`), which
re-derives `ConsumerFilter` and resets the consumer. A bare process restart re-derives the filter too
(`cmd/refractor/main.go:1009-1013`) but lands it through `CreateOrUpdateConsumer`
(`substrate/consumer_supervisor.go:407-449`), which updates `FilterSubjects` in place **without** moving
the delivery cursor — so it corrects future events and never repairs rows that projected under the old,
wrongly-exhaustive verdict. A future lens whose verdict actually moves needs a `Rebuild`, not a restart.

### 13.4 0b — the premise is falsified; not built, re-scoped

§4.3's census concluded *"package-built vertices set `class` equal to their key type … So the invariant
**holds** — it is simply unwritten"*, and on that basis specified a **blocking-from-day-one** gate over a
zero-debt tree. The tree is not zero-debt:

- `packages/location-domain/ddls.go:182,187,318` — `LOCATION_TYPES = ["unit", "building", "property"]`
  and `LOCATION_CLASS = "location"`: all three vertex key types carry a **bare-token class differing from
  their key type segment**, deliberately and documented (`ddls.go:28-33`: *"the type is the key segment,
  the class is the shared discriminator"*). `loftspace-domain` guards on it (`ddls.go:29-31,54,64`).
- The §4.3 census generalized from `pkgmgr/build.go:847-851` — which covers **pkgmgr-built** vertices
  (roles, permissions, meta). Op-created business vertices are a different population and were not in it.

The mechanism is also wrong for this corpus, independently of the debt: package Starlark writes the class
as a **variable** (`make_vtx(loc_key, LOCATION_CLASS, {})`; `{"class": cls, …}` at
`clinic-domain/ddls.go:938`, `loftspace-domain/ddls.go:235`), and `lint-conventions.go` is line-based
regex with no AST or Starlark evaluation. A literal-pair gate would therefore report the tree **clean
while missing the one real violation in it** — precisely the fingers-crossed state §4.3 says the fire
exists to end.

**Still no live victim:** no lens in `packages/**` or `internal/bootstrap` labels a pattern node
`:location`, so nothing binds a `unit`/`building`/`property` vertex through the body-class path today.
The hazard §4.3 names is unchanged and latent.

**Re-scope (filed, not silently dropped).** The enforcement point should follow the threat: the unsound
step is a **lens pattern label** that names a class-only token, since that is where the narrowing decision
is taken and it is statically decidable from the cypher plus the installed type vocabulary — not the
class write, which is neither statically visible nor actually invariant. That is a change to a ratified
design's mechanism, so it goes back through the Designer rather than being improvised here; the board row
carries it.

### 13.5 Adversarial review — one finding was in the fire's own change

Two independent read-only reviewers ran against the diff (soundness; blast-radius + house rules). The
blast-radius pass was clean and independently reproduced the census result. The soundness pass landed one
finding **in the new code**, folded before merge:

- **The closing `WITH`'s own `WHERE` was judged in the pre-`WITH` scope.** `applyWith`
  (`executor.go:1042-1060`) projects *first* and filters the projected rows *second*, so a variable the
  `WITH` drops is already unbound when its own `WHERE` runs — and a pattern there naming that variable
  re-seeds through the whole-bucket scan exactly as one in a following `MATCH` does. The first cut
  swapped the scope only *after* walking the `WHERE`, so `WITH p WHERE (a)-[:heldBy]->(b:identity)`
  still reported `exhaustive = true`. The carry now lands before the `WHERE` walk, and the case is a
  pinned test (confirmed to fail against the un-fixed ordering).

Two further findings are **pre-existing and out of this increment's scope**, both filed as rows:

- Pass 1 is forward-looking within a segment, so a label reaching a variable only from an **OPTIONAL
  MATCH** or a **negated/disjunctive `WHERE` pattern** still excuses an earlier unlabeled sighting —
  neither position constrains the surviving binding, so the executor seeds it whole-bucket. A required
  `MATCH` later in the segment *does* constrain it, and stays sound.
- `nodeMatches`' body-`class` fallback (already §13.4's row).

**Live-victim census for the first:** a detector was written for the precise shape — a variable whose
first pattern sighting is unlabeled and whose only later label sits in an OPTIONAL MATCH or a `WHERE`
pattern — self-checked against both unsound shapes *and* against the sound required-`MATCH` shape, then
swept over all 101 specs. **Zero lenses flagged.** Latent, no live victim. (A blunter probe — stripping
every OPTIONAL/`WHERE` label source at once — flips 9 lenses, but that over-approximates: those lenses
label a variable on its *first* sighting inside an OPTIONAL MATCH, which is a binding position, not a
re-reference. The blunt number is not the victim count.)

### 13.6 Gates run

`go build ./...` · `make vet` · `golangci-lint run ./...` (cache-cleaned, 0 issues) · `make verify-kernel`
· all six `scripts/lint-*.go` gates under `STRICT=1` · the **full `go test ./... -p 4`** suite (§9's
requirement for this increment — a derivation every plain lens consumes), green. The new negative case was
confirmed to **fail** against the pre-change derivation, so it pins the fix rather than the shape.

### 13.7 Non-goals

Increments 1–3 (the relevance gate, the eligibility swap, the pattern-directed derivation); the enumerator's
caps and its `reportsTo` hop; `verify-claim-ceremony.go`'s convergence poll (its own row); any Contract
amendment. Nothing is staged uncommitted.

---

## 14. Increment 1 fire brief (build note, 2026-08-02)

**Scope sentence (§10, verbatim).** *"§4.2's five conjuncts wired at `projection/driver.go` /
`projection/personal.go` (all defaulting broad); the three arms at `pipeline.go:1186/:1197/:1243` consult
one shared gate; fix the stale `:1207-1209` comment; units + e2e (a)/(b)."*

### 14.1 Verified touch-list (checked live; the §4/§10 line numbers had drifted)

| File | Anchor (live) | What |
|---|---|---|
| `pipeline/pipeline.go` | new field beside `secureDecryptor:161`; new predicate beside `plainVertexRelevant:516-541` | `patternClosedOutput bool` (defaults **false**) + `SetPatternClosedOutput`; `actorAwareNarrowingLabels()` evaluating §4.2's conjunction; `actorAwareFanOutRelevant(types…)`; `vertexEventRelevant` |
| `pipeline/pipeline.go:1174-1189` | aspect arm (`p.actorEnumerator != nil` → `evalAspectFanOut`) | ack-and-skip when eligible and the `ParseAspectKey` parent type ∉ labels |
| `pipeline/pipeline.go:1190-1200` | link arm (→ `evalLinkFanOut`) | ack-and-skip when eligible and **neither** `ParseLinkKey` endpoint type ∈ labels |
| `pipeline/pipeline.go:1243` | `if p.actorEnumerator == nil && !p.plainVertexRelevant(label)` | replaced by the shared `vertexEventRelevant(label)` |
| `pipeline/pipeline.go:1207-1209` | stale comment on the empty-body `tombstone` ack | rewritten — the actor-aware pipeline does **not** emit a cap Delete here; the retraction path is the soft-delete (`evaluate.go:131-148`) |
| `projection/driver.go:403-405` | `InstallActorAggregate` | `p.SetPatternClosedOutput(true)` beside `SetActorEnumerator` |
| `projection/personal.go:129-130` | `InstallPersonalLens` | **no edit** — never setting the field is what keeps every personal lens broad (§14.2) |
| `pipeline/actor_aware_relevance_internal_test.go` | new | conjunct table + per-arm skip/no-skip units |
| `refractor/refractor_capability_relevance_gate_e2e_test.go` | new | e2e (a)/(b) with the gate **armed** through the real install gate |

Design citations re-verified live: `useFullEngineBranches` at `pipeline.go:401-464` ✓ ·
`NarrowedFilterEligible` at `:543-567` ✓ · `evaluateLinkFanOut`'s both-endpoint union at
`evaluate.go:786-800` ✓ and its adjacency pre-apply at `:768-784` ✓ · `sweepEnrolment`'s three refusals at
`driver.go:309-324` ✓ · `SetSweepPlan`/`Sweeper()` at `sweep.go:256-266` ✓ ·
`IsPersonalLens` at `personal.go:26-28` ✓ · `SecureDecryptor.readPiiKeyEnvelope`'s
`identityKey + ".piiKey"` at `secure.go:194-196` ✓. The arms sit at **:1174/:1190/:1243**, not
:1186/:1197/:1243.

### 14.2 The one narrowing, and why (scope-diff gate)

The scope sentence says the conjuncts are *"wired at `driver.go` / `personal.go`"*, and §6 calls them
*"four booleans on the pipeline set at installation"*. **Built instead as one lazily-evaluated predicate
reading live pipeline fields, with exactly one new field (`patternClosedOutput`) set at `driver.go`.** Same
conjunction, same fail-closed defaults — only the evaluation moment moves, so this narrows rather than
substitutes.

It has to move: activation order is `UseFullEngineBranches` (`cmd/refractor/main.go:845`) →
`InstallActorAggregate` (`:920`) → **`SetSecureDecryptor` (`:989`)**. A snapshot taken inside
`InstallActorAggregate` would read `secureDecryptor == nil` for **every** Secure Lens and narrow the one
class §4.2 makes a conjunct to protect. `seedAnchorLabel` already documents this exact hazard
(`pipeline.go:111-116`) and `seedAnchorFor` (`:488-499`) is the shipped lazy-evaluation precedent this
mirrors. `personal.go` therefore needs no edit at all: `patternClosedOutput` defaults false, so a personal
lens is broad because nothing ever set it — the fail-closed default doing its job rather than an
easily-deleted negative assignment.

`anchorType` is read from `p.actorEnumerator.actorType` (same package; the value
`InstallActorAggregate` passes is `desc.AnchorType`, `driver.go:403`). `hasSweepPlan` is `p.sweeper != nil`.
The secure conjunct pins the literal type `identity` — the decryptor's read is
`vtx.identity.<id>.piiKey` (`secure.go:195`) and step 6's `sensitiveAspectScope` admits no other parent.

**e2e (a) is the untightened form here.** §9's (a) ends *"and leaves every co-holder's row revision
unchanged — the direct proof of Term B+C"*, which Increment 1 cannot deliver: a `holdsRole` link has both
endpoint types in `capabilityRoles`' label set, so the gate keeps the full fan-out. §10 confirms it —
Increment 3 is where (a) is *"tightened to co-holder-revision-unchanged"*. At Increment 1 (a) asserts
AssignRole still projects U's grant with the gate armed.

### 14.3 Increment order + green checks

1. Predicate + field + setter, no call sites → `go build ./...`
2. Units (conjunct table; personal lens never narrows) →
   `go test ./internal/refractor/pipeline/ -run 'ActorAware' -count=1`
3. Three arms + the stale comment → `go test ./internal/refractor/... -count=1`
4. e2e (a)/(b)+skip → `go test ./internal/refractor/ -run RelevanceGate -count=1`
5. Full suite (capability-plane change, wide blast radius) → `go test ./... -p 4`

### 14.4 In-scope gotchas

- **Fail-open on a parse failure.** A key that fails `ParseAspectKey`/`ParseLinkKey` must fall through to
  the fan-out (which raises the real error), never be skipped.
- **Empty vertex type ⇒ relevant**, mirroring `plainVertexRelevant:536`.
- **The link arm's adjacency pre-apply is lost on a skip, and that is sound**: it is an ordering heal for
  this pipeline's own reprojection (`evaluate.go:752-760`); the authoritative build is the dedicated
  whole-stream adjacency consumer (`consumer/bootstrap.go:23-29`). No reprojection ⇒ nothing to order.
- **Review depth**: capability-plane change ⇒ full 3-layer adversarial before admit, regardless of size.
- No DDL/package/key change ⇒ no `verify-package-*`, no version bump, no `provision-readpath`.

### 14.5 Adjacent finds

- The three hand-wired capability e2es (`refractor_capability_{aspect,link}fanout_e2e_test.go`,
  `ruleengine/full/bootstrap_e2e_test.go`) wire the pipeline field-by-field and never call
  `InstallActorAggregate`, so the gate does **not** arm in them. Not a defect and not filed: it is the
  fail-closed default demonstrating itself, and it is why 14.1 adds a *new* e2e that installs through the
  real gate rather than retrofitting one of those.
- No new board rows. The two soundness rows §13.5 filed (`ReferencedLabels` OPTIONAL/negated-`WHERE`
  pass-1; the `nodeMatches` body-`class` label gate) remain the standing residuals and are untouched here.

### 14.6 Non-goals

Increments 2–3 (`NarrowedFilterEligible`'s eligibility swap and the `:550-561` comment rewrite; the
pattern-directed derivation); the enumerator's caps and its `reportsTo` hop; any change to what the
fan-out does once it runs; `verify-claim-ceremony.go`. Nothing staged uncommitted.

### 14.7 Eligibility census — the gate arms ~10× wider than §4.6 discusses

§4.6 is about **Increment 2's** filter subjects and names two lenses. Increment 1's *client* gate arms
for every lens satisfying §4.2, which this fire enumerated over the whole installed corpus (all
`packages/**` actor-aggregate + personal lenses via `pkgregistry`, plus the four kernel lenses). **17
lenses narrow**, not two:

- **Corrected 2026-08-03 (Increment 2's review re-derived this census).** The counts below did not add up:
the header said 17, the business bullet was labelled 13, and the bullet lists **16** names. The correct
total is **20** — 4 auth-plane + 16 business-convergence. It matters more at Increment 2 than it did
here, because Increment 2 turns each of those client-side skips into a **server-side non-delivery**:
§4.6 discusses two lenses by name, and ~20 are affected.

**Auth-plane (4):** `capabilityRoles` `{identity, role, permission}` · kernel `capability`
  `{identity, role}` · kernel `capabilityRead` `{identity}` · the generated
  `edgeManifestProviderReadGrants` (a D1 `cap-read` grant producer) — the last stays exhaustive only
  because the generated staging `WITH` carries the anchor as a bare variable, which is exactly the case
  Increment 0a's `carryLabeled` keeps labeled.
- **Business convergence (13):** `unroutedTasks`, `leaseExpiry`, `renewalComplete`, `cafeTabSettlement`,
  `cafeStaleTabSettlement`, `clinicNoShowSettlement`, `wellnessNoShowSettlement`,
  `wellnessClassPriceSettlement`, `wellnessOrphanedBookingSettlement`, `wellnessBookingReminders`,
  `appointmentReminders`, `followUpReminders`, `visitSeriesDue`, `pastDueAppointments`,
  `capabilityAuthorPending`, `augurDispatchPending`.

Correctly staying broad: `capabilityEphemeral`, `myTasks`, `orphanedTaskGrants`,
`capabilityServiceAccess`, `clauseSatisfaction`, `leaseApplicationComplete`, `identityAnchors`,
`objectLiveness`, `objectAttachments`, `edgeManifestReadGrants`, `edgeManifestStaffReadGrants`, and
every personal lens.

**The `$now` question the business lenses raise, answered.** Those lenses depend on wall-clock flips, and
the worry is that narrowing filters away whatever re-triggers them. It does not: the re-touch is
Weaver's `MarkExpired` writing `vtx.<type>.<id>.freshnessExpiry` **on the entity from the projected row's
own `entityKey`** (`packages/orchestration-base/mark_expired.go:23-25`; `internal/weaver/temporal.go:132`,
`:292`) — i.e. on the anchor. The aspect arm parses the marker's parent type, which is therefore the
anchor type, which `anchorType ∈ labels` makes a conjunct. The freshness plane is structurally
un-skippable, not accidentally safe. Weaver's `freshUntil` timer likewise runs off the projected row, not
off a Refractor recompute.

`internal/refractor/auth_plane_narrowing_census_test.go` pins the verdict for the auth-plane lenses
against their **shipped** cypher, so a future cypher edit that changes a verdict fails there rather than
in Capability KV.

### 14.8 Deviation from the ratified design, recorded not hidden

§4.1 says the soundness claim holds *"given Increment 0's two gates, and unsound without them"*.
**Increment 0 shipped 0a only** — §13.4 falsified 0b's premise and re-scoped its mechanism back to the
Designer — so the `nodeMatches` body-`class` binding path (`executor.go:554-573`) is live and ungated,
with a real in-tree violation (`packages/location-domain/ddls.go:182,187,318`, where `unit`/`building`/
`property` all carry `class: "location"`).

**Shipping anyway, deliberately.** The exposure is not created here: shipped D1 already rests on the same
invariant for ~90 plain lenses. No lens in the corpus labels a pattern node `:location` or any other
class-only token, so there is no live victim on either plane. What Increment 1 does is **widen the blast
radius of an existing latent hazard to the auth plane**, where its consequence changes from a stale read
model to a grant that never updates and never retracts. The board row for the 0b re-scope is raised to
★★★ and now names the auth plane as its consumer.

### 14.9 Adversarial review — three findings folded, two recorded

Two independent read-only reviewers (soundness; blast-radius + house rules). Neither could break the gate
on the live corpus. The soundness pass independently reproduced §14.7's census and hand-verified all four
auth-plane lenses sound; it also cleared the link arm's lost adjacency pre-apply (for an exhaustive
pattern-closed lens every pattern edge has both endpoints in-labels, so a skipped edge can only shrink
the BFS, and the last hop into the anchor always carries the anchor type). Folded:

1. **The units and e2e never touched the real derivation** — both hand-assigned a label set, and the e2e
   asserted 2 of 6 conjuncts, so a `ReferencedLabels` regression would have left it passing
   byte-identically. → `ActorAwareNarrowingLabels` is exported, the e2e now asserts the derived verdict
   and set, and §14.7's census test drives the shipped cyphers.
2. **A pre-existing test's claim was falsified by this change** —
   `TestHandle_VertexEvent_ActorAwarePipelineIgnoresTheGate` asserted "the gate never applies to an
   actor-aware pipeline", which is precisely what Increment 1 stops being true. It still passes (its
   fixture is ineligible), so it was a documentation defect, not a false pass. → renamed to
   `…IgnoresThePlainGate` and scoped to the plain gate.
3. **Two history-narrating comments** (CLAUDE.md's most-violated rule) → removed; a third instance the
   reviewer missed was removed with them.

Recorded rather than fixed: the decryptor conjunct is **unreachable** in the shipped corpus (secure +
actorAggregate is rejected at translate time, and a secure personal lens is already ineligible), and
`hasSweepPlan` proves *enrolment*, not that a sweep is turning. Both are now stated at the code site. A
third — `plainReprojectLabels` is written by MATCH hot-reload unsynchronized against the consumer
goroutine, which this change makes actor-aware pipelines read too — is filed as its own board row: every
interleaving lands broad (fail-closed), and it pre-dates this fire on the plain corpus.

## 15. Increment 2 fire brief (build note, 2026-08-03)

**Scope sentence (§10, verbatim).** *"`NarrowedFilterEligible` swaps `actorEnumerator != nil` for the
shared predicate; rewrite the `:550-561` comment (§4.6); e2e (c). Everything else is D1's shipped
machinery."*

### 15.1 Verified touch-list (checked live; §4.6's line numbers had drifted)

| File | Anchor (live) | What |
|---|---|---|
| `pipeline/pipeline.go:716-724` | `NarrowedFilterEligible` | actor-aware pipelines delegate to `ActorAwareNarrowingLabels()`; the plain branch is unchanged |
| `pipeline/pipeline.go:679-715` | its doc comment | states the two questions §4.6 separates — how far a *fan-out* reaches (adjacency, unbounded by labels) vs whether *any* actor can be affected (bounded by the label set once §4.2 holds) — plus the label-set-to-subject alignment that makes "the same data" exact |
| `pipeline/pipeline.go:726-763` | `ConsumerFilter`'s doc | the one *snapshot* of a per-event predicate, the activation-order requirement it rests on, and §15.6's recovery procedure stated where it is needed |
| `cmd/refractor/main.go:1006-1013` | the D1 comment above the activation `ConsumerFilter()` call | states the ordering requirement the snapshot rests on |
| `pipeline/narrowed_filter_internal_test.go` | `TestNarrowedFilterEligible_Table:34-88` + two new siblings at `:90-172` | the actor-aware case is re-stated as "the plain conditions alone are not sufficient"; `…_ActorAwareIsTheFanOutGate` asserts server-side eligibility **is** the client gate's verdict and that each conjunct falls all the way back to the broad filter; `…_EligibleActorAwareNarrowsToEveryForm` pins the three-forms-per-label expansion |
| `refractor/refractor_capability_relevance_gate_e2e_test.go:329-527` | new case + `waitGateConsumerSettled` helper | e2e (c) — real `capabilityRoles` through `InstallActorAggregate`, narrowed consumer, unrelated business write never delivered; a half-in-label link still is |

**Increment 2a's touch-list** (§15.7 — built, then REVERTED; the worktree carries Increment 2 only.
Kept here as the map for the next fire, which must also cover the delete path and the CAS loop):

| File | Anchor | What |
|---|---|---|
| `adapter/adapter.go` | new `ReconcileUpserter` beside `SeqGuarded`/`RowReader` | the optional interface a guarded adapter implements to accept a write at the stored row's own token |
| `adapter/natskv.go` | `guardedWrite` → `guardedWriteAt(…, admitEqualSeq)`; new `UpsertReconcile` | the tie rule made explicit and selected by caller; `>` still rejects under both |
| `pipeline/reproject.go` | the `canRead` block's write | a **present**, read-back-divergent row writes under the reconciliation rule |
| `pipeline/reproject.go` | `Reproject`'s doc | the tie now resolves toward the reconciliation, and why |
| `pipeline/sweep.go` | the `ErrNoOrderingToken` abandon branch | *"every Core KV event"* → every event the consumer's **own filter** admits |
| `pipeline/pipeline.go` | `lastAppliedSeq`'s field doc | a frozen value is not a wedge signal on a narrowed consumer |
| `adapter/natskv_internal_test.go` | `TestGuardedWrite_TieRuleDiffersByCaller` + `TestUpsertReconcile_UnguardedTargetWritesThrough` | 6-case table: equal-seq lands only under the reconcile rule, higher-seq rejects under both |
| `pipeline/reproject_token_test.go` | two new cases | a divergent row at a tied token asks for the reconcile rule; a converged one still writes nothing |

**One test the plan named and this fire did not write.** The plan carried a second e2e in
`pipeline/narrowed_filter_e2e_test.go` — a synthetic actor-aware pipeline proving `ConsumerFilter()`
narrows and a foreign type is not delivered. e2e (c) above proves the same property against the **real**
`capabilityRoles` spec installed through the real `InstallActorAggregate`, which is strictly stronger, so
the synthetic sibling would only re-assert a weaker form of a property already held. Dropped deliberately,
not overlooked. Recorded here because a touch-list that over-claims is worse than a short one.

Design citations re-verified live: `NarrowedFilterEligible` at `:679-703` (§4.6 says `:562-567`) ·
`ActorAwareNarrowingLabels` at `:586-627` · `CoreKVNarrowedFilters`' pairwise non-subset proof at
`subjects/subjects.go:151-187` · `registerWithFilterFallback`'s broad fallback and `Rebuild`'s
`ConsumerFilter()` recompute at `pipeline.go:1177-1193` · the single `RunOn` call site at
`cmd/refractor/main.go:1013-1023` · adjacency's own whole-stream consumer at
`consumer/bootstrap.go:23-29`.

### 15.2 Corpus effect — 20 lenses, not the two §4.6 names

§4.6 states the effect as *"`capabilityRoles` → 9 filter subjects; `unroutedTasks` → 6"*. The eligibility
predicate is the §14.7 census's, so what actually narrows **delivery** is that census's whole narrow-set:
4 auth-plane lenses + 16 business-convergence lenses across the clinic / café / wellness / lease
verticals (§14.7, count corrected there). Recorded rather than left implicit — it is why §9 requires the
full `-p 4` suite for this increment, and it is the population §15.7's defect rides on.

### 15.3 The soundness argument, restated for the server side

Increment 1's client gate already ack-and-skips **before** the fan-out — the aspect arm at
`pipeline.go:1327-1330`, the link arm at `:1348-1351`, the vertex arm at `:1399-1401`. The narrowed
filter set is built by `CoreKVNarrowedFilters` from *the same* label set those gates judge against, so
server-side filtering can only remove events the client already discarded. That is D1's own argument
(`NarrowedFilterEligible`'s doc: "strictly more conservative … computed from the exact same data those
gates already trust"), now resting on §4.2's conjunction instead of `actorEnumerator == nil`.

Two alignments worth stating because they are what makes "same data" true rather than nearly true:

- The vertex form `$KV.<bucket>.vtx.<label>.>` covers a label's **aspect** keys too (Contract #1's
  4-segment `vtx.<type>.<id>.<localName>`), which is exactly what the aspect arm's parent-type gate
  judges. No separate aspect form is needed or missing.
- The link forms are source-pinned **and** target-pinned per label, so a link is admitted when *either*
  endpoint type is in the set — the link arm skips only when **neither** is. Same predicate.

The enumerator is unaffected: it reads adjacency, and adjacency is written by its own dedicated
whole-stream consumer, not by this pipeline's deliveries.

### 15.4 The one in-scope gotcha — a per-event predicate, snapshotted once

`ActorAwareNarrowingLabels` is deliberately lazy (§14.2): activation installs its inputs in stages, so a
snapshot taken mid-installation would read a later stage's component as absent. `ConsumerFilter()` is the
one caller that *must* snapshot it — a JetStream consumer filter is set at registration. The snapshot is
sound only because the activation order is `UseFullEngineBranches` (`main.go:845`) →
`InstallActorAggregate` (`:920`, which sets pattern-closure, the enumerator and the sweep plan) →
`SetSecureDecryptor` (`:989`) → `ConsumerFilter()` (`:1013`) → `RunOn` (`:1014`).

Every way that order can be wrong fails **closed** — an input not yet installed reads as absent, a
conjunct fails, and the lens keeps the broad filter — with one exception that must stay stated: moving
`SetSecureDecryptor` *after* `ConsumerFilter()` would make a secure actorAggregate lens narrow without
the decryptor conjunct ever being evaluated. That combination is rejected at translate time today and
so is unreachable; the requirement is recorded at both code sites rather than left to be rediscovered.
Restructuring `RunOn` to derive its own filter would remove the ordering dependency structurally, but
that widens the ratified scope and is not taken here.

### 15.5 Increment order + green checks

1. `NarrowedFilterEligible` delegation + comment rewrites → `go build ./...`
2. Unit table updated (eligible actor-aware narrows; each conjunct forces broad) →
   `go test ./internal/refractor/pipeline/ -run 'NarrowedFilter|ConsumerFilter'`
3. e2e (c) + the pipeline-package narrowed-delivery case → `go test ./internal/refractor/...`
4. Gates: `make vet` · `golangci-lint run ./...` · `STRICT=1 go run ./scripts/lint-conventions.go` ·
   `make verify-kernel` · full `-p 4` suite (§9 requires it for Increment 2 — delivery changes for a
   class of lenses)

### 15.6 Rollback procedure (§8.3, asymmetric — not a code revert)

A JetStream filter update never rewinds the consumer cursor (nats-server v2.14.0), so reverting this
commit leaves a lens that already narrowed with the events it skipped still skipped. Recovery from a bad
narrow is `Pipeline.Rebuild` (consumer reset + re-projection) or the convergence sweep — which is why
`hasSweepPlan` is a conjunct, and why Increment 1 (symmetric revert) shipped and was measured first.

**Stated at the code site, not only here** (`ConsumerFilter`'s doc, `pipeline.go:753-763`). A rollback
procedure that lives only in a design doc is one an operator reaches for the code without: the site that
derives the filter is the site that has to say a revert does not undo it.

### 15.7 Increment 2a — the healer's ordering token: found, attempted, REVERTED, now a contract proposal

Two independent adversarial reviewers landed on the same defect, and it is one **this increment
activates**: narrowing starves the convergence sweep's ordering token, which is the healer §4.2 names as
the reason narrowing is safe at all.

**The mechanism, verified live.** `handleTracked` advances `lastAppliedSeq` on every ack including
ack-and-skip (`pipeline.go:1322-1324`), and `Reproject` uses that value as its guarded-write token
(`reproject.go:129`). The §6.2 guard drops a write whose stored watermark is `>=` the incoming one
(`adapter/natskv.go:293`) and returns `nil`, which `Reproject` books as `Wrote: true` and the sweep logs
as *"healed a divergent projection"*. Under the broad filter the equality is a millisecond window — any
write anywhere in the graph lifts the token. Under a narrowed consumer the token advances only on the
lens's own labels, so on a quiet auth plane it **rests on the sequence that wrote the newest row**, and
that row's divergence is unrepairable while every tick reports it repaired.

**It is reachable by an ordinary operation, not just by a fault.** `projectedFromRevisions` includes the
lens-definition key `vtx.meta.<lensID>` and reads its live revision per evaluation
(`projection/freshness.go:28-45`), `volatileEnvelopeFields` is `{"projectedAt"}` alone so that field
**is** compared (`reproject.go:62-69`), and `vtx.meta.*` is in no narrowed filter set. A lens-definition
write that leaves the MATCH unchanged reprojects nothing and rebuilds nothing (`lens/update.go:20-27`),
yet diverges every row.

**A fix was built, reviewed, and reverted — the review is why this is a proposal and not a commit.** The
attempt made the guard's tie rule caller-selected (`guardedWriteAt(..., admitEqualSeq)`, exposed as
`adapter.ReconcileUpserter`), keeping `stored > incoming` rejecting under both rules and moving only the
equal case, for a caller holding read-back evidence. A third adversarial pass over that delta returned
three findings that together make it unshippable, and all three were verified against the code:

1. **It is a FROZEN-CONTRACT change.** Contract #6 §6.2 states the rule as `≤`-rejects and names a
   reconciliation write *"a subordinate token … can never outrank real stream truth"*, closing with
   *"Any further non-CDC write class requires a contract change, not a new ad-hoc token."* Preparing that
   change is the correct move; committing it is not mine (CLAUDE.md).
2. **It fixed the wrong half.** The retraction direction still latched: a guarded `Delete` runs through
   the CDC rule, so a **revocation** at a tied watermark is still dropped while `Reprojection.Deleted`/
   `Wrote` report it healed. That is the over-grant direction — the one §6.2 exists for.
3. **It was unsound under concurrency.** The divergence evidence is read at `GetRow`, but the CAS loop
   re-reads and re-applies the relaxed rule without re-proving divergence, so two reconcilers holding the
   same token resolve last-writer-wins rather than by recency — and `seedAppliedSeqFromAckFloor` seeds
   every queue-group replica from the *same* ack floor, which manufactures exactly that tie.

**Resolution (Andrew, ratify session 2026-08-06): HELD — no contract change.** The corrected shape (tie
resolves toward a reconciliation only with read-back evidence, revision-conditioned CAS, divergence
re-proved on conflict, retraction covered, loud failure otherwise) was staged UNCOMMITTED and reviewed;
Andrew held it, and the staged edit is reverted. Grounds: (1) the only divergence-at-tie reachable by
ordinary operation is `projectedFromRevisions` provenance drift, which Contract #6 §6.3 itself classifies
as coherence/debug provenance — a comparator-sensitivity question, not a healing gap; (2) a **content**
divergence at a tied token requires a fault class with no observed producer; (3) committed §6.2 already
carries the sanctioned repair for exactly that state (the 12.1b rebuild interaction: truncate, or
guard-bypass replay). The sweep's contracted healing power stays **missing/stale only**. What ships
instead is code-side honesty: a tie-rejected reconciliation holding read-back divergence evidence must
**report failure loudly** — never the silent `nil` booked as `Wrote: true` — and the comparator must
classify provenance-only divergence distinctly from content divergence. That honest-verdict reshape is
`lens-projection-divergence-audit-design.md` Fire 1's `Verdict`/`UnverifiedReason` surface (📐 at the
time of this decision), which rewrites the same `Reproject` branches; the sweep-side supersession check
(`sweep-rule-snapshot-granularity-design.md`, 📐) rides the same seam.

**Revive shape, recorded for the day a real content divergence is observed at a resting token:** advance
the token instead of relaxing the rule — a reconciliation may stamp the consumer's **fully-drained head**
sequence (captured at `NumPending == 0` with no in-flight deliveries, before re-evaluation). Subordination
holds verbatim (an unreflected in-label event is `> H` by the drain proof), an out-of-label event cannot
move a pattern-closed lens's output (§4.2 is narrowing's own precondition), retraction passes for free at
`H > stored`, and two same-`H` reconcilers are serialized by the existing `≤` rule. A one-sentence
token-definition amendment, not a new write class — and it needs its own adversarial pass before building.

The `RowReader` asymmetry noted during 2a carries over to the revive shape: only the NATS-KV adapter
implements read-back, so a guarded **SQL** target (`actor_read_grants` included) stays on the CDC tie
rule — the revive design must decide that scope explicitly rather than inherit it.

**Build order for the next fire (rewritten after the 2026-08-06 hold).** 2a is dead — no tie-rule change
ships. The gate before merging Increment 2's narrowing is the honest sweep verdict: divergence-audit
Fire 1's `Verdict` reshape (or its minimal loud-failure form if that design is not yet ratified when the
fire runs) plus the sweep supersession check, one fire on the shared `reproject.go`/`sweep.go` seam. Then
merge the worktree's Increment 2 behind it; then Increment 3 as ratified.

### 15.8 Non-goals

Increment 3's pattern-directed derivation and its plain-arm shadow measurement; any change to the
enumerator, its caps, or its `reportsTo` hop; the ≤8-label cap; `verify-claim-ceremony.go`'s convergence
poll (its own row); the §14.8 `nodeMatches` body-`class` residual (its own row, ★★★); any contract edit.

### 15.9 CHECKPOINT (2026-08-03, resolved 2026-08-06) — Increment 2 complete and green; the §6.2 proposal is held

**Worktree:** `/Users/andrewsolgan/Documents/GitHub/lattice-wt-authlat-inc2`, branch
`fire/auth-latency-inc2`, based on `2a96cfcd`. Nothing merged to `main` from it.

**Done.** Increment 2's narrowing is built, reviewed by three adversarial passes, and green on the full
`-p 4` suite (115 packages, twice): `NarrowedFilterEligible` delegates to §4.2's conjunction; the
activation-order requirement and the asymmetric-rollback procedure are stated at both code sites; units
cover eligible-narrows plus each conjunct forcing broad; e2e (c) drives the real `capabilityRoles`
through `InstallActorAggregate` and asserts on the JetStream delivery count, with a half-in-label link
proving the either-endpoint alignment end to end. The negative was falsified deliberately — forcing the
broad filter fails it by exactly the count of the excluded writes.

**Held on — RESOLVED 2026-08-06.** Andrew held the §6.2 amendment: the tie rule is unchanged and the
staged edit is reverted (§15.7 resolution). The "narrowing starves the sweep" framing is corrected with
it: the sweep's contracted job — heal **missing/stale** — is untouched by narrowing; only
divergent-at-equal-seq goes unwritable, which has no observed content producer and keeps the 12.1b
rebuild as its repair. Increment 2 merges behind the honest-verdict fix, not behind a contract change.

**The board row returns to 🏗️** with this resume order (2026-08-06). The worktree stays; its base
`2a96cfcd` has skewed from `main` — re-derive premises against merged main at admit, per the standing
rule.

**Next fire — SUPERSEDED, all three steps resolved; see §15.10 and §15.11.** (1) The honest sweep verdict
shipped as `6f03b32b`. (2) Increment 2 merged behind it, re-derived against the skewed base, with the
relation-dimension fork resolved and two recovery-leg defects fixed. (3) **Increment 3 is what remains** —
pattern-directed derivation and its plain-arm shadow measurement.

The `lattice-wt-authlat-inc2` worktree is **retired**: its uncommitted Increment 2 was re-derived onto
merged `main` rather than merged as-is (its base had skewed 11 refractor commits, and one of them would have
failed its own acceptance (c)). Nothing in it is unlanded.

### 15.10 Build note — Increment 2 re-derived against merged `main` (the fire that lands it)

**Scope sentence (verbatim from §4.6, unchanged).** An eligible actor-aware pipeline's Core KV consumer is
narrowed server-side to the label set §4.2 already proves its fan-out arms may skip, so an unrelated
business write never costs the auth-plane lens a queue slot — Term A of the latency budget.

**Gate cleared.** §15.7's precondition — the honest sweep verdict + the supersession check on the shared
`reproject.go`/`sweep.go` seam — shipped as `6f03b32b`. Increment 2 merges behind it, as ratified.

**Re-derivation against merged `main` (the worktree's base `2a96cfcd` had skewed by 11 refractor commits;
the standing parallel-fire rule).** Two of those commits move the ground under the increment:

- `82c7972b` made the compiled rule a copy-on-write `ruleState` snapshot threaded per event. Every gate now
  takes `rs`, so the delegation must be `narrowedFilterEligible(rs) → actorAwareNarrowingLabels(rs)` — one
  snapshot, never a second `p.ruleState()` call inside the same decision. The public wrappers stay.
- `a322256b` added a SECOND narrowing dimension: `ConsumerFilter` emits the relation-narrowed subject set
  (`CoreKVLinkSourceRelationFilter`/`…Target…`, one pair per label × relation) whenever the compiled rule's
  relation set is exhaustive.

**The relation dimension does not carry to an actor-aware lens, and this is the increment's one new
decision (Winston, in-fire).** Relation narrowing is sound for the plain corpus because it has a
client-side counterpart: `plainLinkReactsTo` skips a link whose relation the patterns never traverse, so
the server withholding it is strictly more conservative than a gate that already ran. The actor-aware link
arm has no such gate — it judges by **endpoint type only** (`actorAwareFanOutRelevant(rs, t1, t2)`,
`pipeline.go:1638`), skipping only when NEITHER endpoint can bind. Applying relation narrowing there would
withhold events the client gate keeps: a **second, independently-fallible judgment**, which is exactly the
shape §4.6's invariant forbids. `capabilityRoles` derives an exhaustive relation set, so this is reachable,
not theoretical — and Increment 2's own acceptance (c) already encodes it: the half-in-label
`identity -bookedBy-> booking` link it requires delivered is on a relation the lens never traverses, so an
unguarded merge would have failed that assertion. **Resolution: an actor-aware pipeline narrows by LABEL
only** — the relation branch is gated on `actorEnumerator == nil` and says why at the site.

**Touch-list (checked live against `main`).** `pipeline.go:906` `narrowedFilterEligible` (delegate) ·
`pipeline.go:947` `ConsumerFilter` (relation branch gated + the alignment argument) · `cmd/refractor/main.go`
(the ordering requirement: `ConsumerFilter` must follow every conjunct-installing stage, since a consumer
filter snapshots a per-event predicate at registration) · `narrowed_filter_internal_test.go` (the
`never eligible` case becomes `plain conditions alone are not sufficient`; the fan-out-gate identity and the
three-forms expansion) · `auth_plane_narrowing_census_test.go` (the real shipped cypher's FILTER verdict,
not just its label verdict) · `refractor_capability_relevance_gate_e2e_test.go` (acceptance (c): the
consumer's own `Delivered.Consumer` tally at a settled fence).

**Non-goals** — §15.8 verbatim, plus: extending relation narrowing to the actor-aware arm (that needs a
client-side relation gate for the fan-out and its own soundness pass — filed as its own row, consumer named).

### 15.11 Increment 2 review outcome — and the recovery leg it exposed

**Three adversarial passes, scaled to the auth plane.** Delivery-side soundness · the relation-dimension
fork · the test-proof audit. The core claim was **not refuted**: for the three CDC key shapes this consumer
carries, server-side admission and the client-side §4.2 gate are **set-equal**, not merely one-sided —
walked arm by arm against Contract #1 §1.5, including the aspect form (a label's vertex form covers its
4-segment aspect keys), both link positions (which is what makes the disjunctive endpoint gate exact), meta
vertices, tombstones, and `ReferencedLabels`' exhaustiveness over every `Clause` type. Adjacency, lens-spec
reloads, sweep ticks and decryptor key custody all ride their own consumers or point reads, so none is
starved by a narrower filter. Restart-with-widened-labels is equivalent to the broad regime, not worse: the
pre-existing client gate ack-skipped the same messages and advanced the same cursor.

**The relation fork was confirmed correct and load-bearing**, with a sharper reason than the one first
written: it is not that pattern-closure fails to cover an untraversed relation — it would — but that
narrowing the relation dimension here would silently extend *another* ratified design's blast radius to a
corpus its own review never covered, and "match the client gate exactly" is the only invariant that
survives a later edit to `actorAwareFanOutRelevant`, since a registered filter can never be widened back.
That design's §3.5 premise ("`NarrowedFilterEligible` already refuses them") is falsified by this increment
and has been corrected in place. Residual cost, stated plainly: a link joining an in-label endpoint over an
untraversed relation still costs a queue slot, which on the auth plane is the dominant remainder of Term A —
its own row.

**Two defects on the RECOVERY leg, fixed here because this increment is what makes them load-bearing.**
§4.2 will only narrow a lens that has a standing healer (the sweeper conjunct), and §8.3 is why: no revert
widens a registered filter, so `Rebuild` or the sweep are the only two recoveries from a wrong narrow.

1. **A failed `Rebuild` switched off both of them at once.** `SetRebuilding` is written before the work, but
   the only writer of rebuilding → active is `watchRebuildCompletion`, launched on the success path alone.
   Every error return cleared `rebuildInFlight` and left the status latched, and `Sweeper.suppressed`
   refuses any tick whose status is not `active` — for the life of the process, since
   `resumeInterruptedRebuild` runs only at start. The heartbeat could not escalate it either
   (`evalRebuildWedged` returns false on a zero `RebuildProgressAt`, exactly what a failed rebuild leaves).
   Reachable from one transient NATS RTT during `Reset`. Now every error return exits through
   `abandonRebuild`: flag cleared, status restored, cause recorded. `active` + a live `LastError` is the
   honest pair — the rebuild did not run, so the lens is still consuming under the filter and cursor it
   already had; status carries liveness, `LastError` carries the verdict. The write order is load-bearing:
   `SetActive` nils `LastError` by design, so the cause is recorded *after* the status, which is what the
   new test caught.
2. **A MATCH hot-reload's filter update failed silently.** `cmd/refractor/reload.go` logged the async
   rebuild's error instead of recording it, and that rebuild is the only thing that re-derives the consumer
   filter after `UseFullEngineBranches` publishes a new label set. A failure after a **widening** edit
   leaves the consumer narrowed to the old set permanently — every write on a newly-referenced type denied
   while the swapped-in client gate would have kept it, in both the grant and the retraction direction. Now
   routed through `rl.refuse`, so it reaches the lens's health entry like every other refusal on that path.

**Deferred with consumers named** (filed as rows, same commit as the ✅ flip): actor-aware relation
narrowing · the sweep-heals-under-narrowing e2e · an actor-aware `Rebuild` widening test · the
aspect-subsumption end-to-end assertion · the secure ∧ actorAggregate exclusion as an explicit
translate-time test · the activation-order fail-closed reshape (verified correct today, latent for a future
edit; both comments now state that early-call costs correctness rather than narrowing).

## 16. Increment 3 fire brief (build note, 2026-08-07) — the derivation, landed in shadow

**Scope sentence (verbatim from §10, Increment 3).** New unit under `pipeline/` deriving the
affected-anchor set from the compiled pattern + the changed element (3a edge-seeded, 3b node-seeded), hop
index over **every** pattern source with a `complete` flag, superset invariant + fallback to the shipped
BFS; wire the three actor-aware arms; plus the sampled plain-arm shadow measurement; differential test;
e2e (a) tightened plus (d).

**Build order inside the increment (Winston, in-fire) — shadow first, flip second.** This fire lands the
derivation and runs it in **shadow on the actor-aware arms**: derive, compare against the
`ActorEnumerator` BFS's answer, count agreement / superset / **shortfall** per lens — and act on the BFS's
answer, unchanged. The flip to acting is the next fire, and the plain-arm shadow (§4.7's own consumer,
§D2 Phase 2) rides the same unit after it.

The reason is the invariant's direction. Under-approximation on the auth plane is a missed revocation, and
§4.7 asks for a differential test over generated mutations as its proof. A synthetic differential proves
the derivation against the corpus the test author thought of; the shadow proves it against the graph that
actually exists, on every live event, before anything depends on it. §4.7 already ratifies exactly this
shape ("derive, compare, count, act on neither") for the plain arm — this applies it to the arm whose
correctness cost is higher, first. Nothing is narrowed: the same increment lands, with the flip carrying
measured shortfall evidence instead of only a generated one.

### 16.1 Verified touch-list (checked live against `main`)

- **NEW `ruleengine/full/hopindex.go`** — `(cr *CompiledRule) AnchorHopIndex()`. Mirrors
  `ReferencedRelations`' walk (`relations.go:38-119`) clause-for-clause and expression-for-expression, so a
  pattern position one derivation reads and the other does not cannot exist. Anchor identification reuses
  `pathPatternReferencesActorKey` / `exprReferencesActorKey` (`ast.go:444-480`).
- **NEW `pipeline/anchor_derivation.go`** — the data walk over `adjacency.Neighbors`
  (`adjacency/store.go:19`), edge-seeded and node-seeded, returning `(anchorKeys, ok)`.
- `pipeline/evaluate.go:733` (vertex fan-out) · `:793` (link fan-out, both endpoints) · `:832` (aspect
  fan-out) — the three shadow call sites, each immediately after the `Enumerate` whose answer still wins.
- `pipeline/pipeline.go` — the sampling counter + the per-lens shadow tally and its accessor.

### 16.2 The completeness predicate — what makes an index refuse to answer

`complete` is a conjunction, and every conjunct is fail-closed. An incomplete index never skips and never
derives; the caller falls back to the shipped BFS.

1. **No `With` clause anywhere.** A `WITH` that drops a variable re-seeds an unlabeled downstream node
   through the whole-bucket scan (`labels.go:16-24`'s scope argument), so that position reaches the anchor
   through *no link at all* — an adjacency walk cannot see the dependency and would under-approximate.
2. **Every relationship carries a `Type`.** An untyped hop matches any relation, so it cannot be indexed by
   relation name — the same arm `ReferencedRelations` fails exhaustiveness on (`relations.go:62-65`).
3. **No variable-length hop** (`MinHops != 1 || MaxHops != 1`) anywhere in the graph. The intermediate
   nodes are the problem, not the relation: a back-chain crossing one cannot be walked hop-by-hop.
4. **The anchor position is identified** — exactly one position carrying literally `{key: $actorKey}`. The
   property name and the whole expression are both matched exactly: `key` is the only property the executor
   point-reads on, so any other makes the anchor a label-prefix SCAN binding many vertices, and `$actorKey`
   embedded in a larger expression pins a different vertex entirely (§16.6.2).
5. **Every pattern is GROUNDED at the anchor** — its head node is a position already reached from the
   anchor by an earlier clause. `matchPath` seeds `Nodes[0]` alone and reaches the rest by traversal, so an
   ungrounded head is a bucket scan: every vertex of that type binds, and every anchor's row depends on all
   of them. That is precisely the case a "derived empty ⇒ skip" answer would get catastrophically wrong.

Conjunct 5 is the one that is easy to miss and the only one that is unsound to omit — the other four
degrade to a wider set, this one degrades to a *smaller* one. It is stated as **grounding rather than
connectivity** because the review falsified the weaker form: a scan-seeded position with one optional or
negated hop back to the anchor is *reachable* in the hop graph while still binding by scan, so reachability
would have passed exactly the shape the conjunct exists to refuse (§16.6.3).

### 16.3 Pattern-graph model

Positions are equivalence classes of pattern node positions, merged by variable name when non-empty (an
unnamed position is its own class). Within one `PathPattern`, `Rels[i]` joins `Nodes[i]` and `Nodes[i+1]`
by index (`executor.go:515-523`), independent of variable names; merging by name is what joins the
separate `OPTIONAL MATCH` clauses of `capabilityEphemeral` into one graph. Merging can only *add* edges,
and an added edge only widens the derived set, so the merge is safe in the invariant's direction.

Back-walking translates one pattern hop into one relation-filtered, direction-flipped `adjacency.Neighbors`
read: a pattern `(a)-[:r]->(b)` walked from `b` toward `a` reads `b`'s edges for `Name == "r" &&
Direction == "inbound"`. `DirBoth` accepts either. Several hops carrying one relation yield several
back-walks, **unioned** (§4.7).

### 16.4 Worked expectations (the acceptance the shadow must reproduce)

- `capabilityRoles` / `holdsRole` link event — the anchor-side endpoint *is* the anchor; derived =
  {that identity}, against a BFS that returns every co-holder reachable through the role.
- `capabilityRoles` / `grantedBy` link event — back-walk gives `role ←[holdsRole]— identity` = every
  holder, which is correct and necessary, so shadow agreement here is the *expected* answer, not a miss.
- `capabilityEphemeral` / an event on `vtx.booking.<id>` — binds the unlabeled `tgt`/`tgt2`/`tgt3`
  positions; three back-chains, each pruning immediately where the typed edge is absent.
- `capabilityServiceAccess` — `containedIn*0..` fails conjunct 3, so the index is incomplete and this lens
  falls back on every event. Expected and recorded, not a defect.

**Correction, from the review's corpus census (this fire).** The paragraph above predicted *one* fallback
lens. The real number is **~16 of the 31 actorAggregate lenses, plus the whole generated `cap-read.<domain>`
producer family** — and almost all of it is conjunct 1 (any `WITH`), not conjunct 3. Fourteen of those lenses
carry a `WITH` with **no `MATCH` after it**, so the downstream bucket re-seed the conjunct exists to refuse is
structurally impossible in them; they are refused by a blanket test rather than by the hazard. The refusal
stays blanket in this fire — it is fail-closed and correct, and narrowing it needs its own soundness pass over
the WITH's own `WHERE` and the `RETURN`'s pattern comprehensions, which can re-seed just as a later `MATCH`
can. Filed as its own row with the consumer named.

### 16.5 Increment order + green checks

1. `hopindex.go` + units against the three shipped cyphers' real text — `go test ./internal/refractor/ruleengine/full/`.
2. `anchor_derivation.go` + units over a seeded adjacency fixture — `go test ./internal/refractor/pipeline/`.
3. Shadow wiring + counters — `go test ./internal/refractor/...`.
4. Full `-p 4` suite (§9: Increment 3 changes evaluation for a class of lenses).

### 16.6 Adversarial review outcome — three findings on the invariant itself

Three passes, scaled to the auth plane: superset soundness · the completeness predicate + AST walk · wiring,
cost and test-proof. Two ran independently on the soundness question and **converged on the same three
defects**, each of which could return a set SMALLER than the truth. All three are fixed here; none was
reachable from the shipped corpus, and that is the point — the derivation's contract has to hold for the next
lens someone authors, not only for the fourteen that exist.

1. **A later label was adopted as the position's label.** The premise ("one variable, one node, so a label
   written anywhere constrains every occurrence") is false for the clause every actorAggregate lens is built
   from: when an `OPTIONAL MATCH` re-references a bound variable with a label that fails, the executor
   restores the row with the **original binding intact** (`nullBindNewVars` nulls only variables not already
   bound). So the variable really can hold another type, and adopting the later label narrows
   `PositionsBinding` below what the executor binds — an event on the other type would derive nothing at all.
   **Fixed:** a position's label is fixed at its FIRST occurrence, which is what binds it; a later label is a
   filter and is ignored. The same false premise underpins `ReferencedLabels`' `labeledVars` pass — noted, not
   changed here, and filed.

2. **The anchor was detected on any property carrying `$actorKey`.** Both load-bearing steps of the walk —
   never expanding from the anchor position, and minting `vtx.<type>.<id>` for what it reaches — assume the
   position is pinned to exactly one vertex. Only `{key: $actorKey}` does that: `key` is the one property the
   executor point-reads on, and any other makes the anchor a label-prefix scan binding many vertices. A
   `{managedBy: $actorKey}` lens would have reported the wrong actor *and* stopped the walk that would have
   found the right ones. `$actorKey` merely *embedded* in the expression (`$actorKey + '-shadow'`) pins a
   different vertex again. **Fixed:** the property name and the whole expression are both matched exactly.

3. **The connectivity conjunct counted hops that do not bind.** §16.2's fifth conjunct is meant to prove a
   position is not a bucket-scan seed, and undirected reachability does not prove it: `MATCH (i {key:
   $actorKey}) MATCH (u:unit)` is correctly refused, but adding **one** `OPTIONAL MATCH (i)-[:owns]->(u)` —
   a hop that filters nothing, since the optional cannot drop `u` — made the scan-seeded position look
   connected. Every anchor's row depends on every unit, and the derivation would have returned the empty set
   and licensed the skip. Adding a hop made it unsound, which is the inversion worth remembering.
   **Fixed:** the conjunct is now GROUNDING, not reachability — mirroring what `pkgmgr/anchorwalk.go` already
   requires of generated lenses. A pattern extends the grounded set only when its HEAD is already grounded,
   because `matchPath` seeds `Nodes[0]` alone. The same distinction fixes a second defect the reviewers found
   independently: `Dist` now counts binding hops only, so a `WHERE NOT` shortcut can no longer make a far
   position look near and seed a link tombstone at the endpoint that could only reach the anchor by crossing
   the edge just removed.

**Also folded:** a read-cap that no test drove and no operator could tune (now `SetAnchorDerivationReadCap`,
with a test proving a refusal rather than a truncated set); `SetAnchorDerivationSampling(0)` documented as
disabling while it restored the default; a self-loop hop read in one direction only; the `anchorHops`
reload lifetime, now tested the way `seedAnchorLabel`'s already was. And one the *stack* surfaced rather
than the review: cycling the rebuilt refractor showed the per-event line is DEBUG, which no deployed
component prints, and the counters sit behind an accessor nothing calls — so the tally now reaches the log
at INFO every fiftieth sampled event, per lens. A measurement nobody can read is not a measurement.

**Accepted without a change:** the label comparison matches key types while the executor's `nodeMatches` also
binds on body `class` — the already-ratified lens-label/key-type binding item, now inherited by a third
derivation and recorded at the site so one fix closes all three.

**Stated rather than fixed — what the shadow cannot tell you.** The shadow measures the narrowing and flags
divergence from the enumerator, and that is all it can do: a genuine shortfall and the intended win are both
"the derived set is smaller". Neither set is ground truth. §9's differential test over pre/post recomputes is
what licenses the flip, and §16's framing is corrected accordingly — the shadow proves the derivation behaves
on the real graph and by how much it would narrow, not that it is a superset.

### 16.7 Non-goals

Flipping any arm to act on the derived set (next fire, with the differential test + e2e (a)/(d) as its
evidence) · the plain-arm shadow and §D2 Phase 2's wiring · any change to the enumerator's caps or its
`reportsTo` hop · any Contract amendment.

## 17. Increment 3b fire brief (build note, 2026-08-07) — the flip

**Scope sentence (verbatim from §16.7's named next fire).** *Flipping any arm to act on the derived set
(next fire, with the differential test + e2e (a)/(d) as its evidence).* Read with §10's Increment 3 row,
whose remaining clauses are exactly these: "wire the three actor-aware arms; … differential test; e2e (a)
tightened to co-holder-revision-unchanged, plus (d)."

**Scope-diff gate, item by item.** Three arms wired to act — in scope, the whole point. Differential test —
in scope, and §8.3 names it as the mitigation for the one risk this fire creates. e2e (a) tightened + (d) —
in scope, named. Two things this brief ADDS to the ratified scope, both narrowings rather than
substitutions, declared here rather than discovered in the diff:

1. **Two further conjuncts before an arm may ACT** (§17.2). Neither widens anything; both refuse to act on
   a lens where acting would be unsound, and both are conjuncts §4.2 already ratified for the sibling
   narrowing. Omitting them would have shipped the flip onto every personal lens.
2. **An operator mode knob** (§17.3), mirroring `REFRACTOR_MAX_BINDINGS`' shape. It exists because the
   rollback is **not** a code revert (§17.5) and an operator needs a way to stop the bleeding at 3am.

Nothing is substituted for an adjacent mechanism, and no declared dependency moved: the derivation, the hop
index, and the shadow all shipped in `f484330d` and are re-read here, not re-derived.

### 17.1 Verified touch-list (checked live against `main`)

- `pipeline/evaluate.go:733-748` (vertex fan-out) · `:789-821` (link fan-out) · `:841-855` (aspect fan-out)
  — each currently runs `Enumerate` then `shadowAnchorDerivation`; each becomes one call to the shared
  chooser, which decides which of the two answers the event acts on.
- **NEW `pipeline/anchor_derivation_mode.go`** — the mode (`off` / `shadow` / `act`), its per-pipeline
  override, its package-level default, and the chooser.
- `pipeline/anchor_derivation.go:117-128` (`derivationIndex`) — gains the two act-only conjuncts of §17.2.
  They are act-only deliberately: shadow mode must keep observing the lenses acting would refuse, since
  "how often would we have been wrong here" is exactly what the observation is for.
- `pipeline/anchor_derivation_shadow.go:52-71` — the stats struct gains the act-side counters
  (`Acted` / `ActedAnchors` / `FellBack`), and `logSummaryIfDue` prints them. In act mode the arm runs on
  **every** event, not one in eight, so the summary interval is against events, not samples.
- `pipeline/pipeline.go:837` (`p.sweeper == nil`) and `:897` (`patternClosedOutput`) — read, not changed;
  they are where §17.2's two conjuncts get their answer.
- `cmd/refractor/main.go:776-783` — the `REFRACTOR_MAX_BINDINGS` read whose shape §17.3 mirrors.

### 17.2 Two conjuncts an arm must clear before it ACTS

`derivationIndex`'s three shipped conjuncts (an enumerator is installed · the hop index is `Complete` ·
the anchor position's label is the enumerator's actor type) all ask whether the derivation *can answer*.
Acting asks a second question — whether a smaller answer is *safe here* — and two of §4.2's already-ratified
conjuncts are the answer to it. Both are fail-closed and both refuse rather than widen:

1. **`patternClosedOutput`.** The derivation reasons entirely over the compiled pattern, so a row that
   depends on an input the pattern does not bind can change with no pattern edge changing — and a derived
   set that legitimately excludes that anchor would skip a real change. §4.4 names the class exactly:
   **every personal lens**, with two out-of-pattern inputs (the D1 read gate and the Interest Set), and
   `projection/personal.go:130` installs an `ActorEnumerator` on each — so these arms are live for personal
   lenses today and without this conjunct the flip would have reached all of them. This is the conjunct
   whose omission would have been a silent defect rather than a pessimisation.
2. **A sweep plan is installed (`p.sweeper != nil`).** §8.3's mitigation for the under-approximation risk is
   "the sweep is a conjunct, so a narrowed lens always has a healer" — and this fire is the first time a
   *derived* set decides a reprojection rather than merely being counted next to one. A lens with no
   standing healer must not also lose the incidental recompute.

What is deliberately **not** required: the rest of §4.2 — an exhaustive label set, the anchor type in the
label set, the decryptor's identity type. Those bound whether an event may be **withheld from the lens**,
which is a different question from which anchors an event that *did* arrive can affect. Requiring them
would exclude `capabilityEphemeral`, and §4.7's 3b names reaching exactly the non-exhaustive lenses
Increments 1–2 cannot as the increment's purpose. Conflating the two gates would have quietly narrowed the
increment to the corpus that already had a narrowing.

### 17.3 The mode, and why it is an operator knob

`REFRACTOR_ANCHOR_DERIVATION` = `act` (default) · `shadow` · `off`, read once in `cmd/refractor/main.go`
and applied as the package default, with a per-pipeline override for tests. The shape mirrors
`REFRACTOR_MAX_BINDINGS` (`main.go:776-783`): read, parse, validate, log what took effect, keep the default
on a bad value.

Three modes rather than a boolean because the third one is not "off" — **`shadow` is the mode this fire
inherits and §D2 Phase 2 will want**, and deleting it to make room for a boolean would throw away the
plain arm's vehicle. The shadow's comparison counters keep their meaning; they simply stop being fed on an
arm that acts, because acting means the BFS is never run and there is nothing to compare against. That is
not an instrumentation regression — running both would spend exactly the cost the increment exists to
remove. What replaces it in act mode is a different and more directly useful measurement: how often the
derivation answered versus fell back, and how many anchors it reprojected, per lens, at INFO.

### 17.4 Increment order + green checks

1. Mode + chooser + the two act conjuncts, arms still on `shadow` — `go test ./internal/refractor/pipeline/`.
2. Flip the default to `act`; the three arms wired — same package green, plus e2e (a)/(d) below.
3. Differential test (§9's superset proof) — `go test ./internal/refractor/pipeline/ -run Differential`.
4. `cmd/refractor` env read — `go build ./...`.
5. Full `-p 4` suite (§9: Increment 3 changes evaluation for a class of lenses).

### 17.5 Rollback is asymmetric — the knob stops the bleeding, the sweep heals

Stated plainly because the natural assumption is wrong and §8.3 already had to state it once for
Increment 2. This flip is pure client-side computation with no consumer-filter change, so setting
`REFRACTOR_ANCHOR_DERIVATION=off` restores today's behaviour on the very next event — but it does **not**
heal a row a shortfall already left stale, because the events that would have reprojected it are gone.
Recovery from a bad narrow is `Rebuild` or the convergence sweep, exactly as for Increment 2. The knob's
value is that it bounds the damage to the window before it is turned, without a redeploy; §17.2's second
conjunct is what guarantees the healer exists to finish the job.

### 17.6 Non-goals

The plain-arm shadow and §D2 Phase 2's wiring (its own filed row, sequenced behind this one) · narrowing
the blanket `WITH` refusal (its own filed row, §16.4) · any change to the enumerator's caps or its
`reportsTo` hop · surfacing the act-side tally to Health KV (the log is the reader this fire owes; the
Health surface is the divergence-audit row's shape, not this one's) · any Contract amendment.
