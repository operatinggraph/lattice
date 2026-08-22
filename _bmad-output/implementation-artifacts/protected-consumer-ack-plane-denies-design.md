# `$JS.ACK.>` is a read primitive, and the protected-consumer registry does not cover it

**Status: ✅ Winston-ratified — build-ready.** · Lattice Steward fire 2026-08-22 (unattended, remote).
**Lane:** Lattice (Stream 2). **Board row:** *[natsperm] `$JS.ACK.>` is a cross-stream READ primitive, not
just an ack-forge* (★★ · S · `internal/natsperm/matrix.go`).
**Filed by:** [app-tier-transport-read-scope-design.md](app-tier-transport-read-scope-design.md) §14/F1,
whose adversarial pass withdrew the matrix's own G17 claim that `$JS.ACK.>` is "consumer protocol plumbing …
not a data-plane privilege".
**Contracts:** none changed. The permission matrix (`internal/natsperm`, `deploy/nats-server.conf`,
`deploy/gen-dev-nkeys`) is component/deploy surface, not a frozen contract — the classification the three
already-ratified matrix designs used.
**Relationship to the read-scope design:** that design is 📐 awaiting-Andrew on a *posture fork* (replace the
blanket `$JS.API.>` publish grant with a declared per-component read scope). Nothing here touches that fork.
This is the write-side hygiene the matrix already does — removing a publish grant that is provably unneeded,
and closing a registry that names a consumer's other inbound verbs but not this one.

---

## 1. What is wrong

`internal/natsperm` already treats six core-events durables as a security-plane registry
(`coreEventsProtectedConsumers`, `matrix.go:103-110`): the two crypto-shred workers, Refractor's two
post-shred nullifiers, and the Gateway's revocation / credential-binding materializers. Suppressing or
draining any of them is a silent, irreversible security outcome, so the matrix denies their admin verbs to
every component (`coreEventsAdminDenies`, `:130-137`) and their create / pull verbs to every component but
the owner (`coreEventsOwnerOnlyDenies`, `:165-172`).

**That registry names four inbound subjects per consumer and misses the fifth.** A JetStream consumer with an
ack policy subscribes to its own **ack subject family**, and publishing to it is not merely acknowledgement:

- `+NXT` on an ack subject **acks the current message and dispatches a next-message request**, delivered to
  the *publisher's own reply subject* (`server/consumer.go:2736-2738`). It is `MSG.NEXT` by another door —
  a **read**, against a consumer the registry deliberately denies `MSG.NEXT` on (`matrix.go:170`).
- A bare `+ACK`/`+TERM` on the same subject is the original board item: silent suppression of a pending
  shred, revocation or binding event, with no admin verb touched.

Sixteen of the eighteen matrix components hold `$JS.ACK.>` — an unscoped publish grant on the whole family
(`matrix.go:287,302,312,323,354,393,411,438,443,461,472,486,503,508,513,518`; only `model-runner` and
`facet` do not). Any one of them reaches all six protected consumers today.

## 2. Grounding ledger

Vendor claims are cited against the pinned `github.com/nats-io/nats-server/v2 v2.14.0` (`go.mod:12`), per
`docs/vendors.md`. Each row was read in the pinned source, not inferred from a comment — the F1 lesson.

