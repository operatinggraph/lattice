# Design — First Postgres read-model + the read boundary (the D1.3 enabler)

**Status: 📐 awaiting-Andrew (ratification)** · Author: Winston (Designer fire, 2026-06-29).
**Backlog row:** `planning-artifacts/backlog/lattice.md` → *Read-model / projection maturity* → "First Postgres read-model + read boundary (D1.3 enabler)".
**Parent:** Read-path authorization (D1) — this resolves the one open design question D1.3 (RLS enforcement) waits on. D1.1 (base `cap-read` lens, `43a476a`) + D1.2 (JWT auth seam, `136f49c`) have shipped; this is the bridge to D1.3.

---

## For Andrew (one-look ratification)

**What it does (two lines).** Gives D1.3 the thing it has been waiting on: **one real protected business read model projected to Postgres under RLS** (lease applications — today read leak-prone), plus the **read boundary** that lets an app read it as an authenticated actor. It resolves the D1 §3.3 open fork ("a thin read API **or** the Gateway translator extended for reads") and turns the never-exercised Postgres adapter into a live, RLS-enforced read path.

**The leak it closes (concrete, today).** `cmd/loftspace-app/applications.go:122` lists the `weaver-targets` bucket via `KVGet` and filters **client-side** by an `applicant` query param — so **any caller can pass any `applicant` and read every application** (and `weaver-targets` is Contract #10 §10.2 "internal Weaver-only, never on the read-path" anyway). D1.3 replaces that with a dedicated lease-applications read model in Postgres where **RLS returns only the actor's rows** — no client-side filter to forge.

**The one fork I'm resolving (your call to confirm): the read boundary.**
- **Option A — app-as-authenticated-reader (RECOMMEND for D1.3).** The app verifies the end-user's JWT via the *shared* `internal/gateway/auth` (D1.2), opens a Postgres session, sets `SET LOCAL lattice.actor_id = <verified actor>`, and queries — **RLS does the authorization, DB-native.** No new service. Auth logic is *not* re-implemented per app (the rejected "auth in each app" alternative was about re-implementing *filtering*; here the app only *authenticates* + sets a session var — RLS is the single authorization source).
- **Option B — thin read-boundary service** (a standalone authenticated read API apps call). One trusted chokepoint sets `actor_id`; cleaner when there are many/untrusted readers — but more infra than D1.3 needs.
- **Option C — Gateway translator extended for reads** — the eventual hardened end-state (external readers, the full Gateway), deferred to ops per the ratified D1 fork.

**Recommendation: A now, C as the deferred hardening.** A is the minimal extension that ships a real RLS read path; its one trust assumption (the first-party app is trusted to set the right `actor_id`) is exactly what the deferred Gateway (C) later removes for untrusted readers. B is a midpoint we don't need yet. **Flag for you: confirm A** (the app holds the actor-session) is the acceptable D1.3 posture for first-party trusted apps.

**Contract surface — no *frozen* change needed (§6.14 already covers the RLS shape).** §6.14 (committed) already specifies protected-by-default, `authzAnchors[]` set + the set-membership RLS policy, `FORCE ROW LEVEL SECURITY`, and the seq-guarded `actor_read_grants`. This design **builds to it**. **One small note staged for you (NOT frozen):** where the **protected-table provisioning** lives — I recommend Refractor generates the `CREATE TABLE` + `FORCE RLS` + the policy **from the protected lens spec** at activation (brainstorm #38's "RLS policy generator"), so the table/policy can't drift from the projection and FORCE-RLS is structural (H3). That's a Refractor-mechanism + a `docs/components/refractor.md` note, not a `docs/contracts/*` edit.

**No architectural fork** beyond the read-boundary choice (which the ratified D1 already pre-resolved toward "minimal seam now, Gateway later"). Build-sequenced **after** D1.1b (the `cap-read.residence` loftspace grant slice) — the grants must exist before RLS can match them.

---

## 1. Problem & intent

D1.3 is "turn on Postgres-RLS enforcement," but it has had **nothing to attach to**: no protected business read model lives in Postgres (the adapter ships but is unexercised), and the *read boundary* — how an app reads RLS-protected data as an authenticated actor — was left an open fork in the D1 design (§3.3: "a thin read API, or the Gateway translator extended for reads"). This design lands both, on **one** read model, so D1.3 becomes a build, not a blocked increment.

**The grounded leak (the demo target).** `handleApplications` (`cmd/loftspace-app/applications.go:122`):

```go
bucket := bootstrap.WeaverTargetsBucket          // §10.2: "internal Weaver-only, never on the read-path"
keys, _ := conn.KVListKeys(ctx, bucket)          // lists EVERY application's row
applicant := r.URL.Query().Get("applicant")      // a CLIENT-SUPPLIED filter — forgeable
rows := computeApplications(keys, get, applicant) // filtered in Go, in the app
```

Any caller lists all applications and filters client-side; the `applicant` scope is advisory. This is the read-path twin of the write-path's "any actor could submit any op before step-3 auth" — and it is exactly what D1 exists to close. Lease applications is the right first model: it is genuinely per-applicant-scoped, it already has a Weaver read-model projection to mirror, and it exercises the residence grant slice (`cap-read.residence`) end-to-end.

**Intent.** Project lease applications to a **dedicated, protected Postgres read model** carrying `authzAnchors`; provision its table with `FORCE ROW LEVEL SECURITY` + the §6.14 set-membership policy; have loftspace-app read it as the **authenticated actor** (Option A); and prove, e2e, that **identity A's token sees only A's applications** and a forged `actor_id` is DB-denied.

---

## 2. Grounding — what already exists (do not rebuild)

| Piece | State | Reference |
|---|---|---|
| **Lens → Postgres target** | **Exists.** A lens declares `TargetType: "postgres"` + `TargetPostgresConfig{DSN, Table, Key, …}`; the pipeline selects the adapter on `spec.TargetType` (`switch` at :400). There is even a Postgres-target example lens. | `internal/refractor/lens/corekv_source.go:76,137,400`; `internal/refractor/lens/bootstrap.go:48-97` |
| **Postgres adapter** | **Exists, unexercised by a live business lens.** `Upsert` (INSERT … ON CONFLICT), `Delete`, `Truncate` (just shipped), `Probe`. **Assumes the table already exists** — no schema/DDL/RLS management. | `internal/refractor/adapter/postgres.go` |
| **Postgres in the stack** | **Exists.** `postgres:16-alpine`, `make up` starts it; DSN `postgres://lattice:…@localhost:5432/lattice`. | `docker-compose.yml:21`, `Makefile:25` |
| **The read-auth source of truth** | **Shipped (D1.1).** `cap-read.<actor>` base lens projects the self-anchor for every identity; auth-plane by bucket; §6.13 fail-closed + §6.2 seq-guard inherited. | `bootstrap.CapabilityReadLensDefinition` (`43a476a`) |
| **Authenticated actor seam** | **Shipped (D1.2), dark.** `internal/gateway/auth` (verify IdP JWT → `actor` full vertex key) + `internal/gateway/revocation` (per-request kill-switch) + `Authenticator` (fails closed). Asymmetric-only, `none`/HS\* refused, `exp` required. | `internal/gateway/{auth,revocation}` (`136f49c`) |
| **The read-path contract** | **Committed (§6.14).** protected-by-default; `authzAnchors[]` set; set-membership RLS policy; `FORCE RLS`; `actor_read_grants(actor_id,anchor_type,anchor_id,grant_source,projection_seq)` seq-guarded; source-scoped retraction. | `docs/contracts/06-capability-kv.md` §6.14 |
| **The residence grant slice** | **D1.1b (pending) — package work.** `cap-read.residence` (loftspace) projects `actor → residesIn/leases → {unit, lease}` anchors to `actor_read_grants`. This design **depends on** it. | D1 board row, D1.1b |

**Net:** the *adapter* and *target-declaration* exist; the *missing* pieces are (a) **protected-table provisioning** (CREATE TABLE + FORCE RLS + the policy — nothing creates a table today), (b) the **read boundary** (apps read NATS-KV via `KVGet`; no Postgres-read-as-actor path exists), and (c) the **first protected lens** wired to all of it. Honoring P2 (writes via ops — unchanged; this is read-path), P5 (apps read a *projection*, now a protected one), P1 (the grant table + read model are read-models, not Core KV).

---

## 3. The shape

### 3.1 The lease-applications protected Postgres read model

A dedicated `loftspace`-package read-model lens (replacing the `weaver-targets` expedient for this view), `TargetType: "postgres"`:

- **Projects** one row per lease application: the application's business columns (unit, applicant, status, submittedAt, …) **plus** an `authz_anchors text[]` column.
- **`authz_anchors`** carries the resource scope (§6.14, H5 set): `["unit.<unitKey>", "identity.<applicantKey>"]` — the application is readable by an actor granted **either** the unit (a landlord/manager via residence/ownership) **or** the applicant identity (the applicant reading their own). Multi-anchor = the H5 win: no separate lens per audience.
- **`protected: true`** in the lens spec (the §6.14 default is protected, but the lens declares it explicitly for the provisioning step to key on; a `public: true` lens would be the opposite).
- **Table:** `read_lease_applications(... , authz_anchors text[], projection_seq bigint)`; `Key` = the application id.

> **Why a dedicated lens, not `weaver-targets`.** §10.2 forbids `weaver-targets` on the read path; D1 is the moment to give this view a *real* read-model surface. The lens cypher mirrors the existing applications projection but emits `authz_anchors` and targets Postgres.

### 3.2 Protected-table provisioning — the RLS generator (Fire 1, platform)

Nothing creates a Postgres table today (the adapter assumes it exists). For a **protected** lens, Refractor provisions, at lens activation, **from the lens spec** (brainstorm #38):

```sql
CREATE TABLE IF NOT EXISTS read_lease_applications ( ... , authz_anchors text[] NOT NULL, projection_seq bigint NOT NULL );
ALTER TABLE read_lease_applications ENABLE ROW LEVEL SECURITY;
ALTER TABLE read_lease_applications FORCE ROW LEVEL SECURITY;            -- H3: missing policy ⇒ deny-all
CREATE POLICY rls_read_lease_applications ON read_lease_applications USING (
  EXISTS (SELECT 1 FROM unnest(authz_anchors) a
          WHERE a IN (SELECT anchor_type||'.'||anchor_id FROM actor_read_grants
                      WHERE actor_id = current_setting('lattice.actor_id', true))) );
```

Deriving the DDL+policy from the spec (the columns, the `authz_anchors` convention, `protected:true`) keeps schema and projection from drifting and makes `FORCE RLS` **structural**, not a checklist item. The `actor_read_grants` table + its seq-guard (§6.14, H4) is provisioned the same way (it is the target of the `cap-read.*` lenses). `current_setting(..., true)` returns NULL if `lattice.actor_id` was never set → the `IN` matches nothing → **deny-all** (a boundary that forgets to set the actor sees nothing — fail-closed).

### 3.3 The read boundary (the fork — Option A: app-as-authenticated-reader)

`loftspace-app` becomes an **authenticated reader** of the protected model:

```
handleApplications:
  actor, err := s.authenticator.Authenticate(r)        // D1.2: verify JWT → actor; revocation-checked; fail closed
  if err != nil { 401 }
  conn := pgPool.Acquire(ctx)                            // a pgx session
  conn.Exec(ctx, "SET LOCAL lattice.actor_id = $1", actor)   // the RLS principal, from the VERIFIED actor only
  rows := conn.Query(ctx, "SELECT ... FROM read_lease_applications")   // RLS returns only the actor's rows
  // NO client-supplied `applicant` filter — RLS is the scope
```

- The `actor` comes **only** from the verified JWT (`internal/gateway/auth`), never a query param or header the client controls. `SET LOCAL` is transaction-scoped (per-request), so sessions can't bleed actor identity across requests (use an explicit transaction or a per-request connection).
- The shared `internal/gateway/auth` means the *authentication* is resolved once (one library), and **RLS** resolves the *authorization* once (the DB). Neither is re-implemented per app — this is **not** the rejected "auth in each app."
- **Trust assumption (flagged):** the app process is trusted to set `lattice.actor_id` honestly. For a first-party app (like Loupe's loopback-bind posture) that is the accepted D1.3 trust boundary; an untrusted/external reader is what Option C (the Gateway translator, deferred) later interposes. The app's Postgres role holds only `SELECT` on protected read tables (no direct grant-table write), so a compromised app can mis-set `actor_id` to *another* actor but cannot forge grants — bounded blast radius, and the forged-actor read is still attributable to the app's DB role in logs.

### 3.4 Read path (P5) / write path (P2)

- **P5:** the app reads a **lens projection** (the protected Postgres read model), not Core KV — *more* P5-correct than today (it currently reads `weaver-targets`, a §10.2 violation). The protected projection replaces the expedient.
- **P2:** unchanged — writes are still ops; the read model is Refractor-projected from CDC. The `actor_read_grants` and read tables are Refractor-written lens targets (the sanctioned exception), never app-written.
- **No Core KV / Contract #1 shapes touched** (read-models are not vertices).

---

## 4. Contract surface

| Surface | Touch? | Why |
|---|---|---|
| **#6 §6.14** | **build-to (committed)** | Already specifies protected-by-default, `authzAnchors[]` set, the set-membership policy, FORCE-RLS, the seq-guarded grant table. This design realizes it on one model. |
| **#10 §10.2** | **build-to** | This *honors* §10.2 ("`weaver-targets` never on the read-path; a Postgres read model carries the authz anchor there") by giving the view a real read-model surface. No edit. |
| **`docs/components/refractor.md`** | **doc update (not frozen)** | New: protected-lens provisioning (the table-DDL + FORCE-RLS + policy generator) + the Postgres-target-as-protected path. A component-doc note, direct to main. |
| **`docs/contracts/*`** | **none** | No frozen-contract edit. (The §6.14 anchorId-representation clarification staged by the D1.1 build is a *separate* uncommitted edit, not this design's.) |

**No frozen-contract change is staged by this design.** The RLS-provisioning mechanism is Refractor implementation + a component-doc note.

---

## 5. Migration & test strategy

### 5.1 Migration
- The lease-applications **Postgres** read model lands **alongside** the existing `weaver-targets`/NATS-KV projection (dual, briefly); loftspace-app's `handleApplications` cuts over to the Postgres read in the same fire that adds the auth boundary. The old client-filtered path is deleted (not left as a bypass). Other loftspace views stay NATS-KV until classified (D1.5).
- `actor_read_grants` + the protected table are provisioned at lens activation (Fire 1/2); a fresh `make up-loftspace` seeds them. No hand-run SQL.
- **Reversibility:** code-only (revert the handler to the NATS-KV read); the Postgres tables are additive.

### 5.2 Tests
- **Refractor provisioning (Fire 1):** a `POSTGRES_TEST_DSN`-gated test asserts a protected lens activation creates the table with `FORCE ROW LEVEL SECURITY` + the policy; a table created without a policy **denies all** (the H3 fail-closed proof); the `actor_read_grants` seq-guard rejects a stale-seq upsert (H4 — a delete-then-stale-upsert does **not** resurrect the grant).
- **The read model + grants (Fire 2):** the lease-applications lens projects `authz_anchors` correctly (unit + applicant); `cap-read.residence` projects the landlord's unit anchors; a conformance test on the projected rows.
- **The boundary e2e (Fire 3) — the headline proof:** ephemeral `make up-loftspace` + Postgres; seed applicant A, applicant B, landlord L; **A's JWT** → `GET /api/applications` returns **only A's** application; **B's JWT** sees only B's; **L's JWT** (granted the unit via residence) sees applications for L's units; an **un-authenticated** request → 401; a request that tries to set `applicant=B` while authed as A → still only A (RLS, not the param); a **forged/expired JWT** → 401 (D1.2). This is the Gate-3 read-bypass vector set (D1.4) exercised on a live model.
- **Gates:** `go build`, `make vet`, `golangci-lint`, STRICT lint-conventions, `make verify-kernel`, the loftspace package verify, + the gated Postgres integration tests. The convergence e2e suites stay green (the write path is untouched).

---

## 6. Risks & alternatives

- **R1 — the app-trust assumption (Option A).** A compromised loftspace-app can set `actor_id` to another actor. *Mitigation:* the app's DB role has `SELECT`-only on protected tables (cannot forge grants); the read is attributable to the app role; and Option C (the Gateway translator) is the designed-later removal of this assumption for untrusted readers. Acceptable for first-party D1.3, flagged for Andrew (§ For-Andrew).
- **R2 — schema/RLS provisioning is new surface.** Generating DDL+policy from a lens spec is real platform work and the first time Refractor manages Postgres schema. *Mitigation:* `CREATE TABLE IF NOT EXISTS` + idempotent `CREATE POLICY IF NOT EXISTS`-equivalent; the generator is small and derives from the spec; a `POSTGRES_TEST_DSN` test pins it. Alternative — a hand-maintained migration file — was rejected: it drifts from the projection and makes FORCE-RLS a checklist item (the H3 failure mode).
- **R3 — `SET LOCAL` scoping.** A pooled connection that doesn't reset `lattice.actor_id` could leak across requests. *Mitigation:* `SET LOCAL` inside a per-request transaction (auto-reset on commit/rollback); a test asserts a second request on the same pooled conn doesn't inherit the first's actor.
- **R4 — depends on `cap-read.residence` (D1.1b).** Without the residence grant slice, a landlord's unit grant doesn't exist and the e2e can only prove the self-anchor (applicant-sees-own) case. *Mitigation:* sequence after D1.1b; the applicant-self case is provable independently as a Fire-2 milestone.

**Alternatives:** Option B (thin read-API service) — deferred, more infra than D1.3 needs; Option C (Gateway translator) — the ratified deferred end-state. Per-app direct-NATS-KV-filter (today) — the leak being closed.

---

## 7. Decomposition for the Steward (each independently shippable + green)

**Fire 1 — Refractor protected-table RLS provisioning (platform; full 3-layer, security-plane).** At protected-lens activation, generate `CREATE TABLE IF NOT EXISTS` + `ENABLE`/`FORCE ROW LEVEL SECURITY` + the §6.14 set-membership policy, and provision the seq-guarded `actor_read_grants`. The §6.14 seq-guard (H4) on the grant-table upsert/delete. `POSTGRES_TEST_DSN` tests incl. the FORCE-RLS deny-all + the stale-seq-no-resurrect proofs. Green: a protected lens activates and its table is RLS-locked; no consumer yet, but Fire 2/3 land in the same initiative.

**Fire 2 — the lease-applications protected Postgres lens + the residence grant projection to `actor_read_grants`.** The loftspace read-model lens (`TargetType: postgres`, `authz_anchors`, `protected`) + `cap-read.residence` projecting unit/lease anchors (depends on D1.1b). Conformance tests on both projections. Green: the protected model + grants populate; still no enforcement-at-read until Fire 3 wires the boundary.

**Fire 3 — the read boundary in loftspace-app (Option A) + the e2e.** `handleApplications` authenticates via `internal/gateway/auth`, opens a per-request Postgres transaction, `SET LOCAL lattice.actor_id`, queries the protected table; delete the `weaver-targets` client-filter path. The headline e2e (§5.2): A sees only A, the `applicant=B`-while-A attack fails, unauth → 401. Full 3-layer (the enforcement turn-on — D1's §7 said "D1.3's enforcement turn-on gets the full 3-layer"). Green: the leak is closed, live.

*(Fires 1–3 are D1.3. D1.4 — the Gate-3 read-bypass suite + the protected⇒Postgres lint — folds in next; D1.5 rolls the remaining loftspace/clinic protected models onto this pattern.)*

---

## 8. Open ratification items (my calls, for Andrew)

1. **Read-boundary fork → Option A (app-as-authenticated-reader) for D1.3; Gateway (C) deferred.** Confirm the first-party-app-sets-`actor_id` trust boundary is acceptable now. *(rec: yes — it's the minimal real RLS path; C hardens it later.)*
2. **RLS provisioning → Refractor generates the table+policy from the protected lens spec** (vs. hand-maintained migrations). *(rec: generator — keeps schema/projection in lockstep + FORCE-RLS structural.)* Refractor-mechanism + a refractor.md note; no frozen-contract edit.
3. **First model = lease applications.** *(rec: yes — genuinely per-applicant-scoped, exercises residence grants, mirrors an existing projection, closes a real live leak.)*
4. **Sequence after D1.1b** (the `cap-read.residence` grant slice) so RLS has grants to match. *(rec: yes; the applicant-self case is provable earlier as a Fire-2 milestone.)*

---

*Designer: Winston (lattice-designer) · 2026-06-29 · grounds: the D1 design (§3.3 read-boundary fork, §7 D1.3), Contract #6 §6.14 (committed), `internal/refractor/lens/corekv_source.go` (Postgres target), `internal/refractor/adapter/postgres.go`, `cmd/loftspace-app/applications.go:122` (the leak), `internal/gateway/{auth,revocation}` (D1.2), Contract #10 §10.2, brainstorm #38 (RLS policy generator). Runs after D1.1/D1.2 (shipped) + D1.1b; enables D1.3.*
