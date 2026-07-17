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
  (`bbolt`) keyed by the exact Contract #1 key strings (`vtx.<type>.<id>`, `vtx.<type>.<id>.<localName>`,
  `lnk.<typeA>.<idA>.<rel>.<typeB>.<idB>`). Each entry carries the projected fragment plus the cloud
  revision that produced it. `ApplyUpsert`/`ApplyDelete` implement **last-writer-wins by revision** — a
  write applies iff its revision is ≥ the currently-stored one, so a stale/duplicate/reordered delta
  (JetStream is at-least-once and can reorder) is dropped, never applied out of order. A `Cursor`/
  `SetCursor` pair persists the Sync Manager's last-applied stream sequence across restarts. A separate
  `local:` bbolt bucket (`PutLocal`/`GetLocal`) scaffolds the design's **sovereign, device-only**
  namespace — entries a user creates locally that are never uploaded — kept in its own bucket so the
  mirror's apply path can never reach it.
- **`internal/edge/sync`** — the Sync Manager (design §3.2): a durable JetStream consumer
  (`substrate.RunDurableConsumer`, stable per-`(identityId, deviceId)` durable name) on the Personal-Lens
  `SYNC` stream, filtered to the actor's own `lattice.sync.user.<id>` subject. Each delivered delta drives
  `store.ApplyUpsert`/`ApplyDelete` and advances `store.SetCursor` to the message's stream sequence — a
  malformed envelope is `Term`inated (poison, never redelivered), an apply failure is `Nak`ed for retry. On
  cold start (no local cursor) or a detected **gap** (the local cursor has fallen behind the SYNC stream's
  current `FirstSeq` — retention pruned messages the node never saw), it calls the Personal-Lens
  `personal.register`/`personal.hydrate` control RPCs (`internal/refractor/control`) before subscribing; a
  warm cursor still within retention skips both and resumes incrementally from the durable's own ack floor.
  Control-plane requests carry a `Lattice-Actor` header stamped with the device's bearer JWT
  (`EDGE_TOKEN`) — when the Refractor's `ActorVerifier` is configured, `personal.register`/`deregister`/
  `hydrate` bind to the verified actor server-side, overriding any `identityId` the request body asserts
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
  Facet Stage 3. Remaining: W2 (engine seams), W3 (the wasm host + JS shell), W4 (Facet browser-native).

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
- `docs/vendors.md` — `go.etcd.io/bbolt`, the local store's embedded KV.
