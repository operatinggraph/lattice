# Vault + crypto-shredding — design

**Status: ✅ Andrew-ratified (2026-06-27).** Author: Winston (Designer fire, 2026-06-27).

> **Ratification decisions (Andrew, 2026-06-27):**
> 1. **Fork 1 (backend) — Path A:** pluggable `internal/vault.Vault` interface + a **local
>    envelope-encryption backend first**; production KMS adapters (HashiCorp Transit / AWS·GCP KMS) are
>    later pluggable backends (the Refractor adapter pattern). Same interface either way.
> 2. **Fork 2 (phasing) — REVISED: deliver Phase A + Phase B *together*, behind D1 (no Phase-A-now split).**
>    The original "Phase A now / Phase B behind D1" is **dropped.** Reasoning (Andrew, confirmed):
>    Phase A in isolation only serves PII consumed *solely* by the Processor's business logic; the moment
>    any **non-Processor** consumer needs plaintext — a **vertical app displaying** PII, or a **bridge
>    adapter sending** PII to a vendor — it needs the Secure Lens (Phase B → RLS-Postgres), which needs D1.
>    Pre-B you'd hack a per-app decrypt RPC (violates P5) or leave the field unencrypted (defeats the point).
>    Phase A is technically dormant/additive (nothing declares `sensitive:true` today), so it *realizes no
>    value* until real PII exists — and the first real PII use needs B + D1 anyway. With **no production
>    PII-at-rest exposure pressure** (single-user experiment, AI POs) there is no reason to ship a half-done
>    interim. So: **sequence the whole feature behind D1; build A+B as one coherent delivery.** (Phase-A-now
>    would only win under production at-rest-exposure pressure, which does not exist here.)
>
> **Two findings folded in from a renewed-skill re-review (2026-06-27):**
> - **GCM nonce-uniqueness grounding (§2.5):** the design relied on "random GCM nonce" without stating the
>   per-key message bound or citing primary guidance — the #1 GCM footgun. Now grounded (NIST SP 800-38D;
>   per-identity DEK keeps the message count far below the 96-bit-random birthday bound) + a DEK-rotation
>   posture.
> - **Sensitive-blob scope (§2.6, NEW):** Phase A/B encrypt sensitive *aspects* (Core KV) — **not** PII-bearing
>   *blobs* (lease PDFs, ID scans) in the Object Store, where document-PII often lives. Crypto-shred would
>   leave those recoverable. **Scoped OUT** of this feature; a follow-on **"crypto-shred for object-store
>   blobs"** item is filed on the Lattice board so the right-to-erasure claim is honest, not silently partial.
Backlog row: `planning-artifacts/backlog/lattice.md` → *Privacy / Vault → Vault + crypto-shredding*
(★★★, L). Grounds in `lattice-architecture.md` Items 5 & 6 (the pre-written PII rubric) + the Vault
SPOF / KMS decisions, Contracts #1/#2/#3/#5/#7 (the sensitivity hooks already wired), the Obsidian
*Brainstorm PII and Crypto-Shredding* subdoc, brainstorming inventory items #56–#62 / #120, and the
read-path-authorization (D1) design (the dependency for Phase B).

---

## For Andrew (one-look ratification block)

**What it does, in two lines.** Makes "right to be forgotten" real on an append-only ledger: aspects
marked `sensitive: true` are stored as **ciphertext** in Core KV (encrypted on write with a per-identity
key held outside Core KV), and a `ShredIdentityKey` operation **destroys the key** so the ciphertext —
in live KV *and* in the immutable JetStream history — becomes permanent gibberish, instantly removing the
PII from every downstream projection. The **sensitivity boundary already ships** (the `sensitive` DDL
flag, identity-anchoring at commit step 6, `SensitivityViolation`, the reserved Vault health metrics +
the privacy-critical failure tier); this design adds the **crypto layer** those hooks were built for.

**The safety insight that makes this shippable now (no D1 / no Edge):** decryption is **opt-in**. The
Refractor's default projection path copies the *ciphertext* it reads from Core KV — so sensitive aspects
are unreadable at every general lens target **by construction**, no read-path auth required. Plaintext is
produced in exactly two places: the **Processor** (decrypt-on-read into the Starlark context, so business
logic still works) and **trusted tools** (Loupe, via a Vault decrypt RPC — the same trusted-single-identity
model Loupe already runs under). The queryable-plaintext "Secure Lens" (the architecture's *blind
projection* rule) is the one piece that needs read-path auth — so it is cleanly deferred as **Phase B**
behind D1, and **Phase A delivers the full crypto-shred guarantee on its own.**

**Two forks I designed through — your call on both:**

1. **Vault backend (the KMS choice the architecture flagged for "week 1").** Key material must never live
   in Core KV (`lattice-architecture.md` line 988). Options:
   - **Path A — pluggable `Vault` interface + a local envelope-encryption backend first (my
     recommendation).** Define `internal/vault.Vault` (`CreateIdentityKey / Encrypt / Decrypt / ShredKey`)
     and ship a **local backend** using **envelope encryption** — a per-identity DEK wrapped by a single
     master KEK, the KEK sealed in config (env / file) for the dev + trusted-tool deployment, exactly the
     posture Loupe already runs under. Production **KMS adapters** (HashiCorp Vault Transit, AWS/GCP KMS)
     are pluggable backends added later — mirrors Refractor's adapter pattern (NATS-KV / Postgres). This
     unblocks the whole feature **without** committing to a cloud KMS now and keeps the crypto-shred
     guarantee real (shred = destroy the wrapped DEK + evict caches; for a real KMS, also destroy the KMS
     key version).
   - **Path B — commit to an external KMS/HSM now (HashiCorp Transit or AWS KMS).** Strongest custody
     immediately, but pulls a heavy integration dependency + ops surface into Phase 3 and contradicts the
     "trusted-tool, binds to one identity, runs on 127.0.0.1" framing the rest of Phase 3 lives in.
   - **My recommendation: Path A.** Same interface either way; the backend is swappable; the *fork* is only
     *which production KMS and when*, not the architecture.

2. **Phasing vs. D1 — [SUPERSEDED by the ratification block above: deliver A+B together, behind D1.]**
   *The original recommendation below ("Phase A now, Phase B behind D1") was **not** adopted — Andrew's
   reasoning (a Phase-A-only world strands every non-Processor PII reader, and there's no production
   exposure pressure to ship a half-done interim) is recorded at the top. Retained for context only.*
   ~~Phase A now, Phase B behind D1 (my recommendation).~~ Phase A (crypto layer +
   shred, ciphertext-safe everywhere) is independently valuable and **unblocked today**. Phase B (the
   Refractor **Secure Lens** that decrypts sensitive aspects into RLS-protected, *queryable* read models —
   the architecture's decision-of-record) needs read-path authorization to protect those plaintext rows,
   and D1 is itself only 📐 awaiting-Andrew. I recommend **ratifying + building Phase A now** and gating
   Phase B on D1's ratification — they compose, and Phase B's input (an authz-anchored protected lens) is
   exactly what D1 produces. The alternative is to hold all of crypto-shred until D1 lands; I think that
   needlessly delays the right-to-erasure guarantee, which stands alone.

**Frozen-contract change (uncommitted, staged as the proposal-diff).** One genuine change:
`docs/contracts/03-mutation-batch-event-list.md` gets a new **§3.10 — Sensitive-aspect encryption at
rest**. Today the contract is silent on storage format, so a `sensitive: true` aspect would land in Core
KV as **plaintext** (only its *anchoring* is enforced). §3.10 makes the observable invariant explicit:
the `data` of a sensitive aspect is stored **ciphertext** (encrypt-on-write after step-6 validation,
before the step-8 atomic commit), with the encryption envelope referenced by the anchoring identity's
`piiKey` aspect. Edited in `main`, **left uncommitted** for your ratification (the diff *is* the
proposal). Affected consumers: every direct Core-KV reader of sensitive aspects (Refractor — projects
ciphertext as-is; Loupe — decrypts via Vault RPC; the platform binaries). Everything else is **build-to**:
Contracts #1 §(sensitivity lookup), #2 (`SensitivityViolation`), #5 §5.4/§5.5 (`vault_calls_total`,
`keyshredded_handled_total`, `VaultUnreachable`), #7 (`sensitive` reserved aspect-type DDL) are **already
written** for this feature — no change.

