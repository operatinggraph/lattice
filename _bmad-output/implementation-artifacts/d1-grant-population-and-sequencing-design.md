# Design — D1.3: populate the grant table, and the un-thrashed sequence to the demo

**Status: ✅ Andrew-ratified (2026-06-29) — build-ready** · Author: Winston (Designer fire, 2026-06-29) · Supersedes the
implicit "grants already exist" assumption in
[`first-postgres-read-model-and-read-boundary-design.md`](first-postgres-read-model-and-read-boundary-design.md)
§R4.

> **For Andrew (one-look ratification).**
>
> **The thrash you flagged, root-caused (one sentence):** D1.1 projected the base `cap-read` self-anchor to a
> **NATS-KV** bucket, but the RLS enforcement boundary (Path A, the ratified end-state) reads **Postgres
> `actor_read_grants`** — and *every* downstream piece (the D1.3 design §R4, my Fire-2 filing, the just-filed
> "grant-population gap") silently **assumed the self-anchor was already in Postgres**. It never was. So Verticals
> built the protected read model (Fire 2 ✅), found the grant table empty, and bounced it back — the ping-pong.
>
> **What this design does:** closes that **one keystone** — the base cap-read lens's *missing Postgres
> projection* (§6.14 already mandates it: every read-grant lens "**also** projects to `actor_read_grants`") — and
> then **grounds the entire remaining D1.3 chain end-to-end** so it builds straight to your headline demo (*A's
> JWT sees only A's applications; B denied; unauth → 401*) **with no further cross-lane discovery**. I walked the
> whole chain and pre-empted the *next two* bounces (below), so what's left is one Lattice fire + one Verticals
> fire, in that order, with a fully-specified hand-off — not an open-ended back-and-forth.
>
> **No architectural fork** (Path A / Postgres-RLS is already ratified — §6.14). **No frozen-contract change**
> (this *builds to* §6.14 as written — the dual NATS-KV-doc + Postgres-grant-table projection is already in the
> contract; the multi-Lens decomposition note already says the second projection is a separate lens). So this is
> a **ratify-and-build** design, not a fork to adjudicate.
>
> **The two bounces I caught while grounding (so they don't become the next ping-pong):**
> 1. **The bootstrap lens-seeder can't express a Postgres lens at all** — `makeLensSpecBody` hardcodes
>    `targetType: "nats_kv"` (`primordial.go:1030`, and the comment at `:938` says so outright). The keystone
>    therefore *first* needs the bootstrap analog of the package declaration surface that just shipped
>    (`pkgmgr.LensSpec` posture passthrough, `c1a8901`). Folded into Increment 1 below — not a surprise mid-build.
> 2. **`internal/gateway/auth` only *verifies* JWTs; nothing *issues* them** (`auth.go:170` `Verify`, no `Sign`).
>    Fire 3's e2e must **mint** A's/B's tokens with a test IdP key (the Verifier accepts asymmetric keys). A real
>    login flow is the **deferred Gateway (Option C)** — explicitly out of scope for the D1.3 demo. Named in
>    Increment 2 so Fire 3 doesn't bounce on "how do we get a token."

**Backlog rows:** `lattice.md` → *Read-model / projection maturity* → "Populate Postgres `actor_read_grants` —
the cap-read self-anchor GrantTable projection" (this is Increment 1, the keystone); `verticals.md` → the D1.3
Fires 2–3 row (Fire 3 = Increment 2).

---

## 1. Problem & intent — why D1 keeps bouncing

D1 (read-path authorization) decomposed cleanly on paper: Lattice builds the platform RLS machinery, Verticals
builds the loftspace consumer. In practice it has ping-ponged because **the producer of the data RLS reads — the
rows in `actor_read_grants` — was never assigned to a fire.** It fell *between* the two lanes:

- The D1.3 design's decomposition (§7) listed **Fire 1** (Refractor *provisions* the grant table + RLS) and
  **Fires 2–3** (the loftspace *protected read model* + the *read boundary*). It assumed the grants themselves
  would be there — "depends on D1.1b" (the residence slice) — and the **applicant-self milestone (§R4)** went
  further: *"the shipped base `cap-read.<actor>` self-anchor already grants each applicant their own NanoID → RLS
  matches → A sees only A."*
- **That sentence is false.** The base `cap-read` lens (`bootstrap.CapabilityReadLensDefinition`, D1.1) is an
  `actorAggregate` lens projecting a `readableAnchors[]` document to the **NATS-KV `capability` bucket**
  (`TargetBucket: "capability"`). It does **not** write `actor_read_grants`. Grep confirms **no lens anywhere
  declares `grantTable: true`** — so the Postgres grant table, which a protected lens's activation *provisions*
  (`cmd/refractor/main.go:242`), is **created empty and stays empty.**

So Verticals shipped the protected read model (Fire 2, `446567e`), correctly found that RLS would deny-all
against an empty grant table, and filed the gap back to Lattice. **That's the bounce.** The fix is not "build the
grant projector and hand back again" — it is to **close the keystone *and* lay out the complete remaining
sequence so the chain runs to the demo without another mid-build discovery.**

**Why the self-anchor in Postgres is the whole keystone.** For the milestone (one applicant reading their own
applications), RLS needs exactly one grant per actor: *actor A may read anchor A* (`grant_source = cap-read`).
The protected read model already carries `authz_anchors = [applicantNanoID]` (Fire 2 ✅). The moment
`actor_read_grants` contains `(A, A, cap-read)` for every identity, the §6.14 set-membership policy returns A's
rows to A and nobody else's. **One producer, and the entire D1.3 demo lights up.**

---

## 2. Grounding — what exists, and the contract that already blesses the fix

| Piece | State | Reference |
|---|---|---|
| **Protected read model** `read_lease_applications` (`authz_anchors=[applicantNanoID]`) | ✅ Shipped (Fire 2) | `packages/lease-signing/lenses.go` |
| **RLS provisioning** — `BuildProtectedTableDDL` + `FORCE RLS` + the set-membership policy; `actor_read_grants` table + the §6.14 seq-guard | ✅ Shipped (Fire 1a/1b) | `internal/refractor/adapter/rls.go`, `read_path_adapters.go` |
| **`GrantWriterAdapter`** — projects a `(actor_id, anchor_id, grant_source)` row into `actor_read_grants`; `Delete`→`RevokeGrant` (soft-tombstone); seq-guarded | ✅ Shipped, **no producer yet** | `internal/refractor/adapter/read_path_adapters.go:10-61` |
| **Package** declaration of `protected`/`grantTable`/`columns` + DSN-from-env | ✅ Shipped (`c1a8901`) | `internal/pkgmgr/{definition,build}.go` |
| **`nanoIdFromKey`** auth-plane cypher fn (fail-closed) | ✅ Shipped | `internal/refractor/ruleengine/full/executor.go` |
| **Base `cap-read` self-anchor** → **NATS-KV** `capability` bucket (`readableAnchors[]`) | ✅ Shipped (D1.1) — **wrong target for RLS** | `bootstrap.CapabilityReadLensDefinition` |
| **Bootstrap lens-seeder** | **NATS-KV only** — `makeLensSpecBody` hardcodes `targetType:"nats_kv"`; `LensDefinition` has no posture fields | `internal/bootstrap/primordial.go:938,1001,1030`; `lenses.go:7` |
| **JWT seam** `internal/gateway/auth` | ✅ Shipped — **verify-only**, no issuer | `auth.go:170` `Verify`; `auth.go:315` `Authenticate` |
| **`cap-read.residence`** (loftspace landlord audience) | **Not built** — also needs a landlord→unit ownership link loftspace-domain doesn't model | D1.1b / D1.3 §R4 |

**The contract already mandates the fix — no §6.14 change.** §6.14 (`docs/contracts/06-capability-kv.md`):

- L547–548 *"The merge point — the Postgres `actor_read_grants` table (Path A). Every read-grant lens **also**
  projects to a shared table…"* — i.e. each `cap-read.*` lens is **supposed** to project to Postgres. D1.1
  shipped only the NATS-KV half; the Postgres half is **missing**, not **disallowed**.
- L45 (the multi-Lens architectural note) *"each Lens has one RETURN producing one shape; multi-output patterns
  are expressed as **additional Lenses**, not Lens-internal complexity … The same pattern applies to … Postgres
  RLS link mirroring."* — so the Postgres projection is a **separate lens**, not a second target bolted onto the
  NATS-KV one. This is exactly how packages will add their `cap-read.<domain>` grant lenses, so the base must set
  the precedent the same way.
- L604–609 confirms the **NATS-KV `cap-read.*` projection is the Path-B transitional scaffold** (the read-gateway
  filter), and Path B is *not being built* (Path A / RLS is the ratified boundary). So the existing NATS-KV
  cap-read lens is an **audit/transitional artifact with no live consumer** — see the reconciliation note (§5).

**Net:** the missing piece is a single **base cap-read Postgres `GrantTable` lens**, contract-blessed, fork-free.

---

## 3. The shape — the keystone, and the full chain to the demo

### 3.1 Increment 1 (Lattice — THE KEYSTONE, build first)

**(a) Teach the bootstrap lens-seeder the read-path posture** (the bootstrap analog of `c1a8901`). Extend
`bootstrap.LensDefinition` with the read-path fields it lacks — `Adapter` (default `nats-kv`), `Table`,
`Protected`, `GrantTable`, `Columns []PostgresColumn` — and extend `makeLensSpecBody` (`primordial.go:1001`) to
emit a **postgres** `targetType` + a `targetConfig` carrying `{grantTable, …}` when `Adapter == "postgres"`
(mirroring `pkgmgr.lensSpecBody`'s just-shipped serialization, and resolving an empty DSN from `REFRACTOR_PG_DSN`
the same way). The NATS-KV path stays byte-identical (every existing primordial lens unchanged). This is small
and mechanical — it is the *exact* transform the package side already proved.

**(b) Add the base `capabilityReadGrants` primordial lens** — a **plain** (one-row-per-identity) Postgres
`GrantTable` lens projecting each actor's **self-grant**:

```
MATCH (id:identity)
RETURN nanoIdFromKey(id.key) AS actor_id,
       nanoIdFromKey(id.key) AS anchor_id,
       'cap-read'            AS grant_source
```

- `Adapter: "postgres"`, `GrantTable: true` → Refractor defaults `Table = actor_read_grants` and
  `IntoKey = (actor_id, anchor_id, grant_source)` (the platform composite — `corekv_source.go:450`), and wraps it
  in the seq-guarded `GrantWriterAdapter`. The lens need only `RETURN actor_id/anchor_id/grant_source`.
- `grant_source = 'cap-read'` is the base slice's source id (the §6.14 convention — the lens owns/retracts only
  its own rows; package slices use `cap-read.residence` etc., so they never collide with the base).
- **Tombstone retraction is already handled:** a soft-deleted identity is the *anchor* of this plain projection,
  so the **anchor-tombstone retraction just shipped** (`full.Engine.AnchorDeleteResult`, `679fe25`) emits a
  `Delete` keyed by `(actor_id, anchor_id, grant_source)` → `GrantWriterAdapter.Delete` → `RevokeGrant`
  (`is_deleted=true`, seq-guarded). So a deactivated actor loses its read grant the moment its vertex tombstones,
  with no resurrection on a stale replay (H4). *(This is a clean reuse — the retraction primitive landed for the
  clinic tombstone item and serves this for free.)*
- It is a **separate lens** from the existing NATS-KV `capabilityRead` (the multi-Lens note, L45) — same anchor,
  one RETURN each.

**Acceptance (Increment 1 is provable on its own, no vertical needed):** a `POSTGRES_TEST_DSN`-gated test seeds a
few identities through the real bootstrap, activates `capabilityReadGrants`, and asserts `actor_read_grants`
holds `(idNanoID, idNanoID, 'cap-read')` for each; a tombstoned identity's grant flips `is_deleted=true` and a
stale-seq replay does not resurrect it. **Green = the grant table is populated for every actor.** This serves
**clinic and every future protected model**, not just loftspace — it is the platform's read-grant base producer.

### 3.2 Increment 2 (Verticals — Fire 3, builds AFTER Increment 1 is green)

The read boundary in `cmd/loftspace-app`, per the D1.3 design §3.3 (Option A, already ratified) — **with its full
prerequisite set enumerated here so nothing is discovered mid-build:**

1. **A `pgxpool`** in loftspace-app (the app gains a Postgres dependency it does not have today; `make
   up-loftspace` wires `REFRACTOR_PG_DSN`/the app DSN).
2. **A non-superuser, `SELECT`-only DB role** for the app (superuser/`BYPASSRLS` skips RLS; `FORCE RLS` makes the
   policy apply even to the owner). Provision it where the stack provisions Postgres (docker-compose seed / a
   bootstrap SQL the e2e runs). `SELECT`-only bounds the blast radius: a compromised app can mis-set `actor_id`
   but cannot forge a grant row.
3. **`handleApplications`** → authenticate the JWT via the shared `internal/gateway/auth` (D1.2) → `BEGIN` a
   per-request txn on a pooled conn → `SET LOCAL lattice.actor_id = <verified actor NanoID>` → `SELECT … FROM
   read_lease_applications` (RLS auto-scopes; **no** auth `WHERE`) → `COMMIT` (discards `SET LOCAL`, conn returns
   clean). **Delete** the `weaver-targets` `KVListKeys` + client-side `?applicant=` filter (the leak being
   closed). *(`SET LOCAL` not `SET` — txn-scoped, the pooling-safety crux, design §3.3.)*
4. **Token issuance for the e2e** (bounce #2, pre-empted): `gateway/auth` is verify-only, so the e2e **mints** A's
   and B's tokens with a **test IdP signing key** (asymmetric — the Verifier rejects `none`/HS\*) and presents
   them as `Authorization: Bearer`. A **real login flow is the deferred Gateway (Option C), explicitly out of
   D1.3 scope** — the demo proves *enforcement*, not *login*.
5. **The headline e2e (full 3-layer — the enforcement turn-on):** ephemeral `make up-loftspace` + Postgres; seed
   A, B; **A's JWT** → `GET /api/applications` returns **only A's**; **B's JWT** → only B's; **unauth → 401**; a
   request that sets `?applicant=B` while authed as A → **still only A** (RLS keys off the session var, not the
   param); a **forged/expired JWT → 401** (D1.2). This is the D1.4 Gate-3 read-bypass vector set on a live model.

### 3.3 Increment 3 (later — NOT needed for the demo)

The **landlord/residence audience**: a loftspace `cap-read.residence` package GrantTable lens (a second anchor on
each application's row for the unit's owner/manager) — which additionally needs a **landlord→unit ownership
link** loftspace-domain does not model yet. And the **primordial root-read scope** (the kernel-seeded all-access
anchor, the read analog of the write base's `scope:"any"` grant) — deferred in D1.1 pending the wildcard-anchor
representation. Both are real, both are **post-milestone**; neither blocks the headline demo. Filed as follow-ons,
not sequenced into the demo path.

---

## 4. The un-thrashed sequence (this is the deliverable Andrew asked for)

```
Increment 1 (Lattice — internal/bootstrap)         Increment 2 (Verticals — cmd/loftspace-app)
────────────────────────────────────────────       ────────────────────────────────────────────
1a. bootstrap seeder learns postgres/grantTable  →  (waits on 1)
1b. capabilityReadGrants base GrantTable lens    →  3. pgxpool + non-superuser SELECT-only role
    → actor_read_grants populated (self-grants)  →  4. authenticate → SET LOCAL → query; delete the leak
    [gated test proves it; serves all verticals] →  5. e2e: A-sees-only-A, B denied, unauth 401  [full 3-layer]
```

**Why this stops the thrash:**

- **There is exactly ONE remaining Lattice-lane piece** (Increment 1, the grant producer) and **one Verticals
  piece** (Increment 2, the consumer), in **strict order**, with a **fully-specified one-way hand-off**: Increment
  1's acceptance (*`actor_read_grants` is populated and provable by a gated test*) is precisely Increment 2's
  precondition. No open question is left for the consumer to discover.
- **The two hidden gaps that would have caused the next bounces are folded in now** (the bootstrap seeder
  extension is part of Increment 1; the e2e token-minting + DB-role are part of Increment 2's enumerated
  prerequisites). The chain has been walked to the demo; there is nothing left to find mid-build.
- **It respects the disjoint-lane model without splitting a sequential chain across parallel fires.** Increment 1
  is a *platform* primitive (it serves clinic + every protected model, so it genuinely belongs in Lattice), and
  it lands **before** Verticals starts Increment 2 — so the two never run concurrently on coupled code, and there
  is no back-and-forth, just a baton pass in one direction.

**Operational note (name it, don't surprise anyone):** once Increment 1 lands, **the kernel's base read-auth
depends on Postgres at lens-activation** (Refractor needs `REFRACTOR_PG_DSN` + Postgres up to populate
`actor_read_grants`). That is inherent to Path A (RLS *is* Postgres), the `contract_view` bootstrap lens already
takes a Postgres dependency, and `make up` already starts Postgres — so this is a documentation point, not new
infra. The NATS-KV `capabilityRead` lens keeps working regardless (it has no Postgres dependency).

---

## 5. Reconciliation with the existing mental model (pre-empt "but didn't we…?")

- **"Didn't D1.1 already build the read grants?"** It built the **NATS-KV** half (`readableAnchors[]` to the
  `capability` bucket — the Path-B transitional scaffold). It did **not** build the **Postgres** half that RLS
  (Path A, the ratified boundary) reads. §6.14 mandates *both* ("every read-grant lens **also** projects to
  `actor_read_grants`"); only the "also" was shipped-as-NATS-KV-only. This design adds the missing Postgres
  projection.
- **"Is the NATS-KV `capabilityRead` lens now dead?"** Its only consumer is the **Path-B read-gateway filter,
  which is the explicitly-transitional scaffold §6.14 L604 says is *not* an end-state** and which is *not being
  built* (Path A is the boundary). So today it is an **audit/transitional artifact with no live reader.** I am
  **not** retiring it in this design — it is shipped, contract-mentioned (the `cap-read.<source>.<actor>` doc
  shape, L514), and harmless — but I flag it as a **candidate for a later cleanup** once Path A is fully on (the
  honest "this scaffold has served its purpose" call, separate from this keystone). Calling it out here so it is
  not mistaken for load-bearing.
- **"Does this duplicate a pattern, or introduce new state?"** Neither. It **mirrors** the §6.1
  contract-contribution decomposition the write-path already uses (base lens in core, `*.<domain>` slices in
  packages) and the **multi-Lens** rule (separate lens per RETURN). The state it writes — `actor_read_grants` —
  **already exists** (Fire 1 provisions it); this design only supplies the **producer** the table was built to
  receive. The `GrantWriterAdapter`, the seq-guard, and the anchor-tombstone retraction are all **already
  shipped** — this is the smallest possible "use what we have."
- **"Why a new lens, not a second target on the NATS-KV one?"** The contract's own multi-Lens note (L45): one
  RETURN per lens; a second output shape is a second lens. A flat `(actor_id, anchor_id, grant_source)` grant row
  is a *different shape* from the `readableAnchors[]` aggregate doc — so it is a separate lens, and the base sets
  the exact precedent packages will follow for `cap-read.<domain>`.

---

## 6. Contract surface, risks, alternatives

**Contract surface.** **No frozen-contract change.** §6.14 already specifies the dual projection + the
`actor_read_grants(actor_id, anchor_id, grant_source, projection_seq, is_deleted)` shape + the seq-guard + the
multi-Lens decomposition. A short `docs/components/refractor.md` (or `bootstrap`) note records "the base cap-read
grant producer + the bootstrap seeder's postgres support" — a component-doc note, direct to main, not a
`docs/contracts/*` edit.

**Risks.**

- **R1 — bootstrap now seeds a Postgres lens (new surface in the kernel seeder).** *Mitigation:* it is the exact
  transform `c1a8901` already proved on the package side; the NATS-KV seed path stays byte-identical; the gated
  test pins the new path; and the seeder fails closed (empty DSN + unset env = hard error, no silent
  dev-localhost default — same posture the package side took).
- **R2 — empty grant table = total read outage (not a leak).** If Increment 1 regresses, RLS denies *all*
  protected reads (FORCE-RLS H3). This is **fail-closed** (an outage, never a leak), and it is exactly why
  Increment 1 ships **before** Increment 2 with its own gated proof — the consumer never runs against an
  unproven producer.
- **R3 — the self-anchor is the *only* grant for now.** A landlord cannot yet read their units' applications
  (Increment 3). *Mitigation:* the milestone is explicitly applicant-self; Increment 3 is filed, not forgotten.

**Alternatives considered.**

- **Retarget the existing NATS-KV `capabilityRead` lens to Postgres (drop NATS-KV).** Rejected: it changes a
  shipped, contract-mentioned artifact and violates the multi-Lens "one RETURN per lens" rule (the grant row is a
  different shape). Adding a separate lens is lower-churn *and* contract-correct, and leaves the audit doc intact.
- **Make the base lens dual-target (one lens → NATS-KV + Postgres).** Rejected for the same multi-Lens reason —
  and the package `cap-read.<domain>` lenses will be single-RETURN GrantTable lenses, so the base must match.
- **Have loftspace declare the self-anchor grant in its own package.** Rejected: the self-anchor is **core** (every
  actor reads its own vertex, package-independent) — §6.14 L504 puts the base lens in core. A package self-anchor
  would duplicate it per-package and scatter the read-auth base. The base belongs in bootstrap.
- **Skip the grant table; let the app filter by the verified actor in Go.** Rejected: that is the *write-path's
  pre-auth leak* reborn on the read path (a forgeable app-side filter) and abandons RLS — the entire point of D1.

---

## 7. Test strategy

- **Increment 1:** `POSTGRES_TEST_DSN`-gated bootstrap test — fresh seed → `actor_read_grants` holds `(id, id,
  'cap-read')` per identity; tombstone → `is_deleted=true`; stale-seq replay → no resurrect (H4). Plus a
  seeder-unit test that `makeLensSpecBody` emits a postgres `grantTable` targetConfig for a postgres
  `LensDefinition` and stays byte-identical for nats-kv. Gates: build, vet, golangci, STRICT lint-conventions,
  `verify-kernel`, the refractor read-path suite.
- **Increment 2:** the §3.2 headline e2e (A-sees-only-A / B-denied / unauth-401 / `?applicant=B`-defeated /
  forged-JWT-401) on an ephemeral `up-loftspace` + Postgres; plus a `SET LOCAL` pooling-safety unit test (a second
  request on the same pooled conn inherits no actor). **Full 3-layer** (the enforcement turn-on).
- The convergence e2e suites stay green throughout (the write path is untouched).

---

## 8. Open questions — resolved (my calls, per decide-don't-defer)

1. **Grant target = Postgres `actor_read_grants` via a separate base GrantTable lens** (not retarget, not
   dual-target, not package-local). *Resolved* — §6.14 + the multi-Lens note settle it; the only thing for Andrew
   is ratifying the design, not the target.
2. **`grant_source = 'cap-read'`** for the base self slice (packages use `cap-read.<domain>`). *Resolved.*
3. **The bootstrap seeder gains Postgres/grantTable support as part of Increment 1** (not a separate item).
   *Resolved* — it is the unavoidable sub-gap, scoped in.
4. **E2e mints tokens with a test IdP key; real login is the deferred Gateway.** *Resolved* — D1.3 proves
   enforcement, not login.
5. **Increment 1 lands before Increment 2; that is the only remaining cross-lane hand-off, one-way, fully
   specified.** *Resolved* — the sequencing in §4 is the anti-thrash deliverable.

Nothing here is a fork or a contract change, so **ratification is a single yes**: confirm the keystone shape +
the sequence, and the Lattice Steward builds Increment 1 immediately, then Verticals builds Increment 2 against a
proven grant table.

---

*Designer: Winston (lattice-designer) · 2026-06-29 · grounds: the ratified D1 design (Path A / `actor_read_grants`
end-state), Contract #6 §6.14 (L45 multi-Lens, L504 base-in-core, L547 the merge point, L604 Path-B-transitional),
`bootstrap.CapabilityReadLensDefinition` + `primordial.go:938-1032` (NATS-KV-only seeder), `internal/refractor/
adapter/read_path_adapters.go` (GrantWriterAdapter), `internal/refractor/ruleengine/full` (nanoIdFromKey +
AnchorDeleteResult), `internal/gateway/auth/auth.go` (verify-only), the D1.3 design §3.3/§7, and the just-shipped
Fire 2 (`446567e`).*
