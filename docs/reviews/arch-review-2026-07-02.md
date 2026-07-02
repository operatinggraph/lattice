# Lattice full-platform architecture review — 2026-07-02

**Adjudicator:** Winston (bmad-architect). **Mode:** report-only (`/arch-review`, no `file` arg) — boards
untouched; Andrew decides what gets filed. **Method:** 14 read-only sub-agent auditors (12 components + a
contract sweep + a read/write-path map), each grounded in the pinned sources (architecture spine P1–P7,
`docs/contracts/*` FROZEN, `docs/vendors.md` + `go env GOMODCACHE`, the founding brainstorm charters), then
synthesized and the load-bearing findings re-verified by the lead against code and the pinned vendor.

Every claim below carries `file:line` evidence in the per-component detail. Findings that overlap an existing
board row are marked **[tracked]** and excluded from the proposed rows; findings that need an *update* to an
existing row are marked **[update]**.

---

## 1. Executive summary

**The platform is architecturally healthy.** The hard invariants hold in first-party runtime code: the
read/write-path sweep found **no P2 or P5 violation** anywhere in `internal/*` or `cmd/*` runtime code — the
Processor is the sole Core-KV writer, apps read only lens projections, and the sanctioned exceptions (Loom's
guard read, Health-KV self-reports, bootstrap provisioning, the off-graph blob plane, Loupe the inspector)
stay within their bounds. The four engines (Processor, Refractor, Loom, Weaver) are charter-true, closely
built-to-contract, and adversarially tested; Loom and Weaver are the best-conformed components audited (Weaver
holds *zero* Core-KV point reads — even its registry is CDC-index-based).

**The debt clusters in four bands, in priority order:**

1. **Genuine security / correctness gaps at the edges shipped this week** — a root-capability create-guard
   that nothing pins (Bootstrap), an object-plane NATS permission matrix that is mechanically wrong against
   the pinned vendor (blob writes should be transport-denied on the live stack), the ratified
   protected-by-default lens gate that simply does not exist (an undeclared Postgres lens activates
   unguarded), a Loupe admin API with no CSRF/DNS-rebind guard, and `privacy-base` shipped uninstallable
   (breaking `make up-full`).
2. **Enforcement / feature machinery declared-but-unwired** — Refractor's failure-tier back half (retry
   queue / DLQ / audit) is library-only, never wired into the binary the docs describe; the Gateway's
   token-revocation kill-switch exists only as code (no bucket, no revoke surface, silent downgrade); the
   §6.14 conventions-lint gate is unbuilt though RLS shipped.
3. **Pervasive documentation drift + governance holes** — four built components have no `docs/components`
   page (object-store-manager, bootstrap, vault, privacyworker); the components index still lists Gateway
   and Vault as "Designed"; three components emit false-green heartbeats; two components
   (object-store-manager, bootstrap) are in no Surveyor rotation and have no board presence; five
   `CONTRACT-AMENDMENT-REQUEST.md` files are resolved journals living in the code tree.
4. **Contract text lagging ratified evolution** — Contract #10's Weaver sections drift in five spots (worst:
   an `augur.pattern` field a package author would write and the engine silently ignores); Contract #2's
   §2.6 error-code table no longer matches the wire; §10.5's async-deadline paragraph contradicts its own
   §10.6; Contract #7 §7.2 describes a kernel that hasn't existed since Story 4.7.

**Contracts tree is clean** — zero dangling uncommitted contract edits at review time; every recently-ratified
dormant section (§2.5 read-posture, §2.6/§3.9.1 batch ceiling, §8.3/§8.6 per-key OCC) is already
board-tracked build-pending. No untracked contract-vs-code drift in that set.

