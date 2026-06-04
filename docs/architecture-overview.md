# Lattice Architecture Overview

This diagram shows the full platform as designed — including components that are implemented today and those planned for later phases. See [Project status](../README.md#project-status) for what is built now.

```mermaid
flowchart TB
    subgraph Actors["Actors"]
        Human("Human Actor<br/>(web · mobile)")
        AI("AI Agent<br/>(identity vertex)")
        Admin("Admin / CLI<br/>(internal service actor)")
    end

    subgraph EdgeLattice["Edge Lattice — Phase 3+"]
        EdgeNode("Sovereign Client Node<br/>local VAL + Starlark<br/>mobile · web · IoT")
    end

    subgraph GW["Gateway — Trust Boundary"]
        Proxy["Reverse Proxy<br/>NGINX / Envoy<br/>TLS · rate-limit · CORS"]
        Trans["Gateway Translator<br/>JWT → Lattice-Actor<br/>token revocation check<br/>HTTP → NATS"]
    end

    subgraph NATS["NATS — Core Plane"]
        Ops[["core-operations<br/>(meta · urgent · bulk lanes)"]]
        Evts[["core-events<br/>(at-least-once)"]]
    end

    subgraph WritePath["Write Path"]
        Proc["Processor<br/>─────────────────<br/>Sole writer to Core KV<br/>9-step commit path<br/>Starlark sandbox<br/>Event schema validation"]
    end

    CoreKV[("Core KV<br/>vertices · aspects · links<br/>DDL meta-vertices<br/>Loom instances · op trackers")]

    subgraph ReadPath["Read Path"]
        Refr["Refractor<br/>─────────────────<br/>openCypher lens engine<br/>Durable CDC consumers<br/>Capability Lens (auth)<br/>Crypto-shred handler"]
    end

    subgraph OpKV["Operational State (NATS KV)"]
        CapKV[("Capability KV<br/>O(1) auth cache")]
        HealthKV[("Health KV<br/>all components")]
        TokKV[("Token Revocation KV")]
        WeavKV[("Weaver State & Claims KV")]
    end

    subgraph Targets["Lens Targets — Query Surfaces"]
        PG[("Postgres<br/>business lenses")]
        NKV[("NATS KV targets<br/>capability · Weaver")]
        PLens[("Personal Lens<br/>per-device security-filtered<br/>Phase 3+")]
    end

    subgraph Orch["Orchestration"]
        Loom["Loom<br/>─────────────────<br/>Linear procedure engine<br/>Pattern interpreter<br/>Task coordination"]
        Weaver["Weaver<br/>─────────────────<br/>Convergence engine<br/>Target-as-Lens<br/>Two-Phase Nudge<br/>Temporal scheduler"]
    end

    subgraph VaultExt["Vault & Crypto — Phase 3+"]
        Vault["Vault<br/>Per-identity key management<br/>Encrypt-on-write · Crypto-shredding"]
        KMS["KMS / HSM<br/>(external key material)"]
    end

    subgraph External["External"]
        IdP["External IdP<br/>(JWT signing keys)"]
        Svc["Third-Party Services<br/>(Stripe · email · …)"]
    end

    %% Actors → Gateway
    Human & AI -->|HTTPS| Proxy
    Admin -->|"NATS direct"| Ops

    %% Gateway flow
    Proxy --> Trans
    Trans <-->|revocation check| TokKV
    Trans -->|publish op| Ops
    IdP -.->|signing keys| Trans

    %% Write path
    Ops --> Proc
    Proc <-->|"O(1) auth check"| CapKV
    Proc <-->|entity state reads| CoreKV
    Proc -->|atomic batch write| CoreKV
    Proc -->|outbox publish| Evts
    Proc <-.->|"encrypt / decrypt — Phase 3+"| Vault

    %% Refractor (read path)
    CoreKV -->|"CDC (durable consumer per lens)"| Refr
    Refr -->|projects| CapKV
    Refr -->|projects| PG
    Refr -->|projects| NKV
    Refr -->|"pushes filtered stream"| PLens
    Refr <-.->|"key lookups / shred — Phase 3+"| Vault

    %% Orchestration
    Evts --> Loom
    Evts --> Weaver
    Loom -->|submit ops| Ops
    Weaver -->|submit ops| Ops
    Weaver <-->|convergence state| WeavKV
    Weaver -->|reads violation flags| NKV
    Weaver -->|Two-Phase Nudge| Svc

    %% Vault
    Vault <-->|key material| KMS

    %% Health heartbeats
    Proc & Refr & Loom & Weaver -->|heartbeat| HealthKV

    %% Edge Lattice
    PLens <-->|"sync on reconnect (revision-based reconcile)"| EdgeNode

    %% Styles
    classDef store fill:#dbeafe,stroke:#2563eb,color:#1e3a8a
    classDef engine fill:#fefce8,stroke:#ca8a04,color:#713f12
    classDef gwStyle fill:#f0fdf4,stroke:#16a34a,color:#14532d
    classDef extNode fill:#faf5ff,stroke:#9333ea,color:#581c87
    classDef edgeNode fill:#fff7ed,stroke:#ea580c,color:#7c2d12
    classDef natsQueue fill:#ecfdf5,stroke:#059669,color:#064e3b
    classDef actor fill:#f0f9ff,stroke:#0284c7,color:#0c4a6e

    class CoreKV,CapKV,HealthKV,TokKV,WeavKV,PG,NKV,PLens store
    class Proc,Refr,Loom,Weaver,Vault engine
    class Proxy,Trans gwStyle
    class IdP,Svc,KMS extNode
    class EdgeNode edgeNode
    class Ops,Evts natsQueue
    class Human,AI,Admin actor
```

## Key data flows

**Write path (left side, top-down):**
Clients submit operations over HTTPS → the Gateway authenticates the actor (JWT), stamps `Lattice-Actor`, and publishes onto `core-operations`. The Processor consumes the operation, checks authorization against Capability KV, hydrates entity state from Core KV, runs the Starlark script, validates the resulting mutations and events against DDL, and commits everything atomically to Core KV. A transactional outbox consumer then publishes business events to `core-events`.

**Read path (right side, CDC-driven):**
The Refractor holds one durable JetStream consumer per active Lens. Each consumer watches Core KV's backing stream, evaluates openCypher rules, and projects results into target stores — Postgres tables for business queries, NATS KV for the Capability cache (auth) and Weaver targets, and Personal Lens streams for edge clients.

**Orchestration (bottom loop):**
Loom and Weaver consume `core-events`, then submit new operations back through `core-operations` → Processor → Core KV. They never write state directly; the ledger is the only source of truth. Weaver's Two-Phase Nudge reaches external services via a claim-before-execute protocol recorded in `weaver.claims.>`.

**Authorization (always-on, not a separate call):**
The Capability Lens is a Refractor projection that continuously maintains a flattened permission cache in Capability KV. The Processor reads it at O(1) in commit-path step 3. No separate auth service; auth correctness is projection correctness.

## Phase status

| Component | Phase |
|-----------|-------|
| Substrate (NATS/KV primitives), Processor, Refractor, Capability Lens | ✅ Phase 1 — implemented |
| Identity & RBAC packages, Hello Lattice vertical slice | ✅ Phase 1 — implemented |
| Package install/uninstall, transactional event outbox, per-lens delete mode | ✅ Phase 1.5 — implemented |
| Loom, Weaver, Two-Phase Nudge, `orchestration-base` package | 🔨 Phase 2 — in progress |
| Gateway (JWT auth, token revocation, HTTP→NATS translation) | 🔭 Phase 3 — designed |
| Vault, crypto-shredding, KMS integration | 🔭 Phase 3 — designed |
| Edge Lattice, Personal Lens, offline-first sync | 🔭 Phase 3+ — designed |
| Cells & sharding, multi-cell routing | 🔭 Phase 3+ — designed |

## Related reading

- [Component reference pages](./components/README.md) — per-component deep dives
- [Data contracts](./contracts/README.md) — wire shapes, key patterns, behavioral rules
- [Deployment isolation model](./operations/deployment-isolation.md) — per-deployment NATS and Postgres
