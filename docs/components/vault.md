# Vault

**Component reference** | Audience: implementers + architects | Contract authority: `docs/contracts/03-mutation-batch-event-list.md` §3.10 (sensitive-aspect encryption at rest) / §3.11 (sensitive-object encryption); design: `vault-crypto-shredding-design.md`

---

## Overview

Vault is the **per-key-holder key-custody + crypto library** behind crypto-shredding: encrypt-on-write
and decrypt-on-read for sensitive aspects (SSN, DOB, …), plus the irreversible key-destruction
primitive (`ShredKey`) that makes right-to-be-forgotten a cryptographic guarantee rather than a
best-effort delete. A key holder is the vertex a sensitive aspect's custody resolves to: an aspect-type
DDL's `Custody.Kind` declares which — `identity` (the aspect's own anchoring identity vertex) is the
default and the only kind any shipped package uses; `retentionClass` (a package-declared
`vtx.retentionclass.<id>` holder, so a record with a retention obligation survives its data subject's
erasure) is defined but currently refused at install (`internal/pkgmgr/custodyscope.go`) because the
verb that destroys a class-held DEK, `ShredRetentionClassKey`, does not exist yet. Unlike
Loom/Weaver/the Bridge/object-store-manager, **Vault is not an always-on binary** — it is a library
embedded in the two processes that need it: `cmd/processor` (the authoritative instance, hosting
encrypt/decrypt/shred on the commit path) and `cmd/refractor` (a second, independently-KEK-loaded
instance used only for Secure-Lens decrypt-at-projection).

Key material never lands in Core KV — only the **wrapped** (ciphertext) DEK does, as the key holder's
`piiKey` aspect. `ShredKey` makes that wrapped DEK permanently unusable, rendering every ciphertext it
protects — in live KV *and* in JetStream history — unrecoverable. The `Vault` interface
(`internal/vault/vault.go`) is deliberately stateless with respect to key custody: callers supply the
`Envelope` they already hold from the holder's `piiKey` aspect, so Core KV's `piiKey` stays the
single source of truth and the backend stays swappable (local envelope encryption today, a KMS later)
without a second, potentially divergent copy of wrapped key material.

Every decrypt site resolves the holder from the ciphertext's own `keyId` — never from the aspect's
anchor or a lens-projected column — because `keyId` is the AEAD associated data the ciphertext was
sealed under (`vault.KeyHolder`, `internal/vault/keyholder.go`): naming a different holder buys an
attacker nothing, since that holder's real envelope unwraps that holder's real DEK and then fails the
GCM tag against a ciphertext sealed under a different key and a different AAD. A `keyId` that is absent
or not a well-formed vertex key is refused, with no fallback to the anchor. The five call sites are
`internal/processor/sensitive_decrypt.go`, `internal/refractor/pipeline/secure.go`,
`internal/vault/service.go` (`handleDecryptRef`), `internal/bridge/egress.go`, and `cmd/loupe/vault.go`.

---

## Two backends, one interface

| Type | File | Role |
|------|------|------|
| `Vault` (interface) | `internal/vault/vault.go` | `CreateIdentityKey` / `Encrypt` / `Decrypt` / `ShredKey` — the contract every backend implements |
| `LocalBackend` | `internal/vault/local.go` | The shipped backend (design §2.5, "Path A"): a single master KEK (env/file-sourced, never in Core KV) wraps a random per-holder AES-256-GCM DEK. A 5-minute TTL cache holds *unwrapped* DEKs in memory for the steady-state hot path; an in-memory deny-list (`shredded`) is what makes `ShredKey` stick. |
| `Service` | `internal/vault/service.go` | A NATS Services responder (`lattice.vault.decrypt`) exposing `Decrypt` to trusted-tool callers (Loupe) that hold an `Envelope` + `Ciphertext` from their own Core-KV inspector reads but not the master KEK itself. |

`keyHolderKey` is cryptographically bound into both `Encrypt` and `Decrypt` as AEAD associated data —
presenting the right `Envelope` under the wrong holder fails closed (`ErrInvalidEnvelope`), it does
not silently succeed. The parameter carries this name on all seven `Vault` interface methods and on
the package-level `OpenWithSessionKey`; the NATS RPC wire field stays the frozen `identityKey` (see
"In / Out contracts" below) while the Go struct field is `KeyHolderKey`.