**Verdict tally:** healthy 8 · drifted 3 · at-risk 1 (Refractor's protected-by-default gap) · design sound 1.
Nothing is failing; nothing needs a stop-the-line. The ★★★ items are pre-emptive hardening of surfaces that
are inert *today* only by convention or by not-yet-exercised paths.

---

## 2. Per-component verdicts

### Processor — **drifted** (machinery healthy; textual + dormant-validation drift)
Every hard invariant holds and is gate-tested: 9-step commit path + step 6.5 encryption, per-SUBJECT CAS
(`Nats-Expected-Last-Subject-Sequence`, `substrate/batch.go:126-131`), §3.2 hydrated-revision defaulting with
bounded internal retry, idempotency dedup, transactional outbox, task auto-completion — all real, ~290 test
funcs, zero `t.Skip`. Drift is textual: Contract #2 §2.6's error-code table diverged from the wire in **both**
directions (contract lists 7 codes none emitted; wire emits 6 codes none listed); two contract-asserted
validations (§3.5 dangling-reference, §3.4/§3.8 event-type DDL) are **unbuilt and untracked**; and
`processor.md`/`doc.go` omit step 6.5, the OCC retry, task auto-completion, and `kv.Links`, with `commit_path.go`
still carrying "stubbed 4-10 / auth (stub)" comments. Top corrections: reconcile the §2.6 wire enum
(contract edit for Andrew); decide build-vs-amend on the two dormant validators; doc/comment sweep.

### Substrate — **healthy** (doc-drifted)
Strongest vendor-pin discipline of the fleet — per-subject CAS, batch-protocol headers, `@every` floor, and
KV delete-marker semantics all verified against the pinned nats-server v2.14.0 / nats.go v1.52.0 source, with
a compile-time test pinning schedule-header constants to the server's own. **No `substrate.EnsureKV`
anywhere** (grep-clean); the seam is consumed by 70+ packages with no un-principled bypass. Debt is doc-only:
`substrate.md` shows a wrong `AtomicBatch` signature (timeout param vs ctx — contradicting its own Contract #3),
omits six files and the whole object-store/publish/schedule surface, and a `SubscribeKVChanges` godoc claims a
durable is deleted on shutdown while the code deliberately preserves it. Plus tree debris
(`CONTRACT-AMENDMENT-REQUEST.md`) and one already-tracked dormant section (§3.9.1). Nothing structural.

### Refractor — **at-risk on one gate** (sound core; three real gaps)
The CDC pipeline, §6.2 projection-seq guard, and the freshly-ratified §6.14 verify-and-pause are genuinely
built and fail closed on every *declared* path; the charter boundary (no event streams, no Core-KV writes) is
clean. Three gaps: (1) **★★★ the ratified protected-by-default gate does not exist** — a Postgres business
lens declaring neither `protected` nor `public` activates as a plain unguarded LWW table with no RLS verify,
and `lint-conventions.go` has zero protected/public logic; (2) the **failure-tier back half is library-only**
— `cmd/refractor` never calls `SetRetryQueue`/`SetAuditWriter`, so transients Nak-redeliver forever, terminals
drop with a log line (no DLQ, no `errorCount` bump), and the advertised audit subjects are never emitted; (3)
three §6.14 Postgres seams drifted — a protected/grant lens pauses **dark** (the heartbeat alert filters on
the capability-kv bucket), the protected adapter's `Delete` is seq-blind (a stale replay can resurrect a
deleted row — **[update]** board row `lattice.md:142` is now stale, its remaining substance is the delete
path), and the shipped RLS policy carries a wildcard-anchor branch §6.14's normative text lacks.

### Loom — **healthy** (best-conformed engine audited)
§10.3/§10.5/§10.6/§10.9 implemented branch-for-branch with the contract text mirrored in comments; the
Core-KV read exception is held (guard read is precondition-only, dormant in prod — all shipped patterns are
guardless) and scheduled to die via the ratified Processor-side guard. Three findings: the just-ratified
Chronicler F1 design **mis-grounds loom-state semantics** (terminal cursor records persist forever — the
design assumes the terminal batch deletes them; also the root of unbounded `loom-state` growth + a heartbeat
that KVGets every retained record each 10s); a **latent cold-registry trap** if an operator follows
`loom.md`'s stable-`Instance` advice and then crashes (the pattern-source durable reattaches empty); and live
externalTask ops carry a dangling `vtx.meta.<name>`-shaped `authContext.target` (inert under scope-any, breaks
when scope-specific auth lands). The read/write-path sweep also notes the "single guard exception" wording
undercounts Loom's two Contract-#4/§10.6 probe reads — **[invariant text drift, not a violation]**.

### Weaver — **healthy** (healthiest engine; documentary debt)
Charter-true, P2-exemplary (the only engine with zero Core-KV point reads), mechanism-dense but every
mechanism contract-anchored and adversarially tested (~20 adversarial Augur-dispatch arms alone). Nudge
retirement is verifiably complete in code and docs. Debt is Contract #10 lagging three weeks of ratified
evolution in five spots — worst: the §10.8 augur block still specifies a `pattern` field + triggerLoom
dispatch, but the code uses `op`/`adapter`/`replyOp` + a directOp (the lead-resolved escalation addendum), so a
package author following the frozen text writes a field `json.Unmarshal` silently drops. Also: `weaver.md`
contradicts itself in three places (claimId always-empty, past-`freshUntil` never-schedules), and the
`exhausted` escalation trigger + `augur.model` override are parsed-but-dead (contract advertises capability the
engine never fires).

### Bridge — **healthy** (sound engine, stale mandate page)
The platform's most disciplined boundary component: strictly type-agnostic (one hardwired vertical op name in
a test-only fake is the only leak), P2-clean, crash-safe by deterministic-id + adapter-dedup with no durable
state of its own, loud on every failure arm, FR58 genuinely proven. The async increment landed exactly on the
ratified collapse semantics. Debt is representational: `bridge.md` (the envelope spec's declared home)
predates the async SPI, the `dispatchOp` field, the poll/timeout schedule lane, and Augur; `scheduling.md`
doesn't mention the bridge at all and still calls `@every` deferred; and the bridge is one of three engines
whose heartbeat reports `"healthy"` while carrying error-severity issues (an unregistered-adapter outage rides
a green heartbeat).

### object-store-manager — **healthy** (code excellent; least-governed component)
Both owned contracts (#7 §7.2, #10 §10.8) build-to; P2/P5-clean with a fully-mapped touch surface; race-
hardened GC (epoch-CAS + lag-free `liveLinks` + owner-cascade) with layered proofs incl. a CI e2e. The debt is
entirely *around* the code: it is the only always-on platform binary with **no component page**, no README
row, no `architecture-overview` blob-plane mention, and no Surveyor-rotation slot; its heartbeat is the
platform's canonical **false-green** (static `"healthy"`, can never degrade — a dead cascade consumer stays
green forever); and a cluster of comments + the Makefile launch recipe narrate the pre-cascade design.

### Gateway — **healthy** (sound, fail-closed; revocation dormant; docs lag)
Fire 1+2 match the ratified design with no scope creep; every authentication/authorization decision point
defaults to deny; the JWT seam is properly hardened (alg allow-list, kid-strict, exp-required, fail-closed
construction) and genuinely shared with both vertical read boundaries; Contract #9 is honored end-to-end incl.
anti-enumeration. Real debts: the **token-revocation kill-switch is dormant** — the bucket is provisioned
nowhere, no admin surface can revoke an actor, and a failed bucket-open at startup silently disables checking
(stderr warn only); a stale/overstated docs cluster (index says "Designed"; `service-actors.md` says "there
is no Gateway"; `gateway.md` cites phantom Contract-#2 §2.34/§2.39; "only Gateway may publish core-operations"
overstates the shipped matrix); and Gate-3 vector #14's proving test lives in a package the gate target never
runs.

### Bootstrap — **healthy** (mechanically; mandate-layer debt; one ★★★ security gap)
One of the healthiest components audited in mechanics: disciplined per-bucket provisioning, a versioned
create-only atomic v14 seed, genuine two-phase crash-recovery, a tightly-scoped P2 seed exception. But: it has
no component page and no Surveyor slot; Contract #7 §7.2/§7.7 describe a superseded kernel; ~8 code comments
are stale (one **security-plane comment asserts the capability lens walks the graph** — the opposite of the
shipped literal-grant implementation); one true boundary hole (Refractor runtime-provisions lens buckets with
bare defaults — the no-`EnsureKV` question through a side door); and the sharpest security finding of the run
— **root capability now hinges on `identity.data.protected=true`, but no Gate-2/3 vector pins that a non-root
actor cannot *create* an identity with `protected:true`** (step-8 exempts create by design; only convention —
identity-domain hardcoding `data:{}` — protects it today). The Epic-12 decision record carries this obligation
verbatim; nothing tracks it.

### Loupe — **healthy** (one security hole)
A genuinely thin, read-heavy inspector whose every mutation is an op submission or an allow-listed control
request, with P2 enforced twice (code + broker deny). Contract conformance is build-to on #1/#5/#10.
**One untracked security hole:** the auth-less loopback admin API trusts the network bind but not the
operator's own browser — no Origin/Sec-Fetch-Site check on mutating routes, no Host allowlist, and
`POST /api/op` accepts `text/plain` — so any web page can blind-submit ops as the primordial admin
(no-preflight CORS-simple), and DNS rebinding defeats loopback for reads. Plus one doc overclaim (package
install/uninstall — actually list-only) and one small correctness gap (the task inbox ignores `isDeleted`).
The big evolution (Loupe 2.0 F1–F9) and known UI defects are already lane-tracked.

### The Chronicler (event-ledger materializer) — **design sound, ratified; 3 pre-build corrections**
Code-verified design-only. The Fork-C rework is architecturally right: it protects Refractor's founding
CDC-only boundary, is P2/P5-pure, needs no contract change for the component, and F1 is buildable now. Three
pre-build corrections: **F2 consumes `events.weaver.>` but Weaver emits no events at all** (no outbox; the
producer is a new weaver lifecycle-event family the banner promotes without restating the contingency); the
**archive segments would be garbage-collected** by object-store-manager's never-attached sweep (they carry no
object vertices — needs a dedicated bucket outside the sweep scope); and the F1 projection example maps
`data.instanceId`/`committedAt` while the published Event doc is `payload.*`/`timestamp` (would self-reject).
Also overlaps the Loom finding: the design's loom-state premise (terminal cursor deletion) is wrong.

