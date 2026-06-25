# Loupe — System Map landing view (UX spec)

**Author:** Sally (UX Designer) · **For:** FE Engineer · **Status:** ready-for-build
**Renders:** `GET /api/systemmap` → `{ nodes[], edges[], overall }` (see `cmd/loupe/systemmap.go`).
**Stack constraint:** vanilla HTML/CSS/JS, no framework, no build step, no CDN/lib — must work offline on
`127.0.0.1`. Match the existing Loupe idioms exactly: `el()`/`$()`/`$all()` helpers, the `api()` fetch
wrapper, `.tab`/`.panel` switching via `lazyLoad`, `:root` CSS custom properties. **No graph library** — the
diagram is hand-laid SVG connectors over absolutely-positioned DOM nodes.

This is the **★★★ top experience** and the **base layer the agent-activity console layers onto** (§6). Build
the DOM structure so the console can grow on top without a rewrite.

---

## 1. Placement — System Map is the landing view

- Add a **first** tab `data-tab="systemmap"` labelled **"System Map"**, and make it `class="tab active"`.
  Remove `active` from the current Core KV tab button.
- Add a **first** panel `<section class="panel active" id="panel-systemmap">`. Remove `active` from
  `#panel-corekv`.
- Keep **all six** existing tabs/panels unchanged (Core KV / Health / Control / Packages / Files / Submit Op),
  in their current order after System Map.
- **Default load swap** (`app.js`, bottom of file): today it calls `loadCoreKV(); loaded.corekv = true;`.
  Change to load the map first instead:
  ```js
  loadSystemMap();
  loaded.systemmap = true;
  ```
  Add `if (tab === "systemmap") loadSystemMap();` to `lazyLoad`, and add `if (tab === "corekv" && !loaded.corekv) { loadCoreKV(); loaded.corekv = true; }`
  to `lazyLoad` so Core KV still loads on first visit now that it is no longer the boot tab.
  *(Design note: Core KV was eagerly loaded at boot; moving the map to boot means Core KV must move into the
  lazy path. This is the one behavioural change to existing tabs — call it out in the PR.)*

---

## 2. Diagram layout — a real map, read top-to-bottom along the data flow

### 2.1 Tier model (the spine reads ingress → commit → fan-out → projection)

Lay the topology in **5 horizontal tiers**, top to bottom, so the dominant eye-path *is* the data flow. Each
node is absolutely positioned inside a `position: relative` stage (`#sysmap-stage`); the SVG connector layer
is a sibling `<svg>` filling the stage, painted **under** the nodes (lower z-index).

```
  ┌──────────────────────── #panel-systemmap ─────────────────────────┐
  │  [ overall rollup banner ]     [ Refresh ]  auto ⟳ ☐   updated 4s  │  ← controls bar (§4)
  ├────────────────────────────────────────────────────────────────────┤
  │  #sysmap-stage (position:relative)                                  │
  │                                                                     │
  │   TIER 0  ingress          ( core-operations )      ← infra pill    │
  │                                   │ ops                             │
  │   TIER 1  engine            [   Processor   ]        ← component    │
  │                              ╱ commit    ╲ outbox                   │
  │   TIER 2  spine      ( Core KV )      ( core-events )  ← infra      │
  │              watch │                  │ CDC (fan-out)               │
  │                    │            ┌─────┼───────┬─────────┐           │
  │   TIER 3  engines  [Refractor] [Weaver][Loom][Bridge][ObjStoreMgr] │
  │                    │            └──── submit ops ──▲────┘           │
  │                    │ project              externalTask │           │
  │   TIER 4  lens     ┌──────────── lens shelf ───────────┐           │
  │   shelf            [lensA][lensB][lensC][lensD] … wraps │           │
  │                    └────────────────────────────────────┘           │
  └─────────────────────────────────────────────────────────────────────┘
```

Tier assignment is **derived from `kind` + `id`**, not hardcoded x/y, so the layout survives backend
node-set changes:

