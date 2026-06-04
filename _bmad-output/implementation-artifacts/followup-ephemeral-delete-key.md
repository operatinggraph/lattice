# Follow-up — ephemeral pipeline actor-deletion delete-key derivation

**Status:** done — spun off from Story 7.1 retro-review (886dadd); fix shipped (CI green). Lens-aware actor-delete-key derivation; capabilityRoleIndex verified NOT affected (no actorEnumerator). Accepted judgment call: consumer-side `processor.ephemeralKeyFromActor` left local to avoid dragging refractor/pipeline+NATS into the auth hot-path.
**Tier:** Opus (security-plane Capability KV delete logic in the generic pipeline).
**Workflow:** dev sub-agent → 3-layer adversarial review → Winston adjudicates/commits/CI. Do NOT commit/push/branch; leave changes in the working tree. Follow CLAUDE.md (no history comments). Do NOT edit frozen contracts (`docs/contracts/*`) or planning artifacts.

## The bug
The generic Refractor pipeline derives the actor-deletion delete key with a hardcoded `capabilityKeyForActor(...)` → `cap.<actor>`, in BOTH actor-disappearance paths:
- `internal/refractor/pipeline/evaluate.go:88-94` — actor-tombstone shortcut.
- `internal/refractor/pipeline/evaluate.go:351-356` — `reprojectActors` missing-actor path.

