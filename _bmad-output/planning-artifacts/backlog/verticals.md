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
| **A tab shows a total and nothing else** | Self-order shipped, so the app is now the ordering surface — but a resident's only feedback is the running `totalCents`: no line items on the tab, no memo on the posted ledger debit (live — two identical $2.75 self-orders render "$5.50", the ledger reads "+$5.50" unlabelled), so nobody can check what was ordered or whether a second tap registered. `cafe-domain`'s README parks itemization as YAGNI "no demand row asks for it yet"; this is that row. | Café | pkg + FE | ★★ | M | 📋 ready |
| **`cafe-domain` README says a shipped guard isn't built** | Its "Out of scope" claims one-open-tab-per-lease exclusivity "is not built" while the same README's Inventory lists the `cafeOpenTabGuard` aspect and `ddls.go:45,101-102` rejects `OpenTabAlreadyExists` — live-confirmed on a second `OpenTab`. A stale "not built" on a safety guard invites rebuilding it. | Café | pkg | ★ | XS | 📋 ready |
| **My Classes hides attendance and lets a member erase it** | `/api/bookings` serves `status` (booked/attended/noShow); `myClassCard` ([app.js:572](../../../cmd/wellness-app/web/app.js)) renders the rate badge instead and never shows it, while offering an unconditional Cancel. `CancelBooking` (ddls.go:2171) has no past-class and no attendance guard — the asymmetry to `SetBookingAttendance`'s `SessionNotStarted`. Live: cancelling a booking on a class that ended 7 days earlier was accepted, so a no-show is member-deletable. | Wellness | pkg + FE | ★★ | M | 📋 ready |
| **Front of house can't name a single customer** | `cafeIdentitiesRead` anchors each row on the identity's OWN NanoID ([lenses.go:240](../../../packages/cafe-domain/lenses.go)), so a `worksAt` staffer resolves only themselves (live: count=1) despite seeing 6 leases + every `bookerKey` via `/api/residents`; `frontDeskCard` then titles each card `shortKey(leaseAppKey)` ([app.js:562](../../../cmd/cafe-app/web/app.js)). Same self-anchor shape as the Clinic roster row — that fix won't reach this lens. | Café | pkg + FE | ★★ | M | 📋 ready |

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

- **Rotation to date:** LoftSpace ×17, Clinic ×16, Café ×7, Wellness ×3.
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799/:7801/:7802), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. All four apps exist + are exercisable live (`:7788` / `:7799` / `:7801` / `:7802`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-07-22:** Clinic — drove no-show→ledger auto-charge live (first-ever verify, converged once an account existed, as designed) + multi-site provider assignment; found unprofiled-site rows render blank, filed FE-only fix.
- **2026-07-22:** Café — drove self-order OpenTab→Charge→Settle→ledger live end-to-end (all correct); found no charge-correction op exists, filed pkg fix.
- **2026-07-22:** LoftSpace — drove Apply live via `127.0.0.1` origin, got silent write failures; root-caused to Gateway CORS default, confirmed clean via `localhost`, filed platform fix (lattice.md).
- **2026-07-22:** Wellness — drove studios/sessions/bookings live on the shared stack; found `CreateBooking` has no double-book or past-time guard, confirmed via a live duplicate booking, filed pkg fix.
- **2026-07-23:** Clinic — drove staff visit-series + Care→Wellness referral live; found `StartVisitSeries` has no active-series dedup guard, confirmed via 2 live duplicate series, filed pkg fix.
- **2026-07-27:** Clinic (Andrew-directed) — repro'd a reported patient/NanoID display leak live; root-caused to an unscoped localStorage key + no self-booking write path (403, Checkpoint unfiled) + a "Signed in as" name-resolution gap confirmed cross-vertical (3 of 4 apps, LoftSpace the exception); filed 4.
- **2026-07-28:** Café — drove self-order + staff POS live; the picker offers items `Charge` refuses, the POS has no catalog, tabs aren't itemized; filed 4.
- **2026-07-28:** Wellness — drove member/staff/instructor hats live; the schedule's Book is mostly dead, a called-off class tells nobody, attendance is invisible + member-erasable; filed 3.
- **2026-07-28:** LoftSpace — drove applicant, landlord + back-of-house hats live; no seeded world can approve a lease, and maintenance work never reaches the app; filed 2.
- **2026-07-29:** Clinic — drove patient, provider, front-desk + root hats live; the front desk renders a staff console it can't read a roster into; filed 3.
- **2026-07-29:** Café — drove self-order + front-desk hats live; the demo world seeds no menu, the composition layer is installed by nothing, front of house can't name anyone; filed 3.
- **Next:** Wellness.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

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
