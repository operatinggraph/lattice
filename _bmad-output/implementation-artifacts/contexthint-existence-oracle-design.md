# `contextHint.reads` pre-script existence oracle — deferred-miss hydration

**Status:** ✅ Winston-ratified — build-ready (implementation-level throughout; see §7 for the one
additive Contract #2 §2.5 paragraph left UNCOMMITTED for Andrew).
**Lane:** Lattice (Stream 2). **Board row:** `contextHint.reads` is a pre-script Core-KV existence oracle (★★★, M).
**Filed by:** `8375b4d4`, as the residual the package-level probe-before-guard sweep (`c4b45b33`) does not reach.

## 1. The leak

`contextHint` is a **client-supplied** field. The Gateway forwards it verbatim; `ParseEnvelope`
(`opwire.go:206`) validates only the enumeration shape and the `egressReads`-vs-`reads` ambiguity rule.
Step 3 authorizes on `operationType + actor + authContext` and **never inspects the declared read set**
(`step3_auth_capability.go` — no `contextHint` reference). Step 4 then hydrates every declared key
unconditionally, and a not-found in `reads` (`step4_hydrate.go:173`) or `egressReads`
(`step4_hydrate.go:243`) returns

```go
&HydrationError{Code: "HydrationMiss", MissingKey: key, OperationRequestID: rid}
```

which `classifyStepError` (`commit_path.go:908–913`) turns into `ErrCodeHydrationFailed` with
`details.missingKey`, serialized straight to the HTTP client (`gateway.go:528`).

So: **any actor holding any operation grant can test the existence of any Core KV key, one key per
round trip, without a script ever running.** The actor's own authorization is irrelevant to the answer —
the probe happens a full step before the package guards that decide whether they may touch the resource.

This is the same class the package sweep `c4b45b33` closed in-script ("ownership guards answer before
payload-matching probes"), one layer lower and strictly wider: the sweep's leaks each required a specific
op on a specific resource shape; this one is op-agnostic and key-agnostic.

### 1.1 Why stripping `missingKey` is not the fix

The obvious mirror of the `ClaimKeyInvalid` / NFR-S6 precedent (`commit_path.go:918` returns `nil` details)
is to strip `missingKey` from the reply. **That closes nothing here.** The attacker chose the key; they do
not need to be told it back. The discriminator is the *outcome*, not the detail:

- declare `reads: [K]` alongside an otherwise-legitimate op the actor is authorized for;
- `K` absent → `HydrationFailed`, the op is rejected;
- `K` present → the op runs and **succeeds** exactly as it would have.

Success-vs-rejection is the oracle. Any fix must make those two branches indistinguishable.

### 1.2 Why read-scope authorization is not the fix (here)

Validating each declared key against the actor's read scope is the architecturally complete answer, but
it *is* read-path authorization (D1) — a named Andrew fork, and far beyond this row. It is also not
needed to close this leak: the declared read set only matters where the operation consumes it.

## 2. The fix — defer the miss to first use

**A declared-but-absent required read stops being a step-4 fault and becomes a fault at the moment the
operation actually depends on the key.**

Step 4 records an absent `reads` / `egressReads` key in a new `RequiredAbsent` set and keeps hydrating.
The fault is then raised — with the same `HydrationMiss` code and the same `missingKey` — the first time
the operation touches that key, through any of its three dependence paths:

| Path | Site | Behavior |
|---|---|---|
| `kv.Read(K)` | `starlark_kv.go` cache-first branch | fault |
| `state[K]`, `K in state`, `state.get(K)` | `stateMapValue.Get` | fault |
| a mutation whose `key` is `K` | `StarlarkRunner.Run`, post-`parseScriptResult` | fault |

If the operation touches the key, **the outcome is byte-identical to today** — same error code, same
`details.missingKey`, same rejection. If it does not touch the key, the declaration was surplus: it could
not have changed the operation's result, and it now does not change the reply either. That surplus case
is exactly and only the oracle.

### 2.1 Why this closes it

To learn whether `K` exists an attacker must make the operation depend on `K`. The script is
package-installed and not attacker-controlled; the only lever on which keys a script reads or mutates is
the **payload**. Putting `K`'s id in the payload is precisely the path the ownership guards stand on —
the path `c4b45b33` just hardened so the guard answers before any payload-matching probe. The hydration
oracle therefore collapses into the script-guard surface, which is defended, rather than sitting a step
in front of it, which is not.

Declaring a key the script never names buys nothing: both branches succeed.

### 2.2 Why the write side must be covered too

`applyHydratedRevisions` (`commit_path.go:571`) defaults an update/tombstone's `expectedRevision` from the
step-4 snapshot and **skips keys not in `hydrated`**, leaving them *unconditioned* (`commit_path.go:584-587`).
Deferring the miss without covering mutations would therefore convert a script that blindly updates a
declared-but-absent key from "rejected at step 4" into "unconditioned write to a key that does not exist" —
a silent correctness regression, not just a diagnostic one. The mutation-key check in §2 closes that: a
mutation naming a `RequiredAbsent` key is dependence, and faults.

This check is not itself an oracle, by the §2.1 argument: reaching it requires the script to mutate the
probed key, which requires payload control through the guards.

### 2.3 What is deliberately unchanged

- **`optionalReads`** — already absence-tolerant (`step4_hydrate.go:206`), already not a processor-level
  oracle; its absence branch is script-visible by design (class (d)). Untouched.
- **`missingKey` in the reply** — kept. By the time it is emitted the operation demonstrably depended on
  the key, so the caller already named it; stripping it would cost operators the diagnostic and buy
  nothing. This is the opposite disposition from `ClaimKeyInvalid` for a reason: there, the *detail*
  (which claim outcome) was the secret; here the key is the caller's own input.
- **`NoDDLForClass` / `NoScriptForClass` / `EmptyScript`** — these name meta-vertex keys in the DDL
  namespace, derived from the op's `class`, not from a free-form client key list. Not this row's shape.
  Not widened, not narrowed.
- **Fail-closed discipline.** `reads` still means "absence is a correctness error." A script that reads
  its declared key still fails closed on absence. §7 records the one contract sentence this refines.

## 3. Mechanism

`ScriptContext` gains two fields:

```go
// RequiredAbsent holds declared `reads` / `egressReads` keys that were NOT
// found at the step-4 snapshot. Unlike KnownAbsent (optionalReads, class (d),
// which reads as None) these are fail-closed: touching one faults HydrationMiss.
RequiredAbsent map[string]struct{}
// DeferredMiss records the first RequiredAbsent key the operation actually
// touched, so the runner can raise the HydrationMiss the step-4 read deferred.
DeferredMiss *deferredMissTracker
```

`deferredMissTracker` is a one-shot recorder (first key wins), mirroring `sensitiveReadTracker`'s
shared-pointer shape. The tracker exists so the fault survives Starlark's error wrapping: the builtin /
mapping returns an ordinary eval error to abort the script, and `StarlarkRunner.Run` consults the tracker
**before** `classifyScriptError`, synthesizing the `*HydrationError` directly. Nothing depends on
`EvalError` unwrap semantics.

`Run` checks the tracker on **both** exit paths — the error path (normal) and the success path
(defensive: if a future mapping ever swallows the error, the operation still fails closed rather than
committing having silently skipped a required read).

Ordering inside `kv.Read` matters: `Hydrated` → `RequiredAbsent` → `KnownAbsent` → lazy live GET. A key
in `Hydrated` was present, so it can never also be required-absent; checking `RequiredAbsent` before the
lazy fallthrough is what stops a required-absent key from silently degrading into a live re-read.

## 4. Increments

1. `deferredMissTracker` + the two `ScriptContext` fields; step 4 records instead of faulting, for both
   `reads` and `egressReads`.
2. `kv.Read` + `stateMapValue.Get` fault on a `RequiredAbsent` key.
3. `StarlarkRunner.Run` raises the deferred `*HydrationError` (both exit paths) and rejects a mutation
   naming a `RequiredAbsent` key.
4. Tests (§5) + comment truth-up at the four sites that document the old pre-script fatality
   (`starlark_kv.go:40-44`, `step4_hydrate.go:28`, `script_context.go`, `opwire.go:53`).

## 5. Security proof obligations

The negative vectors must each be verified non-vacuous by reverting the mechanism they pin.

- **Oracle closed (the point).** Two ops identical but for a surplus declared read of a key that exists
  vs. one that does not — outcomes must be *indistinguishable*, and both must be `accepted`.
- **Fail-closed preserved, per path.** A script that reads its declared-absent key via `kv.Read`, via
  `state[K]`, and via `K in state` — each still rejects with `HydrationMiss` + the right `missingKey`.
- **Write side.** A script emitting an update/tombstone on a declared-absent key rejects with
  `HydrationMiss`, and does **not** reach the commit unconditioned.
- **`egressReads` parity.** Same deferral, same fault — the existing
  `TestEgressReads_MissingKey_HydrationMiss` moves from step-4 to first-use.
- **`optionalReads` unchanged.** A key in both lists keeps `reads` semantics (the existing
  `optional_reads_test.go:95` assertion), and a pure `optionalReads` miss still reads as `None`.
- **OCC retry.** A deferred miss across a re-hydration retry behaves identically each pass (fresh tracker
  per attempt).

## 6. Non-goals

Read-scope authorization of declared keys (D1, Andrew's fork). Changing `optionalReads`. Stripping
`missingKey`. Touching the DDL-namespace hydration errors. Any package/FE work — this is `internal/processor`
only.

## 7. Contract note — UNCOMMITTED for Andrew

Contract #2 §2.5's authoring rule says a key whose absence is a correctness error MUST go in `reads`
("fail-closed `HydrationMiss`"). That remains exactly true. What this design refines is *when* the fault
is raised — at first use rather than at step 4 — and therefore that a declared key the operation never
touches no longer rejects it.

The contract does not state the step-4 timing as a normative guarantee, so the build does not depend on
the edit. But the distinction is load-bearing enough to be written down, so an additive paragraph is
prepared in `docs/contracts/02-operation-envelope.md` §2.5 and left **UNCOMMITTED** in `main` per
CLAUDE.md — that diff is the proposal. No frozen invariant changes.
