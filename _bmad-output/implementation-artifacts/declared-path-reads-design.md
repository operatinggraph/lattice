# Declared-path reads — the declaration channel for a key that does not exist until a traversal runs

**Status: 📐 awaiting-Andrew (ratification)** · Designer fire 2026-09-01 · Winston
**Frozen-contract edits staged UNCOMMITTED in `main`:** `docs/contracts/02-operation-envelope.md` §2.5
(class (e) wording; new class (h) + its declared-path paragraph; ceiling + replay-stability sentences) and
`docs/contracts/10-orchestration-loom.md` §10.5 (the substantive clause — an externalTask step may declare
subject-rooted paths). No architectural fork.

> **This document was re-derived at its own adversarial close.** The draft put the declaration on the
> **owning DDL** with a `{payload.<field>}` root. Two cold passes broke that shape on three blocking
> counts — the root was submitter-supplied, the DDL meta is per-**class** and the class is an envelope
> field, and the one fact used to reject the alternative was false. The superseded reasoning is **rewritten
> in place**, not banner-ed over; §11 records what was tested and what it changed. The shape below —
> **Loom declares a subject-rooted path, the Processor resolves it** — is the alternative the draft
> rejected, and it is better on every count that broke the draft.

**Board rows consolidated (filing gate (1) — one shared missing primitive, not two):**
- `[Processor] A walk whose hub is link-discovered cannot be declared on any surface` (★★, M–L,
  `no-pattern: chained/link-discovered enumeration hub declaration`)
- `[Loom] Sensitive-egress reads can't reach a link-discovered aspect` (★★, M,
  `no-pattern: link-discovered egress`)

**Unblocks:** `verticals.md` — *The executed lease still doesn't name its tenant* (LoftSpace, ★★,
`🚧 blocked-on` the second row).

---

## For Andrew

**What this is, in three lines.** Every read-declaration surface the platform has resolves the key it
declares **at dispatch time**. A key reached by *following a link* does not exist at dispatch time, so it
is undeclarable by construction on all of them. Two filed rows are that one hole seen from two sides: a
**walk** whose hub is link-discovered, and a **sensitive aspect on a linked vertex** that an external
adapter needs. The fix is one primitive — **the orchestration surface that owns the subject declares an
unresolved PATH; the Processor resolves it before hydration** — plus, for the walk side, one word of
contract and one guard clause.

**Why it is here and not Winston-adjudicated.** It edits two frozen contracts. Both edits are staged
uncommitted in `main`; the diff is the proposal.

**Three things want your attention. None is a fork; the third is a correction I owe you.**

**(i) A ratchet loosening you may not want (Inc 1).** Admitting a chained walk on reachability means a
*new relation* walked on an already-reached vertex lands silently, where today it fails and gets a
baseline row. In exchange, part of the drift baseline's 47 structurally-undeclarable walk rows (of 106)
stop being permanent unpayable debt. I lean to taking it — the read arm has worked this way since it
shipped, and a ledger where 44% of rows can never be paid is not a ledger — but **Inc 1 is independently
droppable and Inc 2 does not depend on it.** §5.2 also records a real defect in the *retirement* half that
the build must prove around, so if you want the ratchet kept tight, dropping Inc 1 costs the design
nothing.

**(ii) Who may name a linked-vertex sensitive aspect (Inc 2).** The answer this design lands on is: **only
a surface whose subject the platform itself resolved** — in practice, a Loom pattern step, rooted at the
pattern's own subject. Not the submitter, and not a package field the submitter can steer. That is a
tighter rule than what exists today (see (iii)), and it is the whole security content of Inc 2.

**(iii) A correction to how I first priced this, which you should have.** I told this design's own first
draft that today's egressable set is "the set some dispatcher named", and framed Inc 2 as a widening of
it. **That was wrong in the unsafe direction.** `contextHint.egressReads` is submitter-supplied wire,
`opwire.ParseEnvelope` validates only count and list-disjointness, and step 3 never inspects `contextHint`
at all (`internal/processor/step3_*`: zero references). Any actor holding any non-NFR-S6 operation grant
can already name any identity's sensitive aspect there; what actually contains it on the one live consumer
is a **package-authored actor guard in the script** (`packages/lease-signing/leasedoc_scripts.go:163-165`),
not a platform rule. So Inc 2 is not a widening at all — it adds a *narrower* channel beside an open one.
**The open one is a separate finding this fire surfaced and does not close**; it is filed as its own row
rather than folded in, because closing it is an authorization-plane change with its own blast radius.

**What I am NOT proposing.** No new Starlark builtin. No new envelope field and no wire change. No
submitter-supplied path root. No change to any crypto or ref-provenance rule. No general-purpose
`pathReads`, and no path grammar on the descriptor or Weaver surfaces — only where a platform-resolved
subject exists, because that is the only place the root can be trusted.

---

## 1. The demand, clause by clause

### 1.1 Row A — the undeclarable walk

