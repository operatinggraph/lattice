# Contract #7 — Primordial Bootstrap

The primordial bootstrap is the set of Core KV entries that `make up` seeds into a fresh Lattice deployment before any operation can be processed. It establishes the self-describing meta-meta layer, the platform's foundational types, and the topology required for the Capability Lens to produce auth projections for system identities.

### 7.1 Bootstrap Principle

**Bootstrap establishes graph topology; the Capability Lens does the rest.** No Core KV mutations bypass the Capability Lens's role as the sole authorization surface (NFR-S2). System identities — including the bootstrap identity and internal service actor identities — receive their Capability KV entries through normal Lens projection, derived from the topology that `make up` seeds.

This is the critical design principle: every actor's auth traces back to graph topology. No actor has a "direct-seeded" Capability KV entry that doesn't follow the Lens's logic.

### 7.2 Primordial Seeding Inventory

`make up` writes the following directly to Core KV at first initialization (the sole sanctioned non-Processor write path **into Core KV**, and only during bootstrap). One other non-Processor write path exists, and it is deliberately **not** a Core KV path: trusted clients stream binary blob **bytes** directly into the `core-objects` Object Store — the off-graph blob plane, parallel to Health-KV being a non-Processor *state* plane (Decision #4). Those byte writes carry no graph state and never touch the Capability Lens; the **graph** record of an object (its `vtx.object.<oid>` vertex + `.content` aspect + links) is still written through the Processor like any other state. See the large-file/binary design.

**1. Meta-meta root DDL** — the kernel's **sole** DDL: one `vtx.meta.<NanoID>` vertex (`canonicalName: "root"`, `class: "meta.ddl.vertexType"`) that governs **all** `vtx.meta.*` mutations via `CreateMetaVertex` / `UpdateMetaVertex` / `TombstoneMetaVertex`, dispatching on `op.payload.targetClass` (one of `meta.ddl.vertexType` / `aspectType` / `linkType` / `eventType` / `meta.lens`). It is self-describing (a `meta.ddl.vertexType` that itself governs meta-vertices). The former five separate per-class meta-meta DDLs collapsed into this one root DDL, plus the reserved aspect-type DDLs (item 3) and the package-lifecycle DDLs (`InstallPackage` / `UninstallPackage` / `UpgradePackage`).

**2. Reserved type DDLs** — DDLs for the platform's foundational vertex types:
- `meta` type DDL (used by all meta-vertices)
- `op` type DDL (used by idempotency trackers)
- `identity` type DDL (used by all actor identities)
- `role` type DDL (used by role vertices in the auth graph)
- `permission` type DDL (used by permission vertices)

**3. Reserved aspect-type DDLs** — aspect types used by the meta-meta layer itself:
- `canonicalName`
- `description`
- `schema`
- `sensitive`
- `permittedCommands`
- `vertexSchema`
- `cypherRule` (used by Lens definitions)
- `targetBucket` (used by Lens definitions)
- `outputSchema` (used by Lens definitions to declare projection document shape)

**4. Reserved link-type DDLs** — link types the Capability Lens cypher rule walks:
- `holdsRole` — identity → role (identity holds role)
- `grantedBy` — permission → role (permission is granted by role)
- (additional link types the rule walks; the exact set is established by the cypher rule's authoring in Story 3.x)

**5. Capability Lens definition** — a `vtx.meta.<NanoID>` vertex with `class: "meta.lens"` carrying:
- `canonicalName: "capability"`
- `cypherRule`: the openCypher rule that walks identity → role → permission topology and (post-bootstrap) availableAt/unavailableAt/containedIn topology for service access
- `targetBucket: "capability"`
- `outputSchema`: JSON Schema for the Capability KV document (Contract #6 §6.2)

**6. Operator role + kernel permission vertices** — the topology that produces root-equivalent capability when projected. The **only** primordial role is `operator` (one `vtx.role.<NanoID>`, `canonicalName: "operator"`). The kernel seeds the meta-permission vertices (`CreateMetaVertex` / `UpdateMetaVertex` / `TombstoneMetaVertex`, `scope: "any"`) and the package-lifecycle permissions, each linked `grantedBy` → operator (link direction `permission → role`; reads "permission granted by role"). An identity holding the operator role via `holdsRole` (item 8) projects to root-equivalent capability — this bounded single-link existence check **is** the root designation (Contract #6 §6.1): root capability is established by **graph topology, never by class-based special-casing**, and not by a `data.protected` flag (`protected` carries only anti-brick immutability).

**7. System identity vertices** (seven kernel actors, each carrying `data.protected: true` for anti-brick immutability — per §6.1, `protected` is *not* a capability designator):
- The **primordial admin identity** (`vtx.identity.<NanoID>`, `class: "identity"`) — authors all primordial entries' provenance.
- **Five internal service-actor identities** — Loom, Weaver, the Bridge, object-store-manager, and the privacy worker (`class: "identity.system.<component>"`). **There is no `identity.system.processor`**: the Processor is the sole Core-KV *writer* (P2), not an actor that submits operations, so it needs no seeded actor identity.
- **The Gateway identity** (`class: "identity.system.gateway"`) — unlike the six above, it does **not** hold the operator role (item 8): it is internet-facing (triggered by every unauthenticated HTTP request that reaches it), so it is deliberately scoped narrow instead of root-equivalent. It earns only the package-declared `identityProvisioner` role via a one-time post-install ops action (`gateway-claim-flow-identity-provisioning-design.md` §3.3/§4).

Six of the seven hold the operator role (item 8), which is what projects their root-equivalent capability; the Gateway is the one exception.

**8. Topology links — six of the seven system identities `holdsRole` the operator role (the Gateway does not):**
- `lnk.identity.<admin-id>.holdsRole.role.<operator-role-id>`
- one `holdsRole` → operator edge per service actor (Loom / Weaver / Bridge / object-store-manager / privacy)

This `holdsRole → operator` topology is the root designation (item 6).

(Additional internal service actor identities for Loom, Weaver, etc. are seeded by their respective stream's bootstrap procedures in Phase 2+, following the same pattern — with or without the operator `holdsRole` link, per that actor's own trust-boundary needs.)

**9. Bootstrap operation tracker** — a synthetic `vtx.op.<NanoID>` representing platform genesis. This tracker has **no TTL** (it's a permanent record, not subject to the 24h idempotency horizon). All primordial entities reference this tracker in their `createdByOp` field, making the entire bootstrap a "single operation" in the provenance audit trail.

**Direct Capability KV writes from `make up`:** **None.** Once Refractor starts, the Capability Lens projects `cap.<actor>` for each of the six operator-holding kernel identities by walking its `holdsRole → operator` topology above — no `cap.*` document is directly seeded. The Gateway's `cap.<actor>` doc, once `identityProvisioner` is wired, is instead projected via the ordinary role-grant path (Contract #6 §6.1), same as any package-declared role.

### 7.3 NanoID Generation and Bootstrap Config

All NanoIDs for primordial vertices are generated at first `make up` execution and persisted to `lattice.bootstrap.json` (or equivalent path determined by deployment conventions). The config file's top level carries a version marker plus the nested primordial-ID set (`internal/bootstrap.BootstrapFile` / `PrimordialIDsRaw` is authoritative for the full field list, which grows as the kernel does — see that file's version history comment):

```json
{
  "version": "16",
  "generatedAt": "2026-05-12T14:32:18.142Z",
  "status": "committed",
  "primordialIDs": {
    "bootstrapOp": "vtx.op.<NanoID>",
    "bootstrapIdentity": "vtx.identity.<NanoID>",
    "loomIdentity": "vtx.identity.<NanoID>",
    "weaverIdentity": "vtx.identity.<NanoID>",
    "bridgeIdentity": "vtx.identity.<NanoID>",
    "objmgrIdentity": "vtx.identity.<NanoID>",
    "privacyIdentity": "vtx.identity.<NanoID>",
    "gatewayIdentity": "vtx.identity.<NanoID>",
    "metaRoot": "vtx.meta.<NanoID>",
    "capabilityLens": "vtx.meta.<NanoID>",
    "roleOperator": "vtx.role.<NanoID>",
    "permCreateMetaVertex": "vtx.permission.<NanoID>"
  }
}
```

This config provides the deployment a stable reference set for the primordial NanoIDs across restarts.

### 7.4 Bootstrap Idempotence and Re-runs

**Re-running `make up` on an existing deployment** detects the existing `lattice.bootstrap.json` and skips re-seeding. `make up` is idempotent in the sense that running it twice produces the same end state — NOT in the sense that it rewrites primordial vertices.

**Core KV, not the file, is the authority on whether a bucket has been seeded.** `lattice.bootstrap.json` is file-local: it records what a bootstrap run once did on *some* Core KV, not what *this* Core KV holds. The two can disagree — a recreated or wiped bucket behind a surviving `status="committed"` file — and a deployment that skipped seeding on the file's word alone would come up "ready" with silently-empty reads. Bootstrap therefore probes the bucket (the op tracker key) after provisioning and seeds on the bucket's answer.

The file remains authoritative for the *identity* of the primordial set: on that disagreement the re-seed reuses the file's NanoIDs, so restored keys are exactly the ones existing packages and data already reference. **The op tracker is written first** — it marks a seed *started*, not finished, so a re-seed against a `committed` file first rewrites the file to `status="in-progress"`, keeping the two-phase window that makes an interrupted run retryable.

If an operator wants a fresh deployment, the procedure is:
1. `make down` — clears all NATS buckets, drops Postgres data, deletes `lattice.bootstrap.json`
2. `make up` — re-seeds from scratch with new NanoIDs

This is consistent with the immutability principle: primordial keys aren't reassigned in place — a re-seed restores absent keys at their recorded ids.

### 7.5 Readiness Gate

`make up` does NOT complete until Refractor has projected the bootstrap identity's Capability KV entry
(Refractor healthy with an active lens count, and `cap.<bootstrap-identity-suffix>` present with root
capability). This eliminates the startup race window where Capability KV is empty and operations would
fail auth. The readiness poll's timeout is deployment-configurable (default 30s); on expiry `make up`
exits with an error naming the component that failed to reach readiness. Sequence and mechanics:
`docs/components/bootstrap.md`.

### 7.6 What's NOT in the Primordial Bootstrap

Several things deliberately stay out of `make up`:

**No "Hello Lattice" demo data.** The canonical reference implementation (FR55) is opt-in via a separate `make hello-lattice` (or equivalent) target. Bootstrap produces a minimal, viable, empty platform; demo content is a layer on top.

**No business DDLs.** The bootstrap seeds only the meta-meta layer and platform-essential types (`meta`, `op`, `identity`, `role`, `permission`). Business types (`lease`, `unit`, `building`, `service`, etc.) are authored by operators (or by AI agents in self-improvement flows) after bootstrap completes, via the standard write path (`ops.meta.>` lane).

**No user identities.** The only identities at bootstrap are the seven kernel actors (the primordial admin `identity` plus the Loom / Weaver / Bridge / object-store-manager / privacy / Gateway service actors — §7.2 item 7). Human and AI agent identities are created post-bootstrap through the standard `CreateIdentity` flow.

**No Lens projections beyond Capability.** Other Lenses (business projections, query surfaces) are authored after bootstrap and activate via CDC.

### 7.7 Bypass coverage

The bypass suite MUST prove the topology-not-class rule (§7.2 item 6) in both directions: an identity
**with** the `holdsRole → operator` topology projects root capability; an identity carrying a system
`class` value but **without** the topology does not; and removing the role's inbound `grantedBy` links
drops the corresponding capabilities on the next projection cycle (the auth boundary is reactive to
topology).
