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
   `worksAt`, `identifiedBy`-inverse), matching what `manifest.me` shows Facet. This is the *only* new
   app-facing query surface; it is what lets a vertical render hats without joining the SYNC plane.
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
hardening held (FACET_STAFF_NANOID stayed Dana despite Sam gaining frontOfHouse). **Known tail (filed,
lattice lane):** Sam's `manifest.me` summary row is frozen at his pre-hat first-projection state (only
consumer+residesIn, 2 of 5 selfAnchor types) — the Personal-lens update-over-existing reconciliation gap
([[project_capability_projection_reconciliation]]), presentation-only (authz + per-hat data all correct),
which stalls the multi-hat Work-tab/hat-summary display until that Refractor gap is closed. **CI miss caught
post-merge:** `verify-package-clinic-domain` (stack-gate only, invisible to `go test`) asserted the pre-W0
provider-DDL command count; fixed in `a8069d16`.

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

## 10a. Non-goals

No OIDC/IdP build; no SSO; no runtime archetype enum; no generic collections surface (named-deferred); no café
supplier domain; no Loupe changes; no change to Contract #1/#6/#11 semantics; no touch of planning artifacts
(FR24 amendment flagged for the planning lead); global-identity-hyperscale untouched (Andrew-gated).
