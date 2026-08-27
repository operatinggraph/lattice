# Lattice designer triage — the ten `📐 needs designer pass` rows + component consolidation

**2026-08-27, Winston (Andrew-directed session; the sibling pass:
[verticals-designer-triage-2026-08-27.md](verticals-designer-triage-2026-08-27.md)).** Mandate: the
same skeptical pass over every lattice-lane `📐 needs designer pass` row — real or imagined, prefer
the smallest shape — plus consolidation of same-component items. Method: eight read-only grounding
agents briefed to **falsify** each row's premise, lead spot-checks of every load-bearing claim, and
one live Health-KV read (via the dev `lattice.nk` NKey) where a verdict hinged on live state.

**Outcome across ten 📐 rows: two retire outright, two dissolve to `📋 ready` with the shape
resolved, one re-points at an already-ratified sibling design, one becomes a decided `📋` retirement
recipe, and four consolidate into two genuinely design-worthy 📐 items (both now ★★★). Board: 15
component-maintenance rows → 11; two shelved feature pairs merge.**

| Row | Verdict |
|---|---|
| [Processor] NFR-S6 membership hardcode (§2) | **Merge** with the payload row — a dependency pair; Andrew's "dissolve by simplifying" direction partially falsified |
| [Processor] NFR-S6 payload prices the quantum (§2) | Real, **under-stated** — a movable-threshold timing oracle, ★★→★★★; merged item stays 📐 |
| [Loupe] newPackage proposal binding (§3) | **Dissolves to 📋** — the durable receipt is already computed and discarded |
| [identity-domain] credentialindex residue (§4) | Real, but **its design already exists, ratified** — re-point, `🚧 seq` |
| [Weaver] issueKeyGapConfig strands (§5) | **Merge** — same root as the sweep-coverage row; no-pattern half-falsified |
| [Weaver] never-fired gap gets no sweep coverage (§5) | Real ★★★; its own remedy sentence is **unimplementable as written**; merged 📐 |
| [Refractor] varlength anchor derivation (§6) | The genuine design item — ★★→★★★ on live evidence; 📐 kept |
| [Refractor] "wedged rebuild / event loss" (§6) | **Retire** — refuted hypothesis re-filed post-close; live re-check today shows no divergence signal |
| [Loom] heartbeat 1,024-cap breakdown (§7) | Wrong altitude — **lifecycle, not scan-shape**; dissolves to 📋 |
| [Processor] Reads-template `:type` segment (§8) | **Retire** — `derive_reads` serves the sole consumer package-only; revive trigger named |
| [Weaver/Loom/Refractor] control-operator zero holders (§9) | **Decided: retire the role** — 📋 ready recipe |

## 1. The pass's recurring findings

