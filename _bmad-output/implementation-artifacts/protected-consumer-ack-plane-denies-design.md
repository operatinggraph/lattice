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

**3.3 What this does NOT close, stated plainly.**

**First, and larger than this design: every deny in the matrix is defeasible for a component carrying
`AllowResponses`.** The cold bypass review found it and I reproduced it independently. nats-server checks a
static deny first and then, *only if the subject was denied*, consults the client's dynamic response
permissions (`server/client.go:4126-4141`); a message delivered to such a client registers its reply subject
as a response permission **precisely when the client is denied on it** (`:3881-3884`); and a publisher's
chosen reply subject is never permission-checked (`:4278-4295`). So a component with `AllowResponses` and a
wildcard subscribe grants itself publish on any denied subject by receiving a message whose reply *is* that
subject. Verified against the committed conf with the real dev seeds: `loom`, denied `$KV.core-kv.>`, takes a
`PubAck={"stream":"KV_core-kv","seq":1}` and the forged value reads back — the transport half of P2, which
`matrix.go` calls the load-bearing invariant.

This is **pre-existing** and not caused by anything here, but it bounds what this design may claim. Filed as
[its own board row](../planning-artifacts/backlog/lattice.md), because expressing reply authority without
`AllowResponses` spans the control plane, the auth-callout plane and the micro-service plane at once and has
no ratified pattern here to extend.

**Amended 2026-08-22, post-build.** As first written this section went on to call the ack denies "advisory"
for the six components carrying the flag. That is **false for the ack family specifically**, and the cold
bypass review found it by probing rather than reading: a *client* publish whose reply is prefixed `$JS.ACK.`
is rejected by `isReservedReply` before any permission logic runs (`server/client.go:4218-4224`,
`:4305-4307`), so the self-service route cannot reach an ack subject at all. Probed against the committed
conf: `loom` takes the `$KV.core-kv.>` grant this way and is refused outright on the ack subject. What
remains for those six is narrower and materially different to design against — a reply arriving from the
*server*, on a real delivery to the legitimate puller, seen through a `>` subscribe, yielding one bounded
registration. The front door (`MSG.NEXT`) is genuinely open to the six, which the review also confirmed by
probe — that half stands.

**Amended again 2026-08-22 by §8, which supersedes this section's scoping.** Two claims above are wrong and
are corrected there, not here, so read §8 as the current statement. (i) *"every deny in the matrix is
defeasible for a component carrying `AllowResponses`"* under-scopes it: every deny is defeasible for **every**
component, because the governing publisher is the server's own internal client (`perms == nil`, and the
reserved-reply guard is gated on `c.kind == CLIENT`), which no grant a row carries can take away. (ii) the
sentence this amendment originally ended on — that the ack denies are *an unconditional guarantee for the
twelve and defence-in-depth for the six*, a phrasing `matrix.go` also carried — is false: a stream
`RePublish` destination lands on an ack subject with no reply and no `CLIENT` kind, and cold review probed it
forging an ack on a protected consumer. `matrix.go`'s block is rewritten accordingly and no longer carries
the "KNOWN LIMIT" heading this text pointed at.

**Second, the read primitive at large.** `$JS.ACK.>` remains an unscoped read primitive against
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

The listing path is the one that has to be traced rather than assumed. `substrate.KVListKeys` holds a
`jetstream.KeyValue` (`internal/substrate/conn.go:502`), and that package's watcher builds its consumer
through the **legacy** push-subscribe API — `nats.OrderedConsumer()` passed as a `nats.SubOpt`
(`nats.go@v1.52.0/jetstream/kv.go:1304-1305`) — which forces `FlowControl: true` and
`AckPolicy: AckNonePolicy` (`js.go:1780-1781`). By G8 the server subscribes no ack subject for such a
consumer, so there is nothing for the app to publish to; what the listing actually depends on is
`$JS.FC.>`, which every component keeps. (`jetstream/ordered.go:634` sets `AckNonePolicy` for the *new*
ordered-consumer type — the right citation for `cmd/loupe/events.go:157`, the wrong one for this tier, and
naming it here would have pinned a line the app tier never executes.)

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

**Fire 1 — the whole design — shipped 2026-08-22.** Built across two runs: a first that landed Inc 1–3 on a
branch and died before gating or review, and a second that took the item over (the branch had been idle three
hours with the work unmerged), ran the gates, ran the review, fixed what it found and merged.

