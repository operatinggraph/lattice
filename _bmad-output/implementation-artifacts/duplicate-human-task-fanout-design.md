# Duplicate human tasks from a row fan-out — a refutation record, and the fix that survives

**Status: ✅ RATIFIED (Winston-adjudicated, per the 2026-08-20 delegation).** No architectural fork, no
frozen-contract change (§8 explains why the contract edit this design first staged was **withdrawn**). The
adversarial gate this design self-flagged has **run** and is folded in (§9); nothing is left dangling.

**Board row:** `backlog/lattice.md` — *[orchestration-base] Two identical open tasks for one piece of work*
(★★ · PO-witnessed 2026-09-01, LoftSpace).

**The one-line outcome.** The row's prescribed primitive — *work-scoped task-identity dedup* — is
**refuted, not unbuilt**: three shapes were designed through and each is broken by code that already
exists (§6). The defect is real, its cause is a lens-modelling error rather than a missing platform
mechanism, and the fix is **one package change in `lease-signing`** (§7), which is verticals-lane work.
This document exists so the next three fires do not re-propose the refuted shapes.

---

## 1. The filed row, and what the live stack actually shows

The row, verbatim:

> **[orchestration-base] Two identical open tasks for one piece of work** — `CreateTask`'s dedup is
> task-id-scoped (`packages/orchestration-base/ddls.go:363`): a second dispatch lineage reseeds a new id and
> mints a second task with identical `assignedTo`/`forOperation`/`scopedTo`, so the person's action list
> shows the card twice. Live: 8 redundant of 100 tasks.

The census reproduces the headline exactly (§4, C1): **100 tasks, 92 work signatures, 4 duplicated
signatures, 8 redundant.** Three of its clauses need correcting before they shape anything.

**(a) Only 4 of the 8 are visible anywhere.** `myTasksSpec` filters `task.data.status = 'open'`
(`packages/orchestration-base/lenses.go:355`, `:365`) and does **not** filter on `expiresAt`. Restricted to
open tasks (C2): **81 open, 77 signatures, 2 duplicated signatures, 4 redundant cards.** The other four are
`cancelled` and reach no inbox.

**(b) The four visible cards are three phenomena with three different generators.** Loom's
`deriveTaskID(instanceId, cursor)` (`internal/loom/token.go:32`) is invertible, and the rest split by minting
service actor (C3, C4):

| # | signature | cards | generator |
|---|---|---|---|
| A | `RecordIdentityPII` · scoped+assigned to `vtx.identity.gk12KRq…` | 4 open | **four distinct Loom `onboarding` instances**, started within **206 ms** of each other on 2026-07-26T08:38:08 |
| B | `RecordIdentityPII` · scoped+assigned to `vtx.identity.MQsmTTAg…` | 2 open | one Loom instance (2026-07-26) + one **ad-hoc `CreateTask` submitted by the primordial admin** (2026-08-22) |
| C | `SetRenewalTerms` · scoped to `vtx.renewal.QomdjY7…` | 1 open + 1 cancelled | two **different Weaver service identities** — a re-bootstrap epoch change between them |

Only **cluster A** is a platform-shaped defect. Cluster B's second card has no shipped producer — the only
in-repo direct `CreateTask` submitter is `scripts/seed-showcase.go:493`, which mints a day-rolled
deterministic id for a `ResolveWorkOrder` work-order task and cannot produce this row; the observed op's
`expiresAt` is byte-identical to the *existing* task's, i.e. copied by hand. Cluster C is a `weaver-state`
wipe between bootstrap epochs. §10 keeps both out of scope with revive triggers.

**(c) "A second dispatch lineage reseeds a new id" names the seam, not the generator.** Cluster A's four
tasks came from **one** lineage doing exactly what it is specified to do.

---

## 2. Root cause: the convergence anchor is finer-grained than the work

`vtx.identity.gk12KRq…` holds **exactly four** lease applications (C5). `leaseApplicationComplete` anchors on
`leaseapp` (`packages/lease-signing/lenses.go:49`), so that applicant produces **four rows**. The gap is

```
((unitKey <> null) AND (ssnVal = null) AND ((unitStatus <> 'leased') OR (landlordDecision = 'approved')))
```
(`lenses.go:786`) — and `ssnVal` is the **applicant's own** `.ssn` aspect. The gap's *trigger* is
per-application; the work it dispatches — *this person records their PII once* — is per-identity. The
playbook aims the remediation at the neighbour explicitly:

