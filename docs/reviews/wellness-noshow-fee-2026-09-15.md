# Wellness — "The desk's No-show click bills an undisclosed flat $25 on top of the class price" (2026-09-15)

**Filed (PO, 2026-09-13):** the roster button says only "No-show" and sends no fee, so `SetBookingAttendance`'s
2500 default applies: a $0 resident class no-show costs $25, a $15 guest $40, and the studio has no fee policy
(`studioProfile` = `{name}`). A studio-level `noShowFeeCents`, the button states the amount, the desk may mark
fee-free. Winston-adjudicated (implementation-level; no contract surface, no fork).

## Grounding

- **The fee is a script constant nobody sees.** `SetBookingAttendance` reads `noShowFeeCents` from the payload and
  falls back to `2500` ([ddls.go:5292-5299](../../packages/wellness-domain/ddls.go)); `merged["noShowFeeCents"]`
  is what `wellnessNoShowSettlement` bills ([wellness-ledger/lenses.go](../../packages/wellness-ledger/lenses.go)).
  The roster's `attendanceActions` renders `No-show` with no amount ([app.js:2949-2954](../../cmd/wellness-app/web/app.js))
  and `markAttendance` sends `{bookingKey, session, status}` ([app.js:2228-2259](../../cmd/wellness-app/web/app.js));
  the Facet descriptor exposes no fee field and its `status` enum label is a bare "No-show"
  ([opmetas.go:440-460](../../packages/wellness-domain/opmetas.go)). The class price is owed regardless of
  attendance (`wellnessClassPriceSettlement`), so the fee is a PENALTY on top — that is the design, not the harm;
  the harm is that its amount is undisclosed and un-settable.
- **The auto-closer already bills nothing.** `pastDueBookings`' playbook dispatches `SetBookingAttendance` with
  `noShowFeeCents: "json:0"` — "a documentation lapse, not a staff-observed missed visit"
  ([pastdue.go:140-160](../../packages/wellness-reminders/pastdue.go)). The explicit-zero path is therefore
  already the "fee-free" verb, and the desk simply never had a button for it.
