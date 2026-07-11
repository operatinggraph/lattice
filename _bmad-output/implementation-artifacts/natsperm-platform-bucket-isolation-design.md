# NATS permission matrix — platform-bucket write isolation (Refractor `$KV.>` narrowing) — design

**Status: 📐 awaiting-Andrew (ratification)** · Designer fire 2026-07-10
**Backlog row:** [lattice.md](../planning-artifacts/backlog/lattice.md) → Arch-review intake → *natsperm-matrix-hygiene* (the deferred Refractor half; the bridge phantom-bucket half shipped `0377938`)
**Origin:** [arch-review 2026-07-02](../../docs/reviews/arch-review-2026-07-02.md) finding + action #19; the residual **accepted for v1** by the ratified [NATS account write restriction design](nats-account-write-restriction-design.md) §3.4/§8.3
**Contracts:** none changed — the matrix (`deploy/gen-dev-nkeys`), `deploy/nats-server.conf`, `internal/pkgmgr/bucketguard.go`, and `internal/natsperm` are component/deploy surface, not frozen contracts. Contract #7 §7.1 (bootstrap as the sanctioned provisioner) is built-to verbatim.

---

## For Andrew (one-look ratification block)

**What it does (two lines).** Introduces a single **platform-bucket registry** in `internal/bootstrap`
(bucket → owner + `lensTarget`/`sharedWrite` flags) from which four surfaces that today drift
independently are all **derived**: bootstrap's `ProvisionBuckets`, pkgmgr's reserved-bucket lens guard,
and the per-component NATS **owner publish-allows AND non-owner denies** in the permission matrix —
narrowing Refractor's `$KV.>` so it can no longer write `loom-state` / `weaver-state` /
`token-revocation` / `credential-bindings` / `orchestration-history`, and closing the matrix-wide
`$JS.API.>` backing-stream side-channel the Chronicler fire documented as tracked debt.

**Why now — the drift is no longer hypothetical (two live instances, found while grounding this):**

1. **`credential-bindings` is missing from `bucketguard.go`'s reserved list** — a package lens
   declaring `Bucket: "credential-bindings"` would be accepted at install, and Refractor's rebuild
   Truncate would wipe the Gateway's credential→identity resolution set (exactly the failure the
   guard exists to prevent). The bucket was added to `internal/bootstrap/primordial.go` (60f5fca)
   but the parallel map in pkgmgr was not updated.
2. **The Gateway's matrix grant is missing `$KV.credential-bindings.>`** — its shipped materializer
   (`internal/gateway/credential_bindings_materializer.go:136`, started `cmd/gateway/main.go:317`)
   KVPuts into the bucket on the Gateway's own NKey connection, but the matrix allows only
   `$KV.token-revocation.>` — under the enforced dev matrix **every fold is transport-denied**, so
   credential resolution is silently degraded. This is a **live bug**, filed as its own 📋
   maintenance row (ships as a plain fix, not gated on this ratification — see §9 Fire 0).

Same bucket, two independent stale lists. The registry makes a third instance structurally
impossible: a bucket that isn't in the registry doesn't get provisioned, so it cannot exist without
its guard entry and its matrix denies.

**The fork (your call) — how Refractor's broad write grant gets narrowed:**

- **A — keep `$KV.>` allow, derive explicit denies from the registry (my recommendation).**
  Refractor's allow stays `$KV.>` (the dynamically-named package lens-target buckets are
  un-enumerable in a static conf — unchanged §3.4 reasoning), and every platform bucket it does not
  own becomes a two-clause deny (publish + backing-stream admin verbs), generated. Zero migration,
  closes the concrete exploit surface (a mis-authored or compromised Refractor writing engine
  state), and the deny set can never rot because it derives from the same registry that provisions
  the buckets. Residual: Refractor can still write a *hypothetical future* platform bucket that was
  never registered — but an unregistered bucket is now also unprovisioned, so the residual is empty
  by construction.
