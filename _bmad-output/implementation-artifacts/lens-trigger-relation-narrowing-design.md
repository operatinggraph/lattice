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

**Actor-aware pipelines are untouched by the RELATION dimension** — but not, any longer, for the reason
originally written here. This section said `NarrowedFilterEligible` "already refuses them
(`actorEnumerator != nil`)", which stopped being true when
[auth-plane-projection-latency](auth-plane-projection-latency-design.md) Increment 2 made an actor-aware
pipeline eligible for **label** narrowing off §4.2's conjunction. The conclusion survives the premise's
loss, and is now enforced explicitly rather than inherited: `ConsumerFilter` gates the relation branch on
`actorEnumerator == nil`, because `actorAwareFanOutRelevant` keeps its endpoint-type-only judgment and so
has no client-side relation gate for a relation-pinned subject to be conservative against. An actor-aware
lens therefore narrows by label only. Extending this dimension to it means giving that fan-out arm a
relation gate first — its own row, not a consequence of this design.

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

The drain is therefore an explicit durable reset — `lattice lens rebuild`, which is exactly the
operation whose inability to complete was this row's original symptom. **Run live at 11:31 UTC, and it
is the end-to-end proof the fix works:**

```
before   ackpend 600   flr_s 4,287    dlv_c 27,295   pend 0
+12s     ackpend 1     flr_s 5,535    dlv_c 115      pend 324
+48s     ackpend 0     flr_s 47,026   dlv_c 439      pend 0
```

Under a minute, from a cold cursor to the head of the stream, on **439 total deliveries** — against the
27,295 the relation-blind consumer had accumulated without ever reaching a drained state. `lattice lens
health` reports `status active`, `consumerLag 0`, and the `clinic-providers` read model carries all
7 provider rows. The board row that opened this — *"a lens consumer's ack floor freezes, so its rebuild
can never drain"* — is closed by a rebuild that drained.

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

## 8. Build note — rule-swap synchronization, fire 2026-08-03

**Scope sentence:** make the compiled-rule field set a MATCH hot-reload rewrites safe against the
consumer goroutine that reads it — §4.1's last finding, filed as its own row.

**The defect.** `cmd/refractor/reload.go:346` calls `UseFullEngineBranches` on a **live** pipeline from
CoreKVSource's dispatch goroutine while the consumer goroutine is inside `handle` reading the same
fields. Ten fields were written by separate unsynchronized stores. The label pair fails closed, but the
relation pair does not: `plainRelationsExhaustive` is stated positively, so a reader seeing the new flag
against the not-yet-published map judges every link against an **empty exhaustive set** and skips all of
them. Beyond the race, the four narrowing fields could be read **torn** — one rule's labels against
another's relations — which is a filter no MATCH ever declared.

**Shape.** A `ruleMu sync.RWMutex` guards exactly the set `useFullEngineBranches` writes; that function
now derives everything into a local `ruleState` and publishes it under one `Lock`. Readers take one
`ruleState` **value snapshot** and thread it down, so an event is judged *and* evaluated by a single
rule. Three entry points snapshot: `handle` (consumer), `Reproject` (sweep / control RPC / retry queue),
`Hydrate` (personal-lens attach). Every other field on `Pipeline` is install-time only.

Two constraints the shape exists to satisfy: `sync.RWMutex` does not admit recursive read locks, so the
in-pipeline callers take `actorAwareNarrowingLabels(rs)` / `narrowedFilterEligible(rs)` rather than the
exported wrappers; and the published maps are **copy-on-write** — fresh every call, never mutated after
publication — which is what makes a snapshot safe to read after the lock is dropped.

**`ConsumerFilter` was the live tear.** It read the label set and the relation set in two separate
steps, so a reload between them built a filter from two different rules.

**Verification.** `rule_swap_race_internal_test.go` drives a writer alternating two specs that differ in
*both* narrowing dimensions against readers on every gate. `TestRuleSwap_ConcurrentHotReload_NoRace` is
the `-race` verdict; `TestRuleSwap_ObservedRuleIsNeverTorn` is the assertion the detector cannot make —
every snapshot must be a rule that actually exists. Both were proven to **fail without the fix**: with
the two lock bodies removed the detector reports the race, and the tear test fails *without* `-race`,
observing `{labels:[owner unit], relations:[bookedBy]}` — spec A's labels against spec B's relation.

**Non-goals:** the other §4.1 findings, each its own row.

### 8.1 What the adversarial pass added — a stale write is not self-correcting

The three-axis pass could not refute the fix (the concurrency axis ran the right sensitivity check:
stripping only the lock pairs from a copy produced 12 race reports, so its green verdict is a real
negative). The auth-plane axis found something the fix did not cause but did **widen**, and it is now
closed in the same change.

**The chain, verified link by link.** Every result is stamped with its own message's stream sequence
(`pipeline.go`'s `writeResults`), and a MATCH reload's rebuild replays the same messages with the *same*
sequences — which the guarded adapter drops as an idempotent no-op (`adapter/natskv.go`,
`storedSeq >= incomingSeq`). The rebuild's truncate is what normally clears the way for the replay, but
it runs on the reload's own goroutine (`cmd/refractor/reload.go`'s `MatchChange` arm) while the handler
is still mid-flight. So an in-flight evaluation can land its stale row **after** the purge and then
swallow its own correction.

On the auth plane that row is the pre-edit permission set: a MATCH edit made to **revoke** something is
silently defeated for every actor the in-flight event touched. The convergence sweep heals it, but only
for a lens that got a sweep plan — `projection/driver.go` refuses enrolment with a warning only — so
that is not a bound worth relying on.

**Pre-existing, but widened here.** Before the snapshot, the rule was read live at each execute call, so
a fan-out picked up the new rule partway through; the window was one executor call. The snapshot makes
the whole event use one rule, which is the coherence being bought — and it stretches the window to
"handle entry → last write." Shipping a wider auth-plane over-grant window was not acceptable, so the
snapshot now carries a **generation counter**: `writeResults` re-reads it and **Naks** rather than
writing results derived from a rule no longer in force. Redelivery re-evaluates under the rule actually
in force, which is what the reload wanted. It cannot loop — each Nak is answered by a redelivery that
finds a settled rule unless another reload has landed.

`TestWriteResults_SupersededRuleIsNakedNotWritten` pins it: no row reaches the target, and the
re-evaluation under the current rule writes exactly once.

**Also corrected from the reviews:** the `ruleMu` doc claimed every other field is install-time only —
false (`adpt`/`requireGuardedAdapter` are rewritten post-`Run` under `adapterMu`, and the progress and
rebuild counters mutate at runtime), and that sentence is the invariant a future author would trust; and
`ruleState`'s doc justified "take it once" with a deadlock that cannot occur, since the lock is released
before the function returns. The binding reason is coherence alone.
