# Loupe 2.0 — platform-edges extension (UX design, F10–F13)

**Author:** Sally (UX Designer) · **For:** FE Engineer + Loupe Steward (build lane) · **Adjudication:** Winston (Andrew-delegated, Loupe lane)
**Status: ADJUDICATED — Winston, 2026-07-02 (Andrew-delegated). Build authorized per §7; F10 buildable-first,
F11–F13 gated on the cross-lane asks (§6, filed to lattice.md). Extends the Loupe 2.0 program with fires
F10–F13 per the PO brief ([loupe-platform-edges.md](loupe-platform-edges.md)). Companion to
[loupe-2-ux-design.md](loupe-2-ux-design.md) — same "the map is the console" thesis, same stack constraints,
same conventions; the program's §14 fire table gains F10–F13 (see §7 below).**

> **Adjudication (Winston, 2026-07-02 — folds in Andrew's calls; resolves §8).**
> - **Grants (Andrew: both).** Loupe's operator actor is granted **`ShredIdentityKey`** (F12 proof — the op
>   already shipped `604342b`, so this is a scoped grant, not a build) and **`RevokeActor`** (F11 — see revoke
>   below). Filed as the Vault→Loupe enablers row + the revoke item on lattice.md (§6).
> - **Design-ahead map (Andrew: show).** Vault/Chronicler render as `design-ahead` per §1.4 — the map shows
>   the roadmap; nodes auto-go-live on first heartbeat.
> - **Revoke mechanism (Andrew's steer — refines §2.3, supersedes both the operational-bucket and the
>   projected-lens shapes).** A `RevokeActor` op **outboxes a revocation event** the **Gateway consumes into
>   its own internal-state KV** (its per-request check reads locally). Keeps Loupe write-free (it submits the
>   op), lands the revocation in the Chronicler ledger (auditable), and gives the Gateway a fast local check.
>   **The mechanism does not exist yet** — the existing `gateway-revocation-kill-switch-activation` row
>   (lattice.md) is refined to a **Designer** item carrying this steer, and **blocks F11's revoke surface**.
> - **Accepted as-is (Winston):** Vault `lateral` render-flag (§1.2); ship the **v1 flow-liveness scrubber**
>   first, edge-level pulse as the F3-gated upgrade behind the same UI (§4.2).

**Why a companion doc, not an append.** The program doc is already ~740 lines and its §14 explicitly
anticipates cross-linked follow-on fires ("the flagged Loupe-side PG seam ships in F9"). F10–F13 add a new
*topology dimension* (three components at the edges) and a whole new *leg* (history / the Time Machine),
so they read better as one coherent extension than as four more rows bolted into the middle of the
existing body. This doc is authoritative for F10–F13; everything it does not restate inherits from the
program doc.

