# Root-identity designation — design

**Status: 📐 awaiting-Andrew (ratification).** · Designer: Winston (architect) · 2026-07-02
Backlog row: `planning-artifacts/backlog/lattice.md` → *Arch-review intake* →
"protected-flag-create-guard-vector". Source: `docs/reviews/arch-review-2026-07-02.md` ranked
correction #1 (Bootstrap §) + the Epic-12 carried obligation
(`docs/decisions/projection-plane-decomposition.md`, "Review adjudication" bullet 1).

---

## For Andrew (the one-look)

**What it does (two lines).** Today an actor is **root-equivalent** iff its identity vertex carries
`data.protected = true` — a self-asserted boolean sitting *inside the business `data` map*, whose
only guard (`rejectProtectedMutations`, `step8_commit.go:129`) covers **update/tombstone but exempts
create**. Nothing pins that a non-root actor can't *create* an identity with `protected:true`, and the
same bit is **overloaded** (it also means "kernel-immutable / un-tombstoneable" for DDLs & lenses), so
you can't reason about root-ness without reasoning about immutability. This design **re-designates root
identities off the bit** onto a dedicated, out-of-band, **seed-only** marker the Processor forbids any
op from ever setting — and lands an immediate create-guard floor first so the hole closes now.

**The fork is the marker's shape** (§4). My recommendation: a **reserved top-level envelope field
`rootActor`** (sibling of `isDeleted`), seeded only by bootstrap, addressable by the capability lens as
a self-predicate (`WHERE identity.rootActor = true`) with **zero engine change** (verified — the full
engine resolves top-level fields off the vertex root body, `executor.go:1416`). Rejected alternatives:
an `isRootActor` **link** (graph-honest per Contract #1, but reintroduces a join into the hottest auth
lens — fighting Epic-12's deliberate self-predicate decomposition — and needs an endpoint-spoof-proof
link guard) and a **sealed aspect** (an extra point-read per authz + a broader `_`-prefix ban to
enforce). The field is the smallest change that fixes all four defects at once.

**Frozen-contract changes (staged UNCOMMITTED for you, Fire 2 only):**
- **Contract #1 §1.3** — add `rootActor` to the universal envelope field table (a platform-semantic
  boolean the Processor manages, exactly like `isDeleted`; default absent/false; **seed-only**).
- **Contract #6 §6.1 + §6.14** — the two capability-lens predicates change
  `WHERE identity.data.protected = true` → `WHERE identity.rootActor = true`.
- **Contract #7 §7.2** — the primordial seed shape: root identities carry `rootActor:true`
  (**and keep** `data.protected:true` for anti-brick).
- **Contract #8 §** (create-exemption note) — a one-line clarification that the create-exemption is
  scoped to *overwrite-conflict* reasoning and does **not** license minting a new protected/root
  identity (Fire 1 needs no contract *change* — it enforces existing intent — but this sentence removes
  the apparent license).

**Two fires, sequenced, both independently valuable** (§6): **Fire 1** = the Processor create-guard +
Gate-2/Gate-3 vectors — discharges the Epic-12 obligation immediately, no contract change, ships alone.
**Fire 2** = introduce `rootActor`, migrate the two lens predicates + `SystemActorKeys`, re-seed, and
generalize the guard to the new field — the re-designation you asked for, behind the contract edits.

---

## 1. Problem + intent

### 1.1 What "root" means today, and where the bit is load-bearing

Root capability in Lattice is **six platform grants at `scope:any`** —
`CreateMetaVertex`, `UpdateMetaVertex`, `TombstoneMetaVertex`, `InstallPackage`, `UninstallPackage`,
`UpgradePackage` — plus (read side) the **wildcard `*` read anchor**. Those are what let an actor author
DDL, install packages, and read across every anchor. After the Epic-12 god-cypher decomposition, core
projects that set for a **fixed, kernel-seeded handful of identities only**, identified by a **literal
self-predicate** so core references no rbac/service vocabulary and stays authorizable even when no rbac
package is installed. The predicate is, verbatim, in three places:

