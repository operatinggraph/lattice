# A dynamic type taxonomy — an abstract type is graph data, and a lens label expands against it

**Status: ✅ Andrew-RATIFIED 2026-08-06 · Fire A increment 1 in flight** — build-ready as **two fires**, both in the **Lattice lane**
(§14, rewritten at ratification). Five amendments below are Andrew's and supersede the body where they
differ. Contract #1 §1.7's abstract-vertexType note is **committed** with a transitional marker. Authored
2026-08-06, co-designed with Andrew in the ratify session; subsumes the location-domain class question.

> **AMENDMENTS (Andrew, ratify session 2026-08-06) — these supersede the body where they differ.**
>
> **A1 — Taxonomy events ride the EXISTING meta watch; no new consumer.** The "the taxonomy needs its own
> consumer" conclusion does not follow from its own evidence, which shows only that *today's* filter and
> handler exclude links. Two additive changes suffice: let `SubscribeKVChanges` take more than one prefix
> (plural `FilterSubjects` is what the shipped narrowing already relies on) and add a `lnk.meta.*` branch
> where `corekv_source.go:550`'s `// Unknown / link / malformed → ignore` currently sits. **There is no
> backfill window to reason about at all**: that consumer's durable is `lens-source-<instance>-<bootNonce>`
> with `IncludeHistory: true` and `PruneStaleDurables` sweeping the prior boot's, so it already replays
> matching history from the beginning on every start — the taxonomy is reconstructed at boot and live link
> events arrive as they happen. **Reuse is the correct shape, not merely the cheap one:** a lens-definition
> change and a taxonomy change invalidate the *same* artifact (a compiled rule and its derived label set),
> so one consumer gives a single total order over both. Two consumers would manufacture a genuine race —
> "rule recompiled under the old taxonomy, taxonomy event arrives after" — as a state the design would then
> have to handle. Cost: a marginally longer boot replay.
>
> **A2 — Expansion is EXPLICIT in the cypher: `:unit` is exactly `vtx.unit.<id>`; `:unit*` expands.**
> Implicit resolution is rejected, and the reason inverts the argument the session first reached for.
> Implicit means adding `studio subtypeOf unit` **silently widens** every existing `:unit` lens's result
> set — and on this platform a widened result set can be a widened *grant*, since read-grant producers
> anchor on exactly these types. Silently failing to include a new leaf is a visible gap; silently
> including it is an over-grant nobody sees. Explicit is the fail-closed reading of a label. It also gives
> an author the only opt-out that matters — a lens wanting units and not studios simply writes `:unit`,
> with **no class check needed**, which independently confirms that retiring the body-`class` binding
> fallback was right. `*` is the **reflexive-transitive** closure: self ∪ all descendants at any depth (for
> a purely abstract name, just the descendants), so a multi-level taxonomy never needs `**`. The sigil is
> free in this grammatical position — Cypher uses `*` for variable-length relationships and `RETURN *`,
> never on a node label — but accepting a trailing `*` on `OC_LabelName` is a **real parser extension** to
> size, not a string tweak. This section is the canonical source for the syntax until the lens-authoring
> skill exists.
>
> **A3 — A bare label naming no concrete key type is an ERROR, not an empty match.** "`:location` finds
> nothing because it is abstract" is right about the semantics and wrong as a runtime behaviour: a lens
> that silently projects nothing is the failure class this session spent its day removing. The taxonomy
> supplies a vocabulary for the first time, so the case is detectable. The gate gets three checks: a bare
> label naming no concrete key type (error — a typo, or a forgotten `*`); `label*` where the name is not
> declared abstract (error — nothing to expand); and an expansion exceeding the ≤8-label cap (a health
> signal, never a silent drop to the broad filter). This makes label resolution a vocabulary lookup where
> today it is an uninterpreted string, so the **unknown-label posture must be a declared decision** — a
> cross-package label's resolvability depends on install order.
>
> **A5 (Andrew, 2026-08-08) — a CONCRETE type may have subtypes; the parent of a `subtypeOf` need not be
> abstract.** §3.4's "a type may be both concrete and abstract-of-others" row is correct and authoritative;
> §3.5's rule that the install fails unless the named parent is `abstract: true` was the mistaken clause and is
> rewritten above. This is also what amendment A2 already implied — *"self ∪ all descendants at any depth (for a
> purely abstract name, just the descendants)"* only needs its parenthetical if a **non**-purely-abstract name
> can have descendants too. What the install still refuses is a parent that does not resolve, is tombstoned, or
> does not name a live `meta.ddl.vertexType`: that class check, not an abstractness check, is what catches a ref
> naming a lens or op-meta canonicalName, which is the authoring typo the guard exists for. Raised during Fire A
> increment 1, whose build had implemented §3.5 faithfully and pinned the wrong behaviour with a test.
>
> **DD pass, 2026-08-06 — §§1–8 verified, §9.3's census corrected.** An independent probe checked ~35
> citations across the four label-keyed mechanisms, anchor retraction, `seedAnchorFor`, the meta-CDC watch,
> the caps and `location-domain/ddls.go:56`; **every one verified exact or functionally exact**, including
> all seven `step6_resolve_ddl.go` fail-open paths (the evidence that the backfill precondition is false),
> the demand claim (`capabilityServiceAccess` really is unlabeled at `loc0`/`loc`/`exLoc`), zero premature
> adopters, and the ratified-not-built state of the sibling label-binding design. Three corrections to
> **§9.3 only**, folded here:
>
> 1. **The census undercounts: ≥10 sites across 7 packages, not 8 across 5.** Missing
>    `wellness-domain/ddls.go:1612` and `maintenance-domain/ddls.go:485`, both reaching the same check
>    through a generic `require_live_typed(…, "location")` helper rather than a named wrapper — so a
>    grep for the named wrappers misses them. The fire must census by the *check*, not by the wrapper.
> 2. **One site is miscategorised in the unsafe direction.** `clinic-domain/site.go`'s `SetSiteProfile`
>    (`:124-130`) is listed as "redundant — drop the class check". It is not: `require_live_building` tests
>    alive-ness and `cls != "location"` and nothing else, and the file's five `parts_of` calls all belong to
>    a *different* DDL script (`:288-315`), so they do not guard this op. The class check is the **sole**
>    type guard on `buildingKey` — dropping it would let the op write a `clinicSiteProfile` aspect onto any
>    live vertex. A shipped test pins the current behavior
>    (`TestClinic_SetSiteProfileRejectsNonLocationBuilding`). That site needs a key-segment or taxonomy
>    check **added**, not the class check deleted.
> 3. **Site #6's citation drifted 25 lines** (`clinic-domain/ddls.go:2021-2022`, not `:1996-1997`).
>
> And one caveat the section should carry rather than imply: the "dropping the class check makes the guard
> *stricter*" claim rests on an **empirical** invariant — that only `location-domain` mints these key
> shapes today — not on anything Contract #1 enforces. It also does not address the migration window in
> which pre-rename vertices still carry the old shared class while new guards expect the per-type one.

Designer fire 2026-08-06 · owner: Refractor (rule engine) + Processor (step 6 gate) + pkgmgr (declaration surface) ·
Size **L** (3 fires) · Imp **★★★** · depends on `lens-label-key-type-binding-design.md` (✅ ratified `f365d80a`)

---

## For Andrew

**What it does.** A *type* becomes a first-class graph object: `unit`, `building`, `property` each get their own
type meta vertex, and a new **abstract** type `location` gets one too, joined by
`lnk.meta.<unitMetaId>.subtypeOf.meta.<locationMetaId>`. A lens writes `MATCH (l:location*)` — the `*` is the explicit opt-in of amendment A2 — and the Refractor
resolves that label, **at activation**, into the concrete leaf set `{unit, building, property}`. A different
package installing `room` later adds one link, and every lens filtering on `location` picks it up — no lens edit,
no redeploy, no human action. This is the mechanism your ratification of the sibling design specified in place of
its §8.3 label disjunction, and it is what makes an abstract label *dynamic* rather than another static list.

**The engineering core is that the taxonomy resolves into a label SET, not into a cypher traversal.** You asked
whether the lens could instead walk the taxonomy in its own pattern, accepting the loss of the JetStream filter.
Verified: it inverts. A pattern-polymorphic position must be **unlabeled**, and an unlabeled node sets
`exhaustive = false` (`labels.go:101-107`), which empties *every* efficiency mechanism at once — the server filter
goes broad (`pipeline.go:934-935`), the client relevance gate stops skipping anything (`:743-744`), and — the one that
decides it — `AnchorLabel()` returns `ok=false` for an unlabeled anchor (`anchor_delete.go:252`), so
`seedAnchorLabel` is empty and `seedAnchorFor` returns `""` (`pipeline.go:667`), and `""` means **recompute the
lens's whole row set on every write anywhere in the graph**. Anchoring on the meta vertex instead is no better:
an instance write becomes a *neighbour* event, which `seedAnchorFor`'s first conjunct keeps at full recompute by
design (`:653-656`), and the row shape degenerates to one row per type. So the taxonomy is read once at
activation and cached; the hot path stays a map lookup.

**Three corrections my grounding forced on the brief — each one is load-bearing.** (1) There are **four**
label-equality sites to generalize, not three: the brief's list omits `seedAnchorBinds`
(`executor.go:774`, the engine half of seeding) and `AnchorProjectionKey` (`anchor_delete.go:86`, **anchor
retraction**) — and missing the second one on a grant-producing lens is a grant that never revokes. (2) The
brief's backfill precondition — *"an instance cannot pre-exist its own type DDL"* — is **false**: there is no
vertex-type vocabulary anywhere in the platform (`keys.go:141-155` is a bare `[a-z][a-z0-9]*` regex; step 6's
DDL resolution fails **open** on seven distinct paths, `step6_resolve_ddl.go:202-240`), so `vtx.room.<id>` can
and does commit before anything declares `room`. A taxonomy widening therefore needs a **Rebuild**, not an
in-place filter update. (3) Refractor's only meta-vertex CDC source is server-side filtered to
`$KV.<bucket>.vtx.meta.>` and its handler explicitly drops links (`corekv_source.go:395-419`, `:550`), so a
`subtypeOf` link event is **structurally invisible** to it — the taxonomy needs its own consumer. Extending the
existing watch would have produced a trigger that silently never fires.

**One fork for you (§9.3).** The write-side guards. Eight hand-written copies of `class != "location"` across five
packages (`loftspace-domain/ddls.go:373` is the one you named; the others are enumerated in §9.2) all break when
the shared bare class is dropped. Two of the eight genuinely want *any location* — `location-domain`'s own
`WireContainedIn` and `service-location`'s four wiring ops — and those are the only sites that need a taxonomy
*read on the write path*. That read has a real cost (Contract #2 §2.5 read-posture declarations at every
dispatcher, plus a fail-closed answer when the taxonomy is unreadable), and it is a genuinely separable mechanism
from the read-path expansion this design is about. **My recommendation: build the read path (Fires 1–2), land
location-domain with those two guards checking `cls in LOCATION_TYPES` against a package-local list (Fire 3), and
file the taxonomy-read guard as its own row with these two sites named as its consumer.** The alternative — one
larger fire that also does the write path — is defensible; I am not asking you to accept the local list as
permanent, only to decide whether it ships in this design or the next.

**Honest size: L, three fires.** It touches Processor (one new fail-closed gate), pkgmgr (a declaration surface, a
cross-package name resolution, an install preflight), Refractor (a new resolver + consumer, four matcher sites, an
exhaustiveness rule, a health field), and five packages. It is not shrinkable to M without dropping either the
invalidation trigger (which makes the whole thing unsound) or the retraction site (which makes it an over-grant).
**No frozen-contract edit is staged.** One proposed Contract #1 §1.7 note is recorded as text in §11, to be staged
only if you ratify.

---

## 1. Problem + intent

