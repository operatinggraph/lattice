# Architecture review — Refractor (2026-07-06)

**Scope:** Refractor only (`internal/refractor` + `cmd/refractor`) — the deferred post-update re-review
the [2026-07-02 full-platform review](arch-review-2026-07-02.md) explicitly held back ("Refractor findings
are deliberately absent: that component is mid-update and Andrew re-reviews it after"). Read-only audit;
three independent sub-agents (purpose/charter/docs · contract conformance · invariants/tests) each with
file:line evidence, synthesized here. Nothing was executed against the running stack.

**Method note:** every claim below is verified against the pinned source at review time. The two board
rows this review retires (`lattice.md` fan-out-untested, protected-Postgres-LWW) were each re-verified in
code before retirement rather than taken from a sub-agent's say-so.

---

## Verdict: **drifted**

The mechanics got *stronger* since 2026-07-02 — the protected-Postgres upsert is now seq-guarded, FR30
control-plane capability auth is enforced end-to-end, PL.3's D1 read gate shipped fail-closed and
test-pinned, and the liveness alerting matches its ratified design. But the same three days of velocity
introduced two ★★★ findings, and the one ★★★ gate the prior review hung "at-risk" on is still open. Both
new ★★★ findings are governance/seam failures (a ratified-decision contradiction; an unprovisioned
transport grant), not core-logic bugs — the auth plane itself audits clean and fails closed on every path
checked.

### What is healthy (verified, not assumed)

- **P2 is clean.** No Core-KV write exists anywhere in the tree, and the deploy conf proves it
  independently: Refractor's NATS user carries an explicit `deny $KV.core-kv.>` + `KV_core-kv` stream-admin
  deny (`deploy/nats-server.conf:32`, generator `deploy/gen-dev-nkeys/main.go:118`), pinned by
  `internal/natsperm/conf_test.go:150`. Reads are exactly the chartered set (`core-kv`,
  `refractor-adjacency`, `capability-kv` for the D1 gate, `personal-lens-interest`, own lens targets,
  Postgres system catalogs); no foreign read-model / weaver-* / loom-* / core-operations reads.
- **Every auth surface fails closed, including error paths.** `capabilityread.IsReadable` denies on empty
  anchor, no contributing slice, all-tombstoned slices, **and** every error path (list-keys failure, KV Get
  failure, malformed slice JSON return `(false, error)`, propagated to fail the actor's projection). The D1
  gate runs strictly before the Interest Set and denial short-circuits (`projection/personal.go:113`); the
  `capKV=nil` disable has no non-test caller (`cmd/refractor/main.go:598` always passes the real handle and
  exits if the open fails). RLS `VerifyProtectedTable` checks ENABLE **and** FORCE, rejects `USING(true)`,
  verifies `authz_anchors text[]`/`projection_seq bigint`/all columns, executes **zero DDL at activation**,
  and pauses recoverably (auto-resume). Unset `lattice.actor_id` → NULL → deny. `__actor` resolution errors
  instead of panicking.
- **Health reporting is honest.** No static greens; per-rule status written only on real transitions;
  TTL'd heartbeat so a dead instance's key expires; the CapabilityLens/business-lens alert debounce +
  clear-band hysteresis are real state machines, test-pinned; `lastProjectedAt` advances only on a real
  target write.
- **Test posture is strong.** H3 deny-all and H4 no-resurrect proven against real Postgres under a
  non-superuser role; guarded-rebuild-forces-truncate + stale-lower-seq-loses; OPTIONAL MATCH null-restore;
  anchor-tombstone composite-key delete + unresolvable-column fallthrough; filter-retraction +
  neighbor-keyed structural rejection + `ValidateUnanchoredForDiffRetraction`; link/aspect fan-out (create
  **and** tombstone/revocation); `__actor` unsafe-value error; interest-set defaults; D1-gate-wins-over-
  interest. CI runs both the Postgres-gated (`POSTGRES_TEST_DSN`) and embedded-NATS suites.
- **Contracts #1, #3, #6 (core), #8 build-to.** Key parsing funnels through `substrate.ClassifyKey` with no
  legacy short-form tolerance; the re-derive-by-re-scan CDC posture is safe under Contract #3's atomic-batch
  set semantics; §6.2 projectionSeq guard, §6.6/§6.7/§6.8 auth semantics, §6.13 output descriptor, §6.14
  read-path (grant table, RLS verify-and-pause, monotonic seq-guard, H3/H4) all match the frozen text.

---

## Findings

### F1 (★★★) — the Chronicler was built inside Refractor, against the ratified Fork-C banner

Two sub-agents found this independently. [orchestration-history-read-model-design.md](../../_bmad-output/implementation-artifacts/orchestration-history-read-model-design.md)
was ratified 2026-07-02 as **Fork C: a new component** — the rework banner explicitly supersedes "every
body mention of 'extend Refractor / LensSpec / the pipeline'," and its decisive rationale is Refractor's
founding charter (`brainstorming-session-2026-04-08.md:573`: lenses source the Core-KV change feed, **not**
`lattice-events`): "Fork A would cross that line inside the auth-plane-critical binary." F1 is specified as
a separate small binary in the bridge/objmgr weight class.

What shipped 2026-07-05 is the superseded Fork-A body, verbatim: the `eventStream` source went onto
**`LensSpec`** (`internal/refractor/lens/eventsource.go`), projection into `internal/refractor/eventlens/`,
a durable consumer on `events.loom.>` wired in `cmd/refractor/main.go:417` — and **no `cmd/chronicler`
exists**. The Steward's own pre-build regrounding cited the rework banner yet never re-decided the host;
the AS-BUILT notes and the board Done-log rows (`lattice.md:198-200`, tagged `[Refractor]`) record no host
re-adjudication. This is the inverse of the failure mode the banner rule exists to prevent: they built the
body, not the banner.

Mitigants, stated honestly: it went through 3-layer review, ships dark off the auth path, and eventStream
lenses are structurally barred from `protected`/`grantTable`/`secure` targets (`lens/corekv_source.go:683`).
The extraction is mechanical — `eventlens` is library-shaped and the banner says the projection model
carries over; the one real unwind is removing `Source` from Refractor's `LensSpec`.

**Recommendation:** extract to the F1 small binary per the banner (the security rationale is not weakened
by anything that shipped, and Fire 2 being live production makes extraction *cheaper* now than after later
fires), **or** record an explicit host re-ratification in the design banner + charter. The silent deviation
is the defect either way — this is a ratified-decision call for Andrew, not a lead adjudication.

### F2 (★★★) — Refractor's NATS allow-list is missing its two newest publish subjects; the crypto-shred finalization is live and would Nak forever

The deployed ACL grants `["$KV.>", "$JS.API.>", "$JS.ACK.>", "lattice.refractor.>"]`
(`deploy/gen-dev-nkeys/main.go:118`) — but two shipped features publish outside it:

- **`ops.system`** — the shred-finalization op submission at `internal/refractor/keyshredded/manager.go:340`.
  Unlike Loom/Weaver, Refractor has no `ops.>` grant. In the perm-enforced compose stack the submit fails,
  and the redelivery bound covers only `ErrRuleNotRegistered` — a real shred's finalization
  **Nak-redelivers unbounded**. `privacy-base` ships `shredIdentityKey` today, so this is a live path.
- **`lattice.sync.>`** — Personal Lens delta publishes (`internal/refractor/adapter/natssubject.go:255`).
  Latent (no production `nats_subject` lens installed yet), but any future one is dead on arrival.

On the adjacent question — is Refractor submitting operations itself a P2 violation? — the adjudication is
**sanctioned**. P2's write path *is* op submission, and the ratified
[vault-crypto-shredding-design.md](../../_bmad-output/implementation-artifacts/vault-crypto-shredding-design.md)
§2.4 (Fire 4b) specifies the finalization recorder in Refractor using the weaver/objmgr idiom: only
Refractor knows when its own projection-nullification work completed, and it records
`RecordShredFinalization{projectionsNullified}` under the kernel-seeded `identity.system.privacy` service
actor so the `shredStatus` convergence lens can project it (`shredStatus` is what Loupe's crypto-shred
proof view reads). Health KV is TTL'd operational state, not durable compliance evidence, and not
lens-projectable — so the op is the correct transport. The defect is purely that the ACL matrix + its
`natsperm` proof vectors were never extended when those features shipped. The `make test-crypto-shred` e2e
proves the *capability-auth* pipeline but runs on embedded NATS without the transport ACL, which is why it
never caught this.

**Note:** the Fire-4b checkpoint itself flagged (and board-filed) that `RecordShredFinalization` — like
MarkExpired/CreateTask/DetachObject — currently authorizes only because the dev stack runs
`LATTICE_AUTH_MODE=stub`; the operator-grant idiom for system-actor package ops is aspirational until that
gap gets a design. That is tracked separately (`real-actor-write-auth-e2e`).

### F3 (★★★, carried from 2026-07-02) — the §6.14 protected-by-default gate still does not exist

Re-verified unchanged. Contract #6 §6.14 mandates that a Postgres business lens declaring neither
`protected` nor `public` **fails closed at activation/lint**. Today it passes `translateSpec` with only
dsn/table/key checks (`lens/corekv_source.go:536`) and activates as a plain unguarded LWW table;
`scripts/lint-conventions.go` has zero protected/public logic. The adjacent gates (both-flags rejection,
protected-on-NATS-KV rejection, secureColumns⇒protected) all exist — this one gap is the contract's
mandated default posture, which is why the prior review hung "at-risk" on it.

### F4 (★★, carried) — failure-tier back half is library-only

`cmd/refractor` never calls `SetRetryQueue`/`SetAuditWriter`; transients Nak-redeliver forever, terminals
drop with a log line (no DLQ routing, no `errorCount` bump), and the audit subjects both docs advertise are
never emitted. The libraries are built and well-tested — it's binary wiring, or a ratified Nak-only posture
plus doc rewrite. (The `refractor-failure-tiers.md` truth-up in this pass corrects the *doc* claims that a
privacy-critical tier and Capability-Lens alert are "not built"; the *binary-wiring* gap remains a build.)

### F5 (★★, carried) — reserved-bucket guard still absent, and slightly worse than reported

`pkgmgr/bucketguard.go` denies exactly one alias (`"capability"`); Refractor auto-creates whatever bucket
a lens names (`cmd/refractor/main.go:346`). A mis-authored lens targeting `health-kv` or
`refractor-adjacency` doesn't just write it — a rebuild **`Truncate` purges every key in it**
(`adapter/natskv.go:362`), and ACL-less runs (embedded NATS, dev without nkeys) have no backstop at all.

### F6 (★★) — §6.14 Postgres seams: one of three fixed, two open

The protected *upsert* is now seq-guarded (`read_path_adapters.go:124` → `postgres.go:117`) — retiring the
"protected adapter unguarded LWW" board row. Still open: (1) `PostgresAdapter.Delete` stays seq-blind even
when guarded (`postgres.go:395`) — a stale replayed insert after a hard DELETE resurrects the row; (2) the
M5 wildcard-anchor branch the shipped RLS policy enforces (`adapter/rls.go:164`, citing "§6.14 M5") appears
nowhere in the frozen contract and no edit is staged; (3) a paused grant/protected lens now surfaces via
the business-lens path at `warning` severity, though `actor_read_grants` is the read-auth source of truth —
arguably auth-plane severity. Plain-Postgres LWW is **contract-sanctioned** (§6.14 permits business-table
LWW), so it is not a defect.

### F7 (★★) — §6.13 Invalidation is frozen text describing deleted code

§6.13 specifies a `ProjectionPlan.Invalidation` compiled reverse-traversal "replacing the broad
ActorEnumerator BFS" and mandates uncovered MATCH constructs fail activation. The ratified
retire-simple-engine design deliberately deleted that scaffolding (its "no frozen-contract change" claim
checked *engine* mentions and missed §6.13's Invalidation text); `ProjectionPlan` has no Invalidation
member (`projection/plan.go:57`), every actor-aggregate lens installs the broad BFS, and activation
warn-and-proceeds. Direction is conservative (over-reprojection, no security hole), but the frozen text now
describes a mechanism the codebase removed. Needs a staged in-place contract edit for Andrew.

### F8 (★) — smaller code findings

- **capabilityread's fail-closed *error* arms are untested.** The deny arms are pinned; the `(false,
  error)` paths (Get failure, malformed JSON, list-keys failure) — the posture that keeps the gate
  rot-proof — have no test.
- **`int64(math.MaxUint64)` wrap trap.** keyshredded stamps `MaxUint64` as the nullify projectionSeq; at a
  grant-table target the `int64` cast (`rls.go:278`) wraps negative and would *lose* every guard
  comparison. Unreachable today (no grant lens is a shred target) but a latent interaction.
- **Contract #5 minors, Refractor as the outlier.** Heartbeat `version` is `"0.1.0"` vs the contract's
  `"1.0"` (Processor conforms); status `"shutdown"` vs the enum's `shuttingDown` (Processor conforms).
- **`pendingSpecs` ordering buffer untested** (spec aspect before parent vertex — the CDC-ordering arm of
  lens loading).

---

## Doc staleness (corrected in this pass unless noted)

- **`refractor-failure-tiers.md`** was the worst offender: its "Control-plane authorization (currently
  stubbed)" section is false (FR30 shipped), and its "Designed-but-not-built" section is false on **both**
  halves (the PrivacyCritical tier + KeyShredded listener shipped; the Capability-Lens alert shipped and is
  documented *in the very refractor.md section it cited as lacking one*). The two component docs directly
  contradicted each other. **Corrected in this pass**, along with `classify.go:32`'s stale cross-reference.
- **`refractor.md`** claimed "13 packages" (tree has 17 — `eventlens`, `keyshredded`, `capabilityread`,
  `personalinterest`, `projection` absent from the owns-table), a phantom control-plane `list` op, an
  inverted step-8 hot-swap description, a step-9 "pipeline drained, consumer removed" path that does not
  exist, a `health.refractor.<instance>.lens.<canonicalName>` key shape that is not a real key (per-lens
  latency is an inline heartbeat metric), a SHA embedded in a deferred-table cell, and history-narration
  violations. **Factual + package-table + health-key + SHA corrections made in this pass**; a residual
  Personal-Lens narration sweep is noted where deeper.
- **`docs/vendors.md`** had no row for `antlr4-go/antlr/v4 v4.13.1` (+ the vendored `jtejido/go-opencypher`
  grammar) — the runtime of the only rule engine, squarely load-bearing. **Row added in this pass**
  (version verified against `go env GOMODCACHE` + `go.sum`).
- **`rls.go:195-203`** described the frozen §6.14 grant-table schema as an "illustrative four-column shape"
  the code deviates from, with a "(Staged … flagged for Andrew)" marker — but that five-column `is_deleted`
  schema **landed** in the contract (`0b3ec29`). **Marker corrected in this pass.**
- **`personal-secure-lens-design.md` banner** still reads "the design stays on the shelf" behind "D1 **and**
  a concrete consumer," carries no AS-BUILT checkpoints — yet PL.1–PL.3 shipped and the board tracks them.
  D1 landed; the concrete-consumer half is recorded nowhere. Board-transparent, so banner-hygiene rather
  than a hidden deviation, but the doc should say what happened. **Left for the owning design doc** (not a
  component-doc edit).

---

## Ranked corrections

| # | Imp | Correction | Owner |
|---|-----|-----------|-------|
| 1 | ★★★ | Reconcile the Chronicler host: extract `eventlens` + `LensSpec.Source` to the F1 binary per the Fork-C banner, or record an explicit host re-ratification | lattice (Andrew decision) |
| 2 | ★★★ | Grant Refractor `ops.system` + `lattice.sync.>` in the NKey matrix + extend the `natsperm` proof vectors; record the shred-finalization op-submit as a sanctioned exception | lattice |
| 3 | ★★★ | Add the §6.14 protected-by-default gate (activation reject + lint) + migrate plain lenses to explicit `public:true` | lattice |
| 4 | ★★ | Close the §6.14 Postgres seams: seq-guard the protected Delete; stage the M5 wildcard contract edit; auth-plane severity for a paused grant/protected lens; fix the `int64(MaxUint64)` wrap | lattice |
| 5 | ★★ | Wire the failure-tier back half (retry queue / DLQ / audit) into `cmd/refractor`, or ratify Nak-only + rewrite the failure-tier Route column | lattice |
| 6 | ★★ | Reserved-bucket denylist in `pkgmgr` + a fail-closed mirror at Refractor activation | lattice |
| 7 | ★★ | Stage the §6.13 Invalidation in-place contract edit reconciling the frozen text to the as-ratified broad-BFS reality | lattice (Andrew decision) |
| 8 | ★★ | Pin capabilityread's fail-closed error arms | lattice |
| 9 | ★ | Health contract minors (version `"1.0"`, `shuttingDown`) + a `pendingSpecs` ordering test | lattice |

---

## Proposed board rows (lattice lane)

Ready-to-paste, lint-conformant (≤600 chars, no SHA+prose, no Fire-N-SHIPPED). The docs-refresh and
vendors-row corrections are **done in this pass** (Done log), not filed as open rows.

| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **chronicler-host-reconciliation** | Chronicler Fires 1–2 built the banner-superseded Fork-A shape (`eventStream` on `LensSpec` + `eventlens` inside `cmd/refractor`, durable on `events.loom.>`) after the design ratified Fork C: a separate small binary; no host re-adjudication recorded. Extract to the F1 binary per the banner, or record an explicit host re-ratification in the design + charter. | ★★★ | M | 🔭 flag-for-Andrew |
| **refractor-publish-acl-gap** | The deployed NKey allow-list misses `ops.system` (keyshredded finalization submit — Naks unbounded in the perm-enforced stack; live via `privacy-base`) and `lattice.sync.>` (Personal Lens deltas — latent). Add the grants + extend the `natsperm` proof vectors; record the shred-finalization op-submit as a sanctioned exception. Distinct from (and complements) `natsperm-matrix-hygiene`'s `$KV.>`-narrowing. | ★★★ | S | 📋 |
| **refractor-protected-by-default-gate** | §6.14 mandates a Postgres business lens declaring neither `protected` nor `public` fails closed (activation + lint) — neither exists: an undeclared lens activates as a plain unguarded LWW table. Add the declare-one requirement at `translateSpec` + the `lint-conventions` gate; migrate existing plain lenses to explicit `public:true`. | ★★★ | S | 📋 |
| **refractor-6-14-postgres-seam-truthup** | Close the remaining §6.14 seams: seq-guard the protected `Delete` (stale-replay resurrection window); stage the M5 wildcard-anchor contract edit the shipped RLS policy already enforces (reconcile the `rls.go`/`capabilityread.go` §6.14 citations with it); decide auth-plane vs warning severity for a paused grant/protected lens; fix the `int64(MaxUint64)` wrap in the shred→grant-table seq stamp. Supersedes the protected-Postgres-LWW row. | ★★ | S | 📋 |
| **refractor-failure-tier-backhalf** | `cmd/refractor` never wires `SetRetryQueue`/`SetAuditWriter`: no deferred retry, no DLQ routing, no audit emission. Wire the shipped libraries, or ratify the Nak-only posture and rewrite the failure-tier Route column. | ★★ | S | 📋 |
| **lens-target-reserved-bucket-guard** | `pkgmgr` denies only the `"capability"` alias; Refractor auto-creates any bucket a lens names and rebuild `Truncate` purges it — a mis-authored lens can wipe `health-kv`/`refractor-adjacency`; ACL-less dev runs have no backstop. Add a reserved-bucket denylist in `pkgmgr` + a fail-closed mirror at Refractor activation. | ★★ | S | 📋 |
| **section-6-13-invalidation-amendment** | §6.13's frozen text specifies an `Invalidation` plan member + fails-activation rule that retire-simple-engine deliberately deleted (code: broad-BFS enumerator, warn-and-proceed). Stage the in-place contract edit reconciling §6.13 to the as-ratified reality, uncommitted for Andrew. | ★★ | S | 🔭 flag-for-Andrew |
| **capabilityread-error-arm-tests** | Pin the D1 gate's fail-closed *error* posture: `(false, error)` on KV Get failure, malformed slice JSON, and list-keys failure is unpinned and free to rot. | ★★ | S | 📋 |
| **refractor-health-contract-minors** | Align the heartbeat `version` (`"0.1.0"`→`"1.0"`) and status (`"shutdown"`→`shuttingDown`) to Contract #5 (Processor already conforms; update the observability schema doc); add a `pendingSpecs` spec-before-parent ordering test. | ★ | S | 📋 |

**Board reconciliation performed in this pass:** retired the fan-out-untested row (verified covered by
`disposition_internal_test.go` + the two `*fanout*_e2e` files) and the protected-Postgres-LWW row
(upsert now guarded; the remaining Delete seq-guard folds into `refractor-6-14-postgres-seam-truthup`).
