# Loupe 2.0 — F16: the AI review console (UX design)

> **Status: 📐 awaiting adjudication — Winston (Andrew-delegated for the Loupe program, 2026-07-02).**
> Author: Sally (UX). Filed by the PO survey 2026-07-18. Builds after adjudication, UX-then-FE, per the
> Loupe pipeline. Grounded live against the shipped read-models + op DDLs (`packages/capability-author`,
> `packages/augur`, `internal/pkgmgr`) — every not-yet-readable dependency is flagged inline ⚠️ **ASSUMES**.
> House style follows [loupe-2-ux-design.md](loupe-2-ux-design.md) + [loupe-platform-edges-ux.md](loupe-platform-edges-ux.md).

---

## 0. What F16 adds — the human's window onto the AI-native loop

Picture the operator at 2 a.m. Somewhere in the platform an AI just decided the system is missing a
capability — "there's no lens listing active providers by specialty" — reasoned out a *real DDL artifact*,
validated it, and parked it. Right now that moment is **invisible from the console.** The only way to see it,
or to say *yes, install it* / *no, discard it*, is the CLI (`lattice capability …`). The platform's single
most consequential event — **an AI proposing to change the running system, waiting for a human** — has no
operator surface. F16 is that surface.

There are **two** AI-proposal loops in the platform, and F16 gives each a home. They are structurally alike — a
proposal card with intent, a proposed artifact, the model's rationale, a confidence score, provenance — and,
crucially, **both are human-gated: nothing acts until an operator approves.** They differ in the *shape* of the
approve, not in whether a human holds the gate.

| | **(a) Capability authoring** | **(b) Augur escalation (L3)** |
|---|---|---|
| The proposal | A new DDL artifact (lens / grant / weaverTarget / loomPattern / vertexTypeDDL / opMeta) | A remediation op for an orchestration gap |
| Package | `packages/capability-author` | `packages/augur` |
| Read model (bucket) | `capabilityProposals` → `capability-proposals` | `augurProposals` → `augur-proposals` |
| **The gate** | **A human.** `ReviewCapabilityProposal{verdict:approve\|reject}`, submitted BY the operator (`op.actor`). | **A human too.** `ReviewProposal{verdict:approve\|reject}`, submitted BY the operator. Dispatch (`augurDispatchPending`) fires only on `review.state="approved"` — confirmed at `packages/augur/lenses.go` — so **approval is the gate**, not the deterministic `pending` verdict before it. |
| **The approve, in detail** | Two-part + heavy: operator's approve must carry a **fresh** `validation` re-computed against the live catalog (§3.3), THEN a separate **apply** step installs the artifact (`MarkCapabilityProposalApplied`). | One-part + light: `ReviewProposal` **re-validates server-side** (no client validation payload), and approval directly arms autonomous dispatch — **no separate apply step.** |
| **The console's job** | Review the diff → approve (re-validate) → apply. | Review the proposed op → approve/reject. Then observe dispatch. |

**The spine of the design is not "who gates" — both are human-gated — it is "how heavy is the approve."**
Capability approve is the heavy one: a fresh re-validation *and* a follow-on apply (its signature is the
diff-as-confirm, the idiom the Packages page uses for install deltas, main §9.3). Augur approve is light: a
single re-validated-server-side `/api/op` submit, after which the platform dispatches on its own. Both tabs
share one proposal-card renderer and both carry an approve/reject action row; they diverge only in the approve
*plumbing* and their column vocabulary (artifact/target vs gap/proposed-op).

**One honest caveat the design must carry:** Augur's `ReviewProposal` is **test-envelope-only today — F16 would
be its first production submitter** (`packages/augur/ddls.go` flags "no production dispatcher yet"). The read
side (both buckets) is fully shipped; the Augur *write* exercises a previously-untriggered op, so F16.3 must
verify the approve→dispatch loop live end-to-end (§7).

**One console, two tabs, one shared card component.** `#/review` with a `capability` | `augur` segmented
control. The shared proposal-card renderer (intent · artifact · rationale · confidence · provenance · review
state) is written once in the goja logic tier; the two tabs differ only in their **action row** (approve/reject
vs read-only audit) and their **column vocabulary** (artifact/target vs gap/proposed-op).

---

## 1. The two loops, grounded (so the design binds to real data)

### 1.1 Capability authoring lifecycle (the human gate)

The `capabilityproposal` vertex accretes aspects across its episode; the `capabilityProposals` lens
(`packages/capability-author/lenses.go`) projects **one flat row per proposal** with every aspect surfaced
(null while in-flight). The lifecycle the console renders:

