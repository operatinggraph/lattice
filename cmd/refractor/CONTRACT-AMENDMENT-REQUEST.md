# Contract / Planning Artifact Amendment Requests (Story 2.1)

These are planning-artifact-only corrections discovered during Story 2.1 morph work. Each is a text-only fix in `_bmad-output/planning-artifacts/epics.md`; no code impact.

---

## Request 1: epics.md AC #1 binary name

**Location:** `epics.md` Story 2.1 AC #1
**Current text:** "binary is `lattice-refractor`"
**Requested text:** "binary is `refractor`"
**Rationale:** All other Lattice binaries (`bootstrap`, `processor`, `refractor-stub`) use bare component names; no `lattice-` prefix is established convention. Winston ratified `refractor` as the binary name in handoff brief Decision #1.

## Request 2: epics.md AC #2 key prefix table

**Location:** `epics.md` Story 2.1 AC #2 — text mentioning `vtx.*`, `asp.*`, `lnk.*` prefix routing
**Current text:** Implies `asp.*` is a top-level key prefix for aspects.
**Requested text:** Replace with reference to `data-contracts.md` Contract #1 §1.5: aspects are keyed `vtx.<type>.<id>.<localName>` (4-segment `vtx.` prefix), NOT `asp.*`. Classification is by key SHAPE (segment count) and the value document's `class` field, not by raw prefix string matching. Use `substrate.ClassifyKey`.
**Rationale:** `asp.*` does not exist in the data contract. The stale text predates the contract finalization.

## Request 3 [Story 2.1b — RESOLVED]: data-contracts.md §1.7 meta-vertex key shape

**Location:** `data-contracts.md` Contract #1 §1.7 (meta-vertex pattern)
**Issue:** The handoff brief Decision #5 references `vtx.meta.lens.<NanoID>` as the lens-definition key. This is a 4-segment key, which `substrate.ClassifyKey` treats as an ASPECT, not a vertex. Story 2.1 used `vtx.lens.<NanoID>` (3-segment) instead.
**Requested clarification:** Either (a) confirm §1.7 actually uses the 3-segment shape `vtx.<reservedType>.<id>` where `meta` is a type-prefix convention (in which case the brief wording is just imprecise), or (b) define the multi-segment meta-vertex shape as a substrate extension with explicit support in `ClassifyKey`. See MORPH-DEVIATIONS Deviation 12.

**Resolution (Story 2.1b):** Option (a) confirmed by data-contracts.md §1.2 line 70: "`lens`, `event`, `ddl`, `actor` — these are *flavors of `meta`*, distinguished by the document's `class` field (`meta.lens`, `meta.event.<name>`, `meta.ddl.vertexType`, etc.)" The canonical lens key shape is `vtx.meta.<NanoID>` (3-segment vertex with type `meta`) with the document envelope's `class` field set to `"meta.lens"`. No amendment to `data-contracts.md` is needed; the Story 2.1 handoff brief Decision #5 wording was just imprecise (it said `vtx.meta.lens.<NanoID>` when it should have said `vtx.meta.<NanoID>` with class `meta.lens`). Story 2.1b corrected the implementation accordingly — see MORPH-DEVIATIONS Deviation 12 resolution. The 3-segment shape with class-based routing is also what `internal/bootstrap/primordial.go` already uses for the Capability Lens (line 322: `MakeVertexEnvelope(CapabilityLensKey, "meta.lens", ...)`), confirming the pattern across Lattice components.
