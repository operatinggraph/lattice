# Sensitive-read tracker — flip on consumption, not on hydration

**Status:** ✅ Winston-ratified — build-ready (implementation-level throughout; no contract change).
**Lane:** Lattice (Stream 2). **Board row:** [Processor] Sensitive-read tracker flips at hydration, not at
consumption (★★ M).
**Filed by:** `4098ba21`, as the residual §2.5 of
[contexthint-existence-oracle-design.md](contexthint-existence-oracle-design.md) named and deliberately
did not reach.

## 1. The leak

Step 4 decrypts **every present** declared `reads` / `optionalReads` key whose class is sensitive and
flips `sensitiveReadTracker.plaintextRead` (`sensitive_decrypt.go:152`). Step 6's
`validateExternalEgressGuard` (`step6_validate.go:96`) then rejects the operation with a `DDLViolation`
if it emits any `external.*`-domain event while that flag is set.

The flag is keyed on what **hydration pre-fetched**, not on what the **script consumed**. So for an actor
holding a grant on any external-egress-emitting op, a *surplus* declared read splits:

- `reads: [vtx.identity.<id>.demographics]`, aspect **present** → step 4 decrypts → `plaintextRead` →
  `DDLViolation` (`externalEgressSensitivePlaintext`).
- same envelope, aspect **absent** → nothing decrypts → the op **commits**.

The script never names the key. Accept-vs-reject therefore answers *"does this key exist, and is its
class sensitive?"* — the same shape as the hydration existence oracle closed by `3a78c109`, surviving in
a different mechanism. `optionalReads` carries the identical split, and the deferred-miss fix could not
touch it: its absence branch is legitimate by design, so there is no fault to defer.

A declared key that exists but fails to decrypt is a third outcome (`ErrCodeInternalError`, reachable
only for an existing key) — same family, closed by the same change.

### 1.1 Why this is narrower than the closed oracle, and still worth closing

It needs an op that emits `external.*` **and** a grant on it, and it answers only "exists AND is
sensitive-classed" rather than plain existence. But the probe is still free: `contextHint` is
client-supplied, step 3 never inspects the declared read set, and the identity PII classes
(`identity-domain/ddls.go:223+`) are exactly the aspects worth probing for. Live external-egress
emitters exist today (`lease-signing`, `clinic-reminders`, `augur`, `capability-author`).

## 2. The fix — the tracker records, the script consumes

`decryptSensitiveDoc` stops flipping a boolean. It **records the key** whose `doc.Data` it replaced with
decrypted plaintext. `plaintextRead` then becomes true only where the **script itself** takes that
document, at the seams the script reaches through:

| Path | Site | Flips? |
|---|---|---|
| `kv.Read(K)` cache-first hit | `starlark_kv.go` hydrated branch | **yes** — names `K` |
| `kv.Read(K)` lazy live read | `starlark_kv.go` `KVReader.ReadVertex` branch | **yes** — names `K` |
| `state[K]`, `K in state`, `state.get(K)` | `stateMapValue.Get` | **yes** — names `K` |
| `state.items()`, `state.values()` | `stateMapValue.Attr` — re-bound | **yes** — hands out every hydrated value (§2.2) |
| `state.keys()`, `for k in state` | `Attr` / `Iterate` delegate untouched | **no** — key names only, no document |
| `egressReads` ref disposition | `sensitive_decrypt.go` egress branch | **no** — never decrypts (unchanged) |
| step 6's governing-DDL walk | `step6_resolve_ddl.go:287` | **no** — Processor-internal (§2.3) |

If the script consumes the plaintext, the outcome is **byte-identical to today** — same `DDLViolation`,
same `ViolatedConstraint`. If it does not, the declaration was surplus: it could not have put sensitive
plaintext into an external event, and it now does not change the reply either. That surplus case is
exactly and only the oracle.

### 2.1 Why the whole set flips on `items()` / `values()`

`state` is built eagerly over the entire hydrated set (`vertexMapToStarlarkWithHydrated`,
`starlark_runner.go:521`), and its values are already-decrypted plaintext. `items()` / `values()`
delegate straight to the underlying dict, so they hand the script every hydrated document **without
naming a key**. Flipping only on named keys would therefore have been a *real* leak, not a narrowing: a
script could enumerate values, derive from sensitive plaintext, and emit `external.*` with the guard
silent.

