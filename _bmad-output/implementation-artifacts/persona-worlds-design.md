# Persona worlds — the Provider archetype, unified sign-in, and verticals as skins

**Status: ✅ RATIFIED (Andrew, 2026-07-23, interactive) — forks F1–F4 decided per recommendation; the §3.5
archetype ladder folded at ratification. Fires build-ready per §8 sequencing.**
**Board rows:** [verticals lane](../planning-artifacts/backlog/verticals.md) *Persona worlds* · [lattice lane](../planning-artifacts/backlog/lattice.md) *Persona-worlds platform seams*.
**Extends:** [facet-staff-worlds-design.md](facet-staff-worlds-design.md) (the staff half of this move, SHIPPED),
[edge-showcase-app-design.md](edge-showcase-app-design.md) (descriptor vocabulary + manifest plane),
[facet-entity-browse-design.md](facet-entity-browse-design.md), [clinic-domain-design.md](clinic-domain-design.md).
**Contracts:** builds to #1 (key shapes), #6 (capability), #11 (claim/opaque binding). **Frozen-contract change: NONE.**
**Grounds in:** PRD `endUsers` + FR17/FR18/FR19/FR24; brainstorm #52/#54/#55; shipped mechanisms cited per-section below (file:line audited 2026-07-23 @ `0bf28a52`).

---

## For Andrew (one-look ratification block)

**What it does (two lines).** Adds the fourth *human* archetype — **Provider** (doctor, laundry operator, yoga
instructor) — as pure capability-graph content: one new role + an `identifiedBy` binding from each vertical's
provider entity to a real identity, so a provider logs in like anyone else and their world derives from grants.
Converts all four vertical apps to **sign-in-first** (Facet's session pattern, extracted into a shared kit), deletes
every "pick who you are" surface, and makes each vertical a *UX skin* over the same discovered capability set.

**The one thing to understand before ratifying.** Nothing here mints a new platform concept. "Customer /
front-of-house / back-of-house / provider" never becomes a runtime enum — an archetype is a *design-time
convention for which roles + topology links a package should define*. The staff half already shipped exactly this
way (`facet-staff-worlds`): role = what you may do, a topology link = where your world is, and the manifest +
grant plane derive the rest. This design is the third spine (provider) plus the app-side consequence
(sign-in-first verticals). One human can hold all the hats at once — that scenario is the acceptance test, not an
edge case (§3.4).

**Naming note.** The PRD reserves "fourth user" for the AI agent (`prd.md:33-37`). This is therefore the **fourth
human archetype**, canonical role name **`provider`** — matching the shipped domain language (`vtx.provider`,
`providedBy`, `practicesAt`); "vendor" appears nowhere in the codebase and is not introduced.

### Forks

**F1 — Role granularity: one platform `provider` role vs per-vertical roles (`practitioner`, `instructor`, …).
DECIDED: ONE role, `provider`, seeded by identity-domain (Andrew, 2026-07-23).** Vertical scoping comes from the *binding*
(which provider entity your identity is `identifiedBy`-bound to) and the entity's own topology, exactly as
`consumer` is one role scoped by `residesIn`/`identifiedBy` and `frontOfHouse` is one role scoped by `worksAt`.
- *Road not taken — per-vertical roles:* multiplies `personalLensPermissions` + grant rows per vertical with no
  authorization gain (write-path scoping is in-script linkage + grant tables either way); contradicts the
  staff-worlds adjudication ("reuse `frontOfHouse`; do not mint `frontDesk`").

**F2 — The whoami surface: extend Gateway `GET /v1/actor` with `roles[]` + `anchors[]` vs a new per-app endpoint
or app-side capability-kv reads. DECIDED: EXTEND `/v1/actor` (Andrew, 2026-07-23).** It already exists, is already called by
every app's auth path, and the Gateway already reads `capability-kv` legitimately. Apps asking "who am I, which
hats" get one authenticated answer; nobody grows a new capability-kv consumer (`cap.roles.*` stays
Processor/Refractor/Gateway-only).
- *Road not taken — apps read `cap.roles.*` KV directly:* a new ambient consumer of the auth plane in every
  vertical binary; natsperm would have to widen per app; violates the P5 spirit even though capability-kv is not
  Core KV.

**F3 — Provider-entity granularity: per-domain entities + one generic `vtx.serviceprovider` in service-domain
vs one shared provider type everywhere. DECIDED: PER-DOMAIN (Andrew, 2026-07-23).** Clinic keeps `vtx.provider` (rich:
hours, time-off, `practicesAt`); wellness mints `vtx.instructor` (leads sessions, teaches at a studio);
service-domain mints a lean generic `vtx.serviceprovider` for template-attached vendors (the laundry operator)
— `providedBy` is already type-open (`service-domain/ddls.go:33,158-175`), so templates point at whichever
entity fits. Each binds to a login identically: `<entity> identifiedBy identity`.
- *Road not taken — one shared type:* couples every vertical to one package's DDL and flattens genuinely
  different aspect shapes (clinic hours ≠ laundry SLA ≠ instructor bio) into one vertex type — against D5
  data-placement.

**F4 — Session topology: per-app cookie sessions on one shared sign-in kit vs central SSO/redirect.
DECIDED: PER-APP COOKIE on the shared kit (Andrew, 2026-07-23).** Same identity plane, same credential, same code — but each app
sets its own HttpOnly cookie after its own `/login`, exactly Facet's shipped pattern. Answers "similar or
same?": **same sign-in system, per-app session.**
- *Road not taken — SSO now:* cross-origin cookie/redirect infrastructure for five localhost apps ahead of any
  real IdP (OIDC remains the deferred §4.1 step-1 of the edge-showcase design); pure scaffolding today. Revisit
  when a real IdP lands — the kit is the seam it would plug into.

**Also for your attention (not forks):** (a) FR24's actor-type list (`prd.md:732`) lacks the provider archetype —
a one-line PRD amendment for the planning lead once ratified; this doc does not touch planning artifacts.
(b) The café **supplier** slot is deliberately deferred — there is no café inventory/replenishment domain for a
supplier to act in, so a supplier role today is dead scaffolding; the named trigger is "café gains a
replenishment/inventory op set" (§7.4). (c) The **landlord** already proves the external-party-login pattern
(real identity + `manages` + RLS, `landlord_applications_rls_test.go:231-362`) — this design does not
re-taxonomize landlords; their submit-actor migration rides the LoftSpace fire (§7.2).

---

## 1. Problem

Three of the four human archetypes can already hold a real, capability-derived world. The fourth cannot:

- A clinic **provider is not an identity**. `cmd/clinic-app` mints a JWT whose subject is the provider *vertex's*
  bare NanoID (`clinic-domain/lenses.go:307-310`), picked from a dropdown; the provider holds no role, no
  `cap.roles` doc, no manifest world, and cannot submit ops as themself through the Gateway. Entity-as-actor is a
  dead end the identity plane was designed to prevent.
- Wellness has **no instructor concept**; café has **no supplier**; LoftSpace's laundry service template has a
  `providedBy` hook (`service-domain/ddls.go:158-175`) but nothing bindable behind it.
- Every vertical app asks the user to **select who they are** (applicant picker + landlord toggle; patient picker +
  provider dropdown + book-self checkbox; two Me-bars), built on a loopback endpoint that mints a JWT **for any
  caller-supplied subject** (`loftspace-app/readauth.go:224-262` et al.), and submits nearly everything as the
  primordial bootstrap admin — so per-role and scope=self grants on those surfaces are dormant
  ([[feedback_scoped_grant_dormant_if_write_uses_wildcard_actor]]).
- Facet, meanwhile, already renders per-identity worlds from the grant topology (13 `edge-manifest` lenses; op
  visibility via `holdsRole → grantedBy ← permission → forOperation`, `edge-manifest/lenses.go:569-599`) — but
  the verticals share none of its session/discovery posture.

The promise being enforced: **Facet is sufficient for every archetype** (everything you may do is discoverable
and self-describing there), and **a dedicated vertical adds UX, never capability**.

## 2. Grounding ledger (what exists, verified)

| Piece | State | This design's use |
|---|---|---|
| Role + permission + `forOperation` graph shapes | shipped (`rbac-domain/ddls.go:333,370`; `pkgmgr/build.go:277-315`) | provider grants are plain `PermissionSpec{GrantsTo:{"provider"}}` rows |
| Grant-derived op catalog per identity | shipped (`edge-manifest/lenses.go:564-599`, `viaRole` provenance) | provider ops appear in Facet with zero renderer changes; `viaRole` feeds hat-grouping |
| Link-scoped read grants (role × link → anchor) | shipped (`staffReadGrants`, `service-location/lenses.go:165-172`) | the provider grant producer is the same shape: `holdsRole provider × identifiedBy → provider-entity anchor` |
| Non-identity self-anchor producers | shipped (`clinicProviderReadGrants`, `clinic-domain/lenses.go:569-583`) | retired for logins (kept for nothing new); rows' `authz_anchors` stay unchanged — only the actor side moves (§4.3) |
| Row-side anchor comprehensions | shipped (`clinicAppointmentsRead`, `clinic-domain/lenses.go:526-527`) | provider schedules already carry the provider-vertex token; no Protected-lens rewrite |
| In-script linkage self-checks | shipped (patient `identifiedBy` check, `clinic-domain/ddls.go:1727-1751`) | provider ops assert "acting identity is `identifiedBy`-bound to the target provider entity" |
| Session pattern: login page → mint → HttpOnly cookie → login-time `GET /v1/actor` resolution → refresh → logout | shipped in Facet only (`cmd/facet/session.go:238-525`) | extracted into the shared kit (§5); verticals adopt it |
| Claim ceremony + persona fence + demo cards | shipped (`cmd/facet/claim.go:137-250`, `session.go:63-121`) | providers claim their login the same way patients do |
| Actor integrity at the transport | shipped (Gateway stamps verified subject, `gateway.go:374-377,478-483`; apps hold no `ops.>` publish, `natsperm/matrix.go:327-353`) | parity invariant §6 rests on this — no new enforcement machinery |
| whoami | shipped, minimal (`internal/gateway/whoami.go:18-23` — no roles) | grows `roles[]`+`anchors[]` (F2) |
| `worksAt` spine + Work-tab honesty invariant | shipped (`facet-staff-worlds`; `cmd/facet/web/app.js:808-818`) | generalized: every hat's tab/section appears iff its role+binding exists |

## 3. The archetype model

### 3.1 Archetypes are conventions, not runtime state

An archetype = a **role** (what you may do) + a **binding/topology link** (where, and as whom, your world is):

| Archetype | Role (identity-domain) | Binding / topology | Shipped? |
|---|---|---|---|
| Customer | `consumer` | `residesIn` (locality), `identifiedBy`/`applicationFor` (per-vertical records) | ✅ |
| Front-of-house | `frontOfHouse` | `worksAt` → building | ✅ (staff-worlds) |
| Back-of-house | `backOfHouse` | `worksAt` → building | ✅ (staff-worlds) |
| **Provider** | **`provider`** (new) | **`<providerEntity> identifiedBy identity`** (new) + the entity's own topology (`practicesAt`, `teachesAt`, `providedBy`) | this design |
| Owner-operator | `operator` today (root); `proprietor` carve-out named-deferred (§3.5) | the `'*'` wildcard anchor — the scope is the whole business | ✅ (as root) |

No platform component ever branches on an archetype name. The Processor authorizes grants; Refractor projects
reachability + read grants; renderers group by provenance. FR24's list grows a word; the runtime grows none.

### 3.2 The provider binding

Per-domain provider entities (F3), each gaining one link + one claim path:

- `lnk.provider.<id>.identifiedBy.identity.<id>` (clinic) — mirrors the patient link verbatim
  (`clinic-domain/ddls.go:918-927`); sentence test: *provider identifiedBy identity* ✓.
- `lnk.instructor.<id>.identifiedBy.identity.<id>` (wellness, new `vtx.instructor` with `teachesAt` → studio,
  `session ledBy instructor`).
