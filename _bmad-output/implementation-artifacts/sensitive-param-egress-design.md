# Sensitive-param egress — the `egressReads` disposition + bridge egress unwrap

**Status: ✅ RATIFIED (Andrew, 2026-07-10).** Both §For-Andrew decisions approved as recommended:
(1) the bridge joins the `lattice.vault.decrypt` allowlist — Fire 2 pairs the grant with the core-kv
read-deny + a natsperm read vector; (2) refs stay trusted at the package-DDL boundary — Processor-MAC'd
refs are the named follow-on, triggered before AI-authored DDLs ship (AI-caps Fire 4). The #2/#3/#10
contract edits are committed with this ratification. Ratification DD folded two corrections
(§1.1 capability-author live templates; the #2 cell's "never the key envelope" wording). The Lattice
Steward builds Fires 1–2. Author: Winston (Designer fire, 2026-07-10).
Backlog row: `planning-artifacts/backlog/lattice.md` → *External-I/O maturity → Adapter read-seam /
richer params* (★★, S–M) — the row's `🚧 blocked-on: Designer (Starlark sensitivity-detection
primitive)` gate. Parent design: `adapter-read-seam-subject-templated-params-design.md` (✅ ratified
2026-06-28; Fire 1 shipped; Fires 2–3 blocked by the 2026-07-06 grounding finding this design resolves).

---

## For Andrew (one-look ratification block)

**What it does, in two lines.** An externalTask param template over a Vault-sensitive aspect
(`subject.ssn.data.value`) stops resolving to plaintext-in-the-event-log: the Processor hydrates such
reads as **sensitive-refs** (ciphertext, never plaintext) under a new `contextHint.egressReads`
declaration class, and the **bridge** unwraps them just before the adapter call — fetching the identity's
**live** key envelope from the shipped `piiKeyEnvelope` lens and calling the existing
`lattice.vault.decrypt` RPC — so a vendor gets the real SSN/DOB while the durable `events.external.>`
stream carries only what Core KV already stores (ciphertext), and a shredded identity's PII cannot
egress (the live envelope is the shred gate — restart- and replay-proof, §3.5).

**The headline design call — there is deliberately NO "Starlark sensitivity-detection primitive."** The
blocking finding asked for a way for the resolver helper to *tell* an aspect is sensitive. I designed that
option through (§6-A) and rejected it: any scheme where Starlark must *check* is **default-open** — a
forgotten check silently bakes plaintext SSN into the durable event stream, and nothing errors. Instead
the sensitivity decision stays **Processor-side, where the DDL registry already lives** (`ref.Sensitive`,
the same lookup `decryptSensitiveDoc` does today): for reads declared under `egressReads`, a sensitive
aspect is **never decrypted into Starlark at all**. The script cannot leak what it never saw — the
structural fail-closed, per the D1 lesson (a security default must deny on omission).

**Two decisions for your call (both designed through, my recommendation first):**

1. **The bridge joins the `lattice.vault.decrypt` allowlist** (today: Loupe only). This is the one real
   privilege widening, and I state its blast radius honestly (the adversarial pass corrected my first
   draft): the RPC trusts callers wholesale (no in-handler identity check — pre-existing posture, same as
   Loupe and the apps' wrap/unwrap; `internal/vault/service.go`), and the natsperm matrix denies core-kv
   **writes only** — every account today can *read* any KV via `$JS.API.DIRECT.GET`
   (`deploy/gen-dev-nkeys/main.go:405,43-53`). So a compromised bridge with this grant could decrypt the
   identity corpus — the same power a compromised Loupe has today. I still recommend the grant: the
   bridge **is** the platform's designed PII-egress boundary, the alternatives are strictly worse
   (§6-B/C), and Fire 2 **pairs the grant with a read-tightening** — pub-deny the bridge on
   `$JS.API.DIRECT.GET.KV_core-kv.>` + core-stream `MSG.GET` (allowing the one lens bucket §3.5 needs),
   proven by a **read** vector, so the grant's reachable set really does shrink to what ops hand it.
   (The account-wide read-side laxity beyond the bridge is the open `natsperm-matrix-hygiene` board row —
   flagged, not silently absorbed here.)
2. **Refs are trusted at the package-DDL boundary, not countersigned.** A malicious DDL could hand-craft a
   `$sensitiveRef` for a *different* identity's aspect. I recommend accepting this today because it adds
   **no new power**: a malicious DDL can already declare any identity's sensitive aspect in plain
   `contextHint.reads` and receive **plaintext** (§3.6). The real boundary is package-install trust, as
   today. The hardening follow-on (Processor-MAC'd refs) is named with its trigger — **before AI-authored
   DDLs ship** (AI-caps Fire 4, itself Andrew-gated) — not built now (dead scaffolding otherwise).

**Frozen-contract changes — three small edits, staged UNCOMMITTED in `main` (the diffs are the
proposal):** #2 §2.3/§2.5 (the `egressReads` declaration class), #10 §10.5 loom-shard (sensitive
templates resolve to refs; bridge is the unwrap point), #3 §3.10 (bridge egress-unwrap named as a
sanctioned Vault-decrypt consumer + the external-event plaintext emission guard). Details §4. (The dirty
`docs/contracts/11-*` in the tree is the multi-credential design's — untouched by this fire.)

**Unblocks:** the parent row's Fires 2–3 (collapsed into this design's Fire 2), i.e. the first real
subject-PII adapter payload, with the lease-signing e2e as the live consumer.

---

## 1. Problem & intent

### 1.1 The blocking finding (grounded, verified this fire)

The parent design's Fire 2 ("template `.demographics` fields into the backgroundCheck adapter call") died
on grounding (2026-07-06): the as-built schema has no `.demographics` — the real aspects are `.name`,
`.email`, `.phone`, `.ssn`, `.dob`, and **every one is `sensitive: true`**
(`packages/identity-domain/ddls.go:57-63`). Meanwhile decrypt-on-read is a Processor middleware applied to
**any** op's hydrated or lazy read (`internal/processor/sensitive_decrypt.go:20`, called from
`step4_hydrate.go:179,215` and the lazy seam `starlark_kv.go:334`) — so the shipped Fire-1 resolver
(`packages/orchestration-base/external_params.go`) would receive **plaintext** SSN/DOB and bake it into
the `external.<adapter>` event, which lands in the **durable `events.external.>` stream**. That is exactly
the exposure the parent design's Fire 3 existed to prevent — live risk since Vault shipped, so the row was
blocked rather than built. Verified today (ratification DD corrected the first draft's "no template exists" claim): the only live
`subject.*` templates are capability-author's `subject.request.data.{requesterId,intent,contextRef}`
(`packages/capability-author/patterns.go:34` — AI-caps Fire 1, 2026-07-04) — over a **non-sensitive**
aspect, and lease-signing's live params is the literal `{family}` — so nothing is exposed *now*; the
mechanism is simply unusable for every field a real vendor needs (all of identity's are sensitive).

### 1.2 Intent

Make subject-templated params work for the fields that actually exist — which are all sensitive — such
that (a) plaintext PII never enters the **external-egress plane** (the `events.external.>` payload, the
claim vertex, the adapter inputs at rest) — the general "no sensitive plaintext in *any* emitted event"
invariant remains the DDL-trust boundary it is today, §3.6; (b) the vendor call receives real plaintext
at the last possible moment, at the platform's designed egress boundary, (c) a crypto-shredded
identity's PII cannot egress — including across Vault restarts and event replays, and (d) no per-script
vigilance is required — the guarantee is enforced by the Processor and the transport, not by Starlark
discipline.

### 1.3 Provenance

Brainstorm #48 (external adapter framework) via the ratified parent design; the Vault design
(`vault-crypto-shredding-design.md`) names *"a bridge adapter sending PII to a vendor"* as a planned
plaintext consumer; Contract #3 §3.10 already frames plaintext as producible only by the Processor and
*"an explicit Vault-decrypt consumer"* — this design makes the bridge that consumer, explicitly.

## 2. Grounding — what exists (the pattern this extends)

- **Sensitivity knowledge is already centralized Processor-side.** One function, `decryptSensitiveDoc`,
  keyed off the DDL cache's `ref.Sensitive`, with exactly two call sites (step-4 hydrate for
  `reads`/`optionalReads`; the lazy `kv.Read` seam). This design adds a *disposition branch* at that same
  chokepoint — it does not create a second sensitivity authority.
- **The declaration-class taxonomy is already extensible.** `contextHint` = `reads: string[]` (fail-closed)
  + `optionalReads: string[]` (absence-tolerant) + `enumerations` (metadata) — `envelope.go:46-62`, frozen
  in #2 §2.3/§2.5 with lettered read classes. `egressReads` is a fourth sibling in the same shape, the same
  extension move the script-read-posture design made for `optionalReads`.
- **The Vault decrypt RPC already takes everything from the caller.** `DecryptRequest{identityKey,
  envelope, ciphertext}` (`internal/vault/service.go:56-65`) — *"the caller supplies everything the Vault
  needs"*. A ref carrying ciphertext + envelope inline therefore needs **zero Vault-side change** and zero
  bridge read surface. The shred gate is the backend's live shredded-set OR'd with `envelope.Shredded`
  (`internal/vault/local.go:330`) — authoritative regardless of how stale the caller's envelope copy is.
- **The allowlist-grant precedent.** Loupe holds `lattice.vault.decrypt`; the vertical apps hold
  wrap/unwrap (`deploy/gen-dev-nkeys/main.go:229,261`). The bridge today holds neither, and is
  **pub-denied on `$KV.core-kv.>`** (`main.go:168`) — which this design preserves.
- **The bridge chokepoint.** All adapter inputs assemble at one point — `dispatch.go:181` builds
  `Request{Params: coerceParams(ev.Params), RawParams: ev.Params}` and calls `executeAdapter`. Note
  `coerceParams` keeps only string values (`dispatch.go:378`), so unwrap must run **before** coercion.
  The bridge is deliberately subject-blind (the event carries no subject key; `dispatch.go:25-28`).
- **Shipped Fire 1** (parent design, Mechanism 2/2a): `inferExternalTaskReads` declares template aspect
  keys by pure string parsing (`internal/loom/externaltask_params.go`); the shared
  `resolve_subject_params` Starlark helper resolves them from hydrated state, failing loudly on
  absent/null. Both are extended, not replaced.

## 3. The shape

### 3.1 `contextHint.egressReads` — a declaration class, not a privilege

A fourth contextHint list, `egressReads: string[]`: known aspect/vertex keys the op reads **for external
egress**. Semantics:

- **Hydration:** identical to `reads` (fail-closed, missing key ⇒ `HydrationMiss`) **except** at the
  decrypt branch: when the key's DDL class is `sensitive: true`, the Processor does **not** decrypt.
  Instead the doc hydrates with `data` replaced by a ref-marker (§3.2). A **non-sensitive** key under
  `egressReads` hydrates exactly like a plain read — the disposition is *ref-if-sensitive*, so callers
  need no sensitivity knowledge to choose the list.
- **The lazy seam honors it too:** `connKVReader` consults the op's egress set before `decryptSensitiveDoc`
  — one disposition, both read paths (the same two call sites the decrypt already has).
- **Validation:** a key in both `reads` and `egressReads` is an envelope parse error (reject at
  `envelope.go` validation — ambiguous intent, refuse loudly). `optionalReads` × egress is not offered:
  a param template's target is by definition required (absence is already a loud data error in the
  helper), and an unneeded option is scaffolding.
- **It grants nothing.** `egressReads` is strictly *less* information than `reads` (refs instead of
  plaintext for sensitive keys, identical otherwise) — a submitter self-restriction, not a new authz
  surface. That is why it can be an open declaration like the other classes.

**Why per-read, not per-op:** the instanceOp legitimately mixes dispositions — the subject root stays a
normal read (the `vertex_alive` check needs it), only template-inferred aspects are egress. A DDL-level
flag would force the coarse choice; per-read is the same cost and strictly more precise.

### 3.2 The sensitive-ref (what hydrates, what rides the event)

For a sensitive aspect under `egressReads`, the hydrated doc's `data` becomes:

```json
{ "$sensitiveRef": {
    "ref":        "vtx.identity.<id>.ssn",
    "ciphertext": { "ct": "...", "nonce": "...", "keyId": "..." }
} }
```

- `ciphertext` is the aspect's at-rest `data` verbatim (what any direct reader already observes, §3.10).
- **The key envelope is deliberately NOT carried** (the adversarial pass killed my first draft's inline
  envelope as a shred-durability regression): the `LocalBackend` shredded-set is **in-memory,
  restart-volatile** (`internal/vault/local.go:46,128` — no rebuild), so a pre-shred `WrappedDEK` frozen
  into a durable event would decrypt again after a Vault restart + event redelivery — post-shred PII
  egress. The restart-proof shred signal is the **live `piiKey` aspect itself**, which `ShredKey`
  rewrites to a dud placeholder (`Shredded: true`, empty `WrappedDEK`); every existing consumer reads it
  live (`readPiiKeyEnvelope` per-op; loftspace via the `piiKeyEnvelope` lens), and this design follows:
  **the envelope is always resolved live at decrypt time, never from a stored copy** (§3.5; the rule is
  added to the §3.10 contract edit so no future consumer re-makes my mistake).
- **Ref-marker authoring needs at-rest state, not a live Vault backend** — a DDL lookup + the ciphertext
  already in hand. (It does *not* work for an identity with no `piiKey` provisioned — but neither does
  anything else sensitive; there is nothing to egress and the bridge's envelope fetch fails loudly.)
- **Representation-follows-use:** `ref` is the dereferenceable address (the bridge derives
  `DecryptRequest.identityKey` = the vertex key, and fetches the live envelope by it) and the audit/log
  label; `ciphertext` rides inline because the op's OCC snapshot already holds it and projecting it
  anywhere else would add read surface for no gain. (The parent design's §7 sketch carried `keyId` alone
  — superseded; see the banner added there.)
- **Durable-plane posture:** the event stream gains a *copy of ciphertext* — an already-at-rest artifact
  any substrate reader can reach in Core KV today; the substrate is the existing trust boundary (the
  dedup design's §10-C argument). Post-shred the copy is permanently inert — the live envelope it must be
  decrypted with is a dud, regardless of Vault restarts or `DeliverAll` durable-consumer replays (the
  bridge's consumer is replayable by design; adapter idempotency memoizes pre-shred replays, and the
  live-envelope gate kills post-shred ones).

### 3.3 The resolver helper — shape the ref, add the field

`resolve_subject_params` gains one branch, **checked before** the field lookup: if the node's `data`
carries `$sensitiveRef`, the token resolves to `{"$sensitiveRef": {…as hydrated…, "field": "<field>"}}` —
the requested field name is appended for the bridge's post-decrypt extraction, and the helper's
absent-field failure does not apply (the plaintext fields are legitimately not there). Everything else is
unchanged, including loud failure on absent/tombstoned keys. The helper still contains **no sensitivity
check** — it pattern-matches a marker the Processor authored; if the Processor hydrated plaintext (a plain
aspect), the marker is absent and plaintext resolves as today.

### 3.4 Loom — same inference, second list

`inferExternalTaskReads` splits its output: subject root → `reads` (unchanged, `vertex_alive`);
template-inferred aspect keys → **`egressReads`** (was: appended to `reads`). Still pure string parsing,
still no Core-KV read in Loom, still deterministic ordering. Loom's `contextHint` struct
(`actuator.go:34,100`) gains the field. The lease-signing read-set drift-guard updates to pin the split
(subject root in `reads`, template aspects in `egressReads` — a template added without its egress
declaration fails hydration-closed, the safe direction). Scope note (wider than one struct): the field
threads `contextHint` (`actuator.go:38`) → `outboxRecord`/`buildOutbox` (`actuator.go:100,130`) → the
relay envelope → the Processor's `OperationEnvelope` parse + validation (`envelope.go:46`) — each hop a
place the set could silently drop; the §7 plumbing test pins the full chain.

### 3.5 The bridge egress unwrap

At the `dispatch.go:181` chokepoint, before `coerceParams`: walk `ev.Params`; for each value carrying
`$sensitiveRef` → derive `identityKey` = the vertex key of `ref` (parse `vtx.identity.<id>.<aspect>`;
**MUST be a well-formed identity-anchored aspect key — anything else fails the dispatch**, mirroring
`decryptSensitiveDoc`'s own anchor check) → fetch the identity's **live** key envelope from the
**`privacy-pii-key-envelopes` lens bucket** (the exact `fetchPiiKeyEnvelope` pattern loftspace-app ships,
`cmd/loftspace-app/objects_crypto.go:35` — a P5 lens read, keyed by the identity vertex key) → call the
existing `lattice.vault.decrypt` RPC with `{identityKey, envelope, ciphertext}` → parse the plaintext
JSON map → extract `field` → substitute the scalar into the params. Then coerce and dispatch as today.
Properties:

- **Fail-closed, loudly — split by failure class** (my first draft hand-waved "the deadline backstop
  surfaces it"; the adversarial pass proved that backstop disarms at instanceOp commit and the sync path
  has no timeout — a forever-Nak would park the pattern unbounded):
  - **Permanent failures** — shredded key (dud live envelope / `ErrKeyShredded`), malformed or
    non-identity ref, absent/non-scalar field, absent envelope row: **do not retry.** The bridge posts
    the terminal `replyOp` with a **failed outcome** through the same structured-result path an adapter
    failure takes, so the Loom instance converges to its failure branch (FR29: converge, never park
    forever) + a Health issue. Never a blank/garbage field to a vendor (§10.8 null-is-data-error).
  - **Transient failures** — Vault RPC error/timeout, envelope-lens row not yet projected for a
    just-created identity: `NakWithDelay` (the existing redelivery posture; the adapter is never
    reached, so retry is side-effect-free), with the bridge's bounded-attempts give-up escalating to the
    permanent path.
- **Shred-correct by construction — restart- and replay-proof:** the decrypt input is the **live**
  envelope, which a shred rewrites to a dud at the source; a shred between op commit and egress, a Vault
  restart (in-memory shredded-set lost), or a `DeliverAll` durable replay of an old event all fail the
  decrypt. The only residual is CDC projection lag on the envelope lens (sub-second p99 budget): a
  decrypt in that window uses a pre-shred envelope — bounded, and covered by the Vault's in-memory
  shredded-set whenever the Vault has not restarted in that same sub-second window.
- **Plaintext lifetime = the adapter call.** In bridge memory only; `Request.Params`/`RawParams` are
  handed to the adapter and dropped. The bridge logs refs (`ref`, `field`), never values.
- **Type-agnosticism preserved:** the bridge parses the *ref key* (a Contract #1 shape, same as the
  natsperm/objectcrypto key parsers it already lives beside), not the claim-vertex type; the
  `InstanceKey`-is-opaque discipline is untouched.
- **Async adapters:** unwrap runs at `Dispatch` (the one leg that talks to the vendor); `Poll` carries the
  vendor's own reference, no params re-resolution — no change.

### 3.6 The emission guard + the trust boundary (what is and isn't defended)

**New commit-path guard (Fire 1):** an op that emits an `external.*`-domain event AND decrypted any
sensitive aspect this execution (plain `reads`, `optionalReads`, or lazy `kv.Read`) is **rejected at
validation** — sensitive data may reach an external event only as a ref via `egressReads`. This makes the
plaintext-in-event-log leak *structurally impossible* for the **external-egress plane**, independent of
engine or DDL correctness. Scope stated honestly: the guard does **not** police non-`external.*` events —
an op can still decrypt PII and emit a derived value in an ordinary domain event; that is today's
DDL-trust surface, unchanged (extending the guard to all events would need value-matching — Alternative
F's rejected fragility). The **runtime guard is the enforcement**; the Fire-1 migration grep of
`external.*`-emitting DDLs is best-effort pre-flight only (it cannot see unannotated lazy `kv.Read`s).
The adversarial pass spot-checked the live emitters — clinic-reminders' `external.notification` carries
appointment fields, augur/capability-author carry gap/proposal context, lease-signing reads the subject
root + a literal — so the guard over-blocks **no shipped op**.

**One expressiveness cliff, accepted deliberately:** `reads` × `egressReads` mutual exclusion + the guard
means a single op cannot both *derive from* a sensitive plaintext and *egress* a sensitive ref. No shipped
or designed op needs that composition (write-time PII derivation — e.g. the dedup design's index probe —
lives in ops that emit no external event); the sanctioned pattern if one appears is **two ops** (derive,
then dispatch), and a flow-scoped guard is the future relaxation, not built now.

**What remains trusted:** a *malicious* package DDL can still (a) hand-craft a `$sensitiveRef` naming a
different identity's aspect (the bridge would unwrap it — it cannot bind refs to a subject the event
doesn't carry, and an event-carried subject would be attacker-controlled by the same DDL anyway), or
(b) exfiltrate via non-external channels (copy plaintext into a plain aspect via ordinary reads). Both are
**today's boundary, not a new one**: any DDL can already declare any sensitive key in `reads` and receive
plaintext. Package-install trust is the enforcement point, as it is for every DDL power. **Named
follow-on with a trigger:** Processor-authored refs gain an HMAC (a real provenance transport, verified
Vault-side at decrypt) *before AI-authored DDLs can ship* (AI-caps Fire 4 — its deterministic-validation
gate should also lint AI-authored scripts for `$sensitiveRef` literals and sensitive-key read
declarations). Not built now: its consumer (untrusted DDL authorship) does not exist yet.

### 3.7 Invariants

**P2** — every Core-KV read stays inside the Processor (hydration + lazy seam); Loom still string-parses
only; the bridge reads no **Core KV** — its inputs arrive in the event, and its two new transport
surfaces are one RPC subject plus one **lens-bucket** read. **P5** — honored, not bent: the bridge's
envelope read is a lens-projection read (`privacy-pii-key-envelopes`), the sanctioned app-plane pattern
(loftspace precedent); no PII rides any read model (the envelope is non-secret key metadata, exactly
what that lens already serves apps). **Contract #1** — refs carry canonical 4-segment aspect keys;
nothing new minted. **D5** — the claim vertex is untouched; params ride the event only, and any DDL that
persists params persists refs. **No-scans** — all reads are known-key.

## 4. Contract surface (all edits staged UNCOMMITTED in `main`; diffs are the proposal)

| Contract § | Change vs build-to | Detail |
|---|---|---|
| **#2 §2.3 envelope table + §2.5** | **CHANGE (staged)** | `contextHint` gains `egressReads: string[]`; §2.5 gains class **(f) egress reads** — hydrated fail-closed like (a), but a sensitive-DDL key hydrates as a sensitive-ref, never plaintext; key-in-both-lists = parse error. |
| **#10 §10.5 (loom shard) — externalTask `params`** | **CHANGE (staged)** | The params paragraph gains the sensitive rule: a template over a sensitive aspect resolves to a sensitive-ref (plaintext never enters the event); the bridge unwraps at egress via the Vault decrypt RPC; unwrap failure = data error, do not dispatch. Loom declares template aspects under `egressReads`. |
| **#3 §3.10** | **CHANGE (staged)** | Three additions: (1) the bridge's external-egress unwrap joins the named Vault-decrypt consumers; (2) the emission guard — an op emitting an `external.*` event must not have decrypted sensitive plaintext, sensitive egress rides refs; (3) the **live-envelope rule** — a decrypt consumer resolves the key envelope from the identity's current `piiKey` state (aspect or its lens projection) at decrypt time, **never from a stored/carried copy** (a frozen envelope defeats crypto-shred across a Vault restart). |
| #4 idempotency / #10 §10.6 completion | build-to | Unwrap is inside the existing dispatch; idempotency, correlation, async SPI unchanged. |
| Bridge `external.<adapter>` envelope (`bridge.md`, package data) | build-to | `params` stays a free-form map; `$sensitiveRef` is a value convention, not an envelope field. |

The `deploy/gen-dev-nkeys` bridge-user grant (`lattice.vault.decrypt`) is code, not contract — Fire 2,
with a natsperm vector proving the bridge still cannot touch `$KV.core-kv.>`.

## 5. Reconciliation with the existing mental model

- **"Didn't we already design this?"** Partially — the parent design's §7 sketched "sensitive-ref +
  bridge-side Vault unwrap" and its §11-3 left *bridge vs Secure Lens* open. This design supersedes the
  sketch with the grounded mechanism (the §7 sketch predates the shipped Vault: it assumed hydration
  returns ciphertext — false, decrypt-on-read is unconditional — and left the fetch/unwrap transport
  unnamed). A supersession banner is added to the parent §7 this fire.
- **"The Vault design said PII consumers go through the Secure Lens."** For *read-path* consumers (apps
  displaying PII). This is a *write-path* dispatch whose op already holds the at-rest bytes at hydration;
  routing it through a per-identity RLS-Postgres read-model would add a projection dependency, a lag
  window, a bridge Postgres credential, and a shred race — for data the op snapshot already carries
  (§6-B). §3.10's "explicit Vault-decrypt consumer" is the frame that fits, and the staged edit makes it
  explicit rather than implied.
- **"Does this duplicate the wrap/unwrap machinery loftspace uses?"** No — it reuses it: same RPC family,
  same allowlist mechanism, same caller-supplies-everything protocol; the bridge becomes the third
  allowlisted caller class. New state introduced: none (no new buckets, vertices, links, or lenses; one
  new envelope list + one value convention).

## 6. Alternatives considered

- **A — Starlark sensitivity primitive** (`kv.IsSensitive(key)` / a `.sensitive` node attr; what the
  blocking finding literally asked for): default-**open** — the guarantee lives in every script
  remembering the check; one forgotten call = silent plaintext in the durable stream. Also spreads
  security metadata into Starlark against the "Starlark stays pure" posture. A helper-internal check
  variant narrows but doesn't close it (any script bypassing the helper leaks). Rejected — could a
  variant beat the recommendation? Only by adding the emission guard anyway, at which point the primitive
  is redundant: the guard + disposition already decide correctly with zero script involvement.
- **B — bridge reads the Secure Lens** (Vault design's generic consumer frame): needs D1-gated Phase-B
  machinery per package, a bridge Postgres/RLS credential, tolerates projection lag on a dispatch path,
  and re-opens the shred race the live shredded-set closes. Heavier in every dimension.
- **C — bridge reads Core KV + holds a local Vault/KEK:** spreads the master KEK to a second binary and
  breaks the load-bearing *"only `processor` may publish/read core-kv"* transport posture
  (`gen-dev-nkeys/main.go:95`) for no gain over the RPC. Rejected outright.
- **D — per-op DDL flag** ("this op's sensitive reads hydrate as refs"): coarser than per-read for equal
  cost; the instanceOp mixes dispositions (§3.1). Rejected.
- **E — ref-only (nothing inline), bridge fetches both:** for the **ciphertext**, rejected — it would
  need a new ciphertext-projection lens + bucket grants to avoid carrying bytes that are already
  at-rest-visible, and freshness buys nothing (the envelope, not the ciphertext copy, is the security
  gate). For the **envelope**, this alternative *won* on adversarial review (my draft had it inline; a
  frozen envelope defeats shred across a Vault restart, §3.2) — and it needs **no new surface**: the
  `privacy-pii-key-envelopes` lens already ships with the exact reader pattern (loftspace). The final
  shape is the hybrid: ciphertext inline, envelope live.
- **F — emission-time redaction** (scan outgoing events for hydrated sensitive values): byte-matching
  fails on any transform; magical. Rejected — but its *deterministic* core survives as the §3.6 guard,
  which keys on *what was decrypted*, not on value matching.

## 7. Migration & test strategy

**Migration: one mechanical mover, zero package edits.** capability-author's `subject.request.data.*`
templates (§1.1, non-sensitive) re-class from `reads` to `egressReads` automatically when Loom's
inference splits — the templates live in the pattern, the classing lives in `inferExternalTaskReads`,
so no package changes; hydration is byte-identical (ref-if-sensitive leaves plain aspects untouched)
and no drift-guard pins its read set (lease-signing's is the only one, updated in Fire 1). It becomes
Fire 1's live exerciser of the non-sensitive egress path. `egressReads` absent = today's behavior
byte-for-byte; the emission guard's migration grep (§3.6) is expected clean. No
bootstrap bump (no new kernel entity; contextHint is envelope shape, parsed leniently by version).

**Tests (the proof set):**
- *Processor:* egress hydration table — sensitive→ref (both step-4 and lazy seam), plain→plaintext,
  key-in-both→parse reject, missing→`HydrationMiss`, ref authoring without a Vault backend (piiKey aspect
  seeded — the property is "no live Vault needed," not "no key state needed"); emission guard —
  external-event + sensitive-plaintext-read rejects, external-event + egress-ref commits, non-external
  event + sensitive read commits (the deliberate scope bound, §3.6).
- *Loom:* inference split (root→reads, aspects→egressReads), determinism, drift-guard update, and the
  egress set surviving the full outbox plumbing (`outboxRecord` → `buildOutbox` → relay envelope →
  Processor parse — the field must not silently drop at any hop).
- *Helper:* ref pass-through with `field` appended; marker checked before absent-field failure; plain
  fields unchanged.
- *Bridge:* unwrap happy path (live envelope from the lens bucket); the **permanent** arms
  (shredded/dud envelope, malformed/non-identity ref, absent field, absent envelope row) post the
  terminal failed `replyOp` — the pattern **converges** to its failure branch, never parks; the
  **transient** arms (RPC timeout, envelope not yet projected) Nak-with-delay then escalate on the
  attempts bound; unwrap precedes `coerceParams`.
- *e2e (the live consumer):* lease-signing backgroundCheck declares `subject.name.data.*` /
  `subject.dob.data.*`; the ephemeral-stack convergence test asserts (1) `FakeBackgroundCheck` received
  plaintext, (2) the `events.external.>` message carries only `$sensitiveRef` values — no plaintext
  anywhere durable, (3) green convergence; a shred-then-dispatch arm asserting the vendor call is refused
  and the pattern converges failed; and a **shred → Vault-restart → event-replay arm** asserting the
  replayed dispatch still refuses (the live-envelope gate — the B1 regression pinned forever). Natsperm
  vectors: bridge gains exactly `lattice.vault.decrypt`; core-kv **write** denial unchanged; and the new
  **read denial** (`$JS.API.DIRECT.GET.KV_core-kv.>` / core-stream `MSG.GET`) proven by a read vector,
  with the envelope-lens bucket read still allowed.

## 8. Risks & edges

- **Unknown DDL class under egressReads** hydrates as plain (can't be sensitive without a DDL); if the
  at-rest bytes were ciphertext anyway, the helper's absent-field check fails loudly — the failure
  direction is noise, never leak.
- **Envelope-lens projection lag** — the one shred residual: a decrypt inside the CDC lag window
  (sub-second p99) can use a pre-shred envelope; covered by the Vault's in-memory shredded-set except
  when the Vault restarted inside that same window. Accepted with its bound stated (§3.5); the
  alternative (synchronous envelope read from core-kv by the bridge) would trade it for a standing
  core-kv read exception — worse.
- **`RawParams` still carries refs post-unwrap** — adapters reading `RawParams` (docGen) see refs, not
  plaintext; the unwrap substitutes into the params map the coercion consumes. Adapters needing raw
  sensitive JSON don't exist; if one appears it forces an explicit design, which is correct.
- **Blast radius of the decrypt grant** — a compromised *bridge process* holds a wholesale-trust decrypt
  RPC, and KV **reads** are account-open today (write-denial only), so without Fire 2's read-tightening
  its reachable set would be the whole identity corpus (same as Loupe's today). Fire 2 pairs the grant
  with the read denial + a read vector (§For-Andrew #1); the in-handler caller↔identity check remains a
  pre-existing platform gap shared with Loupe/apps — flagged, not silently widened.

## 9. Fire-by-fire decomposition (for the Lattice Steward)

| Fire | Scope | Review depth |
|---|---|---|
| **Fire 1 — the disposition + guard (Processor + Loom + helper)** | `egressReads` parse/validate/hydrate (both seams) + ref-marker authoring; the §3.6 emission guard + migration grep; Loom inference split + drift-guard; helper ref-shaping. Live immediately (all template-inferred reads move to the new class; plain aspects behave identically). Contract #2/#3/#10 edits ride the ratification commit. | **Full 3-layer** — security-plane commit-path change. |
| **Fire 2 — the egress unwrap + the consumer (bridge + lease-signing)** | Bridge unwrap at the dispatch chokepoint: live envelope-lens fetch, permanent-vs-transient failure split incl. the terminal failed `replyOp` convergence path; `lattice.vault.decrypt` grant **paired with the core-kv read denial** + read/write natsperm vectors; lease-signing templates + the §7 e2e incl. both shred arms. Closes the parent row (its old Fires 2+3 collapse here). | **Full 3-layer** — PII egress + a permission widening. |

Sequencing: Fire 2 strictly after Fire 1 (refs must exist before the unwrap has input). No dark increments:
Fire 1's mechanism is exercised by every existing template-inferred read path and its own guard; Fire 2
ships with the live e2e consumer.

## 10. Adversarial pass (run this fire, independent agent — findings folded)

The pass verified every mechanism claim against code and killed two of my draft's load-bearing choices:

- **B1 (BLOCKER, folded → §3.2/§3.5/§6-E):** the draft carried the key envelope inline in the ref. The
  `LocalBackend` shredded-set is in-memory and restart-volatile, and shred is a deny-list, not KEK
  destruction — so a frozen pre-shred `WrappedDEK` + a Vault restart + a durable-consumer replay would
  decrypt a shredded identity's PII. **Fixed:** envelope is never carried; the bridge fetches it live
  from the `piiKeyEnvelope` lens (shred rewrites the source to a dud — restart/replay-proof); the
  live-envelope rule is added to the §3.10 edit; a shred→restart→replay e2e arm pins it.
- **B2 (BLOCKER, folded → §For-Andrew #1/§8):** the draft claimed the bridge "cannot reach ciphertext"
  because core-kv is transport-denied — false: the natsperm denial covers **writes only**; reads via
  `$JS.API.DIRECT.GET` are account-open. **Fixed:** blast radius stated honestly; Fire 2 pairs the grant
  with a bridge read-denial proven by a read vector; the account-wide read laxity is the open
  `natsperm-matrix-hygiene` row.
- **M1 (folded → §3.5/§7):** "the deadline backstop surfaces unwrap failure" was false — it disarms at
  instanceOp commit and the sync path never arms a bridge timeout; a forever-Nak would park the pattern
  unbounded. **Fixed:** permanent failures post a terminal failed `replyOp` (converge), transients Nak
  with a bounded escalation.
- **M2/M3/M4 (folded → §1.2/§3.6):** the "any durable plane" claim narrowed to the external-egress plane;
  the migration grep demoted to pre-flight (blind to lazy reads — the runtime guard is the enforcement);
  the derive+egress expressiveness cliff acknowledged with the two-op pattern as the sanctioned escape.
  The pass also spot-checked all live `external.*` emitters: the guard over-blocks no shipped op.
- **m1–m3 (folded → §3.2/§3.4/§7):** the "vault-less" ref-authoring property restated precisely (needs
  the piiKey aspect, not a live backend; absence fails loudly); "old events are never re-dispatched"
  struck (the consumer is `DeliverAll`-replayable — safety now rests on the live-envelope gate, not a
  false consume-once claim); the five-hop Loom plumbing pinned by test.

Checks that passed: coerceParams ordering, identityKey/AAD derivation, caller-supplies-everything
DecryptRequest, transient-retry idempotency (adapter never reached), async Poll untouched.
**The pre-build gate is discharged; no deferred gate is left for the Steward.**

## 11. Open items for Andrew — ✅ all resolved at ratification (2026-07-10)

1. `lattice.vault.decrypt` grant to the bridge user — **approved** (Fire 2, paired with the read-deny).
2. Ref-trust posture — **approved**; the MAC-before-AI-DDLs trigger stands as the named follow-on.
3. The three contract edits (§4) — **committed** with the ratification.

---

*Designed by Winston (Designer fire, 2026-07-10); ratified by Andrew 2026-07-10. The Lattice Steward
builds Fires 1–2.*
