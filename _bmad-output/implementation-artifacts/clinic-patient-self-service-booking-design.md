# Clinic patient self-service booking

**Status:** ✅ Winston-ratified — build-ready. Pure implementation decision (mirrors an
already-proven platform pattern), no frozen-contract change, no architectural fork —
decided per CLAUDE.md / steward §0 and built this fire.

## Scope of this increment (Fire 1 — capability-plane grant)

`verticals.md`'s "No patient self-service booking" row asks for a real patient-authenticated
write path: today every `CreateAppointment` is staff-initiated via the front-desk picker. This
increment ships the **write-authorization half only**: `CreateAppointment` now grants
`consumer`, scope=self, so a real patient can book their own appointment through the Gateway.
The self-booking **FE** (`cmd/clinic-app`) is deferred to a follow-up fire — see Checkpoint.

## Ground: mirrors lease-signing's `CreateLeaseApplication` consumer scope=self grant (shipped `921fda4`)

The real-actor-write-auth-e2e platform mechanism (Contract #6: step 3 authorizes `scope=self`
by checking `authContext.target == actor`) is already fully proven — see
`real-actor-write-auth-e2e-design.md` and `packages/lease-signing/permissions.go` /
`scripts.go`. This increment applies that same proven mechanism to one more package
("wear the other hat" / small-extension test, steward §2) — it is **not** a new platform
primitive.

**One real difference from lease-signing, resolved:** lease-signing's applicant IS a
`vtx.identity` — the op's endpoint and the capability-plane actor are the same vertex, so its
guard is a direct string compare (`authContextTarget == payload.applicant`). Clinic's booking
endpoint is `payload.patient`, a **`vtx.patient`**, a different class than `vtx.identity` — and
the write-path capability plane is hard-anchored to identity actors
(`internal/processor/step3_auth_capability.go`'s `capabilityKeyFromActor` converts
`vtx.identity.<NanoID>` → `cap.identity.<NanoID>`; no other vertex class ever resolves a
capability doc). So `authContext.target` is necessarily the caller's own **identity** key, and
the script closes the patient↔identity gap by requiring the target identity to be the *named
patient's* linked identity — read via `kv.Read("lnk.patient.<id>.identifiedBy.identity.<id>")`,
mirroring `wellness-domain`'s `CreateBooking` residency-check idiom (a known-key relation read,
not a declared read — see the `read-posture: (e)` annotation at the call site,
`packages/clinic-domain/ddls.go`).

