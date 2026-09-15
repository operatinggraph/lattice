# Verticals designer triage — the Facet per-op picker courtesy row (2026-09-15)

**2026-09-15, Winston (unattended Vertical Steward fire).** The lane carried one open row, `📐 needs designer
pass · no-pattern: per-op entity-ref candidate filter in the descriptor vocabulary (FORK-1)`, filed by the
[refusal-courtesy gate's close pass](verticals-refusal-courtesy-gate-2026-09-15.md) from the `CreateBooking` /
`JoinWaitlist` pair: *`CreateBooking` refuses `SessionFull` while `JoinWaitlist` requires a full session, and
Facet's entity-ref picker drops rows on ONE op-agnostic `available` column, so neither op can get its courtesy
without breaking the other.* Method as on [2026-08-27](verticals-designer-triage-2026-08-27.md) and
[2026-09-10](verticals-designer-triage-2026-09-10.md): the `no-pattern:` claim re-run against today's tree,
briefed to falsify; one read-only scout over the consumers; lead reads of every file:line cited.

**Outcome: the row does not need the mechanism it names — it dissolves into a lens column plus two
`Dispatch.VisibleWhen` declarations, both shipped shapes. Built in this fire.** No fork, no frozen-contract
change; FORK-1's vocabulary is a component spec (`docs/components/edge-manifest.md`), and this fire adds no
vocabulary member — it uses one that exists. Winston-adjudicated under the 2026-08-20 delegation.

## 1. The premise, falsified twice

1. **The pair is not a picker pair.** `CreateBooking` and `JoinWaitlist` declare `Dispatch.TargetField:
   "session"` with no `x-entityRef` on the field (`packages/wellness-domain/opmetas.go`): the session is
   auto-filled from the ROW being viewed, never picked. `entityRefCandidates` (`cmd/facet/web/app.js`) is never
   consulted for either op. The filing evaluated the harm in the picker's units; on the wire the ops are
   offered from the entity detail view and pane rows, where `opButton(o, {entityKey, row})` runs
   `opVisibleForRow` — the descriptor vocabulary's existing **per-op, per-row** predicate
   (`OpDispatchSpec.VisibleWhen`, `internal/pkgmgr/definition.go`; consumers `app.js` `opButton`,
   `paneRowOps`). The "per-op filter" the row asks for is that predicate.
2. **The picker corpus has no conflicting pair.** Every `x-entityRef` field today: `menuitem` (`Charge`),
   `provider`, `building`, `patient`, `identity` ×4. The only state column any candidate carries is
   `edgeEntityMenuItems`' `available`, read by exactly one dispatching op. A per-op candidate filter would
   have no second consumer; the `📐` was solution-shaped.

What IS missing is the column: `edgeSessionsTail` projects no seat state (the op-metas' own
`refusal-courtesy(facet): SessionFull: none — … projects no available column reflecting … seat count`), so
`VisibleWhen` has nothing to read. A missing lens column is package work (`verticals.md` header), not a
designer pass. The wellness app already derives the same fact client-side (`cmd/wellness-app/sessions.go`
`countBookingsBySession`: a booking holds a seat unless `waitlisted` / `forfeited`) and swaps Book for Join
waitlist on it (`app.js` `scheduleCard`); Facet gets the identical rule from the read model.

## 2. Design

- **`edgeEntitySessions` projects `full`** (boolean): `count(DISTINCT CASE WHEN NOT (bk.status.data.value =
  'waitlisted') AND NOT (bk.status.data.value = 'forfeited') THEN bk.key ELSE null END) >= capacity` over a
  tail-local `OPTIONAL MATCH (sess)<-[:forSession]-(bk:booking)`. Precedents: `cafe-domain` `tabSettlementSpec`
  (`count(tx.key)` in a `WITH` beside `OPTIONAL MATCH`), `lease-signing` `count(DISTINCT CASE WHEN … THEN
  inst.key ELSE null END)`. The negated-equality form keeps a status-less booking counted (the
  `edgeEntityBookingsTail` `NOT (… = 'forfeited')` reasoning: `compareAny` answers false on nil). Both walks
  share the tail, so both project it. A booking write reaches the row through provenance (the T4 census,
  `personal_delta_exactness_corpus_census_test.go`).
- **`CreateBooking` declares `VisibleWhen{Field: "full", Equals: false}`; `JoinWaitlist` declares
  `{Field: "full", Equals: true}`.** Fail-closed by the vocabulary's own rule: a row without the column offers
  neither. Facet's home "What I can do" list drops the two degraded "open a Class session to do this" cards
  for these ops — an op with `VisibleWhen` is offered only against a row (`opButton`'s documented semantic,
  identical to identity-domain's four); the sessions stay one tap away under Nearby.
- **`entityMeta` renders `full`** as a trailing "full" fact, beside the existing `available === false` →
  "sold out" line; the browse card then says why only Join waitlist is offered.
- **`lint-manifest-entity-type` gains a third rule, VISIBLE-WHEN:** every op meta (corpus-wide) whose
  `Dispatch.VisibleWhen` names a `TargetType` some edge-manifest lens stamps as `entityType` must name a
  `Field` that EVERY such lens projects (`AS <Field>`). Same hazard shape as the gate's PAIRING rule — a
  cross-package promise to a `===` in the renderer with no compiler — and this fire arms it (identity-domain's
  four target pane rows, not `manifest.ent` rows, so the rule was vacuous until now).
- **Refusal-courtesy declarations** move from `none` to `hide — Dispatch.VisibleWhen{Field:"full"…}` for
  `CreateBooking/SessionFull`; `JoinWaitlist` has no not-full refusal (the script waitlists regardless), so
  its `VisibleWhen` is a courtesy with no code and needs no line.

## 3. Fire brief

**Scope sentence.** Give `CreateBooking` and `JoinWaitlist` their per-state courtesy in Facet by projecting
`full` on `edgeEntitySessions` and gating each op with `Dispatch.VisibleWhen` on it; render the fact; gate the
column pairing.

**Touch-list (verified live at `b723aded`).**

- `packages/edge-manifest/lenses.go` `edgeSessionsTail` (the `OPTIONAL MATCH` + `WITH` + `full`);
  `package.go` `Version` 0.17.17 → 0.17.18; `lens_cypher_test.go` vectors beside
  `TestEdgeEntitySessions_*` (:446).
- `packages/wellness-domain/opmetas.go` `CreateBooking` (:168) / `JoinWaitlist` (:244) `Dispatch.VisibleWhen` +
  the `refusal-courtesy(facet)` lines; `package.go` `Version` 0.27.8 → 0.27.9; `lenses.go` :389's
  "the lens engine has no aggregate COUNT" rationale is false today (`cafe-domain` :352) — rewrite to what is true.
- `cmd/facet/web/app.js` `entityMeta` (:1253).
- `scripts/lint-manifest-entity-type.go` rule 3; `docs/components/lint-gates.md` row; `docs/components/
  edge-manifest.md` (`full` on session rows; `VisibleWhen` against a `manifest.ent` column).
- `internal/refractor/grouping_reduction_corpus_census_test.go` :130 and
  `label_derivation_corpus_census_test.go` :174/:275 — re-pin `edgeEntitySessions#0/#1` after reading the verdict.

**Increment order.** (1) lens column + vectors + census re-pins; (2) op-meta `VisibleWhen` + courtesy lines +
version bumps; (3) `entityMeta` + the gate rule + docs; (4) live: `make reinstall-package` both packages,
`make cycle-facet`, read a session row's `full` from the mirror, fill a session and watch Book → Join waitlist;
(5) lead review + gates.

**Gotchas / part 5.** `_packages`: a lens MATCH edit is a corpus edit — run `go test ./internal/refractor/ -run
'Corpus|Census'` and re-pin deliberately. `lint-gates`: a new rule is keyed on the hazard — the field must be
projected by EVERY lens stamping the type, not by some; prove it by mutating the field name once. `edge-manifest`:
a hand-listed vector population exempts its next member — the `full` vectors cover empty / below / at capacity /
excluded statuses / missing capacity. `vertical-apps`: the courtesy clause names the mechanism, never a line.

**Non-goals.** No per-op picker filter; no `DoubleBooked` courtesy (a personal column on the viewer's own claim
— a different row); no change to `wellnessSessions` (the wellness app keeps its client-side tally); no pane
targeting sessions.
