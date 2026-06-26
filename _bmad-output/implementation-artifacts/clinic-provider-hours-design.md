# Clinic appointment conflict — Increment 2b: provider availability windows

**Status:** ✅ Winston-ratified — build-ready (no frozen-contract touch; Capability-KV §06 explicitly
sanctions "the operation's own Starlark logic" for temporal availability — the same path Increments 1 / 2a took).
**Owner:** clinic-domain package + a pure core-Processor Starlark builtin. **Size:** M.
**Ref board row:** "Appointment scheduling — conflict + temporal availability" (★★★, Clinic) — the remaining
Increment 2b. Follows Increment 1 (CreateAppointment double-book) + 2a (reschedule-into-conflict).

## Problem

A provider has business hours (e.g. Mon/Wed/Fri 09:00–17:00 UTC). Today `CreateAppointment` /
`RescheduleAppointment` accept any instant — a booking at 03:00 Sunday is admitted. §06 (FROZEN, L354–359)
defers *availability windows* to "a Phase 2 mechanism **or the operation's own Starlark logic**." We take the
sanctioned second path again — no contract amendment.

## The enabler (why 2b needed a design fire, not a clean build)

A window-membership test on an appointment instant needs **day-of-week** and **time-of-day** — but the
Processor Starlark `time` module exposed ONLY `rfc3339_utc` / `rfc3339_add` (no weekday / hour extractor;
verified `internal/processor/starlark_builtins.go`). Adding a pure, clock-free time-introspection builtin is a
**Winston implementation call** (no trust surface, no frozen contract), so we add two:

- `time.weekday(s)` → int **0..6** (Sunday=0 … Saturday=6, Go's `time.Weekday`), the UTC weekday of `s`.
- `time.seconds_of_day(s)` → int **0..86399**, the UTC seconds-since-midnight of `s` (`h*3600 + m*60 + sec`).

Both are **pure** (deterministic, no I/O, no wall-clock read — the same sandbox-safe pattern as
`rfc3339_utc` / `crypto.sha256`): the output is a function of the input string only. Determinism preserves the
at-least-once replay invariant (Contract #3 §3.6). A malformed input raises a Starlark error surfaced as a
structured ScriptError. **Integers, not "HH:MM" strings** — integer comparison is exact and avoids the
mixed-width lexical-prefix hazard ("17:00" vs "17:00:00" mis-orders at a boundary).

## Data model

A new **opt-in `.hours` aspect** on the provider vertex (class `providerHours`):

```
vtx.provider.<id>.hours = { "windows": [ {"day": 1, "openSec": 32400, "closeSec": 61200}, ... ] }
```

- `day` ∈ 0..6 (Sun=0). `openSec` / `closeSec` are UTC seconds-of-day, `0 <= openSec < closeSec <= 86400`.
- **Opt-in / backward-compatible:** a provider with NO `.hours` aspect (or `windows = []`) is **unconstrained**
  (every existing provider + every current test keeps passing — no `CreateProvider` change). This is read on
  demand via `kv.Read` (§2.5), NOT a declared/OCC `contextHint.reads` key: hours are config, not a concurrency
  serialization point (the `.bookings` index remains the only OCC anchor), and a hours-edit racing a booking is
  benign.

New aspect-type DDL `providerHours` (declaration-only step-6 write gate, `PermittedCommands: [SetProviderHours]`,
NON-sensitive). The op script lives on the **provider** vertexType DDL (mirrors the `.bookings` split).

## SetProviderHours op (provider DDL)

`SetProviderHours { providerKey, windows: [{day, openSec, closeSec}] }`:

1. Validate `providerKey` alive + class=provider.
2. Validate each window: `day` int 0..6; `openSec` / `closeSec` ints; `0 <= openSec < closeSec <= 86400`.
3. Unconditioned upsert of `.hours = {windows}` (re-runnable; `windows: []` clears the constraint).

## Enforcement (CreateAppointment + RescheduleAppointment)

After the existing double-book block, before minting / rewriting the schedule:

1. `hours = kv.Read(provider + ".hours")`. If `None` / `isDeleted` / no windows → **unconstrained**, skip.
2. `sw = time.weekday(starts_at)`, `ew = time.weekday(ends_at)`, `ss = time.seconds_of_day(starts_at)`,
   `es = time.seconds_of_day(ends_at)`.
3. If `sw != ew` → `OutsideHours` (an appointment crossing UTC midnight can't sit inside one window).
4. If NO window `w` has `w.day == sw AND w.openSec <= ss AND es <= w.closeSec` → `OutsideHours`.

CreateAppointment has the validated `provider` endpoint; RescheduleAppointment already requires + link-validates
the provider (for the 2a conflict check) — the same `provider` variable is reused. The hours read is a single
on-demand `kv.Read` of a known key (no scan; `TestPackage_NoScans` stays satisfied).

## Surfaces touched

- `internal/processor/starlark_builtins.go` — add `time.weekday` + `time.seconds_of_day` to `timeModule()`.
- `internal/processor/starlark_builtins_test.go` — unit-test both builtins (values, malformed, arity, determinism).
- `packages/clinic-domain/ddls.go` — new `providerHours` aspect-type DDL; `SetProviderHours` on the provider DDL
  PermittedCommands + handler; hours-enforcement in CreateAppointment + RescheduleAppointment.
- `packages/clinic-domain/{permissions.go,manifest.yaml,package.go}` — register `SetProviderHours` + the DDL.
- `scripts/verify-package-clinic-domain.go` — assert the new DDL + permission + PermittedCommand.
- `cmd/clinic-app/web/` — a "Manage availability" hours editor (day + open/close → seconds) + the existing
  op-error toast surfaces `OutsideHours`.
- Tests — Processor-driven `integration_test.go` (in-hours allowed, out-of-hours rejected on create + reschedule,
  unconstrained when no hours, wrong-day rejected, boundary inclusive, SetProviderHours validation), `package_test.go`.

## Non-goals / deferred

- Available-**slot** picking (the FE proposing free slots) — a later FE increment over this enforcement.
- Recurring `@every` provider schedules — the separate §10.4-amendment item (Andrew gate).
- Per-window timezone (hours are UTC; the clinic is single-TZ under the trusted-tool posture).
- Patient-side availability — out of scope (lower value).
