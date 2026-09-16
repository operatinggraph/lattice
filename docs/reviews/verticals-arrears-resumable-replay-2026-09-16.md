# Verticals — "No ledger reminds a debtor past ~30 lines" (2026-09-16)

**Filed (clinic close pass, 2026-09-16):** the arrears replay walks the account's history inside the op
(≈300 round trips for 98 entries) and the 250 ms script wall aborts it; clinic degrades honestly at 30 entries,
café / wellness / loftspace still promise 500. Live: Riley Chen, the one clinic debtor, is never reminded. Filed
`📐 needs designer pass · no-pattern: a one-round-trip read of an enumeration's follow-up aspects, or a per-op
script wall`. Method as on [2026-09-15](verticals-designer-triage-2026-09-15.md): the `no-pattern:` claim re-run
against today's tree, briefed to falsify; lead reads of every file:line cited. Winston-adjudicated
(implementation-level; no contract surface, no fork — the mechanism is package-only).

**Outcome: the row does not need the primitive it names. The replay becomes RESUMABLE — one page per
dispatch, the running aggregate recorded on the account, Weaver's own gap loop chaining the pages — and any
history length is reached exactly, in the same package, under the same wall.** Built in this fire, clinic first
(the live instance), then the three siblings.

## 1. The premise, re-censused

1. **Half the named primitive is SHIPPED.** The round-trip-collapse design
   (`script-live-read-round-trip-collapse-design.md`, Andrew-ratified 2026-08-13) landed Fire 1 at `15c46beb`:
   a `kv.Links` page reads its link bodies in one `KVGetMulti` (`internal/processor/starlark_kv.go`
   `connLinkLister.ListLinks`, `listLinksGetChunkSize`), and the lazy-read DDL resolution is memoized per
   execution. A page of the `postedTo` enumeration is two round trips, whatever its size.
2. **The other half is real, and a Lattice-lane extension of that ratified design — not this row's fix.** The
   per-entry follow-up reads (`kv.Read(tx + ".entry")`, one live round trip each) and the per-credit `reverses`
   walks (`kv.Links(credit, "reverses", "out", None, 1)`, two round trips each) are what the 98-entry account
   spends its ≈300 round trips on. No batched lazy read (`kv.ReadMulti`) exists; Fires 2–3 of the ratified design
   address the multi-page listing term and the budget's denomination, not follow-ups. **But a batched follow-up read
   would not make the replay safe:** the `reverses` walks are per-credit enumerations, ~2 ms each after Fire 1,
   so a 100-credit account still spends ≈200 ms of a 250 ms wall. The primitive moves the ceiling; it does not
   remove it. A ceiling that scales with history is the design defect, whichever primitive sits under it.
3. **The demand side is four copies of one script.** `arrears_entries` / `arrears_head` are the same function in
   `cafe-ledger/scripts.go:154-296`, `wellness-ledger/scripts.go:154-341`, `loftspace-ledger/scripts.go:392-526`
   and `clinic-ledger/scripts.go:174-314`. Three still carry the 50 × 10 budget the wall refutes at ~100 entries —
   past which the op is a `ScriptFailed` rejection, the gap stays open, and Weaver re-dispatches the doomed replay
   on every reclaim until its default retry budget (3, `internal/weaver/evaluator.go` `defaultDirectOpRetryBudget`)
   parks the row under `GapBudgetExhausted`. Their live accounts are smaller today; the shape is the same.

## 2. The mechanism the ledgers already have, applied to time instead of space

The replay is exact because `arrears_head` sees every entry. It is unsafe because it sees them all in one
execution. **Nothing in the FIFO needs one execution:** the head is a function of aggregates that accumulate
page by page — each debit's `(postedAt, face)`, each debit's total reversal, the total of all credits — and the
FIFO walk (`arrears_head`) runs once, over the accumulated set, when the enumeration's cursor runs out.

The account's `.arrears` aspect already carries per-episode recorded state written by two verbs (evaluation
mints / recomputes; `post_entry` opens / ends / carries). It gains one more recorded fact of the same kind — a
**replay checkpoint** — present only between the first page and the last:

