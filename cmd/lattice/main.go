// Command lattice is the unified Lattice CLI tool (FR45). It provides
// operator and developer access to all Phase 1 Lattice capabilities:
// submitting operations, inspecting graph state, querying projection
// surfaces, and reading platform health — without a browser client
// and without writing custom NATS code.
//
// Usage:
//
//	lattice [command-group] [subcommand] [flags]
//
// Environment:
//
//	NATS_URL         default nats://localhost:4222
//	LATTICE_CONFIG   default ~/.lattice/config.json
package main

func main() {
	Execute()
}
