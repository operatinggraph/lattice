# Contracts slimming — review + proposal

> **Status:** ✅ **EXECUTED 2026-08-08** — Andrew ratified the plan and pre-authorized the slice
> commits (review post-merge). All 16 files landed across `44f5af38..f425d1e9`; the corpus went
> **391 KB → 286 KB (−27%)** with every danger-flagged rule promoted before its surroundings moved,
> and the stale-false passages corrected against shipped code. The dispositions below are the record
> of what moved where. Review provenance: three read-only reviewers, 2026-08-08.

## 0. The test applied — the corpus's own rule

`docs/contracts/README.md:23` already states the norm: *"Architecture explains rationale; PRD explains
requirements; these contracts bind them to concrete shapes."* Every disposition below is measured against
that line: a block is NORMATIVE only if an implementer would build differently without it. The README
itself violates its own frontmatter (claims 7 contracts complete; 11 exist across 15 files) and should be
fixed in the same campaign.

**Headline:** roughly **a third of the corpus can go** with zero normative loss — after 22 danger-flagged
rules (normative MUSTs currently living inside movable prose) are promoted to body clauses first.

| Group | Now | After proposal | Δ |
|---|---|---|---|
| 01–05 (addressing, op envelope, mutation batch, idempotency, health) | 1,474 lines | ~1,017 | **−31%** |
| 06–09 + README (capability, bootstrap, package install, claim flow) | 1,237 lines | ~845 | **−32%** |
| 10-* + 11 (orchestration shards, external authn) | 163.9 KB | ~99.2 KB | **−39%** (−64.7 KB) |

Two files are already the target shape and should be held up as the standard: **09-identity-claim-flow**
(after one move) and **11-external-actor-authn** (leave alone).

## 1. The five disease classes

1. **Story-scoped "Implementation Notes"** ("For the AI agent implementing Story N.x") in 01 §1.9, 02
   §2.9, 03 §3.8, 04 §4.5, 05 §5.8, 06 §6.13, 07 §7.7 — ~160 lines total. All describe shipped code;
   several are now **factually wrong** (see §3). Delete the class, after rescuing the danger-flagged
   rules inside it.
2. **Transitional/amendment narration left beside its replacement** — dated "(Transitional…)" blobs,
   "(REMOVED)" invariants kept in place, RETIRED mechanisms narrated up to four times (`nudge`,
   `weaver-claims`), "Corrected YYYY-MM-DD" blockquotes whose correction is already folded into the body.
   Delete; keep at most a one-clause dated ratification marker.
3. **Rationale walls** — why-chosen essays, worked tutorials, sizing derivations, alternatives-considered.
   Move to the owning `docs/components/*.md` / `docs/decisions/*` / `docs/hello-lattice.md` (§5
   destinations). The extreme case: **10-orchestration-surfaces is 93% revision-history table (29.9 KB
   of deliberation narration) — 18% of the entire 10-*/11 group in one table.**
