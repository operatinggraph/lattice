# Identity claim secret — why Option C (client-minted)

**What this is.** The design rationale behind Contract #9's claim-secret custody model, relocated from
the contract (2026-08-08, contracts-slimming campaign). The contract states the mechanism and the
invariants; this records why the alternatives were rejected.

A claim secret lets the holder bind their credential to an unclaimed identity. Earlier designs minted
the plaintext server-side (inside the Starlark script) and returned it in the operation reply — making
the synchronous write reply a delivery channel for sensitive data, with a "must not be logged" caveat.
A reply field that needs a do-not-log warning is carrying data it should not.

Option C removes the server-side mint and the return channel entirely:

- The **client** mints the claim secret (plaintext).
- The client computes `sha256(plaintext)` and submits **only the hash** in the op payload.
- Lattice stores the hash verbatim. The plaintext is never persisted (not in the `core-operations`
  stream, not in Core KV) and never returned.

The client retains the plaintext it minted; it is the single copy and the single delivery channel
(shown once to an operator, or handed to the end user out of band). The corresponding refuse-rules —
no `secret.mint()` builtin, no `OneTimeSecret` reply field, plaintext never persisted or replied —
are Contract #9 §9.4 invariants.
