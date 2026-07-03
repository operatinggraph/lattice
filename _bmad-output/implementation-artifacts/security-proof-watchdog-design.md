# Continuous security-proof watchdog — **Warden** (a new Lattice component)

**Status: 🗄️ deferred-backup (Andrew, 2026-07-03).** Superseded by
[retire-phase1-security-gates-design.md](./retire-phase1-security-gates-design.md): the gate suites
this watchdog would have run continuously are being **retired** (duplicated by each defense's colocated
test), so a component to run them is no longer needed. Warden's **one** surviving justification is
**deployment-config drift** (that the *running* `nats-server.conf` matrix + *installed* RLS policies
haven't drifted from what the tests assume) — low on a single-cell stack, so Warden waits for a real
multi-node driver. If it revives, take the **corrected model** (§ below): a lean live
under-privileged-adversary **drift-probe** against the real stack, NOT the embedded stack-runner the
body of this doc describes. The embedded framing below is retained for the record but is **not** the
design to build.

Author: Winston (Designer fire, 2026-07-02). Brief:
[security-proof-watchdog-brief.md](./security-proof-watchdog-brief.md).

---

## For Andrew (two-line + the calls that are yours)

**What it does.** A new always-on platform component, **Warden**, re-runs the two live security
proofs (gate2 bypass-defense, gate3 capability-lens) **continuously and non-destructively** — against
its own throwaway embedded stack, never the operator's live one — and reports pass + freshness to
Health KV so a console can show "Bypass Defense ✓ · verified 3h ago" and go stale past the cadence.

**Your calls (three):**

1. **The name.** You reserved this for ratification. I recommend **Warden** (your working name) — it
   reads as a standing guard, is unambiguous next to Loom/Weaver/Refractor/Loupe, and no existing
   subject/identity collides. Alts you offered: Vigil, Assayer, Bulwark, Proctor. **Recommendation:
   Warden.** *(The whole design is written to `warden`; a rename is a global-replace, no structural
   change.)*

2. **RLS read-vector scope (the one real fork — §7, Fire 3).** Three of gate3's vectors (the Postgres
   RLS read-boundary — ReadV2/V4/V5) need a live Postgres; the other ~18 vectors run fully embedded.
   Do you want Warden to **continuously re-prove the RLS vectors too** (Fire 3: Warden provisions an
   *isolated dedicated proof-Postgres* — a drop-and-recreate `lattice_proof` scratch DB), or is
   **CI-only coverage of those three enough** (they stay `needsPostgres`-skipped in Warden, honestly
   reported as skipped, and the `make` gate keeps proving them on a clean stack)? **Recommendation:
   ship Fires 1–2 covering everything embedded/pure-Go can prove — the bulk — and leave RLS
   CI-only; add Fire 3 only if/when you want the RLS boundary continuously re-proven.** Fire 3 is
   fully designed below so it's a green-light, not a re-design.

3. **No frozen-contract change is required** — nothing here is staged uncommitted for you. Warden's
   `health.warden.<instance>` heartbeat is a standard Contract #5 §5.1 heartbeat; the proof markers
   extend the already-existing, **un-frozen** `health.gates.phase1.*` namespace (documented only in
   `docs/observability/health-kv-schema.md`, not in a frozen contract). Contract #5 §5.1's component
   list is explicitly illustrative ("Phase 2+ additions: loom, weaver, gateway"); adding `warden` to
   it is a courtesy the schema-doc already carries, so I did **not** stage a §5.1 edit. Flagging so
   it's a conscious call: if you'd prefer `warden` named in the frozen §5.1 list, say so and the
   Steward stages it — but the design does not need it.

**Everything else is decided (no TBDs).** The proof-execution mechanism, scheduler, control plane,
marker shape, isolation guarantee, and fail-closed semantics are all resolved below.

---

## 1. Problem & intent

**Source.** Brainstorming inventory #96 (the on-platform closed-loop auditor) + FR54 anomaly detection;
the Agentic-Operating-Model north star (fewer Andrew-interventions per change). The brief is the
definition-of-ready; this is the design.

The platform has two *live* security proofs — the Phase-1 adversarial gates:

- **gate2** (`make test-bypass`, [Makefile](../../Makefile)) — every write-path/authorization **bypass**
  attempt is BLOCKED (direct-KV write, off-namespace publish, Starlark-I/O escape, DDL-schema tamper).
