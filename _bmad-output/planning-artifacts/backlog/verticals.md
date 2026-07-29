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
| **Staff POS can't ring up a catalog item** | The café prices a catalog only the resident self-order path can use: a staff `Charge` requires a hand-keyed `amountCents` and ignores `menuItemKey` (live — staff charged a free-typed $99.99; `Charge{menuItemKey}` with no amount → `InvalidArgument: amountCents: required`), so whoever is at the counter retypes every price and can key any figure. Bind a catalog item on the staff path the way self-order does, keeping the free amount for off-menu. | Café | pkg + FE | ★★ | M | 📋 ready |
| **A tab shows a total and nothing else** | Self-order shipped, so the app is now the ordering surface — but a resident's only feedback is the running `totalCents`: no line items on the tab, no memo on the posted ledger debit (live — two identical $2.75 self-orders render "$5.50", the ledger reads "+$5.50" unlabelled), so nobody can check what was ordered or whether a second tap registered. `cafe-domain`'s README parks itemization as YAGNI "no demand row asks for it yet"; this is that row. | Café | pkg + FE | ★★ | M | 📋 ready |
| **`cafe-domain` README says a shipped guard isn't built** | Its "Out of scope" claims one-open-tab-per-lease exclusivity "is not built" while the same README's Inventory lists the `cafeOpenTabGuard` aspect and `ddls.go:45,101-102` rejects `OpenTabAlreadyExists` — live-confirmed on a second `OpenTab`. A stale "not built" on a safety guard invites rebuilding it. | Café | pkg | ★ | XS | 📋 ready |
| **My Classes hides attendance and lets a member erase it** | `/api/bookings` serves `status` (booked/attended/noShow); `myClassCard` ([app.js:572](../../../cmd/wellness-app/web/app.js)) renders the rate badge instead and never shows it, while offering an unconditional Cancel. `CancelBooking` (ddls.go:2171) has no past-class and no attendance guard — the asymmetry to `SetBookingAttendance`'s `SessionNotStarted`. Live: cancelling a booking on a class that ended 7 days earlier was accepted, so a no-show is member-deletable. | Wellness | pkg + FE | ★★ | M | 📋 ready |
| **No seeded world can approve a lease — the terminal step is unreachable** | 0 `.decision`/`.tenancy`/renewal across 29 applications; three blocks: the showcase unit's `availableFrom` is date-only ([seed-showcase.go:1237](../../../scripts/seed-showcase.go)) so the first approve's `time.rfc3339_utc` rejects live, classic-demo units carry no `manages` link so no landlord can decide, the other managed unit has no `.listing`. `SetListing` stores it verbatim against its RFC3339 contract ([ddls.go:387](../../../packages/loftspace-domain/ddls.go)). | LoftSpace | pkg | ★★★ | M | 📋 ready |
| **LoftSpace can't work a maintenance queue** | `maintenance-domain` ships with the showcase world and 14 work orders sit `locatedAt` LoftSpace units, but `cmd/loftspace-app` names neither `ReportIssue` nor `ResolveWorkOrder` — no tenant report path, no super queue. Live, 5 open `ResolveWorkOrder` tasks render as disabled "Complete in Loupe" cards titled by a bare NanoID, and `openTaskRow` ([tasks.go:34](../../../cmd/loftspace-app/tasks.go)) drops the lens's `queuedRole` so none can be claimed. | LoftSpace | pkg + FE | ★★★ | M | 📋 ready |

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

