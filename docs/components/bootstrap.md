# Bootstrap

**Component reference** | Audience: implementers + architects | Contract authority: `docs/contracts/07-primordial-bootstrap.md` (primordial seeding, readiness gate)

---

## Overview

Bootstrap is the **one-shot provisioning binary** that turns an empty NATS/JetStream server into a
running Lattice kernel. It runs once per environment stand-up (invoked by `make up` after NATS and
Postgres containers are healthy), provisions every KV bucket / stream / object store the platform
needs, writes the primordial Core KV entries (Contract #7 §7.2 — the meta-meta DDL, the Capability
Lens anchor, the internal service-actor identities, the operator role + its grants), then exits 0.
It is **not** an always-on component — no process stays resident after a successful run — but it is
the seam every other component depends on existing before they can start.

Bootstrap is the **sole sanctioned non-Processor writer to Core KV** (Contract #7 §7.1): the kernel
must exist before the Processor can enforce anything, so bootstrap writes directly, once, under a
fixed provenance (`BootstrapIdentityKey`/`BootstrapOpKey`, a fixed `BootstrapTime` for deterministic
output) — never a channel any other component reuses.

---

## Two-phase commit + readiness phasing

Bootstrap solves two ordering problems that `make up` alone cannot:

1. **Crash-safe primordial IDs.** `LoadOrGenerate` implements a two-phase commit against
   `lattice.bootstrap.json`: no file → generate fresh NanoIDs, write the file with
   `status="in-progress"`, then seed; file with `status="in-progress"` → crash recovery, reuse the
   same IDs and re-run seeding (idempotent — `SeedPrimordial`'s own guard skips a key that already
   landed); file with `status="committed"` → load the IDs, then probe Core KV and skip seeding only
   if the bucket confirms the primordial set is there (see *Freshness probe* below). This keeps the
   NanoID set stable across restarts regardless of where a prior run crashed.
2. **The readiness-gate deadlock.** The §7.5 readiness gate blocks until the admin/Loom/Weaver
   `cap.*` projections exist — but those are produced by Refractor, which `make up` starts *after*
   seeding. Bootstrap runs in two invocations to avoid the deadlock: a seed pass with
   `-skip-ready-wait` (provision + seed + mark, no wait) runs first, Refractor starts, then a second
   idempotent pass (no flag, seeding already done) runs the readiness gate. The skip is an explicit
   CLI flag on the seed pass only — never an ambient env var — so an exported variable in an
   operator/CI shell can never leak into the second pass and silently defeat the gate.

---

## What this component owns

| Path | Role |
|------|------|
| `internal/bootstrap/primordial.go` | `Seeder` — `ProvisionBuckets` (KV buckets, the object store, the three JetStream streams) + `SeedPrimordial` (the ~75-entry primordial Core-KV batch, atomic) + the readiness marker (`MarkBootstrapComplete`/`WaitForBootstrapComplete`) |
| `internal/bootstrap/nanoid.go` | The stable primordial NanoID set + `lattice.bootstrap.json` two-phase-commit load/generate/persist |
| `internal/bootstrap/meta_ddl.go` | `MetaRootDDLScript` — the kernel's one DDL (Starlark), governing all `vtx.meta.*` mutations |
| `internal/bootstrap/install_ddl.go` | `InstallPackageDDLScript`/`UninstallPackageDDLScript` — the two DDLs that route Capability-Package install/uninstall through the Processor |
| `internal/bootstrap/lenses.go` | `LensDefinition` — the primordial Capability Lens (+ any other bootstrap-seeded Lens) payload shape |
| `internal/bootstrap/system_actors.go` | `SystemActorKeys`/`PrivacyActorKey` — discovers kernel-seeded service actors from the graph (root-designation topology: `holdsRole → operator`, not `data.protected`) |
| `internal/bootstrap/envelope.go` | `MakeVertexEnvelope`/`MakeAspectEnvelope` — deterministic envelope construction under the fixed bootstrap provenance |
| `internal/bootstrap/verify.go` | `VerifyKernel` — the callable assertion set `scripts/verify-kernel.go` and `lattice bootstrap verify` share |
| `cmd/bootstrap/main.go` | Binary entry point: connects to NATS, runs `ProvisionBuckets` → `SeedPrimordial` → `MarkBootstrapComplete` → the readiness wait |

---

## Kernel composition (what gets seeded)

Per Contract #7 §7.2, one atomic batch (`substrate.AtomicBatch` — all-or-nothing) writes, in
order: op tracker → identities → meta DDLs → Lens definitions → roles → permissions → links. Roughly:

- 1 bootstrap op tracker
- 1 primordial admin identity + 3 internal service-actor identities (Loom / Weaver / Bridge —
  arch §92) — later additions (object-store-manager, privacy) follow the same shape
- 1 meta-meta DDL vertex (`canonicalName="root"`) + 9 aspects
- 1 Capability Lens meta-vertex (the primordial-identity anchor) + 5 aspects
- 5 aspect-type meta-vertices × 7 aspects each
- 1 operator role vertex + 2 aspects
- 3 meta-permission vertices (`CreateMetaVertex`/`UpdateMetaVertex`/`TombstoneMetaVertex`, scope=any)
  + their `grantedBy → operator` links
- 1 admin→operator `holdsRole` link + 1 per internal service actor

Everything else — roles like `consumer`/`frontOfHouse`/`backOfHouse`, the identity DDL, RoleMgmt —
lives in packages (`rbac-domain`, `identity-domain`), not here: the kernel stays minimal
(Decision #10), packages carry business shape.

---

## In / Out contracts

| Direction | Contract | Notes |
|-----------|----------|-------|
| Out | KV buckets: `core-kv`, `health-kv`, `capability-kv`, `weaver-state`, `loom-state`, `weaver-targets`, `refractor-adjacency`, `personal-lens-interest`, `token-revocation` | idempotent `CreateOrUpdateKeyValue`; `AllowAtomicPublish` enabled on `core-kv` + `loom-state`'s underlying streams |
| Out | Object store `core-objects` | the off-graph blob plane (Contract #7 §7.2) |
| Out | Streams `core-operations`, `core-events`, `core-schedules` | Processor input, event outbox output, and the `@at`/`@every` scheduling stream (ADR-51) respectively |
| Out | Core KV primordial entries | the ~75-entry batch above, written directly (the one sanctioned non-Processor write, Contract #7 §7.1) |
| Out | `lattice.bootstrap.json` | the local two-phase-commit marker recording the stable NanoID set + committed/in-progress status |
| Out | readiness marker (NATS, `MarkBootstrapComplete`) | polled by `WaitForBootstrapComplete` / downstream readiness consumers |

---

## Key invariants

- **Idempotent by construction.** `ProvisionBuckets` always re-runs safely (`CreateOrUpdate*`);
  `SeedPrimordial` probes the op-tracker key first and skips the whole batch if it already exists.
- **All-or-nothing seeding.** The primordial batch is one `AtomicBatch` — a partial crash can never
  leave a half-seeded kernel visible to the Processor.
- **Deterministic output.** A fixed `BootstrapTime` + the stable NanoID set from
  `lattice.bootstrap.json` make every successful run produce byte-identical primordial envelopes.
- **The explicit-flag readiness skip.** `-skip-ready-wait` is a CLI flag, never an env var — the one
  invariant that keeps the readiness gate from being silently defeated by shell state.

---

## Failure modes

| Mode | Behavior |
|------|----------|
| Crash mid-seed (before `status="committed"`) | next run reuses the same NanoIDs (file says `in-progress`), re-runs `SeedPrimordial`; its idempotency guard skips already-committed keys |
| NATS not yet accepting connections | `connectNATSWithRetry` retries (20 attempts, 1s delay) before failing |
| Readiness gate times out (`cap.*` projections never appear) | seed pass exits 1 with `try make down && make up` — Refractor never came up or never projected |
| **`lattice.bootstrap.json` stale vs. a recreated Core KV** | Caught at two layers, `make up`'s first. With kernel processes up, `make up` runs `lattice bootstrap verify` and on mismatch deletes the file — recovering with a **fresh** ID set, orphaning existing references. Otherwise `cmd/bootstrap` probes Core KV itself (see *Freshness probe*) and re-seeds at the file's **stable** NanoIDs. Repro: `lattice bootstrap verify`. |

---

## Principles that apply

- **P2 exception, by design.** Bootstrap is the sole non-Processor Core-KV writer — a narrow,
  contract-named exception (Contract #7 §7.1) that exists only because the kernel must be seeded
  before the Processor has anything to enforce.
- **Decision #10 / minimal core.** The primordial set is deliberately small (~75 entries); role
  vocabulary, the identity DDL, and RoleMgmt all move to packages.
- **Determinism over cleverness.** Fixed timestamps + stable NanoIDs make the seeded kernel
  reproducible and diffable across environments, which is what makes `VerifyKernel` a meaningful gate.

---

## Implementation status

**Built and CI-gated.** `make verify-kernel` runs `VerifyKernel`'s assertions in CI; `go test
./internal/bootstrap/...` covers the seeder, the meta/install DDLs, and the two-phase-commit file
handling.

**Freshness probe.** `lattice.bootstrap.json` is file-local: it records what a bootstrap run once
did on some Core KV, not what this Core KV currently holds. A `status="committed"` file is therefore
never on its own grounds to skip seeding. After provisioning, `cmd/bootstrap` asks the bucket via
`Seeder.PrimordialSeeded`, which probes the op tracker key that `SeedPrimordial` writes first
(§7.4 — the op tracker is written first) and that therefore stands for the whole primordial set. Core KV — not the file — is
the authority on whether a given bucket has been seeded.

When the two disagree (a recreated or wiped Core KV behind a surviving committed file), bootstrap
logs a warning naming the disagreement, rewrites the file to `status="in-progress"`, and re-seeds
using its stable NanoIDs — so the restored keys are exactly the ones existing packages and data
already reference. Reopening the two-phase window is what makes the re-seed safe to interrupt: the
op tracker is written *first* (§7.4), so it marks a seed *started*, not finished, and a run that
died partway would otherwise leave the sentinel present, the rest of the kernel absent, and the file
still claiming `committed` — unrecoverable, because nothing would signal a retry. With the window
open, the next run reads `in-progress` and re-seeds.

A *partially* populated Core KV self-heals rather than erroring: the `CreateOnly` batch is rejected
with a revision conflict, and `seedPrimordialPerKey` fills only the absent keys and exits 0. That is
the same path a concurrent second bootstrapper takes, which is what it was built for.

**Two layers, and `make up`'s takes precedence.** `make up`'s reuse branch independently runs
`lattice bootstrap verify`, and on mismatch *deletes* `lattice.bootstrap.json` (Makefile) — so
bootstrap then starts with no file and mints a **fresh** NanoID set, orphaning existing references.
That branch only runs when the kernel processes are already up (`PROC_HEALTHY=1`), which is exactly
the recreated-containers-under-a-live-stack case, so on that path the binary-level probe never
fires. The probe therefore covers the remaining ones: a stopped-process stack, and any invocation
that never goes through `make` (Docker, CI, running the binary directly). Reconciling the two so the
stable-ID recovery wins where it can is tracked as a separate board item.

## Review keeps catching (dossier)

Same contract as every dossier: fire briefs copy the applicable entries into part 5
(`agents/fire-brief-template.md`); the item-close review appends new ones (`agents/steward/SKILL.md` §4);
**capped at 12 one-liners**; an entry retires when a lint/test gate mechanizes it.

- **A read-only computation added to `planReconcile` inherits the failure posture of the repair path it
  shares.** The plan has three consumers with different tolerances — `KernelOrphans` (informational),
  `KernelDrift` (drives `verify-kernel`'s exit status), `ReconcilePrimordial` (drives boot) — so an error
  returned from the shared function converts an advisory scan into a failed boot. Minted:
  kernel-orphan-retirement Inc 1 (the orphan listing's error reached all three; `verify-kernel` exit 1 on a
  converged bucket). Check: an advisory computation carries its error **in the plan**, and only its own
  reporter reads it — never a `return`.
- **A boot-time scan's cost is set by the population it ENUMERATES, not the one it is about.** The kernel is
  ~76 entries; `vtx.meta.>` on an installed bucket is 2,488 (388 roots, 2,100 aspects) and grows with every
  package. Minted: kernel-orphan-retirement Inc 1 — a design ledger row reading "~76 entries, a cheap
  boot-time read" licensed 4,487 sequential KVGets per plan and took `verify-kernel` 0.21s → 2.0s, on a path
  `make up` runs every invocation. Check: state the candidate count from a **live listing** before costing a
  scan, and derive from the listing anything the listing already knows (key presence needs no `KVGet`).
- **`BootstrapOpKey` identifies the deployment, not the binary generation.** Two binaries sharing one
  `lattice.bootstrap.json` compute the same op key, so "this key is bootstrap-provenanced and I don't build
  it" cannot distinguish *retired* from *newer than me*. Minted: kernel-orphan-retirement §12.4 — an older
  binary reads the current kernel's whole delta as retired. Check: any verb keyed on kernel provenance
  states its behaviour under a rollback, and fails closed when the stored generation exceeds the running
  binary's.
- **An unloaded primordial identifier reads as a value, not as "unconfigured" — so a predicate keyed on one
  answers "none" instead of failing.** The identifier table is package state populated by `Load` /
  `LoadOrGenerate`; a binary that never calls either leaves `RoleOperatorID` empty, and
  `SystemActorKeys`'s `id2 != RoleOperatorID` filter then matches no link and returns an empty set with no
  error. Every consumer reads that as "this deployment has no system actors". Minted: capability-kv single
  read path — `lattice capability review approve` and `cmd/refractor`'s control-plane checker both routed
  every actor, the primordial admin included, as ordinary. Check: a predicate keyed on a primordial
  identifier refuses an unloaded table (`ErrPrimordialIDsUnloaded`) before it reads the graph — and the test
  that covers such a path must reach it the way the BINARY does, not by calling `EnsurePrimordials` /
  `LoadOrGenerate` itself, which loads the file the binary never loads and hides the wiring gap.
- **`VerifyKernel` is not a report surface — its exit code is `make up`'s `FRESH` oracle, so a new failure
  class there changes the Makefile's control flow.** `cmd/lattice/bootstrap/bootstrap.go:134,165` exits 1 on
  any failure and `Makefile:202` reads that as "not fresh", falling through to `probe-empty` → `rm -f
  lattice.bootstrap.json` → a fresh epoch minted into the un-wiped bucket. Minted: stranded-epoch Fire 1 —
  a detector whose hard failure would have made the defect it detects self-amplify once per `make up`, with
  the reason swallowed by `>/dev/null 2>&1`. Check: the scope-diff gate runs over the **consumers of every
  value the fire changes**, not over the files it opens; a new entry in a `failures` slice states which
  exit codes it moves.
- **A reachability predicate that suppresses on "somebody holds it" is silenced when the holder is part of
  the same stranded island.** Rotation strands holder and held together, so "has a holder" refutes nothing;
  and capability here is conferred by the `holdsRole` edge the suppressor was reading, not by the
  `grantedBy` edge the severity keyed on — the gate was loudest on the inert state and quiet on the
  dangerous one. Minted: stranded-epoch Fire 1, twice (the suppressor at design time, the severity at
  review time). Check: a predicate over residue names which edge actually **projects** the capability —
  read the lens cypher, not the topology's shape — and scopes reachability to the *current* generation's
  actor set, never to "any actor at all".
