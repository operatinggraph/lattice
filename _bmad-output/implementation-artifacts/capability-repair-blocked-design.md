# `CapabilityRepairBlocked` — the auth plane's loudest alert cannot name what it found

**Status: ✅ Winston-ratified — build-ready (2026-08-24).** No frozen-contract change and no architectural
fork: guard semantics, the §6.2 tie rule and the write path are untouched. The fire carries the divergence
classification that `internal/refractor/pipeline` already computes across the two aggregation boundaries
that currently discard it, and drives the health severity from the class rather than from a streak alone.
Steward fire, Winston, 2026-08-24.
Owning component: **Refractor** (`internal/refractor/pipeline`, `internal/refractor/health`,
`cmd/refractor`).
Board row: `backlog/lattice.md` → *[Refractor] `CapabilityRepairBlocked` leaves an auth-plane row
permanently wrong*.

---

## For Andrew

**What it does, in two lines.** `CapabilityRepairBlocked` is the capability plane's most severe standing
alert, and today every blocked repair renders identically: one count, one lexicographically-chosen reason
string, one severity ladder driven by streak length. The comparator one layer down already separates
*provenance-only* drift — which Contract #6 §6.3 calls coherence/debug provenance and which §15.7 of the
auth-plane design records as **reachable by ordinary operation** — from a *content* divergence, which that
same section records as having **no observed producer** and which the shipped code comment calls "a real
finding on sight". This fire carries that distinction up to the operator.

**Why it matters right now.** The live signal behind the board row is *17 rows unrepairable over 250+
consecutive sweep passes*, at `error` severity, on the auth plane. Nothing in that alert says whether it is
250 passes of benign provenance drift or a standing grant divergence, and a genuine content divergence
arriving today would land inside the same counter at the same severity and change nothing an operator sees.
The alert has been at maximum volume for long enough that it can no longer raise its voice.