| # | Claim | Evidence |
|---|---|---|
| G1 | `+NXT` on an ack subject is a read: `processAck` acks, then calls `processNextMsgRequest(reply, …)`, delivering to the publisher-chosen reply subject. Nothing checks who published | `server/consumer.go:2716`, `:2736-2738` |
| G2 | A consumer subscribes to **both** ack formats, **unconditionally** — the v2 subscription is not gated on the v2 feature flag, which only decides which format the server *stamps* on delivered messages | `server/consumer.go:1699-1707`; flag read at `:1383`, default `false` at `server/feature_flags.go:35` |
| G3 | v1 ack subject = `$JS.ACK.<stream>.<consumer>.*.*.*.*.*` (exactly 5 trailing tokens) | `server/consumer.go:1388-1390`; template `jsAckT = "$JS.ACK.%s.%s"`, `server/jetstream_api.go:220` |
| G4 | v2 ack subject = `$JS.ACK.<domain>.<accHash>.<stream>.<consumer>.*.*.*.*.>` (≥5 trailing tokens) | `server/consumer.go:1395-1398`; template `jsAckTv2`, `server/jetstream_api.go:221` |
| G5 | `<domain>` is the literal `_` when no JetStream domain is configured — ours configures none (`deploy/nats-server.conf`'s `jetstream {}` block sets only `store_dir`) | `server/consumer.go:1377-1379` |
| G6 | `<accHash>` is a deterministic 8-character base-62 SHA-256 of the **account name**, and our conf declares no `accounts` block, so every component user lands in the global account `$G` — the hash is a constant any holder of the grant can compute offline | `server/consumer.go:1381`; `server/events.go:1151-1163`; `digits`/`base` at `server/accounts.go:2363-2364`; `DEFAULT_GLOBAL_ACCOUNT` at `server/const.go:247` |
| G7 | `+NXT` does not need real sequence numbers: `ackReplyInfo` returns zeroes for a subject it cannot parse, and the `+NXT` arm runs `processNextMsgRequest` regardless of what `processAckMsg` did with them | `server/consumer.go:2736-2738`, `:6038-6060` |
| G8 | The ack subscriptions exist only when `AckPolicy` is neither `AckNone` nor `AckFlowControl` — so this is a live inbound path exactly for explicit-ack consumers, which all six protected ones are | `server/consumer.go:1699` |
| G9 | No **bare** ack subject (`$JS.ACK.<stream>.<consumer>` with no trailing tokens) is a live inbound path: both subscriptions require ≥5 trailing tokens (G3/G4). The bare-vs-wildcard rule the Bridge's `DIRECT.GET` denies follow (`matrix.go:378-386`) therefore does **not** apply here | G3, G4 |

**The class this belongs to.** The matrix has now been bitten three times by the same shape: *a deny that
names one wire form of a subject family that the server accepts in several.* `DIRECT.GET` needed the bare
form alongside the wildcard (`matrix.go:378-386`); `CONSUMER.CREATE` needed the filtered form alongside the
bare one and the legacy `DURABLE.CREATE` family (`matrix.go:144-157`); the ack family needs v1 alongside v2.
An **allow** that misses a form merely breaks a client (the per-identity edge grant lists only the v1 ack
shape, `internal/gateway/natsauth/natsauth.go:371` — narrow, therefore safe). A **deny** that misses a form
is a bypass. §5 mechanizes the check rather than adding a fourth comment about it.

## 3. The shape

**3.1 The registry gains the ack family, owner-scoped.** A new `coreEventsAckDenies(stream, name)` returns
both wire forms, appended inside `coreEventsOwnerOnlyDenies` — the owner genuinely publishes acks for its own
durable, so this cannot be universal like `coreEventsAdminDenies`:

```
$JS.ACK.<stream>.<name>.>          # v1 (G3)
$JS.ACK.*.*.<stream>.<name>.>      # v2 — domain and accHash are runtime values, so wildcards (G4/G5/G6)
```

The v2 wildcards cannot over-deny a v1 subject: matching one would require a v1 ack whose *trailing* tokens
begin `<stream>.<name>`, and those five tokens are always numeric (`server/consumer.go:1389`).

**3.2 The four vertical apps lose `$JS.ACK.>` entirely.** The tier the platform already decided not to trust
(#75 Fire 2b, `matrix.go:491`) holds an unscoped grant on every consumer in the deployment while running no
ack-policy consumer of its own — §4 is the census. This is a strict narrowing of a publish allow-list, not a
posture change: it neither adds a read scope nor removes `$JS.API.>`, so it is independent of the read-scope
design's Andrew fork, and it shrinks that design's Increment 1 rather than pre-empting it.

**3.3 What this does NOT close, stated plainly.** `$JS.ACK.>` remains an unscoped read primitive against
every *other* consumer on every stream for the twelve platform components that keep it. Closing that needs
per-component ack scoping, which needs a component's consumer names to be statically enumerable — they are
not (Loom/Weaver mint per-domain consumers, Refractor per-lens). The read-scope design owns that; this fire
closes the security-plane registry it names and the untrusted tier that provably needs nothing.

## 4. Executable census — does the app tier ever publish an ack?

A client publishes to `$JS.ACK.…` only when acknowledging a delivery from a consumer whose `AckPolicy` is not
`AckNone` (G8). The census enumerates every JetStream call path reachable from the four `cmd/<x>-app`
binaries and the ack policy each bottoms out in. Re-run before relying on it:

```sh
for a in clinic cafe wellness loftspace; do
  echo "== $a"; go list -deps ./cmd/$a-app | grep lattice/internal
done
grep -rn '\.Ack(\|\.Nak(\|\.Term(\|\.InProgress(\|\.DoubleAck(' cmd/*-app internal/substrate
```

**Run 2026-08-22 — the count of ack-publishing call sites reachable from the four app binaries is zero.**
Their entire NATS surface is `substrate.Connect`, an `OpenKV` handle on `token-revocation` read through
`internal/gateway/revocation` (`cmd/clinic-app/main.go:126`, `:177`, `:180` and the analogous lines in the
other three), `KVGet`/`KVListKeys*` (`internal/substrate/kv.go:37`, `:195`, `:234`, `:282`), and
`healthkv`'s `KVPutWithTTL` heartbeat (`internal/healthkv/reporter.go:207`), which is a plain publish to
`$KV.health-kv.>`. None of the four creates a consumer, and none calls `Ack`/`Nak`/`Term`/`InProgress`/
`DoubleAck`.

The listing path is the one that could have surprised us, and it does not: `KVListKeys` bottoms out in
nats.go's **ordered** consumer, which is constructed `AckPolicy: AckNonePolicy`
(`nats.go@v1.52.0/jetstream/ordered.go:634`; the legacy KV watcher takes the same route,
`jetstream/kv.go:1304-1305`). By G8 the server does not even subscribe an ack subject for such a consumer,
so there is nothing for the app to publish to.

For contrast, every component that *does* run a durable consumer builds it `AckExplicitPolicy` and acks
(`internal/substrate/consumer.go:214`, `internal/substrate/consumer_supervisor.go:592`;
`jetstream/message.go:430-432` publishes the ack to `msg.Reply`, an ack subject) — which is why §3.1's deny
must stay owner-scoped, and why the grant stays for the platform tier.

## 5. Test strategy — an oracle, not a string comparison

Every vector runs against the **committed** `deploy/nats-server.conf` with the components' **real** dev NKey
seeds, via this package's existing live harness (`startServerFromConf` / `connectAs`,
`conf_test.go:59`/`:113`). Structural assertions over the Go `Deny()` slice are deliberately *not* the proof:
they would pass for a subject the server never subscribes.

1. **Positive control first (standing checklist #3).** The owner publishes `+NXT` to each protected
   consumer's ack subject — **both wire forms** — as a NATS request, and receives the pending event. This is
   what makes the negative meaningful: it proves the subject is a live inbound read path, and, for the v2
   form, that the domain/`accHash` this test derives are the ones the server actually subscribed. A wrong
   hash fails here loudly instead of turning every negative vector into a vacuous pass.
2. **Negative, registry-driven.** For each of the six protected consumers × every non-owner component from
   `nonBootstrapComponentNames()`, the same request on both forms is denied. Derived from source on both
   axes, so a new component or a new protected consumer is covered without a hand-edited list — the
   `TestRegistryDrivenWriteIsolation` precedent (`conf_test.go:933`).
3. **Owner exception is real.** The owner is excluded from the deny loop and its own ack still lands
   (vector 1 is that proof).
4. **App tier holds no ack grant at all**, proven by a denied `+NXT` against a *live, unprotected* consumer
   — not by the absence of a string in a slice. Plus a live read regression: an app credential still opens a
   KV handle, `KVGet`s and lists keys after losing the grant.
5. **Drift.** `TestConfMatchesMatrix` (`conf_test.go:1223`) fails until `go run ./deploy/gen-dev-nkeys`
   regenerates the committed conf; the regenerated conf is part of this fire's diff.

**Mechanizing the class (§2).** Vector 2's subject set is built by an **oracle**: the test derives a
protected consumer's inbound ack subjects the way the server does (v1 and v2, from the pinned templates)
rather than restating the strings the matrix denies. A future third wire form added by the server changes
what the oracle generates and the vector goes red — which is what a mechanized check for "a deny must cover
every form the server accepts" looks like when the form set lives in the vendor, not in our source.

## 6. Fire brief (build note, 2026-08-22)

Compiled from two read-only scouts (natsperm test surface; app-tier ack blast radius) plus a direct read of
the pinned nats-server. `agents/fire-brief-template.md` shape.

**1 · Scope sentence (verbatim, board row).** *"`+NXT` on an ack subject dispatches a next-message request
delivering to the caller's reply subject (`server/consumer.go:2736`), publisher unchecked — so the grant
reads any PULL consumer on any stream, not only suppression on the 6 core-events durables. Deny registry
covers neither."* **Green bar:** the registry covers the ack family in both wire forms, owner-scoped, proven
against the committed conf with real credentials; the untrusted app tier holds no ack grant; full gates +
CI green.

**2 · Verified touch-list** (anchors re-checked live 2026-08-22):

| File | Anchor | Edit |
|---|---|---|
| `internal/natsperm/matrix.go` | `:165-172` `coreEventsOwnerOnlyDenies` | append the ack family via a new `coreEventsAckDenies` |
| `internal/natsperm/matrix.go` | `:503`, `:508`, `:513`, `:518` | drop `"$JS.ACK.>"` from loftspace/clinic/cafe/wellness-app |
| `internal/natsperm/conf_test.go` | `:750-875` `TestCoreEventsSideChannel` | ack vectors: positive control + registry-driven negatives |
| `internal/natsperm/conf_test.go` | `:485` `TestVerticalAppOpsPublishDenied` neighbourhood | new app-tier ack-denied + read-still-works vectors |
| `deploy/nats-server.conf` | generated | regenerate (`go run ./deploy/gen-dev-nkeys`) |

**3 · Precedents to mirror.** The deny helper mirrors `coreEventsOwnerOnlyDenies`/`coreEventsAdminDenies`
(`matrix.go:130-172`) exactly — a per-consumer subject builder called from `Deny()`'s registry loop
(`:246-251`). The live vectors mirror `TestCoreEventsSideChannel`'s owner-only loop (`conf_test.go:851-873`)
including its positive controls, and its component axis comes from `nonBootstrapComponentNames()`
(`conf_test.go:915`) the way `TestRegistryDrivenWriteIsolation` (`:933`) derives its own. Denial is observed
as "a request that gets no reply within `deniedTimeout`" (`conf_test.go:35`), the idiom every vector in the
file uses. Nothing here is greenfield.

**4 · Increment order.**

- **Inc 1 — the registry.** `coreEventsAckDenies` + its wiring; `go test ./internal/natsperm/ -run
  'TestCoreEventsSideChannel|TestConfMatchesMatrix' -count=1` (expected: conf drift RED until Inc 3).
- **Inc 2 — the app tier.** Drop the four grants; `go test ./internal/natsperm/ -count=1`.
- **Inc 3 — regenerate + full package.** `go run ./deploy/gen-dev-nkeys`; `go test ./internal/natsperm/
  -count=1` all green, `git diff --stat deploy/nats-server.conf` shows only the intended lines.
- **Gates.** `go build ./...`; `make vet`; `golangci-lint run ./...`; `STRICT=1 go run
  ./scripts/lint-conventions.go`; `go test ./... -p 4` with `POSTGRES_TEST_DSN` set (REMOTE.md §3 — a remote
  suite without Postgres is falsely green).

**5 · In-scope gotchas.**

- `deploy/nats-server.conf` is **generated**; hand-editing it fails `TestConfMatchesMatrix`
  (`conf_test.go:1223`). Regenerate and commit the result in the same change.
- A deny must cover **every wire form the server accepts** (§2). This fire's own trap is the v2 ack subject
  (G4) — invisible in this deployment's traffic, live in the server's subscription set.
- Substrate dossier entries this fire trips, copied in: *"A vendor-behaviour claim in a comment needs a
  pinned `file:line`"* — the entry this item was minted by, since the matrix's own `$JS.ACK.>` comment
  (`matrix.go:189-191`) is the wrong claim being corrected; *"Adopting ONE `ConsumerLimits` field
  re-validates every existing consumer"* — the general form applies: a permission test on a *fresh* fixture
  cannot see what a populated one would, so the ack vectors run against a consumer that has a real pending
  message; *"A server-immutable consumer field needs delete-then-create in BOTH directions"* — the
  positive-control consumers must be created and torn down the way `TestCoreEventsSideChannel` already does.
- Standing checklist: #3 (negative needs its positive vector proven first) is load-bearing here and is why
  §5 vector 1 exists; #6 (precedent may carry debt) — the precedent being mirrored is precisely the
  registry that shipped incomplete.

**6 · Adjacent finds.** (a) The seven unprotected core-events durables (object-store-manager,
object-store-cascade, loom-trigger, loom-deadline, weaver-temporal, weaver-sweep, bridge-external) are
reachable by the same primitive — a deliberate registry exclusion recorded at `matrix.go:90-94` as
reliability/data-integrity rather than security scope, unchanged by this fire and not a new finding.
(b) The per-identity edge grant lists only the v1 ack form (`internal/gateway/natsauth/natsauth.go:371`);
safe today (allow-side, and the server stamps v1 while `js_ack_fc_v2` is off) but it would break edge sync
if that flag were ever enabled — noted here, not filed: it is a latent vendor-flag coupling with no consumer
and no defect today. (c) A scout reported the 2026-08-20 backlog audit's `83891a8c` citation unresolvable;
it resolves fine (`fix(natsperm): close the core-events JS.API side channel for six security-plane
consumers`). The clone this fire ran in was **shallow** — 76 commits — so every history-based negative in it
was an artifact until `--deepen`. Recorded as an environment rule in `agents/steward/REMOTE.md` §7 rather
than left as a one-off correction here.

**7 · Non-goals.** No `ReadBuckets` / declared-read-scope mechanism (that is the awaiting-Andrew fork). No
narrowing of `$JS.API.>` for anyone. No change to `SubscribeAllow` for any component. No change to the
protected-consumer registry's *membership*. No ack scoping for the platform tier (§3.3).

**Scope-diff gate:** every touch above traces to the scope sentence — the registry edit to *"deny registry
covers neither"*, the app-tier edit to *"reads any PULL consumer on any stream"* narrowed to the one tier
that provably needs no ack, the conf regeneration to the generated-artifact rule. Nothing widens; no adjacent
mechanism is substituted. The design's one census (§4) was re-run live before the first edit.

## 7. Build note
