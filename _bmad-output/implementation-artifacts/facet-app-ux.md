# facet-app — UX design (Fire 2: dev host + renderer v0)

> **Status:** ✅ **SHIPPED** (`f5b3031`, 2026-07-13). `cmd/facet` built per this spec and live-verified against
> the seeded demo stack: Home → Service Detail → descriptor form → submit → Outbox confirmed → instance
> appears in both Activity and Service Detail. Not live-clicked this fire: the offline/reconnect banner and
> task-completion auto-clear (no task was seeded) — same proven SSE/agent-retry code paths `cmd/edge` already
> exercises, not a gap in what shipped. Next: Fire 3 (real auth turn-on) — subscribe-ACL Fires 1-3 +
> multi-credential Fire 2 both shipped 2026-07-11/12 (audited 2026-07-16: no longer gated).
>
> **Platform gaps this fire also closed** (surfaced by live verification, not anticipated by this spec):
> `packages/edge-manifest` never shipped its D1 read-grant producer (added `edgeManifestReadGrants`, an
> actorAggregate nats-kv lens — every non-self-anchored manifest row was silently dropped without it);
> `internal/edge/agent.GatewaySubmitter` never forwarded `env.AuthContext` to the Gateway; `cmd/edge`'s NATS
> connection `Name` used a composite string that breaks natsauth's durable-consumer permission match (bare
> device id required) — `cmd/facet` and `cmd/edge` both fixed. See the commit message for full detail.

---

## 1. What this is

Facet's **Fire 2** is Transport Stage 0 from the design (§5): "dev showcase (buildable now)" — a Go host
(`cmd/facet`) embeds `internal/edge` in the same **trusted posture** `cmd/edge` already runs today
(env-configured `EDGE_IDENTITY_ID` / `EDGE_TOKEN`, no real login), and serves a **PWA renderer** that
draws its entire UI from that one identity's `manifest.*` rows. There is no OIDC, no `whoami`, no claim
UX here — that is explicitly **Fire 3**, which rides on subscribe-ACL + multi-credential Fire 2 (a
different Fire 2 belonging to the *multi-credential-identity-linking* design; do not confuse the two
"Fire 2"s across designs) — both now shipped, so Fire 3 is unblocked.

So structurally Facet v0 is single-identity-per-process like `loupe` / `loftspace-app` / `clinic-app` — but
unlike those three (which are CRUD-over-a-lens apps with hand-authored forms), Facet's entire point is that
**the UI has no per-op knowledge at all**: every screen, list, and form is generated at runtime from
`manifest.*` rows and the descriptor vocabulary (design §3.3). This is the first descriptor-driven renderer
in the codebase — there is no vanilla-JS precedent to copy for the rendering engine itself, only for the
visual chrome (`style.css` dark theme: cards, tabs, badges, modal, toast — inherited verbatim from
`cmd/loupe/web/style.css`).

New binary `cmd/facet` on **`127.0.0.1:7810`** (Loupe `:7777`, loftspace-app `:7788`, clinic-app `:7799`,
cafe-app `:7801`, wellness-app `:7802` — next free port in the sequence).

## 2. Grounding — the platform surface (do not reinvent)

Everything Facet reads is one of the five `packages/edge-manifest` Personal Lenses, already shipped and
documented at `docs/components/edge-manifest.md` and design §3.2:

- **`manifest.me`** (`edgeIdentity`) — `{identityKey, displayName, credential:{claimed}, roles:[{key,name}],
  anchors:[{key,type,relation,label,container}], vocab:1}`.
- **`manifest.svc.<tplId>`** (`edgeServices`) — `{serviceKey, name, description, icon, category,
  provider:{key,name}, resolvedVia:[{key,label}], operations:[{operationType, opMetaKey}], vocab:1}`.
- **`manifest.op.<opMetaId>`** (`edgeCatalog`) — `{opMetaKey, operationType, presentation:{title,
  shortLabel?, description?, icon, tone, submitLabel?, group?}, inputSchema, fieldDescriptions?,
  dispatch:{class, authContext, targetField?, contextParams?, reads?}, sensitive, vocab:1}`.