```
RequestCapabilityAuthoring   → .request   {requesterId, intent, contextRef}      (episode minted, write-ahead)
CreateAuthoringClaim (Loom)  → .claim     {claimedAt}                            (reasoning in flight)
RecordCapabilityProposal     → .artifact  {kind, content}                        (the model's DDL)
   (bridge replyOp)             .target   {mode, packageName, baseVersion, newVersion}
                                .rationale {text}   .confidence {score}
                                .validation {state, report, deltaPreview, checkedAt}   ← the DIFF
                                .provenance {model, promptHash, catalogHash, reasonedAt}
ReviewCapabilityProposal     → .review    {state, invalidReason, reviewedAt, appliedAt, appliedByOp}
   (OPERATOR, op.actor)          verdict = approve | reject
apply (pkgmgr) + MarkCapabilityProposalApplied → .review.appliedAt / .appliedByOp
```

**Review states** (`review.state`): `pending` · `approved` · `rejected` · `invalid` · (applied = `approved`
with `appliedAt` set). A row with null `.artifact`/`.review` = reasoning still in flight — render it as
`authoring…`, not as an actionable proposal.

**The load-bearing subtlety — approve re-validates.** `ReviewCapabilityProposal`'s DDL
(`packages/capability-author/ddls.go`) is explicit: on `approve`, the caller must supply a **FRESH** `validation`
payload — `pkgmgr.ValidateCapabilityArtifact` re-run **against the current catalog/registry immediately before
submitting**, because record-time and approve-time can drift (a package the proposal targets may have changed).
If it no longer validates, the approve **fail-closes to `invalid`**. So the console's approve is *not* "POST the
stored verdict" — it must re-validate at click-time. See §3.3; this is F16's one genuine architectural fork.

### 1.2 Augur escalation lifecycle (human-gated, lighter approve)

`packages/augur` mirrors the capability shape structurally, with **two** verdict stages — a machine gate then a
human gate. The `augurProposals` lens projects one flat row per `augurproposal`:

```
CreateAugurReasoningClaim (Weaver directOp) → .gap {targetId, entityId, gapColumn, trigger, model}   (escalation context)
RecordProposal (bridge replyOp)             → .proposed {action, params}     (the model's remediation op)
   (DETERMINISTIC gate: pending|invalid)      .rationale {text}  .confidence {score}
                                              .provenance {model, promptHash, catalogHash, reasonedAt}
                                              .review {state=pending|invalid, invalidReason}
ReviewProposal (HUMAN verdict, op.actor)    → .review {state=approved|rejected, reviewedAt}
   verdict = approve | reject                    (approve re-validates the §5 boundary SERVER-SIDE — no client payload)
augurDispatchPending (fires on state="approved") + RecordProposalDispatch → .review.dispatchedAt
```

**Two gates, in order:** (1) `RecordProposal` applies a **deterministic** validity gate → `pending`
(dispatchable-eligible) or `invalid` (out-of-vocabulary / bad confidence / scope-escape — auditable, never
dispatched). (2) A `pending` proposal then waits for the **human** `ReviewProposal` verdict → `approved` or
`rejected`. Dispatch (`augurDispatchPending`) fires **only on `approved`** (confirmed in the lens spec), so the
human approve is the real gate. Unlike capability, Augur's approve re-runs its §5 boundary check **entirely
server-side from the stored aspects** — the operator's approve carries **no** validation payload, making it a
plain `/api/op` submit.

**Review states** (`review.state`): `pending` (machine-validated, awaiting human) · `invalid` (machine-blocked,
terminal) · `approved` (human-approved, will dispatch) · `rejected` (human-rejected) · dispatched (`approved` +
`dispatchedAt`). **The operator gates dispatch by approving** — and audits the `invalid` ones the machine
blocked.

---

## 2. Information architecture

### 2.1 Routes (append to the main doc's §1.1 route table)

| Route | View |
|---|---|
| `#/review` | AI review console, defaults to the `capability` tab |
| `#/review/capability` | Capability-proposal queue |
| `#/review/capability/<proposalId>` | One capability proposal — the review card (detail) |
| `#/review/augur` | Augur-escalation queue |
| `#/review/augur/<proposalId>` | One Augur escalation — the audit card (detail) |

Deep links resolve with the §1.2 key-resolver posture (no dead ends): a `<proposalId>` that no longer exists
renders "this proposal is no longer in the read model" + a link back to the queue.

