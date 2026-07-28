# `cap-read` size bound — per-anchor grant keys (the KV read-grant slice stops being one unbounded document)

**Status: ✅ Andrew-ratified 2026-07-25 (Option A — keep the KV family, per-anchor) — 🏗️ BUILDING
(Fire 1, in progress).** The §6.13/§6.14 contract edit is **committed** at ratification per the house
rule (the contract is the build-to target; its transitional note marks the legacy document shape as the
live wire format until the build drains it). The build-shelve (showcase priority) lifted 2026-07-27 —
Facet's showcase build shipped end-to-end — and Fire 1 started the same fire. Fires ship 1→2→3 (+4
independent) as decomposed in §10; §4.5's prefix-scoped truncate may land earlier via the standalone
fire-briefs Fire B, which Fire 1 then inherits.

**CHECKPOINT (Fire 1, increment 1 — 2026-07-27, worktree `.claude/worktrees/cap-read-per-anchor-fire1`,
branch `fire/cap-read-per-anchor-fire1`, merged to main `ccf17553`):** shipped the §3.3 `entryKeyColumn`
output-descriptor field — `lens.OutputDescriptorSpec.EntryKeyColumn` + `projection.OutputDescriptor
.EntryKeyColumn` + `ParseOutputDescriptor` fail-closed validation (exactly one bodyColumns entry when
set, blank rejected). Deliberately **not** wired into the write path yet: `EnvelopeFn` is untouched, and
`InstallActorAggregate` **refuses registration** of any lens that sets `entryKeyColumn` (no perEntry
emission consumer exists yet — a loud refusal, not a silent doc-mode fallback), pinned by
`TestInstallActorAggregate_EntryKeyColumnSet_Refuses`. `outputDescriptorsEqual` (cmd/refractor/reload.go)
now includes the field so a future flip is correctly judged not-hot-reloadable. No lens sets the field
today — zero behavior change for any existing lens. Adversarially reviewed (opus) before merge; 2 majors
found and fixed in the same fire (the reload-equality gap and the silent-no-op registration hole above),
4 minors fixed (TrimSpace on the stored value, test-coverage precision, this checkpoint).
**Next increment:** §4.1 driver perEntry emission — this needs new pipeline plumbing first
(`pipeline.EnvelopeFn` is a strict 1-row-in-1-row-out transform; true per-entry key emission needs either
a new N-envelope registration path on `Pipeline` or an equivalent fan-out inside `EnvelopeFn`'s caller in
`evaluate.go` — ground that shape before writing the driver side), then remove the
`InstallActorAggregate` refusal added this increment. §4.2's prefix-diff retraction is the increment
after that (§4.1 and §4.2 are tightly coupled — likely land together). Left with gates green, CI to
watch after push.
**Author:** Winston (Designer fire, 2026-07-25)
**Backlog:** Stream-2 Security & trust boundary — *[Refractor] A `cap-read` document has no size bound* (★★, M)
**Owning components:** `internal/refractor/{projection,pipeline,adapter,capabilityread,keyshredded}` (mechanism), `internal/bootstrap/lenses.go` + `internal/pkgmgr/anchorwalk.go` (producers), `packages/edge-manifest` (+ any package shipping a `cap-read.*` NATS-KV producer). Docs: `docs/contracts/06-capability-kv.md` §6.13/§6.14 (edit prepared uncommitted in the working tree), `docs/components/refractor.md`.

---

## For Andrew

**What it does (two lines).** A `cap-read.<domain>.<actor>` NATS-KV document aggregates *every* anchor an actor may read into one value, so a well-connected actor's grant slice can exceed NATS's 1 MiB `max_payload` — at which point the write fails **permanently** and that actor's grant set **freezes at its last storable state, so revocations stop landing (fail-OPEN)**. This design replaces the per-actor *document* with per-actor-per-anchor *keys* (`cap-read[.<domain>].<actorSuffix>.<anchorId>`, each a few hundred bytes, guarded per key) — the same per-row shape the Postgres `actor_read_grants` side of §6.14 already ratified — so no single write ever grows with grant cardinality and a revocation is an independent per-key tombstone that cannot be blocked by the size of the rest of the set.

**Architectural fork (D1-adjacent — resolved at ratification 2026-07-25: Option A).** The deeper question is whether the NATS-KV `cap-read.*` family should exist at all: the Postgres `actor_read_grants` table is the ratified Path-A source of truth, and §6.14 calls the NATS-KV filter "transitional scaffold." But the family has one real, load-bearing consumer that is *not* Path B: the **Personal Lens D1 gate** (`capabilityread.IsReadable`, personal-secure-lens-design.md §3.4/PL.3), which filters every Edge-bound delta at projection time inside Refractor. **Option A (recommended): keep the KV family as the Personal-Lens gate's substrate-native store and bound it per-anchor** (this design). **Option B: retire the KV family and point the gate at Postgres `actor_read_grants`.** I recommend A; §8.1 argues it through (B couples the Edge push plane to Postgres availability and per-delta SQL, silently changes wildcard-anchor semantics on the Edge path, and forfeits the auth-plane convergence sweep that healed the live incident). If you pick B anyway, §8.1 sketches what it takes honestly.

