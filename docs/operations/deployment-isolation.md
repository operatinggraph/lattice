# Deployment Isolation

## Overview

Each Lattice operator deployment is a fully self-contained runtime. It owns its own NATS server (or cluster), its own Postgres instance, and its own bootstrap identity set. No NATS subject, KV bucket, JetStream stream, or Postgres schema is shared across deployments at any infrastructure level. A component configured for one deployment has no path — credential or network — to another deployment's data.

This isolation model is enforced by topology and credential boundaries in Phase 1. It extends naturally to per-cell isolation in Phase 3 multi-cell deployments without any change to the Core KV data model, because Lattice keys embed no cell or deployment identity.

---

## Phase 1 Topology: Single-Deployment, Single-Server

A Phase 1 deployment consists of:

- One NATS server process (`nats:2.14-alpine`), hosting all KV buckets and JetStream streams for that deployment.
- One Postgres instance (`postgres:16-alpine`), used exclusively by the Refractor as the Lens projection target.
- One bootstrap JSON file (`lattice.bootstrap.json`, path overridable via `BOOTSTRAP_JSON_PATH`) written at startup, containing per-deployment NanoIDs for primordial vertices. These IDs are deployment-specific and are generated fresh on first boot.
- All Lattice binaries — Bootstrap, Processor, Refractor, and the CLI — connect to this single NATS server at startup via the `NATS_URL` environment variable (default: `nats://localhost:4222`). Changing `NATS_URL` is the only configuration change required to point any component at a different deployment.

Phase 1 uses a single NATS server rather than a cluster. This satisfies NFR-R6: single-server mode is acceptable for development and portfolio demonstration. High-availability NATS clustering is a Phase 2+ concern.

### NATS Isolation

All Lattice NATS primitives are scoped to their server. There is no super-cluster configuration, no leaf-node bridging, and no cross-deployment subject routing at Phase 1.

The following KV buckets exist within a deployment's NATS server and are inaccessible from any other deployment's server:

| Bucket | Owner |
|--------|-------|
| `core-kv` | Processor (sole writer) |
| `health-kv` | All components (self-reporting) |
| `capability-kv` | Refractor (Capability Lens target) |
| `refractor-adjacency` | Refractor (private graph index) |

The JetStream streams `core-operations` and `core-events` are likewise scoped to the deployment's NATS server. A Processor consuming from `core-operations` in deployment A reads only the operations submitted to A's NATS server.

Per NFR-SC4: each operator deployment runs in its own isolated NATS cluster; no cross-tenant data access is possible at the infrastructure level.

### Postgres Isolation

The Refractor is the only Lattice component that connects to Postgres. Its connection is governed by the `REFRACTOR_PG_DSN` environment variable (default in `make up`: `postgres://lattice:lattice_dev@localhost:5432/lattice?sslmode=disable`).

Each deployment's Postgres instance holds:

- A separate `lattice` database.
- Separate Lens projection tables populated by that deployment's Refractor.
- No shared schema and no shared Postgres user with any other deployment.

A Lens projection query against deployment A's Postgres has no path to deployment B's tables.

### Credential Boundary

The full Phase 1 credential surface for a deployment is two values:

| Credential | Consumed by | Scope |
|------------|-------------|-------|
| `NATS_URL` | Bootstrap, Processor, Refractor, CLI | Deployment's NATS server endpoint |
| `REFRACTOR_PG_DSN` | Refractor only | Deployment's Postgres instance |

A component started with deployment A's `NATS_URL` cannot reach deployment B's NATS server — they are distinct network endpoints. There is no shared credential, no shared certificate authority, and no shared secret between deployments in Phase 1.

Phase 2+ will introduce mTLS with per-deployment certificates and NATS NKey/Creds authentication, making the isolation guarantee cryptographically enforced rather than network-topology enforced. Phase 1 relies on network isolation (separate container networks or separate host ports in development).

---

## Cell-Agnostic Key Design (NFR-SC2)

Lattice key naming embeds no cell identity, no deployment identity, and no topology-aware segment. The key conventions are:

| Entity | Key pattern | Example |
|--------|-------------|---------|
| Vertex | `vtx.<type>.<id>` | `vtx.tenant.aB3kR7x9pQ2mN5yZ` |
| Aspect | `asp.<vtxId>.<name>` | `asp.aB3kR7x9pQ2mN5yZ.leaseTerm` |
| Link | `lnk.<youngerId>.<name>.<olderId>` | `lnk.cD4nS8y0qR3pO6zA.memberOf.aB3kR7x9pQ2mN5yZ` |
| DDL meta-vertex | `vtx.meta.<NanoID>` with `.canonicalName` aspect | — |
| Op tracker | `vtx.op.<NanoID>` | — |