- **`manifest.task.<taskId>`** (`edgeTasks`) — `{taskKey, assignee, queuedRole, forOperation,
  operationType, title, scopedTo, scopedToLabel, expiresAt, vocab:1}`.
- **`manifest.inst.<instId>`** (`edgeInstances`) — `{instanceKey, template:{key,name,icon}, status,
  outcome, createdAt, vocab:1}`.

Facet's Go host is a **thin serving layer** over the already-embedded engine — it does not add any new
read or write mechanism:

- **Reads:** `internal/edge/store.Store.ScanPrefix("manifest.")` for the snapshot/list case (already
  shipped, verified against the source) + `internal/edge/overlay.Overlay.Read(key)` for a single key
  overlaid with any pending optimistic value (design R3). No new engine API needed — both primitives exist.
- **Change notification:** `internal/edge/sync.Config.OnChange func(key string, deleted bool)` (design
  G3, shipped Fire 0) fires on every applied delta — this is what pushes live UI updates.
- **Writes:** `internal/edge/agent.Agent` — the same optimistic-overlay + durable intent queue + Gateway
  submitter `cmd/edge` already drives. Facet's write path is "build an envelope from a descriptor, call
  `agent.Enqueue`" — no new write mechanism (design §4.3).
- **Bootstrap:** `internal/edge/sync.Manager` — the same `personal.register` (Interest Set) +
  `personal.hydrate` sequence `cmd/edge` runs, minus the parts of design §4.1 that are Fire-3 scope
  (OIDC login, `GET /v1/actor` whoami). At Stage 0 the identity is **given**, not authenticated.

**One small gap noted, not filed:** `sync.go`'s `handle()` switch (line ~312) currently only **logs** on
`case "hydrationComplete"` — it does not invoke `OnChange` or any other hook, so nothing today tells a host
process "the initial catch-up is done, stop showing a loading state." This is a one-line mirror of the
existing `OnChange` pattern in the same file (`internal/edge/sync/sync.go`) — a new `Config.OnHydrationComplete
func()` field, invoked from that switch case exactly like `OnChange` is invoked from the two cases above it.
**Small, same-file, mirrors an established pattern — the FE Engineer adds it inline at the start of the
`cmd/facet` build** (per the steward's "wear the other hat" rule), not a filed lattice.md blocker.

The FE inherits **`cmd/loupe/web/style.css`'s dark theme verbatim** (cards, tabs, badges, modal, toast,
stepper, the `api()`/`toast()`/`$()` vanilla-JS helper shape) for visual consistency across the fleet's
apps — but the *rendering logic* is new: nothing in the existing four apps generates a form from a JSON
Schema at runtime.

## 3. The experience

### 3.0 Bootstrap — loading states (Stage 0, no OIDC)

The process starts with `EDGE_IDENTITY_ID` / `EDGE_DEVICE_ID` / `EDGE_TOKEN` already configured (env, like
`cmd/edge`) — there is no login screen in Fire 2. The browser tab hits `GET /` and the renderer walks three
states before showing real content:

1. **Connecting** — full-screen centered brand mark ("Facet") + a spinner + "Connecting…". Shown while the
   Go host's NATS connection + `personal.register`/`personal.hydrate` call are in flight (host hasn't yet
   opened the browser-facing feed).
