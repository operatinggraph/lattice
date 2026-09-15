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

(filled at close)
