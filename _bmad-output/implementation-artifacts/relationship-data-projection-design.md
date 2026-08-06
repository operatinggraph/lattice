# Binding a relationship in the full engine — `type(r)`, `r.key`, `r.data.<field>`

**Status: 📐 DRAFT awaiting Andrew — authored 2026-08-06 (critical-necessity session, filed-by-steward row re-examined)**

Designer fire 2026-08-06 (Winston) · Lattice lane, Refractor projection-maturity · Size **S–M** (Inc 1 S · Inc 2 S · Inc 3 XS)
· Backlog row re-examined: *[Refractor] A lens cannot project a relationship's own `data`* (planning-artifacts/backlog/lattice.md,
Component maintenance) · **the row's scope is corrected here, not merely accepted** — see §3.

---

## For Andrew

**What it does (two lines).** `traverseRel` walks a relationship and throws it away — it extends the binding
with the neighbour **node** only, so a lens can name a relationship (`-[r:boundTo]->`) but can never read
anything off it. This binds it, which makes three things projectable that are not today: the relation name
`type(r)`, the link key `r.key`, and the link's own `r.data.<field>`.

**The filed row asked for the third of those and named the weakest consumer. I am recommending a wider,
cheaper scope and a different justification.** The row's consumer (`identityCredentialBindingsRead`'s missing
`bound_at`) does not survive examination: that timestamp is **already** on a plaintext vertex and in an
encrypted aspect, and it does not solve the problem its own pane says it has (§3.1). The consumer that makes
this worth building is `objects-base`: **`DetachObject` is a shipped operation whose required `linkName`
argument no read model can supply**, so LoftSpace's Documents tab can list a document and cannot offer
"remove" (§3.2). That half — `type(r)` / `r.key` — costs **zero extra reads**: the relation name and the link
key are already sitting in the adjacency entry the walk has in hand.

**Architectural fork: none.** No new engine, no new substrate, no new plane. One binding written in
`traverseRel`, plus a resolver arm.

