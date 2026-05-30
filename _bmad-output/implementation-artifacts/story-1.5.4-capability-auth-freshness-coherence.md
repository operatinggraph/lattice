# Story 1.5.4 — Capability auth freshness coherence (B4)

**Phase 1.5 (Hardening Block) · Wave A**
**Tier:** Opus
**Author:** Winston · **Date:** 2026-05-29
**Sources:** Refractor CR **F-009** + **GAP-001**, Gate 5 **B4**
**Andrew's architectural decisions (this session, binding):** see §3.0.

---

## 🔴 MANDATORY OPERATING RULES (READ FIRST)

1. **Work in the repo root** `/Users/andrewsolgan/Documents/GitHub/Lattice`. No worktrees.
2. **Do NOT commit or push.** Leave changes in the working tree for Winston.
3. **Do NOT edit planning artifacts** (`_bmad-output/planning-artifacts/{prd.md,epics.md,lattice-architecture.md,MORPH-DEVIATIONS.md}`). Winston owns the NFR-S7 + Gate-3 AC edits (§3.4). If you believe an AC needs changing, note it in `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` and continue. Component/contract docs under `docs/` ARE yours to edit.
4. **No history comments** (`// Story 1.5.4`, `// removed`, `// was`). Comments describe current behavior only.
5. **Halt and escalate** (append to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md`) on any stuck loop: re-attempting the same op after 3+ failures, immediate reverts, re-reading the same files for an absent answer, cycling between two approaches, or an unresolved test failure after 2 genuine debug attempts. Token budget tracked, NOT enforced.
6. **SURVEY FIRST.** Before editing, grep the full blast radius (§2) and write yourself the removal/keep map. This story removes a cross-cutting machinery; a partial removal that leaves dangling refs will fail to build. Confirm the survey matches §3 before cutting.
7. **Append a closing summary** to §7 of THIS file when done.

---

## 1. Goal & rationale

The Processor's Capability-KV **freshness gate** (`checkFreshness`: deny when `now − projectedAt > 2500ms`) causes false denials for the common case — an actor whose graph is unchanged is never reprojected, so `projectedAt` ages past the ceiling and valid operations get `AuthFreshnessExceeded` (the Hello-Lattice M4 failure / Gate-5 B4). The gate also makes `projectedAt` wall-clock, so replay/rebuild churns it (F-009).

**Decision (Andrew, this session): remove the per-operation freshness gate entirely** and make `projectedAt` deterministic provenance. Freshness is a property of the *projector*, not of each doc; per-doc reprojection of quiet actors is write-amplification measuring the wrong thing. The bounded staleness window is **accepted** (brainstorming #111 option c, made explicit), backstopped operationally (Refractor Capability-Lens health) and, in future, by Gateway JWT/token revocation. This matches the brainstorming intent (#91 heartbeat watermark; #111(a); architecture L90 "Refractor health is the system-level liveness indicator") far better than a per-doc wall-clock age.

## 2. Required context — SURVEY these before cutting

Freshness machinery to REMOVE (survey every ref):
- `internal/processor/step3_auth_capability.go` — `checkFreshness` (def + the call ~L198), config fields `StaleCeiling` and the staleness use of `NFRP3`, `staleness *latencyRing`, `stalenessExceedingNFRP3`, `StalenessStats()`, `StalenessExceedingNFRP3()`, the `auth-freshness-exceeded` alert emit.
- `internal/processor/health.go` (~L107, ~L174, ~L201–215) — `AttachCapabilityAuthorizer` staleness wiring + the `cap-staleness` Health-KV signal emission (`health.processor.<instance>.cap-staleness`).
- `internal/processor/health_alerts.go` — the `auth-freshness-exceeded` alert case.
- `internal/processor/envelope.go` (~L83–87) — `ErrCodeAuthFreshnessExceeded`.
- `internal/processor/step3_denial_response.go` (~L93–167) — the `AuthFreshnessExceeded` denial-response branches.
- `internal/aiagent/traversal.go` (~L132–135) — the comment asserting the AI agent relies on the Processor rejecting stale docs with `AuthFreshnessExceeded` (update the dependency note; the agent must no longer rely on that).
- Tests: `internal/processor/step3_auth_capability_test.go`, `step3_auth_trace_test.go`, `step3_denial_response_test.go`, `internal/bypass/capadv_projection_lag_test.go` (Gate-3 **Vector #2**), `internal/bypass/gate3_test.go` (vector list/report row).

Deterministic projectedAt:
- `internal/refractor/pipeline/evaluate.go` (~L82, ~L94 simple path; ~L110–123 full path) — where `projectedAt = time.Now()` today; `nodeProps` (the actor vertex's Core KV body) is available and carries provenance.
- Contract #1 §1.3 provenance fields (the source doc's `committedAt`).

Docs: `docs/components/{processor,refractor}.md`, `docs/contracts/05-health-kv.md` (cap-staleness signal removal), `docs/contracts/06-capability-kv.md` (projectedAt semantics).

Do NOT read large planning artifacts.

## 3. Design decisions (LOCKED by Winston)

### 3.0 Two time-checks — REMOVE one, KEEP the other
- **REMOVE:** the `projectedAt`-based projection-freshness gate (all of §2's "machinery to REMOVE").
- **KEEP, untouched:** `ephemeralGrants[].expiresAt` expiry (`step3_auth_capability.go` ~L262–271) — a real, intentional grant TTL, unrelated to projection lag. Also KEEP: the cap-doc read + `NoCapabilityEntry` denial (a missing entry still denies — fail-safe), the Authorize **latency** ring (perf metric), and the auth-trace recording of `projectedAt` (now provenance).

### 3.1 Remove the freshness denial + its now-meaningless metric
- Delete `checkFreshness` and its call. The authorizer no longer has any projection-age concept. An over-aged projection NEVER denies.
- Remove the soft `cap-staleness` metric too: once `projectedAt` is deterministic provenance (§3.2), `now − projectedAt` measures *data age*, not projector lag — keeping it would be an actively misleading "staleness" signal. Remove the staleness ring/counter/methods and the `cap-staleness` Health-KV emission. (Keep the latency signal.)
- Remove `ErrCodeAuthFreshnessExceeded` and its denial-response branches **if cleanly dead** after the above; if any non-trivial coupling remains, leave the constant with a one-line current-behavior comment and remove only the emission — survey-driven, your call, but no dangling emit path.

### 3.2 Deterministic projectedAt = source `committedAt`
- Replace `projectedAt = time.Now()` (both engine paths in `evaluate.go`) with a **deterministic value derived from the source data's provenance**: the anchor actor vertex's `committedAt` (available in `nodeProps` / the projection input). Same input → same `projectedAt` across replay/rebuild (F-009 closed; no churn).
- `projectedAt` stays an RFC3339 string in the cap doc (Contract #6 shape unchanged) — it's now provenance ("as-of input state"), consumed only by monitoring + auth-trace.
- If `committedAt` is not cleanly available at the projection point, do NOT silently fall back to wall-clock — append a CAR describing what provenance *is* reachable and stop on that sub-item (the rest of the story can proceed). Multi-source "max committedAt" is NOT required; the anchor vertex's committedAt is sufficient (provenance, not security-load-bearing).

### 3.3 Gate-3 Vector #2 reframe (the security-posture change)
Vector #2 ("Projection lag window") currently claims DEFENDED via the freshness gate. New honest posture:
- Normal lag (<500ms p99, NFR-S7): event-driven reprojection converges; the stale-but-recent entry is acceptable and the action is observable in the auth-trace.
- Excessive lag / projector grossly behind: the bounded window is **ACCEPTED at the Processor** (no per-op denial). Enforcement of the projector-death case is **operational** (Refractor Capability-Lens health — see §3.5) and, for hard identity/session revocation, the **Gateway JWT-revocation** path (future). The `NoCapabilityEntry` check still denies a missing doc.
- Rework `capadv_projection_lag_test.go`: assert that a grossly-stale `projectedAt` **no longer denies** at the Processor (operation proceeds on permission-match), and that the cap-doc + auth-trace remain observable. Update the `gate3_test.go` Vector #2 row + `Enforcement` string to the accepted-window posture. The vector REMAINS in the suite (documented accepted risk), but it is no longer a denial-based defense — phrase the report row honestly (e.g. `Projection lag window | ACCEPTED-WINDOW | bounded; operational + Gateway enforcement (1.5.4)`); do not assert a denial that no longer happens. Keep all OTHER Gate-3 vectors DEFENDED and unchanged.

### 3.4 Planning artifacts — WINSTON ONLY (do not touch)
Winston updates `epics.md` (the line-1024 Gate-3 "Projection lag window exposure" scenario + the NFR-S7 notes at L130/L995) to the accepted-window posture and records the Gateway-token-revocation dependency. You do NOT edit these; if you spot a needed change, note it in the CAR.

### 3.5 Survey-and-document current Capability-Lens health — DO NOT BUILD
Andrew: **do not** build or "ensure" new Refractor Capability-Lens health alerting. Just **survey what exists today** (the Refractor's per-lens health / heartbeat / lag emission — `internal/refractor/health`, `cmd/refractor`, per-lens pipelines) and **document the current state** in `docs/components/refractor.md`: what liveness/lag signal the Capability Lens pipeline emits (or does not), and that this is the (current, as-is) operational backstop now that the per-op gate is gone. If the answer is "nothing specific is emitted/alerted for the Capability Lens pipeline," document exactly that as a known gap — do not fix it here.

## 4. Out of scope (do NOT touch)
- Building/wiring new Refractor health alerting (§3.5 — survey + document only).
- A Processor circuit-breaker / watched-liveness-boolean (explicitly deferred per Andrew).
- Gateway JWT/token revocation (future component; just reference it as the planned hard control).
- `ephemeralGrants` expiry, latency metrics, the cap-doc read/NoCapabilityEntry path.
- Routing installs through Processor (1.5.5); conformance freeze (1.5.7).

## 5. Verification gates (run all; paste tails into §7). Between local full-suite runs `make down && make up` (Deviation 14).
```
go build ./...
make vet
golangci-lint run ./...
make down && make up && make verify-kernel
go test ./internal/processor/... -p 1 -count=1
go test ./internal/refractor/... -p 1 -count=1
go test ./... -p 1 -count=1
make test-bypass
make test-capability-adversarial      # Gate 3 — Vector #2 reframed; the other 3 stay DEFENDED
```
If an unrelated package flakes on a repeated full-suite run, re-run it in isolation (Deviation 14); per-package green + CI are authoritative.

## 6. Deliverables checklist
- [ ] Freshness gate removed: no `checkFreshness`, no `StaleCeiling`, no projectedAt-age denial or `cap-staleness` Health signal; build clean with no dangling refs.
- [ ] `ephemeralGrants` expiry, latency ring, cap-doc/NoCapabilityEntry denial, auth-trace projectedAt — all preserved.
- [ ] `projectedAt` deterministic from source `committedAt` in both engine paths (no wall-clock); replay yields identical value (test it).
- [ ] `ErrCodeAuthFreshnessExceeded` + denial-response branches removed-if-dead (or dormant with comment, survey-justified) — no emit path remains.
- [ ] Gate-3 Vector #2 reframed (test asserts no-denial on stale projection; report row + enforcement string updated honestly); other 3 vectors unchanged + DEFENDED.
- [ ] aiagent traversal dependency note updated (no longer relies on Processor freshness rejection).
- [ ] `docs/components/{processor,refractor}.md` + `docs/contracts/{05-health-kv,06-capability-kv}.md` updated; Refractor Capability-Lens health current-state surveyed + documented (§3.5).
- [ ] All §5 gates green.

## 7. Closing summary (sub-agent fills in)
_(append here)_
