# Root-identity designation — design

**Status: ✅ Ratified 2026-07-02 (Andrew) — Fork A.** · Designer: Winston (architect) · 2026-07-02
Backlog row: `planning-artifacts/backlog/lattice.md` → *Arch-review intake* →
"root-designation-topology-reconverge". Source: `docs/reviews/arch-review-2026-07-02.md` ranked
correction #1 + the Epic-12 carried obligation (`docs/decisions/projection-plane-decomposition.md`).

**Ratification (2026-07-02, Andrew).**
- **Fork A** — re-converge core on the **`holdsRole → operator` topology** and retire `data.protected`
  as a capability designator (it keeps only its anti-brick meaning). Unforgeable by construction,
  matches the frozen contract, invents no new state.
- **Collapsed to ONE fire.** The design's Fire 1 (a `protected`-create-guard "interim floor") is
  **dropped**: Fork A closes the escalation *by construction* (a forged `protected:true` identity
  confers nothing after the predicate swap), so no interim floor is needed; its only residual value was
  anti-brick griefing (a forged immutable vertex), which is moot pre-prod — *nothing is deployed to
  brick*, and the real threat surface (untrusted `create`) does not exist until external actors write
  through the Gateway (revive-then, §6, no board row now). The **one capadv acceptance vector** —
  *forge a `protected:true` identity, assert it materializes neither a `cap.<actor>` write anchor nor a
  `*` read grant* — is retained; it is the proof A closed the hole and the regression tripwire, and
  belongs to A's fire (the create is *allowed* to succeed; the vertex just gets nothing).
- **Contract site corrected — the frozen edit is Contract #6 §6.1, NOT §7.7.** Due diligence at
  ratification found the draft had it backwards: **§7.7 is already correct** (pure topology; the code
  drifted away from it — no §7.7 edit needed) and **§6.1** is the *only* place in either frozen contract
  that enshrined the drift (`WHERE identity.data.protected = true`). §6.1 is edited to the `holdsRole →
  operator` primordial-topology anchor (and its now-false "core names no rbac vocabulary" claim narrowed
  to a package-*dependency* break), **committed with this ratification**. §6.1 and §7.7 previously
  contradicted each other; the edit makes them agree. (An optional `systemRoot`→`operator` naming nit in
  §7.2/§7.7 rides the broader docs-truth-sweep, not this fire.)

---

## The finding (retained analysis — the decision is in the Ratification block above)

**The finding (why the premise moved).** Root is designated **two different ways at once**, and they
disagree:
- **Contract #7 §7.7 (frozen, of-record)** says root = **`holdsRole → operator` topology**, verbatim:
  *"root capability is established by graph topology, not by class-based special-casing"* + a mandated
  test that an identity *without `holdsRole` topology does NOT get root*. The `protected` bit is **not
  in §7.7 at all**.
- **The shipped core code** designates root by `WHERE identity.data.protected = true` in three places —
  the write anchor (`CapabilityLensDefinition`, `lenses.go:124`), the read wildcard grant
  (`CapabilityReadWildcardGrantsLensDefinition`, `lenses.go:342`), and `SystemActorKeys`
  (`system_actors.go:60`). This is an **Epic-12 drift**: to keep core authorizable without rbac-domain,
  the god-cypher's `holdsRole` walk was replaced by the `protected` literal and *moved* to rbac-domain's
  `cap.roles.<actor>` lens. The 7 kernel actors now carry **both** markers; at runtime they authorize
  via the `protected` anchor (their `holdsRole → operator` is projected-but-unread).

**Why the bit is "easily compromisable" and the link is not.** `protected` is a boolean inside the
vertex's own `data`, set at create time, and identity-create is **unguarded** (step-8 exempts create).
The read wildcard is **unconditionally** forgeable (a plain projection, ungated by routing: forge a
`protected:true` identity → `*` read grant → read every RLS-protected row). The `holdsRole → operator`
topology, by contrast, is **self-protecting**: `AssignRole`/`GrantPermission`/`CreateRole` are granted
only to `operator` at `scope:any`, so **you must already be root to grant root** — it cannot be
bootstrapped from nothing.

