# Core KV CR Fix Summary — Phase 1.5

## Applied Fixes

| Finding | File:Line | Description |
|---|---|---|
| F-002 | `internal/substrate/conn.go:107-126` | Hold mutex across `KeyValue()` open call to eliminate TOCTOU race on first bucket open |
| F-003 | `internal/substrate/kv.go:165-181` | Export `isRevisionConflict` as `IsRevisionConflict`; update two internal callers |
| F-003 | `internal/bootstrap/primordial.go:239,260` | Replace `looksLikeCreateConflict` calls with `substrate.IsRevisionConflict` |
| F-003 | `internal/bootstrap/primordial.go:271-282` | Delete `looksLikeCreateConflict` function (bootstrap duplicate) |
| F-004 | `internal/substrate/kv.go:27-46` | Doc-only: extend `KVGet` godoc explaining Core KV holds logical-delete entries by design |
| F-005 | `internal/substrate/subscribe.go:131-154` | `normalizePrefix` now returns `(string, error)`; bare literal without `.`/`>`/`*` suffix returns error from `SubscribeKVChanges` |
| F-006 | `internal/substrate/kv.go:152-186` | Add `KVDeleteRevision(ctx, bucket, key, expectedRevision)` for optimistic-concurrency delete; update `KVDelete` godoc to note it is unconditional |
| F-007 | `internal/substrate/kv.go:106-120` | Doc-only: extend `KVListKeys` godoc noting logical-delete envelopes are not filtered |
| F-008 | `internal/substrate/envelope.go:70-84` | Add panic guards for empty `class`/`actor`/`opTracker` in `NewDocumentEnvelopeAt` and `UpdateAt` |
| N-001 | `internal/substrate/keys.go:124-139` | Implement `CanonicalLinkOrder(aKey, aCreatedAt, bKey, bCreatedAt) (younger, older)` |
| N-002 | `internal/substrate/subscribe.go:19-26` | Remove `KVEvent.Sequence` field (always equalled `Revision`); remove assignment in `decodeKVMessage` |
| N-002 | `internal/substrate/subscribe_test.go:71-73` | Update assertion from `evt.Sequence` to `evt.Revision` |
| N-003 | `internal/substrate/conn.go:74-77` | Replace "reserved for future use" comment with concrete note: `nats.Connect` does not accept context; use `ConnectOpts.MaxReconnects`/`ReconnectWait` |
| N-004 | `internal/substrate/subscribe.go:162-172` | Drop `streamName` parameter from `runKVSubscription`; remove `_ = durableName` suppression |
| N-005 | `internal/substrate/batch.go:265` | Replace `return nil, fmt.Errorf("unreachable")` with `panic("substrate: publishAtomicBatch: unreachable")` |
| HC batch.go:60-62 | `internal/substrate/batch.go:60-62` | Remove "Story 1.1 spike findings" reference; keep nats.go API gap fact |
| HC batch.go:71-72 | `internal/substrate/batch.go:71-72` | Remove "Story 1.1 spike documented this" reference; keep single-bucket constraint |
| HC batch.go:156 | `internal/substrate/batch.go:155-156` | Remove "Story 1.8 step 9" reference from `PublishOp` godoc |
| HC batch.go:187-188 | `internal/substrate/batch.go:186-188` | Remove "Story 1.1 spike findings (Behavioral Test 3b)" reference; keep all-or-nothing fact |
| HC batch.go:231-232 | `internal/substrate/batch.go:230-233` | Remove "ported from the Story 1.1 spike" reference; keep protocol description |

## Skipped

| Finding | Reason |
|---|---|
| F-001 | Deferred — Story #11 (substrate write-path contracts) |

## Build / Test Results

- `go build ./...` — pass (clean)
- `go vet -unreachable=false ...` — pre-existing failure in `internal/processor/nfr_r1_test.go` (EventPublisher interface mismatch, unrelated to these changes; confirmed by zero diff on that file)
- `go test ./internal/substrate/... -p 1 -count=1` — pass (`ok` in 1.695s)
