# EDGE.5 — the browser Edge node (browser-native transport + the engine's second host) — design

**Status: ✅ RATIFIED (Andrew, 2026-07-16) — FORK-W A′ (wasm semantics core + JS transport shell;
B stays the pre-approved tripwire fallback). Single-lane directive (Andrew, at ratification):
fires W1–W4 ALL run in the lattice lane — the whole chain in one lane so neither steward can see
itself blocked on the other; this also restores the Facet design's own fire-4→Lattice routing.
No frozen-contract change.** Author: Winston (Designer fire, 2026-07-16).
**Backlog rows:** [lattice.md](../planning-artifacts/backlog/lattice.md) → *Edge & personal lenses → Edge Lattice (full)* (EDGE.5 is its §7 item 5) · realizes **Facet Fire 4 `[lattice]`** ([edge-showcase-app-design.md](edge-showcase-app-design.md) §7) and **Facet transport Stage 2** (its §5).
**Consumers:** Facet (the ratified PWA client — Fire 2 shipped `f5b3031`, Fire 3 📋 ready in the verticals lane) is the named, already-built consumer; the wasm engine host is what lets it drop the Go host and run on any browser device.
**Contracts:** #11 (external actor authN — build-to; the same bearer JWT authenticates the WS connect via the shipped auth callout) · #1 (subject shapes — build-to) · #2 (envelope — build-to via the shipped Gateway door). **Frozen-contract change: NONE.**