The sibling design (`lens-label-key-type-binding-design.md`, ✅ ratified `f365d80a`) deletes `nodeMatches`'
body-`class`/`label` fallback so a pattern label resolves against the **address** — the Contract #1
`vtx.<type>.<id>` key type — and nothing else. That closes a real soundness hole and brings the binder into line
with the five consumers that already read a label as a key type.

It also removes the platform's only way, however accidental, to say **"any location."** Your ratification settled
what replaces it (`lens-label-key-type-binding-design.md:25-42`):

> It resolves as a **dynamic type taxonomy**: a `subtypeOf` link between the *type meta vertices*, so a new leaf
> (`room`, `hallway`) can be declared **by a different package** and picked up by any lens labelling the abstract
> type, with no lens edit and no redeploy. … Under the taxonomy the leaf types stay as they are, `location`
> becomes a declared abstract type, the shared bare class is dropped, and `loftspace-domain:373`'s
> `cls != "location"` guard becomes a taxonomy check.

Two things make this more than an expressiveness nicety.

**It is the platform's own idiom, applied to the one thing that had escaped it.** DDLs, lenses, weaver targets,
loom patterns, op-metas and panes are all meta vertices joined by links (`internal/pkgmgr/build.go:99-296`). A
*type* is the one schema object with no graph representation of its own — `unit` exists nowhere except as a string
inside keys. Giving it a vertex is what lets a relationship between types be *declared* rather than hardcoded.

**And it is the only shape that satisfies the extensibility requirement.** Every static alternative — a label
disjunction, a spec-declared list, a compile-time expansion in pkgmgr — requires editing the *consuming* lens
when a *new* leaf appears, in a *different* package. §12 works through each.

**Scope note.** This design subsumes the location-domain class question. There is no separate rename item; §9
states how location-domain lands.

## 2. Grounding ledger (verified `file:line`, this fire)

Every row was opened in this fire against current `main` (`d89d57e1`). Each cites code that *does* the thing.
Rows marked **⚠** are the ones that changed the design.

