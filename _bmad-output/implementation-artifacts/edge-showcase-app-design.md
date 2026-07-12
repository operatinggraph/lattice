# Edge showcase app ("Facet") — the discovery-driven personal client — design

**Status: ✅ RATIFIED (Andrew, 2026-07-11) — "Approved, add to the backlog."** Blanket approval; the four forks stand at their RECOMMENDED options (FORK-1 B, FORK-2 A, FORK-3 A, FORK-4 A), rewritten as DECIDED below with the roads not taken — flag any single fork to override. No frozen-contract change. **Build runs through the fleet, not this session:** platform fires 0/1/4 → Lattice lane, app fires 2/3/5 → Verticals lane. Author: Winston (lead) · Designer fire 2026-07-10, ratified 2026-07-11.
**Backlog rows:** [verticals.md](../planning-artifacts/backlog/verticals.md) → *Edge showcase app (Facet)* (app fires 2/3/5) · [lattice.md](../planning-artifacts/backlog/lattice.md) → *Edge & personal lenses* → *Edge-manifest + personal-lens consumer* (platform fires 0/1/4).
**Consumers:** this app is the named consumer that un-defers **PL.6** (WebSocket/push bridge) and **EDGE.5** (browser/mobile node), and the demand driver for the per-actor write-surface migration.
**Contracts:** build-to #1, #2 (§2.5 read posture), #6 (§6.5 service path, §6.10 availability, §6.14 read grants), #9 (claim), #10 (§10.1 tasks, §10.5, §10.7 auto-complete), #11 (external actor authn). **Frozen-contract change: NONE** (see FORK-1 — the descriptor vocabulary ships as a component spec, not a contract edit).

**Grounds in:** `edge-lattice-full-design.md` (✅ ratified 2026-06-29; EDGE.1+2 CLOSED 2026-07-10) · `personal-secure-lens-design.md` (✅ ratified; PL.1–5 shipped, PL.6 deferred "no Edge consumer yet") · `per-identity-nats-subscribe-acl-design.md` (✅ ratified 2026-07-10, unbuilt) · `multi-credential-identity-linking-design.md` (✅ ratified 2026-07-10, unbuilt — whoami) · `gateway-external-trust-boundary-design.md` + `gateway-claim-flow-identity-provisioning-design.md` (✅ shipped) · `service-location-design.md` rev.3 (✅ shipped; cap.svc lens) · `5-1-ddl-self-description-aspects.md` (shipped) · brainstorming-session-2026-04-08 items **#52 / #54 / #55** (Stream 5, never built — this design realizes them) · vault vision `Edge Lattice/{Edge Lattice,Personal Lens}.md` ("By traversing the local graph, the Processor tells the frontend which UI components to render").

---

## For Andrew (one-look ratification block)

**What it does (two lines).** A personal client (PWA first, iOS later) whose only hardcoded behavior is *authenticate against the deployment IdP and connect*; everything else — which services exist for this identity, which operations they permit, what forms they need, which tasks await — arrives as **data** over the already-shipped Personal Lens, projected by a new `edge-manifest` package and rendered by a fixed, service-agnostic widget vocabulary. A new service wired `availableAt` the user's building appears in the app with **zero app change**.

**The one thing to understand before ratifying.** This design adds *no new planes and relaxes no invariant*: reads are lens projections delivered over the shipped per-identity SYNC stream (P5), writes are ordinary operations through the shipped Gateway door (P2), authorization stays step-3 capability checks — the manifest only makes *visibility* honest, never *permission*. The app is a pure function of its local mirror.

**Frozen-contract change: NONE.**

**FORK-1 — where the descriptor vocabulary lives. DECIDED: B (Andrew, 2026-07-11).** Ship it as `docs/components/edge-manifest.md` (build-to spec, versioned `vocab: 1`); freeze as a contract when the **second renderer** (iOS/SwiftUI, Fire 5) proves client-neutrality — the freeze trigger is named, not open-ended.
- *Road not taken — A:* freeze now as a new Contract #12. Rejected for v1: freezing v0 guesses before a second renderer exists invites amendment churn.

