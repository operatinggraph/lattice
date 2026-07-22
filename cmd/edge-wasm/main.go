//go:build js

// Command edge-wasm is the Edge engine's browser artifact
// (edge-browser-node-design.md §3.3): the wasm binary the PWA loads into a Web
// Worker, containing the same semantics packages cmd/facet embeds natively.
//
// It is deliberately nothing but a registration and a park. Everything the
// page can do is internal/edge/browser's exported JS API, which lives there
// rather than here so it can be exercised by a real browser test — a main
// package cannot be imported by one.
//
// Build it with `make build-edge-wasm`.
package main

import (
	"log/slog"
	"os"

	"github.com/operatinggraph/lattice/internal/edge/browser"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	browser.Register(logger)
	// The JS API is callback-driven, so main must not return: a returned main
	// exits the wasm instance and every registered js.Func with it.
	select {}
}