No key contains a cell prefix or a deployment prefix. A key written in one deployment is structurally valid if that data is later migrated to another deployment or cell — the key itself requires no transformation. Routing and replication are topology concerns resolved by the Gateway layer (Phase 3), not by the keys.

**NFR-SC2 is a locked invariant.** No future story may introduce cell-prefixed or deployment-prefixed keys in Core KV. This is Architectural Principle P6 from the architecture document: "Multi-cell is purely a routing/replication concern layered underneath — no data model or business logic changes required."

The consequence of this design is that multi-cell scale-out in Phase 3 requires no data-model changes and no key-migration tooling. The deployment isolation model of Phase 1 extends directly to inter-cell isolation in Phase 3.

---

## Phase 3 Scale-Out Path: Multi-Cell

Multi-cell (Phase 3) adds horizontal scale by distributing the deployment topology across multiple cells. The Core KV data model is unchanged.

### Cell Topology

Each cell in Phase 3 replicates the Phase 1 deployment structure at a per-cell level:

- One NATS server or NATS cluster per cell.
- One Core KV bucket per cell, containing the vertices, aspects, and links owned by that cell.
- One Refractor instance per cell, projecting only that cell's Core KV via durable CDC consumers.
- One Capability KV bucket per cell, populated by that cell's Refractor from that cell's Core KV.

The deployment isolation model of Phase 1 — separate NATS server, separate Postgres, separate credential surface — extends to inter-cell isolation without modification. A cell is an isolated deployment unit.

### Gateway Routing Layer

The Gateway component (not yet shipped) is the Phase 3 addition that makes multi-cell transparent to clients. The Gateway routes incoming operations to the correct cell based on operator-defined routing policy. The operation payload, key shapes, and commit path are identical to Phase 1 — the Gateway resolves topology; the components do not.

Cross-cell link traversal (Bridge Links, Phase 3) uses the same key shapes. The Gateway resolves cross-cell resolution; no key schema changes are required.

### Per-Cell Components

Each cell's Refractor projects only its cell's Core KV CDC stream. Lens projections, Capability KV, and Adjacency KV remain strictly per-cell. There is no cross-cell projection fan-out. A query against cell A's Lens target returns only cell A's projected data; cross-cell queries are assembled at the Gateway or application layer via explicit cross-cell operations.

---

## Verification Path (Phase 2)

The Phase 1 isolation claim is validated by topology — separate NATS server endpoints, separate Postgres DSNs — rather than by an automated integration test. This section specifies the future integration test that will provide automated verification. The test itself is deferred to Phase 2; this section is the specification for that work.

### Port-Override Mechanism

`docker-compose.yml` already exposes two environment variable overrides that make running two independent deployments side-by-side possible:

```
${NATS_PORT:-4222}:4222    # NATS client port, default 4222
${POSTGRES_PORT:-5432}:5432 # Postgres port, default 5432
```

Two deployments can be started on non-overlapping ports:

```sh
# Deployment A — default ports
make up

# Deployment B — alternate ports, in a separate working directory or with separate bootstrap JSON path
NATS_PORT=4232 POSTGRES_PORT=5442 BOOTSTRAP_JSON_PATH=./lattice-b.bootstrap.json make up
```

No changes to `docker-compose.yml` are required. The override mechanism is already present.

### Future Test Assertions

The Phase 2 integration test asserts the following properties:

1. **Independent bootstrap.** Bootstrap deployment A and deployment B independently. Confirm that `lattice.bootstrap.json` and `lattice-b.bootstrap.json` contain different primordial NanoIDs — deployments are identity-distinct.

2. **NATS credential isolation.** A Refractor started with `NATS_URL=nats://localhost:4222` (A's server) cannot connect to `nats://localhost:4232` (B's server). The wrong NATS URL results in a connection refused or authentication failure, depending on Phase 2 mTLS configuration.

3. **Operation isolation.** An operation submitted to deployment A is visible in A's Core KV and A's Refractor Lens projection, but does not appear in B's Core KV or B's Refractor Lens projection.

4. **Postgres isolation.** After operations in deployment A, A's Postgres tables contain A's projected data. B's Postgres tables contain only B's data. No cross-contamination exists at the database level.

5. **Identity distinction.** The NanoIDs of primordial vertices in A and B are different; there is no shared vertex identity between independently bootstrapped deployments.

### Proposed `make test-isolation` Target (Deferred)

```makefile
test-isolation:
	NATS_PORT=4232 POSTGRES_PORT=5442 BOOTSTRAP_JSON_PATH=./lattice-b.bootstrap.json make up
	go test -tags integration ./internal/isolation/... -v -timeout 120s
	NATS_PORT=4232 POSTGRES_PORT=5442 make down
```

The corresponding test file (`internal/isolation/isolation_test.go`) and this Makefile target are Phase 2 work. Neither is created by the current implementation.