4. **Duplication** — within a file (substrate §10.1 vs §10.7 tell the capability-lens extraction twice),
   across siblings (the `augur` block is written out three times — **the third copy, in
   `docs/components/augur.md`, has already drifted**), and contracts↔docs (refractor.md carries Contract
   #6 §6.14's RLS enforcement nearly verbatim and *more completely* than the contract).
5. **Mega-lines** — single logical lines of 600–1,700 chars packing rule + rationale + history (01:7,
   02:15, 03:4, 05:1). Compress to the rule; the line count understates these badly.

## 2. DANGER FLAGS — promote these rules BEFORE any cut (22)

Each is a normative rule whose **only record** sits inside a block otherwise classified movable/deletable.
The promotion diffs must land (as their own reviewable slice) before the surrounding prose moves.

| # | Contract · location | The buried rule |
|---|---|---|
| D1 | 06 §6.1 L77–92 | Step-3 key derivation: system-actor **two-key union** (`cap.<actor>` ∪ `cap.roles.<actor>`, deny-closed); ordinary actor = exactly one key; absent rbac-domain ⇒ degrade to `cap.<actor>` |
| D2 | 07 §7.7 L177 | Root capability = **graph topology, never class-based special-casing** (cited by 06 §6.1) |
| D3 | 09 §9.1 L28–29 | No `secret.mint()` builtin, no `OneTimeSecret` reply field — Processor stays generic |
| D4 | 06 §6.14 L604–618 | RLS membership join = NanoID-to-NanoID, **no `anchor_type` concat**; `anchorType` audit-only |
| D5 | 06 §6.14 L714–719 | Protected-by-default: public **only** by explicit `public: true`, never by omission |
| D6 | 06 §6.1 L94–100 | Privileged-lane allowlist rule (honored iff `{operationType, lane}` allowlisted; else stripped + `PrivilegedLaneGrantRejected`; anchor doc-level lanes exempt) — sits beside stale Fire-2 text |
| D7 | 01 §1.9 L356 | Substrate tests MUST include NanoID collision-rate validation against published alphabet/length |
| D8 | 01 §1.1 L25 | Links carry **no `direction` field** — direction is encoded by segment order only |
| D9 | 01 §1.6 L180 + 03 §3.10 | ~~REFUSED AT INSTALL~~ **Resolved 2026-08-08 in the pending-proposal refresh**: the install gate is lifted (`internal/pkgmgr/custodyscope.go` — class-key destruction now reaches read models), so the transitional clauses were deleted, not promoted; #03 keeps the one surviving rule (egress carries identity-held records only) as a normative sentence |
| D10 | 02 §2.9 L415 | Envelope JSON parsing is **lenient on unknown fields** — the wire forward-compat guarantee |
| D11 | 02 §2.2 L43 | operationType→class index built from **vertexType DDLs only** + global ambiguity guard drops multi-admitted ops |
| D12 | 05 §5.4 L97–105 | The **Vault** health group (`backend` field semantics: local `ShredKey` = deny-list refusal vs KMS key destruction) — schema doc has **no Vault section at all**; move, never delete |
| D13 | weaver L396–400 | Terminal-leg rule: gap-closing op MUST declare a `pre` entailing the remainder of the goal, mirrored in the op's own write guard; an op MUST NOT rely on the planner for write-safety |
| D14 | substrate L327–332 | FR58: bridge result-op `requestId` MUST be `deterministic(idempotencyKey = instanceKey)` — buried in the RETIRED `weaver-claims` obituary |
| D15 | loom L89–91 | `subjectKey` MUST be 3-segment `vtx.<type>.<id>`; engine **drops the trigger** otherwise |
| D16 | loom L157–172 | Bridge give-up timeout posts terminal `failed` `replyOp` — a never-answered call **converges** (if moved to bridge.md, it lands as a MUST) |
| D17 | weaver L70–81 | `directOp` default retry budget = **3 dispatches**, then `GapBudgetExhausted` |
| D18 | weaver L261–268 | Liveness invariant: discharged / excluded / escalated — keep verbatim |
| D19 | substrate L248–257 | `assignTask`→`CreateTask` uses `optionalReads` (never `reads`); revive uses `expectedRevision` (blind `CreateOnly` = RevisionConflict forever) |
| D20 | loom L296–300, L333–339 | Missing-completion-consumer is a **load-time warn, not reject** — keep one full statement when folding the duplicate |
| D21 | augur L39–43 | Install-time validation: `escalate ∈ {unplannable, exhausted}`; `autoApply.actions ⊆ {triggerLoom, assignTask, directOp}`; single-NATS-token strings |
| D22 | 11 §11.3 L106–109 | Multi-`iss`-form: MUST declare one; other form **rejected**; issuer check exact-match |

## 3. STALE-FALSE — contract contradicts shipped code (reconcile-then-cut, not pure cut)

| Where | Claim | Reality |
|---|---|---|
| 06 §6.1 L61–65 | "no `service-location` package… key space registered-but-empty" | `packages/service-location/lenses.go:32` ships `capabilityServiceAccess` |
| 06 §6.1 L101–106 (+ §6.4 L242) | privileged-lane allowlist "Fire 2, not yet built… inert" | `step3_auth_capability.go:412–473` shipped, tested |
| 06 §6.13 L448 | `LATTICE_AUTH_STUB=allow-all` guidance | `cmd/processor/main.go:85` **refuses** stub auth |
| 05 §5.8 L169 | Refractor health key `health.refractor.refr-<NanoID>` | shipped prefix is **`rfx-`** (`lattice_heartbeater.go:585`) |
| 03 §3.8 L153/L165 | cites `StarlarkExecutionFailed: InvalidReturnShape`, `MetaLaneCollision` | Contract #2 §2.6 retired both codes — contract contradicts contract |
| 07 §7.7 L171–176 | match `role.canonicalName == "systemRoot"`; `grantedBy` walk | only primordial role is `operator`; the walk was deleted from core by Epic 12 |
| 07 §7.5 | single-pass `make up` sequence | bootstrap.md documents the two-invocation `-skip-ready-wait` flow |
| substrate L196–200 | "extend `enableAtomicPublish` to `loom-state`" (build instruction) | done — `primordial.go:127` |
| substrate L399–401 | "`core-schedules` is NEW… does not exist yet" | exists — `primordial.go:67` |
| weaver L19 | "`weaver-targets` is NEW — joins the create list" | exists — `primordial.go:28` |
| 02 L53, L387 | cites `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` | file does not exist (dangling ref) |
| docs side | `docs/components/augur.md:127` shows `"pattern"` in the augur block; `docs/components/weaver.md:880` says mark `claimId` "always empty" | contract + code disagree — the **docs** are stale here; fix docs, keep contract |

## 4. Biggest single cuts (by value)

1. **surfaces revision-history table** (L43–67, 29.9 KB) → `docs/decisions/contract-10-revision-history.md`,
   leave a ~10-line date/§ index. All 25 rows verified duplicated-or-dead. **44% of the 10-* reduction in
   one zero-risk move.**
2. **06 §6.14 Enforcement** (55 lines) → already ~95% in `docs/components/refractor.md:189–288`; keep ~12
   normative lines (protected ⇒ Postgres+FORCE-RLS; absent posture fail-closed; `'*'` anchor; fold in
   refractor.md's `is_deleted` correction).
3. **06 §6.1 decomposition narration** (45 lines) → `docs/decisions/projection-plane-decomposition.md`
   (already carries it); extract D1 first.
4. **01 §1.8 worked examples** (80 lines) + **01 §1.7 duplicate DDL examples** (38→12) + **03 §3.6
   Starlark walkthrough** (32→8) → `docs/hello-lattice.md` or delete.
5. **weaver planner-bullet rationale** (~4.5 KB of 9.2) → `weaver.md` + planner-mandate design doc.
6. **substrate `weaver-claims` RETIRED + `weaver-work` deferred** (~3.8 KB) — bucket verified gone from
   code; promote D14 first.
7. **substrate §10.7** collapsed into §10.1 (same-file duplicate, ~1.9 KB).
8. **The story-notes class** (§1 item 1, ~160 lines across seven files).
9. **04 §4.4 timeline + §4.5 notes** (40→3 lines; −46% of the file).
10. **05 §5.4 metric lists** → `docs/observability/health-kv-schema.md` (which claims canonical status);
    **Vault block moves, never deleted** (D12).

## 5. Move destinations

Existing: `docs/components/{processor,refractor,bootstrap,substrate,weaver,loom,bridge,augur,vault,
gateway,scheduling}.md` · `docs/observability/health-kv-schema.md` · `docs/hello-lattice.md` ·
`docs/decisions/projection-plane-decomposition.md` · `docs/vendors.md` (NATS pin — make it the single
owner; 03 §3.9.1 and 04 §4.3 currently cross-cite it circularly) ·
`_bmad-output/planning-artifacts/lattice-architecture.md` (permissive-by-default why) ·
`_bmad-output/implementation-artifacts/weaver-planner-mandate-design.md`.

New (2): `docs/decisions/contract-10-revision-history.md` · `docs/decisions/identity-claim-secret-option-c.md`.
(Optional third: `docs/decisions/contract-10-design-rationale.md` for why-paragraphs with no component home;
`docs/components/service-location.md` stub for 06 §6.10's acceptance criteria.)

Net new prose in `/docs` is modest (~90 lines for group 06–09) because most moved material already exists
downstream — the moves are mostly **deletes with a pointer check**.

## 6. Structural fixes to take in passing

- README: fix frontmatter (7→11), delete L50–54 (verbatim dup of L46) and L56–59 (a backlog, not an
  index; first item duplicates 06 §6.11 and cites a resolved ADR-51 state). **Promote L23 into an
  explicit numbered authoring rule** ("A contract states the shape and the rule — not why it was chosen,
  what was considered, when it landed, or which story built it") — the cheap re-accretion prevention.
- 02: sections physically out of order (2.4, 2.7, 2.5, 2.5.1, 2.6, 2.8) — reorder.
- 08: §8.5 does not exist (§8.4 → §8.6) — resolve the numbering.
- augur shard: unwrap the blockquote walls into normative bullets; fix the broken "shapes below" ref.
- 03: revision-history table (only 03 and surfaces carry one) → one-line ratification markers.

## 7. Execution plan (each slice = uncommitted contract diff; Andrew commits)

0. **Prerequisite:** resolve the three pending dirty proposals (01/03/10 — retention narrowings +
   StepSpec.Reads) first; slimming touches the same files and must not entangle with them. Note the
   Loom `reads`/`optionalReads` amendment is **already implemented** in code (`internal/loom/pattern.go`,
   `actuator.go:134`) — the pending text is catch-up, not gating.
1. **Slice 1 — the revision-history move** (cut #4.1). One file, zero normative risk, 28.3 KB. Proves the
   pattern cheaply.
2. **Slice 2 — promote the 22 danger-flagged rules** into body clauses. Pure additions/relocations, no
   deletions in the same diff.
3. **Slice 3 — the verified-stale sweep** (§3 rows + §1 class 2): pure deletions, each independently
   checkable against code. Includes killing the story-notes class.
4. **Slices 4+ — consolidations**, one contract file per session (06, then substrate, weaver, loom, 01,
   02, 03/04/05, 07/08/09), reconcile-then-cut where §3 flagged the docs side as newer.
5. **Hold up 09 and 11 as the target shape**; leave 11 alone.

Full per-file line-ref dispositions live in the three reviewer reports from the 2026-08-08 session; this
doc carries every load-bearing ref (danger flags, stale-false rows, top cuts) needed to execute without
re-deriving.