Built as specified, with one addition the build made and the design did not anticipate: `LEADER.STEPDOWN`
joins both `protectedStreamDenies` and `coreEventsAdminDenies`. It is the clustered analogue of the `PAUSE`
verb already denied there — inert at 503 on a single server, invoked by nothing in this repo, and a live
stall primitive the day JetStream is clustered. Closing it now costs nothing; discovering it later costs a
fire. The regenerated conf's diff decomposes exactly and only into the three intended shapes: 96 ack denies
(6 protected consumers × 16 non-owner components × 2 wire forms, halved per consumer by the owner
exclusion), 102 consumer + 13×17 stream `LEADER.STEPDOWN` denies, and the four `$JS.ACK.>` removals.

**Review — three cold passes (bypass · test-oracle · vendor-citation), none by the implementer.** No blocking
findings; the deny is complete in both wire forms, the owner exclusion is real, and the app tier's removal was
re-derived independently rather than taken from §4. The vendor pass verified all 14 citations against the
pinned `nats-server` v2.14.0 and `nats.go` v1.52.0 and found none wrong.

One MAJOR, and it was in a security comment rather than in the code. §3.3 and `matrix.go`'s KNOWN LIMIT both
claimed the `AllowResponses` reply-registration bypass made the ack denies advisory for six components. It
does not: `isReservedReply` refuses a client publish carrying a `$JS.ACK.` reply before any permission logic
runs. The review established this by probe, not by reading — the same probe that confirmed the bypass IS real
for `$KV.*` and for `MSG.NEXT`. Both texts are corrected in place (§3.3 carries the amendment); the failure
mode it prevents is a later reader dropping a deny they were told was worthless.

Four minors fixed in the same round: the ack rationale led with confidentiality, which this matrix does not
defend (`MSG.GET`/`DIRECT.GET` stay open on core-events by design) — rewritten to lead with control of the
durable, which is what the deny actually buys, and to note `+NXT` is pull-only; `TestAckGrantRoster` compared
the grant by exact string, so `$JS.ACK.*.>` would have reported not-granted and passed — now a prefix test;
both new `LEADER.STEPDOWN` denial loops asserted refusal with no positive control, breaking the file's own
idiom, so bootstrap now proves each endpoint answers at all; and `model-runner`'s roster entry read as
containment when it carries `AllowResponses`.

Three findings deliberately not acted on, each with its reason. The substrate dossier's "model of a retired
entry" illustration was dropped to stay at the cap of 12 — correct: its gate (`lint-conventions` blocking a
bare `nats.Connect` in tests) has landed, which is exactly when the dossier's contract says an entry retires.
`verticalAppNames` stays hardcoded because `Matrix` carries no *is a vertical app* field, and the roster's
exhaustiveness clause already forces a new component to declare its side. And the read-path regression writes
one key, so it cannot trip the flow-control stall its own rationale names — true, and out of this item's
scope: it belongs with the read-scope design's `TestAppTierRevocationCheckStillWorks`.

**Gates.** `go build ./...`, `make vet`, `golangci-lint run ./...` (0 issues), all 10 `STRICT=1 lint-*.go`,
`lint-package-version` with `DIFF_BASE`, and `go test ./... -p 4` with `POSTGRES_TEST_DSN` set against a
native Postgres at CI parity (REMOTE.md §3 — a remote suite without it is falsely green). CI green on the
merge.

---

## 8. The reply-subject write primitive needs no `AllowResponses` — grounding, 2026-08-22

**Status of this section: 🔭 GROUNDED FINDING, unbuilt.** Lattice Steward fire 2026-08-22 (unattended,
remote), selected as the board's *[natsperm] A consumer's `DeliverSubject` is unchecked* row, whose stated
first task was to ground the Processor arm. The grounding refuted the row's mechanism and found a sharper
one, which belongs here because it is the same root cause §3.3 names: **reply subjects carry write authority
the matrix does not model.**

### 8.1 What was probed, and what each probe returned