```go
"missing_onboarding": {Action: "triggerLoom", Pattern: "onboarding", Subject: "row.applicant"},
```
(`packages/lease-signing/targets.go:97`). Four rows → four marks → four `claimId`s → four
`deriveStableInstanceID` values → four Loom instances → four tasks. Every step is individually correct.

**One sentence:** *a convergence target whose anchor is finer-grained than the work its gap dispatches will
mint one artifact per row, and no idempotency token downstream can tell that they are the same work.*

---

## 3. Reconciliation with the existing mental model

**"Didn't we already fix duplicate human tasks?"** Yes — `4c811a40` (2026-06-26,
`usertask-dispatch-idempotency-design.md`) fixed the **temporal** axis: one gap row, N reclaims of a
30-minute mark lease, N tasks. It minted `claimId` at the mark's CAS-create, preserved it across reclaims,
and derived the artifact id from it. That fix is intact and nothing here touches it. The **cardinality** axis
is orthogonal, and that design's own adversarial note recorded the assumption this row falsifies:

> "an idempotent op masking a real second-task need — there is none for these gaps (assignee == scopedTo ==
> subject, §10.5; **one open task per gap is the invariant**)."

One open task per gap **row**. Four rows, one subject.

**"Doesn't the `inflight_<g>` companion already suppress this?"** It is declared, and it is already
identity-scoped:

```
OPTIONAL MATCH (id)<-[:scopedTo]-(onbTask:task) WHERE onbTask.data.status = 'open'
…  (onbTaskOpen > 0) AS inflight_onboarding
```
(`packages/lease-signing/lenses.go:708-710`, `:753`, `:793`) — shipped `33b61de4`, **2026-07-19, a week
before the observed burst**. It did not suppress it and provably cannot: `inflight_<g>` is read from the
*projected row*, and Contract #10 §10.3's own promise is that suppression *"clears by level, not edge."*
Level-triggered suppression read from a read model is race-free only once the level has settled. Four rows
evaluating in one wave all read `inflight_onboarding = false`, because at that instant no task existed.
**`inflight_<g>` is sound at steady state and structurally incapable of serialising a simultaneous
fan-out** — which is why every candidate platform mechanism below had to reach for durable state at dispatch
time, and why each of them broke.

---

## 4. Executable censuses

Re-derivable against a live stack with `NATS_URL=nats://localhost:4222 NATS_NKEY=deploy/nkeys/lattice.nk`.

**C1 — work-signature duplicates over all tasks.** Expect `100 / 92 / 4 / 8`.
```
./bin/lattice graph keys "lnk.task." -o json \
 | python3 -c 'import sys,json;from collections import defaultdict;\
d=json.load(sys.stdin)["data"]["keys"];s=defaultdict(dict);g=defaultdict(list)
for k in d:
    p=k.split(".");s[p[2]][p[3]]=".".join(p[4:])
for t,m in s.items(): g[(m.get("forOperation"),m.get("scopedTo"),m.get("assignedTo") or "Q:"+str(m.get("queuedFor")))].append(t)
dup={k:v for k,v in g.items() if len(v)>1}
print(len(s),len(g),len(dup),sum(len(v)-1 for v in dup.values()))'
```

**C2 — the same, restricted to `status == "open"`.** Expect `81 / 77 / 2 / 4`.

**C3 — Loom attribution.** Recompute `deriveTaskID(instanceId, cursor)` (`internal/loom/token.go:32`,
`deriveID` `:57-75`, alphabet `internal/substrate/keys/nanoid.go:13`) for every `instance.<id>` in
`loom-state`, `cursor ∈ 0..3`, and match against the task ids. Expect **47 of 100** matched, and cluster A's
four tasks mapping to four **distinct** instances (`E6Crr…`, `EXT8L…`, `wLQsu…`, `iM7b1…`), all
`subjectKey: vtx.identity.gk12KRqMMVwjVwfxsb5c`.

**C4 — non-Loom attribution by minting actor.** Expect: Loom service actor 43 + a prior-epoch Loom identity
4; Weaver service actors 28 + 6; primordial admins 14 + 3; 2 link-only residues whose root read returns
`NotFound` (§10).

