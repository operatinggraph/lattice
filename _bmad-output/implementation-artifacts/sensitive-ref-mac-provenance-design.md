# Processor-MAC'd sensitive-refs — ref provenance for the external-egress unwrap

**Status: 📐 awaiting-Andrew (ratification).**
Author: Winston (Designer fire, 2026-07-12).
Backlog row: `planning-artifacts/backlog/lattice.md` → *Security & trust boundary → Processor-MAC'd
sensitive-refs (ref provenance)*.
Parent design: `sensitive-param-egress-design.md` (✅ ratified 2026-07-10; Fires 1–2 shipped) — this is
its §3.6 named follow-on, with the ratified trigger **"before AI-authored DDLs ship."**
Consumer that forces it now: **AI-caps Fire 4** (`ai-authored-capabilities-design.md` §8 — sign-off
GRANTED 2026-07-10 with this as **condition 1**; Fire 4's materializer kinds stay blocked until it lands).

---

## For Andrew (one-look ratification block)

**What it does, in two lines.** The Processor stamps every `$sensitiveRef` it authors with an HMAC
(key derived from the master KEK it already holds), and the bridge's egress unwrap moves to a new
**`lattice.vault.decryptref`** RPC that verifies the stamp before decrypting — so a *fabricated* ref
(hand-crafted by a malicious or AI-authored DDL naming another identity's aspect) can no longer be
decrypted by the honest bridge, and the bridge's own transport grant shrinks from "decrypt anything"
to "decrypt only Processor-provenance-verified egress refs."

**Why now (the two-pronged closure for AI-authored DDLs).** Fire 4's static lint (sign-off condition 2)
rejects AI artifacts carrying `$sensitiveRef` **literals** or **sensitive-key read declarations** — the
*declaration* channel. But no static lint catches a script that **computes** a marker at runtime
(`{"$sensitiveRef": {"ref": "vtx.identity." + x + ".ssn", …}}`) and returns it as a param value; the
§3.6 emission guard doesn't trip either (it keys on plaintext *decrypted this execution* — a fabricated
marker decrypts nothing Processor-side). The MAC closes exactly this *fabrication* channel: only
Processor-authored markers verify. Condition 2 (static lint) + condition 1 (this design) together cover
the **external-egress marker channel's** two prongs (declaration + fabrication); neither alone suffices.
(A third, non-egress channel — a script *computing* a sensitive key for a lazy `kv.Read` and exfiltrating
the plaintext via a non-`external.*` event or a plain-aspect write — is outside both conditions and is
closed by Fire 4's other gate, the verified-pure `internal/starlarksandbox` read-confinement; named in §4
so the two conditions aren't misread as self-complete for AI sign-off.)

**The one real design call — a dedicated verified RPC, not optional fields on the existing one
(my recommendation, trade-off below).** Verification must be Vault-side (only the KEK-holder can check
the MAC), and I put it on a **new subject** `lattice.vault.decryptref` with the MAC **mandatory**, then
**move the bridge's natsperm grant** from `lattice.vault.decrypt` to it. Optional-MAC-on-the-existing-RPC
(Alternative A) verifies nothing a compromised bridge can't skip by omitting the fields. With the swap,
the parent design's honestly-stated §8 blast radius ("a compromised bridge could decrypt the identity
corpus — same power as Loupe") genuinely shrinks: a compromised bridge can decrypt only tuples the
Processor actually minted for egress. Loupe keeps the wholesale RPC unchanged (the inspector exception).

**No new secret, no key distribution.** Mint (Processor hydration) and verify (the decrypt responder)
both live in `cmd/processor`, which already holds the master KEK; the MAC key is derived from it
(purpose-labeled HMAC), stdlib-only. The `Vault.MAC` primitive this adds is also the "new Vault method"
the shelved *keyed identity-index hashes* row (`dedup-over-encrypted-pii-design.md` §10-C) said it would
need — that item stays shelved; only the ref consumer is built.

**Frozen-contract changes — three small edits, staged UNCOMMITTED in `main` (the diffs are the
proposal):** #10 §10.5 loom-shard (the marker shape gains `mac`; the bridge unwraps via the ref-verified
RPC), #3 §3.10 (the **ref-provenance rule** joins the live-envelope rule; the egress consumer decrypts
only through the verified RPC), #2 §2.3/§2.5(f) (one-phrase: the sensitive-ref is Processor-
authenticated). Details §5. Two fires for the Steward (§8); Fire 4 of AI-caps builds its lint against
the §7 reject set defined here.