- **The studio is one bounded hop from the session, and the op already walks it.** `session_studio(session)` /
  `session_locations` ([ddls.go:4585](../../packages/wellness-domain/ddls.go)) — the front-of-house confinement
  walk `SetBookingAttendance` runs on the staff leg. `studioProfile` is `{name}`, written by `CreateStudio` only
  ([ddls.go:159-182](../../packages/wellness-domain/ddls.go)); there is no studio-profile setter (`SetInstructorProfile`
  is the profile-setter precedent, [ddls.go:1119-1150](../../packages/wellness-domain/ddls.go), [opmetas.go:873](../../packages/wellness-domain/opmetas.go)).
  `wellnessStudios` projects `{studioKey, name}` ([lenses.go:201-210](../../packages/wellness-domain/lenses.go));
  `wellnessSessions` projects `studioKey` (the roster's join key).
- **Which studio?** `atStudio` is repointed by `ReassignSession`, so the walk pages with the first-live cursor loop
  the dossier prescribes; the op reads the studio the session is at NOW — a class moved to another studio is
  billed by that studio's policy, the same rule its confinement already applies.
- **`scripts/verify-package-wellness-domain.go`** pins the `studio` vertex type + `studioProfile` aspect
  `PermittedCommands` ([verify-package-wellness-domain.go:205, 210](../../scripts/verify-package-wellness-domain.go))
  — a new op reddens `main` from a green local run unless added there.

## Verdict — *the fee is the studio's recorded policy, the button says the amount, and "no fee" is a button*

1. **`studioProfile.noShowFeeCents`** — optional non-negative integer cents. Written by `CreateStudio` (new optional
   param) and by **`SetStudioProfile{studioKey, name?, noShowFeeCents?}`** (new op; a merge-update, amended at build — the
   descriptor vocabulary cannot pre-fill an editable field; granted operator + frontOfHouse confined by the studio's `locatedAt`, the confinement `TombstoneStudio`
   shipped at `56deea3e`). A profile with no `noShowFeeCents` means "no policy recorded".
2. **`SetBookingAttendance` resolves the fee from the studio.** With `noShowFeeCents` absent from the payload and
   `status = noShow`: `session_studio(session)` → `(e)` read `.studioProfile` → `noShowFeeCents` if present, else
   the documented `2500` default (a studio that never set a policy behaves as today, now disclosed). An explicit
   payload `noShowFeeCents` still wins (0 = fee-free; the Weaver sweep's `json:0` is unchanged). The DDL text, the
   descriptor's `status` enum label ("No-show — bills the studio's no-show fee") and `pastdue.go`'s description
   ("bills the default $25") say the rule.
3. **The lens says the amount.** `wellnessStudios` projects `noShowFeeCents`; `/api/studios` carries it.
4. **The roster button states it, and offers the waiver.** `attendanceActions(b, studio)` renders `No-show ($25)`
   (or `No-show (no fee)` at 0 / no-policy-but-default → `No-show ($25)`) and, when the resolved fee is > 0, a second
   `No-show, waive fee` button that sends `noShowFeeCents: 0`; the studio comes from the roster session's
   `studioKey` joined to `/api/studios`. Goja-pinned per fee state.
5. **The studio panel sets it.** The create form gains a "No-show fee" field; the studio card shows the policy and
   an edit form dispatching `SetStudioProfile` (the same confinement courtesy as Retire).
6. wellness-domain version bump; `verify-package-wellness-domain.go`'s `ddlCheck` gains `SetStudioProfile`.

### Fire brief (build note, 2026-09-15)

1. **Scope** (board row, verbatim): *The roster button says only "No-show" and sends no fee, so `SetBookingAttendance`'s
   2500 default applies: a $0 resident class no-show costs $25, a $15 guest $40, and the studio has no fee policy
   (`studioProfile` = `{name}`). A studio-level `noShowFeeCents`, the button states the amount, the desk may mark
   fee-free.* Green bar: a studio with `noShowFeeCents = 1000` bills $10 on a payload-less no-show mark; one with
   `0` bills nothing; one with no policy bills $25; the desk's button names the amount and the waiver button bills
   nothing; `SetStudioProfile` is refused to a staffer at another building.
2. **Touch-list (verified live at fire start; re-verify after the arrears fire's merge shifts `ddls.go`):**
   wellness-domain `ddls.go:100-182` (studio vertex + profile DDLs), `:4585` (`session_studio`), `:5148-5378`
   (`SetBookingAttendance`; the fee at `:5292-5299`), the `SetInstructorProfile` branch (the setter precedent) ·
   `opmetas.go:440-499` (`SetBookingAttendance` descriptor), `:751-797` (`CreateStudio`), `:873-929`
   (`SetInstructorProfile` → mirror for `SetStudioProfile`) · `permissions.go` (`TombstoneStudio`'s frontOfHouse
   confinement grant) · `package.go` + `manifest.yaml` · `tombstone_studio_test.go` (the confinement vector shape) ·
   `refund_marker_test.go:968-1062` (the fee vectors) · `scripts/verify-package-wellness-domain.go:205, 210` ·
   wellness-reminders `pastdue.go:144-148` (description text only, no version bump unless content changes —
   a description IS content: bump) · wellness-app `studios.go:11-15, 48-66` · `app.js:2225-2259`, `:2949-2954`,
   `:3301-3366` · `web_attendance_gate_test.go`, `web_retire_studio_test.go`.
3. **Precedents:** `SetInstructorProfile` (op + descriptor + `Reads`/`OptionalReads` posture); `TombstoneStudio`'s
   frontOfHouse confinement + `web_retire_studio_test.go`; `session_studio` for the walk; the `promotedBadge` goja
   pin shape.
4. **Increments:** (1) package — aspect field + `CreateStudio` param + `SetStudioProfile` + the fee resolution in
   `SetBookingAttendance` + descriptor/DDL text + `wellnessStudios.noShowFeeCents` + version + verify script;
   tests: policy 1000 → 1000, policy 0 → no fee, no policy → 2500, explicit 0 overrides a policy, `SetStudioProfile`
   as frontOfHouse at the studio's building accepted / at another refused / operator accepted, negative refused,
   `CreateStudio` with the fee; lens pin. Green: `go test ./packages/wellness-domain/ ./packages/wellness-reminders/
   -count=1`, refractor corpus census. (2) app — `/api/studios` field, `attendanceActions(b, studio)` + the waiver
   button + `markAttendance(…, feeCents)`, studio create/edit forms; goja pins. Green: `go test ./cmd/wellness-app/
   -count=1`, all `scripts/lint-*.go`, `golangci-lint`, `make vet`. (3) live: refresh wellness-domain (+ reminders),
   cycle `bin/wellness-app`, set the live studio's fee, mark a no-show, read the ledger.
5. **Gotchas:** `lint-app-op-descriptors` (a new op the app dispatches needs its descriptor); `lint-refusal-courtesy`
   (`SetStudioProfile`'s state refusals at the app site + Facet); `lint-seed-declared-reads` for any seed naming
   `CreateStudio`; `lint-links-page-limit` on the studio walk; `verify-package-wellness-domain.go`'s counts; the
   `_packages.md` dossier's *a field a self-scoped op stores as informational becomes load-bearing the moment another
   op derives from it — validate at the mint* (`noShowFeeCents` is validated non-negative integer at BOTH writers,
   the reader keeps a named refusal for a malformed stored value) and *every writer of the guarded VALUE* (the fee's
   writers: the desk click, Facet's form, the Weaver sweep — all three resolve through the one script branch);
   standing checklist #3 (revert-prove each fee branch) · #6 (the 2500 literal stays only as the no-policy default,
   named once).
6. **Adjacent finds:** none.
7. **Non-goals:** capping an explicit payload fee above the policy (staff-trusted today; not the filed harm); a
   per-class fee; Facet showing the studio amount (no session-row column carries it; the enum label names the rule).

### Build note (2026-09-15)

Shipped `0b256550` (CI green); brief `9c6c67a5`. Live on the shared stack (wellness-domain 0.27.13 + wellness-reminders
0.3.7 diff-applied, `bin/wellness-app` cycled): `SetStudioProfile{noShowFeeCents: 1000}` on Riverside Movement Studio
committed as the operator and `/api/studios` reads `noShowFeeCents: 1000` off `wellnessStudios`. The fee resolution
itself (policy / zero / none → 2500 / explicit override / malformed refused, across a moved class) is proven by the
package vectors and the reviewer's mutants rather than by billing a live member — a no-show mark needs a started
class, which cannot be called off afterwards, and the demo stack keeps no probe litter.

Deviations from the brief: `SetStudioProfile` is a merge-update, not `SetInstructorProfile`'s wholesale replacement
(the descriptor vocabulary cannot pre-fill an editable field, so a fee-only form would re-type or blank the name);
the aspect's local name is `.profile` (class `studioProfile`); `lint-app-op-descriptors`' wellness ceiling 15 → 16
with the reason recorded beside it. Review classification (one cold pass over the package diff, a lead pass over the
app diff): **test-gap** — `session_studio`'s isDeleted-skip was unpinned by the whole package (the move vector had the
tombstone sorting first, so last-wins still landed live); fixed with the reverse-ordered vector. **implementation-bug**
— the absent-profile refusal's text and its Facet courtesy described an inverted mechanism (an undeclared read finds
the profile; a declared read of an absent key faults `HydrationMiss` before the branch); fixed. **convention** — two
narrating comments; fixed. Two nits (a non-string `name` silently dropped; a rename carrying a malformed stored policy)
fixed. Adjacent finds: none.
