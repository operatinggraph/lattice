# Facet host health emission (`health.facet.<instance>`) — design

**Status: 📐 awaiting-Andrew (ratification)** · Designer fire 2026-07-21 · lane row: backlog/lattice.md → Component maintenance → *[Refractor/Facet] Facet host health emission*

## For Andrew

The Facet host process (`cmd/facet`) starts emitting a Contract #5 heartbeat
(`health.facet.<instance>`) so a crash-looping per-user sync engine — today FE-visible only via the
syncDegraded frame — becomes visible to the Lamplighter / `lattice health` rollup. It mirrors the
vertical apps' `healthkv.Reporter` wiring exactly; the per-identity engine plane is untouched.

**Trust-boundary fork (the row's "host service credential vs Gateway-mediated vs stays-FE-only" —
your call):** my recommendation is **Option A2** — give the host its own platform NKey user
(`facet`) that is the **narrowest row in the matrix** (publish: `$KV.health-kv.>` + `$JS.FC.>` only
— no `$JS.API.>`, no `ops.>`), *and* add the small per-component `subscribe` allow so this one
user's subscribe side is pinned to `_INBOX.>` instead of today's uniform `">"`. §5 lays out A1/A2/B/C
with trade-offs. **No frozen-contract change** — this builds to Contract #5 as written (§6).

## 1. Problem + intent

