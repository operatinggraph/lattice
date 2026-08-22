# LoftSpace lease renewal — the first goal-authored Weaver target — design

**Status:** ✅ **Andrew-ratified 2026-07-05 (ratify session) — ratified as staged, lane consolidated to
Lattice.** The §10.8 strike-and-replace (per-leg execution, `actions` catalog, doctrine rider incl. the
terminal-leg rule) + §10.3 mark riders are ratified; adversarial pass ran pre-ratification (3 read-only
lenses, §11). **Lane consolidation (Andrew, anti-ping-pong — the kv.Links precedent):** the verticals
row is REMOVED; R1 dispatch wiring → R2 package → R3 FE all ship from the **Lattice lane** (the FE
Engineer + Sally pair on R3). The Lattice Steward had already shipped Inc3's parse+validate half
(`b99d51c`) against this design's §5 surface before ratification.
**Stream:** Lattice (Stream 2) — lane-consolidated at ratification (Verticals-filed demand; the
LoftSpace package + FE fires ship from the Lattice lane with the engine work, one lane end-to-end).
Inc3's synthesis-dispatch machinery lands **with** this first real consumer, per the mandate's
2026-07-05 unblock.
**Designer fire:** Winston, 2026-07-05 (Andrew-routed demand, `verticals.md`).
**Builds on:** `weaver-planner-mandate-design.md` (ratified 2026-07-04; Fires 1–5 + 6 Inc1–2 + 7 shipped),
Contract #10 §10.2/§10.3/§10.5/§10.8, the shipped `lease-signing` chain, `loftspace-domain` ownership +
listing economics.
**Contract change:** **RATIFIED 2026-07-05 — §10.8 + §10.3** (the strike-and-replace of three ratified
2026-07-04 sentences — plan execution, mark pinning, synthesis catalog — plus the goal-first doctrine
rider and the per-gap `actions` grammar). Commits with this ratify session's disposition of the two
sibling proposals sharing the file (`surface`, `goalColumns`).

---

## For Andrew (ratification in one look) — DECIDED 2026-07-05: ratified as recommended; lane consolidated to Lattice

**What:** lease renewal for LoftSpace — a lease nearing expiry opens a per-tenant renewal chain (fresh
bgcheck only if stale, guarantor re-verify only if one exists, landlord rent adjustment, tenant
signature), authored as **one `goal` gap** the planner sequences, not five gating-cypher'd `missing_*`
columns. The demand that un-holds planner Fire 6 Inc3, and the doctrine-setting reference for goal-first
authoring.

**Fork 1 — synthesized-plan EXECUTION shape (revises ratified §10.8 text; my recommendation: per-leg
dispatch).** The ratified clause "the plan compiles to a linear Loom pattern → `triggerLoom`" cannot
execute a **multi-actor human chain**: the frozen §10.5 userTask invariant (`assignedTo == scopedTo ==
the instance subject`, `internal/loom/engine.go:930`; the step shape carries no assignee field) means one
compiled pattern can never assign one leg to the landlord and another to the tenant while both write the
renewal vertex — and it would run a second program counter beside Weaver's level machinery.
**Recommendation:** the plan dispatches **per leg** through the frozen action vocabulary — each episode
fires `plan.Steps[0]` via `buildPlan` (`triggerLoom`/`assignTask`/`directOp`, existing machinery,
dispatch-time re-validation preserved per leg), and the graph's own advance is the program counter. The
compile-to-pattern clause becomes **RESERVED for op-only single-actor plans** — a real class with **no
consumer today**, so it stays unbuilt (dead-scaffolding test). Trade-off: per-leg advance costs one
sweep-cadence hop between legs — irrelevant at human/external leg latency (hours/days); the reserved
shape is exactly for machine-speed op chains where those hops would matter.

**Fork 2 — episode pinning becomes per-leg (revises the ratified lifetime-pin sentence).** A goal
chain's gap stays open across all legs; a literal episode-lifetime pin would re-dispatch leg 1 forever.
**Recommendation:** the pin releases when the pinned leg's declared `effects` all hold in the current
row (a pure row predicate at the existing single-mark-read seams — verified implementable with zero new
KV reads, §11); reclaim re-plans **only past a completed leg**. Pin-release is also the leg's `__effect`
close-credit and resets the gap's dispatch count (per-leg budget semantics) — without these two
couplings, healthy chains would read as permanent lens/effect mismatches and waiting human legs would
burn the chain budget on reclaim cadence (§11 findings A2/E2). For single-step `candidates` this all
degenerates to exactly the ratified behavior (one leg = one gap-close).