---

## 1. Problem & intent

### 1.1 What is trusted today (ratified, deliberately)

The egress design ratified (For-Andrew #2, 2026-07-10): *"refs are trusted at the package-DDL boundary,
not countersigned"* — accepted because a malicious installed DDL already has equal power via plain
`reads` (declare any identity's sensitive key, receive plaintext). Package-install trust is the
boundary, and the hardening follow-on was named **with its trigger**: Processor-MAC'd refs *before
AI-authored DDLs ship* (AI-caps Fire 4). Andrew granted Fire 4 sign-off on 2026-07-10 **conditioned on
this landing first**; verified 2026-07-11 that no code exists anywhere — this design is that pass.

### 1.2 The channel that opens at Fire 4 (grounded in the shipped mechanism)

The shipped unwrap chain trusts marker *shape*, not marker *origin*:

- The Processor authors `{"$sensitiveRef": {ref, ciphertext}}` at hydration for `egressReads`-declared
  sensitive aspects (`internal/processor/sensitive_decrypt.go:60`).
- The `resolve_subject_params` helper pattern-matches the marker and appends `field`
  (`packages/orchestration-base/external_params.go` — `egress = dict(sref)`, a whole-object copy).
- The bridge detects any params value carrying the key `$sensitiveRef`, parses `{ref, ciphertext,
  field}`, fetches the identity's live envelope, and calls `lattice.vault.decrypt`
  (`internal/bridge/egress.go:85,158`). **Nothing anywhere checks that the Processor authored the
  marker.** A Starlark script that *returns a computed dict of that shape as a param value* gets it
  unwrapped: the victim's at-rest ciphertext is account-readable (`$JS.API.DIRECT.GET` — the natsperm
  read-laxity the egress design flagged), and even without substrate access the ciphertext need not be
  *valid* for the attack to be attempted — but with it, the honest bridge decrypts a chosen identity's
  aspect into a vendor call.

For **human-authored** packages this is today's install-trust boundary (unchanged by this design). For
**AI-authored** DDLs (Fire 4), install trust is deliberately weakened — the artifact is generated code —
and the two defenses that exist are blind to fabrication: the **static lint** (condition 2) cannot see a
marker assembled at runtime from string parts, and the **§3.6 emission guard** keys on
`sensitiveReadTracker.plaintextRead`, which a fabricated marker never sets (nothing was decrypted).

### 1.3 Intent

(a) A `$sensitiveRef` is decryptable at the egress boundary **only if the Processor authored it** —
fabrication fails closed at the KEK-holder, independent of bridge or script correctness. (b) A marker is
additionally **splice-bound to its minting op's `requestId`** — a harvested marker cannot ride a
different op's event unless that op reuses the exact original `requestId`, which the idempotency tracker
dedupes for 24h (`TrackerTTL`); this is an operational bar-raiser, **not cryptographic freshness** (§3.2
states the property honestly). (c) The bridge's decrypt authority shrinks to exactly the
Processor-minted set (transport-enforced, natsperm-vector-proven) — against a *compromised bridge* this
grant swap is the entire reduction (it already holds every tuple it ever consumed; no binding helps
there). (d) Zero new key custody surface: no new secret, nothing distributed, Starlark never sees key
material. (e) The lint reject set for AI-caps Fire 4 (condition 2) is precisely defined so that fire
builds against a spec, not a vibe.

## 2. Grounding — what exists (the pattern this extends)

- **Mint site (one function, two seams).** `decryptSensitiveDoc(…, egress bool, …)` is the single
  sensitivity authority — step-4 hydration calls it for `reads`/`optionalReads` (plaintext disposition)
  and `egressReads` (ref disposition, `step4_hydrate.go:254`); the lazy seam consults the same egress
  set (`starlark_kv.go:347`). The ref branch today needs no live Vault ("authoring needs only the DDL
  lookup + the ciphertext in hand"). The MAC changes that *when a Vault is wired* — §3.2 keeps the
  vaultless-harness posture intact.
- **The requestId is in scope at both seams.** Step 4 already threads `rid` (`OperationRequestID` in
  `HydrationError`); `connKVReader` is constructed by the Hydrator, which holds the same envelope. The
  external event's top-level envelope carries the emitting op's `requestId` (Contract #3 §3.4; the
  bridge's `eventBody` comment names it, `dispatch.go:19` — the bridge just doesn't read it yet).
- **Verify site = the mint site's own process.** The Vault backend and the decrypt RPC responder are
  hosted **in the Processor** (`cmd/processor/main.go:277` — `vault.NewService(v)` on the same
  `LocalBackend` the hydrator uses). Key custody for a MAC is therefore trivial: derive from the KEK
  both ends already share by identity.
- **The RPC caller set is exactly two.** natsperm grants `lattice.vault.decrypt` publish to the
  **bridge** (`internal/natsperm/matrix.go:208`) and **Loupe** (`matrix.go:286`); the vertical apps hold
  only wrap/unwrap. The grant swap touches one line + vectors.
- **The marker rides through the helper untouched.** `dict(sref)` copies every inner field — a `mac`
  field needs **zero helper change**.
- **The bridge's failure taxonomy already exists.** Permanent-vs-transient egress failures with the
  terminal failed `replyOp` convergence path (`egress.go:37-58`, design §3.5) — a MAC reject is one more
  **permanent** arm.
- **Vendor grounding (pins per `docs/vendors.md`).** HMAC-SHA256 via Go stdlib `crypto/hmac` +
  `crypto/sha256` (constant-time `hmac.Equal`); no new dependency (`golang.org/x/crypto` stays
  indirect). Key derivation is a single-block NIST SP 800-108 KDF-in-HMAC form:
  `macKey = HMAC-SHA256(KEK, "lattice/mac/" + purpose)` — domain-separated from AES-GCM use of the KEK
  (the KEK never directly MACs data). Production KMS backends map 1:1: HashiCorp Vault Transit HMAC
  endpoints / AWS KMS `GenerateMac`/`VerifyMac` implement the same primitive server-side.

## 3. The shape

### 3.1 `Vault.MAC` — one purpose-labeled primitive

```go
// MAC computes a keyed MAC over data under a purpose-scoped key derived from
// the backend's platform secret. Purpose strings are frozen constants
// ("sensitive-ref/v1"); distinct purposes yield independent keys.
MAC(ctx context.Context, purpose string, data []byte) ([]byte, error)
```

`LocalBackend`: `HMAC-SHA256(HMAC-SHA256(kek, "lattice/mac/"+purpose), data)`, derived key memoized
per purpose. **Platform-scoped, not per-identity** — deliberately *not* gated by the shredded-set: a
shred must kill *decryption* (the live-envelope rule does), not the ability to *recognize* a
Processor-minted marker; a post-shred replay must fail at the decrypt with `ErrKeyShredded` (the
observable the e2e pins), not decay into an unverified-looking reject. Verification is
recompute-and-`hmac.Equal` by the caller — no separate Verify method until a KMS backend (AWS
`VerifyMac`) forces one. This is the "new Vault method" the shelved keyed-index-hashes row needs; that
item stays shelved (its custody problem — Gateway/Starlark hash computers — is its own, unsolved here).

### 3.2 Mint — the marker gains `mac` (+ the requestId binding)

`decryptSensitiveDoc`'s egress branch (both seams — step 4 and the lazy `connKVReader`, which gain a
`requestID` field from the Hydrator):

```json
{ "$sensitiveRef": {
    "ref":        "vtx.identity.<id>.ssn",
    "ciphertext": { "ct": "...", "nonce": "...", "keyId": "..." },
    "mac":        "<base64 HMAC-SHA256>"
} }
```

- **MAC input is length-prefixed raw fields, never JSON** (JSON re-serialization is not canonical; the
  bridge re-parses the marker before forwarding): `u32len(ref)‖ref ‖ u32len(requestId)‖requestId ‖
  u32len(keyId)‖keyId ‖ u32len(nonce)‖nonce ‖ u32len(ct)‖ct`, purpose `"sensitive-ref/v1"`. **Mint-side
  canonicalization trap, pinned:** at the egress seam `doc.Data` holds ct/nonce as **base64 strings**
  (the generic-map decode, `sensitive_decrypt.go:118`'s own comment) — the mint MUST MAC over the
  *decoded bytes* via the existing `ciphertextFromData` round-trip, the same bytes the responder
  receives in `DecryptRefRequest.Ciphertext`, or mint/verify silently diverge (a Fire-1 test vector).
- **`requestId` binds the marker to its minting op — splice-resistance, stated honestly.** The
  instanceOp that hydrates the refs is the op that resolves the templates and emits the
  `external.<adapter>` event — same execution, same `requestId`, carried top-level on the event
  (`step7_events.go:83` sets `Event.RequestID = env.RequestID`). A marker harvested from one op's
  hydrated state and re-emitted by *another* op fails verification **unless the re-emitting op carries
  the original op's exact `requestId`** — and `requestId` is submitter-chosen (Contract #4), deduped by
  the tracker on `requestId` alone with a **24h TTL** (`tracker.go:19`). So the binding is
  **splice-resistance plus a 24h operational reuse bar, not cryptographic freshness** — a determined
  malicious DDL that harvested a marker AND can force the original requestId AND waits out the TTL can
  replay it; true freshness is unachievable without breaking at-least-once redelivery (a responder-side
  seen-set would reject legitimate `DeliverAll` replays — Alternative G′, rejected). The binding still
  pays for itself: it costs one field each side and turns "copy a marker anywhere, ever" into a narrow
  compound. Same-event redelivery re-presents the *same* requestId — verifies, as it must (idempotency
  memoizes pre-shred replays; the live-envelope gate kills post-shred ones — unchanged division of
  labor). `requestId` is NOT carried in the marker — the bridge reads it from the event envelope the
  Processor's outbox wrote, so a script cannot substitute a chosen one at emission time.
- **`field` is deliberately outside the MAC** — it's appended *after* mint by the Starlark helper, and
  it only selects among plaintext fields of an aspect this op legitimately declared for egress; a script
  that can set `field` could equally have templated that field. No added power.
- **Vaultless harness posture preserved:** `v == nil` mints the marker without `mac` (nothing changes
  for the many test pipelines with no Vault — no end-to-end unwrap exists in such a stack anyway, the
  RPC has no responder). `v` present + `MAC()` error → **hydration fails loudly** (never author an
  unauthenticated ref when a Vault is wired — fail closed, the D1 direction).

### 3.3 Verify — `lattice.vault.decryptref`, a dedicated mandatory-MAC RPC

A fourth endpoint on the existing Processor-hosted `micro.Service` (the `vault.decrypt` pattern):

```
DecryptRefRequest  { ref, requestId, envelope, ciphertext, mac }
DecryptRefResponse { plaintext | error }
```

Handler order: (1) parse `ref` — MUST be a well-formed **identity-anchored aspect key**; the
`identityKey` for decrypt is **derived server-side from the verified ref** (the caller-supplied
`identityKey` input of the wholesale RPC disappears — one less attacker-controlled field, and the AAD
binding follows the authenticated value); (2) recompute the §3.2 MAC, `hmac.Equal` — mismatch or
empty → `ErrRefUnverified` (a new sentinel, echoed over the wire like `ErrKeyShredded`;
non-identifying); (3) delegate to the same `vault.Decrypt(identityKey, envelope, ciphertext)` — the
live-envelope shred gate, DEK unwrap, AAD check all unchanged. Same panic-recovery +
generic-error-detail posture as `handleDecrypt`.

**The natsperm swap (the blast-radius shrink).** The bridge's `ExtraPubAllow` entry
`"lattice.vault.decrypt"` (`matrix.go:208`) becomes `"lattice.vault.decryptref"`. Loupe's grant is
untouched. Vectors prove: bridge **denied** on `lattice.vault.decrypt` (a new DENY vector — the old
grant must not linger), bridge **allowed** on `lattice.vault.decryptref`, apps denied on both. A
substrate reader who harvests `(ref, ciphertext, mac, requestId)` tuples from the durable stream can do
nothing with them without the bridge's transport identity — and *with* it, can decrypt only what the
Processor already minted for egress: that residual **is** the reduction, stated plainly.

### 3.4 The bridge — require, forward, classify

`detectSensitiveRef` requires `mac` non-empty → else **permanent** egress failure (fail-closed *before*
any RPC; a pre-MAC marker or a fabricated one never leaves the bridge). `eventBody` gains the top-level
`requestId` field; `resolveSensitiveRef` calls `decryptref` with `{ref, requestId, envelope,
ciphertext, mac}`. `ErrRefUnverified` → **permanent** (terminal failed `replyOp`, the pattern converges
— never retried: a bad MAC cannot become good). Everything else (transient classes, the retry budget,
`ErrKeyShredded` permanent) is byte-identical to the shipped taxonomy.

### 3.5 Invariants

**P2** — unchanged: Core-KV reads stay in the Processor; the bridge still reads no Core KV (one lens
bucket + one RPC — the RPC just changed subject). **P5** — unchanged (the envelope lens read is
untouched). **Contract #1** — the ref is still the canonical 4-segment aspect key. **Starlark stays
pure** — scripts see the marker (as today) but never key material; the MAC is opaque data to them.
**D1 lesson (fail-closed on omission)** — a missing `mac` denies (bridge-side), a present-but-wrong
`mac` denies (Vault-side), a mint failure with a live Vault denies (Processor-side); no arm defaults
open.

## 4. Reconciliation with the existing mental model

- **"Didn't we decide refs are trusted at the DDL boundary?"** Yes — *for human-authored packages*,
  and that ratified posture is unchanged: an installed malicious DDL can still *declare* any identity's
  aspect under `egressReads` and receive a genuinely-minted, valid-MAC ref (install trust is that
  boundary, as it is for plain `reads` plaintext). The MAC does not pretend otherwise; it closes the
  channel that has **no** install-trust gate once Fire 4 ships — runtime fabrication by generated code
  the static lint can't see. (Declared reads in AI artifacts are condition 2's lint, §7.)
- **"Does this duplicate the emission guard?"** No — complementary by construction: the guard polices
  *plaintext that was decrypted*; the MAC polices *refs that were never minted*. A fabricated marker is
  invisible to the guard (nothing decrypted) and dead at the MAC.
- **"Is Fire 4 then fully covered by conditions 1+2?"** For the **external-egress marker channel**, yes
  (declaration → lint; fabrication → MAC). A third channel — an AI script *computing* a sensitive key at
  runtime for a lazy `kv.Read` (plaintext disposition, `sensitive_decrypt.go:68`) and exfiltrating via a
  **non**-`external.*` event or a plain-aspect write — is covered by neither condition and neither
  mechanism here; it is closed by Fire 4's *other* gate, the verified-pure `internal/starlarksandbox`
  read-confinement (AI-caps §3.2/§8 — the sandbox builds WITH Fire 4). All three legs are load-bearing
  for AI-authored DDLs; this design supplies exactly one of them.
- **"New state?"** None durable: one derived in-memory key per purpose, one field on a value convention
  that already rides the event, one RPC subject. No new buckets/vertices/links/lenses.
- **"Didn't the parent design reject countersigning?"** It *deferred* it with a named trigger (dead
  scaffolding before AI DDLs exist); the trigger has now fired — Fire 4 is signed off and blocked on
  this. Building it now is the ratified sequencing, not a reversal.

## 5. Contract surface (edits staged UNCOMMITTED in `main`; diffs are the proposal)

| Contract § | Change vs build-to | Detail |
|---|---|---|
| **#10 §10.5 (loom shard) — externalTask `params`** | **CHANGE (staged)** | The sensitive-ref sentence: marker shape `{ref, ciphertext, mac, field}` — the Processor authenticates `{ref, requestId, ciphertext}` at hydration; the bridge unwraps via the **ref-verified Vault decrypt RPC**; an unverifiable ref is a permanent data error (converge, never dispatch). |
| **#3 §3.10 — Readers + rules** | **CHANGE (staged)** | The **ref-provenance rule** joins the live-envelope rule: a sensitive-ref carries a Processor-minted MAC binding `{ref, requestId, ciphertext}`; the external-egress unwrap consumer decrypts **only** through the ref-verified RPC (mandatory verification); the wholesale decrypt RPC remains for the inspector class (Loupe). |
| **#2 §2.3 table + §2.5 class (f)** | **CHANGE (staged, one phrase each)** | The sensitive-ref parenthetical gains "Processor-authenticated (MAC'd) — see §3.10 ref-provenance". |
| #5 §5.4 Health | build-to | `egress_unverified_total` (bridge) is author-discretion metric space. |
| #4 idempotency / #10 §10.6 | build-to | Unchanged — same-requestId redelivery verifies; correlation/async SPI untouched. |

Wire types (`DecryptRefRequest`), the natsperm matrix line, and the marker field are **code, not
contract** beyond the above.

## 6. Alternatives considered

- **A — optional `ref`/`mac` fields on the existing `lattice.vault.decrypt` (verify-when-present).**
  Rejected: a compromised bridge omits the fields and keeps wholesale decrypt; the transport can't
  distinguish verified from unverified calls, so the grant can't shrink. Could a variant beat B? Only by
  making MAC mandatory on the shared subject — which breaks Loupe (the inspector legitimately decrypts
  ciphertext it read from Core KV, for which no marker/MAC exists) or forces a mint-for-inspector RPC
  that would hand MACs to whoever asks, nullifying provenance. The two caller classes genuinely have
  different trust shapes; two subjects express that.
- **B — dedicated mandatory-MAC RPC + grant swap (RECOMMENDED, §3.3).**
- **C — asymmetric signature (Ed25519) instead of HMAC.** Buys verification by parties *without* the
  KEK — but the only verifier is the KEK-holder itself (the bridge forwards, never verifies), so
  asymmetry is pure cost (new keypair custody + rotation surface). Revisit only if verification ever
  moves off the Processor.
- **D — bind to `instanceKey` instead of `requestId`.** The claim handle is Loom-plane and not in scope
  at the generic hydration seam (and not every future egress emitter need be an externalTask);
  `requestId` is universally present at both mint seams and on every event envelope. Note honestly:
  `instanceKey` would NOT buy freshness either — it too is a durable, replayable token; neither binding
  is a nonce. Rejected on availability, not on a freshness claim.
- **E — MAC over `(ref, ciphertext)` only, no requestId.** Simpler, and the adversarial pass showed the
  requestId binding is weaker than first claimed (splice-resistance + a 24h reuse bar, §3.2 — not
  freshness). It still loses to the binding: E leaves "copy a marker anywhere, ever" open, the binding
  narrows it to an exact-requestId-after-TTL compound, for one already-available field on each side.
  Rejected — with the corollary stated: any future *legitimate* cross-op ref transport must re-mint via
  its own `egressReads` declaration (refs are per-execution artifacts, not durable capabilities).
- **G′ — responder-side freshness (a seen-marker set / nonce check).** The only way to true replay
  immunity, and structurally wrong here: the bridge's consumer is `DeliverAll`-replayable by design —
  a legitimate redelivery presents the identical tuple, indistinguishable from a replay. Freshness
  belongs to the delivery/idempotency plane (which already memoizes), not the decrypt plane. Rejected.
- **F — bridge-side verification (give the bridge the MAC key).** Spreads a KEK-derived secret to a
  second binary for zero gain (the bridge must call the Vault anyway for the DEK); violates the
  "only the Processor holds Vault key material" custody line. Rejected outright.
- **G — TTL/expiry in the MAC.** Breaks legitimate `DeliverAll` redelivery; time is not the security
  boundary here — the live envelope is (shred). Rejected.

## 7. The Fire-4 lint reject set (condition 2 — defined here, built in AI-caps Fire 4)

So the two prongs interlock precisely, the deterministic validator (AI-caps §5, record + approve time)
rejects an AI-authored artifact iff:

1. **`$sensitiveRef` literal** — the string `$sensitiveRef` appearing anywhere in artifact `content`
   (any kind — script source, pattern params, target templates). Refs are Processor-authored; no
   legitimate artifact ever spells the marker.
2. **Sensitive-key read declaration** — any declared read surface naming a `sensitive: true`-classed
   aspect (resolved against the live DDL registry the validator already holds): an opMeta's declared
   reads; a loomPattern step's `params` templates whose `subject.<aspect>` resolves sensitive for the
   pattern's subject type; a weaverTarget template likewise. An AI-authored capability that needs PII
   egress is exactly the case that must route to human authoring.

(The lint stays *advisory pre-flight* for what it can't see — a computed marker — which is precisely
why the MAC, not the lint, is the enforcement for fabrication. Stated so nobody later "hardens" the
lint instead of keeping the MAC.)

## 8. Migration & test strategy — and fires for the Steward

**Migration: none durable.** Pre-prod platform; the dev stack's JetStream is ephemeral. The one
in-flight window (events minted pre-MAC, unwrapped post-upgrade) converges loudly to the failure branch
via the bridge's missing-`mac` permanent arm — acceptable for dev (a re-trigger re-mints); noted, not
engineered around. Deploy order within a live stack: Processor (mint + responder) before bridge
(require) — one release in practice. No bootstrap bump (no kernel entity). capability-author's
non-sensitive egress templates mint no marker — untouched.

| Fire | Scope | Review depth |
|---|---|---|
| **Fire 1 — mint + verify (Vault + Processor + contracts)** | `Vault.MAC` (+ `LocalBackend` impl, purpose-key derivation, memoization); marker gains `mac` at both seams (requestID plumbed to the Hydrator + `connKVReader`); mint-failure fail-closed; `lattice.vault.decryptref` responder (parse→verify→decrypt, `ErrRefUnverified` sentinel, server-derived identityKey). Tests: purpose separation + determinism; both-seam marker table; forged-ref / spliced-ciphertext / wrong-requestId / missing-mac reject table; shredded-still-refused **after** a valid MAC (order pinned); vaultless-harness unchanged. Contract edits ride the ratification commit. | **Full 3-layer** — security-plane. |
| **Fire 2 — the bridge swap + the DEFENDED e2e (bridge + natsperm + lease-signing)** | Bridge: require `mac`, read event `requestId`, call `decryptref`, `ErrRefUnverified` permanent arm; natsperm matrix swap + vectors (bridge DENY `lattice.vault.decrypt`, ALLOW `decryptref`; apps deny both). **Known test blast radius (update, don't mistake for regression):** every existing egress test builds MAC-less markers and runs the wholesale responder — `internal/bridge/egress_test.go` (`seedSensitiveAspect`, the `resolveSensitiveRef` table, `TestUnwrapEgressParams_*`, `startTestVault`) and `internal/leaseconvergence/sensitive_param_egress_test.go` — all re-mint via `Vault.MAC` + the `decryptref` responder. E2e (ephemeral stack): lease-signing happy path still green end-to-end; a **fabricated-marker arm** — a test DDL emits a computed `$sensitiveRef` naming a second identity's aspect → the dispatch is refused, the pattern converges failed, the vendor fake never sees plaintext (the Gate-3-style DEFENDED assertion this design exists for); the shipped shred→restart→replay arm still green (same requestId verifies, live-envelope still kills it). | **Full 3-layer** — PII egress + permission change. |

Sequencing: Fire 2 strictly after Fire 1 (verification needs minted MACs). Then **AI-caps Fire 4
unblocks**: its materializer kinds build with the §7 lint in its own lane. No dark increments — Fire 1's
mint is exercised by every existing sensitive-template path (lease-signing) and its own responder tests;
Fire 2 ships with the live e2e.

## 9. Risks & edges

- **KEK rotation** rotates the derived MAC key with it — in-flight markers minted under the old KEK
  fail verification after a rotation. Same v1 posture as the KEK itself (rotation unimplemented,
  `local.go:114` — one KEK for the backend's lifetime); when rotation lands, `kekVersion` (already in
  every Envelope) keys a grace window. Not built now.
- **A marker's ciphertext goes stale** (aspect updated between mint and unwrap): decrypts fine under
  the identity's DEK — identical to the shipped posture (the op's OCC snapshot rides); the MAC changes
  nothing here.
- **The wholesale RPC still trusts Loupe wholesale** — pre-existing, explicitly out of scope, tracked
  by the natsperm-matrix-hygiene row; this design must not silently absorb it.
- **`egressReads` declarations are not subject-bound** — a *human-authored* DDL's dispatcher can still
  declare an unrelated identity's aspect and get a valid ref (install trust, §4). Loom's inference
  binds template keys to the subject by construction; AI artifacts are lint-blocked (§7). A general
  subject-binding of `egressReads` would be a new authorization surface with its own design — flagged,
  not smuggled in.
- **Health/observability:** the bridge's permanent-failure path already raises Health issues; an
  `egress_unverified_total` counter makes a fabrication *attempt* operator-visible (it is an attack
  signal, not noise — worth the counter).

## 10. Adversarial pass (run this fire, independent agent — findings folded)

An independent adversarial agent attacked the draft against code. **Verdict: no blocker — the core
mechanism stands (fabrication genuinely closed; AAD binding sound; grant swap real) — but it killed the
draft's headline anti-replay claim and found a completeness overstatement.** Folded:

- **F1 (MEDIUM, folded → §1.3(b)/§3.2/§6-D/E/G′):** the draft claimed the requestId binding makes
  "cross-op replay fail closed independent of bridge or script correctness" — **overstated on both
  counts.** `requestId` is submitter-chosen (Contract #4) and the dedup tracker keys on `requestId`
  alone with a 24h TTL (`step2_dedup.go:48`, `tracker.go:19`) — so the binding is splice-resistance
  plus an operational reuse bar, not freshness; and against a *compromised bridge* (which holds every
  tuple it ever consumed off the replayable stream) the binding adds zero — there the entire reduction
  is the mandatory-MAC grant swap. True responder-side freshness was examined (G′) and rejected: it is
  indistinguishable from legitimate `DeliverAll` redelivery. The property is now stated honestly
  everywhere; D/E's rejection rationales were corrected to not rest on a freshness premise.
- **F2 (LOW-MEDIUM, folded → §For-Andrew/§4):** "conditions 1+2 cover both channels" read as
  self-complete for Fire-4 sign-off — but a script *computing* a sensitive key for a lazy `kv.Read`
  (plaintext disposition) and exfiltrating via a non-`external.*` event is covered by neither; the
  verified-pure Starlark sandbox read-confinement is the third load-bearing leg. Scoped the claim to
  the external-egress marker channel and named the sandbox explicitly.
- **F3 (nit, folded → §3.2/§8 Fire 1):** at the mint seam `doc.Data` carries ct/nonce as **base64
  strings**, not raw bytes — the mint must MAC over the `ciphertextFromData`-decoded bytes (what the
  responder receives) or mint/verify silently diverge. Pinned as a Fire-1 test vector.
- **Scope note (folded → §8 Fire 2):** every shipped egress test builds MAC-less markers against the
  wholesale responder — the Fire-2 blast radius is enumerated in the fire row so it isn't mistaken for
  a regression mid-build.

Checks that passed (verified file:line by the reviewer): the event's top-level `requestId` is the
emitting op's (`step7_events.go:83`) and the hydrating instanceOp IS the emitter; `rid` is in scope at
both mint seams; the envelope-outside-the-MAC is safe (identityKey is AAD-bound into both the DEK
unwrap and the ciphertext open, `local.go:345,243` — a wrong-identity envelope fails GCM auth);
length-prefixed MAC input unambiguous; KEK domain separation sound; the bridge's only Vault-RPC use is
the decrypt subject, so the grant swap breaks nothing else; shredded-check ordering keeps
`ErrKeyShredded` the post-verify observable; the helper's `dict(sref)` carries `mac` with zero Starlark
change. **The pre-build gate is discharged; no deferred gate is left for the Steward.**

---

*Designed by Winston (Designer fire, 2026-07-12). Awaiting Andrew's ratification; the Lattice Steward
builds Fires 1–2 after ✅; AI-caps Fire 4 unblocks on Fire 1+2 landing (its own lane builds the §7 lint).*