**Consequence, stated plainly (not a gap):** a patient with no linked identity (the common
shape today — `CreatePatient`'s `identityKey` is optional) can never receive this grant, because
there is no identity vertex for them to authenticate as. Self-booking is only reachable for a
patient who has been identity-linked (the same precondition `clinicPatientsRead`'s Secure-Lens
contact display already requires).

## Shape

- **`packages/clinic-domain/permissions.go`** — `CreateAppointment` now carries two permission
  entries: the existing `scope=any → operator` (staff/front-desk, unconstrained), plus a new
  `scope=self → consumer` (a patient booking themselves).
- **`packages/clinic-domain/ddls.go`**, `CreateAppointment` script — after the existing
  patient/provider liveness checks, when `op.authContextTarget != ""` (empty for the standing
  operator grant, a no-op there — operator keeps booking on behalf of any patient exactly as
  before), the script requires `lnk.patient.<patientId>.identifiedBy.identity.<targetIdentityId>`
  to be live. Absent or mismatched → `AuthDenied`.
- **Tests** (`packages/clinic-domain/integration_test.go`):
  `TestClinic_CreateAppointmentConsumerSelfScope_Allowed` (a patient linked to the caller's own
  identity books themselves → accepted) and
  `TestClinic_CreateAppointmentConsumerNamesUnlinkedPatient_Rejected` (step 3's `target == actor`
  is satisfied, but the named patient isn't linked to that identity → rejected by the script
  guard, not step 3 — the same two-test shape as lease-signing's
  `TestCreateLeaseApplication_ConsumerSelfScope_Allowed` /
  `_ConsumerNamesDifferentApplicant_Rejected`).
- **Test-harness role wiring**: none of `clinic-domain`/`clinic-reminders`/`clinic-ledger`'s test
  suites install `identity-domain`, so each needed a stand-in `consumer` role NanoID registered
  directly in its `Installer.RoleIDs` (the `lsConsumerRoleID` idiom lease-signing's own tests
  already use) — otherwise package install rejects the new `GrantsTo: ["consumer"]` entry as an
  unresolvable role.

## Read-posture worked example (the correct fix, not just an annotation)

The `identifiedBy`-link guard's `kv.Read` was first left as an unclassified lazy read, mirroring
`wellness-domain`'s (also-unclassified) residency-check idiom — the lint tool flagged it as
"class-(b) debt" (Contract #2 §2.5). The actual fix is **not** to annotate a lazy read as
deliberate — it's to make it a **declared, absence-tolerant read**: the self-service caller
already knows both `payload.patient` and its own `authContext.target` before submitting, so it
can compute `lnk.patient.<id>.identifiedBy.identity.<id>` client-side and list it in
`ContextHint.OptionalReads`. `kv.Read` (`internal/processor/starlark_kv.go`) transparently serves
a declared key from the step-4 hydrated snapshot — present or known-absent — with **zero script
change**; only the caller's envelope changes. This is class (d), matching
`orchestration-base/ddls.go`'s engine-dispatcher availability-gate precedent exactly. A caller
that omits the declaration still gets a correct answer (`kv.Read` falls through to a live
on-demand read) — declaring it only buys OCC-snapshot consistency, proven by
`TestClinic_CreateAppointmentConsumerSelfScope_AllowedWithoutDeclaredRead`. The remaining
unclassified `kv.Read` debt elsewhere (board row: "Read-posture debt sweep") should get this same
treatment wherever the caller can predeclare the key — a `(c)`/`(e)` annotation is the fallback
for a read that is genuinely, structurally live (config, or a bounded enumeration), not a
substitute for declaring a predictable key.

## Checkpoint — next fire

- **FE**: `cmd/clinic-app` has no patient-authenticated *write* path today (only the read-side
  JWT for My Appointments/My Schedule, `readauth.go`). Wiring a real self-booking UI needs: (a) a
  capability-mode JWT/actor binding for the patient's own identity on the write path (distinct
  from the existing Postgres-RLS read JWT), (b) a booking form reading `clinicProviders` +
  `clinicAppointmentsRead` (already-live P5 lenses) to pick a provider/slot, (c) submitting
  `CreateAppointment` with `authContext.target` set to the patient's own identity key. This is
  genuinely new FE + Gateway-wiring work (no vertical app has ever exercised a real consumer
  write path end-to-end yet, per `real-actor-write-auth-e2e-design.md`'s own Phase 1 scope) —
  size it as its own fire via `fe-engineer` + UX (Sally), not folded into this one.
- Row stays `verticals.md` (not filed to `lattice.md`) — no new platform primitive was needed;
  the write-authorization half is genuinely done, the FE half is the named remaining consumer.

## Fire — patient can't read their own encounter note (2026-08-28)

**Scope sentence:** widen `clinicEncountersReadSpec`'s `authz_anchors` to admit the patient
alongside the treating provider, then drop the FE's own `asSelf` suppression, so
`GET /api/my-encounters` returns — and the patient card renders — the same clinical note the
provider already sees for that appointment. No write-side change, no new link, no new platform
primitive.

**Ground (verified live in this repo, not assumed):**
- `packages/clinic-domain/lenses.go:1003-1017` (`clinicEncountersReadSpec`) is PROVIDER-only
  today: `authz_anchors = [nanoIdFromKey(pr.key)]`. `forPatient` is already an `OPTIONAL MATCH`
  (line 1006) projecting `patient_key` for display, never as an anchor.
- **The precedent to mirror is in the SAME file, not loftspace's**: `clinicAppointmentsReadSpec`
  (lines 917-947, D1.5, already shipped and live for "My Appointments") anchors on
  `nanoIdFromKey(p.key)` — the **patient vertex's own bare NanoID**, not a walk to its
  `identifiedBy` identity. RLS's `lattice.actor_id` for a patient self-service session therefore
  matches the `patient` vertex key directly; confirmed by `queryMyEncounters`
  (`cmd/clinic-app/encounters.go:66-101`) reusing the identical `set_config`/RLS shape
  `queryMyProviderSchedule`/`queryMySchedule` already use successfully for the patient's own
  appointments today. No `identifiedBy` walk needed in the anchor arm.
- **Comprehension, not a bare element** — `clinicPatientsSpec`'s `buildingAnchors` comment
  (lines 910-916) is load-bearing here too: `p` is bound by an `OPTIONAL MATCH`, so a bare
  `[nanoIdFromKey(p.key)]` would carry a `NULL` array element (rejected by
  `ProtectedAdapter.toStringSlice`, failing the WHOLE row's upsert) whenever an appointment has
  no `forPatient` link. The fix uses a fresh pattern comprehension
  `[(a)-[:forPatient]->(p2:patient) | nanoIdFromKey(p2.key)]`, which degrades to `[]`, not
  `[null]`, exactly the rule `clinicAppointmentsReadSpec`'s own comment documents.
- **Decrypt-side needs no separate change** — `internal/refractor/pipeline/secure.go:208-225` and
  `docs/contracts/03-mutation-batch-event-list.md` §3.10 confirm custody ("can this be
  decrypted") is keyed off the ciphertext's own `keyId` → `HolderTypes` (`["retentionclass"]`,
  unchanged), independent of which actor's NanoID is in `authz_anchors`. Widening the anchor
  widens who can see + decrypt the row in one change; `applicantRosterRead`
  (`packages/loftspace-domain/lenses.go:81-82,257-258`) is the existing precedent for a
  multi-arm Secure Lens working this way.
- FE gate: `cmd/clinic-app/web/app.js:3931` — `a.documentedAt && !opts.asSelf` — the ONLY thing
  suppressing display once the backend row exists; `opts.asSelf` cards call the same
  `state.myEncounters` map providers already read (populated by `loadMyEncounters`,
  `app.js:2607-2637`), so no new fetch or state is needed.

**Touch-list:**
1. `packages/clinic-domain/lenses.go` — widen `authz_anchors` (comprehension form above); rewrite
   the two stale comment blocks that assert provider-only anchoring (lines 359-395 lens
   declaration doc, lines 986-990 spec doc — CLAUDE.md: comments describe the code as it is now).
2. `packages/clinic-domain/manifest.yaml` + `packages/clinic-domain/package.go` — version bump
   (content-changing package edit; CLAUDE.md package-version-bump rule).
3. `cmd/clinic-app/web/app.js:3931` (+ the comment above it, lines 3920-3929) — drop
   `&& !opts.asSelf` so the patient's own card renders the note through the same path the
   provider's card already uses.

**Non-goals:** no `identifiedBy` walk (ground above shows it's unnecessary and would be wrong —
`p.key`, not the identity vertex, is what RLS matches); no change to `SecureColumns`/HolderTypes;
no FE data-fetch change (`loadMyEncounters` already runs unconditionally); front-desk gets no new
anchor (workplace token deliberately absent from this lens, per the existing lens-declaration
comment, and stays that way — the clinical note is not front-desk material).

**Increment order:** one increment — package lens + version bump, then FE gate removal; run
`go test ./packages/clinic-domain/...` and `node --check cmd/clinic-app/web/app.js`, `go build
./...`, `make vet`, `golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`,
`DIFF_BASE=<base-sha> go run ./scripts/lint-package-version.go`.