- **gate3** (`make test-capability-adversarial`) — every **Capability-Lens** attack is DEFENDED
  (cross-target bleed, read-bypass, projection resurrection, lane-unauthorized, service-access bleed,
  the D1.4 read-path vectors, …).

Each writes a `health.gates.phase1.<gate>` marker `{passed, timestamp, commit}` that Loupe rendered as
a chip. Two problems:

1. **Only run manually** → a green chip means "proven whenever someone last ran it," not "proven
   recently." No continuous re-verification, no staleness.
2. **Destructive to a live stack.** Both `make` targets begin `make down && make up`, and `make up` is
   **kernel-only** — running either against a live `up-full` stack tears everything down and restores
   only NATS/Postgres/bootstrap/Refractor/Processor; Loom/Weaver/Bridge/Object-Store stay red and Loupe
   is killed. This is the "stack goes down and only partially comes back" Andrew observed.

**The grounding insight that shrinks this design.** The destructiveness is **entirely in the Makefile
recipe**, not the tests. Every per-vector test already stands up its **own embedded, isolated
in-process NATS** — `startBypassNATS` / `setupBypassHarness` bind a random port (`Port = -1`) and own a
private JetStream store dir (`jsstore.Dir(t)` → `t.TempDir()`), fresh per test
([internal/bypass/helpers.go](../../internal/bypass/helpers.go)). The `down && up` exists only so the
*roll-up* marker-writers (`TestGate2_Report` / `TestGate3_Report`) can (a) touch a known-clean live
Health KV and (b) satisfy the three Postgres-backed RLS read-vectors. **The proofs themselves never
needed the live stack.** So Warden does not stand up a Docker stack, does not shell `make`, and never
touches the operator's stack: it runs the *existing suite* against the temp-dir/loopback isolation the
suite already provides, and writes the markers itself.

**Intent.** A standing red-team component that (1) re-proves g2/g3 on a loose cadence and on demand,
(2) reports pass + freshness + the proven commit to Health KV under a generic proof namespace a console
reads, and (3) is the **operator-facing** path, superseding the destructive `make` targets for
everything but CI.

---

## 2. The shape

### 2.1 A new component, `warden`

Mirrors the shape of the platform's lean always-on binaries (object-store-manager, gateway): a
`cmd/warden/main.go` entry point + an `internal/warden/` package, all NATS access through `substrate`
(no raw `nats.go`), a Contract #5 heartbeat, and a `internal/warden/control` micro-service responder
(the loom/weaver control-plane precedent).

| Path | Role |
|------|------|
| `cmd/warden/main.go` | Binary entry point; wires the substrate connection, the proof registry, the scheduler, the control responder; stamps its own build SHA (ldflags) |
| `internal/warden/runner.go` | The proof runner: execs the precompiled proof binary per proof, parses `test2json`, aggregates per-vector results, writes the markers |
| `internal/warden/registry.go` | The proof registry — the extensibility seam (§4): a static list of `{proofId, displayName, testFilter, needsPostgres}` |
| `internal/warden/health.go` | The `health.warden.<instance>` heartbeat + issue tracking (fail-closed aggregation) |
| `internal/warden/control/service.go` | The `lattice.ctrl.warden.*` responder (`status`, `run`) |

**What Warden is NOT.** It submits **no ops**, holds **no capability identity / service actor**, reads
**no Core KV**, and mutates **no business state**. Its entire live-stack footprint is: *write* its own
Health KV keys, and *respond* on its control subject. This is materially lighter than
object-store-manager (which submits `DetachObject`) — Warden needs no bootstrap service-actor seed.

### 2.2 Read path (P5) & write path (P2)

