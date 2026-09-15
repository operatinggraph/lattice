# Verticals packages dossier census — 2026-09-14

A falsifying census of the `docs/components/_packages.md` "Review keeps catching" dossier over the twelve
vertical packages (`cafe-{domain,ledger}`, `clinic-{domain,ledger,reminders}`, `wellness-{domain,ledger,reminders}`,
`loftspace-{domain,ledger}`, `lease-signing`, `one-bill`): every enumerable class briefed to FIND live instances
(candidates with `file:line`, verdict per site), every instance fixed in the same fire, and every class whose check
is mechanizable and twice-seen promoted to a CI gate. The package-side twin of
[verticals-dossier-census-2026-09-13.md](verticals-dossier-census-2026-09-13.md).

## Fire brief

**Scope sentence.** Census the nine enumerable `_packages.md` classes over the vertical packages; fix every live
instance; promote the `kv.Links` page-limit class to a gate that reproduces the census on `main` and goes clean on
the fix; amend the dossier where the census falsified a class's own prescribed check.

**Touch-list (verified live at `9594cb3d`).** Every `kv.Links(` call in a shipped package script with no explicit
limit — 33 sites: `clinic-domain/ddls.go` ×7, `clinic-reminders/visitseries.go` ×3, `clinic-ledger/scripts.go` ×1,
`cafe-domain/ddls.go` ×1, `cafe-ledger/scripts.go` ×5, `wellness-domain/ddls.go` ×8, `wellness-ledger/scripts.go` ×1,
`loftspace-ledger/scripts.go` ×2, `lease-signing/leasedoc_scripts.go` ×2, and outside the verticals
`orchestration-base/ddls.go` ×2 + `maintenance-domain/ddls.go` ×1 (the gate runs over the whole corpus, so the fire
fixes them too rather than allowlisting). Precedent for the gate: `scripts/lint-link-target-count.go` (corpus
enumeration via `pkgregistry`, `go.starlark.net/syntax` parse, self-test, zero-examined refusal, `--list`).
Precedent for the fix: the `MAX_*_PAGES` / `*_PAGE_LIMIT` cursor loop (`cafe-domain/ddls.go:592-615`) and the
`None, 1` write-once probes (`cafe-domain/ddls.go:936`, `wellness-domain/ddls.go:4711`).

**Increments.** (1) the gate, wired into `lint-static`, the Makefile and `docs/components/lint-gates.md`, proven
to list the 33 sites on `main`; (2) the 33 fixes, each package's manifest + `Version` bumped, package tests +
`lint-package-version` green; (3) this document's findings tables + the dossier amendment.

