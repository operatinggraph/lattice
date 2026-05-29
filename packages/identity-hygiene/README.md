# identity-hygiene

**Installable Lattice Capability Package.**

Provides duplicate-identity detection + operator-approved merge as an
opt-in package layered on top of the in-bootstrap identity DDL.

## What you get

After `lattice-pkg install packages/identity-hygiene`:

- **`duplicateCandidates` Lens** projects pairs of non-merged identities
  that match on exact email, exact phone, or `levenshteinRatio(name) >=
  0.85` into the `duplicate-candidates` KV bucket. Output key shape:
  `flagged.identity.<lo-id>.identity.<hi-id>`. Each entry also carries
  `secondaryInboundEdges` + `secondaryOutboundEdges` — the link vertex
  keys touching the (would-be) secondary, enumerated by Refractor via
  `collect(DISTINCT ...)` so the operator CLI can construct
  `MergeIdentity` without scanning the graph. The lens reprojects
  automatically as identity / link vertices come and go.

- **`MergeIdentity` operation** lets an operator commit a merge of two
  identities the lens has flagged. The caller passes `edges` (the
  union of `secondaryInboundEdges` + `secondaryOutboundEdges` read from
  the lens) as a command parameter. The script:
  - Verifies both identity vertices exist and neither is `merged`.
  - Validates every declared edge against Core KV: must be a link
    envelope, must endpoint-touch the secondary, must not be tombstoned.
    (Actors are not trusted — fabricated, stale, or mis-targeted edge
    keys reject with `EdgeNotFound` / `EdgeNotALink` /
    `EdgeDoesNotTouchSecondary`.)
  - Tombstones every validated link and re-creates each against primary
    (self-loops tombstone only; primary-side collisions primary-wins).
  - Transitions secondary `.state` → `merged`; sets `.mergedInto` →
    primary key.
  - Optionally applies `aspectConflictResolution` for `name` / `email`
    / `phone` (`secondary-wins`).
  - Emits an `IdentityMerged` event.
  - Returns a commit-trace shaped `OperationReply.Detail` — counts and
    keys only, no business data leak.

- **MergeIdentity permission** + grant link to the operator role.

## What this package does NOT do

- It does not provide a duplicate-candidate review CLI verb. Consumers
  read the `duplicate-candidates` bucket directly (or via a separate
  read-CLI in a future story).
- It does not configure the levenshtein threshold — 0.85 is hard-coded.
  A future package version may parameterize this via a Lens `parameters`
  aspect.
- It does not migrate existing data. Pre-package identities flow through
  the lens on first CDC tick after install.

## Install notes

- Install writes directly to `core-kv` as an atomic batch.
- Uninstall is soft-delete only (tombstones remain queryable for audit).
- The installer uses the admin actor NanoID from `lattice.bootstrap.json`.
