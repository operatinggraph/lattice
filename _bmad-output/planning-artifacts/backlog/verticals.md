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
| **Edge showcase app (Facet)** | Discovery-driven personal client on the Edge foundation: hardcodes only IdP login + connect; services, ops, forms, tasks arrive as data via `edge-manifest` personal lenses + a descriptor vocabulary (#52/#54/#55). PWA-first. | Cross-vertical | Sally + FE Engineer + pkg | ★★★ | XL | 🏗️ building · [design §7.12](../../implementation-artifacts/edge-showcase-app-design.md) · 2nd-renderer spike Inc 3 shipped (descriptor-form renderer, live-verified confirmed write) · next: literal iOS build only |
| **Clinical notes are write-only** | `RecordEncounter` PHI (`ddls.go:333-336`) captured, never projected. The cited `clinicPatientsRead` Secure-Lens precedent does NOT extend — that decrypts identity-anchored Vault ciphertext; this is raw plaintext on a non-identity vertex, and that exact shortcut was already REJECTED pre-Vault (`vault-crypto-shredding-design.md` ratification decision #2). | Clinic | pkg | ★★★ | M | 🚧 blocked-on: Vault extended to non-identity content (architectural fork, Andrew) |
| **CreateBooking has no double-book / past-time guard** | `wellness-domain` `CreateBooking` (`ddls.go:1002-1082`) claims a free seat but never checks the booker already holds one on that session (unlike clinic's `PatientDoubleBook` slot-claim / café's `OpenTabAlreadyExists`), nor that `session.schedule.startsAt` is still future (unlike clinic's `ScheduleInPast`). Live-confirmed: identity `MQsmTTAgNkngkdEjQz9L` holds 2 live bookings on session `wvgK4ajnFVyfYJbuhYhJ`, whose class already ended. | Wellness | pkg | ★★ | S | 📋 ready |

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

- **Rotation to date:** LoftSpace ×16, Clinic ×13, Café ×5, Wellness ×2.
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799/:7801/:7802), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. All four apps exist + are exercisable live (`:7788` / `:7799` / `:7801` / `:7802`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-07-12:** Clinic — drove booking/My Appointments live + code-verified permission pins; found self-service patients can book but never reschedule/cancel themselves (operator-only ops), filed.
- **2026-07-17:** Café — hand-minted a lease + drove OpenTab/Charge/Settle + self-service scope=self live (open/settle-own-lease ✅, cross-lease + Charge correctly denied ✅); found no classic demo seed data + no self-order catalog, filed both.
- **2026-07-18:** LoftSpace — drove Applicant Browse/Apply/My Applications live (clean) + Landlord console; caught a live reload race hard-failing sign-in with `RotateClaimKey requires state=unclaimed, got claimed`, root-caused + filed.
- **2026-07-18:** Wellness — first-ever PO exercise (live since 07-09, never driven); found empty studios/sessions, hand-minted one + proved self-service booking/cancel end-to-end live, filed the seed gap + missing studio-admin FE.
- **2026-07-22:** Clinic — drove no-show→ledger auto-charge live (first-ever verify, converged once an account existed, as designed) + multi-site provider assignment; found unprofiled-site rows render blank, filed FE-only fix.
- **2026-07-22:** Café — drove self-order OpenTab→Charge→Settle→ledger live end-to-end (all correct); found no charge-correction op exists, filed pkg fix.
- **2026-07-22:** LoftSpace — drove Apply live via `127.0.0.1` origin, got silent write failures; root-caused to Gateway CORS default, confirmed clean via `localhost`, filed platform fix (lattice.md).
- **2026-07-22:** Wellness — drove studios/sessions/bookings live on the shared stack; found `CreateBooking` has no double-book or past-time guard, confirmed via a live duplicate booking, filed pkg fix.
- **Next:** Café.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

- 2026-07-22 · `239e3846` · Clinic staff site-management list shows "(unnamed site)" instead of a bare trailing separator for a provider assigned to a building whose `SetSiteProfile` never ran
- 2026-07-22 · `—` · Facet for staff — front-desk/operations worlds CLOSED — F1–F5 all shipped; F5's `e269c27d` + the live-proven claim beat were the last pieces, board row was stale
- 2026-07-22 · `78927466` · Facet claim button now reads/submits the Core-KV vertex key (`data.taskKey`), not the manifest row's own storage key — was failing every claim closed with HydrationMiss; live-proven severed-network claim beat
- 2026-07-22 · `35ca90f5` · `seed-showcase.go`'s `ensureStaff` resolves by `holdsRole` instead of an ambiguous `worksAt` scan — was silently mis-resolving `FACET_STAFF_NANOID` to the maintenance tech once F5 gave it a second `worksAt` link
- 2026-07-22 · `28c69837` · `RecordIdentityPII` scoped to unclaimed identities for standing frontOfHouse/backOfHouse grants; operator + task/self-scoped submissions exempt — facet-staff-worlds-design.md §3.2
- 2026-07-22 · `3d98f51c` · Café `VoidCharge` — corrects a mis-tapped charge; mirrors Charge's OCC-conditioned totalCents accumulate but subtracts, clamped at 0; operator/frontOfHouse only, no self-service grant (fraud vector)
- 2026-07-21 · `e269c27d` · Facet staff worlds F5 Inc 2 — `edgeStaffWorkOrders` workplace-spine lens (first inbound var-length walk) + grant branch + offline-capable Work-order UI; severed-network resolve→drain live-proven
- 2026-07-21 · `5f2517ab` · Facet staff worlds F5 Inc 1 — `maintenance-domain` work orders; ResolveWorkOrder is the op the queued task grants, §10.6 auto-complete closes it, so no completion op exists
- 2026-07-21 · `5f2517ab` · Maintenance work-order producer CLOSED — shipped in F5 Inc 1; identical-notes re-resolve is an accepted no-op so an offline drain retry cannot lose the tech's work
- 2026-07-21 · `566d710a` · Showcase demo session no longer ages out — wellness session id rolls by UTC day so a reseed always mints a FUTURE class + Nearby hides past-start entities; live-proven (rolled FUTURE, legacy fixed PAST + filtered)
- 2026-07-21 · `bded5cc8` · scope=self ownership guards treat a tombstoned link as absent — café/clinic/wellness `== None` probes adopt F4's `== None or .isDeleted` (7 sites); tombstoned applicationFor drives Rejected (accepts without the fix)
- 2026-07-20 · `a3fa5318` · Facet staff worlds F4 — worksAt write confinement, the multi-org gate; live: staff AuthDenied at a second building, operator unconfined
- 2026-07-20 · `21130319` · Facet staff worlds F3 pane — `GET /api/staff/worklist` (one txn, no workplace predicate; RLS is the boundary) + Worklist screen; live: staff reads 1 of 3 appointments, no-`worksAt` actor 0, no Work tab for a resident
- 2026-07-19 · `c663a27e` · Cancel a booking from Facet — `edgeEntityBookings` own-bookings lens (bookedBy, inherently private) + `{entity.<column>}` fill seam; live-proven booking AND session seat both tombstoned, released seat re-booked
- 2026-07-19 · `415e18f3` · Facet staff worlds F3 read spine — `staffReadGrants` (cap-read.staff, building-anchored) + workplace anchors on both worklist tables; live wire→read-1 / unwire→read-0
- 2026-07-19 · `c662dc54` · ~~F3 anchors~~ — **this SHA never reached main** (dangling, on no branch); the work was reconstructed in `5c797e03` + `415e18f3`. Left as a marker: a Done-log SHA is not proof the code landed
- 2026-07-19 · `58d165ee` · Facet staff worlds F2 — `permission forOperation meta` + role-derived catalog/queue sibling lenses + staff read-grant slice; frontOfHouse Personal-Lens control grant (staff device synced nothing without it)
- 2026-07-19 · `cc50f86a` · Facet staff worlds F1 — `worksAt` staff spine + staff op set widened onto the shipped `frontOfHouse`; showcase staff persona; live-verified narrower-than-root
- 2026-07-19 · `753637ca` · Descriptor dispatch declares its optional reads — `Dispatch.OptionalReads` + a `:id` bare-id modifier for link keys; café OpenTab/Settle now fully Facet-drivable, open→settle→reopen proven from the declarations
- 2026-07-19 · `212dd3f1` · Self-anchored op params are declared, not name-guessed — `edgeIdentity` projects typed `selfAnchors`; `{me.<type>}` joins the contextParams vocabulary; café OpenTab renders a fieldless form, live-proven in Core KV
- 2026-07-19 · `c3ec584b` · `reinstall-package` same-version edits reach the Processor again — op requestId now folds a mutation digest, so only genuinely identical work dedups; unblocked the live proof of `51a418b5`
- 2026-07-19 · `51a418b5` · Personal Lens keeps its business key columns across a cypher edit — hot-reload threaded none, dropping the executor to its single-key fallback; edgeCatalog's 142k-error retry loop cleared to 0
- 2026-07-19 · `—` · Display-name N3 CLOSED — live-verified on the showcase stack: feed serves decrypted `displayName` "Riley Chen" + named anchors; tail was a stale device mirror, no code defect
- 2026-07-19 · `—` · Display-name N3 tail localized — SYNC delta carries both N3 fields; cloud path clean end-to-end, device mirror serves a stale pre-N3 row (diagnosis only, no code change)
- 2026-07-19 · `2a0af7e3` · Display-name N3 tail narrowed — both design-named candidates falsified (engine resolves both aspect shapes, now pinned; compiled rule is current); omission is downstream of projection
- 2026-07-19 · `93c6064d` · SetLocationPresentation replaces (was create-only → RevisionConflict on any second set, untested) + seed-showcase names an already-seeded world; live-verified on the showcase stack
- 2026-07-19 · `dda7ad98` · Facet dispatch targets resolve by declared type — `dispatch.targetType` vocabulary + renderer gate; unresolvable ops degrade instead of failing at the Processor; live-verified both directions
- 2026-07-19 · `502d3b4d` · Wellness classic-demo seed — `seed-classic-demo` mints a studio + one bookable session (15-min grid, slot-claim optionalReads); both ops accepted live and projected through to `/api/studios` + `/api/sessions`
- 2026-07-19 · `074c0b86` · Facet session idle gate + auth-death bounce — renewal gated on activity (30m window); a terminal 401 or a whoami-confirmed dead cookie closes the feed and bounces to `/login`, both modes; live-verified
- 2026-07-19 · `4185b3c0` · Display names N3 (self-name) — me-lens projects the sealed `name` envelope, `edge/vault.SelfName` decrypts it in-engine on both hosts; fixes a Refractor hot-reload bug that silently refused Personal Lens cypher edits
- 2026-07-19 · `a02784ee` · Display names N2-tail (scoped-target name lens) — edgeTasks walks task→leaseapp→unit, projects the unit's `.presentation` name; Facet task rows read "Unit 1 lease" not a bare NanoID
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