| Tier | Members (by `id` / `kind`) | Notes |
|------|----------------------------|-------|
| 0 ingress | `core-operations` (infra) | the op intake |
| 1 engine | `processor` (component) | the only writer to Core KV / core-events |
| 2 spine | `core-kv`, `core-events` (infra) | the two streams everything hangs off |
| 3 engines | every `component` except `processor` → `refractor`, `weaver`, `loom`, `bridge`, `object-store-manager` | the CDC consumers; render in `declaredComponents` order |
| 4 lens shelf | every `node.kind === "lens"` (each carries `parent: "refractor"`) | dynamic count — see §2.3 |

**Within a tier**, nodes are spaced evenly across the stage width. Compute slot x by
`(i + 1) / (n + 1) * stageWidth` (centered, even gutters). Tier y is a fixed band:
`TIER_Y = [40, 150, 270, 400, 530]` (px from stage top) with node-height ~58px — gives room for the
connector labels between bands. Stage min-height = last lens-row bottom + 40.

> **Routing exception — Refractor.** Refractor is a tier-3 engine but it is the *only* parent of the lens
> shelf, and its inbound edge is `core-kv → refractor "watch"` (from tier 2), not the CDC fan-out. Place
> Refractor as the **left-most** tier-3 slot so its `project` edges drop cleanly into the lens shelf below
> without crossing the other engines' `submit ops` returns. (See §2.2 edge routing.)

### 2.2 Edges — SVG connectors measured via `getBoundingClientRect`

The expected technique, do not deviate:

1. Render every node DOM element first (absolutely positioned). Then, in a `requestAnimationFrame` after
   layout, measure each node's box with `getBoundingClientRect()` **relative to the stage's own
   `getBoundingClientRect()`** (subtract stage left/top) so coordinates are stage-local.
2. For each `edge` in the JSON, look up the `from`/`to` node boxes by `id` (keep a `Map<id, element>`).
   Draw an SVG `<path>` from the bottom-center of `from` to the top-center of `to` (or side-center when the
   two nodes share a tier / flow upward — see routing rules below).
3. Use a **cubic Bézier** with vertical control-point offset (`dy * 0.4`) for smooth orthogonal-ish curves;
   add an arrowhead via a single shared `<marker id="sysmap-arrow">`.