```
.arrears.replay = {
  phase:       "a" | "b",         # flips on every page — Weaver's continuation trigger (§3)
  cursor:      <opaque kv.Links cursor>,
  pages:       n,                 # pages consumed so far, incl. this one
  debits:      { <txId>: {postedAt, amountCents} },
  reversed:    { <txId>: cents }, # Σ reversal credits naming that debit (uncapped; capped at finalize)
  creditCents: n                  # Σ every credit, plain and reversing alike
}
```

`arrears_entries(acct_key, cursor)` consumes ONE page (`ARREARS_PAGE_LIMIT`, 30 — the per-dispatch round-trip
budget the wall arithmetic already sized, `a3b8e12d`) and folds it into the aggregate. The finalize step
rebuilds `arrears_head`'s input from the aggregate: `netface(d) = face − min(face, reversed[d])`,
`plain = creditCents − Σ_d min(face(d), reversed[d])`, head = the first debit in `(postedAt, key)` order whose
cumulative net face exceeds `plain`. That is `arrears_head`'s own result restated as sums — credits cover
debits oldest-first whatever their timing, so only the totals and the per-debit netting matter, and the
existing FIFO walk over synthesized rows (one debit row per `debits` entry, one plain credit row for
`plain`) reproduces it without a second implementation. `cmd/clinic-app/ledger.go`'s `deriveStatement`, the
statement's derivation, is unchanged — the op computes the same head from the same facts.

**Why the checkpoint keys debits by transaction id (and is not the key-list anti-pattern).** The house rule
(`CLAUDE.md`; `agents/steward/SKILL.md` §2 no-paper-over) forbids an aspect that STORES A RELATIONSHIP as keys so
an op can skip enumerating links (`.bookings`). The checkpoint is the carry of an enumeration in progress: every
page reads the `postedTo` links themselves, the finished aspect holds none of it, and no reader — lens, app, or
op — consumes it as a relation. The id (not the full key) is carried because exactness demands identity: keying
`reversed` by the target's `postedAt` is exact only until two charges share a second and one is reversed for
more than its face (constructible — `ClinicCreditAccount` caps no reversal), where the merged cap moves the
head a day earlier than the statement's. §5 records the key-free form as the rejected alternative.

**State-lifetime table for `.arrears.replay`.**

| Boundary | Rule |
|---|---|
| Created | By the evaluation's first page, when `kv.Links` returns a live cursor; never on a history that fits one page (today's shape, no checkpoint ever written) |
| Carried | By each further page, which rewrites it whole (aggregate + advanced cursor + flipped phase) — the write is OCC-conditioned on the `.arrears` revision the page hydrated (the target's `OptionalReads`, the DDL's `derive_reads`), so two pages racing on one checkpoint serialize |
| Reset | By `post_entry` on EVERY branch — a posted entry changes the enumerated set under the cursor, so the checkpoint is dropped and `stale` set (the partial-payment carry already does; the legacy, open and end branches gain the drop) — and by the finalize page, which writes `.arrears` without it |
| Ordered | Pages consume the sorted key set the substrate pages (`substrate.pageFilteredKeys`: sort, de-dup, strict-greater cursor), so a set unchanged between pages is walked once, exactly |
| Bounded | `ARREARS_MAX_PAGES` (20 → 600 entries): a history past it finalizes as `historyTooLong` — today's degrade, resized, quiet + visible, the next posted entry buying one more attempt — and RECORDS the budget it exhausted (`historyBudget: 600`). The lens suppresses only a flag recorded at or above the CURRENT budget; a flag recorded under a smaller one (or none — the flag predates the field) re-opens the gap for exactly one evaluation under the raised budget. A raised ceiling reaches the accounts the old one parked, once, by a recorded fact, not by a migration |
| Replay / redelivery | A redelivered dispatch re-reads the checkpoint and consumes the NEXT page — progress, not repetition; a second concurrent dispatch loses the OCC race and re-executes on the advanced state |
| Tombstone | Tombstoned links occupy page slots and are skipped, as today (`starlark_kv.go` G5) |
| Upgrade | No account carries `replay` at install (the field is new). The nine standing clinic accounts' closed rows are unchanged by the `replay = null` conjunct; the one flagged `historyTooLong` under the 30-entry budget carries no `historyBudget`, so the recorded-budget arm above re-evaluates it once — that is the live proof |