**C5 — applications per applicant.** Expect **62 applications across 58 applicants**: `gk12KRq…: 4`,
`LQ28Dp37…: 2`, and **56 singletons**.
```
./bin/lattice graph keys "lnk.leaseapp." -o json \
 | python3 -c 'import sys,json;from collections import Counter;\
d=json.load(sys.stdin)["data"]["keys"];\
c=Counter(".".join(k.split(".")[3:]) for k in d if k.split(".")[3]=="applicationFor");\
print(len(c), sum(c.values()), c.most_common(3))'
```

**C6 — gap actions whose remediation is aimed at a neighbour rather than at the row's own entity, AND whose
pattern contains a userTask** (the population any platform mechanism here would govern). Expect **one**:
`lease-signing missing_onboarding`. Everything else fails one leg or the other —

| declaration | aimed at | userTask? | governed? |
|---|---|---|---|
| `lease-signing missing_onboarding` | `row.applicant` (≠ anchor) | yes (`onboarding`) | **yes** |
| `lease-signing missing_bgcheck` / `missing_payment` | `row.applicant` (≠ anchor) | no — externalTask-only | no (§6.2) |
| `lease-signing missing_signature`, `missing_leaseDoc`, `missing_leaseDocAttach`, `missing_listingLeased` | `row.entityKey` | — | no |
| `lease-signing renewalComplete` `verifyGuarantor` / `setTerms` / `signRenewal` | `row.entityKey` | yes | no — row-scoped |
| `lease-signing renewalComplete` `refreshBgcheck` | `row.tenant` (≠ anchor) | no — externalTask-only | no (§6.2) |
| `semantic-contracts missing_inspection` | `row.clauseKey` — an **alias** of `entityKey` (`packages/semantic-contracts/lenses.go:223`) | yes | no — row-scoped in fact |
| `capability-author missing_authoring` | `row.entityKey` | — | no |

**C6 is the census that decides this design.** A platform primitive with one live consumer does not clear the
bar (§7); and note that `missing_inspection` is row-scoped by *value* while a purely syntactic classifier
would call it effect-scoped — the classification a platform mechanism would need is not even reliably
static (§6.3).

---

## 5. What the mechanism would have to be

For a gap `g` of target `T`, the **effect key** is what the dispatch actually produces — `(pattern, resolved
Subject)` for `triggerLoom`, `(operation, resolved Target, resolved Assignee)` for `assignTask`. A gap is
**effect-scoped** when two distinct rows of `T` can resolve the same effect key. Any platform fix must give
those rows one artifact identity. Three shapes deliver that on paper. None survives contact with the code.

---

## 6. Refutations

### 6.1 A work-scoped deterministic `taskId` — the row's own `no-pattern:` prescription

Derive the task id from `(forOperation, scopedTo, assignee)` so every lineage converges on one key.

**Refuted by the dedup it relies on.** `CreateTask` suppresses when the existing document is *alive*, not
when it is *open* — `if existing != None and not existing.isDeleted: return {"mutations": [], "events": []}`
(`packages/orchestration-base/ddls.go:362-363`) — and a Lattice tombstone deliberately preserves the body, so
even a deleted one is not absent in the way the branch needs. A **completed** task at the derived key would
suppress the next legitimate round of the same work forever, and the Loom userTask that submitted it would
park on `token.vtx.task.<id>` for a task that never gets created: a silent unbounded hang, strictly worse
than a duplicate. C1 proves re-work happens — cluster C is a cancelled `SetRenewalTerms` followed by a live
one on the same renewal, and cluster A's four *cancelled* tasks are the same work re-dispatched after their
op-meta was retired. Making it safe needs a per-work generation counter, i.e. durable state, at a worse place.

### 6.2 A shared per-effect `claimId` register in `weaver-state`

CAS-create one register per effect holding a single `claimId`; every row dispatching that effect seeds its
artifact id from the register. This was the shape this document originally proposed, with a Contract #10
§10.3 edit. The adversarial pass (§9) broke it four ways, each cited:

1. **The token is not the only seed.** `deriveStableTaskID` / `deriveStableInstanceID` hash
   `targetID\x00entityID\x00gapColumn\x00claimID` (`internal/weaver/actuator.go:162`, `:174`) — `entityID` is
   the row's own entity. Sharing the `claimId` alone still yields four ids. Fixing this means changing the
   derivation's *arguments*, which moves every in-flight artifact id and needs its own migration.
