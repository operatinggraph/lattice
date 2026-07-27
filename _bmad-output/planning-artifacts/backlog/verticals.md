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
| **Descriptor reads wrap a conditionally-supplied field** | `lint-package-standard`'s read-template rule holds 1 remaining entry — service-domain `RecordServiceOutcome`'s `serviceprovider`→identity read, absent on the operator path. `serviceprovider` is caller-asserted, not derivable (unlike `template`), so this is deliberate residual debt, not a quick fix. | Cross-vertical | pkg | ★ | XS | 📋 ready |
| **`docs/components/edge-manifest.md` says six lenses; there are seventeen** | The page names none of the entity lenses and predates the walk-declared read-grant producers, against its own header rule that it updates in the same commit as the code. Consumer: whoever reads it before adding lens eighteen. | Cross-vertical | pkg | ★ | XS | 📋 ready |
| **A naturally-ended visit series still shows a working Resume button** | `active` fuses "not paused" with "not past `activeUntil`", so an ended-not-paused series shows Resume; it submits and succeeds but changes nothing observable. Needs a raw `paused`/`activeUntil` column + a 3-state badge. Consumer: front desk on an ended series. | Clinic | pkg | ★ | S | 📋 ready · [§9](../../implementation-artifacts/facet-entity-browse-design.md) |
| **`actor_holds_operator`'s multi-page branch has no test coverage** | The just-shipped cursor fix (S10, all 15 copies) is proven by a 15-copy digest pin + adversarial sandbox review, but no `_test.go` seeds an actor with >50 `holdsRole` links, so a regression in the cursor threading would ship silently. One fixture (51 roles, operator on page 2, assert `True`) in one package would retire the risk. Consumer: whoever next touches this helper without re-deriving the review. | Cross-vertical | pkg | ★ | XS | 📋 ready |
| **`cafe-domain`'s README predates three shipped fires** | The inventory table claims 1 vertex type / 1 aspect type and names no `menuitem`, `menuItemPrice`, `cafeOpenTabGuard`, `menuCatalog`, or `cafeLeaseWorkplaces` — roughly half the package is undocumented. Consumer: whoever reads it before adding to cafe-domain. | Café | pkg | ★ | XS | 📋 ready |
| **Task detail offers only the task's own op, and bypasses the resolve gate** | `openTaskDetail` renders `forOperationKey` alone, so `ClaimTask` (targetType `task`) never reaches the descriptor path — it stays on the hardcoded `claimTask()` affordance and the resolver's task candidate has no consumer. It also calls `openDescriptorForm` directly, skipping `opButton`'s degrade gate, so an unresolvable target surfaces as a server-side InvalidArgument. Consumer: any task-targeted op. | Cross-vertical | FE Engineer | ★ | S | 📋 ready |
| **Clinical notes are write-only** | `RecordEncounter` PHI (`ddls.go:333-336`) captured, never projected. The cited `clinicPatientsRead` Secure-Lens precedent does NOT extend — that decrypts identity-anchored Vault ciphertext; this is raw plaintext on a non-identity vertex, and that exact shortcut was already REJECTED pre-Vault (`vault-crypto-shredding-design.md` ratification decision #2). | Clinic | pkg | ★★★ | M | 🚧 blocked-on: Vault extended to non-identity content (architectural fork, Andrew) |
| **Entity detail attaches cross-hat ops** | `openEntityDetail` offers any op whose `dispatchTargetType` matches the row's `entityType` (`app.js`), so a multi-hat human sees another hat's op on a shared type and it fails closed in-script. The hat surface got a `dispatchClass` term; entity detail needs a provenance stamp `manifest.ent` rows don't carry. UX-only, no authorization defect. Consumer: §3.4's multi-hat human on session/appointment rows — surface grew with the §3.3 Inc 1 descriptors (8 more ops now carry a targetType). | Cross-vertical | FE Engineer | ★ | S | 📋 ready |
| **A non-resident guest cannot be booked into a class** | The front desk's member directory is lease-anchored (`wellnessMembers` requires a `.tenancy`), so a guest with no tenancy at the building is unofferable — while `CreateBooking` itself constrains only the session's location and never the booker, so the write side would permit it. Needs a product call on whether wellness classes admit non-residents before any surface. Consumer: a member's guest, and the drop-in class the demo narrates. | Wellness | pkg | ★ | S | 📋 ready |
| **Wellness roster picker offers classes at other buildings** | The staff `<select>` is populated from the open `/api/sessions`, which carries no location column, so a staffer picks a class at another building and gets the raw 403 string rendered into the roster pane (`app.js`). The boundary is correct; the affordance lies about what it will answer. Either a staff-scoped session list or a client-side narrowing that does not publish topology to members. Consumer: multi-building wellness front-desk staff. | Wellness | FE Engineer | ★ | S | 📋 ready |
| **`permissions.go` grant matrix isn't a table in 10 packages** | S2 asks for identity-domain's tabular `Grant matrix:` header; 10 packages (named in §14) document the same grants as narrative prose instead — same info, unlintable shape. Mechanical, ~10 files. Consumer: whoever next scans a package's grants expecting the canonical table. | Cross-vertical | pkg | ★ | M | 📋 ready · [§14](../../implementation-artifacts/vertical-package-standard.md) |
| **`OpMetaSpec.Sensitive` has no per-field granularity** | The flag is per-OP and a client masks every field it renders, dropping any prefilled value — so it is only honest when every rendered field is secret. `RecordIdentityPII` qualifies (its targetField is filtered out); `ClaimIdentity` does not, rendering a one-time secret beside a transcribed vertex key, so the secret is echoed in plain text rather than make the key unenterable. Consumer: `ClaimIdentity`'s claim secret, echoed today. | Cross-vertical | pkg | ★ | S | 📋 ready |
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
- 2026-07-27 · `7a52a673` · A tab's two lease relations get their two lifetimes — permanent `chargedTo` anchors settlement, transient `openFor` is retracted by `Settle`, bounding the read grant to open tabs. facet-entity-browse §8
- 2026-07-26 · `51bc4afa` · The first paint waits for a world — the boot gate releases on hydration, sticky + replayed on both hosts, not on a 3s silence a cold sign-in outlasts. facet-app-ux §10
- 2026-07-26 · `87010105` · The café charge form picks a menu item — `menuitem servedAt location` + `edgeEntityMenuItems`; the `x-entityRef` widget stops being a stub and becomes type-driven. facet-entity-browse §7
- 2026-07-26 · `6b74deaf` · The domain resolvers test the vertices they walk through — a tombstoned provider / studio / withdrawn leaseapp conferred its ex-topology; six resolvers, `vertex_live` joins the S10 pin. facet-staff-worlds §11
- 2026-07-26 · `1946ce92` · The workplace guard joins the S10 pin — nine copies were two functions, not three variants; the four `parts_of` aliases deleted; per-helper floors, `_test.go` scanned, a digest-alias rule. Standard §13
- 2026-07-26 · `618196de` · The ownership ops bind their actor — `enforce_manages` default-denies every non-operator with no live `manages` link; the exemption is the operator ROLE, not a validated target. persona-worlds W2 Inc 5
- 2026-07-26 · `c35eb3be` · One vertex-key parser — `parts_of` converges 31 copies onto one body and joins the S10 pin; the `< 3` arity laxity that truncated a four-segment aspect key into a `scopedTo` link is closed. Standard §12
- 2026-07-26 · `57e089b9` · Workplace guard covers every containment parent, and no tombstoned location — 9 copies unified across 7 packages; new S10 gate. facet-staff-worlds §10
- 2026-07-26 · `cc6a377b` · The café front desk records a payment again — `CreditCafeAccount` grants `frontOfHouse` behind a workplace guard off the account's own lease; the prefix keeps a name-matched grant off the sibling ledgers. New S9 gate
- 2026-07-26 · `0b14d0f7` · The wellness front desk books a member in again — `wellnessMembers` backs a workplace-scoped `/api/members` + the roster's book control. persona-worlds W3 Inc 4
- 2026-07-26 · `ff9c7278` · Wellness staff writes are workplace-confined — CreateStudio/CreateBooking/CancelBooking widen to frontOfHouse behind `require_workplace`; new-studio + release-seat FE restored. persona-worlds W3 Inc 3
- 2026-07-26 · `48a06798` · Staff reads are workplace-confined — café's seven read sites unify on one `cafeLeaseWorkplaces` visibility rule (own ∪ covered, operator exempt); closes the wellness+café row. facet-staff-worlds §9
- 2026-07-26 · `1532a6c5` · Wellness `coveringLocations` hop bound matched to the write side — `*0..8` reached one level deeper than `worksAt_covers`; now pinned
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
