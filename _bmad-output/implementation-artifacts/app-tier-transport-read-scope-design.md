# The app tier's transport read side is unrestricted — a declared read scope for the NATS permission matrix

**Status: 🗄️ SHELVED (Andrew, 2026-08-22).** Not built. The exposure is real and vendor-verified, but it
requires a **compromised app-tier binary** to read beyond its need, and Andrew's posture is that the
vertical-app binaries are **trusted infra** — a breached app process is out of the current threat model, so
a read-side narrowing is defense-in-depth against a game-over breach rather than a boundary the platform
enforces today. Refuse/simplify call in the 2026-08-22 ratify session. No contract edit was staged, so
nothing reverts.

> **Revive trigger:** the app-tier NATS account stops being trusted infra — e.g. third-party / tenant /
> untrusted app code sharing an app-tier NKey, or a compliance requirement that PHI/PII on the `ops.>`
> lane not be readable by an app process. At that point this finishes the read side of the same boundary
> `#75 Fire 2b` started on the publish side.

**Related rows.** F1 (`$JS.ACK.>` cross-stream read) shipped independently as a parallel steward fire
(`9c24c918`, ack-plane read primitive CLOSED). F2 (`DeliverSubject` unchecked) stays a standalone `📋` —
it tests the **publish**-side Fire-2b integrity boundary the platform *does* enforce, gated on the cheap
Processor-arm verification (if the Processor rejects the forged envelope at step-1/step-3, F2 is moot;
else it is a real hole in a ratified boundary and returns to Andrew) — independent of this read-side shelve.

*Original design retained below for the record — do not build it.*

**Status (original): 📐 awaiting-Andrew (ratification).** · Designer fire 2026-08-21 (Winston, unattended).
**Adversarial pass: RUN (2026-08-21), two independent lenses — security-soundness and citation/census audit.
They returned 5 blockers, 4 majors and several minors, and they RESHAPED this design**: the headline exposure
moved from the at-rest plane to the in-flight one (§For-Andrew ¶2), the mechanism gained a per-bucket mode and
lost `$JS.ACK.>`, and three buckets the first draft's census missed would have taken all four apps down. Every
blocker was re-verified against the pinned source before folding. Findings + dispositions: §14.
**Lane:** Lattice (Stream 2). **Board row (rewritten by this design):** *[natsperm] The `capability-author-context`
catalog bucket is app-tier readable* → *[natsperm] The app tier's read side is unrestricted*.
**Filed by:** [natural-language-weaver-targets-design.md](natural-language-weaver-targets-design.md) §For-Andrew,
after NL-1's context-lens widening projected lens `.spec` bodies into `capability-author-context`.
**Contracts:** none changed. The permission matrix (`internal/natsperm`, `deploy/nats-server.conf`,
`deploy/gen-dev-nkeys`) is component/deploy surface, not a frozen contract — the same classification the two
ratified matrix designs used.
**Builds on (all shipped + ratified):** [nats-account-write-restriction-design.md](nats-account-write-restriction-design.md)
(the per-component NKey users), [natsperm-platform-bucket-isolation-design.md](natsperm-platform-bucket-isolation-design.md)
(the platform-bucket registry + derived denies, Fork A), [facet-host-health-emission-design.md](facet-host-health-emission-design.md)
§4.1/§4.2 (the `SubscribeAllow` field and the first component with **no** `$JS.API.>` grant — the precedent this
design generalizes), [per-identity-nats-subscribe-acl-design.md](per-identity-nats-subscribe-acl-design.md)
(auth-callout, external connections — orthogonal, see §8).

---

## For Andrew (ratify in one look)

**What it does, in two lines.** Lattice's NATS matrix is by construction a *write*-only restriction — its own
doc comment says *"gates writes only; reads are unrestricted"* (`internal/natsperm/matrix.go:48`) — and every
component but one holds a blanket `$JS.API.>` publish grant. A JetStream KV **read** is a publish to
`$JS.API.DIRECT.GET.*` / `STREAM.MSG.GET.*`, so that blanket grant is a read grant on **every bucket in the
deployment**. This design adds a **declared read scope** to the matrix — a per-component enumerated read set
that replaces the blanket grant, default-deny, mirroring the already-ratified `SubscribeAllow` field — **and
a narrowed subscribe side, which is the half that closes the worse exposure (¶2)**. Applied first to the four
vertical apps.

