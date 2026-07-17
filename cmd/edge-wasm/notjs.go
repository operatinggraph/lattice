//go:build !js

package main

import (
	"fmt"
	"os"
)

// This binary is the Edge engine's browser artifact and only builds under
// GOOS=js GOARCH=wasm (see main.go). Building it for a native target is a
// mistake — almost certainly a plain `go build ./...` that should have been
// `make build-edge-wasm` — so it fails loudly rather than producing a binary
// that does nothing. It also gives the package buildable Go for a non-js GOOS,
// so `go list ./...` and the unit-shard-coverage gate do not trip on it.
func main() {
	fmt.Fprintln(os.Stderr, "edge-wasm builds only for GOOS=js GOARCH=wasm; run `make build-edge-wasm`.")
	os.Exit(1)
}