**Also for your eye (data-shape calls):** the renewal vertex id is a **deterministic
`crypto.sha256NanoID("renewal:"+leaseappId+":"+cycleEnd)`** — grammar-valid, the `identityindex`
precedent (a first draft used a readable composite id; the review killed it against Contract #1's
readable-id prohibition + Weaver's NanoID-only `splitRowKey`). The renewal root carries **two** scalars
`{cycleEnd, status}` — `status` is the sanctioned lifecycle scalar; `cycleEnd` is a second root scalar
(cycle identity for lens equality-match), in mild tension with §10.3's "at most **a** justified
lifecycle scalar" phrasing — flagged rather than hidden.

**Net effect on Inc3:** the Loom/plan-vertex side **shrinks** (no plan-vertex minting, no runtime
pattern authorship, no plan GC, no §10.5 pinning interplay); the pkgmgr side **grows** (the
`mode`/`goal`/`goalColumns`/`actions` package-authoring surface, which no fire had scoped — today no
package can author a planner target at all).

---

## 1. Problem + intent

No renewal surface exists: a lease reaches its term end and nothing notices — no re-qualification, no new
terms, no signature, no term extension. The product gap is real (LoftSpace's lifecycle dead-ends at
move-in) and the platform gap is structural: every shipped target closes every gap in **one** dispatch,
because §10.8's dependency-gating doctrine makes lens authors pre-decompose chains into single-op gaps at
authoring time — so goal regression's value-add (chaining ≥2 legs, per-entity variability) never fires
(the mandate's Inc3 hold, "structural, not temporal"). Renewal is the honest counter-example: the chain
**varies per tenant** (stale vs fresh bgcheck; guarantor vs none), so a static table or gating cypher
would enumerate 2×2 variants of one procedure. One goal + four declared actions lets the planner derive
each tenant's chain. Intent: ship the product surface AND establish goal-first authoring as the
sanctioned pattern (§10.8 rider), with Inc3 landing against a real consumer.

## 2. Grounding — what exists (verified in code this fire; re-verified by the §11 pass)

- **Term facts:** `.listing` on the unit carries `availableFrom` (RFC3339) + `leaseTermMonths` + `rentAmount`/`rentCurrency` (`packages/loftspace-domain/ddls.go`). No tenancy-end fact exists anywhere; nothing models renewal.
- **Temporal lane:** `freshUntil` is an engine-recognized §10.2 column (`internal/weaver/temporal.go:39`); any **future** instant arms a per-row @at (shipped future-instant precedent: clinic-reminders' `remindAt = startsAt−24h`), re-armed per reprojection; a past instant fires immediately — so a lens must null it once past (§4.2).
- **Bgcheck freshness:** the replyOp stamps `validUntil`; the signing lens projects only-if-fresh and treats stale as missing, with the `leaseshortwindow` build tag for bounded e2e (`freshness_window.go`).
- **Guarantor facts:** `SetApplicantProfile` stores `hasGuarantor` (default false) + RAW guarantor fields on `.profile`; only derived booleans project.
- **Ownership:** `AssignUnitOwner` writes the landlord→unit management link — and the DDL explicitly allows **many** landlords per unit and does not require one (§4.2/§4.3 handle zero/many deliberately).
- **Planner library (Fire 3):** `Action{Ref, Cost, Precondition *Guard, Effects []*Guard}`; `Synthesize` = forward uniform-cost search, canonical tie-breaks (cost, then lexicographic ref-join); `EvalGuard` supports the full grammar **including `anyOf`/`not`** on goals and preconditions; `ApplyEffects` accepts concrete assertions only; an already-true atom needs no action — **vacuous satisfaction is native**.
- **Fire 4/5/6-Inc2 engine:** `registry.go` parses `mode`/`candidates`/`goal`/`goalColumns` with fail-wholesale validation; `rowState` maps columns to root paths, bridging aspect-real columns via per-gap `goalColumnPaths`; `resolvePlannedAction` is the single choke point, mark read once and threaded (dispatch `evaluator.go:212-229`, reclaim `reconciler.go:474`). **A goal-only planned gap today** falls through to `errConfig "unknown action"` → a standing `PlaybookConfigError` alert, no dispatch (loud, not silent) — R1's goal branch slots at exactly that seam (`strategist.go:284`).
- **The authoring gap:** pkgmgr's `WeaverTargetSpec`/`GapActionSpec` expose **none** of the planner fields — no package can author a planner target today. This design adds that surface.
- **§10.5 userTask invariant:** `assignee == scopedTo == inst.SubjectKey` hard-coded (`engine.go:929-932`); the step shape has no assignee/target field (`pattern.go:27-45`). Weaver's `assignTask` carries separate row-templated `Assignee`/`Target` generically (`strategist.go:160-172` — `row.landlord` works like `row.applicant`).
- **Deterministic ids:** vertex ids are NanoID-only (Contract #1: "Deterministic readable IDs are NOT permitted"; `splitRowKey`/`splitMarkKey` drop non-NanoID entity segments). The sanctioned deterministic form is **`crypto.sha256NanoID(...)`** (`starlark_builtins.go` — mints a valid NanoID; `identityindex` vertices ship on it). Clinic's slot claims are deterministic **aspect localNames**, not vertex ids.
- **Multi-vertex writes:** CreateAppointment's one batch writes claim aspects on provider AND patient (`clinic-domain/ddls.go:1560-1573`); write scope is permittedCommands-per-DDL, reads via `ContextHint.Reads`.
- **Row retraction (the transport, named):** an actorAggregate convergence lens **never retracts on a filter change** — a non-binding anchor emits zero rows (never a delete), and the plain-lens filter-retraction pass explicitly excludes actorAggregate. Rows disappear only on anchor tombstone (or via `RealnessFilter` + `EmptyBehavior: delete`, the my-tasks precedent). The shipped signing lens keeps one row per leaseapp forever with false columns; gap-close is Weaver's level reconcile. §4.2/§4.4 build to this.
- **actorAggregate reach:** adjacency fan-out is an undirected BFS (depth ≤ 10) — the renewal walks (depth 2–3) are safely inside; a new link can't race its own edge.
- **Overlap check:** no other 📐/🏗️ design touches `strategist.go`/`resolvePlannedAction`/§10.8 goal semantics.

## 3. The two conflicts the first consumer exposes (and their resolution)

**3.1 Multi-actor human legs vs the compiled pattern.** The renewal chain's legs: externalTask (bgcheck
refresh — bridge I/O), userTask→landlord (verify guarantor), userTask→landlord (set terms),
userTask→tenant (sign). Under §10.5 a pattern's userTask is always assigned to (and scoped to) the one
pattern subject — no single compiled pattern expresses landlord AND tenant legs against a renewal-vertex
subject. Amending §10.5's step shape would be a larger frozen-surface change than Fork 1, and would still
leave Loom's cursor duplicating Weaver's level machinery.

**3.2 Episode pinning vs chain advance.** §10.3 marks pin the chosen action; the episode today ends when
the gap closes. A goal gap stays true until the whole chain completes — lifetime-pinning leg 1 wedges the
chain.

**Resolution (Fork 1 + Fork 2):** per episode the engine synthesizes from the current row, dispatches
**`plan.Steps[0]`** via the action's dispatch binding (`buildPlan` — existing; dispatch-time
re-validation per the `proposedOp` precedent preserved per leg), and pins that actionRef exactly as
Fire 5 does. When the leg's declared effects hold in the row — evaluated through the gap's
`goalColumnPaths` bridge at the existing single-mark-read seams (dispatch `evaluator.go:218`; sweep
`reconciler.go:207-262`; `clearClosedMarks` `evaluator.go:452-489` — row and mark co-resident at all
three, zero new KV reads) — the mark closes (one revision-conditioned mark write; `marks.replace`
machinery exists), **the leg's `__effect` close is credited, and the gap's dispatch count resets**
(per-leg budget). The next episode re-synthesizes from the advanced state → the next leg. `ErrNoPlan`
routes to the `unplannable` escalation. Determinism is untouched — the plan stays a pure function of
(row, catalog, `__effect` window); the mark records the plan hash for diagnostics.

## 4. The shape

### 4.1 Data model (Contract #1 / D5)

- **`vtx.renewal.<id>` — one vertex per renewal cycle.** Id = **`crypto.sha256NanoID("renewal:" +
  leaseappId + ":" + cycleEnd)`** — deterministic AND grammar-valid (the `identityindex` precedent), so
  `OpenRenewal` is CreateOnly-idempotent per (leaseapp, cycle): a duplicate fire collides on the vertex
  create and converges (plus the episode requestId dedup). Root `data`: `{cycleEnd, status:
  open|complete|cancelled}` — `status` is the sanctioned lifecycle scalar; `cycleEnd` (the leaseEnd this
  cycle renews, copied at open) is a second root scalar for the lens's cycle equality-match, flagged for
  Andrew (For-Andrew block).
- **Link:** `lnk.renewal.<id>.renews.leaseapp.<idA>` — "renewal renews leaseapp" (sentence ✓;
  later-arriving renewal = source ✓). No other links: tenant, unit, landlord are reached through the
  leaseapp's existing links in the lens walk.
- **Aspects on the renewal** (D5): `.terms {rentAmount, termMonths, setAt}` (SetRenewalTerms),
  `.guarantorVerification {verifiedAt, method}` (VerifyGuarantor), `.signature {signedAt}` (SignRenewal).
- **`.tenancy` aspect on the leaseapp** `{leaseStart, leaseEnd, renewalOpensAt}` — the tenancy-term fact
  that today exists nowhere. Stamped by **`DecideLeaseApplication(decision=approved)`** —
  **create-only** (skipped when already present, so the op's documented idempotent re-approve can never
  re-derive it and silently truncate renewal extensions — §11 B3): `leaseStart = unit
  .listing.availableFrom`, `leaseEnd = leaseStart + leaseTermMonths`, `renewalOpensAt = leaseEnd −
  renewalWindow` (computed in the op's Starlark — bespoke-contracts date-math precedent; the cypher never
  does date arithmetic). The caller adds the unit key to `ContextHint.Reads`. `renewalWindow` is a
  package constant with a short-window build tag mirroring `bgcheckFreshnessWindow`. **Extended
  exclusively by `SignRenewal`** (§4.4). Existing already-approved leases carry no `.tenancy` and never
  enter the expiry lens — renewal applies to leases approved after this ships (no backfill op).

### 4.2 Target A — `leaseExpiry` (frozen table; opens the cycle)

Lens (full engine, actorAggregate, `weaver-targets` bucket): anchored on leaseapps having `.tenancy` +
approved + signed **+ an alive unit with ≥1 manages-landlord** (a leaseapp on an ownerless unit never
opens a cycle — there is no counterparty to set terms; `AssignUnitOwner` is wired into post-listing, so
owned is the norm). Columns: `entityKey`; `freshUntil = CASE WHEN renewalOpensAt > $now THEN
renewalOpensAt ELSE null END` (a past @at re-published verbatim would fire on every delivery — the
shipped null-when-stale posture, §11 C9); `missing_renewalCycle = ($now ≥ renewalOpensAt) AND
count(non-tombstoned renewals WHERE cycleEnd = tenancy.leaseEnd) = 0` — **cancelled cycles count**: a
landlord's recorded decline must not be reopened by the sweep (§4.4); `violating`. Gap (frozen table —
single-step, deterministic; goal-authoring it would be ceremony):
`missing_renewalCycle → directOp OpenRenewal {leaseApp: row.entityKey}, Reads [row.entityKey]` —
Weaver's service actor, the `SetListingStatus` cross-package directOp precedent. `OpenRenewal`'s DDL
reads the leaseapp + `.tenancy`, derives the sha256NanoID, creates the vertex + `renews` link.

### 4.3 Target B — `renewalComplete` (the goal-authored target; `mode: "planned"`)

Lens (full engine, actorAggregate): anchored on **all** renewal vertices (tombstone-filtered, anchor
**unfiltered** by status — an actorAggregate lens has no filter-retraction transport, §2; completed/
cancelled rows linger benignly with false columns, the shipped signing-lens posture). Walks
renewal→leaseapp→{applicant identity → `.profile`, bgcheck service instances → `.outcome`},
leaseapp→unit→manages-landlord. Columns:

| column | source | notes |
|---|---|---|
| `entityKey`, `tenant`, `landlord` | keys off the walk | `landlord` = deterministic pick: **min(landlord key)** across manages links (many-landlords is legal; an engine-arbitrary pick would break one-row-per-anchor determinism — §11 B5). v1 semantic: the canonical manager |
| `open` | root `status == 'open'` | gates the gap + `freshUntil` |
| `leaseappAlive` | walk | a withdrawn/tombstoned leaseapp must not leave an immortal violating renewal |
| `hasGuarantor` | applicant `.profile.hasGuarantor` | bool |
| `bgcheckValidUntil` | freshest bgcheck outcome, `CASE WHEN validUntil > $now THEN validUntil ELSE null END` | stale projects null (walk-computed row fact); also `freshUntil = CASE WHEN open THEN bgcheckValidUntil ELSE null END` so a mid-chain lapse re-arms only open cycles |
| `guarantorVerifiedAt` / `termsSetAt` / `signedAt` | renewal aspects | aspect-real |
| `maxretries_renewalComplete` | literal 6 | **per-leg** retry bound (the count resets at pin-release, §3); without the projected column the budget is inert |
| `missing_renewalComplete` | `open AND leaseappAlive AND NOT goal-met` | status-gating means a completed cycle can never re-violate when its bgcheck later lapses |
| `violating` | = the gap | |

**The gap** declares:

```
goal: allOf(
  present  subject.data.bgcheckValidUntil,
  anyOf( equals(subject.data.hasGuarantor, false),
         present subject.guarantorVerification.data.verifiedAt ),
  present  subject.terms.data.setAt,
  present  subject.signature.data.signedAt )
goalColumns: { guarantorVerifiedAt: subject.guarantorVerification.data.verifiedAt,
               termsSetAt:          subject.terms.data.setAt,
               signedAt:            subject.signature.data.signedAt }
```

`bgcheckValidUntil` stays root-mapped — a **walk-computed** row fact (the outcome lives on a service
instance, not the renewal), so its goal atom and its action's effect meet at the root path; the three
aspect-real columns bridge via `goalColumns` (Inc2's machinery, both classes exercised — the rider's
core authoring rule, §5).

**The actions catalog** (per-target, package-authored — the new surface):

| ref | dispatch | pre | effects | cost |
|---|---|---|---|---|
| `refreshBgcheck` | `triggerLoom backgroundCheck, subject: row.tenant` (the shipped pattern, verbatim) | — | `present subject.data.bgcheckValidUntil` | 2 |
| `verifyGuarantor` | `assignTask VerifyGuarantor, assignee: row.landlord, target: row.entityKey` | `equals(subject.data.hasGuarantor, true)` | `present subject.guarantorVerification.data.verifiedAt` | 3 |
| `setTerms` | `assignTask SetRenewalTerms, assignee: row.landlord, target: row.entityKey` | — | `present subject.terms.data.setAt` | 1 |
| `signRenewal` | `assignTask SignRenewal, assignee: row.tenant, target: row.entityKey` | `allOf( present subject.terms.data.setAt, present subject.data.bgcheckValidUntil, anyOf( equals(subject.data.hasGuarantor,false), present subject.guarantorVerification.data.verifiedAt ) )` | `present subject.signature.data.signedAt` | 1 |

**`signRenewal`'s `pre` is the goal's full remainder — the terminal-leg rule** (§11 finding B1, the
review's headline): because SignRenewal's commit flips the completion scalar, an under-specified `pre`
plus the canonical tie-break (`"signRenewal" < "verifyGuarantor"`) would order signing *before*
verification and close the gap with the guarantor atom permanently unmet. The rider generalizes this
(§5): **an action whose op closes the gap's anchor MUST declare a `pre` entailing the rest of the goal,
mirrored in the op's write guard.** Note `pre` here addresses aspect-bridged paths — legal because a
goal gap's State carries the `goalColumns` bridge; the install rule is **row-reachability** (root column
∪ this gap's `goalColumns` paths) for both `pre` and `effects` (an unreachable effect path would make a
leg permanently un-releasable — §11 E11). Per-tenant behavior falls out of the search: a fresh bgcheck
means no refresh leg; `hasGuarantor=false` satisfies the `anyOf` (and `verifyGuarantor`'s `pre` keeps it
un-appliable). `maxDepth = len(actions) + 2` (R1 constant). v1 catalog = the target's declared actions
**only** — an op's DDL effects carry no dispatch binding (§7 F); the ops' own DDL `Effects` are still
declared as the integrity source the entries mirror, **with Target A/B effect-path sets kept disjoint**
(the Fire-7 oscillation ring is keyed by bare path globally: SignRenewal's DDL declares only
`signature.signedAt`; OpenRenewal declares no root-status effect — §11 A9/E12).

### 4.4 Write path + permissions (P2)

- **`OpenRenewal`** — Weaver's service actor (directOp precedent). Creates vertex + `renews` link
  (CreateOnly, deterministic id).
- **`SetRenewalTerms`** — landlord task-grant (+ operator). Writes `.terms`; validates `rentAmount > 0`,
  **`termMonths ≥ ceil(renewalWindow in months)`** (a term shorter than the window would open cycle N+1
  the instant N signs — monthly rollover is explicitly out of scope, §11 D10); **rejects when
  `.signature` present or `status != open` (`TermsLocked`)** — terms can never drift under a recorded
  signature (§11 B4a). Revision before signature: the §10.6 task auto-complete consumes the ephemeral
  grant on the first successful submit, so in-chain revision has **no task path**; revision rides the
  operator/trusted-tool model (the shipped SignLease posture), and the FE copy says so (§4.5). A tenant
  "request changes" op that clears `.terms` (level-triggered leg reopen) is a clean v2, not built now.
- **`VerifyGuarantor`** — landlord task-grant (+ operator). Writes `.guarantorVerification`; rejects
  when the applicant profile has `hasGuarantor=false` (`NoGuarantorToVerify`); the applicant identity
  key in the payload is **verified against the leaseapp's applicant link** before its `.profile` read.
- **`SignRenewal`** — tenant task-grant. Write-path mirror of its planner `pre` (write-path honesty —
  the op must not rely on the planner for write-safety): rejects unless `.terms` present
  (`NotReadyToSign`) and, when the verified profile says `hasGuarantor=true`, unless
  `.guarantorVerification` present (`GuarantorNotVerified`). Bgcheck freshness is **dispatch-gated
  only** (the outcome instance key has no exact-key form op-side; a lapse inside the open sign-task
  window is an accepted, documented residual). Writes `.signature`, sets `status=complete`, and
  **extends the leaseapp `.tenancy`** (`leaseEnd += terms.termMonths`, `renewalOpensAt` recomputed) —
  the leaseapp key in the payload is **verified against the live `renews` link**
  (`lnk.renewal.<rid>.renews.leaseapp.<claimed>` must exist + alive — the Withdraw link-verification
  precedent; a tampered payload cannot extend an arbitrary leaseapp, §11 B6). Multi-vertex write per the
  CreateAppointment precedent; keys ride `ContextHint.Reads`.
- **`CancelRenewal`** — landlord task-less grant (+ operator): the terminal path (§11 B7 — without it a
  declining landlord / unwilling tenant / withdrawn leaseapp leaves an immortal open renewal). Sets
  `status=cancelled` + `reason`; rejects when signed. Cancelled gates the gap off (row stays, inert) and
  **counts as the cycle's renewal** in Target A (a recorded decline is final for that term — the sweep
  must not re-open it; re-running a cancelled cycle is unsupported in v1, §8.7). Open tasks left behind
  expire on their own `expiresAt` (the §10.1 lifecycle).