**Review.** Self-adversarial pass run (Vault-in-commit-path SPOF, encrypt/OCC/idempotency interaction,
shred atomicity, cache coherency after shred, lens-target plaintext leakage) — findings folded into §7
(risks) and the resolutions below. A full `bmad-party-mode` pass is warranted at build time on the Fire-2
commit-path wiring (the security-plane change).

---

## 1. Problem & intent

**The gap (NFR-Privacy / GDPR right-to-erasure).** Lattice's ledger is **immutable** — Core KV is backed
by JetStream history; "deleting" a KV key (tombstone) leaves every prior value in the stream forever. So
a literal delete **cannot** satisfy right-to-be-forgotten for PII (SSN, DOB, …). The only sound mechanism
on an append-only substrate is **crypto-shredding**: store the PII encrypted, and "forget" by destroying
the key — the ciphertext (live + historical) becomes unrecoverable. The Obsidian *Brainstorm PII and
Crypto-Shredding* subdoc states it directly: *"Crypto-shredding is the only way to achieve true Right to
be Forgotten in a system backed by an immutable ledger."*

**What already exists (verified in code — this is a crypto-layer addition, not greenfield).** The
*sensitivity boundary* is built and shipping:

- **DDL flag** — `pkgmgr` emits a `.sensitive` aspect on an aspect-type DDL when `DDLSpec.Sensitive`
  (`internal/pkgmgr/build.go:127`, `definition.go:270`); the DDL cache reads `<root>.sensitive`
  (opt-in, absent ⇒ non-sensitive). Contract #7 reserves `sensitive` as a meta-layer aspect type.
- **Identity-anchoring** — Processor commit **step 6** rejects a `sensitive` aspect on a non-identity
  vertex with `SensitivityViolation` / `sensitiveAspectScope` (`internal/processor/step6_validate.go:125`).
  Contract #2 documents the `SensitivityViolation` error; Contracts #1 §+ #3 §3.8 document the
  "apply sensitivity constraints" commit-path rule.
- **Health hooks** — Contract #5 §5.4/§5.5 already define `vault_calls_total`, `keyshredded_handled_total`,
  and the `VaultUnreachable` issue (today "Phase 1 stub may report 0").
- **Failure tier** — `docs/components/refractor-failure-tiers.md` reserves the **privacy-critical
  crypto-shred tier** (a shredded-but-still-decrypting row ⇒ halt, no retry, page on-call) as
  "designed-but-not-built, dependency: Vault/Phase 3."

**What is missing (this design).** The cryptography itself: a **Vault** (per-identity key custody),
**encrypt-on-write / decrypt-on-read** in the Processor, the **`ShredIdentityKey` operation +
`KeyShredded` event**, and the **Refractor `KeyShredded` nullification handler** + the privacy-critical
tier. `lattice-architecture.md` Items 5 & 6 are the rubric; this design resolves the open mechanics.

**Intent.** Deliver true right-to-erasure for PII **now**, on the existing single-cell trusted-tool
platform, without waiting on the Edge node or read-path auth — and structured so the queryable Secure
Lens drops in cleanly once D1 lands.

## 2. The shape

### 2.1 Data model (Contract #1 key-shapes)

- **`vtx.identity.<id>.piiKey`** — a non-sensitive aspect on the identity vertex holding the **encryption
  envelope reference**, not key material: `{ wrappedDEK, keyId, kekVersion, alg: "AES-256-GCM",
  createdAt, shredded: false }`. Created **lazily** — on the first sensitive-aspect write for an identity
  with no `piiKey`, the Processor calls `Vault.CreateIdentityKey`, receives the wrapped DEK, and writes
  `piiKey` in the **same atomic batch** (non-PII identities never get one). "Key material never in Core
  KV" holds: only the *wrapped* DEK (ciphertext, openable solely by the master KEK / KMS) lands here.
- **Sensitive aspect** (e.g. `vtx.identity.<id>.ssn`, declared `sensitive: true` in its DDL) — stored
  with `data` = **ciphertext** (`{ ct, nonce, keyId }`), AES-256-GCM under the identity's DEK. Anchoring
  to an identity vertex is already enforced (step 6), so the key is always resolvable: it is the host
  identity's `piiKey`.
- **No new vertex types.** Reuses the existing identity vertex + aspect model; `piiKey` is just another
  aspect. Aligns with architecture D5 (minimum in vertex root; business/sensitive data in aspects).

**Granularity — aspect-level, resolved.** The architecture (Item 6) and the brainstorm subdoc conflict:
the subdoc proposes *field-level* `encrypted: true` per JSON property; Item 6 (the later, considered
decision-of-record) chose *aspect-level* `sensitive: true`. **I resolve to aspect-level** — the aspect is
the atomic unit of encryption *and* of shredding; field-level partial-JSON encryption makes crypto-shred
non-atomic and complicates every read/write path. "Some fields sensitive, others not" ⇒ split into
separate aspects. (Field-level is recorded as a considered-and-rejected alternative, §8.)

### 2.2 Write path (P2 — operations only; the Processor is the sole Core-KV writer)