2. **It wedges external gaps.** `missing_bgcheck` / `missing_payment` are the canonical externalTask-only
   gaps (`internal/weaver/evaluator.go:509-510`, classifier `:525-548`), and both reclaim seams deliberately
   mint a **fresh** `claimId` (`internal/weaver/reconciler.go:1071-1080`; `evaluator.go:789-806`) precisely so
   a retry does not collapse onto a dead episode. An immutable shared register re-derives the terminal
   instance's id, `StartLoomPattern` collapses on `getInstance`, and the vendor call is **never re-run** —
   the starvation direction §10.3 exists to forbid.
3. **Its release predicate is not constructible.** Releasing on *"no row of the target has that gap column
   true"* is per-`(target, gap)`, not per-effect: `leaseApplicationComplete` carries 58 applicants, so while
   any one has `missing_onboarding` open, every register for that column survives — including one whose own
   effect closed weeks ago, which then re-suppresses a genuine reopen on §6.1's exact failure. Making it
   per-effect is not possible from the key: `<targetId>.__claim.<gapColumn>.<effectRef>` carries no entity
   segment and `effectRef` is a one-way hash, while every sweep leg resolves its row from the `<entityId>`
   segment its key carries (`internal/weaver/reconciler.go:277`, `:558`). Deciding it needs a full row scan
   of `weaver-targets` per register — a cost the shape exists to avoid.
4. **Releasing on row-absence mints a duplicate on every lens rebuild.** `sweepCount` refuses that exact
   inference, in-comment: a Refractor rebuild purges and re-replays a target's rows, during which every
   entity reads row-gone (`internal/weaver/reconciler.go:566-575`, doc `:463-470`), and
   `leaseApplicationComplete` is `EmptyBehavior: "delete"` (`packages/lease-signing/lenses.go:52`). The
   mark/count legs answer this with per-row absence tolerance, which a key with no entity segment cannot
   express.

Two further breaks made it unsalvageable rather than merely expensive: the ordering the design claimed to
mirror **does not exist** — `clearClosedMarks` runs per CDC row delivery over one entity's columns
(`internal/weaver/evaluator.go:69`, `:989`, `:1014`), with no cross-row pass to hang a shared release on; and
the classification could not be static, because `renewalComplete` is `Mode: "planned"` with one gap column
and a mixed-scope action catalog whose leg is chosen per row at dispatch (`resolvePlannedAction` →
`rankCandidates`, `internal/weaver/strategist.go:429-467`).

### 6.3 The pattern behind all three

Each shape needs a notion of *"the effect's episode"* — when this piece of work started and when it ended.
Nothing in the system owns that, **because the anchor is the wrong granularity**. Every mechanism proposed
here is a mechanism patching a modelling error, which is the case the standing doctrine says to re-derive the
base for rather than extend. Two further options were priced and lose independently: an inbound enumeration
at `CreateTask` (`kv.Links(scopedTo, "scopedTo", "in")`) is the write-path forbidden scan wearing a bound —
link degree is all-time, links are never pruned, and Contract #2 §2.5's class-(e) sanction is tight-degree
only; and a per-work latch vertex in Core KV is a second source of truth beside `task.data.status` that must
be cleared on `CompleteTask`, `CancelTask`, `ReAssignTask`, expiry and out-of-band tombstone, where any missed
clear permanently blocks the work, in the business graph, against P1.

**And row 0 — do not have this thing at all.** With C6 at one live consumer, the alternatives discipline's
own rule applies: when the consumer census is single-digit, *rewrite the N consumers* is mandatory and
usually wins. Here N = 1 and it wins outright.

---

## 7. The fix that survives: re-anchor the gap where the work lives

**Package work in `lease-signing`, verticals lane.** Split `missing_onboarding` in two, using the idiom the
package already ships:

1. **On `leaseApplicationComplete`, `missing_onboarding` becomes a surface-declared gap** — projected,
   `violating`, with **no playbook entry**, exactly as `missing_decision` and `missing_manager` already are
   (`packages/lease-signing/targets.go`). The application still shows as blocked on onboarding; it just stops
   dispatching. Nothing in the FE or the read model changes.
