---
title: Story 2.2 Implementation Handoff Brief
story: 2.2 — Functional Gap Analysis (Epic 2 Closing Story)
model_tier: Opus (locked)
token_budget: ~130K
session: Fresh implementation session
architecture_lead: Winston
date: 2026-05-15
---

# Story 2.2 — Functional Gap Analysis: Handoff Brief

## Your Role

You produce **`_bmad-output/planning-artifacts/refractor-gap-analysis.md`** — the formal Epic 2 exit artifact. This is a written analysis document. **You do NOT modify production code, modify tests, or change behavior.** Pure analytical work: inventory what the morphed Refractor actually does today, compare it to Epic 3 + Phase 2+ requirements, consolidate deviations, register risks. Every Epic 3 story must have an unambiguous "prerequisite from gap analysis" reference once you're done.

## 🔴 MANDATORY OPERATING RULES (READ FIRST)

**Pattern across Phase 1: sub-agents have self-reported tokens 30-50% under outer telemetry.** Story 2.1+2.1b combined came in at 371K vs 145K budget (37% under-reporting). This story is analysis-heavy, but the brief is tight.

- **At every checkpoint (every 8-10 tool calls OR after any deliverable section OR after any file read >25KB):** send a "checkpoint message" with sections completed, sections remaining, honest token estimate (lower bound, rounded UP).
- **Halt unconditionally if you estimate > 135K used** (5% over budget).

