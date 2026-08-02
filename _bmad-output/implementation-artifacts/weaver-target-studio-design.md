# Weaver Target Studio — observe, verify, author convergence targets in Loupe — design

**Status: ✅ Andrew-ratified (2026-08-02)** — all three §14 forks accepted per recommendation:
**Fork 1** authoring width = the capability-materializer's restricted subset (planner surfaces render
read-only; widening is its own ratification); **Fork 2** trial arming = explicit `enable` only, never
auto-arm; **Fork 3** `SubmitCapabilityProposal` ratified with the program (lattice-lane row filed,
AI-native section — it gates F25.3b only). The paired §8-adjacent doc/contract edits (Contract #10's
liveness invariant + weaver.md's obligations-from-occurrences guidance) were ratified in the same
decision and committed with this status. Drafted 2026-08-02 (Winston, Designer fire; Andrew's
chat-scoped brief — his scoping is the brief, no Surveyor row precedes this). Adversarial pass run and
folded the same fire (§15).
**Component:** Loupe (`cmd/loupe` Go handlers + `web/` UI) — read-side joins over the convergence plane;
the author stage rides the shipped capability-artifact lane. **Zero engine changes, zero contract
changes; ONE cross-lane ask (an operator-submission op in `packages/capability-author`) gates only
F25.3b's propose step — F25.1/2/3a need nothing outside the lane.**
**Lane:** Loupe (`backlog/loupe.md` → F25). **Ratification: Andrew** — the lane's default is
Winston-adjudicated (delegated 2026-07-02), but Andrew requested ratification for this program directly
(chat, 2026-08-02); that request governs.

## 1. Problem + intent

Targets + playbooks are the platform's declarative programming model: a package declares what must hold
(a violation Lens) and what closes each gap (a playbook), and Weaver converges reality onto it. That
plane now carries real machinery — planner selection/synthesis, per-gap budgets, contraction/oscillation
diagnostics, admission pacing — but the console still shows it only as **tables and exception lists**.
Three needs are unmet:

1. **Observe** — "why is entity E not converging on target T?" has no single answer surface. The
   operator today joins, by hand: the `weaver-targets` row (which gaps are true), `weaver-state` marks
   (what's in-flight, retry counts, budget exhaustion), the control-plane `list` (disabled?), Health
   issues (oscillation-frozen? effect-mismatch? rejected?), and the pattern/task the dispatch produced.
   Nothing renders a target's *structure* — gaps, their playbook actions, the patterns/ops/tasks those
   dispatch — with the live state on it.
2. **Verify** — a target author's feedback loop is install → watch → fail: `TargetRejected` surfaces
   only after CDC-load (`registry.go:605`), a gaps key that drifts from its Lens's `missing_<gap>`
   column surfaces only as runtime `LensEffectMismatch`, and two targets whose remediations fight
   surface only when the runtime oscillation detector freezes them both (`weaver.md` Fire 7).
3. **Author** — authoring a target means hand-writing pkgmgr specs in package Go source, or the
   AI-authored capability lane. There is no operator-facing way to compose a target visually, check it,
   and submit it through the existing review gate.

Vision anchors (vault): the Weaver Mission's Audit → Nudge → Resolve loop and "living requirements"
framing (`Obsidian Vault/Lattice/Weaver/Weaver Mission.md`); Semantic Contracts' "visualizing liability —
you can query the graph to see exactly which clauses are currently driving cash flow"
(`…/Contract as Executable paper/Semantic Contracts.md`) — the studio is the operator face of that.
Brainstorming lineage: #44–46 (convergence loop, target-state diffing, nudge emitter), #65 (compliance
mode). Lane context: the Loupe lane is drained of ready work (PO note 2026-07-25) — this is the refill
program.

## 2. What exists today (grounding)

| Surface | What it gives | What it doesn't |
|---|---|---|
| `lattice.ctrl.weaver.list` (Loupe allow-listed read, `cmd/loupe/control.go:51`) | per-target `targetId`, `lensRef`, sorted gap columns, `active`/`disabled` | no rows, marks, or structure |
| F18 planner diagnostics panel (shipped 2026-07-19) | exception-first: oscillation · mismatch · contraction · admission · shadow, from Health | aggregate exceptions, not target-shaped; no entity drill |
| Component view tables + F17 task inbox | raw targets/gaps/marks tables; stuck/unrouted task flags | joins left to the reader |
| F23 flow detail (`#/flows/<id>`) | a Loom instance's step sequence off the pinned pattern + cursor | per-instance, downstream of a dispatch; no target context |
| Lens Contents panel (`cmd/loupe/lens.go:391,583` — prefix-scoped shared-bucket reads; `weaver-targets` carries 13+ lenses' rows) | raw row inspection | no gap/mark/playbook semantics |
| `graph.js` / `map.js` | shipped SVG node/edge renderers (entity ego-graph; system map) | neither draws a target's definition with live state |
| Install-time validation: engine `validateTarget` + planner suite (`registry.go:611-657,752,811,878,911,954`), mirrored in pkgmgr (`plannerfields.go`) and in `ValidateCapabilityArtifact` (F16.2's server-side re-validation) | fail-closed authoring checks | verdicts surface as install failures / Health issues, not while authoring |
| Op-effects corpus: op-DDL `Effects` → `.effects` aspects → registry index; `effectPathsFor` joins a dispatch to its concretely-asserted paths (oscillation detector); `effectsCatalog()` is **built, tested, and explicitly reserved for "a future global-catalog consumer"** (`registry.go:1215-1247`) | declared write-knowledge per op | no design-time consumer exists |
| Capability-artifact lane (ratified 2026-06-29; live kinds incl. `weaverTarget` + `lens` — `ENABLED_KINDS`, `packages/capability-author/ddls.go:355`; `capabilitymaterializer.go` materializes target artifacts as a deliberately restricted subset) + Loupe F16 review console (queue, approve = server-side re-validation via `ReviewCapabilityProposal`, apply = two-commit F-004 install; the augur tab submits its own lane's `ReviewProposal`) | a shipped validate → review → apply loop for dynamically-authored capability content | proposal *creation* is bridge-locked to the AI path — no human entry today (§6) |
| FR30 control plane: `disable`/`enable` (registered targets only, `weaver.md:726`); **`revoke` on an unknown `targetId` is not an error and "still writes the disabled marker so a future registration of that `targetId` starts disabled"** (`weaver.md:754-756`); disabled = remediation-inert while bookkeeping continues (`weaver.md:704-716`) | a race-free born-disabled seam, already shipped | nothing consumes it for authoring trials |

**The gap:** the pieces all exist — renderers, validators, declared effects, a review/apply lane, a
born-disabled seam — and no surface composes them into the target-shaped console the convergence plane
deserves.

## 3. The shape — one shared model, three fires

A shared `cmd/loupe` **target-model module** (Go) parses one target into a renderable, checkable
structure, joining: the `meta.weaverTarget` body (Core-KV read — inspector charter), its `lensRef` +
`meta.loomPattern` metas for `triggerLoom` actions, live `weaver-targets` rows (prefix-scoped, the
`lens.go` precedent), `weaver-state` marks (`<targetId>.>` — a **new but precedented** direct
operational-KV read; Loupe already reads Core KV (`corekv.go`) and Health KV (`health.go`) under the
inspector exception, and §10.3's key/value shapes are frozen), the control `list` state, and the
target's Health issues. All three fires render or check this one model; it is built once, in F25.1.

Everything below is **read-path or ops-path only**: no engine change, no new NATS surface, no
contract change, no repo access from Loupe (hard rule), no live-editing of installed targets.

## 4. F25.1 — Observe: the target map (M)

Routes: `#/weaver` (all targets: state chip, contraction trend **class** — `shrinking`/`steady`/
`diverging`, straight off the heartbeat the shipped F18 panel already reads — and frozen/disabled
badges; exact row/gap counts render only per-target, because an aggregate count cannot be paged and an
all-targets scan of the shared bucket per render is exactly the cost §11 refuses) →
`#/weaver/<targetId>` (the target map; one prefix-scoped scan for this target's counts) →
`#/weaver/<targetId>/<entityId>` (entity drill). The list also surfaces **orphan `__control`
markers** (marker present, target not registered) as first-class rows — nothing engine-side sweeps
them, and a stale one silently disables a future target installed under that id (see the trial, §6,
and §11).

**The target map** renders the definition as a diagram (SVG, reusing `map.js`/`graph.js` idioms):

- **Gap nodes** — one per `gaps` key, badged with live aggregate state: open count (rows where the
  column is `true`), in-flight count (marks), budget-exhausted count (`__count` ≥ `maxretries_<g>`),
  and the gap's dispatch mode (explicit action / candidates / goal — planner fields render read-only).
- **Action edges** — gap → what it dispatches: `triggerLoom` → the pattern node (expanding to its
  linear step strip, the F23 renderer's data shape), `assignTask` → the op + assignee binding,
  `directOp` → the op, `surface` → the Health-issue severity. Templated params shown against the
  §10.2 row columns they bind.
- **Lens binding panel** — `lensRef`, the observed row columns (from live rows), and the
  gaps-key ↔ `missing_<gap>` column join, with any unbound side flagged (see F25.2 V1).
- **Target-level chips** — `active`/`disabled`/oscillation-frozen (rendering the `TargetOscillation`
  issue text as-is — F18 deliberately declined to parse the pair back out of free text as brittle,
  and this design follows that call rather than re-proposing it), admission policy, mode, augur
  policy.

**Entity drill** joins one row's columns against its marks: per-gap chips
(open / in-flight with lease + `claimId` + retry count / budget-exhausted / closed), and links each
**in-flight** dispatch to its artifact — the §10.3 `claimId`-seeded derivation
(`deriveStableInstanceID`/`deriveStableTaskID`, seeded on target + entity + gap + `claimId`,
`actuator.go:157,169`) computes the Loom `instanceId`/task id from the **live mark**, so the chip
links straight into `#/flows/<id>` or the task inbox. Two deliberate bounds: a *closed* gap's mark is
deleted (that is what closed means — `evaluator.go:54`), so completed dispatches are reached through
the existing flows/tasks views by subject, never re-derived; and an external gap's reclaim re-mints
`claimId` (`reconciler.go:613-621`), so the link is always computed from the current mark, never
cached. Health issues naming this entity surface inline.

F18's exception-first panel stays; its rows gain "open in the target map" links. The component-view
tables stay as the raw inspector. Sally UX pass in-fire per the lane pipeline (Winston adjudicates
UX in-fire; the *program* is what Andrew ratifies here).

## 5. F25.2 — Verify: the target checker (M)

A "Checks" panel on the target map + a lane-wide `#/weaver/verify` summary. Read-only computation at
view time; no stored verdicts, no new platform state. Three tiers, each labeled with its evidence
class so a green never overclaims:

- **V1 — structural (exact, static).** The §10.2↔§10.8 seam checked both ways: every `gaps` key has
  its `missing_<gap>` column observed in live rows (absent ⇒ "column never observed" — the static
  cousin of `LensEffectMismatch`; explicitly marked *observed-evidence*, since a lens with zero
  candidate entities legitimately has no rows yet); every observed `missing_*` column has a playbook
  entry (or is flagged unhandled). `triggerLoom` pattern refs resolve to installed
  `meta.loomPattern`s and the pattern's `subjectType` matches the action's `subject` binding;
  templated params reference columns the rows actually carry; `assignee`/`target` bindings resolve.
- **V2 — install-verdict surfacing (exact, existing).** `TargetRejected` Health issues rendered in
  place, and the parsed planner surface rendered structurally: candidates ranked with their `pre`s,
  a `goal` gap's `goalColumns` bridge + `actions` catalog with each entry's `pre`/`effects` — making
  visible what `validateTarget`/`parseGoalColumns`/`validateActionsCatalog` enforce at install. No
  re-implementation: verdicts come from the engine's issues; structure from the installed body.
- **V3 — interference (advisory, declared-effects-based).** The static twin of the runtime
  oscillation detector's join: for every pair of installed targets, compute each dispatched action's
  concretely-asserted aspect paths from the op-meta `.effects` aspects (same semantics as
  `effectLeafPaths` — present/absent/equals leaves, walking `allOf`; `anyOf`/`not` assert no definite
  path). `cmd/loupe` imports `guardgrammar` and re-derives from the same Core-KV aspects the engine
  indexes off its CDC stream: one full `vtx.meta.` enumeration of the core-kv bucket **per verify
  render** — bounded by the meta-vertex corpus (schema, not entity data) and cached across all pairs
  in the render — including the aspect-envelope unwrap that precedes `guardgrammar.Parse`
  (`registry.go:1190-1208`) — and
  flag any aspect path two different targets' actions both assert — the exact signal Fire 7 freezes on
  at runtime (`oscillation.go`), computed over declarations before any dispatch. Ops with no declared
  `Effects` are **listed as unanalyzable** (today that is most of the corpus — only
  `packages/lease-signing` declares `Effects`), which is both honest incompleteness and the adoption
  nudge for declaring them. The panel states plainly that the runtime detector remains authoritative
  (prevention here is best-effort; detect-and-recover is the doctrine).

Placement: all checker logic lives in `cmd/loupe` (in-lane), importing `guardgrammar` and reusing
`ValidateCapabilityArtifact` for draft-shaped input (F25.3); nothing is added to `internal/weaver`.
This makes the studio the first design-time consumer of the declared-effects corpus whose global
catalog the registry explicitly reserved for a future consumer (`registry.go:1221-1229`) — consumed
by mirroring the aspect parse Loupe-side, not by widening the engine's API.

## 6. F25.3 — Author: draft, check, propose, trial (L)

**Artifact-first, not a new install path.** The canvas edits build a **capability artifact** — the
same kinds/schema the ratified AI-authored-capabilities lane validates and F16 reviews/applies. The
studio is a *second proposal source* (a human operator, visually) into the *same* pipeline:

1. **Draft** — compose gaps, actions, bindings on the canvas; edit the paired violation-Lens cypher
   with the §10.2 conventions scaffolded (the `violating` flag, `missing_<gap>` columns, param
   columns). Drafts are browser-local until proposed — no new platform state.
2. **Check** — F25.2's V1/V3 run against the draft; `ValidateCapabilityArtifact` (the F16.2
   server-side validator, already constructible in `cmd/loupe`) is the verdict of record.
3. **Export (F25.3a)** — the checked artifact downloads as its validated JSON bundle, usable today
   through the ordinary package/repo path even before the propose step exists. Draft-check-export is
   a complete, independently shippable loop with no dependency outside the lane.
4. **Propose (F25.3b — gated on the program's one cross-lane ask)** — the capability lane has **no
   human entry today**: `ReviewCapabilityProposal` (F16) and the augur tab's `ReviewProposal` are
   *verdict* ops, and the only artifact-bearing creation op, `RecordCapabilityProposal`, is
   creation-locked to the AI bridge — it demands a live authoring-claim (`externalRef` → a
   `vtx.capabilityauthorclaim` minted only by `CreateAuthoringClaim`, which itself fires the external
   AI adapter) plus model-shaped provenance and a mandatory confidence
   (`packages/capability-author/ddls.go:384-519,844`). The studio therefore needs
   **`SubmitCapabilityProposal`** — a small creation op on the same `capabilityproposal` DDL taking
   `{proposalId, kind, content, target, rationale}` from a capability-gated operator, stamping an
   explicit **`source: "operator"`** field (queue badging then reads a declared field, never an
   inference from model-shaped provenance), entering the proposal `pending` with no claim
   indirection. That is a `packages/capability-author` addition — a **lattice-lane row filed on
   ratification**, and the only cross-lane ask in this program. Review/approve/apply stay exactly the
   shipped F16 flow — the studio itself never installs anything, so the review gate cannot be
   bypassed from the canvas.
5. **Trial (dev stack — F25.3b, rides propose→apply)** — born-disabled, using the shipped FR30 seam,
   single-replica by precondition (the disabled set is per-process, seeded at engine Start — a second
   Weaver replica started before the revoke would dispatch; the standard dev stack runs one engine),
   in order: (a) `revoke
   <draftTargetId>` — sanctioned on an unknown target, writes the `__control` disabled marker so
   "a future registration of that targetId starts disabled" (`weaver.md:754-756`); (b) apply the
   approved artifact — the target registers remediation-inert while rows still project and marks
   still clear (`weaver.md:704-716`); (c) seed fixture entities via ordinary ops (`op.go` submit
   precedent); (d) watch the map light up — which gaps open, per entity, live; a **would-dispatch
   panel** explains, per open gap, what the strategist would resolve (explicit action > candidates >
   goal, rendered from the installed body — an explanation, not an execution); (e) `enable` is the
   operator's explicit arm switch when live dispatch on the dev stack is actually wanted.
6. **Change lane for installed targets** — an edit to an installed target is a new artifact revision
   through the same propose→review→apply flow (F-004 upgrade). Never a live `ops.meta` edit from the
   canvas: package/artifact source stays the single writer of target bodies.

**Scope guard (v1):** the authoring surface is exactly the capability-materializer's deliberately
restricted subset (explicit-action gaps; no `goal`/`candidates`/`augur` authoring — same posture as
its existing augur exclusion, `weaver.md:568-576`). The planner surfaces render read-only in
Observe/Verify but cannot be authored from the canvas; widening that is a separate ratification.
**Exposure guard:** the Author stage is capability-gated (consoleOperator write posture, F15) and
fully suppressed under the demo/read-only postures (F20 mechanisms — method default-deny +
affordance suppression). Observe/Verify are read-only and safe everywhere Loupe runs.

## 7. Read/write paths (invariants)

| Access | Path | Invariant posture |
|---|---|---|
| Target/pattern/op metas, `.effects` aspects | Core-KV read from `cmd/loupe` | Loupe inspector exception (P5's sole app exception) |
| `weaver-targets` rows | prefix-scoped bucket read | read-model; P5-legal for any app; `lens.go` precedent |
| `weaver-state` marks, `__control` | direct operational-KV read (new) | inspector precedent (`corekv.go`, `health.go`); §10.3 shapes frozen; read-only — Loupe never writes this bucket |
| Control state / trial disable+enable | FR30 verbs via the existing capability-gated control relay | shipped surface, operator-authorized |
| Propose / apply / fixture seeding | ops via the Gateway (`SubmitCapabilityProposal` once landed; the shipped F16 verdict/apply ops; domain ops) | P2 — everything mutates through the Processor |
| Repo | — | none, ever (hard rule) |

## 8. Contract surface

Builds **to** §10.2 (row shape), §10.3 (marks, `__control`, `claimId` derivations), §10.8 (target +
playbook + planner fields), FR30 (control verbs), and the capability-artifact schema. **No contract
changes required or proposed by this design.**

## 9. Reconciliation with the existing mental model

- *Didn't we already build Weaver views?* Yes — F18 (exceptions), component tables (raw), F23
  (per-instance flows). None is target-shaped and none joins definition ↔ rows ↔ marks ↔ artifacts;
  §2 maps each and F25.1 links into all three rather than replacing them.
- *Doesn't validation already exist?* Entirely — the studio **surfaces** engine/pkgmgr/artifact
  validators and never re-implements them; the one new analysis (V3 interference) consumes the
  declared-effects corpus whose design-time consumer the registry explicitly reserved space for.
- *Is this the Andrew-gated agent console?* No — that row (ops layer atop the system map: Steward
  queue, per-agent health, `#sysmap-console` mount) stays parked and untouched; the studio shares no
  scope with it.
- *Parallel designs?* Checked: `augur-dispatch-pickup-design.md` (ratified) *adds* a target
  (`augurDispatch`) the studio will simply render; `client-ceremony-op-descriptors-design.md`
  (📐) touches client op descriptors, not this surface. No collision.
- *New state?* None server-side. Browser-local drafts until proposed; proposals/artifacts live where
  F16 already keeps them.

## 10. Alternatives considered

- **Extend F18 with more tables** — answers "what is exceptional now", not "what is this target and
  why isn't E converging"; no authoring story. Rejected; F18 stays and cross-links.
- **CLI-first checker (`lattice-pkg` / `lattice weaver`)** — real value for CI, but no live overlay,
  no entity drill, no review-lane integration; and V1/V3's inputs (live rows, marks, installed
  metas) are exactly what Loupe already holds. Rejected as the primary; nothing here precludes a
  later CLI mirror of the checker.
- **Live-edit installed targets via `ops.meta` from the canvas** — hot-reload makes it *instant* and
  the package/artifact model makes it *wrong*: two writers of one target body, clobbered on the next
  package version bump, and it would bypass the review gate. Rejected; the change lane is an
  artifact revision (§6.6).
- **New engine read-verbs for marks/effects** — grows a control surface whose only consumer is the
  inspector, which may already read the buckets. Rejected (revisit only if a non-inspector consumer
  appears).
- **Trial via `mode:"shadow"` instead of born-disabled** — wrong mechanism: shadow suppresses only
  the *planner's* choices; "the table path dispatches exactly as frozen"
  (contract §10.8, `10-orchestration-weaver.md:300-302`). The `revoke`-first born-disabled seam is
  the correct inert trial and is already shipped. Rejected.
- **A standalone design webapp** — Loupe is the console: auth, session, read plumbing, review UI,
  and the operator all live there. Rejected.

## 11. Risks & mitigations

- **§10.3 shape coupling** — the model module parses frozen shapes only, in one place; a shape
  change is a contract event, not silent drift.
- **Scan cost** — per-target row/mark reads are prefix-scoped + paged (`lens.go` precedent);
  aggregate counts (unpageable) render only on the single-target map, never the all-targets list
  (§4); the verify render's `vtx.meta.` enumeration is one cached scan bounded by the meta corpus
  (§5).
- **V3 false comfort** — mitigated structurally: unanalyzable ops are *listed*, verdicts carry their
  evidence class, and the panel names the runtime detector as authoritative.
- **Born-disabled semantic drift** — the trial choreography is pinned by an embedded-NATS e2e test
  (revoke-unknown → register → assert zero dispatch + rows/marks bookkeeping live → enable →
  dispatch); if the FR30 semantics ever change, the fire's test fails, not the operator's trial. The
  trial is **single-replica by stated precondition** — the disabled set is per-process, seeded at
  Start; a second Weaver replica started before the revoke would dispatch (the standard dev stack
  runs one engine).
- **Review-queue provenance** — human- and AI-authored proposals share the queue by design (one
  gate), distinguished by the new op's explicit `source` field — a declared field, never an
  inference from model-shaped provenance.
- **Uninstall clears `__control`** (`weaver.md:758-762`) — trial cleanup order documented in-view:
  uninstalling the draft removes the marker with the target; re-trialing re-runs (a) first.
- **Orphan `__control` markers** — a typo'd trial `revoke` writes a marker for an id that never
  registers; nothing engine-side sweeps it (the reconciler skips `__control` keys, and reconcile
  clears markers only for registered targets), and it silently disables a future target installed
  under that id. Observe lists orphans first-class (§4) — surfacing is what this program ships;
  clearing one today means installing that id then `enable` (`enable` errors on an unregistered
  target), and a sweep is deliberately **not** proposed (an engine change this program doesn't need).

## 12. Test strategy

Go handler tests (model joins, checker verdicts — including a fighting-pair fixture mirroring
`oscillation_internal_test.go`'s two-targets-one-path shape, asserted statically); goja
`web_logic_*` tests for view logic (lane convention); the born-disabled e2e above; live verification
on the dev stack per lane discipline (headless-first).

## 13. Decomposition

| Fire | Scope | Size | Ships alone? |
|---|---|---|---|
| **F25.1 Observe** | target-model module + `#/weaver` list/map/entity-drill + F18/F23/tasks cross-links | M | yes — pure read view |
| **F25.2 Verify** | checks panel + `#/weaver/verify` (V1/V2/V3) | M | yes — reads F25.1's model |
| **F25.3a Author: draft + check + export** | canvas + artifact build + `ValidateCapabilityArtifact` + validated-bundle export + posture gating | M | yes — no new platform surface |
| **F25.3b Author: propose + trial** | `SubmitCapabilityProposal` submission + `source` badge in the review queue + born-disabled trial | M | 🚧 needs the capability-author operator-submission op (lattice-lane row, filed on ratification) |

Order fixed (each consumes its predecessor's module); one FE fire at a time per lane rules; Sally UX
pass opens each fire.

### Build note — the §6.4 cross-lane ask SHIPPED 2026-08-02 (`6d2614fb`, lattice lane)

`SubmitCapabilityProposal` is live in `packages/capability-author` (v0.8.0), so **F25.3b is unblocked**.
Four things F25.3b should build ON rather than rediscover:

1. **The wire shape is top-level fields, not a result blob:** `{proposalId, kind, content, target{mode,…},
   rationale, validation{state,report?,deltaPreview?}, intent?}`. `target.mode` is required non-empty and a
   malformed payload is rejected synchronously (unlike `RecordCapabilityProposal`, which can never fail
   post-Ack). `validation` is the studio's own Check-stage verdict — the op records it, never recomputes it.
2. **The badge reads `source`, a real lens column now** (`capabilityProposals` → `provenance.source`,
   `'ai' | 'operator'`). `cmd/loupe/review.go`'s `capabilityProposalCols` still has no `Source` field, so
   adding it is F25.3b's first move; a proposal recorded before v0.8.0 projects `source` null.
3. **Do not render a confidence for an operator proposal.** There is no model score, so `.confidence.score`
   carries the `-1.0` absent-sentinel. `logic/review.js` now exports `hasConfidenceScore`, which the band and
   all three render sites gate on — reuse it rather than testing `typeof === "number"`.
4. **The trial step's precondition is intact but sharper than it reads.** A submitted proposal deliberately
   carries no `.claim`, and the `capabilityAuthorPending` gap was narrowed to `no claim AND no artifact` so
   Weaver does not fire the AI authoring pattern at it. Any future change to that lens must keep the artifact
   arm, or every operator submission triggers an unrequested reasoning call whose reply can never commit.

The grant-kind scope containment is **not** established by the submitted verdict (the submitter is also the
party it constrains) — it is the approve-time re-validation in `freshCapabilityVerdict`, which re-reads the
requester's live held permissions. For a submitted proposal that requester is `op.actor`, the real submitter.

### 🏗️ Build checkpoint — F25.2 SHIPPED 2026-08-02 (`e9408470`), next is F25.3a

The Checks panel (target map) + `#/weaver/verify` (lane-wide) landed in `cmd/loupe/weaververify.go`.
V1/V2 read fields F25.1's `buildTargetDetail` already computes (`Observed`, `Unhandled`,
`PatternKnown`, `Bindings[].Observed`) plus two additions to that module: `weaverRowScan.Samples`
(first observed string value per column, for a best-effort subjectType cross-check) and
`weaverMetaIndex.PatternSubject` (a pattern's declared `subjectType`, read off the same spec GET
`buildWeaverMetaIndex` already does). V3 mirrors `internal/weaver/registry.go`'s
`indexOpMeta`/`indexOpEffects`/`effectLeafPaths` join Loupe-side (`buildOpEffectsIndex`) rather than
importing the engine, per §5. Live-verified against the real 20-target dev stack: `/api/weaver/verify`
found `leaseApplicationComplete`'s pre-existing `missing_decision` GapWithoutPlaybook (a genuine live
finding, not a fixture artifact) and reported the honest op-effects coverage (2 of 19 referenced ops
declare `.effects` today, 17 unanalyzable — matching this doc's "only `packages/lease-signing`
declares Effects" baseline plus one more op declared since). No cross-lane ask; lead self-review.

### 🏗️ Build checkpoint — F25.1 SHIPPED 2026-08-02 (`d0b879d8`), next is F25.2

The shared target-model module landed in `cmd/loupe/weaver.go` and is what F25.2 reads: three routes
(`/api/weaver/targets`, `/api/weaver/target/<targetId>`, `.../entity/<entityId>`), the `weaverTargetBody`
parse of §10.8, `scanWeaverRows`, `splitWeaverStateKeys`, `buildWeaverMetaIndex` and the row/mark/count
joins. View logic is `web/js/logic/weaver.js` (goja-tested), DOM in `web/js/views/weaver.js`.

Three grounding corrections the live stack forced, which F25.2's checks must build ON rather than
rediscover:

1. **Targets and patterns are indexed off their SPEC BODY's `targetId` / `patternId`, never
   canonicalName.** A `meta.weaverTarget` carries no `canonicalName` aspect at all, and the violation
   lens a target binds routinely carries the *target's* name (`leaseApplicationComplete` is the lens's
   canonicalName on the dev stack). This is the same keying the engine's registry uses
   (`targetOwner` / `patternMeta`). V1's "pattern refs resolve to installed `meta.loomPattern`s" must
   use `weaverMetaIndex.Patterns`, which also aliases the vertex NanoID (the engine resolves either).
2. **`reads` accepts a derived-aspect form `row.<column>.<aspect>`** — the column resolves to a vertex
   root key and the aspect is joined on (`strategist.go` `resolveReadKey`). Params / subject / assignee
   / target are EXACT column lookups. V1's "templated params reference columns the rows actually carry"
   check must keep that asymmetry or it will flag working playbooks.
3. **Column observation is evidence, not proof.** A gap column absent from every scanned row means the
   lens does not project it *or* the lens has no candidate entities yet — two live targets
   (`capabilityAuthorDispatch`, `clauseSatisfaction`) have zero rows today. V1 is specified as
   observed-evidence for exactly this reason; keep the evidence class on the verdict.

Also available to F25.2 and unused so far: the retry-budget rule (`gapBudget` — declared
`maxretries_<g>` wins, `directOp` falls back to the engine's 3, everything else is UNKNOWN not zero) and
`issuesNaming`, the whole-token message match that attributes heartbeat issues to a target (the
`issueCache` key carrying the targetId is not published, so text is the only attribution available —
label verdicts accordingly).

F25.1 deliberately did NOT surface `TargetRejected` — that is V2's "install-verdict surfacing", and its
message names the meta vertex (`vtx.meta.<id>`), not the targetId, so it will not come through
`issuesNaming`; V2 needs its own attribution path.

## 14. For Andrew (ratification)

**What:** a Loupe program (F25) giving the convergence plane a target-shaped console — observe
(structure + live state + entity drill), verify (structural/install/interference checks over declared
effects), author (visual target/lens drafting through the *existing* capability review lane, with a
born-disabled dev-stack trial). Zero engine changes, zero contract changes; one cross-lane ask (the
operator-submission op, Fork 3) gating only F25.3b; the author stage cannot bypass the review gate.

**Fork 1 — authoring width (recommendation: restricted subset).** v1 authors exactly what the
capability-materializer accepts (explicit-action gaps; no `goal`/`candidates`/`augur` authoring —
they render read-only). Matches the AI lane's deliberate restriction; widening is its own
ratification later. Alternative — author the full planner surface now — rejected: it would make the
visual lane *wider* than the AI lane the same reviewers gate, and the planner surface is the part
still accruing build (Fire 9 tail).

**Fork 2 — trial arming (recommendation: explicit `enable` only).** The trial never auto-arms; the
operator's `enable` is the single switch from inert to live dispatch on the dev stack. Alternative —
an auto-arm-with-TTL convenience — rejected: an unattended arm on a stack with real data is exactly
the class of surprise the born-disabled seam exists to prevent.

**Fork 3 — the human entry into the capability lane (recommendation: ratify the op with this
program).** F25.3b needs `SubmitCapabilityProposal` (§6.4): a creation op on the existing
`capabilityproposal` DDL, capability-gated to operators, explicit `source` field, no claim
indirection — a `packages/capability-author` addition, filed to the lattice lane on ratification.
The alternative — keep the lane AI-only and stop the studio at export (F25.3a) — is coherent but
leaves the review console one-source and the studio's loop ending in a file download. F25.3b is
cleanly severable if you prefer that.

## 15. Adversarial pass (run this fire, findings folded)

Read-only adversarial reviewer, 2026-08-02 — ten claims re-grounded in code (`weaver.md` treated as
possibly drifted per arch-review W1; on the revoke seam it had **not** drifted — re-proven at
`internal/weaver/control.go:184-227`).

- **BLOCKER (folded — reshaped §6/§7/§13/§14):** the propose step as first drafted named a
  nonexistent transport. `ReviewCapabilityProposal` (F16) and the augur `ReviewProposal` are
  *verdict* ops; the only artifact-bearing creation op, `RecordCapabilityProposal`, is
  creation-locked to the AI bridge (live authoring-claim indirection, model-shaped provenance,
  mandatory confidence — `packages/capability-author/ddls.go:384-519`; minting the claim fires the
  external AI adapter, `:844`). Fold: §6.4 specifies `SubmitCapabilityProposal` as the program's one
  cross-lane ask; F25.3 split into 3a (no dependency) / 3b (gated); the "zero cross-lane asks"
  headline corrected.
- **MATERIAL (all folded):** (1) the born-disabled trial is single-replica-scoped (per-process
  disabled set, seeded at Start) — precondition stated in §6.5/§11; a typo'd revoke leaks an orphan
  `__control` marker nothing sweeps (`reconciler.go:177-188` skips `__control`; reconcile clears
  only registered targets' markers) — Observe now lists orphans first-class (§4/§11). (2)
  Entity-drill artifact links scoped to in-flight marks only — a closed gap's mark is deleted
  (`evaluator.go:54`, `reconciler.go:280`) and an external reclaim re-mints `claimId`
  (`reconciler.go:613-621`); completed dispatches route through flows/tasks by subject (§4). (3)
  The `.effects` read is a full `vtx.meta.` enumeration plus the aspect-envelope unwrap — costed,
  cached per render, bounded by the meta corpus (§5/§11). (4) Contraction is a trend class, not a
  count (`contraction.go:86-94`) — the all-targets list shows class + state; counts are per-target
  (§4). The oscillation chip renders issue text unparsed, following F18's deliberate refusal to
  parse pairs out of free text (§4).
- **CLEAN (re-grounded, load-bearing):** revoke-of-unknown born-disabled proven in code end to end,
  including mid-run registration (marker + in-memory set written synchronously,
  `control.go:184-227`; reconcile cannot eat a never-registered id's marker; `handleRow` skip at
  `evaluator.go:89` after mark-clearing at `:54`; Loupe's relay carries disable/enable/revoke).
  `ValidateCapabilityArtifact` exported + already constructed in `cmd/loupe/review.go:541`; both
  artifact kinds live (`ENABLED_KINDS`, `ddls.go:355`); Fork 1's restricted subset confirmed
  field-for-field (`capabilitymaterializer.go:374-399`). weaver-state read blocked by no gate
  (`lint-conventions.go:186` matches only core-kv, and Loupe is the inspector) with transport
  permission already granted (`natsperm/matrix.go:283`); §10.3 shapes frozen incl. `__count` and
  `__effect`. `guardgrammar.Parse` exported, importable. The would-dispatch panel is computable
  without dispatching (shadow mode is the engine's own existence proof). Author gating is
  fail-closed by construction (`demo.go:41-70` method default-deny; control's omission-denies
  read allow-list). Parlance clean; citations verified (one off-by-a-few fixed in §5).