**Frozen-contract change: yes — §6.14 (and one §6.13 descriptor row).** The per-lens NATS-KV document shape in Contract #6 §6.14 becomes a per-anchor entry keyspace; §6.13's Output-descriptor aspect table gains `entryKeyColumn`. The actual edit is prepared **uncommitted** (a working-tree modification) in `docs/contracts/06-capability-kv.md` — the diff is the proposal. Affected consumers: `internal/refractor/capabilityread` (the only reader), the bootstrap `capabilityRead` base lens, `pkgmgr`'s generated AnchorWalk producers, `packages/edge-manifest`. The Postgres §6.14 half (`actor_read_grants`, RLS) is **untouched** — it is already per-row and is the pattern being mirrored.

**One pre-existing hazard this fire surfaced (not introduced by it, must not be tripped by it):** `Pipeline.Rebuild` on any **guarded** lens force-truncates its adapter, and `NatsKVAdapter.Truncate` purges the **whole bucket** — every capability-bucket lens shares one bucket, so a single lens rebuild wipes `cap.*`, `cap.roles.*`, `my-tasks.*` and every sibling slice (the Postgres side already refuses exactly this — `GrantWriterAdapter` deliberately implements no Truncater). §6/§10 therefore use **no rebuilds**; Fire 1 ships a per-lens **prefix-scoped truncate** so the hazard is closed rather than danced around.

**Grounded incident (why ★★ is earned).** 2026-07-25, live dev stack: `cap-read.edgeManifest.identity.MQsmTTAgNkngkdEjQz9L` reached **1,021,658 bytes** (12,558 entries carrying 34 distinct grants); the auth-plane sweep failed to write it every 60 s **for days** with `maximum payload exceeded` while the lens reported `alert: ok`. The `collect(DISTINCT)` fix (`1b9852f2`) removed that *inflation*, but the *unboundedness* is structural: a building manager granted every unit in a large building, deduped, still grows without bound.

---

## 1. Problem & intent

### 1.1 The failure mechanism, grounded in code

The §6.14 NATS-KV read-grant slice is an **actorAggregate document**: one key per (domain, actor), whose body is the complete `readableAnchors[]` roster ([driver.go](../../internal/refractor/projection/driver.go) `EnvelopeFn` — one envelope per actor evaluation, keyed `OutputDescriptor.BuildKey(actorKey)`). Three properties combine into the failure:

1. **The value grows with grant cardinality.** Every anchor the actor may read is one more entry in one JSON value. Nothing bounds it; the hand-chosen `ReadGrantDomain` split (anchorwalk.go `ReadGrantDomainSpec`) only divides the total by a small constant.
2. **NATS refuses the whole write past `max_payload`.** Our pin (NATS 2.14; `go.mod` `nats-server/v2 v2.14.0`, `docker-compose.yml` `nats:2.14-alpine`) runs the default **1 MiB** `max_payload` (`deploy/nats-server.conf` sets no override; the nats.io server-configuration reference documents the 1 MB default and an 8 MB recommended ceiling). The Processor's batch commit pre-flights against the negotiated `INFO` limit (`nc.MaxPayload() − ValueHeadroomBytes`, `internal/substrate/batch.go:201-219`) — but **Refractor's `NatsKVAdapter.guardedWrite` has no size check at all** (`natskv.go:200-247` calls `Create`/`Update` directly), which is why the incident surfaced as a *server-side* `maximum payload exceeded` rather than a pre-flight refusal. Fire 4's alarm therefore *adds* a check to a path that has none today, reusing the batch.go derivation.
3. **The failure is total, not partial.** One document = one write: when it exceeds the limit, *no* update lands — not the new grants, and **not the revocations**. The retry queue and the auth-plane convergence sweep re-attempt the same oversized value forever. `CapabilityRepairFailing` (`3b0798c8`) now makes the freeze *visible*, but nothing makes it *writable*.

On the security plane the failure direction is the worst one: a frozen grant set is an **over-grant** (revoked anchors stay readable to the Personal-Lens gate) that no amount of retrying repairs.

### 1.2 Who actually consumes these documents

Grounding the consumer set (grep, 2026-07-25) — this is load-bearing for the design choice:

- **`internal/refractor/capabilityread.IsReadable`** — the **only** reader. Called per Edge-bound delta by the Personal-Lens envelope ([personal.go:147](../../internal/refractor/projection/personal.go)). It asks one question: *is this one `anchorId` in the actor's unioned set?* It lists the domain slices with the wildcard filter `cap-read.*.<actorSuffix>`, GETs each document, and scans `readableAnchors[]` for the NanoID. **It never needs the whole set** — it materializes an unbounded roster to answer a membership test.
- Nothing else reads the documents. The Postgres RLS boundary reads `actor_read_grants` rows; the Gateway auth code references the doc shape only in comments; Loupe may inspect the bucket (inspector exception, cosmetic).

The mismatch is the design: **an unbounded set, stored as one value, consumed only by point membership queries.** The fix is to store it as what it is — a keyed set.

### 1.3 Intent

Make every NATS-KV read-grant write **O(1) in grant cardinality**, make revocation a per-key operation that cannot be blocked by the size of the rest of the set, keep the §6.2 monotonic guard **per key** so a stale replay still can't resurrect a revoked grant, and keep the Personal-Lens gate substrate-native. Secondary: give the remaining (legitimately) document-mode capability lenses a payload-proximity alarm so the next unbounded-doc class is caught before it freezes.