`ce050a7` (2026-07-19) closed the *user-facing* half of a silent per-user sync wedge: `runSyncLoop`
marks a sticky `syncDegraded` bit on the engine's feed, the FE shows a banner, and the browser-host
frame has parity. The *operator-facing* half was re-filed as this row: a Facet engine that
crash-loops its sync manager is invisible to the platform's observability plane, because `cmd/facet`
holds **no platform NATS credential by documented design** (`cmd/facet/main.go`'s "No Health-KV
reporting" block): every NATS connection the process opens is a **per-identity** engine connection,
confined by `internal/gateway/natsauth`'s issued permission set to exactly
`lattice.sync.user.<U>` + its own `_INBOX.edge.<U>.>` + the `personal.*` control RPCs. Publishing to
health-kv on those connections is a permissions violation, not a missing grant to request — and
widening the *per-identity* grant is the one move this design explicitly rules out (§8.1: any edge
user could then spoof platform health).

Named consumer (the build-over-defer test): the **Lamplighter** / `lattice health summary` rollup.
Since `3a5cd35`, `classifyKey` classifies `health.<component>.<instance>` keys **structurally**, so a
new `facet` component group appears in the rollup with **zero reader changes** — the demand side is
already built; only the emission is missing. Vision anchor: Health KV is the operational
observability plane every running component writes to (Contract #5 §5.7 "every component writes its
own heartbeat"); the Facet host is the one running platform process that doesn't.

## 2. Grounding — the existing pattern this mirrors

- **Emitter:** `internal/healthkv.Reporter` — the shared Contract #5 heartbeat loop for
  consumer-less daemons (probe each tick, never echo a boot snapshot; interval-derived TTL per §5.6;
  `starting`/`shuttingDown` lifecycle emissions; panic-safe probe). Already used by all four vertical
  apps + the Gateway (`dcfe4af`).
- **Probe:** `cmd/loftspace-app/health.go` — dependency-probing (`NatsUnreachable`,
  `ReadModelUnreachable`, auth-posture), severity → status fold. Facet's probe is the same shape
  plus the engine-fleet aggregates (§4.3).
- **Credential:** `internal/natsperm.Matrix` — one NKey user per `cmd/<name>` binary;
  registry-derived allow/deny (`health-kv` is `SharedWrite`, so any matrix row picks up
  `$KV.health-kv.>` automatically); `deploy/gen-dev-nkeys` renders the committed
  `deploy/nats-server.conf`, with `TestConfMatchesMatrix` as the drift gate.
- **Write mechanics (guard named precisely):** the Reporter's only substrate call is
  `Conn.KVPutWithTTL` — a bare `js.PublishMsg` to `$KV.health-kv.<key>` with a `Nats-TTL` header
  (per-key message TTL, NATS ADR-48, in-pin since 2.11; our pin is **NATS 2.14**, `docs/vendors.md`).
  It opens **no KV handle** (no `$JS.API.STREAM.INFO`), and the write is an **unconditional
  overwrite** — correct here by construction, because the key embeds the instance's own boot-minted
  NanoID, so the sole writer to `health.facet.<instance>` is that instance (§5.7); LWW is the
  contract's own §5.6 semantics, not an unguarded hazard.
- **Modes:** `up-facet` (host-engine mode: `engineManager` multiplexes one in-process engine per
  signed-in identity) and `up-facet-edge` (browser-native, `FACET_BROWSER_ENGINE=1`: the host is a
  static file server; engines run in-page over the :9222 WebSocket). The probe must be honest in
  both (§4.3).

Parallel-design check: no other 📐/🏗️ design touches `health.facet`, the natsperm matrix, or the
Reporter (grepped `_bmad-output/implementation-artifacts/` 2026-07-21).

## 3. The trust split (why a host credential is posture-consistent)

The row calls this a trust-boundary call, so name the boundary: **`cmd/facet` the HOST is
server-side infrastructure; `cmd/edge` the NODE is user-owned.** The host already holds
server-side trust artifacts that dwarf a health-kv publish grant: the dev/demo **signing key** (mints
identity JWTs for the login flow), the `FACET_PG_DSN` Postgres role for the
`identityCredentialsRead` Protected lens, and every user's session cookie. Adding a
**publish-only-to-health-kv** platform credential does not move the host across any line it isn't
already on. `cmd/edge` is on the other side of the line and **stays credential-less** — it is the
sovereign per-user device binary; "cmd/edge has never reported health for the same structural
reason" remains true and correct after this design.

The two planes stay strictly separate inside the process: the health connection is a second,
host-level `substrate.Connect` (NKey, listed in `auth_users`, bypasses the callout), used by the
Reporter and nothing else; the per-identity engine connections keep their natsauth-confined
credentials, unchanged.

## 4. The shape

### 4.1 natsperm matrix row

```go
{
    Name: "facet",
    Desc: "edge showcase app host — health-plane-only platform credential; " +
        "per-identity engine traffic stays on the natsauth callout connections",
    // No ExtraPubAllow at all: Allow() derives $KV.health-kv.> (SharedWrite)
    // + $JS.FC.>; KVPutWithTTL needs nothing else (no $JS.API.>, no KV-handle
    // open). The narrowest user in the matrix, by design — this host fronts
    // the hosted demo.
    SubscribeAllow: []string{"_INBOX.>"}, // A2 — see §5; drop this field under A1
},
```

Registry-derived denies apply as to every row (publish denies on all non-shared platform buckets +
stream-admin denies). Fail-closed boundary worth stating: with no `$JS.API.>`, even a *plain*
`KVPut` (which opens a KV handle via `STREAM.INFO`) is **denied** — and that is correct, because a
TTL-less heartbeat is exactly the leaking-key bug `dcfe4af` just fixed. The credential can do one
thing: TTL'd heartbeat writes into health-kv.

### 4.2 The `SubscribeAllow` field (A2's one mechanism addition)

Today `RenderConf` renders a uniform `subscribe { allow: [">"] }` for every user — the matrix's
deliberate "gates writes only; reads are unrestricted" stance, adopted for trusted platform daemons.
A2 adds an optional `SubscribeAllow []string` to `Component`: nil renders `[">"]` (every existing
row is byte-identical), non-nil renders the given list. Only `facet` sets it, to `_INBOX.>` (the
pub-ack reply inbox `js.PublishMsg` awaits — its only subscribe need). `TestConfMatchesMatrix`
regenerates and diffs as usual. This is not dead scaffolding: it ships in the same fire as its
consumer, and it is the security itself, not a stub of it.

### 4.3 The probe (`cmd/facet/health.go`)

Same fold as `loftspace-app` (any `error` issue ⇒ `unhealthy`, else any `warning` ⇒ `degraded`):

- `NatsUnreachable` (error) — the host health connection is down (per-identity engines may still be
  fine; this is the host's own dependency signal, same as every other component's).
- `EngineSyncDegraded` (warning) — N in-process engines currently hold the sticky
  `syncDegraded` bit (`feed.connectivityState()`), i.e. their sync manager is in restart-backoff.
  **This is the row's demanded signal** — the crash-looping engine, now operator-visible. Count
  only, never identity ids (§8.2).
- `EngineNatsDisconnected` (warning) — N engines whose per-identity NATS connection is currently
  down (`connected == false`). Distinct code: connectivity ≠ sync-loop crash-looping (the two axes
  `ce050a7` deliberately separated).
- `ReadModelUnreachable` (warning) — `FACET_PG_DSN` configured but the pool ping fails (mirrors
  loftspace verbatim; unset DSN is a posture, not an issue).

Metrics: `mode` (`host-engine` | `browser-native`), `engines_active`, `engines_pinned`,
`engines_sync_degraded`, `engines_nats_disconnected`. In **browser-native mode** the engine numbers
are honestly `0`/absent — the engines live in browsers, invisible to the host by design; the probe
reports the host's own serving posture (assets resolvable, WS URL configured) and does **not**
fabricate fleet health it cannot see (§7 lists platform-side visibility of browser-native sync as an
explicit non-goal, with the Gateway/natsauth callout named as the only honest future vantage point).

Supporting accessor: `engineManager` gains a read-only `healthSnapshot()` (under `mu`: total/pinned
counts + each entry's `fd.connectivityState()` and `eng.conn.NATS().IsConnected()`). No behavior
change to acquire/release/reap.

### 4.4 Wiring

- `main.go`: if a host credential is configured (`NATS_NKEY` / `NATS_CREDS`), open the host
  `substrate.Connect` and start `healthkv.New(...)` with `Component: "facet"`, `Instance:
  envOrDefault("FACET_INSTANCE", "facet-"+NanoID)`, default 10s interval (env-overridable,
  mirroring `LOFTSPACE_APP_HEARTBEAT_EVERY`). Unconfigured ⇒ one warn log, no reporter — the absent
  card is itself the operator signal (loftspace's "gated on a live NATS dial" posture), and older
  launchers keep working unchanged. The "No Health-KV reporting" doc block is **rewritten** to
  describe the two-plane posture as it now is (no history narration).
- Launchers: `Makefile` gains `NKEY_FACET ?= $(NKEY_DIR)/facet.nk`; `up-facet`, `up-facet-edge` (and
  `run-facet`) pass `NATS_NKEY=$(NKEY_FACET)`; the hosted-demo launcher `deploy/demo/demo-up.sh`
  gains the same env.
- `deploy/gen-dev-nkeys` run once: mints `deploy/nkeys/facet.nk`, regenerates
  `deploy/nats-server.conf` (committed). Live stacks need `docker restart lattice-nats` to pick the
  conf up (the SIGHUP bind-mount gotcha — never `--force-recreate`/`make down` on the shared stack).

## 5. The fork, designed through

- **A1 — host NKey row, uniform subscribe (the pure vertical-app mirror).** Zero new mechanism.
  Residual: the credential inherits the account posture of unrestricted subscribe, so a compromised
  Facet host could passively subscribe `$KV.core-kv.>` live traffic (as any vertical app's
  credential could today).
- **A2 (recommended) — A1 + the `SubscribeAllow` pin.** ~10 lines in natsperm + a regen. The Facet
  host is the first matrix user that fronts a public deployment (the F20 hosted demo); its
  credential becomes inert beyond health emission: publish `{$JS.FC.>, $KV.health-kv.>}`, subscribe
  `{_INBOX.>}`. Cost: one new optional axis in the matrix. I recommend A2 — it is structural
  fail-closed at the exact boundary the hosted demo exposes, and it is severable: ratify A1 and
  strike §4.2 if you judge the axis premature.
- **B — Gateway-mediated emission** (host POSTs snapshots; Gateway writes the key). Rejected: breaks
  §5.7 ("no component writes to another component's health entry"), needs a new authenticated
  Gateway surface (unauthenticated ⇒ anyone can forge `health.facet.*`; authenticated ⇒ a new
  shared-secret custody story that is *more* credential machinery than A, not less), and buys
  nothing A doesn't — the host is already trusted server-side infrastructure (§3).
- **C — stays FE-only** (status quo). Rejected by the build-over-defer test: the consumer is named
  and already shipped (Lamplighter/rollup, structural `classifyKey`), and the exact failure this
  leaves invisible (crash-looping sync engine) is the one `ce050a7` proved real.

## 6. Contract surface

**Build-to only; no frozen-contract edit.** Contract #5 is an explicitly soft convention; §5.1's
component list is already informally extended (gateway, bridge, the four vertical apps all emit
today). `facet` satisfies the component-name rule (lowercase, no dots), the §5.2 document shape
(Reporter-emitted), §5.6 cadence/TTL (interval-derived, `DefaultTTLMultiplier`), and §5.7
self-report-only. Health KV direct writes are the sanctioned P2 exception (CLAUDE.md; Contract #5
§5.1 note). Contract #1 key shapes are untouched (Health KV is its own addressing space).

## 7. Reconciliation + non-goals

- *Didn't `ce050a7` already fix this?* It fixed the **user-facing** axis (FE banner + frame parity)
  and deliberately re-filed this operator-facing half as needs-design — the fork below was not its
  scope. The sticky bit it added is exactly what §4.3's probe now aggregates; nothing is duplicated.
- *Didn't we decide the Facet host holds no platform credential?* The documented posture was about
  the **per-identity engine plane**, and it stands (§3, §8.1). What changes is the recognition that
  the host is server-side infrastructure that already holds stronger trust artifacts; the health
  credential is additive on the host plane only. `cmd/edge` (the row's flagged sibling) is on the
  user side of the line and stays as-is.
- *Reader/rollup changes?* None — `classifyKey` is structural since `3a5cd35`; `facet` buckets as
  `component-heartbeat` automatically. `docs/observability/health-kv-schema.md` gains a facet
  section (doc truth-up, part of the fire).
- *Completeness test?* `internal/healthkv/completeness_test.go` asserts the **base** stack
  (`make up`); Facet is an opt-in overlay (`up-facet`) and is **not** added to the required-keys
  set. Its own proof lives in `cmd/facet` tests (§9).
- **Non-goals:** platform-side visibility of **browser-native** per-user sync (the host cannot see
  in-page engines; the only honest vantage point would be Gateway/natsauth connection telemetry —
  file separately if ever demanded); any widening of natsauth's per-identity grants; per-identity
  detail in health documents.

## 8. Risks + adversarial pass (run 2026-07-21, findings folded in)

1. **Spoofing via the edge plane** — could a per-identity connection write `health.facet.*`?
   No: natsauth issues an exact allow-list (`lattice.sync.user.<U>`, own inbox, `personal.*`);
   `$KV.health-kv.>` is not in it and never becomes so under this design. This was the checked-and-
   rejected alternative transport (widening per-identity grants), kept rejected.
2. **Privacy of a broadly-readable bucket** (§5.7: any NATS-cluster actor reads Health KV) — the
   probe emits **aggregate counts only**; no identity NanoIDs, no per-engine keys. Caught in this
   pass and pinned as a test vector (§9): the marshaled heartbeat must contain no engine identity id.
3. **Credential blast radius on the hosted demo** — driven to the A1/A2 fork (§5); A2 pins both
   sides.
4. **False-healthy** — the probe re-checks per tick (Reporter's anti-boot-snapshot posture is the
   package's own doc'd invariant); the sticky syncDegraded bit is cleared only by
   `OnRunEstablished`, so a crash-loop cannot flap to healthy between ticks.
5. **TTL semantics** — interval-derived TTL via `KVPutWithTTL` (never bare `KVPut` — which the A-row
   credential structurally cannot perform, turning the `dcfe4af` class of leak into a hard failure
   rather than a silent one).
6. **Second connection cost** — one idle NKey connection + one 10s publish; negligible, and the
   pattern every vertical app already carries.

## 9. Test strategy

- `cmd/facet/health_test.go` (mirrors `cafe-app`/`loftspace-app`): probe folds — healthy;
  N-degraded-engines ⇒ `degraded` + `EngineSyncDegraded`; host-NATS-down ⇒ `unhealthy`;
  browser-native mode ⇒ mode metric + no fabricated engine counts; **no identity id appears in the
  marshaled document** (finding #2).
- `internal/natsperm`: `TestConfMatchesMatrix` regen; a facet permission vector (can publish
  `$KV.health-kv.>`; cannot publish `ops.>` / `$KV.core-kv.>` / `$JS.API.STREAM.INFO.*`; under A2,
  subscribe limited to `_INBOX.>`) — mirroring the existing per-component vector style.
- Live check (Steward's admit): `make up-facet`, sign in a showcase tenant, `lattice health summary`
  shows a green `facet` row; stop NATS-side sync (or use the harness fake) and watch
  `EngineSyncDegraded` surface.

## 10. Migration / compatibility

Purely additive. Old launchers without `NATS_NKEY` run exactly as today (warn + no card). The conf
regen is committed; live dev stacks need the one-time `docker restart lattice-nats`. No package DDL,
no lens, no bootstrap change; `verify-kernel` unaffected.

## 11. Decomposition for the Steward

**One fire** (S–M; the pieces must ship together — a credential with no emitter, or an emitter with
no credential, is dead weight). Internal build order: (1) natsperm row (+ `SubscribeAllow` if A2
ratified) + gen-dev-nkeys regen + Makefile/demo-up env wiring → (2) `healthSnapshot()` accessor +
`cmd/facet/health.go` probe + Reporter wiring + main.go doc rewrite → (3) tests + 
`docs/observability/health-kv-schema.md` facet section. Gates: `go build ./...`, `make vet`,
`golangci-lint run ./...`, `go test ./cmd/facet/... ./internal/natsperm/...`, board lint.