**FORK-2 — the browser engine. DECIDED: A (Andrew, 2026-07-11); confirmable at Fire 4.** Compile `internal/edge` to wasm (store interface → IndexedDB, transport → WebSocket) — EDGE.5's ratified "same engine, new host"; LWW/overlay/queue semantics stay single-sourced; wasm feasibility empirically verified 2026-07-02 (~1.3 MB gz interpreter-only, smaller without Starlark).
- *Road not taken — B:* a protocol-parity TypeScript mini-engine pinned by shared conformance fixtures — kept as the **fallback** if wasm ergonomics/size disappoint when Fire 4 lands. *C (REST-only thin client):* rejected — a second read plane that abandons the offline-first store.

**FORK-3 — manifest transport. DECIDED: A (Andrew, 2026-07-11).** Personal Lens only: the manifest is a set of `nats_subject` personal lenses; the app's world-feed is the SYNC stream + hydration, exactly as EDGE.1/2 consume it.
- *Road not taken — B:* add a Gateway REST snapshot (`GET /v1/manifest`, RLS-backed) as a cold-start/degraded fallback — a compatible later extension, not v1. *C (REST-primary):* rejected (same second-plane smell as FORK-2 C).

**FORK-4 — first target platform. DECIDED: A (Andrew, 2026-07-11).** Browser/PWA first: App Store guideline 2.5.2 does not apply to the PWA route (edge design §6 addendum); NATS WebSocket is native to our pinned server (§5); one link demos anywhere including phones; iOS/SwiftUI follows as the second renderer that triggers the FORK-1 freeze.
- *Road not taken — B:* native iOS first — better device story (push, biometrics) but pays TestFlight friction + the 2.5.2 open item immediately, and delays the two-renderer proof.

---

## 1. The thesis — discover, don't hardcode

Every existing vertical FE hardcodes its world: routes, bucket names, `operationType` strings, per-op form fields, even client-side reconstruction of 6-segment link keys for `reads` declarations (`cmd/loftspace-app/web/app.js` — 16 hardcoded op call sites and a hand-authored `COMPLETIONS` form registry). The codebase itself names the way out, in the comment at `app.js:76`: *"the generic DDL-self-describing form needs an op-catalog read model — a Core-KV op-meta scan would violate P5 in a vertical app."*

Facet inverts the posture. Its **entire hardcoded surface** is:

1. an OIDC client flow (IdP discovery URL, client id — deployment config),
2. two base URLs (Gateway for writes/whoami; sync endpoint for the Personal Lens),
3. the **descriptor vocabulary interpreter** — a fixed set of widgets and screen archetypes that render whatever vocabulary-conformant rows appear in the local mirror.

Everything a user can *see* is a manifest row that arrived over their personal delta stream; everything they can *do* is an operation descriptor that arrived the same way. The identity's graph relationships — `residesIn` a unit contained in a building where a laundry service is `availableAt`, a task `assignedTo` them, a role they hold — are the *only* source of the UI. Same binary, different identity ⇒ different app.

This is the vault's original Edge vision made concrete ("traversing the local graph… tells the frontend which UI components to render"), and it is brainstorm Stream 5 (#52 UI form schema, #54 command discovery, #55 dynamic form renderer SDK) landing on the machinery that has shipped since: the Personal Lens, the Edge engine, the Gateway trust boundary, and DDL self-description.

## 2. What already exists (grounding ledger)

