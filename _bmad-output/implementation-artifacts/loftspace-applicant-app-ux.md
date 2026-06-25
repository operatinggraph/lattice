# LoftSpace Applicant App — UX Design (build-ready)

**Role:** UX Designer (Sally) spec, adjudicated + ratified by Winston.
**Status:** ✅ **Winston-ratified — build-ready.** No frozen-contract change, no architectural
fork. The FE Engineer builds against this; Winston admits at L2 after in-browser verification.
**Vertical:** LoftSpace · **Owner:** Sally (UX) → FE Engineer (build) · **Imp ★★★ · Size L (multi-fire).**

Supersedes nothing; this is the first design for the greenfield applicant FE the backlog flags as the
**top experience priority** (`backlog.md` → *Now — the experience layer*, *Vertical demand backlog* row
"LoftSpace applicant app — scoped FE"). Grounded in the real ops/lenses, not speculation.

---

## 1. The problem (verified live by the PO)

The LoftSpace lease vertical is **headless**. Every step — apply, run checks, complete PII, sign — had
to be driven by `lattice op submit` as the system actor. An applicant has no way in. The platform now has
everything a real applicant journey needs *underneath*:

- `CreateLeaseApplication` anchors an application to a real `vtx.unit` with optional lease terms
  (`packages/lease-signing/scripts.go`).
- `loftspace-domain` gives every unit a `.listing` (rent / bedrooms / availability / status) and
  `.address` aspect (`packages/loftspace-domain/ddls.go`).
- The `leaseApplicationComplete` convergence lens projects the live application state — which gaps are
  open (`missing_onboarding / bgcheck / payment / signature`), what unit is being leased
  (`unitKey / unitAddress / unitRent`), and what is in flight (`packages/lease-signing/lenses.go`).
- `GET /api/tasks` already resolves the human-readable `RecordIdentityPII` / `SignLease` userTasks.
- `POST /api/objects` (AttachObject) is a generic content-addressed upload path.

What's missing is the **front door**: a surface that lets a person browse a listing, apply, watch their
application progress, complete their tasks, and upload documents — without a CLI.

---

## 2. Trust & identity model (the load-bearing decision)

**Decision (Winston):** the applicant app v1 is a **trusted single-identity tool**, exactly like Loupe —
it submits ops as the bootstrap **admin actor** and binds `127.0.0.1`. There is **no** authN/authZ,
Gateway, read-path auth, or Personal Lens (all Phase-3+, `backlog.md` → *Security & trust boundary*).

The app is nonetheless **applicant-centric in the view**: the user selects "who they are" (an existing
`vtx.identity.<id>`, or creates one) and the whole UI scopes its reads and writes to that applicant:

- **Writes** carry the chosen identity in the op payload (`CreateLeaseApplication.applicant`,
  `RecordIdentityPII` on that identity, `SignLease` on that applicant's application).
- **Reads** filter the lens rows / tasks by that applicant (`row.applicant` / task `assignee`).

This mirrors the headless reality (the PO drove the flow *as* the system actor on behalf of an applicant)
and is the honest v1: the per-user sovereign node + per-identity auth is the Edge-Lattice evolution this
prototype grows into, not v1 scope. **This is the same framing Loupe ships under** — do not reinvent it.

> **Why not per-user auth now?** That is a standing architectural deferral (Gateway / D1 read-path auth /
> Personal Lens). Building it here would be an Andrew-gated fork. The trusted-tool model is the ratified
> path; the applicant app is a second tool on it.

---

## 3. Host & stack

- **New `cmd/loftspace-app`** Go binary (a *separate vertical app*, distinct from Loupe the operator
  tool), serving an embedded `web/` of **vanilla HTML / CSS / JS** — the Loupe stack, no framework, no
  build step. Binds `127.0.0.1:7788` by default (`LOFTSPACE_APP_ADDR`), so it can run alongside Loupe
  (`:7777`). `make run-loftspace-app` + a `make up-full` add-on.
- The Go backend reuses the substrate plumbing Loupe already proves: `substrate.Conn`, `output.SubmitOp`,
  `bootstrap` bucket constants, the admin-actor load, the `requireConn` / `writeJSON` / `reqContext`
  helpers. **Lift the shared helpers** — do not fork them blindly; a small internal package
  (`internal/webkit` or similar) is acceptable if duplication gets ugly, but copying the four helpers is
  fine for v1 (the FE Engineer decides; flag if it grows).
- Reuse the object-upload handler shape verbatim (`cmd/loupe/objects.go`) — it is generic over
  `targetKey` + `linkName`.

---

## 4. Information architecture — four surfaces (single page, tabbed/routed)

A single-page app with a left identity switcher and four views. Order = the applicant's journey.

### 4.1 Browse & Apply (intake)

**Goal:** "What can I lease, and apply to one." Replaces the headless `CreateLeaseApplication`.

- **Listings grid** — cards of available units: address (`.address.line1, city, region`), rent
  (`.listing.rentAmount rentCurrency`), bedrooms / bathrooms / sqft, `availableFrom`, `leaseTermMonths`.
  Only `status == "available"` listings show by default (a filter toggle reveals `pending` / `leased`).
- **Apply** — a card's *Apply* button opens an intake form. The form is **DDL-self-describing**: it
  fetches `CreateLeaseApplication`'s `inputSchema` / `fieldDescription` / `examples` from the op catalog
  (`GET /api/ops`, the same source Loupe renders op-submit forms from — select the
  `CreateLeaseApplication` entry) and pre-fills `applicant` (the selected
  identity) + `unit` (the card's unit key). The applicant fills the optional terms (`moveInDate`,
  `leaseTermMonths`, `requestedRent`). Submit → `POST .../op CreateLeaseApplication` → on success, route
  to *My Applications* with the new `vtx.leaseapp.<id>` highlighted.
- **Empty / error states** — no available listings ("No units are listed for lease right now");
  NATS-down → the standard 502 banner.

**New backend endpoint (gap):** `GET /api/listings?status=available` — lists `vtx.unit.<id>` that carry a
`.listing` aspect, joining `.listing` + `.address`, filtered by status. Today nothing exposes listings;
this assembles them by listing `core-kv` keys, selecting `vtx.unit.*.listing`, and reading the sibling
`.address`. Mirror `computeTasks` / `computeSystemMap` (key-scan + per-key read + assemble). Unit-tested
headlessly.

### 4.2 My Applications (status tracker)

**Goal:** "Where is my application — submitted → checks → sign → decision."

- **One card per application** for the selected applicant, sourced from the `leaseApplicationComplete`
  lens rows in the `weaver-targets` bucket, filtered to `row.applicant == <selected identity>`.
- **A progress stepper** rendered from the gap booleans, in journey order:
  1. **Onboarding (PII)** — `missing_onboarding` → *To do* (links to the `RecordIdentityPII` task) / *Done*.
  2. **Background check** — `missing_bgcheck`; show *In progress* when `inflight_bgcheck`, *Complete* when
     the gap is closed, *Action needed* when neither (escalation / max-retries).
  3. **Payment** — `missing_payment` (same in-flight / complete language).
  4. **Sign lease** — `missing_signature` → *Sign now* (links to the `SignLease` task) / *Signed*.
- **What am I leasing** — `unitKey` / `unitAddress` / `unitRent` rendered as the card header so the
  application is anchored to a real place.
- **Decision banner** — when no gaps remain (`violating == false`): "Application complete." (The terminal
  **declined / manual-review** state is a *separate backlog row* — `backlog.md` "Decline / manual-review
  application outcome"; the stepper should leave a labelled slot for it but not block on it.)

**New backend endpoint (gap):** `GET /api/applications?applicant=<vtx.identity.<id>>` — reads the
`weaver-targets` bucket, selects keys `leaseApplicationComplete.*`, returns the projected rows filtered by
applicant. Mirror Loupe's bucket-list pattern but against `weaver-targets`, not `core-kv`. Unit-tested.

### 4.3 Tasks (inbox)

**Goal:** complete `RecordIdentityPII` + `SignLease` without the CLI.

- Reuse `GET /api/tasks`, **filtered to `assignee == selected applicant`** and `status == open` by
  default. Each task renders its op `name` / `description` (already resolved from the `forOperation` meta)
  and a *Complete* button that opens that op's DDL-self-describing form (pre-filling the known fields —
  e.g. `SignLease.leaseAppKey`, `RecordIdentityPII`'s identity), submits, and refreshes the inbox + the
  application card.
- **PII note:** `RecordIdentityPII` writes sensitive aspects (`.ssn` / `.dob`). The form labels them as
  sensitive; v1 still submits as the admin actor (the trust model in §2). No new masking primitive — the
  Vault/crypto-shred plane is a standing deferral; do **not** build it here.

*(No new backend endpoint — `GET /api/tasks` already exists; add an `assignee=` query filter or filter
client-side. Prefer a server-side `assignee=` filter for symmetry with `status=`.)*

### 4.4 Documents (upload)

**Goal:** upload an ID / proof-of-income / signed-lease PDF.

- Reuse `POST /api/objects` (multipart `file` + `targetKey` + `linkName`) and `GET /api/objects/<oid>`.
  `targetKey` = the applicant's `vtx.leaseapp.<id>` (or their identity for ID docs); `linkName` is a
  human-chosen slot (`idDocument`, `proofOfIncome`, `signedLeasePdf`).
- A simple list of attached documents (filename / size / type) with view + detach, exactly as Loupe's
  Files tab. PDFs/SVGs are served as neutral attachments (the existing CSP/`octet-stream` guard in
  `handleObjectGet` — keep it).

---

## 5. Visual & interaction language

Match Loupe's vanilla design tokens (`cmd/loupe/web/style.css`) so the two tools feel like one platform:
the same type scale, status-token color language (green = done / healthy, amber = in-flight / pending, red
= action-needed / violating, grey = not-started), card shells, and the 502/empty/loading states. **No new
framework, no CSS reset library.** Accessibility: semantic landmarks, labelled form controls, keyboard-
reachable tabs, visible focus — the FE Engineer's standing checklist.

The identity switcher is a top-bar control: a select of existing `vtx.identity.<id>` (label =
`.profile`/`canonicalName` if present, else the id) + a "New applicant" action that submits the
identity-domain create op. Persist the selection in `localStorage` so a refresh keeps context.

---

## 6. New backend surface (the only platform gaps) — summary for the FE Engineer

| Endpoint | Why | Pattern to copy |
|---|---|---|
| `GET /api/listings?status=` | Browse leasable units (§4.1) — nothing exposes listings today | `computeSystemMap` (key-scan + per-key read + join `.listing`+`.address`) |
| `GET /api/applications?applicant=` | Per-applicant status (§4.2) — read the `weaver-targets` lens rows | Loupe bucket-list, against `weaver-targets` not `core-kv` |
| `GET /api/ops` (catalog) + `POST /api/op` | Intake + task forms (§4.1/4.3) — op `inputSchema`/`fieldDescription`/`examples` + submit | lift verbatim from `cmd/loupe/ops.go` / `op.go` |
| `GET /api/tasks?assignee=&status=` | Applicant inbox (§4.3) — add an `assignee=` filter | extend `computeTasks` with an assignee filter |
| `POST/GET/DELETE /api/objects…` | Documents (§4.4) | lift verbatim from `cmd/loupe/objects.go` |

Everything except the two new read assemblers (`/api/listings`, `/api/applications`) is a **lift** of an
already-shipped, tested Loupe handler. The two new assemblers are **headlessly unit-testable** (feed a key
list + a fake getter, assert the rows) — no browser needed for their gate.

---

## 7. Build increments (the multi-fire FE plan)

Each increment is its own green commit; in-browser verification per the unattended-verify note
(`claude-in-chrome` against the already-running app, **not** `preview_start`).

1. **Increment A — scaffold + intake.** `cmd/loftspace-app` server (lifted helpers) + `GET /api/listings`
   + `GET/POST /api/op` + the *Browse & Apply* view + the identity switcher. Verify: browse a seeded
   listing, apply, see the new application. *(Ships the front door.)*
2. **Increment B — status tracker.** `GET /api/applications` + the *My Applications* stepper. Verify the
   gaps render and update as the flow converges.
3. **Increment C — tasks.** `GET /api/tasks?assignee=` + the *Tasks* inbox completing
   `RecordIdentityPII` / `SignLease`. Verify a task closes its gap.
4. **Increment D — documents.** The objects handlers + the *Documents* view. Verify upload + view + detach.
5. **Increment E — `make` wiring.** `make run-loftspace-app` + an `up-full` add-on so Andrew sees it live.

A is the highest-value single fire (the front door); B–D layer on. Scale review to risk: lead review for
each green increment; full 3-layer if an increment touches the capability/op-submit plane in a novel way
(it shouldn't — it reuses the trusted-actor submit path).

---

## 8. Explicit non-goals (stay Phase-3+ / other rows)

- Per-user authN/authZ, Gateway, read-path auth, Personal Lens (§2 — standing deferrals).
- Terminal **declined / manual-review** disposition (separate backlog row — leave a labelled slot).
- PII masking / Vault / crypto-shred (standing deferral; the clinic vertical is its forcing function).
- A new vertex type or contract change — the app is **pure read-assembly + existing-op submission**.