**Stack constraint (inherited, binding — restated so a build sub-agent needn't chase it):** vanilla ES
modules + `go:embed`, **no Node, no framework, no build step, no CDN** — offline on `127.0.0.1:7777`.
Dark theme on the **existing** `style.css` tokens (`--bg / --bg-raised / --bg-input / --border / --text /
--text-dim / --accent / --accent-dim / --green / --yellow / --red / --mono`) — **no new `:root`
tokens.** New `cmd/loupe` Go endpoints are in-scope; **control-plane op allow-lists are FIXED**
(`cmd/loupe/control.go`) — any new privileged surface (revoke / decrypt / shred) needs a new, explicitly
called-out endpoint. The logic/DOM split stays goja-testable (`logic/*.js` = declarations + one trailing
`export {…}`, ES6-conservative; DOM/render/fetch stays out). The map stays **curated** (no
self-describing rearchitecture). The `#sysmap-console` slot stays **reserved and empty** (agent console
shelved — Loupe never reads the repo).

---

## 0. What these fires add — two new dimensions on the same console

Loupe 2.0 (F1–F9) built a navigable console on three legs: **graph** (Lattice is a graph), **lenses**
(the read path), **events-live** (convergence, watched live). F10–F13 extend it along two axes the
program didn't have:

1. **The edges of the platform become first-class on the map (F10–F12).** Three components land at
   Lattice's trust/custody/history boundaries — **Gateway** (the external write-path door), **Vault**
   (key custody for crypto-shred), **Chronicler** (append-only history materializer). Each gets the F3
   treatment (curated map node + `#/component/<id>` page) **plus** a signature operator surface: the
   Gateway **security console** (JWKS + revoke), the Vault **Reveal + crypto-shred proof**.
2. **History becomes the fourth leg (F13).** The Chronicler makes the past queryable and replayable — the
   **Time Machine**: a flow-history browser, a map scrubber (F6's live pulse becomes the *now* edge of a
   timeline you can drag back), and a ledger browser over the immutable archive, culminating in the
   flagship **Vault-shred × ledger pairing** (durable audit *and* right-to-be-forgotten, both true).

**Buildable now vs design-ahead.** Only **Gateway** is near-term (built as `cmd/gateway`, just not in
`make up-full` yet). **Vault** and **Chronicler** are ratified-and-being-built on the Lattice lane but not
yet deployed/queryable, so their Loupe surfaces are **design-ahead**: fully specified here so the build
starts the moment each backend dependency clears, with every not-yet-built-backend assumption flagged
inline (⚠️ **ASSUMES**). This matches the PO's "design-ahead all three" decision.

---

## 1. F10 — Curated topology: Gateway, Vault, Chronicler on the map

The 2.0 map is a curated six (`declaredComponents`) hung off the infra spine (`infraNodes`) with the
tier model in `sysmapTier` (core-operations=0, Processor=1, core-kv/core-events=2, other components=3,
lenses=4). F10 hand-adds three nodes with deliberate placement, so the map reads as **one door in →
spine → two mirror materializers → key custody to the side**.

### 1.1 The target portrait

```
        ┌───────────────────────────── external actors · Bearer JWT ─────────────────────────────┐
        ▼                                                                                          (ingress marker, tier -1)
   ╔═══════════╗
   ║  Gateway  ║   ← the one external door (tier -1, above the spine)
   ╚═══════════╝
        │ stamp + publish
        ▼
   [core-operations]  ── tier 0 ──────────────────────────────────────────────────────────────────
        │ ops
        ▼
    ( Processor )  ── tier 1 ──  ── encrypt / decrypt ──▶  ╔═══════╗
        │ commit / outbox                                  ║ Vault ║  ← key custody, LATERAL to Core-KV
        ├───────────────┐                                  ╚═══════╝     (tier 2, offset right — NOT in the spine)
        ▼               ▼
   [core-kv]      [core-events]  ── tier 2 (spine) ──────────────────────────────────────────────
        │  CDC          │  consume / archive-in
        ▼               ▼
 ( Refractor )   …engines…   ( Chronicler )  ── tier 3 ── Refractor LEFT (present state) / Chronicler RIGHT (history)
        │ project                    │ project (history read-models)  ├─ archive ─▶ [object-store]
        ▼                            ▼
   ‹ lens shelf ›              ‹ history read-models ›  ── tier 4 ──────────────────────────────────
```

Refractor (present state, off Core-KV CDC, left) and Chronicler (append-only history, off all the
streams, right) are visual mirrors at tier 3 — the map *is* the architecture diagram, live.

### 1.2 Node / edge / tier deltas (concrete, build-ready)

**`declaredComponents` (`cmd/loupe/systemmap.go`)** — add three, in render order after the existing six.
Because `classifyHealthKey` groups `health.<component>.<instance>` by component id, a declared component
is picked up automatically the moment it heartbeats — no other server wiring needed for presence:

```go
var declaredComponents = []declaredComponent{
    {"processor", "Processor"},
    {refractorID, "Refractor"},
    {"weaver", "Weaver"},
    {"loom", "Loom"},
    {"bridge", "Bridge"},
    {"object-store-manager", "Object Store Mgr"},
    {"gateway", "Gateway"},
    {"vault", "Vault"},
    {"chronicler", "Chronicler"},
}
```

**Health-KV group ids** must match the heartbeaters exactly: `gateway` (`health.gateway.<instance>`, per
`docs/components/gateway.md` §Health — already emitting), `vault`, `chronicler`. ⚠️ **ASSUMES** the Vault
and Chronicler binaries heartbeat under those group ids (Vault's design reserves `vault_calls_total` /
`keyshredded_handled_total` on the *Refractor/privacy-worker* heartbeat today — a dedicated
`health.vault.*` heartbeat is a small Lattice-lane ask, §6). Until they do, the nodes render **absent /
design-ahead** (§1.4), which is the honest state.

**`skeletonEdges` (`cmd/loupe/systemmap.go`)** — add the edges. Gateway feeds only core-operations (Fire
1 is write-path only); Vault is a lateral side-node off the Processor (not in the spine); Chronicler
mirrors Refractor's inbound-from-streams / outbound-to-targets shape:

```go
// Gateway — the external door (write-path only).
{From: "external", To: "gateway", Label: "external actors · Bearer JWT"},
{From: "gateway", To: "core-operations", Label: "stamp + publish"},
// Vault — key custody, lateral (Processor encrypts on commit step 6.5, decrypts on read).
{From: "processor", To: "vault", Label: "encrypt / decrypt"},
// Chronicler — the mirror materializer: inbound from the streams, outbound to its history read-models
// + (archive mode) the object-store plane.
{From: "core-operations", To: "chronicler", Label: "archive"},
{From: "core-events", To: "chronicler", Label: "history"},
{From: "core-kv", To: "chronicler", Label: "CDC"},
{From: "chronicler", To: "object-store", Label: "archive segments"},
```

Two supporting **infra-ish nodes** are needed as edge endpoints:

- **`external`** — the ingress marker above the Gateway. Not a component (nothing heartbeats), not really
  infra either. Render it as a new node kind **`ingress`** (a labelled chip "external actors · Bearer
  JWT", non-interactive, no status dot) so `buildSysmapNode` gives it a distinct, deliberately-plain
  treatment. Placed at **tier -1** with the Gateway (§1.3). Add to a small `ingressNodes` slice alongside
  `infraNodes`, or synthesize it in `computeSystemMap` — either is a code change, no contract surface.
- **`object-store`** — the archive sink for Chronicler archive mode. This is the object-store *plane*
  (`objects-base`), distinct from the `object-store-manager` *component* node that already exists. Render
  it as an **`infra`** node (a store on the spine's edge, like `core-kv`), id `object-store`, label
  "Object Store". Add to `infraNodes`. ⚠️ **ASSUMES** archive mode (Chronicler F3) is live; until then
  the `chronicler → object-store` edge simply doesn't resolve (both endpoints must exist for
  `drawSysmapEdges` to draw it) — see §1.4, and the edge renders once both land.

**`sysmapTier` (`cmd/loupe/web/js/logic/status.js`)** — the pure tier function gains the new placements.
Gateway must sit **above** core-operations, which today is tier 0 — so introduce **tier -1** for the
ingress band (Gateway + the `external` marker). Vault sits at tier 2 (with the KV band) but renders
**offset to the right** as a lateral node (§1.3). Chronicler is a tier-3 component like the other
materializers:

```js
function sysmapTier(node) {
  if (node.kind === "ingress") return -1;              // external actors marker
  if (node.id === "gateway") return -1;                // the door, above the spine
  if (node.kind === "lens") return 4;
  if (node.kind === "infra") {
    if (node.id === "object-store") return 4;           // the archive sink, bottom band with the read-models
    return node.id === "core-operations" ? 0 : 2;       // core-kv / core-events = spine
  }
  // component
  if (node.id === "processor") return 1;
  return 3;                                              // refractor, weaver, loom, bridge, objmgr, chronicler, vault*
}
```

**Vault is the one exception to pure-tier placement.** It is a tier-3 component by the fall-through above,
but the portrait wants it *lateral to Core-KV* (tier-2 band, offset), not down in the tier-3 row. Rather
than special-case the tier number (which would fight the spine layout), mark it in the **render layer**:
give Vault `kind:"component"` but tag it `lateral:true` on the node payload (a new bool on `mapNode`,
`json:"lateral,omitempty"`, set for the Vault node in `computeSystemMap`), and in `map.js` place a
`lateral` component beside its anchor rather than in the tier row (§1.3). This keeps `sysmapTier`
honest-and-pure (Vault *is* a tier-3-class engine by dependency depth) while the portrait reads right.
(Alternative considered — a dedicated tier-2.5 — rejected: it distorts the even tier spacing and the
`SYSMAP_TIER_Y` array; the `lateral` flag is a smaller, more honest change.)

### 1.3 Render-layer deltas (`cmd/loupe/web/js/views/map.js`)

The map renders tiers 0–3 as absolutely-positioned rows (`SYSMAP_TIER_Y`), tier-4 as the lens shelf,
clients as a shelf. F10 needs:

- **`SYSMAP_TIER_Y` gains a tier -1 band above tier 0.** Today `SYSMAP_TIER_Y = [40, 150, 270, 400, 530]`
  (tiers 0–4). Prepend a top band and shift the rest down, or (cleaner, avoids re-indexing everywhere)
  keep the array 0-indexed and map tier -1 to a computed `y` above tier 0 (e.g. `SYSMAP_TIER_Y[0] - 90`),
  clamped ≥ 8. The `tierMembers` collection loop currently allocates `[[],[],[],[]]` for tiers 0–3;
  extend it to include a tier -1 bucket (an `ingressMembers` array collected alongside). The ingress band
  holds the `external` marker and the Gateway node, laid out like any tier row.
- **The ingress node kind.** `buildSysmapNode` gains an `else if (n.kind === "ingress")` branch: a plain
  chip (`.sysmap-node.ingress`) with just the label, no dot, no hover drill, non-interactive — the
  `external` marker. The Gateway itself is an ordinary `component` node (dot, status, drill → its page).
- **Lateral placement for Vault.** After the tier rows are laid out, place any `lateral` component
  absolutely to the **right of Core-KV** (its anchor): read Core-KV's box, set Vault's `left` to
  `coreKvBox.right + gap`, `top` to the tier-2 `y`. Because edges are measured from boxes after layout
  (`drawSysmapEdges`), the `processor → vault` edge draws correctly wherever Vault lands — no edge-code
  change. If Core-KV isn't present (empty stack), fall back to the tier-3 row (harmless).
- **The `object-store` infra node** renders as an ordinary infra node on the tier-4 band, to the right,
  so `chronicler → object-store` drops cleanly. (Infra nodes are already non-interactive.)

No change to `drawSysmapEdges` is required for any of this — it is box-measured and kind-agnostic; the new
edges resolve automatically once both endpoints render. The one thing to verify in-browser: the
tier -1 band doesn't collide with the topbar/rollup (the stage has its own scroll; bump `SYSMAP_TIER_Y`
offsets if needed).

### 1.4 Honest absent / design-ahead rendering (the load-bearing F10 subtlety)

A declared component with no heartbeat renders `absent` today — **red**, and `absent` reds the overall
rollup (`computeSystemMap` calls `worse(red)` for an absent component). That is correct for a component
that *should* be running (Processor down = real problem). But **Vault and Chronicler are not deployed
yet** — showing them as red "absent" faults would cry wolf on every dev stack for weeks, exactly the
"7 degraded" anti-pattern F4 fixed for lenses.

**Design decision — a `designAhead` flag on the declared component, rendering a distinct
`design-ahead` status that does NOT red the rollup.** Mirror the `pending-readpath` precedent (an
expected, informational state that uses the `--accent` family and is excluded from degraded counts):

- `declaredComponent` gains `designAhead bool`. Vault and Chronicler are `designAhead:true` until their
  builds deploy; Gateway is **not** (it's built — once it's in `up-full` it should be truthfully absent =
  red if it fails to start).
- In `computeSystemMap`, a `designAhead` declared component with **no** heartbeat gets
  `Status:"design-ahead"`, `Detail:"not yet deployed"`, and **does not** call `worse(red)` — it
  contributes nothing to the rollup (like `pending-readpath`). The instant it **does** heartbeat, it
  takes its normal live status (green/stale/etc.) via the existing `applyBeats` path and the `designAhead`
  flag becomes moot — so flipping the flag off later is a no-op cleanup, not a behavior change. (The flag
  is really "suppress the absent-red until first heartbeat.")
- **Visual (`map.js` + `logic/status.js`):** `design-ahead` renders like `absent` structurally (dimmed,
  struck-through label) **but** in the `--accent-dim` informational family, not red — a dashed
  `--accent-dim` border + a `◇ design-ahead` tag (open diamond, echoing Vault's `◆` but hollow = "not
  live yet"). Add `componentStatusClass["design-ahead"] = "designahead"` and a matching
  `.sysmap-node.component.designahead` rule (dashed `--accent-dim` border, dimmed label, NOT
  line-through-red). `sysmapSummary` counts `design-ahead` into a new `designAhead` bucket, never
  `degraded`/`absent` (same shape as the `pending` bucket) — the banner says "… · 2 design-ahead
  (not yet deployed)".
- **Hover tip** for a design-ahead node: "design-ahead — surface built, backend not yet deployed" + a
  one-line pointer ("Vault: crypto-shred, behind the Lattice-lane Vault build"). This makes the map
  *teach* the roadmap instead of alarming about it.

This is the honest portrait the PO asked for: Gateway truthful (absent=red if it should run and doesn't),
Vault/Chronicler visibly *coming* (accent, not red) until they report, then live like everything else.

### 1.5 Cross-lane deploy ask (F10's only external dependency)

**Gateway into `make up-full`.** Today `run-gateway` exists (port 8080, dev mode) but the `orchestration`
target doesn't start it, so Gateway never heartbeats and its node would be a ghost. Filing to `lattice.md`
(`🚧 blocks F10 truthfulness`): add the Gateway binary to the `orchestration` tier of `up-full` (build
`cmd/gateway`, start it with `GATEWAY_DEV_MODE=true` + the dev key so it has a trusted key set and comes
up fail-open-for-dev), so `health.gateway.<instance>` appears and the node is truthful. Without it, F10
ships the node + `design-ahead` treatment (Gateway would need `designAhead:true` too until deployed) —
but the PO's intent is Gateway *live*, so the deploy ask is the real close. See §6.

---

## 2. F11 — Gateway security console (`#/component/gateway`)

Gateway gets the standard component-page template (§5 of the program doc: header · two-column body
`1fr 380px`, left = live state, right = the control/action surface) with Gateway-specific content. This
is the **edge security console** — the reason to open Loupe when you care about who's writing to the
platform.

### 2.1 Route + shell

- Route: `#/component/gateway` — already handled by the F3 component-page router; Gateway is just another
  declared component id. The map's Gateway node click drills here (existing `drillSysmapNode`).
- No new route grammar. The revoke and JWKS surfaces live *inside* this page.

### 2.2 Left column — live security state

**Header:** "Gateway" · status pill (worst-of instance) · instance count · "on map →" back-link.

**1. Instances (plural — the standard instance cards).** One card per `health.gateway.<instance>` with
the Gateway metrics line (a new `metricsLine(comp, doc)` case in `logic/component.js`):

```js
if (comp === "gateway") {
  return "requests " + num(m.requests_total) +
    " · ops submitted " + num(m.ops_submitted_total) +
    " · auth failures " + num(m.auth_failures_total);
}
```

**2. Auth-failure rate panel (the security headline).** The three counters
(`requests_total` / `ops_submitted_total` / `auth_failures_total`, per `docs/components/gateway.md`
§Health) rendered as a compact stat row, plus a **derived failure ratio** — `auth_failures_total /
requests_total` as a percentage, colored: `--green` under a floor, `--yellow` above (a spike in forged/
expired tokens is the thing an operator wants to see). The ratio derivation is pure and goja-tested:

```js
// logic/gateway.js
function authFailureRate(m) {
  var req = (m && typeof m.requests_total === "number") ? m.requests_total : 0;
  var fail = (m && typeof m.auth_failures_total === "number") ? m.auth_failures_total : 0;
  if (req <= 0) return { pct: null, cls: "muted" };          // no traffic — "—", not 0%
  var pct = fail / req;
  return { pct: pct, cls: pct >= 0.2 ? "warn" : "ok" };       // ≥20% failing = yellow (tunable)
}
```

(No-traffic renders "—" not "0%" — the `num()`/honesty convention from `logic/component.js`.) Because
counters are cumulative, the rate is lifetime; a per-poll delta ("N failures since last refresh") is a
nice-to-have riding the component page's existing refresh, but the lifetime ratio is the load-bearing
signal and ships first.

**3. The live JWKS key set (the `JWKSPoller` story, made visible).** A panel listing every trusted `kid`
with its provenance — this is the F11 differentiator that turns "auth works" into "here is exactly which
keys are trusted right now, and where they came from." One row per trusted key:

| Column | Source | Meaning |
|---|---|---|
| `kid` | key id | mono; the JWT header `kid` this key verifies |
| source | `dir` / `url` / `dev` | `GATEWAY_JWT_KEYS_DIR` (static snapshot) · `GATEWAY_JWKS_URL` (live IdP) · dev key (`GATEWAY_DEV_MODE`) |
| alg | key alg | RS256 / ES256 (from the parsed key) |
| added | first-seen | when this kid entered the trusted set (hot-swap history — a JWKS poll that *added* a key stamps this) |

Below the table: a **last-poll line** — for a `url`-sourced set, "JWKS polled `<freshness>` ago from
`<host>` (interval 5m)" with a `--green`/`--yellow` dot for poll health (a failed poll keeps last-known-
good and yellows, per the gateway doc's fail-safe posture); for a `dir`/`dev`-only set, "static key set
(restart to rotate)". A **hot-swap history** mini-log (last ~10 key-set changes: "12:04 +kid abc123
(url)", "11:30 −kid old99 (url retired)") makes the "rotated with no restart" story concrete — the
`JWKSPoller` feature you can *watch*.

⚠️ **Needs a new endpoint — Gateway does not expose its key set today.** The gateway heartbeat carries
only the three counters; the trusted `kid` set + poll state live in the Gateway process
(`auth.Verifier` / `auth.JWKSPoller`) and are not published anywhere Loupe can read. So F11 needs **one
of**:
- **(preferred) a Gateway health-metrics extension** — the heartbeater adds a `jwks` block to its
  `health.gateway.<instance>` doc: `{keys:[{kid, source, alg, addedAt}], lastPoll:{at, source, ok},
  swaps:[…]}`. Loupe reads it from Health KV like every other heartbeat field — **no new Loupe endpoint,
  no new Gateway HTTP surface, P5-clean.** This is a Lattice-lane ask (extend `internal/gateway.Heartbeater`).
- **(fallback) a Gateway read endpoint** `GET /v1/jwks-state` on the Gateway's own HTTP server, proxied
  by a new Loupe `GET /api/gateway/jwks` handler. Heavier (Loupe would need the Gateway's URL + a client),
  and it puts Loupe on Gateway's HTTP surface rather than the health plane — less clean.

**Recommendation: the health-metrics extension.** It keeps Loupe a pure Health-KV/Core-KV/NATS reader
(no new outbound HTTP target), the data is exactly heartbeat-shaped, and it rides the refresh Loupe
already does. Filed to `lattice.md` as the F11 dependency (§6). The panel renders a designed empty state
("JWKS state not reported by this Gateway build") until the heartbeat carries it — so the FE ships against
the shape and lights up when the field arrives, zero rework.

### 2.3 Right column — the revoke surface (the signature)

The arch review (2026-07-02) found the token-revocation kill-switch exists only in code: no bucket, no
surface, silent downgrade (`docs/components/gateway.md` documents "a revoked actor → 403", but nothing
*populates* the revocation set). **F11 makes Loupe the revoke surface** — revoke an actor from the
console, watch the next forged request 403.

**Layout (right column, `1fr 380px` body):**

- **Revoke an actor** — a small form: an actor key input (`vtx.identity.<id>`, with the §7.3 linkifier so
  a pasted/typed key validates as an entity key) + a **Revoke** button, styled destructive (the
  `.file-detach` red-outline family) behind a **typed confirm** ("This revokes `<actor>` — every future
  request bearing its token is refused with 403. Type the identity id to confirm."). The confirm reuses
  the program's `.confirm-modal` idiom (F5's typed-delete pattern).
- **Currently revoked** — a list of revoked actor keys (each a `keyLink` → its `#/graph/<key>`), with an
  **Un-revoke** action per row (revocation should be reversible — an operator who fat-fingers an actor id
  needs to undo it). Freshness/who-revoked if the bucket carries it.
- **Copy that teaches the demo:** a one-line note under the form — "Revoked actors are refused at the
  Gateway (401/403) before any op is published. Mint a test token (`bin/gateway dev-token -sub <id>`),
  revoke it here, and the next `POST /v1/operations` returns 403." (This is the live proof loop the PO
  wants — the operator can literally watch it work against `run-gateway`.)

**⚠️ The revoke bucket is a cross-lane Lattice ask (the PO flagged it; here is the scoped design).** The
Gateway's `auth.Verifier` checks structural validity (signature/kid/exp/issuer/audience → 401) and then a
**revocation check** (a revoked-but-valid actor → 403). For that check to have anything to read, there
must be a **revocation set** the Gateway consults and Loupe writes. Design within the platform's
invariants:

- **The revocation set is a NATS-KV bucket** (`gateway-revocations` or similar), keyed by actor id
  (`vtx.identity.<id>` → `{revoked:true, at, by}`). The Gateway's Verifier reads it (cache + watch, like
  the JWKS poll) on each request's revocation check. This is **operational security state, not Core KV**
  — the same class as Health KV (self-reported operational state), *not* a lens read-model and *not* a
  Core-KV graph write. So writing it is **not** a P2 violation (P2 governs Core KV; this is an
  out-of-band operational bucket, the sanctioned-direct-write class the CLAUDE.md P2 note carves out for
  Health KV). ⚠️ **ASSUMES** the Lattice lane agrees the revocation set is an operational KV bucket
  (bootstrap-provisioned, like `weaver-targets` / `loom-state`), not modeled as identity aspects. If
  instead revocation is modeled as an **identity aspect** (`vtx.identity.<id>.revoked`, a Core-KV write),
  then Loupe must **submit a `RevokeActor` op** (P2 — the Processor writes it) and the Gateway reads it
  off a projected lens — a heavier, more "Lattice-native" shape. **This is the fork for Andrew** (§8):
  operational-bucket (lighter, Health-KV-class) vs Core-KV-aspect-via-op (heavier, fully ledgered/
  auditable — and, note, it would then appear in the Chronicler ledger, which is a *nice* property).
- **Loupe's write path — a new endpoint either way:**
  - operational-bucket model → **`POST /api/gateway/revoke`** + **`POST /api/gateway/unrevoke`** (body:
    `{actor}`): Loupe writes/clears the bucket key directly (the one sanctioned operational-KV write, like
    it already does nothing else — this would be Loupe's *first* write outside op-submit; call it out
    explicitly for adjudication). Reads: **`GET /api/gateway/revocations`** lists the bucket.
  - op model → Loupe **submits the `RevokeActor` op** via the existing `/api/op` path (no new privileged
    endpoint — reuses the op-submit surface), and reads the revocation state from the projecting lens's
    read-model bucket via a `GET /api/gateway/revocations` list. **This is P2-clean and needs no new
    write endpoint** — a point in its favor.

  **Recommendation: the op model if a `RevokeActor` op is cheap to add on the Lattice lane** — it keeps
  Loupe write-free (P2 intact), makes revocation auditable in the ledger (and thus a Time-Machine
  artifact), and reuses `/api/op`. If the Lattice lane prefers the operational bucket (lower latency, no
  Processor round-trip on the kill-switch — a defensible choice for an *emergency* revocation), then the
  two new write endpoints are the cost, flagged for Winston's adjudication. Either way the UI is
  identical — the form + confirm + list — so the FE ships against a small internal `revoke(actor)` /
  `unrevoke(actor)` / `listRevocations()` seam and binds to whichever backend lands.

### 2.4 F11 endpoints summary

| Endpoint | New? | Serves | Depends on |
|---|---|---|---|
| `GET /api/component/gateway` | reuses F3 `GET /api/component/<id>` | instances + gateway metrics line | Gateway heartbeating (deploy) |
| JWKS key set | **prefer no new Loupe endpoint** — read `jwks` block from the gateway heartbeat | §2.2 key table | Lattice: heartbeat `jwks` block |
| `GET /api/gateway/revocations` | **new** | the revoked-actor list | the revocation set (bucket or lens) |
| `POST /api/gateway/revoke` · `/unrevoke` | **new (only in the operational-bucket model)** | write the kill-switch | Lattice: revocation bucket + Gateway read |
| (op model) reuse `POST /api/op` for `RevokeActor` | no new endpoint | submit the revocation | Lattice: `RevokeActor` op DDL |

Pure logic added: `logic/gateway.js` (`authFailureRate`, and a `jwksRows(healthDoc)` shaper that flattens
the heartbeat `jwks` block into sorted table rows — goja-tested).

---

## 3. F12 — Vault surface (design-ahead)

**Blocked on:** the Vault build (Fires 1–4 shipped on the Lattice lane; the `lattice.vault.decrypt`
responder + the `privacy-shreds` observability lens are live — see the vault design's Fire-3/4b
checkpoints). F12 is design-ahead: fully specified, every not-yet-live assumption flagged, so it builds
the moment the Vault node heartbeats and the decrypt RPC is reachable from Loupe.

Three surfaces: the **node + page** (§3.1), **Reveal** (audited decrypt in the Graph explorer, §3.2), and
the **crypto-shred proof** (§3.3).

### 3.1 The Vault component page (`#/component/vault`)

Standard component template; Vault-specific content.

**Left column — custody state.** Instances (per `health.vault.<instance>`, ⚠️ **ASSUMES** a dedicated
Vault heartbeat group — §6) with a Vault metrics line:

```js
if (comp === "vault") {
  return "DEKs " + num(m.dek_count) +
    " · vault calls " + num(m.vault_calls_total) +
    " · shreds " + num(m.keyshredded_handled_total) +
    " · backend " + (doc.backend || "?");   // "local-envelope" | "kms:transit" | "kms:aws" …
}
```

- **DEK count** — how many per-identity keys are held (the size of the custody set).
- **Shred events** — `keyshredded_handled_total` (Contract #5 §5.4, wired in the vault build's Fire 4a).
- **Backend** — `local-envelope` (dev, master KEK sealed in config) vs a KMS adapter — a plain chip.
- **The privacy-critical failure tier**, surfaced honestly: if any lens is halted in the
  `CatPrivacyCritical` tier (a shredded-but-still-decrypting row — the vault design's reserved tier), the
  Vault page shows a **red banner** "privacy-critical: N lens(es) halted — a shred did not fully
  propagate" linking to the affected lens page(s). This is the one Vault state that *should* red the
  rollup (a confidentiality failure), distinct from the informational design-ahead state.

**Right column — read-only info panel** ("no operator control plane — Vault custody is not operator-
mutable; the shred surface is per-identity, in the Graph explorer"). Vault has no list/pause/enable
control ops, so the right column is informational (the program's `controlSurface() === "none"` shape),
plus a **shred-status summary**: a compact count from the `privacy-shreds` read-model bucket ("N
identities shredded · M shreds in flight (finalization pending)") — the fleet view of the per-identity
proof in §3.3. Each in-flight shred links to the identity's graph page.

⚠️ **ASSUMES** the `privacy-shreds` bucket (vault Fire 4b — `{identityKey, shredded, vaultKeyDestroyed,
projectionsNullified}` per shredded identity) is readable by Loupe over P5. It is a lens target bucket
(`privacy-shreds`), so this is the ordinary P5 read (`KVListKeys` + `KVGet`) — **new endpoint
`GET /api/vault/shreds`** returning the bucket rows (copy the `corekv.go` list/get shape + the
`loftspace-app` read-model precedent, exactly as the Chronicler flow-history read does in §4.1).

### 3.2 Reveal — audited decrypt in the Graph explorer (Signature #1)

Loupe is a *named* trusted plaintext consumer via `lattice.vault.decrypt` (the vault design §2.3). A
`sensitive:true` aspect projects as **ciphertext** everywhere by construction; in the Graph explorer,
Loupe can decrypt it for the trusted operator — encryption-at-rest, made visible and honest.

**Where it lands.** In the graph detail view (`#/graph/<key>`), the **aspects** section (F2's lazy
expander rows) already renders each aspect's document via the linkifying `renderDoc`. A sensitive aspect's
data is the ciphertext envelope `{ct, nonce, keyId}` (per Contract #3 §3.10). F12 detects that shape and
renders the aspect row specially:

- **Sealed rendering (default).** The aspect row shows a `🔒 sensitive` tag (mono, `--accent-dim`) and,
  instead of dumping `{ct, nonce, keyId}` as noise, a compact "encrypted at rest ·
  `<keyId short>`" line + a **Reveal** button. The raw envelope stays available in a collapsed
  `<details>` for the curious (it's not secret — it's unreadable ciphertext).
- **Reveal (audited).** Clicking **Reveal** calls a new **`POST /api/vault/decrypt`** (body: the aspect
  key `vtx.identity.<id>.<local>` + its envelope, or just the aspect key and Loupe re-reads the envelope
  server-side). The Loupe server calls the `lattice.vault.decrypt` micro.Service responder, gets
  plaintext, returns it. The row swaps the sealed line for the plaintext (rendered via `renderDoc` so
  nested keys still linkify), with a **⚠ revealed** marker and an **auto-reseal** affordance (a "hide"
  button + optional auto-reseal on navigate-away, so PII isn't left on screen). **Audited** = the decrypt
  RPC call is logged server-side (the vault design's "audited decrypt-RPC"); the UI states "this reveal
  is audited" beside the button so the operator knows it's not a free peek.
- **Reveal is opt-in, per-aspect, never bulk.** No "reveal all" — each sensitive aspect is revealed
  deliberately. A shredded identity's aspect **cannot** reveal: the decrypt RPC returns
  `ErrKeyShredded` (the vault design's guarantee), so Reveal renders "permanently unreadable — key
  shredded (`ShredIdentityKey`)" — which is the whole point of §3.3, visible right here.

**Endpoint — `POST /api/vault/decrypt` (new, privileged — flagged).** This is a genuinely new privileged
surface (it produces plaintext PII), so per the constraints it's called out explicitly:
- It is **not** a control-plane op (not in the FIXED `control.go` allow-list) — it's a Loupe→Vault RPC
  proxy, the same trusted-single-identity model Loupe already runs under (Loupe is the P5 inspector
  exception; the vault design *names* Loupe as a trusted plaintext consumer).
- Reads only: it decrypts, it never writes. P2 intact.
- ⚠️ **ASSUMES** the `lattice.vault.decrypt` responder is reachable from Loupe's NATS user (it is a
  `micro.Service` on the substrate — Loupe already has NATS access). If the responder requires a
  capability/actor Loupe doesn't hold, that's a Lattice-lane grant ask (§6).
- Bounds: single-aspect per call (no batch), request-scoped timeout, `requireConn` guard — the house
  handler pattern.

**Logic split.** `logic/sensitive.js`: `isSealedAspect(data)` (detects the `{ct, nonce, keyId}` envelope
shape — pure, goja-tested against Contract #3 §3.10's shape, mirroring how `logic/keys.js` mirrors the Go
classifier) + `sealedSummary(data)` (the "encrypted · keyId" line). The DOM/RPC lives in the graph view.

### 3.3 The crypto-shred proof (Signature #2)

The flagship right-to-be-forgotten demo: run `ShredIdentityKey` from Loupe; watch the same aspect go
**readable → permanent gibberish** across live KV *and* JetStream history *and* every projection, on one
screen.

**Where it lands.** On the identity's graph detail page (`#/graph/vtx.identity.<id>`), a **Shred** action
in the actions row (styled destructive, apart from the others), **and** a dedicated **proof view** the
Shred action opens (a focused screen, not just a fire-and-forget button) — because the *proof* is the
product, not the button.

**The proof view (`#/graph/vtx.identity.<id>?view=shred` — a new view param on the existing route, like
`?view=hood`).** A single screen with a **before / after** of one identity's sensitive aspects, plus the
cross-plane confirmation:

```
┌ Identity vtx.identity.V1Sta… — crypto-shred proof ─────────────────────────┐
│ Sensitive aspects (7):  name · email · phone · ssn · dob · claimKey · …     │
│   ssn   🔓 REVEALED   "123-45-6789"      ← via Reveal, before shred          │
│                                                                             │
│  [ Shred this identity's key ]  (typed confirm: "type the identity id")     │
│                                                                             │
│ ── after shred ──────────────────────────────────────────────────────────  │
│   ssn   🔒 Vault.Decrypt → ErrKeyShredded   "permanent gibberish"           │
│                                                                             │
│ Cross-plane confirmation (this is the append-only-ledger guarantee):        │
│   ✓ live Core KV        — ciphertext unreadable (DEK destroyed)             │
│   ✓ JetStream history   — every prior value is the same dead ciphertext     │
│   ✓ projections         — Secure-Lens rows scrubbed to null (Fire 5a)       │
│   shred finalization:  vaultKeyDestroyed ✓ · projectionsNullified ✓         │
└─────────────────────────────────────────────────────────────────────────────┘
```

- **Before:** the operator can Reveal a sensitive aspect (§3.2) to see real plaintext — establishing
  "this was readable."
- **Shred:** the **Shred** button submits `ShredIdentityKey` behind a typed confirm ("This permanently
  destroys the encryption key for `<identity>`. Its PII becomes unrecoverable everywhere — live, history,
  and every projection. This cannot be undone. Type the identity id to confirm."). ⚠️ **Needs a submit
  path:** `ShredIdentityKey` is an **op** (`packages/privacy-base`, lane `ops.system`/`ops.urgent`), so
  Loupe submits it via the existing **`/api/op`** surface — **no new privileged endpoint** (P2-clean;
  reuses op-submit). ⚠️ **ASSUMES** Loupe's actor is granted `ShredIdentityKey` — the vault design notes
  `ShredIdentityKey` has a **deliberately no-default-grant** posture, so Loupe submitting it needs an
  explicit grant. **This is a real Lattice-lane ask** (§6): either grant the shred op to Loupe's operator
  actor, or accept that the proof runs only under `LATTICE_AUTH_MODE=stub` (the dev stack) and document
  that. Given it's the flagship demo and Loupe is the trusted operator console, a scoped grant is the
  right call — flagged for Andrew.
- **After:** the same aspects, now showing `Vault.Decrypt → ErrKeyShredded` when Reveal is attempted —
  "permanent gibberish." The proof view re-polls the `privacy-shreds` bucket (`GET /api/vault/shreds`,
  §3.1) to show the **finalization** progressing (`vaultKeyDestroyed`, `projectionsNullified` flipping
  true — the vault Fire 4b observability lens, watched live). This is the "on one screen" the PO wants:
  the shred *and* its cross-plane propagation, confirmed.
- **The cross-plane checklist** is the teaching device — it names *why* this works on an immutable ledger
  (destroying the key kills the ciphertext in history too), which is the exact insight the Vault×ledger
  pairing (§4.3) makes flagship.

**Honest bounds (⚠️ ASSUMES / states the vault design's known residuals):**
- Object-store *blobs* are **not** shredded by `ShredIdentityKey` (vault design §2.6 — aspects only). The
  proof view states this: "This shreds sensitive **aspects**. PII in uploaded **documents** (object
  store) is a separate follow-on (`crypto-shred for object-store blobs`) — not erased by this action."
  Honesty over a false-complete claim.
- Non-Secure-Lens **plain** projections may briefly re-upsert a row post-shred (the Fire-4a re-upsert
  limitation, resolved for Secure Lenses but standing for plain lenses). The checklist's "projections"
  line is accurate for **Secure-Lens** targets (scrubbed to null, Fire 5a) and notes plain-lens rows hold
  only dead ciphertext (not a new leak). Don't overclaim.

### 3.4 F12 endpoints summary

| Endpoint | New? | Serves | Notes |
|---|---|---|---|
| `GET /api/component/vault` | reuses F3 | instances + vault metrics | ⚠️ needs `health.vault.*` heartbeat (Lattice) |
| `GET /api/vault/shreds` | **new** | `privacy-shreds` bucket rows (shred fleet + finalization) | P5 read (copy `corekv.go` + loftspace-app read-model shape) |
| `POST /api/vault/decrypt` | **new, privileged (flagged)** | Reveal — proxy to `lattice.vault.decrypt` RPC | reads only; single-aspect; audited server-side |
| reuse `POST /api/op` for `ShredIdentityKey` | no new endpoint | the Shred action | ⚠️ needs Loupe granted the shred op (Lattice) |

Pure logic: `logic/sensitive.js` (`isSealedAspect`, `sealedSummary`) + `logic/shred.js`
(`shredStatusLine(shredRow)` — the finalization-progress shaper: "in flight" / "vaultKeyDestroyed ✓" /
"complete" — goja-tested).

---

## 4. F13 — The Chronicler Time Machine (design-ahead) — Loupe's fourth leg

**Blocked on:** the Chronicler build (F1 the component + PROJECT mode + `loomFlowHistory`; F2 Weaver
history; F3 ARCHIVE mode + the intent-ledger archive — Lattice lane, `chronicler-prebuild-regrounding`
first). F13 is design-ahead: fully specified, every not-yet-live assumption flagged.

The Chronicler adds **history** — the fourth leg beside graph / lenses / events-live. Four layers, each
riding a Chronicler fire; **L1–L3 are designed here**, **L4 is the horizon** (noted, not designed).

**IA — a new top-level nav section.** History earns a nav link (the program's nav is Map · Graph ·
Packages · Tasks · Files · Submit Op — seven with History): **`#/history`**, defaulting to the flow-
history browser (L1). The map scrubber (L2) lives *on the map* (`#/map`), not under History — it's a
control on the existing map, not a separate page. The ledger browser (L3) is `#/history/ledger`.

| Route | View | Layer |
|---|---|---|
| `#/history` | Flow-history browser (faceted list of every flow) | L1 |
| `#/history/flow/<instanceId>` | Per-flow lifecycle timeline | L1 |
| `#/map` + scrubber | Map scrubber (timeline under the map; F6 pulse = its now-edge) | L2 |
| `#/history/ledger` | Ledger browser (archive-mode segments) | L3 |
| `#/history/ledger/<segmentId>` | One archived segment (ops verbatim, hash-chained) | L3 |

### 4.1 Layer 1 — Flow history ("what happened")

Rides Chronicler F1/F2 (`loomFlowHistory` + Weaver history read-models). Today a Loom flow vanishes at
terminal (`loom-state` deletes its cursor), so "why did the 3am onboarding fail?" is unanswerable. The
Chronicler projects one durable row per flow instance into the `orchestration-history` read-model bucket.

**The browser (`#/history`).** The program's faceted-list idiom (like the Graph explorer §7.1 — the same
`.split` two-pane, facet rail, paged list), specialized for flows:

- **Facet rail:** by **type** (`patternRef` — onboarding / lease-signing / …), by **outcome**
  (`running` / `complete` / `failed`), by **time** (today / 7d / all — bounded by the 7d source-retention
  reality the Chronicler design §2.5 documents). All URL-carried (`#/history?type=onboarding&outcome=failed`).
- **Rows:** one per flow instance — `instanceId` (mono, `keyLink`-adjacent but note: a flow has **no
  Core-KV vertex** per P1, so `instanceId` is *not* a graph key — it links to the flow timeline
  `#/history/flow/<instanceId>`, not `#/graph/…`) · `patternRef` · outcome chip (status vocabulary:
  `running` → `--accent` if live / `--yellow` if orphaned, `complete` → `--green`, `failed` → `--red`) ·
  started / ended · `failure_reason` (for failed). Paged (`offset`/`limit`), like the graph list.
- **Live-vs-orphaned badge** (the Chronicler design §2.5.2): a `running` history row is cross-referenced
  against the live control plane (`lattice.ctrl.loom.list`, already proxied) — badge **live** (a matching
  live cursor) vs **orphaned** (`running` with no live cursor and no terminal event = the "stuck flow"
  signal). This is a genuinely useful operator signal the read model alone can't give.

**The flow timeline (`#/history/flow/<instanceId>`).** The row clicks into the flow's **full lifecycle
timeline** — every step, event, and op it submitted, in order:

- A vertical timeline: `patternStarted` → each step's events → `patternCompleted`/`patternFailed`, each
  entry timestamped, with the **op it submitted** as a `keyLink` (`vtx.op.<requestId>` → the op tracker in
  the Graph explorer — walking from a historical flow step straight to the operation it ran, the
  program's "no dead ends" rule extended into history). ⚠️ **ASSUMES** the `loomFlowHistory` row carries
  enough to reconstruct the step sequence. Per the Chronicler design §2.6, the v1 row is *flat* (one row
  per instance: `instance_id, pattern_ref, subject_key, status, started_at, ended_at, failure_reason,
  last_event_seq`) — it does **not** carry a per-step event list. So the v1 timeline is a **lifecycle
  summary** (started → ended, outcome, reason, the submitting ops if projected), not a full per-step
  replay. A richer per-step timeline needs either the Chronicler to project step events (a follow-on lens)
  or the timeline to *also* read `core-events` history (the #68 op-trace direction). **Design decision:**
  ship the **lifecycle summary** timeline for v1 (honest to the flat row), with the per-step expansion
  designed as a **slot** ("per-step detail — pending a step-event history lens") — the same
  designed-slot-lights-up-later pattern the program uses for lens freshness (§6.2). Flagged so the FE
  doesn't over-build against a row that isn't there yet.

**Endpoint — `GET /api/history/flows` (new).** P5 read of the `orchestration-history` bucket
(`KVListKeys` + `KVGet`, copy `corekv.go` + the loftspace-app read-model precedent — **exactly** what the
Chronicler design §2.7 prescribes), with in-handler filter/sort (by `status`, `pattern_ref`, time) and
paging. **`GET /api/history/flows/<instanceId>`** returns one flow's row (+ the live-vs-orphaned cross-ref
by calling `lattice.ctrl.loom.list` — the existing control-read proxy). ⚠️ **ASSUMES** the
`orchestration-history` bucket exists and is P5-readable (it's a lens target — the ordinary read).

### 4.2 Layer 2 — The map scrubber ("replay it")

The visual centerpiece: **F6's live pulse is only the *now* edge of a timeline.** A **scrubber** (a
timeline slider) under the System Map; drag it back and the map re-renders the platform's state at that
instant (flows in flight, edges pulsing, the rollup), replayed as animation from Chronicler history
instead of the live SSE tail. Watch Lattice think, then rewind.

**Where it lands.** On `#/map`, in the map rail region (the program's `#sysmap-rail` — but **NOT** the
reserved `#sysmap-console` slot, which stays empty). A new **`#sysmap-scrubber`** element, placed as a
horizontal strip **under the map stage** (full stage width, above or below the rail — a strip, not in the
rail column, since it spans the timeline). It has two modes:

- **LIVE (default) — the now-edge.** The scrubber handle sits at the right end ("now"); the map behaves
  exactly as F6 today (live SSE pulse, 10s refresh). No behavior change to the shipped map when the
  scrubber is at "now" — F13 is purely additive; a stack without the Chronicler shows the scrubber
  disabled ("history unavailable — Chronicler not deployed", the design-ahead honesty).
- **REPLAY — drag back.** Dragging the handle left enters replay: the map **stops** the live SSE
  animation and instead renders **historical frames** from the Chronicler. A frame at time *t* is: which
  flows were `running` at *t* (from `orchestration-history` — a flow contributes to the frame between its
  `started_at` and `ended_at`), and — richer, if archive/event history is available — which edges pulsed
  around *t* (from the archived event/op stream). The map edges pulse per the frame (reusing the
  `.sysmap-edge.pulse` CSS from F6 — **the same animation primitive**, driven by frame data instead of
  live SSE), and a **playhead clock** shows the replayed timestamp. Play/pause/step controls scrub through
  frames as animation.

**What a "frame" honestly is (⚠️ the load-bearing design-ahead call).** The fidelity of replay is bounded
by what the Chronicler durably records:

- **From `orchestration-history` (F1/F2, the near-term data):** flow **liveness** over time — which flows
  were in flight at *t*. This alone gives a meaningful replay: the map's engine nodes light up when they
  had flows running, the rollup reflects the historical health, and the flow count animates. This is the
  **v1 scrubber** — buildable the moment L1's bucket exists.
- **From archive mode (F3, `core-operations` intent-ledger, §4.3's data):** the **per-op stream** — every
  operation submitted, in order, with timestamps. This upgrades replay to **edge-level pulse fidelity**
  (the actual `core-operations → processor → core-events → consumers` fan, replayed op-by-op, exactly like
  the live pulse but historical). This is the **full scrubber** — the "watch Lattice think" experience.
- **Design decision:** specify **both tiers**, ship the **v1 (flow-liveness) scrubber** first (rides L1's
  data), and treat edge-level pulse as the **F3-gated upgrade** — a data-source swap behind the same
  scrubber UI, not a redesign. ⚠️ **ASSUMES** the Chronicler exposes historical frames in a
  Loupe-readable way. **This is the biggest not-yet-built assumption in F13**: the Chronicler design's
  read models are *convergent current-state* rows (`orchestration-history` = one row per flow *now*), not
  a *time-series frame API*. Reconstructing "state at time *t*" from flow rows is feasible (each row
  carries `started_at`/`ended_at` — a flow is live in `[started, ended)`), but **edge-level historical
  pulse needs the archived event/op stream read back in time order** — an archive-read surface the
  Chronicler doesn't expose yet. **Flagged as the F13-L2-full dependency** (§6): a Loupe-readable
  historical-event feed (either a bounded archive-segment read, §4.3, replayed client-side, or a
  Chronicler "events between t0 and t1" read endpoint). The **v1 flow-liveness scrubber needs none of
  this** — it reconstructs frames from the flow rows Loupe already reads in L1.

**Endpoint — `GET /api/history/timeline?from=&to=` (new, v1).** Returns the flow-liveness data for a time
window: the set of flows with `[started_at, ended_at)` overlapping the window (derived in-handler from the
`orchestration-history` rows — no new backend needed beyond L1's bucket). The FE reconstructs frames from
this (a flow is live in a frame if the frame's *t* ∈ `[started, ended)`). The frame-reconstruction math is
**pure and goja-tested** — `logic/scrubber.js`: `framesFromFlows(flows, from, to, step) → [{t, liveFlows,
rollup}]` (positions in, frames out — the same "pure model, DOM paints" discipline as `logic/hood.js`).
Edge-level pulse (F3) binds a richer feed to the same frame model.

### 4.3 Layer 3 — The ledger browser ("prove it") + the Vault×ledger flagship

Rides Chronicler F3 (ARCHIVE mode: the `core-operations` intent-ledger archived verbatim to the
object-store plane — sequenced segments, a manifest with first/last-seq + a **prev-segment hash chain**,
ack-only-after-durable-write, per the Chronicler design's ratification rework). This is the **immutable,
ordered record of every operation ever submitted** — the tamper-evident audit surface.

**The ledger browser (`#/history/ledger`).** A list of archived **segments** (the unit the Chronicler
writes): each row = `segmentId` · seq range (`first..last`) · archived-at · a **chain-link indicator**
(the prev-segment hash — a `⛓ verified` chip when this segment's `prevHash` matches the prior segment's
hash, i.e. the chain is intact; `⚠ broken` if not — tamper-evidence *shown*, not just claimed). Rows in
seq order (the ledger is ordered). Paged.

**A segment (`#/history/ledger/<segmentId>`).** The archived operations in that segment, in order —
each op rendered via the linkifying `renderDoc` (so `vtx.op.<id>` and the keys it touched linkify back
into the Graph explorer — walking from the immutable ledger into the live graph). The segment header shows
its `prevHash` / `hash` (the chain math, verifiable by eye), first/last seq, and archived-at.

**The flagship — Vault-shred × ledger pairing.** The single most important demo of the whole program.
When browsing a segment (or a specific op) that references a **shredded** identity, the ledger browser
**pairs the two guarantees on one screen**:

```
┌ Ledger segment 0042 (seq 8100..8250) — ⛓ chain verified ─────────────────────┐
│  op vtx.op.9fQ… CreateIdentity  actor …  →  vtx.identity.V1Sta…               │
│     payload: { name: 🔒 <gibberish — key shredded>, ssn: 🔒 <gibberish> }     │
│                                                                              │
│  ▸ This identity was shredded (ShredIdentityKey, seq 8990).                   │
│    • The ledger entry is INTACT — the operation, actor, timestamp, and        │
│      chain hash are all here, unaltered (audit: satisfied).                   │
│    • Its PII is PERMANENT GIBBERISH — the ciphertext in this immutable         │
│      segment is unrecoverable (erasure: satisfied).                           │
│    → Both true, on an append-only ledger.  [view shred proof →]              │
└──────────────────────────────────────────────────────────────────────────────┘
```

- The op's envelope, actor, timestamp, and the chain hashes render **intact and readable** (audit is
  satisfied — the record is whole and tamper-evident).
- The sensitive fields in the archived payload render as **permanent gibberish** (the ciphertext is dead —
  the DEK was destroyed; erasure is satisfied). Loupe *cannot* Reveal them here (the decrypt RPC returns
  `ErrKeyShredded`) — and that inability *is the proof*.
- A **"both true" callout** names the property: durable audit **and** right-to-be-forgotten,
  simultaneously true, on an append-only ledger — with a `[view shred proof →]` link to the identity's
  §3.3 shred-proof view (the two signature surfaces, cross-linked).

This is the pairing the PO calls "the flagship demo" — and it composes the two design-ahead fires (F12
Vault + F13 Chronicler) into one screen that only exists because both landed.

**Endpoints (new, L3):**

| Endpoint | Serves | Notes |
|---|---|---|
| `GET /api/history/ledger` | segment list (id, seq range, archived-at, chain status) | ⚠️ reads archive-mode segment manifests from the object-store plane |
| `GET /api/history/ledger/<segmentId>` | one segment's ops (verbatim, ordered) + chain hashes | ⚠️ reads the archived segment blob |

⚠️ **ASSUMES — the biggest L3 dependency:** archive mode (Chronicler F3) exists and its **segments +
manifests are readable by Loupe**. The Chronicler archives to the **object-store plane**
(`objects-base`) — Loupe already reads objects (`cmd/loupe/objects.go`), so reading archived segments is
*plausibly* within Loupe's sanctioned surface (object-store reads). But **the exact archive layout — how
segments are keyed, the manifest schema (first/last seq, prevHash), how a segment blob is fetched — is
defined by the Chronicler F3 build, which hasn't happened.** So L3's endpoints are specified against the
*designed* shape (sequenced segments + hash-chained manifest, per the Chronicler ratification rework) and
**flagged to bind to the actual archive layout when F3 lands** (§6). The **chain-verification math** is
pure and goja-testable now (`logic/ledger.js`: `verifyChain(segments) → [{segmentId, ok}]` — each
segment's `prevHash` must equal the prior's `hash`), independent of the fetch shape.

### 4.4 Layer 4 — Point-in-time reconstruction (the horizon — NOT designed)

FR51 (`historical-state-query-design.md`) re-homes its archive layers to the Chronicler on revive: "show
me the whole graph as of last Tuesday 14:00" — reconstruct any vertex's past state by replaying the
ledger. **This is the horizon, not near-term** — noted here as where the Time Machine ultimately goes
(a `#/graph/<key>?asOf=<timestamp>` mode reconstructing a vertex's historical document by replaying its
ops from the ledger), but **not designed in F13** per the PO. When FR51 revives on the Chronicler, it
gets its own fire.

### 4.5 F13 endpoints + logic summary

| Endpoint | Layer | New? | Notes |
|---|---|---|---|
| `GET /api/history/flows` · `/flows/<instanceId>` | L1 | **new** | P5 read of `orchestration-history` (copy corekv.go); live-vs-orphaned via `ctrl.loom.list` |
| `GET /api/history/timeline?from=&to=` | L2 (v1) | **new** | flow-liveness frames from the same bucket — no extra backend |
| `GET /api/history/ledger` · `/ledger/<segmentId>` | L3 | **new** | ⚠️ archive-segment reads (object-store) — bind to Chronicler F3 layout |

Pure logic (all goja-tested): `logic/flows.js` (row shaping, outcome→status, orphaned-vs-live label),
`logic/scrubber.js` (`framesFromFlows`), `logic/ledger.js` (`verifyChain`, segment row shaping).

---

## 5. Visual system & CSS (additive, existing tokens only)

No new `:root` tokens (binding). New class namespaces, all additive, matching the program's naming:

- **F10 map:** `.sysmap-node.ingress` (the external-actors marker — plain, no dot, non-interactive),
  `.sysmap-node.component.designahead` (dashed `--accent-dim` border, dimmed label — *not*
  line-through-red), the `◇ design-ahead` and `◆` Vault tags reuse `.sysmap-tag`. `lateral` placement is
  a positioning tweak in `map.js`, no new class needed (or `.sysmap-node.lateral` if a visual offset cue
  helps).
- **F11 gateway:** `.gw-*` (`.gw-jwks` key table, `.gw-authrate` the ratio stat, `.gw-revoke` the revoke
  form). The revoke destructive button reuses `.file-detach`; the confirm reuses `.confirm-modal`.
- **F12 vault:** `.vault-*` (page panels), `.seal-*` (the sealed-aspect row: `.seal-tag` the `🔒
  sensitive` chip in `--accent-dim`, `.seal-reveal` the Reveal button, `.seal-revealed` the `⚠ revealed`
  plaintext state), `.shred-proof-*` (the §3.3 proof view). Shred/reveal destructive actions reuse
  `.file-detach` + `.confirm-modal`.
- **F13 history:** `.hist-*` (flow list + timeline), `.scrub-*` (the map scrubber strip + playhead — the
  slider is a native `<input type="range">` styled on tokens, no library), `.ledger-*` (segment list +
  the `⛓` chain chips + the Vault×ledger callout). Edge pulse reuses `.sysmap-edge.pulse` from F6
  **unchanged** (same animation primitive, historical frame data).

**Status-color discipline (inherited):** every color pairs with a glyph or text tag (never color-only) —
`◇` design-ahead, `🔒` sealed, `⚠` revealed, `⛓` chain-verified, the outcome chips carry their word.
Density stays operator-grade (12–13px, mono for keys/ids). Keyboard/a11y floor inherited: route changes
move focus to the view heading; `.confirm-modal` focus-traps + ESC-closes; every key is a real
`<a href="#/…">`.

---

## 6. Cross-lane asks (filed to `lattice.md`, `🚧 blocks` the dependent fire)

These are the platform primitives F10–F13 need from the Lattice lane. Each blocks a specific fire.

| # | Ask | Blocks | Notes |
|---|---|---|---|
| A | **Gateway into `make up-full`** (build `cmd/gateway`, start in the `orchestration` tier with `GATEWAY_DEV_MODE=true` + dev key) | F10 truthfulness, F11 | `run-gateway` exists; `up-full`'s `orchestration` target doesn't start it. Without it Gateway is a ghost / must be `designAhead:true`. |
| B | **Gateway heartbeat `jwks` block** — `internal/gateway.Heartbeater` adds `{keys:[{kid,source,alg,addedAt}], lastPoll, swaps}` to `health.gateway.<instance>` | F11 JWKS panel | Preferred over a Gateway HTTP read endpoint — keeps Loupe a pure Health-KV reader. |
| C | **The revocation set** — a `gateway-revocations` operational KV bucket (Loupe writes, Gateway Verifier reads) **OR** a `RevokeActor` op + projecting lens | F11 revoke | The fork (§2.3 / §8). Op model = P2-clean + auditable (appears in the Chronicler ledger — a bonus); bucket model = lower-latency kill-switch, needs Loupe's first operational-KV write. |
| D | **A dedicated `health.vault.<instance>` heartbeat group** (DEK count, `vault_calls_total`, `keyshredded_handled_total`, backend, privacy-critical tier state) | F12 node + page | Today those metrics ride the Refractor/privacy-worker heartbeat; a Vault node wants its own group id. |
| E | **`lattice.vault.decrypt` reachable from Loupe's actor** (grant/capability if the responder gates callers) | F12 Reveal | The vault design *names* Loupe a trusted plaintext consumer; confirm Loupe's NATS actor can call the responder. |
| F | **Loupe's operator actor granted `ShredIdentityKey`** (scoped grant) | F12 shred proof | `ShredIdentityKey` has a no-default-grant posture; the flagship demo needs Loupe to submit it (else it runs only under `LATTICE_AUTH_MODE=stub`). Andrew's call — a scoped grant fits Loupe's trusted-operator role. |
| G | **`orchestration-history` bucket P5-readable** (it's a lens target — the ordinary read) | F13 L1/L2 | Chronicler F1/F2. No special surface — the loftspace-app read-model precedent. |
| H | **A Loupe-readable historical-event / archive-segment read surface** for edge-level scrubber fidelity (L2-full) and the ledger browser (L3) | F13 L2-full, L3 | The Chronicler F3 archive layout (segment keying, manifest schema, blob fetch) defines this. L2-v1 (flow-liveness) needs none of it; L2-full + L3 bind to F3's actual layout. |

**Sequencing note for the Steward:** F10 + F11 are near-term (asks A–C, all small). F12 unblocks when the
Vault node heartbeats + the decrypt RPC is reachable (asks D–F). F13 L1 unblocks with ask G (Chronicler
F1/F2 shipped); L2-v1 rides L1's data; L2-full + L3 wait on ask H (Chronicler F3).

---

## 7. Fire decomposition — appends to the program's §14 table

F10–F13 extend the program's §14 build map. Same gate discipline (`go build`, `make vet`, `golangci-lint`,
`lint-conventions` STRICT, `go test ./cmd/loupe/...`, goja logic tests, the goja-parse syntax check over
`web/js/**`, in-browser verify against `make up-full`). Review depth: F11 and F12 touch the security plane
(revoke, decrypt, shred) → **full 3-layer**; F10 and F13 are substantial → full 3-layer (F13 for the new
leg; F10 for the map-substrate + rollup-semantics change). One FE surface = one fire at a time.

| Fire | Size | Delivers | Depends on |
|---|---|---|---|
| **F10 — Curated topology (Gateway · Vault · Chronicler nodes)** | **M** | `declaredComponents`/`skeletonEdges`/`sysmapTier` deltas (§1.2) + the `ingress` node kind, `object-store` infra node, Vault `lateral` placement, tier -1 band in `map.js` (§1.3); the `designAhead` flag + `design-ahead` status rendering (accent, not red; excluded from the rollup — §1.4); Vault/Chronicler render honest-pending until they heartbeat. | F1 (shell); ask A for Gateway to be live (else Gateway ships `designAhead` too) |
| **F11 — Gateway security console** | **L** | `#/component/gateway` — auth-failure rate + the three counters (§2.2), the live JWKS key-set panel (reads the heartbeat `jwks` block, ask B — empty state until then), the revoke surface (form + typed confirm + revoked list, §2.3); `GET /api/gateway/revocations` (+ `/revoke`·`/unrevoke` in the bucket model, or reuse `/api/op` for `RevokeActor`); `logic/gateway.js`. | F10, ask A; ask C (revoke set); ask B (JWKS block) |
| **F12 — Vault surface (design-ahead)** | **L** | `#/component/vault` (§3.1) + `GET /api/vault/shreds`; **Reveal** on sealed aspects in the Graph explorer (§3.2) + `POST /api/vault/decrypt` (privileged, flagged); the **crypto-shred proof** view `?view=shred` (§3.3) submitting `ShredIdentityKey` via `/api/op`; `logic/sensitive.js` + `logic/shred.js`. | Vault build live; asks D, E, F |
| **F13 — Chronicler Time Machine (design-ahead)** | **L** | L1 flow-history browser `#/history` + timeline `#/history/flow/<id>` (§4.1, `GET /api/history/flows`); L2 map scrubber (§4.2, v1 flow-liveness frames `GET /api/history/timeline`, `logic/scrubber.js`) with the F3-gated edge-pulse upgrade; L3 ledger browser `#/history/ledger` + the **Vault×ledger flagship pairing** (§4.3, `GET /api/history/ledger[/<seg>]`, `logic/ledger.js`); `logic/flows.js`. **L4 (point-in-time) is the horizon — not built.** | Chronicler F1/F2 (L1, ask G); F3 (L2-full + L3, ask H); F12 (the Vault×ledger pairing) |

**Program §14 cross-link note (for whoever edits the program doc):** add to the program's §14 a trailing
line — "**F10–F13 (platform-edges extension):** curated topology + Gateway/Vault/Chronicler surfaces + the
Chronicler Time Machine — designed in [loupe-platform-edges-ux.md](loupe-platform-edges-ux.md)." No change
to F1–F9.

**Sequencing:** F10 → F11 (near-term, once Gateway deploys); F12 when the Vault build lands; F13 L1 when
Chronicler F1/F2 ship, L3 + L2-full when F3 ships. F12 and F13 are design-ahead — ready to build the moment
each dependency clears.

---

## 8. Open questions for Andrew

**RESOLVED at adjudication (Andrew, 2026-07-02) — see the Adjudication block at the top. Retained as the
decision record:** (1) revoke → **op model**, refined to Andrew's op-outboxes-event → Gateway-internal-KV
mechanism (a Designer item); (2) **grant** Loupe both `ShredIdentityKey` + `RevokeActor`; (3) **show**
design-ahead; (4) Vault `lateral` flag accepted; (5) ship the **v1** flow-liveness scrubber first.

1. **Revocation model (the F11 fork, ask C).** Is the Gateway revocation set an **operational KV bucket**
   (`gateway-revocations`, Health-KV-class — Loupe's *first* direct operational-KV write, lower latency
   for an emergency kill-switch) or a **Core-KV identity aspect written via a `RevokeActor` op** (P2-clean,
   fully ledgered — and it would then appear in the Chronicler ledger, which is a nice audit property)?
   I lean **op model** (keeps Loupe write-free, auditable, reuses `/api/op`), but an emergency revocation
   arguably wants the no-Processor-round-trip bucket. Your call shapes whether F11 adds write endpoints.
2. **Loupe granted `ShredIdentityKey`? (F12, ask F).** The flagship crypto-shred proof needs Loupe to
   submit `ShredIdentityKey`, which has a deliberate no-default-grant posture. Grant it to Loupe's
   operator actor (fits the trusted-operator console role) so the proof runs under real capability auth —
   or accept the proof runs only under `LATTICE_AUTH_MODE=stub` (dev stack) and document that limit? I
   recommend the scoped grant.
3. **Design-ahead map treatment (F10, §1.4).** I'm rendering not-yet-deployed Vault/Chronicler as
   **`design-ahead`** (accent, dashed, excluded from the rollup) rather than red `absent`, so the map
   doesn't cry wolf on every dev stack for weeks (the "7 degraded" lesson). Confirm you want the map to
   *show the roadmap* this way vs. simply not listing a component until it's built. (I think showing them
   — visibly *coming* — is the better portrait, but it's a product call.)
4. **Vault `lateral` placement (F10, §1.2).** I keep `sysmapTier` pure (Vault is a tier-3-class engine by
   dependency depth) and place it lateral-to-Core-KV via a render-layer `lateral` flag, rather than
   inventing a tier-2.5. Confirm that's the right trade (the alternative distorts the even tier spacing).
5. **Scrubber fidelity expectation (F13 L2).** The v1 scrubber replays **flow-liveness** (which flows were
   in flight) from the `orchestration-history` rows — buildable with L1's data alone. **Edge-level pulse
   replay** ("watch Lattice think" op-by-op) needs the archived event/op stream read back in time
   (ask H, Chronicler F3). Is the v1 flow-liveness scrubber a satisfying "map scrubber" for the near term,
   with edge-pulse as the F3 upgrade — or do you consider the scrubber not worth shipping until it has
   edge-level fidelity? (I designed both; v1 is genuinely useful and independently shippable.)

---

*UX design, Sally, 2026-07-02. Companion to loupe-2-ux-design.md. Design/doc-only (L1) — no application
code written, nothing committed; left in the working tree for Winston's adjudication. F10/F11 build-ready;
F12/F13 design-ahead with every not-yet-built-backend assumption flagged inline (⚠️ ASSUMES).*