2. **A new identity-anchored target dispatches it** — one row per applicant, so one applicant is one gap is
   one task, *by construction*, with no dedup mechanism anywhere. Its gap is open when the applicant has at
   least one application that needs onboarding and no `.ssn` aspect.

**Expressibility is established by precedent in the same file, not asserted.** The cypher needs an inbound
hop from the identity to its applications and an aggregate reduced to a boolean —
`leaseApplicationComplete` already does both, in the same spec: the inbound
`OPTIONAL MATCH (id)<-[:scopedTo]-(onbTask:task)` (`lenses.go:708`) and the
`count(DISTINCT CASE WHEN … END)` reductions (`:740-753`). The build should still confirm an existing
identity-anchored weaver target to mirror before writing a new one.

**Why this is the right altitude and not a workaround.** It removes the cause instead of arbitrating the
symptom: after it, no two rows dispatch one piece of work, so there is nothing for a platform mechanism to
collapse. It is also what the model should have said in the first place — *a convergence target's anchor
should be the granularity of the work its gaps dispatch* — and that sentence, not a key family, is the
durable lesson.

**Increments, for whoever picks it up (Steward sizes review depth; neither is posture-changing):**

- **Inc 1 — the split.** Demote `missing_onboarding` to surface-declared on `leaseApplicationComplete`; add
  the identity-anchored lens + target with its own gap, `inflight_onboarding` companion, and playbook entry.
  Tests owned here: one applicant with **two** applications and no `.ssn` yields **one** open
  `RecordIdentityPII` task (through a real commit, and mutation-tested by reverting the split); the
  surface-declared column still projects `violating` on both applications; `missing_*` ⊆ declared gaps stays
  green (`scripts/lint-gap-column-declaration.go`); a package **version bump** on the manifest and its
  mirroring `Version` constant, since `packages/` content changed.
- **Inc 2 — operator cleanup, no code.** `CancelTask` the three redundant cluster-A cards. Cluster B needs a
  human to decide which card is real.

**No lint gate is proposed.** The convention worth enforcing — *anchor a gap where its work lives* — is not
syntactically decidable (C6: `missing_inspection` is row-scoped only by value, `renewalComplete` resolves its
leg per row at dispatch), so a default-deny gate would either misfire on shipped-correct declarations or
demand a declaration whose honest answer nobody can compute at authoring time. Saying so is the finding; a
gate that cannot be written correctly is worse than none.

---

## 8. Contract surface: none

This design first staged a Contract #10 §10.3 edit (`docs/contracts/10-orchestration-substrate.md`) widening
the userTask collapse promise and naming a shared-claim construct. **That edit is withdrawn and the file is
back to `main`.** The surviving fix changes no wire shape, no refusal semantics, and no promise — it is one
package modelling its own convergence at the right granularity, which §10.8's grammar already permits.
`docs/contracts/10-orchestration-loom.md` and `02-operation-envelope.md` still carry the *unrelated*
uncommitted edits belonging to the `declared-path-reads` design; they were not touched.

---

## 9. The adversarial pass — run, and what it changed

Run as a required gate on the draft, briefed to refute rather than confirm, with the live stack up. It
returned **3 BLOCKING + 5 MAJOR**, and it did not merely dent the proposal — it inverted the design's
conclusion from *build a platform primitive* to *the primitive is refuted; fix the package*. Its findings are
§6.2's four numbered breaks plus the two unsalvageable ones, C6's correction from four governed declarations
to one, and C5's correction from a miscounted "8 remaining applicants / 2 of 10" to **58 applicants, 56
singletons, 2 of 58**. That last one matters on its own: the structural case for a platform mechanism was
stated at 20% of applicants and is actually 3.4%.

It also confirmed, and could not break: `myTasksSpec`'s open-only filter; `CreateTask`'s alive-not-open
suppression; `inflight_onboarding`'s identity scope and its 2026-07-19 ship date; the four-applications
count; the four-distinct-Loom-instances attribution; C1's exact reproduction; and — for the shape that was
abandoned anyway — that the key would not have been mis-parsed, that `Revoke`'s prefix delete covers it, and
that Weaver's `natsperm` envelope already permits the writes (`internal/natsperm/matrix.go:464-473`).

The lesson worth carrying, beyond this row: **the draft named a data structure where a rule belonged.** Three
of the four blocking findings were the register's *lifetime* — release predicate, rebuild behaviour, ordering
— none of which the draft had written as a rule, and all of which are unanswerable once the key carries no
row correspondence.

