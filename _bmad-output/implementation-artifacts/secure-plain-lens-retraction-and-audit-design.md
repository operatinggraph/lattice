# A Secure plain lens retracts and audits like any plain lens — an orphan is a key fact, not a plaintext fact

**Status: ✅ RATIFIED (Winston-adjudicated, per the 2026-08-20 delegation) — 2026-09-04.** Designer fire
2026-09-04 · Stream 2 (Lattice) · board row: *[Refractor] A Secure plain lens has no orphan healer*. No
architectural fork, no frozen-contract edit (§8 shows the proof). Adversarial pass run 2026-09-04 and
folded (§13: 21 findings, 4 blocking — every one changed a section below). Size **M** (three increments;
the third carries the gate and the package debt).

> **Rewritten after the adversarial pass.** Everything below is the corrected design. §13 records what the
> pass refuted; the refuted text is gone, not banner-covered.

---

## 0. For Andrew

**What it does, in two lines.** A Secure Lens (a plain, RLS-protected read model with decrypt-at-projection
columns — twelve exist) today gets *no* retraction when a required neighbour drops out and *no* standing
correctness verdict of any kind, because two refusals name "plaintext" as their reason while guarding code
paths that never touch plaintext. This design lifts both refusals on that grounding — the Secure lens joins
the plain corpus's shipped retraction transport (the licensed neighbour-anchor derivation) and its shipped
detector (the divergence audit, comparing under a mask that excludes the secure columns) — and adds the gate
the business plane was missing: a plain lens whose row existence depends on a required neighbour must carry
a neighbour-retraction transport, refused at activation otherwise.

**Fork / contract check — neither.** The audit keeps its ratified "never writes" posture (§7 row 3 prices
the alternative and leaves it deferred behind the trigger the audit design named). No Gateway / D1 / Vault /
multi-cell / HA question is touched. No `docs/contracts/*` sentence changes (§8): the design makes Contract
#3 §3.10's *"erasure must reach the read models … re-projecting the affected lenses is convergent"* true
for the one row it is false on today (an orphan is never re-projected), and writes the retraction Contract
#6 §6.14 already promises for a protected table (a seq-guarded soft tombstone).

**What I corrected in the row and in my own first reading.** Four premises, each grounded in §2:
1. The mechanism is not *"a missed or failed retraction event."* It is structural: on a plain lens a
   drop-out caused by a **neighbour** event (a neighbour vertex tombstoned, a link two hops out removed, a
   neighbour's aspect flipping a WHERE) has exactly two retraction transports — the whole-target key diff
   (`DiffRetraction`) or the licensed per-anchor derivation — and a Secure lens is refused both.
2. The sweep does not *"enrol auth-plane actor-aggregate lenses only."* It enrols every actor-aggregate lens
   on three structural conjuncts; the auth plane only sets its cadence. Secure lenses are outside it because
   the package installer refuses `SecureColumns` on any `ProjectionKind` — a Secure lens is plain by
   construction.
3. The audit's Secure refusal is real for 10 of 12; two refuse one conjunct earlier, on `DiffRetraction`.
   And the refusal's *reason* is false about its own code: the audit's recompute (`executeFullForAudit`)
   never runs the decryptor, so it never re-derives plaintext. What the conjunct actually prevents is a
   verdict that reads `stale` forever (ciphertext computed vs plaintext stored). The string also attributes
   its rule to Contract #3 §3.10, which constrains *who* produces plaintext and says nothing about
   background jobs or request contexts (§4.1).
4. *"`renewalsRead` today"* is five: `renewalsRead`, `clinicAppointmentsRead`, `providerAppointmentsRead`,
   `clinicEncountersRead`, `visitSeriesRead` (§3.1). `clinicPatientsRead`'s required set is empty — every
   neighbour hop is OPTIONAL — so the `DiffRetraction` it kept as a "continuous healer" heals a different,
   rarer channel than the one its comment describes (§4.3); it stays, and the comment is corrected.

---

## 1. Problem and intent

### 1.1 The gap, mechanically

A plain (non-actor-aggregate) lens retracts a stored row through three paths in
`internal/refractor/pipeline/evaluate.go`:

| Trigger | Path | Reaches |
|---|---|---|
| the **anchor** vertex is tombstoned | read-free anchor Delete (`AnchorDeleteResult`, `evaluate.go:262-268`) | one-row-per-anchor lenses |
| an event **seeded from the anchor** (its root, its own aspects, a link with the anchor as an endpoint) after which the anchor no longer projects | presence-check Delete (`AnchorProjectionKey`, `evaluate.go:330-338`) | one-row-per-anchor lenses |
| a **neighbour** event (a vertex, aspect or link not seeding from the anchor) | (a) `applyDiffRetraction` if declared (`:339-351`), or (b) the licensed derivation substituting seeded evaluations per derived anchor, each of which then runs the presence check (`anchor_derivation_plain.go:530-552`) | (a) any shape on a target it owns; (b) lenses the licence admits, up to the derived-anchor cap (§4.2) |

`evalPlainLinkReprojection` seeds from **each endpoint** of a link (`dispatch.go:280-333`), so a required
link directly on the anchor is covered by the second row. Everything one hop further is a neighbour event.
Without (a) or (b) a neighbour event is a whole-corpus rescan that upserts every row still matching and
emits **no Delete** for the one that stopped: the row stays, served under RLS, until something else touches
its anchor.

`renewalsRead` (`packages/lease-signing/renewal_lenses.go:355-398`) requires four hops off its anchor:
`renews`, then `applicationFor`, `appliesToUnit`, `manages`. A landlord's `manages` link removed, the
tenant identity tombstoned, the application tombstoned — each is a neighbour event, each drops the renewal
out of the match, and none retracts the row.

### 1.2 Why a Secure lens is the sharpest instance

Two properties make the orphan worse on a Secure lens than on any other plain lens:

- **It is served plaintext PII under a grant that may no longer hold.** `authz_anchors` on the orphan still
  names the tenant and the landlords as of the last projection.
- **It is plaintext no erasure reaches.** Contract #3 §3.10: *"A key destruction … is complete when no
  projected read model still holds the plaintext … re-projecting the affected lenses is convergent."* A
  shred of the tenant's identity key scrubs a Secure column by re-projection (the piiKey aspect event
  re-executes the lens and the decryptor writes `null`). An orphan row is, by definition, a row no
  re-projection produces — so its `tenant_name` stays plaintext after the shred, indefinitely.

And a Secure lens has **no detector**: the audit refuses it (`audit.go:975-977`), the sweep cannot hold it
(§2.2), and nothing else revisits a stored row off-request except an operator `Rebuild`. Live, all ten
Secure-refused lenses publish `auditEnrolled: false` and nothing else (§2.4).

### 1.3 Intent

The row asked for *"an off-request orphan healer for a Secure plain lens."* Split at the noun phrases:

- **off-request** — a mechanism that does not wait for the next CDC event. Delivered as the **detector** (the
  audit, enrolled under a mask, publishing `retained`) — not as an automatic writer, for the reason the audit
  design ratified and this design re-prices (§7 row 3). The automatic repair stays behind the trigger that
  design named, which becomes measurable on Secure lenses for the first time.
- **orphan healer** — something that removes the row. Delivered as the **transport** every other plain lens
  has: the licensed neighbour-anchor derivation, which retracts on the event that orphans, plus the
  `DiffRetraction` fallback where the shape cannot partition.
- **for a Secure plain lens** — the first customer, but the invariant holds for the whole business plane,
  so the design carries the gate that keeps it closed there (§4.4).

---

## 2. Grounding ledger

### 2.1 The two refusals, and what each actually guards

| Refusal | Text | Site | What the code path does |
|---|---|---|---|
| audit enrolment | *"it is a Secure Lens, and a background comparison must not re-derive plaintext outside a request context"* | `audit.go:975-977` | `auditAnchor` calls `executeFullForAudit` → `executeFullForActorCosted` → `executeFullForActorAttempt` (`evaluate.go:456-498`). `applySecureDecrypt` is called from `evaluateForEntry` (`evaluate.go:88`) and the two actor fan-out handlers (`dispatch.go:210`, `:353`) only. **The audit never decrypts.** Its computed row carries the raw ciphertext envelope map the cypher returned for `tenant.name.data`; the stored row carries the decrypted string or `null`. Without the conjunct, `rowsComparable` (`reproject.go:738`) would report every row `stale`, forever. |
| plain-derivation licence | *"it is a Secure Lens, whose columns a per-anchor re-entry would decrypt twice over"* | `anchor_derivation_plain.go:329-331` | True, and a plumbing fact: `evaluatePlainDerivedAnchors` re-enters through `evaluatePlainFromVertex` (`pipeline.go:1509`) → `evaluateForEntry`, which decrypts, and returns into the outer `evaluateForEntry`, which decrypts the already-decrypted string → `"value is string, not a ciphertext envelope map"` → Terminal → **redacted to `null`** (`secure.go:163-187`, `:197`). The seam is documented at `:506-516`: *"Any change that makes the licence's Secure conjunct advisory re-opens this seam."* |

Neither reason is a soundness bound on the Secure lens. The first guards a comparison; the second guards a
call graph. Both are closed by construction below (§4.1, §4.2), not by declaring them advisory.

