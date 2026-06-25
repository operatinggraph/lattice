# Lattice Backlog & Roadmap (Phase 3+)

**Owner:** Andrew (architect / planning lead). **Status:** living document.

This is the single consolidated backlog for everything deferred past the Phase 2 orchestration core,
plus the active next initiative. It supersedes the scattered deferral lists —
`lattice-architecture.md` "Open Items (Phase 3+)", `epics/index.md` "Deferred Architectural
Capabilities", and the per-component *Implementation status / Deferred* sections in
`docs/components/*` — which now point here. Frozen architecture *decisions* (e.g. the D1 read-path-auth
rubric) stay in `lattice-architecture.md`; this doc tracks *what to build next*, not *how it is designed*.

**Scales.** Importance: ★ low · ★★ medium · ★★★ high. Size: XS · S · M · L · XL (relative epic/story effort).

---

## Progress board

The single index of what is in flight and what has landed. Everything in the themed tables below
defaults to **📋 Backlog**; when an item is picked up it runs the normal loop (design → review → build →
review → commit) and surfaces here as **🏗️ Active → ✅ Done (commit)**. This board is the *index* —
per-item detail lives in design / story docs + git history, never in agent memory and never in a
`sprint-status` file (house rule).

| Item | Status | Ref |
|---|---|---|
| Loom control plane *(Loupe blocker #1)* | ✅ Done | `implementation-artifacts/loom-control-plane.md` |
| Loupe — view & control app | ✅ Done (v1 stab) | `implementation-artifacts/loupe-v1.md` |
| Large-file / binary handling — **v1a** (attach/read/detach) | ✅ Done (built + 3-layer-reviewed; merged to main) | `implementation-artifacts/large-file-binary-design.md` §1–§18 |
| Large-file / binary handling — **v1b** (GC) | ✅ Done (Option A: objectLiveness lens + Weaver directOp + epoch-CAS + object-store-manager; full Loop A+B convergence e2e green in CI; 3-layer-reviewed; merged to main; contracts #7 §7.2 + #10 §10.8 directOp.reads ratified) | `implementation-artifacts/large-file-binary-design.md` §20 |
| Refractor substrate inner-package migration | ✅ Done (Path B, d420ca4) | `implementation-artifacts/substrate-migration-plan.md` |
| Structured adapter result *(terminal-failure producer)* | ✅ Done (828f24d) | `implementation-artifacts/structured-adapter-result-design.md` |
| Async external-reply | ✅ Done (increments 1–3: 0860fb1, 0f85d45, 3504db6) | `implementation-artifacts/async-reply-design.md` |
| location-domain — spatial base package *(SL.1)* | ✅ Done (ae3a056) | `implementation-artifacts/service-location-design.md` |
| service-location — service-access authZ scheme *(SL.2)* | ✅ Done (e4af07c, 715b14b) | `implementation-artifacts/service-location-design.md` |
| Loupe live system-map *(★★★ experience)* — **data layer** (`/api/systemmap` assembler) | ✅ Done (backend + unit tests; lead-reviewed, gates green) | `implementation-artifacts/loupe-v1.md` (API list) |
| Loupe live system-map *(★★★ experience)* — **FE landing view** | ✅ Done (UX-then-FE: Sally spec → FE Engineer build → Winston in-browser-verified against `make up-full` + lead-reviewed; gates green) | `implementation-artifacts/loupe-system-map-ux.md` |
| Task-content — **`my-tasks` lens self-describing** | ✅ Done (cypher aspect-hops `op.canonicalName`/`op.description` → `operationName`/`operationDescription` on the read-model row; refractor e2e asserts; for all consumers) | `packages/orchestration-base/lenses.go` |
| Loupe task-inbox — **operator data layer** (`GET /api/tasks`) | ✅ Done (all-identity task list + op label resolved from forOperation meta; unit-tested, gates green) | `cmd/loupe/tasks.go` |
| Loupe task-inbox — **FE tab** | ✅ Done (Tasks tab: cards per task with op name/description, assignee, expiry, status filter, "Complete in Submit Op →" deep-link; in-browser-verified against a live task, no console errors) | `cmd/loupe/web/{index.html,app.js,style.css}` |
| LoftSpace property domain — **Increment 1: `loftspace-domain` package** | ✅ Done (the `.listing` + `.address` aspect-type DDLs on location-domain's `vtx.unit` + `SetListing`/`SetUnitAddress` ops on a single `loftspaceListing` vertexType DDL; permissions, manifest, `make verify-package-loftspace-domain` (67 assertions) + unit tests; lead-reviewed, gates green; **live-verified** against the running stack — minted a unit, set listing+address through the real Processor, all three negative guards fire) | `packages/loftspace-domain/` |
| LoftSpace applicant app *(★★★ experience — top priority)* — **UX design** | ✅ Done (Winston-ratified, build-ready proposal — four surfaces: Browse&Apply / My Applications / Tasks / Documents; trusted single-identity-in-view model like Loupe; new `cmd/loftspace-app` on `:7788`; the only platform gaps are two headlessly-unit-testable read assemblers `/api/listings` + `/api/applications`, everything else lifts shipped Loupe handlers; 5-increment FE plan). No contract / architecture gate. 🏗️ **FE Increment A (scaffold + intake) is the queued next pick.** | `implementation-artifacts/loftspace-applicant-app-ux.md` |
| LoftSpace property domain — **Increment 2: lease-signing integration** | ✅ Done (047a67e) — `CreateLeaseApplication` now **requires** a live location-domain `unit` + writes the `appliesToUnit` link + optional `.terms` aspect (moveInDate/leaseTermMonths/requestedRent); the `leaseApplicationComplete` convergence lens walks `appliesToUnit` and projects `unitKey`/`unitAddress`/`unitRent` as **informational** columns (NOT in `violating` — unit is required at create, no `missing_unit` gap). One-row-per-anchor preserved (0..1). Cross-package e2e (`make test-lease-convergence`) mints the unit via `CreateLocation`+`SetListing`/`SetUnitAddress` and asserts the columns project end-to-end; 2 new cypher tests + UnknownUnit guard. All gates green; lead-reviewed. | `packages/lease-signing/{scripts,ddls,lenses}.go` |
| Loupe agent-activity console *(★ experience — deferred)* — **design** | ⏸️ Shelved by Andrew (2026-06-25) — read-seam options rejected; revisit once the dependency map + ops-state data-home mature. Design retained; **do not build now.** Experience layer redirected to the vertical-app FEs. | `implementation-artifacts/loupe-agent-activity-console-design.md` |
| _all other items_ | 📋 Backlog | see themed tables below |

---

## Active initiative — Loupe: the View & Control app *(first Edge Lattice prototype)*

> **Name:** *Loupe* (tentative). A jeweler's loupe is the tool you inspect a crystal through — apt for
> a window onto the lattice.

**What it is.** An internal **view-and-control client** for a running Lattice deployment: browse Core
KV (vertices / aspects / links), submit operations, install / uninstall capability packages, drive each
component's control plane (Refractor / Weaver / Loom), and observe Health KV. The first concrete UI on
top of the platform.

**Framing (the "a-ha").** Loupe is an **internal, trusted-operator tool**, but built *around the
Edge-node local-first machinery* — the same substrate + VAL mirror + reconcile-by-revision a real Edge
Lattice node would use — so it doubles as the **first prototype of Edge Lattice** without taking on the
Edge security layer. It is a stepping stone: prove the local-first view/control loop now; grow into the
per-user sovereign node later, once the deferred security pieces land.

**Non-goals (explicitly OUT — these stay Phase 3+).** Loupe runs as a **single trusted / privileged
identity** (like the CLI / admin), so this initiative does **not** build:

- per-user **authN / authZ**,
- the **Gateway**,
- **read-path authorization** (D1),
- **Personal Lens** / per-user filtering.

Loupe reads the **full** graph directly as a trusted client; per-user scoping is a later Edge evolution.

**Capabilities (v1).** Read Core KV + lens projections · submit ops (forms driven by DDL
self-description: `inputSchema` / `fieldDescription` / `examples`) · install / uninstall packages ·
Refractor / Weaver / Loom control ops · Health KV dashboard · view + upload large binaries (photos,
lease PDFs).

**Enabling work (the picked "Now" set) — ✅ all shipped (see the Progress board).**

| Enabling item | Why Loupe needs it | Imp | Size |
|---|---|---|---|
| **Loom control plane** | *Hard blocker.* Refractor + Weaver expose `lattice.ctrl.*` responders; Loom has none. Build `internal/loom/control` + `cmd/lattice/loom` + a `lattice.ctrl.loom.*` responder (list running instances, pause/resume consumers, inspect/fail an instance), mirroring `internal/weaver/control`. | ★★★ | M |
| **Large-file / binary handling** | Loupe shows + uploads profile photos and lease PDFs. NATS Object Store (chunked, content-addressed); the graph holds a pointer-aspect, the store holds the bytes; blobs never flow through the Refractor. *Authorization simplifies under the trusted-tool model* (binds to the trusted identity, not per-user). | ★★ | M–L |
| **Refractor substrate inner-package migration** | Hygiene + directly supports "around Edge machinery": ~30 `internal/refractor` files still hold raw `nats.go` / `jetstream` handles; a clean substrate boundary is what makes a local / embeddable node tractable. Needs substrate Watch / UpdatesOnly / NumPending / durable-consumer helpers first. | ★★ | M |

**Supporting / not blocking.** `UI Form Schema aspect` (brainstorming #52) would standardize form
rendering (DDL self-description already suffices for v1) · NATS **WebSocket** transport if Loupe is
browser-based (desktop / TUI / Electron use the native client) · Processor + Bridge have **no** control
plane — Loupe reads their Health instead (a minimal admin endpoint is optional, later).

**Open design questions for the epic.** Transport + host (desktop / TUI / Electron / browser-WS) ·
does Loupe embed a **local VAL mirror** via reconcile-by-revision (the Edge machinery) or read live
only · whether to add a thin read/query convenience surface (direct KV + lens reads work for v1).

---

## Now — the experience layer (UX + FE)

Prior near-term picks (Loom control plane, large-file/binary, Refractor substrate migration) all shipped; the
ride-along cleanups are parked (see **Parking lot**). **Active focus: the vertical-app front-ends — built
ambitiously by the UX Designer (Sally) + the FE Engineer.** Flow: **PO scopes → Sally designs the UX → FE
Engineer builds + verifies in-browser → Winston admits.** M/L is fine (risk-bounded L2 + multi-fire).

- **Vertical app front-ends (★★★) — the top experience priority.** Whatever the Vertical POs (LoftSpace,
  Clinic) decide their apps should do. Greenfield per app: the PO defines the capability, Sally designs, the FE
  Engineer builds (Loupe's vanilla HTML/CSS/JS stack as the default). LoftSpace is exercisable live with a
  build-ready path — the `loftspace-domain` package (property / unit / listing; design build-ready) → the
  applicant app FE (intake → status tracker → task inbox → document upload). Be ambitious — new app
  capabilities welcome.
- **Loupe operator surfaces** — the live "system map" landing page is ✅ shipped. The **agent-activity console
  is ⏸️ shelved** (Andrew, 2026-06-25 — read-seam options rejected; revisit later). Not an active pick.

---

## Vertical demand backlog (PO discovery)

Filed by the Vertical PO discovery loop (demand side). Each item is tagged with the **vertical** and
the **owner** (FE = Sally + FE Engineer · pkg = Package Designer · platform = component owner). Scored
Imp ★ / Size. The Steward + FE Engineer pick these up; the PO only files.

| Item | What it is (PO view) | Vertical | Owner | Imp | Size |
|---|---|---|---|---|---|
| Property / Unit / Listing domain + richer application | ✅ **Design build-ready** (`implementation-artifacts/loftspace-property-domain-design.md` — decisions resolved by PO+Winston, adversarially reviewed + hardened, no contract change / no Andrew gate; new `loftspace-domain` pkg owning `vtx.unit`, `unit` required at create, convergence-lens walks to it for display). ✅ Increment 1 (the `loftspace-domain` package — `.listing`/`.address` aspects + `SetListing`/`SetUnitAddress`) **DONE + live-verified**; ✅ Increment 2 (lease-signing integration: required `unit` at create + `appliesToUnit` link + `.terms` aspect + convergence-lens walk projecting `unitKey`/`unitAddress`/`unitRent`) **DONE (047a67e), cross-package e2e green**; 🏗️ Increment 3 (applicant FE) next — now unblocked. The biggest *product* gap: `vtx.leaseapp.<id>` is a bare shell (root `{}` + one `applicationFor` link). The vertical models the *workflow* but not the *thing being leased* — there is no property/unit/listing, no rent, lease term, move-in date, applicant income/employment, co-applicants or guarantor. "What am I even applying to lease?" is unanswerable today. Needs a `loftspace-domain` (or similar) package: a unit/listing vertex type + application-detail aspects, with the convergence lens able to walk to the unit. Foundation for a real applicant app. | LoftSpace | pkg + FE | ★★★ | L |
| Decline / manual-review application outcome | **Re-scoped 2026-06-25 (Steward) — the auto-approve correctness defect is FIXED; residual is product/FE.** The PO's "every application auto-approves" was the *default fakes* passing on a happy-path run, not a missing failed path: 828f24d (verified in-tree) already ships the full terminal-failure consumption — `RecordLeaseServiceOutcome` reads/validates `status ∈ {completed,failed}` from the bridge payload (`scripts.go:363 required_status(p)`, **no** hard-coded `completed`), writes it to `.outcome`, and the `leaseApplicationComplete` lens counts only `completed`, so a **failed** check keeps the application's gap violating (it does NOT auto-pass) — pinned by `lens_cypher_test.go` (failed bgcheck + failed payment). **Remaining (product, not correctness):** (1) a *terminal* **rejected / manual-review** state — today a failed check leaves the application permanently gap-violating ("blocked forever") with no terminal disposition; (2) a way to **drive a decline live** (the fakes only return `failed` for a configured subject/decline — not reachable from the CLI/Loupe today); (3) a **lens/FE surface** showing the outcome (declined / approved / pending). Now S–M, not M. | LoftSpace | pkg + FE | ★★ | S–M |
| Human-readable task content on userTasks / assignTasks | **Re-scoped 2026-06-25 (Winston) — NOT a platform/package gap; the content is reachable today.** Task *relationships are links* by design (Contract #10 §10.1): the `my-tasks` lens already projects `forOperation: vtx.meta.<id>`, and that op meta-vertex carries `.canonicalName` + the DDL self-description (inputSchema / fieldDescription / examples) Loupe **already reads to render op-submit forms**. So a task-inbox renders {op canonicalName = title, op description = instructions, op inputSchema = the form} by resolving the `forOperation` meta — the same Core-KV meta-read pattern Loupe's `resolveLens` already ships. Decision: **project the op's human label from the `forOperation` meta, do NOT stamp a `.prompt` aspect** (single source of truth; no dual-write; preserves the task root's deliberate `{status,expiresAt}`-only / NO-aspects invariant; no §10.8 dispatch-`reads` / contract touch). ✅ **Platform fix DONE** — the `my-tasks` lens cypher now aspect-hops `op.canonicalName.data.value` / `op.description.data.value`, so the per-identity read-model row is self-describing for **every** consumer (`operationName` / `operationDescription`); refractor e2e asserts it. ✅ **Loupe operator task-inbox data layer done** (`GET /api/tasks`: all tasks across identities, link-sources assignee/op/target, resolves the op label; unit-tested). Residual: the FE tab (UX-then-FE) + the optional op-`description` authoring nicety (ensure each userTask op carries a human `description`, not just the machine `canonicalName`). | LoftSpace (all) | FE | ★★ | S |
| LoftSpace applicant app — scoped FE | 🏗️ **Active — UX ratified, FE build queued.** ✅ **UX design build-ready** (`implementation-artifacts/loftspace-applicant-app-ux.md` — Winston-ratified; four surfaces, trusted single-identity-in-view model, `cmd/loftspace-app` on `:7788`, two new headlessly-testable read assemblers + lifted Loupe handlers, 5-increment plan). Prerequisites now met: property-domain (Increments 1+2) shipped + task-content self-describing. **Verified live: the vertical is headless** — every step (apply, run checks, complete PII, sign) had to be driven via `lattice op submit` as the system actor; an applicant has no way in. Scoping the greenfield FE the *Now* section flags generically: (1) application-intake form (driven by DDL self-description), (2) "my application status" tracker (submitted → checks → sign → decision), (3) task inbox to complete `RecordIdentityPII` + `SignLease`, (4) document upload (ID / lease PDF, via objects-base). Default to Loupe's vanilla HTML/CSS/JS stack. **Next pick: FE Increment A (scaffold + intake).** | LoftSpace | FE | ★★★ | L |
| Close assignTask tasks when their gap is satisfied | **Verified live bug.** After `SignLease` wrote `.signature` (closing `missing_signature`), the §10.8-spawned `SignLease` task stayed `status:"open"` — nothing reconciles an assignTask task when the underlying fact lands via another path. An applicant inbox would show a permanently-stale "Sign your lease" item after they have signed. The gap is the source of truth; the task that actuates it has no closure path on gap-satisfaction. | LoftSpace (all) | platform (orchestration) | ★★ | S–M |
| `clinic-domain` package — patient / provider / appointment / visit model | **Static (design-only — no clinic package exists yet; `ls packages/` has none).** The forcing-function vertical has zero domain: no patient, provider, appointment-slot, visit/encounter, or scheduling aspects. Like LoftSpace's missing property domain, nothing is bookable. Needs a `clinic-domain` package: `vtx.patient.*`, `vtx.provider.*`, `vtx.appointment.*` (or slot) vertex types + scheduling/availability aspects, with a convergence lens that walks patient↔appointment↔provider. Foundation for every clinic flow below. | Clinic | pkg + FE | ★★★ | L |
| Recurring `@every` schedules — the clinic forcing function | **Static, verified by grep: `@every` has NO consumer.** The `core-schedules` stream is bootstrapped with `AllowMsgSchedules` (primordial.go:198 advertises `@at/@every`), but every component — bridge poll/timeout, the Weaver temporal lane — uses **`@at` one-shot only** (contract §10.4: "Phase 2 uses @at one-shot"). Clinic is the demand that pulls `@every` into existence: appointment reminders ("remind 24h before"), recurring provider availability ("Dr.X Mon/Wed 9–5"), recurring follow-ups. This is the deferred platform work the clinic vertical exists to force (agentic-ops-design §5/§11). Build the recurring-schedule emit + re-arm path on the temporal lane and a vertical that uses it. | Clinic | platform (orchestration / Weaver) | ★★★ | M |
| Appointment scheduling — conflict + temporal availability | **Static, contract-grounded.** Capability-KV §06 (L354–359) **explicitly defers** temporal availability (recurring schedules, availability windows, double-book rejection) out of Phase-1 scope to "a Phase 2 mechanism / the operation's own Starlark logic." A clinic `BookAppointment` op must reject a slot that's already taken or outside provider hours — there is no slot-uniqueness or availability-window enforcement primitive today. Clinic forces this gap concrete: provider-hours + slot-uniqueness enforcement at op time (and surfacing "why was this rejected" to the booker). | Clinic | pkg + platform | ★★★ | M |
| Clinic FE — patient booking + provider schedule | **Static (greenfield, like LoftSpace's headless app).** Scopes the generic *Now*-section vertical-FE item for clinic: (1) patient self-booking (pick provider/service → available slot → confirm), (2) "my appointments" tracker (upcoming / past / cancel / reschedule), (3) provider day/week schedule view, (4) clinic-admin slot & availability management. Default to Loupe's vanilla HTML/CSS/JS stack. Depends on `clinic-domain` + the scheduling/`@every` items above to render meaningfully. | Clinic | FE | ★★★ | L |

**Observations (low priority — folded, not filed as rows):**
- **Clinic = PHI → it's the demand driver for the deferred Vault / crypto-shred plane.** Patient records (DOB, SSN, diagnoses, medical history) are exactly the sensitive-aspect + right-to-be-forgotten case the *Vault + crypto-shredding* item (Deferred → Privacy/Vault) was specified for. Not refiling the enabler — flagging that the clinic vertical is its forcing function (per agentic-ops-design §5: demand pulls deferred platform work into existence). Whoever schedules the Vault work should treat clinic patient-record deletion as the validating flow.
- **No vertical (LoftSpace *or* clinic) is installed by `make up-full`** — already noted for LoftSpace below; clinic isn't even packaged yet (the `clinic-domain` row above is the prerequisite). A per-vertical opt-in install (`make up-clinic`) would unblock this PO loop ever exercising clinic *live* rather than statically.
- **Operator can't see *open* gaps per anchor.** `lattice weaver list` (and Loupe by extension) shows the target's *declared* gap set, not which gaps are actually open right now — after signing, it still listed all four. "What is this application blocked on?" is unanswerable from the operator surface. Largely subsumed by the planned *Loupe system-map / agent-activity console* (★★★, Refinements & ops) — flagging the per-anchor live-gap-state requirement for whoever builds it.
- **`make up-full` does not install the LoftSpace vertical** (only rbac / identity / objects-base). ✅ **Resolved** (32bb340): `make install-loftspace` installs orchestration-base → service-domain → lease-signing onto a running `up-full`, in dependency order (verified live). `up-full` stays core-only by design; the vertical is opt-in.

### PO notes (dated — drives rotation)

- **2026-06-24 — LoftSpace (first PO run).** Brought up `make up-full` (clean), but it omits the vertical
  → manually installed orchestration-base + service-domain + lease-signing on top. Drove the real
  lease-application flow end-to-end via the `lattice` CLI: created an applicant identity →
  `CreateLeaseApplication` → watched convergence auto-dispatch via Weaver + Loom. Confirmed the
  background-check + payment externalTasks ran through the bridge (fake adapters) and wrote `completed`
  outcomes (with `validUntil` freshness), and two human userTasks opened (`RecordIdentityPII`,
  `SignLease`). Drove `SignLease` to close `missing_signature`. Findings above are all **verified against
  the live stack**, not static analysis. Stack torn down (`make down`). **Next run rotates to Clinic**
  (forcing-function vertical — currently exists only in design docs, so expect a static
  capability/product-gap pass: what a non-leasing domain needs that the platform/packages don't yet
  provide).
- **2026-06-24 — Clinic (first PO run, static).** As predicted, **no clinic package exists** (`ls
  packages/` → identity/lease/location/objects/orchestration/rbac/service; none clinic), so this was a
  static capability/product-gap pass — did **not** bring up a stack (`make up-full` carries no clinic
  vertical to drive). Grounded every finding in code/contracts, not speculation: confirmed (a) `@every`
  recurring scheduling has **no consumer** — substrate enables it (`AllowMsgSchedules`, primordial.go:198)
  but bridge + Weaver temporal lane are all `@at` one-shot (§10.4); (b) temporal availability / double-book
  rejection is **explicitly deferred** by Capability-KV §06 (L354–359) to a "Phase 2 mechanism"; (c) PHI =
  the demand driver for the deferred Vault/crypto-shred plane. Filed 4 clinic rows (domain pkg, `@every`
  forcing function, scheduling-conflict, FE) + 2 observations. The clinic is doing its job as the
  flywheel-validation vertical (agentic-ops-design §5): it pulls `@every` / Vault / temporal-availability
  out of "deferred" and into "demanded." **Next run rotates back to LoftSpace** (now exercisable live —
  re-drive the lease flow and check whether the Steward has burned down any of the 2026-06-24 LoftSpace
  rows, e.g. the decline/manual-review outcome or task-content items).

---

## Deferred backlog (Phase 3+)

### Security & trust boundary
| Item | What it is | Imp | Size |
|---|---|---|---|
| Read-path authorization (D1) | Reads from lens targets (Postgres / KV) bypass the write-path Capability boundary. Rubric in `lattice-architecture.md` D1: Postgres RLS + a Capability-Read Lens; Gateway sets `lattice.actor_id`. Subsumes `cap.svc` serviceAccess read-auth. | ★★★ | L |
| Gateway | Edge trust boundary: JWT auth, `Lattice-Actor` stamping, read-path enforcement. Gates external actors + the real Edge node. | ★★★ | L |
| NATS account-level write restriction | Close the fabricated-KV-write surface at the substrate level (today defended only by overwrite-by-reprojection). | ★★ | M |

### Privacy / Vault
| Item | What it is | Imp | Size |
|---|---|---|---|
| Vault + crypto-shredding | Per-identity keys for sensitive aspects (SSN / DOB); right-to-be-forgotten = destroy the key; transient-session-key decryption for the Edge node; + the privacy failure tier (`KeyShredded` listener). | ★★★ | L |

### External-I/O maturity (bridge follow-ons)
| Item | What it is | Imp | Size |
|---|---|---|---|
| Real adapters + async result-return | Replace the `Fake*` adapters with real vendors and design the **asynchronous** result path. Today the bridge's `Adapter.Execute` is synchronous and must return a final `Result`; real checks / payments submit → pending ref → webhook/poll callback hours–days later. Needs (a) an inbound-result mechanism (webhook receiver, or poll via the `core-schedules` temporal lane), (b) an `Execute` contract that expresses "submitted, resolve later" (the bridge claim vertex stays open until the inbound result drives the replyOp), (c) a re-tuned wedged-claim horizon for legitimately-pending async claims. | ★★ | M–L |
| Structured adapter result | The bridge posts `{externalRef, result}` and the replyOp hard-codes `status="completed"` — there is no `failed` producer. Thread a structured pass/fail/detail status onto the reply; lens `missing_*` predicates key off the real status. | ★★ | S–M |
| Adapter read-seam / richer params | Adapters can only use what the target-lens row projects; a vendor needing extra subject fields (SSN / DOB) has no fetch path. Decide: richer projection columns vs. an adapter read seam. | ★★ | S–M |

### Scale-out
| Item | What it is | Imp | Size |
|---|---|---|---|
| Multi-cell / sharding | Graph scales by **cells** (root + subgraph co-located for atomic writes); global adjacency index + bridge links for cross-cell traversal; live migration as a dual-write shadow; multi-cell routing in Processor + Refractor. Keys already embed no cell identity (validated Phase 1). | ★ now / ★★★ at scale | XL |
| HA NATS clustering | Single-server today; clustering + multi-instance engine fan-out (several components note single-instance as a Phase-3 concern). | ★ now / ★★ prod | M–L |

### Edge & personal lenses *(the path Loupe grows into)*
| Item | What it is | Imp | Size |
|---|---|---|---|
| Personal / Secure Lens | Refractor projects a per-identity **security-filtered** subgraph stream; the "Interest Set" watchlist; RLS-style link filtering. Gates the real Edge node; intersects read-path auth. | ★★ | L |
| NATS-subject publish-events adapter | A Refractor target adapter that publishes projection deltas to `lattice.sync.user.<id>` subjects — required for Personal Lens fan-out (only NATS-KV + Postgres adapters ship today). | ★★ | S–M |
| Edge Lattice (full) | The sovereign per-user node: local VAL (SQLite / IndexedDB), local Starlark, offline-first, reconcile-by-revision, transient-key decryption of vaulted aspects. Loupe is its trusted-tool precursor. | ★★ | XL |

### AI-native
| Item | What it is | Imp | Size |
|---|---|---|---|
| AI-authored capabilities | A Lattice-aware agent proposes DDL / Starlark / lenses / workflows through human review + deterministic validation + rollback-friendly contracts. Marquee AI vision. | ★★–★★★ | L |
| L3 evaluator | Weaver's AI-assisted reasoning tier for ambiguous / novel convergence gaps (L1 / L2 ship today). | ★★ | M–L |
| Starlark guards (Loom) | The reserved `{reads, starlark}` guard escape hatch needs a verified-pure sandbox (the declarative grammar covers current flows). | ★ | M |

### Read-model / projection maturity
| Item | What it is | Imp | Size |
|---|---|---|---|
| Historical state query (FR51) | Operators query operational state across a configurable time range. | ★★ | M |
| Elasticsearch target adapter | A third lens target adapter (only NATS-KV + Postgres ship; no consumer yet). | ★ | M |
| Negative / filter-retraction projection | True "emit-only-when-violating" (Weaver targets currently project one row per candidate with a `violating` flag, avoiding retraction work). | ★ | M |
| Link-tombstone re-projection · cross-instance latency rollup | Two projection edge-cases / observability gaps (current approaches work). | ★ | S each |

### Refinements & ops
| Item | What it is | Imp | Size |
|---|---|---|---|
| Loom / Weaver control-API surfacing (beyond Loupe's needs) | Operator pause/resume + a durable `loom.*` read model beyond what the Loupe blocker covers. | ★ | M |
| Package version upgrade (F-004) | In-place re-install over an existing version + DDL migration semantics (install / uninstall exist; upgrade does not). | ★ | M |
| FR28 — role-queue + fallback | Assign tasks to a role queue with fallback (the demo uses direct identity assignment). | ★ | M |
| op-vertex pruner + `@every` schedules | GC of op-tracker vertices (#47 / #49) + recurring schedules (Phase 2 ships one-shot `@at` only). | ★ | M |
| Loupe live "system map" landing page *(Loupe)* | Landing view renders the running component + data-flow topology (the `architecture-overview` shape, deployed subset) as a **live** diagram — per-component/lens Health indicators, edge/link status, drill-in to vertices and control planes. Self-truthing: generated from Health KV + Core KV, not a static image. Base layer for the planned agent-activity operator console (`implementation-artifacts/agentic-ops-design.md`). **✅ Shipped** — data layer (`GET /api/systemmap` / `computeSystemMap`) + the FE landing view (5-tier `getBoundingClientRect` SVG topology, status→token node language, drill-ins, auto-refresh, all states; in-browser-verified healthy + degraded). The agent-activity console attaches at the reserved `#sysmap-console` slot (`implementation-artifacts/loupe-system-map-ux.md` §6). | ★★★ | M |
| Loupe agent-activity console *(Loupe)* | The ops layer atop the live system map: the Steward's queue + work in flight, the **L3 contract-review queue** (Andrew's touchpoint — structured what / why / affected-consumers, not raw uncommitted diffs), per-agent Health, and board state. The agents emit Health KV like components, so Loupe watching the platform watches the agents (dogfoods the dependency-watch). Operator surface for `implementation-artifacts/agentic-ops-design.md`. | ★★★ | M |
| Conventions-linter — edit-time hook *(agentic-ops)* | ✅ Done. The 24 pre-existing `// Story N …` history-comments are swept and `STRICT=1 go run ./scripts/lint-conventions.go` is wired as a CI gate (`.github/workflows/ci.yml`); `go run ./scripts/lint-conventions.go --hook` now reads a `PostToolUse` stdin payload, scans the one edited `.go` file, and feeds advisory findings back via `hookSpecificOutput.additionalContext` (never blocks). Registration is a per-machine `.claude/settings.json` matcher (gitignored) — snippet in the script's doc comment. | ★ | XS |
| Version-control the agentic-ops role-skills *(agentic-ops)* | ✅ Done — canonical defs live in tracked **`agents/`** (`lamplighter`, `steward` + README); **`make install-skills`** copies them into the gitignored `.claude/skills/`. Edit in `agents/`, re-install. bmad tooling skills stay local. Owner skills land here as they're authored. | ★★ | S |

### Parking lot — very low priority (far, far back)

Real but low-value; the Steward should **not** spend design or build effort here unless Andrew explicitly
greenlights one (Steward triage 2026-06-24 — these were the "ride-along" cleanups that turned out not to be
clean small wins).

| Item | Why it's parked | Imp | Size |
|---|---|---|---|
| multi-aspect atomic OCC for `UpdateMetaVertex` | `meta_ddl.go` applies `expectedRevision` to the first changed aspect by design (each aspect has an independent NATS revision sequence); true atomic multi-key OCC needs a substrate per-key-revision primitive — M+, contract-adjacent — for marginal value. | ★ | M+ |
| freshnessExpiry marker tombstone-on-convergence | Per `packages/orchestration-base/mark_expired.go`, a converged marker is read by nothing and harmless; tombstoning buys cleanup not correctness + adds a convergence-edge write — near-zero value, Contract #10 §10.4-adjacent. | ★ | S |
| production freshness-window tuning | A staleness-tolerance vs. timer-churn value judgment — Andrew's call if/when it matters; not worth proactive effort. | ★ | XS |

---

## Done / moot — *not backlog*

- **Per-lens delete mode (Story 1.5.12)** — built; `deleteMode` (default hard) is in use across the
  task / ephemeral lenses.
- **§10.8 nudge-`operation` CAR** — moot: 13.5 retired the nudge GapAction; external remediation is now
  `triggerLoom` of an `externalTask` via the bridge.
- **Capability-Lens god-cypher → contract-contribution** — resolved (Epic 12).

---

*Consolidates and supersedes: `lattice-architecture.md` "Open Items (Phase 3+)" (OI-1 async adapters /
OI-2 large files carried here), `epics/index.md` "Phase 2+ Deferred Architectural Capabilities", and the
per-component "Deferred (Phase 3+)" sections.*
