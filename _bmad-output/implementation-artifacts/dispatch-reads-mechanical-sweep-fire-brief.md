# Fire brief — Dispatch.Reads mechanical sweep (25 ops, 6 packages)

**Board row:** `verticals.md` — "SetAppointmentStatus's descriptor declares a surface too narrow to submit
the op" (Cross-vertical, pkg, ★★, S).
**Steward:** Vertical Steward, unattended fire, 2026-08-27.
**Grounding is already done** — this brief points at it rather than repeating it:
[`docs/reviews/verticals-designer-triage-2026-08-27.md` §8](../../../docs/reviews/verticals-designer-triage-2026-08-27.md)
(section "Dispatch reads (was: no-pattern: link-key reads template)"). Read that section first; it is the
primary spec. This brief only restates the work-list and adds gate/test instructions.

## What §8 already established (do not re-litigate)

- The `no-pattern:` tag on the old row was **falsified** — the link-key reads template, including
  mid-template `:id` splicing, is an **established, shipped, five-package pattern**
  (`internal/processor/descriptor_floor.go:785-868`; precedents: clinic `RemoveProviderSite`, wellness
  `TombstoneSession`, orchestration-base `ClaimTask`, service-domain ×2, lease-signing). **No new engine
  primitive is needed.**
- Re-run census: **25 Dispatch ops across 6 packages lack `Dispatch.Reads`**:
  - **18 are class A** (target+actor only — functionally covered today by `form.mjs`'s auto-push fallback;
    declaring them is §2.5 hygiene, not a behavior change).
  - **7 are class B** (need a declaration the existing grammar already expresses — no new template shape).
  - **0 are class D** (nobody needs the type-agnostic engine segment some earlier framing wanted).
- **`SetAppointmentStatus`'s real break is its `InputSchema`, not its reads**: the `status` enum is already
  `["cancelled"]` (terminal-only), but `provider`/`patient` — which every terminal transition requires
  (`ddls.go:2956-2963`) — are missing from the schema, so a descriptor-driven client can't render/submit
  them. Fix: declare `provider`/`patient` **required** in the InputSchema (honest for the described,
  terminal-only surface) + declare its four `Dispatch.Reads` (`RescheduleAppointment`, same package, has the
  **identical four-read set** with a correct schema already — copy its shape exactly).
- `CreateBooking`'s missing `.schedule` read is class B (declare it); its computed optional-reads tail is a
  **separate**, already-filed item (`derive_reads`) — **out of scope for this fire, don't touch it.**

## Increment order

1. **Re-derive the exact 25-op / 6-package list live** (the triage's own methodology — grep every
   `pkgmgr.OpMetaSpec` / `DDLSpec` with a `Dispatch` field across `packages/` for an absent or empty
   `Reads`, cross-check against `internal/processor/descriptor_floor.go`'s `checkReadTemplates` — the same
   check CI runs). Confirm the 18/7/0 split before writing any declaration; if the live count differs from
   §8's, trust the live count and note the delta.
2. `SetAppointmentStatus`: widen `InputSchema` (`provider`/`patient` required) + declare its 4 reads (mirror
   `RescheduleAppointment`'s exact template shape, same file).
3. The other 6 class-B ops: declare their reads using the established link-key template grammar (mirror the
   5 named precedents above — do not invent a new template shape; if one of the 7 genuinely doesn't fit the
   existing grammar, STOP and report back rather than forcing it).
4. The 18 class-A ops: hygiene declarations (target+actor only) — mechanical, ride along in the same sweep.
5. Bump every touched package's `manifest.yaml` version + its `Version` constant (both — the package-version
   gate needs them in lockstep).

## Gates (from your worktree)

```
go build ./...
make vet
golangci-lint run ./...
STRICT=1 go run ./scripts/lint-conventions.go
DIFF_BASE=<your worktree's base sha> go run ./scripts/lint-package-version.go
go test ./packages/... ./internal/processor/...
```

`checkReadTemplates` (in `descriptor_floor.go`) is the load-bearing verification here — confirm it passes
clean for every op you touch (it's exercised by the packages' own `go test`, but double check by name if
you can find where it's asserted directly).

## Non-goals

- Do NOT touch `CreateBooking`'s computed optional-reads / `derive_reads` migration — separate, already-filed
  item.
- Do NOT invent a new template shape or a type-agnostic engine segment — census says 0 ops need it; if your
  live re-derivation finds one that genuinely does, STOP and report rather than building new engine surface
  unattended.
- This is FE-adjacent hygiene only insofar as it makes `form.mjs`'s descriptor rendering correct — no FE
  file changes are expected (the auto-push fallback already covers class-A ops client-side); if you find you
  need one, keep it minimal and say why.

## Review depth

Package-mechanical, no security/capability-plane change (declaring an already-server-permitted read
explicitly, not granting new read authority) — **lead review**, not full 3-layer. Winston (the calling
Steward) reviews directly at admit.