2. **Hydrating** — same layout, label becomes "Loading your world…" with a thin indeterminate progress bar.
   Shown from feed-connect until the `hydrationComplete` signal arrives (see the gap noted in §2 — until
   the FE Engineer wires `OnHydrationComplete`, a **timeout fallback** of 3s-of-silence-on-the-feed treated
   as "probably done" is an acceptable Fire-2 stopgap, called out explicitly in code as a stopgap so it
   isn't mistaken for the real signal).
3. **Ready** — the chrome (top bar + bottom nav) mounts and **Home** renders. If `manifest.me` shows
   `claimed:false` (design §4.1's "claim beat") — **out of scope for Fire 2** (claim UX is Fire 3, needs
   real `whoami`/`ClaimIdentity`); Fire 2's seed identity is always pre-claimed so this state is simply
   never exercised yet. Note it in the empty-state copy anyway ("claim your identity — coming soon") so
   Fire 3 has a landing spot, but do not wire the flow.

Connection loss mid-session (host's NATS drops): a slim top banner "Reconnecting…" — the local mirror
keeps serving already-hydrated `manifest.*` rows (offline-first, design §4.4); writes queue in the Outbox
(§3.4) rather than failing.

### 3.1 Chrome — top bar + bottom nav

**Top bar:** brand "Facet" (left) · the claimed identity's `displayName` from `manifest.me` (right,
truncated) · a small **Outbox** icon-button showing a badge count of non-`confirmed` outbox entries
(right-most). **Bottom nav** (mobile-first, 5 icons, PWA tab-bar convention): **Home** · **Services**
(grid of all `manifest.svc.*` — Home shows only a strip, this is the full list) · **Tasks** (badge = open
task count) · **Activity** · **Me**. This is a slight de-scope from design §4.2's four bare archetypes
(Home/Service/Task/Activity/Me) — Home's "services grid" from the design becomes a **strip + a dedicated
Services tab** once the grid needs to hold more than ~4 templates; both read the identical `manifest.svc.*`
rows, it's a layout split not a new data need.

### 3.2 Home

Three stacked sections, each empty-state-aware (design says a service wired mid-session "slides in" —
empty states must read as "nothing yet," never as an error):

1. **My places** — one chip per `manifest.me.anchors[]` entry (`label`, e.g. "Unit 4B — Maple Court").
   Non-interactive in Fire 2 (no drill-down view of a place — a named Fire-5 non-goal, §8).
2. **Services strip** — up to 4 `manifest.svc.*` cards (icon + `presentation`-free name/description from
   the row itself — recall `edgeServices` rows already carry name/description/icon directly, no separate
   presentation lookup needed for services). Each card taps through to **Service detail** (§3.3). A
   **"See all →"** link when more than 4 exist, routing to the Services tab.
3. **Tasks strip** — up to 3 open `manifest.task.*` rows (title + `scopedToLabel`), each tapping through to
   **Task detail** (§3.5, the same descriptor-form renderer Service detail uses). "See all →" to Tasks tab
   when more than 3.

### 3.3 Services tab / Service detail

**Services tab** = a full grid of every `manifest.svc.*` row, grouped by `category` (a simple heading per
distinct category value, "home" / "wellness" / etc., insertion order of first appearance — no fixed
taxonomy, unknown categories just get their own heading).

**Service detail** (tap a card): header (icon, name, description, provider name, `resolvedVia` label as a
small "via <label>" caption) then:
- **Operations** — one button per `row.operations[]` entry, `submitLabel` (falling back to `title`,
  falling back to a prettified `operationType` per design §3.3's degraded-render rule) from the matching
  `manifest.op.<opMetaKey>` row's `presentation`. Tapping opens the **descriptor form** (§3.6) as a modal.
- **My instances of this service** — every `manifest.inst.*` row whose `template.key` matches this
  service, newest first, rendered as the compact instance-row from §3.4's Activity list (reused component,
  not a second implementation).

### 3.4 Activity

A single reverse-chronological feed merging two sources, visually distinguished but one scroll:

- **Outbox entries** (§3.4a below) — always sorted to the **top** while non-terminal (`queued` /
  `submitting`), then fall into chronological position once `confirmed` (their `createdAt` becomes the
  matching instance's, so a confirmed order doesn't duplicate — see reconciliation note below) or stay
  pinned with a red state if `rejected`.
- **Instance timeline** — every `manifest.inst.*` row, newest `createdAt` first, rendered as: icon + name +
  a status pill (`open` / whatever `outcome` names, generic — Fire 2 does not special-case outcome values)
  + relative timestamp.

**Reconciliation (avoid double-counting an order):** an Outbox entry and its eventual `manifest.inst.*` row
are **the same real-world thing** viewed at two lifecycle stages. Key them by the outbox entry's client-
generated `requestId` vs. the instance's `instanceKey` — Fire 2 does **not** attempt to link these
(no shared id exists yet in the manifest schema); the simple, honest v1 behavior is: while an outbox entry
is non-terminal it shows in the Outbox section pinned at top; once it flips to `confirmed`, it is removed
from the visible Outbox list (still logged, collapsed under "Outbox history" if the user expands it) and
the *separate* instance row that shows up over the sync delta is what remains. There will be a brief window
where both are visible (the outbox entry hasn't been dropped from the pinned section yet when the instance
delta lands) — acceptable and not worth suppressing with heuristic matching in v0.

#### 3.4a Outbox states (design §4.4, R3)

Every enqueued intent renders as a card: descriptor `presentation.title` (or prettified `operationType`) +
a **state chip**:

| State | Chip | Notes |
|---|---|---|
| `queued` | gray "Queued" | not yet sent (offline, or waiting its turn behind an in-flight submit) |
| `submitting` | amber pulsing "Sending…" | in flight to the Gateway |
| `confirmed` | green "Done" then fades/collapses within ~2s | the authoritative delta landed |
| `rejected` | red "Failed" + a **Retry** button | `RevisionConflict` or any other submit error — see §3.4b |

Any row still `queued`/`submitting` when the mirror shows the write's *own optimistic effect* (an overlay
`Pending` value, design R3) is additionally reflected at the **point of that effect** — e.g. a just-ordered
service instance appearing in "My instances" (§3.3) shows a small **"Pending"** chip on that specific card
(not just in the Outbox) until the confirmed delta replaces the optimistic one. Two presentations of one
fact (Outbox card + Pending chip on the affected row) — both read from the same overlay `Pending` flag, no
separate state to keep in sync.