4. Edge **labels** (`edge.label`, e.g. `"ops"`, `"CDC"`, `"submit ops"`, `"watch"`, `"project"`,
   `"externalTask"`) render as a `<text>` at the path midpoint with a small `--bg` rect behind it (`rx=3`)
   so the label is legible where it crosses a connector. Hide labels under ~9px zoom (n/a at v1 fixed scale —
   just always show, they're sparse).
5. **Re-measure + re-draw on `resize`** (debounced ~120ms) and after every data refresh. Connectors are
   ephemeral: clear the `<svg>` innerHTML and rebuild from boxes each time. This is cheap (≤ ~20 edges + N
   lens edges).

**Routing rules by edge direction:**
- **Downward** (`from` tier above `to` tier): bottom-center → top-center. The common case (ops, commit,
  outbox, CDC, watch, project).
- **Upward return** (`from` tier below `to` tier — the four `submit ops` edges `loom/weaver/bridge/objmgr →
  core-operations`): route up the **right gutter**. Exit from the node's right edge, run a Bézier up to
  `core-operations`' right edge. Give these a dimmer stroke (`--border` → `--text-dim` on hover) and a thinner
  width so the return path reads as secondary to the forward flow and doesn't fight the downward spine.
- **Same-tier** (`loom → bridge "externalTask"`): side-center → side-center, a shallow arc.
- **Fan-out** (`core-events → {loom,weaver,bridge,object-store-manager}`): four separate paths from
  core-events bottom-center, each to its target top-center. They naturally splay — no bundling needed at v1.

> **Crossing tolerance:** at v1 we accept a few connector crossings (the return `submit ops` paths over the
> engine row). The right-gutter routing keeps them out of the readable forward spine. Do **not** invest in a
> routing solver — this is a fixed, small, curated topology.

### 2.3 Lens shelf — arbitrary live lens count without overflow

The lens count is dynamic (one node per live Refractor projection in Health KV). Do **not** position lenses on
the even-slot tier formula (it would overflow at 8+ lenses). Instead:

- Render the lens shelf as a **flex-wrap row container** spanning the full stage width at tier 4, NOT
  absolutely-positioned per-node. Lenses are fixed-width chips (~150px) that wrap to additional rows.
- Because lenses are flex children (not absolute), still measure each chip's box via `getBoundingClientRect`
  for the `refractor → <lens>` `project` connectors — the measurement technique is identical, only the
  positioning differs.
- The shelf is the bottom tier, so its rows can grow downward freely; the stage `min-height` expands to fit.
  If lenses exceed ~2 rows, the shelf gets its own `overflow-y: auto; max-height: 240px` and the stage stops
  growing — connectors still resolve to whatever is currently scrolled into view (acceptable: lens
  drill-in is the primary interaction, not the connector to an off-screen lens).
- Each `project` connector originates from Refractor's bottom edge and fans to the lens chips. With many
  lenses this becomes a dense fan from one source — that reads correctly as "Refractor projects all of
  these," which is the intended mental model.

---

## 3. Node visual language

One shared status→color contract, reusing the **exact existing theme tokens** (`style.css :root`) — no new
colors. Health/Control already use `.green/.yellow/.red`; stay consistent.

### 3.1 Shape & size per `kind`

| `kind` | Shape | Size | Rationale |
|--------|-------|------|-----------|
| `infra` | **rounded pill / stadium** (`border-radius: 999px`), `--bg-raised`, thin `--border`, mono label | ~150×40 | the spine — visually "pipes/streams", recedes |
| `component` | **rectangle card**, `border-radius: 8px`, `--bg-raised`, **3px left status accent** (mirrors `.card` in Health), label + freshness | ~180×58 | the engines — the things you act on |
| `lens` | **small chip**, `border-radius: 6px`, `--bg-input`, status dot, mono `id`-derived label | ~150×34 | projections — many, lightweight, subordinate to Refractor |

Reuse the `.card` border-left-accent idiom so a component node feels like a Health card placed on the map.

### 3.2 Status → token mapping (drive via a CSS class on the node)

Apply a `data-status` attribute AND a status class so CSS can style border/dot. Map status strings exactly as
the backend emits them:

**component** `status` (`green | stale | absent | unknown`):
| status | treatment |
|--------|-----------|
| `green` | left-accent + status dot = `--green`; full opacity |
| `stale` | left-accent + **yellow border** (`1px solid var(--yellow)`) + dot `--yellow`; show "stale" tag |
| `absent` | **dashed border** (`1px dashed var(--red)`), node `opacity: 0.55`, dot `--red`, label struck/dim; this is the "expected but not running" treatment |
| `unknown` | dot `--yellow`, border `--border` (heartbeat doc present but no parseable timestamp) |

**lens** `status` (`active | yellow | paused | rebuilding | unknown`):
| status | dot color | note |
|--------|-----------|------|
| `active` | `--green` | healthy projection |
| `yellow` | `--yellow` | consumer lag / errorCount (`issues[]` carries which) |
| `paused` | `--yellow` | show a small ⏸ glyph |
| `rebuilding` | `--yellow` | show a small ⟳ glyph |
| `unknown` | `--text-dim` | grey dot |

**infra** `status` is always `"present"` → neutral `--border`, no status color (the spine doesn't "fail" in
this view; if Loupe couldn't read Health KV at all you get the error state, §5).

Reuse the existing `.card.stale/.unknown/.paused/.rebuilding { border-left-color: var(--yellow) }` precedent —
the yellow family is shared across non-green-non-red states.

### 3.3 What each node displays inline

- **component:** `node.label` (bold) · status dot · `node.freshness` (small, `--text-dim`, mono — e.g.
  "12s ago"). `node.detail` (the instance id) shown small under the label, truncated with ellipsis
  (`white-space:nowrap; overflow:hidden`). If `node.issues?.length`, show a `⚠ N` count badge (`--yellow`),
  full list on hover/tooltip (§4).
- **infra:** `node.label` only, mono, dim.
- **lens:** status dot · `node.label` (the resolved lens name when present, else the `id`). `node.detail`
  ("lens" or the lens description) and `node.issues[]` surface on hover.

### 3.4 Overall rollup banner

Top of the panel, above the stage, reusing the existing **`.rollup`** component verbatim (it already has
`.green/.yellow/.red` background-tint styles in `style.css`):

```html
<span class="rollup green|yellow|red">GREEN|YELLOW|RED</span>
```
Driven by `body.overall`. Pair it with a one-line plain-English summary next to it, derived client-side:
- `green` → "All components healthy."
- `yellow` → "N component(s)/lens(es) degraded." (count nodes whose status ∉ {green, active, present})
- `red` → "N component(s) absent." (count `status === "absent"`) — and the banner is the call to action.

When `overall: "red"`, give the banner a subtle persistent presence (it already tints red); additionally add a
1px `--red` top-border to the stage so the whole map reads "something is down."

---

## 4. Interactions (v1 — light)

Keep every drill-in consistent with the existing tab-switching mechanism (`btn.click` →
`$all('.tab').remove('active')` … → `lazyLoad`). Factor that into a small helper so the map can call it:

```js
function switchTab(tabName) {
  $all(".tab").forEach((b) => b.classList.remove("active"));
  $all(".panel").forEach((p) => p.classList.remove("active"));
  $(`.tab[data-tab="${tabName}"]`).classList.add("active");
  document.getElementById("panel-" + tabName).classList.add("active");
  lazyLoad(tabName);
}
```
*(Refactor the existing tab `click` handler to call this same helper so there's one code path.)*

**Refresh:**
- A **Refresh** button in the controls bar re-fetches `/api/systemmap` and re-renders (clears stage, rebuilds
  nodes, re-measures, redraws edges). Show "updated Ns ago" / a spinner-in-button while in flight, mirroring
  the `setStatus` pattern.
- **Optional auto-refresh** (design recommendation, ship behind a checkbox **default OFF**): a labelled
  checkbox "auto ⟳" that, when on, polls every **10s** via `setInterval`. 10s matches the component
  heartbeat/stale cadence granularity and is cheap (one JSON GET, no NATS round-trip on the client). Pause the
  interval when `document.hidden` (visibilitychange) so a backgrounded tab doesn't poll. Clear the interval
  when the user leaves the System Map tab. *(Rationale: an operator watching a deploy wants the map to breathe;
  but auto-refresh fighting a hover-tooltip or a mid-read is annoying, so default off, opt-in.)*

**Click drill-ins** (cursor: pointer on `component` and `lens` nodes; infra nodes are non-interactive):
| Click target | Action |
|--------------|--------|
| `component` node (refractor/weaver/loom) | `switchTab("control")`. These three have Control columns (`.control-col[data-comp=…]`). Then scroll that column into view / focus it. For **refractor/weaver/loom**, prefill the column's `.control-name` input is **not** applicable for a component-level click (it's the whole component) — just switch and the column is there. |
| `component` node (processor / bridge / object-store-manager) | No Control column exists for these → `switchTab("health")`. The Health tab shows their heartbeat card. *(Design choice: Control only has refractor/weaver/loom columns today; route the other engines to Health where their card lives, rather than a dead click.)* |
| `lens` node | `switchTab("control")` AND prefill the **Refractor** column's `.control-name` input with the lens `node.id`, so the operator can immediately inspect/pause/resume/rebuild it. (The Refractor column is per-lens by id — `index.html` line ~62.) Secondary affordance: a small "KV" link on the lens hover-card → `switchTab("corekv")` + set `#corekv-prefix` to the lens's meta-vertex key and trigger `loadCoreKV()`. |
| `component` node, secondary "Health" link on hover | `switchTab("health")` for any component, so Health is always one hop away regardless of primary route. |

**Hover → detail (tooltip):** on `mouseenter` of a component or lens node, show a small absolutely-positioned
detail popover (a `div.sysmap-tip`, `--bg-raised`, `--border`, mono small) near the node containing:
`id`, `kind`, `status`, `detail` (instance/desc), `freshness`, and each `issues[]` line in `--yellow`. Dismiss
on `mouseleave`. This is the home for everything that doesn't fit inline (§3.3). No click needed to read it.

---

## 5. States

| State | Trigger | Treatment |
|-------|---------|-----------|
| **Loading** | initial fetch / refresh in flight | Button shows "Loading…" disabled; stage shows a centered `el("div","muted","loading the system map…")`. Don't blank a previously-rendered map until the new data arrives (avoid flthis on auto-refresh) — render into a temp, swap on success. |
| **Error** | `body.error` present (the `api()` wrapper maps transport failures + non-JSON to `{error}`) | Replace the stage with a centered error card: `el("div","error-text", body.error)` plus a Retry button. Keep the rollup banner empty/neutral. This covers "Loupe can't reach NATS / Health KV." |
| **overall: red + absent components** | one or more `status:"absent"` | Render the full map; absent nodes use the dashed/dim treatment (§3.2); banner tints red with the "N absent" summary (§3.4); stage gets the red top-border. The map is still fully readable — absence is information, not a failure of the view. |
| **Empty / no-health** | components all `absent`, **zero** `lens` nodes | Still render the skeleton (infra + the 6 declared components, all dashed/absent). Show a muted hint under the stage: "No live components reporting — is the stack running? (`make up-full`)". The lens shelf renders an empty `el("div","muted","(no lenses projecting)")` placeholder. |
| **Many lenses** | large `lens` count | §2.3 shelf wrap + `overflow-y:auto` cap. |

**Responsive (laptop width):** the app is `max-width: 1280px; margin: 0 auto`. Design the stage for a usable
width of ~1100–1240px. Below ~900px (narrow window) the 5-engine tier-3 row gets cramped: allow the tier-3
engine row to **wrap to two rows** (it's the same flex/measure approach as the lens shelf) and re-measure —
connectors re-resolve to wrapped positions. Don't target mobile/phone widths (this is a localhost operator
tool). Minimum sensible width ~720px; below that, horizontal scroll on the stage is acceptable.

---

## 6. Future hook — agent-activity console attaches here

The agent-activity console (backlog ★★★, `implementation-artifacts/agentic-ops-design.md`) is the **ops layer
that layers onto this map**: the Steward's queue + work-in-flight, the **L3 contract-review queue** (Andrew's
touchpoint), and **per-agent Health**. Structure for it now so it's additive, not a rewrite:

- **The agents emit Health KV like components.** When the console ships, agent health will arrive as more
  Health-KV reporters. Keep the node-rendering + status-dot + hover-tip code **kind-agnostic** (driven by
  `node.kind` + `node.status` lookup tables), so a future `kind:"agent"` node type is a new tier/row + a new
  row in the status table, not new rendering logic.
- **Reserve a right rail.** Lay the System Map panel as a CSS grid `grid-template-columns: 1fr` for v1, but
  put the stage in a `<div id="sysmap-main">` so a future `<aside id="sysmap-console">` can become the second
  grid column (`1fr 320px`) holding: **Steward queue** (board items in flight), **L3 review queue** (the
  what/why/affected-consumers cards — *not* raw diffs), **per-agent health** chips. v1 renders nothing there;
  the slot just exists in the DOM/CSS so the FE for the console is a drop-in.
- **One refresh clock.** The 10s auto-refresh loop (§4) should be written as a single `refreshSystemMap()`
  that the console can extend to also pull its queues — so the operator surface has one heartbeat, not two
  competing intervals.

Leave a one-line `// agent-activity console mounts in #sysmap-console — see loupe-system-map-ux.md §6` comment
at that DOM slot (this is a forward-pointer to a planned feature, not a history/changelog comment — allowed
under the house rule, which bans *change-narrating* comments, not present-tense structural ones).

---

## 7. CSS — new tokens? No. New classes — yes (suggested names)

Add **no** `:root` variables (reuse `--green/--yellow/--red/--bg/--bg-raised/--bg-input/--border/--text/
--text-dim/--accent/--mono`). New classes to add to `style.css`, namespaced `sysmap-`:

```
#panel-systemmap .controls-bar   flex row: rollup + summary + Refresh + auto-refresh checkbox + status
#sysmap-stage                    position:relative; min-height set in JS; overflow:visible
#sysmap-edges                    absolutely-filled <svg> under nodes (z-index:0)
.sysmap-node                     base; position:absolute; z-index:1; cursor default
.sysmap-node.component           rect card, 3px left accent (reuse .card feel)
.sysmap-node.infra               stadium pill, dim, mono
.sysmap-node.lens                small chip (flex child in the shelf)
.sysmap-node.absent              dashed --red border, opacity .55
.sysmap-node.stale               1px solid --yellow border
.sysmap-dot                      8px round status dot; .green/.yellow/.red/.dim variants
.sysmap-shelf                    tier-4 flex-wrap container for lenses
.sysmap-tip                      hover popover, --bg-raised/--border, mono small, z-index:20
.sysmap-freshness                small --text-dim mono
.sysmap-issue                    --yellow line (reuse .card-issue look)
```
Match existing radii (6–8px), 1px `--border`, the `.rollup`/`.card`/`.state-tag` visual family. The map should
look like it was always part of Loupe.

---

## 8. Build checklist (acceptance)

1. System Map is the **default active** tab on load; renders `/api/systemmap` without a click. Core KV still
   loads correctly when first selected.
2. Five tiers render top-to-bottom; `core-operations` at top, lens shelf at bottom; node placement derived
   from `kind`+`id`, not hardcoded.
3. SVG connectors drawn via `getBoundingClientRect` (stage-local coords), with arrowheads and the exact
   `edge.label` strings; redrawn on resize (debounced) and on refresh.
4. Node status colors use only existing theme tokens; `absent` = dashed/dim, `stale` = yellow border, green =
   healthy, per §3.2 — for both component and lens vocabularies.
5. `overall` rollup banner reuses `.rollup` + a derived one-line summary; red state adds the stage top-border.
6. Click a component → Control (refractor/weaver/loom) or Health (processor/bridge/object-store-manager);
   click a lens → Control with the Refractor `.control-name` prefilled; hover → detail popover with
   `issues[]`. All drill-ins go through the shared `switchTab()` helper.
7. Loading / error / red-absent / empty-no-health states all handled (§5); responsive down to ~900px via
   tier-3 + shelf wrapping.
8. DOM has the `#sysmap-main` / reserved `#sysmap-console` slot and a single `refreshSystemMap()` clock (§6).
9. No framework, no build step, no external lib; works offline on `127.0.0.1`.

---

## Open choices made autonomously (no human available — flagging for review)

- **Auto-refresh default OFF, 10s when on.** Could be defaulted ON; chose off to avoid fighting hover/read.
- **processor/bridge/object-store-manager click → Health** (no Control column exists for them today). If
  those get Control columns later, reroute to Control.
- **Lens-shelf scroll cap at ~2 rows.** Pure ergonomics guess; adjust once we see real lens counts.
- **A few accepted connector crossings** (return `submit ops` paths) — explicitly *not* solving routing at v1.
- **`absent` rendered, not hidden.** Showing the expected-but-down skeleton is the whole point of a
  self-truthing map; never drop absent nodes.
```
