# Vertical-app Health-KV self-report — honest dependency-probing heartbeat

**Status: ✅ Andrew-ratified (2026-07-02) — §5.6 TTL ON (the one judgment call: apps born
TTL-conformant); Fires 1–2 COLLAPSED to ONE (reporter + BOTH apps together) per Andrew's
fewer-larger-fires rule; the objmgr adoption stays the optional tail.** Loupe-2.0 reconciliation folded (2026-07-02): `cmd/loupe/**` is its own lane and
F4 "Health absorption" retires the current Health tab, so this design's only interface to Loupe is
the Contract #5 §5.2 document — the render test case moved out of Fire 1 to the Loupe lane (§7, §9).
**Author:** Winston (Designer fire, 2026-07-01)
**Backlog:** Stream-2 feature backlog — *[Verticals] loftspace-app / clinic-app have no Health-KV self-report* (★★, S) — PO-discovered.
**Owning component:** a new thin reporter in `internal/healthkv` (the platform seam) + two consumers `cmd/loftspace-app` and `cmd/clinic-app`. Docs in `docs/observability/health-kv-schema.md`.

---

## For Andrew

**What it does (two lines).** The two vertical apps (`loftspace-app`, `clinic-app`) write **no** Health-KV heartbeat at all, so an admin-actor load failure or a NATS/Postgres outage is invisible to Loupe until a user's write 400s. This adds a **dependency-probing** Contract #5 heartbeat to each app — not a static liveness ping, but one that reports `degraded`/`unhealthy` with structured `issues[]` when its own bootstrap actor, NATS, protected-read-model, or auth posture is broken.