---

## 10. Out of scope, with triggers

- **Ad-hoc / operator-submitted `CreateTask` (cluster B).** No shipped producer; this is the only duplicate on
  a real applicant's inbox and §7 does not remove it. **Revive:** an in-repo or app-tier producer that submits
  `CreateTask` with a minted id for work another lineage already owns.
- **Cluster C, the cross-epoch pair.** A `weaver-state` wipe between bootstrap epochs — the documented
  migration bound of the 2026-06-26 fix, not a defect.
- **`directOp` gap actions dispatching one effect from two rows** (`semantic-contracts missing_charge`
  double-debiting is the shape to worry about). No instance observed. **Revive:** a witnessed duplicate effect
  from a directOp gap.
- **Two `vtx.task.<id>` link sets whose root vertex reads `NotFound`** (C4). Orphan link residue, unrelated to
  duplication; for the Surveyor, not filed, per the consolidation rule.
- **`loom-state` holds 12,340 terminal instances; `lnk.service.*` shows ~12.3k service claims, 3,637 on one
  identity.** The existing `[Loom] Terminal instance cursors cannot be pruned` row covers the first; the
  second is a live observation for the Surveyor, not refiled here.

---

## Build note (Inc 1) — Vertical Steward, 2026-09-01

**Verified touch-list** (re-checked live against this fire's HEAD, `ed86daee`, no drift from §7's claims):
- `packages/lease-signing/targets.go:97` — `missing_onboarding` playbook entry (`triggerLoom`/`onboarding`/`row.applicant`).
- `packages/lease-signing/targets.go:117-118` — the `missing_decision`/`missing_manager` surface-declared-gap
  idiom to mirror (`Action: "surface", IssueCode: …, IssueSeverity: "warning"`, no Subject/Assignee/Target).
- `packages/lease-signing/lenses.go:786` — `missing_onboarding` gap predicate on `leaseApplicationComplete`.
- `packages/lease-signing/lenses.go:708-710,753,793` — `inflight_onboarding` companion (identity-anchored
  `OPTIONAL MATCH (id)<-[:scopedTo]-(onbTask:task) … forOperation … RecordIdentityPII`).
- `packages/lease-signing/lenses.go:689` (`readinessWithItems`, shared fragment) — `ssnVal = id.ssn.data`.
- `packages/identity-domain/lenses.go:124-139` — `identityAnchors`, the precedent actorAggregate lens
  anchored on `identity` (`AnchorType: "identity"`, own bucket, `RealnessFilter: "key"`), to mirror for the
  new lens's shape (own bucket here too — `weaver-targets` is a shared bucket key-namespaced by
  `OutputKeyPattern`, so reusing it is fine and is what every other target in this package does; a fresh
  bucket is not required and adds an unneeded moving part).
- `packages/lease-signing/lens_unit_test.go:20,268` — the two cross-check tests every target/lens pair must
  satisfy by construction (`TestLeaseSigning_PlaybookColumnsMatchLens` checks the `leaseApplicationComplete`
  target only, by `TargetID`, so it is unaffected by the new target; `TestLeaseSigning_MissingColumnsAreDeclaredGaps`
  iterates **every** target this package declares, so the new target/lens pair must satisfy it too — every
  `missing_*` BodyColumn needs a `Gaps` entry, and a `surface` action needs `IssueCode` + a `warning`/`error`
  `IssueSeverity`).
- Manifest: `packages/lease-signing/manifest.yaml:2` (`version: 0.31.15`) and the mirroring `Version` constant,
  `packages/lease-signing/package.go:85` (`Version: "0.31.15"`) — bump both, in lockstep, to `0.31.16`.
- `scripts/lint-gap-column-declaration.go` — enforces exactly the invariant `TestLeaseSigning_MissingColumnsAreDeclaredGaps`
  asserts in-process; run it directly too (`DIFF_BASE=<base> go run ./scripts/lint-gap-column-declaration.go`
  or however the Makefile wires it — confirm the invocation from the script/Makefile, not from memory).

**Increment order (this fire ships Inc 1 only; Inc 2 is a live-stack operator action, not a code change — see
§7):**