So these two enumerators flip whenever any recorded plaintext key exists. This is the one place where
this design and the deferred-miss design (§2.4 there: enumeration must **not** fault) deliberately part
company, and the reason is that they test different things. A deferred miss asks *"did the operation
depend on this key?"* — enumeration names none, and faulting there would answer "some declared read was
absent" to a caller who never named a key, which is the oracle itself. This guard asks *"did sensitive
plaintext reach the script?"* — and via `values()` it demonstrably did. Under-flipping here loses
containment; over-flipping there creates a leak. `keys()` and `Iterate` yield key names only and stay on
the non-flipping side in **both** designs.

### 2.2 The residual this leaves, and why it is not closable here

An op whose script enumerates `state.items()` / `values()` **and** emits `external.*` still splits on a
surplus sensitive declared read: present → recorded → consumed by the enumeration → `DDLViolation`;
absent → commits. That flip is *correct* (the plaintext genuinely reached the script), so the residual is
inherent to a guard keyed on consumption. Closing it needs the declared read set validated against the
actor's read scope — read-path authorization, D1, Andrew's fork (§1.2 of the oracle design).

**No live victim:** no `packages/` script enumerates `state.items()` / `values()` at all (the only
enumerating call sites are `internal/processor`'s own tests). Filed as a board row against D1, not built.

### 2.3 What is deliberately unchanged

- **The guard's semantics and blast radius.** `validateExternalEgressGuard` keeps its code, its
  `ViolatedConstraint`, its message, and its external-egress-only scope. Only the flag's provenance
  changes.
- **`egressReads`.** The `$sensitiveRef` disposition never decrypted and never flipped; it still
  records nothing.
- **The lazy `kv.Read` seam already was consumption-keyed** — a live read the script explicitly named.
  It keeps flipping, now through the same `consume` call rather than as a side effect of decryption.
- **Step 6's governing-DDL walk** (`step6_resolve_ddl.go:287`) shares `ScriptContext.KVReader`, so
  `decryptSensitiveDoc` runs there too. It is a Processor-internal read after the script returned, and it
  must not flip: putting the `consume` call in the script-facing seams (`kv.Read`, `stateMapValue`) rather
  than inside `ReadVertex` is what keeps it out. Today it *does* flip and is merely harmless because the
  guard has already run (`step6_validate.go:74` precedes `validateOne`); this change makes that
  correct rather than accidentally correct.
- **Membership (`K in state`) flips.** Starlark routes `in` and subscript through the same `Mapping.Get`,
  so they are indistinguishable at this seam. Flipping on membership is conservative — and never worse
  than today, which flips at hydration regardless.

## 3. Mechanism

```go
type sensitiveReadTracker struct {
	// plaintextRead is set when the SCRIPT consumed a sensitive aspect's
	// decrypted plaintext, not when hydration decrypted it.
	plaintextRead bool
	// plaintextKeys holds the keys whose Data was replaced with decrypted
	// plaintext, pending consumption.
	plaintextKeys map[string]struct{}
}

func (t *sensitiveReadTracker) markPlaintext(key string) // decrypt side
func (t *sensitiveReadTracker) consume(key string)       // script named key
func (t *sensitiveReadTracker) consumeAll()              // whole-set exposure
```

All three are nil-receiver-safe, mirroring `deferredMissTracker`. `consume` flips only for a recorded
key; `consumeAll` flips only when at least one key is recorded — so a non-sensitive working set never
trips the guard no matter how the script reads it.

`stateMapValue` gains a `sensitiveReads *sensitiveReadTracker` field, wired from `sc.SensitiveReads` in
`vertexMapToStarlarkWithHydrated` alongside the existing `requiredAbsent` / `deferredMiss` fields.

## 4. Increments

1. Tracker shape + `markPlaintext` / `consume` / `consumeAll`; `decryptSensitiveDoc` records instead of
   flipping.
2. Consume hooks: `kv.Read`'s hydrated branch + its lazy branch; `stateMapValue.Get`; `stateMapValue.Attr`
   re-binding `items` / `values`.
3. Tests (§5) + comment truth-up on the tracker doc comment, `sensitive_decrypt.go`'s `egress` paragraph,
   and `docs/components/processor.md` where it states the hydration-time flip.

## 5. Security proof obligations

Each negative vector verified non-vacuous by reverting the mechanism it pins.

- **Oracle closed (the point).** Two envelopes on an `external.*`-emitting op, identical but for a
  surplus `reads` of a sensitive-classed aspect that **exists** vs. one that does **not** — outcomes
  indistinguishable, and both `accepted`. Same vector for `optionalReads`.
- **Containment preserved, per path.** A script that consumes the sensitive plaintext and emits
  `external.*` still rejects with `externalEgressSensitivePlaintext` — via `kv.Read(K)`, via `state[K]`,
  via `state.get(K)`, via `K in state`, and via a lazy undeclared `kv.Read`.
- **Enumeration of values flips.** `for k, v in state.items()` and `for v in state.values()` reject
  (§2.1) — the vector that pins the leak under-flipping would open.
- **Enumeration of keys does not flip.** `for k in state` and `state.keys()` commit.
- **Non-sensitive working set never flips.** A script that reads its whole non-sensitive state via every
  path above and emits `external.*` commits.
- **`egressReads` still never flips.** The existing `sensitive_egress_test.go:220` assertion holds, now
  through consumption.
- **No external event, no guard.** A script that consumes sensitive plaintext and emits only ordinary
  domain events commits (unchanged scope).
- **OCC retry.** A fresh tracker per hydration attempt; consumption state does not leak across retries.

## 6. Non-goals

Read-scope authorization of declared keys (D1, Andrew's fork). Changing the guard's constraint name,
message, or external-egress-only scope. Changing the `egressReads` ref disposition. The §2.2 enumerating
residual. Any package/FE work — `internal/processor` only.

## 7. Build note — fire brief (Phase 0, 2026-07-25)

**Scope sentence (verbatim, board row):** "Step 4 flips `plaintextRead` when it decrypts a present
declared key, and step 6's egress guard rejects an `external.*` emitter that read plaintext — so a
surplus declared read of a sensitive-classed key still splits accept from `DDLViolation`. Fix: key the
tracker on what the script consumed."

**Scope diff:** narrow-only, no substitution. The mechanism above changes the tracker's provenance and
nothing else; the guard, the ref disposition, and `optionalReads`' absence semantics are untouched.

**Verified touch-list** (`file:line` checked live):

| Site | Change |
|---|---|
| `internal/processor/sensitive_decrypt.go:21-23` | tracker gains `plaintextKeys` + three methods |
| `internal/processor/sensitive_decrypt.go:151-153` | `tracker.plaintextRead = true` → `markPlaintext(doc.Key)` |
| `internal/processor/starlark_kv.go:82-84` | hydrated `kv.Read` branch → `consume(key)` |
| `internal/processor/starlark_kv.go:114-122` | lazy `kv.Read` branch → `consume(key)` |
| `internal/processor/starlark_runner.go:445-449` | `stateMapValue` gains `sensitiveReads` |
| `internal/processor/starlark_runner.go:458-466` | `Get` → `consume(key)` |
| `internal/processor/starlark_runner.go:498-501` | `Attr` re-binds `items` / `values` → `consumeAll()` |
| `internal/processor/starlark_runner.go:521-527` | wire `sc.SensitiveReads` into the wrapper |

**Precedents to mirror:** `deferredMissTracker` (same file) for the nil-safe shared-pointer tracker
shape; `RequiredAbsent`'s hook points (`starlark_kv.go:95`, `starlark_runner.go:460`,
`starlark_runner.go:498`) for where a script-facing seam intercepts a key name — the consume hooks land
at the same three places.

**Green checks:** `go build ./...`, `make vet`, `golangci-lint run ./...`,
`STRICT=1 go run ./scripts/lint-conventions.go`, `go test ./internal/processor/...`, then
`go test ./...`.

**In-scope gotchas:** (a) `step6_resolve_ddl.go:287` shares `ScriptContext.KVReader`, so the consume call
must **not** go inside `ReadVertex` (§2.3); (b) `state.get` is already re-bound for `RequiredAbsent` —
`items`/`values` join it, `keys` must stay delegated; (c) `sensitive_egress_test.go` constructs
`sensitiveReadTracker{plaintextRead: true}` literals in four places (`:293`, `:313`, `:325`) that must
keep working — the guard's own unit tests set the flag directly and stay valid.

**Non-goals:** as §6. The ~20 `packages/` read-posture comments are their own filed row.
