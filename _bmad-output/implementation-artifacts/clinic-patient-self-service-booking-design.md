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