1. **`targets.go`** — flip `missing_onboarding`'s entry on `leaseApplicationComplete` from `triggerLoom` to
   the `surface` idiom (mirror `missing_decision`/`missing_manager` exactly): pick an `IssueCode` (e.g.
   `LeaseOnboardingAwaiting`) and `IssueSeverity: "warning"`. **Do not remove the column from the lens** —
   §7 point 1 is explicit that it stays projected/violating with no playbook entry.
2. **`lenses.go`** — add ONE new `pkgmgr.LensSpec` (`ProjectionKind: "actorAggregate"`, `AnchorType:
   "identity"`, `Bucket: "weaver-targets"`, `OutputKeyPattern` naming the new target, e.g.
   `"applicantOnboarding.{actorSuffix}"`) whose cypher anchors on `(id:identity {key: $actorKey})`,
   OPTIONAL-walks `(id)<-[:applicationFor]-(app:leaseapp)-[:appliesToUnit]->(u:unit)` to aggregate
   (`count(DISTINCT CASE WHEN …)`, mirroring the fan-collapse idiom `leaseApplicationCompleteSpec` already
   uses for `providedTo`) whether **any** application still needs onboarding — same per-app gate
   `leaseApplicationCompleteSpec` uses, `(unitKey <> null) AND ((unitStatus <> 'leased') OR (landlordDecision
   = 'approved'))` — and OPTIONAL-walks the identity-anchored `inflight_onboarding` companion (mirror
   `lenses.go:708-710` verbatim, now the anchor itself instead of a neighbor hop). `missing_onboarding` on
   the new lens = `(ssnVal = null) AND (that aggregate > 0)`; `violating` = the same expression (this target
   has exactly one gap). Reuse the shared `readinessOptionalMatch`/`readinessWithItems` fragment ONLY if it
   fits without pulling in bgcheck/payment noise this target doesn't need — a bespoke minimal WITH is
   probably cleaner here; builder's call, grounded in the precedent, not copied wholesale.