For the **`capabilityEphemeral`** pipeline (whose envelope projects to `cap.ephemeral.<actor>` via `internal/refractor/capabilityenv/envelope.go:ephemeralKey`), an actor identity removal therefore deletes the WRONG key (`cap.<actor>`) and leaves `cap.ephemeral.<actor>` **orphaned**. (Story 7.1's FIX 1 only covered the grant-expiry path via `ErrDeleteProjection`; the actor-disappearance shortcut fires before the envelope runs, so it was untouched.)

Severity is low (a removed actor can't authenticate; NanoIDs aren't reused) — but it leaks stale ephemeral grant docs and is a defense-in-depth gap on the auth plane.

## Fix direction (sub-agent: choose the cleanest within this shape)
Make the actor-deletion delete-key derivation **lens-aware** so each pipeline deletes the key its own envelope projects to:
- Add an optional injectable derivation to `Pipeline` (e.g. a field `actorDeleteKey func(actorKey string) string`), used by BOTH delete paths instead of the hardcoded `capabilityKeyForActor`. Default (unset) → preserve today's `capabilityKeyForActor` (`cap.<actor>`) behavior so the PRIMARY capability pipeline is unchanged.
- Wire it in `cmd/refractor/main.go` where the `capabilityEphemeral` pipeline is constructed: pass a derivation that yields `cap.ephemeral.<actor>` — reuse the SAME logic as `capabilityenv.ephemeralKey` (export it or a shared helper; do NOT duplicate the prefix string in two places that can drift). The consumer side already derives the same key in `internal/processor/step3_auth_capability.go:ephemeralKeyFromActor` — keep all three consistent (one source of truth if practical).

## Also verify (flag, don't silently change)
- **capabilityRoleIndex pipeline**: it projects actor-INDEPENDENT keys (`cap.role-by-operation.<operationType>`). Confirm whether it installs an `actorEnumerator` and thus reaches the actor-tombstone shortcut. If it does, deleting `cap.<actor>` there is ALSO wrong (its keys aren't actor-keyed) — determine the correct behavior (likely: the role-index doesn't delete on actor tombstone; it reprojects). If affected, flag it for Winston rather than silently fixing in this pass unless the fix is obvious and in-scope.

## Tests
- Actor tombstone on the ephemeral pipeline → Delete against `cap.ephemeral.<actor>` (NOT `cap.<actor>`).
- `reprojectActors` missing-actor on the ephemeral pipeline → same.
- Regression: the PRIMARY capability pipeline still deletes `cap.<actor>` on actor tombstone (unchanged).

## Gates
`go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`, `make test-bypass` (Gate 2 all BLOCKED), `make test-capability-adversarial` (Gate 3 all DEFENDED), `go test ./internal/refractor/... ./internal/processor/...`. Docker stack is UP (NATS nats://localhost:4222, Postgres DSN per Makefile). Revert any auto-regenerated gate report churn. Flake retry per Deviation 14 allowed.

## Closing summary (append when done)
What you changed (+ how the derivation is injected), the capabilityRoleIndex verdict, test count (from diff), gate results, any deviation/halt. Do NOT commit.

---

### Closing summary (dev)

**What changed**
- `internal/refractor/pipeline/pipeline.go`: added an optional injectable field
  `actorDeleteKey func(actorKey string) string` to `Pipeline` plus a
  `SetActorDeleteKey` setter. Nil (default/unset) preserves the primary
  `capabilityKeyForActor` (`cap.<actor>`) behaviour.
- `internal/refractor/pipeline/evaluate.go`: both actor-disappearance delete
  paths (the actor-tombstone shortcut, and the `reprojectActors` missing-actor
  branch) now call a new `(*Pipeline).actorDeleteKeyFor` helper, which uses the
  injected derivation when set and falls back to `capabilityKeyForActor`
  otherwise. The hardcoded `capabilityKeyForActor(...)` calls in those two
  spots are gone.
- `internal/refractor/capabilityenv/envelope.go`: exported the ephemeral
  key derivation as `EphemeralKey` (was unexported `ephemeralKey`) so it is the
  single source of truth; `NewEphemeralWrapper` now calls `EphemeralKey`.
- `cmd/refractor/main.go`: the `capabilityEphemeral` pipeline now wires
  `p.SetActorDeleteKey(capabilityenv.EphemeralKey)` alongside its envelope +
  enumerator. The PRIMARY `capability` pipeline is untouched (no
  `SetActorDeleteKey` call → default `cap.<actor>`).

**How the derivation is injected**: one source of truth =
`capabilityenv.EphemeralKey`. The producer wraps it into the envelope (project)
and passes the *same function value* into the pipeline via `SetActorDeleteKey`
(delete). No new copy of the `"cap.ephemeral."` prefix string was introduced on
the producer side. The consumer (`processor.ephemeralKeyFromActor`) keeps its
own local derivation **deliberately** — importing `capabilityenv` into
`internal/processor` would drag the whole `refractor/pipeline` (+NATS) tree into
the auth hot-path package, which today imports only `substrate`. That decoupling
is intentional; the consumer is a separate trust boundary and the derivation is
a trivial prefix swap. Flagged here rather than forced.

**capabilityRoleIndex verdict: NOT AFFECTED.** In `cmd/refractor/main.go` the
`capabilityRoleIndex` case installs only `SetEnvelopeFn` + `SetLatencyBuffer` —
it does **not** call `SetActorEnumerator`, so `p.actorEnumerator == nil`. Both
delete paths are gated on `actorEnumerator != nil`: the tombstone shortcut
(`evaluate.go`) skips when nil, and `reprojectActors` is only reachable via the
fan-out paths which require an enumerator. The role-index therefore never
reaches either actor-deletion delete path and never emits a `cap.<actor>`
Delete. No change made; no behavioural decision required.

**Tests added (4, in new `internal/refractor/pipeline/actor_delete_key_test.go`):**
1. `TestActorTombstone_EphemeralDeleteKey` — tombstone shortcut on a pipeline
   with `actorDeleteKey = ephemeral` → Delete keyed `cap.ephemeral.identity.<id>`.
2. `TestReprojectActors_MissingActor_EphemeralDeleteKey` — missing-actor
   reproject path → same ephemeral key.
3. `TestActorTombstone_DefaultDeleteKey_Unchanged` — no derivation installed →
   `cap.identity.<id>` (primary regression).
4. `TestReprojectActors_MissingActor_DefaultDeleteKey_Unchanged` — same default
   regression on the reproject path.
White-box (`package pipeline`) using an in-memory NATS server for real
`ErrKeyNotFound` on the missing-actor read.

**Gate results (all green):**
- `go build ./...` — PASS
- `make vet` — PASS
- `golangci-lint run ./...` — 0 issues
- `make verify-kernel` — ALL ASSERTIONS PASSED
- `make test-bypass` — Gate 2 4/4 BLOCKED
- `make test-capability-adversarial` — Gate 3 4/4 (3 DEFENDED, 1 ACCEPTED-WINDOW)
- `go test ./internal/refractor/... ./internal/processor/...` — all PASS
- Regenerated `gate2-report.txt` / `gate3-report.txt` churn reverted.

**Deviation/halt:** none. One judgment call flagged above (consumer-side
derivation left local to avoid an import-cycle / hot-path dependency bloat).
