# Read-path authorization (D1) — design

**Status: 📐 awaiting-Andrew (ratification).** Author: Winston (Designer fire, 2026-06-26).
Backlog row: `planning-artifacts/backlog/lattice.md` → *Security & trust boundary → Read-path
authorization (D1)* (★★★, L). Grounds in `lattice-architecture.md` D1 (pre-written rubric),
Contract #6 (Capability KV), Contract #10 §10.2, brainstorming #38/#61/#118, and the vault
*Refractor* / *Personal Lens* subdocs.

---

## For Andrew (one-look ratification block)

**What it does, in two lines.** Closes NFR-S2: today every read (Loupe, the vertical apps) reads
lens targets **directly**, bypassing the write-path Capability boundary — anyone who can reach the
read store sees the whole graph. D1 makes reads pass the *same* ReBAC boundary writes pass:
**(1)** the reader is an **authenticated actor** (signed JWT → `lattice.actor_id`), and **(2)** a
new **Capability-Read Lens** projects each actor's readable-resource set so the read store can filter
rows to what that actor is allowed to see — the read-path mirror of Capability KV.

**Two forks I designed through — your call on both:**

1. **Where read-authorization is *enforced* (the central fork).** The rubric's leading approach is
   **Postgres RLS**, but *today every app reads NATS-KV read-model buckets, not Postgres* (verified:
   `cmd/loftspace-app`, `cmd/clinic-app`, Loupe all use `KVGet`/`KVListKeys`; no Postgres-target lens
   ships), and **NATS-KV has no row-level security** (brainstorm line 498 names this exact gap).
   - **Path A — Postgres-RLS (my recommendation as the strategic target).** Migrate *protected*
     business read models to Postgres; the Capability-Read Lens projects actor→readable-anchor grants
     to a Postgres table; RLS policies on the business tables `JOIN` against it; the read boundary sets
     `lattice.actor_id` per DB session. Enforcement is **DB-native** (a buggy app cannot leak rows) and
     **contract-anticipated** — Contract #10 §10.2 already says the authz-anchor "carries… **there**
     [the Phase-3 Postgres read-path]". **Cost:** a read-model migration for protected data + a read API
     that sets the session var. *I recommend A as the destination.*
   - **Path B — read-gateway over NATS-KV (the rubric's documented fallback; lowest churn now).** A
     read-authorization service resolves the actor's read-capability set (a Capability-Read Lens
     projected to a NATS-KV `cap-read.<actor>` bucket) and **filters read-model rows by authz-anchor**
     before returning them. Keeps today's NATS-KV reads; no migration. **Cost:** re-implements RLS in a
     proxy (wider bypass surface — the store itself stays unguarded), and every app must read *through*
     the service instead of raw `KVGet`.
   - **Path C — Personal/Secure Lens fan-out** (vault *Personal Lens*): per-identity filtered NATS
     subjects. This is the **Edge end-state**, not D1's first delivery; D1's Capability-Read Lens is
     exactly the input it needs, so D1 composes *toward* C. Out of D1 scope.
   - **My recommendation, sequenced:** build the **target-store-agnostic Capability-Read Lens first**
     (it is the auth source of truth under either path), then enforce **Path A** for protected data,
     using the already-tracked **Postgres read-model** + **Postgres `Truncater`** backlog items as D1's
     vehicle. If you judge the read-model migration too costly now, **Path B is the ratified interim** —
     same lens, different enforcement seam, no rework of the auth source of truth. The fork is *which
     enforcement seam ships first*, not *which lens* — that part is common.