**Order of the chain (a lifted refusal reveals the next conjunct).** Live, every Secure lens the derivation
would consider refuses at the licence's *first* conjunct — the audit — with the audit's own reason
interpolated: `no divergence audit is enrolled on it, so nothing standing would re-test a row a narrowed
reprojection left behind (it is a Secure Lens, …)` (refractor.log, 2026-09-03T21:22:25, `renewalsRead`,
`clinicAppointmentsRead`, `providerAppointmentsRead`, `visitSeriesRead`, `applicantRosterRead`;
2026-09-04 `clinicEncountersRead`, `wellnessIdentitiesRead`, `cafeIdentitiesRead`). Lifting only the audit
conjunct would surface the licence's own Secure conjunct next; §4 lifts both, then reads the conjuncts that
follow (`$now` / `$projectedAt`, `ProjectsOneRowPerAnchor`, the index) per lens in §3.2.

### 2.2 The sweep — structurally out, not auth-plane-gated

`SetSweepPlan` has one non-test caller, `projection/driver.go:779` inside `InstallActorAggregate`;
enrolment is `sweepEnrolment` (`driver.go:303-326`): a derivable `KeyPrefix`, a round-tripping key, a
`PrefixKeyLister` target. `authPlane` only picks the interval (`driver.go:296-301`). A Secure lens never
reaches that installer: `internal/pkgmgr/bucketguard.go:206-212` refuses `SecureColumns` together with any
`ProjectionKind`. The clinic comment (`packages/clinic-domain/lenses.go:334-337`) and the with-alias design
(§13 Inc 3) both state the auth-plane version; the build corrects the comment (§9).

### 2.3 What can still lose a retraction that *did* have a transport

Named so the residue is honest, not so the design chases it:

| Channel | Mechanism | Disposition |
|---|---|---|
| decrypt failure on the event | redaction, never a dropped event (`secure.go:150-162`, fork F2 of the key-custody design) | closed |
| Terminal evaluation error | DLQ + Ack (`dispatch.go:414-417`); reachable only from a malformed link body (`:284-293`) and an actor-aggregate key collision (`evaluate.go:1073`) | recorded in the DLQ stream; not a plain-lens path in practice |
| transient write failure | retry queue (`results.go:244-260`), `MaxDeliver: -1` on the durable; exhausted → DLQ (`failure/retry.go:196-228`) | recorded |
| retry entry across a restart | the queue is in-memory (`failure/retry.go:60-65`); the message was Acked at enqueue (`results.go:362-368`) | **lost silently** — the one unrecorded channel; the audit's `retained` class is its detector, and a `DiffRetraction` lens's next neighbour event its healer |
| reorder between an Upsert and a Delete | protected tables are guarded (`read_path_adapters.go:284` `SetGuarded(true)`): the Delete is `UPDATE … SET is_deleted=true, projection_seq=$n WHERE … AND projection_seq < $n` (`postgres.go:586-593`); the Upsert's `ON CONFLICT … DO UPDATE … WHERE EXCLUDED.projection_seq >` (`:124`, `:286`) | closed by the watermark, one guard per direction |
| a retraction that landed but a reader ignores | every reader of a protected table honours the tombstone: the RLS policy's leading `NOT is_deleted` (`rls.go:195-206`), `GetRow` and `ListKeys` on a guarded adapter (`postgres.go:415-417`, `:360`) | closed — the removal signal exists at the granularity every consumer tests |
| the derived Delete's presence probe reads through RLS | `derivedRowIsLive` → `ProtectedAdapter.GetRow` → `PostgresAdapter.GetRow`, whose own doc says the read works because the projector pool connects as a superuser that bypasses row security (`postgres.go:467-473`). A hardened projector role on a `FORCE ROW LEVEL SECURITY` table would read every row absent and drop every derived Delete silently (`anchor_derivation_plain.go:544-546`) | **open, named**: the probe's role assumption is a premise of the whole derived-retraction class, not of this design; §12 carries it |
| `Rebuild` | `resolveTruncate` forces truncate on a guarded, truncatable target (`rebuild.go:329-344`); `ProtectedAdapter` is both (`read_path_adapters.go:404,408`) | heals orphans as a side effect — the operator verb today |

### 2.4 Live state (shared dev stack, 2026-09-04, `health.refractor.rfx-4914b152dac5`)

```
nats --nkey=deploy/nkeys/lattice.nk kv get health-kv health.refractor.rfx-4914b152dac5 --raw
  lensLiveness: 104 lenses · status: degraded
  auditRefusal histogram:
    45  actor-aggregate or personal (the sweep's)
    10  it is a Secure Lens, and a background comparison must not re-derive plaintext …
     7  it uses target-diff retraction, whose semantics a single-anchor evaluation would misread
     3  no single derivable anchor pattern
  enrolled: 39
  leaseApplicationsRead: auditEnrolled, alert=diverged, divergentRows={stale:10}, listing 64
  renewalsRead / clinicAppointmentsRead / … (10): auditEnrolled=false, no other verdict field
```

Postgres, the three protected tables this design touches first:

```
docker exec lattice-postgres psql -U lattice -d lattice -Atc "select count(*), count(*) filter (where is_deleted) from read_renewals"          → 1|0
… read_clinic_patients   → 2|0
… read_lease_applications → 59|2
```

The Secure tables hold one and two rows on this stack; there is no live orphan to point at, and the design
does not claim one. The harm is a mechanism with a routine trigger (§1.1), not a measurement.

**`leaseApplicationsRead` is the only Protected plain lens audited today, and its verdict is suspicious.**
The audit's disagreement lines (`refractor.log`, `pipeline: audit: the projection disagrees with the graph`,
201 for this lens) report `stale` on **8–10 of every 10** anchors audited, pass after pass, and the log does
not name the column. Near-universal staleness on a table the apps read correctly is the signature of a
*representation* artifact in the comparator (a text[] vs `[]any` rendering, a number's type after the JSON
round trip), not of eight stale applications — and the masked audit inherits whatever the comparator does.
This is Inc 1's first Phase-0 premise (§15.1): open one anchor, name the column, and if it is an artifact
fix the comparator before the mask is built on it.

### 2.5 The Secure Lens population is closed at twelve

`SecureColumns` is declared at exactly twelve sites (`grep -rn "SecureColumns: \[\]pkgmgr.SecureColumn{" packages/ | grep -v _test`),
all `Adapter: "postgres"`, all `Protected: true`, none with `ProjectionKind`, none with `$actorKey`;
`internal/pkgmgr/build.go:471-531` serializes each `LensSpec` 1:1 (no generated lens can inherit
`SecureColumns`). Two carry `DiffRetraction: true` (`clinicPatientsRead`, `landlordLeaseApplicationsRead`).
No Secure lens references `$now` or `$projectedAt`.

---

## 3. Censuses — every count is a premise

### 3.1 Required-neighbour existence across the plain corpus (hand classification, 2026-09-04)

Population: every `pkgmgr.LensSpec{` under `packages/` (non-test) with no `ProjectionKind`, no `$actorKey`,
not `Personal`. The hand pass found **63**; the shipped pin `plain_scanroot_corpus_census_test.go` holds
**65** rows, and the retraction-transport census takes its population from that pin in both directions — its
prose header carries no count (a number nothing re-derives). *(Settled at build, 2026-09-05: the pin is the
population, this table was the hypothesis, and it missed one member — see the gap-set table.)* Note the pin's `hasNeighbour` is
a *weaker* predicate (any relation referenced, OPTIONAL included) than this design's *existence depends on
a required neighbour*, so the two fields coexist by name.

Classifier: a lens's row *existence* depends on a neighbour when a non-`OPTIONAL` `MATCH` reaches a
non-anchor node, or a `MATCH`/`WITH` `WHERE` reads a non-anchor variable **or an alias derived from one**
(§4.4). Result: **32 YES** (the hand pass said 31 — it missed the kernel `capabilityReadWildcardGrants`, a
required `holdsRole` hop plus a WHERE on the role); of those **23 carry no `DiffRetraction`** — the gap set —
and 9 do. The kernel member: `capabilityReadWildcardGrants` · **auth** · `actor_read_grants` · `closureHolds`;
the kernel `LensDefinition` surface carries no `DiffRetraction` flag, so T2 is not expressible for it. Its
`holdsRole` unwire IS retracted today (the link arm re-evaluates the anchor endpoint and the presence probe
fires); what has no transport is a `role` vertex tombstone or an edit to the operator role's
`canonicalName`, which seed from the role and rescan unseeded.

| Gap set (22) | Plane | Secure | Target | Pinned scan-root verdict |
|---|---|---|---|---|
| renewalsRead · clinicAppointmentsRead · providerAppointmentsRead · clinicEncountersRead · visitSeriesRead | business | ✓ | dedicated protected tables | closureHolds (5) |
| leaseApplicationsRead · identityCredentialBindingsRead | business | | dedicated protected tables | closureHolds |
| cafeLedgerHistory · clinicLedgerHistory · ledgerHistory · wellnessLedgerHistory · frontDeskBookings · frontDeskVisits · frontDeskBookingHistory · wellnessMembers | business | | dedicated nats-kv | closureHolds |
| oneBillRentEntries · oneBillCafeEntries | business | | one-bill-history (shared by 4) | closureHolds |
| oneBillClinicEntries · oneBillWellnessEntries | business | | one-bill-history (shared by 4) | **rootUngrounded** — *"pattern headed by position N (leaseapp) is not reached from the anchor — it binds by bucket scan"* (live log 2026-09-03T21:22:25) |
| consoleOperatorReadGrants · demoOperatorReadGrants | **auth** (GrantTable) | | actor_read_grants | closureHolds; licence refuses the plane |
| capabilityRoleIndex | **auth** | | capability-kv (shared) | closureRefused; key minted by an envelope wrapper (§4.3) |

