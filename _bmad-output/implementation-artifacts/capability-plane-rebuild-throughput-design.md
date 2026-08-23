# Capability-plane rebuild throughput — a declined guarded write is audited as a write that happened

**Status: ✅ Winston-ratified — build-ready** (2026-08-23). No frozen-contract change, no architectural
fork: the fix restores the behaviour `AuditWriter.WriteAudit`'s own doc comment already specifies, and
mirrors the delete-side precedent already shipped in the same function.

Owning item: `backlog/lattice.md` — *[Refractor] Capability-plane lenses wedged rebuilding, backlog barely
draining*.

## 1. What the live stack actually shows

Measured against the running stack (`health.refractor.rfx-152edb7ebd98`, refractor uptime 7 h, 58.7 % CPU),
2026-08-23 04:43–04:52 UTC.

**The rebuilds are not wedged — they are throughput-bound.** `rebuildProgressAt` advances every poll cycle.
Drain measured over 421 s:

| lens | outstanding | drain | ETA at measured rate |
|---|---|---|---|
| `edgeManifestReadGrants` | 41 963 | 0.235 /s | **~50 h** |
| `capabilityServiceAccess` | 34 501 | 0.570 /s | **~17 h** |
| `landlordLeaseApplicationsRead` | 4 328 | 0.309 /s | **~4 h** |

Combined: **1.11 CDC messages/s**. The row's "draining <150/10 min" is confirmed and now quantified.

Two consequences follow from the duration alone, and neither is a new mechanism:

- `capabilityServiceAccess` carries `sweepSuppression: "rebuild in flight"` and `sweepLastPassAt: ""` — the
  convergence sweep has produced **no per-row verdict since the consumer was created** (2026-08-22 16:32),
  and will not for ~17 h more. `CapabilitySweepStalled` is reporting this correctly.
- `capability` holds `blocked: 15` on the `projectedFromRevisions` watermark family, whose repair machinery
  is already ratified and shipped (`capability-projection-reconciliation-design.md`).

## 2. Why one CDC message costs ~200 writes

Both capability-plane lenses carry the broad `$KV.core-kv.>` consumer filter over 50 107 subjects. That
breadth is **forced and fail-closed**, not an oversight: `ReferencedLabels` (`ruleengine/full/labels.go`)
reports `exhaustive = false` for a cypher with a variable-length hop or an unlabeled position, and
`pipeline/filter.go` then refuses to narrow. `capabilityServiceAccess`'s own doc comment states this and
pins it (`label_derivation_corpus_census_test.go`, verdict `broad / modeBroad`).

With nothing to attribute a changed key to, the pipeline enumerates actors, so each CDC message re-evaluates
the whole actor set — 139 `cap.svc.*` keys, ~9 076 `cap-read.edgeManifest*` keys. Sampled live: **200 audit
entries per CDC message**, 100 % of them from the two capability-plane lenses.

**This part is by construction and is NOT what this fire changes.** See §5.

## 3. The defect this fire fixes

`internal/refractor/adapter/natskv.go:188-207` — the **guarded** upsert path returns `Wrote: true`
unconditionally, deliberately, and for a stated reason that is entirely about the watermark:

> advancing — or deliberately holding — the projectionSeq watermark is this path's job on every call
> regardless of row content, so it must never gain a row-content skip the way the unguarded path below has.

That reasoning is sound and this fire does not touch it. The bug is downstream:
`internal/refractor/pipeline/results.go:77` reads only `outcome.Wrote`, never `outcome.DeclinedByWatermark`,
and line 137 gates the audit publish on `wrote` alone. So **an upsert the ordering guard declined — no KV
write, row unchanged — still publishes an audit entry asserting an upsert happened**, carrying the
`outputRowHash` of a row that was never written.

This contradicts the audit writer's own contract (`audit_writer.go:39,126`): *"appended … on each successful
write"*, *"publishes one audit entry for a committed successful write"*.

**It is the unfixed half of a symmetric pair.** `results.go`'s own comment records the delete side being
fixed for precisely this reason — *"a plain Delete discards whether the guard actually committed — so a
retraction the ordering guard DECLINED was still audited as a write that happened"* — which is why
`OutcomeDeleter` exists. `OutcomeUpserter` already carries `DeclinedByWatermark`; nothing reads it.

### Measured blast radius