Every result below is a live probe against the committed `deploy/nats-server.conf` with the real dev seeds
(`startServerFromConf`), connecting as `clinic-app` — a component that carries **no `AllowResponses`**
(`matrix.go`'s app rows) and is therefore inside the population §3.3 called an *unconditional guarantee*.

| Route | Mechanism | Result |
|---|---|---|
| Direct publish to `ops.default` | plain JetStream publish | **DENIED** — the #75 Fire 2b grant is genuinely absent |
| Push consumer, `DeliverSubject: ops.default` | `$JS.API.CONSUMER.CREATE.KV_health-kv.<n>`, raw request | **CONTAINED** — see §8.2 |
| Pull consumer, `MSG.NEXT` reply `ops.default`, message present | `$JS.API.CONSUMER.MSG.NEXT.…` | **CONTAINED** — same mechanism, data path only |
| Pull consumer, `MSG.NEXT` reply `ops.default`, nothing to deliver | as above | **LANDS** — the STATUS/408 frame is an ordinary server publish |
| `DIRECT.GET`, subject-token form, reply `ops.default` | `$JS.API.DIRECT.GET.KV_health-kv.$KV.health-kv.<k>` | **LANDS** on `core-operations` |
| `DIRECT.GET`, bare form, reply `ops.default` | `$JS.API.DIRECT.GET.KV_health-kv` + `last_by_subj` | **LANDS** on `core-operations` |
| `DIRECT.GET`, bare form, reply `$KV.core-kv.<key>` | as above | **LANDS — arbitrary Core KV value written** |
| `DIRECT.GET`, bare form, reply `$KV.capability-kv.<key>` | as above | **LANDS — arbitrary auth-plane value written** |
| Any other `$JS.API.*` verb, reply `ops.default` | e.g. `CONSUMER.CREATE` | **LANDS** — server-chosen bytes; the class is not DIRECT.GET-specific |
| PubAck reply, as **facet** (no `$JS.API.>`) | publish `$KV.health-kv.<k>` with reply `$KV.core-kv.<key>` | **LANDS** — PubAck JSON; corruption, not forgery |
| Stream `RePublish` dest `ops.default` | `STREAM.CREATE` a mirror of `KV_health-kv` | **LANDS — attacker-chosen bytes, no reply subject** |
| Stream `RePublish` dest `$JS.ACK.core-events.<consumer>.…` | as above | **LANDS — forges an ack, clears a protected consumer's pending delivery** |

The attacker controls the bytes by first writing them to its own `health-kv` key (`SharedWrite`, granted to
every non-bootstrap component by `Allow()`), then naming a protected subject as the *reply* of a DIRECT.GET
for that key. The server's response carries the stored bytes verbatim.

### 8.2 Why the consumer routes are contained — and why that is not a permission check

A push or pull delivery is re-subjected at the routing layer only: the message the subscriber receives keeps
its **original** subject (`$KV.health-kv.…`), with the deliver subject used for routing and the ack subject as
its reply. Probed: with the app subscribed to `ops.default`, its own subscriber **did** receive the delivered
message — so the vector reached the mechanism — while `core-operations` captured **nothing**, because a
stream's capture predicate reads the message's subject, which was never `ops.default`.

**Corrected by cold review 2026-08-22: "captured nothing" was measured through a lane-filtered consumer and
is too strong.** The capturing stream's subscription *does* fire — the sublist match is on the deliver
subject — and `core-operations` *does* store the message, but under its **original** subject
(`server/client.go:5088-5092`, `server/stream.go:8058-8060`). Probed: `State.Msgs=1`,
`subjects=map[$KV.health-kv.…:1]`. What that costs is stream capacity, not authority: the Processor's
consumers filter `ops.<lane>` (`internal/processor/step1_consume.go:47`), so no forged operation is ever
dispatched. The residual is real but different in kind — any component can inflate `core-operations`, which
against a `DiscardOld` limit is an eviction primitive on pending operations.

This matters for how the class is designed against: `DeliverSubject` is *not* an authority primitive, and a
narrowing aimed at `CONSUMER.CREATE` would buy nothing against the routes that are. One exception belongs
here rather than in the "contained" column: a pull consumer's **status/timeout frame** is an ordinary server
publish and does reach the reply subject, so `MSG.NEXT` is contained only on its data path.

### 8.3 Why `AllowResponses` is irrelevant here

§3.3's route needs the flag, a wildcard subscribe, and an inbound delivery, because it works by the *client*
accruing a dynamic response permission. This route needs none of them: for a `$JS.API.*` request the
responder is the server's **internal JetStream client**, created with `perms == nil`
(`nats-server@v2.14.0 server/client.go:4280-4287` — the permission branch is gated on `c.perms != nil`), so
its publish to the requester-named reply subject is never permission-checked. `isReservedReply`
(`server/client.go:4215-4226`) rejects only service replies, `$JS.ACK.` and the gateway prefix; `$KV.…` and
`ops.…` are ordinary subjects.

**Consequence — the correction this section exists to make.** §3.3's closing sentence and `matrix.go`'s
KNOWN LIMIT both say the denies are *an unconditional guarantee for the twelve without the flag and
defence-in-depth for the six that carry it*. That is **false for every deny outside the `$JS.ACK.` family**:
every component can self-serve a write on any subject the matrix denies it, with one request and no flag.
The `$JS.ACK.` family is **not** an exception either, via the `RePublish` route below. Both texts are
corrected in place by this fire — `matrix.go`'s block is rewritten and no longer carries the "KNOWN LIMIT"
heading, and §3.3 above carries a second amendment pointing here.

**Corrected by two independent cold reviews 2026-08-22 — the draft of this section named the wrong
precondition, and the correction is the same defect class it was written to fix.** `$JS.API.>` is not what
gates the route. *Any* subject whose responder is a server-internal client answers to a caller-named reply,
and an ordinary JetStream **PubAck** is exactly that. `facet` — the narrowest row in the matrix, holding no
`$JS.API.>` — publishes `$KV.health-kv.<k>` with reply `$KV.core-kv.<key>` and the PubAck JSON lands on the
denied key; probed, with facet's own direct `KVPut` to core-kv denied on the same connection as the positive
control. So **no component here is contained**, and the population is all eighteen, not seventeen.

What `$JS.API.>` actually buys is **content control**, not the write: without it a component is limited to
server-chosen bytes (a PubAck, an API response) — corruption — while a DIRECT.GET holder chooses the bytes
and can forge a well-formed envelope or capability document. That distinction is what §8.4's remedy
discussion turns on, and the draft conflated it with containment.

There is also a route that needs no reply subject at all: a stream's **`RePublish` destination**. It is
emitted by the internal client with the destination as the message's *own* subject and `c.kind == JETSTREAM`,
so neither the permission branch nor `isReservedReply` applies — and the bytes are attacker-chosen (the
mirrored value). Cold review probed it landing a chosen envelope on `ops.default`, and — the sharper result —
landing on a literal `$JS.ACK.core-events.<consumer>.…` subject to **forge an ack**, clearing a protected
consumer's pending delivery, with a harmless destination as the negative control. The ack arm carries one
constraint worth stating precisely, found by the builder rather than assumed: `processAck` treats only a
zero-length body, `+ACK` or `+OK` as a plain ack (`nats-server@v2.14.0 server/consumer.go:2731`), so the
republished value has to be one of those — arbitrary bytes are silently dropped. That narrows the vector,
it does not remove it: the attacker writes the source key empty. How the ack subject is obtained is the
other half, and it is not a barrier either: `<accHash>` is a deterministic constant anyone can compute
offline (§2, G6), and the sequence tokens come off the wire — every component's `subscribe { allow: [">"] }`
makes real deliveries, and therefore their ack subjects, directly observable. The regression test captures
one from the owner's own delivery, which is fixture convenience, not the attacker's constraint. That is the outcome
`coreEventsAckDenies` exists to prevent: silent suppression of a pending crypto-shred, revocation or
credential-binding event. No subject deny can reach it, because a mirror's source is in the request **body**,
so `STREAM.CREATE.<attacker-chosen-name>` is always subject-allowed.

### 8.4 Why no narrowing of this matrix closes it

Recorded so a later fire does not re-derive it:

- **Not a `CONSUMER.CREATE` deny** — §8.2: the consumer routes are already contained.
- **Not a `DIRECT.GET` deny on the writable bucket.** It would remove *content control* only, and it is not
  free: nats.go sets `AllowDirect: true` on every KV bucket it creates (`nats.go@v1.52.0 jetstream/kv.go:684`)
  and on the object store (`object.go:589`), so `KVGet` reads DIRECT.GET — and all four apps read `health-kv`
  through `projectionhealth.Check()`. A denied publish is fire-and-forget, so they would stall, not fail.
- **Not content control at all, for the corruption case.** The response of *any* request/reply API — a
  `STREAM.INFO`, a JSON API error — is bytes published to the named reply subject. Removing raw-bytes reads
  downgrades forgery of a *valid* envelope or capability document to corruption with junk; it does not
  remove the write.
- **Not a server option.** nats-server 2.14 has no setting binding a reply subject to the requester's publish
  permissions; the only reply subjects it reserves are the three in §8.3.

- **Not a `STREAM.CREATE` subject deny either, as currently shaped.** `protectedStreamDenies` scopes its
  denies to *registered* stream names; the `RePublish` route creates a stream under a name of the attacker's
  choosing and names its mirror source in the request **body**. Denying `$JS.API.STREAM.CREATE.>` matrix-wide
  outside bootstrap is the one narrowing that would bite this route, and it is not free — it needs a census
  of every runtime stream/bucket creation (package install's read-model targets are the obvious consumer).
  That census is filed as its own row rather than guessed at here.

**The candidate remedy, named so the decision is one look, not a research project:** NATS's own answer to
cross-tenant write authority is **account isolation** — the protected buckets in an account that exports a
controlled surface, rather than the single global account `$G` this deployment uses (`G6` in §2 records that
the conf declares no `accounts` block). That reshapes the platform's trust topology, so the fork is Andrew's.
Note what removing `$JS.API.>` from the app tier (the shelved read-scope design) does and does not buy, since
the draft of this section got it wrong: it removes the tier's **content control**, downgrading forgery to
corruption — it does **not** narrow the population that can write, because the PubAck route survives on the
`$KV.health-kv.>` grant every row keeps.

### 8.5 Fire brief (build note, 2026-08-22)

1. **Scope sentence.** Ground the filed `DeliverSubject` row against the running server; correct every
   security claim the grounding falsifies, in place; pin all four outcomes — the two contained routes and the
   two live ones — as live tests against the committed conf. **No matrix narrowing** (§8.4: none is sound).
2. **Verified touch-list** (line numbers as of `bf1b9f7`, before this fire's own commits shifted them —
   locate by symbol, not by line). `internal/natsperm/matrix.go` (`Allow`/`Deny`'s doc comment + the four
   app rows' `Desc`), `doc.go`, `conf_test.go` (the three write-isolation/ops-denial doc comments); new
   `internal/natsperm/replysubject_test.go`; this doc (§3.3 + §8);
   `app-tier-transport-read-scope-design.md` §14/F2 and its restatements;
   `nats-account-write-restriction-design.md` (the vertical-apps grant row and the "invariant that does all
   the work" paragraph); `docs/components/{substrate,gateway,control-plane}.md`;
   `_bmad-output/planning-artifacts/backlog/lattice.md`.
3. **Precedents to mirror.** Test fixtures + idiom: `conf_test.go:60-181` (`startServerFromConf`,
   `connectAs`, `provision`, `provisionStream`, and the positive-control-beside-every-denial shape).
   Roster-style premise pinning: `TestAckGrantRoster`. Deny wire-form completeness: `bridge`'s
   `ExtraPubDeny` (`matrix.go:492-497`), which already carries both DIRECT.GET forms and says why the bare
   one is required alongside the wildcard.
4. **Increment order + green checks.** Inc 1 tests → `go test ./internal/natsperm/...`. Inc 2 claim
   corrections → `STRICT=1 go run ./scripts/lint-conventions.go`. Inc 3 board + dossier →
   `go run scripts/lint-board.go` (reads its output; it exits 0 on FAIL). Whole: `go build ./...`,
   `make vet`, `golangci-lint run ./...`, `go test ./... -p 4` with `POSTGRES_TEST_DSN`.
5. **In-scope gotchas.** The substrate dossier's *permission edit complete in BOTH directions* entry — both
   DIRECT.GET wire forms are live and each is probed separately here. Its *vendor-behaviour claim needs a
   pinned `file:line` on the path this code executes* entry, whose third sighting was a citation that was
   real, on-path and still carried a false conclusion: that is exactly what §8.3 corrects, so every vendor
   line above was read in `v2.14.0`, not inferred. Standing checklist #3 — the contained routes carry a
   positive control (§8.2) so they cannot pass vacuously. Standing checklist #2 — nats.go's `AllowDirect`
   default was re-derived from the vendor source after a census answered it from the Lattice config struct
   and got it backwards.
6. **Adjacent finds.** (a) `nats-account-write-restriction-design.md`'s grant table lists `core-operations`
   for the vertical apps, which the code contradicts. (b) The false-scope-claim class recurs in this package.
   (c) Cold review found bridge's core-KV read-side denies defeated by the same mirror trick, so
   `matrix.go`'s "closes the CORE-KV read side channel specifically" is false — pre-existing, and it is
   further evidence for §8.4. Each of these is either corrected in this fire's commits or carries a board
   row; §8.6 is the accounting, and nothing here claims completion on its own.
7. **Non-goals.** Any change to `Allow`/`Deny` output, the shelved read-scope design, account isolation, and
   any Processor-side envelope authentication — §8.4 shows the first three do not close the class and the
   last is a Contract #2 posture change.

### 8.6 Accounting — every discovery, and where it went

Cold review (three lenses, none the implementer) returned four blocking findings, three of them against
**this section's own first draft**. The audit lens also produced a ledger of texts elsewhere in the repo
still asserting what §8 falsifies. Nothing below is filed under a "residual" label; each line is either
fixed in this fire's commits or carries a row with one of the two outs stated.

**Fixed in this fire.**

| Discovery | Where it landed |
|---|---|
| `facet` is not contained; `$JS.API.>` is the wrong precondition | §8.3 rewritten; `matrix.go`'s block rewritten; pinned by a facet PubAck vector |
| The `$JS.ACK.*` family is reachable via `RePublish` (ack forgery on a protected consumer) | §8.3 + §8.1 table; `matrix.go`; pinned with its negative control |
| "a stream capturing the deliver subject never ingests it" — it ingests it, under the original subject | §8.2 + `matrix.go`; the test now pins both halves |
| Pull `MSG.NEXT` is contained on its data path only — the status frame lands | §8.1 table + §8.2; own subtest |
| The class is not DIRECT.GET-specific (any `$JS.API.*` verb answers to the reply) | §8.1 table; a non-DIRECT.GET subtest |
| `isReservedReply` runs *after* the publish-permission check, not before | `matrix.go` (inherited unexamined from §3.3) |
| §3.3's `AllowResponses` scoping, and its pointer to a "KNOWN LIMIT" heading that no longer exists | §3.3's second amendment |
| The negative assertion passed vacuously (`msgs.Error()` unchecked, observer liveness unproven) | the test's observer now carries a positive control |
| The roster pin used exact-string equality on raw `ExtraPubAllow`, with sole-ness unpinned | prefix test over the derived grant list + two-way roster |
| "a compromised app cannot forge an `env.Actor`" surviving in `conf_test.go` after being corrected in four other places | `conf_test.go` |
| `doc.go`, `TestCoreKVWriteIsolation`, `TestCapabilityKVWriteIsolation`, the ack-plane PASS comment — all claiming an invariant this package now disproves | each scoped to "the ordinary client path" |
| `docs/components/gateway.md`'s "no actor can fabricate a Core-KV write", `control-plane.md`'s "cannot reach the planes at the transport layer" | both scoped |
| `nats-account-write-restriction-design.md`'s vertical-apps grant row + its "invariant that does all the work" | corrected in place |
| `app-tier-transport-read-scope-design.md` §14/F2 and its restatements — the refuted `DeliverSubject` mechanism | disposition rewritten |
| The false-scope-claim class, fourth sighting | `docs/components/substrate.md` dossier entry extended (at the cap of 12, so extended rather than added) |

**Carries a row, with its out stated.**

| Discovery | Out |
|---|---|
| Closing the reply/RePublish write authority at all | **Needs Andrew** — account isolation is a trust-topology fork (§8.4). Folded into the existing ★★★ reply-authority row, whose scope this fire corrects. |
| Denying `$JS.API.STREAM.CREATE.>` outside bootstrap — the one narrowing that bites the `RePublish` route | **Steward `📋`** — a precedent exists (`protectedStreamDenies`); what it needs is the runtime stream/bucket-creation census, not a new pattern. Own row. |
| Bridge's core-KV read-side denies defeated by the same mirror trick (`matrix.go`'s "closes the CORE-KV read side channel specifically") | Same row as above — one root cause, one row, per the consolidation gate. |
| `core-operations` inflation via consumer delivery (eviction pressure against a `DiscardOld` limit) | Same row: it is the same missing authority, and no separate mechanism closes it. |
