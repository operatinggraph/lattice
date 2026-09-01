# The batched-read primitive the confinement-walk row asks for does not exist favorably on the pinned substrate — a refutation record, one safe dedup, and named revive economics

**Status: ✅ RATIFIED (Winston-adjudicated, per the 2026-08-20 delegation) — no fork, no frozen-contract
edit; two adversarial passes run, both folded (§9)** · Designer fire 2026-09-01 · Winston
**Board row:** `[Processor] A correctly-bounded confinement walk still blows the wall — no batched-read
primitive exists` (lattice.md, ★★ / S–M) · **Relates:** `verticals.md` — *Every front-desk POS write fails
about half the time, mid-order* (Café, ★★, `🚧 blocked-on` this row; its "fork resolved PLATFORM" stamp is
corrected below)

---

## For Andrew (informational — Winston-adjudicated lane)

**What this concludes, in three lines.** The row prescribed a Starlark `kv.ReadMulti` (or a per-op-class
wall budget). Grounding refuted both, and then refuted — against the pinned `nats-server@v2.14.0` source
and live measurements — all three substrate transports that could have made the walk's reads cheaper
(`multi_last` listing, `STREAM.INFO` subjects listing, batched filtered direct get): each is worse than
the shipped watcher lister on a binding axis the quiet-host benchmark cannot see. What ships is the one
verified demand-side dedup (café's doubled `leaseapp_unit` walk, −4 RTs on every real POS tap), a
recorded hazard map that stops the next fire from re-proposing any of the refuted shapes, and a named,
evidence-gated revive path. No platform mechanism; no contract edit; no budget change.

---

## 1. The demand row, clause by clause

> *"cafe/wellness-domain's `worksAt_covers` needs 2–3 sequential `kv.Read`/`kv.Links` round trips per
> containment hop, all wall-clocked; `starlark_kv.go` exposes only single-key `Read`/`Links`, though the
> engine's dispatch path already has `KVGetMulti`. Live: café staff Charge 9/20 timeouts vs resident 2/20.
> Resolves `verticals.md`'s front-desk-POS fork PLATFORM."*

- **"2–3 round trips per containment hop"** — confirmed in structure, undercounted in total: the staff
  Charge path with `menuItemKey` (every real POS tap) is **20–23 sequential live RTs** (§3.1), because
  the op walks two independent topology chains and resolves `leaseapp_unit` twice.
- **"all wall-clocked"** — confirmed (`starlark_kv.go`'s `ContextFromThread` reads; 250 ms wall,
  `starlark_runner.go:20`).
- **"no batched-read primitive exists"** — true, and after grounding, **the correct state, not a gap**:
  every batched shape the pinned substrate offers loses to the shipped sequential/watcher shapes on a
  load-relevant axis (§4). The prescription is refuted, not unbuilt.
- **"the engine's dispatch path already has `KVGetMulti`"** — true; §4.1 is why importing it into the
  script path is a regression.
- **"Resolves the front-desk-POS fork PLATFORM"** — corrected: the platform half of that fork is
  refuted; the addressable cost is package-side (§5), and the residual is host capacity (§6).

## 2. What exists today (and stays)

- **`kv.Links`** (`internal/processor/starlark_kv.go`) — Contract #2 §2.5.1's bounded, paged, live
  enumeration; `connLinkLister` = `KVListKeysFilter` (watcher listing leg) + exact-key `KVGetMulti`
  value leg (round-trip-collapse Fire 1), the only non-test `ScriptLinkLister`, wired only by the
  Processor (`step4_hydrate.go:444`).
- **The watcher listing leg** — nats.go `ListKeysFiltered` → `WatchFiltered(…, IgnoreDeletes(),
  MetaOnly())` (`nats.go@v1.52.0/jetstream/kv.go:1432-1459`): an ephemeral ordered consumer per call.
  Quiet-host medians 2.5–14 ms/call (§3.2) — the number that motivated this fire, and the number §4
  shows every alternative fails to beat where it matters. Its consumer-create rides the server's
  **priority** API queue (`jetstream_api.go:894-938`), a load posture none of the alternatives match.

## 3. Grounding

### 3.1 Demand-side round-trip census (independent, falsify-briefed; executable as
`grep -rlE 'worksAt_covers|enforce_workplace|require_workplace|actor_holds_operator' packages/` + per-op count)

