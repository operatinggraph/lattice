# Global identity for a hyperscale tenant (cross-cell shadows + cross-region residency) — design

**Status: 📐 awaiting-Andrew (ratification).** Author: Winston (Designer fire, 2026-06-30). The named open
extension the **multi-cell** ratification (2026-06-29) carved out of its core and routed to a dedicated
follow-on. Backlog row: `planning-artifacts/backlog/lattice.md` → *Scale-out → Global identity for a
hyperscale tenant* (★ now / ★★★ at hyperscale, L–XL).

Grounds in: the **ratified multi-cell design** `multi-cell-sharding-design.md` (the Ratified block's
two-axis open extension + §3.4 bridge links / cell-local lenses + §3.5 Loom saga + §3.6 the "never split an
*atomically-related subgraph*" correction); the **ratified D1 read-path** `read-path-authorization-d1-design.md`
+ **Contract #6 §6.14** (the cross-cell **consistency contract** this design hinges on — `cap-read.*` slices,
`actor_read_grants`, `projectionSeq` monotonic guard, soft-tombstone revocation, token-revocation KV, the
`nanoIdFromKey` pseudonymous anchor); **Contract #6 §6.1/§6.3/§6.8** (Capability KV, the *no per-op
freshness-ceiling* frozen decision, "no entry = no access"); **Contract #9** (identity claim flow — PII shape);
the **vault** `Obsidian Vault/Lattice/Edge Lattice/Edge Lattice.md` ("Shadow Vertexes: anchors for local links
without requiring the device to download the entire remote dataset" — the precedent reused server-side) +
`Sharding/Cell.md`; the **brainstorming inventory** §160–173 (items **77–89**, esp. #78 anchor principle, #79
the index, #80 bridge links) + §199 (#109); the operations doc `deployment-isolation.md` (NFR-SC2 cell-agnostic
keys, per-cell Refractor/Capability-KV/Postgres isolation); `lattice-architecture.md` (**P1/P2/P5**, NFR-SC2/P6).
A self-adversarial pass ran as part of this fire; its findings are folded into §6.

---

## For Andrew (one-look ratification block)

**What it does, in two lines.** Lets a **hyperscale tenant** (WeWork — coworking, hundreds of properties; Flow
— branded residential) whose **membership is global across the tenant** span many cells *without* the
tenant-global identity becoming a cross-cell read hotspot: the canonical identity (with PII) stays in one
**home cell**, and a **PII-stripped pseudonymous shadow** carrying only the member's *tenant-global membership
grants* is replicated into each cell where the member acts — so each cell's **existing** step-3 auth + D1 RLS
authorize the member **locally**, turning would-be cross-cell links into local ones. Going global (Axis 2) adds
a **residency dimension to placement** and reshapes the shadow into a strictly PII-stripped anchor; it does
**not** fork the core model.

**No architectural fork; NO frozen-contract change required.** This is the rare ambitious design that needs
**zero contract edits** — and that is itself the headline ratification signal, so I call it out explicitly:
- **Key shapes (Contract #1 §1.1): unchanged.** A shadow reuses the **same** `vtx.identity.<id>` key
  (NFR-SC2 — keys embed no cell id; the home cell + shadow-cell set live in *operational* state, never a key);
  a `.shadow` aspect (`vtx.identity.<id>.shadow`, 4-seg) marks a replica vs. the canonical.
- **Error enumeration (Contract #2 §2.6): no new code.** A first-act *cache miss* is a **transparent
  Gateway hydrate-and-retry** (not a client-observable reject); a **stale-wrt-revocation** shadow degrades to
  the **existing** `AuthDenied` via §6.8 "no entry = no access." (Sub-option: an advisory `ShadowHydrating`
  §2.6 code if you prefer an explicit signal over transparent retry — I recommend **transparent**, no edit;
  §4.)
- **Capability KV (Contract #6): build-to.** The shadow's grants ride the **existing** `cap.<actor>` /
  `cap-read.*.<actor>` / `actor_read_grants` machinery, the **existing** `projectionSeq` monotonic guard, the
  **existing** soft-tombstone revoke, and the **existing** token-revocation KV. The revocation fast-path is a
  **monotonic generation comparison** (registry-side), **not** a wall-clock age check — so it does **not**
  contradict §6.3's frozen "the Processor performs no per-operation projection-age check."

**The one deliberate, scoped invariant exception (flagged for your ratification).** Multi-cell holds
"one vertex lives in exactly one cell" (the cell-index maps `vertex → one cell`). This design makes the
**`identity` type the single multi-homed exception**: a shadowed identity's index entry becomes
`{homeCell, shadowCells:[…]}`. The exception is **justified** (the tenant-global identity is *the* high-fan-in
hub §3.6 says the anchor principle cannot co-locate), **bounded** (only `identity`, only its pseudonymous
auth-relevant slice, shadows are **read-only** in acting cells), and **fail-safe** (a stale shadow under-permits
on additions, fail-closes on revocations). **Your call:** ratify the multi-homed-identity exception (recommend
yes — it is the minimum that dissolves the hub problem), or direct a heavier "dedicated identity tier"
(§6.2, rejected as a hot-path read hotspot).

**The consistency contract is the real content (not the mechanism).** Per the multi-cell Ratified block + the
stress-test memory: **grant additions lag** (eventually-consistent shadow refresh — *under*-permit, safe);
**grant revocations are globally fast** (a per-actor **revocation generation** in the registry, bumped on
revoke, compared at auth — *fail-closed*); **whole-actor cut** is instant (the existing token-revocation KV).
**Residency is fail-closed**: home-cell placement is residency-governed, an **absent residency tag ⇒
most-restrictive**; canonical PII never auto-replicates across borders (the shadow is PII-stripped); **air-gapped
sovereign jurisdictions (PIPL/Russia) are a federation case, not a shadow case** (§5, the named hard edge).

**Build sequencing (honest — ratify-now / build-on-driver).** This sits on **unbuilt multi-cell** (itself
shelved behind Gateway + HA-NATS + a real scale driver). You cannot shadow across cells that do not exist, so
**every fire here sequences behind multi-cell Fire 2** (the Global Adjacency Index + Gateway router) **plus a
real hyperscale driver** (none at 10–100 ops/sec / ≤100K keys / ~500 members — a single cell is orders of
magnitude within ceiling). The recommendation mirrors multi-cell/Vault/D1: **ratify the design, shelve the
build** behind its prerequisites + driver. No fire is buildable now (the would-be "Fire 0" pseudonymous-anchor
discipline is already shipped by D1's `nanoIdFromKey`), so there is **nothing to start** — "the design is ready
and sequenced" is the correct output.

---

## 1. Problem + intent

**Multi-cell co-locates an atomically-related subgraph in one cell** (the Anchor Principle, brainstorm #78:
"a vertex's aspects + outgoing links MUST be co-located"). That is what keeps the Processor's atomic batch
(Contract #2 step-8) atomic and keeps reads/auth cell-local. Multi-cell §3.6 was corrected at ratification:
the honest rule is **"never split an *atomically-related subgraph*," not "never split a tenant."**

A **hyperscale tenant** breaks the convenient case where tenant == subgraph. WeWork (hundreds of properties)
or Flow (branded residential) has **membership global across the tenant** — one member can act at *any*
property. Such a tenant is **too big for one cell** and **must** span cells (1:N at the tenant level, which
§3.6 now permits *as long as each atomically-related subgraph stays whole*). The casualty is the
**tenant-global identity**: the member's `vtx.identity.<id>` is a **high-fan-in hub** referenced from every
cell the member acts in — exactly the thing the anchor principle **cannot** co-locate. Naïvely, every op the
member performs anywhere would have to reach the member's **home cell** to resolve their capability — making
the home cell a **read hotspot** and coupling every cross-cell op to home-cell latency + availability.

Two axes (the multi-cell Ratified block named both; the stress-test memory warns "global X is overloaded"):

- **Axis 1 — cross-cell, within one deployment/region.** Identity spans cells. The hub/hotspot problem above.
- **Axis 2 — cross-region + data residency, going global.** Identity spans **jurisdictions** under residency
  law (GDPR, etc.). PII must not auto-cross borders; placement must be residency-aware and fail-closed.

**Intent:** realize tenant-global identity by **reusing the architecture's own shadow-vertex concept**
(Edge Lattice: "a read-only anchor for a remote entity") **server-side, cross-cell** — replicating only the
member's **pseudonymous, PII-stripped, tenant-global membership slice** into the cells where they act, so each
cell's **existing** per-cell Refractor + Processor + D1-RLS authorize the member locally with **no cross-cell
read on the hot path**, **no new contract surface**, and a **consistency contract** (additions lag,
revocations fast, residency fail-closed) that is the design's real substance.

## 2. Reconciliation with the existing mental model (didn't we already…?)

- **"Isn't this just multi-cell bridge links?"** *No.* A bridge link (§3.4) resolves a cross-cell *link* (a
  cell-B lease pointing at a cell-A identity) — it makes the **edge** resolvable. It does **not** make the
  member **authorizable** in cell B: to *submit* an op in cell B the member needs cell-B `cap.<actor>` /
  `cap-read.*`, which depend on the member's identity + global grants living in cell A. The shadow replicates
  exactly that auth-relevant slice, so cell B's **unchanged** step-3 + RLS work; and it converts the member's
  would-be cross-cell links into **local** links (the shadow is locally present), preserving cell-local atomic
  batches. Bridge links and shadows compose; they solve different halves.

- **"Isn't this the Personal / Secure Lens?"** *No — orthogonal, shared substrate.* The Personal Lens
  (`personal-secure-lens-design.md`) projects a per-identity **read** stream *out to an Edge device*, gated by
  D1 `cap-read.<actor>` / `readableAnchors`. The shadow is a *server-side, cross-cell* replica of the
  identity's auth slice for *local authorization*. Both stand on D1's `readableAnchors` join; the directions
  differ (device-bound read fan-out vs. cell-bound auth replication).

- **"Isn't this Edge Lattice's shadow vertex?"** *Yes — the same concept, reused server-side.* Edge Lattice
  keeps "Shadows of remote entities… anchors for local links without downloading the entire remote dataset"
  on a *device*; this applies the identical idea **cell-to-cell**. Deliberate precedent reuse, not a new
  invention. (The vault overloads "shadow" — uniqueness-index in `Constraints.md`, migration-destination in
  `Cell.md`; the one meant here is **Edge Lattice's remote-entity anchor**.)

- **"Does it introduce new persistent state, and is it shaped like state we already keep?"** Two artifacts.
  (1) The **Global Identity Registry** — a **replicated operational KV** (the same family as multi-cell's
  `cell-index` / `cell-registry`, Health KV, Weaver-state) mapping an identity's **bare NanoID** →
  `{homeCell, shadowCells:[…], residency, revocationGen}`. **P1 operational state**, not Core KV, not a lens.
  (2) The **shadow** itself — a **read-only Core-KV replica** (`vtx.identity.<id>` + a thin grant slice + a
  `.shadow` marker aspect) in each acting cell, written via ops (P2) and projected by that cell's own
  Refractor (P5), so the existing machinery needs no change.

- **"Doesn't this duplicate the 'dedicated identity tier' we rejected (multi-cell block: 'weaker')?"** *No —
  it is the thing the block called the right answer ("shadows ≈ cache the identity tier into its consuming
  cells").* The rejected tier serves auth on the **hot path** (every cross-cell op reads the tier → the
  hotspot the shadow exists to avoid). The Registry here is a **thin pseudonymous index** (home + shadow set +
  residency + a revocation generation) consulted on the **cold path** (materialize) and for the **fast
  revocation check** (a *local replicated* read) — **never** to serve a grant on the hot path. Grants are
  cached as cell-local shadows; auth stays local.

- **"Does this contradict §6.3's 'no per-op freshness ceiling'?"** *No — the revocation guard is a
  **monotonic generation** comparison, not a wall-clock age check.* §6.3 froze "the Processor performs no
  per-operation projection-age check" because in a single cell the home Refractor keeps capability monotonically
  current. A shadow can lag arbitrarily if its refresh stalls, so revocation safety needs a bound — but the
  bound is a **revocation-generation** compare against the Registry (the §6.2 monotonic-guard idiom), not a
  timestamp. Additions don't bump the generation (so they simply lag — under-permit); revocations do (fast,
  fail-closed). §6.3 is untouched.

## 3. The shape

Six pieces: the home cell + canonical PII; the pseudonymous shadow (what it carries / what it omits); the
Global Identity Registry; the materialize-and-refresh transport (the Loom/Weaver saga); the consistency
contract (additions lag / revocations fast / whole-actor cut); and the auth read path.

### 3.1 The home cell + canonical identity (PII stays home)

- Every `vtx.identity.<id>` has a **home cell** (its entry in the multi-cell `cell-index`, like any root).
  **All canonical PII aspects** — `.contact` (name/email/phone, Contract #9), `.claimKey`, and any
  Vault-encrypted sensitive aspect (SSN/DOB) — live **only** in the home cell and are **never** replicated
  out. Identity **mutations** (`UpdateIdentityContact`, role grants, `ClaimIdentity`) route to the **home
  cell** via the index (the existing multi-cell router, §3.3) and commit there atomically.
- The home cell is chosen by a **residency-aware placement policy** (§5): the member's residency tag → a home
  cell in a matching jurisdiction. This is the multi-cell §3.1 placement policy, **extended with residency**.

### 3.2 The pseudonymous shadow (carry the minimum; omit all PII)

When a member acts in a non-home cell B, a **shadow** of their identity is materialized there:

- **Identity:** the **same** `vtx.identity.<id>` key (NFR-SC2 — bucket-scoped; no cell id in the key). The
  identity's **bare NanoID** (via the shipped `nanoIdFromKey`, Contract #6 §6.14) is **already pseudonymous**
  — it carries no PII and is the globally-unique join token D1's grant match already uses. The shadow **is**
  that pseudonym; no extra pseudonymisation is invented.
- **Marker:** a `.shadow` aspect `vtx.identity.<id>.shadow = { isShadow:true, homeCell, projectedGen,
  projectionSeq }`. The canonical home copy carries **no** `.shadow` aspect. Refractor, Loupe, and any reader
  distinguish replica vs. canonical by this aspect (never by cell id).
- **Carries ONLY the tenant-global membership slice:** the member's **tenant-global grants** — the
  membership-derived roles/permissions that apply *across* the tenant (e.g. "WeWork member ⇒ may book any
  WeWork common service"). Concretely, the slice is the projection inputs the home cell's grant lenses
  produced for **tenant-global** scope, replicated as a thin set of aspects/links on the shadow so cell B's
  **own** Refractor projects `cap.<actor>` / `cap-read.*.<actor>` from local state.
- **Omits everything else.** No PII (name/email/SSN — stay home, §3.1). No **cell-local** grants: a member's
  lease/booking in cell B is a **cell-B-native** entity that links to the shadow; cell B's Refractor projects
  the *local* grant from that *local* link with **no shadow involvement**. The shadow is exactly the
  *tenant-global membership* slice — the part that is genuinely the cross-cell hub — and nothing more.
- **Read-only in the acting cell.** Only the refresh saga (§3.4) writes a shadow, via ops to cell B's
  Processor (P2 honored). The member cannot mutate their identity through the shadow — those ops route home
  (§3.1). The shadow is the Edge-Lattice "read-only anchor," server-side.

**Why this is the minimum that dissolves the hub.** Local relationships and local grants are *already*
cell-local and need no replication; only the *tenant-global membership identity + its global grants* is the
high-fan-in hub. Replicating exactly that — pseudonymous, PII-free, read-only — is the smallest thing that
makes a member locally authorizable everywhere without a home-cell hotspot.

### 3.3 The Global Identity Registry (replicated, pseudonymous, P1 operational state)

A replicated operational KV `identity-registry` (R3; the multi-cell `cell-index`/`cell-registry` family),
keyed by the identity's **bare NanoID** (pseudonymous — globally replicable without leaking PII):

```
identity-registry[<identityNanoId>] = {
  homeCell:      "<cellId>",
  shadowCells:   ["<cellId>", …],   // exactly the cells holding a live shadow (the refresh fan-out address list)
  residency:     "<jurisdictionTag>",
  revocationGen: <monotonic int>     // bumped on ANY grant revocation for this actor (the global fast-path)
}
```

- **Pseudonymous + PII-free** ⇒ globally replicable, including across regions (Axis 2), with no residency
  violation (it carries a jurisdiction *tag*, never PII).
- **`shadowCells`** is the **explicit transport address list** for refresh/retraction fan-out (§3.4) — the
  refresh is *targeted*, never a broadcast.
- **`revocationGen`** is the consistency contract's fast-path (§3.5). It is a **generation counter**, not a
  timestamp (§2 reconciliation — keeps §6.3 intact).
- **Not on the hot path for grants.** Steady-state auth in cell B reads cell B's *local* `cap.<actor>` (the
  shadow's projection). The Registry is read (a) cold, to materialize a shadow, and (b) cheaply (local
  replicated GET) for the revocation-generation compare. It is **not** a grant-serving tier.

### 3.4 The materialize-and-refresh transport (Loom saga + Weaver fan-out — naming the precedent)

A shadow's lifecycle is **explicit transport**, mirroring multi-cell §3.5 (Loom cross-cell saga) and the
Weaver convergence/fan-out pattern — *never* "resolved from context."

- **Materialize on first act (cache miss).** The Gateway routes a member's op to cell B; if cell B has no
  live shadow (no local `cap.<actor>` for the member, and the actor's home ≠ B per the Registry), the Gateway
  triggers a **shadow-hydrate Loom saga**: read the member's tenant-global slice + `revocationGen` from the
  **home cell** (a *cold* cross-cell read inside the saga — the home cell is the source; this is the only
  cross-cell read, and it is off the steady-state hot path), then **submit a `MaterializeShadow` op to cell
  B's Processor** writing the shadow anchor + the global-grant slice + the `.shadow` marker (stamped with the
  home `revocationGen` + the op's `projectionSeq`). Cell B's own Refractor projects `cap.*`/`cap-read.*` from
  it. The Registry's `shadowCells` gains B. The Gateway **retries the original op** once the shadow projects
  (a transparent cache-fill, §4 — no client-observable contract surface).
- **Refresh on canonical change (fan-out).** When the member's **tenant-global** grants change in the home
  cell (a role grant/revoke), the **home cell's Weaver** (the convergence engine) detects it and **fans out a
  refresh** to **exactly `shadowCells`** (from the Registry): each fan-out is a `MaterializeShadow`/`Delete`
  op to that cell's Processor carrying the new slice + the bumped/unchanged `revocationGen`. Only
  *tenant-global* grant changes trigger fan-out; **local** changes never do. Write amplification is bounded by
  `|shadowCells|` and rare global-grant churn (§6).
- **Retraction has a real transport (the reflex).** A grant **removed** from the canonical (member loses
  membership) is **not** assumed to "drop via reprojection." The home Weaver emits, into **each** shadow
  cell's `actor_read_grants` (and `cap.*`), the **existing seq-guarded soft-tombstone** (Contract #6
  §6.8/§6.14, `is_deleted=true`, `projection_seq` retained) — addressed by `shadowCells`. A member fully
  off-boarded ⇒ a `DeleteShadow` op per `shadowCells` entry + the Registry entry's removal.
- **GC of stale shadows (Edge-Lattice precedent).** A shadow in a cell the member no longer acts in is
  pruned (Edge Lattice §4 "Garbage Collection"): the home Weaver drops it from `shadowCells` after an
  inactivity TTL and issues `DeleteShadow`. Bounds shadow count + refresh fan-out.

### 3.5 The consistency contract (the real content — additions lag, revocations fast, whole-actor instant)

Three classes, each with a defined direction and transport:

1. **Grant ADDITION → eventually consistent, *under*-permit (safe).** A newly-granted tenant-global
   capability becomes usable in cell B only after the §3.4 refresh lands. During the lag the member is
   **denied** the new op locally (existing §6.8 "no entry = no access"). Safe direction: lag never
   over-permits; the member retries shortly. Bounded by the refresh saga latency (sub-second under healthy
   convergence; the Weaver freshness window otherwise).
2. **Grant REVOCATION → globally fast, *fail-closed*.** A revoke **bumps `revocationGen`** in the Registry
   (a global replicated write) *before/independent of* the per-cell tombstone fan-out. At auth, the
   shadow-actor path compares the **shadow's stamped `projectedGen`** to the Registry's current
   `revocationGen` (a local replicated GET): **if `projectedGen < revocationGen` the shadow is
   known-stale-wrt-a-revocation ⇒ fail closed** (deny + trigger an immediate refresh). This is the
   **"global pseudonymous-keyed fast-path"** the multi-cell block named — the revoke is honoured *everywhere*
   the instant the generation replicates, even before each shadow's tombstone arrives. (Additions do **not**
   bump the gen, so they don't trip this — they just lag, per class 1.) This is a **monotonic compare**, not a
   wall-clock check (§6.3 preserved).
3. **Whole-actor cut (suspend / right-to-be-forgotten) → instant, global.** The **existing token-revocation
   KV** (D1, brainstorm #118/#111) is replicated and checked at every read/write boundary: killing the
   member's JWT denies **all** ops everywhere immediately, independent of shadows. Right-to-be-forgotten
   additionally destroys the home-cell Vault key (crypto-shred) — the shadows hold no PII, so there is nothing
   to shred in them; only the canonical home aspect is destroyed. **One multi-cell reconciliation owed
   (Fire 4):** the ratified single-cell activation mechanism
   (`gateway-token-revocation-activation-design.md`, 2026-07-03) materializes each Gateway's bucket from
   **that cell's own** `events.gateway.>` stream, so a home-cell `RevokeActor` does not reach acting cells'
   buckets by itself. At multi-cell time the cut needs a named cross-cell transport — the revocation bucket
   joins the R3-replicated operational family (the `identity-registry`/`cell-index` precedent) **or** the
   revoke event rides the §3.4 `shadowCells` fan-out. Owned by Fire 4, whose pre-build gate already covers
   the revocation boundary.

This separation is the heart of the design: **additions tolerate lag because lag under-permits; revocations
must not, so they ride a generation fast-path that fails closed; account-level cut is the existing instant
kill-switch.**

### 3.6 The auth read/write path (P5/P2 — existing machinery, one added compare)

- **Write (submit an op in cell B).** The Gateway routes to cell B (multi-cell §3.3). Cell B's Processor runs
  the **unchanged** step-3: GET `cap.<actor>` (projected by cell B's Refractor from the shadow's slice +
  local links) and dispatch per Contract #2 §2.8. **The one addition** (only when the actor is a *shadow* —
  i.e. home ≠ this cell, known from the `.shadow` aspect / Registry): the **`revocationGen` compare** (§3.5
  class 2) gates the result fail-closed. A home-cell actor (no shadow) is the unchanged single-cell path.
- **Read (D1 RLS in cell B).** Cell B's Postgres `actor_read_grants` carries the member's rows (projected by
  cell B's Refractor from the shadow's `cap-read.*` slice), so the **existing** set-membership RLS policy
  (Contract #6 §6.14) authorizes the member's reads of cell B rows with **no change** — `SET LOCAL
  lattice.actor_id = <NanoID>`; the NanoID-to-NanoID `authz_anchors` join is identical. Reads of cell B data
  are residency-safe (cell-local lenses, multi-cell §3.4 — no implicit cross-region projection).
- **P5 holds** — apps read the cell-local lens targets; the Registry is operational routing/revocation state,
  never a business query surface. **P2 holds** — every shadow write is an op to the cell's Processor; no
  direct KV write, no cross-cell projection fan-out (each cell's Refractor projects only its own bucket,
  multi-cell §3.4).

## 4. Contract surface

**Zero frozen-contract edits required** (the headline — see the For-Andrew block). Everything is build-to or
new operational state:

- **Contract #1 §1.1 (key shapes): no change.** Shadow reuses `vtx.identity.<id>`; the `.shadow` aspect is a
  standard 4-seg aspect; home/shadow-cell sets + residency + generation are **operational state**
  (`identity-registry`), never keys. NFR-SC2 is the enabling invariant (a shadow needs no key rewrite; a
  member moving home cells is the existing migration dance with zero key rewrite).
- **Contract #2 §2.6 (errors): no new code (recommended).** First-act cache miss = **transparent Gateway
  hydrate-and-retry** (a slow-path fill, like any cache miss — not a structured client reject). Stale-wrt-
  revocation = the **existing** `AuthDenied` (§6.8). *Sub-option for your call:* if you prefer an **explicit**
  advisory over transparent retry, reserve a `ShadowHydrating` §2.6 code (the caller waits + retries) — I
  recommend **transparent** (no contract edit; the Gateway already owns routing/retry, multi-cell §3.3). If
  you pick the explicit signal, that one §2.6 addition would be staged uncommitted at build time; the default
  design carries no edit.
- **Contract #2 step-8 (atomic batch): no change.** A `MaterializeShadow`/`DeleteShadow` op is an ordinary
  single-cell atomic batch in the *target* cell. The cold home-cell read in the hydrate saga is read-only and
  outside the batch.
- **Contract #6 (Capability KV): build-to.** Shadow grants ride existing `cap.<actor>` / `cap-read.*` /
  `actor_read_grants`; existing `projectionSeq` guard + soft-tombstone revoke; existing token-revocation KV.
  The **`revocationGen`** compare is **registry-side operational state + a Processor auth check gated on the
  shadow actor class** — a *monotonic generation* compare, **not** the §6.3-forbidden wall-clock age check.
  No §6 edit.
- **Contract #9 (identity): build-to.** Canonical PII (`.contact`, `.claimKey`) stays in the home cell
  unchanged; the shadow simply never carries it.
- **The `identity-registry`, the `.shadow` marker semantics, the `revocationGen`, the residency-placement
  policy, the hydrate/refresh saga: P1 operational state + orchestration** (the cell-index / Weaver-state /
  Loom-saga precedents) — not contract.
- **Doc touch-ups the Steward applies at build (docs, not contracts):** a "global identity / shadow" section
  in `multi-cell-sharding-design.md`'s consuming docs (`deployment-isolation.md` Phase-3 path,
  `lattice-architecture.md` Stream-8/NFR-SC2 residency note, a `gateway.md` shadow-hydrate routing note,
  `refractor.md`/`processor.md` shadow-actor auth note). `/docs` edits, staged by the Steward — not part of
  this design's commit.

## 5. Axis 2 — cross-region + data residency (going global)

The multi-cell core is **validated** for residency (the stress-test memory): NFR-SC2 makes residency
relocation key-free; cell-local lenses are the residency-safe read posture. This design adds a **residency
dimension to placement** and **reshapes the shadow** — it does **not** fork the core.

- **`cell-registry` gains `region/jurisdiction` → cell = residency zone** (multi-cell `cell-registry` is
  already operational state; this is a metadata column).
- **Residency-aware home placement (fail-closed).** A new identity's home cell is chosen in a cell whose
  jurisdiction matches the identity's **residency tag**. **An absent/ambiguous residency tag ⇒ the
  most-restrictive jurisdiction** (deny-by-default placement — never a permissive default). This is the
  fail-closed-default reflex applied to placement: omission must not open a permissive path.
- **Canonical PII never auto-crosses borders.** The shadow is **PII-stripped by construction** (§3.2 — only
  the pseudonymous NanoID + grants), so replicating it into a different-region acting cell is **residency-safe
  with no PII transfer**. The canonical PII stays in the home jurisdiction.
- **Cross-border PII access is explicit / consented / logged — never via the shadow.** A genuine need to read
  an EU member's *name* from a US cell is a **deliberate, gated, audited** cross-cell read of the home cell
  (a Gateway-level consented operation), **not** something the shadow silently enables. The shadow's PII-free
  shape makes the safe default the only easy path.
- **Residency relocation = the existing migration dance.** Moving a member's home cell to another region is
  multi-cell §3.7 with **zero key rewrite** (NFR-SC2); the shadows + Registry `homeCell` flip on the same
  atomic index-flip commit point.
- **The hard edge — air-gapped sovereign jurisdictions (PIPL / Russia), where even pointers may not cross,
  is a FEDERATION case, not a shadow case.** There the pseudonymous Registry entry itself cannot replicate
  into the air-gapped zone; the member holds a **separate federated identity per sovereign jurisdiction** (no
  global shadow spanning the boundary; cross-boundary interaction is an explicit federation bridge, multi-cell
  model C cross-cluster at its limit). **Out of this design's core** — named so the replication primitive is
  not pretended to cover it (the stress-test memory's explicit warning).

## 6. Risks + alternatives (self-adversarial pass folded in)

### 6.1 Rejected alternatives (earn the recommendation)

- **Dedicated identity tier (auth served from a global tier).** *Rejected — the hot-path read hotspot.* Every
  cross-cell op would read the tier to resolve capability → the exact hotspot + cross-cell latency + home/tier
  availability coupling the shadow exists to avoid. The Registry here is the *cold-path* sliver of a tier
  (home + revocation gen), not an auth-serving tier; grants cache locally as shadows. (This is the multi-cell
  block's "shadows ≈ cache the identity tier into its consuming cells.")
- **Shard-by-identity (co-locate all of a member's activity in their home cell).** *Rejected — defeats
  multi-cell for the hyperscale tenant.* A member acting at hundreds of properties spread across cells cannot
  have all that property activity dragged into their home cell without recreating the single-cell ceiling per
  member and shattering each property's local atomicity.
- **No shadow; cross-cell read on every op (resolve capability from home each time).** *Rejected — the naïve
  baseline.* Home-cell hotspot + cross-cell latency on the **hot** path + every op coupled to home-cell
  availability. The shadow is precisely the cache that removes this.
- **An advisory `ShadowHydrating` §2.6 reject instead of transparent retry.** *Not rejected outright — offered
  as a sub-option (§4).* I recommend transparent Gateway hydrate-and-retry (no contract edit, better UX); the
  explicit code is available if Andrew prefers a visible signal.

### 6.2 Risks + mitigations

- **Revocation latency (the security-critical risk).** *Mitigated by the layered fast-path:* whole-actor cut
  is instant (token-revocation KV); per-grant revoke bumps `revocationGen` (globally replicated) and the
  shadow-actor auth compare **fails closed** on a stale gen — so a revoked grant is denied the instant the
  generation replicates, **ahead of** the per-cell tombstone. The tombstone fan-out is the eventual cleanup,
  not the safety boundary. Residual: the replication latency of a single generation int (small, R3 KV) —
  acceptable, and strictly better than the per-grant tombstone latency it backstops.
- **Stale shadow over-permitting an addition.** *Safe direction — under-permit.* An un-refreshed shadow lacks
  the new grant ⇒ the member is denied + retries; lag never over-permits an addition.
- **PII leak via the shadow.** *Mitigated structurally* — the shadow is PII-free **by construction** (§3.2);
  the lint/activation check on the `MaterializeShadow` slice rejects any PII-class aspect (`.contact`,
  Vault-encrypted) in a shadow. Cross-border PII is an explicit gated op only (§5).
- **Residency violation.** *Fail-closed placement* (§5 — absent tag ⇒ most-restrictive); shadows carry no PII;
  air-gapped = federation (out of core).
- **Shadow fan-out write amplification** (a member at 500 properties = 500 shadows to refresh on a global-grant
  change). *Bounded:* only **tenant-global** grant changes fan out (rare — membership churn, not daily
  activity); **local** changes never do; the Registry's `shadowCells` makes fan-out **targeted**; and **GC**
  (§3.4) prunes shadows in inactive cells, capping `|shadowCells|` to *actively-used* cells. A pathological
  hub still bounds at active-cells × global-grant-churn — orders below per-op cost.
- **The multi-homed-identity exception's blast radius.** *Scoped + read-only:* only `identity`, only its
  pseudonymous global slice, only system-written, never member-mutable in the acting cell — so the "one vertex
  one cell" invariant holds for *all other* types and for identity *mutation* (which routes home). The
  exception is a replica, not a second writable copy.
- **Registry as a new SPOF / hotspot.** *R3 replicated + cold-path:* not read to serve grants; a transient
  Registry read failure on the revocation compare **fails closed** (deny + retry), never over-permits.
- **`MaterializeShadow` hydrate races a concurrent home-cell grant change.** *Idempotent + seq/gen-guarded:*
  the hydrate stamps the home `revocationGen` it read; a concurrent revoke bumps the gen, so the freshly-
  materialized-but-already-stale shadow trips the fail-closed compare on first use and re-refreshes. The
  saga is idempotent per the Loom tracker (multi-cell §3.5 precedent).

### 6.3 Self-adversarial findings folded in

- *"Does the `revocationGen` compare resurrect the §6.3 freshness-ceiling the architecture rejected?"* — No:
  §6.3 forbade a **wall-clock projection-age** check; this is a **monotonic generation** compare (the §6.2
  guard idiom), gated to the **shadow-actor class only**, and it is a *revocation* signal, not an age policy.
  Home-cell actors keep the unchanged single-cell path. Folded into §2/§3.5/§4.
- *"Is the shadow Core KV (multi-homed) defensible against the anchor principle?"* — Yes, and scoped: the
  shadow is **read-only**, carries no outgoing business links the member mutates (local entities link *to*
  it), and identity *mutation* routes home — so the atomic-batch-co-location guarantee holds for every cell-B
  op (they write cell-B-local entities + a link to the local shadow). Flagged as the one deliberate exception
  for Andrew (§For-Andrew, §6.2).
- *"Does 'tenant-global grant' have a crisp definition, or is it hand-wavy?"* — Defined by **scope**: a grant
  whose justification (`resolvedVia`/`via`, Contract #6 §6.5/§6.14) is the **tenant/membership** anchor (not a
  cell-local resource) is tenant-global and rides the shadow; a grant justified by a **cell-local** resource
  (a specific lease/unit) is local and is not replicated. The classifier is the grant's justifying anchor —
  reusing D1's existing `via`/`resolvedVia` provenance, no new field.
- *"Could a parallel in-flight design touch the same seam?"* — Checked the `📐 awaiting-Andrew` /
  `🏗️ designing` rows: the Personal/Secure Lens (✅ ratified) and D1 (🏗️ building) both touch `cap-read.*` /
  `readableAnchors` / `actor_read_grants`, but this design **consumes** that machinery unchanged (no edit to
  it) — it adds the cross-cell *replication* + *revocation-gen* layer above. No collision; build-sequenced
  strictly behind both (and behind multi-cell). Folded into §2 + §7.

## 7. Decomposition for the Steward (fire-by-fire)

**Honest sequencing — the whole feature is build-deferred behind multi-cell + a real hyperscale driver; no
fire is buildable now.** (The one piece that *could* precede — a pseudonymous-anchor primitive — is **already
shipped** as D1's `nanoIdFromKey`, so there is no de-risking "Fire 0" to start.) Ratify the design, shelve the
build like Vault/D1/HA/multi-cell. When the gate clears, the fires are:

- **Fire 1 — the Global Identity Registry + residency on `cell-registry` (behind multi-cell Fire 2).** The
  replicated `identity-registry` KV (`homeCell`/`shadowCells`/`residency`/`revocationGen`); the
  residency-aware, fail-closed home-placement policy at the Gateway; the `region/jurisdiction` column on
  `cell-registry`. Tests: residency-matched placement; absent-tag ⇒ most-restrictive; Registry read/replicate.
- **Fire 2 — the shadow data model + `MaterializeShadow`/`DeleteShadow` ops + the `.shadow` marker (behind
  Fire 1).** The read-only shadow anchor + the tenant-global-grant slice; the PII-exclusion lint (no PII-class
  aspect in a shadow); cell-B Refractor projecting `cap.*`/`cap-read.*` from a shadow (existing machinery,
  fed by local shadow state). Tests: a member with a shadow in cell B is locally authorizable; no PII in any
  shadow; local grants still project from local links.
- **Fire 3 — the hydrate-on-first-act saga + transparent Gateway retry (behind Fire 2).** The Loom
  shadow-hydrate saga (cold home read → `MaterializeShadow` in B → Registry `shadowCells += B`); the Gateway
  cache-miss detect + hydrate-and-retry. Tests: first act in a fresh cell hydrates + the original op succeeds
  on retry; idempotent re-hydrate.
- **Fire 4 — the consistency contract: refresh fan-out + the revocation-generation fast-path (behind Fire 3;
  FULL 3-layer + the pre-build party-mode gate below — this is the security-plane increment).** The home
  Weaver refresh/retraction fan-out addressed by `shadowCells` (soft-tombstone retraction reused); the
  `revocationGen` bump-on-revoke + the shadow-actor auth compare (fail-closed); the whole-actor
  token-revocation-KV path validated across cells **including its cross-cell transport** (§3.5 class 3 —
  replicate the bucket or ride the fan-out; the single-cell materializer alone does not propagate). Tests:
  addition lags then converges (under-permit during
  lag); revoke bumps gen → denied everywhere before the tombstone arrives; whole-actor cut instant; stale-gen
  fail-closed; off-board retracts across all `shadowCells`.
- **Fire 5 — shadow GC + residency relocation + the cross-region acceptance gate (behind Fire 4).** The
  inactivity-TTL shadow GC (Edge precedent); a home-cell residency relocation via the migration dance
  (zero key rewrite); a **multi-region e2e** (member acts across two jurisdictions; PII never crosses;
  revocation honoured cross-region) + the federation hard-edge documented as out-of-scope. The `/docs`
  touch-ups (§4).

## 8. Open questions — resolved

- **Mechanism for tenant-global identity?** → **Server-side cross-cell shadow** (Edge-Lattice precedent
  reused): canonical PII home-cell; a **PII-stripped pseudonymous read-only shadow** carrying only the
  tenant-global grant slice into acting cells; each cell's existing Refractor/Processor/RLS authorize locally.
  §3.2/§3.6.
- **Dedicated tier vs. shadows?** → **Shadows** (cache the identity into consuming cells); a thin **Registry**
  is the cold-path index, **not** a hot-path auth tier. Tier + shard-by-identity rejected. §6.1.
- **The consistency contract?** → **Additions lag (under-permit, safe); revocations ride a global
  `revocationGen` fast-path (fail-closed); whole-actor cut is the existing token-revocation KV (instant).**
  §3.5. Not a §6.3 wall-clock check — a monotonic generation compare. §2.
- **Transport for materialize/refresh/retraction?** → **Loom hydrate saga** (cold home read → op to the
  target cell) + **Weaver fan-out** addressed by the Registry `shadowCells`; retraction = the **existing
  seq-guarded soft-tombstone**; GC by inactivity TTL. §3.4. (Named the transport end-to-end — no "resolved
  from context.")
- **New contract surface?** → **None required.** Keys unchanged (NFR-SC2); errors unchanged (transparent
  retry + existing `AuthDenied`); Capability/identity build-to. The only deliberate change is the **scoped
  multi-homed-`identity` exception** (operational, flagged for Andrew). §4/§For-Andrew.
- **Axis 2 — residency?** → **Residency dimension on placement (fail-closed on absent tag) + a PII-stripped
  shadow** makes going global key-free + PII-safe; **air-gapped sovereign = federation, out of core.** §5.
- **Build now or shelve?** → **Ratify now; shelve the build** behind multi-cell Fire 2 + a real hyperscale
  driver. No de-risking fire is buildable now (`nanoIdFromKey` already shipped). §For-Andrew / §7.

---

### Recommended pre-build gate

For **Fire 4** (the consistency contract — the revocation-generation fast-path + the cross-cell retraction
fan-out, the one security-plane, new-coordination increment), run a **`bmad-party-mode` adversarial pass on
the revocation boundary** (the `revocationGen` bump-vs-compare race, the shadow-actor auth gate, the
fail-closed-on-stale-gen window, the off-board retraction across `shadowCells`, the hydrate-races-revoke case,
the cross-cell token-revocation transport — §3.5 class 3)
**before building**, and record it as run — mirroring the pre-build passes multi-cell/D1/HA self-flagged. The
rest of the surface (the Registry, the shadow data model, the hydrate saga) is covered by the integration +
multi-cell-fixture tests. This gate is a **Designer-lane obligation discharged at build time**, not a dangling
flag.