**Shred semantics differ by backend, honestly reported.** On `LocalBackend`, `ShredKey` is a
deny-list *refusal* — the master KEK is shared across holders, so it cannot be destroyed
per-holder — while a future production KMS backend would additionally destroy the per-holder key
*version*, true cryptographic erasure. The `health.vault` heartbeat's `backend` field
(`LocalBackendName = "local-envelope"`) tells an operator which guarantee strength is live.

---

## Where it runs

- **`cmd/processor`** wires the authoritative `*vault.LocalBackend` (`loadVault`, reading
  `LATTICE_VAULT_MASTER_KEK` or `_FILE`; the process refuses to start with neither set). This one
  instance backs the commit-path decrypt-on-read (step 4) and encrypt-on-write (step 6.5) hooks, hosts
  the `lattice.vault.decrypt` NATS responder, and is the **same** instance `internal/privacyworker`
  (see "Adjacent consumers" below) shreds through — a separate instance from the same KEK would not
  share the in-memory shredded-set / DEK cache and would silently fail to observe a shred recorded
  elsewhere.
- **`cmd/refractor`** wires a second, independent `*vault.LocalBackend` from the *same* KEK sources
  (`make provision-vault-kek` provisions one KEK for both) — used only by `NewSecureDecryptor` for
  Secure-Lens decrypt-at-projection. Optional: a Secure Lens with no Vault backend configured fails to
  activate rather than projecting plaintext-shaped columns with no way to fill them.

Neither process runs Vault as a separate binary or service actor — it has no `identity.system.vault`
identity of its own; it acts under whichever process's own service-actor context invokes it.

---

## Adjacent consumers

Two **separate, independently-durable** consumers react to `events.privacy.keyShredded` — neither's
failure blocks the other:

| Consumer | Package | Does |
|---|---|---|
| The key-destruction listener | `internal/privacyworker` | Calls `Vault.ShredKey(identityKey)` against the Processor's authoritative instance — the actual irreversible destruction the `shredIdentityKey` DDL only records *intent* for synchronously (it never touches the Vault itself, so a KMS round-trip can never block or fail an op commit). Runs inside `cmd/processor`. |
| The projection-nullification listener | `internal/refractor/keyshredded` | Removes a shredded identity's already-projected rows from configured **plain** lens targets (belt-and-suspenders — those rows hold now-garbage ciphertext already; a Secure Lens instead self-nullifies via `piiKey`-CDC-triggered reprojection, `pipeline/secure.go`). Runs inside `cmd/refractor`. |

---

## In / Out contracts

