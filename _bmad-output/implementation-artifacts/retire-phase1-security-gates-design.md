# Retire the Phase-1 destructive security-gate apparatus

**Status: ✅ Andrew-ratified (direction, in-session 2026-07-03).** The per-vector coverage audit
(§4) is Fire 1's own gate — a vector is deleted only once its assertion is shown covered elsewhere.
Author: Winston. Supersedes the Warden new-component proposal
([security-proof-watchdog-design.md](./security-proof-watchdog-design.md), now deferred — §6).

---

## For Andrew & the agentic team (read this before touching a gate)

**What this is.** gate2 (`make test-bypass`) and gate3 (`make test-capability-adversarial`) were
**Phase-1 adversarial proofs written when auth was stubbed.** The real defenses have since shipped —
NATS account-level write-restriction, the Starlark sandbox, the step-6 DDL validator, the Postgres RLS
read boundary, the CapabilityAuthorizer — **each with its own colocated white-box test.** The gate
suites are now a **redundant (and in places stale) second copy, wrapped in a destructive `make down &&
up` recipe** that half-restores a live `up-full` stack. We retire the apparatus.

**The one invariant — no defense is being removed.** Every enforcement mechanism keeps its own test
(they are untouched). We delete a *duplicate*, its *destructive wrapper*, and the *dead marker* — never
a defense, and never a proof that isn't covered elsewhere. §4 is the guard: **anything the audit finds
NOT covered is KEPT (promoted to a colocated/CI test), never silently dropped.**

**Why the team must not "fix" these instead of retiring them.** Do not add gate3's vector #14 into the
gate target; do not rewrite gate2's direct-KV-write vector to assert transport-deny; do not re-file
"gate coverage gap" demand; do not rebuild the Loupe gate chips or a watchdog component. Those are all
motion around a Phase-1 scaffold that is being removed. The correct move for any gate finding now is:
**confirm the colocated mechanism test covers it → delete the gate copy; or if uncovered → move the
assertion into the mechanism's package.**

---

## 1. Problem & intent

The platform carried two *live security proofs* — the Phase-1 gates — that Loupe rendered as chips.
Two facts, both grounded, make the apparatus obsolete:

1. **Destructive by construction.** Both `make` targets begin `make down && make up`, and `make up` is
   **kernel-only** — running either against a live `up-full` stack tears everything down and restores
   only the kernel (Loupe killed; Loom/Weaver/Bridge/Object-Store left red). This is the "stack goes
   down and only partially comes back" pain. The destruction is in the recipe, not the tests.
2. **The proofs are duplicated by better-placed tests.** The gate vectors were written against a
   Phase-1 world of stubbed auth. The real enforcement now ships with its own white-box tests, run on
   every change to the mechanism (§2). The gate suite re-asserts — sometimes *staler* than — what those
   tests already prove.

The vectors **already run non-destructively in CI** as plain `go test ./internal/bypass/...`
([ci.yml:115,129](../../.github/workflows/ci.yml)); the destructive targets add only the
`health.gates.phase1.*` marker write — the Loupe-chip surface that has already been removed. So the
targets gate nothing the normal test job doesn't already gate.

**Intent.** Delete the destructive apparatus; keep every real defense's colocated test; preserve — in a
clearly-named CI home — only the genuinely-unique outcome-level adversarial residual; update the
load-bearing references (CLAUDE.md, CI, docs) so no agent trips over a dangling "Gate 2/3" mandate.

---

## 2. Grounding — every gate vector's real enforcement has its own test

Verified against the tree (2026-07-03):

| Gate vector | Real enforcement today | Colocated test that owns it |
|---|---|---|
| gate2 #1 — direct-KV write | NATS account write-restriction (transport **deny**) | `internal/natsperm/conf_test.go` |
| gate2 #2 — off-namespace publish | account pub-scoping + JetStream consumer `FilterSubjects` | `internal/natsperm/conf_test.go` |
| gate2 #3 — Starlark I/O escape | Starlark sandbox | `internal/processor/starlark_runner_test.go`, `starlark_kv_test.go`, `starlark_builtins_test.go` |
| gate2 #4 — DDL schema violation | step-6 validator | `internal/processor/step6_validate_test.go` |
| gate3 read-path — ReadV2/V4/V5 | generated Postgres RLS policy | `internal/refractor/adapter/rls_test.go`, `rls_verify_test.go` |
| gate3 read-path — ReadV1/V3 | JWT actor-authentication | `internal/gateway/auth/*_test.go` |
| gate3 write-path — V1–V8 | CapabilityAuthorizer + Refractor projection-write guard | CapabilityAuthorizer + adapter guard unit tests (colocated) |

