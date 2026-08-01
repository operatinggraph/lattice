# Backlog — App Verticals (Stream 1)

Stream 1 = app-vertical packages + FEs (LoftSpace, Clinic). Advanced by the **Vertical Steward**; demand
filed by the **Vertical PO** (file-only). Index + cross-lane rules: [../backlog.md](../backlog.md).
**Row discipline** (one item = one row; State = token + ref + one-line next; detail lives in the design
doc + git, never narrated in the cell): see [lattice.md → How this board works](lattice.md).

**Scales.** Imp ★/★★/★★★ · Size XS–XL. **State.** 📋 ready · 🏗️ building · 📐 awaiting-Andrew ·
✅ ratified (designed, not built) · 🚧 blocked (`blocked-on:` / Andrew-gated).

## Vertical demand backlog (PO discovery)

Open items only — shipped demand is in the Done log. The PO files (tagged vertical + owner: FE = Sally +
FE Engineer · pkg = Package Designer · platform = component owner + Lattice lane); the Steward + FE
Engineer build. **No-paper-over:** a missing platform *primitive* routes to [lattice.md](lattice.md) and
the row is `🚧 blocked-on:` it (a missing *lens* is package work, built here).

| Item | What it is (PO view) | Vertical | Owner | Imp | Size | State |
|---|---|---|---|---|---|---|
| **Edge showcase app (Facet)** | Discovery-driven personal client on the Edge foundation: hardcodes only IdP login + connect; services, ops, forms, tasks, panes arrive as data via `edge-manifest` personal lenses + a descriptor vocabulary (#52/#54/#55). PWA-first. Covenant structurally enforced by `lint-facet-discovery` (CI). | Cross-vertical | Sally + FE Engineer + pkg | ★★★ | XL | 🚧 blocked-on: full Xcode (this host has CommandLineTools only) · [design §7.12](../../implementation-artifacts/edge-showcase-app-design.md) · every non-iOS increment shipped |
| **Clinical notes are write-only** | `RecordEncounter` PHI (`ddls.go:333-336`) captured, never projected. The cited `clinicPatientsRead` Secure-Lens precedent does NOT extend — that decrypts identity-anchored Vault ciphertext; this is raw plaintext on a non-identity vertex, and that exact shortcut was already REJECTED pre-Vault (`vault-crypto-shredding-design.md` ratification decision #2). | Clinic | pkg | ★★★ | M | 🚧 blocked-on: Vault extended to non-identity content (architectural fork, Andrew) |
| **Five identity ceremony ops stay undiscoverable** | `CreateUnclaimedIdentity`, `RotateClaimKey`, `InitiateCredentialLink`, `CompleteCredentialLink`, `UnlinkCredential` carry stated `[no-op-meta:]` exemptions ([§8](../../implementation-artifacts/vertical-package-standard.md)). Consumer is 3 hardcoded in Facet (`cmd/facet/credentials.go`) + 2 in staff web apps/the CLI, not "Facet, all five" as first filed. | Cross-vertical | pkg | ★★ | M | 🚧 blocked-on: 3 new OpMetaSpec vocabulary primitives, no precedent to mirror — [lattice.md](lattice.md) |
| **A wellness class still has no price or pass** | wellness-ledger covers no-show fees only; a class booking itself has no price and there is no pass/membership product — café/clinic/loftspace all have one, wellness doesn't yet. | Wellness | pkg | ★★ | M | 📋 ready |
| **A full class turns a member away with nothing to do** | No waitlist exists anywhere in the repo — `claim_first_free_seat` fails `SessionFull` and the FE only disables the button (`app.js:590-604`), so a seat freed by a cancellation is silently first-come again instead of going to whoever wanted it. The auto-promote transport is already in-house: the `wellnessOrphanedBookingSettlement` convergence lens + `ReleaseOrphanedBooking`. | Wellness | pkg + FE | ★★ | L | 📋 ready |
| **Every occurrence of a weekly class is hand-created** | `CreateSession` mints exactly one session — no recurrence param anywhere in `ddls.go` — so a studio's Mon/Wed/Fri 6pm class is N separate creates; live, "Evening Flow with Sam" is six unrelated session vertices. Both precedents are in-house: clinic's `visitseries` op family and the shipped `@every`/`ScheduleEvery` schedule primitive. | Wellness | pkg + FE | ★★ | M | 📋 ready |
| **A booked class never reminds anyone** | wellness-domain carries no reminder lens or notification at all, while clinic ships a whole `clinic-reminders` package (`appointmentReminders` + `followUpReminders` + `notifications.go`) — a member who books a week out hears nothing until the no-show mark lands. | Wellness | pkg | ★★ | M | 📋 ready |
| **A resident's to-do list holds a task nobody can do** | `myTasksSpec` OPTIONAL-MATCHes `forOperation`, so a task whose bound op meta is tombstoned keeps projecting with a null `operationName`; `app.js:2072`/`:2100` then titles the card with a raw key and renders no action button. Live: Riley's `vtx.task.S2YnAHmW…` → tombstoned `vtx.meta.EUayYDxp…`, open until 2026-08-17. Retraction transport is in-house (`CancelTask` + the `wellnessOrphanedBookingSettlement` convergence precedent). | Cross-vertical | pkg | ★★ | S | 📋 ready |
| **A signed lease never bills anyone** | `loftspace-ledger` 0.4.4 ships `CreateAccount`/`DebitAccount` and the FE wires them (`app.js:2537`, `:2791`), but not one `account` or `transaction` vertex exists in the graph — so Riley's Ledger card reads $0 and the one-bill statement, the mixed-use composition payoff, is 100% café (`-1709`, three café rows). `seed-showcase.go:1237` opens her *clinic* account; nothing opens her rent one. | LoftSpace | pkg | ★★ | S | 📋 ready |
| **The front desk's portfolio card mixes two portfolios** | `landlordUnitsReadSpec` anchors `[landlord]` alone while `landlordLeaseApplicationsReadSpec` anchors `[landlord] + covering buildings`, and `handlePortfolioPulse` composes one card from both — live, front-desk Dana reads "1 unit · 100% occupancy · 0 of 8 leases attached". The building fan-out already shipped for every sibling roster (`applicantRosterRead`, `cafeIdentitiesRead`, `wellnessIdentitiesRead`). | LoftSpace | pkg | ★★ | S | 📋 ready |
| **No way to demonstrate that Facet survives going offline** | Offline-first is the Edge's headline claim and nothing lets a viewer see it — the mirror only serves a disconnected world during a real NATS outage nobody can stage. Per [UX §6](../../implementation-artifacts/facet-app-ux.md) the honest offline story is a host↔NATS drop, not the browser going offline, so a truthful toggle disconnects the host engine: reads keep serving from bbolt, writes queue and drain on reconnect. | Cross-vertical | Sally + FE + platform | ★★ | M | 📋 designer · needs a fenced control surface |
| **A café credit renders as `$-21.59`** | `money()` (`app.js:179-182`) prefixes a sign-carrying amount, and the ledger pane never says whether a negative café balance is money owed or money held — live, Riley's account reads `Balance: $-21.59` after two counter payments. | Café | FE | ★ | XS | 📋 ready |
| **clinic-domain's README still under-documents 4 shipped op surfaces** | The README's Operations/Inventory never mention `BindProviderIdentity` (+ its `identityClaim`/`providerClaim` guard aspects) or `CreatePatient`'s `identityClaim`/`patientClaim` guard, and the appointment op docs omit `CreateAppointment`'s `leaseAppKey`/`site` params and `SetAppointmentStatus`'s `noShowFeeCents` + terminal-status finality rule (all confirmed live in `ddls.go`). | Clinic | pkg | ★ | S | 📋 ready |

**Explicitly descoped (ambitious-PO pass, 2026-07-09):** structured diagnosis/procedure coding (ICD/CPT),
vitals, and e-prescribing were considered and deliberately NOT filed — a certified EHR is out of scope for a
reference vertical whose job is demonstrating platform mechanics, not clinical-coding/DEA compliance. Flagging
the boundary so it reads as a decision, not an oversight.

**Spec** = the go-live composition demo (public-presence site, `localhost:7900/#demo`) — four lenses × package
toggles. PO ruling: all composition is **package-level, no Lattice block** (ledger `heldFor` anchor · generic
`claim_cell` · `contextHint.reads` — precedent: `DebitAccount`→clause; file:line grounding in the commit).
Build against the real key shapes, not the demo's: keys are **NanoIDs** (Contract #1) and the account→lease
relation is `heldFor` (the demo's `ACC88`/`BK7`/`L204` + `billedWith` are cosmetic).

## PO notes (dated — drives rotation)

Compact rotation memory only — PO *findings* are filed as demand rows above + the Done log; the verbose
dated run-logs live in git history. Rotate LoftSpace ↔ Clinic ↔ Café ↔ Wellness, staggered from the Steward.
**Wellness joined** 2026-07-09 (`cmd/wellness-app` shipped, live on :7802) — fold it into rotation; see
[agents/vertical-po/SKILL.md](../../../agents/vertical-po/SKILL.md) §1.

- **Rotation to date:** LoftSpace ×19, Clinic ×17, Café ×8, Wellness ×5.
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799/:7801/:7802), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. All four apps exist + are exercisable live (`:7788` / `:7799` / `:7801` / `:7802`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-07-29:** Clinic — drove patient, provider, front-desk + root hats live; the front desk renders a staff console it can't read a roster into; filed 3.
- **2026-07-29:** Café — drove self-order + front-desk hats live; the demo world seeds no menu, the composition layer is installed by nothing, front of house can't name anyone; filed 3.
- **2026-07-30:** Wellness — drove member, instructor + front-desk hats live; the staff console names nobody, a class's teacher and time are frozen, nothing charges; filed 4.
- **2026-07-30:** LoftSpace — drove landlord, staff + 2 applicant hats live; an approved lease never leases the unit and the roster names nobody; filed 2.
- **2026-07-31:** Clinic — drove patient, provider + front-desk hats live; no seeded visit ever completes and the provider hat can act on nothing; filed 4.
- **2026-07-31:** Café — drove resident self-order/settle + front-desk hats live; the front desk composes nothing and one-bill has no reader; filed 4 (1 → lattice).
- **2026-08-01:** Wellness — drove member, instructor + front-desk hats live; My Classes hides the studio, and there is no waitlist, recurrence or reminder; filed 5.
- **2026-08-01:** LoftSpace — drove tenant, landlord + front-desk hats live; browsable inventory is seven copies of one flat, rent never bills, six duplicate staff; filed 4 + broadened 1.
- **Next:** Clinic.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

- 2026-08-01 · `b0cf3d62` · seed-classic-demo now pins the unit + studio (unitID/studioID) so reruns stop minting duplicate inventory rows
- 2026-08-01 · `f5551e87` · Staff seed guard no longer self-defeats after sign-in — ensureStaff/ensureMaintenanceTech exclude by residesIn, not consumer; reruns now reuse the same Dana/Theo
- 2026-08-01 · `f9a6a55e` · My Classes now names the studio to show up at — wellnessBookings walks se→atStudio→studio; wellness-domain 0.19.2
- 2026-08-01 · `16becb24` · A wellness no-show now actually costs the member money — new wellness-ledger package + noShowFeeCents auto-charge; wellness-domain 0.19.1
- 2026-07-31 · `3e081e95` · A café tab now shows what was rung up, not just a total — Charge/VoidCharge append a running itemsMemo line the tab card AND the settled ledger entry's memo both render; cafe-domain 0.11.9
- 2026-07-31 · `00cfd10d` · loftspace-app's tenant view now reads the one-bill statement — combined rent+café charges, self-anchored via the protected applications RLS rows; Inc 3's payoff finally reaches a user
- 2026-07-31 · `30f0b467` · Front desk can now compose all three badges for both showcase residents — each tenant gets a backfilled approved lease, a residentVisit appointment, and a resident-rate booking
- 2026-07-31 · `df76a401` · clinic-domain README now documents the live write-path slot-claim booking mechanism instead of the retired `.bookingGuard`/`hasBooking` design
- 2026-07-31 · `adbf2571` · `seed-classic-demo` patient + provider now pin fixed handles (alive()-guarded); appointment id derived per-day so reruns converge instead of duplicating the shared booking picker/roster
- 2026-07-31 · `7630e28c` · A provider can now act in her own clinic — RecordEncounter grants provider (guarded); My Schedule + Availability unlock self-scoped; her name resolves. clinic-domain 0.28.15
- 2026-07-31 · `12b155cd` · Showcase seed now completes a visit + names its site — fills Follow-ups, noShowSettlement, clinicSites on a fresh world
- 2026-07-31 · `f7d744d8` · Staff console can now reassign a class's instructor and/or time — Roster panel's "Reassign" control wires the already-shipped ReassignSession op, mirroring CreateSession/TombstoneSession's staff-only affordance idiom
- 2026-07-31 · `4ede405e` · Wellness Schedule filters to upcoming-only, groups by day, renders local times — mirrors Facet's isUpcoming + clinic-app's local formatting
- 2026-07-31 · `5280967a` · Console can now name landlords/staffers/applicants — `applicantRosterRead` authz_anchors gets self+landlord+building fan-out, mirroring café/clinic. loftspace-domain 0.10.5
- 2026-07-30 · `d51a22aa` · An approved lease now actually leases the unit — `seed-showcase.go` heals a stalled application by driving `RecordIdentityPII`, unblocking bgcheck dispatch; verified live, all 3 units flipped to leased
- 2026-07-30 · `5ddd6a73` · Staff console can now name its members — `wellnessIdentitiesRead`'s authz_anchors fan out over the staffer's own workplace, mirroring café's front desk. wellness-domain 0.18.2
- 2026-07-30 · `21d45f85` · My Classes shows the attended/no-show badge + disables Cancel once started/marked; CancelBooking rejects both server-side, incl. front-desk Release-seat. wellness-domain 0.18.1
- 2026-07-30 · `92439169` · Front desk can now name its customers — `cafeIdentitiesRead` fans authz_anchors out over the staffer's own workplace buildings; front-desk grid resolves it via `/api/residents`. cafe-domain 0.11.7
- 2026-07-30 · `5d2440d8` · Staff POS Charge now accepts menuItemKey too (catalog-priced + location-bound like self-order), keeping amountCents for off-menu; POS view renders the confined catalog picker. cafe-domain 0.11.6
- 2026-07-30 · `96c353a4` · Riley's showcase clinic patient now opens a ledger account + visit series; Osei's stale `.hours` window (unrelated blocker hit live) reset unconstrained every seed run
- 2026-07-30 · `29a98f1f` · `/api/residents` no longer leaks the whole lease-applicant roster to a bare patient session — `resolveSubjectHats` scopes it to the caller's own row or staff, mirroring cafe-app/wellness-app
- 2026-07-29 · `2b693874` · LoftSpace can now work its maintenance queue — Claim/Resolve wired in Tasks, staff can Report an issue
- 2026-07-29 · `b7ec7655` · Café composition layer now installs — `install-front-desk`/`install-one-bill` join `install-showcase-domains`, mirroring `install-maintenance`
- 2026-07-29 · `ccd24300` · A showcase resident can now order something — `seed-showcase.go` seeds Latte + Croissant servedAt the building, idempotent per-item, mirroring `seedCafeTemplate`. cafe self-order menu no longer empty
- 2026-07-29 · `a9c1e7c0` · Front desk can now read the patient roster — `clinicPatientsRead` anchors on the practicesAt workplace of a patient's appointment providers, not just the patient self-anchor + wildcard. clinic-domain 0.28.12
- 2026-07-29 · `b3458cb5` · All 3 seeded LoftSpace worlds can now reach a lease decision — 3 seed-script bugs fixed + verified live (`.tenancy` now stamps)
- 2026-07-28 · `a8c4eee9` · A called-off class no longer strands its bookings — `wellnessOrphanedBookingSettlement` Weaver target releases the seat + guard; My Classes shows "Class cancelled" not `? → ?`. wellness-domain 0.18.0
- 2026-07-28 · `1f1f5ca0` · cafe-app + wellness-app "Signed in as" resolves a name via self-anchored Secure Lenses; all four vertical apps now resolve one
- 2026-07-28 · `bb190dab` · clinic-app's "Signed in as" resolves a name via the existing `clinicPatientsRead` roster, mirroring loftspace-app's nameFor/renderSignedInAs
- 2026-07-28 · `20893d56` · `menuCatalog` projects each item's `servedAt`; self-order `/api/menu?leaseAppKey=` now offers only what that lease's Charge would accept, mirroring `location_covers`. cafe-domain 0.11.4
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
