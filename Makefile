# Lattice Phase 1 — Story 1.3 Dev Harness
# Requires: Docker, Docker Compose, Go 1.26.1+
#
# Quick reference:
#   make up              — start everything (cold or warm)
#   make down            — tear down everything cleanly
#   make verify-bootstrap — assert primordial state; exit 0 on success
#   make build           — compile all binaries
#   make vet             — run go vet ./...

SHELL := /bin/bash
NATS_URL ?= nats://localhost:4222
BOOTSTRAP_JSON ?= ./lattice.bootstrap.json

# Load .env if it exists (ignored by git).
-include .env

.PHONY: up down verify-bootstrap build vet test test-bypass processor run-processor clean logs ps

## up — Bring up NATS + Postgres, run bootstrap binary, block until readiness gate.
up:
	@echo "==> Starting NATS + Postgres..."
	docker compose up -d --wait
	@echo "==> Containers healthy."
	@echo "==> Building bootstrap binary..."
	go build -o bin/bootstrap ./cmd/bootstrap
	@echo "==> Building refractor-stub binary..."
	go build -o bin/refractor-stub ./cmd/refractor-stub
	@echo "==> Starting refractor-stub in background..."
	NATS_URL=$(NATS_URL) ./bin/refractor-stub &
	@echo "==> Running bootstrap..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/bootstrap
	@echo "==> Lattice ready."

## down — Tear down all containers and remove the bootstrap JSON.
## Volumes are ephemeral (not named), so container removal clears NATS + Postgres data.
down:
	@echo "==> Stopping and removing containers..."
	docker compose down --remove-orphans
	@echo "==> Removing bootstrap JSON (if present)..."
	rm -f $(BOOTSTRAP_JSON)
	@echo "==> Killing any background refractor-stub processes..."
	-pkill -f "bin/refractor-stub" 2>/dev/null || true
	@echo "==> Down complete."

## verify-bootstrap — Assert all primordial keys exist with correct envelopes.
verify-bootstrap:
	@echo "==> Running bootstrap verification..."
	NATS_URL=$(NATS_URL) go run ./scripts/verify-bootstrap.go

## build — Compile all binaries under cmd/.
build:
	@echo "==> Building all binaries..."
	go build ./...
	mkdir -p bin
	go build -o bin/bootstrap ./cmd/bootstrap
	go build -o bin/refractor-stub ./cmd/refractor-stub
	go build -o bin/processor ./cmd/processor

## processor — Build the Processor binary (Story 1.5).
processor:
	@echo "==> Building processor binary..."
	mkdir -p bin
	go build -o bin/processor ./cmd/processor

## run-processor — Run the Processor against the local make-up harness.
## Requires `make up` to have completed (NATS reachable, core-operations stream live).
run-processor: processor
	@echo "==> Starting processor (Ctrl-C to stop)..."
	NATS_URL=$(NATS_URL) ./bin/processor

## test — Run all Go unit + integration tests.
test:
	@echo "==> go test ./..."
	go test ./...

## test-bypass — Run the Phase 1 Gate 2 adversarial bypass test suite.
## Requires a running Docker stack (make up). Exits 0 only when all 4 bypass
## categories are BLOCKED. Writes gate2-report.txt and the Health KV marker.
.PHONY: test-bypass
test-bypass:
	@$(MAKE) down
	@$(MAKE) up
	@$(MAKE) verify-bootstrap
	go test ./internal/bypass/... -v -count=1

## vet — Run go vet on all packages.
vet:
	@echo "==> go vet ./..."
	go vet ./...

## clean — Remove compiled binaries.
clean:
	rm -rf bin/

## logs — Show container logs.
logs:
	docker compose logs -f

## ps — Show running containers.
ps:
	docker compose ps
