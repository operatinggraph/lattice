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
| **Vertical Package Standard — canonicalize the reference corpus** | THE standard a vertical package builds to (S1–S8): persona-complete roles/bindings/grants + producers on both read paths, op-metas for every user-facing op, read-posture-clean scripts, tiered read boundary, verify script + structure pins. W1–W4 briefs cite it; per-package fires decide converge-vs-rewrite. | Cross-vertical | pkg | ★★★ | L | 🏗️ building · [standard §6](../../implementation-artifacts/vertical-package-standard.md) · Inc 1 shipped · next: Inc 2 ledger lens-cypher tests |
| **Clinical notes are write-only** | `RecordEncounter` PHI (`ddls.go:333-336`) captured, never projected. The cited `clinicPatientsRead` Secure-Lens precedent does NOT extend — that decrypts identity-anchored Vault ciphertext; this is raw plaintext on a non-identity vertex, and that exact shortcut was already REJECTED pre-Vault (`vault-crypto-shredding-design.md` ratification decision #2). | Clinic | pkg | ★★★ | M | 🚧 blocked-on: Vault extended to non-identity content (architectural fork, Andrew) |
| **Bound entity personas hold no `consumer`** | `ProvisionConsumerIdentity` is first-touch only, so the Gateway cannot backfill `consumer` onto a pre-existing seeded identity — only `seedTenant`/`seedLandlord` grant it. The bound provider / serviceprovider / instructor personas therefore hold their entity role alone and can reach no `consumer` scope=self grant. Needs a sweep of which self-service ops that actually closes off. Consumer: the provider + serviceprovider self-service surfaces (W1/W3; café's is deferred with its supplier hat). | Cross-vertical | pkg | ★★ | S | 📋 ready |
| **`AssignUnitOwner` binds no actor** | The op that CONFERS unit management never compares `payload.landlord` to `op.actor` and never reads `authContextTarget`, so any holder of its grant makes any identity the landlord of any unit. Operator-only today, which is the only thing containing it — an ephemeral task grant for it would buy nothing. Consumer: the landlord self-service listing chain (`app.js` post-listing flow) can never be opened to a real consumer landlord until this binds. | LoftSpace | pkg | ★★ | S | 📋 ready |
| **Café front desk cannot record a payment** | `CreditAccount` is `operator`-only in every ledger, so W4's mint deletion leaves Record Payment with no authorized hat and it is removed. A plain `frontOfHouse` grant would be unconfined credit-any-account authority in a package with no workplace binder; needs a confined ledger credit (rationale: [W4 brief §4](../../implementation-artifacts/persona-worlds-design.md)). Consumer: the café front-desk payment flow. | Café | pkg | ★★ | M | 📋 ready |
| **Staff read hats are workplace-unscoped (café + wellness)** | A `worksAt` anchor to any location anywhere grants the whole house — café's tabs/ledger/front-desk grid, and every wellness studio's roster. Both packages' staff *writes* are workplace-confined and Facet's staff *reads* grant-confined; these read paths carry no workplace term. Needs a join neither lens projects ([W4/W3 as-built](../../implementation-artifacts/persona-worlds-design.md)). Consumer: café + wellness staff reads, multi-building. | Cross-vertical | pkg | ★★ | M | 📋 ready |
| **Wellness staff cannot create a studio or cancel a member's seat** | `CreateStudio` and `CreateBooking`/`CancelBooking` scope=any are `operator`-only, so W3's mint deletion left three FE surfaces with no authorized hat and they were removed (ops + grants untouched). A plain `frontOfHouse` grant would be unconfined authority over any member's booking; the clean form mirrors `CreateSession`'s workplace walk. Consumer: the Studios tab + front-desk seat management. | Wellness | pkg | ★★ | M | 📋 ready |
| **Vertical dev-login persona fence is unwired** | The kit fences `/api/dev-login` to a persona list, but no vertical's Makefile target sets `{CAFE,CLINIC,LOFTSPACE,WELLNESS}_APP_DEMO_PERSONAS`, so `personaAllowed` admits any valid NanoID on a `make up-*` stack. Platform-wide dev posture, not one app's regression; hard fences (non-loopback bind, public-origin+signer) still hold. Consumer: any vertical exposed beyond loopback. | Cross-vertical | pkg | ★★ | S | 📋 ready |
| **Instructor + serviceprovider hats carry no ops** | No op in `packages/` declares dispatch class or targetType `instructor`/`serviceprovider` — wellness targets a `session`, service-domain a `service` instance — so those two bindings render an inert Facet hat chip while the clinic `provider` hat has three ops. Either the two domains gain record-administering ops (profile/availability, mirroring `SetProviderHours`) or the hat surface is clinic-only by design. Consumer: Kai's + Sam's instructor hats, live on the demo login. | Cross-vertical | pkg | ★★ | M | 📋 ready |
| **Entity detail attaches cross-hat ops** | `openEntityDetail` offers any op whose `dispatchTargetType` matches the row's `entityType` (`app.js`), so a multi-hat human sees another hat's op on a shared type and it fails closed in-script. The hat surface got a `dispatchClass` term; entity detail needs a provenance stamp `manifest.ent` rows don't carry. UX-only, no authorization defect. Consumer: §3.4's multi-hat human on session/appointment rows — surface grew with the §3.3 Inc 1 descriptors (8 more ops now carry a targetType). | Cross-vertical | FE Engineer | ★ | S | 📋 ready |
| **Package-standard census covers 15 of 29 packages** | §3's convergence routing was written "mechanical against the census scorecards", but the census pinned 15 packages while 29 existed at that same commit — 14 were never routed by anything, and the gate only holds debt entries for a subset of those. "The Standard is converged" can therefore read true while uncensused packages carry unmeasured S1/S3/S6 debt. Consumer: the §3.3 sweep's own completion claim. | Cross-vertical | pkg | ★★ | M | 📋 ready |
| **seed-showcase partial-failure recovery + ctx-window gaps** | Death between `CreateUnclaimedIdentity` and the bind wedges the seed (re-mint hits the email-index RevisionConflict, no orphan-adopt path); main's 60s ctx can't reach the 4-min projection-lag window on a cold stack (raw deadline mid-sequence); `findLinkedIdentity`/`findBoundIdentity` fail-open on transient KV error + trust the first `identifiedBy` link; tenant-recovery skips 3 personas stderr-silent. Recoverable by a wipe; no runtime-feature impact. | Cross-vertical | pkg | ★★ | M | 📋 ready |

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

- 2026-07-25 · `2507abb3` · Self-service ops are self-describing — 7 full descriptors + loftspace-domain's first op-metas; DecideLeaseApplication names the self path. Standard §3.3 Inc 1; closes the authContext row
- 2026-07-25 · `—` · Persona worlds CLOSED — P1/P2 + W0–W5 + W3 Inc 2 all shipped; the named residuals live on as their own rows
- 2026-07-25 · `5397dd2e` · Wellness attendance — `SetBookingAttendance` marks attended/noShow behind the two-hop instructor guard, merging seat/booker so a marked booking still cancels. persona-worlds W3 Inc 2
- 2026-07-25 · `e5b5c375` · Facet hats + landing — a bound provider's binding is a third anchor spine rendering as "What I provide", and its detail surfaces the record-administering ops. persona-worlds W5
- 2026-07-25 · `d3a7cc7b` · Wellness is sign-in-first — both mints + the fixed admin actor deleted, the `?bookerKey=` identity filter closed, per-user reads session-scoped; member/staff/instructor hats. persona-worlds W3 Inc 1
- 2026-07-25 · `f5a9c8a3` · Café is sign-in-first — session spine, both mints + the fixed admin actor deleted, all eight reads authenticated and scoped to the signed-in subject; Record Payment removed (no authorized hat). persona-worlds W4
- 2026-07-25 · `4aad350b` · The plain-landlord persona exists — a seeded identity holding `consumer` + `manages` over its own unit, listing and signed application; the scope=self path Inc 3 shipped now has a walker. persona-worlds W2 Inc 4
- 2026-07-25 · `4790992b` · LoftSpace landlord hat can write — `consumer` scope=self grants + a `manages` ownership probe on all five landlord ops; fixes first-match permission shadowing + Loupe role resolution. persona-worlds W2 Inc 3
- 2026-07-24 · `13c01922` · LoftSpace is sign-in-first — session spine, both dev-token mints deleted, `adminActor` retired; whoami `manages` anchor gates the landlord hat; server-side credential-link ceremony. persona-worlds W2 Inc 2
- 2026-07-24 · `02be1f86` · LoftSpace applicant self-service grants — `SetApplicantProfile` + `WithdrawLeaseApplication` get `consumer` scope=self grants + owner guards; FE submits per-actor. persona-worlds W2 loftspace Inc 1
- 2026-07-24 · `a36625a3` · Clinic provider hat — a bound provider sees a read-only **My Schedule** (own day) via RLS-scoped `/api/my-schedule`, gated on the `identifiedBy`-provider anchor; closes that endpoint's zero-caller residual. W1 Inc 2
- 2026-07-24 · `2dbc8232` · `identityAnchors` projects the `identifiedBy` binding anchor — whoami `anchors[]` now carries a provider's binding (persona-worlds §4.1); a provider had no anchors before; identity-domain 0.7.0. W1 Inc 2c
- 2026-07-24 · `a934fd8b` · Clinic hat-gating — whoami forwards `/v1/actor` roles+anchors; FE gates the 5 staff tabs + New-patient on the `worksAt` anchor (patient sees Book + My Appointments only); live-verified. W1 Inc 2b
- 2026-07-24 · `4fe21968` · Clinic FE session-resilience — activity-gated keepalive (no TTL hard-lapse mid-read), 5 lapse-blind reads → `appGet` (slot picker stops offering taken times), whoami retried; persona-worlds W1 Inc 2b FE tail
- 2026-07-24 · `6392ea7f` · IdP session-boundary hardened (kit) — `_JWT_AUDIENCE` trimmed + `parsePublicKeyPEM` refuses non-RSA/ECDSA keys at startup; two silent-misconfig boots now fail closed (persona-worlds W1 Inc 2 tail; discriminating tests)
- 2026-07-24 · `82b9eaec` · Clinic front-desk (`frontOfHouse`) can run the Follow-ups tab — Start/Pause/ResumeVisitSeries grant frontOfHouse + workplace confinement (mirror CreateAppointment), operator-exempt-only guard; clinic-reminders 0.6.0
- 2026-07-24 · `50ff65dd` · Wellness `CreateBooking` rejects double-books (per-(session,booker) `sessionBookerClaim` guard, café-idiom, released on cancel) + past-start sessions (`SessionInPast`); wellness-domain 0.10.0
- 2026-07-24 · `6b1c667c` · Patient names out of open clinic nats-kv lenses — `clinicAppointments`/`clinicPatients` are key-only; names stay Protected (`clinicPatientsRead` RLS); provider directory stays public
- 2026-07-23 · `1e8dc41b` · Clinic front-desk (`frontOfHouse`) can book + register again — grants audit + workplace confinement; closes a forgeable-`authContext.target` bypass (persona-worlds W1 Inc 2a)
- 2026-07-24 · `283dd1a9` · Clinic is sign-in-first — session-keyed reads/writes, both dev-token mints deleted, appsession production branch, patient identity read-grant bridge (persona-worlds W1 Inc 1+1b)
- 2026-07-23 · `626763bc` · Persona-worlds W0 — provider archetype spine (role + per-domain bindings + operator-only Bind* + provider grants/guards + clinic GrantTable + 3 edge-manifest hat lenses + seeds); live-verified
- 2026-07-23 · `8c246540` · Clinic booking date/time field now snaps to the 15-minute grid — off-grid `min` was rejecting legal grid times and suggesting off-grid ones; Andrew-reported, live-verified (change-time snap + submit-time backstop)
- 2026-07-24 · `c4b45b33` · Ownership guards answer before payload-matching probes — 6 sites (lease-signing, clinic ×2, wellness ×2, service-domain); `ClaimTask` exempt: no caller-supplied candidate; testutil can now assert WHICH check answered
- 2026-07-24 · `5556e753` · Maintenance `ResolveWorkOrder` authorizes before its terminal `.resolution` read — closes the resolved-vs-open oracle for a caller outside the building; wellness bind-grant comment truthed up
- 2026-07-23 · `29b653c8` · Clinic `StartVisitSeries` rejects a duplicate active series — per-patient+provider guard aspect, live-verified (accept/reject/pause-revival/expiry-revival)
- 2026-07-22 · `239e3846` · Clinic staff site-management list shows "(unnamed site)" instead of a bare trailing separator for a provider assigned to a building whose `SetSiteProfile` never ran
- 2026-07-22 · `—` · Facet for staff — front-desk/operations worlds CLOSED — F1–F5 all shipped; F5's `e269c27d` + the live-proven claim beat were the last pieces, board row was stale
- 2026-07-22 · `78927466` · Facet claim button now reads/submits the Core-KV vertex key (`data.taskKey`), not the manifest row's own storage key — was failing every claim closed with HydrationMiss; live-proven severed-network claim beat
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