## 3. Weaver chains the pages — two phase gaps, not one open gap

Weaver is level-triggered with an in-flight mark: a gap column that stays true after a successful dispatch
holds its mark for the whole lease (30 min, `defaultMarkLease`) before the reconciler reclaims and re-dispatches,
and the dispatch count accrues across the episode against `maxretries_<g>` / the engine default of 3
(`_packages.md` dossier: "a level-triggered gap that stays OPEN across successful dispatches … wedges the anchor
at the 4th cycle"). A checkpointed replay must therefore NOT leave `missing_evaluation` open across pages.

Instead each page CLOSES the gap that dispatched it and OPENS the other: the lens projects two continuation gaps
— `missing_replay_a` = `(a.arrears.data.replay.phase = 'a')`, `missing_replay_b` = `(… = 'b')` — each a
`directOp(Evaluate<Ledger>Arrears)` entry on the playbook with the same `Params` / `Reads` / `OptionalReads` /
`Enumerations` as `missing_evaluation`; and `missing_evaluation` gains the conjunct `AND (a.arrears.data.replay =
null)`, as does `freshUntil`; its `historyTooLong` suppression becomes `NOT (historyTooLong AND historyBudget >= 600)` with a
fourth arm `(historyTooLong AND NOT (historyBudget >= 600))` (§2's Bounded row). Page 1 (dispatched by `missing_evaluation`) writes `phase: a` → that gap closes
(mark + count cleared), `missing_replay_a` opens → dispatched on the row's next evaluation → page 2 writes
`phase: b` → … → the finalize page writes no `replay` → every gap false. Each gap episode is one dispatch, so
the engine default retry budget stands (a REJECTED page — a wall breach — is reclaimed up to three times, then
parked loud under `GapBudgetExhausted`, which is the right outcome for a page that cannot run), and a history
chains at the row-update cadence: 98 entries in four dispatches, seconds apart, not hours. `violating` is the
OR of the three gaps.

The op does not know which gap dispatched it: it reads `.arrears.replay` and continues or starts. A
`post_entry` mid-replay drops the checkpoint and sets `stale` → the phase gap closes, `missing_evaluation`
re-opens, the next evaluation starts at page 1.

## 4. Per ledger

| Ledger | Reverses? | Aggregate | Version |
|---|---|---|---|
| clinic-ledger | yes (`reverses`, uncapped) | full (`debits`, `reversed`, `creditCents`) | 0.6.1 → 0.7.0 |
| cafe-ledger | yes (`reverses`, refund capped at the charge) | full — mirror clinic verbatim; the cap does not make the key-free form exact for legacy data | 0.7.0 → 0.8.0 |
| wellness-ledger | none | `debits` + `creditCents` (no `reversed` map, no netting) | 0.2.26 → 0.3.0 |
| loftspace-ledger | none (`scripts.go:402`) | as wellness; its lens names `remindAt`, not `dueAt` | 0.8.2 → 0.9.0 |

The three siblings drop their 50 × 10 budget for clinic's 30 × 20 constants, bound into the prelude from Go
(`ArrearsPageLimit` / `ArrearsMaxPages`), the way clinic's `a3b8e12d` did — the number the wall refutes leaves
the corpus.

## 5. Alternatives

| Alternative | Verdict |
|---|---|
| **Delete the replay** — keep "no ledger reminds past 30 lines" as the recorded ceiling | Rejected: it is the filed harm; the one live clinic debtor sits past it. |
| **Episode-scoped dueAt** (keep the opening charge's date through partial payments; no head recomputation) | Rejected: overstates lateness after a partial payment (the FIFO head moves to a later charge); the statement's derivation would have to change with it, on every surface, for a worse product fact. |
| **`kv.ReadMulti` — the row's named primitive** (batched lazy follow-up read; Fire 4 of the ratified round-trip design) | Not this row's fix (§1.2): removes the `.entry` term, leaves the per-credit `reverses` term, ceiling still ∝ credits. Real as a Lattice-lane extension of the ratified design when a consumer's wall cost is follow-up-bound after this fire; not filed — no such consumer remains once the replay pages. |
| **Weaver declares the follow-up reads from a lens `collect()` column** | Rejected: `resolveReadKey` (`internal/weaver/strategist.go`) resolves one string per `Reads` entry — a list column needs a Weaver grammar extension; and the `reverses` term stays. |
| **Incremental aging schedule maintained by `post_entry`** (`[{postedAt, remaining}]`) | Rejected: exact only under commit order == `submittedAt` order (two debits interleaving a credit inside the submit→commit skew swap heads); legacy accounts need the very backfill replay this row is about. |
| **Key-free checkpoint** (`debits` merged by `postedAt`) | Rejected (§2): inexact when two same-second charges meet an over-face reversal. |
| **One open gap, paced by the mark-lease reclaim, `maxretries_evaluation` = page cap** | Rejected (§3): 30 min per page (600 entries = 10 h), the dispatch count accrues across the episode, and it is the exact dossier shape the `visitSeriesDue` wedge minted. |
| **A Weaver "continuation" mode** (a gap whose mark clears when the row's revision advances past the mark's) | The platform-shaped form of §3. Not filed: any re-projection would re-arm it (a `post_entry` mid-flight double-dispatches), and the phase flip is precise with no engine change; revive if a second package needs a paged Weaver-driven computation — then the pattern is promoted, not the trick repeated. |
| **Widen the wall** | Rejected by the ratified design (§8.2) — hides the cost, converts a fast refusal into a slow one. |

## 6. Fire brief (build note, 2026-09-16)

1. **Scope** (board row, verbatim): *The arrears replay walks the account's history inside the op (≈300 round
   trips for 98 entries) and the 250 ms script wall aborts it; clinic degrades honestly at 30 entries,
   café/wellness/loftspace still promise 500. Live: Riley Chen, the one clinic debtor, is never reminded.* Green
   bar: an account of any history length up to 600 entries is evaluated exactly across as many dispatches as it
   has pages, Weaver chaining them without operator action; Riley Chen's 98-entry account records `sentAt` and
   the bridge replies, live; a posted entry mid-replay restarts it; a history past 600 entries records
   `historyTooLong`; the three sibling ledgers carry the same mechanism and constants; no lens reads a clock.
2. **Touch-list (verified live):** clinic-ledger `scripts.go:21-52` (the Go constants + prelude — `ArrearsMaxPages`
   1 → 20) · `:158-228` (`arrears_entries` → one page, takes + returns the cursor, folds into the aggregate) ·
   `:230-314` (`arrears_head` unchanged; a `head_from_aggregate` builds its rows) · `:316-320` (`carry_arrears`) ·
   `:397-585` (the evaluate branch: read the checkpoint, page, mid-replay write vs finalize; `historyTooLong` at
   `pages > ARREARS_MAX_PAGES`) · `:768-800` (transactionDDLScript's `carry_arrears` drops `replay` beside
   `historyTooLong`; every `post_entry` `.arrears` branch drops it) · `lenses.go:102` (BodyColumns +
   `missing_replay_a/b`) · `:268-380` (the spec: two gap columns, the `replay = null` conjunct on
   `missing_evaluation` and `freshUntil`, `violating` = OR of three; the doc comment's lifecycle gains the paged
   case) · `targets.go:80-140` (two more `GapActionSpec` entries, identical to `missing_evaluation`'s) · `ddls.go`
   (the `.arrears` aspect DDL description names `replay`) · `package.go:111` + `manifest.yaml:2` (0.7.0) ·
   `README.md:199` (the ceiling sentence) · tests `arrears_test.go` (`TestArrears_HistoryPastTheBudgetDegrades`
   :808 re-seeded at `ArrearsPageLimit*ArrearsMaxPages + 1`; new: a 31-entry account completes in two dispatches
   with the same head a one-page account computes; a `post_entry` between pages drops the checkpoint and the next
   evaluation restarts; the checkpoint carries `dueAt`/`remindedFor`/`sentAt` untouched mid-replay and sends
   nothing; a redelivered dispatch advances, never repeats; a reversal on page 1 of a charge on page 2 nets
   correctly; the phase alternates a→b→a) · `arrears_lens_test.go` (pins: mid-replay `missing_evaluation` false,
   `missing_replay_a` true, `freshUntil` null; phase b; checkpoint gone → all false) · `derive_reads_test.go`
   (unchanged set). Then café `scripts.go:151-300, 377-560, 782+` · `lenses.go:77, 150-260` · `targets.go:55-78`;
   wellness `scripts.go:151-341, 422-560, 779+` · `lenses.go:163, 240-280` · `targets.go:216`; loftspace
   `scripts.go:389-526, 635-760, 1100+` · `lenses.go:75, 100-160` · `targets.go:59`.
3. **Precedents:** the shipped `historyTooLong` degrade branch (clinic `scripts.go:466-497`) for a write that carries
   `prior` and records a flag; wellness `maxretries_evaluation` (`wellness-ledger/lenses.go:278`) for a
   literal-valued column; the two-gap playbook shape (`clinic-ledger/targets.go` `missing_account` +
   `missing_charge` on one target); `substrate.pageFilteredKeys` for the cursor's contract; lease-signing's
   version-derived re-install for any test that must install a modified `Definition`.
4. **Increments:** (1) clinic-ledger — script + lens + playbook + DDL text + version + tests; green: `go test
   ./packages/clinic-ledger/ -count=1`, `go test ./internal/refractor/ -run 'Corpus|Census' -count=1`. (2)
   café-ledger, (3) wellness-ledger, (4) loftspace-ledger — mirrors, each with its own `go test ./packages/<p>/
   -count=1` + the refractor census. Each: `go build ./...`, `make vet`, `golangci-lint run ./...`, every
   `scripts/lint-*.go` under `STRICT=1`, `DIFF_BASE=<base> go run ./scripts/lint-package-version.go`. (5) live:
   `make reinstall-package PKG=clinic-ledger` (then the siblings), the nine clinic accounts re-evaluate (the
   `replay = null` conjunct changes no closed row), Riley Chen's chains four pages and records `sentAt`; the
   `.arrearsNotification` reply lands.
5. **Gotchas:** one package edit ⇒ one version bump + `Version` constant; a lens RETURN edit is a corpus edit
   (refractor census pins per lens); `lint-gap-column-declaration` for the two new gap columns on each target;
   `lint-links-page-limit` on the unchanged `kv.Links` calls; `lint-live-read-pinned-mutation` — the checkpoint
   write rides the same OCC-conditioned `.arrears` upsert; `lint-refusal-courtesy` — no new refusal (the evaluate
   op has no dispatch site); `lint-seed-declared-reads` unchanged. `_packages.md` dossier: *a gap's budget and
   cadence are derived from its WHOLE loop* (this design's §3 is that walk — pin the gap FALSE over the state
   every arm leaves: page, finalize-due, finalize-clear, finalize-degrade); *a mirror that drops one of the
   precedent's checks or branches drops the INVARIANT it enforced* (wellness/loftspace drop only the `reversed`
   map — they have no `reverses` relation to net; every other branch is kept); *a recorded value is read as the
   FACT it records* (`replay` is a checkpoint, `evaluatedAt` still names the last COMPLETED evaluation — a
   mid-replay write carries the prior stamp, never the page's); *the "leg" is every writer of the guarded VALUE*
   (`post_entry`'s FOUR branches all drop `replay`; `carry_arrears` in both scripts). Standing checklist #1 (the
   lifetime table above) · #3 (each new lens conjunct and each `post_entry` drop revert-proven) · #5 (`replay` has
   one writer per verb: the evaluation writes it, `post_entry` only drops it).
6. **Adjacent finds:** the ratified round-trip design's unshipped follow-up-read term (§1.2) — not filed (no
   consumer after this fire). None else.
7. **Non-goals:** any change to `internal/processor` or `internal/weaver`; the apps (recorded-wins already reads
   the record); the notification adapters; a faster page (the constant is the knob, sized by the wall
   arithmetic); the FIFO semantics themselves.