- `lnk.serviceprovider.<id>.identifiedBy.identity.<id>` (service-domain, new lean `vtx.serviceprovider`;
  laundry's template gets `providedBy` → it).

Binding is established by the shipped claim ceremony (an unclaimed identity is seeded/created per provider with a
claim key; `ClaimIdentity` + a per-package `Bind<Entity>Identity` op mint the link with an in-script guard that the
entity isn't already bound). Role `provider` is granted at bind time, exactly as `ClaimIdentity` grants
`consumer` (`identity-domain/ddls.go:82-85`).

### 3.3 The provider world, derived

Once bound, every existing mechanism composes with **no new platform machinery**:

- **What I see (Path A / Postgres):** a new GrantTable producer per binding package, the `staffReadGrants` shape:
  `MATCH (i:identity)-[:holdsRole]->(r{provider}) MATCH (pe)-[:identifiedBy]->(i) RETURN nanoIdFromKey(i.key),
  nanoIdFromKey(pe.key), 'cap-read.provider.<domain>'`. Rows already anchored on provider-entity tokens
  (`providerAppointmentsRead`) become visible to the *login* with **zero row-side changes**.
- **What I see (Path B / manifest):** provider-hat slices in `edge-manifest`: `manifest.me` `selfAnchors` grows
  the bound provider-entity types; a `manifest.sched`-style lens walks `identifiedBy ← provider → appointments`
  (one sibling lens per path, per the no-UNION ceiling, `edge-manifest/lenses.go:72-77`); the laundry/work slice
  walks `identifiedBy ← serviceprovider ← providedBy ← template ← instances`. Each ships **in lockstep** with its
  read-grant slice (the Fire-1 lesson, `lenses.go:22-28`).
- **What I do:** `PermissionSpec{GrantsTo:{"provider"}}` on provider ops (clinic: manage own availability,
  complete/no-show own appointments; wellness: create/cancel own-led sessions, roster attendance; service-domain:
  advance own instances' `.outcome`). In-script linkage guards enforce "own" (§2 row 6). The grant-derived
  catalog then shows exactly these ops to exactly these identities — discovery and authorization share one
  topology by construction.

### 3.4 One human, many hats (the acceptance scenario)

A person who **lives in** Building B (consumer), **works the desk** at Building A (frontOfHouse ×
`worksAt`→A), and **teaches yoga** (provider × `identifiedBy`← instructor) is one identity, one login, three
bindings. Their `manifest.me` already unions roles + anchors; their catalog rows carry `viaRole`; their grant
docs union per-hat anchors. The renderer groups by provenance — "My home / My work / My services" — and a hat
*switcher* is a presentation filter over real bindings, never an identity change. **Green bar for the whole
design:** seed this human; one Facet login shows all three worlds correctly scoped; the wellness app shows
member+instructor hats; the LoftSpace app shows only the resident hat; front-desk scope covers Building A only.

### 3.5 The full ladder — where the landlord and the owner sit

Single-tenancy makes the sorting rule crisp: **the installation IS the tenant business**, and archetypes
classify people by their relationship to it — service flows *to* you (customer), you *are* the business
(front/back-of-house), service flows *through* you to its customers (provider), or the business *answers to*
you (**owner-operator** — the slot the PRD already reserves as FR24's `operator` actor type; Journey 4's
VP-Ops persona; Loupe is explicitly its console). Five human archetypes, the AI agent riding across all of
them. Everyone — the owner included — is graph content; other businesses appear inside the graph as provider
entities with scoped logins, never as tenants (multi-tenancy is multi-cell territory, parked).

Two corollaries this design leans on:

- **"Landlord" is a domain role, not an archetype.** In the owner-operator configuration (the showcase world:
  the building operator hosting clinic/café/studio) landlord-humans are the owner archetype and `manages`
  scopes their own portfolio. In a manager-serving-owners configuration they are external principals —
  provider-shaped mechanically (real identity + binding + link-scoped grants + a curated op set) even though
  colloquially they're B2B clients. The machinery is identical either way, which is why §7.2 migrates the
  landlord's submit actor and deliberately does not re-taxonomize them.
- **The `operator` role conflates platform-root with business-root.** Right for dev/demo; wrong posture for a
  real client, where the proprietor of the experience business needs wildcard reads, executive/decision ops,
  and convergence direction — but not package lifecycle or raw writes. The carve-out precedent already ships
  (`consoleOperator` "not root — no anchor"; `demoOperator` read-only wildcard); a **`proprietor`** role is
  the third slice. **Named-deferred; trigger: a real client deployment.** The small-business collapse is §3.4
  again from the top of the authority gradient: one human holding `proprietor` + `frontOfHouse` + `provider`
  is three hats, one login.

## 4. World discovery — what's added

The three questions and their surfaces:

1. **Who am I / which hats?** `GET /v1/actor` (F2) grows `roles[]` (from `cap.roles.<actor>`, which already
   carries them — `capabilitykv/doc.go:30-76`) and `anchors[]` (relation-stamped bindings: `residesIn`,
   `worksAt`, `manages`, `identifiedBy`-inverse), matching what `manifest.me` shows Facet. This is the *only*
   new app-facing query surface; it is what lets a vertical render hats without joining the SYNC plane.
   *(The set is open: a hat whose binding is a distinct relation joins by adding its walk to
   `identityAnchorsSpec`. `manages` — the landlord hat — was added by W2 Inc 2; the walk is purely additive,
   since every consumer selects by `relation` rather than position.)*
2. **What may I do?** Already answered per-identity by the two catalog lenses; provider ops join by declaring
   grants (§3.3). The deferred task-`forOperation` catalog path stays deferred (unchanged consumer).
3. **What may I see?** Already answered by reachability + read grants; provider slices join per §3.3.

Facet renderer completion (rides the Facet fire, §7.5): read `presentation.group` + `viaRole`/`resolvedVia` to
group Home/nav by hat; add bound-provider types to the `{me.<type>}` selfAnchor resolution so provider-targeted
ops resolve. The generic declare-a-collection browse surface (gap: three hand-written `manifest.ent` lenses;
`tab`/`appointment` targetTypes are dead ends — `edge-manifest/lenses.go:455-545`) is **out of scope** here and
filed as its own demand — this design only adds the provider-hat slices it names.

## 5. Unified sign-in — the kit

Extract Facet's session block into **`internal/appsession`** (new; platform-internal, imported by all five FE
binaries): login-page handler (shared static page, app-name parameter), `POST /api/login` (dev posture:
NanoID/claim-key, persona-fence allow-list, loopback-gated — Facet's `handleDevLogin` semantics verbatim),
HttpOnly cookie issue/verify (strict + refresh authenticators, 5m grace), login-time credential→identity
resolution via `GET /v1/actor`, `POST /api/session/refresh`, logout, and a `RequireSession` middleware returning
the verified subject. Production posture stays verify-only JWT (`LOFTSPACE_APP_JWT_PUBLIC_KEY` shape) — the kit
is where a real IdP plugs in later (F4).

Per vertical, adoption means: serve `/login` before anything; delete `/api/dev-token` (the any-subject
impersonation mint) and `/api/staff/dev-token` (the fixed-admin mint) as ambient API; all reads/submits carry the
session subject; staff are identities with roles who log in like anyone else. The FE asks whoami for
roles+anchors and renders hats from the answer — the pickers' *legitimate* residue (which patient record, which
provider schedule) becomes data selection *within* an authorized hat, never actor selection.

## 6. The parity invariant

**Capability truth lives only in the graph.** Concretely, an app conforms iff:

1. It holds no minting surface for subjects other than the logging-in user (dev posture included — the fence
   lists *personas*, not "any subject").
2. Every submit goes browser-direct to the Gateway under the signed-in user's token (already structurally
   enforced: apps hold no `ops.>` publish — `natsperm/matrix.go:327-353`; the Gateway ignores any client actor
   field — `gateway.go:374-377`).
3. Every read boundary keys on the verified session subject (RLS `set_config` / Path-B ACL), never an app-held
   admin credential standing in for a user.
4. Anything it *offers* the user is a subset of the identity's discovered capability set; anything it *hides* is
   still reachable in Facet. (Curation is UX; capability deltas are bugs.)

Enforcement posture: (2) is already structural; (1)+(3) become a `lint-conventions` gate — **no
`bootstrap.BootstrapIdentityKey` reference and no any-subject mint handler in `cmd/<app>` outside the kit's dev
posture** — flipped blocking once the four reworks land; (4) is the per-fire live-verify script (each hat's op
list in the vertical == the same identity's catalog rows in Facet).

## 7. Per-vertical rework (each = one fire, §8)

Common to all: adopt the kit; delete pickers/mints; whoami-driven hats; per-actor submits with a **grants audit**
— every op the UI offers a hat must carry a real `GrantsTo` for that hat's role (the staff-wildcard default
currently masks the gaps; e.g. staff booking has no `frontOfHouse` grant on clinic `CreateAppointment`,
`clinic-domain/permissions.go:50-79`), plus the two lockstep obligations (§8 gates).

- **7.1 Clinic** — hats: patient (self-book/cancel, My Appointments), front-desk (book-for-anyone, Schedule,
  Follow-ups, Availability, Sites), **provider** (My Schedule via own binding, availability/time-off self-service,
  complete/no-show own appointments). Retires entity-as-actor: the provider-subject dev mint dies; grant
  re-anchoring per §3.3 (rows untouched). The provider dropdown becomes a front-desk *view* filter only.
- **7.2 LoftSpace** — hats: applicant/resident, landlord (existing RLS world; submits migrate off the admin mint
  onto the signed-in landlord — the decision op's `frontOfHouse` grant gains a landlord-scoped path or an
  explicit landlord role decision *inside the fire*, flagged if it smells contract-ish), staff (portfolio pulse,
  worklist), **provider** (laundry: `vtx.serviceprovider` + `providedBy` on the laundry template + instance work
  queue + advance-outcome op).
- **7.3 Wellness** — hats: member (browse/book/cancel), staff (create sessions, roster), **instructor**
  (`vtx.instructor`, `ledBy` on sessions, own-roster + attendance + cancel-own-session ops). Stands up the Tier-B
  read boundary for per-user reads (bookings/My Classes move behind the session; schedule stays public-read).
- **7.4 Café** — hats: resident (own tab, self open/settle), staff (POS, front-desk grid). **Supplier deferred**
  (named trigger: a replenishment/inventory op set exists for a supplier to act in). Stands up read auth for
  tab/ledger reads (today an unauthenticated clinic-wide dump).
- **7.5 Facet** — provider hat + hat-grouped landing (§4); demo-persona cards gain the provider + multi-hat
  personas; seed-showcase adds Dr. Amara Osei (clinic provider), Kai the laundry operator (serviceprovider),
  and makes one existing persona the §3.4 multi-hat human.

## 8. Decomposition for the Steward (fire-by-fire, each independently shippable + green)

- **Fire P1 `[lattice]` — whoami hats.** `GET /v1/actor` gains `roles[]` + `anchors[]` (F2). *Green:* an
  authenticated call returns the seeded multi-hat identity's three roles + relation-stamped anchors; existing
  callers unaffected. *Depends on:* nothing.
- **Fire P2 `[lattice]` — `internal/appsession` kit.** Extract Facet's session block (§5); Facet refactors onto
  it (behavior-identical). *Green:* Facet login/refresh/logout unchanged end-to-end on the kit; the kit's tests
  cover fence, cookie, refresh-grace, resolution. *Depends on:* nothing (P1 parallel-ok).
- **Fire W0 `[verticals]` — the provider spine (packages).** identity-domain seeds role `provider` +
  `personalLensPermissions` lockstep; clinic/wellness/service-domain bindings + bind ops + grant producers +
  provider ops/grants + manifest provider slices + read-grant slices; seed-showcase personas. *Green:* seeded
  Dr. Osei logs in **in Facet** and sees her schedule + provider ops, scoped to her; the multi-hat human's three
  worlds compose; `verify-package` + protected-lens tests green. *Depends on:* P1 (whoami hats for the fence
  check), not P2.
- **Fires W1–W4 `[verticals]` — clinic / loftspace / wellness / café reworks** per §7, one fire each, any order.
  *Green (each):* sign-in-first; pickers + both mints deleted; every hat's offered op set == the same identity's
  Facet catalog (§6.4 script); RLS tests keep passing with session subjects; café/wellness read boundaries
  authenticated. *Depend on:* P1+P2; W1–W3 also on W0.
- **Fire W5 `[verticals]` — Facet hats + landing.** §7.5. *Green:* the §3.4 one-login-three-worlds demo,
  live-verified. *Depends on:* W0.

**Build gates (every fire):** (a) any new role must join `personalLensPermissions` GrantsTo in the same change
(pinned by `control-authz` test); (b) any lens anchoring on a new kind ships its read-grant slice in the same
change; (c) `make provision-readpath` after Protected/GrantTable DDL; (d) package version bumps for live stacks;
(e) the §6 lint gate flips blocking after W1–W4.

**Deferred, named:** generic declarable entity-browse/collections (consumer: café `tab` + clinic `appointment`
targetType dead-ends — files as its own lattice-lane demand); café supplier (trigger §7.4); OIDC/real IdP (kit
is the seam); cross-app SSO (F4); landlord re-taxonomization (§ For-Andrew c); the `proprietor` business-root
carve-out (§3.5; trigger: a real client deployment).

## 9. Reconciliation (didn't-we-already / duplicate-or-diverge / new state?)

- *Didn't staff-worlds already do this?* It shipped the **staff** half and the method; this design reuses its
  spine pattern and its adjudications (role reuse, honesty invariant) for the provider half + the app rework.
  Nothing overlaps: staff-worlds' board row is CLOSED and untouched.
- *Doesn't clinic already have provider self-service?* Read-side yes — anchored on the provider **vertex** as
  actor. This design moves the *actor* to a real identity and keeps every row/anchor as-is; the old grant
  producer stays for nothing new and is retired with the entity-as-actor mint in W1.
- *Does this duplicate the landlord pattern?* No — it generalizes it: landlord binds identity→unit directly
  (`manages`); provider-hood hangs off a domain entity, hence the `identifiedBy` binding. Both end at the same
  RLS/grant machinery.
- *New state?* One role vertex, three link types, one lean vertex type (`serviceprovider`, plus wellness
  `instructor`), grant/permission rows, lens rows — all package content. No engine state, no new Core-KV
  readers, no contract edits.
- *Fleet interaction:* rows file 📐; stewards build only post-ratification, W-fires sequenced per §8.

## 10. Build notes (fire briefs)

### Fire P1 fire brief (build note, 2026-07-23)

**1 · Scope sentence (verbatim §8):** *"Fire P1 `[lattice]` — whoami hats. `GET /v1/actor` gains `roles[]` +
`anchors[]` (F2). Green: an authenticated call returns the seeded multi-hat identity's three roles +
relation-stamped anchors; existing callers unaffected. Depends on: nothing."* **Green restated (narrowed):**
no multi-hat identity is seeded until W0 — P1 greens on existing personas (Dana → `[frontOfHouse]` + one
`worksAt` anchor; Riley → `[consumer]` + one `residesIn` anchor); the three-role assertion discharges in W0.

**2–3 · Verified touch-list + precedents (scouted live @ `8e9d4c6c`):**
- `packages/identity-domain/lenses.go` — NEW **`identityAnchors`** lens: `nats-kv` actorAggregate → own
  bucket **`identity-anchors`**, `OutputKeyPattern "anchors.{actorSuffix}"`, body `["anchors"]`,
  `EmptyBehavior "delete"`; cypher mirrors the me-lens anchors walk (`edge-manifest/lenses.go:293-313` —
  OPTIONAL MATCH `residesIn`/`worksAt`, relation stamped as a literal, entry shape
  `{key,name,container,containerName,relation}`). Bucket auto-created by Refractor at lens activation
  (`cmd/refractor/main.go:396-411`); no provisioning, no contract text. *Rejected paths:* landing the doc in
  `capability-kv` (Contract #6 §6.1/§6.2 are key-class/shape-closed → frozen-contract touch, against §9) and
  extending rbac's `cap.roles` doc (same contract closure + rbac would acquire a topology dependency).
- `packages/identity-domain/package.go` — **version bump** (package edits don't reach live stacks without one).
- `internal/gateway/rolesanchors/` (NEW) — mirror `internal/gateway/identityindexhint/` exactly (kvGetter
  interface + compile-time `*substrate.KV` pin + warn-and-degrade): **roles** =
  `capabilitykv.ReadAndMerge` single GET on `RolesKeyFromActor(resolvedActor)` (`capabilitykv/keys.go:28-34`;
  never wire `bootstrap.SystemActorKeys` — it scans core-kv); **anchors** = `OpenKV("identity-anchors")` GET.
- `internal/gateway/whoami.go:18-23,71-83` — response gains `Roles []string` (role **vertex keys**) +
  `Anchors []` (`omitempty`), keyed by the resolved actor. Both existing decoders are lenient
  (`cmd/facet/session.go:377-380,407`; `whoami_test.go`) — additive-safe; whoami is login-cold-path.
- `internal/gateway/gateway.go` + `cmd/gateway/main.go:272-283` — `Configure*` seam + best-effort wiring
  beside the identity-index-hint block.
- `internal/gateway/whoami_test.go` — fake-resolver vectors (mirror `fakeIdentityIndexHintResolver` :50-60).
- natsperm — **no matrix change** (daemon reads are unrestricted; the capability-kv write-deny pin
  `conf_test.go:372` stays untouched); add a positive gateway-read vector mirroring
  `bridge_egress_test.go:99` inverted.

**4 · Increments + green script:** (1) identity-domain lens + bump; (2) gateway resolver pkg + response
fields + wiring → `go test ./internal/gateway/...`; (3) natsperm vector + ALL `scripts/lint-*.go` + gates;
(4) live: cycle `bin/gateway` (up-full inline recipe; MERGED ≠ RUNNING), `make reinstall-package
PKG=identity-domain`, then
`TOK=$(gateway dev-token <dana|riley>) && curl -s -H "Authorization: Bearer $TOK" :8080/v1/actor | jq '.roles,.anchors'`
→ Dana: frontOfHouse role key + `worksAt` anchor (building container); Riley: consumer + `residesIn`.

**5 · In-scope gotchas:** roles are vertex keys, not names (labels come later via canonicalName consumers);
gateway is absent from lint-conventions `platformCmds` — keep core-kv strings out of it; fresh worktree
(three stale ones exist); `jsstore.Dir(t)` for any embedded-NATS test.

**6 · Adjacent finds (filed pre-build):** `/v1/actor` writes no CORS headers unlike its sibling handlers
(`gateway.go:423-431`) → XS row filed to the lattice lane (consumer: any browser-direct whoami caller; the
appsession kit resolves server-side, so no current one).

**7 · Non-goals:** no contract text; no natsperm matrix edit; no SystemActorKeys in the gateway; no Facet
changes; no W0 seeds; no CORS (filed instead).

**Scope-diff gate: PASS** — every touch traces to `roles[]`/`anchors[]`; the green bar narrowed (recorded
above), never widened; declared "depends on nothing" re-verified true.

**As-built (2026-07-23, `a16b7589`):** shipped per brief, sonnet builder, all gates green. Deviations
(each precedent-grounded): the roles reader takes `*substrate.Conn` directly (every `ReadAndMerge` caller
does; `*substrate.KV` lacks the bucket-keyed Get); `RealnessFilter:"key"` + `Freshness:"auto"` added —
without a realness filter, degenerate OPTIONAL-MATCH collect entries keep `EmptyBehavior:"delete"` from
ever firing (myTasks precedent); `Lanes` omitted (a capability-kv-only semantic); no identity-domain
lens-count pin exists to extend (the manifest cross-check covers it). **Live-verified** on the running
stack (2026-07-23): identity-domain 0.4.1→0.5.0 diff-applied in place; Refractor auto-created
`identity-anchors` at activation (gateway restart logged no unavailable-warning); `bin/gateway` cycled per
the up-full recipe; `/v1/actor` returns Dana → `[frontOfHouse]` + `worksAt` Riverside Building, Riley →
`[consumer]` + `residesIn` Unit 1 (container Riverside, names projected). Three-role assertion → W0.

### Fire W0 fire brief (build note, 2026-07-23)

**1 · Scope sentence (verbatim §8):** *"Fire W0 `[verticals]` — the provider spine (packages).
identity-domain seeds role `provider` + `personalLensPermissions` lockstep; clinic/wellness/service-domain
bindings + bind ops + grant producers + provider ops/grants + manifest provider slices + read-grant slices;
seed-showcase personas. Green: seeded Dr. Osei logs in in Facet and sees her schedule + provider ops, scoped
to her; the multi-hat human's three worlds compose; verify-package + protected-lens tests green. Depends on:
P1."* **Narrowings (recorded):** wellness "roster attendance" has NO substrate anywhere (no attendance
machinery exists) — dropped from W0, owned by W3's design pass; instructor ops scope to cancel-own-session
(`TombstoneSession` grant + guard). Provider-hat W0 GrantTable producer ships for **clinic only** — wellness/
service-domain have no Protected table to consume one (orphan-grant avoidance); W3/W2 add theirs with their
read boundaries.

**2–3 · Touch-list + precedents (scouted live @ `8e9d4c6c`; scout detail in git — this is the checklist):**
- **Frozen link seam** (everything else builds against these): `lnk.provider.<id>.identifiedBy.identity.<id>`
  · `lnk.instructor.<id>.identifiedBy.identity.<id>` · `lnk.serviceprovider.<id>.identifiedBy.identity.<id>`
  (all mirror the patient link, clinic `ddls.go:918-931`) · `lnk.instructor.<id>.teachesAt.studio.<id>` ·
  `lnk.session.<id>.ledBy.instructor.<id>` · existing type-open `providedBy` reused untouched
  (`service-domain/ddls.go:429-439`).
- `packages/identity-domain` — RoleSpec `provider` (package.go:39-44) + tests (package_test.go:27-45) +
  `scripts/verify-package-identity.go:105`; bump 0.4.1→0.5.0. **Load-bearing non-package edit:**
  `cmd/lattice-pkg/main.go:565` roleIDsFromBootstrap += "provider" — without it every downstream
  `GrantsTo:["provider"]` install fails (each install is a separate lattice-pkg process).
- `packages/control-authz` — permissions.go:63 GrantsTo += "provider" (+Note) + package_test.go:80 +
  manifest grantsTo ×5; bump 0.6.0→0.7.0.
- `packages/clinic-domain` (bump 0.23.1→0.24.0): `BindProviderIdentity` in the provider vertexType DDL —
  identifiedBy mint + **idempotent** holdsRole mint (AssignRole's state-check branch, rbac `ddls.go:337-339`,
  role key pinned via the `__EXPECTED_*__`/`strings.ReplaceAll` idiom, identity-domain `ddls.go:17,526`) +
  CreateOnly guards BOTH sides (entity-keyed `.identityClaim` + identity-keyed `.providerClaim`, mirroring
  `claim_identity` `ddls.go:888-903`). Provider grants **scope=any** + a third standing binder (actor
  identity `identifiedBy`-bound to the target provider, beside `require_workplace` — its doc `:1395-1401`
  frames binders as complementary): `SetProviderHours`/`SetProviderTimeOff` (guard added from scratch —
  import the operator-exemption walk), `SetAppointmentStatus`/`RescheduleAppointment` (extend the standing
  branch; the consumer self branch stays patient-only). NEW op-metas: SetProviderHours + SetProviderTimeOff
  (`TargetType "provider"`, authContext standing) — granted-but-meta-less ops are invisible (`forOperation`
  links mint only with a meta). GrantTable producer `providerIdentityReadGrants`: staffReadGrants shape
  (`service-location/lenses.go:67-74,165-172` — unanchored, `GrantSource "cap-read.provider.clinic"`,
  `DiffRetraction: true` — link-revocation retraction cannot ride an anchor tombstone), cypher
  `holdsRole→provider × (pr)-[:identifiedBy]→(i)` → `{identity-nanoid, provider-nanoid}`. Tests:
  TestPackage_Permissions tuples, lens pins (10→11), protected_lens_test both-links-required +
  either-link-dropped vectors.
- `packages/wellness-domain` (bump 0.8.1→0.9.0): `instructor` vertexType DDL (Create/Tombstone/
  BindInstructorIdentity; profile aspect; optional `teachesAt` studio param) + optional `instructor` param on
  CreateSession minting `ledBy`; TombstoneSession GrantsTo += provider + standing guard (session `ledBy`
  instructor × instructor `identifiedBy` actor — known-key probes) + op-meta "Cancel class"
  (`TargetType "session"`). **No attendance** (§1 narrowing). P7 gate: no `.class/.family/.kind` aspects.
- `packages/service-domain` (bump 0.8.0→0.9.0): lean `serviceprovider` vertexType DDL (Create/Bind + guard
  aspects); `WireProvidedBy` op (mirrors the seed's Wire* idiom) to wire the live laundry template;
  `RecordServiceOutcome` GrantsTo += provider + standing ownership chain guard
  (`instanceOf → providedBy → identifiedBy`, caller-declared known keys) — **the advance op already exists;
  build none**. Verify rides `verify-package-service-location`.
- `packages/edge-manifest` (bump 0.8.0→0.9.0, one atomic change with its grants — the Fire-1
  invisible-rows trap, lenses.go:14-28): me-lens selfAnchors += three inbound-`identifiedBy` walks
  (provider/instructor/serviceprovider); `edgeProviderSchedule` — **ns `manifest.ent`, entityType
  `"appointment"`** (a `manifest.sched` ns would render NOWHERE — renderer knows seven namespaces;
  entityType must equal the entityKey's vtx-type segment for op-attach + payload-resolve), walk
  `(identity)<-[:identifiedBy]-(pr:provider)<-[:withProvider]-(appt)`, columns
  reason/status/startsAt/endsAt/providerKey — **D3: no patient names on SYNC rows**; `edgeProviderQueue` —
  ns `manifest.ent`, entityType `"service"`, walk `<-[:identifiedBy]-(sp)<-[:providedBy]-(tpl)
  <-[:instanceOf]-(inst)` (instance→template is `instanceOf`, NOT providedTo), title from template
  presentation, status via the edgeInstances CASE idiom, no startsAt (always-current); `edgeInstructorSessions`
  — ns `manifest.ent`, entityType `"session"`, RETURN **byte-identical to edgeEntitySessionsSpec** (the
  resident-instructor LWW overlap must be idempotent); third read-grant producer
  `edgeManifestProviderReadGrants` → `cap-read.edgeManifestProvider.{actorSuffix}` (separate producer per the
  staff-slice cross-product rationale, lenses.go:697-703) with the three anchor branches. Structure pins:
  package_test 13→17 lenses; manifest.yaml declares; `scripts/verify-package-edge-manifest.go` map.
- `scripts/seed-showcase.go` — **harden `ensureStaff`/`ensureMaintenanceTech` first**: exclude candidates
  holding `consumer` (the seed invariant "Dana is purely staff"; a second `frontOfHouse` holder otherwise
  re-creates the `35ca90f5` mis-resolution). Then: Dr. Amara Osei = NEW second fixed-id provider (+
  practicesAt + identity + bind + role; Patel stays UNBOUND — the scoping negative); fixed-id patient
  (identityKey=Riley) + one future 15-min-grid appointment per provider (day-derived ids); Kai = NEW
  serviceprovider + identity + bind + `WireProvidedBy` laundry + one OPEN instance providedTo Sam; Sam =
  the §3.4 multi-hat human (consumer+residesIn kept; + frontOfHouse + worksAt; + instructor `identifiedBy`
  + `teachesAt` studio + `ledBy` on the day-rolled session — re-wire per reseed). Env prints
  `FACET_PROVIDER_NANOID` + `FACET_LAUNDRY_NANOID` in BOTH branches; `waitForRoleGrant` per new persona.

**4 · Increments:** (1) identity-domain role + lattice-pkg roster; (2) control-authz lockstep; (3) clinic
spine; (4) wellness spine; (5) service spine; (6) edge-manifest slices+grants; (7) seed; (8) gates + live.
1→(2..5); 3/4/5 parallel (disjoint); 6 after the link seam is frozen (parallel to 3-5 by files); 7 after
3-6; 8 last. **Green script:** reinstall ×6 bumped packages → `make provision-readpath` (clinic GrantTable)
→ `make seed-showcase` → dev-login Osei on :7810 → SSE snapshot to `ready` → grep `manifest.ent` +
`"entityType":"appointment"` (hers) + `manifest.op` provider op + **negative**: no Patel-appointment id in
her feed → same for Kai (queue row + RecordServiceOutcome op) → Sam's feed shows all three hats' rows.

**5 · Gotchas:** every new script read carries `# read-posture:` annotations ((a)/(d) per the clinic
patterns — new scripts land clean); cypher ceilings (no UNION → sibling lens per path; `when` reserved →
`startsAt`; `<> null`; degenerate collect entries expected; every row aliases `anchor`); NanoID alphabet
(no l/I/O/0) — mint new fixed ids with `substrate.NewNanoID`; manifest grantsTo lists mirror permissions
field-by-field; appointment times future + 15-min grid; `isUpcoming` hides past appointments (act-on-past =
W1/W5 residue); wrong-hat op cards attach cross-hat and fail closed in-script (W5 grouping residue);
`selfAnchorKey` answers only when exactly one entity of a type exists.

**6 · Adjacent finds:** none new filed — all residues attach to already-filed fires: attendance design →
W3; act-on-past-appointment + hat-grouped rendering of target-less provider ops (SetProviderHours renders
only on provider entity detail until W5's landing) → W5/W1; hosted-demo persona-card redeploy (demo-up.sh
labels) → deployment task named at W5, not a lane row.

**7 · Non-goals:** no attendance domain; no wellness/service GrantTable producers; no cmd/facet changes;
no cmd/<app> FE changes (W1–W4); no Protected-table changes (rows already carry provider anchors); no
contract text; no hosted-demo box redeploy.

**Scope-diff gate: PASS** — all touches trace to the §8 sentence; two narrowings recorded (attendance out,
clinic-only producer), no widening (lattice-pkg roster + ensureStaff hardening are load-bearing enablers of
in-scope items, recorded here); dependency re-verified: P1 confirmed (Andrew, standing) and satisfied by
build order.

**As-built + live-verified (2026-07-23, `a8069d16`; CI green):** all six packages upgraded + seeded on the
running stack. **Dr. Osei** logs into Facet and sees exactly her appointment ("Sports physical", her provider
key) with all four provider ops in her catalog — and **zero** rows for Patel's provider (the scoping negative
control passes). **Kai** sees his open "Maple Laundry" instance + the Record-outcome op. **Sam** holds all
three hats at the authoritative layer (whoami: consumer+frontOfHouse+provider, residesIn Unit 2 + worksAt
Riverside) and his instructor session ("Evening Flow", the fixed 19:00 hour) renders in Facet. `ensureStaff`
hardening held (FACET_STAFF_NANOID stayed Dana despite Sam gaining frontOfHouse). **Known tail —
investigated + resolved at the server level (2026-07-24, `6aa4959c`):** Sam's `manifest.me` summary row was
observed frozen at his pre-hat first-projection state (consumer+residesIn only). The originally-filed
mechanism — "guarded-write ordering-token reconciliation" ([[project_capability_projection_reconciliation]])
— was **disproven**: a `nats-subject` Personal lens has no such guard. `NatsSubjectAdapter` is fire-and-forget
(`internal/refractor/adapter/natssubject.go`, no CAS concept), and `Reproject` explicitly refuses
`KeySetPublisher` adapters (`internal/refractor/pipeline/reproject.go`) — the §6.2 ordering-token guard lives
only on the `nats-kv` actorAggregate path, a different adapter. The full server chain was then traced and
**proven sound** end-to-end: (1) a pure `KindLink` event (`worksAt` / a 2nd `holdsRole` / an inbound
`identifiedBy`) fans out to the recipient — `evaluateLinkFanOut` enumerates both endpoints and the
identity-typed one fast-paths, and a `Personal:true` lens IS on the `actorEnumerator` path
(`InstallPersonalLens` sets it, `PersonalActorType="identity"`); (2) re-execution re-runs the self-anchored
cypher live, growing its `collect()` columns; (3) the D1 read-gate passes a self-anchored row because the
kernel base `cap-read.identity.<actor>` slice (`internal/bootstrap/lenses.go` `CapabilityReadLensDefinition`)
grants an actor read of its own vertex; (4) the publish stamps a strictly-advancing revision
(`ProjectionSeq = msg.Sequence`), and the client LWW gate applies on `>=`. The regression e2e
`TestPersonalLens_SelfAnchoredRow_GrowsWhenActorGainsALink_E2E` (an existing self-anchored row grows from one
hat to two on a later `holdsRole` link, D1 gate active) guards this. So **there is no Refractor bug**; any
residual freeze that still reproduces live is client-side (Facet/Edge sync re-subscribe / re-render) and needs
a live repro to confirm — not a server build. Presentation-only either way (authz + per-hat data all correct).
**CI miss caught post-merge:** `verify-package-clinic-domain` (stack-gate only, invisible to `go test`)
asserted the pre-W0 provider-DDL command count; fixed in `a8069d16`.

**Wave-1 build corrections (2026-07-23; increments 1–3 green in the worktree; W1–W4 briefs inherit these):**
(1) **A permission's identity is `(operationType, scope)` — Contract #8 §8.1** — so granting `provider` on
an existing op means *widening the existing scope=any row's GrantsTo*, never adding a second row (the
brief said "new rows"; the installer rejects the collision — proven live in tests). (2) A DDL script that
never minted links has no `make_link` helper — the bind ops import it. (3) Every test harness that
installs a package carrying provider grants needs `"provider": pkgmgr.RoleID("identity-domain",
"provider")` in its `inst.RoleIDs` map — clinic-domain, clinic-ledger, clinic-reminders fixed; siblings
checked proactively. (4) Adjacent find, filed to the Loupe lane: `cmd/loupe/pkg.go` `kernelRoleIDs()`
resolves only `operator` — Loupe-UI installs of packages granting any other role fail (pre-existing).

**Adversarial review (2026-07-23; full suite + golangci-lint green; blind-hunter + edge-case-hunter):** one
MEDIUM finding actioned — the three `Bind*` ops (+ service `CreateServiceProvider`) were `{operator,
frontOfHouse}`; because a bind mints the `provider` role and the provider guards deliberately omit a
`worksAt` check, a front-desk actor could bind an *unbound* provider at another building (Patel is seeded as
exactly that target) and escalate past workplace confinement. Fixed to **operator-only** — consistent with
the operator-only entity-creation ops that are a bind's precondition, so the front-desk grant bought only
attack surface, never a workflow (front-desk can't create the entity to bind). Two findings accepted
by-design, recorded: `RecordServiceOutcome` authorizes at *template* granularity (an instance has no
per-provider link to check against — tightening needs new mechanism; wiring is operator-only so not
attacker-reachable); the `providerIdentityReadGrants` `WHERE`-between-MATCHes form is well-precedented and
the `identifiedBy` MATCH is the real constraint (no over-grant) — activation confirmed by live-verify.

**Edge-case review (2026-07-23; two HIGH actioned, remainder filed):** (1) the clinic provider DDL's
`InputSchema` was malformed JSON — one extra `}` closed `properties` early, exposing `identityKey` to the
root; fixed + validated (a broken schema Loupe/agents would silently reject). (2) the seed's day-derived
appointments/sessions used `Now()`-relative times, so a reseed one day later landed the +1-day entity on the
+2-day entity's date at the same wall-clock slot → deterministic patient/studio hub collision; fixed with a
run-time-independent `futureDayAt(days, hour)` pinning each W0 entity to a distinct fixed hour (same-day
idempotency preserved). Also fixed incidentally: the verify-package-clinic-domain grantee pin already
expected operator-only `BindProviderIdentity` while the real grant was `{operator, frontOfHouse}` — a latent
CI-stack-gate mismatch the security fix resolved. Filed, not blocking: seed partial-failure recovery +
ctx-window gaps (verticals lane — recoverable by a wipe, no runtime impact); the tombstoned-`holdsRole`
no-revive pattern shared with rbac `AssignRole` (lattice lane). Everything the review "walked and sound" —
the bind-guard 6-combo matrix, the standing-guard confinement, the byte-identical LWW lens overlap, degenerate
rows, Facet-now rendering — stands.

### Fire P2 fire brief (build note, 2026-07-24)

**1 · Scope sentence (verbatim §8):** *"Fire P2 `[lattice]` — `internal/appsession` kit. Extract Facet's
session block (§5); Facet refactors onto it (behavior-identical). Green: Facet login/refresh/logout unchanged
end-to-end on the kit; the kit's tests cover fence, cookie, refresh-grace, resolution. Depends on: nothing
(P1 parallel-ok)."*

**2–3 · Verified touch-list + precedents (scouted live @ `23a1ad56`):**
- `internal/appsession/` (NEW, the first `internal/` home for session cookies — the two shipped
  implementations both live under `cmd/`: `cmd/facet/session.go`, `cmd/loupe/readauth.go:93-468`).
  `signer.go` = `Signer`/`Mint`/`NewDevSigner(envPrefix, loopback)` + `Truthy`/`IsLoopbackHost`/`HostOf`
  lifted verbatim from `cmd/facet/claim.go:59-135`; `personas.go` = `Persona`/`ParsePersonas`
  (`session.go:70-106`); `session.go` = `Manager` + the seven handlers, `RequireSession`, cookie issue/clear,
  `Identity`/`ViaCookie`/`WithSession` ctx accessors, `NewAuthenticators` (`session.go:537-558`).
  Wiring precedent for the env-prefix seam: `internal/controlauth/wire_actor_verifier.go:42-86`.
- `cmd/facet/session.go` — **deleted** (whole file moves); `cmd/facet/claim.go:59-135` loses
  `devSigner`/`mint`/`setupDevSigner`/`isTruthy`/`isLoopbackHost`/`hostOf`.
- Call-site rewires: `server.go:29-106` (struct fields `authn`/`refreshAuthn`/`loopback`/`personas` collapse
  into one `session *appsession.Manager`; `registerRoutes` delegates the six session routes), `main.go:131-217`,
  `claim.go:158-190`, `credentials.go:115-351` (7 sites), `staff.go:180-191`, `browserengine.go:136-144`,
  `enginemanager.go:26,120-125`.
- Tests: `cmd/facet/session_test.go` (534 lines, 29 tests) moves to `internal/appsession/session_test.go`
  against a `Manager`; `credentials_test.go:18-31` + `browserengine_test.go` switch to
  `appsession.WithSession`; `claim_test.go:24-32`'s `testDevSigner` builds an `appsession.Signer`.

**4 · Increments + green script:** (1) the kit + its moved tests → `go test ./internal/appsession/...`;
(2) Facet refactor + test rewires → `go test ./cmd/facet/...`. Both land in ONE commit (Facet does not
compile in between). Gates: `go build ./...`, `make vet`, `golangci-lint run ./...`,
`STRICT=1 go run ./scripts/lint-conventions.go`, all `scripts/lint-*.go`. Live: cycle `bin/facet`, then
`curl` whoami → login-options → dev-login (cookie) → whoami → session/refresh → logout, plus one browser load.

**5 · In-scope gotchas (behavior-identical is the bar):** cookie name stays exactly `facet_session` and every
route path stays exactly as shipped — `web/login.html:144,153`, `web/app.js:344,421,521` and `web/boot.mjs:150,216`
hard-code them (§5's "`POST /api/login`" is prose; the shipped name is `/api/dev-login`). Preserve: present-but-invalid
cookie **fails closed** while an absent cookie falls back to the boot identity (`session.go:212-225`); the persona
fence applied **twice** — at the typed credential and again at the whoami-resolved identity (`:325,357`);
credential-binding resolve **fails OPEN** (`:346-352`); logout purges the local mirror only when the cookie's
subject differs from the boot identity (`:430`); refresh returns the raw token **and** re-sets the cookie
(`:523-524`) and never re-runs resolution; `Secure: !loopback`. `/api/claim` is Facet-only exempt → injected,
not hard-coded. P5/P2 clean: the kit's only outbound call is the Gateway's own `/v1/actor` door.

**6 · Adjacent finds (filed pre-build):** the four vertical apps + Loupe each carry a verbatim copy of
`devSigner`/`mint`/`isTruthy`/`isLoopbackHost` — de-duplicated onto this kit by W1–W4, no new row needed
(§8 already scopes it). None of the four validate the dev-token subject is a NanoID before minting, and
wellness/café read the body uncapped (`readauth.go:132`/`:129`) — both close when they adopt the kit.

**7 · Non-goals:** the four vertical apps do **not** adopt the kit here (W1–W4); no route renames; no shared
default login page — each vertical's login UX is its own fire's FE work (§7: "a dedicated vertical adds UX,
never capability"), so the kit takes the page as an injected asset; no Loupe operator-session change; no
production-IdP branch (§5 keeps verify-only, F4 defers OIDC); no `/v1/actor` change (P1 shipped it).

**Scope-diff gate: PASS** — every touch traces to "extract Facet's session block + Facet refactors onto it";
the green bar is unchanged, not widened; "depends on nothing" re-verified (the kit reads P1's `/v1/actor`
only through the two fields it already decoded before P1).

**As-built (2026-07-24, `a2e71712`):** shipped per brief, all gates green. Route paths, cookie name,
status codes, guard order, timeouts, body caps and both asymmetries preserved verbatim (a line-by-line
parity review against `6e12cce1` found no behavioral delta). Deviations, each deliberate: §5's
"`POST /api/login`" stayed the shipped `/api/dev-login` (the FE assets address it by literal); no shared
default login page — the kit takes the page as injected bytes, since each vertical's login UX belongs to
its own fire (§7); `Config.EnvPrefix` was added so the operator-facing "disabled" message keeps naming
`FACET_DEV_AUTH` (`web/login.html` renders `body.error` verbatim). Hardened past the original: the refresh
endpoint no longer assumes a Signer exists whenever a refresh verifier does — the doc'd verify-only
production posture would have panicked — and a mux-level test now proves Facet's `/api/claim` exemption is
wired, not merely that the kit honors an exemption list. New coverage the original lacked: the
credential→identity resolution (bound-credential success, resolved identity refused by the fence, resolve
failure failing open). **Live-verified** on the running stack: `bin/facet` cycled per the `up-facet` recipe;
whoami→login-options→dev-login (HttpOnly cookie)→whoami→refresh (rotated token)→logout→signed-out all
correct, plus 401 on a gated API, 302→`/login` on browser nav, and `/api/claim` reaching its handler
unauthenticated. **Residual filed:** the kit has no production verify-only branch or revocation checker,
which loftspace/clinic's read boundaries already wire — a lattice-lane row, consumer W1/W2.

### Fire W1 fire brief (build note, 2026-07-24)

**1 · Scope sentence (verbatim §8):** *"Fires W1–W4 `[verticals]` — clinic / loftspace / wellness / café
reworks per §7, one fire each, any order. Green (each): sign-in-first; pickers + both mints deleted; every
hat's offered op set == the same identity's Facet catalog (§6.4 script); RLS tests keep passing with session
subjects; café/wellness read boundaries authenticated. Depend on: P1+P2; W1–W3 also on W0."* — clinic's §7.1
hats: patient (self-book/cancel, My Appointments), front-desk (book-for-anyone, Schedule, Follow-ups,
Availability, Sites), provider (My Schedule via own binding).

**Split (W1 is L; §4 multi-fire).** **Inc 1 (this fire) — the session spine:** sign-in-first, both mints
deleted, every read and write carries the verified session subject, `asSelf` becomes *derived* rather than
chosen. **Inc 2 — the hats:** whoami `roles[]`/`anchors[]`-driven surface gating, the provider hat on its own
`identifiedBy` binding, the front-desk picker split (actor→data selection), and the §6.4 op-set parity audit
against Facet's catalog. Inc 1 is independently shippable and green: it is the whole parity invariant §6.1–6.3
(no foreign-subject mint, session-keyed read boundary); §6.4 (offered ⊆ discovered) is Inc 2's bar.

**2 · Verified touch-list (scouted live @ `e04ff757`):**
- **Blocker, resolved in-fire (not bounced):** `internal/appsession.NewAuthenticators` is dev-mode-only —
  `signer.go:91-94` hardcodes `KeySourceConfig{DevMode: true}` and `:107` passes `nil` as the revocation-checker
  slot. Clinic ships **both** things that would drop: a pinned-issuer production branch
  (`readauth.go:137-160`, `_JWT_PUBLIC_KEY` + required `_JWT_ISSUER` + `_KID` + `_AUDIENCE`) and a real
  revocation checker (`main.go:156-159`, `revocation.New(revKV)`). Adopting the kit as-is is a security
  regression, so the kit gains the production branch + a `revocationChecker` parameter **in this fire** — five
  lines mirroring clinic's own shipped code, the §2 "small and mirrors an established pattern" case, not a new
  mechanism. Closes the filed lattice row (`lattice.md:144`, consumer named "W1/W2").
- **Mints deleted:** `readauth.go:228-263` `handleDevToken` (**any-subject** — subject straight from the request
  body at `:242-254`, no caller identity at all) and `readauth.go:274-301` `handleStaffDevToken` (**fixed
  root-equivalent** — `bootstrap.BootstrapIdentityKey` via `main.go:98`, unauthenticated, no body, no test).
  Routes `server.go:78,83`.
- **Superseded by the kit:** `devSigner`/`mint` (`readauth.go:69-94`), `setupReadAuth` (`:100-161`),
  `parsePublicKeyPEM` (`:164-174`), `isTruthy` (`:304-310`), `isLoopbackHost`/`hostOf` (`main.go:274-295`),
  `devTokenTTL` (`:62`).
- **Read boundary:** `authenticateRead` (`readauth.go:190-223`) keeps its shape but sources the subject from
  `appsession.Identity(ctx)` instead of the `Authorization` header (`:194-197`); the credential-binding
  resolve it did per-request (`:211-221`) is now done once at login by the kit (`session.go:394`). The five RLS
  `set_config` call sites are untouched: `appointments.go:197,292`, `visitseries.go:54`, `patients.go:62`,
  `ledger.go:130`.
- **Wiring:** `main.go:131-217` (kit construction, mirroring `cmd/facet/main.go:136-151,209-240`),
  `server.go:26-85` (inner mux + `RequireSession`, mirroring `cmd/facet/server.go:63-88`).
- **New:** `cmd/clinic-app/web/login.html` — clinic-branded, mechanism copied from `cmd/facet/web/login.html`
  (whoami bounce · `/api/login-options` · `POST /api/dev-login`), **claim form dropped** (Facet-only ceremony).
- **FE (`web/app.js`, 4113 lines):** the four token caches collapse to one session token —
  `readTokenCache:82`/`readToken:91`, `providerTokenCache:134`/`providerReadToken:136`,
  `selfTokenCache:191`/`selfWriteToken:206`, `staffTokenCache:379`/`staffReadToken:381`, plus the whole claim
  ceremony (`:227-352`, `mintDeviceToken:265`, `postOpAsSubject:280`, `ensureClaimedDevice:299`,
  `runClaimCeremony:311`). `submitOp:471-493` takes the session token; `authedGet`/`authedGetAsProvider`/
  `authedGetAsStaff` (`:114,159,398`) collapse to one cookie-authenticated getter.
- **Tests:** six RLS files present a session cookie instead of a Bearer header —
  `appointments_rls_test.go:180-262`, `provider_schedule_rls_test.go:117-178`,
  `staff_appointments_rls_test.go:106-155`, `staff_patients_rls_test.go:96-175`, `visitseries_test.go:95-185`,
  `ledger_rls_test.go:86-137`; `readauth_test.go` loses the three `handleDevToken` tests (`:401,411,438`) and
  its `setupReadAuth`/`isTruthy` coverage (now the kit's).

**3 · Precedents to mirror:** `cmd/facet` end-to-end — it is the kit's only current consumer and shipped
this exact shape yesterday (`a2e71712`): construction `main.go:209-234`, inner-mux delegation
`server.go:63-88`, the `Identity`+`ViaCookie` handler pattern `credentials.go:116-132`, and the browser's
write-token-from-refresh path `web/boot.mjs:150`. The kit's production branch mirrors clinic's own
`readauth.go:137-160` verbatim, not a new design.

**4 · Increment order + green checks:** (1) kit production branch + revocation param → `go test
./internal/appsession/... ./cmd/facet/...`; (2) clinic server wiring + mint deletion + test rewires →
`go test ./cmd/clinic-app/...`; (3) login page + FE collapse → `node --check web/app.js`, live curl.
Gates: `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`,
all `scripts/lint-*.go`. Live: cycle `bin/clinic-app` per the Makefile recipe, then
whoami → login-options → dev-login → gated read → refresh → logout, plus one browser load.

**5 · In-scope gotchas:** the cookie name is derived (`<AppName>_session` → `clinic-app_session`), so the
`AppName` string is load-bearing, not cosmetic. `IsAuthExempt` is **exact-match only** (`session.go:168`) — the
SPA's static assets sit under `/` and are reachable only *after* a session exists, which is correct but means
`/api/config` (`server.go:89`) becomes gated; the login page must therefore be self-contained (Facet's is).
`resolve` **fails closed** on a present-but-invalid cookie and falls back to `FallbackIdentityID` only when the
cookie is **absent** (`session.go:225-232`) — clinic sets no fallback, so anonymous is genuinely anonymous.
A bare `:7799` bind yields host `""` → **not** loopback → dev auth refused (`signer.go:121`); the Makefile binds
`localhost` (`Makefile:719,1226`). `asSelf` stops being a checkbox and becomes derived
(`sessionIdentity == identityKeyForPatient(selected)`) — the `#book-self`/`#appts-self` markup
(`index.html:43,156`) and its enable/disable sync (`app.js:662-688`) go with it. `CLINIC_APP_DEMO_PERSONAS`
is left unset (free-form dev sign-in by identity id, exactly Facet's dev-loop posture) — the fence is
available, not mandatory.

**6 · Adjacent finds:** `/api/staff/dev-token` had **no test at all** — an unauthenticated root-equivalent
mint on every clinic dev stack; deleted here, noted because the same shape exists in the three sibling apps
(loftspace/wellness/café) and dies with W2–W4, already scoped by §8, so no new row. The ungated reads
(`/api/providers`, `/api/sites`, `/api/provider-sites`, `/api/residents`, `/api/appointments`,
`/api/wellness/sessions` — `server.go:69-82`) become session-gated as a side effect of `RequireSession`;
that is a tightening, and none of them is reachable pre-login by design.

**7 · Non-goals (Inc 1):** no role/hat-driven surface gating (Inc 2); no provider `identifiedBy` rework —
the Schedule tab's provider dropdown stays a data-selection filter for now (Inc 2 splits it); no grants
audit / §6.4 parity script (Inc 2); no `lint-conventions` parity gate flip (§8(e), after W1–W4); no changes to
the five RLS `set_config` call sites or any package/DDL content; no sibling-app adoption (W2–W4); no contract
text.

**Scope-diff gate: PASS** — every touch traces to "sign-in-first; pickers + both mints deleted; RLS tests keep
passing with session subjects". One narrowing recorded (hats deferred to Inc 2, so §6.4 op-set parity is not
this increment's bar) and one in-fire enabler recorded (the kit's production branch — required for the
adoption to not regress security, mirrors clinic's own shipped code, closes an already-filed row rather than
widening scope). Dependencies re-verified both ways: P1 shipped (`/v1/actor` roles+anchors — Inc 2 consumes
them, Inc 1 does not), P2 shipped (`a2e71712`), W0 shipped (`a8069d16`).

**As-built — Inc 1 + Inc 1b SHIPPED (2026-07-24, merged to `main`).** Clinic is sign-in-first: a person
signs in at `/login`, and every read and write is keyed on that verified session. Both dev-token mints are
gone — the any-subject one (subject straight from the request body, no caller auth) and the unauthenticated
root-equivalent one. The FE's four token caches and the device-claim ceremony collapse onto one session
token; `asSelf` is derived from the signed-in identity rather than declared by a checkbox. The kit gained
its production verify-only branch + revocation checker (closing the filed lattice row; Facet passes nil,
unchanged). `adminActor` retired to `bootstrapLoaded`, removing the last `BootstrapIdentityKey` reference
from `cmd/clinic-app` that §6's parity gate will reject.

*Inc 1b — the bridge the reviews caught.* `clinicPatientReadGrants` makes the **patient vertex** its own
actor, which only ever worked while a token could be minted with that NanoID as its subject; moving the RLS
principal to the session identity left a real patient reading nothing.
**`patientIdentityReadGrants`** mirrors W0's provider producer (identity actor → patient anchor, its own
`cap-read.patient.clinic` source so neither producer's diff retracts the other's) with **no role
predicate** — being the person a record is about is what `identifiedBy` asserts, and a patient not yet
granted `consumer` still owns their own record. `clinicPatientsRead` now anchors per-patient instead of
projecting an empty anchor set, so a patient session can find its own roster row; decrypted contact
therefore reaches the wildcard holder and the person the row is about, and nobody else. The FE narrows
`my-appointments`/`my-visit-series` to the patient on screen — unnarrowed, a wildcard front-desk session
made the slot picker treat every appointment in the practice as blocking.

*Test lesson, pinned:* the suite stayed green across a dead read path because its RLS cases use the patient
NanoID **as** the session subject, collapsing the two ids the bridge exists to keep apart. Inc 1b adds a case
whose subject is a distinct identity, plus `TestPatientIdentityReadGrants` proving the cypher itself against
a real graph the way its provider sibling already was. The first version of that RLS case passed while
hand-seeding its own grant row — it proved the RLS mechanism, not the producer; that is why the fixture test
exists alongside it.

*Carried into Inc 2 (reviews, non-blocking, ranked):* the kit can only ever *set* a cookie via its own
minter, so the production verify-only posture it just gained is unreachable end-to-end — an external token
has no way in, and the FE's write path 404s on `/api/session/refresh` with a message naming the wrong cause;
the FE has no proactive refresh loop, so a browse-only session hard-lapses at 30 min mid-work (Facet's
`boot.mjs` refresher is the pattern); five plain `api()` reads never detect a lapse and degrade to silent
empties, one of which makes the slot picker offer already-taken times; `asSelf` is computed once per render
from `state.patient` but applied per-row, and the per-row form `actingAsSelf(patientKey)` already exists
unused; a failed whoami is terminal and un-retried, leaving a patient rendered as staff with sign-out
hidden. **Deployment note, pre-existing but now load-bearing for the whole
app rather than six reads:** `loopback` derives from the *bind* address, so a reverse proxy to `127.0.0.1`
(the shape the hosted demo box already runs) both permits the in-process minter and drops the cookie's
`Secure` flag — `CLINIC_APP_DEMO_PERSONAS` must be set before clinic is ever proxied. **Cleanup:**
`clinicPatientReadGrants` (patient-as-actor) is now vestigial for any real session and is the honest tail of
§7.1's "retires entity-as-actor"; it stays until Inc 2 so the A/B RLS cases keep their shape.

**Inc 2 tail — IdP session-boundary hardening SHIPPED (2026-07-24, `6392ea7f`).** Two of the carried-into-Inc-2
kit findings landed on their own (headless, no FE): `_JWT_AUDIENCE` is now `TrimSpace`d like issuer/kid/pemKey
(a padded value no longer makes every token fail `ErrWrongAudience` with no startup signal), and
`parsePublicKeyPEM` now refuses any non-RSA/ECDSA PKIX key at startup with a clear error (the verifier accepts
only RS*/ES*, so an Ed25519 PEM used to boot clean then fail every verification). Both fail closed; each is
proven by a discriminating test in `internal/appsession/signer_test.go`. The remaining carried FE items
(unreachable verify-only posture, refresh 404 wrong-cause, no proactive refresh loop, silent-empty reads,
`asSelf` per-row, terminal whoami) stay under Inc 2b — they need the FE + browser verification.

**Inc 2b session-resilience tail SHIPPED (2026-07-24, `4fe21968`).** Three of the carried FE findings landed
as a self-contained FE fire (no hat-gating, no provider hat — those remain Inc 2b's headline). (1) *No
proactive renewal* — a browse-only session that never submits a write hard-lapsed at the session TTL mid-read;
`cmd/clinic-app/web/app.js` now runs an activity-gated sliding keepalive (a forced `sessionWriteToken()` on a
20-min interval, idle cap 30 min, visibility catch-up) that re-sets the cookie + cached write token in one
round trip, mirroring `cmd/facet/web/boot.mjs`'s `createTokenRefresher`; started only for a real cookie
session (`canSignOut`). (2) *Silent-empty reads* — the five lapse-blind `api()` reads (providers, sites,
provider-sites, the slot-picker appointments, wellness sessions) now go through `appGet`, so a 401 bounces to
`/login` instead of a swallowed empty; the appointments one had made the booking slot picker offer
already-taken times. The residents read stays bare (deliberately tolerant of unreachability — "book without
lease confinement"). (3) *Terminal whoami* — retried on a bounded backoff, so a transient stumble at first
paint no longer strands a real session rendered as staff with sign-out hidden. **Not-a-live-bug (verified,
dropped):** the `asSelf`-per-row finding — the two `actingAsSelf()` sites (book-submit, My-Appointments) are
single-patient by construction (`/api/my-appointments` rows carry no `patientKey`), so the once-per-render
value already equals each row's; the `actingAsSelf(patientKey)` parameter serves a *future* mixed-patient
worklist render, not these. **Still open under Inc 2b:** unreachable verify-only posture (a kit/deployment
concern — the kit can only set a cookie via its own minter) and the refresh-404-wrong-cause item (the kit now
serves `/api/session/refresh` via `RegisterRoutes`, so the 404 itself is gone; any residual message wording is
FE-cosmetic).

**Inc 2b hat-gating (front-desk vs patient) SHIPPED (2026-07-24, `a934fd8b`).** Clinic renders its hats from
the signed-in identity's bindings instead of showing every surface to everyone. Two layers: (1) *the kit
surfaces the hat hints* — `internal/appsession`'s `/api/whoami` now forwards the Gateway `/v1/actor` `roles[]`
(role-derived grant keys) + `anchors[]` (residence/workplace bindings, each relation-stamped) for a real
cookie session; a boot-env fallback carries no token and gates nothing. Soft: any `/v1/actor` failure yields
empty hints, never a whoami failure (mirrors the resolver's degrade-to-empty). Shared groundwork W2–W4 reuse.
(2) *the clinic FE gates* the five front-desk-only tabs (Schedule, Follow-ups, Series, Availability, Sites),
the New-patient control, and the Book tab's cross-links to those tabs on a **`worksAt` anchor** — a patient
session (residesIn or none) sees only Book + My Appointments; `showView` routes any gated view to Book so a
stray cross-link cannot surface staff content. Live-verified in-browser on the showcase stack: a `worksAt`
identity sees all seven tabs + New-patient; a `residesIn`-only patient sees only the two. Gating is UX
curation — every op these views submit stays enforced by its `GrantsTo` + RLS.

**Gating-signal choice — the anchor, not role-name resolution.** The design (§4) frames gating as roles-driven,
but whoami's `roles[]` arrives as **opaque role vertex keys** (`vtx.role.<id>`), not canonical names — and that
is **frozen**: Contract #6 §6 (line 232) specifies `cap.roles.<actor>.roles` = *vertex keys of role vertices*,
consumed by the Processor's FR22 denial builder, so making it emit names is a frozen-contract change, not a
package edit. Gating therefore rides **anchors**, exactly as §4.1 already specifies (`anchors[]` = the
relation-stamped `residesIn` / `worksAt` / **`identifiedBy`-inverse** bindings). Front-desk/patient gates on the
`worksAt` anchor. The **provider hat** gates on the **`identifiedBy` anchor** — a provider has no
`worksAt`/`residesIn` (she `practicesAt` / is `identifiedBy`-bound), so before Inc 2c she projected *no anchors
at all* and was invisible to the hats surface. No role-name resolution, no Lattice-lane item, no frozen-contract
touch — the enabling lens edit is package work, shipped in Inc 2c below.

**Inc 2c — `identityAnchors` projects the `identifiedBy` binding anchor SHIPPED (2026-07-24, `2dbc8232`).**
The shipped `identityAnchors` lens (identity-domain) walked only `residesIn` + `worksAt`, so §4.1's third
binding was missing and every provider identity produced an empty (deleted) anchors doc. Added the untyped
inbound `(identity)<-[:identifiedBy]-(bound)` walk (mirroring the production `edge-manifest` `edgeIdentitySpec`),
stamped `relation: 'identifiedBy'`, carrying `bound.key`. The lens stays domain-agnostic: it reports every
binding (provider/patient/instructor/serviceprovider) by key, leaving hat interpretation to the domain-aware
caller — a provider's display name lives on `.profile.data.fullName`, which the generic lens cannot resolve per
bound-entity type, and a patient binding is a `vtx.patient` key the clinic FE's `vtx.provider` gate skips.
identity-domain 0.6.0→0.7.0; live-applied (diff-apply, no teardown); colocated deterministic full-engine
coverage proves the Osei case (provider-only binding → one `identifiedBy` anchor, no residesIn/worksAt) and
patient-key distinctness. *Live-backfill note:* a lens-spec change reprojects per-actor on the next adjacency
CDC event, so an already-bound provider's anchors doc populates when her binding is next touched (or via a
control-plane `lens reproject`); the provider-hat FE fire that consumes the anchor is the natural trigger.
**Inc 2 provider hat — the provider's own My Schedule SHIPPED (2026-07-24, `a36625a3`).** The clinic FE
gains the third hat. A clinician signed in as an identity BOUND to a provider entity (`BindProviderIdentity`,
§3.2) now sees a **My Schedule** tab — her own day, served by the RLS-scoped `/api/my-schedule`
(`handleMyProviderSchedule`) **as herself**, with no provider picker (the endpoint answers strictly for its
own caller). Read-only; lifecycle transitions stay on the front-desk Schedule and a patient's own My
Appointments. The gate `isProvider()` keys on the `identifiedBy` anchor whose bound key is a
`vtx.provider.*` — **the named consumer of Inc 2c's `identityAnchors` `identifiedBy` walk**, closing the
`/api/my-schedule` zero-FE-callers residual. A `vtx.patient.*` `identifiedBy` binding, `residesIn`, or no
anchor leaves the tab hidden; a provider gets neither the front-desk tabs nor the patient New-patient
control; the hat is additive (a `worksAt`+`identifiedBy` multi-hat sees both surfaces) and losing it bounces
the active view to Book. The stale `app.js` role-name-resolution gating comment is rewritten to the shipped
anchors framing. In-browser verified on the showcase stack: the real gate functions driven with all four
anchor shapes + the real `/api/my-schedule` rendering Osei's two RLS-scoped rows.

*Live-demo dependency (stack freshness, not a defect):* Osei's `/api/whoami` still returns **empty anchors**
on the currently-running showcase stack because its `identityAnchors` lens is **pre-Inc-2c** (every
`identity-anchors` row was projected 07-18/07-20 and carries no `identifiedBy` anchor, even the bound
patient/instructor). The FE degrades correctly (a provider with no anchor sees nothing extra), but for the
provider hat to light up **live** the running stack must be brought to identity-domain 0.7.0 **and** each
bound provider's row reprojected (`lattice lens reproject identityAnchors --actor-key <identityKey>` — the
Inc 2c "natural trigger", needs a control-plane actor holding `ctrl.refractor.reproject`) or the binding
CDC-touched. An ops/`demo-bootstrap` step, tracked with the demo box, not this FE fire.

### Fire W1 Inc 2a fire brief (build note, 2026-07-23)

**1 · Scope sentence (from §7.1 + §7 grants-audit intro):** *"every op the UI offers a hat must carry a real
`GrantsTo` for that hat's role (the staff-wildcard default currently masks the gaps; e.g. staff booking has no
`frontOfHouse` grant on clinic `CreateAppointment`)."* Inc 1 deleted the root-equivalent staff mint
(`handleStaffDevToken`, subject = `bootstrap.BootstrapIdentityKey`), so the clinic "staff" session is now a
genuine `frontOfHouse` identity — and every clinic-domain **front-desk service op** that only ever granted
`operator` is now `AuthDenied`. This increment closes the clinic-domain half of the §6.4 grants audit for the
front-desk *service* surface.

**Live-confirmed break (grounding, not assumption).** Signed in on the running stack as Dana Whitfield
(`noNa5Fc2vrkBojZ2QPAv`, `frontOfHouse` `worksAt` Riverside Building), `POST /v1/operations`:
- `CreateAppointment` → `AuthDenied` `OperationNotPermitted`, `rolesCarryingPermission:["operator","consumer"]`.
- `CreatePatient` → `AuthDenied`, `["operator"]`.
- `CreateUnclaimedIdentity` → `ScriptFailed` (passed authz — identity-domain **already** grants `frontOfHouse`,
  `permissions.go:41`), so the register-patient flow's identity half already works; only its `CreatePatient`
  half (clinic-domain) is missing.

**Decision — service ops vs practice-administration (Winston, §0; consistent with the shipped
`BindProviderIdentity` principle "front-desk cannot create the entity … the grant would add only attack
surface").** Front-desk *serves patients*; the practice *owner* administers the practice. So this fire restores
only the pure front-desk **service** ops, and leaves administration/clinical ops `operator`/`provider`:
- **Restore to `frontOfHouse`:** `CreateAppointment` (book-for-anyone) + `CreatePatient` (register a walk-in).
- **Stay `operator`-only (correct denial, not a gap):** `CreateProvider`/`SetProviderProfile`/`TombstoneProvider`
  (onboarding doctors = practice admin), `SetSiteProfile`/`AssignProviderSite`/`RemoveProviderSite`/
  `CreateLocation` (site configuration), `CreateAccount` (billing setup). **Stay `operator`+`provider`:**
  `SetProviderHours`/`SetProviderTimeOff` (a provider sets their own availability). **Stays `operator`-only,
  belongs to the provider hat later:** `RecordEncounter` (clinical documentation — a clinician act, not
  front-desk; the Inc 2b hat-gating hides the surface). The FE offering these to a front-desk session today is
  the *staff-wildcard residue* Inc 2b's hat-gating removes; the grants correctly say no now.

**2 · Verified touch-list (scouted live @ `eddb06e6`):**
- **`permissions.go`** — `mk("CreateAppointment")` (the `scope=any` operator row, `:92`) becomes an explicit
  spec `GrantsTo:["operator","frontOfHouse"]` (widens the **existing** `scope=any` row — not a second vertex,
  per the permTag-identity note already in this file's header comment); `mk("CreatePatient")` (`:75`) becomes
  `GrantsTo:["operator","frontOfHouse"]`.
- **`ddls.go`** — `CreateAppointment`'s script (`if ot == "CreateAppointment":`, `:2013`) gains a
  workplace-confinement branch right after `require_live_typed(state, provider, …)` (`:2020`):
  `if not workplace_exempt(): require_workplace(sites_for_provider(provider), …)`. A **verbatim mirror** of the
  branch `RescheduleAppointment` (`:2205`) and `SetAppointmentStatus` (`:2338`) already run — same
  `workplace_exempt`/`sites_for_provider`/`require_workplace` helpers, resolved off the **payload** provider
  (validated alive+class=provider just above) since no appointment exists yet. **No** `actor_bound_to_…` third
  binder: a `provider` role holds no `CreateAppointment` grant (providers accept/reschedule their own
  appointments, never originate them), so that branch cannot apply. `CreatePatient` needs **no** script change —
  a patient vertex is practice-wide (no building), so front-desk registration is unconfined, exactly like
  `operator` and like identity-domain's already-shipped `frontOfHouse` `CreateUnclaimedIdentity`.
- **`package.go` + `manifest.yaml`** — version `0.25.0 → 0.26.0` (additive grants; live stacks re-install via
  `refresh-clinic`).
- **`scripts/verify-package-clinic-domain.go`** — `clinicOpGrants` (`:89`): `CreatePatient` +
  `{"any","frontOfHouse"}`, `CreateAppointment` + `{"any","frontOfHouse"}`. Both add a grantee at the
  **existing** `any` scope, so the `len(permIDs)==len(wantScopes)` vertex-count check is unchanged. Also
  reconcile the audit's completeness: add the already-shipped `{"any","frontOfHouse"}` to `RescheduleAppointment`
  + `SetAppointmentStatus` rows (the map asserts a subset today and silently omits them).
- **`package_test.go`** — grant matrix (`:185-186` already list `frontOfHouse` on Reschedule/SetStatus); add it
  to `CreateAppointment` + `CreatePatient` rows.
- **NEW `frontdesk_confinement_test.go`** — the durable security proof, mirroring
  `cafe-domain/workplace_confinement_test.go`: a `frontOfHouse` actor `worksAt` building A, provider PA
  `practicesAt` A, provider PB `practicesAt` B, one patient. Vectors: front-desk `CreateAppointment` with PA =
  **Accepted**; with PB = **Rejected** (the multi-org gate); front-desk `CreatePatient` = **Accepted**
  (unconfined); operator with either provider = **Accepted** (unconfined). Harness: `setupClinicEnv` helpers
  (`clSeedVertex`/`clSeedLink`/`SeedCapDoc`), a local `submitAs(actorKey,…)` (the default `clSubmit` hardcodes
  the operator actor). `actor_holds_operator` reads the holdsRole link, so the front-desk actor — cap-doc
  `Roles:["frontOfHouse"]`, no operator holdsRole — is confined exactly as cafe's is.

**3 · Precedents to mirror:** `RescheduleAppointment`/`SetAppointmentStatus` confinement branches
(`ddls.go:2203-2209,2336-2342`) verbatim; `cafe-domain/workplace_confinement_test.go` for the test harness;
identity-domain `permissions.go:41` (`frontOfHouse` on `CreateUnclaimedIdentity`) for the unconfined-registration
precedent; the `permTag`-identity header comment already in `permissions.go` for why a second `frontOfHouse`
grant widens the existing `scope=any` row rather than minting a second vertex.

**4 · Increment order + green checks:** (1) permissions.go + ddls.go confinement → `go test
./packages/clinic-domain/...` (existing suite submits as operator = workplace-exempt, so no regression); (2)
new confinement test + package_test/verify-map updates → same + `go run ./scripts/lint-conventions.go`; (3)
version bump. Gates: `go build ./...`, `make vet`, `golangci-lint run ./...`,
`STRICT=1 go run ./scripts/lint-conventions.go`, all `scripts/lint-*.go`, `make verify-package-clinic-domain`
(needs the live stack). Live: `refresh-clinic`, then re-run the Dana probes — book at own building Accepted,
book with an off-site provider Rejected, register a patient Accepted.

**5 · In-scope gotchas:** `workplace_exempt()` returns true when `authContextTarget != ""` (consumer self-book)
**or** `actor_holds_operator` — so the new branch fires **only** for a non-operator, non-self actor
(front-desk), never touching the operator or patient-self paths. `sites_for_provider(provider)` returns `[]`
for a provider with no `practicesAt` link → `require_workplace([],…)` fails closed → front-desk cannot book an
unassigned provider (only operator can); this matches Reschedule/SetStatus exactly and is the safe direction.
`CreatePatient`'s grant is unconfined by design — do **not** add a workplace branch to it (there is no location
to confine to).

**6 · Adjacent finds (filed now):** `StartVisitSeries` (the Follow-ups tab) lives in the **separate**
`clinic-reminders` package (its own version + confinement helpers) and is `operator`-only — a `frontOfHouse`
follow-up-series grant is the same audit, filed as its own row (consumer: front-desk Follow-ups tab) rather
than folded here, to keep this a single-package capability change. `CreateAccount` (ledger) is left
`operator`-only pending a product call on whether front-desk opens billing accounts (not a live-broken *service*
workflow — booking/registration are).

**7 · Non-goals (Inc 2a):** no FE change (the FE already submits `CreateAppointment`/`CreatePatient` with no
`authContext` on the staff path — it only needed the grant to exist); no hat-driven surface gating (Inc 2b); no
provider hat / `/api/my-schedule` consumer (Inc 2b); no `StartVisitSeries` grant (filed); no `RecordEncounter`
grant (provider-hat/operator); no contract text; no changes to the five RLS `set_config` sites or any lens.

**Scope-diff gate: PASS** — both grants trace to §7.1's front-desk service hats (book-for-anyone, register) and
the §7 grants-audit intro's named `CreateAppointment` example. One narrowing recorded (`StartVisitSeries` filed
not folded — separate package) and one in-fire security addition recorded (workplace confinement on the new
front-desk booking grant — required so the grant does not over-widen past the staff-worlds §3.5 invariant,
mirrors the sibling ops, not a new mechanism). Dependencies re-verified: identity-domain front-desk grants
already shipped (`permissions.go:41`), so register-patient is fully restorable within clinic-domain.

**As-built — Inc 2a SHIPPED (2026-07-23, merged to `main`).** clinic-domain 0.25.0→0.26.0 grants `frontOfHouse`
`CreateAppointment` (scope=any, workplace-confined) + `CreatePatient` (unconfined). A signed-in front-desk
session can again book and register; the confinement is proven live (Dana books at her building = accepted, an
off-site provider = rejected, registers a patient = accepted) and by `frontdesk_confinement_test.go`.

*Deviations from the brief (mid-build residuals, recorded per the process):*
- **Two sibling test fixtures needed the holdsRole link.** The new `CreateAppointment` workplace guard reads the
  holdsRole *graph link* to decide root (`actor_holds_operator`), not the cap-doc Roles. `clinic-reminders` and
  `clinic-ledger` fixtures seeded the operator cap-doc role WITHOUT the link (a shortcut that worked only while
  `CreateAppointment` had no link-reading guard), so both reddened; each gains `SeedHoldsRole(...operator)` —
  making the fixture realistic (in production the cap-doc role is projected FROM that link), not adding
  authority. The brief should have anticipated this (a brief-quality miss: adding a guard to a widely-driven op
  exposes every fixture that drives it as a bare-cap-doc operator).
- **The confinement guard was forgeable as first written — the adversarial review caught it (CONFIRMED,
  high-severity), and the root-cause fix landed in the same fire.** Step 3 authorizes a scope=**any** grant
  WITHOUT inspecting `authContext.target` (`step3_auth_capability.go` `matchPlatformPermission` "any" case), and
  the Gateway forwards the client's `authContext` verbatim (`gateway.go:753`) into `op.authContextTarget`
  (`starlark_runner.go:432`). Both `workplace_exempt()` AND `require_workplace()` keyed their self-exemption on
  `authContextTarget != ""`, so a front-desk actor holding the new scope=any booking grant could attach any
  target and skip confinement (book cross-building). Fixed by keying the exemption on `authContextTarget ==
  op.actor` in **both** functions — the genuine scope=self path always carries `target == actor` (step 3
  *requires* it for scope=self), so equality admits exactly that path and nothing a scope=any caller can
  manufacture; a scope=any caller setting `target == its own actor` gains nothing, since the op's own
  identifiedBy check then binds the patient to the caller's identity (the legitimate self-book). This also closes
  the **pre-existing** identical bypass on `RescheduleAppointment`/`SetAppointmentStatus` (same shared helpers).
  `frontdesk_confinement_test.go` gains `TestFrontDesk_ForgedTargetCannotSkipConfinement` (both forgery shapes
  rejected); it accepted-then-rejected across the two-location fix, so it discriminates the vulnerable state.

*Adjacent find, filed (cross-package security):* the `authContextTarget != ""` self/workplace-exemption pattern
is duplicated in **cafe-domain, wellness-domain, maintenance-domain, lease-signing** — cafe's is the same
workplace multi-org gate and is exploitable by the same mechanism; the others use it for self-service checks that
need per-op exploitability verification. The **root enabler is platform-level** (scope=any authorization forwards
an unvalidated `authContext.target` to scripts), so the clean fix may be one processor change (zero/ignore
`authContext.target` when authorization did not go through a scope=self grant) rather than per-package edits.
Filed to `lattice.md` as a security row; clinic is fixed here to make its own new grant sound.

### Fire W2 (LoftSpace) build note

**Inc 1 — applicant-hat grants audit SHIPPED (2026-07-24, `02be1f86`).** The §7.2 grants audit begins on the
applicant hat. `SetApplicantProfile` and `WithdrawLeaseApplication` are self-service applicant ops but carried
only an `operator` grant, so the loftspace FE submitted them via the trusted-tool admin mint
(`submitOp("staff", …)`) — the dormant-grant failure mode ([[feedback_scoped_grant_dormant_if_write_uses_wildcard_actor]]:
a scope=self capability masked by a wildcard-actor write). `CreateLeaseApplication` already modelled the fix; these
were the two remaining applicant gaps. Each op gains a `consumer` scope=self grant (lease-signing 0.23.0→0.24.0) +
an in-script owner guard, and its FE submit flips to `submitOp("applicant", …, {authContext:{target: state.applicant}})`
(coupling the grant with the per-actor write, so it is not left dormant). Withdraw mirrors CreateLeaseApplication's
`authContextTarget == applicant` guard verbatim (the applicant is already verified as the app's `applicationFor`
endpoint); SetApplicantProfile derives the `applicationFor` link key from the actor's own id (no forgeable payload
applicant) and rejects an absent link. The operator scope=any path is unchanged (the guard is a no-op when
authContext is absent). Full adversarial review (opus) traced the guards against step-3 `authContext.target ==
actor` and found no bypass/regression (keying on "authContext present" only further-restricts a scope=any caller,
never escalates — consistent with the platform-level `lattice.md` security row on scope=any target forwarding).
**Next (Inc 2):** the sign-in-first FE flip (delete the applicant picker + any-subject mint, whoami-driven hats) +
the landlord submit migration off the admin mint (a landlord-role decision the design flags as possibly
contract-ish — resolve or flag in-fire).

#### Fire W2 Inc 2 fire brief (build note, 2026-07-24)

**1 · Scope sentence (verbatim §8, clinic-W1's own split applied):** *"sign-in-first; pickers + both mints
deleted; RLS tests keep passing with session subjects"* — LoftSpace's §7.2 hats: applicant/resident, landlord
(existing RLS world), staff. **Inc 2 = the session spine only** (clinic W1 Inc 1's bar verbatim: parity
invariant §6.1–6.3 — no foreign-subject mint, session-keyed read boundary). §6.4 (offered ⊆ discovered) and the
landlord *write* migration are Inc 3's bar, for the reason clinic hit: the grants audit is its own increment
(clinic shipped it as Inc 2a, `1e8dc41b`).

**2 · The landlord-role fork — RESOLVED IN-FIRE (Winston, not contract-ish; no Andrew gate).** §7.2 left open
whether the landlord decision ops get "a landlord-scoped path or an explicit landlord role". **Decided: NO new
role — a `consumer` scope=self grant + an in-script guard that walks the acting identity's `manages` link**,
mirroring Inc 1's applicant guards keyed off `manages` instead of `applicationFor`. Grounds:
- The **read** side already scopes a landlord with **no role at all** — `landlordLeaseApplicationsRead` bakes the
  managing landlord's NanoID into each row's `authz_anchors` by walking `manages`
  (`lease-signing/lenses.go:151-153,217-254`), and the primordial cap-read self-grant does the rest;
  `lenses.go:137-146` states outright that the residence audience needs no grant lens. A role would add nothing
  the write path cannot get the same way.
- A landlord **is already** a `vtx.identity`; `consumer` is the generic signed-in-human self-service role. The
  `provider` role existed only because a provider was *not* an identity at all (§1) — the reason does not
  transfer.
- A new role costs identity-domain + `personalLensPermissions` (`control-authz/permissions.go:56-67`, pinned by
  the `control-authz` test) + every cross-package install harness — the `provider` precedent (`626763bc`) touched
  47 files. That is the "possibly contract-ish" weight §7.2 flagged, bought for no authorization gain.
- Guard shape is further-restricting only (no-op when `op.authContextTarget == ""`), so the `operator` /
  `frontOfHouse` scope=any path is untouched — the same property Inc 1's review cleared.
Deferred to Inc 3 with its consumer named: the grant rows + guards + FE flip for `DecideLeaseApplication`,
`SetRenewalTerms`, `VerifyGuarantor`, `CancelRenewal`, `SetListingStatus`.

**3 · Verified touch-list (scouted live @ `cf95b5e5`):**
- **Mints deleted:** `readauth.go:224-262` `handleDevToken` (**any-subject** — subject straight from the request
  body at `:248`) and `readauth.go:264-300` `handleStaffDevToken` (**fixed root-equivalent** —
  `bootstrap.BootstrapIdentityKey` via `main.go:95`, unauthenticated, no body). Routes `server.go:85-86`.
- **Superseded by the kit:** `devSigner`/`mint` (`readauth.go:63-93`), `setupReadAuth` (`:95-160`),
  `parsePublicKeyPEM` (`:162-173`), `bearerToken` (`:175-184`), `isTruthy` (`:302-309`), `devTokenTTL` (`:61`),
  `credentialBindingResolver` + its per-request resolve (`:32-34,210-220` — the kit resolves the binding **once
  at login**, `session.go:394`), `hostOf`/`isLoopbackHost` (`main.go:279-302`).
- **Read boundary:** `authenticateRead` (`readauth.go:186-222`) keeps its signature and sources the subject from
  `appsession.Identity(ctx)`. **All twelve read handlers already funnel through it** — `applications.go:198`,
  `credentials.go:93`, `landlord_applications.go:235`, `lease_document.go:49`, `objects.go:301,381,573`,
  `portfolio.go:235`, `renewals.go:110`, `search.go:292`, `staff_identities.go:92`,
  `tasks.go:94`, `unit_applications.go:201` — so one function change moves the whole boundary.
- **The one admin-standing-in-for-a-user read (§6.3):** `unit_applications.go:270` decorates the landlord console
  with applicant names read as `s.adminActorID()` (the WildcardAnchor holder). It reads as the **session** actor
  now; a landlord with no roster grant degrades to bare keys down the path already there (`:277-279`).
  `adminActor` then retires to `bootstrapLoaded` (`health.go:18`, `server.go:28,132-143`, `main.go:90-97`),
  removing the last `BootstrapIdentityKey` reference §6's gate will reject.
- **Wiring:** `main.go:79-199` (kit construction, mirroring `cmd/clinic-app/main.go:176-228`), `server.go:62-88`
  (inner mux + `RequireSession`, mirroring `cmd/clinic-app/server.go:58-85`).
- **New:** `cmd/loftspace-app/web/login.html` — LoftSpace-branded, mechanism copied from
  `cmd/clinic-app/web/login.html` (whoami bounce · `/api/login-options` · `POST /api/dev-login`).
- **FE (`web/app.js`, 4106 lines):** the two token caches collapse to one session token —
  `readTokenCache:209`/`readToken:218`, `staffTokenCache:261`/`staffReadToken:263`; `authedGet:241` +
  `authedGetAsStaff:280` collapse to one cookie-authenticated `appGet`; the whole device-claim ceremony goes
  (`APPLICANT_AUTH_KEY:381`, `pendingClaimSecrets:405`, `mintDeviceToken:419`, `postOpAsSubject:434`,
  `ensureClaimedDevice:460`, `runClaimCeremony:472`). `submitOp:327` drops its `actorKind` parameter.
  `state.applicant` stops being picked (`APPLICANT_KEY:8` localStorage + the `#applicant` select) and becomes the
  signed-in identity; `state.mode` (`MODE_KEY:9`, `applyMode:1147`) stops being a stored toggle and becomes
  hat-gating on the whoami `manages` anchor, mirroring clinic's `applyHatGating` (`clinic web/app.js:3941-3960`).
- **Tests:** the RLS files present a session cookie instead of a Bearer header —
  `applications_rls_test.go`, `landlord_applications_rls_test.go`, `objects_rls_test.go`, `search_rls_test.go`,
  `staff_identities_rls_test.go`; `readauth_test.go` loses its `handleDevToken`/`setupReadAuth`/`isTruthy`
  coverage (now the kit's).

**4 · Precedents to mirror:** `cmd/clinic-app` end-to-end — it shipped this exact conversion three days ago
(`17aecdbf` + `4fe21968`): construction `main.go:176-228`, inner-mux delegation `server.go:58-85`, the
session-sourced `authenticateRead` `readauth.go:42-52`, the FE session block `web/app.js:79-240`
(`appGet`/`sessionWriteToken`/keepalive/`loadWhoami`), and hat gating `web/app.js:3886-3960`. The kit itself
(`internal/appsession`) is unchanged by this fire — clinic already gave it its production branch + revocation
parameter.

**5 · Increment order + green checks:** (1) Go wiring + mint deletion + `adminActor` retirement →
`go test ./cmd/loftspace-app/...`; (2) login page + FE collapse → `node --check web/app.js`; (3) test rewires →
full package. Gates: `go build ./...`, `make vet`, `golangci-lint run ./...`,
`STRICT=1 go run ./scripts/lint-conventions.go`, every `scripts/lint-*.go`. Live: cycle `bin/loftspace-app` per
the Makefile's `up-loftspace` recipe, then whoami → login-options → dev-login → gated read → refresh → logout,
plus one browser load.

**6 · In-scope gotchas:** the cookie name is derived (`loftspace-app_session`), so `AppName` is load-bearing.
`IsAuthExempt` is exact-match only (`session.go:168`), so `/api/config` becomes gated and the login page must
stay self-contained (clinic's is). `resolve` falls back to `FallbackIdentityID` only when the cookie is
**absent** — LoftSpace sets none, so anonymous is genuinely anonymous. The default bind is `127.0.0.1:7788`
(`main.go:64`), already loopback, so dev auth is not refused (`signer.go:121`). `submitOp`'s applicant path
already carries the `isTransientAuthLag` retry — it must survive the collapse, because the claim-race it
covers is replaced by, not removed with, the login-time binding resolve.

**7 · Non-goals (Inc 2):** no landlord/staff **grant** rows or Starlark guards (Inc 3, §2 above); no §6.4
op-set parity script; no `lint-conventions` parity-gate flip (§8(e), after W1–W4); no sibling-app adoption
(W3/W4); no contract text.

**8 · Two in-fire enablers (grounded mid-build; the brief's first draft had ruled both out as non-goals and
was wrong to).** Neither widens the item — each is the thing that makes a mandated deletion non-regressive:
- **`manages` joins the whoami anchor set** (`identity-domain/lenses.go`'s `identityAnchorsSpec`, 0.7.0→0.8.0).
  §4.1 named three relation-stamped bindings; `manages` was not among them, so gating the landlord surface on
  a `manages` anchor — the obvious mirror of clinic's `worksAt` gate — would have been **dead on arrival**, the
  tab hidden for every session forever. One more `OPTIONAL MATCH` + container walk in an existing lens, purely
  additive to `anchors[]` (clinic keys on `worksAt`/`identifiedBy`, Facet reads its own `edgeIdentitySpec`), and
  it is the same link `landlordLeaseApplicationsRead` already bakes into each row's `authz_anchors` — so the tab
  appears exactly when the console behind it has rows. Two discriminating tests (positive: unit + containing
  building resolved; negative: a resident who manages nothing gets no anchor).
- **`POST /api/credentials/link/complete`** (`credentials_link.go`, mirroring `cmd/facet/claim.go` verbatim).
  Completing a credential link means authenticating as a credential the browser does not hold — the one thing
  the any-subject mint was legitimately used for. Deleting that mint without this would have silently dropped a
  shipped capability (the Account tab's "Link another sign-in method"). The ceremony runs server-side: the app
  mints a throwaway device credential of its own choosing, and the identity being linked to is read from the
  **session**, never the request body, so a caller cannot link a credential onto someone else's account.
The new-applicant flow needed no such enabler: under sign-in-first, creating an identity no longer means
claiming it into *this* browser, so the auto-claim is dropped and the claim key is surfaced once instead — a
correction, not a regression.

**As-built — Inc 2 SHIPPED (2026-07-24, `13c01922`).** LoftSpace is sign-in-first. Both mints are gone, and
deleting them closed a real escalation on every loopback dev box: `AssignUnitOwner` is scope=any/`operator`
and its script does not check `payload.landlord` against `op.actor`, so any browser reaching `:7788` could
mint the root-equivalent actor and make itself landlord of any unit. `cmd/loftspace-app` now holds no
`BootstrapIdentityKey` reference at all — §6's parity gate can flip blocking for this app.

*Review (3 adversarial lenses; opus on the auth plane).* The auth lens **confirmed** claims §6.2 and §6.3 —
all twelve read call sites key on the session subject, no residual subject-naming surface, link forgery
refuted, `ContextHint` byte-identical to the shipped precedent — and found two HIGH gaps that were **the
absence of tests, not of behavior**: the new ceremony shipped untested, and *nothing* asserted that
`registerRoutes` puts routes behind `RequireSession` (protection had moved from per-handler to wiring, and
every ported negative test wraps only its own handler, so a route registered outside the guard would fail no
test). Both are now covered and both new suites were **mutation-verified** — a route moved to the outer mux
and a caller-supplied `targetIdentityKey` each make them fail. It also caught the `ViaCookie` guard missing on
both credential surfaces, dormant only because this app sets no `FallbackIdentityID` — confinement resting on
an optional precondition, the exact pattern this codebase has been bitten by before. The FE lens caught
`init()` gating every listener behind a whoami round trip (a painted surface silently swallowing clicks, a
divergence from clinic's own ordering) and the claim secret being rendered into the shared auto-hiding toast,
where any later toast would destroy the single existing copy of a key with no recovery path. The lens review
came back clean, and independently **refuted** the "silent compile-error fallback" worry the brief raised:
Refractor registers only the `full` engine and `dispatchSpec` logs-and-drops a bad spec, so a broken walk
would yield zero rows for the whole lens and a loud error — never a quiet partial projection.

*Live verification (running stack, `bin/loftspace-app` + `bin/loupe` rebuilt from `main` and cycled).* Both
mint routes now 401 rather than minting; anonymous is refused at every `/api/*` route AND at the SPA shell,
while `/login` and `/api/whoami` stay reachable; sign-in yields a session whose subject the Gateway sees as
the actor on the refreshed bearer; a landlord reads 1 unit and a non-landlord reads 0 through the same
endpoint; logout returns the boundary to 401. In-browser: `/` bounces to the LoftSpace login page, and the
signed-in surface renders with no applicant picker and no landlord toggle.

*One gap live verification exposed, filed:* the `manages` walk is correct and tested, and the 0.8.0 package
installed cleanly — but **adding a walk to an actorAggregate lens does not backfill rows already stored**.
`identityAnchors` refreshes an actor only when a CDC event next touches it, and the ops that would touch it
no-op when state already matches (`AssignUnitOwner` emits no mutation for an existing link). The
`reproject`/`rebuild` control verbs exist and `identityAnchors` does register a Reprojector, but both require
an asserted control-plane actor, so there is no operator-reachable backfill. Consequence on the running demo
stack: a seeded landlord still projects `residesIn`/`worksAt`/`identifiedBy` only, so the Landlord tab stays
hidden for them until something unrelated touches that identity. The FE gate itself was verified against the
live binary by injecting a `manages` anchor in-page — toggle, mode bar and New-applicant all unhide, and
`isLandlord()` flips — so what remains is projection freshness, not gate logic. Not worked around by
mutating demo ownership state: the remove-and-re-assign that would have forced a reprojection was correctly
gated as a live write and left undone.

*Honest residuals, filed as rows in the same commit:* the production (IdP) posture cannot open a session at
all — `setCookie` runs only under a non-nil `Signer`, so with an external IdP nothing can issue the cookie;
it fails closed (401 everywhere), but the documented posture is unreachable, and the per-request
credential→identity resolution `authenticateRead` used to do now happens only at dev-login. **Still open, and
now Designer-first**: the IdP→cookie handoff shape is an architectural fork, and it additionally needs a
per-path exemption at the origin gate. The CSRF half — cookies are domain- not port-scoped, so the sibling
localhost apps are same-site — is **CLOSED** by the `RequireSession` cross-origin gate (`8e61174f`;
`docs/components/appsession.md`). Both were `internal/appsession` properties inherited from P2, affecting
Facet and clinic identically; neither was introduced here.

**Scope-diff gate: PASS** — every touch traces to "sign-in-first; pickers + both mints deleted; RLS tests keep
passing with session subjects". Two narrowings recorded (landlord write-grant migration → Inc 3, matching
clinic's own Inc 1 → Inc 2a split; §6.4 parity not this increment's bar) and one in-fire fork resolution
recorded (§2, no new role). No substitution: the mints deleted are the two the design names, not an adjacent
mechanism. Dependencies re-verified both ways: P1 shipped (`/v1/actor` roles+anchors — the hat gating consumes
`anchors`), P2 shipped (`internal/appsession` present, clinic-hardened), W0 shipped; Inc 1 shipped (`02be1f86`,
`git merge-base --is-ancestor` verified against `main`).

#### Fire W2 Inc 3 fire brief (build note, 2026-07-25)

**1 · Scope sentence (verbatim, Inc 2 §2's deferral):** *"the grant rows + guards + FE flip for
`DecideLeaseApplication`, `SetRenewalTerms`, `VerifyGuarantor`, `CancelRenewal`, `SetListingStatus`"* —
the landlord half of the §7.2 grants audit, closing the hat Inc 2 opened.

**2 · Why this is a FUNCTIONAL fix, not only a hardening.** Inc 2 deleted both mints, so the landlord console
now writes under the signed-in landlord's own token. Four of the five ops are granted `operator` scope=any
**only** (`lease-signing/permissions.go:126,132,144`, `loftspace-domain/permissions.go:20`); the seeded
landlord persona holds `frontOfHouse`, not `operator` (`scripts/seed-showcase.go:550`, ~`:1123` wires its
`manages` link). So today a signed-in landlord is AuthDenied on Set-terms / Verify-guarantor / Cancel-renewal /
Set-listing-status, and reaches `DecideLeaseApplication` only through the front-desk grant. Inc 3 gives the
landlord hat its own authorization path.

**3 · Guard shape — the scope=self counterpart to `require_workplace`.** `scripts.go:266-291`'s own doc states
the structure: `require_workplace` "binds the STANDING path only … a scope=self caller is bound instead by its
own op's ownership probe … each binds the path the other cannot see." The landlord probe is that missing half:
walk the op's subject to its unit and require the acting identity's `manages` link
(`lnk.identity.<landlordID>.manages.unit.<unitID>`, `loftspace-domain/ownership.go:18-28,191`).
**Fires exactly when the caller declares self-action** — `op.authContextTarget != "" and op.authContextTarget
== op.actor` — chosen over the bare presence test Inc 1 used and over `op.authTargetValidated`:
- vs. **bare presence** (Inc 1's applicant shape): the renewal ops are task-minted, and a task's
  `authContext.target` is the *renewal*, not an identity — a presence test would try to parse a renewal key as
  the landlord and misfire. Equality with `op.actor` is presence plus the fact that makes the read meaningful.
- vs. **`authTargetValidated`**: that bit is also true on the task path
  (`internal/processor/operation_context.go:46-58`), which would newly confine every task-minted renewal write.
- Safety is unchanged from Inc 1's cleared review: a scope=self holder **cannot** omit `authContext`
  (`matchPlatformPermission` denies an absent target), so the guard is unbypassable for the path it binds; a
  scope=any holder that sends its own key is only *further*-restricted, never escalated.
**Ordering:** the probe goes **before** any payload-matching or PII read (`c4b45b33`, ownership-guards-answer-
first) — for `VerifyGuarantor` that means before the applicant `.profile` read.

**4 · Verified touch-list (scouted live @ `d60daa56`):**
- **Grants (+1 `consumer` scope=self row each, mirroring `permissions.go:96-99`/`114-117`):**
  `lease-signing/permissions.go:102` (Decide, keeps `operator`+`frontOfHouse` scope=any), `:126` (SetRenewalTerms),
  `:132` (VerifyGuarantor), `:144` (CancelRenewal); `loftspace-domain/permissions.go:20` (SetListingStatus —
  needs a per-op row, its `mk()` helper is uniform `operator`/any).
- **`DecideLeaseApplication`** (`lease-signing/scripts.go:495-637`): the unit walk already exists at `:521-533`
  (`kv.Links(app_key,"appliesToUnit","out")`, annotated `(e)`) but runs only inside `if not workplace_exempt()`
  — and `workplace_exempt()` returns true for a scope=self caller (`:259-268`), so the landlord path skips it.
  Hoist the walk into a helper both branches call.
- **Renewal ops** (`lease-signing/renewal_scripts.go:196` SetRenewalTerms, `:235` VerifyGuarantor, `:371`
  CancelRenewal): this script is its own Starlark module with no `kv.Links` use today. Add a two-hop walk
  `renewal -renews-> leaseapp -appliesToUnit-> unit`; both hops are single-valued by construction (OpenRenewal
  writes exactly one `renews` link in the vertex's own batch, `:180-186`; `appliesToUnit` is required at
  `CreateLeaseApplication`), the same bounded shape `cafe-domain/ddls.go:517` already ships.
- **`SetListingStatus`** (`loftspace-domain/ddls.go:391-433`): unit is the payload field, already
  `require_live_unit`-checked — no walk. The convergence **directOp** path carries no `authContext`, so the
  guard is inert there (verified: Weaver's service actor dispatches it off `missing_listingLeased`,
  `lease-signing/targets.go:20`; the renewal tasks are `Assignee: "row.landlord"`, `renewal_targets.go:90,100`).
- **FE** (`cmd/loftspace-app/web/app.js`): `loadWhoami:348-372` drops `body.roles` on the floor — capture it;
  `isLandlord:817` already reads the `manages` anchor. Submit sites: `:3491` Decide, `:3569` SetListingStatus,
  `:2153` the task-completion submit that carries all three renewal ops via `task.operationName`.
- **Versions:** `lease-signing/package.go:85` 0.24.4→0.25.0, `loftspace-domain/package.go:51` 0.7.1→0.8.0
  (a permission + script edit no-ops on install without a bump — `lint-package-version`).

**5 · FE predicate — `isLandlord() && !roles.includes("operator")`.** One predicate for all five ops, and it
duplicates no grant matrix. A **plain landlord** (consumer + `manages`) and the **front-desk landlord** (the
demo's own persona: `frontOfHouse` + `manages`) both write as themselves and satisfy the probe; a
**front-desk non-landlord** holds no `manages` anchor, sends no `authContext`, and keeps the standing
`frontOfHouse` decide path exactly as today; an **operator** is excluded so a portfolio-wide console is never
narrowed to the units it happens to manage. No regression in any of the four cells. Degradation is consistent:
a session whose `manages` anchor has not projected yet cannot see the landlord surface at all, so the predicate
and the tab hide together.

**6 · Increment order + green checks:** (1) grants + guards + package tests → `go test ./packages/...`;
(2) FE → `node --check web/app.js` + `go test ./cmd/loftspace-app/...`. Gates: `go build ./...`, `make vet`,
`golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`, `lint-package-version`,
`lint-lens-anchors`, `lint-board`. Live: `make reinstall-package` both packages, cycle `bin/loftspace-app`.

**7 · In-scope gotchas:** `parts_of(op.actor, …)` must tolerate an actor that is not `vtx.identity.*` — a
service actor never reaches the guard because it sends no `authContext`, but the parse must not raise before
that check. The renewal script's helper set is independent (no cross-script imports, `:114-120`), so the walk
helpers are written there rather than shared. `RemoveUnitOwner`/`AssignUnitOwner` are deliberately NOT granted
to `consumer` — a landlord must not be able to assign themselves units (the escalation Inc 2's as-built found).

**8 · Non-goals (Inc 3):** no new role (§2 of the Inc 2 brief, standing); no `require_workplace` on the renewal
ops (operator-only standing path, operator is exempt anyway); no §6.4 op-set parity script; no `manages`-anchor
backfill (its own filed row); no sibling-app adoption (W3/W4); no contract text.

#### Fire W2 Inc 4 fire brief (build note, 2026-07-25)

**1 · Scope sentence (verbatim, the filed row):** *"Both `manages`-holding personas are the frontOfHouse staff
spine, which `seed-showcase.go` gives no consumer role by design — so their cap docs carry no scope=self row and
the landlord hat still cannot SetRenewalTerms / VerifyGuarantor / CancelRenewal / SetListingStatus live."* Seed
the persona cell that makes Inc 3's five grants reachable.

**2 · The row's premise is right, its count is wrong — and the mechanism is one layer deeper.** Scouted live at
`3feceaa7`: there is exactly **one** `manages` link in the whole seed, not two —
`seed-showcase.go:1121` wires Dana Whitfield (the frontOfHouse staff persona) to Unit 3, and no other call site
writes the relation. The row's "both `manages`-holding personas" overcounts; the correction rides this fire.

The deeper fact is that this is **not** merely "the seed forgot to assign `consumer`". The Gateway auto-grants
`consumer` to actors it authenticates (`gateway.go:628` → `ProvisionConsumerIdentity`), which is why every
*Facet* consumer works — but that script is **first-touch only**: `existing = kv.Read(target_actor_key); if
existing != None: return {"mutations": [], "events": []}` (`identity-domain/ddls.go:809-815`). A seeded persona
is a pre-existing identity by construction, so the auto-provisioner is a clean no-op for it and **structurally
cannot** backfill the role. Every seeded persona that is to act as a signed-in human must therefore be granted
`consumer` **in the seed**, explicitly — `seedTenant` already does (`:477-479`); nothing else does.

**3 · Why Dana cannot simply be given `consumer` (the option this fire rejected).** `seedStaff`'s own doc
(`:549-554`) states the persona "deliberately gets NO residesIn link and NO consumer role, so its world is
purely staff-derived" — it is the showcase's proof that a world composes from `worksAt` + `holdsRole` alone.
That is intent, but the **hard** blocker is resolution: `ensureStaff` (`:523-532`) and `ensureMaintenanceTech`
(`:442-451`) recover their persona with `findLinkedIdentity(…, "holdsRole", roleKey, consumerRoleKey)`, where
the last argument **excludes** any candidate that also holds `consumer` — the discriminator that keeps Dana
distinct from Sam (tenant2, who holds `frontOfHouse` **and** `consumer`, `seedSamMultiHat:995-1002`). Granting
Dana `consumer` makes `ensureStaff` match nothing and mint a **duplicate Dana on every rerun**. Rejected.

**4 · The build: seed the missing PLAIN-LANDLORD cell.** §7.2 already names `consumer` the signed-in-human
role and the landlord a `consumer` + `manages` identity; Inc 3 §5 enumerated four cells and shipped guards for
all of them, but the seed only ever populated the *front-desk landlord* (Dana: `frontOfHouse` + `manages`).
The plain landlord — `consumer` + `manages`, no staff role — is the cell that exercises the five scope=self
grants, and it is the one that does not exist. Add it, additively:
- `ensureLandlord` — recover by scanning the `manages` link into its own unit (the same one-relation-over
  recovery shape `recoverTenants`/`ensureStaff` use); else mint `CreateUnclaimedIdentity` → `AssignRole`
  (`consumer`) → `AssignUnitOwner` → `UpdateIdentityState` claimed. Mirrors `seedTenant:468-480`.
- A fourth unit + its own signed, undecided lease application, mirroring `seedStaffWorklistApplication:1108`
  — without an application on a unit it manages, `DecideLeaseApplication`/`VerifyGuarantor` have no subject and
  the cell is not actually exercisable. Dana keeps Unit 3 and her worklist beat untouched, so the two landlord
  cells stay independent and neither empties the other's pane.
- Both seed branches (the `alive(buildingKey)` already-loaded layering path and the from-scratch path), plus a
  machine-readable `LOFTSPACE_LANDLORD_NANOID=` line mirroring `FACET_*_NANOID`.

**5 · Verified touch-list (scouted live @ `3feceaa7`):** `scripts/seed-showcase.go` only — constants
(`:105-157` unit/persona ids + `showcaseLocationNames`/`showcaseLocationOrder`), the already-loaded branch
(`:214-218`), the from-scratch branch (`:266-274`), and the new functions alongside
`seedStaffWorklistApplication:1108`. **No package, permission, script, lens, DDL, or FE change** — Inc 3 already
shipped every grant and guard, `identityAnchors` already walks `manages` (`identity-domain/lenses.go:164`), and
`app.js:817`'s `isLandlord` already gates on that anchor. No version bump (no package edit).

**6 · Increment order + green checks:** one increment. `go build ./...`, `make vet`, `golangci-lint run ./...`,
`STRICT=1 go run ./scripts/lint-conventions.go`, `go test ./scripts/... ./packages/loftspace-domain/...
./packages/lease-signing/...`. Live: reseed against the running stack, `dev-login` as the new landlord's
NanoID, drive `SetListingStatus` + `DecideLeaseApplication` as a plain consumer landlord.

**7 · In-scope gotchas:** the seed's two branches must BOTH call the new function (the already-loaded path is
how an existing dev stack gains the persona without a wipe) and every mutation must be liveness-guarded, since
that path re-runs on every `make up`. `AssignUnitOwner` is operator-only and the seed submits under `adminKey`
— correct and unchanged; the landlord must NOT be able to call it (Inc 3 §7, standing). The new unit needs a
`SetUnitAddress` or the landlord console renders it bare, and it must join `showcaseLocationNames` +
`showcaseLocationOrder` or `seedLocationPresentation` leaves it nameless.

**8 · Non-goals (Inc 4):** no op-meta `authContext` change (its own filed row — Facet's renderer still lands on
the standing path); no `AssignUnitOwner` actor binding (its own filed row); no new role; no grant, guard, lens
or FE change; no `LOFTSPACE_APP_DEMO_PERSONAS` wiring (the demo box fences **Facet** only, `demo-up.sh:86` —
LoftSpace is not in persona posture there, so no deploy config rides on this).

**As-built — Inc 4 SHIPPED (2026-07-25, `4aad350b`).** The plain-landlord cell exists. Nora Vance holds
`consumer` and nothing else, manages Unit 4, and that unit carries a listing, an address, and a signed
undecided application from a walk-in applicant. Seed-only: no package, permission, script, lens or FE change,
so no version bump (`lint-package-version` confirms no `packages/` content changed).

*One addition beyond the brief's touch-list, in scope.* The brief planned a unit + application; the build also
seeds a **listing**, because `SetListingStatus` — named in the scope sentence — rejects a unit with no listing
to transition, so the cell was not exercisable without one. Caught by live verification, not by the scouts.

*Live-verified against the running stack, both directions.* The discriminating pair uses the fact that the
ownership probe answers **before** `require_live_unit` and before the listing read: the landlord's
`SetListingStatus` is **accepted and committed** on Unit 4, and **AuthDenied by `require_manages`** on Unit 3
(the staff persona's unit). The denial also proves the grant half — a persona lacking `consumer` would have
been denied at step 3 with `NoCapabilityEntry`/`OperationNotPermitted` and never reached the script's own
probe, so reaching the probe *is* the evidence the scope=self row matched. `/api/landlord/applications` returns
`scope: rls` with exactly one unit and one application, and a rerun of the seed recovers the same identity
rather than minting a second.

*A first positive attempt was inconclusive and was redone.* Declaring `.listing` in `reads` killed the op at
step-4 hydration (`HydrationMiss`) before the script ran, which proves nothing about authorization; moving it
to `optionalReads` let the script run and fail `NoListing`, which does. Recorded because the two rejections
look equally like "it didn't work" and only one of them is evidence.

*Residual, filed as a row in the same docs commit:* the same first-touch limitation means the **bound
provider / serviceprovider / instructor** personas hold their entity role alone and no `consumer`, so they too
can reach no `consumer` scope=self grant — consumer named as the W1/W3/W4 self-service surfaces. Not filed,
deliberately: the two Inc 3 residuals (op-meta `authContext`, `AssignUnitOwner` actor binding) already have
their own rows and are untouched by this increment.

**Scope-diff gate: PASS** — every touch traces to seeding the persona cell that makes Inc 3's five grants
reachable. The listing is the one addition, and it is required by an op the scope sentence names rather than an
adjacent mechanism substituted for it. The staff persona, its Unit 3 beat, and every existing grant, guard and
role assignment are untouched.

### Fire W4 (café) fire brief (build note, 2026-07-25)

**1 · Scope sentence (verbatim, §7.4):** *"Café — hats: resident (own tab, self open/settle), staff (POS,
front-desk grid). **Supplier deferred** (named trigger: a replenishment/inventory op set exists for a supplier
to act in). Stands up read auth for tab/ledger reads (today an unauthenticated clinic-wide dump)."* Plus the §7
common obligations: adopt the kit, delete pickers/mints, whoami-driven hats, per-actor submits, grants audit.

**2 · The premise verified live, and it is worse than "no login".** Scouted at `76df2769` against the running
stack (:7801). Three separate facts, each checked by reading:
- **A fixed admin actor.** `main.go:89` sets `adminActor = bootstrap.BootstrapIdentityKey`, and
  `readauth.go:77-108`'s `POST /api/staff/dev-token` mints a Bearer token for it. Every staff write in the app
  — OpenTab, Charge, Settle, CreditAccount — is submitted as the **bootstrap identity**. This is the §6(1)
  fixed-admin mint verbatim.
- **An any-subject mint.** `readauth.go:110-147`'s `POST /api/dev-token` mints a token for whatever `subject`
  the request body names, with no check that the caller *is* that identity. The FE's "sign in as resident" is
  `app.js:45-49` writing a NanoID to `localStorage["cafe.selfBookerKey"]` — UI state, not authentication. This
  is the §6(1) any-subject impersonation mint verbatim.
- **Every read is unauthenticated.** `readauth.go:19-26` states the app "has no protected read boundary".
  Confirmed live: `/api/leases`, `/api/tabs`, `/api/residents`, `/api/menu` return **200 with no credential**,
  dumping every lease key, every tab with its balance and settlement state, and every resident's identity key
  to any client that can reach the port. Eight endpoints, zero auth calls.

**3 · Fork resolved in-fire — the café read boundary is session-scoped, not Postgres/RLS.** Clinic and LoftSpace
authenticate reads by handing the session subject to Postgres RLS; **café has no Postgres read model at all**
(`readauth.go:19-26`; the `up-cafe` recipe comment says the same, which is why it has no `provision-*-role`
step). Standing one up is not the answer here: S3 tiers the read boundary **by data, not habit**, and Path A is
triggered by *person-identifying columns*. Every café lens projects opaque keys only — `leaseAppKey`,
`bookerKey`, `accountKey`, cents, timestamps; no names anywhere — which is precisely the "bare keys only for
the people the rows are about" shape S3 declares **admissible** on open NATS-KV. So the boundary this fire owes
is the one §8 actually names — *"café/wellness read boundaries **authenticated**"* — not a read-model rewrite.
Shipped shape: the whole mux behind `RequireSession` (clinic's `server.go:84` wiring), plus **server-side
scoping keyed on the verified session subject** (§6(3)) — a resident sees only rows for leases whose
`bookerKey` is their own identity; a `worksAt` staffer sees the house. The resident→lease fact needs no new
primitive: the `leaseApplicationComplete` lens already maps `leaseAppKey → bookerKey` and the app already reads
it (`residents.go:81`).

**4 · Fork resolved in-fire — "Record Payment" loses its surface rather than gaining an unconfined grant.**
The FE offers `CreditAccount` (`app.js:657-682`), which `cafe-ledger/permissions.go:28` grants to **`operator`
only** — all three ledgers do, deliberately ("orchestrator-submitted... the trusted-tool app submits"). It
works today *only because the app impersonates the bootstrap identity*, i.e. only because of the exact
anti-pattern this design deletes. Post-flip it has no authorized hat, and §6(4) makes offering it a bug. Three
options were weighed:
- *Grant `frontOfHouse` scope=any* — **rejected.** It would ship an **unconfined financial authority** (credit
  any account any amount, including one's own) into a package whose scripts have no workplace binder at all,
  while café-*domain*'s own staff ops **are** workplace-confined (`ddls.go:430-462`). That is below café's
  existing bar, and it is the same fraud-vector reasoning that already denies `VoidCharge` a self grant
  (`permissions.go:31-37`).
- *Grant it confined* — the architecturally right end state, but confinement requires walking
  account→lease→unit→building, a read shape **no ledger package has any precedent for**. Per §2 that is new
  mechanism, i.e. design work, not this fire's execution.
- *Gate the surface on the `operator` role from whoami* — **not possible**, and the reason is worth recording:
  `/v1/actor` forwards role **keys**, not canonical names (`rolesanchors` doc-comment; `fetchActorHats`
  returns `doc.Roles`, which are `vtx.role.<NanoID>`). An FE cannot tell which key means `operator`. This is
  the same constraint W2 Inc 3 hit when it could not write an FE-side role predicate.

  **Shipped: remove the surface, file the row.** The op, its grant, and the ledger are **untouched** — an
  operator still records payments through Loupe or the CLI. A capped row goes in the same docs commit naming
  its consumer (the café front-desk payment flow) and the mechanism it waits on (a workplace-confined ledger
  credit).

**5 · Verified touch-list (read live @ `76df2769`).** `cmd/cafe-app/` — `main.go:84-91` (bootstrap loader +
`adminActor`), `server.go:21-37` (the `adminActor`/`devSigner` fields), `server.go:39-59` (route table: the two
mint routes die, `RegisterRoutes` + `RequireSession` arrive), `readauth.go:77-147` (both mint handlers die;
the file collapses to clinic's ~50-line context-extraction shape, `readauth.go:42-52`), the eight read handlers
in `leases.go` / `tabs.go` / `ledger.go` / `residents.go` / `menu.go` / `frontdesk.go`, `health.go:1-48`
(drops the `adminActor` probe, gains the signer probe), `web/index.html:20-26` (the me-bar picker), `web/app.js`
(753 lines: token caches → one cookie, picker → whoami hats), **new** `web/login.html` (mirror
`cmd/clinic-app/web/login.html`). Makefile: `CAFE_APP_DEMO_PERSONAS` alongside `CAFE_APP_DEV_AUTH` in
`up-cafe:745-757` + `refresh-cafe:1247-1272`. **No package, DDL, lens, script, grant or seed change** — the
grants audit (§6 below) found café-domain already archetype-complete.

**6 · The grants audit came back clean, which is the load-bearing finding.** Every café-domain op already
carries the hat it needs: OpenTab / Charge / Settle hold **both** `{operator, frontOfHouse}` scope=any **and**
`consumer` scope=self (`permissions.go:44-80`), and all three scope=self paths already prove ownership through
the lease's `applicationFor→identity` link with the `== None or .isDeleted` tombstone probe
(`ddls.go:599-608`, `:698-708`, `:791-800`), each declared in `contextHint.optionalReads`. `VoidCharge` is
scope=any by design. Read posture is clean — no class-(b) debt. **So café needs no package change to become
sign-in-first**, and the one gap the audit did find is §4's `CreditAccount`. That is why this fire is app-only.

**7 · Increment order + green checks.** One increment, built in order so each step is independently runnable:
(a) kit wiring in `main.go`/`server.go` + `login.html` → `/login` serves and `/api/whoami` answers; (b) delete
both mints + `adminActor` → `go build` names every dead reference; (c) `RequireSession` over the mux → every
read endpoint 401s uncredentialed; (d) subject-scoping in the read handlers → a resident's `/api/tabs` returns
only their own; (e) FE picker → whoami hats, per-actor submits, Record Payment removed.
*Gates:* `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`,
`STRICT=1 go run ./scripts/lint-board.go`, `go test ./cmd/cafe-app/... ./internal/appsession/...`.
*Live:* `refresh-cafe`, sign in as a seeded tenant, drive OpenTab→Charge→Settle as **that resident**, and
confirm the discriminating pair — the resident's reads scope to their own lease, and an uncredentialed
`/api/tabs` 401s where it returned 200 before.

**8 · In-scope gotchas.** (i) The two mints are what `CAFE_APP_DEV_AUTH=1` gates; the flag **survives** the
flip as the dev *sign-in* posture (clinic and loftspace both still set it post-flip — `Makefile:735`, `:673`),
so do not delete the flag, only the any-subject semantics behind it. (ii) `RequireSession` must wrap an
**inner** mux with the kit's routes registered on it, or `/login` itself needs a credential (clinic
`server.go:66-84`). (iii) The three `frontdesk-*` endpoints read **cross-vertical** front-desk lenses — they
are a staff surface and scope to the `worksAt` hat, not to a lease. (iv) A present-but-invalid cookie must fail
**closed** (`session.go:247-269`); café has no `FallbackIdentityID` and must not acquire one — clinic
deliberately omits it so an uncredentialed visitor is genuinely anonymous.

**9 · Non-goals (W4).** No café supplier hat (§7.4 named-deferred, trigger unchanged: a replenishment/inventory
op set). No Postgres read model for café (§3). No new role, grant, guard, lens, DDL or seed change (§6). No
`CreditAccount` grant change (§4). No wellness work — W3 is its own fire. No change to the ledger idiom in
loftspace-ledger / clinic-ledger.

**Scope-diff gate (pre-build): PASS.** Item-by-item against §7.4 + the §7 common list, narrow-only: kit
adoption, mint/picker deletion, whoami hats and per-actor submits each trace to a §7 common clause; the
`RequireSession` + subject-scoping work is "stands up read auth for tab/ledger reads" and nothing more (§3
argues it *down* from a Postgres rewrite, not up); the grants audit is a §7 common clause and returned clean,
which is what makes this fire app-only. The one item not literally in the scope sentence — removing Record
Payment — is forced by §6(4) given the audit's single gap, and is a **narrowing** of what the app offers, not
an adjacent mechanism substituted for a named one. Supplier stays deferred with its trigger unchanged.
Declared dependencies re-verified both ways: W4 depends on P1+P2 (both SHIPPED — `internal/appsession` exists
and clinic/loftspace run on it) and **not** on W0's provider spine (café has no provider hat), so nothing
sequences ahead of it.

**As-built — W4 SHIPPED (2026-07-25, `f5a9c8a3`).** Café is sign-in-first. Both mints and the fixed admin
actor are gone, the eight read endpoints answer only a verified session and scope to its subject, and the FE
renders hats from whoami. App-only, exactly as the brief predicted: no package, DDL, lens, script, grant or
seed change, so no version bump.

*The brief's two forks held, and the audit that made this fire app-only was the load-bearing one.* Every
café-domain op already carried both its staff scope=any and its `consumer` scope=self grant with a
tombstone-probed ownership guard, so the sign-in flip needed no package work at all — the single gap was
`CreditAccount`, resolved as the brief decided (surface removed, op untouched, confined-credit work filed).

*Three defects found in review, none of which the gates would have caught.*
- **The FE stamped `authContext.target` on every submit.** This is the one that would have shipped broken.
  cafe-domain branches on the mere **presence** of that field rather than on `authTargetValidated`, and the
  Processor passes it through verbatim from the envelope (`starlark_runner.go:645-648`) no matter which grant
  authorized the op — so a staff `Charge` was pushed onto the self-order branch, **silently discarding the
  staff-entered `amountCents` in favour of the catalog price**, and `OpenTab`/`Settle` then required the
  staffer to be the lease's own applicant. Every POS and front-desk write would have been denied. The target
  now rides the resident hat only (`selfMode` is already `!isFrontDesk()`, so the hat boundary was exact).
  The builder had written a comment asserting the opposite — that attaching it was "harmless for a staff
  submit (the scope=any grant never consults target)" — which is true of the *grant* and false of the
  *script*; the DDL's own comment says the check "is a no-op there" precisely **because** a scope=any caller
  sends no authContext.
- **`isStaff` tested the anchor's `relation` alone.** `identityAnchors` stamps `relation` as a literal
  constant on every collected entry, so an identity with no workplace still yields `{key:null,
  relation:"worksAt"}` from the unmatched OPTIONAL MATCH (`identity-domain/lenses.go:168`; the package's own
  test calls it "the degenerate {key:null} OPTIONAL-MATCH entry"). Only the lens's `RealnessFilter: "key"`
  strips it — so café's entire staff boundary rested on a filter declared in a *different package*. Not
  exploitable today (traced end to end, and confirmed live: a resident's whoami carries no worksAt entry),
  but one clause makes it self-contained. The idiom was transplanted from clinic and Facet, where the same
  predicate is **UX curation** over a grant/RLS authority; here it *was* the authority.
- **`computeTabs` kept a row with a `tabKey` but no `leaseAppKey`**, which every sibling compute function
  already skips.

*Live-verified against the running stack, both directions, with positive controls.* Uncredentialed reads 401
where they returned 200. A resident sees 1 lease / 2 tabs against staff's 13 / 12; is 403'd naming another
resident's lease and **200 on their own** (so the 403 is not a blanket refusal); front-desk endpoints 403 for
a resident, 200 for staff. A resident opened a tab and self-ordered at the catalog price (450); front-of-house
charged 275 on that tab. **The discriminating pair:** the same `Settle`, same actor, same tab is `AuthDenied:
a resident may only settle their own tab` **with** `authContext.target` attached and **committed** without it
— the pre-fix FE against the shipped one, differing in exactly one field. A `backOfHouse` `worksAt` holder is
correctly denied (café grants it nothing), which also caught the first live attempt using the wrong persona.

*Residuals, filed as rows in the same docs commit.* (a) The staff read hat is **workplace-unscoped** — a
`worksAt` anchor to any location anywhere grants the whole café house, a read/write asymmetry inside one
vertical, since café-domain's staff *writes* are workplace-confined (`ddls.go:441-467`) and Facet's staff
*read* spine is grant-confined. Ratified as-is by §7.4's "a `worksAt` staffer sees the house" and a strict
improvement on the unauthenticated dump it replaces, but it needs a lease→unit→building join no café lens
projects, so it is design work rather than this fire's execution. (b) The kit's `/api/dev-login` persona
**fence is unwired in every vertical's Makefile target** — `CAFE_APP_DEMO_PERSONAS` and its clinic/loftspace
equivalents are set nowhere, so `personaAllowed` returns true for any valid NanoID on a `make up-*` stack.
The brief's touch-list named this for café; it was deliberately not done, because neither shipped sibling does
it either (verified) and doing it café-only would invent local-dev behaviour no sibling has. It is a
platform-wide dev-posture gap, not a café regression, and the hard fences still hold (`signer.go:78-80`
refuses a non-loopback bind; `session.go:145-154` refuses a public origin + signer without personas).

### Fire W3 (wellness) Inc 1 fire brief (build note, 2026-07-25)

**1 · Scope sentence (verbatim, §7.3):** *"Wellness — hats: member (browse/book/cancel), staff (create
sessions, roster), **instructor** (`vtx.instructor`, `ledBy` on sessions, own-roster + attendance +
cancel-own-session ops). Stands up the Tier-B read boundary for per-user reads (bookings/My Classes move behind
the session; schedule stays public-read)."* Plus the §7 common obligations: adopt the kit, delete
pickers/mints, whoami-driven hats, per-actor submits, grants audit.

**Increment split.** Inc 1 = the board row's named next step, *sign-in-first flip + read auth* + the three
hats over already-granted ops. **`attendance` is Inc 2** — it is a genuinely new op (no `AttendBooking` /
`MarkNoShow` exists in wellness-domain), and its consumer is the instructor roster this increment ships, so it
files as a row rather than hiding in a paragraph (deferred-tail rule).

**2 · The premise verified live, and it is café's exactly.** Scouted at `753a7fad`. Four facts, each read:
- **A fixed admin actor.** `main.go:87` sets `adminActor = bootstrap.BootstrapIdentityKey`; `readauth.go:78-109`
  mints a Bearer token for it. Every staff write — CreateStudio, CreateSession, roster CancelBooking — is
  submitted as the **bootstrap identity** (`app.js:26-33`, `app.js:79-88`). The §6(1) fixed-admin mint verbatim.
- **An any-subject mint.** `readauth.go:111-150` mints a token for whatever `subject` the body names, with no
  check the caller *is* that identity. The FE's "sign in as resident" is `app.js:45-49` writing a NanoID to
  `localStorage["wellness.selfBookerKey"]` — UI state, not authentication. The §6(1) impersonation mint verbatim.
- **Every read is unauthenticated,** and one is worse than a dump: `/api/bookings?bookerKey=<anyone>`
  (`bookings.go:94-96`) is a **client-supplied identity filter** — any caller reads any resident's class
  history. This is the same vector clinic already closed when it deleted `?provider=` (`appointments.go:328-331`).
  `?sessionKey=` returns a session's full roster of booker keys to anyone.
- **`/api/residents` dumps the house** (`residents.go`) — every lease applicant's identity key, uncredentialed,
  to populate the picker.

**3 · Fork resolved in-fire — "schedule stays public-read" is honored literally, and it is the narrower claim.**
Café put its whole mux behind `RequireSession`, `/api/menu` included. §7.3 does not say that: it tiers
*by data*, naming bookings/My Classes as the reads that move behind the session and the schedule as staying
public. Studios and sessions project class name / time / capacity / studio name — no person-identifying column,
the S3-admissible open-KV shape. Shipped: `/api/studios` + `/api/sessions` are `ExtraExemptPaths`
(`session.go:58` Config), everything else is gated. The **app shell stays sign-in-first** (`/` gated, browser
lands on `/login`) exactly like every sibling — so the *data* is public-read while the *app* is sign-in-first,
which is what the scope sentence claims and nothing more.

**4 · Fork resolved in-fire — the grants audit came back DIRTY, and §7.3's ratified staff hat is what resolves it.**
Unlike café, three surfaces this FE offers have no authorized hat once the admin mint dies
(`permissions.go:49-84`):

| Surface | Op | Granted to | Post-flip |
|---|---|---|---|
| Studios tab → "New studio" | `CreateStudio` | `operator` only | **no hat** |
| Schedule tab → book-for-a-resident picker | `CreateBooking` scope=any | `operator` only | **no hat** |
| Roster tab → per-seat Cancel | `CancelBooking` scope=any | `operator` only | **no hat** |

§7.3's ratified staff hat is exactly **"create sessions, roster"** — not create-studios, not book-for-anyone,
not cancel-anyone's-booking. `CreateSession` already grants `frontOfHouse` **and** confines it to the studio's
location by a `worksAt`→`containedIn` walk (`ddls.go:931-972`), so the one op §7.3 names is already correct and
already confined. The other three are surfaces the app only ever had **because it impersonated bootstrap**.
Shipped, per café's §6(4) precedent: **remove the surfaces, leave the ops and grants untouched** (an operator
still creates studios and force-cancels through Loupe or the CLI), and file a capped row naming each consumer.
Roster becomes a read-only seat list — which is what "roster" means in the scope sentence. The member cancels
their own booking through My Classes on the `consumer` scope=self grant that already exists and already probes
`bookedBy` with the tombstone idiom (`ddls.go:1480-1489`).

*Rejected: granting `frontOfHouse` on the three ops.* For `CreateStudio` a confined grant is arguably
mirrorable (the `worksAt` helpers exist in the sibling `sessionDDLScript`), but it is **scope widening** — the
ratified staff hat does not include it, and the scope-diff gate is narrow-only. For `CancelBooking`/`CreateBooking`
scope=any it would hand staff unconfined authority over any resident's bookings, below the bar café already set
for `CreditAccount`.

**5 · The one package change: `wellnessSessions` must project its instructor.** §7.3 names "`ledBy` on
sessions" as the instructor hat's spine, and the lens does not project it (`lenses.go:68-87` walks `atStudio`
only), so nothing can answer "which sessions do I lead". A missing **lens** is package work built here (not a
platform primitive), and the fix mirrors the spec's own existing `OPTIONAL MATCH` studio walk one line down.
Adds `instructorKey` + `instructorName`. Projecting an instructor's `displayName` on the public-read schedule is
deliberate and precedented: clinic's **provider directory stays public** while patient names went Protected
(`6b1c667c`) — a class instructor's professional name is the provider-directory analog, not private PII.
⇒ wellness-domain **0.10.4 → 0.11.0**, `make refresh-wellness` to hot-activate.

**6 · Verified touch-list (read live @ `753a7fad`).** `cmd/wellness-app/` — `main.go:79-89` (bootstrap loader +
`adminActor`), `main.go:110-125` (signer setup → kit setup, mirror `cafe-app/main.go:145-204`), `main.go:1-35`
(the doc comment's "NO authentication and acts as admin" is now false), `server.go:21-37` (drop `adminActor` /
`devSigner`, gain `authn` + `session`), `server.go:39-55` (inner mux + `RegisterRoutes` + `RequireSession`),
`readauth.go:78-150` (both mint handlers die; file collapses to café's `authenticateRead`/`resolveSubjectHats`
shape, `cafe-app/readauth.go:29-115`), `bookings.go:76-98` (`bookerKey` param deleted, subject-scoped, roster
gated), `residents.go` (house dump → `/api/my-residency`, one row, subject-scoped), `sessions.go` (pass the new
instructor columns through), `health.go:16-20` (drop the `adminActor` probe, gain the signer probe),
`web/index.html:20-25` (me-bar picker dies), `web/app.js` (707 lines: token caches → one cookie, picker →
whoami hats, three surfaces removed, instructor tab added), **new** `web/login.html` (mirror
`cmd/cafe-app/web/login.html`). `packages/wellness-domain/lenses.go:68-87` + `manifest.yaml:2` + `package.go:92`.
Makefile `up-wellness:996-1008` / `refresh-wellness:1276-1292` — env is **already** the post-flip shape
(`WELLNESS_APP_DEV_AUTH=1`, same as café's), so only the echo text changes.

**7 · The hats, and how each is resolved.** All three from the Gateway's `/v1/actor` anchors, server-side,
fail-closed (café's `resolveSubjectHats` verbatim):
- **member** — any verified session. Books/cancels **as themselves** with `authContext.target` = own identity.
- **staff** — a `worksAt` anchor with a **non-empty key**. Café's lesson is load-bearing and applies unchanged:
  `identityAnchors` stamps `relation` as a literal constant on every collected entry, so an identity with no
  workplace still yields `{key:null, relation:"worksAt"}` from the unmatched OPTIONAL MATCH
  (`identity-domain/lenses.go:168`); testing the relation alone would rest this boundary on another package's
  `RealnessFilter`.
- **instructor** — an `identifiedBy` anchor whose key is prefixed `vtx.instructor.` (the anchor's key **is** the
  bound entity key — `identityAnchors` collects `{key: bound.key, relation: 'identifiedBy'}`, so the type prefix
  is what distinguishes a wellness instructor from a clinic provider on a multi-hat human). That key scopes the
  own-roster read and is the `instructor` payload param `TombstoneSession` already requires.

**8 · Increment order + green checks.** (a) kit wiring in `main.go`/`server.go` + `login.html` → `/login`
serves, `/api/whoami` answers; (b) delete both mints + `adminActor` → `go build` names every dead reference;
(c) `RequireSession` over the mux with the two schedule exemptions → `/api/bookings` 401s uncredentialed while
`/api/sessions` still 200s; (d) lens instructor columns + version bump; (e) subject-scoping + hat-gating in the
read handlers; (f) FE picker → whoami hats, per-actor submits, three surfaces removed, instructor roster added.
*Gates:* `go build ./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`,
`STRICT=1 go run ./scripts/lint-board.go`, `go run ./scripts/lint-package-version.go`,
`go test ./cmd/wellness-app/... ./packages/wellness-domain/... ./internal/appsession/...`, `make verify-package-wellness`.
*Live:* `refresh-wellness`, sign in as a seeded member, book and cancel **as that member**, and confirm the
discriminating pairs — an uncredentialed `/api/bookings` 401s where it returned 200; `/api/sessions` still 200s
uncredentialed; a member's `/api/bookings` returns only their own rows; a member is 403'd on a roster; the
bound instructor (Sam, `seedSamMultiHat`) sees the roster for the session they lead and not one they don't.

**9 · In-scope gotchas.** (i) `WELLNESS_APP_DEV_AUTH` **survives** the flip as the dev *sign-in* posture — do
not delete the flag, only the any-subject semantics behind it. (ii) `RequireSession` must wrap an **inner** mux
or `/login` needs a credential (`cafe-app/server.go:56-70`). (iii) **No `FallbackIdentityID`** — a wellness
browser with no cookie is genuinely anonymous; café deliberately omits it and so must this. (iv) The FE stamps
`authContext.target` on the **member hat only** — café's shipped defect was stamping it on every submit, and
`wellness-domain` likewise branches on presence (`ddls.go:1361-1362`). (v) The resident-rate hint needs the
caller's **own** `leaseAppKey`; that is what `/api/my-residency` returns, and it is why `/api/residents` is
rescoped rather than simply deleted. (vi) `cmd/wellness-app` has **zero tests today** — the sibling apps carry
10–13 files; this fire lands the boundary tests with the boundary.

**10 · Non-goals (W3 Inc 1).** No `attendance` op (Inc 2, row filed). No new role, grant, guard, DDL, script or
seed change — the only package edit is the lens's two instructor columns. No Postgres read model for wellness
(every lens is plain NATS-KV; §7.3 asks for authenticated reads, not a rewrite). No `CreateStudio` /
`CancelBooking`-any grant change (§4). No W5 Facet work. No café/clinic/loftspace changes.

**Scope-diff gate (pre-build): PASS.** Item-by-item against §7.3 + the §7 common list, narrow-only. Kit
adoption, mint/picker deletion, whoami hats, per-actor submits and the grants audit each trace to a §7 common
clause. `RequireSession` + subject-scoping is "bookings/My Classes move behind the session", and §3 argues the
schedule exemption *down* from café's blanket gate rather than up. The lens instructor columns are §7.3's own
"`ledBy` on sessions" and are the minimum that makes the named instructor hat answerable. The three removed
surfaces are **narrowings** forced by §6(4) given §4's audit, not adjacent mechanisms substituted for named
ones. `attendance` is the one named item deliberately not built — split to Inc 2 with a filed row, not dropped.
Declared dependencies re-verified both ways: W3 depends on P1+P2 (**both SHIPPED** — `internal/appsession`
exists, café/clinic/loftspace run on it) and on W0 (**SHIPPED `626763bc`** — `vtx.instructor`,
`BindInstructorIdentity`, the `provider` role grant and `TombstoneSession`'s instructor confinement are all
present, verified at `ddls.go:1680-1727` and `permissions.go:57-62`), so nothing sequences ahead of it.

**As-built — W3 Inc 1 SHIPPED (2026-07-25, `d3a7cc7b`).** Wellness is sign-in-first. Both mints and the fixed
admin actor are gone, the per-user reads answer only a verified session and scope to its subject, and three
hats render from whoami. wellness-domain 0.11.0.

*The brief's two forks held.* The schedule stayed public-read while the app shell stayed sign-in-first, and the
dirty grants audit resolved by removing three surfaces rather than widening a grant — exactly as §4 decided.

*The brief's own completeness claim was wrong, and the review caught it.* §10 asserted `attendance` was "the
one named item deliberately not built". It was not: **cancel-own-session** — `TombstoneSession`, whose
`provider` grant and in-script `ledBy` + `identifiedBy` confinement W0 already shipped — had no FE surface
either, and by the brief's own increment definition ("the three hats over already-granted ops") it belonged in
Inc 1. Worse, the own-roster that *was* built would have been unreachable in practice: `CreateSession`'s
optional `instructor` param had no field on the scheduling form, so every class created in-app projected no
instructor and the hat would have lit up for seed data alone. Both were built rather than deferred — the
consumer was the roster this same increment shipped. That needed one more lens (`wellnessInstructors`, backing
the form's picker), which is why the package changed more than the brief predicted.

*Three defects found in review, all fixed before the merge.*
- **Stored XSS in the render path.** `instructorName` was a new `innerHTML` sink on a **public-read** endpoint,
  and class names are staff-entered free text with no charset restriction. The chain was real: the app's own
  origin serves `/api/session/refresh`, which returns the caller's raw Gateway bearer. The sign-in-first
  conversion is what made it worth an attacker's time — before it there was no per-user session to steal. The
  sibling `cafe-app` already had an `escapeHtml` helper and it simply had not been followed; every data sink
  now escapes.
- **A Gateway outage was reported as 401**, which the FE reads as "your session is over" and answers by
  navigating to `/login` — so a transient upstream blip signed a valid session out. Split into `errNoSession`
  (401) versus an unresolvable upstream (502), and `handleBookings` no longer consults the Gateway at all for
  the own-bookings answer, which needs only the session's subject.
- **`kvGetter` conflated "no such key" with "the read failed"**, so a NATS blip told an instructor their own
  class was not theirs. The roster check now reads through `substrate.ErrKeyNotFound` directly.

*A lens bug the new tests caught before it ever ran.* `wellnessInstructorsSpec`'s `WHERE` was written after its
`OPTIONAL MATCH`, where it binds to the optional pattern instead of filtering the driving row — the
profile-less instructor rostered anyway. The rule-engine test failed on it immediately; `wellnessStudiosSpec`'s
ordering was the fix.

*Live-verified against the running stack, both directions, with positive controls.* Uncredentialed:
`/api/sessions` and `/api/studios` 200 while `/api/bookings`, `/api/my-residency` and `/api/instructors` 401.
The seeded multi-hat human (Sam Okafor — member + `worksAt` staff + bound instructor, §3.4's acceptance
scenario) renders all three hats and four tabs; a plain member renders two. **The discriminating pairs:** Sam's
own bookings return 1 row against the same session's roster at 3; a plain member is 403 on that roster and sees
only their own 3 bookings; and the same `TombstoneSession`, same actor, differs only in which session — the
class Sam leads **committed** (releasing its four studio slot cells), the one they do not came back `AuthDenied:
… does not lead session …` with that session still standing. In-browser, "Call off this class" appears on a
class Sam leads and is absent on one they do not, the create-studio form is gone, and a member booked and
cancelled a future class end-to-end (3 → 4 → 3 classes). A past-dated seeded class was correctly refused
`SessionInPast` — a prior fire's guard, surfaced cleanly by the new toast.

*Residuals, filed as rows in the same docs commit.* (a) `attendance` (Inc 2), consumer named: the instructor
roster this increment shipped. (b) The three removed staff surfaces — create-studio and cancel-anyone's-booking
are operator-only, and the clean form is a confined grant, not a wildcard one. (c) The staff READ hat is
**workplace-unscoped**: any `worksAt` anchor anywhere grants every studio's roster, while wellness-domain's own
staff *writes* are workplace-confined by a `locatedAt`→`containedIn` walk. It is the widest thing this fire
opens, a strict improvement on the unauthenticated dump it replaces, and identical in shape to café's shipped
hat — so the row is broadened to cross-vertical rather than duplicated.

**As-built — W3 Inc 2 SHIPPED (2026-07-25, `5397dd2e`).** The instructor hat can record who showed.
`SetBookingAttendance` (wellness-domain 0.12.0) moves `.status.value` from `booked` to `attended` or
`noShow`, granted `[operator, provider]` on the existing scope=any row — front-of-house is deliberately
excluded, since a plain `frontOfHouse` grant is unconfined authority to restate any member's attendance in
any studio, the same boundary Inc 1's residual (c) already names.

*Three things this increment turns on, none of them the op itself.*

**1 · The guard is ordered, and the order is the security property.** It is TombstoneSession's two-hop, one
entity deeper: the caller's OWN `identifiedBy` binding answers first, then the session's `ledBy` link, then
the booking↔session match. Every bound instructor in the deployment holds the identical scope=any grant, so
the capability plane cannot separate them — reversed, the pair is an oracle that locates who leads a
stranger's class by which denial returns. `TestSetBookingAttendance_InstructorConfinedToTheirOwnClass`
asserts the real leader and a decoy are indistinguishable, which fails if the checks are ever reordered.

**2 · The write merges, and had to.** `.status` carries `{value, rate, seat, booker}`, and CancelBooking
reads `.seat` to release the seat cell and `.booker` to release the per-(session, booker) double-book guard.
A write storing `value` alone would strip both: the booking would fail to cancel with `InvalidState` AND
keep its guard alive, locking that booker out of ever re-booking the session. The op therefore OCC-upserts
the aspect on its own revision, carrying the three fields forward; the test drives mark→cancel→re-book and
asserts both cells released.

**3 · Timing and correction.** Attendance before the class begins is `SessionNotStarted`, the exact mirror of
CreateBooking's `SessionInPast`, on the same rfc3339-normalized lexical compare. Either value corrects the
other — café's missing charge-correction op is the shape deliberately avoided.

*Read path and consumers.* No lens change: `wellnessBookings` already projected `b.status.data.value`, so
the mark reaches the roster the moment it commits; `/api/bookings` gained the `status` column it was
dropping. The Roster tab badges the mark and offers the other value to the bound instructor of a class that
has started. The op carries an op-meta (`authContext: standing`, `{me.instructor}` + `{entity.sessionKey}`
auto-fills, `status` the one user-entered field) but is NOT service-catalog wired — matching TombstoneSession,
the sibling provider-hat op, which Facet reaches through the hat surface rather than a `permitsOperation` path.

*Live-verified end to end (`a922fe35`), and the browser earned its keep.* Signed in as Sam Okafor
(staff · instructor · member) on the running stack: scheduled a Sam-led class, booked it, and before the
start instant the roster read `1 booked — attendance opens when the class starts` with NO marking controls.
At the start instant the two controls appeared; Attended committed through the Gateway on Sam's real
session — which is the one thing the integration tests cannot prove, since they seed capability docs
directly rather than earning the grant through a signed-in provider hat. Core KV then read
`{value: attended, rate: standard, seat: 1, booker: …}` — the merge, live. No-show corrected it, and
cancelling the MARKED booking from My Classes tombstoned the booking and released BOTH
`session.<s>.seat1` and `session.<s>.bkr<booker>` — the wedge this increment's whole design guards
against, disproven on the real stack. Both verification classes were called off afterwards and their four
studio slot cells released, leaving the stack at its original eleven sessions.

The browser also caught a defect no gate would have: the mark committed but the roster re-read the
`wellnessBookings` LENS 700ms later, before it reprojected, so the badge did not appear and the click read
as a no-op until a manual Refresh. The roster now polls the read model on a bounded backoff and renders
only what the lens actually reports — a write that silently failed to project still reads as unmarked
rather than as done, which the optimistic alternative would have gotten backwards.

*Residual.* Row L32 (`instructor`/`serviceprovider` hats carry no record-administering ops) is UNCHANGED by
this fire and stays open: both wellness provider-hat ops target a `session`/`booking`, neither targets an
`instructor` record, so the profile/availability surface that row names is still absent.

**Fire W3 closes with this increment, and with it the persona-worlds initiative** — P1/P2, W0–W5 and W3 Inc 2
are all shipped. What remains are the named residuals, each already filed as its own board row.

### Fire W5 (Facet hats + landing) fire brief (build note, 2026-07-25)

**1 · Scope sentence (verbatim, §7.5 + §4).** "Facet — provider hat + hat-grouped landing (§4); demo-persona
cards gain the provider + multi-hat personas; seed-showcase adds Dr. Amara Osei (clinic provider), Kai the
laundry operator (serviceprovider), and makes one existing persona the §3.4 multi-hat human." §4 renderer
completion: "read `presentation.group` + `viaRole`/`resolvedVia` to group Home/nav by hat; add bound-provider
types to the `{me.<type>}` selfAnchor resolution so provider-targeted ops resolve." *Green (§8):* the §3.4
one-login-three-worlds demo, live-verified.

**2 · Scope-diff gate — two of the four named clauses ALREADY SHIPPED in W0; this fire is the remainder.**
Ground-checked live, not assumed:
- *seed-showcase personas* — **done.** Osei `seedOseiProvider()` (`scripts/seed-showcase.go:810-827`), Kai
  `seedKaiServiceProvider()` (`:884-905`), Sam the multi-hat human `seedSamMultiHat()` (`:1016-1039`:
  consumer+`residesIn` kept, +`frontOfHouse`/`worksAt`, +instructor `identifiedBy`/`teachesAt`/`ledBy`).
  `FACET_PROVIDER_NANOID`/`FACET_LAUNDRY_NANOID` printed both branches (`:244,254,400,401`).
- *bound-provider types in `{me.<type>}` resolution* — **done.** `edgeIdentitySpec.selfAnchors` stamps
  `provider`/`instructor`/`serviceprovider` (`packages/edge-manifest/lenses.go:445-449`) and
  `resolveTargetKey` already falls through to `selfAnchorKey(want)` (`cmd/facet/web/app.js:1526`). Re-verified
  rather than rebuilt — the false-bounce this gate exists to catch is the *inverse* (rebuilding shipped work).
- **REMAINING, and this fire's whole scope:** the hat-grouped landing + the persona cards.

**3 · The defect the remainder closes (verified, file:line).** A bound provider's binding is **displayable
nowhere and actionable nowhere**. `edgeIdentitySpec` projects the three `identifiedBy` bindings into
`selfAnchors` only — `{type, key}`, no name, no relation (`lenses.go:445-449`) — while `anchors[]` carries
only `residesIn` + `worksAt` (`:443-444`). So `splitAnchors` (`app.js:89-95`) sees nothing, and Dr. Osei's
Home renders the empty-residence branch (`renderHome`, `:536-559`) and her Me screen shows Places "None"
(`renderMe`, `:1084-1096`). Her ops fare no better: ops render only from service detail (filtered
`viaServices`, `:666`) or entity detail (filtered `dispatchTargetType === entityType`, `:777`), and
`SetProviderHours` (`clinic-domain/opmetas.go:138-159`, `TargetType: "provider"`) is attached to neither — it
**renders on no surface she can reach**, exactly the residue W0 named. `viaRole`/`viaRoleName` are projected
(`lenses.go:705-706`) and read by **zero** FE lines.

**4 · Touch-list + increment order.**
1. **Lens** `packages/edge-manifest/lenses.go` `edgeIdentitySpec` — `anchors` gains the three `identifiedBy`
   bindings stamped `relation: 'identifiedBy'` + `type` + name from each entity's `.profile`
   (provider `fullName`; instructor/serviceprovider `displayName` — confirmed
   `clinic-domain/ddls.go:315`, `wellness-domain/ddls.go:1660`, `service-domain/ddls.go:629`). Precedent to
   mirror: the existing 5-way `collect(...) + …` concat on the same lens's `selfAnchors` line — proven, not a
   new engine capability. Version 0.10.0 → 0.11.0 (`package.go:20`).
2. **FE lockstep (must ship with 1).** `splitAnchors` buckets `relation !== "worksAt"` into *homes*, so the new
   anchors would land in "My places" unless it becomes a three-way hat split in the same change. → `hatGroups(m)`
   (home / work / services) driving Home + Me.
3. **FE hat detail** — a services-hat chip opens a modal listing the ops that resolve against **that binding's
   key**, mirroring `openServiceDetail`/`openEntityDetail` (chip → modal → `opButton`) with `viaRoleName` as the
   grouping heading. This is what gives `SetProviderHours` a surface at all.
4. **Persona cards** — `deploy/demo/demo-up.sh:31-44` lists three hardcoded personas; add Osei + Kai and label
   Sam as the multi-hat human. The two NanoIDs are already printed by the seed and read by nobody.

**5 · Runnable green checks.** `node --check` + the `*.test.mjs` suite (`descriptor_autofill`, `staff_world`,
`dispatch_target`, `display_label` all touch the changed helpers) · `go test ./packages/edge-manifest/...` ·
`STRICT=1 go run ./scripts/lint-conventions.go` · `make verify-package-edge-manifest` (lens DDL touched) ·
live: reinstall edge-manifest → dev-login Osei on :7810 → her Home shows a **My services** hat → its detail
offers `SetProviderHours` → Sam's login shows **three** hats.

**6 · Gotchas.** Degenerate `{key: null}` collect entries are the expected OPTIONAL MATCH shape and drop
client-side (`app.js:1287`) — the new anchors must tolerate the same. An anchor with no `relation` is legacy
and still means residence (`app.js:87-88` comment) — the three-way split must keep that floor, not invert it.
NanoID alphabet (no `l/I/O/0`) if any id is minted. `anchorLabel` (`:104-110`) already falls through
name → containerName → `prettify(key)`, so a nameless binding degrades to a typed label, never a bare NanoID.

**As-built — W5 SHIPPED (2026-07-25, `e5b5c375`).** The lens stamps the three `identifiedBy` bindings into
`anchors` with their relation and each domain's declared profile name; `splitAnchors` became a three-way hat
split feeding "My places / Where I work / What I provide" on Home and Me; a binding chip opens a detail view
where that hat's ops render grouped by `viaRoleName`; the demo login offers all five personas.

The review found the surface's membership test wrong as first built, and the correction is the fire's one
design call worth recording. Filtering by `dispatchTargetType` alone offers a clinician **"Book appointment"
against her own record**: `CreateAppointment` declares `targetType: "provider"` exactly as `SetProviderHours`
does, and the clinic template permits it for every resident of the building, so binding any resident as a
provider surfaces it. The discriminator is `dispatch.class` — `"provider"` when the record is the op's
*subject*, `"appointment"` when it is the *counterparty* — which the manifest already projects. The filter is
dispatch-level curation, explicitly **not** authorization: the capability plane still decides what a hat may
do, per §6.4's "curation is UX; capability deltas are bugs".

Two residuals, filed as rows in the same docs commit. (a) **Only the clinic `provider` hat has ops at all** —
no op in `packages/` declares class or targetType `instructor`/`serviceprovider` (wellness targets a
`session`, service-domain a `service` instance), so those two hats render inert chips rather than inviting a
tap that says nothing. The three binding types were declared symmetrically and the op corpus is not, which is
a package gap, not a renderer one. (b) `openEntityDetail`'s cross-hat attachment, below.

**7 · Non-goals (this fire).** No narrowing of `openEntityDetail`'s cross-hat op attachment — it attaches any
op whose `dispatchTargetType` matches the row's `entityType` (`app.js:777`), so a multi-hat human sees another
hat's op on a shared type and it **fails closed in-script**. Correcting it needs a provenance stamp
`manifest.ent` rows do not carry; UX-only, filed as its own row rather than freelanced here. No attendance ops
(W3 Inc 2's row); no `cmd/<app>` FE changes; no contract edits; no new engine capability.

## 10a. Non-goals

No OIDC/IdP build; no SSO; no runtime archetype enum; no generic collections surface (named-deferred); no café
supplier domain; no Loupe changes; no change to Contract #1/#6/#11 semantics; no touch of planning artifacts
(FR24 amendment flagged for the planning lead); global-identity-hyperscale untouched (Andrew-gated).

**As-built — Inc 3 SHIPPED (2026-07-25, `4790992b`).** The landlord hat can write. All five ops carry a
`consumer` scope=self grant and a `require_manages` probe, and the design's §7.2 landlord question is now
closed on the write side as well as the read side.

*Two fork resolutions taken in-fire, both narrower than the brief proposed.* The brief's §3 keyed the probe
on `authContextTarget == actor`; review showed that confines a **scope=any** caller too — step 3 authorizes
a standing grant without inspecting the target at all, so an operator who happened to send its own key would
have had its writes narrowed to units it manages while its reads stayed portfolio-wide through the staff
wildcard anchor. The shipped predicate is `op.authTargetValidated AND target == op.actor`: the first clause
is the platform's own "I checked this target" bit, so a scope=any caller can never reach the probe; the
second excludes the task path, whose validated target is the renewal rather than the acting identity. That
also made the brief's §5 FE predicate unnecessary — the FE sends `authContext` whenever the session holds a
`manages` anchor, and safety is a server-side property rather than a claim about the FE's role knowledge
(which it could not have made anyway: whoami forwards role *keys*, not canonical names).

*Two platform defects the new grants made load-bearing, both fixed here rather than filed.* Neither is new,
and both would have shipped as regressions:
- **`matchPlatformPermission` decided on the first matching row** (`step3_auth_capability.go`). An actor's
  `platformPermissions` are the union of its roles, nothing sorts them, and the Gateway grants `consumer` to
  **every** actor it authenticates — so once these ops carried both a `consumer` scope=self and a role-derived
  scope=any row, whether the front-desk decide path worked at all depended on which `holdsRole` edge was
  written first. Two reviewers found this independently. The scan now continues past an unmet row and denies
  only after every match fails, reporting the first denial so a single-row actor's diagnostic is unchanged.
  The pre-existing `{any, self}` pairs (`CreateLeaseApplication`, `WithdrawLeaseApplication`,
  `SetApplicantProfile`, several café/clinic ops) were already exposed to this and are now covered too.
- **Loupe's `kernelRoleIDs` resolved `operator` alone** while claiming parity with lattice-pkg's
  `roleIDsFromBootstrap`. `loftspace-domain` was installable through Loupe only because it had been
  operator-only; the `consumer` grant would have hard-failed both its install and upgrade paths there.

*Ordering hardening from the review.* Every probe now answers ahead of its op's liveness check, so a denial
cannot report whether a leaseapp or renewal exists (the `4098ba21` existence-oracle posture, extended to the
four lease-signing ops that had kept the check first), and no denial names the unit — a caller holding a
resource key must not be able to read off which unit it belongs to.

*Tests.* Six guard tests plus three dual-scope matcher tests, all **mutation-verified**: disabling each guard
fails its own tests on the intended assertion, and the dual-scope tests reproduce the shadowed front-desk
denial against the old first-match scan. The `VerifyGuarantor` pair discriminates on the failure *message*
(`AuthDenied` vs `NoGuarantorToVerify`) because both legs reject — an outcome-only assertion would have
passed for the wrong reason.

*Residuals, filed as rows in the same docs commit:* `AssignUnitOwner` binds no actor (the op that CONFERS
management compares nothing to `op.actor`; operator-only is the only thing containing it), and the five ops'
op-metas still declare `authContext: standing`, so only the bespoke LoftSpace FE can drive the new self path
— Facet's descriptor renderer sends none. Not filed, deliberately: a `.profile` declared in `optionalReads`
is decrypted at step-4 hydration before any guard runs, so an unauthorized VerifyGuarantor leaves a vault
decrypt in the audit trail while leaking no plaintext — that is a platform-wide property of declared
sensitive reads, not this op's, and it belongs with the sensitive-read tracker work.

**Scope-diff gate: PASS** — every touch traces to "the grant rows + guards + FE flip for
`DecideLeaseApplication`, `SetRenewalTerms`, `VerifyGuarantor`, `CancelRenewal`, `SetListingStatus`". The two
`internal/` changes are not a widening: each is a defect that this increment's own grants would otherwise
have turned into a live regression, which is the "wear the other hat / do not bounce coupled work" rule, not
new scope. No op was left un-granted or un-guarded, and no adjacent mechanism was substituted for a named one.

### Fire W3 Inc 3 (wellness staff writes) fire brief (build note, 2026-07-26)

**Scope sentence (verbatim, lane row).** "`CreateStudio` and `CreateBooking`/`CancelBooking` scope=any are
`operator`-only, so W3's mint deletion left three FE surfaces with no authorized hat and they were removed
(ops + grants untouched). A plain `frontOfHouse` grant would be unconfined authority over any member's
booking; the clean form mirrors `CreateSession`'s workplace walk."

**The three surfaces** W3 (`d3a7cc7b`) removed rather than granted: **create-studio**, **book-for-a-resident**,
and **roster cancel-anyone**. They existed only because the app impersonated bootstrap; deleting the mint left
them with no authorized hat.

**Grounding ledger (verified live in code this fire).**

| fact | where |
|---|---|
| `CreateStudio` scope=any → `operator` only | `permissions.go:57` |
| `CreateBooking` / `CancelBooking` scope=any → `operator` only; scope=self → `consumer` | `permissions.go:71,78` / `:73-77,80-84` |
| `CreateSession` scope=any → `operator` + `frontOfHouse`, **confined** | `permissions.go:60-64`, guard `ddls.go:1128-1129` |
| the pattern to mirror | `if not workplace_exempt(): require_workplace(studio_locations(studio), …)` |
| `studio_locations` walks the studio's own `locatedAt` links | `ddls.go:1043-1053` |
| `require_workplace` exempts `op.authTargetValidated` — the scope=self path | `ddls.go:1012-1041` |
| session → studio is a **deterministic** `atStudio` link written at CreateSession | `ddls.go:1092,1144,1151` |
| the four DDL scripts are standalone; helpers are **duplicated per script** | `parts_of` at `:748,888,1295,1728`; `actor_holds_operator` at `:950,1345` |
| the workplace block exists **only** in `sessionDDLScript` | `ddls.go:973-1053` |

**Two forks, both resolved here as Winston (implementation calls, not contract).**

1. **`CreateStudio` has an OPTIONAL `location` payload field** (`ddls.go:797-802`; a studio with no location
   is legal and simply un-browsable). The confinement therefore falls out with no new concept: guard on the
   supplied location, and an omitted one yields an EMPTY candidate list, which `require_workplace` already
   denies for anyone but an operator. So **a staffer must name a location they work at, and an unlocated
   studio stays operator-only.** No new payload field, no new rule.
2. **The booking ops carry no location** — but they carry `session`, and `session -atStudio-> studio
   -locatedAt-> location` is two bounded link enumerations, structurally identical to `studio_locations` and
   annotated the same class-(e) read posture. So a `session_locations(session_key)` helper mirrors an
   established idiom; this is **package work, not a missing platform primitive**, and does not get bounced to
   `lattice.md`.

**Increment order** (each independently green):
1. **Package.** `session_locations` + the workplace helper block copied into `bookingDDLScript` (which
   already has `parts_of` + `actor_holds_operator`) and into `studioDDLScript`; `require_workplace` guards on
   `CreateStudio`, `CreateBooking`, `CancelBooking`; `frontOfHouse` added to those three scope=any rows;
   version bump + `verify-package-wellness`.
2. **FE.** Restore the three surfaces in `cmd/wellness-app/web/app.js`, gated on the `worksAt` staff anchor
   whoami already returns. Writes go browser-direct to the Gateway's `POST /v1/operations` — there is no
   generic `/api/op` in this app (`server.go:69-74`), so no new endpoint is needed. UX-then-FE.

**Non-goals.** The scope=self `consumer` path is untouched: `require_workplace` returns early on
`op.authTargetValidated`, so a member still books and cancels their own seat — the two guards are
complementary, which is the same property the café read boundary relied on this morning.
`SetBookingAttendance` keeps its instructor binding and is not re-guarded. No prelude refactor: the helper
duplication is the established idiom in this file (four scripts, four `parts_of`), and de-duplicating it is
its own item, filed — substituting it here would be an adjacent mechanism, not this one.

**Deliberately NOT deferred to a primitive.** Grounded both ways before concluding: the `atStudio` link is
deterministic and already written, and `kv.Links` enumeration is the sanctioned class-(e) posture, so nothing
here needs an engine change.

**As-built — W3 Inc 3 SHIPPED (2026-07-26, `073e2c8f` package + `ff9c7278` FE).** wellness-domain
0.13.1→0.14.0. `CreateStudio`, `CreateBooking` and `CancelBooking` widen their scope=any row to
`frontOfHouse`, and each script binds that standing path with `require_workplace`. Both forks resolved as
briefed: CreateStudio guards on the single location it is about to link (omitted → empty candidate list →
operator-only), and the booking pair resolve theirs through a new `session_locations` helper —
`session -atStudio-> studio -locatedAt-> location`, two class-(e) `kv.Links` enumerations mirroring
`studio_locations`. No platform primitive was needed, as the brief predicted.

Two things the brief did not anticipate, both decided here as Winston:

- **CancelBooking's guard must answer AFTER `require_matching_session`.** The caller supplies `session`, so
  confining on it before it is bound to *this* booking would let a staffer name a class at their own building
  and cancel a seat elsewhere. `workplace_confinement_test.go`'s substitution vector pins the order, asserting
  the rejection is `WrongSession` — the forSession link declared `optionalReads` so a mismatch reaches the
  script rather than faulting at hydration.
- **The `frontOfHouse` widening made `CreateStudio` fail the S1 gate** (a granted non-operator op with no
  descriptor). It gets a full op-meta whose `location` is the `{me.workplace}` self-anchor with no browsable
  `targetType` — maintenance-domain's `ReportIssue` shape, and the client-side mirror of the script's own
  empty-list denial.

**Two of the three FE surfaces are restored** (`cmd/wellness-app/web`), both `worksAt`-gated and live-verified
on the running stack: a **new-studio** form that asks only for a name and states which building it opens at,
and a per-seat **Release seat** control on the roster, absent for an instructor (who holds no CancelBooking
grant). **Book-for-a-resident is NOT restored** and is filed as its own row: its picker was fed by the
`/api/residents` directory W3 (`d3a7cc7b`) deleted, and republishing an unscoped one would undo the read
boundary that deletion drew — the clean form needs a workplace-scoped member read, which is a new lens, not a
UI restore.

### W3 Inc 4 — the front desk books a member in (fire brief, 2026-07-26)

**Scope sentence (verbatim, from `backlog/verticals.md`).** *"The front desk cannot book a member in. The third
surface W3's mint deletion removed. Its picker was fed by the `/api/residents` directory that deletion also
removed, and republishing an unscoped one would undo the read boundary it drew — `CreateBooking`'s staff grant
+ workplace guard already ship, so all that is missing is a member list scoped to the staffer's covering
locations (the `cafeLeaseWorkplaces` shape, on wellness bookers)."*

**Scope-diff gate.** The row's premise re-verified both ways, live: `CreateBooking` scope=any *does* already
grant `frontOfHouse` (`permissions.go:92`) and its script *does* already confine a non-operator caller with
`require_workplace(session_locations(session), …)` (`ddls.go:1683`) — so **no write-side change is in scope**,
and adding one would be substituting an adjacent mechanism. What is missing is exactly one read: a member
directory a staffer may see. That is a **lens**, which is package work by the no-paper-over rule, not a
platform primitive — nothing is filed to `lattice.md`.

**Verified touch-list.**
- `packages/wellness-domain/lenses.go` — a fourth bucket const + a fifth lens, mirroring
  `packages/cafe-domain/lenses.go:102` (`leaseWorkplacesSpec`) with the applicant column added.
- `packages/wellness-domain/{manifest.yaml:70,package.go:96,package_test.go:45}` — the manifest lens list,
  the version, and the S6 structure pin (`Lenses: 4` → `5`).
- `packages/wellness-domain/lens_cypher_test.go:318` — the `coveringLocations` proofs to mirror.
- `cmd/wellness-app/residents.go` — the file the handler joins; `readauth.go:103` (`covers`) is the
  intersection already built for the roster boundary, reused unchanged.
- `cmd/wellness-app/web/app.js:585` — the Roster view, whose stale header comment ("Nobody cancels a seat from
  here") Inc 3 falsified and this fire corrects while editing the same block.

**Precedents to mirror.** `cafeLeaseWorkplaces` for the lens shape and its zero-hop / `*0..7` bound reasoning;
`cmd/cafe-app/frontdesk.go:55` for a staff-only handler that intersects rows against a resolved visibility;
`lease-signing`'s `leaseApplicationSummary` (`lenses.go:781`) for making `applicationFor` a REQUIRED match.

**Increment order + green checks.**
1. Package: the `wellnessMembers` lens → `go test ./packages/wellness-domain/`.
2. App: `GET /api/members` → `go test ./cmd/wellness-app/`.
3. FE: the roster's book-a-member control → `node --check`, then in-browser on the running stack.

**In-scope gotchas.** (a) The picker is an **affordance, not the authority** — the write boundary confines by
the *session's* location, never by who the booker is, so the member list narrows what is *offered* and the
Starlark guard still decides. (b) A member holding two leases is two rows, deliberately: the lease is what
carries both the covering location and the resident-rate hint. (c) `CreateBooking` re-derives residency
itself, so the row needs no approval column — passing `leaseAppKey` is enough.

**Non-goals.** No write-side change. No display names (identity carries none; the picker shows bare keys, as
the deleted one did). Not the roster session-picker's own cross-building narrowing — that is its own filed row.

**As-built — W3 Inc 4 SHIPPED (2026-07-26, `0b14d0f7`).** wellness-domain 0.14.0→0.15.0. `wellnessMembers`
projects one row per lease carrying its member and `coveringLocations`; `GET /api/members` intersects that set
with the caller's own `worksAt` keys and refuses a member or an instructor outright. The FE's book-a-member
control lives on the Roster, `worksAt`-gated. The brief's premise held: no write-side change was needed and no
platform primitive was filed.

The adversarial review found three things the brief did not, all fixed before the merge:

- **The lens had to drop refused applicants to be a MEMBER directory at all.** `DecideLeaseApplication`
  tombstones neither the leaseapp nor its `applicationFor` link on a decline, so the first cut would have
  handed the front desk the identity of everyone who ever applied to their building — refused applicants
  included — labelled as members. The first fix reached for `.tenancy` presence, the signal `CreateBooking`'s
  resident-rate check reads, on the reasoning that the two sides should agree. **Driving it against the live
  stack disproved that** (`8b8f7c8b`): thirteen leases, thirteen applicants, and not one `.tenancy` among
  them — every demo lease is signed and awaiting a landlord, so the front desk would have opened a picker that
  could never offer anybody and been correct about it. A rate is a claim about money and belongs on proof of
  tenancy; a directory is a claim about who is around, and somebody living in the building on an undecided
  application is exactly who the desk books in. The lens now projects `landlordDecision` and the reader drops
  only `declined` — projected rather than filtered in the cypher because the column is three-state and only
  two states are decidable against a literal. The verdict itself stops at the server. Discriminating vectors
  on both sides pin it (a refusal beside an approval; a refusal beside an undecided).
- **The Book button was a one-shot.** The first cut re-bound it per render via `cloneNode(true)`, which copies
  the reflected `disabled` attribute — so after one successful booking every later render re-cloned a dead
  button. It is now bound once at init, resolving its class from the session picker at CLICK time, which also
  closes the second half: two overlapping roster renders could otherwise leave the button submitting against a
  class the staffer had already navigated away from. A render-generation token stops a slow render painting a
  picker over a newer one — including beside a roster that just 403'd.
- **The control offered submits that could only fail.** It is hidden for a class that has already begun
  (`SessionInPast`) or is full (`SessionFull`), the same reasoning that already excludes the already-seated
  (`DoubleBooked`) and keeps the attendance control hidden until a class starts.

Also aligned in passing: `cmd/wellness-app`'s `covers` trimmed a projected location before testing it for
emptiness but compared the untrimmed value — fail-closed, so a padded key silently denied. `cmd/cafe-app`
had already fixed exactly this and says why; wellness now matches.

**Live-verified end to end** on the running stack, wellness-domain 0.16.0 diff-applied with no teardown (the
lens backfilled 13 rows on load — no Refractor restart). A `worksAt` staffer is offered 6 of those 13, the 6
their building covers; a plain member is 403'd on the same endpoint; the response carries neither the covering
set nor the landlord's verdict. Booking a member through the picker committed, projected, and came back on the
roster at the standard rate — correct, since that lease carries no `.tenancy` — with the picker down to 5 and
the Book button live for the next one, which is the one-shot defect above, disproven in the browser rather
than only in a test.

**Named residual.** The directory is lease-anchored, so a genuine non-resident guest cannot be booked through
the FE even though `CreateBooking` itself constrains only the session's location and never the booker. That is
a product question (do wellness classes admit non-residents?), filed as its own row rather than decided here.

### Café confined ledger credit — fire brief (build note, 2026-07-26)

**1 · Scope sentence (verbatim, `backlog/verticals.md` row):** *"`CreditAccount` is `operator`-only in every
ledger, so W4's mint deletion leaves Record Payment with no authorized hat and it is removed. A plain
`frontOfHouse` grant would be unconfined credit-any-account authority in a package with no workplace binder;
needs a confined ledger credit. Consumer: the café front-desk payment flow."*

**2 · Verified touch-list** (`file:line`, checked live at `6f5610d8`):

- `packages/cafe-ledger/permissions.go:24-29` — `CreditAccount` scope=any grants `[operator]` only.
- `packages/cafe-ledger/manifest.yaml:36` — the same grant; `package_test.go` pins manifest↔Go sync (S6).
- `packages/cafe-ledger/scripts.go:113-253` — `transactionDDLScript`. Today it references **no** `op.actor`
  and **no** `kv.` at all: it is a pure known-key, state-only script.
- `packages/cafe-ledger/scripts.go:249` — the `if ot == "CreditAccount"` branch of `execute`.
- `packages/cafe-ledger/scripts.go:92-103` — the `heldFor` link (`cafeaccount` → `leaseapp`), written at
  `CreateAccount`; this is the first hop of the confinement chain.
- `packages/cafe-ledger/package.go:62` — version `0.2.0` (a package edit needs a bump to diff-apply).
- `cmd/cafe-app/ledger.go:151-156` — `/api/ledger` already returns `accountKey` + `balanceCents`; the FE
  needs no new read and no new lens.
- `cmd/cafe-app/readauth.go:114-140` — `subjectHats.workplaces`; staff ledger reads are already
  workplace-confined (`facet-staff-worlds-design.md` §9), so the write guard mirrors the read boundary.
- `cmd/cafe-app/web/index.html` — three panes (`pos` / `frontdesk` / `resident`); no payment control exists.

**3 · Precedents to mirror** (all `packages/cafe-domain/ddls.go`):

- `:414-416` the three `WORKPLACE_*` constants · `:418-439` `actor_holds_operator` · `:441-468`
  `worksAt_covers` · `:470-478` `workplace_exempt` · `:480-509` `require_workplace` · `:511-522`
  `leaseapp_unit`.
- **Call-site ordering** — `:683-691` (`Charge`): gate on `workplace_exempt()` *first*, then
  `require_workplace([resolver(...)], what)`. Starlark evaluates arguments eagerly, so the gate is what stops
  an operator paying to walk the target's topology.
- **Read posture** (Contract #2 §2.5) — every `kv.Links` enumeration and its data-derived follow-up read
  carries a `# read-posture: (e)` annotation with the relation and why it is bounded.

**4 · The confinement chain.** `CreditAccount` names an account, not a location, so the guard resolves one:

```
payload.accountKey  -heldFor->  leaseapp  -appliesToUnit->  unit  -containedIn*->  building
```

`worksAt_covers` then walks that unit's containment chain, testing the actor's deterministic `worksAt` link at
each level. The account→lease hop is the one resolver `cafe-domain` does not already have; the rest is its
idiom verbatim. Every hop reads a link the *platform* wrote (`heldFor` at `CreateAccount`, `appliesToUnit` at
`CreateLeaseApplication`), never a payload field, so the workplace a credit resolves to cannot be forged.

**5 · Increment order + runnable green checks.**

1. **Inc 1 (package)** — widen the `CreditAccount` grant to `frontOfHouse`, add the guard on that branch only,
   bump to `0.3.0`, sync `manifest.yaml`. Green: `go test ./packages/cafe-ledger/...`, then the full gates.
2. **Inc 2 (FE)** — a staff-only Record Payment control on the resident-lookup pane, which already renders the
   lease's balance and carries its `accountKey`. Green: `node --check`, then in-browser against the stack.

**6 · Non-goals.**

- **`loftspace-ledger` stays `operator`-only.** The row's *"in every ledger"* states the general fact; the
  named consumer is the café front desk. Widening loftspace's credit has no filed consumer and would be the
  adjacent-mechanism substitution the scope-diff gate exists to catch.
- **`DebitAccount` stays `operator`-only** and stays unguarded. It is playbook-dispatched (the
  `cafeTabSettlement` Weaver target) and gains no non-operator path here, so it needs no binder.
- No new lens, no new endpoint, no `contextHint` change (the chain is entirely class-(e) live reads), no
  contract change, no café supplier hat (§7.4, named-deferred).

**Scope-diff gate.** Brief vs the row, item-by-item: narrow-only — the row asks for *a confined ledger credit*
plus its front-desk consumer, and that is exactly Inc 1 + Inc 2. No adjacent mechanism substituted; the
declared dependency (a café workplace binder to mirror) re-verified both ways — `cafe-domain` has the idiom at
the six line ranges above, and `cafe-ledger` genuinely has none, which is what made the row's premise true.

**As-built — SHIPPED, with one scope correction the brief did not anticipate.** Adversarial review
refuted the brief's central assumption. The brief treated "widen the grant + confine the script" as
self-contained inside `cafe-ledger`; it is not. A standing grant is matched by **operationType string
equality alone** (`processor.matchPlatformPermission`), and the envelope's `class` — which selects the
DDL that will run — is a client hint step 3 never reads. `loftspace-ledger` and `clinic-ledger` each
declare their own `CreditAccount`, and every ledger's transaction DDL admits it in `permittedCommands`.
Granting `frontOfHouse` the name `CreditAccount` here would therefore have authorized every front-desk
actor in every vertical against LoftSpace's and Clinic's ledgers, which carry no workplace guard at all
— a cross-vertical escalation produced by a one-word permission edit, invisible in this package's diff.

The collision itself was already known and already mitigated in the wrong plane: `cafe-domain`'s Weaver
target pins `class` explicitly because "DebitAccount is claimed by 4 installed ledger DDLs"
(`targets.go`). Pinning the class fixes *dispatch*; authorization never sees it.

So the café's payment op ships as **`CreditCafeAccount`** — the same vertical-prefixing device this
package already applies to `cafetransaction` and `.cafeLedgerAccount`, and for the same reason.
`CreateAccount` and `DebitAccount` keep their bare names deliberately: granted to `operator` alone, and
the operator is unconfined everywhere by design, so for them the collision confers nothing.

Two additions beyond the brief's §5, both compelled rather than opportunistic:

- **`opmetas.go`** — the S1 gate requires a full descriptor for any op granted beyond the trusted-tool
  roles. Widening to `frontOfHouse` is exactly what makes `CreditCafeAccount` user-facing, so the
  descriptor is part of the grant, not an extra.
- **`lint-package-standard` S9** — a new corpus-wide gate: an operationType granted beyond the
  trusted-tool roles must be admitted by exactly one package. The corpus passes with zero debt, and the
  gate was verified to FIRE by re-applying the vulnerable name (it names both sibling claimants). Without
  it the invariant this fix depends on binds nobody, and the next author re-introduces the hole by
  choosing an obvious name.

**Named residuals.** One is filed: the `worksAt_covers` single-parent row gains `cafe-ledger` as a third
copy site, so whoever fixes that walk fixes all three. One is deliberately **not** filed: the sibling
ledgers' `CreditAccount` stays operator-only and unconfined. That is correct today — the operator is
unconfined everywhere by design — and it has no consumer, since neither Clinic nor LoftSpace has filed
demand for a front-desk payment. A row naming no consumer would just age. The invariant is held by the
S9 gate instead, which will red the fire that widens either one rather than let it ship the hole.