Other rules:
- **Model tier:** Opus only. Halt if Sonnet/Haiku.
- **No PRs.** Direct commit to `main` after Winston review.
- **No code edits.** This is an analysis story. If you spot a bug worth fixing, note it in the risk register and STOP — do not fix it here. Story 2.3 (or an Epic 3 story) can land the fix.
- **DO NOT silently edit planning artifacts other than `refractor-gap-analysis.md` and the Phase 2+ deferred items section of `epics.md`** (the AC explicitly permits the latter; see Deliverable #2 below).
- **No git commits by you.** Winston + Andrew commit.
- **Token tracker:** update Row 2.2 at session close — HONEST estimate, round UP.

## Story Scope (from `epics.md` lines 653-691 — authoritative)

> As a platform architect, I want a written gap analysis comparing the morphed Refractor's actual capabilities to Lattice's full Phase 1 and Phase 2 requirements, so that Epic 3 prerequisites and the Phase 2+ backlog are grounded in measured reality rather than pre-morph predictions.

**Recommended model tier:** Opus
**Estimated token budget:** ~130K

### Acceptance Criteria (verbatim distilled — read epics.md lines 661-691 for full text)

The deliverable `_bmad-output/planning-artifacts/refractor-gap-analysis.md` MUST contain:

1. **Capabilities as-shipped** — precise inventory of morphed Refractor today: rule/expression language supported by the current parser, target adapters implemented (NATS KV, Postgres), Lens lifecycle ops (activate/deactivate/modify with re-materialization), control surface endpoints, observability surface (Health KV keys emitted, metrics types, alert flags), failure tier handling behaviors.

2. **Required for Epic 3 (Authorization & Security Perimeter)** — each item marked **GAP / PARTIAL / READY** against current state:
   - openCypher parser integration (expected GAP)
   - Capability Lens cypher rule semantics (role-based, task-derived, manager-via-reporting-chain, service-access topology)
   - Capability KV target adapter — does existing `nats-kv` adapter cover Contract #6's three-section model (`platformPermissions`, `serviceAccess`, `ephemeralGrants`)?
   - Read-after-write coherence for Capability KV (Processor reads it at step 3 — does the projection commit pattern guarantee sub-500ms lag at expected load?)

3. **Required for Phase 2+** — each marked GAP/PARTIAL/READY:
   - Historical state query support (FR51, deferred from MVP)
   - Read-path authorization for Lens targets (Postgres RLS or Gateway proxy)
   - ES target adapter
   - NATS streams target adapter
   - Multi-cell scale-out adjustments
   - Anything else surfaced during 2.1 / 2.1b execution

4. **Deviations from morph plan** — consolidated from `MORPH-DEVIATIONS.md` (15 entries from 2.1 + 2.1b), each with: morph plan section, actual decision, driving constraint, downstream implication. Mark which are RESOLVED vs OPEN.

5. **Risk register** — known-fragile areas in morphed Refractor to harden before Phase 2: concurrent Lens modification, target adapter failure cascades, Adjacency KV consistency under crash recovery, anything else surfaced during your inventory work.

### Secondary deliverable (per AC's second `Given/Then`)

After the gap analysis is complete, the Phase 2+ deferred items section in `epics.md` is updated to include any new gaps surfaced during the analysis. This is a small, scoped edit — append to the existing deferred-items list, don't reorganize.

## Required Context — Read These Only

| File | Why |
|---|---|
| `_bmad-output/planning-artifacts/MORPH-DEVIATIONS.md` | THE primary input. 15 deviations from 2.1+2.1b — most should be referenceable in Section 4 of the analysis with light reformatting. |
| `cmd/refractor/CONTRACT-AMENDMENT-REQUEST.md` | 3 amendment requests from 2.1, all resolved in 2.1b. Cross-reference in Section 4 if relevant. |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #6 (Capability KV) | Section 2.2.3 of gap analysis: assess whether nats-kv adapter covers the three-section model + read-after-write requirements. |
| `_bmad-output/planning-artifacts/epics.md` Story 3.1 + 3.2 + 3.3 (lines 717-820 — Story 3.1 starts at 717; you can skim forward through 3.2 and 3.3) | Authoritative list of what Epic 3 needs from Refractor. Source of truth for the Section 2 items. |
| `_bmad-output/planning-artifacts/epics.md` "Deferred Items" or equivalent Phase 2+ section (search for "Phase 2+" or "deferred") | Where Deliverable #2 (the secondary edit) lands. |
| `internal/refractor/` directory listing (`ls -la internal/refractor/`) + each package's top-of-file doc comments | Section 1 inventory: capabilities as-shipped. Do NOT read implementation bodies unless you need to settle a specific GAP/PARTIAL/READY call. |
| `internal/refractor/lens/translator.go` + `corekv_source.go` + `bootstrap.go` | Section 1 — Lens lifecycle behavior. |
| `internal/refractor/adapter/postgres.go` + `natskv.go` (skim for capability surface, not exhaustively) | Section 1 — target adapter capability inventory. |
| `internal/refractor/control/` (skim for endpoint list) | Section 1 — control surface inventory. |
| `internal/refractor/health/` (skim for emitted Health KV keys) | Section 1 — observability surface. |
| `internal/refractor/failure/` (read full — small package) | Section 1 — failure tier behaviors. |
| `docs/refractor-failure-tiers.md` | Section 1 supplement. |
| `internal/refractor/refractor_e2e_test.go` | The AC #10 test from 2.1b — note its perf numbers (p99=10.3ms) in the risk register or capabilities section as an empirical anchor. |

**DO NOT read** Materializer's source (the morph plan + your morphed code is the truth). DO NOT read deep implementation files — top-of-file doc comments + function signatures are sufficient.

## Architectural Decisions Already Made (Winston)

1. **Output path:** `_bmad-output/planning-artifacts/refractor-gap-analysis.md`. This is a PLANNING artifact (research/decision), not an implementation artifact.

2. **Document structure:** the AC's 5 numbered sections become the document's H2 sections. Use clean headers:
   ```
   # Refractor Gap Analysis (Epic 2 Closing Artifact)
   ## 1. Capabilities As-Shipped
   ## 2. Required for Epic 3 (Authorization & Security Perimeter)
   ## 3. Required for Phase 2+
   ## 4. Deviations from Morph Plan
   ## 5. Risk Register
   ## Appendix A: Epic 3 Story Prerequisite Mapping
   ```
   Appendix A (NEW — Winston-mandated) is described in Decision #6 below.

3. **GAP/PARTIAL/READY labels are binding.** Use them consistently. Definitions:
   - **READY** = exists today in morphed Refractor; meets requirement without modification
   - **PARTIAL** = exists today but requires extension/modification to fully meet requirement; note the size of the extension
   - **GAP** = does not exist; new work required; note approximate scope (story-sized vs. epic-sized vs. multi-epic)

4. **Don't pad the inventory.** Section 1 is precise, not exhaustive. If a capability is obvious from the file structure (e.g., "Refractor has a Postgres adapter"), one sentence suffices. Spend depth where the inventory genuinely informs the GAP/PARTIAL/READY calls in Sections 2 + 3.

5. **Source MORPH-DEVIATIONS.md verbatim where it serves.** Section 4 should reformat the existing 15 deviations, NOT rewrite them. Add a status column (RESOLVED in 2.1b / OPEN / OPEN-deferred-to-2.2). Cross-link to file paths or PRs where applicable.

6. **Appendix A — Epic 3 Story Prerequisite Mapping (REQUIRED, my addition):** the AC's second Given/Then says "each Epic 3 story has a clear 'prerequisite from gap analysis' reference (zero ambiguity about what Epic 3 needs Refractor to gain before its own work proceeds)." Don't bury this in the body — produce an explicit two-column table in an appendix:
   ```
   | Epic 3 Story | Prerequisite from Gap Analysis |
   |---|---|
   | 3.1 openCypher full engine | §2.1 (parser GAP — explicit work item) |
   | 3.2 Capability Lens activation | §2.2 + §2.3 (Capability KV adapter PARTIAL/GAP) |
   | 3.3 Step 3 auth | §2.4 (read-after-write coherence; §1.6 lag empirics from 2.1b e2e) |
   | 3.4 Structured denial | ... |
   | 3.5 Three-plane traceability | ... |
   | 3.6 Role-scoped access + audit | ... |
   | 3.7 Capability Lens adversarial suite | ... |
   ```
   Each entry should be ONE-LINE specific: a section reference and the GAP/PARTIAL/READY shorthand. If a story has no Refractor prerequisite, write "No Refractor prerequisite" — explicit absence is better than silence.

7. **Length target:** 1,200-2,500 words (analysis doc, not encyclopedia). The MORPH-DEVIATIONS.md content carries weight in Section 4 — keep prose tight elsewhere.

8. **Phase 2+ section of epics.md edit (Deliverable #2):** find the existing deferred-items list; append any net-new gaps surfaced in Section 3. Don't reorganize. Don't rephrase existing entries. Smallest possible edit. Log in the closing summary which entries you added.

9. **Empirical anchors:** the AC #10 e2e from 2.1b proved p99=10.3ms vs 500ms NFR-P3 budget (46× headroom). This empirical fact belongs in Section 1 (observability surface, perf characteristics) AND in Section 2.4 (read-after-write coherence — the e2e measurement is direct evidence the projection commit pattern is fast enough under trivial-Lens load). State that the e2e measured one-Lens-one-Postgres-target — it does NOT generalize to Capability KV write load until tested explicitly.

10. **Open carries from 2.1+2.1b (most important deviations to spotlight in Section 4):**
    - **Deviation 13 (OPEN, highest priority):** pipeline still parses legacy Materializer `node_<label>_<id>` key shape; Lattice `vtx.<type>.<id>` adaptation deferred. **This blocks projecting any domain entity beyond meta-lenses.** Almost certainly a Phase 2 production-readiness blocker — flag it specifically in Section 3 + Section 5 (risk register) + Appendix A as a prerequisite for Story 3.1 or 3.2.
    - **Deviation 5 (RESOLVED with caveat):** substrate refactor of inner refractor packages deferred (30 files exceeded the scope guard). Section 5 risk: cross-cutting refactor risk if Phase 2 needs e.g. centralized observability hooks.
    - **Deliverable #12 from 2.1 (OPEN):** adapter document-envelope reshape deferred (would require adapter interface signature change). Section 3 item.
    - **Personal Lens NATS-subject adapter, Path Projection / RLS, Crypto-shred listener, Secure Lens** (morph plan Phase 5 — explicit Phase 2+ items per Section 3).
    - **openCypher parser expansion** (morph plan Phase 4 — Section 2.1 GAP, owned by Story 3.1).

11. **Risk register honesty:** if you discover a real fragility while reading the code (e.g., unprotected map access, missing context cancellation in a watch loop), name it specifically with file:line refs. Don't hedge. The risk register is the primary feedback channel from analysis to future hardening stories; vague risks are useless.

12. **NO new Contract amendments expected.** If you discover one, append to `cmd/refractor/CONTRACT-AMENDMENT-REQUEST.md` and escalate before resolving. But this is unlikely in an analysis story.

## Deliverables Checklist

1. ✅ `_bmad-output/planning-artifacts/refractor-gap-analysis.md` produced, structured per Decision #2, with all 5 AC-mandated sections + Appendix A (Epic 3 Story Prerequisite Mapping)
2. ✅ Section 1 inventory accurately reflects morphed Refractor (parser, adapters, lens lifecycle, control surface, observability, failure tiers)
3. ✅ Sections 2 + 3 use GAP/PARTIAL/READY labels per Decision #3, with scope notes where helpful
4. ✅ Section 4 reformats all 15 MORPH-DEVIATIONS.md entries with RESOLVED/OPEN status
5. ✅ Section 5 risk register names specific fragilities with file:line refs where applicable
6. ✅ Appendix A: per-Epic-3-story prerequisite table (7 stories: 3.1 through 3.7)
7. ✅ `epics.md` Phase 2+ deferred items section updated with any net-new gaps surfaced (Deliverable #2 of the AC)
8. ✅ Token tracker Row 2.2 updated — HONEST estimate, round UP
9. ✅ Closing summary listing: sections present, # of GAP/PARTIAL/READY items by section, # of risks in register, list of epics.md additions, token estimate (honest)

## What Story 2.2 Is NOT

- **Not** any code change in `internal/refractor/`, `cmd/refractor/`, or anywhere else
- **Not** any test addition or modification
- **Not** new Lens definitions, new adapter implementations, parser work, etc.
- **Not** a fix for Deviation 13 (the legacy key parser) — analyze it, don't repair it
- **Not** a re-derivation of the architecture; consume the existing artifacts

## Escalation

Halt and escalate via Andrew if:
- Any AC section cannot be filled because a required input is missing (e.g., Contract #6 doesn't actually describe the three-section model in the depth Section 2.3 requires)
- You uncover a defect in the morphed Refractor that you believe blocks Epic 3 from starting at all (not just one Epic 3 story)
- Token estimate exceeds 135K
- A CONTRACT-AMENDMENT-REQUEST emerges

## Closing

1. Verify all 9 deliverables
2. Re-read the produced gap analysis: does Section 5 have ≥3 substantive risks? Does Appendix A cover all 7 Epic 3 stories explicitly? Does Section 4 cover all 15 deviations?
3. Update token tracker Row 2.2 — round UP
4. Closing summary as Deliverable #9 above
5. Flag: this is Epic 2's exit artifact — Andrew + Winston will review the document itself, not just the metadata

Do NOT commit. Winston + Andrew review and commit.