#### 3.4b Conflict presentation (design §4.4)

On `RevisionConflict`: the outbox card becomes `rejected` (red "Failed — the world moved") with two
actions: **Dismiss** (drop it, no further action) and **Review** (opens the same descriptor form
pre-filled with the user's original input, showing a one-line banner "This changed since you started —
review and resubmit if it still applies" above the form; the engine has already re-hydrated the true state
by this point per §4.4, so any read-only context shown in the form, e.g. a service's current
availability, reflects the fresh truth). **No auto-retry, no merge** — explicitly per design §4.4 v1 scope.

### 3.5 Tasks tab / Task detail

**Tasks tab** = every `manifest.task.*` row not past `expiresAt`, sorted soonest-`expiresAt`-first, each
showing `title` + `scopedToLabel` + a relative "due" chip. Tapping opens the same **descriptor form**
modal (§3.6) the Service detail uses, driven by `manifest.op.<forOperation>`'s `dispatch` — except
`dispatch.authContext` is `"task"`, so the envelope's `authContext` becomes `{task: taskKey, target:
scopedTo}` per design §4.3, and the app relies on the **platform's own auto-complete** (the task
disappears from this list when the matching sync delta marks it done/deleted — no client-side "mark task
complete" call is ever made, per design §4.3's explicit "no client-side CompleteTask workaround").

### 3.6 The descriptor form (the core novel mechanism)

Given an `manifest.op.<opMetaKey>` row, the form renderer:

1. **Header:** `presentation.title`, `presentation.description` (if present) as helper text below it.
2. **Fields:** iterate `inputSchema.properties` in **declaration order** (JS object key order — schemas are
   authored, not sorted). For each field, look up `fieldDescriptions[fieldName]` for help text and render
   by JSON-Schema-shape → widget, per this fixed mapping (Fire 2's complete widget vocabulary, design
   §4.2):

   | Schema shape | Widget | Notes |
   |---|---|---|
   | `{"type":"string"}` no `enum`, no `format`, `maxLength` ≤ 120 (or absent) | **text** `<input type=text>` | `maxLength` set as the HTML attr when present |
   | `{"type":"string"}`, `maxLength` > 120 | **textarea** | rows=4 default |
   | `{"type":"string","enum":[...]}` | **enum** | ≤4 options → segmented button group; >4 → `<select>`. Option labels = the enum values title-cased unless a sibling `enumLabels` map is present (not in Fire-1's shipped schema — support the field defensively, degrade to the raw value) |
   | `{"type":"integer"\|"number"}` | **integer/number** | `<input type=number>`, `min`/`max` from `minimum`/`maximum`, `step=1` for integer |
   | `{"type":"integer\|number", "x-format":"money"}` or field name matches `/Cents$/` | **money** | display as dollars (divide/multiply by 100 at the boundary), `<input type=number step=0.01>` — Fire 2 has no shipped op using this yet; implement the mapping, it's cheap, and it's named in the design's widget list |
   | `{"type":"string","format":"date"}` | **date** | `<input type=date>` |
   | `{"type":"string","format":"date-time"}` | **datetime** | `<input type=datetime-local>` |
   | `{"type":"boolean"}` | **toggle** | a switch control, not a checkbox (matches the dark-theme chrome) |
   | `{"type":"string","x-entityRef":"<vertexType>"}` | **entity-ref** | a searchable picker over local mirror rows of that type (Fire 2: filter `manifest.*` rows client-side by a naive substring match on their label field — there is no shipped entity-ref usage in Fire 1's `RequestService` schema either; implement the widget generically so it's ready, and note in code that "search" here is a client-side filter over already-hydrated rows, not a server query) |
   | any field marked `sensitive` in the op row (design §3.4) | **sensitive-masked** | `<input type=password>`-style masking regardless of underlying type, and the value is **never** echoed back into any local-persisted state (kept only in the in-flight submit) |
   | unrecognized `type`/shape | falls back to **text**, per design §3.3's "unknown widget kind → text input" evolution rule | |

   `required` array fields get a client-side non-empty check before Submit enables (courtesy only — the
   Starlark script remains the enforcer, per design §4.2 — a rejected submit still round-trips normally and
   surfaces the platform's error).

3. **Auto-filled fields:** any key present in `dispatch.contextParams` is **not rendered** — its value is
   computed from the template (`{actor}` → the local identity's resolved key, `{scopedTo}` /
   `{service}` / `{payload.<field>}` per design §4.3) and injected into the payload at submit time.
4. **Submit button:** label = `presentation.submitLabel` (fallback "Submit"), tone = `presentation.tone`
   (`primary` = accent color, `destructive` = red, `neutral` = default button style — the same tone tokens
   `style.css` already defines for the other apps' action buttons).
5. **On submit:** build the Contract #2 envelope exactly per design §4.3 (`operationType`/`class` from
   `dispatch`, payload = form fields + `contextParams` substitutions, `reads`/`optionalReads` from
   `dispatch.reads` templates, `authContext` from `dispatch.authContext`), hand it to
   `internal/edge/agent.Agent.Enqueue` (or the equivalent host-side call), close the modal, and the new
   Outbox card appears immediately (§3.4a `queued`/`submitting`).

**Degraded render (design §3.3):** if an op appears in `manifest.svc.*.operations[]` or a task's
`forOperation` but has **no matching `manifest.op.*` row** (a package that never adopted the vocabulary),
render a disabled card: title = prettified `operationType`, body = "This isn't completable here yet — ask
staff to help via the admin console," no form, no submit button. Facet never crashes or blocks on an
undescribed op; it degrades gracefully per the design's explicit contract.

### 3.7 Me

Read-only in Fire 2 (claim/link entry is Fire 3, design §4.2's "named FE consumer" for that later fire):
`manifest.me.displayName`, the `roles[]` list (name chips), and the `anchors[]` list (same chips as Home's
"My places," here with the `relation`/`type`/`container` shown as secondary text for completeness). A
placeholder card "Manage sign-in methods — coming in a future release" sits at the bottom, deliberately
inert, so Fire 3 has a visual landing spot without wiring any behavior now.

## 4. `cmd/facet` binary shape

- **HTTP surface** (Go host, mirrors the `server.go` + `embed` pattern of `cmd/loupe`/`cmd/*-app`):
  - `GET /` and static PWA assets (`index.html`, `app.js`, `style.css`, a minimal `manifest.webmanifest` +
    icon for installability — PWA-first per design FORK-4, but Fire 2 does not need a service worker /
    offline-cache-of-the-shell; the *data* offline story is the engine's local mirror, not an HTTP cache).
  - `GET /api/feed` (**SSE**, not WebSocket, for Fire 2 — one-directional server→browser push is all this
    stage needs, and SSE needs no extra library on either side: the browser's native `EventSource`, and Go's
    `http.Flusher`). Registers `sync.Config.OnChange` (and `OnHydrationComplete` once added, §2) to write an
    `event: change\ndata: {"key":"...","deleted":false}\n\n` frame per delta to every connected SSE client.
    On (re)connect, the handler first replays a **snapshot**: every currently-held `manifest.*` row as a
    burst of synthetic `change` frames, so a browser refresh doesn't need a separate "initial load" code
    path — the SSE stream *is* the only data path, from empty state to live.
  - `POST /api/enqueue` — browser submits `{operationType, class, payload, reads, optionalReads,
    authContext}` (the envelope §3.6 step 5 built client-side); the Go host hands it to
    `internal/edge/agent.Agent.Enqueue` and returns `{requestId}` immediately (the browser does not block
    on the actual Gateway round-trip — that happens on the host's existing drain loop, same as `cmd/edge`
    today, and its outcome arrives back over `/api/feed` as an overlay-state change).
  - No other endpoints. No Core-KV, no direct NATS from the browser (that is Stage 2 / Fire 4, design §5)
    — the browser only ever talks to `cmd/facet`'s own localhost HTTP surface.
- **Reused from `cmd/edge/main.go` near-verbatim:** the `store.Open` / `substrate.Connect` /
  `sync.New` / `overlay.New` / `agent.New` / `runAgentLoop` wiring, same env vars
  (`EDGE_STORE_PATH`, `NATS_URL`, `EDGE_GATEWAY_URL`, `EDGE_IDENTITY_ID`, `EDGE_DEVICE_ID`, `EDGE_TOKEN`).
  Facet adds an HTTP listener (`FACET_HTTP_ADDR`, default `127.0.0.1:7810`) alongside the existing
  `mgr.Run(ctx)` blocking call — both run under the same `ctx`/signal handling.
- **Not reused / explicitly new:** the SSE handler, the `/api/enqueue` handler, and the PWA static assets.

## 5. The Fire 2 "green" acceptance scenario (design §7, restated as a walkthrough)

Against the seeded stack (`make seed-edge-demo` from Fire 1, a laundry service template `availableAt` a
building, a pre-claimed tenant identity `residesIn` a unit in it):

1. Start `cmd/facet` with that tenant's `EDGE_IDENTITY_ID`/`EDGE_TOKEN`; open the browser to
   `127.0.0.1:7810`. See **Connecting** → **Hydrating** → **Home**, with "Maple Laundry" in the services
   strip and the unit chip under "My places."
2. Tap the laundry service card → Service detail → tap "Place order" → the descriptor form renders
   `pickupWindow` (segmented: morning/afternoon/evening), `bags` (number, 1–6), `notes` (textarea) exactly
   per `edgeCatalog`'s shipped `RequestService` schema (design §3.2's worked example). Fill it, submit.
3. Modal closes; an Outbox card appears "Order laundry pickup — Sending…" then "Done" (fades); the new
   instance appears in "My instances of this service" with a brief **Pending** chip, then loses it once the
   confirmed delta lands (watch the SSE frames if verifying manually — the "wire panel" the design's
   throwaway demo showed is not required in the real app, but the same frames are what's flowing).
4. Kill the local NATS server mid-session (`docker stop lattice-nats` or equivalent), place a second order
   — the modal still lets you fill and submit; the resulting Outbox card sits at `queued` (offline banner
   shown in the top bar). Restart NATS. Within a few seconds (the agent's fixed drain interval) the queued
   entry advances `submitting` → `confirmed` and the instance shows up, with **no user action required**
   ("reconnect drains" — design §7).
5. Separately: a task seeded `assignedTo` this identity (e.g. a `SignLease`-shaped demo task, or any op
   with a descriptor) shows in the Tasks tab; completing its descriptor form causes the row to **vanish**
   from the list once the platform's auto-complete lands the task's terminal state over the sync delta — no
   explicit "complete task" call was ever made by the client.

Each numbered step above is an independent thing an in-browser reviewer can check off; §7's "green" line in
the design doc is exactly steps 1–3 plus step 4 (offline queue+drain) plus step 5 (task auto-complete).

## 6. Out of scope (Fire 2 guardrails)

- **No OIDC login, no `whoami`, no claim/link UX** — Fire 3 (rode on subscribe-ACL Fires 1–3 +
  multi-credential Fire 2, both shipped 2026-07-11/12 — no longer gated). The Me screen's "claim your identity" and
  "manage sign-in methods" affordances are placeholders only.
- **No browser-native NATS / WebSocket** — Fire 4. The browser only speaks HTTP/SSE to `cmd/facet`.
- **No vertical-specific screens beyond generic service instances** (a lease detail view, an appointment
  detail view) — named Fire-5 non-goal (design §8); "my instances" stays generic template/instance/outcome.
- **No push/background wake** — deferred (G13), not this app's job in Fire 2 (foreground-only; the SSE
  connection only delivers while the tab is open).
- **No offline shell caching / service-worker install** beyond a minimal manifest for "Add to Home
  Screen" — a full PWA offline-app-shell story is not required for the design's Fire-2 green scenario
  (which assumes the tab stays open through a *backend* NATS outage, not a *browser* going fully offline).
- **No entity-ref server-side search, no money-typed op yet exists** — both widgets are implemented per
  §3.6's mapping table (cheap, and future ops will need them immediately) but cannot be exercised
  end-to-end against a real op until one ships; note this plainly in the PR/commit, don't claim it
  "verified" beyond the widget rendering correctly against a hand-built test schema.

## 7. Local-stack wiring

A `make up-facet` target mirroring `make up-loftspace` / `make up-clinic`: `up-full` → ensure
`edge-manifest` + `service-location` are in the install chain (Fire 1 already put them there) →
`make seed-edge-demo` (idempotent) → build + background-start `facet` on `:7810` with a seeded tenant's
`EDGE_IDENTITY_ID`/`EDGE_TOKEN` (the seed script should print or fix these deterministically so
`make up-facet` can wire them without a manual copy-paste step — a small addition to `seed-edge-demo`'s
output if it doesn't already emit them). `make down` also reaps `facet`. `make run-facet` runs the binary
alone against an already-up + already-seeded stack (mirrors `make run-clinic-app`).

## 8. Review plan

Unlike `clinic-app-ux.md`'s "lead review" call (a same-shape mirror of an already-reviewed lens), Facet's
descriptor-form renderer (§3.6) is a **genuinely new mechanism** with no existing pattern in this codebase
to mirror — it is the first place client code interprets a JSON Schema + a dispatch descriptor into both a
rendered form *and* a Contract #2 envelope. Treat as **L (multi-fire) / full 3-layer review** for the
renderer + write-path pieces specifically (§3.6, §3.4a/b, §4's `/api/enqueue` envelope construction),
because a bug there is a **security-adjacent** correctness bug (a malformed `authContext`/`dispatch`
substitution could mis-target an operation) even though the descriptor data itself is untrusted-but-honest
(the manifest only affects *visibility*, never *permission*, per design §4.5 — so the blast radius of a
renderer bug is "wrong form/wrong optimistic UI," never a privilege escalation, but it's still worth the
fuller pass given novelty). The chrome/layout pieces (§3.1–3.3, §3.5, §3.7 — reading rows and laying out
cards) are ordinary FE work, lead-review-sufficient, same tier as the other vertical apps.

**Gate set:** `go build ./...`, `make vet`, `golangci-lint run ./...`,
`STRICT=1 go run ./scripts/lint-conventions.go` (P5 — `cmd/facet` must show **zero** `core-kv` references;
it reads only the local Edge mirror, never even a NATS-KV lens bucket directly, which is *stricter* than
P5's floor, not a gap against it), `go test ./internal/edge/... ./cmd/facet/...`, and in-browser
verification of the §5 walkthrough against `make up-facet` (including the mid-session NATS-kill step —
this is the one step that can't be curl'd, it needs a real tab open to watch the Outbox/Pending states
transition).

## 9. Follow-ups flagged for Winston before the FE Engineer builds

Both resolved in the Fire 2 build (`f5b3031`): `OnHydrationComplete` added to `internal/edge/sync/sync.go`;
port `:7810` confirmed free and wired into `make up-facet`.