**In-scope gotcha (the census's own finding against the dossier).** The dossier's check reads "every `kv.Links`
whose relation is single-valued passes `None, 1`". That is wrong for a single-valued relation that is ever
REPOINTED: `ListLinks` returns tombstoned links (`isDeleted=true`) in the page, keys sort by target id, and a
repoint tombstones the old key and writes a new one — so a page of one can be the tombstone and the live link is
never seen. `atStudio` / `atLocation` (wellness `ReassignSession` with `newStudio`) and `appliesToUnit`
(lease-signing `scripts.go:1236`) are repointed; `practicesAt` and `manages` are multi-valued and retired
per-link. Those sites take a small page plus the cursor loop until a live link is found; only a relation no op
ever tombstones takes `None, 1`.

**Non-goals.** P3 (a `Params` column sourced from an `OPTIONAL MATCH`) is once-seen and its mechanization needs
the cypher alias chain — recorded below, not gated. P4/P6/P7/P9 were scouted at file:line and found clean; no
gate is derivable from a clean census.

## Method

Four read-only `haiku` scouts (one per vertical) enumerated candidates per class from a shared brief; the lead
verified every VIOLATION live before editing and re-ran the P1 census by grep. Where the fix is a gate, the gate
is run against `main`'s files to prove it reproduces the census, and against the fixed worktree to prove it goes
clean — never a green run alone.

## Findings by class

| Class | Sites enumerated | Live instances | Disposition |
|---|---|---|---|
| P1 `kv.Links` with no page limit | 105 `kv.Links` calls across 83 scripts / 31 packages (gate `--list` on `9594cb3d`) | 33 — clinic-domain ×7, clinic-reminders ×3, clinic-ledger ×1, cafe-domain ×1, cafe-ledger ×5, wellness-domain ×8, wellness-ledger ×1, loftspace-ledger ×2, lease-signing ×2, orchestration-base ×2, maintenance-domain ×1 | fixed per relation (write-once → `None, 1`; repointed → bounded first-live loop; multi-valued → named page constant + full walk); **gate** `lint-links-page-limit`; entry retired. The dossier's own `None, 1` prescription falsified for repointed relations (fire brief) — `ledBy` was briefed write-once and reclassified from `ReassignSession`'s doc comment |
| P2 confinement guard tested only as operator | 25 confined ops (café 10, clinic 8, wellness 5, LoftSpace 2) | 0 — every op has a non-operator staff vector (`workplace_confinement_test.go` / `frontdesk_confinement_test.go` / `landlord_manages_guard_test.go`) | keeps |
| P3 playbook `Params` on an `OPTIONAL MATCH` column | 16 targets | 0 — clinic-ledger's `missing_charge` conjoins `accountKey <> null`; `missing_reversal` implies `txCount = 1`, and no op tombstones a `clinicaccount` or its `heldFor`, so a null `accountKey` is unreachable; clinic-reminders `handledAt` gated `<> null` | keeps; once-seen (café `cafeArrearsReminders`), the mechanization needs the cypher alias chain (which `WITH`/`RETURN` name a variable bound by `OPTIONAL MATCH`, and whether the gap column conjoins it `<> null`) |
| P4 live `kv.Read` then unconditioned mutation | 94 `kv.Read` sites (café 18, clinic 52, wellness 6, LoftSpace sampled) | 0 — the bare-`update` helpers (`make_aspect_update`, cafe-ledger ×5 / clinic-ledger ×1) act only on keys `derive_reads` hydrates on every dispatch | keeps |
| P5 sensitive read without a shredded check | 5 `Sensitive: true` aspects (lease-signing 4, clinic 1) | 0 — no script `kv.Read`s one; the leasedoc `tenantName` read sits behind the erasure gate | keeps |
| P6 self-leg vs staff-leg asymmetry | 12 branching ops | 0 — every asymmetry carries its reason in the script (a self cap on `CreditAccount`, `identifiedBy` on the patient legs) | keeps |
| P7 status-value census | `.status` / `.decision` / `type` / `reason` writers vs guards vs lens conjuncts | 0 divergences | keeps |
| P8 absence read as benign | 27 `in state` / `.get` tests on OptionalReads | 0 — the `DebitAccount` fail-closed from 2026-09-13 holds; the rest are liveness helpers | keeps |
| P9 link-key type segment | every `make_link` / `lnk.` construction | 0 | keeps |

## Gate

`lint-links-page-limit` parses every shipped package script (the `pkgregistry` corpus) with `go.starlark.net/syntax`;
a `kv.Links` call must pass a 5th positional argument or `limit=` that resolves to an integer literal, a module-level
integer constant, or the enclosing helper's own parameter; a missing limit, a cursor-only call, or a bare literal at
or above the engine default (256) is a finding; a named constant resolving that high is exempt (six platform-package
sites). Self-tests on every run including the drop-the-limit mutation, refuses an all-clear over zero examined
scripts, `--list` audits every call, and a script shared by several DDL registrations is examined once. Wired into
`lint-static`, the Makefile and `docs/components/lint-gates.md`. Proof: 33 findings on `9594cb3d`, clean on the fix.

## Residual (kept in the dossier, not filed)

The `_packages.md` dossier holds 21 live entries against its stated cap of 12; the classes with no enumerable check
(conserved quantity, "by construction" consumer rows, informational-field-becomes-load-bearing) are walk-only and
a later census cannot retire them by gate. P3's gate shape is recorded above for its second sighting.