2. **Authentication / Gateway seam (sub-fork).** D1 needs "who is reading," and today there is **no
   authN** (Loupe/apps run as one trusted identity; `internal/gateway` is unbuilt). I recommend
   building the **minimal read-actor auth seam** (signed-JWT keyed to the Identity vertex → verified →
   `lattice.actor_id`; reuse `internal/gateway/auth` + a token-revocation KV — brainstorm #118/#111) as
   **D1 increment 1**, and **deferring the full internet-facing Gateway** (NGINX/Envoy hardening, IdP
   integration — `lattice-architecture.md` "Gateway Architecture Decision") to ops. This unblocks D1
   without staffing the whole Gateway epic.

**Frozen-contract change (uncommitted, staged as the proposal-diff).** `docs/contracts/06-capability-kv.md`
gets a new **§6.14 — Read-path authorization (D1)**: the `cap-read.<actor>` shape (read-path mirror of
§6.2), the **authz-anchor column** convention for protected read-model lenses, and the read-boundary
dispatch. Edited in `main`, **left uncommitted** for your ratification (the diff *is* the proposal).
Affected consumers: Refractor (new lens class), the read boundary (Postgres RLS / read-gateway), and
every protected business lens (must project the anchor). No change to the write-path §6.2–§6.13.

---

## 1. Problem & intent

**The gap (NFR-S2 / D1).** Lattice's authorization boundary is the **write path**: the Processor reads
Capability KV at commit step 3 and denies any op the actor isn't granted (Contract #6). **Reads have no
such boundary.** A lens target — a NATS-KV read-model bucket today, Postgres/ES tomorrow — can be read
directly, returning the full projected graph to anyone who can reach the store. `lattice-architecture.md`
D1 states it plainly: *"Lens targets (Postgres/ES/streams) can be read directly, bypassing the
Capability-Lens write-path boundary (NFR-S2)."* This is the single largest standing security gap and
the explicit reason Loupe is scoped as a **trusted single-identity tool with no read-path auth** — D1 is
what a multi-identity, Gateway-fronted, or Edge deployment needs before it can exist.

**The intent (the vision this serves).** The vault is unambiguous about the end-state
(`Lens and Refractor/The Refractor.md` §3): *"The Refractor doesn't just move data; it moves
**permissions** … ReBAC-to-RLS mirrors the Lattice's ReBAC into the target's Row-Level Security, so a
user's query is automatically filtered by their real-time authorization paths."* The brainstorm names
the mechanisms: **#38** (RLS policy generator — *"mirrors auth links into target store"*), **#61**
(Refractor "Secure Lens" type with RLS), **#118** (the `Lattice-Actor` JWT trust model — actor claim as
a **signed JWT keyed by Identity vertex**, verified, because "if only the entry-gateway trusts itself to
set the actor, any internal misbehavior = total impersonation"). D1 is the first concrete step of that
arc, and the direct prerequisite for **Personal Lens** and the **Edge Lattice** (vault
*Edge Lattice/Personal Lens.md*: the per-user filtered stream *is* RLS applied to a NATS subject — same
ReBAC source, different target).

**The architectural principle that makes it tractable.** *"Everything derived from Core KV is a Lens."*
The write path solved O(1) authz by projecting actor→grants into Capability KV via the **Capability Lens**
(Contract #6). The read path is the **symmetric move**: a **Capability-Read Lens** projects
actor→readable-resources, and the read store filters against it. The ReBAC traversal still runs **once**
in the Refractor (single source of truth = Core KV); the read store does cheap filtering, never graph
walks. D1 is not a new auth model — it is the existing auth model, projected for reads.

---

## 2. Grounding — the pattern this extends (do not redesign it)

**Write-path auth, as it works today (the mirror).**
- The **Capability Lens** (`vtx.meta.lens.capability` + the decomposed `cap.roles`/`cap.svc`/`cap.ephemeral`
  package lenses) walks `actor → roles → permissions`, `actor → residence → services`,
  `actor → tasks → granted-ops` and writes a flat per-actor doc to **Capability KV** (`cap.<actor>`,
  Contract #6 §6.1/§6.2). It is a **lens target** — Refractor is the sole writer (P2), the Processor
  reads (P5-for-auth).
- The Processor at **commit step 3** does a **single GET** by actor key and dispatches
  (task → service → platform, Contract #2 §2.8). **"No entry = no access"** (§6.8) — absence denies; no
  anonymous fallback.
- Security-critical invariants D1 must inherit verbatim: **projection correctness = auth correctness**
  (§6, NFR-S2); the **monotonic `projectionSeq` write-ordering guard + soft-tombstone** (§6.2/§6.8 — a
  stale replay must never resurrect a revoked grant); **fail-closed on the security plane** (an
  auth-plane `actorAggregate` lens whose MATCH uses an uncovered construct **fails activation**,
  §6.13). D1's read-grant projection is an `actorAggregate` lens and gets these for free.

**The read path, as it actually is today (the reality the fork turns on).**
- All vertical apps + Loupe read **NATS-KV read-model buckets** via `conn.KVGet` / `conn.KVListKeys`
  (verified in `cmd/loftspace-app/{listings,applications,objects,identities,tasks}.go`,
  `cmd/clinic-app/*`). **No Postgres-target lens ships.** The Postgres adapter
  (`internal/refractor/adapter/postgres.go`) exists but is unexercised (and lacks `Truncater` — its own
  backlog row).
- Contract #10 §10.2 is explicit that the shared `weaver-targets` bucket is **internal Weaver-only
  operational state, "never on the read-path,"** and that *"if a target Lens is also projected to the
  Phase-3 Postgres read-path, it carries the D1 authz anchor **there**."* So the contracts have already
  decided the **proper read-path surface is a dedicated business read-model lens carrying the authz
  anchor — not `weaver-targets`** — and have pre-positioned that anchor for Postgres. (Today's apps
  reading `weaver-targets` is a known P5 expedient, noted in memory; D1 is the moment to give protected
  data a *real* read-path surface.)
- **No authentication exists.** `internal/gateway` and `internal/identity/{capability,rebac}` are
  empty/unbuilt. Loupe and the apps connect to NATS as one trusted identity.

**Net:** D1 = (a) authenticate the reader, (b) project actor→readable-resources as a lens, (c) enforce
at the read boundary. (b) is common to all paths; (a) and (c) are the forks.

---

## 3. The shape

### 3.1 The Capability-Read Lens (common to every path — the heart of D1)

A new **auth-plane lens**, `capabilityRead`, projecting per-actor **read** grants — the symmetric twin
of the write-path `capability` lens. It is an **`actorAggregate`** lens (Contract #6 §6.13), so it
inherits the narrow reverse-traversal invalidation, the `projectionSeq` guard, the soft-tombstone, and
fail-closed activation.

- **Source paths (walk Core KV topology, same vocabulary as the write path):** for each actor, resolve
  the set of **resource anchors** the actor may read. The first delivery reuses the read-relevant subset
  of the write-path traversal: `actor → residesIn/leases/containedIn → locations` (residence scope),
  `actor → holdsRole → role → grants` (role-derived read scope), and direct ownership links. The output
  is **not** the op vocabulary — it is a flat set of **anchor identifiers** (vertex keys / NanoIDs / type
  tags) the read store can match a row's authz-anchor against.
- **Output shape — `cap-read.<actor-suffix>`** (a disjoint key space in the Capability KV bucket,
  exactly the multi-lens-one-bucket pattern of §6.1):

  ```json
  {
    "key": "cap-read.identity.Hj4kPmRtw9nbCxz5vQ2y",
    "actor": "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y",
    "version": "1.0",
    "projectedAt": "2026-06-26T14:32:18.142Z",
    "projectionSeq": 10481,
    "readableAnchors": [
      { "anchorType": "identity", "anchorId": "Hj4kPmRtw9nbCxz5vQ2y", "via": ["self"] },
      { "anchorType": "unit",     "anchorId": "Lk2Pn6mQrtwzKbcXvP3T", "via": ["residesIn"] },
      { "anchorType": "lease",    "anchorId": "Op4Nb2mPq6rTwzKxVyP7", "via": ["leases"] }
    ]
  }
  ```

  `readableAnchors[]` is the read analog of `serviceAccess[]`/`platformPermissions[]`: a flat,
  pre-resolved set, with `via` for auditability ("why can this actor read this?" — same role as
  §6.5 `resolvedVia`). **"No entry = no read"** mirrors §6.8: absence denies; there is no public-read
  fallback (a non-protected lens that is intentionally public simply declares no authz-anchor — see
  §3.2).

- **Write path (P2):** none directly — it is a **lens**, produced by the Refractor from Core KV CDC.
  Defining/installing it is an **op** (`InstallPackage` / a bootstrap meta-vertex), never a KV write.
- **Read path (P5):** the read boundary GETs `cap-read.<actor>` (NATS-KV) **or** the lens additionally
  projects to a Postgres `actor_read_grants` table for RLS to `JOIN` (Path A). One lens, one or two
  targets — the standard multi-target lens adapter pattern.

### 3.2 The authz-anchor column convention (protected read-model lenses)

Every **protected** business read-model lens must project an **authz-anchor** column identifying the
resource-scope a row belongs to (the unit, the lease, the owning identity). This is the join key between
a row and `readableAnchors[]`. The convention (formalized in the §6.14 contract edit):

- Column name **`authzAnchor`** (camelCase per the KV convention), value `<anchorType>.<anchorId>`
  (e.g. `unit.Lk2Pn6mQrtwzKbcXvP3T`).
- A lens with **no `authzAnchor`** column is **public-read** (explicit opt-out — e.g. a public listings
  index). The read boundary returns its rows unfiltered. *Absence is opt-out, not a silent bypass:* the
  conventions linter (a follow-on lint, see §6) flags any lens whose target is marked `protected: true`
  but projects no `authzAnchor`.
- This is exactly Contract #10 §10.2's "carries the D1 authz anchor there" promise, generalized from
  the Postgres-only phrasing to **any** protected target.

### 3.3 The read-authorization boundary (the fork — both paths designed)

**Path A — Postgres-RLS (recommended destination).**
- The `capabilityRead` lens projects a second target: a Postgres table
  `actor_read_grants(actor_id, anchor_type, anchor_id)` (the NATS-KV adapter enforces `projectionSeq`;
  the Postgres adapter is guard-exempt per §6.2 — RLS reads current rows, and CDC-ordered upserts
  converge).
- Each protected business read model is a Postgres table with an `authz_anchor` column and an **RLS
  policy**: `USING (authz_anchor IN (SELECT anchor_type||'.'||anchor_id FROM actor_read_grants WHERE
  actor_id = current_setting('lattice.actor_id')))`.
- The **read boundary** (a thin read API, or the Gateway translator extended for reads) authenticates
  the JWT, then opens the DB session with `SET LOCAL lattice.actor_id = '<verified actor>'`. RLS does the
  rest — **DB-native, unbypassable by app code**. This is brainstorm #38's "RLS policy generator" and the
  vault's ReBAC-to-RLS, realized.
- **Migration vehicle:** the already-tracked **Postgres read-model** + **Postgres `Truncater`** backlog
  items are D1's natural carrier — protected read models move to Postgres as D1 lands them; public/
  operational read models stay NATS-KV.

**Path B — read-gateway over NATS-KV (ratified interim if A's migration is too costly now).**
- A small **read-authorization service** (extends the Gateway translator, or a sidecar the apps call):
  authenticate JWT → GET `cap-read.<actor>` → list/scan the requested read-model bucket → **filter rows
  whose `authzAnchor` ∉ `readableAnchors`** → return. Apps read *through* the service, not raw `KVGet`.
- Honest trade-off (the rubric's reason A is "leading"): the store itself stays unguarded, so the
  security depends on every read going through the service (no DB-native backstop). It re-implements RLS
  in Go. It is the **rubric's explicit fallback** ("choose the read-proxy fallback only if reads are
  non-Postgres" — they are today), and it ships with **zero read-model migration**.

**Common to both:** the auth source of truth (`capabilityRead` lens + `cap-read.<actor>` +
`readableAnchors`) is identical. Switching A↔B does **not** rebuild the lens — only the enforcement seam.
This is why the fork is safe to leave for Andrew without blocking the lens work.

### 3.4 The read-actor authentication seam (sub-fork increment)

D1 needs a verified actor. The minimal seam (brainstorm #118, `lattice-architecture.md` Gateway
decision):

- The reader presents a **signed JWT carrying the Identity vertex id** (signed by an IdP/KMS — *external
  to Lattice*, per the brainstorm "does NOT own actor signing keys"). The read boundary **verifies the
  signature** and extracts `actor_id`. This is the *read* analog of write-path `Lattice-Actor` stamping —
  it **authenticates, it does not filter rows** (`lattice-architecture.md` D1: "same mechanism as
  write-path `Lattice-Actor` stamping — it authenticates, it does not filter").
- A **token-revocation KV** (`token-revocation`, already in `config.yaml` / the Gateway decision) gives
  an instant kill-switch (brainstorm #111 MVP answer); the v2 capability-vector-clock fence is **not**
  D1 scope (recorded, deferred).
- **Reuse, don't invent:** this lands in `internal/gateway/auth` (the planned home) + `internal/gateway/
  revocation`. The **full** internet-facing Gateway (NGINX/Envoy TLS/rate-limit/DDoS — "infrastructure
  config, not Go code" per the architecture) is **deferred to ops** and explicitly out of D1.

---

## 4. Contract surface

| Contract | Change vs. build-to | What |
|---|---|---|
| **#6 Capability KV** | **CHANGE (uncommitted edit staged)** — new **§6.14**: `cap-read.<actor>` shape (read-path mirror of §6.2), `readableAnchors[]` field spec, the `authzAnchor` column convention, the read-boundary dispatch (Path A RLS / Path B filter), and the inherited security invariants (`projectionSeq`, soft-tombstone, fail-closed, "no entry = no read"). | The read-path mirror belongs in the Capability-KV contract — it is the same bucket, the same multi-lens pattern, the same security plane. |
| **#10 §10.2** | **build-to (no edit)** — already says a protected lens "carries the D1 authz anchor there." D1 fulfils that promise; §6.14 generalizes "there [Postgres]" to "any protected target." | Cross-reference only. |
| **#1 addressing** | **build-to** — `cap-read.<actor-suffix>` follows the existing Capability-KV key conventions (§6.1); `readableAnchors` anchors are `<type>.<id>`. | No new key shapes beyond the documented convention. |
| **Gateway / authN** | **build-to** — `lattice-architecture.md` "Gateway Architecture Decision" already specifies the translator + token-revocation KV + JWT. D1 builds the *read-auth* slice of it. | No architecture edit; D1 is a first realization. |

The §6.14 edit is the **only** frozen-contract change. It is made in `main` and **left uncommitted** for
Andrew (the diff is the proposal). The design builds against it as if ratified.

---

## 5. Migration, compatibility, test strategy

**Migration / compatibility.**
- **Additive and dark-launchable.** The `capabilityRead` lens, `cap-read.*` keys, and `authzAnchor`
  columns are all new. Until a read boundary *enforces*, nothing changes — the lens can project and be
  conformance-tested with all reads still trusted (the current Loupe/app posture is unaffected).
- **Enforcement is opt-in per read model**, gated by a `protected: true` target flag. A read model with
  no flag and no `authzAnchor` stays public — today's behavior. This lets D1 land one protected read
  model at a time (e.g. lease applications first) without a big-bang cutover.
- **Loupe stays the inspector exception** (memory: P5 inspector carve-out). Loupe authenticates as a
  privileged read-actor whose `readableAnchors` is the all-access anchor (or bypasses with an explicit
  privileged claim), so D1 does not break the admin console. This is the read analog of the
  internal-service-actor root-equivalent model (`lattice-architecture.md` "Internal service actor model").

**Test strategy.**
- **Unit:** the `capabilityRead` cypher conformance test (a seeded graph → assert `readableAnchors`
  shape), mirroring the §6.6 conformance test that moves with each capability lens. `projectionSeq`
  guard + soft-tombstone tests reuse the §6.2 harness.
- **Ephemeral-stack e2e:** seed two identities with disjoint residence; project a protected read model;
  assert identity A's read returns only A's anchored rows and **none** of B's (Path A: via RLS;
  Path B: via the filter). A revoke (delete the residence link) must **stop** A's reads after CDC
  converges — and a *stale lower-seq replay must not resurrect* the grant (the §6.2 resurrection test,
  read-side).
- **Gate-3 adversarial read-bypass vectors** (the read-path twin of §6.10 item 6, which D1 should add to
  the capability bypass suite): **(1)** direct read of a protected store without a valid JWT → denied;
  **(2)** read with a JWT for actor A requesting actor B's anchor → filtered out; **(3)** a revoked
  token (token-revocation KV) → denied even with a structurally-valid JWT; **(4)** cross-anchor bleed
  (actor with `unit.X` access reading rows anchored `unit.Y`) → none returned; **(5)** a protected lens
  shipped *without* an `authzAnchor` column → activation/lint fails closed (never silently public).
  These join `make test-capability-adversarial` (Gate 3).

---

## 6. Risks & alternatives

**Risks.**
- **R1 — read-model migration cost (Path A).** Moving protected read models to Postgres is real work and
  partially walks back the freshly-established NATS-KV read pattern. *Mitigation:* the fork lets Andrew
  pick Path B (no migration) with identical lens work; migrate later without rebuilding the auth source.
- **R2 — projection staleness on the read security plane.** A revoked grant is enforced only after CDC
  converges (bounded by Refractor lag — same window as write-path Capability KV). *Mitigation:* the
  token-revocation KV is the instant kill-switch for the actor as a whole; per-resource revocation rides
  CDC lag (acceptable, matches the write path; the vector-clock fence is the deferred v2).
- **R3 — projection correctness = auth correctness.** A bug in the `capabilityRead` cypher = a read
  leak. *Mitigation:* the Gate-3 read-bypass suite (§5) + fail-closed activation + the conformance test,
  exactly as the write-path Capability Lens is guarded.
- **R4 — partial coverage looks like full coverage.** A protected lens that forgets `authzAnchor` would
  silently serve everything. *Mitigation:* the `protected: true` → must-have-`authzAnchor` lint (a
  conventions-linter gate, like the P5 gate) fails closed.

**Alternatives considered (and why not).**
- **Per-row encryption / Secure Lens instead of RLS** — that is the **Vault/crypto-shred** initiative
  (a *different* ★★★ row); it protects *confidentiality at rest*, not *read authorization*. Orthogonal;
  D1 does not need it and should not absorb it.
- **Auth in each app** (every app filters its own reads) — rejected: re-implements ReBAC N times,
  no single source of truth, trivially bypassable. The whole point of "everything is a Lens" is to
  resolve once.
- **Personal Lens fan-out as the first delivery** (Path C) — rejected as *first*: heavier (per-identity
  streams), and it *consumes* the Capability-Read Lens D1 builds. D1 is its prerequisite, not its
  competitor.

---

## 7. Decomposition for the Steward (fire-by-fire, each independently shippable + green)

Ordered so the auth source of truth lands first (common to all paths), enforcement second, hardening
third. Each increment is green on its own; enforcement is dark until its read model opts in.

1. **D1.1 — `capabilityRead` lens + `cap-read.<actor>` (the auth source of truth).** Author the
   `actorAggregate` read-grant lens (bootstrap or `rbac-domain`/residence package, mirroring the
   write-path split); project `readableAnchors[]` to the Capability KV bucket. Conformance test +
   `projectionSeq`/soft-tombstone/fail-closed reuse. **No enforcement yet** — pure projection. *Builds to
   §6.14 (ratified).* Independently green: a new lens that projects and conformance-passes.

2. **D1.2 — read-actor authentication seam.** `internal/gateway/auth` (verify signed JWT → `actor_id`)
   + `internal/gateway/revocation` (token-revocation KV check). A read boundary that authenticates but
   does **not** yet filter (returns the same data, now actor-attributed). Reuses the
   `lattice-architecture.md` Gateway decision. Green: auth verified end-to-end, no behavior change for
   trusted callers.

3. **D1.3 — enforcement (the fork resolves here).**
   - **If Path A:** project `capabilityRead` to a Postgres `actor_read_grants` table; land the first
     protected business read model in Postgres with `authz_anchor` + an RLS policy; the read boundary
     sets `lattice.actor_id` per session. (Pulls in the **Postgres read-model** + **`Truncater`** backlog
     items.)
   - **If Path B:** the read-authorization service filters NATS-KV read-model rows by `authzAnchor`
     against `cap-read.<actor>`; apps read through it.
   - Either way: **one** protected read model (lease applications is the natural first), e2e-proven
     (identity A sees only A's rows). Green on one read model; others opt in later.

4. **D1.4 — Gate-3 read-bypass adversarial suite** (§5 vectors 1–5) into
   `make test-capability-adversarial`, all DEFENDED. The `protected: true` → must-have-`authzAnchor`
   conventions-lint gate. Green: the bypass suite passes; the lint fails closed on a mis-declared lens.

5. **D1.5 — roll the remaining protected read models** (clinic, loftspace listings/identities as each is
   classified protected vs public) onto the chosen enforcement seam. Loupe configured as the privileged
   all-access read-actor. Pure extension of D1.3's pattern; one read model per fire.

**Deferred beyond D1 (recorded, not in scope):** the full internet-facing Gateway (NGINX/Envoy/IdP);
the capability **vector-clock fence** (v2 consistency, brainstorm #111); **Personal/Secure Lens**
fan-out (Path C, the Edge step that builds on D1); ES-target read auth (no ES consumer ships).

---

## 8. Adversarial review note

This is a security-plane, cross-cutting L/XL design — it warrants a `bmad-party-mode` / adversarial pass
before the Steward builds. The highest-leverage things to attack in that review: **(a)** the
fail-closed-vs-silent-public boundary in §3.2 (the #1 leak shape — a protected lens with no anchor);
**(b)** the staleness window on revocation (R2) vs. the write path's matching window — is the
token-revocation kill-switch sufficient interim; **(c)** whether the `readableAnchors` *anchor model*
(coarse resource-scope) is expressive enough for operation-level read distinctions, or whether reads
need a richer grant than "can see this anchor's rows" (it likely does not — reads are row-visibility,
not op-authorization — but worth a skeptic). Fold findings in before D1.1.
