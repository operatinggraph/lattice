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

**Considered and REJECTED — pre-Vault plaintext contact projection** into `clinicPatientsRead`
(technically buildable, no test fails, outside M4's *letter* since `.demographics` cannot be
`sensitive:true` on a non-identity vertex): it ships queryable plaintext PHI into Postgres that
crypto-shred can never erase, dissolves clinic's forcing-function role, and relitigates ratification
decision #2 ("leave the field unencrypted — defeats the point"). Fallback if this build stalls: the D1.5
readiness clone (non-sensitive boolean, technically pre-Vault-buildable) — a Winston-level re-ratification
of Rec C, recorded in the D1.5 design.