**The stale-not-just-redundant case.** gate2 #1 literally lets the rogue write **succeed** and proves
only that it is *detectable* ([bypass_direct_kv_test.go:60-68](../../internal/bypass/bypass_direct_kv_test.go)) —
a Phase-1 fallback. The system now *transport-denies* that write (natsperm, live enforcement ON), and
`natsperm/conf_test.go` owns that proof. The gate vector asserts a property the platform no longer
relies on: it is misleading and must go, not be "fixed."

---

## 3. What is retired vs kept

**RETIRED (deleted):**
- The `make test-bypass` and `make test-capability-adversarial` targets (the `down && up` recipe).
- The `TestGate2_Report` / `TestGate3_Report` roll-ups + the `health.gates.phase1.gate2/gate3` marker
  writes + the `gate2-report.txt` / `gate3-report.txt` artifacts.
- Every per-vector test whose assertion the §4 audit confirms is fully owned by a colocated mechanism
  test — and the stale Phase-1-fallback vectors (gate2 #1's "detectability") outright.

**KEPT (untouched or promoted):**
- **Every mechanism's colocated white-box test** — `natsperm`, `starlark_*`, `step6_validate`, `rls_*`,
  `gateway/auth`, the CapabilityAuthorizer tests. These are the real, ongoing proofs. Not touched.
- **The genuinely-unique residual:** the *outcome-level, whole-system adversarial composition* a
  white-box unit test doesn't replicate — chiefly the assembled **read-path** proof (JWT → actor-stamp
  → RLS denies actor-B-reads-actor-A) and any "forbidden outcome unreachable **by any path**" assertion
  the audit finds uncovered. This residual is relocated to a **clearly-named CI home** (recommended:
  `internal/security/adversarial` or kept in `internal/bypass` renamed to drop the "gate" framing),
  running embedded in the normal `go test ./...` job. It is NOT a destructive target and writes no
  marker.

The dividing rule is §4 and it is not optional.

---

## 4. The coverage audit (Fire 1's gate — the anti-regression guard)

**No vector is deleted until its assertion is shown covered by a named colocated test.** For each of
the 54 test functions in `internal/bypass/`:

1. Identify the security *assertion* (the forbidden outcome it proves impossible).
2. Find the colocated mechanism test that owns that assertion (the §2 table is the starting map, not
   the finish — confirm at the assertion level, not the file level).
3. **Covered →** delete the gate copy. **Uncovered →** KEEP it: move the assertion into the mechanism's
   own package as a colocated test, or retain it in the renamed adversarial-residual home. **Never drop
   an uncovered assertion.**

The audit's output is a short table in the Fire-1 commit message (vector → covering test → deleted/kept)
so the deletion is auditable. A green `go test ./...` after the prune, plus a diff review that every
deleted vector maps to a live covering test, is the gate.

---

## 5. Contract & reference surface (what the retirement must also update — or agents will trip)

**No frozen-contract change.** Everything below is house-rules / CI / docs (freely editable):

- **`CLAUDE.md` verification gates.** Currently lists `make test-bypass (Gate 2, all BLOCKED)` and
  `make test-capability-adversarial (Gate 3, all DEFENDED)` as required gates. **Remove both** in the
  same change that deletes the targets; the security proof now = the colocated mechanism tests + the
  adversarial residual, all under the normal `go test`/`make test` gate.
- **`.github/workflows/ci.yml`.** Remove the stack-gate steps that run the roll-ups + marker writes
  (lines ~115/129/130 region) and any `make down` teardown they anchored. The per-vector tests that
  survive the audit run in the existing `unit` job (`go test ./... -p 4`).
- **`docs/observability/health-kv-schema.md`.** Mark the `health.gates.phase1.*` namespace **retired**
  (no producer). Leave gate4/gate5 references intact — this retirement is scoped to the two *security*
  gates only (gate4 embedded-rollback + gate5 hello-lattice are separate, out of scope).
