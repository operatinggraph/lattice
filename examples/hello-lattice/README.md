# Hello Lattice — Example Files

This directory contains the reference implementation files for the Hello Lattice
60-minute tutorial.

See the root [README.md](../../README.md) section **Hello Lattice (60-minute tutorial)**
for the full step-by-step walkthrough.

## Files

| File | Purpose |
|------|---------|
| `book-ddl.yaml` | Reference payload for the "book" DDL (Milestone 2) |
| `books-lens.yaml` | Reference payload for the "books" Lens (Milestone 4) |
| `ai-agent.go` | Standalone AI agent program for Milestone 5 |
| `Makefile` | `make demo` runs all five milestones end-to-end |

## Quick start

```console
# From repo root — start infrastructure
make up

# Set actor key
export BOOTSTRAP_ACTOR_KEY=$(lattice graph keys vtx.identity. | head -1)

# Run all milestones
cd examples/hello-lattice
make demo
```
