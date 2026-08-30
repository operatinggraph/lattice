# Backlog — App Verticals (Stream 1)

Stream 1 = app-vertical packages + FEs (LoftSpace, Clinic). Advanced by the **Vertical Steward**; demand
filed by the **Vertical PO** (file-only). Index + cross-lane rules: [../backlog.md](../backlog.md).
**Row discipline** (one item = one row; State = token + ref + one-line next; detail lives in the design
doc + git, never narrated in the cell): see [lattice.md → How this board works](lattice.md).

**Scales.** Imp ★/★★/★★★ · Size XS–XL. **State.** 📋 ready · 🏗️ building · 📐 awaiting-Andrew ·
✅ ratified (designed, not built) · 🚧 blocked (`blocked-on:` / Andrew-gated) · 🗄️ shelved (`revive:`).

## Vertical demand backlog (PO discovery)

Open items only — **shipped demand is in the Done log**. The PO files (tagged vertical + owner: FE = Sally +
FE Engineer · pkg = Package Designer · platform = component owner + Lattice lane); the Steward + FE
Engineer build. **No-paper-over:** a missing platform *primitive* routes to [lattice.md](lattice.md) and
the row is `🚧 blocked-on:` it (a missing *lens* is package work, built here).

| Item | What it is (PO view) | Vertical | Owner | Imp | Size | State |
|---|---|---|---|---|---|---|
| **Edge showcase app (Facet)** | Discovery-driven personal client on the Edge foundation: hardcodes only IdP login + connect; services, ops, forms, tasks, panes arrive as data via `edge-manifest` personal lenses + a descriptor vocabulary (#52/#54/#55). PWA-first. | Cross-vertical | Sally + FE Engineer + pkg | ★★★ | XL | 🚧 no open residual — next increment TBD · [design §7](../../implementation-artifacts/edge-showcase-app-design.md) |
| **Facet on a literal iOS device** | The SwiftUI renderer builds + runs as a macOS proxy only. A real iOS/simulator build proves platform packaging (App Store viability), which FORK-1's freeze no longer waits on. Also unblocks real `swift test` in place of the hand-mirrored `swift run` harness. | Cross-vertical | Sally + FE Engineer | ★ | M | 🗄️ shelved (revive: a machine with full Xcode — this host has CommandLineTools only) · [design §7.10](../../implementation-artifacts/edge-showcase-app-design.md) |
| **The executed lease still doesn't name its tenant** | `/api/lease-document` renders `Tenant: vtx.identity.edu97ix…` — the applicant's real name is never assembled (`doc.TenantName`), a sensitive link-discovered aspect with no egress-declaration path. The landlord party now resolves via the unit's `manages` link (shipped `d46ab947`). | LoftSpace | pkg | ★★ | S | 🚧 blocked-on: [lattice.md](lattice.md) Loom link-discovered egress · refuted at build 2026-08-27 · [evidence](../../implementation-artifacts/lease-tenant-name-fire-brief.md) |
| **The executed lease document generates but never attaches** | `missing_leaseDocAttach`'s directOp submits `AttachObject` as the Weaver service actor — was `AuthDenied`, now fixed (grant restored live 2026-08-27, [lattice.md](lattice.md) done log). Weaver's own sweep reclaims the stale claim + re-fires on its normal lease-expiry schedule; not yet observed converged. | LoftSpace | platform | ★★ | S | 🏗️ converging · permission fix landed 2026-08-27, awaiting Weaver's next reclaim (no code change needed) |
| **29 of 63 appointments carry no site and `clinicSiteBackfill` closes the gap for none** | 16 have a provider at EXACTLY ONE site — the shape the op documents as convergent — yet stay unlinked; verified live (`weaver-state` kv) that 26 of 28 violating entities have never been dispatched even once — a Weaver engine gap, not a query bug (a `violating`-predicate fix would silently mask it). | Clinic | platform | ★★ | S–M | 🚧 blocked-on: [lattice.md](lattice.md) `[Weaver] The sweep enumerates state keys, not declared×projected work` |
| **Every front-desk POS write fails about half the time, mid-order** | A barista gets `ScriptTimeout: script exceeded wall budget 250ms`, no retry. The staff path walks confinement live inside the wall ([ddls.go:765](../../../packages/cafe-domain/ddls.go:765)); the resident's identical op is `authTargetValidated` and skips it — paced 1/s, same host: staff Charge 9/20 vs resident 2/20. `b997ff2a` already removed the clinic-shaped redundancy here. | Café | platform | ★★ | S–M | 🚧 blocked-on: [lattice.md](lattice.md) confinement-walk batched-read row · fork resolved PLATFORM |
| **The auto no-show sweep still bills the $25 it was fixed not to bill** | The `pastDueBookings` playbook states `noShowFeeCents: "0"`, but `GapActionSpec.Params` is `map[string]string` so the op receives the string, `optional_number` ([ddls.go:2989](../../../packages/wellness-domain/ddls.go:2989)) returns None and the 2500 default is written. Live: 3 more $25 fees since `fab81059`; 23 of 42 bookings carry one. | Wellness | pkg | ★★ | S | 🚧 blocked-on: [lattice.md](lattice.md) `[Weaver] A playbook param can only be a string literal` · alt: clinic's separate-op split |
| **8 bookings stand `booked` on live classes that ended weeks ago** | `pastDueBookings` flipped one booking 1s after its class ended yet has never dispatched 8 older ones on live sessions (`endsAt` 2026-07-20…08-23) — a second vertical witnessing the never-dispatched shape the clinic site row names. | Wellness | platform | ★ | S | 🚧 blocked-on: [triage §5](../../../docs/reviews/lattice-designer-triage-2026-08-27.md) — the clinic row's named lattice.md row no longer exists (design HELD `097fdfb6`, decline-retry half shipped `81a1c94`); re-derive the blocker |

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

- **Rotation to date:** LoftSpace ×27, Clinic ×26, Café ×17, Wellness ×14.
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799/:7801/:7802), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. All four apps exist + are exercisable live (`:7788` / `:7799` / `:7801` / `:7802`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-08-27:** Wellness — drove member/instructor/staff hats live through book/roster/bill; $525 of no-show fees nobody asked for and nobody can waive, 29 classes nobody can check in; filed 2.
- **2026-08-27:** LoftSpace — drove applicant + landlord hats live through browse/apply/sign/decide/renew; a closed task keeps its grant while an expired one keeps its inbox slot, 19 ruled-out applicants can still sign; filed 4.
- **2026-08-27:** Clinic — drove patient, provider + front-desk hats live through schedule/document/series; the whole forward schedule is front-desk-invisible, 3 of 4 providers can't sign in, a patient can't read their own note; filed 3.
- **2026-08-28:** Café — drove resident + two front-of-house hats live through menu/tab/charge/settle/ledger; every staff write ScriptTimeouts while the resident path converges clean; filed 3.
- **2026-08-28:** Wellness — drove member/instructor/two staff hats live through schedule/book/roster/ledger; the auto no-show sweep still bills $25, 8 bookings never converge, one class remains; filed 3 + 1 platform.
- **2026-08-28:** LoftSpace — drove applicant + landlord hats live through browse/apply/sign/decide/renew/statement; expired tasks read as live work, the one bill is 97 ungrouped lines, 33 of them quote an internal PO ruling; filed 3.
- **2026-08-29:** Clinic — drove patient, provider + front-desk hats live through book/cancel/document/bill; staff writes time out ~1 in 10 while the same ops self-serve in a third the time, and the descriptors publish only the patient's slice; filed 2.
- **2026-08-29:** Café — drove resident + front-of-house + backOfHouse hats live through menu/tab/charge/void/settle/pay; every staff write blows the 250ms script wall, the balance never becomes a bill; filed 2 + 1 platform.
- **2026-08-29:** Wellness — drove member/instructor/two staff hats live through schedule/book/roster/ledger/bill; nobody at the desk can mark attendance and 25 of 25 bookings are no-shows; filed 3.
- **Next:** LoftSpace.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

- 2026-08-30 · `de326532`+`9eee9bcd` · café/clinic/wellness's `/api/op-catalog` narrows via `?types=` to just the ops each app renders; loftspace stays unfiltered (task-bound ops can name any op meta).
- 2026-08-30 · `5be4d4ee` · Facet's Me screen gets a demo-only reversible offline-pause toggle (`FACET_DEMO_CONTROLS`), env-gated. [Design §11](../../implementation-artifacts/facet-app-ux.md).
- 2026-08-30 · `a5077b9a` · Front desk can finally mark a member present — `SetBookingAttendance` grants frontOfHouse, workplace-confined exactly like `CancelBooking`. wellness-domain 0.22.14.
- 2026-08-29 · `75b05007`+`6f3b6212` · No-show ledger lines now name the class's date, not just its (repeating) name — classStartsAt was already served, just never rendered. wellness-app FE only.
- 2026-08-29 · `02d007a2` · Wellness studio grid-dry warning (client-side, no lens change) + a bare-entity-key guard on every app's ledger-memo render fixes the live remediation memo with no data mutation.
- 2026-08-29 · `681f36f1`+`71aba1b4` · Wellness front desk gets a debounced guest name-search + "＋ New guest" modal, replacing the raw identity-key input. wellness-domain 0.22.13.
- 2026-08-29 · `b3af3268` · Café/clinic/wellness staff nav now hides front-desk tabs for a worksAt-only, non-frontOfHouse staffer instead of hard-403ing on click — new GET /api/staff-hats exposes the server-resolved hat.
- 2026-08-29 · `361286fb` · wellnessClassPriceSettlement's exhausted missing_price_charge gap now escalates to Augur instead of parking behind an unread Health warning — mirrors lease-signing's renewalComplete. wellness-ledger 0.2.16.
- 2026-08-29 · `71a4c245` · The Manage Menu grid badges an item whose place was retired and can Relocate it to the staffer's own workplace via the existing SetMenuItemLocation op. cmd/cafe-app only.
- 2026-08-29 · `abd881cf` · The house tab now carries a due date and OVERDUE banner — FIFO-aged from the oldest unpaid debit, 15-day grace, no ledger schema change (balance stays derived). cmd/cafe-app only.
- 2026-08-29 · `ffcef9f5` · OpenTab now rejects a lease the landlord hasn't approved (live $4.50 self-order gap) — server-side LeaseNotApproved + POS picker badges the unapproved option. cafe-domain 0.11.27.
- 2026-08-29 · `c5c7f994` · LoftSpace task cards for the same op now show which application they're about (unit address, joined off `scopedTo`) — verified live. triage §4
- 2026-08-29 · `fe85490d` · A descriptor-driven front desk can finally run the clinic — CreateAppointment/SetAppointmentStatus op-metas widened to the staff-reachable surface (wellness precedent). clinic-domain 0.34.15.
- 2026-08-29 · `a8de5330` · A charge/refund for a called-off wellness class now names the class — className/classStartsAt snapshot onto booking.status at write time, survives TombstoneSession. wellness-domain 0.22.11, wellness-ledger 0.2.15.
- 2026-08-29 · `0bda5e71`+`aca5009d` · Front-desk `ScriptTimeout`s (~1 in 10) fixed — confinement check no longer runs `actor_holds_operator()` twice per write. clinic-domain 0.34.14.
- 2026-08-29 · `f8f4c454`+`3da97510`+`f9a03a08` · A recurring visit series' front-desk card now shows its clinic site and can set one via the existing `SetVisitSeriesSite` op, reusing the appointment "Set site" modal. clinic-reminders 0.10.5.
- 2026-08-29 · `160ae80a` · A stuck renewal (retry budget exhausted) now escalates to Augur AI-reasoning instead of only raising a standing Health issue — mirrors leaseApplicationComplete's Augur block. lease-signing 0.31.11.
- 2026-08-29 · `f7f58eac` · A landlord's decision now preserves the qualification profile it was based on — `.decidedProfileSnapshot` create-only-stamped on the first decision. lease-signing 0.31.10.
- 2026-08-29 · `13eb28ac` · A workplace-holding wellness instructor without `frontOfHouse` can no longer read a whole building's roster via bare `covers()` — confined to the session(s) they personally lead. bookings.go, sessions.go, residents.go.
- 2026-08-28 · `682b3d5d` · The one-bill statement now groups by month with a net subtotal per period, instead of one 97-line flat list. loftspace-app FE only, no lens change.
- 2026-08-28 · `2b495eba` · Expired open tasks now badge "expired" (red) instead of "open" — mirrors Facet's isExpired check; Complete stays enabled per platform's surface-only-past-deadline design.
- 2026-08-28 · `2f3ba08d` · A patient can finally read their own encounter note — `clinicEncountersReadSpec` gains a patient self-anchor, FE `asSelf` note-suppression dropped. clinic-domain 0.34.12
- 2026-08-28 · `4da005a0` · Visit series survive a tombstoned provider via a new atSite link, mirroring appointment's mechanism. clinic-reminders 0.10.4
- 2026-08-28 · `792ca811` · Clinic's 4 duplicate providers + the "Dr Proof" sentinel reaped live, incl. orphaned litter appointments; Patel stays deliberately unbound (row's "bind Patel" note was wrong).
- 2026-08-28 · `ecb0a0d1` · Unled wellness classes now flag `missingInstructor`; roster badges + points staff at the already-granted Reassign control. wellness-domain 0.22.10
- 2026-08-28 · `38906487` · Dr. Classic Demo given a practicesAt site + all appointments stamped atSite — front desk's forward schedule (0 → 22 scheduled rows visible) finally readable/actionable.
- 2026-08-28 · `48c59f4b` · Front-desk surfaces (café/clinic/wellness) now require `frontOfHouse`, not just `worksAt` — a worksAt-only staffer no longer sees other residents'/patients'/members' rows they hold zero write grant for. triage §7
- 2026-08-28 · `ec63ae71` · descriptorform vocabulary strand CLOSED (items 1–9) — the drift gate now pins `{me.<type>}`/`{entity.<column>}` too. triage §2
- 2026-08-28 · `5ac9b361` · VoidCharge's itemized (lineId) form declared in its InputSchema — descriptor clients (Facet included) can finally submit it, not just cafe-app's hand-built button. cafe-domain 0.11.25
- 2026-08-28 · `b997ff2a` · Café front-desk ScriptTimeout on OpenTab/Charge/VoidCharge/CreateMenuItem fixed — a redundant workplace_exempt() operator recheck removed; verified live, no timeout. cafe-domain 0.11.24
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
