# Vendors — authoritative external sources

The canonical record of Lattice's load-bearing third-party dependencies, **where their authoritative
behavior is documented**, and **which version we pin**. Referenced from `CLAUDE.md` ("Authoritative
external sources").

**Rule.** When you need the authoritative behavior of a vendored dependency — semantics, version-gated
features, edge cases — cite the **upstream project's own docs / source / ADRs, version-matched to our
pinned version**. Never rely on a secondary blog or an unqualified web search. Web search is a last
resort and must be **corroborated against the upstream** (the project's docs site or its source/ADRs at
the pinned version) before you act on it.

Add a row when a new vendor's behavior becomes load-bearing.

| Vendor | Role in Lattice | Authoritative sources | Our pin |
|--------|-----------------|-----------------------|---------|
| **NATS / JetStream** | The **substrate**: KV (Core KV, Health KV, operational buckets), JetStream streams (`core-operations` / `core-events` / `core-schedules`), atomic batch (single-stream multi-key commit), per-key message TTL, message scheduling (`@at` / `@every`). | <https://nats.io> (docs) · <https://github.com/nats-io> (source). Design semantics live in the **ADRs** at `nats-io/nats-architecture-and-design` — e.g. **ADR-48** (per-key message TTL), **ADR-51** (message scheduling). Match every claim to **our pinned version's** docs/source. | **NATS 2.14** — `go.mod` `github.com/nats-io/nats-server/v2 v2.14.0`, client `github.com/nats-io/nats.go`; `docker-compose.yml` `nats:2.14-alpine`. |

## Version-gated NATS features (why the pin matters)

Feature availability is version-gated; cite the version that introduced a feature and confirm it is
≤ our pin:

| Feature | Introduced | Notes |
|---------|-----------|-------|
| Per-key message TTL (ADR-48) | NATS 2.11 | Idempotency-tracker 24h TTL (Contract #4 §4.3). |
| Atomic batch (single-stream multi-key, revision-conditioned) | NATS 2.12 | The Processor step-8 commit; the reason all Core KV is one stream. |
| `@at` one-shot message schedules (ADR-51) | NATS 2.12 | The temporal lane's freshness timers + the bridge poll/timeout lane (Contract #10 §10.4). |
| `@every` / 6-field cron / timezone message schedules (ADR-51) | NATS 2.14 | Recurring schedules — the cron-killer (Contract #10 §10.4 "Recurring schedules"). |

**Platform floor: NATS 2.14** (the highest of the above). Pinned in `go.mod` + `docker-compose.yml`;
recorded in Contract #4 §4.3. Do not assume a lower floor — `@every`/cron need 2.14 and the platform
provides it.