| Piece | State | Facet's use |
|---|---|---|
| Personal Lens PL.1–5: `nats_subject` adapter → `lattice.sync.user.<id>`, SYNC stream, delta envelope `{op,key,anchor,kind,class,revision,projectionSeq,encrypted,data}`, Interest Set, `personal.hydrate`, D1 fail-closed `readableAnchors` gate, Vault ciphertext passthrough | ✅ shipped (`internal/refractor/adapter/natssubject.go`) | The world-feed. **No production lens is installed yet** — Facet's manifest lenses are the first. |
| Edge engine EDGE.1+2: bbolt mirror under Contract-#1 keys, LWW-by-revision, durable consumer, hydrate, optimistic overlay (`Pending` flag), durable intent queue + drain, `overlay.Links` "UI Discovery" | ✅ shipped (`internal/edge/*`, `cmd/edge`) | The app's data layer. Trusted posture today (`EDGE_ACTOR_KEY`); EDGE.3 gated on subscribe-ACL. |
| Gateway external door: `POST /v1/operations` (verify → strip → stamp verified actor), CORS, JWKS/dev-key, first-touch `ProvisionConsumerIdentity`, A→U resolution, revocation kill-switch | ✅ shipped (`internal/gateway/*`) | The only write path. |
| Contract #11 opaque binding + claim flow (Contract #9): `CreateUnclaimedIdentity` (client-minted secret, hash-only server-side), `ClaimIdentity` scope=self | ✅ shipped | Onboarding: fresh IdP login ⇒ bare A; claim ⇒ full U; the manifest re-projects and the world "blooms". |
| `cap.svc` availability join: `residesIn ∘ containedIn*0.. ← availableAt − unavailableAt`, fanned over `permitsOperation` → `serviceAccess[{service, resolvedVia, allowedOperations}]`; step-3 service path (`authContext.service`) | ✅ shipped (`packages/service-location/lenses.go:97`) — **zero topology seeded anywhere; not in any install chain** | The availability semantics Facet re-projects (richer columns, app-facing keys). The laundry example is literally this package's worked example (`ddls.go:98`). |
| DDL self-description: `.description` `.inputSchema` `.outputSchema` `.fieldDescription` `.examples` aspects, install-time mandatory (Story 5.1); Loupe's Submit-Op catalog proves they suffice for form rendering | ✅ shipped — but Core KV (Loupe-only read) and **per-DDL merged schema**, not per-op (`"required":[]` in most verticals; rbac/identity's per-op `oneOf` is the exception) | Source for the catalog lens; per-op schema is a vocabulary upgrade (§3.3). |
| Tasks: `vtx.task` root `{status,expiresAt}` + `forOperation/scopedTo/assignedTo|queuedFor` links; submit-bound-op-with-`authContext:{task,target}` ⇒ platform **auto-completes** in the same batch; `my-tasks` per-identity aggregate | ✅ shipped | The inbox. Facet uses the *designed* path (real identity + ephemeral grant + auto-complete) that today's staff-actor FEs bypass. |
| Per-identity NATS subscribe-ACL (auth callout; confines a connection to `lattice.sync.user.<U>` + its own durable + 3 `personal.*` RPCs) | ✅ ratified 2026-07-10, **unbuilt** (Fires 1–3 queued; Fire 3 flips EDGE.3) | Gate for any untrusted client on the sync plane (Fire 3 here depends on it). |
| whoami `GET /v1/actor` (client cannot compute its own opaque-derived ActorID) | ✅ ratified (multi-credential Fire 2), **unbuilt** | Required for `authContext.target` on scope=self ops and claim UX. |
| Consumer-invocable surface | 3 standing scope=self grants (ClaimIdentity, CreateAppointment, CreateLeaseApplication) + task-ephemeral ops; **no service-path op exists** | Honest v1 surface; Fire 1 adds the first service-path consumer op (`RequestService`). |

## 3. The world manifest (the data contract)

### 3.1 Delivery

The manifest is a set of **personal (`nats_subject`) lenses** authored by a new `packages/edge-manifest` package, delivered over the shipped SYNC plane and applied to the device mirror like any other Personal-Lens rows. Manifest row keys live in a reserved **`manifest.`** key namespace (they are projection-row keys, not Core-KV keys — same as `my-tasks.*` rows). The app renders by prefix: `manifest.me`, `manifest.svc.*`, `manifest.op.*`, `manifest.task.*`, `manifest.inst.*`.

Visibility is doubly guarded: the lens cypher only *derives* rows from the actor's own relationships, and the shipped D1 `readableAnchors` gate filters publication fail-closed. Neither is permission — submit-time step-3 remains the sole authority; the manifest only prevents the app from *offering* what would be denied.