| Package · op (staff path) | Live RTs | Notes |
|---|---|---|
| cafe-domain `Charge` **with `menuItemKey`** | **20–23** | `leaseapp_unit` runs twice — for `require_workplace` and inside `location_covers`' locality bound (`ddls.go:1129-1131`, `:1159-1161`, different predicates, same lease key both times); `menu_item_served_at` + `location_covers` add a second chain |
| cafe-domain `Charge` (hand-keyed) / `OpenTab` / `VoidCharge` / `Settle` | 9–13 | single chain |
| cafe-ledger `CreditCafeAccount` | 14–18 | `actor_holds_operator` runs twice |
| wellness-domain staff writes | 8–17 | seven double-gate sites; two ORDER-LOAD-BEARING (§5.2) |
| lease-signing `DecideLeaseApplication` | 12–16 | double operator call |
| clinic-domain appointment ops | 8–12 | single-gated since 2026-08-29; correspondingly lower live failure (~1/10) |
| clinic-reminders series ops | 9–13 | resolver eager for operators — but see §5.2: the collapse is NOT inert everywhere it looks inert |
| loftspace-ledger / maintenance-domain | 8–15 | double operator call |

### 3.2 Measured constants (live stack, 66,130-key / 275k-sequence `core-kv`, quiet noisy dev host; medians, 21–31 iters; run-to-run variance noted)

| Shape | Median (range across sessions) |
|---|---|
| single `KVGet` | 1.5 ms |
| watcher lister, "out" hub filter (today) | 2.5–6.9 ms |
| watcher lister, "in" filter | 14–28 ms |
| `multi_last` hub filter | 3.0 ms |
| `multi_last` mixing key families | 11.6 ms |
| `STREAM.INFO` subjects, "out" / "in" | 0.7 ms / 2.3 ms |
| batched filtered direct get (`next_by_subj`+`batch`), "out" / "in" / sparse broad | 5.2 / 16.7 / 18.3 ms |

Re-run recipe: ~80-line `package main` in a temp dir inside the module, `substrate.Connect` + raw
`nats.Connect` with `deploy/nkeys/processor.nk`, benchmark against `core-kv`. **Quiet-host medians were
the trap of this fire twice over** — §4's refutations all live on axes (queue priority, scaling
dimension) the bench cannot see; treat these numbers as floors, never as the decision.

## 4. The refutation record — every batched transport, against the pinned source

Pin: `nats-server@v2.14.0`, `nats.go@v1.52.0` (`docs/vendors.md`). Each entry names the mechanism, the
citation, and the axis on which it loses.

### 4.1 A Starlark `kv.ReadMulti` (the row's prescription) — refuted

