# Loupe 2.0 — the map is the console (UX design, full program)

**Author:** Sally (UX Designer) · **For:** FE Engineer + Lattice Steward (build lane) · **Scope:** the whole Loupe operator-console redesign (PO items L1–L7, reviewed with Andrew 2026-07-01)
**Status: ADJUDICATED — Winston, 2026-07-02 (Andrew delegated build authority). Build authorized; fires per §14.**
Adjudication amendments are folded into the body (§4 protected-lens semantics corrected against the live
stack; §14 gates de-Node'd; §15 questions answered in place). Build checkpoints (🏗️ worktree · done · next)
append at the end of this doc.

**Stack constraint (unchanged, binding):** vanilla ES modules + `go:embed` static assets, **no Node
toolchain, no framework, no build step, no CDN** — works offline on `127.0.0.1:7777`. Dark theme on the
existing `style.css` tokens (`--bg/--bg-raised/--bg-input/--border/--text/--text-dim/--accent/--accent-dim/
--green/--yellow/--red/--mono`) — **no new `:root` tokens**. New Go endpoints on `cmd/loupe` are in-scope
and cheap. Control-plane op allow-lists are FIXED (`cmd/loupe/control.go`): refractor
`health/validate/rebuild/pause/resume/delete`; weaver `list + disable/enable/revoke`; loom
`list/consumers + inspect/pause/resume` — design inside them.

**Live magnitudes designed for:** 6 components (each 1..N instances), 29 lenses today → design to ~60,
244 vertices today → design list paging to ~5k, 42 map edges.

---

## 0. What this is — one console, one graph, one spine

Loupe today is eight disconnected tabs: a map that drills into paste-a-NanoID columns, a flat 244-row
Core KV scroll with dead-end link rows, a Health tab duplicating the map's overlay, and a Control tab that
makes the operator the message bus. Loupe 2.0 makes the **System Map the home page of a navigable
console**: every entity everywhere is a link, every view has a URL, and the three things Lattice is
showing off become the three legs of the IA:

1. **Lattice IS a graph** → the Graph explorer (§7): click any link to walk it, any key in any document is
   a hyperlink, a vertex-centered neighborhood view renders links as sentences.
2. **Lenses are the read path** → the Lens page (§6): definition (DDL from the graph) · state (health) ·
   control · **contents** (the projected read model itself, browsable).
3. **Everything converges through events** → the Live pulse (§8): watch an op commit, fan out on
   core-events, and light up the engines on the map in real time.

Design posture per the PO review: **bold, not incremental** — the tab chrome survives, everything behind
it is re-plumbed. Loupe stays the trusted loopback inspector (P5 exception, `docs/components/loupe.md`);
nothing here adds auth or changes its security posture.

---

## 1. Information architecture

### 1.1 Route table (hash router — every view URL-addressable)

Navigation is a **hash router** (`#/…`), zero server routing changes, native back/forward via
`hashchange`, deep links shareable/bookmarkable. Keys contain dots but never `/` or `#`, so a raw key is a
safe path segment. Query params after `?` inside the hash.

| Route | View | Replaces |
|---|---|---|
| `#/map` | System Map — **home** (default on load + unknown-hash fallback) | System Map tab |
| `#/graph` | Graph explorer: type-faceted entity list | Core KV tab |
| `#/graph?type=identity&q=smith&deleted=1` | filtered list state (URL-carried) | — |
| `#/graph/<key>` | Entity detail — any Core KV key: vertex, meta, link, aspect, op tracker | Core KV detail pane |
| `#/graph/<key>?view=hood` | Neighborhood (ego-graph) mode centered on `<key>` | — (new) |
| `#/component/<id>` | Component page: the six declared engines + any runtime-discovered heartbeat group (§5) | Control columns + Health cards |
| `#/lens/<id>` | Lens page (id = lens NanoID = its `vtx.meta.<id>` = its Health-KV key) | Control→Refractor paste flow |
| `#/packages` | Package list | Packages tab |
| `#/package/<key>` | Package detail + lifecycle | — (new) |
| `#/tasks` | Task inbox (kept, links re-pointed) | Tasks tab |
| `#/files` | Objects/upload (kept) | Files tab |
| `#/op` · `#/op?type=<operationType>` | Submit Op (kept + follow-through §10; `type=` pre-selects) | Submit Op tab |

**Tabs become nav links** (same `.tab` visual family): **Map · Graph · Packages · Tasks · Files ·
Submit Op** — six. **Health and Control tabs are retired** (Health absorbed by the map + component pages
§4–§5; Control dissolved into component/lens pages §5–§6). Component/lens/package detail pages are
drill-through pages, not tabs; the nav highlights their parent section (Map for components/lenses,
Packages for a package).

Router core is a pure function (goja-testable, §2.3):
`parseRoute(hash) → { view, arg, params }` — e.g. `parseRoute("#/graph/vtx.role.abc?view=hood")` →
`{ view:"graph", arg:"vtx.role.abc", params:{view:"hood"} }`. A `navigate(route)` helper sets
`location.hash`; the single `hashchange` listener dispatches to the view module. Unknown route → `#/map`
with a small toast-style notice, never a blank panel.

### 1.2 No dead ends — the key resolver

**Rule: every id/key rendered anywhere in the console is a link.** One shared resolver decides where a
key-shaped string goes; the FE calls `keyLink(key)` everywhere it renders one (list rows, document JSON,
tooltips, control replies, feed rows, task cards):

| Key shape | Resolves to | Note |
|---|---|---|
| `vtx.<type>.<id>` | `#/graph/vtx.<type>.<id>` | incl. `vtx.task.*`, `vtx.package.*` |
| `vtx.meta.<id>` (class `meta.lens`) | `#/graph/…` **plus** a paired "lens page →" affordance to `#/lens/<id>` | detail page cross-links both ways |
| `vtx.op.<requestId>` (op tracker) | `#/graph/vtx.op.<requestId>` | 24h TTL — an expired tracker renders a friendly "op tracker expired (24h TTL)" state, not an error dump |
| `lnk.<a>.<idA>.<rel>.<b>.<idB>` | `#/graph/<full link key>` | the link *document* view: envelope + both endpoints as links + the sentence rendering (§7.3) |
| `vtx.<type>.<id>.<localName>` (aspect) | `#/graph/<parent vertex>` scrolled to that aspect row (fragment param `?aspect=<localName>`) | aspects are viewed in their parent's context |
| component id (`processor` … ) | `#/component/<id>` | map nodes, feed rows |
| lens NanoID in a health/control context | `#/lens/<id>` | map lens chips, refractor roster |
| package key | `#/package/vtx.package.<id>` | packages list, lens "owned by" |

**Muscle-memory aliases** (the old deep-link habits, re-pointed — each old affordance keeps existing but
lands on the new IA):

| Old habit | New behavior |
|---|---|
| map lens hover → "view in Core KV" | "open lens page" → `#/lens/<id>` + a secondary "meta-vertex in Graph" → `#/graph/vtx.meta.<id>` |
| map component click → Control/Health tab | → `#/component/<id>` |
| Task "Complete in Submit Op →" | → `#/op?type=<opName>` (router-param prefill — same pre-selection, now URL-addressable) |
| Core KV prefix box (`vtx.role.`) | Graph explorer type facet + search `q=` (a raw-prefix escape hatch remains in the Graph toolbar) |

### 1.3 Breadcrumbs + history

- Native browser back/forward work by construction (hash history).
- A **breadcrumb bar** renders under the topbar on drill pages: `Map › Refractor › clinicAppointments`
  or `Graph › vtx.identity.V1Sta…` — each crumb a link. On `#/graph/<key>` the key itself renders
  segment-decomposed (`vtx . identity . V1StGX…`) with the type segment linking to the type-filtered list.
- The Graph explorer additionally keeps a **session trail** (last ~15 visited keys, in-memory) rendered as
  a compact "recently viewed" row in the list view — cheap re-entry into a walk.

---

## 2. Shell

### 2.1 Topbar + global alert strip

Topbar unchanged in feel: brand · nav links · (new, right-aligned) a **live status cluster**: the overall
rollup pill (`.rollup green|yellow|red`, always visible on every view — it is the one-glance platform
answer) + the pulse LED (§8.4). Clicking either → `#/map`.

**Alert strip (global, all views).** A one-line strip directly under the topbar, present **only when there
is something to say**, in priority order:

1. `health.alerts.*` lines — rendered verbatim in the existing `[severity] key: message` form (the live
   `[warning] health.alerts.security.stub-auth-active` MUST keep appearing exactly like this). Severity
   colors: `[error]` → `--red`, else `--yellow`. Multiple alerts: show the worst + "＋N more" expanding
   the strip.
2. `health.bootstrap.complete` **absent** → a `--red` strip line "bootstrap incomplete — kernel seed not
   verified (`make up`)" and the overall pill goes red (existing `computeHealth` rule, kept).

The strip is not dismissible (it reflects live state; it disappears when the alert key does). Data rides
the same `/api/health` poll the map already makes; on non-map views the shell polls `/api/health` at a
relaxed 30s just for the strip + pill.

### 2.2 Layout regions

```
┌ topbar: brand · nav · rollup pill · pulse LED ────────────────┐
├ alert strip (conditional, global) ────────────────────────────┤
├ breadcrumb bar (drill pages only) ────────────────────────────┤
│ <main> — the routed view                                      │
└───────────────────────────────────────────────────────────────┘
```

`main` keeps `max-width: 1280px`. Views manage their own internal grids (map rail §3.1, graph split §7,
component two-column §5). Minimum sensible width stays ~900px; below it rails stack under their main
column (the existing map wrap behavior generalized).

### 2.3 Module structure — the logic/DOM split stays goja-testable

`app.js` (1142 LOC single script) decomposes into ES modules under `web/js/`:

```
web/js/
  main.js            entry: router wiring, shell, boot   (module, DOM)
  router.js          parseRoute/navigate                 (logic + a thin DOM binding)
  api.js             api()/setStatus()/el()/$ helpers    (DOM-adjacent)
  logic/keys.js      isEntityKey, classifyKey mirror, keyTarget (the §1.2 resolver), shortId
  logic/status.js    the §4 status vocabulary tables + rollup summarizer
  logic/reads.js     deriveReads, coerceField, schemaTypeLabel (Submit Op logic)
  logic/route.js     parseRoute (pure)
  logic/feed.js      pulse feed ring buffer, event→feed-row shaping, poll-diff derivation (§8)
  logic/hood.js      ego-graph model: radial layout math, expansion set, "+N more" grouping (§7.4)
  views/map.js …     one module per §1.1 view (DOM render + fetch orchestration)
  pulse.js           EventSource lifecycle + map edge animation hooks
```

**Reconciliation with `loupe-fe-test-strategy-design.md` (✅ ratified 2026-07-02, amendment folded there):**
the convention is: **`logic/*.js` files contain only declarations (no `import`, no DOM/`fetch`/timer/
`async` references) and exactly one trailing `export { … }` statement**, in **ES6-conservative syntax**
(goja = ES5.1 + most-of-ES6: no optional chaining, no nullish coalescing; ES2017+ built-ins get their ES5
spellings — the harness's parse failure is the loud gate). The goja harness loads a logic file by
stripping the trailing `export` line (a 2-line transform in the Go test) and reading the declared
functions — same seam, same zero-toolchain property. The harness + dep land **with F1**. Everything in
`logic/` is pure by construction and every view module keeps its `shape(json) → viewModel` functions in
`logic/` when they grow beyond trivial.

---

## 3. The map is home (`#/map`)

The shipped map (loupe-system-map-ux.md) is the base layer — tiers, SVG connectors, status tokens, the
auto-refresh clock all stay. Changes:

### 3.1 The right rail — pulse feed + preserved console reservation

`#sysmap-main` becomes the two-column grid the system-map spec reserved:
`grid-template-columns: 1fr 320px`. The second column is a new `<aside id="sysmap-rail">` whose children
**stack**:

1. `<div id="sysmap-console">` — **first child, rendered empty.** This is the reserved mount for the
   Andrew-gated agent-activity console (`loupe-system-map-ux.md` §6, `loupe-agent-activity-console-design.md`
   — shelved, design around it, do not build it). The existing forward-pointer comment moves onto this
   element. When that console ships it occupies the top of the rail; nothing in this design may claim the
   slot.
2. `<section id="sysmap-gates">` — the **gates panel** (§4.3): one compact row of chips.
3. `<section id="pulse-feed">` — the **live activity feed** (§8.3), filling remaining rail height,
   internally scrolled.

Below ~900px the rail stacks under the stage (feed capped at ~240px). The rail refreshes on the **same**
`refreshSystemMap()` clock (one heartbeat — the §6 rule holds; the SSE feed is push, not a second poll).

### 3.2 Node changes

- **Plural instances (fixes the last-write-wins collapse).** `computeSystemMap` currently keeps one beat
  per component (`beats[group] = b` overwrites). The node payload becomes
  `instances: [{instance, status, freshness, issues[]}]` with the node-level `status` = **worst-of** and
  `freshness` = freshest. A component node with N>1 renders a small `×N` count tag next to the label;
  `detail` shows the worst instance's id. Per-instance truth lives on the component page (§5) — the map
  states the headline, the page itemizes.
- **Click drill-ins:** component node → `#/component/<id>`. Lens chip → `#/lens/<id>`. Hover tips keep
  everything they show today; the lens tip's affordances become "open lens page" / "meta-vertex in Graph"
  (§1.2 aliases). Infra pills stay non-interactive.
- **Lens shelf at ~60:** the shelf already wraps + scroll-caps. Add a small inline filter box
  (`filter lenses…`, substring on label) that appears when the shelf holds >20 chips, and a
  count badge (`29 lenses`) on the shelf header. Chips keep the §4 status treatments — at 60 chips the
  color scan is the point.
- **Rollup summary** uses the corrected vocabulary (§4.2): `pending-readpath` lenses are **excluded** from
  the "degraded" count (surfaced as "N pending read path" instead). "7 degraded" stops crying wolf; a
  genuinely lagging lens still yellows.

### 3.3 Pulse animation

When the SSE feed (§8) delivers an event, the map animates the flow path: `core-operations → processor`
edge flashes, then `processor → core-events`, then the fan `core-events → {consumers}` — implemented as a
`.pulse` class on the corresponding `<path>` (stroke-color to `--accent` + a dash-offset transition,
~600ms, CSS-only, no per-frame JS). Feed-row hover re-fires the highlight for that event's path.
Animation only runs while `#/map` is the active view and the document is visible.

---

## 4. Status vocabulary — one contract, honest about "paused by design"

### 4.1 The problem being fixed

Protected `*Read` postgres lenses (cap-read family + grant tables) start **fail-closed paused** and stay
paused until their Postgres table is provisioned + verified **out-of-band** (the verify-and-pause
activation gate); once verified they activate and project like any lens (observed live 2026-07-02: 7
paused → all active after read-path provisioning ran). So `paused` on a protected lens means **"read
path not provisioned/verified yet"** — an expected, potentially long-lived pending state on a stack that
hasn't run provisioning, not a fault and not an operator pause. Today it renders identically to a manual
pause and yellows the banner for as long as the deployment leaves the read path unprovisioned.

### 4.2 The vocabulary (server-derived, shared by map · component pages · lens pages · rollups)

The Loupe server derives a **renderedState** for every lens by joining its Health-KV reporter doc
(`status`, `pauseReason`, `consumerLag`, `errorCount`, `lastError`) with its `vtx.meta.<id>.spec` from
Core KV (`targetType`, `targetConfig.protected/grantTable`):

| renderedState | Derivation (precedence top→bottom) | Visual | Rollup effect |
|---|---|---|---|
| `fault` | `errorCount > 0` with a `lastError`, or `pauseReason:"structural"` | `--red` dot + red left border, `⚠` | red-worthy detail, yellow overall (matches existing worst-of) |
| `paused` (operator) | `status:"paused"` ∧ `pauseReason:"manual"` | `--yellow` dot + `⏸` glyph + "paused" tag | yellow |
| `pending-readpath` (protected, fail-closed) | `status:"paused"` ∧ spec is a postgres target with `protected` or `grantTable` | **`--accent-dim` outline + a `◆ protected` tag, dot `--accent`** — deliberately *not* in the yellow family; copy: "awaiting read-path provisioning (out-of-band verify)" | **none** — excluded from degraded counts; listed under its own "pending read path" grouping |
| `rebuilding` | `status:"rebuilding"` | `--yellow` dot + `⟳` | yellow (transient) |
| `lagging` | `status:"active"` ∧ `consumerLag > 0` | `--yellow` dot, "lag N" tag | yellow |
| `projecting` | `status:"active"`, no lag/errors | `--green` dot | green |
| `unknown` | anything else / unparseable | `--text-dim` dot | yellow |

`--accent` (the console blue) is the "informational, not a problem" family — it exists in the tokens and
is not used for any health state today, so the distinction is unambiguous at a glance. Ground truth
(confirmed live, 2026-07-02): a **verified** protected lens reports `status:"active", pauseReason:null` —
indistinguishable from any healthy lens in its reporter doc — so the **`◆ protected` tag derives from the
spec join alone** (`targetType:"postgres"` ∧ `protected`/`grantTable`) and renders in **every** state;
the `pending-readpath` row applies only while `status:"paused"`. A verified-then-lagging/faulting
protected lens takes the normal yellow/red states, `◆` tag retained.

Component statuses keep the existing vocabulary (`green/stale/degraded/unhealthy/absent/unknown`)
unchanged.

### 4.3 Health-KV events + gates get homes

- **Component-scoped event keys** (`health.processor.<inst>.step3-latency`, `.malformed-operation.<rid>`,
  `.claim-attempts.<outcome>`, `.auth-trace.<rid>` — today classified `kindEvent` and dropped): rendered on
  that **component's page** (§5) in an "events" section — grouped by event kind, newest first, each row =
  key tail · freshness · expandable raw doc. Not part of any rollup (matches the CLI's exclusion rule).
- **`health.gates.*`** → the map rail **gates panel** (§3.1): one chip per gate
  (`gate2 ✓ · gate3 ✓ · gate4 ✓ · gate5 ✓`), green when `passed:true`, dim "—" when absent (absence is
  informational — gates are written by test suites, not deploys; the panel subtitle says so). Hover shows
  `completedAt` + `commit`. Gate 1 = the bootstrap key, already covered by the alert strip.
- The **Health tab is retired** once component pages exist (fire sequencing §14): cards → component pages,
  alerts → the global strip, rollup → topbar pill + map banner. Nothing the Health tab shows today loses a
  home; the retirement fire's checklist enumerates each element and its destination.

---

## 5. Component pages (`#/component/<id>`) — L2

One template, **dynamically instantiated** — the six declared engines get curated map placement + the
control surfaces below, and **any other `health.<comp>.<instance>` group discovered at runtime** (e.g.
the vertical apps once `vertical-app-health-self-report-design.md` ships its reporters, 📐 lattice lane)
auto-gets the same page: instances + status + freshness + issues + events, read-only control panel.
On the map, undeclared heartbeat groups render as a compact **clients shelf** of chips (no skeleton
edges; hover tip + click → their page) — nothing that heartbeats is ever invisible in the console (the
Health tab, which F4 retires, is today the only surface that renders undeclared groups).
**Layout:** header · two-column body (`1fr 380px`): left = live state,
right = the control surface (read-only info panel for the control-less components).

**Header:** component label + status pill (worst-of) · instance count · "on map" link back to `#/map`.

**Left column — state:**
1. **Instances (plural — the point).** One card per `health.<comp>.<instance>` key: instance id (mono),
   status (§4), freshness, `uptime`, `version`, and the component-appropriate metrics line
   (processor: `ops_consumed/committed/rejected` + `lane_lag`; weaver: `targets`, `marksInFlight`, sweep
   counters, `timers*`; loom: `runningInstances`; refractor: `lensLags` summary — count lagging / total).
   `issues[]` render as the existing `.card-issue` lines.
2. **Component events** (§4.3) — present only for components that emit them (processor today; the section
   renders for any `health.<comp>.<inst>.<…>` keys found, so future emitters appear for free).

**Right column — control (allow-list-shaped, row-level actions — the copy-a-NanoID workflow dies here):**

| Component | Control surface |
|---|---|
| **loom** | *Instances* list (`ctrl list`): each row `instanceId · state` + **inspect** button → inline expandable reply. *Consumers* list (`ctrl consumers`): each row `name · state` + **pause / resume** buttons (resume shown when paused, pause when running). |
| **weaver** | *Targets* list (`ctrl list`): each row `targetId · state` + **disable / enable / revoke** (enable shown when disabled, etc.; **revoke** behind a confirm — it is terminal for the target). Per-target state comes from the list reply verbatim. |
| **refractor** | The **lens roster**: every lens (id · canonicalName · §4 state dot · targetType chip `kv`/`pg`) → each row links to `#/lens/<id>`. Per-lens actions live on the lens page, not here — the roster is the directory. A "pending read path" group footer collects the `pending-readpath` rows so the roster's health scan reads clean. |
| **processor / bridge / object-store-manager** | Read-only panel: "no operator control plane — state is above." For processor, the panel instead hosts the **events** summary counts (malformed ops, claim attempts by outcome) since that is its de-facto observability surface. |

Every action POSTs the existing `/api/control/<comp>/<name>/<op>`, renders the raw reply inline under the
row (the `.control-out` idiom, collapsible), and re-fetches the list — same semantics as today, minus the
paste. Buttons disable while in flight.

**Data:** new `GET /api/component/<id>` returns `{component, instances:[…], events:[…], control:{…}}` —
the health-KV scan filtered to the component (fixing plural), plus the same control reads `controlRead`
already proxies. Refractor's roster comes from `GET /api/lenses` (§6.5).

---

## 6. The Lens page (`#/lens/<id>`) — L3, the heart

Header: canonicalName (falls back to id) · §4 state pill · `targetType` chip (`nats_kv` → `kv` /
`postgres` → `pg`) · lens NanoID (mono, copyable). Four stacked panels:

### 6.1 DEFINITION — the DDL, resolved from the graph

From `vtx.meta.<id>` + its `.spec` aspect (`GET /api/lens/<id>` assembles):

- **Identity row:** canonicalName · description (`.description` aspect) · engine (`spec.engine`, "simple→full
  fallback" when empty) · `projectionKind` when present.
- **Target row:** `targetType` + the adapter config rendered honestly: nats_kv → bucket + key columns +
  deleteMode; postgres → table + key columns + deleteMode + posture chips (`◆ protected` / `public` /
  `grant table`). DSN is **never rendered** (secret-shaped; show "configured").
- **Source query:** the `cypherRule` in a collapsed `<details>` (mono block, open-by-default under ~15
  lines). This is the showcase artifact — the lens IS its query.
- **Output schema:** collapsed `<details>`, pretty-printed.
- **Owned by:** the owning package as a link → `#/package/<key>`, resolved server-side by matching the
  lens canonicalName against installed package manifests; "kernel (bootstrap-seeded)" when no package
  claims it. Plus "meta-vertex in Graph →" (`#/graph/vtx.meta.<id>`) — the full envelope/provenance view
  lives there (§1.2 cross-link both ways).

### 6.2 STATE — control-plane + reporter truth

- Reporter doc fields: renderedState (§4) · `consumerLag` · `errorCount` · `lastError` (red, wrapped) ·
  `activeSequence` · `pauseReason` · `lastUpdated` + computed freshness.
- Refractor heartbeat overlay: `metrics.lensLags[canonicalName]` and `lensLatency` stats (count / mean /
  p95 / p99) when present.
- **Freshness slot (designed now, lights up later):** a labeled row `projection freshness` rendering `—`
  with a muted "(pending: lens-projection-liveness — platform design)" note. When that separate platform
  design ships its signal, this row renders it; the slot, label, and layout are fixed here so the later
  fire is a data bind, not a redesign.

### 6.3 CONTROL — inline, allow-list-shaped

Buttons: **validate · pause · resume · rebuild** — POST `/api/control/refractor/<id>/<op>`, raw reply
inline below (collapsible), then re-fetch state. Enablement by state: `resume` only when `paused`
(operator); `pause` only when projecting/lagging; `rebuild` is the same async re-projection machinery for
every lens (fire-and-forget ack; errors log server-side) — for a `◆ protected` lens it carries an inline
note ("re-projects rows into the verified protected table; the table DDL/verify is out-of-band and
untouched") + a confirm, and is **disabled while `pending-readpath`** (nothing verified to project into).

**delete** — exposed for the first time, styled destructive (the `.file-detach` red-outline family, placed
apart from the others) behind a **typed confirm modal**: "This deletes the projection and its target
rows. Type the lens canonicalName to confirm." Confirm requires exact match; the reply renders inline; on
success navigate back to `#/component/refractor` with a status note (the lens page's subject no longer
exists).

### 6.4 CONTENTS — browse the read model itself

- **nats_kv targets (feasible today):** `GET /api/lens/<id>/rows?limit=200&q=` lists the target bucket's
  keys (server: `KVListKeys(bucket)` + `KVGet` per rendered row, capped + `truncated` flag — the
  Core-KV-list pattern reused). Each row: key (mono) + expandable pretty-printed document with **every
  key-shaped string linkified** (§7.3 renderer reused — a projected row pointing at `vtx.identity.X` walks
  straight back into the graph: the read-path story told in one click). A `q=` substring filter box; row
  count + truncation notice.
- **postgres targets (all protected `*Read` lenses + grant tables):** the panel renders its designed
  empty state: `contents unavailable — postgres target; read seam pending` with a one-line explanation
  ("this projection lives in a Postgres table Loupe cannot read yet; the read-only seam is a flagged
  later fire"). **The panel, toolbar, and row rendering are identical for both target types** — when the
  flagged Loupe-side read-only PG seam ships (fire F9, §14), the panel binds to the same endpoint's PG
  path and lights up with zero layout change.

---

## 7. Graph explorer (`#/graph`) — L4, the showcase

### 7.1 List view — faceted, grouped, paged (244 → 5k)

Two-pane layout (the existing `.split` idiom, list pane widened to 420px):

- **Facet rail (left of the list, inside the list pane header):** one chip per vertex type with counts
  (`identity 12 · role 9 · meta 96 · task 5 · package 14 · op 31 · …`), derived server-side. Clicking a
  chip filters (`?type=`); "all" resets. A search box (`q=`, substring over label + key) and a
  `show deleted` toggle (`deleted=1` — tombstones render dimmed + struck + `del` badge, hidden by
  default). All state URL-carried (§1.1) so a filtered view is shareable.
- **Grouped rows:** within a filter, rows group under type headers (sticky), each row = type badge ·
  label · short id (the existing `.vtx-row` rendering). **Paging, not virtualization:** the server pages
  (`offset`/`limit=500`) and the list renders a "show next 500 (N remaining)" tail row — at 5k keys this
  is 10 clicks worst-case and zero scroll-jank machinery on a no-framework stack. Counts come from the
  facet response so "N remaining" is honest.
- `GET /api/vertices` extends: `type=`, `q=`, `offset=`, `includeDeleted=`, and the response gains
  `facets: {identity:12,…}` + `total`.

### 7.2 Detail view (`#/graph/<key>`) — no dead ends

The current vertex detail, re-plumbed:

- **Header:** key (segment-decomposed, §1.3) · revision · class badge · `isDeleted` flag. For a
  `meta.lens` meta-vertex: the "lens page →" chip. For `vtx.op.<id>`: an "op tracker" badge (+ the
  expired-friendly state, §1.2).
- **Provenance chips (the walk-to-the-op showcase):** a row under the header — `created by <actor>` ·
  `created by op <vtx.op.…>` · `last modified by op <…>` · timestamps — every one a link (`createdBy` →
  the actor's vertex; `*ByOp` → the op tracker). From any vertex you reach the operation that made it in
  one click.
- **Document:** the envelope pretty-printed by the **linkifying renderer** (§7.3) — not a `<pre>` dump.
- **Aspects:** the existing lazy expander rows, their expanded documents also linkified.
- **Links (the fix for the dead ends):** each link row renders as a **sentence**:
  `→ holdsRole  role · 4bfQ…` — the far end (`otherKey`, both directions — `/api/vertex` already returns
  it) is the row's primary click → `#/graph/<otherKey>`. A small `⧉` expander on the row still opens the
  link *document* in place (lazy `/api/corekv/entry`, linkified); the link key itself links to the link's
  own detail view. Direction glyphs `→`/`←` kept.
- **Actions row:** `neighborhood view` (→ `?view=hood`) · `copy key` · for a task vertex, `open task
  inbox`; for a package vertex, `package page →`.

Tasks tab cards re-point here: assignee / scopedTo / forOperation / the task key itself all become
`keyLink`s.

### 7.3 The linkifying document renderer

`renderDoc(json) → DOM`: walks the value tree rendering JSON syntax highlighting-lite (keys dim, strings
default) where **any string value that is an entity key becomes an `<a>`** via `keyLink`. The key test is
the pure `logic/keys.js` `isEntityKey(s)` (prefix `vtx.`/`lnk.` + Contract #1 segment-shape check —
mirrors `classifyKey` in `corekv.go`, incl. rejecting malformed segment counts) — goja-tested against the
Go classifier's cases so FE and server never disagree on what is clickable. Used by: graph detail
(document/aspects/links), lens contents rows, control replies (op-tracker keys in a Weaver reply become
links), Submit-Op replies (§10).

### 7.4 Neighborhood mode (`#/graph/<key>?view=hood`) — the ego-graph

The visual "Lattice IS a graph" moment. Same technique as the system map — absolutely-positioned DOM
nodes + an SVG edge layer measured via `getBoundingClientRect`; **no graph library**.

- **Layout:** the center vertex as a card mid-stage; first-degree neighbors on a radial ring
  (`angle = i / n * 2π`, radius adaptive to count, minimum spacing enforced by bumping the radius).
  Neighbor nodes are compact chips: type badge + label/short-id + status dimming for tombstones.
- **Edges:** straight/gentle-curve SVG paths, each labeled with the relation; hovering an edge shows the
  full **sentence** in a tip: `identity holdsRole role` — source→target order per Contract #1 §1.1 (the
  arrowhead carries direction; the sentence is the teaching device).
- **Expand-on-click:** clicking a neighbor chip fetches its `/api/vertex` (the existing endpoint carries
  everything needed — no new API) and unfolds *its* neighbors in an outer arc sector near it, breadth
  capped. **Node budget ~60:** beyond it, further expansion replaces the oldest non-path sector
  (simple LRU by expansion order) — never an unbounded hairball.
- **Grouping:** a vertex with many same-relation neighbors (e.g. a role held by 30 identities) renders
  one **group chip** `identity ×30 (holdsRole)` that expands to a paged mini-list in a popover; picking
  one materializes it on the ring. This is what makes 5k-vertex graphs walkable.
- **Re-center:** double-click any chip (or its `⌖` affordance) → `navigate('#/graph/<thatKey>?view=hood')`
  — a real route change, so back-button un-recenters. A `list view` toggle returns to detail.
- Layout math + expansion/grouping model live in `logic/hood.js` (pure — positions in, positions out) —
  the DOM layer only paints.

---

## 8. Live pulse (`#/map` rail + map edges) — L5

**Scope guard:** live tail + feed only. Durable history is the separate orchestration-history platform
design — nothing here persists events; refresh loses the feed (by design, stated in the UI header).

### 8.1 Transport — SSE

New endpoint `GET /api/events/stream` (Server-Sent Events over the existing mux — chosen over WebSocket:
one-way is all this needs, `EventSource` reconnects natively, zero client library). Server behavior:

- Per connected client, an **ephemeral ordered consumer** on `core-events` (`events.>`), **deliver-new**
  (no replay — the feed is a tail, not history).
- Each event forwards as one SSE message:
  `{eventId, requestId, eventType, domain, targetKey, timestamp}` (the `processor.Event` envelope minus
  `payload` — the feed links to the entities; the payload is readable at the op tracker / target).
- Bounds: max ~4 concurrent SSE clients (this is a loopback single-operator tool; excess connections get
  a 429-style SSE error event), heartbeat comment every 15s so proxies/idle timers don't kill the stream,
  client slow-consumer protection by dropping (the FE ring buffer makes loss non-fatal; a `dropped: n`
  counter rides the next message).

### 8.2 What the feed shows (real-time + honest derivation)

Two row sources, visually distinguished:

1. **Events (push, real-time):** `12:04:31 · clinic.appointmentCreated · vtx.appointment.8fQ… · op V1S…`
   — eventType mono, targetKey + requestId (`→ vtx.op.<rid>`) as links.
2. **Derived rows (from the existing 10s poll, marked `~`):** state transitions on lens / component /
   client nodes (`~ weaver green → stale`, `~ clinicAppointments projecting → rebuilding`) and lens rule
   updates (`~ clinicAppointments rule updated (seq 41 → 43)`). Marked with the `~` prefix + dim styling
   because they are poll-derived (≤10s lag), not stream truth — honest about the mechanism. (The original
   "re-projected (seq 41→43)" example was spec'd on a false premise: the reporter's `activeSequence` is
   the NATS sequence of the active RULE VERSION — `SetRuleSequence` fires on rule activation/hot-reload,
   never on row projection — and the health entry carries no per-projection counter, so transitions +
   rule updates are the strongest honest poll-derived signals. Grounded during the F6 build.)

Ring buffer capped at 200 rows (`logic/feed.js`, pure). Header: `live` LED · rows/min counter · pause
button (stops appending, stream stays open) · clear.

### 8.3 Degraded modes

| Condition | Treatment |
|---|---|
| `EventSource` drops | LED goes `--yellow`, header line "live tail disconnected — retrying…" (EventSource auto-retries); existing rows retained; map animation stops. |
| Server can't subscribe (NATS down / stream absent) | The stream emits one SSE `error` event with the message; feed shows the empty-state card "no event stream available — <reason>" + the LED `--red`. The rest of the map is unaffected (it already handles NATS-down per-endpoint). |
| No traffic | Empty-state text "listening — no events yet. Submit an op to see the flow." (with a link to `#/op`). |
| Backgrounded tab | Stream stays open (cheap), animation suppressed via `document.hidden` (matches the map's auto-refresh discipline). |

### 8.4 Topbar pulse LED

A small dot in the topbar status cluster mirrors the stream state (`--green` connected / `--yellow`
retrying / `--text-dim` off-map-and-idle). It makes stream health visible from any view without running
the feed everywhere; clicking it → `#/map`.

---

## 9. Packages first-class (`#/packages`, `#/package/<key>`) — L6

### 9.1 List

The existing table + two columns: `installedAt` (from the package vertex envelope) and a per-row
`detail →` link. Row click → `#/package/<key>`. The Install action (§9.3) sits in the toolbar.

### 9.2 Package detail — what it put in the graph

`GET /api/package?key=` resolves server-side and renders:

- **Header:** name · version · package key (link → `#/graph/…` for the raw envelope/provenance) ·
  installedAt.
- **Manifest:** the `.manifest` aspect's description block (collapsed details for the raw doc).
- **Contents — resolved from the graph, all linked (the L6 point):** one section per kind, each item a
  `keyLink` into the Graph explorer (and lenses **also** link to their `#/lens/<id>` page):
  `Entities (vertex types) · Operations · Lenses · Link types · Permissions/grants · Roles`. Resolution:
  match the package's declared canonicalNames to their `vtx.meta.*` vertices server-side (same
  canonicalName-resolution helpers `ops.go` already uses). Each section shows a count; an unresolvable
  declared item renders dimmed with "not found in graph" (honest — never silently dropped).

### 9.3 Lifecycle actions (mechanics exist — F-004; UI adds the confirms)

- **Install from file:** a file picker accepting the package directory's files (`manifest.yaml` + DDL
  YAMLs, multi-select; a `.tar.gz`/`.zip` of the directory also accepted) → `POST /api/packages/install`
  (multipart; server unpacks to a temp dir under the scratchpad-equivalent, runs the `pkgmgr` installer,
  streams the result). Reply (created keys, roles, grants) renders linkified.
- **Upgrade / refresh:** on the detail page — re-submit the package files against the existing install
  (the F-004 in-place refresh semantics: package *edits* apply; a newly-added entity or kernel-seed change
  still needs a fresh bootstrap — the confirm dialog states exactly this caveat).
- **Uninstall:** destructive-styled, typed confirm ("type the package name") + a summary of what will be
  tombstoned (the resolved contents counts from §9.2). Reply linkified.

All three render their full installer reply inline (collapsible) — the operator sees exactly what the
platform did.

---

## 10. Submit-Op follow-through (`#/op`) + Tasks/Files polish — L7

Submit Op keeps its form (catalog select · schema-driven fields · advanced overrides). After submit:

- **Accepted reply panel (structured, not a JSON dump):** status line (`accepted · committed`) ·
  **committed keys** — the `revisions` key set as `keyLink`s with their revision numbers, `primaryKey`
  first and highlighted · `opTrackerKey` link · `committedAt`. The raw reply stays in a collapsed
  `<details>`. A rejected/failed reply keeps today's error rendering (red, verbatim).
- **"What happened next" (rides the pulse):** for ~12s after an accepted reply, the op view subscribes to
  the shared feed filtered by `requestId` and appends: emitted events (real-time) and then any derived
  lens re-projection rows (§8.2). Degraded (stream down): the section renders "live follow-through
  unavailable (event stream disconnected)" — the committed-keys links always work regardless.
- **Session op log:** `sessionStorage`-backed list (cap 50) under the form: time · operationType · status
  chip · requestId link · primaryKey link. Survives route changes, dies with the tab (deliberately —
  durable op history is the platform's, not Loupe's). "clear log" button.
- **Tasks:** keep the inbox; every entity is a `keyLink` (§7.2); "Complete →" navigates `#/op?type=…`.
- **Files:** keep function; the object→owner rows linkify `targetKey`; a vertex page (§7.2) gains an
  "attach file" affordance pre-filling `#/files` target key (polish, not a rework).

---

## 11. API surface — new/changed `cmd/loupe` endpoints

| Endpoint | Change | Serves |
|---|---|---|
| `GET /api/vertices` | + `type`, `q`, `offset`, `includeDeleted`; response + `facets{}`, `total` | §7.1 |
| `GET /api/vertex?key=` | unchanged (already bidirectional links) | §7.2, §7.4 |
| `GET /api/component/<id>` | **new** — plural instances + component events + control reads | §5 |
| `GET /api/lenses` | **new** — roster: `{id, canonicalName, renderedState, targetType, protected}` | §5 refractor, §3.2 |
| `GET /api/lens/<id>` | **new** — definition (meta+spec) ⋈ state (reporter + heartbeat overlay) ⋈ owning package | §6.1–6.2 |
| `GET /api/lens/<id>/rows?limit=&q=` | **new** — nats_kv target bucket list/inspect; postgres → `{unavailable:"pg-read-seam-pending"}` until F9 | §6.4 |
| `GET /api/events/stream` | **new** — SSE tail of `core-events` (`events.>`), deliver-new, bounded clients | §8 |
| `GET /api/package?key=` | **new** — manifest + graph-resolved contents | §9.2 |
| `POST /api/packages/install` / `…/upgrade` / `…/uninstall` | **new** — multipart files / package key; wraps `pkgmgr` | §9.3 |
| `GET /api/systemmap` | node gains `instances[]` (worst-of stays in `status`); lens nodes gain `renderedState` | §3.2, §4 |
| `GET /api/health` | components list per-instance (no LWW); lens entries gain `renderedState`; alerts unchanged | §2.1, §4 |
| retired UI surfaces | `/api/control/*` stays verbatim (component/lens pages call it); no endpoint is removed | §5, §6.3 |

Everything new follows the house handler pattern: pure `computeX(keys, get, …)` assembly functions with
injected getters, unit-tested without NATS (the `computeHealth`/`computeSystemMap` seam), JSON `{error}`
degradation per endpoint, `requireConn` guard, request-scoped timeouts. The SSE handler is the one
long-lived exception — it documents its own lifecycle (client count bound, consumer cleanup on
disconnect).

**Reads stay within the sanctioned surface:** Core KV + health-kv + lens target *nats_kv* buckets +
core-events subscribe — all P5-inspector-sanctioned for Loupe. Postgres reads are exactly the flagged F9
seam and nothing else touches PG. No new writes anywhere: every mutation remains an op submit or an
existing control-plane/allow-listed call (P2 intact).

---

## 12. Visual system & CSS

- **No new `:root` tokens.** `--accent`/`--accent-dim` take on the "informational/by-design" role (§4.2)
  — currently only used for interactive affordances, so a `◆ protected` outline is visually distinct from
  both the health families and plain borders.
- New class namespaces (all additive): `.crumb-*` (breadcrumbs), `.alertstrip`, `.facet-*` (graph facets),
  `.doc-*` (linkified renderer: `.doc-key`, `.doc-link`), `.hood-*` (ego-graph stage/chips/edges),
  `.comp-*` (component pages), `.lens-*` (lens page panels), `.pkg-*`, `.pulse-*` (feed rows, LED,
  `.sysmap-edge.pulse`), `.confirm-modal` (typed confirms — the console's first modal; `--bg-raised`
  panel, `--border`, focus-trapped, ESC closes).
- Density stays operator-grade: 12–13px body, mono for keys/ids everywhere, `.card`/`.rollup`/
  `.state-tag`/`.badge`/expander idioms reused throughout — a component page should look like the Health
  cards grew a control column, not like a new app.
- Keyboard/a11y floor: route changes move focus to the view's `h1`-equivalent; modals focus-trap; every
  `keyLink` is a real `<a href="#/…">` (middle-click/new-tab works — a side benefit of the hash router);
  status colors always pair with a glyph or text tag (⏸ ⟳ ◆ ⚠), never color-only.

**Performance at the stated magnitudes:** graph list pages at 500 rows/pane (5k = paged, no virtual
scroller needed); hood capped at 60 nodes; lens shelf wraps + scrolls + filters at 60; the SSE feed ring
is 200 rows; map redraw remains the measured-boxes rebuild (≤ ~100 edges incl. lens fan). The heaviest
new server call is lens-contents (`KVGet` per rendered row, capped 200 + truncation notice — same cost
profile as the existing Core KV browse).

---

## 13. States & degraded modes (console-wide contract)

| Condition | Treatment |
|---|---|
| NATS down | Every endpoint already returns `{error}`; each view renders its inline error card + Retry (map keeps its existing full-stage error). The shell still routes — a dead stack never blanks the console. |
| Route to a missing entity (`/api/vertex` 502/absent) | An honest not-found card: the key (mono) + "not present in Core KV" + a `neighborhood of last viewed` / `back to Graph` pair. Expired op trackers get the friendlier TTL wording (§1.2). |
| Health key present but unparseable | `unknown` status rendering (existing behavior, kept everywhere the §4 vocabulary applies). |
| Lens meta missing its `.spec` | Lens page renders STATE/CONTROL panels and an explicit "definition aspect missing" card in DEFINITION — control still works (the control plane keys off the id). |
| Postgres-target contents | The designed pending state (§6.4) until F9. |
| SSE degradations | §8.3 table. |
| Empty stack (all absent, zero lenses) | Map keeps its existing skeleton + `make up-full` hint; Graph explorer renders facet counts of 0 with the same hint. |

---

## 14. Fire decomposition — the build map

Independently-shippable fires; each lands green through the standard gates
(`go build`, `make vet`, `golangci-lint`, `lint-conventions` STRICT, `go test ./cmd/loupe/...`, goja
logic tests once the harness lands, a goja-parse syntax check over `web/js/**` — NO Node on the box, the
no-toolchain rule applies to gates too — and in-browser verify against `make up-full`). The build-lane
board mirrors this table. Review depth per CLAUDE.md: full 3-layer for L fires and anything touching the
control/delete surfaces; lead review acceptable for S fires (stated so it can be overridden).
**F1 + the goja harness:** the FE test-strategy design is **✅ Andrew-ratified (2026-07-02)** — F1 ships
the `logic/` split, the strip-export convention, the goja dep (+ its `docs/vendors.md` row), and the
`web_logic_test.go` harness together; later fires extend the harness tables as they add `logic/` modules.

| Fire | Size | Delivers | Depends on |
|---|---|---|---|
| **F1 — Console shell: router + module split + linkify seed** | **M** | Hash router + route table (§1.1) with 1:1 routes for today's eight views (no view redesigned yet); ES-module decomposition of `app.js` with the `logic/` split + strip-export convention (§2.3) and goja tests for `logic/route.js`/`logic/keys.js`; `keyLink` resolver; Core-KV detail's link rows become far-end-clickable + provenance chips linkified (the §1.2 seed); breadcrumb bar. Health/Control tabs still present. | — |
| **F2 — Graph explorer (L4)** | **L** | `#/graph` faceted/grouped/paged list (+`/api/vertices` extensions), full linkifying renderer (§7.3), detail view re-plumb (§7.2), tombstone dimming, tasks-tab links re-pointed, neighborhood ego-graph mode (§7.4, `logic/hood.js` + goja tests). Core KV tab retired (route alias `#/corekv` → `#/graph`). | F1 |
| **F3 — Component pages + Control dissolution (L2)** | **L** | `#/component/<id>` — declared six + runtime-discovered heartbeat groups + the map clients shelf (§5); `GET /api/component/<id>` + plural-instance fixes in `/api/systemmap` + `/api/health`; row-level control actions (loom/weaver); refractor roster (`GET /api/lenses`, links land on `#/graph/vtx.meta.<id>` until F5 ships the lens page); component-scoped health events section; map component click → page; **Control tab retired**. | F1 |
| **F4 — Health absorption + status vocabulary (L1 remainder)** | **M** | Global alert strip + topbar rollup pill (§2.1); gates panel in the map rail (creates `#sysmap-rail` with the preserved empty `#sysmap-console` first slot, §3.1); the §4.2 `renderedState` derivation server-side + visuals on map/rosters (`pending-readpath` stops yellowing the banner — the "7 degraded" fix); **Health tab retired** with the element-by-element destination checklist (§4.3). | F3 |
| **F5 — Lens page (L3)** | **L** | `#/lens/<id>` four-panel page (§6): `GET /api/lens/<id>` + `/rows` (nats_kv path + the pg-pending state), inline validate/pause/resume/rebuild, **delete** behind typed confirm, owning-package resolution, freshness slot, map/roster lens links re-pointed here. | F1, F4 (vocabulary) |
| **F6 — Live pulse (L5)** | **M** | `GET /api/events/stream` SSE + bounded consumer lifecycle; feed in the map rail below the console reservation (§8.2–8.3); map edge pulse animation (§3.3); topbar LED (§8.4); poll-diff derived rows. | F1 (map rail exists after F4 — if built before F4, F6 creates the rail with the same reserved-slot rule) |
| **F7 — Submit-Op follow-through + session op log (L7)** | **S** | Structured accepted-reply panel with linkified committed keys + op-tracker link; `#/op?type=` prefill route; session op log; the ~12s requestId-filtered follow-through section riding the feed (degrades cleanly when F6 absent/down); Files linkification + attach-from-vertex polish. | F1 (full value with F2 + F6) |
| **F8 — Packages first-class (L6)** | **M** | `#/package/<key>` detail with graph-resolved, linkified contents (`GET /api/package`); install-from-file / upgrade / uninstall endpoints wrapping `pkgmgr` + typed confirms + linkified installer replies (§9.3). | F1, F2 (content links) |
| **F9 — Postgres read seam for lens contents (flagged)** | **M** | The Loupe-side **read-only** PG connector (`LOUPE_PG_DSN`, read-only role, SELECT-only, bounded rows) + the `/api/lens/<id>/rows` postgres path; the §6.4 panel lights up for the 7 protected lenses + grant tables. **Flagged:** this softens Loupe's pure-NATS-client property (the same class of call as the shelved agent-console repo seam) — Winston adjudicates explicitly before build; the UI ships in F5 regardless. | F5 |

**Sequencing:** F1 → F2 → F3 → F4 → F5 → (F6 ∥ F8) → F7 → F9. F6 can float earlier (only F1 truly
required); F7 is the natural small closer after F6. One FE surface = one fire at a time (no parallel
fires inside `cmd/loupe/web`). Every fire leaves the console fully usable — no fire strands a retired
tab before its replacement exists (F2 retires Core KV, F3 retires Control, F4 retires Health — each in
the same fire as its replacement).

**Build checkpoint (for the next fire):** F1 ✅ `e6a8a46` · F2 ✅ `976a18f` · F3 ✅ `5865e0e` ·
F4 ✅ `24768e8` · F5 ✅ `7f724c5` (2026-07-02) · F6 ✅ `0821a36` · F8 ✅ `73a3146`+`e1af145`
(2026-07-03; all 3-layer-reviewed). Live now: the Graph explorer (`#/graph` faceted/paged list —
`/api/vertices` carries type/q/offset/includeDeleted + facets/total and sorts keys so offset windows are
stable), the linkifying renderer (`web/js/render.js`: `renderDoc` + `keyLinkEl` — reuse these for any
rendered reply), the hood mode (`logic/hood.js` pure model + goja tests), the `#/corekv` → `#/graph` and
`#/control` → `#/map` aliases; component pages (`#/component/<id>` — `computeComponent`, plural instance
cards, events grouped by kind, row-level loom/weaver control + the refractor lens roster with per-row
inspect/validate/pause/resume/rebuild and a persistent per-column reply box); `GET /api/lenses`
(canonicalName + spec-join targetType/protected/grantTable — F4/F5 reuse this roster); `/api/systemmap`
nodes carry `instances[]` (worst-of status, freshest freshness, ×N tags) and undeclared heartbeat groups
render as client chips on a clients shelf (kind `"client"`, no skeleton edges, click → their page).
`/api/health` needed no plural fix — `computeHealth` was already per-key; only `computeSystemMap` had the
LWW overwrite. Drill routes carry `nav`/`crumbHref` route-table fields (component pages highlight the Map
tab). **Deferred, still owed:** hood neighbor chips don't dim tombstones — `/api/vertex` link rows carry
no far-end `isDeleted` (add the field when a fire next touches that handler); `keyTarget` gains its
component-id row when feed rows need it (F6). **F4 shipped state:** `cmd/loupe/renderedstate.go` holds
`lensRenderedState` (the §4.2 derivation — F5's lens page reuses it via `/api/lenses`) + `computeGates`;
all three health-derived endpoints share one `healthReaders` path. Derivation decisions grounded during
the 3-layer review: **fault requires `errorCount>0` AND a live `lastError`** (the reporter's errorCount
is cumulative while `SetActive` nulls lastError — the conjunct un-latches a recovered lens);
`lastError` surfaces as an issue line in every state; an unattributed/infra pause on a NON-protected
lens renders `paused` (not `unknown`); a paused protected pg lens is `pending-readpath` regardless of
`pauseReason` (the infra-pause probe loop IS the activation gate, `read_path_adapters.go` — a later PG
outage parks in the same posture by design, auto-resumes, write errors escalate to fault). Alert
severity + the bootstrap marker fold into `/api/systemmap`'s overall so the topbar pill and map banner
never disagree on one screen (§2.1's "rides the map's /api/health poll" premise was wrong — the map
polls /api/systemmap; the shell polls /api/health at 30s on ALL views instead). Gates read
`timestamp` OR `completedAt` (gate4/5 stamp the latter); join is sorted/first-wins. An undeclared
client reporter going stale DOES degrade the rollup (adjudicated with F3; only pending-readpath lenses
are exempt).

**F5 shipped state:** `cmd/loupe/lens.go` — `GET /api/lens/<id>` (definition/reporter/overlay/package
join; 404 only when neither meta-vertex nor reporter exists; tombstoned meta surfaces `isDeleted`) +
`GET /api/lens/<id>/rows` (nats_kv browse, limit clamped ≤1000; postgres answers `{pgPending:true}` —
**F9 binds to that shape**, not §11's `unavailable` string; blank/unknown targetType errors instead of
masquerading as the F9 wait state). Owner resolution matches `vtx.meta.<id>` membership in package
manifest `declaredKeys` (collision-proof vs §6.1's canonicalName wording — spec touch-up, not a bug).
The heartbeat overlay merges lensLags/lensLatency per metric from the freshest instance reporting each.
FE: `logic/lens.js` (enablement table + delete-confirm + latency format, goja-tested) + `views/lens.js`
(control replies survive the post-mutation re-render via reply-box re-parenting; the delete modal is
route-lifecycle-bound with ESC + focus trap + in-flight guard; page DOM clears before fetch). `toast`
moved to `api.js` as the shared cross-view notice. Enablement rules now live in two places — rosterRow
(component.js) and `logic/lens.js` — consolidate when a fire next touches the roster.

**F6 shipped state:** `cmd/loupe/events.go` — `GET /api/events/stream` (per-client ephemeral ordered
consumer, deliver-new, 4-client CAS bound, per-write rolling deadline at 3× the 15s heartbeat,
`ConsumeErrHandler` → terminal `streamError` so a dead consumer never renders as a deaf green tail);
`/api/systemmap` lens nodes carry `activeSequence`. FE: `logic/feed.js` (pure ring/shape/derive/rate,
goja-tested), `pulse.js` (ONE console-wide EventSource opened at boot — F7's follow-through subscribes
to this same module), the map-rail `#pulse-feed` (after the reserved `#sysmap-console` slot),
`.sysmap-edge.pulse` CSS flow animation. **Adjudicated deviations:** liveness is hello-event-gated
(never the bare 200 handshake — a refused client must not blink green); the server's SSE error event is
named `streamError` (an SSE event literally named `error` is indistinguishable from EventSource's
transport-error event); the topbar LED adds a red state (server-reported terminal error) to §8.4's
green/yellow/dim and its `--text-dim` off-map-idle state is unreachable (boot-opened stream — strictly
more visibility); §8.2 derived rows are transitions + rule updates, not "re-projected" (see §8.2's
correction note). Terminal stream failures manual-reconnect (streamError 15s / fatal non-SSE response
8s — EventSource retries neither natively). Feed rebuilds are rAF-coalesced (also defers hidden-tab
work) and preserve scroll; the systemmap derive base survives an error poll (`lastNodes`, not the
nulled `data`). F7 hookup: subscribe via `pulse.subscribe`, filter rows by `opKey`.

**F8 shipped state:** `cmd/loupe/pkg.go` — `GET /api/package?key=` (pure `computePackage` classifies the
manifest aspect's `declaredKeys` into entities/aspects/operations/lenses/orchestration/roles/permissions/
grants; aspects fold into their parent's count; unresolved keys stay visible; the page's own package
vertex — which the install batch declares — is filtered; a transport read failure 502s rather than
rendering "not found" rows, since that state feeds the uninstall confirm) +
`POST /api/packages/{install,upgrade,uninstall}` wrapping `pkgmgr.Apply`/`Uninstall` with the compiled
package registry (mirror of `cmd/lattice-pkg`'s, drift-pinned by a test that scans
`packages/*/manifest.yaml`), `MaxBytesReader` + a reject-don't-truncate manifest cap, and
`crossOriginBlocked` (Origin-vs-Host gate on the mutating endpoints — the console-wide rollout to
`/api/op`//`api/control`//`api/objects` is a filed XS maintenance row). **Corrected premises:** §9.3's
"DDL YAMLs + archives" and §15 Q5's archive-only answer both fell to the same grounding — packages are
compiled-in Go Definitions, so the upload IS `manifest.yaml` (multi-select multipart picks it by name;
archives add nothing). **Adjudicated deviations:** the mandatory dry-run Preview (Apply stays disarmed
until one succeeds; file/force changes disarm it again) is the §9.3 confirm — it shows the exact
create/update/tombstone delta linkified, which the post-apply reply cannot (pkgmgr populates key lists
only on dry-run; the applied reply shows counts); the upgrade modal has no force checkbox
(`RequireInstalled` makes same-version diff-apply unconditional); the uninstall confirm states the real
tombstone scope (declared − unresolved + manifest + vertex) and the success reply renders the tombstoned
keys linkified in-modal. FE: `logic/pkg.js` (manifest pick / summary lines, goja-tested),
`views/package.js` (detail page + the shared `openApplyModal` the list's Install button reuses; modals
are route-lifecycle-bound in BOTH views — packages.js gained `leave()`), `keyTarget` routes
`vtx.package.*` roots to `#/package/` (lens owned-by chip re-pointed for free; package aspects stay on
Graph; goja-pinned). Lifecycle actions disable on a tombstoned or name-less package.

---

## 15. Open questions — ANSWERED (Winston adjudication, 2026-07-02; grounded live where marked)

1. **`pauseReason` ground truth — RESOLVED (live check).** A verified protected lens reports
   `status:"active", pauseReason:null` (observed via the control plane, 2026-07-02); the fail-closed
   pending state is `status:"paused"` before verify. The `◆ protected` tag derives from the spec join
   alone; `pending-readpath` applies only while paused. §4 rewritten accordingly.
2. **Rebuild semantics — RESOLVED (code check).** `rebuildRule` is the same async Rebuilder path for every
   lens (fire-and-forget ack; errors log server-side); the protected table's DDL/verify is out-of-band and
   untouched. §6.3 carries the final copy: note + confirm on `◆ protected`, disabled while
   `pending-readpath`.
3. **Test-strategy doc amendment — HELD WITH THE 📐.** The strip-export ESM convention (§2.3) supersedes
   the `module.exports` shim; fold the one-paragraph amendment into `loupe-fe-test-strategy-design.md`
   when Andrew ratifies it (flagged to Andrew 2026-07-02). F1 does not block on it (§14 note).
4. **SSE replay — ADJUDICATED: no replay.** Deliver-new only; "what happened while I was away" is the
   orchestration-history platform design's job (lattice lane, 📐). The feed header states this.
5. **Package upload shape — ADJUDICATED: archive-only is an acceptable v1**; accept multi-file too only
   if the multipart handling stays trivial. The confirm dialog + F-004 caveat copy are the load-bearing
   parts, not the transport.
6. **F9 read-only guarantee — ADJUDICATED: approved in principle.** Enforcement = the DSN's Postgres role
   (SELECT-only), never code discipline; bounded rows + statement timeout. Role *provisioning* is a
   platform concern — when F9 starts, the role/DDL bit files to the lattice lane per the cross-lane rules
   if it needs deploy/bootstrap changes; Loupe ships only the connector + env wiring.
