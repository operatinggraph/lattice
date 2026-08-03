# A lens subscribes to every link touching a referenced type, not to the relations it traverses

**Status:** ✅ SHIPPED 2026-08-03 (`a322256b` + `4336978b`). No fork, no contract change.
Verified live: the `clinicProviders` consumer's filter subjects now pin `identifiedBy` (§6).
**Board row:** `[Refractor] A lens consumer's ack floor freezes, so its rebuild can never drain`
(`backlog/lattice.md`) — this is the answer to that row's open question, and its fix.
**Owning code:** `internal/refractor/{subjects,pipeline,ruleengine/full}`.
**Predecessor:** [`lens-consumer-ack-window-design.md`](lens-consumer-ack-window-design.md) — Inc 1
(pump prefetch) + Inc 2 (frozen-floor health signal) shipped there; its §5b left "why the 678 went
un-acked" open. §1 below closes it.

## 1. What the 674 messages actually are

Measured live 2026-08-03 on the wedged consumer `refractor-ynnaZCqFhB3t22dLynna`
(lens `clinicProviders`), `num_pending: 0`, `num_ack_pending: 674`, floor pinned at consumer
sequence 3,934 for hours.

Every entity the pump logged is the same shape:

```
pipeline: processed  ruleId=ynnaZCqFhB3t22dLynna
  entityId=lnk.service.<nanoid>.providedTo.identity.edu97ixj2CJB6auNi6L4
```

`clinicProviders` is a plain (non-actor-aware) full-engine lens over **7 provider vertices**:

```cypher
MATCH (pr:provider)
WHERE pr.profile.data.fullName <> null
OPTIONAL MATCH (pr)-[:identifiedBy]->(id:identity)
RETURN pr.key AS key, ..., id.key AS identityKey
```

The only link relation it can traverse is `identifiedBy`. Yet its consumer's filter subjects are

```
$KV.core-kv.vtx.identity.>        $KV.core-kv.vtx.provider.>
$KV.core-kv.lnk.identity.>        $KV.core-kv.lnk.provider.>
$KV.core-kv.lnk.*.*.*.identity.>  $KV.core-kv.lnk.*.*.*.provider.>
```

— `CoreKVNarrowedFilters` emits three forms per *referenced label*, and the link forms wildcard the
relation segment. So the lens is subscribed to **every link in the graph whose source or target is any
identity**, whatever the relation.

Live Core KV holds **1,325 `lnk.service.<id>.providedTo.identity.<id>` links**, 796 of them on the
single identity `edu97ixj2CJB6auNi6L4`. None of them can change a `clinicProviders` row — the relation
is `providedTo`, the lens traverses `identifiedBy` — and each one is nonetheless delivered, and each one
drives `evalPlainLinkReprojection` to re-execute the lens from both endpoints.

**So the 678 never "went un-acked" through any pump defect.** They are a backlog of events the lens had
no business receiving: the trigger derivation admits a link on the strength of its *endpoint type* alone
and discards the relation, which is the one segment that decides relevance. The ack-window design's Inc 1
and Inc 2 are both real and both stand; neither could have drained this, because the work was never the
pump's to shed.

**What is still open, and is now unblocked from this consumer:** the delivery *cadence*. With one pull
request parked (`num_waiting: 1`) and 647 long-expired pending messages, the server delivered nothing for
five-minute stretches and then released a burst — i.e. the redelivery queue is empty between AckWait
ticks rather than holding the whole expired set, which nats-server v2.14.0's `checkPending`
(`server/consumer.go:5970-5983`, which adds *all* expired seqs to `o.rdq` in one pass) does not on its own
explain. It is not this lens's problem after this fix — the backlog stops existing — so it is recorded
here and left to whichever consumer next exhibits it.

## 2. The mechanism

`subjects.CoreKVNarrowedFilters(bucket, labels)` (`subjects.go:170`) expands each referenced label `L` to

- `$KV.<b>.vtx.<L>.>` — vertex root + aspects,
- `$KV.<b>.lnk.<L>.>` — **every** link whose source type is `L`,
- `$KV.<b>.lnk.*.*.*.<L>.>` — **every** link whose target type is `L`.

Contract #1's link key is `lnk.<typeA>.<idA>.<relation>.<typeB>.<idB>`. Both link forms wildcard
`<relation>`, so the filter's selectivity is exactly "one endpoint has this type" — for a hub type like
`identity`, that is close to the whole link keyspace.