### 2.2 Shell placement

A **top-nav "Review" entry** with a count badge — the number of **actionable** items, i.e. proposals in
`review.state = pending` across **both** loops (a `pending` capability proposal *and* a `pending` Augur proposal
each await a human approve/reject). The badge means "a human decision is waiting on the AI." `invalid`/
`approved`/`rejected`/dispatched/applied never count. When zero, no badge.

The map's reserved `#sysmap-console` mount (kept vacant since F1 for the shelved agent-activity console) stays
reserved — F16 is a full page, not that console. No collision.

### 2.3 Module structure (the goja logic/DOM split, per main §2.3)

- `web/js/logic/review.js` — **pure, goja-tested**: `proposalRows(bucketEntries)` (shape + sort: pending first,
  then newest `reasonedAt`), `reviewStateClass(state)`, `confidenceBand(score)` (low/med/high → color), the
  artifact-kind glyph map, and `isActionable(row)` (any `review.state="pending"`, both loops). Zero DOM.
- `web/js/views/review.js` — DOM + fetch, binds the logic-tier output to the card/queue.
- Decision logic (what's actionable, how a row sorts, how a verdict maps to a state) lives entirely in the logic
  tier so it is unit-tested without a browser — the house rule.

---

## 3. F16(a) — Capability review console (the signature)

### 3.1 The queue — `#/review/capability`

Read `capability-proposals` via a new `GET /api/review/capability` (server does `KVListKeys` + `KVGet` over the
bucket, exactly like `lens.go`'s read-model rows path — P5-clean, no Core-KV scan). One card per proposal,
**pending first**:

- **Headline:** the `intent` (plain-language request) — this is what the human reasons about, so it leads.
- **State chip:** `pending` (amber, actionable) · `approved`/`applied` (green) · `rejected` (grey) ·
  `invalid` (red, with `invalidReason` on hover) · `authoring…` (blue, in-flight — null artifact).
- **Meta line:** artifact `kind` glyph (lens ▤ / grant 🔑 / weaverTarget ◇ / loomPattern ⛓ / vertexTypeDDL ▦ /
  opMeta ⚙) · target `mode` + `packageName@newVersion` · **confidence band** (a 0.42 vs 0.98 score is real
  triage signal) · `model` (which AI authored it) · `reasonedAt` ("ago").
- **Requester:** `requesterId` as a `keyLink` → `#/graph/<key>` (no dead ends).

Empty state: "No capability proposals yet. When an AI authors a new lens, grant, or op, it lands here for your
review." (Teaches the feature.)

### 3.2 The review card — `#/review/capability/<proposalId>`

The heart. Top-to-bottom, the way a reviewer actually reads it:

1. **Intent + state** — the request in plain language, the state chip, `reasonedAt`/`reviewedAt` timeline.
2. **Rationale** — `rationale.text`, the model's own argument for why this artifact. Rendered prose.
3. **The artifact** — `artifact.kind` + `artifact.content`, syntax-presented (Cypher for a lens, JSON for a
   grant/opMeta) in a monospace panel. This is the *what will be installed*.
4. **The delta — the diff-as-confirm.** `validation.deltaPreview` rendered as the change preview (the same role
   the Packages dry-run delta plays as the install confirm, main §9.3). `validation.state` + `validation.report`
   shown alongside — the recorded verdict at author-time. ⚠️ **Note the timestamp:** this delta was computed at
   `validation.checkedAt`; the approve action re-computes a fresh one (§3.3) — the card labels this "author-time
   preview" so the operator isn't surprised when approve re-validates.
5. **Provenance** (collapsible `<details>`) — `model`, `promptHash`, `catalogHash`, `reasonedAt`. The audit trail:
   which model, against which catalog snapshot, reasoned this.
6. **The action row** (only when `review.state = pending`):
   - **Approve & install** — primary, but gated behind the re-validation + a confirm (§3.3).
   - **Reject** — destructive-styled (`.file-detach` family), typed-confirm ("type the proposalId") since it
     discards an AI's work; submits `ReviewCapabilityProposal{verdict:reject}`.
   - For non-pending states the row shows the outcome instead: "approved & applied by `<appliedByOp>` at
     `<appliedAt>`" (op key linkified) / "rejected at `<reviewedAt>`" / "invalid — `<invalidReason>`".

### 3.3 The approve path — the re-validation fork ⚠️ (F16's one real decision)

Approve is **not** a blind POST of the stored verdict. Per the DDL, the operator's approve must carry a **fresh**
`validation` payload, re-computed against the *current* catalog. Two ways to get it — **this is the fork for
Winston/Andrew:**

- **Option A — Loupe re-validates server-side (recommended).** On `POST /api/review/capability/<id>/approve`,
  the Loupe server calls `pkgmgr.ValidateCapabilityArtifact(kind, content, parser, requesterHeld,
  sensitiveAspects)` itself (Loupe already imports `internal/pkgmgr` — `cmd/loupe/pkg.go`), then stamps
  `ReviewCapabilityProposal{verdict:approve, validation:{state,report}}` via the existing op-submit path. If the
  fresh validation ≠ `valid`, the endpoint returns the failure **to the UI before submitting** — the operator
  sees "this no longer validates against the current catalog: `<report>`" and no op is sent (or they submit
  anyway and let the op fail-close to `invalid` — a product choice; recommend blocking client-side + offering an
  explicit "record as invalid" override for audit).
  **Cost:** Loupe must construct the three validator dependencies — a `CypherParser`, the operator's
  `requesterHeld []HeldPermission`, and a `SensitiveAspectResolver`. The parser Loupe can build (it's a pure
  dependency); the held-permissions + sensitive-aspect resolver need a small seam. ⚠️ **ASSUMES** those three are
  constructible in `cmd/loupe` without a cross-lane primitive — **the FE-build fire must spike this first**; if
  any needs platform help, it becomes the §6 cross-lane ask and F16(a)'s approve degrades to Option B until then.
- **Option B — a validation endpoint on the Lattice side.** A `pkgmgr`/capability-author RPC that re-validates a
  proposal by id against the live catalog and returns the fresh `{state,report}`; Loupe calls it, then submits.
  Heavier (a new platform surface) but keeps the validator's dependency-wiring on the lane that owns it.

**Recommendation: Option A**, spiked in the build fire. It keeps the whole approve loop inside Loupe + the
existing `/api/op` submit, mirrors how the Packages page already drives `pkgmgr` in-process, and needs no new
platform surface — *if* the three deps wire cleanly. The FE ships against an internal
`revalidate(proposal) → {state,report}` seam so the backend choice is swappable without touching the card UI.

**After approve → apply (the real boundary of F16).** Approve only flips `review.state=approved`; the artifact
still has to be *installed*. Apply is **not** a `/api/op` submit — it is a two-Processor-commit platform flow
(`internal/pkgmgr.CapabilityApplyPlanForProposal` → `Installer.Apply` (a real F-004 install) → then
`MarkCapabilityProposalApplied`, which verifies the target `vtx.package.<id>` is live and its manifest name
matches `target.packageName`). Today only `cmd/lattice-pkg apply-proposal <id>` drives it, needing a bootstrap
path + admin actor + direct-KV reads. So F16 has a real fork for apply:

- **Apply-in-Loupe (recommended for the closed loop):** a new **`POST /api/review/capability/<id>/apply`** that
  wraps `CapabilityApplyPlanForProposal` server-side and submits both commits, rendering the installer reply
  inline (collapsible, exactly like the Packages install reply, main §9.3). ⚠️ **ASSUMES** the apply flow's
  bootstrap/admin-actor context is constructible inside the running Loupe process — **the F16.2 fire spikes this
  alongside the approve re-validation** (same class of dependency-wiring question). This is what makes Loupe the
  *complete* human-in-the-loop surface.
- **Defer apply to the CLI (F16.2 fallback):** if that context isn't cleanly constructible, the card shows the
  `approved`-but-not-applied state + the exact `lattice-pkg apply-proposal <id>` command to run, and files the
  Loupe apply endpoint as a §6 cross-lane ask. The approve loop still ships; only the last mile waits.

**The card honors reality:** it shows an **"Apply now"** button only if the Loupe apply endpoint exists; otherwise
it shows the approved state + the CLI hand-off. No invented button.

### 3.4 F16(a) endpoints summary

| Endpoint | New? | Serves | Depends on |
|---|---|---|---|
| `GET /api/review/capability` | **new** | the proposal queue (bucket rows) | `capability-proposals` read model (shipped) |
| `GET /api/review/capability/<id>` | **new** | one proposal's full row | same |
| `POST /api/review/capability/<id>/approve` | **new** | re-validate + submit `ReviewCapabilityProposal{approve}` | Option A: `pkgmgr.ValidateCapabilityArtifact` deps ⚠️ / Option B: validation RPC |
| reject → reuse `POST /api/op` | no new endpoint | `ReviewCapabilityProposal{verdict:reject}` | op DDL (shipped) |
| `POST /api/review/capability/<id>/apply` | **new** (or defer to CLI) | wrap `CapabilityApplyPlanForProposal` → install + `MarkCapabilityProposalApplied` | ⚠️ apply context in-process (§3.3) |

All writes go through the same-origin gate + operator-login already wrapping the mux (F15). Op submits **relay to
the Gateway under the operator's own Bearer token** (the existing `op.go` path); the Gateway stamps `actor` from
the verified token, so the reviewer identity is the logged-in operator automatically — Loupe stamps no actor.
**Apply is the exception** — it is not an op relay but a two-commit `pkgmgr` flow, hence its own endpoint (or the
CLI hand-off).

---

## 4. F16(b) — Augur escalation console (human-gated, lighter approve)

The Augur tab is an **action** console like the capability tab — approve arms autonomous dispatch — but its
approve is *lighter* (server-side re-validation, plain `/api/op`, no apply step). It reuses the §3.2 card
renderer wholesale.

### 4.1 The queue — `#/review/augur`

`GET /api/review/augur` over the `augur-proposals` bucket (same read-model path). One card per escalation,
**pending first** (those await a human), then newest:

- **Headline:** the **gap** — `gapColumn` on `targetId`/`entityId`, with `trigger` (what fired the escalation:
  `unplannable` = no playbook entry, or `exhausted` = retry budget spent). This is what the AI was reacting to.
- **State chip:** `pending` (amber — **awaiting your approve/reject**) · `invalid` (red — machine-blocked
  out-of-vocabulary / bad confidence / scope-escape, `invalidReason` shown, terminal) · `approved` (green, will
  dispatch) · `rejected` (grey) · dispatched (green + `dispatchedAt`).
- **Meta line:** proposed `action` + `params` summary · confidence band · `model` · `reasonedAt`.

### 4.2 The review card — `#/review/augur/<proposalId>`

Same shared card renderer as §3.2:

1. **Gap + state** — what triggered the escalation, current verdict, timeline.
2. **Rationale** — the model's reasoning.
3. **Proposed op** — `proposed.action` (∈ `triggerLoom` / `assignTask` / `directOp`) + `proposed.params` (JSON
   panel) — **exactly what will be dispatched on approval.** This is the reviewer's decision surface.
4. **The action row** (only when `review.state = pending`):
   - **Approve & dispatch** — primary, behind a confirm ("Approving arms autonomous dispatch of this op against
     `<entityId>`."). Submits `ReviewProposal{externalRef, verdict:approve}` via `/api/op` — **no validation
     payload** (Augur re-validates server-side). No apply step; the platform dispatches on `approved`.
   - **Reject** — destructive-styled, typed-confirm; submits `ReviewProposal{verdict:reject}`.
   - For non-pending states: `invalid` shows `invalidReason` **prominent** (the audit payoff — *why the machine
     gate blocked an AI action*); `approved`/dispatched shows `dispatchedAt` + a link to the dispatched op/outcome
     if the read model carries one; `rejected` shows `reviewedAt`.
5. **Provenance** (collapsible) — model, hashes, reasonedAt.

### 4.3 First production submitter — verify live ⚠️

`ReviewProposal` is **test-envelope-only today** (`packages/augur/ddls.go`: "no production dispatcher yet") —
F16.3 is its first production caller. The read side is fully shipped; the write is unexercised in production, so
F16.3 **must verify the full approve→`augurDispatchPending`→`RecordProposalDispatch` loop end-to-end against the
running stack**, not just that the op is accepted. This is the one place F16(b) touches a cold path.

### 4.4 F16(b) endpoints summary

| Endpoint | New? | Serves | Depends on |
|---|---|---|---|
| `GET /api/review/augur` | **new** | escalation queue (bucket rows) | `augur-proposals` read model (shipped) |
| `GET /api/review/augur/<id>` | **new** | one escalation's full row | same |
| approve / reject → reuse `POST /api/op` | no new endpoint | `ReviewProposal{verdict}` (server-side re-validated) | op DDL (shipped, test-only → F16.3 is first prod use) |

---

## 5. Visual system (additive, existing tokens only)

No new CSS system — reuse the program's card, chip, `keyLink`, `.confirm-modal` (typed-confirm), `<details>`
collapsibles, and monospace artifact panels. New pieces are compositions:

- **Proposal card** — the roster/lens-card family with a state chip + confidence band (reuse the lens-freshness
  dot color ramp for the confidence band: red→amber→green over 0..1).
- **Artifact/delta panel** — the Packages install-delta panel, verbatim styling.
- **Confidence band** — a thin inline meter; the goja `confidenceBand(score)` returns the class, CSS colors it.

The one net-new visual idea is the **`invalid`-reason callout** — when a proposal was machine-blocked
(`review.state=invalid`), its `invalidReason` renders in the existing `.alert-line`-warn style so the audit
signal reads at a glance; no new component.

---

## 6. Cross-lane asks (file to `lattice.md`, `🚧 blocks` only the dependent slice)

Both are **contingent** — filed only if the F16.2 spike fails; nothing is a hard up-front dependency:

1. **(Blocks capability *approve* only, if Option A spike fails)** — a way for `cmd/loupe` to obtain the three
   `ValidateCapabilityArtifact` dependencies (CypherParser + operator `requesterHeld` + SensitiveAspectResolver),
   OR a capability-author re-validation RPC (Option B, §3.3). The CLI already does this in-process
   (`cmd/lattice/capability` `freshApprovalVerdict`), so precedent says Option A should work.
2. **(Blocks capability *apply* only, if the apply spike fails)** — a Loupe-constructible path into
   `pkgmgr.CapabilityApplyPlanForProposal` + its bootstrap/admin context, OR accept the CLI hand-off fallback
   (§3.3). *Queue + detail + reject + all of the Augur tab do not depend on either ask.*

Everything else F16 needs is **already shipped**: both read-model buckets exist and are P5-readable, all review
ops exist (Augur's is test-only → F16.3 is first prod use), and Loupe's op-submit + login + same-origin gates are
in place. **F16.1 (capability see + reject) and F16.3 (the whole Augur tab) are buildable today** with zero
cross-lane dependency; only F16.2's approve + apply carry the two contingent spikes.

---

## 7. Fire decomposition (appends to the program's §14 table)

Sized so each fire is one green, independently valuable increment (the "fewer, larger fires" rule, but split at
the honest capability/augur seam):

- **F16.1 — Capability review: see + reject.** Route + shell "Review" nav + badge · `#/review/capability` queue
  + detail card (all fields, diff-as-confirm render) · **reject** via `/api/op` · `logic/review.js` +
  goja tests. Ships the whole read + the safe half of the action loop. **No cross-lane dep.**
- **F16.2 — Capability review: approve & apply.** The §3.3 re-validation approve path (spike Option A first) +
  the apply step (spike the Loupe apply endpoint; CLI hand-off fallback). Closes the capability human-in-the-loop.
  Carries both contingent §6 asks.
- **F16.3 — Augur review tab.** `#/review/augur` queue + review card + **approve/reject** via `/api/op` (server-
  side re-validated, no apply step). Shares the F16.1 card renderer. **No cross-lane dep**, but is `ReviewProposal`'s
  first production use — **must verify the approve→dispatch loop live** (§4.3).

Suggested order: **F16.1 → F16.3 → F16.2** (ship the two zero-dependency fires first — F16.3's approve is
actually *simpler* than F16.2's; land the heavy capability approve/apply fork last, after its spikes). Each fire
retires nothing — F16 is purely additive.

## 8. Open questions for Andrew

1. **Capability approve re-validation (§3.3):** Option A (Loupe re-validates in-process — recommended, matches
   the CLI) vs Option B (a Lattice validation RPC)? F16.2 spikes A first; this confirms the preference.
2. **Capability apply (§3.3):** apply-in-Loupe (a new endpoint wrapping `CapabilityApplyPlanForProposal` —
   recommended for the complete closed loop) vs CLI hand-off (`lattice-pkg apply-proposal`)? F16.2 spikes the
   endpoint; the fallback is graceful.
3. **Reject friction:** typed-confirm on reject (it discards an AI's authored work) — right, or too heavy?
   (Approve keeps its confirm regardless — it changes / dispatches against the running system.)
4. **Augur confidence floor:** should the queue visually de-emphasize (or fold) very-low-confidence `pending`
   Augur proposals so an operator's attention goes to the credible ones first? (A pure display choice — the data's
   there in `confidence`.)

---

*Two AI-proposal loops, one console. Both are moments where a human says yes or no to the platform changing
itself — the capability tab gates a new artifact being *installed*, the Augur tab gates a remediation being
*dispatched*. The design keeps them under one roof and one card, and is honest that the capability approve is the
heavy one (re-validate, then apply) while the Augur approve is light (approve, and the platform runs with it).*