**The decision (§4) — Fork A, ratified.** **(A) Re-converge core on the topological model** the contract
already mandates: designate root by a bounded `holdsRole → operator` existence check in the three core
sites, and **retire `protected` as a capability designator** (it keeps only its unrelated anti-brick
meaning). Closes both escalations *by construction*, uses the already-seeded self-protecting topology,
invents nothing. (B) keep + harden the `protected` anchor and (C) invent a `rootActor` field were the
alternatives — both double down on the data-derived mechanism Andrew flagged; see §4 for why they lost.

**Frozen-contract change (Fork A) — Contract #6 §6.1, committed with the ratification.** §6.1 was the
*only* frozen text carrying the drift (`WHERE identity.data.protected = true`); it is edited to the
`holdsRole → operator` primordial-topology anchor. **§7.7 needs no edit** — it already specifies the
topology model, and the code re-converges onto it (the draft's "§7.7 is the crux" was corrected at
ratification: §7.7 is the *correct* target, §6.1 is the drifted one). §6.1 and §7.7, previously in
conflict, now agree.

**Fire 1 (create-guard) — dropped at ratification.** Fork A closes the escalation by construction, so no
interim create-guard floor is needed; its only residual (anti-brick griefing via a forged immutable
vertex) is moot pre-prod and revives only when untrusted `create` exists (post-Gateway). The single
capadv acceptance vector (forge-`protected` → gets-nothing) is retained under A's one fire.

---

## 1. Problem + intent

### 1.1 What confers root — the two layers, precisely

Root capability = six platform grants at `scope:any` (`CreateMetaVertex`, `UpdateMetaVertex`,
`TombstoneMetaVertex`, `InstallPackage`, `UninstallPackage`, `UpgradePackage`) **plus** the wildcard
`*` read anchor. The primordial seed establishes this for 7 identities (the admin + Loom / Weaver /
Bridge / object-store-manager / privacy service actors) via **two redundant mechanisms**, both seeded
in `internal/bootstrap/primordial.go`:

1. **Topological (the contract's model, §7.7).** A full operator role is seeded: the `operator` role
   vertex, six `permission` vertices (the six ops above), six `grantedBy` links (permission → operator),
   and a `holdsRole → operator` link from each of the 7 identities (`primordial.go:704-766`, entries
   10 + 10a). rbac-domain's `capabilityRoles` lens walks `identity -[:holdsRole]-> role <-[:grantedBy]-
   permission` (`packages/rbac-domain/lenses.go:72`) and projects the grants into `cap.roles.<actor>`.
2. **The `protected` literal (the Epic-12 core anchor).** Post-Epic-12, core's own three sites key on
   `data.protected = true` so core is self-sufficient without rbac-domain installed:
   - `CapabilityLensDefinition` (`lenses.go:124`) → `cap.<actor>` write grants (actor-aggregate,
     `WHERE identity.data.protected = true`).
   - `CapabilityReadWildcardGrantsLensDefinition` (`lenses.go:342`) → the `*` read anchor (a **plain**
     full-graph projection, `WHERE identity.data.protected = true`, one row per matching identity).
   - `SystemActorKeys` (`system_actors.go:60`) → a **startup scan** for `protected==true` identities,
     wired once into step-3 routing (`cmd/processor/main.go:124`).

**Runtime routing** (`step3_auth.go:183`, `classAwarePlatformKey(SystemActorKeys)`): when rbac-domain is
installed (the standard stack — `Makefile:449`), the actors in the startup `SystemActorKeys` snapshot
read `cap.<actor>` (the `protected` anchor); every other actor reads `cap.roles.<actor>`. When
rbac-domain is absent, **all** actors read `cap.<actor>`. So the seeded actors always authorize via the
`protected` anchor; their `holdsRole → operator` topology feeds a `cap.roles.<actor>` doc that is
**projected but never read** for them.

### 1.2 The threat model (corrected — conditional writes, unconditional reads)

The escalation is "cause a `protected:true` identity to exist." Its power splits:

- **Read path — unconditional.** The read wildcard lens is a plain projection keyed on `protected=true`,
  with **no routing gate**. A forged `protected:true` identity gets a `*` wildcard read grant →
  every RLS-protected read-model row is readable. Immediate, independent of rbac, independent of restart.
  *(This is the clean, always-live exploit — the first draft under-weighted it.)*
- **Write path — conditional.** `SystemActorKeys` is a **startup snapshot**, so a runtime-forged
  protected identity is not in it: with rbac installed it routes to `cap.roles.<forged>` → no
  `holdsRole` → **denied**. But it escalates (a) with rbac **absent** (all actors read `cap.<actor>` →
  the `protected` anchor projects root), or (b) after a **Processor restart** re-snapshots
  `SystemActorKeys` and pulls the forged identity into the `cap.<actor>`-routed set.

### 1.3 Why the bit is the weak designator (and the link is not)

- **In-band + create-unguarded.** `protected` sits in the vertex's own `data`; `rejectProtectedMutations`
  (`step8_commit.go:135`) guards **update/tombstone** but **exempts create** (`step8_commit.go:134`) —
  sound for overwriting an existing root, vacuous for minting a new identity. Only identity-domain's
  `data:{}` convention (`ddls.go:399`) stops it today — the Epic-12 carried obligation.
- **The topology is self-protecting.** Forging root topologically means creating
  `lnk.identity.<x>.holdsRole.role.<operatorId>`, which only `AssignRole` writes — and `AssignRole`,
  `GrantPermission`, `CreateRole` are all `GrantsTo:["operator"] scope:any`
  (`packages/rbac-domain/permissions.go:25`). **You must already hold operator to grant operator.** Root
  cannot be bootstrapped from nothing. This is the property `protected` structurally lacks.

### 1.4 Intent

Designate root by a mechanism that is (a) **out-of-band** from business data, (b) **unforgeable without
already being root**, and (c) **consistent with the frozen contract**. The contract already describes
such a mechanism (§7.7 topology); the work is to make the shipped code match it, and to guard the mint
path in the interim.

---

## 2. Grounding — contract, drift, and the invariant tension

- **Contract #7 §7.7 is the of-record designation and it is topological.** It instructs the capability
  cypher to *"Walk identity → `holdsRole` → role"* and *"walk inbound `grantedBy` links from the role to
  discover permission vertices"*, and states *"root capability is established by graph topology, not by
  class-based special-casing."* The shipped `protected` anchor contradicts this — so **§7.7 vs. the code
  is a live, untracked contract-vs-code drift** that this design must resolve either way.
- **Epic-12's reason for the drift was real but narrower than it looks.** The decomposition removed the
  `holdsRole/role/permission/grantedBy` walk from **core's** cypher so core "references no rbac
  vocabulary" and stays authorizable when the rbac-domain **package** is absent
  (`06-capability-kv.md` §6.1). But the operator-role topology is **primordial (core-seeded)** — it
  exists in the graph with or without the rbac-domain package. So a core anchor that walks the
  *primordial* topology is **package-independent**; what Epic-12 actually bought was cypher-vocabulary
  cleanliness, not a genuine dependency break. That reframes Fork A's cost as *nominal vocabulary
  coupling to primordial concepts*, not *a package dependency* (§4).
- **The self-predicate is cheap to preserve as a bounded check.** Epic-12 feared an "unbounded whole-type
  scan." "Does identity X hold the operator role" is not that — it is a **single deterministic outbound
  link check** from one anchor (`MATCH (i:identity {key:$actorKey})-[:holdsRole]->(r:role {…operator})`),
  the same bounded traversal the full engine already runs and the same class of bounded op-time link read
  the write-path-read posture now sanctions. No scan.
- **Invariants unaffected:** the seed stays the sanctioned direct-write (P2); every runtime mutation
  flows through the Processor; no engine gains a new Core-KV read (the check runs in the Refractor
  projection, where the capability lens already runs); key shapes (Contract #1) unchanged.

---

## 3. The shape

### 3.1 The one fire (Fork A, ratified) — re-converge core on the topology, retire `protected` as designator

Replace the `protected` predicate with a bounded `holdsRole → operator` check in the three core sites:

1. **`CapabilityLensDefinition`** (`lenses.go:124`) — anchor the root grant set on
   `MATCH (identity:identity {key:$actorKey})-[:holdsRole]->(:role {canonicalName:'operator'})` instead
   of `WHERE identity.data.protected = true`. (Grants stay a literal set — this only changes the *gate*,
   not the projected shape.)
2. **`CapabilityReadWildcardGrantsLensDefinition`** (`lenses.go:342`) — same gate swap; the `*` read
   grant now flows only to operator-holders. **This is the fix that closes the unconditional read
   escalation.**
3. **`SystemActorKeys`** (`system_actors.go:60`) — discover the root set by the `holdsRole → operator`
   topology instead of scanning `data.protected` (the link keys
   `lnk.identity.<id>.holdsRole.role.<operatorId>` are directly enumerable — no per-vertex `KVGet`).
   **Kept, not replaced** — its job is unchanged (a startup snapshot that routes each actor at step-3 to
   `cap.<actor>` vs `cap.roles.<actor>`); only its predicate moves with the other two sites, so routing
   and projection never disagree on what confers root. Snapshot staleness stays benign under A: both docs
   derive from the same topology, so a runtime-granted operator authorizes via `cap.roles.<actor>` until
   the next restart re-snapshots — the write-path forgery window the bit had (restart pulls a forged
   identity into the anchor set) is closed.

`data.protected` is **retired as a capability designator** and keeps **only** its anti-brick meaning
(the `rejectProtectedMutations` update/tombstone guard, unchanged). After the swap, forging `protected:true`
grants **nothing** — capability is conferred solely by operator-role topology, which is self-protecting.

**Acceptance vector (retained from the dropped Fire 1).** A Gate-3 (capadv) DEFENDED vector: forge a
`protected:true` identity as a non-root actor (the create is *allowed* to succeed) and assert it
materializes **neither** a `cap.<actor>` write anchor **nor** a `*` read grant. This proves the swap
closed the escalation and is the regression tripwire against any future protected-keyed predicate.

**Contract:** **#6 §6.1** is edited to the `holdsRole → operator` primordial anchor (committed with the
ratification — see the Ratification block). **#7 §7.7 needs no edit** — it already specifies the topology
model; the code re-converges onto it. No new envelope field, no new vertex/aspect/link type, no new op.

### 3.2 Why not the create-guard, and why not Forks B/C

The **create-guard** (draft Fire 1 — reject a `create` minting `data.protected:true` on an identity) is
**dropped at ratification**: Fork A makes a forged `protected` identity confer nothing, so the guard's
escalation job is subsumed; its only residual is anti-brick griefing (a forged *immutable* vertex), moot
pre-prod (nothing deployed to brick) and gated on an untrusted-`create` surface that does not exist until
external actors write through the Gateway — revive it then (no board row now; §6). **Forks B (harden the
bit) / C (new `rootActor` field)** both keep a data-derived designation and force §7.7 to be rewritten
*away* from the model it correctly specifies — see §4 for the full comparison and why A won.

### 3.3 Read/write paths, key shapes — unchanged

The `cap.<actor>` / `cap-read.root` doc shapes are byte-identical to today; only the **gate predicate**
changes. Step-3's hot path (one KVGet by actor class) is untouched — the gate is evaluated at projection
time, not on the authz hot path.

---

## 4. The fork — topological re-convergence (A ✅ ratified) vs. hardened marker (B) / new field (C)

| | **A. Re-converge on `holdsRole → operator`** ✅ **ratified** | **B. Harden the `protected` marker** | **C. New `rootActor` field** (first draft — retired) |
|---|---|---|---|
| Designation | Link topology (contract §7.7) | Reserved seed-only bit/field | Reserved seed-only envelope field |
| Forgeable? | **No — self-protecting** (grant-role is root-gated) | No, once guarded (but create-guard is the load-bearer) | No, once guarded |
| Read escalation | Closed **by construction** | Closed by the guard | Closed by the guard |
| Invents mechanism? | **No** — already seeded | A new marker | A new marker (a *third* one) |
| Frozen-contract edit | **#6 §6.1** (swap the anchor predicate); **§7.7 already correct — no edit** | **Rewrite §7.7** away from topology + amend #1 §1.3 | Rewrite §7.7 + amend #1 §1.3 |
| Cost | Nominal rbac-*vocabulary* in core's cypher (primordial concepts; not a package dep) | Keeps a data-derived root-of-trust; two markers to keep coherent | Same as B, plus it ignores the contract's existing link model |

**Recommendation: A.** It is **unforgeable by construction** (you cannot grant yourself operator without
already holding it), it **matches the frozen contract** instead of rewriting it away, and it **invents
nothing** — the topology is already seeded and already walked by rbac-domain. Its only real cost is
re-admitting `holdsRole`/`operator` vocabulary into core's cypher, and §2 shows that coupling is to
**primordial** concepts, not to the rbac-domain package — so core stays authorizable standalone. B and C
both double down on the data-derived designation Andrew flagged as easily compromisable, and both force
§7.7 to be rewritten *away* from the model it correctly specifies.

**Why C (my first draft) lost.** It proposed a new reserved `rootActor` envelope field — a *third*
designation mechanism, data-derived, requiring a Contract #1 envelope amendment — while the contract
already specifies a link-based, self-protecting mechanism that is already in the seed. It was the
"greenfield a new mechanism where the codebase already decomposed" reflex; Andrew's grounding challenge
surfaced it. Retained here only to record the rejection.

---

## 5. Reconciliation with the existing mental model

- **"Isn't root already `holdsRole → operator`?"** Yes — per Contract §7.7 and the seed, and that is
  exactly the point of Fork A. The shipped `protected` anchor is an Epic-12 drift *away* from that model;
  A restores it. (This is the question Andrew's challenge raised, and it reframed the whole design.)
- **"Didn't Epic-12 deliberately remove the graph walk from core?"** It removed it to avoid a **package**
  dependency on rbac-domain and to keep an unbounded scan out of the hot path. §2 shows the operator
  topology is **primordial** (no package dep) and the check is a **bounded** one-key traversal (no scan),
  so A recovers the contract model without reincurring what Epic-12 was actually avoiding. What A does
  reverse is the narrower "core's cypher names no rbac vocabulary" preference — which was the ratification
  crux (ratified 2026-07-02: A).
- **"Are we adding state?"** Fork A adds **none** — it re-gates on topology that is already seeded. (Only
  the retired Forks B/C would add a marker.)
- **Stale-`cap.<actor>` eviction** (the Epic-12 sibling obligation) is out of scope — a downgrade/realness
  concern, moot on the store-reset upgrade path, orthogonal to designation.
- **Redundancy note.** Under A, the seeded actors' `cap.<actor>` (core anchor) and `cap.roles.<actor>`
  (rbac projection) would both derive from the same `holdsRole → operator` topology — consistent, not
  conflicting. A later simplification *could* drop the core anchor for actors once rbac is guaranteed
  present, but core self-sufficiency argues for keeping the core-owned anchor; that trade is notable but
  not in this scope.

---

## 6. Decomposition for the Steward

**One fire (Fork A). Size S–M. Full 3-layer adversarial review (security plane).** The draft's two-fire
split existed only to ship a create-guard floor ahead of an undecided fork; with A ratified and the
create-guard dropped (§3.2), it collapses to a single fire.

- **Predicate swap.** Move the three core sites (`CapabilityLensDefinition` `lenses.go:124`,
  `CapabilityReadWildcardGrantsLensDefinition` `lenses.go:342`, `SystemActorKeys` `system_actors.go:60`)
  from `WHERE identity.data.protected = true` to the bounded `holdsRole → operator` check (§3.1). The
  three move **together** — no window where one site reads topology while another reads the bit.
- **Contract.** The **#6 §6.1** edit is **already committed** with this ratification (the anchor
  predicate + the narrowed "no rbac vocabulary" claim); the Steward builds to it. **No §7.7 edit.**
- **Unit tests.** The anchor + read wildcard project root **iff** the identity holds operator, and
  **not** for a `protected:true`-only identity (the inverse of the drifted test — proving the drift is
  closed); `SystemActorKeys` discovers the root set by topology.
- **e2e.** The ephemeral stack still authorizes the admin + the service actors for their root ops after
  the swap (behavior-preserving); a forged `protected:true` identity gets **neither** write nor read root.
- **Gate-3 (capadv) DEFENDED — the retained acceptance vector.** Forge a `protected:true` identity as a
  non-root actor (the create is *allowed* to succeed) → assert **no** `cap.<actor>` and **no** `*` read
  grant materialize. Add as the next capadv vector #. (No Gate-2 BLOCKED vector — with the create-guard
  dropped, the forged create is not rejected; the escalation is defended at projection, not at the mint.)

**Deferred (no board row):** the `protected`-create-guard revives only when an **untrusted `create`**
surface exists (external actors writing through the Gateway) — filing it now would be scaffolding for a
threat that cannot yet occur (§3.2).

---

## 7. Migration / compatibility

- **The fire (A)** changes only lens **predicates** + the `SystemActorKeys` discovery query; the seed
  already carries the operator topology, so **no re-seed is required for correctness** (the topology
  exists). A bootstrap-version bump is still the clean path if the seed drops the now-vestigial
  `protected:true` from the 7 identities — but note `protected` is **retained** on them for anti-brick,
  so the seed need not change at all; only the code predicates move. This makes A a notably low-risk
  migration (no store reset strictly required — a projection rebuild suffices, since the topology is
  already present).
- **The three predicate sites move together** — there is no window where one site reads topology while
  another reads the bit.

---

## 8. Risks + alternatives considered

- **Risk (A): a seeded actor lacks the `holdsRole → operator` link** → it loses root (fail-closed — a
  denial, caught by the e2e that authorizes every seeded root actor). All 7 are seeded with it
  (`primordial.go:704-766`), so this is a regression guard, not a live gap.
- **Risk (A): the operator role canonicalName resolution.** The check keys on the operator role; pin it
  by the seeded role NanoID or its `canonicalName` aspect (`role.canonicalName.data.value == "operator"`)
  — the same resolution rbac-domain's lens already uses — not a brittle string in the vertex root.
- **Alternative — keep the bit, add only a create-guard.** Closes the write-mint hole but leaves the
  **unconditional read wildcard** open and does not answer "something other than the bit." Rejected as a
  standalone: A closes both by construction, so the create-guard is neither necessary nor sufficient (§3.2).
- **Alternative — enumerated kernel set as a lens param.** Rejected: couples the lens to a
  bootstrap-injected list; buys nothing over the topology.
- **Fork B / C** — §4.

---

## 9. Open questions — all resolved (Fork A ratified 2026-07-02)

- *Which mechanism actually confers root?* → Two, redundantly; the seeded actors authorize via the
  `protected` anchor at runtime, but the **contract designates the `holdsRole → operator` topology** and
  the `protected` anchor is an Epic-12 drift (§1.1).
- *Is the bit really compromisable?* → **Yes, unconditionally on the read path** (wildcard grant),
  conditionally on writes (rbac-absent or post-restart) (§1.2).
- *Shape?* → **Fork A, ratified** (§4): re-converge on the self-protecting topology; retire `protected`
  as designator. B/C (data-marker alternatives) rejected.
- *Contract surface?* → **Contract #6 §6.1** (swap the anchor predicate) — committed with the
  ratification. **§7.7 needs no edit** (it already specifies topology; the code re-converges onto it).
  The draft's "§7.7 is the crux" was corrected at ratification (§6.1 is the drifted section).
- *Migration?* → predicate-only, no re-seed required (topology already seeded) — a projection rebuild
  suffices.

**Pre-build gate:** none self-imposed — an S / S–M security-plane change; the build's standard full
3-layer adversarial review is the gate (run at build time), given the capability-anchor + read-wildcard
surface.