**Architectural fork:** **none.** Health KV is the already-sanctioned direct-write plane (architecture P2 exception, Contract #5 §5.7). No new primitive (`substrate.KVPutWithTTL` + `NewNanoID` already exist), no new bucket (`health-kv` is already provisioned with per-key TTL), **no reader change** (Loupe's `computeHealth` already renders any `health.<component>.<instance>` key uniformly). The one design choice is *where the shared code lives* — a thin `internal/healthkv` reporter vs. copy-paste per app; I recommend the shared reporter (§4) and explain the trade-off.

**Frozen-contract change:** **none required.** The apps emit a Contract #5 §5.2 document as written; the only doc edit is to the **non-frozen** schema inventory `docs/observability/health-kv-schema.md` (add the two app components + their issue codes). I deliberately did **not** stage a frozen-contract edit — there is nothing to change in Contract #5.

**The one judgment call for you (not a fork).** Should the reporter set the §5.6 **TTL** on its write? I say **yes** — the apps should be born TTL-conformant so they don't become two more no-TTL writers the sibling *health-kv-ttl-orphan-expiry* design (📐 awaiting-Andrew) then has to retrofit. This costs nothing (the mechanism exists) and is strictly the correct §5.6 behavior. If you ratify the TTL design first, these apps already conform; if you don't, they're still no worse than the incumbents. Called out in §5.4.

---

## 1. Problem & intent

### 1.1 The symptom (grounded demand)

`cmd/loftspace-app` and `cmd/clinic-app` are auth-less, admin-acting HTTP front-ends over a running Lattice deployment (both bind loopback, connect to NATS as the primordial admin actor, submit ops on the user's behalf — see each `main.go` header). They have **no** Health-KV writer. Every platform daemon self-reports (`processor`, `refractor`, `loom`, `weaver`, `bridge`, `object-store-manager`); the two vertical apps are the only long-running processes on the stack that are **dark** to Loupe and the Lamplighter.

The failure that filed this item (hit live **2026-07-01**, fixed in `40f4d25`): the on-disk `lattice.bootstrap.json` carried `version:"13"` while `checkVersion` required `"14"`, so `bootstrap.Load` failed and `adminActor` was left empty. Per `cmd/loftspace-app/main.go:74-97`, that is **non-fatal by design** — the UI keeps serving and pure reads work — so nothing surfaced until a user tried to *apply/sign* and got a `/api/op` 400 ("unconfigured actor"). An operator watching Loupe saw a fully green board while the app was, in fact, unable to perform its primary responsibility.

That is the exact shape of failure a heartbeat is supposed to make visible — and the reason a *naive* heartbeat is a trap (§1.3).

### 1.2 Consequences

- **Loupe blind spot.** The system-map / health rollup (`cmd/loupe/health.go`, `computeHealth`) shows every daemon but omits the two apps entirely — an operator cannot tell a running-but-broken app from a not-deployed one.
- **Lamplighter blind spot.** `agents/lamplighter/SKILL.md` reads Health KV to classify anomalies; it has no signal for the vertical tier at all.
- **Silent-degradation class.** Every app dependency that is *non-fatal at startup* (bootstrap actor, NATS dial, protected-read-model Postgres pool, JWT/auth posture — all explicitly "serve anyway, report per-request" in `main.go`) is invisible in aggregate. The app can be 100% unable to submit ops and still look alive.

### 1.3 The trap: a liveness ping is worse than nothing here

The closest precedent, `object-store-manager`'s `emitHeartbeat` (`internal/objectmanager/manager.go:294`), writes a **static** doc: `{"component","instance","status":"healthy","updatedAt":now}` — no dependency probe, no `issues[]`, no TTL. If the vertical apps mirrored *that*, they would have reported **healthy** all through the 2026-07-01 bootstrap failure — false-green, the one outcome Contract #5 §5.3 explicitly warns against ("err on the side of being honest about degradation rather than reporting false-healthy").

So the design's real content is not "emit a heartbeat" — it is **"probe the app's own dependencies each tick and report honest status + issues."** That is what makes this ★★ rather than boilerplate.

### 1.4 Vision tie

- **Brainstorming #90/#91** — *Health-as-KV* + a *component heartbeat library* (`status`, `error_rate`, `latency_p99`). This design is the first move toward that library: a reusable reporter, not a sixth hand-rolled heartbeater.
- **Brainstorming #96** — the *Closed-loop Weaver auditor* (reads Health-KV, issues remediation Nudges); the Lamplighter is its dev-loop precursor. The vertical tier must be *in* Health KV for that loop to ever cover it.

---

## 2. Grounding — the pattern this mirrors

Everything reused already exists; nothing greenfield.

| Building block | Where | Status |
|---|---|---|
| Direct Health-KV heartbeat from a non-engine daemon | `internal/objectmanager/manager.go:278-305` (`heartbeatLoop` + `emitHeartbeat`, `HealthKVBucket`-gated goroutine) | **Shipped.** The structural precedent (a simple daemon, no consumer machinery) — but static; this design adds the probe + compliance it lacks. |
| Contract #5 §5.2 document + §5.3 status + §5.5 issues | `docs/contracts/05-health-kv.md` | **Frozen.** The apps emit to it; no change. |
| Reader that renders any `health.<component>.<instance>` key | `cmd/loupe/health.go` (`classifyHealthKey`, `componentLiveness`, `computeHealth`) | **Shipped.** Already handles arbitrary components uniformly, keys on `status` + `issues[]`, computes freshness itself. **Zero reader change.** |
| `Conn.KVPutWithTTL(ctx, bucket, key, value, ttl)` | `internal/substrate/kv.go:331` | **Exists.** `ttl<=0` → plain `KVPut`. `health-kv` bucket already TTL-enabled (`internal/bootstrap/primordial.go`). |
| `substrate.NewNanoID()` + `<prefix>-<NanoID>` instance ids | `internal/substrate/derive.go`; every `cmd/*/main.go` (`objmgr-`, `loom-`, `proc-`…) | **Shipped.** The instance-id convention (Contract #5 §5.1). |
| Sibling TTL-conformance design | `_bmad-output/implementation-artifacts/health-kv-ttl-orphan-expiry-design.md` | 📐 awaiting-Andrew. This design aligns with it (write with TTL from birth). |

**Read path (P5):** the apps do not *read* Health KV; they only write their own key. P5's "apps read lens projections, not Core KV" is untouched — Health KV is a separate plane, not Core KV, and this is a write.
**Write path (P2):** direct Health-KV writes are the sanctioned P2 exception (Contract #5 §5.7 — "the only sanctioned direct-KV writes outside Refractor's own lens targets are Health KV"). No Processor op, no lens. **No Contract #1 key shapes** are involved (Health keys are their own addressing space, §5.1).

---

## 3. The component & instance naming

Following the `object-store-manager` precedent (hyphenated component, no dots — accepted by `classifyHealthKey`, `cmd/loupe/health.go:56-58`):

| App | `<component>` | `<instance>` prefix | Health key |
|---|---|---|---|
| `cmd/loftspace-app` | `loftspace-app` | `loft-` | `health.loftspace-app.loft-<NanoID>` |
| `cmd/clinic-app` | `clinic-app` | `clinic-` | `health.clinic-app.clinic-<NanoID>` |

`<NanoID>` is `substrate.NewNanoID()` at startup (alphanumeric — never contains a `.`, so `classifyHealthKey` sees a clean two-segment key → `kindComponent`). Overridable via `LOFTSPACE_APP_INSTANCE` / `CLINIC_APP_INSTANCE` (mirrors `OBJMGR_INSTANCE`).

---

## 4. The shape — a thin shared reporter (`internal/healthkv`)

### 4.1 Why shared, not copy-paste (the design choice)

The two apps are structurally identical: auth-less admin HTTP servers with the *same* dependency set (bootstrap admin actor, NATS conn, optional protected-read-model Postgres pool, optional auth posture). Their heartbeat envelope, cadence, TTL, and NanoID handling are byte-for-byte the same; only the *probe* differs, and even that only in which deps exist.

I considered three options:

- **(A) Copy `object-store-manager`'s static heartbeat into each app.** Rejected: perpetuates the false-green trap (§1.3) and the non-compliance (no `issues[]`, no TTL), in two more places.
- **(B) A per-app hand-rolled `health.go`** (mirror the engines' `internal/<component>/health.go`). This is the "mirror the decomposed pattern" instinct — but the engines' health files carry consumer-state/issue caches *because they run JetStream consumers*. The apps have none of that; a per-app health.go would be two near-identical copies of the same envelope+cadence+TTL boilerplate, and the §5.6 TTL semantics would live in two more places for the sibling TTL design to chase.
- **(C, chosen) A thin `internal/healthkv` reporter** that owns exactly the Contract #5 envelope + §5.6 cadence/TTL loop + NanoID, and takes a caller-supplied `Probe` snapshot. Each app supplies a small probe of *its own* deps. Immediate consumers (2) both exist, so it is not dead scaffolding; and `object-store-manager` is a natural third consumer that would *gain* compliance (optional Fire 3).

This is not greenfielding a parallel pattern: the reporter **is** the `heartbeater` loop the engines already have, minus the consumer machinery they need and the apps don't. The rich engine heartbeaters stay exactly as they are (they legitimately differ); this design does **not** touch them.

`internal/healthkv` already exists as a namespace (currently only `completeness_test.go`, `package healthkv_test`). The reporter lands as `package healthkv` alongside it — the natural home.

### 4.2 The reporter API

```go
// Package healthkv provides a Contract #5 heartbeat reporter for simple
// (consumer-less) daemons — vertical apps and service daemons that self-report
// liveness + dependency health to the Health KV plane.
package healthkv

// Status mirrors Contract #5 §5.3.
type Status string

const (
    StatusStarting     Status = "starting"
    StatusHealthy      Status = "healthy"
    StatusDegraded     Status = "degraded"
    StatusUnhealthy    Status = "unhealthy"
    StatusShuttingDown Status = "shuttingDown"
)

// Issue mirrors Contract #5 §5.5. Since is filled by the reporter (first-seen
// tracking by Code) — the probe supplies Code/Severity/Message only.
type Issue struct {
    Code     string `json:"code"`
    Severity string `json:"severity"` // "warning" | "error"
    Message  string `json:"message"`
    Since    string `json:"since,omitempty"`
}

// Snapshot is what a Probe returns each tick.
type Snapshot struct {
    Status  Status
    Issues  []Issue
    Metrics map[string]any // optional §5.4 counters/gauges; may be nil
}

// Probe reports the caller's current health. Called once per heartbeat with a
// short per-tick context. It MUST be non-blocking-ish (bounded by the reporter's
// probeTimeout) and MUST NOT panic; a panic is recovered and reported as an
// unhealthy "HealthProbePanicked" issue.
type Probe func(ctx context.Context) Snapshot

type Config struct {
    Conn      *substrate.Conn
    Bucket    string        // bootstrap.HealthKVBucket
    Component string        // "loftspace-app"
    Instance  string        // "loft-<NanoID>"
    Interval  time.Duration // default 10s (§5.6)
    TTL       time.Duration // default Interval*10 (§5.6); 0 disables (fallback to KVPut)
    Probe     Probe
    Logger    *slog.Logger
    now       func() time.Time // injectable clock for tests
}

// Reporter runs the heartbeat loop.
type Reporter struct { /* … */ }

func New(cfg Config) *Reporter          // applies defaults, records startedAt
func (r *Reporter) Run(ctx context.Context) // emit-now, then tick; final shuttingDown write on ctx.Done
```

`Run` mirrors `objectmanager.heartbeatLoop` exactly (immediate first emit, then `time.Ticker`, `ctx.Done` exit) **plus**: (a) a `starting` first beat before the first probe, (b) a final `shuttingDown` beat on ctx cancel (Contract #5 §5.8 step 7), (c) `Since` continuity — the reporter remembers when each `Code` first appeared and re-stamps it across ticks (Contract #5 §5.5 "persists across heartbeats while the issue continues").

### 4.3 The emitted document (§5.2 conformant)

```json
{
  "key": "health.loftspace-app.loft-Lk2Pn6mQ",
  "component": "loftspace-app",
  "instance": "loft-Lk2Pn6mQ",
  "version": "1.0",
  "status": "degraded",
  "heartbeatAt": "2026-07-01T14:32:18.142Z",
  "startedAt": "2026-07-01T14:17:00.000Z",
  "uptime": "PT15M18S",
  "metrics": { "ops_submitted_total": 12, "ops_failed_total": 1 },
  "issues": [
    { "code": "AdminActorUnconfigured", "severity": "error",
      "message": "bootstrap.json not loaded (version mismatch?); apply/sign will 400",
      "since": "2026-07-01T14:17:00.000Z" }
  ]
}
```

It uses `heartbeatAt` (not `object-store-manager`'s non-standard `updatedAt`) — Loupe reads either (`componentHeartbeat`, `cmd/loupe/health.go:88`), but `heartbeatAt` is the §5.2 field.

### 4.4 The per-app probe

Each app supplies a `Probe` closure over the state its `run()` already computes. The probe **re-checks liveness cheaply each tick** — it does not merely echo a boot-time snapshot (that would miss a NATS drop or a Postgres recovery).

**loftspace-app / clinic-app probe (shared logic, per-app dep list):**

| Check | Signal | Result |
|---|---|---|
| Admin actor configured | `adminActor == ""` (bootstrap load failed) | `error` `AdminActorUnconfigured` → **unhealthy** (primary responsibility — submitting ops — is dead) |
| NATS reachable | `conn == nil` OR `conn.NATS().IsConnected()` false | `error` `NatsUnreachable` → **unhealthy** |
| Protected read model reachable *(if configured)* | `pgPool.Ping(ctx)` fails | `warning` `ReadModelUnreachable` → **degraded** (reads 502, writes still work) |
| Auth posture present *(read boundary)* | `authn == nil` | `warning` `NoAuthPosture` → **degraded** (protected reads 401) |

Status is the worst of the checks (`unhealthy` > `degraded` > `healthy`); `issues[]` carries every failing check. This is honest: the 2026-07-01 failure now surfaces as a **red** `loftspace-app` card in Loupe with `[error] AdminActorUnconfigured` — exactly the missing signal.

`substrate.Conn` already exposes `NATS() *nats.Conn` (`internal/substrate/conn.go:120`), so the probe calls `conn.NATS().IsConnected()` directly — **no new substrate seam**. NATS's `IsConnected` is authoritative for "currently connected" (reconnect runs in the background per the app's `MaxReconnects:-1`).

Metrics are **optional** and thin at v1 (`ops_submitted_total`, `ops_failed_total` if the server already counts them; omit otherwise — §5.4 metrics are recommended, not required, and Loupe does not key on them). Not gating.

---

## 5. Contract & config surface

### 5.1 Frozen contracts — no change

Contract #5 is emitted-to as written. The two apps join the `<component>` value space (§5.1 lists "Phase 1: processor, refractor; Phase 2+: loom, weaver, gateway" as *examples*, not a closed enum — `object-store-manager` already added itself without a contract edit). **No `docs/contracts/*` edit is staged.**

### 5.2 Non-frozen schema doc — one edit (committed with the build fire, not here)

`docs/observability/health-kv-schema.md` (the explicit authority for per-component key details) gains two rows: `health.loftspace-app.<instance>` and `health.clinic-app.<instance>`, with their issue codes (`AdminActorUnconfigured`, `NatsUnreachable`, `ReadModelUnreachable`, `NoAuthPosture`). This is a **non-frozen** doc; the Steward edits it in the build fire (not in this design fire).

### 5.3 App config (env)

Mirrors `OBJMGR_*`. Both apps degrade gracefully if unset (heartbeat simply disabled when the bucket is empty — same gate as `object-store-manager`'s `HealthKVBucket != ""`):

- `LOFTSPACE_APP_INSTANCE` / `CLINIC_APP_INSTANCE` — instance id (default auto `<prefix>-<NanoID>`).
- Heartbeat bucket = `bootstrap.HealthKVBucket` (no new env — the apps already `bootstrap.Load`).
- Optional `LOFTSPACE_APP_HEARTBEAT_EVERY` / `CLINIC_APP_HEARTBEAT_EVERY` (default 10s) — parity with other components' configurable cadence.

### 5.4 TTL posture

The reporter writes via `KVPutWithTTL` with `TTL = Interval*10` (§5.6 default, 100s). This makes the apps born-conformant and aligns with the sibling *health-kv-ttl-orphan-expiry* design — the apps will **not** be two more no-TTL writers it has to retrofit. If Andrew prefers to defer all TTL to that design, set `TTL:0` (falls back to `KVPut`, no worse than incumbents) — a one-field change. Recommendation: **keep the TTL** (§ For Andrew).

---

## 6. Wiring (per app `main.go`)

Minimal, mirrors `object-store-manager`'s `HealthKVBucket`-gated goroutine. After the `server` is constructed and before/alongside `httpServer.ListenAndServe`:

```go
if conn != nil { // heartbeat only when NATS was dialed
    instance := envOrDefault("LOFTSPACE_APP_INSTANCE", "loft-"+mustNanoID())
    reporter := healthkv.New(healthkv.Config{
        Conn:      conn,
        Bucket:    bootstrap.HealthKVBucket,
        Component: "loftspace-app",
        Instance:  instance,
        Probe:     srv.healthProbe, // closes over adminActor, conn, pgPool, authn
        Logger:    logger,
    })
    go reporter.Run(ctx) // ctx from signal.NotifyContext — final shuttingDown beat on shutdown
}
```

`srv.healthProbe(ctx) healthkv.Snapshot` lives beside the server (e.g. `health.go` in each app package) and reads the fields the server already holds. Note: even when `conn == nil` at boot (NATS down), the app still serves; the heartbeat is simply absent until a restart with NATS up — acceptable (an absent card is itself a signal, and reconnect-from-nil is out of scope; the incumbent `object-store-manager` has the same boot-gate).

---

## 7. Test strategy

Unit (no live NATS — the reporter takes a `*substrate.Conn` but the probe/envelope logic is pure):

- **`internal/healthkv` reporter:**
  - envelope shape: `New` records `startedAt`; a tick produces a §5.2-conformant doc (all required fields, `heartbeatAt`/`uptime` computed from the injected clock).
  - `Since` continuity: an issue with the same `Code` across two ticks keeps its first-seen `Since`; a resolved issue drops out; a re-appearing code gets a fresh `Since`.
  - status precedence: worst-of severities; a `starting` first beat; a `shuttingDown` final beat on ctx cancel.
  - TTL: `KVPutWithTTL` is called with `Interval*10` (assert via a fake `Conn` seam or the existing substrate test harness); `TTL:0` falls back to `KVPut`.
  - probe panic → recovered, emitted as `unhealthy` `HealthProbePanicked` (never crashes the app).
- **per-app `healthProbe`:** table test over the dep matrix (§4.4) — unconfigured actor → unhealthy+`AdminActorUnconfigured`; nil conn → unhealthy; ping-fail pool → degraded; nil authn → degraded; all-good → healthy+empty issues. Uses the same fakes the apps' existing `*_test.go` use (there is already a rich test suite per app).
- **Loupe render — moved to the Loupe lane (2026-07-02, Loupe 2.0):** no `cmd/loupe` test in these fires. The F4 Health-absorption redesign owns how `health.*` keys render; this design's contract with the reader is the §5.2 document, pinned here by an `internal/healthkv` envelope-conformance assertion (all required §5.2 fields + `heartbeatAt`). The Loupe lane verifies the two new components render on its own surface.

No new integration test is required for Fire 1–2; an optional live check can extend `internal/healthkv/completeness_test.go` (integration-tagged) to assert the two app keys appear when the apps run — deferred, not gating (the apps aren't part of the current completeness stack fixture).

`make vet` / `golangci-lint` / `go build ./...` / the touched `go test` packages are the gate.

---

## 8. Risks & alternatives

- **False-green risk (the whole point).** Mitigated by making the probe re-check deps each tick rather than echo a boot snapshot. Explicitly *not* mirroring `object-store-manager`'s static doc.
- **Probe cost / blocking.** The `pgPool.Ping` is the only I/O in the probe; it runs on a bounded per-tick context (default a small fraction of the 10s interval). NATS/actor/auth checks are in-memory field reads. A slow probe degrades cadence, not correctness; a panicking probe is recovered.
- **Over-abstraction risk.** Bounded by keeping the reporter to *exactly* the envelope+cadence+TTL+NanoID the apps share, and explicitly **not** touching the engine heartbeaters or trying to model their consumer-state caches. Two real consumers exist at Fire 2; no speculative generality.
- **Rejected — extract-and-migrate-everything.** Refactoring the five engine heartbeaters onto the shared reporter is *out of scope* (they have genuine consumer-machinery differences; a forced unification is the greenfield-monolith blind spot). `object-store-manager` — the one true structural match — is an *optional* Fire 3, not a gate.
- **Rejected — a Refractor lens over Health KV.** Health KV is deliberately *not* lens-projected at this phase (Contract #5 §5.7); Loupe/Lamplighter read it directly. No lens.

---

## 9. Fire-by-fire decomposition (for the Lattice Steward)

Each fire is independently shippable and green; each realizes value on its own (an app becomes Loupe-visible).

- **Fire 1 — reporter + loftspace-app (end-to-end proof).**
  Add `package healthkv` reporter (`internal/healthkv/reporter.go`) + unit tests. Add `cmd/loftspace-app` `healthProbe` (uses the existing `conn.NATS().IsConnected()`) + wire `reporter.Run` in `main.go`. **Cross-lane note (2026-07-02, Loupe 2.0):** `cmd/loupe/**` is its own lane now and the F4 "Health absorption" redesign retires the current Health tab — so the Loupe render test case is **dropped from this fire** (a test pinned to the old card rendering is churn). This design's interface to Loupe is the Contract #5 §5.2 document alone; Fire 1 instead adds an `internal/healthkv` envelope-conformance assertion, and the Loupe lane's F4 absorbs the two new `health.<app>.*` keys uniformly like every other component (its verification lives there). Schema-doc row for `loftspace-app`. **Value:** loftspace-app self-reports; the 2026-07-01 failure class is now visible in Loupe (2.0's alert strip/gates panel). Bundling the package with its first consumer avoids dead scaffolding.
- **Fire 2 — clinic-app (mirror).**
  Add `cmd/clinic-app` `healthProbe` + wire it (identical shape; dep list per that app's `main.go`/`server.go`). Schema-doc row for `clinic-app`. **Value:** clinic-app self-reports. Pure mirror of Fire 1's consumer half.
- **Fire 3 (optional) — object-store-manager adoption.**
  Replace `objectmanager`'s static `emitHeartbeat` with a `healthkv.Reporter` + a real probe (Core-KV reachable? cascade actor configured?), gaining §5.2 compliance + `issues[]` + TTL it currently lacks. **Value:** fixes the incumbent's false-green + validates the abstraction with a third consumer. Not gating; a clean-up dividend.

Fires 1–2 are the item; Fire 3 is a bonus the abstraction makes cheap.

---

## 10. Adversarial self-review (discharged 2026-07-01)

Ran a focused adversarial pass (proportionate to an S-sized, single-plane design; no cross-cutting fork, no contract change — a full 3-layer/party review is not warranted). Findings folded in:

- **"A heartbeat that always says healthy is the bug, not the fix."** — Caught pre-draft; it is now the design's thesis (§1.3, §8). The probe re-checks each tick.
- **"`object-store-manager` uses `updatedAt`; will Loupe render the apps?"** — Verified `componentHeartbeat` (`cmd/loupe/health.go:88`) tries both; the apps use the §5.2-correct `heartbeatAt`. A render test case pins it.
- **"Does `classifyHealthKey` accept a hyphenated component?"** — Yes; `object-store-manager` already proves it (`cmd/loupe/health_test.go:19`). NanoID has no dots, so the two-segment cut is clean.
- **"Is a new `internal/healthkv` package greenfield / against mirror-the-pattern?"** — It **is** the engine `heartbeater` loop minus consumer machinery; two immediate consumers; engines untouched. Reconciled in §4.1 (option B rejected with reasoning).
- **"TTL now, or wait for the sibling design?"** — Write with TTL from birth (§5.4); it's the correct §5.6 behavior, costs nothing, and avoids being a retrofit target. Surfaced as the one judgment call for Andrew (not a fork).
- **"Boot-gated on `conn != nil` — a NATS-down boot never heartbeats."** — Acknowledged (§6); matches the incumbent `object-store-manager` gate; reconnect-from-nil is a separate, broader concern (out of scope). An *absent* card is itself an operator signal.
- **"P5 violation? apps touching KV directly."** — No; Health KV is not Core KV, this is the sanctioned P2 write-plane exception, and it's a write not a read (§2).

---

## 11. Definition of done

- `internal/healthkv.Reporter` emits a §5.2-conformant, TTL'd, dependency-probing heartbeat; unit-tested (envelope, `Since` continuity, status precedence, TTL, probe-panic recovery).
- `loftspace-app` and `clinic-app` each self-report to `health.<app>.<instance>`, honestly reflecting admin-actor / NATS / read-model / auth-posture health; a broken dep shows a `degraded`/`unhealthy` card with `issues[]` in Loupe.
- `docs/observability/health-kv-schema.md` lists both components + their issue codes.
- No frozen-contract change; no reader code change; all gates green.