| Direction | Contract | Notes |
|-----------|----------|-------|
| In | commit-path step 4 (hydrate) / step 6.5 (encrypt) calls, `internal/processor` | decrypt-on-read / encrypt-on-write for a sensitive aspect, against the caller-supplied `piiKey` `Envelope` |
| In | `lattice.vault.decrypt` NATS Services RPC | trusted-tool (Loupe) plaintext reads; request carries `identityKey` + `Envelope` + `Ciphertext`, response is `{plaintext}` or a generic (non-identifying) `{error}` |
| In | `events.privacy.keyShredded` (via `internal/privacyworker`) | triggers `ShredKey` on the Processor's authoritative instance |
| In | Secure-Lens projection pipeline, `internal/refractor/pipeline/secure.go` | decrypt-at-projection against the Refractor's own instance |
| Out | `health.vault.<instance>` heartbeat (hosted by the Processor) | `backend`, cumulative `vault_calls_total` / encrypt / shred counters, DEK-cache + shredded-set gauges (Contract #5 §5.4 Vault baseline) |

---

## Key invariants

- **No plaintext key material leaves the backend.** Only the wrapped DEK (`Envelope.WrappedDEK`)
  is ever persisted, in Core KV's `piiKey` aspect; the master KEK lives in env/file config, never KV.
- **Holder-bound crypto.** `keyHolderKey` is AEAD-bound into every `Encrypt`/`Decrypt` — an
  `Envelope` cannot be replayed under a different holder.
- **A decrypt site trusts the ciphertext's own `keyId`, not the aspect's anchor.** `vault.KeyHolder`
  refuses a `keyId` that is absent or malformed rather than falling back to the anchoring identity, so
  a caller can never open a record under a holder the ciphertext does not itself name.
- **Shred is terminal and idempotent.** Once `ShredKey` returns, every subsequent `Encrypt`/`Decrypt`
  for that holder fails with `ErrKeyShredded` regardless of the `Envelope` presented; shredding an
  already-shredded (or never-created) key is not an error.
- **One authoritative instance for shred-observability.** Only the Processor's own `LocalBackend`
  instance is guaranteed to see a shred recorded through it — a second differently-constructed
  instance (even from the same KEK) has its own independent in-memory shredded-set.
- **Panic-isolated RPC surface.** `Service.handleDecrypt` is reachable with arbitrary caller-controlled
  JSON over NATS; it recovers from any backend panic and never echoes backend error detail over the
  wire — only a generic message, with full detail logged server-side.

---

## Failure modes

| Mode | Behavior |
|------|----------|
| Neither `LATTICE_VAULT_MASTER_KEK` nor `_FILE` set | `cmd/processor` refuses to start — a sensitive-aspect write must never silently land as plaintext |
| Decrypt/Encrypt against a shredded holder | `ErrKeyShredded`, surfaced as-is over the NATS RPC (a legitimate, non-generic error the caller should distinguish) |
| Decrypt with a tampered ciphertext or wrong key | `ErrDecryptFailed` (AEAD authentication failure) |
| `Envelope` presented under the wrong `keyHolderKey` | `ErrInvalidEnvelope` — fails closed, never silently decrypts under the wrong holder |
| Ciphertext's `keyId` absent or not a well-formed vertex key | `ErrInvalidEnvelope` (`vault.KeyHolder`) — refused with no fallback to the aspect's anchor |
| A `$sensitiveRef` egress marker names a non-`identity` key holder | refused where authored (`internal/processor/sensitive_decrypt.go`) and again by the bridge (`internal/bridge/egress.go`) — the `piiKeyEnvelope` lens the bridge resolves a live envelope from enumerates identity holders only |
| Secure Lens configured with no Vault backend | Refractor logs an error and does not activate that lens, rather than projecting unfillable plaintext-shaped columns |
| Backend panic inside the decrypt RPC handler | recovered; caller gets a generic `{error}` reply, full detail logged server-side |

---

## Principles that apply

- **P2.** Vault never writes Core KV directly — the wrapped `piiKey` Envelope is written by the
  Processor's own commit path, under the ordinary op-submission route.
- **D5 / minimum data in the vertex root.** Sensitive data lives in an aspect (`piiKey`,
  the sensitive aspect's own `data`), never the vertex root.
- **Swappable backend, stable interface.** The `Vault` interface is the seam a production KMS backend
  (HashiCorp Vault Transit, AWS/GCP KMS) implements later without touching any caller.

---

## Implementation status

**Built.** `LocalBackend` (encrypt/decrypt/shred + the DEK cache + the shredded deny-list), the
`lattice.vault.decrypt` NATS Services responder, and both `events.privacy.keyShredded` consumers
(`internal/privacyworker`, `internal/refractor/keyshredded`) are implemented and covered by
`internal/vault`'s own test suite plus the Vault Fire 5b crypto-shred e2e test
(`fb66e7c`, proven through the real async shred chain).

**Deferred:** a production KMS backend (the interface's other implementer) — the local envelope
backend is the dev + trusted-tool (Loupe, loopback-only) posture. The attended delivery-boundary
reset + a live e2e against a running stack are the last Vault Fire 5b gate (destructive to the shared
dev stack — see `vault-crypto-shredding-design.md`). `ShredRetentionClassKey` — the verb that would
destroy a `retentionClass`-custodied DEK — is also unbuilt, which is why `pkgmgr` refuses that custody
kind at install (`retention-class-key-custody-design.md`).
