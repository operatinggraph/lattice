# Archive — Lattice parking lot

Rolled out of [`../lattice.md`](../lattice.md) to keep the live board under its size ceiling.
These are real but low-value: **do not spend design or build effort here unless Andrew greenlights one.**
A row that acquires a real driver moves back to the live board.

| Item | Why it's parked | Imp | Size | State |
|---|---|---|---|---|
| **Expose the authorizer's resolved roles to op scripts (`op.actorRoles`)** | Step 3 resolves the actor's roles but scripts cannot see them, so an op asking "is my caller root" re-derives it by walking `holdsRole` — which can disagree with what step 3 authorized, plus a `kv.Links` round trip per op. | ★★ | S | 📋 ready · consumer: the staff workplace guards ([staff-worlds F4](../../../implementation-artifacts/facet-staff-worlds-design.md)) |
| **Historical state query (FR51)** | Operators query historical state across a time range (audit/ledger + point-in-time reconstruction). Low value, standing cost. | ★ now / ★★ if real need | M→L | ✅ ratified · [design](../../../implementation-artifacts/historical-state-query-design.md) · build deferred (Andrew; revive on a concrete need) |
| multi-aspect atomic OCC for `UpdateMetaVertex` | `meta_ddl.go` applies `expectedRevision` to the first changed aspect by design; true multi-key OCC needs a substrate per-key-revision primitive. | ★ | M+ | 🗄️ parked |
| freshnessExpiry marker tombstone-on-convergence | A converged marker is read by nothing and harmless; tombstoning buys cleanup not correctness. | ★ | S | 🗄️ parked |
| production freshness-window tuning | A staleness-tolerance vs. timer-churn value judgment — Andrew's call if/when it matters. | ★ | XS | 🗄️ parked |