- **B — allowlist via a reserved bucket-name prefix (`lens-*`) — INFEASIBLE, retire it.** The
  ratified write-restriction design §3.4/§11 "noted" this as the future tightening path. It does
  not exist on the substrate: NATS subject wildcards are **whole-token only** (pinned server
  v2.14.0, `server/sublist.go` — `*`/`>` match only when they occupy an entire `.`-delimited
  token), and a KV bucket name is a **single token** that cannot contain dots (pinned client
  nats.go v1.52.0, `jetstream/kv.go:501` `^[a-zA-Z0-9_-]+$`). `$KV.lens-*.>` is a literal, not a
  prefix match. **Ratifying this design should also fold a one-line correction into
  nats-account-write-restriction-design.md §3.4/§8.3/§11** striking the noted path (see §8).
- **C — dynamic permission provisioning (regenerate/reload the conf on package install, or move
  Refractor behind the Gateway's auth-callout).** Rejected: package install would couple to server
  config reload (fragile — the dev stack's bind-mount doesn't even pick up a replaced conf on
  SIGHUP), and the callout path issues permissions at *connect* time, so a lens installed after
  Refractor connects couldn't write its new bucket until a reconnect. Heavy machinery to replace an
  allow that is correct as-is; the [per-identity subscribe-ACL design](per-identity-nats-subscribe-acl-design.md)
  keeps auth-callout scoped to *unrecognized external* connections, and this design keeps internal
  components on static NKey users — the two compose, neither needs the other.

**No frozen-contract change.** One ratified-*design-doc* correction (the §3.4 "noted path" strike,
§8) and one live-bug maintenance row are the only side effects outside this doc.

---

## 1. Problem & intent

**The residual.** The ratified NATS account write restriction (Path A: static conf, 16 per-component
NKey users, `internal/natsperm` offline conformance) made the transport enforce *most* of the write
topology: only the Processor may write `core-kv`, only Refractor may write `capability-kv`, vertical
apps hold no `ops.>`. But Refractor — "the sole lens projector" — was granted **`$KV.>` allow with a
single `core-kv` deny** (`deploy/gen-dev-nkeys/main.go:136-137`), because package lens-target buckets
are package-chosen names auto-created at activation (`cmd/refractor/main.go:372-383`) that a static
conf cannot enumerate. The design accepted the residual explicitly (§3.4: *"Refractor could
technically write `weaver-state` / `loom-state`; accepted for v1"*) and the 2026-07-02 arch review
filed the tightening: *"narrow **or explicitly-deny** Refractor's, extend the natsperm vectors"*.

**What the broad grant actually exposes.** Refractor holding `$KV.>` (minus core-kv) can write:

| Bucket | Owner | Consequence of a rogue/buggy write |
|---|---|---|
| `loom-state` | Loom | corrupt pattern cursors → replayed/skipped guarded steps |
| `weaver-state` | Weaver | forge/expire dispatch marks → duplicate or suppressed remediations |
| `token-revocation` | Gateway | **un-revoke an actor** (delete a kill-switch row) — auth plane |
| `credential-bindings` | Gateway | rebind a credential to a different identity — auth plane |
| `orchestration-history` | Chronicler | falsify the durable audit read model |

None of these is a lens target; the in-process `bucketguard.go` denylist blocks a *lens* from
naming them, but that is Go-code enforcement inside the same trust domain — the write-restriction
design's whole point (§8.4) was to stop relying on that. The transport should refuse.

**The side-channel half (documented debt).** Every component holds broad `$JS.API.>` (JetStream API
access for consumers/streams), so any of them can `STREAM PURGE/DELETE KV_<bucket>` on buckets whose
*publish* they are denied — unless the bucket's backing stream carries explicit stream-admin denies.
Today only `core-kv` / `capability-kv` / (for Chronicler itself) `orchestration-history` are
protected this way; the Chronicler fire's comment (`gen-dev-nkeys/main.go:190-196`) and
`TestGatewayRevocationBucketWriteIsolation` / `TestChroniclerBackingStreamSideChannel`
(`internal/natsperm/conf_test.go:400-421, 505-530`) both name the rest as
*"natsperm-matrix-hygiene-tracked debt"* — this row.

**Intent.** Make the platform-bucket write topology *derived, not hand-maintained*: one registry,
three consumers (provisioning, lens guard, matrix denies), so the transport matches the ownership
truth by construction and a new platform bucket cannot ship half-guarded again.

## 2. Grounding — what exists today

- **The matrix** (`deploy/gen-dev-nkeys/main.go`) is the single source of truth for per-component
  NATS permissions; it renders `deploy/nats-server.conf` + the dev NKey seeds. Denies are
  hand-rolled per component via `denyProtected(kvSubjects, streams...)`, which already implements
  the **two-clause protected pattern**: deny the KV publish subject *and* the write-shaped
  stream-admin verbs (`protectedStreamDenies`, `main.go:45-53`). This design generalizes exactly
  that shipped pattern — no new mechanism.
- **The platform-bucket inventory** already lives in `internal/bootstrap/primordial.go`: the named
  constants (`:21-53`) and the `ProvisionBuckets` list (`:101-131`, name + description + TTL flag).
  `gen-dev-nkeys` already imports `internal/bootstrap` (for `OpsWildcardSubject` etc.), so deriving
  the matrix from a bootstrap-hosted registry adds **no new dependency edge**.
- **The lens guard** (`internal/pkgmgr/bucketguard.go:33-41`) is a hand-copied subset of the same
  inventory (7 of the now-11 platform buckets; `credential-bindings` missing, and the three
  legitimate shared lens targets — `weaver-targets`, `capability-kv`, `orchestration-history` —
  correctly absent). Refractor mirrors it fail-closed at activation
  (`cmd/refractor/main.go:369-371`).
- **The conformance proof** (`internal/natsperm/conf_test.go`) starts an embedded JetStream server
  from the *real committed conf* and asserts the matrix offline (write-isolation vectors per
  protected bucket, side-channel purge denials, positive owner-write pins). New denies get proven
  the same way.
- **Ownership truth** (verified in code, not assumed): `core-kv` → processor;
  `capability-kv`/`weaver-targets`/`refractor-adjacency`/`personal-lens-interest` → refractor
  (the interest registry is written only by Refractor's own `personal.register/.deregister` control
  RPCs, `primordial.go:37-42`); `loom-state` → loom; `weaver-state` → weaver;
  `orchestration-history` → chronicler; `token-revocation`/`credential-bindings` → gateway;
  `health-kv` → **shared-write** (every component self-reports); `core-objects` is the `$O.` object
  plane, out of scope here (its grants shipped with object-plane-nats-permissions). `bootstrap` is
  the unrestricted provisioner (Contract #7 §7.1) and stays exempt.

## 3. The shape

### 3.1 The registry (`internal/bootstrap`)

A small exported table colocated with the constants it names — the one place a platform bucket is
born:

```go
// PlatformBucket describes one platform-provisioned KV bucket: who may write
// its rows, whether packages may target it with lenses, and how it is
// provisioned. The registry is the single source for ProvisionBuckets,
// pkgmgr's reserved-bucket lens guard, and the gen-dev-nkeys permission
// matrix denies — a bucket absent here does not exist.
type PlatformBucket struct {
    Name        string
    Description string
    PerKeyTTL   bool
    // Owner is the matrix component name whose connection writes the rows
    // ("" = SharedWrite). Bootstrap is always exempt (the provisioner).
    Owner       string
    // SharedWrite marks the buckets every component writes (health-kv).
    SharedWrite bool
    // LensTarget marks the shared projection buckets package lenses may
    // legitimately declare (weaver-targets, capability-kv,
    // orchestration-history). Non-LensTarget buckets are platform-private:
    // pkgmgr rejects a lens naming them.
    LensTarget  bool
}

func PlatformBuckets() []PlatformBucket { … } // the 11 rows
```

Four consumers, each losing its private copy:

1. **`ProvisionBuckets`** ranges over `PlatformBuckets()` (name/description/TTL move into the
   rows). ⇒ *an unregistered bucket is an unprovisioned bucket* — the registry cannot under-cover.
2. **`pkgmgr.validateLensBuckets`** derives its reserved set: `!LensTarget` ⇒ reject as a lens
   Bucket (plus the existing alias map, unchanged). Fixes the `credential-bindings` hole and
   retires the hand map. Refractor's activation-time mirror (`reservedActivationBuckets`) derives
   identically.
3. **`gen-dev-nkeys`** derives the **owner publish-allows**: `$KV.<b>.>` is emitted into
   `b.Owner`'s allow-list, and every `SharedWrite` bucket into every component's, from the registry
   — never hand-listed. This is the axis whose hand-maintenance caused the live Gateway bug
   (registering `credential-bindings` with denies alone would *not* have granted the Gateway its
   write); deriving the allow closes the bug's whole class, not just the instance. (For Refractor
   the owned-bucket allows are subsumed by `$KV.>` and emitting them anyway is harmless.)
4. **`gen-dev-nkeys`** derives per-component denies (§3.2).

**The component roster becomes importable.** The `matrix` itself moves from the un-importable
`deploy/gen-dev-nkeys` `package main` into **`internal/natsperm`** (exported), with `gen-dev-nkeys`
reduced to a thin renderer that imports it. Motive: the conformance suite's denied-sets need "every
matrix component except the owner and bootstrap," and today those lists are hand-copied and already
stale — `conf_test.go:198` omits `cafe-app`/`wellness-app`/`lattice-pkg` from the core-kv denied
set, `:216` omits even more from capability-kv's. With the matrix importable, both test axes
(buckets × components) derive from source, and a newly added component automatically joins every
denied-set vector. natsperm is the natural home — the matrix lives next to its proof.

### 3.2 Derived matrix denies

For each matrix component `c` (bootstrap exempt) and each registry bucket `b`:

- **publish deny `$KV.<b>.>`** iff `b.Owner != c.name && !b.SharedWrite` **and** `c`'s allow-list
  could admit it — i.e. materially for Refractor (`$KV.>`); for allowlist-scoped components the
  publish clause is belt-and-suspenders, kept for consistency with the shipped `denyProtected`
  style (deny wins over allow, and the conf is generated — verbosity is free).
- **stream-admin denies `protectedStreamDenies("KV_"+b.Name)`** for **every** non-bootstrap
  component, *including the owner* — the Chronicler precedent (`conf_test.go:523-527`): a row
  writer never needs to create/update/delete/purge its own backing stream; bootstrap primordially
  provisions all of them. This closes the `$JS.API.>` side-channel matrix-wide, including
  `health-kv` (any component could today purge everyone's heartbeats) and `weaver-targets`.

The hand-rolled `denyProtected(...)` calls and the three `*Stream` constants collapse into the
derivation. Net effect on **Refractor**: allow unchanged (`$KV.>` …), deny grows from 6 entries to
publish denies on {`core-kv`, `loom-state`, `weaver-state`, `token-revocation`,
`credential-bindings`, `orchestration-history`} + stream-admin denies on all 11 backing streams.
Refractor keeps writing what it legitimately writes today: every package lens-target bucket
(unenumerable, via `$KV.>`), `capability-kv`, `weaver-targets`, `refractor-adjacency`,
`personal-lens-interest`, `health-kv`, and bucket auto-create for *new* package targets stays
allowed (`$JS.API.STREAM.CREATE.*` is denied only for the 11 registered `KV_<platform>` streams).

Two adversarially-verified non-breakages worth pinning (they were the likeliest silent casualties):

- **Refractor's rebuild Truncate survives.** `NatsKVAdapter.Truncate`
  (`internal/refractor/adapter/natskv.go:377-390`) iterates per-key `KVPurge` — a *publish* to
  `$KV.<b>.<key>` with a rollup header, not `$JS.API.STREAM.PURGE` — so the stream-admin denies
  never touch lens rebuilds, even on Refractor-owned buckets. Likewise no runtime code path uses
  `STREAM.MSG.DELETE` (`KVDeleteRevision` is a conditioned publish), and the only `stream.Purge`
  caller targets the schedules stream (`internal/substrate/publish.go:155`), unregistered here.
- **Owner self-create is deliberately revoked.** Today Refractor could `CreateKeyValue` an absent
  `KV_weaver-targets`/`KV_capability-kv`; after this change only bootstrap can (re)create a
  registered platform bucket. That is intentional least-privilege (the Chronicler precedent) and
  leans on the standing bootstrap-provisions-before-components-connect invariant — the
  `cmd/refractor` activation path only creates on an `OpenKV` miss, which cannot occur for a
  primordially-provisioned bucket.

### 3.3 What deliberately does not change

- **Subscribe stays `allow: [">"]` for internal components** — reads are unrestricted in the v1
  posture; the per-identity subscribe-ACL design owns the external-subscribe story. Orthogonal.
- **Refractor's `$KV.>` allow** — the un-enumerable dynamic lens targets remain the reason it
  exists; this design narrows by *denies*, not by enumerating allows.
- **`bootstrap`** stays unrestricted (the sanctioned provisioner, Contract #7 §7.1).
- **The `$O.` object plane and `ops.>`/`events.>` stream grants** — out of scope, already designed.

## 4. Reconciliation with the existing mental model

- *Didn't we already decide Refractor's broad grant was fine?* Yes — **accepted for v1** with a
  tightening path "noted, not forced" (write-restriction §3.4/§8.3/§11). This is that tightening —
  except the noted path (`lens.*` prefix allowlist) turns out to be **inexpressible on the
  substrate** (whole-token wildcards + dot-free bucket names, §For-Andrew fork B), so the design
  delivers the same confinement via derived denies instead. The ratified doc gets a one-line
  correction at fold time (§8) so no future design grounds on the infeasible path.
- *Doesn't `bucketguard.go` already prevent this?* It prevents a **lens** from naming a platform
  bucket — in-process, same trust domain. The transport-level deny is the fail-closed door the
  write-restriction design built for every other component; Refractor was the one exception. Both
  layers remain (defense in depth), now derived from one registry.
- *Does this introduce new state?* No — a compile-time table that already exists in three
  hand-copied fragments (`primordial.go` list, `bucketguard.go` map, matrix denies) becomes one
  exported slice. No runtime state, no new buckets, no new component.
- *Parallel in-flight designs touching the same seam?* The
  [per-identity subscribe-ACL design](per-identity-nats-subscribe-acl-design.md) (📐, same file
  `gen-dev-nkeys/main.go`) adds an `auth_callout` block + responder for **external** connections;
  this design edits the **internal** users' deny lists. Textual merge in either order is trivial;
  no semantic interaction (callout-issued permissions are minted per-connection and never read the
  registry). The object-plane grants are already in the matrix and untouched.

## 5. Fail-closed analysis (the default-direction check)

- **New platform bucket, author forgets the guard/deny:** impossible by construction — the bucket
  is provisioned *from* the registry, so registration is the act of creation; registration emits
  the lens-guard entry and the matrix denies on the next regen. The registry row's zero-value is
  the safe one: `Owner` unset + `SharedWrite` false ⇒ **every** component is denied publish;
  `LensTarget` false ⇒ packages may not target it. Omission denies.
- **Conf regen forgotten after a registry or matrix edit:** `internal/natsperm` derives its
  expected vectors from `bootstrap.PlatformBuckets()` × the now-importable natsperm matrix at test
  time but runs against the *committed conf* — a registry/matrix/conf mismatch fails CI (the suite
  already runs in the `unit` job). One explicit drift vector re-renders the conf from the in-repo
  matrix and asserts it matches the committed file byte-for-byte (the cheapest possible
  regen-forgotten alarm).
- **Over-denying a legitimate writer** (the inverse risk): the ownership column is asserted by
  positive vectors — every `Owner` gets a `KVPut` success pin (the Gateway/`credential-bindings`
  pin is what would have caught today's live bug at ship time).

## 6. Migration & compatibility

Config-only; no data, no key shapes, no contract text. `go run ./deploy/gen-dev-nkeys` regenerates
the conf (seeds are reused — idempotent); the dev stack picks it up with `docker restart
lattice-nats` (the bind-mount does not reload on SIGHUP; never `--force-recreate`/`make down` — the
dev JetStream store is ephemeral). CI needs nothing: `internal/natsperm` boots its own embedded
server from the committed conf. Rollback = revert the commit and regenerate.

## 7. Test strategy

All in `internal/natsperm/conf_test.go` (the established embedded-real-conf pattern) +
`internal/pkgmgr` / `internal/bootstrap` unit tests:

1. **Registry-driven write isolation (replaces the per-bucket hand vectors):** for each
   `PlatformBuckets()` row with an `Owner`: owner `KVPut` succeeds; every other matrix component's
   `KVPut` is denied — with the denied list derived from the **importable matrix roster** minus
   owner/bootstrap (§3.1), fixing the already-stale hand lists (`conf_test.go:198/:216` omit
   several components today) and auto-covering future components. Covers the Refractor narrowing
   (refractor appears in the denied set for `loom-state`/`weaver-state`/`token-revocation`/
   `credential-bindings`/`orchestration-history` — exactly the rows
   `TestGatewayRevocationBucketWriteIsolation`'s comment excludes today).
2. **Side-channel closure, matrix-wide:** for each registry bucket, `STREAM.PURGE.KV_<b>` succeeds
   as bootstrap and is denied for every matrix component (generalizes
   `TestChroniclerBackingStreamSideChannel`).
3. **Positive Refractor pins stay green:** `TestLensTargetWriteIsolation` (weaver-targets),
   `TestCapabilityKVWriteIsolation`, plus new pins for `refractor-adjacency` /
   `personal-lens-interest` / a *dynamically-named* package-style bucket (e.g.
   `test-pkg-bucket`) proving `$KV.>` still admits unregistered lens targets **including
   auto-create** (`CreateKeyValue` under the refractor user).
4. **Shared-write pin:** every component can still write its own `health.<component>.<inst>` key.
5. **pkgmgr guard derivation:** a lens naming any `!LensTarget` registry bucket is rejected
   (table-driven over the registry — `credential-bindings` now included); `weaver-targets` /
   `capability-kv` / `orchestration-history` still accepted.
6. **Bootstrap provisioning parity:** `ProvisionBuckets` provisions exactly the registry (existing
   `verify.go` checks re-pointed at it).

Gates: `go build ./...`, `make vet`, `golangci-lint run ./...`, `go test ./...` (natsperm, pkgmgr,
bootstrap), `make verify-kernel` untouched.

## 8. Fold obligations at ratification

- **nats-account-write-restriction-design.md** §3.4 / §8.3 / §11: strike the *"later tightening can
  prefix all lens targets (`lens.*`) and narrow Refractor to that prefix"* notes (inexpressible —
  whole-token wildcards, dot-free bucket names) and point the residual-risk lines at this design.
  Per the banner-rewrite rule this is an in-place rewrite of the superseded sentences, done in the
  ratification commit, not a banner.
- **Board:** this row → ✅ Andrew-ratified; Fire 0 row (§9) is independent and may already be done.

## 9. Fire decomposition for the Steward

- **Fire 0 — live-bug fix (its own 📋 board row, NOT gated on this ratification):** add
  `$KV.credential-bindings.>` to the Gateway's matrix allow + `credential-bindings` to
  `bucketguard.go`'s reserved map, regenerate the conf, add the positive Gateway pin + denied-puts
  vector + the pkgmgr rejection case. Restores the shipped materializer under enforcement. XS.
- **Fire 1 — the registry + derivations (this design):** `bootstrap.PlatformBuckets()`;
  `ProvisionBuckets` + `verify.go` + `pkgmgr.validateLensBuckets` +
  `cmd/refractor` `reservedActivationBuckets` derive from it; the matrix hoists into
  `internal/natsperm` with `gen-dev-nkeys` as its thin renderer deriving owner-allows + denies;
  regenerate conf; the §7 table-driven vectors + conf-parity drift vector. Subsumes Fire 0's hand
  edits if Fire 0 hasn't shipped yet (either order is safe). S–M, one fire, independently green.

## 10. Risks & alternatives considered

- **Risk — a legitimate writer I haven't mapped.** Mitigated by grepping every `KVPut`/`OpenKV`
  call site against the ownership table during the build fire (done for this doc: the table in §2
  is code-verified) and by the positive vectors (§7.1/7.4) failing loudly if wrong.
- **Risk — conf size / server limits.** ~15 components × ~11 buckets × 6 deny subjects ≈ ≤1k deny
  entries in a generated file; NATS permission lists are plain sublist entries, no documented
  practical ceiling at this scale. Accepted.
- **Alternative — narrow only Refractor, skip the matrix-wide side-channel.** Rejected: the
  side-channel is the same tracked row (the Chronicler comment names it verbatim), the mechanism is
  identical, and splitting it leaves `health-kv`/`weaver-targets` purge-able by every component for
  no saved effort. Fewer, larger fires.
- **Alternative — hand-extend the deny lists without the registry.** Rejected: that is a *third*
  parallel copy of the inventory — the exact failure mode §For-Andrew documents twice. The
  registry is the structural fix; the deny lists are its cheapest consumer.
- **Alternatives B/C for the narrowing itself** — see the For-Andrew fork block (B infeasible on
  the substrate; C heavy dynamic machinery with a connect-time staleness hole).

## 11. Adversarial pass (pre-build gate — discharged 2026-07-10, this fire)

An adversarial review sub-agent attacked the draft; outcome, folded in above:

- **Confirmed safe (the likeliest silent breaks, all verified non-issues):** Refractor's rebuild
  Truncate is per-key rollup publishes, not `STREAM.PURGE`; `AtomicBatch`/consumer setup never use
  the denied stream verbs; no non-Refractor component `CreateKeyValue`s at runtime (Gateway/apps
  use open-only `OpenKV`); nothing administers `KV_health-kv`; no runtime `STREAM.MSG.DELETE`; the
  ownership table matches every `KVPut` call site; the Gateway `credential-bindings` live bug is
  real (same connection, no second creds path).
- **MAJOR (fixed):** the draft derived provision/guard/denies but left the **owner's positive
  allow** hand-maintained — the exact axis the live bug rotted on, contradicting the "cannot rot"
  claim. Fixed: owner/shared allows are now the registry's 4th derivation (§3.1.3).
- **MAJOR (fixed):** "derive the denied set from the matrix" was unachievable with the matrix
  stuck in `package main` — and the existing hand-copied denied-sets are already stale. Fixed: the
  matrix hoists into importable `internal/natsperm` (§3.1), tests derive both axes, plus a
  conf-parity drift vector (§5).
- **MINOR (folded):** owner self-create of an absent owned bucket is revoked by the owner-included
  stream-admin denies — intentional, stated with its bootstrap-before-connect dependency (§3.2).

No open findings remain.
