# Edge

**Component reference** | Audience: operators + implementers

> Edge is an **application** (`internal/edge/*`, eventually `cmd/edge`), not a platform engine — it has
> no frozen interface contract of its own. Its framing of record is
> `_bmad-output/implementation-artifacts/edge-lattice-full-design.md` (✅ Andrew-ratified) and the
> *Edge & personal lenses* row of `_bmad-output/planning-artifacts/backlog/lattice.md`. Update this page
> in the same commit as the code; drift between page and code is a documentation bug.

---

## Overview

Edge is the sovereign per-user node design's Go reference implementation: a device holds a **local VAL
mirror** of just its authorized slice, kept fresh by the Personal Lens delta stream (`refractor.md`,
`lattice.sync.user.<id>`), and reconciles by revision rather than trusting a local authoritative writer —
the cloud Processor remains the platform's **sole authority** (P2 is untouched; see the design's FORK-A
resolution). Edge composes five sub-components (design §3); each maps to its own `internal/edge/*`
package, built incrementally per the design's §7 Steward decomposition (EDGE.1 → EDGE.6).

## Status

**EDGE.1 + EDGE.2 + EDGE.3 done.** Shipped so far:

- **`internal/edge/store`** — the Local VAL Store (design §3.1): an embedded, transactional local KV
  keyed by the exact Contract #1 key strings (`vtx.<type>.<id>`, `vtx.<type>.<id>.<localName>`,
  `lnk.<typeA>.<idA>.<rel>.<typeB>.<idB>`). Each entry carries the projected fragment plus the cloud
  revision that produced it. `ApplyUpsert`/`ApplyDelete` implement **last-writer-wins by revision** — a
  write applies iff its revision is ≥ the currently-stored one, so a stale/duplicate/reordered delta
  (JetStream is at-least-once and can reorder) is dropped, never applied out of order. A `Cursor`/
  `SetCursor` pair persists the Sync Manager's last-applied stream sequence across restarts. A separate
  `local:` namespace (`PutLocal`/`GetLocal`) scaffolds the design's **sovereign, device-only** entries —
  ones a user creates locally that are never uploaded — kept in its own bucket/object store so the
  mirror's apply path can never reach it.

  `Store` is an **interface with two engines**, because the same semantics run on two hosts:

  | Engine | Host | Build |
  |---|---|---|
  | `BoltStore` (`bolt.go`, `bbolt`) | the trusted Go hosts (`cmd/edge`, `cmd/facet`) | `!js` — bbolt is mmap-based and has no js/wasm build |
  | `IDBStore` (`idb.go`, IndexedDB via `syscall/js`) | the browser node (EDGE.5 W3) | `js` |

  Neither engine is the definition. **`store/storetest` is**: a conformance suite both engines answer
  to, so a port is proven to have preserved last-writer-wins, FIFO intent order, and durability across a
  reopen — rather than merely to have compiled. The browser engine runs it against a **real IndexedDB in
  a real headless Chrome** (`make test-edge-idb-conformance`; CI job `edge-browser-store`), since Node
  ships no IndexedDB and a fake would only prove the port matches someone's reimplementation of the
  engine the PWA actually runs on.

  Two IndexedDB properties the port is written around (authority + rationale in
  [`docs/vendors.md`](../vendors.md)): a transaction is active only while one of its requests is pending,
  so the last-writer-wins read-modify-write issues its write from **inside** the read's success callback
  rather than after a Go channel round-trip; and a transaction's `complete` event — not a request's
  success — is the durability point, so every write awaits it.