### Packages tier — **conformant** (decision #10 working as designed)
15 of 16 packages are fully self-contained (DDL + lenses + permissions in-package; only `internal/pkgmgr`
imported outside tests); install/upgrade machinery in places exceeds its contract (provenance carry-forward);
both vertical apps confirmed thin P5 clients (zero core-kv references). Key-shape + sentence-test conformance
passes across sampled DDLs. **One ★★★ operational break:** `privacy-base` shipped with Vault Fire 2 as
installable-in-name-only — no `manifest.yaml`, no registry entry — yet `Makefile install-packages` invokes it,
so `make up-full` aborts at the privacy-base step *before* identity-domain/objects-base install; CI never runs
`install-packages` so stack-gates stay green. Plus: `_packages.md` inventories 12 of 16 (augur, both ledgers,
privacy-base absent); two ledger packages have zero CI install coverage. Dormant §8.3/§8.6 OCC is
**[tracked]**.

---

## 3. Cross-cutting findings

### 3.1 Seam audit
- **CDC feed (Core-KV → materializers):** single-owner, clean. Refractor (auth + read models), Weaver
  (registry only, `vtx.meta.>`), Loom (pattern source), object-store-manager (owner-cascade), bridge (reply
  probe) all consume Core KV as sanctioned readers; none writes it. The Chronicler is designed to consume the
  *event* stream (`events.*`), the boundary Refractor deliberately excludes — no overlap.
- **weaver-targets:** written by **Refractor** (lens projection), read by Weaver + the vertical apps + Loupe.
  Contract #10 §10.2 says it is "read only by Weaver … never on the read-path" — **drifted**: the vertical
  apps read it as their sanctioned P5 read model (D1.5 even hardened an RLS boundary over it). Two
  authoritative texts disagree; the code is correct, the contract sentence is stale.
