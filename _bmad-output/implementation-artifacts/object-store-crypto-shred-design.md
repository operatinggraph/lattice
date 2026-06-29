# Crypto-shred for object-store blobs — design

**Status: ✅ Andrew-ratified (2026-06-28).** Author: Winston (Designer fire, 2026-06-28). Build-sequenced behind the ratified Vault feature (A+B), which is behind D1 — designed-not-built, on the shelf for the Lattice Steward. Contract #3 §3.11 committed with the ratification.

> Backlog row: `planning-artifacts/backlog/lattice.md` → *Privacy / Vault → [Object Store] Crypto-shred
> for object-store blobs* (★★, M). Filed 2026-06-27 by the prior Designer fire (Vault re-review §2.6) as
> the honest-erasure follow-on to the ratified Vault feature. **Composes on** the Andrew-ratified
> `vault-crypto-shredding-design.md` and the shipped off-graph blob plane
> (`large-file-binary-design.md`, merged 8da5fd4). Grounds in Contract #3 §3.10 (sensitive-aspect
> encryption — the machinery this mirrors), Contract #7 §7.2 (the `core-objects` bytes plane),
> `packages/objects-base/ddls.go` (the `vtx.object.<oid>.content` shape + `AttachObject`),
> `internal/substrate/object.go` (the Object Store wrapper), and `lattice-architecture.md` Items 5 & 6
> (the PII / crypto-shred rubric).

---

## For Andrew (one-look ratification block)

**What it does, in two lines.** Today `ShredIdentityKey` makes a person's **aspect**-PII (SSN/DOB in
Core KV) permanently unrecoverable (§3.10), but their **document**-PII — the lease PDF, the ID scan, the
signature image stored content-addressed and **unencrypted** in `core-objects` — survives the shred, so
right-to-erasure is *silently partial*. This design encrypts a `sensitive: true` object's bytes client-side
under a per-object key that is **wrapped by the same per-identity DEK §3.10 already mints**, so the *one*
`ShredIdentityKey` that erases the aspect plane also turns every one of that identity's blobs into permanent
gibberish — closing the erasure claim with **no new key hierarchy** (the §3.10 DEK is the only secret).

**The one decision that needs your call** (designed-through, recommendation given — **not** a Gateway/D1-class
fork): **content-addressed dedup and crypto-shred are mathematically incompatible for the same object.**
Content-addressing wants a *content-derived* key (so identical bytes converge to one stored copy);
crypto-shred wants a *destroyable, identity-held* key (a content-derived key can't be destroyed — anyone
holding the plaintext re-derives it). You cannot have both. **Recommendation:** a `sensitive` object gives
up *cross-identity* dedup — its oid becomes **identity-salted** (`sha256NanoID("object:"+keyId+":"+digest)`),
which *also* closes a real PII **linkage leak** (today two people's identical uploads collapse to one shared
vertex) while keeping *within-identity* idempotent dedup. Non-sensitive objects are **completely unchanged**
(content-addressed, plaintext, deduped). Detail + the rejected alternative (convergent encryption) in §4.1.

**Frozen-contract change (staged UNCOMMITTED for you):** a new **Contract #3 §3.11 — "Sensitive-object
(blob) encryption at rest"** (sibling to §3.10), in `docs/contracts/03-mutation-batch-event-list.md`. It is
the *blob* analog of §3.10 and reuses §3.10's per-identity DEK verbatim. The diff is the proposal. **No
other frozen contract changes** — Contract #7 §7.2 (the bytes plane) is judged *unchanged* (§5.2): the plane
already carries opaque bytes "with no graph state"; ciphertext is still opaque bytes.

**Architectural fork status:** **none introduced.** The Vault dependency is *inherited* (already ratified)
— this design adds no new fork. It is **build-sequenced behind Vault** (which is behind D1), exactly like
its parent and siblings (control-plane-authz, Personal Lens). The *design* is ratifiable now and goes on the
shelf; the Steward builds it when Vault Phase A+B lands.

---

## 1. Problem & intent

### 1.1 The gap (grounded)

The off-graph blob plane (shipped, `large-file-binary-design.md`) stores binary blobs — profile photos,
**lease PDFs, ID scans, signed-document images** — in the `core-objects` NATS Object Store. The graph holds
a content-addressed pointer; the bytes live off-graph:

```
vtx.object.<oid>.content   aspect: { digest, size, contentType, storeName }   # oid = sha256NanoID("object:"+digest)
lnk.object.<oid>.<linkName>.<ownerType>.<ownerId>                              # object -<rel>-> owner (Contract #1 §1.1)
core-objects[storeName]    = the raw bytes (PLAINTEXT today)                   # §7.2 bytes plane, written client-side
```

Per Contract #7 §7.2 the bytes are streamed **directly to `core-objects` by trusted clients** (Loupe /
the vertical apps) — a *non-Processor* write path ("the off-graph blob plane, parallel to Health-KV").
Because the bytes never pass through the Processor, the §3.10 commit-path encryption middleware **never
touches them**: they are stored **plaintext**.

The Vault feature (`vault-crypto-shredding-design.md`, ratified) makes `sensitive: true` *aspects*
crypto-shreddable: `ShredIdentityKey(identity)` destroys the per-identity DEK, and every aspect ciphertext
wrapped under it becomes permanent gibberish in live KV *and* in the immutable JetStream history (§3.10).
**But it does not — cannot — reach the blob plane.** So after a right-to-erasure shred:

- ✅ the applicant's SSN/DOB aspect → unrecoverable;
- ❌ the applicant's **uploaded ID scan / signed lease PDF** in `core-objects` → still there, still
  readable.

Right-to-erasure is therefore **complete for aspect-PII and partial for document-PII** — and silently so.
The Vault design's own §2.6 flags exactly this and files this row so "the right-to-erasure claim is honest,
not silently partial."

### 1.2 Intent

Encrypt a `sensitive: true` object's bytes at rest under a key bound to the governing identity, so that the
**same `ShredIdentityKey`** that erases the aspect plane also renders the blob unrecoverable — **one shred,
both planes, one secret**. Mirror §3.10's guarantees and safety posture (opt-in decryption; ciphertext-safe
by default) on the blob plane, reusing §3.10's per-identity DEK with **no new key hierarchy**.

---

## 2. Grounding summary (the patterns this mirrors)

| Existing pattern | Where | What this design reuses |
|---|---|---|
| **Per-identity DEK + wrapped-key custody** | §3.10; `vtx.identity.<id>.piiKey` (the *wrapped* DEK, never key material) | The §3.10 DEK is the **KEK** here — the only secret; `ShredIdentityKey` already destroys it |
| **Opt-in decryption / ciphertext-safe default** | §3.10 "Readers" | A general reader observes ciphertext; plaintext only via a Vault unwrap (trusted tool / Secure Lens) — no read-path auth needed for the *default* path |
| **Envelope encryption** | the Vault SPI (`internal/vault`) wraps/unwraps small key material, never bulk data | A per-object **CEK** encrypts the bulk bytes locally; the Vault wraps only the small CEK |
| **Client-side bytes plane** | §7.2; `cmd/loupe/objects.go`, `cmd/loftspace-app/objects.go` (POST → stream → `AttachObject`) | The uploader encrypts before streaming — the natural seam (the Processor never sees the bytes) |
| **`AttachObject` mint-or-dedup + `.content`** | `packages/objects-base/ddls.go` | Extended with `sensitive` + an `encryption` sub-field; sensitive ⇒ identity-salted oid |
| **GC by ownership** | `objectLiveness` lens → Weaver `directOp(TombstoneObject)` → `object-store-manager` byte-reclaim | **Unchanged** — orthogonal to shred; reclaims the (now-inert-ciphertext) bytes when ownership → 0 |

---

## 3. The shape

### 3.1 Envelope encryption — bulk bytes never reach the Vault

A `sensitive` blob is encrypted with a **per-object Content Encryption Key (CEK)**; the CEK — *not* the
bytes — is wrapped under the governing identity's §3.10 DEK. This is the standard envelope pattern and is
the **only** correct shape here: encrypting the bulk bytes directly under the DEK would force the bulk data
through the Vault (the Vault must hold no plaintext PII), whereas wrapping a 32-byte CEK keeps the Vault a
pure key-custody service.

```
CEK              = 32 random bytes (per object, per upload)
ciphertext       = AES-256-GCM(CEK, nonce, plaintextBytes)          # encrypted client-side, by the uploader
wrappedCEK       = Vault.WrapKey(governingIdentity, CEK)            # the Vault wraps only the small CEK
keyId            = governingIdentity's piiKey reference (== §3.10's keyId)
```

`ShredIdentityKey(governingIdentity)` destroys the §3.10 DEK ⇒ `wrappedCEK` can never be unwrapped ⇒ the
CEK is gone ⇒ the ciphertext (live `core-objects` *and* any backup) is permanent gibberish. **The §3.10 DEK
is the single point of erasure for both planes.**

