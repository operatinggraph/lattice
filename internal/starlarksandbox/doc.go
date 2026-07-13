// Package starlarksandbox is the shared verified-pure Starlark execution
// leaf: the compile+thread+cancellation harness, the pure (I/O-free,
// deterministic) builtin modules, and the Go<->Starlark converters used by
// every write-path script surface on the platform. The Processor's DDL
// scripts are its first consumer; a Loom predicate guard (internal/loom,
// `{reads, starlark}`, loom-starlark-guards-design.md Fire 2) is the second,
// using Validate at pattern-load time and Execute at guard-eval time.
//
// Purity charter: this package imports only go.starlark.net and the Go
// standard library — zero internal Lattice packages. It is a primitive
// (the same category as internal/substrate), not an engine: it holds no
// NATS handle, no KV client, no vendor client, and makes no host-clock or
// environment read. A caller wires in whatever impure builtins its own
// domain needs (the Processor's `kv.Read`, `nanoid.new`) as ordinary
// starlark.StringDict entries passed to Execute — the sandbox neither
// knows nor enforces what a caller-supplied builtin does; it only
// guarantees that a name NOT present in the globals it is given cannot be
// referenced, and that `load(...)` always fails.
package starlarksandbox
