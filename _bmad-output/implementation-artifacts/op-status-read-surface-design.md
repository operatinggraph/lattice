# Op-status read surface — a sanctioned way to ask "did my operation land?"

**Status: 🏗️ Fire 1 SHIPPED (`f12f4ce`, 2026-07-12)** — the `lattice.op.status` responder
(`internal/opstatus`) + the bridge's skip-on-redelivery probe migration are live; the interim
`BRIDGE_SKIP_ON_REDELIVERY=false` mitigation is REVERTED on the shared dev stack (bridge/processor
restarted with the new binaries, `deploy/nats-server.conf` regenerated + `lattice-nats` restarted to
pick up the bridge's `lattice.op.status` grant). Fires 2–4 (Gateway status endpoint, Loom probe
migration, CLI) remain open. Ratified (Andrew, 2026-07-11): Fire 1 as designed; the Fires 2–4
sequencing accepted; the interim mitigation approved and APPLIED the same session (bridge restarted
with `BRIDGE_SKIP_ON_REDELIVERY=false` — the env lever added in cmd/bridge — all 6 stuck events
drained, result ops committed, bridge health clean). Designer: Winston (main session, 2026-07-11) ·
Origin: live incident (bridge skip-on-redelivery probe broken by the same-day read-tightening) +
Andrew's framing: read posture was meant to stop components reading *business data* from Core KV;
checking the status of an op you submitted is a legitimate generic need everyone has, and today the
only way to do it is a Core KV `Get`.

## Ratification block (decided 2026-07-11; kept for the build fire's context)