---

## 2. Grounding — the pattern we mirror

This is not a new mechanism; it is the **KV twin of the shape §6.14 already ratified for Postgres**:

- **`actor_read_grants` is per-row**: `(actor_id, anchor_id, grant_source)` primary key, one row per grant, each write/revoke independent and seq-guarded per row (`PostgresGrantWriter.UpsertGrant`/`RevokeGrant`; `GrantWriterAdapter` in [read_path_adapters.go](../../internal/refractor/adapter/read_path_adapters.go)). No row grows with cardinality; no size bound exists to hit. The document shape was the outlier, not the norm.
- **The guarded per-key machinery already exists on the KV side**: `NatsKVAdapter` writes are CAS-guarded per key with the §6.2 monotonic `projectionSeq` (`guardedWrite`: `Update` with `ExpectedRevision`, `≤`-rejects), and a guarded delete is a **soft tombstone** `{isDeleted, projectionSeq}` that carries the watermark across the delete (§6.8). Per-anchor keys inherit all of it verbatim, per key — with one carve-out named in §4.2 (the guard protects keys that *exist*; an absent key's first write is an unconditioned `Create`, which is what makes the retry path a hole to close, §4.3).
- **The sweep's orphan direction is already multi-key-per-anchor aware**: `SweepPlan.AnchorFromKey` maps any owned target key back to its anchor, and the sweeper deduplicates orphan candidates "so a lens with several keys per anchor sizes the share by actors" ([sweep.go](../../internal/refractor/pipeline/sweep.go)). The coverage/orphan membership tests and the survey enumeration need named deltas (§4.4) — the design does not pretend the sweep works unmodified.
- **The precedent for a negotiated-payload bound**: `internal/substrate/batch.go` computes the per-value ceiling as `nc.MaxPayload() − ValueHeadroomBytes` (honoring a production override rather than hardcoding 1 MiB); the Processor's step-8 commit enforces it. Fire 4's proximity alarm reuses exactly this derivation.

What this design **must add** (and names precisely, per the retraction-transport rule): a per-actor **row-set** has a retraction problem the single document never had. Overwriting one document auto-retracts dropped anchors; per-anchor keys are a **row-set shrink**, and an upsert-only reprojection would leave a revoked anchor's key live forever — on the security plane, an over-grant. §4.2's per-actor prefix diff is the explicit transport, and §4.3 closes the retry-replay hole that a naive port would open.

---

## 3. The shape

### 3.1 Key space (Contract #6 §6.14, revised — edit staged uncommitted)

```
cap-read.<actorSuffix>.<anchorId>            # core base lens (self-grant): cap-read.identity.<id>.<id>
cap-read.<domain>.<actorSuffix>.<anchorId>   # each package's domain slice, one key per granted anchor
```

`<actorSuffix>` stays `<type>.<id>` (two tokens); `<anchorId>` is the anchor's bare NanoID (the §6.14 opaque match token, unchanged).

**Disjointness from the legacy shapes (the actual argument, not a token-count hand-wave):** the 5-token domain grant key is count-disjoint from every legacy shape (legacy base doc = 3 tokens, legacy domain doc = 4). The 4-token **base** grant key shares its count with a legacy **domain** doc; they cannot be confused because (a) the reader's base check is an **exact GET** whose token 3 is a specific actor's 20-char NanoID, while a legacy domain doc's token 3 is a vertex-**type** name — and no declared vertex type is a 20-char NanoID; and (b) the perEntry `AnchorFromKey` NanoID-validates the actor-id and anchor-id tokens, so neither shape's sweeper can claim the other's keys. Residual hardening (Fire 2): `validateGrantDomainName` additionally rejects a domain name that collides with a declared vertex-type token, closing the corner structurally instead of by argument.

### 3.2 Per-key document (small, constant-size)

```json
{
  "key":           "cap-read.residence.identity.Hj4kPmRtw9nbCxz5vQ2y.Lk2Pn6mQrtwzKbcXvP3T",
  "actor":         "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
  "version":       "1.0",
  "projectedAt":   "2026-07-25T14:32:18.142Z",
  "projectionSeq": 10481,
  "anchorType":    "unit",
  "via":           ["residesIn"]
}
```

`anchorId` lives in the key (no duplication in the body; the reader never parses it out of the value). `anchorType`/`via` stay audit-only (§6.14 representation note, unchanged). `projectedFromRevisions` is dropped from the per-anchor entry — it was anchor-provenance metadata on the aggregate; the per-key guard token (`projectionSeq`) is the ordering authority and stays. Each value is ~250–400 bytes regardless of how many anchors the actor holds. The incident actor's 34 grants become 34 keys ≈ 12 KB total; an actor with 10,000 grants is 10,000 independent ~350 B writes — no single write ever approaches the payload limit.

### 3.3 The descriptor extension — `entryKeyColumn` (§6.13, one new optional aspect)

An actorAggregate lens opts in by naming which field of its (single) list body column keys each entry:

```
Output: { anchorType: identity, outputKeyPattern: "cap-read.<domain>.{actorSuffix}",
          bodyColumns: [readableAnchors], entryKeyColumn: "anchorId",
          emptyBehavior: delete, realnessFilter: "anchorId", freshness: auto }
```

Semantics: each **real** entry (post realness-filter) of the list column writes one guarded key `BuildKey(actorKey) + "." + entry[entryKeyColumn]`, body = the entry's remaining fields + the envelope metadata above. Validation (fail-closed, `ParseOutputDescriptor`): `entryKeyColumn` requires exactly one list body column; an entry whose key field is absent, empty, non-string, or not a valid NanoID-alphabet key token (a subject metacharacter must never reach a key) **errors the whole actor evaluation** (`RecordError` → §4.3 re-evaluation retry → visible), never silently drops a grant or writes a malformed key. Producer cyphers are **unchanged** — same walks, same `collect(DISTINCT …)`; only the output mode changes. Duplicate `anchorId`s across walk branches collapse to one key (last entry's audit fields win — audit-only, so benign; the DISTINCT dedup already collapses exact duplicates).

