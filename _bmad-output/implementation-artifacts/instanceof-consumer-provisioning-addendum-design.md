# Design addendum — provisioning the `instanceOf → type-DDL` link (the consumer's missing enabler)

**Status: ✅ Andrew-ratified (2026-06-29) — Mechanism A** · Winston (Designer fire, 2026-06-29) · addendum to
[`instanceof-template-op-discovery-design.md`](instanceof-template-op-discovery-design.md)

## For Andrew (ratify in one look)

The **Verticals "Service-instance modeling" refactor** (verticals.md Row 112) was marked *"package-only,
no platform change, build-ready"* once the Fire-1 resolver shipped. **That premise is false** — I grounded
it in code this fire. Fire 1 built the resolver's `instanceOf → vtx.meta.<typeDDL>` **terminal**, but **no
package can produce that link**: a Starlark op script cannot obtain its own type-DDL meta key (the `ddl`
global exposes only `canonicalName` + `permittedCommands`), and pkgmgr install seeds no business vertices.
Without the link, a fine-grained-class instance (`service.bgCheck.instance`) misses the exact DDL lookup and
falls to the **permissive default = the write-gate security regression** the parent design §1.2 warns about.

**The fix is a tiny Lattice-lane enabler that completes Fire 1**, not new machinery:

> **Recommended — Mechanism A:** expose the executing op's own **type-DDL meta key** to its Starlark script
> (one field on the `ddl` global). Then a create-op writes `instanceOf → <its own DDL meta>` in the same
> batch → the resolver's already-built meta terminal resolves it. **~3 lines of `internal/processor` + a
> test; no contract change** (Contract #2 §2.1 already blesses the discriminator-vs-script-DDL model — committed).

**Fork for your call (lane + sequence):**
- **A (recommended):** the meta-key exposure is **Lattice-lane** (`internal/processor`). So **Row 112 is
  🚧 blocked-on a one-line Lattice fire** ("Fire E"), then the Verticals consumer builds against it. Cleanest
  — it *completes* a primitive that already exists (the resolver's meta terminal is dead code until a producer
  exists). One-way dependency, no ping-pong.
- **D (package-only fallback):** a per-package **"type-anchor" business vertex** whose envelope class **is**
  the bare DDL canonical name (`vtx.service.<anchor>` class `service`), which templates `instanceOf →`,
  exploiting the resolver's *second* terminal (`classOf(target)` is a DDL). **No platform change → stays
  fully Verticals-lane, build-now** — but it invents a new vertex role + a standing convention every templated
  package must follow, to work around a meta key the script simply can't see. Inferior; offered only if you
  want to avoid the cross-lane hop.

Pick A (tiny Lattice enabler, then the consumer) or D (package-only now, new anchor convention). Everything
below grounds the call. **No frozen-contract edit is staged** — Mechanism A needs none.

---

## 1. What this addendum settles

The parent design (ratified, Fire 1 shipped `cea0c3b`) specified the resolver and *asserted* the Verticals
consumer "consumes Fire 1." It did **not** specify **how a package produces the `instanceOf → type authority`
link** — it showed the link existing in the worked example (§4) but never named the mechanism that writes it.
verticals.md Row 112 then filled that void with a guess — *"install-provisioned templates ... as
`make install-loftspace` already seeds vertices"* — which is **factually wrong**. This addendum grounds the
real mechanics and picks the enabler.

## 2. Evidence — the gate is closed, but a second prerequisite is missing

### 2.1 Fire 1 (the resolver) IS on main — confirmed

`internal/processor/step6_resolve_ddl.go` (`resolveGoverningDDL`, commit `cea0c3b`, CI green run
28396055390, full 3-layer reviewed). It walks a fine-grained-class vertex's `instanceOf` chain to the
governing **vertexType** DDL when the exact `DDLs.Lookup(class)` misses (`maxInstanceOfHops=4`, cycle-guarded,
fail-open to the §1.5 permissive default). **Two terminals** (lines 128–144):
- **Terminal #1** — the `instanceOf` target IS a `vtx.meta.*` vertexType DDL (`LookupByMetaKey`, `Kind=="vertexType"`).
- **Terminal #2** — the target is a *business vertex whose own class is itself a registered DDL* (`DDLs.Lookup(classOf(target))`).

### 2.2 The provisioning blocker — neither path can write `instanceOf → meta` today

The refactor moves the discriminator onto the envelope class (`service.<fam>.instance`), which **misses** the
exact lookup → it depends entirely on reaching a terminal. To reach **Terminal #1** the instance/template must
link `instanceOf → vtx.meta.<typeDDL>`, which needs the **type-DDL meta key**. Grounded:

- **Runtime scripts cannot see the meta key.** `internal/processor/starlark_runner.go:ddlMapToStarlark`
  builds each `ddl` entry as `{canonicalName, permittedCommands}` only. `MetaVertex.Key` exists but is
  `json:"-"` and is **never surfaced to Starlark**. And at hydration the `ddl` global is
  `map[string]MetaVertex{class: metaVtx}` (`step4_hydrate.go:179`) — it already holds **exactly the executing
  op's own type DDL** (`metaVtx`, the one the hydrator resolved + logs as `ddlKey`) — but with no key field a
  script cannot name `vtx.meta.<service>` to write the link.
- **Install seeds no business vertices.** `internal/pkgmgr/build.go` `addCreate` emits only meta-vertices
  (DDLs / lenses / roles / roleindex / permissions / package) — there is **no seed-business-vertex / template
  primitive** in `pkgmgr.Definition`. `make install-loftspace` runs `lattice-pkg install` per package and
  seeds **no** `vtx.service.*` template (the Row 112 claim is false).

**Consequence:** built as Row 112 describes (package-only, fine-grained class, "install-seeded templates"),
every `service.<fam>.instance` create would walk `instanceOf` → find no link / no terminal → **notFound →
permissive default**. That is the exact write-gate regression the parent design §1.2 calls out: today the
coarse class `leaseServiceInstance` / `service` exact-matches its DDL and `permittedCommands` is enforced;
the naive refactor silently *removes* that enforcement. The terminal the resolver was built for is
**unreachable** because no producer can mint the link.

### 2.3 What the consumer actually needs (grounded)

- The lease instance vertex `vtx.service.<handle>` is written **only** by `CreateLeaseServiceInstance`
  (`leaseServiceInstance` DDL, `permittedCommands:[CreateLeaseServiceInstance]`). So the instance's
  **vertex-root** governing DDL need only admit the create op — its `instanceOf` target resolves to *its own
  create-op's type DDL*, which is **already the script's own DDL** (`ddl[scriptClass]` in the execution
  context). So the producer needs nothing more than *"the meta key of the DDL I'm already running under."*
  This is why **Mechanism A is the minimal, exact fix**: surface the key the script context already holds.

> **⚠️ CORRECTION (build fire, 2026-06-29) — this section originally claimed the `.outcome`/`.dispatch`
> aspects "have their own aspectType DDLs → resolve by exact lookup and never touch the instanceOf chain."
> That was WRONG, caught by grounding it in code during the build.** Those aspects are written with class
> `outcome`/`dispatch` for which **no aspectType DDL existed** — today they pass via the §1.5 **permissive
> default** (the instance was template-less, so an aspect-class miss walked to *no* instanceOf target). The
> moment the instance gains its `instanceOf → meta` link, an aspect-class miss walks the parent's chain to
> the `leaseServiceInstance` DDL (which permits only `CreateLeaseServiceInstance`) and **fails closed** — a
> regression introduced by the fix. **The build therefore ALSO adds two declaration-only aspect-type DDLs**
> (`leaseServiceOutcome` for `.outcome`, `leaseServiceDispatchMarker` for `.dispatch`) so each aspect write
> resolves by **exact class match** to its own gate and never walks the chain. This is a *strengthening*
> (those writes were ungated before). Lesson: the "aspects have their own DDLs" assumption must be **grounded
> per package** — an aspect class is gated only if a registered aspectType DDL bears that exact canonical
> name; otherwise it falls to the permissive default, and adding an instanceOf link to its parent changes
> that. (Shipped: lease-signing `2a5087a`.)

## 3. Mechanisms evaluated

| | Mechanism | Lane | Platform change | Verdict |
|---|---|---|---|---|
| **A** | Expose the op's own type-DDL **meta key** to its Starlark script; create-ops write `instanceOf → <own DDL meta>` (terminal #1, 1 hop) | **Lattice** | ~3 lines (`ddlMapToStarlark` + populate `MetaVertex.Key`) + test | **✅ RECOMMENDED** |
| **B** | A `pkgmgr` **seed-business-vertex** primitive: declare templates in `Definition`, install provisions them + their `instanceOf →` (just-minted) meta | **Lattice** | New `Definition` field + install-emit + wiring (substantial) | Rejected — heavier than A, and templates aren't required for the write-gate (§2.3) |
| **C** | `CreateServiceTemplate` takes the **meta key as an op param**; an operator/seed flow discovers `canonicalName→metaKey` and passes it | Verticals (claimed) | None | Rejected — discovery machinery isn't clean in-lane (apps read lenses, not meta state, P5); leaks a platform meta key into a domain op surface |
| **D** | Per-package **"type-anchor" business vertex** with envelope class == the bare DDL name; templates `instanceOf →` it (terminal #2, no meta key) | **Verticals** | None | Viable fallback — but a *new* vertex role + a standing convention, working around a key the script can't see |

### 3.1 Why A over B
B genuinely enables install-provisioned templates (useful someday) but it is **more machinery than the
problem needs**: §2.3 shows the write-gate needs only `instanceOf → own-DDL-meta`, which A delivers in three
lines by surfacing state the script context **already loads**. The design discipline — *simplest extension of
existing machinery over a new mechanism* — points at A: Fire 1 already built the meta terminal; A makes it
**producible**. B builds a parallel provisioning channel for a link a runtime op can write directly.

### 3.2 Why A over D
D is the cleverer-new-mechanism trap. It works (terminal #2 resolves a target whose class is a DDL), and it
is genuinely package-only — but it requires:
- a new **"anchor"** vertex role outside the parent design's 3-role model (type / template / instance);
- provisioning the anchor (an op + a **deterministic** key so templates can reference it without discovery);
- a standing convention ("every templated package keeps a bare-class anchor vertex") that the next reader
  must learn.
All of that to avoid surfacing a meta key the script context already holds. A is architecturally cleaner and
removes the same friction permanently for *every* future templated package. **D is the fallback only if the
cross-lane hop to A is judged not worth one Lattice fire.**

### 3.3 Why not C
C keeps it in-lane on paper, but meta-key **discovery** isn't clean from the Verticals lane: a vertical app
reads lens projections, not meta-vertices (P5); there is no `canonicalName→metaKey` lens, and threading a
platform meta key through a domain op's payload is exactly the kind of identifier-misuse the design reflexes
warn against (a platform address leaking into a business op surface). Rejected.

## 4. The shape under Mechanism A (recommended)

### 4.1 Lattice-lane enabler — "Fire E" (the producer half of Fire 1)
- In `ddlMapToStarlark` (`starlark_runner.go`), add `metaKey` to each `ddl` entry, sourced from
  `MetaVertex.Key`. Ensure the hydrator populates `metaVtx.Key` (it already resolves `ddlKey` at
  `step4_hydrate.go` — surface it onto the struct). A script then reads its own type-DDL meta key as
  `ddl[<its class>].metaKey` (the `ddl` global already contains exactly that DDL).
- **No contract change.** Contract #2 §2.1 (committed) already specifies the write-gate resolves a vertex's
  governing DDL "by its own class — by exact lookup, falling back to its `instanceOf` type authority per
  Contract #1 §1.5." The `ddl` global's *field set* is an internal script-environment detail, not a
  contract-enumerated surface (Contract #2 enumerates the **envelope** fields, not the script globals).
- **Tests:** a script that writes `instanceOf → ddl[self].metaKey` produces a link the resolver's terminal #1
  resolves; the `metaKey` is the correct `vtx.meta.<NanoID>`; absent/unknown name → no key (fail-safe).
- **Security:** auth (step 3) is unchanged (keys on `operationType`). A script linking `instanceOf →` some
  *other* DDL meta can only ever land on that DDL's `permittedCommands` check — which, for an op that DDL
  doesn't list, **fails closed** (`WriteScopeViolation`). The meta key is not a secret; exposure cannot widen
  the auth surface (parent design §2.4).

### 4.2 Verticals-lane consumer — "Fire C" (the refactor, builds on Fire E)
ONE coupled fire with an internal order (the parts are inert/unsafe apart — do not split across fires):
1. **service-domain:** `CreateServiceTemplate` writes template class `service.<fam>.template` +
   `instanceOf → ddl["service"].metaKey`; `CreateServiceInstance` writes instance class
   `service.<fam>.instance` + `instanceOf →` the template; **drop the `.class` aspect**; validate
   template/instance by **envelope class** (`...template` / `...instance`) instead of the `.class` aspect.
2. **lease-signing:** `CreateLeaseServiceInstance` writes `vtx.service.<handle>` class `service.<fam>.instance`
   + `instanceOf → ddl["leaseServiceInstance"].metaKey` (1 hop — §2.3); **drop the `.class` and `.family`
   aspects**; rewrite the `leaseApplicationComplete` lens predicates `inst.family.data.value = 'X'` →
   `inst.class = 'service.X.instance'` (the full engine matches a node label on key-type *or* envelope class
   — `executor.go:348` — so `MATCH (inst:service)` still binds; pin with a cypher unit test).
   *(Templates for lease families are needed only if `service-location`'s `availableAt` attaches to them — a
   separable follow-on; the write-gate itself needs only the 1-hop instance→own-DDL-meta link.)*
3. **Tests:** a **write-gate regression test** — `CreateLeaseServiceInstance` / `RecordServiceOutcome`
   against a fine-grained-class vertex still enforce `permittedCommands` *through the resolver* (PASS when
   admitted; `WriteScopeViolation` when not); update `lease_signing_test.go` / `service_instance_test.go`
   `.class`/`.family`/`class:leaseServiceInstance` assertions; verify the full path via the load-bearing
   `make test-lease-convergence` e2e.
4. **P7 lint gate** (the `.class`/`.family` outliers are retired here, so the gate can finally pass — it lives
   in the Lattice lane's instanceOf row but must land *after* this refactor; coordinate).

## 5. Reconciliation with the mental model

- *Didn't Fire 1 already make this build-ready?* No — Fire 1 built the **resolver** (the *consumer* of the
  link). It did not build a **producer** of the `instanceOf → meta` link. The meta terminal is dead code
  until Fire E (or D) gives a package a way to write the link. This addendum is the producer half.
- *Does Mechanism A introduce new state?* No. The op's own DDL meta-vertex already exists and is already
  loaded into the script context (`metaVtx`); A surfaces its key. No new vertex, aspect, link type, or store.
- *Is this a contract change?* No — Contract #2 §2.1 (committed) already describes the instanceOf type model;
  A is an internal script-environment field.

## 6. Sequencing & lane (the one-way hand-off)
**A:** Lattice ships **Fire E** (tiny) → Verticals ships **Fire C** (the refactor) against it. One-way, no
ping-pong (Fire E is a general primitive — *"a script can name its own type DDL"* — not a one-off for this
consumer). Until Fire E lands, **Row 112 is 🚧 blocked-on Fire E** and must **not** be assume-built
(never hand a consumer a producer that doesn't exist). **D:** if Andrew prefers no cross-lane hop, the
consumer builds now with the anchor-vertex convention — record the convention in the package READMEs.

## 7. Definition of done (for the chosen path)
- **A:** Fire E merged (meta key on the `ddl` global + test, gates green) → Row 112 unblocked → Fire C merged
  (refactor + write-gate regression test + `test-lease-convergence` green) → P7 lint gate lands.
- **D:** Fire C merged with the anchor-vertex provisioning + convention; no Lattice change.
