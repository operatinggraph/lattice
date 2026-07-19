# Display names across the graph — the "fewer NanoIDs" convention

**Status: ✅ RATIFIED (Andrew, 2026-07-18).** Initiated by Andrew's PO pass on the live Facet run
("Facet ocZv1Ptn…" header, id-labeled Unit/Building/leaseapp chips, "Unnamed" on the Me tab): raw
NanoIDs are the primary label almost everywhere a human name should be. Andrew's ask: a generic,
long-term mechanism — explicitly not a demo hack. Build: verticals lane, one item, internal order
N1→N3 (§3).

## 0. Decisions — RATIFIED (Andrew, 2026-07-18)

- **D1 — four display classes** (§2) are *the* convention every lens + renderer follows.
- **D2 — nameable business vertices ride a shared `.presentation` aspect shape** (service-domain's
  proven `{name, description?, icon?, category?}`), declared per-type in each package's own DDL.
  **Not `canonicalName`**: on meta vertices that aspect *is* the vertex's identity (Contract #1
  dedup/recognition semantics, unique by construction); business display names are non-unique
  mutable labels — overloading the meta-identity aspect for display would blur a load-bearing
  invariant. (Andrew's "use canonicalName where available" holds exactly for class 1 — metas —
  where it already works today.)
- **D3 — identity names stay sealed end-to-end.** Self-display decrypts locally via the shipped
  EDGE.4 vault client; other-identity display (staff surfaces) is Protected-lens territory
  (clinicPatientsRead precedent) and stays out of this design. Never plaintext PII in a broadcast
  KV lens row — crypto-shred semantics survive display.
- **D4 — renderer floor rule:** a bare NanoID is never a primary label (§2, last block).

Non-goals: no global name registry; no canonicalName on business vertices; no plaintext PII in KV
targets; no `FACET_DEMO_PERSONAS`-label shortcut for the header (the demo must run the product's
own mechanism).

## 1. Grounding (what exists, verified live 2026-07-18)

- **Metas already display correctly**: the me-lens projects `role.canonicalName.data.value` — the
  "consumer" chip renders a name today. Op-metas carry `.presentation` (title/icon) per the
  descriptor vocabulary.
- **Service templates already display correctly**: `.presentation` `{name, description, icon,
  category}` (seed-showcase writes "Riverside Café" etc.; catalog cards render them). This is the
  proven shape D2 generalizes.
- **The identity name is *deliberately* null in the manifest**: `CreateUnclaimedIdentity` writes
  `name`/`email`, but those are sensitive PII aspect types (identity-anchored, step-6) and
  Vault-encrypted at rest — so `edgeIdentitySpec`'s `identity.name.data.value AS displayName`
  projects null *by design* (the ciphertext envelope has no `.data.value`). Observed live:
  seeded "Riley Chen", `displayName: null`. Not a bug — the projection slot exists, the plaintext
  correctly can't reach it.
- **EDGE.4 shipped the decrypt mechanism**: `internal/edge/vault.Client` — transient session key
  from the Personal Lens control plane, TTL-cached in memory, decrypts ciphertext-shaped aspect
  data from the local mirror on read; plaintext never written back; a shredded identity's key
  request fails permanently ("local copies become gibberish").
- **Row-context precedent**: `manifest.inst` rows already carry `templateName`/`templateIcon` —
  "project the human context into the row" is the established Personal-Lens pattern.
- **The gap**: location-domain vertices (building/unit) declare no name-bearing aspect; leaseapp/
  tab/booking have no intrinsic name at all; the identity self-name has no display path wired.

## 2. The convention — four display classes

| # | Vertices | Display source | Why |
|---|---|---|---|
| 1 | `vtx.meta.*` (roles, op-metas, DDLs) | `canonicalName` (+ `.presentation` where richer copy exists) | already the meta's identity; unique by construction; works today |
| 2 | Nameable business vertices (locations, service templates, studios, providers, …) | `.presentation` aspect `{name, icon?, …}`, declared in the owning package's DDL | reuse the proven service shape; display ≠ identity (see D2) |
| 3 | `vtx.identity.*` | **self:** engine-local decrypt of the sealed `name` aspect via EDGE.4 `vault.Client` · **others:** Protected/Secure lens surfaces only (staff apps) | names are PII: sealed at rest, plaintext only inside an authorized session; shredding must keep working |
| 4 | Relational vertices (leaseapp, tab, booking, service instance, task) | the projecting lens carries linked display context (`templateName` pattern); the renderer composes a typed label — "Lease application · Unit 2" | no intrinsic name; the label *is* the relationship |

