# Loupe 2.0 — platform-edges extension (PO brief)

**Author:** Loupe PO (Winston, owner routine) · **For:** Sally (UX Designer) → Winston adjudication → Loupe Steward
**Status: PO brief — filed 2026-07-02 (Andrew session). Extends the Loupe 2.0 program with fires F10–F13.**
**Altitude:** PO vision + curated topology spec. Sally extends the 2.0 UX design
([loupe-2-ux-design.md](loupe-2-ux-design.md) §14+) with these fires; this brief is the ground truth it builds on.
Not a new program — the same "the map is the console" thesis, now absorbing the components landing at Lattice's
edges and gaining a history dimension.

---

## 0. Why these fires extend Loupe 2.0

Loupe 2.0 makes the System Map the home of a navigable console — but every fire in it (F1–F9) predates the
three platform components now landing at Lattice's edges:

- **Gateway** — built (Fires 1–2), the external write-path trust boundary. Not wired into `make up-full`, so it
  has never appeared on the map. If it *were* running it would render as an anonymous "client" chip, not the
  first-class external door it is.
- **Vault** — ratified (`vault-crypto-shredding-design.md`), sequenced behind D1. Crypto-shredding / right-to-
  be-forgotten. No map node, no page, no surface.
- **Chronicler** — ratified (`orchestration-history-read-model-design.md`), unbuilt. Append-only history
  materializer. Its own design defers display to Loupe 2.0 F6 — which undersells it badly (see §4).

The 2.0 map is a curated six — [`declaredComponents`](../../cmd/loupe/systemmap.go) + `skeletonEdges`. These
fires curate the three new components onto it and build each one's signature operator surface, culminating in
the Chronicler-powered **Time Machine** — Loupe's fourth leg. Same program, four more fires.

## 1. Decisions locked (Andrew, 2026-07-02)

1. **The map stays curated, not self-describing.** Hand-add the three components to `declaredComponents` /
   `skeletonEdges` / `sysmapTier` with deliberate placement (§2). No manifest/auto-derive rearchitecture.
2. **The agent-activity console stays shelved — out of scope, do not raise.** It would require Loupe to read
   the repo, which is a hard security no. It reappears only in a distant future where Lattice feature-
   development tracking lives *on Lattice itself*. The `#sysmap-console` slot stays reserved and empty.
3. **Design-ahead all three.** Only Gateway is buildable near-term (Vault behind D1, Chronicler behind its own
   build); produce the full UX for all three now so each build starts the moment its dependency clears.
4. **Extend 2.0, don't fork a new program.** These are fires F10–F13 in the existing 2.0 backlog (Andrew:
   2.0 is half-built and this is the same thesis).

## 2. The curated topology (the map)

Andrew's placement, mapped onto the existing tier model
([`sysmapTier`](../../cmd/loupe/web/js/logic/status.js): core-operations=0, Processor=1, core-kv/core-events=2,
other components=3, lenses=4):

- **Gateway — the one external door.** A new top-of-map node *above* core-operations: the entry point for
  external actors publishing to `core-operations`. Edges: an inbound "external actors · Bearer JWT" marker →
  `gateway`, and `gateway → core-operations` ("stamp + publish"). Write-path only in Fire 1 (it feeds
  core-operations, nothing else). Implementation: a new top tier (shift the spine down, or a dedicated ingress
  row), a `declaredComponents` entry, two `skeletonEdges`, a `sysmapTier` case.
- **Vault — key custody, to the side of Core-KV.** A node lateral to `core-kv` (tier-2 band, offset). It is
  *not* in the data-flow spine — it holds the keys that make sensitive Core-KV aspects ciphertext, deliberately
  outside Core-KV. Edge: `processor → vault` ("encrypt / decrypt" — the Processor encrypts on commit step 6.5
  and decrypts on read through Vault). Renders even without a heartbeat until Vault reports (design its
  absent/pending state honestly).
- **Chronicler — the mirror of Refractor.** A tier-3 materializer on the *opposite* side from Refractor.
  Refractor projects present state off Core-KV CDC (left); the Chronicler materializes append-only history off
  all the streams (right). Inbound edges: `core-operations → chronicler`, `core-events → chronicler`,
  `core-kv → chronicler` (+ `events.loom.>` / `events.weaver.>` as those producers land). Outbound: its history
  read-model targets (Loupe reads via P5) and, in archive mode, the object-store plane.

The result reads as a complete portrait: **one door in (Gateway) → the spine (ops → Processor → KV + events) →
two mirror materializers (Refractor present / Chronicler history) → key custody to the side (Vault) → the
engines reacting and submitting back.** The map *is* the architecture diagram, live.

Deploy dependency (cross-lane, → lattice.md): get the Gateway binary into `make up-full` so it heartbeats and
the node is truthful, not a ghost. Without this, Gateway on the map is aspirational.

## 3. The three signature surfaces

Each component gets the F3 treatment — first-class map node + `#/component/<id>` page — **plus** a signature
surface that makes it a reason to open Loupe.

### 3.1 Gateway — the security console (buildable now)

- **Page:** auth-failure rate, `requests_total` / `ops_submitted_total`, and the live **JWKS key set** — which
  `kid`s are trusted, source (dir / URL / dev), last poll, hot-swap history. The `JWKSPoller` story, visible.
- **Signature — the revoke surface.** The arch review (2026-07-02) found the token-revocation kill-switch
  exists only in code: no bucket, no surface, silent downgrade. Make Loupe *the* revoke surface — revoke an
  actor from the console, watch the next forged request 403. Loupe becomes the edge security console.
  (Revocation-bucket provisioning may be a cross-lane lattice ask — scope during UX.)

