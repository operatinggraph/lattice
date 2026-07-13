// Package loom implements the Loom orchestration engine: a generic linear-
// sequence interpreter that drives deterministic procedures ("patterns") to
// completion. The engine ships zero domain knowledge — patterns are package
// data (meta.loomPattern meta-vertices) and the engine interprets them.
//
// Steps are systemOp or userTask, each optionally carrying a §10.5 guard (a
// pure on/off predicate over the subject's current Core KV state — either the
// declarative atom/composition grammar, or the {reads, starlark} escape hatch;
// guard.go / guard_eval.go / guard_starlark.go). A false guard skips its step
// (cursor advances, no op); replaying guards forward over a partially-populated
// subject rebuilds the cursor, which is what makes a lost loom-state
// recoverable (§10.6) — a Starlark guard carries the same purity/determinism
// obligation as the declarative grammar (loom-starlark-guards-design.md §2.3).
// The loader spine, per-domain completion consumers, write-ahead cursor, and
// crash-safe restart are fully present.
//
// Module boundary (Contract / Story 8.1 AC #8): loom imports ONLY
// internal/substrate + stdlib, with ONE sanctioned exception —
// internal/starlarksandbox (the shared verified-pure Starlark leaf; zero
// internal deps of its own beyond go.starlark.net + stdlib) for the guard
// Starlark escape hatch. Every OTHER cross-component interaction is via NATS:
//
//   - patterns are loaded from Core KV via a durable KV-changes subscription
//     (the same mechanism Refractor uses for lens defs);
//   - a pattern instance is triggered by a committed StartLoomPattern op whose
//     events.loom.patternStarted event Loom consumes on a fixed durable
//     (Contract #10 §10.9) — the trigger is on the event plane, never a direct
//     Go call;
//   - systemOps + the event-only lifecycle ops (CompletePattern / FailPattern)
//     are submitted to core-operations via the command outbox: the op is written
//     as a loom-state outbox.<token> record in the SAME AtomicBatch as the cursor
//     transition (no dual write), and a durable relay fire-and-forget publishes it
//     and deletes the record on publish-ack (re-publish idempotent via the chosen
//     requestId + the Contract #4 tracker). The Processor stays the sole Core KV
//     writer / event producer — Loom never writes Core KV or publishes events
//     directly, P2;
//   - step completions are consumed from core-events (one durable consumer per
//     referenced completionDomain) and correlated by a direct token.<requestId>
//     GET on loom-state — domain-independent, multi-instance-safe, no in-memory
//     index (Contract #10 §10.6);
//   - a rejected/lost op is invisible on core-events (no tracker, no event), so
//     the failed terminal is learned off-stream via a per-step deadline.<instanceId>
//     TTL: its expiry (a KeyValuePurge/MaxAge marker) trips a read-before-act
//     probe (GET vtx.op.<token>: committed → advance + alert; not yet relayed →
//     re-arm; rejected → fail) — never a synchronous submit-reply (§10.6);
//   - the per-instance cursor, the co-located token reverse index, the outbox
//     record, and the deadline mark all live in the operational loom-state bucket;
//     each step transition is one AtomicBatch.
//
// The substrate consumer mechanisms are kept visibly separate (3 + N durables):
// exactly one pattern source (Conn.SubscribeKVChanges, durable
// "loom-pattern-source") answering "what patterns exist"; exactly one fixed
// trigger consumer (Conn.RunDurableConsumer, durable "loom-trigger") answering
// "what started a flow"; exactly one command-outbox relay + one deadline watcher
// (Conn.RunDurableConsumer on the loom-state backing stream, durables
// "loom-outbox-relay" / "loom-deadline") driving op submission and the timeout
// backstop; and N per-domain completion consumers (Conn.RunDurableConsumer,
// durable "loom-<domain>") answering "what completions happened".
package loom