- **`internal/edge/sync`** — the Sync Manager (design §3.2): a durable JetStream consumer
  (`substrate.RunDurableConsumer`, stable per-`(identityId, deviceId)` durable name) on the Personal-Lens
  `SYNC` stream, filtered to the actor's own `lattice.sync.user.<id>` subject. Each delivered delta drives
  `store.ApplyUpsert`/`ApplyDelete` and advances `store.SetCursor` to the message's stream sequence — a
  malformed envelope is `Term`inated (poison, never redelivered), an apply failure is `Nak`ed for retry. On
  cold start (no local cursor) or a detected **gap**, it calls the Personal-Lens
  `personal.register`/`personal.hydrate` control RPCs (`internal/refractor/control`) before subscribing; a
  warm cursor still within retention skips both and resumes incrementally from the durable's own ack floor.
  Gap detection is a control-plane RPC, not a JetStream admin call: the node holds no `$JS.API.STREAM.*`
  grant, so `gapped()` asks `personal.syncgap` (request `{identityId, deviceId, cursor}`, response
  `{personalSyncGap: {gapped}}`) — the Refractor compares the cursor to the SYNC stream's earliest retained
  sequence on its own full-grant read and returns one boolean; a transient control-plane unavailability at
  warm boot is retried with bounded backoff, then fails closed (never resume unverified).
  Control-plane requests carry a `Lattice-Actor` header stamped with the device's bearer JWT
  (`EDGE_TOKEN`) — when the Refractor's `ActorVerifier` is configured, `personal.register`/`deregister`/
  `hydrate`/`sessionkey`/`syncgap` bind to the verified actor server-side, overriding any `identityId` the request body asserts
  (per-identity-nats-subscribe-acl-design.md §3.4); no verifier configured preserves the self-asserted
  body (dev/e2e fixtures).