### 3.2 The five manifest lenses (row schemas normative for `vocab: 1`)

All rows carry `"vocab": 1`. Keys shown with cosmetic ids; real ids are NanoIDs (Contract #1).

**`edgeIdentity` → `manifest.me`** — who am I, and what grounds my world:

```json
{"identityKey":"vtx.identity.h7Qk…","displayName":"Riley Chen",
 "credential":{"claimed":true},
 "roles":[{"key":"vtx.role.r1","name":"consumer"}],
 "anchors":[{"key":"vtx.unit.u4B","type":"unit","relation":"residesIn",
             "label":"Unit 4B — Maple Court","container":"vtx.building.mc1"}],
 "vocab":1}
```

`displayName` projects the identity's name aspect when readable (sensitivity rules apply; null for a bare unclaimed A — the renderer shows the "claim your identity" affordance exactly when `claimed:false` ∧ `anchors:[]`).

**`edgeServices` → `manifest.svc.<tplId>`** — one row per service template reachable via the actor's residence chain (the `cap.svc` walk, re-projected app-facing with presentation):

```json
{"serviceKey":"vtx.service.LNDR1","name":"Maple Laundry",
 "description":"Wash-and-fold, 24h turnaround","icon":"laundry","category":"home",
 "provider":{"key":"vtx.identity.pv1","name":"Maple Court Services"},
 "resolvedVia":[{"key":"vtx.building.mc1","label":"Maple Court"}],
 "operations":[{"operationType":"RequestService","opMetaKey":"vtx.meta.opRQ"}],
 "vocab":1}
```

**`edgeCatalog` → `manifest.op.<opMetaId>`** — one row per operation meta reachable by this actor (via `permitsOperation` on their services, standing role grants, or an open task's `forOperation`); the **operation descriptor**, deduplicated across services/tasks:

```json
{"opMetaKey":"vtx.meta.opRQ","operationType":"RequestService",
 "presentation":{"title":"Order laundry pickup","description":"Schedule a wash-and-fold pickup from your unit",
                 "icon":"basket","tone":"primary","submitLabel":"Place order","group":"laundry"},
 "inputSchema":{"type":"object","required":["pickupWindow","bags"],"properties":{
   "pickupWindow":{"type":"string","enum":["morning","afternoon","evening"],"title":"Pickup window"},
   "bags":{"type":"integer","minimum":1,"maximum":6,"title":"Bags"},
   "notes":{"type":"string","title":"Notes","maxLength":280}}},
 "fieldDescriptions":{"pickupWindow":"When we should collect from your door"},
 "dispatch":{"class":"service.laundry.instance","authContext":"service",
             "targetField":"service","contextParams":{"providedTo":"{actor}"},"reads":[]},
 "sensitive":false,"vocab":1}
```

**`edgeTasks` → `manifest.task.<taskId>`** — one row per open task `assignedTo` me or `queuedFor` a role I hold (per-row rather than the `my-tasks` aggregate, for delta-friendliness on the sync plane):

```json
{"taskKey":"vtx.task.t1","assignee":"vtx.identity.h7Qk…","queuedRole":null,
 "forOperation":"vtx.meta.opSGN","operationType":"SignLease",
 "title":"Sign your lease","scopedTo":"vtx.leaseapp.a1",
 "scopedToLabel":"Lease application — Unit 4B","expiresAt":"2026-09-01T00:00:00Z","vocab":1}
```

Renderer obligations inherited from §10.1 and the my-tasks corpus: treat `isDeleted:true` as absence, drop degenerate entries, and visually gate on `expiresAt` (an expired-open task can no longer authorize its op).

**`edgeInstances` → `manifest.inst.<instId>`** — my service instances ("my orders"): every `vtx.service.*` instance `providedTo` me, rendered generically from the template's presentation + the instance `outcome` aspect:

```json
{"instanceKey":"vtx.service.in9","template":{"key":"vtx.service.LNDR1","name":"Maple Laundry","icon":"laundry"},
 "status":"open","outcome":null,"createdAt":"2026-07-10T14:02:11Z","vocab":1}
```

This is deliberately the whole v1 domain-data story: service instances are already a *generic* cross-vertical shape (template/instance/providedTo/outcome), so "my orders" needs no per-vertical view descriptors. Vertical-specific slices (my lease, my appointments) join in Fire 5 as additional personal lenses + a small view-descriptor extension — a named non-goal for v1 (§8).

### 3.3 The descriptor vocabulary (#52 realized)

New **package-authored aspects**, projected by `edgeCatalog`/`edgeServices` (pkgmgr surface additions in Fire 1):

- **On op metas** (`pkgmgr.OpMetaSpec` grows optional fields → aspects):
  - `.presentation` `{title, shortLabel?, description?, icon, tone: primary|neutral|destructive, submitLabel?, group?}`
  - `.inputSchema` — **per-op** JSON Schema (today's per-DDL merged bag with `"required":[]` cannot drive a form; the rbac/identity per-op `oneOf` pattern becomes the projected norm)
  - `.dispatch` `{class, authContext: self|service|task, targetField?, contextParams?, reads?: [templates]}` — the machine-readable version of the loftspace `COMPLETIONS` registry. Template substitutions: `{actor}`, `{scopedTo}`, `{service}`, `{payload.<field>}` — this turns today's client-side hardcoded `reads` reconstruction into data.
- **On service templates**: `.presentation` `{name, description?, icon, category?}` (today a service root is `{}` — it has no name at all).

**Rules.** Icons and tones are semantic tokens from a small fixed set; the client owns all pixels. Descriptors declare *intent, not layout*. Evolution is additive-only: unknown fields are ignored, unknown icon → generic glyph, unknown widget kind → text input, rows with `vocab` above the client's supported version render with a graceful "update to use this" card. **Ops without descriptors still render, degraded** (title = prettified `operationType`, form = schema-less "not completable here" card linking staff to Loupe) — a package that never adopts the vocabulary degrades Facet, never breaks it.

### 3.4 Sensitive data

`vocab: 1` manifest rows are non-PII by construction (titles, schemas, keys, labels). Aspects classed sensitive keep arriving as `encrypted:true` deltas the app cannot read until EDGE.4 (`internal/edge/vault`) ships; forms *collecting* sensitive input (e.g. `RecordIdentityPII`) mark fields `sensitive` — masked entry, no local echo persisted, payload rides the TLS Gateway door like any op (processor-side handling: the shipped sensitive-param-egress mechanism).

## 4. The app

### 4.1 Bootstrap sequence

1. **OIDC login** (code + PKCE against the deployment IdP; Lattice never sees the login UI — Contract #11's boundary). Token refresh is the app's job; there is deliberately no OAuth code in the platform.
2. **whoami** `GET /v1/actor` → `{actorId, resolvedActorId, …}` (hard dependency; ratified, multi-credential Fire 2).
3. **Sync plane connect** — NATS (WebSocket in the browser) with the bearer JWT as the connect token → auth callout confines the connection to `lattice.sync.user.<U>` (+ its durable + `personal.*` RPCs).
4. **`personal.register`** (Interest Set) + **`personal.hydrate`** → deltas fill the mirror → `hydrationComplete` → UI composes from `manifest.*`.
5. Live thereafter: every graph change that survives the D1 gate lands as a delta; the UI recomposes. A service wired `availableAt` mid-session slides in; a revoked `permitsOperation` removes an action.

First-run with a fresh IdP account is the **claim beat**: first touch provisions bare A (`consumer` role, no anchors) → `manifest.me` shows `claimed:false` → the app offers "claim your identity" (QR/claim-link, secret in the URL fragment per Contract #9) → `ClaimIdentity` (scope=self, `authContext.target` = whoami's actorId) → the lens re-projects → the world blooms. That moment *is* the showcase.

### 4.2 Renderer

Screen archetypes (fixed): **Home** (anchors + services grid + tasks strip), **Service** (presentation + its operations + my instances of it), **Task** (bound-op form via the catalog descriptor), **Activity** (outbox + instances timeline), **Me** (identity, roles, credentials, claim/link entry — the multi-credential design's named FE consumer).

Widget vocabulary v1 (from `inputSchema` types + hints): text, textarea, integer/number, money, enum (segmented/select), date/datetime, toggle, entity-ref (picker over mirror rows of a named type), sensitive-masked. Form generation: per-op `inputSchema` drives fields; `fieldDescriptions` drive help text; `dispatch.contextParams` fields are auto-filled and hidden; `required`/bounds/enums validate client-side as courtesy — the Starlark script remains the enforcer.

### 4.3 Write path

Invoking an operation = authoring a Contract #2 envelope **from the descriptor**: `operationType` + `class` from `dispatch`, payload = user fields + `contextParams` substitutions, `reads`/`optionalReads` rendered from `dispatch.reads` templates, `authContext` per `dispatch.authContext` (`self` → `{target: actorId}`; `service` → `{service: serviceKey}`; `task` → `{task, target: scopedTo}`). Enqueue via the edge agent → drain submits through the Gateway (Bearer; the wire struct has no actor field; the Gateway stamps the verified actor). Task ops ride the **designed** path: real identity + ephemeral grant ⇒ the platform auto-completes the task in the same batch — no client-side `CompleteTask` workaround.

### 4.4 Offline & conflict UX (the "UI concern" the edge design left unowned — owned here)

- Optimistic overlay values render with a **provisional** treatment (R3: `Pending` flag ⇒ visible chip; retire only on the authoritative confirmed delta).
- The intent queue is a first-class **Outbox** surface (queued / submitting / confirmed / rejected), with an offline banner; drain-on-reconnect is engine behavior.
- `RevisionConflict` ⇒ engine re-hydrates; the app presents "the world moved — your change wasn't applied" with the refreshed truth and a re-do affordance (F7's presentation model, v1: no auto-retry, no merge).
- Revocation: Gateway writes die immediately (403 → sign-out flow); sync dies at authorization expiry (≤15 m). On confirmed revocation/sign-out the local mirror is purged (documented residual: host-level storage until purge).

### 4.5 Security posture (what the app is trusted with: nothing)

- Client holds only the IdP JWT; no Lattice-minted secret ever reaches the device.
- Visibility = D1 read grants (fail-closed) + lens derivation; permission = step-3 at submit. The renderer *reflects* both and *enforces* neither; hiding a button is UX, not security.
- Actor forgery impossible at three layers (no wire field, transport denial, Gateway stamp). `authContext` from manifest data only selects which grant is *checked*.
- Subscribe confinement = the ratified auth-callout ACL; bucket isolation = the ratified natsperm work. Facet never reads Core KV, never holds `ops.>` publish, never reads platform buckets.

## 5. Transport staging

| Stage | Transport | Gate |
|---|---|---|
| **0 — dev showcase (buildable now)** | `cmd/facet` Go host embeds `internal/edge` (trusted posture, `EDGE_ACTOR_KEY`), serves the PWA + a localhost UI feed; native NATS to the dev cell | none — EDGE.1/2 + seeded topology |
| **1 — real identities** | same, minus trusted posture: bearer JWT on connect, whoami, claim UX | subscribe-ACL Fires 1–3 (EDGE.3) + multi-credential Fire 2 |
| **2 — browser-native** | the PWA connects directly: **NATS's native WebSocket listener** (supported since server 2.2; our pin is 2.14 — enable in `deploy/nats-server.conf` with TLS + origin policy; auth callout applies at the connection layer, parity verified in-fire) — no bespoke bridge component | Fire 4 (small: config + natsperm vectors + parity tests) |
| **3 — pocket reality** | background wake: **push-waker** (WebPush for PWA; APNs when iOS lands) nudging the device to drain/hydrate | its own design (named deferred item — the only genuinely undesigned transport piece; PL.6's "WebSocket bridge" otherwise collapses into Stage 2) |

The corpus treats "Gateway WS/push bridge" as one unbuilt blob gating EDGE.5; grounding against the pinned vendor splits it: WebSocket is **native server capability** (config, not construction), and only the push-waker needs design. This materially shortens the road to a real browser client.

## 6. Gap register

| # | Gap (evidence) | Disposition |
|---|---|---|
| G1 | Packages cannot declare a `nats_subject` lens — `pkgmgr.LensSpec` accepts `nats-kv`/`postgres` only; the whole PL plane has **zero production lenses** (`internal/natsperm/conf_test.go:303` "latent") | **Fire 0** (lattice) |
| G2 | SYNC stream ships with no `MaxAge` (design says 24 h) — unbounded growth, hydrate-vs-replay trade unenforced (`adapter/natssubject.go ensureSyncStream`) | **Fire 0** |
| G3 | Edge engine has no change-notification hook — `overlay.Read` is pull-only; a UI host cannot react to deltas | **Fire 0** |
| G4 | Interest Set is static per run (no re-registration API on the engine) | **Fire 0** (passthrough), UI use v1.1 |
| G5 | No app-facing discovery projection: `cap.svc` is auth-plane, `allowedOperations` carries bare `operationType`, op metas carry only `data.operationType`, rich metadata is per-DDL in Core KV, and no op-meta→DDL link exists (mapping implicit via `permittedCommands`) | **Fire 1** — the `edgeCatalog`/`edgeServices` lenses + per-op schema convention |
| G6 | No presentation metadata anywhere: services have no name aspect; ops have no title/icon/tone | **Fire 1** — vocabulary aspects (§3.3) |
| G7 | Client-side dispatch knowledge is hardcoded (`COMPLETIONS`, client-built `reads`) | **Fire 1** — `.dispatch` descriptor |
| G8 | No consumer-invocable service-path op exists (`authContext.service` has zero users; `CreateServiceInstance` is operator-only) | **Fire 1** — `RequestService` (creates instance `providedTo` the actor) — the laundry-order op |
| G9 | Zero service topology seeded; `service-location` isn't in any install chain (no templates, no `availableAt`, no `residesIn`, empty `cap.svc` plane) | **Fire 1** — install-chain + `make seed-edge-demo` |
| G10 | whoami unbuilt (client can't compute its opaque ActorID → no `authContext.target`, no claim UX) | dependency: multi-credential **Fire 2** (ratified, queued) |
| G11 | Subscribe isolation unbuilt (any broker-reachable connection may SUB anyone's sync subject) | dependency: subscribe-ACL **Fires 1–3** (ratified, queued) |
| G12 | Browser transport: no WebSocket listener configured; auth-callout/WS parity unproven here | **Fire 4** (sharpened by §5 — config + vectors, not a bridge) |
| G13 | Background wake: no push story at all (FakeNotification only; recipient targeting blocked on the Vault-decrypt-at-send fork) | deferred, **named consumer = Facet Stage 3**; file as its own lattice design when Stage 2 lands |
| G14 | Sensitive aspects unreadable on device (EDGE.4 unbuilt) | sequenced behind EDGE.3 (existing edge design §7); v1 manifest avoids the need (§3.4) |
| G15 | Consumer write surface is thin (3 standing self grants; most vertical ops operator-only) | existing named Phase-3 tail; Facet is the demand driver — honest manifest meanwhile |
| G16 | `personal.{register,hydrate}` trust body `identityId` (verified-actor override designed in subscribe-ACL §3.4, unbuilt) | rides subscribe-ACL Fire 2/3 |
| G17 | Per-op availability overrides + temporal windows deferred (Contract #6 §6.10/§6.11, Andrew) | unchanged; manifest inherits service-level availability |

## 7. Decomposition for the Steward (fire-by-fire, each independently shippable + green)

- **Fire 0 `[lattice]` — PL consumer enablement.** `pkgmgr.LensSpec` grows the `nats_subject` fields (subjectPrefix/stream/personal/keys); `ensureSyncStream` sets the designed 24 h `MaxAge`; `internal/edge` exports a change-notification hook (overlay watch) + Interest re-registration passthrough. *Green:* a package-declared personal lens installs and streams e2e; stream info shows MaxAge; a host receives per-key change callbacks. *Depends on:* nothing.
- **Fire 1 `[lattice]` — the manifest package + vocabulary.** `packages/edge-manifest` (five lenses §3.2); `docs/components/edge-manifest.md` vocabulary spec (FORK-1 B); pkgmgr `OpMetaSpec` presentation/per-op-inputSchema/dispatch fields + service-template `.presentation`; `RequestService` consumer op (service path) in service-domain; service-location joins the install chain + `make seed-edge-demo` (laundry template, `availableAt` building, tenant `residesIn`, `permitsOperation`). *Green:* `verify-package-edge-manifest` + e2e — a seeded tenant receives all five row kinds over SYNC; `RequestService` submits under `authContext.service` and the instance row arrives; an undescribed op degrades per §3.3. *Depends on:* Fire 0.
- **Fire 2 `[verticals]` — Facet v0 (dev host + renderer).** Sally UX spec first (`facet-app-ux.md`); then `cmd/facet`: Go host embedding the engine (trusted posture) + PWA renderer v1 (Home/Service/Task/Activity/Me, widget vocabulary, outbox, R3 pending treatment, conflict presentation §4.4). *Green:* in-browser e2e on the seeded stack — hydrate → order laundry (form from schema) → pending→confirmed → task completes via descriptor form with auto-complete → row vanishes; kill NATS mid-session → outbox queues → reconnect drains. *Depends on:* Fire 1.
- **Fire 3 `[verticals]` 🚧 GATED — real auth turn-on.** OIDC PKCE login, whoami wiring, claim/link UX (Me screen), revocation UX; `EDGE_ACTOR_KEY` retired from Facet. *Green:* fresh IdP user → provisioned A → claim link → world blooms; revoked actor is cut per §4.4. *Depends on:* subscribe-ACL Fires 1–3 + multi-credential Fire 2 (both ratified, queued).
- **Fire 4 `[lattice]` — browser-native transport.** WebSocket listener in `deploy/nats-server.conf` (TLS, `allowed_origins`), natsperm vectors extended to ws connections, auth-callout parity proven; FORK-2 resolved (wasm engine or TS mini-engine) and Facet drops the Go host. *Green:* the PWA on a second machine completes the Fire-2 e2e under confined permissions with no local binary. *Depends on:* Fire 3.
- **Fire 5 `[verticals]` — adoption + the second renderer.** Presentation aspects adopted across clinic/café/wellness consumer-shaped ops; a second domain slice (e.g. wellness booking via the service path); iOS/SwiftUI renderer spike over the identical manifest — triggers the FORK-1 freeze decision. *Green:* the acceptance demo — wire a brand-new service `availableAt` a building and watch it appear in both renderers with zero app change.

**Deferred, named:** push-waker design (G13, consumer = Stage 3) · EDGE.4 sensitive display · PL.6 multicast dedup (revisit when fan-out warrants) · vertical view-descriptors beyond service instances.

## 8. Non-goals (v1)

No local authority (EDGE.6 stays a separate Andrew-gated decision); no admin/cross-identity surfaces (Loupe exists; Facet renders only vocabulary-described personal projections — it is not a graph browser); no payments UX; no vendor push integration before the waker design; no per-vertical bespoke screens — a vertical that wants richer-than-vocabulary UI builds its own FE (the existing pattern) while Facet keeps the universal floor.

## 9. The throwaway demo

An interactive single-file mockup accompanies this design (Claude artifact, ephemeral by intent — <https://claude.ai/code/artifact/d7af37cd-dce1-47a3-83c5-dd1609a50356>): two personas over one "binary", a visible wire panel streaming the §3.2 frames verbatim, and the beats — login → hydrate → UI composes; order laundry (form generated from `inputSchema`) → outbox → confirmed → instance timeline; a mid-session `WireAvailableAt` sliding a new service in; a `permitsOperation` revoke removing an action; a task arriving, completed through its descriptor form; offline queue + drain; the claim beat. The frames in the demo are the row schemas above — the mockup is the manifest contract by example, not a separate truth.