**The sharpest exposure is IN FLIGHT, not at rest — and it is live today.** Every component, the four
vertical apps included, renders `subscribe { allow: [">"] }` (`deploy/nats-server.conf:210` and siblings). That
is not "may read every bucket"; it is **may receive every message in the account**. Including `ops.>` — and the
Processor encrypts sensitive mutations at **step 6.5**, inside the commit path (`internal/processor/commit_path.go:374-386`),
*after* the operation envelope has already crossed `ops.>`. So the in-flight plane carries **plaintext**
sensitive data (Contract #3's at-rest guarantee bounds the KV plane and says nothing about this one), alongside
`lattice.vault.decrypt`/`decryptref` request-reply traffic, which is plaintext PII by construction. Narrowing
the app tier's *subscribe* side is therefore the primary win of this design; the read-scope allow-list is the
other half of the same door.

**The at-rest half — the row as filed, generalized.** A JetStream KV read is a publish, so the blanket grant is
a read grant on **every bucket**: all of Core KV (`$JS.API.DIRECT.GET.KV_core-kv.>`; the only read-side deny in
the whole matrix is the Bridge's, `matrix.go:379-387`), `capability-kv`, `privacy-pii-key-envelopes`, and every
other vertical's read models. P5 — *"applications read lens projections, never Core KV"* — is enforced **only
by a source lint** (`scripts/lint-conventions.go:1044`), which is an authoring gate, not a boundary.
`capability-author-context`, the bucket the row named, turns out to be one of the milder items on that list.

**Severity, restated after the adversarial pass — sharper than the first draft claimed.** The first draft said
"blast radius, not a live escalation", reasoning that the at-rest exposure needs a compromised app binary. That
argument does not survive ¶2: the in-flight plane needs only the credential, and carries plaintext PII. It also
does not survive §3's corrected PHI row — `clinic-appointments` is a **nats-kv** lens projecting `reason`
(*"Visit reason / chief complaint"*, `packages/clinic-domain/ddls.go:700-707`) and `statusNote` against
`patientKey`, from an aspect its own DDL declares **non-sensitive**, so it is plaintext in Core KV and plaintext
in a bucket every app can read. The tier this is exposed to is the one the platform already decided not to
trust: #75 Fire 2b hardened its **write** side ("holds NO `ops.>` publish so a compromised app cannot forge an
`env.Actor`", `matrix.go:491`). The read side of the same threat model was never closed — and §14/F2 shows the
write side is not as closed as that comment claims either.

**The row's premise contains a false fork — the Loupe carve-out dissolves.** The row reads
`no-pattern: read-restricted package bucket + loupe-charter carve-out (Andrew)`. That framing assumes the
mechanism must restrict the **bucket** (a deny-side rule, which hits every reader — hence a carve-out for the
inspector). Restricting the **reader** needs no carve-out at all: Loupe declares `ReadUnrestricted: true` with
its charter as the reason, and nothing about it changes. There is no Loupe decision in this design. (It also
sidesteps the constraint that killed Fork B of the bucket-isolation design: package bucket names are
dynamically created and un-enumerable in a static conf — but a *reader's* read set is a short list of Go
consts in its own source, §4.3.)

**The one decision that is yours (posture + reach, §For-Andrew fork).** Increment 1 flips the matrix's stated
v1 stance from "reads unrestricted" to "reads declared" for **one tier** (4 rows), and makes every other row
state its posture explicitly. Whether to go further — Increment 2 narrows `model-runner` and `gateway`; the
seven platform daemons and Loupe stay unrestricted-by-declaration and are **not** proposed for narrowing — is
a security-posture-and-capacity call. My recommendation: **ratify Increment 1, ratify Increment 2 as
sequenced-not-urgent, and do not narrow the daemons** (§9 alt 5 prices why). I am flagging this rather than
self-adjudicating because it changes a posture two ratified designs recorded as a deliberate v1 residual, and
because the *declared read-scope* mechanism you shelved as disproportionate on 2026-08-06 shares a name with
this one — it does not share a subject (that was per-**actor** read authorization inside the Processor's write
path; this is per-**component** transport confinement). §8 states the difference explicitly so the shelving
precedent is not applied to the wrong thing by reflex.

**One finding worth landing regardless of how you rule.** Grounding this design against the pinned server
turned up that **a NATS permission violation is currently invisible platform-wide**: a denied publish never
reaches a responder, so the caller sees a plain timeout, and the server's `-ERR` frame is uncorrelated and
dropped because `substrate.Connect` registers no `nats.ErrorHandler` (§4.4, G18/G19). Every matrix narrowing
we have already shipped — the Bridge's core-kv read denies included — has been unobservable. Increment 1's
**step 0** is that ~10-line handler, and it stands on its own even under a "do nothing" fork.

**No frozen-contract edit is staged. No new runtime state, no new component, no new bucket** — a compile-time
table gains a field, and a generated config file gets shorter.

---

## 1. Problem & intent

`internal/natsperm.Matrix` gives every Lattice binary its own NKey user with a scoped **publish** allow-list.
Its stated model, restated in three places in the file, is that this "gates writes only; reads are
unrestricted" — the matrix's stance for trusted platform daemons (`matrix.go:48`, `:399`, `:426`).

That stance has a consequence the file does not state: **in JetStream, a read is a publish.** Getting a value
out of a KV bucket means publishing a request to `$JS.API.DIRECT.GET.<stream>` or
`$JS.API.STREAM.MSG.GET.<stream>`; listing keys means publishing a `$JS.API.CONSUMER.CREATE.<stream>.…`. So a
component holding `$JS.API.>` — which is 17 of the matrix's 18 rows — holds a **read grant over every stream
and every KV bucket in the deployment**, platform-private buckets and other verticals' read models included.

The single exception proves the rule: the Bridge carries three hand-authored read denies
(`$JS.API.DIRECT.GET.KV_core-kv`, `…KV_core-kv.>`, `$JS.API.STREAM.MSG.GET.KV_core-kv`, `matrix.go:379-387`),
added by the sensitive-param-egress fire because a decrypt-RPC holder reaching all of Core KV was judged
unacceptable *for that one component*. Nothing generalized the judgement.

**Intent.** Give the matrix a read side: a component declares what it reads, and gets exactly that. Start with
the tier whose trust boundary the platform has already drawn — the vertical apps.

## 2. Grounding ledger

Every load-bearing fact, pinned to the code that *does* the thing.

| # | Fact | Evidence |
|---|---|---|
| G1 | Publish permissions are an allow-list plus derived denies; subscribe renders `allow: [">"]` unless a row sets `SubscribeAllow` | `matrix.go:614-634` (`RenderConf`) |
| G2 | `Allow()` = `ExtraPubAllow` + universal `$JS.FC.>` + registry-derived `$KV.<owned>.>` | `matrix.go:194-204` |
| G3 | `Deny()` = `ExtraPubDeny` + non-owned `$KV.<b>.>` + `protectedStreamDenies` per bucket + core-events consumer denies | `matrix.go:224-254` |
| G4 | `protectedStreamDenies` deliberately denies **write-shaped** verbs only — *"Ordinary reads (MSG.GET, DIRECT.GET, INFO) and consumer ops stay allowed"* | `matrix.go:68-69` (the quote), `:70-79` (the verb list) |
| G5 | The four vertical apps' entire publish grant is `["$JS.API.>"]` (+ `$O.core-objects.>` and the two vault RPCs for loftspace). **Amended 2026-08-22:** as first written this row also listed `$JS.ACK.>`, which the F1 fire has since removed from all four (`7e24913`) — so Increment 1 no longer has to strip it, and §4.2's "no read-scoped row carries `$JS.ACK.>`" is already true for this tier | `matrix.go:556-590` |
| G6 | No app sets `SubscribeAllow`; all four render `subscribe { allow: [">"] }` | `deploy/nats-server.conf:210` (clinic-app) and siblings |
| G7 | `facet` already ships with **no** `$JS.API.>` and `SubscribeAllow: ["_INBOX.>"]` — "the narrowest user in the matrix, by design" | `matrix.go:521-531` |
| G8 | A KV `Get` publishes `$JS.API.DIRECT.GET.<stream>.<$KV.b.key>`; a get-by-revision publishes the **bare** `$JS.API.DIRECT.GET.<stream>`; the non-direct fallback is `$JS.API.STREAM.MSG.GET.<stream>` | pinned `nats.go@v1.52.0` `jetstream/stream.go:575-596`, `jetstream/api.go:96-101` |
| G9 | Opening a KV handle is a `$JS.API.STREAM.INFO.<stream>` | `jetstream/api.go:81-82`, `jetstream/jetstream.go:808`; corroborated in-repo by `matrix.go:417-419` ("opening the model-results KV handle is a STREAM.INFO") |
| G10 | `ListKeys`/`Watch` create a consumer at `CONSUMER.CREATE.<stream>.<name>` or `…<name>.<filterSubject>` — the filter is multi-token, so `CONSUMER.CREATE.KV_<b>.>` covers both | `jetstream/consumer.go:319-321`, `jetstream/api.go:50-55` |
| G11 | `substrate.KVGet`/`KVListKeys`/`KVListKeysPrefix` all open the handle first, then `Get` / `ListKeys` / `ListKeysFiltered` | `internal/substrate/kv.go:37-45`, `:195-207`, `:234-243` |
| G12 | The only read-side deny in the whole matrix is the Bridge's three core-kv subjects | `matrix.go:379-387` |
| G13 | P5's read-path invariant is enforced by a source lint over `cmd/<app>` files, with a platform allow-list | `scripts/lint-conventions.go:262`, `:702-712`, `:1044` |
| G14 | Sensitive aspect bodies are ciphertext in Core KV — *"Plaintext sensitive `data` never lands in Core KV"* | `docs/contracts/03-mutation-batch-event-list.md:170-171` |
| G15 | The rendered conf is live: the dev stack mounts it and NATS is started with `-c /etc/nats/nats-server.conf` | `docker-compose.yml:18-20` |
| G16 | The app tier's **write** side was deliberately hardened on this threat model: "holds NO core-operations (`ops.>`) publish so a compromised app cannot forge an `env.Actor` (#75 Fire 2b)" | `matrix.go:491`, `:507`, `:512`, `:517` (the `Desc` lines) |
| ~~G17~~ | **WITHDRAWN (§14/F1).** The first draft cited `matrix.go:180-192`'s claim that `$JS.FC.>` and `$JS.ACK.>` are "consumer protocol plumbing … not a data-plane privilege". It holds for `$JS.FC.>` (empty acks, returns no data) and is **false for `$JS.ACK.>`**, which carries the `+NXT` pull primitive. The row is kept struck rather than deleted: it is the exact shape of citing a comment as evidence for behavior, and the shipped comment it quotes is itself wrong | struck; see F1's citations |
| G18 | A denied publish is refused **before** the message reaches the sublist, so the JS API responder never runs and no reply is produced: the caller sees only its own context deadline (`ErrTimeout`), while the server emits an uncorrelated connection-level `-ERR Permissions Violation for Publish to …` | pinned `nats-server@v2.14.0` `server/client.go:4280-4286`, `:5623-5629`; `nats.go@v1.52.0` `nats.go:4196-4197`, `:3921-3943` (the `permissionsRe` correlation regex matches **subscribe** violations only; a publish violation reaches `AsyncErrorCB` with `sub == nil`) |
| G19 | `substrate.Connect` registers **no** `nats.ErrorHandler` — its option list is `Timeout/Name/MaxReconnects/ReconnectWait/NKey/UserCredentials` plus a `SetDisconnectErrHandler` — so nats.go's `AsyncErrorCB` is nil and that `-ERR` is dropped on the floor platform-wide | `internal/substrate/conn.go:156-189`, `:214-226` |
| G20 | A plain core subscribe on `$KV.<b>.>` receives every KV write live — a stream captures messages via an ordinary internal sublist subscription, and core delivery is fan-out — so a publish-side deny does nothing against it; `subscribe { deny: [...] }` is parsed and enforced symmetrically with publish; `RePublish` is configured nowhere in this repo | `server/stream.go:4804-4812`, `:5043-5054`; `server/opts.go:4796-4802`, `:4960-4968`; `server/client.go:3282-3289`; `grep -rn RePublish --include='*.go' internal cmd` → empty |
| G21 | An ordered push consumer's deliver subject is `nc.NewInbox()` (default prefix `_INBOX.`), including after an ordered reset | `nats.go@v1.52.0` `js.go:1853`, `:1892`, `:2229` |
| G22 | Deny beats allow, and `KV_B.>` does **not** match the bare `KV_B` — the sublist bails once the literal subject is exhausted before reaching the `>` token | `server/client.go:4104-4129`; `server/sublist.go:1501-1503`, `:1549-1554` |
| G23 | Decrypt-at-projection (Secure Lens) is **structurally** RLS-postgres-only — `validateSecureColumns` takes a `TargetPostgresConfig` and fails closed at spec-load on `!cfg.Protected`; `Protected`/`Public`/`SecureColumns` are postgres-target fields, and a postgres business model with no declared posture is refused | `internal/refractor/lens/corekv_source.go:1611-1616` (the refusal); `internal/pkgmgr/bucketguard.go:162-163`, `:205-206` (the install-time twin). Declaration site `internal/refractor/lens/schema.go:77-85` is cited for *where the fields live*, not as evidence of behavior |

## 3. What is actually exposed (and what contains it)

Census C3 (§6) ran against the mechanism, not the filed instance. Per exposed class, what an app-tier read
yields today and what bounds it:

| Reachable by any app today | What it yields | Containment |
|---|---|---|
| **`core-kv`** (the whole graph) | Every vertex/aspect/link envelope in plaintext — all four verticals' business data, identities, permissions, roles, the meta corpus | Sensitive aspect bodies are ciphertext (G14). Nothing else is. **Uncontained otherwise** — this is the headline. |
| **`capability-kv`** | Every actor's grant documents (`cap.<actor>`, `cap.roles.*`, `cap.svc.*`, `cap.ephemeral.*`) — `{operationType, scope, lanes, origin}` | Read-only: the authorization decision is the Processor's (`step3_auth_capability.go`), so this is reconnaissance, not privilege. |
| **`privacy-pii-key-envelopes`** | Per-identity `{wrappedDEK, keyId, kekVersion, alg, shredded}` (`packages/privacy-base/lenses.go` `piiKeyEnvelopeSpec`) | Wrapped material is inert without `lattice.vault.unwrapkey`, held only by `loupe` + `loftspace-app` (`matrix.go:461`, `:503`). For the other three apps: metadata disclosure (who has PII keys, kek version, shred state). |
| **Other verticals' read models** (`wellness-members`, `clinic-appointments`, `loftspace-lease-accounts`, `cafe-*`, `front-desk-*`, `my-tasks`, …) | Whatever each plain lens projects — identity keys, decisions, bookings, account bindings | **Partially uncontained — the first draft was wrong here (§14/F6).** Decrypt-at-projection genuinely cannot target a KV bucket (G23, three independent gates), so no *decrypted* PII lands here. But PHI that was never classed sensitive does: `clinic-appointments` (nats-kv) projects `reason` — the DDL's own words, *"Visit reason / chief complaint"* — plus `statusNote`, `documentedAt`, `followUpRequested`, against `patientKey`, off a `.schedule` aspect declared **non-sensitive** (`packages/clinic-domain/lenses.go:548-570`; `ddls.go:700-707`). `clinic-patients` in KV is opaque-key-only, which is what misled the first draft: it is the wrong bucket to reason from. |
| **`capability-author-context`** (the filed row) | Every installed meta vertex incl. `m.spec.data` — lens/weaverTarget/loomPattern bodies, RLS posture, `permittedCommands`, schemas | Reconnaissance. Strictly *narrower* than the `core-kv` read that is also available, since these specs are projections of meta vertices in Core KV. |
| **`capability-proposals`** | The full authored artifact `content`, the model's rationale, provenance | Same class; arguably richer than the filed row's bucket. |

**A re-identification chain exists across these buckets, all plaintext, all nats-kv** (found by the adversarial
pass, verified): `clinic-appointments` gives `reason` per `patientKey`; `clinic-ledger-history` gives `billedTo`
/ `memo` / `visitStartsAt` per `patientKey` (`packages/clinic-ledger/lenses.go:148-164`); `front-desk-visits`
joins an appointment to a `leaseAppKey` (`packages/front-desk/lenses.go:123-129`); `front-desk-lease-details`
maps that to a street address (`:101-110`); and `one-bill-history` — a **shared cross-vertical** bucket — carries
clinic charges with free-text memos keyed by `leaseAppKey` (`packages/one-bill/lenses.go:112-126`). No single
bucket is a PHI dump; the join is.

**What this table is not.** It is not a claim that any of this is read across the boundary today by a Lattice
binary — census C2 found the shipped cross-boundary reads to be deliberate ones (§4.3). It is also not the
whole exposure: the in-flight plane (§For-Andrew ¶2) is separate, live, and worse.

**What it *is*:** the row's bucket is the least interesting item on it, and the mechanism is uniform. A fix
aimed at `capability-author-context` would leave `core-kv`, the PHI join above, and the in-flight plane open to
the same caller. That is the argument for treating the mechanism rather than the instance.

## 4. The shape

### 4.1 One field, mirroring the ratified `SubscribeAllow` precedent

`Component` gains a **declared read posture** — exactly one of two fields must be set, and the conformance test
default-denies the undeclared shape:

```go
// ReadBuckets is the component's declared read surface: bucket -> MODE.
// Non-nil replaces the blanket $JS.API.> grant with the per-bucket
// JetStream read verbs (§4.2) — a JetStream KV read is a publish, so the
// read surface is an allow-list like every other publish grant. A
// component that declares ReadBuckets must also pin a NON-EMPTY
// SubscribeAllow (a ">" subscribe tails $KV.<bucket>.> live, and an EMPTY
// allow list is subscribe-everything — see §4.5).
ReadBuckets map[string]ReadMode

// ReadMode distinguishes the two shapes, because they are not the same
// privilege. ReadGet grants point reads only. ReadList additionally grants
// consumer create/delete on the backing stream — and a consumer carries a
// caller-chosen DeliverSubject the server does NOT permission-check, which
// makes CONSUMER.CREATE an arbitrary-subject publish primitive (§14/F2).
// So ReadList is never granted on a bucket the component can also WRITE,
// and never by default.
type ReadMode int
const (
    ReadGet  ReadMode = iota // KVGet only
    ReadList                 // KVGet + KVListKeys/Watch
)

// ReadUnrestricted keeps the v1 posture — the blanket $JS.API.> grant, and
// therefore read access to every stream in the deployment. It is a
// DECLARATION, not a default: TestEveryComponentDeclaresReadPosture fails a
// row that sets neither field, so a new component cannot inherit
// read-everything by omission. Desc must say why.
ReadUnrestricted bool
```

Two-field-and-declare rather than "nil means unrestricted", deliberately: a nil-means-open field is
default-**open** on the exact failure the design exists to prevent — someone adding a matrix row and not
thinking about reads. This is the shipped `# read-posture: (a|c|d|e|f)` idiom (`scripts/lint-conventions.go`)
applied to the matrix: the gate does not classify, it makes the author declare. Declaring is cheap; forgetting
fails closed.

### 4.2 What `ReadBuckets` derives (the subject table)

For each declared bucket `B` (backing stream `KV_B`), `Allow()` appends — **`ReadGet` grants rows 1–3;
`ReadList` grants rows 1–5**:

| # | Subject | Mode | Why (pinned) |
|---|---|---|---|
| 1 | `$JS.API.STREAM.INFO.KV_B` | both | opening the KV handle — every substrate KV call starts here (G9, G11). Also what `KVStatus` needs |
| 2 | `$JS.API.DIRECT.GET.KV_B.>` | both | `KVGet` — direct get last-by-subject, `$KV.B.<key>` is multi-token (G8) |
| 3 | `$JS.API.STREAM.MSG.GET.KV_B` | both | the non-direct fallback when a stream has no `AllowDirect` (G8) |
| 4 | `$JS.API.CONSUMER.CREATE.KV_B.>` | **list only** | the ordered consumer behind `KVListKeys`/`KVListKeysPrefix`/`KVListKeysFilter`/`Watch` |
| 5 | `$JS.API.CONSUMER.DELETE.KV_B.>` | **list only** | `lister.Stop()` → `Unsubscribe` → a **synchronous** `DeleteConsumer` (`nats.go@v1.52.0` `js.go:2012`, `nats.go:5199-5202`, `js.go:1452-1467`; `internal/substrate/kv.go:203`, `:242`). Denied, every listing blocks for `defaultRequestWait` = **5s** (`js.go:299`). Safe to grant: it deletes only the ephemeral the caller just created (§14/F5) |

Deliberately **not** granted: the **bare** `$JS.API.DIRECT.GET.KV_B` (the get-by-revision / `next_by_subj` /
`multi_last` body form — it reads *superseded revisions*, i.e. the bucket's history rather than the lens view,
and census C2 found no app that calls `GetRevision` or `History`, so it is dead weight the first draft granted
by reflex; a future caller adds it deliberately, per bucket); the legacy no-name
`$JS.API.CONSUMER.CREATE.KV_B`; and every write-shaped verb, which `Deny()` still covers unchanged.
| `$JS.API.CONSUMER.CREATE.KV_B.>` | `KVListKeys` / `KVListKeysPrefix` / `Watch` — the ordered consumer. The wire subject is `$JS.API.CONSUMER.CREATE.KV_B.<generatedHash>.$KV.B.>`: nats.go inlines the **filter subject as literal tokens**, so the name and the filter both sit after the stream token (G10) |

Deliberately **not** granted: the bare `$JS.API.CONSUMER.CREATE.KV_B` (the legacy no-name endpoint this
codebase never uses — `matrix.go:239-245` already denies it matrix-wide for core-events), and every
write-shaped verb (those stay covered by `Deny()` unchanged).

**`$JS.ACK.>` is REMOVED from every read-scoped row — it is not plumbing, it is a cross-stream read
primitive.** The first draft repeated the matrix's own characterisation of it as "not a data-plane privilege"
(G17). That is false, and the adversarial pass broke it: an ack payload prefixed `+NXT` is dispatched as a
**next-message request** whose results are delivered to the **ack message's own reply subject**
(`server/consumer.go:2736-2738` → `processNextMsgRequest(reply, …)`), the ack subscription is
`$JS.ACK.<stream>.<consumer>.*.*.*.*.*` (`:1386-1390`, subscribed at `:1704`), and **nothing checks the
publisher**. `$JS.ACK.>` therefore lets its holder pull messages from any *pull* consumer whose stream and
consumer name it can name — and this repo's durables are pull consumers with explicit ack and **package-const
names**, the six protected `core-events` durables included (`matrix.go:103-110`). `Deny()` closes
`$JS.API.CONSUMER.MSG.NEXT.core-events.<name>` (`matrix.go:170`) and nothing on `$JS.ACK`.

The four apps need none of it: their only consumers are nats.go KV-watcher ordered consumers, which are forced
to `AckNonePolicy` (`nats.go@v1.52.0` `js.go:1781`), and an `AckNone` consumer never creates an ack
subscription at all (`server/consumer.go:1699-1707`). `$JS.FC.>` stays universal — its payloads are empty acks
that return no data, and it is genuinely required for a listing large enough to trip flow control
(`matrix.go:180-192`).

**This also under-scopes an existing board row**, which frames `$JS.ACK.>` as *ack-forge* (suppression) against
six named `core-events` consumers. `+NXT` is a **read** against any pull consumer on any stream. §14/F1 records
the correction; the row is retargeted as part of this fire.

**Object-store reads need their own derivation.** `loftspace-app` calls `ObjectPut`/`ObjectGet`
(`cmd/loftspace-app/objects.go:419`, `:489`, `objects_crypto.go:110`, `:159`, `lease_document.go:99`), and
`$O.core-objects.>` — the grant it already holds — is the chunk/meta **publish** subject family, not the JS-API
surface the handle needs: opening the store is `$JS.API.STREAM.INFO.OBJ_core-objects`, a `Get` does a
`GetLastMsg` for the meta and creates an **ordered consumer on `OBJ_core-objects`** for the chunks
(`nats.go@v1.52.0` `object.go:312-314`, `:959-961`, `:697-703`). So `Component` also takes
`ReadObjectStores map[string]ReadMode`, deriving the same five rows against `OBJ_<bucket>`. Without it
Increment 1 breaks the lease-signing PDF upload *and* download — which is the very flow §10 nominates as the
live-verification case (§14/F4).

**Writes are untouched.** `Allow()`'s registry-derived `$KV.<owned>.>` and the whole of `Deny()` are unchanged;
`ReadBuckets` adds read verbs and removes the blanket `$JS.API.>` from the rows that declare it. A component
that both reads and writes a bucket needs it in `ReadBuckets` as well as owning it — the two derivations are
independent by design, so "I own it" never silently implies "I may read it via the API".

### 4.3 What each vertical app declares (derived from its own source, not guessed)

Every app's read set is a short list of Go consts in its own package — but **not one a grep over
`cmd/<app>/` can produce**, which is how the first draft got three of the four rows wrong (§14/F3, F7). The
lists below are corrected against the adversarial census; the builder still re-derives them by **call graph**
(§6 C2), because the misses came from reads that live behind an `OpenKV` handle, a closure, or a shared
`internal/**` package. `L` marks `ReadList`; everything else is `ReadGet`.

| Component | `ReadBuckets` | `SubscribeAllow` |
|---|---|---|
| `clinic-app` | **`token-revocation`**, `health-kv`, `weaver-targets`L, `op-catalog`L, `clinic-appointments`L, `clinic-providers`L, `clinic-sites`L, `clinic-provider-sites`L, `clinic-ledger-history`L, `clinic-patient-accounts`L, **`wellness-sessions`**L, **`wellness-bookings`**L | `["_INBOX.>"]` |
| `cafe-app` | **`token-revocation`**, `health-kv`, `weaver-targets`L, `cafe-menu-catalog`L, `cafe-lease-workplaces`L, `cafe-ledger-history`L, `cafe-lease-accounts`L, `front-desk-bookings`L, `front-desk-lease-details`L, `front-desk-visits`L | `["_INBOX.>"]` |
| `wellness-app` | **`token-revocation`**, `health-kv`, `weaver-targets`L, `wellness-studios`L, `wellness-sessions`L, `wellness-bookings`L, `wellness-instructors`L, `wellness-members`L, `wellness-ledger-history`L, `wellness-member-accounts`L | `["_INBOX.>"]` |
| `loftspace-app` | **`token-revocation`**, `health-kv`, `weaver-targets`L, `op-catalog`L, `loftspace-listings`L, `loftspace-ledger-history`L, `loftspace-lease-accounts`L, `front-desk-bookings`L, `privacy-pii-key-envelopes`, **`one-bill-history`**L, **`my-tasks`**L *(the only app that reads it)* · `ReadObjectStores: {core-objects: L}` | `["_INBOX.>"]` |

**`token-revocation` is the row that matters most, and the first draft both omitted it AND listed it among
what the tier loses.** All four apps open it at boot (`cmd/clinic-app/main.go:173` and siblings →
`revocation.BucketName`) and `KVGet` it on **every authenticated request**; `IsRevoked` **fails closed** on a
KV error (`internal/gateway/revocation/revocation.go:54-62` → `internal/gateway/auth/auth.go:557-559`). Shipped
as first drafted, Increment 1 would have failed 100% of authenticated requests in all four apps — as a
per-request hang to the context deadline, then a 500 (§14/F3). It is `ReadGet`, never `ReadList`: the checker
only ever `Get`s a single key.

**The cross-boundary reads are more numerous than the first draft said, and that is the point.** `clinic-app`
genuinely reads two *wellness* buckets (`cmd/clinic-app/wellness.go:126`, `:133`); `cafe-app` reads three
`front-desk-*` buckets (`cmd/cafe-app/frontdesk.go:78`, `:155`, `:233`); `loftspace-app` reads
`front-desk-bookings` (`portfolio.go:286`) and `one-bill-history` (`one_bill.go:135`), the latter a
cross-vertical aggregate carrying clinic charges with free-text memos. So "cross-vertical read" is not
uniformly illegitimate — several are shipped and intended. **A blanket grant cannot tell the intended ones from
the rest; a declared scope makes each one a reviewable line in the matrix and the rest impossible.** That is
the shape of the value here, more than any single bucket on §3's table.

The exact lists are **Phase-0 work for the builder**, mechanically derived by census C2's command and pinned by
the positive conformance tests (§11) — not transcribed from this table. The table is here to show the size and
to make the point: this is enumerable because it is *the app's own* read set, which is why the
un-enumerability that killed the bucket-isolation design's Fork B (dynamically-named lens buckets, dot-free
single-token names, no prefix wildcards) does not bite here.

`_INBOX.>` is sufficient for the subscribe side, on two grounds rather than by analogy. First, none of the four
apps calls `Subscribe` or `Watch` anywhere (census C2 → empty), so there is no standing subscription to
preserve. Second — and this is the part `facet`'s precedent does *not* cover, because facet never lists a
bucket — a `KVListKeys` ordered consumer's **deliver subject** is `nc.NewInbox()`, default prefix `_INBOX.`
(G21), so the listing's own deliveries land inside the allowed prefix. Every other reply the apps await (JS API
responses, direct-get responses, loftspace's two vault RPCs) is an ordinary request inbox.

**The subscribe half is not optional.** A publish-only read scope would be bypassable: a plain core subscribe
on `$KV.<bucket>.>` receives every write to that bucket live, because a stream captures its subjects through an
ordinary sublist subscription and core delivery is fan-out (G20). A component with `subscribe { allow: [">"] }`
can therefore tail any bucket without touching `$JS.API` at all. Narrowing `SubscribeAllow` is what closes it;
`subscribe { deny: … }` is also supported and enforced symmetrically, but an allow-list is the fail-closed
direction and is what `RenderConf` already emits. `RePublish` — the other way stream contents can surface on a
tappable subject — is configured nowhere in this repo (G20), so there is no third path today; an adversarial
pass item (§13) is to keep it that way.

### 4.4 What the app tier loses, stated plainly

`core-kv`, `capability-kv`, `capability-proposals`, `capability-author-context`, `orchestration-history`,
`weaver-state`, `loom-state`, `model-results`, `refractor-adjacency`, `personal-lens-interest`,
`credential-bindings`, `core-events`, and every other vertical's buckets — plus, via the narrowed
`SubscribeAllow`, the ability to tail **any subject in the account**: `ops.>` (plaintext sensitive mutations,
§For-Andrew ¶2), `events.>`, the Vault RPC traffic, and other components' reply inboxes. Plus `$JS.ACK.>` and
its `+NXT` pull primitive (§4.2). Each app keeps exactly its own P5 read surface plus the deliberately-shared
buckets. (`token-revocation` is **kept** — see §4.3; the first draft listed it here in error.)

**A denied read fails as a TIMEOUT, not as a permission error — and today nobody hears the `-ERR` at all.**
This is the design's one genuinely surprising mechanical fact, and it inverts the migration-safety argument a
first draft of this design made. Grounded per G18/G19: the server refuses a denied publish *before* the message
reaches the sublist, so the JS API responder never runs and no reply is ever produced — `substrate.KVGet`
therefore observes only its own context deadline (`nats.ErrTimeout` / `context.DeadlineExceeded`),
indistinguishable from a slow leader or a network stall. The server does emit
`-ERR Permissions Violation for Publish to "$JS.API.DIRECT.GET.KV_x…"`, but it is a **connection-level frame
with no reply-to correlation**, and nats.go's correlation regex matches *subscribe* violations only, so a
publish violation lands on `AsyncErrorCB` with a nil subscription — and `substrate.Connect` sets **no**
`nats.ErrorHandler`, so `AsyncErrorCB` is nil and the frame is dropped. A permission mistake anywhere in this
platform is currently invisible.

**So Increment 1 pays that debt first, as its own step:** `substrate.Connect` registers a `nats.ErrorHandler`
that logs the async error (at `Error` level, with the connection's component name), turning every permission
violation — this design's, the Bridge's shipped core-kv denies, and every future narrowing — into a searchable
log line naming the exact subject. It is ~10 lines, it is the only thing that makes the migration debuggable,
and it is worth more than this design: it is the observability the two prior matrix fires shipped without.
(This replaces what an earlier draft assumed was already true. It is *not* a substitute for the per-bucket
positive-control tests in §11 — those are what prove the read scope is complete before it ships; the handler
is what makes a miss diagnosable in seconds rather than mistaken for NATS flakiness.)

### 4.5 The lint/conformance gate ships in the same increment

Per the ratified lint doctrine — a design that establishes a convention ships the gate that binds the next
author, in the same fire, blocking, never as a follow-on:

- `TestEveryComponentDeclaresReadPosture` — every `Matrix` row sets exactly one of `ReadBuckets` /
  `ReadUnrestricted`. Fails closed on a new row that declares neither. Zero debt after Increment 1, because
  every existing row gets a declaration (most of them `ReadUnrestricted: true` with the reason in `Desc`).
- `TestReadScopedComponentHasNoBlanketGrants` — a row with `ReadBuckets` must not carry `$JS.API.>` **or any
  wildcard that overlaps the derived read set**, must not carry `$JS.ACK.>` (§4.2), and must set a
  **non-empty** `SubscribeAllow` that contains no `>`. Three sharpenings the adversarial pass forced, each
  closing a way to void the scope while passing the first draft's version of this test:
  - **An EMPTY `SubscribeAllow` is subscribe-EVERYTHING, not subscribe-nothing.** `RenderConf` gates on
    `c.SubscribeAllow != nil` (`matrix.go:624`), so a non-nil empty slice renders `subscribe { allow: [] }`;
    the server parses that to a nil allow-sublist, and `canSubscribe` short-circuits — *"Check allow list. If
    no allow list that means all are allowed"* — leaving `allowed = true` (`server/client.go:3266-3267`,
    `:1094`). This is the **same fail-open class the matrix already documents twelve lines from the field**
    (`matrix.go:541-545`: "NATS treats an empty allowed_origins as allow-any-origin"). The lesson was in the
    file and the first draft did not apply it. `RenderConf` must gate on `len(...) > 0`, and so must the test.
  - **A string-equality check on `"$JS.API.>"` is not enough** — `"$JS.>"` or `"$JS.API.STREAM.>"` would pass
    it. Use a wildcard-overlap check against the derived set.
  - **`ExtraPubDeny` can silently void a declared read** (deny beats allow, G22) with nothing to catch it.
    Not a new gate — §11's per-bucket positive controls are what catch it — but it is why those controls are
    per-bucket rather than one smoke test.
- `TestConfMatchesMatrix` (existing) keeps `deploy/nats-server.conf` in lockstep — unchanged, it just gets a
  much shorter file to diff.

## 5. State lifetime

**This design introduces no runtime state** — no registry, no cache, no latch, no accumulated set. `ReadBuckets`
is a compile-time table field, rendered once into a static config file. The lifetime table below exists because
the obligation is to *state* that, not to skip it:

| Thing | Created | Reset / carried | Ordered against |
|---|---|---|---|
| `ReadBuckets` / `ReadUnrestricted` | compile time, in `Matrix` | n/a — immutable, no runtime mutation path | n/a |
| The rendered `deploy/nats-server.conf` | `go run ./deploy/gen-dev-nkeys` | regenerated wholesale; never patched | pinned by `TestConfMatchesMatrix`; a stale conf is a failing test, not a runtime drift |
| The server's in-memory permission set | NATS server start / config reload | per connection, at connect | a component that reconnects re-derives from the same static conf — no per-connection state to lose |

The one operational consequence worth naming: a **package that adds a new lens target bucket an app must read**
now requires a matrix edit + `gen-dev-nkeys` + a stack restart, where today it required nothing. That is the
real cost of the change and it is priced in §12 R1.

## 6. Executable censuses

Each count this design relies on ships as the command that derives it, so Phase 0 re-runs rather than trusts.

**C1 — every KV bucket that exists at runtime.**
```sh
# platform registry
sed -n '/func PlatformBuckets/,/^}/p' internal/bootstrap/platform_buckets.go | grep -c 'Name:'   # expect 12
# package lens targets (sweep every .go under packages/, not just lenses.go)
grep -rn 'Bucket:' packages/ --include='*.go' | grep -v '_test.go'
grep -rn 'Bucket = "' packages/ --include='*.go'
```
Expected: 12 platform buckets + ~30 package lens-target buckets. **The count is not load-bearing** — the design
enumerates *readers*, not buckets — but the list is what census C2 iterates.

**C2 — each component's real read set** (the input to §4.3). **A grep is NOT sufficient and must not be used
alone** — that is exactly how the first draft invented `clinic-patients` + three `my-tasks` entries and missed
`token-revocation`, `one-bill-history` and the object store (§14/F3, F7). A call-site grep sees only
`KVGet(ctx, <ident>)`-shaped lines; it cannot see a read behind an `OpenKV` handle held on a struct
(`cmd/clinic-app/main.go:173` + `internal/gateway/revocation/revocation.go:55`), behind a `kvGetter` closure
(`cmd/cafe-app/server.go:135`, `cmd/wellness-app/server.go:145`), or inside a shared `internal/**` package the
app links (`internal/projectionhealth`). It also cannot resolve `bucket := <const>`, and every one of the four
apps rebinds that same identifier to different consts across files.

The census is therefore a **call-graph sweep**: from each `cmd/<app>`'s `main`, walk every function reachable
into `internal/**`, and collect every `KV*` / `OpenKV` / `ObjectStore` call with its resolved bucket constant.
Start from the widest possible net and then narrow:
```sh
for app in clinic-app cafe-app wellness-app loftspace-app; do
  echo "== $app"
  grep -rn 'OpenKV\|KVGet\|KVListKeys\|KVStatus\|ObjectGet\|ObjectPut\|Bucket\b' cmd/$app/ | grep -v _test
  # every packages/... import is a bucket-const candidate:
  grep -rn 'operatinggraph/lattice/\(packages\|internal\)/' cmd/$app/*.go | grep -v _test
done
grep -rn 'Subscribe\|Watch(' cmd/clinic-app/ cmd/cafe-app/ cmd/wellness-app/ cmd/loftspace-app/   # expect: empty
```
Corrected expectation: **9–12 buckets per app** (clinic 11, cafe 9, wellness 9, loftspace 10 + the object
store); **zero** subscribe/watch call sites in all four, production and test. A Phase-0 run that finds a
subscribe call site invalidates §4.3's `_INBOX.>` and must widen that row. **The §4.3 table is a corrected
starting point, not an authority — re-derive it.**

**C3 — the negative that motivates the design** (falsifies "this is a `capability-author-context` problem"):
```sh
grep -n 'DIRECT.GET\|STREAM.MSG.GET\|SubscribeAllow' internal/natsperm/matrix.go
```
Expected: the Bridge's three core-kv denies (`matrix.go:379-387`), `facet`'s `SubscribeAllow`
(`matrix.go:528`), and `protectedStreamDenies`' explicit "ordinary reads stay allowed" comment
(`matrix.go:68-69`). If a future run returns more, some other fire has started closing
reads and this design must reconcile with it.

## 7. Contract surface

**None.** No `docs/contracts/*` section is touched, and no contract edit is staged. Checked, not assumed:

- Contract #7 (primordial bootstrap) §7.1 names bootstrap as the sanctioned provisioner — unchanged, and
  `bootstrap` keeps `ReadUnrestricted: true` for exactly that reason.
- Contract #6 (capability KV) describes the capability projection's *content*, not who may read the bucket at
  the transport. Unchanged.
- Contract #5 (health KV) — `health-kv` stays shared-write and appears in every read scope.
- The matrix, `deploy/gen-dev-nkeys`, and `deploy/nats-server.conf` are component/deploy surface, the same
  classification both prior matrix designs used and Andrew ratified.

One **documentation** obligation, not a contract change: `lattice-architecture.md`'s P5 currently reads as an
application-layer rule. After Increment 1 it is transport-enforced for the app tier, and the two matrix
designs' "reads are unrestricted in the v1 posture" sentences
(`natsperm-platform-bucket-isolation-design.md` §3.3) become partially superseded. Increment 1 rewrites those
sentences in place with a pointer here — per the ratification-revision rule, a banner over a stale body is how
a later design grounds on a withdrawn shape.

## 8. Reconciliation with the existing mental model

*Didn't we already restrict what components can touch?* Yes — **writes**. The write-restriction design gave
each binary its own NKey; the bucket-isolation design derived per-bucket write denies from the platform
registry. Both explicitly deferred reads: *"Subscribe stays `allow: [">"]` for internal components — reads are
unrestricted in the v1 posture"* (`natsperm-platform-bucket-isolation-design.md` §3.3). This is that deferral,
taken up for one tier.

*Didn't we already harden the app tier?* Yes — its write side, on this same threat model (G16). The apps hold
no `ops.>` publish precisely so a compromised app cannot forge an actor. An attacker who cannot write but can
read the entire graph has still won most of what that hardening was protecting.

*Doesn't the P5 lint already stop an app reading Core KV?* It stops an app's **source** from naming the
bucket (`lint-conventions.go:1044`). It is a string scan over `cmd/<app>/*.go` with a platform allow-list — a
correct authoring gate, and no boundary at all against a compromised binary, a vendored dependency, or a read
assembled at runtime. This design is the fail-closed door the lint has been standing in for.

*Isn't this the read-scope mechanism Andrew shelved on 2026-08-06?* **No — different subject, and the shelving
reasoning does not transfer.** [declared-read-scope-authorization-design.md](declared-read-scope-authorization-design.md)
is about a **per-actor** read authorization inside the **Processor's write path** — relating an operation's
`contextHint.reads` to the submitting actor's grant, with a template resolver and a live-seam check; it was
held because the four faces of the exposure were verified *contained by the corpus* and the mechanism was
judged the wrong altitude for that. This design is **per-component transport confinement**: no runtime
evaluation, no new declaration language, no per-request cost — a static allow-list in a file that already
exists, in a mechanism (`SubscribeAllow`) already ratified. I raise it because the name collision is exactly
the kind of precedent-transfer that produces a reflex "no".

**But the adversarial pass was right to refuse "and nothing else", and the honest version is worth stating.**
What the two *do* share is the **form of the reasoning that shelved the earlier one**: Andrew's ratify session
verified containment face by face rather than accepting the filed severity, and the first draft of this design
ran the identical containment argument against itself ("no app reads any of it today; the exposure needs a
compromised binary"). Applied consistently, the 2026-08-06 test would have downgraded this too. Two things
break the transfer, and neither is "it's a different subject":
1. **The containment premise is false here.** The in-flight plane (§For-Andrew ¶2) needs only the credential,
   not a compromise, and it carries plaintext PII. There is no corpus accident doing the containing.
2. **The cost asymmetry is an order of magnitude.** The shelved design needed a runtime declaration language,
   a template resolver and a live-seam check on the write path; this needs a field on a compile-time table and
   a shorter generated file.

If either of those stops being true — if the in-flight finding turns out to be wrong, or the mechanism grows —
the shelving precedent *should* apply, and I would rather say so here than have it discovered at ratification.

*Does this duplicate the auth-callout / per-identity subscribe ACL?* No, and the bucket-isolation design
already recorded why they compose: the callout mints per-connection permissions for **unrecognized external**
connections (Edge browsers); component users bypass the callout entirely and stay on static NKey users. This
design edits only the static users. No semantic interaction.

*Does this introduce new state?* No — §5.

*Is a parallel in-flight design touching this seam?* Checked (`grep -l natsperm _bmad-output/implementation-artifacts/*.md`
against the lane's 📐/🏗️ rows): the two live natsperm residuals are the NL design's **matrix-wide
`allow_responses` property** (a *reply-subject publish* question, flagged for Andrew, orthogonal to reads) and
the `$JS.ACK.>` ack-forge row. The first draft called that second one orthogonal; **it is not** (§14/F1): it
is filed as *suppression* (ack-forge) against six named `core-events` consumers, and the mechanism is in fact a
**read** primitive (`+NXT`) against any pull consumer on any stream. This design removes `$JS.ACK.>` from the
read-scoped rows, which closes it for the app tier only; the row survives, retargeted, for every row that keeps
the grant. The `allow_responses` residual stays genuinely orthogonal (a reply-subject *publish* question) and
merges textually in either order.

## 9. Alternatives considered

1. **Do nothing / keep the v1 posture.** Defensible while the app tier is trusted — but the tier was
   explicitly *un*trusted on the write side by #75 Fire 2b, and the asymmetry is the finding. Rejected, though
   this is the option §For-Andrew's fork can still choose.
2. **Demand-side fix: stop projecting `spec` in the catalog lens.** The mandatory single-digit-census
   alternative, and the *right* answer to the row as filed — one package edit, XS, no matrix change. Rejected
   as the whole answer because census C3 shows the same caller reaches `core-kv` regardless: it fixes the
   symptom named in the row and leaves the mechanism, and the mechanism's worst instance, untouched. **Worth
   noting it is not exclusive** — if the fork lands on "do nothing", this is the fallback that still closes
   the filed row, and it should be filed as such rather than lost.
3. **Per-bucket read denies (extend the Bridge precedent, deny-side).** Add a `Sensitive` flag to the platform
   registry and derive `$JS.API.DIRECT.GET.KV_<b>*` / `MSG.GET` / `CONSUMER.CREATE` denies for every non-reader.
   Rejected on three counts: it is **default-open** (a new bucket is readable until someone flags it — the
   exact direction the fail-closed reflex forbids); package lens buckets are not in the registry at all, so
   the mechanism structurally cannot reach the buckets census C3 cares most about; and it is the framing that
   manufactures the Loupe carve-out. The deny-list is also already the reason the rendered conf carries ~180
   subjects per component (`deploy/nats-server.conf:208`) — an allow-list for the app tier makes that row ~15
   lines and legible.
4. **Separate NATS accounts per tier, with stream exports/imports.** The textbook NATS answer and the true
   end-state: an account boundary makes cross-tier reads structurally impossible rather than
   enumeration-dependent. Rejected for now, not on principle but on cost and sequencing — JetStream across
   accounts needs API-prefix mappings and per-account domains, every component's connection story changes, and
   it collides head-on with the un-ratified HA-NATS/multi-cell work. Naming it here so the incremental step is
   not mistaken for the destination; if Andrew wants the account model, this design should be shelved rather
   than half-built.
5. **Narrow the platform daemons too (processor/refractor/loom/weaver/chronicler/loupe/lattice/bridge).**
   Rejected for now, and this is the alternative I ran hardest against my own recommendation. Refractor writes
   and reads *dynamically-named* package lens buckets — the un-enumerable set that killed Fork B — so it
   cannot be enumerated without coupling the transport config to the installed package set; Loupe's whole
   charter is reading everything; the Processor *is* Core KV. Narrowing these buys confinement against a
   compromise of components that already hold far stronger privileges (op submission, Vault decrypt, sole
   Core-KV write), which is a much weaker argument than the app tier's. `model-runner` and `gateway` are the
   two genuinely enumerable exceptions, hence Increment 2.
6. **Turn off `allow_direct` on the sensitive buckets.** A stream with `AllowDirect: false` never subscribes
   the two `DIRECT.GET` subjects at all (`server/stream.go:4849-4855`), collapsing the read surface to the one
   `STREAM.MSG.GET` subject. Rejected: it is a **per-stream, all-clients** knob, not a per-user one — the
   legitimate readers lose the direct path too, falling back to the leader-only JS API request path that the
   server's own doc comment (`stream.go:80`) contrasts as the lower-performance option. It restricts nobody in
   particular and taxes everybody. It also does nothing about listing or about the core-subscribe path (§4.3).
7. **Route app reads through the Gateway instead (apps hold no NATS credential at all).** Structurally the
   cleanest — it would make P5 a code path rather than a permission — but it turns every app read into an HTTP
   hop through a component that today translates *writes*, needs a read API that does not exist, and re-opens
   read-path authorization (D1) for a tier that is currently trusted with whole read models. An order of
   magnitude more work than the row's demand justifies. Rejected; recorded because it is the shape a future
   "apps are untrusted" posture would actually take.

**Running each rejection back against the recommendation** (the discipline that catches self-inflicted
versions of the flaw you just named): alternative 3's fatal property is *default-open* — §4.1's two-field
declaration is the direct answer, and `TestEveryComponentDeclaresReadPosture` is what makes it real rather
than aspirational. Alternative 5's fatal property is *un-enumerability* — which is why the recommendation
touches only components whose read set is a list of consts in their own source, and why §4.3's lists are
Phase-0-derived and test-pinned rather than transcribed. Alternative 2's fatal property is *fixing the named
instance and leaving the mechanism* — which is the charge this design must not also earn: Increment 1 closes
`core-kv` and every unrelated bucket to the app tier, i.e. the mechanism for that tier, not the instance.

## 10. Decomposition for the Steward

**Phase-0 verifications** (before any code — each one is a premise the design rests on):

- **V1 — re-run census C2** and build §4.3's lists from its output. Any subscribe/watch call site found in an
  app widens that row's `SubscribeAllow` and must be stated.
- **V2 — ANSWERED by the adversarial pass, recorded so it is not re-run blind:** no app uses
  `KVListKeysFilter`, `KVStatus`, `GetRevision` or `History`. `KVStatus` would need `STREAM.INFO` (row 1,
  granted); `KVListKeysFilter` needs the same `CONSUMER.CREATE` as `KVListKeys` (row 4). What the first draft
  *did* miss is `CONSUMER.DELETE` (row 5) and the **object store** — which V2 wrongly treated as covered by
  loftspace's `$O.core-objects.>` grant. It is not (§4.2, §14/F4). Re-verify the object-store derivation
  specifically.
- **V3 — confirm the `-ERR` is still uncorrelated and unlogged** on the pinned pair before writing the handler
  (G18/G19 say it is). Note two connection paths G19's phrasing does not name and step 0 must cover:
  `cmd/bootstrap/main.go:229-246` connects via `nats.Connect` directly, bypassing `substrate.Connect`; and
  `internal/modelrunner/engine.go:275` sets a **`micro.Config.ErrorHandler`**, which is a different mechanism
  and does *not* catch a JS-API publish-permission violation — do not mistake it for coverage.

*(An earlier draft carried "verify a denied read fails loudly" here. It does not — G18/G19 settled it, the
answer was the opposite of the assumption, and step 0 below exists because of it.)*

**Increment 1 — the mechanism + the four vertical apps** (the whole of the filed row; posture-changing, full
review depth). **Step 0: the `nats.ErrorHandler` in `substrate.Connect`** (§4.4) — first, alone, and verifiable
on its own: it makes every subsequent step's failures legible, and it is worth landing even if the fork below
lands on "do nothing". Then: `ReadBuckets`/`ReadUnrestricted` on `Component`; the §4.2 derivation in `Allow()`; every one of
the 18 rows given an explicit posture (14 `ReadUnrestricted: true` with the reason in `Desc`; the 4 apps
enumerated; `facet` gets `ReadBuckets: []string{"health-kv"}` — it already has no `$JS.API.>`, so this is the
declaration catching up with the shipped shape, and the conformance test will demand it); `SubscribeAllow:
["_INBOX.>"]` on the four apps; `go run ./deploy/gen-dev-nkeys` regenerating `deploy/nats-server.conf`; the
§4.5 gates; the §7 doc-supersession edits. Conformance tests per §11.

**Live verification is not optional for this increment and must precede the commit:** `make up-full`, then
every page of all four apps rendered end to end **while signed in** (the `token-revocation` read is on the
authenticated-request path, §4.3 — an unauthenticated smoke test would miss it entirely), plus loftspace's
sensitive-object upload **and download** (the object-store derivation, §4.2), and the one-bill history page.
The adversarial pass found three of these four flows broken by the first draft's lists; they are the
regression set.

**Increment 2 — `model-runner` and `gateway`** (sequenced, not urgent; posture-changing, full review depth).
Both have small, enumerable read sets already documented in their own matrix comments (`model-results` for the
runner, `matrix.go:415-431`; `token-revocation` + `credential-bindings` for the Gateway (`matrix.go:475-489`), plus
`capability-kv`, which its Desc does **not** mention — the read is `cmd/gateway/main.go:299`). The Gateway additionally consumes its two protected core-events durables, so its
declaration must cover the core-events consumer subjects it already holds — the increment's one real piece of
work, and the reason it is separate rather than folded into Increment 1. **Not dead scaffolding:** both
consumers exist and run today; this narrows live components, it does not pre-build for an absent one.

**Not proposed for build:** narrowing `bridge` (it already carries the targeted core-kv read deny that is 90%
of the value), or any of the seven daemons + Loupe (§9 alt 5). They ship a `ReadUnrestricted: true`
declaration in Increment 1 and that is the end of it unless the posture question in §For-Andrew says otherwise.

**Review depth** is the Steward's sizing per `agents/steward/SKILL.md` §4; both increments above are flagged
**posture-changing** (they are the security boundary), so they earn the full pass. No blanket claim is made
about any other increment.

## 11. Test strategy

Every test below is **owned by Increment 1** unless marked I2. The conformance suite is offline — it renders
the matrix, boots an embedded server from the rendered conf, and connects as each component
(`internal/natsperm`'s existing `startServerFromConf` / `connectAs` harness), so none of it needs the stack.

| Test | Owner | What it proves |
|---|---|---|
| `TestEveryComponentDeclaresReadPosture` | I1 | the gate: a row declaring neither field fails (§4.5) |
| `TestReadScopedComponentHasNoBlanketGrants` | I1 | `ReadBuckets` + `$JS.API.>` / an overlapping wildcard / `$JS.ACK.>` / a `>` subscribe / an **empty** `SubscribeAllow` are each refused — the five ways to silently void the scope (§4.5) |
| `TestEmptySubscribeAllowIsRefused` | I1 | the fail-open that `RenderConf` would otherwise emit: `subscribe { allow: [] }` renders never, and a live server started from such a conf is proven to accept `>` (the positive control that makes the gate meaningful, not the gate alone) |
| ~~`TestAppTierHoldsNoAckGrant`~~ | I1 | **Shipped 2026-08-22 by the F1 fire as `TestVerticalAppHoldsNoAckGrant`** (`internal/natsperm/conf_test.go`), with the vector as specified — a `+NXT` publish against a live pull consumer, both ack wire forms, the Processor as the positive control — plus `TestAckGrantRoster` pinning the converse. Increment 1 inherits it; writing it again would duplicate |
| `TestAppTierCannotCreateForeignDeliverySubject` | I1 | the B2 primitive: an app is denied `CONSUMER.CREATE` on a bucket it can write (`health-kv`), so it cannot mint a consumer whose `DeliverSubject` is `ops.system` |
| `TestAppTierRevocationCheckStillWorks` | I1 | `token-revocation` `KVGet` succeeds for all four apps — the fail-closed auth path (§4.3), the single most breakable thing in this increment |
| `TestLoftspaceObjectStoreStillWorks` | I1 | `ObjectPut` **and** `ObjectGet` round-trip on `core-objects` under the narrowed grant (§4.2's `OBJ_` derivation) |
| `TestAppTierReadsItsOwnBuckets` | I1 | **positive control, per app per bucket**: `KVGet` *and* `KVListKeys` both succeed for every entry in §4.3 (two distinct permission paths — the `TestCapabilityAuthorCatalogAccess` precedent pins both for exactly this reason) |
| `TestAppTierCannotReadCoreKV` | I1 | each of the four apps is denied `DIRECT.GET` (both forms), `STREAM.MSG.GET`, and `CONSUMER.CREATE` on `KV_core-kv` |
| `TestAppTierCannotReadForeignBuckets` | I1 | clinic-app denied on `wellness-members`, `loftspace-lease-accounts`, `capability-kv`, `capability-author-context`, `capability-proposals`, `privacy-pii-key-envelopes` — and the symmetric cases per app |
| `TestAppTierCannotTailBucketSubjects` | I1 | a core subscribe to `$KV.core-kv.>` is refused under the narrowed `SubscribeAllow` — the half a publish-only scope would miss |
| `TestAppTierCannotTailOpsOrVault` | I1 | subscribes to `ops.>`, `events.>` and `lattice.vault.decrypt` are refused — the in-flight plane that is §For-Andrew ¶2's headline, and the only test here that covers plaintext-PII-in-transit |
| `TestAppTierListingCompletesPromptly` | I1 | a `KVListKeys` on a declared `ReadList` bucket returns without a 5s stall — proves the `CONSUMER.DELETE` grant (§4.2 row 5); asserts a bound well under `defaultRequestWait`, not a fixed sleep |
| `TestPermissionViolationIsLogged` | I1 | the step-0 handler: a connection whose publish is denied surfaces the `-ERR` through `substrate`'s error handler (asserted on the logged subject, not on the call's return — which is a timeout, G18) |
| `TestConfMatchesMatrix` (existing) | I1 | the regenerated conf matches; catches a hand-edit |
| `TestModelRunnerReadScope`, `TestGatewayReadScope` | I2 | same positive/negative pair for Increment 2's two components |
| live: `make up-full` + all four app UIs + loftspace sensitive upload | I1 | the migration did not miss a bucket; the loud-failure property (§4.4/V1) makes a miss visible |

The negative tests must assert the **denial**, not merely an empty result — a bucket that is empty in the
fixture returns "no keys" whether or not the permission was denied, which is the classic false pass. Each
negative therefore pairs with a positive control write by the bucket's real owner first, exactly as
`TestCapabilityAuthorCatalogAccess` does (`internal/natsperm/model_egress_test.go:104-112`).

## 12. Risks + residuals

- **R1 — a new package lens bucket an app reads now needs a matrix edit** (plus `gen-dev-nkeys` and a stack
  restart), where today it needed nothing. Real, and the honest cost of the change. The step-0 error handler
  makes the symptom diagnosable — a log line naming the exact denied subject — but it does not make it
  *impossible*, and the raw call still returns a timeout (G18). Not mitigated by a lint today: a package's
  bucket const and its app-side reader live in different trees, and the coupling is genuinely new. Stated
  rather than engineered around; the follow-on with a nameable trigger (the second time it bites) is a lint
  that cross-checks each `cmd/<app>`'s bucket consts against its matrix row.
- **R2 — the failure direction is over-restriction, never over-grant** — but it is a *timeout*, not a clean
  refusal (G18), which is the strongest argument for the step-0 handler and for per-bucket positive controls
  rather than one smoke test. A missed bucket presents as a hanging page, and a reviewer with no error handler
  would reasonably first suspect NATS.
- **R3 — the four apps are the demo surface.** A missed bucket breaks a showcase page, slowly. V1 + the
  per-bucket positive controls + the live pass are the answer; the Steward should run the live pass **before**
  commit, not after.
- **R4 — `weaver-targets` stays broadly readable** and three apps list it unfiltered (census C2). It is a
  deliberately shared convergence bucket, so it stays in every app's read scope; a cross-vertical read *within*
  it is unaffected by this design. Naming it so nobody reads §4.4 as "cross-vertical reads are closed" —
  they are closed except through the buckets the platform deliberately shares.
- **R5 — the F2 primitive is pre-existing and this design only declines to extend it.** Every `$JS.API.>`
  holder can already mint a consumer with a caller-chosen `DeliverSubject`. Increment 1 closes it for the four
  apps as a side effect of the `ReadGet`/`ReadList` split, and leaves it wide for the thirteen rows that keep
  the blanket grant. Do not read this design as closing actor-forgery; the filed row (§14/F2) owns that, and
  its first task is to verify whether the Processor actually accepts such an envelope.
- **R6 — Increment 2's Gateway declaration touches the auth-callout responder's component.** It changes only
  the Gateway's own JS API surface, not the callout-issued permissions (§8), but it is the one place where a
  mistake reaches externally-authenticated connections. Full review depth, and the increment stands alone so
  it can be reverted without touching Increment 1.
- **Noted, and deliberately NOT filed as a row:** `handleUnwrapKey` (`internal/vault/service.go:365`) takes a
  caller-supplied `KeyHolderKey` and does no caller-level authorization — the transport grant on
  `lattice.vault.unwrapkey` *is* the boundary, and only `loupe` and `loftspace-app` hold it (`matrix.go:461`,
  `:503`). I checked whether that is a defect before writing it down, and it is the platform's **sanctioned**
  posture for this class: `TestModelEgressReachability`'s own doc states the identical rule for the model
  runner — *"The runner does NO caller-level authorization … so this publish allow-list IS the boundary"*
  (`internal/natsperm/model_egress_test.go:19-25`). Filing it would be manufacturing a row out of a ratified
  design decision. It is recorded here only so a reader of §3's containment column can see that the wrapped-DEK
  row's "inert without unwrapkey" bound rests on a *transport* grant, which is precisely the kind of boundary
  this design is arguing should be declared rather than blanket.

## 13. Adversarial pass — RUN (2026-08-21), and what it changed

The design self-flagged a pre-build adversarial pass. Per the standing rule that flagging a gate creates the
obligation to discharge it — and that the Steward correctly refuses to cold-start a design whose own gate is
open — **the pass was run inside this designer fire**, on the frozen draft, in two independent lenses:

1. **Security-soundness** — attack the confinement, the containment table, the derivation, the declare-or-fail
   gate, the reconciliation, and the payoff claim. Briefed to default to "refuted" when uncertain.
2. **Citation + census audit** — verify every `file:line` in the ledger and the body against the code, and
   re-derive every census a *second way* rather than re-running the doc's own commands.

They returned **5 blockers, 4 majors and several minors**. Every blocker was re-verified against the pinned
source before folding — the two that would have been most expensive to take on trust (`+NXT` as a read
primitive, and the empty-`SubscribeAllow` fail-open) were read directly in `nats-server@v2.14.0`. The design
below is the folded version; §14 is the ledger.

**The pass is recorded as RUN. The design is build-ready on ratification** — no gate of this design's own is
left open for the Steward.

## 14. Findings ledger — what the pass broke, and the disposition

| # | Finding | Verified | Disposition |
|---|---|---|---|
| **F1** | **`$JS.ACK.>` is a cross-stream READ primitive, not plumbing.** `+NXT` on an ack subject dispatches a next-message request delivering to the caller's reply subject; nothing checks the publisher. G17 (quoting the matrix's own comment) was false. | `server/consumer.go:2736-2738`, `:1386-1390`, `:1704`; the AckNone gate at `:1699-1707` | **Folded.** `$JS.ACK.>` removed from every read-scoped row (§4.2); the apps provably need none (ordered consumers are forced `AckNone`, `js.go:1781`). Gate + `TestAppTierHoldsNoAckGrant` added. **Also retargets an existing board row** filed as *ack-forge/suppression on six core-events consumers* — the mechanism is broader and is a read. |
| **F2** | **A consumer's `DeliverSubject` is not permission-checked**, making `CONSUMER.CREATE` an arbitrary-subject publish primitive: write a forged envelope to `$KV.health-kv.<k>` (a `SharedWrite` grant every component holds), create a push consumer filtered to it with `DeliverSubject: ops.system`, and the server publishes it onto the ops lane. | no permission check anywhere in `server/jetstream_api.go` (grep for `perms`/`pubAllowed`/`canSubscribe` → empty); delivery re-subjects at `server/stream.go:8058`; `core-operations` captures `ops.>` (`internal/bootstrap/primordial.go:186-191`) | **Partly folded, partly filed.** Folded: `ReadBuckets` gains a per-bucket **mode**, and `ReadList` is never granted on a bucket the component can write (§4.1/§4.2) — so this design does not hand the primitive out. **Filed as its own row:** the chain is *pre-existing* (any `$JS.API.>` holder can do it today) and it falsifies the #75 Fire 2b claim that an app "cannot forge an `env.Actor`". Different root cause from the read scope, so a separate row, not a fold. **Caveat stated in the row:** the Processor-side reachability (does the delivered envelope survive its validation?) is **not** verified — the row's first task is to ground that before sizing. |
| **F3** | **`token-revocation` was missing from all four app rows and listed among what the tier loses** — while every app `KVGet`s it on every authenticated request, fail-closed. | `cmd/{clinic,cafe,wellness,loftspace}-app/main.go` `OpenKV(revocation.BucketName)`; `internal/gateway/revocation/revocation.go:54-62`; `internal/gateway/auth/auth.go:557-559` | **Folded.** Added as `ReadGet` to all four (§4.3), removed from §4.4's loss list, and given its own test. As first drafted this was a 100%-authenticated-request outage in all four apps. |
| **F4** | **The object store was not covered.** `$O.core-objects.>` is the chunk/meta publish family, not the JS-API surface an object handle needs (`STREAM.INFO.OBJ_*`, a meta `GetLastMsg`, an ordered consumer for chunks). | `nats.go@v1.52.0` `object.go:312-314`, `:959-961`, `:697-703`; call sites `cmd/loftspace-app/objects.go:419`, `:489`, `objects_crypto.go:110`, `:159`, `lease_document.go:99` | **Folded.** `ReadObjectStores map[string]ReadMode` added, deriving the same rows against `OBJ_<bucket>` (§4.2); the lease-signing PDF upload **and download** are in the regression set (§10). |
| **F5** | **`CONSUMER.DELETE` is on the read path.** `lister.Stop()` → `Unsubscribe` → a *synchronous* `DeleteConsumer`; denied, every listing stalls for `defaultRequestWait` = 5s. | `nats.go@v1.52.0` `js.go:2012`, `nats.go:5199-5202`, `js.go:1452-1467`, `js.go:299`; `internal/substrate/kv.go:203`, `:242` | **Folded.** Row 5 of §4.2, `ReadList` only; `TestAppTierListingCompletesPromptly` pins it. |
| **F6** | **§3's "no clinical PHI in NATS-KV" was false.** `clinic-appointments` (nats-kv) projects `reason` — the DDL's own *"Visit reason / chief complaint"* — and `statusNote` against `patientKey`, off an aspect declared **non-sensitive**, so encryption-at-rest never applied. A plaintext re-identification join spans four more nats-kv buckets. | `packages/clinic-domain/lenses.go:548-570`, `ddls.go:700-707`; `packages/clinic-ledger/lenses.go:148-164`; `packages/front-desk/lenses.go:101-110`, `:123-129`; `packages/one-bill/lenses.go:112-126` | **Folded, claim withdrawn.** §3's row rewritten and the join documented. G14 survives but does far less work than the first draft implied — the PHI at issue was never classed sensitive. G23 (decrypt-at-projection cannot target KV) survives intact. |
| **F7** | **§4.3's per-app lists were wrong in both directions** (invented `clinic-patients` + three `my-tasks`; missed `token-revocation`, `one-bill-history`, the object store), because §6 C2's census was a call-site grep that cannot see reads behind an `OpenKV` handle, a closure, or a shared `internal/**` package. `clinic-patients` was in fact *retired* from KV precisely as a disclosure fix. | `cmd/clinic-app/patients.go:119-131`; `cmd/loftspace-app/tasks.go:113`, `one_bill.go:135`; `cmd/cafe-app/server.go:135`, `cmd/wellness-app/server.go:145` | **Folded.** Table corrected, C2 rewritten as a **call-graph** sweep with a corrected 9–12-per-app expectation, and the table explicitly demoted to "a corrected starting point, not an authority". |
| **F8** | **An empty `SubscribeAllow` is subscribe-EVERYTHING** — `RenderConf` gates on `!= nil`, the server parses `[]` to a nil allow-sublist, and `canSubscribe` short-circuits to allowed. The proposed gate ("does not contain `>`") passed it. | `matrix.go:624`; `server/client.go:3266-3267`, `:1094` | **Folded.** `len(...) > 0` required in both `RenderConf` and the gate, plus a live positive control (§4.5, §11). Noted in the doc that **the matrix documents this exact fail-open class twelve lines from the field** (`matrix.go:541-545`, `WebsocketAllowedOrigins`) and the first draft did not apply it. |
| **F9** | **The payoff was UNDER-stated**: the apps' `subscribe { allow: [">"] }` is a live full-account tap, and sensitive mutations cross `ops.>` in **plaintext** because encryption is at commit step 6.5. | `deploy/nats-server.conf:210`; `internal/processor/commit_path.go:374-386` | **Folded, and it moved the headline.** §For-Andrew ¶2 now leads with the in-flight plane; §4.4 and §11 cover it. This is also what breaks the 2026-08-06 shelving precedent's transfer (§8) — the containment premise the first draft conceded is simply false. |
| **F10** | §8 over-claimed "shares the word and nothing else" versus the shelved design; the *reasoning* transfers even though the subject does not. | `declared-read-scope-authorization-design.md:24-53` | **Folded.** §8 now states what transfers and names the two things that break it. |
| minors | bare `DIRECT.GET.KV_B` grants history reads no caller needs; `Allow()`'s bootstrap early return is a trap for a later derivation; the blanket-grant check must be wildcard-overlap not string equality; `ExtraPubDeny` can void a declared read | — | **All folded** into §4.2 / §4.5. |

**Paths the pass confirmed are NOT reachable** (recorded so a later fire does not re-litigate them): `$SYS.>`,
`$SRV.>`, `STREAM.LIST`/`NAMES`, `CONSUMER.LIST`/`INFO` and `STREAM.INFO` on undeclared streams (all closed
once `$JS.API.>` goes); mirror-direct subjects (no stream in this repo is a mirror); `RePublish` (configured
nowhere); `$JS.FC.>` (empty acks, returns no data); the auth-callout path (component keys are in `auth_users`
precisely so they use the static permissions); and `_INBOX.>` sufficiency — `nats.CustomInboxPrefix` is set
only by `cmd/edge`, `cmd/facet` and one script, never by a vertical app.

**What I take from this pass, beyond the fixes.** Two of the ten findings (F1 and F6) are the same failure:
**I cited a comment as evidence for behavior.** `matrix.go`'s "not a data-plane
privilege", the "PHI is in Postgres" mental model, and the ledger's own G17 row all read as settled facts and
none had been checked against the code that decides. The ledger rule — *cite the code that does the thing,
never the comment that describes it* — was in the skill, and I applied it to the vendor citations (which the
audit found clean) and not to the in-repo ones. F8 is the sharper version: the fail-open lesson was written
**twelve lines from the field I was adding**, and I still shipped the draft with it.