**Frozen-contract change: none, and none is staged.** Links already carry `data` as a universal envelope
field (Contract #1 §1.3) and link `data` is already DDL-schema-declarable there. This design adds no contract
surface and **touches no file under `docs/contracts/`** — which matters right now, since `01`, `02`, `03` and
`06` already carry four other designs' uncommitted proposals.

**One thing to know before ratifying.** Every adjacent mechanism this could have broken is already generic
and already closed: the CDC re-trigger for a link mutation is **unconditional** (not opt-in), footprint
consistency-validation is **key-shape-agnostic**, and the sweep recompute runs the same engine entry point.
I went looking for the "projection feature whose re-trigger path doesn't exist" failure and it is not here
(§5). That is the finding that moved this from *park* to *build*.

---

## 1. Grounding ledger

Every claim in this document resolves to one of these. All read on `main` at `ff9f2cc4`.

### 1.1 The gap

| Fact | Evidence |
|---|---|
| A relationship variable is parsed into the AST | `internal/refractor/ruleengine/full/visitor.go:274` — `rp.Variable = identifierText(vr)` |
| `RelPattern` carries `Variable`, `Type`, `Direction`, hops, `Properties` | `internal/refractor/ruleengine/full/ast.go:102-109` |
| `traverseRel` extends the binding with the **neighbour node only** | `internal/refractor/ruleengine/full/executor.go:1017-1036` — the output loop writes `nb[to.Variable] = n` and nothing else; `rel.Variable` is never read in the function |
| The **only** place a rel variable enters a binding is the OPTIONAL-MATCH null sentinel | `internal/refractor/ruleengine/full/executor.go:423-430` (`nullBindNewVars`) — binds `r.Variable` to `(*nodeRef)(nil)` |
| Dereferencing an unbound rel variable is **silent null**, not an error | `executor.go:1295-1298` (`*VariableRef` → `return nil, nil` when absent from the binding) → `executor.go:1300-1305` (`*PropertyAccess` → `resolveProperty`) → `executor.go:1683-1685` / `1710-1713` (`propertyOf(nil, key)` → `nil`) |
| `type(r)` by contrast is a **hard error** | `executor.go:1546` — `full engine: unsupported function %q`; the function switch handles only `collect`, `count`, `max`, `min`, `levenshtein`, `nanoidfromkey`, `coalesce` (`executor.go:1418-1546`) |
| The relation name and the link key are **already in hand** during the walk | `internal/refractor/adjacency/builder.go:21-28` — `EdgeEntry{CoreKvKey, EdgeID, Name, Direction, OtherNodeID, OtherType}`. `Name` is the relation; `CoreKvKey` is the link key |
| `EdgeEntry` carries **no `data`** | same struct — so `r.data.<field>` needs one Core-KV point-read on `CoreKvKey`; the key needs no derivation |
| A `nodeRef` is exactly `{key, props, revision}` — the shape a link document already fits | `executor.go:45-49` |
| A binding value may be any of node/scalar/list/map | `executor.go:51-54` |

### 1.2 Contract #1

| Fact | Evidence |
|---|---|
| `data` is a **universal** envelope field — vertex, aspect **and link** | `docs/contracts/01-addressing-and-envelope.md:53`, field table row `data` ("Optional type-specific payload") |
| The link envelope adds `sourceVertex` / `targetVertex` / `localName` on top of the universal fields, `data` included | `docs/contracts/01-addressing-and-envelope.md:115-139` |
| A link type's DDL may declare a JSON schema **for its link `data`** | `docs/contracts/01-addressing-and-envelope.md:244` |

Link `data` is therefore a first-class, already-contracted, already-schema-declarable place to put a fact.
Nothing in this design needs that changed.

### 1.3 The live link-data corpus

Censused across all of `packages/**` (no `.star` files exist; `cmd/**` holds only Go-side link-key
builders). **59 distinct link relations; 5 write non-empty `data`; 0 are read by any lens.**

| relation | package | mutation | `data` written |
|---|---|---|---|
| `boundTo` | identity-domain | helper `packages/identity-domain/ddls.go:780-792`; call sites `:1221`, `:1439`, `:1662` | `{boundAt}` |
| `boundTo` | identity-hygiene | `packages/identity-hygiene/ddls.go:619-623` (merge repoint) | `{boundAt}` |
| `duplicateOf` | identity-domain | `packages/identity-domain/ddls.go:1005-1009` | `{criteria}` |
| `reviewedBy` | augur | `packages/augur/ddls.go:706-707` | `{reviewedAt, verdict}` |
| `reviewedBy` | capability-author | `packages/capability-author/ddls.go:779-780` | `{reviewedAt, verdict}` |
| `appliedAs` | capability-author | `packages/capability-author/ddls.go:864-865` | `{appliedAt, installRequestId}` |
| *caller-named* (`photoOf`, `signedLeaseOf`, …) | objects-base | assembled `packages/objects-base/ddls.go:495-497`, written `:502` via `link_ensure_alive` | `{filename}` when supplied, else `{}` |

The other 54 relations carry `{}`. Only three relations have a registered `meta.ddl.linkType` with a data
schema, all in identity-domain (`packages/identity-domain/ddls.go:459-482`, `:484-506`, `:508-536`).

**Read this table twice.** Every non-empty payload is a **provenance/audit** fact — who reviewed it, when a
binding was made, which criteria matched, what the file was called. Every purely topological edge (ownership,
containment, assignment, ledger-anchor, offering-wiring) carries `{}` and needs nothing from this design.
That is the shape of the demand: small, specific, and about provenance.

### 1.4 The two named consumers

| Fact | Evidence |
|---|---|
| `identityCredentialBindingsRead` shipped with 4 columns and no `bound_at`, and its own comment states this row's premise | `packages/identity-domain/lenses.go:86-107` |
| Its spec binds no relationship at all — `-[:boundTo]->`, anonymous | `packages/identity-domain/lenses.go:172-179` |
| `objectAttachmentsSpec` **does** bind a rel variable, in a shipped lens | `packages/objects-base/lenses.go:152` — `OPTIONAL MATCH (o)-[r]->(owner)` |
| …and its author documented the blocked feature in place of using it | `packages/objects-base/lenses.go:132-135` — the relationship name "is NOT projected — the full engine cannot project `type(r)`… Detach of a listed doc (which needs the linkName) is therefore a documented follow-up" |
| `objectLivenessSpec` binds `r` the same way | `packages/objects-base/lenses.go:98` |
| `DetachObject` is a **shipped** op whose payload requires `linkName` | `packages/objects-base/ddls.go:74`, `:83`, `:114` ("AttachObject/DetachObject: the relationship localName… Caller-supplied"), worked example `:191-198` |
| The app reconstructs the link key in Go because the read model cannot project it | `cmd/loftspace-app/objects.go:70-78` (`objectLinkKey`) |
| …and the row it serves to the FE carries neither `linkName` nor `filename` | `cmd/loftspace-app/objects.go:38-55` (`attachmentRow`), `:62-68` (`documentView`) |
| Detach is submitted browser-direct, so the **browser** is what needs `linkName` | `cmd/loftspace-app/objects.go:137-139` |
| **No lens anywhere in the repo dereferences a bound rel variable** | repo-wide grep of non-test lens specs: the only bindings are objects-base's two `-[r]->`, both used solely as walk plumbing |

### 1.5 `bound_at` is already duplicated onto a vertex — three times

| Fact | Evidence |
|---|---|
| "credential A signs in as person U" has **four** representations, and `boundAt` is in three | `_bmad-output/implementation-artifacts/credential-binding-plane-lifecycle-design.md:66-72` |
| …one of which is a **plaintext vertex**: `vtx.credentialindex.<sha256NanoID(A)>` → `{actorKey, identityKey, boundAt}` | same, row 2 |
| The DDL says so verbatim | `packages/identity-domain/ddls.go:527` — `boundAt` is "the same instant the credentialBinding entry and the credentialindex vertex carry" |
| `credentialindex` is **not walk-reachable**: no link is ever written to or from it | link census §1.3 — `indexes` links originate at `identityindex`, a different type |
| …and the engine has no hash function, so a lens cannot derive its key from `c.key` | function switch `executor.go:1418-1546` (no `sha256`/`hash` arm) |

---

## 2. The gap, stated once

`traverseRel` (`executor.go:901-1038`) takes a `RelPattern` and returns bindings. It uses `rel.Type` to
filter edges, `rel.Direction` to match direction, and `rel.MinHops`/`MaxHops` to bound the walk. It never
looks at `rel.Variable`. The relationship is a filter, never a value.

Everything downstream follows from that one omission, and — this is the point — **all three unavailable
things are the same omission**, not three features:

- `type(r)` — the relation name. Already in `EdgeEntry.Name`.
- `r.key` — the link key. Already in `EdgeEntry.CoreKvKey`.
- `r.data.<field>` — the link's payload. Needs a point-read on `EdgeEntry.CoreKvKey`.

Splitting these into separate rows would be the false fork: one binding site serves all three, and any
design that delivers one has already done the work for the others.

Two asymmetries worth naming, because they shape the increments:

1. **The first two are free.** The walk has already read the adjacency document; `Name` and `CoreKvKey` are
   sitting in the `EdgeEntry` it is iterating (`executor.go:957`). Projecting them adds no read, no
   footprint entry, no cost to any lens.
2. **The failure modes differ.** `type(r)` errors loudly today (`executor.go:1546`). `r.data.x` returns
   **null, silently** (§1.1). An author who writes the second gets a column of nulls and no diagnostic —
   which is precisely how `objects-base`'s author ended up writing a comment explaining the limitation
   instead of getting an error. Inc 3 closes that.

---

## 3. Necessity — and the correction to the filed row

The mandate for this session was to treat "add engine capability" as guilty until proven necessary. The
filed row does not clear that bar on its own evidence. A different consumer does.

### 3.1 The filed row's consumer does not justify it

The row names `identityCredentialBindingsRead`'s missing `bound_at`. Three findings against it:

**The value is already on a plaintext vertex.** `vtx.credentialindex.<sha256NanoID(A)>` carries
`{actorKey, identityKey, boundAt}` in plaintext Core KV (§1.5), and `vtx.identity.<U>.credentialBinding`
carries `boundAt` in the encrypted aspect. The row's framing — *"Every relationship fact therefore has to be
duplicated onto a vertex to be projectable"* — describes a duplication that **already exists and was not
created for projectability**: representation 2 exists to enforce the one-credential-≤-one-identity guard.
The duplicate is load-bearing state that stays whether or not this design ships. There is no tax to remove.

**`bound_at` does not solve the problem its own pane states.** `packages/edge-manifest/panes.go:68-72`
explains that `binding_id` (a raw NanoID) takes the `title` role because `boundAt` is unprojectable, and that
it "is the only thing on the row that identifies WHICH sign-in method this is." But a timestamp does not
identify which sign-in method either — a person with two Google credentials bound a minute apart gets two
timestamps and still cannot tell them apart. The pane's real need is a **provider/method label**, which lives
in `.idpBinding` / `credentialBinding`, not in the link. `bound_at` is a subtitle nicety for that pane, not
the fix it is filed as.

**The generalisation in the row is false for 54 of 59 relations.** They carry `{}` (§1.3). There is nothing
to duplicate, so nothing to relieve.

**Conclusion:** were `identityCredentialBindingsRead` the only consumer, the honest verdict would be *park —
demand-gated*. I would have written no doc. It is not the only consumer.

Two further notes, so the scope is not accidentally widened later:
- The sibling design `credential-binding-plane-lifecycle-design.md` (📐 awaiting ratification) **does not
  depend on this row** and does not plan a `bound_at` column: it considered and dispositioned a re-anchor of
  this lens (`:647`, ground **G9** — "`traverseRel` reads the neighbour node either way"). There is no fold
  target there. It also puts `credentialindex` into an erasure set under its recommended §7 option B, which
  makes representation 2 a *shrinking* projection source, not a stable one.
- Reaching `credentialindex` from this lens without this design would need a cross-product scan-join
  (`MATCH (c)-[:boundTo]->(u), (x:credentialindex) WHERE x.data.actorKey = c.key`) — expressible, but a
  whole-type scan per evaluation, on a source the sibling design is trying to erase. Not recommended.

### 3.2 The binding consumer: `objects-base` / LoftSpace detach

This is the case that makes the item real, and the filed row does not mention it.

`DetachObject` is **shipped**. Its payload requires `linkName` — the relationship's own local name, the
upload "slot" (`packages/objects-base/ddls.go:114`). It is submitted **browser-direct**
(`cmd/loftspace-app/objects.go:137-139`), so the browser is what must know `linkName`.

`objectAttachmentsSpec` is the P5-clean read model that Documents tab reads. It **binds `r`**
(`packages/objects-base/lenses.go:152`) and cannot project anything off it, so `linkName` never reaches the
row (`cmd/loftspace-app/objects.go:38-55`, `:62-68`). The author wrote the consequence into the lens file
rather than shipping the feature: *"Detach of a listed doc (which needs the linkName) is therefore a
documented follow-up"* (`packages/objects-base/lenses.go:132-135`).

So the platform is in this state: **a working operation, a working read model, and no way to pass the
argument from one to the other.** The Documents tab lists a document and cannot offer "remove". That is a
present-tense functional gap in a shipped vertical app, not a projection nicety — and it is closed by the
*free* half of this design.

The same lens also wants `r.data.filename` — `AttachObject` writes it
(`packages/objects-base/ddls.go:495-497`) and no reader has ever been able to see it, which is why the
Documents tab shows no filename. Two fields, one consumer, one primitive.

Note what is *not* being claimed: the Go-side `objectLinkKey` helper
(`cmd/loftspace-app/objects.go:70-78`) is not a defect. It is the honest workaround available today, and it
works on the attach path where the client chooses the slot. It cannot work on the detach path, because there
the slot must come *back* from the graph.

### 3.3 What the corpus says about future demand

Three more provenance payloads are written and unreadable today: `duplicateOf.data.criteria`,
`reviewedBy.data.{reviewedAt, verdict}` (two packages), `appliedAs.data.{appliedAt, installRequestId}`
(§1.3). None has filed demand, and none is used to justify this design — I am counting them as evidence that
the idiom "put provenance on the edge" is already how this codebase models these facts, not as consumers.

The count that matters: **two independent shipped lenses already bind a rel variable** (`objects-base`,
twice), and **one shipped operation cannot be fed** because of the gap.

---

## 4. Alternatives, weighed honestly

**(a) Do nothing — keep the duplicate-onto-vertex idiom.** Genuinely defensible for the credential case
(§3.1), and for 54 of 59 relations there is nothing to decide. It fails on `objects-base`: `linkName` is
**not duplicable onto a vertex**, because it is not a fact *about* either endpoint — it is the identity of
the edge itself. An object attached to the same owner under two slots has two links and one object vertex;
there is no vertex field that can hold "which slot". Duplicating it would mean inventing a per-link vertex,
which is a worse model than reading the link. **Rejected on `objects-base`, not on the credential case.**

**(b) Narrow fix — bind the relationship; project `type(r)`, `r.key`, `r.data`.** Recommended. §5 shows every
soundness edge is already closed; §7 splits it so the free half ships first.

**(c) Full relationship semantics — `WHERE` on rel fields, rel in grouping keys, `r` as a returned entity,
variable-length path collections.** **Rejected for now.** `WHERE r.data.x = …` would need the walk's edge
filter to become predicate-driven, which changes what `recordEdgeSelector` can honestly certify
(`executor.go:864-889`): the selector footprint records `(RelType, Direction)` pairs and matched edge IDs, and
a data-predicate filter is not expressible as a selector, so it would degrade every such lens to
`Fallback` whole-document comparison. A rel in a grouping key needs `normalizeForKey` to render a link
injectively — doable, but no consumer asks. Variable-length `r` (a *list* of relationships) is a different
type in the binding and has no consumer at all. **None of this is needed by either named consumer, and (b)
does not foreclose it** — binding the relationship is the precondition for all of it.

**(d) Fold into a sibling design.** Considered and rejected. `link-aspect-triggered-reprojection-plain-lenses-design.md`
is **🗄️ SUBSUMED** ("do not build from this doc") and its Fire 1 already shipped — it is the reason §5.1 is
already closed, not a fold target. `credential-binding-plane-lifecycle-design.md` does not depend on this and
plans no `bound_at` column (§3.1). And the owner is wrong either way: this is Refractor engine work, not
identity-domain or objects-base package work.

---

## 5. Soundness edges — all four already closed, generically

This is the section that changed the verdict. The brief's warning was right in principle: *a projection
feature whose re-trigger path doesn't exist is a converged-but-wrong generator*. I went looking. It exists.

### 5.1 CDC re-trigger on a link mutation — exists, and is unconditional

`internal/refractor/pipeline/pipeline.go:1624-1644` dispatches `substrate.KindLink` to
`evalPlainLinkReprojection` on a plain lens (and `evalLinkFanOut` on an actor-aware one).
`evalPlainLinkReprojection` (`pipeline.go:1799-1875`) re-executes from **both** endpoint vertices and
deduplicates. Its own doc comment states that "a link tombstone… and a link create take the same evaluate
path — the re-execute reads current adjacency either way."

Three properties matter here:

- It is **not opt-in.** Every plain lens gets it. The gate is relevance only:
  `plainLinkReactsTo(linkName)` (`pipeline.go:712-721`) plus `plainReactsTo` on either endpoint type.
- It fires on a **data-only update**, not just create/tombstone. The arm keys off `substrate.KindLink` and
  computes `isDeleted` from the body (`pipeline.go:1818-1832`); an update carrying new `data` is an ordinary
  non-deleted event and re-executes.
- Both of `objects-base`'s rel-binding lenses use an **untyped** `-[r]->`, so `plainLinkReactsTo` returns
  true unconditionally for them (`relationsExhaustive` is false — `pipeline.go:716`). They cannot be missed.

**So (b) does not require the link-aspect reprojection design to land first.** It landed.

### 5.2 Footprint / evaluation-consistency validation — closes itself, zero new code

This is the edge I expected to have to design around, and it is already generic:

- `footprint()` (`executor.go:280-297`) builds `NodeRevisions` by iterating **`ex.nodes`** — the memo keyed by
  whatever key `fetchNode` was called with. No key-shape classification.
- `footprintValid` (`internal/refractor/pipeline/evaluate.go:341-350`) re-reads **every** `NodeRevisions`
  entry via `currentNodeRevision`.
- `currentNodeRevision` (`evaluate.go:420-439`) is a plain `coreKV.Get(ctx, key)` with no key-shape gate, and
  it maps `isDeleted: true` → revision `0` — exactly the right semantics for a tombstoned link.

A link document read through `fetchNode(e.CoreKvKey)` therefore enters the footprint and is
revision-validated **by construction**. A concurrent write to a projected link's `data` is detected by the
existing seam. The `EvalFootprint` doc comment says "vertex or aspect" (`ruleengine.go:88-90`); that is a
comment about today's callers, not a constraint in the code — Inc 2 updates the wording to match.

### 5.3 Narrowing / `ReferencedLabels` — unaffected

Binding a relationship changes no relation the walk traverses and no label it binds. `plainLinkReactsTo`
gates on the relation **name** (`pipeline.go:712-721`), which a variable does not alter; `labels.go:39-40`,
`:102-106` track **node** labels only. `recordEdgeSelector` (`executor.go:864-889`) is likewise driven by
`rel.Type` and `rel.Direction`, both untouched. Inc 1 and Inc 2 add no selector, no label, and no
`Fallback` degradation.

### 5.4 Sweep recompute parity — automatic

`Sweeper.pass` → `Pipeline.Reproject` (`internal/refractor/pipeline/reproject.go:98`) →
`reprojectActors`, which lives in `evaluate.go:849` — the same shared evaluate path as the CDC arms, running
the same `full.Engine.ExecuteWithFootprint`. Any read performed inside the engine is performed identically on
the sweep recompute, so a swept row and a CDC-projected row cannot disagree. No sweep-side change.

### 5.5 The one real cost, sized

`adjacency.EdgeEntry` carries no `data` (`builder.go:21-28`), so `r.data.<field>` costs **one Core-KV
point-read per traversed edge that a lens actually dereferences**. Sized honestly against the
just-closed Refractor footprint-reduction campaign:

- **Inc 1 (`type(r)`, `r.key`) costs nothing.** Both values are already in the `EdgeEntry` the loop holds
  (`executor.go:957`). This is the increment that unblocks detach.
- **Inc 2 is opt-in by syntax.** The read fires only when a rel variable is bound **and** a property other
  than `key` is dereferenced. Every one of the 59 existing relations and every existing lens pays zero,
  including `objects-base`'s two, which bind `r` but touch no field.
- The key needs no derivation and no scan — `CoreKvKey` is the link key.
- The read goes through `fetchNode`, so it is **memoized per evaluation** (`executor.go:782-796`): a
  hub link re-walked in two clauses is read once.

The honest statement for the board: this raises the read surface of a lens **that opts in**, by one point-read
per dereferenced edge, in exchange for making the fact readable at all. It lowers nothing. A lens author who
does not name a relationship is unaffected.

---

## 6. Contract surface

**None.** No file under `docs/contracts/` is touched by this design, and none needs to be:

- Link `data` is already a universal envelope field (`01-addressing-and-envelope.md:53`, `:115-139`).
- Link `data` is already DDL-schema-declarable (`01-addressing-and-envelope.md:244`).
- The lens-spec surface (what Cypher a lens may write) is **not** a frozen contract.
- The relation name and link key this projects are derived entirely from the Contract #1 key shape already
  parsed by `substrate.ParseLinkKey`.

This is deliberate and worth stating plainly given the current tree: `docs/contracts/01`, `02`, `03` and `06`
each already carry another design's uncommitted proposal. Adding a fifth would make all five ambiguous at
ratification. This design adds nothing there.

Doc updates (not contracts): `docs/components/refractor.md` gains the projectable-relationship forms, and
`internal/refractor/ruleengine/ruleengine.go:88-90`'s `NodeRevisions` comment widens from "vertex or aspect"
to include a link key.

---

## 7. Increments

Ordered so the feature that unblocks a shipped app ships first, and so each increment is independently
green and independently useful.

### Inc 1 — bind the relationship; project `type(r)` and `r.key` (S, zero new reads)

1. In `traverseRel` (`executor.go:901-1038`), thread the `adjacency.EdgeEntry` that produced each match
   through to the output loop, and when `rel.Variable != ""` bind it alongside `to.Variable`
   (`executor.go:1031-1034`). Bind it as the existing `nodeRef` shape (`executor.go:45-49`) —
   `key: e.CoreKvKey`, `revision: 0`, `props` seeded with the relation name — so every downstream consumer
   (`resolveProperty`, `normalizeForKey`, DISTINCT rendering, the null-sentinel checks) works unchanged.
   **`props` is NOT the link body in this increment** — no read is added here.
2. Add a `type` arm to the function switch (`executor.go:1418-1546`), returning the bound relationship's
   relation name; error on a non-relationship argument, and return `nil` for the OPTIONAL-MATCH null
   sentinel (mirroring `nanoIdFromKey`'s and `levenshtein`'s nil handling).
3. `r.key` resolves through the existing `resolveProperty` `key` arm (`executor.go:1690-1692`) with no change.
4. **Constrained-target parity:** the already-bound-variable check at `executor.go:1023-1029` currently
   guards `to.Variable` only. Apply the same guard to `rel.Variable`: a rel variable already bound in this
   binding must resolve to the same link key, or the expansion is dropped. Without this, reusing a rel
   variable across clauses would silently over-match.
5. Variable-length hops (`MinHops`/`MaxHops` ≠ 1) **must fail closed**: a multi-hop expansion has no single
   relationship to bind, so reject at parse/validate with a clear message rather than binding the last hop.
   Note `minHops == 0` admits `from` itself with no edge traversed at all (`executor.go:930-938`) — that path
   binds no relationship and must be caught by the same gate.

*Unblocks:* `objectAttachmentsSpec` projects the slot name, LoftSpace's Documents tab can offer detach.

### Inc 2 — project `r.data.<field>` (S, one point-read per dereferenced edge)

1. In `resolveProperty` (`executor.go:1682-1707`), when the target is a relationship binding and the key is
   neither `key` nor the relation name, point-read the link document via `fetchNode(r.key)` and resolve the
   property off its body — the same path an aspect reference already takes at `executor.go:1700`, so
   memoization and the `errCoreKVReadDisabled` read-free mode (`executor.go:1697-1699`) are inherited.
2. A tombstoned or absent link resolves to `nil` (Cypher missing-property semantics), matching
   `executor.go:1704-1706`.
3. Widen `ruleengine.EvalFootprint.NodeRevisions`' doc comment (`ruleengine.go:88-90`) to include a link key.
   No behavioural change — §5.2.

*Unblocks:* `r.data.filename` for the Documents tab; `bound_at` for `identityCredentialBindingsRead` if
anyone still wants it; `duplicateOf.criteria`, `reviewedBy.verdict`, `appliedAs.installRequestId` for free.

### Inc 3 — close the silent-null authoring hazard (XS, fail-closed)

Reject at lens **validate** time any dereference of a relationship variable the walk cannot bind — the
variable-length case from Inc 1.5, and (before Inc 2 lands) a `data` dereference. Today
`r.data.x` returns a silently-null column with no diagnostic (§1.1), which is how the objects-base
limitation got discovered by comment rather than by error. Default-deny with a message naming the variable
and the reason, consistent with `nanoIdFromKey`'s fail-closed posture (`executor.go:1499-1524`).

Ship this **with Inc 1**, not after: the moment a rel variable becomes bindable-in-principle, the set of
"bound but unprojectable" shapes needs a gate, and a lint/validation gate is never an optional follow-on.

### Gates

`go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`,
`STRICT=1 go run ./scripts/lint-conventions.go`, `go run ./scripts/lint-package-standard.go`, every
`scripts/lint-*.go` gate, and — because this changes a shared engine path that every lens in the corpus
runs through — the **full** `go test ./...`, not just the refractor packages.

---

## 8. Tests

Colocated with the mechanism, per house rules.

**`internal/refractor/ruleengine/full` (engine semantics)**
1. `type(r)` on a typed single-hop walk returns the relation name; on an untyped `-[r]->` it returns the
   actual traversed relation, not `""`.
2. `r.key` equals the full Contract #1 6-segment link key, byte-for-byte against a fixture-written link.
3. `r.data.<field>` resolves a written payload; a link with `data: {}` resolves `nil`; an absent/tombstoned
   link resolves `nil`.
4. **OPTIONAL MATCH null parity:** a zero-match `OPTIONAL MATCH (a)-[r:x]->(b)` leaves `type(r)`, `r.key` and
   `r.data.f` all `nil` and does **not** error — the `nullBindNewVars` sentinel path
   (`executor.go:423-430`) already binds `r`, and it must stay indistinguishable from a real-match null.
5. **Constrained-target parity (Inc 1.4):** a rel variable reused across two clauses matches only when both
   resolve to the same link key. Written to **fail without the guard**.
6. **Variable-length fails closed (Inc 1.5):** `-[r:x*1..3]->` and `-[r:x*0..]->` are rejected at
   parse/validate with a message naming the variable. Both bounds, plus the `minHops == 0` zero-edge case.
7. **DISTINCT / grouping does not collapse distinct links:** two links between the same endpoint pair under
   different relation names project two distinct rows — the `normalizeForKey` hazard the existing node case
   documents at `executor.go:1228-1234`.
8. **Read count is zero for Inc 1:** a lens binding `r` and projecting only `type(r)`/`r.key` performs no
   more Core-KV reads than the same lens with an anonymous relationship. Asserted on a counting KV, so the
   §5.5 cost claim is a test, not a comment.
9. **Memoization:** a link traversed in two clauses is read once (Inc 2).

**`internal/refractor/ruleengine/full` (footprint)**
10. A lens projecting `r.data.f` puts the **link key** in `EvalFootprint.NodeRevisions` at the observed
    revision — the §5.2 claim, asserted directly rather than inferred.
11. `EdgeSelectors`/`Fallback` for that lens are byte-identical to the same lens with an anonymous
    relationship — §5.3, no selector degradation.

**`internal/refractor/pipeline` (liveness — the converged-but-wrong guard)**
12. **A `data`-only link update reprojects the row.** Write a link, project `r.data.f`, then update **only**
    the link's `data` and assert the projected row's value changes with no vertex or aspect touch. This is
    the §5.1 claim and the single most important test in this design: it is what proves the feature is not a
    converged-but-wrong generator. Verified to fail if the `KindLink` arm is stubbed out.
13. A link tombstone retracts / nulls the projected field.
14. **Sweep parity:** a swept recompute of the same row produces a body byte-identical to the CDC-projected
    one (§5.4).

**Consumers (Inc 2, once green)**
15. `objects-base`: `objectAttachmentsSpec` projects the slot name per attached owner; a two-slot object
    yields two distinguishable entries. Plus `filename` when `AttachObject` supplied one, `nil` when not.

---

## 9. Non-goals

- **`WHERE` on relationship fields**, a relationship in a grouping key, a relationship as a returned entity,
  and variable-length relationship **lists** — alternative (c), §4. Explicitly deferred, with the selector-
  footprint reason recorded there so the next designer does not re-open it blind.
- **A `bound_at` column on `identityCredentialBindingsRead`.** Inc 2 makes it *possible*; this design does
  not recommend adding it. §3.1 shows the pane needs a provider label, not a timestamp. That is a
  `packages/identity-domain` + `edge-manifest` decision, on its own row, after this lands.
- **Retiring `cmd/loftspace-app`'s `objectLinkKey` helper** (`objects.go:70-78`). Still correct on the attach
  path. Whether the detach path reads the projected `r.key` instead is an app-side follow-on.
- **Backfilling link `data` onto the 54 empty-payload relations.** Nothing asks for it, and §1.3's reading is
  that topological edges are correct to carry `{}`.
- **Any change to `docs/contracts/*`** — §6.