### 3.2 The `.content` aspect (extended)

```
vtx.object.<oid>.content   aspect: {
    digest, size, contentType, storeName,                          # unchanged (digest = PLAINTEXT digest, for integrity)
    sensitive:   true,                                             # NEW — marks the encrypted-at-rest object
    encryption:  { algo: "AES-256-GCM", nonce, wrappedCEK, keyId } # NEW — the envelope (useless without the DEK)
}
```

`wrappedCEK`, `nonce`, `keyId` live in Core KV (written through the Processor by `AttachObject`, P2-clean)
— they are **safe in plaintext**: a wrapped CEK is inert without the identity DEK, exactly as §3.10's
`{ct, nonce, keyId}` envelope is. `digest` stays the **plaintext** digest: it is the post-decrypt integrity
claim (verified alongside the GCM tag), not a storage-integrity claim over the ciphertext.

### 3.3 The oid — identity-salted for sensitive objects (the §4.1 decision)

| Object | oid derivation | Dedup | Bytes at rest |
|---|---|---|---|
| **non-sensitive** (today) | `sha256NanoID("object:" + digest)` | content-addressed (cross-identity) | plaintext |
| **sensitive** (new) | `sha256NanoID("object:" + keyId + ":" + digest)` | **within-identity only** | ciphertext |

Identity-salting keeps the property that makes content-addressing valuable for idempotency — a crash-retry
or a same-identity re-upload of the same document **collapses to one vertex** (deterministic oid, same
governing identity ⇒ the surviving ciphertext is decryptable by that identity) — while dropping
*cross-identity* convergence, which we *want* dropped (§4.1). `AttachObject` branches on `sensitive`:
sensitive ⇒ salt the oid with `keyId` and require the `encryption` block; non-sensitive ⇒ the existing
content-addressed path, byte-for-byte unchanged.

### 3.4 Read path (P5-preserving, opt-in decrypt)

Mirrors §3.10's "Readers" exactly:

- **Default read** (`GET /api/objects/<oid>` in Loupe; a vertical app's object fetch): serve the
  **ciphertext** with its existing `octet-stream + attachment` anti-XSS posture. A sensitive object served
  to an un-privileged reader is unreadable **by construction** — no read-path authorization required (the
  same safety insight that lets §3.10 ship ahead of D1).
- **Plaintext read** (a trusted tool — Loupe — or a D1-authorized Secure-Lens consumer): fetch ciphertext +
  `.content.encryption`, call `Vault.UnwrapKey(keyId, wrappedCEK) → CEK`, decrypt locally, verify the GCM
  tag **and** re-hash to confirm the plaintext `digest`. This is **not** a Core-KV read of a typed vertex
  and **not** a new lens — it is the same trusted Vault-decrypt seam §3.10 already defines, extended from
  "decrypt aspect" to "unwrap a CEK." P5 is untouched: the read-model plane never carries plaintext PII
  (the lens projects ciphertext-pointer metadata only; the GC lens `objectLiveness` reads no bytes).

### 3.5 Write path (P2-clean)

The bytes are written on the §7.2 **non-Processor** plane (uploader → `core-objects`), encrypted *before*
the stream. The **graph** record — `vtx.object` + `.content` (incl. the `encryption` envelope) + the
ownership link — is written **through the Processor** via `AttachObject` exactly as today (P2). Nothing about
the write *path* changes; only *what the client streams* (ciphertext) and *what `AttachObject` records* (the
envelope + `sensitive`).

The uploader resolves the **governing identity** (`encryption.keyId`) — the identity whose
erasure-right governs the document, usually the person the PII is about (e.g. the lease applicant). For a
self-service vertical the owning identity is the obvious choice; the upload API carries it explicitly
(`governingIdentity`), never inferred by the platform (type-agnostic, D7).

### 3.6 Crypto-shred & GC interaction

- **Shred is automatic** for blobs once Fire 1 ships: because every sensitive blob's CEK is wrapped under
  the identity's §3.10 DEK, `ShredIdentityKey` (which already destroys that DEK) makes every one of that
  identity's blobs unrecoverable with **no blob-specific shred logic**. The *guarantee* is key-destruction,
  not byte-deletion (identical to §3.10).
- **GC is unchanged and orthogonal.** The existing `objectLiveness` → `TombstoneObject` →
  `object-store-manager` pipeline reclaims bytes when ownership drops to zero. A shredded-but-not-yet-GC'd
  blob is **inert ciphertext** — harmless — until GC reclaims it. We deliberately do **not** make
  `ShredIdentityKey` eagerly tombstone the identity's blobs: key-destruction is the erasure guarantee, and
  eager byte-deletion would (a) require the shred path to *enumerate* an identity's objects (a scan / a new
  lens — cost for no correctness gain) and (b) duplicate the GC that already runs. This mirrors the
  parking-lot ruling that converged-marker tombstoning "buys cleanup not correctness." (A future
  `@every` blob-sweep that reclaims orphaned inert ciphertext is a possible ops nicety, **explicitly out of
  scope** here — and would be that primitive's first real consumer if ever wanted.)

---

## 4. Decisions resolved (decide-don't-defer)

### 4.1 Content-addressed dedup vs. crypto-shred — the core tension

**Claim:** you cannot content-address-dedup a blob *and* crypto-shred it under one identity. Proof:

- **Content-addressed dedup** requires identical plaintext → identical stored ciphertext → so the
  encryption key must be a deterministic function of the *content* (convergent encryption, `CEK =
  KDF(plaintext)`), or there's no convergence.
- **Crypto-shred** requires the key to be **destroyable** — held by/derivable only via a secret you can
  obliterate. A *content-derived* key is **not** destroyable: anyone holding the plaintext re-derives it
  forever. So convergent encryption **defeats** crypto-shred.
- Convergent encryption additionally leaks via **confirmation-of-a-file**: an attacker who *guesses* a
  low-entropy plaintext (an SSN on a known form template) derives the key and confirms the guess. For PII
  this is a real weakness.

**Three options:**

| Option | Dedup | Shreddable | Verdict |
|---|---|---|---|
| **A — Convergent encryption** (CEK = KDF(plaintext)) | cross-identity | **No** (key re-derivable) | ❌ defeats the whole feature |
| **B — Per-identity-salted oid + random CEK** *(recommended)* | within-identity | **Yes** | ✅ shreddable; closes a linkage leak; within-identity idempotency kept |
| **C — Single shared object + multi-recipient wraps** (one CEK, wrapped per owner) | cross-identity | Yes (per-wrap) | ⚠️ correct but heavier; deferred (§4.2) |

**Recommendation: Option B.** A sensitive object's oid is `sha256NanoID("object:"+keyId+":"+digest)`.
Cross-identity dedup is dropped — and that is a *feature*, not a cost: today two different people uploading
the byte-identical document collapse to **one shared `vtx.object` with both as owners**, a genuine PII
**linkage leak** (the graph now asserts these two identities share a document). Dropping cross-identity
convergence closes that leak. The dedup we keep — *within* one identity (crash-retry, re-upload) — is the
only dedup that matters for idempotency, and it survives (deterministic oid + same governing identity).
Storage cost: two people's *actual* PII documents are different bytes anyway, so cross-identity dedup saved
~nothing for this class.

### 4.2 Multi-party shared documents (e.g. a lease both parties may erase)

A document two identities each have an erasure right over (a lease signed by landlord + tenant). Two shapes:

- **B-default — separate per-identity copies** *(recommended).* Identity-salting *naturally* yields one
  encrypted object per party, each independently shreddable. Each party legitimately retains their own copy;
  shredding one is independent of the other. Simple, no shared-CEK custody problem.
- **C — one object, a `wraps[]` list** (`encryption.wraps = [{keyId, wrappedCEK}, …]`, the CEK wrapped once
  per governing identity; shredding one party drops its wrap; the object dies when the last wrap is gone).
  Saves the duplicate bytes but adds shared-custody complexity and a multi-wrap `AttachObject`/shred path.

**Recommendation: ship B-default** (separate copies). The bytes for a lease PDF are small; independent
shreddability is the privacy-correct default; the storage saving of C is marginal. The `.content.encryption`
shape is authored so a future `wraps[]` is an *additive* extension if a real high-volume shared-document
case ever lands — not designed in now (no consumer).

### 4.3 Where encryption happens — the uploader, not a new component

The uploader (a **trusted client** under §7.2) already streams the bytes; it is the one place that holds the
plaintext and is the natural encryption seam. It generates the CEK, encrypts locally (bulk bytes never leave
the client), and calls the Vault to wrap *only* the CEK. Rejected: a "staging + Processor/manager encrypts"
flow — it creates a plaintext-at-rest window and a new cross-component data path for zero benefit.

### 4.4 Reuse the §3.10 DEK as the KEK — no new key hierarchy

The CEK is wrapped under the **same** per-identity DEK §3.10 mints and `ShredIdentityKey` already destroys
(`vtx.identity.<id>.piiKey`). No new per-object identity key, no new shred op, no second secret to manage —
the aspect plane and the blob plane share one DEK and one erasure action. This is the decision that makes
the feature *small*.

---

## 5. Contract surface

### 5.1 Contract #3 — NEW §3.11 (frozen change; staged UNCOMMITTED for Andrew)

A new subsection in `docs/contracts/03-mutation-batch-event-list.md`, sibling to §3.10, is staged
uncommitted (the diff is the proposal). It is the blob analog of §3.10: a `sensitive: true` object's bytes
are ciphertext at rest (AES-256-GCM under a per-object CEK); the CEK is wrapped under the governing
identity's §3.10 DEK via the Vault; the envelope `{algo, nonce, wrappedCEK, keyId}` lives on `.content`; the
oid is identity-salted (no cross-identity dedup); readers observe ciphertext (opt-in Vault-unwrap, §3.10
posture); `ShredIdentityKey` destroys the shared DEK and renders both planes unrecoverable. **Affected
consumers named in the edit:** `objects-base` (the `AttachObject` DDL + `.content` schema), the bytes-plane
uploaders (Loupe / vertical apps), and the Vault SPI (`WrapKey`/`UnwrapKey`).

### 5.2 Contract #7 §7.2 — judged UNCHANGED (no edit)

§7.2 describes the bytes plane as "byte writes [that] carry no graph state and never touch the Capability
Lens." Ciphertext is still opaque bytes with no graph state — the plane's *contract* is unchanged; what the
client chooses to write (ciphertext vs plaintext) is not a plane-shape concern. The security-relevant fact
(sensitive bytes are client-encrypted) is documented where it belongs — §3.11 and `docs/components/`. **No
§7.2 edit.** (Stated explicitly so the judgment can be overridden.)

### 5.3 Package / DDL (not frozen — `objects-base` build work)

`AttachObject`'s payload schema gains optional `sensitive` + `encryption{algo,nonce,wrappedCEK,keyId}` +
`governingIdentity`; the script branches the oid derivation and requires the envelope when `sensitive`.
DDL change ⇒ the out-of-band `make verify-package-objects-base` gate (see the verify-package memory).

---

## 6. Migration & test strategy

**Migration: purely additive, dormant until a consumer opts in.** Nothing declares a `sensitive` object
today (exactly like §3.10's "nothing declares `sensitive: true` today"), so there is **no data migration** —
no existing blob is retro-encrypted (and could not be: re-encrypting a content-addressed object would change
its identity). The non-sensitive path is byte-for-byte unchanged; the first sensitive object appears only
when a vertical opts in (Fire 4). Bootstrap version unchanged (no new primordial entity).

**Tests:**
- Round-trip: encrypt-on-upload → ciphertext at rest → Vault-unwrap → plaintext + GCM-tag + digest verify.
- **Erasure (the headline)**: `ShredIdentityKey(id)` → `UnwrapKey` fails → the blob is permanently
  undecryptable; assert the bytes-at-rest are not plaintext and the plaintext is unrecoverable.
- Ciphertext-safe default: a non-privileged `GET` returns ciphertext (octet-stream/attachment), never
  plaintext.
- No cross-identity dedup: two identities upload identical bytes → **two** `vtx.object` vertices (distinct
  identity-salted oids), no shared-ownership linkage. Within-identity re-upload **dedups** to one.
- GC orthogonality: a shredded blob whose ownership → 0 is still reclaimed by the existing
  `objectLiveness`→`TombstoneObject`→`object-store-manager` path (inert ciphertext, harmless meanwhile).
- **Gate-3 adversarial vector**: a reader without Vault-unwrap capability cannot obtain plaintext for a
  sensitive object (DEFENDED) — added to the capability-adversarial suite alongside §3.10's.

---

## 7. Risks & alternatives

- **Risk — uploader trust.** Encryption happens client-side, so a buggy/hostile uploader could stream
  plaintext under `sensitive: true`. *Mitigation:* the uploaders are the **trusted** §7.2 clients (Loupe /
  the platform's own vertical apps) — the same trust the bytes plane already assumes; a malicious uploader
  could already write arbitrary bytes. A future integrity check (the `object-store-manager` sampling that a
  `sensitive` object's bytes are not low-entropy plaintext) is a possible hardening, **out of scope**.
- **Risk — Vault availability on the read path.** A plaintext read needs the Vault online to unwrap. This is
  identical to §3.10's aspect-decrypt dependency and inherits its posture (the Vault SPOF / availability
  decisions in the ratified Vault design); the *default* (ciphertext) path needs no Vault.
- **Alternative — encrypt the whole `core-objects` bucket at rest (storage-level).** Rejected: bucket-level
  encryption is not *per-identity* and not *destroyable per person* — it protects against disk theft, not
  right-to-erasure. Crypto-shred fundamentally needs per-identity keying.
- **Alternative — keep blobs plaintext, rely on GC-on-erasure.** Rejected: erasure would then mean
  *enumerate + tombstone + wait for byte-reclaim* (eventually-consistent, scan-based, and it cannot reach
  JetStream-history copies the way key-destruction does). §3.10 deliberately chose crypto-shred over delete
  for exactly these reasons; the blob plane should match.

---

## 8. Fire-by-fire decomposition (for the Lattice Steward — build after Vault A+B)

Each fire is independently shippable + green. **Prerequisite: the Vault feature (Phase A+B) has landed** —
this design needs §3.10's per-identity DEK + the `internal/vault` SPI. Build order:

1. **Fire 1 — the platform seam (dormant, additive).** Extend the Vault SPI with `WrapKey(identityRef, key)
   → wrappedKey` / `UnwrapKey(identityRef, wrappedKey) → key` (small-key envelope ops, exposed to trusted
   callers — the §3.10 decrypt-seam, generalized). Extend the `objects-base` `AttachObject` DDL + `.content`
   schema with `sensitive` + `encryption{…}` + `governingIdentity`, and branch the oid derivation
   (identity-salted when sensitive). Unit tests: oid-salting golden values, the envelope round-trips, the
   non-sensitive path is byte-identical. **No behavior visible yet** (no uploader sets `sensitive`). Review:
   capability/crypto plane ⇒ **full 3-layer** (this is the load-bearing increment). `make
   verify-package-objects-base`.
2. **Fire 2 — the trusted-client encrypt/decrypt path (Loupe).** `cmd/loupe/objects.go`: on upload of a
   `sensitive` object, generate the CEK, AES-256-GCM-encrypt, `Vault.WrapKey`, stream ciphertext, submit
   `AttachObject` with the envelope; on `GET`, default-serve ciphertext and offer an authorized
   `Vault.UnwrapKey` + decrypt path. In-browser-verified on the running stack (per the Loupe unattended-verify
   pattern). Full 3-layer (security-plane FE/handler).
3. **Fire 3 — erasure coverage + the adversarial gate.** The test that `ShredIdentityKey` makes a blob
   unrecoverable (the headline guarantee) + the Gate-3 ungranted-unwrap DEFENDED vector. (Shred itself needs
   no new code — it falls out of Fire 1's DEK-wrapping — so this fire is the *proof*, plus the multi-party
   B-default coverage from §4.2.)
4. **Fire 4 — a real vertical consumer (the demand proof, not dead scaffolding).** Flip one genuine PII
   blob to `sensitive` — the **loftspace lease-signing signed-PDF** or a **clinic ID-scan** — end-to-end
   through Fires 1–2, so the feature ships *with* a consumer that exercises it (mirrors how every recent
   platform design ships with its first consumer in-initiative). Vertical-stream territory, so it can land
   from either steward.

> **Sequencing on the board:** behind D1 → behind Vault (A+B) → this. The design is ratifiable now and sits
> on the shelf; the Steward picks Fire 1 once Vault has shipped.

---

## 9. Open ratification items (for Andrew)

1. **The §4.1 decision** — confirm sensitive objects give up *cross-identity* dedup (identity-salted oid),
   accepting the deliberate divergence from the content-addressed plaintext path (rec: yes — it also closes
   the PII linkage leak).
2. **The new Contract #3 §3.11** — review the uncommitted edit; ratify or adjust. (Confirm the §7.2
   no-change judgment, §5.2.)
3. **Multi-party shape (§4.2)** — confirm B-default (separate per-identity copies) over the multi-wrap C
   (rec: B-default; C deferred unless a real shared-document volume case lands).
4. **Sequencing** — confirm build-behind-Vault (which is behind D1), design-on-the-shelf now.

**Ratification state: 📐 awaiting-Andrew → ✅ Andrew-ratified (then the Lattice Steward builds, after Vault).**
