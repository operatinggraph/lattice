# Verticals dossier census — 2026-09-13

A falsifying census of the `docs/components/vertical-apps.md` "Review keeps catching" dossier over all four
vertical FEs and their packages: every class briefed to FIND live instances (candidates enumerated with `file:line`,
verdict per site), every instance fixed in the same fire, and every class seen a second time promoted to a CI gate.
The dossier held nine classes; four are now gates, one is narrowed, one is amended with its second sighting.

## Method

Four read-only scouts (one per vertical) enumerated candidates per class; the lead verified each VIOLATION live
before editing. Where the fix was a gate, the gate was run against `main`'s files to prove it reproduces the
census's findings, and against the fixed worktree to prove it goes clean — never a green run alone.

## Findings by class

| Class | Sites enumerated | Live instances | Disposition |
|---|---|---|---|
| C1 `{actor}` enumeration without `me` | every `renderOpForm(` (wellness 3, LoftSpace 2, clinic ~8, café 3) + hand-built submits | 0 | keeps; hand-built submits omit `contextHint.enumerations`, which the Processor treats as observation-only for non-NFR-S6 ops |
| C2 sibling form dead-ends on a refusal | ops dispatched from 2+ sites × their scripts' `fail(`s | 1 — wellness guest picker offered already-seated bookers (`DoubleBooked`) | fixed (disabled option + label); second sighting recorded |
| C3 count promise vs op predicate | every "N will…" label (wellness 5, clinic 4, café/LoftSpace none) | 0 | keeps |
| C5 script refusal vs OpMeta `required` | 395 unconditional requirements across 160 scripts | 0 | **gate** `lint-opmeta-required-fields` (mutation-proven); entry retired |
| C6 write after await bypasses the staleness guard | every guarded async renderer | 5 — wellness `renderRoster` (own writes), clinic `renderProviderEditForm` / `renderStartSeriesForm` / `openCorrectStatus` (catch before guard), LoftSpace `submitCatalogComplete` (closes another task's modal) | fixed; **gate** `lint-stale-render-guard` (reproduces the 6 pre-fix findings on `main`); entry retired |
| C7 transport throw narrated as "did not land" | every ceremony / irreversible submit | 4 ceremony catches (wellness new guest, LoftSpace new applicant, clinic new patient + connect login) lost a possibly-minted secret; 7 generic descriptor-form dispatchers; hand-built irreversible submits | ceremony + dispatcher catches fixed with build→sent→confirmed staging (a confirmed mint's secret is always handed over); **gate** `lint-ceremony-throw-path`; entry narrowed to the hand-built irreversible submits |
| C8 released uniqueness guard vs FE pair-keyed sets | every `Map`/`Set`/`find` keyed on a pair, across all four | 0 (both wellness sites already exclude `forfeited`) | keeps |
| C9 regex source-scanning test is an open set | every `_test.go` reading `app.js` | 0 (all three catalog tests are closed-set) | keeps |
| C10 value reaches markup unescaped | 169 `innerHTML`/`outerHTML`/`insertAdjacentHTML` sinks | 6 confirmed — café `e.message` ×4, `booking.sessionName`, LoftSpace `rentCurrency` via `money()` (a stored XSS every applicant rendered); clinic wellness-session `<option>` template; 7 unescaped server-typed fields | fixed; **gate** `lint-markup-escaping` (reproduces 17 findings on `main`); entry added |

## Gates

All four read the JavaScript through goja's parser (the engine the FE tests run under) or the Starlark through
`go.starlark.net/syntax`, derive their sets from the corpus (the escaper by its `&amp;` map, the write transports by
the non-GET `method` literal, the ceremony ops by their `Ceremony` spec, the requirers by their refuse-on-absence
bodies), self-test on every run, refuse an all-clear over zero examined units, and are wired into `lint-static`,
the Makefile and `docs/components/lint-gates.md`.

## Residual (kept in the dossier, not filed)

C7's hand-built irreversible submits (clinic `setStatus` / `endSeries` / `submitRemoveProviderSite`, LoftSpace
`UnlinkCredential` / `WithdrawLeaseApplication` / `DecideLeaseApplication`) still say "could not" on a transport
throw; C10's DOM path (`setAttribute("href")`, `src =`) is outside the gate. Both are dossier checks a builder
walks, not board rows.