`fileStore.MultiLastSeqs` (`filestore.go:3769-3900`) resolves a multi-subject request by walking message
blocks backwards until every matched subject resolves, iterating each block's per-subject state — cost ∝
the **block distance between the matched subjects' last writes**, not the subject count. Mixed-family
batches (a walk level's vertex + link keys) measured **11.6 ms vs 3 ms** for the sequential reads they
replace, and the axis grows with bucket churn forever. A per-key `KVGet` is a flat index seek. The
builtin would also touch five enforcement/stub sites (`derive_reads.go:179` + its
`TestDeriveReads_StubsCoverEveryRealMember` drift guard, `lint-conventions.go:391,2454`,
`docs/components/processor.md`) and falsify Contract #2 §2.5.1's live "exactly one such primitive"
clause. Revive trigger: a measured wall breach dominated by a *clustered, same-family* read set.

### 4.2 A per-op-class wall budget (the row's other branch) — refused

The ratified honest-cost posture (`script-live-read-round-trip-collapse-design.md` §1): make reads cost
what they should; the wall is NFR-P4's bound. Widening one class reduces no server work — under the
causal saturation it taxes every co-resident op. §6 routes what a breach means *after* the waste is
removed.

### 4.3 `multi_last` wildcard listing with 413→lister fallback — refuted (this fire's own first draft)

Variant (i) of the ratified §8.1 rejection: the 413 lands only after the server resolves and walks the
whole matched set (`stream.go:5847-5857`; `filestore.go:3896` tests `maxAllowed` after the walk), so
every >1,024-degree page pays both paths — and the repo's multi-page walks (erasure seal ×160 pages,
unbind ×64) live exactly there. Serving values for the whole matched set per page also violates
§2.5.1's Lazy bullet ("only for the pages it pulls") with degree/`limit` amplification — live `limit=1`
call sites exist (`wellness-domain/ddls.go:3818,3838`). Killed by adversarial pass 1 (§9).

### 4.4 `STREAM.INFO` subjects-filter listing — refuted (this fire's second draft)

Three independent disqualifiers, pass 2 (§9):
1. **Every `$JS.API.STREAM.INFO.*` rides the server's deliberately deprioritized API queue**
   (`jetstream_api.go:868-872,894-938` — "This effectively means infos are deprioritized"; drained only
   when the priority queue is empty), and past `JSDefaultRequestQueueLimit = 10,000` backlog the info
   queue is **silently discarded wholesale** (`:874-891`) — the client sees nothing, burns the whole
   250 ms wall, then a fallback lister inherits a dead context. The swap could invert under exactly the
   saturation it targets, while measuring 3× faster on a quiet bench.
2. The in-direction filter (`lnk.*.*.<rel>.<t>.<id>`) forces `SubjectTree.match` to iterate every child
   at each wildcard level (`stree/stree.go:392-399`) — cost ∝ the total live `lnk.` keyspace, which is
   monotone non-decreasing (nothing hard-deletes links), i.e. the same age-degradation that refuted 4.1.
3. The API reply bypasses `max_payload` entirely (`norace_1_test.go:4293-4331` asserts a 100k-subject
   reply through a 512-byte-cap connection), so the imagined oversized-reply fallback can never fire;
   a 16k-degree hub is a ~1.3 MB reply + 16k-entry unmarshal inside the wall, bounded only at 100k
   subjects (~8.8 MB). Offset pagination also re-sorts a fresh snapshot per request
   (`jetstream_api.go:2032-2062`) — silent member loss across pages, plus a nil-`Subjects`/full-`Total`
   response shape at clamped offsets that turns a naive loop infinite.

### 4.5 Batched filtered direct get (`next_by_subj` + `batch` + `max_bytes`) — refuted by measurement

