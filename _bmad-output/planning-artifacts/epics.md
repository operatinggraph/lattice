# Lattice — Epic Breakdown (sharded)

**This file is now a pointer.** The epic breakdown was sharded by phase on 2026-06-03 so that
`bmad-create-story` (SELECTIVE_LOAD over `*epic*/*.md`) loads only the relevant phase, and Phase 1
(shipped history) stays out of the way of active Phase 2 work.

## Where the content lives

| What | File |
|------|------|
| **Index** — Overview, Documentation Layering Rule, Requirements Inventory, Epic List, Phase 2 sprint reference, Story Totals | [`epics/index.md`](./epics/index.md) |
| **Phase 1 stories** (Epics 1–6, SHIPPED) | [`epics/phase-1-epics.md`](./epics/phase-1-epics.md) |
| **Phase 2 stories** (Epics 7–11, active) | [`epics/phase-2-epics.md`](./epics/phase-2-epics.md) |

Existing references to `epics.md` resolve here; follow the links above for detail. `bmad-create-story`
finds the shards via its sharded pattern and tries that *before* this whole-file pointer.