- **`internal/edge/overlay`** (design §3.4, the Edge "Processor" — pure-A this increment, no local
  Starlark prediction yet): `Apply` installs the caller-supplied intended value as a pending overlay over
  a key, visible immediately through `Read`; the overlay retires the instant ANY fresher confirmed value
  lands for that key (the intent's own eventual commit or an unrelated concurrent write) — R3's "cleared
  by the authoritative cloud value, never local success alone." `Discard` drops a rejected intent's
  overlay. `Links` answers "UI Discovery" — a presentation-only read enumerating confirmed + pending link
  keys incident on a hub, merging pending creations/deletions.
- **`internal/edge/agent`** (design §3.5) — the durable intent uploader + reconcile-by-revision:
  `Enqueue` durably queues an operation envelope (called after `overlay.Apply`); `Drain` submits every
  queued intent in FIFO order via a pluggable `Submitter`, stopping at the first transport failure so a
  later `Drain` resumes. **`GatewaySubmitter`** (EDGE.3, `submit_gateway.go`) POSTs to the Gateway's
  `/v1/operations` presenting `EDGE_TOKEN` as the caller's own Bearer credential — the Gateway
  re-verifies the token and stamps the verified subject as `env.Actor` itself, so a denied/revoked token
  is refused before any envelope ever reaches `core-operations`; this is `cmd/edge`'s production
  Submitter. **`NATSSubmitter`** (`submit_nats.go`) is the EDGE.1/2 trusted-posture direct-to-
  `core-operations` submitter, kept for tests and any fully-trusted deployment run without a Gateway. A
  `RevisionConflict` reply — the only hard case, the cloud state moved under the offline edit — triggers
  a full re-hydrate (no anchor-scoped hydrate RPC ships yet, so `sync.Manager.Rehydrate` reuses the
  existing `personal.hydrate` call) before discarding the stale overlay; any other rejection discards
  without re-hydrating. `GC` sweeps pending overlays a `Read` never revisited.
- **`cmd/edge`** — the binary wiring `store` + `sync` + `overlay` + `agent` together (mirrors `cmd/loupe`'s
  flat layout): `EDGE_STORE_PATH`/`NATS_URL`/`EDGE_GATEWAY_URL`/`EDGE_IDENTITY_ID`/`EDGE_DEVICE_ID`/
  `EDGE_TOKEN` env config, connects to NATS via the auth-callout boundary (`substrate.ConnectOpts.Token`
  + a `_INBOX.edge.<id>`-scoped `InboxPrefix`, per-identity-nats-subscribe-acl-design.md §3.3), runs the
  Sync Manager, and drains the agent's intent queue (via `GatewaySubmitter`) + sweeps overlay GC on a
  fixed interval (submit-on-reconnect rides the NATS client's own auto-reconnect) until SIGINT/SIGTERM.
  `EDGE_TOKEN` is the sole credential, authenticating both the NATS connection and every Gateway submit —
  `EDGE_ACTOR_KEY` self-assertion has retired.

- **`internal/edge/vault`** (EDGE.4) — the transient session-key Vault Proxy for sensitive aspects: an
  identity-bound `personal.sessionkey` control RPC + a TTL-cached client that AEAD-opens sensitive values
  locally via `vault.OpenWithSessionKey`. `Reader` composes it over `overlay.Read`; it stays unwired until
  a sensitive-display consumer exists.

**Not yet built** (see the design doc §7 for the full fire-by-fire plan):

- **EDGE.5** — the browser/mobile node ([edge-browser-node-design.md](../../_bmad-output/implementation-artifacts/edge-browser-node-design.md),
  ✅ ratified). It is **not** gated on a Gateway WS bridge — no such component exists or is planned.
  WebSocket is a native NATS listener (a `websocket {}` block, shipped by fire W1 below); the only
  genuinely undesigned piece is the **push-waker** (background wake when the tab is dead), deferred to
  Facet Stage 3. W1–W3 are in (native WS listener, the store/transport seams, the wasm host + JS shell
  over vendored `nats.js`); W4 makes Facet browser-native. W4 inc 1–3 wired the renderer feed-source
  swap; **inc 4** turns `cmd/facet` into a static host for the in-page engine — see below. The remaining
  W4 tail is the cross-machine, no-binary Gate-3 e2e (the ratified Fire-4 green bar).

**Browser-native serving mode (`FACET_BROWSER_ENGINE`, W4 inc 4).** With the flag set, `cmd/facet` stops
being the engine host and becomes a static file server: it serves the wasm artifact + the JS shell
(`FACET_EDGE_WASM_DIR`, default `bin/edge-wasm` — run `make build-edge-wasm` first; `FACET_EDGE_SHELL_DIR`,
default `internal/edge/browser/shell`) and rewrites the app-shell index to carry a per-session
`window.__EDGE_BOOT__ = {identityId, wsUrl, gatewayUrl, token}` (`EDGE_WS_URL`, default the `:9222`
listener). `boot.mjs` reads it and starts the in-page engine over WebSocket, so the browser does the
projection with **no local Go engine**. Two consequences worth stating plainly: (1) the bearer JWT now
lives in the page body — it *must*, since `nats.js` is a JS client and cannot use an HttpOnly cookie — so
the injected page is `Cache-Control: no-store` and rides the same ≤15 m authz TTL as the Go host's own
NATS connection (this is the mode's inherent trade for dropping the local binary; the shipped Go host,
flag unset, keeps the token HttpOnly and is byte-for-byte unchanged). (2) The **device id is
browser-local** (localStorage, `boot.mjs`'s `resolveDeviceId`), not injected — persisted so a reload
resumes the same durable consumer instead of orphaning one per load.

**The browser-build boundary.** The engine's semantics packages compile under `GOOS=js GOARCH=wasm` and
reach **no NATS client** — CI asserts both (a build check, and a `go list -deps` assertion over the same
package set). That confinement is what keeps the browser node honest: its only sanctioned write door is
the Gateway (Fetch) and its only read door is the JS shell's WebSocket, so a linked NATS client would be
both dead weight in the artifact and a trusted transport waiting to be wired around the boundary. Two
mechanisms hold it:

- **Build tags** keep the trusted, pre-Gateway paths off the browser entirely: `internal/edge/transport/
  natstransport` (the substrate `DeltaSource`/`ControlClient` adapters) and `agent/submit_nats.go` (the
  `NATSSubmitter`) are `!js`.
- **Wire-leaf packages** keep NATS from arriving by accident. The DTOs the engine needs used to sit beside
  server machinery — an `OperationEnvelope` lived in `internal/processor`, a `ControlRequest` in
  `internal/refractor/control` — so referencing a struct linked a NATS client. Each now has a leaf package
  holding the wire format and nothing else: `internal/processor/opwire`, `internal/refractor/control/
  controlwire`, `internal/refractor/health/healthwire`, `internal/vault/vaultwire`, and
  `internal/substrate/keys`. The parent packages re-export them as **type aliases**, so platform call sites
  read as processor/control/vault vocabulary and nothing changed shape; the Edge imports the leaves. Client
  and server share one definition rather than each declaring their own — a re-declared struct is exactly the
  drift hazard the round-trip test (edge design §8.1 RR-4) exists to catch.

### Transports

The engine is host-coupled only through its dial URL. NATS exposes two listeners, and the shipped
per-identity auth callout is **transport-invariant** — it consumes exactly the bearer token and the device
name, splices every allowed subject from the *verified* identity, and never sees the listener type:

| Listener | Port | Who dials it |
|---|---|---|
| TCP | 4222 | `cmd/edge`, `cmd/facet`, every platform component |
| WebSocket | 9222 (dev `no_tls`) | the browser Edge node (EDGE.5 W3+) |

That invariance is proven, not asserted: `internal/natsperm`'s six Edge auth-callout vectors (acl-design
§8 vectors 1–4, 6, 7 — own-slice allow, cross-identity deny, the fail-closed cases, revocation, delta
forgery, inbox isolation) each run twice, once per transport, via `forEachEdgeTransport`, against the real
committed conf. The platform components (§8 vector 5, the NKey users) stay TCP-only: they bypass the
callout entirely and are not browser-facing. The `websocket {}` block is generated by
`natsperm.RenderConf` — never hand-edited — and pinned by `TestWebsocketConfigured`.

**Origins are a second, independent surface** from the Gateway's CORS allow-set: a browser's WS handshake
is gated by NATS's own `allowed_origins`, which the conf always renders explicitly because **NATS reads an
empty list as allow-any-origin**. `TestWebsocketOriginEnforced` drives the real handshake (403 on a
disallowed origin). The origin gate is CSRF-class hardening for browser-initiated connects only — a
non-browser client sends no `Origin` header and is accepted by design (RFC 6455 §1.6); the bearer token
remains the trust boundary. Dev ships `no_tls`; **production must ship a `tls{}` block** — an ops fire.

**EDGE.3 (untrusted multi-identity) is live**: the node authenticates via a Gateway-verified JWT
(Contract #11), reads the Personal Lens PL.3 security-filtered SYNC stream, connects under the
per-identity NATS subscribe-ACL, and submits every intent through the Gateway (not directly to
`core-operations`) — `internal/gateway`'s `TestEdgeGate3_*` tests prove a valid token submits and a
revoked token is denied before ever reaching the Processor. Sensitive-aspect confidentiality is still
EDGE.4; until that lands, a sensitive delta is unreadable ciphertext on the wire and in the local store,
never a Gate-3 exposure.

## Grounding

- `_bmad-output/implementation-artifacts/edge-lattice-full-design.md` — the full design, forks, and
  §7 Steward decomposition.
- `_bmad-output/implementation-artifacts/personal-secure-lens-design.md` — the cloud-side producer
  (`nats_subject` adapter, `SYNC` stream, delta envelope, hydration/register control RPCs) Edge consumes.
- `docs/contracts/01-addressing-and-envelope.md` §1.1 — the key shapes the local store mirrors
  byte-for-byte.
- `docs/vendors.md` — `go.etcd.io/bbolt` (the Go hosts' embedded KV), **IndexedDB** (the browser host's,
  incl. the transaction-lifetime and key-generator semantics the port is written around), and
  **wasmbrowsertest** (the headless-Chrome runner the browser conformance gate uses).
