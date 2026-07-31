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
| **A wellness studio can't take money** | `CreateBooking` classifies `rate: standard\|resident` and writes the `residentRate` link its own DDL calls the audit link "a future billing composition lens can walk" — but there is no wellness ledger (café/clinic/loftspace each have one), so the rate is a badge and nothing more: no class price, no pass or membership, no no-show charge (clinic already posts one). | Wellness | pkg | ★★ | L | 📋 ready |
| **A provider can't act in her own clinic** | `clinic-domain` grants a bound provider SetProviderHours/TimeOff + Reschedule/SetAppointmentStatus (live: SetProviderTimeOff commits as Dr. Osei), but `PROVIDER_VIEWS` is `["myschedule"]` rendered `cancelable:false` and availability sits behind the staff `worksAt` gate, so she can act on nothing and renders as a NanoID. `RecordEncounter` grants `operator` alone (live: AuthDenied, `rolesCarryingPermission:["operator"]`) — the one clinical verb a clinician owns. | Clinic | pkg + FE | ★★★ | M | 📋 ready |
| **Every `seed-classic-demo` run mints a duplicate patient + provider** | `CreatePatient`/`CreateProvider` are called with no caller-supplied id, so the seed is not idempotent — live, the booking picker offers four indistinguishable "Dr. Classic Demo · Family Medicine" entries and 9 patient roots back a 3-row roster. Every other seeded entity pins a NanoID as its idempotency seam (`seed-showcase.go`). | Clinic | pkg | ★★ | XS | 📋 ready |
| **clinic-domain's README documents a retired booking design** | 8 references to `.bookingGuard` OCC epochs + `hasBooking` link enumeration across the inventory, key shapes, op contracts and the whole conflict-detection table; the package ships 15-minute `providerSlotClaim`/`patientSlotClaim` CreateOnly aspects instead and neither documented shape exists live. §Operations tells a caller to declare `<provider>.bookingGuard` in `contextHint.reads` — a key that never exists. | Clinic | pkg | ★★ | S | 📋 ready |
| **A tab shows a total and nothing else** | Self-order shipped, so the app is now the ordering surface — but a resident's only feedback is the running `totalCents`: no line items on the tab, no memo on the posted ledger debit (live — two identical $2.75 self-orders render "$5.50", the ledger reads "+$5.50" unlabelled), so nobody can check what was ordered or whether a second tap registered. `cafe-domain`'s README parks itemization as YAGNI "no demand row asks for it yet"; this is that row. | Café | pkg + FE | ★★ | M | 📋 ready |

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

- **Rotation to date:** LoftSpace ×18, Clinic ×17, Café ×7, Wellness ×4.
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799/:7801/:7802), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. All four apps exist + are exercisable live (`:7788` / `:7799` / `:7801` / `:7802`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-07-28:** Café — drove self-order + staff POS live; the picker offers items `Charge` refuses, the POS has no catalog, tabs aren't itemized; filed 4.
- **2026-07-28:** Wellness — drove member/staff/instructor hats live; the schedule's Book is mostly dead, a called-off class tells nobody, attendance is invisible + member-erasable; filed 3.
- **2026-07-28:** LoftSpace — drove applicant, landlord + back-of-house hats live; no seeded world can approve a lease, and maintenance work never reaches the app; filed 2.
- **2026-07-29:** Clinic — drove patient, provider, front-desk + root hats live; the front desk renders a staff console it can't read a roster into; filed 3.
- **2026-07-29:** Café — drove self-order + front-desk hats live; the demo world seeds no menu, the composition layer is installed by nothing, front of house can't name anyone; filed 3.
- **2026-07-30:** Wellness — drove member, instructor + front-desk hats live; the staff console names nobody, a class's teacher and time are frozen, nothing charges; filed 4.
- **2026-07-30:** LoftSpace — drove landlord, staff + 2 applicant hats live; an approved lease never leases the unit and the roster names nobody; filed 2.
- **2026-07-31:** Clinic — drove patient, provider + front-desk hats live; no seeded visit ever completes and the provider hat can act on nothing; filed 4.
- **Next:** Café.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

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
- 2026-07-28 · `e21e374c` · Schedule's Book button now mirrors `renderBookMember`'s started/full gate + adds an own-bookings DoubleBooked check — no longer offers a class `CreateBooking` will refuse
- 2026-07-28 · `a0414185` · clinic-domain/clinic-app self-anchor doc comments corrected + `staff_patients_rls_test.go` now seeds the real per-patient `authz_anchors` shape (confirmed to fail pre-fix)
- 2026-07-28 · `c98871b0` · Patient self-booking re-verified live (write path already worked; "still 403s" was stale) — Book Appointment now locks "Select patient" to the signed-in patient's own record
- 2026-07-28 · `e1900307` · Book Appointment no longer leaks a prior identity's raw patient NanoID — a persisted `clinic.patient` key not in the current identity's roster is evicted, not `shortKey()`-rendered; cleared on sign-out too
- 2026-07-28 · `67f87379` · The 10 prose-format packages' `permissions.go` gets identity-domain's tabular `Grant matrix:` doc block (S2); each package version-bumped alongside. standard §14
- 2026-07-27 · `c6294007` · The 24×-duplicated lens_cypher_test.go harness (embedded-NATS KVs + NanoID) collapses onto `internal/lenstest`; per-package vtx/aspect/edge fixtures untouched. standard §9
- 2026-07-27 · `5d26f215` · `rbac-domain`/`privacy-base`/`augur`'s hand-rolled vertex-key parsers converge onto the S10-pinned `parts_of` body.
- 2026-07-27 · `90a51b17` · `ClaimIdentity`'s `claimKey` gets a per-field `x-sensitive` mask; `targetIdentityKey` stays plain/prefillable — `RecordServiceOutcome`'s stale demand row (already closed by `7911ccf6`) also cleared.
- 2026-07-27 · `a8054fe8` · `cafe-domain` README inventory refreshed past Increment 2 — self-order catalog, workplace confinement, shipped FE + Inc 3 one-bill now documented.
- 2026-07-27 · `52e24218` · `actor_holds_operator`'s role-page cursor gets a 51-role fixture (operator on page 2) — retires the untested-multi-page-branch risk.
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