- **Close cascade (level-triggered, transports named):** `status=complete` → Target B reprojects →
  `open=false` → gap false → `clearClosedMarks` clears (rows linger benignly — no retraction needed);
  `.tenancy` extension → Target A reprojects → `missing_renewalCycle` stays false, `freshUntil` re-arms
  for the next cycle.

### 4.5 FE (pkg + FE owner; Sally pairs at build)

- **`renewalsRead`** protected Postgres lens (Contract #6 §6.14): one row per renewal cycle — status,
  cycle dates, terms, verification/signature state; **one lens, two anchors**:
  `authz_anchors = [tenant NanoID, landlord NanoID]` (§6.14's `authz_anchors` is an array with
  any-match RLS semantics — no new machinery; the D1.5 lenses are single-anchor siblings, cited as
  posture not as a two-anchor precedent).
- **Tenant** (My Applications): Renewal card — window/state timeline; "Sign renewal" CTA when the
  SignRenewal task is assigned (existing my-tasks surface).
- **Landlord** (unit applications view): Renewal card — chain progress, "Set terms" / "Verify
  guarantor" task CTAs, a "Decline renewal" (CancelRenewal) action; terms display is **set-once via the
  task** (revision goes through the operator console — §4.4, no false FE promise).

## 5. Contract surface (staged UNCOMMITTED for Andrew — a revision, not an addition)

The staged diff **strikes and replaces three ratified 2026-07-04 sentences** in §10.8's Planner
extension and rides a small §10.3 rider (affected consumers: Weaver engine, pkgmgr install validation,
package authors, Augur Fire 9):

1. **Execution clause** (was: compile to `plan-<hash>` Loom pattern → `triggerLoom`; §10.5 pinning; GC)
   → **per-leg dispatch** through the frozen action vocabulary; pin-until-leg-complete; pin-release =
   the leg's `__effect` close-credit + dispatch-count reset; dispatch-time re-validation per leg
   unchanged; the compiled-pattern shape **RESERVED for op-only single-actor plans** (unbuilt, no
   consumer; the multi-actor blocker is §10.5's userTask invariant, deliberately untouched).
2. **Mark-pinning clause** (was: pinned for the episode's lifetime; replanning only at close→reopen) →
   pinned **per leg**; leg boundaries (effects-hold) and gap boundaries both mint fresh marks; for
   `candidates` this degenerates to the ratified behavior verbatim.
3. **Catalog clause** (was: "the installed catalog — ops with `effects` + Loom patterns as
   macro-actions") → the gap's **declared `actions` catalog** (closed, package-authored; a global
   ops-derived auto-catalog is reserved — an op effect alone has no dispatch binding).
4. **New `actions` grammar + prose:** `[{ref, <one frozen action's fields>, pre?, effects, cost?}]`,
   required alongside `goal`; install-validated — refs unique; the dispatch binding passes the same
   validation as a static gap action; `effects` concrete assertions only; **`pre` and `effects` paths
   must be row-reachable (root column ∪ the gap's `goalColumns` paths)**.
5. **Doctrine rider:** goal-first authoring sanctioned as the alternative to gating-cypher
   decomposition for genuine chains (≥2 legs or per-entity variability); single-step gaps stay
   frozen-table; the row-fact naming rules (aspect-real → `goalColumns`; walk-computed → root); the
   **terminal-leg rule** (a completion-flipping action's `pre` must entail the goal remainder, mirrored
   in its op's write guard); write paths carry their own guards regardless of the planner.
6. **§10.3 rider:** mark clears on gap-close, **planned-leg completion**, or lease expiry; leg
   boundaries also mint fresh marks (`claimId` rotation unchanged in shape).

## 6. Reconciliation with the existing mental model

- *Didn't the ratified planner design already settle execution?* It settled **selection**; its
  execution sketch was drawn against op-effect chains. The Inc3 hold said "lands with its first real
  consumer" precisely so a real consumer could pressure-test it — it did, and three ratified sentences
  need revision (listed exhaustively in §5; nothing else in §10.8/§10.3 moves).
- *Is per-leg dispatch just the frozen table with extra steps?* No: the leg is chosen by search over
  declared effects against the row's current state; ordering emerges from `pre` + level advance;
  per-tenant legs drop out via `anyOf`/already-true atoms; one budget/escalation envelope covers the
  chain.
- *Does dropping plan-vertices lose determinism or audit?* Neither — the plan stays a pure function
  (Fire-3 property tests); the mark carries the plan hash; the audit trail is the graph itself.
- *New state?* None beyond the ratified `__effect`. Renewal vertices/aspects are ordinary business
  state; `.tenancy` is the missing tenancy-term fact.
- *Overlap with bespoke-contracts self-amendment?* Different concern (clause economics vs tenancy
  lifecycle); a later increment could bridge them; no coupling now.
- *Augur Fire 9:* plan-shaped proposals become **action-sequences** under the same per-leg execution —
  the AI boundary also never authors runtime Loom patterns.

## 7. Alternatives considered

- **A. Compile-to-one-pattern (ratified shape, as-is).** Dead-ends on §10.5's userTask invariant for
  multi-actor chains; fixing *that* means amending the frozen step shape — a larger contract change —
  and accepting two program counters. A variant **does** beat per-leg for op-only machine-speed chains:
  kept, reserved, unbuilt (no consumer).
- **B. Subject-per-leg patterns.** Contorts the domain (a terms-phase "subject" is the landlord?) and
  still can't scope tasks to the renewal.
- **C. No renewal vertex — chain aspects on the leaseapp, overwritten per cycle.** Cycle 2's goal would
  see cycle 1's `.signature` → vacuously complete. Rejected (stale-fact soundness + auditable cycles).
- **D. Goal-author the open-cycle gap too.** Single-step → frozen table (anti-ceremony).
- **E. Guarantor re-verify as a bridge externalTask.** No verification adapter exists; the human task is
  the honest v1 — and swapping the action entry later changes nothing else (the doctrine's payoff).
- **F. Global auto-catalog from installed op effects.** No dispatch binding on an op effect (assignee?
  params?) → undispatchable steps. Per-target authored catalog v1; auto-catalog can layer later.
- **G. Readable deterministic renewal id** (first draft). Violates Contract #1's readable-id
  prohibition + 20-char/alphabet grammar, and Weaver's `splitRowKey` would drop every row. Replaced by
  `sha256NanoID` (§4.1).

## 8. Resolved questions

1. **Cypher date math?** Avoided — ops compute (Starlark, bespoke-contracts precedent); lenses compare.
2. **Existing leases?** No `.tenancy` → never enter Target A. No backfill in v1.
3. **Windows/constants:** `renewalWindow` 60d (+ short-window build tag); bgcheck reuses
   `leaseshortwindow`; `maxretries_renewalComplete = 6` **per leg** (count resets at pin-release);
   `maxDepth = len(actions)+2`; budget exhaustion raises a standing Health issue at the suppression
   sites (R1 — today both return silently; the `exhausted`→Augur wiring stays Fire 9 as ratified).
4. **`hasGuarantor` null (no profile)?** The verify disjunct is unreachable (`equals` on absent =
   false) AND `verifyGuarantor`'s `pre` is false → **`ErrNoPlan` → `unplannable` standing alert** (Target
   B declares no augur block, so it is a Health alert, not an AI escalation). Fail-closed, correctly
   attributed — a broken row plans nothing rather than half-planning.
5. **Mid-chain bgcheck lapse?** Pre-dispatch: `freshUntil` re-arms, the next episode re-includes
   `refreshBgcheck` (and `signRenewal`'s `pre` blocks signing until fresh). Inside an open sign-task
   window: accepted residual (§4.4) — the op cannot exact-key-read the outcome instance.
6. **Landlord changes mid-cycle?** actorAggregate reprojects; the min-key pick may move; open tasks
   survive on their own `expiresAt`. Acceptable v1 semantics, noted in FE copy.
7. **Re-opening a cancelled cycle?** Unsupported in v1 (a tombstoned cancelled renewal would collide
   with the deterministic id on re-open, and reviving it would inherit the dead cycle's aspects — a
   revive-with-reset verb is a v2 decision, not silently improvised).
8. **Oscillation visibility:** R1 bridges the oscillation bump to the **action entry's declared
   effects** (refs aren't operationTypes — today they'd silently miss the ring). Row-fact effect names
   are per-target, so cross-target false alternation requires deliberately shared aspect paths — kept
   disjoint by §4.3's DDL rule.

## 9. Decomposition (one shipping window, **one lane — Lattice** (consolidated at ratification), one-way sequence)

| Fire | Lane | Scope | Proves |
|---|---|---|---|
| **R1 — Inc3′ engine** | Lattice (`internal/weaver`, `internal/pkgmgr`) | pkgmgr `mode`/`goal`/`goalColumns`/`actions` authoring fields + emission + install validation (incl. **row-reachability of `pre`/`effects`**); registry `actions` parse; `resolvePlannedAction` goal branch → `Synthesize` (`maxDepth` const) → `Steps[0]` dispatch via `buildPlan` **with per-leg dispatch-time re-validation**; pin-release on effects-hold (via `goalColumnPaths`) + leg `__effect` close-credit + `__count` reset; budget-suppression Health issue; oscillation ref→declared-effects bridge; `ErrNoPlan` → `unplannable` | Renewal-shaped fixture (4 actions, `anyOf` goal, terminal-leg `pre`): per-tenant plans differ; same state → same plan (hash-stable); reclaim re-fires the pinned leg (no re-rank); effects-hold advances to the next leg; count resets per leg; **zero Loom diffs**; mode-absent targets byte-identical (suite invariant) |
| **R2 — package** | Lattice (lane-consolidated; code in `packages/lease-signing`, `loftspace-domain`) | `.tenancy` create-only stamping in DecideLeaseApplication; renewal vertex/link/aspect DDLs + OpenRenewal/SetRenewalTerms/VerifyGuarantor/SignRenewal/CancelRenewal (+ DDL `Effects`, path-disjoint across targets); both lenses + targets (A frozen, B `mode:planned`); permissions; `verify-package` | E2E (ephemeral stack, short windows): two tenants — (guarantor, stale bgcheck) vs (none, fresh) — converge through **different** chains to signed + extended `.tenancy`; a declined renewal parks terminally; Target A re-arms for the next cycle |
| **R3 — FE** | Lattice + FE Engineer/Sally (lane-consolidated) | `renewalsRead` dual-anchor lens; tenant + landlord renewal cards + task CTAs + decline | Live two-browser walkthrough on the running stack |

R1 ships first (engine before consumer — fixture-proven, with R2 in the same window; shipping R2 first
would raise loud `PlaybookConfigError` alerts, not a silent wedge, but the order stands). The build
lives on the **lattice board only** (the verticals row was removed at ratification — lane
consolidation); the contract edits committed at ratification per the ratified-contract-commit rule.

> **🏗️ CHECKPOINT (2026-07-05, R2 shipped).** Package built + 3-layer reviewed. Two as-built deviations
> from this doc's §4 text: **(1)** the renewal completion aspect is `.renewalSignature`, not `.signature`
> as written above — `SignLease`'s pre-existing DDL already declares the effect path
> `subject.signature.data.signedAt`, and Weaver's Fire-7 oscillation ring keys purely on the bare
> `{Aspect,Field}` path with no vertex-type/operationType component, so the as-written name would have
> let ordinary concurrent lease-signing + renewal-signing traffic false-trigger a freeze of both
> unrelated targets (caught by the adversarial pass, not present in §11's original review — that pass
> only checked disjointness within the new Target A/B pair, not against the pre-existing
> `leaseApplicationComplete` target). **(2)** `SignRenewal` additionally rejects `RenewalNotOpen` when
> `status != open` (a §4.4 gap: nothing else stopped a second submission against an already-complete
> renewal from re-reading the by-then-extended `leaseEnd` and double-extending it); `SetRenewalTerms`
> rejects non-integer `termMonths` (`InvalidTermMonths`) rather than silently truncating. `applicant`
> confirmed load-bearing (a real link-verification hop distinct from `leaseApp`'s) and now declared in
> `InputSchema` alongside `leaseApp` for both `VerifyGuarantor`/`SignRenewal`. **R3 note:** the
> `assignTask` dispatch path (`internal/weaver/strategist.go`) never populates a task's bound op payload
> beyond `assignee`/`scopedTo`/`forOperation` — `leaseApp`/`applicant`/`renewalKey` are NOT derivable by
> a human from the task alone. R3's FE must supply these as hidden fields sourced from the
> `renewalComplete`/`renewalsRead` row (which already carries `entityKey`/`tenant`/`landlord`), not ask
> the operator to type NanoIDs. Next: R3 (FE).

## 10. Test strategy / migration

Unit: planner fixtures (anyOf vacuous satisfaction, terminal-leg ordering under tie-break — the B1
regression test, per-tenant divergence, determinism property over the new catalog shape); registry/
pkgmgr validation tables (bad `actions` rejected wholesale, unreachable effect paths rejected); op DDL
tests (TermsLocked, NotReadyToSign, GuarantorNotVerified, NoGuarantorToVerify, link-verified cross-vertex
writes, create-only `.tenancy`, extension math, deterministic-id collision, CancelRenewal terminality).
E2E: the R2 two-tenant divergence proof + decline path + the standing "mode-absent byte-identical" suite
invariant. Migration: none — all additive + opt-in; existing leases out of scope until approved with
`.tenancy`. Rollback: remove `mode` from Target B → the gap parks at the loud `PlaybookConfigError`
posture (no static table to fall back to) — correct visibility for "planner turned off under an open
chain"; in-flight tasks drain on `expiresAt`.

## 11. Adversarial pass (run 2026-07-05, this fire — 3 parallel read-only lenses)

Mechanism verifier (10 claims): 8 CONFIRMED with file:line evidence — both fork premises hold
(§10.5 invariant; pin-release implementable at the existing seams, zero new reads); 2 REFUTED and
folded — **(A6)** the readable deterministic id (→ `sha256NanoID`, §4.1/§7 G) and **(A8)** the
"open-only anchor drops the row" retraction (→ unfiltered anchor + status-gated gap, §4.3; as-written it
would have left every completed renewal violating forever). Edge-case hunter: 1 critical — **(B1)** the
tie-break amputating the guarantor leg (→ the terminal-leg rule, §4.3/§5); majors **(B2)** reclaim-cadence
budget burn (→ per-leg count reset + Health issue on suppression), **(B3)** re-approve `.tenancy` wipe
(→ create-only), **(B4)** terms-after-signature + the impossible revision promise (→ `TermsLocked` +
operator-model revision), **(B5)** landlord zero/many (→ min-key pick + anchor requires an owner),
**(B6)** unverified cross-vertex leaseapp key (→ link-verified), **(B7)** no terminal path
(→ `CancelRenewal` + `leaseappAlive` gate); minors folded (§4.2 freshUntil null-when-past, §8.3 budget
constants, §8.4 corrected escalation mechanism, §8.7, §8.8, termMonths floor). Contract auditor:
key-shape critical = A6 (same root cause); the §10.8 amendment reframed as an explicit
strike-and-replace (§5); §10.3 rider added; `__effect` leg-crediting added; §10.6 auto-complete
implication folded (B4); dual-anchor citation corrected (§4.5); "Inc3 shrinks" qualified (For-Andrew);
R1 scope completed (re-validation, zero-Loom-diffs). Verdicts on the unchanged remainder: task-grant
model, P2/P5 posture, temporal lane, reprojection reach, oscillation safety (with §4.3's disjointness
rule) — all pass.

## Risks

- **Per-leg sweep-cadence latency** — one sweep hop between legs; negligible at human/external leg
  latency (the reserved compiled shape is for the machine-speed class).
- **Budget semantics are new** (per-leg reset) — R1 fixture covers reset + suppression visibility; the
  signing target's no-budget human-gap posture remains available as fallback config if the constant
  proves noisy.
- **Lens walk depth** (renewal→leaseapp→identity→instances, depth 3) — inside the BFS cap with shipped
  depth-2 precedent; flagged for the R2 reviewer to profile on the e2e stack.

## Build note — R3 residual: name the tenant on the renewal card (verticals lane, steward fire 2026-08-22)

**Scope sentence:** `renewalsReadSpec` (`packages/lease-signing/renewal_lenses.go:300`) projects only
`tenant.key`, so `renewalsRead`'s `tenant` column is a bare key and `renderRenewalCard`
(`cmd/loftspace-app/web/app.js:2393`) never shows who the landlord is setting terms for or declining.
Mirror `landlordLeaseApplicationsRead`'s SECURE `applicant_name` column
(`packages/lease-signing/lenses.go:967-1044`) verbatim onto `renewalsReadSpec`.

**Verified touch-list (scout `haiku`, re-verified live by this fire):**
- `packages/lease-signing/renewal_lenses.go` — `renewalsReadSpec` WITH/RETURN (add
  `tenant.name.data AS tenantNameEnv` → `tenantNameEnv AS tenant_name`); the `renewalsRead` lens's
  `Columns` (add `tenant_name text`) and a new `SecureColumns` field (none existed on this lens before).
- `packages/lease-signing/package.go:85` — version bump (additive column, `0.31.3` → `0.31.4`).
- `cmd/loftspace-app/renewals.go` — `renewalRow` struct + `selectRenewalsSQL` + the `Scan` call gain
  `tenant_name` / `TenantName *string`.
- `cmd/loftspace-app/web/app.js:2393` (`renderRenewalCard`) — landlord-only title line names the tenant,
  mirroring the established `a.applicantName || shortKey(a.applicant)` idiom
  (`app.js:3389`/`3698`/`3795`); the tenant's own view of their own renewal is unchanged (no self-name).
- `packages/lease-signing/renewals_read_lens_test.go` — extend `seedOpenRenewal` to optionally seed a
  `.name` aspect (mirroring `TestLandlordLeaseApplicationsRead_ProjectsContactEnvelopesWhole`'s
  `nameEnv` ciphertext-envelope fixture), add a `tenant_name` assertion to
  `TestRenewalsRead_ProjectsDualAnchor` (or a sibling) plus a missing-aspect-projects-null case.

**Precedent to mirror:** `landlordLeaseApplicationsReadSpec` (`packages/lease-signing/lenses.go:978-1044`)
— `id.name.data AS applicantNameEnv` in WITH, `applicantNameEnv AS applicant_name` in RETURN, declared as
a `SecureColumn{Column: "applicant_name", HolderTypes: []string{"identity"}, Field: "value"}`. The
Secure-Lens decryptor rewrites the ciphertext envelope to plaintext before the row reaches the adapter
(Contract #3 §3.10) — the RETURN must stay the whole envelope, never a `.value` hop.

**Non-goals:** landlord naming (already shipped, `landlord_key` stays a display body column, not
SECURE — landlord identity isn't sensitive contact PII the same way an applicant's is, and the FE never
needed it); re-deriving `qualified`-style readiness columns; touching `renewalComplete` (the
Weaver-internal sibling, no display concern).

**Adjacent finds:** none — the `landlord` display field predates this row and already resolves via
`landlordMin`/`landlordKey`; no second raw-key surface found in the renewal card.

**Adversarial pass (3 parallel `opus` reviewers — security/mechanism, edge-case, contract/convention),
this fire.** Verdict: safe to ship, PII exposure is a strict subset of what `landlordLeaseApplicationsRead`
/ `applicantRosterRead` already disclose to the same anchor set (no new category, no new authorization
path — a `SecureColumn` is a value transform, never a grant). Two real gaps found and fixed before merge,
not filed:
- `internal/refractor/grouping_reduction_corpus_census_test.go`'s `renewalsRead` pin didn't include the
  new `tenantNameEnv` WITH item — the pin is a deliberate "force a re-reading of the grouping change"
  gate (its own header), and the re-reading (does the new map-valued key mis-partition rows?) checks
  out: `normalizeForKey` is injective, and `tenantNameEnv` is functionally determined by the already-keyed
  `tenantKey`, so no group splits. Pin updated to match; a new
  `TestRenewalsRead_CoManagedUnitWithTenantNamePresent` pins that co-management + a present name envelope
  still collapses to one row.
- `make refresh-loftspace` never called `provision-readpath` (unlike `refresh-cafe`/`refresh-wellness`/
  `refresh-clinic`, which all do) — so this being the FIRST `SecureColumns` declaration on `renewalsRead`
  would have left the dev-loop refresh 502ing the renewals tab (missing `tenant_name` column; hot-reload
  refuses a `secureColumns` change and Refractor pauses fail-closed rather than issuing DDL itself).
  Fixed in the Makefile to match the other three verticals' refresh targets.