**Grounds in:** `edge-lattice-full-design.md` (✅ ratified; EDGE.1–4 shipped; EDGE.5 is its FORK-B option B, deferred "gated on the Gateway WS bridge") · `edge-showcase-app-design.md` (✅ ratified 2026-07-11; FORK-2 = A "wasm engine, confirmable at Fire 4"; §5 split the "WS bridge" blob into native-WS config + a deferred push-waker) · `per-identity-nats-subscribe-acl-design.md` (✅ CLOSED; the transport-agnostic permission template this design rides) · `personal-secure-lens-design.md` (✅ CLOSED; PL.6's "WebSocket/push-bridge" half collapses into this design) · the code map in §2 · vendor sources fetched this fire (§2.3, per `docs/vendors.md`: NATS 2.14 pin, nats.go v1.52.0, the nats.js browser client).

---

## For Andrew (one-look ratification block)

**What it does (two lines).** Turns the shipped Go Edge engine into a **two-host engine**: NATS's *native* WebSocket listener goes on in `deploy/nats-server.conf` (config + conformance vectors, no bridge component), and `internal/edge`'s semantics core (LWW mirror, overlay, intent queue, reconcile) compiles to **js/wasm** with an IndexedDB store and a thin JS shell (the official `nats.js` browser client) owning the connection — so the Facet PWA drops its Go host and runs the *same* engine on any browser device, under the *same* per-identity auth-callout confinement.

**The one thing to understand before ratifying — a ratified premise needed one correction.** Facet FORK-2 A ("compile `internal/edge` to wasm; store → IndexedDB, **transport → WebSocket**") assumed the Go engine could hold the NATS connection in a browser. Vendor grounding falsifies that half: **`nats.go` has no js/wasm/browser transport** — it dials raw TCP sockets a browser doesn't have, and wasm support is a still-open upstream feature request (nats.go issues #530/#588/#661, open since 2021); the vendor's browser client is **`nats.js`** (`@nats-io/nats-core`'s W3C-WebSocket `wsconnect` + `@nats-io/jetstream`; the old `nats.ws` package was archived 2026-05 into it). So the engine splits at a **transport seam**: the **semantics** (everything subtle and already-tested — LWW-by-revision, cursor/gap→hydrate, overlay pending/retire, intent FIFO/drain/conflict-re-audit, vault AEAD-open) stay **single-sourced in Go/wasm**, and the **transport** (WS connection, JetStream durable consume, control RPCs) lives in a thin JS shell that feeds envelopes in and takes calls out. This *preserves the ratified FORK-2 A intent* (one engine, semantics never forked) while correcting its mechanism — see FORK-W.

**FORK-W — the browser engine split (the one fork; my recommendation A′):**

- **A′ — wasm semantics core + JS transport shell (RECOMMENDED).** Extract the two seams the code map shows are missing (`store.Store` is a concrete bbolt struct; `sync.Manager` concretely builds a substrate durable consumer — §2.1), compile the semantics packages to js/wasm with a `syscall/js` IndexedDB store, and hand the NATS I/O to a ~200-line JS shell over the official browser client. *Pros:* the semantics that took EDGE.1/2 three adversarial passes to get right (R3 overlay retirement, conflict re-audit, by-revision dedup) are never re-implemented; the Go node and the browser node share one test suite over the store/transport seams; wasm feasibility is already empirically verified (2026-07-02: interpreter-only ≈ 1.3 MB gz — and EDGE.5 ships *without* Starlark, so smaller). *Cons:* a Go↔JS bridge surface + a wasm toolchain in the build.
- **B — TypeScript mini-engine (the ratified fallback, kept).** The transport must be JS either way; B makes the *whole* node JS: reimplement mirror/overlay/queue in TS, pinned by conformance fixtures generated from the Go tests. *Pros:* one language in the browser, no wasm. *Cons:* a second implementation of exactly the subtle semantics, guarded only by fixtures — permanent drift risk on the plane where a drift means silently wrong local state.
- **Recommendation: A′, with B's tripwire named:** if at Fire W3 the wasm artifact exceeds ~2× the measured 1.3 MB-gz baseline or the JS↔wasm bridge proves unworkable for the async store, fall back to B without re-ratification (Andrew pre-approved B as the fallback in Facet FORK-2).

**Frozen-contract change: NONE.** The WS listener is deploy/component surface (the generated `nats-server.conf` + `internal/natsperm`'s matrix); the auth story is the *shipped* callout — its permission template derives every subject from the verified token and never sees the transport (§2.2), and the issued user JWTs already permit WebSocket connections (absent `AllowedConnectionTypes` = all types, jwt/v2 default). `nats.js` becomes a new `docs/vendors.md` row at build. Nothing in `docs/contracts/*` moves.

**One vendor default to be aware of (designed around, not open):** NATS's `websocket {}` block treats an **empty `allowed_origins` as allow-any-origin** — a fail-open default. The rendered block always sets it explicitly (§3.1), and a natsperm config-shape pin makes an empty origins list a test failure, so the property cannot silently drop.

---

## 1. Problem & intent

**The gap.** EDGE.1–4 delivered the sovereign node — but only as a **Go daemon on a trusted machine**. The real per-user device (the vault's actual vision: *"a functional node of the graph … directly on a user's device"*) is a browser or phone, and today nothing lets one connect: the NATS server exposes no WebSocket listener, and the engine cannot run outside a Go host. Facet Fire 2 shipped the PWA renderer, but it renders through a localhost Go chaperone (`cmd/facet` serving SSE) — one machine, one process, not a pocket device. EDGE.5 is the increment where the engine reaches the user's own hardware.

**The sharpened premise (inherited from Facet §5, executed here).** The corpus long treated "the Gateway WS/push bridge" as one unbuilt blob gating EDGE.5. The Facet design split it against the pinned vendor: **WebSocket is a native NATS 2.14 server capability** (a `websocket {}` config block — no bridge component, no Gateway involvement beyond the callout responder it already hosts), and only the **push-waker** (background wake when the tab is dead) is genuinely undesigned — deferred to Facet Stage 3 by the ratified G13 disposition, *unchanged by this design*. PL.6's "WebSocket/push-bridge" half likewise collapses into this design; PL.6's multicast dedup stays deferred on its own bandwidth trigger.

**Intent.** One design that (a) turns the WS listener on with proof the shipped security boundary holds over it unchanged, and (b) gives the engine its second host so the PWA runs it in-page — each independently shippable, together = Facet Fire 4's green bar ("the PWA on a second machine completes the Fire-2 e2e under confined permissions with no local binary").

---

## 2. Grounding — what exists, verbatim (do not redesign it)

### 2.1 The engine (code map, 2026-07-16)

- **Everything host-coupled is behind exactly two concrete types.** `internal/edge/{store,overlay,sync,agent,vault}` use no `os`, no tickers, no goroutines — the hosts own those. The two hard seams:
  - **`store.Store` is a concrete bbolt struct** (`store.go:67-70`, `db *bbolt.DB`); `overlay`/`sync`/`agent` hold the concrete pointer but **only ever call methods** (~15: `ApplyUpsert/ApplyDelete/Get/ScanPrefix`, the pending-overlay trio, the intent-queue trio, `PutLocal/GetLocal`, `Cursor/SetCursor`) — interface extraction is mechanical. bbolt (mmap) does not build for js/wasm; this is the IndexedDB port target.
  - **`sync.Manager` concretely builds the transport** (`sync.go:134-139`): `substrate.RunDurableConsumer` on stream `SYNC`, durable `edge-sync-<identityID>-<deviceID>` (`sync.go:118`), filter `subjects.PersonalSync` — plus core-NATS request-reply for the control RPCs via one helper (`controlRequest`, `sync.go:265-284`, stamping the `Lattice-Actor` header). `vault/client.go:131-136` uses the same request-reply shape for `personal.sessionkey`.
- **The write path is already portable.** Both shipped hosts wire `agent.GatewaySubmitter` (`cmd/edge/main.go:130`, `cmd/facet/main.go:148`) — plain `net/http` `POST /v1/operations` with a Bearer token, which under js/wasm rides the Fetch API. `NATSSubmitter` stays tests/trusted-only.
- **The engine already pushes changes.** `sync.Config.OnChange(key, deleted)` + `OnHydrationComplete(revision)` (`sync.go:68-77`) fire only when a delta actually lands (stale/reordered drops don't) — shipped for exactly this consumer class (edge-manifest Fire 0, G3).
- **The Fire-2 renderer's protocol is the porting contract.** `cmd/facet` serves the PWA three SSE frame kinds — `manifest` (key + merged overlay value), `outbox` (intent lifecycle), `ready` (hydration high-water) — plus `POST /api/enqueue` (`cmd/facet/feed.go:27-35`, `server.go:36-156`). The browser engine's JS API mirrors these one-for-one (§3.3), so the renderer swap is mechanical.
- **No wasm/TS code exists anywhere in the repo** — EDGE.5 is greenfield against the engine above.

### 2.2 The security boundary (shipped, transport-agnostic)

- The per-identity **auth callout** (`internal/gateway/natsauth`, hosted by `cmd/gateway`) consumes exactly two request fields: `ConnectOptions.Token` (the Contract-#11 bearer JWT) and `ClientInformation.Name` (the device id) — **nothing about the listener type**. `PermissionsFor(identityID, deviceID)` (`natsauth.go:348-366`) splices every allowed subject from the *verified* identity: own `lattice.sync.user.<U>`, own `_INBOX.edge.<U>.>`, the exact filtered-create/MSG.NEXT/INFO/DELETE/ACK subjects for durable `edge-sync-<U>-<D>`, and the four `lattice.ctrl.refractor.personal.*` RPCs. Deny-by-omission for everything else.
- The issued user JWT sets no `AllowedConnectionTypes` → **all connection types allowed** (jwt/v2 `UserPermissionLimits` default; NATS docs: "the absence of `allowed_connection_types` means that all types of connections are allowed") — a WS client is admitted by the *same* issued grant with zero responder change.
- The callout channel's xkey sealing (`UnsealRequest`/`SealResponse`) protects the server↔responder leg and is orthogonal to the client's transport.
- `deploy/nats-server.conf` is **generated** by `internal/natsperm.RenderConf` and byte-diff-enforced by `TestConfMatchesMatrix` (`conf_test.go:792`) — the `websocket {}` block must be added to the **template**, never hand-edited. Today the conf has no listener config at all (ports live on the compose command line: 4222/8222); no WS port is exposed.
- `internal/natsperm/auth_callout_test.go` already runs the full sealed callout round trip against an embedded server loaded from the *real committed conf*, with 7 vectors (own-slice allow, cross-identity deny, fail-closed malformed/expired/unknown-kid, revocation, delta-forgery deny, inbox isolation). The embedded server API takes `opts.Websocket.{Port,NoTLS}` — the WS twins reuse the identical harness and dial `ws://`.
- **Gateway CORS does not cover the WS handshake.** `gateway.ConfigureCORS` (exact-origin allow-set, never wildcard) gates the PWA's HTTP writes only; a browser's WebSocket connect is gated by NATS's own `websocket { allowed_origins }` — a **second, independent origin surface** this design owns explicitly (§3.1).

### 2.3 Vendor grounding (fetched this fire; authority + pin per `docs/vendors.md`)

- **WS listener is in-pin.** NATS 2.14 (`nats-server/v2 v2.14.0`, `nats:2.14-alpine`); the `websocket {}` block (docs.nats.io → *WebSocket Configuration*): `port`, `tls` (**required unless `no_tls: true`**, test/dev only), `same_origin`/`allowed_origins` (**empty = any origin** — the fail-open default flagged in the For-Andrew block), `compression`, `handshake_timeout`, optional per-listener `authorization` override (unused here — the main authorization block + callout apply to WS clients).
- **`nats.go` cannot be the browser transport.** No js/wasm support: issues [#530](https://github.com/nats-io/nats.go/issues/530), [#588](https://github.com/nats-io/nats.go/issues/588), [#661](https://github.com/nats-io/nats.go/issues/661) are open feature requests (2021→); the client dials TCP `net.Conn`s. (Its ws:// scheme support is for native processes — usable for our **Go-side parity tests**, present well before our v1.52.0 pin.)
- **The vendor's browser client is `nats.js` v3** (currently v3.4.0): `@nats-io/nats-core` carries the W3C-WebSocket browser transport (`wsconnect`) + token auth + `inboxPrefix` + headers; `@nats-io/jetstream` layers durable consumers over it. The legacy `nats.ws` package (v1.30.3) was archived 2026-05 into nats.js. Exact package pins recorded in `docs/vendors.md` at Fire W3.
- **The consumer-create wire form must match the pinned grant.** The ACL grants only the filtered-create form `$JS.API.CONSUMER.CREATE.SYNC.<durable>.<filter>`; a client using a legacy form fails **closed** (permission violation), so a mismatch is loudly detected, never silently open. Fire W3 proves the nats.js jetstream client emits the granted form against the embedded server before anything ships (§5).

### 2.4 Invariants inherited (unchanged)

**P2/P5/P1 exactly as the Go node:** the browser node reads only the Personal-Lens projection (P5), writes only ordinary ops through the Gateway door (P2), and all of its state (IndexedDB mirror, queue, cursor, device id) is device-local operational state (P1). It is another *client* of shipped seams — no engine reads Core KV, no new server-side state plane, no new writer.

---

## 3. The shape

### 3.1 Fire W1 — the WebSocket listener (config + conformance, no new component)

Add a `websocket {}` block to `internal/natsperm.RenderConf`'s template (regenerating `deploy/nats-server.conf`) and a compose port:

```
websocket {
  port: 9222                      # explicit — never a default; 8080 collides with the Gateway
  no_tls: true                    # DEV ONLY — prod ships a tls{} block (ops fire, like #75's mTLS tail)
  allowed_origins: [ ... ]        # ALWAYS non-empty (vendor default is allow-any) — the dev origins
}
```

- **Origins.** `allowed_origins` carries the dev origins (the Facet dev host's, e.g. `http://localhost:<facet-port>`, parameterized the same way `GATEWAY_CORS_ORIGINS` is). A natsperm config-shape pin (a `TestWebsocketConfigured` sibling of `TestAuthCalloutConfigured`) asserts the block exists, the port is explicit, **and `allowed_origins` is non-empty** — the fail-open vendor default becomes structurally unreachable. (Origin checks gate *browser-initiated* connects only — a non-browser client can forge the header; the real authn stays the token. The origin gate is CSRF-class hardening, not the boundary.)
- **Compose:** `- "${NATS_WS_PORT:-9222}:9222"` on the nats service. Dev-stack only; TLS + public exposure is the prod ops fire (same split as the Gateway's NGINX fire — infra config, not a Steward build).
- **Auth parity vectors (the fire's real content).** WS twins of the seven `auth_callout_test.go` vectors: same harness, embedded server additionally opens `opts.Websocket.Port=-1, NoTLS=true`, `connectEdge` dials the WS URL via nats.go's native `ws://` scheme. Green bar: **all seven vectors pass verbatim over WS** — own-slice allow, cross-identity deny, fail-closed trio, revocation, forgery deny, inbox isolation — proving the callout + template are transport-invariant *by test, not by assertion*.

Independently shippable: any browser NATS client (including hand-rolled nats.js experiments) is unblocked the moment W1 lands, before any wasm work.

### 3.2 Fire W2 — the engine seams (Go-only; the Go node stays byte-identical)

Two mechanical extractions, no behavior change:

1. **`store.Store` becomes an interface** (same package; the bbolt implementation keeps the name/constructor so `cmd/edge`/`cmd/facet` don't change). `overlay`, `sync`, `agent`, `vault` retarget to the interface. The existing test suites double as the **conformance suite**: the store tests restructure into a harness run against any implementation (bbolt now; IndexedDB in W3).
2. **The transport seam.** `sync.Manager` (and `vault.Client`) currently take `*substrate.Conn`; they gain a narrow interface pair the concrete substrate types already satisfy:
   - `DeltaSource` — "run a durable consumer for (stream, durable, filter) delivering `(payload, streamSeq)` + expose the stream's last-seq for gap detection" (what `RunDurableConsumer` + `JetStream().Stream()` provide today);
   - `ControlClient` — "request-reply a payload on a control subject with an optional `Lattice-Actor` header" (what `NATS().RequestMsgWithContext` provides today).
   The substrate-backed implementations are thin adapters in the same files; **`cmd/edge` and `cmd/facet` compile against them unchanged** — the proof of no-behavior-change is the untouched existing test suites staying green.

After W2, `internal/edge`'s semantics packages hold no host-coupled concrete type and compile under `GOOS=js GOARCH=wasm` (a CI build check, no runtime yet).

**W2 SHIPPED (2026-07-17).** `store.Store` is an interface (`*BoltStore` behind `//go:build !js`, `Open` unchanged for the Go hosts); `internal/edge/transport` carries the `DeltaSource`/`ControlClient` pair in plain types, with the substrate adapter isolated in `internal/edge/transport/natstransport` (`!js`-tagged, so a browser build cannot dial NATS and bypass the Gateway); `agent/submit_nats.go` (the trusted pre-Gateway submitter) is `!js`-tagged for the same reason; the store tests are now the `storetest` conformance harness (bbolt passes it; IndexedDB answers to it at W3). The pure Contract-#1 key/NanoID helpers moved to the leaf package `internal/substrate/keys`, re-exported from `internal/substrate` so no call site changed.

**Correction — the "no substrate imports" acceptance was necessary but not sufficient, and W3's size budget is the thing at stake.** Measured this fire (`GOOS=js` probe binaries, gzip -9):

| wasm probe | raw | gz |
|---|---|---|
| hello-world baseline | 1.84 MB | 0.55 MB |
| `store`+`overlay`+`transport` (NATS-free after this fire) | 3.04 MB | **0.87 MB** |
| full engine incl. `sync`/`agent`/`vault` | 8.32 MB | **2.28 MB** |

`sync`/`agent`/`vault` still link `nats.go` — not through `substrate`, which W2 removed, but through the **wire-type packages** they need for DTOs: `internal/refractor/control` (`ControlRequest`/`ControlResponse`), `internal/processor` (`OperationEnvelope`), `internal/vault` (`Ciphertext`/`SessionKey`). Each bundles server-side machinery beside its types, so importing a struct links a NATS client. That is ~1.4 MB gz of dead transport in the browser artifact, against FORK-W's tripwire of ~2× the 1.3 MB-gz baseline (~2.6 MB): **the engine alone is already at 2.28 MB before `syscall/js`, the IndexedDB store, or the JS shell** — so W3 would likely trip the fallback to FORK-W B on dead code rather than on a real limit. The same root cause blocks a dependency-level CI assertion (`go list -deps` must not reach `nats.go`), which is why the js gate today can only catch what fails to compile (bbolt) and is commented to say exactly that: `nats.go` builds for js/wasm, so the build check cannot see it.

**W3 prerequisite (named, not optional):** extract the DTOs these three packages expose to the Edge into leaf packages (std-lib only), leaving the server machinery behind — then the engine drops to ~0.9 MB gz, the FORK-W tripwire measures the artifact rather than the debt, and the js gate can be upgraded from "compiles" to "cannot reach nats.go", which is the assertion that actually confines the browser build to the Gateway door.

**W3 increment 1 — the DTO extraction — SHIPPED (2026-07-17).** Four wire-leaf packages now hold the DTOs the engine needs, with the server machinery left behind: `internal/processor/opwire` (the operation envelope + reply + `ParseEnvelope`), `internal/refractor/control/controlwire` (the control request/response + subject), `internal/refractor/health/healthwire` (the health `Entry` a control response embeds — the transitive blocker), `internal/vault/vaultwire` (the vault wire types + `OpenWithSessionKey`). Each parent re-exports its leaf as **type aliases**, so no platform call site changed — the W2 `internal/substrate/keys` precedent. Client and server share one definition rather than re-declaring (the §8.1 RR-4 drift hazard).

Measured (`GOOS=js` probe importing the six engine packages, gzip -9):

| wasm probe | raw | gz |
|---|---|---|
| full engine, before (W2) | 8.32 MB | 2.28 MB |
| **full engine, after** | **4.65 MB** | **1.32 MB** |

**The engine reaches zero `github.com/nats-io/*` packages under `GOOS=js`** — not one, which is the claim the js gate now asserts (`go list -deps`, upgraded from "compiles"; verified to fail on a package that genuinely links NATS, so it is not vacuous). Every existing suite is untouched-green (the no-behavior-change proof), and the moved structs' JSON tags were diffed as an exact multiset against their originals — the wire format is provably unchanged.

**Correction to this section's own estimate:** the predicted ~0.9 MB gz was optimistic — it was the W2 `store`+`overlay`+`transport` figure (0.87 MB) assuming `sync`/`agent`/`vault` add nothing. They add ~0.45 MB gz of real semantics (intent queue, reconcile, envelope construction, AEAD). **1.32 MB gz is the honest engine floor**, and it is now artifact rather than debt. Against the ~2.6 MB FORK-W tripwire that leaves ~1.28 MB of headroom for `syscall/js` + the IndexedDB store + the JS shell — the tripwire now measures what W3 actually adds, which was the point.

### 3.3 Fire W3 — the browser host (wasm engine + JS shell)

**W3 increment 2 — the IndexedDB store — SHIPPED (2026-07-17).** `store.Store`'s second engine is in:
`IDBStore` (`internal/edge/store/idb.go`, `js`-tagged, `syscall/js`) beside the `!js` bbolt engine, and it
passes the **same** `storetest` conformance suite W2 extracted — LWW-by-revision, FIFO intent order, and
durability across a close-and-reopen are proven *of the port*, not inferred from it compiling. IndexedDB's
key generator (`autoIncrement`) stands in for bbolt's `NextSequence`: monotonic and persisted with the
object store, which the reopen vector pins. Per §5's licence, the suite was checked **non-vacuous against
this engine** the way inc 1 checked its dependency gate — breaking the LWW gate fails both stale-drop
vectors; unbounding `ScanPrefix` fails the scan vector.

**The runner fork, resolved (§5 left it "pick at fire against toolchain reality"): headless Chrome via
`wasmbrowsertest`, not Node + fake-IDB.** Grounded, not assumed: **Node ships no IndexedDB at all**
(`typeof indexedDB` → `undefined`), so the Node route needs `fake-indexeddb` — an npm toolchain this repo
does not have, to prove the port matches *a reimplementation of* the engine the PWA actually runs on. On
the plane where a divergence means silently-wrong local state, that is the wrong thing to be right about.
`wasmbrowsertest` runs the suite against a **real IndexedDB in a real headless Chrome**, installs as a
pinned Go tool (v0.11.0, `WASM_BROWSER_TEST_VERSION`), and needs no npm — `make test-edge-idb-conformance`,
CI job `edge-browser-store` (the runner image ships Chrome). New `docs/vendors.md` rows: **IndexedDB**
(W3C spec as authority) + **wasmbrowsertest**.

**Two IndexedDB semantics the port is written around** (now in `vendors.md`, since the next person will
need the authority): a transaction is active only while a request it issued is pending and auto-commits
once the event loop goes idle — so the LWW read-modify-write issues its `put` from **inside** the `get`'s
success callback; and a transaction's `complete`, not a request's success, is the durability point, so
every write awaits it. A probe showed Go's wasm scheduler resumes a blocked goroutine *within* the
callback's own dispatch — a plain `await`-then-`put` does work today — but that is an **undocumented
runtime property**, and the LWW gate is too load-bearing to rest on it, so the dependency is kept
structural. Two hazards were closed beyond what the suite asks: `OpenIDB` fails on **`blocked`** (fired
*instead of* success/error when another connection holds an older version open — an await watching only
those two stalls forever with no event; unreachable at schema version 1, reachable the moment a bump meets
the multi-tab host of §3.3), and the conformance factory deletes the database before opening so
"fresh, empty store" holds by construction rather than by the runner's incidental clean profile.

**Correction to this fire's own scope:** inc 2 was filed as "the browser host" — store **+**
`make build-edge-wasm` **+** the JS shell. It shipped the **store half only**, deliberately: the wasm
artifact has nothing to emit until the **host entry point** the JS shell brings exists, so a
`build-edge-wasm` target landed now would build a `main` that exports nothing. **Inc 3 splits at the wasm↔JS boundary:**

- **Inc 3a — SHIPPED (2026-07-17): the wasm host entry + `make build-edge-wasm` + the in-Chrome host test.**
  `internal/edge/browser` (js-tagged) wires the same semantics packages `cmd/facet` embeds (store, overlay,
  sync, agent) onto the IndexedDB store and a JS transport shell, exposing them to the page as
  `globalThis.latticeEdge` (the frame kinds are `cmd/facet`'s SSE kinds verbatim, so W4's renderer swap is a
  transport change, not a rewrite). `cmd/edge-wasm` is the artifact `main`; `make build-edge-wasm` emits it +
  the version-matched `wasm_exec.js`. The host is driven over its exported JS API against a real IndexedDB in
  headless Chrome (`test-edge-idb-conformance` now runs `internal/edge/browser` beside the store suite). The
  js CI gate's build + `go list -deps` nats-free assertion now covers `cmd/edge-wasm` too.

- **Inc 3b — SHIPPED (2026-07-17): the JS shell** (`internal/edge/browser/shell`, `86d29c9`). The transport
  half the `jsTransport` seam calls out to, over vendored `nats.js` 3.4.0 (single static ESM bundle, no npm
  in the tree; `shell/VENDOR.md` + a `docs/vendors.md` row): `createSyncCore` supplies
  `startConsumer`/`stopConsumer`/`firstSequence`/`request` + pushes deltas via `deliver`, with a token-refresh
  authenticator, the durable's `InactiveThreshold`, and the `Lattice-Actor` header; `createShell` adds Web
  Locks leader election (`leader.mjs` + node unit vectors) and `storage.persist()`. The **consumer-create
  wire-form parity test** (`internal/natsperm`, build-tag `edgeparity`, `make test-edge-consumer-parity`, CI
  job `edge-consumer-parity`) drives the real shell from Node against the real per-identity callout and pins
  that nats.js emits the ACL-granted `$JS.API.CONSUMER.CREATE.SYNC.<durable>.<filter>` form — fail-closed,
  with a cross-identity control case (non-vacuous) and a MSG.NEXT/ACK round-trip. Two findings it grounded:
  (1) nats.js's `jetstreamManager` probes `$JS.API.INFO` (ungranted, un-probed by nats.go) → opened
  `{checkAPI:false}` for wire parity; (2) **STREAM.INFO is denied to the Edge grant** but
  `natstransport.FirstSequence` (gap detection) needs it — a **pre-existing** gap affecting the shipped Go
  nodes too, filed for design (lattice.md Security row); the shell stays at parity with the Go node rather
  than diverging. **Deferred to W4** (the renderer/in-page integration where it is exercised): the shell's
  BroadcastChannel follower change-signal.

**The size tripwire, measured honestly (inc 3a).** The 1.32 MB-gz "engine floor" was measured by a
**blank-import probe** (`_ "internal/edge/…"`), which the linker's dead-code elimination strips down to the
imported *packages*, not the *reachable* call graph. A real `main` that actually calls the write path retains
`net/http` **and** `crypto/tls` — and the wasm artifact came out at **3.0 MB gz, past the ~2.6 MB tripwire**.
The excess is **not** dead weight and FORK-W B would not remove it (B changes the read transport, not the
write door): `net/http` is reachable in the js engine graph *only* through `agent.GatewaySubmitter`, and in a
browser it is a wasm reimplementation of `fetch` — which the page has natively, TLS and all. So inc 3a gives
the browser host a **`syscall/js` fetch submitter** (`submit_fetch.go`, the same POST `/v1/operations` wire
contract + `ErrCredentialRejected` taxonomy, over `fetch`); the Go host keeps `agent.GatewaySubmitter`. That
drops `net/http`/`crypto/tls` from the live graph and the artifact to **1.71 MB gz (gzip -9)** — under the
tripwire, ~0.9 MB of headroom over the 1.32 MB floor for `syscall/js` + the store, with the shell being
JS-side (it does not grow the wasm). The tripwire stands un-tripped, now on a real-link measurement.

**The split (FORK-W A′):**

| Layer | Runs | Owns |
|---|---|---|
| **Semantics core** (Go → wasm, in a **Web Worker**) | `store` interface consumers: LWW apply, cursor/gap→hydrate decision, overlay pending/retire, intent queue + drain + conflict re-audit, envelope construction, `GatewaySubmitter` (Fetch), `vault` AEAD-open | correctness |
| **IndexedDB store** (Go, `syscall/js`, in the same Worker) | the `store.Store` interface over IndexedDB (async bridged: each JS→Go entry point returns a Promise; Go goroutines block on IDB request callbacks — the standard wasm discipline; IndexedDB is Worker-available) | persistence |
| **JS shell** (~200 lines over vendored `nats.js` ESM) | `wsconnect` (token = the bearer JWT, `name` = device id, `inboxPrefix` = `_INBOX.edge.<U>`), the JetStream durable consumer (`edge-sync-<U>-<D>`, AckExplicit, `InactiveThreshold` set so abandoned browser durables self-clean — new; the Go node can adopt it later), the three-plus-one `personal.*` control RPCs with the `Lattice-Actor` header, token-refresh on reconnect (the server disconnects at authz expiry ≤15 m; the shell's authenticator re-supplies the current token) | transport |
| **Renderer** (the Fire-2 PWA, unchanged in spirit) | subscribes the engine's `manifest`/`outbox`/`ready` events + calls `enqueue()` — the same frames `cmd/facet`'s SSE serves today, now in-page | pixels |

- **Placement:** the wasm host + JS shell live under `internal/edge/` (build-tagged `js`) with a `make build-edge-wasm` target emitting the artifact the PWA embeds; the `nats.js` core+jetstream ESM bundles are **vendored** as static files (the repo has no npm toolchain and the PWA is served as plain files — introducing a bundler for one dependency fails the simplest-extension test), with the exact versions pinned in `docs/vendors.md`.
- **Multi-tab correctness (a real browser-only hazard):** two tabs share `localStorage` → same device id → same durable → **two consumers splitting one pull stream = both mirrors diverge**. The shell takes a **Web Locks API leader election**: one tab holds the sync lease (connection + consumer + drain); followers read the shared IndexedDB and get change signals over `BroadcastChannel`; leader death releases the lock and a follower takes over (cursor is in the store, so takeover resumes exactly). Device id stays stable.
- **Storage honesty:** the shell requests `navigator.storage.persist()`. The mirror is a disposable cache by design (eviction ⇒ re-hydrate, the same gap path as retention expiry). The **intent queue is not disposable** — an eviction while offline loses queued writes; v1 accepts this as a documented residual bounded by (a) the persist() request, (b) the Outbox surface making unsynced intents visible, (c) drain-on-reconnect shrinking the window. (A "export unsynced" affordance is renderer polish, not engine scope.)
- **Not ported (deliberately):** Starlark prediction (the Go node ships pure-A too — A′ prediction stays gated on the edge sandbox for *both* hosts; keeps the wasm artifact small); `NATSSubmitter`; any query surface beyond the manifest/outbox frames. `vault.Client` *ports* (it is just a `ControlClient` consumer + pure-Go AEAD) but stays **unwired** until a sensitive-display consumer exists — the same shipped posture as the Go node's `Reader` (dead-scaffolding test).

### 3.4 Fire W4 — Facet goes browser-native (the verticals hand-off)

The PWA drops the Go host: renderer binds to the engine's JS API, `cmd/facet` shrinks to a static file server (or the PWA is served by anything). Green bar = **Facet Fire 4's ratified acceptance**: the PWA on a second machine completes the Fire-2 e2e — hydrate → order laundry → pending→confirmed → task auto-complete → offline queue → reconnect drain — under confined WS permissions, **no local binary**. Ratification routed this fire to the **lattice lane with W1–W3** (Andrew's single-lane directive — the whole W1→W4 chain runs in one lane so neither steward reads itself as blocked on the other; this matches the Facet design's own fire-4→Lattice routing). The verticals lane consumes the result; it does not build it.

**W4 increment 1 — the shell's multi-tab correctness layer — SHIPPED (2026-07-17, `fa99b34`).** The
in-page integration's two browser-only coordination mechanisms, self-contained in the JS shell with
`node --test` vectors (no live stack): (a) fixed a latent bug in the shipped inc 3b `createShell` —
`electLeader({...}).catch(...)` was a TypeError on the non-thenable election handle, so the **Web-Locks
leader path threw and the leader tab never opened its consumer** (uncaught because the parity harness only
drives the no-locks path); now watches `handle.settled` for a real election failure, acquisition stays via
`onAcquire`. (b) the **follower change-signal** (§3.3, deferred here from inc 3b): the leader posts each
landed change on a per-identity BroadcastChannel (`signalChange`), every other tab hears it via
`onPeerChange` and re-reads the touched key from the shared IndexedDB (a channel never echoes to its
poster; `close()` tears the channel down and releases the lock so a still-open leader hands off on
sign-out). New `createCore`/`channel` injection seams make `createShell` unit-testable (it never was —
only `electLeader` was); wired into the `edge-consumer-parity` gate.

**W4 increment 2 — host-side consumption — SHIPPED (2026-07-17, `e7a81c6`).** The wasm host closes the
loop inc 1's shell change-signal opened. Leader path: `OnChange` (which the shell's leadership gate ensures
fires only on the consumer-holding tab — `handle()` runs only off the delta feed, never hydration) now also
calls `shell.signalChange`, posting each landed key to peers. Follower path: `Start` registers an
`onPeerChange` handler that re-reads the touched key from the shared IndexedDB through the overlay and
republishes its manifest frame — the re-read spawned on a goroutine, not run inline, because the handler is
invoked from the JS event loop and `publishManifestKey` blocks on an IndexedDB read whose completion is
itself an event-loop callback (inline would deadlock; the `onFrame` goroutine takes the same care). Both
guard on the shell exposing the function, so a single-context host (parity harness, no-BroadcastChannel
browser) is a clean no-op; `Stop` unregisters the follower handler before releasing its `js.Func`. Verified
in-Chrome against real IndexedDB (`make test-edge-idb-conformance`): a landed delta signals peers with the
touched key; firing the registered handler republishes that key's frame from the store.

**W4 remaining increments (each independently green):** **inc 3** — the
renderer swap: `cmd/facet/web`'s `EventSource("/api/feed")` → `latticeEdge.start({shell})` + `onFrame`, the
enqueue `POST /api/enqueue` → `api.enqueue`, and a wasm+shell boot module the page loads. **inc 4** —
`cmd/facet` shrinks to a static file server + the ratified Fire-4 cross-machine, no-binary e2e (the W4
green bar, Gate-3 class).

### 3.5 Read/write/state summary (unchanged invariants)

| Concern | Mechanism | Invariant |
|---|---|---|
| Read | JetStream durable over WS → wasm engine → IndexedDB mirror | P5 — the same Personal-Lens projection; never Core KV |
| Write | engine envelope → `GatewaySubmitter` (Fetch) → Gateway stamps → Processor | P2 — unchanged sole writer |
| Local state | IndexedDB (mirror/queue/cursor), localStorage (device id) | P1 — device-local operational |
| Transport authN | bearer JWT on WS CONNECT → shipped auth callout → per-identity template | #11 build-to; template unchanged, proven over WS by W1 vectors |
| Origins | NATS `allowed_origins` (explicit, non-empty, natsperm-pinned) + existing Gateway CORS | fail-closed at both doors |
| Confidentiality | ciphertext deltas land as-is; vault client ports but stays unwired | blind projection unchanged |

---

## 4. Contract surface

| Contract / doc | Change vs. build-to | What |
|---|---|---|
| **`docs/contracts/*`** | **NO CHANGE** | The WS listener is deploy/component surface; the engine split is Edge-internal; the auth story is the shipped #11-conformant callout. Nothing staged. |
| #11 external actor authN | build-to | The same bearer JWT authenticates the WS connect; the callout responder is already #11 §11.1's NATS-connect consumer row (committed with the ACL ratification). No new verifying surface. |
| #1 / #2 | build-to | Same subjects, same envelope, same Gateway stamping. |
| `docs/components/edge.md` | **DOC UPDATE at build** | The two-host engine, the seams, the browser host's leader-election + storage posture. |
| `docs/vendors.md` | **ROW ADD at build (W3)** | nats.js (`@nats-io/nats-core` + `@nats-io/jetstream`), exact pins + authority (github.com/nats-io/nats.js). |
| `internal/natsperm` matrix / conf | component surface | The `websocket {}` block in `RenderConf` + the new config-shape pin (W1). |

---

## 5. Migration, compatibility, test strategy

**Migration/compatibility.** Purely additive at every step. W1 adds a listener existing clients don't dial; W2 is a refactor proven by the untouched existing test suites (plus the CI `GOOS=js` compile check); W3 adds artifacts nothing existing loads; W4 changes only the Facet app. The Go reference node stays fully supported (it remains the CI proving ground and the trusted-deployment shape).

**Test strategy.**
- **W1:** the seven auth-callout vectors over WS (embedded server, real committed conf) + the config-shape pin (`websocket` block present, explicit port, non-empty `allowed_origins`) + `TestConfMatchesMatrix` regeneration.
- **W2:** existing `internal/edge` suites unchanged and green (the no-behavior-change proof); the store conformance harness extracted; `GOOS=js GOARCH=wasm go build ./internal/edge/...` in CI.
- **W3:** store conformance harness green on IndexedDB (headless browser runner, e.g. `wasmbrowsertest`-style, or Node + fake-IDB — pick at fire against toolchain reality); a **consumer-create wire-form parity test**: the nats.js jetstream client creating `edge-sync-<U>-<D>` against the embedded WS server under the real issued permissions — proving the client emits the granted filtered-create form (fail-closed if not, per §2.3); leader-election unit vectors (lock handoff, cursor resume); wasm size vs the 1.3 MB-gz baseline (tripwire → FORK-W B).
- **W4 (Gate-3 class):** the Fire-2 e2e cross-machine; plus the WS twins of the Edge read-bypass posture — A's browser never receives B's deltas (the W1 vectors already prove the transport half; PL.3's e2e proves the projection half); a revoked token cannot reconnect (vector 4's live path, now over WS).

---

## 6. Risks & alternatives

- **R1 — semantics drift between hosts.** The reason FORK-W A′ exists: with the semantics in wasm there is one implementation to drift. Residual risk moves to the *seams* (store/transport contracts), which is exactly where the conformance harness sits.
- **R2 — multi-tab durable contention.** §3.3's leader election. Without it the failure is silent mirror divergence — the design treats it as a correctness requirement, not polish.
- **R3 — IndexedDB eviction loses queued intents.** Bounded per §3.3 (persist(), visible Outbox, drain-on-reconnect); accepted v1 residual, documented in `edge.md`.
- **R4 — origin fail-open.** The vendor default (`allowed_origins` empty = any). Killed structurally by the natsperm pin (§3.1).
- **R5 — token expiry churn.** The ≤15 m authz TTL disconnects live WS connections; the shell's re-auth reconnect is a designed path (same posture as the Go node's reconnect), and hydration-vs-resume is already the engine's gap logic. No new server surface.
- **R6 — wasm toolchain friction / size.** Empirically de-risked (2026-07-02 verification); explicit tripwire to FORK-W B.
- **Rejected — a bespoke Gateway WS bridge** (subscribe-on-behalf proxy). Rejected now for the third time (Personal Lens Fork 1, subscribe-ACL fork C, Facet §5): it re-implements JetStream consumer semantics in a stateful proxy and the native listener makes it pure waste.
- **Rejected — tunneling nats.go through a browser socket shim** (a `syscall/js` `net.Conn` over the WebSocket API via `CustomDialer`). Unsanctioned by the vendor (the open issues *are* this request); fragile stream-over-messages framing; the vendor's answer is nats.js. The engine split honors the vendor boundary instead of fighting it.
- **Rejected — REST-polling the manifest** (no NATS in the browser at all). Facet FORK-3 C already rejected the second read plane; polling also abandons deltas + offline.

---

## 7. Decomposition for the Steward (each independently shippable + green)

1. **W1 `[lattice]` — WS listener + transport-parity vectors.** `RenderConf` websocket block (explicit port, dev `no_tls`, non-empty `allowed_origins`) + regenerated conf + compose port + the seven WS auth-callout vectors + the config-shape pin. *Green:* vectors pass over `ws://`; `TestConfMatchesMatrix` green. *Depends on: nothing.*
2. **W2 `[lattice]` — engine seams.** `store.Store` interface + `DeltaSource`/`ControlClient` seams; substrate/bbolt adapters; conformance harness extracted; CI `GOOS=js` compile check. *Green:* existing suites untouched-green. *Depends on: nothing (parallel with W1).*
3. **W3 `[lattice]` — the browser host.** Shipped across three increments: **inc 1** the DTO extraction (§3.2 — without it the engine carried ~1.4 MB gz of unreachable NATS client); **inc 2** the IndexedDB store (syscall/js, conformance-passing in headless Chrome); **inc 3a** the wasm host entry (`internal/edge/browser` + `cmd/edge-wasm`) + `make build-edge-wasm` + the in-Chrome host test + the js CI gate extended to `cmd/edge-wasm`, with the size tripwire honestly re-measured and held under budget by the fetch submitter (§3.3). **Remaining: inc 3b** — the JS shell (vendored nats.js, leader election, token-refresh reconnect, `InactiveThreshold`) + the consumer-create wire-form parity test + the nats.js vendors.md row. *Green:* conformance harness on IndexedDB ✅; host test ✅; size budget ✅; js gate upgraded from "compiles" to "`go list -deps` cannot reach nats.go" ✅ (inc 1); parity test + shell (3b). *Depends on: W1 + W2.*
4. **W4 `[lattice]` — Facet browser-native (= Facet Fire 4).** Renderer binds the engine JS API; Go host dropped. *Green:* the ratified Fire-4 acceptance (cross-machine, confined, no binary). *Depends on: W3 + Facet Fire 3 (auth turn-on, 🏗️ building — Inc 1 shipped).* Single-lane: built here, not handed to the verticals steward.

**Deferred, named (unchanged dispositions):** the **push-waker** (Facet G13 — file as its own lattice design when Stage 2 lands = when W4 ships); **PL.6 multicast dedup** (bandwidth trigger); **EDGE.6** local authority (Andrew-gated); native iOS host (re-opens the 2.5.2 store-policy item; the PWA route ships first per Facet FORK-4).

---

## 8. Adversarial review note

Run as a self-adversarial pass this fire (the substantial-design gate); findings **folded above**: the `allowed_origins` fail-open default → the natsperm pin (§3.1/R4); the FORK-2 A transport premise falsified by vendor grounding → FORK-W (the For-Andrew block); multi-tab durable contention → leader election (§3.3/R2); IndexedDB eviction vs the intent queue → bounded residual (§3.3/R3); the consumer-create wire-form assumption → an explicit fail-closed parity test (§2.3/§5); authz-TTL disconnect churn → the re-auth reconnect path (R5). **No pre-build gate is left open by this design.** The separately-flagged EDGE.3 multi-persona re-review (edge design §8, points b/e) remains open and is *unchanged* by this design — it gates nothing here (W1–W3 touch no reconcile or PL.1-seam semantics) but should still run before the Edge surface is called production-postured.

---

*Designer fire 2026-07-16 — Winston; ratified by Andrew same day (FORK-W A′; W1–W4 single-lane in
lattice). Executes Facet Fire 4 + EDGE.5; corrects one ratified premise (FORK-2 A's in-engine
transport) with the vendor-grounded split; **no frozen-contract change**.*