Same disease as the verticals pass, plus three new staleness modes: (a) a **scheduled observer
re-filed a hypothesis 43h after the close that refuted it** (Lamplighter, §6); (b) a row's fix
**already existed as a deferred increment of a ratified sibling design** nobody connected (§4);
(c) a row's own **remedy sentence was unimplementable** — it named the arm when the hole was the
enumeration source (§5). And `derive_reads` (Contract #2 §2.5 class (g)) dissolved its second and
third rows in one day (§8 here; the verticals computed-reads fork).

## 2. [Processor] The NFR-S6 tail — one item, two increments, payload first

**Membership cannot dissolve.** All three behaviors `isNFRS6Operation` gates are class-scoped by
nature, none safe-universal: the closed declared-read set applied universally refuses most of the
corpus (a nil/empty descriptor resolver admits nothing; ~70 hand-built submission sites); the
wire-shape collapse universally destroys every error message; the 50 ms quantized release
universally adds latency to every rejection and overflows the shared 1024-deferral bound into
silent drops. So the literal "de-hardcode by simplifying, membership dissolves" direction is
falsified — and no existing declaration coincides with membership (Ceremony and Sensitive are
*anti*-correlated with the two members; `derive_reads` is per-DDL, wrong granularity). Moreover the
hardcoded list is the **fourth instance of a ratified core-owned op-name map**
(`privilegedLaneAllowlist`, `reservedOperationTypes`, the Gateway's `rawCredentialCarveOut` — a
byte-identical two-op set kept for a different reason), and the sibling's stated rule is "core owns
the policy; packages own only the assignment." Package-declared membership would be
package-withdrawable — the fail-open direction the whole fire was built against.

**The payload axis is the sharper half and is under-stated at ★★.** It is a timing oracle, not
DoS: with `T(cause) = base(cause) + C·P` and release at `ceil(T/Q)·Q`, an attacker tunes payload
size `P` to position the quantum boundary onto the sub-millisecond per-cause delta and reads one
bit per request — the floor's own "a number the caller already controls by padding carries no
information" comment is the claim §20.8 flags as false. It re-opens the identity-existence oracle
two shipped fires (`624d445`, `c69aa4a4`) were spent closing. Smallest fix: a **class-scoped byte
cap at admission** (`len(env.Payload)`, ~4 lines, ~4–8 KiB against identity-domain's own
4096-per-field precedent); explicitly refuse server-side `InputSchema` validation (new dependency,
new cache path, O(payload) work inside the very window it protects).

**Why one item:** the cheap payload fix *consumes* the membership lookup — fixing (2) alone adds a
fourth consumer of the hardcoded list and makes (1) worse. And one candidate shape resolves both
with the net line reduction Andrew asked for: **release unconditionally at `receipt + Q`,
fail-closed drop past Q** (the posture the 1024-bound already uses) — the "which quantum" channel
disappears entirely, closing the payload axis and every future axis with no cap and no membership
question. That trades availability on the interactive error path for the security property — a
fork the eventual design flags for Andrew. Board: merged row, ★★★ M, 📐 kept
(`no-pattern: submitter-priced work inside the release quantum`). The design owes one measurement:
`C` (decode ms/KiB) to prove `C·P_max ≪ Q` post-cap.

## 3. [Loupe] newPackage proposal→install binding — 📋, the receipt exists and is discarded

The gap is real and **live** (the Studio's `SubmitCapabilityProposal` path is operator-granted and
fully ungated — the dormant `BRIDGE_CAPABILITY_AUTHOR` flag gates only the model-backed producer),
and the same ambiguous name+version heuristic decides both the apply 409 **and** the mark-applied
recovery. But no platform primitive is missing: `InstallPackage` already receives the Processor's
`OperationReply` — real `RequestID`, `OpTrackerKey`, committed per-key `Revisions` — and drops
everything but `.Status`; the only platform-native "did my install commit" record is the 24h-TTL
tracker. Resolved shape (M): (1) thread the reply through `ApplyResult`; (2) immediately after
apply, stamp `{packageKey, installRequestId}` as a **no-TTL aspect on the proposal's own vertex**
via a narrow op mirroring `MarkCapabilityProposalApplied`'s grant shape; `targetInstall` reads that
first, name+version stays the legacy fallback. Deriving the requestId from the proposal NanoID is
safe against the `contentRequestID` precedent bug (proposals are write-once; a retry is identical
content, which dedup should collapse). The crash window between install and receipt degrades
gracefully to today's heuristic — the same accepted class as the existing two-commit boundary.

## 4. [identity-domain] credentialindex residue — the design already exists; `🚧 seq`

The structural claim survives scrutiny (no link type → the lens engine and the attestation
categorically cannot enumerate the class; an anchored arm requires a bound edge, and the unbound
seed path is a whole-bucket scan refused at the 1M cap — the CLI's prefix scan is fine only because
it is one-shot). Both live producers already mint `boundTo` atomically; the residue is the
pre-link legacy corpus plus `ReconcileCredentialBinding`'s never-linked population when an endpoint
is later erased — reaped today by two tested but unscheduled operator CLIs. **The fix is already
scoped in a ratified design:** `credential-binding-plane-lifecycle-design.md` §9's deferred
reachability increment ("a new link per credential written by four ops, a backfill, and a
class-preserving change to the shred's tombstone loop"), and the same doc's ratification section
names "credentialindex reachability made structural" as **precondition (i) of the unblocked
erasure-pattern direction (option C)**. So the `no-pattern` tag is wrong in a new way: the pattern
isn't missing — its ratified design was never connected to this row. Board: drop 📐 →
`🚧 seq: credential-binding-plane-lifecycle §9's reachability increment`. XS ride-along for
whichever fire picks it up: the attestation's runtime `Description` (the operator-visible text)
never states the exclusion its own source comment names — one wording edit.

## 5. [Weaver] One root, one merged row: the sweep enumerates state keys, not work

The two rows share one root, nameable without force-fitting: **the sweep's work set is whatever
weaver-state keys past dispatches left behind, not the work the target's current declared shape ×
currently-projected lens rows says exists.** Every symptom follows — a never-dispatched gap has no
mark and no count, contributes zero keys to the one `KVListKeys(weaver-state)` that feeds every
leg, and is invisible forever (the ★★★ row; and because the lane-1 durable survives restarts, a
quiet row gets exactly ONE evaluation ever, so a single transient decline is permanent — that is
why this is genuinely the root of the verticals clinic row); and the `issueKeyGapConfig` latch can
only be retired per-entity because no leg ever holds the target's whole column population.

**Two corrections to the rows as filed.** The ★★★ row's remedy sentence is unimplementable:
relaxing `sweepCount`'s `count.Count != 0` test changes nothing — there is no key for the pass to
iterate; the hole is the enumeration source. And the ★ row's `no-pattern: per-target
observed-column set` is half-falsified: declared-set reaping (`target.Gaps`, already the orphan
authority on the `__effect` and mark families) retires two of the three issue codes alone; the
observed set — needed only for `GapWithoutPlaybook` — is *computed* in the same walk, not a new
stateful primitive.

**The merged mechanism (the remaining design fire's map):** one new per-target sweep leg — for
each registered target, `KVListKeysPrefix(weaver-targets, "<targetId>.")` (server-side filtered;
paging via `KVListKeysFilter` designed in, not retrofitted), build the observed-column set and the
dispatchable-work set in one walk, subtract the pass's `listed` marks/counts so tracked gaps stay
owned by existing legs, run `sweepCount`'s arm-(n) gate ladder (extracted, not re-derived) on the
remainder, and reap `gap:` config latches whose column is in neither `target.Gaps` nor the observed
set. State keys demote from enumeration source to memory. What the design fire still owes: pacing
for a markless population an order of magnitude larger than today's (the `SweepOrphanWarmup` gate
is the precedent), soft-tombstone filtering on the prefix list, and a live check of the four
candidate reasons lane 1 missed the 26 entities originally (unregistered-at-delivery, `__control`
disabled, non-bool column → RowDataError, `planGap` Nak cycle). Board: one row, ★★★ M, 📐 kept
(`no-pattern: sweep work derived from declared gap columns × projected rows`). The independent
`GapActionSpec.OptionalReads` row stays its own S (all downstream plumbing already ships; five
touch points: `definition.go` field, `build.go` emit, `registry.go` parse, `strategist.go` resolve,
two package declarations; two named decisions — `ActionCatalogEntrySpec` symmetry, and the authored
`GapActionArtifact` subset legitimately omits it).

## 6. [Refractor] Varlength anchors are the real item; the "wedged" row retires

The "wedged rebuild / real event loss" row was **filed 2026-08-25 — ~43h AFTER `e63cff5`** measured
the identical symptom cluster throughput-bound (0.235–0.57 msg/s drains, ~17–50h ETAs) and refuted
"wedged"; the platform's own `evalRebuildWedged` detector (error at ~10 min of zero drain) never
fired. Its `CapabilityCoverageDivergence`-at-error headline was almost certainly a **latched
republication**: a rebuild-suppressed sweep deliberately keeps republishing its last completed
pass's verdicts (that's why `CapabilitySweepStalled` exists), and the error latch needs only a
2-pass streak. **Live re-check this session (Health KV, 2026-08-27 20:53):** no
`CapabilityCoverageDivergence` issue exists; the visible state is exactly the varlength mechanism —
`edgeManifestReadGrants` + `capabilityServiceAccess` (both varlength lenses) still `rebuild in
flight` with sweeps suppressed 15h16m, and ~19 lenses lagging 8k–65k. Retired to the Done log;
residue folds into the varlength row as consequence.

**The varlength row is the board's co-top designer target, raised ★★→★★★ on consequence:** the
static refusal (`hopindex.go:627` — intermediate nodes make a varlength walk unsteppable) forces
whole-actor-set re-evaluation per CDC event on 10 of 55 anchored lenses + 6 plain, which is what
starves the auth plane's delivery, sweep verification, and every co-tenant lens for days per
rebuild — observed live today. Three prior designs each disclaimed it with reasons (typed
signatures fixes delivery, not re-execution; the plain-lens terminus reuses the refusal verbatim;
the specimen lens has a security-polarity argument — the deliberately-unlabelled negated position —
any redesign must preserve). The tractable design named for the fire: **bounded per-hop-count
expansion under the existing `DefaultActorMaxDepth` cap** (the projection-plan compiler already
lists bounded varlength as covered — the one ratified precedent nobody connected to `HopIndex`) +
a per-lens rewrite census for the `*0..` family. 📐 kept, ★★★ M–L.

## 7. [Loom] Heartbeat cap — lifecycle, not scan-shape; 📋

The row's two named options both treat the KVGetMulti cap as the problem; it is the trip-wire.
Root cause: the terminal transition deletes only the `.pattern` pin, **never the `instance.<id>`
cursor** — `loom-state` accretes forever (9,260+ mostly-terminal single-step flows), a gap the
architecture diagnosed twice (the op-vertex-pruner design *mistakenly* asserted instances delete at
terminal; the Chronicler design corrected it and explicitly deferred the prune as "a follow-on, not
scoped here" — never filed). Two dissolving facts: a **running-only index already exists** (pins
are deleted in the terminal batch, so listing `instance.*.pattern` IS the live set — no body
fetches, no cap exposure), and the Chronicler's durable `loomFlowHistory` makes pruning safe
(Loupe probes live state only for rows the Chronicler still shows running). Resolved shape (📋,
S–M): (1) prune/TTL terminal `instance.<id>` records — the same idiom the bucket already uses for
`deadline.<id>`, with `InspectInstance`'s not-found made a matchable sentinel; (2) point the
heartbeat counter **and the equally-exposed `ListInstances` control RPC** at the pin index, with a
per-tick deadline (today's deadline-free fallback can stall the whole heartbeat doc past its 100s
TTL — a worse failure than the row reported).

## 8. [Processor] Reads-template `:type` — retired; `derive_reads` serves the only consumer

Grounding sharpened the row twice. The template grammar is confirmed **non-contract** (zero
placeholder-vocabulary hits in `docs/contracts/`; the direct precedent —
`descriptor-floor-template-coverage-design.md` §7 — shipped the `:id` machinery Winston-adjudicated
on exactly that basis), and a `:type` operator's honest cost is multi-file (floor + tests + the S1
lint's placeholder parser, plus **three independent client template engines** that would each need
it before any client could faithfully expand the declaration). But the sole consumer doesn't need
any of it: DetachObject's payload already carries the owner as a full key, and a `derive_reads(op)`
in objects-base's own DDL — Contract #2 §2.5 class (g), shipped, precedented — splits `targetKey`
and supplies the 6-segment link-key read server-side. Package-only, zero engine/lint/client
changes, and it *shrinks* what a future descriptor-driven client must know. The redundant
`ownerType` payload field (§22's candidate B) is rejected outright: unchecked client-supplied type
drifts into a silently dishonest declared-read set with no fail-closed backstop. **Row retired**
(Done log); revive trigger: an op needing client-side, pre-submission-predictable type extraction —
none exists (class-D census: 0). This also **un-blocks the verticals AttachObject row**: Inc-C is
package work (`derive_reads` in objects-base + wire the Dispatch), not Lattice-lane.

## 9. [Weaver/Loom/Refractor] control-operator — decided: retire the role

The intended holder (Loupe's operator identity, per the ratifying design's §3.5–3.6) was redirected
to the broader `consoleOperator` **one day after the role shipped**, and two later Andrew-ratified
designs each explicitly declined to route a real actor through `control-operator` when the chance
arose. No consumer references it outside its own package, its tests, one ephemeral build-tagged
fixture, and a lint allowlist entry. Enforcement is live and fail-closed either way
(`consoleOperator` is a strict superset; capability mode is the only permitted posture). The
duplication the row worried about is the platform's chosen convention, CI-pinned on both sides
(`demo-operator` is a third instance). Retirement recipe (📋 ready, XS): a `control-authz` version
bump dropping the `RoleSpec` + the sole-`control-operator` permissions and demoting the five
Personal-Lens `GrantsTo`; `Upgrade`'s ratified `diffManifest` tombstones the orphaned
role/permission/`grantedBy` vertices (the `9718dac7` mechanism — and its revive arm makes this a
two-way door); drop the dead `trustedToolRoles` lint entry in the same change. Winston-decidable:
no fork, no contract, no behavior change for any live actor.

## 10. Consolidations outside the 📐 set

- **[capability-author]** plaintext-laundering + admission-holes: merged — same design doc, same
  ★★★ L, same dormancy (`BRIDGE_CAPABILITY_AUTHOR`), same Andrew shelving; the doc enumerates the
  holes.
- **[natsperm]** server-published-bytes + unscoped `STREAM.CREATE`: merged — same doc (§8/§8.4),
  same root (NATS server-originated publishes carry no permission check; `RePublish` is the shared
  primitive), both revive triggers kept.
- **[Pkgmgr]** additive-apply + reinstall: considered, **not merged** — different mechanisms,
  different docs, distinct named revive triggers a merge would blur.
- The NFR-S6 pair (§2) and the Weaver pair (§5) are the in-📐 merges; the Refractor fold (§6) is a
  retirement-into, not a merge.

## 11. Adjudication

No committed contract edits and no forks decided in this pass — the one fork surfaced (the
`receipt + Q` fail-closed release, §2) is *flagged inside* the merged row for its eventual design
doc to put to Andrew, alongside the finding that his "membership dissolves by simplifying"
direction is unachievable in its literal form. Everything else — the two retirements, the two
dissolutions, the re-point, the role retirement, the merges — is Winston-adjudicated under the
2026-08-20 delegation (no contract surface, no product fork; the retired rows carry named revive
triggers). Remaining 📐 inventory after this pass: exactly two items, both ★★★, both genuinely
design-worthy — the NFR-S6 tail (§2) and the Weaver sweep-enumeration root (§5) — plus the
Refractor varlength item (§6) as the co-top target. That is what the designer lane should pick up
next, in that order of tractability: Weaver, then varlength, then NFR-S6.