Encryption is a **Processor commit-path middleware**, not script logic (so Starlark stays pure — it
returns plaintext; the engine guarantees ciphertext-at-rest, per the brainstorm's "Security is
Guaranteed by the DDL"):

```
step 4 (hydrate)   → decrypt-on-read: for each sensitive aspect pulled into the Starlark context,
                     Vault.Decrypt(ct, DEK) → plaintext. Starlark sees strings/numbers, never AES.
step 5 (execute)   → Starlark runs on plaintext, returns a MutationBatch with plaintext sensitive data.
step 6 (validate)  → unchanged: schema + permittedCommands + sensitiveAspectScope validated on PLAINTEXT.
step 6.5 (NEW —    → encrypt-on-write: for each mutation whose DDL is sensitive, lazily ensure piiKey
  encrypt)           (CreateIdentityKey + add the piiKey mutation to the batch if absent), then replace
                     mutation.data with Vault.Encrypt(plaintext, DEK) = { ct, nonce, keyId }.
step 8 (commit)    → atomic batch lands CIPHERTEXT (+ piiKey) in Core KV. Plaintext never touches KV.
```

Validation **before** encryption is deliberate: schema is validated against the plaintext shape; the
stored bytes are opaque ciphertext. Encryption is non-deterministic (random GCM nonce) — harmless under
last-writer-wins-by-revision, and idempotency keys on `requestId` (step 2 dedup) not on content, so a
resubmit returns the prior commit without re-encrypting.

**`ShredIdentityKey` operation.** A system/kernel op (lane `ops.urgent.>` — Contract #2 names urgent for
"emergency revocations") whose Starlark marks `piiKey.shredded = true` (an aspect update, P2) and emits a
**`KeyShredded` event** (`class: "privacy.keyShredded"`, payload `{ identityKey }`). The op records
*intent* in Core KV; the irreversible KMS destruction + projection nullification happen in the async
listeners (§2.4) so the commit path never blocks on an external KMS round-trip.

### 2.3 Read path (P5 — apps read lens projections; ciphertext-safe by construction)

- **Refractor (default lenses):** projects sensitive-aspect `data` **as-is = ciphertext**. No decrypt, no
  Vault call on the hot projection path. General lens targets (the NATS-KV read models, Postgres) hold
  **unreadable ciphertext** — so a sensitive aspect is safe at every shared read surface **without**
  read-path auth. This is the property that lets Phase A ship before D1.
- **Trusted tools (Loupe):** to display PII to the trusted operator, Loupe calls a **Vault decrypt RPC**
  (`lattice.vault.decrypt`, micro.Service responder) — acceptable under the trusted-single-identity model
  (no per-user read-path auth in Phase 3; same posture as Loupe reading full Core KV today). Optional;
  not required for correctness.
- **Secure Lens (Phase B, D1-gated):** the Refractor Secure Lens adapter decrypts sensitive aspects into
  **RLS-protected, queryable** read models (the architecture's *blind projection* rule). This is the only
  consumer that produces queryable plaintext, and it is exactly the surface D1's read-path auth protects —
  hence deferred behind D1.

### 2.4 Orchestration — shred finalization (mirrors the Weaver convergence-lens / `freshnessExpiry` precedent)

Shred is a multi-step, must-not-silently-fail flow. After the `ShredIdentityKey` commit + `KeyShredded`
event:

1. **Vault key destruction** — a **privacy worker** (a thin listener; co-located with Refractor's CDC
   path or a standalone `cmd/privacy-worker` — see §3) calls `Vault.ShredKey(identityKey)` to destroy the
   wrapped DEK (and, for a real KMS backend, the KMS key version). After this, `Vault.Decrypt` for that
   identity **fails permanently** — the ciphertext in live KV *and* JetStream history is gibberish.
2. **Cache eviction** — the **Processor** subscribes to `KeyShredded` and evicts that identity's DEK from
   its in-memory cache (architecture Item 5: "Cache invalidation via `KeyShredded` event"), so a cached
   DEK can't outlive the shred.
3. **Projection nullification** — the **Refractor `KeyShredded` listener** nullifies/removes projected
   rows derived from the shredded identity's sensitive aspects (belt-and-suspenders in Phase A, where rows
   already hold now-garbage ciphertext; **load-bearing** in Phase B, where Secure-Lens rows hold
   plaintext). On nullification failure the **privacy-critical tier** fires — halt the lens, no automatic
   retry, page on-call (the reserved tier in `refractor-failure-tiers.md`).
4. **Convergence guarantee (orphaned-PII reconcile, brainstorm #5/#62).** A **Weaver convergence marker**
   tracks "shred not yet finalized" — it stays *violating* until the Vault key is destroyed **and**
   projections are nullified, and re-drives the steps until convergence (mirrors the
   `orchestration-base` freshness/`MarkExpired` convergence-lens pattern). This catches the
   crash-after-commit-before-destroy window: the marker survives a restart and finalizes the shred.

This keeps the irreversible work **out of the synchronous commit path** (availability) while making it
**guaranteed-eventual** (Weaver convergence) and **loud on failure** (privacy-critical tier) — the right
posture for a confidentiality operation.

### 2.5 Vault backend (envelope encryption — the recommended Path A)

```
internal/vault (the interface + the local backend)
  type Vault interface {
    CreateIdentityKey(ctx, identityKey) (Envelope, error)  // new wrapped DEK
    Encrypt(ctx, identityKey, plaintext) (Ciphertext, error)
    Decrypt(ctx, identityKey, Ciphertext) (plaintext, error)
    ShredKey(ctx, identityKey) error                       // destroy DEK; irreversible
  }
```

- **Envelope encryption.** Per-identity **DEK** (random 256-bit) encrypts the aspects; the DEK is stored
  **wrapped** by a single **master KEK** in `piiKey.wrappedDEK`. To encrypt/decrypt, the backend unwraps
  the DEK once and caches the **plaintext DEK** in memory (short TTL, per-identity) — so steady-state
  encrypt/decrypt make **zero** external calls; only `CreateIdentityKey` / `ShredKey` touch the KEK
  custody. This collapses the "Vault SPOF in the write path" risk (architecture line 88 / brainstorm
  #120) to "KEK reachable at key-create / cold-cache time."
- **AEAD parameters + nonce uniqueness (grounded — NIST SP 800-38D).** AES-256-GCM is the AEAD; the
  **single security-critical invariant is that a (DEK, nonce) pair is never reused** — GCM authentication
  collapses catastrophically on nonce reuse. Strategy: a fresh **96-bit random nonce per encryption**,
  stored alongside the ciphertext (`{ct, nonce, keyId}`). NIST SP 800-38D permits random 96-bit nonces up
  to ~2³² invocations **per key**; because a DEK is **per-identity** and encrypts only that one identity's
  handful of sensitive aspects (re-nonced on each update), the per-key message count is microscopic —
  many orders of magnitude below the birthday bound — so random nonces are sound here with margin. (A
  deterministic per-key counter is the alternative if a future backend wants zero birthday-risk; not
  needed at this scale.) **DEK rotation:** v1 uses one DEK per identity for its lifetime (rotation =
  re-encrypt under a new DEK); deferred — the message-count bound makes it unnecessary now, and shred
  (destroy the DEK) is the only key-lifecycle event this feature requires. The party-mode crypto pass at
  build time (Fire 2) re-checks the nonce source + the bound.
- **Local backend (dev + trusted-tool deployment):** the master KEK is sealed in config (env var / file,
  `make`-provisioned), matching Loupe's 127.0.0.1 trusted-tool posture. Shred = delete the wrapped DEK +
  evict caches (the wrapped DEK is the only opener; destroying it shreds).
- **Production KMS adapters (later, Andrew's backend choice):** HashiCorp Vault *Transit* or AWS/GCP KMS
  implement the same interface; the DEK is wrapped/unwrapped by the KMS, `ShredKey` destroys the KMS key
  version. Pluggable like Refractor's target adapters — no change above the interface.

### 2.6 Scope boundary — sensitive *aspects*, not sensitive *blobs* (Object Store)

This feature encrypts/​shreds sensitive **aspects** in Core KV. It does **not** cover PII-bearing **blobs**
in the Object Store (`objects-base`) — lease PDFs, ID scans, signature images — which are stored
content-addressed and **unencrypted**, referenced by a pointer-aspect on the graph. Shredding an
identity's DEK destroys its sensitive *aspects* but leaves any such *document* recoverable, so the
right-to-erasure guarantee is **complete for aspect-PII and explicitly partial for document-PII**.

This is **out of scope** here (deliberately, to keep the feature coherent), and a follow-on Lattice item —
**"crypto-shred for object-store blobs"** — is filed on the board: encrypt a sensitive object under the
owning identity's DEK (or a per-object key wrapped by it) at upload, so the *same* `ShredIdentityKey`
destroys both planes. Until that ships, a deployment handling document-PII must treat object-store blobs
as non-shreddable — stated here so the GDPR-completeness claim is honest rather than silently partial.

## 3. Component & package layout

- **`internal/vault`** (new) — the `Vault` interface + envelope-encryption local backend + the
  `lattice.vault.decrypt` micro.Service responder (for trusted-tool reads). Substrate-only (no raw NATS
  outside the `micro.Service` responder, per the accepted exception).
- **`internal/processor`** — step-4 decrypt hook, step-6.5 encrypt hook, lazy `piiKey` creation, the DEK
  cache + `KeyShredded` eviction subscription. The Vault is injected (interface), so the Processor stays
  testable with a fake.
- **Privacy worker** — the `KeyShredded` → `Vault.ShredKey` + nullification listener. **Decision:** ship
  it **inside Refractor's CDC runtime** first (Refractor already owns the row-nullification handler per
  the architecture's Stream-2 ownership and already consumes Core-KV CDC), not a separate binary — fewer
  moving parts; a standalone `cmd/privacy-worker` is a later extraction if the privacy plane grows.
- **`packages/privacy-base`** (new package, P5/decision-#10 "everything-is-a-package") — ships the DDL:
  the `piiKey` aspect-type DDL, the `ShredIdentityKey` operation DDL + its Starlark, the
  `privacy.keyShredded` event-type DDL, the Weaver shred-finalization convergence lens, and permissions.
  A reference sensitive aspect (e.g. `ssn`) lives in a test/demo package, not here.
- **No app reads Core KV** for PII (P5): apps that need PII go through the Secure Lens (Phase B) or, in
  Phase A, simply don't surface it (ciphertext). Loupe (the inspector exception) uses the Vault RPC.

## 4. Contract surface

| Contract | § | Change vs build-to |
|---|---|---|
| #3 MutationBatch | **§3.10 — Sensitive-aspect encryption at rest (NEW)** | **CHANGE** — staged uncommitted. Makes ciphertext-at-rest the observable invariant + names the commit-path placement (validate plaintext → encrypt → commit). |
| #1 Addressing | §(DDL lookup / sensitivity constraints) | build-to (already written) |
| #2 Operation envelope | `SensitivityViolation` (§errors); `ShredIdentityKey` as a normal op | build-to (error already documented; the op is package DDL) |
| #5 Health KV | §5.4 `vault_calls_total` / `keyshredded_handled_total`; §5.5 `VaultUnreachable` | build-to (already written; wire the real counters) |
| #7 Primordial bootstrap | `sensitive` reserved aspect-type DDL | build-to (already reserved) |
| #10 Orchestration | convergence-lens / marker pattern for shred finalization | build-to (reuse `orchestration-base` precedent) |

The `KeyShredded` event is a **registered event-type DDL** shipped by `privacy-base` (Contract #3 §3.4
typed-event model) — package work, **not** a contract change. Only §3.10 is a frozen-contract edit, and
it is staged **uncommitted** in `main` as the proposal.

## 5. Migration / compatibility

**Zero data migration.** No package ships a `sensitive: true` aspect today (verified — no `.sensitive`
DDL in any installed package; the only references are the test fixtures + the boundary code). So
encrypt-at-rest is **purely additive**: every existing aspect is non-sensitive and untouched; the first
sensitive DDL + sensitive write exercises the new path. `piiKey` is created lazily, so existing
identities are unaffected until they receive PII. Backward compatible across the board.

## 6. Test strategy

- **Unit (`internal/vault`):** envelope encrypt/decrypt round-trip; DEK wrap/unwrap under the master KEK;
  `ShredKey` ⇒ subsequent `Decrypt` fails (the shred guarantee); cache TTL + `KeyShredded` eviction.
- **Unit (`internal/processor`):** step-6.5 produces `{ct,nonce,keyId}` (never plaintext) in the
  committed batch; step-4 returns plaintext to the Starlark context; lazy `piiKey` creation lands in the
  same batch; non-sensitive aspects bypass the crypto path entirely (no Vault call).
- **e2e (ephemeral stack, a new `make test-crypto-shred`):** install a package with a `sensitive: true`
  aspect → write PII → assert **Core KV holds ciphertext** (raw `KVGet` shows no plaintext) → assert the
  Vault decrypt RPC returns plaintext → submit `ShredIdentityKey` → assert `Vault.Decrypt` fails, the
  Refractor projection rows are nullified, `keyshredded_handled_total` increments, and the Weaver marker
  converges. Mirrors `make test-object-gc` (the Loop-A/B convergence e2e precedent).
- **Gate 3 (adversarial, all DEFENDED):** add a vector — *read PII after shred* must be DEFENDED
  (decrypt fails; no plaintext anywhere); *write a sensitive aspect to a non-identity vertex* stays
  DEFENDED (existing `SensitivityViolation`).
- **Failure-tier test:** a forced nullification failure raises the privacy-critical tier (lens halts, no
  retry, alert emitted) — not a silent DLQ.

## 7. Risks & resolutions (from the adversarial pass)

| Risk | Resolution |
|---|---|
| **Vault as a write-path SPOF** (architecture line 88) | Envelope encryption + in-Processor plaintext-DEK cache ⇒ steady-state encrypt/decrypt make **no** external calls. Degradation: if the DEK is uncacheable (cold cache + KEK unreachable), **sensitive** writes fail-closed (Terminal/retry); non-sensitive writes proceed. Health: `VaultUnreachable` (Contract #5 §5.5). |
| **Plaintext leaks to a general lens target** | Refractor projects ciphertext as-is on the default path; decryption is opt-in (Secure Lens / Vault RPC only). Safe by construction in Phase A. |
| **Crash after `ShredIdentityKey` commit, before KMS destroy** (orphaned PII) | Weaver shred-finalization convergence marker re-drives destroy + nullify until converged (survives restart); brainstorm #5's "orphaned-PII nudge." |
| **Cached DEK outlives a shred** | Processor subscribes to `KeyShredded` and evicts immediately (TTL is the fallback, not the primary invalidation). |
| **Encryption breaks OCC / idempotency** | Validation runs on plaintext (step 6); idempotency keys on `requestId` (step 2). Non-deterministic GCM nonce is fine under last-writer-wins-by-revision. |
| **Nullification silently DLQ'd** | Privacy-critical failure tier: halt + page, no auto-retry (reserved tier, now built). |
| **`piiKey` itself leaking key material** | `piiKey` holds only the **wrapped** DEK (openable solely by the KEK/KMS) — never plaintext key material; satisfies "key material never in Core KV." |

## 8. Alternatives considered

- **Field-level `encrypted: true`** (brainstorm subdoc) — rejected per architecture Item 6: the aspect is
  the atomic encrypt/shred unit; partial-JSON encryption makes shred non-atomic and burdens every path.
- **Shadow aspects** (decrypted copy beside the encrypted original) — rejected per architecture Item 5:
  second source of truth, stale/failed-write consistency hazard.
- **Plaintext + access control only** — rejected: cannot satisfy right-to-erasure on an immutable ledger
  (the whole premise).
- **Encrypt in Starlark** — rejected: pushes AES/KMS into every script, breaks the "Starlark stays pure"
  guarantee, and makes the invariant un-enforceable. Encryption must be engine middleware.
- **Commit to a cloud KMS now (Path B above)** — viable, deferred: the pluggable interface lets it drop in
  later without rework; Path A keeps Phase 3 in its trusted-tool posture.

## 9. Decomposition for the Steward (fire-by-fire, each independently shippable + green)

> **Sequencing (ratified):** the **whole feature is gated behind D1 ✅** and ships **Phase A + Phase B
> together** — not a Phase-A-now split. Fires 1–4 (Phase A: crypto + shred) and Fire 5 (Phase B: Secure
> Lens) are one coherent delivery once D1 has landed (D1 provides the RLS-Postgres surface Fire 5 + the
> vertical-app/​adapter PII readers require). The Fire breakdown below stands; only the *start gate* moves
> (after D1, not now).

1. **Fire 1 — `internal/vault` + local envelope backend.** The `Vault` interface, envelope encryption
   (DEK wrap/unwrap under a sealed master KEK), `CreateIdentityKey/Encrypt/Decrypt/ShredKey`, the
   `lattice.vault.decrypt` responder, unit tests. No commit-path wiring. Ships green standalone.
2. **Fire 2 — Processor encrypt-on-write + decrypt-on-read.** Step-4 decrypt hook, step-6.5 encrypt hook,
   lazy `piiKey`, DEK cache. `privacy-base` ships the `piiKey` DDL; a test package ships a `sensitive`
   aspect. e2e: Core KV holds ciphertext, Starlark sees plaintext. *(Security-plane change — run the full
   3-layer review + a party-mode pass here.)*
3. **Fire 3 — `ShredIdentityKey` op + `KeyShredded` event + Vault destruction + cache eviction.**
   `privacy-base` ships the op + event DDL; the privacy listener calls `Vault.ShredKey`; Processor evicts
   on `KeyShredded`. e2e: shred ⇒ decrypt fails.
4. **Fire 4 — Refractor `KeyShredded` nullification handler + privacy-critical failure tier + health
   counters + Weaver shred-finalization convergence lens.** Completes the guarantee + the orphaned-PII
   reconcile. `make test-crypto-shred` + the Gate-3 vector go green here.
5. **Fire 5 (gated on D1 ✅ ratified) — Refractor Secure Lens decrypt-at-projection.** Queryable PII into
   RLS-protected read models (the *blind projection* rule). Deferred behind read-path auth.

Production KMS adapters (HashiCorp Transit / AWS KMS) are a follow-on after Fire 1, pending Andrew's
backend choice (Fork 1).

---

*Designer fire, 2026-06-27. **Ratified — sequenced behind D1; build Phase A + Phase B together** (Fires
1–5 as one delivery after D1 ✅, not a Phase-A-now split). The §3.10 contract edit stays staged
**uncommitted** in `main` (commit it when the Vault build starts, so the contract doesn't claim
ciphertext-at-rest ahead of the code). Follow-on filed: crypto-shred for object-store blobs (§2.6).*

---

## Build-start addendum (2026-07-02 — Winston, lead adjudication; PO + FE-engineer review folded)

**The D1 start-gate is CLEARED — this feature is build-next.** Basis: D1.1–D1.5 all shipped (the
protected-lens/RLS/`actor_read_grants` surface Fire 5 consumes is live, incl. out-of-band provisioning +
verify-and-pause); the D1 design (§7) lists the full Gateway + Personal Lens as *deferred beyond D1, not
in scope*, so they do not gate this feature; the M7 prerequisite (NATS account write-restriction live
enforcement) shipped. Contract #3 **§3.10 and §3.11 are already ratified + committed** (`6d60ed5`,
blob-shred follow-on included) — the closing note above ("stays uncommitted until build") is superseded;
no contract work remains at build start.

**Premise refresh — §5 "zero data migration" is STALE; reset ruling (Andrew, 2026-07-02): fresh stack,
no migration path.** `identity-domain` now ships **seven** `sensitive: true` DDLs (name, email, phone,
ssn, dob, claimKey, credentialBinding) and loftspace writes real values through them — plaintext in Core
KV *and* JetStream history for every pre-Vault identity. **Ruling:** nothing runs in production, and both
NATS and Postgres are ephemeral by design (`docker-compose.yml` — no named volumes; "data is lost on
`make down`, which is intentional"), so the sanctioned Fire-2 posture is a **full-stack reset**
(`make down && make up-full`) at the Vault delivery boundary: pre-Vault plaintext history is destroyed
with the containers, every identity re-mints through encrypt-on-write, and **no migrate-encrypt path is
built**. A migrate-encrypt tool becomes a prod-era follow-on only if a deployment ever adopts sensitivity
*after* accumulating data — out of scope until one exists.

**Unlisted Fire-5 migration dependency (FE finding) — NOT resolved by the reset ruling.** This is a
*read-path/behavior* migration, not a data migration: Fire 2's encrypt-on-write turns three *live*
name-projecting lenses to ciphertext — `applicantRoster`, `applicantRosterRead` (loftspace-domain), and
`duplicateCandidates` (identity-hygiene). Under the reset posture the effect is *total*, not partial:
post-reset **every** identity is post-Fire-2, so those views render ciphertext for **all** rows from the
moment Fire 2 lands until the Secure-Lens migration ships. Consequence: **Fires 2–5 land as one delivery
with the lens migration sequenced inside Fire 5** — the ratified one-coherent-delivery posture is
load-bearing, not just preferred. (The reset also dissolves the table-reshape wrinkle: read-model tables
re-provision out-of-band on a fresh stack; no `ALTER TABLE` path needed in dev.)

**Named Fire-5 consumers (the verticals this unblocks — build the last two in parallel with Fires 1–4):**
- **LoftSpace landlord contact + name display**: `landlordLeaseApplicationsReadSpec` gains
  `applicant_name` / `applicant_email` / `applicant_phone` as Secure-Lens columns; completes the D1.5
  Rec-C deferral bundle (readiness clone + console retirement ship here — see the D1.5 design §4–§6).
- **Clinic patient contact re-model** (pre-blessed by `clinic-domain-design.md` — "`vtx.identity` + an
  `identifiedBy` link — not a rework"): extend `CreatePatient` with optional `identityKey` +
  `lnk.patient.<pid>.identifiedBy.identity.<iid>` (no backfill op — the reset ruling covers pre-existing
  patients); the FE does the loftspace two-step (mint unclaimed identity carrying sensitive contact →
  create patient linked to it);
  `.demographics` keeps only the non-sensitive `fullName`. Display = a Secure-Lens staff-anchored
  protected model at Fire 5. The re-model half is **Vault-independent** and buildable now (Verticals lane).

**Fire 1 CHECKPOINT (2026-07-02, Lattice Steward).** Shipped: `internal/vault` — the `Vault` interface,
`LocalBackend` (envelope encryption: AES-256-GCM, per-identity DEK wrapped under a sealed master KEK,
identityKey bound as AEAD associated data), and the `lattice.vault.decrypt` NATS Services responder.
Interface resolved to `Encrypt(ctx, identityKey, envelope, plaintext)` / `Decrypt(ctx, identityKey,
envelope, ct)` (envelope passed explicitly by the caller, not held by the backend) — keeps the Vault
stateless w.r.t. durable key custody, so Core KV's `piiKey` stays the single source of truth for the
wrapped DEK; resolves the abbreviated §2.5 sketch vs. the more detailed §2.2 mechanics in favor of the
latter. 3-layer adversarial review found and fixed two real bugs pre-merge: a malformed-nonce-length RPC
request could panic the process, and a TOCTOU race let an in-flight `Decrypt` succeed against a
concurrently shredded identity — both fixed, verified with `-race`. `Envelope`/`Ciphertext` field shapes
match §2.1/§3.10 verbatim; no translation layer needed in Fire 2. No commit-path wiring — that's Fire 2.
Known Fire-1-scope limitation: the local backend's shredded-set is in-memory only (process-restart loses
it); harden when Fire 3/4 wires the privacy worker to something that needs shred to survive a restart.
Next: Fire 2 — Processor step-4 decrypt hook, step-6.5 encrypt hook, lazy `piiKey` creation, DEK cache;
`privacy-base` ships the `piiKey` DDL; security-plane change, full 3-layer + party-mode at that fire.

**Fire 2 CHECKPOINT (2026-07-02, Lattice Steward, `83b7976`).** Shipped: the commit-path wiring — step 4
(`internal/processor/step4_hydrate.go` + `sensitive_decrypt.go`) decrypts a sensitive aspect pulled into
the Starlark context via both `contextHint.reads` and the lazy `kv.Read()` seam (`connKVReader`); step 6.5
(`step65_encrypt.go`) encrypts every sensitive-DDL mutation post-validate, lazily minting `piiKey` in the
SAME atomic batch on an identity's first sensitive write. `packages/privacy-base` ships the `piiKey`
aspect-type DDL (non-sensitive, declaration-only script, no install-order coupling to identity-domain).
Wired into `MakePipeline` (production; `cmd/processor` requires a master KEK via
`LATTICE_VAULT_MASTER_KEK`/`_FILE`, refusing to start without one) and `testutil.CapabilityPipeline` (a
shared `TestVault`) — identity-domain's full `ssn/dob/name/email/phone/claimKey/credentialBinding` suite,
including `ClaimIdentity`'s hash verification against ciphertext, round-trips correctly with zero
regressions across the full `go test ./...` sweep. 3-layer adversarial review (Blind Hunter, Edge Case
Hunter, Acceptance Auditor) found and fixed two real bugs pre-merge: a `piiKey` create-once collision
wasn't retry-eligible under the commit path's OCC retry loop (two concurrent first-sensitive-writes for
the same identity would hard-reject the loser instead of transparently retrying — fixed by extending the
retry condition to a new `mintedPiiKey` signal), and `encryptSensitiveMutations` mutated the caller's
shared `Document` map in place instead of a fresh copy (fixed). Also hardened the local dev KEK file to
600 permissions (`chmod` + `umask 077` in `make provision-vault-kek`) and corrected an overclaiming
"fails closed" comment on the `piiKey` DDL (empty `permittedCommands` blocks `piiKey` being dispatched
*as an operation*, not a script writing a `.piiKey` mutation directly — the same trust model already
governing every other aspect type; not new to this fire).
**Known non-live limitation (documented inline, not fixed this fire):** step 6.5 / decrypt-on-read
resolve sensitivity by exact DDL class name only (`DDLs.Lookup`), unlike step 6's `resolveGoverningDDL`,
which additionally walks a bounded `instanceOf` chain to a fine-grained discriminator class's type
authority. No shipped sensitive DDL today needs that walk (all seven register under their own exact
canonical name) — but a **future** sensitive DDL resolvable only via the chain would silently commit as
plaintext (Lookup miss) while step 6 still scope-checks it correctly. Fold the shared resolution path in
if that pattern is ever used for a sensitive aspect.
Next: Fire 3 — `ShredIdentityKey` op + `KeyShredded` event + `Vault.ShredKey` + Processor cache eviction;
`privacy-base` ships the op/event DDL.

**Fire 3 CHECKPOINT (2026-07-02, Lattice Steward, `604342b`).** Shipped: `packages/privacy-base`'s
`ShredIdentityKey` op DDL (marks `piiKey.shredded=true`, an unconditioned update) and the registered
`privacy.keyShredded` event-type DDL (Contract #3 §3.4). `internal/privacyworker` is a new durable
`events.privacy.keyShredded` consumer — co-located inside `cmd/processor` (not Refractor, as the body's §3
sketch suggested) sharing the Processor's own `*vault.LocalBackend` instance, because that sharing is what
makes a shred observable at all: `internal/vault/local.go`'s `shredded` deny-list and DEK cache are
per-instance in-memory state, so a separately-constructed backend from the same KEK would never see it.
The listener's sole job is `Vault.ShredKey(identityKey)` — destruction + cache eviction are one atomic call
in the local backend, so "cache eviction" (design §2.4 point 2) needed no separate code path.
3-layer adversarial review (Blind Hunter, Edge Case Hunter, Acceptance Auditor) found and fixed one real
bug pre-merge: an identity that never received a sensitive write has no `piiKey` row, so the original build
recorded its shred ONLY in the in-memory deny-list — a Processor restart forgot it, and a later sensitive
write would silently mint a fresh, unshredded key and reopen the identity to PII. Fixed by having the DDL
always write a durable placeholder (empty `wrappedDEK`, `shredded=true`) instead of skipping the mutation,
and reordering `internal/vault/local.go`'s `checkAndDeriveDEK` to check `envelope.Shredded` BEFORE its
`WrappedDEK`-empty validation (so a placeholder envelope reports `ErrKeyShredded`, not `ErrInvalidEnvelope`)
— `CreateIdentityKey` also now refuses to mint for an already-shredded identity (defense in depth). A new
e2e test (`TestShredIdentityKey_EndToEnd_VaultDecryptFails`) proves the full chain — op commit → outbox
publish → privacy-worker consume → `Vault.ShredKey` — against a real embedded-NATS harness; a second test
proves the placeholder survives a simulated restart (a fresh `LocalBackend` sharing only the KEK still
denies). `internal/testutil/pipeline.go` gained an injectable `Vault` field (tests need the SAME instance
across pipeline setup and assertion) and its `ProvisionHarness` now sets `AllowAtomicPublish` on
`core-events`, fixing a harness gap (no external test package had driven the outbox against it before) that
otherwise nak-loops every outbox publish forever.
Next: Fire 4 — Refractor `KeyShredded` nullification handler + the privacy-critical failure tier + health
counters (`vault_calls_total`, `keyshredded_handled_total`) + the Weaver shred-finalization convergence
lens (the crash-after-commit-before-destroy guarantee); `make test-crypto-shred` + the Gate-3 vector go
green there.

**Fire 4a CHECKPOINT (2026-07-02, Lattice Steward, `a55ad4e`).** Shipped: `internal/refractor/keyshredded` —
a new durable consumer on `events.privacy.keyShredded` inside the Refractor process (§3's placement decision),
independent of `internal/privacyworker` (Fire 3). For each explicitly-configured `NullifyTarget{RuleID,
KeyField}` it calls a new `control.Service.NullifyRow` (→ a new `Pipeline.Delete` → the existing
`adapter.Delete`) to remove the shredded identity's already-projected row. Targets are a Go-level allowlist,
not auto-discovered — Refractor has no registry of lenses by source-vertex-type (a lens's `MATCH` is opaque
compiled cypher, not a declared field), so inventing one would be a new primitive, not this fire's scope;
production ships with an empty list (a harmless no-op sweep that still exercises the event/counters/failure
path) until a real Phase-A consumer lens (`applicantRoster` and friends) opts in as a deferred follow-up.
A real `Delete` failure raises the new `failure.CatPrivacyCritical` tier (`internal/refractor/failure`):
the affected lens is paused via a new exported `control.Service.PauseRule` and the event is Acked, never
retried — matching `refractor-failure-tiers.md`'s reserved "privacy-critical — crypto-shred failure" posture
verbatim. Wired Contract #5 §5.4's `vaultCallsTotal` (Refractor heartbeat, Phase-1 stub `0` — Refractor makes
no Vault calls until Fire 5's Secure Lens) and `keyshreddedHandledTotal` (real, incremented per handled event)
into `LatticeHeartbeater`. Added Gate-3 vector #15 ("Read PII after crypto-shred," DEFENDED) and a new
`make test-crypto-shred` e2e gate (`internal/cryptoshred`, mirrors `make test-object-gc`'s shape).

3-layer adversarial review (Blind Hunter / Edge Case Hunter / Acceptance Auditor) found two real hardening
gaps, both fixed pre-merge: a nil `control.Service` would panic the consumer goroutine mid-stream on the
first event instead of failing fast at construction (fixed: `New` panics immediately on `Control == nil`,
mirroring `control.Service`'s own nil-panic convention); and a permanently-misconfigured target (a typo'd or
decommissioned `RuleID`) would nak-loop the same event forever with no escalation path (fixed: bounded to
`maxNotRegisteredDeliveries` = 20 redeliveries, well above any real startup race, after which the listener
gives up loudly instead of retrying indefinitely).

**KNOWN LIMITATION (disclosed, not silently swept — documented in-code at
`internal/refractor/keyshredded/manager.go`'s `handleKeyShredded`).** Nullification is best-effort/transient,
not a permanent guarantee: empirically, against a live full-engine test harness, a row this listener deletes
can be **re-upserted shortly after** by Refractor's own projection pipeline — the identity vertex stays alive
(not tombstoned) after a shred, so a later CDC delivery for that vertex re-evaluates the lens's `MATCH`
(which still matches a living vertex) and re-projects the row with a fresh, later `projectionSeq` that
legitimately beats any watermark this listener stamps, guarded target or not. This is **consistent with**
Phase A's "belt-and-suspenders" framing (rows hold only ciphertext, so a resurrected row is not a new leak)
but means it is **not yet load-bearing** the way Phase B needs it to be. Closing this gap needs either a
lens-side shredded-identity filter (mirroring the already-ratified negative/filter-retraction projection
pattern) or Fire 5's Secure Lens — tracked as residual work, not re-filed as a fresh backlog item since it's
scoped inside this same feature.

Next: **Fire 4b** — the Weaver shred-finalization convergence lens (pure observability: a projected
`{identityKey, shredded, vaultKeyDestroyed, projectionsNullified}` row so an operator/Loupe can see
in-flight/stuck shreds). Crash-survival for both async steps (Vault key destruction, row nullification) is
**already guaranteed today** by JetStream's own durable at-least-once redelivery on both consumers — Fire 4b
adds visibility on top, it does not close a correctness gap. Requires `internal/privacyworker` and
`internal/refractor/keyshredded` to gain op-submission capability (neither currently submits ops; Fire 3's
privacy-worker only calls `Vault.ShredKey` directly) to record completion durably — a clean, separable
increment. The re-upsert known-limitation above is a separate, harder problem Fire 4b does not resolve.

**Fire 4b CHECKPOINT (2026-07-02, Lattice Steward, `7d094ac`).** Shipped: durable shred-finalization
recording + the `shredStatus` observability lens. A new kernel-seeded **`identity.system.privacy` service
actor** (bootstrap v14→15, `PrimordialVertexKeyCount` 34→36 — vertex + holdsRole→operator link, the
Contract #7 line-64 "additional service actor" pattern, readiness-gated like Loom/Weaver/Bridge/objmgr;
graph-discovered by the hosting binaries via `bootstrap.PrivacyActorKey`, since cmd/processor and
cmd/refractor deliberately never load `lattice.bootstrap.json`). `privacy-base` ships
**`RecordShredFinalization{identityKey, step: vaultKeyDestroyed|projectionsNullified}`** (admitted by the
existing `shredIdentityKey` vertexType DDL; flips one boolean + At stamp on the already-shredded `piiKey`,
fail-closed NotFound/FailedPrecondition otherwise), an operator grant for it (the MarkExpired idiom —
ShredIdentityKey's own no-default-grant posture is deliberately unchanged), and the **`shredStatus` lens**
(`privacy-shreds` bucket, flat full-engine row per SHREDDED identity via `WHERE i.piiKey.data.shredded =
true`; a not-yet-recorded step projects null = "in flight"; booleans only transition false→true so no
retraction machinery is needed). `ShredIdentityKey` now also stamps `shreddedAt`. Both listeners submit the
record **publish-then-ack** on `ops.system` with deterministic requestIds
(`substrate.DeriveNanoID("shredfin:<step>:", identityKey+seq)` — a redelivery of the same event collapses
on the Contract #4 tracker; the piiKey is declared in `contextHint.reads`, so the record is
hydrated + OCC-conditioned and the two sibling records racing on the system lane's 2 concurrent pump
workers collapse to a commit-path retry instead of a lost flag — a 3-layer-review find, fixed pre-merge,
as was a re-shred inheriting the prior cycle's booleans (ShredIdentityKey now resets them, so re-shred
doubles as the remediation for a privacy-critical-stuck row)); `internal/refractor/keyshredded` records only when **every** configured target
nullified cleanly this delivery (a privacy-critical halt or a given-up target skips the record, leaving the
row visibly stuck — the observability the lens exists for); an empty `ActorKey` (pre-v15 kernel) disables
recording with a warning without disabling the shred/nullification themselves. The `make test-crypto-shred`
e2e now drives the full loop through a REAL capability-auth ops.system pipeline and asserts both booleans
land on the piiKey; the lens spec is proven on the full engine (`lens_cypher_test.go`).
**Found while grounding (filed on the board, pre-existing, NOT a 4b regression):** under real capability
auth, a kernel-seeded system actor's platform path reads only its `cap.<actor>` anchor doc — the fixed
6-op kernel grant set — so EVERY system-actor-submitted package op (Weaver's MarkExpired, Loom's
CreateTask, objmgr's DetachObject, and now RecordShredFinalization) authorizes only because the dev stack
runs `LATTICE_AUTH_MODE=stub`; the operator-grant idiom these ops share is aspirational until that gap gets
a design. Residual: the Fire-4a re-upsert limitation stands (unchanged); Loupe surfacing of
`privacy-shreds` rides the Loupe lane.
Next: **Fire 5** — the Secure Lens (decrypt-at-projection into RLS-protected read models) + the named
Fire-5 consumers above; the ratified one-coherent-delivery posture applies.

**Fire 5a CHECKPOINT (2026-07-02, Lattice Steward, `4bbc8f3`).** Fire 5 split into 5a (the platform
primitive) + 5b (the consumer migrations); 5a shipped: **Secure-Lens decrypt-at-projection in the
Refractor**. A lens spec declares `secureColumns: [{column, identityKeyColumn, field}]` — the cypher
RETURNs the sensitive aspect's ciphertext envelope whole (`i.name.data`; aspect presence via
`i.name.data.ct <> null`, since `data.value` no longer exists post-encryption) — and the pipeline
decrypts each declared column under the owning identity's DEK (piiKey envelope read from Core KV,
`vault.LocalBackend` from the same `LATTICE_VAULT_MASTER_KEK(_FILE)` the Processor uses, now provisioned
before refractor in `make up`) before the row reaches the adapter. Fail-closed posture at every layer:
protected-RLS-postgres-only (translateSpec + a pkgmgr install-time mirror; plain-projection lenses only;
platform RLS columns + output-key columns rejected; RETURN aliases statically validated at activation),
no-Vault ⇒ no activation, tampered/misdeclared/soft-deleted-or-malformed-piiKey inputs ⇒ Terminal (never
fail-silent), shredded ⇒ the column projects **null**. **The Fire-4a re-upsert limitation is RESOLVED for
Secure-Lens targets**: a piiKey aspect CDC event (every shred commits one) now triggers anchor
reprojection on a secure lens, so an already-projected plaintext row is scrubbed to null-PII immediately —
proven end-to-end through the stream handler against embedded NATS. Full 3-layer adversarial review;
fixed pre-merge: that shred-scrub HIGH, a MatchChange hot-reload path that bypassed the secureColumns
refusal, refused-update baseline poisoning (guards now compare against the running pipeline's activated
set), and the validation gaps above. Known residuals: the retry queue is documented-unsafe for a secure
lens (captures decrypted rows; unwired in production); hot-reload of a secure lens's secureColumns or
table/DSN is refused (delete-and-re-create is the path); `vault_calls_total` now reports real decrypt
counts.
Next: **Fire 5b** — the consumer migrations per the build-start addendum: `applicantRoster` (retire or
re-model — NATS-KV cannot be secure), `applicantRosterRead` + `duplicateCandidates` re-authored as secure
lenses (WHERE keyed on `.ct`), `landlordLeaseApplicationsReadSpec` gains applicant_name/email/phone
secure columns (D1.5 Rec-C bundle), clinic patient-contact display, and the loftspace/clinic FE tails;
extend `make test-crypto-shred` to assert a secure-lens row scrubs on shred. Wear-the-other-hat package/FE
work; stays in this lane.

**Fire 5b-i CHECKPOINT (2026-07-02, Lattice Steward, `603fd1f`).** First 5b slice shipped: the **applicant
roster migrates onto the Secure Lens**. `loftspace-domain` 0.6.0 retires the unprotected `applicantRoster`
NATS-KV lens outright (a name roster now has no unprotected surface) and re-authors `applicantRosterRead`
as a secure lens — WHERE on ciphertext presence (`i.name.data.ct <> null`), RETURN carries the envelope
whole, `SecureColumns: [{name, identity_key, value}]` — proven on the full engine by a new
`lens_cypher_test.go` (envelope-whole projection; unnamed AND plaintext-shaped identities excluded, so the
lens can never self-serve plaintext). `cmd/loftspace-app`'s two server-side name resolutions
(unit-applications console, lease-document rendering) move onto the protected Postgres model read as the
app's admin actor (`rosterIdentities`); the durable attach path resolves names STRICTLY (a roster outage
refuses to persist a nameless rendering — idempotent-digest protection), display paths degrade to bare
keys. Full 3-layer review (auditor PASS ×6); folded pre-merge: NULL-`state` Scan brick (`COALESCE`),
attach-path idempotency break, console availability regression, package version bump. The real-Postgres
RLS suite now proves a NULL-name (shredded) row stays out of the wildcard picker.
**Grounding correction to the 5b plan — `duplicateCandidates` is NOT secure-lens-migratable:** its WHERE
does cross-identity equality (`a.email = b.email`) + `levenshteinRatio(a.name, b.name)` — matching runs
in-engine BEFORE any decrypt-at-projection, and per-identity DEKs make ciphertext equality meaningless, so
the lens is functionally inert post-Fire-2. Dedup-over-encrypted-PII needs its own design (blind-index /
HMAC companion aspect at write time, or a sanctioned engine-side mechanism) — **routed to the Designer**;
do not bodge it into 5b. Residuals otherwise unchanged: a `.name` tombstone/rewrite has no
reprojection/retraction transport on a plain-projection lens (latent — no shipped op does either; exactly
the ratified negative/filter-retraction item's scope); the orphaned `loftspace-identities` bucket + any
legacy plaintext rows die with the Andrew-ratified full-stack reset at the delivery boundary, where the
live e2e runs (a secure-lens spec change refuses hot-reload by design — delete-and-re-create/fresh
bootstrap is the path).
Worktree: `/Users/andrewsolgan/Documents/GitHub/lattice-wt-vault-5b-roster` (branch
`steward-vault-5b-roster`, merged through `603fd1f`). Next: **Fire 5b-ii** —
`landlordLeaseApplicationsReadSpec` applicant_name/email/phone secure columns + the D1.5 Rec-C bundle
(readiness clone via a shared cypher fragment, console retirement, FE tail); then 5b-iii clinic
patient-contact re-model + display; extend `make test-crypto-shred`; delivery-boundary reset + live e2e
close 5b.

**Fire 5b-ii CHECKPOINT (2026-07-02, Lattice Steward, `a710c7a`).** Second 5b slice shipped: **landlord
applicant contact via Secure-Lens columns** — `landlordLeaseApplicationsRead` gains `applicant_name` /
`applicant_email` / `applicant_phone` (envelope-whole RETURN, `applicant` as the key-custody column,
`Field: value` for all three; no WHERE on ciphertext presence — a contactless/shredded applicant's
application still projects, columns null). `cmd/loftspace-app` threads the nullable columns through the
landlord handler and the RLS card view renders name + a contact line; the real-Postgres RLS suite proves
they serve null-safe to the managing landlord only. This ships the "LoftSpace landlord contact + name
display" named consumer.
**Mechanism correction (review-driven, replaces a wrong 5b-i/5b-ii assumption):** no per-anchor scrub
transport was needed for a NEIGHBOR-owned secure column. The lens's anchor MATCH is unanchored (no
`{key: $actorKey}`), and the full engine ignores the seed vertex for an unanchored MATCH — every piiKey
reprojection re-scans all leaseapps and re-projects every row with a fresh decrypt, so a shred scrubs the
landlord rows in ONE evaluation. An adjacency-BFS anchor fan-out built mid-fire was proven redundant by
the 3-layer review (and strictly worse: N full scans + partial-failure modes) and dropped; the guarantee
is pinned by `TestSecureLens_NeighborShredReprojectsAnchoredRows` through the real `handle()` path. This
re-confirms the standing "full engine scans → no enumerator" ruling.
**UPGRADED RESIDUAL — the manages-retraction plaintext hole (Acceptance-Auditor find; gates 5b close).**
`UnassignUnitOwner` (shipped) tombstones the `manages` link; the plain pipeline has no link-retraction
transport and a zero-row re-evaluation retracts nothing, so the stale `(app_id, old_landlord_id)` row
retains **decrypted plaintext**, stays RLS-readable by the unassigned landlord, and a later shred's
re-scan never overwrites it (the current-cypher rows no longer include it) — a right-to-erasure gap
reachable via a shipped op, more severe than the display-scalar staleness D1.3 accepted.
**PREMISE CORRECTION (2026-07-02, retraction Fires 1+2 build, `5624392`):** the ratified fire's
anchor-self transport does **NOT** retract this row — `landlordLeaseApplicationsRead` is keyed
`(app_id, landlord_id)` where `landlord_id` binds a NEIGHBOR variable, which the read-free key
derivation rejects structurally (the designed `ok=false` fall-through, pinned by
`TestPlainLens_NeighborKeyedComposite_FallsThroughToLinger`). The fix is the retraction design's
**Fire 3 target-diff** (adapter `ListKeysForAnchor` + set-difference Deletes), whose ratified
build-condition — a live consumer — this very row satisfies. **Sequencing: 5b close — the completed
right-to-erasure claim + the live e2e — is gated on Fire 3 shipping.** (The dev stack may re-bootstrap
and serve the columns sooner — single trusted user, synthetic data, and the v15 kernel bump
independently forces resets — but the claim is not complete, and 5b is not closable, until the Fire-3
retraction lands. Fires 1+2 DID land the plain-lens aspect/link freshness transport, so the shred-scrub
and single-key retraction halves are live.)
Remaining Rec-C bundle (readiness clone via a shared cypher fragment + console retirement + full RLS
decisioning) is the next slice — note the readiness clone needs WITH-aggregation while carrying envelope
columns through grouping, an unverified engine behavior to spike first (and note a WITH-bearing lens is
excluded from anchor-self retraction by design — its stale-row posture needs stating at design time).
**Fire 3 CHECKPOINT (2026-07-02, Lattice Steward).** Retraction Fire 3 (target-diff) shipped —
`landlordLeaseApplicationsRead` now retracts the manages-unassign row: `DiffRetraction`
(lens-definition opt-in) + `adapter.KeyLister.ListKeys` (full live-key read) +
`(*full.CompiledRule).ValidateUnanchoredForDiffRetraction` (activation-time backstop —
fails closed rather than mass-retracting if a future lens misuses this on an
`$actorKey`-scoped query). The build realized the design's target-diff *intent* with a
**lens-wide** diff rather than the sketched per-anchor `ListKeysForAnchor` scoping: since
`landlordLeaseApplicationsRead`'s cypher is genuinely unanchored (no `{key: $actorKey}`
anywhere), the re-execute already recomputes the complete global truth on every trigger,
so a full-target-vs-full-fresh diff is exact — an anchor-scoped variant would have been
ambiguous anyway (the `identity` endpoint is either the applicant or the managing
landlord role, with no single stable id to scope by). See `docs/components/refractor.md`
"Neighbor-driven / multi-row retraction" for the shipped behavior. **5b-close's Fire-3
gate is now CLEARED.**
Worktree: `/Users/andrewsolgan/Documents/GitHub/lattice-wt-vault-5b-ii` (branch `steward-vault-5b-ii`,
merged through `a710c7a`). Next: **5b-ii-b** (Rec-C remainder), then 5b-iii clinic contact;
extend `make test-crypto-shred` to drive the REAL `landlordLeaseApplicationsReadSpec` through
shred-scrub (the composition is currently proven in halves); delivery-boundary reset + live
e2e close 5b.

**Fire 5b-ii-b CHECKPOINT (2026-07-02, Lattice Steward, `13ffb75`).** Readiness clone shipped:
`landlordLeaseApplicationsRead` gains `qualified` (ssn/fresh-bgcheck/payment/signature — the same
formula `leaseApplicationComplete` derives as `applicantApproved`), via a shared cypher fragment
(`readinessOptionalMatch`/`readinessWithItems`, two Go constants spliced into BOTH lenses) so the
readiness rule can't drift between hand-copies (§4 Option A). This is the lens's first `WITH`
clause; spiked first and confirmed safe — the full engine carries the map-valued Secure columns
(the ciphertext envelopes) through a non-aggregating WITH passthrough unmodified, and the
Secure-Lens decryptor resolves a column by RETURN alias only, indifferent to whether that alias
came from a direct hop or a WITH passthrough. `qualified` is wired through the Go handler
(`protectedLandlordRow`, SQL, Scan) but **not yet consumed by any FE** — moving Approve/Decline
onto the RLS-scoped view and retiring the trusted console is a UX-consolidation decision, not just
wiring, and is deferred to the next slice. Full 3-layer adversarial review (security-plane
change): no confirmed bugs; noted residual — `qualified` (this lens) and `applicantApproved` (the
convergence lens) are independently materialized and could transiently disagree, dormant until an
FE reads the new field (the same cross-lens materialization-timing class already accepted for
other display scalars, not a new hazard).
Worktree: `/Users/andrewsolgan/Documents/GitHub/lattice-wt-vault-5b-ii-b` (branch
`steward-vault-5b-ii-b`, merged through `13ffb75`).

**Fire 5b-ii-c CHECKPOINT (2026-07-02, Lattice Steward, `7eb3330`).** Console retirement shipped:
Approve/Decline now render on `renderRLSApplicantRow` (the RLS-scoped card list), gated on the
protected lens's own `qualified` column, reusing the existing `DecideLeaseApplication` `/api/op`
POST (`entityKey` → `leaseAppKey`, no new endpoint needed). The trusted operator console
(`renderApplicantRow`) drops its Approve/Decline block entirely — informational only now, kept for
unit/listing management (Edit / Unpublish / Relist / Photos). This wakes the dormant
cross-lens-materialization risk the 5b-ii-b checkpoint flagged (`qualified` vs `applicantApproved`
independently projected): both still derive the identical readiness formula off the same
underlying facts, so a landlord sees a transient mismatch only within one lens's reprojection
lag window, never a wrong final state — accepted, same class as other display-scalar staleness
(D1.3). Verified in-browser (no seed identities on the current dev stack — synthetic RLS rows
injected via the console to drive `renderRLSUnitCard`/`renderRLSApplicantRow` directly): a
qualified+unleased row renders Approve/Decline and posts the correct
`{operationType:"DecideLeaseApplication", payload:{leaseAppKey, decision}}`; an unqualified or
leased-to-another row correctly suppresses actions; the console renders zero action buttons.
Lead review only (FE-only, reuses the established gated-button + generic-`/api/op` pattern;
`DecideLeaseApplication`'s own authorization is untouched — no new security mechanism).
**Remaining for 5b close:** extend `make test-crypto-shred` to drive
`landlordLeaseApplicationsReadSpec` through real shred-scrub (still only proven in halves); then
**5b-iii** clinic contact; delivery-boundary reset + a live e2e.

**Fire 5b-ii-d CHECKPOINT (2026-07-02, Lattice Steward, `04bcbf0`).** Investigating the
"real shred-scrub" remainder above surfaced a **severity-1 production bug**, not just a
coverage gap: `readinessWithItems` (the fragment shared by `leaseApplicationCompleteSpec`
*and* `landlordLeaseApplicationsReadSpec`) tested "ssn on file" via `id.ssn.data.value`. Step
6.5 replaces a sensitive aspect's entire `data` field with its ciphertext envelope (`{ct,
nonce, keyId}` — no `value` key), so that hop resolves **null for every real, correctly
Vault-encrypted ssn** — `missing_onboarding` could never close and `applicantApproved` /
`qualified` could never turn true for any real (non-fixture) applicant once Vault is live.
Every existing test used the fixture-only plaintext `{value: "..."}` shape, which masked it —
confirmed by reproducing against a real `vault.LocalBackend`-encrypted envelope before fixing.
**Fix:** test presence at the whole-aspect level (`id.ssn.data`, non-null under both shapes,
never itself returned as a value) — one-line change to the shared fragment, so both consumers
fix together. Added `TestLandlordLeaseApplicationsRead_QualifiedWithRealVaultCiphertext`
(real-ciphertext regression guard); full `packages/lease-signing` suite, `test-lease-convergence`,
and `test-crypto-shred` all green. Lead review (S-sized, non-security-mechanism cypher fix,
fully covered by the new + existing tests). **Still remaining for 5b close:** a `ShredIdentityKey`
run against `landlordLeaseApplicationsReadSpec`'s own committed ciphertext (proving `Vault.Decrypt`
fails post-shred for *this* lens specifically, not just readiness-formula correctness); then 5b-iii.

**Considered and REJECTED — pre-Vault plaintext contact projection** into `clinicPatientsRead`
(technically buildable, no test fails, outside M4's *letter* since `.demographics` cannot be
`sensitive:true` on a non-identity vertex): it ships queryable plaintext PHI into Postgres that
crypto-shred can never erase, dissolves clinic's forcing-function role, and relitigates ratification
decision #2 ("leave the field unencrypted — defeats the point"). Fallback if this build stalls: the D1.5
readiness clone (non-sensitive boolean, technically pre-Vault-buildable) — a Winston-level re-ratification
of Rec C, recorded in the D1.5 design.
