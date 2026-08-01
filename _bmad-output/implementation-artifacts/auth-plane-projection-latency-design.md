# Bounding auth-plane projection latency — relevance-gate and pattern-direct the actor-aware fan-out

**Status: 📐 awaiting-Andrew (ratification)** — Designer fire, Winston, 2026-08-01.
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

**No architectural fork forced, but one product question is yours** (§8.1): should the platform offer a
*read-your-own-grant* guarantee (the Gateway blocking on grant convergence after `ClaimIdentity`), or
stay eventually-consistent with the client-side retry Facet already ships? **My recommendation: stay
eventually-consistent.** A convergence wait puts a lens round-trip on the authentication hot path and
converts a latency problem into an availability one; the fires below remove the two *unbounded* terms,
which is the actual defect. Options + trade-offs in §8.1.

**No frozen-contract change.** Contract #6 states no projection-latency promise and none of its
semantics move (§7). Nothing is staged uncommitted.

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
  rides hot-reload → `Rebuild` → consumer reset; and a `vtx.meta.*` event reaches no actor through the
  BFS today either, so narrowing changes nothing about it.

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

**Relationship to §D2 Phase 2.** `refractor-footprint-reduction-design.md` §D2 Phase 2 describes this
same reverse-chain derivation for the **plain**-lens corpus, and its named trigger is currently
**unmeasured** (the board row records that the §7 crawl attributed to it was an actorAggregate AckWait
live-lock, since fixed). This design therefore does **not** absorb Phase 2 and does not build the plain
arm: it builds the derivation as a standalone unit under `pipeline/` (usable by both), wires only the
actor-aware arms — the corpus with **measured** demand — and leaves Phase 2 as "wire the same primitive
into the plain arm" if and when its trigger is measured. One primitive, one consumer today, no dead
scaffolding.

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
| **3 — Pattern-directed affected-anchor derivation** | New unit under `pipeline/` deriving the affected-anchor set from the compiled pattern + the changed element (3a edge-seeded, 3b node-seeded), hop index over **every** pattern source with a `complete` flag, superset invariant + fallback to the shipped BFS; wire the three actor-aware arms; differential test; e2e (a) tightened to co-holder-revision-unchanged, plus (d) | M–L | Full `-p 4` suite. Built as a standalone unit so §D2 Phase 2 can wire the plain arm later without a second derivation |

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