### 3.4 Read path (P5) — `capabilityread.IsReadable`, membership by key

- **Base:** one exact `GET cap-read.<actorSuffix>.<anchorId>`.
- **Domains:** one server-side filtered listing `cap-read.*.<actorSuffix>.<anchorId>` (mid-token `*` is a supported, tested subject-filter class — `substrate_test.go:599`, documented `substrate/kv.go:251-261`; note `KVListKeysFilter` sorts and pages while `KVListKeysPrefix` returns unspecified order — nothing in this design may assume prefix-listing order), then GET each match and skip `isDeleted: true`. Matches = the domains that currently grant **or ever granted-then-revoked** this anchor (soft tombstones are listed by design — `KVListKeysFilter` deliberately returns them); typically a handful either way, each a few hundred bytes.
- **Input hardening (a change, not "unchanged"):** `anchorID` becomes part of a subject filter, so `IsReadable` extends its existing loud metacharacter rejection (today applied to `actorType`/`actorID`) to `anchorID` — a `.`/`*`/`>` in the anchor errors, it never builds a broadened filter. (Today's sole caller pre-validates via `ParseVertexKey`; the check is the same defense-in-depth the function already documents for actor inputs.)

Cost per check *drops*: today it GETs and JSON-parses every slice document (up to ~1 MiB each); after, it lists one actor+anchor-scoped filter and parses a few hundred bytes per match. Fail-closed posture: no key / all tombstoned / malformed inputs all deny or error loudly, as today. The wildcard-anchor non-admission stays deliberate and documented (Postgres-only, §6.14 M5 — unchanged).

### 3.5 Write path (P2 posture unchanged)

Refractor remains the sole writer of its own lens targets; nothing here touches Core KV or the Processor. The producers stay actorAggregate lenses — per-affected-actor evaluation via the broad-BFS fan-out, auth-plane classification (bucket-derived), guarded activation — all unchanged.

---

## 4. Mechanism (what the Steward builds)

### 4.1 Driver: per-entry emission

`OutputDescriptor.EnvelopeFn` currently returns one envelope per actor. With `entryKeyColumn` set, the projection path instead produces the actor's **fresh child-key set**: `{BuildKey(actor)+"."+id → entryBody}` for each real entry. (Mechanically: a per-entry sibling of `EnvelopeFn` on the driver, not a change to the plain doc path — doc-mode lenses stay byte-identical.)

### 4.2 The retraction transport — per-actor prefix diff (the load-bearing piece)

Every per-actor projection pass (fan-out reprojection, sweep deep-verify, reconciliation):

1. List the actor's child keys: server-side filtered listing on `BuildKey(actorKey) + ".*"`. The listing returns **live ∪ tombstoned** keys (soft tombstones are real JetStream entries and are listed — this is by design; the watermark must be discoverable).
2. **Tombstone first (deny-closed ordering):** for each listed key **not** in the fresh set, GET it; if already `isDeleted`, **skip** (no rewrite — safe, because any grant-era replay carries a seq below the tombstone's stored watermark and the `≤`-reject already refuses it; re-stamping would be pure churn); if live, guard-tombstone it at the incoming seq. Also guard-tombstone the **legacy parent document key** (`BuildKey(actorKey)` itself) when a live one exists — the migration transport (§6).
3. **Then upsert** every fresh entry (guarded per key, incoming `projectionSeq`).

If a pass dies mid-way, the interim state only ever *under*-grants (revocations land before grants), never over. The diff is scoped to the actor's own prefix — never a whole-bucket enumeration, never another actor's keys.

**Honest cost statement:** one per-actor evaluation costs O(granted + ever-revoked) KV operations for that actor (the listing + one GET per dropped/tombstoned candidate + one guarded write per live transition). The ever-revoked term is the price of per-key watermark retention (§6.8, same posture as `actor_read_grants` rows) and never drains; if it ever needs reclaim, that is the shelved hard-delete-verb discussion, explicitly not re-opened here.

**Actor-disappearance, empty-set, and shred — all multi-key now (enumerated, not hand-waved).** The single-key delete contract has four consumers that each become a prefix-scoped list-then-tombstone with the same deny-closed partial-failure semantics — via **two distinct hooks**, not one: (a) the CDC actor-disappearance delete and (b) `emptyBehavior: delete` on a zero-real-entry evaluation both flow through `actorDeleteKeyFor` (`evaluate.go:131,490`) and (c) sweep `Reproject` deletes ride the same evaluation path — these three share the prefix-aware delete hook. (d) the **shred path** is a *separate enumeration problem*: `keyshredded` calls `Control.NullifyRow(ruleID, keys, MaxInt64)` with **caller-supplied explicit keys** (`keyshredded/manager.go:237`), so the shred site itself must list the actor's child keys by prefix and nullify **each** at `MaxInt64`, not just the parent — or the privacy plane leaks per-anchor grants. Fire 1 lands all four, costed as two pieces of work.

### 4.3 The retry path — re-evaluate, never replay (closes a hole the doc shape doesn't have)

The pipeline's retry queue today replays the **captured raw write** (keys/row/seq) from a separate goroutine. For a document lens the §6.2 guard makes that safe: the stale replay loses to the doc's advanced watermark. For per-anchor keys it is **unsafe**: a grant-era write for anchor X that failed transiently leaves **no key and no watermark at X**; a later revocation's prefix diff cannot tombstone a key that was never created; the replayed `Create` then lands at its stale seq and **resurrects the revoked grant** until the sweep's next deep-verify of that actor. This is the §6.2 amendment's original defect class re-entering through the absent-key door.

Therefore: **a perEntry lens's write failures are retried as actor re-evaluations** (the existing `Reproject` path, reconciliation-token stamped — the fresh evaluation re-derives current truth, so a revoked anchor is simply absent from the fresh set), and the raw-replay retry path **refuses** perEntry-lens entries fail-closed (wired at activation, tested). No poison-tombstone bookkeeping, no diff-against-pending-set complexity — the retry unit changes from "the write" to "the actor," which is also the natural unit the sweep already repairs at.

### 4.4 Sweep (the healer keeps working — SweepPlan perEntry mode, three named deltas)

The auth-plane convergence sweep is what detected and would have healed the incident; it must understand the new shape:

- **Survey target enumeration: already lens-prefix-scoped — shipped `34b13ffd` (2026-07-25), nothing to build.** `Sweeper.survey` lists targets via `lister.ListKeysPrefix(ctx, s.plan.KeyPrefix)` (`sweep.go:605`), enrolment-gated so a plan without a `KeyPrefix` is refused (`driver.go:166-181`, `sweep.go:47-52`) — so per-anchor keys grow only the *owning* lens's own listing, and §5's cardinality growth taxes no sibling sweeper. (The still-unscoped listing is the **Core-KV anchor** side, `sweep.go:580` — a different listing, already filed as its own board row, untouched by this design.)
- **Coverage direction:** the presence test becomes *"the anchor has ≥1 target key under its prefix"* — computed by grouping the listed target keys via `AnchorFromKey` (no new I/O), replacing the single-key `targets[BuildKey(actor)]` probe. A zero-grant actor is indistinguishable from a missed one by presence alone — exactly as today with `emptyBehavior: delete` — and the deep-verify direction re-evaluates and repairs either way.
- **Orphan direction:** candidacy is `key ∉ expected`, where `expected` today maps **exact keys**; under perEntry `BuildKey(actor)` is a prefix equal to no real key, so *every* live child of *every* live actor would flood the orphan set and starve real orphans (the only detector for departed-actor keys — an over-grant direction). The membership test becomes *"recovered anchor ∈ the live-anchor set"* (via the perEntry `AnchorFromKey`). Small fix, load-bearing.
- **`AnchorFromKey` perEntry variant:** strip the pattern's literal prefix, then parse `<type>.<id>.<anchorId>` — accepting exactly an `<AnchorType>`-typed `<type>.<id>` plus one trailing NanoID token, keeping sibling lenses' keys unclaimable (the same exactness guarantee the doc-mode parse provides today).
- **Deep verify:** re-evaluate the actor, run the §4.2 diff — repair and retraction are the same code path the normal projection uses (one mechanism, not two).

### 4.5 Prefix-scoped truncate (closing the shared-bucket rebuild hazard)

`Pipeline.Rebuild` forces truncate on guarded adapters (`pipeline.go:585-594`; its only caller is the operator-triggered `Service.rebuildRule`), and `NatsKVAdapter.Truncate` purges the whole bucket (`natskv.go:407-421`) — on the shared `capability` bucket that is a platform-wide auth wipe (every sibling lens's keys), healed only at sweep pace. This design **prescribes no rebuilds** (§6) and requires the hazard closed rather than danced around: a perEntry (and doc-mode capability) lens truncates only keys its own `AnchorFromKey` claims. The Postgres side already embodies this rule (`GrantWriterAdapter` implements no Truncater precisely because the table is shared); the KV side gets the equivalent. **Build routing:** the identical prefix-scoped truncate is independently compiled as `refractor-open-rows-fire-briefs.md` **Fire B** (standalone, buildable now — the hazard is live regardless of this ratification, and the board row is 📋 ready). Whichever fire reaches it first builds it; if Fire B lands ahead of this design's Fire 1, Fire 1 **inherits** it and adds only the perEntry-specific vector (sibling keys intact under per-anchor cardinality). Not double-built, not presented as unclaimed ground.

### 4.6 What deliberately does NOT change

The write-path capability documents (`cap.<actor>`, `cap.roles.<actor>`, `cap.svc.<actor>`, `cap.ephemeral.<actor>`, `my-tasks.<actor>`) **stay documents**: their consumer (Processor step 3 / my-tasks readers) reads the whole document by design, and their cardinality drivers (roles, open tasks per actor) are domain-bounded rather than resource-set-shaped. They get Fire 4's proximity alarm, not the perEntry split. The Postgres §6.14 half is untouched. The Personal-Lens security-wins-over-relevance ordering, Interest-Set filter, and Hydrate are untouched.

---

## 5. Reconciliation with the existing mental model

- *"Didn't we already fix this?"* Two adjacent fixes landed; neither closes it. `1b9852f2` fixed the **multiplicative inflation** (composed `collect(DISTINCT)` losing DISTINCT) — the deduped set still grows without bound. `3b0798c8` (`CapabilityRepairFailing`) made the freeze **detectable** — detection without a writable repair path. This design removes the bound itself.
- *"Isn't the NATS-KV cap-read family transitional scaffold?"* §6.14's "transitional" verdict applies to **Path B** — serving *protected read models* from NATS-KV instead of RLS. The `cap-read.*` **grant slices** are a different thing: the substrate-native store the Personal-Lens D1 gate reads (PL.3), with no Postgres equivalent consumer path shipped or designed. §8.1 confronts the consolidation question directly rather than leaving the tension in comments.
- *"Does this duplicate the GrantTable/DiffRetraction pattern?"* It mirrors its **semantics** (per-row grants, per-row guard, explicit retraction) but not its **machinery**, deliberately: GrantTable lenses are plain *unanchored* lenses whose DiffRetraction compares a full re-execution against the source-scoped live set — per-event cost scales with the whole population, and plain lenses get no auth-plane convergence sweep. The cap-read producers are per-actor lenses; the actor-scoped prefix diff is the same idea at the right scope (§8.2 rejects the conversion).
- *"New state?"* No new buckets, no new stores. Same `capability` bucket, finer keys. Tombstones accumulate per revoked anchor — exactly as `actor_read_grants` rows already do on the Postgres side (the watermark must outlive the grant; §6.8). Key cardinality becomes O(live + revoked grants) instead of O(actors×domains); the per-tick enumeration cost this implies is answered structurally by §4.4's prefix-scoped survey, not by an appeal to existing paging (the substrate's listing "paging" is client-side slicing — a convenience, not a cost bound).

---

## 6. Migration & compatibility (mechanisms that exist, no rebuilds)

Shape disjointness (§3.1) means both shapes coexist without reader ambiguity, which makes the migration **drainable, not flag-day**. The deployment mechanics are named honestly — three facts constrain them: bootstrap seeding is presence-gated (an edited base-lens *definition* never reaches a live cell's Core KV by itself); nothing triggers `Pipeline.Rebuild` on install/upgrade (and §4.5 forbids it here anyway); and an Output-descriptor change does **not** hot-reload (the update path handles cypher and INTO changes; the envelope/output wiring installs at activation only).

1. **Dual-read first (Fire 1):** `IsReadable` unions the legacy document shapes with the new per-anchor shape. Correctness during the window, argued per the retraction rule: a producer not yet flipped keeps live-maintaining its legacy doc (correct); a flipped producer's **first evaluation of each actor writes the per-anchor keys and guard-tombstones that actor's legacy parent doc in the same pass, tombstones-first** (§4.2). The event that revokes a grant *is* an evaluation of that actor — so the legacy doc can never serve a stale grant past the actor's first post-flip write.
2. **Flip transport (Fires 1–2):** the base `capabilityRead` lens flips via an explicit **lens-vertex migration** (an `UpdateMetaVertex` meta-op on the lens vertex — the P2-sanctioned DDL path — shipped with the fire, the same way bootstrap-seeded lens changes have landed before), followed by a **Refractor restart** (the standard deploy step; MERGED ≠ RUNNING already requires rebuilding the binary) so activation re-derives the envelope from the updated Output descriptor. **The same migration must update the lens's `OutputSchema` in the same stroke** — today it hard-`require`s `projectedFromRevisions` and `readableAnchors` (`bootstrap/lenses.go:224-232`), which the per-anchor entry body no longer carries; flipping the descriptor without the schema would fail activation-time validation against the new shape. Package producers flip via the normal package **version bump** (the `lint-package-version` gate enforces it) whose reinstall updates the lens vertices — again converging at the next restart/activation.
3. **Convergence/drain:** actors converge to the new shape via (a) any fan-out event touching them (immediate) and (b) the auth-plane sweep's **deep-verify rotation** — the mechanism that actually exists — at its budgeted pace (default ~25 actors/min per lens; population/25 minutes to full coverage). During the drain, legacy docs are still-current truth for untouched actors (their grants haven't changed) and are tombstoned actor-by-actor as evaluations reach them.
4. **Legacy-read retirement (Fire 3):** after every `cap-read.*` NATS producer is perEntry and a full sweep rotation has completed, run an explicit **one-shot purge** of any remaining legacy-shape keys (bounded prefix enumeration + guard-tombstone — this also catches docs of actors who departed *before* the flip, which no sweep direction can claim: the perEntry `AnchorFromKey` rejects legacy shapes by design), then drop the legacy-shape read from `IsReadable` and delete the doc-shape fixtures. Dropping the read earlier would *under*-grant never-evaluated actors (fail-closed, but an Edge-stream outage); the fire's gate is the bucket scan coming back empty.

Rollback at any point = revert the flip migration + keep dual-read; the per-key guard makes replayed writes in either shape safe.

## 7. Test strategy

- **Unit (driver/adapter):** perEntry emission (fresh set, entry-key validation fail-closed), prefix-diff retraction (dropped anchor → guarded tombstone; already-tombstoned candidate skipped without rewrite; stale-seq replay of a revoked anchor rejected per key), tombstones-before-upserts ordering, empty-set / actor-disappearance / **shred** prefix deletes (all four §4.2 consumers), legacy-parent tombstone, descriptor validation, `AnchorFromKey` perEntry exactness (sibling-lens keys unclaimed), prefix-scoped truncate leaves sibling keys intact.
- **Retry-path (the §4.3 security vector):** a transiently-failed grant write followed by a revocation must **not** resurrect the grant — the raw-replay path refuses perEntry entries and the re-evaluation retry converges to the revoked state. This is the test the doc shape never needed; it is the proof the port is sound.
- **Sweep:** prefix-scoped survey (sibling lenses' keys never enumerated), coverage presence-by-prefix (zero-grant vs missed actor), orphan membership by recovered-anchor (live actors' children never flood the candidate set; a departed actor's keys are claimed), deep-verify repairing both a missing key and a stale-extra key.
- **Reader:** `IsReadable` per-anchor membership incl. tombstone-skip, dual-read union, `anchorID` metacharacter rejection (loud error, not broadened filter), and a **wildcard-anchor non-admission pin** (a `'*'` anchor entry is never admitted — today a documented absence with no test, `capabilityread.go:14-20`; the pin converts §8.1's Option-B semantics argument from prose into evidence).
- **Contract-conformance:** the `capabilityRead` base-lens and generated-producer contract tests (`ruleengine/full/capability_read_*_contract_test.go`) re-assert the per-anchor shape.
- **E2E (the security proof):** the Personal-Lens suite (`personal_lens_pl*_e2e_test.go`, `edge_manifest_fire1_e2e_test.go`) driven over per-anchor grants — admit on grant, **drop on revoke** (the retraction e2e is the point), self-anchor grow, migration dual-read (legacy doc + new keys coexisting, revocation wins).
- **Bound proof:** a producer granting N≫payload/entry-size anchors projects successfully (every write small), where the doc-mode equivalent is the shipped failure; plus Fire 4's alarm unit tests against a fake negotiated `MaxPayload`.

## 8. Alternatives considered

### 8.1 Option B — retire the KV family; point the Personal-Lens gate at Postgres `actor_read_grants` (the fork, argued through)

**For:** one source of truth (the ratified Path-A table); deletes a whole keyspace + the `capabilityread` package; per-row bound for free; grant producers collapse to the GrantTable family. **Against, and why I recommend A:** (1) **Infra coupling** — the gate runs inside Refractor's Personal-Lens envelope on every Edge-bound delta; it would make the Edge push plane's correctness depend on Postgres liveness and add a per-delta SQL round-trip where today it is a substrate-local KV read (the substrate is definitionally present wherever Refractor runs; Postgres is present only where Postgres-target lenses are provisioned). (2) **Silent semantics change on the security plane** — `IsReadable` deliberately does *not* honor the `'*'` wildcard anchor (capabilityread.go's scope note); the Postgres membership lookup does (M5). Consolidating would newly admit wildcard holders' devices to every personal delta — maybe *desirable*, but a behavior change that deserves its own decision, not a side effect. (3) **Machinery loss** — the Personal-Lens domains (edge-manifest's generated producers) have no GrantTable twins; they'd be regenerated as plain unanchored lenses (whole-population re-execution per event, no auth-plane convergence sweep — the healer that caught the live incident). (4) **Could a variant of B beat A?** A Refractor-side cache of the grant table re-introduces a KV-shaped copy (that's Option A with extra steps); a "Postgres-only where provisioned, KV where not" hybrid forks the security gate's code path per deployment — worse than either pole. **Revisit trigger:** if the Personal Lens itself ever moves behind a Postgres-backed boundary, B becomes the natural end-state; the perEntry keys migrate trivially (they are already row-shaped).

### 8.2 Convert the producers to plain lenses + DiffRetraction (reuse the GrantTable machinery verbatim)

Rejected: per-event cost becomes a full re-execution over every actor (vs the fan-out's affected-actor set); plain lenses get no convergence sweep (driver.go installs `SweepPlan` only for actorAggregate); and `ValidateUnanchoredForDiffRetraction`'s soundness condition (the result set is the complete current truth) forces exactly the unanchored shape that scales worst. The per-actor prefix diff is DiffRetraction's idea at the actor scope the data already has.

### 8.3 Shard the document (`cap-read.<domain>.<actor>.<shard-0..15>` by anchor-hash)

Tempting because retraction stays free (an anchor's shard is hash-stable, so the shard-doc overwrite auto-retracts it) **and** it would not open the §4.3 retry hole (shard docs keep document watermark semantics). Rejected anyway: a fixed shard count only multiplies the ceiling (16 × 1 MiB), and re-sharding to grow it is a rehash migration of the whole keyspace under live security traffic. A structural bound beats a bigger ceiling; per-anchor keys are this spectrum's fixed point, and §4.3 closes the hole they open.

### 8.4 Raise `max_payload` (config lever)

The nats.io docs allow up to ~64 MB and recommend ≤8 MB. Global blast radius (every subject on the connection), defers rather than removes the bound, and enlarges every other payload-adjacent buffer. Not a fix; noted as an *ops emergency lever* only.

### 8.5 Do nothing beyond detection

`CapabilityRepairFailing` already alarms. Rejected: the alarm's only remediation today is a hand-crafted domain re-split (a `ReadGrantDomain` refactor + package upgrade under incident pressure) — and the frozen state it alarms on is an active over-grant while it persists.

## 9. Risks

- **First-convergence write volume:** the flip converges every actor × every grant as individual writes, paced by the sweep budget and fan-out — no burst rebuild (rebuilds are forbidden here, §4.5).
- **Per-evaluation cost carries the revocation history:** O(granted + ever-revoked) KV ops per actor evaluation (§4.2's honest cost statement) — actor-scoped and small per key, but permanent; reclaim, if ever needed, is the shelved hard-delete-verb discussion.
- **Retry-unit coarsening:** §4.3 retries a whole actor evaluation where a doc lens retried one write. Strictly more work per retry, strictly safer; the sweep already operates at this unit.
- **A future whole-set consumer** (something that wants "all anchors for actor X" from KV) would face an actor-scoped enumeration instead of one GET — acceptable, and the Postgres table remains the natural home for set-shaped queries.

## 10. Fire decomposition for the Steward (after ✅ Andrew-ratified)

1. **Fire 1 (M–L) — mechanism + first consumer:** `entryKeyColumn` descriptor + validation; driver perEntry emission + §4.2 prefix-diff (tombstones-first, tombstone-skip) + all four delete consumers costed as two hooks (the `actorDeleteKeyFor` trio + the shred site's own prefix enumeration); §4.3 re-evaluation retry (raw-replay refusal); §4.4 sweep deltas (coverage/orphan membership, `AnchorFromKey` variant — the survey target listing is already prefix-scoped, `34b13ffd`); §4.5 prefix-scoped truncate (**inherited from fire-briefs Fire B if it lands first**, plus the perEntry sibling-keys vector); flip the **bootstrap `capabilityRead` base lens** via the lens-vertex migration **incl. its `OutputSchema`**; `IsReadable` dual-read + anchor-token hardening + the wildcard non-admission pin. Green: units + retry-path vector + base-lens contract test + PL self-anchor e2e. (The mechanism ships with its live consumer — no dead scaffolding.)
2. **Fire 2 (S–M) — producer flips:** `pkgmgr.generateProducerLens` emits `entryKeyColumn`; flip every remaining `cap-read.*` NATS-KV producer (enumerate by `OutputKeyPattern` grep — edge-manifest's generated domains + any hand-authored); package version bumps; `validateGrantDomainName` type-collision hardening; edge-manifest e2e incl. revoke-drops-key and legacy-doc drain.
3. **Fire 3 (XS–S) — legacy retirement:** the §6.4 one-shot purge of remaining legacy-shape keys (incl. pre-flip-departed actors' docs); drop the doc-shape read from `IsReadable` + fixtures; gate on the bucket scan coming back empty.
4. **Fire 4 (S, independent — can ship any time):** payload-proximity alarm for the remaining doc-mode capability lenses: on a written value exceeding ~50% of `nc.MaxPayload() − ValueHeadroomBytes` (the batch.go derivation), surface a per-lens Health **warning** issue (e.g. `ProjectionDocNearPayloadLimit`) — plumbed through the pipeline's existing per-lens reporter/heartbeat (the adapter itself holds no reporter; the write path returns the observation, the pipeline reports it) — the tripwire for the next unbounded-doc class.

## 11. Adversarial pass (Designer-lane obligation — discharged)

An independent adversarial review ran 2026-07-25 against the full draft, grounded in the adapter/pipeline/sweep/substrate code. It confirmed the core shape (per-anchor guarded keys + actor-scoped diff; Option A over B) and returned **2 blockers + 6 majors**, all folded in: **(1)** the retry queue's raw-write replay resurrects a revoked grant through the absent-key `Create` door → §4.3 (re-evaluate, never replay) + the §7 retry vector; **(2)** the §6.13/§6.14 contract edit is now actually prepared uncommitted in the working tree; **(3)** the migration originally cited flip/rebuild mechanisms that don't exist (presence-gated bootstrap, no rebuild-on-install, no Output hot-reload) → §6 rewritten around the lens-vertex migration + restart + sweep-rotation drain; **(4)** `Pipeline.Rebuild`'s forced truncate purges the whole shared capability bucket → rebuilds forbidden, §4.5 prefix-scoped truncate; **(5)** listings return soft tombstones → §4.2 tombstone-skip semantics + the honest O(granted + ever-revoked) cost statement; **(6)** the sweep survey is a full-bucket unpaged enumeration → §4.4 prefix-scoped survey; **(7)** the orphan `expected`-set membership floods under prefix keys → recovered-anchor membership; **(8)** `anchorID` enters a subject filter → §3.4 metacharacter hardening; plus the §3.1 disjointness argument rewritten to the real one, legacy stragglers of departed actors handled in Fire 3's purge, and the four multi-key delete consumers (incl. shred at `MaxInt64`) enumerated. No pre-build gate is left open for the Steward.