**Renderer floor rule (Facet, and every future renderer of the vocabulary):** a bare NanoID is
never a primary label. Fallback ladder: `displayName` → composed relational label (class 4) →
`<Type> · <short-id>` — with the full key reachable on inspect (honesty preserved, noise removed).
"Unnamed" disappears as a state: an absent name renders the typed fallback, not a shrug.

## 3. Fires (ONE verticals item, internal order — coupled, ships together per lane discipline)

- **N1 (pkg):** location-domain accepts optional `presentation` at creation (and a
  `SetLocationPresentation` op for live worlds); showcase + classic seeds name the world
  ("Riverside Building", "Unit 1", "Unit 2"); edge-manifest lenses project location names into
  `manifest.me.anchors` and every row that references a location.
- **N2 (FE + lens):** renderer floor rule everywhere; leaseapp/task rows gain projected context
  (lens adds the subject's display fields; "Sign Lease — Leaseapp Lh1ry1" becomes
  "Sign Lease — Unit 1 lease").
- **N3 (pkg + FE) — SHIPPED:** self-name. `edgeIdentitySpec` projects `identity.name.data` as
  `sealedName` (the `{ct, nonce, keyId}` envelope; the pre-existing `displayName` alias stays for
  a no-Vault stack), and `internal/edge/vault.SelfName` decrypts it in memory via the EDGE.4
  client — that client's first consumer. Applied at one `manifestFrame` seam per engine, so the
  live-delta / snapshot / browser-`read` paths cannot drift; both hosts carry it (wasm parity).
  Every failure degrades to leaving the row alone, so the renderer's floor rule paints the typed
  fallback — the shred story holds at the display surface. The FE needed no change: `app.js`'s
  `identityLabel` already read `displayName`.

  **Live-stack tail (not a code gap):** the browser beat — header reading the resident's name —
  was not observed live this fire. Verifying it surfaced a *Refractor* bug, fixed in the same
  commit: the MATCH-update hot-reload path threaded `Into.Key` without activation's
  `IsPersonalLens` exemption, so a Personal Lens's `__actor` key failed RETURN-alias validation
  and **every cypher edit was silently refused**, pinning the running pipeline to its old cypher.
  The live stack still runs the pre-fix Refractor binary, so the in-browser confirmation waits on
  that process being restarted by whoever owns it. Proven meanwhile at both halves: the lens alias
  resolves through the real engine (`edge_manifest_fire1_e2e_test.go`), and the decrypt round-trips
  against a real control service + Vault backend, including the shredded-identity fallback
  (`internal/edge/vault/selfname_test.go`).

  **Live-stack tail, re-grounded 2026-07-19 (the restart was not the blocker).**
  Driving the running showcase stack as tenant1 (Riley Chen) narrowed this a long
  way, and ruled out the two cheap explanations:

  - **Not a stale package.** The installed `edgeIdentity` lens spec in Core KV
    (`vtx.meta.ua4dCK62adbHJDCxua4d.spec`) carries the N3 cypher — both
    `identity.name.data AS sealedName` and the anchors' `loc.presentation.data.name`.
  - **Not a stale Refractor.** The running binary post-dates the N3 fix and booted
    after that spec was written; `lens lag` shows `edgeIdentity` reprojecting on
    demand (confirmed twice, `lastProjectedAt` advancing within a second of a
    location-aspect write).
  - **Not missing source data.** The identity's sealed `.name` aspect exists, and
    the three showcase locations now carry `.presentation` (see below).
  - **Not a stale device mirror.** Facet was rebuilt and restarted, forcing a full
    rehydrate; the row came back identical.

  Yet the emitted `manifest.me` row still carries neither `sealedName` nor an
  anchor `name`/`containerName` — the anchors project as bare
  `{key, container}`, and `displayName` stays null. So the projection genuinely
  omits both N3 fields with the correct rule installed and a reprojection
  demonstrably running. Two candidates remained:
  **(a)** Refractor is executing a compiled rule older than the spec it holds, or
  **(b)** the engine does not resolve these two expression forms — a neighbour's
  aspect hop *inside* a `collect()` map, and an aspect's whole `.data` object as a
  scalar-position alias — and yields null for each.

  **Both are now falsified (2026-07-19, `2a0af7e3`) — the loss is downstream of
  the engine.**

  - **(b) is false.** `aspect_expression_shapes_test.go` exercises the two shapes
    against the real engine: `identity.name.data` in scalar alias position yields
    the `{ct, nonce, keyId}` envelope, and `loc.presentation.data.name` resolves
    inside a `collect()` map across two OPTIONAL MATCH hops. Both pass. They share
    one `resolveProperty` call site, so there is no second evaluator to diverge.
  - **(a) is false.** The installed spec (`vtx.meta.ua4dCK62adbHJDCxua4d.spec`)
    was written at 02:04:30 and carries the N3 cypher verbatim; the running
    Refractor logged `lens loaded edgeIdentity` at 06:04:52 — *after* that write —
    with no refusal in its log. The compiled rule is current.

  So the engine produces both fields and the rule that produces them is live.

  **The SYNC delta was then captured, and the premise itself is false (2026-07-19).**
  Reading the stored `lattice.sync.user.>` deltas off the SYNC stream, the latest
  `manifest.me` upsert for the showcase resident (`projectionSeq` 3094) carries
  **both** N3 fields:

  ```
  "sealedName": { "ct": "lblX+zkn/…", "keyId": "vtx.identity.MQsm…", "nonce": "r8LN+…" },
  "anchors":    [ { "key": "vtx.unit.J11X…", "name": "Unit 1",
                    "container": "vtx.building.A9jn…", "containerName": "Riverside Building" } ]
  ```

  The cloud side is correct end to end — engine, compiled rule, personal envelope
  (`personalEnvelopeFn` passes `row` through unmodified) and the `nats_subject`
  adapter (`splitEnvelopeRow` puts every non-reserved column into `Data`).

  **The loss is the device's local mirror.** Signing in as that resident and
  reading `/api/feed` returns, on a live connection (`connected:true`), the
  *pre-N3* row — `sealedName` absent entirely and `anchors` stripped to
  `{key, container}`. `facet-store/<identityID>.db` contains that same stale
  shape and no `sealedName` at all. So the device is serving a mirror row that
  the newer delta never overwrote — which is exactly why the earlier
  rebuild-and-rehydrate "came back identical".

  **RESOLVED — N3 works end to end; the tail was a stale device mirror
  (2026-07-19).** Purging `facet-store/<identityID>.db` and re-hydrating returns
  the row complete, and `/api/feed` now serves:

  ```
  "displayName": "Riley Chen",
  "anchors": [ { "key": "vtx.unit.J11X…", "name": "Unit 1",
                 "container": "vtx.building.A9jn…", "containerName": "Riverside Building" } ]
  ```

  `displayName` is the decrypted plaintext, so `internal/edge/vault.SelfName` is
  confirmed working in-engine against the real Vault control plane on a live
  stack — the beat the N3 commit could never observe. **No code was wrong.** Both
  the design's candidates and the syncgap theory below were all false; the mirror
  simply held a pre-N3 row that nothing forced it to re-fetch, and a local purge
  is what re-fetches it.

  Two environment findings fell out, worth keeping because both cost real
  debugging time here:

  - **A stale `bin/gateway` denied the gap RPC.** The running auth-callout
    responder (built 2026-07-17 20:33) predated the grant for
    `lattice.ctrl.refractor.personal.syncgap` (`0acd68c3`, 2026-07-17 22:52), so
    every gap check was refused. Rebuilding + restarting the gateway cleared it —
    but it was NOT the cause of the stale row, which survived the fix and only
    cleared on purge. Two fires in a row were misdirected by a stale dev binary;
    check binary build times against the commits under investigation *first*.
  - **A hydrate needs a moment.** Reading `/api/feed` ~8s after login still
    returned the pre-N3 row; the corrected row landed once replay finished.
    Sample the feed until the row settles rather than treating the first frame
    as the answer.

  Two real gaps *were* found and fixed while grounding this (`93c6064d`): the
  showcase seed never named an already-seeded world (it passes location names only
  on its from-scratch path), and `SetLocationPresentation` — the live-world editor
  N1 shipped for exactly that — could not actually replace an existing
  `.presentation`, dying with RevisionConflict against a create-only mutation.

**Green bar:** a signed-in showcase resident sees zero raw NanoIDs across Home/Services/Tasks/
Activity/Me; the header reads "Sam Okafor"; crypto-shredding that identity flips the header to the
typed fallback (the shred story stays demonstrably true).

## 4. Relations

- **Self-anchor parameterization** (verticals row, same PO pass): `manifest.me` growing typed
  self-anchors (leaseapp, patient, …) + `{me.<anchor>}` in `dispatch.contextParams` shares N1's
  me-row work — build together or N1-first; the JWT-authenticated actor remains the server-side
  proof (self-scope capability checks), the manifest is only the UX vehicle.
- **Staff worlds** (separate discovery item): class-3 "others" display is that initiative's read
  spine (Protected lenses); deliberately out of scope here.