- **Control plane (`lattice.ctrl.*`):** a real cross-component request/reply surface (Loom/Weaver/Refractor
  producers; Loupe the hardcoded client) with **no contract or component-doc of record** — Loupe's
  `control.go` allow-list *is* the only written contract. All three control planes carry allow-all capability
  stubs (FR30 ratified-designed, deprioritized).
- **Health KV:** the schema is documented (Contract #5 + `docs/observability/health-kv-schema.md`) and
  completeness-tested — but only for processor/refractor keys. **False-green cluster:** bridge, gateway, and
  object-store-manager emit literal `"healthy"` regardless of carried issues, while Loom and Weaver aggregate
  issue-severity into the status. Separately, **no component implements the §5.6 per-write heartbeat TTL** —
  all use plain `KVPut`; only the *capability* is provisioned. A dead instance's key persists (already a
  tracked board item at `lattice.md:49`).
- **Object plane:** the graph↔blob seam sits exactly where §7.2 puts it (Processor mints `vtx.object.*`;
  bytes flow trusted-client → `core-objects` → op). But the **transport permission matrix is mechanically
  wrong** (§3.3 below).

### 3.2 Contract sweep
- **Tree state:** fully clean — no staged/uncommitted contract edits (an uncommitted `docs/contracts/*` diff
  is, by convention, an awaiting-ratification proposal; there are none).
- **Dormant-but-tracked (no action):** §2.5 read-posture, §2.6 `BatchTooLarge` + §3.9.1, §2.6 `CellMoved`
  (Phase-3 by design), §8.3/§8.6 per-key OCC. All board-owned build-pending. Note: the `pkgmgr` uninstall
  comment now *argues against* the ratified §8.3 mechanism and must be rewritten when that fire lands.
- **Dormant + untracked (needs a decision):** §3.5 batch-internal consistency (dangling-reference) and
  §3.4/§3.8 event-type DDL validation — the frozen text + the spine's commit-path steps 6–7 assert
  validations the Processor doesn't perform; §6.12's worked example promises service-denial fields the denial
  builder never emits; §6.13's Invalidation-compiler paragraph describes a compiler that doesn't exist (the
  shipped broad-BFS is the ratified enumerator-less simplification).
- **Unrecorded load-bearing behaviors:** the `lattice.ctrl.*` plane, the Gateway HTTP binding (gateway.md
  cites phantom §2.34/§2.39), the bridge adapter SPI, the general lens NATS-KV read-model row shape, and
  package-owned read-model bucket naming — none owned by a contract section.