| measurement | value |
|---|---|
| audit publishes | **804.7 /s** |
| `capability-kv` puts (same 40 s window) | **197.9 /s** |
| ratio | **4.07 : 1** — ~75 % of audit entries assert a write that never landed |
| `REFRACTOR_AUDIT` declared `MaxAge` | 7 days |
| `REFRACTOR_AUDIT` **effective** retention (first_ts → last_ts) | **54.7 minutes** |

The forensic trail is self-evicting at ~0.5 % of its declared window, and it takes **every other lens's**
audit history with it. The 512 MiB cap is working as designed (`refractor-footprint-reduction-design.md`
Fire 5, shipped); the firehose it is absorbing is the defect.

## 4. The fix

In `writeResults`, gate the audit publish on the write having actually committed — not on `Wrote` alone.
`Wrote`'s meaning and the guarded path's watermark behaviour are unchanged; only the audit gate narrows.

Proof is a unit test, not the live inference above: a guarded adapter whose write is declined by the
watermark must publish **no** audit entry, while an identical accepted write must publish exactly one.

## 5. Explicit non-goals

- **Not narrowing the broad consumer filter.** It is a fail-closed consequence of non-exhaustive label
  derivation; narrowing it without reshaping the cypher would grant access under partial expansion.
- **Not changing actor fan-out.** `DerivationModeAct` is the built-in default, and the refractor's own log
  records why it cannot act on these three lenses — a **static** refusal, fixed for the life of the
  ruleState, not a per-event fall-back (no `anchor-derivation tally` line is ever emitted for them):

  | lens | logged refusal reason |
  |---|---|
  | `capabilityServiceAccess` | *"pattern carries a variable-length relationship"* |
  | `edgeManifestReadGrants` | *"pattern carries a variable-length relationship"* |
  | `landlordLeaseApplicationsRead` | *"it uses target-diff retraction, which would read a per-anchor row set as every OTHER anchor's rows"* |

  The variable-length hop is the same root that forces the broad consumer filter, so filter breadth and
  actor enumeration are one gap seen twice, not two. Deriving anchors across a variable-length relationship
  is an absent primitive with no ratified pattern to extend — filed as a designer pass, not built here.
- **Not touching `Wrote` semantics or the guarded watermark write.**
- **Not changing `REFRACTOR_AUDIT` limits.** Retention returns on its own once the firehose stops.

### Saturation symptoms, deliberately not filed

`refractor.log` carries **7** ERROR lines total for the whole 7 h run — `context deadline exceeded` on a
`capability-kv` list/get inside fan-out evaluation. They are the load showing through, not an independent
defect, and they have no separate mechanism to fix; they should disappear with the firehose. Re-check after
the fix rather than filing them now.

## 6. Fire brief (build note)

**Scope sentence:** *A guarded upsert declined by the ordering guard must not publish an audit entry.*

**Touch-list** (verified live):
- `internal/refractor/pipeline/results.go:62-138` — the write/audit loop; `wrote` assignment (77), audit gate (137)
- `internal/refractor/adapter/natskv.go:182-232` — `upsert`, guarded branch returning `DeclinedByWatermark`
- `internal/refractor/adapter/adapter.go:196-240` — `UpsertOutcome` / `OutcomeUpserter` doc contract
- `internal/refractor/health/audit_writer.go:126-145` — `WriteAudit`'s stated contract

**Precedent to mirror:** the delete half — `OutcomeDeleter` / `DeleteWithOutcome` in the same loop
(`results.go:68-72`), introduced for this exact failure mode.

**Gotchas (dossier, `docs/components/refractor.md`):**
- *A label narrows the binder, not necessarily the consumer filter* (retired to
  `label_derivation_corpus_census_test.go`) — do not "fix" the broad filter here.
- *New pipeline state without a declared lifetime* — this fire adds no new state; keep it that way.
- Go-embedding trap (`adapter.go`): a test double embedding `*NatsKVAdapter` and overriding only `Upsert`
  still promotes `UpsertWithOutcome` straight through. Fake the outcome method the pipeline actually calls.

**Green checks:** `go build ./...` · `make vet` · `golangci-lint run ./...` ·
`STRICT=1 go run ./scripts/lint-conventions.go` · `go test ./internal/refractor/...` · `make verify-kernel`.

**Live verification:** rebuild + cycle `bin/refractor`, then re-measure the audit:put ratio — it must fall
from ~4:1 toward ~1:1, and `REFRACTOR_AUDIT`'s retention window must start extending past ~55 min.
