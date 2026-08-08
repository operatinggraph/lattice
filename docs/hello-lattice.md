# Hello Lattice — 60-minute tutorial

This tutorial walks through the complete Lattice vertical slice: define an entity type,
create entities, project them to Postgres via a Lens, query them with an AI agent, and
roll a schema change back — all from `git clone` to a working demo in under 60 minutes.

It is also runnable non-interactively via `examples/hello-lattice/Makefile` (`make demo`).

## Prerequisites

A running `make up` stack with the identity + RBAC packages installed (Milestone 5
needs them — see the [Quick start](../README.md#quick-start)):

```console
lattice health gates    # gates 1–4 should show passed: true
lattice bootstrap verify
lattice-pkg install packages/identity-domain   # if not already installed
lattice-pkg install packages/rbac-domain       # if not already installed
```

Obtain your bootstrap actor key (needed for all meta-lane operations):

```console
lattice bootstrap inspect
# or
lattice graph keys vtx.identity.
```

Export it as `BOOTSTRAP_ACTOR_KEY` for use in the tutorial commands below.

---

## Milestone 1: Setup

**Goal:** Verify the platform is healthy and primordial state is confirmed.
**Expected time:** ≤ 10 min

```console
lattice health gates
```

Expected output:

```console
health.gates.phase1.gate1  passed: true
health.gates.phase1.gate2  passed: true
health.gates.phase1.gate3  passed: true
health.gates.phase1.gate4  passed: true
```

```console
lattice bootstrap verify
```

Expected output: `OK` on every primordial key check.

---

## Milestone 2: Define "book" entity type

**Goal:** Register the book DDL meta-vertex via `CreateMetaVertex`.
**Expected time:** ≤ 10 min

The "book" DDL is a normal user-defined DDL submitted at runtime — it is not
primordially seeded. Submit it on the `meta` lane:

```console
lattice op submit \
  --operation-type CreateMetaVertex \
  --lane meta \
  --actor $BOOTSTRAP_ACTOR_KEY \
  --payload '{
    "targetClass": "meta.ddl.vertexType",
    "canonicalName": "book",
    "permittedCommands": ["CreateBook"],
    "description": "Book vertex DDL. A book carries a title.",
    "script": "def execute(state, op):\n    p = op.payload\n    if not hasattr(p, \"title\") or len(p.title.strip()) == 0:\n        fail(\"InvalidArgument: title: required non-empty string\")\n    title = p.title.strip()\n    book_id = nanoid.new()\n    book_key = \"vtx.book.\" + book_id\n    mutations = [{\"op\": \"create\", \"key\": book_key, \"document\": {\"class\": \"book\", \"isDeleted\": false, \"key\": book_key, \"title\": title, \"data\": {\"title\": title}}}]\n    events = [{\"class\": \"BookCreated\", \"data\": {\"bookKey\": book_key}}]\n    return {\"mutations\": mutations, \"events\": events, \"response\": {\"bookKey\": book_key}}",
    "inputSchema": "{\"type\":\"object\",\"required\":[\"title\"],\"properties\":{\"title\":{\"type\":\"string\",\"maxLength\":500}}}",
    "outputSchema": "{\"type\":\"object\",\"required\":[\"bookKey\"],\"properties\":{\"bookKey\":{\"type\":\"string\"}}}",
    "fieldDescription": {"title": "Book title, max 500 characters. Required."},
    "examples": [{"name": "CreateBook — minimal", "payload": {"title": "The Pragmatic Programmer"}, "expectedOutcome": "Creates vtx.book.<NanoID>; returns bookKey."}]
  }'
```

Expected output:

```console
requestId:    <NanoID>
opTrackerKey: vtx.op.<NanoID>
status:       accepted
metaKey:      vtx.meta.<NanoID>
```

Verify the DDL was written:

```console
lattice graph keys vtx.meta.
# find the vtx.meta.<NanoID> key from the reply, then:
lattice graph read vtx.meta.<NanoID>
```

Expected: document with `class: meta.ddl.vertexType` and all 9 aspects (canonicalName,
permittedCommands, description, script, inputSchema, outputSchema, fieldDescription,
examples, compensation).

---

## Milestone 3: Author Starlark rule and create a book vertex

**Goal:** Submit a `CreateBook` operation; verify the book vertex appears in Core KV.
**Expected time:** ≤ 10 min

The Starlark script was submitted as part of the DDL in Milestone 2 — no separate
script-upload step is needed for a fresh DDL.

```console
lattice op submit \
  --operation-type CreateBook \
  --lane default \
  --actor $BOOTSTRAP_ACTOR_KEY \
  --payload '{"title":"The Pragmatic Programmer"}'
```

Expected output:

```console
requestId:    <NanoID>
opTrackerKey: vtx.op.<NanoID>
status:       accepted
bookKey:      vtx.book.<NanoID>
```

Verify the book vertex:

```console
lattice graph read vtx.book.<NanoID>
```

Expected: `class: book`, `data.title: "The Pragmatic Programmer"`.

---

## Milestone 4: Author Lens projection and query Postgres

**Goal:** Register a Lens that projects all `book` vertices to a Postgres `books` table;
query it via `lattice query postgres`.
**Expected time:** ≤ 10 min

**Step 4a — Provision the target table (out-of-band).** The Refractor's Postgres adapter
is thin: it upserts rows into an **existing** table and issues no table DDL. Create the
`books` table before registering the Lens, with columns matching the Lens RETURN
(`book_id`, `title`). The default delete mode is **hard** (`DELETE FROM`), so no
soft-delete columns are needed (those are only required for `deleteMode: soft` targets):

```console
docker exec -i lattice-postgres psql -U lattice -d lattice -c \
  'CREATE TABLE IF NOT EXISTS books (book_id TEXT PRIMARY KEY, title TEXT);'
```

**Step 4b — Register the Lens.** The `spec` field must be a JSON string containing a
`LensSpec` object. The platform decodes it and stores it verbatim as the `.spec` aspect
data for the Refractor.

```console
lattice op submit \
  --operation-type CreateMetaVertex \
  --lane meta \
  --actor $BOOTSTRAP_ACTOR_KEY \
  --payload '{
    "targetClass": "meta.lens",
    "canonicalName": "books",
    "description": "Projects all book vertices to the Postgres books table.",
    "spec": "{\"canonicalName\":\"books\",\"targetType\":\"postgres\",\"targetConfig\":{\"dsn\":\"postgres://lattice:lattice_dev@localhost:5432/lattice?sslmode=disable\",\"table\":\"books\",\"key\":[\"book_id\"]},\"cypherRule\":\"MATCH (b:book) RETURN b.key AS book_id, b.title AS title\",\"engine\":\"simple\"}"
  }'
```

Expected output:

```console
requestId:    <NanoID>
opTrackerKey: vtx.op.<NanoID>
status:       accepted
metaKey:      vtx.meta.<NanoID>
```

The Refractor picks up the new Lens via CDC within ≤ 500ms and projects all existing
`book` vertices to the `books` table.

Query Postgres:

```console
lattice query postgres "SELECT * FROM books"
```

Expected:

```console
book_id                              | title
-------------------------------------+------------------------
vtx.book.<NanoID>                    | The Pragmatic Programmer
```

Check Lens lag:

```console
lattice lens lag
```

Expected: `lag: 0` once projection is complete.

---

## Milestone 5: AI traversal query

**Goal:** Create an AI agent identity, grant it `CreateBook`, and run the cold-start
traversal program.
**Expected time:** ≤ 10 min

**Step 5a — Create a new identity for the AI agent:**

```console
lattice op submit \
  --operation-type CreateUnclaimedIdentity \
  --lane default \
  --actor $BOOTSTRAP_ACTOR_KEY \
  --payload '{}'
```

Note the `identityKey` from the reply (e.g. `vtx.identity.<agentId>`).

**Step 5b — Create a `CreateBook` permission:**

```console
lattice op submit \
  --operation-type CreatePermission \
  --lane default \
  --actor $BOOTSTRAP_ACTOR_KEY \
  --payload '{"operationType":"CreateBook","scope":"any"}'
```

Note the `permissionKey` from the reply.

**Step 5c — Grant the permission to the operator role:**

```console
# Get operator role key:
lattice graph keys vtx.role.

lattice op submit \
  --operation-type GrantPermission \
  --lane default \
  --actor $BOOTSTRAP_ACTOR_KEY \
  --payload '{"permKey":"vtx.permission.<permId>","roleKey":"vtx.role.<operatorId>"}'
```

**Step 5d — Assign the agent to the operator role:**

```console
lattice op submit \
  --operation-type AssignRole \
  --lane default \
  --actor $BOOTSTRAP_ACTOR_KEY \
  --payload '{"actorKey":"vtx.identity.<agentId>","roleKey":"vtx.role.<operatorId>"}'
```

**Step 5e — Run the AI agent program:**

```console
AGENT_ACTOR_KEY=vtx.identity.<agentId> go run examples/hello-lattice/ai-agent.go
```

Expected output:

```console
Agent has N platform permission(s)
CreateBook permission confirmed in capability set
Book DDL key: vtx.meta.<NanoID>
Verified: DDL permittedCommands includes CreateBook
DDL inputSchema: {"type":"object","required":["title"],...}
CreateBook accepted!
  requestId:   <NanoID>
  opTracker:   vtx.op.<NanoID>
  bookKey:     vtx.book.<NanoID>

Verify the projection:
  lattice query postgres "SELECT * FROM books WHERE title = 'Hello Lattice (AI Agent)'"

Done.
```

After the Refractor projects (≤ 500ms):

```console
lattice query postgres "SELECT * FROM books WHERE title = 'Hello Lattice (AI Agent)'"
```

Expected: one row with the new book.

---

## Milestone 6: Rollback the book DDL (≤ 5 min)

**Goal:** Demonstrate the compensation contract — roll back the book DDL itself by
reading its `.compensation` aspect and submitting the inverse operation.
**Expected time:** ≤ 5 min

**Step 6a — Read the compensation aspect:**

```console
lattice graph read $BOOK_DDL_KEY.compensation
```

The aspect data contains `inverseOperationType: TombstoneMetaVertex` plus template
fields for the metaKey and expectedRevision. These encode exactly what to submit to
undo the DDL creation.

**Step 6b — Capture the current revision for conflict detection:**

```console
lattice graph read $BOOK_DDL_KEY
```

Note the `_revision` field in the output. This is passed as `expectedRevision` in the
tombstone payload to prevent a lost-update race.

**Step 6c — Submit the compensating TombstoneMetaVertex:**

```console
lattice op submit \
  --operation-type TombstoneMetaVertex \
  --lane meta \
  --actor $BOOTSTRAP_ACTOR_KEY \
  --context-hint-reads $BOOK_DDL_KEY \
  --payload "{\"metaKey\":\"$BOOK_DDL_KEY\",\"expectedRevision\":<revision>}"
```

Expected reply: `status: accepted`.

**Step 6d — Verify the book DDL is no longer discoverable:**

```console
lattice graph keys vtx.meta.
```

The book DDL key still appears in the key list (tombstoned entries are retained
in KV), but `lattice graph read $BOOK_DDL_KEY` now shows `isDeleted: true`.

Confirm via the traversal API — any attempt to discover the `book` DDL should return
`ErrDDLNotFound` because the vertex's `isDeleted` flag is `true`.

**Step 6e — Verify the compensation aspect reads "none":**

```console
lattice graph read $BOOK_DDL_KEY.compensation
```

Expected: `data.inverseOperationType: none` — the irreversibility signal. Once a
DDL is tombstoned, there is no inverse to the tombstone.

**Step 6f — Verify subsequent CreateBook is rejected:**

```console
lattice op submit \
  --operation-type CreateBook \
  --lane default \
  --actor $BOOTSTRAP_ACTOR_KEY \
  --payload '{"title":"This should be rejected"}'
```

Expected reply: `status: rejected` — the Processor's DDL cache no longer has an
entry for `book`, so `CreateBook` cannot be resolved.

## Appendix — recommended business link names (FR55)

The capability cyphers walk business-graph topology by link name. These names ship with this
reference implementation as **recommended conventions only** — they are not platform-reserved; a
deployment may standardize on its own link types and author its cyphers against those (Contract #6 §6.9).

| Link name | Used between | Semantics |
|-----------|--------------|-----------|
| `containedIn` | Location vertices (unit → building, room → unit, building → property) | Physical or logical containment; transitive |
| `availableAt` | Service vertex → location vertex | Service is offered at this location (and by default, at locations contained within) |
| `unavailableAt` | Service vertex → location vertex | Explicit exclusion override; closer exclusion wins over distant availability |
| `leases` | Identity → lease vertex | Actor holds a lease; lease references a unit via `containedIn` from the unit side |
| `residesIn` | Identity → location vertex | Actor resides at this location (independent of lease — guests, family, etc.) |
| `assignedTo` | Task vertex → identity vertex | Task is assigned to the actor; grants ephemeral capability per FR56 |
| `reportsTo` | Identity → identity | Reporting chain for manager-delegated task auth per FR56 |