- **`internal/bootstrap/lenses.go:124`** — `CapabilityLensDefinition` (write-path anchor →
  `cap.<actor>` platform grants): `MATCH (identity:identity {key:$actorKey}) WHERE identity.data.protected = true`.
- **`internal/bootstrap/lenses.go:342`** — `CapabilityReadWildcardGrantsLensDefinition` (read-path →
  the `*` wildcard grant): `MATCH (identity:identity) WHERE identity.data.protected = true`.
- **`internal/bootstrap/system_actors.go:60`** — `SystemActorKeys` scans core-kv and returns every
  identity whose `env.Data["protected"] == true`; that set feeds step-3's `classAwarePlatformKey`
  routing (`step3_auth.go:151`), deciding who reads the core `cap.<actor>` anchor vs. `cap.roles.<actor>`.

The bit is **set** only in the primordial v14 seed (`internal/bootstrap/primordial.go`): the admin +
the Loom / Weaver / Bridge / object-store-manager / privacy service actors, each
`map[string]any{"protected": true, …}`. It is *also* set on every kernel meta-vertex (root DDL, the
lens definitions) — but those are **not** class `identity`, so they never match the capability lens's
`MATCH (:identity)`.

### 1.2 The four defects

1. **In-band with business data.** `protected` lives in the vertex's own `data` map — the same map an
   identity-writing package populates. The capability plane's root-of-trust is a field co-resident with
   ordinary business state.