> *"Every declaration surface resolves its hub at dispatch time, so a hub that exists only after a prior
> read (`vtx.building.<id> containedIn out`, reached from a unit's `containedIn` target) is undeclarable by
> construction — the residue the read-drift baseline's walk rows now measure."*

- **"every declaration surface resolves its hub at dispatch time"** — confirmed, four surfaces, four
  independent resolvers (§2.2). This is the load-bearing fact and it survives grounding.
- **"undeclarable by construction"** — confirmed for the *hub*. **But the row's own prescription —
  `no-pattern: chained/link-discovered enumeration hub declaration` — is solution-shaped, and grounding
  dissolves it.** The platform already sanctions this exact reachability for *reads*; it is only the *walk*
  arm that refuses it (§2.3). Row A needs no new grammar at all. See §4 row 2.
- **"the residue the walk rows measure"** — 106 walk rows today (103 when this fire's census ran — the corpus moves), of which 1 is actor-role and the rest are the
  population §5.6 of the prior design left uncounted. **This fire ran that census (§6.1): 47 class B
  (link-discovered, undeclarable), 55 class A (payload-rooted, declarable through the shipped
  `OpDispatchSpec.Enumerations` and therefore ordinary debt), 0 ambiguous.** Eleven distinct chain shapes;
  maximum depth 4 hops.

### 1.2 Row B — the unreachable sensitive aspect

> *"`subject.<aspect>` egress declaration only covers the SUBJECT's own key (known at Loom dispatch time);
> a value discovered by walking a link inside the DDL script has no declaration path. A
> retention-class-custody workaround is also refused at the egress boundary."*

Every clause confirmed, and the code says so about itself:

> *"the applicant's and landlord's `.name` aspects are deliberately NOT read here … a link-discovered
> sensitive aspect has no `contextHint.egressReads` declaration path (Loom cannot pre-declare a key this
> DDL only resolves at execute time via `live_link_target`/`live_link_source`)."*
> — `packages/lease-signing/leasedoc_scripts.go:22-25`

The live consumer: LoftSpace's executed lease renders `Tenant: vtx.identity.edu97ix…` instead of a name.
`CreateLeaseDocInstance` walks `applicationFor` at execute time
(`packages/lease-signing/leasedoc_scripts.go:221`) to find the applicant, and then cannot declare that
identity's sensitive `.name`.

**The important grounding result: the crypto boundary is not the blocker.** The applicant's `.name` lives
on `vtx.identity.<applicant>` and is therefore **identity-held** — exactly what the egress boundary admits
(`internal/processor/sensitive_decrypt.go:381-393`). Nothing about custody, Vault, refs, MACs or the bridge
needs to change. **The only thing missing is the ability to name the key.**

---

## 2. Grounding ledger

Every row cites the code that *does* the thing, never a comment that describes it.

### 2.1 The egress path is open; only the declaration is shut

| Fact | Citation |
|---|---|
| A sensitive aspect declared in `egressReads` hydrates as a MAC'd `$sensitiveRef`, never plaintext | `internal/processor/sensitive_decrypt.go:238-269` |
| The egress boundary refuses a **non-identity** key holder, at mint and again at the bridge | `internal/processor/sensitive_decrypt.go:381-393`; `internal/bridge/egress.go:180-193` |
| A sensitive aspect on a **non-identity** vertex can only be custodied on a retention class — `CustodyKindIdentity` derives the holder from the aspect's own key and returns `false` unless `vertexType == "identity"` | `internal/processor/step65_encrypt.go:221-244` |
| ⇒ the fire brief's "snapshot the name onto the leaseapp" workaround **cannot be rescued** by choosing a different custodian, and its refutation is structural, not incidental | the two rows above, together |
| ⇒ but `vtx.identity.<applicant>.name` is identity-held and egress-legal **today**; the blocker is naming it | same |
| Step 4 hydrates `reads` + `optionalReads` + `egressReads` from ONE atomic `KVGetMulti` | `internal/processor/step4_hydrate.go:271-288` |
| Loom infers `egressReads` from `params` templates, Loom-side, with no Core-KV read | `internal/loom/externaltask_params.go:42,79-83` |
| The template grammar accepts exactly `subject.data.<field>` and `subject.<aspect>.data.<field>`; a link-walk-shaped path is `errMalformedGuard` | `internal/loom/guard.go:393-403` |
| The refusal is pinned by tests today | `internal/processor/sensitive_decrypt_keyid_test.go:148`; `internal/bridge/egress_test.go:220` |

### 2.2 Four declaration surfaces, four independent resolvers, one shared property

| Surface | Resolver | Hub/root grammar |
|---|---|---|
| Loom `StepSpec` | `internal/loom/pattern.go:113` `resolveSubjectTemplate` | `subject`, `subject.<aspect>` |
| Loom externalTask `params` | `internal/loom/guard.go:403` `parseGuardPath` | `subject.data.<f>`, `subject.<aspect>.data.<f>` |
| Weaver `GapActionSpec` | `internal/weaver/strategist.go:683` `resolveRowTemplate` | `row.<column>`, literal |
| `pkgmgr.OpDispatchSpec` | `internal/pkgmgr/opdispatchtemplates.go:96,140` | `{actor}`, `{payload.<field>}`, literal (hub: narrowed) |

`EnumerationSpec`'s own doc states the design-of-record — *"Hub's template grammar belongs to the surface
carrying it"* (`internal/pkgmgr/definition.go:571-578`). **The shared property is that every one of them
resolves the key before the operation runs.** That is not an oversight in four places; it is one
structural fact.

**But the four differ on something the draft treated as uniform, and it decides the design: where their
root comes from.** Loom's `subject` is resolved by the *engine* from the pattern anchor. Weaver's
`row.<column>` comes from a *violation row* the engine produced. A descriptor's `{payload.<field>}` and a
hand-built envelope's literal come from the *caller*. Only the first two are platform-owned — and only
Loom's is a vertex key the platform will still own after an arbitrary number of hops from it. That
asymmetry is why the path declaration goes on the Loom step and not on a fifth surface of its own.

### 2.3 The platform already sanctions traversal-reachability — for reads, not for walks

The read-drift guard admits a live read whose vertex root is in `record.EnumeratedVertices` — the far
endpoints this execution's own walks discovered, hubs deliberately excluded
(`internal/testutil/read_drift_guard.go:96-115`; `internal/processor/script_read_record.go:183-188`). The
identical principle applied to the **walk** arm would admit a walk hubbed on a discovered vertex — and the
walk arm has no such clause (`read_drift_guard.go:125-140`). Its own failure message already names the
class it cannot admit: *"a link-discovered hub — one only a prior read resolves, so no dispatcher can name
it up front"*.

Contract #2 §2.5 class (e) has the same asymmetry in prose: it covers `kv.Links` *"and the data-dependent
per-element `kv.Read`s keyed off its results"* — reads, not further enumerations. **That one word is Row
A's entire fix.**

### 2.4 The class-(g) precedent, and where it stops

Class (g)'s `derive_reads` is the precedent for *"declared beside the script, because no dispatcher can
express it"* (`internal/processor/compiled_script.go:47-49,112-131`), and it is a **function** only
because its input is the payload. A path's input is the topology, so a path is **static data** — better,
because it is install-validatable and readable without executing anything.

**Where the precedent stops, and it is the whole design.** `derive_reads` lives beside the script and is
safe there because its output is a pure function of an input the operation already carries. A path's
output depends on a **root**, and a root is a value someone must supply. The DDL meta cannot supply a
trustworthy one: it hangs off the **class** (`internal/processor/ddl_cache.go:738,793,842,888`),
`PermittedCommands` is a list (`internal/pkgmgr/definition.go:1068-1070`), and `resolveClass` reads
`env.Class` from the envelope before falling back (`internal/processor/step4_hydrate.go:505-525`) — so a
declaration there is per-class, shared, and selectable by a client field, and its root would have to come
from the payload. **A Loom pattern step supplies a root the platform itself resolved.** That is the only
place in the system where the surface that knows the traversal and the surface that owns the value are the
same surface, which is why the design lands there and nowhere wider.

### 2.5 What the declaration buys today, honestly

Contract #2 §2.5 names two payoffs for a declared enumeration: the **Edge mirror-coverage gate** and
**static classification**. The Edge gate **is not built** — `internal/edge/agent/submit_gateway.go:48,105`
forwards `enumerations` and nothing consumes it; there is no coverage check anywhere under `internal/edge`
or `cmd/edge`. So today a declaration buys static classification and the drift ratchet, and nothing else.
**This is the fact that sizes Row A down and shelves Inc 3** (§9).

---

## 3. The shape

**One sentence: declare the PATH, not the key — and let the layer that can actually walk it do the walking.**

### 3.1 Row A — chained walks are already covered; say so and admit them

No new vocabulary, no new field, no production code path.

- **Contract:** class (e) covers a `kv.Links` hubbed on a vertex the enumeration itself discovered, on the
  same footing as the per-element reads it already covers (staged edit).
- **Guard:** the walk arm admits an enumeration whose `Hub` is in `record.EnumeratedVertices`, mirroring
  the read arm at `read_drift_guard.go:96-102` **exactly** — same set, same membership test, no extra
  conditions. §5.2 says why the stricter rule I first wrote is both unimplementable and unprincipled.
- **Effect:** the structurally-undeclarable walk rows retire from the baseline, and what remains is
  *declarable* debt — which is what a ratchet should measure.

This is **test-harness-only**. `ScriptReadRecord` is observation-only and must never gate an execution
(`script_read_record.go:33-41`); nothing here changes that.

### 3.2 Row B — a subject-rooted path, declared by Loom, resolved by the Processor

An externalTask step declares, beside its `params`, a static list:

```
{ hops: [ {relation, direction}, … ],   # non-empty, ordered, rooted at the pattern SUBJECT
  aspect: "<localName>" }
```

Loom carries it **unresolved** onto the instanceOp envelope — it reads no Core-KV state to do so, exactly
as `inferExternalTaskReads` reads none today (`internal/loom/externaltask_params.go:33-38`). The Processor
resolves it before hydration:

```
step 4  for each declared path: root = the engine-resolved subject key
        ⤷ hop 1 … hop n, each paged to exhaustion, exactly-one-live-link required
        →  terminal aspect key joins this execution's egressReads set
        →  the existing single KVGetMulti + the existing ref-if-sensitive branch
```

The script is unchanged in shape: it still walks to the applicant with `live_link_target` and still does
`kv.Read(applicant + ".name")` — but that read is now served from the hydrated snapshot as a
`$sensitiveRef` instead of falling through to a lazy live read whose plaintext trips step 6's egress guard.
Event payload, bridge, MAC, unwrap: unchanged.

**Why the root must be the engine's subject, and not a payload field.** This is the design's load-bearing
rule and the draft got it wrong. A `{payload.<field>}` root is *submitter input*, so the caller chooses
whose aspect gets resolved and how expensive the traversal is, before any guard the operation's own script
applies. The Processor has already ruled on exactly this shape, in the same package: a payload-derived
template "contributes nothing" to the descriptor floor because "a payload-derived exclusion is written by
the same hostile party the floor is defending against … An exclusion set the attacker can address is not a
precedence rule, it is a bypass" (`internal/processor/descriptor_floor.go:107-114`). The one root the
platform *does* validate is a step-3-validated target, and that bit is true on two auth paths only
(`internal/processor/operation_context.go:46-58`) — not on `Scope:"any"`, which is what the live consumer's
grant actually is (`packages/lease-signing/lease_signing_test.go:69`). A **Loom pattern subject** has none
of that problem: the engine selected it from the pattern anchor, no caller supplied it, and it is already
the root of every `params` template on the same step.

**Why the Processor and not the script.** CLAUDE.md's standing reflex — Core-KV reads default to
Processor-side. A Starlark `kv.EgressRef(key)` builtin is rejected in §4 row 5: it would move the naming
authority to "anything the script can reach" and put a live Vault `MAC` round trip
(`internal/processor/sensitive_decrypt.go:264`) inside the 250 ms script wall.

**Exactly-one, fail closed — and "exactly one" is a claim about the WHOLE link set, not one page.** Zero or
many fails the operation closed, naming the path and the hop. **The resolver must page to exhaustion**: the
listing drains the full server-side match set and pages client-side (`internal/substrate/kv.go:287-305`),
`limit` bounds the returned page rather than the work, tombstoned links come back in-band (§2.5.1), and
proving *liveness* requires reading the links' values. A first page carrying one live link and a live
cursor has proved nothing. **A single-page implementation of this hop is the defect this paragraph exists
to prevent**, and Inc 2 owns a test for it (§9).

**A deliberately many-valued relation is therefore NOT declarable — and the corpus contains one.**
`live_link_source(unit, "manages")` is documented as *"a flat co-management set with no primary"*
(`packages/lease-signing/leasedoc_scripts.go:108-113`), and `leasedoc_ops_test.go:215-240` seeds it.
Declaring a path over it would fail every `CreateLeaseDocInstance` on a co-managed unit. **It does not
need one:** that walk resolves a bare identity *key*, not a sensitive aspect, and the same comment says so
— *"no egress-declaration path is needed (contrast the tenantName comment below)"*. So Inc 2 declares the
**applicant** path (`applicationFor`, single-valued by construction — a lease application has one
applicant) and explicitly not the landlord one. **A landlord *name* would need a many-valued form this
design does not build**, and that is stated rather than discovered at build time.

**The cost is bounded by the declaration, not by the caller.** Hop resolution runs inside step-4 hydration,
*before* the `ScriptContext` and therefore before both the live-read budget tracker and the script wall
exist (`internal/processor/step4_hydrate.go:427-454`). Two rules close that: a **static ceiling** on total
hops per step, refused at pattern load — the deepest chain in the corpus is 4 hops (§6.1.1), so a small
single-digit cap is generous — and a **pre-charged** budget tracker, so the hops the Processor spent are
deducted from the script's live-read budget rather than being free. With the root fixed to the engine's
subject, the *degree* a hop must scan is a property of the graph at that subject, not a number a caller
picks.

## 4. Alternatives

**Row 1 is deletion. It is written first, and for Row A it very nearly wins.**

| # | Alternative | Verdict |
|---|---|---|
| **1** | **Do not have this thing.** Row A: leave the walk rows baselined forever. Row B: LoftSpace's executed lease keeps rendering a NanoID where a tenant name belongs. | **Row A: this is most of the way right, and the design says so.** With the Edge gate unbuilt (§2.5), a declared chained walk buys nothing a baseline row does not — which is why Row A ships as one guard clause and one contract word, not a grammar, and why it is droppable on its own. What deletion costs is *honesty*: the baseline is a debt ledger, and 47 of its 106 rows can never be paid (§6.1), so read as a to-do list it is 44% noise. What Inc 1 costs in exchange is a real loosening of the ratchet (§5.2). **That trade is the one genuine judgement call in Row A, and it is Andrew's.** **Row B: rejected** — the product defect is real, filed, and the workaround is structurally refused (§2.1). |
| **2** | Row A via a new chained-hub **grammar** (the filed row's own `no-pattern:` prescription). | **Rejected — the prescription is solution-shaped.** It names the primitive that a *dispatch-time* solution would need. Reachability is already recorded (`EnumeratedVertices`) and already sanctioned for reads; comparing against it is level-triggered, race-free, and needs no identity plane. Revive when the Edge gate lands and needs the *relations* named ahead of time (§9 Inc 3). |
| **3** | Row B by **snapshotting** the tenant name onto the leaseapp at `SignLease` (the fire brief's original plan). | **Refuted, structurally.** A sensitive aspect on a non-identity vertex can only be retention-class-custodied (`step65_encrypt.go:221-244`), and a retention-class holder is refused at both egress gates. Not a tuning problem — there is no custodian choice that makes it work. |
| **4** | Row B by **extending the egress boundary to retention-class holders** (deferred tail (a) of `retention-class-key-custody-design.md`, line 1966). | **Rejected, and note it is the *larger* change:** envelope columns on a retention-class lens, a bridge envelope-source switch, and relaxing two identity-only refusals — i.e. widening the crypto boundary to solve a *naming* problem. Its own census found zero live consumers (§8.9 of that design). Keep it shut. |
| **5** | Row B by a Starlark **`kv.EgressRef(key)`** builtin — the script mints the ref for whatever key it reached. | **Rejected on two independent grounds.** (a) *Authority*: the egressable set becomes "anything the script can walk to", which is unbounded and un-auditable; a declared path is a closed set checked at pattern load. (b) *Cost*: ref-mint needs a live Vault `MAC` (`sensitive_decrypt.go:264`) — a network round trip added inside the 250 ms wall, on the op family already at the wall (`kv-links-listing-leg-collapse-design.md` §3.1). **Priced in combination with row 6 it is still worse**, and note it fails the same test row 6b failed: the key it mints for is a value the *script* chose, not one the platform owns. |
| **6** | Row B by declaring on the **orchestration surface that owns the subject** — a Loom externalTask step emits an unresolved path, the Processor resolves it. | **ACCEPTED — this is the recommendation, and the draft rejected it on a false fact.** The draft's rejection said Loom "cannot resolve a hop" (true, and irrelevant once the engine only *carries* the path) and that `CreateLeaseDocInstance` has non-Loom dispatchers so a Loom-side declaration would be silently absent for them. **That second claim is false**: every `CreateLeaseDocInstance` envelope in `packages/lease-signing/leasedoc_ops_test.go:93,227,303,345,416` carries `Actor: bootstrap.LoomIdentityKey`, and the script fails closed on anything else (`leasedoc_scripts.go:163-165`). Loom is the *only* dispatcher, by design and by enforcement. |
| **6b** | Row B by declaring on the **owning DDL** with a `{payload.<field>}` root (the draft's own recommendation). | **REJECTED at close, on three counts.** (a) The root is submitter input, so the caller picks whose aspect resolves and how much it costs — the shape `internal/processor/descriptor_floor.go:107-114` already refuses by name. (b) The DDL meta hangs off the **class**, `PermittedCommands` is a list, and `resolveClass` takes `env.Class` from the envelope first (`internal/processor/step4_hydrate.go:505-525`) — so "per-operation, closed set, caller cannot select it" was false three ways. (c) A traversal-found terminal key is not named in the payload, which is exactly the justification step 4 gives for recording rather than faulting an absent declared key (`step4_hydrate.go:249-256`); a fault there quotes a key the caller never named, i.e. an existence oracle over the *target's* graph. |
| **7** | A general **`pathReads`** (non-egress) alongside the egress path. | **Deferred as dead scaffolding.** Every ordinary link-discovered read the corpus performs is already sanctioned as a class-(e) follow-up and costs the same live round trips wherever it runs. Zero consumers today. Revive trigger: a *replay-stability* requirement on a link-discovered read, or the Edge gate. |
| **8** | Fold this into the shelved **round-trip-collapse Fire 2** (per-execution key-set snapshot). | **Rejected — different problem.** Fire 2 is about multi-page listing cost on a *declared* walk; this is about a key that cannot be named. They do not overlap, and this design makes no performance claim (§10.4). |

**Re-asking the discipline question — could a variant of a rejected row beat the recommendation?** It did.
Row 6 *was* the rejected row, and the fact I used to reject it did not survive being opened. The general
lesson, recorded because it is the one worth carrying: **when a design needs a trustworthy root for a
traversal, the question is not "who knows the shape?" but "whose value is it?"** The package author knows
the shape; the platform must own the value. Loom is the only surface in the system where both are true at
once, which is why the answer is narrower than the draft's and also stronger.
---

## 5. State, lifetime, and the predicate tables

### 5.1 New state

| State | Created | Reset | Carried | Ordered |
|---|---|---|---|---|
| The path list on the Loom pattern step | at package install, with the pattern spec | never — replaced wholesale by the next install | across restarts (it is Core KV, the pattern's own body) | irrelevant (a set) |
| The unresolved path on the instanceOp envelope | at dispatch, by the engine, beside `params` | per dispatch | not at all — it is envelope-scoped | resolved before hydration, always |
| The per-execution resolved key set | at step 4, before the `KVGetMulti` | per commit **attempt** — the OCC retry loop re-enters Hydrate, so a re-execution re-resolves against the fresh snapshot and never reuses a prior attempt's resolution | never outside the execution | resolved before hydration, always |

**Why the declaration does NOT live on the DDL meta-vertex** (the draft's home, withdrawn): `.script` /
`.permittedCommands` / `.custody` / `.sensitive` all hang off the **class**
(`internal/processor/ddl_cache.go:738,793,842,888`), `PermittedCommands` is a *list*
(`internal/pkgmgr/definition.go:1068-1070`), and `resolveClass` reads `env.Class` from the envelope before
falling back to `ClassForCommand` (`internal/processor/step4_hydrate.go:505-525`). A path list there is
per-class, shared across every command the class admits, and selectable by a client-supplied field — none
of which is what "per-operation, fixed at install" means. On the pattern step it is per-step by
construction, and the engine, not the caller, decides which step runs.

**Validation happens at pattern load, where Loom already validates its other step declarations**
(`internal/loom/pattern.go:338` `Pattern.validate()`, alongside `validateSubjectTemplates` and
`validateEnumerations`) — not in the DDL cache. That removes the draft's whole unmarshal-policy question:
a malformed path list refuses the **pattern**, loudly, at load, rather than silently degrading an
operation at hydrate. It also keeps the refusal text on a surface whose fields the message can honestly
name, which is what the draft's validator reuse got wrong (§11 pass 1 #1).

### 5.2 The Row-A admission predicate, per state

| Execution state | Admit the walk? | Why |
|---|---|---|
| Hub declared in `contextHint.enumerations` | yes | today's behaviour, unchanged |
| Hub baselined for this op | yes | today's behaviour, unchanged |
| Hub ∈ `EnumeratedVertices` | **yes — new** | class (e) reachability, mirroring the read arm's membership test verbatim |
| Hub ∈ `EnumeratedVertices` only because a **baselined** walk discovered it | **yes** — accepted, see below | |
| Hub ∈ `EnumeratedVertices`, discovered by a walk that ran *after* this one | **yes** — accepted | the record is a set, not a sequence (`script_read_record.go:130-150`), and the read arm is already order-blind |
| Hub reached through an **aspect body field**, not a walk (cafe's tab `.status.leaseAppKey`) | **no** — stays baselined | no walk discovered it, so it is in no discovered set. §6.1 |
| Neither declared, baselined, nor reachable | no | today's behaviour, unchanged |

**The stricter rule I first wrote — "the root of the chain must be *declared*, not merely baselined" — is
withdrawn, on two counts, and I would rather record why than delete it.** First it is **unimplementable
from what exists**: `ScriptReadRecord` holds two independent sets, `Enumerations` and `EnumeratedVertices`,
with **no edge between them** (`script_read_record.go:130-150,178-188`) — nothing records *which* walk
discovered a given vertex, so "the walk that discovered this hub" is not a question the record can answer.
Implementing it means new per-walk provenance state, with a lifetime, on a test-time ratchet. Second it is
**unprincipled**: it would make the walk arm strictly stricter than the read arm on the same evidence, for
a hazard the read arm has lived with since it shipped.

**A second, separate defect in the RETIREMENT half — the build must prove around it.** The guard reports
and dedupes by normalized **shape** but admits by raw per-execution **membership**
(`internal/testutil/read_drift_guard.go:96-102` vs `NormalizeEnumeration`). So a row retired on the
strength of one execution path reddens on any *other* path through the same op that reaches the walk
without having run the walk that discovers its hub. Inc 1's revert-proof therefore has to test **both
directions** — retirement stays green across every fixture that reaches the op, not just the one that
motivated it — and a row that cannot clear that bar stays baselined. This is the concrete reason Inc 1
retires "what the re-run proves", never "the 47".

**What the ratchet therefore loses, stated plainly:** a *new relation* walked on a vertex the corpus
already reaches will land silently, where today it fails and gets a baseline row and a comment. That is a
real widening of the guard. It is bounded to vertices some already-accepted walk demonstrably reached, it
is exactly what Contract #2 §2.5 class (e) sanctions ("the data-dependent reads keyed off its results",
with no restriction on which), and the *cost* of the extra walk stays bounded by the live-read budget,
which is the mechanism that exists for that job. **If Andrew would rather keep the ratchet tight than the
baseline honest, Inc 1 is the increment to drop** — Inc 2 does not depend on it.

### 5.3 The Row-B hop predicate, per state

| Hop resolves to | Outcome |
|---|---|
| exactly one live link, **cursor exhausted** | continue to the next hop / the terminal aspect |
| one live link on page 1, **cursor NOT exhausted** | keep paging — the answer is not yet known (§3.2) |
| zero links | **fail the operation closed**, naming the path and the hop |
| more than one live link | **fail the operation closed**, naming the path, the hop, and the count |
| exactly one link, tombstoned | treated as zero — fail closed. `kv.Links` returns tombstoned links carrying `isDeleted` (§2.5.1) and the resolver must filter them, or a deleted relationship keeps egressing |
| terminal aspect absent | the ordinary class-(f) `HydrationMiss`, unchanged |
| terminal aspect present, non-sensitive | hydrated normally (ref-**if**-sensitive, per class (f) — declarers need no sensitivity knowledge) |
| the step has no resolved subject | the path is not declarable there — refused at pattern load, not at runtime |
| **terminal sensitive aspect TOMBSTONED** | today this is a bare `fmt.Errorf("read deleted sensitive aspect …")` on the egress branch (`internal/processor/sensitive_decrypt.go:159-172`), wrapped at `step4_hydrate.go:401` as `step4: decrypt <key>` — an untyped internal error whose reachability depends on the *target's* state and whose text quotes the target's key. **Inc 2 must turn this into a typed, key-free refusal** naming the path, not the key. It is pre-existing on the egress branch; Inc 2 is the first caller that can reach it on a key the submitter never named, which is what makes it this design's problem |
| **far-endpoint VERTEX tombstoned, link live** | the listing never reads the endpoint vertex (`internal/processor/starlark_kv.go:355-399`), so the path resolves and a deleted identity's PII egresses. **Inc 2 must read the terminal vertex root and refuse** — a link outliving its endpoint is exactly the state a tombstone-preserving store produces |
| **terminal aspect CRYPTO-SHREDDED** | the aspect stays present and un-tombstoned, and `v.MAC` needs no DEK, so the ref **mints successfully** and the failure relocates to the bridge's live-envelope fetch. That would regress `TestLeaseDocInstance_ShreddedApplicant_OmitsNameNoFailure`, which today asserts the op **commits** with a nameless render. **Inc 2 keeps that behaviour**: a shredded terminal resolves to no egress key and the document renders nameless, exactly as it does now |
| root template unresolvable | cannot occur — the root is the engine's own subject, and a step with no subject is refused at pattern load |

---

## 6. Executable censuses

Each ships as the command plus the expected result, so the build's Phase-0 re-runs it instead of trusting
this prose.

### 6.1 The walk-row split — **run at Phase 0, briefed to FALSIFY this design's number**

Run in this fire, per row, against the scripts: **47 class B / 55 class A / 0 ambiguous** over the 102
non-actor-role rows. Eleven chain shapes; deepest is `CreditCafeAccount` at four hops
(`payload.accountKey → heldFor → leaseapp → appliesToUnit → unit → containedIn → floor → containedIn →
building`; `packages/cafe-ledger/scripts.go:418,430,308`).

The mechanical re-run, which does not depend on reading scripts, because the census already records both
halves per execution (`internal/testutil/read_census.go:44-55`):

```sh
LATTICE_READ_CENSUS=/tmp/rc.jsonl go test ./packages/... ./internal/leaseconvergence/... -count=1
# then, per line: for each .enumerations entry whose {hub,relation,direction} is NOT in
# .hintEnumerations, is .hub ∈ .enumeratedVertices ?
```

**This is a falsification target, not a confirmation.** The two numbers must be compared, because they
measure different things and the difference is the finding:

- **The mechanical count will be LOWER than 47**, and the gap is the point. Three class-B chains reach
  their hub through an **aspect body field**, not a link: cafe-domain's `Charge` / `VoidCharge` / `Settle`
  take the leaseapp key from the tab's own `.status` aspect (`packages/cafe-domain/ddls.go:1140,1258`),
  denormalized there by `OpenTab`. Such a hub is **never** in `EnumeratedVertices` — no walk discovered
  it — so Inc 1 does not admit it, and neither does Inc 2's link-hop path grammar. **Those rows stay
  baselined, and this design says so rather than quietly counting them.**
- If the mechanical count comes back materially below the ~44 the link-reached chains predict, Inc 1's
  payoff shrinks and §4 row 1 wins outright for Row A. Drop Inc 1 rather than ship a guard clause that
  retires a handful of rows.

### 6.1.1 The chain shapes, for Inc 2's grammar

Recorded because a grammar must be checked against the corpus it will serve, not against one example:

| shape | hops | intermediate |
|---|---|---|
| appointment → `withProvider` → provider → `practicesAt` → site → `containedIn`… | 2–3 | link |
| series → `withProvider` → provider → `practicesAt` → site → `containedIn`… | 2–3 | link |
| leaseapp → `appliesToUnit` → unit → `containedIn` → building | 2 | link |
| renewal → `renews` → leaseapp → `appliesToUnit` → unit → `containedIn`… | 3 | link |
| account → `heldFor` → leaseapp → `appliesToUnit` → unit → `containedIn` → floor → `containedIn` → building | **4** | link |
| session → `atStudio` → studio → `locatedAt` → building → `containedIn`… | 2–3 | link |
| booking → `settlesClassPrice` (in) → transaction → `postedTo` → account | 2 | link, one **inbound** |
| menuitem → `servedAt` → building → `containedIn`… | 1–2 | link |
| workorder → `locatedAt` → unit → `containedIn` → building | 2 | link |
| location(payload) → `containedIn` → ancestor → `containedIn`… | 1–2 | link, **self-similar** |
| tab → `.status.leaseAppKey` → leaseapp → `appliesToUnit` → unit → `containedIn`… | 2–3 | **aspect field** — out of scope for both increments |

Two consequences the grammar must honour and this design commits to: **a hop may be `in` as well as
`out`** (the booking→transaction chain), and **a chain may repeat the same relation** (the containment
climb), so hops are an ordered list, never a set keyed by relation.

### 6.2 Who would declare a path — the Inc 2 consumer census

```sh
grep -rn "live_link_target\|live_link_source" packages/ | grep -v _test
```

**Expected:** a small set, of which the ones that also emit an `external.<adapter>` event are the true
consumers. **`CreateLeaseDocInstance` (`packages/lease-signing/leasedoc_scripts.go:221`) is the only one
this design commits to**; any other hit is a candidate, not a scope item. If the count is 1, that is fine
and expected — Inc 2 exists for a filed, blocked product defect, not for breadth.

### 6.3 The egress-boundary invariant is unchanged — pinned both ways

```sh
go test ./internal/processor/ -run 'TestEgressReads' -count=1
go test ./internal/bridge/ -run 'Egress' -count=1
```

**Expected:** green, unmodified. A path that resolves to a **non-identity-held** aspect must still be
refused at mint — Inc 2 adds a positive control proving the path mechanism does not become a way around
`refusableEgressHolder`.

---

## 7. Contract surface

| Contract | § | Change | Why it is promise-altitude |
|---|---|---|---|
| #2 | 2.5 class (e) | *"the data-dependent per-element `kv.Read`s"* → *"the data-dependent reads … the per-element `kv.Read`s, and a further `kv.Links` hubbed on a vertex the enumeration itself discovered"* | states what declaring an enumeration **covers** — observable, and today's prose under-states it |
| #2 | 2.5 | **new class (h)** row + the declared-path paragraph | a new declaration channel with observable refusal semantics (exactly-one over the whole link set, fail-closed) and an observable **authority** rule — the root is the surface's own subject, never a submitter value |
| #2 | 2.5 | ceiling + replay-stability sentences extended to (h) | (h) keys are not envelope-supplied, so the fault class differs; and the traversal is a live read, so the replay claim must be qualified |
| #10 | 10.5 | **the substantive clause** — an externalTask step may declare subject-rooted paths, carried unresolved by the engine and resolved by the Processor | §10.5 today says a linked vertex's field is out of reach and points at "the instanceOp DDL's own declared read", which is inexpressible. This states what a pattern may promise an adapter, and the three refusals that bound it. |

All four are staged **uncommitted** in `main`. No internal file, function, or field name appears in any of
them; the mechanism narration lives here and in `docs/components/processor.md`.

**What is deliberately NOT changed:** the `contextHint` envelope row (§2.1/§2.2). The path is carried on
the engine→Processor dispatch, not authored by a client, so **no external wire shape and no client
changes** — the descriptor surfaces, Weaver's gap grammar and the Edge are all untouched.

---

## 8. Reconciliation with the existing mental model

- ***Didn't the descriptor-declared-enumerations fire just fix this?*** No — and that design says so
  itself: *"A walk whose hub is unknown until the script reads something is undeclarable on every surface
  — that is a platform-wide absent pattern, not a pkgmgr gap"* (§6). It shipped the fourth
  dispatch-time surface. This is the remainder it named.
- ***Doesn't this duplicate `derive_reads` (class g)?*** It is the sibling, deliberately. (g) resolves from
  the **payload** and is therefore a pure function; (h) resolves from the **topology** and is therefore
  static data. Both are "declared where no dispatcher can express it", both are resolved by the
  Processor before hydration, and both fold into an existing class. A single mechanism cannot serve both:
  `derive_reads` is required to read no state (§2.5), which is exactly what a path must do.
- ***Doesn't Contract #2 forbid hydrating an enumerated link set?*** It forbids hydrating an **unbounded**
  one. A path hop is required to be single-valued and fails closed otherwise, so it is a resolution, not an
  enumeration. That refusal is what keeps the two apart, and it is why "many" is an error rather than a
  fan-out.
- ***Does this reopen anything the batched-read fire refuted?*** No. That fire refuted making the *existing*
  reads cheaper (`kv-links-listing-leg-collapse-design.md` §4) and this design proposes no new transport,
  reuses the shipped watcher lister for its hops, and makes **no performance claim** (§10.4).
- ***Does this introduce new state we already keep somewhere?*** The declaration is a field on a pattern
  step that already carries `params`, `reads` and `enumerations`; the resolved set is per-attempt and dies
  with the execution. No new durable plane, no new index, no new lens.
- ***Why is a Weaver gap action not given the same channel?*** Its root (`row.<column>`) is engine-produced
  too, so it is a defensible future extension — but it has **no consumer**, and a second grammar with no
  demand is the dead scaffolding §4 row 7 already refuses. Loom's externalTask is the surface with the
  filed, blocked product defect on it.

---

## 9. Decomposition for the Steward

Independently shippable and green. **Inc 1 is posture-changing (the ratchet's admission rule) and Inc 2 is
posture-changing (an authorization boundary) — both take the Steward's full review depth; Inc 3 is
shelved.** Review depth is the Steward's sizing (`agents/steward/SKILL.md` §4); this design does not set a
blanket.

### Inc 1 — chained walks are class (e) (S)

**Gated on §6.1's mechanical re-run coming back non-trivial.** The contract word; the guard's walk-arm
membership admission (§3.1, §5.2); retire, by hand and with the comment the file's own header requires,
exactly the rows the re-run proves chain-reachable — **not** the 47 this design counted, which includes
the aspect-field chains Inc 1 cannot admit.

*Owned tests:* one guard test per row of §5.2's table, including **both refusals** (a hub in no set; a hub
reached through an aspect body field rather than a walk), because a guard test that only proves admission
proves the guard was weakened. Plus a revert-proof: deleting the admission clause reddens exactly the
retired rows, and no others.

### Inc 2 — the subject-rooted declared path, end to end (M–L)

`LoomPatternSpec`'s externalTask step gains a path list + **validation at pattern load** (`Pattern.validate`,
`internal/loom/pattern.go:338`, beside the declarations it already validates) → the engine carries it
unresolved onto the instanceOp dispatch → step-4 resolution rooted at the engine's subject, each hop
**paged to exhaustion**, before the `KVGetMulti` → the hop ceiling and the pre-charged budget (§3.2) →
`lease-signing` declaring the **applicant** path (`applicationFor`, single-valued) and its script reading
the resolved key from the snapshot instead of re-walking → the executed lease renders the tenant's name.

**Refusal text belongs to the surface that owns the fields.** Do **not** call
`internal/pkgmgr/opdispatchtemplates.go`'s `validateReadTemplateList`: it hardcodes `"Dispatch.%s"` into
every refusal (`:373-399,:417-426`) and its remedy clauses name `Dispatch.ContextParams` and the
descriptor-form client (`:187,:189`) — fields and clients a Loom step does not have. That is the finding
the *previous* fire on that file already recorded (`descriptor-declared-enumerations-design.md` §7 #5), and
this design walked into it once. Loom validates its own step declarations; the path list is validated
there, in that file's own voice.

*Owned tests:* one per row of §5.3's table, each executed — including the three the draft missed: a
**tombstoned terminal aspect** (typed, key-free refusal), a **live link whose far-endpoint vertex is
tombstoned** (refused, not egressed), and a **crypto-shredded terminal** (`OmitsNameNoFailure` stays green
— the op commits, nameless). Plus: a **multi-page hop whose second live link sits on page 2** — the
single-page defect of §3.2, which nothing else in this list catches; a **many-valued relation refused at
pattern load** (the `manages` case, so nobody later declares it by analogy); the install refusal at the hop
ceiling; the **AI-authoring exclusion** pinned (§10.3); the §6.3 non-identity-holder positive control; a
revert-proof that deleting the pattern's path declaration reddens the lease e2e; and the e2e that the
rendered document carries a name.

*Package-edit obligations:* manifest + `Version` bump, `DIFF_BASE=<base> go run
./scripts/lint-package-version.go`, and `verify-package-*` — this edits a pattern and a DDL.

### Inc 3 — path-shaped enumeration hubs *(designed, SHELVED — dead scaffolding today)*

The general form: `EnumerationSpec.Hub` accepts a path, install-validated, resolved Processor-side, so a
chained walk is declared with its **relations named** rather than merely admitted.

**Two independent reasons it is shelved, and the second is the stronger one.**

1. **No consumer.** Its payoff is the Edge mirror-coverage gate, which does not exist (§2.5). Building it
   now ships inert machinery whose only effect is moving rows between two lists nobody reads at runtime.
2. **It cannot express four of the eleven chain shapes the corpus actually walks** (§6.1.1). The
   containment climb is **variable-length** — `containedIn` is followed until it returns nothing, and how
   many times is data — so a fixed hop list cannot describe it, and the exactly-one-per-hop rule that makes
   Inc 2 safe is exactly what a climb violates on its final hop by design. A path grammar for enumeration
   hubs would therefore serve the shallow chains and leave the deep ones baselined anyway. **Anyone
   reviving this must design the variable-length form first; it is not a widening of Inc 2's grammar, it
   is a different one.**

**Revive trigger, named:** the Edge mirror-coverage gate acquires a real consumer — something under
`internal/edge` actually *reading* `ContextHint.Enumerations` to decide predictability rather than
forwarding it — **and** a variable-length hop form is designed. Either alone is insufficient.

---

### Dossiers the build brief must copy in

`docs/components/processor.md`, `docs/components/pkgmgr.md`, `docs/components/loom.md`,
`docs/components/bridge.md` (`agents/fire-brief-template.md` part 5). One entry is load-bearing here and
is answered in advance: processor.md's **"a mechanism whose margin the SUBMITTER prices is not a
margin"** — the path list is declared on the pattern step and rooted at the engine's own subject, so
neither the path count nor the traversal's starting point is a number the caller supplies. That entry is
the reason the draft's `{payload.<field>}` root did not survive close (§11 pass 2 #1), and the reason the
declaration is not an envelope field.

## 10. Risks, and the things I want disagreed with

1. **Not a widening — a narrower channel beside an already-open one.** §For Andrew (iii) states the
   correction in full: `egressReads` is submitter wire that no authorization step inspects, so today any
   actor with any non-NFR-S6 grant can already name any identity's sensitive aspect. Inc 2's channel is
   strictly tighter (engine-owned root, fixed hops, exactly-one, pattern-load-validated). **The risk is
   therefore not what Inc 2 adds but what it leaves standing** — filed as its own ★★★ board row, not
   folded in, because closing it is an authorization-plane change with its own blast radius and its own
   compatibility question for every dispatcher that declares an egress read today.

2. **Two traversals of the same relation, at different times — with a third outcome the draft missed.**
   The script keeps its own `live_link_target` walk and the Processor adds a step-4 walk of the same
   relation. Beyond "stale-but-consistent" and "exactly-one refusal" there is a third: if the two
   *disagree*, the script's `kv.Read` misses the hydrated egress key, falls through to
   `connKVReader.ReadVertex` (`internal/processor/starlark_kv.go:453-471`) where `egressKeys` does not
   contain it, decrypts to plaintext, and step 6's guard rejects an operation that succeeds today —
   silent, timing-dependent, intermittent. **Inc 2 removes the divergence rather than tolerating it:** the
   script reads the resolved key from the snapshot and does not re-walk for it. The sequencing itself is
   confirmed sound (§11 pass 1 #5); it is the duplication that is the hazard.

3. **The AI-authoring path must be explicitly excluded, and pinned.** `vertexTypeDDL` and `opMeta` are
   AI-authorable (`internal/pkgmgr/capabilitymaterializer.go:21-28`), and the materializer already refuses
   a `Dispatch.Reads` entry resolving to a sensitive aspect, failing closed on a nil resolver because
   "an AI-authored capability that might need PII egress is exactly the case that must route to human
   authoring" (`capabilitymaterializer_starlark.go:466-484`). A declared path names a sensitive aspect **by
   design** — structurally the thing that rule forbids, arriving through a door it does not watch. It is
   caught today only incidentally, by the out-of-scope-field allowlist. **Inc 2 owns an explicit exclusion
   plus a test that pins it**, rather than relying on the allowlist.

4. **No performance claim.** Moving the hop out of the script does move round trips out from under the
   250 ms wall, and I have deliberately not counted that as a benefit: the honest-cost posture
   (`script-live-read-round-trip-collapse-design.md` §1) says work should cost what it costs, and the
   op families that would benefit are not the ones that would declare a path. If someone later wants that
   benefit, it is a separate fire with a measurement, not a corollary of this one.
5. **§6.1 may deflate Inc 1.** Written into the increment as a gate rather than discovered mid-build.

**Open questions: none.** The four that existed during this fire — where the declaration lives
(§2.4/§3.2: the Loom step, because it is the only surface owning both the shape and the root), whether the
crypto boundary needs to move (§2.1: no), whether Row A needs a grammar (§4 row 2: no), and what "exactly
one" does to a deliberately many-valued relation (§3.2: it is not declarable, and the corpus case does not
need it) — are resolved above.

---

## 11. Adversarial pass

Two cold reviewers over the draft — one on the security/authorization plane, one on mechanism grounding
("every sentence of the form *just pass X in* is an unopened file"). **Every finding below was folded into
the sections above; none is filed as follow-on work.** Recorded so a later reader sees what was tested.

### Pass 1 — mechanism grounding

| # | severity | finding | where it landed |
|---|---|---|---|
| 1 | **BLOCKING** | "install validation reusing `opdispatchtemplates.go`'s validator" is not reusable as written: `validateReadTemplateList` hardcodes `"Dispatch.%s"` into every refusal (`:373-399,:417-426`) and the hub remedy clauses name `Dispatch.ContextParams` and the descriptor-form client (`:187,:189`) — fields a `DDLSpec` surface does not have. **The identical finding the prior fire on this file already recorded** (`descriptor-declared-enumerations-design.md` §7 #5). | §9 Inc 2 — the refactor is now a named blocking prerequisite with its own executed-refusal test and a positive control on the `Dispatch.*` tails |
| 2 | **MAJOR** | "exactly one live link" was written as one call. The listing is paged (256/1024, `starlark_kv.go:281-284`) and returns tombstones in-band, so a first page with one live link and a live cursor proves nothing — a single-page implementation silently admits a second live link on page 2, defeating the design's own stated security property. | §3.2 paging-to-exhaustion rule; a new §5.3 predicate row; an owned Inc 2 test for the page-2 case specifically |
| 3 | **MAJOR** | Hop resolution runs inside step 4, **before** the live-read budget tracker and the script wall exist (`step4_hydrate.go:427-454`) — the draft named no bound at all on the work a path declaration provokes. | §3.2's two rules: a static hop ceiling refused at install, and a **pre-charged** budget tracker so the reads are charged rather than free |
| 4 | **MAJOR** | The four sibling DDL-cache readers are **not** uniform on unmarshal failure (`.permittedCommands`/`.script` swallow; `.custody` poisons closed; `.sensitive` poisons to `true` and reads a tombstone as LIVE), so "a fifth reader is mechanical" hid a real policy choice on a reader that gates an egress boundary. | §5.1 — decided fail-closed on the `.custody` shape, with the reason (fail-open reaches the *same* refusal several steps later, collapsed to a generic constraint violation with no actionable cause) |
| 5 | confirmed | The sequencing premise holds: class/DDL resolution completes at `step4_hydrate.go:116` and `derive_reads` at `:190-240`, both well before the atomic `KVGetMulti` at `:286`. A fifth `MetaVertexRef` field is available in time, and only two sites construct that struct. | §3.2 unchanged |
| 6 | confirmed | A `kv.Read` of a class-(h)-resolved key returns the `$sensitiveRef` and does **not** trip the plaintext guard: `decryptSensitiveDoc` calls `tracker.markPlaintext` only on the `egress=false` branch (`sensitive_decrypt.go:281`), so `consume` is a structural no-op for a ref-disposed key. The "script is unchanged in shape" claim is sound. | §3.2 unchanged |
| 7 | confirmed | `vtx.identity.<id>.name` is declared `Sensitive: true` with no `Custody` override (`packages/identity-domain/ddls.go:302-323`), i.e. identity-custodied by default — §2.1's load-bearing claim. Every other §2 ledger row checked out, including that nothing under `internal/edge` or `cmd/edge` consumes `Enumerations`. | §2 unchanged |

### Pass 2 — security / authorization plane

This pass **re-derived the design**. Its three blocking findings are the reason §3.2, §5.1 and §4 read as
they do rather than as drafted.

| # | severity | finding | where it landed |
|---|---|---|---|
| 1 | **BLOCKING** | *"a caller cannot add, widen, drop or redirect one"* was false: the draft's root grammar included `{payload.<field>}`, so the caller picks the starting vertex. The Processor already ruled on this exact shape — a payload-derived template "contributes nothing" to the descriptor floor because "an exclusion set the attacker can address is not a precedence rule, it is a bypass" (`descriptor_floor.go:107-114`) — and the draft omitted `{scopedTo}`, the one root the platform validates, while including the one it refuses to trust. Concrete: an actor holding the op at `Scope:"any"` submits another tenant's leaseapp and mints a MAC'd ref for a stranger's `.name`. | the root is now the **engine-resolved subject** only (§3.2, and the contract's first bullet). §4 row 6b records the rejection |
| 2 | **BLOCKING** | The draft's §5.2 "declared-root, not baselined-root" restriction is **unimplementable**: `ScriptReadRecord` holds two independent sets with no edge between them, so "which walk discovered this hub" is unanswerable. Both degradations are defects — optimistic is a blanket pass (the confinement preamble is declared on nearly every op), pessimistic retires almost nothing and is non-monotonic. | already withdrawn mid-fire on the same evidence; §5.2 now states membership-only and says why. The pass also found a **second, live defect in the retirement half** (shape-keyed reporting vs membership-keyed admission) — §5.2's new paragraph, and Inc 1's both-directions revert-proof |
| 3 | **BLOCKING** | "per-operation, closed set" was false three ways on the DDL home: the meta hangs off the **class**, `PermittedCommands` is a list, and `resolveClass` takes `env.Class` from the envelope first (`step4_hydrate.go:505-525`). Plus an **existence-oracle** regression — step 4's own comment justifies recording-not-faulting an absent declared key on the grounds that "touching the key requires naming it in the payload", and a traversal-found key is precisely one the caller never named. | the declaration moved to the Loom pattern step (§5.1's "why not the DDL meta"), where per-step is structural and the engine picks the step |
| 4 | **MAJOR** | Exactly-one contradicts a shipped consumer: `manages` is documented as "a flat co-management set with no primary" and the fixtures seed it. | §3.2 — the landlord path is explicitly **not** declared (it resolves a bare key, needs no egress declaration), and a many-valued relation is refused at pattern load with an owned test |
| 5 | **MAJOR** | Hop cost understated: `KVListKeysFilter` drains the **whole** server-side match set (`internal/substrate/kv.go:287-305`), liveness needs the values, and none of it is budgeted. | §3.2's paging-to-exhaustion rule, hop ceiling, and pre-charged tracker — and, with the root now engine-owned, the degree is a graph property rather than a caller choice |
| 6 | **MAJOR** | §5.3 was presented as exhaustive and missed three states — tombstoned terminal aspect (a bare internal error quoting the target's key), live link with a tombstoned far endpoint (**egresses a deleted identity's PII**), and a crypto-shredded terminal (mints fine, regressing a shipped "commits nameless" test). | three new §5.3 rows, three owned Inc 2 tests |
| 7 | **MAJOR** | The two-traversal divergence has a third outcome: a miss falls through to the plaintext path and step 6 rejects an operation that succeeds today. | §10.2 — Inc 2 removes the duplication rather than tolerating it |
| 8 | **MAJOR** | AI-authoring: a declared path names a sensitive aspect by design, which is structurally what the materializer's rule 2 forbids, arriving through a door it does not watch. | §10.3 — explicit exclusion, pinned by a test |
| 9 | **FALSE CLAIM** | §4's "`CreateLeaseDocInstance` has non-Loom dispatchers in the test corpus" — every envelope there carries `Actor: bootstrap.LoomIdentityKey`, and the script fails closed on anything else. **This was the single fact used to reject the alternative that fixes finding 1.** | §4 row 6 re-priced to ACCEPTED; it is now the recommendation |
| 10 | **FALSE CLAIM** | §For Andrew priced Inc 2 as a *widening* of "the set some dispatcher named". There is no platform closure at all: `egressReads` is submitter wire, `ParseEnvelope` checks count and disjointness only, and step 3 never inspects `contextHint`. | §For Andrew (iii) — corrected in the unsafe direction, and the open channel filed as its own board row rather than folded in |
| 11 | citations | §5.1 cited `ddl_cache.go:793` as `.custody` (it is `.sensitive`, the one reader that inverts the tombstone rule) and `step8_commit.go:414-418` for tombstone-preserves-body (it is `:510-515`). Baseline is 106 rows, not 103. | all three fixed; the §5.1 paragraph they sat in is gone with the DDL home |
| 12 | confirmed | The Edge mirror-coverage gate genuinely is not built, across `internal/edge`, `cmd/edge*` and `internal/gateway`. Inc 3's shelving is correct. Non-identity custody refutation (§4 row 3) holds. `kv.Links`' in-band tombstones are real. | unchanged |

**The shape of pass 2's findings, as one sentence:** every blocking finding was the same question left
unasked — **whose value is this?** I checked that the declaration was *static*, *install-validated* and
*package-authored*, and never that its one variable input was one the platform owns. Static is not the
same as trusted, and "the package declares it" says nothing about who fills in the blank.

**The shape of pass 1's findings, as one sentence:** four of the seven were the *same* blind spot — every
place the draft said "reuse it", "mechanically", "one call", or "before hydration" was a mechanism I had
not opened, and three of the four turned out to hide a real decision. Findings 1 and 4 are the sharper
ones, because in both cases the neighbouring code is **non-uniform** and the draft's word ("reusing",
"mechanical") presumed uniformity it had not checked.
