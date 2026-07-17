//go:build !js

// Package browser is the Edge engine's browser (js/wasm) host. Its whole
// implementation is behind //go:build js — see host.go, jstransport.go,
// jsvalue.go, feed.go — because it is nothing but syscall/js interop over the
// shared semantics packages (store, overlay, sync, agent).
//
// This file exists only so the package has buildable Go for a non-js GOOS:
// without it, `go list ./...` and the unit-shard-coverage gate would fail on a
// package whose files are all excluded by build constraints.
package browser