### 3.2 Vault — the crypto-shred proof (behind D1)

- **Page:** DEK count, shred events, the privacy-critical failure tier, backend (local envelope / KMS).
- **Signature #1 — Reveal.** Loupe is a *named* trusted plaintext consumer via the Vault decrypt RPC (per the
  ratified design). In the Graph explorer, a `sensitive:true` aspect renders as ciphertext with an audited
  **Reveal** button — encryption-at-rest, made visible and honest.
- **Signature #2 — the shred proof.** Run `ShredIdentityKey` from Loupe; watch the same aspect go readable →
  permanent gibberish across live KV *and* JetStream history *and* every projection, on one screen. Pair with
  §4 Layer 3 for the full "audit + erasure simultaneously true" demo.

### 3.3 Chronicler — the Time Machine (behind its build)

The biggest new capability. Detailed in §4.

## 4. The Time Machine — Loupe's fourth leg

Loupe 2.0's three legs: **graph** (Lattice is a graph), **lenses** (the read path), **events-live**
(convergence, watched live). The Chronicler adds the fourth — **history**: the past, queryable and replayable.
Four layers, each riding a Chronicler fire.

**Layer 1 — Flow history ("what happened").** Rides Chronicler F1/F2 (`loomFlowHistory` + Weaver history read
models). Today a Loom flow vanishes at terminal — `loom-state` deletes its cursor — so "why did the 3am
onboarding fail?" is unanswerable. The Chronicler projects one durable row per flow instance. Loupe surface: a
faceted browser (by type / outcome / time), each flow clickable into its full lifecycle timeline — every step,
event, and op it submitted. The concrete near-term win.

**Layer 2 — The map scrubber ("replay it").** A timeline slider under the System Map. F6's live pulse is only
the *now* edge — drag left and the map re-renders the platform's state at that instant (flows in flight, edges
pulsing, the rollup), replayed as animation from Chronicler history instead of the live SSE tail. Watch Lattice
think, then rewind.

**Layer 3 — The ledger browser ("prove it").** Rides Chronicler F3 (archive mode: the core-operations intent-
ledger archived verbatim to the object-store plane — sequenced segments, prev-segment hash chain). Browse the
immutable, ordered record of every operation ever submitted; the tamper-evident audit surface. **The pairing
with Vault (§3.2) is the flagship demo:** here is the ledger of everything; here is the identity we shredded;
its PII is permanent gibberish inside the ledger while the integrity chain is intact. Durable audit **and**
right-to-be-forgotten, both true, on an append-only ledger.

**Layer 4 — Point-in-time reconstruction (the horizon).** FR51 (`historical-state-query-design.md`) re-homes
its archive layers to the Chronicler. "Show me the whole graph as of last Tuesday 14:00" — reconstruct any
vertex's past state by replaying the ledger. Not near-term; where the Time Machine ultimately goes.

**PO call:** the Chronicler design says "display rides Loupe 2.0 F6 — no cmd/loupe work here." Overridden within
the Loupe lane: F6 is a live tail; the Time Machine is a history browser + scrubber + ledger. It earns its own
fire (F13), not a footnote on F6. (No change to the Chronicler build itself — this is display scope.)

## 5. Sequencing & dependencies

| Fire | Depends on | Buildable |
|---|---|---|
| F10 — curated topology (3 nodes/edges) | Gateway → `up-full` (deploy, lattice lane) for Gateway to be live | **Now** (Vault/Chronicler render absent/pending honestly until they report) |
| F11 — Gateway security console | Gateway in `up-full`; revoke bucket (maybe lattice) | **Near-term** |
| F12 — Vault reveal + shred proof | Vault build (behind D1) | Blocked — design-ahead |
| F13 — Chronicler Time Machine (L1–L3) | Chronicler build (F1–F3, lattice lane; `chronicler-prebuild-regrounding` first) | Blocked — design-ahead |
| (horizon) point-in-time (L4) | FR51 revive + Chronicler archive | Horizon |

Near-term order: (1) F10 curated map + Gateway live, (2) F11 Gateway console, (3) F12/F13 designed-ahead and
ready to build the moment each dependency clears.

## 6. Fires (board-tracked in loupe.md, F10–F13)

- **F10 — Curated topology + Gateway node.** `declaredComponents`/`skeletonEdges`/`sysmapTier` for all three;
  Gateway into `up-full` (cross-lane deploy). Vault/Chronicler render as honest absent/pending until live.
- **F11 — Gateway security console.** Component page (auth metrics + JWKS key set) + the revoke surface.
- **F12 — Vault surface.** Node + page + Reveal (decrypt RPC) + the crypto-shred proof flow. (blocked: D1 + Vault)
- **F13 — Chronicler Time Machine.** Flow-history browser + map scrubber + ledger browser (L1–L3). (blocked:
  Chronicler build)
- **Design-ahead (X):** Sally extends the 2.0 UX design (§14+) with F10–F13 per this brief — the immediate
  deliverable; start F10/F11 (buildable), design F12/F13 ahead.

## 7. Handoff

PO brief filed → **Sally extends the 2.0 UX design** (§14+) per §2–§4 (start F10/F11 — buildable — then F12/F13
design-ahead) → **Winston adjudicates** (Andrew-delegated for the Loupe lane) → **Loupe Steward builds**, one
fire at a time, on `/tmp/lattice-loupe-build.lock`. Cross-lane asks (Gateway→`up-full`, revoke bucket) file to
`lattice.md` and `🚧 blocked-on:` the dependent fire.
