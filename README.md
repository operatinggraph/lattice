# Lattice

Lattice is a graph-native application platform that stores entities as vertices and
relationships as links in a Core KV store, executes user-defined Starlark scripts on
the write path, and projects graph subsets to query targets (Postgres, NATS KV) via a
Refractor lens pipeline. AI agents discover operations cold-start by reading the
platform's self-describing DDL graph.

## Prerequisites

- Go 1.26+
- Docker + Docker Compose
- `make`

## Quick start

```console
# Start NATS + Postgres, bootstrap primordial state, start Refractor
make up

# Confirm everything is healthy
lattice health gates
```

Expected output:

```console
health.gates.phase1.gate1  passed: true
health.gates.phase1.gate2  passed: true
health.gates.phase1.gate3  passed: true
health.gates.phase1.gate4  passed: true
```

---

## Hello Lattice (60-minute tutorial)

This tutorial walks through the complete Lattice vertical slice: define an entity type,
create entities, project them to Postgres via a Lens, and query them with an AI agent —
all from `git clone` to a working demo in under 60 minutes.

### Prerequisites

A running `make up` stack. Confirm with:

```console
lattice health gates    # gates 1–4 should show passed: true
lattice bootstrap verify
```

Obtain your bootstrap actor key (needed for all meta-lane operations):

```console
lattice bootstrap inspect
# or
lattice graph keys vtx.identity.
```

Export it as `BOOTSTRAP_ACTOR_KEY` for use in the tutorial commands below.

---

### Milestone 1: Setup

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

### Milestone 2: Define "book" entity type

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

### Milestone 3: Author Starlark rule and create a book vertex

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

### Milestone 4: Author Lens projection and query Postgres

**Goal:** Register a Lens that projects all `book` vertices to a Postgres `books` table;
query it via `lattice query postgres`.
**Expected time:** ≤ 10 min

The `spec` field must be a JSON string containing a `LensSpec` object. The platform
decodes it and stores it verbatim as the `.spec` aspect data for the Refractor.

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

### Milestone 5: AI traversal query

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

## Architecture

See [`docs/components/_index.md`](docs/components/_index.md) for the component overview.

---

## Development

```console
# Build all binaries
make build

# Run all unit + integration tests (serialised for embedded NATS stability)
make test

# Lint
golangci-lint run ./...

# Go vet (ANTLR-generated files excluded)
make vet

# Gate tests
make verify-kernel
make test-bypass
make test-capability-adversarial
make test-rollback
```
