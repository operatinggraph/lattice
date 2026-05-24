# Hello Lattice — Example Makefile
#
# Runs all five tutorial milestones non-interactively against a live
# `make up` stack. Requires:
#   - Docker stack running (cd ../.. && make up)
#   - BOOTSTRAP_ACTOR_KEY env var (obtain via: lattice bootstrap inspect)
#   - NATS_URL (defaults to nats://localhost:4222)
#   - POSTGRES_URL (defaults to postgres://lattice:lattice_dev@localhost:5432/lattice?sslmode=disable)
#
# Usage:
#   export BOOTSTRAP_ACTOR_KEY=$(lattice graph keys vtx.identity. | head -1)
#   make demo

NATS_URL ?= nats://localhost:4222
POSTGRES_URL ?= postgres://lattice:lattice_dev@localhost:5432/lattice?sslmode=disable
LATTICE ?= lattice

.PHONY: help demo milestone-1 milestone-2 milestone-3 milestone-4 milestone-5

## help — Show available targets.
help:
	@echo "Hello Lattice demo targets:"
	@echo "  demo         — Run all five milestones end-to-end"
	@echo "  milestone-1  — Verify health gates and bootstrap"
	@echo "  milestone-2  — Define book entity type (CreateMetaVertex)"
	@echo "  milestone-3  — Create a book vertex (CreateBook)"
	@echo "  milestone-4  — Register books Lens and query Postgres"
	@echo "  milestone-5  — AI agent cold-start traversal (requires AGENT_ACTOR_KEY)"

## demo — Run all five milestones end-to-end.
demo: milestone-1 milestone-2 milestone-3 milestone-4 milestone-5
	@echo "==> All milestones complete. Hello Lattice!"

## milestone-1 — Verify health gates and bootstrap.
milestone-1:
	@echo "==> Milestone 1: Setup"
	@echo "--- Health gates:"
	NATS_URL=$(NATS_URL) $(LATTICE) health gates
	@echo "--- Bootstrap verify:"
	NATS_URL=$(NATS_URL) $(LATTICE) bootstrap verify
	@echo "--- Milestone 1 done."

## milestone-2 — Define the book entity type DDL.
milestone-2:
	@if [ -z "$(BOOTSTRAP_ACTOR_KEY)" ]; then \
	  echo "ERROR: BOOTSTRAP_ACTOR_KEY is required."; \
	  echo "  Run: export BOOTSTRAP_ACTOR_KEY=\$$(lattice graph keys vtx.identity. | head -1)"; \
	  exit 1; \
	fi
	@echo "==> Milestone 2: Define book DDL"
	NATS_URL=$(NATS_URL) $(LATTICE) op submit \
	  --operation-type CreateMetaVertex \
	  --lane meta \
	  --actor $(BOOTSTRAP_ACTOR_KEY) \
	  --payload '{"targetClass":"meta.ddl.vertexType","canonicalName":"book","permittedCommands":["CreateBook"],"description":"Book vertex DDL.","script":"def execute(state, op):\n    p = op.payload\n    if not hasattr(p, \"title\") or len(p.title.strip()) == 0:\n        fail(\"InvalidArgument: title: required non-empty string\")\n    title = p.title.strip()\n    book_id = nanoid.new()\n    book_key = \"vtx.book.\" + book_id\n    mutations = [{\"op\": \"create\", \"key\": book_key, \"document\": {\"class\": \"book\", \"isDeleted\": False, \"key\": book_key, \"title\": title, \"data\": {\"title\": title}}}]\n    events = [{\"class\": \"BookCreated\", \"data\": {\"bookKey\": book_key}}]\n    return {\"mutations\": mutations, \"events\": events, \"response\": {\"bookKey\": book_key}}","inputSchema":"{\"type\":\"object\",\"required\":[\"title\"],\"properties\":{\"title\":{\"type\":\"string\",\"maxLength\":500}}}","outputSchema":"{\"type\":\"object\",\"required\":[\"bookKey\"],\"properties\":{\"bookKey\":{\"type\":\"string\"}}}","fieldDescription":{"title":"Book title, max 500 characters. Required."},"examples":[{"name":"CreateBook — minimal","payload":{"title":"The Pragmatic Programmer"},"expectedOutcome":"Creates vtx.book.<NanoID>; returns bookKey."}]}'
	@echo "--- Milestone 2 done."

## milestone-3 — Create a book vertex.
milestone-3:
	@if [ -z "$(BOOTSTRAP_ACTOR_KEY)" ]; then \
	  echo "ERROR: BOOTSTRAP_ACTOR_KEY is required."; \
	  exit 1; \
	fi
	@echo "==> Milestone 3: Create book vertex"
	NATS_URL=$(NATS_URL) $(LATTICE) op submit \
	  --operation-type CreateBook \
	  --lane default \
	  --actor $(BOOTSTRAP_ACTOR_KEY) \
	  --payload '{"title":"The Pragmatic Programmer"}'
	@echo "--- Milestone 3 done."

## milestone-4 — Register the books Lens and query Postgres.
milestone-4:
	@if [ -z "$(BOOTSTRAP_ACTOR_KEY)" ]; then \
	  echo "ERROR: BOOTSTRAP_ACTOR_KEY is required."; \
	  exit 1; \
	fi
	@echo "==> Milestone 4: Register books Lens"
	NATS_URL=$(NATS_URL) $(LATTICE) op submit \
	  --operation-type CreateMetaVertex \
	  --lane meta \
	  --actor $(BOOTSTRAP_ACTOR_KEY) \
	  --payload '{"targetClass":"meta.lens","canonicalName":"books","description":"Projects book vertices to Postgres books table.","spec":"{\"canonicalName\":\"books\",\"targetType\":\"postgres\",\"targetConfig\":{\"dsn\":\"$(POSTGRES_URL)\",\"table\":\"books\",\"key\":[\"book_id\"]},\"cypherRule\":\"MATCH (b:book) RETURN b.key AS book_id, b.title AS title\",\"engine\":\"simple\"}"}'
	@echo "--- Waiting for Refractor to project (polling, up to 10 seconds)..."
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
	  count=$$(NATS_URL=$(NATS_URL) $(LATTICE) query postgres "SELECT COUNT(*) FROM books" 2>/dev/null | grep -E '^[1-9]' | head -1); \
	  if [ -n "$$count" ]; then break; fi; \
	  sleep 1; \
	done
	@echo "--- Query books table:"
	NATS_URL=$(NATS_URL) $(LATTICE) query postgres "SELECT * FROM books"
	@echo "--- Milestone 4 done."

## milestone-5 — AI agent cold-start traversal.
milestone-5:
	@if [ -z "$(AGENT_ACTOR_KEY)" ]; then \
	  echo "SKIP: milestone-5 requires AGENT_ACTOR_KEY (an identity with CreateBook permission)."; \
	  echo "  Grant it via: lattice op submit CreatePermission + AssignRole"; \
	  exit 0; \
	fi
	@echo "==> Milestone 5: AI agent traversal"
	NATS_URL=$(NATS_URL) AGENT_ACTOR_KEY=$(AGENT_ACTOR_KEY) go run ./ai-agent.go
	@echo "--- Milestone 5 done."