- **Contract-internal contradictions (Andrew's call, frozen):** §10.5 async-deadline paragraph vs its own
  §10.6 (re-imported a superseded Loom-deadline framing increment-3 struck); §10.8 anti-storm cross-ref vs
  §10.3's superseding consumer-idempotency text; §10.9 `instanceId=requestId` vs the shipped optional
  caller-supplied id.

### 3.3 Read/write-path map (P2 / P5)
- **P5:** clean. `lint-conventions.go` matches the stated rule; both vertical apps + gateway have zero
  core-kv references; all app reads target lens/read-model buckets or RLS-scoped Postgres. Gate is
  convention+lint-only (transport read plane is open: every nkey has `subscribe "&gt;"`), bypassable in
  principle via a helper-package import (`internal/aiagent` reads core-kv directly, today imported only by
  examples/tests), string-aliasing, or direct Postgres — noted, not exploited.
- **P2:** clean. Every KV write call site classifies to a sanctioned bucket; **zero unsanctioned writes**.
  `pkgmgr` writes nothing (submits ops); the NKey matrix denies `$KV.core-kv.>` publish to all but
  processor + bootstrap, and live `Permissions Violation` log lines prove enforcement is active.
- **★★★ Object-plane grants are mechanically wrong** (lead-verified against the pin): the pinned nats.go
  publishes object writes on `$O.<bucket>.C.>` / `$O.<bucket>.M.>` (`jetstream/object.go:481-482`) and the
  bucket is `core-objects` — but the matrix grants object-store-manager `$OBJ.objects-base.>` (wrong prefix
  **and** wrong bucket, `gen-dev-nkeys/main.go:154`), and the actual blob writers **Loupe and loftspace-app**
  (`ObjectPut` on `CoreObjectsBucket`) have **no `$O.` grant at all** (lines 176, 196). With a publish
  allow-list present, everything unlisted is denied — so blob upload and GC delete should be transport-denied
  on the live restricted stack (enforcement ON since 2026-07-01). Tests pass only because embedded-NATS
  fixtures don't enforce the matrix; `natsperm` has zero object-plane vectors. *(UNCERTAIN only in that a live
  upload wasn't reproduced this session; the subject mismatch vs the pin is confirmed.)*
- **Reserved-bucket lens-target gap:** `pkgmgr/bucketguard.go` rejects only the `capability` alias, and
  `cmd/refractor/main.go:220` auto-creates whatever bucket a lens DDL names — with Refractor's broad `$KV.>`
  write grant, a lens targeting `loom-state`/`weaver-state`/`health-kv` would project successfully into engine
  or health state.
- **Matrix hygiene:** bridge is granted `$KV.bridge-external.>` / `$KV.bridge-schedule.>` for buckets that
  don't exist (those are durable-consumer names); Refractor's `$KV.>` allow is broader than "its lens
  targets."

---

## 4. Ranked corrections

Severity ranks blast-radius × exploitability/likelihood, not effort. **[tracked]**/**[update]** as defined up
top. Owning lane in the last column.

| # | Imp | Correction | Where | Lane |
|---|---|---|---|---|
| 1 | ★★★ | Pin (Gate-2/3 vector + create-time guard) that a non-root actor cannot create an identity with `data.protected:true` — root capability hinges on it; only convention guards it today | `step8_commit.go:129-131`, identity-domain | lattice |
| 2 | ★★★ | Fix object-plane NATS grants ( `$O.core-objects.C/M.>`, not `$OBJ.objects-base.>`; add `$O.` to Loupe + loftspace-app) + add object vectors to `natsperm`; verify a live upload/GC-delete | `gen-dev-nkeys/main.go:154`, conf | lattice |
| 3 | ★★★ | Build the §6.14 protected-by-default gate — an undeclared Postgres business lens must fail closed (activation + lint), not activate as a plain unguarded table | `corekv_source.go`, `lint-conventions.go` | lattice |
| 4 | ★★★ | Make `privacy-base` installable (manifest + registry entry + `package_test.go` + verify gate) — it currently aborts `make up-full` before identity-domain/objects-base | `packages/privacy-base`, `cmd/lattice-pkg/main.go` | lattice |
| 5 | ★★ | Add a CSRF / DNS-rebind guard to the Loupe admin API (Host allowlist + same-origin check on mutating routes + require `application/json` on `/api/op`) | `cmd/loupe` handlers | loupe |
| 6 | ★★ | Activate the Gateway token-revocation kill-switch: provision the bucket, add a revoke surface, surface the disabled state in Health KV | `cmd/gateway/main.go:181-190`, bootstrap | lattice |
| 7 | ★★ | Wire Refractor's failure-tier back half (retry queue / DLQ / audit) into the binary — or ratify the Nak-only posture and rewrite both failure docs | `cmd/refractor/main.go` | lattice |
| 8 | ★★ | Aggregate issue-severity into the bridge / gateway / object-store-manager heartbeats (adopt the Loom/Weaver `aggregateStatus` rule) — three false-green emitters | `{bridge,gateway,objectmanager}` health | lattice |
| 9 | ★★ | Add a reserved-bucket denylist to `pkgmgr` lens validation + mirror it fail-closed in Refractor activation (a lens must not target core-kv/loom-state/weaver-state/health-kv) | `pkgmgr/bucketguard.go`, `cmd/refractor/main.go:220` | lattice |
| 10 | ★★ | Reconcile the three §6.14 Postgres seams: dark-pause alerting for postgres auth lenses, seq-guard the protected `Delete` path, stage the wildcard-anchor contract edit — **[update]** `lattice.md:142` | `refractor/adapter` | lattice |
| 11 | ★★ | Document the `lattice.ctrl.*` control-plane surface (subject grammar, op vocab, reply envelope, auth posture) — Contract #10 addition or a dedicated doc | `internal/*/control`, Loupe | lattice |
| 12 | ★★ | Reconcile Contract #10's Weaver text in five spots (augur block `pattern` vs op/adapter/replyOp; anti-storm cross-ref; two reserved weaver-state key shapes; weaver-targets read-path; revision-history) — one uncommitted edit for Andrew | Contract #10 | lattice |
| 13 | ★★ | Reconcile Contract #2 §2.6 error-code table with the wire enum (+ §4.1 tracker class, §2.9 unknown-field claim) + pin with a contract-reading conformance test | Contract #2, `envelope.go` | lattice |
| 14 | ★★ | Decide build-vs-amend on the two unbuilt+untracked validators (§3.5 dangling-reference, §3.4/§3.8 event-type DDL) | `step6_validate.go`, `step7_events.go` | lattice |
| 15 | ★★ | Chronicler pre-build (before F1/F2): re-ground the loom-state terminal-cursor premise, give archives a GC-fenced bucket, pin F1 to the published Event doc shape, fold the weaver-event producer into F2 | Chronicler design | lattice |
| 16 | ★★ | Write the four missing component pages (object-store-manager, bootstrap, vault, privacyworker) + index/README/overview rows + add objmgr & bootstrap to the Surveyor rotation | `docs/components` | lattice |
| 17 | ★★ | Refresh mandate docs to as-built: `bridge.md` (async SPI/dispatchOp/schedule lane/Augur), `scheduling.md` (bridge lane, @every), `substrate.md` (batch sig, surfaces) | `docs/components` | lattice |
| 18 | ★★ | Fix the loom pattern-source cold-registry trap (per-boot durable nonce independent of `Instance`) + the contradictory `source.go` comments | `internal/loom/source.go` | lattice |
| 19 | ★★ | Prune stale/over-broad NKey grants (bridge phantom buckets; narrow Refractor `$KV.>`) + extend `natsperm` to pin the tightened matrix | `gen-dev-nkeys`, conf | lattice |
| 20 | ★ | Doc-truth sweep: components index Gateway/Vault "Designed"→built; gateway.md phantom §2.34/§2.39; service-actors.md "no Gateway"/enforcement-pending/missing objmgr actor; CONCEPT.md serviceClass | `docs/components` | lattice |
| 21 | ★ | Fire the `exhausted` augur-escalation trigger (or strike it + `augur.model` from the block) — contract advertises capability the engine never delivers | `internal/weaver` | lattice |
| 22 | ★ | Carry the pattern's real meta-vertex key as the loom dispatch `authContext.target` (today a dangling `vtx.meta.<name>` shape) | `internal/loom` | lattice |
| 23 | ★ | Ensure Gate-3 vector #14 runs in its gate target (or add an in-package bypass test) + refresh stale Gate-2/3 vector-count comments | `internal/bypass`, `internal/gateway` | lattice |
| 24 | ★ | Remove the five resolved `CONTRACT-AMENDMENT-REQUEST.md` journals + the stale-narration comment clusters (bootstrap security-plane comment, objmgr pre-cascade comments, loom `doc.go`) + decide `internal/spike` disposition | repo-wide | lattice |
| 25 | ★ | Small doc/correctness: processor mandate-doc refresh; weaver.md self-contradictions; loupe.md overclaim + task `isDeleted`; objmgr Makefile `BOOTSTRAP_JSON_PATH` | various | mixed |
| 26 | ★ | Add verify-package gates for clinic-ledger + loftspace-ledger (only registered packages with zero CI install coverage) | Makefile, ci.yml | verticals |

---

## 5. Proposed board rows

Ready-to-paste, lint-conformant (`| Item | What | Imp | Size | State |`), grouped by owning lane, deduped
against the boards read this session. **Boards are untouched** — this is Andrew's filing queue. Where a
finding updates an existing row rather than adding one, it is called out first.

### Row updates (not new rows)
- **`lattice.md:142`** (`[Refractor] Protected/plain Postgres adapter is unguarded LWW`) — the upsert half
  shipped (`ef108b4`); rescope the row's substance to the seq-blind `Delete` path (stale-replay row
  resurrection). Folds into proposed row `refractor-6-14-postgres-seam-reconcile` below if filed as new.

### Lattice lane

| Item | What | Imp | Size | State |
|---|---|---|---|---|
| protected-flag-create-guard-vector | Root capability hinges on `identity.data.protected=true`, but nothing pins that a non-root actor cannot CREATE an identity with `protected:true` — step-8 exempts create by design; only identity-domain hardcoding `data:{}` guards it. Add the Gate-2/Gate-3 vector, and a Processor create-time guard if the vector finds a path. Discharges the Epic-12 carried obligation. | ★★★ | S | 📋 |
| object-plane-nats-permissions | The NKey matrix's object grants don't match the pinned nats.go: writes publish on `$O.core-objects.C/M.>`, but objmgr is granted `$OBJ.objects-base.>` (wrong prefix + bucket) and Loupe/loftspace-app get no `$O.` grant — so blob upload + GC delete should be transport-denied on the live stack. Fix the grants, add object vectors to natsperm (zero today), verify a live upload. | ★★★ | S | 📋 |
| refractor-protected-by-default-gate | Contract #6 §6.14 mandates a Postgres business lens declaring neither `protected` nor `public` fails closed (activation + lint) — neither exists: an undeclared lens activates as a plain unguarded table, and lint-conventions has no protected/public logic. Add the declare-one activation requirement + the lint gate; migrate existing plain lenses to explicit `public:true`. | ★★★ | S | 📋 |
| privacy-base-install-wiring | packages/privacy-base can't be installed (no manifest.yaml, not in the lattice-pkg registry) yet Makefile install-packages invokes it — make up-full aborts before identity-domain/objects-base install; CI never runs install-packages so gates stay green. Author the manifest, register it, add package_test.go + a verify-package gate. | ★★★ | S | 📋 |
| gateway-revocation-kill-switch-activation | The token-revocation kill-switch is dormant: the bucket is provisioned nowhere, no admin surface can revoke an actor, and a failed bucket-open at startup silently disables checking (stderr warn only). Provision the bucket in bootstrap, add a revoke surface (CLI/Loupe), surface the disabled state as a Health-KV issue. Keep the per-request fail-closed posture. | ★★ | M | 📋 |
| refractor-wire-failure-tier-backhalf | cmd/refractor never calls SetRetryQueue/SetAuditWriter, so the binary has no deferred retry (transients Nak-redeliver forever), no terminal DLQ (dropped with a log line, no errorCount bump), and never emits the audit subjects both docs advertise. Wire the retry queue + DLQ + audit writer, or ratify the Nak-only posture and rewrite the docs. | ★★ | S | 📋 |
| heartbeat-false-green-aggregation | Bridge, Gateway, and object-store-manager emit status "healthy" unconditionally while carrying (or ignoring) issues — a config-error outage rides a green heartbeat; Contract #5 requires issues empty iff healthy, and Loom/Weaver already aggregate. Port the aggregateStatus rule into all three heartbeaters. | ★★ | S | 📋 |
| lens-target-reserved-bucket-guard | A package lens may declare any NATS-KV bucket as its target; pkgmgr's bucketguard rejects only the "capability" alias and Refractor auto-creates whatever the DDL names — with Refractor's broad $KV.> grant a lens could project into loom-state/weaver-state/health-kv. Add a reserved-bucket denylist to pkgmgr validation + mirror it fail-closed in Refractor activation. | ★★ | S | 📋 |
| refractor-6-14-postgres-seam-reconcile | Three §6.14 seams drifted on Postgres read lenses: a protected/grant lens pauses dark (the heartbeat alert filters on capability-kv); the protected adapter's Delete ignores projectionSeq and hard-deletes, so a stale replay resurrects a deleted row; and the shipped RLS policy carries the M5 wildcard-anchor branch §6.14's text lacks. Extend the alert, seq-guard the delete, stage the contract edit. Supersedes lattice.md:142. | ★★ | S | 📋 |
| control-plane-surface-contract | The lattice.ctrl.* control plane (loom/weaver/refractor pause/resume/disable/revoke/delete) has no contract or component-doc of record; Loupe holds a hardcoded subject+op allow-list it calls "the entire contract Loupe holds with each plane". Document the subject grammar, op vocabulary, reply envelope, and auth posture so producers and Loupe can't silently drift. | ★★ | S | 📋 |
| contract-10-weaver-text-reconciliation | Contract #10's Weaver text drifted from as-built in five spots: §10.8 augur block (pattern+triggerLoom vs the shipped op/adapter/replyOp+directOp — a package author writes a silently-ignored field); anti-storm cross-ref vs §10.3's superseding idempotency text; two reserved weaver-state key shapes absent from §10.3; §10.2 weaver-targets read-path; post-June-19 revision-history rows. Stage one uncommitted edit for Andrew. | ★★ | S | 📋 |
| contract-wire-error-code-reconciliation | Contract #2 §2.6's error-code table diverged from the wire both ways (7 listed codes never emitted; 6 emitted codes never listed), plus §4.1 tracker class "op" vs "op-tracker" and §2.9's unknown-field claim vs the lenient parse. Reconcile the frozen text to the real closed enum (edit staged for Andrew) and pin it with a conformance test that reads the contract's table. | ★★ | S | 📋 |
| step6-batch-internal-consistency-decision | Contract #3 §3.5 and spine steps 6-7 assert validations the Processor doesn't perform (link-endpoint/aspect-host resolution, event-type DDL validation) — unbuilt and untracked. Decide build-vs-amend per layer: dangling-reference + event-DDL checks are cheap and fail-closed-aligned. Build the chosen checks or stage a contract amendment narrowing the text. | ★★ | M | 📋 |
| chronicler-prebuild-regrounding | Before Chronicler F1/F2: (a) F2 consumes events.weaver.> but Weaver emits no events — fold the new weaver lifecycle-event producer into F2, no dead scaffolding; (b) archive segments carry no object vertices and would be GC'd by objmgr's sweep — give them a dedicated bucket outside the sweep; (c) pin F1 to the published Event doc shape (payload/timestamp, not data/committedAt); (d) re-ground the loom-state terminal-cursor-deletion premise (they persist). | ★★ | M | ✅ |
| loom-pattern-source-cold-registry | loom.md tells operators to set a stable Instance, but the pattern-source durable derives from it and resumes from its ack floor — after a crash a stable-Instance Loom reattaches empty, boots with no pattern registry, and new triggers Nak-loop until a meta vertex is rewritten. Un-armed only because no deployment sets LOOM_INSTANCE. Suffix the durable with a per-boot nonce and fix the contradictory source.go comments. | ★★ | S | 📋 |
| natsperm-matrix-hygiene | The transport matrix carries stale/over-broad grants: bridge is allowed $KV.bridge-external.> / $KV.bridge-schedule.> (those are consumer names, not buckets; bridge writes only health-kv), and Refractor's $KV.> lets it write engine-state/health rather than an explicit lens-target set. Prune the stale grants, narrow or explicitly-deny Refractor's, extend the natsperm vectors. | ★★ | S | 📋 |
| objmgr-and-bootstrap-component-pages | object-store-manager and bootstrap are always-on platform binaries with no docs/components page, no README row, no architecture-overview mention, and no Surveyor-rotation slot; vault + privacyworker are also built but page-less. Write the four pages (owns/reads/writes/contracts/failure-modes/status), add the index/README rows, and add objmgr + bootstrap to the survey rotation. | ★★ | M | 📋 |
| bridge-and-substrate-doc-refresh | bridge.md predates the async SPI/dispatchOp field/poll-timeout schedule lane/Augur and is the envelope spec's home; scheduling.md omits the bridge and still calls @every deferred; substrate.md shows a wrong AtomicBatch signature (timeout vs ctx), omits six files + the object/publish/schedule surface, and has a SubscribeKVChanges godoc that contradicts the code. Refresh all three to as-built. | ★★ | S | 📋 |
| contract7-and-processor-mandate-refresh | Contract #7 §7.2/§7.7 describe a superseded kernel (5 meta-meta DDLs, processor identity, topology-walk cypher) — stage the alignment edit for Andrew; and processor.md/doc.go omit step 6.5 encryption, the §3.2 OCC retry, §10.7 task auto-completion, and kv.Links, with commit_path.go retaining "stubbed 4-10"/"auth (stub)" comments. Fix the stale bootstrap security-plane comment asserting a graph-walk. | ★ | S | 📋 |
| docs-truth-sweep-security-plane | Align stale security/status docs: components index says Gateway+Vault "Designed" (both built); service-actors.md says "there is no Gateway", calls transport enforcement pending (live), and omits the seeded objmgr actor; gateway.md cites nonexistent Contract-2 §2.34/§2.39 and overstates the ops-publish matrix; service-location CONCEPT.md keeps the dropped serviceClass. Flag (not edit) the frozen §6.12/§6.5 drift for Andrew. | ★ | S | 📋 |
| weaver-exhausted-escalation-and-model | The ratified augur block accepts "exhausted" as an escalation trigger and parses augur.model, but no engine path fires either — a budget-exhausted gap is silently skipped with no escalation/Health issue, and model is consumed by nothing. Wire the exhausted trigger through augurEscalation (threading model), or strike both from the block; as-is the contract advertises capability the engine doesn't deliver. | ★ | S | 📋 |
| loom-dispatch-authcontext-target | Loom builds each step op's authContext.target as "vtx.meta."+PatternID, but PatternID is a human-readable name while the real vertex is vtx.meta.<NanoID> — live externalTask ops carry a dangling target in the forbidden vtx.meta.<canonicalName> shape. Inert under scope-any, breaks when scope-specific auth lands. Carry the pattern's real meta key through source+pin, and fix pattern.go's false comment. | ★ | S | 📋 |
| gate3-vector14-in-gate | Gate 3's gateway-impersonation vector #14 is backed by a test in internal/gateway that the make target never runs (it only runs internal/bypass), so the gate could report DEFENDED while the test fails. Add the gateway package to the gate's scope or add an in-package bypass test, and refresh the stale four-vector Makefile comment + Gate-2 row-1 enforcement text. | ★ | S | 📋 |
| repo-debris-and-stale-narration | Remove the five resolved CONTRACT-AMENDMENT-REQUEST.md journals (cmd/{loom,processor,refractor,weaver}, internal/substrate — git is the record), the pre-cascade comment clusters (objmgr package doc + objects-base OpMetas naming a nonexistent reclaim Loom pattern; loom doc.go), and decide internal/spike disposition (delete or a README declaring it excluded from gates). Fix the objmgr Makefile launch missing BOOTSTRAP_JSON_PATH. | ★ | S | 📋 |
| contract10-async-deadline-reconcile | Contract #10's async paragraph says the Loom step deadline is per-adapter-sized and backstops a dead bridge, but the same contract's §10.6 and the code disarm that deadline at instanceOp commit and make the bridge wait unbounded (FailPattern is the out-of-band close). Stage a reconciling edit; tighten the bridge fired-consumer filter wording; note the single global CallDeadline as deferred-with-real-adapters. | ★ | XS | 📋 |

### Loupe lane

| Item | What | Imp | Size | State |
|---|---|---|---|---|
| loupe-api-csrf-rebind-guard | The auth-less admin API trusts the loopback bind but not the operator's browser: no Origin/Sec-Fetch-Site check on mutating routes, no Host allowlist, and POST /api/op accepts text/plain — so any web page can blind-submit operations as the primordial admin (no-preflight CORS-simple), and DNS rebinding defeats loopback for reads. Add a Host allowlist + same-origin check on POST/DELETE + require application/json on /api/op; state the posture in loupe.md. | ★★ | S | 📋 |
| loupe-md-drift-sweep | loupe.md overclaims package install/uninstall in three places (code/UI are list-only — that's F8); its framing-of-record pointers are dead (the "Active initiative" section moved to loupe.md; lattice-architecture.md has no experience-layer Loupe row). Fix the three claims + both pointers; note the Loupe 2.0 program as current framing. | ★ | XS | 📋 |
| loupe-tasks-honor-tombstones | computeTasks ignores isDeleted on task roots and on the assignedTo/forOperation/scopedTo links it walks, so a soft-tombstoned task still renders and a tombstoned link still populates the row. The Files tab + vertex list already treat tombstones correctly; bring the task assembler in line (skip deleted roots + links) with coverage. No Loupe 2.0 fire touches tasks.go. | ★ | S | 📋 |

### Verticals lane

| Item | What | Imp | Size | State |
|---|---|---|---|---|
| ledger-package-verify-gates | clinic-ledger and loftspace-ledger are the only registered vertical packages with zero CI install coverage — no verify-package target, and install-clinic/install-loftspace aren't CI targets, so a guardrail or key-shape regression lands unnoticed until a live install. Add verify-package targets that co-install each ledger with its domain dependency and assert the projected lens buckets + create-only guard-aspect keys. | ★ | S | 📋 |

---

## 6. Scope notes & honest limits

- **Read-only, static.** No builds/tests were run; coverage claims come from reading the suites, not
  executing them. The one runtime-verified finding (object-plane grants) was confirmed against the pinned
  vendor source + the grant matrix, but a live blob-upload denial was not reproduced this session — the row
  asks the builder to verify with a live upload.
- **Dedup is best-effort.** Proposed rows were deduped against `backlog/{lattice,verticals,loupe}.md` as read
  at review time; a few findings map to *updates* of existing rows (§5 opening) rather than new ones.
- **Frozen-contract findings are flagged, never edited.** Every contract reconciliation above is a proposal
  for Andrew — the diff, when made, is the proposal (CLAUDE.md convention). None were staged in this
  read-only pass.
- **The ★★★ items are pre-emptive.** None is a live incident: the create-guard and object-plane gaps are
  inert today (convention / unexercised path), the protected-by-default gap needs a mis-declared lens to bite,
  and the privacy-base break only hits `make up-full`. They rank highest because their blast radius is the
  security plane or dev-stack bring-up, not because anything is presently on fire.
