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
| **Vertex-key parsing still happens outside `parts_of`** | ~11 sites hand-roll the three-segment test in bodies too different for S10's exact-digest alias rule to match — `loftspace-domain`'s `require_manages` (inlined, and fails `AuthDenied` not `InvalidArgument` — an error-class call, not a sweep), plus `rbac-domain`, `location-domain`, `privacy-base`, `identity-hygiene`, `augur`. §13.3 records why a structural gate was rejected. Consumer: whoever copies one next. | Cross-vertical | pkg | ★ | M | 📋 ready · [§13.3](../../implementation-artifacts/vertical-package-standard.md) |
| **Cypher-test harness is copy-pasted 24×** | Every `lens_cypher_test.go` opens with the same ~40-line embedded-NATS + deterministic-NanoID + vtx/aspect/edge fixture. S6 now binds with no exemptions, so satisfying it means copying those 40 lines again. One mechanical corpus-wide sweep to an `internal/lenstest` helper; doing it piecemeal would just create a second idiom ([standard §9](../../implementation-artifacts/vertical-package-standard.md)). Consumer: the next package to declare a lens. | Cross-vertical | pkg | ★ | M | 📋 ready |
| **Clinical notes are write-only** | `RecordEncounter` PHI (`ddls.go:333-336`) captured, never projected. The cited `clinicPatientsRead` Secure-Lens precedent does NOT extend — that decrypts identity-anchored Vault ciphertext; this is raw plaintext on a non-identity vertex, and that exact shortcut was already REJECTED pre-Vault (`vault-crypto-shredding-design.md` ratification decision #2). | Clinic | pkg | ★★★ | M | 🚧 blocked-on: Vault extended to non-identity content (architectural fork, Andrew) |
| **`permissions.go` grant matrix isn't a table in 10 packages** | S2 asks for identity-domain's tabular `Grant matrix:` header; 10 packages (named in §14) document the same grants as narrative prose instead — same info, unlintable shape. Mechanical, ~10 files. Consumer: whoever next scans a package's grants expecting the canonical table. | Cross-vertical | pkg | ★ | M | 📋 ready · [§14](../../implementation-artifacts/vertical-package-standard.md) |
| **Five identity ceremony ops stay undiscoverable** | `CreateUnclaimedIdentity`, `RotateClaimKey`, `InitiateCredentialLink`, `CompleteCredentialLink`, `UnlinkCredential` carry stated `[no-op-meta:]` exemptions ([§8](../../implementation-artifacts/vertical-package-standard.md)). Consumer is 3 hardcoded in Facet (`cmd/facet/credentials.go`) + 2 in staff web apps/the CLI, not "Facet, all five" as first filed. | Cross-vertical | pkg | ★★ | M | 🚧 blocked-on: 3 new OpMetaSpec vocabulary primitives, no precedent to mirror — [lattice.md](lattice.md) |

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

- **Rotation to date:** LoftSpace ×16, Clinic ×14, Café ×5, Wellness ×2.
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
- **2026-07-23:** Clinic — drove staff visit-series + Care→Wellness referral live; found `StartVisitSeries` has no active-series dedup guard, confirmed via 2 live duplicate series, filed pkg fix.
- **Next:** Café.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

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
- 2026-07-27 · `499bf482` · CreateSession snapshots its studio onto a new `atLocation` link; `session_locations` falls back to it post-tombstone, mirroring clinic's `atSite` — staff keep authority over outstanding bookings
- 2026-07-27 · `9d8ac19b` · Reschedule/SetAppointmentStatus keep authority after a provider tombstone via the appointment's own `atSite` link; wellness sibling narrowed to its own row
- 2026-07-27 · `—` (closed, not built) · Vertical dev-login persona fence — by-design, mirrors `up-facet` + the F20.5 declared-origin fence; a static list would regress free-form dev sign-in
- 2026-07-27 · `0badf04e` · `RescheduleAppointment`'s `provider` field gets `x-entityRef`; `patient` stays free-text — pickers only read the mirror, still barred to patient PII. facet-entity-browse-design §9
- 2026-07-27 · `6985a24d` · seed-showcase's recovery helpers fail loud on a KV read anomaly, warn on an ambiguous bind (RevisionConflict/orphan-adopt + ctx-window gaps closed by `65254aee`, below)
- 2026-07-27 · `d2ae444f` · `RecordServiceOutcome` resolves its own template off the instance's link too; only the caller-asserted `serviceprovider` hop stays debt
- 2026-07-27 · `023359f5` · `DecideLeaseApplication` stamps tenancy off its own resolved unit, not a payload field a decline never carries; closes 1 of 2 readTemplateDebt entries
- 2026-07-27 · `526edfdf` · The claim ceremony gets a live walker — it found a live capability-projection gap the unit tests couldn't see; bug filed to lattice.md, repro `make test-claim-ceremony`. facet-staff-worlds §13.1
- 2026-07-27 · `5c914479` · A pre-existing identity acquires the consumer grant the Gateway promises it — bound personas reach the 20 self-service ops; absent-only, so a RevokeRole holds. facet-staff-worlds §13
- 2026-07-27 · `2c41318b` · The instructor + serviceprovider hats get their op — two inert chips resolve; the standing guard, not the shared `provider` role, confines. facet-staff-worlds §12
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