2. **Create is unguarded.** `rejectProtectedMutations` (`step8_commit.go:135`, `rejectProtectedMutations`
   → `rootIsProtected`) rejects an **update or tombstone** whose target root carries `data.protected==true`,
   but **create is exempt by design** (`step8_commit.go:134`: "create mutations are exempt — create-only
   already conflicts on overwrite"). That reasoning is sound for *overwriting an existing* protected root
   (create-only fails on the pre-existing key) but **vacuous for minting a new** identity — there is no
   pre-existing key to conflict with. So the Processor will happily commit a `create` of
   `vtx.identity.<newid>` with `data.protected:true`.
3. **Only convention guards the create today.** With the *current* package set there is no live exploit:
   the sole op that creates identities, identity-domain's `CreateIdentity`, **hardcodes
   `data:{}`** (`packages/identity-domain/ddls.go:399`). But that is one line of package convention, not
   a platform invariant — a future identity-minting op that passes caller data through, or a typo in a
   domain DDL, silently becomes a root-escalation. The Epic-12 review flagged exactly this as a
   **carried obligation** ("verify the identity create/claim path cannot let a non-root actor set
   `data.protected:true` … if it can, 12.6's anchor amplifies it into the 5 root grants").
4. **Overloaded.** The one bit means **two** things: "kernel-immutable" (the anti-brick step-8 guard,
   applied to DDLs, lenses, *and* root identities) **and** "root-equivalent actor" (the capability
   anchor, applied to identities only). You cannot tighten or reason about one meaning without touching
   the other.

### 1.3 Why this is ★★★ though inert today

Nothing is presently on fire — the arch-review correctly ranks this **pre-emptive**. It ranks ★★★
because the blast radius is the **entire capability plane**: the marker *is* the root of trust, and its
integrity rests on (defect 3) a package-level coding convention plus (defect 2) an exemption whose
justification doesn't cover the dangerous case. A structural invariant belongs under the Processor, not
under a DDL author's discipline.

---

## 2. Grounding — the pattern this mirrors, and the invariants it must hold

- **The self-predicate is deliberate, keep it.** Epic-12 removed graph walks from core's capability
  cypher precisely so core doesn't depend on rbac/service packages and remains authorizable standalone
  (`06-capability-kv.md` §6.1). Any redesign must keep the anchor a **self-predicate over the actor's
  own vertex** — no join, no package vocabulary. This rules links out (§4).
- **Reserved platform fields already exist at the envelope's top level.** `isDeleted` is a
  platform-semantic boolean the Processor manages, sitting beside `class` in the universal envelope
  (`01-addressing-and-envelope.md` §1.3). `rootActor` is the same kind of thing — a platform flag, not
  business data — so the envelope top level is its natural, precedented home. (Contract #1 also already
  reserves the `_`-prefix namespace for "platform-generated system metadata", which the sealed-aspect
  alternative would lean on — see §4.)
- **The engine resolves top-level fields with no change.** `executor.go:1416 resolveProperty` returns a
  name **present in the vertex root body directly** (`nr.props[key]`), and only treats a *root-absent*
  name as an aspect reference. `identity.key` and `identity.data` already resolve this way; a new
  top-level `rootActor` resolves identically — **verified, zero ruleengine change**.
- **Fail-closed on omission (the security-boundary reflex).** The redesign must keep *absence* meaning
  *not-root*. Both today's `WHERE … = true` and the new `WHERE identity.rootActor = true` are
  affirmative predicates — a missing marker yields **zero rows → no `cap.<actor>` doc → deny by absence**
  (§6.8). Good. The *new* risk to check is the inverse: the guard must not let an op **set** the marker.
  That is the whole point of the Processor guard below, and it is **default-deny** (no op may set it;
  only the direct bootstrap batch, which bypasses the op path, does).
- **P2 holds.** The seed is the sanctioned direct-write exception (bootstrap provisioning); every
  runtime mutation still flows through the Processor. The guard lives in the Processor (step 8), the
  sole Core-KV writer — no engine gains a new responsibility.

---

## 3. The shape

### 3.1 Fire 1 — the create-guard floor (no contract change, ships alone)

Extend the step-8 protected guard to reject a **create** that would mint a **root-capable identity**.
Precisely: reject a `create` mutation whose document **would be matched by the capability lens's
`:identity` label AND carries `data.protected == true`**.

The `:identity` label matches three ways in the engine (`executor.go:352-372`): the key's type segment
(`vtx.identity.<id>`), a `class == "identity"` prop, or a `label == "identity"` prop. The guard mirrors
all three so it can't be dodged by class-spoofing a non-`vtx.identity` key:

```
reject create  ⟺  data.protected == true
                 AND ( keyType(m.Key) == "identity"
                       OR document.class == "identity"
                       OR document.label == "identity" )
```

- **Breaks nothing.** Package install creates protected **meta** vertices (`vtx.meta.*`, class `meta.*`)
  — untouched (not identity-labelled). identity-domain `CreateIdentity` creates `data:{}` identities —
  untouched. No op legitimately creates a protected identity.
- **Unconditional** — no "unless the actor is root" branch needed, because *nothing* legitimately does
  this via an op. That keeps step-8 free of an actor-tier lookup it doesn't currently do.
- **Typed failure.** Surface as the existing `ProtectedKeyError` (or a sibling `ProtectedCreateError`
  mapping to the same `ProtectedKey` reply code) — the commit path already maps it to a `rejected`
  reply (`step8_commit.go:39`).

This is the Epic-12 obligation, discharged: after Fire 1 the create path *cannot* let a non-root actor
set `data.protected:true` on an identity.

### 3.2 Fire 2 — re-designate onto a reserved seed-only field

1. **New universal envelope field `rootActor bool` (`json:"rootActor,omitempty"`)** on
   `substrate.DocumentEnvelope` (`internal/substrate/envelope.go:14`). Default absent. Semantics:
   "this identity is a kernel-seeded root-equivalent actor." **Platform-managed, seed-only** — see the
   guard below.
2. **Seed** (`primordial.go`): the admin + the six service actors gain `rootActor:true` in their vertex
   envelope (a new arg / envelope path — *not* inside `data`). They **keep** `data.protected:true` for
   the anti-brick immutability guard, which is unchanged. Kernel meta-vertices keep `data.protected:true`
   only (they were never root actors).
3. **Migrate the three read sites** off `data.protected`:
   - `CapabilityLensDefinition` cypher → `WHERE identity.rootActor = true` (`lenses.go:124`).
   - `CapabilityReadWildcardGrantsLensDefinition` cypher → `WHERE identity.rootActor = true`
     (`lenses.go:342`).
   - `SystemActorKeys` → match `env.RootActor` (the parsed top-level field) instead of
     `env.Data["protected"]` (`system_actors.go:60`).
4. **Generalize the Processor guard** (supersedes Fire 1's `data.protected`-scoped form): **no op may
   create OR update a document carrying a top-level `rootActor` field, any value** — full stop,
   unconditional, no label scoping. Meta-vertices never carry `rootActor`; only the direct bootstrap
   batch (which does not traverse the op path) sets it. This is *cleaner than Fire 1's guard* — the
   dedicated field needs no `:identity`-label reasoning because nothing but a root identity ever carries
   it. (Fire 1's `data.protected`-create guard **stays** as defense-in-depth against the now-downgraded
   immutability-only misuse — see §3.3.)

After Fire 2: root-ness is **out-of-band** (defect 1 ✓), **create-and-update-guarded structurally**
(defects 2, 3 ✓), and **decoupled from immutability** (defect 4 ✓).

### 3.3 What happens to `data.protected` after the split

`data.protected` retains **exactly one** meaning: **kernel-immutable / un-tombstoneable** (the anti-brick
`rejectProtectedMutations` guard on update/tombstone — unchanged). Consequence for the create path:
after Fire 2, an actor who managed to create a `data.protected:true` vertex would gain **no capability**
— only self-inflicted immutability (a vertex they then can't update/delete). That is a nuisance, not an
escalation, so the residual severity of the create-exemption **drops from ★★★ to ~★**. Fire 1's
create-guard nonetheless remains as hygiene (an actor should not be able to mint immutable junk), and
the two guards compose: no op sets `rootActor` (capability), no op creates a protected identity
(immutability hygiene).

### 3.4 Read/write paths, key shapes — unchanged

No new vertex/aspect/link types, no new lens, no new op, no orchestration. `cap.<actor>` /
`cap-read.root` doc shapes are byte-identical; only the **producing predicate** changes. Step-3's hot
path (one KVGet by actor class) is untouched — `rootActor` is read at *projection* time by the lens, not
on the authz hot path. Key shapes (Contract #1) unaffected.

---

## 4. The fork — marker shape (my recommendation: reserved top-level field)

All three candidates share the **same load-bearing element**: a Processor guard that forbids any op from
fabricating the marker. They differ only in the marker's *representation*, which decides (a) how clean
the guard is, (b) whether the lens stays a self-predicate, and (c) contract surface.

| Option | Lens predicate | Guard | Engine change | Contract surface | Verdict |
|---|---|---|---|---|---|
| **A. Reserved top-level field `rootActor`** (rec.) | `WHERE identity.rootActor = true` — self-predicate, one root-body read | **Unconditional**: no op sets a top-level `rootActor` | **None** (verified) | #1 §1.3 (+#6/#7) | **Recommended** |
| B. `isRootActor` link → kernel root-anchor | one-hop **join** in the hottest auth lens | link-create guard w/ endpoint-spoof surface | join seed path | #1 (new core link type) +#6/#7 | Fights Epic-12's self-predicate; more attack surface |
| C. Sealed `_`-prefixed aspect | aspect **point-read** per authz (`identity._root.data…`) | ban op writes to `_`-prefixed aspects (broad) | none | #6/#7 + enforce #1 §1.2 | Extra read; broad guard needs care |

**Why A wins.** It is the **smallest change that fixes all four defects**: out-of-band (not in `data`),
splits the overload, and — because *nothing but a root identity ever carries the field* — yields an
**unconditional** guard (no `:identity`-label or actor-tier branch). It **preserves the deliberate
self-predicate** (no join, no package vocabulary), so Epic-12's core-standalone-authorizable property is
untouched, and it needs **zero engine change** (verified at `executor.go:1416`). Its cost is one honest
line in the universal-envelope contract — and the envelope is the *correct* home for a platform-semantic
flag (it already houses `isDeleted`, the same kind of Processor-managed boolean).

**Why B loses.** Root-ness *is* conceptually a relationship, and Contract #1 says "relationships are
links" — but this specific relationship is to the kernel, for a fixed sealed set, read on the **hottest
authorization projection**. A link forces the capability lens back into a one-hop join (undoing the
Epic-12 decomposition that made it a self-predicate) and, worse, a link has **endpoints an attacker
could aim** — the guard must prove the source can't be spoofed, a strictly larger surface than "no op
sets this field." (There is already a latent `holdsRole → operator` topology on the service actors, but
core deliberately does **not** read it for the anchor, to stay rbac-independent — reviving it for the
anchor would re-introduce the very package dependency Epic-12 removed.)

**Why C loses.** A sealed `_root` aspect keeps the self-predicate and needs no envelope-contract change
(it rides the existing `_`-prefix reserved namespace), but it costs an **extra point-read per
projection** and forces a **broad guard** ("no op writes any `_`-prefixed aspect") whose blast radius I
can't fully bound without auditing every platform-generated aspect — a bigger, fuzzier guard than
option A's single-field rule. Kept as the fallback if Andrew would rather not touch Contract #1 §1.3.

---

## 5. Reconciliation with the existing mental model

- **"Didn't Epic-12 already settle this?"** Epic-12 settled the **read-side decomposition** (who
  projects which grant into which disjoint key) and *explicitly deferred* this as a **write-side
  create-gate carried obligation** ("This is a write-side create-gate concern, separate from this
  read-side decomposition"). This design is that obligation, discharged — plus the re-designation Andrew
  asked for on top of the bare guard.
- **"Doesn't `rejectProtectedMutations` already cover this?"** It covers **update/tombstone** (the
  anti-brick path) and **exempts create by design**. The exemption's stated reason (create-only
  conflicts on overwrite) is real for an *existing* root but vacuous for a *new* identity. This design
  closes exactly the create gap the existing guard leaves open.
- **"Are we adding new state?"** Fire 1 adds none. Fire 2 adds **one boolean envelope field**, set on
  the seven kernel identities that already exist — no new vertices/aspects/links, no new lens, no new
  op. The state that changes is a *predicate*, not the graph.
- **"Does this contradict the self-predicate decision?"** No — it **preserves** it (option A stays a
  self-predicate). It only moves *which field* the predicate reads, out of `data` and into a
  platform-managed envelope slot.
- **Stale-`cap.<actor>` eviction (the Epic-12 sibling obligation)** is **out of scope** here — it's the
  *downgrade/realness* path (a `true→false` transition leaving a stale doc), moot on the store-reset
  upgrade path, and orthogonal to the create-gate. Noting it so it isn't conflated; it stays its own
  backlog concern.

---

## 6. Decomposition for the Steward

Two fires, sequenced. Both pass the dead-scaffolding test (each realizes value with only components that
exist today).

**Fire 1 — create-guard floor + adversarial vectors. Size S. No contract change. Ships alone.**
- Extend step-8 to reject a create of a `data.protected:true` **identity-labelled** vertex (§3.1); typed
  `ProtectedKey` reply.
- **Gate-2 (bypass) vector — BLOCKED:** an ordinary actor submits an op emitting a `create` of
  `vtx.identity.<x>` with `data.protected:true` → Processor rejects (`ProtectedKey`); a companion
  case proves a `class:"identity"` non-`vtx.identity` key is caught too. Mirror the
  `internal/bypass/capadv_*` structure.
- **Gate-3 (capadv) vector — DEFENDED:** the full escalation — craft the malicious create, attempt it as
  a non-root actor, assert the capability lens never projects a `cap.<actor>` for the attacker (no root
  grants materialize). Add as the next capadv vector # (refresh the Makefile vector-count comment).
- Discharges the Epic-12 obligation. Independently green; requires no part of Fire 2.

**Fire 2 — re-designate onto `rootActor`. Size S–M. Behind the §4 fork + the contract edits.**
- Add `rootActor` to `substrate.DocumentEnvelope`; a bootstrap envelope path to set it; **contract edits
  staged uncommitted** (#1 §1.3, #6 §6.1/§6.14, #7 §7.2).
- Seed the seven root identities with `rootActor:true` (keep `data.protected:true`); bump the bootstrap
  seed version (forces `down && up`, a fresh store — so no in-place-migration eviction problem).
- Migrate the two lens predicates + `SystemActorKeys` (§3.2 step 3).
- Generalize the guard: no op sets a top-level `rootActor` (§3.2 step 4); keep Fire 1's guard as
  defense-in-depth.
- **Tests:** unit — the engine resolves `identity.rootActor` in a lens (pin the zero-change claim);
  bootstrap seed carries both flags; `SystemActorKeys` matches on the new field. Adversarial — an op
  setting `rootActor` (create *and* update) is rejected; the Fire-1 Gate-2/3 vectors re-pointed at the
  new field. e2e — the ephemeral stack still authorizes the admin + service actors for their root ops
  after migration (proves the predicate swap is behavior-preserving).

Sequencing rationale (against my own fewer-larger-fires reflex): they're coupled (same marker) but
**Fire 1 is the urgent ★★★ security floor with no contract dependency**, while **Fire 2 is a
contract-touching redesign gated on Andrew's shape ratification**. Splitting lets the floor land
immediately through the normal build path while the re-designation goes through the contract-ratification
gate — a principled split on *independent value + different ratification gates*, not size-padding.

---

## 7. Migration / compatibility

- **Fire 1** is purely additive enforcement — no data migration, no compatibility surface. Existing
  seeds already satisfy it (no op ever created a protected identity).
- **Fire 2** rides a **bootstrap seed-version bump**, whose supported path is `down && up` on a fresh
  store — so there is **no in-place field backfill** to worry about, and the Epic-12 "stale doc on a
  `true→false` transition" concern doesn't arise (no live store carries the old shape across the bump).
  The dev-loop `make up-full` reseeds; no vertical data depends on the marker (it's kernel-only).
- **Contract review is the ratification gate** — the three predicate sites + the seed move together in
  Fire 2, so there is never a window where a lens reads `rootActor` while the seed still writes only
  `data.protected` (both land in the same fire, behind the same store reset).

---

## 8. Risks + alternatives considered

- **Risk: a lens reads `rootActor` but the seed forgot to set it on some actor** → that actor silently
  loses root (fail-closed — a denial, not an escalation; caught by the Fire-2 e2e that authorizes every
  seeded root actor). Acceptable direction.
- **Risk: `omitempty` + a cypher `= true` on a missing field.** A root-absent `rootActor` resolves to Go
  `nil` → `truthy(nil)=false` → not matched (`executor.go:1465`). Verified the predicate degrades to
  deny on absence, not to a match.
- **Alternative — keep the bit, add only the create-guard (Fire 1 alone).** This *is* the interim floor
  and fully closes the escalation, but it leaves the bit in-band and overloaded — it does **not** answer
  Andrew's ask ("something other than the protected bit"). Fire 1 ships it as the floor; Fire 2 is the
  actual re-designation.
- **Alternative — enumerated kernel set injected as a lens param** (`WHERE identity.key IN $rootKeys`).
  Rejected: couples the lens to a bootstrap-injected list, awkward in cypher, and buys nothing over the
  self-predicate field while making the root set harder to read at a glance.
- **Marker shape alternatives (link / sealed aspect)** — §4.

---

## 9. Open questions — resolved

- *Shape?* → reserved top-level field `rootActor` (§4); link/aspect rejected with reasons.
- *Does the guard need an "unless root" branch?* → **No.** Nothing legitimately sets the marker via an
  op, so both guards are unconditional (Fire 1 scoped by identity-label; Fire 2 unconditional on the
  field).
- *Keep `data.protected`?* → **Yes**, narrowed to its single anti-brick meaning; the overload is what
  Fire 2 removes.
- *Contract surface?* → Fire 1 none (enforces existing intent; one clarifying sentence in #8 optional);
  Fire 2 amends #1 §1.3, #6 §6.1/§6.14, #7 §7.2 — staged uncommitted for Andrew.
- *Migration?* → seed-version bump / fresh store; no backfill (§7).

**Pre-build gate:** none self-imposed — this is an S / S–M security-plane change; the build's standard
full 3-layer adversarial review (the create-guard is a security boundary) is the gate, run at build time
by the Steward, not a deferred designer-lane pass.