The client-side gate has the same blind spot. `evalPlainLinkReprojection` (`pipeline.go:1548`) parses the
key and asks `plainReactsTo(type1) || plainReactsTo(type2)`; `plainReactsTo` consults
`plainReprojectLabels`, a set of *vertex types*. There is no relation dimension anywhere, on either side.

The cost is quadratic in the wrong place: N irrelevant links on a hub vertex produce N full lens
re-executions, and a consumer that accrues them faster than it drains them never catches up. That is what
froze the ack floor.

## 3. The fix — carry the relation set alongside the label set

The label narrowing is already a well-guarded, exhaustiveness-gated derivation
(`full.CompiledRule.ReferencedLabels`, `plainReprojectAll`, `NarrowedFilterEligible`). The relation
dimension is the same derivation on `PathPattern.Rels` and rides the same gates.

**3.1 `full.CompiledRule.ReferencedRelations() (map[string]struct{}, bool)`** — mirrors
`ReferencedLabels`, walking the identical clause/expression tree (`Match` patterns and `WHERE`,
`PatternExpr`, `PatternComprehension`, `Return` items, each `With`'s items and `WHERE`, **and both
patterns' property maps** — see §4.1) and collecting `RelPattern.Type`. `exhaustive = false` when any
traversed relationship is untyped (`Type == ""`). An **empty** set with `exhaustive == true` is
meaningful and is the common case: a lens with no relationship pattern at all (`clinicPatients`,
`clinicSites`) can be affected by **no link whatsoever**.

A **variable-length hop costs nothing here**, which is where this parts company with
`ReferencedLabels`. That derivation must give up on `*1..3` because the intermediate *nodes* bind
arbitrary types; the relation is different, because `traverseRel` re-applies the same `rel.Type` at
every hop of the walk (`executor.go`'s `rel.Type != "" && e.Name != rel.Type`, inside the hop loop). So
`-[:containedIn*0..7]->` traverses `containedIn` and nothing else. This is not a detail: `containedIn`
is the only relation the corpus ever walks variable-length, and the 16 lenses that do it are the
location-hierarchy walks most likely to sit on a hub — copying the label rule here would have forfeited
most of the fire's benefit on exactly them.

**3.2 Pipeline state.** `plainReprojectRelations` / `plainReprojectAllRelations`, derived in
`useFullEngineBranches` beside the label set, unioned across branches, non-exhaustive if any branch is —
byte-for-byte the label set's own treatment, so a MATCH hot-reload re-derives both together and neither
can go stale against the other.

**3.3 Server-side.** `CoreKVNarrowedFilters(bucket, labels, relations)` gains a relation argument. When
relations is non-nil it emits, per label `L` and relation `r`:

- `$KV.<b>.vtx.<L>.>`
- `$KV.<b>.lnk.<L>.*.<r>.>` — source type `L`, relation `r`
- `$KV.<b>.lnk.*.*.<r>.<L>.>` — relation `r`, target type `L`

and, when the relation set is empty, the vertex form alone. `nil` relations keeps today's relation-blind
forms verbatim, so a lens whose relation set is not exhaustive is unchanged.

The pairwise-non-subset property `subjects_test.go` already proves must survive: it does, and for the
same reason as before — any two of the new forms differ at some token position neither side wildcards
(`vtx`/`lnk` at segment 3; the label at segment 4 or 6; the relation at segment 6; and the source form's
`>` at segment 7 lands where the target form has a literal label, which `isSubsetMatch` refuses in both
directions). The extended pairwise test is the proof, not this paragraph.

**3.4 Budget.** Filter count is `|L| x (1 + 2|R|)`, so the existing `maxNarrowedFilterLabels = 8` cap no
longer bounds it. `ConsumerFilter` degrades in two steps instead of one: relation-narrowed if the total
subject count fits `maxNarrowedFilterSubjects` (24 — exactly today's `8 x 3` ceiling, so no lens that
narrows today can stop narrowing because of this change), else label-only narrowed under the existing
label cap, else the broad `$KV.<b>.>`.

**3.5 Client-side.** `plainLinkReactsTo(relation)` gates `evalPlainLinkReprojection` in conjunction with
the existing endpoint-type check. Redundant for a lens that also narrows server-side, load-bearing for one
that does not (over the subject budget, or a label set that is exhaustive while some *other* branch's
relation set is not). Defaults to relevant on every uncertain input, exactly as `plainReactsTo` does.

**Actor-aware pipelines are untouched.** `NarrowedFilterEligible` already refuses them
(`actorEnumerator != nil`) because their fan-out is not bounded by their own MATCH labels, and
`actorAwareFanOutRelevant` keeps its existing endpoint-type-only judgment. Nothing here widens what a
Secure or actorAggregate lens sees, and nothing here narrows it either.

## 4. Why this is sound

The claim is exactly the one `evalPlainLinkReprojection`'s own skip path already makes and documents:
*"Neither endpoint type is bindable by the lens's patterns — the link cannot appear in its traversals;
skip (including the adjacency self-apply: the dedicated consumer owns the index, this lens just doesn't
need it applied-before-read)."* A link whose **relation** no pattern names is unbindable for the same
reason a link whose endpoint types are unbound is, and lands in the same already-sanctioned skip class —
including the adjacency pre-apply, whose authoritative writer is the dedicated whole-stream adjacency
consumer, not any lens pipeline.

Every widening path is preserved by defaulting to relevant: a non-full engine, a non-exhaustive relation
set, an untyped relationship, a multi-branch lens with one non-exhaustive branch, or a subject-budget
overrun all fall back to a filter at least as broad as today's.

### 4.1 What the adversarial review found (and what it could not refute)

A three-axis adversarial pass could not refute the claim. Two findings were real and are fixed in
`4336978b`; the rest are recorded here because they are live risks, not resolved ones.

- **A pattern nested in a node's or relationship's PROPERTY MAP was executed but never walked.** Those
  maps are `map[string]Expr` and the executor really evaluates them (`propsAllMatch` → `evalExpr` →
  `evalPatternComprehension` → `matchPath`), so
  `MATCH (b:book {owners: [(b)-[:borrowedBy]->(a:author) | a.key]})` would have reported an exhaustive
  EMPTY relation set and never been told its `borrowedBy` link changed. **This was a regression this
  design created**: `ReferencedLabels` had the identical blind spot, but the relation-blind link forms
  covered it by accident, and pinning the relation removed the cover. Both derivations now descend into
  both property maps. No corpus lens carries a pattern in a property map, so nothing live moved.
- **Relationship-type alternation `[:a|b]` is silently truncated to the first type** by the visitor.
  Pre-existing and *consistent* — the executor only knows the first type too, so the narrowing adds no
  new divergence; the query was already mis-executed. Zero corpus occurrences. `pkgmgr`'s anchor-walk
  parser rejects alternation outright for this reason; the general Cypher path does not.
- **A relation token now reaches `subjects.validateToken`, which panics on `.`/`*`/`>`/whitespace,** and
  openCypher admits an escaped `` [:`a.b`] ``. Same class already existed for labels via
  `CoreKVVertexFilter`; this widens the hazard rather than creating it.
- **Plain lenses narrow without a standing healer, which the actor-aware path explicitly refuses to do.**
  `ActorAwareNarrowingLabels` will not narrow unless `sweeper != nil`, on the grounds that narrowing
  removes an incidental reprojection that today happens to heal a lost row. `NarrowedFilterEligible` has
  no such conjunct, and `SetSweepPlan`'s only caller is `InstallActorAggregate` — so **every** plain lens
  has `sweeper == nil` by construction and requiring one would disable plain narrowing entirely. This is
  the pre-existing posture of the label narrowing, not something this design introduces; it is recorded
  because the asymmetry is real and undocumented, and because 17 zero-relationship lenses (three of them
  auth-plane `GrantTable` producers, one Protected/RLS) now heal only on `vtx.<label>.>` traffic. Their
  rows do derive from the anchor vertex alone, so the claim holds — but it is now load-bearing where it
  used to have a cushion.
- **The two new fields join `plainReprojectLabels`/`plainReprojectAll` in an unsynchronized
  write against a concurrent reader.** Already a filed row; that row now names these fields too.

## 5. Non-goals

- **Reverse anchor enumeration for neighbour events** (`D2 Phase 2`). That narrows *what a relevant
  neighbour event recomputes*; this narrows *which neighbour events exist at all*. Complementary, and
  this one is unblocked. §1's measurement is the demand trigger that row records as unmeasured.
- **The redelivery-cadence question** (§1, last paragraph).
- **Draining the messages already ack-pending when the filter narrows.** §6 records what cycling
  actually did to them, and it is not what was predicted.
- **actorAggregate / Secure lens fan-out.** §3.5.

## 6. Verification

- Unit, `ruleengine/full`: relation set + exhaustiveness over typed, untyped, variable-length,
  multi-segment-`WITH`, `PatternComprehension` and zero-relationship specs.
- Unit, `subjects`: the three forms, dedup + deterministic order, the empty-relation-set case, and the
  extended pairwise-non-subset proof against nats-server's own `isSubsetMatch`.
- Unit, `pipeline`: `ConsumerFilter`'s three-step degradation; `plainLinkReactsTo`'s default-relevant
  cases; a hot-reload re-deriving both sets together.
- Full `go test ./...` — `ConsumerFilter` is read by activation and by Rebuild.
**Live, after cycling `bin/refractor` from `main` at 11:21 UTC 2026-08-03.** The consumer's own
JetStream config now reads:

```
$KV.core-kv.vtx.identity.>        $KV.core-kv.vtx.provider.>
$KV.core-kv.lnk.identity.*.identifiedBy.>   $KV.core-kv.lnk.provider.*.identifiedBy.>
$KV.core-kv.lnk.*.*.identifiedBy.identity.> $KV.core-kv.lnk.*.*.identifiedBy.provider.>
```

`lnk.service.<id>.providedTo.identity.<id>` no longer matches any of them, and the pump has processed
none since the cycle (the last was 11:19 UTC, one minute before it).

### 6.1 The residual, and the prediction that was wrong

**Predicted:** the ~600 messages already ack-pending when the filter narrowed would keep being
redelivered (a filter update does not retract the server's pending map, and `getNextMsg`'s redelivery
branch loads by stream sequence), and the new client-side `plainLinkReactsTo` gate would ack each in
microseconds instead of driving a re-execute — so the floor would drain fast.

**Observed:** it does not drain at all. Sampled every 20s for the 5.5 minutes after the cycle — past a
full 5-minute AckWait expiry cycle, the interval that had been releasing a burst — `num_ack_pending` sits at exactly 600,
`ack_floor.stream_seq` at 4,287, `delivered.consumer_seq` at 27,295, `num_pending` 0 — every counter
frozen, no deliveries whatever. The server declines to redeliver a pending message whose subject the
consumer's current filter no longer admits, so those 600 are stranded ack-pending and hold the floor
where it is. The client-side gate never gets the chance the prediction gave it; its value is for a lens
that keeps a broader filter than its relation set would allow (§3.5), not as a residual drain.

The drain is therefore an explicit durable reset — `lattice lens rebuild clinicProviders`, which is
exactly the operation whose inability to complete was this row's original symptom. That is filed as its
own row rather than run blind inside this fire, because a rebuild reprojects the whole lens and deserves
to be watched.

## 7. Build note — fire 2026-08-03

**Scope sentence (verbatim, from §3):** carry the relation set alongside the referenced-label set, so a
plain full-engine lens's Core-KV consumer subscribes only to links whose relation its patterns traverse,
and its client-side link gate refuses the rest.

**Touch list (verified live):**

- `internal/refractor/ruleengine/full/labels.go:25` — `ReferencedLabels`, the shape `ReferencedRelations`
  mirrors; `full/ast.go:102` — `RelPattern{Type, MinHops, MaxHops}`.
- `internal/refractor/subjects/subjects.go:137-187` — the link-form builders + `CoreKVNarrowedFilters`.
- `internal/refractor/pipeline/pipeline.go:24-30` — `maxNarrowedFilterLabels`; `:92-99` —
  `plainReprojectLabels`/`plainReprojectAll`; `:437-465` — the derivation in `useFullEngineBranches`;
  `:515-528` — `plainReactsTo`; `:676-729` — `NarrowedFilterEligible` / `ConsumerFilter`;
  `:1548-1600` — `evalPlainLinkReprojection`.

**Precedents to mirror:** `ReferencedLabels`' per-WITH-segment two-pass walk and its "conservative by
construction" default; `plainReactsTo` vs `plainVertexRelevant`'s deliberately different false-cases;
`subjects_test.go`'s `isSubsetMatch` reimplementation of the server's rejection rule.

**Increment order:** (1) `ReferencedRelations` + tests · (2) subject builders + the extended pairwise
proof · (3) pipeline derivation, `ConsumerFilter` degradation, client gate · (4) live verify on the
running stack.

**In-scope gotchas:** the relation set must be re-derived on every `useFullEngineBranches` call, not
cached beside a stale label set — a MATCH hot-reload that widens relations while narrowing labels would
otherwise under-deliver forever (a JetStream filter update never resets the cursor). An empty exhaustive
relation set is a *narrowing*, not a "no data" fallback; the two must not collapse to the same `nil`.

**Non-goals:** §5.