The only shape on the right transport (the stream's own direct-get subscription — no API queue at all)
with real server-side paging (seq cursor, `batch`/`max_bytes` bounds honored per page,
`stream.go:5920-6010`). But `store.LoadNextMsg(filter, wc, seq)` steps the **stream sequence span**, not
the matched set: this bucket holds 66k live keys across 275k sequences, and the measured page costs
(5.2 ms "out" where the watcher is 4.2 ms; 16–18 ms elsewhere) are span-proportional and grow with
total bucket history. Equal or worse than the watcher on every measured shape, worst on old buckets.

**Conclusion:** the watcher lister — an ephemeral consumer on the priority queue whose server-side
last-per-subject computation is done once per call — is the best listing transport this substrate
version offers for this access pattern. The "wasteful lifecycle" premise did not survive: each
alternative relocates the cost to a worse place. (A future NATS pin that ships a bounded, filtered,
priority-served listing verb re-opens §4; so does the shelved hard-delete verb, which changes 4.4's and
4.5's growth axes.)

## 5. What ships, and what is recorded as hazard

### 5.1 Inc A — cafe-domain: memoize `leaseapp_unit` per execution (S) — the one verified dedup

`Charge` resolves `leaseapp_unit(existing.data.get("leaseAppKey"))` at two sites on **different
predicates** (`ddls.go:1129-1131` staff-confinement; `:1159-1161` catalog-locality), same lease key by
construction (`existing` is assigned once before either). A hoist would add cost to paths that pay
nothing today; the correct shape is a **function-local memo dict threaded as a parameter** — NOT a
module-level `_MEMO = {}`: the pinned go.starlark.net's `Init` happens not to freeze module globals, so
a module-level dict works *today by version accident* and becomes a runtime `frozen hash table` fault on
an upstream bump that `Validate` (Init-only) cannot catch. −4 RTs on every real POS tap (20–23 → 16–19).
`leaseapp_unit` is not a pinned shared guard helper (`lint-package-standard.go:447-450`), so no digest
or floor gate is touched. Manifest + `Version` bumps; `DIFF_BASE=<base> go run
./scripts/lint-package-version.go`. Owned test: the existing confinement tests stay green unmodified,
plus one new assertion that the memoized path issues a single `appliesToUnit` enumeration (the
`ScriptReadRecord.Enumerations` list is the observable).

### 5.2 Recorded hazards — the dedups that LOOK mechanical and are not (do not sweep these)

- **The five-package double-operator-gate sweep** (~3 RTs/site): wellness alone has seven sites, two
  ORDER-LOAD-BEARING — `wellness-domain/ddls.go:3933-3939` and `:3719-3725` document that confining on a
  caller-supplied key before the membership proof reopens a cross-tenant probe. The `workplace_exempt`
  lint derivation (`lint-conventions.go:2745-2843`) and the blocking per-helper copy floors + body
  digests (`lint-package-standard.go:447-474,566-625`) gate the shapes involved. Per-site work with
  those citations in hand, or not at all.
- **clinic-reminders' eager site resolver**: three call sites are inert to collapse
  (`visitseries.go:778,860,874`), but `SetVisitSeriesSite`'s resolved set **doubles as the membership
  whitelist that binds operators** (`:986-988` — "ONE enumeration, so the site a caller may pick can
  never be one the guard did not consider"; the `:1008` check runs for everyone) — gating the resolver
  behind an operator check either kills the operator's escape-hatch op or reopens the invent-a-
  `practicesAt` write `:1010-1018` forbids. And any call-site operator pre-gate double-walks
  `actor_holds_operator` on the staff path (+3–5 RTs on the failing class). Net value ≤ 0; dropped.
- **A merged covering-walk** (`worksAt_covers` + `location_covers` share one frontier expansion on the
  POS path): `worksAt_covers` is body-digest-pinned across 9 corpus copies with a blocking floor
  (`lint-package-standard.go:448,467`) — the gate exists because walk variants drift into authz bugs
  (`:391`'s recorded shipped bug). A cafe-local third walk variant is the exact hazard the pin refuses.
  Only a corpus-wide, all-nine-copies change could do this legitimately — a dedicated fire, priced
  against its conditional −1 enumeration/op saving. Not worth it now; recorded.

### 5.3 No contract edit, no new state, no gate changes

Nothing in this design touches Contract #2, the `kv` module surface, the live-read budget or its
rationale, any lint regex, or any pinned helper. The read-drift ratchet is unaffected (it is a
one-directional guard — fires on *new undeclared* reads, never on fewer; `read_drift_guard.go:88-115`).

## 6. The residual, routed honestly

After Inc A, staff POS Charge is ~16–19 necessary, distinct, bounded authz reads — no known waste
remains at S/M cost. The 250 ms wall firing on that op under a saturated dev host (four verticals + a
suite on one box) is then a **host-capacity signal, not a script-cost signal**, in the same family as
the resident path's own 2/20 and the "suite reddens under parallel load" board row (owner: Whetstone).

**Acceptance + revive economics (owned by the Inc A build fire):** re-run the PO's paced 20-op probe
(staff Charge vs resident, same host, 1/s) after Inc A on a loaded stack. If staff still misses parity
by more than the load story explains, THAT probe — not a quiet bench — is the phase-0 evidence for
reviving round-trip-collapse **Fire 2** (per-execution key-set snapshot; ratified-and-shelved, shelf
unchanged by this design), and the measurement must instrument the axis §4 names for whatever shape it
proposes (queue placement, scaling dimension), not medians.

## 7. Reconciliation with the existing mental model

- *Didn't round-trip-collapse Fire 1 fix the read cost?* It batched the value leg. This fire asked
  whether the listing leg (or the single reads) could batch too; the answer is the refutation record.
- *Doesn't the row's `no-pattern:` mean the primitive is wanted?* A `no-pattern:` names what a
  particular solution shape would need — re-derived here, the need dissolves: the prescription's own
  mechanism is slower than what ships. The row is corrected rather than built.
- *Does this override the verticals row's "fork resolved PLATFORM"?* It corrects it with grounding:
  the platform transports were priced and refuted; the addressable share was package-side all along
  (clinic's 2026-08-29 single-gating already demonstrated this — its failure rate is half café's).
- *New state?* One function-local memo dict, lifetime = one script execution, threaded explicitly —
  reset/carry/order trivially answered by locality.

## 8. Decomposition for the Steward

| Inc | Scope | Size | Review depth |
|---|---|---|---|
| A | cafe-domain `leaseapp_unit` memo (function-local, threaded) + enumeration-count test + version bump | S | Standard |
| — | board-row correction + the §6 live probe at next loaded-stack opportunity | XS | — |

Gates: `go build ./...`, `make vet`, `golangci-lint run`, `go test` on cafe-domain + processor,
`lint-package-version`, all `scripts/lint-*.go`.

## 9. Review record

**Pass 1 (adversarial, opus, 2026-09-01) — on the first draft (`multi_last` listing + three-increment
package sweep): NOT READY.** Blockers: the shape was variant (i) of the ratified §8.1 rejection
(mischaracterized in the draft); >1,024-degree multi-page walks pay full server resolution before every
fallback; whole-set value reads violate §2.5.1's Lazy bullet (live `limit=1` sites). Majors killed the
`kv.ReadMulti` generalization (block-spread transfers to accreting "in" hubs), the hoist-shaped café
dedup (mismatched predicates; the `location_covers` short-circuit would turn a tombstoned-unit DENY into
an ALLOW), and the five-package sweep (7× undercounted; order-load-bearing sites; lint derivation +
copy-floor gates). → Shape withdrawn; sweep removed; dedup reshaped to the memo; hazards recorded in
§5.2.

**Pass 2 (adversarial, opus, 2026-09-01) — on the second draft (`STREAM.INFO` listing): NOT READY.**
Blockers: STREAM.INFO rides the deprioritized API queue with silent wholesale drops past 10k backlog
(the payoff plausibly inverts under the target load); the clinic-reminders collapse breaks
`SetVisitSeriesSite`'s operator-binding membership whitelist. Majors: leading-wildcard psim subtree scan
grows with the link keyspace forever; the oversized-reply fallback can never fire (API replies bypass
`max_payload`); offset pagination loses members across per-request snapshots; the "fall back on any
error" path burns the whole wall on a silently-dropped request. Minors verified the permission envelope
(processor `ExtraPubAllow "$JS.API.>"`, no INFO deny), the memo's safe shape (function-local — the
module-global variant is a version accident), and both hard-delete-parity claims. → Shape withdrawn;
findings folded as §4.4, §5.1's memo shape, §5.2's clinic-reminders entry.

**Post-pass grounding (this fire, after pass 2):** the batched filtered direct get (§4.5) was proposed,
measured live, and refuted on the span-proportional axis without a third draft. **The surviving design
contains only content verified by the two passes or by direct vendor/measurement grounding; the
adjudication gate is discharged.**