- **Confirmed incident.** The natsperm-matrix-hygiene Fire 1 read-tightening (2026-07-11, `4258180`,
  executing sensitive-param-egress §8/B2) denies the bridge `$JS.API.DIRECT.GET.KV_core-kv(.>)`. That
  correctly pins the decrypt-RPC-holding bridge away from the Core KV corpus — but it also breaks the
  bridge's month-old **skip-on-redelivery probe** (Story 13.4, `internal/bridge/dispatch.go
  resultAlreadyLanded`): a generic Contract #4 tracker `Get` on `vtx.op.<replyReqID>`, used on redelivery
  to avoid re-calling an external vendor. The probe's error path is NakWithDelay, so every affected
  external event now **redelivers every ~5s forever** (observed: 6+ ops cycling, thousands of
  `Publish Violation` log lines/day, bridge `degraded` in Health KV).
- **Proposal (Fire 1): a Processor-hosted op-status responder** on `lattice.op.status` — the exact
  pattern of the existing `lattice.vault.decrypt` responder. Request `{requestId}`; the Processor (the
  sanctioned Core KV reader) does the tracker `Get` and replies with the Contract #4 verdict
  `{found, committed, isDeleted, committedAt, class}`. The bridge probe switches from `conn.KVGet` to
  this request. Transport: pub-allow `lattice.op.status` for components that submit ops (bridge now;
  the others when they migrate) — a single-subject grant, no KV read surface.
- **Interim mitigation (named, not applied):** `BridgeConfig.SkipOnRedelivery` is an optional
  defense-in-depth mechanism (adapters already dedup on the reused idempotencyKey). Restarting the
  bridge with it disabled stops the redelivery loop and drains the stuck events today, at the cost of
  one redundant (idempotent) adapter call per redelivery. Re-enable when Fire 1 ships.
- **Why only the bridge broke (reviewed with Andrew 2026-07-11):** the B2 deny was deliberately scoped
  to the one decrypt-grant-holding component; every other submitter still rides the account-wide
  read-side laxity. The full submitter map (§1.5) shows Loom carries THREE contracted tracker probes
  and the CLI a raw tracker read — all of which break identically the day the read-side-laxity
  follow-up tightens matrix-wide. **This RPC is therefore the prerequisite for ever closing that row,
  not a bridge patch.** Weaver alone is structurally immune (resubmit-and-collapse, §1.5).
- **No contract change in Fire 1.** Contract #4 §4.4's dedup semantics are unchanged — this relocates
  WHERE the read runs (Processor-side, behind a subject-scoped RPC), not what it means. One touchpoint
  is NAMED for the Loom follow-on: Contract #10 §10.6 words Loom's probe as a direct GET and will need
  reconciling when Loom migrates (§4).
- **Decisions (Andrew, 2026-07-11):** Fire 1 (`lattice.op.status` responder + bridge migration)
  RATIFIED; Fires 2–4 sequencing ACCEPTED; interim probe-off mitigation APPROVED and applied (see
  Status). Fire 1 is build-ready for the Lattice Steward.

## 1. Problem & grounding (verified against code + the live stack this session)

1. **The probe** (`internal/bridge/dispatch.go:159-177`, second call site `schedule.go:147`): on
   external-event redelivery (and on every poll/timeout schedule firing), the bridge `Get`s the generic
   op tracker `vtx.op.<replyReqID>` — deliberately the type-agnostic Contract #4 key, never a typed
   claim vertex — and skips the vendor call if the result op already landed. `SkipOnRedelivery`
   defaults to true (`engine.go:133`).
2. **The deny** (`internal/natsperm/matrix.go`, bridge `ExtraPubDeny`): `$JS.API.DIRECT.GET.KV_core-kv`
   + `.>` + `STREAM.MSG.GET` — sensitive-param-egress §8/B2's read-tightening, landed 2026-07-11 in the
   matrix-hygiene fire. nats.go serves `KVGet` on an AllowDirect bucket via DIRECT.GET, so the probe's
   read dies at the transport (5s client timeout → `context deadline exceeded` → NakWithDelay → loop).
   NATS deny-wins semantics mean the deny cannot be "excepted" for `vtx.op.>` under the same prefix.
3. **Who reads op status today:**
   - the bridge probe (broken — the incident);
   - `lattice op status <requestId>` (cmd/lattice/op/op.go:190 — a raw `KVGet` of the tracker; works
     because only the bridge carries the read-deny today, but it is the same exposure class the
     matrix-hygiene "account-wide read-side laxity" follow-up will eventually tighten);
   - submit-time callers get the reply on their `Lattice-Reply-Inbox` — fine for the synchronous case,
     gone after a process restart, which is exactly why the bridge probes on REDELIVERY.
4. **Read posture** (Andrew, this session): the Core-KV read restriction exists to keep components from
   reading *business data* — Processor reads Core KV; everyone else consumes CDC / core-events / lens
   projections. The op tracker is not business data, but it lives in the same bucket, so any KV-level
   grant that reaches it reaches everything (subject algebra can allow `$KV.core-kv.vtx.op.>` writes,
   but the READ side channel is the JS API DIRECT.GET/MSG.GET surface, which is per-STREAM, not
   per-subject-scoped — a KV-read grant cannot be narrowed to one key prefix).

### 1.5 The full submitter map (review finding, 2026-07-11: "why did only the bridge break?")

How every op-submitting component learns its op's outcome — grounded per call site:

- **Weaver — never reads.** The actuator re-submits the same deterministic requestId and lets the
  Contract #4 step-2 dedup collapse the duplicate Processor-side (`internal/weaver/actuator.go:59,
  139,212`, `temporal.go:181`). Write-only idempotency; reconciliation re-derives everything from the
  lens targets + weaver-state. Structurally immune to any read tightening — and the pattern the
  bridge CANNOT copy (a resubmit re-executes an external vendor call; the probe exists precisely for
  at-most-once side effects).
- **Loom — reads the tracker at three call sites** (`internal/loom/engine.go:1215,1277,1339`,
  `trackerExists`) — the CONTRACTED §10.6 deadline-expiry read-before-act probe (committed → advance;
  outbox-pending → re-arm; else → fail) — **plus a business-vertex probe** `taskVertexExists`
  (`vtx.task.<id>`, engine.go:1263) ending a userTask's bounded creation wait. Works today only
  because Loom is not read-denied. Breaks identically to the bridge under matrix-wide tightening.
- **Gateway — synchronous reply-inbox wait; the async fallback is fiction.** On submit timeout it
  returns 202 + requestId with a comment saying "the caller polls Core KV for read-your-own-writes"
  (`internal/gateway/gateway.go:450`) — but its callers are BROWSERS holding bearer JWTs with no KV
  access of any kind, and the route table (`/v1/operations`, `/v1/actor`, `/v1/<readmodel>`) has no
  status endpoint. The 202 contract is currently unsatisfiable by its intended audience.
- **lattice CLI / lattice-pkg / Loupe-relay** — synchronous reply-inbox waiters; plus the CLI's
  `op status` raw tracker KVGet (§1.3).
- **privacyworker / refractor keyshredded** (RecordShredFinalization) — fire-and-forget;
  detect+recover posture, no status check by design.

## 2. The shape (Fire 1)

**`lattice.op.status` — a Processor-hosted request-reply responder**, mirroring the
`lattice.vault.decrypt` responder (`cmd/processor/main.go:265-279`, `internal/processor/
sensitive_decrypt.go`): same host loop, same subject-scoped transport gate, same "the Processor is the
only component that touches Core KV" invariant.

- **Request** `{"requestId": "<NanoID>"}`. Bare-id validated (no dots/wildcards) before key
  construction — the responder never lets a caller shape an arbitrary key.
- **Reply** `{"found": bool, "committed": bool, "isDeleted": bool, "committedAt": "...", "class": "..."}`
  — a projection of the Contract #4 tracker (`vtx.op.<requestId>` ONLY; the responder reads no other
  key shape). `found:false` after TTL expiry is the contracted §4.3 answer, same as today's raw read.
- **AuthZ:** transport-level (natsperm pub-allow `lattice.op.status` on the components that need it —
  bridge in Fire 1). No in-handler identity check, matching the vault RPC's pre-existing posture; the
  reply exposes op METADATA (status/class/timestamps), never payloads, so its blast radius is a
  traffic oracle at worst. If per-actor scoping is ever wanted ("only the submitter may ask"), the
  tracker's `createdBy` is already in the doc — a follow-on, not Fire 1.
- **Bridge migration (same fire):** `resultAlreadyLanded` gains a `statusClient` seam — the NATS
  request against `lattice.op.status` with the existing 5s timeout; `engine.go` wires it; the KVGet
  path is removed (not fallback-kept — a silent fallback would hide a broken grant again). The landed
  test stays byte-identical: `found && !isDeleted`.
- **natsperm:** bridge `ExtraPubAllow` += `lattice.op.status`; processor `AllowResponses` already
  covers the reply leg (as it does for vault.decrypt). One conformance vector: bridge can request
  op-status; a vertical app (no grant) cannot.

## 3. Alternatives considered (and why not)

- **A. Narrow the deny to spare `vtx.op.>`** — impossible: NATS deny-wins; DIRECT.GET denies are
  per-stream (`KV_core-kv`), not per-key, so any carve-out reopens the whole corpus. Enumerating
  denies for every OTHER prefix is an unmaintainable blacklist that silently reopens on every new
  vertex type.
- **B. Op-status lens (Refractor projects `vtx.op.*` to a shared read-model bucket)** — P5-shaped but
  heavy: doubles the write volume of EVERY committed op to serve an occasional probe, needs TTL
  propagation into the target bucket, and creates a second copy of the idempotency surface that can
  lag its source exactly when the probe needs truth. Reads-are-lenses is the rule for *business*
  read models; the tracker is op-machinery, and its one sanctioned reader pattern (dedup) is
  point-lookup-by-known-id — RPC-shaped, not projection-shaped.
- **C. Bridge-local event-derived state** — the bridge already consumes core-events and could track
  landed replyReqIDs itself, but the state dies with the process (the probe exists precisely for the
  restart case), and rebuilding it needs bounded replay machinery per component. Bespoke where B2's
  point was a generic surface.
- **C′. Weaver's resubmit-and-collapse (write-only idempotency)** — the read-free pattern §1.5
  credits, and the first alternative to reach for in any NEW design: if re-executing is harmless,
  resubmit and let the tracker dedup collapse it, and no status read is needed at all. Unavailable
  to the bridge (re-execution = a second vendor call) and to Loom's §10.6 rejected-vs-slow
  distinction (a resubmit cannot tell you WHICH outcome the deadline expiry means).
- **D. Roll back the bridge read-deny** — reopens sensitive-param-egress B2 (a decrypt-RPC-holding
  bridge able to read the whole identity corpus). The deny is doing its job; the probe is the one
  legitimate read it caught in the blast radius.

## 4. Consumers & sequencing

- **Fire 1 (this design):** responder + bridge migration + natsperm vector. Unblocks the live
  degradation; the stuck events drain on their next redelivery when the probe succeeds.
- **Fire 2 — Gateway status endpoint (`GET /v1/operations/{requestId}`)**, backed by the RPC: turns
  the 202 fallback's currently-unsatisfiable "the caller polls Core KV" contract (§1.5) into a real
  read-your-own-writes poll for browser actors. The strongest product consumer of the surface; a
  Vertical-PO demand row should pair with it (build-over-defer: the consumer is nameable today).
- **Fire 3 — Loom probe migration** (`trackerExists` ×3 → the RPC). Requires the Contract #10 §10.6
  wording reconciliation (it currently names a direct GET) — the contract edit rides the fire,
  staged uncommitted for Andrew per house rules. Loom's `taskVertexExists` (`vtx.task.<id>` — a
  business vertex, out of a vtx.op-scoped RPC's reach) is NAMED here, not solved: candidate
  dispositions are (a) derive the same signal from the CreateTask op's tracker via the RPC, or
  (b) a narrow sanctioned Loom read alongside its existing provisional guard-read exception —
  the fire grounds which.
- **Fire 4 — `lattice op status` CLI** migrates off its raw KVGet.
- **Then, and only then,** the matrix-hygiene "account-wide read-side laxity" row can extend the
  DIRECT.GET deny matrix-wide without breaking anyone — the sequencing that makes this design the
  standing answer rather than a bridge patch. Fires 2–4 are independent of each other; all depend
  on Fire 1.

## 5. Risks & edges

- **Processor unavailability now fails the probe** (it already effectively does: the tracker is
  written by the Processor, and with the Processor down nothing new lands). The probe's existing
  NakWithDelay path handles it; the redelivery resolves when the Processor returns.
- **One more hop on the redelivery path** (~1 RTT + a KVGet, Processor-side). The probe runs only on
  redeliveries and schedule firings, never the hot dispatch path.
- **Traffic oracle:** any grant-holder can ask whether an arbitrary requestId committed. Accepted for
  Fire 1 (metadata-only, matches the vault RPC's caller-trust posture); the per-actor scoping
  follow-on is named above if it ever matters.