- **Read path.** The only consumer is Loupe (the sanctioned inspector), which reads `health.proofs.*`
  and `health.warden.*` **directly from Health KV** — Health KV reads are not Capability-gated (Contract
  #5 §5.7), and Loupe is the P5 inspector exception. **No lens, no Core-KV read, no new read-model.**
- **Write path (P2).** Warden's only writes are Health KV heartbeat + marker writes — the **explicitly
  sanctioned direct-KV-write class** (Contract #5 §5.1; architecture P2's Health-KV exception). It never
  writes Core KV and submits no ops. Fully P2-clean.

### 2.3 Proof-execution mechanism (decided — Winston's call, not Andrew's)

**Chosen: the precompiled proof binary.** `make` produces `bin/proofs.test` via
`go test -c ./internal/bypass -o bin/proofs.test` (a standard build artifact — a compiled test binary,
no toolchain needed at runtime). Warden `exec`s it per proof with a name filter + `-test.v
-test.json`, and parses the `test2json` stream (`go tool test2json` format — `{"Action":"pass|fail",
"Test":"...","Output":"..."}`) into `[]VectorResult{name, result, detail}`. Warden then writes the
markers itself.

Why this over the alternative (extract every vector into a callable non-test library that Warden
imports in-process): the vectors are ~21 `*testing.T` tests that already run embedded and isolated.
Re-authoring them off `*testing.T` into a parallel library — plus a test wrapper that can drift from it,
plus importing the NATS *server* API into a production binary — is a large refactor that **duplicates
the proof logic** and creates a drift surface. The precompiled-binary path keeps **the test suite as
the single authoritative source of proof truth** (zero duplication, zero drift) and is the simplest
extension of what exists. The one cost — Warden execs a subprocess and parses JSON — is small and
well-trodden (`test2json` is a stable Go format). *Recorded alternative, not adopted.*

**Isolation guarantee (the core requirement).** Warden runs the proof binary with the vectors' own
temp-dir + loopback-random-port isolation; it passes **no `NATS_URL`/`NATS_NKEY`** pointing at the live
stack and **excludes the roll-up tests** (`TestGate2_Report` / `TestGate3_Report` — the only tests that
dial a live NATS, and only to write the *old* marker). Result: a Warden proof run touches **only**
ephemeral `t.TempDir()` JetStream stores + loopback ports it owns and tears down — it **cannot** corrupt
or contend with the live stack's state. Warden gives the subprocess its own scratch `TMPDIR` for
defense-in-depth.

### 2.4 Scheduler (decided)

Warden **self-schedules** with an internal ticker — it *is* the scheduler, exactly like
object-store-manager's `defaultReconcileInterval = 1h` reconcile ticker and the bridge's poll loop. A
loose cadence, `WARDEN_PROOF_INTERVAL` (default **6h** — Andrew: "doesn't have to be often"), plus one
run shortly after startup. **Not `@every`.** The `@every`/cron substrate (ADR-51, NATS 2.14) schedules
*platform operations* → Processor; running a Go test suite is an **ops-plane** activity, not a
Core operation — the brief calls this out explicitly, and it holds.

### 2.5 On-demand ("check now") — the control plane

Warden runs a NATS micro-service responder (`github.com/nats-io/nats.go/micro`) over core
request/reply, mirroring `loom-control` / `weaver-control` (see
[docs/components/control-plane.md](../../docs/components/control-plane.md)). Subject root
`lattice.ctrl.warden.*`:

| Op | Subject | Kind | Meaning |
|---|---|---|---|
| `status` | `lattice.ctrl.warden.status` | read (exact) | current per-proof pass/freshness + runner status + last/next run |
| `run` | `lattice.ctrl.warden.run` | mutate (async) | trigger an immediate proof run; `{started:true}`, or `{started:false, note:"run in progress"}` if one is in-flight (debounced) |

`run` is asynchronous like Refractor's `rebuild`: `started:true` means accepted; the result lands in
the markers when the run completes. **Loupe reaches this with no grant change** — Loupe and the
`lattice` CLI already hold `lattice.ctrl.>` publish (nkey matrix); Warden's user gets
`lattice.ctrl.warden.>` + `allowResponses` (mirrors loom). The one client-side task (adding `warden`
to Loupe's `controlComponents` allow-list — the drift guard) is **Loupe-lane** work, filed there.

---

## 3. The marker & heartbeat shape

### 3.1 Proof markers — `health.proofs.<proofId>` (a **new, generic** namespace)

One key per proof, in the Health bucket, written by Warden. Generic by `proofId` so a future proof
joins **without a reader change** (§4):

```json
{
  "key": "health.proofs.gate2",
  "proofId": "gate2",
  "displayName": "Write-path bypass defense",
  "passed": true,
  "vectorsTotal": 4,
  "vectorsPassed": 4,
  "vectorsSkipped": 0,
  "vectors": [
    { "name": "Direct KV write",        "result": "BLOCKED", "enforcement": "undetectable-without-EventList (Phase-1)" },
    { "name": "Off-namespace publish",  "result": "BLOCKED", "enforcement": "JetStream consumer FilterSubjects" }
  ],
  "lastRunAt": "2026-07-02T14:32:18Z",
  "durationMs": 8123,
  "commit": "6a36132",
  "runId": "wr-Lk2Pn6mQrtwzKbcXvP3T",
  "ranBy": "warden"
}
```

- **No TTL** on markers — a proof result is *last-known-good* and must persist even when the runner is
  down (so a console shows "verified 3h ago" and lets *that* age drive staleness). Freshness = `now −
  lastRunAt` vs the cadence, judged by the reader; the runner's own liveness comes from the heartbeat
  below.
- `ranBy` distinguishes `"warden"` (continuous) from `"ci"` (the legacy `make`-path marker), so a
  console can tell a continuous proof from a CI one.
- **Fail-closed:** a vector that *errors* (couldn't execute — subprocess crash, harness failure) is
  **not** counted as passed and **not** counted as skipped; it forces `passed:false` (`result:
  "ERRORED"`). A proof that cannot run is not a proof. Only a vector that is *skipped by configuration*
  (a `needsPostgres` vector in a Postgres-less Warden) counts as `vectorsSkipped` with an explicit
  reason — never silently folded into `vectorsPassed` (the anti-false-green rule).

### 3.2 Runner heartbeat — `health.warden.<instance>` (standard Contract #5 §5.1)

A normal §5.1 heartbeat (10s cadence, 100s TTL, instance `wrd-<NanoID>`, `status`/`issues`), with
Warden-specific metrics:

```json
"metrics": {
  "proof_runs_total": 42,
  "proof_runs_failed_total": 0,
  "last_run_at": "2026-07-02T14:32:18Z",
  "next_run_at": "2026-07-02T20:32:18Z",
  "last_run_duration_ms": 15204,
  "proofs": { "gate2": "pass", "gate3": "pass" }
}
```

- **Aggregated status (avoids the platform's canonical false-green).** If the latest run of any in-scope
  proof did not pass, the heartbeat carries a `severity:"error"` issue (`code: "SecurityProofFailing"`,
  message naming the proof + failing vector) and `status: "unhealthy"`; an *errored/inconclusive* proof
  is `degraded` with `code: "SecurityProofInconclusive"`. Warden aggregates like Loom/Weaver — it does
  **not** ship a static `"healthy"` (the object-store-manager known-gap explicitly not repeated here).

### 3.3 Legacy `health.gates.phase1.*` — untouched (back-compat)

The `make test-bypass` / `-capability-adversarial` CI recipes keep writing
`health.gates.phase1.gate2/gate3` exactly as today (the roll-up tests are unchanged). Warden writes the
**new** `health.proofs.*` namespace; Loupe's returning surface reads `health.proofs.*` +
`health.warden.*`. No breaking change; the two namespaces coexist (CI-marker vs continuous-marker).
`docs/observability/health-kv-schema.md` gains the `warden` component + the `health.proofs.<proofId>`
namespace rows at build time (documentation — not a frozen contract).

---

## 4. Extensibility (open Q4 — resolved)

The marker/report shape is **generic over `proofId`**, and the set of proofs is a **data-driven
registry** (`internal/warden/registry.go`): each entry is `{proofId, displayName, testPackage,
testFilter, needsPostgres}`. Adding a future proof (a new gate, a fuzz suite) is: add a registry entry
(+ compile its test package into the proof binary if it's a new package). The **reader is
change-free** — a console iterating `health.proofs.*` renders any proofId. No component change, no
marker-shape change. gate4 (embedded-only, no live marker) and gate5 (30-min e2e) are out of scope per
the brief — but *nothing structural* excludes a later registry entry for them.

---

## 5. Contract surface

| Contract / doc | Section | Change vs. build-to |
|---|---|---|
| **Contract #5 — Health KV** (frozen) | §5.1 (component list), §5.2 (heartbeat shape), §5.6 (TTL) | **Build-to — no edit.** Warden's heartbeat is a standard §5.1 heartbeat; §5.1's list is illustrative. The proof markers are a non-heartbeat reserved namespace (like the pre-existing `health.gates.*`), which §5.1 already accommodates ("Health KV keys do NOT follow Core KV's patterns … a separate addressing space"). |
| `docs/observability/health-kv-schema.md` (not frozen) | Reserved namespaces + per-component | **Doc edit at build time** — add `warden` + `health.proofs.<proofId>`. Freely editable; not a frozen contract. |
| `docs/components/control-plane.md` (this-page contract authority) | Op vocabulary; drift guard | **Doc edit at build time** — add the `warden` responder (`status`, `run`) + its client reconciliation point. |
| **No other contract** (#1 key-shapes, #2 ops, #6 capability, #10 orchestration) | — | Untouched — Warden mints no vertices/aspects/links, submits no ops, defines no lens. |

**No frozen-contract edit is staged uncommitted.** (See "For Andrew" #3 — a §5.1 courtesy line is
available if Andrew wants it, but the work does not require it.)

---

## 6. Migration & test strategy

- **Migration.** Additive. New binary + new Health namespace; nothing existing changes behavior. The
  `make` gates and their `health.gates.phase1.*` markers keep working. Loupe's chip removal already
  happened (Loupe lane); the surface returns reading the new namespace (Loupe-lane, Fire 4).
- **Deploy.** Add `run-warden` to the Makefile and include Warden in `make up-full`'s `orchestration`
  target (dev-mode). Add the `warden` nkey to `deploy/gen-dev-nkeys/main.go` +
  `deploy/nats-server.conf` (pattern below). No bootstrap seed (no service actor).
- **Tests (each fire ships green).**
  - *Unit:* `runner.go` parses a **canned `test2json` fixture** (pass, fail, errored, skipped) → asserts
    the exact marker shape incl. the fail-closed `ERRORED → passed:false` and `skipped ≠ passed` rules.
  - *Unit:* `health.go` aggregation — a failing proof → `unhealthy` + `SecurityProofFailing` issue; an
    inconclusive proof → `degraded`; all-pass → `healthy` (no static green).
  - *Component smoke (self-contained, embedded — the house pattern):* Warden runs the **real**
    `bin/proofs.test` for gate2 against embedded isolation, asserts `health.proofs.gate2.passed == true`
    and a `warden` heartbeat appears — never touching a shared stack (mirrors `make test-object-gc`).
  - *Control:* a `status` + `run` round-trip against an embedded responder.
- **Gates.** Standard: `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`,
  and — because this **is** the security plane — a green `make test-bypass` / `-capability-adversarial`
  on a clean stack proving Warden's marker path didn't perturb the suite.

### nkey matrix entry (Fire 1)

```go
{
    name:           "warden",
    desc:           "continuous security-proof watchdog; runs g2/g3 against embedded isolation, reports to Health KV",
    // No OpsWildcardSubject: submits no ops. No service actor. Health-KV write +
    // its own control plane only.
    pubAllow:       []string{"lattice.ctrl.warden.>", "$KV.health-kv.>", "$JS.API.>", "$JS.ACK.>"},
    pubDeny:        denyProtected([]string{"$KV.core-kv.>", "$KV.capability-kv.>"}, coreKVStream, capabilityKVStream),
    allowResponses: true, // control responder (lattice.ctrl.warden.>)
},
```

---

## 7. Fire-by-fire decomposition (for the Lattice Steward)

Each fire is independently shippable + green. Fires 1–2 are the ratified core; Fire 3 is gated on
Andrew's RLS-scope call (§For-Andrew #2); Fire 4 is Loupe-lane.

**Fire 1 — Warden binary + gate2 proof + heartbeat + markers (the non-destructive core).**
`cmd/warden` + `internal/warden/{runner,registry,health}.go`; `make` builds `bin/proofs.test`; the
gate2 registry entry (the 4 bypass categories, excluding `TestGate2_Report`); `test2json` parse →
`health.proofs.gate2`; the §5.1 heartbeat with aggregated status; the 6h ticker + startup run; nkey
matrix + `run-warden` + `up-full` wiring; `docs/components/warden.md` + README row +
architecture-overview mention + a Surveyor-rotation slot. *Value:* gate2 is continuously, non-
destructively re-proven with freshness — standalone.

**Fire 2 — gate3 proof + the control plane.** Add the `gate3` registry entry — the embedded-NATS
write-path vectors (V1–V8) + the pure-Go authn read-vectors (ReadV1/ReadV3), excluding the
`needsPostgres` RLS vectors (ReadV2/V4/V5) and `TestGate3_Report`. Write `health.proofs.gate3` with the
RLS vectors honestly reported as `vectorsSkipped` (reason: "Postgres-gated; CI-covered"). Add
`internal/warden/control` (`status`, `run`) + reconcile `docs/components/control-plane.md`. *Value:*
gate3's embedded surface (18 of 21 vectors) is continuously proven; on-demand "check now" works.

**Fire 3 — continuous RLS read-vector proof (Andrew-gated, §For-Andrew #2).** *Only if Andrew wants
continuous RLS re-proof.* Warden provisions an **isolated dedicated proof-Postgres** — recommended
mechanism: a drop-and-recreate `lattice_proof` scratch database (not the live `lattice` DB), created
per run through the platform DDL/grant-writer and dropped after, so the RLS policy runs under the real
non-superuser posture with **zero shared state** with the operator DB. Warden passes
`POSTGRES_TEST_DSN` (→ `lattice_proof`) to the proof subprocess for the RLS vectors, folds ReadV2/V4/V5
into `health.proofs.gate3` (`vectorsSkipped → 0`). *If Andrew says CI-only is enough, this fire is
dropped* — the RLS vectors stay CI-gated and Warden reports them skipped (no dead scaffolding: the
skip is honest and the CI path already proves them). Postgres write-guard note: the scratch DB is
seq-guarded via the same platform grant-writer the RLS vectors already use; there is no plain/protected
`PostgresAdapter` LWW surface here (Warden writes no lens targets).

**Fire 4 — Loupe surface (Loupe lane, not this stream).** A map node for Warden + a security-proof
panel (human-named chips, freshness from `lastRunAt`, "check now" → `lattice.ctrl.warden.run`), reading
`health.proofs.*` + `health.warden.*`. Filed to [loupe.md](../planning-artifacts/backlog/loupe.md)
once Fire 2's read/trigger seams exist; UX pointer: the removed gate chips are the placeholder this
replaces.

---

## 8. Risks & alternatives (adversarial pass — discharged inline)

Per the Designer rigor, a substantial new-component design gets an adversarial pass; run inline and
folded in (no deferred gate left for the Steward).

- **False-green via skipped vectors.** *The* sharp edge of a security watchdog. Mitigated three ways:
  (1) `vectorsSkipped` is a first-class marker field with a per-vector reason — a skip is never folded
  into `vectorsPassed`; (2) an *errored/inconclusive* vector (couldn't execute) is fail-closed
  (`passed:false`), distinct from a *by-config* skip; (3) the heartbeat aggregates to `degraded` when
  any in-scope proof is inconclusive, so a wedged runner can't read green.
- **False-green via a wedged runner.** The markers have no TTL (last-known-good persists), so a dead
  Warden would otherwise leave a stale-but-green marker. Caught by the **heartbeat**: `health.warden.*`
  has the 100s §5.6 TTL, so a dead runner's heartbeat expires and the console sees "runner down" while
  `lastRunAt` stops advancing → the proof reads *stale*, not *fresh-green*. The two-key split
  (persistent marker + TTL'd heartbeat) is deliberate.
- **Isolation leakage into the live stack.** The proof subprocess gets no live `NATS_URL`/`NATS_NKEY`,
  a scratch `TMPDIR`, and Warden excludes the only live-dialing tests (the roll-ups). The vectors' own
  `Port=-1` + `jsstore.Dir` isolation does the rest. A defense-in-depth check: Warden asserts the
  subprocess env carries no live-stack coordinates before exec.
- **Subprocess/`test2json` brittleness.** `test2json` is a stable, documented Go format; the parse is
  unit-tested against canned fixtures incl. malformed lines (→ inconclusive, fail-closed). A missing
  `bin/proofs.test` → `SecurityProofInconclusive` (degraded), never a silent pass.
- **The proof binary drifts from the suite.** It *is* the suite (`go test -c ./internal/bypass`) — the
  registry names test filters, so a renamed/added vector that isn't in a filter shows up as
  `vectorsTotal` drift the smoke test catches. Chosen precisely to avoid the library-duplication drift
  of the rejected alternative (§2.3).
- **Alternative rejected — in-process library extraction (§2.3):** more code, a drift surface,
  NATS-server-in-prod; worse on every axis but "no subprocess."
- **Alternative rejected — Warden shells `make` in a `-p` compose project:** real Docker orchestration
  from a component (heavy, un-Lattice), and the whole point was to *stop* needing a stack stand-up. The
  embedded isolation the suite already has makes it unnecessary.
- **Naming collision / grant creep.** `warden` collides with nothing (`git grep` clean for the
  subject/identity); the nkey is the tightest in the matrix (no ops, no service actor) — strictly
  smaller attack surface than any existing engine user.

---

## 9. Definition of done (the component)

gate2 + gate3 (embedded surface) re-proven on a 6h cadence + on demand, non-destructively, with
per-vector fail-closed results + freshness in `health.proofs.*`, an aggregating `health.warden.*`
heartbeat, a control plane Loupe already reaches, a component doc + README + overview + survey slot, and
green security gates proving the marker path is inert to the suite. RLS read-vectors either
continuously proven (Fire 3) or honestly reported skipped + CI-covered — Andrew's call.