- **`Makefile`.** Delete the two target recipes + their doc comments.
- **Arch-review row `gate3-vector14-in-gate`** (this board) — **subsumed.** The gateway-impersonation
  vector #14's fix under retirement is: its assertion lives as a colocated `internal/gateway` test (it
  already does — that was the finding); the gate that never ran it is deleted. Close it into this fire.
- **Loupe** — the security-proof surface expectation (a returning chip panel / watchdog node) is
  **withdrawn**; Loupe shows no security-gate chips. A Loupe-lane pointer notes the surface is retired,
  not pending. (Loupe-lane edit, filed there — not this stream.)

---

## 6. Warden — deferred, not built (the one residual justification)

The [Warden watchdog design](./security-proof-watchdog-design.md) is **deferred, not deleted.** Its
only justification that survives this retirement is **deployment-config drift**: continuous assurance
that the *running* `deploy/nats-server.conf` account matrix + the *installed* RLS policies have not
drifted from what the tests assume — the one thing neither CI nor colocated tests catch (they prove the
code and the *generated* config, not the *deployed-and-possibly-hand-edited* config). On a single-cell
dev stack that risk is low, so Warden waits for a **real multi-node deployment driver**; if it revives,
it is a **lean live under-privileged-adversary drift-probe** (Warden design §"corrected model"), never
a stack-runner. The Warden doc is re-bannered 🗄️ deferred-backup with a pointer here.

---

## 7. Fire decomposition

**Fire 1 — the retirement (one coherent fire; internal order).** Coupled work that must land together
(deleting the targets while leaving CLAUDE.md pointing at them would red every subsequent fire's gate
list). Internal order: (a) run the §4 coverage audit, emit the vector→covering-test table; (b) delete
the fully-covered + stale vectors, promote any uncovered assertion to its mechanism package /
adversarial-residual home; (c) rename `internal/bypass` → the adversarial-residual home (or keep, drop
"gate" naming) and delete the roll-ups + marker writes; (d) delete the two `make` targets + recipe
comments; (e) strip the CI stack-gate steps; (f) update `CLAUDE.md`, `health-kv-schema.md`, close the
`gate3-vector14-in-gate` row. **Green gate:** `go build ./...`, `make vet`, `golangci-lint run ./...`,
`go test ./...` (the surviving adversarial residual passes embedded), and CI green with the stack-gate
steps gone. Ships as one reviewable diff with the audit table in the commit.

**Fire 2 — Loupe pointer (Loupe lane, optional).** Note the security-proof surface as retired on the
Loupe map/board. Not this stream.

*(No Fire for Warden — deferred to a real drift driver, §6.)*

---

## 8. Risks & alternatives

- **"We deleted a real proof."** The single material risk, fully mitigated by §4: deletion is
  assertion-by-assertion gated on a named covering test; uncovered assertions are promoted, never
  dropped; the audit table makes every deletion reviewable. A green `go test ./...` proves the survivors
  hold.
- **"CI silently loses a merge gate."** No — the surviving vectors + all mechanism tests run in the
  existing `unit` job. Only the *redundant* stack-gate steps (which re-ran embedded tests behind a
  destructive wrapper for a marker) are removed. Net merge-gate coverage is unchanged.
- **Alternative — keep the gates, just make them non-destructive** (drop `down && up`). Rejected: it
  preserves the duplication (two copies of every proof, one staler) and the dead marker, and it keeps a
  "Gate 2/3" mandate the team must maintain. Retiring the duplicate is the simpler, truer end state.
- **Alternative — build Warden to run them continuously.** Rejected for now (§6): with the proofs
  retired, Warden's only remaining value is config-drift, which isn't a live concern on a single-cell
  stack. Deferred, not built.

---

## 9. Definition of done

The two destructive `make` targets, the roll-ups, the `health.gates.phase1.*` writes, and every
audit-confirmed-redundant/stale vector are gone; every real defense keeps its colocated test; any
uncovered assertion is promoted to a named home; `CLAUDE.md` / CI / `health-kv-schema.md` no longer
reference the retired gates; the `gate3-vector14-in-gate` row is closed; CI is green with fewer,
non-destructive steps. The security proof is now *where the enforcement lives*, not in a Phase-1
scaffold — and nothing destructs.