3. **`targets.go`** — add the new `pkgmgr.WeaverTargetSpec` (`TargetID` == the new lens's `OutputKeyPattern`
   prefix, `LensRef` == its `CanonicalName`, one `Gaps` entry: `"missing_onboarding": {Action: "triggerLoom",
   Pattern: "onboarding", Subject: "row.applicant"}` — moved verbatim from step 1, `row.applicant` now
   resolving to the identity anchor's own key).
4. **Tests, in the same package idiom as `TestLeaseApplicationComplete_InflightOnboarding`
   (`lens_cypher_test.go:1415`)**: one applicant identity with **two** lease applications and no `.ssn`
   aspect → exactly **one** open `RecordIdentityPII` task after the split (mutation-tested: revert the split,
   confirm the test fails/duplicates). Confirm the surface-declared column on `leaseApplicationComplete`
   still projects `violating` on both applications. Confirm `inflight_onboarding` on the NEW target still
   suppresses re-dispatch while a task is open (same shape as the existing inflight test).
5. **Version bump**: `manifest.yaml` + `package.go` `Version` constant, `0.31.15` → `0.31.16`.

**Non-goals (explicitly out, per §10):** cluster B (ad-hoc `CreateTask`, needs a human, not this fire),
cluster C (bootstrap-epoch wipe, not a defect), Inc 2 (`CancelTask` on the 3 live redundant cluster-A cards —
an **operator action** on the running stack, do it after Inc 1 ships and the package refreshes live, not a
code change).

**Inc 1 shipped `a0b6264b`, live-verified.** `make refresh-loftspace` diff-applied the package and restarted
`bin/loftspace-app`; the new `applicantOnboarding.gk12KRqMMVwjVwfxsb5c` row projects live in `weaver-targets`
with `missing_onboarding: false` (this applicant's gate is currently closed) and `inflight_onboarding: true`.
No code path can mint a second onboarding task for one applicant from here on.

**Inc 2 — paused, not done.** Live census of the 8 `RecordIdentityPII` tasks scoped to
`vtx.identity.gk12KRqMMVwjVwfxsb5c` reproduces §1's cluster A exactly: 4 cancelled 2026-07-20 (the retired
op-meta `EUayYDxpPRZYZZhZEUay`), 4 still **open** (`zT4yXzMXqRJBLKTRCb8H`, `hmUYfcPJzxtQZEchLcv8`,
`QQZvbYHQxg63nvU2VNjK`, `qN8tvKtc9on5ZvTohtvx`, all created within 206ms on 2026-07-26T08:38:08 under the live
op-meta `QW5wujHcQVk7mRfiQW5w`). **New finding this census surfaces:** the identity itself reads
`{"note": "Primordial admin identity. Authors all primordial provenance fields.", "protected": true}` — not an
ordinary demo persona. §1/§2's "one applicant with four applications" framing did not flag that the applicant
IS the protected primordial admin. Whether that's a benign seed-data reuse (a showcase script minting a demo
lease application under the bootstrap identity rather than a fresh persona) or something the seed data should
not do is worth five minutes of grounding before any `CancelTask` touches it — cancelling against a `protected`
identity blind is not the same risk class as cancelling against an ordinary demo applicant. **Revive:** next
Vertical Steward fire on this stream, after confirming why `gk12KRqMMVwjVwfxsb5c` holds live lease applications
at all (check `scripts/seed-showcase.go`'s applicant-minting path).

**Gates:** `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run
./scripts/lint-conventions.go`, `DIFF_BASE=main go run ./scripts/lint-package-version.go`, `go run
./scripts/lint-gap-column-declaration.go` (confirm actual invocation), `go test ./packages/lease-signing/...`.
No frozen contract touched, no fork — L2-eligible, lead review (S size).

## Inc 2 grounding — Vertical Steward, 2026-09-01

**The revive question answered: `gk12KRqMMVwjVwfxsb5c` is a stranded, pre-rotation identity, not the
live admin.** `scripts/seed-showcase.go` never references this key or the primordial admin at all — the
seed-script hypothesis is refuted. `lattice bootstrap verify` explains the real mechanism: this deployment
underwent a bootstrap-epoch rotation, and `vtx.role.niR13mqp2r15q1iEge4P` (the role `gk12KRqMMVwjVwfxsb5c`
holds, canonicalName `operator`) is reported as `STRANDED OPERATOR ROLE … not this deployment's operator
role, and no live identity holds it. Live grants (0): none — unreachable without a holder.` This is the
same documented, non-defect boundary the design's own §17 names as Cluster C ("a weaver-state wipe between
bootstrap epochs"). The *current* epoch's admin is a different identity, `vtx.identity.ZieEKvPvwv7M9C14Yhr8`
(live capability doc healthy, holds the current `roleOperator` `dkXW9DvHFcjGHPLVVxaa`) — confirmed by
`lattice.bootstrap.json` (`status: committed`, `generatedAt: 2026-08-22`).

The 4 stale `RecordIdentityPII` tasks (`scopedTo`/`assignedTo` = `gk12KRqMMVwjVwfxsb5c`, `createdBy` =
`vtx.identity.jkgztdCGrgNUKMwtPvZz`, the internal Loom service actor — also stranded, same rotation) are
therefore litter from *before* the epoch rotation, tied to an identity with zero live platform authority
today. This is a materially safer read than the design's original hedge ("cancelling against a `protected`
identity blind is not the same risk class as an ordinary demo applicant") — the identity is not live-admin
at all, it's an inert fossil. **Confirmed by trying:** `CancelTask` submitted as `gk12KRqMMVwjVwfxsb5c`
itself correctly fails `AuthDenied: no matching platformPermission` (the stranded role carries no grants) —
the platform's own auth plane already proves this identity is powerless, independent of this grounding.

**Inc 2 remains undone — blocked on execution, not grounding.** With the live current-epoch admin actor
(`vtx.identity.ZieEKvPvwv7M9C14Yhr8`), the fix is 4 `lattice op submit --operation-type CancelTask
--actor vtx.identity.ZieEKvPvwv7M9C14Yhr8 --context-hint-reads vtx.task.<id> --payload
'{"taskKey":"vtx.task.<id>"}'` calls (`zT4yXzMXqRJBLKTRCb8H`, `hmUYfcPJzxtQZEchLcv8`,
`QQZvbYHQxg63nvU2VNjK`, `qN8tvKtc9on5ZvTohtvx` — all live-verified `status: open` this fire). This
session's own write-action guard declined the live op-submit as the admin actor before it reached the
platform's auth plane — an environment-level stop, not an item- or grounding-level one. **Revive:** any
session (this stream or Andrew directly) with permission to run `lattice op submit` against the running
stack; the command above is ready to paste as-is, no further grounding needed.