**What it deliberately does not do.** It does not change the ordering guard, the `≤`-rejects tie rule, or
any write path — Andrew **held** that change on 2026-08-06 (`auth-plane-projection-latency-design.md`
§15.7) and this fire honours the hold verbatim. It does not add a repair path: Contract #6 §6.2 already
carries the sanctioned repair for this state (the 12.1b rebuild interaction — truncate, or guard-bypass
replay), and the fire's contribution there is that the alert now **names** it. §15.7's revive shape
(a reconciliation stamping the consumer's fully-drained head) stays unbuilt and stays Andrew's, and its
own recorded revive trigger — *"the day a real content divergence is observed at a resting token"* —
is precisely what this fire makes observable. **The evidence for that decision does not exist until this
ships.**

---

## 1. Problem

Three aggregation boundaries each discard the divergence class, in the same direction.

**(a) `verdictFold.add` keeps the first blocked reason, not the worst.**
`internal/refractor/pipeline/reproject.go:159-166`: on a tie in verdict severity the fold takes a new
reason only when the held one is empty. A per-entry lens whose actor holds one provenance-only blocked row
and one content-divergence blocked row reports whichever result the loop reached first. `Verdict` has a
severity order; the reason behind it does not.

**(b) `governingReasonLocked` picks the lexicographically smallest reason across the lens.**
`internal/refractor/pipeline/sweep.go:538-546`: `if governing == "" || reason < governing`. That the word
`content` currently sorts ahead of `provenance-only` is an accident of the letter `c`, not an ordering
anyone chose; and only ONE string survives for the whole lens, so a single content divergence among sixteen
provenance ones is at best one line with no count and at worst invisible.

**(c) The health surface has one counter family and one severity ladder.**
`internal/refractor/health/lattice_heartbeater.go:1135-1148` (capability lens) and `:1937-1943` (plain
lens): `SweepBlocked` / `SweepBlockedStreak` / `SweepLastBlocked`, escalating to `error` on
`SweepBlockedStreak >= capabilityDivergenceErrorStreak` regardless of class. A benign class reaches the
same severity as the class that has no known producer, and reaches it on the same schedule.

There is a fourth, worse case hiding inside the same counter. A blocked **retraction** — a declined
`Delete`, `reproject.go:413` — is the over-grant direction: a revoked grant stays live and honoured. It
carries no class at all in its reason string and folds into the identical warning as provenance drift.

## 2. What ships

A `BlockedClass` carried explicitly from the point of classification to the operator, and severity driven
by it. Parsing the class back out of the reason string is refused by construction — it is exactly the
"one clause over a multi-shape set" defect the standing checklist names.

| class | meaning | severity |
|---|---|---|
| `BlockedRetraction` | a declined `Delete`: a revoked grant stays live | `error` on sight |
| `BlockedContent` | content divergence declined at the guard | `error` on sight |
| `BlockedUnknown` | no read-back, so the class cannot be proven | escalates on streak (today's ladder) |
| `BlockedProvenance` | `projectedFromRevisions` drift only; row meaning identical | `warning`, does not escalate |

The ordering is the fold's tie-breaker, the sweep's governing-reason choice, and the health severity — one
order, defined once. `BlockedUnknown` keeps today's behaviour, which is the fail-closed direction: an
unclassifiable blocked row is not quietly demoted to the benign class.

**Why content and retraction are `error` on sight rather than on a streak.** Both are conditions the
ratified text records as having no ordinary producer, so the first sighting is the finding; a streak
requirement on a class that should never appear once only delays the alert. Provenance drift is the
opposite — reachable by an ordinary lens-definition write that leaves the MATCH unchanged — so it stays a
warning however long it persists. It is not silenced: a permanent provenance streak is a real, benign,
standing condition and an operator should be able to see it without it drowning the plane.

**The remedy text.** Every blocked issue now names the sanctioned repair (Contract #6 §6.2's rebuild
interaction: a guarded bucket's rebuild forces `truncate=true`, the purge clears the watermarks with the
data, and the stream replays from empty). An alert that says a row is permanently wrong and stops there
leaves the operator to rediscover a remedy the contract already sanctions.

## 3. State lifetime (standing checklist #1)

The per-class blocked accounting is new state in `Sweeper`, so its lifetime is declared before it is built.
It is a **refinement of an existing standing set**, not a new one: `s.blocked` (`sweep.go:207`) already
holds `map[actor]reason` with a defined lifetime, and this fire widens its value to `(class, reason)`.
Therefore every existing transition carries over unchanged and none is re-derived:

| boundary | behaviour | unchanged from |
|---|---|---|
| a later clean verdict on the same actor | entry deleted | `noteVerdict`'s `default` arm, `sweep.go:656-658` |
| a later `VerdictUnverified` on the same actor | entry deleted, moves to `unverified` | `sweep.go:654` |
| a later blocked verdict on the same actor | entry **replaced** (a new verdict supersedes) | `sweep.go:651-653` |
| actor leaves the corpus | entry deleted | `reapDepartedFailures`, `sweep.go:631-635` |
| process restart | set is in-memory and rebuilds from the next pass | pre-existing |
| streak counters | `BlockedStreak` still counts passes with ≥1 blocked row of ANY class | `sweep.go:1147-1153` |

The per-class counts are derived from the set at publish time (`sweep.go:1147`), not accumulated
separately — one source of truth, so a class count can never disagree with the total it is part of.

**Health `Entry` carry-forward.** New health fields trip
`health/entry_carry_forward_completeness_test.go`, the gate that retired this component's carry-forward
dossier entry. Every field added here is carried forward at each wholesale writer or explicitly
allow-listed as writer-owned with a reason. That gate is the acceptance, not a checklist item.

## 4. Green bar

- A per-entry actor holding both a provenance-only and a content blocked row reports **content** — from the
  fold, from the sweep's governing reason, and in the health issue's severity.
- A lens whose blocked set is entirely provenance-only does **not** reach `error`, at any streak length.
- A lens with exactly one content-divergence blocked row reaches `error` on the **first** pass.
- A declined retraction reaches `error` on the first pass and is not reported as a divergence class.
- The counts published per class sum to `SweepBlocked`.
- Both the capability-lens and plain-lens issue paths carry the same **classification** (`:1135` and
  `:1937`) — per-class counts, worst class, class-named reason, remedy text.

  **Amended 2026-08-24, at build.** This line originally read *"carry the same treatment"*, and the build
  correctly read that as including severity. It does not, and shipping it that way would have been an
  availability regression this item never grounded. The plain-lens path keeps its **`warning` ceiling**:
  `evalLensSweep`'s own reason — *failing the whole Refractor instance for a business lens's sweep issue
  would take the auth plane down with it* — is load-bearing, absolute in both
  `docs/components/refractor.md` and the Health-KV schema, and untouched by anything this fire measured.
  A business lens's blocked row is a data-correctness finding, not an authorization one; it gets the class,
  not the ceiling. The contrast is pinned in both directions (the same input reaches `error` on the
  capability path and stays `warning` on the plain one) so neither constant reads as arbitrary.
- `docs/observability/health-kv-schema.md` documents the new fields in the same commit.
- Every fix is proven by reverting it and watching its test fail (standing checklist #3).

## 5. Non-goals

The ordering guard, the §6.2 tie rule, `guardedWrite`, and every write path (Andrew's 2026-08-06 hold).
The plain-lens severity ceiling (§4's amendment).
§15.7's drained-head token revive shape. Any repair path or new write class. The divergence audit's own
`AuditUnverified` family. `SweepUnverified` / `SweepFailed` families beyond what shares the fold. Any
contract edit.

---

## 6. Fire brief (build note, 2026-08-24)

Phase 0, compiled at selection from two read-only scouts plus first-hand reads. Every anchor below was
re-verified live against `main@c55f3d7`.

### 6.1 Scope sentence

Verbatim from §1–§2 above: *carry the divergence class the comparator already computes across the three
aggregation boundaries that discard it (`verdictFold.add`, `governingReasonLocked`, the health issue's
counter family), drive blocked severity from the class rather than from streak length alone, and name the
contract-sanctioned remedy in the alert.* Green bar: §4.

### 6.2 Verified touch-list

| # | site | what decides it |
|---|---|---|
| 1 | `internal/refractor/pipeline/reproject.go:204-215` | `divergence` enum — the existing classification, `divergenceNone/Provenance/Content` |
| 2 | `internal/refractor/pipeline/reproject.go:111-138` | `Reprojection` struct — gains the explicit class; `VerdictReason` stays for text |
| 3 | `internal/refractor/pipeline/reproject.go:148-166` | `verdictFold` + `add` — ties on `VerdictBlocked` must resolve by class, not by arrival order |
| 4 | `internal/refractor/pipeline/reproject.go:413` | declined `Delete` → the retraction class |
| 5 | `internal/refractor/pipeline/reproject.go:482-489` | declined `Upsert` → the class from `divergedAs` |
| 6 | `internal/refractor/pipeline/reproject.go:503-508` | tokenless drop → `BlockedUnknown` (no stored watermark was ever consulted) |
| 7 | `internal/refractor/pipeline/sweep.go:118-130` | `Status.Blocked/BlockedStreak/LastBlocked` — gains per-class counts |
| 8 | `internal/refractor/pipeline/sweep.go:207,294` | `blocked map[string]string` → carries the class |
| 9 | `internal/refractor/pipeline/sweep.go:538-546` | `governingReasonLocked` — lexicographic pick replaced by class order |
| 10 | `internal/refractor/pipeline/sweep.go:645-659` | `noteVerdict` — the standing-set transitions (§3's table) |
| 11 | `internal/refractor/pipeline/sweep.go:1147-1153` | publish — per-class counts derived from the set |
| 12 | `internal/refractor/health/lattice_heartbeater.go:396-400` | `CapabilityLensStatus` blocked fields |
| 13 | `internal/refractor/health/lattice_heartbeater.go:502-504` | the plain-lens `LensStatus` siblings |
| 14 | `internal/refractor/health/lattice_heartbeater.go:1135-1148` | capability-lens issue raise + severity ladder |
| 15 | `internal/refractor/health/lattice_heartbeater.go:1937-1943` | plain-lens issue raise + severity ladder |
| 16 | `internal/refractor/health/lattice_heartbeater.go:1229-1237`, `:1887-1892` | the two `entryMetric` / `m[...]` publishers |
| 17 | `cmd/refractor/main.go:842-844`, `:974-976` | the two snapshot copiers — both, or one surface silently zeroes |
| 18 | `docs/observability/health-kv-schema.md` | the canonical schema doc (steward SKILL §4: same commit) |

### 6.3 Precedents to mirror

- **Class-ordered severity, not lexicographic:** `Verdict.severity()` (`reproject.go:98-111`) — an explicit
  integer order with a documented rationale. `BlockedClass` copies that shape exactly.
- **Fail-closed default at the zero value:** `VerdictUnverified` is deliberately the zero value
  (`reproject.go:69`, and `verdictFold.concluded` keeps the default distinguishable from the conclusion).
  `BlockedUnknown` takes the zero value for the same reason.
- **One-sided classification under uncertainty:** `classifyDivergence`'s render-failure arm returns
  `divergenceContent` — "a row that cannot be proven provenance-only takes the louder classification"
  (`reproject.go:249-252`).
- **Severity escalation on a streak:** the existing `capabilityDivergenceErrorStreak` ladders
  (`lattice_heartbeater.go:1097-1112`) — `BlockedUnknown` keeps that shape unchanged.
- **Two-surface symmetry:** every existing sweep counter is published at both `:1229` and `:1887` and
  copied at both `main.go:842` and `:974`.

### 6.4 Increment order

1. **`BlockedClass` + the fold** — the type, its order, and `verdictFold` resolving blocked ties by class.
   Green: `go test ./internal/refractor/pipeline/ -run 'Verdict|Reproject'`.
2. **The three call sites** (declined delete, declined upsert, tokenless drop) stamping the class.
   Green: same package, plus a new per-entry mixed-class test.
3. **The sweep** — set value, `governingReasonLocked` by class order, per-class publish.
   Green: `go test ./internal/refractor/pipeline/ -run 'Sweep'`.
4. **Health** — status fields, both issue paths, both publishers, carry-forward.
   Green: `go test ./internal/refractor/health/`.
5. **`cmd/refractor` copiers + the schema doc.**
   Green: `go build ./...`, `go test ./internal/refractor/... ./cmd/refractor/...`.

Gates: `go build ./...`, `make vet`, `golangci-lint run ./...`,
`STRICT=1 go run ./scripts/lint-conventions.go`, `go run scripts/lint-board.go`,
`go test ./internal/refractor/... ./cmd/refractor/...` and the full suite with `POSTGRES_TEST_DSN` set
(REMOTE.md §3 — without it the gated tests skip and the suite is falsely green).

### 6.5 In-scope gotchas

Health-emission change ⇒ `docs/observability/health-kv-schema.md` in the **same** commit (steward SKILL §4).
New health `Entry` fields ⇒ `health/entry_carry_forward_completeness_test.go` is the gate. No history or
changelog comments (CLAUDE.md). Postgres must be up before any green claim.

Touched component's dossier (`docs/components/refractor.md`), the entries this fire trips:

- *A new health `Entry` field ships with no carry-forward line, so the next status transition silently
  zeroes it* — RETIRED, mechanized by `health/entry_carry_forward_completeness_test.go`. Directly live here.
- *A fail-closed posture proved on the DELIVERY axis is not proved on the PROJECTION axis* — for each
  uncertain state name every consumer the value feeds and state the fail direction of each.
- *New pipeline state without a declared lifetime (registry / latch / armed flag)* — reset, carry, and
  order it at replay, reconnect, tombstone and retry. §3 is that table.
- *A two-layer seam can be green at each layer and broken across it* — the seam here is
  `pipeline.Status` → `cmd/refractor` snapshot → `health.CapabilityLensStatus`; write the seam test with
  the real intervening copy step, both copiers.
- *A soundness claim's stated REASON is load-bearing* — state which direction each severity change fails.
- *An upsert-only reprojection retracts nothing whose key drops out — on the security plane that is an
  over-grant* — the retraction class exists because of this entry.

Standing checklist: (1) new state needs a lifetime → §3. (2) every census is a premise → the class counts
are derived, never accumulated. (3) a negative test needs its positive vector proven first, and every fix
is proven by reverting it. (4) a demoted mechanism needs EVERY obligation enumerated → the old
undifferentiated counter fed two issue paths, two publishers and two copiers; all six are in the
touch-list. (5) one deterministic key, one writer. (6) precedent may carry debt.

### 6.6 Adjacent finds

- `verdictFold.add`'s first-reason-wins on a verdict tie (§1a) is a **defect this fire fixes**, not a
  filing — it is inside the item's own mechanism.
- The plain-lens `LensRepairBlocked` path (`:1937`) shares the defect and is **in scope**: the same counter,
  the same ladder. Fixing one surface and filing the other is the split this fire refuses.
- §15.7's drained-head revive shape stays Andrew's and stays unbuilt; the board row for it is this one, and
  it does not fragment.

### 6.7 Non-goals

§5 above, verbatim.

### 6.8 Scope-diff gate

Parts 6.2–6.4 diffed item-by-item against 6.1. Every touch traces to "carry the class across the
aggregation boundaries, drive severity from it, name the remedy". Touch 17 (the `cmd/refractor` copiers) is
not a widening: without it the new fields are zero at the health surface and increments 1–4 are inert.
Touch 18 is mandated by SKILL §4 for any health-emission change. No adjacent mechanism is substituted: the
guard, the tie rule and the write path are untouched, which is the hold's whole boundary. Declared
dependency re-verified both ways — the fire depends on `classifyDivergence` already shipping (it does,
`6f03b32`), and on nothing else; no unlisted dependency surfaced.

## 7. Close pass — three cold reviews, findings classified

Three cold adversarial reviewers (correctness, security/capability-plane, seam/lifetime), none the
implementer, over the whole item diff.

**Verdicts.** Security: SHIP, no defects — verified the guard, tie rule and every write path are
byte-for-byte untouched (the only write-branch change is `fold.add(...)` → `fold.addBlocked(...)`, which
touches class/reason, never verdict/`Wrote`/`Deleted`/`Converged`), that a meaning-change cannot land in
`BlockedProvenance` (the comparator strips only `projectedFromRevisions`), and that `clampToWarning` has
exactly one call site and cannot reach the capability path. Correctness and seam: SHIP WITH FIXES.

**Fixed this round (all fixes revert-proven by the reviews' own scenarios).**

1. **The tokenless-drop reason named a class it disclaims** (correctness MINOR, seam MINOR — found
   independently). The `!outcome.Committed` branch stamps `BlockedUnknown` correctly but built its reason as
   `"…; " + divergedAs.String() + " unrepairable"`, so a content read-back on that branch produced
   `class=unknown` / `reason="…content divergence unrepairable"` — the exact text-vs-class disagreement the
   item's own invariant forbids. Reachable only via a test adapter (`dropUpsertNoToken` at a nonzero seq); no
   shipped adapter produces `Committed=false` with a real `divergedAs`, since seq==0-with-reader bails at
   `reproject.go:589` and a byte-identical row converges before the upsert. Fixed: the reason now names only
   the block cause it observed (a missing token), never the comparator's kind. The test
   (`TestReprojection_TokenlessDropIsUnknownNotTheComparatorsClass`) previously enshrined the mismatch by
   asserting only `Contains("no ordering token")`; it now also asserts the reason carries no divergence-kind
   phrase.
2. **Two history/changelog comments in test files** (seam MINOR — the repo's most-policed rule, and one
   `lint-conventions` does not catch). *"used to hide inside the same counter"* narrated the pre-diff
   undifferentiated counter; both reworded to present tense.

**Latent, recorded not filed — no live producer, so nothing to fix.** The non-outcome `adpt.Delete`
fallback (`reproject.go:547`) cannot detect a watermark-declined retraction, since a non-outcome adapter
returns no outcome. All four shipped guarded adapters (NatsKV, Postgres, GrantWriter, Protected) implement
`OutcomeDeleter`, so the fallback is unreachable on every guarded target and carries no `BlockedRetraction`
risk today. It is pre-existing write-path shape behind Andrew's frozen boundary, not this fire's mechanism;
a guard here would be building for an adapter shape that does not exist. If a fifth guarded adapter ever
ships without `OutcomeDeleter`, the symmetric `SeqGuarded && !OutcomeDeleter` fail-closed check (mirroring
the upsert-side guard at `reproject.go:589`) is where it belongs.

**Dossier classification (`docs/components/refractor.md`).** The two findings map to existing entries, so
nothing new is minted and nothing promotes to a gate: (1) is *"an authoring gate and its runtime resolver
must agree"* in its text-vs-field form (the class field is the resolver, the reason string the advisory
text); (2) is the standing no-changelog rule. Both were caught by cold review, not the author — the value
the close pass is designed to deliver.