Covered set (9, `DiffRetraction: true`): duplicateCandidates, providerSites, wellnessMemberAccounts,
landlordUnitsRead, objectIdentityAttachmentsRead, landlordLeaseApplicationsRead (business, dedicated
targets); providerIdentityReadGrants, patientIdentityReadGrants, staffReadGrants (auth plane,
`GrantSource`-scoped).

**Falsification of the row's headline.** `clinicPatientsRead` is **not** in the gap set: `identifiedBy`,
`forPatient`, `practicesAt`, `atSite` are all `OPTIONAL MATCH`; its `WHERE` reads `p.demographics` (the
anchor's own aspect, `clinic-domain/lenses.go:868-874`). Its rows can be orphaned only by an anchor-event
loss — the retry-across-restart channel of §2.3 — which is what its `DiffRetraction` actually heals (§4.3).

### 3.2 The conjuncts behind the two lifted ones, per Secure lens in the gap set

Read before promising the payoff (the licence chain is `anchor_derivation_plain.go:282-345`, headed by
`plainDerivationIndex` at `:111-128`; the audit's is `audit.go:911-997`):

| Lens | `$now`/`$projectedAt` | `ProjectsOneRowPerAnchor` | index complete, single seed label, ≤1 branch | RowReader target |
|---|---|---|---|---|
| renewalsRead | none | ✓ — `plain_scanroot_corpus_census_test.go:169` (closureHolds) with the identifying half at `:327-330`; the with-alias pin (`plain_with_alias_closure_census_test.go:129`, bucket F) proves only closure through the WITH, not identification | rootIndexed, one label, one branch | ProtectedAdapter ✓ |
| clinicAppointmentsRead · providerAppointmentsRead · clinicEncountersRead · visitSeriesRead | none | ✓ (closureHolds) | rootIndexed | ✓ |

All five clear every static conjunct that follows the two this design lifts. Aggregation is not a break for
`renewalsRead`: the seed narrows only the anchor pattern (`seed_nodes.go:172-175`) and the effective
grouping key contains `entityKey` (`grouping_reduction_corpus_census_test.go:176`), so `min(DISTINCT …)` /
`collect(DISTINCT …)` are computed within one renewal's group. The build's Phase 0 re-runs both pins.

### 3.3 Shared-target DiffRetraction is a latent cross-delete

`NatsKVAdapter.ListKeys` lists the **whole bucket**; for a composite key `mapKeys` keeps only keys with the
right segment count (`natskv.go:731-741`), and for a single-column key it keeps **every key in the bucket
verbatim** (`:723-729`). A `DiffRetraction` lens on a bucket it shares would diff every sibling's keys against
its own row set and Delete them. No shipped lens is in that state (§3.1's covered set is dedicated or
`GrantSource`-scoped), and nothing refuses it: `SetDiffRetraction` checks `KeyLister` only
(`pipeline.go:1067`), and `applyDiffRetraction` calls `ListKeys` unconditionally (`evaluate.go:1447`) —
`ListKeysPrefix` exists (`natskv.go:709-721`) but is called only by `Truncate`, the sweep and the
child-prefix path. `one-bill-history` is shared by four lenses with one key shape, so `DiffRetraction` can
never be the one-bill family's transport; `capability-kv` is shared across the auth plane. §4.4 encodes this.

---

## 4. The shape

Four parts; no new component, no new persistent state.

### 4.1 The audit compares under a mask — the Secure conjunct becomes a column exclusion

`auditEnrolment` drops the `secureDecryptor` refusal. `AuditPlan` gains `MaskedColumns []string`, filled
from the installed decryptor's declared columns (a `Columns()` accessor beside `HolderTypes()`,
`secure.go:116`). The audit's one comparison site (`audit.go:737`) calls a new `rowsComparableMasked(stored,
computed, mask)` — `rowsComparable`'s body with the mask threaded to `canonicalJSON`'s existing
`alsoIgnore` (`reproject.go:748-760`) on both sides. `rowsComparable` itself is untouched: it is shared
with the sweep's reconciliation writer through `rowsEquivalent` → `classifyDivergence` (`:362`, `:572`),
which must never learn a mask. Name safety holds: `SecureColumn.Column` is the RETURN alias
(`secure.go:19-22`), the alias is the Postgres column (`renewal_lenses.go:113`, `:381`), and `GetRow` keys
by column name stripping only the three platform columns (`postgres.go:439-443`, `:515-523`). The three
classes then mean:

| Class | Under the mask |
|---|---|
| `missing` | exact — a presence question |
| `retained` | exact — a key question (`AnchorProjectionKey` / `AnchorDeleteResult`, `audit.go:687-771`) |
| `stale` | exact over the **non-secure** columns; a secure column is **unverified** |

The bound is published, not assumed (§5). The plaintext posture is unchanged by construction — the audit's
evaluation path never called the decryptor before and does not now; the comparison simply stops reading a
column it could never compare. **The retired refusal string's contract attribution is corrected, not
dropped silently:** `audit.go:970-977` cites Contract #3 §3.10 for *"a background job with no request
context must not re-derive plaintext"*; §3.10 (`03-mutation-batch-event-list.md:204-207`) constrains
*who* produces plaintext and contains no such sentence. The replacement doc comment states the real rule —
the audit compares under a mask because its recompute yields ciphertext — so the next reader does not
re-import the false premise.

**The sibling conjunct lifts on the same grounding, with a smaller payoff than it looks.** The audit refuses
`DiffRetraction` lenses because a single-anchor row set would be misread by `applyDiffRetraction`
(`audit.go:962-964`) — and `executeFullForAudit` never calls `applyDiffRetraction` (its only call is
`evaluate.go:347`, inside `evaluateForEntryRaw`'s plain arm). Dropping the conjunct enrols seven lenses (the
three GrantTable members stay refused on the auth-plane conjunct, which comes first). But the audit's
`retained` direction is gated on `AnchorProjectionKey` (`audit.go:753`), and a lens that *uses*
`DiffRetraction` is one where that derivation declines: six of the seven are pinned `closureRefused` /
`rootUntypedHop` (`plain_scanroot_corpus_census_test.go:139,147,148,154,168,179`) and gain **`missing` and
*(Amended 2026-09-04: `rootUntypedHop` no longer exists — `objectIdentityAttachmentsRead`'s index is complete and it is refused on its `DiffRetraction` declaration instead; see [untyped-hop-anchor-derivation-design.md](untyped-hop-anchor-derivation-design.md).)*
`stale` only**; `clinicPatientsRead` (closureHolds, `:133`) gains all three. The health entry's
`divergentRows` already carries only the classes that fired, so an absent `retained` on those six reads as
"not detected in this direction" — the doc comment on the conjunct says so explicitly.

### 4.2 The derivation licence admits a Secure lens — the double-decrypt seam is closed, not declared safe

`plainDerivationLicence` drops the `secureDecryptor` conjunct. The seam it guarded is closed at the **one
shared re-entry**: `evaluatePlainDerivedAnchors` (`anchor_derivation_plain.go:534`) re-enters through a
new `evaluatePlainFromVertexRaw` (→ `evaluateForEntryRaw`, no decrypt), so a Secure lens's columns are
decrypted exactly once, by the outer `evaluateForEntry` every stream event already flows through
(`evaluate.go:82-91`). Both producers of that re-entry are covered by the one edit —
`evaluatePlainNeighbourEvent` (`:611-616`) and `evaluateSeededMultiPosition` (`:643-648`, whose live example
`identityCredentialBindingsRead` is Protected) both reach it through `plainDerivationDecide`
(`:666-724`). The two *outer* callers of `evaluatePlainFromVertex` (`dispatch.go:239` aspect arm, `:316`
link arm) keep decrypting; getting this wrong does not error — a second decrypt of a string is Terminal and
`Apply` nulls the column (`secure.go:183`, `:197`) — which is why §10's mutation test exists. Delete results
carry `Row == nil` and are skipped by `Apply` (`secure.go:167-169`). The seam note at `:506-516` is
rewritten to the invariant that now holds: *the re-entrant path never decrypts; the outer wrapper is the
single choke point*. Contract #3 §3.10's live-envelope rule (`:224-227`, the holder's piiKey resolved at
decrypt time, never from a carried copy) is satisfied by construction: the one decrypt per row goes through
`SecureDecryptor.readPiiKeyEnvelope` exactly as today.

**Cost direction, and its bound.** Today a neighbour event on a Secure lens rescans the corpus and decrypts
**every** row; licensed, it evaluates the derived anchors and decrypts **theirs**. That holds up to the
derived-anchor cap: `plainDerivedAnchorCap` (default 64, `anchor_derivation_plain.go:52`, no override in
`cmd/refractor`) — over it, `plainDerivationDecide` returns `declined()` (`:715-723`), which for a neighbour
event is the **unseeded rescan** (`:612-614`): no Delete, and — because the rescan returns through
`evaluateForEntry` — every row decrypted. The walk does not stop at the first anchor: `walkToAnchors`
terminates only at the anchor position (`anchor_derivation.go:418-421`), so from a unit it crosses
`manages` to every co-landlord, back to each unit they manage, and via `app → tenant → app` to every other
application of every tenant it reaches — K is a connected component's renewal set, and a landlord
reassignment (this design's headline trigger) is exactly the event that grows it. Two consequences, both
built in Inc 2:

- the fallback becomes **visible**: `recordDerivationFellBack` (`anchor_derivation_shadow.go:324-330`)
  reaches no health field today; Inc 2 publishes `derivationFellBack` (count) and `derivationOverCapSize`
  (last derived-set size) on the lens's liveness status, so `retractionTransport: "derivation"` can never
  read as a transport that is silently off (§5);
- an over-cap drop-out is **detected**, not lost: the anchor whose row should be gone is a live anchor whose
  seeded recompute produces no row — the audit's `retained` class (§4.1) — and healed by the next event that
  derives it, the operator verb, or the deferred repair (§7 row 3). §12 carries the row.

The licence's remaining conjuncts (audit fresh, `ProjectsOneRowPerAnchor`, no `$now`) apply unchanged, so
the lens is licensed exactly when a non-Secure plain lens would be — including the refused window after a
restart, when `LastPassAt` is zero until the audit's first pass (`audit.go:423-470` restores the cursor, not
the pass time; first pass at `startOffset` ≤ 15 min, `:414-418`). The plain corpus's shipped posture, now
shared.

### 4.3 The package edits the censuses name

| Package / lens | Edit | Why |
|---|---|---|
| clinic-domain · `clinicPatientsRead` | **keep `DiffRetraction: true`**; rewrite the comment block (`lenses.go:326-345`) | the sweep sentence is wrong (§2.2), and the healer rationale names the wrong channel: its OPTIONAL-only shape cannot produce a neighbour drop-out (§3.1), so what the per-event diff heals is an anchor-event loss (the retry queue across a restart, §2.3). That is a rarer channel than the comment says, but it is a real one, and this design ships a detector for it (`retained`, now reachable on this lens) and no repair (§7 row 3). Trading the healer for a detector on a PHI table is a downgrade the design declines; the cost it keeps paying — seeding disabled, so every event rescans and decrypts the table (`rulestate.go:400-402`) — is priced in §7 row 2 and stays until the deferred repair lands |
| one-bill · `oneBillClinicEntries`, `oneBillWellnessEntries` | reverse the trailing pattern so it is headed by a bound variable: `MATCH (id)<-[:applicationFor]-(l:leaseapp)` for `MATCH (l:leaseapp)-[:applicationFor]->(id)` (`lenses.go:116`, `:138`) | the edit is **not** a no-op and is not meant to be: a MATCH headed by an unbound variable is `rootUngrounded` (`hopindex.go:429-437`, `:848`) and its execution is a `ListKeysPrefix("vtx.leaseapp.")` corpus scan (`seed_nodes.go:86`); reversed, the head is bound, `ScanRootHopIndex.Complete` flips to true, `plainDerivationIndex` admits the lens (`anchor_derivation_plain.go:118`), and the scan becomes an adjacency hop. Row semantics are unchanged (same edges, same direction; `DirIn` parses, `ast.go:21`; shipped precedent `lease-signing/lenses.go:1030`) |
| console-operator · `consoleOperatorReadGrants`; demo-operator · `demoOperatorReadGrants` | `DiffRetraction: true` + `GrantSource: "<producer>"` — **recommended, not gate-forced** (auth plane, §4.4) | the licence refuses the auth plane by design; the family's sanctioned transport is `staffReadGrants`'s shape (`service-location/lenses.go:66-73`); legal per `bucketguard.go:172-178`; the diff is `GrantSource`-scoped by `GrantWriterAdapter.ListKeys` → `ListGrantsBySource` (`read_path_adapters.go:226-231`, `rls.go:470-471`), so no cross-delete |
| rbac-domain · `capabilityRoleIndex` | **no edit; named debt on the auth plane** | its key is minted Go-side by `capabilityenv.NewRoleIndexWrapper` at the `isOperationRoleIndexLens` arm (`cmd/refractor/main.go:1669-1679`, `SetEnvelopeFn` only); it has no `Output` descriptor, `SetKeyPrefix` runs only in the actor-aggregate driver (`projection/driver.go:893-894`), so a declared `OutputKeyPattern` would be inert or collide. Consumer: the denial-response builder (`rolesCarryingPermission`, `lenses.go:83-85`) — a stale entry names a role in a denial message. Outside this gate's plane (§4.4); recorded on the auth plane's own ledger by the census pin (`plain_retraction_transport_corpus_census_test.go`), which names both auth-plane debtors with their reasons |

Each package edit bumps the package version and its `Version` constant (`lint-package-version`).

### 4.4 The gate — on the business plane, a required neighbour needs a declared retraction transport

**Scope: the business plane** — `!projection.IsAuthPlane(r)`, the same boundary the audit draws
(`audit.go:915-925`) and for the same reason: an auth-plane verdict belongs to the plane that has a code, a
severity ladder and an escalation for it. The four auth-plane members of the gap set (§3.1) are outside
the gate, named, with their transport story in §4.3: the two operator grant tables take T2, and
`capabilityRoleIndex` + `capabilityReadWildcardGrants` carry none — pinned by name, with reasons, in
`plain_retraction_transport_corpus_census_test.go`. (§7 row 6 records why "Protected tables only" was the
wrong boundary — the business buckets carry the same shape.)

**Rule.** A plain, full-engine, business-plane lens whose row existence depends on a required neighbour
(`CompiledRule.ExistenceDependsOnNeighbour() (depends bool, reasons []string, exhaustive bool)`, beside
`ValidateNoFilteringWhereForConvergence`, `ruleengine/full/ast.go:494`) must satisfy one of:

- **T1 — derivation-eligible statically.** Not a hand-listed conjunct set: T1 **is** the static prefix of the
  licence chain, exposed as one function the licence and the gate both call —
  `(*Pipeline).plainDerivationStaticallyEligible(rs)` (the adapter is read off the pipeline) = `plainDerivationIndex`'s conditions (`≤ 1` branch, index
  complete, no unresolved expansion position, `anchor_derivation_plain.go:111-128`) ∧ the audit's static
  enrolment conjuncts (full engine, exactly one seed label, no actor/envelope, `RowReader`, no
  `$now`/`$projectedAt`, `audit.go:927-997`) ∧ `ProjectsOneRowPerAnchor`. The licence's *dynamic*
  conjuncts (audit fresh, `auditArmed`) are not T1's — and because `auditArmed` is a deployment kill switch
  (`SetAuditEnabled`, `audit.go:66-72`) that voids every T1 transport corpus-wide, a T1 lens on a deployment
  with the audit disarmed publishes `retractionTransport: "derivation (audit disarmed)"` and the heartbeat
  raises a `warning`-tier issue naming the switch. Re-listing conjuncts by hand is how the first draft
  admitted lenses that could never be licensed (§13 F2).
- **T2 — `DiffRetraction` on a target it owns:** a dedicated target, or a `GrantTable` with `GrantSource`,
  or (new, **T2-prefix**) a shared NATS-KV bucket where `OutputDescriptor.KeyPrefix` is derivable and the
  adapter is a `PrefixKeyLister` — in which case `applyDiffRetraction` lists `ListKeysPrefix(KeyPrefix)`
  instead of the bucket. This mirrors two of `sweepEnrolment`'s three conjuncts (`driver.go:303-326`); the
  third, `KeyOwnershipRoundTrips`, is what lets the sweep map a key *back* to an anchor, which the diff never
  does (it compares key sets), so it is not carried. Sharing is decided at **activation, from the lens
  registry** (`cmd/refractor` knows every registered lens's bucket); `pkgmgr` validates one `Definition` at a
  time (`bucketguard.go:145`) and cannot see a bucket another package shares, so no install-time mirror is
  claimed.

**Alias resolution is part of the classifier.** `MATCH (a:x) OPTIONAL MATCH (a)-->(b) WITH a, count(b) AS n
WHERE n > 0` gates existence on a neighbour while the `WHERE` names only an alias no pattern binds; every
`WITH` in this corpus projects aliases (`renewalsRead` projects fifteen). `ExistenceDependsOnNeighbour`
resolves each `WHERE` reference through the WITH-alias environment the closure predicate already builds
(`withAliasEnv`, with-alias design §4.3) to its source bindings; a reference it cannot resolve sets
`exhaustive=false`, and a non-exhaustive answer is a refusal, never a pass.

**Disposition.** A business-plane lens meeting neither T1 nor T2 is refused at activation in
`cmd/refractor/main.go` beside the `ValidateUnanchoredForDiffRetraction` guard (`:1589-1607`) and with its
disposition: the lens does not activate (`logger.Error` + return — dark, never half-armed), and
`RecordError` puts the reason on its health entry. The corpus census pin (§10) makes the runtime refusal a
backstop rather than the place an author learns of it. A lens that passes T1 statically but is dynamically
unlicensed (audit stale, over cap) activates and publishes `retractionTransport: "derivation"` with the
licence's own refusal or fallback counters beside it (§5).

**Population at the gate's landing.** Business-plane gap set: 19 of the 23 (§3.1). After §4.1–§4.3 all
19 pass T1 (the five Secure ones admitted by the two lifts; the two one-bill lenses by the rewrite; the
rest already `closureHolds`). The six business-plane covered lenses pass T2. **Zero business-plane debt ⇒
the gate lands blocking** (the "same design, blocking when the migration leaves zero debt" rule). The four
auth-plane members are outside the gate by its scope, not by omission. The corpus census test pins every
plain lens's `(plane, dependsOnNeighbour, transport)` triple by name — population taken from the pin, not
from §3.1 — with a floor on the count.

### 4.5 Precedents mirrored (do not invent a second shape)

| Part | Precedent | Where the mirror is inexact |
|---|---|---|
| masked comparison | `volatileEnvelopeFields` + `canonicalJSON(alsoIgnore…)`, `reproject.go:295-302,748-760` | new sibling `rowsComparableMasked`; the shared function is not touched |
| published bound | `auditCoverageBasis: "key-type"`, `audit.go:92-100` | — |
| raw re-entry | `evaluateForEntryRaw` vs `evaluateForEntry`, `evaluate.go:82-91` | — |
| activation refusal | the `DiffRetraction` guard's `logger.Error` + return, `main.go:1589-1607` | — |
| pattern rewrite for anchor reachability | typed-relation-signatures §9 (three lenses, 2026-08-22) | — |
| ownership conjunct on a shared bucket | `sweepEnrolment`, `driver.go:303-326` | round-trip conjunct not carried (§4.4 says why) |
| grant-table diff scoping | `staffReadGrants`, `service-location/lenses.go:66-73`; `bucketguard.go:172-178` | — |
| one static predicate, two consumers | the closure predicate shared by retraction, licence and audit (with-alias design §4.1) | T1 reuses the licence's own prefix rather than a copy |

---

## 5. State-lifetime table

No new persistent state. The new *runtime* facts are per-lens, derived, and live on `LensLivenessStatus`
(`health/lattice_heartbeater.go:446-471`, beside `auditEnrolled` / `auditCoverageBasis`) — **not** on
`healthwire.Entry`, which carries only the audit cursor and cycle stamp (`healthwire.go:187,194`). The
`Entry` carry-forward test therefore does not gate them; the only reflection gate over `LensLivenessStatus`
filters to `Sweep*` fields (`cmd/refractor/sweepstatus_test.go:128`), so §10 adds a status-field pin.

| Fact | Created | Reset | Published | Never-written row |
|---|---|---|---|---|
| `auditMaskedColumns` | with the `AuditPlan` at `InstallAudit` | re-read at every enrolment re-check (top of every pass, `audit.go:473-503`) — a hot reload that adds a secure column audits under the new mask | only for an **enrolled** lens; the audit family sits behind the refused-lens early return (`lattice_heartbeater.go:2274-2280`), so a refused lens publishes none — same as `auditCoverageBasis` | a non-Secure enrolled lens publishes `[]`, following `divergentRows`' rule that the container is published even when empty (`:2288-2299`, *"never as null"*); absence means *not enrolled* |
| `retractionTransport` (`derivation` / `diffRetraction` / `diffRetraction-prefix` / `derivation (audit disarmed)` / `none` / `unclassified`) | none — derived on demand, at activation (the gate) and per heartbeat, from the copy-on-write rule snapshot + descriptor + adapter + plane | n/a — a MATCH or INTO hot-reload moves the published value on the next beat with nothing to invalidate; the MATCH reload re-runs the gate and is refused if the new shape owes a transport it lacks | for a business-plane plain lens whose existence depends on a neighbour; absent otherwise (auth-plane lenses publish `CapabilityLensStatus`) | a lens the gate refused never activates and so has no status — its `RecordError` is the record; `none` / `unclassified` are the error-tier backstop for a state the two gates make unreachable |
| `derivationEligible`-gated `derivationArmed`, `derivationFellBack`, `derivationOverCapSize` | first fallback after activation | never; a restart zeroes both — unlike `reconciled`, which the sweep restores from the health entry, these are process-lifetime with no restore, so a low count on a recently restarted instance says nothing about the day | for a lens whose shape admits the derivation (act mode, index complete): `derivationArmed` (the licence's live verdict — `false` reads "declared, currently off", never "no transport"), `derivationFellBack` (every event that took the rescan: failed walk, declined walk, over-cap), and `derivationOverCapSize` only once it has fired (a last size, not a count); nothing for an ineligible lens | an unlicensed eligible lens keeps its accrued count on the wire |

The licence's `LastPassAt` window (§4.2) is existing state with an existing lifetime; this design adds
members to its population, not rows to its table.

---

## 6. Reconciliation with the existing mental model

- **Didn't we already handle this?** Retraction for plain lenses was built in three fires (anchor Delete,
  presence check, `DiffRetraction`) and the neighbour case was handed to the derivation licence
  (plain-lens-neighbour-anchor-derivation-design.md, ratified). The Secure lens was refused from the last
  two on reasons §2.1 shows guard nothing on that lens. The with-alias design (§8) said Secure lenses
  *"gain retraction and audit, never narrowing"* — they gained the anchor-event transports; this closes the
  neighbour one.
- **Does this contradict the audit design?** No: the audit still never writes (§7 row 3), still refuses on
  the auth plane, still publishes its bounds. It drops two conjuncts whose stated reasons were about code
  paths the audit does not execute, and publishes the one real bound (masked columns).
- **Does this contradict the key-custody design's Secure posture?** No: plaintext is produced at exactly the
  one choke point it names (`evaluateForEntry`) and nowhere new; the re-entry fix *reduces* decrypt calls
  below the cap and leaves them equal above it.
- **Does this add state we already keep?** No. The masked-column list is the decryptor's own declaration,
  read back; the transport verdict is derived at activation; the fallback counters expose a tally the shadow
  path already keeps in memory.
- **The with-alias design's Inc 3 amendment** (keep `DiffRetraction` on `clinicPatientsRead`) stands. Its
  comment named the wrong channel and the wrong sweep rule; the mechanism it kept is the right one for the
  channel that exists, until the repair leg lands.

---

## 7. Alternatives

| # | Option | Priced | Verdict |
|---|---|---|---|
| 1 | **Do not have this thing.** Leave the Secure lenses refused; accept orphans on five protected PII tables until an operator `Rebuild`. | The trigger is routine (a landlord reassignment, a tenant erasure, an application tombstone). The row is served plaintext under stale `authz_anchors`, and after a shred it is plaintext no re-projection reaches — Contract #3 §3.10's promise is false for exactly that row, with no detector to say so. | rejected — the invariant forbids it |
| 2 | **Declare `DiffRetraction` on the five Secure lenses** (package-only, ten lines). | Closes the transport on dedicated tables (safe). Cost: `seedAnchorFor` returns `""` for a `DiffRetraction` lens (`rulestate.go:400-402`), so **every** event — anchor events included — becomes a whole-corpus rescan that decrypts every row of the table (the extra `ListKeys` runs on neighbour events only, since an anchor event's `AnchorProjectionKey` succeeds and the diff arm is not reached). On PHI tables that is a plaintext-derivation amplification per event, and it buys no `retained` verdict on the six non-partitioning shapes (§4.1). `clinicPatientsRead` pays it today for a channel that is real (§4.3). | rejected as the primary; kept as T2 for non-partitioning shapes and as the healer for the retry-loss channel |
| 3 | **Let the audit repair `retained` rows on protected tables.** | Two of the audit design's three §8.1 objections do not apply to a protected table: it is dedicated (no shared keyspace) and guarded (a seq-guarded tombstone carrying the pipeline's last-applied seq loses to any racing CDC event, as the sweep's writes do). The third stands: coupling the detector to a writer is the structure whose collapse the audit was built after, and *"the audit never writes"* is ratified. Its named trigger — a sustained non-zero `retained` count — has never fired because no Secure lens could report one. | deferred, same trigger, now measurable — reversing a ratified posture is not this design's to do, and the evidence it would need does not yet exist |
| 4 | **A scheduled `Rebuild(truncate)` of every Secure table.** | O(everything) per tick; truncates an RLS table the apps are reading, so every protected view goes empty for the replay; forbids nothing at authoring time. | rejected — the manual verb is the default for O(everything), and it already exists |
| 5 | **Lift the two refusals (recommended).** | Zero new mechanism: the Secure lens joins two shipped mechanisms whose refusals guarded phantom paths. Plus the gate so the invariant holds for the plane. Bounded by the derived-anchor cap, which the design makes visible and the audit covers. | recommended |
| 6 | **Scope the gate to Protected tables only.** | Smaller blast radius (7 lenses) but the census shows the same shape on business buckets, two of them a bucket-scan cypher and one a shared bucket where `DiffRetraction` would cross-delete (§3.3). The right boundary is the *plane*, not the adapter: the auth plane has its own ladder and the licence already refuses it. | rejected — replaced by the plane boundary of §4.4 |

Each rejected row's objection run back against row 5: (2) "every event decrypts everything" — row 5
decrypts derived anchors only, and every row only over the cap, where it says so; (3) "a writer coupled to
the detector" — row 5 adds no writer; (4) "empties the table" — row 5 writes single tombstones; (6) "blast
radius" — bounded by a census that leaves zero business-plane debt.

---

## 8. Contract surface — builds to, no change

| Contract | Sentence the design serves | Relation |
|---|---|---|
| #3 §3.10 | *"Erasure must reach the read models … it is complete when no projected read model still holds the plaintext … re-projecting the affected lenses is convergent."* (`:196-202`) | **builds to** — an orphan row is never re-projected; retracting it on the event that orphans it makes the sentence true for the last row it was false on |
| #3 §3.10 | *"Plaintext is produced only by the Processor (for Starlark), by an explicit Vault-decrypt consumer (… the read-path-authorized Secure Lens), or by the bridge's external-egress unwrap."* (`:204-207`) | **builds to** — the audit produces none; the re-entry produces it once, at the same consumer |
| #3 §3.10 | the holder's piiKey is resolved at decrypt time, never from a carried copy (`:224-227`) | **builds to** — the single decrypt goes through `readPiiKeyEnvelope` |
| #6 §6.14 | *"A protected table's own Delete is always a seq-guarded soft tombstone."* (`:598-602`) | **builds to** — the transport's write is exactly that Delete |
| #6 §6.2 | ordering guard | untouched |

`auditEnrolled` and the audit's conjuncts appear nowhere in `docs/contracts/`. No sentence a consumer could
observe against the current text changes. Nothing is staged in `docs/contracts/`.

---

## 9. Documentation surface

- `docs/components/refractor.md` — the audit section's conjunct list (drop two, add the mask + its published
  bound and the per-class caveat for non-partitioning lenses); a short paragraph under *Lens lifecycle* for
  the transport gate, its plane boundary and its health fields; the derivation paragraph gains the cap
  fallback's health fields.
- `packages/clinic-domain/lenses.go:326-345` — the comment block rewritten (§4.3).
- with-alias-anchor-closure-design.md §13 Inc 3 — a one-line pointer to this design (the amendment stands;
  its stated reasons are corrected here).
- `docs/components/refractor.md` dossier — one new entry (§13).

---

## 10. Test strategy — every prescribed test is owned by an increment

| Test | Pins | Increment |
|---|---|---|
| Phase-0 premise (not a test): open one `stale` anchor on `leaseApplicationsRead` and name the column | if it is a representation artifact, `rowsComparableMasked` and `rowsComparable` normalize it first, with a pin | 1 |
| `TestAuditEnrolment_SecureLensEnrolsUnderMask` (`audit_enrolment_test.go`) | a pipeline with a `SecureDecryptor` installed enrols; `AuditPlan.MaskedColumns` equals the declared columns; `LensLivenessStatus` publishes `auditMaskedColumns` for an enrolled lens and nothing for a refused one | 1 |
| `TestAuditAnchor_MaskedColumnNeverReadsStale` | stored `tenant_name` plaintext vs computed ciphertext map → no `stale`; a non-secure column divergence on the same row → `stale`; `rowsComparable` (unmasked) still reports the row unequal, so the sweep's comparator is proven untouched | 1 |
| `TestAuditAnchor_SecureLensRetained` | a live anchor whose seeded evaluation produces no row, with the row present → `retained` | 1 |
| `TestAuditEnrolment_DiffRetractionLensEnrols` | the seeded evaluation of a `DiffRetraction` lens enrols; a counter on `applyDiffRetraction` stays zero across a pass; a `closureRefused` shape reports `missing`/`stale` and never `retained` | 1 |
| `TestLensLivenessStatus_NewFieldsAreCarried` | a reflection pin over `LensLivenessStatus` (the `Sweep*`-filtered one widened, `sweepstatus_test.go:128`) naming every field this design adds | 1, 2 |
| `TestSecureDecryptor_DecryptCallsPerEvaluation` (mutation test) | with the seam fixed, `vaultCallsTotal` per **neighbour** event and per **seeded multi-position** event on a licensed Secure lens equals the derived anchors' secure-column count and no `SecureRedaction` is raised; with the raw re-entry reverted, the redaction fires — the test must fail on the old wiring | 2 |
| `TestPlainDerivation_SecureLensNeighbourDropOutRetracts` (e2e, `plain_derivation_act_e2e_test.go` family) | a Secure lens in `act` mode: the neighbour link two hops out is tombstoned → a seq-guarded soft tombstone lands on the target; the row reads absent through `GetRow`. Built against a guarded NATS-KV bucket (the same watermark-carrying soft tombstone `ProtectedAdapter` writes); the Postgres delete path and the RLS read are the ephemeral-stack row's below | 2 |
| `TestPlainDerivation_OverCapFallsBackVisibly` | a derived set over the cap → the unseeded rescan runs, `derivationFellBack` increments, `derivationOverCapSize` carries the size, and the orphan is reported `retained` by the next audit pass | 2 |
| `TestRenewalsRead_ManagesTombstoneStopsProjectingTheRow` (`packages/lease-signing`) | the `manages` link is tombstoned **after** the row exists and the row leaves the matched set — the existing test proves only the fresh projection. `internal/lenstest` exposes no connection, so no `Pipeline` or target exists in that package; the retraction itself is pinned at pipeline level by the e2e above, on a synthetic required-two-hop Secure lens rather than `renewalsRead`'s four-hop shape (its static eligibility is pinned by the scan-root and with-alias census tests) | 2 |
| `TestExistenceDependsOnNeighbour_Contract` (`ruleengine/full`) | required hop → true; OPTIONAL-only → false; WHERE on a non-anchor variable → true; WHERE on an alias of an aggregate over an OPTIONAL neighbour → true; an unresolvable alias or unparsed construct → `exhaustive=false` | 3 |
| `TestPlainDerivationStaticallyEligible_IsTheLicencePrefix` | for every corpus lens, the static predicate agrees with the licence's own static verdicts (the licence called with a fresh, armed audit) — one derivation, two consumers | 3 |
| `plain_retraction_transport_corpus_census_test.go` | every plain lens's `(plane, dependsOnNeighbour, transport ∈ {none, T1, T2, T2-prefix})` pinned by name through `forEachCorpusCypher`; population from the scan-root pin; floor on the count; **no business-plane lens with `(true, none)`**; the auth-plane members pinned as `(auth, true, none|T2)` with their reasons — two operator grant tables T2, `capabilityRoleIndex` + `capabilityReadWildcardGrants` none | 3 |
| `TestActivation_RefusesUntransportedNeighbourLens` | a synthetic business-plane lens with a required hop, no `DiffRetraction`, non-partitioning key → activation refuses, `RecordError` carries the reason | 3 |
| `TestApplyDiffRetraction_SharedBucketListsOwnPrefixOnly` | two lenses on one bucket, one `DiffRetraction` with a derivable prefix: its diff never Deletes the sibling's keys; a single-column key on a shared bucket without a prefix → activation refuses | 3 |
| `TestOneBill_ReversedPatternProjectsSameRows` (`packages/one-bill`) | the reversed `clinicEntriesSpec`/`wellnessEntriesSpec` project byte-identical rows on the package fixture, and `ScanRootHopIndex.Complete` is true for both | 3 |
| `verify-package-*` for clinic-domain, one-bill (+ console-operator, demo-operator if their edits ship) | the lens edits install | 3 |

Ephemeral-stack e2e: the `lease-signing` renewal flow with a landlord reassignment, read back through
`read_renewals` as the tenant (row gone) — the Steward's close pass runs it live on the shared stack and
reads `auditMaskedColumns`, `retractionTransport`, `derivationFellBack` off the health entry.

---

## 11. Measurement and acceptance

| Claim | Instrument | Accept when |
|---|---|---|
| Secure lenses have a verdict | `lensLiveness.<lens>.auditEnrolled` for the 12 | **12 `true`** (every Secure lens is a Protected business table, so none meets the auth-plane conjunct), `auditMaskedColumns` non-empty on each |
| the comparator is honest on a Protected table | `leaseApplicationsRead`'s `divergentRows` after the Phase-0 premise | either the ten `stale` rows are named real divergences, or the artifact is fixed and the count drops — "unchanged" is not an acceptance |
| the licence admits them | refractor.log: no `cannot act on this lens … Secure Lens` line after the first audit pass; the derived-anchor tally counts neighbour events on `renewalsRead` | observed live on the shared stack |
| the transport is on, not silently off | `derivationFellBack` and `derivationOverCapSize` on the five Secure lenses across a day of the LoftSpace PO's traffic | fallback count and the largest derived set reported; a lens whose neighbourhood routinely exceeds 64 is a cap-sizing finding to hand the derivation design, not a transport this design claims |
| the gate holds the plane | `plain_retraction_transport_corpus_census_test.go` | green with zero business-plane `(true, none)` rows; the count floor equals the scan-root pin's population |
| no cross-delete | `TestApplyDiffRetraction_SharedBucketListsOwnPrefixOnly` | green |

---

## 12. What this design does not close

- **A secure column that should be `null` and is not** — an erasure scrub that never reached a *live* row.
  The mask makes that column unverified, and the vault records shred state internally with no query
  surface (`internal/vault/local.go:252-300`; only `Decrypt` reports `ErrKeyShredded`), so the audit cannot
  ask without decrypting. Its transport is the holder's piiKey aspect event, which re-projects a bound-holder
  lens by design (Contract #3 §3.10); the orphan case — the one row that event can never reach — is the one
  this design closes.
- **A drop-out whose derived set exceeds the cap** (§4.2) is not retracted on its event: it is reported
  `retained` by the audit and healed by the next event that derives its anchor, the operator's
  `reproject`/`Rebuild`, or the deferred repair of §7 row 3. The fallback is counted and published; cap
  sizing belongs to the derivation design.
- **Orphans created inside a licence-refused window** (restart, audit suppressed by a rebuild) — same
  disposition. The window is the plain corpus's shipped posture.
- **The in-memory retry queue across a restart** (§2.3) stays the one unrecorded loss channel; `retained` is
  its detector and a `DiffRetraction` lens's next neighbour event its healer.
- **The presence probe's RLS assumption** (§2.3): every derived Delete rests on the projector role bypassing
  row security. Hardening that role is a platform-wide change with this consequence among others; the
  derivation design owns the probe.
- **Auth-plane plain lenses** are outside the gate by its plane boundary: the two operator grant tables take
  T2 (§4.3); `capabilityRoleIndex` and `capabilityReadWildcardGrants` carry no transport and are the plane's
  named debt, pinned in the census. The heartbeat publishes `retractionTransport` for business-plane lenses
  only (auth-plane lenses publish `CapabilityLensStatus`), so that debt is visible in the pin, not on the wire.
- **A MATCH hot-reload that makes a lens neighbour-dependent** re-runs the gate and is refused (the lens
  keeps its running rule); the wire backstop for a classified business-plane lens that nonetheless owes a
  transport and has none is `retractionTransport: "none"` (or `"unclassified"` for a non-exhaustive
  answer) with an error-tier alert — reachable only if that reload gate is bypassed.
- **Derived-anchor cap sizing and the presence probe's projector-role RLS assumption** are the derivation
  design's (plain-lens-neighbour-anchor-derivation-design.md); this section is their record, not a board row.
- **A stored-only table column** (a migration leftover no RETURN alias produces, returned by `SELECT *`) is
  excluded from the audit's comparison: the computed row's key set is exactly the RETURN alias set, so a
  stored-only key can never be a projected column that evaluated null.

---

## 13. Adversarial pass — findings folded (2026-09-04, one cold reviewer, 21 findings)

**Blocking (4), each changed a section:**

- **F1 — the derived-anchor cap silently reverts the transport to the rescan, decrypting every row, with no
  health signal** (`anchor_derivation_plain.go:715-723`, `:52`; `walkToAnchors` expands the component,
  `anchor_derivation.go:418-421`; `recordDerivationFellBack` reaches no health field). → §4.2 carries the
  cap, publishes `derivationFellBack` / `derivationOverCapSize`, §10 pins the fallback, §11 measures set
  size not event counts, §12 carries the row.
- **F2 — T1 was a hand-listed conjunct set missing `len(seedAnchorLabels)==1`, `≤1 branch`, the unresolved
  expansion position and `auditArmed`**, so a multi-walk or taxonomy-expanded lens would pass the gate and
  never be licensed. → §4.4 defines T1 as the licence's own static prefix behind one function; the
  `auditArmed` switch gets a published state and a warning; §10 pins the agreement corpus-wide.
- **F3 — `capabilityRoleIndex` cannot take T2-prefix** (key minted by an envelope wrapper,
  `main.go:1669-1679`; `SetKeyPrefix` only in the actor-aggregate driver), so "zero debt ⇒ blocking" was
  unearned. → §4.4 scopes the gate to the business plane (the audit's own boundary), names the
  auth-plane members and their transport story (§4.3); zero business-plane debt is what the blocking gate
  now rests on.
- **F4 — the banner asserted a pass that had not run.** → this section.

**Must-fold (14):** `rowsComparable` has no `alsoIgnore` and one audit call site; a masked sibling is
specified and the shared comparator declared untouched (§4.1). Six of the seven `DiffRetraction` lenses the
audit lift enrols gain no `retained` verdict — stated per lens (§4.1). Dropping `DiffRetraction` from
`clinicPatientsRead` traded a healer for a detector on PHI — the edit is withdrawn, the comment corrected to
the channel it really heals (§4.3, §6, §7 row 2 also corrected: the diff's `ListKeys` runs on neighbour
events only). The one-bill reversal flips derivation eligibility and replaces a corpus scan — that is the
edit, said so (§4.3). The double-decrypt seam has two producers; the fix at the shared re-entry covers both
and the mutation test now covers both (§4.2, §10). The prescribed grep had three callers, only one to swap,
and `evaluatePlainFromVertex` lives in `pipeline.go` (§15.1, §15.2). The new fields are `LensLivenessStatus`
fields, ungated by the `Entry` carry-forward test; the empty-container rule was inverted (§5, §10). The
population count (63 by hand vs 65 in the pin vs 61 in its header) is stated as a hypothesis the pin
settles (§3.1). `pkgmgr` cannot see a shared bucket — the sharing test moves to activation (§4.4). Aliased
`WITH … WHERE` was a false negative of the classifier — alias resolution is part of it (§4.4). The
`ProjectsOneRowPerAnchor` citation pointed at the wrong pin (§3.2). The presence probe's superuser-RLS
assumption is a residue channel (§2.3, §12); the tombstone/`ON CONFLICT` citation conflated two guards
(§2.3). `ProtectedAdapter` **does** implement `OutcomeDeleter` (§15.3). The only audited Protected lens
reads 8–10 of 10 anchors `stale` on every pass — a comparator-artifact signature, now Inc 1's first premise
(§2.4, §10, §11).

**Editorial (3):** single-column `mapKeys` has no filter at all (§3.3); T2-prefix mirrors two of the sweep's
three conjuncts, said why (§4.4, §4.5); the retired refusal string's contract attribution is false and is
corrected rather than dropped (§4.1), and §3.10's live-envelope rule is named as satisfied (§4.2, §8).

**Verified true by the pass** (kept as the design's load-bearing claims): the audit path never decrypts and
never diffs; `seedAnchorFor` returns `""` for a `DiffRetraction` lens; `ListKeys` is unscoped; protected
tables are guarded and seq-tombstoned with `NOT is_deleted` readers; both plain arms seed from the anchor
endpoint and flow through the one decrypting wrapper; the mask is name-safe; aggregation does not break the
seeded evaluation of `renewalsRead`; the adjacency walk reaches the renewal from the unit; no import cycle;
the operator grant-table edit is legal and cannot cross-delete; `clinicPatientsRead` has no required
neighbour; twelve Secure lenses, none referencing `$now`; the contract quotes are verbatim and unchanged.

**Dossier entry (for `docs/components/refractor.md`, the item-close review appends it):** *A conjunct's
stated reason can be about a code path the conjunct's own consumer never takes.* Two refusals cited
"plaintext" against an evaluation path (`executeFullForAudit`) that never calls the decryptor, and a third
cited the diff against a path that never diffs; a fourth (the licence's) was a real plumbing seam described
as a soundness bound. Check: for every enrolment/licence conjunct, name the function the consumer actually
calls and grep it for the thing the reason fears; a reason that names a different function is a claim
about the wrong consumer. And the corollary the pass added: **a transport with a cap has a fallback, and
the fallback needs a health field** — a shadow-path counter (`recordDerivationFellBack`) that reaches no
published surface is a transport that can be silently off.

---

## 14. Decomposition for the Steward

Three increments, each independently shippable and green. **Posture-changing: Increment 2** (it turns a
write path on for five PII tables) — full review pass. Increments 1 and 3 are sized normally.

**Increment 1 — the masked audit.** The `leaseApplicationsRead` premise first (§10 row 1).
`AuditPlan.MaskedColumns`, the `Columns()` accessor, `rowsComparableMasked`, both conjuncts dropped from
`auditEnrolment` with their doc rewritten to the real bound (and the false contract attribution corrected),
`auditMaskedColumns` on `LensLivenessStatus` behind the enrolled branch, the status-field pin,
`docs/components/refractor.md` audit section. Green means: on the shared stack, twelve Secure lenses enrol
with a mask; the seven `DiffRetraction` lenses enrol with the per-class caveat; the Protected comparator's
verdict is explained.

**Increment 2 — the licence admits a Secure lens.** `evaluatePlainFromVertexRaw` swapped in at the one
re-entry (`anchor_derivation_plain.go:534`), the seam note rewritten, the licence conjunct dropped with its
refusal string retired from the log latch, `derivationFellBack` / `derivationOverCapSize` published. Tests:
the mutation test first (both producers), then the e2e, the over-cap pin, the package-level renewal
retraction. Green means: a Secure lens in `act` mode retracts a two-hop drop-out with one decrypt per derived
anchor, and an over-cap event is counted where an operator can see it.

**Increment 3 — the gate and the package debt.** `ExistenceDependsOnNeighbour` with alias resolution,
`plainDerivationStaticallyEligible` shared by licence and gate, the `auditArmed` state + warning, T2-prefix
in `applyDiffRetraction` + the activation-time sharing check from the registry, the activation refusal, the
corpus census pin (population from the scan-root pin, header count corrected), the one-bill rewrite with its
fixture pin, the `clinicPatientsRead` comment rewrite, the recommended operator grant-table edits, version
bumps, the with-alias §13 pointer, the dossier entry. Green means: the census test pins the plane's
population with zero business-plane `(true, none)`; `verify-package-*` green for every edited package.

Order is fixed: 2 needs 1 (the licence's first conjunct is the audit); 3 needs 2 (T1 admits the five Secure
lenses only once the licence does).

---

## 15. Fire brief (for the Steward's Phase 0)

### 15.1 Premises to re-derive, each gating the increment that rests on it

| Premise | Re-derive by | Gates |
|---|---|---|
| the ten `stale` rows on `leaseApplicationsRead` are an artifact or are real | read one audited anchor's stored row (`read_lease_applications`) against its seeded recompute; name the column | 1 |
| `executeFullForAudit` never decrypts and never diffs | `grep -n "applySecureDecrypt\|applyDiffRetraction" internal/refractor/pipeline/*.go` — callers are `evaluateForEntry` (`evaluate.go:88`), the fan-out handlers (`dispatch.go:210,353`), and `evaluateForEntryRaw`'s plain arm (`evaluate.go:347`) | 1 |
| exactly one re-entry, two producers | `grep -rn "evaluatePlainFromVertex(" internal/refractor/pipeline/*.go` → three non-test callers: `anchor_derivation_plain.go:534` (**swap this one**), `dispatch.go:239` and `:316` (outer wrappers — **keep**); `plainDerivationDecide` is reached from `:611-616` and `:643-648` | 2 |
| the cap and its fallback | `anchor_derivation_plain.go:52`, `:715-723`, `:612-614`; `grep -rn FellBack internal/refractor/health/` → nothing today | 2 |
| the five Secure gap lenses clear the following conjuncts | run `plain_with_alias_closure_census_test.go`, `plain_scanroot_corpus_census_test.go`, `grouping_reduction_corpus_census_test.go`; read the five rows | 2, 3 |
| `clinicPatientsRead` has no required neighbour | read `clinicPatientsReadSpec` (`lenses.go:868-874`): every non-anchor `MATCH` is `OPTIONAL`; the `WHERE` reads `p.` only | 3 |
| shared-bucket `ListKeys` is unscoped, and single-column keys are unfiltered | `natskv.go:692-698`, `:723-741`; `evaluate.go:1447` | 3 |
| `capabilityRoleIndex` has no descriptor and an envelope-minted key | `packages/rbac-domain/lenses.go:64-72`; `cmd/refractor/main.go:1669-1679` | 3 |
| the population and the split | run the new census test before writing any package edit; reconcile 63 / 65 / 61 | 3 |

### 15.2 Verified touch-list (live at `07c837b2`)

- `internal/refractor/pipeline/audit.go` — `AuditPlan` (`:128-138`), `auditEnrolment` (`:911-997`,
  drop `:962-964` and `:975-977`, correct the doc at `:970-977`), `auditAnchor` (`:737`, the one
  comparison), the enrolment re-check (`:473-503`).
- `internal/refractor/pipeline/secure.go` — `Columns()` beside `HolderTypes()` (`:116`).
- `internal/refractor/pipeline/reproject.go` — `rowsComparableMasked` beside `rowsComparable` (`:738`);
  `canonicalJSON` (`:748-760`) unchanged.
- `internal/refractor/pipeline/anchor_derivation_plain.go` — licence (`:329-331`), seam note (`:506-516`),
  the re-entry (`:534`), the cap fallback (`:715-723`).
- `internal/refractor/pipeline/anchor_derivation_shadow.go:324-330` — `recordDerivationFellBack` reaches
  the reporter.
- `internal/refractor/pipeline/pipeline.go:1509` — `evaluatePlainFromVertexRaw` beside
  `evaluatePlainFromVertex`.
- `internal/refractor/pipeline/evaluate.go` — `applyDiffRetraction` (`:1442-1460`) takes the prefix path.
- `internal/refractor/ruleengine/full/ast.go` — `ExistenceDependsOnNeighbour` beside `:494`, using the
  WITH-alias environment.
- `internal/refractor/health/lattice_heartbeater.go:446-471` (`LensLivenessStatus`), `:2274-2299` (the
  enrolled branch and the empty-container rule).
- `cmd/refractor/main.go` — the activation guard beside `:1589-1607`; the registry-based sharing check;
  `cmd/refractor/sweepstatus_test.go:128` (widen the reflection pin).
- packages: `clinic-domain/lenses.go:326-345` (comment only), `one-bill/lenses.go:116,138`, optionally
  `console-operator/package.go:87`, `demo-operator/package.go:88`; each `package.go` + `manifest.yaml`
  version.
- tests: §10.

### 15.3 In-scope gotchas

- The audit re-checks enrolment at the top of every pass (`audit.go:473-503`); the mask must be re-read
  there too, or a hot-reload that adds a secure column audits under the old mask.
- `auditRefusal` strings are latched for log-once; retiring conjuncts changes the strings the
  `noteStaticPlainDerivationRefusal` latch and any test pins compare — grep both.
- The licence's `Stale` check reads `LastPassAt`, not the restored cursor; do not "fix" the restart window
  here — it is the design of record's posture and out of scope.
- `ProtectedAdapter` implements `KeyLister`, `RowReader` **and** `OutcomeDeleter` by delegation
  (`read_path_adapters.go:263-268`, `:382-384`) — its doc says why re-declaring the last was load-bearing.
- The new health fields are `LensLivenessStatus` fields: the `healthwire.Entry` carry-forward test does not
  see them; the widened reflection pin (§10) is the gate.
- A T1 lens on a deployment with `SetAuditEnabled(false)` has no transport; the published state and the
  warning are the only signal — do not let the gate read "derivation" there.
- `lint-conventions`' P5 gate and `lint-package-version` run on every package edit.

### 15.4 Non-goals

An audit that writes (§7 row 3); an erasure attestation for identity-custodied secure columns (§12); the
licence's restart window; the derived-anchor cap's size; the presence probe's RLS role assumption; any
change to the sweep; auth-plane plain-lens retraction.

---

## 16. Build note — fire brief delta + checkpoint (Steward, 2026-09-05)

**Phase 0 at `86bead09`.** §15 is the brief; a read-only scout re-anchored every §15.2 site live and every
§15.1 premise holds at this commit (line numbers unchanged since `07c837b2`; the refractor drift since then
is the untyped-hop and walk-scope fires, none of it on this design's touch list). The scan-root pin holds
**65** lenses (floor `> 40`); the census test of Inc 3 takes its population from that pin. Live state at
fire start: `health.refractor.rfx-469256e559a4` — `leaseApplicationsRead` still `auditEnrolled`,
`alert=diverged`, `divergentRows={stale:10}` of 10 audited, `auditCycleDivergentTotal=57` of 64; the ten
Secure lenses still refuse on the plaintext string; `read_lease_applications` = 59 rows / 2 deleted, and
its columns include `authz_anchors text[]` and six `double precision` numerics — the two representation
candidates §2.4 names, to be settled by the Inc 1 premise before the mask is built.

**Scope-diff gate:** the three increments of §14 trace item-by-item to §0's scope sentence; no widening, no
substituted mechanism. Dependencies re-verified both ways: Inc 2 rests on Inc 1 (the licence's first
conjunct is audit freshness — `audit.go:329-331` neighbourhood), Inc 3 on Inc 2 (T1's population).

**Landing shape: each increment lands on `main` when green.** The invariant that keeps `main` correct at
every boundary: the audit never writes (Inc 1 changes only its comparison), the licence's other conjuncts
stay in force (Inc 2 admits a Secure lens exactly where a non-Secure one is already licensed), and the
gate is added last, only once the census pins zero business-plane debt (Inc 3).

**Inc 1 premise, settled (2026-09-05).** The ten `stale` rows were a comparator artifact, but not the one
§2.4 guessed: `PostgresAdapter.GetRow` returns content columns only, while the computed row carries every
RETURN alias, key column included — every audited Postgres anchor disagreed on its own key. §4.1's masked
comparator therefore excludes the row's key columns on both sides for every lens, and the mask on top of
that for a Secure lens. **Deviation, Winston-adjudicated:** the same key exclusion is applied in
`Reproject`'s `classifyDivergence` — §4.1's "the shared comparator never learns a mask" clause concerns
content columns (a masked column may disagree); key columns are identity, and a row fetched by its keys
cannot differ in them. `rowsComparable` itself stays untouched. The site is unreachable for a Postgres
target today (all 26 actor-aggregate lenses target NATS buckets; `PostgresAdapter` is no
`PrefixKeyLister`, so the sweep never enrols one) and is corrected so the first such lens does not
re-import the artifact.

**SHIPPED (2026-09-05).** Inc 1 `46ad980e` · Inc 2 `292a9ed0` · Inc 3 `424e2740` · reload follow-up
`ec9f90af`; CI green on each landing (the `424e2740` run reddened once on the edge-manifest e2e's 20 s
activation wait — the Whetstone-owned wall-clock class, green on the unchanged re-run). Worktree removed.

**Inc 3 close pass (three cold reviewers + one cold verifier) — folded:** the shared-bucket scoping was
order-dependent (a diff lens loading first on an empty bucket ran unscoped forever) — scoping is now
unconditional, the sibling check reads the INSTALLED prefix, and every key space on a bucket a diff reads
must be provably disjoint, symmetric in load order, held corpus-wide by
`TestPlainRetractionTransportCensus_SharedBucketsAreDisjoint`; `ExistenceDependsOnNeighbour` was fail-open
on anonymous pattern elements; a MATCH hot-reload bypassed the gate and the wire hid the result (the reload
re-runs both guards and restores the running rule on refusal; `none` / `unclassified` are the error-tier
backstop); the gate is pinned on the production activation order; `corpusLensRule` carries the declaration
fields every census reads the plane off; the second comparator artifact (a stored-only table column) is
excluded on the `projectItems` premise. §3.1's hand census had missed `capabilityReadWildcardGrants`
(32 / 23 / four auth-plane members; the builder's "retracted only by the identity tombstone" claim was
refuted — the `holdsRole` unwire retracts via the link arm; the role vertex tombstone is the residue).
Found live after the merge: a guard-refused lens held no registry entry and no retry queue, so the
package refresh that fixed the two one-bill lenses left them dark until a cycle — `ec9f90af` hands an
update on an unregistered lens to the existence-checked activation entry.

**Live acceptance (§11), shared stack, `health.refractor.rfx-fe2bf3a99d1a`, after the first audit pass:**
12 Secure lenses `auditEnrolled` with non-empty `auditMaskedColumns`; `leaseApplicationsRead` 10 audited /
0 divergent, `alert: ok` (was 10 of 10 `stale`); `visitSeriesRead` 6 audited / 0 divergent (was 2 of 2
`stale` on the stored-only `active` column); 9 of 12 `derivationArmed: true` (the two `DiffRetraction`
Secure lenses are refused by the index by design; `applicantRosterRead` is in its post-restart audit
window); no `cannot act on this lens … Secure Lens` line since the cycle; live `retractionTransport`
census: derivation 19, diffRetraction 6 — the pin's business-plane split exactly; zero activation
refusals once the three edited packages were refreshed in place. Open measurement: `derivationFellBack` /
`derivationOverCapSize` across a day of PO traffic on the five Secure lenses (§11 row 4) — the instrument
is live, the reading is not yet taken.

**Inc 2 review (three cold reviewers, nothing blocking) — folded:** the published shape is
`derivationEligible`-gated (§5 row 3, amended in place); the derivation index refuses an expanding `*`
anchor (a resolved expansion's AST label seeds nothing, so the re-entry would rescan per derived anchor —
no corpus instance, closed by construction); the mutation test's detector is the stored plaintext and the
zero-redaction assertion, not the vault-call count; §10's lease-signing row and the e2e row are amended
to what shipped. Stale comments that named the retired conjuncts (`clinicPatientsRead`, the scan-root
census pins, `rulestate.go`'s custody-scope containment claim) are corrected in the same commit.
