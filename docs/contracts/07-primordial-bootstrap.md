# Contract #7 — Primordial Bootstrap

The primordial bootstrap is the set of Core KV entries that `make up` seeds into a fresh Lattice deployment before any operation can be processed. It establishes the self-describing meta-meta layer, the platform's foundational types, and the topology required for the Capability Lens to produce auth projections for system identities.

### 7.1 Bootstrap Principle

**Bootstrap establishes graph topology; the Capability Lens does the rest.** No Core KV mutations bypass the Capability Lens's role as the sole authorization surface (NFR-S2). System identities — including the bootstrap identity and internal service actor identities — receive their Capability KV entries through normal Lens projection, derived from the topology that `make up` seeds.

This is the critical design principle: every actor's auth traces back to graph topology. No actor has a "direct-seeded" Capability KV entry that doesn't follow the Lens's logic. An operator or AI agent auditing the platform sees a uniform model — even the bootstrap identity's capabilities are explainable by walking the graph from its identity vertex through its role and permission links.

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

**6. Operator role + kernel permission vertices** — the topology that produces root-equivalent capability when projected. The **only** primordial role is `operator` (one `vtx.role.<NanoID>`, `canonicalName: "operator"`). The kernel seeds the meta-permission vertices (`CreateMetaVertex` / `UpdateMetaVertex` / `TombstoneMetaVertex`, `scope: "any"`) and the package-lifecycle permissions, each linked `grantedBy` → operator (link direction `permission → role`; reads "permission granted by role"). An identity holding the operator role via `holdsRole` (item 8) projects to root-equivalent capability — this bounded single-link existence check **is** the root designation (Contract #6 §6.1 / #7 §7.7), **not** a `data.protected` flag (`protected` carries only anti-brick immutability).

**7. System identity vertices** (six kernel actors, each carrying `data.protected: true` for anti-brick immutability — per §6.1, `protected` is *not* a capability designator):
- The **primordial admin identity** (`vtx.identity.<NanoID>`, `class: "identity"`) — authors all primordial entries' provenance.
- **Five internal service-actor identities** — Loom, Weaver, the Bridge, object-store-manager, and the privacy worker (`class: "identity.system.<component>"`). **There is no `identity.system.processor`**: the Processor is the sole Core-KV *writer* (P2), not an actor that submits operations, so it needs no seeded actor identity.

Each of the six holds the operator role (item 8), which is what projects its root-equivalent capability.

**8. Topology links — each of the six system identities `holdsRole` the operator role:**
- `lnk.identity.<admin-id>.holdsRole.role.<operator-role-id>`
- one `holdsRole` → operator edge per service actor (Loom / Weaver / Bridge / object-store-manager / privacy)

This `holdsRole → operator` topology **is** how the Capability Lens designates root-equivalence (Contract #6 §6.1 / #7 §7.7) — a bounded single-link existence check, not a class and not a `data.protected` flag.

(Additional internal service actor identities for Loom, Weaver, etc. are seeded by their respective stream's bootstrap procedures in Phase 2+, following the same pattern.)

**9. Bootstrap operation tracker** — a synthetic `vtx.op.<NanoID>` representing platform genesis. This tracker has **no TTL** (it's a permanent record, not subject to the 24h idempotency horizon). All primordial entities reference this tracker in their `createdByOp` field, making the entire bootstrap a "single operation" in the provenance audit trail.

**Direct Capability KV writes from `make up`:** **None.** Once Refractor starts, the Capability Lens projects `cap.<actor>` for each of the six kernel identities by walking its `holdsRole → operator` topology above — no `cap.*` document is directly seeded.

### 7.3 NanoID Generation and Bootstrap Config

All NanoIDs for primordial vertices are generated at first `make up` execution and persisted to `lattice.bootstrap.json` (or equivalent path determined by deployment conventions). The config file contains:

```json
{
  "platformVersion": "1.0",
  "bootstrapDate": "2026-05-12T14:32:18.142Z",
  "rootRoleKey": "vtx.role.<NanoID>",
  "bootstrapIdentityKey": "vtx.identity.<NanoID>",
  "processorIdentityKey": "vtx.identity.<NanoID>",
  "capabilityLensKey": "vtx.meta.<NanoID>",
  "bootstrapOpKey": "vtx.op.<NanoID>",
  "metaMetaDDLKeys": {
    "vertexType": "vtx.meta.<NanoID>",
    "aspectType": "vtx.meta.<NanoID>",
    "linkType": "vtx.meta.<NanoID>",
    "eventType": "vtx.meta.<NanoID>",
    "lens": "vtx.meta.<NanoID>"
  }
}
```

This config provides the deployment a stable reference set for the primordial NanoIDs across restarts. Without it, post-restart code paths that need to reference (e.g.) "the bootstrap identity" couldn't find it without a class-based Lens query (which would work, but adds startup latency).

### 7.4 Bootstrap Idempotence and Re-runs

**Re-running `make up` on an existing deployment** detects the existing `lattice.bootstrap.json` and skips re-seeding. `make up` is idempotent in the sense that running it twice produces the same end state — NOT in the sense that it rewrites primordial vertices.

If an operator wants a fresh deployment, the procedure is:
1. `make down` — clears all NATS buckets, drops Postgres data, deletes `lattice.bootstrap.json`
2. `make up` — re-seeds from scratch with new NanoIDs

This is consistent with the immutability principle: primordial keys aren't reassigned in place.

### 7.5 Readiness Gate

`make up` does NOT complete until Refractor has projected the bootstrap identity's Capability KV entry. This eliminates the startup race window where Capability KV is empty and operations would fail auth.

**`make up` sequence:**

```
1. Start NATS, provision Core KV / Health KV / Capability KV / Weaver buckets
   (all with `allow_msg_ttl: true` enabled)
2. Start Postgres, run any schema setup
3. Seed primordial Core KV entries (§7.2 inventory) using NATS direct writes
4. Persist lattice.bootstrap.json
5. Start Processor and Refractor (and other configured services)
6. Poll readiness:
   - Refractor health reports `status: "healthy"` AND `lens_count_active >= 1`
   - Capability KV contains `cap.<bootstrap-identity-suffix>` with root capability
7. Print "Lattice ready ({deploymentName})" and exit success
```

**Configurable timeout** (default: 30 seconds) on the readiness poll. If exceeded, `make up` exits with a clear error message identifying which component failed to reach readiness:

```
ERROR: Lattice did not reach ready state within 30s.
  Refractor health: status=starting, lens_count_active=0
  Capability KV: cap.identity.<bootstrap-id> not found
Suggest: check refractor logs at <path>, or `make down && make up` to retry.
```

The default 30s is generous for Phase 1's scale (a handful of bootstrap entries). Production deployments at scale (post-MVP) may need longer; the timeout is deployment-configurable.

### 7.6 What's NOT in the Primordial Bootstrap

Several things deliberately stay out of `make up`:

**No "Hello Lattice" demo data.** The canonical reference implementation (FR55) is opt-in via a separate `make hello-lattice` (or equivalent) target. Bootstrap produces a minimal, viable, empty platform; demo content is a layer on top.

**No business DDLs.** The bootstrap seeds only the meta-meta layer and platform-essential types (`meta`, `op`, `identity`, `role`, `permission`). Business types (`lease`, `unit`, `building`, `service`, etc.) are authored by operators (or by AI agents in self-improvement flows) after bootstrap completes, via the standard write path (`ops.meta.>` lane).

**No user identities.** The only identities at bootstrap are the six kernel actors (the primordial admin `identity` plus the Loom / Weaver / Bridge / object-store-manager / privacy service actors — §7.2 item 7). Human and AI agent identities are created post-bootstrap through the standard `CreateIdentity` flow.

**No Lens projections beyond Capability.** Other Lenses (business projections, query surfaces) are authored after bootstrap and activate via CDC.

### 7.7 Implementation Notes

**For the AI agent implementing Story 1.4 (Dev Harness):**

The `make up` target's implementation:
1. Idempotence check: if `lattice.bootstrap.json` exists, skip seeding and proceed directly to step 5 (start services + poll readiness)
2. Bucket provisioning: create `core-kv`, `health-kv`, `capability-kv`, `weaver-state` buckets; all configured with `allow_msg_ttl: true`
3. NanoID generation: invoke substrate's `nanoid.New()` for each primordial NanoID; assemble into the bootstrap config
4. Direct KV writes: for each primordial entry in §7.2 inventory, construct the document with proper envelope fields (provenance referencing the bootstrap identity and bootstrap op tracker), write to Core KV via NATS direct write
5. Persist `lattice.bootstrap.json`
6. Start Processor, Refractor, and any other configured services
7. Readiness poll loop per §7.5
8. Exit success on readiness OR exit failure on timeout

The order of primordial writes matters for some consistency properties: write the meta-meta DDLs first, then the reserved type DDLs, then the Capability Lens definition, then root role and permissions, then system identities, then topology links. Refractor's CDC processing will handle whatever order it sees, but a logical write order makes debugging easier when bootstrap fails.

**For the AI agent implementing Story 3.x (Capability Lens cypher rule):**

The cypher rule must produce root-equivalent capability when projecting an identity that holds the root role. Concretely:
- Walk identity → `holdsRole` → role
- For role.canonicalName matching `"systemRoot"` (or the deployment's root role convention), emit `platformPermissions[]` entries with `scope: "any"` for every known operation type
- This means the cypher rule must know the operation types — Phase 1 handles this by walking inbound `grantedBy` links from the role to discover permission vertices, which carry the operation types as aspects (cypher: `MATCH (r:role)<-[:grantedBy]-(p:permission)`)
- For non-root roles, the same traversal applies but only the explicitly granted operations are emitted

The rule is uniform across system and non-system identities; root capability is established by graph topology, not by class-based special-casing.

**For the bypass test suite (Stories 1.11 and 3.x):**

Test cases that MUST be covered:
- Bootstrap identity submits operations and they succeed (validates the Lens correctly projects from topology)
- A non-bootstrap identity with the same `class: "identity.system.bootstrap"` value but without `holdsRole` topology does NOT get root capability (proves class doesn't grant access; topology does)
- Tampering with the root role vertex (e.g., removing inbound `grantedBy` links from its permissions) causes the bootstrap identity to lose corresponding capabilities on the next projection cycle (proves the auth boundary is reactive to topology changes)
