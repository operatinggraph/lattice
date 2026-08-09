# Contract #11 — External Actor Authentication (JWT profile & subject binding)

**Status:** FROZEN (Phase 3)

This contract specifies how an **external bearer token** becomes a **Lattice actor**: the accepted
JWT profile and the subject-binding rules every verifying surface applies. It exists because multiple
surfaces verify the same tokens — the Gateway (write stamp + RLS read front), each vertical app's read
boundary, Loupe's operator tier, and the control plane — and the platform's authorization, revocation,
and row-level security all key off the bound actor id. Divergence between any two surfaces is an
authentication bug; this contract is what they must agree on.

The binding principle: **the IdP knows nothing about Lattice.** A deployment's IdP issues standard
OIDC/JWT tokens about its own subjects; Lattice maps them to identity keys internally — a pure
derivation for IdP-native subjects, a validated passthrough for Lattice-native ones. No enrollment
step, no NanoID ever leaves Lattice for the IdP.

---

## 11.1 Scope

Applies to every surface that authenticates an **external** bearer token (`Authorization: Bearer`)
into an actor — today all via the shared `internal/gateway/auth` verifier. Out of scope: **internal
service actors** (Loom / Weaver / Bridge / object-store-manager / privacy / the Gateway's own system
identity), whose trust is the per-service NATS user (transport authN, #75), not a JWT; and the
**claim secret** flow (Contract #9), which this contract composes with but does not alter.

The bound actor feeds, identically, on every surface:

| Consumer | What it receives |
|----------|------------------|
| Operation envelope stamp (Contract #2 §2.34) | the §11.4-**resolved** actor's `ActorID` (`U` when bound, else `A`) |
| RLS read boundary (`set_config('lattice.actor_id', …)`, Contract #6 §6.14) | the bare id of the §11.4-**resolved** actor (`nanoIdFromKey(resolve(ActorID))`) |
| Revocation kill-switch (`token-revocation` bucket) | the pre-resolution `ActorID` (`A`, §11.5) |
| Credential→business resolution (`credential-bindings`, §11.4) | the pre-resolution `ActorID` (`A`), as the lookup key |
| NATS connect surface (Edge sync plane — the auth-callout responder) | the §11.4-**resolved** actor, as the identity every subject in the issued per-connection permission set derives from; the pre-resolution `ActorID` (`A`) for the §11.5 revocation check at connect |

The bound actor id `A` (§11.3) is what the verifier produces; the envelope stamp and the RLS var carry
whatever `A` **resolves to** (§11.4) — never a raw `Subject` a resolving surface would then override.
Revocation and the resolution lookup itself key on the pre-resolution `A`.

## 11.2 Accepted token profile

A token failing any row is rejected (deny; no anonymous fallback — the read/write analog of
Contract #6 §6.8).

| Claim / header | Requirement |
|----------------|-------------|
| `alg` | MUST be one of `RS256 RS384 RS512 ES256 ES384 ES512`. Symmetric (`HS*`) and `none` are refused structurally (allow-list before key selection + keyfunc re-assert). Lattice holds public keys only; it never signs. |
| `kid` | REQUIRED; MUST resolve in the surface's trusted key set. No implicit single-key fallback. |
| `exp` | REQUIRED; enforced with a bounded symmetric clock skew (default 60s). A token without expiry is invalid — short TTLs are the capability/binding freshness backstop. |
| `nbf`, `iat` | OPTIONAL; enforced under the same skew when present. |
| `iss` | Under `opaque` binding (§11.3), REQUIRED and MUST equal the **verifying key source's declared issuer** (per-source, mandatory — the confinement guarantee rests on this). Under `nanoid` binding, unconstrained. |
| `aud` | When the surface configures an audience, the claim MUST contain it. |
| `sub` | REQUIRED, non-empty. Binding per §11.3. Per OIDC Core, `sub` is unique and stable only **within** an issuer — never treat it as global. |
| `jti` | OPTIONAL; carried as `VerifiedActor.TokenID`. RESERVED for per-token revocation; not consulted by any current mechanism (revocation is per-actor, §11.5). |

Token lifetime is not bounded by this contract, but deployments SHOULD keep TTLs short: per-request
revocation is immediate regardless, but capability-projection and binding-materializer lag windows are
backstopped only by expiry.

## 11.3 Subject binding — a property of the trust source

Every trusted key enters a surface through a **key source** and carries, per `kid`, a **binding
spec** `{mode, issuer}` fixed at load time. The mode of the key that **verified the signature** selects
the binding; a `kid` with **no** declared binding spec is a construction error (fail-closed — a
trusted key must never bind by an implicit default). Binding is a property of the **trust source,
never of token content** — inferring mode or trust from the subject's shape is forbidden (a
shape-selected passthrough would let any trusted IdP assert an arbitrary existing identity).

There are exactly two modes, and which one a source gets is **not an operator choice per token**:

- A **configured external source** (static PEM dir, JWKS endpoint) is **always `opaque`** and MUST
  carry a declared expected issuer.
- The **checked-in DEV-ONLY key** (kid `dev`) and the dev/e2e minters that sign with it are pinned
  **`nanoid` in code**, unreachable from configuration. No env grants a configured source `nanoid`
  semantics.

### `opaque` — every configured external source

The subject is an IdP-native identifier (Auth0, Google, Keycloak, …). The actor id is derived:

```
derivationInput = "idpsub:" + <len(iss) in decimal> + ":" + iss + ":" + sub
Subject         = SHA256NanoID(derivationInput)        // substrate.SHA256NanoID / crypto.sha256NanoID
ActorID         = "vtx.identity." + Subject
```

- The token's `iss` MUST be non-empty **and MUST equal the verifying source's declared issuer**
  (reject otherwise). This per-source issuer binding is mandatory and load-bearing: it is what confines
  a source to its own subspace (see the confinement note). OIDC scopes subject uniqueness per-issuer,
  so the issuer also partitions the derivation space. The length framing makes the input unambiguous
  for any `iss`/`sub` contents (no delimiter injection).
- The derivation is pure and stateless: the same `(iss, sub)` binds the same identity on every surface,
  replica, and restart, with no enrollment or lookup.
- Conformance vector (test-derived and asserted, §11.6 — the test is authoritative over this literal):
  `iss = https://accounts.google.com`, `sub = 110169484474386276334` →
  `derivationInput = idpsub:27:https://accounts.google.com:110169484474386276334` →
  `ActorID = vtx.identity.1FF5tdoN7GEGfDedQZ95`.
- **Confinement (holds only under per-source issuer binding).** With each `opaque` source pinned to
  its own issuer, a source can only mint within that issuer's derived subspace — it cannot name a
  pre-existing identity (staff, system actors, the bootstrap root — different subspaces) under SHA-256
  preimage resistance, and it cannot replay **another** trusted issuer's subjects (the mandatory `iss`
  check rejects a foreign issuer before derivation). Without per-source issuer binding this guarantee
  is **false** — any trusted signer could set `iss` to a peer issuer and re-derive a peer user's
  identity — which is exactly why the issuer pin is a MUST, not a SHOULD.
- A source whose IdP emits multiple `iss` forms (e.g. Google's `https://accounts.google.com` vs
  `accounts.google.com`) MUST declare one; the other form is then **rejected** at verification (the
  issuer check is exact-match, not normalization). Tokens in the non-declared form are denied — declare
  the form your relying-party registration receives.
- The raw `(iss, sub)` are surfaced alongside the bound actor (`VerifiedActor.Issuer` /
  `.RawSubject`) for provenance (e.g. the `.idpBinding` aspect written at first-touch provisioning);
  they are never part of the actor id itself.

### `nanoid` — the in-code dev pin only

The subject **is** the bare Lattice identity NanoID:

- `sub` MUST satisfy the Contract #1 NanoID shape (20 chars over the canonical alphabet —
  `substrate.IsValidNanoID`); reject otherwise. A malformed subject is refused at the trust boundary,
  never constructed into a key.
- `Subject = sub`; `ActorID = "vtx.identity." + sub`.
- **Trust grade:** a `nanoid`-mode key holder can mint a token for *any* identity, seeded system
  actors included. It exists solely for the DEV-ONLY checked-in key and the dev/e2e minters that sign
  with it (dev-token, the app dev endpoints, the dev Fake IdP). It is **not** operator-selectable for a
  configured source — the impersonation-grade footgun of granting a third-party IdP arbitrary-identity
  assertion is removed structurally, not by documentation.

### Invariant — identical binding everywhere

Any two surfaces presented the same token MUST bind the same `ActorID`. This is what makes the write
actor, the RLS read actor, the revocation target, and the resolution key one identity. Structurally:
binding happens once, inside the shared verifier, and downstream code consumes `Subject`/`ActorID`
opaquely. A surface's binding specs (mode + issuer per source) are part of what it MUST agree on — two
surfaces trusting the same key under different modes or issuers would violate this invariant.

## 11.4 Credential→business resolution

A bound actor `A` may have claimed a business identity `U` (Contract #9 `ClaimIdentity` →
`credentialBinding` / `credentialindex`; materialized into the `credential-bindings` bucket from the
`identity.claimed` and `identity.rebound` events, and an `identity.unbound` event (credential unlink)
folds as an explicit bucket-key **delete** — the one row-set shrink in this plane, never covered by
overwrite-by-reprojection). Surfaces that resolve MUST do so uniformly:

- Resolution applies identically to the write-path stamp and the read-path actor var (`A → U` on both,
  or on neither) — a split would let one human read as `U` but write as `A`, or vice versa.
- A resolution miss (no binding, materializer lag, bucket error) is deny-safe: act as `A` (self-only
  reach), never an error that blocks authentication.
- An identity may be bound by **multiple** credentials (one human, N sign-in methods); each
  credential still resolves to **at most one** identity — the per-credential dedup guard is
  unchanged. An identity merge repoints the losing identity's credentials to the winner via
  `identity.rebound`.
- **Carve-out:** credential-binding operations (`ClaimIdentity`, `CompleteCredentialLink`) are always
  submitted with the **raw** credential actor `A` (the one-credential-one-identity dedup hashes
  `op.actor`; a resolved actor would let a bound person chain-claim or chain-link).
- **Which surfaces resolve.** The external write door (the Gateway) and the external read boundaries
  (the vertical apps) resolve. The **control-plane seam** (Weaver/Loom/Refractor) does **not**
  re-resolve: its ops arrive already actor-stamped by the door that authenticated them, so a second
  resolution would be redundant (and, on an already-resolved `U`, a no-op miss). "Uniformly" binds the
  set of resolving surfaces to one rule; it does not compel a non-resolving relay to resolve.

## 11.5 Revocation binding

Revocation (the kill-switch, `RevokeActor`/`UnrevokeActor` → `token-revocation` bucket) is keyed on
`ActorID` — the **credential** identity `A`, checked per-request after signature verification and
before resolution. Revoking `A` cuts the credential without touching the business identity `U` or its
history. `jti` remains reserved for a finer per-token cutoff; no current mechanism consumes it.

## 11.6 Invariants

- The IdP never learns, stores, or returns a Lattice identifier; both mappings (account→A, A→U) are
  Lattice-internal.
- Binding is per trust source, carried per `kid`; configured external sources are `opaque` with a
  mandatory declared issuer, the in-code dev key is `nanoid`, and a `kid` with no declared binding
  spec is a construction error. Mode is never inferred from token content.
- Omission fails closed everywhere: no keys ⇒ no verification; unknown `kid` ⇒ reject; `kid` with no
  binding spec ⇒ construction error; no `exp` ⇒ reject; empty `sub` ⇒ reject; `opaque` with a missing
  or non-matching `iss` ⇒ reject; `nanoid` with a malformed sub ⇒ reject; unresolvable binding ⇒ act
  as self.
- The derivation string, once frozen here, is immutable — changing it re-identifies every derived
  actor. The conformance test (§11.3 vector) derives the value and asserts the frozen literal, so the
  test is authoritative and drift is caught in CI. Extensions add new modes; they do not alter
  existing ones.
- All surfaces bind identically (§11.3 invariant), agreeing on both the token profile and the per-`kid`
  binding specs; the shared verifier is the sanctioned implementation point.