- **Rotation to date:** LoftSpace ×17, Clinic ×15, Café ×6, Wellness ×3.
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799/:7801/:7802), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. All four apps exist + are exercisable live (`:7788` / `:7799` / `:7801` / `:7802`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-07-18:** LoftSpace — drove Applicant Browse/Apply/My Applications live (clean) + Landlord console; caught a live reload race hard-failing sign-in with `RotateClaimKey requires state=unclaimed, got claimed`, root-caused + filed.
- **2026-07-18:** Wellness — first-ever PO exercise (live since 07-09, never driven); found empty studios/sessions, hand-minted one + proved self-service booking/cancel end-to-end live, filed the seed gap + missing studio-admin FE.
- **2026-07-22:** Clinic — drove no-show→ledger auto-charge live (first-ever verify, converged once an account existed, as designed) + multi-site provider assignment; found unprofiled-site rows render blank, filed FE-only fix.
- **2026-07-22:** Café — drove self-order OpenTab→Charge→Settle→ledger live end-to-end (all correct); found no charge-correction op exists, filed pkg fix.
- **2026-07-22:** LoftSpace — drove Apply live via `127.0.0.1` origin, got silent write failures; root-caused to Gateway CORS default, confirmed clean via `localhost`, filed platform fix (lattice.md).
- **2026-07-22:** Wellness — drove studios/sessions/bookings live on the shared stack; found `CreateBooking` has no double-book or past-time guard, confirmed via a live duplicate booking, filed pkg fix.
- **2026-07-23:** Clinic — drove staff visit-series + Care→Wellness referral live; found `StartVisitSeries` has no active-series dedup guard, confirmed via 2 live duplicate series, filed pkg fix.
- **2026-07-27:** Clinic (Andrew-directed) — repro'd a reported patient/NanoID display leak live; root-caused to an unscoped localStorage key + no self-booking write path (403, Checkpoint unfiled) + a "Signed in as" name-resolution gap confirmed cross-vertical (3 of 4 apps, LoftSpace the exception); filed 4.
- **2026-07-28:** Café — drove self-order + staff POS live; the picker offers items `Charge` refuses, the POS has no catalog, tabs aren't itemized; filed 4.
- **2026-07-28:** Wellness — drove member/staff/instructor hats live; the schedule's Book is mostly dead, a called-off class tells nobody, attendance is invisible + member-erasable; filed 3.
- **2026-07-28:** LoftSpace — drove applicant, landlord + back-of-house hats live; no seeded world can approve a lease, and maintenance work never reaches the app; filed 2.
- **Next:** Clinic.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

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
- 2026-07-27 · `ff22c446` · `docs/components/edge-manifest.md` refreshed to its real eighteen Personal Lenses + three generated read-grant producers (was six).
- 2026-07-27 · `1389a0e9` · A naturally-ended visit series reads "ended", not "paused" — `series_status` replaces the fused `active` boolean; Resume no longer offered. clinic-recurring-visit-series §8
- 2026-07-27 · `ab2280ed` · Entity detail withholds a self-administer op from a row that isn't the caller's own (`crossHatMismatch`); instructorKey provenance rides 3 lenses (`df3afd75`). persona-worlds §7
- 2026-07-27 · `ab2280ed` · Task detail routes the business op AND `ClaimTask` through `opButton`'s degrade gate via `ctx.taskKey`; a queued row is now click-through too.
- 2026-07-27 · `56504197` · Front desk books a walk-in guest at the standard rate (`CreateBooking` already allowed it); `/api/roster-sessions` stops the roster leaking other buildings' classes.
- 2026-07-27 · `7911ccf6` · The descriptor corpus speaks human — titles/date-widgets/auto-filled keys across 12 packages, `{me.<type>?}` optional templates, label floor; booking E2E-verified at the resident rate. facet-discovery-restoration §7
- 2026-07-27 · `bb9fe41a` · Live-walk follow-on — pickered targets offer without ctx resolution, no label floors to a short id, the orphaned task healed; the modal's lens-race + loader gaps filed to lattice.md. facet-discovery-restoration §6
- 2026-07-27 · `86564999` · The discovery covenant restored — panes/labels/state-conditions become descriptor data, `staff.go` dies, `lint-facet-discovery` gates cmd/facet in CI. facet-discovery-restoration-design.md
- 2026-07-27 · `69d8ccce` · `worksAt_covers` follows the containedIn-page cursor — 9 S10-pinned copies + cafe-domain's `location_covers` sibling fixed identically; `MAX_PARENT_PAGES=4` bounds the walk, common path unchanged.
- 2026-07-27 · `a7b1f935` · Facet resolves `patient`/`visitseries` dispatch targets via the Protected staff-worklist pane, not the mirror. facet-entity-browse-design §9
- 2026-07-27 · `—` · 29-package census re-run vs S2-S5/S8 — S3/S4/S5/S8 clean corpus-wide (S4 now S10-gated); S2 table-format gap filed. standard §14
- 2026-07-27 · `af302004` · `actor_holds_operator` follows the role-page cursor — 15 S10-pinned copies across 9 packages fixed identically; `MAX_ROLE_PAGES=4` bounds the walk, common path unchanged. facet-staff-worlds §12
- 2026-07-27 · `—` (closed, not built) · Ownership-op `consumer` delegation grant — decided NOT to build (irrevocable, PII-decrypt exposure, no demand); stays operator-conferred. persona-worlds W2 Inc 5 §4
- 2026-07-27 · `65254aee` · seed-showcase reminds instead of RevisionConflict-crashing — every `CreateUnclaimedIdentity` call declares its identityindex probes as optionalReads; ctx raised past the projection-lag window
- 2026-07-27 · `b0c6cef4` · Self-order Charge confined to the menu item's own building — `location_covers` mirrors `worksAt_covers` against the item's `servedAt` place
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
