# Contract #9 — Identity Claim Flow (client-minted claim secret)

**Status:** FROZEN

This contract specifies how a one-time identity claim secret flows through the
`identity` DDL's `CreateUnclaimedIdentity` and `ClaimIdentity` operations. The
binding principle: **the claim plaintext never enters Lattice.** The reply path
is not a delivery channel for secrets.

---

## 9.1 The custody model

- The **client** mints the claim secret (plaintext).
- The client computes `sha256(plaintext)` and submits **only the hash** in the
  op payload.
- Lattice stores the hash verbatim. The plaintext is never persisted (not in the
  `core-operations` stream, not in Core KV) and never returned.

Rationale and the rejected server-side-mint alternatives:
`docs/decisions/identity-claim-secret-option-c.md`.

---

## 9.2 `CreateUnclaimedIdentity`

**Payload (request):**

| Field | Required | Notes |
|-------|----------|-------|
| `name` | yes | Display name, maxLen 200. |
| `email` / `phone` | at least one | Normalized; used as deduplication index keys. |
| `claimKeyHash` | yes | Lowercase hex `sha256` of the client-minted claim secret (64 hex chars). Stored verbatim. |
| `claimKeyAlgo` | optional | Hash algorithm. Defaults to `"sha256"` — the only accepted value. |

**Stored `.claimKey` aspect** (`vtx.identity.<id>.claimKey`):

```json
{ "class": "claimKey", "data": { "hash": "<claimKeyHash>", "algo": "sha256" } }
```

**Reply:** `response: {"primaryKey": "vtx.identity.<id>"}` only. The reply returns
**no secret**. Duplicate detection rides the `identity.created` event's
`data.duplicate` flag — not the reply.

The client retains the plaintext it minted; it is the single copy and the single
delivery channel (e.g. shown once to an operator, or handed to the end user out
of band).

## 9.3 `ClaimIdentity`

The actor submits the `claimKey` **plaintext** plus the `targetIdentityKey`. On a
matching hash the identity gains its `credentialBinding` aspect, `state`
transitions `unclaimed → claimed`, and the `.claimKey` aspect is **tombstoned**
— the claim is one-time; a second claim finds no key.

All failure modes collapse to the generic `ClaimKeyInvalid` reply code
(NFR-S6 anti-enumeration); specific outcomes surface only via Health KV.

---

## 9.4 Invariants

- Plaintext claim secret: minted client-side; never persisted; never replied.
- `claimKeyHash`: lowercase hex `sha256`, validated for shape on create.
- The `CreateUnclaimedIdentity` reply carries only `primaryKey` (the created
  identity key) per the closed `response` schema (Contract #2 §2.7).
- Lattice exposes **no server-side secret-mint primitive and no reply field that
  carries a secret** — every minting client follows §9.1, and a server-side mint
  surface may not be introduced.