| Claim | Evidence |
|---|---|
| **⚠** A pattern label is compared to a vertex's key type by **string equality**, in four places | `executor.go:563` (`nodeMatches`), `executor.go:774` (`seedAnchorBinds`), `anchor_delete.go:86` (`AnchorProjectionKey` — retraction), `pipeline.go:667` (`seedAnchorFor`) |
| The label **set** consumers are already set-membership tests | `pipeline.go:690` (`plainReactsTo`), `:746` (`plainVertexRelevant`), `:817-826` (`actorAwareNarrowingLabels`), `:910` (`narrowedFilterEligible`), `:928-957` (`ConsumerFilter`) |
| An **unlabeled** node pattern sets `exhaustive = false` | `labels.go:101-107` (`addPattern`'s unlabeled branch) |
| `exhaustive = false` ⇒ `reprojectAll = true` ⇒ broad filter + no client skip | `pipeline.go:512, :551-552` (`next.reprojectAll`), `:743-744` (`plainVertexRelevant` returns true), `:934-935` (`ConsumerFilter` returns `CoreKVFilter`) |
| **⚠** An unlabeled anchor yields no `seedAnchorLabel`, and an empty `seedAnchorLabel` means **whole-row-set recompute** | `anchor_delete.go:247-256` (`AnchorLabel` returns `ok=false` when `n.Label == ""`), `pipeline.go:563-570` (only a labeled anchor arms it), `:666-679` (`seedAnchorFor` returns `""`) |
| A **neighbour**-type event keeps the full recompute by design | `pipeline.go:653-656` (`seedAnchorFor`'s first conjunct doc + `:667`'s `eventLabel != rs.seedAnchorLabel`) |
| `ReferencedLabels` is a **pure AST function** — no I/O, no engine state | `labels.go` in full (`ReferencedLabels` reads only `cr.Query.Clauses`) |
| Labels → filter subjects: **3 forms per label** (vertex, link-source, link-target) | `subjects.go:170-178` (`CoreKVNarrowedFilters`), `:128-148` (the three builders) |
| The caps: **8 labels**, **24 subjects**, relation count = `|L|×(1+2|R|)` | `pipeline.go:29` (`maxNarrowedFilterLabels`), `:39` (`maxNarrowedFilterSubjects`), `:952` (the arithmetic) |
| A JetStream **filter update never resets the consumer's cursor** | `pipeline.go:926-927` (`ConsumerFilter` doc, citing nats-server v2.14.0) |
| `Rebuild` **recomputes** the filter and **delete-recreates** the durable | `pipeline.go:1408-1433` (`ConsumerFilter()` then `supervisor.UpdateSpec` + `supervisor.Reset`); `consumer_supervisor.go:193-198` (`Reset` = `DeleteConsumer` then `createConsumer`) |
| `UpdateSpec` alone never touches the live durable | `consumer_supervisor.go:209-229` (doc: *"without recreating the durable … before a Reset recreates the durable"*) |
| A narrowed-registration **failure** already logs + writes a health signal, then falls back to broad | `pipeline.go:1253-1276` (`registerWithFilterFallback`: `slog.Error` at `:1266`, `reporter.RecordError` at `:1270`, `applyBroad()` at `:1274`) |
| A **cap-driven** fallback is silent — no log, no health write | `pipeline.go:934-935` (`ConsumerFilter` returns the broad filter on `len(labels) > maxNarrowedFilterLabels` with no side effect) |
| **⚠** Refractor's meta CDC source is server-side filtered to `vtx.meta.>` and **drops link events** | `corekv_source.go:395-419` (`SubscribeKVChanges(…, "vtx.meta.", …)`), `subscribe.go:51` (the filter is `$KV.<bucket>.<prefix>`), `corekv_source.go:550` (`// Unknown / link / malformed → ignore`) |
| A `MatchChange` hot-reload swaps the compiled rule **in place** and then fires `Rebuild` | `cmd/refractor/reload.go:346` (`UseFullEngineBranches`), `:359-363` (`Rebuild`) |
| `CompiledRule` is a two-field struct — a shallow copy is safe | `ast.go:247-258` (`Query *Query`, `KeyColumns []string`) |
| Rule state is published **copy-on-write** under one lock; nothing may be mutated in place | `pipeline.go:574-581` (`ruleState` doc), `:627-643` (`publishRuleState`) |
| **⚠** No vertex-type vocabulary exists — a type segment is a bare regex | `keys.go:141-155` (`isValidTypeSegment`: `[a-z][a-z0-9]*`, no registry lookup) |
| **⚠** Step 6's DDL resolution fails **open** on seven paths; a missing DDL means checks are **skipped**, not rejected | `step6_resolve_ddl.go:202-205`, `:210-213`, `:214-217`, `:219-227`, `:209`+`:240`, `:264-269`, `:280-285`; consumed at `step6_validate.go:156-157` then `return nil` at `:199` |
| `NoDDLForClass` gates only **which script runs**, never what keys its mutations address | `step4_hydrate.go:110-114` (hydration fails on the *operation's own* class); `starlark_runner.go:316-370` (`parseMutations` reads the key verbatim from script output) |
| The `instanceOf` chain is bounded 4 hops with a **visited-set cycle guard** — and fails **open** | `step6_resolve_ddl.go:14-20` (`maxInstanceOfHops = 4`), `:207-217` (`visited` map, `break` on cycle) |
| The `instanceOf` chain terminates only at a `vertexType` meta | `step6_resolve_ddl.go:219-227` (`ok && ref.Kind == "vertexType"`) |
| Exactly one live `instanceOf` edge, else ambiguity ⇒ refuse | `step6_resolve_ddl.go:280-285` (`soleTarget`) |
| `location-domain` declares **ONE** DDL whose canonicalName is the *abstract* name, governing **three** key types | `packages/location-domain/ddls.go:56` (`CanonicalName: "location"`), `:57` (`Class: "meta.ddl.vertexType"`), `:182` (`LOCATION_TYPES = ["unit","building","property"]`), `:187` (`LOCATION_CLASS = "location"`), `:315` (`loc_key = "vtx." + lt + "." + loc_id`), `:318` (`make_vtx(loc_key, LOCATION_CLASS, {})`) |
| The `class != "location"` guard Andrew named | `packages/loftspace-domain/ddls.go:364-374` (`require_live_unit`; `cls != "location"` at `:373`), called at `:383`, `:410`, `:442` |
| A meta vertex is `vtx.meta.<NanoID>` + a `.canonicalName` aspect | `internal/pkgmgr/build.go:29` (`metaVertexPrefix`), `:106-107` (DDL canonicalName aspect), `internal/bootstrap/meta_ddl.go:162-167` (`nanoid.new()` then the aspect) |
| Meta-vertex lookup by canonicalName: the Processor's O(1) cache, or pkgmgr's full-bucket scan | `internal/processor/ddl_cache.go:60-93`, `:298-303` (`Lookup`); `internal/pkgmgr/installer.go:645-707` (`checkCanonicalNameCollision` scans `vtx.meta.*.canonicalName`) |
| The DDL cache is invalidated **synchronously in-commit** for any `vtx.meta.*` mutation | `internal/processor/step8_commit.go:315-340` |
| A package install is **one** atomic batch | `internal/pkgmgr/installer.go:159-171` (one `submitOp`), `internal/bootstrap/install_ddl.go:70-121` (one script pass), `internal/processor/step8_commit.go:278` (one `AtomicBatch`) |
| `meta` is legal as a link endpoint type on **both** sides; both-meta has no production instance yet | source: `build.go:292` (`lnk.meta.<paneId>.offeredTo.role.<roleId>`); target: `build.go:345` (`forOperation.meta`), `packages/service-domain/ddls.go:1082` (`instanceOf.meta`); both-meta: only `pipeline/filter_retraction_internal_test.go:477` (a test fixture) |
| `CreateMetaVertex` cannot create links, and rejects any class outside its five | `internal/bootstrap/meta_ddl.go:131-138` (`ALLOWED_DDL_CLASSES`), `:231` (`fail("UnknownMetaClass")`); no `lnk.` construction anywhere in the file |
| Install guardrails check key **shape** + create-only + no `_`-prefixed aspects — **no** ownership/namespace confinement on a create | `internal/bootstrap/install_ddl.go:39-68`, `:94-95`, `:103-106`; the protected-key backstop fires only on update/tombstone (`:22-32`) |
| A cross-package meta reference is passed through on **syntax alone** today | `internal/pkgmgr/build.go:504-522` (`resolveLensRef`: `substrate.IsValidNanoID(lensRef)` ⇒ accept, no existence check) |
| Uninstall tombstones only the uninstalling package's own `declaredKeys` | `internal/pkgmgr/installer.go:847-872` |
| `authz_anchors` holds bare NanoIDs + the `*` wildcard; RLS is NanoID set membership | `internal/refractor/adapter/rls.go:22`, `:61`, `:99`, `:194-210` |
| Permission `scope` is a closed vocabulary, `default:` **denies** | `internal/processor/step3_auth_capability.go:515`, `:524`, `:555`, `:563`, `:570-575` |
| `WeaverTargetSpec` carries **no** vertex-type field; applicability comes from the bound lens | `internal/pkgmgr/definition.go:231-270`; `internal/weaver/engine.go:405` (registry watches `<targetId>.>`) |
| `entityType` is a per-walk **literal** with no validation, required by convention to equal `entityKey`'s type segment | `packages/edge-manifest/lenses.go:733-735`, `:1034-1038`; consumed by `===` in `cmd/facet/web/app.js:36`, `:293`, `:1207` |
| `AnchorWalk.AnchorType` must equal the Chain's own label, and is stamped as a **body-only audit literal** | `internal/pkgmgr/anchorwalk.go:389-394` (strict equality), `:163` (*"body-only"*), `:608-615` (`collectBranch` writes `anchorType: '%s'`) |
| No label validation exists at lens install/translate/lint time | `internal/refractor/lens/schema.go:189-278` (`Parse`), `corekv_source.go:621-699` (`translateSpec`), `ruleengine/full/visitor.go:248` (`np.Label = ln.GetText()`), `scripts/lint-lens-anchors.go:67-70` (label matched by `[A-Za-z0-9_]+`) |
| `healthwire.Entry` has **no** field for narrowing/footprint state | `internal/refractor/health/healthwire/healthwire.go:38-106`; status vocabulary closed at `:21-29` |
| Contract #1: 6-segment link key; `meta`/`op` are the only reserved type names | `docs/contracts/01-addressing-and-envelope.md:11`, `:38-45` |
| Contract #1 §1.7 enumerates the meta-vertex classes | `docs/contracts/01-addressing-and-envelope.md:192-258` |

### 2.1 Corpus census (this fire)

Distinct node-label counts across all **105** lens specs in `packages/**` + `internal/bootstrap/lenses.go`:

| Distinct labels | 7 | 6 | 5 | 4 | 3 | 2 | 1 | n/a |
|---|---|---|---|---|---|---|---|---|
| Lenses | 1 | 2 | 7 | 17 | 23 | 28 | 26 | 1 |

- **Zero** lenses sit at the 8-label cap today. The maximum is 7 (`edgeIdentity`,
  `packages/edge-manifest/lenses.go:71`) — a Personal lens, which is structurally excluded from narrowing anyway
  (`actorEnumerator != nil` ⇒ `narrowedFilterEligible` returns false, `pipeline.go:910`).
- Of the 105, **47** never narrow regardless of label count (31 `actorAggregate`, 15 `Personal`, 1 eventStream),
  and 9 more carry a variable-length relationship (`exhaustive = false`). **49** are genuinely governed by the
  8-label cap, and the highest among them is **6** (`leaseApplicationsRead`,
  `packages/lease-signing/lenses.go:98`).
- **31** lenses already exceed the 24-subject relation budget and so already run the relation-blind narrowed set.

Location leaf-type label sites: **23 lines / 25 label instances across 20 lenses in 7 packages** — `unit` 13,
`building` 12, `property` **0** (`property` is not a label anywhere in the corpus). Only **3** are in
seed/anchor position (`clinicSites` `packages/clinic-domain/lenses.go:534`, `availableListings`
`packages/loftspace-domain/lenses.go:137`, `landlordUnitsRead` `:201`); the other 20 are traversal.

**Which of those 20 want the abstract label?** Censused one by one: **none of them.** Every site pins a leaf for a
load-bearing domain reason — `appliesToUnit` structurally only ever targets a unit; a clinic "site" is
definitionally a building; and `staffReadGrants` states the narrowness as an *intended* property in its own words:
*"a worksAt wired to a unit or a property therefore grants nothing, which is the intended granularity"*
(`packages/service-location/lenses.go` doc above `:166`). The one lens that genuinely wants "any location on the
containment chain" — `capabilityServiceAccess`, `packages/service-location/lenses.go:133-145` — gets it today by
leaving `loc0`/`loc`/`exLoc` **completely unlabeled**, paying the broad filter, and relying on the relation shapes
to bind only locations.

**This is the honest demand picture, and it does not weaken the case — it sharpens it.** The taxonomy's first
customer is `capabilityServiceAccess`, which today buys polymorphism at the price of a broad filter and no
anchor seeding. Labelling those three nodes `:location` converts a permanently-unnarrowable auth-plane lens into a
narrowed one. That is a concrete, live, nameable consumer — not a speculative one.

## 3. The taxonomy data model

### 3.1 A type meta vertex

A vertex type is declared by a `meta.ddl.vertexType` meta vertex whose `.canonicalName` **is the key type**:

```
vtx.meta.<NanoID>                          class = "meta.ddl.vertexType"
vtx.meta.<NanoID>.canonicalName            {"value": "unit"}
vtx.meta.<NanoID>.permittedCommands        [...]           (concrete types only)
vtx.meta.<NanoID>.script                   <starlark>      (concrete types only)
```

Never `vtx.meta.unit` — the id is a NanoID and the name lives in the aspect (house rule; the shape is
`build.go:106-107`, `meta_ddl.go:162-167`).

This artifact already exists for most types. **It does not exist for `unit`, `building`, or `property`**:
`location-domain` declares exactly one `meta.ddl.vertexType`, whose canonicalName is `location` — the abstract
name — governing all three key types, because it writes `class: "location"` onto each
(`packages/location-domain/ddls.go:56`, `:187`, `:318`). So today the *abstract* has a meta vertex and the
*leaves* have none. §9 inverts that, which is why the class rename is a prerequisite of this design rather than a
cosmetic tail.

### 3.2 An abstract type meta vertex

Identical shape, plus an explicit marker on the root document:

```
vtx.meta.<NanoID>                          class = "meta.ddl.vertexType"
                                           data  = {"abstract": true}
vtx.meta.<NanoID>.canonicalName            {"value": "location"}
```

An abstract type declares **no** `.script` and **no** `.permittedCommands`, because no instance ever carries it as
a class and no key ever uses it as a type segment (§8).

**The marker is explicit, not derived.** "A vertexType DDL with no script" would also identify it, and that is
exactly the *accident of shape* the platform has been burned by before: a guarantee that holds because of what the
corpus happens to contain is not a guarantee. `DDLCache` gains an `Abstract bool` on `MetaVertexRef`, populated
from `data.abstract` in the same scan that already populates `Kind` and `PermittedCommands`
(`internal/processor/ddl_cache.go:120-174`).

### 3.3 The `subtypeOf` link

```
lnk.meta.<leafTypeMetaId>.subtypeOf.meta.<abstractTypeMetaId>
```

Six segments, `meta` on both sides (Contract #1 `01-addressing-and-envelope.md:11`). `meta` is already legal as a
link endpoint type on both the source side (`build.go:292`) and the target side (`build.go:345`,
`packages/service-domain/ddls.go:1082`); a link with `meta` on *both* sides has no production instance yet — only
a test fixture (`pipeline/filter_retraction_internal_test.go:477`) — so this is the first, and worth naming.

**Direction, per Contract #1 §1.1:** the later-arriving vertex is the source, the pre-existing one the target. The
leaf type arrives after the abstract (an abstract with no leaves is useless; a leaf without its abstract cannot
resolve), so **leaf → abstract**.

**Relation name: `subtypeOf`.** Sentence test: *"unit subtypeOf location"* — reads correctly, and both
endpoints are unambiguously *type declarations*. Rejected: `isA` (invites the instance-level reading — "this unit
is a location" — which is what `instanceOf` already means, §7); `subtypeOf` (correct but reads as a
property rather than an act, and the platform's relation names are verbs — `holdsRole`, `grantedBy`,
`assignedTo`, `forOperation`, `instanceOf`, `offeredTo`); `refines` (vaguer). Settled: **`subtypeOf`**.

### 3.4 Shape rules

| Rule | Decision | Why |
|---|---|---|
| **Acyclicity** | Enforced. A cycle in the `subtypeOf` graph makes the leaf set undefined. | §5.4 places the enforcement. |
| **Multiple parents** | **Allowed.** `room subtypeOf location` and `room subtypeOf billable` are both true and both well-defined. | Resolution is always *downward* (abstract → its concrete members), never upward (leaf → its one type), so no ambiguity arises. The platform's one-live-edge precedent (`soleTarget`, `step6_resolve_ddl.go:280-285`) exists for *type authority*, where the question genuinely has one right answer; a taxonomy membership question does not have that shape. **Caveat carried forward:** the §12.8 DDL-inheritance extension *would* reintroduce ambiguity, and must forbid multiple parents or declare a precedence rule before it is built. |
| **Multi-level depth** | **Allowed**, bounded to `maxTaxonomyDepth = 4`. | Mirrors `maxInstanceOfHops = 4` (`step6_resolve_ddl.go:14-20`) and its stated reason: a bound plus a visited set keeps the walk terminating and abuse-proof against crafted cycles. |
| **Transitivity** | **Yes.** `room subtypeOf unit subtypeOf location` ⇒ `location` covers `room`. | Anything else makes an abstract's meaning depend on where in the chain a package chose to attach, which is the opposite of the "declare a leaf anywhere, get picked up" requirement. |
| **A type may be both concrete and abstract-of-others** | Yes. `unit` has instances *and* may have `room subtypeOf unit`. | Nothing forbids it and forbidding it would be arbitrary. |
| **The expanded set** | The **concrete** (non-`abstract`) members of the abstract's transitive downward closure. An abstract mid-type contributes its leaves but **not itself**. | An abstract type has no instances, so including it would add a filter subject (`$KV.<b>.vtx.location.>`) that can never match — and, worse, would let `plainVertexRelevant` admit a type that cannot exist, which is harmless but dishonest. |
| **An abstract type must not be named `meta` or `op`** | Enforced at install. | Contract #1 §1.2 (`:38-45`) reserves both. |
| **An empty expanded set** | The lens becomes **non-exhaustive** (broad filter), and it logs. Never `exhaustive = true` on an empty set. | An empty answer is far more likely "the resolver has not loaded yet" than "genuinely zero leaves." `ConsumerFilter` already treats `len(labels) == 0` as broad (`pipeline.go:934-935`), so this is consistent with the shipped gate rather than a new special case. |

### 3.5 Declaration surface (pkgmgr)

`DDLSpec` gains two fields:

- `Abstract bool` — declares a type with no instances. Mutually exclusive with `Script`/`PermittedCommands`
  (validated at build).
- `SubtypeOfRef string` — names the abstract type this concrete (or mid-abstract) type is a **subtype of**,
  **by canonicalName**. The installer resolves it and emits the `subtypeOf` link into the same atomic batch.
- `LeafBudget int` — abstract types only; see §6.2.

**Resolution, and where it must NOT copy the precedent.** An in-batch abstract resolves from the batch-local map,
exactly as `resolveLensRef` does for a lens canonicalName (`build.go:504-522`). A **cross-package** abstract is
not in-batch, and `resolveLensRef`'s fallback — accept any syntactically valid NanoID with no existence check
(`build.go:517-520`) — **must not be copied**. `checkCanonicalNameCollision` already scans every
`vtx.meta.*.canonicalName` in the bucket (`installer.go:645-707`), so the lookup exists; the install **fails
closed** when the named parent does not resolve, does not resolve to a live `meta.ddl.vertexType` meta vertex,
or is tombstoned. **The parent need not be abstract** — a concrete type may have subtypes (amendment A5), so
the class check, not an abstractness check, is what catches a ref naming a lens or op-meta canonicalName.
(Recorded per the
*verify precedent, don't just copy it* rule: `resolveLensRef`'s pass-through is unfixed debt, not a pattern.)

**Cross-package declaration needs no cooperation from the abstract's owner.** Package B declares
`DDLSpec{CanonicalName: "room", SubtypeOfRef: "location"}` and the installer emits
`lnk.meta.<roomId>.subtypeOf.meta.<locationId>` in B's own batch. The install guardrails permit it: they check
key shape, create-only, and aspect naming (`install_ddl.go:39-68`, `:94-95`, `:103-106`), and the protected-key
backstop fires only on update/tombstone (`:22-32`) — B creates a *new* link key and never mutates
location-domain's vertex. **This is the property that makes the whole design work**, and it is a pre-existing
platform affordance, not a new hole.

**One uninstall hazard, named.** Uninstall tombstones only the uninstalling package's own `declaredKeys`
(`installer.go:847-872`). The `subtypeOf` link lives in B's declaredKeys, so uninstalling B cleans it up. But
uninstalling **location-domain** leaves B's link pointing at a tombstoned target. The resolver therefore treats a
link whose target meta is absent or tombstoned as **not contributing** (§5.1), and that shrink is a taxonomy
narrowing, handled by §5.3's tombstone path.

## 4. Expansion: what happens, and where it is cached

### 4.1 Placement relative to `ReferencedLabels`

`ReferencedLabels` (`labels.go`) is a pure AST function with no I/O. Expansion must not go inside it: it would
need a graph read, and it is called from `useFullEngineBranches`, which a hot-reload re-enters
(`cmd/refractor/reload.go:346`).

Expansion happens in **`useFullEngineBranches` (`pipeline.go:489-571`)**, immediately after `ReferencedLabels()`
returns and before `next.reprojectLabels` / `next.reprojectAll` are set:

```
for each compiled branch:
    ls, ok := fullCR.ReferencedLabels()            // unchanged, pure AST
    if !ok  → exhaustive = false                   // unchanged
    else    → expanded, armed := resolver.Expand(ls)
              if !armed → exhaustive = false       // FAIL CLOSED
              else      → union expanded into labels
```

`resolver.Expand` maps each label to itself when concrete, and to its concrete downward closure when abstract.

### 4.2 The exhaustiveness rule — the one the brief flags

> An abstract label must not make a lens report exhaustive on a leaf set that can grow after activation without
> the trigger firing.

`exhaustive` stays `true` only when the resolver is **armed** — meaning its taxonomy snapshot loaded *and* its
invalidation consumer is live (§5). Any of: resolver absent, snapshot never loaded, consumer not running, cycle
detected, depth bound exceeded, abstract unresolvable, expanded set empty ⇒ `exhaustive = false` ⇒
`reprojectAll = true` ⇒ broad filter + no client skip. **Correct-but-slower, never wrong-but-fast.**

`armed` is a live read at derivation time, not a snapshot taken once at process start — mirroring
`ActorAwareNarrowingLabels`' own reason for being evaluated per event rather than at installation
(`pipeline.go:772-778`: activation installs components in stages, so a snapshot taken during installation reads a
later stage's component as absent).

### 4.3 Where the per-pattern expansion lives (and why it must be a copy)

The four equality sites in §5 need the expansion for a *specific* `NodePattern`, and two of them are inside the
executor, which receives a `*full.CompiledRule` — not the `ruleState`.

`CompiledRule` is `{Query *Query; KeyColumns []string}` (`ast.go:247-258`), and rule state is published
**copy-on-write** precisely so a reader can never observe a half-rewritten rule (`pipeline.go:574-581`,
`:627-643`). So the expansion must **not** be written onto the live `cr` in place — that would race a concurrent
evaluation.

Instead: `full.WithLabelExpansion(cr, exp) *CompiledRule` returns a **shallow copy** carrying a new immutable
field `LabelExpansion map[string]map[string]struct{}` (label → the set that label admits; a concrete label maps to
`{itself}`). `useFullEngineBranches` publishes that copy as `next.cr`. The `Query` AST is shared read-only, so the
copy is cheap and safe. Nothing mutates a published rule, ever.

### 4.4 The hot path stays a map lookup

`nodeMatches` runs per candidate binding. Its generalized form is: parse the key type once
(`substrate.ParseVertexKey`, as today at `executor.go:562`), then one map lookup in the pre-resolved set. **No
graph read, no resolver call, no allocation per binding.** A concrete label's set is a single-entry map, so the
common case costs one hash probe more than today's string compare — the price of the whole feature, paid once per
binding attempt.

## 5. The generalized mechanisms — per-site changes

Four **equality** sites become set-membership. Five **set** consumers generalize for free because they are already
set-membership tests over `reprojectLabels`, which now simply holds more entries.

### 5.1 The four equality sites

| # | Site | Today | After | Why it is load-bearing |
|---|---|---|---|---|
| 1 | `executor.go:563` `nodeMatches` | `if vtype == n.Label` | `if _, ok := cr.LabelExpansion[n.Label][vtype]; ok` | Evaluation-time binding. Lands **after** the sibling design deletes `:567`/`:570`'s class/label branches, so the function becomes: empty label ⇒ true; else parse the key type and test membership. |
| 2 | `executor.go:774` `seedAnchorBinds` | `return ok && vtype == n.Label` | membership test | The **engine half** of event seeding. `seedAnchorFor`'s doc states the engine independently re-derives that the key's type matches the anchor pattern (`pipeline.go:648-651`); leaving this as equality would make site 4's narrowing dead — the pipeline offers a seed and the engine refuses it. |
| 3 | `anchor_delete.go:86` `AnchorProjectionKey` | `eventType != anchorLabel` ⇒ refuse | membership test | **Anchor retraction.** An abstract-anchored lens whose anchor is tombstoned arrives as a vertex event of the *leaf* type. Left as equality, the retraction never fires — the row goes stale rather than retracting. On a read model that is a stale row; on a grant producer it is **a grant that never revokes** (the over-grant direction). This site is absent from the brief's list and is the most dangerous of the four. |
| 4 | `pipeline.go:667` `seedAnchorFor` | `eventLabel != rs.seedAnchorLabel` ⇒ `""` | membership against the expanded anchor set | `""` means recompute the lens's whole row set (`:644-651`). Left as equality, an abstract-anchored lens pays a **full corpus rescan on every leaf write** — strictly worse than today. `ruleState.seedAnchorLabel string` becomes `seedAnchorLabels map[string]struct{}`, derived from `AnchorLabel()` (unchanged — it still returns the *written* label) put through the resolver at `pipeline.go:563-570`. |

Sites 3 and 4 both key off `anchorPattern` (`anchor_delete.go:258-275`), the single shared derivation every
anchor-scoped mechanism uses. Generalizing them together preserves that single-derivation property; generalizing
one and not the other would split it.

### 5.2 The five set consumers — no change needed, but a stated reason each

| Consumer | Site | Why it generalizes free |
|---|---|---|
| `plainReactsTo` | `pipeline.go:690` | `_, ok := rs.reprojectLabels[vertexType]` — a longer set admits more types. |
| `plainVertexRelevant` | `:746` | Same lookup. Its false branch **drops the event with no fallback** (`:722-738`), which is why §5.3's widen ordering matters. |
| `actorAwareNarrowingLabels` | `:817-826` | Two membership tests (`actorType`, `secureIdentityKeyType`). Neither is a location type today, but an abstract-labeled actor type would resolve through the same expanded set — no special case. |
| `narrowedFilterEligible` | `:910` | Returns `rs.reprojectLabels` wholesale. |
| `ConsumerFilter` | `:928-957` | Iterates the set into `labelList` and hands it to `subjects.CoreKVNarrowedFilters` / `CoreKVRelationNarrowedFilters`, both of which take `[]string` and dedupe-sort (`subjects.go:171`, `:229`). The **only** behavioural coupling is the cap comparison at `:934` and `:952` — §6. |

`subjects.CoreKVNarrowedFilters`' pairwise-non-subset property (`subjects.go:159-169`) is preserved: it holds for
*any* label list, equal or distinct, so a longer list from expansion cannot produce the subset-shaped pair
nats-server rejects.

### 5.3 What must NOT change

**Evaluation-time matching stays a pure set-membership test on the key type.** No per-binding graph read, no
resolver call from inside the executor, no lazy resolution. The resolver is consulted exactly once per
activation/re-derivation, in `useFullEngineBranches`. If a future change is tempted to resolve lazily, note that
`readNode` is memoized per evaluation for repeatable-read reasons (`executor.go:778-781`) — a taxonomy read inside
an evaluation would be a second, unmemoized read path with its own consistency question, which is exactly the
class of defect the memoization exists to prevent.

## 6. Invalidation, the trigger, and its race

### 6.1 The trigger does not exist today and cannot be grafted onto the existing watch

Refractor's only meta-vertex CDC source subscribes with prefix `"vtx.meta."`
(`corekv_source.go:395-419`), which `substrate.SubscribeKVChanges` turns into the **server-side** FilterSubject
`$KV.<bucket>.vtx.meta.>` (`subscribe.go:51`, `:331-355`). A `lnk.meta.<a>.subtypeOf.meta.<b>` write is
therefore never delivered to that consumer — and even if it were, the handler's switch covers only `KindVertex`
and `KindAspect`, with `// Unknown / link / malformed → ignore` at `corekv_source.go:550`.

So the taxonomy gets **its own consumer**, owned by a new `internal/refractor/taxonomy` resolver constructed once
in `cmd/refractor/main.go` and injected into every pipeline (the shape `p.sweeper` / `p.secureDecryptor` already
use). One watch, one cache, N pipelines.

```
FilterSubjects: [ $KV.<bucket>.vtx.meta.>                       // type metas: canonicalName, abstract flag
                  $KV.<bucket>.lnk.meta.*.subtypeOf.>  ]      // the taxonomy edges
DeliverPolicy:  DeliverLastPerSubject   (+ IncludeHistory)      // a fresh boot sees the whole taxonomy
```

The second subject pins segment 3 (`lnk`), segment 4 (`meta`), and segment 6 (`subtypeOf`), wildcards the leaf
id, and lets `>` absorb `meta.<abstractId>`. The two forms differ at segment 3 in a position neither wildcards, so
they are not a subset pair — the same property `subjects.go:159-169` argues for the narrowed set.

### 6.2 Ordering — and it is the reverse of the naive reading

A widening must update the **client gate before the server filter**, not after.

The reason is asymmetric consequence. `plainVertexRelevant`'s false branch **acks and drops** the event outright,
with no other write path (`pipeline.go:722-738` says so explicitly). A too-broad server filter merely delivers
events the client then skips — pure cost. So:

- **Client gate first, server filter second.** If the client widens first, it briefly admits events the server is
  not yet delivering: harmless. If the server widens first, it delivers leaf events the client gate then
  ack-and-drops: **permanently lost**.
- Both are published in one `publishRuleState` swap (`pipeline.go:627-643`), so there is no window *within* a
  pipeline — the ordering constraint is between the swap and the consumer-filter change, and the swap goes first.

### 6.3 A widening needs a Rebuild, not a filter update — because the brief's precondition is false

The brief hypothesized the backfill case is near-trivial *"because an instance cannot pre-exist its own type
DDL."* I tried to prove that and it is **false**:

- The type segment of a key is validated by a bare regex with no registry lookup — `isValidTypeSegment`,
  `keys.go:141-155`.
- A mutation's key is read verbatim from Starlark script output — `parseMutations`, `starlark_runner.go:316-370`.
- Step 6's DDL resolution fails **open** on seven paths, and `validateOne` skips the whole DDL-driven block and
  `return nil` when it does — `step6_resolve_ddl.go:202-240`, `step6_validate.go:156-157`, `:199`.
- The one gate that requires a DDL to exist, `NoDDLForClass` (`step4_hydrate.go:110-114`), constrains only which
  **script runs** for the *operation's own* class — never which key types that script's mutations may address.

So `vtx.room.<id>` can commit long before anything declares `room`. And an in-place `FilterSubjects` change
**never moves the cursor** (`pipeline.go:926-927`, nats-server v2.14.0), so those pre-existing instances would
never be delivered — a permanent, silent hole in every lens using the abstract.

**Therefore a `subtypeOf` create triggers `Rebuild(ctx, truncate=false)` on every lens whose expanded label set
actually changed.** `Rebuild` recomputes `ConsumerFilter()` and delete-recreates the durable via
`supervisor.Reset` (`pipeline.go:1417-1433`; `consumer_supervisor.go:193-198`), which under
`DeliverLastPerSubject` (the activation policy, `cmd/refractor/main.go:1025`) replays the last value of every
matching subject — exactly the backfill required. It also sets the lens's health status to `rebuilding`, so the
operator sees it.

This is more expensive than the brief hoped, and it is the honest answer. Bounding it: only lenses whose expanded
set *changed* rebuild (a set comparison, not a blanket sweep); a rebuild is already serialized per pipeline by
`rebuildInFlight`; and the trigger frequency is "a package declared a new type," which is an install event, not a
data event.

**The race the brief names is closed by the same mechanism.** "A package install completes and an op mints a leaf
instance before Refractor has processed the taxonomy event" — the rebuild's fresh cursor sees that instance
whichever order the two events landed in. Without the rebuild it would be a silent permanent miss. So the Rebuild
is not belt-and-braces; it is what makes the ordering irrelevant.

### 6.4 A narrowing (tombstone) must also go through Rebuild — for a different reason

A `subtypeOf` tombstone shrinks the leaf set. The lens should stop matching that type — **and its
already-projected rows for that type must retract.**

That is a **row-set shrink, not a single-row overwrite**, and "overwrite-by-reprojection retracts it" is the exact
assumption the sibling design flags as having burned this lane (`lens-label-key-type-binding-design.md:321`). A
bare filter narrowing would leave every `room` row orphaned forever, and on a grant target an orphaned row is a
live grant.

So: a tombstone triggers `Rebuild` too, which re-derives the correct row set through whichever retraction the lens
already declares (`diffRetraction`, filter retraction, or the anchor-tombstone path). **A tombstone must never
narrow the filter in place.**

### 6.5 Fail-closed posture, complete

| Event | Action |
|---|---|
| `subtypeOf` create, expanded set grew | Publish widened rule state (client gate), then `Rebuild` (recomputes filter + resets cursor). |
| `subtypeOf` tombstone, expanded set shrank | `Rebuild`. Never an in-place narrowing. |
| Type meta created/updated (`abstract` flag flips, canonicalName changes) | Re-derive; treat as grow or shrink accordingly. |
| Abstract's target meta tombstoned (the uninstall hazard, §3.5) | Link stops contributing ⇒ shrink ⇒ `Rebuild`. |
| Resolver read fault, cycle, depth exceeded, unresolvable, empty set | `exhaustive = false` ⇒ `reprojectAll = true` ⇒ broad filter. **Never keep a stale narrow set.** |
| Resolver's consumer dies | `armed = false` ⇒ every abstract-using lens degrades to broad on its next derivation, and a health signal fires. |

The final row is the invariant the whole section serves: **a stale narrow set is the only unacceptable state**,
because it is the one that silently drops real events.

## 7. `instanceOf` and `subtypeOf` — two questions, one meeting point

No per-instance link is needed. But the `instanceOf` precedent is real and must be related rather than ignored.

- **`instanceOf` answers "what is THIS vertex's type authority?"** — per-instance, walked at **commit** time,
  bounded 4 hops with a visited-set cycle guard, terminating only at a `Kind == "vertexType"` meta
  (`step6_resolve_ddl.go:207-227`). `packages/service-location/lenses.go:133-145` reads that same chain from a
  lens to discriminate a service *template* from an *instance*: an instance's `instanceOf` lands on a
  `vtx.service.*` (so it matches `:service` and is excluded), a template's lands on a `vtx.meta.*` (so it does
  not, and is admitted).
- **`subtypeOf` answers "which concrete types does this abstract name cover?"** — per-*type*, resolved once at
  **activation**.

They meet at the type meta vertex: `instanceOf` chains *terminate* there, and `subtypeOf` links those same
terminals to each other. The taxonomy is the edge set over the terminals of the instanceOf chains. Nothing
per-instance is required, because the expansion is a property of the *type*, and every instance already reaches its
type through the chain that exists.

**And a per-instance `instanceOf` pointing at an abstract would be actively wrong** under this design:
`resolveGoverningDDL` would resolve that instance to an abstract DDL, which §8's gate rejects. Stated so nobody
builds it by analogy.

## 8. Scope boundaries — where an abstract type may and may not appear

The whole point of an abstract type is that it names no instance. Every place a type name is consumed must
therefore be classified. Each row is grounded.

| Surface | Abstract allowed? | Reason (code) |
|---|---|---|
| **A lens pattern label** | **YES** — the driving case. | §4–5. |
| **A vertex key's type segment** (`vtx.<type>.<id>`) | **NO — new fail-closed gate.** | No instance has an abstract type. `isValidTypeSegment` is a bare regex (`keys.go:141-155`) so nothing stops it today. Step 6 gains: reject a mutation whose key contains an abstract type segment in **any** position (vertex root, aspect owner, or either link endpoint). |
| **A link key's endpoint type segments** | **NO** — same gate. | A link key names two concrete vertices; the endpoint type is a *restatement* of the endpoint's own key (`docs/contracts/01-addressing-and-envelope.md:11`). An abstract there names no vertex. |
| **A document's `class`** | **NO — new fail-closed gate.** | Step 6 rejects a mutation whose class resolves to a DDL with `Abstract == true`. This is an *addition* to §1.5, not a contradiction: §1.5's permissive default covers the case where **no** DDL is found; here one is found and is structurally unusable. |
| **`authz_anchors`** | **Vacuously N/A.** | Holds bare NanoIDs plus the reserved `*` (`adapter/rls.go:22`, `:61`, `:99`); RLS compares NanoID set membership (`:194-210`). No type name has ever appeared there. |
| **A permission `scope`** | **Already denied.** | Closed vocabulary `any\|self\|specific\|owned`; the `default:` arm denies with `"unknown platformPermission.scope"` (`step3_auth_capability.go:570-575`). Nothing to add. |
| **A Weaver target** | **Transitively yes, no new surface.** | `WeaverTargetSpec` carries no vertex-type field (`definition.go:231-270`); applicability comes entirely from the bound lens's own label, and the registry watches `<targetId>.>` (`weaver/engine.go:405`). An abstract reaches Weaver only *through* a lens, where §5 already handles it. |
| **`manifest.ent`'s `entityType`** | **NO — scoped out, with a named gap.** | A per-walk **literal** (`edge-manifest/lenses.go:733-735`) that must equal `entityKey`'s own type segment for op-attach to work (`:1034-1038`), consumed by `===` comparisons in `cmd/facet/web/app.js:36`, `:293`, `:1207`. An abstract literal would desync from the concrete `entityKey` and silently break op attachment. **Nothing enforces the pairing today** — §14 files it as an authoring gate rather than leaving it implicit. |
| **`AnchorWalk.AnchorType`** (Path-B read-grant walks) | **NO — scoped out, with the reason.** | It must equal the Chain's own anchor label by strict equality (`anchorwalk.go:389-394`) *and* is stamped as a **body-only audit literal** by `collectBranch` (`:163`, `:608-615`). An abstract anchor would write an audit value naming a kind no instance has, and fixing that needs a per-row `typeOf(x.key)` engine function — a separate item with its own consumer. The abstract **is** allowed in non-anchor chain positions. |
| **The names `meta` and `op`** | **NO.** | Contract #1 §1.2 reserves both (`:38-45`). |

### 8.1 Why the body `class` must not re-enter the resolution path

The sibling design makes the **address** the sole authority for a label. This design adds exactly **one**
indirection — a declared taxonomy — and that indirection is itself **address-valued**: it maps a written label to
a set of *key types*. There is still exactly one authority.

Letting `class` back in would break that three ways:

1. **Two authorities, and the asymmetry returns.** The fallback bound in traversal position but seeded nothing in
   seed position (`lens-label-key-type-binding-design.md:171-181`), so behaviour depended on where the label
   appeared. A taxonomy plus a class fallback would reproduce that, now with two disagreeing sources.
2. **A body field can never narrow delivery.** Your own ratification states the mechanism: the Core-KV subject is
   derived from the **key**, and NATS filters on subject tokens
   (`lens-label-key-type-binding-design.md:37-40`). Expansion has to produce key types or the JetStream filter —
   the largest single win here — is unreachable.
3. **A class is per-document; a taxonomy is per-type.** Resolving per document means a read per binding, which
   §5.3 forbids for good reason. Resolving per type means one read per activation.

## 9. How location-domain lands

### 9.1 The shape

| Today | After |
|---|---|
| One DDL, `CanonicalName: "location"`, governing three key types (`packages/location-domain/ddls.go:56`) | **Four** type metas: concrete `unit`, `building`, `property` (each with the DDL's existing script + permittedCommands), and **abstract `location`** (`Abstract: true`, no script) |
| `LOCATION_CLASS = "location"` written onto all three (`:187`, `:318`) | `make_vtx(loc_key, lt, {})` — **class equals the key type**, satisfying the invariant the sibling design's census says every other package already meets (`lens-label-key-type-binding-design.md:108-110`) |
| No taxonomy | `lnk.meta.<unitId>.subtypeOf.meta.<locationId>` × 3, emitted in location-domain's own install batch |
| `LOCATION_TYPES = [...]` hardcoded in Starlark (`:182`) | Still present for the write-side guards in Fire 3 (§9.3); the *read* side no longer consults it |

The three concrete DDLs share one script const (they already do — one Starlark body serves all three key types),
so the duplication is three `DDLSpec` entries pointing at the same `Script`, not three scripts.

### 9.2 The guard census — 8 sites, 5 packages

The `cls != "location"` check is hand-copied eight times. All eight break when the class becomes the key type.

| # | Site | Op(s) guarded | Wants |
|---|---|---|---|
| 1 | `packages/location-domain/ddls.go:305-306` `require_live_location` | `WireContainedIn` (both endpoints, `:364-365`), `SetLocationPresentation` (`:336`) | **Any location** — a `containedIn` chain legitimately spans unit → building → property |
| 2 | `packages/service-location/ddls.go:210`, `:261-262` `require_live_location` | `WireResidesIn` (`:331`), `WireWorksAt` (`:346`), `WireAvailableAt` (`:360`), `WireUnavailableAt` (`:374`) | **Any location** |
| 3 | `packages/cafe-domain/ddls.go:1490`, `:1496-1497` `require_live_location` | `CreateMenuItem` (`:1623`) | A specific level in practice; the local list suffices |
| 4 | `packages/clinic-domain/site.go:129-130` `require_live_building` | `SetSiteProfile` (`:139`) | **`building` only** — drop the class check; the key-segment check already proves it |
| 5 | `packages/clinic-domain/site.go:279-280` (second copy) | `AssignProviderSite` (`:294`) | **`building` only** |
| 6 | `packages/clinic-domain/ddls.go:1996-1997` `require_site_membership` | `CreateAppointment`'s optional `site` | **`building` only** |
| 7 | **`packages/loftspace-domain/ddls.go:373-374`** `require_live_unit` | `SetListing` (`:383`), `SetUnitAddress` (`:410`), `SetListingStatus` (`:442`) | **`unit` only** |
| 8 | `packages/loftspace-domain/ownership.go:243-244` (second copy) | `AssignUnitOwner` (`:265`) | **`unit` only** |

**Sites 4–8 need no taxonomy at all.** Each already double-enforces via `parts_of(key, name, "unit")` /
`parts_of(site_key, "site", "building")`, which hard-requires the key's own type segment. The class check was a
proxy for "location-domain minted this key," and the key-segment check proves it more directly. So those five
**drop the class check** and become *stricter*, not looser — including the site Andrew named, #7, whose own error
string is already `NotAUnit`.

**Sites 1–3 genuinely want "any location."** Their minimal landing is `cls in LOCATION_TYPES` against a
package-local list — behaviourally identical to today, with the list where it already is.

### 9.3 The fork (§For-Andrew)

Making sites 1–3 read the *taxonomy* instead of a local list is the write-path half of this design. It needs a
Starlark read of the `subtypeOf` edges, which under Contract #2 §2.5 must be declared at **every dispatcher** of
those ops — `contextHint.reads` for a required key, or an annotated class-(e) bounded `kv.Links` enumeration — plus
a fail-closed answer when the taxonomy is unreadable. That is a real mechanism with its own read-posture surface,
and it is separable from the read-path expansion.

**Recommendation: local list in Fire 3, taxonomy-read guard filed as its own row with sites 1–3 named as its
consumer.** Not deferred vaguely — the consumer is enumerated above, which is what the *deferred tail must name
its consumer* rule requires. I flag it as a fork rather than deciding it because it is the one place where "the
right long-term shape" and "this design's boundary" genuinely disagree, and that is your call, not mine.

### 9.4 What the 20 leaf-label lens sites do

**Nothing.** §2.1's census found that none of the 20 wants the abstract label — every one pins a leaf for a stated
domain reason, and `staffReadGrants` names its narrowness as intended behaviour. They keep their leaf labels.

The lens that changes is **`capabilityServiceAccess`** (`packages/service-location/lenses.go:133-145`), which
today leaves `loc0`/`loc`/`exLoc` **unlabeled** to get polymorphism, paying `exhaustive = false` — broad filter,
no client skip, no anchor seeding. Labelling those three `:location` converts a permanently-unnarrowable
auth-plane lens into a narrowed one. That is the design's first live customer, and it is the measurable win.

## 10. Cap arithmetic and operator visibility

### 10.1 The arithmetic

`maxNarrowedFilterLabels = 8` (`pipeline.go:29`); 3 subjects per label (`subjects.go:170-178`);
`maxNarrowedFilterSubjects = 24` (`:39`); relation-narrowed count `|L| × (1 + 2|R|)` (`:952`).

A lens with one abstract label plus **K** concrete labels has expanded count **K + |leaves|**. Today
`|leaves(location)| = 3`.

| K | Today (K+3) | After `room`+`hallway` (K+5) | Verdict |
|---|---|---|---|
| 1 | 4 | 6 | narrowed |
| 3 | 6 | 8 | narrowed — **zero headroom** |
| 4 | 7 | **9** | **BROAD** |

**The hazard is exact and the brief names it correctly: a lens author writes ONE label and a DIFFERENT package's
install changes the count.** The author cannot see it, and today's fallback is silent (`pipeline.go:934-935`
returns the broad filter with no log and no health write — unlike a *failed* registration, which does signal,
`:1266-1272`).

**The degradation is a two-step ladder, not a cliff**, which is worth knowing before designing the signal. The
relation budget bites first: `|L| × (1 + 2|R|) ≤ 24`. With `|R| = 2` and `K = 1`, expansion takes `4 × 5 = 20` to
`6 × 5 = 30 > 24`, dropping to the **relation-blind narrowed** set — still narrowed, just coarser
(`pipeline.go:941-947` degrades the two dimensions independently by design). Only the label cap drops all the way
to broad. So: **relation-narrowed → relation-blind narrowed → broad.**

Corpus headroom today (§2.1): zero lenses at 8; max among the 49 cap-governed lenses is 6. So the cap is not
reachable *now* — it becomes reachable the moment a lens uses an abstract label, which is what makes this a
design-time obligation rather than a live bug.

### 10.2 `leafBudget` — turning a dynamic silent regression into a static contract

An abstract type declares `LeafBudget int` (default 8, the label cap). Then:

- **A lens author gets a decidable answer.** At the *lens's* install, `K + leafBudget ≤ maxNarrowedFilterLabels`
  is checked, and the install **fails** if the lens cannot fit its own worst case. The lens author owns their own
  narrowing, and the refusal lands at their install, where they can fix it.
- **A leaf installer is never blocked.** Installing a leaf beyond the abstract's declared budget is a **warning
  plus a health signal, never a rejection.** Blocking it would let one package's lens veto another package's type
  declaration — precisely the coupling this design exists to remove.
- **Raising the budget is deliberate.** The abstract's owner raises `LeafBudget` in their own package version,
  which re-validates dependent lenses on their next upgrade.

Each fail-closed lands on the actor who can act on it — the *enforcement point follows the threat*.

### 10.3 What an operator sees

`healthwire.Entry` has no field for narrowing state (`healthwire.go:38-106`), and the status vocabulary is closed
at `active|paused|rebuilding` (`:21-29`). Additive change, no status added:

```
filterMode        "narrowed-relation" | "narrowed-label" | "broad"
filterLabelCount  int
filterBroadReason ""  |  "not-eligible" | "non-exhaustive" | "label-cap" | "taxonomy-unarmed" | "registration-failed"
```

Written whenever `ConsumerFilter` is computed (activation `cmd/refractor/main.go:1019`, and `Rebuild`
`pipeline.go:1417`), which is already the single place both callers derive the identical value from identical
inputs (`:913-919`).

This is **not** a `RecordError`. A cap-driven fallback is a footprint regression, not a fault, and routing it
through `errorCount`/`lastError` would make it indistinguishable in Loupe from a DLQ write failure or a refused
hot-reload — `RecordError`'s documented shared use (`health/reporter.go:228-236`). The existing
`registerWithFilterFallback` signal stays exactly as it is (it *is* a fault) and gains
`filterBroadReason = "registration-failed"` alongside.

`docs/observability/health-kv-schema.md` gains the three fields; Loupe's `lensRenderedState`
(`cmd/loupe/renderedstate.go:81-118`) renders `filterMode` as an informational badge, not a fault.

**What a package installer sees:** the install reply carries a warning per lens that would cross the cap, and the
same lenses' health entries flip to `filterBroadReason = "label-cap"` on their next derivation. The
`leafBudget` check is what makes the warning computable at install time rather than discoverable in production.

## 11. Contract surface

**No frozen-contract edit is staged.** Checked, surface by surface:

- **Contract #1 §1.1** (link direction): `subtypeOf` follows it — later-arriving leaf as source. No change.
- **Contract #1 §1.2** (`:38-45`, reserved `meta`/`op`): unchanged; §8 enforces it for abstract names.
- **Contract #1 §1.5** (DDL resolution, permissive default): §8's "reject a mutation whose class resolves to an
  abstract DDL" is an **addition**, not a contradiction. §1.5's permissive default governs the case where **no**
  DDL is found; this is the case where one *is* found and is structurally unusable. Abstract DDLs do not exist
  today, so the contract is silent rather than contrary.
- **Contract #2 §2.5** (Starlark read posture): only engaged if §9.3's fork resolves toward a taxonomy read on the
  write path. Read-path expansion is Go-side, not Starlark.
- **Contract #6 §6.14** (`anchorType`): unchanged — §8 scopes abstract out of `AnchorWalk.AnchorType`.
- **Contract #8** (package install): the declaration surface is additive `DDLSpec` fields; the guardrails
  (`install_ddl.go:39-68`) already permit the link create.

**One proposed note, text only, not staged.** Contract #1 §1.7's meta-vertex class table
(`01-addressing-and-envelope.md:192-258`) enumerates what each meta class means. An `abstract` vertexType is a new
member of that vocabulary and belongs in the table. Proposed insertion under §1.7's `meta.ddl.vertexType` entry:

> A `meta.ddl.vertexType` meta-vertex whose root `data.abstract` is `true` declares an **abstract** vertex type: a
> type name that participates in the type taxonomy but has no instances. No key may use an abstract type name in
> any type segment, and no document may carry it as a `class`; the Processor rejects either at commit. An abstract
> type declares no `.script` and no `.permittedCommands`. Concrete types are joined to it by
> `lnk.meta.<concreteTypeMetaId>.subtypeOf.meta.<abstractTypeMetaId>`, whose transitive downward closure is the
> set of concrete types the abstract name covers.

**Committed at ratification** (2026-08-06), with a transitional marker appended: abstract types land with
Fire A, so until that fire ships nothing declares one and the clause constrains nothing. That marker is
removed by Fire A.

## 12. Alternatives considered

**12.1 Spec-declared taxonomy** (a `SubtypeOf []string` on the lens spec, or a Go-side map in Refractor) —
**rejected.** It is static. A new leaf declared by a *different* package cannot be picked up without editing the
consuming lens and redeploying, which is the requirement verbatim. It also puts the type relationship in N places
(every consuming lens) instead of one (the type declaration).

**12.2 Pattern-traversal polymorphism** (leave the node unlabeled and walk `subtypeOf` in the cypher, or anchor
on the meta vertex) — **rejected, with the evidence.** This is the option Andrew asked about, and it inverts:

- A pattern-polymorphic position must be **unlabeled**. `labels.go:101-107` sets `exhaustive = false` for an
  unlabeled node that is not a re-reference.
- `exhaustive = false` ⇒ `reprojectAll = true` (`pipeline.go:512, :551-552`) ⇒ **(a)** `ConsumerFilter` returns the
  broad filter (`:934-935`), **(b)** `plainVertexRelevant` returns true for every type, skipping nothing (`:743-744`).
- **(c) the one that decides it:** `AnchorLabel()` returns `ok=false` for an unlabeled anchor
  (`anchor_delete.go:252`), so `seedAnchorLabel` is never armed (`pipeline.go:563-570`), so
  `seedAnchorFor` returns `""` (`:667`) — and `""` means recompute the lens's **whole row set**
  (`:644-651`). A pattern-polymorphic lens pays a full corpus rescan on every write anywhere in the graph:
  **strictly worse than today.**
- **Anchoring on the meta vertex is no better.** Instance writes become *neighbour* events, which
  `seedAnchorFor`'s first conjunct keeps at full recompute deliberately (`:653-656`), and the row shape
  degenerates to one row per type rather than one per instance. Additionally `AnchorProjectionKey` refuses any
  query containing a `WITH` (`anchor_delete.go:75-79`), and a traversal-polymorphic query will want one.

All three of the pipeline's efficiency mechanisms key off the label **set**, and a polymorphic pattern position
empties it. Resolving to a set at activation keeps all three.

**12.3 Label disjunction** (`MATCH (l:unit|building|property)`) — **superseded by ratification.** The sibling
design designed it and deliberately did not build it (§8.3); Andrew's ratification says *"Do not build §8.3's
disjunction"* (`lens-label-key-type-binding-design.md:33`). Recording *why* it loses on its merits too: it is
static — a new leaf requires editing every lens that names the union, in packages the leaf's author does not own.

**12.4 Do nothing / three patterns** — **rejected.** Either three separate lenses (3× the projections, 3× the
consumers, 3× the read models to keep converged) or one lens with a union — and the engine has no `UNION`, so that
is grammar + visitor + executor work for a strictly worse result. Both require N lens edits per new leaf.

**12.5 Keep the shared bare class and restore the body fallback** — **rejected.** The sibling design deleted it on
soundness grounds and was ratified; it reintroduces two authorities; and a body field can never narrow delivery
(§8.1).

**12.6 Dotted classes** (`location.unit`) — **set aside by Andrew on architectural grounds**
(`lens-label-key-type-binding-design.md:35-40`): the key type is the only thing that can be a *subscription*
filter, so any scheme that moves the discriminator into the body forfeits per-type narrowing permanently.
Recorded, not re-litigated.

**12.7 Collapse the three key types into one `vtx.location`** — same ratification, same reason: it would lose
per-type narrowing for every lens that legitimately wants only units.

**12.8 Resolve the taxonomy at lens *compile* time in pkgmgr** (bake the leaf set into the installed cypher) —
**rejected.** It makes the mechanism static again: a new leaf's install would have to rewrite every dependent
lens's cypher, i.e. mutate other packages' meta vertices, which the install guardrails forbid
(`docs/components/_packages.md:324-336`; the protected-key backstop, `install_ddl.go:22-32`). The whole point is
that resolution happens at *activation*, on the reader's side.

**12.9 Extend `subtypeOf` to inherit the DDL script / permittedCommands down the taxonomy** — **not built,
recorded as the natural extension.** It would remove the three-way `DDLSpec` duplication in §9.1 and give §9.3's
guards their taxonomy check for free. It is rejected *for this design* because it puts the taxonomy on the
**write** path inside `resolveGoverningDDL` (`step6_resolve_ddl.go:197-241`), whose blast radius is every
mutation in the platform, and because multiple parents (§3.4) would become genuinely ambiguous there and need a
precedence rule. Named so it is built deliberately later, not stumbled into.

## 13. Risks

| Risk | Assessment |
|---|---|
| **A missed retraction site becomes an over-grant** | The design's sharpest risk, and the reason §5.1 site 3 exists. An abstract-anchored grant producer whose `AnchorProjectionKey` still string-compares never retracts a tombstoned anchor. Mitigation: the site is enumerated, and §15's retraction test asserts it against a leaf-type tombstone specifically. |
| **A stale narrow set silently drops events** | The only unacceptable state (§6.5). Every uncertain path degrades to `reprojectAll` instead. The asymmetry is grounded: `plainVertexRelevant`'s false branch has no fallback (`pipeline.go:722-738`), a broad filter only costs work. |
| **Rebuild storm on a leaf install** | One leaf install rebuilds every lens whose expanded set changed. Bounded by the count of abstract-using lenses (1 at Fire 3 — `capabilityServiceAccess`), serialized per pipeline by `rebuildInFlight`, and triggered by install events, not data events. Named because it grows with adoption. |
| **Silent cap-driven footprint regression** | Real and dynamic. Mitigated by `leafBudget` (static, decidable, refused at the *lens's* install) + the `filterMode` health field. The degradation ladder (§10.1) means the first step is coarser narrowing, not broad. |
| **The resolver is a new single point** | If it is wrong, every abstract-using lens is wrong. Mitigations: fail-closed to broad on every uncertainty; a corpus census test pinning each lens's expanded set (§15); and it is read-only — it never writes Core KV (P2). |
| **Two concurrent installs form a cycle neither sees** | Real: install batches are atomic individually, not against each other. This is why **resolver-time** cycle detection is the authority and install-time is a courtesy (§5/§14). The resolver's answer is fail-closed to broad, so a cycle costs footprint, never correctness. |
| **`nodeMatches` is the hottest path in the engine** | Generalizing it adds one map probe over a pre-resolved single-entry set for a concrete label. No graph read, no allocation (§4.4). Measured in Fire 2 rather than asserted. |
| **`entityType` desync** | An abstract `entityType` literal would silently break `cmd/facet` op-attach, and nothing enforces the pairing today. Scoped out (§8) and filed as an authoring gate (§14) rather than left implicit. |
| **The class rename touches five packages' guards** | Five of eight sites become *stricter* (drop a redundant check); three keep a local list. Full-suite gate, and the integration test at `packages/location-domain/integration_test.go:195` (which asserts `class == "location"`) must be updated to assert class == key type — a test that pins the old invariant is exactly what should fail. |
| **Collision with in-flight Refractor designs** | `lens-label-key-type-binding-design.md` **must land first** (this design's §8.1 requires the class out of the resolution path). `full-engine-independent-branch-decomposition` touches the same executor file — sequence, no semantic overlap with §5's sites. |

## 14. Decomposition — TWO fires, both in the Lattice lane (rewritten at ratification)

Andrew's standing amendments: **one lane** (no Verticals split — the package-side guard edits and the
location split ride with the engine work, so no cross-lane `blocked-on` exists to go stale) and **fewer,
larger fires**. The original three collapse to two. The reasoning is the no-dead-scaffolding rule applied
honestly: the old Fires 1 *and* 2 are both **inert** until an abstract type is actually declared, so the
value only lands at the old Fire 3. Splitting the two inert halves buys nothing and costs a rebase.

### Fire A — the taxonomy exists, expands, and cannot be misused (Lattice, L). Green with zero consumers.

Old Fires 1 and 2 as one fire. Internal build order:

1. **Declaration surface + write-path gates.** `DDLSpec.Abstract`, `SubtypeOfRef`, `LeafBudget`; installer
   resolution (batch-local, then the `checkCanonicalNameCollision` scan, **fail-closed** on a miss — not
   `resolveLensRef`'s pass-through); `DDLCache.MetaVertexRef.Abstract` from `data.abstract`; step 6's two
   fail-closed gates (an abstract type segment in any key position; a class resolving to an abstract DDL);
   install-time acyclicity + depth check; the `LeafBudget` warning.
2. **The `*` sigil — a real parser extension** (amendment A2, which this design predates). A label is
   `OC_LabelName` in the openCypher grammar, so accepting a trailing `*` means extending the grammar or
   post-processing the label text. Size it as grammar work, not a string tweak. `*` is the
   **reflexive-transitive** closure. `:unit` keeps meaning exactly `vtx.unit.<id>`.
3. **Resolution + expansion.** `internal/refractor/taxonomy` resolver with `armed`;
   `full.WithLabelExpansion` returning a copy (§4.3); expansion in `useFullEngineBranches` with §4.2's
   exhaustiveness rule; the **four** equality sites (§5.1 — including anchor retraction);
   `ruleState.seedAnchorLabels` as a set; resolver-time cycle/depth detection as the authority.
4. **The trigger, on the EXISTING meta watch** (amendment A1 — *not* a new consumer). Widen
   `SubscribeKVChanges` to take more than one prefix and add a `lnk.meta.*` branch where
   `corekv_source.go:550` currently ignores links. That consumer's durable is per-boot with
   `IncludeHistory`, so history replay reconstructs the taxonomy at boot and there is no backfill window;
   one consumer also gives a single total order over lens-definition and taxonomy invalidation. Then the
   invalidation path itself: **client gate before server filter**, then `Rebuild`, for both grow and shrink
   (§6.2–6.4).
5. **The validation gate** (amendment A3): a bare label naming no concrete key type is an **error**, not a
   silent empty match; `label*` on a name that is not declared abstract is an error; an expansion exceeding
   the ≤8-label cap raises a health signal rather than dropping silently to the broad filter. Declare the
   unknown-label posture explicitly — resolution becomes a vocabulary lookup where today it is an
   uninterpreted string, and a cross-package label's resolvability depends on install order.
6. **Observability.** `filterMode` / `filterLabelCount` / `filterBroadReason` health fields, the schema doc,
   Loupe's badge.

**Not splittable, and the design's own argument for that stands:** expansion without the trigger is a stale
narrow set, and the trigger without the retraction site is an over-grant. **Review depth: full 3-layer
adversarial regardless of size** — this touches the auth plane's retraction and narrowing gates.

### Fire B — location lands and the first consumer arrives (Lattice, M).

Old Fire 3, with the census corrected. Four type metas (three concrete sharing one script, one abstract) +
three `subtypeOf` links; the class becomes the key type at `packages/location-domain/ddls.go:318`;
`packages/location-domain/integration_test.go:195` updated; and `capabilityServiceAccess`
(`packages/service-location/lenses.go:133-145`) labels `loc0`/`loc`/`exLoc` **`:location*`** — the first
live consumer, converting a permanently-broad auth-plane lens into a narrowed one.

**Three corrections this fire must honor, from the DD pass** (the doc's §9.2 census is wrong and must be
re-run rather than trusted):

- **At least 10 guard sites across 7 packages, not 8 across 5.** `wellness-domain/ddls.go:1612` and
  `maintenance-domain/ddls.go:485` reach the same check through a generic `require_live_typed(…, "location")`
  helper, so a grep for the named wrappers misses them. **Census by the check, not by the wrapper.**
- **`clinic-domain`'s `SetSiteProfile` needs a guard ADDED, not removed.** It is listed as "redundant — drop
  the class check", but `require_live_building` tests aliveness and the class and nothing else, and that
  file's `parts_of` calls all belong to a *different* DDL script. The class check is the **sole** type guard
  on `buildingKey`; dropping it would let the op write a `clinicSiteProfile` aspect onto any live vertex, and
  `TestClinic_SetSiteProfileRejectsNonLocationBuilding` pins the current rejection.
- **The "becomes stricter" claim is empirical, not enforced**, and the **migration window** is unaddressed:
  pre-rename vertices still carry the shared class while new guards expect the per-type one. The fire states
  its ordering rather than discovering it.

**Because Fire B changes a live security guard in a package as a side effect of an engine feature, it also
takes the full 3-layer review** — not the standard depth the original §14 assigned it.

**Sequencing.** Hard dependency: after `lens-label-key-type-binding-design.md` ships (§8.1) — label
resolution must have exactly one authority before an abstract label expands. Sequenced against
`full-engine-independent-branch-decomposition` (shared executor file).

**Filed alongside, not folded in** (each with its consumer named):

1. **Taxonomy-read write-side guard** — consumer: `location-domain`'s `WireContainedIn`,
   `service-location`'s four wiring ops, `cafe-domain`'s `CreateMenuItem`. Resolved at ratification toward
   the **local list** in Fire B, with the taxonomy-read form filed behind those three sites, because a
   Starlark taxonomy read pulls Contract #2 §2.5 read-posture declarations into every dispatcher.
2. **`entityType` ⟷ `entityKey` type-segment pairing gate** — consumer: every `edge-manifest` walk tail;
   today an unenforced convention.
3. **A per-row `typeOf(x.key)` engine function** — consumer: `AnchorWalk.AnchorType`'s audit literal, which
   is what currently forbids an abstract Path-B anchor.
4. **`subtypeOf`-driven DDL inheritance** — consumer: the three-way `DDLSpec` duplication in §9.1. Note the
   §3.4 caveat: this extension would reintroduce the multiple-parents ambiguity and must forbid multiple
   parents or declare a precedence rule first.

## 15. Test strategy

**Resolver (`internal/refractor/taxonomy`).** Downward closure over one level, multi-level
(`room → unit → location`), multiple parents (`room` under both `location` and `billable`); an abstract mid-type
contributes its leaves but **not itself** (§3.4); a cycle ⇒ `armed = false`; depth > 4 ⇒ `armed = false`; a
tombstoned target meta stops contributing; an empty expanded set ⇒ non-exhaustive. Each asserted on the
`(set, armed)` pair, never on `set` alone.

**The four equality sites**, each with an abstract label, each written to **fail against the un-generalized
code**:
1. `nodeMatches` binds `vtx.building.<id>` for `(l:location*)` and does **not** bind `vtx.patient.<id>`.
2. `seedAnchorBinds` accepts a `vtx.unit.<id>` seed for an anchor written `(l:location*)`.
3. **`AnchorProjectionKey` retracts** on a `vtx.unit.<id>` **tombstone** for a `:location`-anchored lens. This is
   the over-grant test; it must be driven through a grant-shaped target, not just asserted on the key map.
4. `seedAnchorFor` returns the event key (not `""`) for a leaf-type event on a `:location`-anchored lens.

**Exhaustiveness.** A `:location` lens with an **unarmed** resolver reports `exhaustive = false` and takes the
broad filter. A `:location` lens with an armed resolver reports `exhaustive = true` on the expanded set.

**Invalidation.** (a) **Ordering**: a leaf event arriving in the window between the rule-state swap and the
consumer-filter change is **not** ack-skipped (the §6.2 hazard, asserted directly). (b) **Backfill / the
falsified precondition**: a `vtx.room.<id>` written **before** `room` is declared *is* projected after the
`subtypeOf` install — this is the test that proves §6.3's Rebuild is necessary, and it must be written so a
bare filter-update implementation fails it. (c) **Tombstone**: a `subtypeOf` tombstone retracts the leaf's rows
rather than orphaning them (row-set shrink, §6.4).

**Fail-closed gates (Fire 1).** A mutation whose key carries an abstract type segment is rejected — in vertex
root, aspect owner, **and both** link endpoint positions. A mutation whose class resolves to an abstract DDL is
rejected. An install whose `SubtypeOfRef` names a nonexistent / non-abstract / tombstoned meta fails. An
install-time cycle is refused. Each also has a **positive vector first**, so no negative test can pass for the
wrong reason.

**Cap.** A lens at `K + |leaves| = 8` narrows; at 9 it takes the broad filter and its health entry reads
`filterBroadReason = "label-cap"`. The relation ladder: assert relation-narrowed → relation-blind → broad across
the two budgets (`pipeline.go:934-935`, `:952`). A lens whose `K + leafBudget > 8` **fails its own install**.

**Corpus census test.** Extend `internal/refractor/auth_plane_narrowing_census_test.go` to pin each shipped
lens's `(expanded labels, exhaustive, filterMode)` verdict, so a taxonomy change that moves a verdict fails in a
test rather than in Capability KV. Per the sibling design's §9 finding, it **must expand read-grant walks first**
(`ExpandReadGrantWalks` runs only at `pkgmgr/manifest.go:123`, `upgrade.go:138`, `definition.go:31`, so the raw
`pkgregistry` snapshot omits the generated producers).

**Gates.** `go build ./...` · `make vet` · `golangci-lint run ./...` (cache-cleaned) · `make verify-kernel` ·
`make verify-package-*` for location-domain / service-location / loftspace-domain / clinic-domain / cafe-domain
(DDL + permissions touched) · **all** `scripts/lint-*.go` under `STRICT=1` · the **full `go test ./... -p 4`**.
The full suite is required, not optional: Fire 2 changes a matcher every lens in the corpus consumes, and Fire 3
changes a class every location-touching package reads — both are *wide-blast-radius default* changes.

## 16. Adversarial pass (run this fire, findings folded)

I ran a soundness pass over my own draft against the code, not the comments. Seven findings changed the design;
all are folded above. Recording them because the ones that changed it are the design.

1. **The brief's "three mechanisms" was an undercount, and the missing one is the dangerous one.** The draft
   inherited JetStream / `plainVertexRelevant` / `seedAnchorFor`. Grepping every label comparison turned up two
   more: `seedAnchorBinds` (`executor.go:774`) — without which site 4's narrowing is dead, because the pipeline
   offers a seed the engine refuses — and **`AnchorProjectionKey` (`anchor_delete.go:86`)**, the anchor-retraction
   gate. Missing the second on a grant-producing lens is **a grant that never revokes**. → §5.1 now enumerates
   four sites, with site 3 called out as the most dangerous and given its own test.

2. **The brief's backfill precondition is false, and the correction changes the whole invalidation design.** The
   draft accepted *"an instance cannot pre-exist its own type DDL"* and specified an in-place filter widening. I
   tried to prove the precondition and it collapsed: the type segment is a bare regex (`keys.go:141-155`), the
   mutation key is verbatim script output (`starlark_runner.go:316-370`), step 6 fails open on seven paths
   (`step6_resolve_ddl.go:202-240`), and `NoDDLForClass` gates only which script runs (`step4_hydrate.go:110-114`).
   Combined with "a filter update never resets the cursor" (`pipeline.go:926-927`), an in-place widening would
   leave a **permanent silent hole**. → §6.3: a widening triggers `Rebuild`. Which also closes the race the brief
   asked me to handle, making the event ordering irrelevant instead of merely handled.

3. **The widen ORDER is the reverse of the naive reading.** "Widen-then-verify" invites widening the server filter
   first. But `plainVertexRelevant`'s false branch **acks and drops** with no fallback
   (`pipeline.go:722-738`), while a too-broad server filter only costs work — so server-first would permanently
   lose every leaf event in the window. → §6.2: **client gate first, server filter second**, with the asymmetry
   stated.

4. **The trigger I first specified could never fire.** The draft said "extend Refractor's existing meta watch."
   `CoreKVSource` subscribes with prefix `"vtx.meta."`, which becomes the **server-side** filter
   `$KV.<bucket>.vtx.meta.>` (`corekv_source.go:395-419`, `subscribe.go:51`), and its handler ends with
   `// Unknown / link / malformed → ignore` (`:550`). A `subtypeOf` link event is structurally invisible to it.
   → §6.1: its own consumer, with the two-subject filter and the non-subset argument.

5. **Writing the expansion onto the live `CompiledRule` would race a concurrent evaluation.** The draft said
   "attach the expansion to the compiled rule." Rule state is published **copy-on-write** specifically so a reader
   cannot see a half-rewritten rule (`pipeline.go:574-581`, `:627-643`). `CompiledRule` is two fields
   (`ast.go:247-258`), so a shallow copy is cheap. → §4.3: `full.WithLabelExpansion` returns a copy; nothing
   mutates a published rule.

6. **The demand census came back empty, and I nearly reported it as supporting the design.** None of the 20 leaf
   label sites wants the abstract; `staffReadGrants` names its narrowness as *intended*. The honest first consumer
   is `capabilityServiceAccess` (`packages/service-location/lenses.go:133-145`), which today buys polymorphism by
   going **unlabeled** — paying a broad filter, no client skip, and no anchor seeding. → §2.1 and §9.4 restated
   around that one live lens, which is a stronger argument than a speculative one and is measurable.

7. **`location`'s existing DDL is named after the *abstract*, so the leaves have no meta vertex at all.**
   `packages/location-domain/ddls.go:56` declares one `meta.ddl.vertexType` with `CanonicalName: "location"`
   governing three key types. The draft treated the class rename as a cosmetic tail; it is a **prerequisite** —
   without it there is nothing for `unit` to be a taxonomy node *of*. → §3.1 and §9.1 restated; the rename is
   Fire 3's first step, not its last.

**Also checked, no change needed.** `subjects.CoreKVNarrowedFilters`' pairwise-non-subset property holds for any
label list, so a longer expanded list cannot produce the pair nats-server rejects (`subjects.go:159-169`). The
five set consumers really are plain map lookups and generalize free (§5.2). Multiple parents introduce no
ambiguity because resolution is strictly downward — but they *would* under §12.9's inheritance extension, so the
caveat is carried (§3.4). `authz_anchors` and permission `scope` are vacuously safe (§8) — I checked rather than
assumed, because "a type name never appears there" is exactly the kind of negative that turns out to have one
instance. And `resolveLensRef`'s syntactic cross-package pass-through (`build.go:517-520`) is **debt, not a
pattern** — §3.5 states explicitly that this design does not copy it.

## 17. Build notes

### 17.1 Fire A · Increment 1 — declaration surface + write-path gates (2026-08-08)

**Scope sentence (from §14 Fire A, build order item 1, verbatim):** *"Declaration surface + write-path gates.
`DDLSpec.Abstract`, `SubtypeOfRef`, `LeafBudget`; installer resolution (batch-local, then the
`checkCanonicalNameCollision` scan, fail-closed on a miss — not `resolveLensRef`'s pass-through);
`DDLCache.MetaVertexRef.Abstract` from `data.abstract`; step 6's two fail-closed gates (an abstract type segment
in any key position; a class resolving to an abstract DDL); install-time acyclicity + depth check; the
`LeafBudget` warning."*

**Scope-diff gate: narrow-only, no substitution.** Items 2–6 of Fire A (the `*` parser extension, resolution +
expansion, the trigger, the validation gate, observability) are **not** in this increment and no adjacent
mechanism stands in for them. Nothing outside item 1 is built here.

**Landing shape — increments land on `main` (§4's second sound shape), and the invariant is inertness.**
This increment is unreachable in production until Fire B declares the first abstract type: no package sets
`Abstract`, so `DDLCache` reports `Abstract == false` for every type, and both new step-6 gates evaluate their
fail-closed arm zero times. The gates are additions that fire only on a construct nothing creates. That is the
invariant keeping `main` correct across the increment boundary; it holds for items 2–3 as well (the resolver
ships **disarmed**, so §4.2 forces `exhaustive = false` — correct-but-slower — until item 4's trigger lands).

**Verified touch-list (checked live this fire; the design's citations predate `885f39be` and have drifted).**

| Target | Verified location | Design cited |
|---|---|---|
| `DDLSpec` struct + `validateAll` list | `internal/pkgmgr/definition.go:756-818`, `:30-54` | §3.5 |
| DDL→mutations loop; link-mutation precedent (`forOperation.meta`) | `internal/pkgmgr/build.go:119-173`, `:374-376` | `:106-107`, `:345` |
| `resolveLensRef` — the pass-through **not** to copy | `internal/pkgmgr/build.go:533-551` | `:504-522` |
| `checkCanonicalNameCollision` — the bucket scan to reuse | `internal/pkgmgr/installer.go:674-730` | `:645-707` |
| `MetaVertexRef` struct; `loadMetaVertex` root read | `internal/processor/ddl_cache.go:24-68`, `:217-245` | `:120-174` |
| step-6 DDL resolution + its consumer | `internal/processor/step6_resolve_ddl.go:222-266`; `step6_validate.go:156-157` | `:202-240`, `:156-157` |
| `isValidTypeSegment` (bare regex — the gap the segment gate closes) | `internal/substrate/keys/keys.go:148-162` | `keys.go:141-155` |
| `CreateMetaVertex` emits no `lnk.` key | `internal/bootstrap/meta_ddl.go:131-132`, `:162-167` | §3.5 |

**Increment order + green checks.** (a) `DDLSpec` fields + `validateAbstractDDLScope` → `go test ./internal/pkgmgr/`.
(b) `build.go` emits `data.abstract` / `data.leafBudget` on an abstract root and the `subtypeOf` link → same.
(c) installer fail-closed `SubtypeOfRef` resolution + acyclicity/depth (`maxTaxonomyDepth = 4`) + the
`LeafBudget` warning → `go test ./internal/pkgmgr/`. (d) `MetaVertexRef.Abstract` → `go test ./internal/processor/`.
(e) the two step-6 gates → same. Full gates at close: `go build ./...`, `make vet`, `golangci-lint run ./...`,
every `scripts/lint-*.go`, `make verify-kernel`, `go test ./...`.

**In-scope gotchas.**

1. **`resolveLensRef`'s NanoID pass-through is debt, not a pattern** (§3.5 says so outright). Cross-package
   `SubtypeOfRef` resolves through the `checkCanonicalNameCollision` scan and **fails the install** when the name
   does not resolve, does not name a live `meta.ddl.vertexType`, or is tombstoned. The parent need not be
   abstract (amendment A5).
2. **`LeafBudget` asymmetry (§10.2) — a leaf install is NEVER rejected**, only warned; the rejecting check is at
   the *lens's* install and belongs to a later increment. Blocking a leaf would let one package's lens veto
   another package's type declaration, which is the coupling this design removes.
3. **The segment gate must cover all four key positions** — vertex root, aspect owner, and *both* link endpoints
   (§8 rows 2–3). Covering only the vertex root leaves the endpoint restatement unguarded.
4. **`meta` and `op` are reserved** (Contract #1 §1.2) and must be refused as abstract canonical names.
5. **Abstract is mutually exclusive with `Script`/`PermittedCommands`** (§3.2) and is meaningful only on
   `meta.ddl.vertexType`.
6. The marker is **explicit** (`data.abstract`), never derived from "a vertexType with no script" — §3.2 names
   that as the accident-of-shape failure the platform has been burned by.

**Non-goals.** The `*` sigil / grammar work; `internal/refractor/taxonomy`; `WithLabelExpansion`; the four
label-equality sites; the meta-watch `lnk.meta.*` branch; the label-validation gate; `filterMode` health fields;
every `location-domain` and guard-census change (all Fire B).

**Adjacent find filed now, not later.** ANTLR toolchain state, which decides item 2's shape: the grammar is
`internal/refractor/ruleengine/full/cypher/Cypher.g4`, generated Go is **committed**, and there is **no**
`go:generate`, Makefile target, or CI step that regenerates it. `oC_LabelName` resolves to `oC_SymbolicName`
(Unicode `ID_Start`/`ID_Continue` + `Pc`), which cannot lex `*`, so `(l:location*)` is a **parse error today** —
post-processing `ln.GetText()` in the visitor (`visitor.go:239-256`, the sole `.Label` assignment) cannot reach
a token the lexer never produces. The host has `antlr` **4.13.2** while `go.mod` pins the runtime at
**4.13.1**; regenerating a 570 KB parser across a generator/runtime version skew is item 2's first decision, not
a detail. Recorded here so the next fire starts from it.
